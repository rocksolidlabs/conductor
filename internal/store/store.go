// Package store persists small JSON documents in the server data directory:
// a directory of JSON documents, which may hold directories of its own (Sub),
// and a few plain files (WriteFile). Every write goes to a temp file first and is renamed into place so readers
// never see a torn file; a mutex serialises writers in this process.
//
// An observer (Observe) is told every commit of the data directory, through
// the store or any Sub of it, after its rename or remove and in that order:
// the document's path and a sequence number, never its bytes, so a plain
// file's contents (the workbench token's) never leave this package. The
// sync seam reads an allowlisted document when it delivers it (OpenDoc).
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// namePattern limits document names to a flat, lowercase file name so a caller
// can never address a path outside the data directory.
var namePattern = regexp.MustCompile(`^[a-z0-9-]+\.json$`)

// Store is a directory of JSON documents such as catalog.json. It may hold
// directories of its own, each a Store (Sub), such as crews/ with one document
// per crew.
type Store struct {
	dir string
	mu  sync.Mutex
	// prefix is the store's slash path from the store Open made, "" for
	// that one and "crews/" for its Sub("crews"): what an Event's path
	// begins with. obs is the observer record the store shares with the
	// store Open made and every Sub of it.
	prefix string
	obs    *observer
}

// Event is one commit of the data directory: a document or plain file
// saved (renamed into place) or deleted, by its slash path from the
// directory Open was given ("catalog.json", "crews/todo-app.json"), and its
// number in commit order (Seq, from 1 for each Open). It carries no bytes.
type Event struct {
	Op   string // OpSaved or OpDeleted
	Path string
	Seq  int64
}

// The ops of an Event.
const (
	OpSaved   = "saved"
	OpDeleted = "deleted"
)

// observer is what a store and its Subs share: one mutex taken around
// every rename and remove, so the commits of one directory tree, whichever
// store makes them, are numbered and told in the order they land, the
// sequence, and the function Observe gave.
type observer struct {
	mu  sync.Mutex
	seq int64
	fn  func(Event)
}

// Observe gives the store, and every Sub of it made before or after, fn:
// called once after each commit (a save's rename, a delete's remove) that
// succeeded, never after a failed one, with the commit's Event, under the
// mutex that orders the commits. fn must only queue what it is told: it
// holds every writer of the directory tree while it runs, and it must not
// call the store. A later Observe replaces fn; nil removes it.
func (s *Store) Observe(fn func(Event)) {
	s.obs.mu.Lock()
	defer s.obs.mu.Unlock()
	s.obs.fn = fn
}

// Seq answers the number of the last commit told so far (0 for none):
// every commit after it has a greater one.
func (s *Store) Seq() int64 {
	s.obs.mu.Lock()
	defer s.obs.mu.Unlock()
	return s.obs.seq
}

// commit runs do, a write's last step (its rename or remove), under the
// observer's mutex and, when do answers that it changed the directory,
// numbers the commit and tells the observer before letting the mutex go,
// so commits are told in the order they land. The caller holds s.mu.
func (s *Store) commit(name, op string, do func() (bool, error)) error {
	path := s.prefix + name
	if f := beforeCommit; f != nil {
		f(path)
	}
	o := s.obs
	o.mu.Lock()
	defer o.mu.Unlock()
	changed, err := do()
	if err != nil || !changed {
		return err
	}
	if f := afterCommit; f != nil {
		f(path)
	}
	o.seq++
	if o.fn != nil {
		o.fn(Event{Op: op, Path: path, Seq: o.seq})
	}
	return nil
}

// beforeCommit and afterCommit, when a test sets them, run in every
// commit with its path: before the observer's mutex is taken, and once
// the rename or remove has landed and before it is numbered and told.
// nil outside tests.
var beforeCommit, afterCommit func(path string)

// Open creates dir with mode 0700 when it is missing and fails when it is not
// writable, so a misconfigured data directory is caught at startup.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("store: empty directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: create %s: %w", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return nil, fmt.Errorf("store: %s is not writable: %w", dir, err)
	}
	probe.Close()
	os.Remove(probe.Name())
	return &Store{dir: dir, obs: &observer{}}, nil
}

// Dir returns the directory the store reads from and writes to.
func (s *Store) Dir() string { return s.dir }

// subPattern limits a sub-store's name to a flat, lower-case directory name.
var subPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// Sub opens the directory name inside s as a store of its own, made 0700 when
// missing and refused when it cannot be written, as Open does. It shares
// s's observer (Observe), its events' paths beginning with name and a slash.
func (s *Store) Sub(name string) (*Store, error) {
	if !subPattern.MatchString(name) {
		return nil, fmt.Errorf("store: bad name %q", name)
	}
	sub, err := Open(filepath.Join(s.dir, name))
	if err != nil {
		return nil, err
	}
	sub.prefix, sub.obs = s.prefix+name+"/", s.obs
	return sub, nil
}

// Entry is a document in a store, as List reports it.
type Entry struct {
	Name    string // the document's name, such as "todo-app.json"
	Size    int64
	ModTime time.Time
}

// List returns the documents in the store, sorted by name: the regular files
// whose names a document may have. Temp files, the write probe, links,
// directories and anything else are left out.
func (s *Store) List() ([]Entry, error) { return listDir(s.dir) }

// ListDocs is List for the store's directory sub ("" for the store's own),
// as Sub would open it, without making it: a directory that is not there
// holds none.
func (s *Store) ListDocs(sub string) ([]Entry, error) {
	dir := s.dir
	if sub != "" {
		if !subPattern.MatchString(sub) {
			return nil, fmt.Errorf("store: bad name %q", sub)
		}
		dir = filepath.Join(s.dir, sub)
	}
	out, err := listDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return out, err
}

// listDir is List of dir.
func listDir(dir string) ([]Entry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		if !de.Type().IsRegular() || !namePattern.MatchString(de.Name()) {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue // removed meanwhile
		}
		out = append(out, Entry{Name: de.Name(), Size: fi.Size(), ModTime: fi.ModTime()})
	}
	return out, nil
}

// Delete removes the document called name. One that is not there is no error.
func (s *Store) Delete(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("store: bad name %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit(name, OpDeleted, func() (bool, error) {
		err := os.Remove(filepath.Join(s.dir, name))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	})
}

// Load decodes the document called name into v. It reports false, with no
// error, only when the document does not exist yet.
//
// Decoding is strict (DecodeStrict), as it is for the config file and the
// catalog file: an unknown field, an empty file or anything after the JSON
// value is an error, so a typo in a hand-edited file is caught instead of
// silently dropped. A load error is a real error: callers must surface it and
// never treat the document as empty, or their next Save would overwrite what
// is on disk. v may be partly filled after an error.
func (s *Store) Load(name string, v any) (bool, error) {
	if !namePattern.MatchString(name) {
		return false, fmt.Errorf("store: bad name %q", name)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := DecodeStrict(b, v); err != nil {
		return false, &ParseError{Name: name, Err: err}
	}
	return true, nil
}

// ParseError is a load's error for a document that was read whole but
// does not decode: empty, cut short, not JSON, or not v's shape. Any other
// load error is the file system's (or a bound's), so a caller can tell a
// damaged document from a disk that failed.
type ParseError struct {
	Name string
	Err  error
}

func (e *ParseError) Error() string { return "store: parse " + e.Name + ": " + e.Err.Error() }
func (e *ParseError) Unwrap() error { return e.Err }

// ErrChanged is what LoadLimit's error wraps when the file it opened is not
// the one it checked: another was renamed into its place in between, as a save
// does. It passes: a caller may read again.
var ErrChanged = errors.New("changed while it was opened")

// ErrTooLarge says a file is over the limit a bounded load was given.
var ErrTooLarge = errors.New("too large")

// LoadLimit is Load for a document of at most limit bytes. A larger one is an
// error that says so, and is not read past the limit. Only a regular file is
// read: a symbolic link, which List leaves out too, is refused rather than
// followed (Lstat), and so is anything else in the document's place.
func (s *Store) LoadLimit(name string, v any, limit int64) (bool, error) {
	if !namePattern.MatchString(name) {
		return false, fmt.Errorf("store: bad name %q", name)
	}
	path := filepath.Join(s.dir, name)
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		what := "not a regular file"
		if fi.Mode()&os.ModeSymlink != 0 {
			what = "a symbolic link, which is not followed"
		}
		return false, fmt.Errorf("store: %s is %s", name, what)
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	// The file opened is the one checked: a link put in its place meanwhile
	// is refused too.
	ofi, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(fi, ofi) {
		return false, fmt.Errorf("store: %s %w", name, ErrChanged)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return false, err
	}
	if int64(len(b)) > limit {
		return false, fmt.Errorf("store: %s is more than %d bytes: %w", name, limit, ErrTooLarge)
	}
	if err := DecodeStrict(b, v); err != nil {
		return false, &ParseError{Name: name, Err: err}
	}
	return true, nil
}

// DecodeStrict decodes exactly one JSON value from b into v. Unknown fields
// are errors, and so are an empty b and anything but whitespace after the
// value: unlike json.Unmarshal, Decoder.Decode stops after the first value
// and would accept the rest. Every reader of a JSON file Conductor is given
// decodes with it: the store, the config file and the catalog file.
func DecodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) { // Decode only reports a bare EOF when there is no value at all
			return errors.New("empty document")
		}
		return err
	}
	var extra json.RawMessage
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		return nil
	case err == nil:
		return errors.New("more than one JSON value")
	default:
		return fmt.Errorf("unexpected data after the JSON value: %w", err)
	}
}

// rename is os.Rename: the last step of a save. A test replaces it to make a
// save fail after the temp file is written.
var rename = os.Rename

// Encode is v as Save writes it: indented with two spaces, ending in a
// newline, with <, > and & as they are, so patterns and snippets stay
// hand-editable.
func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Save writes v as Encode writes it to a temp file in the data directory and
// renames it over name (mode 0600), so a crash leaves either the old or the new
// document, never a partial one.
func (s *Store) Save(name string, v any) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("store: bad name %q", name)
	}
	b, err := Encode(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(name, b)
}

// filePattern limits the name of a plain file, one that is no JSON document
// (WriteFile), to a flat, lower-case name with no extension.
var filePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// WriteFile writes b to the plain file called name as Save writes a document:
// to a temp file of mode 0600, synced, renamed over name.
func (s *Store) WriteFile(name string, b []byte) error {
	if !filePattern.MatchString(name) {
		return fmt.Errorf("store: bad name %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(name, b)
}

// RemoveFile removes the plain file called name. One that is not there is no
// error; a directory there (a Sub store) is refused.
func (s *Store) RemoveFile(name string) error {
	if !filePattern.MatchString(name) {
		return fmt.Errorf("store: bad name %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, name)
	return s.commit(name, OpDeleted, func() (bool, error) {
		fi, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if fi.IsDir() {
			return false, fmt.Errorf("store: %s is a directory", path)
		}
		err = os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	})
}

// write puts b in place of name through a temp file of mode 0600 in the
// store's directory, synced before the rename, so a crash leaves the old
// contents or the new, never a part. The temp file is written outside the
// observer's mutex and the rename made under it (commit). The caller holds
// s.mu.
func (s *Store) write(name string, b []byte) error {
	final := filepath.Join(s.dir, name)
	tmp, err := os.CreateTemp(s.dir, name+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return s.commit(name, OpSaved, func() (bool, error) {
		if err := rename(tmp.Name(), final); err != nil {
			return false, err
		}
		return true, nil
	})
}

// docPattern is a document's slash path in the store: directories a Sub
// may have, then a document's name.
var docPattern = regexp.MustCompile(`^([a-z0-9-]+/)*[a-z0-9-]+\.json$`)

// OpenDoc opens the document at path, a slash path in the store ("catalog.json",
// "runs/r-1.json"), read-only through the store's directory as a root, so
// nothing outside it is reached: a regular JSON document only. A plain
// file, a symbolic link or anything else in its place is refused; a
// document that is not there is an error that wraps os.ErrNotExist. The
// file opened is one version of the document, whatever is renamed over it
// meanwhile (a save), and the caller closes it.
func (s *Store) OpenDoc(path string) (*os.File, error) {
	if !docPattern.MatchString(path) {
		return nil, fmt.Errorf("store: bad document path %q", path)
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel := filepath.FromSlash(path)
	fi, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("store: %s is not a regular file", path)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	ofi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !os.SameFile(fi, ofi) {
		f.Close()
		return nil, fmt.Errorf("store: %s %w", path, ErrChanged)
	}
	return f, nil
}
