package crew

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// A check's argv in task.md reads as the words it runs as (#590): an
// argument with a space, a quote or nothing at all is single-quoted, so
// `grep -qx 'hello ds2' GREETING.txt` is not read as five words; a plain
// argument stands as it is, and task.json keeps the argv as it is.
func TestTaskFileQuotesACheckArgv(t *testing.T) {
	for _, c := range []struct {
		argv []string
		line string
	}{
		{[]string{"go", "test", "./..."}, "go test ./..."},
		{[]string{"grep", "-qx", "hello ds2", "GREETING.txt"}, "grep -qx 'hello ds2' GREETING.txt"},
		{[]string{"sh", "-c", "test -f fixed.txt || { echo 'fixed.txt must exist first'; exit 1; }"}, `sh -c 'test -f fixed.txt || { echo '\''fixed.txt must exist first'\''; exit 1; }'`},
		{[]string{"printf", "", "a\tb", "$HOME", "*.go"}, `printf '' 'a	b' '$HOME' '*.go'`},
		{[]string{"make", "GOFLAGS=-count=1", "user@host:/a,b+c%d"}, "make GOFLAGS=-count=1 user@host:/a,b+c%d"},
	} {
		if got := shellArgv(c.argv); got != c.line {
			t.Errorf("shellArgv(%q) = %s, want %s", c.argv, got, c.line)
		}
		// A POSIX shell reads the line back as the same words.
		if sh, err := exec.LookPath("sh"); err == nil {
			out, err := exec.Command(sh, "-c", `for a in `+c.line+`; do printf '%s\0' "$a"; done`).Output()
			if err != nil {
				t.Fatalf("sh reading %s: %v", c.line, err)
			}
			if words := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"); !slices.Equal(words, c.argv) {
				t.Errorf("sh reads %s as %q, want %q", c.line, words, c.argv)
			}
		}
	}

	f := TaskFile{Task: "build", Attempt: 1, MaxAttempts: 3, Member: "lead", Kind: TaskWork, Goal: "say hello", Instructions: "write GREETING.txt", Start: strings.Repeat("b", 40),
		Checks: []Check{{ID: "greeting", Argv: []string{"grep", "-qx", "hello ds2", "GREETING.txt"}, TimeoutSeconds: 30}}}
	md, js, _, err := fitTaskFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if want := "- greeting: `grep -qx 'hello ds2' GREETING.txt` (30s)\n"; !strings.Contains(string(md), want) {
		t.Fatalf("task.md has no %q:\n%s", want, md)
	}
	var kept TaskFile
	if err := json.Unmarshal(js, &kept); err != nil {
		t.Fatal(err)
	}
	if len(kept.Checks) != 1 || !slices.Equal(kept.Checks[0].Argv, f.Checks[0].Argv) {
		t.Fatalf("task.json's checks: %+v", kept.Checks)
	}
}
