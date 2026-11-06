package session

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
)

// A session's folder, its working directory, is opened once (os.Root) when
// the session starts, and the Files tab's reads, stats, listings, finds and
// saves go through it. A request's path is checked as before (ResolvePath:
// its links followed, the folder, .git/objects, the deny list), then used
// one element at a time from the folder's handle: each element is looked at
// without following it and opened through the folder before it, and once
// open it must be the file the look found. The check resolved every link,
// so an element that is a link by then, or another file once open, changed
// after the check and is refused. What a request reaches is therefore in
// the folder whatever a link does meanwhile (os.Root refuses a way out of
// it in any case), is the file its check passed, and is held to the deny
// list again by identity as it is opened.
//
// The folder is the one the session started in: when its path comes to
// name another directory (the folder moved, a link put in its place), its
// files are refused rather than read from where the path leads now.

// Folder is a session's working directory, open.
type Folder struct {
	path string
	root *os.Root
	id   os.FileInfo
	// allow, when set, says which files of the folder a request may reach
	// (the agent's plans directory, #289: the plans the agent reported),
	// asked of the path a request resolves to (resolve), the one then
	// opened from the folder element by element with no link followed: so
	// what is opened is what was allowed, whatever a link is made to lead
	// to meanwhile. A file it refuses is refused as one outside the folder.
	allow func(real string) bool
}

var (
	errOutside     = errors.New("path is outside the session directory")
	errFolderGone  = errors.New("session root unavailable")
	errFolderMoved = errors.New("the session's folder is no longer at its path")
	errPathChanged = errors.New("the path changed while it was opened; try again")
	errNotReadable = errors.New("path is not readable")
	errNotDir      = errors.New("not a directory")
)

// afterCheckHook, when set (by a test), runs once a request's path is
// checked and before it is used (afterCheck), where a test changes the
// folder as another process could meanwhile.
var afterCheckHook atomic.Pointer[func()]

func afterCheck() {
	if fn := afterCheckHook.Load(); fn != nil {
		(*fn)()
	}
}

// openDirOnly is how a directory is named to os.Root to open it: its path
// and ".", so that every element of the path is opened as a directory only
// (a FIFO put there fails at once instead of waiting for a writer).
func openDirOnly(path string) string {
	if strings.HasSuffix(path, string(filepath.Separator)) {
		return path + "."
	}
	return path + string(filepath.Separator) + "."
}

// OpenFolder opens path (relative to the process's own working directory
// when it is not absolute, as ResolvePath takes it) as a session's folder.
func OpenFolder(path string) (*Folder, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(openDirOnly(abs))
	if err != nil {
		return nil, err
	}
	return newFolder(path, r)
}

// OpenFolderIn opens the folder rel names under dir through dir (os.Root):
// what it opens is under dir whatever a link on the way does meanwhile. Its
// path is dir joined with rel. want, when given, is dir as it was checked:
// the directory opened must be that one (os.SameFile), so dir replaced in
// between (a link put at its path or its parents') is refused.
func OpenFolderIn(dir, rel string, want os.FileInfo) (*Folder, error) {
	parent, err := os.OpenRoot(openDirOnly(dir))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if want != nil {
		got, err := parent.Stat(".")
		if err != nil {
			return nil, err
		}
		if !os.SameFile(got, want) {
			return nil, errors.New("session: the folder's root changed while it was opened")
		}
	}
	r, err := parent.OpenRoot(openDirOnly(rel))
	if err != nil {
		return nil, err
	}
	return newFolder(filepath.Join(dir, rel), r)
}

func newFolder(path string, r *os.Root) (*Folder, error) {
	fi, err := r.Stat(".")
	if err != nil {
		r.Close()
		return nil, err
	}
	return &Folder{path: path, root: r, id: fi}, nil
}

// Path is the path the folder was opened at ("" for none).
func (f *Folder) Path() string {
	if f == nil {
		return ""
	}
	return f.path
}

// Close closes the folder; what is under way finishes or fails.
func (f *Folder) Close() error { return f.root.Close() }

// Lstat describes rel, a path relative to the folder, through the
// folder's root, a link at its last element not followed: nothing outside
// the folder is looked at, whatever a link on the way names.
func (f *Folder) Lstat(rel string) (os.FileInfo, error) { return f.root.Lstat(rel) }

// Dir opens the folder's directory itself, for a process to start in
// (pty.Spec.DirFile).
func (f *Folder) Dir() (*os.File, error) { return f.root.Open(".") }

// check reports whether the folder's path still names the folder, by its
// links resolved: the paths a request's check speaks of are the folder's.
func (f *Folder) check() error {
	if f == nil {
		return errFolderGone
	}
	abs, err := filepath.Abs(f.path)
	if err != nil {
		return errFolderGone
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return errFolderGone
	}
	return f.at(real)
}

// at reports whether rootReal, the folder's path with its links resolved,
// names the folder opened.
func (f *Folder) at(rootReal string) error {
	fi, err := os.Stat(rootReal)
	if err != nil {
		return errFolderGone
	}
	if !os.SameFile(fi, f.id) {
		return errFolderMoved
	}
	return nil
}

// resolve checks raw as ResolvePath does against the folder's path, and
// returns what it resolves to and the same path from the folder (no
// element of it a link when it was resolved), once that path is checked to
// still name the folder. deny is the request's deny list, looked up once
// for its check and its use.
func (f *Folder) resolve(raw string, deny denySet) (real, rel string, err error) {
	if f == nil {
		return "", "", errFolderGone
	}
	real, rootReal, err := resolvePath(f.path, raw, deny)
	if err != nil {
		return "", "", err
	}
	if f.allow != nil && !f.allow(real) {
		return "", "", errOutside
	}
	if err := f.at(rootReal); err != nil {
		return "", "", err
	}
	rel, err = filepath.Rel(rootReal, real)
	if err != nil {
		return "", "", errFolderGone
	}
	return real, rel, nil
}

// openDir opens the directory rel names in the folder (a path resolve
// returned, or "."), one element at a time (stepDir). The caller closes
// what it returns.
func (f *Folder) openDir(rel string, deny denySet) (*os.Root, os.FileInfo, error) {
	return f.openDirChecked(rel, deny, nil)
}

// openDirChecked is openDir with check run on each directory it opens,
// the folder's own first: an error from it ends the walk with that error.
func (f *Folder) openDirChecked(rel string, deny denySet, check func(*os.Root) error) (*os.Root, os.FileInfo, error) {
	cur, err := f.root.OpenRoot(".")
	if err != nil {
		return nil, nil, errFolderGone
	}
	id := f.id
	if fi, err := cur.Stat("."); err == nil {
		id = fi
	}
	if rel == "" {
		rel = "."
	}
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		if check != nil {
			if err := check(cur); err != nil {
				cur.Close()
				return nil, nil, err
			}
		}
		if name == "." {
			break
		}
		next, nid, err := stepDir(cur, id, name, deny)
		cur.Close()
		if err != nil {
			return nil, nil, err
		}
		cur, id = next, nid
	}
	if check != nil && rel != "." {
		if err := check(cur); err != nil {
			cur.Close()
			return nil, nil, err
		}
	}
	return cur, id, nil
}

// refuseGit is a save's check on each directory it opens, given what a
// repository's metadata was at the save's check (gitMetadataIDs): a git
// directory by its own files (gitDirectoryIn), or one of those, is read
// only, as gitMetadata says of a path, whatever was moved where since.
func refuseGit(snap gitMeta) func(*os.Root) error {
	return func(r *os.Root) error {
		if gitDirectoryIn(r) {
			return errGitDir
		}
		if fi, err := r.Stat("."); err == nil && sameAsAny(fi, snap.files) {
			return errGitDir
		}
		return nil
	}
}

// stepDir opens the directory name in dir (dirID being dir as a file): it
// is looked at without following it, must not be a link (a link where its
// check found none changed since) nor in the deny list, and once open it
// must be the directory looked at. The caller closes what it returns.
func stepDir(dir *os.Root, dirID os.FileInfo, name string, deny denySet) (*os.Root, os.FileInfo, error) {
	fi, err := dir.Lstat(name)
	switch {
	case err != nil:
		return nil, nil, err
	case fi.Mode()&os.ModeSymlink != 0:
		return nil, nil, errPathChanged
	case !fi.IsDir():
		return nil, nil, errNotDir
	case deny.hit(fi, name, dirID):
		return nil, nil, errNotReadable
	}
	next, err := dir.OpenRoot(openDirOnly(name))
	if err != nil {
		return nil, nil, errPathChanged
	}
	got, err := next.Stat(".")
	if err != nil || !os.SameFile(fi, got) {
		next.Close()
		return nil, nil, errPathChanged
	}
	return next, got, nil
}

// folderEntry is an entry of a folder as read through its handle: its
// name and its Lstat through the folder (a link not followed).
type folderEntry struct {
	name string
	fi   os.FileInfo
}

// readChunk is how many names one read of a folder takes (readFolder).
const readChunk = 256

// readFolder reads the entries of dir, open: their names alone, readChunk
// at a time, each counted against *budget before it is looked at through
// dir (Lstat), so every name read is counted, one gone by its look
// included (a stat in the read itself would skip those uncounted on some
// systems). stop, asked between reads, ends it early. complete is false
// when names were left unread (a last read of one name tells). err is
// why the folder could not be opened or read, with what was read before.
func readFolder(dir *os.Root, budget *int, stop func() bool) (entries []folderEntry, complete bool, err error) {
	d, err := dir.Open(".")
	if err != nil {
		return nil, true, err
	}
	defer d.Close()
	for {
		if *budget <= 0 || stop() {
			more, _ := d.Readdirnames(1)
			return entries, len(more) == 0, nil
		}
		names, err := d.Readdirnames(min(readChunk, *budget))
		for _, name := range names {
			*budget--
			if fi, err := dir.Lstat(name); err == nil {
				entries = append(entries, folderEntry{name, fi})
			}
		}
		switch {
		case errors.Is(err, io.EOF) || err == nil && len(names) == 0:
			return entries, true, nil
		case err != nil:
			return entries, true, err
		}
	}
}

// openFileIn opens the regular file name in dir for reading, which must be
// the file fi a look at it found: one put in its place since (a link
// included) is refused. The open does not wait on a FIFO.
func openFileIn(dir *os.Root, name string, fi os.FileInfo) (*os.File, os.FileInfo, error) {
	file, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	got, err := file.Stat()
	if err != nil || !got.Mode().IsRegular() || !os.SameFile(fi, got) {
		file.Close()
		return nil, nil, errPathChanged
	}
	return file, got, nil
}

// denySet is a deny list (see insideAny) ready to compare files with: its
// directory and file entries as files, its name entries as their
// directory, as a file, and the name; and the entries that were not there
// when it was looked up by their path (missingEntry).
type denySet struct {
	files   []os.FileInfo
	names   []nameEntry
	missing []missingEntry
}

type nameEntry struct {
	dir  os.FileInfo
	name string
}

// missingEntry is an entry that was not there when the list was looked up,
// or a name entry whose directory was not: the deepest directory on its
// path that was there (dir, as a file, so that a link on the way or
// another spelling of it is the same directory) and the elements of the
// path below it (tail), compared with a path's elements as a
// case-insensitive file system compares names (sameName); a name
// entry's name too, which an element right below the tail must contain.
// What a revision keeps or a file event names is compared with it whether
// or not the working tree has it: a repository's history can hold what the
// list names, removed from the working tree since.
type missingEntry struct {
	dir  os.FileInfo
	tail []string
	name string
}

// maxMissingLinks bounds the links to nothing yet followed while a missing
// entry is looked up, as the system bounds a path's links (ELOOP).
const maxMissingLinks = 40

// newDenySet looks up deny's entries. One that is not there, or whose
// directory is not, is kept by its path (missingEntry), walked as the
// system walks it (appendMissing).
func newDenySet(deny []string) denySet {
	var d denySet
	for _, e := range deny {
		p, name := e, ""
		if n, ok := nameOf(filepath.Base(e)); ok {
			p, name = filepath.Dir(e), n
		}
		if fi, err := os.Stat(p); err == nil {
			if name != "" {
				d.names = append(d.names, nameEntry{fi, name})
			} else {
				d.files = append(d.files, fi)
			}
			continue
		}
		abs := p
		if !filepath.IsAbs(abs) {
			wd, err := os.Getwd()
			if err != nil {
				continue
			}
			abs = wd + string(filepath.Separator) + p
		}
		links := 0
		d.missing = appendMissing(d.missing, abs, name, &links)
	}
	return d
}

// appendMissing appends to list the missing entries for p, an absolute
// path as given (not cleaned) that is not there, with name, a name
// entry's. p is walked as the system walks it: each element from the
// directory reached so far, a link that leads somewhere followed to where
// it leads before a ".." after it is taken, so that the entry is kept below
// the directory the path really reaches, with the elements below it that
// are not there. A link to nothing yet on the way keeps the path spelt
// through it (a request's path does not follow it either: evalExisting)
// and, from its target on, the path it would lead to once its target is
// made; *links counts the links followed so (maxMissingLinks).
func appendMissing(list []missingEntry, p, name string, links *int) []missingEntry {
	sep := string(filepath.Separator)
	parts := strings.Split(p, sep)
	cur := sep
	for i, el := range parts {
		switch el {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, el)
		fi, err := os.Lstat(next)
		if err != nil {
			return appendTail(list, cur, parts[i:], name)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		if real, err := filepath.EvalSymlinks(next); err == nil {
			cur = real
			continue
		}
		list = appendTail(list, cur, parts[i:], name)
		if *links >= maxMissingLinks {
			return list
		}
		*links++
		target, err := os.Readlink(next)
		if err != nil {
			return list
		}
		if !filepath.IsAbs(target) {
			target = cur + sep + target
		}
		return appendMissing(list, target+sep+strings.Join(parts[i+1:], sep), name, links)
	}
	return appendTail(list, cur, nil, name)
}

// appendTail appends to list the missing entry for rest below cur, a
// directory reached with its links resolved: the deepest directory of
// that path that is there, compared as a file, and the elements below it.
// rest has nothing on it to follow, so it is taken lexically.
func appendTail(list []missingEntry, cur string, rest []string, name string) []missingEntry {
	var tail []string
	for q := filepath.Join(append([]string{cur}, rest...)...); ; q = filepath.Dir(q) {
		if fi, err := os.Stat(q); err == nil {
			slices.Reverse(tail)
			return append(list, missingEntry{fi, tail, name})
		}
		if filepath.Dir(q) == q {
			return list
		}
		tail = append(tail, filepath.Base(q))
	}
}

// and is d with o's entries too.
func (d denySet) and(o denySet) denySet {
	return denySet{slices.Concat(d.files, o.files), slices.Concat(d.names, o.names), slices.Concat(d.missing, o.missing)}
}

func (d denySet) empty() bool {
	return len(d.files) == 0 && len(d.names) == 0 && len(d.missing) == 0
}

// hit reports whether the file fi (nil for a name nothing has yet), named
// name in the directory dir, is denied: one of the entries itself, compared
// as a file, a name a name entry for dir matches, or an entry missing at
// the lookup that names name in dir and has been made since.
func (d denySet) hit(fi os.FileInfo, name string, dir os.FileInfo) bool {
	if fi != nil {
		for _, e := range d.files {
			if os.SameFile(fi, e) {
				return true
			}
		}
	}
	if dir == nil {
		return false
	}
	for _, n := range d.names {
		if containsFold(name, n.name) && os.SameFile(dir, n.dir) {
			return true
		}
	}
	for _, m := range d.missing {
		if m.name == "" && len(m.tail) == 1 && sameName(name, m.tail[0]) && os.SameFile(dir, m.dir) {
			return true
		}
	}
	return false
}

// below reports whether real, an absolute path whose part that is there has
// its links resolved, is one of the missing entries or inside one, given
// one of its parents (or itself), at, and at as a file.
func (d denySet) below(real, at string, atID os.FileInfo) bool {
	var rest []string
	split := false
	for _, m := range d.missing {
		if !os.SameFile(atID, m.dir) {
			continue
		}
		if !split {
			split = true
			rel, err := filepath.Rel(at, real)
			if err != nil {
				return false
			}
			if rel != "." {
				rest = strings.Split(rel, string(filepath.Separator))
			}
		}
		if m.names(rest) {
			return true
		}
	}
	return false
}

// names reports whether rest, the elements of a path below the entry's
// directory, are the entry's or start with them.
func (m missingEntry) names(rest []string) bool {
	n := len(m.tail)
	if m.name != "" {
		n++
	}
	if len(rest) < n {
		return false
	}
	for j, el := range m.tail {
		if !sameName(rest[j], el) {
			return false
		}
	}
	return m.name == "" || containsFold(rest[len(m.tail)], m.name)
}
