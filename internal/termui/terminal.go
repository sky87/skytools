//go:build linux || darwin

package termui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const escapeDelay = 35 * time.Millisecond

// Terminal owns one raw-mode session. Poll and Present are called by the UI
// goroutine; no input goroutine is started and cancellation cannot strand a read.
// The caller must defer Close immediately after a successful Open.
type Terminal struct {
	in, out       *os.File
	original      syscall.Termios
	width, height int
	decoder       keyDecoder
	escapeSince   time.Time
	last          Frame
	presented     bool
	closed        bool
}

// IsTerminal reports whether file supports terminal attributes.
func IsTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	_, err := terminalAttributes(file.Fd())
	return err == nil
}

// Open enters raw input mode and an alternate screen, hiding the cursor. Input
// and output must both be terminals. No subprocess or external terminal utility
// is used. If setup fails, original terminal attributes are restored.
func Open(in, out *os.File) (*Terminal, error) {
	if in == nil || out == nil {
		return nil, errors.New("terminal input and output are required")
	}
	original, err := terminalAttributes(in.Fd())
	if err != nil {
		return nil, fmt.Errorf("read terminal attributes: %w", err)
	}
	if !IsTerminal(out) {
		return nil, errors.New("output is not a terminal")
	}
	if in.Fd() >= 1024 {
		return nil, errors.New("terminal descriptor exceeds select capacity")
	}
	raw := original
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 0
	raw.Cc[syscall.VTIME] = 0
	if err := setTerminalAttributes(in.Fd(), &raw); err != nil {
		return nil, fmt.Errorf("enter terminal raw mode: %w", err)
	}
	t := &Terminal{in: in, out: out, original: original}
	t.width, t.height = t.Size()
	if err := writeAll(out, "\x1b[?1049h\x1b[?25l\x1b[0m\x1b[2J\x1b[H"); err != nil {
		return nil, errors.Join(err, t.Close())
	}
	return t, nil
}

// Close restores terminal attributes, cursor visibility and the original screen.
// It is idempotent and attempts all restoration steps even when output fails.
func (t *Terminal) Close() error {
	if t == nil || t.closed {
		return nil
	}
	t.closed = true
	writeErr := writeAll(t.out, "\x1b[0m\x1b[?25h\x1b[?1049l")
	restoreErr := setTerminalAttributes(t.in.Fd(), &t.original)
	return errors.Join(writeErr, restoreErr)
}

// Size reads the current terminal dimensions. It falls back to a conservative
// 80x24 if a terminal does not report a usable window size.
func (t *Terminal) Size() (width, height int) {
	for _, file := range []*os.File{t.out, t.in} {
		var size struct{ Rows, Cols, X, Y uint16 }
		if file != nil && ioctl(file.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size)) == nil && size.Cols > 0 && size.Rows > 0 {
			return min(int(size.Cols), 4096), min(int(size.Rows), 1024)
		}
	}
	return 80, 24
}

// Poll returns one key, a resize event, or KeyNone after timeout. Context
// cancellation is observed within 50ms without creating any background goroutine.
// A lone Escape waits briefly so fragmented arrow and UTF-8 reads stay intact.
func (t *Terminal) Poll(ctx context.Context, timeout time.Duration) (Event, error) {
	if t.closed {
		return Event{}, os.ErrClosed
	}
	deadline := time.Now().Add(max(timeout, 0))
	for {
		if err := ctx.Err(); err != nil {
			return Event{}, err
		}
		width, height := t.Size()
		if width != t.width || height != t.height {
			t.width, t.height = width, height
			return Event{Key: KeyResize}, nil
		}
		now := time.Now()
		flush := !t.escapeSince.IsZero() && now.Sub(t.escapeSince) >= escapeDelay
		if event, ok := t.decoder.next(flush); ok {
			t.trackEscape(now)
			return event, nil
		}
		t.trackEscape(now)
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return Event{}, nil
		}
		wait := min(remaining, 50*time.Millisecond)
		if !t.escapeSince.IsZero() {
			wait = min(wait, max(0, escapeDelay-time.Since(t.escapeSince)))
		}
		ready, err := waitReadable(int(t.in.Fd()), wait)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return Event{}, fmt.Errorf("wait for terminal input: %w", err)
		}
		if ready {
			var data [256]byte
			n, err := syscall.Read(int(t.in.Fd()), data[:])
			if errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) {
				continue
			}
			if err != nil {
				return Event{}, fmt.Errorf("read terminal input: %w", err)
			}
			if n == 0 {
				return Event{}, io.EOF
			}
			t.decoder.feed(data[:n])
		}
	}
}

func (t *Terminal) trackEscape(now time.Time) {
	if len(t.decoder.pending) > 0 && t.decoder.pending[0] == 0x1b {
		if t.escapeSince.IsZero() {
			t.escapeSince = now
		}
	} else {
		t.escapeSince = time.Time{}
	}
}

// Present writes only changed rows. Each changed row starts at an absolute
// cursor position and clears its old tail; frame text never uses the final
// physical column, preventing automatic wrapping and bottom-row scrolling.
func (t *Terminal) Present(frame Frame) error {
	if t.closed {
		return os.ErrClosed
	}
	width, height := t.Size()
	if frame.Width > width || frame.Height > height {
		// A resize can happen after the application's last Size call. Wait
		// for its next correctly sized frame rather than writing past an edge.
		t.presented = false
		return nil
	}
	var b strings.Builder
	full := !t.presented || t.last.Width != frame.Width || t.last.Height != frame.Height
	if full {
		b.WriteString("\x1b[0m\x1b[2J")
	}
	for y, row := range frame.rows {
		if !full && y < len(t.last.rows) && row == t.last.rows[y] {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[0m", y+1)
		b.WriteString(row)
		b.WriteString("\x1b[0m\x1b[K")
	}
	if err := writeAll(t.out, b.String()); err != nil {
		return err
	}
	t.last = frame
	t.presented = true
	return nil
}

func writeAll(out io.Writer, text string) error {
	for len(text) > 0 {
		n, err := io.WriteString(out, text)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		text = text[n:]
	}
	return nil
}
