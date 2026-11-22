package machine

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The id is made once: a second read returns the same id from the one
// file, which holds it and a newline, 0600 in a 0700 directory, with no
// temporary file left beside it.
func TestMachineIDMadeOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "conductor")
	first, err := ID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !validHex(first) || !proto.ValidHostMachine(first) {
		t.Fatalf("id %q is not %d lowercase hex digits", first, IDLen)
	}
	again, err := ID(dir)
	if err != nil || again != first {
		t.Fatalf("read again: %q %v, made %q", again, err, first)
	}
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil || string(b) != first+"\n" {
		t.Fatalf("the file holds %q (%v)", b, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the directory holds %v", entries)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, FileName))
		di, _ := os.Stat(dir)
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Fatalf("modes: file %v, directory %v", fi.Mode().Perm(), di.Mode().Perm())
		}
	}
	// A directory that is not absolute is no place for it.
	if id, err := ID("conductor"); err == nil || id != "" {
		t.Fatalf("a relative directory: %q %v", id, err)
	}
}

// Two makers that find no file, both held between writing their temporary
// file and linking it: one link wins, both return the winner's id, and one
// file is left, no temporary file beside it.
func TestMachineIDMadeOnceByTwo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "conductor")
	var arrived sync.WaitGroup
	arrived.Add(2)
	gate := make(chan struct{})
	beforeLink = func() {
		arrived.Done()
		<-gate
	}
	t.Cleanup(func() { beforeLink = nil })
	ids := make([]string, 2)
	errs := make([]error, 2)
	var done sync.WaitGroup
	for i := range 2 {
		done.Go(func() { ids[i], errs[i] = ID(dir) })
	}
	// Both found no file, wrote their own, and wait to link it.
	arrived.Wait()
	close(gate)
	done.Wait()
	if errs[0] != nil || errs[1] != nil || ids[0] != ids[1] || !validHex(ids[0]) {
		t.Fatalf("the makers returned %q %v and %q %v", ids[0], errs[0], ids[1], errs[1])
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != FileName {
		t.Fatalf("the directory holds %v", entries)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, FileName)); string(b) != ids[0]+"\n" {
		t.Fatalf("the file holds %q, the makers said %q", b, ids[0])
	}
}

// A file of the wrong form is no id: an error, nothing returned, and the
// file left as it is, never made again over it.
func TestMachineIDUnreadableReportsNone(t *testing.T) {
	for name, content := range map[string]string{
		"empty":          "",
		"short":          "0123456789abcdef\n",
		"no newline":     strings.Repeat("a", IDLen),
		"upper case":     strings.Repeat("A", IDLen) + "\n",
		"two lines":      strings.Repeat("a", IDLen) + "\n" + strings.Repeat("b", IDLen) + "\n",
		"a space before": " " + strings.Repeat("a", IDLen) + "\n",
		"a CR LF":        strings.Repeat("a", IDLen) + "\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, FileName)
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if id, err := ID(dir); err == nil || id != "" {
				t.Fatalf("read %q, %v", id, err)
			}
			if b, _ := os.ReadFile(p); string(b) != content {
				t.Fatalf("the file was changed to %q", b)
			}
		})
	}
	t.Run("a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, FileName), 0o700); err != nil {
			t.Fatal(err)
		}
		if id, err := ID(dir); err == nil || id != "" {
			t.Fatalf("read %q, %v", id, err)
		}
	})
	t.Run("a link to a good id", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.WriteFile(target, []byte(strings.Repeat("a", IDLen)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, FileName)); err != nil {
			t.Skip("no symbolic links here:", err)
		}
		if id, err := ID(dir); err == nil || id != "" || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("read %q, %v", id, err)
		}
	})
}

// A bad file's error names its path and why, and never a byte it holds:
// something near an id, one too long, one that is not hex.
func TestMachineIDErrorsHoldNoContent(t *testing.T) {
	for name, content := range map[string]string{
		"near an id": "7f3c9a01e5d24b6880aa13c4d5e6f70\n",
		"too long":   "7f3c9a01e5d24b6880aa13c4d5e6f7091\n",
		"not hex":    "7f3c9a01e5d24b6880aa13c4d5e6f7zz\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, FileName)
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ID(dir)
			if err == nil {
				t.Fatal("read")
			}
			msg := err.Error()
			if !strings.Contains(msg, p) || !strings.Contains(msg, "does not hold an id") {
				t.Fatalf("the error does not name the path and why: %v", err)
			}
			for _, part := range []string{strings.TrimSpace(content), "7f3c9a01", "e5d24b68", "d5e6f7"} {
				if strings.Contains(msg, part) {
					t.Fatalf("the error holds the file's bytes: %v", err)
				}
			}
		})
	}
}

// One id and two server keys give two unrelated keys; one id and one key
// give the same key each time; the key is never the id, and an id or a
// server key not of its form gives none. Group hashes a key again, and
// what it makes is of the form a server serves.
func TestMachineKeyPerServer(t *testing.T) {
	id := strings.Repeat("ab", IDLen/2)
	one := bytes.Repeat([]byte{1}, ServerKeyLen)
	two := bytes.Repeat([]byte{2}, ServerKeyLen)
	k1, k2 := Key(id, one), Key(id, two)
	if !proto.ValidHostMachine(k1) || !proto.ValidHostMachine(k2) {
		t.Fatalf("keys %q %q are not of the form a server takes", k1, k2)
	}
	if k1 == k2 || k1 == id || k2 == id {
		t.Fatalf("one id gave %q and %q (id %q)", k1, k2, id)
	}
	if again := Key(id, one); again != k1 {
		t.Fatalf("one id and one key gave %q then %q", k1, again)
	}
	if other := Key(strings.Repeat("cd", IDLen/2), one); other == k1 {
		t.Fatal("two ids gave one key")
	}
	for _, bad := range []struct {
		id  string
		key []byte
	}{{"", one}, {strings.ToUpper(id), one}, {id[:31], one}, {id, one[:16]}, {id, append(bytes.Clone(one), 1)}, {id, nil}} {
		if k := Key(bad.id, bad.key); k != "" {
			t.Errorf("Key(%q, %d bytes) = %q", bad.id, len(bad.key), k)
		}
	}
	if g := Group(k1); !validHex(g) || g == k1 || g != Group(k1) || g == Group(k2) {
		t.Fatalf("Group(%q) = %q", k1, g)
	}
	if len(proto.ParseServerKey(strings.Repeat("01", ServerKeyLen))) != ServerKeyLen {
		t.Fatal("a server key the header carries is not ServerKeyLen bytes")
	}
}

// Inside WSL the kernel's release names Microsoft, in either case: the
// platform says +wsl; elsewhere, or with no release to read, it does not,
// and only Linux reads it. A platform out of the form a server takes is
// none.
func TestPlatformReadsWSL(t *testing.T) {
	release := func(s string, err error) func() ([]byte, error) {
		return func() ([]byte, error) { return []byte(s), err }
	}
	for _, c := range []struct {
		goos, goarch string
		release      func() ([]byte, error)
		want         string
	}{
		{"linux", "amd64", release("6.6.87.2-microsoft-standard-WSL2\n", nil), "linux/amd64+wsl"},
		{"linux", "arm64", release("5.15.0-MICROSOFT-standard\n", nil), "linux/arm64+wsl"},
		{"linux", "amd64", release("6.8.0-45-generic\n", nil), "linux/amd64"},
		{"linux", "amd64", release("", errors.New("no such file")), "linux/amd64"},
		{"darwin", "arm64", release("microsoft", nil), "darwin/arm64"},
		{"windows", "amd64", release("microsoft", nil), "windows/amd64"},
		{"Linux", "amd64", release("", nil), ""},
		{"linux", strings.Repeat("x", 16), release("", nil), ""},
		{"linux", "", release("", nil), ""},
	} {
		got := platformOf(c.goos, c.goarch, c.release)
		if got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.goos, c.goarch, got, c.want)
		}
		if got != "" && !proto.ValidHostPlatform(got) {
			t.Errorf("%q is not of the form a server takes", got)
		}
	}
	// Every form validPlatform takes, a server takes, and the other way.
	for _, p := range []string{"linux/amd64", "linux/amd64+wsl", "a/b", strings.Repeat("a", 15) + "/" + strings.Repeat("b", 15), strings.Repeat("a", 13) + "/" + strings.Repeat("b", 15) + "+wsl", "linux/amd64+wsl+wsl", "linux/", "/amd64", "linux/AMD64", "linux/amd64 "} {
		if validPlatform(p) != proto.ValidHostPlatform(p) {
			t.Errorf("%q: machine says %v, proto says %v", p, validPlatform(p), proto.ValidHostPlatform(p))
		}
	}
	if p := Platform(); !proto.ValidHostPlatform(p) || !strings.HasPrefix(p, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("this machine's platform %q", p)
	}
}
