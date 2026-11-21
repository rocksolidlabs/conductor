package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A run's pointer names the ticket whose launch made it only while that
// ticket's project is shared here (Owner decisions, 23): a run of an
// unshared project's ticket names none; once the project is shared, the
// pointer sent again names it; a run no ticket launched never names one.
func TestPointerNamesItsLaunchTicket(t *testing.T) {
	ctx := context.Background()
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	e.newDispatchProject(t, "MINE", nil)
	ticket := e.newDispatchTicket(t, "MINE", "launched from here", nil)["id"].(string)
	ld := e.srv.ledger
	for _, r := range []struct{ id, ticket string }{{"team-00000001", ticket}, {"team-00000002", ""}} {
		if _, err := ld.Transition(ctx, r.id, func(tx *ledger.Tx) error {
			return tx.CreateRun(ledger.RunRow{ID: r.id, CrewID: "team", Crew: `{"id":"team","name":"Team"}`, State: ledger.RunFinished, CreatedAt: tx.Now(), Ticket: r.ticket})
		}); err != nil {
			t.Fatal(err)
		}
	}
	pointer := func(c []syncCall, run string) (syncPointer, bool) {
		cs := callsOf(c, syncKey{syncLedger, run})
		for i := len(cs) - 1; i >= 0; i-- {
			if cs[i].op == "saved" && cs[i].err == nil {
				var p syncPointer
				return p, json.Unmarshal(cs[i].d.Data, &p) == nil
			}
		}
		return syncPointer{}, false
	}
	calls := f.until(t, "both pointers", func(c []syncCall) bool {
		_, a := pointer(c, "team-00000001")
		_, b := pointer(c, "team-00000002")
		return a && b
	})
	if p, _ := pointer(calls, "team-00000001"); p.Ticket != "" {
		t.Fatalf("the pointer of an unshared project's run names %q", p.Ticket)
	}
	if err := e.srv.dispatch.ShareForTest(ctx, "MINE", "w1"); err != nil {
		t.Fatal(err)
	}
	h := &extHost{s: e.srv, name: f.Name(), ctx: ctx}
	for _, run := range []string{"team-00000001", "team-00000002"} {
		if err := h.SyncResend(ctx, syncLedger, run); err != nil {
			t.Fatal(err)
		}
	}
	calls = f.until(t, "the pointer sent again with its ticket", func(c []syncCall) bool {
		p, ok := pointer(c, "team-00000001")
		return ok && p.Ticket == ticket
	})
	if p, _ := pointer(calls, "team-00000002"); p.Ticket != "" {
		t.Fatalf("a run no ticket launched names %q", p.Ticket)
	}
}
