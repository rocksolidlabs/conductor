// Package share issues and resolves share-link tokens. Tokens are random,
// shown once, and stored only as SHA-256 hashes.
package share

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// Hash is the stored form of a token.
type Hash [32]byte

// TokenLen is the length of every token NewToken returns.
const TokenLen = 43

// NewToken returns a TokenLen-character base64url token and its hash.
func NewToken() (string, Hash) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("share: crypto/rand unavailable: " + err.Error())
	}
	tok := base64.RawURLEncoding.EncodeToString(b[:])
	return tok, HashToken(tok)
}

// HashToken computes the storage hash of a presented token.
func HashToken(tok string) Hash { return sha256.Sum256([]byte(tok)) }

// Equal compares two secrets in constant time via their hashes.
func Equal(presented, configured string) bool {
	a, b := HashToken(presented), HashToken(configured)
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
