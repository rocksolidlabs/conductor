package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/export"
)

// What other machines hold (remote.go, sync_status.go).

var remoteAt = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

// remoteOf is a record of kind from device, its body body.
func remoteOf(kind, device, id string, rev int64, body any) ext.Remote {
	b, _ := json.Marshal(body)
	return ext.Remote{Kind: kind, Device: device, DeviceName: "studio", ID: id, Rev: rev, At: remoteAt, Data: b}
}

// remoteDecisionOf is a decision's document as another machine hands it.
func remoteDecisionOf(id, ticket, state string) ext.DecisionDoc {
	doc := ext.DecisionDoc{ID: id, Run: "team-00000009", OpenedIn: "team-00000009", Origin: ext.DecisionOriginPlan, Type: "approval", Gate: "ship", Ticket: ticket,
		Request:  &ext.DecisionDocRequest{Question: "Ship it on studio?", Options: []ext.DecisionDocOption{{Label: "Approve"}, {Label: "Reject"}}},
		OpenedTo: ext.DecisionOpenedToOperator, Policy: json.RawMessage(`{}`), State: state, Rev: 1, CreatedAt: remoteAt, States: ext.DecisionDocStates{Open: state == "open"}}
	p, _ := doc.RequestPart()
	doc.RequestSHA256, _ = p.SHA256()
	return doc
}

// remoteRecords reads GET /api/remote with query and answers its records
// and next.
func remoteRecords(t *testing.T, e *testEnv, query string) ([]remoteRecord, string) {
	t.Helper()
	code, b, _ := e.call(t, http.MethodGet, "/api/remote"+query, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/remote%s: %d %s", query, code, b)
	}
	var reply struct {
		Records []remoteRecord `json:"records"`
		Next    string         `json:"next"`
	}
	if err := json.Unmarshal(b, &reply); err != nil || reply.Records == nil {
		t.Fatalf("the reply %s: %v", b, err)
	}
	return reply.Records, reply.Next
}

// The sync store's doors keep, replace, delete and clear other machines'
// records, each kind under its check; GET /api/remote reads them by kind,
// by ticket and in pages, and refuses a bad query; with no records it
// answers none.
func TestRemoteRoutes(t *testing.T) {
	ctx := context.Background()
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	h := &extHost{s: e.srv, name: f.Name(), ctx: ctx}
	if recs, _ := remoteRecords(t, e, ""); len(recs) != 0 {
		t.Fatalf("records before any: %v", recs)
	}
	e.newDispatchProject(t, "MINE", nil)
	tk := e.newDispatchTicket(t, "MINE", "ours", nil)
	key, ticketID := tk["key"].(string), tk["id"].(string)
	// A run of this server's, which the ticket links: its id names
	// another machine's pointer too here (a run link from elsewhere names
	// that machine's run the same way).
	if err := e.srv.records.Save(crew.Run{ID: "team-00000003", CrewID: "team", Name: "Team", StartedAt: remoteAt, State: crew.RunFinished}); err != nil {
		t.Fatal(err)
	}
	e.expect(t, http.MethodPost, "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "run", "ref": "team-00000003"}, http.StatusCreated, "")
	puts := []ext.Remote{
		remoteOf(ext.RemoteDecision, "dev-1", "dr-7", 1, remoteDecisionOf("dr-7", ticketID, "open")),
		remoteOf(ext.RemoteDecision, "dev-1", "dr-8", 1, remoteDecisionOf("dr-8", "", "open")),
		remoteOf(ext.RemotePointer, "dev-1", "team-00000002", 4, map[string]any{"id": "team-00000002", "crew": "team", "state": "running", "revision": 4, "ticket": ticketID}),
		remoteOf(ext.RemotePointer, "dev-1", "team-00000003", 1, map[string]any{"id": "team-00000003", "crew": "team", "state": "running", "revision": 1}),
		remoteOf(ext.RemotePointer, "dev-1", "team-00000004", 1, map[string]any{"id": "team-00000004", "crew": "team", "state": "running", "revision": 1}),
		remoteOf(ext.RemotePins, "dev-1", "pins", 2, map[string]any{"v": 1, "pins": []any{map[string]any{"session": "s1", "name": "build"}}}),
		remoteOf(ext.RemoteDevice, "dev-1", "dev-2", 1, map[string]any{"id": "dev-2", "name": "laptop", "kind": "server"}),
		remoteOf(ext.RemoteHosts, "dev-1", "summary", 1, map[string]any{"hosts": []any{}}),
	}
	for _, r := range puts {
		if err := h.PutRemote(ctx, r); err != nil {
			t.Fatalf("%s %s: %v", r.Kind, r.ID, err)
		}
	}
	if recs, _ := remoteRecords(t, e, ""); len(recs) != len(puts) {
		t.Fatalf("%d records, not %d", len(recs), len(puts))
	}
	if recs, _ := remoteRecords(t, e, "?kind=pointer"); len(recs) != 3 {
		t.Fatalf("%d pointers", len(recs))
	}
	recs, _ := remoteRecords(t, e, "?ticket="+key)
	var ids []string
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"dr-7", "team-00000002", "team-00000003"}) {
		t.Fatalf("the ticket's records: %v", ids)
	}
	// An older revision keeps the newer; a newer replaces it.
	older := remoteOf(ext.RemotePointer, "dev-1", "team-00000002", 3, map[string]any{"id": "team-00000002", "crew": "team", "state": "finished", "revision": 3})
	if err := h.PutRemote(ctx, older); err != nil {
		t.Fatal(err)
	}
	if recs, _ := remoteRecords(t, e, "?kind=pointer"); !strings.Contains(string(recordByID(recs, "team-00000002").Data), "running") {
		t.Fatal("an older revision replaced the newer")
	}
	// A record its check refuses, and a Host that is not the sync store's.
	bad := remoteOf(ext.RemoteDevice, "dev-1", "dev-3", 1, map[string]any{"id": "dev-3", "name": "h", "kind": "host"})
	if err := h.PutRemote(ctx, bad); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a host as a device: %v", err)
	}
	other := &extHost{s: e.srv, name: "another", ctx: ctx}
	for name, err := range map[string]error{
		"PutRemote":    other.PutRemote(ctx, puts[5]),
		"DeleteRemote": other.DeleteRemote(ctx, ext.RemotePins, "dev-1", "pins"),
		"ClearRemote":  other.ClearRemote(ctx),
	} {
		if !errors.Is(err, ext.ErrUnsupported) {
			t.Errorf("%s on another provider's Host: %v", name, err)
		}
	}
	for _, q := range []string{"?kind=notes", "?ticket=not-a-key", "?after=x", "?page=2", "?kind=pins&kind=pointer"} {
		e.expect(t, http.MethodGet, "/api/remote"+q, nil, http.StatusBadRequest, "invalid_request")
	}
	// Pages of 200.
	for i := range 205 {
		id := fmt.Sprintf("team-%08d", 100+i)
		if err := h.PutRemote(ctx, remoteOf(ext.RemotePointer, "dev-2", id, 1, map[string]any{"id": id, "crew": "team", "state": "running", "revision": 1})); err != nil {
			t.Fatal(err)
		}
	}
	first, next := remoteRecords(t, e, "?kind=pointer")
	if len(first) != remotePage || next == "" {
		t.Fatalf("the first page: %d, next %q", len(first), next)
	}
	rest, more := remoteRecords(t, e, "?kind=pointer&after="+url.QueryEscape(next))
	if len(rest) != 208-remotePage || more != "" {
		t.Fatalf("the next page: %d, next %q", len(rest), more)
	}
	// Deleted one, then cleared.
	if err := h.DeleteRemote(ctx, ext.RemotePins, "dev-1", "pins"); err != nil {
		t.Fatal(err)
	}
	if recs, _ := remoteRecords(t, e, "?kind=pins"); len(recs) != 0 {
		t.Fatal("the deleted pins are still read")
	}
	if err := h.ClearRemote(ctx); err != nil {
		t.Fatal(err)
	}
	if recs, _ := remoteRecords(t, e, ""); len(recs) != 0 {
		t.Fatalf("%d records after the clear", len(recs))
	}
}

func recordByID(recs []remoteRecord, id string) remoteRecord {
	for _, r := range recs {
		if r.ID == id {
			return r
		}
	}
	return remoteRecord{}
}

// Past the bound of files the oldest records of things over go first (a
// decision final, a run closed); with none of those, a new record is
// refused.
func TestRemoteFilesBounded(t *testing.T) {
	ctx := context.Background()
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	e.srv.remoteMax = 3
	h := &extHost{s: e.srv, name: f.Name(), ctx: ctx}
	put := func(id, state string, at time.Time) error {
		r := remoteOf(ext.RemoteDecision, "dev-1", id, 1, remoteDecisionOf(id, "", state))
		r.At = at
		return h.PutRemote(ctx, r)
	}
	for i, d := range []struct{ id, state string }{{"dr-1", "applied"}, {"dr-2", "open"}, {"dr-3", "cancelled"}} {
		if err := put(d.id, d.state, remoteAt.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := put("dr-4", "open", remoteAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	recs, _ := remoteRecords(t, e, "")
	var ids []string
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"dr-2", "dr-3", "dr-4"}) {
		t.Fatalf("after the bound: %v", ids)
	}
	if err := put("dr-5", "open", remoteAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := put("dr-6", "open", remoteAt.Add(3*time.Hour)); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a record past the bound with nothing final to drop: %v", err)
	}
}

// remote/ is left out of an export as derived, never carried.
func TestRemoteLeftOutOfTheExport(t *testing.T) {
	ctx := context.Background()
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	h := &extHost{s: e.srv, name: f.Name(), ctx: ctx}
	if err := h.PutRemote(ctx, remoteOf(ext.RemotePins, "dev-1", "pins", 1, map[string]any{"pins": []any{}})); err != nil {
		t.Fatal(err)
	}
	resp, b := getExport(t, e, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the export: %d", resp.StatusCode)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	left := false
	for _, file := range zr.File {
		if strings.HasPrefix(file.Name, "data/remote/") || strings.HasPrefix(file.Name, "remote/") {
			t.Fatalf("the archive holds %s", file.Name)
		}
		if file.Name == export.ManifestName {
			rc, _ := file.Open()
			var m export.Manifest
			_ = json.NewDecoder(rc).Decode(&m)
			rc.Close()
			left = slices.ContainsFunc(m.Left, func(l export.Left) bool { return l.Path == "remote/" && l.Why == export.WhyDerived })
		}
	}
	if !left {
		rc, _ := zr.Open(export.ManifestName)
		var m export.Manifest
		_ = json.NewDecoder(rc).Decode(&m)
		t.Fatalf("the manifest does not name remote/ left out: %+v", m.Left)
	}
}

// No route takes a remote record for this machine's own: a remote
// decision is neither read, answered, routed nor cancelled here; a remote
// run's pointer is no run of this server's; remote pins are not this
// server's pins; a remote device's or host summary's id names nothing. The
// record is there unchanged after every try.
func TestRemoteRecordsAreNeverActedOn(t *testing.T) {
	ctx := context.Background()
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	h := &extHost{s: e.srv, name: f.Name(), ctx: ctx}
	puts := []ext.Remote{
		remoteOf(ext.RemoteDecision, "dev-1", "dr-77", 1, remoteDecisionOf("dr-77", "", "open")),
		remoteOf(ext.RemotePointer, "dev-1", "team-00000077", 1, map[string]any{"id": "team-00000077", "crew": "team", "state": "running", "revision": 1}),
		remoteOf(ext.RemotePins, "dev-1", "pins", 1, map[string]any{"v": 1, "pins": []any{map[string]any{"session": "s-remote", "name": "remote build"}}}),
		remoteOf(ext.RemoteDevice, "dev-1", "dev-2", 1, map[string]any{"id": "dev-2", "name": "laptop", "kind": "server"}),
		remoteOf(ext.RemoteHosts, "dev-1", "summary", 1, map[string]any{"hosts": []any{}}),
	}
	for _, r := range puts {
		if err := h.PutRemote(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := remoteRecords(t, e, "")
	t.Run("decision", func(t *testing.T) {
		e.expect(t, http.MethodGet, "/api/decisions/dr-77", nil, http.StatusNotFound, "")
		e.expect(t, http.MethodPost, "/api/decisions/dr-77/respond", map[string]any{"responseId": "r1", "choice": 0, "expectedRev": 1}, http.StatusNotFound, "")
		e.expect(t, http.MethodPost, "/api/decisions/dr-77/route", map[string]any{"opId": "o1", "to": map[string]any{"kind": "operator"}, "expectedRev": 1}, http.StatusNotFound, "")
		e.expect(t, http.MethodPost, "/api/decisions/dr-77/cancel", map[string]any{"commandId": "c1", "why": "not ours", "expectedRev": 1}, http.StatusNotFound, "")
		if list := e.expect(t, http.MethodGet, "/api/decisions", nil, http.StatusOK, ""); strings.Contains(fmt.Sprint(list), "dr-77") {
			t.Fatal("the decisions' list holds a remote decision")
		}
	})
	t.Run("pointer", func(t *testing.T) {
		e.expect(t, http.MethodGet, "/api/runs/team-00000077", nil, http.StatusNotFound, "")
		e.expect(t, http.MethodPost, "/api/runs/team-00000077/stop", nil, http.StatusNotFound, "")
	})
	t.Run("pins", func(t *testing.T) {
		if pins := e.expect(t, http.MethodGet, "/api/ui/pins", nil, http.StatusOK, ""); strings.Contains(fmt.Sprint(pins), "s-remote") {
			t.Fatalf("this server's pins hold another machine's: %v", pins)
		}
	})
	t.Run("device and hosts", func(t *testing.T) {
		for _, path := range []string{"/api/remote/dev-2", "/api/hosts/summary"} {
			if code, _, _ := e.call(t, http.MethodPost, path, map[string]any{}); code == http.StatusOK || code == http.StatusCreated {
				t.Errorf("POST %s: %d", path, code)
			}
		}
	})
	after, _ := remoteRecords(t, e, "")
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	if !bytes.Equal(b1, b2) {
		t.Fatal("a remote record changed")
	}
	for _, r := range puts {
		if _, err := os.Stat(filepath.Join(e.srv.store.Dir(), "remote", remoteDir(r.Device), remoteFile(r.Kind, r.ID))); err != nil {
			t.Fatalf("%s %s: %v", r.Kind, r.ID, err)
		}
	}
}

// statusSync is a sync store that reports its state through the report
// func (nil: a fixed current report).
type statusSync struct {
	*fakeSync
	report atomic.Pointer[func() ext.SyncReport]
}

func (s *statusSync) SyncStatus() ext.SyncReport {
	if f := s.report.Load(); f != nil {
		return (*f)()
	}
	return ext.SyncReport{State: ext.SyncCurrent}
}

func (s *statusSync) set(f func() ext.SyncReport) { s.report.Store(&f) }

// GET /api/sync answers {} with no sync store and the report's fields only
// when the store gives a report its check takes in time; the server's own
// words of what it keeps beside them. A query is refused.
func TestSyncStatusRoute(t *testing.T) {
	plain := newTestEnv(t, nil)
	// A text kept for a hosted agent is no reason to answer without a store.
	plain.srv.hostedKept.Add(1)
	if got := plain.expect(t, http.MethodGet, "/api/sync", nil, http.StatusOK, ""); len(got) != 0 {
		t.Fatalf("with no sync store: %v", got)
	}
	f := newFakeSync()
	if got := syncEnv(t, syncDataDir(t), f).expect(t, http.MethodGet, "/api/sync", nil, http.StatusOK, ""); len(got) != 0 {
		t.Fatalf("a sync store without a report: %v", got)
	}
	st := &statusSync{fakeSync: newFakeSync()}
	e := syncEnv(t, syncDataDir(t), st)
	e.expect(t, http.MethodGet, "/api/sync?x=1", nil, http.StatusBadRequest, "invalid_request")
	st.set(func() ext.SyncReport {
		return ext.SyncReport{State: ext.SyncBehind, Waiting: 37, Withheld: []ext.SyncWithheld{{Ref: "CND-12/comment", Words: "quotes captured output"}}}
	})
	got := e.expect(t, http.MethodGet, "/api/sync", nil, http.StatusOK, "")
	if got["state"] != ext.SyncBehind || got["waiting"] != float64(37) || len(got["withheld"].([]any)) != 1 {
		t.Fatalf("the report: %v", got)
	}
	st.set(func() ext.SyncReport { return ext.SyncReport{State: "fine"} })
	if got := e.expect(t, http.MethodGet, "/api/sync", nil, http.StatusOK, ""); got["state"] != nil {
		t.Fatalf("a report its check refuses: %v", got)
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	st.set(func() ext.SyncReport { <-release; return ext.SyncReport{State: ext.SyncCurrent} })
	if got := e.expect(t, http.MethodGet, "/api/sync", nil, http.StatusOK, ""); got["state"] != nil {
		t.Fatalf("a report past its wait: %v", got)
	}
	// The server's own words: a hosted agent's text kept.
	e.srv.hostedKept.Add(2)
	st.set(func() ext.SyncReport { return ext.SyncReport{State: ext.SyncCurrent} })
	got = e.expect(t, http.MethodGet, "/api/sync", nil, http.StatusOK, "")
	if here, _ := got["here"].(map[string]any); here == nil || here["hosted"] != float64(2) {
		t.Fatalf("the server's own words: %v", got)
	}
	// At its bounds the answer is within its own.
	full := ext.SyncReport{State: ext.SyncUnreachable, Since: remoteAt, Waiting: 1 << 30, AnswerURL: "https://example.com/" + strings.Repeat("a", ext.MaxAnswerURL-20),
		Device: strings.Repeat("d", 64), DeviceName: strings.Repeat("é", 32), Workspace: strings.Repeat("w", 64), Person: strings.Repeat("p", 64)}
	for i := range ext.MaxSyncWithheld {
		full.Withheld = append(full.Withheld, ext.SyncWithheld{Ref: fmt.Sprintf("%064d", i), Words: strings.Repeat("<", ext.MaxSyncWords)})
	}
	if err := full.Valid(); err != nil {
		t.Fatal(err)
	}
	st.set(func() ext.SyncReport { return full })
	_, body, _ := e.call(t, http.MethodGet, "/api/sync", nil)
	if len(body) > maxSyncStatusReply || !strings.Contains(string(body), `"unreachable"`) {
		t.Fatalf("the answer at its bounds: %d bytes", len(body))
	}
}

// The report names this server's own device, workspace and person, so a
// page tells its own records from other devices'.
func TestSyncStatusNamesItsOwnDevice(t *testing.T) {
	st := &statusSync{fakeSync: newFakeSync()}
	e := syncEnv(t, syncDataDir(t), st)
	st.set(func() ext.SyncReport {
		return ext.SyncReport{State: ext.SyncCurrent, Device: "dev-1", DeviceName: "studio", Workspace: "ws-1", Person: "p-1"}
	})
	got := e.expect(t, http.MethodGet, "/api/sync", nil, http.StatusOK, "")
	if got["device"] != "dev-1" || got["deviceName"] != "studio" || got["workspace"] != "ws-1" || got["person"] != "p-1" {
		t.Fatalf("%v", got)
	}
}
