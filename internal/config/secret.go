package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// Secret is a credential the server was given in its environment and must
// never write anywhere: its String, every fmt verb, its slog value and
// its JSON are "set" or "unset", never a byte of it, so a Config printed,
// logged or marshalled whole carries none of it. The value is held behind
// a closure, which fmt and reflection print as an address. Reveal is the
// one way to read it.
type Secret struct{ v func() string }

// NewSecret holds s ("" is unset).
func NewSecret(s string) Secret {
	if s == "" {
		return Secret{}
	}
	return Secret{v: func() string { return s }}
}

// Set reports a value is held.
func (s Secret) Set() bool { return s.v != nil }

// Reveal is the value, "" when unset. Only the code that hands it to the
// service it is for calls it (internal/api, credentialFor).
func (s Secret) Reveal() string {
	if s.v == nil {
		return ""
	}
	return s.v()
}

// String is "set" or "unset".
func (s Secret) String() string {
	if s.Set() {
		return "set"
	}
	return "unset"
}

// GoString is the String, for %#v.
func (s Secret) GoString() string { return s.String() }

// Format writes the String for every verb.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(s.String())) }

// LogValue is the String, for slog.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalJSON is the String, as a JSON string.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// MaxTrackerToken bounds a tracker's token, in bytes.
const MaxTrackerToken = 255

// TrackerTokenForm reports whether tok has a token's form: 1 to
// MaxTrackerToken bytes of visible ASCII (no space, no control byte). The
// environment's token and one saved in Settings are both held to it.
func TrackerTokenForm(tok string) bool {
	if tok == "" || len(tok) > MaxTrackerToken {
		return false
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] < 0x21 || tok[i] > 0x7e {
			return false
		}
	}
	return true
}
