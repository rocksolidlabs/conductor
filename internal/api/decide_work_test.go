package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// The decide requests a subject has worked at once (decideWorked) and the
// response judged again once its body is read (decideAnswer). Each test
// holds requests at a seam of the server's (decideSeams.working), at a
// body its own reader gives (heldBody) or at a reply's writer
// (heldWriter), and waits on those, never on a clock.

// overWords are the fixed words of the 429 past a subject's places.
const overWords = "too many of your requests are being worked on: send this one again once one is answered"

// decideCall is one request to a decide route.
type decideCall struct {
	pass, method, path string
	body               []byte
}

// sent is a decide call's answer, taken on a goroutine of the test's.
type sent struct {
	status int
	data   []byte
	err    error
}

// code is the answer's error code.
func (s sent) code() string {
	var out map[string]any
	_ = json.Unmarshal(s.data, &out)
	return codeOf(out)
}

// send makes c and answers its reply; it touches no t, so a goroutine may
// call it.
func (f *decideFixture) send(c decideCall) sent {
	req, err := http.NewRequest(c.method, f.http.URL+c.path, bytes.NewReader(c.body))
	if err != nil {
		return sent{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+c.pass)
	resp, err := f.client.Do(req)
	if err != nil {
		return sent{err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return sent{resp.StatusCode, data, err}
}

func meCall(pass string) decideCall { return decideCall{pass, http.MethodGet, "/api/me", nil} }
func listCall(pass string) decideCall {
	return decideCall{pass, http.MethodGet, "/api/me/decisions", nil}
}
func readCall(pass, did string) decideCall {
	return decideCall{pass, http.MethodGet, "/api/decisions/" + did, nil}
}
func respondCall(pass, did, id string, rev int) decideCall {
	return decideCall{pass, http.MethodPost, "/api/decisions/" + did + "/respond", fmt.Appendf(nil, `{"responseId":%q,"choice":0,"expectedRev":%d}`, id, rev)}
}

// sixteen are sixteen of ana's calls over both her passes and the four
// routes: one list (a subject's lists are read one at a time), five of each
// of the others.
func sixteen(did string, rev int) []decideCall {
	passes := []string{passAna, passHostAna}
	calls := []decideCall{listCall(passHostAna)}
	for i := range 5 {
		p := passes[i%2]
		calls = append(calls, meCall(p), readCall(p, did), respondCall(p, did, fmt.Sprint("w-", i), rev))
	}
	return calls
}

// workHold holds the next room requests that reach the seam where their
// work begins (hold), each sending on at and waiting for a value of
// release, or its close; every other request goes through.
type workHold struct {
	room     atomic.Int32
	at       chan struct{}
	release  chan struct{}
	stop     sync.Once
	reads    atomic.Int32
	answered chan sent
}

// holdWork sets the seam, counting the ledger reads of the read and the
// list, and ends every hold at cleanup.
func (f *decideFixture) holdWork(t *testing.T) *workHold {
	t.Helper()
	h := &workHold{at: make(chan struct{}, 64), release: make(chan struct{}), answered: make(chan sent, 64)}
	read := func() { h.reads.Add(1) }
	f.srv.decideSeam.Store(&decideSeams{afterRead: read, afterList: read, working: func() {
		for n := h.room.Load(); n > 0; n = h.room.Load() {
			if h.room.CompareAndSwap(n, n-1) {
				h.at <- struct{}{}
				<-h.release
				return
			}
		}
	}})
	t.Cleanup(func() {
		h.end()
		f.srv.decideSeam.Store(nil)
	})
	return h
}

// hold holds the next n requests that reach the seam.
func (h *workHold) hold(n int32) { h.room.Store(n) }

// end lets every held request go, and every later one through.
func (h *workHold) end() {
	h.room.Store(0)
	h.stop.Do(func() { close(h.release) })
}

// start sends each call on a goroutine; its answer goes to answered.
func (h *workHold) start(f *decideFixture, calls ...decideCall) {
	for _, c := range calls {
		go func() { h.answered <- f.send(c) }()
	}
}

// waitHeld waits until n more requests are held at the seam.
func (h *workHold) waitHeld(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-h.at:
		case <-time.After(testWait):
			t.Fatalf("%d of %d requests reached the work's seam", i, n)
		}
	}
}

// collect takes n answers, each one a route answers when its work is
// done: never 429, never a 5xx.
func (h *workHold) collect(t *testing.T, n int, what string) {
	t.Helper()
	for range n {
		select {
		case s := <-h.answered:
			if s.err != nil || s.status == http.StatusTooManyRequests || s.status >= 500 {
				t.Errorf("%s: %d %v %.200s", what, s.status, s.err, s.data)
			}
		case <-time.After(testWait):
			t.Fatalf("%s: an answer never came", what)
		}
	}
}

// over checks that every call is refused past the subject's places: 429
// in the fixed words.
func (f *decideFixture) over(t *testing.T, what string, calls ...decideCall) {
	t.Helper()
	for _, c := range calls {
		s := f.send(c)
		var out map[string]any
		_ = json.Unmarshal(s.data, &out)
		if s.status != http.StatusTooManyRequests || codeOf(out) != "rate_limited" || apiMessage(out) != overWords {
			t.Errorf("%s, %s %s by %.12s: %d %.200s", what, c.method, c.path, c.pass, s.status, s.data)
		}
	}
}

// 16 of ana's requests held where the server works on them, over both her
// passes and the four routes: a 17th on any route, by either pass, is 429
// in the fixed words, the ledger not read for it and nothing written;
// bob's and the admin's are answered meanwhile; one let go, the next is
// taken, and a 17th is refused again.
func TestDecideWorksSixteenOfASubjectAtOnce(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	rev := f.rev(t, did)
	h := f.holdWork(t)
	h.hold(16)
	h.start(f, sixteen(did, rev)...)
	h.waitHeld(t, 16)
	seventeenth := []decideCall{meCall(passAna), listCall(passHostAna), readCall(passAna, did), respondCall(passHostAna, did, "over", rev)}
	f.over(t, "a 17th", seventeenth...)
	if n := h.reads.Load(); n != 0 {
		t.Errorf("the ledger was read %d times for a request past the places", n)
	}
	for _, c := range []decideCall{meCall(passBob), listCall(passBob), readCall(passBob, did), readCall(passAdmin, did), readCall(passAdminDecide, did), readCall(adminToken, did), meCall(passAdmin)} {
		if s := f.send(c); s.status != http.StatusOK {
			t.Errorf("%s %s by %.12s while ana's are held: %d %.200s", c.method, c.path, c.pass, s.status, s.data)
		}
	}
	select {
	case h.release <- struct{}{}:
	case <-time.After(testWait):
		t.Fatal("no held request took its release")
	}
	h.collect(t, 1, "the one let go")
	h.hold(1)
	h.start(f, readCall(passHostAna, did))
	h.waitHeld(t, 1)
	f.over(t, "a 17th once the next is taken", seventeenth...)
	h.end()
	h.collect(t, 16, "the held requests")
	for _, row := range f.record(t, did).Responses {
		if row.ResponseID == "over" {
			t.Errorf("the response past the places was kept: %+v", row)
		}
	}
}

// answeredAll checks that every call is answered 200.
func (f *decideFixture) answeredAll(t *testing.T, what string, calls ...decideCall) {
	t.Helper()
	for _, c := range calls {
		if s := f.send(c); s.status != http.StatusOK {
			t.Errorf("%s, %s %s by %.12s: %d %.200s", what, c.method, c.path, c.pass, s.status, s.data)
		}
	}
}

// The places are a subject's, never a name's: with eight of ana's requests
// held under her pass named Ana and eight under her pass named Anna, a 17th
// by either name is 429 and eve's passes named Ana and Anna are answered;
// with all sixteen held under the name Anna, ana's pass named Ana is 429
// and eve's named Anna is answered.
func TestDecideWorkIsCountedBySubjectNotByName(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	const (
		passAnaAnna = "pass.decide.ana-anna-3c71"
		passEveAna  = "pass.decide.eve-ana-6d42"
		passEveAnna = "pass.decide.eve-anna-a915"
	)
	decide := []ext.Grant{ext.GrantDecide}
	f.id.Add(passAnaAnna, ext.Principal{Subject: "ana", Name: "Anna", Grants: decide})
	f.id.Add(passEveAna, ext.Principal{Subject: "eve", Name: "Ana", Grants: decide})
	f.id.Add(passEveAnna, ext.Principal{Subject: "eve", Name: "Anna", Grants: decide})

	h := f.holdWork(t)
	h.hold(16)
	for range 4 {
		h.start(f, meCall(passAna), readCall(passHostAna, did), meCall(passAnaAnna), readCall(passAnaAnna, did))
	}
	h.waitHeld(t, 16)
	f.over(t, "ana's 17th by either name", meCall(passAna), readCall(passHostAna, did), meCall(passAnaAnna), readCall(passAnaAnna, did))
	f.answeredAll(t, "eve by ana's names while ana's are held", meCall(passEveAna), readCall(passEveAna, did), meCall(passEveAnna), readCall(passEveAnna, did), listCall(passEveAna))
	h.end()
	h.collect(t, 16, "ana's held requests by both names")

	h = f.holdWork(t)
	h.hold(16)
	for range 8 {
		h.start(f, meCall(passAnaAnna), readCall(passAnaAnna, did))
	}
	h.waitHeld(t, 16)
	f.over(t, "ana's 17th by the name none of hers are held under", meCall(passAna), readCall(passHostAna, did), listCall(passAna))
	f.answeredAll(t, "eve named Anna while ana's are held under that name", meCall(passEveAnna), readCall(passEveAnna, did), listCall(passEveAnna))
	h.end()
	h.collect(t, 16, "ana's held requests named Anna")
}

// heldBody is a request's body its sender holds back: first, then, once
// that is read, a wait for gate (at told as it starts), then rest.
type heldBody struct {
	first, rest []byte
	at, gate    chan struct{}
	waited      bool
}

func newHeldBody(c decideCall) *heldBody {
	return &heldBody{first: c.body[:len(c.body)/2], rest: c.body[len(c.body)/2:], at: make(chan struct{}), gate: make(chan struct{})}
}

func (b *heldBody) Read(p []byte) (int, error) {
	if len(b.first) > 0 {
		n := copy(p, b.first)
		b.first = b.first[n:]
		return n, nil
	}
	if !b.waited {
		b.waited = true
		close(b.at)
		<-b.gate
	}
	if len(b.rest) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.rest)
	b.rest = b.rest[n:]
	return n, nil
}

// serveHeld serves c with body and w on a goroutine of the test's; served
// is closed when the handler returns.
func (f *decideFixture) serveHeld(c decideCall, body io.Reader, w http.ResponseWriter) (served chan struct{}) {
	req := httptest.NewRequest(c.method, c.path, body)
	req.Header.Set("Authorization", "Bearer "+c.pass)
	served = make(chan struct{})
	go func() {
		defer close(served)
		f.http.Config.Handler.ServeHTTP(w, req)
	}()
	return served
}

// opened closes each gate not yet closed.
func opened(gates ...chan struct{}) {
	for _, g := range gates {
		select {
		case <-g:
		default:
			close(g)
		}
	}
}

// waitServed waits for each served.
func waitServed(t *testing.T, what string, served ...chan struct{}) {
	t.Helper()
	for _, s := range served {
		select {
		case <-s:
		case <-time.After(testWait):
			t.Fatalf("%s never ended", what)
		}
	}
}

// A request being received and one being sent are not counted: with 16
// responses of ana's whose bodies she holds back and 16 reads whose replies
// her reader does not take, a read of hers is answered, 16 more are taken
// where the server works, and a 17th is 429; once let go, each is
// answered.
func TestDecideWorkCountsNoTransfer(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	rev := f.rev(t, did)
	var gates []chan struct{}
	t.Cleanup(func() { opened(gates...) })
	var bodies, writers []chan struct{}
	var recorders []*httptest.ResponseRecorder
	passes := []string{passAna, passHostAna}
	for i := range 16 {
		c := respondCall(passes[i%2], did, fmt.Sprint("b-", i), rev)
		b := newHeldBody(c)
		gates = append(gates, b.gate)
		bodies = append(bodies, f.serveHeld(c, b, httptest.NewRecorder()))
		reached(t, b.at, fmt.Sprint("held body ", i))
		w := heldWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
		gates = append(gates, w.gate)
		recorders = append(recorders, w.ResponseRecorder)
		writers = append(writers, f.serveHeld(readCall(passes[i%2], did), nil, w))
		reached(t, w.at, fmt.Sprint("held reply ", i))
	}
	f.view(t, passAna, did)
	h := f.holdWork(t)
	h.hold(16)
	for i := range 16 {
		h.start(f, readCall(passes[i%2], did))
	}
	h.waitHeld(t, 16)
	f.over(t, "a 17th beside the held transfers", readCall(passAna, did), meCall(passHostAna))
	h.end()
	h.collect(t, 16, "the reads taken")
	opened(gates...)
	waitServed(t, "a held body's response", bodies...)
	waitServed(t, "a held reply's read", writers...)
	for i, w := range recorders {
		if w.Code != http.StatusOK {
			t.Errorf("held reply %d: %d %.200s", i, w.Code, w.Body.Bytes())
		}
	}
	if rec := f.record(t, did); len(rec.Responses) == 0 {
		t.Errorf("no held body's response was taken: %+v", rec)
	}
}

// Every way out of the work lets its place go: after 16 panics where the
// server works (500), 16 refusals (404, a decision not given), and 16
// lists the ledger cannot read (503), 16 more are taken and a 17th is
// 429.
func TestDecideWorkEndsOnEveryWayOut(t *testing.T) {
	f := newDecideFixture(t, nil)
	did, other := f.open(t, "r1"), f.open(t, "r2")
	f.give(t, did)
	var panics atomic.Int32
	f.srv.decideSeam.Store(&decideSeams{working: func() {
		if panics.Add(1) <= 16 {
			panic("the work's seam")
		}
	}})
	t.Cleanup(func() { f.srv.decideSeam.Store(nil) })
	passes := []string{passAna, passHostAna}
	for i := range 16 {
		if s := f.send(readCall(passes[i%2], did)); s.status != http.StatusInternalServerError || s.code() != "internal" {
			t.Fatalf("panic %d: %d %.200s", i, s.status, s.data)
		}
	}
	for i := range 16 {
		if s := f.send(readCall(passes[i%2], other)); s.status != http.StatusNotFound || s.code() != "not_found" {
			t.Fatalf("refusal %d: %d %.200s", i, s.status, s.data)
		}
	}
	f.rawWrite(t, "DROP INDEX decisions_open")
	for i := range 16 {
		if s := f.send(listCall(passes[i%2])); s.status != http.StatusServiceUnavailable || s.code() != "ledger_unavailable" {
			t.Fatalf("unread list %d: %d %.200s", i, s.status, s.data)
		}
	}
	h := f.holdWork(t)
	h.hold(16)
	for i := range 16 {
		h.start(f, readCall(passes[i%2], did))
	}
	h.waitHeld(t, 16)
	f.over(t, "a 17th after every way out", meCall(passAna))
	h.end()
	h.collect(t, 16, "the 16 taken")
}

// A list whose 503 its reader does not take does not keep the subject's
// next list refused: each next list, by either pass, is the same 503,
// never 429; the held one is then written whole.
func TestDecideListUnreadReplyFreesTheNextList(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	f.list(t, passAna)
	f.rawWrite(t, "DROP INDEX decisions_open")
	w := heldWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	t.Cleanup(func() { opened(w.gate) })
	served := f.serveHeld(listCall(passAna), nil, w)
	reached(t, w.at, "the unread list's 503")
	for _, pass := range []string{passAna, passHostAna} {
		if s := f.send(listCall(pass)); s.status != http.StatusServiceUnavailable || s.code() != "ledger_unavailable" {
			t.Errorf("ana's next list (%.12s) while the first's 503 is held: %d %.200s", pass, s.status, s.data)
		}
	}
	opened(w.gate)
	waitServed(t, "the held list", served)
	if w.Code != http.StatusServiceUnavailable || !bytes.Contains(w.Body.Bytes(), []byte("ledger_unavailable")) {
		t.Errorf("the held list: %d %s", w.Code, w.Body.Bytes())
	}
}

// A response whose pass the provider stops admitting while its body is
// sent (no longer known, without the decide grant, another subject's) is
// answered as the gate answers a pass it does not admit, 401 in the same
// words; no response is kept and the decision is unchanged.
func TestDecideResponseIsJudgedOnceItsBodyIsRead(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	rev := f.rev(t, did)
	gate := f.send(respondCall(passUnknown, did, "g", rev))
	ana := ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantDecide}}
	hostOnly, eve := ana, ext.Principal{Subject: "eve", Name: "Eve", Grants: ana.Grants}
	hostOnly.Grants = []ext.Grant{ext.GrantHost}
	for i, change := range []func(){
		func() { f.id.Remove(passAna) },
		func() { f.id.Add(passAna, hostOnly) },
		func() { f.id.Add(passAna, eve) },
	} {
		c := respondCall(passAna, did, fmt.Sprint("a-", i), rev)
		b := newHeldBody(c)
		t.Cleanup(func() { opened(b.gate) })
		w := httptest.NewRecorder()
		served := f.serveHeld(c, b, w)
		reached(t, b.at, fmt.Sprint("response ", i))
		change()
		opened(b.gate)
		waitServed(t, fmt.Sprint("response ", i), served)
		if w.Code != gate.status || w.Code != http.StatusUnauthorized || !bytes.Equal(w.Body.Bytes(), gate.data) || w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("response %d: %d %s, the gate's %d %s", i, w.Code, w.Body.Bytes(), gate.status, gate.data)
		}
		f.id.Add(passAna, ana)
	}
	if rec := f.record(t, did); rec.Rev != rev || len(rec.Responses) != 0 {
		t.Errorf("the decision after the responses: rev %d (was %d), %d responses", rec.Rev, rev, len(rec.Responses))
	}
}

// A response takes its place before its pass is asked about again: with
// ana's sixteen places held, her response is 429 in the fixed words, the
// provider asked about its pass at the gate alone, the decision not read
// and nothing recorded; once a place is let go, the same response id is
// taken.
func TestDecideResponseIsCountedBeforeItsPassIsAskedAgain(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	rev := f.rev(t, did)
	const passAnaRespond = "pass.decide.ana-respond-58e0"
	f.id.Add(passAnaRespond, ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantDecide}})
	asks := func() int {
		f.id.mu.Lock()
		defer f.id.mu.Unlock()
		return f.id.asked[passAnaRespond]
	}
	h := f.holdWork(t)
	h.hold(16)
	for range 8 {
		h.start(f, meCall(passAna), meCall(passHostAna))
	}
	h.waitHeld(t, 16)
	before := asks()
	f.over(t, "a response past the places", respondCall(passAnaRespond, did, "past", rev))
	if n := asks() - before; n != 1 {
		t.Errorf("the provider was asked about the pass %d times for the response past the places, not once (the gate's)", n)
	}
	if n := h.reads.Load(); n != 0 {
		t.Errorf("the decision was read %d times for the response past the places", n)
	}
	if rec := f.record(t, did); rec.Rev != rev || len(rec.Responses) != 0 {
		t.Errorf("the decision after the response past the places: rev %d (was %d), %d responses", rec.Rev, rev, len(rec.Responses))
	}
	select {
	case h.release <- struct{}{}:
	case <-time.After(testWait):
		t.Fatal("no held request took its release")
	}
	h.collect(t, 1, "the one let go")
	before = asks()
	if s := f.send(respondCall(passAnaRespond, did, "past", rev)); s.status != http.StatusCreated {
		t.Fatalf("the same response once a place is free: %d %.200s", s.status, s.data)
	}
	if n := asks() - before; n < 2 {
		t.Errorf("the provider was asked about the pass %d times for the response taken, not at the gate and again in its place", n)
	}
	kept := false
	for _, row := range f.record(t, did).Responses {
		kept = kept || row.ResponseID == "past"
	}
	if !kept {
		t.Error("the response taken once a place was free is not in the record")
	}
	h.end()
	h.collect(t, 15, "the held requests")
}
