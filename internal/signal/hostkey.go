package signal

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/rocksolidlabs/conductor/internal/machine"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The prefixes a host's process and a session are hashed under for their
// key (hostKeyOf): neither is machine.Group's, so no key made of one equals
// a machine's, nor this server's own.
const (
	processKeyPrefix = "conductor host process\x00"
	sessionKeyPrefix = "conductor host session key\x00"
)

// hostKeyOf is the key a hosted session's machine is grouped by on this
// server's pages (session.Info.HostKey), always 32 lowercase hex digits,
// and what it was made from. A host that sent a machine key is kept under
// machine.Group of it, the one function the server keys its own row by, so
// a host on this server's machine and the server's own row hold one key; a
// host that sent none but an instance is kept under a hash of its owner
// (one process); the oldest hosts, which send neither, under a hash of the
// session's own id, so no two of them share one. What the host sent is
// never kept.
func hostKeyOf(sent, owner, sessionID string) (key, from string) {
	switch {
	case sent != "":
		return machine.Group(sent), session.HostKeyMachine
	case owner != "":
		return prefixedKey(processKeyPrefix, owner), session.HostKeyProcess
	default:
		return prefixedKey(sessionKeyPrefix, sessionID), session.HostKeySession
	}
}

// prefixedKey is the first 16 bytes of SHA-256 of prefix and s, in hex.
func prefixedKey(prefix, s string) string {
	sum := sha256.Sum256([]byte(prefix + s))
	return hex.EncodeToString(sum[:16])
}

// setHostFields puts on info what the host's register says of itself,
// already held to its forms by the register's checks
// (proto.HostInfo.CheckMachine): its platform, its version when that is of
// its form (none otherwise: an older host's may be of any), and its key,
// made from what it sent.
func setHostFields(info *session.Info, h proto.HostInfo, owner string) {
	info.HostPlatform = h.Platform
	info.HostVersion = ""
	if proto.ValidHostVersion(h.Version) {
		info.HostVersion = h.Version
	}
	info.HostKey, info.HostKeyFrom = hostKeyOf(h.Machine, owner, info.ID)
}
