package crew

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A launch's first message (P19): typed by TypeFirstMessage once the
// session is ready, held behind every startup question, withdrawn by a
// person's keys, dropped when the session ends. The readiness wait looks
// only when the test hands it a tick (readyWait.ticks), so that what the
// screen shows at each look is the test's to say.

const firstText = "fix the parser"

// firstSession is a session over a fake process with opts, its startup
// questions among them, and a controlling viewer.
func firstSession(t *testing.T, opts session.Options) (*session.Local, *sessiontest.FakeProc, *session.Subscription) {
	t.Helper()
	p := sessiontest.NewFakeProc()
	t.Cleanup(func() { p.End(0) })
	opts.Log = quietLog
	if opts.SubmitPause == 0 {
		opts.SubmitPause = 5 * time.Millisecond
	}
	l := session.NewLocal(session.Info{ID: session.NewID(), Name: "first", Cwd: t.TempDir(), Cols: 80, Rows: 24}, p, opts)
	sub, err := l.Attach("", session.RoleControl, "", 80, 24, nopSink{})
	if err != nil {
		t.Fatal(err)
	}
	return l, p, sub
}

// typeFirst starts TypeFirstMessage on l, its wait looking at each tick
// sent on the channel it returns, and returns that channel and one closed
// once it has returned.
func typeFirst(t *testing.T, l *session.Local, fm FirstMessage) (chan<- time.Time, <-chan struct{}) {
	t.Helper()
	ticks := make(chan time.Time)
	if fm.Wait == nil {
		fm.Wait = readyWait{poll: time.Hour, ticks: ticks}.await
	}
	fm.Text, fm.poll = firstText, time.Millisecond
	l.SetFirstMessage("waiting")
	done := make(chan struct{})
	go func() {
		defer close(done)
		TypeFirstMessage(t.Context(), l, fm)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("TypeFirstMessage did not return")
		}
	})
	return ticks, done
}

// entriesWith is l's activity entries of type typ whose words hold text.
func entriesWith(l *session.Local, typ, text string) []session.ActivityEntry {
	var out []session.ActivityEntry
	for _, e := range l.Activity() {
		if e.Type == typ && strings.Contains(e.Message, text) {
			out = append(out, e)
		}
	}
	return out
}

// untilEntries waits for n entries of type typ with text.
func untilEntries(t *testing.T, l *session.Local, typ, text string, n int) {
	t.Helper()
	waitTrue(t, text, func() bool { return len(entriesWith(l, typ, text)) >= n })
}

// untilShown waits until l shows what ok asks for.
func untilShown(t *testing.T, l *session.Local, what string, ok func(session.Attention) bool) session.Attention {
	t.Helper()
	waitTrue(t, what, func() bool { return ok(l.Info().Attention) })
	return l.Info().Attention
}

// startupShown says the session shows a startup question.
func startupShown(a session.Attention) bool {
	return a.State == session.AttentionNeedsInput && a.Source == session.SourceTrust
}

// returned waits for TypeFirstMessage to return: within its own bounds,
// a hang failing the test rather than outlasting it.
func returned(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("TypeFirstMessage did not return")
	}
}

// noInput fails when anything was written to p.
func noInput(t *testing.T, p *sessiontest.FakeProc) {
	t.Helper()
	select {
	case b := <-p.Input:
		t.Fatalf("written %q", b)
	default:
	}
}

// tickUntilTyped hands ticks until the message's text and its Enter are
// written, and returns what was written, in order.
func tickUntilTyped(t *testing.T, ticks chan<- time.Time, p *sessiontest.FakeProc) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	var got string
	for !strings.HasSuffix(got, "\r") || !strings.Contains(got, firstText) {
		select {
		case ticks <- time.Now():
		case b := <-p.Input:
			got += string(b)
		case <-deadline:
			t.Fatalf("the first message was not typed: written %q", got)
		}
	}
	return got
}

var trustRe = regexp.MustCompile(`Trust\s*this\s*folder\?`)

const trustScreen = "\x1b[2J\x1b[HTrust this folder?\r\n› 1. Yes, continue\r\n  2. No, quit\r\n"

// The trust question holds the message while it shows, noted once, with
// no cap; a person's Enter answers it and leaves the message waiting; the
// prompt drawn, the message is typed after the person's key, once, as
// Conductor's own line by launch, and the session no longer says it waits.
func TestAFirstMessageWaitsForTheTrustQuestion(t *testing.T) {
	l, p, sub := firstSession(t, session.Options{TrustPattern: trustRe, TrustAnswers: []session.Option{{Label: "Yes", Input: "\r"}}})
	p.Print(trustScreen)
	untilShown(t, l, "the trust question", startupShown)
	ticks, done := typeFirst(t, l, FirstMessage{})
	untilEntries(t, l, session.ActivityStatus, `the first message waits: answer "Trust this folder?" first`, 1)
	ticks <- time.Now()
	ticks <- time.Now()
	noInput(t, p)
	if err := l.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	if b := <-p.Input; string(b) != "\r" {
		t.Fatalf("written %q", b)
	}
	p.Print("\x1b[2J\x1b[H> ")
	untilShown(t, l, "past the question", func(a session.Attention) bool { return !startupShown(a) })
	if got := tickUntilTyped(t, ticks, p); !strings.Contains(got, firstText) {
		t.Fatalf("written %q", got)
	}
	returned(t, done)
	if n := len(entriesWith(l, session.ActivityStatus, "the first message waits")); n != 1 {
		t.Fatalf("%d notes of the wait, want one", n)
	}
	typed := entriesWith(l, session.ActivityInput, firstText)
	if len(typed) != 1 || typed[0].ByName != FirstMessageBy || typed[0].Op != session.OpTyped {
		t.Fatalf("the message's entries: %+v", typed)
	}
	if fm := l.Info().FirstMessage; fm != "" {
		t.Fatalf("the session says its first message is %q", fm)
	}
}

// An update dialog, the trust question drawn straight after its answer
// (no look between), then the sign-in screen the program leaves by
// itself: one note each, three, and the message typed only after the
// last.
func TestAFirstMessageWaitsForEachStartupQuestion(t *testing.T) {
	l, p, sub := firstSession(t, session.Options{
		TrustPattern: trustRe, TrustAnswers: []session.Option{{Label: "Yes", Input: "1"}},
		Questions: []session.StartupQuestion{
			{Pattern: regexp.MustCompile(`(?i)update\s*now\s*\(\s*runs`), Words: regexp.MustCompile(`(?i)update\s*available`), Answers: []session.Option{{Label: "Skip until next version", Input: "3"}}},
			{Pattern: regexp.MustCompile(`(?i)paste\s*code\s*here\s*if\s*prompted`), Words: regexp.MustCompile(`(?i)browser\s*didn.t\s*open\?\s*use\s*the\s*url\s*below\s*to\s*sign\s*in`)},
		},
	})
	p.Print("\x1b[2J\x1b[HUpdate available 0.1.0 -> 0.2.0\r\n› 1. Update now (runs the installer)\r\n  2. Skip\r\n  3. Skip until next version\r\n")
	update := untilShown(t, l, "the update dialog", startupShown)
	ticks, done := typeFirst(t, l, FirstMessage{})
	untilEntries(t, l, session.ActivityStatus, "the first message waits", 1)

	// Its answer, and the trust question drawn straight after it.
	if err := l.Input(sub, []byte("3")); err != nil {
		t.Fatal(err)
	}
	<-p.Input
	p.Print(trustScreen)
	trust := untilShown(t, l, "the trust question", func(a session.Attention) bool { return startupShown(a) && a.ID != update.ID })
	ticks <- time.Now()
	untilEntries(t, l, session.ActivityStatus, "the first message waits", 2)
	noInput(t, p)

	if err := l.Input(sub, []byte("1")); err != nil {
		t.Fatal(err)
	}
	<-p.Input
	p.Print("\x1b[2J\x1b[HWelcome\r\n\r\n Browser didn't open? Use the url below to sign in (c to copy)\r\n\r\nhttps://example.invalid/sign-in\r\n\r\n Paste code here if prompted > ")
	untilShown(t, l, "the sign-in screen", func(a session.Attention) bool { return startupShown(a) && a.ID != trust.ID })
	ticks <- time.Now()
	untilEntries(t, l, session.ActivityStatus, "the first message waits", 3)
	noInput(t, p)

	// Signed in through the browser: the program leaves the screen itself.
	p.Print("\x1b[2J\x1b[H> ")
	untilShown(t, l, "past the sign-in", func(a session.Attention) bool { return !startupShown(a) })
	tickUntilTyped(t, ticks, p)
	returned(t, done)
	if n := len(entriesWith(l, session.ActivityStatus, "the first message waits")); n != 3 {
		t.Fatalf("%d notes of the wait, want three", n)
	}
}

// A person's keys at the prompt while the message waits withdraw it: the
// wait ends at once, nothing of the message is written, the session's
// Activity says why, and it no longer says a message waits. The prompt
// is drawn, so the session is ready the moment the wait looks: the typer
// is held in its wait (a gate), the keys typed once it is there and in,
// so that they come first by the test's order, not the scheduler's; the
// gate is opened after them, and the message, withdrawn, is still not
// typed.
func TestAPersonsKeysWithdrawTheFirstMessage(t *testing.T) {
	l, p, sub := firstSession(t, session.Options{})
	p.Print("> ")
	gate, waiting := make(chan struct{}), make(chan struct{})
	hold := func(ctx context.Context, _ *session.Local, _ func(string)) error {
		close(waiting)
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	_, done := typeFirst(t, l, FirstMessage{Wait: hold})
	// The typer is in its wait, ready to type once let go.
	<-waiting
	for _, k := range []string{"ls", "\r"} {
		if err := l.Input(sub, []byte(k)); err != nil {
			t.Fatal(err)
		}
		if b := <-p.Input; string(b) != k {
			t.Fatalf("written %q", b)
		}
	}
	close(gate)
	returned(t, done)
	noInput(t, p)
	untilEntries(t, l, session.ActivityStatus, "the first message was not typed: a person typed in the session first", 1)
	if fm := l.Info().FirstMessage; fm != "" {
		t.Fatalf("the session says its first message is %q", fm)
	}
}

// A session that ends while the message waits gets nothing: the call
// returns, nothing is written, and one entry says why.
func TestAFirstMessageIsDroppedWhenTheSessionEnds(t *testing.T) {
	l, p, _ := firstSession(t, session.Options{})
	_, done := typeFirst(t, l, FirstMessage{})
	p.End(0)
	returned(t, done)
	noInput(t, p)
	if n := len(entriesWith(l, session.ActivityStatus, "the first message was not typed: the session ended first")); n != 1 {
		t.Fatalf("%d notes of the end, want one", n)
	}
}

// notReady is a readiness wait that answers at once that the session was
// not ready by the cap.
func notReady(context.Context, *session.Local, func(string)) error { return errNotReady }

// Past the cap, an agent with a recognised prompt gets the message only
// at that prompt: it waits, noted once, while the screen is another, and
// is typed once the prompt is drawn.
func TestAFirstMessagePastTheCapWaitsForTheAgentsPrompt(t *testing.T) {
	l, p, _ := firstSession(t, session.Options{PromptShape: regexp.MustCompile(`(?m)^agent> *$`)})
	p.Print("Loading the agent…")
	_, done := typeFirst(t, l, FirstMessage{Wait: notReady, PromptShape: true})
	untilEntries(t, l, session.ActivityStatus, "the first message waits for the agent's prompt", 1)
	noInput(t, p)
	p.Print("\x1b[2J\x1b[Hagent> ")
	got := ""
	for !strings.HasSuffix(got, "\r") {
		got += string(<-p.Input)
	}
	returned(t, done)
	if !strings.Contains(got, firstText) {
		t.Fatalf("written %q", got)
	}
	if n := len(entriesWith(l, session.ActivityStatus, "the first message waits for the agent's prompt")); n != 1 {
		t.Fatalf("%d notes, want one", n)
	}
}

// Past the cap, an agent without a recognised prompt gets the message
// typed anyway, as a crew's prompt is.
func TestAFirstMessagePastTheCapIsTypedAnyway(t *testing.T) {
	l, p, _ := firstSession(t, session.Options{})
	p.Print("Loading the agent…")
	_, done := typeFirst(t, l, FirstMessage{Wait: notReady})
	got := ""
	for !strings.HasSuffix(got, "\r") {
		got += string(<-p.Input)
	}
	returned(t, done)
	if !strings.Contains(got, firstText) {
		t.Fatalf("written %q", got)
	}
}
