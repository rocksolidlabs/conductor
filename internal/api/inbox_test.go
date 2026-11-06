package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The decision inbox's routes (docs/plans/decision-inbox.md, PR-1): the
// open items as the sources show them, one item whole, the four steps of
// an action. The tests wait on the sources' own state (the session's
// attention, the task's, the router's steps), never on the clock.

// inboxPage is a page of GET /api/inbox, decoded.
type inboxPageOut struct {
	Items []inboxSummary `json:"items"`
	Next  string         `json:"next"`
	Total int            `json:"total"`
	At    time.Time      `json:"at"`
}

// inboxList reads a page of the inbox after after, failing t unless 200.
func (e *testEnv) inboxList(t *testing.T, after string) (inboxPageOut, int) {
	t.Helper()
	path := "/api/inbox"
	if after != "" {
		path += "?after=" + url.QueryEscape(after)
	}
	status, body := e.get(t, path)
	if status != http.StatusOK {
		t.Fatalf("inbox: %d %s", status, body)
	}
	var out inboxPageOut
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("inbox: %v in %s", err, body)
	}
	return out, len(body)
}

// get reads path as the admin: its status and body.
func (e *testEnv) get(t *testing.T, path string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", e.http.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// itemOf reads the item ref, failing t unless 200.
func (e *testEnv) itemOf(t *testing.T, ref string) inboxItem {
	t.Helper()
	status, body := e.get(t, "/api/inbox/item?ref="+url.QueryEscape(ref))
	if status != http.StatusOK {
		t.Fatalf("item %s: %d %s", ref, status, body)
	}
	var out struct {
		Item inboxItem `json:"item"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Item
}

// trackOf reads the four steps of an action on ref, by name.
func (e *testEnv) trackOf(t *testing.T, ref, receipt, attempt string) map[string]deliveryStep {
	t.Helper()
	v := url.Values{"ref": {ref}, "receipt": {receipt}}
	if attempt != "" {
		v.Set("attempt", attempt)
	}
	status, body := e.get(t, "/api/inbox/track?"+v.Encode())
	if status != http.StatusOK {
		t.Fatalf("track %s %s: %d %s", ref, receipt, status, body)
	}
	if len(body) > maxInboxTrack {
		t.Fatalf("track %s: %d bytes, past %d", ref, len(body), maxInboxTrack)
	}
	var out struct {
		Steps []deliveryStep `json:"steps"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Steps) != 4 {
		t.Fatalf("track %s: %d steps", ref, len(out.Steps))
	}
	m := map[string]deliveryStep{}
	for i, s := range out.Steps {
		if want := []string{stepAvailable, stepRecorded, stepValidated, stepResumed}[i]; s.Step != want {
			t.Fatalf("track %s: step %d is %s", ref, i, s.Step)
		}
		m[s.Step] = s
	}
	return m
}

// listed reports whether ref is among the page's items.
func listed(p inboxPageOut, ref string) bool {
	return slices.ContainsFunc(p.Items, func(s inboxSummary) bool { return s.Ref == ref })
}

// actionNames is the names of the item's actions, in order.
func actionNames(it inboxItem) []string {
	out := []string{}
	for _, a := range it.Actions {
		out = append(out, a.Name)
	}
	return out
}

// sessionHex is a session id for the name: 16 hex digits, as session.NewID
// makes them and Brakeman's refs take them.
func sessionHex(name string) string {
	h := fnv.New64a()
	h.Write([]byte(name))
	return fmt.Sprintf("%016x", h.Sum64())
}

// threeChoices are a permission dialog's three choices.
var threeChoices = []session.Option{{Label: "Yes", Input: "1"}, {Label: "Yes, and don't ask again for git commands", Input: "2"}, {Label: "No", Input: "3"}}

// A session's question is an item as the session shows it: its ref, its
// words, three answer actions whose inputs are the choices' and whose
// scope names each key, the question's id as each one's Expect.
func TestInboxListsAQuestionAsTheSessionShowsIt(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := newFakeSession(t, e, sessionHex("api-fix"), session.Options{})
	fs.l.SetAttentionFull(session.AttentionNeedsInput, "Allow git push?", session.SourceAPI, session.KindPermission, threeChoices)
	q := fs.l.Info().Attention.ID
	ref := questionRef(sessionHex("api-fix"), q)
	page, _ := e.inboxList(t, "")
	if !listed(page, ref) || page.Total != 1 {
		t.Fatalf("the list: %+v", page)
	}
	if s := page.Items[0]; s.Kind != inboxQuestion || s.Question != "Allow git push?" || s.Answerer != inboxAnswerer || len(s.Actions) != 3 {
		t.Fatalf("the summary: %+v", s)
	}
	it := e.itemOf(t, ref)
	if it.Subject != ref || it.Revision != q || it.Question != "Allow git push?" || it.BriefBy != "conductor" || it.Recommended != -1 {
		t.Fatalf("the item: %+v", it)
	}
	if len(it.Actions) != 3 {
		t.Fatalf("actions: %+v", it.Actions)
	}
	for i, a := range it.Actions {
		o := threeChoices[i]
		if a.Name != "answer" || a.Input != o.Input || a.Expect != q || a.Label != o.Label || !strings.Contains(a.Scope, `"`+o.Input+`"`) || !strings.Contains(a.Scope, sessionHex("api-fix")) || a.Route != "/ws/sessions/"+sessionHex("api-fix") {
			t.Fatalf("action %d: %+v", i, a)
		}
	}
	if len(it.Evidence) == 0 || it.Evidence[0].Ref != "session:"+sessionHex("api-fix") {
		t.Fatalf("evidence: %+v", it.Evidence)
	}
}

// A question answered by keys typed in the terminal is no item; nor is a
// session's once the registry no longer lists it.
func TestInboxLeavesOutAQuestionAnsweredInTheTerminal(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := newFakeSession(t, e, sessionHex("typed"), session.Options{Questions: codexQuestions(t)})
	if err := fs.proc.Print(codexApproval("curl -sI https://example.com")); err != nil {
		t.Fatal(err)
	}
	q := shownDialog(t, fs).ID
	if page, _ := e.inboxList(t, ""); !listed(page, questionRef(sessionHex("typed"), q)) {
		t.Fatalf("the dialog before the keys: %+v", page)
	}
	sub := fs.owner(t, "Nate")
	if err := fs.l.Input(sub, []byte("y")); err != nil {
		t.Fatal(err)
	}
	until(t, "the question read answered in the terminal", func() bool {
		a := fs.l.Info().Attention
		return a.ID == q && a.Answered == session.AnsweredTerminal
	})
	if page, _ := e.inboxList(t, ""); listed(page, questionRef(sessionHex("typed"), q)) || page.Total != 0 {
		t.Fatalf("a question answered in the terminal: %+v", page)
	}
	// While it still shows, a page's answer reads refused, the terminal's
	// keys first, never "not answered yet"; and so once it has left.
	ref := questionRef(sessionHex("typed"), q)
	terminal := func(when string) {
		t.Helper()
		got := e.trackOf(t, ref, q, "r1")
		if got[stepRecorded].State != stepRefused || got[stepRecorded].Words != terminalWords || got[stepValidated].State != stepNotApplicable || got[stepResumed].State != stepNotApplicable {
			t.Fatalf("the track %s: %+v", when, got)
		}
		it := e.itemOf(t, ref)
		if len(it.Actions) != 0 || it.Now != "no longer open: "+terminalWords || len(it.Delivery) != 4 || it.Delivery[1].State != stepRefused {
			t.Fatalf("the item %s: %+v", when, it)
		}
	}
	terminal("while it shows")
	fs.report(t, session.AttentionWorking, "")
	until(t, "the question gone", func() bool { return fs.l.Info().Attention.ID != q })
	terminal("once it left")
}

func TestInboxLeavesOutASessionNoLongerListed(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := newFakeSession(t, e, sessionHex("gone"), session.Options{})
	q := fs.ask(t, "Which database?", dbChoices)
	if page, _ := e.inboxList(t, ""); !listed(page, questionRef(sessionHex("gone"), q)) {
		t.Fatalf("the question before: %+v", page)
	}
	e.srv.registry.Remove(sessionHex("gone"))
	if page, _ := e.inboxList(t, ""); page.Total != 0 {
		t.Fatalf("a session no longer listed: %+v", page)
	}
	if status, _ := e.get(t, "/api/inbox/item?ref="+url.QueryEscape(questionRef(sessionHex("gone"), q))); status != http.StatusNotFound {
		t.Fatalf("its item: %d", status)
	}
}

// A submission held for the terminal (the hold a line let go makes) is one
// item with no answer: its words send the person to the terminal, which is
// first in its evidence; an answer naming its id is refused by the
// session, nothing typed.
func TestInboxHeldTurnOffersNoAnswer(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := newFakeSession(t, e, sessionHex("held"), session.Options{SubmitPause: time.Hour})
	b := fs.ask(t, "Which name?", nil)
	try, giveUp := context.WithCancel(t.Context())
	late := make(chan error, 1)
	go func() {
		_, err := testHost(e.srv, try, "sched").Submit(context.Background(), ext.SubmitRequest{Session: sessionHex("held"), Text: "late", Question: b, By: ada})
		late <- err
	}()
	until(t, "the line typed", func() bool { return slices.Contains(fs.written(), "late") })
	giveUp()
	if err := <-late; !errors.Is(err, context.Canceled) {
		t.Fatalf("the given-up line: %v", err)
	}
	until(t, "the hold shown", func() bool {
		att := fs.l.Info().Attention
		return fs.l.Held() && att.State == session.AttentionNeedsInput && att.Source == session.SourceSubmit
	})
	hold := fs.l.Info().Attention.ID
	page, _ := e.inboxList(t, "")
	if page.Total != 1 || !listed(page, questionRef(sessionHex("held"), hold)) || len(page.Items[0].Actions) != 0 {
		t.Fatalf("the list: %+v", page)
	}
	it := e.itemOf(t, questionRef(sessionHex("held"), hold))
	if len(it.Actions) != 0 || !strings.Contains(it.Brief, "held for keys typed in its terminal") || it.Evidence[0].Ref != "session:"+sessionHex("held") {
		t.Fatalf("the held turn's item: %+v", it)
	}
	before := len(fs.written())
	sub := fs.owner(t, "Nate")
	if err := fs.l.AnswerInput(context.Background(), sub, hold, []byte("x\r")); !errors.Is(err, session.ErrQuestionAnswered) {
		t.Fatalf("an answer naming the hold: %v", err)
	}
	if got := fs.written(); len(got) != before {
		t.Fatalf("typed into a held turn: %q", got[before:])
	}
}

// A question's track: answered through the claim, recorded and validated
// done at the answer's time and resumed done once the agent works; two
// answers with two request ids, the loser's reading another answer
// first; one withdrawn by the screen, the reason; one forgotten past 64,
// unknown.
func TestInboxTrackQuestion(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := newFakeSession(t, e, sessionHex("asked"), session.Options{})
	nate := fs.owner(t, "Nate")
	q := fs.ask(t, "Which database?", dbChoices)
	ref := questionRef(sessionHex("asked"), q)
	if steps := e.trackOf(t, ref, q, "r1"); steps[stepRecorded].State != stepWaiting {
		t.Fatalf("before an answer: %+v", steps)
	}
	if err := fs.l.AnswerInputReq(context.Background(), nate, q, "r1", []byte("Postgres\r"), nil); err != nil {
		t.Fatal(err)
	}
	if err := fs.l.AnswerInputReq(context.Background(), nate, q, "r2", []byte("SQLite\r"), nil); !errors.Is(err, session.ErrQuestionAnswered) {
		t.Fatalf("the second answer: %v", err)
	}
	fs.written()
	win := e.trackOf(t, ref, q, "r1")
	if win[stepRecorded].State != stepDone || win[stepValidated].State != stepDone || win[stepRecorded].At == nil || !strings.Contains(win[stepRecorded].Words, "Nate") || win[stepResumed].State != stepWaiting {
		t.Fatalf("the winner's: %+v", win)
	}
	lose := e.trackOf(t, ref, q, "r2")
	if lose[stepRecorded].State != stepRefused || !strings.Contains(lose[stepRecorded].Words, "another answer first") || lose[stepResumed].State != stepNotApplicable {
		t.Fatalf("the loser's: %+v", lose)
	}
	fs.report(t, session.AttentionWorking, "")
	if got := e.trackOf(t, ref, q, "r1")[stepResumed]; got.State != stepDone || got.At == nil {
		t.Fatalf("resumed: %+v", got)
	}
	// Withdrawn by the screen: the agent moved on.
	w := fs.ask(t, "Which port?", nil)
	fs.report(t, session.AttentionWorking, "")
	if got := e.trackOf(t, questionRef(sessionHex("asked"), w), w, "r3"); got[stepRecorded].State != stepRefused || !strings.Contains(got[stepRecorded].Words, "withdrawn") {
		t.Fatalf("withdrawn: %+v", got)
	}
	// The item of an answered question: no longer open, its delivery.
	if it := e.itemOf(t, ref); len(it.Actions) != 0 || len(it.Delivery) != 4 || it.Delivery[1].State != stepDone {
		t.Fatalf("the answered question's item: %+v", it)
	}
	// Forgotten past the 64 kept.
	for i := range 65 {
		x := fs.ask(t, fmt.Sprintf("Question %d?", i), dbChoices)
		fs.answer(t, nate, x, "Postgres\r")
	}
	if got := e.trackOf(t, ref, q, "r1"); got[stepRecorded].State != stepUnknown || got[stepResumed].State != stepUnknown {
		t.Fatalf("forgotten: %+v", got)
	}
}

// The resumption kept: a question answered, the agent working and its
// turn done before the first read: resumed done at the time it worked,
// the same from two reads as two browsers.
func TestInboxTrackReadsTheKeptResumption(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := newFakeSession(t, e, sessionHex("kept"), session.Options{})
	nate := fs.owner(t, "Nate")
	q := fs.ask(t, "Which database?", dbChoices)
	if err := fs.l.AnswerInputReq(context.Background(), nate, q, "r1", []byte("Postgres\r"), nil); err != nil {
		t.Fatal(err)
	}
	fs.written()
	fs.report(t, session.AttentionWorking, "")
	worked := fs.l.Info().Attention.Since
	fs.report(t, session.AttentionDone, "done")
	fs.report(t, session.AttentionNone, "")
	ref := questionRef(sessionHex("kept"), q)
	one, two := e.trackOf(t, ref, q, "r1"), e.trackOf(t, ref, q, "r1")
	for _, got := range []deliveryStep{one[stepResumed], two[stepResumed]} {
		if got.State != stepDone || got.At == nil || worked == nil || !got.At.Equal(*worked) {
			t.Fatalf("resumed %+v, worked at %v", got, worked)
		}
	}
}

// A hosted session's question: recorded and validated unknown with the
// words, resumed from the milestone the server kept as the host synced
// the session's attention, and unknown for one whose leaving the server
// did not see.
func TestInboxTrackHostedQuestion(t *testing.T) {
	e := newTestEnv(t, nil)
	id := startHost(t, e, []string{"/bin/cat"})
	report(t, e, id, map[string]any{"state": "needs_input", "message": "Allow?", "kind": "permission", "options": yes})
	var q string
	until(t, "the hosted question shown", func() bool {
		q = hostedSession(t, e, id).Info().Attention.ID
		return q != ""
	})
	ref := questionRef(id, q)
	if page, _ := e.inboxList(t, ""); !listed(page, ref) {
		t.Fatalf("the hosted question: %+v", page)
	}
	steps := e.trackOf(t, ref, q, "r1")
	if steps[stepRecorded].State != stepUnknown || steps[stepRecorded].Words != hostHoldsWords || steps[stepValidated].State != stepUnknown || steps[stepResumed].State != stepWaiting {
		t.Fatalf("while asked: %+v", steps)
	}
	report(t, e, id, map[string]any{"state": "working"})
	// Synced: the host said it (HostView), not only the server forwarded it.
	until(t, "the host's working synced", func() bool { return hostedSession(t, e, id).HostView().Attention.State == session.AttentionWorking })
	for range 2 {
		if got := e.trackOf(t, ref, q, "r1")[stepResumed]; got.State != stepDone || got.At == nil {
			t.Fatalf("resumed: %+v", got)
		}
	}
	// A change the server forwarded and the host has not applied (its
	// attention in Info, not in what the host said) is no resumption: the
	// hub's OnChange with it records none.
	report(t, e, id, map[string]any{"state": "needs_input", "message": "Again?", "kind": "permission", "options": yes})
	var q2 string
	until(t, "the second question shown", func() bool {
		hs := hostedSession(t, e, id)
		q2 = hs.HostView().Attention.ID
		return q2 != "" && q2 != q && hs.HostView().Attention.Message == "Again?"
	})
	unapplied := hostedSession(t, e, id).Info()
	unapplied.Attention = session.Attention{State: session.AttentionWorking}
	e.srv.hosts.OnChange(unapplied)
	if got := e.trackOf(t, questionRef(id, q2), q2, "r2")[stepResumed]; got.State != stepWaiting {
		t.Fatalf("resumed by a change the host has not applied: %+v", got)
	}
	e.srv.hostedSeen.gone(id) // what a restart forgets
	if got := e.trackOf(t, ref, q, "r1"); got[stepResumed].State != stepUnknown {
		t.Fatalf("not seen here: %+v", got)
	}
}

// Every field at its bound with characters that escape to six bytes: a
// summary at most 4 KiB, an item at most 96 KiB, encoded.
func maxItem() *inboxItem {
	esc := func(n int) string { return strings.Repeat("<", n) }
	ctl := strings.Repeat("\x01", proto.MaxAnswerInput)
	ref := "file:" + strings.Repeat("a", 80)
	it := &inboxItem{Ref: "question:" + strings.Repeat("a", 16) + "/" + strings.Repeat("b", 16), Kind: inboxQuestion, Since: time.Now(),
		Title: fitText(esc(400), 200), Brief: fitText(esc(4000), 2<<10), BriefBy: "brakeman", Card: "brief:br-123456789012345678/99",
		Question: esc(session.MaxAttentionMessage), Answerer: fitText(esc(200), 120), RoutedRule: fitText(esc(400), 200),
		Revision: strings.Repeat("9", 64), Now: fitText(esc(900), 500), WhyHuman: fitText(esc(3000), 1<<10),
		Uncertainty: fitText(esc(3000), 1<<10), Authorizes: fitText(esc(3000), 1<<10), CardRev: strings.Repeat("9", 64),
		CardNote: fitText(esc(400), 200), Recommended: 5}
	it.Subject = it.Ref
	for range ext.MaxDestinations {
		it.RoutedTo = append(it.RoutedTo, inboxDestination{Kind: "person", Name: strings.Repeat("<", ext.MaxDestinationName)})
	}
	it.Blocking.Words = fitText(esc(900), 500)
	for range inboxRefs {
		it.Blocking.Refs = append(it.Blocking.Refs, fitText(ref, maxInboxRef))
		it.Evidence = append(it.Evidence, inboxRef{Ref: fitText(ref, maxInboxRef), Label: fitText(esc(400), 200)})
	}
	for range inboxActions {
		it.Actions = append(it.Actions, inboxAction{Name: "answer", Label: fitText(esc(100), 80), Input: ctl, Text: true, Expect: strings.Repeat("9", 64),
			TicketRev: 1 << 62, Scope: fitText(esc(2000), 1<<10), Confirm: true, Route: fitText(esc(400), 200)})
	}
	for range 6 {
		it.Considered = append(it.Considered, cardAlternative{Label: fitText(esc(400), 320), Tradeoffs: fitText(esc(3000), 1<<10)})
	}
	at := time.Now()
	for _, s := range []string{stepAvailable, stepRecorded, stepValidated, stepResumed} {
		it.Delivery = append(it.Delivery, deliveryStep{Step: s, State: stepNotApplicable, At: &at, Words: fitText(esc(900), 500), Receipt: fitText(ref, maxInboxRef)})
	}
	return it
}

func TestInboxSummaryBounded(t *testing.T) {
	b, err := json.Marshal(maxItem().summary())
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxInboxSummary {
		t.Fatalf("a summary at its bounds: %d bytes, past %d", len(b), maxInboxSummary)
	}
}

func TestInboxItemBounded(t *testing.T) {
	b, err := json.Marshal(maxItem())
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxInboxItem {
		t.Fatalf("an item at its bounds: %d bytes, past %d", len(b), maxInboxItem)
	}
	// A display field is cut on a rune boundary until its encoded form
	// fits; an exact one never is.
	if got := fitText(strings.Repeat("é<", 300), 100); jsonLen(got) > 100 || !strings.HasSuffix(got, "…") {
		t.Fatalf("cut: %d %q", jsonLen(got), got)
	}
}

// 150 summaries at their bounds: the reply at most 640 KiB.
func TestInboxReplyBounded(t *testing.T) {
	s := maxItem().summary()
	out := inboxPageOut{Total: 1 << 30, Next: strings.Repeat("x", maxInboxCursor), At: time.Now()}
	for range inboxPage {
		out.Items = append(out.Items, s)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxInboxReply {
		t.Fatalf("a page at its bounds: %d bytes, past %d", len(b), maxInboxReply)
	}
}

// An item whose choices' inputs are 47 '<' and a CR and 48 control bytes,
// and whose question is 500 bytes of '<', '&' and quotes, round trips:
// each decoded value equal to the session's byte for byte, and the decoded
// input taken by AnswerAs as the choice.
func TestInboxExactFieldsRoundTrip(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := newFakeSession(t, e, sessionHex("exact"), session.Options{})
	words := strings.Repeat(`<&"`, 167)[:session.MaxAttentionMessage]
	choices := []session.Option{{Label: "angles", Input: strings.Repeat("<", 47) + "\r"}, {Label: "controls", Input: strings.Repeat("\x02", 48)}}
	fs.l.SetAttentionFull(session.AttentionNeedsInput, words, session.SourceAPI, session.KindPermission, choices)
	att := fs.l.Info().Attention
	it := e.itemOf(t, questionRef(sessionHex("exact"), att.ID))
	if it.Question != att.Message || len(it.Question) != session.MaxAttentionMessage {
		t.Fatalf("the question: %d bytes, the session's %d", len(it.Question), len(att.Message))
	}
	if len(it.Actions) != 2 || it.Actions[0].Input != att.Options[0].Input || it.Actions[1].Input != att.Options[1].Input {
		t.Fatalf("the inputs: %+v, the session's %+v", it.Actions, att.Options)
	}
	if b, _ := json.Marshal(it.Actions[0].Input); len(b)-2 != 47*6+2 { // 284: each '<' six bytes, the CR two
		t.Fatalf("the input encoded in %d bytes", len(b)-2)
	}
	if err := fs.l.AnswerAs(context.Background(), att.ID, it.Actions[1].Input, session.Actor{Name: "Ada"}); err != nil {
		t.Fatalf("the decoded input as the choice: %v", err)
	}
	if got := fs.written(); len(got) == 0 || got[len(got)-1] != choices[1].Input {
		t.Fatalf("written %q", got)
	}
}

// 200 open items over two pages: next and total, none lost or repeated.
func TestInboxPages(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.MaxSessions = 300 })
	want := map[string]bool{}
	for i := range 200 {
		fs := newFakeSession(t, e, sessionHex(fmt.Sprintf("s%03d", i)), session.Options{})
		want[questionRef(fs.l.Info().ID, fs.ask(t, "Which?", dbChoices))] = true
	}
	first, _ := e.inboxList(t, "")
	if len(first.Items) != inboxPage || first.Next == "" || first.Total != 200 {
		t.Fatalf("the first page: %d items, next %q, total %d", len(first.Items), first.Next, first.Total)
	}
	second, _ := e.inboxList(t, first.Next)
	if len(second.Items) != 50 || second.Next != "" || second.Total != 200 {
		t.Fatalf("the second page: %d items, next %q, total %d", len(second.Items), second.Next, second.Total)
	}
	seen := map[string]bool{}
	for _, s := range append(first.Items, second.Items...) {
		if seen[s.Ref] || !want[s.Ref] {
			t.Fatalf("%s repeated or unknown", s.Ref)
		}
		seen[s.Ref] = true
	}
	if status, _ := e.get(t, "/api/inbox?after=nonsense"); status != http.StatusBadRequest {
		t.Fatalf("a bad cursor: %d", status)
	}
	// A parameter a route does not take, or one given twice, is refused,
	// never read as page one or as its first value.
	ref := url.QueryEscape(first.Items[0].Ref)
	for _, path := range []string{"/api/inbox?cursor=" + url.QueryEscape(first.Next), "/api/inbox?after=" + url.QueryEscape(first.Next) + "&after=" + url.QueryEscape(first.Next),
		"/api/inbox/item?ref=" + ref + "&ref=" + ref, "/api/inbox/item?ref=" + ref + "&receipt=x",
		"/api/inbox/track?ref=" + ref + "&ref=" + ref, "/api/inbox/track?ref=" + ref + "&receipt=a&receipt=b", "/api/inbox/track?ref=" + ref + "&nope=1"} {
		if status, body := e.get(t, path); status != http.StatusBadRequest || !strings.Contains(string(body), "invalid_request") {
			t.Fatalf("%s: %d %s", path, status, body)
		}
	}
}

// A test router routes a question to a person: the item names whom, with
// destinations at their bound holding commas and parentheses whole; with
// the empty route, nothing; a route held at the router's gate while the
// question is replaced, then let go, names the old question and not the
// new.
func TestInboxRoutedToKeyedByQuestion(t *testing.T) {
	seams := watchRouter(t)
	gate := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	name := strings.Repeat("a", ext.MaxDestinationName-10) + "(b, c), d!"
	var to []ext.Destination
	for range ext.MaxDestinations {
		to = append(to, ext.Destination{Kind: "person", Name: name})
	}
	f := &fakeRouter{route: func(_ context.Context, d ext.Decision) (ext.Route, error) {
		switch d.Message {
		case "Routed?":
			return ext.Route{To: to, Rule: "owners (all)"}, nil
		case "Held?":
			<-gate
			return ext.Route{To: to[:1]}, nil
		}
		return ext.Route{}, nil
	}}
	e := routerEnv(t, f)
	fs := newFakeSession(t, e, sessionHex("routed"), session.Options{})
	q1 := fs.ask(t, "Routed?", dbChoices)
	seams.wait(t, "routed", q1)
	it := e.itemOf(t, questionRef(sessionHex("routed"), q1))
	if len(it.RoutedTo) != ext.MaxDestinations || it.RoutedTo[0].Name != name || it.RoutedTo[0].Kind != "person" || it.RoutedRule != "owners (all)" {
		t.Fatalf("routed to: %+v %q", it.RoutedTo, it.RoutedRule)
	}
	if page, _ := e.inboxList(t, ""); page.Items[0].Routed != ext.MaxDestinations {
		t.Fatalf("the summary: %+v", page.Items[0])
	}
	q2 := fs.ask(t, "Empty?", dbChoices)
	seams.wait(t, "routed", q2)
	if it := e.itemOf(t, questionRef(sessionHex("routed"), q2)); len(it.RoutedTo) != 0 {
		t.Fatalf("the empty route: %+v", it.RoutedTo)
	}
	held := fs.ask(t, "Held?", dbChoices)
	until(t, "the router given the held question", func() bool { return slices.Contains(f.routedIDs(), held) })
	newer := fs.ask(t, "Newer?", dbChoices)
	close(gate)
	seams.wait(t, "routed", held)
	seams.wait(t, "routed", newer)
	if it := e.itemOf(t, questionRef(sessionHex("routed"), newer)); len(it.RoutedTo) != 0 {
		t.Fatalf("the newer question carries the old one's route: %+v", it.RoutedTo)
	}
	if rt, ok := e.srv.router.routeOf(sessionHex("routed"), held); !ok || len(rt.To) != 1 {
		t.Fatalf("the held question's route: %+v %v", rt, ok)
	}
}

// The routes are the admin's alone: no token, an agent's, a wrong one, a
// session's control link's and view link's (the roles that may answer or
// watch that session in its terminal) and a host's are each refused on
// all three, a hosted session's question among the items tried; on a
// switchyard 403 switchyard.
func TestInboxIsAdminOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	tok := e.agentToken(id)
	_, control := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	_, view := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	hosted := startHost(t, e, []string{"/bin/cat"})
	report(t, e, hosted, map[string]any{"state": "needs_input", "message": "Allow?", "kind": "permission", "options": yes})
	var hq string
	until(t, "the hosted question shown", func() bool {
		hq = hostedSession(t, e, hosted).Info().Attention.ID
		return hq != ""
	})
	// The refusals are many from one address: the limiter would answer
	// 429 before the 401 this test reads.
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	tokens := map[string]string{"no token": "", "an agent's": tok, "a wrong one": "nope", "a control link's": control["token"].(string),
		"a view link's": view["token"].(string), "a host's": "test-host-token"}
	for _, path := range []string{"/api/inbox", "/api/inbox/item?ref=question:" + id + "/0123456789abcdef", "/api/inbox/item?ref=" + url.QueryEscape(questionRef(hosted, hq)),
		"/api/inbox/track?ref=task:a-00000000/b&receipt=c", "/api/inbox/track?ref=" + url.QueryEscape(questionRef(hosted, hq)) + "&receipt=" + hq} {
		for name, token := range tokens {
			if resp, _ := e.do("GET", path, token, nil); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s with %s: %d", path, name, resp.StatusCode)
			}
		}
	}
	if resp, _ := e.do("GET", "/api/inbox", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the admin: %d", resp.StatusCode)
	}
}

func TestInboxOnASwitchyard(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	for _, path := range []string{"/api/inbox", "/api/inbox/item?ref=task:a-00000000/b", "/api/inbox/track?ref=task:a-00000000/b&receipt=c"} {
		if resp, out := e.do("GET", path, adminToken, nil); resp.StatusCode != http.StatusForbidden || errCodeOf(out) != "switchyard" {
			t.Fatalf("%s: %d %v", path, resp.StatusCode, out)
		}
	}
}

// keepCard keeps a brief of one decision card about about, written at
// the subject's revision rev, through Brakeman's store, as a turn leaves
// one.
func keepCard(t *testing.T, e *testEnv, about, happened, authorizes, rev string) string {
	t.Helper()
	st := e.srv.brakeman.st
	r, _, err := st.Create(brakeman.Request{OpID: fmt.Sprintf("card-%d", bkOps.Add(1)), Kind: brakeman.KindBriefing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Start(r.ID, brakeman.DefaultConfig(e.root), brakeman.Stamp{AgentID: "bk"}); err != nil {
		t.Fatal(err)
	}
	rec := 0
	d := &brakeman.Decision{About: ref(t, about), Happened: happened, WhyHuman: "Its attempts are spent.", Question: "Retry it?",
		Alternatives:   []brakeman.Alternative{{Label: "Retry", Tradeoffs: "One more try."}, {Label: "Leave it", Tradeoffs: "The run stays failed."}},
		Recommendation: &rec, Uncertainty: "The test may be flaky.", Authorizes: authorizes, Evidence: []brakeman.Ref{ref(t, about)}, SubjectRevision: rev}
	b := brakeman.Brief{Schema: brakeman.BriefSchema, Title: "Decide", Summary: "A decision.", Items: []brakeman.Item{{Kind: brakeman.ItemDecision, Decision: d}}}
	if _, err := st.KeepBrief(r.ID, brakeman.Kept{OpID: "brief-1", Brief: b}); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// Two briefs about one question: the newer card's fields fill the item,
// its 2 KiB happened whole, its authorizes in Authorizes beside
// Conductor's scope, and a card read at another revision says so.
func TestInboxCarriesTheNewestBrakemanCard(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := newFakeSession(t, e, sessionHex("carded"), session.Options{})
	q := fs.ask(t, "Retry it?", dbChoices)
	about := questionRef(sessionHex("carded"), q)
	keepCard(t, e, about, "older", "the older card", q)
	happened := strings.Repeat("h", 2<<10)
	newer := keepCard(t, e, about, happened, "A fourth attempt of build.", "0000000000000000")
	it := e.itemOf(t, about)
	if it.BriefBy != "brakeman" || it.Brief != happened || it.Card != "brief:"+newer+"/0" || it.Authorizes != "A fourth attempt of build." ||
		it.WhyHuman != "Its attempts are spent." || len(it.Considered) != 2 || it.Recommended != 0 || it.Uncertainty == "" {
		t.Fatalf("the card's fields: %+v", it)
	}
	if it.Actions[0].Scope == it.Authorizes || it.Actions[0].Scope == "" {
		t.Fatalf("Brakeman's words in Conductor's scope: %+v", it.Actions[0])
	}
	if it.CardRev != "0000000000000000" || !strings.Contains(it.CardNote, "Brakeman read this at revision 0000000000000000; it is at "+q) {
		t.Fatalf("the card's revision: %q %q", it.CardRev, it.CardNote)
	}
	if page, _ := e.inboxList(t, ""); !page.Items[0].Card {
		t.Fatalf("the summary: %+v", page.Items[0])
	}
}

// An Approve's scope keeps what it must disclose whole within its 1 KiB:
// 30 tasks of 40 characters and their agents are shortened ("and N
// more"), never the yolo choice, the approval it supersedes or that
// launching is a separate step.
func TestInboxApprovalScopeKeepsItsDisclosures(t *testing.T) {
	var tasks, agents []string
	for i := range 30 {
		tasks = append(tasks, fmt.Sprintf("%040d", i))
		agents = append(agents, fmt.Sprintf("agent-%034d", i))
	}
	on := true
	p := dispatch.OpenPlan{PlanSummary: dispatch.PlanSummary{N: 3, Base: strings.Repeat("a", 40)}, Key: "CND-12", TicketRev: 9}
	scope := approvalScope(p, strings.Repeat("<", 300), tasks, agents, &on, []int{2})
	if jsonLen(scope) > maxScope {
		t.Fatalf("%d encoded bytes", jsonLen(scope))
	}
	for _, want := range []string{"Approves revision 3 of CND-12", yoloWords(&on), "sets revision 2's approval superseded", "launching is a separate step on the ticket page", "more", "against the ticket at revision 9"} {
		if !strings.Contains(scope, want) {
			t.Fatalf("the scope lost %q: %s", want, scope)
		}
	}
	a := inboxAction{Scope: scope}
	it := &inboxItem{}
	it.addAction(a)
	if it.Actions[0].Scope != scope {
		t.Fatal("the scope cut again by addAction")
	}
}

// Every way a server session's question leaves (session's
// TestHowAQuestionLeaves holds the session to the same table), against
// what the track and the item then read: kept to show again, waiting
// and not open now; gone for good, recorded refused at the time it went
// with how, the rest not applicable, and the item no longer open saying
// so; the session no longer listed, unknown and no item. Each row waits
// for the entry the session keeps of how the question left
// (QuestionLeft, noted in the step that let it go), never for what
// shows.
func TestInboxTrackHowAQuestionLeft(t *testing.T) {
	e := newTestEnv(t, nil)
	n := 0
	session1 := func(t *testing.T, o session.Options) (*fakeSession, string) {
		t.Helper()
		n++
		id := sessionHex(fmt.Sprintf("leaves %d", n))
		return newFakeSession(t, e, id, o), id
	}
	leftEntry := func(t *testing.T, fs *fakeSession, q string) {
		t.Helper()
		until(t, "the entry of how the question left", func() bool {
			_, ok := fs.l.QuestionLeft(q)
			return ok
		})
	}
	gone := func(t *testing.T, id, q, words string) {
		t.Helper()
		ref := questionRef(id, q)
		got := e.trackOf(t, ref, q, "r1")
		if got[stepRecorded].State != stepRefused || got[stepRecorded].Words != words || got[stepRecorded].At == nil ||
			got[stepValidated].State != stepNotApplicable || got[stepResumed].State != stepNotApplicable {
			t.Fatalf("the track: %+v", got)
		}
		it := e.itemOf(t, ref)
		if len(it.Actions) != 0 || it.Now != "no longer open: "+words || len(it.Delivery) != 4 || it.Delivery[1].State != stepRefused {
			t.Fatalf("the item: %+v", it)
		}
	}
	t.Run("keys typed in the terminal, then the process ends", func(t *testing.T) {
		fs, id := session1(t, session.Options{Questions: codexQuestions(t)})
		if err := fs.proc.Print(codexApproval("curl -sI https://example.com")); err != nil {
			t.Fatal(err)
		}
		q := shownDialog(t, fs).ID
		if err := fs.l.Input(fs.owner(t, "Nate"), []byte("y")); err != nil {
			t.Fatal(err)
		}
		until(t, "the question read answered in the terminal", func() bool {
			a := fs.l.Info().Attention
			return a.ID == q && a.Answered == session.AnsweredTerminal
		})
		fs.proc.End(0)
		leftEntry(t, fs, q)
		gone(t, id, q, terminalWords)
	})
	t.Run("the session ends while it shows", func(t *testing.T) {
		fs, id := session1(t, session.Options{})
		q := fs.ask(t, "Which database?", dbChoices)
		fs.proc.End(0)
		leftEntry(t, fs, q)
		gone(t, id, q, leftWords[session.LeftEnded])
	})
	t.Run("another question in its place", func(t *testing.T) {
		fs, id := session1(t, session.Options{})
		q := fs.ask(t, "Which database?", dbChoices)
		fs.ask(t, "Which port?", dbChoices)
		leftEntry(t, fs, q)
		gone(t, id, q, leftWords[session.LeftSuperseded])
	})
	t.Run("withdrawn by the agent's next state", func(t *testing.T) {
		fs, id := session1(t, session.Options{})
		q := fs.ask(t, "Which database?", dbChoices)
		fs.report(t, session.AttentionWorking, "")
		leftEntry(t, fs, q)
		gone(t, id, q, leftWords[session.LeftWithdrawn])
	})
	t.Run("set aside, then withdrawn by its id", func(t *testing.T) {
		fs, id := session1(t, session.Options{})
		q := fs.askOwn(t, "Which database?", dbChoices)
		fs.report(t, session.AttentionWorking, "")
		ref := questionRef(id, q)
		got := e.trackOf(t, ref, q, "r1")
		for _, s := range []string{stepRecorded, stepValidated, stepResumed} {
			if got[s].State != stepWaiting || got[s].Words != keptWords {
				t.Fatalf("set aside: %+v", got)
			}
		}
		if it := e.itemOf(t, ref); len(it.Actions) != 0 || it.Now != "not open now: "+keptWords {
			t.Fatalf("the item set aside: %+v", it)
		}
		fs.l.WithdrawQuestion(q)
		leftEntry(t, fs, q)
		gone(t, id, q, leftWords[session.LeftWithdrawn])
	})
	t.Run("the session no longer listed", func(t *testing.T) {
		fs, id := session1(t, session.Options{})
		q := fs.ask(t, "Which database?", dbChoices)
		fs.proc.End(0)
		leftEntry(t, fs, q)
		e.srv.registry.Remove(id)
		ref := questionRef(id, q)
		if got := e.trackOf(t, ref, q, "r1"); got[stepRecorded].State != stepUnknown || got[stepResumed].State != stepUnknown {
			t.Fatalf("the track: %+v", got)
		}
		if status, body := e.get(t, "/api/inbox/item?ref="+url.QueryEscape(ref)); status != http.StatusNotFound {
			t.Fatalf("the item: %d %s", status, body)
		}
	})
}

// A hosted session's question set aside while the agent works (the
// host's Info.Kept) has not left: waiting, no resumption noted; shown
// again at its turn's done it waits still; gone at the work of the next
// turn, its resumption is that change's, set once.
func TestInboxTrackHostedQuestionSetAside(t *testing.T) {
	e := newTestEnv(t, nil)
	id := startHost(t, e, []string{"/bin/cat"})
	report(t, e, id, map[string]any{"state": "needs_input", "message": "Which database?", "kind": "prompt", "options": yes, "ask": true})
	var q string
	until(t, "the hosted question shown", func() bool {
		q = hostedSession(t, e, id).HostView().Attention.ID
		return q != ""
	})
	ref := questionRef(id, q)
	report(t, e, id, map[string]any{"state": "working"})
	until(t, "the question set aside", func() bool {
		v := hostedSession(t, e, id).HostView()
		return v.Attention.State == session.AttentionWorking && slices.Contains(v.Kept, q)
	})
	got := e.trackOf(t, ref, q, "r1")
	if got[stepRecorded].State != stepWaiting || got[stepRecorded].Words != keptWords || got[stepResumed].State != stepWaiting {
		t.Fatalf("set aside: %+v", got)
	}
	if res, _, _ := e.srv.hostedSeen.of(id, q); res != nil {
		t.Fatalf("a resumption noted for a question set aside: %+v", res)
	}
	report(t, e, id, map[string]any{"state": "done", "message": "done for now"})
	until(t, "shown again at the done", func() bool { return hostedSession(t, e, id).HostView().Attention.ID == q })
	if got := e.trackOf(t, ref, q, "r1"); got[stepResumed].State != stepWaiting || got[stepRecorded].State != stepUnknown {
		t.Fatalf("shown again: %+v", got)
	}
	report(t, e, id, map[string]any{"state": "working"})
	var worked *time.Time
	until(t, "gone at the next turn's work", func() bool {
		v := hostedSession(t, e, id).HostView()
		worked = v.Attention.Since
		return v.Attention.State == session.AttentionWorking && !slices.Contains(v.Kept, q)
	})
	until(t, "the resumption noted", func() bool {
		res, _, _ := e.srv.hostedSeen.of(id, q)
		return res != nil
	})
	for range 2 {
		got := e.trackOf(t, ref, q, "r1")[stepResumed]
		if got.State != stepDone || got.At == nil || worked == nil || !got.At.Equal(*worked) {
			t.Fatalf("resumed %+v, the work at %v", got, worked)
		}
	}
}

// The server notes how a hosted question went on before it publishes the
// change's session event: held at the moment between the two (the noted
// seam), no event of the change is out yet and the track reads it
// resumed; the event, once out, finds the same. And the moment before the
// noting (the host's change taken, the server's note not made yet) reads
// waiting, never unknown, so a page that read it then reads again.
func TestInboxTrackHostedNotedBeforeTheEvent(t *testing.T) {
	e := newTestEnv(t, nil)
	id := startHost(t, e, []string{"/bin/cat"})
	report(t, e, id, map[string]any{"state": "needs_input", "message": "Allow?", "kind": "permission", "options": yes})
	var q string
	until(t, "the hosted question shown", func() bool {
		q = hostedSession(t, e, id).HostView().Attention.ID
		return q != ""
	})
	ref := questionRef(id, q)
	ch := e.srv.events.subscribe()
	t.Cleanup(func() { e.srv.events.unsubscribe(ch) })
	working := func(msg []byte) bool {
		data, ok := strings.CutPrefix(string(msg), "event: session\ndata: ")
		if !ok {
			return false
		}
		var info session.Info
		return json.Unmarshal([]byte(strings.TrimSpace(data)), &info) == nil && info.ID == id && info.Attention.State == session.AttentionWorking
	}
	before, letBefore := make(chan struct{}), make(chan struct{})
	at, release := make(chan struct{}), make(chan struct{})
	var onceBefore, once sync.Once
	gate := func(sess string, noted bool) {
		if sess != id || hostedSession(t, e, id).HostView().Attention.State != session.AttentionWorking {
			return
		}
		if !noted {
			onceBefore.Do(func() {
				close(before)
				<-letBefore
			})
			return
		}
		once.Do(func() {
			close(at)
			<-release
		})
	}
	e.srv.hostedSeen.seam.Store(&gate)
	t.Cleanup(func() { e.srv.hostedSeen.seam.Store(nil) })
	// The host's report of its work, as the hub takes it from the host
	// (no change forwarded first: what the server publishes then is the
	// host's change alone).
	applied := make(chan struct{})
	go func() {
		defer close(applied)
		hostedSession(t, e, id).HostAttentionKept(session.AttentionWorking, "", session.SourceAPI, "", nil, "", false, "", "", nil)
	}()
	<-before
	if got := e.trackOf(t, ref, q, "r1")[stepResumed]; got.State != stepWaiting {
		close(letBefore)
		t.Fatalf("resumed before the server noted the host's change: %+v", got)
	}
	close(letBefore)
	<-at
	for {
		select {
		case msg := <-ch:
			if working(msg) {
				close(release)
				t.Fatal("the change's event went out before the server noted how the question went on")
			}
			continue
		default:
		}
		break
	}
	if got := e.trackOf(t, ref, q, "r1")[stepResumed]; got.State != stepDone {
		close(release)
		t.Fatalf("resumed at the moment it is noted: %+v", got)
	}
	close(release)
	<-applied
	for msg := range ch {
		if working(msg) {
			break
		}
	}
	if got := e.trackOf(t, ref, q, "r1")[stepResumed]; got.State != stepDone || got.At == nil {
		t.Fatalf("resumed as the event reaches the page: %+v", got)
	}
}
