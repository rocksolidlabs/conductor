//go:build !linux && !darwin

package exttest

import (
	"errors"
	"os"
)

// terminalSize has no terminal to read on this platform.
func terminalSize() (rows, cols int, err error) {
	return 0, 0, errors.New("exttest helper: no terminal size on this platform")
}

// notifyResize tells nothing: no terminal here tells its size changing.
func notifyResize(chan<- os.Signal) {}
