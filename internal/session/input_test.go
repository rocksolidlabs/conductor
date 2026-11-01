package session

import (
	"context"
	"errors"
	"io"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// stopTaking makes p take no input until the returned function is called,
// or the test ends: every write to it waits, as one to a program that
// stopped reading its terminal does once the terminal's buffer is full.
func stopTaking(t *testing.T, p *fakeProc) (takeAgain func()) {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	takeAgain = func() { once.Do(func() { close(release) }) }
	p.onWrite = func() { <-release }
	t.Cleanup(takeAgain)
	return takeAgain
}

// writesWaiting is how many writes wait for the session's writer, and
// whether it is writing one.
func writesWaiting(s *Local) (waiting int, writing bool) {
	s.writer.mu.Lock()
	defer s.writer.mu.Unlock()
	return len(s.writer.queue), s.writer.running
}

// untilWaiting waits for n writes to wait behind the one being written.
func untilWaiting(t *testing.T, s *Local, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		waiting, writing := writesWaiting(s)
		if waiting == n && writing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d writes waiting (writing %v), want %d", waiting, writing, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// untilIdle waits until the writer has no write in hand: the last one made
// has been reported to whoever waited for it. What the process received is
// not that: it has the bytes a moment before the writer reports the write.
func untilIdle(t *testing.T, s *Local) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		waiting, writing := writesWaiting(s)
		if waiting == 0 && !writing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d writes waiting (writing %v), want none", waiting, writing)
		}
		time.Sleep(time.Millisecond)
	}
}

// untilWrites waits until n writes have reached the process, as its onWrite
// counted them in writes: the n-th has begun, though it may wait there.
// (untilWaiting cannot tell it from the write before it, which the writer
// may still be reporting.)
func untilWrites(t *testing.T, writes *atomic.Int32, n int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for writes.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d writes reached the process, want %d", writes.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// written returns the next write the process took.
func written(t *testing.T, p *fakeProc) string {
	t.Helper()
	select {
	case b := <-p.input:
		return string(b)
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was written to the process")
	}
	return ""
}

// nothingWritten fails when the process takes a write within d.
func nothingWritten(t *testing.T, p *fakeProc, d time.Duration) {
	t.Helper()
	select {
	case b := <-p.input:
		t.Fatalf("the process was written %q", b)
	case <-time.After(d):
	}
}

// sawOutput waits for sink to have been sent output that holds text.
func sawOutput(t *testing.T, sink *chanSink, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var out []byte
		for i := range sink.count() {
			if f, err := proto.Decode(sink.frame(i)); err == nil && (f.Type == proto.TypeOutput || f.Type == proto.TypeScrollback) {
				out = append(out, f.Payload...)
			}
		}
		if strings.Contains(string(out), text) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no output %q; saw %q", text, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A controller's keystrokes to a program that takes nothing fail in their
// time; those that never began are never written, while the one that began
// lands once the program reads again, and what comes after is written at
// once. Nothing is left waiting meanwhile: the connection, other viewers
// and the output go on.
func TestInputToAProgramThatTakesNoInputFailsInItsTime(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 200 * time.Millisecond})
	takeAgain := stopTaking(t, p)
	sub := ownerOf(t, s)
	before := runtime.NumGoroutine()
	for i, k := range []string{"a", "b", "c", "d"} {
		start := time.Now()
		err := returnsWithin(t, 5*time.Second, "input "+k, func() error { return s.Input(sub, []byte(k)) })
		if !errors.Is(err, ErrInputStalled) {
			t.Fatalf("input %s: %v", k, err)
		}
		// Once the writer has waited its time on one, the program is not
		// reading: the next are refused at once, not a wait each.
		if took := time.Since(start); i > 0 && took > 100*time.Millisecond {
			t.Fatalf("input %s waited %v on a program known not to read", k, took)
		}
	}
	if waiting, _ := writesWaiting(s); waiting != 0 {
		t.Fatalf("%d writes still wait after their time", waiting)
	}
	// The writer waiting on the program, and the begun keystrokes' wait to
	// finish what follows them, are what is left: none for those refused
	// (others' winding down from earlier tests may still count a moment).
	for deadline := time.Now().Add(5 * time.Second); runtime.NumGoroutine()-before > 2; {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines more after four keystrokes the program did not take", runtime.NumGoroutine()-before)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The session goes on for its other viewers.
	other := newChanSink(false)
	if _, err := s.AttachWith(AttachOptions{Role: RoleView, Name: "other"}, other); err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte("still printing"))
	sawOutput(t, other, "still printing")
	takeAgain()
	if got := written(t, p); got != "a" {
		t.Fatalf("written %q, want the keystroke whose write had begun", got)
	}
	nothingWritten(t, p, 200*time.Millisecond)
	if err := s.Input(sub, []byte("e")); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "e" {
		t.Fatalf("written %q", got)
	}
}

// Writes that wait for the program are written in the order they came,
// every caller's together.
func TestWritesWaitingForTheProgramKeepTheirOrder(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 5 * time.Second})
	takeAgain := stopTaking(t, p)
	a, b := ownerOf(t, s), ownerOf(t, s)
	errs := make(chan error, 4)
	go func() { errs <- s.Input(a, []byte("1")) }()
	untilWaiting(t, s, 0)
	go func() {
		_, err := s.Submit(context.Background(), Submission{Text: "2", ByName: "run"})
		errs <- err
	}()
	untilWaiting(t, s, 1)
	go func() { errs <- s.Input(b, []byte("3")) }()
	untilWaiting(t, s, 2)
	go func() { errs <- s.Input(a, []byte("4")) }()
	untilWaiting(t, s, 3)
	takeAgain()
	var got []string
	for range 5 {
		got = append(got, written(t, p))
	}
	if want := []string{"1", "2", "3", "4", "\r"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("written %q, want %q", got, want)
	}
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

// The writes waiting for the program are bounded, every caller's together:
// one past the bound is refused at once, and one that waited out its time
// leaves its place.
func TestTheWritesWaitingForTheProgramAreBounded(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	takeAgain := stopTaking(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.writeProc(ctx, []byte("begun"), writeWait{})
	untilWaiting(t, s, 0)
	for range maxQueuedWrites {
		go s.writeProc(ctx, []byte("waits"), writeWait{})
	}
	untilWaiting(t, s, maxQueuedWrites)
	if _, err := s.writeProc(ctx, []byte("over"), writeWait{}); !errors.Is(err, ErrTooManyRequests) {
		t.Fatalf("a write past the bound: %v", err)
	}
	cancel()
	untilWaiting(t, s, 0)
	takeAgain()
	if got := written(t, p); got != "begun" {
		t.Fatalf("written %q", got)
	}
	nothingWritten(t, p, 200*time.Millisecond)
}

// The writer never begins a write whose wait has ended (its connection
// gone, its context ended, its time passed), though its waiter has not yet
// taken it out of the queue: what is written is only what is still wanted.
func TestTheWriterNeverBeginsAWriteWhoseWaitHasEnded(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	takeAgain := stopTaking(t, p)
	go s.writeProc(context.Background(), []byte("begun"), writeWait{})
	untilWaiting(t, s, 0)
	// Queued as writeProc queues them, with no waiter to take them out: the
	// writer itself must find that their wait has ended.
	typist, revoked, left := ownerOf(t, s), ownerOf(t, s), ownerOf(t, s)
	// A link's controller whose connection failed, and whose link is
	// revoked after.
	var linkRevoked atomic.Bool
	failed := linkedController(t, s, failingSink{}, &linkRevoked)
	<-failed.Done()
	ctx, cancel := context.WithCancel(context.Background())
	bg := context.Background()
	gone := make(chan struct{})
	writes := map[string]*procWrite{
		// A keystroke's connection that goes withdraws it.
		"typist's": s.newWrite(bg, []byte("typist's"), time.Time{}, writeWait{from: typist, anyEnd: true}),
		// A submission's, only when the session ends the connection.
		"revoked's": s.newWrite(bg, []byte("revoked's"), time.Time{}, writeWait{from: revoked}),
		"left's":    s.newWrite(bg, []byte("left's"), time.Time{}, writeWait{from: left}),
		"failed's":  s.newWrite(bg, []byte("failed's"), time.Time{}, writeWait{from: failed}),
		// A submission whose sender is withdrawn (Submission.Withdrawn).
		"withdrawn's": s.newWrite(bg, []byte("withdrawn's"), time.Time{}, writeWait{gone: gone}),
		"expired":     s.newWrite(ctx, []byte("expired"), time.Time{}, writeWait{}),
		"late":        s.newWrite(bg, []byte("late"), time.Now().Add(50*time.Millisecond), writeWait{}),
		"live":        s.newWrite(bg, []byte("live"), time.Time{}, writeWait{}),
	}
	order := []string{"typist's", "revoked's", "left's", "failed's", "withdrawn's", "expired", "late", "live"}
	for _, k := range order {
		if err := s.writer.add(writes[k], s.proc, 0); err != nil {
			t.Fatal(err)
		}
	}
	// As the program reads again: a connection goes, a link is revoked,
	// a reply box closes, a context ends.
	s.Detach(typist)
	revoked.closeWith(ErrRevoked)
	s.Detach(left)
	linkRevoked.Store(true)
	close(gone)
	cancel()
	time.Sleep(100 * time.Millisecond)
	takeAgain()
	for _, want := range []string{"begun", "left's", "live"} {
		if got := written(t, p); got != want {
			t.Fatalf("written %q, want %q: only the writes still wanted", got, want)
		}
	}
	nothingWritten(t, p, 200*time.Millisecond)
	for k, want := range map[string]error{"typist's": errGone, "revoked's": ErrRevoked, "left's": nil, "failed's": ErrRevoked, "withdrawn's": ErrRevoked, "expired": context.Canceled, "late": ErrInputStalled, "live": nil} {
		if err := <-writes[k].done; !errors.Is(err, want) {
			t.Fatalf("%s: %v, want %v", k, err, want)
		}
	}
}

// The session's end ends the waits of its writes.
func TestWritesWaitingForTheProgramEndWithTheSession(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: time.Minute})
	stopTaking(t, p)
	sub := ownerOf(t, s)
	errs := make(chan error, 2)
	go func() { errs <- s.Input(sub, []byte("begun")) }()
	untilWaiting(t, s, 0)
	go func() { errs <- s.Input(sub, []byte("waits")) }()
	untilWaiting(t, s, 1)
	p.exit()
	for range 2 {
		select {
		case err := <-errs:
			if !errors.Is(err, ErrSessionEnded) {
				t.Fatalf("a write as the session ended: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a write waited on past the session's end")
		}
	}
}

// A submission whose text was being taken when its time ran out is
// accepted (ErrSubmitPending, typed) and finished as the program reads: its
// text, then its own Enter, holding the session's turn till then. The next
// submission, which waited out its time for the turn, is neither typed nor
// written; one after the program reads again gets a line of its own, never
// joined to the first.
func TestASubmissionWhoseTextIsTakenSlowlyIsFinishedAsTheProgramReads(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, SubmitPause: 10 * time.Millisecond})
	takeAgain := stopTaking(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	res, err := s.Submit(ctx, Submission{Text: "first", ByName: "run"})
	cancel()
	if !errors.Is(err, ErrSubmitPending) || !res.Typed || res.Entered {
		t.Fatalf("first: %+v, %v", res, err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	res, err = s.Submit(ctx, Submission{Text: "second", ByName: "run"})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || res.Typed {
		t.Fatalf("second: %+v, %v", res, err)
	}
	takeAgain()
	done := make(chan error, 1)
	go func() {
		_, err := s.Submit(context.Background(), Submission{Text: "third", ByName: "run"})
		done <- err
	}()
	var got []string
	for range 4 {
		got = append(got, written(t, p))
	}
	if want := []string{"first", "\r", "third", "\r"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("written %q, want %q", got, want)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	nothingWritten(t, p, 300*time.Millisecond)
}

// InputWaiting waits until its context ends, not Options.InputWait, and
// however long the program has not read: the host's own terminal waits for
// the program to read, as a terminal does. InputContext, a host viewer's,
// waits until its context ends too, but is refused at once, as Input is,
// once the writer has waited Options.InputWait on another write.
func TestInputWaitingWaitsForTheProgramAndInputContextNotOnceItStalled(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 100 * time.Millisecond})
	takeAgain := stopTaking(t, p)
	sub := ownerOf(t, s)
	done := make(chan error, 1)
	go func() { done <- s.InputWaiting(context.Background(), sub, []byte("k")) }()
	select {
	case err := <-done:
		t.Fatalf("returned while the program took nothing: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	// The writer has waited past InputWait on k: a host viewer's keys are
	// refused at once, the own terminal's wait.
	start := time.Now()
	if err := s.InputContext(context.Background(), sub, []byte("viewer's")); !errors.Is(err, ErrInputStalled) || time.Since(start) > 50*time.Millisecond {
		t.Fatalf("a host viewer's keys while the program is not reading: %v after %v", err, time.Since(start))
	}
	more := make(chan error, 1)
	go func() { more <- s.InputWaiting(context.Background(), sub, []byte("more")) }()
	untilWaiting(t, s, 1)
	takeAgain()
	for _, c := range []chan error{done, more} {
		if err := returnsWithin(t, 5*time.Second, "input", func() error { return <-c }); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"k", "more"} {
		if got := written(t, p); got != want {
			t.Fatalf("written %q, want %q", got, want)
		}
	}
}

// A controller's submission whose text waits behind another's write is
// withdrawn when the session ends the controller (a revoked link): nothing
// of it is written when the program reads again.
func TestASubmissionWaitingBehindAnothersWriteIsWithdrawnOnRevoke(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 5 * time.Second, SubmitPause: 10 * time.Millisecond})
	takeAgain := stopTaking(t, p)
	other := ownerOf(t, s)
	go s.Input(other, []byte("other's"))
	untilWaiting(t, s, 0)
	guest, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "guest", LinkID: "l1"}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Submit(context.Background(), Submission{Text: "guest's", By: guest})
		done <- err
	}()
	untilWaiting(t, s, 1)
	s.DisconnectLink("l1")
	select {
	case err := <-done:
		if !errors.Is(err, ErrRevoked) {
			t.Fatalf("the revoked controller's submission: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the revoked controller's submission still waits")
	}
	takeAgain()
	if got := written(t, p); got != "other's" {
		t.Fatalf("written %q", got)
	}
	nothingWritten(t, p, 300*time.Millisecond)
}

// returnsWithin runs fn and fails the test when it has not returned within d.
func returnsWithin[T any](t *testing.T, d time.Duration, what string, fn func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- fn() }()
	select {
	case v := <-done:
		return v
	case <-time.After(d):
		t.Fatalf("%s did not return within %v", what, d)
	}
	var zero T
	return zero
}

// A submission to a program that takes no input ends with its context, its
// text's write included, and gives the session's turn back: the next one
// waits only for its own time.
func TestASubmissionToAProgramThatTakesNoInputEndsWithItsContext(t *testing.T) {
	s, p := newLocal(t, t.TempDir())
	stopTaking(t, p)
	for _, text := range []string{"first", "second"} {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := returnsWithin(t, 5*time.Second, "a submission of "+text, func() error {
			_, err := s.Submit(ctx, Submission{Text: text, ByName: "run"})
			return err
		})
		cancel()
		if err == nil {
			t.Fatalf("%s: no error from a program that takes no input", text)
		}
	}
}

// Keystrokes that wait behind another's write are never written once their
// connection has ended (revoked, or gone): they are refused as it ends.
func TestKeystrokesWaitingWhenTheirConnectionEndsAreNeverWritten(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: time.Minute})
	takeAgain := stopTaking(t, p)
	a, b := ownerOf(t, s), ownerOf(t, s)
	aDone := make(chan error, 1)
	go func() { aDone <- s.Input(a, []byte("a's")) }()
	untilWaiting(t, s, 0)
	bDone := make(chan error, 1)
	go func() { bDone <- s.Input(b, []byte("b's")) }()
	untilWaiting(t, s, 1)
	s.Detach(b)
	select {
	case err := <-bDone:
		if !errors.Is(err, errGone) {
			t.Fatalf("the keystrokes of a connection that ended: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the keystrokes of a connection that ended still wait")
	}
	if waiting, _ := writesWaiting(s); waiting != 0 {
		t.Fatalf("%d writes wait", waiting)
	}
	takeAgain()
	if got := written(t, p); got != "a's" {
		t.Fatalf("written %q", got)
	}
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	nothingWritten(t, p, 200*time.Millisecond)
}

// A submission whose Enter's write has begun when its time runs out is
// accepted (ErrSubmitPending) and entered as the Enter lands, when the
// program reads again: its word comes then (Finished), and Confirm presses
// nothing more for it.
func TestASubmissionWhoseEnterWaitsForTheProgramIsEntered(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 200 * time.Millisecond, SubmitPause: 10 * time.Millisecond, ConfirmSubmit: true, ConfirmWait: 50 * time.Millisecond})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var writes int
	p.onWrite = func() {
		// The text goes in; the Enter, the second write, waits.
		if writes++; writes > 1 {
			<-release
		}
	}
	word := make(chan submitted, 1)
	got := returnsWithin(t, 5*time.Second, "the submission", func() submitted {
		res, err := s.Submit(context.Background(), Submission{Text: "line", ByName: "run", Confirm: true, Finished: func(res SubmitResult, err error) { word <- submitted{res, err} }})
		return submitted{res, err}
	})
	if !errors.Is(got.err, ErrSubmitPending) || !got.res.Typed || !got.res.Entered || got.res.Reentered {
		t.Fatalf("result %+v, %v", got.res, got.err)
	}
	if got := written(t, p); got != "line" {
		t.Fatalf("written %q", got)
	}
	select {
	case w := <-word:
		t.Fatalf("a word before the Enter landed: %+v %v", w.res, w.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if w := returnsWithin(t, 5*time.Second, "the word", func() submitted { return <-word }); w.err != nil || !w.res.Entered || w.res.Reentered {
		t.Fatalf("the word: %+v %v", w.res, w.err)
	}
	nothingWritten(t, p, 300*time.Millisecond)
}

// linkedController attaches a controller through a link whose credential
// stands until revoked is set (AttachOptions.Authorize).
func linkedController(t *testing.T, s *Local, sink Sink, revoked *atomic.Bool) *Subscription {
	t.Helper()
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "linked", LinkID: "l-" + NewID()[:6], Authorize: func() (Grant, error) {
		if revoked.Load() {
			return Grant{}, ErrRevoked
		}
		return Grant{Role: RoleControl}, nil
	}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// A link's controller whose connection failed has its submission typed,
// as a reply box's is, unless its link is revoked before it is: the
// credential is asked again, though the connection ended for another
// reason first.
func TestASubmissionWhoseConnectionFailedIsWithdrawnWhenItsLinkIsRevoked(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 5 * time.Second, SubmitPause: 10 * time.Millisecond})
	takeAgain := stopTaking(t, p)
	go s.Submit(context.Background(), Submission{Text: "first", ByName: "run"})
	untilWaiting(t, s, 0)
	var linkRevoked atomic.Bool
	guest := linkedController(t, s, failingSink{}, &linkRevoked)
	<-guest.Done() // its connection failed
	done := make(chan error, 1)
	go func() {
		_, err := s.Submit(context.Background(), Submission{Text: "guest's", By: guest})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // it waits for the turn
	linkRevoked.Store(true)
	takeAgain()
	for _, want := range []string{"first", "\r"} {
		if got := written(t, p); got != want {
			t.Fatalf("written %q, want %q", got, want)
		}
	}
	if err := returnsWithin(t, 5*time.Second, "the submission", func() error { return <-done }); !errors.Is(err, ErrRevoked) {
		t.Fatalf("the submission of a revoked link: %v", err)
	}
	nothingWritten(t, p, 300*time.Millisecond)
}

// failingSink is a connection whose every write fails, as one whose
// client has gone does.
type failingSink struct{}

func (failingSink) WriteFrame([]byte) error { return errors.New("connection lost") }
func (failingSink) Close(error)             {}

// A controller's submission waiting for the session's turn is not typed
// once the session has ended the controller for its link (revoked); one
// whose controller only went (a reply box that closed) or whose
// connection failed still is.
func TestASubmissionOfAControllerTheSessionEndedIsNotTyped(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 5 * time.Second, SubmitPause: 10 * time.Millisecond})
	takeAgain := stopTaking(t, p)
	// Another submission has the session's turn, its text waiting for the
	// program.
	go s.Submit(context.Background(), Submission{Text: "first", ByName: "run"})
	untilWaiting(t, s, 0)
	guest, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "guest", LinkID: "l1"}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	left := ownerOf(t, s)
	failed, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "failed"}, failingSink{})
	if err != nil {
		t.Fatal(err)
	}
	<-failed.Done() // its first write failed: the reason is the transport's
	done := map[string]chan error{}
	for _, c := range []struct {
		name string
		by   *Subscription
	}{{"guest's", guest}, {"left's", left}, {"failed's", failed}} {
		done[c.name] = make(chan error, 1)
		go func() {
			_, err := s.Submit(context.Background(), Submission{Text: c.name, By: c.by})
			done[c.name] <- err
		}()
		time.Sleep(50 * time.Millisecond) // in the order they came
	}
	time.Sleep(100 * time.Millisecond) // they wait for the turn
	s.DisconnectLink("l1")
	s.Detach(left)
	select {
	case err := <-done["guest's"]:
		if !errors.Is(err, ErrRevoked) {
			t.Fatalf("the revoked controller's submission: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the revoked controller's submission still waits")
	}
	takeAgain()
	var got []string
	for range 6 {
		got = append(got, written(t, p))
	}
	// The two that go on take their turns in either order.
	if strings.Join(got[:2], ",") != "first,\r" || got[3] != "\r" || got[5] != "\r" ||
		!(got[2] == "left's" && got[4] == "failed's" || got[2] == "failed's" && got[4] == "left's") {
		t.Fatalf("written %q", got)
	}
	for _, k := range []string{"left's", "failed's"} {
		if err := <-done[k]; err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	nothingWritten(t, p, 300*time.Millisecond)
}

// A run's chat sent by a link's controller to another member's agent, its
// text waiting behind another's write there, is withdrawn when the link is
// revoked: nothing of it is written when that program reads again.
func TestARunChatSendIsWithdrawnWhenItsSendersLinkIsRevoked(t *testing.T) {
	room := NewChatRoom(nil, nil)
	a, _ := newLocalWith(t, Options{ScrollbackBytes: 4096, RunChat: room})
	b, pb := newLocalWith(t, Options{ScrollbackBytes: 4096, RunChat: room, InputWait: 5 * time.Second, SubmitPause: 10 * time.Millisecond})
	room.Join(a, "core")
	room.Join(b, "tests")
	takeAgain := stopTaking(t, pb)
	other := ownerOf(t, b)
	go b.Input(other, []byte("other's"))
	untilWaiting(t, b, 0)
	var linkRevoked atomic.Bool
	guest := linkedController(t, a, newChanSink(false), &linkRevoked)
	msg, err := a.Chat(guest, proto.ChatPost{Scope: proto.ChatScopeRun, Text: "run the tests"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- a.ChatSend(context.Background(), guest, proto.ChatSend{T: proto.CtlChatSend, Ref: msg.ID, Scope: proto.ChatScopeRun, To: "tests"})
	}()
	untilWaiting(t, b, 1)
	linkRevoked.Store(true)
	a.DisconnectLink(guest.LinkID)
	// The program reads again well within the send's time.
	takeAgain()
	if got := written(t, pb); got != "other's" {
		t.Fatalf("written %q", got)
	}
	nothingWritten(t, pb, 300*time.Millisecond)
	if err := returnsWithin(t, 5*time.Second, "the send", func() error { return <-done }); err == nil {
		t.Fatal("the send of a revoked link went")
	}
}

// A submission whose Enter's write has begun when the session ends is not
// entered: the program, gone, never takes it.
func TestASubmissionWhoseEnterWaitsWhenTheSessionEndsIsNotEntered(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: time.Minute, SubmitPause: 10 * time.Millisecond})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var writes atomic.Int32
	p.onWrite = func() {
		// The text goes in; the Enter, the second write, waits.
		if writes.Add(1) > 1 {
			<-release
		}
	}
	type submitted struct {
		res SubmitResult
		err error
	}
	done := make(chan submitted, 1)
	go func() {
		res, err := s.Submit(context.Background(), Submission{Text: "line", ByName: "run"})
		done <- submitted{res, err}
	}()
	if got := written(t, p); got != "line" {
		t.Fatalf("written %q", got)
	}
	untilWrites(t, &writes, 2) // the Enter has begun, and waits
	p.exit()
	got := returnsWithin(t, 5*time.Second, "the submission", func() submitted { return <-done })
	if !errors.Is(got.err, ErrSessionEnded) || !got.res.Typed || got.res.Entered {
		t.Fatalf("result %+v, %v", got.res, got.err)
	}
}

// A submission waiting behind another's write is withdrawn as soon as its
// link's credential no longer stands, before the link's connections are
// closed for it.
func TestASubmissionIsWithdrawnOnceItsLinkNoLongerStands(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 5 * time.Second, SubmitPause: 10 * time.Millisecond})
	takeAgain := stopTaking(t, p)
	other := ownerOf(t, s)
	go s.Input(other, []byte("other's"))
	untilWaiting(t, s, 0)
	var linkRevoked atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &linkRevoked)
	done := make(chan error, 1)
	go func() {
		_, err := s.Submit(context.Background(), Submission{Text: "guest's", By: guest})
		done <- err
	}()
	untilWaiting(t, s, 1)
	linkRevoked.Store(true) // marked, its connection not closed yet
	takeAgain()
	if got := written(t, p); got != "other's" {
		t.Fatalf("written %q", got)
	}
	nothingWritten(t, p, 300*time.Millisecond)
	if err := returnsWithin(t, 5*time.Second, "the submission", func() error { return <-done }); !errors.Is(err, ErrRevoked) {
		t.Fatalf("the submission of a link no longer standing: %v", err)
	}
}

// A submission whose pending Enter's wait ends (its context) as the session
// ends is not entered, whichever of the two its wait saw first.
func TestASubmissionWhosePendingEnterSeesTheSessionEndIsNotEntered(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: time.Minute, SubmitPause: 10 * time.Millisecond})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var writes atomic.Int32
	p.onWrite = func() {
		if writes.Add(1) > 1 {
			<-release
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	type submitted struct {
		res SubmitResult
		err error
	}
	done := make(chan submitted, 1)
	go func() {
		res, err := s.Submit(ctx, Submission{Text: "line", ByName: "run"})
		done <- submitted{res, err}
	}()
	if got := written(t, p); got != "line" {
		t.Fatalf("written %q", got)
	}
	untilWrites(t, &writes, 2) // the Enter has begun, and waits
	// The wait wakes on the cancel, and the session ends before it can
	// take the Enter out of the queue (the writer's lock, held here).
	s.writer.mu.Lock()
	cancel()
	time.Sleep(100 * time.Millisecond)
	p.exit()
	<-s.Ended()
	s.writer.mu.Unlock()
	got := returnsWithin(t, 5*time.Second, "the submission", func() submitted { return <-done })
	if !errors.Is(got.err, ErrSessionEnded) || got.res.Entered {
		t.Fatalf("result %+v, %v", got.res, got.err)
	}
}

// A submission accepted while its text was taken slowly keeps the session's
// turn until its Enter is written, however long that waits: another's
// keystrokes the program stops taking between its text and its Enter do
// not make it give up the turn, and the next submission's text never joins
// its line.
func TestAPendingSubmissionKeepsItsTurnUntilItsEnter(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 200 * time.Millisecond, SubmitPause: 300 * time.Millisecond})
	var gate atomic.Pointer[chan struct{}]
	hold := func() {
		ch := make(chan struct{})
		gate.Store(&ch)
	}
	takeAgain := func() {
		if ch := gate.Swap(nil); ch != nil {
			close(*ch)
		}
	}
	t.Cleanup(takeAgain)
	p.onWrite = func() {
		if ch := gate.Load(); ch != nil {
			<-*ch
		}
	}
	other := ownerOf(t, s)
	hold()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err := s.Submit(ctx, Submission{Text: "first", ByName: "run"})
	cancel()
	if !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("first: %v", err)
	}
	takeAgain()
	if got := written(t, p); got != "first" {
		t.Fatalf("written %q", got)
	}
	// During its pause the program stops taking input again, another's
	// keystrokes ahead of its Enter, for longer than InputWait.
	hold()
	go s.Input(other, []byte("other's"))
	time.Sleep(800 * time.Millisecond)
	takeAgain()
	done := make(chan error, 1)
	go func() {
		_, err := s.Submit(context.Background(), Submission{Text: "third", ByName: "run"})
		done <- err
	}()
	var got []string
	for range 4 {
		got = append(got, written(t, p))
	}
	if want := []string{"other's", "\r", "third", "\r"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("written %q, want %q", got, want)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A submission whose text is written but whose Enter has no turn in time
// (another's keystrokes the program stops taking during its pause) is
// accepted, and its Enter pressed as the program reads, the session's turn
// held till then: the next submission's text never joins its line.
func TestASubmissionWhoseEnterHasNoTurnInTimeIsFinishedAsTheProgramReads(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 200 * time.Millisecond, SubmitPause: 300 * time.Millisecond})
	var gate atomic.Pointer[chan struct{}]
	hold := func() {
		ch := make(chan struct{})
		gate.Store(&ch)
	}
	takeAgain := func() {
		if ch := gate.Swap(nil); ch != nil {
			close(*ch)
		}
	}
	t.Cleanup(takeAgain)
	p.onWrite = func() {
		if ch := gate.Load(); ch != nil {
			<-*ch
		}
	}
	other := ownerOf(t, s)
	done := make(chan error, 1)
	go func() {
		_, err := s.Submit(context.Background(), Submission{Text: "first", ByName: "run"})
		done <- err
	}()
	if got := written(t, p); got != "first" {
		t.Fatalf("written %q", got)
	}
	// During its pause the program stops taking input, another's
	// keystrokes ahead of its Enter, for longer than InputWait.
	hold()
	go s.Input(other, []byte("other's"))
	if err := returnsWithin(t, 5*time.Second, "the submission", func() error { return <-done }); !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("first: %v", err)
	}
	takeAgain()
	third := make(chan error, 1)
	go func() {
		_, err := s.Submit(context.Background(), Submission{Text: "third", ByName: "run"})
		third <- err
	}()
	var got []string
	for range 4 {
		got = append(got, written(t, p))
	}
	if want := []string{"other's", "\r", "third", "\r"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("written %q, want %q", got, want)
	}
	if err := <-third; err != nil {
		t.Fatal(err)
	}
}

// A submission's Enter queued behind another's write is left out when a
// prompt is raised while it waits: the writer looks again as it takes it,
// and the newer prompt is never answered by it.
func TestAQueuedEnterIsLeftOutForAPromptRaisedMeanwhile(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 5 * time.Second, SubmitPause: 200 * time.Millisecond})
	var gate atomic.Pointer[chan struct{}]
	hold := func() {
		ch := make(chan struct{})
		gate.Store(&ch)
	}
	takeAgain := func() {
		if ch := gate.Swap(nil); ch != nil {
			close(*ch)
		}
	}
	t.Cleanup(takeAgain)
	p.onWrite = func() {
		if ch := gate.Load(); ch != nil {
			<-*ch
		}
	}
	other := ownerOf(t, s)
	type submitted struct {
		res SubmitResult
		err error
	}
	done := make(chan submitted, 1)
	go func() {
		res, err := s.Submit(context.Background(), Submission{Text: "line", ByName: "run"})
		done <- submitted{res, err}
	}()
	if got := written(t, p); got != "line" {
		t.Fatalf("written %q", got)
	}
	// During its pause another's keys take the writer, and the program
	// stops reading: the Enter waits behind them.
	hold()
	go s.Input(other, []byte("other's"))
	untilWaiting(t, s, 1)
	s.SetAttention(AttentionNeedsInput, "Allow?", SourceAPI)
	takeAgain()
	if got := written(t, p); got != "other's" {
		t.Fatalf("written %q", got)
	}
	got := returnsWithin(t, 5*time.Second, "the submission", func() submitted { return <-done })
	if got.err != nil || got.res.Entered {
		t.Fatalf("result %+v, %v", got.res, got.err)
	}
	nothingWritten(t, p, 300*time.Millisecond)
	if att := s.Info().Attention; att.State != AttentionNeedsInput || att.Message != "Allow?" {
		t.Fatalf("the newer prompt: %+v", att)
	}
}

// gateWrites lets the test stop p taking input (hold) and take it again
// (takeAgain), as often as it likes; it takes input again as the test ends.
func gateWrites(t *testing.T, p *fakeProc) (hold, takeAgain func()) {
	t.Helper()
	var gate atomic.Pointer[chan struct{}]
	hold = func() {
		ch := make(chan struct{})
		gate.Store(&ch)
	}
	takeAgain = func() {
		if ch := gate.Swap(nil); ch != nil {
			close(*ch)
		}
	}
	t.Cleanup(takeAgain)
	p.onWrite = func() {
		if ch := gate.Load(); ch != nil {
			<-*ch
		}
	}
	return hold, takeAgain
}

// A submission's Enter queued behind another's write is left out when a
// dialog asked during the work is drawn while it waits, before the screen
// has gone quiet: the writer looks at the screen as it takes the Enter,
// and the dialog is asked.
func TestAQueuedEnterIsLeftOutForADialogDrawnMeanwhile(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions(), InputWait: 5 * time.Second, SubmitPause: 100 * time.Millisecond}))
	hold, takeAgain := gateWrites(t, p)
	other := ownerOf(t, s)
	p.outW.Write([]byte("\x1b[?2004h› "))
	waitPaste(t, s, true)
	time.Sleep(700 * time.Millisecond)
	type submitted struct {
		res SubmitResult
		err error
	}
	done := make(chan submitted, 1)
	go func() {
		res, err := s.Submit(context.Background(), Submission{Text: "line", ByName: "crew"})
		done <- submitted{res, err}
	}()
	if got := written(t, p); got != "\x1b[200~line\x1b[201~" {
		t.Fatalf("written %q", got)
	}
	hold()
	go s.Input(other, []byte("x"))
	untilWaiting(t, s, 1) // the Enter waits behind the other's key
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	time.Sleep(50 * time.Millisecond) // read, not yet quiet
	takeAgain()
	if got := written(t, p); got != "x" {
		t.Fatalf("written %q", got)
	}
	got := returnsWithin(t, 5*time.Second, "the submission", func() submitted { return <-done })
	if got.err != nil || got.res.Entered {
		t.Fatalf("result %+v, %v", got.res, got.err)
	}
	nothingWritten(t, p, 300*time.Millisecond)
	if a := s.Info().Attention; !asked(a) {
		t.Fatalf("attention %+v", a)
	}
}

// The Enter pressed again for a prompt the agent did not report taking
// (Confirm) is left out when a prompt is raised while it waits behind
// another's write.
func TestAnEnterPressedAgainIsLeftOutForAPromptRaisedMeanwhile(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, InputWait: 5 * time.Second, SubmitPause: 10 * time.Millisecond, ConfirmSubmit: true, ConfirmWait: 300 * time.Millisecond})
	hold, takeAgain := gateWrites(t, p)
	other := ownerOf(t, s)
	type submitted struct {
		res SubmitResult
		err error
	}
	done := make(chan submitted, 1)
	go func() {
		res, err := s.Submit(context.Background(), Submission{Text: "line", ByName: "crew", Confirm: true})
		done <- submitted{res, err}
	}()
	for _, want := range []string{"line", "\r"} {
		if got := written(t, p); got != want {
			t.Fatalf("written %q, want %q", got, want)
		}
	}
	hold()
	go s.Input(other, []byte("x"))
	untilWaiting(t, s, 1) // the Enter pressed again waits behind the key
	s.SetAttention(AttentionNeedsInput, "Allow?", SourceAPI)
	takeAgain()
	if got := written(t, p); got != "x" {
		t.Fatalf("written %q", got)
	}
	got := returnsWithin(t, 5*time.Second, "the submission", func() submitted { return <-done })
	if got.err != nil || !got.res.Entered || got.res.Reentered {
		t.Fatalf("result %+v, %v", got.res, got.err)
	}
	nothingWritten(t, p, 300*time.Millisecond)
}

// A submission whose time runs out in its pause, its text typed, is
// accepted and its line finished as the program reads, the session's turn
// held: the next submission's text never joins it.
func TestASubmissionWhoseTimeRunsOutInItsPauseIsFinished(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096, SubmitPause: 300 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	res, err := s.Submit(ctx, Submission{Text: "line", ByName: "run"})
	cancel()
	if !errors.Is(err, ErrSubmitPending) || !res.Typed || res.Entered {
		t.Fatalf("result %+v, %v", res, err)
	}
	third := make(chan error, 1)
	go func() {
		_, err := s.Submit(context.Background(), Submission{Text: "third", ByName: "run"})
		third <- err
	}()
	var got []string
	for range 4 {
		got = append(got, written(t, p))
	}
	if want := []string{"line", "\r", "third", "\r"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("written %q, want %q", got, want)
	}
	if err := <-third; err != nil {
		t.Fatal(err)
	}
}

// A key answering a startup question that the program takes late (its
// write begun when its time ran out) reaches the question once it lands:
// consumed, it is withdrawn once the screen shows the program moved on,
// not left held.
func TestALateKeyAnswersTheQuestionItWasTypedAt(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{
		InputWait:    100 * time.Millisecond,
		TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`),
		TrustAnswers: []Option{{Label: "Yes, trust this folder", Input: "\r"}},
	}))
	hold, takeAgain := gateWrites(t, p)
	sub := ownerOf(t, s)
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Yes"))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNeedsInput && a.Source == SourceTrust })
	hold()
	if err := s.Input(sub, []byte("\r")); !errors.Is(err, ErrInputStalled) {
		t.Fatalf("the answer: %v", err)
	}
	takeAgain()
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if err := s.AnswerInput(t.Context(), sub, s.Info().Attention.ID, []byte("\r")); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("a reply after the late key: %v", err)
	}
	p.outW.Write([]byte("\x1b[2J\x1b[H> "))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone })
}

// A submission's text queued behind another's write is held when a dialog
// asked during the work is drawn while it waits: the writer looks at the
// screen as it takes the text, and nothing of it is written; a person's
// reply is refused (ErrQuestion), Conductor's own text with UnlessWaiting
// gets a zero result.
func TestQueuedTextIsHeldForADialogDrawnMeanwhile(t *testing.T) {
	for _, c := range []struct {
		name    string
		reply   bool
		wantErr error
	}{{"a reply", true, ErrQuestion}, {"a handoff", false, nil}} {
		t.Run(c.name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions(), InputWait: 5 * time.Second}))
			hold, takeAgain := gateWrites(t, p)
			other := ownerOf(t, s)
			p.outW.Write([]byte("\x1b[?2004h› "))
			waitPaste(t, s, true)
			time.Sleep(700 * time.Millisecond)
			hold()
			go s.Input(other, []byte("x"))
			untilWaiting(t, s, 0) // the other's key has the writer
			sub := Submission{Text: "over to you", ByName: "crew", UnlessWaiting: true}
			if c.reply {
				sub = Submission{Text: "over to you", By: other}
			}
			type submitted struct {
				res SubmitResult
				err error
			}
			done := make(chan submitted, 1)
			go func() {
				res, err := s.Submit(context.Background(), sub)
				done <- submitted{res, err}
			}()
			untilWaiting(t, s, 1) // the text waits behind the key
			p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
			time.Sleep(50 * time.Millisecond) // read, not yet quiet
			takeAgain()
			if got := written(t, p); got != "x" {
				t.Fatalf("written %q", got)
			}
			got := returnsWithin(t, 5*time.Second, "the submission", func() submitted { return <-done })
			if !errors.Is(got.err, c.wantErr) || (c.wantErr == nil && got.err != nil) || got.res.Typed {
				t.Fatalf("result %+v, %v", got.res, got.err)
			}
			nothingWritten(t, p, 300*time.Millisecond)
			if a := s.Info().Attention; !asked(a) {
				t.Fatalf("attention %+v", a)
			}
		})
	}
}

// A key not written (its time out before its turn) does nothing to the
// question it would have answered: it is not noted against the dialog,
// and the screen is not held.
func TestAKeyNotWrittenIsNotNotedAgainstTheDialog(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub := ownerOf(t, s)
	p.outW.Write([]byte("\x1b[?2004h› "))
	waitPaste(t, s, true)
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	waitAttention(t, s, asked)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.InputContext(ctx, sub, []byte("y")); err == nil {
		t.Fatal("a key whose time had run out was written")
	}
	nothingWritten(t, p, 100*time.Millisecond)
	s.mu.Lock()
	consumed := s.consumed
	s.mu.Unlock()
	if consumed != "" || s.asked.Held() {
		t.Fatalf("a key not written consumed the dialog (%q), held %v", consumed, s.asked.Held())
	}
	if a := s.Info().Attention; !asked(a) {
		t.Fatalf("attention %+v", a)
	}
}

// Several keys with an Enter, queued behind another's write, are refused
// when a dialog asked during the work is drawn while they wait and they
// are none of its choices: the writer looks at the screen as it takes them
// (a choice clicked late must not pick the dialog's highlighted line).
func TestQueuedKeysWithAnEnterAreRefusedForADialogDrawnMeanwhile(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions(), InputWait: 5 * time.Second}))
	hold, takeAgain := gateWrites(t, p)
	a, b := ownerOf(t, s), ownerOf(t, s)
	p.outW.Write([]byte("\x1b[?2004h› "))
	waitPaste(t, s, true)
	time.Sleep(700 * time.Millisecond)
	hold()
	go s.Input(a, []byte("x"))
	untilWaiting(t, s, 0)
	done := make(chan error, 1)
	go func() { done <- s.Input(b, []byte("1\r")) }()
	untilWaiting(t, s, 1) // the keys wait behind the other's
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	time.Sleep(50 * time.Millisecond) // read, not yet quiet
	takeAgain()
	if got := written(t, p); got != "x" {
		t.Fatalf("written %q", got)
	}
	if err := returnsWithin(t, 5*time.Second, "the keys", func() error { return <-done }); !errors.Is(err, ErrQuestion) {
		t.Fatalf("keys at a dialog drawn meanwhile: %v", err)
	}
	nothingWritten(t, p, 300*time.Millisecond)
	if att := s.Info().Attention; !asked(att) {
		t.Fatalf("attention %+v", att)
	}
}

// A caller may reuse its buffer once Input has returned, also when the
// keys' write had begun and lands later: what runs as they land reads keys
// of its own (go test -race tells).
func TestInputsBufferIsTheCallersOnceItReturns(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions(), InputWait: 100 * time.Millisecond}))
	hold, takeAgain := gateWrites(t, p)
	sub := ownerOf(t, s)
	hold()
	buf := []byte("\r")
	if err := s.Input(sub, buf); !errors.Is(err, ErrInputStalled) {
		t.Fatalf("the keys: %v", err)
	}
	takeAgain()  // the keys land
	buf[0] = 'x' // meanwhile the caller's buffer is its own again
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	time.Sleep(100 * time.Millisecond) // what follows the keys runs
}

// A write made is never held against the next: a waiter whose time ran
// out just as the write was made, marking the writer stalled, marks
// nothing once the write's waiter can learn it was made, and the next
// write is queued, however soon it comes.
func TestAWriteMadeIsNeverHeldAgainstTheNext(t *testing.T) {
	release := make(chan struct{})
	told := make(chan struct{}, 1)
	q := procWriter{told: func() {
		select {
		case told <- struct{}{}:
		default:
		}
		<-release
	}}
	defer close(release)
	write := func(data string) *procWrite {
		return &procWrite{data: []byte(data), done: make(chan error, 1), over: func() error { return nil }}
	}
	w := write("a")
	if err := q.add(w, io.Discard, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := <-w.done; err != nil {
		t.Fatal(err)
	}
	<-told     // the writer is past telling, not yet at the next
	q.stall(w) // a waiter whose time ran out as the write was made
	next := write("b")
	if err := q.add(next, io.Discard, time.Second); err != nil {
		t.Fatalf("the write after one made was refused: %v", err)
	}
}
