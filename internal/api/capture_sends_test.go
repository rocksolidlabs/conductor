package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A capture's sends over HTTP (Program item PV1, PR-6): to a ticket and to
// Brakeman, the desk's answers, the settlement, the cancel, the frozen
// send, and a Brakeman turn's reads of a capture. The send tests' server
// has Brakeman on with its scripted agent, its desk's loop held (a
// request asked stays queued: no turn starts) and the settlement's loop
// ended, so each test runs the passes it needs (settleCaptureSends,
// which a start runs first).

var sendOps atomic.Int64

// sendTest is the server, its capture of team's attempt build.1 and the
// capture's revision.
type sendTest struct {
	*bkEnv
	capture string
}

// The capture's moment and revision label.
var (
	sendAt      = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	sendCommit  = strings.Repeat("a", 40)
	sendContent = strings.Repeat("c", 40)
)

// newSendTest is the send tests' server: Brakeman on (bk), its desk's
// loop and the settlement held, a project CND and a capture.
func newSendTest(t *testing.T) *sendTest {
	t.Helper()
	st := &sendTest{bkEnv: brakemanEnv(t, nil)}
	st.hold(t)
	st.enable(t, "bk", nil)
	doneOf(t)(st.srv.dispatch.CreateProject(t.Context(), adminOp("Ann"), dispatch.NewProject{Key: "CND", Name: "Conductor"}))
	st.capture = storedCapture(t, st.testEnv, "team-0000aaaa", dispatch.CaptureScope{Crew: "team", Repo: st.root, Project: "CND"})
	return st
}

// hold ends the settlement's loop and the desk's (a request asked stays
// queued); the test makes each pass.
func (st *sendTest) hold(t *testing.T) {
	t.Helper()
	st.srv.stopCaptureSends()
	d := st.desk
	d.stopOnce.Do(func() { close(d.done) })
	<-d.loopDone
}

// storedCapture keeps a capture through the store: of run's attempt
// build.1 with the scope given, or, for run "", of a link.
func storedCapture(t *testing.T, e *testEnv, run string, scope dispatch.CaptureScope) string {
	t.Helper()
	vp := dispatch.Viewport{Mode: "desktop", Width: 1280, Height: 800, Scale: 1, DPR: 1, ColorScheme: "light", UserAgent: "test"}
	n := dispatch.NewCapture{Via: "web", URL: "http://127.0.0.1:5173/app", Title: "App", Viewport: vp, Epoch: 3, SelectedAt: sendAt, ImageHow: "none", ImageWhy: "the test takes none"}
	f := dispatch.CaptureFacts{Origin: dispatch.OriginURL}
	if run != "" {
		n.Label = dispatch.TreeRead{Commit: sendCommit, Content: sendContent, Changed: 1, ReadAt: sendAt}
		n.Annotations = []dispatch.NewAnnotation{{Kind: "element", Note: "The save button is hidden", Rect: dispatch.Rect{X: 10, Y: 10, Width: 80, Height: 20},
			Element: &dispatch.ElementReport{Tag: "button", Name: "Save", Selector: "button.save", Box: dispatch.Rect{X: 10, Y: 10, Width: 80, Height: 20},
				Hit: dispatch.ElementHit{Self: true, Selector: "button.save", Tag: "button"}},
			Observed: &dispatch.Observed{At: sendAt, URL: n.URL, Title: n.Title, Viewport: vp, Epoch: 3, Label: dispatch.RevisionLabel{Commit: sendCommit, Content: sendContent}}}}
		f = dispatch.CaptureFacts{Origin: dispatch.OriginAttempt, Run: run, Task: "build", Attempt: 1, Tree: dispatch.TreeWorktree, Commit: sendCommit, Content: sendContent,
			Changed: 1, ChangedPaths: []string{"web/app.vue"}, Mutable: true, Where: "native", Scope: scope}
	}
	return doneOf(t)(e.srv.dispatch.CreateCapture(t.Context(), adminOp("Ann"), strings.Repeat("a", 64), n, f, nil)).ID
}

// storedCandidate keeps a capture of run's candidate build.1 (its preview
// checkout) through the store, with the scope given.
func storedCandidate(t *testing.T, e *testEnv, run string, scope dispatch.CaptureScope) string {
	t.Helper()
	vp := dispatch.Viewport{Mode: "desktop", Width: 1280, Height: 800, Scale: 1, DPR: 1, ColorScheme: "light", UserAgent: "test"}
	n := dispatch.NewCapture{Via: "web", URL: "http://127.0.0.1:5173/app", Title: "App", Viewport: vp, Epoch: 3, SelectedAt: sendAt, ImageHow: "none", ImageWhy: "the test takes none",
		Label: dispatch.TreeRead{Commit: sendCommit, ReadAt: sendAt},
		Annotations: []dispatch.NewAnnotation{{Kind: "element", Note: "The candidate's button", Rect: dispatch.Rect{X: 10, Y: 10, Width: 80, Height: 20},
			Element: &dispatch.ElementReport{Tag: "button", Name: "Save", Selector: "button.save", Box: dispatch.Rect{X: 10, Y: 10, Width: 80, Height: 20},
				Hit: dispatch.ElementHit{Self: true, Selector: "button.save", Tag: "button"}},
			Observed: &dispatch.Observed{At: sendAt, URL: n0URL, Title: "App", Viewport: vp, Epoch: 3, Label: dispatch.RevisionLabel{Commit: sendCommit}}}}}
	f := dispatch.CaptureFacts{Origin: dispatch.OriginCandidate, Run: run, Task: "build", Attempt: 1, Tree: dispatch.TreePreview, Commit: sendCommit,
		Candidate: candName(run, "build", 1, sendCommit), Where: "native", Scope: scope}
	return doneOf(t)(e.srv.dispatch.CreateCapture(t.Context(), adminOp("Ann"), strings.Repeat("c", 64), n, f, nil)).ID
}

// n0URL is the page every stored capture is of.
const n0URL = "http://127.0.0.1:5173/app"

// rev is the capture's revision now.
func (st *sendTest) rev(t *testing.T) int64 {
	t.Helper()
	c, err := st.srv.dispatch.Capture(t.Context(), st.capture)
	if err != nil {
		t.Fatal(err)
	}
	return c.Rev
}

// sendBody is a send's body to to at the capture's revision now.
func (st *sendTest) sendBody(t *testing.T, to map[string]any) map[string]any {
	return map[string]any{"opId": fmt.Sprintf("send-%d", sendOps.Add(1)), "rev": st.rev(t), "to": to}
}

// post sends body as the admin.
func (st *sendTest) post(t *testing.T, body map[string]any) (int, map[string]any) {
	t.Helper()
	status, _, out := st.callAs(t, adminToken, "POST", "/api/captures/"+st.capture+"/send", body)
	return status, out
}

// ask sends the capture to Brakeman with the question.
func (st *sendTest) ask(t *testing.T, question string) (int, map[string]any) {
	t.Helper()
	return st.post(t, st.sendBody(t, map[string]any{"kind": "brakeman", "question": question}))
}

// printedRefs is refs as they print.
func printedRefs(refs []brakeman.Ref) []string {
	out := []string{}
	for _, r := range refs {
		out = append(out, r.String())
	}
	return out
}

// sendOf is a reply's send.
func sendOf(out map[string]any) map[string]any {
	sv, _ := out["send"].(map[string]any)
	return sv
}

// stored is the send as the store holds it.
func (st *sendTest) stored(t *testing.T, id string) dispatch.CaptureSend {
	t.Helper()
	sv, err := st.srv.dispatch.CaptureSend(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return sv
}

// requests is every request the desk keeps, oldest first.
func (st *sendTest) requests(t *testing.T) []brakeman.Request {
	t.Helper()
	page, _, err := st.desk.st.List("", brakeman.MaxPage)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(page)
	return page
}

// pending asks the capture to Brakeman while the desk does not hold its
// records, and answers the send left pending, asked nothing.
func (st *sendTest) pending(t *testing.T, question string) string {
	t.Helper()
	st.desk.recovered.Store(false)
	status, out := st.ask(t, question)
	st.desk.recovered.Store(true)
	if status != http.StatusAccepted || sendOf(out)["state"] != "pending" {
		t.Fatalf("the send: %d %v", status, out)
	}
	return sendOf(out)["id"].(string)
}

// seamAt sets the send seam to fail stage for every send (a stop there).
func (st *sendTest) seamAt(t *testing.T, stage string) {
	t.Helper()
	f := func(at, _ string) error {
		if at == stage {
			return errors.New("stopped at " + stage)
		}
		return nil
	}
	st.srv.sends.seam.Store(&f)
	t.Cleanup(func() { st.srv.sends.seam.Store(nil) })
}

// lostAdmission asks the capture to Brakeman with the sent append failing
// after the desk admitted it: the send pending, its request queued.
func (st *sendTest) lostAdmission(t *testing.T, question string) (string, brakeman.Request) {
	t.Helper()
	st.seamAt(t, "admitted")
	status, out := st.ask(t, question)
	st.srv.sends.seam.Store(nil)
	if status != http.StatusAccepted || sendOf(out)["state"] != "pending" {
		t.Fatalf("the send: %d %v", status, out)
	}
	id := sendOf(out)["id"].(string)
	req, found, err := st.desk.st.ByOp(id)
	if !found || err != nil {
		t.Fatalf("the desk holds no request of the send: %v %v", found, err)
	}
	return id, req
}

// cancel cancels the send as the admin under opID.
func (st *sendTest) cancel(t *testing.T, send, opID string) (int, map[string]any) {
	t.Helper()
	status, _, out := st.callAs(t, adminToken, "POST", "/api/captures/"+st.capture+"/sends/"+send+"/cancel", map[string]any{"opId": opID})
	return status, out
}

// cancelOf is a cancel reply's cancel.
func cancelOf(out map[string]any) map[string]any {
	c, _ := out["cancel"].(map[string]any)
	return c
}

// asked is the sends the store lists with a cancel open.
func (st *sendTest) asked(t *testing.T) []string {
	t.Helper()
	page, err := st.srv.dispatch.AskedCancels(t.Context(), "", dispatch.MaxCapturePage)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, sv := range page.Sends {
		ids = append(ids, sv.ID)
	}
	return ids
}

// restarted stops the server and starts another over its data, as a
// restart: the new server's settlement makes its first pass at once.
func (st *sendTest) restarted(t *testing.T) *sendTest {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	st.srv.Shutdown(ctx)
	return &sendTest{bkEnv: bkEnvOf(t, st.restart()), capture: st.capture}
}

// waitSend waits for the send to hold one of states in the store.
func (st *sendTest) waitSend(t *testing.T, id string, states ...string) dispatch.CaptureSend {
	t.Helper()
	var sv dispatch.CaptureSend
	if !waitFor(time.Minute, func() bool {
		var err error
		sv, err = st.srv.dispatch.CaptureSend(t.Context(), id)
		return err == nil && slices.Contains(states, sv.State)
	}) {
		t.Fatalf("the send %s is %s, not %v", id, sv.State, states)
	}
	return sv
}

// A send to Brakeman cites the document it froze: the request's subjects
// are capture:<id>/<send> and the attempt's ref, its note the question,
// its op id the send's; the send is sent to it, and its pin let go. A
// repeat is answered as it stands and asks nothing more.
func TestSendToBrakemanCitesTheCapture(t *testing.T) {
	st := newSendTest(t)
	body := st.sendBody(t, map[string]any{"kind": "brakeman", "question": "Why is the save button hidden?"})
	status, out := st.post(t, body)
	sv := sendOf(out)
	if status != http.StatusCreated || sv["state"] != "sent" || sv["kind"] != "brakeman" {
		t.Fatalf("the send: %d %v", status, out)
	}
	id := sv["id"].(string)
	reqs := st.requests(t)
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	want := []string{"capture:" + st.capture + "/" + id, "attempt:team-0000aaaa/build/1"}
	if r.ID != sv["target"] || r.OpID != id || r.Kind != brakeman.KindAsk || r.Note != "Why is the save button hidden?" || !slices.Equal(printedRefs(r.Subjects), want) {
		t.Fatalf("the request: %+v, the send %v", r, sv)
	}
	if pins, err := st.desk.st.Pinned(); err != nil || len(pins) != 0 {
		t.Fatalf("pinned after the send: %+v %v", pins, err)
	}
	var kept sendRequest
	if err := json.Unmarshal(st.stored(t, id).Request, &kept); err != nil || kept.Note != r.Note || len(kept.Subjects) != 1 || kept.Subjects[0].String() != want[1] {
		t.Fatalf("the stored request: %+v %v", kept, err)
	}
	status, again := st.post(t, body)
	if status != http.StatusOK || receiptOf(t, again)["repeat"] != true || sendOf(again)["id"] != id || len(st.requests(t)) != 1 {
		t.Fatalf("the repeat: %d %v", status, again)
	}
	body["to"].(map[string]any)["question"] = "another"
	if status, out := st.post(t, body); status != http.StatusConflict || codeOf(out) != "op_conflict" {
		t.Fatalf("another body under the opId: %d %v", status, out)
	}
}

// A send to a ticket is the store's one step: a new ticket with the
// document attached (the capture has no screenshot), sent; an existing one
// by its key; a stale revision refused.
func TestASendToATicketAttachesTheDocument(t *testing.T) {
	st := newSendTest(t)
	status, out := st.post(t, st.sendBody(t, map[string]any{"kind": "ticket", "project": "CND", "title": "The save button is hidden"}))
	sv := sendOf(out)
	if status != http.StatusCreated || sv["state"] != "sent" || len(sv["files"].([]any)) != 1 {
		t.Fatalf("the send: %d %v", status, out)
	}
	key := sv["target"].(string)
	tk, err := st.srv.dispatch.Ticket(t.Context(), key)
	if err != nil || len(tk.Attachments) != 1 || tk.Attachments[0].Name != "capture-"+st.capture+"-r1.json" {
		t.Fatalf("the ticket: %+v %v", tk, err)
	}
	if status, out := st.post(t, st.sendBody(t, map[string]any{"kind": "ticket", "key": key})); status != http.StatusCreated || sendOf(out)["target"] != key {
		t.Fatalf("to the ticket by its key: %d %v", status, out)
	}
	stale := st.sendBody(t, map[string]any{"kind": "ticket", "key": key})
	stale["rev"] = 2
	if status, out := st.post(t, stale); status != http.StatusConflict || codeOf(out) != "stale_revision" || out["receipt"] == nil {
		t.Fatalf("a stale revision: %d %v", status, out)
	}
	for name, to := range map[string]map[string]any{
		"a question to a ticket": {"kind": "ticket", "key": key, "question": "why?"},
		"no question":            {"kind": "brakeman"},
		"a question too long":    {"kind": "brakeman", "question": strings.Repeat("é", maxSendQuestion+1)},
	} {
		if status, out := st.post(t, st.sendBody(t, to)); status != http.StatusBadRequest || codeOf(out) != "invalid_request" {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
	// What the form allows the route takes: 4,000 characters in any script,
	// and as many characters a request escapes.
	for name, q := range map[string]string{"Chinese": strings.Repeat("中", maxSendQuestion), "emoji": strings.Repeat("\U0001F600", maxSendQuestion),
		"escaped": strings.Repeat("<\u2028", maxSendQuestion/2)} {
		if status, out := st.post(t, st.sendBody(t, map[string]any{"kind": "brakeman", "question": q})); status != http.StatusCreated && status != http.StatusAccepted {
			t.Errorf("a question of %d characters, %s: %d %v", maxSendQuestion, name, status, out)
		}
	}
	big := st.sendBody(t, map[string]any{"kind": "brakeman", "question": strings.Repeat("a", maxCaptureSendBody)})
	if status, out := st.post(t, big); status != http.StatusRequestEntityTooLarge || codeOf(out) != "bound" {
		t.Errorf("a body past its bound: %d %v", status, out)
	}
}

// Every answer of the desk is mapped: Brakeman off, the queue full and the
// budget spent refuse a first try with the desk's words, the send
// refused; stuck and not yet the owner leave it pending, waiting on the
// desk's words, and the settlement sends it once they pass; the queue full
// at the settlement keeps it pending.
func TestEveryDeskAnswerIsMapped(t *testing.T) {
	st := newSendTest(t)
	d := st.desk
	refused := func(name string, status int, code string) {
		t.Helper()
		got, out := st.ask(t, "Why is it hidden?")
		sv := sendOf(out)
		if got != status || codeOf(out) != code || sv["state"] != "refused" || sv["why"] != out["error"].(map[string]any)["message"] || receiptOf(t, out)["status"] != "done" {
			t.Errorf("%s: %d %v", name, got, out)
		}
	}
	st.configure(t, func(c map[string]any) { c["enabled"] = false })
	refused("Brakeman off", http.StatusConflict, "brakeman_off")
	st.enable(t, "bk", nil)
	d.mu.Lock()
	d.queue = []string{"br-91", "br-92", "br-93", "br-94", "br-95", "br-96", "br-97", "br-98"}
	d.mu.Unlock()
	refused("the queue full", http.StatusTooManyRequests, "queue_full")
	st.configure(t, func(c map[string]any) { c["budget"].(map[string]any)["turnsPerDay"] = 1 })
	d.mu.Lock()
	d.queue = []string{"br-91"}
	d.mu.Unlock()
	refused("the budget spent", http.StatusTooManyRequests, "budget_spent")
	st.configure(t, func(c map[string]any) { c["budget"].(map[string]any)["turnsPerDay"] = 40 })
	d.mu.Lock()
	d.queue, d.stuck = nil, true
	d.mu.Unlock()
	waits := func(name, words string) string {
		t.Helper()
		got, out := st.ask(t, "Why is it hidden?")
		if sv := sendOf(out); got != http.StatusAccepted || sv["state"] != "pending" || !strings.Contains(fmt.Sprint(sv["waiting"]), words) {
			t.Fatalf("%s: %d %v", name, got, out)
		}
		return sendOf(out)["id"].(string)
	}
	stuck := waits("stuck", "the last turn's run did not stop")
	d.mu.Lock()
	d.stuck = false
	d.mu.Unlock()
	d.recovered.Store(false)
	notOwner := waits("not the owner", "not this server's yet")
	d.recovered.Store(true)
	if n := len(st.requests(t)); n != 0 {
		t.Fatalf("%d requests before the settlement", n)
	}
	// At the settlement, the queue full keeps them pending with its words.
	d.mu.Lock()
	d.queue = []string{"br-91", "br-92", "br-93", "br-94", "br-95", "br-96", "br-97", "br-98"}
	d.mu.Unlock()
	st.srv.settleCaptureSends(t.Context())
	for _, id := range []string{stuck, notOwner} {
		if sv := st.stored(t, id); sv.State != "pending" || st.srv.sends.waitingOf(sv) != "8 requests wait already" {
			t.Fatalf("%s at a full queue: %s %q", id, sv.State, st.srv.sends.waitingOf(sv))
		}
	}
	d.mu.Lock()
	d.queue = nil
	d.mu.Unlock()
	st.srv.settleCaptureSends(t.Context())
	for _, id := range []string{stuck, notOwner} {
		if sv := st.stored(t, id); sv.State != "sent" || !strings.HasPrefix(sv.Target, "br-") || st.srv.sends.waitingOf(sv) != "" {
			t.Fatalf("%s after the settlement: %+v", id, sv)
		}
	}
	if status, out := st.ask(t, "And now?"); status != http.StatusCreated || sendOf(out)["state"] != "sent" {
		t.Fatalf("admitted: %d %v", status, out)
	}
	if n := len(st.requests(t)); n != 3 {
		t.Fatalf("%d requests", n)
	}
}

// A send left pending (the desk did not hold its records) is asked
// nothing while the desk does not, and is sent at the next start with the
// question it stored, never the capture's notes as they are now.
func TestALostBrakemanSendIsSettledAtStart(t *testing.T) {
	st := newSendTest(t)
	question := "What hides the save button?"
	id := st.pending(t, question)
	st.desk.recovered.Store(false)
	st.srv.settleCaptureSends(t.Context())
	st.desk.recovered.Store(true)
	if sv := st.stored(t, id); sv.State != "pending" || len(st.requests(t)) != 0 {
		t.Fatalf("settled before the desk held its records: %+v", sv)
	}
	edit := map[string]any{"opId": "edit-1", "expectedRev": 1, "annotations": []map[string]any{{"k": 1, "kind": "element", "note": "Edited after the send"}}}
	st.expect(t, "POST", "/api/captures/"+st.capture+"/edit", edit, http.StatusOK, "")
	again := st.restarted(t)
	sv := again.waitSend(t, id, "sent")
	r, err := again.desk.st.Get(sv.Target)
	if err != nil || r.Note != question || r.OpID != id || r.Subjects[0].String() != "capture:"+st.capture+"/"+id || sv.Rev != 1 {
		t.Fatalf("the request: %+v %v, the send %+v", r, err, sv)
	}
	// The restarted server's settlement commits the send sent and only
	// then lets its pin go (sendSent: MarkSent, then Release), on a
	// goroutine of its own: the state seen says nothing of the pin yet, so
	// wait for the pin itself to go.
	var pins []brakeman.PinnedOp
	if !waitFor(testWait, func() bool {
		var err error
		pins, err = again.desk.st.Pinned()
		return err == nil && len(pins) == 0
	}) {
		t.Fatalf("pinned after %s: %+v", testWait, pins)
	}
}

// A try held at the desk's door with the send's gate taken: the cancel's
// intent is committed at once and its steps wait for the gate; let go,
// the request is admitted and sent, then the cancel cancels it at
// Brakeman: one request in all, cancelled, the cancel done.
func TestACancelHeldBeforeTheDeskCall(t *testing.T) {
	st := newSendTest(t)
	id := st.pending(t, "Why?")
	at, release := make(chan struct{}), make(chan struct{})
	f := func(stage, send string) error {
		if stage == "try" && send == id {
			close(at)
			<-release
		}
		return nil
	}
	st.srv.sends.seam.Store(&f)
	defer st.srv.sends.seam.Store(nil)
	passed := make(chan struct{})
	go func() {
		defer close(passed)
		st.srv.settleCaptureSends(context.Background())
	}()
	<-at
	type answer struct {
		status int
		out    map[string]any
	}
	cancelled := make(chan answer, 1)
	go func() {
		status, _, out := st.callAs(t, adminToken, "POST", "/api/captures/"+st.capture+"/sends/"+id+"/cancel", map[string]any{"opId": "cancel-1"})
		cancelled <- answer{status, out}
	}()
	if !waitFor(time.Minute, func() bool { return len(st.stored(t, id).Cancels) == 1 && st.srv.sends.gates.queued(id) == 2 }) {
		t.Fatalf("the cancel's intent is not kept while the try holds the gate: %+v", st.stored(t, id))
	}
	if sv := st.stored(t, id); sv.State != "pending" {
		t.Fatalf("the send while held: %+v", sv)
	}
	close(release)
	a := <-cancelled
	<-passed
	if a.status != http.StatusOK || cancelOf(a.out)["done"] != true || cancelOf(a.out)["asked"] != true || cancelOf(a.out)["words"] != nil {
		t.Fatalf("the cancel: %d %v", a.status, a.out)
	}
	reqs := st.requests(t)
	if sv := st.stored(t, id); sv.State != "sent" || len(reqs) != 1 || reqs[0].ID != sv.Target || reqs[0].State != brakeman.StateCancelled {
		t.Fatalf("the send %+v, the requests %+v", sv, reqs)
	}
	if ids := st.asked(t); len(ids) != 0 {
		t.Fatalf("cancels still open: %v", ids)
	}
}

// A lost admission: the cancel commits its intent and sends the send to
// the request the desk admitted, and stops before the desk's cancel; the
// next start's settlement cancels the request, and the cancel sent again
// is answered by its receipt, done. A stop right after the intent of
// another: the next start's settlement cancels the send.
func TestACancelSurvivesAStopBeforeTheDeskCancel(t *testing.T) {
	st := newSendTest(t)
	id, req := st.lostAdmission(t, "Why?")
	st.seamAt(t, "desk_cancel")
	status, out := st.cancel(t, id, "cancel-1")
	if status != http.StatusOK || cancelOf(out)["done"] != false || sendOf(out)["state"] != "sent" || sendOf(out)["target"] != req.ID {
		t.Fatalf("the cancel stopped before the desk: %d %v", status, out)
	}
	st.srv.sends.seam.Store(nil)
	other := st.pending(t, "And this?")
	st.seamAt(t, "intent")
	if status, out := st.cancel(t, other, "cancel-2"); status != http.StatusOK || cancelOf(out)["done"] != false || sendOf(out)["state"] != "pending" {
		t.Fatalf("the cancel stopped after its intent: %d %v", status, out)
	}
	again := st.restarted(t)
	if !waitFor(time.Minute, func() bool { return len(again.asked(t)) == 0 }) {
		t.Fatalf("cancels still open after the start: %v", again.asked(t))
	}
	if r, err := again.desk.st.Get(req.ID); err != nil || r.State != brakeman.StateCancelled {
		t.Fatalf("the request after the start: %+v %v", r, err)
	}
	if sv := again.stored(t, other); sv.State != "cancelled" {
		t.Fatalf("the send stopped after its intent: %+v", sv)
	}
	if n := len(again.requests(t)); n != 1 {
		t.Fatalf("%d requests", n)
	}
	status, out = again.cancel(t, id, "cancel-1")
	if status != http.StatusOK || receiptOf(t, out)["repeat"] != true || cancelOf(out)["done"] != true {
		t.Fatalf("the cancel again: %d %v", status, out)
	}
}

// A request answered before the cancel reached it: nothing is closed, the
// cancel is done, with the words.
func TestACancelAfterTheAnswerIsDone(t *testing.T) {
	st := newSendTest(t)
	status, out := st.ask(t, "Why?")
	if status != http.StatusCreated {
		t.Fatalf("the send: %d %v", status, out)
	}
	id, target := sendOf(out)["id"].(string), sendOf(out)["target"].(string)
	for _, state := range []string{brakeman.StateStarting, brakeman.StateEnded} {
		if _, err := st.desk.st.Update(target, func(r *brakeman.Request) error { return r.To(state, "the turn ended", time.Now()) }); err != nil {
			t.Fatal(err)
		}
	}
	status, out = st.cancel(t, id, "cancel-1")
	if c := cancelOf(out); status != http.StatusOK || c["done"] != true || c["words"] != finishedFirst {
		t.Fatalf("the cancel: %d %v", status, out)
	}
	if r, err := st.desk.st.Get(target); err != nil || r.State != brakeman.StateEnded {
		t.Fatalf("the request: %+v %v", r, err)
	}
	if ids := st.asked(t); len(ids) != 0 {
		t.Fatalf("cancels still open: %v", ids)
	}
}

// A cancel before a try: the send is cancelled, and the try finds it so
// and asks nothing; a cancel whose steps stopped after its intent leaves
// the send pending with its cancel asked, and a try asks nothing either.
func TestACancelBeforeATryStopsIt(t *testing.T) {
	st := newSendTest(t)
	id := st.pending(t, "Why?")
	st.desk.recovered.Store(false)
	status, out := st.cancel(t, id, "cancel-1")
	st.desk.recovered.Store(true)
	if status != http.StatusOK || cancelOf(out)["done"] != true || sendOf(out)["state"] != "cancelled" {
		t.Fatalf("the cancel: %d %v", status, out)
	}
	if _, err := st.srv.trySend(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	other := st.pending(t, "And this?")
	st.seamAt(t, "intent")
	if status, out := st.cancel(t, other, "cancel-2"); status != http.StatusOK || cancelOf(out)["done"] != false {
		t.Fatalf("the cancel stopped after its intent: %d %v", status, out)
	}
	if _, err := st.srv.trySend(t.Context(), other, false); err != nil {
		t.Fatal(err)
	}
	if sv := st.stored(t, other); sv.State != "pending" || len(st.requests(t)) != 0 {
		t.Fatalf("a try asked the desk: %+v, %d requests", sv, len(st.requests(t)))
	}
}

// Admitted, the sent append failed: the cancel finds the request by its
// op id, sends the send to it and cancels it.
func TestACancelAfterALostAdmission(t *testing.T) {
	st := newSendTest(t)
	id, req := st.lostAdmission(t, "Why?")
	status, out := st.cancel(t, id, "cancel-1")
	if status != http.StatusOK || cancelOf(out)["done"] != true || sendOf(out)["state"] != "sent" || sendOf(out)["target"] != req.ID {
		t.Fatalf("the cancel: %d %v", status, out)
	}
	reqs := st.requests(t)
	if len(reqs) != 1 || reqs[0].State != brakeman.StateCancelled {
		t.Fatalf("the requests: %+v", reqs)
	}
	if pins, err := st.desk.st.Pinned(); err != nil || len(pins) != 0 {
		t.Fatalf("pinned: %+v %v", pins, err)
	}
}

// A cancel is idempotent by its op id: the same again answers its receipt,
// done; another cancels nothing more. A send to a ticket and a refused
// one take none (409 stale_revision, the state named); a send of another
// capture is not found.
func TestACancelIsIdempotent(t *testing.T) {
	st := newSendTest(t)
	id := st.pending(t, "Why?")
	first := func(status int, out map[string]any) map[string]any {
		t.Helper()
		if status != http.StatusOK || cancelOf(out)["done"] != true || sendOf(out)["state"] != "cancelled" {
			t.Fatalf("the cancel: %d %v", status, out)
		}
		return receiptOf(t, out)
	}
	rc := first(st.cancel(t, id, "cancel-1"))
	status, out := st.cancel(t, id, "cancel-1")
	if again := receiptOf(t, out); status != http.StatusOK || again["repeat"] != true || again["n"] != rc["n"] || cancelOf(out)["done"] != true {
		t.Fatalf("the same cancel again: %d %v", status, out)
	}
	if status, out := st.cancel(t, id, "cancel-2"); status != http.StatusConflict || codeOf(out) != "stale_revision" || !strings.Contains(fmt.Sprint(out["error"]), "cancelled") {
		t.Fatalf("another cancel of a cancelled send: %d %v", status, out)
	}
	_, tk := st.post(t, st.sendBody(t, map[string]any{"kind": "ticket", "project": "CND", "title": "Hidden"}))
	if status, out := st.cancel(t, sendOf(tk)["id"].(string), "cancel-3"); status != http.StatusConflict || codeOf(out) != "stale_revision" {
		t.Fatalf("a send to a ticket: %d %v", status, out)
	}
	st.configure(t, func(c map[string]any) { c["enabled"] = false })
	_, off := st.ask(t, "Why?")
	if status, out := st.cancel(t, sendOf(off)["id"].(string), "cancel-4"); status != http.StatusConflict || codeOf(out) != "stale_revision" || !strings.Contains(fmt.Sprint(out["error"]), "refused") {
		t.Fatalf("a refused send: %d %v", status, out)
	}
	elsewhere := storedCapture(t, st.testEnv, "team-0000aaaa", dispatch.CaptureScope{Crew: "team"})
	if status, _, out := st.callAs(t, adminToken, "POST", "/api/captures/"+elsewhere+"/sends/"+id+"/cancel", map[string]any{"opId": "cancel-5"}); status != http.StatusNotFound || codeOf(out) != "not_found" {
		t.Fatalf("a send of another capture: %d %v", status, out)
	}
}

// Admitted, MarkSent failed, more requests made than the desk's op window
// holds (a window of three, set on the desk's store): the settlement
// finds the request by the send's pinned op id, makes no second, sends the
// send to it and lets the pin go.
func TestARecoveredSendOutlivesTheOpWindow(t *testing.T) {
	st := newSendTest(t)
	// A window of three op ids in place of 1,000: four later requests pass
	// it as a thousand and one would.
	const window = 3
	st.desk.st.SetOpsWindow(window)
	id, req := st.lostAdmission(t, "Why?")
	for i := range window + 1 {
		if _, _, err := st.desk.st.Create(brakeman.Request{OpID: fmt.Sprintf("later-%d", i), Kind: brakeman.KindBriefing}); err != nil {
			t.Fatal(err)
		}
	}
	st.srv.settleCaptureSends(t.Context())
	if sv := st.stored(t, id); sv.State != "sent" || sv.Target != req.ID {
		t.Fatalf("the send: %+v", sv)
	}
	if _, err := st.desk.st.Get(fmt.Sprintf("br-%d", window+3)); !errors.Is(err, brakeman.ErrNotFound) {
		t.Fatalf("a second request: %v", err)
	}
	// The window was passed: an op id that is not pinned, the oldest later
	// one, is no longer held.
	if _, found, err := st.desk.st.ByOp("later-0"); found || err != nil {
		t.Fatalf("the window was not passed: %v %v", found, err)
	}
	if pins, err := st.desk.st.Pinned(); err != nil || len(pins) != 0 {
		t.Fatalf("pinned: %+v %v", pins, err)
	}
}

// Admitted, MarkSent failed, the request closed and pruned at the desk:
// the settlement sends the send to the pruned request's number and never
// asks again.
func TestAPrunedRequestSettlesTheSend(t *testing.T) {
	st := newSendTest(t)
	id, req := st.lostAdmission(t, "Why?")
	if _, err := st.desk.closeTurn(req.ID, brakeman.StateCancelled, "closed by the test"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.desk.st.PruneKeeping(0); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	st.srv.settleCaptureSends(t.Context())
	st.srv.settleCaptureSends(t.Context())
	if sv := st.stored(t, id); sv.State != "sent" || sv.Target != req.ID {
		t.Fatalf("the send: %+v", sv)
	}
	if reqs := st.requests(t); len(reqs) != 0 {
		t.Fatalf("asked again: %+v", reqs)
	}
	if pins, err := st.desk.st.Pinned(); err != nil || len(pins) != 0 {
		t.Fatalf("pinned: %+v %v", pins, err)
	}
}

// The send, cancel and frozen-send routes are the admin's: none, a
// session's, a host's and a share link's tokens are 401, and a Brakeman
// turn's on the two writes; a switchyard answers 403. The frozen send is
// a turn's shared read (TestBrakemanReadsACaptureUnderItsDispatchClass).
func TestCaptureSendRoutesAreAdminOnly(t *testing.T) {
	b := brakemanEnv(t, nil)
	sid := b.createSession("cat")
	tokens := map[string]string{"none": "", "session": b.agentToken(sid), "host": "test-host-token"}
	for _, role := range []string{"view", "control"} {
		status, data, out := b.callAs(t, adminToken, "POST", "/api/sessions/"+sid+"/links", map[string]any{"role": role})
		if status != http.StatusCreated {
			t.Fatalf("a %s link: %d %s", role, status, data)
		}
		tokens["link "+role] = out["token"].(string)
	}
	b.enable(t, "bk", nil)
	bsid, btok, _ := b.turnSession(t, b.briefing(t))
	id, send := strings.Repeat("a", 16), strings.Repeat("b", 16)
	routes := [][2]string{{"POST", "/api/captures/" + id + "/send"}, {"POST", "/api/captures/" + id + "/sends/" + send + "/cancel"}, {"GET", "/api/captures/" + id + "/sends/" + send}}
	for _, r := range routes {
		var body any
		if r[0] == "POST" {
			body = map[string]any{"opId": "x"}
		}
		for who, tok := range tokens {
			if status, data, _ := b.callAs(t, tok, r[0], r[1], body); status != http.StatusUnauthorized {
				t.Errorf("%s %s with %s: %d %s", r[0], r[1], who, status, data)
			}
		}
		if r[0] == "POST" {
			raw, _ := json.Marshal(body)
			if status, data, _ := b.callAsTurn(t, btok, bsid, r[0], r[1], raw); status != http.StatusUnauthorized {
				t.Errorf("%s %s as a Brakeman turn: %d %s", r[0], r[1], status, data)
			}
		}
	}
	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	for _, r := range routes {
		if status, data, out := yard.callAs(t, adminToken, r[0], r[1], nil); status != http.StatusForbidden || codeOf(out) != "switchyard" {
			t.Errorf("%s %s on a switchyard: %d %s", r[0], r[1], status, data)
		}
	}
}

// A Brakeman send's question holding a tree's secret is kept scrubbed: in
// the send's stored request and in the desk's request, the secret's name
// in its place; the same body again after the tree ended is answered by
// its receipt; no byte of Dispatch's or Brakeman's records holds it.
func TestABrakemanSendHoldsNoTreesSecret(t *testing.T) {
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
		append([]catalog.Agent{{ID: "ready-cat", Name: "ready cat", Command: []string{"/bin/sh", "-c", "echo ready; exec cat"}}}, bkAgents()...))
	e.gitRepo(t)
	p := launchPreview(t, e, previewedCrew(e.root))
	st := &sendTest{bkEnv: bkEnvOf(t, e)}
	st.hold(t)
	st.enable(t, "bk", nil)
	st.capture = created(t, e, p.body(t, p.label(t), button()), nil)["id"].(string)
	body := st.sendBody(t, map[string]any{"kind": "brakeman", "question": "the key " + strings.ToUpper(p.secret) + " leaks"})
	status, out := st.post(t, body)
	if status != http.StatusCreated {
		t.Fatalf("the send: %d %v", status, out)
	}
	id := sendOf(out)["id"].(string)
	r := st.requests(t)[0]
	if !strings.Contains(r.Note, treeSecretName) || strings.Contains(strings.ToLower(r.Note), p.secret) || !strings.Contains(string(st.stored(t, id).Request), treeSecretName) {
		t.Fatalf("the request: %q, the stored one %s", r.Note, st.stored(t, id).Request)
	}
	if resp, out := e.do("POST", "/api/runs/"+p.run+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	if status, out := st.post(t, body); status != http.StatusOK || receiptOf(t, out)["repeat"] != true || sendOf(out)["id"] != id {
		t.Fatalf("the send again after the tree ended: %d %v", status, out)
	}
	p.keptNowhere(t, p.secret)
	if found := hasSecret(t, filepath.Join(e.srv.store.Dir(), "brakeman"), p.secret); len(found) > 0 {
		t.Fatalf("a tree's secret is in %v", found)
	}
}

// ---- a Brakeman turn's reads of a capture

// obsCapture keeps, in the observe fixture, a capture of run with the
// project it recorded, and sends it to the CND ticket; it answers the
// capture and the send.
func (f *obsFixture) obsCapture(t *testing.T, run, project string) (string, string) {
	t.Helper()
	id := storedCapture(t, f.testEnv, run, dispatch.CaptureScope{Crew: crewOfRun(run), Project: project})
	rc := doneOf(t)(f.srv.dispatch.SendCapture(t.Context(), adminOp("Ann"), strings.Repeat("e", 64), id, 1, dispatch.Destination{Kind: "ticket", Key: f.ticket}, dispatch.SendFacts{}))
	return id, rc.ID
}

// The frozen send reads the same after the capture's edit and its delete:
// the document it froze at r1, noted with the send's digest; the live
// capture reads r2, then its tombstone, noted deleted.
func TestBrakemanReadsTheFrozenSendAcrossEditAndDelete(t *testing.T) {
	f := newObsFixture(t)
	id, send := f.obsCapture(t, f.run, "CND")
	f.turn(t, nil)
	frozen := "/api/captures/" + id + "/sends/" + send
	first := f.expectRead(t, frozen, http.StatusOK, "")
	edit := map[string]any{"opId": "edit-1", "expectedRev": 1, "annotations": []map[string]any{{"k": 1, "kind": "element", "note": "Edited after the send"}}}
	f.expect(t, "POST", "/api/captures/"+id+"/edit", edit, http.StatusOK, "")
	live := f.expectRead(t, "/api/captures/"+id, http.StatusOK, "")
	if live["rev"] != 2.0 || live["ref"] != "capture:"+id || !strings.Contains(fmt.Sprint(live["annotations"]), "Edited after the send") {
		t.Fatalf("the live capture: %v", live)
	}
	if sends := live["sends"].([]any); len(sends) != 1 || sends[0].(map[string]any)["ref"] != "capture:"+id+"/"+send || sends[0].(map[string]any)["target"] != nil {
		t.Fatalf("the live capture's sends: %v", sends)
	}
	afterEdit := f.expectRead(t, frozen, http.StatusOK, "")
	f.expect(t, "POST", "/api/captures/"+id+"/delete", map[string]any{"opId": "delete-1"}, http.StatusOK, "")
	afterDelete := f.expectRead(t, frozen, http.StatusOK, "")
	for name, out := range map[string]map[string]any{"after the edit": afterEdit, "after the delete": afterDelete} {
		if fmt.Sprint(out["document"]) != fmt.Sprint(first["document"]) || out["ref"] != "capture:"+id+"/"+send {
			t.Fatalf("the frozen send %s: %v, first %v", name, out, first)
		}
	}
	doc := first["document"].(map[string]any)
	if doc["rev"] != 1.0 || !strings.Contains(fmt.Sprint(doc["annotations"]), "The save button is hidden") || first["send"].(map[string]any)["asked"] != nil {
		t.Fatalf("the document: %v", first)
	}
	sv, _ := f.srv.dispatch.CaptureSend(t.Context(), send)
	if rd, ok := f.readOf(t, "capture:"+id+"/"+send); !ok || rd.Rev != "1" || rd.SHA256 != sv.Digest || rd.Class != brakeman.ClassDispatch {
		t.Fatalf("the frozen read noted: %+v %v", rd, ok)
	}
	tomb := f.expectRead(t, "/api/captures/"+id, http.StatusOK, "")
	if tomb["deletedAt"] == nil || tomb["annotations"] != nil {
		t.Fatalf("the tombstone: %v", tomb)
	}
	if rd, ok := f.readOf(t, "capture:"+id); !ok || rd.State != refDeleted {
		t.Fatalf("the tombstone noted: %+v %v", rd, ok)
	}
	f.submitFact(t, "capture:"+id+"/"+send)
}

// A turn reads a capture under Dispatch's class: refused without it
// (403 not_visible) on both reads, and its guide lists neither; with it,
// the capture names its screenshot's size and hash, never its bytes.
func TestBrakemanReadsACaptureUnderItsDispatchClass(t *testing.T) {
	f := newObsFixture(t)
	id, send := f.obsCapture(t, f.run, "CND")
	f.turn(t, func(c map[string]any) { c["visibility"].(map[string]any)["dispatch"] = false })
	for _, path := range []string{"/api/captures/" + id, "/api/captures/" + id + "/sends/" + send} {
		f.expectRead(t, path, http.StatusForbidden, "not_visible")
	}
	_, _, guide := guideOf(t, f.testEnv, f.tok, "/api/sessions/"+f.sid+"/guide?section=operations")
	if names := opNames(t, guide); slices.Contains(names, "observe_capture") || slices.Contains(names, "observe_capture_send") {
		t.Fatalf("the guide without dispatch lists the capture reads: %v", names)
	}
	f.again(t, func(c map[string]any) { c["visibility"].(map[string]any)["dispatch"] = true })
	out := f.expectRead(t, "/api/captures/"+id, http.StatusOK, "")
	img := out["image"].(map[string]any)
	if img["how"] != "none" || out["ref"] != "capture:"+id {
		t.Fatalf("the capture: %v", out)
	}
	resp, raw := f.readRaw(t, "/api/captures/"+id+"/image")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the image as a turn: %d %.200s", resp.StatusCode, raw)
	}
}

// Admission follows the scope, never the sends: under a grant of project
// CND a capture of team's run (CND) is read, one of other's (OTH) refused
// though it was sent to CND's ticket, a link's capture refused (it is of
// no project), and a capture of a run gone is admitted by what it
// recorded; once that capture is deleted, its tombstone keeps no scope
// and its frozen send is refused under the project (fail closed); a scope
// narrowed between two reads refuses the second; an unscoped grant reads
// the link's and the deleted one's frozen send.
func TestCaptureAdmissionFollowsTheScope(t *testing.T) {
	f := newObsFixture(t)
	team, teamSend := f.obsCapture(t, f.run, "CND")
	other, otherSend := f.obsCapture(t, f.other, "OTH")
	link, _ := f.obsCapture(t, "", "")
	gone, goneSend := f.obsCapture(t, "gone-0000cccc", "CND")
	goneElsewhere, _ := f.obsCapture(t, "gone-0000dddd", "OTH")
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"projects": []string{"CND"}} })
	for path, status := range map[string]int{
		"/api/captures/" + team:                          http.StatusOK,
		"/api/captures/" + team + "/sends/" + teamSend:   http.StatusOK,
		"/api/captures/" + other:                         http.StatusForbidden,
		"/api/captures/" + other + "/sends/" + otherSend: http.StatusForbidden,
		"/api/captures/" + link:                          http.StatusForbidden,
		"/api/captures/" + gone:                          http.StatusOK,
		"/api/captures/" + goneElsewhere:                 http.StatusForbidden,
	} {
		code := ""
		if status == http.StatusForbidden {
			code = "outside_scope"
		}
		f.expectRead(t, path, status, code)
	}
	f.expect(t, "POST", "/api/captures/"+gone+"/delete", map[string]any{"opId": "gone-delete"}, http.StatusOK, "")
	f.expectRead(t, "/api/captures/"+gone+"/sends/"+goneSend, http.StatusForbidden, "outside_scope")
	// Narrowed between two reads: a run's scope the capture is not of.
	f.configure(t, func(c map[string]any) {
		c["scope"] = map[string]any{"projects": []string{"CND"}, "runs": []string{f.other}}
	})
	f.expectRead(t, "/api/captures/"+team, http.StatusForbidden, "outside_scope")
	f.again(t, func(c map[string]any) { c["scope"] = map[string]any{} })
	f.expectRead(t, "/api/captures/"+link, http.StatusOK, "")
	f.expectRead(t, "/api/captures/"+other, http.StatusOK, "")
	f.expectRead(t, "/api/captures/"+gone+"/sends/"+goneSend, http.StatusOK, "")
}

// A candidate's capture (its preview checkout) is admitted by its run as
// an attempt's is, never as a link's: under a grant of project CND, a
// candidate capture of team's run (CND) and its frozen send are read, and
// one of other's run (OTH) is refused; a send of it to Brakeman asks with
// the attempt's ref.
func TestACandidatesCaptureIsAdmittedByItsRun(t *testing.T) {
	f := newObsFixture(t)
	cand := storedCandidate(t, f.testEnv, f.run, dispatch.CaptureScope{Crew: crewOfRun(f.run), Project: "CND"})
	elsewhere := storedCandidate(t, f.testEnv, f.other, dispatch.CaptureScope{Crew: crewOfRun(f.other), Project: "OTH"})
	send := doneOf(t)(f.srv.dispatch.SendCapture(t.Context(), adminOp("Ann"), strings.Repeat("e", 64), cand, 1, dispatch.Destination{Kind: "ticket", Key: f.ticket}, dispatch.SendFacts{})).ID
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"projects": []string{"CND"}} })
	f.expectRead(t, "/api/captures/"+cand, http.StatusOK, "")
	f.expectRead(t, "/api/captures/"+cand+"/sends/"+send, http.StatusOK, "")
	f.expectRead(t, "/api/captures/"+elsewhere, http.StatusForbidden, "outside_scope")
	req, err := f.srv.sendRequestOf(t.Context(), f.srv.dispatch, cand, "Why?", "")
	if err != nil || !strings.Contains(string(req), `"attempt:`+f.run+`/build/1"`) {
		t.Fatalf("the request of a candidate's capture: %s %v", req, err)
	}
}

// The guide carries the two reads of a capture, observe_capture and
// observe_capture_send, under Dispatch's class, and the observe tool
// reads them by what.
func TestTheGuideCarriesCaptureRead(t *testing.T) {
	ops := Operations()
	for name, path := range map[string]string{"observe_capture": "/api/captures/{id}", "observe_capture_send": "/api/captures/{id}/sends/{send}"} {
		i := slices.IndexFunc(ops, func(o Operation) bool { return o.Name == name })
		if i < 0 {
			t.Fatalf("no %s", name)
		}
		op := ops[i]
		props := op.Input["properties"].(map[string]any)
		if op.Method != http.MethodGet || op.Path != path || op.Scope != "observe" || !slices.Equal(opClasses(op), []string{brakeman.ClassDispatch}) ||
			props["id"] == nil || (strings.Contains(path, "{send}") != (props["send"] != nil)) || op.Bounds["reply"] != maxTurnReply {
			t.Fatalf("%s: %s", name, mustJSON(op))
		}
	}
	if !slices.Contains(observeWhats, "capture") || !slices.Contains(observeWhats, "capture_send") {
		t.Fatalf("the observe tool's whats: %v", observeWhats)
	}
	if _, ok := compareDoors[captureSendRead]; !ok {
		t.Fatal("the frozen send has no door")
	}
}
