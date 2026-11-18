package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A background brief that holds a decision notifies (brakeman_notify.go):
// the pages by a brakeman_needs_you event after the brakeman event that
// says briefed, and the webhooks that list brakeman_needs_you, in fixed
// words. "None" is proved by order, never by a clock: what a kept brief
// queues is queued before its submission is answered, so a person's
// question asked after the answer is the receiver's next delivery, and on
// the stream a mark of the test's (brakeman_opened) arrives with no
// brakeman_needs_you before it.

// needsYouFixture is a server with Brakeman in the background (no
// cooldown to wait out between turns), one webhook listing events, the
// event stream read from the start, and tickets a decision card is about.
type needsYouFixture struct {
	*bkEnv
	rcv     *receiver
	c       *clock
	events  <-chan string
	tickets []string
}

// needsYouMarks are words each brief of these tests holds, in its title,
// reading, decision's question and every field a person reads: none may
// reach a notification.
var needsYouMarks = []string{"MARKTITLE", "MARKREADING", "MARKQUESTION", "MARKHAPPENED", "MARKWHY", "MARKAUTH", "MARKOPTION", "CND-"}

func newNeedsYouFixture(t *testing.T, events []string, opts ...Option) *needsYouFixture {
	t.Helper()
	rcv := newReceiver(t, nil)
	e := newTestEnvAgents(t, func(c *config.Config) {
		c.Webhooks = []config.Webhook{{URL: rcv.URL + "/hook", Events: events, AllowPrivate: true}}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), bkAgents(), opts...)
	f := &needsYouFixture{bkEnv: bkEnvOf(t, e), rcv: rcv}
	ctx := t.Context()
	done := doneOf(t)
	done(f.srv.dispatch.CreateProject(ctx, adminOp("Ann"), dispatch.NewProject{Key: "CND", Name: "Conductor"}))
	for _, title := range []string{"The build fails", "The deploy waits"} {
		f.tickets = append(f.tickets, done(f.srv.dispatch.CreateTicket(ctx, adminOp("Ann"), dispatch.NewTicket{Project: "CND", Title: title})).Key)
	}
	f.events = f.sse(t)
	f.c = f.background(t, "bk", func(c map[string]any) { c["wake"].(map[string]any)["cooldownMinutes"] = 0 })
	return f
}

// wakeTurn wakes the role by a failure of task and answers the wake
// request and its turn's session and token.
func (f *needsYouFixture) wakeTurn(t *testing.T, task string) (id, sid, tok string) {
	t.Helper()
	id = f.failTask(t, f.c, task)
	sid, tok, _ = f.turnSession(t, id)
	return id, sid, tok
}

// endTurn moves the clock past the kept brief's grace: the turn's run
// ends and the slot is given up, and the role is idle again.
func (f *needsYouFixture) endTurn(t *testing.T) {
	t.Helper()
	f.c.add(brakemanGrace + time.Second)
	f.settle(t)
	f.slotFree(t)
	f.waitRole(t, crew.RoleIdle)
}

// decisionBrief is a brief of a decision card about each ticket of
// tickets, each read first as the turn of request id (a card names the
// revision it read), every word a person reads one of needsYouMarks, a
// fact first.
func (f *needsYouFixture) decisionBrief(t *testing.T, id, sid, tok string, tickets ...string) map[string]any {
	t.Helper()
	turn := &obsFixture{bkEnv: f.bkEnv, req: id, sid: sid, tok: tok}
	items := []any{readFact("ticket:" + tickets[0])}
	for _, k := range tickets {
		turn.expectRead(t, "/api/dispatch/tickets/"+k, http.StatusOK, "")
		seen, ok := turn.readOf(t, "ticket:"+k)
		if !ok || seen.Rev == "" {
			t.Fatalf("the turn's read of %s: %+v", k, seen)
		}
		items = append(items, map[string]any{
			"kind": brakeman.ItemDecision, "about": "ticket:" + k, "happened": "MARKHAPPENED", "whyHuman": "MARKWHY",
			"question": "MARKQUESTION?", "alternatives": []any{map[string]any{"label": "MARKOPTION 1", "tradeoffs": "MARKOPTION a"}, map[string]any{"label": "MARKOPTION 2", "tradeoffs": "MARKOPTION b"}},
			"recommendation": 0, "authorizes": "MARKAUTH", "evidence": []string{"ticket:" + k}, "subjectRevision": seen.Rev,
		})
	}
	return map[string]any{"schema": brakeman.BriefSchema, "title": "MARKTITLE", "summary": "MARKREADING", "items": items}
}

// keep submits brief as the turn and wants it kept (201).
func (f *needsYouFixture) keep(t *testing.T, sid, tok, opID string, brief map[string]any) {
	t.Helper()
	if status, out := f.submit(t, sid, tok, opID, brief); status != http.StatusCreated {
		t.Fatalf("brief: %d %v", status, out)
	}
}

// loud has a person's session ask a question; the webhook lists
// needs_input, so its delivery is queued behind whatever came before.
func (f *needsYouFixture) loud(t *testing.T) {
	t.Helper()
	other := f.createSession("cat")
	if status, data, _ := f.callAs(t, f.agentToken(other), "POST", "/api/sessions/"+other+"/attention", map[string]any{"state": "needs_input", "message": "loud"}); status >= 300 {
		t.Fatalf("ask: %d %s", status, data)
	}
}

// nextIsLoud wants the receiver's next delivery to be the person's
// question: nothing the brief queued came before it.
func (f *needsYouFixture) nextIsLoud(t *testing.T) {
	t.Helper()
	f.loud(t)
	if d := f.rcv.next(t); d.message(t) != "loud" {
		t.Fatalf("a delivery before the person's question: %s %s", d.header.Get("X-Conductor-Event"), d.body)
	}
}

// needsYouUntilMark sends a mark of the test's (a brief opened, whose
// brakeman_opened event follows every event sent before it) and answers
// the brakeman_needs_you events the stream carried before the mark.
func (f *needsYouFixture) needsYouUntilMark(t *testing.T, opened string) []string {
	t.Helper()
	f.expect(t, "POST", "/api/brakeman/opened", map[string]any{"ids": []string{opened}}, http.StatusOK, "")
	return needsYouBefore(t, f.events)
}

// needsYouBefore reads events up to the next brakeman_opened, the test's
// mark, and answers the brakeman_needs_you events before it. The bound
// only a hang reaches.
func needsYouBefore(t *testing.T, events <-chan string) []string {
	t.Helper()
	var got []string
	deadline := time.After(testWait)
	for {
		select {
		case ev := <-events:
			if strings.HasPrefix(ev, "brakeman_opened ") {
				return got
			}
			if strings.HasPrefix(ev, "brakeman_needs_you ") {
				got = append(got, ev)
			}
		case <-deadline:
			t.Fatal("the mark never arrived")
		}
	}
}

// needsYouEvent is the stream's line of a brakeman_needs_you event.
func needsYouEvent(id string, n int) string {
	return fmt.Sprintf(`brakeman_needs_you {"id":%q,"decisions":%d}`, id, n)
}

// TestABackgroundBriefWithADecisionNotifiesOnce: a wake turn's brief with
// a decision card sends, after the brakeman event that says briefed, one
// brakeman_needs_you event of its id and count, and one delivery to the
// webhook that lists the name: its header, the fixed words, the path, an
// empty session, and no word of the brief. The brief submitted again
// (a lost reply), the second briefed event as the slot is given up, and a
// later read notify no one again.
func TestABackgroundBriefWithADecisionNotifiesOnce(t *testing.T) {
	t.Parallel()
	f := newNeedsYouFixture(t, []string{config.EventBrakemanNeedsYou, "needs_input"})
	id, sid, tok := f.wakeTurn(t, "build")
	brief := f.decisionBrief(t, id, sid, tok, f.tickets[0])
	f.keep(t, sid, tok, "b-1", brief)
	briefed := fmt.Sprintf(`brakeman {"id":%q,"state":"briefed"}`, id)
	want := needsYouEvent(id, 1)
	sawBriefed := false
	deadline := time.After(testWait)
read:
	for {
		select {
		case ev := <-f.events:
			if ev == briefed {
				sawBriefed = true
			}
			if strings.HasPrefix(ev, "brakeman_needs_you ") {
				if ev != want || !sawBriefed {
					t.Fatalf("%s (briefed before it: %v)", ev, sawBriefed)
				}
				break read
			}
		case <-deadline:
			t.Fatal("no brakeman_needs_you event")
		}
	}
	kept := f.waitState(t, id, brakeman.StateBriefed)
	d := f.rcv.next(t)
	if ev := d.header.Get("X-Conductor-Event"); ev != config.EventBrakemanNeedsYou {
		t.Fatalf("X-Conductor-Event %q", ev)
	}
	var body struct {
		SessionID string            `json:"sessionId"`
		Session   map[string]string `json:"session"`
		Entry     map[string]any    `json:"entry"`
	}
	if err := json.Unmarshal(d.body, &body); err != nil {
		t.Fatal(err)
	}
	at, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(body.Entry["at"]))
	wantEntry := map[string]any{"at": body.Entry["at"], "type": "brakeman_needs_you", "message": "Brakeman's background brief " + id + " holds 1 decision", "url": "/brakeman?request=" + id}
	if body.SessionID != "" || fmt.Sprint(body.Session) != "map[agentId: id: name:]" || fmt.Sprint(body.Entry) != fmt.Sprint(wantEntry) || !at.Equal(kept.BriefAt) {
		t.Fatalf("the delivery %s", d.body)
	}
	for _, m := range needsYouMarks {
		if strings.Contains(string(d.body), m) {
			t.Fatalf("the delivery holds %q: %s", m, d.body)
		}
	}
	// Submitted again: answered as kept, nothing sent.
	if status, out := f.submit(t, sid, tok, "b-1", brief); status != http.StatusOK {
		t.Fatalf("again: %d %v", status, out)
	}
	f.endTurn(t)
	f.nextIsLoud(t)
	f.expect(t, "GET", "/api/brakeman/requests/"+id, nil, http.StatusOK, "")
	if got := f.needsYouUntilMark(t, id); len(got) != 0 {
		t.Fatalf("notified again: %v", got)
	}
}

// TestABackgroundBriefWithoutADecisionStaysSilent: a wake turn's brief of
// facts alone tells neither the webhook nor the pages.
func TestABackgroundBriefWithoutADecisionStaysSilent(t *testing.T) {
	t.Parallel()
	f := newNeedsYouFixture(t, []string{config.EventBrakemanNeedsYou, "needs_input"})
	id, sid, tok := f.wakeTurn(t, "build")
	f.keep(t, sid, tok, "b-1", factBrief("ticket:"+f.tickets[0]))
	f.nextIsLoud(t)
	if got := f.needsYouUntilMark(t, id); len(got) != 0 {
		t.Fatalf("a brief without a decision notified: %v", got)
	}
}

// TestAPersonsBriefWithADecisionStaysSilent: a brief a person asked for
// that holds a decision card tells neither the webhook nor the pages.
func TestAPersonsBriefWithADecisionStaysSilent(t *testing.T) {
	t.Parallel()
	f := newNeedsYouFixture(t, []string{config.EventBrakemanNeedsYou, "needs_input"})
	id := f.briefing(t)
	sid, tok, _ := f.turnSession(t, id)
	f.keep(t, sid, tok, "b-1", f.decisionBrief(t, id, sid, tok, f.tickets[0]))
	f.nextIsLoud(t)
	if got := f.needsYouUntilMark(t, id); len(got) != 0 {
		t.Fatalf("a person's brief notified: %v", got)
	}
}

// TestARepeatedDecisionNotifiesNoOneAgain: a later background brief that
// repeats a decision an earlier one held tells no one; one that holds a
// decision no earlier one held tells the pages and the webhook again, its
// count the brief's whole count.
func TestARepeatedDecisionNotifiesNoOneAgain(t *testing.T) {
	t.Parallel()
	f := newNeedsYouFixture(t, []string{config.EventBrakemanNeedsYou, "needs_input"})
	first, sid, tok := f.wakeTurn(t, "build")
	f.keep(t, sid, tok, "b-1", f.decisionBrief(t, first, sid, tok, f.tickets[0]))
	if d := f.rcv.next(t); d.message(t) != "Brakeman's background brief "+first+" holds 1 decision" {
		t.Fatalf("%s", d.body)
	}
	f.endTurn(t)
	again, sid, tok := f.wakeTurn(t, "lint")
	f.keep(t, sid, tok, "b-2", f.decisionBrief(t, again, sid, tok, f.tickets[0]))
	f.nextIsLoud(t)
	if got := f.needsYouUntilMark(t, again); len(got) != 1 || got[0] != needsYouEvent(first, 1) {
		t.Fatalf("the repeat: %v", got)
	}
	f.endTurn(t)
	third, sid, tok := f.wakeTurn(t, "test")
	f.keep(t, sid, tok, "b-3", f.decisionBrief(t, third, sid, tok, f.tickets[0], f.tickets[1]))
	if d := f.rcv.next(t); d.message(t) != "Brakeman's background brief "+third+" holds 2 decisions" {
		t.Fatalf("%s", d.body)
	}
	if got := f.needsYouUntilMark(t, third); len(got) != 1 || got[0] != needsYouEvent(third, 2) {
		t.Fatalf("the new decision: %v", got)
	}
}

// TestADecisionIsHeldAgainstTheRolesEarlierBriefs: the rule over the
// store's records. A card is the decision it is about at the revision it
// was written at: the same subject at another revision is new; a
// person's brief and a background brief past the twenty before it hold
// nothing a new one is held against; a brief that is not the role's, or
// not briefed, never notifies.
func TestADecisionIsHeldAgainstTheRolesEarlierBriefs(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	events := b.sse(t)
	rec := 0
	card := func(about, rev string) brakeman.Item {
		return brakeman.Item{Kind: brakeman.ItemDecision, Decision: &brakeman.Decision{About: listRef(t, about), Happened: "h", WhyHuman: "w", Question: "q?",
			Alternatives: []brakeman.Alternative{{Label: "a", Tradeoffs: "x"}, {Label: "b", Tradeoffs: "y"}}, Recommendation: &rec, Authorizes: "z",
			Evidence: []brakeman.Ref{listRef(t, about)}, SubjectRevision: rev}}
	}
	wake := func(items ...brakeman.Item) brakeman.Request {
		id := b.listed(t, brakeman.Request{Kind: brakeman.KindWake, Batch: fmt.Sprintf("wb-%d", bkOps.Add(1))}, "t", "s", append([]brakeman.Item{listFact(t, "f", "ticket:CND-1")}, items...), "", "")
		r, err := b.desk.st.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	told := func(r brakeman.Request, items ...brakeman.Item) bool {
		t.Helper()
		b.desk.briefKept(r, brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s", Items: items})
		b.srv.events.brakemanOpened()
		got := needsYouBefore(t, events)
		if len(got) > 1 || len(got) == 1 && got[0] != needsYouEvent(r.ID, len(items)) {
			t.Fatalf("%s: %v", r.ID, got)
		}
		return len(got) == 1
	}
	a3, a4, other := card("ticket:CND-1", "3"), card("ticket:CND-1", "4"), card("ticket:CND-2", "")
	first := wake(a3)
	if !told(first, a3) {
		t.Fatal("a first decision told no one")
	}
	if r := wake(a3); told(r, a3) {
		t.Fatal("the same decision told again")
	}
	if r := wake(a4); !told(r, a4) {
		t.Fatal("the subject at a new revision told no one")
	}
	// A person's brief holds nothing a background brief is held against,
	// and never notifies itself.
	ask := b.listed(t, brakeman.Request{Kind: brakeman.KindBriefing}, "t", "s", []brakeman.Item{listFact(t, "f", "ticket:CND-2"), other}, "", "")
	if r, _ := b.desk.st.Get(ask); told(r, other) {
		t.Fatal("a person's brief notified")
	}
	if r := wake(other); !told(r, other) {
		t.Fatal("a decision only a person's brief held told no one")
	}
	// A wake request not briefed notifies no one.
	if r := wake(); told(brakeman.Request{ID: r.ID, Kind: r.Kind, State: brakeman.StateRunning}, card("ticket:CND-3", "")) {
		t.Fatal("a request not briefed notified")
	}
	// Twenty background briefs after it, the first's decision is told again.
	for range needsYouEarlier {
		wake()
	}
	if r := wake(a3); !told(r, a3) {
		t.Fatal("a decision past the twenty earlier briefs told no one")
	}
}

// TestEarlierBriefsThatCannotBeRead: the two ways the earlier briefs fail
// to read, each as the code takes it. A list of the earlier requests that
// cannot be read notifies no one. An earlier brief that does not read is
// passed over as holding no decision, so a decision only it held is told
// again: the rule errs toward telling a person twice, never toward
// silence about a decision no readable brief held.
func TestEarlierBriefsThatCannotBeRead(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	b := brakemanEnv(t, nil)
	events := b.sse(t)
	rec := 0
	card := brakeman.Item{Kind: brakeman.ItemDecision, Decision: &brakeman.Decision{About: listRef(t, "ticket:CND-1"), Happened: "h", WhyHuman: "w", Question: "q?",
		Alternatives: []brakeman.Alternative{{Label: "a", Tradeoffs: "x"}, {Label: "b", Tradeoffs: "y"}}, Recommendation: &rec, Authorizes: "z",
		Evidence: []brakeman.Ref{listRef(t, "ticket:CND-1")}, SubjectRevision: "3"}}
	wake := func() brakeman.Request {
		id := b.listed(t, brakeman.Request{Kind: brakeman.KindWake, Batch: fmt.Sprintf("wb-%d", bkOps.Add(1))}, "t", "s", []brakeman.Item{listFact(t, "f", "ticket:CND-1"), card}, "", "")
		r, err := b.desk.st.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	told := func(r brakeman.Request) []string {
		t.Helper()
		b.desk.briefKept(r, brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s", Items: []brakeman.Item{card}})
		b.srv.events.brakemanOpened()
		return needsYouBefore(t, events)
	}
	reqs := filepath.Join(b.srv.store.Dir(), brakeman.Dir, brakeman.RequestsDir)
	earlier, repeat := wake(), wake()
	// The control: a repeat of the decision a readable brief held is silent.
	if got := told(repeat); len(got) != 0 {
		t.Fatalf("a repeat: %v", got)
	}
	// The list cannot be read (the folder's entries refused; its files
	// still open by name): no one is notified, though the card is new.
	fresh := wake()
	if err := os.Chmod(reqs, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(reqs, 0o700) })
	if _, _, err := b.desk.st.List(fresh.ID, brakeman.MaxPage); err == nil {
		t.Fatal("the list still reads")
	}
	if got := told(fresh); len(got) != 0 {
		t.Fatalf("an unreadable list notified: %v", got)
	}
	if err := os.Chmod(reqs, 0o700); err != nil {
		t.Fatal(err)
	}
	// Every earlier brief that held the decision does not read: each is
	// passed over, and the decision is told again.
	for _, r := range []brakeman.Request{earlier, repeat, fresh} {
		if err := os.WriteFile(filepath.Join(reqs, r.ID+"-brief.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := b.desk.st.BriefAlone(r.ID); err == nil {
			t.Fatalf("%s still reads", r.ID)
		}
	}
	again := wake()
	if got := told(again); len(got) != 1 || got[0] != needsYouEvent(again.ID, 1) {
		t.Fatalf("a decision only unreadable briefs held: %v", got)
	}
}

// TestAWebhookThatListsOnlyQuestionsHearsNoBrief: a webhook of today,
// listing needs_input, attention and decision, receives nothing new for a
// background brief with a decision, though the pages are told.
func TestAWebhookThatListsOnlyQuestionsHearsNoBrief(t *testing.T) {
	t.Parallel()
	f := newNeedsYouFixture(t, []string{"needs_input", "attention", config.EventDecision})
	id, sid, tok := f.wakeTurn(t, "build")
	f.keep(t, sid, tok, "b-1", f.decisionBrief(t, id, sid, tok, f.tickets[0]))
	f.nextIsLoud(t)
	if got := f.needsYouUntilMark(t, id); len(got) != 1 {
		t.Fatalf("the pages: %v", got)
	}
}

// TestAProvidersSinkHearsNoBrief: a provider's sink whose Events is nil,
// which takes every session entry, is handed nothing for a background
// brief with a decision: the person's question after it is all it gets.
func TestAProvidersSinkHearsNoBrief(t *testing.T) {
	t.Parallel()
	sink := &recSink{name: "everything"}
	p := ext.Providers{Notifications: []ext.NotificationSink{sink}, All: []ext.Entry{{Kind: ext.KindNotifications, Name: sink.Name(), Value: sink}}}
	f := newNeedsYouFixture(t, []string{config.EventBrakemanNeedsYou, "needs_input"}, WithExtensions(local.Fill(p)))
	id, sid, tok := f.wakeTurn(t, "build")
	f.keep(t, sid, tok, "b-1", f.decisionBrief(t, id, sid, tok, f.tickets[0]))
	if d := f.rcv.next(t); d.header.Get("X-Conductor-Event") != config.EventBrakemanNeedsYou {
		t.Fatalf("the webhook: %s", d.body)
	}
	f.loud(t)
	got := sink.waitFor(t, "the question", func(ns []ext.Notification) bool { return len(messages(ns, "attention")) > 0 })
	for _, n := range got {
		if n.Type == config.EventBrakemanNeedsYou || n.Event == config.EventBrakemanNeedsYou || strings.Contains(n.Message, id) {
			t.Fatalf("the sink was handed %+v", n)
		}
	}
}

// TestTheNeedsYouEventIsBounded: the longest event, an id of 18 digits
// and 100 decisions, fits its bound; a count of 0 or past brakeman.MaxItems
// sends nothing.
func TestTheNeedsYouEventIsBounded(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	longest, err := json.Marshal(brakemanNeedsYouEvent{ID: "br-" + strings.Repeat("9", 18), Decisions: brakeman.MaxItems})
	if err != nil || len(longest) > maxBrakemanNeedsYou {
		t.Fatalf("%d bytes: %s", len(longest), longest)
	}
	events := b.sse(t)
	b.srv.events.brakemanNeedsYou("br-1", 0)
	b.srv.events.brakemanNeedsYou("br-2", brakeman.MaxItems+1)
	b.srv.events.brakemanNeedsYou("br-"+strings.Repeat("9", 18), brakeman.MaxItems)
	b.srv.events.brakemanOpened()
	got := needsYouBefore(t, events)
	if len(got) != 1 || got[0] != "brakeman_needs_you "+string(longest) {
		t.Fatalf("%v", got)
	}
}

// TestTheBriefsNotificationIsConductorsWords: the message for one decision
// and for several, one line within its bound; the path within its bound;
// the notification names no session, and nothing but the id and count.
func TestTheBriefsNotificationIsConductorsWords(t *testing.T) {
	t.Parallel()
	if got := needsYouMessage("br-14", 1); got != "Brakeman's background brief br-14 holds 1 decision" {
		t.Fatal(got)
	}
	if got := needsYouMessage("br-14", 3); got != "Brakeman's background brief br-14 holds 3 decisions" {
		t.Fatal(got)
	}
	id := "br-" + strings.Repeat("9", 18)
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	n := briefNotification(brakeman.Request{ID: id, Kind: brakeman.KindWake, State: brakeman.StateBriefed, BriefAt: at}, brakeman.MaxItems)
	if len(n.Message) > maxNeedsYouMessage || strings.ContainsAny(n.Message, "\r\n") || len(n.URL) > maxNeedsYouURL || n.URL != "/brakeman?request="+id {
		t.Fatalf("%+v", n)
	}
	want := ext.Notification{Event: "brakeman_needs_you", Type: "brakeman_needs_you", At: at, State: "briefed", Message: needsYouMessage(id, brakeman.MaxItems), URL: n.URL}
	if fmt.Sprintf("%+v", n) != fmt.Sprintf("%+v", want) {
		t.Fatalf("%+v", n)
	}
	body, err := webhookBody(n)
	if err != nil || string(body) != `{"sessionId":"","session":{"id":"","name":"","agentId":""},"entry":{"at":"2026-11-18T09:00:00Z","type":"brakeman_needs_you","message":"Brakeman's background brief `+id+` holds 100 decisions","url":"/brakeman?request=`+id+`"}}` {
		t.Fatalf("%s %v", body, err)
	}
	for _, r := range []brakeman.Request{{Kind: brakeman.KindBriefing, State: brakeman.StateBriefed}, {Kind: brakeman.KindWake, State: brakeman.StateRunning}, {Kind: brakeman.KindAsk, State: brakeman.StateBriefed}} {
		if got := briefNeedsYou(r, brakeman.Digest{Counts: brakeman.DigestCounts{Decisions: 2}}); got != 0 {
			t.Fatalf("%+v: %d", r, got)
		}
	}
}
