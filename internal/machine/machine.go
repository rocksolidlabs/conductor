// Package machine says which machine a host runs on without telling any
// server who that machine is (docs/plans/hosts.md, PR-1).
//
// Each machine keeps a random id of its own, per user, in a file no export
// carries and no session's file routes read (Dir, ID). A host sends a
// server only a key made of that id and the server's key of its start
// (Key), and a server keeps and serves only a hash of what it was sent
// (Group): the one function that makes a machine's key on a server, so a
// host on the server's own machine and the server's own row hold one key.
// The key groups what a page shows and decides nothing: a host holding a
// host token can send any key.
//
// Nothing here logs, and no error holds a byte of the file: a bad file is
// named by its path and why. Standard library only.
package machine

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// FileName is the file in Dir that holds the machine's id.
const FileName = "machine-id"

// IDLen is the length of a machine's id, of a key a host sends (Key) and of
// a key a server keeps (Group), in lowercase hex digits.
const IDLen = 32

// ServerKeyLen is the length of a server's key in bytes.
const ServerKeyLen = 32

// MaxPlatform bounds Platform.
const MaxPlatform = 32

// validHex says s is exactly IDLen lowercase hex digits.
func validHex(s string) bool {
	if len(s) != IDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Dir is where the machine's id is kept: conductor in the user's config
// directory (os.UserConfigDir: ~/.config/conductor on Linux and inside WSL,
// ~/Library/Application Support/conductor on macOS, %AppData%\conductor on
// Windows), outside every default data directory. A server and a host of
// one user on one machine read one file there.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("machine: the user's config directory is not an absolute path")
	}
	return filepath.Join(base, "conductor"), nil
}

// beforeLink, when set (tests), is called by a maker between writing its
// temporary file and linking it into place.
var beforeLink func()

// ID is the machine's id kept in dir (FileName), made there when there is
// none: IDLen random lowercase hex digits (crypto/rand, never a hardware or
// system identifier) and a newline, 0600 in a directory made 0700. The file
// is published by a link from a fully written temporary file beside it, and
// a link never replaces a file that is there: of two makers that find none,
// one link wins and the other reads the winner's id, so both return one id.
// A file that cannot be read or is not of that form, and a file system that
// refuses the link, are an error that names the path and why, never what
// the file holds: no id is reported then.
func ID(dir string) (string, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return "", errors.New("machine: the id's directory is not an absolute path")
	}
	p := filepath.Join(dir, FileName)
	id, err := read(p)
	if !errors.Is(err, fs.ErrNotExist) {
		return id, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("machine: %s cannot be made: %w", dir, reason(err))
	}
	var b [IDLen / 2]byte
	_, _ = rand.Read(b[:]) // never fails (crypto/rand)
	made := hex.EncodeToString(b[:])
	tmp, err := os.CreateTemp(dir, FileName+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("machine: a file cannot be made in %s: %w", dir, reason(err))
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, werr := tmp.WriteString(made + "\n")
	if werr == nil {
		werr = tmp.Chmod(0o600)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("machine: a file cannot be written in %s: %w", dir, reason(werr))
	}
	if beforeLink != nil {
		beforeLink()
	}
	if err := os.Link(name, p); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Another maker's link won: its id is the machine's.
			return read(p)
		}
		return "", fmt.Errorf("machine: %s: the file system refused the link that publishes it: %w", p, reason(err))
	}
	return made, nil
}

// read is the id the file p holds: exactly IDLen lowercase hex digits and a
// newline, in a regular file (never through a link). Its errors name p and
// why, never the bytes.
func read(p string) (string, error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fs.ErrNotExist
	}
	if err != nil {
		return "", fmt.Errorf("machine: %s cannot be read: %w", p, reason(err))
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("machine: %s is not a regular file", p)
	}
	f, err := os.Open(p)
	if err != nil {
		return "", fmt.Errorf("machine: %s cannot be read: %w", p, reason(err))
	}
	defer f.Close()
	// The file opened is the one looked at: not one put in its place since.
	if opened, err := f.Stat(); err != nil || !os.SameFile(fi, opened) {
		return "", fmt.Errorf("machine: %s changed while it was read", p)
	}
	b, err := io.ReadAll(io.LimitReader(f, IDLen+2))
	if err != nil {
		return "", fmt.Errorf("machine: %s cannot be read: %w", p, reason(err))
	}
	if len(b) != IDLen+1 || b[IDLen] != '\n' || !validHex(string(b[:IDLen])) {
		return "", fmt.Errorf("machine: %s does not hold an id (%d lowercase hex digits and a newline)", p, IDLen)
	}
	return string(b[:IDLen]), nil
}

// reason is err without the path an *fs.PathError or *os.LinkError repeats
// (the callers name the path themselves): the operation's reason alone.
func reason(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err
	}
	return err
}

// Key is what a host sends the server whose key of this start is
// serverKey: HMAC-SHA-256 of the machine's id keyed by serverKey, its first
// 16 bytes in lowercase hex (IDLen digits). One id gives every server, and
// one server across its restarts, an unrelated key, and the id is never
// sent. "" (none sent) for an id or a server key not of its form.
func Key(id string, serverKey []byte) string {
	if !validHex(id) || len(serverKey) != ServerKeyLen {
		return ""
	}
	m := hmac.New(sha256.New, serverKey)
	m.Write([]byte(id))
	return hex.EncodeToString(m.Sum(nil)[:IDLen/2])
}

// groupPrefix is what Group hashes a key under. A server hashes a process's
// or a session's key under prefixes of its own (internal/signal), so none
// of theirs equals a machine's.
const groupPrefix = "conductor host machine\x00"

// Group is what a server keeps and serves of a machine's key it was sent:
// the first 16 bytes of SHA-256 of groupPrefix and the key, in lowercase
// hex. It is the only maker of a machine's key on a server: the hub keys a
// host that sent one by it, and the server keys its own row by
// Group(Key(its own id, its key)), so a host on its machine and its own
// row hold one string. What a host sent is never served.
func Group(sent string) string {
	sum := sha256.Sum256([]byte(groupPrefix + sent))
	return hex.EncodeToString(sum[:IDLen/2])
}

// osrelease is the kernel's release string, which names Microsoft inside WSL.
const osrelease = "/proc/sys/kernel/osrelease"

var platformOnce = sync.OnceValue(func() string {
	return platformOf(runtime.GOOS, runtime.GOARCH, func() ([]byte, error) { return os.ReadFile(osrelease) })
})

// Platform is this machine's <GOOS>/<GOARCH>, with +wsl when it runs in
// WSL (the kernel's release names Microsoft), read once; "" when that is
// not of the form `^[a-z0-9]{1,15}/[a-z0-9]{1,15}(\+wsl)?$` within
// MaxPlatform bytes, so no host sends one a server refuses.
func Platform() string { return platformOnce() }

// platformOf is Platform for goos and goarch on a kernel whose release
// reads: the seam a test gives its own release.
func platformOf(goos, goarch string, release func() ([]byte, error)) string {
	p := goos + "/" + goarch
	if goos == "linux" {
		if b, err := release(); err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft") {
			p += "+wsl"
		}
	}
	if !validPlatform(p) {
		return ""
	}
	return p
}

// validPlatform says p is <a>/<b> or <a>/<b>+wsl, each part 1 to 15 bytes
// of [a-z0-9], within MaxPlatform bytes.
func validPlatform(p string) bool {
	if len(p) > MaxPlatform {
		return false
	}
	p = strings.TrimSuffix(p, "+wsl")
	goos, arch, ok := strings.Cut(p, "/")
	return ok && part(goos) && part(arch)
}

// part is 1 to 15 bytes of [a-z0-9].
func part(s string) bool {
	if len(s) < 1 || len(s) > 15 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}
