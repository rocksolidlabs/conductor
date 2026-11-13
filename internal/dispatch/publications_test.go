package dispatch

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
	return f.s.SettlePublication(context.Background(), ActorAdmin, p.ID, p.Attempt, o)
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
	if p = f.pub(t, p.ID); p.State != PublicationSent || p.RemoteID != "2153" || p.CloseState != "" || p.SentAt.IsZero() {
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
		rc, err := f.s.DiscardPublication(ctx, testOp(fmt.Sprint("qd", i)), q.ID)
		if to == PublicationSending {
			want(t, rc, err, StatusFailed, RefusalInvalid)
			continue
		}
		want(t, rc, err, StatusDone, "")
		if q = f.pub(t, q.ID); q.State != PublicationDiscarded {
			t.Fatalf("%s discarded: %+v", to, q)
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
	ids := texts(t, f.s, "SELECT DISTINCT op_id FROM changes WHERE entity = ? AND actor = ? ORDER BY seq", EntityPublication, ActorAdmin)
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("the settles' ids %v", ids)
	}
	if p = f.pub(t, p.ID); p.RemoteID != "2" || failedReceipts(t, f.s, IntentPublicationSettle) != 0 {
		t.Fatalf("the row %+v", p)
	}
}

// A settle that changes nothing is not applied: a listing's words, then
// the same words again, leave one operation and no receipt for the second;
// a finding at the same revision is applied; two different outcomes at one
// revision: one applied, the other stale_revision, never op_conflict.
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
	// Two settles that read one revision: the first applies, the second's
	// is a different id that the moved revision refuses.
	at := f.pub(t, p.ID)
	if _, applied, err := f.settle(at, found("7", "101")); !applied || err != nil {
		t.Fatalf("the finding: %v %v", applied, err)
	}
	other := Outcome{State: PublicationUnconfirmed}
	digest, _ := fingerprint(IntentPublicationSettle, struct {
		ID      string  `json:"id"`
		Attempt int     `json:"attempt"`
		Outcome Outcome `json:"outcome"`
	}{at.ID, at.Attempt, other})
	op := Op{Actor: ActorAdmin, ID: DerivedOpID(at.ID, "settle", strconv.FormatInt(at.Rev, 10), digest), ExpectedRev: at.Rev}
	rc, err := f.s.Apply(context.Background(), op, IntentPublicationSettle, digest, func(tx *tx) error {
		return tx.changePublication(at.ID, func(p Publication, instance string) (Publication, error) {
			return p.settled(at.Attempt, other, instance, tx.now)
		})
	})
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
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
	ctx := context.Background()
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
			_, applied, err = f.s.BeginClose(ctx, ActorAdmin, p.ID)
		} else {
			_, applied, err = f.s.SettleClose(ctx, ActorAdmin, p.ID, st.o)
		}
		if p = f.pub(t, p.ID); !applied || err != nil || p.CloseState != st.state || p.CloseTries != st.tries {
			t.Fatalf("step %d: %v %v %+v", i, applied, err, p)
		}
	}
	if _, _, err := f.s.BeginClose(ctx, ActorAdmin, p.ID); !isRefusal(err, RefusalInvalid) || p.NeedsYou() {
		t.Fatalf("a closed one begun again: %v", err)
	}
	q := f.prepared(t, "p2", "")
	f.send(t, "s2", q)
	if _, _, err := f.settle(f.pub(t, q.ID), found("10", "101")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.BeginClose(ctx, ActorAdmin, q.ID); !isRefusal(err, RefusalInvalid) {
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
	ctx := context.Background()
	p := f.prepared(t, "p1", "completed")
	f.send(t, "s1", p)
	if _, _, err := f.settle(f.pub(t, p.ID), found("9", "101")); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= MaxPublicationCloseTries; i++ {
		if _, applied, err := f.s.BeginClose(ctx, ActorAdmin, p.ID); !applied || err != nil {
			t.Fatalf("begin %d: %v %v", i, applied, err)
		}
		if p = f.pub(t, p.ID); p.CloseState != CloseClosing || p.CloseTries != i {
			t.Fatalf("begin %d: %+v", i, p)
		}
	}
	if _, _, err := f.s.BeginClose(ctx, ActorAdmin, p.ID); !isRefusal(err, RefusalBound) {
		t.Fatalf("the sixth: %v", err)
	}
}

// No operation the server makes for itself changes nothing: each settle
// and close outcome, from every state a publication can be in, twice with
// the same input, keeps no refused receipt, and the second is applied
// only when the first was not; the next different outcome applies.
func TestServerOpsNeverNoop(t *testing.T) {
	f := newPubFixture(t)
	ctx := context.Background()
	// One publication in each state.
	states := map[string]Publication{}
	for _, st := range []string{PublicationDraft, PublicationSending, PublicationFailed, PublicationUnconfirmed, PublicationSent, PublicationDiscarded, "closing"} {
		p := f.prepared(t, "p-"+st, "completed")
		if st != PublicationDraft && st != PublicationDiscarded {
			f.send(t, "s-"+st, p)
		}
		p = f.pub(t, p.ID)
		switch st {
		case PublicationFailed, PublicationUnconfirmed:
			f.settle(p, Outcome{State: st})
		case PublicationSent, "closing":
			f.settle(p, found("1"+strconv.Itoa(len(states)), "101"))
			if st == "closing" {
				f.s.BeginClose(ctx, ActorAdmin, p.ID)
			}
		case PublicationDiscarded:
			f.s.DiscardPublication(ctx, testOp("d-"+st), p.ID)
		}
		states[st] = f.pub(t, p.ID)
	}
	type serverOp struct {
		name string
		run  func(p Publication) (bool, error)
	}
	settle := func(o Outcome) func(Publication) (bool, error) {
		return func(p Publication) (bool, error) {
			_, applied, err := f.settle(f.pub(t, p.ID), o)
			return applied, err
		}
	}
	ops := []serverOp{
		{"words", settle(Outcome{Words: "cut"})}, {"failed", settle(Outcome{State: PublicationFailed, Words: "no"})},
		{"unconfirmed", settle(Outcome{State: PublicationUnconfirmed})}, {"found", settle(found("99", "101"))},
		{"close begun", func(p Publication) (bool, error) { _, a, err := f.s.BeginClose(ctx, ActorAdmin, p.ID); return a, err }},
		{"closed", func(p Publication) (bool, error) {
			_, a, err := f.s.SettleClose(ctx, ActorAdmin, p.ID, CloseOutcome{State: CloseClosed})
			return a, err
		}},
	}
	for st, p := range states {
		for _, op := range ops {
			first, _ := op.run(p)
			second, _ := op.run(p)
			// A begin always changes a field; anything else applied twice
			// would have changed nothing the second time.
			if second && op.name != "close begun" {
				t.Errorf("%s from %s: applied twice (first %v)", op.name, st, first)
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
	f.s.BeginClose(ctx, ActorAdmin, p.ID)
	f.s.SettleClose(ctx, ActorAdmin, p.ID, CloseOutcome{State: CloseClosed})
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
