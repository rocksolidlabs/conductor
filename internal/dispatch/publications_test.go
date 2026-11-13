package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The tests of a ticket's publications (publications.go). Nothing here
// reaches a tracker: the store is handed what the API would have read.

// pubFixture is a store with a project, a ticket imported from GitHub's
// issue I_1 (key), and a native ticket with a delivery link to a pull
// request read once as node PR_9 (native, link).
type pubFixture struct {
	s                 *Store
	dir               string
	key, native, link string
}

func newPubFixture(t *testing.T) pubFixture {
	t.Helper()
	dir := storeDir(t)
	s, err := OpenOptions(dir, Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	f := pubFixture{s: s, dir: dir}
	newProject(t, s, "CND")
	f.key = mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "Title", "Body"))
	_, f.native = newTicket(t, s, "CND", "Delivered")
	rc, err := addLink(s, "d1", f.native, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/9"})
	want(t, rc, err, StatusDone, "")
	f.link = rc.ID
	if err := s.RecordRead(context.Background(), f.link, prRead("PR_9", 1, "open")); err != nil {
		t.Fatal(err)
	}
	return f
}

// markOf is GitHub's marker of a publication's id.
func markOf(id string) string { return "<!-- conductor-publication:" + id + " -->" }

var (
	issueTarget  = PublicationTarget{NodeID: "I_1", SpaceID: "4000001", Kind: ExternalIssue, URL: "https://github.com/rocksolidlabs/conductor/issues/612", Ref: "rocksolidlabs/conductor#612"}
	changeTarget = PublicationTarget{NodeID: "PR_9", SpaceID: "4000001", Kind: ExternalChange, URL: "https://github.com/rocksolidlabs/conductor/pull/9", Ref: "rocksolidlabs/conductor#9"}
	alice        = Sender{ID: "101", Login: "alice"}
)

// prepared prepares a publication of the imported ticket's linked issue
// (closeIssue as given) and reads it back.
func (f pubFixture) prepared(t *testing.T, opID, closeIssue string) Publication {
	t.Helper()
	rc, err := f.s.PreparePublication(context.Background(), testOp(opID), PublicationArgs{Key: f.key, Target: TargetLinked, Body: "Fixed in v2.", CloseIssue: closeIssue}, issueTarget, markOf)
	want(t, rc, err, StatusDone, "")
	return f.pub(t, rc.ID)
}

func (f pubFixture) pub(t *testing.T, id string) Publication {
	t.Helper()
	p, err := f.s.Publication(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// send begins a send of p as alice, failing unless the store answers.
func (f pubFixture) send(t *testing.T, opID string, p Publication) (Receipt, bool) {
	t.Helper()
	rc, owned, err := f.s.BeginSend(context.Background(), testOp(opID), p.ID, p.PostSha256, alice)
	if err != nil {
		t.Fatal(err)
	}
	return rc, owned
}

// settle settles p's current attempt with o as the server.
func (f pubFixture) settle(p Publication, o Outcome) (Receipt, bool, error) {
	return f.s.SettlePublication(context.Background(), p.ID, p.Attempt, o)
}

// beginClose begins the close of the publication's issue.
func (f pubFixture) beginClose(id string) (Receipt, bool, error) {
	return f.s.BeginClose(context.Background(), id)
}

// settleClose settles the publication's close with o.
func (f pubFixture) settleClose(id string, o CloseOutcome) (Receipt, bool, error) {
	return f.s.SettleClose(context.Background(), id, o)
}

// sentWithClose is a publication of the imported ticket's linked issue,
// sent and found, its close asked for and pending.
func (f pubFixture) sentWithClose(t *testing.T, opID string) Publication {
	t.Helper()
	p := f.prepared(t, opID, "completed")
	f.send(t, opID+"-s", p)
	if _, applied, err := f.settle(f.pub(t, p.ID), found("1", "101")); !applied || err != nil {
		t.Fatalf("found: %v %v", applied, err)
	}
	return f.pub(t, p.ID)
}

// heldAtOnce runs the calls at once, each held where Apply takes the
// store's lock (atOnce) until every one has read what it reads before it,
// and gives what each answered.
func heldAtOnce(t *testing.T, s *Store, calls ...func() (bool, error)) ([]bool, []error) {
	t.Helper()
	applied, errs := make([]bool, len(calls)), make([]error, len(calls))
	writes := make([]func() (Receipt, error), len(calls))
	for i, call := range calls {
		writes[i] = func() (Receipt, error) {
			applied[i], errs[i] = call()
			return Receipt{}, nil
		}
	}
	atOnce(t, s, writes...)
	return applied, errs
}

// found is an outcome finding the comment id by author.
func found(id, author string) Outcome {
	return Outcome{State: PublicationSent, Comment: &SentComment{ID: id, URL: "https://github.com/rocksolidlabs/conductor/issues/612#issuecomment-" + id, AuthorID: author, Author: "alice"}}
}

// failedReceipts counts the receipts the store keeps of refusals of the
// intent.
func failedReceipts(t *testing.T, s *Store, intent string) int {
	t.Helper()
	return countOf(t, s, "SELECT COUNT(*) FROM ops WHERE intent = ? AND status = ?", intent, StatusFailed)
}

// Every transition of the state machine, and every one refused: a draft
// sent and settled each way, a failed one sent again, an unconfirmed one
// never sent again and made sent by a finding, a sent one never sent
// again; discards from draft, failed and unconfirmed alone. Publications
// move no ticket's rev, and each operation tells OnChange of the
// publication with its ticket.
func TestPublicationStates(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	rev := ticketOf(t, f.s, f.key).Rev
	l := listen(f.s)
	p := f.prepared(t, "p1", "")
	if c := l.one(t); len(c) != 1 || c[0].Entity != EntityPublication || c[0].ID != p.ID || c[0].Ticket != p.TicketID || c[0].Project != "CND" {
		t.Fatalf("the prepare's changes: %+v", c)
	}
	if p.State != PublicationDraft || p.Rev != 1 || p.Ticket != f.key || p.Restored || p.Posted() != "Fixed in v2.\n\n"+markOf(p.ID) {
		t.Fatalf("the draft: %+v", p)
	}
	if rc, owned := f.send(t, "s1", p); !owned || rc.Status != StatusDone {
		t.Fatalf("the first send: %+v", rc)
	}
	p = f.pub(t, p.ID)
	if p.State != PublicationSending || p.Tries != 1 || p.Attempt != 1 || p.SentAsID != "101" || p.SentAsLogin != "alice" || p.SentBy != "Tester" ||
		!p.FindSince.Equal(p.SendingAt.Add(-PublicationFindWindow)) {
		t.Fatalf("sending: %+v", p)
	}
	// A second operation's send of a publication being sent is refused and
	// owns nothing.
	if rc, owned := f.send(t, "s1b", p); owned || rc.Refusal != RefusalInvalid || !strings.Contains(rc.Error, "being sent") {
		t.Fatalf("a sending one sent again: %v %+v", owned, rc)
	}
	if q := f.pub(t, p.ID); q.Tries != 1 || q.Rev != p.Rev {
		t.Fatalf("after a send refused: %+v", q)
	}
	since := p.FindSince
	if _, applied, err := f.settle(p, Outcome{State: PublicationFailed, Words: "GitHub refused it"}); !applied || err != nil {
		t.Fatalf("failed: %v %v", applied, err)
	}
	if p = f.pub(t, p.ID); p.State != PublicationFailed || p.Error != "GitHub refused it" {
		t.Fatalf("failed: %+v", p)
	}
	if _, owned := f.send(t, "s2", p); !owned {
		t.Fatal("a failed one is sent again")
	}
	if p = f.pub(t, p.ID); p.Attempt != 2 || !p.FindSince.Equal(since) || p.Error != "" {
		t.Fatalf("the second send: %+v", p)
	}
	if _, applied, err := f.settle(p, Outcome{State: PublicationUnconfirmed}); !applied || err != nil {
		t.Fatalf("unconfirmed: %v %v", applied, err)
	}
	p = f.pub(t, p.ID)
	rc, owned := f.send(t, "s3", p)
	if owned || rc.Refusal != RefusalInvalid || !strings.Contains(rc.Error, "not confirmed") {
		t.Fatalf("an unconfirmed one sent: %+v", rc)
	}
	if _, applied, err := f.settle(p, found("2153", "101")); !applied || err != nil {
		t.Fatalf("found: %v %v", applied, err)
	}
	if p = f.pub(t, p.ID); p.State != PublicationSent || p.RemoteID != "2153" || p.CloseState != "" || p.SentAt.IsZero() ||
		p.RemoteURL != "https://github.com/rocksolidlabs/conductor/issues/612#issuecomment-2153" {
		t.Fatalf("sent: %+v", p)
	}
	if rc, owned := f.send(t, "s4", p); owned || !strings.Contains(rc.Error, "already sent") {
		t.Fatalf("a sent one sent: %+v", rc)
	}
	rc, err := f.s.DiscardPublication(ctx, testOp("x1"), p.ID)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	// A settle that does not fit the state is refused and kept nowhere.
	if _, _, err := f.settle(p, Outcome{State: PublicationFailed}); !isRefusal(err, RefusalInvalid) {
		t.Fatalf("a sent one failed: %v", err)
	}
	// Discards: a draft, a failed and an unconfirmed one; a sending one is
	// not discarded, and a discarded one is never sent.
	for i, to := range []string{PublicationDraft, PublicationFailed, PublicationUnconfirmed, PublicationSending} {
		q := f.prepared(t, fmt.Sprint("q", i), "")
		if to != PublicationDraft {
			f.send(t, fmt.Sprint("qs", i), q)
			q = f.pub(t, q.ID)
		}
		if to == PublicationFailed || to == PublicationUnconfirmed {
			if _, _, err := f.settle(q, Outcome{State: to}); err != nil {
				t.Fatal(err)
			}
		}
		if to == PublicationDraft {
			// A draft of this store's is settled by nothing.
			if _, applied, err := f.settle(q, found("8", "101")); applied || !isRefusal(err, RefusalInvalid) {
				t.Fatalf("a draft settled: %v %v", applied, err)
			}
		}
		rc, err := f.s.DiscardPublication(ctx, testOp(fmt.Sprint("qd", i)), q.ID)
		if to == PublicationSending {
			want(t, rc, err, StatusFailed, RefusalInvalid)
			continue
		}
		want(t, rc, err, StatusDone, "")
		if q = f.pub(t, q.ID); q.State != PublicationDiscarded {
			t.Fatalf("%s discarded: %+v", to, q)
		}
		if _, applied, err := f.settle(q, found("8", "101")); applied || !isRefusal(err, RefusalInvalid) {
			t.Fatalf("a discarded one settled: %v %v", applied, err)
		}
		if rc, owned := f.send(t, fmt.Sprint("qx", i), q); owned || rc.Refusal != RefusalInvalid {
			t.Fatalf("a discarded one sent: %+v", rc)
		}
	}
	if got := ticketOf(t, f.s, f.key).Rev; got != rev {
		t.Fatalf("the ticket's rev moved: %d, was %d", got, rev)
	}
	if n := failedReceipts(t, f.s, IntentPublicationSettle); n != 0 {
		t.Fatalf("%d settles' refusals kept", n)
	}
}

// Every remote identity is opaque text: a UUID sender and comment id are
// kept and read back as given; 65 bytes is bound.
func TestPublicationRemoteIDs(t *testing.T) {
	f := newPubFixture(t)
	const uuid = "550e8400-e29b-41d4-a716-446655440000"
	p := f.prepared(t, "p1", "")
	rc, owned, err := f.s.BeginSend(context.Background(), testOp("long"), p.ID, p.PostSha256, Sender{ID: strings.Repeat("9", MaxRemoteID+1), Login: "a"})
	if err != nil || owned || rc.Refusal != RefusalBound {
		t.Fatalf("a sender of 65 bytes: %+v %v", rc, err)
	}
	if _, owned, err := f.s.BeginSend(context.Background(), testOp("s1"), p.ID, p.PostSha256, Sender{ID: uuid, Login: "alice"}); !owned || err != nil {
		t.Fatalf("a UUID sender: %v", err)
	}
	p = f.pub(t, p.ID)
	if _, _, err := f.settle(p, found(strings.Repeat("9", MaxRemoteID+1), uuid)); !isRefusal(err, RefusalBound) {
		t.Fatalf("a comment id of 65 bytes: %v", err)
	}
	if _, _, err := f.settle(p, found(uuid, uuid)); err != nil {
		t.Fatal(err)
	}
	if p = f.pub(t, p.ID); p.SentAsID != uuid || p.RemoteID != uuid {
		t.Fatalf("the ids read back: %+v", p)
	}
}

// What is sent is what was previewed: the prepare's post_sha256, the
// SHA-256 of the body, a blank line and the marker, is accepted; the
// body's own digest is refused (stale_revision), nothing changed.
func TestSendNeedsThePreviewedDigest(t *testing.T) {
	f := newPubFixture(t)
	p := f.prepared(t, "p1", "")
	if p.PostSha256 != sha256Hex("Fixed in v2.\n\n"+markOf(p.ID)) {
		t.Fatalf("post_sha256 %s", p.PostSha256)
	}
	rc, owned, err := f.s.BeginSend(context.Background(), testOp("s1"), p.ID, sha256Hex(p.Body), alice)
	if err != nil || owned || rc.Refusal != RefusalStaleRevision {
		t.Fatalf("the body's digest: %+v %v", rc, err)
	}
	if f.pub(t, p.ID).State != PublicationDraft {
		t.Fatal("a refused send changed the publication")
	}
	if _, owned := f.send(t, "s2", p); !owned {
		t.Fatal("the preview's digest was refused")
	}
}

// Only the call whose transaction made the change owns the send: the same
// operation again is answered from its receipt, owned false, and so is it
// once the receipt is pruned; one try.
func TestSendOwnedOnce(t *testing.T) {
	f := newPubFixture(t)
	p := f.prepared(t, "p1", "")
	if _, owned := f.send(t, "s1", p); !owned {
		t.Fatal("the first send owns nothing")
	}
	if rc, owned := f.send(t, "s1", p); owned || !rc.Repeat || rc.Status != StatusDone {
		t.Fatalf("the resend: %+v %v", rc, owned)
	}
	if _, err := f.s.db.Exec("DELETE FROM ops"); err != nil {
		t.Fatal(err)
	}
	if rc, owned := f.send(t, "s1", p); owned || !rc.Repeat || rc.Status != StatusDone {
		t.Fatalf("the resend, its receipt pruned: %+v %v", rc, owned)
	}
	if p = f.pub(t, p.ID); p.Tries != 1 {
		t.Fatalf("%d tries", p.Tries)
	}
}

// A settle is bound to its send's attempt: one of attempt 1 after attempt 2
// began is refused, kept nowhere; each settle of one row has an id of its
// own.
func TestSettleBoundToAttempt(t *testing.T) {
	f := newPubFixture(t)
	p := f.prepared(t, "p1", "")
	f.send(t, "s1", p)
	first := f.pub(t, p.ID)
	if _, _, err := f.settle(first, Outcome{State: PublicationFailed, Words: "refused"}); err != nil {
		t.Fatal(err)
	}
	f.send(t, "s2", first)
	if _, _, err := f.settle(first, found("1", "101")); !isRefusal(err, RefusalInvalid) {
		t.Fatalf("attempt 1's late answer: %v", err)
	}
	if _, applied, err := f.settle(f.pub(t, p.ID), found("2", "101")); !applied || err != nil {
		t.Fatalf("attempt 2's: %v %v", applied, err)
	}
	ids := texts(t, f.s, "SELECT DISTINCT op_id FROM changes WHERE entity = ? AND op_id LIKE '~%' ORDER BY seq", EntityPublication)
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("the settles' ids %v", ids)
	}
	if p = f.pub(t, p.ID); p.RemoteID != "2" || failedReceipts(t, f.s, IntentPublicationSettle) != 0 {
		t.Fatalf("the row %+v", p)
	}
}

// Two different settles that read one revision, held where Apply takes the
// store's lock until both have read: one applied, the other refused
// stale_revision (never op_conflict) and kept nowhere, two operations with
// ids of their own.
func TestSettlesAtOneRevision(t *testing.T) {
	f := newPubFixture(t)
	p := f.prepared(t, "p1", "")
	f.send(t, "s1", p)
	p = f.pub(t, p.ID)
	var ids [2]string
	applied, errs := heldAtOnce(t, f.s,
		func() (bool, error) {
			rc, a, err := f.settle(p, found("7", "101"))
			ids[0] = rc.OpID
			return a, err
		},
		func() (bool, error) {
			rc, a, err := f.settle(p, Outcome{State: PublicationFailed, Words: "refused"})
			ids[1] = rc.OpID
			return a, err
		})
	won := slices.Index(applied, true)
	if won < 0 || applied[1-won] || errs[won] != nil || !isRefusal(errs[1-won], RefusalStaleRevision) {
		t.Fatalf("two settles at rev %d: applied %v, %v", p.Rev, applied, errs)
	}
	if ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Fatalf("the settles' ids %v", ids)
	}
	if q := f.pub(t, p.ID); q.Rev != p.Rev+1 || failedReceipts(t, f.s, IntentPublicationSettle) != 0 {
		t.Fatalf("the row %+v", q)
	}
}

// A settle that changes nothing is not applied: a listing's words, then
// the same words again, leave one operation and no receipt for the second;
// a finding at the same revision is applied.
func TestSettleSkipsUnchanged(t *testing.T) {
	f := newPubFixture(t)
	p := f.prepared(t, "p1", "")
	f.send(t, "s1", p)
	p = f.pub(t, p.ID)
	unknown := Outcome{Words: "the listing was cut at its tenth page"}
	if _, applied, err := f.settle(p, unknown); !applied || err != nil {
		t.Fatalf("the first listing: %v %v", applied, err)
	}
	ops := countOf(t, f.s, "SELECT COUNT(*) FROM ops")
	if _, applied, err := f.settle(f.pub(t, p.ID), unknown); applied || err != nil {
		t.Fatalf("the same words: %v %v", applied, err)
	}
	if n := countOf(t, f.s, "SELECT COUNT(*) FROM ops"); n != ops {
		t.Fatalf("%d receipts, %d before", n, ops)
	}
	if _, applied, err := f.settle(f.pub(t, p.ID), found("7", "101")); !applied || err != nil {
		t.Fatalf("the finding: %v %v", applied, err)
	}
	if p = f.pub(t, p.ID); p.State != PublicationSent {
		t.Fatalf("the row %+v", p)
	}
}

// A close is asked for an issue alone: a pull request's prepare with a
// close is invalid, an issue's is kept; a reason other than completed or
// not_planned is invalid.
func TestCloseIssueOnlyForIssues(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		target PublicationTarget
		key    string
		args   PublicationArgs
		status string
	}{
		{changeTarget, f.native, PublicationArgs{Target: "link:" + f.link, CloseIssue: "completed"}, StatusFailed},
		{issueTarget, f.key, PublicationArgs{Target: TargetLinked, CloseIssue: "wontfix"}, StatusFailed},
		{changeTarget, f.native, PublicationArgs{Target: "link:" + f.link}, StatusDone},
		{issueTarget, f.key, PublicationArgs{Target: TargetLinked, CloseIssue: "not_planned"}, StatusDone},
	} {
		c.args.Key, c.args.Body = c.key, "b"
		rc, err := f.s.PreparePublication(ctx, testOp("p"+newID()), c.args, c.target, markOf)
		if err != nil || rc.Status != c.status {
			t.Fatalf("%+v: %+v %v", c.args, rc, err)
		}
	}
}

// A close moves pending, closing, refused, closing again, closed; every
// begin is one more try and is committed before the close; a close of a
// publication with none asked for, or one closed, is refused and kept
// nowhere.
func TestPublicationCloseStates(t *testing.T) {
	f := newPubFixture(t)
	p := f.prepared(t, "p1", "completed")
	f.send(t, "s1", p)
	if _, _, err := f.settle(f.pub(t, p.ID), found("9", "101")); err != nil {
		t.Fatal(err)
	}
	if p = f.pub(t, p.ID); p.CloseState != ClosePending || !p.NeedsYou() {
		t.Fatalf("sent with a close asked: %+v", p)
	}
	steps := []struct {
		begin bool
		o     CloseOutcome
		state string
		tries int
	}{
		{true, CloseOutcome{}, CloseClosing, 1},
		{false, CloseOutcome{State: CloseRefused, Words: "GitHub refused the close"}, CloseRefused, 1},
		{true, CloseOutcome{}, CloseClosing, 2},
		{false, CloseOutcome{State: CloseClosed}, CloseClosed, 2},
	}
	for i, st := range steps {
		var applied bool
		var err error
		if st.begin {
			_, applied, err = f.beginClose(p.ID)
		} else {
			_, applied, err = f.settleClose(p.ID, st.o)
		}
		if p = f.pub(t, p.ID); !applied || err != nil || p.CloseState != st.state || p.CloseTries != st.tries {
			t.Fatalf("step %d: %v %v %+v", i, applied, err, p)
		}
	}
	if _, _, err := f.beginClose(p.ID); !isRefusal(err, RefusalInvalid) || p.NeedsYou() {
		t.Fatalf("a closed one begun again: %v", err)
	}
	q := f.prepared(t, "p2", "")
	f.send(t, "s2", q)
	if _, _, err := f.settle(f.pub(t, q.ID), found("10", "101")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.beginClose(q.ID); !isRefusal(err, RefusalInvalid) {
		t.Fatalf("a close not asked for: %v", err)
	}
	if n := failedReceipts(t, f.s, IntentPublicationClose); n != 0 {
		t.Fatalf("%d refusals kept", n)
	}
}

// A close left closing by a crash, with no other field to change, begins
// again: each begin commits and moves close_tries, and the sixth is bound.
func TestCloseResumesFromClosing(t *testing.T) {
	f := newPubFixture(t)
	p := f.prepared(t, "p1", "completed")
	f.send(t, "s1", p)
	if _, _, err := f.settle(f.pub(t, p.ID), found("9", "101")); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= MaxPublicationCloseTries; i++ {
		if _, applied, err := f.beginClose(p.ID); !applied || err != nil {
			t.Fatalf("begin %d: %v %v", i, applied, err)
		}
		if p = f.pub(t, p.ID); p.CloseState != CloseClosing || p.CloseTries != i {
			t.Fatalf("begin %d: %+v", i, p)
		}
	}
	if _, _, err := f.beginClose(p.ID); !isRefusal(err, RefusalBound) {
		t.Fatalf("the sixth: %v", err)
	}
}

// No operation the server makes for itself changes nothing, and each is
// decided from the state it starts in: every settle and close outcome, on a
// fresh publication in each state (a restored draft and a restored failed
// one among them), moves the row as the table says, or is refused (kept
// nowhere) or not applied (no operation, no receipt); the same outcome
// again is not applied, but a close begun, which always moves close_tries;
// the next different outcome applies.
func TestServerOpsNeverNoop(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	const nOps = 6
	n := 0
	opID := func() string { n++; return fmt.Sprint("n", n) }
	// A restored draft and failed publication for each outcome, made
	// before the store is a new instance.
	restored := map[string][]string{}
	for range nOps {
		d := f.prepared(t, opID(), "completed")
		r := f.prepared(t, opID(), "completed")
		f.send(t, opID(), r)
		if _, _, err := f.settle(f.pub(t, r.ID), Outcome{State: PublicationFailed}); err != nil {
			t.Fatal(err)
		}
		restored["restored draft"] = append(restored["restored draft"], d.ID)
		restored["restored failed"] = append(restored["restored failed"], r.ID)
	}
	f.s.Close()
	if err := Reissue(ctx, filepath.Join(f.dir, File)); err != nil {
		t.Fatal(err)
	}
	s, err := OpenOptions(f.dir, Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	f.s = s
	// fresh is a new publication in the state, the i-th of a restored one.
	fresh := func(state string, i int) Publication {
		if ids, ok := restored[state]; ok {
			return f.pub(t, ids[i])
		}
		switch state {
		case "sent":
			return f.sentWithClose(t, opID())
		case "closing":
			p := f.sentWithClose(t, opID())
			if _, applied, err := f.beginClose(p.ID); !applied || err != nil {
				t.Fatal(applied, err)
			}
			return f.pub(t, p.ID)
		}
		p := f.prepared(t, opID(), "completed")
		switch state {
		case PublicationDiscarded:
			rc, err := f.s.DiscardPublication(ctx, testOp(opID()), p.ID)
			want(t, rc, err, StatusDone, "")
		case PublicationSending, PublicationFailed, PublicationUnconfirmed:
			f.send(t, opID(), p)
			if state != PublicationSending {
				if _, _, err := f.settle(f.pub(t, p.ID), Outcome{State: state}); err != nil {
					t.Fatal(err)
				}
			}
		}
		return f.pub(t, p.ID)
	}
	settle := func(o Outcome) func(Publication) (bool, error) {
		return func(p Publication) (bool, error) { _, a, err := f.settle(p, o); return a, err }
	}
	ops := [nOps]struct {
		name string
		run  func(p Publication) (bool, error)
	}{
		{"words", settle(Outcome{Words: "cut"})}, {"failed", settle(Outcome{State: PublicationFailed, Words: "no"})},
		{"unconfirmed", settle(Outcome{State: PublicationUnconfirmed})}, {"found", settle(found("99", "101"))},
		{"close begun", func(p Publication) (bool, error) { _, a, err := f.beginClose(p.ID); return a, err }},
		{"closed", func(p Publication) (bool, error) {
			_, a, err := f.settleClose(p.ID, CloseOutcome{State: CloseClosed})
			return a, err
		}},
	}
	// What each outcome makes of each state, in the order of ops: the row's
	// state and its close's after it, refused, or same (not applied).
	const refused, same = "refused", "same"
	table := []struct {
		state string
		after [nOps]string
	}{
		{PublicationDraft, [nOps]string{refused, refused, refused, refused, refused, refused}},
		{PublicationSending, [nOps]string{"sending/", "failed/", "unconfirmed/", "sent/pending", refused, refused}},
		{PublicationFailed, [nOps]string{refused, refused, refused, refused, refused, refused}},
		{PublicationUnconfirmed, [nOps]string{"unconfirmed/", refused, same, "sent/pending", refused, refused}},
		{"sent", [nOps]string{refused, refused, refused, refused, "sent/closing", refused}},
		{"closing", [nOps]string{refused, refused, refused, refused, "sent/closing", "sent/closed"}},
		{PublicationDiscarded, [nOps]string{refused, refused, refused, refused, refused, refused}},
		{"restored draft", [nOps]string{same, same, same, "sent/pending", refused, refused}},
		{"restored failed", [nOps]string{same, same, same, "sent/pending", refused, refused}},
	}
	for _, row := range table {
		for i, op := range ops {
			p := fresh(row.state, i)
			kept := countOf(t, f.s, "SELECT COUNT(*) FROM ops")
			first, err := op.run(p)
			q := f.pub(t, p.ID)
			got := q.State + "/" + q.CloseState
			switch want := row.after[i]; want {
			case refused, same:
				if first || (want == refused) != isRefusal(err, RefusalInvalid) || (want == same && err != nil) || q.Rev != p.Rev ||
					countOf(t, f.s, "SELECT COUNT(*) FROM ops") != kept {
					t.Errorf("%s from %s: applied %v, %v, %s; want %s, nothing kept", op.name, row.state, first, err, got, want)
				}
			default:
				fields := map[string]bool{"words": q.Error == "cut", "failed": q.Error == "no", "found": q.RemoteID == "99" && q.SentAsID == "101",
					"close begun": q.CloseTries == p.CloseTries+1}
				if held, named := fields[op.name]; !first || err != nil || got != want || (named && !held) {
					t.Errorf("%s from %s: applied %v, %v, %+v; want %s", op.name, row.state, first, err, q, want)
				}
			}
			second, _ := op.run(q)
			if second != (first && op.name == "close begun") {
				t.Errorf("%s from %s again: applied %v", op.name, row.state, second)
			}
		}
	}
	for _, intent := range []string{IntentPublicationSettle, IntentPublicationClose} {
		if n := failedReceipts(t, f.s, intent); n != 0 {
			t.Errorf("%d refusals of %s kept", n, intent)
		}
	}
	// The next different outcome applies.
	p := f.prepared(t, "next", "")
	f.send(t, "next-s", p)
	for i, o := range []Outcome{{Words: "a"}, {Words: "a"}, {Words: "b"}, found("5", "101")} {
		_, applied, err := f.settle(f.pub(t, p.ID), o)
		if err != nil || applied != (i != 1) {
			t.Fatalf("outcome %d: %v %v", i, applied, err)
		}
	}
}

// The five destination columns are fixed at prepare: immutable, and no
// later operation's change row names one; a target read that is not the
// linked issue's node, or not the node a link's last read found, or a link
// never read, is invalid.
func TestPrepareTargetImmutable(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	dest := []string{"target_kind", "target_url", "target_ref", "target_node_id", "target_space_id"}
	for _, col := range dest {
		if Class(EntityPublication, col) != ClassImmutable {
			t.Errorf("%s is not immutable", col)
		}
	}
	p := f.prepared(t, "p1", "completed")
	f.send(t, "s1", p)
	f.settle(f.pub(t, p.ID), found("1", "101"))
	f.beginClose(p.ID)
	f.settleClose(p.ID, CloseOutcome{State: CloseClosed})
	for _, col := range dest {
		if n := countOf(t, f.s, "SELECT COUNT(*) FROM changes WHERE entity = ? AND field = ?", EntityPublication, col); n != 0 {
			t.Errorf("%d rows write %s", n, col)
		}
	}
	if q := f.pub(t, p.ID); q.TargetNodeID != issueTarget.NodeID || q.TargetSpaceID != "4000001" || q.TargetKind != ExternalIssue || q.TargetRef != issueTarget.Ref {
		t.Fatalf("the destination moved: %+v", q)
	}
	other := issueTarget
	other.NodeID = "I_2"
	_, unread := newTicket(t, f.s, "CND", "Unread")
	lrc, err := addLink(f.s, "d2", unread, NewLink{Kind: LinkExternal, URL: "https://github.com/rocksolidlabs/conductor/pull/10"})
	want(t, lrc, err, StatusDone, "")
	wrongNode := changeTarget
	wrongNode.NodeID = "PR_10"
	for i, c := range []struct {
		key, target string
		t           PublicationTarget
	}{
		{f.key, TargetLinked, other}, {f.native, TargetLinked, changeTarget}, {f.native, "link:" + f.link, wrongNode},
		{unread, "link:" + lrc.ID, changeTarget}, {f.key, "link:" + f.link, changeTarget},
	} {
		rc, err := f.s.PreparePublication(ctx, testOp(fmt.Sprint("bad", i)), PublicationArgs{Key: c.key, Target: c.target, Body: "b"}, c.t, markOf)
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
}

// A restored store is a new instance: its draft and its failed
// publication are never sent from it; a listing without the marker changes
// nothing; a finding makes either sent, its sender the comment's author.
func TestSendRefusedAfterReissue(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	draft := f.prepared(t, "p1", "")
	failed := f.prepared(t, "p2", "")
	f.send(t, "s2", failed)
	f.settle(f.pub(t, failed.ID), Outcome{State: PublicationFailed})
	f.s.Close()
	if err := Reissue(ctx, filepath.Join(f.dir, File)); err != nil {
		t.Fatal(err)
	}
	s, err := OpenOptions(f.dir, Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	f.s = s
	for i, p := range []Publication{f.pub(t, draft.ID), f.pub(t, failed.ID)} {
		if !p.Restored || !p.NeedsYou() {
			t.Fatalf("not restored: %+v", p)
		}
		rc, owned, err := s.BeginSend(ctx, testOp(fmt.Sprint("r", i)), p.ID, p.PostSha256, alice)
		if err != nil || owned || rc.Refusal != RefusalInvalid || !strings.Contains(rc.Error, "restored") {
			t.Fatalf("a restored %s sent: %+v %v", p.State, rc, err)
		}
		for _, o := range []Outcome{{State: PublicationUnconfirmed}, {Words: "not found"}} {
			if _, applied, err := f.settle(p, o); applied || err != nil {
				t.Fatalf("a restored %s settled by %+v: %v %v", p.State, o, applied, err)
			}
		}
		if _, applied, err := f.settle(p, found(fmt.Sprint(70+i), "202")); !applied || err != nil {
			t.Fatalf("a restored %s found: %v %v", p.State, applied, err)
		}
		if q := f.pub(t, p.ID); q.State != PublicationSent || q.SentAsID != "202" {
			t.Fatalf("found: %+v", q)
		}
	}
}

// A ticket's read carries its newest ten publications and counts the
// older ones a person can act on (an unconfirmed one eleventh newest);
// Publications pages them all, fifty a page, and needYou lists the one.
func TestPublicationsPaged(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	old := f.prepared(t, "old", "")
	f.send(t, "old-s", old)
	f.settle(f.pub(t, old.ID), Outcome{State: PublicationUnconfirmed})
	for i := 0; i < MaxPublicationPage; i++ {
		f.prepared(t, fmt.Sprint("p", i), "")
	}
	v, err := f.s.Ticket(ctx, f.key)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Publications) != MaxTicketPublications || v.PublicationsNeedYou != 1 || slices.ContainsFunc(v.Publications, func(p PublicationSummary) bool { return p.ID == old.ID }) {
		t.Fatalf("the read: %d publications, %d need you", len(v.Publications), v.PublicationsNeedYou)
	}
	page, next, err := f.s.Publications(ctx, f.key, "", false)
	if err != nil || len(page) != MaxPublicationPage || next == "" || page[0].ID != v.Publications[0].ID {
		t.Fatalf("page 1: %d %q %v", len(page), next, err)
	}
	rest, end, err := f.s.Publications(ctx, f.key, next, false)
	if err != nil || len(rest) != 1 || rest[0].ID != old.ID || end != "" {
		t.Fatalf("page 2: %+v %q %v", rest, end, err)
	}
	mine, _, err := f.s.Publications(ctx, f.key, "", true)
	if err != nil || len(mine) != 1 || mine[0].ID != old.ID {
		t.Fatalf("need you: %+v %v", mine, err)
	}
}

// A publication is posted at most five times: the sixth send is bound.
func TestPublicationTriesBound(t *testing.T) {
	f := newPubFixture(t)
	p := f.prepared(t, "p1", "")
	for i := 1; i <= MaxPublicationTries; i++ {
		if _, owned := f.send(t, fmt.Sprint("s", i), f.pub(t, p.ID)); !owned {
			t.Fatalf("send %d", i)
		}
		f.settle(f.pub(t, p.ID), Outcome{State: PublicationFailed})
	}
	if rc, owned := f.send(t, "s6", f.pub(t, p.ID)); owned || rc.Refusal != RefusalBound {
		t.Fatalf("the sixth: %+v", rc)
	}
}

// The marker the adapter gives is stored and inside post_sha256, and is
// immutable; one without the id, or past 128 bytes, is invalid.
func TestPrepareKeepsTheMarker(t *testing.T) {
	f := newPubFixture(t)
	p := f.prepared(t, "p1", "")
	if p.Marker != markOf(p.ID) || p.PostSha256 != sha256Hex(p.Body+"\n\n"+markOf(p.ID)) || Class(EntityPublication, "marker") != ClassImmutable {
		t.Fatalf("the marker: %+v", p)
	}
	for i, mark := range []func(string) string{
		func(string) string { return "<!-- conductor-publication -->" },
		func(id string) string { return id + strings.Repeat("x", MaxPublicationMarker+1-len(id)) },
		func(string) string { return "" },
	} {
		rc, err := f.s.PreparePublication(context.Background(), testOp(fmt.Sprint("m", i)), PublicationArgs{Key: f.key, Target: TargetLinked, Body: "b"}, issueTarget, mark)
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
}

// A body is kept in the form a listing's match reads: CRLF and lone CR
// made LF, trailing white space trimmed; empty, past 16 KiB, or holding a
// control character is refused.
func TestPublicationBody(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	rc, err := f.s.PreparePublication(ctx, testOp("b1"), PublicationArgs{Key: f.key, Target: TargetLinked, Body: "one\r\ntwo\rthree \t\n\n"}, issueTarget, markOf)
	want(t, rc, err, StatusDone, "")
	if p := f.pub(t, rc.ID); p.Body != "one\ntwo\nthree" {
		t.Fatalf("the body %q", p.Body)
	}
	for i, c := range []struct{ body, refusal string }{
		{" \n", RefusalInvalid}, {strings.Repeat("a", MaxPublicationBody+1), RefusalBound}, {"a\x1bb", RefusalInvalid}, {"a\xffb", RefusalInvalid},
	} {
		rc, err := f.s.PreparePublication(ctx, testOp(fmt.Sprint("bad", i)), PublicationArgs{Key: f.key, Target: TargetLinked, Body: c.body}, issueTarget, markOf)
		want(t, rc, err, StatusFailed, c.refusal)
	}
}

// A person's act on a publication needs its ticket live and its project
// open, as a comment's write does: with the ticket deleted, or its project
// archived, a send's begin, a discard and a close's begin are refused as a
// comment is (not_found, archived), nothing owned and the row unchanged;
// a send begun before is still settled sent by a finding.
func TestPublicationNeedsALiveTicket(t *testing.T) {
	ctx := context.Background()
	for _, gone := range []string{RefusalNotFound, RefusalArchived} {
		t.Run(gone, func(t *testing.T) {
			f := newPubFixture(t)
			draft := f.prepared(t, "p1", "")
			sending := f.prepared(t, "p2", "")
			f.send(t, "s2", sending)
			sent := f.sentWithClose(t, "p3")
			var rc Receipt
			var err error
			if gone == RefusalNotFound {
				rc, err = f.s.DeleteTicket(ctx, testOp("gone"), f.key)
			} else {
				rc, err = f.s.ArchiveProject(ctx, testOp("gone"), "CND")
			}
			want(t, rc, err, StatusDone, "")
			comment, err := f.s.AddComment(ctx, testOp("c1"), f.key, "a comment")
			if err != nil || comment.Refusal != gone || comment.Error == "" {
				t.Fatalf("a comment: %+v %v", comment, err)
			}
			if rc, owned, err := f.s.BeginSend(ctx, testOp("s1"), draft.ID, draft.PostSha256, alice); err != nil || owned || rc.Refusal != gone || rc.Error != comment.Error {
				t.Errorf("a send's begin: %v %+v %v, a comment's %q", owned, rc, err, comment.Error)
			}
			if rc, err := f.s.DiscardPublication(ctx, testOp("x1"), draft.ID); err != nil || rc.Refusal != gone || rc.Error != comment.Error {
				t.Errorf("a discard: %+v %v, a comment's %q", rc, err, comment.Error)
			}
			var ref *Refusal
			if _, applied, err := f.beginClose(sent.ID); applied || !isRefusal(err, gone) || !errors.As(err, &ref) || words(ref.Words) != comment.Error {
				t.Errorf("a close's begin: %v %v, a comment's %q", applied, err, comment.Error)
			}
			for _, p := range []Publication{draft, sent} {
				if q := f.pub(t, p.ID); q.Rev != p.Rev || q.State != p.State || q.CloseState != p.CloseState {
					t.Errorf("the row moved: %+v", q)
				}
			}
			if _, applied, err := f.settle(f.pub(t, sending.ID), found("2", "101")); !applied || err != nil {
				t.Errorf("a send begun before, found: %v %v", applied, err)
			}
			if q := f.pub(t, sending.ID); q.State != PublicationSent {
				t.Errorf("a send begun before: %+v", q)
			}
		})
	}
}

// What prepare fixes is the item the store holds, by its node, its host
// and its kind: with two tickets linked to one node id on two hosts, a
// target of the other host is refused; a target of another kind than the
// store's is refused; a close asked of a pull request is refused although
// the caller calls it an issue. The refusal names the ticket's key and
// nothing of the target.
func TestPrepareTargetIsTheStores(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	ghes := ghRead("I_1", 0, "T", "B")
	ghes.Snapshot.Host, ghes.Snapshot.URL = "ghe.example.com", "https://ghe.example.com/rocksolidlabs/conductor/issues/612"
	other := mustImport(t, f.s, "imp2", "CND", ghes)
	ghesTarget := issueTarget
	ghesTarget.URL = ghes.Snapshot.URL
	asChange, asIssue := issueTarget, changeTarget
	asChange.Kind, asIssue.Kind = ExternalChange, ExternalIssue
	for i, c := range []struct {
		why, key, target, close string
		t                       PublicationTarget
		status                  string
	}{
		{"another host's item", f.key, TargetLinked, "", ghesTarget, StatusFailed},
		{"another host's item", other, TargetLinked, "", issueTarget, StatusFailed},
		{"another kind", f.key, TargetLinked, "", asChange, StatusFailed},
		{"a pull request called an issue", f.native, "link:" + f.link, "", asIssue, StatusFailed},
		{"a pull request's close", f.native, "link:" + f.link, "completed", asIssue, StatusFailed},
		{"the item itself", other, TargetLinked, "completed", ghesTarget, StatusDone},
	} {
		rc, err := f.s.PreparePublication(ctx, testOp(fmt.Sprint("t", i)), PublicationArgs{Key: c.key, Target: c.target, Body: "b", CloseIssue: c.close}, c.t, markOf)
		if err != nil || rc.Status != c.status || (c.status == StatusFailed && (rc.Refusal != RefusalInvalid || !strings.Contains(rc.Error, c.key) ||
			strings.Contains(rc.Error, "ghe.") || strings.Contains(rc.Error, c.t.NodeID))) {
			t.Errorf("%s: %+v %v", c.why, rc, err)
		}
	}
}

// A link's target is on the host of both the link's address and the
// store's last read of it: with a read kept under another host than the
// address's, a target on either host is refused. A refusal of a target
// names the ticket's key and nothing the caller sent of it: not the page's
// user, query or host.
func TestPrepareTargetHostIsTheReadsToo(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	_, elsewhere := newTicket(t, f.s, "CND", "Read elsewhere")
	rc, err := addLink(f.s, "d2", elsewhere, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/10"})
	want(t, rc, err, StatusDone, "")
	link := rc.ID
	read := prRead("PR_10", 1, "open")
	read.Host = "ghe.example.com"
	if err := f.s.RecordRead(ctx, link, read); err != nil {
		t.Fatal(err)
	}
	onAddress := PublicationTarget{NodeID: "PR_10", SpaceID: "4000001", Kind: ExternalChange, URL: "https://github.com/rocksolidlabs/conductor/pull/10", Ref: "rocksolidlabs/conductor#10"}
	onRead := onAddress
	onRead.URL = "https://ghe.example.com/rocksolidlabs/conductor/pull/10"
	for i, target := range []PublicationTarget{onAddress, onRead} {
		rc, err := f.s.PreparePublication(ctx, testOp(fmt.Sprint("h", i)), PublicationArgs{Key: elsewhere, Target: "link:" + link, Body: "b"}, target, markOf)
		if err != nil || rc.Status != StatusFailed || rc.Refusal != RefusalInvalid || !strings.Contains(rc.Error, elsewhere) {
			t.Errorf("a target on %s: %+v %v", target.URL, rc, err)
		}
	}
	for i, page := range []string{
		"https://bob:hunter2@github.com/rocksolidlabs/conductor/pull/9",
		"https://github.com/rocksolidlabs/conductor/pull/9?token=SECRET",
		"https://github.com/rocksolidlabs/conductor/pull/9#SECRET",
		"https://ghe.example.com/rocksolidlabs/conductor/pull/9",
		"http://x.example.com/rocksolidlabs/conductor/pull/9",
		"https://github.com/rocksolidlabs/conductor/pull/9/" + strings.Repeat("s", MaxExternalURL),
		"https://github.com/rocksolidlabs/conductor/pull/9\x00SECRET",
	} {
		target := changeTarget
		target.URL = page
		rc, err := f.s.PreparePublication(ctx, testOp(fmt.Sprint("u", i)), PublicationArgs{Key: f.native, Target: "link:" + f.link, Body: "b"}, target, markOf)
		if err != nil || rc.Status != StatusFailed || !strings.Contains(rc.Error, f.native) {
			t.Errorf("a target at %q: %+v %v", page, rc, err)
		}
		for _, sent := range []string{"bob", "hunter2", "token", "SECRET", "github.com", "ghe.", "x.example", "sss"} {
			if strings.Contains(rc.Error, sent) {
				t.Errorf("a target at %q: the refusal %q names %q", page, rc.Error, sent)
			}
		}
	}
}

// A link target is the item the link's last read found under a system the
// store links: a read kept under another system, of the node on the link's
// own host, is refused in prepare's words, which name the key.
func TestPrepareTargetSystemIsTheStores(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	_, other := newTicket(t, f.s, "CND", "Read under another system")
	rc, err := addLink(f.s, "d2", other, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/10"})
	want(t, rc, err, StatusDone, "")
	link := rc.ID
	read := prRead("PR_10", 1, "open")
	read.System = "forge"
	if err := f.s.RecordRead(ctx, link, read); err != nil {
		t.Fatal(err)
	}
	target := PublicationTarget{NodeID: "PR_10", SpaceID: "4000001", Kind: ExternalChange, URL: "https://github.com/rocksolidlabs/conductor/pull/10", Ref: "rocksolidlabs/conductor#10"}
	rc, err = f.s.PreparePublication(ctx, testOp("p1"), PublicationArgs{Key: other, Target: "link:" + link, Body: "b"}, target, markOf)
	if err != nil || rc.Status != StatusFailed || rc.Refusal != RefusalInvalid || !strings.Contains(rc.Error, other) {
		t.Errorf("a target read under another system: %+v %v", rc, err)
	}
	// The fixture's link, read under the store's own system, is a target.
	rc, err = f.s.PreparePublication(ctx, testOp("p2"), PublicationArgs{Key: f.native, Target: "link:" + f.link, Body: "b"}, changeTarget, markOf)
	want(t, rc, err, StatusDone, "")
}

// The list of the publications being sent has the store's bound: none for
// n under 1, at most n, and never more than MaxSendingPublications, here
// with 300 publications being sent (three tickets of MaxPublications).
func TestSendingPublicationsBound(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	const sending = 300
	keys, targets := []string{f.key}, []PublicationTarget{issueTarget}
	for i := 2; len(keys)*MaxPublications < sending; i++ {
		read := ghRead(fmt.Sprint("I_", i), 0, "Title", "Body")
		read.Snapshot.ID = fmt.Sprint(611 + i)
		read.Snapshot.URL = "https://github.com/rocksolidlabs/conductor/issues/" + read.Snapshot.ID
		keys = append(keys, mustImport(t, f.s, fmt.Sprint("imp", i), "CND", read))
		target := issueTarget
		target.NodeID, target.URL, target.Ref = read.Snapshot.NodeID, read.Snapshot.URL, "rocksolidlabs/conductor#"+read.Snapshot.ID
		targets = append(targets, target)
	}
	for i := range sending {
		k := i / MaxPublications
		rc, err := f.s.PreparePublication(ctx, testOp(fmt.Sprint("p", i)), PublicationArgs{Key: keys[k], Target: TargetLinked, Body: "b"}, targets[k], markOf)
		want(t, rc, err, StatusDone, "")
		if _, owned := f.send(t, fmt.Sprint("s", i), f.pub(t, rc.ID)); !owned {
			t.Fatalf("publication %d: its send not begun", i)
		}
	}
	if n := countOf(t, f.s, "SELECT COUNT(*) FROM publications WHERE state = ?", PublicationSending); n != sending {
		t.Fatalf("%d publications being sent, want %d", n, sending)
	}
	for _, c := range []struct{ n, want int }{{-1, 0}, {0, 0}, {1, 1}, {2, 2}, {MaxSendingPublications, MaxSendingPublications},
		{sending, MaxSendingPublications}, {1000, MaxSendingPublications}} {
		if ids, err := f.s.SendingPublications(ctx, c.n); err != nil || len(ids) != c.want {
			t.Errorf("SendingPublications(%d): %d ids %v, want %d", c.n, len(ids), err, c.want)
		}
	}
}

// The store's actor is the store's alone: every exported method of the
// store that takes an operation, handed one under ActorServer, answers the
// actor's refusal (ErrBadActor) and writes nothing. ImportImage looks at
// its image's shape first (uploaded, or skipped for its type or its size),
// so it is handed a skipped one.
func TestTheServerActorIsTheStores(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	p := f.prepared(t, "p1", "")
	changes, ops := countOf(t, f.s, "SELECT COUNT(*) FROM changes"), countOf(t, f.s, "SELECT COUNT(*) FROM ops")
	opType, ctxType := reflect.TypeFor[Op](), reflect.TypeFor[context.Context]()
	given := map[reflect.Type]reflect.Value{reflect.TypeFor[ImageUpload](): reflect.ValueOf(ImageUpload{Skipped: SkippedType})}
	// filled is a value of the type with every string an id (the
	// publication's) and every int 1, so an argument's shape is not what
	// a method refuses first.
	var filled func(reflect.Type) reflect.Value
	filled = func(typ reflect.Type) reflect.Value {
		if v, ok := given[typ]; ok {
			return v
		}
		v := reflect.New(typ).Elem()
		switch typ.Kind() {
		case reflect.String:
			v.SetString(p.ID)
		case reflect.Int, reflect.Int64:
			v.SetInt(1)
		case reflect.Struct:
			for i := range typ.NumField() {
				if typ.Field(i).IsExported() {
					v.Field(i).Set(filled(typ.Field(i).Type))
				}
			}
		}
		return v
	}
	n := 0
	st := reflect.TypeFor[*Store]()
	for i := range st.NumMethod() {
		m := st.Method(i)
		var ins []reflect.Type
		for j := 1; j < m.Type.NumIn(); j++ {
			ins = append(ins, m.Type.In(j))
		}
		if !slices.Contains(ins, opType) {
			continue
		}
		n++
		args := []reflect.Value{reflect.ValueOf(f.s)}
		for _, in := range ins {
			switch {
			case in == opType:
				args = append(args, reflect.ValueOf(Op{Actor: ActorServer, ID: fmt.Sprint("walk", i), By: "Tester"}))
			case in == ctxType:
				args = append(args, reflect.ValueOf(ctx))
			default:
				args = append(args, filled(in))
			}
		}
		var out []reflect.Value
		if m.Type.IsVariadic() {
			out = m.Func.CallSlice(args)
		} else {
			out = m.Func.Call(args)
		}
		err, _ := out[len(out)-1].Interface().(error)
		if !errors.Is(err, ErrBadActor) {
			t.Errorf("%s under the store's actor: %v", m.Name, err)
		}
	}
	if t.Logf("%d methods take an operation", n); n < 60 {
		t.Errorf("%d methods take an operation", n)
	}
	if c, o := countOf(t, f.s, "SELECT COUNT(*) FROM changes"), countOf(t, f.s, "SELECT COUNT(*) FROM ops"); c != changes || o != ops {
		t.Errorf("%d changes and %d receipts, %d and %d before", c, o, changes, ops)
	}
}

// A store opened read-only makes no server operation: a settle, a close
// begun and a close's answer each answer the read-only refusal as an
// error, never applied, and the rows are as they were.
func TestServerOpsOnAReadOnlyStore(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	sending := f.prepared(t, "p1", "")
	f.send(t, "s1", sending)
	pending, closing := f.sentWithClose(t, "p2"), f.sentWithClose(t, "p3")
	if _, applied, err := f.beginClose(closing.ID); !applied || err != nil {
		t.Fatalf("a close begun: %v %v", applied, err)
	}
	sending, closing = f.pub(t, sending.ID), f.pub(t, closing.ID)
	if _, err := f.s.db.Exec("UPDATE schema_version SET version = ?", Version+1); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenOptions(f.dir, Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	if readOnly, why := ro.ReadOnly(); !readOnly {
		t.Fatalf("not read-only: %s", why)
	}
	for _, c := range []struct {
		name string
		call func() (Receipt, bool, error)
	}{
		{"a settle", func() (Receipt, bool, error) {
			return ro.SettlePublication(ctx, sending.ID, sending.Attempt, found("7", "101"))
		}},
		{"a close begun", func() (Receipt, bool, error) { return ro.BeginClose(ctx, pending.ID) }},
		{"a close's answer", func() (Receipt, bool, error) {
			return ro.SettleClose(ctx, closing.ID, CloseOutcome{State: CloseClosed})
		}},
	} {
		if _, applied, err := c.call(); applied || !isRefusal(err, RefusalReadOnly) {
			t.Errorf("%s: %v %v", c.name, applied, err)
		}
	}
	for _, p := range []Publication{sending, pending, closing} {
		if q, err := ro.Publication(ctx, p.ID); err != nil || q.Rev != p.Rev || q.State != p.State || q.CloseState != p.CloseState {
			t.Errorf("the row moved: %+v %v", q, err)
		}
	}
}

// A server's operation tells one caller it made the change, under an actor
// of the store's own: two close begins that read one revision (held where
// Apply takes the lock) are one operation, one of them told applied,
// close_tries 1, its history the server's; an operation a caller hands the
// store with a close's derived id is refused under the store's actor, and
// done under the admin's, and either way the close begins; a server's
// operation the store refuses returns the refusal.
func TestServerOpAppliedOnce(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	p := f.sentWithClose(t, "p1")
	begin := func() (bool, error) { _, a, err := f.beginClose(p.ID); return a, err }
	applied, errs := heldAtOnce(t, f.s, begin, begin)
	if errs[0] != nil || errs[1] != nil || applied[0] == applied[1] {
		t.Errorf("two begins at rev %d: applied %v, %v", p.Rev, applied, errs)
	}
	if q := f.pub(t, p.ID); q.CloseState != CloseClosing || q.CloseTries != 1 {
		t.Errorf("after two begins: %+v", q)
	}
	if actors := texts(t, f.s, "SELECT DISTINCT actor FROM changes WHERE entity = ? AND op_id LIKE '~%'", EntityPublication); !slices.Equal(actors, []string{ActorServer}) {
		t.Errorf("the server's operations are kept under %v", actors)
	}
	for _, actor := range []string{ActorServer, ActorAdmin} {
		q := f.sentWithClose(t, "p2"+actor)
		digest, err := fingerprint(IntentPublicationClose, struct {
			ID    string `json:"id"`
			Begin bool   `json:"begin"`
		}{q.ID, true})
		if err != nil {
			t.Fatal(err)
		}
		rc, err := f.s.AddComment(ctx, Op{Actor: actor, ID: DerivedOpID(q.ID, "close", strconv.FormatInt(q.Rev, 10), digest), By: "Tester"}, f.key, "a comment")
		if actor == ActorServer && !errors.Is(err, ErrBadActor) {
			t.Errorf("a caller's operation under the store's actor: %+v %v", rc, err)
		} else if actor == ActorAdmin {
			want(t, rc, err, StatusDone, "")
		}
		if _, applied, err := f.beginClose(q.ID); !applied || err != nil {
			t.Errorf("a close begun after a caller's operation of its id under %s: %v %v", actor, applied, err)
		}
		if q = f.pub(t, q.ID); q.CloseState != CloseClosing || q.CloseTries != 1 {
			t.Errorf("the close: %+v", q)
		}
	}
	// The ticket deleted between the begin's read and its transaction.
	r := f.sentWithClose(t, "p3")
	f.s.mu.Lock()
	f.s.beforeLock = func() {
		f.s.mu.Lock()
		f.s.beforeLock = nil
		f.s.mu.Unlock()
		rc, err := f.s.DeleteTicket(ctx, testOp("gone"), f.key)
		want(t, rc, err, StatusDone, "")
	}
	f.s.mu.Unlock()
	if _, applied, err := f.beginClose(r.ID); applied || !isRefusal(err, RefusalNotFound) {
		t.Errorf("a begin the store refused: %v %v", applied, err)
	}
	// The refusal is kept by no receipt: the ticket back, the same begin at
	// the same revision is decided anew.
	rc, err := f.s.UndeleteTicket(ctx, testOp("back"), f.key)
	want(t, rc, err, StatusDone, "")
	if _, applied, err := f.beginClose(r.ID); !applied || err != nil || failedReceipts(t, f.s, IntentPublicationClose) != 0 {
		t.Errorf("the begin, the ticket back: %v %v", applied, err)
	}
}

// A finding for a send of this store's is its sender's: a comment by
// another account is refused, for a publication being sent and for an
// unconfirmed one, kept nowhere and the row unchanged.
func TestSettleNeedsTheSender(t *testing.T) {
	f := newPubFixture(t)
	for i, st := range []string{PublicationSending, PublicationUnconfirmed} {
		p := f.prepared(t, fmt.Sprint("p", i), "")
		f.send(t, fmt.Sprint("s", i), p)
		if st == PublicationUnconfirmed {
			if _, _, err := f.settle(f.pub(t, p.ID), Outcome{State: st}); err != nil {
				t.Fatal(err)
			}
		}
		p = f.pub(t, p.ID)
		if _, applied, err := f.settle(p, found("5", "202")); applied || !isRefusal(err, RefusalInvalid) {
			t.Errorf("%s found by another: %v %v", st, applied, err)
		}
		if q := f.pub(t, p.ID); q.Rev != p.Rev || q.State != st || q.RemoteID != "" {
			t.Errorf("%s: the row moved: %+v", st, q)
		}
	}
	if n := failedReceipts(t, f.s, IntentPublicationSettle); n != 0 {
		t.Errorf("%d refusals kept", n)
	}
}

// A login is letters, digits and hyphens, at most 39 bytes: anything else
// is kept as none, sent_as_id naming the account, and the send goes on;
// what was not kept is in no row, change row, receipt or feed of changes.
func TestPublicationKeepsALoginOrNone(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	for i, login := range []string{"bob:hunter2@x.example.com", "x.example.com?token=SECRET", "dependabot[bot]", strings.Repeat("a", MaxSnapshotLogin+1), "alice-2"} {
		p := f.prepared(t, fmt.Sprint("p", i), "")
		rc, owned, err := f.s.BeginSend(ctx, testOp(fmt.Sprint("s", i)), p.ID, p.PostSha256, Sender{ID: "101", Login: login})
		if err != nil || !owned {
			t.Fatalf("a send as %q: %+v %v", login, rc, err)
		}
		kept := ""
		if login == "alice-2" {
			kept = login
		}
		if p = f.pub(t, p.ID); p.SentAsLogin != kept || p.SentAsID != "101" {
			t.Errorf("the login %q kept as %q", login, p.SentAsLogin)
		}
	}
	feed, err := f.s.ChangesAfter(ctx, 0, MaxFeedPage)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := encodeValue(feed)
	for _, secret := range []string{"hunter2", "SECRET", "dependabot", strings.Repeat("a", MaxSnapshotLogin+1)} {
		if n := countOf(t, f.s, "SELECT COUNT(*) FROM publications WHERE sent_as_login LIKE ?", "%"+secret+"%") +
			countOf(t, f.s, "SELECT COUNT(*) FROM changes WHERE value LIKE ? OR prior LIKE ?", "%"+secret+"%", "%"+secret+"%") +
			countOf(t, f.s, "SELECT COUNT(*) FROM ops WHERE receipt LIKE ?", "%"+secret+"%"); n != 0 || strings.Contains(encoded, secret) {
			t.Errorf("%q is kept: %d rows", secret, n)
		}
	}
}

// routeCodes is every code the routes' sentences may carry: what
// trackerAnswer and the credential's lookup answer (internal/api).
var routeCodes = []string{"rate_limited", "moved", "external_not_found", "token_refused", "token_permission", "tracker_bound", "tracker_unavailable",
	"no_token", "invalid_host", "not_a_tracker_url", "credentials_unusable", "target_changed", "tracker_busy"}

// routeWords is every sentence the publication routes hand a settle as an
// outcome's words (issue 877's internal/api/dispatch_publish.go), with
// each code, and the store's own.
func routeWords() []string {
	out := []string{
		"the item is no longer in the repository it was prepared for, or no longer of its kind: prepare it again",
		"GitHub has not shown the comment yet; Check looks again",
		"GitHub's comments could not be listed whole; Check looks again",
		"closed on GitHub before Conductor's close",
		"GitHub refused the comment",
		"GitHub has not shown this comment; it may still appear",
	}
	for _, code := range routeCodes {
		out = append(out, "GitHub's comments could not be listed: "+code+"; Check looks again", "GitHub refused the close: "+code, "GitHub refused the comment: "+code)
	}
	return out
}

// addressWordsTable is words that hold an address, in these spellings: a
// scheme, none, two slashes, a user and a password, a query after a host
// and after a path, a fragment, an IPv6 host, a host of one label, a
// zero-width character (zeroWidth), a line break inside, an escaped query;
// and three whose one character past the plain set is a slash (a scheme, a
// host and a path; a host and a path) or a question mark (a host and a
// bare query).
var addressWordsTable = []string{
	"GitHub said https://x.example.com/p",
	"x.example.com/o/r",
	"x.example.com?SECRET",
	"GitHub said https://bob:hunter2@x.example.com/p?q=SECRET#frag",
	"GitHub said x.example.com/p?q=SECRET",
	"GitHub said //bob:hunter2@x.example.com/p?q=SECRET",
	"bob:hunter2@x.example.com",
	"see x.example.com?token=SECRET",
	"(x.example.com?token=SECRET)",
	"x.example.com#SECRET",
	"http://[2001:db8::1]/p?q=SECRET",
	"[::1]:8080/p?q=SECRET",
	"intranet?q=SECRET",
	"localhost:8080/p?q=SECRET",
	"x.example" + zeroWidth + ".com?q=SECRET",
	"x.example.com" + zeroWidth,
	"GitHub said x.example.com\n?q=SECRET",
	"GitHub said\nbob:hunter2@x.example.com",
	"x.example.com%3Fq%3DSECRET",
	"mailto:bob@x.example.com",
	"ssh://git@x.example.com:22/o/r.git",
}

// zeroWidth is a zero-width space.
var zeroWidth = string(rune(0x200B))

// generatedWords is n texts from a fixed seed, of plain characters and
// others an address is spelled with, from empty to past MaxReadError.
func generatedWords(n int) []string {
	r := rand.New(rand.NewPCG(769, 2))
	alphabet := append([]rune("abcXYZ019 ',;:._-()/?#@&=%[]~!\"<>\t\n\r"), 0x200B, 0xE9, 0x2028, '+', '*')
	out := make([]string, n)
	for i := range out {
		rs := make([]rune, r.IntN(MaxReadError+12))
		for j := range rs {
			if r.IntN(4) == 0 {
				rs[j] = alphabet[r.IntN(len(alphabet))]
			} else {
				rs[j] = alphabet[r.IntN(18)]
			}
		}
		out[i] = string(rs)
	}
	return out
}

// plainOnly reports whether every character of s is one an outcome's kept
// words may hold.
func plainOnly(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(" ',;:._-()", r))
	}) < 0
}

// An outcome's words are kept whole when they are plain, at most
// MaxReadError characters of letters, digits, spaces and ' , ; : . _ - ( ),
// and otherwise not at all: the store's own words for that outcome take
// their place. Every sentence the routes hand a settle is kept whole;
// words holding an address in any spelling, and a few hundred texts from
// a fixed seed, are kept unchanged or not at all, never a third thing;
// and through SettlePublication and SettleClose the rows and the change
// rows of error and close_error hold no other character.
func TestPublicationWordsArePlain(t *testing.T) {
	const own = "the store's own words"
	for _, w := range routeWords() {
		if got := pubWords(w, own); got != w {
			t.Errorf("a route's words %q kept as %q", w, got)
		}
	}
	for _, w := range addressWordsTable {
		if got := pubWords(w, own); got != own {
			t.Errorf("%q kept as %q", w, got)
		}
	}
	generated := generatedWords(400)
	kept := 0
	for _, w := range generated {
		got, trimmed := pubWords(w, own), strings.TrimSpace(w)
		switch {
		case got != trimmed && got != own:
			t.Errorf("%q kept as %q", w, got)
		case trimmed != "" && len(trimmed) <= MaxReadError && plainOnly(trimmed) && got != trimmed:
			t.Errorf("plain words %q not kept", w)
		case got == trimmed && trimmed != own:
			kept++
		}
	}
	if kept == 0 || kept == len(generated) {
		t.Errorf("%d of %d generated texts kept: the seed tries one side alone", kept, len(generated))
	}
	f := newPubFixture(t)
	words := slices.Concat(routeWords(), addressWordsTable, generated)
	// Words alone on a send, as a listing's, then each failed and
	// unconfirmed in its turn.
	p := f.prepared(t, "p1", "")
	f.send(t, "s1", p)
	for _, w := range words {
		if _, _, err := f.settle(f.pub(t, p.ID), Outcome{Words: w}); err != nil {
			t.Fatalf("%q: %v", w, err)
		}
	}
	for i, w := range addressWordsTable {
		q := f.prepared(t, fmt.Sprint("q", i), "")
		f.send(t, fmt.Sprint("qs", i), q)
		o := Outcome{State: PublicationFailed, Words: w}
		if i%2 == 1 {
			o.State = PublicationUnconfirmed
		}
		if _, applied, err := f.settle(f.pub(t, q.ID), o); !applied || err != nil {
			t.Fatalf("%q: %v %v", w, applied, err)
		}
	}
	// A close's answers, each refused but the last of a publication's tries.
	closes := slices.Concat(addressWordsTable, generated[:20], routeWords()[:5])
	for i := 0; i < len(closes); i += MaxPublicationCloseTries {
		c := f.sentWithClose(t, fmt.Sprint("c", i))
		for j := i; j < min(i+MaxPublicationCloseTries, len(closes)); j++ {
			if _, applied, err := f.beginClose(c.ID); !applied || err != nil {
				t.Fatalf("a close begun: %v %v", applied, err)
			}
			if _, applied, err := f.settleClose(c.ID, CloseOutcome{State: CloseRefused, Words: closes[j]}); !applied || err != nil {
				t.Fatalf("%q: %v %v", closes[j], applied, err)
			}
		}
	}
	held := texts(t, f.s, "SELECT error FROM publications UNION ALL SELECT close_error FROM publications")
	for _, v := range texts(t, f.s, "SELECT value FROM changes WHERE entity = ? AND field IN ('error', 'close_error') UNION ALL "+
		"SELECT prior FROM changes WHERE entity = ? AND field IN ('error', 'close_error')", EntityPublication, EntityPublication) {
		var s string
		if err := json.Unmarshal([]byte(v), &s); err != nil {
			s = v
		}
		held = append(held, s)
	}
	if len(held) < len(routeWords()) {
		t.Fatalf("%d words held", len(held))
	}
	for _, s := range held {
		if !plainOnly(s) || len(s) > MaxReadError {
			t.Errorf("%q is held", s)
		}
	}
	if n := countOf(t, f.s, "SELECT COUNT(*) FROM changes WHERE entity = ? AND field = 'error' AND value LIKE '%could not be listed: tracker_bound%'", EntityPublication); n != 1 {
		t.Errorf("a route's words in %d change rows", n)
	}
}

// A sent comment's address keeps the comment's anchor (#issuecomment- and
// digits), and no other fragment, no query and no user.
func TestSentCommentKeepsItsAnchor(t *testing.T) {
	f := newPubFixture(t)
	for i, c := range []struct{ url, kept string }{
		{"https://github.com/rocksolidlabs/conductor/issues/612#issuecomment-2153", "https://github.com/rocksolidlabs/conductor/issues/612#issuecomment-2153"},
		{"https://github.com/rocksolidlabs/conductor/issues/612#top", "https://github.com/rocksolidlabs/conductor/issues/612"},
		{"https://bob:hunter2@github.com/rocksolidlabs/conductor/issues/612?token=SECRET#issuecomment-7", "https://github.com/rocksolidlabs/conductor/issues/612#issuecomment-7"},
		{"https://github.com/rocksolidlabs/conductor/issues/612#issuecomment-7x", "https://github.com/rocksolidlabs/conductor/issues/612"},
	} {
		p := f.prepared(t, fmt.Sprint("p", i), "")
		f.send(t, fmt.Sprint("s", i), p)
		o := found("2153", "101")
		o.Comment.URL = c.url
		if _, applied, err := f.settle(f.pub(t, p.ID), o); !applied || err != nil {
			t.Fatalf("%s: %v %v", c.url, applied, err)
		}
		if got := f.pub(t, p.ID).RemoteURL; got != c.kept {
			t.Errorf("%s kept as %s", c.url, got)
		}
	}
}

// The lists read no body: the statements Publications and a ticket's read
// make of the publications name no body column; a publication's own read
// does.
func TestPublicationListsReadNoBody(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	p := f.prepared(t, "p1", "")
	var mu sync.Mutex
	var seen []string
	f.s.readQueryer = func(q queryer) queryer { return recordingQueryer{q, &mu, &seen} }
	t.Cleanup(func() { f.s.readQueryer = nil })
	read := func(do func() error) []string {
		mu.Lock()
		seen = nil
		mu.Unlock()
		if err := do(); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return slices.DeleteFunc(slices.Clone(seen), func(s string) bool { return !strings.Contains(s, "FROM publications p") })
	}
	for name, do := range map[string]func() error{
		"Publications": func() error { _, _, err := f.s.Publications(ctx, f.key, "", false); return err },
		"Ticket":       func() error { _, err := f.s.Ticket(ctx, f.key); return err },
	} {
		stmts := read(do)
		if len(stmts) == 0 || slices.ContainsFunc(stmts, func(s string) bool { return strings.Contains(s, "body") }) {
			t.Errorf("%s reads %q", name, stmts)
		}
	}
	if stmts := read(func() error { _, err := f.s.Publication(ctx, p.ID); return err }); !slices.ContainsFunc(stmts, func(s string) bool { return strings.Contains(s, "p.body") }) {
		t.Errorf("a publication's read: %q", stmts)
	}
}

// recordingQueryer records each statement a read makes.
type recordingQueryer struct {
	queryer
	mu   *sync.Mutex
	seen *[]string
}

func (q recordingQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.mu.Lock()
	*q.seen = append(*q.seen, query)
	q.mu.Unlock()
	return q.queryer.QueryContext(ctx, query, args...)
}

func (q recordingQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.mu.Lock()
	*q.seen = append(*q.seen, query)
	q.mu.Unlock()
	return q.queryer.QueryRowContext(ctx, query, args...)
}
