package skytools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// systemInstallPrefix says how the dashboard may run the platform's install
// commands once the user asks it to: directly when already root, through sudo,
// or not at all.
func (m *manager) systemInstallPrefix() ([]string, bool) {
	euid := os.Geteuid
	if m.euid != nil {
		euid = m.euid
	}
	if euid() == 0 {
		return nil, true
	}
	if sudo, err := m.lookup("sudo"); err == nil {
		return []string{sudo}, true
	}
	return nil, false
}

// displayCommand is how a privileged step is shown before it runs.
func displayCommand(prefix, step []string) string {
	words := []string{}
	if len(prefix) > 0 {
		words = append(words, "sudo")
	}
	if len(step) > 0 {
		words = append(words, filepath.Base(step[0]))
		words = append(words, step[1:]...)
	}
	return strings.Join(words, " ")
}

// installSystemPackagesInTerminal is the one place skytools gives a child
// process the user's terminal: the dashboard has already left the screen, the
// user pressed a key on a screen listing these exact commands, and sudo needs
// the terminal to ask for a password. Each step runs in the foreground process
// group, so Ctrl-C reaches sudo and apt but not skytools.
func (m *manager) installSystemPackagesInTerminal(tty, out *os.File, prefix []string, steps [][]string) error {
	fmt.Fprintln(out, "skytools: installing system packages")
	for _, step := range steps {
		fmt.Fprintln(out, "$ "+displayCommand(prefix, step))
		args := append(append([]string{}, prefix...), step...)
		if err := m.runInForeground(tty, out, args); err != nil {
			err = fmt.Errorf("%s: %w", displayCommand(prefix, step), err)
			fmt.Fprintf(out, "skytools: %v\nPress Enter to return to skytools.", err)
			waitForEnter(tty)
			return err
		}
	}
	return nil
}

func (m *manager) runInForeground(tty, out *os.File, args []string) error {
	name, err := m.lookup(args[0])
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(m.ctx, name, args[1:]...)
	cmd.Env, cmd.Dir = m.commandEnvironment(), m.home
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, out, out
	// Hand the terminal over only when skytools holds it as its controlling
	// terminal's foreground group; otherwise the child shares skytools' group.
	handover := false
	if group, err := terminalForeground(tty); err == nil && group == syscall.Getpgrp() {
		handover = true
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: 0}
	}
	err = cmd.Run()
	if handover {
		err = errors.Join(err, reclaimTerminal(tty))
	}
	return err
}

func terminalForeground(tty *os.File) (int, error) {
	var group int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, tty.Fd(), uintptr(syscall.TIOCGPGRP), uintptr(unsafe.Pointer(&group))); errno != 0 {
		return 0, errno
	}
	return int(group), nil
}

// reclaimTerminal makes skytools the foreground group again. A background group
// that sets the foreground group is sent SIGTTOU, so it is ignored meanwhile.
func reclaimTerminal(tty *os.File) error {
	signal.Ignore(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	group := int32(syscall.Getpgrp())
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, tty.Fd(), uintptr(syscall.TIOCSPGRP), uintptr(unsafe.Pointer(&group))); errno != 0 {
		return fmt.Errorf("take back the terminal: %w", errno)
	}
	return nil
}

func waitForEnter(tty *os.File) {
	buffer := make([]byte, 1)
	for {
		n, err := tty.Read(buffer)
		if err != nil || n == 1 && (buffer[0] == '\n' || buffer[0] == '\r') {
			return
		}
	}
}
