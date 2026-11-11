//go:build unix

package agents

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// feed makes a named pipe the helper copies to its stdout after its answer
// (USAGE_AFTER), and writes in to it from a goroutine in chunks of several
// sizes, in turn. A write refused because the helper was ended (a bound
// passed) ends the feed. At the test's end a writer still waiting for its
// reader is released and waited for.
func feed(t *testing.T, in string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "after")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer f.Close()
		sizes := []int{7, 4093, 65536, 1<<20 + 3}
		for i := 0; in != ""; i++ {
			n := min(sizes[i%len(sizes)], len(in))
			if _, err := f.Write([]byte(in[:n])); err != nil {
				return
			}
			in = in[n:]
		}
	}()
	t.Cleanup(func() {
		if r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			r.Close()
		}
		<-done
	})
	return path
}

// noise is a line both readers read past: a type Claude Code's reader
// does not read, a notification Codex's skips; pad bytes of padding.
func noise(pad int) string {
	return `{"method":"fixture/noise","type":"noise","pad":"` + strings.Repeat("n", pad) + `"}` + "\n"
}

// claudeFirstLines is the main stream fixture's first n lines: what Claude
// Code prints before its result line.
func claudeFirstLines(t *testing.T, n int) string {
	t.Helper()
	b, err := os.ReadFile(fixture(t, "claude-usage.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(strings.TrimSpace(string(b)), "\n")
	p := filepath.Join(t.TempDir(), "first.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines[:n], "")), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestUsageReadEndTable: a bound holds from the child's first byte to its
// exit, and the read is decided once, after the child has ended, from the
// evidence, any bound passed and the exit. For both readers, every row of
// (a valid answer | the refusal's evidence | no answer) x (within the
// bounds | the whole output past its bound after the answer | one line
// past its bound after the answer | stderr past the output's bound) x (a
// clean exit | a failing exit | the deadline), what follows the answer fed
// in chunks through a pipe the test writes:
//
//   - a bound passed at any time fails the read with the bound's words
//     (Claude Code: Inconclusive), except that a proven turn stands
//     (unqualified, Turned), the bound said beside it;
//   - with no bound passed, an answer stands however the child then ends,
//     and a read with no answer says how it ended: its exit, or the
//     deadline;
//   - stderr is counted, never kept or bounded (the plan bounds stdout),
//     so its row is the within-the-bounds row.
//
// The refusal is Claude Code's model turn, a permanent one, and Codex's
// error reply to account/rateLimits/read, which is no more than a failed
// read. The deadline is a child that closes its output and never exits,
// expired through a seam once its output has been read to its end, so a
// child past a bound, which is not waited for, never needs it.
func TestUsageReadEndTable(t *testing.T) {
	within := noise(100) + noise(100) + noise(100)
	bounds := map[string]struct {
		after  string
		stderr int
		words  string // the bound's words; "" for none
	}{
		"within the bounds":              {within, 0, ""},
		"output past its bound":          {strings.Repeat(noise(64<<10), usageMaxOutput/(64<<10)+2), 0, "printed more than 4 MiB"},
		"one line past its bound":        {within + noise(usageMaxLine+10) + within, 0, "printed a line longer than 1 MiB"},
		"stderr past the output's bound": {within, usageMaxOutput + 1, ""},
	}
	exits := map[string][]string{"clean exit": nil, "failing exit": {"USAGE_THEN=fail"}, "deadline": {"USAGE_THEN=close"}}
	for _, reader := range []string{"claude", "codex"} {
		for _, evidence := range []string{"answer", "refusal", "no answer"} {
			for bname, b := range bounds {
				for ename, exit := range exits {
					t.Run(reader+"/"+evidence+"/"+bname+"/"+ename, func(t *testing.T) {
						env := append([]string{"USAGE_AFTER=" + feed(t, b.after)}, exit...)
						if b.stderr > 0 {
							env = append(env, "USAGE_AFTER_STDERR="+strconv.Itoa(b.stderr))
						}
						switch {
						case reader == "claude" && evidence == "refusal":
							env = append(env, "USAGE_STREAM="+variant(t, "claude-usage.jsonl", `"num_turns":0`, `"num_turns":1`))
						case reader == "claude" && evidence == "no answer":
							env = append(env, "USAGE_STREAM="+claudeFirstLines(t, 2))
						case reader == "codex" && evidence == "refusal":
							env = append(env, "USAGE_RPC_ERROR=account/rateLimits/read")
						case reader == "codex" && evidence == "no answer":
							env = append(env, "USAGE_NOREPLY=1")
						}
						if ename == "deadline" {
							expire := readDeadline(t)
							was := usageDrained
							t.Cleanup(func() { usageDrained = was })
							usageDrained = func(argv []string) {
								if k := childKind(argv); k == "usage" || k == "app-server" {
									expire()
								}
							}
						}
						s := newStub(t, reader, env...)
						var rep UsageReport
						adm := &fakeAdmit{}
						tool, timeout := "Claude Code", "90 s"
						if reader == "claude" {
							rep = claudeRead(t, s, adm)
						} else {
							tool, timeout = "Codex", "30 s"
							rep = codexRead(t, s)
						}

						state, outcome, words := UsageOK, Inconclusive, []string{}
						if reader == "claude" && evidence == "answer" {
							outcome = Local
						}
						switch {
						case reader == "claude" && evidence == "refusal":
							state, outcome, words = UsageUnqualified, Turned, []string{"model turn"}
							if b.words != "" {
								words = append(words, tool+" "+b.words)
							}
						case b.words != "":
							state, outcome, words = UsageError, Inconclusive, []string{tool + " " + b.words}
						case evidence == "refusal":
							state, words = UsageError, []string{"Codex refused account/rateLimits/read (error -32600)"}
						case evidence == "no answer" && ename == "deadline":
							state, words = UsageError, []string{tool + " did not answer within " + timeout}
						case evidence == "no answer" && ename == "failing exit":
							state, words = UsageError, []string{tool + " ended before it answered (exit status 3; "}
						case evidence == "no answer":
							state, words = UsageError, []string{tool + " ended before it answered (exit status 0"}
						}
						if rep.State != state || rep.Outcome != outcome {
							t.Fatalf("state %s, outcome %v; want %s, %v: %+v", rep.State, rep.Outcome, state, outcome, rep)
						}
						for _, w := range words {
							if !strings.Contains(rep.Words, w) {
								t.Errorf("words %q, want %q in them", rep.Words, w)
							}
						}
						if state == UsageOK && (rep.Words != "" || len(rep.Windows) == 0) {
							t.Errorf("an answer that stands: %+v", rep)
						}
						if state != UsageOK && len(rep.Windows) != 0 {
							t.Errorf("windows in a read that failed: %+v", rep)
						}
						if reader == "claude" && !slices.Equal(adm.outcomes(), []Outcome{outcome}) {
							t.Errorf("Done %v, want [%v]", adm.outcomes(), outcome)
						}
						noMarks(t, t.Name(), rep)
					})
				}
			}
		}
	}
}
