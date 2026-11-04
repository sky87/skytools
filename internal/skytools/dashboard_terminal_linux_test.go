//go:build linux

package skytools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/sky87/skytools/internal/termui"
)

// These tests drive the real dashboard, installer subprocess and inventory
// rescan through a Linux PTY. The HTTP transport serves only a local fixture;
// neither the installed fixture executable nor any upstream binary is run.
func TestDashboardTerminalInstallAndRescan(t *testing.T) {
	m, requests := dashboardTerminalManager(t, `#!/bin/sh
set -eu
printf 'uv fixture download progress\n'
while [ ! -e "$HOME/uv-fixture-continue" ]; do sleep 0.01; done
mkdir -p "$HOME/.local/bin"
cat > "$HOME/.local/bin/uv" <<'BINARY'
#!/bin/sh
printf 'unexpected version probe\n' > "$HOME/uv-was-executed"
exit 91
BINARY
chmod +x "$HOME/.local/bin/uv"
printf 'uv fixture installation finished\n'
`)
	pty := startDashboardTerminal(t, m)
	pty.waitFor(0, "developer packages", 3*time.Second)

	// Exercise the small-screen branch, then restore a screen large enough for
	// the complete list and live installer output before navigating controls.
	checkpoint := pty.output.Len()
	pty.resize(32, 8)
	pty.waitFor(checkpoint, "Enlarge terminal", 3*time.Second)
	checkpoint = pty.output.Len()
	pty.resize(120, 32)
	pty.waitFor(checkpoint, "Install missing", 3*time.Second)

	pty.selectUVAndRun()
	pty.waitFor(0, "uv fixture download progress", 3*time.Second)
	if err := os.WriteFile(m.at("uv-fixture-continue"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	pty.waitFor(0, "Operation completed.", 3*time.Second)
	pty.waitFor(0, "uv         Installed", 3*time.Second)
	if !executable(m.at(".local/bin/uv")) {
		t.Fatal("completed dashboard operation did not create executable uv")
	}
	if _, err := os.Stat(m.at("uv-was-executed")); !os.IsNotExist(err) {
		t.Fatalf("inventory rescan executed the installed uv fixture: %v", err)
	}
	pty.quitAndCheckRestore()
	if got := requests.Load(); got != 1 {
		t.Fatalf("installer HTTP requests = %d, want exactly one fixture request", got)
	}
}

func TestDashboardTerminalCancelInstallerAndContinue(t *testing.T) {
	m, requests := dashboardTerminalManager(t, `#!/bin/sh
set -eu
printf '%s\n' "$$" > "$HOME/uv-installer.pid"
printf 'uv fixture waiting for cancellation\n'
sleep 30
mkdir -p "$HOME/.local/bin"
printf '#!/bin/sh\nexit 0\n' > "$HOME/.local/bin/uv"
chmod +x "$HOME/.local/bin/uv"
`)
	pty := startDashboardTerminal(t, m)
	pty.waitFor(0, "developer packages", 3*time.Second)
	pty.selectUVAndRun()
	pty.waitFor(0, "uv fixture waiting for cancellation", 3*time.Second)
	checkpoint := pty.output.Len()
	started := time.Now()
	pty.send("\x03")
	pty.waitFor(checkpoint, "Operation cancelled.", 2*time.Second)
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("installer cancellation took %s; process group was not stopped promptly", elapsed)
	}
	if _, err := os.Stat(m.at(".local/bin/uv")); !os.IsNotExist(err) {
		t.Fatalf("cancelled installer left uv installed: %v", err)
	}
	if state := m.scan(m.tools())[0]; state.Name != "uv" || state.Installed || state.Partial {
		t.Fatalf("cancelled uv inventory = %+v, want missing", state)
	}
	// Cancellation leaves the dashboard alive and keyboard-driven: Refresh is
	// handled by the normal UI before q exits and restores the original screen.
	checkpoint = pty.output.Len()
	pty.send("r")
	pty.waitFor(checkpoint, "Local package inventory refreshed.", 3*time.Second)
	pty.quitAndCheckRestore()
	if got := requests.Load(); got != 1 {
		t.Fatalf("installer HTTP requests = %d, want exactly one fixture request", got)
	}
}

func TestDashboardTerminalLoadsVersionsAfterFirstFrameAndQuitsWhileChecking(t *testing.T) {
	m, _ := dashboardTerminalManager(t, "#!/bin/sh\nexit 99\n")
	if err := atomicWrite(m.at(".go/current/bin/go"), []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(m.at(".go/current/VERSION"), []byte("go1.25.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	probes := dashboardVersionProbes{
		installed: func(context.Context, PackageStatus) (string, error) { return "", fmt.Errorf("no probe expected") },
		latest: func(ctx context.Context, name string) (ReleaseInfo, error) {
			if name == "go" {
				select {
				case <-gate:
					return ReleaseInfo{Version: "1.26.2", Channel: "stable"}, nil
				case <-ctx.Done():
					return ReleaseInfo{}, ctx.Err()
				}
			}
			return ReleaseInfo{}, fmt.Errorf("offline fixture")
		},
	}
	pty := startDashboardTerminalWithProbes(t, m, probes)
	pty.waitFor(0, "go1.25.0", time.Second)
	pty.waitFor(0, "checking", time.Second)
	pty.send("r")
	pty.waitFor(0, "Local package inventory refreshed.", time.Second)
	close(gate)
	pty.waitFor(0, "1.26.2", time.Second)
	pty.waitFor(0, "Update available", time.Second)
	pty.quitAndCheckRestore()
}

func TestDashboardTerminalQuitCancelsPendingVersionChecks(t *testing.T) {
	m, _ := dashboardTerminalManager(t, "#!/bin/sh\nexit 99\n")
	probes := dashboardVersionProbes{installed: func(context.Context, PackageStatus) (string, error) { return "1.0.0", nil }, latest: func(ctx context.Context, name string) (ReleaseInfo, error) {
		<-ctx.Done()
		return ReleaseInfo{}, ctx.Err()
	}}
	pty := startDashboardTerminalWithProbes(t, m, probes)
	pty.waitFor(0, "developer packages", time.Second)
	pty.quitAndCheckRestore()
}

func dashboardTerminalManager(t *testing.T, installer string) (*manager, *atomic.Int32) {
	t.Helper()
	m := testManager(t)
	// Limit PATH to the native shell utilities needed by the fixture. This
	// makes all package rows initially missing even on a developer-rich image.
	bin := t.TempDir()
	for _, name := range []string{"sh", "mkdir", "chmod", "sleep", "cat"} {
		if err := os.Symlink(filepath.Join("/bin", name), filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	m.env = setEnv(m.env, "PATH", bin)
	requests := new(atomic.Int32)
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if req.Method != http.MethodGet || req.URL.String() != "https://astral.sh/uv/install.sh" {
			t.Errorf("unexpected network request: %s %s", req.Method, req.URL)
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("unexpected fixture request")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(installer)), Header: make(http.Header)}, nil
	})
	return m, requests
}

type dashboardTerminalPTY struct {
	t        *testing.T
	master   *os.File
	slave    *os.File
	fd       int
	original syscall.Termios
	output   strings.Builder
	done     chan error
	cancel   context.CancelFunc
	returned bool
	result   error
}

func startDashboardTerminal(t *testing.T, m *manager) *dashboardTerminalPTY {
	return startDashboardTerminalWithProbes(t, m, dashboardVersionProbes{
		installed: func(context.Context, PackageStatus) (string, error) { return "0.0.1", nil },
		latest: func(context.Context, string) (ReleaseInfo, error) {
			return ReleaseInfo{}, fmt.Errorf("fixture offline")
		},
	})
}

func startDashboardTerminalWithProbes(t *testing.T, m *manager, probes dashboardVersionProbes) *dashboardTerminalPTY {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open Linux PTY: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	var unlock int32
	if err := dashboardTerminalIOCTL(master.Fd(), 0x40045431, unsafe.Pointer(&unlock)); err != nil {
		t.Fatalf("unlock Linux PTY: %v", err)
	}
	var number uint32
	if err := dashboardTerminalIOCTL(master.Fd(), 0x80045430, unsafe.Pointer(&number)); err != nil {
		t.Fatalf("read Linux PTY number: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	p := &dashboardTerminalPTY{t: t, master: master, slave: slave, fd: int(master.Fd()), done: make(chan error, 1)}
	if p.fd >= 1024 {
		t.Fatal("PTY descriptor exceeds select capacity")
	}
	if err := syscall.SetNonblock(p.fd, true); err != nil {
		t.Fatal(err)
	}
	if err := dashboardTerminalIOCTL(slave.Fd(), syscall.TCGETS, unsafe.Pointer(&p.original)); err != nil {
		t.Fatal(err)
	}
	p.resize(120, 32)
	m.ctx, p.cancel = context.WithTimeout(context.Background(), 10*time.Second)
	go func() { p.done <- m.dashboardWithVersions("pty-test", slave, slave, probes) }()
	t.Cleanup(func() {
		p.cancel()
		// A failing test must not strand the fixture's sleeping child, even if
		// the regression being tested broke installer cancellation itself.
		if data, err := os.ReadFile(m.at("uv-installer.pid")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 1 {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
		if !p.returned {
			select {
			case p.result = <-p.done:
				p.returned = true
			case <-time.After(4 * time.Second):
				t.Error("dashboard goroutine did not stop after test context cancellation")
			}
		}
	})
	return p
}

func (p *dashboardTerminalPTY) resize(width, height uint16) {
	p.t.Helper()
	size := struct{ Rows, Cols, X, Y uint16 }{Rows: height, Cols: width}
	if err := dashboardTerminalIOCTL(p.slave.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		p.t.Fatal(err)
	}
}

func (p *dashboardTerminalPTY) send(keys string) {
	p.t.Helper()
	for len(keys) > 0 {
		n, err := syscall.Write(p.fd, []byte(keys))
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			p.t.Fatalf("write dashboard keys: %v", err)
		}
		if n == 0 {
			p.t.Fatal("short write sending dashboard keys")
		}
		keys = keys[n:]
	}
}

func (p *dashboardTerminalPTY) selectUVAndRun() {
	p.t.Helper()
	// Initial order is Install, Update, Refresh, then uv. Selecting uv adds
	// Run after all package rows; four Up keys wrap from uv back onto Run.
	checkpoint := p.output.Len()
	p.send(strings.Repeat("\x1b[B", 3) + " ")
	p.waitFor(checkpoint, "Selected: uv", 3*time.Second)
	p.send(strings.Repeat("\x1b[A", 4) + "\r")
}

func (p *dashboardTerminalPTY) pump(timeout time.Duration) {
	p.t.Helper()
	var set syscall.FdSet
	bits := (*[128]byte)(unsafe.Pointer(&set))
	bits[p.fd/8] |= 1 << uint(p.fd%8)
	value := syscall.NsecToTimeval(timeout.Nanoseconds())
	n, err := syscall.Select(p.fd+1, &set, nil, nil, &value)
	if errors.Is(err, syscall.EINTR) {
		return
	}
	if err != nil {
		p.t.Fatalf("wait for dashboard output: %v", err)
	}
	if n == 0 {
		return
	}
	var data [8192]byte
	for {
		n, err := syscall.Read(p.fd, data[:])
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.EAGAIN) {
			break
		}
		if err != nil {
			p.t.Fatalf("read dashboard output: %v", err)
		}
		if n == 0 {
			break
		}
		p.output.Write(data[:n])
		if p.output.Len() > 2*1024*1024 {
			p.t.Fatal("dashboard output exceeded test capture limit")
		}
	}
}

func (p *dashboardTerminalPTY) waitFor(from int, text string, timeout time.Duration) {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(termui.Sanitize(p.output.String()[from:]), text) {
			return
		}
		select {
		case p.result = <-p.done:
			p.returned = true
			p.pump(0)
			p.t.Fatalf("dashboard exited before %q: %v\noutput: %s", text, p.result, termui.Sanitize(p.output.String()))
		default:
		}
		p.pump(10 * time.Millisecond)
	}
	p.t.Fatalf("timed out waiting for %q\noutput: %s", text, termui.Sanitize(p.output.String()))
}

func (p *dashboardTerminalPTY) quitAndCheckRestore() {
	p.t.Helper()
	p.send("q")
	deadline := time.Now().Add(2 * time.Second)
	for !p.returned && time.Now().Before(deadline) {
		select {
		case p.result = <-p.done:
			p.returned = true
		default:
			p.pump(10 * time.Millisecond)
		}
	}
	if !p.returned {
		p.t.Fatal("q did not exit the dashboard")
	}
	if p.result != nil {
		p.t.Fatalf("dashboard exit error: %v", p.result)
	}
	p.pump(10 * time.Millisecond)
	var restored syscall.Termios
	if err := dashboardTerminalIOCTL(p.slave.Fd(), syscall.TCGETS, unsafe.Pointer(&restored)); err != nil {
		p.t.Fatal(err)
	}
	if !reflect.DeepEqual(p.original, restored) {
		p.t.Fatalf("dashboard did not exactly restore terminal attributes\nbefore: %+v\nafter: %+v", p.original, restored)
	}
	output := p.output.String()
	if strings.Count(output, "\x1b[?1049h") != 1 || strings.Count(output, "\x1b[?25l") != 1 || strings.Count(output, "\x1b[?25h") != 1 || strings.Count(output, "\x1b[?1049l") != 1 {
		p.t.Fatalf("dashboard did not pair alternate-screen/cursor setup and restoration: %q", output)
	}
	if !strings.HasSuffix(output, "\x1b[0m\x1b[?25h\x1b[?1049l") {
		p.t.Fatalf("dashboard exit output did not restore cursor and original screen: %q", output)
	}
}

func dashboardTerminalIOCTL(fd, request uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}
