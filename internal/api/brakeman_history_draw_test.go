package api

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/history"
)

// plantRows puts entries into the fixture's index as a source of their
// own, one no indexer pass reads.
func plantRows(t *testing.T, f *obsFixture, source string, entries []history.Entry) {
	t.Helper()
	for i := 0; i < len(entries); i += 1000 {
		src := history.Source{Key: fmt.Sprintf("zz:%s/%d", source, i), Mark: "m"}
		if err := f.srv.history.index().Replace(t.Context(), src, entries[i:min(i+1000, len(entries))], nil); err != nil {
			t.Fatal(err)
		}
	}
}

// drawnBy reads path as the turn and answers its reply and the rows its
// read drew from the index.
func drawnBy(t *testing.T, f *obsFixture, path string) (map[string]any, int) {
	t.Helper()
	n := 0
	ix := f.srv.history.index()
	ix.SetDrawSeam(func(int64) { n++ })
	defer ix.SetDrawSeam(nil)
	return f.expectRead(t, path, http.StatusOK, ""), n
}

// A turn's read reads only rows its grant covers, in its cost too: a turn
// scoped to CND reads its comment by its words, a prefix of them, a kind
// and the empty line, and with ten thousand newer rows of OTH holding the
// same words, outside its grant, each read answers the same and draws the
// same rows from the index.
func TestATurnsReadDrawsOnlyRowsItsGrantCovers(t *testing.T) {
	f := newObsFixture(t)
	kept := "comment:" + f.ticket + "/" + doneOf(t)(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), f.ticket, "zebra kept")).ID
	ix := histSettle(t, f.testEnv)
	until(t, "the comment indexed", func() bool {
		_, err := ix.Entry(t.Context(), kept)
		return err == nil
	})
	f.turn(t, func(c map[string]any) {
		c["scope"] = map[string]any{"projects": []string{"CND"}}
		c["budget"].(map[string]any)["readsPerTurn"] = brakeman.MaxReadsPerTurn
	})
	lines := []string{"zebra", "zeb", "zebra kind:comment", "kind:comment", ""}
	type answer struct {
		refs  []string
		drawn int
	}
	read := func() map[string]answer {
		out := map[string]answer{}
		for _, l := range lines {
			got, n := drawnBy(t, f, searchPath(l))
			out[l] = answer{historyRefs(got), n}
		}
		return out
	}
	before := read()
	if !slices.Contains(before["zebra"].refs, kept) {
		t.Fatalf("the turn's comment: %v", before["zebra"].refs)
	}
	var hidden []history.Entry
	for i := range 10_000 {
		key := fmt.Sprintf("comment:%s/h%05d", f.otherTicket, i)
		hidden = append(hidden, history.Entry{Key: key, Kind: "comment", Ref: key, Anchor: "ticket:" + f.otherTicket, Project: "OTH", Ticket: f.otherTicket,
			At: time.Now().Add(time.Hour), Actor: "admin", Title: "zebra hidden", Body: "zebra hidden"})
	}
	plantRows(t, f, "hidden", hidden)
	if total, _ := hsSearch(t, f.testEnv, "zebra")["total"].(float64); total < 10_000 {
		t.Fatalf("the rows outside the grant are not in the index: %v", total)
	}
	after := read()
	for _, l := range lines {
		if !slices.Equal(after[l].refs, before[l].refs) || after[l].drawn != before[l].drawn {
			t.Errorf("%q: %v drawing %d rows, before %v drawing %d", l, after[l].refs, after[l].drawn, before[l].refs, before[l].drawn)
		}
	}
}

// A reply examines at most maxHistoryTurnExamine rows of its grant: a
// turn's line that matches a comment older than more rows of the grant
// than that is answered short with cut "examined" and no entries, and the
// same line narrowed to a time window finds it.
func TestATurnsReadSaysWhenItExaminedItsBound(t *testing.T) {
	f := newObsFixture(t)
	old := "comment:" + f.ticket + "/" + doneOf(t)(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), f.ticket, "pelican under them")).ID
	ix := histSettle(t, f.testEnv)
	until(t, "the comment indexed", func() bool {
		_, err := ix.Entry(t.Context(), old)
		return err == nil
	})
	e, err := ix.Entry(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}
	var newer []history.Entry
	for i := range maxHistoryTurnExamine + 1 {
		key := fmt.Sprintf("comment:%s/n%05d", f.ticket, i)
		newer = append(newer, history.Entry{Key: key, Kind: "comment", Ref: key, Anchor: "ticket:" + f.ticket, Project: "CND", Ticket: f.ticket,
			At: e.At.Add(time.Duration(i+1) * time.Second), Actor: "admin", Title: "heron", Body: "heron"})
	}
	plantRows(t, f, "newer", newer)
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"projects": []string{"CND"}} })
	out, n := drawnBy(t, f, searchPath("pelican"))
	if len(historyRefs(out)) != 0 || out["cut"] != history.CutExamined || out["next"] != nil || n > maxHistoryTurnExamine {
		t.Fatalf("a read past its bound: %v, %d rows drawn", out, n)
	}
	window := fmt.Sprintf("pelican until:%s", e.At.Add(time.Second).UTC().Format(time.RFC3339))
	out = f.expectRead(t, searchPath(window), http.StatusOK, "")
	if refs := historyRefs(out); !slices.Equal(refs, []string{old}) || out["cut"] != nil {
		t.Fatalf("the read narrowed to a window: %v", out)
	}
}

// A ticket's row that names a run is placed by both: a verified delivery
// of the ticket by a run outside a scope of repositories is outside the
// read, and two hundred of them, newer and holding the line's words, change
// nothing of what a turn so scoped is served (the ticket's comment); a
// turn scoped to the delivering run is served them.
func TestADeliveryIsPlacedByItsRunToo(t *testing.T) {
	f := newObsFixture(t)
	kept := "comment:" + f.ticket + "/" + doneOf(t)(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), f.ticket, "osprey kept")).ID
	ix := histSettle(t, f.testEnv)
	until(t, "the comment indexed", func() bool {
		_, err := ix.Entry(t.Context(), kept)
		return err == nil
	})
	inside := t.TempDir()
	gitInit(t, inside, true)
	f.turn(t, func(c map[string]any) {
		c["scope"] = map[string]any{"repos": []string{inside}}
		c["budget"].(map[string]any)["readsPerTurn"] = brakeman.MaxReadsPerTurn
	})
	before := historyRefs(f.expectRead(t, searchPath("osprey"), http.StatusOK, ""))
	if !slices.Equal(before, []string{kept}) {
		t.Fatalf("the ticket's comment under a scope of repositories: %v", before)
	}
	var deliveries []history.Entry
	for i := range 200 {
		deliveries = append(deliveries, history.Entry{Key: fmt.Sprintf("ticket:%s#delivery/d%03d", f.ticket, i), Kind: "delivery.verified", Ref: "ticket:" + f.ticket,
			Anchor: "ticket:" + f.ticket, Project: "CND", Ticket: f.ticket, Run: f.run, Commit: commitID(1), Outcome: "passed",
			At: time.Now().Add(time.Hour), Actor: "admin", Title: "osprey delivered", Summary: "1 receipts"})
	}
	plantRows(t, f, "deliveries", deliveries)
	if got := historyRefs(f.expectRead(t, searchPath("osprey"), http.StatusOK, "")); !slices.Equal(got, before) {
		t.Fatalf("with the deliveries of a run outside the scope: %v, before %v", got, before)
	}
	f.again(t, func(c map[string]any) { c["scope"] = map[string]any{"runs": []string{f.run}} })
	out := f.expectRead(t, searchPath("osprey kind:delivery.verified"), http.StatusOK, "")
	if refs := historyRefs(out); len(refs) == 0 || refs[0] != "ticket:"+f.ticket {
		t.Fatalf("the deliveries to a turn scoped to their run: %v", refs)
	}
}

// The admin's read of the route is what a turn of every class and no
// scope is served: a kind never served to a turn (a run's tasks) is never
// counted either, so a comment under more of a run's task rows than a
// reply examines is the admin's first entry.
func TestTheAdminsReadIsAnUnscopedTurns(t *testing.T) {
	f := newObsFixture(t)
	kept := "comment:" + f.ticket + "/" + doneOf(t)(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), f.ticket, "egret kept")).ID
	ix := histSettle(t, f.testEnv)
	until(t, "the comment indexed", func() bool {
		_, err := ix.Entry(t.Context(), kept)
		return err == nil
	})
	var tasks []history.Entry
	for i := range maxHistoryTurnExamine + 1 {
		key := fmt.Sprintf("task:%s/t%05d", f.run, i)
		tasks = append(tasks, history.Entry{Key: key, Kind: "task", Ref: key, Anchor: "run:" + f.run, Run: f.run, Crew: "team",
			At: time.Now().Add(time.Hour), Title: "egret task"})
	}
	plantRows(t, f, "tasks", tasks)
	status, _, out := f.callAs(t, adminToken, http.MethodGet, searchPath("egret"), nil)
	if status != http.StatusOK {
		t.Fatalf("the admin's read: %d %v", status, out)
	}
	if refs := historyRefs(out); !slices.Equal(refs, []string{kept}) || out["cut"] != nil {
		t.Fatalf("the admin's read: %v", out)
	}
}

// A filter on Dispatch's fields reads them whether it finds anything or
// not: a turn's read filtered by a ticket that has no entries puts
// Dispatch in its brief's footprint. And a brief's footprint holds the
// class of every ref it cites, read or not.
func TestAFilterAndACitationAreInTheFootprint(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{} })
	if refs := historyRefs(f.expectRead(t, searchPath("kind:comment ticket:"+f.otherTicket+"9"), http.StatusOK, "")); len(refs) != 0 {
		t.Fatalf("the filter matched %v", refs)
	}
	f.submitFact(t, "run:"+f.run)
	if r, _ := f.desk.st.Get(f.req); r.Footprint == nil || !slices.Contains(r.Footprint.Classes, brakeman.ClassDispatch) {
		t.Fatalf("a filter on a ticket that matched nothing: footprint %+v", r.Footprint)
	}
	f.again(t, nil)
	f.submitFact(t, "ticket:"+f.ticket)
	if r, _ := f.desk.st.Get(f.req); r.Footprint == nil || !slices.Contains(r.Footprint.Classes, brakeman.ClassDispatch) {
		t.Fatalf("a brief citing a ticket it did not read: footprint %+v", r.Footprint)
	}
}
