package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Writing to the process. What the session types into the program, a
// controller's keystrokes (Input) and a submission's text and Enter (Submit),
// goes through one writer per session, in the order the writes came. A
// program that stops reading its terminal fills the terminal's buffer, and a
// write to it then waits until the program reads again: the writer makes
// that wait on a goroutine of its own, so whoever asked for the write waits
// only until its time is up (a context, and InputWait), and goes on with an
// error then. A write the writer has not begun by then is dropped and never
// written; one it has begun lands when the program reads again, or fails as
// the session ends. Once the writer has waited InputWait on one write, the
// program is taken to be not reading: a write asked for with a wait is
// refused at once (ErrInputStalled) until that write is taken, so that a
// connection's frames are not held InputWait each. The writer's goroutine is
// started for the first write and ends once none is left: an idle session
// holds none, and at most one waits on the program.

// InputWait is how long a write to the process waits for the program to
// take it (Options.InputWait), the writes before it included: a keystroke's
// (Input), each of a submission's writes (its text, its Enter).
const InputWait = 10 * time.Second

// maxQueuedWrites bounds the writes waiting for the writer, every caller's
// together. Each caller waits for its own write before it asks for another
// (a connection's read loop, a host viewer's keystrokes, the session's one
// submission at a time, the host's own terminal), so a session of
// MaxViewers connections stays far below it; past it a write is refused
// with ErrTooManyRequests.
const maxQueuedWrites = 256

// ErrInputStalled says the program did not take a write within its time
// (InputWait): it is not reading its terminal.
var ErrInputStalled = errors.New("session: the program is not taking input")

// errGone refuses what a connection asked for once it has gone: a
// keystroke not yet written when it went, an editor's open.
var errGone = errors.New("session: the connection has gone")

// writeWait says how long a write waits, and for whom (writeProc).
type writeWait struct {
	// wait is the write's own time, none when zero (its context alone).
	wait time.Duration
	// stall refuses the write at once while the writer has waited this
	// long on another write (the program is not reading); none when zero.
	stall time.Duration
	// from is the connection that asked for the write, nil for what
	// Conductor types itself. The end of its credential (a revoked or
	// expired link, revoked: on a host, the server heard from lately too)
	// withdraws a write not yet begun, asked as the write is queued and
	// again as the writer takes it; with anyEnd (keystrokes, a reply's keys
	// among them), so does any end of the connection. Without it, only the
	// credential's: a reply box closes its connection once it has sent, and
	// its submission goes on, as it does for a connection that failed or
	// was too slow.
	from   *Subscription
	anyEnd bool
	// gone, when set, is closed once whoever asked for the write no longer
	// stands, as a revoked link's connection no longer does
	// (Submission.Withdrawn): it withdraws a write not yet begun
	// (errWithdrawn), asked as the write is queued and again as the writer
	// takes it, as from's credential does.
	gone <-chan struct{}
	// before, when set, runs as the writer takes the write, just before it
	// is made, and never for a write not made: a submission's BeforeEnter.
	before func()
	// guard, when set, is asked as the writer takes the write, as over is,
	// the last of its checks: an error from it and the write is not made (a
	// submission's Enter that a newer prompt would take); nil, and it is.
	guard func() error
	// hold, when set (else from's, AttachOptions.Hold), is the credential
	// held while the write is under way (Held): taken as the writer takes
	// the write, after every other check but guard, asked again at the
	// write's commit boundary (Held.Stands, before the guard, the last
	// check before the write is begun), and let go once the process write
	// has returned or the credential's end stopped waiting for it
	// (Held.Cancelled): a revoke or an expiry that marks the credential
	// before the write is begun refuses it; one after waits for it, up to
	// its bound, and the write begun lands as the program reads it. An
	// error from it, taken or asked, refuses the write (a kind of
	// ErrRevoked), nothing written.
	hold func() (Held, error)
}

// Held is a credential held while one write is under way (writeWait.hold):
// a decision link's (share.Held, through the server's hold of the link).
// A link's hold covers the write from its check until the process write
// has returned: a revoke and an expiry of the link wait for it alike, up
// to their bound. Nothing is written once the link's end has marked it; a
// write begun before lands as the program reads it.
type Held interface {
	// Stands is asked at the write's commit boundary, just before it is
	// begun: an error (ErrRevoked, ErrExpired) and nothing of it is
	// written. It must not call the session and must not wait.
	Stands() error
	// Cancelled is closed once whoever ends the credential stops waiting
	// for the write (a process write the program has not taken within the
	// link's bound): the hold is let go at once, and the write answers as
	// the link's end and resolves nothing, though its bytes, begun, still
	// land as the program reads them.
	Cancelled() <-chan struct{}
	// Release lets the credential go: once; again does nothing.
	Release()
}

// cancelledBy is why a write whose credential's end stopped waiting for
// it is refused: why the credential no longer stands (a kind of
// ErrRevoked, ErrExpired for an expiry), the write having begun and not
// returned within the bound the credential's end waits for. Its bytes are
// the program's to read; it is not the link's answer.
func (w *procWrite) cancelledBy() error {
	why := w.held.Stands()
	if why == nil {
		why = ErrRevoked
	}
	return fmt.Errorf("%w: the program had not taken the write when its link ended: it answers nothing", asRevoked(why))
}

// withdrawn says why the write's connection withdraws it, nil while it
// does not: once its link's credential no longer stands, whether or not
// the connection has been closed for it yet (a revoke marks the link
// before it closes the link's connections), and that first; a
// keystroke's also once the connection has ended (errGone).
func (o writeWait) withdrawn() error {
	if o.gone != nil {
		select {
		case <-o.gone:
			return errWithdrawn
		default:
		}
	}
	if o.from == nil {
		return nil
	}
	if err := revoked(o.from); err != nil {
		return err
	}
	if o.anyEnd {
		select {
		case <-o.from.done:
			return errGone
		default:
		}
	}
	return nil
}

// errWithdrawn withdraws a submission whose sender no longer stands
// (Submission.Withdrawn closed): a kind of ErrRevoked, so that whatever a
// revoked link does to a submission, its text typed or not, this does too.
var errWithdrawn error = senderWithdrawn{}

// senderWithdrawn is errWithdrawn's type: an error that is also ErrRevoked.
type senderWithdrawn struct{}

func (senderWithdrawn) Error() string { return "session: the submission's sender was withdrawn" }

// Is makes errors.Is(errWithdrawn, ErrRevoked) hold.
func (senderWithdrawn) Is(target error) bool { return target == ErrRevoked }

// revoked says why sub's credential no longer stands (a revoked or expired
// link: ErrRevoked, ErrExpired), nil while it does, whether sub is still
// attached or went, failed or was dropped as too slow: the session's end
// of sub for its link first, else the credential asked again
// (AttachOptions.Authorize), since a connection that ended for another
// reason keeps that reason, and then whether it may be acted on now
// (AttachOptions.Fresh). A credential that is not a link's, revoked
// (Subscription.Revoke), is kept of its own, since the connection may
// have ended for another reason first, and so is looked at after the
// session's end of sub. It does not call the session.
func revoked(sub *Subscription) error {
	if r := sub.Reason(); errors.Is(r, ErrRevoked) {
		return r
	}
	if r := sub.revocation.Load(); r != nil {
		return *r
	}
	if sub.authorize != nil {
		if _, err := sub.authorize(); err != nil {
			return asRevoked(err)
		}
	}
	if sub.fresh != nil {
		if err := sub.fresh(); err != nil {
			return asRevoked(err)
		}
	}
	return nil
}

// asRevoked is err when it is ErrRevoked or a kind of it, else ErrRevoked.
func asRevoked(err error) error {
	if errors.Is(err, ErrRevoked) {
		return err
	}
	return ErrRevoked
}

// LinkRefusal is how a viewer is answered for what it sent and the session
// refused with err because the viewer's link no longer opens the session
// (revoked: ErrRevoked, ErrExpired, or a kind of either with words of its
// own, such as a host's that has not heard from its server lately): the
// error code, expired or revoked, and the words a page shows. ok is false
// for any other err.
func LinkRefusal(err error) (code, message string, ok bool) {
	switch {
	case errors.Is(err, ErrExpired):
		return proto.ErrCodeExpired, "this link has expired", true
	case err == ErrRevoked:
		// The plain error; a kind of it keeps its own words.
		return proto.ErrCodeRevoked, "this link no longer opens the session", true
	case errors.Is(err, ErrRevoked):
		return proto.ErrCodeRevoked, err.Error(), true
	}
	return "", "", false
}

// procWrite is one write waiting for the writer, or being made.
type procWrite struct {
	data []byte
	// done receives the write's result once the writer has made it, or why
	// it did not begin it.
	done chan error
	// begun says the writer has taken it; under procWriter.mu.
	begun bool
	// over says why the write is no longer wanted (its wait has ended), nil
	// while it is: the writer asks it as it takes the write, under its
	// lock, and never begins one it finds over, whether or not its waiter
	// has seen the end yet.
	over func() error
	// before runs as the writer takes it (writeWait.before).
	before func()
	// held is the credential over took (writeWait.hold), nil for none: let
	// go once the write has returned or was cancelled (letGo).
	held Held
}

// cancelledEarly is closed when the write's credential was cancelled
// before the write began (never for a write without one).
func (w *procWrite) cancelledEarly() <-chan struct{} {
	if w.held == nil {
		return nil
	}
	return w.held.Cancelled()
}

// newWrite is a write of data for writeProc's wait: until ctx ends, until
// deadline (none when zero), until o's connection withdraws it or the
// session ends.
func (s *Local) newWrite(ctx context.Context, data []byte, deadline time.Time, o writeWait) *procWrite {
	ended := s.ended
	hold := o.hold
	if hold == nil && o.from != nil {
		hold = o.from.hold
	}
	w := &procWrite{data: bytes.Clone(data), done: make(chan error, 1), before: o.before}
	w.over = func() error {
		select {
		case <-ended:
			return ErrSessionEnded
		default:
		}
		if err := o.withdrawn(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return ErrInputStalled
		}
		// The credential held from here until the write has returned or
		// was cancelled (run): checked with the write, so nothing comes
		// between.
		if hold != nil {
			held, err := hold()
			if err != nil {
				return asRevoked(err)
			}
			w.held = held
			// The write's commit boundary, the last thing asked before the
			// guard (which claims a reply's question for a write that is
			// made): the credential asked once more, fail-closed, so one
			// that ended since it was taken (a link revoked or expired, its
			// barrier begun) begins nothing.
			if err := w.stands(); err != nil {
				return err
			}
		}
		// The guard is asked last: the writer begins the write when it
		// passes, so a guard that notes something of the write (a reply
		// noting its question answered) notes it only for a write made.
		if o.guard != nil {
			if err := o.guard(); err != nil {
				w.letGo()
				return err
			}
		}
		return nil
	}
	return w
}

// letGo lets go of the credential the write holds (writeWait.hold), if
// any: again does nothing.
func (w *procWrite) letGo() {
	if w.held != nil {
		w.held.Release()
	}
}

// stands asks the write's credential at its commit boundary (over), just
// before the write is begun: nil while it stands (or there is none), else
// why it does not (a kind of ErrRevoked), the credential let go.
func (w *procWrite) stands() error {
	if w.held == nil {
		return nil
	}
	if err := w.held.Stands(); err != nil {
		w.letGo()
		return asRevoked(err)
	}
	return nil
}

// watchCancel watches the write's credential while the write is made:
// once the credential's end stops waiting for it (Held.Cancelled), its
// hold is let go at once. done ends the watch and says whether it was.
func (w *procWrite) watchCancel() (done func() bool) {
	if w.held == nil {
		return func() bool { return false }
	}
	var cancelled atomic.Bool
	wrote, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-w.held.Cancelled():
			cancelled.Store(true)
			w.letGo()
		case <-wrote:
		}
	}()
	return func() bool {
		close(wrote)
		<-watched
		return cancelled.Load()
	}
}

// procWriter writes to the process in the order the writes were added, on a
// goroutine it starts for the first and that ends once none is left. Its
// zero value is ready.
type procWriter struct {
	mu      sync.Mutex
	queue   []*procWrite
	running bool
	// writing is when the write being made began; zero while none is.
	writing time.Time
	// current is the write being made, nil while none is; stalled says
	// that a wait for it ran out of its own time (ErrInputStalled) while
	// it was being made: the program is not reading.
	current *procWrite
	stalled bool
	// told, when set (tests), runs once a write's waiter has been told how
	// it went, before the writer takes the next.
	told func()
}

// add queues w for to. It refuses w, never blocking, with ErrInputStalled
// when stall is set and the program is known not to read (a wait for the
// write being made ran out of its time, or the writer has been on it for
// stall), and with ErrTooManyRequests when maxQueuedWrites wait already.
func (q *procWriter) add(w *procWrite, to io.Writer, stall time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if stall > 0 && (q.stalled || !q.writing.IsZero() && time.Since(q.writing) >= stall) {
		return ErrInputStalled
	}
	if len(q.queue) >= maxQueuedWrites {
		return ErrTooManyRequests
	}
	q.queue = append(q.queue, w)
	if !q.running {
		q.running = true
		go q.run(to)
	}
	return nil
}

func (q *procWriter) run(to io.Writer) {
	for {
		q.mu.Lock()
		q.writing, q.current, q.stalled = time.Time{}, nil, false
		if len(q.queue) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		w := q.queue[0]
		q.queue[0] = nil
		q.queue = q.queue[1:]
		if err := w.over(); err != nil {
			// Its sender withdrew it, or its time ended, while it waited:
			// never begun, though its waiter may not have dropped it yet.
			q.mu.Unlock()
			w.done <- err
			continue
		}
		w.begun = true
		q.writing, q.current = time.Now(), w
		q.mu.Unlock()
		// The credential is held until the write has returned, or is let go
		// as the credential's end stops waiting for it (a program that does
		// not take it within the bound): the write, begun, lands as the
		// program reads it, and is refused as the link's end, nothing of
		// it noted as its own (a reply's question not resolved by it).
		cancelled := w.watchCancel()
		var err error
		select {
		case <-w.cancelledEarly():
			err = w.cancelledBy()
		default:
			if w.before != nil {
				w.before()
			}
			_, err = to.Write(w.data)
		}
		if cancelled() && err == nil {
			err = w.cancelledBy()
		}
		w.letGo()
		// The write is made: nothing of it is held against the next
		// (a stall marked for it by a waiter whose time ran out as it was
		// being made) from the moment its waiter can learn so.
		q.mu.Lock()
		q.writing, q.current, q.stalled = time.Time{}, nil, false
		q.mu.Unlock()
		w.done <- err
		if q.told != nil {
			q.told()
		}
	}
}

// stall notes that a wait for w ran out of its time while the writer was
// making it: until it is made, the program is known not to read.
func (q *procWriter) stall(w *procWrite) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current == w {
		q.stalled = true
	}
}

// drop takes w out of the queue unless the writer has begun it, and reports
// whether it did: a write dropped is never made.
func (q *procWriter) drop(w *procWrite) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if w.begun {
		return false
	}
	if i := slices.Index(q.queue, w); i >= 0 {
		q.queue = slices.Delete(q.queue, i, i+1)
	}
	return true
}

// writeProc writes data to the process in its turn among the session's
// writes, and waits for it until ctx ends, o.wait passes, o's connection
// withdraws it (writeWait) or the session ends: ctx's error,
// ErrInputStalled, errGone (or why the session ended the connection) or
// ErrSessionEnded then. With o.stall, it is refused at once
// (ErrInputStalled) while the writer has waited that long on another
// write. pending, when not nil, says the write had begun and not finished
// when the error came: the program has not taken it yet, it lands when the
// program reads again, and pending gives its result then; a write that had
// not begun is never made, the writer itself making sure of it as it takes
// the write. Once the write is made, err is its own. data is copied: the
// caller may reuse it once writeProc returns.
func (s *Local) writeProc(ctx context.Context, data []byte, o writeWait) (pending <-chan error, err error) {
	if err := ctx.Err(); err != nil {
		// Its time ran out before its turn came (a host's keystrokes that
		// waited behind others): never written.
		return nil, err
	}
	if err := o.withdrawn(); err != nil {
		return nil, err
	}
	var deadline time.Time
	var expired <-chan time.Time
	if o.wait > 0 {
		deadline = time.Now().Add(o.wait)
		t := time.NewTimer(o.wait)
		defer t.Stop()
		expired = t.C
	}
	w := s.newWrite(ctx, data, deadline, o)
	if err := s.writer.add(w, s.proc, o.stall); err != nil {
		return nil, err
	}
	var fromEnded <-chan struct{}
	if o.from != nil {
		fromEnded = o.from.done
	}
wait:
	for {
		select {
		case err := <-w.done:
			return nil, err
		case <-expired:
			err = ErrInputStalled
		case <-ctx.Done():
			err = ctx.Err()
		case <-fromEnded:
			if err = o.withdrawn(); err == nil {
				// An end that leaves the write be.
				fromEnded = nil
				continue
			}
		case <-o.gone:
			err = errWithdrawn
		case <-s.ended:
			err = ErrSessionEnded
		}
		break wait
	}
	if s.writer.drop(w) {
		return nil, err
	}
	// Begun: it may have finished meanwhile.
	select {
	case werr := <-w.done:
		return nil, werr
	default:
	}
	if errors.Is(err, ErrInputStalled) {
		// Its own time ran out on it: the writes after it are refused at
		// once while it is still being made, not a wait each, from now on
		// rather than once the writer's clock says so.
		s.writer.stall(w)
	}
	if s.isEnded() {
		// The session ended meanwhile, whatever woke the wait: the write
		// never lands.
		return w.done, ErrSessionEnded
	}
	return w.done, err
}

// isEnded reports whether the session has ended (Ended is closed).
func (s *Local) isEnded() bool {
	select {
	case <-s.ended:
		return true
	default:
		return false
	}
}
