package tracker

import (
	"errors"
	"fmt"
	"net"
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
		host = strings.ToLower(u.Hostname())
		if !hostAdmitted(host, o) {
			return "", "", fmt.Errorf("%w: %s is not a host Conductor reads", ErrRef, host)
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
		host = strings.ToLower(h)
		if !hostAdmitted(host, o) {
			return "", "", fmt.Errorf("%w: %s is not a host Conductor reads", ErrRef, host)
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
	host := strings.ToLower(u.Host)
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
		return "", nil, fmt.Errorf("%w: %s is not a host Conductor reads", ErrRef, host)
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

// IsLoopback reports a host (with or without a port) that is a loopback
// address or localhost.
func IsLoopback(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func hostAdmitted(host string, o ParseOptions) bool {
	if host == GitHubHost {
		return true
	}
	if o.Loopback && IsLoopback(host) {
		return true
	}
	if !validHostName(host) {
		return false
	}
	for _, h := range o.Hosts {
		if strings.ToLower(h) == host {
			return true
		}
	}
	return false
}

// validHostName reports a lower-case DNS name of letters, digits, hyphens
// and dots.
func validHostName(h string) bool {
	if h == "" || len(h) > 253 || h[0] == '.' || h[0] == '-' {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func makeRef(host, owner, name, kind, num string) (Ref, error) {
	if err := checkSpace(owner, name); err != nil {
		return Ref{}, err
	}
	if num == "" || len(num) > MaxID || num[0] == '0' {
		return Ref{}, fmt.Errorf("%w: the number is 1 to %d digits", ErrRef, MaxID)
	}
	if _, err := strconv.ParseUint(num, 10, 64); err != nil {
		return Ref{}, fmt.Errorf("%w: the number is decimal digits", ErrRef)
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
