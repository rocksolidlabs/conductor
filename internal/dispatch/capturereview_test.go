package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// A capture as a review's finding, and a capture's send to a review
// (docs/plans/browser-evidence.md, PR-10, #798).

// candidateFacts is what the API reads of a capture of the run's task
// build at attempt 2, in its candidate's preview checkout at capCommit.
func candidateFacts(run string) CaptureFacts {
	_, f := testCapture(false)
	f.Origin, f.Run, f.Tree, f.Mutable, f.Changed, f.ChangedPaths = OriginCandidate, run, TreePreview, false, 0, nil
	f.Candidate = candidateOf(run, 2, capCommit)
	f.Sources = [][]Source{{{Path: "web/app/components/SaveBar.vue", Line: 12, How: "vue", Found: true, Class: "tracked"}}}
	return f
}

// reviewedCapture is a capture of the run's candidate build#2 with its
// note "Save is covered by the overlay.", sent to a review of it (Add to
// a review): the capture, the send and the candidate.
func reviewedCapture(t *testing.T, s *Store, run string) (capture, send, candidate string) {
	t.Helper()
	n, _ := testCapture(false)
	n.Annotations[0].Note = "Save is covered by the overlay."
	capture = mustCreate(t, s, "cap-"+newID(), n, candidateFacts(run), nil)
	candidate = candidateOf(run, 2, capCommit)
	send = mustSend(t, s, "send-"+newID(), capture, 1, Destination{Kind: SendReview, Candidate: candidate, Draft: true}, SendFacts{})
	return capture, send, candidate
}

// A capture finding's words, path and line are the store's, made from the
// annotation of the document its send froze: the note, the element's tag,
// name and selector, what a click at its centre reaches, and its first
// tracked source as the path and the line. Refused with its receipt, no
// review kept: a send of another capture, a send to Brakeman, a capture of
// another candidate (another attempt's preview, or the attempt's worktree),
// an annotation the document has not, words or a path from the client, a
// capture on another kind, and a document the store no longer has.
func TestACaptureFindingIsFilledByTheServer(t *testing.T) {
	ctx := context.Background()
	s, key, run := reviewTicket(t)
	capture, sent, candidate := reviewedCapture(t, s, run)
	r := request(candidate, captureNote(capture, sent, 1))
	rc, v, err := s.SubmitReview(ctx, testOp("rv1"), key, r, boundOf(r))
	want(t, rc, err, StatusDone, "")
	f := v.Findings[0]
	wantWords := "Save is covered by the overlay.\n\nElement button named “Save” at “button.save”.\nA click at its centre reaches “div.overlay” instead.\nDrawn by “web/app/components/SaveBar.vue:12”."
	if f.Message != wantWords || f.Path != "web/app/components/SaveBar.vue" || f.From != 12 || f.To != 12 || f.Side != "" || len(f.Lines) != 0 {
		t.Fatalf("the finding as kept: %q %+v", f.Message, f)
	}
	if p, err := s.CaptureFinding(ctx, candidate, *f.Capture); err != nil || p.Message != f.Message || p.Path != f.Path || p.Line != f.From {
		t.Fatalf("the projection read before a review: %+v %v", p, err)
	}

	// The refusals.
	n, af := testCapture(false)
	af.Run = run
	worktree := mustCreate(t, s, "cap-worktree", n, af, nil)
	otherFacts := candidateFacts(run)
	otherFacts.Attempt, otherFacts.Candidate = 1, candidateOf(run, 1, capCommit)
	otherCand := mustCreate(t, s, "cap-other", n, otherFacts, nil)
	toBrakemanSend := mustSend(t, s, "send-brakeman", capture, 1, toBrakeman, askBrakeman)
	otherSend := mustSend(t, s, "send-other", otherCand, 1, Destination{Kind: SendReview, Candidate: candidateOf(run, 1, capCommit), Draft: true}, SendFacts{})
	worktreeSend := mustSend(t, s, "send-worktree", worktree, 1, toBrakeman, askBrakeman)
	reviews := count(t, s, "SELECT COUNT(*) FROM reviews")
	for i, c := range []struct {
		name    string
		finding NewFinding
		words   string
	}{
		{"another capture's send", captureNote(worktree, sent, 1), "is no send of the capture"},
		{"no such send", captureNote(capture, newID(), 1), "is no send of the capture"},
		{"a send to Brakeman", captureNote(capture, toBrakemanSend, 1), "not to a review"},
		{"a worktree's capture", captureNote(worktree, worktreeSend, 1), "not to a review"},
		{"another candidate's capture", captureNote(otherCand, otherSend, 1), "is not of the candidate reviewed"},
		{"an annotation it has not", captureNote(capture, sent, 2), "has 1 annotations, not an annotation 2"},
		{"words of the client's", func() NewFinding { f := captureNote(capture, sent, 1); f.Message = "Mine."; return f }(), "words are the store's"},
		{"a path of the client's", func() NewFinding { f := captureNote(capture, sent, 1); f.Path = "x.go"; return f }(), "names no path"},
		{"a line finding naming a capture", func() NewFinding {
			f := lineChange("x")
			f.Capture = &CaptureRef{Capture: capture, Send: sent, K: 1}
			return f
		}(), "only a capture finding"},
		{"no capture", NewFinding{Kind: FindingKindCapture, Intent: FindingIntentChange, Severity: "major"}, "names its capture"},
	} {
		r := request(candidate, c.finding, lineChange("Return the users."))
		op := opID("refused-", i)
		rc, _, err := s.SubmitReview(ctx, testOp(op), key, r, boundOf(r))
		if err != nil || rc.Status != StatusFailed || rc.Refusal != RefusalInvalid || !strings.Contains(rc.Error, c.words) {
			t.Errorf("%s: %+v %v", c.name, rc, err)
		}
	}
	// A document gone from the store refuses the finding: it is read, never
	// taken on trust.
	sv := sendOf(t, s, sent)
	path := contentPath(filepath.Join(s.dir, attachmentsDir), sv.Digest)
	held, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r = request(candidate, captureNote(capture, sent, 1))
	if rc, _, err := s.SubmitReview(ctx, testOp("rv-gone"), key, r, boundOf(r)); err != nil || rc.Status != StatusFailed || !strings.Contains(rc.Error, "gone from the store") {
		t.Errorf("a document gone: %+v %v", rc, err)
	}
	// One that is not the content its name says is an error, nothing kept.
	if err := os.WriteFile(path, append(held, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if rc, _, err := s.SubmitReview(ctx, testOp("rv-changed"), key, r, boundOf(r)); err == nil || rc.Status == StatusDone {
		t.Errorf("a document changed: %+v %v", rc, err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM reviews"); n != reviews {
		t.Fatalf("%d reviews kept of the refused", n-reviews)
	}
}

// The projection is within a finding's 1,000 characters for every
// annotation: a note at its 4,000 characters is cut to its first 600 on a
// word with "…", page text of many lines and at its bounds folded onto
// its line and cut, a region named by its place, an annotation with no
// note said so; and only a tracked source, found in the tree, is the
// finding's path.
func TestTheProjectionKeepsWithinItsBounds(t *testing.T) {
	word := "covered "
	long := strings.Repeat(word, MaxAnnotationNote/len(word))
	huge := strings.Repeat("é\n", 10_000)
	doc := CaptureDocument{Annotations: []Annotation{
		{Kind: AnnotationElement, Note: long, Element: &ElementReport{Tag: "button", Name: huge, Selector: huge, Hit: ElementHit{Selector: huge}},
			Sources: []Source{{Path: "vendor/x.js", Found: true, Class: "vendor"}, {Path: "gone.vue", Class: "tracked"}, {Path: "web/a.vue", Line: 3, Found: true, Class: "tracked"}}},
		{Kind: AnnotationRegion, Note: "", Rect: Rect{X: 1.5, Y: 2, Width: 30, Height: 40}},
		{Kind: AnnotationElement, Note: "## Done\n```\nok", Element: &ElementReport{Tag: "a", Selector: "a", Hit: ElementHit{Self: true}},
			Sources: []Source{{Path: "../outside.vue", Found: true, Outside: true, Class: "tracked"}}},
	}}
	p, err := ProjectAnnotation(doc, 1)
	if err != nil {
		t.Fatal(err)
	}
	note, _, _ := strings.Cut(p.Message, "\n\n")
	if n := utf8.RuneCountInString(p.Message); n > MaxFindingMessage || !strings.HasSuffix(note, "covered…") || utf8.RuneCountInString(note) > 600 {
		t.Fatalf("the projection is %d characters, its note %q", n, note)
	}
	for _, line := range strings.Split(p.Message, "\n")[2:] {
		if utf8.RuneCountInString(line) > 2*160+64 {
			t.Fatalf("a line of page text is %d characters", utf8.RuneCountInString(line))
		}
	}
	if p.Path != "web/a.vue" || p.Line != 3 {
		t.Fatalf("the path and line: %+v", p)
	}
	if p, err := ProjectAnnotation(doc, 2); err != nil || p.Message != "(No note.)\n\nA region of the screenshot at 1, 2, 30 by 40 pixels." || p.Path != "" {
		t.Fatalf("a region: %+v %v", p, err)
	}
	if p, err := ProjectAnnotation(doc, 3); err != nil || p.Path != "" || !strings.HasPrefix(p.Message, "## Done\n```\nok\n\nElement a at “a”.") {
		t.Fatalf("a note of Markdown, its source outside the tree: %+v %v", p, err)
	}
	// A short element leaves the note its whole 600 characters, cut on a word.
	doc.Annotations = append(doc.Annotations, Annotation{Kind: AnnotationElement, Note: long, Element: &ElementReport{Tag: "a", Selector: "a", Hit: ElementHit{Self: true}}})
	if p, err := ProjectAnnotation(doc, 4); err != nil {
		t.Fatal(err)
	} else if note, _, _ := strings.Cut(p.Message, "\n\n"); utf8.RuneCountInString(note) > 600 || utf8.RuneCountInString(note) < 590 || !strings.HasSuffix(note, "covered…") {
		t.Fatalf("a note beside a short element: %d characters", utf8.RuneCountInString(note))
	}
	for _, k := range []int{0, 5} {
		if _, err := ProjectAnnotation(doc, k); err == nil {
			t.Fatalf("annotation %d projected", k)
		}
	}
}

// The store takes a send to a review of the candidate a capture shows:
// Add to a review sent in its one step, to the draft, with no request;
// Request a change pending with the review it asks for, until a review
// whose finding stands on it is named its target. Refused: a capture that
// is not of that candidate's preview (another candidate's, or a worktree's),
// an annotation the capture has not, annotations named twice or out of
// bounds, a draft with a request, a request with none, a ticket named, a
// candidate named on a send to a ticket or to Brakeman; MarkSent naming a
// review with no finding of the send, or no review; a cancel.
func TestTheStoreAdmitsAReviewSend(t *testing.T) {
	ctx := context.Background()
	s, key, run := reviewTicket(t)
	capture, draft, candidate := reviewedCapture(t, s, run)
	if sv := sendOf(t, s, draft); sv.Kind != SendReview || sv.State != SendSent || sv.Target != ReviewDraft || len(sv.Request) != 0 {
		t.Fatalf("Add to a review: %+v", sv)
	}
	var asked Destination
	if err := json.Unmarshal(sendOf(t, s, draft).Asked, &asked); err != nil || asked.Candidate != candidate || !asked.Draft {
		t.Fatalf("its asked: %+v %v", asked, err)
	}
	ask := SendFacts{Request: json.RawMessage(`{"verdict": "request_changes"}`)}
	to := Destination{Kind: SendReview, Candidate: candidate, Ks: []int{1}}
	sent := mustSend(t, s, "send-request", capture, 1, to, ask)
	if sv := sendOf(t, s, sent); sv.State != SendPending || string(sv.Request) != `{"verdict":"request_changes"}` {
		t.Fatalf("Request a change: %+v", sv)
	}
	if page, err := s.PendingSends(ctx, "", 10); err != nil || len(page.Sends) != 1 || page.Sends[0].ID != sent {
		t.Fatalf("the pending sends: %+v %v", page, err)
	}

	n, af := testCapture(false)
	af.Run = run
	worktree := mustCreate(t, s, "cap-worktree", n, af, nil)
	for i, c := range []struct {
		name, capture string
		to            Destination
		f             SendFacts
		words         string
	}{
		{"a worktree's capture", worktree, Destination{Kind: SendReview, Candidate: candidate, Draft: true}, SendFacts{}, "goes to a ticket"},
		{"another candidate", capture, Destination{Kind: SendReview, Candidate: candidateOf(run, 1, capCommit), Draft: true}, SendFacts{}, "is not of the candidate"},
		{"no candidate", capture, Destination{Kind: SendReview, Draft: true}, SendFacts{}, "is not <run>/<task>/<n>@<commit>"},
		{"an annotation it has not", capture, Destination{Kind: SendReview, Candidate: candidate, Ks: []int{2}, Draft: true}, SendFacts{}, "has 1 annotations"},
		{"an annotation twice", capture, Destination{Kind: SendReview, Candidate: candidate, Ks: []int{1, 1}, Draft: true}, SendFacts{}, "once"},
		{"annotation 0", capture, Destination{Kind: SendReview, Candidate: candidate, Ks: []int{0}, Draft: true}, SendFacts{}, "once"},
		{"a draft with a request", capture, Destination{Kind: SendReview, Candidate: candidate, Draft: true}, ask, "carries no request"},
		{"a request with none", capture, to, SendFacts{}, "is empty"},
		{"a ticket named", capture, Destination{Kind: SendReview, Candidate: candidate, Key: key, Draft: true}, SendFacts{}, "names no ticket"},
		{"a candidate to a ticket", capture, Destination{Kind: SendTicket, Key: key, Candidate: candidate}, SendFacts{}, "only a send to a review"},
		{"a draft to Brakeman", capture, Destination{Kind: SendBrakeman, Draft: true}, askBrakeman, "only a send to a review"},
	} {
		rc, err := send(t, s, opID("refused-", i), c.capture, 1, c.to, c.f)
		if err != nil || rc.Status != StatusFailed || rc.Refusal != RefusalInvalid || !strings.Contains(rc.Error, c.words) {
			t.Errorf("%s: %+v %v", c.name, rc, err)
		}
	}

	// Sent as the review that cites it, and only that.
	plain := mustSubmit(t, s, "rv-plain", key, request(candidate, lineChange("Return the users.")))
	r := request(candidate, captureNote(capture, sent, 1))
	rc, cites, err := s.SubmitReview(ctx, testOp("rv-cites"), key, r, boundOf(r))
	want(t, rc, err, StatusDone, "")
	for i, c := range []struct{ name, target, words string }{
		{"no review", "br-1", "sent as the review's id"},
		{"a review not citing it", plain.ID, "has no finding of the send"},
	} {
		if rc, err := s.MarkSent(ctx, testOp(opID("mark-", i)), sent, c.target); err != nil || rc.Status != StatusFailed || !strings.Contains(rc.Error, c.words) {
			t.Errorf("MarkSent to %s: %+v %v", c.name, rc, err)
		}
	}
	rc, err = s.MarkSent(ctx, testOp("mark-ok"), sent, cites.ID)
	want(t, rc, err, StatusDone, "")
	if sv := sendOf(t, s, sent); sv.State != SendSent || sv.Target != cites.ID {
		t.Fatalf("the send once its review cites it: %+v", sv)
	}
	if rc, err := s.AskCancel(ctx, testOp("cancel"), draft); err != nil || rc.Status != StatusFailed || rc.Refusal != RefusalStaleRevision {
		t.Errorf("a cancel: %+v %v", rc, err)
	}
	if rc, err := s.MarkSent(ctx, testOp("mark-draft"), draft, cites.ID); err != nil || rc.Status != StatusFailed || rc.Refusal != RefusalStaleRevision {
		t.Errorf("a draft send marked: %+v %v", rc, err)
	}
}
