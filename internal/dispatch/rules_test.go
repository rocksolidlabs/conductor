package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestClassForEveryFieldInTheTable(t *testing.T) {
	cases := []struct {
		entity string
		fields []string
		class  FieldClass
	}{
		{EntityTicket, []string{"id", "project_id", "key", "created_at", "created_by"}, ClassImmutable},
		{EntityTicket, []string{"number"}, ClassAllocation},
		{EntityTicket, []string{"status"}, ClassGated},
		{EntityTicket, []string{"title", "description", "acceptance", "priority", "horizon", "owner", "parent_id", "milestone_id",
			"accepted_at", "accepted_by", "accepted_for", "accepted_criteria", "accepted_note", "closed_at", "deleted_at",
			"external_id", "external_url", "external_snapshot", "authority"}, ClassScalar},
		{EntityTicket, []string{"assignees", "ticket_assignees", "labels", "ticket_labels", "relations", "links", "attachments"}, ClassSet},
		{EntityTicket, []string{"comments"}, ClassLog},
		{EntityProject, []string{"id", "created_at"}, ClassImmutable},
		{EntityProject, []string{"key", "next_number", "number_from", "number_to"}, ClassAllocation},
		{EntityProject, []string{"name", "description", "cwd", "tracker", "tracker_url", "close_requires", "archived_at"}, ClassScalar},
		{EntityLabel, []string{"id", "project_id", "created_at"}, ClassImmutable},
		{EntityLabel, []string{"name", "color", "deleted_at"}, ClassScalar},
		{EntityMilestone, []string{"id", "project_id", "created_at"}, ClassImmutable},
		{EntityMilestone, []string{"name", "description", "target", "state", "deleted_at"}, ClassScalar},
		{EntityView, []string{"id", "project_id", "created_at"}, ClassImmutable},
		{EntityView, []string{"name", "kind", "query", "deleted_at"}, ClassScalar},
		{EntityComment, []string{"id", "ticket_id", "author", "by", "created_at"}, ClassImmutable},
		{EntityComment, []string{"body", "edited_at", "deleted_at"}, ClassScalar},
		{EntityPerson, []string{"name", "created_at"}, ClassImmutable},
		{"change", []string{"actor", "op_id", "n"}, ClassImmutable},
		{"change", []string{"value", "prior", "seq"}, ClassLog},
		{"changes", []string{"at"}, ClassLog},
		{EntityTicket, []string{"rev", "updated_at", "nothing"}, ""},
		{"nothing", []string{"id"}, ""},
	}
	for _, c := range cases {
		for _, f := range c.fields {
			if got := Class(c.entity, f); got != c.class {
				t.Errorf("Class(%s, %s) = %q, want %q", c.entity, f, got, c.class)
			}
		}
	}
}

// row is a change row of an actor at a time, for the merge tests.
func row(entity, field, op string, at time.Time, actor, opID string, n int) ChangeRow {
	return ChangeRow{Entity: entity, Field: field, Op: op, At: at, Actor: actor, OpID: opID, N: n}
}

func TestMergeByClass(t *testing.T) {
	t1 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)
	check := func(name string, got, want Winner) {
		t.Helper()
		if got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	// Scalar: the later wins, and the tie-breakers in order.
	check("later scalar", Merge(row("ticket", "title", OpSet, t1, "a", "x", 0), row("ticket", "title", OpSet, t2, "b", "y", 0)), TakeRemote)
	check("earlier scalar", Merge(row("ticket", "title", OpSet, t2, "a", "x", 0), row("ticket", "title", OpSet, t1, "b", "y", 0)), KeepLocal)
	check("equal at, the greater actor", Merge(row("ticket", "title", OpSet, t1, "a", "x", 0), row("ticket", "title", OpSet, t1, "b", "x", 0)), TakeRemote)
	check("equal at, the lesser actor", Merge(row("ticket", "title", OpSet, t1, "b", "x", 0), row("ticket", "title", OpSet, t1, "a", "x", 0)), KeepLocal)
	check("equal at and actor, the greater op id", Merge(row("ticket", "title", OpSet, t1, "a", "1", 0), row("ticket", "title", OpSet, t1, "a", "2", 0)), TakeRemote)
	check("equal at and actor, the lesser op id", Merge(row("ticket", "title", OpSet, t1, "a", "2", 0), row("ticket", "title", OpSet, t1, "a", "1", 0)), KeepLocal)
	check("a project's scalar", Merge(row("project", "name", OpSet, t1, "a", "x", 0), row("project", "name", OpSet, t2, "b", "y", 0)), TakeRemote)
	// Gated: the scalar rule, the policy never re-applied.
	check("gated later", Merge(row("ticket", "status", OpSet, t1, "a", "x", 0), row("ticket", "status", OpSet, t2, "b", "y", 0)), TakeRemote)
	check("gated earlier", Merge(row("ticket", "status", OpSet, t2, "a", "x", 0), row("ticket", "status", OpSet, t1, "b", "y", 0)), KeepLocal)
	// Immutable and allocation: never written twice.
	check("immutable later", Merge(row("ticket", "key", OpSet, t1, "a", "x", 0), row("ticket", "key", OpSet, t2, "b", "y", 0)), KeepLocal)
	check("allocation later", Merge(row("ticket", "number", OpSet, t1, "a", "x", 0), row("ticket", "number", OpSet, t2, "b", "y", 0)), KeepLocal)
	// A second create of an id held is dropped, whenever it was made.
	check("a second create, later", Merge(row("ticket", "", OpCreate, t1, "a", "x", 0), row("ticket", "", OpCreate, t2, "b", "y", 0)), KeepLocal)
	check("a second create, earlier", Merge(row("ticket", "", OpCreate, t2, "a", "x", 0), row("ticket", "", OpCreate, t1, "b", "y", 0)), KeepLocal)
	// A change row of a held identity is dropped, a log row's included.
	check("a held identity", Merge(row("ticket", "title", OpSet, t1, "a", "x", 0), row("ticket", "title", OpSet, t2, "a", "x", 0)), KeepLocal)
	check("a held identity in a log", Merge(row("ticket", "comments", OpAdd, t1, "a", "x", 0), row("ticket", "comments", OpAdd, t1, "a", "x", 0)), KeepLocal)
	// A log is never merged: both rows stand.
	check("a log", Merge(row("ticket", "comments", OpAdd, t1, "a", "x", 0), row("ticket", "comments", OpAdd, t2, "b", "y", 0)), KeepBoth)
	check("the changes log", Merge(row("change", "value", OpSet, t1, "a", "x", 0), row("change", "value", OpSet, t1, "b", "y", 0)), KeepBoth)
	// Sets: observed remove.
	a1 := row("ticket", "labels", OpAdd, t1, "a", "x", 0)
	b1 := row("ticket", "labels", OpAdd, t1, "b", "y", 0)
	removeA1 := row("ticket", "labels", OpRemove, t2, "a", "z", 0)
	removeA1.Prior = EncodeTags([]Tag{TagOf(a1)})
	removeNone := row("ticket", "labels", OpRemove, t2, "b", "w", 0)
	removeNone.Prior = EncodeTags(nil)
	check("two adds", Merge(a1, b1), KeepBoth)
	check("an observed add is removed", Merge(a1, removeA1), TakeRemote)
	check("an add the remove did not see survives, the remove standing for what it names", Merge(b1, removeA1), KeepBoth)
	check("a remove that saw the add drops it", Merge(removeA1, a1), KeepLocal)
	check("a remove that did not see the add keeps both", Merge(removeA1, b1), KeepBoth)
	check("a remove naming nothing", Merge(a1, removeNone), KeepBoth)
	check("a remove naming nothing, from its side", Merge(removeNone, a1), KeepBoth)
	check("two removes", Merge(removeA1, removeNone), KeepBoth)
	// The plan's case: two adds of one member from two actors with equal
	// at, one removed, leave the member present: b1 survives, merged from
	// either side.
	if Merge(b1, removeA1) == TakeRemote || Merge(removeA1, b1) == KeepLocal {
		t.Error("the member removed though an add of it was not seen")
	}
	// A merge makes the same rows whichever side merges: every pair of a
	// set's changes is answered from the two sides as mirrors, keep both
	// both ways, or take remote one way and keep local the other.
	removeB1 := row("ticket", "labels", OpRemove, t2, "a", "v", 0)
	removeB1.Prior = EncodeTags([]Tag{TagOf(b1)})
	removeBoth := row("ticket", "labels", OpRemove, t2, "c", "u", 0)
	removeBoth.Prior = EncodeTags([]Tag{TagOf(a1), TagOf(b1)})
	set := []struct {
		name string
		c    ChangeRow
	}{{"a1", a1}, {"b1", b1}, {"remove a1", removeA1}, {"remove b1", removeB1}, {"remove both", removeBoth}, {"remove none", removeNone}}
	mirror := map[Winner]Winner{KeepBoth: KeepBoth, TakeRemote: KeepLocal, KeepLocal: TakeRemote}
	for i, x := range set {
		for j, y := range set {
			if i == j {
				continue
			}
			if xy, yx := Merge(x.c, y.c), Merge(y.c, x.c); yx != mirror[xy] {
				t.Errorf("Merge(%s, %s) = %v but Merge(%s, %s) = %v", x.name, y.name, xy, y.name, x.name, yx)
			}
		}
	}
	// An unknown field is kept as it is.
	check("unknown", Merge(row("ticket", "nothing", OpSet, t1, "a", "x", 0), row("ticket", "nothing", OpSet, t2, "b", "y", 0)), KeepLocal)
	for _, w := range []Winner{KeepLocal, TakeRemote, KeepBoth, Winner(7)} {
		if w.String() == "" {
			t.Errorf("no words for %d", int(w))
		}
	}
}

func TestTagsRoundTrip(t *testing.T) {
	tags := []Tag{{"a", "x", 0}, {"b", "y", 3}}
	if got := Tags(EncodeTags(tags)); len(got) != 2 || got[0] != tags[0] || got[1] != tags[1] {
		t.Fatalf("round trip: %+v", got)
	}
	if got := Tags(""); got != nil {
		t.Fatalf("no prior: %+v", got)
	}
	if got := Tags("garbage"); got != nil {
		t.Fatalf("garbage: %+v", got)
	}
	if EncodeTags(nil) != "[]" {
		t.Fatalf("no tags: %q", EncodeTags(nil))
	}
	c := ChangeRow{Actor: "a", OpID: "x", N: 2}
	if TagOf(c) != (Tag{"a", "x", 2}) {
		t.Fatalf("TagOf: %+v", TagOf(c))
	}
}

// edge is an edge change at second sec by actor, for the reconcile tests.
func edge(sec int, actor, kind, op, from, to string) EdgeChange {
	return EdgeChange{At: time.Date(2026, 10, 28, 12, 0, sec, 0, time.UTC), Actor: actor, OpID: fmt.Sprintf("%s%d", actor, sec), Kind: kind, Op: op, From: from, To: to}
}

func parent(sec int, actor, child, parent string) EdgeChange {
	return edge(sec, actor, EdgeParent, OpSet, child, parent)
}

func blocks(sec int, actor, from, to string) EdgeChange {
	return edge(sec, actor, EdgeBlocks, OpAdd, from, to)
}

func unblocks(sec int, actor string, add EdgeChange) EdgeChange {
	e := edge(sec, actor, EdgeBlocks, OpRemove, add.From, add.To)
	e.Tags = []Tag{add.Tag()}
	return e
}

func link(sec int, actor, ticket, run string) EdgeChange {
	return edge(sec, actor, EdgeRun, OpAdd, ticket, run)
}

func unlink(sec int, actor string, add EdgeChange) EdgeChange {
	e := edge(sec, actor, EdgeRun, OpRemove, add.From, add.To)
	e.Tags = []Tag{add.Tag()}
	return e
}

func tombstone(sec int, actor, ticket string, deleted bool) EdgeChange {
	e := edge(sec, actor, EdgeTombstone, OpSet, ticket, "")
	e.Deleted = deleted
	return e
}

// refusedNames is the refused edges as "from→to" in order.
func refusedNames(refused []EdgeChange) string {
	var out []string
	for _, e := range refused {
		out = append(out, e.From+"→"+e.To)
	}
	return strings.Join(out, " ")
}

// reversed is changes in the other order.
func reversed(changes []EdgeChange) []EdgeChange {
	out := make([]EdgeChange, len(changes))
	for i, e := range changes {
		out[len(changes)-1-i] = e
	}
	return out
}

// both runs Reconcile on the log in the order given and reversed, and
// fails unless both refuse what is wanted.
func both(t *testing.T, name string, log []EdgeChange, want string) {
	t.Helper()
	for _, l := range [][]EdgeChange{log, reversed(log)} {
		if got := refusedNames(Reconcile(l)); got != want {
			t.Errorf("%s: refused %q, want %q", name, got, want)
		}
	}
}

func TestReconcileParentSets(t *testing.T) {
	// Two concurrent sets that make a 2-cycle: the later refused, from
	// either replica's log in either order.
	both(t, "a 2-cycle", []EdgeChange{parent(1, "a", "A", "B"), parent(2, "b", "B", "A")}, "B→A")
	both(t, "a 2-cycle the other way", []EdgeChange{parent(2, "a", "A", "B"), parent(1, "b", "B", "A")}, "A→B")
	// Three that make a 3-cycle.
	both(t, "a 3-cycle", []EdgeChange{parent(1, "a", "A", "B"), parent(2, "b", "B", "C"), parent(3, "a", "C", "A")}, "C→A")
	// A chain of 9: the set that makes the ninth is refused; 8 stands.
	var chain []EdgeChange
	for i := 2; i <= 9; i++ {
		chain = append(chain, parent(i, "a", fmt.Sprintf("T%d", i), fmt.Sprintf("T%d", i-1)))
	}
	both(t, "a chain of 9", chain, "T9→T8")
	both(t, "a chain of 8", chain[:7], "")
	// A subtree of height 4 moved under a chain of 5 is refused, under a
	// chain of 4 allowed.
	var log []EdgeChange
	for i := 2; i <= 5; i++ {
		log = append(log, parent(i, "a", fmt.Sprintf("C%d", i), fmt.Sprintf("C%d", i-1)))
	}
	for i := 2; i <= 4; i++ {
		log = append(log, parent(10+i, "b", fmt.Sprintf("S%d", i), fmt.Sprintf("S%d", i-1)))
	}
	both(t, "a subtree of 4 under a chain of 5", append(append([]EdgeChange(nil), log...), parent(20, "a", "S1", "C5")), "S1→C5")
	both(t, "a subtree of 4 under a chain of 4", append(append([]EdgeChange(nil), log...), parent(20, "a", "S1", "C4")), "")
	// A set that makes a cycle through a longer chain, and a ticket set
	// under itself.
	both(t, "a cycle through the chain", append(append([]EdgeChange(nil), log...), parent(20, "a", "C1", "C5")), "C1→C5")
	both(t, "under itself", []EdgeChange{parent(1, "a", "A", "A")}, "A→A")
	// A clear is always admitted, and frees what it held.
	both(t, "a clear", []EdgeChange{parent(1, "a", "A", "B"), parent(2, "a", "A", ""), parent(3, "b", "B", "A")}, "")
	// A refused set stays refused: a later clear of the other side does
	// not admit it after the fact (serial).
	both(t, "serial", []EdgeChange{parent(1, "a", "A", "B"), parent(2, "b", "B", "A"), parent(3, "a", "A", "")}, "B→A")
	// A reparent refused on a ticket that has a parent leaves it with
	// none, as its repair clears parent_id: B→A replaced B→C under the
	// scalar rule, so C→B, a cycle only with B→C, stands.
	both(t, "a reparent refused on a ticket with a parent", []EdgeChange{parent(1, "a", "B", "C"), parent(2, "b", "A", "B"), parent(3, "a", "B", "A"), parent(4, "b", "C", "B")}, "B→A")
	// The parent cleared counts for nothing in a chain either: T5's
	// reparent under T7 is refused, which leaves T5 a root and T8 4 deep,
	// so a ticket set under T8 is 5 deep and stands.
	cut := append(append([]EdgeChange(nil), chain[:7]...), parent(20, "b", "T5", "T7"), parent(21, "a", "Y", "T8"))
	both(t, "a chain cut by a refused reparent", cut, "T5→T7")
	// The input is left as it was.
	in := []EdgeChange{parent(2, "b", "B", "A"), parent(1, "a", "A", "B")}
	Reconcile(in)
	if in[0].From != "B" {
		t.Fatal("Reconcile reordered its input")
	}
}

func TestReconcileBlocks(t *testing.T) {
	// Two blocks that make a cycle: the later refused, either order.
	both(t, "a 2-cycle", []EdgeChange{blocks(1, "a", "A", "B"), blocks(2, "b", "B", "A")}, "B→A")
	both(t, "a 3-cycle", []EdgeChange{blocks(1, "a", "A", "B"), blocks(2, "b", "B", "C"), blocks(3, "a", "C", "A")}, "C→A")
	both(t, "a self edge", []EdgeChange{blocks(1, "a", "A", "A")}, "A→A")
	// The four edges of the field rules: B→A, B→C, A→B, C→A in log order
	// leave B→A, B→C and C→A and refuse A→B, delivered whole, as {1, 3}
	// then {2, 4}, and as {2, 3, 4} then {1}.
	e := []EdgeChange{blocks(1, "a", "B", "A"), blocks(2, "b", "B", "C"), blocks(3, "a", "A", "B"), blocks(4, "b", "C", "A")}
	deliveries := map[string][][]EdgeChange{
		"whole":              {e},
		"{1, 3} then {2, 4}": {{e[0], e[2]}, {e[1], e[3]}},
		"{2, 3, 4} then {1}": {{e[1], e[2], e[3]}, {e[0]}},
	}
	for name, batches := range deliveries {
		var log []EdgeChange
		for _, b := range batches {
			log = append(log, b...)
			// After every batch the answer is a function of the log so far.
			if got := refusedNames(Reconcile(log)); len(log) == 4 && got != "A→B" {
				t.Errorf("%s: refused %q after the whole log", name, got)
			}
		}
	}
	// A repair made against each batch's state would differ: the edges a
	// batch admitted taken as the state the next batch meets (as if they
	// came first), {2, 3, 4} refuses C→A, and then {1} refuses B→A.
	stateRepair := func(batches [][]EdgeChange) string {
		var state []EdgeChange
		var refused []string
		for _, batch := range batches {
			log := append(append([]EdgeChange(nil), state...), batch...)
			out := Reconcile(log)
			refused = append(refused, refusedNames(out))
			for _, e := range batch {
				if !hasTag(tagsOf(out), e.Tag()) {
					e.At = time.Time{} // admitted: state, before anything that follows
					state = append(state, e)
				}
			}
		}
		return strings.TrimSpace(strings.Join(refused, " "))
	}
	if got := stateRepair(deliveries["{2, 3, 4} then {1}"]); got != "C→A B→A" {
		t.Errorf("a repair against each batch's state refused %q", got)
	}
	if got := stateRepair(deliveries["whole"]); got != "A→B" {
		t.Errorf("the whole log against no state refused %q", got)
	}
	// An edge refused after one batch and admitted once an earlier remove
	// arrives: {A→B at 1, B→A at 3} refuses B→A; the remove of A→B at 2
	// arriving admits it, restored with a second repair row.
	add := blocks(1, "a", "A", "B")
	batch1 := []EdgeChange{add, blocks(3, "b", "B", "A")}
	if got := refusedNames(Reconcile(batch1)); got != "B→A" {
		t.Fatalf("before the remove: %q", got)
	}
	log := append(append([]EdgeChange(nil), batch1...), unblocks(2, "a", add))
	both(t, "after the remove", log, "")
	refusedRow := Repair(batch1[1], false, time.Now(), 0)
	restoredRow := Repair(batch1[1], true, time.Now(), 1)
	if refusedRow.Actor != ActorMerge || restoredRow.Actor != ActorMerge || refusedRow.OpID != restoredRow.OpID || refusedRow.N == restoredRow.N {
		t.Fatalf("the repair rows: %+v %+v", refusedRow, restoredRow)
	}
	// The reverse: an edge admitted after one batch and refused once an
	// earlier add arrives.
	if got := refusedNames(Reconcile([]EdgeChange{blocks(3, "b", "B", "A")})); got != "" {
		t.Fatalf("alone: %q", got)
	}
	both(t, "an earlier add arrives", []EdgeChange{blocks(3, "b", "B", "A"), blocks(1, "a", "A", "B")}, "B→A")
	// Two adds of one member are two tags, both needed gone: a remove
	// naming one leaves the edge, and the reverse edge is still refused.
	add2 := blocks(2, "b", "A", "B")
	both(t, "two tags, one removed", []EdgeChange{add, add2, unblocks(3, "a", add), blocks(4, "b", "B", "A")}, "B→A")
	both(t, "two tags, both removed", []EdgeChange{add, add2, unblocks(3, "a", add), unblocks(4, "b", add2), blocks(5, "b", "B", "A")}, "")
	// A remove of every tag names them all.
	all := edge(3, "a", EdgeBlocks, OpRemove, "A", "B")
	all.Tags = []Tag{add.Tag(), add2.Tag()}
	both(t, "a remove of every tag", []EdgeChange{add, add2, all, blocks(4, "b", "B", "A")}, "")
	// A remove naming no tag removes nothing, as Merge and the set rule
	// read it: the edge stands, so the reverse edge is refused.
	none := edge(3, "a", EdgeBlocks, OpRemove, "A", "B")
	both(t, "a remove naming no tag", []EdgeChange{add, none, blocks(4, "b", "B", "A")}, "B→A")
	addRow := ChangeRow{Entity: EntityTicket, Field: "relations", Op: OpAdd, At: add.At, Actor: add.Actor, OpID: add.OpID, N: add.N}
	noneRow := ChangeRow{Entity: EntityTicket, Field: "relations", Op: OpRemove, At: none.At, Actor: none.Actor, OpID: none.OpID, N: none.N, Prior: EncodeTags(none.Tags)}
	if Merge(addRow, noneRow) == TakeRemote || Merge(noneRow, addRow) == KeepLocal {
		t.Error("Merge and Reconcile disagree on a remove naming no tag: Merge removes the add")
	}
	// A remove that sorts before the add it names (its writer's clock
	// behind the adder's) removes it all the same, as the set rule does
	// whatever the clocks say: the add never stands, and the reverse edge
	// is admitted.
	late := blocks(20, "b", "A", "B")
	early := unblocks(10, "a", late)
	both(t, "a remove before its add", []EdgeChange{early, late, blocks(30, "a", "B", "A")}, "")
	// Only the member it names: the same tag on another member's add is
	// not removed.
	elsewhere := unblocks(10, "a", late)
	elsewhere.From, elsewhere.To = "C", "D"
	both(t, "a remove of another member", []EdgeChange{elsewhere, late, blocks(30, "a", "B", "A")}, "B→A")
	// A refused add stays refused when the edge that refused it goes
	// later (serial), and a remove of a refused add changes nothing.
	both(t, "serial", []EdgeChange{add, blocks(2, "b", "B", "A"), unblocks(3, "a", add)}, "B→A")
}

func tagsOf(changes []EdgeChange) []Tag {
	var out []Tag
	for _, e := range changes {
		out = append(out, e.Tag())
	}
	return out
}

func TestReconcileRunLinks(t *testing.T) {
	// Two run links of one run on two tickets: the later refused.
	both(t, "two tickets", []EdgeChange{link(1, "a", "T1", "R"), link(2, "b", "T2", "R")}, "T2→R")
	both(t, "two tickets the other way", []EdgeChange{link(2, "a", "T1", "R"), link(1, "b", "T2", "R")}, "T1→R")
	// Two runs, no conflict; one ticket linking its run twice, two tags.
	both(t, "two runs", []EdgeChange{link(1, "a", "T1", "R1"), link(2, "b", "T2", "R2")}, "")
	both(t, "one ticket twice", []EdgeChange{link(1, "a", "T1", "R"), link(2, "b", "T1", "R")}, "")
	// A link removed frees the run; a refused link stays refused when
	// the holder's link goes later (serial).
	first := link(1, "a", "T1", "R")
	both(t, "removed then taken", []EdgeChange{first, unlink(2, "a", first), link(3, "b", "T2", "R")}, "")
	both(t, "taken then removed", []EdgeChange{first, link(2, "b", "T2", "R"), unlink(3, "a", first)}, "T2→R")
	// A tombstone holds no run: a deleted ticket's link lets another
	// ticket take the run, and the undelete while the other holds it
	// leaves the undeleted ticket's link refused.
	both(t, "deleted then taken", []EdgeChange{first, tombstone(2, "a", "T1", true), link(3, "b", "T2", "R")}, "")
	both(t, "undeleted while held", []EdgeChange{first, tombstone(2, "a", "T1", true), link(3, "b", "T2", "R"), tombstone(4, "a", "T1", false)}, "T1→R")
	// Undeleted while nobody holds it: the link holds the run again.
	both(t, "undeleted, free", []EdgeChange{first, tombstone(2, "a", "T1", true), tombstone(3, "a", "T1", false), link(4, "b", "T2", "R")}, "T2→R")
	second := link(3, "b", "T2", "R")
	both(t, "undeleted after the other let go", []EdgeChange{first, tombstone(2, "a", "T1", true), second, unlink(4, "b", second), tombstone(5, "a", "T1", false), link(6, "b", "T3", "R")}, "T3→R")
	// A link added to a tombstone (its writer had not seen the delete)
	// holds no run while the ticket is deleted, so the live ticket holding
	// the run then does not refuse it: it stands, inactive.
	taken := link(2, "b", "T2", "R")
	inactive := link(3, "a", "T1", "R")
	both(t, "a link added to a tombstone", []EdgeChange{tombstone(1, "b", "T1", true), taken, inactive}, "")
	// It comes back with the undelete, and holds the run when no other
	// live ticket does at that moment.
	both(t, "a link added to a tombstone, the run free at the undelete", []EdgeChange{tombstone(1, "b", "T1", true), taken, inactive, unlink(4, "b", taken), tombstone(5, "b", "T1", false), link(6, "b", "T3", "R")}, "T3→R")
	// Held by another live ticket at the undelete, it is refused there.
	both(t, "a link added to a tombstone, the run held at the undelete", []EdgeChange{tombstone(1, "b", "T1", true), taken, inactive, tombstone(5, "b", "T1", false)}, "T1→R")
	// A remove naming no tag removes nothing: the run stays held.
	none := edge(2, "a", EdgeRun, OpRemove, "T1", "R")
	both(t, "a remove naming no tag", []EdgeChange{first, none, link(3, "b", "T2", "R")}, "T2→R")
	// A remove that sorts before the link it names removes it all the
	// same: the link never holds the run, and another ticket takes it.
	late := link(20, "a", "T1", "R")
	early := unlink(10, "b", late)
	both(t, "a remove before its link", []EdgeChange{early, late, link(30, "b", "T2", "R")}, "")
	// Every refused edge of the undelete case is the link, the edge.
	out := Reconcile([]EdgeChange{first, tombstone(2, "a", "T1", true), second, tombstone(4, "a", "T1", false)})
	if len(out) != 1 || out[0].Kind != EdgeRun || out[0].Tag() != first.Tag() {
		t.Fatalf("the undelete's refusal: %+v", out)
	}
}

func TestRepairRowsCarryTheMergeActor(t *testing.T) {
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		e     EdgeChange
		field string
	}{
		{parent(1, "a", "A", "B"), "parent_id"},
		{blocks(2, "b", "A", "B"), "relations"},
		{link(3, "a", "T1", "R"), "links"},
	} {
		for n, admitted := range []bool{false, true} {
			r := Repair(c.e, admitted, at, n)
			var tag Tag
			if err := json.Unmarshal([]byte(r.OpID), &tag); err != nil || tag != c.e.Tag() {
				t.Fatalf("the repair row's op id %q is not the edge's tag: %+v %v", r.OpID, tag, err)
			}
			if r.Actor != ActorMerge || r.Op != OpReconcile || r.Entity != EntityTicket || r.EntityID != c.e.From || r.TicketID != c.e.From ||
				r.Field != c.field || r.N != n || !r.At.Equal(at) || r.Prior != "" {
				t.Fatalf("the repair row of %+v: %+v", c.e, r)
			}
			var value struct {
				Kind, From, To string
				Tag            Tag
				Admitted       bool
			}
			if err := json.Unmarshal([]byte(r.Value), &value); err != nil || value.Kind != c.e.Kind || value.From != c.e.From || value.To != c.e.To || value.Tag != c.e.Tag() || value.Admitted != admitted {
				t.Fatalf("the repair row's value: %s %v", r.Value, err)
			}
			if Class(r.Entity, r.Field) == "" {
				t.Fatalf("the repair row's field %s has no class", r.Field)
			}
		}
	}
}

// Two edges whose actor and op id differ only in where a '.' falls (an
// op id may hold one, and so may a crew member's name in an attempt's
// actor) get two repair identities: a join of the parts with '.' would
// give both agent:a.b.c.0, and the second repair row would trip the log's
// UNIQUE(actor, op_id, n).
func TestRepairIdentitiesNeverCollide(t *testing.T) {
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	a := blocks(1, "agent:a.b", "A", "B")
	a.OpID = "c"
	b := blocks(1, "agent:a", "C", "D")
	b.OpID = "b.c"
	ra, rb := Repair(a, false, at, 0), Repair(b, false, at, 0)
	if TagOf(ra) == TagOf(rb) {
		t.Fatalf("two edges' repairs share the identity %+v", TagOf(ra))
	}
	// In a store's log, both rows are kept.
	s := openStore(t, storeDir(t))
	for _, r := range []ChangeRow{ra, rb} {
		if _, err := s.db.Exec(`INSERT INTO changes (entity, entity_id, ticket_id, field, op, value, at, actor, op_id, n) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Entity, r.EntityID, r.TicketID, r.Field, r.Op, r.Value, stamp(r.At), r.Actor, r.OpID, r.N); err != nil {
			t.Fatalf("the repair row of %s: %v", r.EntityID, err)
		}
	}
}

// Two replicas' logs merged come out in the one order, the same from
// either side: at, then actor, then op_id, then n.
func TestTheOneOrderOfTheLog(t *testing.T) {
	t1 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Nanosecond)
	a := []ChangeRow{row("ticket", "title", OpSet, t2, "a", "x", 0), row("ticket", "labels", OpAdd, t1, "a", "y", 1), row("ticket", "labels", OpAdd, t1, "a", "y", 0), row("ticket", "title", OpSet, t1, "a", "z", 0)}
	b := []ChangeRow{row("ticket", "title", OpSet, t1, "b", "x", 0), row("ticket", "owner", OpSet, t2, "b", "w", 0), row("ticket", "priority", OpSet, t1, "a", "y", 2)}
	key := func(rows []ChangeRow) string {
		var out []string
		for _, r := range rows {
			out = append(out, fmt.Sprintf("%s/%s/%d@%d", r.Actor, r.OpID, r.N, r.At.Sub(t1)))
		}
		return strings.Join(out, " ")
	}
	ab := append(append([]ChangeRow(nil), a...), b...)
	ba := append(append([]ChangeRow(nil), b...), a...)
	Order(ab)
	Order(ba)
	want := "a/y/0@0 a/y/1@0 a/y/2@0 a/z/0@0 b/x/0@0 a/x/0@1 b/w/0@1"
	if key(ab) != want || key(ba) != want {
		t.Fatalf("the one order:\n ab %s\n ba %s\nwant %s", key(ab), key(ba), want)
	}
	if !Less(ab[0], ab[1]) || Less(ab[1], ab[0]) || Less(ab[0], ab[0]) {
		t.Fatal("Less is not a strict order")
	}
}

// The uniqueness rules on the store, in both delivery orders. What the
// schema keeps unique beyond the minted keys is a rule of the operation,
// never an index, so a merge (Program item CS1's) may bring a second
// label of one name, a second add of one relation or link, a second
// create of one person, a reference to what another replica deleted. The
// rows another replica's operation made are written here by hand, as
// that merge would write them (its row, its change rows under its actor
// and time), before or after this store's own operation or each other;
// the store's reads and local operations answer the same whichever came
// first.

// replica is another replica's writer: its actor and the time of its
// operation.
type replica struct {
	actor string
	at    time.Time
}

// change writes one change row of the replica's operation opID, n as
// given.
func (r replica) change(t *testing.T, s *Store, opID string, n int, c ChangeRow) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO changes (entity, entity_id, ticket_id, project_id, field, op, value, prior, at, actor, by, op_id, n)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Entity, c.EntityID, c.TicketID, c.ProjectID, c.Field, c.Op, c.Value, c.Prior, stamp(r.at), r.actor, r.actor, opID, n); err != nil {
		t.Fatal(err)
	}
}

// exec runs a statement of the merge's on s.
func exec(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// fill writes, by hand in one transaction, links external links of the
// ticket with the id and relations relates relations from it to as many
// live tickets of the project, made here (keys from <project>-1000): a
// set brought near its bound without the operations that would make it.
func fill(t *testing.T, s *Store, project, projectID, ticketID string, links, relations int) {
	t.Helper()
	now := stamp(time.Now())
	x, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	for i := range links {
		url := fmt.Sprintf("https://example.com/filled/%d", i)
		if _, err := x.Exec("INSERT INTO links (id, ticket_id, kind, ref, url, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			newID(), ticketID, LinkExternal, url, url, now); err != nil {
			t.Fatal(err)
		}
	}
	for i := range relations {
		other := newID()
		if _, err := x.Exec("INSERT INTO tickets (id, project_id, number, key, title, created_at, updated_at, rev) VALUES (?, ?, ?, ?, 'Filled', ?, ?, 1)",
			other, projectID, 1000+i, fmt.Sprintf("%s-%d", project, 1000+i), now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := x.Exec("INSERT INTO relations (id, from_id, to_id, kind, created_at) VALUES (?, ?, ?, ?, ?)",
			newID(), ticketID, other, RelationRelates, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
}

// relation writes the relation the replica's relation.add made: its row
// and its add row on its from ticket. It returns the relation's id.
func (r replica) relation(t *testing.T, s *Store, opID, projectID, from, to, kind string) string {
	t.Helper()
	rel := Relation{ID: newID(), FromID: from, ToID: to, Kind: kind, CreatedAt: r.at, CreatedBy: r.actor}
	exec(t, s, "INSERT INTO relations (id, from_id, to_id, kind, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)", rel.ID, from, to, kind, stamp(r.at), r.actor)
	value, err := encodeValue(rel)
	if err != nil {
		t.Fatal(err)
	}
	r.change(t, s, opID, 0, ChangeRow{Entity: EntityTicket, EntityID: from, TicketID: from, ProjectID: projectID, Field: "relations", Op: OpAdd, Value: value})
	return rel.ID
}

// link writes the link the replica's link.add made: its row and its add
// row. It returns the link's id.
func (r replica) link(t *testing.T, s *Store, opID, projectID, ticketID string, l NewLink) string {
	t.Helper()
	ln := Link{ID: newID(), TicketID: ticketID, Kind: l.Kind, Ref: l.Ref, URL: l.URL, Note: l.Note, CreatedAt: r.at, CreatedBy: r.actor}
	exec(t, s, "INSERT INTO links (id, ticket_id, kind, ref, url, note, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		ln.ID, ticketID, ln.Kind, ln.Ref, ln.URL, ln.Note, stamp(r.at), r.actor)
	value, err := encodeValue(ln)
	if err != nil {
		t.Fatal(err)
	}
	r.change(t, s, opID, 0, ChangeRow{Entity: EntityTicket, EntityID: ticketID, TicketID: ticketID, ProjectID: projectID, Field: "links", Op: OpAdd, Value: value})
	return ln.ID
}

// label writes the label the replica's label.create made: its row, its
// create row and its two set rows. It returns the label's id.
func (r replica) label(t *testing.T, s *Store, opID, projectID, name, color string) string {
	t.Helper()
	id := newID()
	exec(t, s, "INSERT INTO labels (id, project_id, name, color, created_at, rev) VALUES (?, ?, ?, ?, ?, 1)", id, projectID, name, color, stamp(r.at))
	value, err := encodeValue(labelCreated{ID: id, ProjectID: projectID, CreatedAt: r.at})
	if err != nil {
		t.Fatal(err)
	}
	base := ChangeRow{Entity: EntityLabel, EntityID: id, ProjectID: projectID}
	for n, c := range []ChangeRow{{Op: OpCreate, Value: value}, {Field: "name", Op: OpSet, Value: name}, {Field: "color", Op: OpSet, Value: color}} {
		row := base
		row.Field, row.Op, row.Value = c.Field, c.Op, c.Value
		r.change(t, s, opID, n, row)
	}
	return id
}

// Two creates of one label name in one project, on two replicas, leave
// both labels, whichever arrives first and whether one of them is this
// store's own: the local rule refused only what its writer saw. A local
// create of that name is refused while either lives; renaming one frees
// nothing of the other's.
func TestTwoCreatesOfOneLabelNameLeaveBothLabels(t *testing.T) {
	b := replica{"replica-b", time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	c := replica{"replica-c", time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)}
	var reads []string
	for _, order := range []string{"local, then b", "b, then c", "c, then b"} {
		s := openStore(t, storeDir(t))
		ctx := context.Background()
		pid := newProject(t, s, "CND")
		switch order {
		case "local, then b":
			newLabel(t, s, "CND", "api")
			b.label(t, s, "lb", pid, "api", "red")
		case "b, then c":
			b.label(t, s, "lb", pid, "api", "red")
			c.label(t, s, "lc", pid, "api", "green")
		case "c, then b":
			c.label(t, s, "lc", pid, "api", "green")
			b.label(t, s, "lb", pid, "api", "red")
		}
		labels, err := s.Labels(ctx, "CND")
		if err != nil || len(labels) != 2 || labels[0].Name != "api" || labels[1].Name != "api" || labels[0].ID == labels[1].ID {
			t.Fatalf("%s: the labels %+v %v", order, labels, err)
		}
		if order != "local, then b" {
			// Read in the order of their making, whatever the arrival.
			reads = append(reads, labels[0].Color+","+labels[1].Color)
		}
		rc, err := s.CreateLabel(ctx, testOp("third"), "CND", NewLabel{Name: "API", Color: "blue"})
		want(t, rc, err, StatusFailed, RefusalInvalid)
		rc, err = s.EditLabel(ctx, testOp("rename"), "CND", labels[0].ID, LabelEdit{Name: ptr("backend")})
		want(t, rc, err, StatusDone, "")
		rc, err = s.CreateLabel(ctx, testOp("third2"), "CND", NewLabel{Name: "api", Color: "blue"})
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
	if reads[0] != reads[1] || reads[0] != "green,red" {
		t.Fatalf("the two arrival orders read %v", reads)
	}
}

// Two adds of one relation and of one link from two replicas (the
// link's with two URLs and two notes) leave one member with two tags:
// read as the later add's row (its id, and the link's URL and note), in
// both delivery orders; a local remove through that id names both tags
// and takes every row of the member, so it is gone on every replica.
func TestTwoAddsOfOneMemberReadAsTheLaterAdd(t *testing.T) {
	early := replica{"replica-a", time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	late := replica{"replica-b", early.at.Add(time.Second)}
	// At one moment the greater actor's add is the later.
	tieLow := replica{"replica-a", early.at}
	tieHigh := replica{"replica-z", early.at}
	for _, c := range []struct {
		name          string
		first, second replica
		later         replica
	}{
		{"early, then late", early, late, late},
		{"late, then early", late, early, late},
		{"a tie, low then high", tieLow, tieHigh, tieHigh},
		{"a tie, high then low", tieHigh, tieLow, tieHigh},
	} {
		s := openStore(t, storeDir(t))
		ctx := context.Background()
		pid := newProject(t, s, "CND")
		aID, a := newTicket(t, s, "CND", "A")
		bID, _ := newTicket(t, s, "CND", "B")
		rels := map[string]string{}
		links := map[string]string{}
		for i, r := range []replica{c.first, c.second} {
			op := opID("add", i)
			rels[r.actor] = r.relation(t, s, op+"r", pid, aID, bID, RelationBlocks)
			links[r.actor] = r.link(t, s, op+"l", pid, aID, NewLink{Kind: LinkDelivery, Ref: "#7", URL: "https://" + r.actor + ".example.com/pull/7", Note: "from " + r.actor})
		}
		rs, err := s.Relations(ctx, a)
		if err != nil || len(rs) != 1 || rs[0].ID != rels[c.later.actor] {
			t.Fatalf("%s: the relations %+v %v, want %s", c.name, rs, err, rels[c.later.actor])
		}
		ls, err := s.Links(ctx, a)
		if err != nil || len(ls) != 1 || ls[0].ID != links[c.later.actor] || ls[0].URL != "https://"+c.later.actor+".example.com/pull/7" || ls[0].Note != "from "+c.later.actor {
			t.Fatalf("%s: the links %+v %v", c.name, ls, err)
		}
		// One member: a local add of it is refused. (It takes one place in
		// a bound: TestAMemberOfTwoAddsTakesOnePlaceInItsBound.)
		rc, err := relate(s, "again", a, RelationBlocks, "CND-2")
		want(t, rc, err, StatusFailed, RefusalInvalid)
		rc, err = addLink(s, "again-l", a, NewLink{Kind: LinkDelivery, Ref: "#7", URL: "https://example.com/x"})
		want(t, rc, err, StatusFailed, RefusalInvalid)
		// A local remove through the standing id names both tags, in the
		// log's order, and leaves no live row of the member.
		tags := func(suffix string) string {
			first, second := Tag{c.first.actor, "add0" + suffix, 0}, Tag{c.second.actor, "add1" + suffix, 0}
			if c.second.at.Before(c.first.at) || (c.second.at.Equal(c.first.at) && c.second.actor < c.first.actor) {
				first, second = second, first
			}
			return EncodeTags([]Tag{first, second})
		}
		rc, err = s.RemoveRelation(ctx, testOp("x-r"), a, rs[0].ID)
		want(t, rc, err, StatusDone, "")
		if rows := changesOf(t, s, "test", "x-r"); len(rows) != 1 || rows[0].Prior != tags("r") {
			t.Fatalf("%s: the relation's remove %+v, want the prior %s", c.name, rows, tags("r"))
		}
		rc, err = s.RemoveLink(ctx, testOp("x-l"), a, ls[0].ID)
		want(t, rc, err, StatusDone, "")
		if rows := changesOf(t, s, "test", "x-l"); len(rows) != 1 || rows[0].Prior != tags("l") {
			t.Fatalf("%s: the link's remove %+v, want the prior %s", c.name, rows, tags("l"))
		}
		if n := count(t, s, "SELECT COUNT(*) FROM relations WHERE deleted_at = ''") + count(t, s, "SELECT COUNT(*) FROM links WHERE deleted_at = ''"); n != 0 {
			t.Fatalf("%s: %d live rows of the members after their removes", c.name, n)
		}
	}
}

// A member two replicas added takes one place in its ticket's bound,
// however many rows it has: with the rest of each bound but one place
// written by hand, the ticket takes one more link and one more relation,
// the last places, and refuses the next of each.
func TestAMemberOfTwoAddsTakesOnePlaceInItsBound(t *testing.T) {
	s := openStore(t, storeDir(t))
	pid := newProject(t, s, "CND")
	aID, a := newTicket(t, s, "CND", "A")
	bID, _ := newTicket(t, s, "CND", "B")
	early := replica{"replica-a", time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	late := replica{"replica-b", early.at.Add(time.Second)}
	for i, r := range []replica{early, late} {
		r.relation(t, s, opID("r", i), pid, aID, bID, RelationBlocks)
		r.link(t, s, opID("l", i), pid, aID, NewLink{Kind: LinkDelivery, Ref: "#7", URL: "https://example.com/pull/7"})
	}
	if n := count(t, s, "SELECT COUNT(*) FROM links WHERE ticket_id = ? AND deleted_at = ''", aID) +
		count(t, s, "SELECT COUNT(*) FROM relations WHERE from_id = ? AND deleted_at = ''", aID); n != 4 {
		t.Fatalf("%d live rows of the two members, want two each", n)
	}
	fill(t, s, "CND", pid, aID, MaxLinks-2, MaxRelations-2)
	rc, err := addLink(s, "last-l", a, NewLink{Kind: LinkExternal, URL: "https://example.com/last"})
	want(t, rc, err, StatusDone, "")
	rc, err = addLink(s, "past-l", a, NewLink{Kind: LinkExternal, URL: "https://example.com/past"})
	want(t, rc, err, StatusFailed, RefusalBound)
	if !strings.Contains(rc.Error, fmt.Sprintf("this would make %d", MaxLinks+1)) {
		t.Fatalf("the links' refusal: %q", rc.Error)
	}
	_, last := newTicket(t, s, "CND", "Last")
	rc, err = relate(s, "last-r", a, RelationRelates, last)
	want(t, rc, err, StatusDone, "")
	_, past := newTicket(t, s, "CND", "Past")
	rc, err = relate(s, "past-r", a, RelationRelates, past)
	want(t, rc, err, StatusFailed, RefusalBound)
	if !strings.Contains(rc.Error, fmt.Sprintf("this would make %d", MaxRelations+1)) {
		t.Fatalf("the relations' refusal: %q", rc.Error)
	}
}

// A member more replicas added than one row's prior can name, each add's
// tag as long as tags get, is removed whole all the same, by each path a
// remove takes: a link, a relation, an assignee through ticket.edit, and a
// run link a ticket's undelete releases. The remove's tags go into as
// many rows as they need, each prior within MaxChangeValue, every add's
// tag named once, in the log's order, and no live row of the member is
// left.
func TestAMemberOfManyReplicasAddsIsRemovedWhole(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	s.SetRunExists(runs("run1"))
	pid := newProject(t, s, "CND")
	aID, a := newTicket(t, s, "CND", "A")
	bID, _ := newTicket(t, s, "CND", "B")
	rID, r := newTicket(t, s, "CND", "Ran first")
	_, live := newTicket(t, s, "CND", "Runs it now")
	// 200 replicas, each actor MaxActor bytes, most of them '<', which a
	// tag escapes to six, and each op id 32: 200 tags of 355 bytes, more
	// than one prior holds.
	const replicas = 200
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	// adds writes every replica's add of one member, in one transaction as
	// a merge would bring them, a millisecond apart: row writes the add's
	// own row of the member, when it has one, and gives the add's value. It
	// gives the adds' tags in the log's order.
	adds := func(prefix, ticketID, field string, row func(x *sql.Tx, actor string, when time.Time) string) []Tag {
		t.Helper()
		x, err := s.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer x.Rollback()
		var tags []Tag
		for i := range replicas {
			actor := fmt.Sprintf("replica-%04d-", i)
			actor += strings.Repeat("<", MaxActor-len(actor))
			opID := prefix + fmt.Sprintf("%0*d", 32-len(prefix), i)
			when := at.Add(time.Duration(i) * time.Millisecond)
			if _, err := x.Exec(`INSERT INTO changes (entity, entity_id, ticket_id, project_id, field, op, value, prior, at, actor, by, op_id, n)
				VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, 0)`,
				EntityTicket, ticketID, ticketID, pid, field, OpAdd, row(x, actor, when), stamp(when), actor, actor, opID); err != nil {
				t.Fatal(err)
			}
			tags = append(tags, Tag{actor, opID, 0})
		}
		if err := x.Commit(); err != nil {
			t.Fatal(err)
		}
		if n := len(EncodeTags(tags)); n <= MaxChangeValue {
			t.Fatalf("the %d tags are %d bytes: one prior holds them", len(tags), n)
		}
		return tags
	}
	link := func(ticketID, kind, ref, url string) func(*sql.Tx, string, time.Time) string {
		return func(x *sql.Tx, actor string, when time.Time) string {
			l := Link{ID: newID(), TicketID: ticketID, Kind: kind, Ref: ref, URL: url, CreatedAt: when, CreatedBy: actor}
			if _, err := x.Exec("INSERT INTO links (id, ticket_id, kind, ref, url, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?)",
				l.ID, l.TicketID, l.Kind, l.Ref, l.URL, stamp(when), actor); err != nil {
				t.Fatal(err)
			}
			value, err := encodeValue(l)
			if err != nil {
				t.Fatal(err)
			}
			return value
		}
	}
	// removedWhole checks the rows the operation opID wrote after its first
	// skip: each a remove of the field, all of one value, each prior within
	// MaxChangeValue, naming the tags between them once each, in order.
	removedWhole := func(opID, field string, skip int, tags []Tag) {
		t.Helper()
		rows := changesOf(t, s, "test", opID)
		if len(rows) < skip+2 {
			t.Fatalf("%s: %d rows, want the tags in more than one", opID, len(rows))
		}
		var named []Tag
		for _, row := range rows[skip:] {
			if row.Op != OpRemove || row.Field != field || row.Value != rows[skip].Value || len(row.Prior) > MaxChangeValue {
				t.Fatalf("%s: a row %s:%s, its prior %d bytes", opID, row.Op, row.Field, len(row.Prior))
			}
			named = append(named, Tags(row.Prior)...)
		}
		if !slices.Equal(named, tags) {
			t.Fatalf("%s: the rows name %d tags, want the %d adds' in order", opID, len(named), len(tags))
		}
	}
	// A link.
	tags := adds("link", aID, "links", link(aID, LinkDelivery, "#7", "https://example.com/pull/7"))
	ls, err := s.Links(ctx, a)
	if err != nil || len(ls) != 1 {
		t.Fatalf("the links: %d %v", len(ls), err)
	}
	rc, err := s.RemoveLink(ctx, testOp("x-link"), a, ls[0].ID)
	want(t, rc, err, StatusDone, "")
	removedWhole("x-link", "links", 0, tags)
	if n := count(t, s, "SELECT COUNT(*) FROM links WHERE ticket_id = ? AND deleted_at = ''", aID); n != 0 {
		t.Fatalf("%d live rows of the link", n)
	}
	// A relation.
	tags = adds("rela", aID, "relations", func(x *sql.Tx, actor string, when time.Time) string {
		rel := Relation{ID: newID(), FromID: aID, ToID: bID, Kind: RelationBlocks, CreatedAt: when, CreatedBy: actor}
		if _, err := x.Exec("INSERT INTO relations (id, from_id, to_id, kind, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)",
			rel.ID, rel.FromID, rel.ToID, rel.Kind, stamp(when), actor); err != nil {
			t.Fatal(err)
		}
		value, err := encodeValue(rel)
		if err != nil {
			t.Fatal(err)
		}
		return value
	})
	rs, err := s.Relations(ctx, a)
	if err != nil || len(rs) != 1 {
		t.Fatalf("the relations: %d %v", len(rs), err)
	}
	rc, err = s.RemoveRelation(ctx, testOp("x-rel"), a, rs[0].ID)
	want(t, rc, err, StatusDone, "")
	removedWhole("x-rel", "relations", 0, tags)
	if n := count(t, s, "SELECT COUNT(*) FROM relations WHERE deleted_at = ''"); n != 0 {
		t.Fatalf("%d live rows of the relation", n)
	}
	// An assignee: one membership row however many adds.
	exec(t, s, "INSERT INTO ticket_assignees (ticket_id, actor, added_at) VALUES (?, 'person:Alice', ?)", aID, stamp(at))
	tags = adds("asgn", aID, "assignees", func(*sql.Tx, string, time.Time) string { return "person:Alice" })
	rc, err = edit(s, "x-asgn", a, TicketEdit{Remove: TicketMembers{Assignees: []string{"person:Alice"}}})
	want(t, rc, err, StatusDone, "")
	removedWhole("x-asgn", "assignees", 0, tags)
	if as := ticketOf(t, s, a).Assignees; len(as) != 0 {
		t.Fatalf("the assignees: %v", as)
	}
	// A run link the undelete releases, a live ticket having taken the run
	// while its ticket was deleted: the undelete's own row, then the
	// remove's.
	tags = adds("runl", rID, "links", link(rID, LinkRun, "run1", ""))
	rc, err = s.DeleteTicket(ctx, testOp("del-r"), r)
	want(t, rc, err, StatusDone, "")
	rc, err = addLink(s, "l-live", live, NewLink{Kind: LinkRun, Ref: "run1"})
	want(t, rc, err, StatusDone, "")
	rc, err = s.UndeleteTicket(ctx, testOp("undel-r"), r)
	want(t, rc, err, StatusDone, "")
	if rows := changesOf(t, s, "test", "undel-r"); rows[0].Op != OpSet || rows[0].Field != "deleted_at" {
		t.Fatalf("the undelete's first row: %s:%s", rows[0].Op, rows[0].Field)
	}
	removedWhole("undel-r", "links", 1, tags)
	if n := count(t, s, "SELECT COUNT(*) FROM links WHERE ticket_id = ? AND deleted_at = ''", rID); n != 0 {
		t.Fatalf("%d live rows of the released link", n)
	}
}

// A second person.add of a held name is dropped: the name is the person,
// so two replicas adding one name add one row. Each replica holds its own
// create and drops the other's, in either order, and the directory reads
// one name; the row is never written twice (people.name is the key a
// second insert would trip, which is why the merge must drop it).
func TestASecondPersonAddOfAHeldNameIsDropped(t *testing.T) {
	ctx := context.Background()
	stores := map[string]*Store{}
	creates := map[string]ChangeRow{}
	for _, actor := range []string{"replica-a", "replica-b"} {
		s := openStore(t, storeDir(t))
		rc, err := s.AddPerson(ctx, Op{Actor: actor, ID: "p1"}, "Alice")
		want(t, rc, err, StatusDone, "")
		stores[actor] = s
		creates[actor] = changesOf(t, s, actor, "p1")[0]
	}
	for local, other := range map[string]string{"replica-a": "replica-b", "replica-b": "replica-a"} {
		s := stores[local]
		if got := Merge(creates[local], creates[other]); got != KeepLocal {
			t.Fatalf("%s receiving %s's create: %v", local, other, got)
		}
		if _, err := s.db.Exec("INSERT INTO people (name, created_at) VALUES ('Alice', ?)", stamp(creates[other].At)); err == nil {
			t.Fatalf("%s: a second row of a held name was written", local)
		}
		people, err := s.People(ctx)
		if err != nil || len(people) != 1 || people[0].Name != "Alice" {
			t.Fatalf("%s: the directory %+v %v", local, people, err)
		}
		// A local add of it is refused, as any held name is.
		rc, err := s.AddPerson(ctx, testOp("p2"), "Alice")
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
}

// A membership of a tombstoned label reads absent: a label deleted on one
// replica while another added it to a ticket (the delete's cascade did
// not see the add) leaves the membership row, read as absent in both
// orders and counting for nothing toward the ticket's bound.
func TestAMembershipOfATombstonedLabelReadsAbsent(t *testing.T) {
	ctx := context.Background()
	b := replica{"replica-b", time.Now().UTC()}
	for _, order := range []string{"added here, deleted there", "deleted here, added there"} {
		s := openStore(t, storeDir(t))
		pid := newProject(t, s, "CND")
		label := newLabel(t, s, "CND", "api")
		keep := newLabel(t, s, "CND", "ui")
		tid, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Labelled", Labels: []string{keep}})
		switch order {
		case "added here, deleted there":
			rc, err := edit(s, "add", key, TicketEdit{Add: TicketMembers{Labels: []string{label}}})
			want(t, rc, err, StatusDone, "")
			exec(t, s, "UPDATE labels SET deleted_at = ? WHERE id = ?", stamp(b.at), label)
			b.change(t, s, "del", 0, ChangeRow{Entity: EntityLabel, EntityID: label, ProjectID: pid, Field: "deleted_at", Op: OpSet, Value: stamp(b.at)})
		case "deleted here, added there":
			rc, err := s.DeleteLabel(ctx, testOp("del"), "CND", label)
			want(t, rc, err, StatusDone, "")
			exec(t, s, "INSERT INTO ticket_labels (ticket_id, label_id) VALUES (?, ?)", tid, label)
			b.change(t, s, "add", 0, ChangeRow{Entity: EntityTicket, EntityID: tid, TicketID: tid, ProjectID: pid, Field: "labels", Op: OpAdd, Value: label})
		}
		if n := count(t, s, "SELECT COUNT(*) FROM ticket_labels WHERE ticket_id = ? AND label_id = ?", tid, label); n != 1 {
			t.Fatalf("%s: the membership row is not there to read", order)
		}
		tk, err := readTicket(ctx, s.db, key)
		if err != nil || !slices.Equal(tk.Labels, []string{keep}) {
			t.Fatalf("%s: the ticket's labels %v %v", order, tk.Labels, err)
		}
		if labels, err := s.Labels(ctx, "CND"); err != nil || len(labels) != 1 || labels[0].ID != keep {
			t.Fatalf("%s: the project's labels %+v %v", order, labels, err)
		}
	}
}

// A milestone_id set on one replica while the milestone was deleted on
// another (the delete's clears are scalar sets of the tickets it saw, and
// it did not see this one) names the tombstone and reads as none, in
// both orders.
func TestAMilestoneSetWhileDeletedElsewhereReadsAsNone(t *testing.T) {
	ctx := context.Background()
	b := replica{"replica-b", time.Now().UTC()}
	for _, order := range []string{"set here, deleted there", "deleted here, set there"} {
		s := openStore(t, storeDir(t))
		pid := newProject(t, s, "CND")
		m := newMilestone(t, s, "CND", "1.0")
		tid, key := newTicket(t, s, "CND", "Planned")
		switch order {
		case "set here, deleted there":
			rc, err := edit(s, "set", key, TicketEdit{Set: TicketSet{Milestone: ptr(m)}})
			want(t, rc, err, StatusDone, "")
			exec(t, s, "UPDATE milestones SET deleted_at = ? WHERE id = ?", stamp(b.at), m)
			b.change(t, s, "del", 0, ChangeRow{Entity: EntityMilestone, EntityID: m, ProjectID: pid, Field: "deleted_at", Op: OpSet, Value: stamp(b.at)})
		case "deleted here, set there":
			rc, err := s.DeleteMilestone(ctx, testOp("del"), "CND", m)
			want(t, rc, err, StatusDone, "")
			exec(t, s, "UPDATE tickets SET milestone_id = ? WHERE id = ?", m, tid)
			b.change(t, s, "set", 0, ChangeRow{Entity: EntityTicket, EntityID: tid, TicketID: tid, ProjectID: pid, Field: "milestone_id", Op: OpSet, Value: m})
		}
		if raw := ticketOf(t, s, key).MilestoneID; raw != m {
			t.Fatalf("%s: the row names %q, not the tombstone", order, raw)
		}
		tk, err := readTicket(ctx, s.db, key)
		if err != nil || tk.MilestoneID != "" {
			t.Fatalf("%s: the milestone read %q %v", order, tk.MilestoneID, err)
		}
	}
}

// A relation added on one replica to a ticket deleted on another is live
// and inactive in both orders: kept, read with its other end deleted,
// blocking nothing; and active again after the undelete.
func TestARelationToATicketDeletedElsewhereIsInactiveUntilItsUndelete(t *testing.T) {
	ctx := context.Background()
	b := replica{"replica-b", time.Now().UTC()}
	for _, order := range []string{"related here, deleted there", "deleted here, related there"} {
		s := openStore(t, storeDir(t))
		pid := newProject(t, s, "CND")
		_, x := newTicket(t, s, "CND", "Blocked")
		xID := ticketOf(t, s, x).ID
		yID, y := newTicket(t, s, "CND", "Blocker")
		switch order {
		case "related here, deleted there":
			rc, err := relate(s, "rel", y, RelationBlocks, x)
			want(t, rc, err, StatusDone, "")
			exec(t, s, "UPDATE tickets SET deleted_at = ? WHERE id = ?", stamp(b.at), yID)
			b.change(t, s, "del", 0, ChangeRow{Entity: EntityTicket, EntityID: yID, TicketID: yID, ProjectID: pid, Field: "deleted_at", Op: OpSet, Value: stamp(b.at)})
		case "deleted here, related there":
			rc, err := s.DeleteTicket(ctx, testOp("del"), y)
			want(t, rc, err, StatusDone, "")
			b.relation(t, s, "rel", pid, yID, xID, RelationBlocks)
		}
		if n := count(t, s, "SELECT COUNT(*) FROM relations WHERE from_id = ? AND to_id = ? AND deleted_at = ''", yID, xID); n != 1 {
			t.Fatalf("%s: the relation is not live", order)
		}
		rs, err := s.Relations(ctx, x)
		if err != nil || len(rs) != 1 || rs[0].Other.ID != yID || rs[0].Other.DeletedAt.IsZero() {
			t.Fatalf("%s: the relations %+v %v", order, rs, err)
		}
		if blocked, err := s.Blocked(ctx, x); err != nil || blocked {
			t.Fatalf("%s: blocked by a tombstone: %v %v", order, blocked, err)
		}
		rc, err := s.UndeleteTicket(ctx, testOp("undel"), y)
		want(t, rc, err, StatusDone, "")
		rs, err = s.Relations(ctx, x)
		if err != nil || len(rs) != 1 || !rs[0].Other.DeletedAt.IsZero() {
			t.Fatalf("%s: after the undelete %+v %v", order, rs, err)
		}
		if blocked, err := s.Blocked(ctx, x); err != nil || !blocked {
			t.Fatalf("%s: not blocked after the undelete: %v %v", order, blocked, err)
		}
	}
}

// Every field version 2 adds has its class (docs/plans/dispatch-evidence.md,
// "Field rules for replication"): every column of the plans, launches and
// launch_files tables but a row's rev, which a replica computes, and a
// link's three new columns, each read off the schema so a column added
// later without a class fails here.
func TestEveryNewFieldHasItsClass(t *testing.T) {
	s := openStore(t, storeDir(t))
	want := map[string]map[string]FieldClass{
		EntityPlan: {
			"id": ClassImmutable, "ticket_id": ClassImmutable, "based_on": ClassImmutable, "crew_id": ClassImmutable, "crew_name": ClassImmutable,
			"crew_updated_at": ClassImmutable, "base": ClassImmutable, "body": ClassImmutable, "digest": ClassImmutable, "bytes": ClassImmutable,
			"created_at": ClassImmutable, "created_by": ClassImmutable, "fingerprint": ClassImmutable,
			"state": ClassGated, "approved_at": ClassScalar, "approved_by": ClassScalar, "approved_note": ClassScalar, "approved_rev": ClassScalar,
			"approved_intent": ClassScalar, "withdrawn_at": ClassScalar, "withdrawn_by": ClassScalar,
		},
		EntityLaunch: {
			"id": ClassImmutable, "ticket_id": ClassImmutable, "plan_id": ClassImmutable, "run_id": ClassImmutable, "label": ClassImmutable,
			"ticket_rev": ClassImmutable, "intent": ClassImmutable, "criteria": ClassImmutable, "package": ClassImmutable,
			"package_sha256": ClassImmutable, "created_at": ClassImmutable, "created_by": ClassImmutable, "fingerprint": ClassImmutable,
			"state": ClassScalar, "why": ClassScalar, "agents": ClassScalar, "started_at": ClassScalar,
		},
		"launch_files": {"launch_id": ClassImmutable, "sha256": ClassImmutable},
	}
	tableOf := map[string]string{EntityPlan: "plans", EntityLaunch: "launches", "launch_files": "launch_files"}
	for entity, classes := range want {
		cols := columnsOf(t, s.db, tableOf[entity])
		classed := 0
		for _, col := range cols {
			if col == "rev" {
				if got := Class(entity, col); got != "" {
					t.Errorf("%s.rev has the class %q: a rev is a replica's own count", entity, got)
				}
				continue
			}
			c, ok := classes[col]
			if !ok {
				t.Errorf("%s.%s is a column the test does not know", entity, col)
				continue
			}
			if got := Class(entity, col); got != c {
				t.Errorf("Class(%s, %s) = %q, want %q", entity, col, got, c)
			}
			classed++
		}
		if classed != len(classes) {
			t.Errorf("%s: %d columns with a class, %d classes the test names", entity, classed, len(classes))
		}
	}
	for _, col := range []string{"fingerprint", "candidate", "verified"} {
		if !slices.Contains(columnsOf(t, s.db, "links"), col) {
			t.Errorf("links has no %s", col)
		}
		if got := Class("link", col); got != ClassImmutable {
			t.Errorf("Class(link, %s) = %q, want immutable", col, got)
		}
	}
	// A link's set is still its ticket's, merged as one.
	if Class(EntityTicket, "links") != ClassSet {
		t.Error("a ticket's links are no set")
	}
	// A revision's state merges as a scalar, the approval's rule never
	// applied again: the later wins.
	t1 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if got := Merge(row(EntityPlan, "state", OpSet, t1, "a", "x", 0), row(EntityPlan, "state", OpSet, t1.Add(time.Second), "b", "y", 0)); got != TakeRemote {
		t.Errorf("a revision's later state: %v", got)
	}
	if got := Merge(row(EntityPlan, "body", OpSet, t1, "a", "x", 0), row(EntityPlan, "body", OpSet, t1.Add(time.Second), "b", "y", 0)); got != KeepLocal {
		t.Errorf("a revision's body written twice: %v", got)
	}
}

// Two revisions of one ticket approved on two replicas are kept, both
// standing: each is an entity of its own, so a merge of the two
// approvals' rows touches neither the other's, and the ticket's read
// shows both, newest first, until a person withdraws one.
func TestTwoApprovedRevisionsOfOneTicketAreKept(t *testing.T) {
	s, _, key := planTicket(t)
	r1 := propose(t, s, "p1", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	r2 := propose(t, s, "p2", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	approve(t, s, "a1", r1, "")
	// The second approved elsewhere, its rows as a merge writes them:
	// the first's supersession was never seen there.
	tk := ticketOf(t, s, key)
	if _, err := s.db.Exec("UPDATE plans SET state = ?, approved_at = ?, approved_by = 'elsewhere', approved_rev = ?, approved_intent = ? WHERE id = ?",
		PlanApproved, stamp(time.Now()), tk.Rev, IntentDigest(tk), r2); err != nil {
		t.Fatal(err)
	}
	ids, stale := approvedOf(t, s, key)
	if !slices.Equal(ids, []string{r2, r1}) || stale[0] != nil || stale[1] != nil {
		t.Fatalf("the ticket's approved revisions after the merge: %v %v", ids, stale)
	}
	// No reconcile rule undoes either: a person withdraws the other.
	rc, err := s.WithdrawPlan(context.Background(), testOp("w2"), r2)
	want(t, rc, err, StatusDone, "")
	if ids, _ := approvedOf(t, s, key); !slices.Equal(ids, []string{r1}) {
		t.Fatalf("after the other's withdrawal: %v", ids)
	}
}

// However many approved revisions merges leave (up to MaxPlans), a
// ticket's read carries the newest MaxTicketApproved, newest first, so it
// keeps its bound; the revisions' own read has every one.
func TestATicketReadCarriesTheNewestApprovedRevisions(t *testing.T) {
	s, _, key := planTicket(t)
	for i := range MaxTicketApproved + 2 {
		propose(t, s, opID("p", i), key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	}
	tk := ticketOf(t, s, key)
	// Every revision approved, as merges of as many replicas leave them.
	if _, err := s.db.Exec("UPDATE plans SET state = ?, approved_at = ?, approved_by = 'elsewhere', approved_rev = ?, approved_intent = ?",
		PlanApproved, stamp(time.Now()), tk.Rev, IntentDigest(tk)); err != nil {
		t.Fatal(err)
	}
	all, err := s.Plans(context.Background(), key)
	if err != nil || len(all) != MaxTicketApproved+2 {
		t.Fatalf("the revisions' read: %d %v", len(all), err)
	}
	var newest []string
	for _, p := range all[:MaxTicketApproved] {
		if p.State != PlanApproved {
			t.Fatalf("revision %d is %s", p.N, p.State)
		}
		newest = append(newest, p.ID)
	}
	if ids, _ := approvedOf(t, s, key); !slices.Equal(ids, newest) {
		t.Fatalf("the ticket's approved revisions: %v, want the newest %d %v", ids, MaxTicketApproved, newest)
	}
}
