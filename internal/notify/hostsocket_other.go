//go:build !unix

package notify

import "errors"

var errNoHostSocket = errors.New("the host's socket is a unix platform's alone")

// CheckHostSocketDir refuses every folder where there are no unix sockets
// to check: a report goes to the server.
func CheckHostSocketDir(string) error { return errNoHostSocket }

func checkOwnSocket(string) error { return errNoHostSocket }
