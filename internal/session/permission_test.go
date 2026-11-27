package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A chat's permission request (docs/plans/chat-or-terminal.md, design 7,
// row 6): through the session's own calls, against the fake driver, on a
// fake clock.

// fakeTimers is a session's timer (Options.AfterFunc) that fires only when
// the test says.
type fakeTimers struct {
	mu sync.Mutex
	ts []*fakeTimer
}

type fakeTimer struct {
	d             time.Duration
	f             func()
	stopped, done bool
}

func (c *fakeTimers) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{d: d, f: f}
	c.ts = append(c.ts, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped && !t.done
		t.stopped = true
		return was
	}
}

// fire runs every pending timer of d, and says how many it ran.
func (c *fakeTimers) fire(d time.Duration) int {
	c.mu.Lock()
	var run []func()
	for _, t := range c.ts {
		if t.d == d && !t.stopped && !t.done {
			t.done = true
			run = append(run, t.f)
		}
	}
	c.mu.Unlock()
	for _, f := range run {
		f()
	}
	return len(run)
}

// pending counts the timers of d not yet fired or stopped.
func (c *fakeTimers) pending(d time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.ts {
		if t.d == d && !t.stopped && !t.done {
			n++
		}
	}
	return n
}

// permissionSession is a chat on the fake clock, in a turn.
func permissionSession(t *testing.T, opts Options) (*Local, *sessiontest.FakeProtocol, *fakeTimers) {
	t.Helper()
	clock := &fakeTimers{}
	opts.AfterFunc = clock.AfterFunc
	s, _, f := protocolSession(t, opts)
	f.TurnStarted("")
	return s, f, clock
}

// heldAsk is a request to the prompt tool under way: its POST's context,
// and what it was answered.
type heldAsk struct {
	cancel context.CancelFunc
	done   chan struct{}
	// lose makes its reply fail, as a POST gone before its write does.
	lose bool
	mu   sync.Mutex
	got  []Verdict
}

func (a *heldAsk) verdicts() []Verdict {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Verdict(nil), a.got...)
}

// ask puts a request to the prompt tool on its own goroutine.
func ask(s *Local, call, tool, input string) *heldAsk {
	return askLosing(s, call, tool, input, false)
}

func askLosing(s *Local, call, tool, input string, lose bool) *heldAsk {
	ctx, cancel := context.WithCancel(context.Background())
	a := &heldAsk{cancel: cancel, done: make(chan struct{}), lose: lose}
	go func() {
		defer close(a.done)
		s.RequestPermission(ctx, PermissionAsk{Call: call, Tool: tool, Input: []byte(input)}, func(v Verdict) error {
			a.mu.Lock()
			a.got = append(a.got, v)
			a.mu.Unlock()
			if a.lose {
				return errors.New("the POST is gone")
			}
			return nil
		})
	}()
	return a
}

// returned waits for the request to return and gives its one verdict.
func (a *heldAsk) returned(t *testing.T) Verdict {
	t.Helper()
	select {
	case <-a.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the held request never returned")
	}
	got := a.verdicts()
	if len(got) != 1 {
		t.Fatalf("the request was answered %d times: %+v", len(got), got)
	}
	return got[0]
}

// held says the request has not returned.
func (a *heldAsk) held() bool {
	select {
	case <-a.done:
		return false
	default:
		return true
	}
}

// waitPerms waits for the session's open and waiting requests to be open
// and waiting; the bound only bounds a hang.
func waitPerms(t *testing.T, s *Local, open, waiting int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		o, w := s.OpenPermissions()
		if o == open && w == waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("open %d waiting %d, want %d and %d", o, w, open, waiting)
		}
		time.Sleep(time.Millisecond)
	}
}

// openRequest is the open request entry of the call, waited for.
func openRequest(t *testing.T, s *Local, call string) TranscriptEntry {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, e := range allEntries(t, s) {
			if e.Kind == EntryPermission && e.Call == call && e.State == PermissionOpen {
				return e
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no open request for %s: %+v", call, allEntries(t, s))
		}
		time.Sleep(time.Millisecond)
	}
}

func bash(cmd string) string { return `{"command":"` + cmd + `"}` }

func announce(f *sessiontest.FakeProtocol, call, tool, input string) {
	f.ToolStarted(ToolStep{Ref: call, Name: tool, Input: []byte(input)})
}

// I8: a request is taken only for a call of the current turn the stream
// showed, with its name and digest, not yet resulted, once per call, never
// Conductor's own prompt tool's; every other is denied with its words.
func TestPermissionNamesAnAnnouncedCall(t *testing.T) {
	s, f, clock := permissionSession(t, Options{})

	// Never announced: denied once AnnounceWait passes, never allowed.
	never := ask(s, "c-none", "Bash", bash("ls"))
	waitPerms(t, s, 0, 1)
	if n := clock.fire(AnnounceWait); n != 1 {
		t.Fatalf("%d announcement waits fired", n)
	}
	if v := never.returned(t); v.Allow || v.Message != NotACallWords {
		t.Fatalf("a call never announced: %+v", v)
	}

	// Already resulted.
	announce(f, "c-done", "Bash", bash("ls"))
	f.ToolEnded("c-done", ToolResult{Status: ToolOK})
	if v := ask(s, "c-done", "Bash", bash("ls")).returned(t); v.Allow || v.Message != NotACallWords {
		t.Fatalf("a call with a result: %+v", v)
	}

	// Another name, another digest, Conductor's own tool.
	announce(f, "c-1", "Bash", bash("make"))
	for name, a := range map[string]*heldAsk{
		"another name":    ask(s, "c-1", "Write", bash("make")),
		"another digest":  ask(s, "c-1", "Bash", bash("make install")),
		"the tool itself": ask(s, "c-1", permissionToolName, bash("make")),
	} {
		if v := a.returned(t); v.Allow || v.Message != NotACallWords {
			t.Fatalf("%s: %+v", name, v)
		}
	}
	announce(f, "c-self", permissionToolName, `{}`)
	if v := ask(s, "c-self", permissionToolName, `{}`).returned(t); v.Allow || v.Message != NotACallWords {
		t.Fatalf("a call of the prompt tool: %+v", v)
	}

	// The matching one is taken, with the same input in another form.
	taken := ask(s, "c-1", "Bash", `{ "command" : "make" }`)
	e := openRequest(t, s, "c-1")
	if e.Tool != "Bash" || e.Plain != "command" || e.InputSha256 != inputDigest("Bash", []byte(bash("make"))) {
		t.Fatalf("the request's entry %+v", e)
	}
	if !taken.held() {
		t.Fatal("the matching request was answered at once")
	}
	// A second for the same call.
	if v := ask(s, "c-1", "Bash", bash("make")).returned(t); v.Allow || v.Message != AlreadyAskedWords {
		t.Fatalf("a second request for one call: %+v", v)
	}

	// Another turn's call: the turn ends (the open one withdrawn), the next
	// begins, and a call of the last turn is none of this one's.
	announce(f, "c-old", "Bash", bash("pwd"))
	f.TurnEnded("done", Usage{})
	if v := taken.returned(t); v.Allow || !v.Cancel {
		t.Fatalf("the open request at the turn's end: %+v", v)
	}
	f.TurnStarted("")
	if v := ask(s, "c-old", "Bash", bash("pwd")).returned(t); v.Allow || v.Message != NotACallWords {
		t.Fatalf("another turn's call: %+v", v)
	}

	// A request that names no call takes the one call that matches; two
	// calls alike are denied.
	announce(f, "c-a", "Read", `{"file_path":"a.txt"}`)
	one := ask(s, "", "Read", `{"file_path":"a.txt"}`)
	if e := openRequest(t, s, "c-a"); e.Call != "c-a" {
		t.Fatalf("bound to %q", e.Call)
	}
	announce(f, "c-b", "Read", `{"file_path":"b.txt"}`)
	announce(f, "c-c", "Read", `{"file_path":"b.txt"}`)
	if v := ask(s, "", "Read", `{"file_path":"b.txt"}`).returned(t); v.Allow || v.Message != NotACallWords {
		t.Fatalf("two calls alike: %+v", v)
	}
	one.cancel()
	one.returned0(t)
}

// returned0 waits for a request whose POST went: it is answered nothing.
func (a *heldAsk) returned0(t *testing.T) {
	t.Helper()
	select {
	case <-a.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the held request never returned")
	}
	if got := a.verdicts(); len(got) != 0 {
		t.Fatalf("a request whose POST went was answered %+v", got)
	}
}

// I8: a request that comes before its call's announcement waits, with no
// question asked, and is taken once the stream shows the call.
func TestPermissionBeforeItsAnnouncement(t *testing.T) {
	s, f, _ := permissionSession(t, Options{})
	waits := make(chan struct{}, 1)
	seam := func() {
		select {
		case waits <- struct{}{}:
		default:
		}
	}
	s.permissionWaits.Store(&seam)
	// The driver's ToolStarted is held at its seam until the request has
	// entered its wait.
	gate := func() { <-waits }
	s.beforeToolStarted.Store(&gate)
	a := ask(s, "c-1", "Bash", bash("make"))
	announce(f, "c-1", "Bash", bash("make"))
	s.beforeToolStarted.Store(nil)
	e := openRequest(t, s, "c-1")
	if e.State != PermissionOpen || !a.held() {
		t.Fatalf("the request after its announcement %+v, answered %+v", e, a.verdicts())
	}
	if att := s.Info().Attention; att.Kind != KindPermission || att.ID != e.Question {
		t.Fatalf("the attention %+v", att)
	}
}

// The floor: a file tool's path under the deny list and a command naming
// one are denied before any question; a command that computes the path is
// not claimed as floored.
func TestPermissionFloor(t *testing.T) {
	data := realDir(t, t.TempDir())
	secret := filepath.Join(data, "secrets.json")
	if err := os.WriteFile(secret, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, f, _ := permissionSession(t, Options{FileDeny: DenyList([]string{data}, nil)})
	cases := []struct {
		name, tool, input string
		floored           bool
	}{
		{"a read of a denied file", "Read", `{"file_path":"` + secret + `"}`, true},
		{"an edit of one", "Edit", `{"file_path":"` + secret + `","old_string":"a","new_string":"b"}`, true},
		{"a write under the folder", "Write", `{"file_path":"` + filepath.Join(data, "new.txt") + `","content":"x"}`, true},
		{"a grep of the folder", "Grep", `{"pattern":"token","path":"` + data + `"}`, true},
		{"a glob of it", "Glob", `{"pattern":"` + data + `/**/*.json"}`, true},
		{"a command naming it", "Bash", bash("cat " + secret), true},
		{"a command naming the folder", "Bash", bash("ls " + data), true},
		{"a read of the session's own file", "Read", `{"file_path":"notes.txt"}`, false},
		{"a command that computes the path", "Bash", bash("D=" + filepath.Dir(data) + "; cat $D/" + filepath.Base(data) + "x/secrets.json"), false},
		{"a command naming nothing", "Bash", bash("make test"), false},
		// Codex's steps (internal/agentproto, codex.go): a file change names
		// its files in a list, and may ask to write under a root; a command
		// names the folder it runs in.
		{"a Codex file change of a denied file", "fileChange", `{"changes":[{"path":"notes.txt","kind":"update","diff":""},{"path":"` + secret + `","kind":"update","diff":""}]}`, true},
		{"a Codex file change asking to write under the folder", "fileChange", `{"changes":[{"path":"notes.txt","kind":"update","diff":""}],"grantRoot":"` + data + `"}`, true},
		{"a Codex command run in the folder", "commandExecution", `{"command":"cat secrets.json","cwd":"` + data + `"}`, true},
		{"a Codex command naming nothing", "commandExecution", `{"command":"make test","cwd":""}`, false},
		{"a Codex file change of the session's own file", "fileChange", `{"changes":[{"path":"notes.txt","kind":"update","diff":"-a\n+b"}]}`, false},
	}
	for i, c := range cases {
		call := fmt.Sprintf("c-%d", i)
		announce(f, call, c.tool, c.input)
		a := ask(s, call, c.tool, c.input)
		if !c.floored {
			e := openRequest(t, s, call)
			if !a.held() {
				t.Fatalf("%s: answered %+v", c.name, a.verdicts())
			}
			f.ToolEnded(call, ToolResult{Status: ToolOK})
			if v := a.returned(t); v.Allow || !v.Cancel {
				t.Fatalf("%s: withdrawn as %+v", c.name, v)
			}
			_ = e
			continue
		}
		if v := a.returned(t); v.Allow || v.Message != FloorWords {
			t.Fatalf("%s: %+v", c.name, v)
		}
		last := lastEntry(t, s)
		if last.Kind != EntryPermission || last.State != PermissionDenied || last.Why != FloorWords || last.Question != "" {
			t.Fatalf("%s: its entry %+v", c.name, last)
		}
		if att := s.Info().Attention; att.Kind == KindPermission {
			t.Fatalf("%s: a floored call was asked: %+v", c.name, att)
		}
	}
}

// The bound: the ninth open or waiting request is denied with its words.
func TestPermissionBound(t *testing.T) {
	s, f, _ := permissionSession(t, Options{})
	var held []*heldAsk
	for i := range MaxPendingPermissions {
		call := fmt.Sprintf("c-%d", i)
		if i%2 == 0 {
			announce(f, call, "Bash", bash(call))
		}
		held = append(held, ask(s, call, "Bash", bash(call)))
	}
	waitPerms(t, s, MaxPendingPermissions/2, MaxPendingPermissions/2)
	announce(f, "c-past", "Bash", bash("c-past"))
	if v := ask(s, "c-past", "Bash", bash("c-past")).returned(t); v.Allow || v.Message != TooManyRequestsWords {
		t.Fatalf("the ninth: %+v", v)
	}
	// An in-stream one counts against the same bound.
	askInStream(f, "req-past", "c-in-stream", "Bash", `{}`)
	if e := lastEntry(t, s); e.State != PermissionDenied || e.Why != TooManyRequestsWords {
		t.Fatalf("an in-stream ninth %+v", e)
	}
	for _, a := range held {
		if !a.held() {
			t.Fatalf("a request within the bound was answered %+v", a.verdicts())
		}
	}
	// A place freed takes the next.
	f.ToolEnded("c-0", ToolResult{Status: ToolOK})
	held[0].returned(t)
	announce(f, "c-next", "Bash", bash("c-next"))
	ask(s, "c-next", "Bash", bash("c-next"))
	openRequest(t, s, "c-next")
}

// I11: nothing silent allows, and no request is left held: every end
// answers deny or withdraws with its words, and its held request returns.
func TestPermissionNeverSilentlyAllowed(t *testing.T) {
	type end struct {
		name   string
		end    func(s *Local, f *sessiontest.FakeProtocol, clock *fakeTimers, a *heldAsk)
		words  string
		cancel bool
	}
	ends := []end{
		{"the session's stop", func(s *Local, _ *sessiontest.FakeProtocol, _ *fakeTimers, _ *heldAsk) {
			_ = s.Stop(context.Background())
		}, EndedBeforeAnswerWords, false},
		{"the child's exit", func(_ *Local, f *sessiontest.FakeProtocol, _ *fakeTimers, _ *heldAsk) { f.Child().End(1) }, EndedBeforeAnswerWords, false},
		{"an interrupt", func(_ *Local, f *sessiontest.FakeProtocol, _ *fakeTimers, _ *heldAsk) {
			f.TurnEnded("interrupted", Usage{})
		}, TurnStoppedWords, true},
		{"a turn's end", func(_ *Local, f *sessiontest.FakeProtocol, _ *fakeTimers, _ *heldAsk) { f.TurnEnded("done", Usage{}) }, AnsweredByAgentWords, true},
		{"the agent's cancellation", func(_ *Local, _ *sessiontest.FakeProtocol, _ *fakeTimers, a *heldAsk) { a.cancel() }, RequestLostWords, false},
		{"PermissionWait", func(_ *Local, _ *sessiontest.FakeProtocol, clock *fakeTimers, _ *heldAsk) { clock.fire(PermissionWait) }, NobodyAnsweredWords, false},
	}
	for _, c := range ends {
		t.Run(c.name, func(t *testing.T) {
			s, f, clock := permissionSession(t, Options{})
			announce(f, "c-1", "Bash", bash("make"))
			a := ask(s, "c-1", "Bash", bash("make"))
			r := openRequest(t, s, "c-1")
			if clock.pending(PermissionWait) != 1 {
				t.Fatal("no PermissionWait armed")
			}
			c.end(s, f, clock, a)
			select {
			case <-a.done:
			case <-time.After(30 * time.Second):
				t.Fatal("the held request was left behind")
			}
			got := a.verdicts()
			if c.name == "the agent's cancellation" {
				if len(got) != 0 {
					t.Fatalf("a request whose POST went was answered %+v", got)
				}
			} else if len(got) != 1 || got[0].Allow || got[0].Cancel != c.cancel || got[0].Message != c.words {
				t.Fatalf("answered %+v, want deny %q (withdrawal %v)", got, c.words, c.cancel)
			}
			e := entryOf(t, s, r.Seq)
			if e.State == PermissionAllowed || e.State == PermissionOpen || e.Why != c.words {
				t.Fatalf("its entry %+v", e)
			}
			if o, w := s.OpenPermissions(); o != 0 || w != 0 {
				t.Fatalf("open %d waiting %d", o, w)
			}
		})
	}
	// A request waiting for its call is denied by the turn's end, the
	// session's end and its wait, never allowed.
	for _, name := range []string{"turn", "stop"} {
		s, f, _ := permissionSession(t, Options{})
		a := ask(s, "c-later", "Bash", bash("make"))
		waitPerms(t, s, 0, 1)
		want := NotACallWords
		if name == "turn" {
			f.TurnEnded("done", Usage{})
		} else {
			_ = s.Stop(context.Background())
			want = EndedBeforeAnswerWords
		}
		if v := a.returned(t); v.Allow || v.Message != want {
			t.Fatalf("waiting at the %s: %+v", name, v)
		}
	}
}

// The digest is of one form: the same input with keys reordered and spaces
// changed has one digest, a changed byte another; an input that does not
// decode cleanly is denied.
func TestPermissionDigestCanonical(t *testing.T) {
	one, ok1 := strictCanonical([]byte(`{"b":1.50,"a":{"y":[1,2],"x":"é"}}`))
	two, ok2 := strictCanonical([]byte("{ \"a\" : { \"x\" : \"\\u00e9\", \"y\" : [ 1 , 2 ] } ,\n\t\"b\" : 1.50 }"))
	if !ok1 || !ok2 || string(one) != string(two) || string(one) != `{"a":{"x":"é","y":[1,2]},"b":1.50}` {
		t.Fatalf("canonical forms %q %q", one, two)
	}
	if digestOf("Bash", one) == digestOf("Bash", []byte(`{"a":{"x":"é","y":[1,2]},"b":1.51}`)) {
		t.Fatal("one changed byte gave the same digest")
	}
	if digestOf("Bash", one) == digestOf("Edit", one) {
		t.Fatal("another tool gave the same digest")
	}
	for name, in := range map[string]string{
		"a lone high surrogate": `{"command":"\ud800"}`,
		"a lone low surrogate":  `{"command":"a\udc00b"}`,
		"a high one twice":      `{"command":"\ud800\ud800"}`,
		"a key twice":           `{"command":"ls","command":"rm -rf /"}`,
		"a nested key twice":    `{"a":{"k":1,"k":2}}`,
		"not UTF-8":             "{\"command\":\"\xff\"}",
		"two values":            `{"a":1}{"b":2}`,
		"not JSON":              `{"a":`,
	} {
		if _, ok := strictCanonical([]byte(in)); ok {
			t.Errorf("%s was taken", name)
		}
	}
	if _, ok := strictCanonical([]byte(`{"command":"\ud83d\ude00 and \\ud800"}`)); !ok {
		t.Error("a pair, and an escaped backslash before u, were refused")
	}
	s, f, _ := permissionSession(t, Options{})
	announce(f, "c-1", "Bash", `{"command":"ls"}`)
	if v := ask(s, "c-1", "Bash", `{"command":"ls","command":"rm -rf /"}`).returned(t); v.Allow || v.Message != NotExactWords {
		t.Fatalf("a key twice: %+v", v)
	}
}

// Design 9: no move between ways is made while a request is open, waiting
// for its call, or on its way back.
func TestNoMoveWhileARequestWaits(t *testing.T) {
	s, _, _ := permissionSession(t, Options{})
	a := ask(s, "c-later", "Bash", bash("make"))
	waitPerms(t, s, 0, 1)
	s.mu.Lock()
	why, ok := s.atRestLocked()
	s.mu.Unlock()
	if ok || why != RequestWaitsWords {
		t.Fatalf("at rest with a request waiting: %q %v", why, ok)
	}
	a.cancel()
	a.returned0(t)
}

// The floor's spellings (commandNamesDenied): what the docs say is judged a
// path of the deny list is floored, a quote, a backslash, a doubled slash, a
// dot segment, the home folder's forms and a path relative to the session's
// folder among them; a glob, a variable other than HOME and a computed path
// are asked like any other call; a pattern is a path for Glob alone.
func TestPermissionFloorSpellings(t *testing.T) {
	var deny []string
	s, f, _ := permissionSession(t, Options{FileDenyFunc: func() []string { return deny }})
	cwd := s.Info().Cwd
	t.Setenv("HOME", cwd)
	data := filepath.Join(cwd, "cdata")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	deny = DenyList([]string{data}, nil)
	cases := []struct {
		name, tool, input string
		floored           bool
	}{
		{"as the list spells it", "Bash", bash("cat " + data + "/s.json"), true},
		{"a doubled slash", "Bash", bash("cat " + cwd + "//cdata/s.json"), true},
		{"a dot segment", "Bash", bash("cat " + cwd + "/./cdata/s.json"), true},
		{"in quotes", "Bash", bash(`cat \"` + data + `\"/s.json`), true},
		{"quotes inside it", "Bash", bash("cat " + cwd + "/cd''ata/s.json"), true},
		{"a backslash inside it", "Bash", bash(`cat ` + cwd + `/cd\\ata/s.json`), true},
		{"relative to the session's folder", "Bash", bash("cat cdata/s.json"), true},
		{"relative with a dot", "Bash", bash("ls ./cdata"), true},
		{"after an equals sign", "Bash", bash("CONF=cdata/s.json make"), true},
		{"the home folder by ~", "Bash", bash("cat ~/cdata/s.json"), true},
		{"the home folder by $HOME in quotes", "Bash", bash(`cat \"$HOME\"/cdata/s.json`), true},
		{"in capitals", "Bash", bash("cat " + strings.ToUpper(data) + "/S.JSON"), true},
		{"a Glob's pattern", "Glob", `{"pattern":"` + data + `/**/*.json"}`, true},
		{"a Glob's pattern relative to the session's folder", "Glob", `{"pattern":"cdata/*.json"}`, true},
		{"a Glob's pattern with nothing literal", "Glob", `{"pattern":"**/*.json"}`, false},
		{"a line continuation inside it", "Bash", bash(`cat ` + cwd + `/cda\\\nta/s.json`), true},
		{"a glob in the command", "Bash", bash("cat " + cwd + "/cdat?/s.json"), false},
		{"a variable", "Bash", bash("D=" + cwd + "; cat $D/cdata/s.json"), false},
		{"a word that only ends in the name", "Bash", bash("cat oldcdata/s.json"), false},
		{"a Grep's pattern", "Grep", `{"pattern":"` + data + `"}`, false},
	}
	for i, c := range cases {
		call := fmt.Sprintf("c-%d", i)
		announce(f, call, c.tool, c.input)
		a := ask(s, call, c.tool, c.input)
		if c.floored {
			if v := a.returned(t); v.Allow || v.Message != FloorWords {
				t.Fatalf("%s (%s): %+v", c.name, c.input, v)
			}
			continue
		}
		openRequest(t, s, call)
		f.ToolEnded(call, ToolResult{Status: ToolOK})
		if v := a.returned(t); v.Allow || !v.Cancel {
			t.Fatalf("%s: %+v", c.name, v)
		}
	}
	// The hook knows no folder: the absolute spellings alone.
	if !CommandNamesDenied("cat "+cwd+"//cdata/s.json", deny) || CommandNamesDenied("cat cdata/s.json", deny) {
		t.Fatal("the hook's judgement")
	}
}

// A request's summary folds white space before it is cut and says it was
// cut: a command of ls, six hundred spaces and a second command never reads
// as ls alone; the whole input is the part.
func TestPermissionSummaryIsHonest(t *testing.T) {
	s, f, _ := permissionSession(t, Options{})
	padded := bash("ls" + strings.Repeat(" ", 600) + "&& rm -rf build")
	announce(f, "c-1", "Bash", padded)
	ask(s, "c-1", "Bash", padded)
	if e := openRequest(t, s, "c-1"); !strings.Contains(e.Summary, "rm -rf build") || strings.HasSuffix(e.Summary, SummaryCutMark) {
		t.Fatalf("a padded command's summary %q", e.Summary)
	}
	long := bash(strings.Repeat("echo word ", 300))
	announce(f, "c-2", "Bash", long)
	ask(s, "c-2", "Bash", long)
	e := openRequest(t, s, "c-2")
	if len(e.Summary) > MaxEntrySummary || !strings.HasSuffix(e.Summary, SummaryCutMark) {
		t.Fatalf("a long command's summary (%d bytes) %q", len(e.Summary), e.Summary)
	}
	if part, total, cut, err := s.TranscriptPart(e.Seq, PartInput, 0); err != nil || total != len(long) || cut || string(part) != long {
		t.Fatalf("the input part: %d of %d, cut %v: %v", len(part), total, cut, err)
	}
}

// An in-stream request's input is bounded as the route's is: past 256 KiB
// it is denied with the too-large sentence, never asked with its part cut
// under a digest of the whole.
func TestPermissionInStreamInputBound(t *testing.T) {
	s, f, _ := permissionSession(t, Options{})
	big := `{"command":"` + strings.Repeat("x", MaxTranscriptPart) + `"}`
	f.TurnStarted("")
	askInStream(f, "req-1", "c-1", "Bash", big)
	if e := lastEntry(t, s); e.State != PermissionDenied || e.Why != TooLargeWords || e.Question != "" {
		t.Fatalf("a request past the bound %+v", e)
	}
	waitCalls(t, f, "the deny", func(c []string) bool { return sessiontest.Count(c, "reply") == 1 })
	if r := f.Replies(); r[0].Verdict.Allow || r[0].Verdict.Message != TooLargeWords {
		t.Fatalf("replied %+v", r)
	}
	at := `{"command":"` + strings.Repeat("x", MaxTranscriptPart-len(`{"command":""}`)) + `"}`
	askInStream(f, "req-2", "c-2", "Bash", at)
	if e := lastEntry(t, s); e.State != PermissionOpen {
		t.Fatalf("a request at the bound %+v", e)
	}
}

// A name entry (dir/*name*: the copies beside a config or catalog file)
// is named by a word holding the name in a file name whose folder is dir:
// spelt in full, or, when the session's folder is dir, with no folder at
// all. A file of that name in another folder is asked; so is a word that
// only ends in a denied path relative to the session's folder.
func TestPermissionFloorNameEntries(t *testing.T) {
	run := func(t *testing.T, s *Local, f *sessiontest.FakeProtocol, cases []floorCase) {
		t.Helper()
		for i, c := range cases {
			call := fmt.Sprintf("c-%s-%d", t.Name(), i)
			announce(f, call, "Bash", bash(c.cmd))
			a := ask(s, call, "Bash", bash(c.cmd))
			if c.floored {
				if v := a.returned(t); v.Allow || v.Message != FloorWords {
					t.Fatalf("%q: %+v, want the floor's deny", c.cmd, v)
				}
				continue
			}
			openRequest(t, s, call)
			f.ToolEnded(call, ToolResult{Status: ToolOK})
			if v := a.returned(t); v.Allow || !v.Cancel {
				t.Fatalf("%q: %+v, want it asked", c.cmd, v)
			}
		}
	}
	t.Run("the session's folder is the entry's", func(t *testing.T) {
		var deny []string
		s, f, _ := permissionSession(t, Options{FileDenyFunc: func() []string { return deny }})
		cwd := s.Info().Cwd
		deny = DenyList([]string{filepath.Join(cwd, "conductor.d")}, []string{filepath.Join(cwd, "conductor.json")})
		run(t, s, f, []floorCase{
			{"cat conductor.json", true},
			{"cat ./conductor.json.bak", true},
			{`cat \"conductor.json\"`, true}, // in the JSON input: cat "conductor.json"
			{"cat " + cwd + "/conductor.json~", true},
			{"grep -rn conductor.json docs/", true},
			{"cat web/conductor.json", false},
			{"cat notes/conductor.json.md", false},
			{"git diff -- examples/conductor.json", false},
			{"ls conductor.d", true},
			{"ls conductor.d/x", true},
			{"ls conductor.dx", false},
			{"ls old-conductor.d", false},
		})
	})
	t.Run("the entry's folder is another", func(t *testing.T) {
		other := realDir(t, t.TempDir())
		var deny []string
		s, f, _ := permissionSession(t, Options{FileDenyFunc: func() []string { return deny }})
		deny = DenyList(nil, []string{filepath.Join(other, "conductor.json")})
		run(t, s, f, []floorCase{
			{"cat conductor.json", false},
			{"cat " + other + "/conductor.json.bak", true},
			{"cat " + other + "/web/conductor.json", false},
		})
	})
	// The hook knows no folder: the folder-less word is asked there.
	dir := t.TempDir()
	deny := DenyList(nil, []string{filepath.Join(dir, "conductor.json")})
	if CommandNamesDenied("cat conductor.json", deny) || !CommandNamesDenied("cat "+dir+"/conductor.json.bak", deny) {
		t.Fatal("the hook's judgement of a name entry")
	}
}

type floorCase struct {
	cmd     string
	floored bool
}

// inStreamAsked hands over an in-stream request and returns its entry and
// the verdict replied to it (none while it is open).
func inStreamAsked(t *testing.T, s *Local, f *sessiontest.FakeProtocol, ref, call, tool, input string) (TranscriptEntry, *Verdict) {
	t.Helper()
	f.Permission(PermissionRequest{Ref: ref, Call: call, Tool: tool, Input: []byte(input)})
	e := lastEntry(t, s)
	if e.Kind != EntryPermission || e.Ref != ref {
		t.Fatalf("the request's entry %+v", e)
	}
	if e.State == PermissionOpen {
		return e, nil
	}
	waitCalls(t, f, "the reply to "+ref, func(c []string) bool {
		for _, r := range f.Replies() {
			if r.Ref == ref {
				return true
			}
		}
		return false
	})
	for _, r := range f.Replies() {
		if r.Ref == ref {
			v := r.Verdict
			return e, &v
		}
	}
	return e, nil
}

// deniedWith says the request was denied with words, before any question.
func deniedWith(t *testing.T, s *Local, e TranscriptEntry, v *Verdict, words string) {
	t.Helper()
	if e.State != PermissionDenied || e.Why != words || e.Question != "" || v == nil || v.Allow || v.Message != words {
		t.Fatalf("want denied %q: entry %+v, replied %+v", words, e, v)
	}
	if s.isPermissionQuestion(e.Question) {
		t.Fatalf("a denied request is a question: %+v", e)
	}
}

// An in-stream request for a call of the current turn that the stream
// announced, with that call id, the same tool name and input, and no
// result yet, is the session's question, bound to that call.
func TestAnInStreamRequestForAnAnnouncedOpenCallIsTaken(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	// The same input in another spelling has the same canonical form.
	f.ToolStarted(ToolStep{Ref: "c1", Name: "commandExecution", Input: []byte(`{"cwd":"/work","command":"make"}`)})
	e, v := inStreamAsked(t, s, f, "req-1", "c1", "commandExecution", `{"command":"make", "cwd":"/work"}`)
	if v != nil || e.State != PermissionOpen || e.Call != "c1" || e.Question == "" {
		t.Fatalf("the request %+v, replied %+v", e, v)
	}
	if att := s.Info().Attention; att.Kind != KindPermission || att.ID != e.Question {
		t.Fatalf("the attention %+v", att)
	}
}

// An in-stream request for a call the stream never showed is denied at
// once, "Not a call this session is making.": nothing waits for it to be
// announced later, and its call announced after does not take it.
func TestAnInStreamRequestForACallNeverShownIsDenied(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	e, v := inStreamAsked(t, s, f, "req-1", "c1", "commandExecution", `{"command":"make"}`)
	deniedWith(t, s, e, v, NotACallWords)
	f.ToolStarted(ToolStep{Ref: "c1", Name: "commandExecution", Input: []byte(`{"command":"make"}`)})
	if att := s.Info().Attention; att.Kind == KindPermission {
		t.Fatalf("the denied request was asked once its call came: %+v", att)
	}
	// None names no call either: with no call announced, nothing matches.
	e, v = inStreamAsked(t, s, f, "req-2", "", "fileChange", `{"changes":[]}`)
	deniedWith(t, s, e, v, NotACallWords)
}

// An in-stream request whose tool name or input differs from the call it
// names as the stream announced it is denied: what the person would be
// asked about is not the call shown.
func TestAnInStreamRequestWhoseNameOrInputDiffersIsDenied(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	f.ToolStarted(ToolStep{Ref: "c1", Name: "commandExecution", Input: []byte(`{"command":"make","cwd":"/work"}`)})
	e, v := inStreamAsked(t, s, f, "req-1", "c1", "fileChange", `{"command":"make","cwd":"/work"}`)
	deniedWith(t, s, e, v, NotACallWords)
	e, v = inStreamAsked(t, s, f, "req-2", "c1", "commandExecution", `{"command":"make install","cwd":"/work"}`)
	deniedWith(t, s, e, v, NotACallWords)
	e, v = inStreamAsked(t, s, f, "req-3", "c1", "commandExecution", `{"command":"make","cwd":"/work/sub"}`)
	deniedWith(t, s, e, v, NotACallWords)
	// The call itself is still open to a request that matches it.
	e, v = inStreamAsked(t, s, f, "req-4", "c1", "commandExecution", `{"command":"make","cwd":"/work"}`)
	if v != nil || e.State != PermissionOpen {
		t.Fatalf("the matching request %+v, replied %+v", e, v)
	}
}

// An in-stream request for a call whose result the stream has shown is
// denied: the agent's own rules answered it.
func TestAnInStreamRequestForACallWithItsResultIsDenied(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	f.ToolStarted(ToolStep{Ref: "c1", Name: "commandExecution", Input: []byte(`{"command":"ls"}`)})
	f.ToolEnded("c1", ToolResult{Status: ToolOK})
	e, v := inStreamAsked(t, s, f, "req-1", "c1", "commandExecution", `{"command":"ls"}`)
	deniedWith(t, s, e, v, NotACallWords)
}

// One request per call: a second in-stream request for a call already
// asked about is denied "Already asked.", while the first stays open.
func TestASecondInStreamRequestForTheSameCallIsAlreadyAsked(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	f.ToolStarted(ToolStep{Ref: "c1", Name: "commandExecution", Input: []byte(`{"command":"ls"}`)})
	first, v := inStreamAsked(t, s, f, "req-1", "c1", "commandExecution", `{"command":"ls"}`)
	if v != nil || first.State != PermissionOpen {
		t.Fatalf("the first %+v, replied %+v", first, v)
	}
	e, v := inStreamAsked(t, s, f, "req-2", "c1", "commandExecution", `{"command":"ls"}`)
	deniedWith(t, s, e, v, AlreadyAskedWords)
	if got := entryOf(t, s, first.Seq); got.State != PermissionOpen {
		t.Fatalf("the first after the second %+v", got)
	}
	// Answered, it is still asked: a third is "Already asked." too.
	f.PermissionWithdrawn("req-1")
	e, v = inStreamAsked(t, s, f, "req-3", "c1", "commandExecution", `{"command":"ls"}`)
	deniedWith(t, s, e, v, AlreadyAskedWords)
}

// An in-stream request naming a call of an earlier turn is denied: a call
// is the current turn's alone.
func TestAnInStreamRequestForACallOfAnEarlierTurnIsDenied(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	f.ToolStarted(ToolStep{Ref: "c1", Name: "commandExecution", Input: []byte(`{"command":"ls"}`)})
	f.TurnEnded("done", Usage{})
	f.TurnStarted("")
	e, v := inStreamAsked(t, s, f, "req-1", "c1", "commandExecution", `{"command":"ls"}`)
	deniedWith(t, s, e, v, NotACallWords)
	// And between turns, no call is this session's.
	f.TurnEnded("done", Usage{})
	f.ToolStarted(ToolStep{Ref: "c2", Name: "commandExecution", Input: []byte(`{"command":"ls"}`)})
	e, v = inStreamAsked(t, s, f, "req-2", "c2", "commandExecution", `{"command":"ls"}`)
	deniedWith(t, s, e, v, NotACallWords)
}

// The floor holds on the in-stream path: a call announced and asked about
// in the stream is denied before any question when its command line names
// a denied place, when one of its changes' paths or the place a change
// moves a file to is denied, when the root it asks to write under is, when
// the folder its command runs in is, and when its command's relative words
// name a denied place from the folder it runs in.
func TestAnInStreamRequestIsFloored(t *testing.T) {
	data := realDir(t, t.TempDir())
	secret := filepath.Join(data, "secrets.json")
	if err := os.WriteFile(secret, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, f := protocolSession(t, Options{FileDeny: DenyList([]string{data}, nil)})
	f.TurnStarted("")
	j := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	change := func(path string, kind map[string]any) map[string]any {
		return map[string]any{"path": path, "kind": kind, "diff": ""}
	}
	for i, tc := range []struct {
		name, tool, input string
		floored           bool
	}{
		{"a line naming the place", "commandExecution", j(map[string]any{"command": "cat " + secret, "cwd": ""}), true},
		{"a change's path", "fileChange", j(map[string]any{"changes": []any{change(secret, map[string]any{"type": "update"})}}), true},
		{"a move's destination", "fileChange", j(map[string]any{"changes": []any{change("notes.txt", map[string]any{"type": "update", "move_path": filepath.Join(data, "x")})}}), true},
		{"a root to write under", "fileChange", j(map[string]any{"changes": []any{change("notes.txt", map[string]any{"type": "update"})}, "grantRoot": data}), true},
		{"a command's folder", "commandExecution", j(map[string]any{"command": "ls", "cwd": data}), true},
		{"a relative line from the folder it runs in", "commandExecution", j(map[string]any{"command": "cat " + filepath.Base(data) + "/secrets.json", "cwd": filepath.Dir(data)}), true},
		{"a call naming nothing of the list", "commandExecution", j(map[string]any{"command": "make test", "cwd": ""}), false},
	} {
		call := fmt.Sprintf("c-%d", i)
		f.ToolStarted(ToolStep{Ref: call, Name: tc.tool, Input: []byte(tc.input)})
		e, v := inStreamAsked(t, s, f, "req-"+call, call, tc.tool, tc.input)
		if !tc.floored {
			if v != nil || e.State != PermissionOpen {
				t.Fatalf("%s: %+v, replied %+v", tc.name, e, v)
			}
			continue
		}
		deniedWith(t, s, e, v, FloorWords)
	}
}

