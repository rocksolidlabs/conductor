package tracker

import (
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
// its JSON are token(<fingerprint>), never a byte of the secret, so no
// caller can log one by printing what it holds. An adapter opens it one
// way: Transport, for requests to the system's API host alone.
type Credential struct{ c *credential }

// credential holds the secret behind a closure: fmt and reflection print a
// func as its address, so even a credential reached through an unexported
// field, a pointer or a bad verb never prints a byte of it.
type credential struct {
	host        string
	secret      func() string
	fingerprint string
	write       *Write
}

// Token makes the credential of a token for a host.
func Token(host, secret string) Credential {
	if secret == "" {
		return Credential{&credential{host: host}}
	}
	sum := sha256.Sum256([]byte(secret))
	return Credential{&credential{host: host, secret: func() string { return secret }, fingerprint: hex.EncodeToString(sum[:6])}}
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

// Fingerprint is the first 12 hex digits of the SHA-256 of the secret:
// what a person is shown to tell tokens apart, never a character of the
// token.
func (c Credential) Fingerprint() string {
	if c.c == nil {
		return ""
	}
	return c.c.fingerprint
}

// String is token(<fingerprint>).
func (c Credential) String() string {
	if c.IsZero() {
		return "token(none)"
	}
	return "token(" + c.c.fingerprint + ")"
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

// Transport is the round tripper for requests to the system's API host,
// over base: a token's sets Authorization on each request it is given
// (a copy of it) and does nothing else. An adapter uses it for its API
// host's requests alone; an address the system signs is fetched over base,
// with no credential.
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
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+t.c.secret())
	return t.base.RoundTrip(r2)
}
