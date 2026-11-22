package proto

import (
	"encoding/hex"
	"errors"
	"regexp"
)

// What a host says of its machine on register (docs/plans/hosts.md, PR-1):
// HostInfo.Machine, HostInfo.Platform, and HostInfo.Version, which a server
// keeps. The forms are checked here, where the wire is; internal/machine
// makes the values and its tests hold them to these forms.
const (
	// MaxHostMachine is HostInfo.Machine's length: exactly this many
	// lowercase hex digits, never fewer.
	MaxHostMachine = 32
	// MaxHostPlatform bounds HostInfo.Platform, which is also of
	// hostPlatformPattern's form.
	MaxHostPlatform = 32
	// MaxHostVersion bounds HostInfo.Version as a server keeps it, which is
	// also of hostVersionPattern's form.
	MaxHostVersion = 64
)

// ServerKeyHeader is the response header the owner's own server sets on an
// upgrade of GET /ws/host from a principal that may register a host (set
// before the WebSocket handshake is answered, so it is there also on one the
// handshake itself refuses): the server's key of this start,
// ServerKeyLen bytes as exactly twice as many lowercase hex digits, from
// which the host makes HostInfo.Machine (machine.Key). A switchyard sends
// none; an upgrade without it, or with one of another form, means the host
// sends no machine and no platform.
const (
	ServerKeyHeader = "Conductor-Server-Key"
	ServerKeyLen    = 32
)

var (
	hostMachinePattern  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hostPlatformPattern = regexp.MustCompile(`^[a-z0-9]{1,15}/[a-z0-9]{1,15}(\+wsl)?$`)
	// hostVersionPattern is what a release name and `git describe` are made
	// of (v1.2.3, v1.2.3-rc.4-12-gabcdef0-dirty, dev).
	hostVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
	serverKeyPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidHostMachine says m is a machine key's form: exactly MaxHostMachine
// lowercase hex digits.
func ValidHostMachine(m string) bool { return hostMachinePattern.MatchString(m) }

// ValidHostPlatform says p is a platform's form, <GOOS>/<GOARCH> with +wsl
// inside WSL, within MaxHostPlatform bytes.
func ValidHostPlatform(p string) bool {
	return len(p) <= MaxHostPlatform && hostPlatformPattern.MatchString(p)
}

// ValidHostVersion says v is a version a server keeps: within
// MaxHostVersion bytes, of hostVersionPattern's form.
func ValidHostVersion(v string) bool {
	return len(v) <= MaxHostVersion && hostVersionPattern.MatchString(v)
}

// ErrHostMachine refuses a register (or a register_desk) whose machine is
// said and not of its form: a key is made by one function and has one form,
// so one out of it is refused whole, never cut down or kept in part.
var ErrHostMachine = errors.New("register: the host's machine is not of its form")

// CheckMachine checks the machine key the host says: empty (an older host,
// a host with no machine id, a server that sent no key) or of its form.
// Platform and Version are not checked here: one out of its form is not
// kept (ValidHostPlatform, ValidHostVersion) and the register stands, so a
// later release that widens a form, or an older host's version of any form,
// still connects.
func (h HostInfo) CheckMachine() error {
	if h.Machine != "" && !ValidHostMachine(h.Machine) {
		return ErrHostMachine
	}
	return nil
}

// ParseServerKey is the key an upgrade's ServerKeyHeader carries: nil
// unless the value is exactly 2*ServerKeyLen lowercase hex digits.
func ParseServerKey(v string) []byte {
	if !serverKeyPattern.MatchString(v) {
		return nil
	}
	b, err := hex.DecodeString(v)
	if err != nil || len(b) != ServerKeyLen {
		return nil
	}
	return b
}
