package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

type doc struct {
	N int `json:"n"`
}

func TestOpenCreatesDirAndRejectsUnwritable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "conductor.d")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v %v", fi, err)
	}
	if s.Dir() != dir {
		t.Fatalf("Dir %q", s.Dir())
	}
	if _, err := Open(filepath.Join(os.DevNull, "x")); err == nil {
		t.Fatal("expected error for unwritable parent")
	}
}

func TestLoadMissingSaveRoundTrip(t *testing.T) {
	s, _ := Open(t.TempDir())
	var d doc
	if ok, err := s.Load("catalog.json", &d); ok || err != nil {
		t.Fatalf("missing: %v %v", ok, err)
	}
	if err := s.Save("catalog.json", doc{N: 7}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Load("catalog.json", &d); !ok || err != nil || d.N != 7 {
		t.Fatalf("round trip: %v %v %+v", ok, err, d)
	}
	fi, _ := os.Stat(filepath.Join(s.Dir(), "catalog.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v", fi.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(s.Dir(), "*.tmp")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestSaveRejectsBadNames(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, name := range []string{"../x.json", "a/b.json", "x.txt", "", "X.json"} {
		if err := s.Save(name, doc{}); err == nil {
			t.Fatalf("%q accepted", name)
		}
	}
}

// A directory the server user cannot write is refused at Open, not at the
// first save: the write probe catches it.
func TestOpenRefusesADirectoryItCannotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory of mode 0500")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "is not writable") {
		t.Fatalf("Open of a 0500 directory: %v", err)
	}
}

func TestConcurrentSavesLeaveValidJSON(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Save("c.json", doc{N: -1}); err != nil {
		t.Fatal(err)
	}
	// A reader the whole time: it never finds the file torn or missing.
	stop := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		defer close(readErr)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var d doc
			if ok, err := s.Load("c.json", &d); !ok || err != nil {
				readErr <- fmt.Errorf("load during the saves: ok=%v err=%v", ok, err)
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _ = s.Save("c.json", doc{N: i}) }(i)
	}
	wg.Wait()
	close(stop)
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(), "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d doc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("torn file: %v %q", err, b)
	}
}

func TestLoadReportsBadNamesAndCorruptFiles(t *testing.T) {
	s, _ := Open(t.TempDir())
	var d doc
	if _, err := s.Load("../x.json", &d); err == nil {
		t.Fatal("bad name accepted by Load")
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Load("bad.json", &d); ok || err == nil {
		t.Fatalf("corrupt file: ok=%v err=%v", ok, err)
	}
}

func TestFailedSaveKeepsPreviousDocument(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Save("c.json", doc{N: 1}); err != nil {
		t.Fatal(err)
	}
	// An encode error stops the save before anything is written.
	if err := s.Save("c.json", make(chan int)); err == nil {
		t.Fatal("expected an encode error")
	}
	// A rename that fails stops it after the temp file is written in full.
	refused := errors.New("rename refused")
	rename = func(string, string) error { return refused }
	t.Cleanup(func() { rename = os.Rename })
	if err := s.Save("c.json", doc{N: 2}); !errors.Is(err, refused) {
		t.Fatalf("Save with a failing rename: %v", err)
	}
	var d doc
	if ok, err := s.Load("c.json", &d); !ok || err != nil || d.N != 1 {
		t.Fatalf("previous document lost: %v %v %+v", ok, err, d)
	}
	if left, _ := filepath.Glob(filepath.Join(s.Dir(), "*.tmp")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

// Patterns and snippets are written as typed: <, > and & are not escaped, so
// a hand-edited catalog.json reads like what the Agents page shows.
func TestSaveWritesPatternsAsTyped(t *testing.T) {
	s, _ := Open(t.TempDir())
	type sig struct {
		Pattern string `json:"pattern"`
	}
	if err := s.Save("c.json", sig{Pattern: `^<a & b>$`}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir(), "c.json"))
	if want := "{\n  \"pattern\": \"^<a & b>$\"\n}\n"; string(b) != want {
		t.Fatalf("file %q, want %q", b, want)
	}
	if enc, err := Encode(sig{Pattern: `^<a & b>$`}); err != nil || string(enc) != string(b) {
		t.Fatalf("Encode %q %v, want what Save wrote", enc, err)
	}
}

// writeRaw puts a hand-written file into the store directory, as a user editing
// the data directory would.
func writeRaw(t *testing.T, s *Store, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Dir(), name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A typo in a hand-edited document must fail the load, not be dropped silently,
// at the top level and inside list elements (the catalog overlay is a list).
func TestLoadRejectsUnknownFields(t *testing.T) {
	type item struct {
		N int `json:"n"`
	}
	type list struct {
		Items []item `json:"items"`
	}
	s, _ := Open(t.TempDir())
	for _, tc := range []struct{ name, body, field string }{
		{"top level", `{"items":[],"extra":1}`, "extra"},
		{"inside a list element", `{"items":[{"n":1},{"n":2,"nn":3}]}`, "nn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeRaw(t, s, "c.json", tc.body)
			var l list
			ok, err := s.Load("c.json", &l)
			if ok || err == nil {
				t.Fatalf("accepted: ok=%v err=%v %+v", ok, err, l)
			}
			if msg := err.Error(); !strings.Contains(msg, "c.json") || !strings.Contains(msg, tc.field) {
				t.Fatalf("error should name the file and the field %q: %v", tc.field, err)
			}
		})
	}
}

// Decoder.Decode stops after the first value, so Load has to insist on the end
// of input itself; json.Unmarshal used to do that for free.
func TestLoadRejectsTrailingData(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, tc := range []struct{ name, body string }{
		{"text", `{"n":1} oops`},
		{"stray brace", `{"n":1}}`},
		{"second value", `{"n":1}{"n":2}`},
		{"second value on a new line", "{\"n\":1}\n{\"n\":2}\n"},
		{"truncated second value", `{"n":1}{"n":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeRaw(t, s, "c.json", tc.body)
			var d doc
			ok, err := s.Load("c.json", &d)
			if ok || err == nil {
				t.Fatalf("accepted: ok=%v err=%v %+v", ok, err, d)
			}
			if !strings.Contains(err.Error(), "c.json") {
				t.Fatalf("error should name the file: %v", err)
			}
		})
	}

	// Whitespace after the value is fine: Save ends its files with a newline.
	writeRaw(t, s, "c.json", "{\"n\":5}\n\n \t\r\n")
	var d doc
	if ok, err := s.Load("c.json", &d); !ok || err != nil || d.N != 5 {
		t.Fatalf("trailing whitespace: %v %v %+v", ok, err, d)
	}
}

// An empty or cut-off file is damage, never an empty document: callers must not
// be able to mistake it for "nothing stored yet" and overwrite it on the next Save.
func TestLoadRejectsEmptyAndTruncatedFiles(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, tc := range []struct{ name, body, want string }{
		{"empty file", "", "empty document"},
		{"only whitespace", " \n\t\n", "empty document"},
		{"truncated value", `{"n":`, "unexpected EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeRaw(t, s, "c.json", tc.body)
			var d doc
			ok, err := s.Load("c.json", &d)
			if ok || err == nil {
				t.Fatalf("accepted: ok=%v err=%v %+v", ok, err, d)
			}
			if msg := err.Error(); !strings.Contains(msg, "c.json") || !strings.Contains(msg, tc.want) {
				t.Fatalf("error should name the file and say %q: %v", tc.want, err)
			}
			var pe *ParseError
			if !errors.As(err, &pe) || pe.Name != "c.json" {
				t.Fatalf("a document that does not decode is no ParseError: %v", err)
			}
			if _, err := s.LoadLimit("c.json", &d, 64); !errors.As(err, &pe) {
				t.Fatalf("LoadLimit: a document that does not decode is no ParseError: %v", err)
			}
		})
	}
}

// A sub-store is a directory of the store, made 0700; it lists its documents,
// and only them, by name, and deletes them.
func TestSubListAndDelete(t *testing.T) {
	s, _ := Open(t.TempDir())
	sub, err := s.Sub("crews")
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(sub.Dir()); err != nil || fi.Mode().Perm() != 0o700 || sub.Dir() != filepath.Join(s.Dir(), "crews") {
		t.Fatalf("sub dir %s: %v %v", sub.Dir(), fi, err)
	}
	for _, bad := range []string{"", "../x", "a/b", "X"} {
		if _, err := s.Sub(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if list, err := sub.List(); err != nil || len(list) != 0 {
		t.Fatalf("empty: %v %v", list, err)
	}
	for _, name := range []string{"b.json", "a.json"} {
		if err := sub.Save(name, doc{N: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// Not documents: a temp file, the probe, an upper-case name, a link, a directory.
	writeRaw(t, sub, ".probe-1", "")
	writeRaw(t, sub, "c.json.123.tmp", "{}")
	writeRaw(t, sub, "Upper.json", "{}")
	if err := os.Symlink(filepath.Join(sub.Dir(), "a.json"), filepath.Join(sub.Dir(), "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sub.Dir(), "dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	list, err := sub.List()
	var names []string
	for _, e := range list {
		names = append(names, e.Name)
	}
	if err != nil || !slices.Equal(names, []string{"a.json", "b.json"}) || list[0].Size == 0 || list[0].ModTime.IsZero() {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := sub.Delete("a.json"); err != nil {
		t.Fatal(err)
	}
	if err := sub.Delete("a.json"); err != nil {
		t.Fatalf("deleting a missing document: %v", err)
	}
	if err := sub.Delete("../x.json"); err == nil {
		t.Fatal("a bad name was deleted")
	}
	if list, _ := sub.List(); len(list) != 1 || list[0].Name != "b.json" {
		t.Fatalf("after delete: %+v", list)
	}
}

// LoadLimit reads a document of at most limit bytes, and refuses a larger one
// without reading past the limit.
func TestLoadLimit(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Save("c.json", doc{N: 12345}); err != nil { // 17 bytes
		t.Fatal(err)
	}
	var d doc
	if ok, err := s.LoadLimit("c.json", &d, 64); !ok || err != nil || d.N != 12345 {
		t.Fatalf("within: %v %v %+v", ok, err, d)
	}
	var pe *ParseError
	if ok, err := s.LoadLimit("c.json", &d, 8); ok || err == nil || !strings.Contains(err.Error(), "more than 8 bytes") || errors.As(err, &pe) {
		t.Fatalf("over: %v %v", ok, err)
	}
	if ok, err := s.LoadLimit("gone.json", &d, 8); ok || err != nil {
		t.Fatalf("missing: %v %v", ok, err)
	}
	writeRaw(t, s, "bad.json", `{"n":1} x`)
	if _, err := s.LoadLimit("bad.json", &d, 64); err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Fatalf("trailing data: %v", err)
	}
	// A link is refused, never read through, as List leaves links out.
	if err := os.Symlink(filepath.Join(s.Dir(), "c.json"), filepath.Join(s.Dir(), "link.json")); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.LoadLimit("link.json", &d, 64); ok || err == nil || !strings.Contains(err.Error(), "symbolic link") || errors.As(err, &pe) {
		t.Fatalf("a link: %v %v", ok, err)
	}
}

// A plain file is written as a document is: mode 0600 through a temp file,
// replaced in full; RemoveFile takes it away, and a missing one is no error.
func TestWriteAndRemoveAPlainFile(t *testing.T) {
	s, _ := Open(t.TempDir())
	path := filepath.Join(s.Dir(), "plain-file")
	for _, body := range []string{"first contents\n", "second\n"} {
		if err := s.WriteFile("plain-file", []byte(body)); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil || string(b) != body {
			t.Fatalf("contents %q %v, want %q", b, err, body)
		}
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v %v", fi.Mode(), err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(s.Dir(), "*.tmp")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
	// A plain file is no document.
	if docs, err := s.List(); err != nil || len(docs) != 0 {
		t.Fatalf("List: %v %v", docs, err)
	}
	if err := s.RemoveFile("plain-file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("still there: %v", err)
	}
	if err := s.RemoveFile("plain-file"); err != nil {
		t.Fatalf("a missing file: %v", err)
	}
	for _, bad := range []string{"", "a.json", "../x", "A", "a/b", ".hidden"} {
		if err := s.WriteFile(bad, nil); err == nil {
			t.Errorf("WriteFile(%q) accepted", bad)
		}
		if err := s.RemoveFile(bad); err == nil {
			t.Errorf("RemoveFile(%q) accepted", bad)
		}
	}
	// A sub-store's directory is not a plain file.
	if _, err := s.Sub("sub"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFile("sub"); err == nil {
		t.Fatal("RemoveFile removed a directory")
	}
}

// A failed write keeps the previous contents and leaves no temp file.
func TestFailedWriteFileKeepsThePreviousContents(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.WriteFile("plain-file", []byte("old")); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("rename refused")
	rename = func(string, string) error { return refused }
	t.Cleanup(func() { rename = os.Rename })
	if err := s.WriteFile("plain-file", []byte("new")); !errors.Is(err, refused) {
		t.Fatalf("WriteFile with a failing rename: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(s.Dir(), "plain-file")); err != nil || string(b) != "old" {
		t.Fatalf("previous contents lost: %q %v", b, err)
	}
	if left, _ := filepath.Glob(filepath.Join(s.Dir(), "*.tmp")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
