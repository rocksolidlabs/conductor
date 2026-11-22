package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The workspace's other machines (docs/plans/hosts.md, PR-5): a fake
// registered sync store reports its state, and the records other machines
// hold are put through its doors, as a provider puts them.

// othersEnv is a server whose sync store reports current as the device
// dev-self, and its Host's doors.
func othersEnv(t *testing.T) (*testEnv, *statusSync, *extHost) {
	t.Helper()
	st := &statusSync{fakeSync: newFakeSync()}
	st.set(func() ext.SyncReport {
		return ext.SyncReport{State: ext.SyncCurrent, Since: time.Now().UTC(), Device: "dev-self", DeviceName: "this"}
	})
	e := syncEnv(t, syncDataDir(t), st)
	return e, st, &extHost{s: e.srv, name: st.Name(), ctx: context.Background()}
}

// put keeps each record through the sync store's door.
func put(t *testing.T, h *extHost, recs ...ext.Remote) {
	t.Helper()
	for _, r := range recs {
		if err := h.PutRemote(context.Background(), r); err != nil {
			t.Fatalf("%s %s of %s: %v", r.Kind, r.ID, r.Device, err)
		}
	}
}

// deviceOf is the device record of id, named name, reported by device.
func deviceOf(by, id, name string, lastReport time.Time) ext.Remote {
	r := remoteOf(ext.RemoteDevice, by, id, 1, map[string]any{"id": id, "name": name, "kind": "server", "platform": "darwin/arm64",
		"version": "v0.7.0", "lastReport": lastReport})
	r.DeviceName = name
	return r
}

// pointerOf is a run pointer device holds.
func pointerOf(device, name, id string, closed bool) ext.Remote {
	body := map[string]any{"id": id, "crew": "team", "state": "running", "revision": 1}
	if closed {
		body["state"], body["closedAt"] = "finished", remoteAt
	}
	r := remoteOf(ext.RemotePointer, device, id, 1, body)
	r.DeviceName = name
	return r
}

// decisionOn is an open decision device holds, opened in openedIn and held
// by run now.
func decisionOn(device, name, id, run, openedIn string) ext.Remote {
	doc := remoteDecisionOf(id, "", "open")
	doc.Run, doc.OpenedIn = run, openedIn
	r := remoteOf(ext.RemoteDecision, device, id, 1, doc)
	r.DeviceName = name
	return r
}

// One device with its summary, two pointers (one closed) and two open
// decisions on the open run, one inherited from the run it continued: one
// machine row, its summary, one run reported by its name and time,
// waiting 2 on it, and 2 open on the machine.
func TestHostsOtherMachines(t *testing.T) {
	e, _, h := othersEnv(t)
	last := time.Date(2026, 11, 20, 14, 5, 0, 0, time.UTC)
	sum := exttest.SampleRemoteHosts("dev-2", "studio", nil)
	put(t, h, deviceOf("dev-2", "dev-2", "studio", last), sum,
		pointerOf("dev-2", "studio", "team-00000002", false), pointerOf("dev-2", "studio", "team-00000001", true),
		decisionOn("dev-2", "studio", "dr-1", "team-00000002", "team-00000002"), decisionOn("dev-2", "studio", "dr-2", "team-00000002", "team-00000001"))
	r := e.hostsOf(t)
	if r.Other == nil || r.Other.State != ext.SyncCurrent || len(r.Other.Machines) != 1 {
		t.Fatalf("other: %+v", r.Other)
	}
	m := r.Other.Machines[0]
	if m.Device != "dev-2" || m.Name != "studio" || m.Platform != "darwin/arm64" || !m.LastReport.Equal(last) || !m.Own || m.Summary == nil ||
		m.Decisions != (HostsDecisions{Open: 2}) || len(m.Runs) != 1 {
		t.Fatalf("the machine: %+v", m)
	}
	if run := m.Runs[0]; run.ID != "team-00000002" || run.Waiting != 2 || run.ReportedBy != "studio" || !run.ReportedAt.Equal(remoteAt) {
		t.Fatalf("its run: %+v", run)
	}
	if s := m.Summary; s.Platform != "linux/amd64" || len(s.Hosts) != 2 || s.Hosts[0].Name != "studio" || s.Runs != (HostsRemoteRuns{Held: 2, Waiting: 1}) {
		t.Fatalf("its summary: %+v", s)
	}
}

// One run id from two devices shows under each, each as reported, with no
// holder.
func TestHostsOtherSameRunTwoDevices(t *testing.T) {
	e, _, h := othersEnv(t)
	put(t, h, deviceOf("dev-2", "dev-2", "studio", remoteAt), deviceOf("dev-3", "dev-3", "desk", remoteAt.Add(time.Minute)),
		pointerOf("dev-2", "studio", "team-00000005", false), pointerOf("dev-3", "desk", "team-00000005", false))
	r := e.hostsOf(t)
	if r.Other == nil || len(r.Other.Machines) != 2 {
		t.Fatalf("other: %+v", r.Other)
	}
	for _, m := range r.Other.Machines {
		if len(m.Runs) != 1 || m.Runs[0].ID != "team-00000005" || m.Runs[0].ReportedBy != m.Name || m.Own {
			t.Fatalf("%s: %+v", m.Name, m)
		}
	}
}

// Every state but current, and behind past 10 minutes, empties machines
// in the same reply; current again shows them, with no restart.
func TestHostsOtherOnlyWhenCurrent(t *testing.T) {
	e, st, h := othersEnv(t)
	put(t, h, deviceOf("dev-2", "dev-2", "studio", remoteAt))
	if r := e.hostsOf(t); r.Other == nil || len(r.Other.Machines) != 1 {
		t.Fatalf("current: %+v", r.Other)
	}
	since := time.Now().UTC().Add(-time.Minute)
	for _, rep := range []ext.SyncReport{{State: ext.SyncUnreachable, Since: since}, {State: ext.SyncLapsed, Since: since},
		{State: ext.SyncBehind, Since: time.Now().UTC().Add(-othersBehindShown - time.Minute)}} {
		st.set(func() ext.SyncReport { return rep })
		r := e.hostsOf(t)
		if r.Other == nil || r.Other.State != rep.State || len(r.Other.Machines) != 0 || !r.Other.Since.Equal(rep.Since) {
			t.Fatalf("%s: %+v", rep.State, r.Other)
		}
	}
	st.set(func() ext.SyncReport {
		return ext.SyncReport{State: ext.SyncBehind, Since: time.Now().UTC().Add(-time.Minute)}
	})
	if r := e.hostsOf(t); r.Other == nil || len(r.Other.Machines) != 1 {
		t.Fatalf("behind a minute: %+v", r.Other)
	}
	st.set(func() ext.SyncReport { return ext.SyncReport{State: ext.SyncCurrent} })
	if r := e.hostsOf(t); r.Other == nil || len(r.Other.Machines) != 1 {
		t.Fatalf("current again: %+v", r.Other)
	}
}

// noOther fails t unless the reply's bytes have no other field.
func noOther(t *testing.T, e *testEnv, what string) {
	t.Helper()
	status, body := e.get(t, "/api/hosts")
	if status != http.StatusOK || strings.Contains(string(body), `"other"`) {
		t.Fatalf("%s: %d %s", what, status, body)
	}
}

// No sync store: no other field, whatever remote/ holds.
func TestHostsNoOtherWithoutSync(t *testing.T) {
	e := newTestEnv(t, nil)
	dir := filepath.Join(e.srv.store.Dir(), "remote", remoteDir("dev-2"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(remoteRecord{Kind: ext.RemoteDevice, Device: "dev-2", DeviceName: "studio", ID: "dev-2", Rev: 1, At: remoteAt, Data: remoteJSON(t, map[string]any{"id": "dev-2", "name": "studio", "kind": "server"})})
	if err := os.WriteFile(filepath.Join(dir, remoteFile(ext.RemoteDevice, "dev-2")), b, 0o600); err != nil {
		t.Fatal(err)
	}
	noOther(t, e, "no sync store")
}

// A store still starting, never current, or signed out: the reply of a
// plain build, no other field.
func TestHostsNoOtherUntilCurrent(t *testing.T) {
	st := &statusSync{fakeSync: newFakeSync()}
	st.set(func() ext.SyncReport { return ext.SyncReport{State: ext.SyncCurrent} })
	starting := syncEnv(t, syncDataDir(t), statusStarter{st})
	noOther(t, starting, "a store still starting")
	e, rep, _ := othersEnv(t)
	rep.set(func() ext.SyncReport { return ext.SyncReport{State: ext.SyncUnreachable, Since: remoteAt} })
	noOther(t, e, "never current")
	rep.set(func() ext.SyncReport { return ext.SyncReport{State: ext.SyncCurrent} })
	if r := e.hostsOf(t); r.Other == nil {
		t.Fatal("current: no other")
	}
	rep.set(func() ext.SyncReport { return ext.SyncReport{State: ext.SyncSignedOut} })
	noOther(t, e, "signed out")
}

// statusStarter is a reporting store that needs Start, never started here.
type statusStarter struct{ *statusSync }

func (statusStarter) Start(context.Context, ext.Host) error { return nil }

// The rule, on its own: present and shown for each state.
func TestOtherMachinesShown(t *testing.T) {
	e, st, _ := othersEnv(t)
	now := time.Now().UTC()
	for _, c := range []struct {
		rep            ext.SyncReport
		present, shown bool
	}{
		{ext.SyncReport{State: ext.SyncCurrent}, true, true},
		{ext.SyncReport{State: ext.SyncBehind, Since: now.Add(-9 * time.Minute)}, true, true},
		{ext.SyncReport{State: ext.SyncBehind, Since: now.Add(-11 * time.Minute)}, true, false},
		{ext.SyncReport{State: ext.SyncUnreachable, Since: now}, true, false},
		{ext.SyncReport{State: ext.SyncLapsed, Since: now}, true, false},
		{ext.SyncReport{State: ext.SyncSignedOut}, false, false},
	} {
		st.set(func() ext.SyncReport { return c.rep })
		if v := e.srv.otherMachinesShown(context.Background()); v.present != c.present || v.shown != c.shown || c.present && v.state != c.rep.State {
			t.Errorf("%s since %v: %+v", c.rep.State, c.rep.Since, v)
		}
	}
}

// This server's own device, the records of that device and a pointer of
// a run this ledger holds are dropped.
func TestHostsOtherDropsThis(t *testing.T) {
	e, _, h := othersEnv(t)
	newLedgerRun(t, e.srv.ledger, "team-00000007", ledger.RunRunning)
	put(t, h, deviceOf("dev-2", "dev-self", "this", remoteAt), deviceOf("dev-2", "dev-2", "studio", remoteAt),
		pointerOf("dev-self", "this", "team-00000008", false), pointerOf("dev-2", "studio", "team-00000007", false),
		pointerOf("dev-2", "studio", "team-00000009", false))
	r := e.hostsOf(t)
	if r.Other == nil || len(r.Other.Machines) != 1 || r.Other.Machines[0].Device != "dev-2" {
		t.Fatalf("other: %+v", r.Other)
	}
	if runs := r.Other.Machines[0].Runs; len(runs) != 1 || runs[0].ID != "team-00000009" {
		t.Fatalf("its runs: %+v", runs)
	}
}

// Many machines with long crews: other within its budget, the counts
// exact.
func TestHostsOtherBounded(t *testing.T) {
	e, _, h := othersEnv(t)
	const machines, runs = 70, 5
	crewName := strings.Repeat("<", 120)
	for i := range machines {
		dev := fmt.Sprintf("dev-%03d", i)
		put(t, h, deviceOf(dev, dev, strings.Repeat("é", 32), remoteAt.Add(time.Duration(i)*time.Minute)))
		for j := range runs {
			p := pointerOf(dev, "studio", fmt.Sprintf("team-%08d", i*10+j), false)
			var body map[string]any
			_ = json.Unmarshal(p.Data, &body)
			body["crew"] = crewName
			p.Data, _ = json.Marshal(body)
			put(t, h, p)
		}
	}
	r := e.hostsOf(t)
	if r.Other == nil {
		t.Fatal("no other")
	}
	if n := hostsEncodedLen(r.Other.Machines); n > hostsOtherBudget {
		t.Fatalf("other's machines take %d bytes", n)
	}
	shownRuns := 0
	for _, m := range r.Other.Machines {
		shownRuns += len(m.Runs)
	}
	if len(r.Other.Machines)+r.Other.More.Machines != machines || shownRuns+r.Other.More.RemoteRuns != machines*runs || r.Other.More.Machines == 0 && r.Other.More.RemoteRuns == 0 {
		t.Fatalf("%d machines and %d more, %d runs and %d more", len(r.Other.Machines), r.Other.More.Machines, shownRuns, r.Other.More.RemoteRuns)
	}
	_, body := e.get(t, "/api/hosts")
	if len(body) > hostsReplyMax {
		t.Fatalf("the reply is %d bytes", len(body))
	}
}

// A kept hosts record whose body is no summary (written past the door) is
// never served, by GET /api/remote or the host overview.
func TestRemoteHostsJudgedOnRead(t *testing.T) {
	e, _, h := othersEnv(t)
	put(t, h, deviceOf("dev-2", "dev-2", "studio", remoteAt))
	dir := filepath.Join(e.srv.store.Dir(), "remote", remoteDir("dev-2"))
	b, _ := json.Marshal(remoteRecord{Kind: ext.RemoteHosts, Device: "dev-2", DeviceName: "studio", ID: "summary", Rev: 1, At: remoteAt,
		Data: remoteJSON(t, map[string]any{"platform": "linux/amd64", "version": "v1", "sessions": map[string]any{}, "runs": map[string]any{}, "hosts": []any{}, "cwd": "/home/ada"})})
	if err := os.WriteFile(filepath.Join(dir, remoteFile(ext.RemoteHosts, "summary")), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if recs, _ := remoteRecords(t, e, "?kind=hosts"); len(recs) != 0 {
		t.Fatalf("a body that is no summary was served: %+v", recs)
	}
	if r := e.hostsOf(t); r.Other == nil || len(r.Other.Machines) != 1 || r.Other.Machines[0].Own || r.Other.Machines[0].Summary != nil {
		t.Fatalf("the machine: %+v", r.Other)
	}
}

// An interrupted run another machine continued is no open run there: its
// pointer, interrupted and not closed but continued, beside its running
// continuation, shows the continuation alone.
func TestHostsOtherContinuedRunNotShown(t *testing.T) {
	e, _, h := othersEnv(t)
	interrupted := remoteOf(ext.RemotePointer, "dev-2", "team-00000011", 2, map[string]any{"id": "team-00000011", "crew": "team",
		"state": "interrupted", "revision": 2, "continuedBy": "team-00000012"})
	interrupted.DeviceName = "studio"
	continuation := remoteOf(ext.RemotePointer, "dev-2", "team-00000012", 1, map[string]any{"id": "team-00000012", "crew": "team",
		"state": "running", "revision": 1, "continuedFrom": "team-00000011"})
	continuation.DeviceName = "studio"
	put(t, h, deviceOf("dev-2", "dev-2", "studio", remoteAt), interrupted, continuation)
	r := e.hostsOf(t)
	if r.Other == nil || len(r.Other.Machines) != 1 || len(r.Other.Machines[0].Runs) != 1 || r.Other.Machines[0].Runs[0].ID != "team-00000012" {
		t.Fatalf("other: %+v", r.Other)
	}
}
