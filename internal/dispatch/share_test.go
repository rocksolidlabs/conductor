package dispatch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// shareOp is an operation of the operator's.
func shareOp(id string) Op { return Op{Actor: ActorAdmin, ID: id, By: "Operator"} }

// A store that has joined nothing numbers and keys as before sync: its
// tickets take next_number in order, a share is asked of no store.
func TestUnjoinedStoreIsMain(t *testing.T) {
	s := openStore(t, storeDir(t))
	newProject(t, s, "CND")
	for i := 1; i <= 3; i++ {
		_, key := newTicket(t, s, "CND", "t")
		if want := TicketKey("CND", int64(i)); key != want {
			t.Fatalf("ticket %d is %s, want %s", i, key, want)
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE origin != ''"); n != 0 {
		t.Fatalf("%d rows with an origin", n)
	}
}

// A share reserves ShareReserve numbers past the project's next, which
// its creates take while the share completes, then the overflow range;
// once shared they take the block handed to the active tag, then the
// block asked ahead, then the overflow range, so a create never waits.
func TestShareReservesItsNumbersAndBlocksNeverWait(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	id := newProject(t, s, "CND")
	join(t, s, "a1")
	newTicket(t, s, "CND", "before")
	rc, err := s.ShareProject(ctx, shareOp("share"), "CND")
	want(t, rc, err, StatusDone, "")
	if got := texts(t, s, "SELECT share_state || ' ' || share_reserved_to FROM projects"); !slices.Equal(got, []string{"pending 1001"}) {
		t.Fatalf("the share %v", got)
	}
	_, k := newTicket(t, s, "CND", "while pending")
	if k != "CND-2" {
		t.Fatalf("a create while pending took %s", k)
	}
	if err := s.CompleteShare(ctx, id, "w1", 0); !errors.As(err, new(ErrShareMoved)) {
		t.Fatalf("a share completed past rows it has not handed out: %v", err)
	}
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteShare(ctx, id, "w1", head); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmShare(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBlock(ctx, id, 10, 11, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBlock(ctx, id, 20, 20, true); err != nil {
		t.Fatal(err)
	}
	var got []int64
	for range 5 {
		_, key := newTicket(t, s, "CND", "shared")
		got = append(got, ticketOf(t, s, key).Number)
	}
	if !slices.Equal(got, []int64{10, 11, 20, 1_000_001, 1_000_002}) {
		t.Fatalf("the numbers %v", got)
	}
}

// A project rekeyed only while it is not shared: pending or completing
// share_in_progress, shared shared; the key taken, invalid; done, its
// tickets' keys follow and its history keeps the key it was made under.
func TestRekeyOnlyBeforeShare(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	newProject(t, s, "CND")
	newProject(t, s, "OPS")
	newTicket(t, s, "CND", "one")
	for _, c := range []struct{ key, code string }{{"OPS", RefusalKeyTaken}, {"lower", RefusalInvalid}, {"CND", RefusalInvalid}} {
		rc, err := s.RekeyProject(ctx, shareOp("bad-"+c.key), "CND", c.key)
		want(t, rc, err, StatusFailed, c.code)
	}
	rc, err := s.RekeyProject(ctx, shareOp("rekey"), "CND", "NEW")
	want(t, rc, err, StatusDone, "")
	if got := texts(t, s, "SELECT key FROM tickets"); !slices.Equal(got, []string{"NEW-1"}) {
		t.Fatalf("the tickets' keys %v", got)
	}
	if got := texts(t, s, "SELECT value FROM changes WHERE entity = 'project' AND op = 'create' AND entity_id = (SELECT id FROM projects WHERE key = 'NEW')"); len(got) != 1 || !strings.Contains(got[0], `"key":"CND"`) {
		t.Fatalf("the create row %v", got)
	}
	join(t, s, "a1")
	rc, err = s.ShareProject(ctx, shareOp("share"), "NEW")
	want(t, rc, err, StatusDone, "")
	rc, err = s.RekeyProject(ctx, shareOp("rekey-pending"), "NEW", "NEWER")
	want(t, rc, err, StatusFailed, RefusalShareInProgress)
	if _, err := s.db.Exec("UPDATE projects SET share_state = 'shared', shared = 'w1' WHERE key = 'NEW'"); err != nil {
		t.Fatal(err)
	}
	rc, err = s.RekeyProject(ctx, shareOp("rekey-shared"), "NEW", "NEWER")
	want(t, rc, err, StatusFailed, RefusalShared)
}

// A share refused is refused, its reserved numbers taken back, and keeps
// the words for the project's page; a dormant project is still the
// workspace's, nothing of it handed out, and the first row from another
// machine makes it shared again; an unshared one takes nothing.
func TestRefuseDormantAndUnshared(t *testing.T) {
	ctx := context.Background()
	a, b, first, after := planPair(t)
	id := ticketOf(t, a, first).ProjectID
	// Refused: a project of b's own, shared and refused.
	own := newProject(t, b, "OWN")
	rc, err := b.ShareProject(ctx, shareOp("share-own"), "OWN")
	want(t, rc, err, StatusDone, "")
	if err := b.RefuseShare(ctx, own, "OWN is another project's key in the workspace: give it another key"); err != nil {
		t.Fatal(err)
	}
	v, err := b.Project(ctx, "OWN")
	if err != nil || v.Project.Share == nil || v.Project.Share.State != ShareRefused || !strings.Contains(v.Project.Share.Words, "another key") ||
		v.Project.Share.ReservedTo != 0 {
		t.Fatalf("the refused share %+v %v", v.Project.Share, err)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM projects WHERE id = ? AND share_state = 'refused' AND share_reserved_to = 0", own); n != 1 {
		t.Fatal("the reservation kept")
	}
	// Dormant: nothing handed out, and a row from a turns it shared.
	if err := b.Dormant(ctx, "w1", "the workspace's copy is gone"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM projects WHERE id = ? AND share_state = 'dormant'", id); n != 1 {
		t.Fatal("not dormant")
	}
	_, key := newTicket(t, b, "CND", "while dormant")
	if rows, _, err := b.HandedChanges(ctx, 0, MaxChangesPage); err != nil || slices.ContainsFunc(rows, func(c ChangeRow) bool { return c.ProjectID == id }) {
		t.Fatalf("a dormant project's rows handed out: %v", err)
	}
	newTicket(t, a, "CND", "from a")
	pull(t, a, b, after)
	if n := count(t, b, "SELECT COUNT(*) FROM projects WHERE id = ? AND share_state = 'shared'", id); n != 1 {
		t.Fatal("a dormant project not shared again by a row from another machine")
	}
	_ = key
	// Unshared: refused from then on.
	if err := b.Unshared(ctx, "w1", "no longer shared with you"); err != nil {
		t.Fatal(err)
	}
	newTicket(t, a, "CND", "after unshared")
	rows, _, err := a.HandedChanges(ctx, *after, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyRemote(ctx, rows, nil, registryKeys(t, a)); !errors.Is(err, ErrNotShared) {
		t.Fatalf("a row of an unshared project: %v", err)
	}
	if rows, _, err := b.HandedChanges(ctx, 0, MaxChangesPage); err != nil || slices.ContainsFunc(rows, func(c ChangeRow) bool { return c.ProjectID == id }) {
		t.Fatalf("an unshared project's rows handed out: %v", err)
	}
}

// A project's own numbers at ShareNumbersBelow or past it refuse a share,
// and an archived project does too.
func TestShareRefusesNumbersInTheReservedRange(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	newProject(t, s, "CND")
	if _, err := s.db.Exec("UPDATE projects SET next_number = ?", ShareNumbersBelow); err != nil {
		t.Fatal(err)
	}
	newTicket(t, s, "CND", "high")
	rc, err := s.ShareProject(ctx, shareOp("share-high"), "CND")
	want(t, rc, err, StatusFailed, RefusalNumbersOutOfRange)
	newProject(t, s, "OLD")
	rc, err = s.ArchiveProject(ctx, shareOp("archive"), "OLD")
	want(t, rc, err, StatusDone, "")
	rc, err = s.ShareProject(ctx, shareOp("share-archived"), "OLD")
	want(t, rc, err, StatusFailed, RefusalArchived)
}

// A block handed to a tag the store no longer has active is never taken:
// a create in the project takes the overflow range.
func TestBlockOfARetiredTagNotUsed(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	id := newProject(t, s, "CND")
	join(t, s, "a1")
	shareAll(t, s)
	join(t, s, "b2")
	// A block left by the tag a1 (as a store copied before its retire
	// holds one).
	if _, err := s.db.Exec("UPDATE projects SET number_from = 10, number_to = 20, next_number = 10, block_tag = 'a1' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	_, key := newTicket(t, s, "CND", "after a re-join")
	if n, first := ticketOf(t, s, key).Number, int64(count(t, s, "SELECT overflow_from FROM replica_tags WHERE tag = 'b2'")); n != first || first <= 1_100_000 {
		t.Fatalf("the number %d, want the overflow range's first, %d", n, first)
	}
	if err := s.SetBlock(ctx, "no-such", 1, 2, false); !errors.Is(err, ErrNoSuchProject) {
		t.Fatalf("a block of no project: %v", err)
	}
}

// A create that takes a shared project's block past its half tells
// OnBlockLow after its commit, from a goroutine of the store's own; a
// call that has not returned drops the next.
func TestBlockLowCalledAfterCommit(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	id := newProject(t, s, "CND")
	join(t, s, "a1")
	shareAll(t, s)
	if err := s.SetBlock(ctx, id, 10, 13, false); err != nil {
		t.Fatal(err)
	}
	told := make(chan string, 4)
	release := make(chan struct{})
	s.OnBlockLow(func(project string) {
		// The commit is done: the ticket is there to read.
		if n := count(t, s, "SELECT COUNT(*) FROM tickets"); n == 0 {
			t.Error("told before the commit")
		}
		told <- project
		<-release
	})
	newTicket(t, s, "CND", "10")
	select {
	case p := <-told:
		t.Fatalf("told at the block's first number: %s", p)
	default:
	}
	newTicket(t, s, "CND", "11")
	newTicket(t, s, "CND", "12")
	if p := <-told; p != id {
		t.Fatalf("told of %s", p)
	}
	// The call has not returned: a create past the half again drops it,
	// and no second call waits behind the first.
	newTicket(t, s, "CND", "13")
	if !s.blockLowBusy.Load() {
		t.Fatal("the first call not under way")
	}
	select {
	case p := <-told:
		t.Fatalf("told twice while the first call ran: %s", p)
	default:
	}
	close(release)
}

// Another replica's device names are kept by tag, words only; a name
// that is not one line of words, or of a tag that is not one, is refused.
func TestSetNames(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	if err := s.SetNames(ctx, map[string]string{"a1": "studio", "b2": "see https://x.example/p?q=1"}); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, s, "SELECT tag || '=' || name FROM replica_names ORDER BY tag"); len(got) != 2 || got[0] != "a1=studio" || strings.Contains(got[1], "?q=1") {
		t.Fatalf("the names %v", got)
	}
	for _, bad := range []map[string]string{{"A1": "x"}, {"a1": ""}, {"a1": "two\nlines"}, {"a1": strings.Repeat("n", MaxDeviceName+1)}} {
		if err := s.SetNames(ctx, bad); err == nil {
			t.Errorf("names %v taken", bad)
		}
	}
}

// Rows held for a workspace's project whose key a project of this
// machine's holds survive a restart, the project's read says so, and a
// rekey of the local project applies them at once.
func TestRekeyAfterRestartAppliesHeldRows(t *testing.T) {
	ctx := context.Background()
	a := openStore(t, storeDir(t))
	join(t, a, "a1")
	dir := storeDir(t)
	b := openStore(t, dir)
	join(t, b, "b1")
	newProject(t, b, "CND")
	newProject(t, a, "CND")
	shareAll(t, a)
	newTicket(t, a, "CND", "Theirs")
	var after int64
	pull(t, a, b, &after)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b = openStore(t, dir)
	v, err := b.Project(ctx, "CND")
	if err != nil || v.Project.HeldBy == nil || v.Project.HeldBy.Key != "CND" || v.Project.HeldBy.Rows < 2 {
		t.Fatalf("the project's read: %+v %v", v.Project.HeldBy, err)
	}
	rc, err := b.RekeyProject(ctx, shareOp("rekey"), "CND", "OWN")
	want(t, rc, err, StatusDone, "")
	if n := count(t, b, "SELECT COUNT(*) FROM held_rows"); n != 0 {
		t.Fatalf("%d still held", n)
	}
	if n := count(t, b, "SELECT COUNT(*) FROM tickets t JOIN projects p ON p.id = t.project_id WHERE p.key = 'CND' AND t.title = 'Theirs'"); n != 1 {
		t.Fatal("the workspace's CND not received")
	}
}

// A refused share outlasts a restart: the project's read and the
// provider's say refused with its words, its numbers are a project's not
// shared, its rows stay here, and a rekey is still allowed; a share asked
// again forgets the words. Dormant words outlast it too, and a dormant
// project shared again by another machine's row forgets them.
func TestARefusedShareOutlastsARestart(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	join(t, s, "a1")
	own := newProject(t, s, "OWN")
	newTicket(t, s, "OWN", "first")
	rc, err := s.ShareProject(ctx, shareOp("share"), "OWN")
	want(t, rc, err, StatusDone, "")
	const words = "OWN is another project's key in the workspace: give it another key"
	if err := s.RefuseShare(ctx, own, words); err != nil {
		t.Fatal(err)
	}
	if err := s.RefuseShare(ctx, "no-such", words); !errors.Is(err, ErrNoSuchProject) {
		t.Fatalf("a refusal of no project: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, dir)
	v, err := s.Project(ctx, "OWN")
	if err != nil || v.Project.Share == nil || v.Project.Share.State != ShareRefused || v.Project.Share.Words != words || v.Project.Share.ReservedTo != 0 {
		t.Fatalf("the refused share after a restart %+v %v", v.Project.Share, err)
	}
	shared, err := s.Shared(ctx)
	if err != nil || len(shared) != 1 || shared[0].State != ShareRefused || shared[0].Words != words {
		t.Fatalf("the provider's read %+v %v", shared, err)
	}
	if _, key := newTicket(t, s, "OWN", "after"); key != "OWN-2" {
		t.Fatalf("a refused project's create took %s", key)
	}
	if rows, _, err := s.HandedChanges(ctx, 0, MaxChangesPage); err != nil || slices.ContainsFunc(rows, func(c ChangeRow) bool { return c.ProjectID == own }) {
		t.Fatalf("a refused project's rows handed out: %v", err)
	}
	rc, err = s.RekeyProject(ctx, shareOp("rekey"), "OWN", "MINE")
	want(t, rc, err, StatusDone, "")
	rc, err = s.ShareProject(ctx, shareOp("share-again"), "MINE")
	want(t, rc, err, StatusDone, "")
	if v, err := s.Project(ctx, "MINE"); err != nil || v.Project.Share == nil || v.Project.Share.State != SharePending || v.Project.Share.Words != "" {
		t.Fatalf("a share asked again %+v %v", v.Project.Share, err)
	}

	// Dormant: the words outlast a restart, and a row from another
	// machine that makes the project shared again forgets them.
	a, b, first, after := planPair(t)
	id := ticketOf(t, a, first).ProjectID
	if err := b.Dormant(ctx, "w1", "the workspace's copy is gone"); err != nil {
		t.Fatal(err)
	}
	if v, err := b.Project(ctx, "CND"); err != nil || v.Project.Share == nil || v.Project.Share.State != ShareDormant || v.Project.Share.Words != "the workspace's copy is gone" {
		t.Fatalf("a dormant share %+v %v", v.Project.Share, err)
	}
	newTicket(t, a, "CND", "from a")
	pull(t, a, b, after)
	if n := count(t, b, "SELECT COUNT(*) FROM projects WHERE id = ? AND share_state = 'shared' AND share_words = ''", id); n != 1 {
		t.Fatal("a dormant project shared again keeps its words")
	}
}

// A received project's key is the workspace registry's, never a row's: a
// project rekeyed twice before its share arrives on a replica that holds a
// project under one of its old keys, is received under the registry's
// key, its tickets with it, and a key row from elsewhere naming another
// key changes nothing.
func TestRegistryKeyProjectedNeverTheRowsKey(t *testing.T) {
	ctx := context.Background()
	a, b := replicaPair(t)
	newProject(t, a, "CND")
	rc, err := a.RekeyProject(ctx, shareOp("rekey-1"), "CND", "OWN")
	want(t, rc, err, StatusDone, "")
	newTicket(t, a, "OWN", "First")
	rc, err = a.RekeyProject(ctx, shareOp("rekey-2"), "OWN", "ZZZ")
	want(t, rc, err, StatusDone, "")
	shareAll(t, a)
	newProject(t, b, "OWN")
	var after int64
	if got := pull(t, a, b, &after); len(got.Held) != 0 {
		t.Fatalf("held on b: %v", got.Held)
	}
	if got := texts(t, b, "SELECT key || '/' || registry_key FROM projects ORDER BY key"); !slices.Equal(got, []string{"OWN/", "ZZZ/ZZZ"}) {
		t.Fatalf("b's projects: %v", got)
	}
	if got := texts(t, b, "SELECT key FROM tickets"); !slices.Equal(got, []string{"ZZZ-1"}) {
		t.Fatalf("b's tickets: %v", got)
	}
	rows, _, err := a.ChangesFrom(ctx, 0, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rows, func(c ChangeRow) bool { return c.Entity == EntityProject && c.Field == "key" })
	if i < 0 {
		t.Fatal("no key row on a")
	}
	key := rows[i]
	key.Value, key.OpID, key.At = "QQQ", "key-from-elsewhere", time.Now().Add(time.Hour)
	if _, err := b.ApplyRemote(ctx, []ChangeRow{key}, nil, registryKeys(t, a)); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, b, "SELECT key FROM projects WHERE registry_key != ''"); !slices.Equal(got, []string{"ZZZ"}) {
		t.Fatalf("the received project after a key row: %v", got)
	}
}
