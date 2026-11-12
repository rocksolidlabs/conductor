package tracker

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// GitHub is the GitHub adapter's system name; GitHubHost github.com's
// host.
const (
	GitHub     = "github"
	GitHubHost = "github.com"
)

// ParseOptions say which hosts a reference may name: github.com always,
// Hosts (GitHub Enterprise Server hosts, lower case) besides, and, with
// Loopback (CONDUCTOR_TRACKER_LOOPBACK=1, which the tests and the e2e
// harness set and nothing else does), a loopback address with a port over
// http.
type ParseOptions struct {
	Hosts    []string
	Loopback bool
}

// ErrRef is a reference Conductor does not read.
var ErrRef = errors.New("not a GitHub issue or pull request reference")

// ParseGitHub reads a GitHub item's reference: its URL
// (https://<host>/<owner>/<name>/issues/<n> or …/pull/<n>), owner/name#n,
// or #n in the repository defaultSpace names (owner/name, a repository's
// URL or a git remote, ParseRepository). A URL with a query, a fragment or
// a path past the number is refused, as is a host the options do not
// admit. A short form is of kind issue: GitHub answers a pull request at
// the issue's route too, and the read says which it is.
func ParseGitHub(raw, defaultSpace string, o ParseOptions) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return Ref{}, ErrRef
	}
	if strings.Contains(raw, "://") {
		host, segs, err := parseURL(raw, o)
		if err != nil {
			return Ref{}, err
		}
		if len(segs) != 4 {
			return Ref{}, fmt.Errorf("%w: the path is /<owner>/<name>/issues/<n> or /<owner>/<name>/pull/<n>", ErrRef)
		}
		kind := ""
		switch segs[2] {
		case "issues":
			kind = KindIssue
		case "pull":
			kind = KindChange
		default:
			return Ref{}, fmt.Errorf("%w: the path is /<owner>/<name>/issues/<n> or /<owner>/<name>/pull/<n>", ErrRef)
		}
		return makeRef(host, segs[0], segs[1], kind, segs[3])
	}
	space, num, ok := strings.Cut(raw, "#")
	if !ok {
		return Ref{}, ErrRef
	}
	host := GitHubHost
	if space == "" {
		if defaultSpace == "" {
			return Ref{}, fmt.Errorf("%w: #n needs a repository", ErrRef)
		}
		h, s, err := ParseRepository(defaultSpace, o)
		if err != nil {
			return Ref{}, err
		}
		host, space = h, s
	} else if defaultSpace != "" {
		if h, _, err := ParseRepository(defaultSpace, o); err == nil {
			host = h
		}
	}
	owner, name, ok := strings.Cut(space, "/")
	if !ok {
		return Ref{}, ErrRef
	}
	return makeRef(host, owner, name, KindIssue, num)
}

// ParseRepository reads a repository: owner/name, its URL
// (https://<host>/<owner>/<name>, with .git or a trailing slash), or a git
// remote (git@<host>:<owner>/<name>.git, ssh://git@<host>[:port]/<owner>/<name>.git).
// It gives the host, lower case, and owner/name.
func ParseRepository(raw string, o ParseOptions) (host, space string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return "", "", ErrRef
	}
	var segs []string
	switch {
	case strings.HasPrefix(raw, "ssh://"):
		u, perr := url.Parse(raw)
		if perr != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
			return "", "", ErrRef
		}
		host, _ = asciiLower(u.Hostname())
		if !hostAdmitted(host, o) {
			return "", "", fmt.Errorf("%w: %s", ErrRef, ErrHost)
		}
		segs = splitPath(u.Path)
	case strings.Contains(raw, "://"):
		host, segs, err = parseURL(raw, o)
		if err != nil {
			return "", "", err
		}
	case strings.Contains(raw, "@") && strings.Contains(raw, ":"):
		at := strings.Index(raw, "@")
		rest := raw[at+1:]
		h, p, ok := strings.Cut(rest, ":")
		if !ok {
			return "", "", ErrRef
		}
		host, _ = asciiLower(h)
		if !hostAdmitted(host, o) {
			return "", "", fmt.Errorf("%w: %s", ErrRef, ErrHost)
		}
		segs = splitPath(p)
	default:
		host = GitHubHost
		segs = strings.Split(raw, "/")
	}
	if len(segs) != 2 {
		return "", "", fmt.Errorf("%w: a repository is <owner>/<name>", ErrRef)
	}
	name := strings.TrimSuffix(segs[1], ".git")
	if err := checkSpace(segs[0], name); err != nil {
		return "", "", err
	}
	return host, segs[0] + "/" + name, nil
}

// parseURL reads an https URL (http on a loopback host with the loopback
// switch) of an admitted host with no query, fragment or user, and gives
// its host and path segments.
func parseURL(raw string, o ParseOptions) (string, []string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", nil, ErrRef
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.User != nil || u.Opaque != "" {
		return "", nil, fmt.Errorf("%w: a reference has no query, fragment or user", ErrRef)
	}
	host, ok := asciiLower(u.Host)
	if !ok {
		return "", nil, fmt.Errorf("%w: %s", ErrRef, ErrHost)
	}
	switch u.Scheme {
	case "https":
		if u.Port() != "" && !(o.Loopback && IsLoopback(host)) {
			return "", nil, fmt.Errorf("%w: a host is named without a port", ErrRef)
		}
	case "http":
		if !o.Loopback || !IsLoopback(host) {
			return "", nil, fmt.Errorf("%w: https only", ErrRef)
		}
	default:
		return "", nil, fmt.Errorf("%w: https only", ErrRef)
	}
	if !hostAdmitted(host, o) {
		return "", nil, fmt.Errorf("%w: %s", ErrRef, ErrHost)
	}
	return host, splitPath(u.Path), nil
}

// splitPath splits a path into its segments, one trailing slash and a
// leading one dropped.
func splitPath(p string) []string {
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// ErrHost is a host Conductor does not admit.
var ErrHost = errors.New("not a host Conductor admits")

// CheckHost is the one rule of what a host is, for every host Conductor
// sends a request to or judges an address by: a credential's, a saved
// Enterprise Server host, an image's, a log's and a reference's. A host is
// a name, never a number: an exact DNS name of at least two labels (so a
// word typed in the wrong field is not one), lower-case ASCII, labels of 1
// to 63 bytes of [a-z0-9-], neither starting nor ending with '-', joined
// by single dots, 253 bytes at most, no trailing dot, whose last label
// begins with a letter (so no spelling of an address, dotted, decimal,
// octal or hex, which a URL parser or a resolver reads by its last label,
// is one), and never localhost or a name under it. With loopback (the
// loopback switch) it may instead be a loopback form, IsLoopback, the only
// way an address or localhost is ever a host. Nothing else is a host: a
// user, a path, a query, a fragment, a port, a bracket, a percent sign, an
// upper case letter or a byte past ASCII is refused, never normalised, so
// the string judged is the string the transport dials. A refusal is
// ErrHost alone: it quotes nothing of what was refused.
func CheckHost(host string, loopback bool) error {
	if loopback && IsLoopback(host) {
		return nil
	}
	if !dnsName(host) {
		return ErrHost
	}
	return nil
}

// dnsName reports an exact lower-case ASCII DNS name of at least two
// labels that is a name: its last label begins with a letter, and it is
// not localhost.
func dnsName(h string) bool {
	if h == "" || len(h) > 253 || !strings.Contains(h, ".") || strings.HasSuffix(h, ".localhost") {
		return false
	}
	if last := h[strings.LastIndexByte(h, '.')+1:]; last == "" || last[0] < 'a' || last[0] > 'z' {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// IsLoopback reports one of the loopback forms the loopback switch admits:
// localhost, 127.0.0.1 or [::1], then ':' and a port of 1 to 5 digits
// (1 to 65535, no leading zero). Nothing spelled around them is one.
func IsLoopback(host string) bool {
	for _, name := range []string{"localhost:", "127.0.0.1:", "[::1]:"} {
		if port, ok := strings.CutPrefix(host, name); ok {
			return portDigits(port)
		}
	}
	return false
}

func portDigits(p string) bool {
	if p == "" || len(p) > 5 || p[0] == '0' {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(p)
	return err == nil && n <= 65535
}

// asciiLower folds ASCII upper case, as a person may type a reference's
// host; a byte past ASCII is no host at all (”, false).
func asciiLower(s string) (string, bool) {
	b := []byte(s)
	for i, c := range b {
		if c >= 0x80 {
			return "", false
		}
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b), true
}

// hostAdmitted reports a reference's host the options admit: github.com,
// a loopback form with the switch, or a host of o.Hosts, each passing
// CheckHost and compared byte for byte.
func hostAdmitted(host string, o ParseOptions) bool {
	if CheckHost(host, o.Loopback) != nil {
		return false
	}
	if host == GitHubHost || (o.Loopback && IsLoopback(host)) {
		return true
	}
	for _, h := range o.Hosts {
		if h == host {
			return true
		}
	}
	return false
}

// Check refuses a reference that is not one this package's parsing makes:
// its system github, its kind issue or change, its space owner/name as
// GitHub names a repository, its id a number and its SpaceID, when one is
// held, a RemoteID (Host is the adapter's to
// check, against the credential). A Ref read back from a store or built by
// a caller is held to it before any of it goes into a request's path.
func (r Ref) Check() error {
	if r.System != GitHub || (r.Kind != KindIssue && r.Kind != KindChange) {
		return fmt.Errorf("%w: not a GitHub issue or pull request", ErrRef)
	}
	owner, name, ok := strings.Cut(r.Space, "/")
	if !ok {
		return fmt.Errorf("%w: a repository is <owner>/<name>", ErrRef)
	}
	if _, err := makeRef(r.Host, owner, name, r.Kind, r.ID); err != nil {
		return err
	}
	if r.SpaceID != "" {
		if err := CheckRemoteID(r.SpaceID); err != nil {
			return fmt.Errorf("%w: %v", ErrRef, err)
		}
	}
	return nil
}

func makeRef(host, owner, name, kind, num string) (Ref, error) {
	if err := checkSpace(owner, name); err != nil {
		return Ref{}, err
	}
	if num == "" || len(num) > MaxID || num[0] == '0' {
		return Ref{}, fmt.Errorf("%w: the number is 1 to %d digits", ErrRef, MaxID)
	}
	for i := 0; i < len(num); i++ {
		if num[i] < '0' || num[i] > '9' {
			return Ref{}, fmt.Errorf("%w: the number is decimal digits", ErrRef)
		}
	}
	return Ref{System: GitHub, Host: host, Space: owner + "/" + name, Kind: kind, ID: num}, nil
}

// checkSpace refuses an owner or a repository name GitHub would not make.
func checkSpace(owner, name string) error {
	if owner == "" || len(owner) > 39 || name == "" || len(name) > 100 || name == "." || name == ".." || len(owner)+1+len(name) > MaxSpace {
		return fmt.Errorf("%w: a repository is <owner>/<name>", ErrRef)
	}
	for i := 0; i < len(owner); i++ {
		c := owner[i]
		if !alnum(c) && c != '-' {
			return fmt.Errorf("%w: an owner is letters, digits and hyphens", ErrRef)
		}
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !alnum(c) && c != '-' && c != '_' && c != '.' {
			return fmt.Errorf("%w: a repository name is letters, digits, '-', '_' and '.'", ErrRef)
		}
	}
	return nil
}

func alnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
