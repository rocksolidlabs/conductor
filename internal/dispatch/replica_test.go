package dispatch

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// join joins the store to the workspace w as the device d under tag,
// with the overflow range 1,000,001 to 1,100,000, failing the test unless
// it did.
func join(t *testing.T, s *Store, tag string) {
	t.Helper()
	if err := s.Join(context.Background(), tag, "w1", "dev-"+tag, 1_000_001, 1_100_000); err != nil {
		t.Fatalf("join %s: %v", tag, err)
	}
}

// Joining gives every local row written before the join the tag as its
// origin and every row written after it too; joining again under a new
// tag keeps the old rows' origin and qualifies only the rows written
// since. A tag, a workspace or a device not of its shape is refused.
func TestJoinGivesTheRowsTheirOrigin(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "Before")
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE origin != ''"); n != 0 {
		t.Fatalf("%d rows with an origin before any join", n)
	}
	join(t, s, "a1")
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE origin != 'a1'"); n != 0 {
		t.Fatalf("%d rows without the tag after the join", n)
	}
	title := "After"
	mustEdit(t, s, "after-a1", key, TicketEdit{Set: TicketSet{Title: &title}})
	join(t, s, "b2")
	title = "Later"
	mustEdit(t, s, "after-b2", key, TicketEdit{Set: TicketSet{Title: &title}})
	if got := texts(t, s, "SELECT DISTINCT origin FROM changes WHERE op_id = 'after-a1'"); !slices.Equal(got, []string{"a1"}) {
		t.Fatalf("a row written under a1: %v", got)
	}
	if got := texts(t, s, "SELECT DISTINCT origin FROM changes WHERE op_id = 'after-b2'"); !slices.Equal(got, []string{"b2"}) {
		t.Fatalf("a row written under b2: %v", got)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE origin = 'b2' AND op_id != 'after-b2'"); n != 0 {
		t.Fatalf("%d earlier rows given the new tag", n)
	}
	if tag, ws, dev, err := s.Replica(ctx); err != nil || tag != "b2" || ws != "w1" || dev != "dev-b2" {
		t.Fatalf("the active replica %q %q %q %v", tag, ws, dev, err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM replica_tags WHERE active = 1"); n != 1 {
		t.Fatalf("%d active tags", n)
	}
	for _, bad := range []struct{ tag, ws, dev string }{{"", "w", "d"}, {"Upper", "w", "d"}, {"toolongtag", "w", "d"}, {"ok", "", "d"}, {"ok", "w", "a b"}} {
		if err := s.Join(ctx, bad.tag, bad.ws, bad.dev, 0, 0); err == nil {
			t.Errorf("a join of %+v taken", bad)
		}
	}
}

// An actor beginning with @ is another replica's qualified actor: no
// local operation may take it.
func TestAQualifiedActorIsRefused(t *testing.T) {
	s := openStore(t, storeDir(t))
	_, err := s.CreateProject(context.Background(), Op{Actor: "@a1/admin", ID: "qualified"}, NewProject{Key: "CND", Name: "Conductor"})
	if !errors.Is(err, ErrBadActor) {
		t.Fatalf("an operation of a qualified actor: %v", err)
	}
}

// Retiring drops every allocation of the active tag: the current and next
// blocks, the tag they were handed to and the overflow range, and a share
// in progress; a shared project's share stays; every project gets a
// provisional window above the floor.
func TestRetireDropsBlocksOverflowAndShares(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	a := newProject(t, s, "CND")
	b := newProject(t, s, "OPS")
	join(t, s, "a1")
	if _, err := s.db.Exec(`UPDATE projects SET number_from = 10, number_to = 20, next_from = 21, next_to = 30, block_tag = 'a1',
		share_state = CASE id WHEN ? THEN 'pending' ELSE 'shared' END, share_reserved_to = 1000, shared = 'w1'`, a); err != nil {
		t.Fatal(err)
	}
	if err := s.Retire(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b} {
		var from, to, nf, nt, of, ot, pf, pt, pn, reserved int64
		var tag, state string
		if err := s.db.QueryRow(`SELECT number_from, number_to, next_from, next_to, block_tag, overflow_from, overflow_to,
			provisional_from, provisional_to, provisional_next, share_state, share_reserved_to FROM projects WHERE id = ?`, id).
			Scan(&from, &to, &nf, &nt, &tag, &of, &ot, &pf, &pt, &pn, &state, &reserved); err != nil {
			t.Fatal(err)
		}
		if from != 0 || to != 0 || nf != 0 || nt != 0 || tag != "" || of != 0 || ot != 0 {
			t.Errorf("%s keeps an allocation: %d-%d next %d-%d tag %q overflow %d-%d", id, from, to, nf, nt, tag, of, ot)
		}
		if pf < ProvisionalFloor || pt != pf+ProvisionalSize-1 || pn != pf {
			t.Errorf("%s's provisional window %d-%d next %d", id, pf, pt, pn)
		}
		switch {
		case id == a && (state != "" || reserved != 0):
			t.Errorf("a pending share kept: %q %d", state, reserved)
		case id == b && state != "shared":
			t.Errorf("a shared project's share dropped: %q", state)
		}
	}
}

// A store restored from an export is another instance (Reissue): its open
// retires every allocation its inherited tag was handed, in the open's
// first transaction, before any write; a store opened again as the same
// instance keeps them.
func TestARestoredStoreRetiresAtOpen(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	newProject(t, s, "CND")
	join(t, s, "a1")
	if _, err := s.db.Exec("UPDATE projects SET number_from = 10, number_to = 20, block_tag = 'a1'"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again := openStore(t, dir)
	if n := count(t, again, "SELECT COUNT(*) FROM projects WHERE block_tag = 'a1' AND number_to = 20"); n != 1 {
		t.Fatalf("the same instance's block dropped: %d", n)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Reissue(ctx, filepath.Join(dir, File)); err != nil {
		t.Fatal(err)
	}
	restored := openStore(t, dir)
	if n := count(t, restored, "SELECT COUNT(*) FROM projects WHERE block_tag = '' AND number_to = 0 AND provisional_from >= ?", ProvisionalFloor); n != 1 {
		t.Fatalf("the restored store kept its inherited block")
	}
}

// The log is read whole, page by page: every row that replicates in seq
// order with its origin, the others skipped, a row of another replica's given back with its raw actor and
// its origin, a remove's prior naming canonical tags, a plan's create its
// view; the position to read from next; a page within its bounds.
func TestChangesFromReadsTheWholeLog(t *testing.T) {
	ctx := context.Background()
	s, _, key := planTicket(t)
	join(t, s, "a1")
	l := mustLabel(t, s, "lbl", "CND", "bug")
	mustEdit(t, s, "label-on", key, TicketEdit{Add: TicketMembers{Labels: []string{l}}})
	mustEdit(t, s, "label-off", key, TicketEdit{Remove: TicketMembers{Labels: []string{l}}})
	planID := propose(t, s, "plan", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	// An attachment's add stays on this machine.
	if _, _, err := s.AddAttachment(ctx, testOp("attach"), key, "notes.txt", "text/plain", strings.NewReader("notes")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO changes (entity, entity_id, field, op, value, at, actor, op_id, n, origin, remote)
		VALUES ('person', 'nate', '', 'create', '{}', '2026-01-01T00:00:00.000000000Z', '@b2/admin', 'remote-op', 0, 'b2', 1)`); err != nil {
		t.Fatal(err)
	}
	every, err := readChanges(ctx, s.db, "ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, c := range every {
		if Replicates(c) {
			total++
		}
	}
	if total == len(every) {
		t.Fatal("every row replicates: the walk shows no row skipped")
	}
	var all []ChangeRow
	for after := int64(0); ; {
		page, next, err := s.ChangesFrom(ctx, after, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 3 || next != page[len(page)-1].Seq {
			t.Fatalf("a page of %d, next %d", len(page), next)
		}
		all = append(all, page...)
		after = next
	}
	if len(all) != total {
		t.Fatalf("%d rows read of %d", len(all), total)
	}
	var sawRemote, sawRemove, sawPlan bool
	for i, c := range all {
		if i > 0 && c.Seq <= all[i-1].Seq {
			t.Fatalf("rows out of seq order at %d", i)
		}
		if c.Origin == "" || strings.HasPrefix(c.Actor, "@") {
			t.Fatalf("a row handed out without its origin or with its actor qualified: %+v", c)
		}
		switch {
		case c.OpID == "remote-op":
			sawRemote = c.Actor == "admin" && c.Origin == "b2" && c.Remote
		case c.Op == OpRemove && c.Field == "labels":
			tags := Tags(c.Prior)
			sawRemove = len(tags) == 1 && tags[0].Actor == "@a1/test" && tags[0].OpID == "label-on"
		case c.Entity == EntityPlan && c.Op == OpCreate:
			sawPlan = c.EntityID == planID && c.ViewDigest != ""
		}
	}
	if !sawRemote || !sawRemove || !sawPlan {
		t.Fatalf("another replica's row %v, a remove's canonical prior %v, a plan's view %v", sawRemote, sawRemove, sawPlan)
	}
	if view, err := s.RevisionView(ctx, all[len(all)-1].ViewDigest); err == nil || view != nil {
		t.Fatalf("a view of no digest: %s %v", view, err)
	}
	for _, bad := range [][2]int64{{0, 0}, {0, MaxChangesPage + 1}, {-1, 10}} {
		if _, _, err := s.ChangesFrom(ctx, bad[0], int(bad[1])); !errors.Is(err, ErrBadQuery) {
			t.Errorf("a page of %d after %d: %v", bad[1], bad[0], err)
		}
	}
}

// mustLabel makes a label in the project with the key, failing the test
// unless it was made, and returns its id.
func mustLabel(t *testing.T, s *Store, opID, project, name string) string {
	t.Helper()
	rc, err := s.CreateLabel(context.Background(), testOp(opID), project, NewLabel{Name: name, Color: LabelColors[0]})
	want(t, rc, err, StatusDone, "")
	return rc.ID
}

// Which rows replicate, and what each needs.
func TestReplicatesAndRefs(t *testing.T) {
	for _, c := range []struct {
		row  ChangeRow
		want bool
	}{
		{ChangeRow{Entity: EntityProject, Op: OpCreate}, true},
		{ChangeRow{Entity: EntityProject, Op: OpSet, Field: "name"}, true},
		{ChangeRow{Entity: EntityProject, Op: OpSet, Field: "cwd"}, false},
		{ChangeRow{Entity: EntityTicket, Op: OpSet, Field: "title"}, true},
		{ChangeRow{Entity: EntityTicket, Op: OpSet, Field: "external_snapshot"}, false},
		{ChangeRow{Entity: EntityTicket, Op: OpSet, Field: "external_id"}, true},
		{ChangeRow{Entity: EntityTicket, Op: OpObserve, Field: "title"}, true},
		{ChangeRow{Entity: EntityTicket, Op: OpAdd, Field: "labels"}, true},
		{ChangeRow{Entity: EntityTicket, Op: OpAdd, Field: "attachments"}, false},
		{ChangeRow{Entity: EntityView, Op: OpCreate}, false},
		{ChangeRow{Entity: EntityView, Op: OpCreate, ProjectID: "p"}, true},
		{ChangeRow{Entity: EntityPlan, Op: OpSet, Field: "state"}, true},
		{ChangeRow{Entity: EntityLaunch, Op: OpCreate}, false},
		{ChangeRow{Entity: EntityReview, Op: OpCreate}, false},
		{ChangeRow{Entity: EntityCapture, Op: OpCreate}, false},
		{ChangeRow{Entity: EntityPublication, Op: OpCreate}, false},
		{ChangeRow{Entity: EntityTicket, Op: OpReconcile, Field: "relations", Actor: ActorMerge}, false},
	} {
		if got := Replicates(c.row); got != c.want {
			t.Errorf("Replicates(%s %s %s) = %v", c.row.Entity, c.row.Op, c.row.Field, got)
		}
	}
	for _, c := range []struct {
		row  ChangeRow
		want []string
	}{
		{ChangeRow{Entity: EntityTicket, EntityID: "t1", ProjectID: "p1", Op: OpCreate}, []string{"project:p1"}},
		{ChangeRow{Entity: EntityComment, EntityID: "c1", TicketID: "t1", Op: OpCreate}, []string{"ticket:t1"}},
		{ChangeRow{Entity: EntityTicket, EntityID: "t1", Field: "labels", Op: OpAdd, Value: "l1"}, []string{"ticket:t1", "label:l1"}},
		{ChangeRow{Entity: EntityTicket, EntityID: "t1", Field: "relations", Op: OpAdd, Value: `{"id":"r","fromId":"t1","toId":"t2"}`}, []string{"ticket:t1", "ticket:t2"}},
		{ChangeRow{Entity: EntityTicket, EntityID: "t1", Field: "title", Op: OpSet}, nil},
	} {
		if got := Refs(c.row); !slices.Equal(got, c.want) {
			t.Errorf("Refs(%s %s %s) = %v, want %v", c.row.Entity, c.row.Op, c.row.Field, got, c.want)
		}
	}
}
