//go:build linux

package termui

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	var unlock int32
	if err := ioctl(master.Fd(), 0x40045431, unsafe.Pointer(&unlock)); err != nil {
		t.Fatalf("unlock PTY: %v", err)
	}
	var number uint32
	if err := ioctl(master.Fd(), 0x80045430, unsafe.Pointer(&number)); err != nil {
		t.Fatalf("PTY number: %v", err)
	}
	slave, err := os.OpenFile(fmtPTYName(number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	var size = struct{ Rows, Cols, X, Y uint16 }{Rows: 10, Cols: 40}
	if err := ioctl(slave.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		t.Fatal(err)
	}
	return master, slave
}

func fmtPTYName(n uint32) string {
	if n == 0 {
		return "/dev/pts/0"
	}
	var digits [10]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = '0' + byte(n%10)
		n /= 10
	}
	return "/dev/pts/" + string(digits[i:])
}

func TestTerminalRawPollingResizeAndRestore(t *testing.T) {
	master, slave := openPTY(t)
	before, err := terminalAttributes(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	term, err := Open(slave, slave)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { term.Close() })
	raw, err := terminalAttributes(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	if raw.Lflag&(syscall.ECHO|syscall.ICANON|syscall.ISIG) != 0 {
		t.Fatalf("raw mode flags not cleared: %x", raw.Lflag)
	}
	if _, err := master.Write([]byte("\x1b[")); err != nil {
		t.Fatal(err)
	}
	if event, err := term.Poll(context.Background(), 5*time.Millisecond); err != nil || event.Key != KeyNone {
		t.Fatalf("fragment poll = %+v, %v", event, err)
	}
	if _, err := master.Write([]byte("B")); err != nil {
		t.Fatal(err)
	}
	if event, err := term.Poll(context.Background(), 100*time.Millisecond); err != nil || event.Key != KeyDown {
		t.Fatalf("arrow poll = %+v, %v", event, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := term.Poll(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled poll error = %v", err)
	}
	deadlineCtx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	started := time.Now()
	if _, err := term.Poll(deadlineCtx, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting poll cancellation = %v", err)
	}
	if time.Since(started) > 200*time.Millisecond {
		t.Fatal("waiting input prevented prompt cancellation")
	}
	var size = struct{ Rows, Cols, X, Y uint16 }{Rows: 5, Cols: 20}
	if err := ioctl(slave.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		t.Fatal(err)
	}
	if event, err := term.Poll(context.Background(), time.Millisecond); err != nil || event.Key != KeyResize {
		t.Fatalf("resize poll = %+v, %v", event, err)
	}
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := terminalAttributes(slave.Fd())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal not restored: before=%+v after=%+v error=%v", before, after, err)
	}
	if err := term.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	var output string
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(output, "\x1b[?25h\x1b[?1049l") && time.Now().Before(deadline) {
		ready, err := waitReadable(int(master.Fd()), 50*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			data := make([]byte, 4096)
			n, err := master.Read(data)
			if err != nil {
				t.Fatal(err)
			}
			output += string(data[:n])
		}
	}
	if !strings.Contains(output, "\x1b[?1049h\x1b[?25l") || !strings.Contains(output, "\x1b[?25h\x1b[?1049l") {
		t.Fatalf("missing screen/cursor setup or restoration: %q", output)
	}
}

func TestRendererDiffAndClearTail(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	term := &Terminal{out: write}
	var ui Context
	ui.BeginFrame(10, 2, Event{})
	ui.Label(0, 0, "long", StyleNormal)
	frame := ui.EndFrame()
	if err := term.Present(frame); err != nil {
		t.Fatal(err)
	}
	if err := term.Present(frame); err != nil {
		t.Fatal(err)
	}
	ui.BeginFrame(10, 2, Event{})
	ui.Label(0, 0, "x", StyleNormal)
	if err := term.Present(ui.EndFrame()); err != nil {
		t.Fatal(err)
	}
	write.Close()
	data, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	if strings.Count(output, "long") != 1 || strings.Count(output, "\x1b[2;1H") != 1 {
		t.Fatalf("unchanged frame/row was redrawn: %q", output)
	}
	if !strings.Contains(output, "\x1b[1;1H\x1b[0mx\x1b[0m\x1b[K") {
		t.Fatalf("shorter changed row did not clear old tail: %q", output)
	}
}

func TestOpenRejectsPipesWithoutChangingInput(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if IsTerminal(read) || IsTerminal(nil) {
		t.Fatal("pipe or nil reported as terminal")
	}
	if _, err := Open(read, write); err == nil {
		t.Fatal("Open accepted pipes")
	}
	_, slave := openPTY(t)
	before, _ := terminalAttributes(slave.Fd())
	if _, err := Open(slave, write); err == nil {
		t.Fatal("Open accepted pipe output")
	}
	after, _ := terminalAttributes(slave.Fd())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed Open changed input attributes")
	}
}

func TestSetupOutputFailureRestoresRawMode(t *testing.T) {
	_, slave := openPTY(t)
	before, err := terminalAttributes(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	readOnlyOutput, err := os.Open(slave.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer readOnlyOutput.Close()
	if _, err := Open(slave, readOnlyOutput); err == nil {
		t.Fatal("Open accepted unwritable terminal output")
	}
	after, err := terminalAttributes(slave.Fd())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed screen setup left raw mode active: %v", err)
	}
}
