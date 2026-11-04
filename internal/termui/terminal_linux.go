//go:build linux

package termui

import (
	"syscall"
	"time"
	"unsafe"
)

func ioctl(fd uintptr, request uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}

func terminalAttributes(fd uintptr) (syscall.Termios, error) {
	var state syscall.Termios
	err := ioctl(fd, syscall.TCGETS, unsafe.Pointer(&state))
	return state, err
}

func setTerminalAttributes(fd uintptr, state *syscall.Termios) error {
	return ioctl(fd, syscall.TCSETS, unsafe.Pointer(state))
}

func waitReadable(fd int, timeout time.Duration) (bool, error) {
	var set syscall.FdSet
	bits := (*[128]byte)(unsafe.Pointer(&set))
	bits[fd/8] |= 1 << uint(fd%8)
	value := syscall.NsecToTimeval(timeout.Nanoseconds())
	n, err := syscall.Select(fd+1, &set, nil, nil, &value)
	return n > 0, err
}
