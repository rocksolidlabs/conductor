package notify

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The shell reading names only what it is sure of: the reads of the plain
// readers, the writes and edits of redirects, tee and sed -i, a file that
// exists now; never a flag, a pattern, a variable, a substitution, a
// heredoc's body, a descriptor or /dev/null.
func TestShellFiles(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"README.md", "main.go", "out.txt", "log.txt", "my file.txt", "sub/x.go"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644)
	}
	p := func(f string) string { return filepath.Join(dir, f) }
	cases := []struct {
		cmd  string
		want []FileRef
	}{
		{"cat README.md", []FileRef{{"read", p("README.md")}}},
		{"cat README.md main.go | head -n 5", []FileRef{{"read", p("README.md")}, {"read", p("main.go")}}},
		{"head -n 20 main.go && tail -c 10 README.md", []FileRef{{"read", p("main.go")}, {"read", p("README.md")}}},
		{"sed -n '1,40p' main.go", []FileRef{{"read", p("main.go")}}},
		{"sed -i 's/a/b/' main.go", []FileRef{{"edit", p("main.go")}}},
		{"sed 's/a/b/' main.go", nil},
		{"printf 'hi\\n' > out.txt", []FileRef{{"write", p("out.txt")}}},
		{"echo there >> log.txt", []FileRef{{"edit", p("log.txt")}}},
		{"go test ./... 2>&1 | tee out.txt", []FileRef{{"write", p("out.txt")}}},
		{"echo x | tee -a log.txt >/dev/null", []FileRef{{"edit", p("log.txt")}}},
		{"cat 'my file.txt'", []FileRef{{"read", p("my file.txt")}}},
		{"cat \"$FILE\"", nil},
		{"cat $(ls)", nil},
		{"cat `ls`", nil},
		{"cat > out.txt <<'EOF'\nREADME.md\nEOF", []FileRef{{"write", p("out.txt")}}},
		{"cat missing.txt sub", nil},
		{"cd sub && cat x.go", nil}, // cat's file is relative to sub, which a plain reading does not follow: not found here, so nothing
		{"cat sub/x.go; wc -l < main.go", []FileRef{{"read", p("sub/x.go")}, {"read", p("main.go")}}},
		{"grep -rn foo .", nil},
		{"FOO=1 cat README.md", []FileRef{{"read", p("README.md")}}},
		{"cat README.md README.md", []FileRef{{"read", p("README.md")}}},
		{"ls -la", nil},
		{"", nil},
	}
	for _, c := range cases {
		if got := shellFiles(c.cmd, dir); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %v, want %v", c.cmd, got, c.want)
		}
	}
	// Without the command's directory a relative path names nothing; an absolute one still does.
	if got := shellFiles("cat README.md "+p("main.go"), ""); !reflect.DeepEqual(got, []FileRef{{"read", p("main.go")}}) {
		t.Errorf("no cwd: %v", got)
	}
}

// A word under /dev is a device and names nothing, judged on the word as
// written, before the file system is asked: /dev/fd/3 and /dev/stdin can
// resolve to a regular file the hook itself holds open. The one exception is
// /dev/shm, a tmpfs where people keep files, for a word that stays under it
// once cleaned. Every other word is reported as before: an absolute one as
// written, never cleaned, since the function cannot know that a component
// before a ".." is a symlink (link/.. is the link target's parent, not the
// folder holding link); a relative one joined to the command's directory.
// The temporary folder may itself be under /dev/shm (TMPDIR in memory), so
// no case depends on where it is.
func TestShellFilesDeviceWords(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"a/b", "sub"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"notes.txt", "a/notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "a", "b"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	p := func(f string) string { return dir + "/" + f }
	type c struct {
		word, want string // want "" is nothing reported
	}
	cases := []c{
		{"/dev/null", ""},
		{"/dev/tty", ""},
		{"/dev/fd/0", ""},
		{"/dev/stdin", ""},
		{"/dev/mqueue/q", ""},
		{"/dev/shmfoo", ""},           // not under /dev/shm/: a device word, as on main
		{"/dev/shm", ""},              // the folder itself, not under it
		{"/dev/shm/../null", ""},      // /dev/null once cleaned
		{"/dev/shm/../fd/0", ""},      // /dev/fd/0 once cleaned, a regular file when stdin is one
		{"notes.txt", p("notes.txt")}, // relative: joined to the command's directory
		{"sub/../notes.txt", p("notes.txt")},
		{p("notes.txt"), p("notes.txt")},
		{p("sub/../notes.txt"), p("sub/../notes.txt")},   // through "..": as written
		{p("link/../notes.txt"), p("link/../notes.txt")}, // link/.. is a/, so this is a/notes.txt; cleaned it would be dir/notes.txt
	}
	// A file under /dev/shm itself, when there is one to write.
	if shm, err := os.MkdirTemp("/dev/shm", "notify-shell-"); err == nil {
		t.Cleanup(func() { os.RemoveAll(shm) })
		if err := os.WriteFile(shm+"/notes.txt", []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Mkdir(shm+"/sub", 0o755)
		cases = append(cases,
			c{shm + "/notes.txt", shm + "/notes.txt"},
			c{shm + "/sub/../notes.txt", shm + "/sub/../notes.txt"},
		)
	} else {
		t.Logf("no writable /dev/shm here, its kept cases not run: %v", err)
	}
	for _, c := range cases {
		var want []FileRef
		if c.want != "" {
			want = []FileRef{{"read", c.want}}
		}
		if got := shellFiles("cat "+c.word, dir); !reflect.DeepEqual(got, want) {
			t.Errorf("cat %s: %v, want %v", c.word, got, want)
		}
	}
}
