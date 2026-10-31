package ext

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"
)

// MediaRoomProvider adds ICE servers to the configured ones (minted TURN
// credentials, for one) and answers rooms. Its free default adds no server
// and has no room: the configured STUN and TURN entries and the WebSocket
// relay are what connects.
type MediaRoomProvider interface {
	Provider
	// ICEServers answers the servers one connection may use, beside the
	// configured ones: a hosted session's viewer, a host's registration, a
	// paste invite's answer. Safe for concurrent use.
	ICEServers(ctx context.Context, r ICERequest) ([]ICEServer, error)
	// Room answers how to join the room of a piece of work; ok false: none.
	Room(ctx context.Context, r RoomRequest) (RoomJoin, bool, error)
}

// ICERequest is the connection ICE servers are asked for.
type ICERequest struct {
	Purpose string `json:"purpose"` // viewer | host | paste (GET /api/ice's offer never reaches a provider)
	Session string `json:"session,omitempty"`
	Role    string `json:"role,omitempty"`
	Viewer  string `json:"viewer,omitempty"`
}

// ICEServer is one STUN or TURN server, as WebRTC's RTCIceServer.
type ICEServer struct {
	URLs       []string `json:"urls"` // stun:, stuns:, turn: or turns:; 1 to MaxICEURLs of at most MaxICEURL bytes
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"` // each at most MaxICESecret bytes
}

// RoomRequest is a piece of work whose room is asked for.
type RoomRequest struct {
	Kind string    `json:"kind"` // ticket | decision | review | run
	ID   string    `json:"id"`
	By   Principal `json:"by"`
}

// RoomJoin is how to join a room.
type RoomJoin struct {
	URL       string    `json:"url"`
	Token     string    `json:"token,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
}

const (
	MaxICEServers = 8 // a provider's, per connection; the configured ones are not counted and never cut
	MaxICEURLs    = 4
	MaxICEURL     = 512
	MaxICESecret  = 256
	ICEWait       = 2 * time.Second
)

// Valid checks s against its bounds and against what WebRTC takes, since a
// browser or Pion refuses a whole configuration for one server it cannot
// use: 1 to MaxICEURLs URLs, each of at most MaxICEURL bytes of printable
// ASCII and a STUN (RFC 7064) or TURN (RFC 7065) URI (iceURL); a username
// and a credential each of one line of at most MaxICESecret bytes, and both
// of them when a URL is a turn: or turns: one.
func (s ICEServer) Valid() error {
	const r = "ice server"
	if len(s.URLs) == 0 || len(s.URLs) > MaxICEURLs {
		return invalid(r, "%d urls, not 1 to %d", len(s.URLs), MaxICEURLs)
	}
	turn := false
	for _, u := range s.URLs {
		switch {
		case len(u) > MaxICEURL:
			return invalid(r, "a url is %d bytes, past %d", len(u), MaxICEURL)
		case !printableASCII(u):
			return invalid(r, "url %q is not printable ASCII without spaces", u)
		}
		isTURN, why := iceURL(u)
		if why != "" {
			return invalid(r, "url %q %s", u, why)
		}
		turn = turn || isTURN
	}
	if err := line(r, "username", s.Username, MaxICESecret, false); err != nil {
		return err
	}
	if err := line(r, "credential", s.Credential, MaxICESecret, false); err != nil {
		return err
	}
	if turn && (s.Username == "" || s.Credential == "") {
		return invalid(r, "a turn: or turns: url needs a username and a credential")
	}
	return nil
}

// iceURL reads u as a STUN URI (RFC 7064: stun: or stuns:, a host, an
// optional port) or a TURN URI (RFC 7065: turn: or turns:, a host, an
// optional port, an optional ?transport=udp or ?transport=tcp), and answers
// whether it is a TURN one, or why it is neither ("" when it is one). A host
// is a name of letters, digits and "-._~", or an IPv6 address in brackets; a
// port is 1 to 65535. It takes no more than Pion's stun.ParseURI takes, so a
// server it passes never makes WebRTC refuse the configuration it is in.
func iceURL(u string) (turn bool, why string) {
	scheme, rest, _ := strings.Cut(u, ":")
	switch scheme {
	case "stun", "stuns":
	case "turn", "turns":
		turn = true
	default:
		return false, "is not stun:, stuns:, turn: or turns:"
	}
	addr, query, hasQuery := strings.Cut(rest, "?")
	host, port, hasPort := addr, "", false
	if strings.HasPrefix(addr, "[") {
		end := strings.IndexByte(addr, ']')
		if end < 0 {
			return turn, "has a [ with no ] in its host"
		}
		host = addr[1:end]
		if ip := net.ParseIP(host); ip == nil || !strings.Contains(host, ":") {
			return turn, "has a host in brackets that is not an IPv6 address"
		}
		if after := addr[end+1:]; after != "" {
			if port, hasPort = strings.CutPrefix(after, ":"); !hasPort {
				return turn, "has something after its host that is not a port"
			}
		}
	} else {
		if i := strings.LastIndexByte(addr, ':'); i >= 0 {
			host, port, hasPort = addr[:i], addr[i+1:], true
		}
		if !hostName(host) {
			return turn, "names no host of letters, digits and -._~"
		}
	}
	if hasPort {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strings.Trim(port, "0123456789") != "" {
			return turn, "has a port that is not 1 to 65535"
		}
	}
	switch {
	case hasQuery && !turn:
		return turn, "has a query, and a stun: or stuns: url takes none"
	case hasQuery && query != "transport=udp" && query != "transport=tcp":
		return turn, "has a query that is not transport=udp or transport=tcp"
	}
	return turn, ""
}

// hostName reports whether h is a host's name: 1 or more of letters,
// digits and "-._~" (RFC 3986's unreserved characters).
func hostName(h string) bool {
	if h == "" {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '.', c == '_', c == '~':
		default:
			return false
		}
	}
	return true
}
