//go:build unix

package exttest

import (
	"syscall"
	"testing"

	"github.com/rocksolidlabs/conductor/ext/local"
)

// A socket that answers nothing holds the port Port answered (bound and
// not listening, as a client's outbound port is): every probe is refused,
// and the helper finds the port taken. The panicking Started's check reads
// the helper's PortTaken in its directory, since no output reaches a log
// while Started runs, and starts again on a fresh port; the native Runtime
// passes.
func TestASilentSocketOnThePortIsAskedForAgain(t *testing.T) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	r := &strangerPort{Runtime: local.Native(), held: sa.(*syscall.SockaddrInet4).Port}
	RuntimeExec(t, r, helperCase(t))
	if n := r.handed.Load(); n < 2 {
		t.Errorf("Port was asked for the panicking Started's Run %d time(s): want the silent socket's port, then a fresh one", n)
	}
}
