//go:build linux || darwin

package proc

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// writersGone says whether no process holds the write end of the output
// pipe whose read end is r any more: the kernel's hang-up on the read end
// (POLLHUP), which it gives once the last writer has closed the pipe or
// exited, whenever the goroutine that reads the output gets to its end
// (launched.eof), and whatever is still buffered. A process that
// redirected its output holds none of it. known is false where it cannot
// be told (the file closed, a poll that fails): the caller reads the
// output's end then.
func writersGone(r *os.File) (gone, known bool) {
	if r == nil {
		return false, false
	}
	rc, err := r.SyscallConn()
	if err != nil {
		return false, false
	}
	var revents int16
	var perr error
	// Control, not Fd: Fd would put the file in blocking mode under the
	// goroutine reading it. A poll with no timeout reads the state alone.
	if err := rc.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			_, perr = unix.Poll(fds, 0)
			if !errors.Is(perr, unix.EINTR) {
				break
			}
		}
		revents = fds[0].Revents
	}); err != nil || perr != nil {
		return false, false
	}
	return revents&unix.POLLHUP != 0, true
}
