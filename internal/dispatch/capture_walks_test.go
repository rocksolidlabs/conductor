package dispatch

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

// The walks of the captures (Program item PV1, docs/plans/browser-evidence.md):
// every read the store offers after a delete, and every way out of an
// upload after its file is published.

// carries is whether a result of the type can hold a capture's words, in
// whatever shape: a string or bytes anywhere in it (a field of a struct
// inside a struct, a slice, an array or a map of them, behind a pointer),
// a file, or an interface; an error or a time alone holds none.
func carries(t reflect.Type, seen map[reflect.Type]bool) bool {
	switch t {
	case reflect.TypeFor[os.File]():
		return true
	case reflect.TypeFor[error](), reflect.TypeFor[time.Time]():
		return false
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.String, reflect.Interface:
		return true
	case reflect.Slice:
		return t.Elem().Kind() == reflect.Uint8 || carries(t.Elem(), seen)
	case reflect.Pointer, reflect.Array, reflect.Chan:
		return carries(t.Elem(), seen)
	case reflect.Map:
		return carries(t.Key(), seen) || carries(t.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if f := t.Field(i); f.IsExported() && carries(f.Type, seen) {
				return true
			}
		}
	}
	return false
}

// The walk's detector finds text in whatever shape a result holds it:
// result types it would once have missed (a summary of plain strings, a
// capture's image, text nested in structs and slices) are found, and a
// result of numbers, times and errors alone is not.
func TestTheReadWalkSeesNestedText(t *testing.T) {
	type summary struct{ Title string }
	type nested struct {
		Inner struct{ Deeper []summary }
	}
	type numbers struct {
		N  int
		At time.Time
		OK bool
	}
	for _, c := range []struct {
		name string
		t    reflect.Type
		want bool
	}{
		{"a summary of a string", reflect.TypeFor[summary](), true},
		{"a capture's image", reflect.TypeFor[CaptureImage](), true},
		{"a viewport", reflect.TypeFor[Viewport](), true},
		{"a slice of summaries in a struct in a struct", reflect.TypeFor[nested](), true},
		{"a map to pointers to summaries", reflect.TypeFor[map[int][]*summary](), true},
		{"bytes in a struct", reflect.TypeFor[struct{ B []byte }](), true},
		{"an array of summaries", reflect.TypeFor[[2]summary](), true},
		{"numbers, times and flags", reflect.TypeFor[numbers](), false},
		{"an error", reflect.TypeFor[error](), false},
		{"a slice of numbers", reflect.TypeFor[[]int64](), false},
	} {
		if got := carries(c.t, map[reflect.Type]bool{}); got != c.want {
			t.Errorf("%s: carries %v, want %v", c.name, got, c.want)
		}
	}
}

// storeReads is every exported method of the store that takes no
// operation (an operation's answer is its receipt, Replay's below) and
// whose results can hold a capture's words (carries).
func storeReads() []string {
	st := reflect.TypeFor[*Store]()
	var names []string
	for i := 0; i < st.NumMethod(); i++ {
		m := st.Method(i)
		op := false
		for in := 0; in < m.Type.NumIn(); in++ {
			op = op || m.Type.In(in) == reflect.TypeFor[Op]()
		}
		if op {
			continue
		}
		for o := 0; o < m.Type.NumOut(); o++ {
			if carries(m.Type.Out(o), map[reflect.Type]bool{}) {
				names = append(names, m.Name)
				break
			}
		}
	}
	sort.Strings(names)
	return names
}

// callRead calls the store's read by its name with an argument of each
// type the walk has: ctx, word for every string, k for every number, the
// zero value of anything else. It gives the results but the error, and the
// error.
func callRead(ctx context.Context, s *Store, name, word string, k int) ([]any, error) {
	m := reflect.ValueOf(s).MethodByName(name)
	in := make([]reflect.Value, m.Type().NumIn())
	for i := range in {
		switch at := m.Type().In(i); {
		case at == reflect.TypeFor[context.Context]():
			in[i] = reflect.ValueOf(ctx)
		case at.Kind() == reflect.String:
			in[i] = reflect.ValueOf(word).Convert(at)
		case at.Kind() >= reflect.Int && at.Kind() <= reflect.Int64:
			in[i] = reflect.ValueOf(k).Convert(at)
		default:
			in[i] = reflect.Zero(at)
		}
	}
	var results []any
	var err error
	for _, v := range m.Call(in) {
		if v.Type() == reflect.TypeFor[error]() {
			if !v.IsNil() {
				err = v.Interface().(error)
			}
			continue
		}
		results = append(results, v.Interface())
	}
	return results, err
}

// answersNothingElse fails unless every result beside a non-nil error is
// its type's zero value.
func answersNothingElse(t *testing.T, what string, results []any, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for i, r := range results {
		if v := reflect.ValueOf(r); v.IsValid() && !v.IsZero() {
			t.Errorf("%s answers %v with its result %d not its zero value: %.300s", what, err, i, encodeResults(results))
		}
	}
}

// encodeResults is the results as JSON (as Go prints them when they have
// no JSON), a file's bytes read whole and the file closed.
func encodeResults(results []any) []byte {
	var out []byte
	for _, r := range results {
		if fh, ok := r.(*os.File); ok {
			if fh != nil {
				b, _ := io.ReadAll(fh)
				fh.Close()
				out = append(out, b...)
			}
			continue
		}
		b, err := json.Marshal(r)
		if err != nil {
			b = fmt.Appendf(nil, "%+v", r)
		}
		out = append(out, b...)
	}
	return out
}

// A deleted capture is its tombstone, not found or left out through every
// read the store offers: each read whose results can hold a capture's
// words (storeReads) is driven after the delete or says why it holds none,
// a read added later in neither failing, and the feed's reads among them.
// A deleted capture's words reach a read only as a send of it froze them:
// its sends are read whole, and what it held after its sends (an edit's
// note) reaches none.
func TestEveryReadHidesADeletedCapture(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	img := pngOf(t, 8, 8, 40)
	n, f := testCapture(true)
	n.Title, n.URL, n.Viewport.UserAgent = "Walk title", "http://127.0.0.1:5173/walk-page", "WalkAgent/1"
	n.Annotations[0].Note = "walk note"
	f.ChangedPaths, f.Scope.Repo = []string{"walk/changed.vue"}, "/walk-repo"
	moment(&n)
	id := mustCreate(t, s, "walk-create", n, f, img)
	edits := kept(captureOf(t, s, id))
	edits[0].Note = "walk note edited"
	rc, err := reedit(t, s, "walk-edit", id, 1, edits)
	want(t, rc, err, StatusDone, "")
	rc, err = s.DeleteCapture(ctx, testOp("walk-delete"), id)
	want(t, rc, err, StatusDone, "")
	words := []string{"Walk title", "walk-page", "WalkAgent", "walk note", "walk/changed.vue", "/walk-repo", sumOf(img)}
	ops := []string{"walk-create", "walk-edit", "walk-delete"}
	// A capture sent, then edited, then deleted: what its sends froze stays
	// theirs (the ticket's description, attachments and comment, the files
	// the sends hold), and what was never sent reaches no read.
	sn, sf := testCapture(true)
	sn.Title, sn.URL = "Sent title", "http://127.0.0.1:5173/sent-page"
	sn.Annotations[0].Note = "sent note"
	moment(&sn)
	sent := mustCreate(t, s, "walk-sent", sn, sf, pngOf(t, 8, 8, 44))
	toTicket := mustSend(t, s, "walk-send-ticket", sent, 1, Destination{Kind: SendTicket, Key: key}, SendFacts{})
	toDesk := mustSend(t, s, "walk-send-desk", sent, 1, toBrakeman, askBrakeman)
	rc, err = s.AskCancel(ctx, testOp("walk-cancel"), toDesk)
	want(t, rc, err, StatusDone, "")
	sentEdits := kept(captureOf(t, s, sent))
	sentEdits[0].Note = "walk never sent"
	rc, err = reedit(t, s, "walk-sent-edit", sent, 1, sentEdits)
	want(t, rc, err, StatusDone, "")
	rc, err = s.DeleteCapture(ctx, testOp("walk-sent-delete"), sent)
	want(t, rc, err, StatusDone, "")
	sentDigest := sendOf(t, s, toTicket).Digest
	unsent := []string{"walk never sent"}
	ops = append(ops, "walk-sent", "walk-send-ticket", "walk-send-desk", "walk-cancel", "walk-sent-edit", "walk-sent-delete")
	enc := func(vs ...any) []byte {
		b, _ := json.Marshal(vs)
		return b
	}
	file := func(fh *os.File) []byte {
		if fh == nil {
			return nil
		}
		defer fh.Close()
		b, _ := io.ReadAll(fh)
		return b
	}
	reads := map[string]func() ([]byte, error){
		"Capture": func() ([]byte, error) {
			c, err := s.Capture(ctx, id)
			if c.DeletedAt.IsZero() || c.ID != id {
				err = fmt.Errorf("not its tombstone: %+v", c)
			}
			return enc(c), err
		},
		"Captures": func() ([]byte, error) {
			var out []byte
			for _, filter := range []CaptureFilter{{}, {Run: f.Run}, {Run: f.Run, Task: f.Task, Attempt: f.Attempt}, {Ticket: key}} {
				page, err := s.Captures(ctx, filter, "", MaxCapturePage)
				if err != nil || len(page.Captures) != 0 {
					return enc(page), fmt.Errorf("listed: %+v %v", page, err)
				}
			}
			return out, nil
		},
		"ChangeValue": func() ([]byte, error) {
			feed, err := s.ChangesAfter(ctx, 0, MaxFeedPage)
			if err != nil {
				return nil, err
			}
			var out []byte
			for _, c := range feed {
				v, err := s.ChangeValue(ctx, c.Seq)
				if err != nil {
					return nil, err
				}
				out = append(out, v...)
			}
			return out, nil
		},
		"Committed": func() ([]byte, error) {
			var out []byte
			for _, op := range ops {
				rc, _, err := s.Committed(ctx, "test", op)
				if err != nil {
					return nil, err
				}
				out = append(out, enc(rc)...)
			}
			return out, nil
		},
		"Receipt": func() ([]byte, error) {
			var out []byte
			for _, op := range ops {
				rc, _, err := s.Receipt(ctx, "test", op)
				if err != nil {
					return nil, err
				}
				out = append(out, enc(rc)...)
			}
			return out, nil
		},
		"PlanReceipt": func() ([]byte, error) {
			var out []byte
			for _, op := range ops {
				rc, bound, _, err := s.PlanReceipt(ctx, "test", op, id)
				if err != nil && !errors.Is(err, ErrNotFound) {
					return nil, err
				}
				out = append(out, enc(rc, bound)...)
			}
			return out, nil
		},
		"OpTicket": func() ([]byte, error) {
			var out []byte
			for _, op := range ops {
				k, _, err := s.OpTicket(ctx, "test", op)
				if err != nil && !errors.Is(err, ErrNotFound) {
					return nil, err
				}
				out = append(out, k...)
			}
			return out, nil
		},
		"Document": func() ([]byte, error) {
			doc, err := s.Document(ctx, id, 2)
			if !errors.Is(err, ErrNotFound) {
				return doc, fmt.Errorf("not not found: %v", err)
			}
			return doc, nil
		},
		"OpenCaptureImage": func() ([]byte, error) {
			c, fh, err := s.OpenCaptureImage(ctx, id)
			b := append(enc(c), file(fh)...)
			if !errors.Is(err, ErrNotFound) {
				return b, fmt.Errorf("not not found: %v", err)
			}
			return b, nil
		},
		// A deleted capture's sends, whole: what they froze is theirs.
		"CaptureSend": func() ([]byte, error) {
			var out []byte
			for _, sid := range []string{toTicket, toDesk} {
				sv, err := s.CaptureSend(ctx, sid)
				if err != nil {
					return nil, err
				}
				if sv.Capture != sent || sv.Rev != 1 {
					return enc(sv), fmt.Errorf("not the send as it was made: %+v", sv)
				}
				out = append(out, enc(sv)...)
			}
			return out, nil
		},
		"CaptureSends": func() ([]byte, error) {
			p, err := s.CaptureSends(ctx, SendTicket, key, "", MaxCapturePage)
			if err == nil && (len(p.Sends) != 1 || p.Sends[0].ID != toTicket) {
				err = fmt.Errorf("the ticket's sends: %+v", p)
			}
			return enc(p), err
		},
		"PendingSends": func() ([]byte, error) {
			p, err := s.PendingSends(ctx, "", MaxCapturePage)
			if err == nil && (len(p.Sends) != 1 || p.Sends[0].ID != toDesk) {
				err = fmt.Errorf("the pending sends: %+v", p)
			}
			return enc(p), err
		},
		"AskedCancels": func() ([]byte, error) {
			p, err := s.AskedCancels(ctx, "", MaxCapturePage)
			if err == nil && (len(p.Sends) != 1 || p.Sends[0].ID != toDesk) {
				err = fmt.Errorf("the asked cancels: %+v", p)
			}
			return enc(p), err
		},
		"OpenSendFile": func() ([]byte, error) {
			var out []byte
			for _, sid := range []string{toTicket, toDesk} {
				fh, err := s.OpenSendFile(ctx, sid, sentDigest)
				if err != nil {
					return nil, err
				}
				doc := file(fh)
				if !bytes.Contains(doc, []byte("sent note")) {
					return doc, errors.New("the send's document is not what was sent")
				}
				out = append(out, doc...)
				if fh, err := s.OpenSendFile(ctx, sid, sumOf(img)); !errors.Is(err, ErrNotFound) {
					return file(fh), fmt.Errorf("a send opened another capture's image: %v", err)
				}
			}
			return out, nil
		},
		"OpenAttachment": func() ([]byte, error) {
			a, fh, err := s.OpenAttachment(ctx, id)
			b := append(enc(a), file(fh)...)
			if !errors.Is(err, ErrNotFound) {
				return b, fmt.Errorf("not not found: %v", err)
			}
			return b, nil
		},
		"OpenLaunchFile": func() ([]byte, error) {
			fh, err := s.OpenLaunchFile(ctx, id, sumOf(img))
			if err == nil {
				return file(fh), errors.New("a capture's image opened as a launch's file")
			}
			return nil, nil
		},
		"History": func() ([]byte, error) {
			page, err := s.History(ctx, key, 0, 0)
			return enc(page), err
		},
		"Finding": func() ([]byte, error) {
			v, w, err := s.Finding(ctx, id)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			return enc(v, w), nil
		},
		"LaunchActor": func() ([]byte, error) {
			a, err := s.LaunchActor(ctx, id)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			return []byte(a), nil
		},
		"Instance": func() ([]byte, error) {
			v, err := s.Instance(ctx)
			return []byte(v), err
		},
		// Beside the store's reads that carries finds: the feed's rows, a
		// resend's answer and an export's hashes.
		"ChangesAfter": func() ([]byte, error) {
			feed, err := s.ChangesAfter(ctx, 0, MaxFeedPage)
			return enc(feed), err
		},
		"Replay": func() ([]byte, error) {
			rc, _, err := s.Replay(ctx, testOp("walk-create"), IntentCaptureCreate, captureArgs{Received: digestOf(t, n, img)})
			return enc(rc), err
		},
		"SnapshotHashes": func() ([]byte, error) {
			snap := filepath.Join(t.TempDir(), "walk.sqlite")
			if err := s.Snapshot(ctx, snap); err != nil {
				return nil, err
			}
			sums, err := SnapshotHashes(ctx, snap)
			return enc(sums), err
		},
	}
	// The reads driven above alone by the capture's names (below): each
	// reads the tables of what it names, none a capture's.
	other := "reads no capture's table: driven by the capture's names alone"
	notReads := map[string]string{
		"Dir":      "the store's own directory",
		"ReadOnly": "whether the store takes writes, and the store's own words why",
	}
	for _, name := range []string{"Attachment", "Blockers", "ChainHolders", "ChangedSince", "Children", "ClaimReview", "CloseState", "Comment",
		"Comments", "Findings", "Label", "Labels", "LaunchByID", "Launches", "LinkReadOf", "LinkedTicket", "Links", "List", "Marks", "Milestone", "Milestones",
		"OpenPlans", "PendingLaunches", "PendingReviews", "People", "Plan", "Plans", "Project", "Projects", "Relations", "Review", "Reviews",
		"RunTicket", "Ticket", "View", "Views"} {
		notReads[name] = other
	}
	for _, name := range storeReads() {
		_, driven := reads[name]
		if _, why := notReads[name]; !driven && !why {
			t.Errorf("the read %s is not in the walk: drive it after a delete, or say why it holds no capture", name)
		}
	}
	for name := range notReads {
		if !slices.Contains(storeReads(), name) {
			t.Errorf("notReads names %s, which is no read of the store's", name)
		}
	}
	// Every read, driven by each of the capture's names (its id, its image's
	// hash, its run and task, its create's op id), the sent capture's and its
	// sends', a capture that never was and the ticket's key for every word,
	// at 0 and 10 for every number: no result gives a word of the deleted
	// capture or what the sent one never sent, and an error answers nothing
	// else.
	for _, name := range storeReads() {
		for _, word := range []string{id, newID(), key, sumOf(img), f.Run, f.Task, "walk-create", "", sent, toTicket, toDesk, sentDigest} {
			for _, k := range []int{0, 10} {
				results, err := callRead(ctx, s, name, word, k)
				what := fmt.Sprintf("%s(%q, %d)", name, word, k)
				answersNothingElse(t, what, results, err)
				got := encodeResults(results)
				for _, w := range append(words, unsent...) {
					if bytes.Contains(got, []byte(w)) {
						t.Errorf("%s after the delete gives %q: %.300s", what, w, got)
					}
				}
			}
		}
	}
	names := make([]string, 0, len(reads))
	for name := range reads {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		got, err := reads[name]()
		if err != nil {
			t.Errorf("%s after the delete: %v", name, err)
		}
		for _, w := range append(words, unsent...) {
			if bytes.Contains(got, []byte(w)) {
				t.Errorf("%s after the delete gives %q: %.300s", name, w, got)
			}
		}
		if bytes.Contains(got, img) {
			t.Errorf("%s after the delete gives the screenshot", name)
		}
	}
}

// failingAfter is a read's queryer (the store's readQueryer) whose
// statements past the first after fail as a cancelled context fails them;
// steps counts every statement it was given.
type failingAfter struct {
	queryer
	after int64
	steps *atomic.Int64
}

func (q failingAfter) fails() bool { return q.steps.Add(1) > q.after }

func (q failingAfter) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if q.fails() {
		return nil, context.Canceled
	}
	return q.queryer.QueryContext(ctx, query, args...)
}

func (q failingAfter) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if q.fails() {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		return q.queryer.QueryRowContext(cctx, query, args...)
	}
	return q.queryer.QueryRowContext(ctx, query, args...)
}

// cancelAtEnd is an upload's reader that cancels the upload's context once
// it has given its last byte, before the upload takes its lease.
type cancelAtEnd struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (c cancelAtEnd) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err == io.EOF {
		c.cancel()
	}
	return n, err
}

// An error answers nothing else: every read of the package, driven into
// its errors (a capture deleted, one that never was, a page past its
// bound, a store closed, and a read whose statements in the store's read
// transaction fail after each of its steps), gives the zero value of every
// result beside a non-nil error; the reads of sends among them, over a
// ticket's send, a pending one with a cancel asked and one of the deleted
// capture, those of arguments of more than one kind by name (CaptureSends,
// Captures by a ticket, OpenSendFile). The failing statements reach the reads
// made through read alone (People, main's, reads outside it), and they
// fail a statement, never a read's rows or its commit after its last.
func TestAnErrorAnswersNothingElse(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	n, f := testCapture(true)
	gone := mustCreate(t, s, "gone", n, f, pngOf(t, 8, 8, 42))
	goneSend := mustSend(t, s, "gone-send", gone, 1, toBrakeman, askBrakeman)
	rc, err := s.DeleteCapture(ctx, testOp("delete"), gone)
	want(t, rc, err, StatusDone, "")
	live := mustCreate(t, s, "live", n, f, pngOf(t, 8, 8, 43))
	// The sends the reads of sends read: a ticket's, and one to Brakeman
	// pending with a cancel asked.
	ticketSend := mustSend(t, s, "ticket-send", live, 1, Destination{Kind: SendTicket, Key: key}, SendFacts{})
	deskSend := mustSend(t, s, "desk-send", live, 1, toBrakeman, askBrakeman)
	rc, err = s.AskCancel(ctx, testOp("cancel"), deskSend)
	want(t, rc, err, StatusDone, "")
	docSum := sendOf(t, s, ticketSend).Digest
	errs := 0
	drive := func(when string) {
		for _, name := range storeReads() {
			for _, word := range []string{gone, newID(), key, f.Run, "", "not an id", goneSend, ticketSend} {
				for _, k := range []int{-1, 0, MaxCapturePage + 1} {
					results, err := callRead(ctx, s, name, word, k)
					if err != nil {
						errs++
					}
					answersNothingElse(t, fmt.Sprintf("%s %s(%q, %d)", when, name, word, k), results, err)
				}
			}
		}
	}
	drive("open:")
	for _, c := range []struct {
		name string
		call func() ([]any, error)
	}{
		{"the document of a deleted capture", func() ([]any, error) { d, err := s.Document(ctx, gone, 2); return []any{d}, err }},
		{"the image of a deleted capture", func() ([]any, error) { c, fh, err := s.OpenCaptureImage(ctx, gone); return []any{c, fh}, err }},
		{"a capture that never was", func() ([]any, error) { c, err := s.Capture(ctx, newID()); return []any{c}, err }},
		{"a page past its bound", func() ([]any, error) {
			p, err := s.Captures(ctx, CaptureFilter{}, "", MaxCapturePage+1)
			return []any{p}, err
		}},
		{"a page after no capture", func() ([]any, error) { p, err := s.Captures(ctx, CaptureFilter{}, newID(), 10); return []any{p}, err }},
		{"a send that never was", func() ([]any, error) { sv, err := s.CaptureSend(ctx, newID()); return []any{sv}, err }},
		{"a page of sends after no send", func() ([]any, error) { p, err := s.PendingSends(ctx, newID(), 10); return []any{p}, err }},
		{"a ticket's sends past their bound", func() ([]any, error) {
			p, err := s.CaptureSends(ctx, SendTicket, key, "", MaxCapturePage+1)
			return []any{p}, err
		}},
		{"a file the send does not hold", func() ([]any, error) {
			fh, err := s.OpenSendFile(ctx, ticketSend, sumOf([]byte("x")))
			return []any{fh}, err
		}},
		{"the hashes of no copy", func() ([]any, error) {
			h, err := SnapshotHashes(ctx, filepath.Join(t.TempDir(), "none.sqlite"))
			return []any{h}, err
		}},
	} {
		results, err := c.call()
		if err == nil {
			t.Errorf("%s: no error", c.name)
		}
		answersNothingElse(t, c.name, results, err)
	}
	// A read that fails in its middle: every read, driven by the captures'
	// names and the seqs of their change rows, its statements failing from
	// the one after its first, then after its second, and so on until it
	// makes no statement more.
	feed, err := s.ChangesAfter(ctx, 0, MaxFeedPage)
	if err != nil {
		t.Fatal(err)
	}
	ks := []int{0, 1, 2, 10}
	for _, c := range feed {
		if c.Entity == EntityCapture {
			ks = append(ks, int(c.Seq))
		}
	}
	failed := map[string]bool{}
	inTheMiddle := func(name, what string, call func() ([]any, error)) {
		for after := int64(1); ; after++ {
			var steps atomic.Int64
			s.readQueryer = func(q queryer) queryer { return failingAfter{q, after, &steps} }
			results, err := call()
			s.readQueryer = nil
			if steps.Load() <= after {
				break
			}
			// A read whose statement failed answers the error, and nothing
			// else; it failed in its middle only when it did.
			if err == nil {
				t.Errorf("failing after %d step(s): %s answers no error: %.300s", after, what, encodeResults(results))
				continue
			}
			failed[name] = true
			answersNothingElse(t, fmt.Sprintf("failing after %d step(s): %s", after, what), results, err)
		}
	}
	for _, name := range storeReads() {
		for _, word := range []string{gone, live, newID(), key, f.Run, f.Task, "", goneSend, ticketSend, deskSend} {
			for _, k := range ks {
				inTheMiddle(name, fmt.Sprintf("%s(%q, %d)", name, word, k), func() ([]any, error) { return callRead(ctx, s, name, word, k) })
			}
		}
	}
	// The reads of sends whose arguments are of more than one kind, by name.
	for _, c := range []struct {
		name string
		call func() ([]any, error)
	}{
		{"CaptureSends", func() ([]any, error) { p, err := s.CaptureSends(ctx, SendTicket, key, "", 10); return []any{p}, err }},
		{"Captures by ticket", func() ([]any, error) {
			p, err := s.Captures(ctx, CaptureFilter{Ticket: key}, "", 10)
			return []any{p}, err
		}},
		{"OpenSendFile", func() ([]any, error) { fh, err := s.OpenSendFile(ctx, ticketSend, docSum); return []any{fh}, err }},
	} {
		inTheMiddle(c.name, c.name, func() ([]any, error) {
			results, err := c.call()
			if fh, ok := results[0].(*os.File); ok && err == nil {
				fh.Close()
			}
			return results, err
		})
	}
	for _, name := range []string{"ChangeValue", "Document", "Captures", "Capture", "CaptureSend", "PendingSends", "AskedCancels", "CaptureSends",
		"Captures by ticket", "OpenSendFile"} {
		if !failed[name] {
			t.Errorf("%s never failed in its middle: %v", name, failed)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before := errs
	drive("closed:")
	if errs == before {
		t.Fatal("no read of a closed store failed")
	}
}

// The image of a capture deleted (and the store swept) between the row's
// read and the file's open is not found, as an attachment's is, and the
// answer gives nothing else: no capture, no file.
func TestAnImageDeletedWhileOpeningIsNotFound(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	img := pngOf(t, 8, 8, 41)
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, img)
	s.beforeOpen = func() {
		s.beforeOpen = nil
		rc, err := s.DeleteCapture(ctx, testOp("d1"), id)
		want(t, rc, err, StatusDone, "")
		if err := s.sweep(ctx, time.Now()); err != nil {
			t.Error(err)
		}
	}
	c, fh, err := s.OpenCaptureImage(ctx, id)
	if fh != nil {
		fh.Close()
	}
	if exists(t, contentAt(s, sumOf(img))) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("the image opened while its capture was deleted: %v (its file there %v)", err, exists(t, contentAt(s, sumOf(img))))
	}
	if !reflect.ValueOf(c).IsZero() || fh != nil {
		t.Fatalf("the not found answer gives %+v and the file %v", c, fh)
	}
}

// A delete that releases a file collects it as it commits: captures
// created and deleted in turn leave no file behind and no sweep is needed,
// while a screenshot another capture or an attachment names stays and
// still opens; a delete answered by its receipt collects nothing.
func TestADeleteCollectsTheFileItReleases(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	n, f := testCapture(true)
	for i := range 20 {
		img := pngOf(t, 8, 8, byte(100+i))
		id := mustCreate(t, s, opID("turn", i), n, f, img)
		rc, err := s.DeleteCapture(ctx, testOp(opID("turn-delete", i)), id)
		want(t, rc, err, StatusDone, "")
		if exists(t, contentAt(s, sumOf(img))) {
			t.Fatalf("turn %d: the released screenshot is still there", i)
		}
	}
	if got := contentFiles(t, s); len(got) != 0 {
		t.Fatalf("after the creates and deletes: %v left", got)
	}
	// One screenshot another capture names, one an attachment names.
	shared, attached := pngOf(t, 8, 8, 90), pngOf(t, 8, 8, 91)
	mustPut(t, s, "attached", key, "attached.png", attached)
	keep := mustCreate(t, s, "keep", n, f, shared)
	for i, img := range [][]byte{shared, attached} {
		id := mustCreate(t, s, opID("held", i), n, f, img)
		rc, err := s.DeleteCapture(ctx, testOp(opID("held-delete", i)), id)
		want(t, rc, err, StatusDone, "")
		if !exists(t, contentAt(s, sumOf(img))) {
			t.Fatalf("a screenshot another holder names (%d) is gone", i)
		}
		// The resend, answered by its receipt, takes nothing.
		rc, err = s.DeleteCapture(ctx, testOp(opID("held-delete", i)), id)
		if want(t, rc, err, StatusDone, ""); !rc.Repeat || !exists(t, contentAt(s, sumOf(img))) {
			t.Fatalf("the resend of a delete: %+v, the file there %v", rc, exists(t, contentAt(s, sumOf(img))))
		}
	}
	_, fh, err := s.OpenCaptureImage(ctx, keep)
	if err != nil {
		t.Fatalf("the live capture's screenshot: %v", err)
	}
	fh.Close()
	rc, err := s.DeleteCapture(ctx, testOp("keep-delete"), keep)
	want(t, rc, err, StatusDone, "")
	if exists(t, contentAt(s, sumOf(shared))) || !exists(t, contentAt(s, sumOf(attached))) {
		t.Fatal("the last holder's delete kept its file, or took the attachment's")
	}
}

// Every way out of an upload after its file is published ends in the one
// collection: a failed sync of its directory, a refusal in the
// transaction, a repeat answered by another create's receipt (two creates
// at once under one op id and digest with other images), a context
// cancelled; and before it, a lease that fails. After each the store's own
// sweep finds nothing to collect, no lease or temp file is left, and a file
// another holder names is kept. An attachment's failed sync answers as
// before, its file the sweep's.
func TestEveryWayOutOfAnUploadCollectsItsFile(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	shared := pngOf(t, 8, 8, 50)
	mustPut(t, s, "shared", key, "shared.png", shared)
	settled := func(name string) {
		t.Helper()
		noUploads(t, s)
		before := contentFiles(t, s)
		for _, sum := range before {
			if named, err := isNamed(ctx, s.db, sum); err != nil || !named {
				t.Errorf("%s: the file %.12s is there and nothing names it (%v)", name, sum, err)
			}
		}
		if err := s.sweep(ctx, time.Now().Add(2*defaultSweepGrace)); err != nil {
			t.Fatal(err)
		}
		if after := contentFiles(t, s); !slices.Equal(after, before) {
			t.Errorf("%s: the sweep collected %v of %v", name, after, before)
		}
		if !exists(t, contentAt(s, sumOf(shared))) {
			t.Fatalf("%s: the attachment's file is gone", name)
		}
	}
	errSync := errors.New("the shard's sync failed")
	failSync := func(on bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.syncDir = syncDirectory
		if on {
			s.syncDir = func(p string) error {
				if len(filepath.Base(p)) == 2 {
					return errSync
				}
				return syncDirectory(p)
			}
		}
	}
	syncs := runtime.GOOS != "windows" // a Windows publish writes its move through and syncs nothing
	n, f := testCapture(true)
	if syncs {
		for i, img := range [][]byte{pngOf(t, 8, 8, 51), shared} {
			failSync(true)
			_, err := create(t, s, opID("sync", i), n, f, img)
			failSync(false)
			if !errors.Is(err, errSync) {
				t.Fatalf("a create whose sync fails: %v", err)
			}
			settled(fmt.Sprintf("a failed sync (%d)", i))
		}
	}
	// A refusal in the transaction: a retake of no capture.
	retake := n
	retake.RetakeOf = newID()
	rc, err := create(t, s, "refused", retake, f, pngOf(t, 8, 8, 52))
	want(t, rc, err, StatusFailed, RefusalInvalid)
	settled("a refusal")
	// Two creates at once under one op id and digest, other images: the
	// second answered by the first's receipt.
	a, b := pngOf(t, 8, 8, 53), pngOf(t, 8, 8, 54)
	digest := digestOf(t, n, b)
	var rcB Receipt
	var errB error
	s.beforeCommit = func() {
		s.beforeCommit = nil
		rcB, errB = s.CreateCapture(ctx, testOp("twice"), digest, n, f, bytes.NewReader(b))
	}
	rcA, errA := s.CreateCapture(ctx, testOp("twice"), digest, n, f, bytes.NewReader(a))
	want(t, rcB, errB, StatusDone, "")
	if errA != nil || rcA.Status != StatusDone || !rcA.Repeat || rcA.ID != rcB.ID {
		t.Fatalf("the repeat: %+v %v, the first %+v", rcA, errA, rcB)
	}
	if exists(t, contentAt(s, sumOf(a))) || !exists(t, contentAt(s, sumOf(b))) {
		t.Fatal("the repeat's file is kept, or the first's is gone")
	}
	settled("a repeat")
	// A context cancelled between the publication and the operation.
	cctx, cancel := context.WithCancel(ctx)
	s.beforeCommit = func() { s.beforeCommit = nil; cancel() }
	if _, err := s.CreateCapture(cctx, testOp("cancelled"), digestOf(t, n, pngOf(t, 8, 8, 55)), n, f, bytes.NewReader(pngOf(t, 8, 8, 55))); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled create: %v", err)
	}
	settled("a cancelled context")
	// A send's document takes the same ways out: its sync failing, its
	// context cancelled between the publication and the operation, a
	// refusal in the transaction (a capture's sends past their bound being
	// TestSendBoundsAtTheirEdges'), each leaving no file of it.
	sn, sf := testCapture(false)
	sendable := mustCreate(t, s, "sendable", sn, sf, nil)
	sendDoc := sumOf(documentOfRev(t, s, sendable, 1))
	if syncs {
		failSync(true)
		_, err := send(t, s, "send-sync", sendable, 1, toBrakeman, askBrakeman)
		failSync(false)
		if !errors.Is(err, errSync) || exists(t, contentAt(s, sendDoc)) {
			t.Fatalf("a send whose sync fails: %v, its document there %v", err, exists(t, contentAt(s, sendDoc)))
		}
		settled("a send whose sync fails")
	}
	cctx, cancel = context.WithCancel(ctx)
	s.beforeCommit = func() { s.beforeCommit = nil; cancel() }
	if _, err := s.SendCapture(cctx, testOp("send-cancelled"), sendDigest(t, sendable, 1, toBrakeman, askBrakeman), sendable, 1, toBrakeman,
		askBrakeman); !errors.Is(err, context.Canceled) || exists(t, contentAt(s, sendDoc)) {
		t.Fatalf("a cancelled send: %v", err)
	}
	settled("a send cancelled")
	rc, err = send(t, s, "send-refused", sendable, 1, Destination{Kind: SendTicket, Key: "CND-999"}, SendFacts{})
	want(t, rc, err, StatusFailed, RefusalNotFound)
	if exists(t, contentAt(s, sendDoc)) {
		t.Fatal("a refused send's document is left")
	}
	settled("a send refused")
	// Before the publication: a context cancelled as the upload's file is
	// read whole, so its lease fails; a capture's upload and an attachment's.
	for i, capture := range []bool{true, false} {
		img := pngOf(t, 8, 8, byte(56+i))
		lctx, cancel := context.WithCancel(ctx)
		r := cancelAtEnd{bytes.NewReader(img), cancel}
		var err error
		if capture {
			_, err = s.CreateCapture(lctx, testOp(opID("unleased", i)), digestOf(t, n, img), n, f, r)
		} else {
			_, _, err = s.AddAttachment(lctx, testOp(opID("unleased", i)), key, "u.png", "image/png", r)
		}
		cancel()
		if !errors.Is(err, context.Canceled) || exists(t, contentAt(s, sumOf(img))) {
			t.Fatalf("an upload whose lease fails (capture %v): %v", capture, err)
		}
		settled(fmt.Sprintf("a lease that fails (capture %v)", capture))
	}
	// An attachment whose sync fails answers its error, as before, and
	// leaves its file to the sweep.
	if syncs {
		content := []byte("an attachment's content")
		failSync(true)
		_, _, err := put(s, "attach-sync", key, "a.txt", content)
		failSync(false)
		if !errors.Is(err, errSync) || !exists(t, contentAt(s, sumOf(content))) {
			t.Fatalf("an attachment whose sync fails: %v, its file there %v", err, exists(t, contentAt(s, sumOf(content))))
		}
		if err := s.sweep(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		if exists(t, contentAt(s, sumOf(content))) {
			t.Fatal("the sweep kept a failed attachment's file")
		}
		settled("an attachment's failed sync")
	}
}

// The captures are listed newest first, a run's, a task's and an
// attempt's among them, in pages of at most MaxCapturePage by their
// cursor, a capture made and one deleted between two pages, the cursor's
// own capture deleted; a cursor that names no capture is refused, and a
// listing shows the first annotation's kind and note alone.
func TestCapturesAreListedNewestFirstByPage(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	var ids []string
	add := func(name string, set func(n *NewCapture, f *CaptureFacts)) string {
		n, f := testCapture(false)
		if set != nil {
			set(&n, &f)
		}
		moment(&n)
		id := mustCreate(t, s, name, n, f, nil)
		// Each its own moment, a second after the one before.
		if _, err := s.db.Exec("UPDATE captures SET created_at = ? WHERE id = ?", stamp(momentAt.Add(time.Duration(len(ids))*time.Second)), id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		return id
	}
	long := rep("é", 300)
	for i := range 3 {
		add(opID("attempt2-", i), func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Note = long })
	}
	add("attempt3", func(_ *NewCapture, f *CaptureFacts) { f.Attempt = 3 })
	add("task", func(_ *NewCapture, f *CaptureFacts) { f.Task = "test" })
	add("run", func(_ *NewCapture, f *CaptureFacts) { f.Run = "crew-89abcdef" })
	add("link", func(n *NewCapture, f *CaptureFacts) { n.Label, *f = TreeRead{}, CaptureFacts{Origin: OriginURL} })
	newest := slices.Clone(ids)
	slices.Reverse(newest)
	list := func(f CaptureFilter, after string, limit int) CapturesPage {
		t.Helper()
		page, err := s.Captures(ctx, f, after, limit)
		if err != nil {
			t.Fatalf("%+v after %q: %v", f, after, err)
		}
		return page
	}
	idsOf := func(p CapturesPage) []string {
		var out []string
		for _, c := range p.Captures {
			out = append(out, c.ID)
		}
		return out
	}
	if got := idsOf(list(CaptureFilter{}, "", MaxCapturePage)); !slices.Equal(got, newest) {
		t.Fatalf("newest first: %v, want %v", got, newest)
	}
	run, task := "crew-0123abcd", "build"
	for _, c := range []struct {
		f    CaptureFilter
		want []string
	}{
		{CaptureFilter{Run: run}, []string{ids[4], ids[3], ids[2], ids[1], ids[0]}},
		{CaptureFilter{Run: run, Task: task}, []string{ids[3], ids[2], ids[1], ids[0]}},
		{CaptureFilter{Run: run, Task: task, Attempt: 2}, []string{ids[2], ids[1], ids[0]}},
		{CaptureFilter{Run: "crew-89abcdef"}, []string{ids[5]}},
	} {
		if got := idsOf(list(c.f, "", MaxCapturePage)); !slices.Equal(got, c.want) {
			t.Errorf("%+v: %v, want %v", c.f, got, c.want)
		}
	}
	for _, c := range []struct {
		f     CaptureFilter
		after string
		limit int
	}{
		{CaptureFilter{}, "", MaxCapturePage + 1}, {CaptureFilter{}, "", 0}, {CaptureFilter{Task: task}, "", 10},
		{CaptureFilter{Run: run, Attempt: 2}, "", 10}, {CaptureFilter{}, newID(), 10},
	} {
		if _, err := s.Captures(ctx, c.f, c.after, c.limit); !errors.Is(err, ErrBadQuery) {
			t.Errorf("%+v after %q, %d: %v, want a bad query", c.f, c.after, c.limit, err)
		}
	}
	// Pages of two by their cursors, every cursor's capture live: each
	// capture once and in its order, neither repeated nor skipped, the last
	// page with no next.
	var walked []string
	for after, pages := "", 0; pages == 0 || after != ""; pages++ {
		if pages > len(ids) {
			t.Fatalf("the pages do not end: %v", walked)
		}
		page := list(CaptureFilter{}, after, 2)
		walked, after = append(walked, idsOf(page)...), page.Next
	}
	if !slices.Equal(walked, newest) {
		t.Fatalf("the pages of two: %v, want %v", walked, newest)
	}
	// Two pages and the rest: between the first and the second, a capture
	// made, one of the second page deleted and the cursor's own.
	first := list(CaptureFilter{}, "", 3)
	if !slices.Equal(idsOf(first), newest[:3]) || first.Next != newest[2] {
		t.Fatalf("the first page: %v next %q", idsOf(first), first.Next)
	}
	add("between", nil)
	for _, id := range []string{newest[3], newest[2]} {
		rc, err := s.DeleteCapture(ctx, testOp("delete-"+id), id)
		want(t, rc, err, StatusDone, "")
	}
	second := list(CaptureFilter{}, first.Next, 3)
	if !slices.Equal(idsOf(second), newest[4:7]) || second.Next != "" {
		t.Fatalf("the second page: %v next %q, want %v", idsOf(second), second.Next, newest[4:7])
	}
	c := second.Captures[2]
	if len(c.Annotations) != 1 || c.Annotations[0].Kind != AnnotationElement || c.Annotations[0].Note != rep("é", 200) ||
		c.Annotations[0].Element != nil || c.Annotations[0].Observed != nil {
		t.Fatalf("a listed capture's annotation: %+v", c.Annotations)
	}
}
