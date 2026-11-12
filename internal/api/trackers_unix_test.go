//go:build unix

package api

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// The read refuses, naming only the file, everything that is not the
// private regular file it keeps in the private directory it keeps, and
// never waits on what is at either name: one table, a row per refusal.
// Every step after the directory's open goes through what was opened:
// between that open and the file's (afterDirOpen), the directory swapped
// for a named pipe or for a link to a directory of another mode is not
// followed and makes nothing wait, the file's name replaced by a link to
// the same file is refused, a rename is read through (the new file
// answered), and a write there puts nothing in the link's directory.
func TestTrackerReadRefuses(t *testing.T) {
	const secret = "ghp_PLANTEDinTHEfileTOKEN0123456789abWXYZ"
	savedAt := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	valid := func(host string) []byte {
		b, err := store.Encode(trackerWire{GitHub: []trackerWireEntry{{Host: host, Token: secret, Login: "mona", AccountID: "1", SavedAt: savedAt}}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// fresh is a keep over a new data directory with credentials/ at 0700.
	fresh := func(t *testing.T) (*trackerKeep, string) {
		t.Helper()
		dir := t.TempDir()
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		creds := filepath.Join(dir, trackerDir)
		if err := os.Mkdir(creds, 0o700); err != nil {
			t.Fatal(err)
		}
		return newTrackers(&config.Config{}, st, slog.New(slog.DiscardHandler)), creds
	}
	put := func(t *testing.T, path string, b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// replace renames a new file of b over path, as a save does.
	replace := func(t *testing.T, path string, b []byte) {
		t.Helper()
		put(t, path+".new", b)
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
	}
	// readBounded is k.read in a goroutine of its own, failing by a bound
	// of its own when it waits.
	readBounded := func(t *testing.T, k *trackerKeep, unblock func()) (trackerFile, error) {
		t.Helper()
		type answer struct {
			f   trackerFile
			err error
		}
		done := make(chan answer, 1)
		go func() {
			f, err := k.read()
			done <- answer{f, err}
		}()
		select {
		case a := <-done:
			return a.f, a.err
		case <-time.After(testWait):
			if unblock != nil {
				unblock()
			}
			select {
			case <-done:
			case <-time.After(testWait):
			}
			t.Fatalf("the read waited %s on what is at a name", testWait)
			return trackerFile{}, nil
		}
	}
	// unblockPipe opens a named pipe for writing, which lets a read that
	// waits on it go on.
	unblockPipe := func(path string) func() {
		return func() {
			if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				w.Close()
			}
		}
	}
	for _, c := range []struct {
		name  string
		set   func(t *testing.T, creds, file string) func()
		words string
	}{
		{"a link", func(t *testing.T, creds, file string) func() {
			put(t, filepath.Join(creds, "real.json"), valid("github.com"))
			return nil
		}, "is not a regular file"},
		{"a named pipe", func(t *testing.T, creds, file string) func() {
			if err := syscall.Mkfifo(file, 0o600); err != nil {
				t.Fatal(err)
			}
			return unblockPipe(file)
		}, "is not a regular file"},
		{"a directory", func(t *testing.T, creds, file string) func() {
			if err := os.Mkdir(file, 0o700); err != nil {
				t.Fatal(err)
			}
			return nil
		}, "is not a regular file"},
		{"credentials a file", func(t *testing.T, creds, file string) func() {
			if err := os.Remove(creds); err != nil {
				t.Fatal(err)
			}
			put(t, creds, valid("github.com"))
			return nil
		}, "is not a directory"},
		{"credentials a link", func(t *testing.T, creds, file string) func() {
			real := creds + "-real"
			if err := os.Rename(creds, real); err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(real, githubFile), valid("github.com"))
			if err := os.Symlink(real, creds); err != nil {
				t.Fatal(err)
			}
			return nil
		}, "is not a directory"},
		{"credentials a named pipe", func(t *testing.T, creds, file string) func() {
			if err := os.Remove(creds); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(creds, 0o600); err != nil {
				t.Fatal(err)
			}
			return unblockPipe(creds)
		}, "is not a directory"},
		{"one byte past 16 KiB", func(t *testing.T, creds, file string) func() {
			b := valid("github.com")
			put(t, file, append(b, bytes.Repeat([]byte(" "), maxTrackerFile+1-len(b))...))
			return nil
		}, "is more than 16384 bytes"},
		{"a host's entry twice", func(t *testing.T, creds, file string) func() {
			b, _ := store.Encode(trackerWire{GitHub: []trackerWireEntry{{Host: "github.com", Token: "a", Login: "mona", AccountID: "1", SavedAt: savedAt}, {Host: "github.com", Token: "b", Login: "mona", AccountID: "1", SavedAt: savedAt}}})
			put(t, file, b)
			return nil
		}, "holds a host's entry twice"},
		{"an unknown member", func(t *testing.T, creds, file string) func() {
			put(t, file, []byte(`{"github":[],"`+secret+`":1}`))
			return nil
		}, "cannot be read as a token file"},
		{"a file that does not decode", func(t *testing.T, creds, file string) func() {
			put(t, file, []byte(`{"github":[{"host":"github.com","token":"`+secret+"\x01"))
			return nil
		}, "cannot be read as a token file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			k, creds := fresh(t)
			file := filepath.Join(creds, githubFile)
			unblock := c.set(t, creds, file)
			if c.name == "a link" {
				if err := os.Symlink("real.json", file); err != nil {
					t.Fatal(err)
				}
			}
			_, err := readBounded(t, k, unblock)
			if err == nil || !strings.Contains(err.Error(), c.words) || strings.Contains(err.Error(), secret[4:16]) {
				t.Fatalf("%v, not %q", err, c.words)
			}
		})
	}
	// Each bit of the group's and the others' on the directory (0700) and
	// on the file (0600).
	for _, bit := range []os.FileMode{0o040, 0o020, 0o010, 0o004, 0o002, 0o001} {
		for _, which := range []string{"directory", "file"} {
			mode := 0o700 | bit
			if which == "file" {
				mode = 0o600 | bit
			}
			t.Run(fmt.Sprintf("the %s at %04o", which, mode), func(t *testing.T) {
				k, creds := fresh(t)
				file := filepath.Join(creds, githubFile)
				put(t, file, valid("github.com"))
				path := creds
				if which == "file" {
					path = file
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
				if _, err := readBounded(t, k, nil); err == nil || !strings.Contains(err.Error(), "open to its group or others") {
					t.Fatalf("%04o: %v", mode, err)
				}
			})
		}
	}
	t.Run("16 KiB", func(t *testing.T) {
		k, creds := fresh(t)
		b := valid("github.com")
		put(t, filepath.Join(creds, githubFile), append(b, bytes.Repeat([]byte(" "), maxTrackerFile-len(b))...))
		if f, err := readBounded(t, k, nil); err != nil || len(f.GitHub) != 1 {
			t.Fatalf("a file of 16 KiB: %v %v", f, err)
		}
	})

	// Between the directory's open and the file's, through the seam: once.
	between := func(t *testing.T, k *trackerKeep, step func(must func(error))) {
		n := 0
		must := func(err error) {
			if err != nil {
				t.Error(err)
			}
		}
		k.afterDirOpen = func() {
			if n++; n == 1 {
				step(must)
			}
		}
	}
	t.Run("a rename between the directory's open and the file's", func(t *testing.T) {
		k, creds := fresh(t)
		file := filepath.Join(creds, githubFile)
		put(t, file, valid("github.com"))
		between(t, k, func(func(error)) { replace(t, file, valid(gheHost)) })
		if f, err := readBounded(t, k, nil); err != nil || len(f.GitHub) != 1 || f.GitHub[0].Host != gheHost {
			t.Fatalf("the read across a rename: %v %v", f, err)
		}
	})
	for _, swap := range []string{"a link", "a named pipe", "a link to the same file"} {
		t.Run(swap+" put at the file's name there", func(t *testing.T) {
			k, creds := fresh(t)
			file := filepath.Join(creds, githubFile)
			put(t, file, valid("github.com"))
			put(t, filepath.Join(creds, "other.json"), valid(gheHost))
			between(t, k, func(must func(error)) {
				switch swap {
				case "a link":
					must(os.Remove(file))
					must(os.Symlink("other.json", file))
				case "a named pipe":
					must(os.Remove(file))
					must(syscall.Mkfifo(file, 0o600))
				default:
					must(os.Rename(file, filepath.Join(creds, "same.json")))
					must(os.Symlink("same.json", file))
				}
			})
			_, err := readBounded(t, k, unblockPipe(file))
			if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
				t.Fatalf("%v", err)
			}
		})
	}
	// elsewhere is a directory of mode 0777 beside credentials/, holding a
	// file of its own.
	elsewhere := func(t *testing.T, creds string) string {
		t.Helper()
		dir := filepath.Join(filepath.Dir(creds), "elsewhere")
		if err := os.Mkdir(dir, 0o700); err != nil || os.Chmod(dir, 0o777) != nil {
			t.Fatal(err)
		}
		put(t, filepath.Join(dir, githubFile), valid(gheHost))
		return dir
	}
	for _, swap := range []string{"a named pipe", "a link to a directory of mode 0777"} {
		t.Run("the directory swapped for "+swap+" there", func(t *testing.T) {
			k, creds := fresh(t)
			put(t, filepath.Join(creds, githubFile), valid("github.com"))
			other := elsewhere(t, creds)
			between(t, k, func(must func(error)) {
				must(os.Rename(creds, creds+".old"))
				if swap == "a named pipe" {
					must(syscall.Mkfifo(creds, 0o600))
				} else {
					must(os.Symlink(other, creds))
				}
			})
			// The operation goes on in the directory it opened, waiting on
			// nothing; the next one is refused at once.
			if f, err := readBounded(t, k, unblockPipe(creds)); err != nil || len(f.GitHub) != 1 || f.GitHub[0].Host != "github.com" {
				t.Fatalf("the read after the swap: %v %v", f, err)
			}
			if _, err := readBounded(t, k, unblockPipe(creds)); err == nil || !strings.Contains(err.Error(), "is not a directory") {
				t.Fatalf("the next read: %v", err)
			}
		})
	}
	t.Run("a write with the directory swapped for a link there", func(t *testing.T) {
		k, creds := fresh(t)
		put(t, filepath.Join(creds, githubFile), valid("github.com"))
		other := elsewhere(t, creds)
		if err := os.Remove(filepath.Join(other, githubFile)); err != nil {
			t.Fatal(err)
		}
		between(t, k, func(must func(error)) {
			must(os.Rename(creds, creds+".old"))
			must(os.Symlink(other, creds))
		})
		if err := writeTrackerFile(k, trackerFile{GitHub: []trackerEntry{{Host: gheHost, keptSecret: kept(secret), Login: "mona", AccountID: "1", SavedAt: savedAt}}}); err != nil {
			t.Fatal(err)
		}
		if names, err := os.ReadDir(other); err != nil || len(names) != 0 {
			t.Fatalf("written in the link's directory: %v %v", names, err)
		}
		if b, err := os.ReadFile(filepath.Join(creds+".old", githubFile)); err != nil || !bytes.Contains(b, []byte(gheHost)) {
			t.Fatalf("not written in the directory opened: %s %v", b, err)
		}
	})
}
