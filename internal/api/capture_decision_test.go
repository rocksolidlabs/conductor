package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A capture attached to a decision (PV1 PR-11): a comment on a DR1 record
// made with the send's id as its operation's id, and the send this item's
// row. Each test's decision is dr-1, an approval its attempt asks of the
// managed run of newDecisionFixture, and its capture one of that run's
// attempt kept through the store.

// decisionSend is the fixture with its decision and capture.
type decisionSend struct {
	*decisionFixture
	capture string
}

func newDecisionSend(t *testing.T) *decisionSend {
	t.Helper()
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.srv.stopCaptureSends()
	return &decisionSend{decisionFixture: f, capture: storedCapture(t, f.testEnv, f.run, dispatch.CaptureScope{Crew: crewOfRun(f.run)})}
}

// post sends the capture at r1 to the decision as the admin.
func (d *decisionSend) post(t *testing.T, decision string) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"opId": fmt.Sprintf("dsend-%d", sendOps.Add(1)), "rev": 1, "to": map[string]any{"kind": "decision", "decision": decision}}
	status, _, out := d.callAs(t, adminToken, "POST", "/api/captures/"+d.capture+"/send", body)
	return status, out
}

// comments is the comments decision dr-1's history keeps, by their text.
func (d *decisionSend) comments(t *testing.T) []string {
	t.Helper()
	rec, err := d.srv.runs.Decision(t.Context(), "dr-1")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, h := range rec.History {
		if h.Kind == "commented" {
			out = append(out, string(h.Detail))
		}
	}
	return out
}

// seamAt fails the stage for every send, as a stop there.
func (d *decisionSend) seamAt(t *testing.T, stage string) {
	t.Helper()
	f := func(at, _ string) error {
		if at == stage {
			return errors.New("stopped at " + stage)
		}
		return nil
	}
	d.srv.sends.seam.Store(&f)
	t.Cleanup(func() { d.srv.sends.seam.Store(nil) })
}

// A send to a decision is a comment on it, naming the capture, its
// revision and the send's ref and none of the page's text, and a row of
// this item's: sent to the decision, listed by the decision's captures; a
// decision that is not there is refused with DR1's words, and the send
// cannot be cancelled.
func TestADecisionSendIsACommentAndARow(t *testing.T) {
	d := newDecisionSend(t)
	status, out := d.post(t, "dr-1")
	sv := sendOf(out)
	if status != http.StatusCreated || sv["state"] != "sent" || sv["target"] != "dr-1" || sv["kind"] != "decision" {
		t.Fatalf("the send: %d %v", status, out)
	}
	id := sv["id"].(string)
	got := d.comments(t)
	ref := "capture:" + d.capture + "/" + id
	if len(got) != 1 || !strings.Contains(got[0], ref) || !strings.Contains(got[0], "Capture "+d.capture+" is attached at revision 1") || strings.Contains(got[0], "save button") {
		t.Fatalf("the comments: %v", got)
	}
	listed := d.expect(t, "GET", "/api/decisions/dr-1/captures", nil, http.StatusOK, "")
	if sends := listed["sends"].([]any); len(sends) != 1 || sends[0].(map[string]any)["id"] != id || sends[0].(map[string]any)["request"] != nil {
		t.Fatalf("the decision's captures: %v", listed)
	}
	d.expect(t, "GET", "/api/decisions/dr-0/captures", nil, http.StatusBadRequest, "invalid_request")
	if status, _, out := d.callAs(t, adminToken, "POST", "/api/captures/"+d.capture+"/sends/"+id+"/cancel", map[string]any{"opId": "c-1"}); status != http.StatusConflict || codeOf(out) != "stale_revision" {
		t.Fatalf("a cancel: %d %v", status, out)
	}
	status, out = d.post(t, "dr-9")
	if status != http.StatusNotFound || codeOf(out) != crew.CodeNotFound || sendOf(out)["state"] != "refused" {
		t.Fatalf("a decision that is not there: %d %v", status, out)
	}
	for who, tok := range map[string]string{"none": "", "the attempt's agent": d.tok} {
		if status, _, _ := d.callAs(t, tok, "GET", "/api/decisions/dr-1/captures", nil); status != http.StatusUnauthorized {
			t.Errorf("the decision's captures with %s: %d", who, status)
		}
	}
}

// A send lost before its comment (the server stopped before DR1 was
// called): pending, no comment; the settlement makes the one comment.
func TestADecisionSendLostBeforeItsComment(t *testing.T) {
	d := newDecisionSend(t)
	d.seamAt(t, "try")
	status, out := d.post(t, "dr-1")
	if status != http.StatusAccepted || sendOf(out)["state"] != "pending" || len(d.comments(t)) != 0 {
		t.Fatalf("the send: %d %v, comments %v", status, out, d.comments(t))
	}
	d.srv.sends.seam.Store(nil)
	d.srv.settleCaptureSends(t.Context())
	id := sendOf(out)["id"].(string)
	if sv, err := d.srv.dispatch.CaptureSend(t.Context(), id); err != nil || sv.State != "sent" || sv.Target != "dr-1" {
		t.Fatalf("the send settled: %+v %v", sv, err)
	}
	if got := d.comments(t); len(got) != 1 {
		t.Fatalf("the comments: %v", got)
	}
}

// A send lost after its comment (the sent append never made): the
// settlement finds DR1's receipt for the send's id, makes no second
// comment, and sends it.
func TestADecisionSendLostAfterItsComment(t *testing.T) {
	d := newDecisionSend(t)
	d.seamAt(t, "admitted")
	status, out := d.post(t, "dr-1")
	if status != http.StatusAccepted || sendOf(out)["state"] != "pending" || len(d.comments(t)) != 1 {
		t.Fatalf("the send: %d %v, comments %v", status, out, d.comments(t))
	}
	d.srv.sends.seam.Store(nil)
	d.srv.settleCaptureSends(t.Context())
	d.srv.settleCaptureSends(t.Context())
	id := sendOf(out)["id"].(string)
	if sv, err := d.srv.dispatch.CaptureSend(t.Context(), id); err != nil || sv.State != "sent" {
		t.Fatalf("the send settled: %+v %v", sv, err)
	}
	if got := d.comments(t); len(got) != 1 {
		t.Fatalf("one comment, not %v", got)
	}
}

// A send that races an answer: the decision is answered between the
// send's read of its rev and its comment; the comment is refused stale,
// the rev read again, and the same comment lands on the answered decision.
func TestASendRacingAnAnswerLandsAsAComment(t *testing.T) {
	d := newDecisionSend(t)
	var answered atomic.Bool
	f := func(stage, _ string) error {
		if stage == "decision_read" && answered.CompareAndSwap(false, true) {
			open := d.decisionWhen(t, "dr-1", "open", inState("open"))
			d.respond(t, adminToken, "dr-1", "race-1", 0, int(open["rev"].(float64)), http.StatusCreated, "")
		}
		return nil
	}
	d.srv.sends.seam.Store(&f)
	defer d.srv.sends.seam.Store(nil)
	status, out := d.post(t, "dr-1")
	if status != http.StatusCreated || sendOf(out)["state"] != "sent" || !answered.Load() {
		t.Fatalf("the send: %d %v", status, out)
	}
	rec, err := d.srv.runs.Decision(t.Context(), "dr-1")
	if err != nil || rec.State == "open" {
		t.Fatalf("the decision: %+v %v", rec.State, err)
	}
	if got := d.comments(t); len(got) != 1 || !strings.Contains(got[0], sendOf(out)["id"].(string)) {
		t.Fatalf("the comments: %v", got)
	}
}
