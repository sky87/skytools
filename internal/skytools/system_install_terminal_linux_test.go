//go:build linux

package skytools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

const systemInstallHelperEnv = "SKYTOOLS_SYSTEM_INSTALL_HELPER"

// The helper process owns a real controlling terminal, as skytools does when a
// person runs it. sudo must be able to read a password from that terminal,
// Ctrl-C must reach sudo and not skytools, and skytools must get the terminal
// back afterwards.
func TestSystemInstallHandsTheTerminalToSudoAndTakesItBack(t *testing.T) {
	if os.Getenv(systemInstallHelperEnv) != "" {
		systemInstallHelper(os.Getenv(systemInstallHelperEnv))
		return
	}
	master, slave := openTestPTY(t)
	dir := t.TempDir()
	sudo := filepath.Join(dir, "sudo")
	script := `#!/bin/sh
printf 'Password: '
IFS= read -r password
echo "sudo got $password for $*"
if [ "$2" = install ]; then echo sleeping; exec sleep 30; fi
`
	if err := os.WriteFile(sudo, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSystemInstallHandsTheTerminalToSudoAndTakesItBack$")
	cmd.Env = append(os.Environ(), systemInstallHelperEnv+"="+sudo)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	slave.Close()
	output := &lockedBuffer{}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			output.write(buffer[:n])
			if err != nil {
				return
			}
		}
	}()
	waitForCount := func(text string, count int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for strings.Count(output.String(), text) < count {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q:\n%s", text, output.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitForCount("Password: ", 1)
	master.Write([]byte("first\r"))
	waitForCount("Password: ", 2)
	master.Write([]byte("second\r"))
	waitForCount("sleeping", 1)
	master.Write([]byte{3}) // Ctrl-C
	waitForCount("Press Enter to return to skytools.", 1)
	master.Write([]byte("\r"))
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper failed: %v\n%s", err, output.String())
	}
	for _, want := range []string{
		"$ sudo apt-get update", "sudo got first for /usr/bin/apt-get update",
		"$ sudo apt-get install --no-install-recommends curl", "sudo got second for /usr/bin/apt-get install --no-install-recommends curl",
		"signal: interrupt", "helper got no SIGINT", "helper is the foreground group again",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, output.String())
		}
	}
}

func systemInstallHelper(sudo string) {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Println("helper has no controlling terminal:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m := &manager{ctx: ctx, home: os.TempDir(), env: os.Environ()}
	steps := [][]string{{"/usr/bin/apt-get", "update"}, {"/usr/bin/apt-get", "install", "--no-install-recommends", "curl"}}
	err = m.installSystemPackagesInTerminal(tty, tty, []string{sudo}, steps)
	fmt.Fprintln(tty)
	if err == nil {
		fmt.Fprintln(tty, "helper: interrupted install reported success")
		os.Exit(1)
	}
	select {
	case <-interrupts:
		fmt.Fprintln(tty, "helper got SIGINT")
		os.Exit(1)
	case <-time.After(200 * time.Millisecond):
		fmt.Fprintln(tty, "helper got no SIGINT")
	}
	if group, err := terminalForeground(tty); err != nil || group != syscall.Getpgrp() {
		fmt.Fprintln(tty, "helper is not the foreground group:", group, err)
		os.Exit(1)
	}
	fmt.Fprintln(tty, "helper is the foreground group again")
}

// Pressing i on a blocked review leaves the dashboard, runs the shown commands,
// comes back and checks again, so the plan can run.
func TestDashboardTerminalInstallsMissingSystemPackagesWithSudo(t *testing.T) {
	m, requests := dashboardTerminalManager(t, "#!/bin/sh\nexit 99\n")
	bin := ""
	for _, entry := range m.env {
		if strings.HasPrefix(entry, "PATH=") {
			bin = strings.TrimPrefix(entry, "PATH=")
		}
	}
	marker := m.at("curl-installed")
	sudo := "#!/bin/sh\necho \"fake sudo $*\"\nif [ \"$2\" = install ]; then : > '" + marker + "'; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(sudo), 0755); err != nil {
		t.Fatal(err)
	}
	m.systemProvider = packageProviderFunc(func(context.Context, []SystemDependency) (SystemDependencyReport, error) {
		if exists(marker) {
			return SystemDependencyReport{Platform: "fixture"}, nil
		}
		return SystemDependencyReport{
			Platform: "fixture", InstallCommand: "sudo apt-get install --no-install-recommends curl",
			Missing:      []MissingSystemDependency{{"http-client", []string{"curl"}, []string{"uv"}}},
			InstallSteps: [][]string{{"/usr/bin/apt-get", "update"}, {"/usr/bin/apt-get", "install", "--no-install-recommends", "curl"}},
		}, nil
	})
	pty := startDashboardTerminal(t, m)
	pty.waitFor(0, "skytools pty-test", 3*time.Second)
	pty.send("\r")
	pty.waitFor(0, "i install with sudo", 3*time.Second)
	checkpoint := pty.output.Len()
	pty.send("i")
	pty.waitFor(checkpoint, "fake sudo /usr/bin/apt-get install --no-install-recommends curl", 3*time.Second)
	pty.waitFor(checkpoint, "all present", 3*time.Second)
	receipt := pty.quitAndCheckRestore()
	for _, want := range []string{"$ sudo apt-get update", "fake sudo /usr/bin/apt-get update"} {
		if !strings.Contains(pty.output.String(), want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(receipt, "sudo apt-get install") {
		t.Fatalf("receipt still asks for the installed package: %q", receipt)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("installer requests = %d", got)
	}
}

func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no PTY available: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	var unlock int32
	if err := dashboardTerminalIOCTL(master.Fd(), 0x40045431, unsafe.Pointer(&unlock)); err != nil {
		t.Fatal(err)
	}
	var number uint32
	if err := dashboardTerminalIOCTL(master.Fd(), 0x80045430, unsafe.Pointer(&number)); err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	return master, slave
}

type lockedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *lockedBuffer) write(p []byte) {
	b.mu.Lock()
	b.data = append(b.data, p...)
	b.mu.Unlock()
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
