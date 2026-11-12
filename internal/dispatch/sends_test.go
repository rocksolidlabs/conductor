package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A capture's sends (Program item PV1, PR-1b, issue #825,
// docs/plans/browser-evidence.md).

// toBrakeman and askBrakeman are a send to Brakeman and its request.
var (
	toBrakeman  = Destination{Kind: SendBrakeman}
	askBrakeman = SendFacts{Request: json.RawMessage(`{"question": "Why is Save covered?", "subjects": ["capture"]}`)}
)

// sendDigest is the digest of the client's request to send.
func sendDigest(t *testing.T, id string, rev int64, to Destination, f SendFacts) string {
	t.Helper()
	return digestOf(t, struct {
		ID      string      `json:"id"`
		Rev     int64       `json:"rev"`
		To      Destination `json:"to"`
		Request string      `json:"request,omitempty"`
	}{id, rev, to, string(f.Request)}, nil)
}

// send sends the capture at rev under the op id.
func send(t *testing.T, s *Store, opID, id string, rev int64, to Destination, f SendFacts) (Receipt, error) {
	t.Helper()
	return s.SendCapture(context.Background(), testOp(opID), sendDigest(t, id, rev, to, f), id, rev, to, f)
}

// mustSend is send, done now, and gives the send's id.
func mustSend(t *testing.T, s *Store, opID, id string, rev int64, to Destination, f SendFacts) string {
	t.Helper()
	rc, err := send(t, s, opID, id, rev, to, f)
	want(t, rc, err, StatusDone, "")
	if rc.Repeat || !ValidID(rc.ID) || rc.Rev != 0 {
		t.Fatalf("%s: %+v", opID, rc)
	}
	return rc.ID
}

// sendOf reads the send with the id whole.
func sendOf(t *testing.T, s *Store, id string) CaptureSend {
	t.Helper()
	sv, err := s.CaptureSend(context.Background(), id)
	if err != nil {
		t.Fatalf("send %s: %v", id, err)
	}
	return sv
}

// documentOfRev is the capture's document at rev, as Document gives it.
func documentOfRev(t *testing.T, s *Store, id string, rev int64) []byte {
	t.Helper()
	doc, err := s.Document(context.Background(), id, rev)
	if err != nil {
		t.Fatalf("the document of %s at r%d: %v", id, rev, err)
	}
	return doc
}

// sendFile is the content the send holds with the hash, read whole.
func sendFile(t *testing.T, s *Store, id, sum string) []byte {
	t.Helper()
	fh, err := s.OpenSendFile(context.Background(), id, sum)
	if err != nil {
		t.Fatalf("the send %s's file %.12s: %v", id, sum, err)
	}
	defer fh.Close()
	b, err := io.ReadAll(fh)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pageIDs is the ids of a page of sends, the read's error failing t.
func pageIDs(t *testing.T, p SendsPage, err error) []string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, sv := range p.Sends {
		if sv.Request != nil {
			t.Fatalf("a page of sends gives the request of %s", sv.ID)
		}
		ids = append(ids, sv.ID)
	}
	return ids
}

// pending, asked and sentTo are the ids of the store's pending sends, of
// its sends with a cancel not cancelled, and of its sends of the kind sent
// to the target.
func pending(t *testing.T, s *Store) []string {
	t.Helper()
	p, err := s.PendingSends(context.Background(), "", MaxCapturePage)
	return pageIDs(t, p, err)
}

func asked(t *testing.T, s *Store) []string {
	t.Helper()
	p, err := s.AskedCancels(context.Background(), "", MaxCapturePage)
	return pageIDs(t, p, err)
}

func sentToIDs(t *testing.T, s *Store, kind, target string) []string {
	t.Helper()
	p, err := s.CaptureSends(context.Background(), kind, target, "", MaxCapturePage)
	return pageIDs(t, p, err)
}

// ticketCaptures is the ids of the ticket's captures (Captures by Ticket).
func ticketCaptures(t *testing.T, s *Store, key string) []string {
	t.Helper()
	page, err := s.Captures(context.Background(), CaptureFilter{Ticket: key}, "", MaxCapturePage)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, c := range page.Captures {
		ids = append(ids, c.ID)
	}
	return ids
}

// ticketPage reads the ticket with the key whole.
func ticketPage(t *testing.T, s *Store, key string) TicketView {
	t.Helper()
	v, err := s.Ticket(context.Background(), key)
	if err != nil {
		t.Fatalf("ticket %s: %v", key, err)
	}
	return v
}

// A send to a new ticket makes the ticket with its description, two
// attachment rows naming the capture's screenshot and its document (one
// content, one file each), a comment naming the capture and its revision,
// and its sent outcome, the ticket's rev bumped once, in one commit; a
// send to the ticket again adds its rows to it, the description as it was.
func TestSendToANewTicket(t *testing.T) {
	ctx := context.Background()
	s, _ := attachmentTicket(t)
	img := pngOf(t, 8, 8, 60)
	n, f := testCapture(true)
	n.URL = "http://user:pw@127.0.0.1:5173/app?token=QUERYWORD#FRAGWORD"
	moment(&n)
	id := mustCreate(t, s, "c1", n, f, img)
	doc := documentOfRev(t, s, id, 1)
	told := listen(s)
	rc, err := send(t, s, "s1", id, 1, Destination{Kind: SendTicket, Project: "CND", Title: "Save is covered"}, SendFacts{})
	want(t, rc, err, StatusDone, "")
	if !ValidID(rc.ID) || rc.Key == "" || rc.Rev != 0 {
		t.Fatalf("the receipt: %+v", rc)
	}
	changes := told.one(t)
	tk := ticketPage(t, s, rc.Key)
	_, plain := newTicket(t, s, "CND", "A plain ticket")
	if tk.Title != "Save is covered" || tk.Rev != ticketPage(t, s, plain).Rev {
		t.Fatalf("the ticket: %q at r%d", tk.Title, tk.Rev)
	}
	entities := map[string]int{}
	for _, c := range changes {
		entities[c.Entity]++
	}
	if entities[EntityCaptureSend] != 1 || entities[EntityTicket] != 1 || entities[EntityComment] != 1 || entities[EntityCapture] != 0 {
		t.Fatalf("the commit's changes: %+v", changes)
	}
	for _, w := range []string{
		"> What the page served is not confirmed against the tree.\n\nCapture " + id + ", revision 1, as the page and the tree gave it:\n\n```\n" +
			"page: http://127.0.0.1:5173/app\ntitle: App\nviewport: desktop 1280 by 800\ntree: worktree\nrun: crew-0123abcd\ntask: build\nattempt: 2\n" +
			"commit: aaaaaaa\nchanged paths: 1\n```\n",
		"### Annotation 1\n\n```\nkind: element\nelement: button\nname: Save\nselector: button.save\na click at its centre reaches: div.overlay\n" +
			"source: web/app/components/SaveBar.vue\nsource class: tracked\n```\n\n> Broken\n",
		"### Annotation 2\n\n```\nkind: region\n```\n\n> here\n",
	} {
		if !strings.Contains(tk.Description, w) {
			t.Errorf("the description lacks %q:\n%s", w, tk.Description)
		}
	}
	for _, w := range []string{"QUERYWORD", "FRAGWORD", "user", "pw@"} {
		if strings.Contains(tk.Description, w) || strings.Contains(tk.Comments[0].Body, w) {
			t.Errorf("the ticket's words keep the address's %q", w)
		}
	}
	if !strings.HasPrefix(tk.Description, "> What the page served") {
		t.Errorf("the warnings are not first:\n%s", tk.Description)
	}
	png, js := fileName(Capture{CaptureDocument: CaptureDocument{ID: id, Rev: 1}}, "png"), fileName(Capture{CaptureDocument: CaptureDocument{ID: id, Rev: 1}}, "json")
	if len(tk.Attachments) != 2 {
		t.Fatalf("the attachments: %+v", tk.Attachments)
	}
	byName := map[string]Attachment{}
	for _, a := range tk.Attachments {
		byName[a.Name] = a
	}
	if a := byName[png]; a.SHA256 != sumOf(img) || a.Size != int64(len(img)) || a.ContentType != "image/png" {
		t.Errorf("the screenshot's attachment: %+v", a)
	}
	if a := byName[js]; a.SHA256 != sumOf(doc) || a.Size != int64(len(doc)) || a.ContentType != "application/json" {
		t.Errorf("the document's attachment: %+v", a)
	}
	wantComment := "Capture " + id + " r1, attached as `" + png + "` and `" + js + "`."
	if len(tk.Comments) != 1 || tk.Comments[0].Body != wantComment {
		t.Fatalf("the comments: %+v", tk.Comments)
	}
	sv := sendOf(t, s, rc.ID)
	if sv.State != SendSent || sv.Target != rc.Key || !slices.Equal(sv.Files, []string{byName[png].ID, byName[js].ID}) || sv.Kind != SendTicket ||
		sv.Rev != 1 || sv.Capture != id || sv.Digest != sumOf(doc) || sv.Request != nil || len(sv.Outcomes) != 1 || len(sv.Cancels) != 0 ||
		string(sv.Asked) != `{"kind":"ticket","project":"CND","title":"Save is covered"}` || sv.CreatedBy != "Tester" || sv.OpID != "s1" {
		t.Fatalf("the send: %+v", sv)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM capture_files WHERE capture_id = ? AND holder = ?", id, rc.ID); n != 2 {
		t.Fatalf("the send holds %d files", n)
	}
	// The ticket again: its rows added, its rev once, its description kept.
	rc2, err := send(t, s, "s2", id, 1, Destination{Kind: SendTicket, Key: rc.Key}, SendFacts{})
	want(t, rc2, err, StatusDone, "")
	again := ticketPage(t, s, rc.Key)
	if rc2.Key != rc.Key || again.Rev != tk.Rev+1 || len(again.Attachments) != 4 || len(again.Comments) != 2 || again.Description != tk.Description {
		t.Fatalf("the second send: %+v, the ticket at r%d with %d attachments", rc2, again.Rev, len(again.Attachments))
	}
	// One content, one file: the screenshot and the one document.
	if got := contentFiles(t, s); !slices.Equal(got, sortedOf(sumOf(img), sumOf(doc))) {
		t.Fatalf("the content files: %v", got)
	}
	if c := captureOf(t, s, id); len(c.Sends) != 2 || !slices.Equal(sortedOf(c.Sends[0].ID, c.Sends[1].ID), sortedOf(rc.ID, rc2.ID)) || c.Rev != 1 {
		t.Fatalf("the capture's sends: %+v", c.Sends)
	}
	if got := sentToIDs(t, s, SendTicket, rc.Key); !slices.Equal(sortedOf(got...), sortedOf(rc2.ID, rc.ID)) {
		t.Fatalf("the ticket's sends: %v", got)
	}
	if got := ticketCaptures(t, s, rc.Key); !slices.Equal(got, []string{id}) {
		t.Fatalf("the ticket's captures: %v", got)
	}
	noUploads(t, s)
	_ = ctx
}

// sortedOf is the strings sorted.
func sortedOf(ss ...string) []string {
	slices.Sort(ss)
	return ss
}

// A capture with no screenshot is sent as its document alone, to a new
// ticket and to an existing one: one attachment row, the description and
// the comment saying there is no screenshot and why, and the attachments'
// bound counting the one row.
func TestASendWithoutAScreenshotIsTheDocumentAlone(t *testing.T) {
	s, _ := attachmentTicket(t)
	n, f := testCapture(false)
	id := mustCreate(t, s, "c1", n, f, nil)
	rc, err := send(t, s, "s1", id, 1, Destination{Kind: SendTicket, Project: "CND", Title: "No pixels"}, SendFacts{})
	want(t, rc, err, StatusDone, "")
	tk := ticketPage(t, s, rc.Key)
	if len(tk.Attachments) != 1 || !strings.HasSuffix(tk.Attachments[0].Name, ".json") || tk.Attachments[0].SHA256 != sendOf(t, s, rc.ID).Digest {
		t.Fatalf("the attachments: %+v", tk.Attachments)
	}
	if !strings.HasPrefix(tk.Description, "> What the page served is not confirmed against the tree.\n> **No screenshot.**\n") ||
		!strings.Contains(tk.Description, "\nno screenshot: the person declined the tab capture\n") ||
		!strings.HasSuffix(tk.Comments[0].Body, ".json`. It has no screenshot.") {
		t.Fatalf("the words: %s / %s", tk.Description, tk.Comments[0].Body)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM capture_files WHERE holder = ?", rc.ID); n != 1 {
		t.Fatalf("the send holds %d files", n)
	}
	// The bound counts one: a ticket at 49 takes it, one at 50 refuses it.
	for range MaxAttachments - 2 {
		insertAttachment(t, s, tk.ID, 1)
	}
	rc, err = send(t, s, "s2", id, 1, Destination{Kind: SendTicket, Key: tk.Key}, SendFacts{})
	want(t, rc, err, StatusDone, "")
	tk = ticketPage(t, s, tk.Key)
	if len(tk.Attachments) != MaxAttachments || len(tk.Comments) != 2 {
		t.Fatalf("at the bound: %d attachments", len(tk.Attachments))
	}
	rc, err = send(t, s, "s3", id, 1, Destination{Kind: SendTicket, Key: tk.Key}, SendFacts{})
	want(t, rc, err, StatusFailed, RefusalBound)
	if n := count(t, s, "SELECT COUNT(*) FROM capture_sends"); n != 2 {
		t.Fatalf("%d sends", n)
	}
	noUploads(t, s)
}

// A send to a ticket whose attachments its rows would pass is refused
// bound, and leaves nothing: no send, no attachment, no comment, the
// ticket's rev as it was, no lease, and its document's file collected at
// once. A ticket that is not there, or deleted, is not_found, the same.
func TestSendToATicketPastItsAttachments(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	tk := ticketPage(t, s, key)
	for range MaxAttachments - 2 {
		insertAttachment(t, s, tk.ID, 1)
	}
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 61))
	mustSend(t, s, "s1", id, 1, Destination{Kind: SendTicket, Key: key}, SendFacts{})
	if got := len(ticketPage(t, s, key).Attachments); got != MaxAttachments {
		t.Fatalf("at the bound: %d", got)
	}
	_, other := newTicket(t, s, "CND", "Almost full")
	for range MaxAttachments - 1 {
		insertAttachment(t, s, ticketPage(t, s, other).ID, 1)
	}
	if _, err := s.DeleteTicket(ctx, testOp("del"), key); err != nil {
		t.Fatal(err)
	}
	n2, f2 := testCapture(true)
	n2.Title = "Another page"
	moment(&n2)
	id2 := mustCreate(t, s, "c2", n2, f2, pngOf(t, 8, 8, 62))
	before := ticketPage(t, s, other)
	for i, c := range []struct {
		to   Destination
		code string
	}{
		{Destination{Kind: SendTicket, Key: other}, RefusalBound},
		{Destination{Kind: SendTicket, Key: "CND-999"}, RefusalNotFound},
		{Destination{Kind: SendTicket, Key: key}, RefusalNotFound},
		{Destination{Kind: SendTicket, Project: "NOPE", Title: "x"}, RefusalNotFound},
	} {
		rc, err := send(t, s, opID("refused", i), id2, 1, c.to, SendFacts{})
		want(t, rc, err, StatusFailed, c.code)
		if n := count(t, s, "SELECT COUNT(*) FROM capture_sends WHERE capture_id = ?", id2); n != 0 {
			t.Fatalf("%+v: %d sends", c.to, n)
		}
		if exists(t, contentAt(s, sumOf(documentOfRev(t, s, id2, 1)))) {
			t.Fatalf("%+v: the document's file is left", c.to)
		}
		noUploads(t, s)
	}
	after := ticketPage(t, s, other)
	if after.Rev != before.Rev || len(after.Attachments) != MaxAttachments-1 || len(after.Comments) != 0 {
		t.Fatalf("the refused ticket: r%d, %d attachments, %d comments", after.Rev, len(after.Attachments), len(after.Comments))
	}
}

// A send's row holds its document's digest and never the document: the
// content file of that hash holds the bytes, the send's capture_files rows
// name it and the screenshot, the send opens them and nothing else, and
// no change row of the send holds the document's words.
func TestASendKeepsItsDocumentAsAFileOnly(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	img := pngOf(t, 8, 8, 63)
	n, f := testCapture(true)
	n.Annotations[0].Note = "only in the document"
	moment(&n)
	id := mustCreate(t, s, "c1", n, f, img)
	doc := documentOfRev(t, s, id, 1)
	sid := mustSend(t, s, "s1", id, 1, toBrakeman, askBrakeman)
	bn, bf := testCapture(false)
	other := mustSend(t, s, "s2", mustCreate(t, s, "c2", bn, bf, nil), 1, toBrakeman, askBrakeman)
	var digest, asked, request string
	if err := s.db.QueryRow("SELECT digest, asked, request FROM capture_sends WHERE id = ?", sid).Scan(&digest, &asked, &request); err != nil {
		t.Fatal(err)
	}
	if digest != sumOf(doc) || strings.Contains(asked+request, "only in the document") || asked != `{"kind":"brakeman"}` {
		t.Fatalf("the send's row: %s %s %s", digest, asked, request)
	}
	if b, err := os.ReadFile(contentAt(s, digest)); err != nil || string(b) != string(doc) {
		t.Fatalf("the document's file: %v", err)
	}
	if got := sendFile(t, s, sid, digest); string(got) != string(doc) {
		t.Fatal("the send's document is not the document")
	}
	if got := sendFile(t, s, sid, sumOf(img)); string(got) != string(img) {
		t.Fatal("the send's screenshot is not the screenshot")
	}
	for _, c := range []struct{ id, sum string }{{sid, sumOf([]byte("x"))}, {other, sumOf(img)}, {newID(), digest}, {"not an id", digest}, {sid, "zz"}} {
		if fh, err := s.OpenSendFile(ctx, c.id, c.sum); !errors.Is(err, ErrNotFound) || fh != nil {
			t.Fatalf("the send %q opened %.12s: %v", c.id, c.sum, err)
		}
	}
	holds := map[string]bool{}
	rows, err := s.db.Query("SELECT sha256 FROM capture_files WHERE capture_id = ? AND holder = ?", id, sid)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var sum string
		rows.Scan(&sum)
		holds[sum] = true
	}
	rows.Close()
	if len(holds) != 2 || !holds[digest] || !holds[sumOf(img)] {
		t.Fatalf("the send's files: %v", holds)
	}
	for _, row := range changesOf(t, s, "test", "s1") {
		if strings.Contains(row.Value, "only in the document") {
			t.Fatalf("the send's change row %s holds the document", row.Field)
		}
	}
}

// A person sends what they see: a send of another revision is refused
// stale_revision, one of a capture deleted or never made not_found, and a
// capture edited between its document's read and the send's transaction
// is refused stale_revision; none leaves a send, a lease or the document's
// file.
func TestASendNamesTheRevisionNow(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	img := pngOf(t, 8, 8, 64)
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, img)
	edits := kept(captureOf(t, s, id))
	edits[0].Note = "r2"
	rc, err := reedit(t, s, "e1", id, 1, edits)
	want(t, rc, err, StatusDone, "")
	only := func(what string) {
		t.Helper()
		if n := count(t, s, "SELECT COUNT(*) FROM capture_sends"); n != 0 {
			t.Fatalf("%s: %d sends", what, n)
		}
		if got := contentFiles(t, s); !slices.Equal(got, []string{sumOf(img)}) {
			t.Fatalf("%s: the files %v", what, got)
		}
		noUploads(t, s)
	}
	for _, rev := range []int64{1, 3} {
		rc, err := send(t, s, opID("stale", int(rev)), id, rev, toBrakeman, askBrakeman)
		want(t, rc, err, StatusFailed, RefusalStaleRevision)
		only(fmt.Sprintf("r%d", rev))
	}
	// Edited between the document's publication and the transaction.
	edits[0].Note = "r3"
	s.beforeCommit = func() {
		s.beforeCommit = nil
		rc, err := reedit(t, s, "e2", id, 2, edits)
		want(t, rc, err, StatusDone, "")
	}
	rc, err = send(t, s, "raced", id, 2, toBrakeman, askBrakeman)
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	only("an edit in between")
	rc, err = send(t, s, "never", newID(), 1, toBrakeman, askBrakeman)
	want(t, rc, err, StatusFailed, RefusalNotFound)
	gn, gf := testCapture(false)
	gone := mustCreate(t, s, "c2", gn, gf, nil)
	rc, err = s.DeleteCapture(ctx, testOp("d2"), gone)
	want(t, rc, err, StatusDone, "")
	rc, err = send(t, s, "deleted", gone, 1, toBrakeman, askBrakeman)
	want(t, rc, err, StatusFailed, RefusalNotFound)
	only("a deleted capture")
	sid := mustSend(t, s, "now", id, 3, toBrakeman, askBrakeman)
	if sv := sendOf(t, s, sid); sv.Rev != 3 || sv.Digest != sumOf(documentOfRev(t, s, id, 3)) {
		t.Fatalf("the send at r3: %+v", sv)
	}
}

// A pending send is settled once: two settlements at once, one is done and
// the other refused stale_revision; a resend is its receipt's, and once
// the receipts are pruned the outcome's own fingerprint answers it.
func TestAPendingSendIsSettledOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	n, f := testCapture(false)
	id := mustCreate(t, s, "c1", n, f, nil)
	sid := mustSend(t, s, "s1", id, 1, toBrakeman, askBrakeman)
	if got := pending(t, s); !slices.Equal(got, []string{sid}) {
		t.Fatalf("pending: %v", got)
	}
	rcs := make([]Receipt, 2)
	var wg sync.WaitGroup
	for i := range rcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			rcs[i], err = s.MarkSent(ctx, testOp(opID("m", i)), sid, fmt.Sprintf("br-%d", i+1))
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	won := slices.IndexFunc(rcs, func(rc Receipt) bool { return rc.Status == StatusDone })
	lost := 1 - won
	if won < 0 || rcs[lost].Status != StatusFailed || rcs[lost].Refusal != RefusalStaleRevision || rcs[won].ID != sid {
		t.Fatalf("the settlements: %+v", rcs)
	}
	sv := sendOf(t, s, sid)
	if sv.State != SendSent || sv.Target != fmt.Sprintf("br-%d", won+1) || len(sv.Outcomes) != 1 || len(pending(t, s)) != 0 {
		t.Fatalf("the send: %+v", sv)
	}
	target := fmt.Sprintf("br-%d", won+1)
	if got := sentToIDs(t, s, SendBrakeman, target); !slices.Equal(got, []string{sid}) {
		t.Fatalf("sent to %s: %v", target, got)
	}
	rc, err := s.MarkSent(ctx, testOp(opID("m", won)), sid, target)
	if err != nil || !rc.Repeat || rc.Status != StatusDone {
		t.Fatalf("the resend: %+v %v", rc, err)
	}
	if _, err := s.db.Exec("DELETE FROM ops"); err != nil {
		t.Fatal(err)
	}
	if rc, err = s.MarkSent(ctx, testOp(opID("m", won)), sid, target); err != nil || !rc.Repeat || rc.Status != StatusDone || rc.ID != sid {
		t.Fatalf("the resend, its receipt pruned: %+v %v", rc, err)
	}
	rc, err = s.MarkSent(ctx, testOp(opID("m", won)), sid, "br-99")
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	rc, err = s.MarkRefused(ctx, testOp(opID("m", won)), sid, "no")
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	rc, err = s.MarkSent(ctx, testOp(opID("m", lost)), sid, fmt.Sprintf("br-%d", lost+1))
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	if sv := sendOf(t, s, sid); len(sv.Outcomes) != 1 {
		t.Fatalf("%d outcomes", len(sv.Outcomes))
	}
}

// outcomeRow is an outcome as a replica's row: its kind, target, time,
// actor and op id, and its id (a new one when "").
type outcomeRow struct {
	kind, target  string
	at            time.Time
	actor, op, id string
}

// insertOutcomes writes a send's outcome rows in their order, as a
// replica's arrive, past the store's own rule.
func insertOutcomes(t *testing.T, s *Store, sendID string, rows ...outcomeRow) {
	t.Helper()
	for _, o := range rows {
		id := o.id
		if id == "" {
			id = newID()
		}
		if _, err := s.db.Exec("INSERT INTO capture_send_outcomes (send_id, id, kind, target, files, why, at, actor, op_id) VALUES (?, ?, ?, ?, '[]', '', ?, ?, ?)",
			sendID, id, o.kind, o.target, stamp(o.at), o.actor, o.op); err != nil {
			t.Fatal(err)
		}
	}
}

// A send's state is one projection of its outcomes: pending beside sent,
// refused beside cancelled and sent beside cancelled, and outcomes tied
// through their time and actor (told apart by the op id) and through the
// op id too (by the id), each in both orders of arrival, give one state
// and one target, the same through sendState and through every read that
// picks sends by their state.
func TestSendOutcomesMergeByTheProjection(t *testing.T) {
	s := openStore(t, storeDir(t))
	n, f := testCapture(false)
	id := mustCreate(t, s, "c1", n, f, nil)
	t0 := momentAt
	sent := outcomeRow{SendSent, "br-1", t0.Add(2 * time.Second), "b", "o1", ""}
	early := outcomeRow{SendSent, "br-2", t0.Add(time.Second), "z", "o2", ""}
	refused := outcomeRow{SendRefused, "", t0, "a", "o3", ""}
	cancelled := outcomeRow{SendCancelled, "", t0.Add(time.Second), "a", "o4", ""}
	tied := outcomeRow{SendCancelled, "", t0, "b", "o5", ""}
	late := outcomeRow{SendRefused, "", t0.Add(3 * time.Second), "a", "o6", ""}
	// Tied through at and actor: the lesser op id decides; tied through the
	// op id too: the lesser id.
	opFirst := outcomeRow{SendRefused, "", t0, "a", "p1", ""}
	opSecond := outcomeRow{SendCancelled, "", t0, "a", "p2", ""}
	sentOpFirst := outcomeRow{SendSent, "br-1", t0, "a", "p1", ""}
	sentOpSecond := outcomeRow{SendSent, "br-2", t0, "a", "p2", ""}
	idFirst := outcomeRow{SendCancelled, "", t0, "a", "p1", "0000000000000001"}
	idSecond := outcomeRow{SendRefused, "", t0, "a", "p1", "0000000000000002"}
	sentIDFirst := outcomeRow{SendSent, "br-2", t0, "a", "p1", "0000000000000001"}
	sentIDSecond := outcomeRow{SendSent, "br-1", t0, "a", "p1", "0000000000000002"}
	for i, c := range []struct {
		name   string
		rows   []outcomeRow
		state  string
		target string
	}{
		{"pending beside sent", []outcomeRow{sent}, SendSent, "br-1"},
		{"two sent", []outcomeRow{sent, early}, SendSent, "br-2"},
		{"refused beside cancelled", []outcomeRow{refused, cancelled}, SendRefused, ""},
		{"refused beside cancelled at once", []outcomeRow{refused, tied}, SendRefused, ""},
		{"cancelled before refused", []outcomeRow{cancelled, late}, SendCancelled, ""},
		{"sent beside cancelled", []outcomeRow{cancelled, sent}, SendSent, "br-1"},
		{"sent beside refused and cancelled", []outcomeRow{refused, tied, sent}, SendSent, "br-1"},
		{"tied but for the op id", []outcomeRow{opFirst, opSecond}, SendRefused, ""},
		{"two sent tied but for the op id", []outcomeRow{sentOpFirst, sentOpSecond}, SendSent, "br-1"},
		{"tied but for the id", []outcomeRow{idFirst, idSecond}, SendCancelled, ""},
		{"two sent tied but for the id", []outcomeRow{sentIDFirst, sentIDSecond}, SendSent, "br-2"},
	} {
		for j, order := range [][]outcomeRow{c.rows, arrivedBackwards(c.rows)} {
			sid := mustSend(t, s, opID(fmt.Sprintf("s%d-", i), j), id, 1, toBrakeman, askBrakeman)
			if _, err := s.db.Exec("INSERT INTO capture_send_cancels (send_id, op_id, at, actor) VALUES (?, 'k', ?, 'a')", sid, stamp(t0)); err != nil {
				t.Fatal(err)
			}
			insertOutcomes(t, s, sid, order...)
			sv := sendOf(t, s, sid)
			if sv.State != c.state || sv.Target != c.target {
				t.Errorf("%s, order %d: %s %q, want %s %q", c.name, j, sv.State, sv.Target, c.state, c.target)
			}
			state, decided := sendState(sv.Outcomes)
			if state != sv.State || (decided != nil && decided.Target != sv.Target) {
				t.Errorf("%s, order %d: sendState %s, the read %s", c.name, j, state, sv.State)
			}
			// The reads that pick sends by their state agree with it.
			if got := slices.Contains(pending(t, s), sid); got != (c.state == SendPending) {
				t.Errorf("%s, order %d: pending %v", c.name, j, got)
			}
			if got := slices.Contains(asked(t, s), sid); got != (c.state != SendCancelled) {
				t.Errorf("%s, order %d: asked %v", c.name, j, got)
			}
			for _, target := range []string{"br-1", "br-2"} {
				if got := slices.Contains(sentToIDs(t, s, SendBrakeman, target), sid); got != (c.state == SendSent && c.target == target) {
					t.Errorf("%s, order %d: sent to %s %v", c.name, j, target, got)
				}
			}
		}
	}
	// A ticket's send another replica recorded sent elsewhere first: the
	// earliest sent outcome decides its ticket, for every read of a ticket.
	s2, key := attachmentTicket(t)
	_, key2 := newTicket(t, s2, "CND", "Another")
	c2 := mustCreate(t, s2, "c1", n, f, nil)
	sid := mustSend(t, s2, "s1", c2, 1, Destination{Kind: SendTicket, Key: key}, SendFacts{})
	insertOutcomes(t, s2, sid, outcomeRow{SendSent, key2, time.Time{}.Add(time.Hour), "a", "o1", ""})
	if sv := sendOf(t, s2, sid); sv.Target != key2 || !slices.Equal(ticketCaptures(t, s2, key2), []string{c2}) || len(ticketCaptures(t, s2, key)) != 0 ||
		len(sentToIDs(t, s2, SendTicket, key)) != 0 || !slices.Equal(sentToIDs(t, s2, SendTicket, key2), []string{sid}) {
		t.Fatalf("a ticket's send sent elsewhere first: %+v", sv)
	}
	// sendState alone: an outcome of no kind it knows decides nothing.
	if state, decided := sendState([]SendOutcome{{Kind: "other"}}); state != SendPending || decided != nil {
		t.Fatalf("an unknown outcome: %s", state)
	}
}

// A page's address in a ticket's text is named without its user, query or
// fragment.
func TestAPagesAddressInATicket(t *testing.T) {
	for in, want := range map[string]string{
		"http://u:p@h:8/a/b?token=x#f": "http://h:8/a/b", "https://h/a%20b": "https://h/a%20b", "not a url": "the page",
	} {
		if got := pageAddress(in); got != want {
			t.Errorf("pageAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

// arrivedBackwards is a copy of rows in the other order of arrival.
func arrivedBackwards(rows []outcomeRow) []outcomeRow {
	out := slices.Clone(rows)
	slices.Reverse(out)
	return out
}

// A send that is not pending takes no outcome: sent (to a ticket, or by
// Brakeman), refused and cancelled sends refuse every settlement
// stale_revision, their outcomes as they were.
func TestATerminalSendTakesNoOtherOutcome(t *testing.T) {
	w := newSendWalk(t)
	for _, state := range []string{"sent to a ticket", "sent by Brakeman", "refused", "cancelled"} {
		for _, op := range []string{"mark sent", "mark refused", "cancel"} {
			w.step(t, state, op)
		}
	}
}

// sendWalk is a store with a capture and a ticket, making sends in each
// state a send can be in and taking each operation on them.
type sendWalk struct {
	s       *Store
	capture string
	key     string
	n       int
}

func newSendWalk(t *testing.T) *sendWalk {
	s, key := attachmentTicket(t)
	n, f := testCapture(false)
	return &sendWalk{s: s, capture: mustCreate(t, s, "walk", n, f, nil), key: key}
}

// sendIn makes a send in the state.
func (w *sendWalk) sendIn(t *testing.T, state string) string {
	t.Helper()
	ctx := context.Background()
	w.n++
	op := func(what string) Op { return testOp(fmt.Sprintf("%s-%d", what, w.n)) }
	if state == "sent to a ticket" {
		return mustSend(t, w.s, op("send").ID, w.capture, 1, Destination{Kind: SendTicket, Key: w.key}, SendFacts{})
	}
	sid := mustSend(t, w.s, op("send").ID, w.capture, 1, toBrakeman, askBrakeman)
	do := func(rc Receipt, err error) {
		t.Helper()
		want(t, rc, err, StatusDone, "")
	}
	switch state {
	case "pending, a cancel asked", "sent by Brakeman, a cancel asked", "cancelled":
		do(w.s.AskCancel(ctx, op("ask"), sid))
	}
	switch state {
	case "sent by Brakeman", "sent by Brakeman, a cancel asked":
		do(w.s.MarkSent(ctx, op("sent"), sid, "br-5"))
	case "refused":
		do(w.s.MarkRefused(ctx, op("refused"), sid, "Brakeman is off"))
	case "cancelled":
		do(w.s.CancelSend(ctx, op("cancelled"), sid))
	}
	return sid
}

// sendWalkStates is every state a send can be in, and sendWalkTakes what
// each operation on each leaves it: the state after, or the refusal.
var (
	sendWalkStates = []string{"pending", "pending, a cancel asked", "sent to a ticket", "sent by Brakeman", "sent by Brakeman, a cancel asked",
		"refused", "cancelled"}
	sendWalkOps   = []string{"mark sent", "mark refused", "cancel", "ask cancel"}
	sendWalkTakes = map[[2]string]string{
		{"pending", "mark sent"}: SendSent, {"pending", "mark refused"}: SendRefused, {"pending", "cancel"}: RefusalInvalid,
		{"pending", "ask cancel"}:                SendPending,
		{"pending, a cancel asked", "mark sent"}: SendSent, {"pending, a cancel asked", "mark refused"}: SendRefused,
		{"pending, a cancel asked", "cancel"}: SendCancelled, {"pending, a cancel asked", "ask cancel"}: SendPending,
		{"sent to a ticket", "ask cancel"}:                 RefusalStaleRevision,
		{"sent by Brakeman", "ask cancel"}:                 SendSent,
		{"sent by Brakeman, a cancel asked", "ask cancel"}: SendSent,
		{"refused", "ask cancel"}:                          RefusalStaleRevision,
		{"cancelled", "ask cancel"}:                        SendCancelled,
	}
)

// step takes the operation on a send made in the state, and checks what
// it leaves by sendWalkTakes (a state not pending refusing every
// settlement stale_revision), and that the reads that pick sends by their
// state agree with the send's.
func (w *sendWalk) step(t *testing.T, state, op string) {
	t.Helper()
	ctx := context.Background()
	sid := w.sendIn(t, state)
	before := sendOf(t, w.s, sid)
	o := testOp(fmt.Sprintf("step-%d", w.n))
	var rc Receipt
	var err error
	switch op {
	case "mark sent":
		rc, err = w.s.MarkSent(ctx, o, sid, "br-7")
	case "mark refused":
		rc, err = w.s.MarkRefused(ctx, o, sid, "the queue is full")
	case "cancel":
		rc, err = w.s.CancelSend(ctx, o, sid)
	case "ask cancel":
		rc, err = w.s.AskCancel(ctx, o, sid)
	}
	if err != nil {
		t.Fatalf("%s, %s: %v", state, op, err)
	}
	takes, ok := sendWalkTakes[[2]string{state, op}]
	if !ok {
		takes = RefusalStaleRevision
	}
	after := sendOf(t, w.s, sid)
	switch takes {
	case RefusalStaleRevision, RefusalInvalid:
		if rc.Status != StatusFailed || rc.Refusal != takes || len(after.Outcomes) != len(before.Outcomes) || len(after.Cancels) != len(before.Cancels) {
			t.Errorf("%s, %s: %+v, %d outcomes and %d cancels after %d and %d, want %s", state, op, rc, len(after.Outcomes), len(after.Cancels),
				len(before.Outcomes), len(before.Cancels), takes)
		}
	default:
		if rc.Status != StatusDone || rc.ID != sid || after.State != takes {
			t.Errorf("%s, %s: %+v, the send %s, want %s", state, op, rc, after.State, takes)
		}
		// A cancel is kept while one could still act; asked of a send
		// cancelled, it is answered from the state, nothing written.
		if added := len(after.Cancels) - len(before.Cancels); op == "ask cancel" && added != map[bool]int{true: 0, false: 1}[before.State == SendCancelled] {
			t.Errorf("%s, %s: %d cancels added", state, op, added)
		}
	}
	if got := slices.Contains(pending(t, w.s), sid); got != (after.State == SendPending) {
		t.Errorf("%s, %s: listed pending %v, the send %s", state, op, got, after.State)
	}
	if got := slices.Contains(asked(t, w.s), sid); got != (len(after.Cancels) > 0 && after.State != SendCancelled) {
		t.Errorf("%s, %s: listed asked %v, the send %s with %d cancels", state, op, got, after.State, len(after.Cancels))
	}
}

// Every state a send can be in, and every operation taken on it: the
// terminal rule and sendState walked whole, each allowed step to its
// state and each refused one leaving the send as it was.
func TestEveryStateOfASendTakesItsOperations(t *testing.T) {
	w := newSendWalk(t)
	for _, state := range sendWalkStates {
		for _, op := range sendWalkOps {
			w.step(t, state, op)
		}
	}
	for k := range sendWalkTakes {
		if !slices.Contains(sendWalkStates, k[0]) || !slices.Contains(sendWalkOps, k[1]) {
			t.Errorf("sendWalkTakes names %v, which the walk does not take", k)
		}
	}
}

// A pending send keeps its request, as the API made it (compacted JSON),
// whole through CaptureSend alone (a list leaves it out), through an edit
// and the capture's delete and after the send is settled; a request beside
// a ticket, none for Brakeman, or one that is no JSON object is refused.
func TestAPendingSendKeepsItsRequest(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 65))
	sid := mustSend(t, s, "s1", id, 1, toBrakeman, askBrakeman)
	compact := `{"question":"Why is Save covered?","subjects":["capture"]}`
	check := func(what, state string) {
		t.Helper()
		sv := sendOf(t, s, sid)
		if string(sv.Request) != compact || sv.State != state {
			t.Fatalf("%s: %s %s", what, sv.Request, sv.State)
		}
	}
	check("made", SendPending)
	if c := captureOf(t, s, id); len(c.Sends) != 1 || c.Sends[0].Request != nil || c.Sends[0].State != SendPending {
		t.Fatalf("the capture's sends: %+v", c.Sends)
	}
	if got := pending(t, s); !slices.Equal(got, []string{sid}) {
		t.Fatalf("pending: %v", got)
	}
	edits := kept(captureOf(t, s, id))
	edits[0].Note = "edited"
	rc, err := reedit(t, s, "e1", id, 1, edits)
	want(t, rc, err, StatusDone, "")
	check("after an edit", SendPending)
	rc, err = s.DeleteCapture(ctx, testOp("d1"), id)
	want(t, rc, err, StatusDone, "")
	check("after the delete", SendPending)
	rc, err = s.MarkSent(ctx, testOp("m1"), sid, "br-3")
	want(t, rc, err, StatusDone, "")
	check("settled", SendSent)
	on, of := testCapture(false)
	other := mustCreate(t, s, "c2", on, of, nil)
	for i, c := range []struct {
		to      Destination
		request string
		code    string
	}{
		{Destination{Kind: SendTicket, Project: "CND", Title: "x"}, compact, RefusalInvalid},
		{toBrakeman, "", RefusalInvalid},
		{toBrakeman, `["a list"]`, RefusalInvalid},
		{toBrakeman, `{"cut":`, RefusalInvalid},
		{Destination{Kind: SendBrakeman, Key: "CND-1"}, compact, RefusalInvalid},
		{Destination{Kind: SendReview}, compact, RefusalInvalid},
		{Destination{Kind: SendDecision}, compact, RefusalInvalid},
		{Destination{Kind: "elsewhere"}, compact, RefusalInvalid},
		{Destination{Kind: SendTicket}, "", RefusalInvalid},
		{Destination{Kind: SendTicket, Key: "CND-1", Title: "x"}, "", RefusalInvalid},
		{Destination{Kind: SendTicket, Key: "cnd-1"}, "", RefusalInvalid},
		{Destination{Kind: SendTicket, Project: "cnd", Title: "x"}, "", RefusalInvalid},
		{Destination{Kind: SendTicket, Project: "CND"}, "", RefusalInvalid},
	} {
		var req SendFacts
		if c.request != "" {
			req.Request = json.RawMessage(c.request)
		}
		rc, err := send(t, s, opID("bad", i), other, 1, c.to, req)
		want(t, rc, err, StatusFailed, c.code)
	}
	op := testOp("expects")
	op.ExpectedRev = 1
	rc, err = s.SendCapture(ctx, op, sendDigest(t, other, 1, toBrakeman, askBrakeman), other, 1, toBrakeman, askBrakeman)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if n := count(t, s, "SELECT COUNT(*) FROM capture_sends WHERE capture_id = ?", other); n != 0 {
		t.Fatalf("%d refused sends kept", n)
	}
	noUploads(t, s)
}

// A cancel is kept before it acts: AskCancel's row and its receipt are one
// transaction (a receipt that fails leaves no row); a pending send is
// cancelled only once one was asked; a send to a ticket and a refused one
// refuse it stale_revision; a repeat is answered by its receipt, and once
// that is pruned by its row; a send the desk took first is settled sent
// with its cancel still asked, for the settlement to finish.
func TestACancelIsKeptBeforeItActs(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	n, f := testCapture(false)
	id := mustCreate(t, s, "c1", n, f, nil)
	sid := mustSend(t, s, "s1", id, 1, toBrakeman, askBrakeman)
	rc, err := s.CancelSend(ctx, testOp("too-soon"), sid)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	failed := errors.New("the receipt's write failed")
	s.beforeReceipt = func() error { return failed }
	if _, err := s.AskCancel(ctx, testOp("k1"), sid); !errors.Is(err, failed) {
		t.Fatalf("a cancel whose receipt fails: %v", err)
	}
	s.beforeReceipt = nil
	if n := count(t, s, "SELECT COUNT(*) FROM capture_send_cancels"); n != 0 {
		t.Fatalf("%d cancels without their receipt", n)
	}
	if _, found, _ := s.Receipt(ctx, "test", "k1"); found {
		t.Fatal("a receipt without its cancel")
	}
	rc, err = s.AskCancel(ctx, testOp("k1"), sid)
	want(t, rc, err, StatusDone, "")
	if rc.ID != sid || count(t, s, "SELECT COUNT(*) FROM capture_send_cancels WHERE send_id = ? AND op_id = 'k1' AND actor = 'test'", sid) != 1 {
		t.Fatalf("the cancel: %+v", rc)
	}
	if sv := sendOf(t, s, sid); sv.State != SendPending || len(sv.Cancels) != 1 || sv.Cancels[0].OpID != "k1" {
		t.Fatalf("a cancel is no outcome: %+v", sv)
	}
	if got := asked(t, s); !slices.Equal(got, []string{sid}) {
		t.Fatalf("asked: %v", got)
	}
	again, err := s.AskCancel(ctx, testOp("k1"), sid)
	if err != nil || !again.Repeat || count(t, s, "SELECT COUNT(*) FROM capture_send_cancels") != 1 {
		t.Fatalf("a repeat: %+v %v", again, err)
	}
	rc, err = s.CancelSend(ctx, testOp("x1"), sid)
	want(t, rc, err, StatusDone, "")
	if got := asked(t, s); len(got) != 0 {
		t.Fatalf("asked after the cancel: %v", got)
	}
	// One already cancelled is answered from its state: done, nothing
	// written, and the same answer for a repeat, its receipt kept or
	// pruned (below).
	before := count(t, s, "SELECT COUNT(*) FROM changes")
	for range 2 {
		rc, err = s.AskCancel(ctx, testOp("k2"), sid)
		want(t, rc, err, StatusDone, "")
		if rc.ID != sid || count(t, s, "SELECT COUNT(*) FROM capture_send_cancels WHERE send_id = ?", sid) != 1 || count(t, s, "SELECT COUNT(*) FROM changes") != before {
			t.Fatalf("a cancel of a cancelled send: %+v", rc)
		}
	}
	if got := asked(t, s); len(got) != 0 {
		t.Fatalf("asked of a cancelled send: %v", got)
	}
	// The desk took it before the cancel acted: sent, its cancel asked.
	taken := mustSend(t, s, "s2", id, 1, toBrakeman, askBrakeman)
	rc, err = s.AskCancel(ctx, testOp("k3"), taken)
	want(t, rc, err, StatusDone, "")
	rc, err = s.MarkSent(ctx, testOp("m3"), taken, "br-4")
	want(t, rc, err, StatusDone, "")
	if got := asked(t, s); !slices.Equal(got, []string{taken}) {
		t.Fatalf("asked of a send taken: %v", got)
	}
	rc, err = s.CancelSend(ctx, testOp("x3"), taken)
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	// Nothing of a ticket's send or a refused one is under way.
	ticket := mustSend(t, s, "s4", id, 1, Destination{Kind: SendTicket, Key: key}, SendFacts{})
	rc, err = s.AskCancel(ctx, testOp("k4"), ticket)
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	refused := mustSend(t, s, "s5", id, 1, toBrakeman, askBrakeman)
	rc, err = s.MarkRefused(ctx, testOp("r5"), refused, "Brakeman is off")
	want(t, rc, err, StatusDone, "")
	rc, err = s.AskCancel(ctx, testOp("k5"), refused)
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	rc, err = s.AskCancel(ctx, testOp("k6"), newID())
	want(t, rc, err, StatusFailed, RefusalNotFound)
	// Pruned: the cancel's own row answers it, and no other operation.
	if _, err := s.db.Exec("DELETE FROM ops"); err != nil {
		t.Fatal(err)
	}
	if again, err = s.AskCancel(ctx, testOp("k1"), sid); err != nil || !again.Repeat || again.Status != StatusDone || again.ID != sid {
		t.Fatalf("a repeat, its receipt pruned: %+v %v", again, err)
	}
	rc, err = s.CancelSend(ctx, testOp("k1"), sid)
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	rc, err = s.AskCancel(ctx, testOp("k1"), taken)
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	rc, err = s.AskCancel(ctx, testOp("k2"), sid)
	if err != nil || rc.Status != StatusDone || rc.ID != sid {
		t.Fatalf("a cancel of a cancelled send, its receipt pruned: %+v %v", rc, err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM capture_send_cancels WHERE send_id = ?", sid); n != 1 {
		t.Fatalf("%d cancels", n)
	}
	// Another person's cancel whose op id one cancel of the send took:
	// refused as what it is, the id taken on this send, nothing written,
	// and the same answer again; under another id it is kept.
	other := mustSend(t, s, "s6", id, 1, toBrakeman, askBrakeman)
	rc, err = s.AskCancel(ctx, testOp("k7"), other)
	want(t, rc, err, StatusDone, "")
	bob := Op{Actor: "person:bob", ID: "k7", By: "Bob"}
	for range 2 {
		rc, err = s.AskCancel(ctx, bob, other)
		want(t, rc, err, StatusFailed, RefusalOpConflict)
		if !strings.Contains(rc.Error, "is taken on this send") || len(sendOf(t, s, other).Cancels) != 1 {
			t.Fatalf("another's cancel under a taken id: %+v", rc)
		}
	}
	bob.ID = "k8"
	rc, err = s.AskCancel(ctx, bob, other)
	want(t, rc, err, StatusDone, "")
	if c := sendOf(t, s, other).Cancels; len(c) != 2 || c[1].Actor != "person:bob" && c[0].Actor != "person:bob" {
		t.Fatalf("the cancels: %+v", c)
	}
}

// A ticket's captures are its sends: a capture is listed on a ticket only
// once a send of it was sent there; its retake, whose run was launched
// from that ticket, is related to it by the ticket (retake_via ticket) and
// is listed only once it is sent there too; a send to Brakeman or to
// another ticket lists nothing, and a delete takes the capture out of the
// list but not its send out of the ticket's sends.
func TestCapturesByTicketAreItsSends(t *testing.T) {
	ctx := context.Background()
	s, _, key := planTicket(t)
	_, key2 := makeTicket(t, s, NewTicket{Project: "CND", Title: "Another", Acceptance: "- it works"})
	launched := mustLaunch(t, s, "l1", key, simplePlan(t, s, key), "first").RunID
	launched2 := mustLaunch(t, s, "l2", key2, simplePlan(t, s, key2), "second").RunID
	n, f := testCapture(false)
	first := mustCreate(t, s, "first", n, f, nil)
	if got := ticketCaptures(t, s, key); len(got) != 0 {
		t.Fatalf("an unsent capture listed: %v", got)
	}
	mustSend(t, s, "to-brakeman", first, 1, toBrakeman, askBrakeman)
	if got := ticketCaptures(t, s, key); len(got) != 0 {
		t.Fatalf("a capture sent to Brakeman listed: %v", got)
	}
	firstSend := mustSend(t, s, "to-ticket", first, 1, Destination{Kind: SendTicket, Key: key}, SendFacts{})
	if got := ticketCaptures(t, s, key); !slices.Equal(got, []string{first}) {
		t.Fatalf("the ticket's captures: %v", got)
	}
	// Retakes: by the ticket the first was sent to, or by the repository.
	unsent := mustCreate(t, s, "unsent", n, f, nil)
	brakeman := mustCreate(t, s, "brakeman-only", n, f, nil)
	mustSend(t, s, "brakeman-only-send", brakeman, 1, toBrakeman, askBrakeman)
	retake := func(opID, of, run string) Capture {
		t.Helper()
		rn, rf := testCapture(false)
		rn.RetakeOf, rf.Run = of, run
		rc, err := create(t, s, opID, rn, rf, nil)
		want(t, rc, err, StatusDone, "")
		return captureOf(t, s, rc.ID)
	}
	for i, c := range []struct {
		name, of, run, via string
	}{
		{"run launched from the ticket the first was sent to", first, launched, RetakeTicket},
		{"run launched from another ticket", first, launched2, RetakeRepo},
		{"the first never sent", unsent, launched, RetakeRepo},
		{"the first sent to Brakeman alone", brakeman, launched, RetakeRepo},
		{"a run launched from no ticket", first, "crew-89abcdef", RetakeRepo},
		{"the same run and task beats the ticket", first, f.Run, RetakeRun},
	} {
		if got := retake(opID("retake-", i), c.of, c.run); got.RetakeVia != c.via {
			t.Errorf("%s: via %q, want %q", c.name, got.RetakeVia, c.via)
		}
	}
	second := retake("second", first, launched)
	if second.RetakeVia != RetakeTicket || !slices.Equal(ticketCaptures(t, s, key), []string{first}) {
		t.Fatalf("a retake not yet sent: %s, listed %v", second.RetakeVia, ticketCaptures(t, s, key))
	}
	mustSend(t, s, "second-to-ticket", second.ID, 1, Destination{Kind: SendTicket, Key: key}, SendFacts{})
	mustSend(t, s, "first-to-other", first, 1, Destination{Kind: SendTicket, Key: key2}, SendFacts{})
	if got := ticketCaptures(t, s, key); !slices.Equal(sortedOf(got...), sortedOf(second.ID, first)) {
		t.Fatalf("the ticket's captures, the retake sent: %v", got)
	}
	if got := ticketCaptures(t, s, key2); !slices.Equal(got, []string{first}) {
		t.Fatalf("the other ticket's captures: %v", got)
	}
	rc, err := s.DeleteCapture(ctx, testOp("delete-first"), first)
	want(t, rc, err, StatusDone, "")
	if got := ticketCaptures(t, s, key); !slices.Equal(got, []string{second.ID}) {
		t.Fatalf("the ticket's captures after a delete: %v", got)
	}
	if got := sentToIDs(t, s, SendTicket, key); len(got) != 2 || !slices.Contains(got, firstSend) {
		t.Fatalf("the ticket's sends after a delete: %v", got)
	}
	for _, bad := range []CaptureFilter{{Ticket: "not a key"}, {Ticket: key, Run: f.Run}, {Ticket: "CND-01"}} {
		if _, err := s.Captures(ctx, bad, "", 10); !errors.Is(err, ErrBadQuery) {
			t.Errorf("%+v: %v, want a bad query", bad, err)
		}
	}
}

// A deleted capture keeps its sends whole: each read whole with its
// request, the ticket's sends and the pending ones listing them, the files
// they hold opened, kept by the sweep and named by an export, the ticket's
// attachments and comment as they were, and a pending send still settled;
// the capture itself is its tombstone alone, and not listed on its ticket.
func TestADeletedCaptureKeepsItsSends(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	img := pngOf(t, 8, 8, 66)
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, img)
	doc := documentOfRev(t, s, id, 1)
	ticket := mustSend(t, s, "s1", id, 1, Destination{Kind: SendTicket, Key: key}, SendFacts{})
	brakeman := mustSend(t, s, "s2", id, 1, toBrakeman, askBrakeman)
	before := ticketPage(t, s, key)
	rc, err := s.DeleteCapture(ctx, testOp("d1"), id)
	want(t, rc, err, StatusDone, "")
	if c := captureOf(t, s, id); c.DeletedAt.IsZero() || c.Sends != nil || c.URL != "" {
		t.Fatalf("the tombstone: %+v", c)
	}
	if sv := sendOf(t, s, ticket); sv.State != SendSent || sv.Target != key || sv.Digest != sumOf(doc) || len(sv.Files) != 2 {
		t.Fatalf("the ticket's send: %+v", sv)
	}
	if sv := sendOf(t, s, brakeman); sv.State != SendPending || sv.Request == nil {
		t.Fatalf("the pending send: %+v", sv)
	}
	if got := sentToIDs(t, s, SendTicket, key); !slices.Equal(got, []string{ticket}) {
		t.Fatalf("the ticket's sends: %v", got)
	}
	if got := pending(t, s); !slices.Equal(got, []string{brakeman}) {
		t.Fatalf("pending: %v", got)
	}
	if got := ticketCaptures(t, s, key); len(got) != 0 {
		t.Fatalf("a deleted capture listed on its ticket: %v", got)
	}
	after := ticketPage(t, s, key)
	if len(after.Attachments) != 2 || len(after.Comments) != 1 || after.Rev != before.Rev {
		t.Fatalf("the ticket after the delete: %d attachments, %d comments, r%d", len(after.Attachments), len(after.Comments), after.Rev)
	}
	for _, a := range after.Attachments {
		if _, fh, err := s.OpenAttachment(ctx, a.ID); err != nil {
			t.Fatalf("the attachment %s: %v", a.Name, err)
		} else {
			fh.Close()
		}
	}
	if err := s.sweep(ctx, time.Now().Add(2*defaultSweepGrace)); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{ticket, brakeman} {
		if got := sendFile(t, s, sid, sumOf(doc)); string(got) != string(doc) {
			t.Fatalf("the send %s's document after the delete", sid)
		}
		if got := sendFile(t, s, sid, sumOf(img)); string(got) != string(img) {
			t.Fatalf("the send %s's screenshot after the delete", sid)
		}
	}
	snap := filepath.Join(t.TempDir(), "snap.sqlite")
	if err := s.Snapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if sums, err := SnapshotHashes(ctx, snap); err != nil || !slices.Equal(sums, sortedOf(sumOf(img), sumOf(doc))) {
		t.Fatalf("the snapshot's hashes: %v %v", sums, err)
	}
	if _, err := s.Document(ctx, id, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted capture's document: %v", err)
	}
	rc, err = s.MarkSent(ctx, testOp("m2"), brakeman, "br-8")
	want(t, rc, err, StatusDone, "")
}

// An edit after a send keeps what was sent: the send's digest, its file's
// bytes and the ticket's attachment are the revision sent, the edit
// writing only the capture's live rows; the revision now is sent as a send
// of its own, both files kept.
func TestAnEditAfterASendKeepsWhatWasSent(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 67))
	r1 := documentOfRev(t, s, id, 1)
	first := mustSend(t, s, "s1", id, 1, Destination{Kind: SendTicket, Key: key}, SendFacts{})
	row := func(sid string) string {
		var v string
		if err := s.db.QueryRow("SELECT id || rev || kind || asked || digest || request || created_at || fingerprint FROM capture_sends WHERE id = ?", sid).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	was := row(first)
	edits := kept(captureOf(t, s, id))
	edits[0].Note = "edited after the send"
	rc, err := reedit(t, s, "e1", id, 1, edits)
	want(t, rc, err, StatusDone, "")
	r2 := documentOfRev(t, s, id, 2)
	if string(r1) == string(r2) || row(first) != was {
		t.Fatal("the edit changed what was sent")
	}
	sv := sendOf(t, s, first)
	if sv.Digest != sumOf(r1) || string(sendFile(t, s, first, sv.Digest)) != string(r1) {
		t.Fatalf("the send after the edit: %+v", sv)
	}
	for _, a := range ticketPage(t, s, key).Attachments {
		if strings.HasSuffix(a.Name, ".json") {
			if got := readContent(t, s, a.ID); string(got) != string(r1) {
				t.Fatal("the ticket's document is not the revision sent")
			}
		}
	}
	second := mustSend(t, s, "s2", id, 2, Destination{Kind: SendTicket, Key: key}, SendFacts{})
	if sv2 := sendOf(t, s, second); sv2.Rev != 2 || sv2.Digest != sumOf(r2) || !exists(t, contentAt(s, sumOf(r1))) || !exists(t, contentAt(s, sumOf(r2))) {
		t.Fatalf("the second send: %+v", sv2)
	}
	if !strings.Contains(string(r2), "edited after the send") || strings.Contains(string(sendFile(t, s, first, sumOf(r1))), "edited after the send") {
		t.Fatal("the edit reached what was sent")
	}
	// Each send opens what it holds alone: the first not the second's.
	if fh, err := s.OpenSendFile(ctx, first, sumOf(r2)); !errors.Is(err, ErrNotFound) || fh != nil {
		t.Fatalf("the first send opened the second's document: %v", err)
	}
	_ = ctx
}

// A send's resend is answered before anything is uploaded or written: its
// receipt (Repeat, the send's id and the ticket's key), op_conflict for
// other content; once the receipt is pruned, the send's own row answers it
// with its id and key, or op_conflict, nothing sent twice.
func TestReplayAnswersASendResend(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 68))
	to := Destination{Kind: SendTicket, Key: key}
	first, err := send(t, s, "s1", id, 1, to, SendFacts{})
	want(t, first, err, StatusDone, "")
	resend := func(what string, d Destination) {
		t.Helper()
		rc, err := send(t, s, "s1", id, 1, d, SendFacts{})
		if err != nil || !rc.Repeat || rc.Status != StatusDone || rc.ID != first.ID || rc.Key != key {
			t.Fatalf("%s: %+v %v", what, rc, err)
		}
	}
	resend("a resend", to)
	rc, err := send(t, s, "s1", id, 1, Destination{Kind: SendTicket, Project: "CND", Title: "other"}, SendFacts{})
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	if got, found, err := s.Replay(ctx, testOp("s1"), IntentCaptureSend, captureArgs{ID: id, Received: sendDigest(t, id, 1, to, SendFacts{})}); err != nil || !found || got.ID != first.ID {
		t.Fatalf("Replay: %+v %v %v", got, found, err)
	}
	b1 := mustSend(t, s, "b1", id, 1, toBrakeman, askBrakeman)
	if _, err := s.db.Exec("DELETE FROM ops"); err != nil {
		t.Fatal(err)
	}
	resend("a resend, its receipt pruned", to)
	rc, err = send(t, s, "s1", id, 1, Destination{Kind: SendTicket, Project: "CND", Title: "other"}, SendFacts{})
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	if rc, err := send(t, s, "b1", id, 1, toBrakeman, askBrakeman); err != nil || !rc.Repeat || rc.ID != b1 || rc.Key != "" {
		t.Fatalf("a Brakeman send's resend, pruned: %+v %v", rc, err)
	}
	rc, err = s.MarkSent(ctx, testOp("s1"), b1, "br-1")
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	rc, err = s.CreateCapture(ctx, testOp("b1"), digestOf(t, n, nil), n, f, nil)
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	if n := count(t, s, "SELECT COUNT(*) FROM capture_sends"); n != 2 || len(ticketPage(t, s, key).Comments) != 1 {
		t.Fatalf("%d sends, %d comments", n, len(ticketPage(t, s, key).Comments))
	}
	if _, err := s.SendCapture(ctx, testOp("s9"), "not a digest", id, 1, to, SendFacts{}); !errors.Is(err, ErrBadReceived) {
		t.Fatalf("a bad digest: %v", err)
	}
	noUploads(t, s)
}

// Every bound of a send at its edge and one past: a capture's sends, the
// request, the target (its field rule's bound and its form's), why it was
// refused, a send's cancels, and the pages of sends; the ticket's
// attachments are TestSendToATicketPastItsAttachments' and
// TestASendWithoutAScreenshotIsTheDocumentAlone's, the description's cut
// TestTheFullestCaptureFitsItsDocument's.
func TestSendBoundsAtTheirEdges(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	n, f := testCapture(false)
	id := mustCreate(t, s, "c1", n, f, nil)
	var sends []string
	for i := range MaxCaptureSends {
		sends = append(sends, mustSend(t, s, opID("s", i), id, 1, toBrakeman, askBrakeman))
	}
	rc, err := send(t, s, "s-past", id, 1, toBrakeman, askBrakeman)
	want(t, rc, err, StatusFailed, RefusalBound)
	if !exists(t, contentAt(s, sumOf(documentOfRev(t, s, id, 1)))) {
		t.Fatal("a refused send took the file its siblings hold")
	}
	noUploads(t, s)
	other := mustCreate(t, s, "c2", n, f, nil)
	for _, c := range []struct {
		size int
		code string
	}{{MaxSendRequest, ""}, {MaxSendRequest + 1, RefusalBound}} {
		req := SendFacts{Request: json.RawMessage(`{"q":"` + rep("a", c.size-len(`{"q":""}`)) + `"}`)}
		rc, err := send(t, s, opID("request-", c.size), other, 1, toBrakeman, req)
		if c.code == "" {
			want(t, rc, err, StatusDone, "")
			if got := sendOf(t, s, rc.ID).Request; len(got) != MaxSendRequest {
				t.Fatalf("the request at its bound: %d bytes", len(got))
			}
		} else {
			want(t, rc, err, StatusFailed, c.code)
		}
	}
	for i, c := range []struct {
		target, code string
	}{
		{"br-1" + rep("0", 17), ""}, {"br-1" + rep("0", 18), RefusalInvalid}, {"br-" + rep("1", MaxSendTarget-3), RefusalInvalid},
		{"br-" + rep("1", MaxSendTarget-2), RefusalBound}, {"br-0", RefusalInvalid}, {"BR-1", RefusalInvalid}, {"", RefusalInvalid},
	} {
		rc, err := s.MarkSent(ctx, testOp(opID("target", i)), sends[i], c.target)
		if c.code == "" {
			want(t, rc, err, StatusDone, "")
		} else {
			want(t, rc, err, StatusFailed, c.code)
		}
	}
	for i, c := range []struct {
		why, code string
	}{{rep("é", MaxSendWhy), ""}, {rep("é", MaxSendWhy+1), RefusalBound}, {"", RefusalInvalid}, {"a\x00b", RefusalInvalid}} {
		rc, err := s.MarkRefused(ctx, testOp(opID("why", i)), sends[10+i], c.why)
		if c.code == "" {
			want(t, rc, err, StatusDone, "")
			if sendOf(t, s, sends[10+i]).Why != c.why {
				t.Fatal("why at its bound is not kept whole")
			}
		} else {
			want(t, rc, err, StatusFailed, c.code)
		}
	}
	// The words of a refusal are kept address-free.
	rc, err = s.MarkRefused(ctx, testOp("why-address"), sends[20], "see https://h.example/p?token=QUERYWORD#FRAG")
	want(t, rc, err, StatusDone, "")
	if why := sendOf(t, s, sends[20]).Why; strings.Contains(why, "QUERYWORD") || strings.Contains(why, "FRAG") || !strings.Contains(why, "https://h.example/p") {
		t.Fatalf("why kept as %q", why)
	}
	for i := range MaxSendCancels {
		rc, err := s.AskCancel(ctx, testOp(opID("cancel", i)), sends[30])
		want(t, rc, err, StatusDone, "")
	}
	rc, err = s.AskCancel(ctx, testOp("cancel-past"), sends[30])
	want(t, rc, err, StatusFailed, RefusalBound)
	// The pages: 1 to MaxCapturePage, by a cursor that names a send, each
	// send once in its order.
	for _, read := range []func(after string, limit int) (SendsPage, error){
		func(after string, limit int) (SendsPage, error) { return s.PendingSends(ctx, after, limit) },
		func(after string, limit int) (SendsPage, error) { return s.AskedCancels(ctx, after, limit) },
		func(after string, limit int) (SendsPage, error) {
			return s.CaptureSends(ctx, SendBrakeman, "br-1"+rep("0", 17), after, limit)
		},
	} {
		for _, c := range []struct {
			after string
			limit int
		}{{"", 0}, {"", MaxCapturePage + 1}, {newID(), 10}, {"not an id", 10}} {
			if p, err := read(c.after, c.limit); !errors.Is(err, ErrBadQuery) || p.Sends != nil {
				t.Errorf("after %q, %d: %v %+v, want a bad query", c.after, c.limit, err, p)
			}
		}
		if _, err := read("", MaxCapturePage); err != nil {
			t.Errorf("a page at its bound: %v", err)
		}
	}
	all := pending(t, s)
	var walked []string
	for after, pages := "", 0; pages == 0 || after != ""; pages++ {
		if pages > len(all) {
			t.Fatalf("the pages do not end: %v", walked)
		}
		p, err := s.PendingSends(ctx, after, 2)
		walked, after = append(walked, pageIDs(t, p, err)...), p.Next
	}
	if !slices.Equal(walked, all) || len(all) < 30 {
		t.Fatalf("the pages of two: %d of %d", len(walked), len(all))
	}
	for _, c := range []struct{ kind, target string }{{SendTicket, "br-1"}, {SendBrakeman, "CND-1"}, {SendReview, "x"}, {"", ""}, {SendTicket, "cnd-1"}} {
		if _, err := s.CaptureSends(ctx, c.kind, c.target, "", 10); !errors.Is(err, ErrBadQuery) {
			t.Errorf("%s to %q: %v, want a bad query", c.kind, c.target, err)
		}
	}
}

// atOnce runs two writes, each held where Apply takes the store's lock
// (beforeLock) until both have done what they do before it, so the lock
// alone orders them, and gives their receipts.
func atOnce(t *testing.T, s *Store, writes ...func() (Receipt, error)) []Receipt {
	t.Helper()
	var arrived sync.WaitGroup
	arrived.Add(len(writes))
	var n atomic.Int32
	s.mu.Lock()
	s.beforeLock = func() {
		if int(n.Add(1)) <= len(writes) {
			arrived.Done()
			arrived.Wait()
		}
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.beforeLock = nil
		s.mu.Unlock()
	}()
	rcs := make([]Receipt, len(writes))
	var wg sync.WaitGroup
	for i, write := range writes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if rcs[i], err = write(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	return rcs
}

// oneRefused fails t unless one receipt is done and the other refused
// bound.
func oneRefused(t *testing.T, what string, rcs []Receipt) {
	t.Helper()
	done := slices.IndexFunc(rcs, func(rc Receipt) bool { return rc.Status == StatusDone })
	if done < 0 || rcs[1-done].Status != StatusFailed || rcs[1-done].Refusal != RefusalBound {
		t.Fatalf("%s: %+v", what, rcs)
	}
}

// Each counted bound of a send holds under two writes at once, ordered by
// the store's lock alone: two sends of a capture at 49 sends, two sends of
// a screenshot and its document to a ticket at 48 attachments, two cancels
// of a send at 9; one is done and the other refused bound.
func TestTwoWritesAtOnceMeetTheBounds(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	n, f := testCapture(false)
	id := mustCreate(t, s, "c1", n, f, nil)
	for i := range MaxCaptureSends - 1 {
		mustSend(t, s, opID("s", i), id, 1, toBrakeman, askBrakeman)
	}
	oneRefused(t, "two sends at 49", atOnce(t, s,
		func() (Receipt, error) { return send(t, s, "a1", id, 1, toBrakeman, askBrakeman) },
		func() (Receipt, error) { return send(t, s, "a2", id, 1, toBrakeman, askBrakeman) }))
	if got := count(t, s, "SELECT COUNT(*) FROM capture_sends WHERE capture_id = ?", id); got != MaxCaptureSends {
		t.Fatalf("%d sends", got)
	}
	tk := ticketPage(t, s, key)
	for range MaxAttachments - 2 {
		insertAttachment(t, s, tk.ID, 1)
	}
	in, iff := testCapture(true)
	shot := mustCreate(t, s, "c2", in, iff, pngOf(t, 8, 8, 70))
	to := Destination{Kind: SendTicket, Key: key}
	oneRefused(t, "two sends of two rows at 48 attachments", atOnce(t, s,
		func() (Receipt, error) { return send(t, s, "b1", shot, 1, to, SendFacts{}) },
		func() (Receipt, error) { return send(t, s, "b2", shot, 1, to, SendFacts{}) }))
	if got := len(ticketPage(t, s, key).Attachments); got != MaxAttachments {
		t.Fatalf("%d attachments", got)
	}
	sid := mustSend(t, s, "d1", shot, 1, toBrakeman, askBrakeman)
	for i := range MaxSendCancels - 1 {
		rc, err := s.AskCancel(ctx, testOp(opID("k", i)), sid)
		want(t, rc, err, StatusDone, "")
	}
	oneRefused(t, "two cancels at 9", atOnce(t, s,
		func() (Receipt, error) { return s.AskCancel(ctx, testOp("k-a"), sid) },
		func() (Receipt, error) { return s.AskCancel(ctx, testOp("k-b"), sid) }))
	if got := len(sendOf(t, s, sid).Cancels); got != MaxSendCancels {
		t.Fatalf("%d cancels", got)
	}
	noUploads(t, s)
}

// Once its receipt is pruned, what an operation on a capture or a send
// kept answers a request of its own kind alone: another operation (the
// adding of a person, a ticket's create) under the id of a pruned send, a
// pruned outcome, a pruned cancel or a pruned capture's create is
// op_conflict, and writes nothing.
func TestAPrunedAnswerIsForItsOwnKind(t *testing.T) {
	ctx := context.Background()
	s, _ := attachmentTicket(t)
	n, f := testCapture(false)
	id := mustCreate(t, s, "made", n, f, nil)
	sid := mustSend(t, s, "sent", id, 1, toBrakeman, askBrakeman)
	rc, err := s.AskCancel(ctx, testOp("asked"), sid)
	want(t, rc, err, StatusDone, "")
	rc, err = s.CancelSend(ctx, testOp("ended"), sid)
	want(t, rc, err, StatusDone, "")
	if _, err := s.db.Exec("DELETE FROM ops"); err != nil {
		t.Fatal(err)
	}
	for _, opID := range []string{"made", "sent", "asked", "ended"} {
		changes := count(t, s, "SELECT COUNT(*) FROM changes")
		rc, err := s.AddPerson(ctx, testOp(opID), "Ann")
		want(t, rc, err, StatusFailed, RefusalOpConflict)
		rc, err = s.CreateTicket(ctx, testOp(opID), NewTicket{Project: "CND", Title: "Another"})
		want(t, rc, err, StatusFailed, RefusalOpConflict)
		if people, err := s.People(ctx); err != nil || len(people) != 0 || count(t, s, "SELECT COUNT(*) FROM changes") != changes {
			t.Fatalf("under %s: %+v %v", opID, people, err)
		}
	}
	// Each still answers its own.
	if rc, err := s.AskCancel(ctx, testOp("asked"), sid); err != nil || !rc.Repeat || rc.Status != StatusDone {
		t.Fatalf("the cancel's own resend: %+v %v", rc, err)
	}
	if rc, err := s.CancelSend(ctx, testOp("ended"), sid); err != nil || !rc.Repeat || rc.Status != StatusDone {
		t.Fatalf("the outcome's own resend: %+v %v", rc, err)
	}
}

// A file is held while something can still use it: a capture deleted
// whose sends ended refused or cancelled (before the delete, or after it)
// lets their screenshot and document go, the sweep leaves none, and each
// send is still read whole, its files not_found; a sent send keeps its
// files whichever came first, and a file another live capture or an
// attachment names is kept.
func TestAFileIsHeldWhileSomethingCanUseIt(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	end := func(sid, how string) {
		t.Helper()
		var rc Receipt
		var err error
		switch how {
		case SendRefused:
			rc, err = s.MarkRefused(ctx, testOp("r-"+sid), sid, "Brakeman is off")
		case SendCancelled:
			if rc, err = s.AskCancel(ctx, testOp("k-"+sid), sid); err == nil {
				want(t, rc, err, StatusDone, "")
				rc, err = s.CancelSend(ctx, testOp("x-"+sid), sid)
			}
		case SendSent:
			rc, err = s.MarkSent(ctx, testOp("m-"+sid), sid, "br-9")
		}
		want(t, rc, err, StatusDone, "")
	}
	swept := func() {
		t.Helper()
		if err := s.sweep(ctx, time.Now().Add(2*defaultSweepGrace)); err != nil {
			t.Fatal(err)
		}
	}
	made := 0
	capture := func(img []byte) (string, string) {
		t.Helper()
		made++
		n, f := testCapture(true)
		n.Title = fmt.Sprintf("page %d", made)
		moment(&n)
		id := mustCreate(t, s, opID("c", made), n, f, img)
		return id, sumOf(documentOfRev(t, s, id, 1))
	}
	gone := func(what, sid string, sums ...string) {
		t.Helper()
		for _, sum := range sums {
			if exists(t, contentAt(s, sum)) {
				t.Errorf("%s: %.12s is kept", what, sum)
			}
			if fh, err := s.OpenSendFile(ctx, sid, sum); !errors.Is(err, ErrNotFound) || fh != nil {
				t.Errorf("%s: the send opens %.12s: %v", what, sum, err)
			}
		}
		if n := count(t, s, "SELECT COUNT(*) FROM capture_files WHERE holder = ?", sid); n != 0 {
			t.Errorf("%s: the send holds %d files", what, n)
		}
	}
	kept := func(what, sid string, sums ...string) {
		t.Helper()
		for _, sum := range sums {
			if !exists(t, contentAt(s, sum)) {
				t.Errorf("%s: %.12s is gone", what, sum)
			}
		}
	}
	// Ended, then deleted.
	img := pngOf(t, 8, 8, 71)
	id, doc := capture(img)
	cancelled, refused := mustSend(t, s, "s1", id, 1, toBrakeman, askBrakeman), mustSend(t, s, "s2", id, 1, toBrakeman, askBrakeman)
	end(cancelled, SendCancelled)
	end(refused, SendRefused)
	rc, err := s.DeleteCapture(ctx, testOp("d1"), id)
	want(t, rc, err, StatusDone, "")
	swept()
	gone("cancelled, then deleted", cancelled, sumOf(img), doc)
	gone("refused, then deleted", refused, sumOf(img), doc)
	if sv := sendOf(t, s, cancelled); sv.State != SendCancelled || len(sv.Cancels) != 1 || sv.Request == nil || sendOf(t, s, refused).State != SendRefused {
		t.Fatalf("the send after its files went: %+v", sv)
	}
	// The delete is still its own first row: a resend, its receipt pruned,
	// is answered by it.
	if _, err := s.db.Exec("DELETE FROM ops"); err != nil {
		t.Fatal(err)
	}
	if rc, err := s.DeleteCapture(ctx, testOp("d1"), id); err != nil || !rc.Repeat || rc.Status != StatusDone {
		t.Fatalf("the delete's resend, pruned: %+v %v", rc, err)
	}
	// Deleted, then ended: the last send to end lets them go.
	img2 := pngOf(t, 8, 8, 72)
	id2, doc2 := capture(img2)
	first, last := mustSend(t, s, "s3", id2, 1, toBrakeman, askBrakeman), mustSend(t, s, "s4", id2, 1, toBrakeman, askBrakeman)
	rc, err = s.DeleteCapture(ctx, testOp("d2"), id2)
	want(t, rc, err, StatusDone, "")
	end(first, SendRefused)
	swept()
	kept("one of two ended", last, sumOf(img2), doc2)
	if got := sendFile(t, s, last, doc2); sumOf(got) != doc2 {
		t.Fatal("the pending send's document")
	}
	end(last, SendCancelled)
	swept()
	gone("deleted, then refused", first, sumOf(img2), doc2)
	gone("deleted, then cancelled", last, sumOf(img2), doc2)
	// Sent keeps, whichever came first; so does a send to a ticket.
	img3 := pngOf(t, 8, 8, 73)
	id3, doc3 := capture(img3)
	before, after := mustSend(t, s, "s5", id3, 1, toBrakeman, askBrakeman), mustSend(t, s, "s6", id3, 1, toBrakeman, askBrakeman)
	toTicket := mustSend(t, s, "s7", id3, 1, Destination{Kind: SendTicket, Key: key}, SendFacts{})
	end(before, SendSent)
	rc, err = s.DeleteCapture(ctx, testOp("d3"), id3)
	want(t, rc, err, StatusDone, "")
	end(after, SendSent)
	swept()
	for _, sid := range []string{before, after, toTicket} {
		kept("sent", sid, sumOf(img3), doc3)
		if got := sendFile(t, s, sid, sumOf(img3)); sumOf(got) != sumOf(img3) {
			t.Fatalf("the sent send %s's screenshot", sid)
		}
	}
	// A file something else names is kept: the screenshot by a live
	// capture, the document by an attachment.
	img4 := pngOf(t, 8, 8, 74)
	id4, doc4 := capture(img4)
	capture(img4)
	body := documentOfRev(t, s, id4, 1)
	if rc, _, err := s.AddAttachment(ctx, testOp("a1"), key, "document.json", "application/json", strings.NewReader(string(body))); err != nil || rc.Status != StatusDone {
		t.Fatalf("the attachment: %+v %v", rc, err)
	}
	ended := mustSend(t, s, "s8", id4, 1, toBrakeman, askBrakeman)
	end(ended, SendCancelled)
	rc, err = s.DeleteCapture(ctx, testOp("d4"), id4)
	want(t, rc, err, StatusDone, "")
	swept()
	kept("named elsewhere", ended, sumOf(img4), doc4)
	if n := count(t, s, "SELECT COUNT(*) FROM capture_files WHERE holder = ?", ended); n != 0 {
		t.Fatalf("the ended send holds %d files", n)
	}
	noUploads(t, s)
}

// The store's form of a Brakeman request's id is Brakeman's: the copy is
// held to internal/brakeman/refs.go's requestIDPattern, read from its
// source (this package imports none of the module's), at 18 digits and 19.
func TestBrakemansRequestIDIsTheStores(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "brakeman", "refs.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("requestIDPattern += regexp\\.MustCompile\\(`([^`]*)`\\)").FindSubmatch(src)
	if m == nil || string(m[1]) != brakemanTarget.String() {
		t.Fatalf("Brakeman's request id is %q, the store's %q", m, brakemanTarget)
	}
	for digits, ok := range map[int]bool{1: true, 18: true, 19: false} {
		if id := "br-1" + rep("0", digits-1); brakemanTarget.MatchString(id) != ok {
			t.Errorf("%s: %v", id, !ok)
		}
	}
}
