package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	f.Permission(PermissionRequest{Ref: "req-past", Tool: "Bash", Input: []byte(`{}`)})
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
