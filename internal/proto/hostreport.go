package proto

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a host reports of its session for the session's record
// (docs/plans/history.md, PR-12): the host is the launch site, so it reads
// what its session is launched in on its own machine, as the server reads
// it for its own sessions. Each field is optional: an older host sends none
// of them and an older server's decoding ignores them; a server that
// records the session says "not reported by the host" for each one absent,
// never a fact of its own machine in its place.

// HostReport is the part of a register's session (HostSession, in which it
// is embedded) that describes where the session runs: the host's Runtime
// (Where, the slug ext.Where takes), the agent's version from the host's
// own probe of it, and, read in the host's folder before its process
// starts and only when its Runtime is native, the working tree's top
// (Repo), the repository's common git directory (RepoCommon) and the
// commit HEAD named (StartCommit). Check holds it to its bounds; a
// register past one is refused. A host publishing to a switchyard sends
// none of it.
type HostReport struct {
	Where        string `json:"where,omitempty"`
	AgentVersion string `json:"agentVersion,omitempty"`
	Repo         string `json:"repo,omitempty"`
	RepoCommon   string `json:"repoCommon,omitempty"`
	StartCommit  string `json:"startCommit,omitempty"`
}

// The bounds of a HostReport, in bytes: a Where as ext.MaxWhere, a version
// as a probe's first group, a path as a working directory (MaxCwd).
const (
	MaxReportWhere  = 32
	MaxAgentVersion = 64
	MaxReportPath   = MaxCwd
	MaxSessionInfo  = 2 << 10
	HostSessionInfo = "session_info" // host -> server (HostSessionInfoMsg)
)

// ErrReport refuses a register whose HostReport is past its bounds, and
// ErrSessionInfo a session_info that is not one within its own.
var (
	ErrReport      = errors.New("register: what the host reports of its session is out of bounds")
	ErrSessionInfo = errors.New("session_info: not one, or past its bounds")
)

// Check reports whether r is within its bounds: Where none or 1 to
// MaxReportWhere of a-z, 0-9 and '-' beginning with a letter; AgentVersion
// none or at most MaxAgentVersion bytes of one line of UTF-8 text (oneLine);
// Repo and RepoCommon none or an absolute path (a POSIX one, a Windows
// drive's or a UNC one: the host's OS may not be the server's) of at most
// MaxReportPath bytes of one line, never an address; StartCommit none or
// a full commit id (ValidCommit).
func (r HostReport) Check() error {
	if r.Where != "" && !whereSlug(r.Where) {
		return ErrReport
	}
	if len(r.AgentVersion) > MaxAgentVersion || !oneLine(r.AgentVersion) {
		return ErrReport
	}
	if !reportedPath(r.Repo) || !reportedPath(r.RepoCommon) {
		return ErrReport
	}
	if r.StartCommit != "" && !ValidCommit(r.StartCommit) {
		return ErrReport
	}
	return nil
}

// HostSessionInfoMsg (host -> server) is what the host reads of its
// session once its process has ended: the commit its folder's HEAD names
// then (EndCommit, ValidCommit; none when it was not read, its Runtime not
// native among the reasons). Sent before the status that says the session
// ended, on the same connection, so the server has it as it records the
// end; only to a server whose registered said SessionInfo, never to a
// switchyard. The whole message is at most MaxSessionInfo bytes.
// SessionID is the host's, ignored: the connection says which session.
type HostSessionInfoMsg struct {
	T         string `json:"t"`
	SessionID string `json:"sessionId,omitempty"`
	EndCommit string `json:"endCommit,omitempty"`
}

// ParseSessionInfo reads a session_info message: at most MaxSessionInfo
// bytes, of its type, an EndCommit none or ValidCommit. The server drops
// one it refuses and keeps the connection.
func ParseSessionInfo(data []byte) (HostSessionInfoMsg, error) {
	var m HostSessionInfoMsg
	if len(data) > MaxSessionInfo {
		return m, ErrSessionInfo
	}
	if err := json.Unmarshal(data, &m); err != nil || m.T != HostSessionInfo {
		return HostSessionInfoMsg{}, ErrSessionInfo
	}
	if m.EndCommit != "" && !ValidCommit(m.EndCommit) {
		return HostSessionInfoMsg{}, ErrSessionInfo
	}
	return m, nil
}

// ValidCommit reports whether s is a full commit id: 40 (SHA-1) or 64
// (SHA-256) lowercase hex digits.
func ValidCommit(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// whereSlug is ext.Where's form: 1 to MaxReportWhere of a-z, 0-9 and '-',
// beginning with a letter.
func whereSlug(s string) bool {
	if s == "" || len(s) > MaxReportWhere || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// oneLine reports whether s is one line of UTF-8 text made of what a
// reported field may hold, and nothing else: each character a letter, a
// number, punctuation, a symbol or the space U+0020, or a combining mark
// that follows one of those (or another such mark), so no field begins
// with a mark; and none a renderer ignores by default (Unicode's
// Default_Ignorable_Code_Point: a variation selector, the grapheme joiner,
// a filler). A control, a format character, a separator other than the
// space, a private or an unassigned character is none of these. Every text
// field a host reports is held to it.
func oneLine(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	based := false
	for _, r := range s {
		switch {
		case unicode.In(r, unicode.Variation_Selector, unicode.Other_Default_Ignorable_Code_Point):
			return false
		case r == ' ' || unicode.In(r, unicode.L, unicode.N, unicode.P, unicode.S):
			based = true
		case !based || !unicode.Is(unicode.M, r):
			return false
		}
	}
	return true
}

// reportedPath reports whether p is none, or an absolute path of at most
// MaxReportPath bytes of one line: /…, a drive's X:\… or X:/…, or a UNC
// \\…. An address (scheme://…) is none of them.
func reportedPath(p string) bool {
	if p == "" {
		return true
	}
	if len(p) > MaxReportPath || !oneLine(p) {
		return false
	}
	if p[0] == '/' || strings.HasPrefix(p, `\\`) {
		return true
	}
	c := p[0] | 0x20
	return len(p) >= 3 && c >= 'a' && c <= 'z' && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}
