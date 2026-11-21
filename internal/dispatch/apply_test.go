package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// replicaPair is two stores of one shared workspace: a, which made the
// project, and b, which receives it.
func replicaPair(t *testing.T) (a, b *Store) {
	t.Helper()
	a, b = openStore(t, storeDir(t)), openStore(t, storeDir(t))
	join(t, a, "a1")
	join(t, b, "b1")
	return a, b
}

// shareAll marks every project of s shared in the workspace w1, as a
// share's cut-over leaves it (OS-1b's), so its rows may leave.
func shareAll(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec("UPDATE projects SET shared = 'w1', share_state = 'shared', registry_key = key"); err != nil {
		t.Fatal(err)
	}
}

// pull hands every row from has after its position *after to to, page
// by page as a provider would (the views each plan's create names, the
// projects' registry keys), and moves the position; it fails the test on
// a refused batch and returns what the last batch held.
func pull(t *testing.T, from, to *Store, after *int64) RemoteApplied {
	t.Helper()
	ctx := context.Background()
	var last RemoteApplied
	for {
		rows, next, err := from.ChangesFrom(ctx, *after, 50)
		if err != nil {
			t.Fatal(err)
		}
		if next == *after {
			return last
		}
		*after = next
		if len(rows) == 0 {
			continue
		}
		views := map[string][]byte{}
		for _, c := range rows {
			if c.ViewDigest != "" {
				v, err := from.RevisionView(ctx, c.ViewDigest)
				if err != nil {
					t.Fatal(err)
				}
				views[c.ViewDigest] = v
			}
		}
		keys := registryKeys(t, from)
		if last, err = to.ApplyRemote(ctx, rows, views, keys); err != nil {
			t.Fatalf("applying %d rows: %v", len(rows), err)
		}
	}
}

// registryKeys is each shared project's registry key, by its id.
func registryKeys(t *testing.T, s *Store) map[string]string {
	t.Helper()
	ids := texts(t, s, "SELECT id FROM projects WHERE shared != '' ORDER BY id")
	keys := texts(t, s, "SELECT CASE WHEN registry_key != '' THEN registry_key ELSE key END FROM projects WHERE shared != '' ORDER BY id")
	out := map[string]string{}
	for i := range ids {
		out[ids[i]] = keys[i]
	}
	return out
}

// shared is the state two replicas must agree on: every row of the
// tables a shared project's rows make, the columns each replica keeps
// for itself (its revs, its times of change, its numbers' allocation,
// its share, its working directory, a revision's body) left out.
func sharedState(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, q := range []string{
		"SELECT 'project', id, key, name, description, tracker, tracker_url, close_requires, archived_at, created_at FROM projects ORDER BY id",
		`SELECT 'ticket', id, project_id, number, key, title, description, acceptance, status, priority, horizon, owner, parent_id, milestone_id,
			closed_at, deleted_at, created_at, created_by, external_id, external_url, authority FROM tickets ORDER BY id`,
		"SELECT 'assignee', ticket_id, actor FROM ticket_assignees ORDER BY ticket_id, actor",
		"SELECT 'label', id, project_id, name, color, deleted_at FROM labels ORDER BY id",
		"SELECT 'ticket_label', ticket_id, label_id FROM ticket_labels ORDER BY ticket_id, label_id",
		"SELECT 'milestone', id, project_id, name, description, target, state, deleted_at FROM milestones ORDER BY id",
		"SELECT 'relation', id, from_id, to_id, kind, deleted_at != '' FROM relations ORDER BY id",
		"SELECT 'comment', id, ticket_id, body, edited_at, deleted_at FROM comments ORDER BY id",
		"SELECT 'link', id, ticket_id, kind, ref, url, note, deleted_at != '' FROM links ORDER BY id",
		"SELECT 'person', name FROM people ORDER BY name",
		"SELECT 'plan', id, ticket_id, digest, state, approved_by, approved_rev, withdrawn_by, view_digest FROM plans ORDER BY id",
	} {
		rows, err := s.db.Query(q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprint(vals...))
		}
		rows.Close()
	}
	return out
}

// sameState fails the test unless the two replicas hold one state.
func sameState(t *testing.T, a, b *Store) {
	t.Helper()
	sa, sb := sharedState(t, a), sharedState(t, b)
	if !slices.Equal(sa, sb) {
		for i := range max(len(sa), len(sb)) {
			var x, y string
			if i < len(sa) {
				x = sa[i]
			}
			if i < len(sb) {
				y = sb[i]
			}
			if x != y {
				t.Fatalf("the replicas differ at %d:\n a %s\n b %s", i, x, y)
			}
		}
	}
}

// writeMuch writes on s, in the project with the key, a ticket of each
// shape a person makes: fields set, members added and removed, a
// comment edited and one deleted, a relation added and removed, a link,
// a milestone, a person, a plan revision approved; and returns the key
// of the first ticket.
func writeMuch(t *testing.T, s *Store, prefix, project string) string {
	t.Helper()
	ctx := context.Background()
	op := func(id string) Op { return testOp(prefix + "-" + id) }
	_, k1 := makeTicket(t, s, NewTicket{Project: project, Title: prefix + " one", Description: "First.", Acceptance: "- done"})
	_, k2 := makeTicket(t, s, NewTicket{Project: project, Title: prefix + " two", Priority: 2})
	l := mustLabel(t, s, prefix+"-label", project, prefix+"-bug")
	title, status := prefix+" one, again", TicketReady
	for i, e := range []TicketEdit{
		{Set: TicketSet{Title: &title, Status: &status}},
		{Add: TicketMembers{Labels: []string{l}, Assignees: []string{"agent:dev"}}},
		{Remove: TicketMembers{Assignees: []string{"agent:dev"}}},
	} {
		mustEdit(t, s, fmt.Sprintf("%s-edit-%d", prefix, i), k1, e)
	}
	c1 := comment(t, s, prefix+"-c1", k1, "A comment.")
	c2 := comment(t, s, prefix+"-c2", k1, "Another.")
	if rc, err := s.EditComment(ctx, op("c1-edit"), k1, c1, "A comment, edited."); err != nil || rc.Status != StatusDone {
		t.Fatalf("edit comment %+v %v", rc, err)
	}
	if rc, err := s.DeleteComment(ctx, op("c2-delete"), k1, c2); err != nil || rc.Status != StatusDone {
		t.Fatalf("delete comment %+v %v", rc, err)
	}
	rc, err := s.AddRelation(ctx, op("rel"), k1, RelationBlocks, k2)
	want(t, rc, err, StatusDone, "")
	rc, err = s.AddRelation(ctx, op("rel2"), k2, RelationRelates, k1)
	want(t, rc, err, StatusDone, "")
	if rc, err := s.RemoveRelation(ctx, op("rel2-off"), k2, rc.ID); err != nil || rc.Status != StatusDone {
		t.Fatalf("remove relation %+v %v", rc, err)
	}
	rc, err = s.AddLink(ctx, op("link"), k2, NewLink{Kind: LinkExternal, URL: "https://example.com/" + prefix}, nil)
	want(t, rc, err, StatusDone, "")
	rc, err = s.CreateMilestone(ctx, op("ms"), project, NewMilestone{Name: prefix + " ms"})
	want(t, rc, err, StatusDone, "")
	rc, err = s.AddPerson(ctx, op("person"), prefix+"-person")
	want(t, rc, err, StatusDone, "")
	return k1
}

// A shared project's whole history applied on a replica that held none
// of it: the project received as shared under its registry key, every
// entity and every member as the maker holds them, each row kept once
// with its actor qualified by its origin and its origin; applied again,
// nothing changes.
func TestApplyRemoteProjectsByClass(t *testing.T) {
	a, b := replicaPair(t)
	newProject(t, a, "CND")
	shareAll(t, a)
	key := writeMuch(t, a, "x", "CND")
	propose(t, a, "plan", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	var after int64
	pull(t, a, b, &after)
	sameState(t, a, b)
	if n := count(t, b, "SELECT COUNT(*) FROM projects WHERE share_state = 'shared' AND shared = 'w1' AND registry_key = 'CND'"); n != 1 {
		t.Fatal("the project not received as shared")
	}
	if n := count(t, b, "SELECT COUNT(*) FROM changes WHERE remote = 1 AND (origin != 'a1' OR actor NOT LIKE '@a1/%')"); n != 0 {
		t.Fatalf("%d rows without their origin's qualification", n)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM plans WHERE body_here = 0 AND body = '' AND view_digest IN (SELECT view_digest FROM plan_views)"); n != 1 {
		t.Fatal("the revision not kept as its view")
	}
	rows := count(t, b, "SELECT COUNT(*) FROM changes")
	after = 0
	pull(t, a, b, &after)
	if n := count(t, b, "SELECT COUNT(*) FROM changes"); n != rows {
		t.Fatalf("applied again: %d rows, were %d", n, rows)
	}
	sameState(t, a, b)
}

// Rows that never leave their machine, a row with no origin or a
// qualified actor, a store that joined nothing and a project of this
// machine's that is not shared refuse the batch whole: nothing applied.
func TestApplyRemoteRefusesWhatDoesNotReplicate(t *testing.T) {
	ctx := context.Background()
	a, b := replicaPair(t)
	newProject(t, a, "CND")
	shareAll(t, a)
	_, key := newTicket(t, a, "CND", "One")
	rows, _, err := a.ChangesFrom(ctx, 0, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	keys := registryKeys(t, a)
	base := rows[0]
	for _, bad := range []struct {
		name string
		row  ChangeRow
		err  error
	}{
		{"a launch", ChangeRow{Entity: EntityLaunch, EntityID: "l", Op: OpCreate, Actor: "admin", OpID: "x", Origin: "a1"}, ErrNotReplicated},
		{"a review", ChangeRow{Entity: EntityReview, EntityID: "r", Op: OpCreate, Actor: "admin", OpID: "x", Origin: "a1"}, ErrNotReplicated},
		{"a capture", ChangeRow{Entity: EntityCapture, EntityID: "c", Op: OpCreate, Actor: "admin", OpID: "x", Origin: "a1"}, ErrNotReplicated},
		{"a publication", ChangeRow{Entity: EntityPublication, EntityID: "p", Op: OpCreate, Actor: "admin", OpID: "x", Origin: "a1"}, ErrNotReplicated},
		{"a project's cwd", ChangeRow{Entity: EntityProject, EntityID: base.EntityID, ProjectID: base.EntityID, Field: "cwd", Op: OpSet, Value: "/x", Actor: "admin", OpID: "x", Origin: "a1"}, ErrNotReplicated},
		{"a snapshot", ChangeRow{Entity: EntityTicket, EntityID: "t", Field: "external_snapshot", Op: OpSet, Actor: "admin", OpID: "x", Origin: "a1"}, ErrNotReplicated},
		{"an attachment", ChangeRow{Entity: EntityTicket, EntityID: "t", Field: "attachments", Op: OpAdd, Actor: "admin", OpID: "x", Origin: "a1"}, ErrNotReplicated},
		{"no origin", ChangeRow{Entity: EntityPerson, EntityID: "n", Op: OpCreate, Actor: "admin", OpID: "x"}, ErrBadActor},
		{"a qualified actor", ChangeRow{Entity: EntityPerson, EntityID: "n", Op: OpCreate, Actor: "@a1/admin", OpID: "x", Origin: "a1"}, ErrBadActor},
	} {
		batch := append(slices.Clone(rows), bad.row)
		if _, err := b.ApplyRemote(ctx, batch, nil, keys); !errors.Is(err, bad.err) {
			t.Errorf("%s: %v, want %v", bad.name, err, bad.err)
		}
		if n := count(t, b, "SELECT COUNT(*) FROM changes"); n != 0 {
			t.Fatalf("%s: %d rows applied", bad.name, n)
		}
	}
	// A project b holds that it has not shared.
	if _, err := b.db.Exec("INSERT INTO projects (id, key, name, created_at, updated_at) VALUES (?, 'OWN', 'own', '', '')", base.EntityID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyRemote(ctx, rows, nil, keys); !errors.Is(err, ErrNotShared) {
		t.Fatalf("a row of a project not shared here: %v", err)
	}
	// A store that joined nothing.
	c := openStore(t, storeDir(t))
	if _, err := c.ApplyRemote(ctx, rows, nil, keys); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("a store that joined nothing: %v", err)
	}
	_ = key
}

// A comment before its ticket, a label's add before its label and a
// relation before its far end are held, each with its words, and applied
// once what they wait for arrives, in whatever batch; a row of a project
// not here is held until the project's first change.
func TestChildBeforeParentHeldThenApplied(t *testing.T) {
	ctx := context.Background()
	a, b := replicaPair(t)
	newProject(t, a, "CND")
	shareAll(t, a)
	key := writeMuch(t, a, "x", "CND")
	rows, _, err := a.ChangesFrom(ctx, 0, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	keys := registryKeys(t, a)
	// The project's rows and the first ticket's first, so its label's add
	// and its relation come before their label and their far end; then
	// every other row, newest first; one row at a time.
	first := ticketOf(t, a, key).ID
	var order []ChangeRow
	for _, c := range rows {
		if c.Entity == EntityProject || c.EntityID == first {
			order = append(order, c)
		}
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if c := rows[i]; c.Entity != EntityProject && c.EntityID != first {
			order = append(order, c)
		}
	}
	var held int
	var waitedForARef bool
	for i := range order {
		got, err := b.ApplyRemote(ctx, order[i:i+1], nil, keys)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		for _, w := range got.Held {
			// A label's add held for its label, a relation's for its far
			// end: a reference not yet here.
			waitedForARef = waitedForARef || strings.Contains(w, "waits for label") || strings.Contains(w, "waits for ticket")
		}
		if len(got.Held) > 0 {
			held++
			for _, w := range got.Held {
				if len(w) > MaxHeldWord || !strings.Contains(w, "held") && !strings.Contains(w, "numbered") {
					t.Fatalf("a held row's words: %q", w)
				}
			}
		}
	}
	if held == 0 || !waitedForARef {
		t.Fatalf("held %d times, a reference waited for %v: the batch's order made no child come before its parent", held, waitedForARef)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM held_rows"); n != 0 {
		t.Fatalf("%d rows still held", n)
	}
	sameState(t, a, b)
}

// Held rows are kept in the database: a store closed with rows held and
// opened again applies them once what they wait for arrives.
func TestHeldRowsSurviveARestart(t *testing.T) {
	ctx := context.Background()
	a := openStore(t, storeDir(t))
	join(t, a, "a1")
	dir := storeDir(t)
	b := openStore(t, dir)
	join(t, b, "b1")
	newProject(t, a, "CND")
	shareAll(t, a)
	writeMuch(t, a, "x", "CND")
	rows, _, err := a.ChangesFrom(ctx, 0, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	keys := registryKeys(t, a)
	if _, err := b.ApplyRemote(ctx, rows[1:], nil, keys); err != nil {
		t.Fatal(err)
	}
	held := count(t, b, "SELECT COUNT(*) FROM held_rows")
	if held == 0 {
		t.Fatal("nothing held without the project's create")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b = openStore(t, dir)
	if n := count(t, b, "SELECT COUNT(*) FROM held_rows"); n != held {
		t.Fatalf("%d held after a restart, were %d", n, held)
	}
	if _, err := b.ApplyRemote(ctx, rows[:1], nil, keys); err != nil {
		t.Fatal(err)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM held_rows"); n != 0 {
		t.Fatalf("%d still held", n)
	}
	sameState(t, a, b)
}

// Past MaxHeldRows held, a batch that would hold more is refused whole.
func TestHeldRowsBounded(t *testing.T) {
	ctx := context.Background()
	_, b := replicaPair(t)
	for i := range MaxHeldRows {
		if _, err := b.db.Exec("INSERT INTO held_rows (project_key, why, waits_for, row, held_at) VALUES ('X', 'w', 'project:p', ?, '')", fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	orphan := ChangeRow{Entity: EntityTicket, EntityID: "t", ProjectID: "p", Field: "title", Op: OpSet, Value: "x", Actor: "admin", OpID: "o", Origin: "a1"}
	if _, err := b.ApplyRemote(ctx, []ChangeRow{orphan}, nil, nil); !errors.Is(err, ErrHeldFull) {
		t.Fatalf("a held row past the bound: %v", err)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM held_rows"); n != MaxHeldRows {
		t.Fatalf("%d held", n)
	}
}

// A project whose registry key a project of this machine's own holds is
// held, every row of it, with words that say to give the local one
// another key; once the local project gives it up, the next batch
// receives it.
func TestIncomingKeyHeldByALocalProjectIsHeld(t *testing.T) {
	ctx := context.Background()
	a, b := replicaPair(t)
	newProject(t, a, "CND")
	shareAll(t, a)
	newTicket(t, a, "CND", "Theirs")
	newProject(t, b, "CND")
	var after int64
	got := pull(t, a, b, &after)
	if len(got.Held) == 0 || !strings.Contains(got.Held[0], "CND is this machine's own project") {
		t.Fatalf("the held words %v", got.Held)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM projects"); n != 1 {
		t.Fatalf("%d projects", n)
	}
	if _, err := b.db.Exec("UPDATE projects SET key = 'OWN'"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyRemote(ctx, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM tickets t JOIN projects p ON p.id = t.project_id WHERE p.key = 'CND' AND t.title = 'Theirs'"); n != 1 {
		t.Fatal("the project not received once its key was free")
	}
}

// Two replicas numbering one project's ticket alike: the second create is
// held with its words, never failed, never renumbered.
func TestDuplicateNumberHeldNotFailed(t *testing.T) {
	ctx := context.Background()
	a, b := replicaPair(t)
	newProject(t, a, "CND")
	shareAll(t, a)
	var after int64
	pull(t, a, b, &after)
	if _, err := a.db.Exec("UPDATE projects SET number_from = 1, number_to = 100, next_number = 1, block_tag = 'a1'"); err != nil {
		t.Fatal(err)
	}
	newTicket(t, a, "CND", "A's first")
	// b numbers its first ticket 1 too, as a store with a block from 1
	// would.
	if _, err := b.db.Exec("UPDATE projects SET number_from = 1, number_to = 100, next_number = 1, block_tag = 'b1'"); err != nil {
		t.Fatal(err)
	}
	newTicket(t, b, "CND", "B's first")
	got := pull(t, a, b, &after)
	if len(got.Held) == 0 || !strings.Contains(got.Held[0], "CND-1 was numbered twice") {
		t.Fatalf("the held words %v", got.Held)
	}
	if got := texts(t, b, "SELECT title FROM tickets WHERE key = 'CND-1'"); !slices.Equal(got, []string{"B's first"}) {
		t.Fatalf("CND-1 on b: %v", got)
	}
	_ = ctx
}

// A row this store wrote, handed back by another replica in a recovery,
// is the store's own: never applied twice; another origin's row sent by a
// third replica keeps that replica as recovered_by.
func TestRecoveredByKept(t *testing.T) {
	ctx := context.Background()
	a, b := replicaPair(t)
	newProject(t, a, "CND")
	shareAll(t, a)
	newTicket(t, a, "CND", "One")
	rows, _, err := a.ChangesFrom(ctx, 0, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		rows[i].RecoveredBy = "c3"
	}
	if _, err := b.ApplyRemote(ctx, rows, nil, registryKeys(t, a)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM changes WHERE recovered_by = 'c3' AND origin = 'a1'"); n != len(rows) {
		t.Fatalf("%d rows kept their recovery, of %d", n, len(rows))
	}
	back, _, err := b.ChangesFrom(ctx, 0, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	before := count(t, a, "SELECT COUNT(*) FROM changes")
	got, err := a.ApplyRemote(ctx, back, nil, registryKeys(t, a))
	if err != nil || got.Applied != 0 {
		t.Fatalf("a's own rows handed back: %+v %v", got, err)
	}
	if n := count(t, a, "SELECT COUNT(*) FROM changes"); n != before {
		t.Fatalf("%d rows on a, were %d", n, before)
	}
}

// A row that comes in naming one of this store's own origin tags, and that
// is not one of this store's own rows handed back, is refused, the refusal
// naming the tag, and nothing of its batch is kept: a row from elsewhere
// is never stored as this machine's own, its approval included.
func TestARowNamingThisStoresOwnOriginIsRefused(t *testing.T) {
	ctx := context.Background()
	a, b, key, after := planPair(t)
	id := propose(t, a, "propose-on-a", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	pull(t, a, b, after)
	approve(t, a, "approve-on-a", id, "a's")
	rows, _, err := a.ChangesFrom(ctx, *after, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		rows[i].Origin = "b1"
	}
	// b's own rows handed back with them are dropped; the rows naming b
	// that b never wrote refuse the batch.
	own, _, err := b.ChangesFrom(ctx, 0, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range own {
		if !c.Remote {
			rows = append(rows, c)
		}
	}
	before := count(t, b, "SELECT COUNT(*) FROM changes")
	got, err := b.ApplyRemote(ctx, rows, nil, registryKeys(t, a))
	if !errors.Is(err, ErrBadActor) || !strings.Contains(err.Error(), "b1") || got.Applied != 0 {
		t.Fatalf("rows naming b's own origin: %+v %v", got, err)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM changes"); n != before {
		t.Fatalf("%d rows on b, were %d", n, before)
	}
	if v := planOf(t, b, id); v.State == PlanApproved {
		t.Fatalf("the revision on b: %s", v.State)
	}
}

// A resend of an operation is answered from this store's own rows: a row
// of another replica's with the same actor and op id is not this store's
// operation.
func TestResendLooksUpLocalRowsOnly(t *testing.T) {
	ctx := context.Background()
	a, b := replicaPair(t)
	newProject(t, a, "CND")
	shareAll(t, a)
	rc, err := a.CreateTicket(ctx, testOp("same-id"), NewTicket{Project: "CND", Title: "On a"})
	want(t, rc, err, StatusDone, "")
	var after int64
	pull(t, a, b, &after)
	rc, err = b.CreateTicket(ctx, testOp("same-id"), NewTicket{Project: "CND", Title: "On b"})
	want(t, rc, err, StatusDone, "")
	if rc.Repeat || rc.Key == "CND-1" {
		t.Fatalf("b's operation answered as a's: %+v", rc)
	}
	if got := texts(t, b, "SELECT title FROM tickets ORDER BY title"); !slices.Equal(got, []string{"On a", "On b"}) || rc.Key == "" {
		t.Fatalf("b's tickets %v", got)
	}
}

// exchange hands every row each store has to the other, in batches of
// the sizes and in the order the seed draws, the rows of a batch shuffled
// (so a child comes before its parent in some), until neither has
// anything new.
func exchange(t *testing.T, seed uint64, stores ...*Store) {
	t.Helper()
	ctx := context.Background()
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for round := 0; round < 3; round++ {
		for i, from := range stores {
			all, _, err := from.ChangesFrom(ctx, 0, MaxChangesPage)
			if err != nil {
				t.Fatal(err)
			}
			r.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
			views := map[string][]byte{}
			for _, c := range all {
				if c.ViewDigest != "" {
					v, err := from.RevisionView(ctx, c.ViewDigest)
					if err != nil {
						t.Fatal(err)
					}
					views[c.ViewDigest] = v
				}
			}
			keys := registryKeys(t, from)
			for j, to := range stores {
				if i == j {
					continue
				}
				for rest := all; len(rest) > 0; {
					n := 1 + r.IntN(len(rest))
					if _, err := to.ApplyRemote(ctx, rest[:n], views, keys); err != nil {
						t.Fatalf("seed %d: %v", seed, err)
					}
					rest = rest[n:]
				}
			}
		}
	}
}

// Two stores holding the same rows hold the same row tables, whatever
// batches the rows arrived in and in whatever order: both write in one
// shared project (fields, members added and removed on both, a relation
// on each side, a parent set, a comment edited and deleted, a revision),
// and every split and order of their logs converges.
func TestReplicasConverge(t *testing.T) {
	for seed := uint64(1); seed <= 6; seed++ {
		a, b := replicaPair(t)
		newProject(t, a, "CND")
		shareAll(t, a)
		var after int64
		pull(t, a, b, &after)
		ka := writeMuch(t, a, "a", "CND")
		kb := writeMuch(t, b, "b", "CND")
		exchange(t, seed, a, b)
		ctx := context.Background()
		titleA, titleB := "by a", "by b"
		mustEdit(t, a, "a-race", kb, TicketEdit{Set: TicketSet{Title: &titleA}})
		mustEdit(t, b, "b-race", ka, TicketEdit{Set: TicketSet{Title: &titleB}})
		parent := ka
		mustEdit(t, b, "b-parent", kb, TicketEdit{Set: TicketSet{Parent: &parent}})
		propose(t, a, "a-plan", ka, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
		if rc, err := a.DeleteTicket(ctx, testOp("a-delete"), kb); err != nil || rc.Status != StatusDone {
			t.Fatalf("delete %+v %v", rc, err)
		}
		exchange(t, seed, a, b)
		sameState(t, a, b)
		if n := count(t, a, "SELECT COUNT(*) FROM held_rows") + count(t, b, "SELECT COUNT(*) FROM held_rows"); n != 0 {
			t.Fatalf("seed %d: %d rows held after the exchange", seed, n)
		}
	}
}

// A person's edit made after a later-dated edit arrived from a machine
// whose clock runs ahead is the newest on every machine: the store stamps
// it past the newest row of its field.
func TestLocalEditAfterALaterDatedRemoteEditWins(t *testing.T) {
	a, b := replicaPair(t)
	newProject(t, a, "CND")
	shareAll(t, a)
	_, key := newTicket(t, a, "CND", "First")
	var after int64
	pull(t, a, b, &after)
	ahead := time.Now().Add(time.Hour)
	a.clock = func() time.Time { return ahead }
	title := "From the future"
	mustEdit(t, a, "ahead", key, TicketEdit{Set: TicketSet{Title: &title}})
	pull(t, a, b, &after)
	mine := "Edited after"
	mustEdit(t, b, "after", key, TicketEdit{Set: TicketSet{Title: &mine}})
	exchange(t, 1, a, b)
	for _, s := range []*Store{a, b} {
		if got := ticketOf(t, s, key).Title; got != mine {
			t.Fatalf("the title %q, want the later edit's", got)
		}
	}
	sameState(t, a, b)
}

// planPair is two replicas of one shared project CND holding its first
// ticket, a with criteria, and the ticket's key.
func planPair(t *testing.T) (a, b *Store, key string, after *int64) {
	t.Helper()
	a, b = replicaPair(t)
	newProject(t, a, "CND")
	shareAll(t, a)
	_, key = makeTicket(t, a, NewTicket{Project: "CND", Title: "Fix the login loop", Description: "It loops.", Acceptance: "- no loop"})
	after = new(int64)
	pull(t, a, b, after)
	return a, b, key, after
}

// A revision made on another machine is held as its view: its read says
// so (no body, its view, the machine it was made on by its device's name)
// and a launch of it here is refused body_elsewhere, approved or not.
func TestViewOnlyRevisionDoesNotLaunch(t *testing.T) {
	ctx := context.Background()
	a, b, key, after := planPair(t)
	id := simplePlan(t, a, key)
	pull(t, a, b, after)
	if _, err := b.db.Exec("INSERT INTO replica_names (tag, name) VALUES ('a1', 'studio')"); err != nil {
		t.Fatal(err)
	}
	v := planOf(t, b, id)
	if v.BodyHere || len(v.Body) != 0 || len(v.View) == 0 || v.Origin != "studio" || v.State != PlanApproved {
		t.Fatalf("the revision held as its view: bodyHere %v body %d view %d origin %q state %s", v.BodyHere, len(v.Body), len(v.View), v.Origin, v.State)
	}
	rc, err := b.Launch(ctx, testOp("launch-here"), key, id, "")
	want(t, rc, err, StatusFailed, RefusalBodyElsewhere)
	if !strings.Contains(rc.Error, "studio") {
		t.Fatalf("the refusal's words %q", rc.Error)
	}
	// Approved here too, still not launched here.
	approve(t, b, "approve-here", id, "here")
	rc, err = b.Launch(ctx, testOp("launch-again"), key, id, "")
	want(t, rc, err, StatusFailed, RefusalBodyElsewhere)
	if mine := planOf(t, a, id); !mine.BodyHere || len(mine.Body) == 0 || mine.Origin != "" {
		t.Fatalf("the maker's read: %+v", mine.PlanSummary)
	}
}

// An approval given on another machine never launches a revision here:
// the launch is refused approved_elsewhere until this machine's operator
// approves it again, which is taken though the revision is approved, and
// then it launches.
func TestRemoteApprovalDoesNotLaunch(t *testing.T) {
	ctx := context.Background()
	a, b, key, after := planPair(t)
	id := propose(t, a, "propose-on-a", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	pull(t, a, b, after)
	approve(t, b, "approve-on-b", id, "from b")
	var back int64
	pull(t, b, a, &back)
	if v := planOf(t, a, id); v.State != PlanApproved || v.ApprovedOrigin != "b1" {
		t.Fatalf("the approval on a: state %s by %q", v.State, v.ApprovedOrigin)
	}
	rc, err := a.Launch(ctx, testOp("launch-on-a"), key, id, "")
	want(t, rc, err, StatusFailed, RefusalApprovedElsewhere)
	approve(t, a, "approve-on-a", id, "mine")
	if v := planOf(t, a, id); v.ApprovedOrigin != "" {
		t.Fatalf("the approval after a's own: %q", v.ApprovedOrigin)
	}
	mustLaunch(t, a, "launch-on-a-2", key, id, "")
}

// An approval's origin is the origin of the newest row of any of its
// fields: an approval here followed by a later one on another machine,
// given to a changed intent by the same operator name, reads as the other
// machine's, and the launch here is refused approved_elsewhere.
func TestAnApprovalIsTheOriginOfItsNewestRow(t *testing.T) {
	ctx := context.Background()
	a, b, key, after := planPair(t)
	id := propose(t, a, "propose-on-a", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	pull(t, a, b, after)
	approve(t, b, "approve-on-b", id, "from b")
	var back int64
	pull(t, b, a, &back)
	approve(t, a, "approve-on-a", id, "mine")
	title := "Fix the login loop for good"
	mustEdit(t, b, "edit-on-b", key, TicketEdit{Set: TicketSet{Title: &title}})
	approve(t, b, "approve-on-b-again", id, "b again")
	pull(t, b, a, &back)
	if v := planOf(t, a, id); v.ApprovedOrigin != "b1" {
		t.Fatalf("the approval on a reads as %q", v.ApprovedOrigin)
	}
	rc, err := a.Launch(ctx, testOp("launch-on-a"), key, id, "")
	want(t, rc, err, StatusFailed, RefusalApprovedElsewhere)
	// Every field of b's approval came with it: a's own row of none is newer.
	n := count(t, a, "SELECT COUNT(*) FROM changes WHERE entity = 'plan' AND entity_id = ? AND field = 'approved_by' AND origin = 'b1'", id)
	if n != 2 {
		t.Fatalf("%d approved_by rows of b's on a, want one for each approval", n)
	}
}

// Every row from elsewhere is bound to the project of the entity it
// touches as this store holds it: a row naming a shared project but
// touching a ticket of a project this machine never shared, one naming no
// project, a ticket's create whose value makes it in another project, and
// a comment's create on a ticket of a project not shared are each refused
// ErrNotShared, and the project not shared is as it was.
func TestARowIsBoundToTheProjectOfWhatItTouches(t *testing.T) {
	ctx := context.Background()
	a, b := replicaPair(t)
	cnd := newProject(t, a, "CND")
	shareAll(t, a)
	_, key := newTicket(t, a, "CND", "Shared")
	comment(t, a, "comment-on-a", key, "a comment from a, long enough to judge")
	var after int64
	pull(t, a, b, &after)
	own := newProject(t, b, "OWN")
	x, xKey := newTicket(t, b, "OWN", "Mine alone")
	rows, _, err := a.ChangesFrom(ctx, 0, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	of := func(entity, op, field string) ChangeRow {
		for _, c := range rows {
			if c.Entity == entity && c.Op == op && c.Field == field {
				return c
			}
		}
		t.Fatalf("no %s %s %s row on a", entity, op, field)
		return ChangeRow{}
	}
	revalue := func(c ChangeRow, set map[string]any) ChangeRow {
		v := map[string]any{}
		if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
			t.Fatal(err)
		}
		for k, x := range set {
			v[k] = x
		}
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		c.Value = string(raw)
		return c
	}
	title := of(EntityTicket, OpSet, FieldTitle)
	title.EntityID, title.TicketID, title.ProjectID, title.Value, title.OpID, title.At = x, x, cnd, "written from elsewhere", "bound-1", time.Now().Add(time.Hour)
	unnamed := title
	unnamed.ProjectID, unnamed.OpID = "", "bound-2"
	fresh := newID()
	made := revalue(of(EntityTicket, OpCreate, ""), map[string]any{"id": fresh, "projectId": own, "number": 77})
	made.EntityID, made.TicketID, made.OpID = fresh, fresh, "bound-3"
	note := newID()
	onX := revalue(of(EntityComment, OpCreate, ""), map[string]any{"id": note, "ticketId": x})
	onX.EntityID, onX.TicketID, onX.OpID = note, x, "bound-4"
	shared := title
	shared.EntityID, shared.TicketID, shared.ProjectID = of(EntityTicket, OpCreate, "").EntityID, of(EntityTicket, OpCreate, "").EntityID, cnd
	shared.Op, shared.Value, shared.Prior = OpAdd, "", ""
	relation, err := json.Marshal(Relation{ID: newID(), FromID: x, ToID: shared.EntityID, Kind: "blocks", CreatedAt: time.Now(), CreatedBy: "someone"})
	if err != nil {
		t.Fatal(err)
	}
	from := shared
	from.Field, from.Value, from.OpID = "relations", string(relation), "bound-5"
	relation, err = json.Marshal(Relation{ID: newID(), FromID: shared.EntityID, ToID: x, Kind: "blocks", CreatedAt: time.Now(), CreatedBy: "someone"})
	if err != nil {
		t.Fatal(err)
	}
	to := shared
	to.Field, to.Value, to.OpID = "relations", string(relation), "bound-6"
	label := shared
	label.Field, label.Value, label.OpID = "labels", newLabel(t, b, "OWN", "mine"), "bound-7"
	before := count(t, b, "SELECT COUNT(*) FROM changes")
	for _, tc := range []struct {
		name string
		row  ChangeRow
	}{{"a title under a shared project's id", title}, {"a title naming no project", unnamed},
		{"a ticket made in another project", made}, {"a comment on a ticket not shared", onX},
		{"a relation from a ticket not shared", from}, {"a relation to a ticket not shared", to}, {"a label of a project not shared", label}} {
		got, err := b.ApplyRemote(ctx, []ChangeRow{tc.row}, nil, registryKeys(t, a))
		if !errors.Is(err, ErrNotShared) || got.Applied != 0 {
			t.Errorf("%s: %+v %v", tc.name, got, err)
		}
	}
	if n := count(t, b, "SELECT COUNT(*) FROM changes"); n != before {
		t.Fatalf("%d rows on b, were %d", n, before)
	}
	if tk := ticketOf(t, b, xKey); tk.Title != "Mine alone" || !tk.DeletedAt.IsZero() {
		t.Fatalf("b's own ticket: %q deleted %v", tk.Title, tk.DeletedAt)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM tickets WHERE project_id = ?", own); n != 1 {
		t.Fatalf("%d tickets in b's own project", n)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM comments WHERE ticket_id = ?", x); n != 0 {
		t.Fatalf("%d comments on b's own ticket", n)
	}
}

// A run link names a run of the machine that wrote it: a link from
// another machine naming a run held here, dated before this machine's own
// link of it, takes nothing from that link, and a run this machine never
// linked is not linked here by another machine's row; the run's reports
// find no ticket here.
func TestARunLinkFromElsewhereNamesThatMachinesRun(t *testing.T) {
	ctx := context.Background()
	a, b, k1, after := planPair(t)
	_, k2 := newTicket(t, a, "CND", "Second")
	pull(t, a, b, after)
	const run, other = "crew-0123abcd", "crew-89abcdef"
	a.SetRunExists(runs(run))
	if rc, err := addLink(a, "link-on-a", k1, NewLink{Kind: LinkRun, Ref: run}); err != nil || rc.Status != StatusDone {
		t.Fatalf("a's link: %+v %v", rc, err)
	}
	b.SetRunExists(runs(run, other))
	earlier := time.Now().Add(-time.Hour)
	b.clock = func() time.Time { return earlier }
	for i, ref := range []string{run, other} {
		if rc, err := addLink(b, fmt.Sprintf("link-on-b-%d", i), k2, NewLink{Kind: LinkRun, Ref: ref}); err != nil || rc.Status != StatusDone {
			t.Fatalf("b's link of %s: %+v %v", ref, rc, err)
		}
	}
	var back int64
	pull(t, b, a, &back)
	if n := count(t, a, "SELECT COUNT(*) FROM links WHERE ticket_id = ? AND ref = ? AND deleted_at = ''", ticketOf(t, a, k1).ID, run); n != 1 {
		t.Fatalf("a's own link of the run: %d live", n)
	}
	if n := count(t, a, "SELECT COUNT(*) FROM changes WHERE op = ?", OpReconcile); n != 0 {
		t.Fatalf("%d repairs on a", n)
	}
	if tk, found, err := a.RunTicket(ctx, run); err != nil || !found || tk.Key != k1 {
		t.Fatalf("the run's ticket on a: %q %v %v", tk.Key, found, err)
	}
	if tk, found, err := a.RunTicket(ctx, other); err != nil || found {
		t.Fatalf("a run a never linked, on a: %q %v %v", tk.Key, found, err)
	}
	a.SetRunExists(runs(run, other))
	if _, err := a.AddRunComment(ctx, testOp("report"), other, "a report of a run a never linked"); !errors.Is(err, ErrNoRunTicket) {
		t.Fatalf("a report of a run a never linked: %v", err)
	}
}

// linkedPair is replicaPair for stores that read GitHub, with the shared
// project CND received on b.
func linkedPair(t *testing.T) (a, b *Store, after *int64) {
	t.Helper()
	a, b = linkedStore(t), linkedStore(t)
	join(t, a, "a1")
	join(t, b, "b1")
	newProject(t, a, "CND")
	shareAll(t, a)
	after = new(int64)
	pull(t, a, b, after)
	return a, b, after
}

// A ticket imported from a tracker on one machine arrives on another with
// its linked item, its owners, its address, its title and its
// description, and without the snapshot of the item, which never leaves
// a machine: the receiver keeps no snapshot and no read of the item, so
// its next open reads the tracker with its own credential.
func TestCounterpartMergedWithoutRemoteSnapshots(t *testing.T) {
	a, b, after := linkedPair(t)
	key := mustImport(t, a, "imp", "CND", ghRead("I_kwDO1", 0, "Login loops", "It loops."))
	pull(t, a, b, after)
	mine, theirs := ticketOf(t, a, key), ticketOf(t, b, key)
	if theirs.ExternalID != mine.ExternalID || theirs.ExternalURL != mine.ExternalURL || string(theirs.Authority) != string(mine.Authority) ||
		theirs.Title != mine.Title || theirs.Description != mine.Description || mine.ExternalID == "" {
		t.Fatalf("the counterpart group on b: %+v, a's %+v", theirs, mine)
	}
	if len(theirs.ExternalSnapshot) != 0 || len(mine.ExternalSnapshot) == 0 {
		t.Fatalf("a snapshot on b: %s", theirs.ExternalSnapshot)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM ticket_reads"); n != 0 {
		t.Fatalf("%d reads of the item on b", n)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM changes WHERE field = 'external_snapshot'"); n != 0 {
		t.Fatalf("%d snapshot rows on b", n)
	}
}

// One item imported on two machines before either held the other's
// ticket: both tickets kept on both, neither held nor renumbered, each
// naming the other as linked to the same item; a third import of it is
// refused external_linked.
func TestSameItemImportedOnTwoReplicasKeptAndMarked(t *testing.T) {
	ctx := context.Background()
	a, b, _ := linkedPair(t)
	read := ghRead("I_kwDO1", 0, "Login loops", "It loops.")
	ka := mustImport(t, a, "imp-a", "CND", read)
	kb := mustImport(t, b, "imp-b", "CND", read)
	exchange(t, 3, a, b)
	if n := count(t, a, "SELECT COUNT(*) FROM held_rows") + count(t, b, "SELECT COUNT(*) FROM held_rows"); n != 0 {
		t.Fatalf("%d held", n)
	}
	for _, s := range []*Store{a, b} {
		va, err := s.Ticket(ctx, ka)
		if err != nil {
			t.Fatal(err)
		}
		vb, err := s.Ticket(ctx, kb)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(va.AlsoLinked, []string{kb}) || !slices.Equal(vb.AlsoLinked, []string{ka}) {
			t.Fatalf("also linked: %s %v, %s %v", ka, va.AlsoLinked, kb, vb.AlsoLinked)
		}
		if got, err := s.TicketsLinkedTo(ctx, va.ExternalID); err != nil || len(got) != 2 {
			t.Fatalf("linked to the item: %v %v", got, err)
		}
	}
	rc, err := b.ImportTicket(ctx, testOp("imp-c"), ImportArgs{Project: "CND", URL: read.Snapshot.URL}, read)
	want(t, rc, err, StatusFailed, RefusalExternalLinked)
	sameState(t, a, b)
}

// Two replicas each adding one half of a blocks cycle before seeing the
// other's: the merged log admits the earlier add in the one order and
// refuses the later, which each replica undoes with a repair row of the
// actor merge (never handed out), so both hold the same graph.
func TestReconcileAfterRemoteRows(t *testing.T) {
	ctx := context.Background()
	a, b, k1, after := planPair(t)
	_, k2 := newTicket(t, a, "CND", "Second")
	pull(t, a, b, after)
	rc, err := a.AddRelation(ctx, testOp("a-blocks"), k1, RelationBlocks, k2)
	want(t, rc, err, StatusDone, "")
	rc, err = b.AddRelation(ctx, testOp("b-blocks"), k2, RelationBlocks, k1)
	want(t, rc, err, StatusDone, "")
	exchange(t, 2, a, b)
	sameState(t, a, b)
	for _, s := range []*Store{a, b} {
		if n := count(t, s, "SELECT COUNT(*) FROM relations WHERE kind = ? AND deleted_at = ''", RelationBlocks); n != 1 {
			t.Fatalf("%d live blocks relations: a cycle kept", n)
		}
		if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE actor = ? AND op = ?", ActorMerge, OpReconcile); n != 1 {
			t.Fatalf("%d repair rows", n)
		}
		rows, _, err := s.ChangesFrom(ctx, 0, MaxChangesPage)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range rows {
			if c.Actor == ActorMerge {
				t.Fatal("a repair row handed out")
			}
		}
	}
}

// Every replica orders the same rows the same way: a local row's actor
// is ordered as its origin qualifies it, so a row written on a and the
// same row applied on b sort alike against a third.
func TestCanonOrdersTheSameEverywhere(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	local := ChangeRow{At: at, Actor: "admin", Origin: "a1", OpID: "x", N: 0}
	applied := ChangeRow{At: at, Actor: Canon("admin", "a1"), Origin: "a1", OpID: "x", N: 0}
	other := ChangeRow{At: at, Actor: Canon("admin", "b1"), Origin: "b1", OpID: "x", N: 0}
	if Less(local, other) != Less(applied, other) || Less(other, local) != Less(other, applied) || TagOf(local) != TagOf(applied) {
		t.Fatal("a row orders otherwise where it was written than where it was applied")
	}
	if Canon("admin", "") != "admin" || Canon("@a1/admin", "b1") != "@a1/admin" || Canon("admin", "a1") != "@a1/admin" {
		t.Fatal("Canon")
	}
}

// Two stores restored from one export are two instances: each retires
// the allocations it inherited at its open, so creates in a shared
// project, offline and then joined under two tags with two overflow
// ranges, never take one number twice.
func TestTwoStoresRestoredFromOneExportNeverShareANumber(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	newProject(t, s, "CND")
	join(t, s, "a1")
	shareAll(t, s)
	if _, err := s.db.Exec("UPDATE projects SET number_from = 1, number_to = 100, next_number = 1, block_tag = 'a1'"); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(t.TempDir(), "export.sqlite")
	if err := s.Snapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	var copies []*Store
	for range 2 {
		d := storeDir(t)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(snap)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, File), b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Reissue(ctx, filepath.Join(d, File)); err != nil {
			t.Fatal(err)
		}
		copies = append(copies, openStore(t, d))
	}
	numbers := map[int64]bool{}
	take := func(st *Store, title string) {
		_, key := newTicket(t, st, "CND", title)
		n := ticketOf(t, st, key).Number
		if numbers[n] {
			t.Fatalf("the number %d taken twice", n)
		}
		numbers[n] = true
	}
	for i, c := range copies {
		take(c, fmt.Sprintf("offline %d", i))
	}
	for i, c := range copies {
		if err := c.Join(ctx, fmt.Sprintf("c%d", i), "w1", fmt.Sprintf("dev-c%d", i), int64(3_000_000+i*1_000_000), int64(3_999_999+i*1_000_000)); err != nil {
			t.Fatal(err)
		}
		take(c, fmt.Sprintf("joined %d", i))
	}
	for n := range numbers {
		if n >= 1 && n <= 100 {
			t.Fatalf("the number %d from the inherited block", n)
		}
	}
}

// A held row that is not a row from another machine (its JSON not a
// change row, or one a batch's check refuses) stays held as it is across
// an open and a batch, and stops neither: the batch applies, and the
// rows held beside it are taken.
func TestAHeldRowNotARowStaysAsItIs(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	join(t, s, "a1")
	for _, row := range []string{"{}", "not a row", `{"seq":1,"entity":"ticket","entityId":"t1","op":"create","value":"{}","at":"2026-11-19T00:00:00Z","actor":"","opId":"x","n":0}`} {
		if _, err := s.db.Exec("INSERT INTO held_rows (project_key, why, waits_for, row, held_at) VALUES ('ODD', 'held', 'project:p1', ?, '2026-11-19T00:00:00.000000000Z')", row); err != nil {
			t.Fatal(err)
		}
	}
	before := texts(t, s, "SELECT seq || ' ' || project_key || ' ' || why || ' ' || waits_for || ' ' || row || ' ' || held_at FROM held_rows ORDER BY seq")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, dir)
	if _, err := s.ApplyRemote(ctx, nil, nil, nil); err != nil {
		t.Fatalf("a batch beside rows not rows: %v", err)
	}
	if got := texts(t, s, "SELECT seq || ' ' || project_key || ' ' || why || ' ' || waits_for || ' ' || row || ' ' || held_at FROM held_rows ORDER BY seq"); !slices.Equal(got, before) {
		t.Fatalf("the held rows:\n got %v\nwant %v", got, before)
	}
}
