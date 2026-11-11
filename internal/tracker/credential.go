package tracker

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// Credential is an opaque value the adapter alone opens: a person's token
// for one host (Token), possibly tagged with the write it is for
// (ForWrite). Its String, its fmt forms (every verb), its slog value and
// its JSON are token(set) or token(none), fixed words that carry nothing
// of the secret, so no caller can log one by printing what it holds. It
// is made for one API
// address (Token, APIAddress) and opened one way: Transport, for requests
// to that address alone.
type Credential struct{ c *credential }

// credential holds the secret behind a closure: fmt and reflection print a
// func as its address, so even a credential reached through an unexported
// field, a pointer or a bad verb never prints a byte of it.
type credential struct {
	host        string
	scheme, api string
	secret      func() string
	fingerprint string
	write       *Write
}

// Token makes the credential of a token for a host, and with it the one
// API address its token is ever sent to (APIAddress): https and the API
// host of github.com or of a GitHub Enterprise Server host, or, made in
// loopback mode (CONDUCTOR_TRACKER_LOOPBACK=1), plain http to a loopback
// form. A host CheckHost does not admit at the moment it is made is
// refused: no credential is made for it.
func Token(host, secret string, loopback bool) (Credential, error) {
	scheme, api, err := APIAddress(host, loopback)
	if err != nil {
		return Credential{}, err
	}
	c := &credential{host: host, scheme: scheme, api: api}
	if secret == "" {
		return Credential{c}, nil
	}
	c.secret, c.fingerprint = func() string { return secret }, Mark(secret)
	return Credential{c}, nil
}

// markKey is made when the process starts and never written anywhere.
var markKey = func() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}()

// Mark is what tells one token from another for this process's life: the
// first 12 hex digits of the token's HMAC-SHA256 under a key the process
// makes at start and never writes, so it cannot be computed from a token
// alone, and it changes when the process restarts.
func Mark(secret string) string {
	m := hmac.New(sha256.New, markKey)
	m.Write([]byte(secret))
	return hex.EncodeToString(m.Sum(nil)[:6])
}

// APIAddress is the address of a host's API, the one place a token for
// that host goes: https://api.github.com for github.com, https to the host
// itself for any other host CheckHost admits (a GitHub Enterprise Server's
// API is on its own host), and plain http only to a loopback form, in
// loopback mode. Any other host is refused (ErrHost).
func APIAddress(host string, loopback bool) (scheme, api string, err error) {
	if err := CheckHost(host, loopback); err != nil {
		return "", "", err
	}
	switch {
	case host == GitHubHost:
		return "https", "api.github.com", nil
	case loopback && IsLoopback(host):
		return "http", host, nil
	}
	return "https", host, nil
}

// IsZero reports the credential of no token.
func (c Credential) IsZero() bool { return c.c == nil || c.c.secret == nil }

// Host is the host the credential is for.
func (c Credential) Host() string {
	if c.c == nil {
		return ""
	}
	return c.c.host
}

// API is the address the credential's token is sent to, its scheme and
// host (APIAddress), fixed when it was made.
func (c Credential) API() (scheme, host string) {
	if c.c == nil {
		return "", ""
	}
	return c.c.scheme, c.c.api
}

// Fingerprint is the secret's Mark: what a person is shown to tell this
// server's tokens apart, never a character of the token nor anything that
// can be computed from it alone.
func (c Credential) Fingerprint() string {
	if c.c == nil {
		return ""
	}
	return c.c.fingerprint
}

// String is token(set), or token(none) for the credential of no token.
func (c Credential) String() string {
	if c.IsZero() {
		return "token(none)"
	}
	return "token(set)"
}

// GoString is the String, for %#v.
func (c Credential) GoString() string { return c.String() }

// Format writes the String for every verb.
func (c Credential) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(c.String())) }

// LogValue is the String, for slog.
func (c Credential) LogValue() slog.Value { return slog.StringValue(c.String()) }

// MarshalJSON is the String, as a JSON string.
func (c Credential) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

// Write is what a write's credential is tagged with: Kind comment or close,
// Publication the publication's id, Try its attempt (a comment) or its
// close_tries (a close), PostSha256 the posted bytes' digest (a comment),
// Comment the publication's remote_id (a close).
type Write struct {
	Kind        string `json:"kind"`
	Publication string `json:"publication"`
	Try         int    `json:"try"`
	PostSha256  string `json:"postSha256,omitempty"`
	Comment     string `json:"comment,omitempty"`
}

// The kinds of write.
const (
	WriteComment = "comment"
	WriteClose   = "close"
)

// ForWrite is the same credential tagged with the write it is for. A
// token sends nothing of the tag to the system; the adapter names the
// publication and the try in the write's log line.
func (c Credential) ForWrite(w Write) Credential {
	if c.c == nil {
		return c
	}
	cp := *c.c
	cp.write = &w
	return Credential{&cp}
}

// Write is the write the credential is tagged for, if it is.
func (c Credential) Write() (Write, bool) {
	if c.c == nil || c.c.write == nil {
		return Write{}, false
	}
	return *c.c.write, true
}

// Transport is the round tripper for requests to the credential's own API
// address (API), over base: its caller names no scheme and no host. A
// token's transport checks each request it is given, at every hop a client
// makes, and sends only one whose URL has that scheme, that host byte for
// byte and no user, setting Authorization on a copy of it and sending no
// Referer; any other is refused with ErrOffHost before base sees it. So
// the destination is fixed by the credential at the level of the address:
// no request for another address carries the token. base itself is the
// caller's round tripper (the adapter's Options.Transport, a seam for
// tests, nil for the standard one): it is handed the copy that carries
// Authorization, and it dials where it dials. An address the system signs
// is fetched over base, with no credential.
func (c Credential) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if c.IsZero() {
		return base
	}
	return tokenTransport{base: base, c: c.c}
}

// tokenTransport holds the credential, whose secret no print reaches.
type tokenTransport struct {
	base http.RoundTripper
	c    *credential
}

func (t tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.c.api == "" || r.URL == nil || r.URL.Scheme != t.c.scheme || r.URL.Host != t.c.api || r.URL.User != nil || (r.Host != "" && r.Host != t.c.api) {
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, ErrOffHost
	}
	r2 := r.Clone(r.Context())
	r2.Header.Del("Referer")
	r2.Header.Set("Authorization", "Bearer "+t.c.secret())
	return t.base.RoundTrip(r2)
}
