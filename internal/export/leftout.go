package export

import (
	"path"
	"regexp"
	"strings"
)

// Why an export left a path out (Left.Why), and why a restore refuses an
// archive that holds it.
const (
	WhySecret     = "secret"             // the workbench token, tls/
	WhyLock       = "lock"               // a lock file: the token's, the ledger's owner lock, restore.lock
	WhyTemporary  = "temporary"          // a database's -wal, -shm or -journal, *.tmp, .probe-*, .restore-*, Dispatch's attachments/tmp/
	WhyProvider   = "provider"           // ext/, a provider's own state
	WhyNotRegular = "not a regular file" // a link, a device, a socket, a pipe
	WhyName       = "unsupported name"   // a name an archive cannot hold (Install would refuse it)
)

// leftRule is a row of leftOut: an exact slash path (file), a directory
// and everything under it (dir), or a pattern any element of a path may
// match (name, path.Match's syntax).
type leftRule struct {
	file, dir, name string
	why             string
}

// leftOut is the one table of what Write leaves out of an archive and
// Install refuses in one (docs/plans/ext.md, C6, "the destination
// reserved"): a credential, a lock, a temporary file, a provider's state
// or the restore's own files can neither leave the data directory nor be
// planted in one. The databases' -wal, -shm and -journal are left because
// the snapshot holds what they did.
var leftOut = []leftRule{
	{file: "workbench-token", why: WhySecret},
	{file: "workbench-token.lock", why: WhyLock},
	{file: "restore.lock", why: WhyLock},
	{file: "runs/owner.lock", why: WhyLock},
	{dir: "tls", why: WhySecret},
	{dir: "ext", why: WhyProvider},
	{dir: "dispatch/attachments/tmp", why: WhyTemporary},
	{name: "*.sqlite-wal", why: WhyTemporary},
	{name: "*.sqlite-shm", why: WhyTemporary},
	{name: "*.sqlite-journal", why: WhyTemporary},
	{name: "*.tmp", why: WhyTemporary},
	{name: ".probe-*", why: WhyTemporary},
	{name: ".restore-*", why: WhyTemporary},
}

// reserved says why the slash path rel, relative to the data directory,
// is one leftOut names (itself, or a directory it is under), and whether
// it is.
func reserved(rel string) (string, bool) {
	elems := strings.Split(rel, "/")
	for _, r := range leftOut {
		switch {
		case r.file != "" && rel == r.file:
			return r.why, true
		case r.dir != "" && (rel == r.dir || strings.HasPrefix(rel, r.dir+"/")):
			return r.why, true
		case r.name != "":
			for _, e := range elems {
				if ok, _ := path.Match(r.name, e); ok {
					return r.why, true
				}
			}
		}
	}
	return "", false
}

// maxPath bounds a path an archive holds, in bytes.
const maxPath = 1024

// elementPattern is what each element of an archive's path is made of.
var elementPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validPath says the slash path rel is one an archive may hold under
// data/: relative, at most maxPath bytes, each element of
// [A-Za-z0-9._-] and none "." or "..", so no backslash, no drive, no
// absolute path and no way out.
func validPath(rel string) bool {
	if rel == "" || len(rel) > maxPath {
		return false
	}
	for _, e := range strings.Split(rel, "/") {
		if e == "." || e == ".." || !elementPattern.MatchString(e) {
			return false
		}
	}
	return true
}
