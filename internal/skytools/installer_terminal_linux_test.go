//go:build linux

package skytools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

const installerTerminalHelperEnv = "SKYTOOLS_INSTALLER_TERMINAL_HELPER"

// The helper runs in a session whose controlling terminal is a real PTY, the
// situation of a person running skytools. Without session isolation the fixture
// prompt below is stopped by SIGTTIN and never exits.
func TestInstallerCannotPromptOnControllingTerminal(t *testing.T) {
	if os.Getenv(installerTerminalHelperEnv) == "1" {
		installerTerminalHelper()
		return
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no PTY available: %v", err)
	}
	defer master.Close()
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
	defer slave.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInstallerCannotPromptOnControllingTerminal$")
	cmd.Env = append(os.Environ(), installerTerminalHelperEnv+"=1")
	cmd.Stdin = slave
	var output strings.Builder
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper failed: %v\n%s", err, output.String())
	}
	for _, want := range []string{"helper has terminal", "installer answered no"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in helper output:\n%s", want, output.String())
		}
	}
}

func installerTerminalHelper() {
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err != nil {
		fmt.Println("helper has no controlling terminal:", err)
		os.Exit(1)
	} else {
		tty.Close()
	}
	fmt.Println("helper has terminal")
	var output strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := &manager{ctx: ctx, home: os.TempDir(), out: &output, errOut: &output, env: os.Environ()}
	// Same decision as prompt_yes_no in the Codex installer.
	script := `if ( : </dev/tty ) 2>/dev/null; then IFS= read -r answer </dev/tty; echo "installer read $answer"; else echo "installer answered no"; fi`
	if err := m.run("/bin/sh", "-c", script); err != nil {
		fmt.Println("installer stalled or failed:", err, output.String())
		os.Exit(1)
	}
	fmt.Print(output.String())
}

func TestManagerMakesCodexInstallerNoninteractive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m, err := newManager(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range m.env {
		found = found || entry == "CODEX_NON_INTERACTIVE=1"
	}
	if !found {
		t.Fatal("Codex installer environment can still prompt")
	}
}
