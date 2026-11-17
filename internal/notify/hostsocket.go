package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// EnvHostSocket names the unix-domain socket a session's host listens on
// for its agent's attention reports (`conductor host` sets it on the
// process it starts, beside EnvURL). A server's own sessions never have it:
// pty.BuildEnv keeps every CONDUCTOR_* variable of the server's own
// environment and of the catalog out of them, and the server injects none.
const EnvHostSocket = "CONDUCTOR_HOST_SOCKET"

// MaxHostSocketPath bounds the socket's path: a unix socket's address
// holds 104 bytes on macOS and 108 on Linux, its terminating NUL among
// them, and 100 fits both.
const MaxHostSocketPath = 100

// HostReportPath is the path, on the host's socket, of the attention
// route's request: the same body as POST /api/sessions/{id}/attention,
// with the session's agent token. The socket serves no other.
const HostReportPath = "/attention"

// HostEventsPath is the path, on the host's socket, of the events route's
// request (POST /api/sessions/{id}/events): an event, or an attention word.
// Only a host that names EnvHostEvents serves it.
const HostEventsPath = "/events"

// EnvHostEvents, set to "1" beside EnvHostSocket, says the host's socket
// takes the agent's events too (HostEventsPath): the sender then posts
// them there first. A host that sets EnvHostSocket alone (an earlier
// release) takes attention reports alone, and the sender posts its events
// to the server's events route.
const EnvHostEvents = "CONDUCTOR_HOST_EVENTS"

// errHostUnreachable says the host's socket could not be reached: it is
// missing, it is not this user's own in a folder of this user's own that
// only this user can enter (CheckHostSocket), no connection to it was
// made, or the process that listens on it is not this user's (its peer,
// read before anything is written). Nothing reached the host, so the report may go to the server
// instead. A host's answer, whatever it says, is never this.
var errHostUnreachable = errors.New("notify: the host's socket cannot be reached")

// Target is where a report goes: the server's attention route (URL,
// Token), as FromEnv reads it, and, when the session's host listens for
// its agent's reports, the host's socket (EnvHostSocket).
type Target struct {
	URL, Token string
	// Socket is the host's socket, "" for none (a server's session, a host
	// of an earlier release).
	Socket string
	// Events says the socket takes the agent's events too (EnvHostEvents).
	Events bool
}

// TargetFromEnv is FromEnv with the host's socket, when the environment
// names one.
func TargetFromEnv(getenv func(string) string) (Target, error) {
	url, token, err := FromEnv(getenv)
	if err != nil {
		return Target{}, err
	}
	return Target{URL: url, Token: token, Socket: getenv(EnvHostSocket), Events: getenv(EnvHostEvents) == "1"}, nil
}

// CheckHostSocket says whether path may be handed a token: an absolute,
// clean path of at most MaxHostSocketPath bytes, naming a socket of this
// user's own in a folder of this user's own, not a link, that only this
// user can enter (mode 0700). Anything else is refused, and a report then
// goes to the server as if there were no socket.
func CheckHostSocket(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("the host's socket is not named by a clean absolute path")
	}
	if len(path) > MaxHostSocketPath {
		return fmt.Errorf("the host's socket path is longer than %d bytes", MaxHostSocketPath)
	}
	if err := CheckHostSocketDir(filepath.Dir(path)); err != nil {
		return err
	}
	return checkOwnSocket(path)
}

// peerUID reads the user id of a connection's peer (socketPeerUID); tests
// replace it.
var peerUID = socketPeerUID

// hostPoster makes one attempt on the host's socket, as post does on the
// server's route. An attempt that made no connection is errHostUnreachable:
// the request was never written. Once a connection is made, every failure
// is the host's (it may have applied the report), never errHostUnreachable.
func hostPoster(socket, path, token string) poster {
	return func(ctx context.Context, body []byte) (int, string, error) {
		if err := CheckHostSocket(socket); err != nil {
			return 0, "", fmt.Errorf("%w: %v", errHostUnreachable, err)
		}
		var connected atomic.Bool
		tr := &http.Transport{
			// No proxy: a variable of the environment never routes it.
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				c, err := d.DialContext(ctx, "unix", socket)
				if err != nil {
					return nil, err
				}
				// Nothing is written to a socket whose peer is not this
				// user: the kernel's record of who listens there, read
				// after the connect, is what the names checked before it
				// cannot change.
				if uid, err := peerUID(c); err != nil || uid != os.Getuid() {
					c.Close()
					if err == nil {
						err = fmt.Errorf("its peer is user %d, not this user", uid)
					}
					return nil, err
				}
				connected.Store(true)
				return c, nil
			},
			DisableKeepAlives: true,
		}
		defer tr.CloseIdleConnections()
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://conductor-host"+path, bytes.NewReader(body))
		if err != nil {
			return 0, "", err
		}
		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("Content-Type", "application/json")
		client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(httpReq)
		if err != nil {
			if !connected.Load() {
				return 0, "", fmt.Errorf("%w: %v", errHostUnreachable, err)
			}
			return 0, "", fmt.Errorf("notify: the host's socket: %w", err)
		}
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, strings.TrimSpace(string(msg)), nil
	}
}
