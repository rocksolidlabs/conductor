package ext

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// HostSummary is a server's host summary (docs/plans/hosts.md, PR-5): what
// one of a person's servers tells their other servers of itself and of the
// hosts that reach it, as its Hosts page shows them. It is the body of the
// sync document hosts/summary that a server hands its sync store, and of
// the remote record of kind RemoteHosts that a sync store hands in for
// another of the person's servers.
//
// It carries counts (the hosts left out of its list among them),
// platforms, versions, availability and the names the hosts gave
// themselves, and nothing else: never a session's id, command,
// folder or branch, a run's name, anything a terminal printed, the OS user
// a host runs as, a machine's id or key, an address, a token or a key of
// any kind. The server's own name is the device record's, not the
// summary's.
//
// ParseHostSummary decodes one strictly; Valid checks one before it is
// handed on.
type HostSummary struct {
	Platform string           `json:"platform"` // this server's, <GOOS>/<GOARCH> with +wsl inside WSL
	Version  string           `json:"version"`  // this server's
	Sessions HostSessions     `json:"sessions"` // this server's own sessions
	Runs     HostRuns         `json:"runs"`     // the open managed runs this server holds
	Hosts    []HostSummaryRow `json:"hosts"`    // at most MaxHostSummaryRows, as the server's Hosts page orders them
	// More is how many of the server's hosts are not in Hosts: those past
	// MaxHostSummaryRows, those left out to fit MaxHostSummary, and those
	// whose name a summary may not carry. A list that was cut says so.
	More int `json:"more,omitempty"`
}

// HostSummaryRow is one machine whose hosts reach the server, as the
// server last saw it: the name its hosts gave, its platform and version
// when its hosts report them, its availability and since when, and its
// sessions.
type HostSummaryRow struct {
	Name         string       `json:"name"`
	Platform     string       `json:"platform,omitempty"`
	Version      string       `json:"version,omitempty"`
	Availability string       `json:"availability"` // HostConnected, HostAway or HostSeen
	Since        time.Time    `json:"since,omitzero"`
	Sessions     HostSessions `json:"sessions"`
}

// HostSessions counts sessions: those starting, running or away, and those
// waiting for a person.
type HostSessions struct {
	Running int `json:"running"`
	Waiting int `json:"waiting"`
}

// HostRuns counts the open managed runs a server holds, and those among
// them that wait for a person.
type HostRuns struct {
	Held    int `json:"held"`
	Waiting int `json:"waiting"`
}

// The availabilities of a HostSummaryRow: at least one of the machine's
// sessions has a live link (since, when given, when it connected); none
// has and one waits in its grace (since when its last link dropped); or no
// session is left (since when the last one went).
const (
	HostConnected = "connected"
	HostAway      = "away"
	HostSeen      = "seen"
)

// The bounds of a host summary.
const (
	MaxHostSummary     = MaxRemoteHosts // the encoded summary, in bytes
	MaxHostSummaryRows = 32
	MaxHostName        = 120 // bytes, the hub's bound for a host's name
	MaxHostPlatform    = 32  // bytes
	MaxHostVersion     = 64  // bytes
	MaxHostCount       = 1_000_000
)

var (
	// hostPlatform is a platform's form: <GOOS>/<GOARCH>, +wsl inside WSL.
	hostPlatform = regexp.MustCompile(`^[a-z0-9]{1,15}/[a-z0-9]{1,15}(\+wsl)?$`)
	// hostVersion is a version's form: what git describe and a release
	// name are made of.
	hostVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
	// hostIDShape is the shape of a session's id, a machine's id and a
	// key: 16 or more hex digits and nothing else. No host's name has it.
	hostIDShape = regexp.MustCompile(`^[0-9A-Fa-f]{16,}$`)
)

// hostSummaryKeys are the field names of a host summary and of its parts,
// each exactly as written.
var hostSummaryKeys = map[string]bool{
	"platform": true, "version": true, "sessions": true, "runs": true, "hosts": true,
	"name": true, "availability": true, "since": true,
	"more": true, "running": true, "waiting": true, "held": true,
}

// ParseHostSummary decodes a host summary strictly and checks it (Valid):
// at most MaxHostSummary bytes of one JSON object, every field of its
// form, none unknown (each name exactly as written, none given twice) and
// nothing after it. A body that is not such a summary is refused, never
// trimmed: the error wraps ErrRefused and holds none of the body.
func ParseHostSummary(data []byte) (HostSummary, error) {
	if len(data) > MaxHostSummary {
		return HostSummary{}, fmt.Errorf("%w: a host summary is %d bytes, past %d", ErrRefused, len(data), MaxHostSummary)
	}
	var s HostSummary
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return HostSummary{}, fmt.Errorf("%w: a host summary is a JSON object of its fields alone", ErrRefused)
	}
	if !bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte("{")) {
		return HostSummary{}, fmt.Errorf("%w: a host summary is a JSON object of its fields alone", ErrRefused)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return HostSummary{}, fmt.Errorf("%w: a host summary has nothing after it", ErrRefused)
	}
	if !exactKeys(data, hostSummaryKeys) {
		return HostSummary{}, fmt.Errorf("%w: a host summary names each of its fields once, exactly as written", ErrRefused)
	}
	if err := s.Valid(); err != nil {
		return HostSummary{}, err
	}
	return s, nil
}

// Valid checks a host summary before it is handed on: this server's
// platform and version of their forms; every count 0 to MaxHostCount, the
// hosts left out (More) among them; at most MaxHostSummaryRows rows, each
// with a name of 1 to MaxHostName bytes of one line that is not a path, a
// login, an address or an id, a platform and a version empty or of their
// forms, an availability of the three, and since set for away and seen;
// and the whole, encoded, at most MaxHostSummary bytes. Its errors wrap
// ErrRefused.
func (s HostSummary) Valid() error {
	switch {
	case !validHostPlatform(s.Platform):
		return fmt.Errorf("%w: a host summary names this server's platform as <GOOS>/<GOARCH>, in at most %d bytes", ErrRefused, MaxHostPlatform)
	case !validHostVersion(s.Version):
		return fmt.Errorf("%w: a host summary names this server's version in at most %d bytes of A-Z a-z 0-9 . + _ -", ErrRefused, MaxHostVersion)
	case !s.Sessions.valid() || !validHostCount(s.Runs.Held) || !validHostCount(s.Runs.Waiting) || !validHostCount(s.More):
		return fmt.Errorf("%w: a host summary's counts are 0 to %d", ErrRefused, MaxHostCount)
	case len(s.Hosts) > MaxHostSummaryRows:
		return fmt.Errorf("%w: a host summary has %d hosts, past %d", ErrRefused, len(s.Hosts), MaxHostSummaryRows)
	}
	for i, h := range s.Hosts {
		if err := h.valid(); err != nil {
			return fmt.Errorf("%w: a host summary's host %d %s", ErrRefused, i+1, err)
		}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("%w: a host summary does not encode", ErrRefused)
	}
	if len(b) > MaxHostSummary {
		return fmt.Errorf("%w: a host summary is %d bytes encoded, past %d", ErrRefused, len(b), MaxHostSummary)
	}
	return nil
}

// valid checks one row; its words follow "a host summary's host n".
func (h HostSummaryRow) valid() error {
	switch {
	case !summaryHostName(h.Name):
		return fmt.Errorf("is named in 1 to %d bytes of one line, never a path, a login, an address or an id", MaxHostName)
	case h.Platform != "" && !validHostPlatform(h.Platform):
		return fmt.Errorf("names its platform as <GOOS>/<GOARCH>, in at most %d bytes", MaxHostPlatform)
	case h.Version != "" && !validHostVersion(h.Version):
		return fmt.Errorf("names its version in at most %d bytes of A-Z a-z 0-9 . + _ -", MaxHostVersion)
	case !h.Sessions.valid():
		return fmt.Errorf("counts its sessions 0 to %d", MaxHostCount)
	}
	switch h.Availability {
	case HostConnected:
	case HostAway, HostSeen:
		if h.Since.IsZero() {
			return fmt.Errorf("says since when it is %s", h.Availability)
		}
	default:
		return errors.New("is connected, away or seen")
	}
	return nil
}

func (c HostSessions) valid() bool { return validHostCount(c.Running) && validHostCount(c.Waiting) }

func validHostCount(n int) bool { return n >= 0 && n <= MaxHostCount }

func validHostPlatform(p string) bool {
	return len(p) <= MaxHostPlatform && hostPlatform.MatchString(p)
}

func validHostVersion(v string) bool { return len(v) <= MaxHostVersion && hostVersion.MatchString(v) }

// summaryHostName reports whether s may be shown as a host's name on
// another of the person's machines: 1 to MaxHostName bytes of one line,
// not blank, and of no shape that would carry what a summary never holds:
// no path separator, no @ (a login, a mail address), no colon (an address
// and its port, a drive), not an IP address, and not an id's or a key's
// shape.
func summaryHostName(s string) bool {
	switch {
	case len(s) > MaxHostName || strings.TrimSpace(s) == "" || !oneLine(s):
		return false
	case strings.ContainsAny(s, `/\@:`):
		return false
	case hostIDShape.MatchString(s):
		return false
	}
	_, err := netip.ParseAddr(s)
	return err != nil
}

// exactKeys reports whether every object in data, at any depth, names only
// keys of allowed, each exactly as written and at most once. encoding/json
// matches a key to a field without regard to case and keeps the last of a
// key given twice; this check refuses both. data has decoded already, so
// its depth is its type's.
func exactKeys(data []byte, allowed map[string]bool) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() bool
	walk = func() bool {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				k, err := dec.Token()
				key, ok := k.(string)
				if err != nil || !ok || !allowed[key] || seen[key] {
					return false
				}
				seen[key] = true
				if !walk() {
					return false
				}
			}
		case json.Delim('['):
			for dec.More() {
				if !walk() {
					return false
				}
			}
		default:
			return true
		}
		_, err = dec.Token() // the closing delimiter
		return err == nil
	}
	return walk()
}
