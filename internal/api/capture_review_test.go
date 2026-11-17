package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A capture as a review's finding and Request a change (PR-10, #798):
// a capture of a candidate's preview checkout sent to the review of that
// candidate, its finding filled by the server, its document and screenshot
// handed to the repair attempt as evidence files.

// testPNG is a small screenshot.
func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	img.Pix[0] = 7
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// capturedOf is a capture of the candidate run/build/1@commit as the
// pane makes it: one element (a Save button an overlay covers) with note,
// its source traced to SaveBar.vue line 12, and the screenshot when shot
// is set; big puts the note and the element's report at their bounds.
func capturedOf(run, commit, note string, shot []byte, big bool) (dispatch.NewCapture, dispatch.CaptureFacts) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	vp := dispatch.Viewport{Mode: "desktop", Width: 1280, Height: 800, Scale: 0.5, DPR: 2, ColorScheme: "light", UserAgent: "Chromium/141"}
	el := &dispatch.ElementReport{Tag: "button", Name: "Save", Selector: "button.save", Box: dispatch.Rect{X: 1, Y: 1, Width: 4, Height: 3},
		Hit: dispatch.ElementHit{Selector: "div.overlay", Tag: "div"}}
	if big {
		el.Name = strings.Repeat("n", dispatch.MaxElementName-8)
		el.Selector = strings.Repeat("s", dispatch.MaxSelector-8)
		el.Text = strings.Repeat("t", dispatch.MaxElementText-8)
		el.HTML = strings.Repeat("<b>h</b>\n", (dispatch.MaxElementHTML-16)/9)
		el.Neighbourhood = strings.Repeat("x", 15<<10)
	}
	n := dispatch.NewCapture{Via: dispatch.CaptureViaWeb, URL: "http://127.0.0.1:5173/app", Title: "App", Viewport: vp, Epoch: 3,
		Label: dispatch.TreeRead{Commit: commit, ReadAt: at}, SelectedAt: at,
		Annotations: []dispatch.NewAnnotation{{Kind: dispatch.AnnotationElement, Note: note, Rect: dispatch.Rect{X: 1, Y: 1, Width: 4, Height: 3}, Element: el,
			Observed: &dispatch.Observed{At: at, URL: "http://127.0.0.1:5173/app", Title: "App", Viewport: vp, Epoch: 3, Label: dispatch.RevisionLabel{Commit: commit}}}}}
	if shot != nil {
		n.ImageHow, n.ImageScale = dispatch.ImageTab, 1
	} else {
		n.ImageHow, n.ImageWhy = dispatch.ImageNone, "the person declined the tab capture"
	}
	f := dispatch.CaptureFacts{Origin: dispatch.OriginCandidate, Run: run, Task: "build", Attempt: 1, Tree: dispatch.TreePreview, Commit: commit,
		Candidate: candName(run, "build", 1, commit), Where: "native", Scope: dispatch.CaptureScope{Crew: "team"},
		Sources: [][]dispatch.Source{{{Path: "web/app/components/SaveBar.vue", Line: 12, How: "vue", Found: true, Class: "tracked"}}}}
	return n, f
}

// seedCapture keeps the capture in srv's Dispatch store, as the capture
// route would, and gives its id.
func seedCapture(t *testing.T, srv *Server, n dispatch.NewCapture, f dispatch.CaptureFacts, shot []byte) string {
	t.Helper()
	sum := ""
	var img io.Reader
	if shot != nil {
		h := sha256.Sum256(shot)
		sum, img = hex.EncodeToString(h[:]), bytes.NewReader(shot)
	}
	digest, err := dispatch.RequestDigest(n, sum)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := srv.dispatch.CreateCapture(t.Context(), dispatch.Op{Actor: adminActor, ID: opID(), By: "Nate"}, digest, n, f, img)
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the capture: %+v %v", rc, err)
	}
	return rc.ID
}

// sendToReview sends the capture at rev to a review, as the send route
// does once its replay found nothing, under the op id.
func sendToReview(t *testing.T, srv *Server, opID, id string, rev int64, to reviewSendTo) (reviewSendReply, *apiError) {
	t.Helper()
	digest, err := dispatch.RequestDigest(map[string]any{"id": id, "rev": rev, "to": to}, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ReviewTimeout)
	defer cancel()
	out, aerr, err := srv.sendCaptureToReview(ctx, srv.dispatch, dispatch.Op{Actor: adminActor, ID: opID, By: "Nate"}, digest, id, rev, to)
	if err != nil {
		t.Fatalf("the send to a review: %v", err)
	}
	return out, aerr
}

func hexSum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// evidenceIn is the attempt's evidence file of the capture, by kind.
func evidenceIn(t *testing.T, l *launchTest, run string, n int, capture, ext string) []byte {
	t.Helper()
	return l.treeFile(t, run, "build", n, fmt.Sprintf("evidence/capture-%s-r1.%s", capture, ext))
}

// Request a change of a candidate from its capture: one send, pending until
// the review it asks for is recorded, then sent to that review; the review
// holds one capture finding whose words, path and line the server made
// from the annotation, and is applied by Yardmaster as a repair whose
// attempt finds the send's document and screenshot in .conductor/evidence
// at their hashes, task.md naming them under the finding. The same send
// again (its receipt found) answers the same send and review, and a second
// Request a change of the same capture is a send of its own.
func TestRequestAChangeRepairsWithTheCapture(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	shot := testPNG(t)
	n, f := capturedOf(run, c1, "Save is covered by the overlay.", shot, false)
	id := seedCapture(t, l.srv, n, f, shot)
	cand := candName(run, "build", 1, c1)
	out, aerr := sendToReview(t, l.srv, "send-1", id, 1, reviewSendTo{Candidate: cand, Verdict: dispatch.VerdictRequestChanges})
	if aerr != nil || out.status != http.StatusCreated {
		t.Fatalf("Request a change: %d %v %v", out.status, out.reply, aerr)
	}
	sv := out.reply["send"].(dispatch.CaptureSend)
	v, _ := out.reply["review"].(*dispatch.Review)
	if v == nil || sv.State != dispatch.SendSent || sv.Target != v.ID || out.reply["key"] != "CND-1" || len(sv.Request) != 0 {
		t.Fatalf("the send: %+v, the review %+v", sv, v)
	}
	if v.Application.State != dispatch.ApplicationApplied || v.Application.Attempt != 2 || len(v.Findings) != 1 {
		t.Fatalf("the review: %+v", v)
	}
	fd := v.Findings[0]
	if fd.Kind != dispatch.FindingKindCapture || fd.Capture == nil || fd.Capture.Send != sv.ID || fd.Path != "web/app/components/SaveBar.vue" || fd.From != 12 ||
		!strings.HasPrefix(fd.Message, "Save is covered by the overlay.\n\nElement button named “Save” at “button.save”.") || fd.Severity != "major" {
		t.Fatalf("the finding: %+v", fd)
	}
	runningAttempt(t, l.testEnv, run, "build", 2)
	doc, err := l.srv.dispatch.OpenSendFile(t.Context(), sv.ID, sv.Digest)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := io.ReadAll(doc)
	doc.Close()
	if got := evidenceIn(t, l, run, 2, id, "json"); !bytes.Equal(got, want) || hexSum(got) != sv.Digest {
		t.Fatalf("the document in the repair attempt: %d bytes, the send's %d", len(got), len(want))
	}
	if got := evidenceIn(t, l, run, 2, id, "png"); !bytes.Equal(got, shot) {
		t.Fatal("the screenshot in the repair attempt is not the capture's")
	}
	md := string(l.treeFile(t, run, "build", 2, "task.md"))
	if !strings.Contains(md, "the page the person captured, drawn by `web/app/components/SaveBar.vue:12`") ||
		!strings.Contains(md, fmt.Sprintf("The capture: `.conductor/evidence/capture-%s-r1.json`, annotation 1", id)) ||
		!strings.Contains(md, fmt.Sprintf("its screenshot: `.conductor/evidence/capture-%s-r1.png`", id)) {
		t.Fatalf("task.md:\n%s", md)
	}
	// The same send again: its receipt, the send and the review as they
	// stand, no second review.
	rc, found, err := l.srv.dispatch.Receipt(t.Context(), adminActor, "send-1")
	if err != nil || !found {
		t.Fatalf("the send's receipt: %v %v", found, err)
	}
	rc.Repeat = true
	again, aerr, err := l.srv.continueReviewSend(t.Context(), l.srv.dispatch, dispatch.Op{Actor: adminActor, ID: "send-1", By: "Nate"}, rc, false)
	if err != nil || aerr != nil || again.status != http.StatusOK || again.reply["send"].(dispatch.CaptureSend).Target != v.ID {
		t.Fatalf("the send again: %d %v %v %v", again.status, again.reply, aerr, err)
	}
	if n := reviewsOn(t, l.srv, "CND-1"); n != 1 {
		t.Fatalf("%d reviews", n)
	}
}

// What Request a change refuses before anything is kept: a capture of the
// attempt's worktree (Create a ticket instead) or of another candidate, a
// candidate that is not of the form, a draft's verdict on a request, a
// verdict other than request_changes, a severity of no name, a run no
// ticket holds, and a run the server no longer runs without the person's
// word (continuation_needed). Nothing is kept: no send, no review.
func TestRequestAChangeIsRefusedBeforeItIsKept(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	cand := candName(run, "build", 1, c1)
	n, f := capturedOf(run, c1, "Covered.", nil, false)
	id := seedCapture(t, l.srv, n, f, nil)
	wf := f
	wf.Origin, wf.Tree, wf.Mutable, wf.Candidate = dispatch.OriginAttempt, dispatch.TreeWorktree, true, ""
	worktree := seedCapture(t, l.srv, n, wf, nil)
	for _, c := range []struct {
		name, id string
		to       reviewSendTo
		status   int
		code     string
	}{
		{"a worktree's capture", worktree, reviewSendTo{Candidate: cand, Verdict: "request_changes"}, http.StatusConflict, dispatch.RefusalInvalid},
		{"another candidate", id, reviewSendTo{Candidate: candName(run, "build", 2, c1), Verdict: "request_changes"}, http.StatusConflict, dispatch.RefusalInvalid},
		{"no candidate", id, reviewSendTo{Verdict: "request_changes"}, http.StatusBadRequest, "invalid_request"},
		{"a comment", id, reviewSendTo{Candidate: cand, Verdict: "comment"}, http.StatusBadRequest, "invalid_request"},
		{"a draft with a severity", id, reviewSendTo{Candidate: cand, Draft: true, Severity: "major"}, http.StatusBadRequest, "invalid_request"},
		{"no such severity", id, reviewSendTo{Candidate: cand, Verdict: "request_changes", Severity: "huge"}, http.StatusBadRequest, "invalid_request"},
		{"no such capture", "0123456789abcdef", reviewSendTo{Candidate: cand, Verdict: "request_changes"}, http.StatusNotFound, dispatch.RefusalNotFound},
	} {
		out, aerr := sendToReview(t, l.srv, opID(), c.id, 1, c.to)
		if aerr == nil || aerr.status != c.status || aerr.Code != c.code {
			t.Errorf("%s: %+v %v", c.name, out, aerr)
		}
	}
	if c, err := l.srv.dispatch.Capture(t.Context(), id); err != nil || len(c.Sends) != 0 {
		t.Fatalf("sends kept: %+v %v", c.Sends, err)
	}
	// A run the server stopped: the person's word first, nothing kept.
	l.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
	out, aerr := sendToReview(t, l.srv, opID(), id, 1, reviewSendTo{Candidate: cand, Verdict: "request_changes"})
	if aerr != nil || out.status != http.StatusConflict || out.reply["error"].(apiError).Code != "continuation_needed" {
		t.Fatalf("a stopped run: %+v %v", out, aerr)
	}
	if c, err := l.srv.dispatch.Capture(t.Context(), id); err != nil || len(c.Sends) != 0 || reviewsOn(t, l.srv, "CND-1") != 0 {
		t.Fatalf("kept: %+v %v", c.Sends, err)
	}
}

// Add to a review: the send is sent in its one step, to the draft; the
// person's draft then sends a capture finding naming it through RV1's
// route, whose words, path and line the server makes. The route refuses
// 409, keeping nothing (no receipt), a capture finding whose send went to Brakeman, a
// send of another capture, one of another candidate's capture and an
// annotation the document has not; and the store refuses, its receipt
// kept, a capture finding with the client's words.
func TestAddToAReviewPutsTheCaptureInTheDraft(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	cand := candName(run, "build", 1, c1)
	n, f := capturedOf(run, c1, "The label is cut.", nil, false)
	id := seedCapture(t, l.srv, n, f, nil)
	out, aerr := sendToReview(t, l.srv, "draft-1", id, 1, reviewSendTo{Candidate: cand, Draft: true})
	sv, _ := out.reply["send"].(dispatch.CaptureSend)
	if aerr != nil || out.status != http.StatusCreated || sv.State != dispatch.SendSent || sv.Target != dispatch.ReviewDraft || out.reply["key"] != "CND-1" {
		t.Fatalf("Add to a review: %d %+v %v", out.status, out.reply, aerr)
	}
	finding := func(capture, send string, k int) map[string]any {
		return map[string]any{"kind": "capture", "intent": "change", "severity": "minor", "capture": map[string]any{"capture": capture, "send": send, "k": k}}
	}
	other := seedCapture(t, l.srv, n, f, nil)
	brakemanSend := func() string {
		rc, err := l.srv.dispatch.SendCapture(t.Context(), dispatch.Op{Actor: adminActor, ID: opID()}, strings.Repeat("0", 64), other, 1,
			dispatch.Destination{Kind: dispatch.SendBrakeman}, dispatch.SendFacts{Request: []byte(`{"kind":"ask"}`)})
		if err != nil || rc.Status != dispatch.StatusDone {
			t.Fatalf("a send to Brakeman: %+v %v", rc, err)
		}
		return rc.ID
	}()
	for _, c := range []struct {
		name    string
		finding map[string]any
		words   string
	}{
		{"a send to Brakeman", finding(other, brakemanSend, 1), "not to a review"},
		{"another capture's send", finding(other, sv.ID, 1), "is no send of the capture"},
		{"an annotation it has not", finding(id, sv.ID, 2), "not an annotation 2"},
	} {
		body := reviewOf(cand, l.head, "request_changes", c.finding)
		got := sendReview(t, l.testEnv, "CND-1", body, http.StatusConflict, dispatch.RefusalInvalid)
		if msg := got["error"].(map[string]any)["message"].(string); !strings.Contains(msg, c.words) {
			t.Errorf("%s: %v", c.name, got)
		}
		// Refused by the route as it binds the finding: before the store, no receipt kept.
		if _, found, err := l.srv.dispatch.Receipt(t.Context(), adminActor, body["opId"].(string)); err != nil || found {
			t.Errorf("%s: a receipt kept: %v %v", c.name, found, err)
		}
	}
	if n := reviewsOn(t, l.srv, "CND-1"); n != 0 {
		t.Fatalf("%d reviews kept", n)
	}
	words := finding(id, sv.ID, 1)
	words["message"] = "Mine."
	sendReview(t, l.testEnv, "CND-1", reviewOf(cand, l.head, "comment", words), http.StatusConflict, dispatch.RefusalInvalid)
	got := sendReview(t, l.testEnv, "CND-1", reviewOf(cand, l.head, "comment", finding(id, sv.ID, 1)), http.StatusCreated, "")
	fd := entityOf(t, got, "review")["findings"].([]any)[0].(map[string]any)
	if fd["kind"] != "capture" || fd["path"] != "web/app/components/SaveBar.vue" || fd["from"] != 12.0 || !strings.HasPrefix(fd["message"].(string), "The label is cut.") {
		t.Fatalf("the finding: %v", fd)
	}
}

// A capture at its bounds (a note of 4,000 characters, an element's report
// near its 32 KiB) sent as Request a change reaches the repair through a
// real launch: its finding within 1,000 characters, the note cut on a word
// with "…", and both files in the attempt's tree at their hashes.
func TestAMaximalCaptureFindingReachesTheRepair(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	shot := testPNG(t)
	note := strings.Repeat("covered ", dispatch.MaxAnnotationNote/8)
	n, f := capturedOf(run, c1, note, shot, true)
	id := seedCapture(t, l.srv, n, f, shot)
	out, aerr := sendToReview(t, l.srv, opID(), id, 1, reviewSendTo{Candidate: candName(run, "build", 1, c1), Verdict: "request_changes", Severity: "blocker"})
	if aerr != nil || out.status != http.StatusCreated {
		t.Fatalf("Request a change: %d %v %v", out.status, out.reply, aerr)
	}
	v := out.reply["review"].(*dispatch.Review)
	msg := v.Findings[0].Message
	if len([]rune(msg)) > dispatch.MaxFindingMessage || !strings.Contains(msg, "covered…\n\nElement button named “nnn") || v.Application.State != dispatch.ApplicationApplied {
		t.Fatalf("the finding: %d characters, %q; the review %+v", len([]rune(msg)), msg[:80], v.Application)
	}
	sv := out.reply["send"].(dispatch.CaptureSend)
	runningAttempt(t, l.testEnv, run, "build", 2)
	if got := evidenceIn(t, l, run, 2, id, "json"); hexSum(got) != sv.Digest || len(got) < 30<<10 {
		t.Fatalf("the document: %d bytes", len(got))
	}
	if got := evidenceIn(t, l, run, 2, id, "png"); hexSum(got) != hexSum(shot) {
		t.Fatal("the screenshot is not the capture's")
	}
	md := string(l.treeFile(t, run, "build", 2, "task.md"))
	if !strings.Contains(md, "covered…") || !strings.Contains(md, "(blocker)") {
		t.Fatalf("task.md:\n%s", md)
	}
}

// A repair queued with its evidence (the run paused, so its attempt has not
// started), the server ended and another started over the same stores,
// and the run continued (and its pause lifted): the continuation's repair
// attempt holds both files at their hashes, read again from the store.
func TestCaptureEvidenceSurvivesARestartAndAContinuation(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	shot := testPNG(t)
	n, f := capturedOf(run, c1, "Save is covered.", shot, false)
	id := seedCapture(t, l.srv, n, f, shot)
	l.expect(t, "POST", "/api/runs/"+run+"/pause", map[string]any{"commandId": "pause-1"}, http.StatusOK, "")
	out, aerr := sendToReview(t, l.srv, opID(), id, 1, reviewSendTo{Candidate: candName(run, "build", 1, c1), Verdict: "request_changes"})
	if aerr != nil || out.status != http.StatusCreated {
		t.Fatalf("Request a change: %d %v %v", out.status, out.reply, aerr)
	}
	sv := out.reply["send"].(dispatch.CaptureSend)
	again := l.restartAfterStop(t, run)
	next := entityOf(t, again.expect(t, "POST", "/api/runs/"+run+"/resume", nil, http.StatusCreated, ""), "run")["id"].(string)
	// The continuation keeps the pause: lifted, its repair attempt starts.
	again.expect(t, "POST", "/api/runs/"+next+"/unpause", map[string]any{"commandId": "unpause-1"}, http.StatusOK, "")
	runningAttempt(t, again, next, "build", 2)
	if got := evidenceIn(t, l, next, 2, id, "json"); hexSum(got) != sv.Digest {
		t.Fatal("the continuation's document is not the send's")
	}
	if got := evidenceIn(t, l, next, 2, id, "png"); hexSum(got) != hexSum(shot) {
		t.Fatal("the continuation's screenshot is not the capture's")
	}
}

// A Request a change whose server stopped after its send was kept and
// before the review was made is finished by the reviews' worker from the
// store alone: the review made once, under the send's op, and the send
// sent to it.
func TestALostReviewSendIsFinishedByTheWorker(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	cand := candName(run, "build", 1, c1)
	n, f := capturedOf(run, c1, "Covered.", nil, false)
	id := seedCapture(t, l.srv, n, f, nil)
	// The worker's own passes stopped: this test runs its pass itself.
	l.srv.stopReviews()
	// The send as the route keeps it, its steps never taken.
	request := []byte(`{"key":"CND-1","review":{"candidate":"` + cand + `","base":"` + l.head + `","verdict":"request_changes","via":"web","findings":[{"kind":"capture","intent":"change","severity":"major","capture":{"capture":"` + id + `","send":"","k":1}}]}}`)
	rc, err := l.srv.dispatch.SendCapture(t.Context(), dispatch.Op{Actor: adminActor, ID: "lost-1", By: "Nate"}, strings.Repeat("0", 64), id, 1,
		dispatch.Destination{Kind: dispatch.SendReview, Candidate: cand}, dispatch.SendFacts{Request: request})
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the send: %+v %v", rc, err)
	}
	l.srv.settleReviewSends(t.Context())
	sv, err := l.srv.dispatch.CaptureSend(t.Context(), rc.ID)
	if err != nil || sv.State != dispatch.SendSent || !dispatch.ValidID(sv.Target) {
		t.Fatalf("the send after the worker: %+v %v", sv, err)
	}
	v := reviewNow(t, l.srv, sv.Target)
	if v.Findings[0].Capture == nil || v.Findings[0].Capture.Send != rc.ID || v.Actor != adminActor || v.CreatedBy != "Nate" {
		t.Fatalf("the review: %+v", v)
	}
	l.srv.settleReviewSends(t.Context())
	if n := reviewsOn(t, l.srv, "CND-1"); n != 1 {
		t.Fatalf("%d reviews", n)
	}
}

// The send route takes a send to a review (POST /api/captures/{id}/send,
// kind review): Request a change sent and answered 201 with its review,
// the same opId again 200 with the same send and no second review; a
// review's fields on another kind, and a summary past its bound, refused
// 400 before anything is kept; a store's refusal (another revision)
// answered with its receipt.
func TestTheSendRouteTakesASendToAReview(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	shot := testPNG(t)
	n, f := capturedOf(run, c1, "Save is covered.", shot, false)
	id := seedCapture(t, l.srv, n, f, shot)
	cand := candName(run, "build", 1, c1)
	path := "/api/captures/" + id + "/send"
	body := map[string]any{"opId": "route-1", "byName": "Nate", "rev": 1, "to": map[string]any{"kind": "review", "candidate": cand, "verdict": "request_changes"}}
	out := l.expect(t, "POST", path, body, http.StatusCreated, "")
	sv := out["send"].(map[string]any)
	rv := out["review"].(map[string]any)
	if sv["state"] != dispatch.SendSent || sv["target"] != rv["id"] || out["key"] != "CND-1" || rv["application"].(map[string]any)["state"] != dispatch.ApplicationApplied {
		t.Fatalf("the send: %v", out)
	}
	again := l.expect(t, "POST", path, body, http.StatusOK, "")
	if again["send"].(map[string]any)["id"] != sv["id"] || again["receipt"].(map[string]any)["repeat"] != true {
		t.Fatalf("the same send again: %v", again)
	}
	if n := reviewsOn(t, l.srv, "CND-1"); n != 1 {
		t.Fatalf("%d reviews", n)
	}
	for _, to := range []map[string]any{
		{"kind": "ticket", "key": "CND-1", "candidate": cand},
		{"kind": "brakeman", "question": "Why?", "draft": true},
		{"kind": "review", "candidate": cand, "verdict": "request_changes", "summary": strings.Repeat("é", dispatch.MaxReviewSummary+1)},
	} {
		l.expect(t, "POST", path, map[string]any{"opId": opID(), "rev": 1, "to": to}, http.StatusBadRequest, "invalid_request")
	}
	l.expect(t, "POST", path, map[string]any{"opId": opID(), "rev": 2, "to": map[string]any{"kind": "review", "candidate": cand, "draft": true}}, http.StatusConflict, dispatch.RefusalStaleRevision)
}
