//go:build linux || darwin

package exttest

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

// terminalSize is the size of the terminal on the helper's standard input.
func terminalSize() (rows, cols int, err error) {
	var ws struct{ Row, Col, X, Y uint16 }
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, 0, uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws))); e != 0 {
		return 0, 0, e
	}
	return int(ws.Row), int(ws.Col), nil
}

// notifyResize tells c at each change of the terminal's size.
func notifyResize(c chan<- os.Signal) { signal.Notify(c, syscall.SIGWINCH) }
