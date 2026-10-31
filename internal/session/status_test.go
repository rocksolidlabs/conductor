package session

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// codexStatus is Codex's program status as it writes it (OSC 7501,
// ST-terminated, #197): 0.163's without a kind, 0.164's with one.
func codexStatus(state, kind string) string {
	seq := "\x1b]7501;state=" + state + ":app=codex"
	if kind != "" {
		seq += ":kind=" + kind
	}
	return seq + "\x1b\\"
}

// lastEntryIs waits until the session's newest attention entry is words,
// or fails the test after 5 s: an attention change's entry is recorded
// after the change, outside the session's lock (recordOwn).
func lastEntryIs(t *testing.T, s *Local, words string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := entries(s)
		if len(got) > 0 && got[len(got)-1] == words {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("entries %q, want the last %q", got, words)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// statusWorks reports whether the session shows working from the status;
// statusWaits whether it needs input on the status's say-so.
func statusWorks(a Attention) bool { return a.State == AttentionWorking && a.Source == SourceStatus }
func statusWaits(a Attention) bool { return a.State == AttentionNeedsInput && a.Source == SourceStatus }

// apiWaits reports whether the session shows a wait reported through the API.
func apiWaits(a Attention) bool { return a.State == AttentionNeedsInput && a.Source == SourceAPI }

// none reports the empty state.
func none(a Attention) bool { return a.State == AttentionNone }

// #197, the issue's case: a wait reported while Codex's title says Action
// Required for an async question it works on gives way to working after its
// TitleSettle, not before, because Codex's program status says working; the
// entry's source is the status.
func TestAWorkingStatusSettlesAWaitUnderAnActionRequiredTitle(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""), codexAction)
	raised := time.Now()
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	waitAttention(t, s, statusWorks)
	if d := time.Since(raised); d < settle {
		t.Fatalf("the wait gave way within %v", d)
	}
}

// A blocked status with a kind keeps a wait standing however long, and the
// title says nothing more; working lets it go, at once once it is older
// than its TitleSettle.
func TestABlockedStatusKeepsAWaitUntilWorking(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	out(p, codexStatus(StatusBlocked, "question"), codexAction)
	stays(t, s, settle+100*time.Millisecond, "the wait under a blocked status", apiWaits)
	out(p, codexStatus(StatusWorking, ""))
	waitAttention(t, s, statusWorks)
}

// Once a status has been read the window title is not weighed: a Working
// title under a blocked status settles nothing, and the blinking Action
// Required title under a working status disarms nothing, the wait settling
// after its second all the same.
func TestTheTitleIsNotWeighedOnceAStatusHasBeenRead(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusBlocked, "question"))
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	out(p, codexWorking(1), codexRedraw, codexWorking(2))
	stays(t, s, settle+100*time.Millisecond, "the wait under a Working title with a blocked status", apiWaits)
	out(p, codexStatus(StatusWorking, ""))
	waitAttention(t, s, statusWorks)
	raised := time.Now()
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which colour?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	out(p, codexAction, codexActionBlink, codexAction)
	waitAttention(t, s, statusWorks)
	if d := time.Since(raised); d < settle {
		t.Fatalf("the wait gave way within %v", d)
	}
}

// Codex 0.163's blocked without a kind may be an async question the agent
// works on: it raises nothing, keeps a wait standing as the title would, and
// working lets the wait go.
func TestAKindlessBlockedStatusRaisesNothingAndKeepsAWait(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	out(p, codexStatus(StatusBlocked, ""), codexAction)
	stays(t, s, 300*time.Millisecond, "no wait from a kindless blocked", func(a Attention) bool { return a.State == AttentionWorking })
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	stays(t, s, settle+100*time.Millisecond, "the wait under a kindless blocked", apiWaits)
	out(p, codexStatus(StatusWorking, ""))
	waitAttention(t, s, statusWorks)
}

// A blocked status with a kind raises a wait of its own where nothing else
// has, with words naming the kind: a form no pattern knows shows Needs you
// the moment Codex draws it. The same status again changes nothing; working
// settles it after its second.
func TestABlockedStatusRaisesAWaitWithItsKindsWords(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	out(p, codexStatus(StatusBlocked, "auth"))
	raised := time.Now()
	a := waitAttention(t, s, statusWaits)
	if a.Message != "The agent needs you to sign in" || a.Kind != "" || a.ID == "" {
		t.Fatalf("the status's wait: %+v", a)
	}
	n := len(entries(s))
	out(p, codexStatus(StatusBlocked, "auth"))
	time.Sleep(50 * time.Millisecond)
	if b := s.Info().Attention; b.ID != a.ID || len(entries(s)) != n {
		t.Fatalf("the same status again changed something: %+v, entries %v", b, entries(s))
	}
	out(p, codexStatus(StatusWorking, ""))
	waitAttention(t, s, statusWorks)
	if d := time.Since(raised); d < settle {
		t.Fatalf("the wait gave way within %v", d)
	}
	for _, k := range []struct{ kind, words, akind string }{{"permission", "The agent asks for a permission", KindPermission}, {"question", "The agent asks a question", KindPrompt}} {
		out(p, codexStatus(StatusBlocked, k.kind))
		if a := waitAttention(t, s, statusWaits); a.Message != k.words || a.Kind != k.akind {
			t.Fatalf("%s: %+v", k.kind, a)
		}
		out(p, codexStatus(StatusWorking, ""))
		waitAttention(t, s, statusWorks)
	}
}

// A status asks the event bucket for nothing, working or blocked: with the
// agent's reports refused (the bucket empty), a working status still shows
// working and a blocked one still raises its wait.
func TestAStatusSpendsNoTokenWhenTheBucketIsEmpty(t *testing.T) {
	s, p := newLocalWith(t, titled(time.Minute, Options{}))
	var refused bool
	for range 4 * EventBurst {
		if err := s.TrySetAttentionFull(AttentionDone, "spent", SourceAPI, KindDone, nil); errors.Is(err, ErrRateLimited) {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatal("the bucket never emptied")
	}
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	out(p, codexStatus(StatusWorking, ""))
	if a := s.Info().Attention; !statusWorks(a) {
		t.Fatalf("a working status with the bucket empty: %+v", a)
	}
	out(p, codexStatus(StatusBlocked, "permission"))
	if a := s.Info().Attention; !statusWaits(a) {
		t.Fatalf("a blocked status with the bucket empty: %+v", a)
	}
}

// A status and a bell in one read, in either order: one wait, with the
// status's words. The bell's wordless wait gives way to them; a bell after
// the status's wait is discarded as any bell while a wait shows.
func TestAStatusAndABellInOneReadMakeOneWaitWithTheStatusesWords(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	out(p, "\a"+codexStatus(StatusBlocked, "permission"))
	a := waitAttention(t, s, statusWaits)
	if a.Message != "The agent asks for a permission" {
		t.Fatalf("the bell first: %+v", a)
	}
	out(p, codexStatus(StatusWorking, ""))
	waitAttention(t, s, statusWorks)
	out(p, codexStatus(StatusBlocked, "permission")+"\a")
	a = waitAttention(t, s, statusWaits)
	time.Sleep(50 * time.Millisecond)
	if b := s.Info().Attention; b.ID != a.ID || b.Message != "The agent asks for a permission" {
		t.Fatalf("the bell after: %+v", b)
	}
	if got := entries(s); len(got) == 0 || got[len(got)-1] != "The agent asks for a permission" {
		t.Fatalf("entries %v", got)
	}
}

// The status is the agent's current state and outlives Conductor's turns:
// a working status read during the ended turn changes the done shown not at
// all, and the wait of the next turn, begun by an Enter, settles against it
// after its second with no new status written; a blocked status persisting
// into the next turn keeps that turn's wait.
func TestAStatusOutlivesTheTurn(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	if err := s.TrySetAttentionFull(AttentionDone, "finished", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	out(p, codexStatus(StatusWorking, ""))
	stays(t, s, 100*time.Millisecond, "the done under a working status", func(a Attention) bool { return a.State == AttentionDone })
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	raised := time.Now()
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	waitAttention(t, s, statusWorks)
	if d := time.Since(raised); d < settle {
		t.Fatalf("the next turn's wait gave way within %v", d)
	}
	out(p, codexStatus(StatusBlocked, "question"))
	waitAttention(t, s, statusWaits)
	if err := s.TrySetAttentionFull(AttentionDone, "finished", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which colour?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	stays(t, s, settle+100*time.Millisecond, "the next turn's wait under a blocked status", apiWaits)
}

// A wait the status raised is the status's to withdraw: idle, error or clear
// without a working (the form cancelled, the turn interrupted, Codex gone)
// withdraws it at once, a wait of another source beside it untouched; the
// person's Enter in the terminal withdraws it as a bell's.
func TestAStatusWithdrawsTheWaitItRaised(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	for _, end := range []string{StatusIdle, StatusError, StatusClear} {
		out(p, codexStatus(StatusBlocked, "auth"))
		waitAttention(t, s, statusWaits)
		out(p, codexStatus(end, ""))
		if a := waitAttention(t, s, none); a.Source != SourceScreen {
			t.Fatalf("%s: withdrawn with source %+v", end, a)
		}
		out(p, codexStatus(StatusWorking, ""))
	}
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	out(p, codexStatus(StatusBlocked, "auth"), codexStatus(StatusIdle, ""))
	stays(t, s, 200*time.Millisecond, "the API's wait beside a status withdrawn", apiWaits)
	out(p, codexStatus(StatusWorking, ""))
	waitAttention(t, s, statusWorks)
	out(p, codexStatus(StatusBlocked, "auth"))
	waitAttention(t, s, statusWaits)
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	if a := waitAttention(t, s, none); a.Source != SourceInput {
		t.Fatalf("the Enter: %+v", a)
	}
}

// A status Conductor does not understand (a kind added in a later Codex, a
// sequence cut at the capture) is a state declared, not nothing: a working
// read earlier no longer counts, the title stays retired, a bell's wait for
// the form stands, and the next working settles it.
func TestAStatusNotUnderstoodIsReadConservatively(t *testing.T) {
	const settle = 200 * time.Millisecond
	for _, odd := range []string{"\x1b]7501;state=blocked:app=codex:kind=review\x1b\\", "\x1b]7501;state=working:app=" + string(make([]byte, 300)) + "\x1b\\"} {
		s, p := newLocalWith(t, titled(settle, Options{}))
		out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
		out(p, odd, "\a", codexAction)
		waitAttention(t, s, func(a Attention) bool { return waits(a) && a.Source == SourceBell })
		out(p, codexWorking(3))
		stays(t, s, settle+100*time.Millisecond, "the bell's wait under a status not understood", waits)
		out(p, codexStatus(StatusWorking, ""))
		waitAttention(t, s, statusWorks)
	}
}

// screenAsks reports whether a question read off the screen shows.
func screenAsks(a Attention) bool {
	return a.State == AttentionNeedsInput && a.Source == SourceQuestion
}

// A question the session holds on the screen (Codex's approval, read by the
// catalog's pattern) is untouched by the statuses read while it shows, as
// under a Working title: a blocked status is kept out by the dialog's better
// words, a working one by the dialog on the screen. The dialog gone (a key
// typed in the terminal answered it), the program's last word stands:
// working, as the status's, with its entry (#281: Codex writes it as its
// overlay closes, and never again).
func TestStatusesLeaveAQuestionHeldOnTheScreen(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	att := waitAttention(t, s, screenAsks)
	out(p, codexStatus(StatusBlocked, "permission"), codexStatus(StatusWorking, ""), codexStatus(StatusBlocked, "permission"), codexStatus(StatusWorking, ""))
	stays(t, s, settle+100*time.Millisecond, "the approval under statuses", func(a Attention) bool { return screenAsks(a) && a.ID == att.ID })
	lastEntryIs(t, s, att.Message)
	before := len(entries(s))
	p.outW.Write([]byte(approvalGone("✔ You approved codex to run curl this time\r\n› ")))
	if a := waitAttention(t, s, statusWorks); a.Since == nil {
		t.Fatalf("working after the dialog, without its time: %+v", a)
	}
	stays(t, s, 200*time.Millisecond, "working after the dialog with the status working", statusWorks)
	// The reconciled status's entry is recorded after the lock is let go
	// (recordStatusReconciled); the window above is not what orders it.
	lastEntryIs(t, s, string(AttentionWorking))
	if got := entries(s); len(got) != before+1 || got[len(got)-1] != string(AttentionWorking) {
		t.Fatalf("entries after the dialog: %v (had %d)", got, before)
	}
}

// A Codex approval answered through Conductor (a row's choice, the chat's),
// the program's status written as its overlay closes: before the answer is
// taken or after it, working follows the answer either way, with its entry
// (#281, codexworking.spec.ts:300, which failed when the status came first).
// Here it comes first, kept out by the dialog on the screen, and the answer
// takes it up; the dialog leaving the screen then changes nothing.
func TestAWorkingStatusReadUnderADialogShowsOnceTheDialogIsAnswered(t *testing.T) {
	var mu sync.Mutex
	var states []AttentionState
	s, p := newLocalWith(t, titled(200*time.Millisecond, Options{OnActivity: func(_ string, e ActivityEntry, st AttentionState) {
		if e.Type == ActivityAttention {
			mu.Lock()
			states = append(states, st)
			mu.Unlock()
		}
	}}))
	go func() {
		for range p.input {
		}
	}()
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	p.outW.Write([]byte(approvalDialog("ls -la")))
	att := waitAttention(t, s, screenAsks)
	out(p, codexStatus(StatusBlocked, "permission"), codexStatus(StatusWorking, ""))
	stays(t, s, 100*time.Millisecond, "the approval under the status", func(a Attention) bool { return screenAsks(a) && a.ID == att.ID })
	sink := newChanSink(false)
	sub, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, sink)
	if err := s.AnswerInput(t.Context(), sub, att.ID, []byte("y")); err != nil {
		t.Fatal(err)
	}
	if a := waitAttention(t, s, statusWorks); a.Since == nil {
		t.Fatalf("working at the answer, without its time: %+v", a)
	}
	p.outW.Write([]byte(approvalGone("✔ You approved codex to run ls -la\r\n› ")))
	stays(t, s, 300*time.Millisecond, "working once the dialog left", statusWorks)
	mu.Lock()
	got := append([]AttentionState(nil), states...)
	mu.Unlock()
	if n := len(got); n < 2 || got[n-1] != AttentionWorking || got[n-2] != AttentionNeedsInput {
		t.Fatalf("the states told: %v", got)
	}
}

// The agent's own question (Ask) under a working status is set aside as under
// a Working title and shows again at the turn's done, where the status arms
// nothing.
func TestAWorkingStatusSetsTheAgentsOwnQuestionAsideUntilItsDone(t *testing.T) {
	s, p, _ := asking(t, titled(150*time.Millisecond, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	shows(t, s, AttentionWorking, "the turn's status")
	q := askOwn(t, s, "Which database?")
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionWorking })
	if _, aside := keeps(s); !aside {
		t.Fatal("the settled question is not set aside")
	}
	reportState(t, s, AttentionDone, "asked")
	showsOwn(t, s, q, "at the done")
	time.Sleep(300 * time.Millisecond)
	showsOwn(t, s, q, "a second after the done")
}

// A blocked status read while a question held on the screen shows is kept
// out by that question's better words and, Codex not writing it again, is
// remembered: once the screen moves on from the held question (a form no
// pattern knows in its place), the status's wait shows with its words and
// its entry, and working settles it.
func TestABlockedStatusKeptOutByAHeldQuestionShowsOnceThatQuestionGoes(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	att := waitAttention(t, s, screenAsks)
	// out returns once the pump has scanned the status, which it reads as
	// it scans (readStatus): read, and weighed against the approval.
	out(p, codexStatus(StatusBlocked, "auth"))
	s.mu.Lock()
	last, kind := s.lastStatus, s.statusKind
	s.mu.Unlock()
	if last != StatusBlocked || kind != "auth" {
		t.Fatalf("the status read %q %q", last, kind)
	}
	if a := s.Info().Attention; a.ID != att.ID {
		t.Fatalf("the status replaced the approval: %+v", a)
	}
	p.outW.Write([]byte(approvalGone("Sign in to continue\r\n")))
	a := waitAttention(t, s, statusWaits)
	if a.Message != "The agent needs you to sign in" || a.ID == "" || a.ID == att.ID {
		t.Fatalf("the status's wait: %+v", a)
	}
	// Its entry is recorded after the change, once the session has let its
	// lock go (recordStatusReconciled): waited for, not read at once.
	lastEntryIs(t, s, "The agent needs you to sign in")
	out(p, codexStatus(StatusWorking, ""))
	waitAttention(t, s, statusWorks)
}
