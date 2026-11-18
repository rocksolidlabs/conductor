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
	if opts.PromptShape == nil {
		// The agent's own prompt, recognised: a row "> " alone.
		opts.PromptShape = promptRe
	}
	if opts.SubmitPause == 0 {
		opts.SubmitPause = 5 * time.Millisecond
	}
	if opts.EchoWait == 0 {
		// The test's agent draws the text as soon as it reads it (typedOn):
		// this bounds a hang alone.
		opts.EchoWait = 30 * time.Second
	}
	l := session.NewLocal(session.Info{ID: session.NewID(), Name: "first", Cwd: t.TempDir(), Cols: 80, Rows: 24}, p, opts)
	sub, err := l.Attach("", session.RoleControl, "", 80, 24, nopSink{})
	if err != nil {
		t.Fatal(err)
	}
	return l, p, sub
}

// promptRe is the test agent's prompt shape: a row "> " and what is
// typed after it.
var promptRe = regexp.MustCompile(`(?m)^>( [^\n]*)?$`)

// typeFirst starts TypeFirstMessage on l, for an agent whose prompt the
// server knows, its wait looking at each tick
// sent on the channel it returns, and returns that channel and one closed
// once it has returned.
func typeFirst(t *testing.T, l *session.Local, fm FirstMessage) (chan<- time.Time, <-chan struct{}) {
	t.Helper()
	ticks := make(chan time.Time)
	if fm.Wait == nil {
		fm.Wait = readyWait{poll: time.Hour, ticks: ticks}.await
	}
	fm.Text, fm.poll, fm.PromptShape = firstText, time.Millisecond, true
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
// written, and returns what was written, in order: typedOn with ticks.
func tickUntilTyped(t *testing.T, ticks chan<- time.Time, p *sessiontest.FakeProc) string {
	t.Helper()
	return typedOn(t, ticks, p, "> ")
}

// typedOn reads what is written to p until the message's text and its
// Enter are, handing ticks meanwhile when ticks is set; as the text
// arrives the agent draws it in its prompt's row, after prompt, as an
// agent echoes what it is given. It returns what was written, in order.
func typedOn(t *testing.T, ticks chan<- time.Time, p *sessiontest.FakeProc, prompt string) string {
	t.Helper()
	deadline := time.After(30 * time.Second)
	var got string
	for !strings.HasSuffix(got, "\r") || !strings.Contains(got, firstText) {
		select {
		case ticks <- time.Now():
		case b := <-p.Input:
			got += string(b)
			if strings.Contains(string(b), firstText) {
				_ = p.Print("\r\x1b[2K" + prompt + string(b))
			}
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
	l, p, _ := firstSession(t, session.Options{PromptShape: regexp.MustCompile(`(?m)^›( [^\n]*)?$`)})
	p.Print("Loading the agent…")
	_, done := typeFirst(t, l, FirstMessage{Wait: notReady, PromptShape: true})
	untilEntries(t, l, session.ActivityStatus, "the first message waits for the agent's prompt", 1)
	noInput(t, p)
	p.Print("\x1b[2J\x1b[H› ")
	got := typedOn(t, nil, p, "› ")
	returned(t, done)
	if !strings.Contains(got, firstText) {
		t.Fatalf("written %q", got)
	}
	if n := len(entriesWith(l, session.ActivityStatus, "the first message waits for the agent's prompt")); n != 1 {
		t.Fatalf("%d notes, want one", n)
	}
}

// An agent whose prompt the server does not know gets no first message
// at all, ready or not, past the cap or not: nothing is written, and the
// session's Activity says why.
func TestAFirstMessageIsNeverTypedWithoutAKnownPrompt(t *testing.T) {
	for _, wait := range []func(context.Context, *session.Local, func(string)) error{ready, notReady} {
		l, p, _ := firstSession(t, session.Options{})
		p.Print("> ")
		l.SetFirstMessage("waiting")
		TypeFirstMessage(t.Context(), l, FirstMessage{Text: firstText, Wait: wait, poll: time.Millisecond})
		noInput(t, p)
		if n := len(entriesWith(l, session.ActivityStatus, "the first message was not typed: Conductor does not know this agent's prompt")); n != 1 {
			t.Fatalf("%d notes, want one", n)
		}
		if fm := l.Info().FirstMessage; fm != "" {
			t.Fatalf("the session says its first message is %q", fm)
		}
	}
}

// ready is a readiness wait that answers at once that the session is
// ready: quiet, no known startup question showing.
func ready(context.Context, *session.Local, func(string)) error { return nil }

// A first message whose context has ended (the server's stop) is typed by
// no one, also when its session is ready at its prompt: nothing is
// written, nothing noted, and the session no longer says it waits.
func TestAFirstMessageOfAStoppedServerIsNeverTyped(t *testing.T) {
	for range 20 {
		l, p, _ := firstSession(t, session.Options{})
		p.Print("> ")
		waitTrue(t, "the prompt drawn", func() bool { return strings.Contains(strings.Join(l.ScreenLines(24), "\n"), ">") })
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		l.SetFirstMessage("waiting")
		TypeFirstMessage(ctx, l, FirstMessage{Text: firstText, PromptShape: true, Wait: ready, poll: time.Millisecond})
		noInput(t, p)
		if fm := l.Info().FirstMessage; fm != "" {
			t.Fatalf("the session says its first message is %q", fm)
		}
		if n := len(entriesWith(l, session.ActivityStatus, "first message")); n != 0 {
			t.Fatalf("%d notes of the first message", n)
		}
	}
}

// Before the cap, the wait saying ready is not enough: a quiet screen that
// is not the agent's prompt (one the catalog does not know) gets nothing,
// noted once, and the message is typed once the prompt is drawn.
func TestAFirstMessageWaitsForTheAgentsPromptBeforeTheCap(t *testing.T) {
	l, p, _ := firstSession(t, session.Options{})
	p.Print("Choose a colour scheme:\r\n  Dark\r\n  Light\r\n")
	_, done := typeFirst(t, l, FirstMessage{Wait: ready})
	untilEntries(t, l, session.ActivityStatus, "the first message waits for the agent's prompt", 1)
	noInput(t, p)
	p.Print("\x1b[2J\x1b[H> ")
	got := typedOn(t, nil, p, "> ")
	returned(t, done)
	if !strings.Contains(got, firstText) {
		t.Fatalf("written %q", got)
	}
	if n := len(entriesWith(l, session.ActivityStatus, "the first message waits for the agent's prompt")); n != 1 {
		t.Fatalf("%d notes, want one", n)
	}
}

// A person who types in the pause between the message's text and its
// Enter withdraws the Enter and is held to nothing: the Enter is never
// written, the session shows no question of its own, the agent's next
// report shows, and the Activity has the one line. The pause is longer
// than the test: only the person's key ends it.
func TestAPersonsKeyBeforeTheFirstMessagesEnterHoldsNothing(t *testing.T) {
	l, p, sub := firstSession(t, session.Options{SubmitPause: time.Hour})
	p.Print("> ")
	_, done := typeFirst(t, l, FirstMessage{Wait: ready})
	if b := <-p.Input; !strings.Contains(string(b), firstText) {
		t.Fatalf("written %q", b)
	}
	if err := l.Input(sub, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if b := <-p.Input; string(b) != "x" {
		t.Fatalf("written %q", b)
	}
	returned(t, done)
	noInput(t, p)
	// A hold is set before the submission returns: none is.
	if l.TurnHeld() {
		t.Fatal("the session's turn is held")
	}
	if a := l.Info().Attention; a.State == session.AttentionNeedsInput {
		t.Fatalf("the session shows %+v", a)
	}
	if err := l.TrySetAttentionFull(session.AttentionWorking, "", session.SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	if a := l.Info().Attention; a.State != session.AttentionWorking {
		t.Fatalf("the agent's working shows %+v", a)
	}
	if n := len(entriesWith(l, session.ActivityStatus, "the first message was typed without its Enter: a person typed in the session first")); n != 1 {
		t.Fatalf("%d notes, want one", n)
	}
}

// When the message's Enter is left out, the words say what is true, and
// the session says the message is typed and not sent until a person's
// key: with no question come, that the screen was not the prompt holding
// the text (the agent drew it otherwise: folded as a paste); with a
// question drawn in its place, that the question comes first.
func TestAFirstMessageTypedAndNotSentSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name, note string
		after      string
	}{
		{"no question", "the first message is typed in the agent's input and was not sent: press Enter in the terminal to send it", "\r\x1b[2K> [Pasted text #1]"},
		{"a question in its place", "the first message was typed without its Enter: answer the question, then press Enter in the terminal", trustScreen},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, p, sub := firstSession(t, session.Options{EchoWait: 100 * time.Millisecond, TrustPattern: trustRe, TrustAnswers: []session.Option{{Label: "Yes", Input: "\r"}}})
			p.Print("> ")
			_, done := typeFirst(t, l, FirstMessage{Wait: ready})
			if b := <-p.Input; string(b) != firstText {
				t.Fatalf("written %q", b)
			}
			_ = p.Print(tc.after)
			returned(t, done)
			noInput(t, p)
			if n := len(entriesWith(l, session.ActivityStatus, tc.note)); n != 1 {
				t.Fatalf("%d notes %q, want one: %+v", n, tc.note, l.Activity())
			}
			if fm := l.Info().FirstMessage; fm != "typed" {
				t.Fatalf("the session says its first message is %q, want typed", fm)
			}
			// A person's key: what sits in the input is theirs now.
			if err := l.Input(sub, []byte("\r")); err != nil {
				t.Fatal(err)
			}
			<-p.Input
			if fm := l.Info().FirstMessage; fm != "" {
				t.Fatalf("after a person's key the session says %q", fm)
			}
		})
	}
}

// A launch's update dialog drawn in a window too short for its heading from
// its first draw is no question the session reads whole: nothing asks it,
// and the first message is refused at each try. The session's Activity
// says so once, however many tries: the agent shows a dialog Conductor
// does not read whole, answered in its terminal. Nothing is typed into it;
// once a key typed in the terminal answers it and the prompt shows, the
// message goes in.
func TestAFirstMessageSaysOnceThatADialogIsNotReadWhole(t *testing.T) {
	l, p, sub := firstSession(t, session.Options{Questions: []session.StartupQuestion{{
		Pattern: regexp.MustCompile(`(?i)update\s*now\s*\(\s*runs`),
		Words:   regexp.MustCompile(`(?i)(update\s*available)\W*?(\d+(?:\.\d+)+\s*(?:→|->)\s*\d+(?:\.\d+)+)`),
		Answers: []session.Option{{Label: "Skip this update", Input: "\x1b"}, {Label: "Skip until next version", Input: "3"}},
	}}})
	if err := l.Resize(sub, 80, 5); err != nil {
		t.Fatal(err)
	}
	p.Print("\x1b[2J\x1b[H› 1. Update now (runs `npm install -g @openai/codex`)\r\n  2. Skip\r\n  3. Skip until next version\r\n\r\n  enter continue · esc skip")
	waitTrue(t, "the dialog on the screen", l.UnreadDialog)
	tries := make(chan struct{}, 64)
	wait := func(context.Context, *session.Local, func(string)) error {
		select {
		case tries <- struct{}{}:
		default:
		}
		return nil
	}
	_, done := typeFirst(t, l, FirstMessage{Wait: wait})
	for range 4 {
		<-tries
	}
	const note = "the first message waits: the agent shows a dialog Conductor does not read whole; answer it in its terminal"
	if n := len(entriesWith(l, session.ActivityStatus, note)); n != 1 {
		t.Fatalf("%d notes, want one: %+v", n, l.Activity())
	}
	noInput(t, p)
	if err := l.Input(sub, []byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	if b := <-p.Input; string(b) != "\x1b" {
		t.Fatalf("written %q", b)
	}
	p.Print("\x1b[2J\x1b[H> ")
	if got := typedOn(t, nil, p, "> "); !strings.Contains(got, firstText) {
		t.Fatalf("written %q", got)
	}
	returned(t, done)
	if n := len(entriesWith(l, session.ActivityStatus, note)); n != 1 {
		t.Fatalf("%d notes, want one", n)
	}
}
