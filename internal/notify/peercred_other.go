//go:build !linux && !darwin

package notify

import (
	"errors"
	"net"
)

// socketPeerUID reads no peer where the platform's is not known here: the
// socket is then not used, and a report goes to the server.
func socketPeerUID(net.Conn) (int, error) {
	return 0, errors.New("the socket's peer cannot be read on this platform")
}
