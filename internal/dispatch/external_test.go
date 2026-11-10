package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// linkedStore is a store opened with the systems given (github when none),
// closed with the test.
func linkedStore(t *testing.T, systems ...string) *Store {
	t.Helper()
	if len(systems) == 0 {
		systems = []string{"github"}
	}
	s, err := OpenOptions(storeDir(t), Options{Systems: systems})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// day0 is the system's clock at a test's first read.
var day0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

// ghRead is a read of GitHub's issue #612 of rocksolidlabs/conductor with
// the node id, updated at minute m of day0, its title and body as given,
// a label, an assignee and two images.
func ghRead(node string, m int, title, body string) ExternalRead {
	return ExternalRead{
		Snapshot: ExternalSnapshot{V: SnapshotVersion, System: "github", Host: "github.com", Space: "rocksolidlabs/conductor", Kind: ExternalIssue, ID: "612",
			NodeID: node, DatabaseID: "2153", URL: "https://github.com/rocksolidlabs/conductor/issues/612", Title: title, State: "open",
			Labels: []string{"bug"}, Assignees: []string{"alice"}, Author: "bob", AuthorID: "101", CreatedAt: day0, UpdatedAt: day0.Add(time.Duration(m) * time.Minute),
			Repro: "1. Log in\n2. Loop", Images: []SnapshotImage{{ID: "https://github.com/user-attachments/assets/a1"}, {ID: "https://github.com/user-attachments/assets/a2"}}},
		Body:      body,
		IssueETag: fmt.Sprintf(`"etag-%d"`, m),
	}
}

// mustImport imports the read into the project, failing the test unless it
// was done, and gives the ticket's key.
func mustImport(t *testing.T, s *Store, opID, project string, read ExternalRead) string {
	t.Helper()
	rc, err := s.ImportTicket(context.Background(), testOp(opID), ImportArgs{Project: project, URL: read.Snapshot.URL}, read)
	want(t, rc, err, StatusDone, "")
	return rc.Key
}

// ticketRows is every change row of the ticket with the id, in the log's
// one order.
func ticketRows(t *testing.T, s *Store, ticketID string) []ChangeRow {
	t.Helper()
	rows, err := s.db.Query(`SELECT seq, entity, entity_id, ticket_id, project_id, field, op, value, prior, at, actor, by, op_id, n
		FROM changes WHERE ticket_id = ? ORDER BY at, actor, op_id, n`, ticketID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []ChangeRow
	for rows.Next() {
		var c ChangeRow
		var at string
		if err := rows.Scan(&c.Seq, &c.Entity, &c.EntityID, &c.TicketID, &c.ProjectID, &c.Field, &c.Op, &c.Value, &c.Prior, &at, &c.Actor, &c.By, &c.OpID, &c.N); err != nil {
			t.Fatal(err)
		}
		c.At = unstamp(at)
		out = append(out, c)
	}
	return out
}

// opRows is the change rows of the operation with the id, in their order:
// "field:op" each.
func opRows(t *testing.T, s *Store, opID string) []string {
	t.Helper()
	return texts(t, s, "SELECT field || ':' || op FROM changes WHERE op_id = ? ORDER BY n", opID)
}

// count is the one integer a query gives.
func countOf(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// snapshotOf is the ticket's snapshot as stored.
func snapshotOf(t *testing.T, tk Ticket) ExternalSnapshot {
	t.Helper()
	sn, ok := ParseSnapshot(string(tk.ExternalSnapshot))
	if !ok {
		t.Fatalf("%s has no snapshot: %q", tk.Key, tk.ExternalSnapshot)
	}
	return sn
}

// An import makes a ticket of GitHub's issue: its source github, its title
// and description FromExternal's (observe rows), its linked issue with the
// snapshot and its manifest, both fields GitHub's, its own fields
// Dispatch's (GitHub's labels and assignees in the snapshot, never
// mapped), its ticket_reads row; one receipt, one OnChange.
func TestImportTicket(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	log := listen(s)
	read := ghRead("I_kwDO1", 0, "Login loops", "It loops.\r\n\r\n### Steps to reproduce\r\n1. Log in")
	rc, err := s.ImportTicket(ctx, testOp("imp"), ImportArgs{Project: "CND", URL: "rocksolidlabs/conductor#612", Priority: 3, Owner: "Alice"}, read)
	want(t, rc, err, StatusDone, "")
	if rc.Key != "CND-1" || log.one(t)[0].Entity != EntityTicket {
		t.Fatalf("the receipt %+v", rc)
	}
	tk := ticketOf(t, s, rc.Key)
	identity := "github:github.com:I_kwDO1"
	if tk.Source != "github" || tk.ExternalID != identity || tk.ExternalURL != read.Snapshot.URL || tk.Title != "Login loops" ||
		tk.Description != "It loops.\n\n### Steps to reproduce\n1. Log in" || tk.Priority != 3 || tk.Owner != "Alice" || len(tk.Labels) != 0 || len(tk.Assignees) != 0 {
		t.Fatalf("the ticket: %+v", tk)
	}
	a := ParseAuthority(string(tk.Authority))
	if a != (Authority{Of: authorityKey(identity), Title: "github", Description: "github"}) {
		t.Fatalf("the owners: %+v", a)
	}
	sn := snapshotOf(t, tk)
	if sn.Identity() != identity || sn.Title != "Login loops" || sn.BodySha256 != sha256Hex(read.Body) || sn.BodyBytes != len(read.Body) ||
		sn.DescriptionSha256 != sha256Hex(tk.Description) || sn.ReproSha256 != sha256Hex("1. Log in\n2. Loop") ||
		!slices.Equal(sn.Labels, []string{"bug"}) || len(sn.Images) != 2 || sn.Images[0].done() || sn.Images[1].done() {
		t.Fatalf("the snapshot: %+v", sn)
	}
	got := opRows(t, s, "imp")
	for _, w := range []string{":create", "title:observe", "description:observe", "priority:set", "owner:set", "source:set",
		"external_id:set", "external_url:set", "external_snapshot:set", "authority:set"} {
		if !slices.Contains(got, w) {
			t.Fatalf("the import's rows %v lack %s", got, w)
		}
	}
	v, err := s.Ticket(ctx, rc.Key)
	if err != nil || v.Owners == nil || *v.Owners != (Owners{Title: "github", Description: "github"}) || v.ExternalRead == nil ||
		v.ExternalRead.ExternalID != identity || !v.ExternalRead.UpdatedAt.Equal(day0) || v.ExternalRead.IssueETag != `"etag-0"` || v.ExternalRead.ReadBy != "Tester" {
		t.Fatalf("the ticket's read: %+v %+v %v", v.Owners, v.ExternalRead, err)
	}
	// A search finds it by GitHub's title, observe rows indexing it.
	if page, err := s.List(ctx, Query{Project: "CND", Filter: Filter{Q: "loops"}}); err != nil || page.Total != 1 {
		t.Fatalf("a search: %+v %v", page, err)
	}
	// An archived project refuses it.
	newProject(t, s, "OPS")
	rc, err = s.ArchiveProject(ctx, testOp("arch"), "OPS")
	want(t, rc, err, StatusDone, "")
	rc, err = s.ImportTicket(ctx, testOp("imp2"), ImportArgs{Project: "OPS", URL: "x"}, ghRead("I_kwDO2", 0, "T", ""))
	want(t, rc, err, StatusFailed, RefusalArchived)
	// So does a read that is not a snapshot's shape.
	bad := ghRead("I_kwDO3", 0, "T", "")
	bad.Snapshot.Kind = "epic"
	rc, err = s.ImportTicket(ctx, testOp("imp3"), ImportArgs{Project: "CND", URL: "x"}, bad)
	want(t, rc, err, StatusFailed, RefusalInvalid)
}

// FromExternal cuts a title past 200 characters at a character with "…"
// (the snapshot keeping it whole), makes a CRLF body's lines \n, drops
// control characters but \t, and cuts a body past 64 KiB at a line's end
// with a last line saying the rest is on GitHub, bodyTruncated set.
func TestImportCutsTitleAndBody(t *testing.T) {
	long := strings.Repeat("é", 300)
	title, _, _ := FromExternal(long, "")
	if rs := []rune(title); len(rs) != MaxTicketTitle || rs[len(rs)-1] != '…' {
		t.Fatalf("the title: %d characters", len(rs))
	}
	if title, _, _ := FromExternal("a\tb\nc\x00d", ""); title != "a b cd" {
		t.Fatalf("a title's controls: %q", title)
	}
	if title, _, _ := FromExternal(" \x01 ", ""); title != "(untitled)" {
		t.Fatalf("an empty title: %q", title)
	}
	if _, d, cut := FromExternal("t", "a\r\nb\rc\td\x07e"); d != "a\nbc\tde" || cut {
		t.Fatalf("a body's controls: %q %v", d, cut)
	}
	line := strings.Repeat("x", 99) + "\n"
	body := strings.Repeat(line, 1000) // 100,000 bytes
	_, d, cut := FromExternal("t", body)
	if !cut || len(d) > MaxTicketDescription || !strings.HasSuffix(d, descriptionMore) || !strings.HasPrefix(body, strings.TrimSuffix(d, descriptionMore)) ||
		!strings.HasSuffix(strings.TrimSuffix(d, descriptionMore), "\n") {
		t.Fatalf("a long body: %d bytes, cut %v, ends %q", len(d), cut, d[len(d)-60:])
	}
	s := linkedStore(t)
	newProject(t, s, "CND")
	key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, long, body))
	tk := ticketOf(t, s, key)
	sn := snapshotOf(t, tk)
	if tk.Title != title || tk.Description != d || sn.Title != long || !sn.BodyTruncated || sn.BodyBytes != len(body) {
		t.Fatalf("the imported ticket: %q, %d bytes, snapshot %+v", tk.Title, len(tk.Description), sn.BodyTruncated)
	}
}

// One live ticket per linked item, on a local write: a second import and a
// link of it are external_linked, naming the ticket; once that ticket is
// deleted or unlinked, it may be linked again.
func TestExternalLinkedOnce(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	read := ghRead("I_1", 0, "T", "B")
	key := mustImport(t, s, "imp", "CND", read)
	rc, err := s.ImportTicket(ctx, testOp("imp2"), ImportArgs{Project: "CND", URL: "x"}, read)
	want(t, rc, err, StatusFailed, RefusalExternalLinked)
	if rc.Key != key {
		t.Fatalf("the refusal names %q, not %s", rc.Key, key)
	}
	_, other := newTicket(t, s, "CND", "Native")
	rc, err = s.LinkExternal(ctx, testOp("l1"), LinkArgs{Key: other, URL: "x"}, read)
	want(t, rc, err, StatusFailed, RefusalExternalLinked)
	rc, err = s.DeleteTicket(ctx, testOp("d1"), key)
	want(t, rc, err, StatusDone, "")
	rc, err = s.LinkExternal(ctx, testOp("l2"), LinkArgs{Key: other, URL: "x"}, read)
	want(t, rc, err, StatusDone, "")
	// A ticket that has a linked issue is unlinked first.
	rc, err = s.LinkExternal(ctx, testOp("l3"), LinkArgs{Key: other, URL: "x"}, ghRead("I_2", 0, "T", "B"))
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if !strings.Contains(rc.Error, "unlink it first") {
		t.Fatalf("the words: %s", rc.Error)
	}
}

// A ticket's source and its owners are the linked item's system, one the
// store was opened with: a store of github and forge imports each as its
// own; a system it was not opened with is invalid; an owner of another
// system than the linked item's is invalid; a store opened with none
// links nothing; a system's name past its shape opens no store.
func TestSourceIsTheSystem(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t, "github", "forge")
	newProject(t, s, "CND")
	gh := mustImport(t, s, "imp1", "CND", ghRead("I_1", 0, "T", "B"))
	fr := ghRead("F_1", 0, "T", "B")
	fr.Snapshot.System, fr.Snapshot.Host, fr.Snapshot.URL = "forge", "forge.example", "https://forge.example/x/y/issues/1"
	fg := mustImport(t, s, "imp2", "CND", fr)
	for key, system := range map[string]string{gh: "github", fg: "forge"} {
		tk := ticketOf(t, s, key)
		a := ParseAuthority(string(tk.Authority))
		if tk.Source != system || a.Title != system || a.Description != system || systemOf(tk.ExternalID) != system {
			t.Fatalf("%s: source %s, owners %+v", key, tk.Source, a)
		}
	}
	other := ghRead("L_1", 0, "T", "B")
	other.Snapshot.System = "linear"
	rc, err := s.ImportTicket(ctx, testOp("imp3"), ImportArgs{Project: "CND", URL: "x"}, other)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, err = s.SetAuthority(ctx, testOp("a1"), AuthorityArgs{Key: gh, Field: FieldTitle, Owner: "forge"}, nil)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	none := openStore(t, storeDir(t))
	newProject(t, none, "CND")
	rc, err = none.ImportTicket(ctx, testOp("imp"), ImportArgs{Project: "CND", URL: "x"}, ghRead("I_1", 0, "T", "B"))
	want(t, rc, err, StatusFailed, RefusalInvalid)
	for _, bad := range []string{"native", "dispatch", "GitHub", strings.Repeat("a", 17), "1abc"} {
		if _, err := OpenOptions(storeDir(t), Options{Systems: []string{bad}}); err == nil {
			t.Fatalf("a store opened with %q", bad)
		}
	}
}

// An item's identity names its host: one node id on two hosts is two
// items, two tickets; a read of one host's item never passes the guard of
// the other's ticket.
func TestExternalIdentityHasHost(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	dotcom := ghRead("I_same", 0, "T", "B")
	ghes := ghRead("I_same", 0, "T", "B")
	ghes.Snapshot.Host, ghes.Snapshot.URL = "ghe.example.com", "https://ghe.example.com/o/r/issues/612"
	k1 := mustImport(t, s, "imp1", "CND", dotcom)
	k2 := mustImport(t, s, "imp2", "CND", ghes)
	if t1, t2 := ticketOf(t, s, k1), ticketOf(t, s, k2); t1.ExternalID == t2.ExternalID || t2.ExternalID != "github:ghe.example.com:I_same" {
		t.Fatalf("the identities: %s %s", t1.ExternalID, t2.ExternalID)
	}
	next := ghes
	next.Snapshot.UpdatedAt = day0.Add(time.Hour)
	next.Snapshot.Title = "Changed"
	rc, err := s.RefreshExternal(ctx, testOp("r1"), k1, next)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if err := s.RecordTicketRead(ctx, k1, ghes.Snapshot.Identity(), TicketRead{UpdatedAt: next.Snapshot.UpdatedAt}); !isRefusal(err, RefusalInvalid) {
		t.Fatalf("a read of the other host's item: %v", err)
	}
	if needed, err := RefreshNeeded(ticketOf(t, s, k1), next); needed || !isRefusal(err, RefusalInvalid) {
		t.Fatalf("RefreshNeeded of the other host's item: %v %v", needed, err)
	}
}

// isRefusal reports whether err is a Refusal of the code.
func isRefusal(err error, code string) bool {
	var r *Refusal
	return errors.As(err, &r) && r.Code == code
}

// A link sets a native ticket's linked issue without a field, every field
// Dispatch's; an unlink empties the four columns (source stays), drops
// ticket_reads, and adopts each field GitHub owned; a ticket with none is
// invalid to unlink. A link's owners name the item and no field.
func TestLinkAndUnlink(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Mine", Description: "My words"})
	read := ghRead("I_1", 0, "GitHub's title", "GitHub's body")
	rc, err := s.LinkExternal(ctx, testOp("l1"), LinkArgs{Key: key, URL: read.Snapshot.URL}, read)
	want(t, rc, err, StatusDone, "")
	tk := ticketOf(t, s, key)
	// Every field Dispatch's, as owners that name the item and no field: a
	// row of its own, so a merge orders the link among the item's owner
	// changes.
	if tk.Title != "Mine" || tk.Description != "My words" || tk.Source != "native" || tk.ExternalID != "github:github.com:I_1" ||
		ParseAuthority(string(tk.Authority)) != (Authority{Of: authorityKey("github:github.com:I_1")}) {
		t.Fatalf("the linked ticket: %+v", tk)
	}
	if got := opRows(t, s, "l1"); !slices.Equal(got, []string{"external_id:set", "external_url:set", "external_snapshot:set", "authority:set"}) {
		t.Fatalf("the link's rows: %v", got)
	}
	v, _ := s.Ticket(ctx, key)
	if v.Owners == nil || *v.Owners != (Owners{Title: OwnerDispatch, Description: OwnerDispatch}) || v.ExternalRead == nil {
		t.Fatalf("the read: %+v %+v", v.Owners, v.ExternalRead)
	}
	rc, err = s.UnlinkExternal(ctx, testOp("u1"), key)
	want(t, rc, err, StatusDone, "")
	tk = ticketOf(t, s, key)
	if tk.ExternalID != "" || tk.ExternalURL != "" || tk.ExternalSnapshot != nil || tk.Authority != nil || tk.Source != "native" {
		t.Fatalf("the unlinked ticket: %+v", tk)
	}
	if n := countOf(t, s, "SELECT COUNT(*) FROM ticket_reads WHERE ticket_id = ?", tk.ID); n != 0 {
		t.Fatalf("%d ticket_reads rows after the unlink", n)
	}
	rc, err = s.UnlinkExternal(ctx, testOp("u2"), key)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	// An imported ticket's unlink adopts both fields with the text they
	// hold, and its source stays github.
	imp := mustImport(t, s, "imp", "CND", ghRead("I_2", 0, "Imported", "Body"))
	rc, err = s.UnlinkExternal(ctx, testOp("u3"), imp)
	want(t, rc, err, StatusDone, "")
	if got := opRows(t, s, "u3"); !slices.Equal(got, []string{"title:adopt", "description:adopt", "external_id:set", "external_url:set", "external_snapshot:set", "authority:set"}) {
		t.Fatalf("the unlink's rows: %v", got)
	}
	adopt := texts(t, s, "SELECT value || '|' || prior FROM changes WHERE op_id = 'u3' AND op = 'adopt' ORDER BY n")
	if !slices.Equal(adopt, []string{"Imported|github", "Body|github"}) {
		t.Fatalf("the adopt rows: %v", adopt)
	}
	if tk := ticketOf(t, s, imp); tk.Source != "github" || tk.Title != "Imported" {
		t.Fatalf("the unlinked import: %+v", tk)
	}
	// Its title is the ticket's own now: an edit takes it.
	title := "Edited"
	mustEdit(t, s, "e1", imp, TicketEdit{Set: TicketSet{Title: &title}})
}

// An edit of a field GitHub owns is external_owned, nothing written; one of
// a field Dispatch owns, or that leaves an owned field as it is, is done.
func TestEditRefusedWhereGitHubOwns(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "Title", "Body"))
	other, same, acceptance := "Other", "Title", "- works"
	rc, err := s.EditTicket(ctx, testOp("e1"), key, TicketEdit{Set: TicketSet{Title: &other}})
	want(t, rc, err, StatusFailed, RefusalExternalOwned)
	if !strings.Contains(rc.Error, "edit it on github and refresh, or take it over") {
		t.Fatalf("the words: %s", rc.Error)
	}
	rc, err = s.EditTicket(ctx, testOp("e2"), key, TicketEdit{Set: TicketSet{Description: &other}})
	want(t, rc, err, StatusFailed, RefusalExternalOwned)
	rc, err = s.EditTicket(ctx, testOp("e3"), key, TicketEdit{Set: TicketSet{Title: &same, Acceptance: &acceptance}})
	want(t, rc, err, StatusDone, "")
	rc, err = s.SetAuthority(ctx, testOp("a1"), AuthorityArgs{Key: key, Field: FieldTitle, Owner: OwnerDispatch}, nil)
	want(t, rc, err, StatusDone, "")
	mustEdit(t, s, "e4", key, TicketEdit{Set: TicketSet{Title: &other}})
	rc, err = s.EditTicket(ctx, testOp("e5"), key, TicketEdit{Set: TicketSet{Description: &other}})
	want(t, rc, err, StatusFailed, RefusalExternalOwned)
}

// A refresh writes the snapshot, the URL when the item moved, and the
// fields GitHub owns (observe rows), never a field Dispatch owns nor the
// ticket's status, whatever GitHub says (a closed issue among it); the
// read's identity must be the ticket's.
func TestRefreshWritesOnlyGitHubFields(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "Title", "Body"))
	rc, err := s.SetAuthority(ctx, testOp("a1"), AuthorityArgs{Key: key, Field: FieldDescription, Owner: OwnerDispatch}, nil)
	want(t, rc, err, StatusDone, "")
	desc := "Dispatch's own words"
	mustEdit(t, s, "e1", key, TicketEdit{Set: TicketSet{Description: &desc}})
	next := ghRead("I_1", 5, "New title", "New body")
	next.Snapshot.State, next.Snapshot.StateReason = "closed", "completed"
	next.Snapshot.URL = "https://github.com/rocksolidlabs/renamed/issues/612"
	next.Snapshot.Labels = []string{"bug", "p1"}
	if needed, err := RefreshNeeded(ticketOf(t, s, key), next); !needed || err != nil {
		t.Fatalf("RefreshNeeded: %v %v", needed, err)
	}
	before := ticketOf(t, s, key)
	rc, err = s.RefreshExternal(ctx, testOp("r1"), key, next)
	want(t, rc, err, StatusDone, "")
	tk := ticketOf(t, s, key)
	if tk.Title != "New title" || tk.Description != desc || tk.Status != before.Status || tk.ExternalURL != next.Snapshot.URL {
		t.Fatalf("the refreshed ticket: %+v", tk)
	}
	sn := snapshotOf(t, tk)
	if sn.State != "closed" || sn.StateReason != "completed" || !slices.Equal(sn.Labels, []string{"bug", "p1"}) || sn.URL != next.Snapshot.URL {
		t.Fatalf("the snapshot: %+v", sn)
	}
	if got := opRows(t, s, "r1"); !slices.Equal(got, []string{"external_url:set", "external_snapshot:set", "title:observe"}) {
		t.Fatalf("the refresh's rows: %v", got)
	}
	// A read of another item is refused by the store's own guard.
	rc, err = s.RefreshExternal(ctx, testOp("r2"), key, ghRead("I_other", 9, "x", "y"))
	want(t, rc, err, StatusFailed, RefusalInvalid)
	// A native ticket that has none.
	_, native := newTicket(t, s, "CND", "Native")
	rc, err = s.RefreshExternal(ctx, testOp("r3"), native, next)
	want(t, rc, err, StatusFailed, RefusalInvalid)
}

// A read that finds nothing new (a 304, or a 200 whose content is
// unchanged) adds nothing to the history: RefreshNeeded says false, and
// RecordTicketRead writes ticket_reads alone, no changes row and no
// receipt; so on a CRLF body, a title past 200 characters and a body past
// 64 KiB, whose stored fields are FromExternal's and not GitHub's raw text.
func TestRefreshNothingNewWritesNoChange(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	body := strings.Repeat("line of the body\r\n", 5000) // CRLF, past 64 KiB
	title := strings.Repeat("t", 250)
	first := ghRead("I_1", 0, title, body)
	key := mustImport(t, s, "imp", "CND", first)
	rows, receipts := countOf(t, s, "SELECT COUNT(*) FROM changes"), countOf(t, s, "SELECT COUNT(*) FROM ops")
	same := first
	same.IssueETag = `"etag-new"`
	if needed, err := RefreshNeeded(ticketOf(t, s, key), same); needed || err != nil {
		t.Fatalf("an unchanged 200: needed %v %v", needed, err)
	}
	reads := 0
	s.OnRead(func(ReadNotice) { reads++ })
	if err := s.RecordTicketRead(ctx, key, first.Snapshot.Identity(), TicketRead{UpdatedAt: first.Snapshot.UpdatedAt, ReadBy: "Alice", IssueETag: `"etag-new"`}); err != nil {
		t.Fatal(err)
	}
	// A 304: no time, the held one kept.
	if err := s.RecordTicketRead(ctx, key, first.Snapshot.Identity(), TicketRead{ReadBy: "Alice", IssueETag: `"etag-new"`}); err != nil {
		t.Fatal(err)
	}
	if n, m := countOf(t, s, "SELECT COUNT(*) FROM changes"), countOf(t, s, "SELECT COUNT(*) FROM ops"); n != rows || m != receipts || reads != 2 {
		t.Fatalf("after the reads: %d rows (was %d), %d receipts (was %d), %d reads told", n, rows, m, receipts, reads)
	}
	v, _ := s.Ticket(ctx, key)
	if v.ExternalRead == nil || v.ExternalRead.IssueETag != `"etag-new"` || v.ExternalRead.ReadBy != "Alice" || !v.ExternalRead.UpdatedAt.Equal(first.Snapshot.UpdatedAt) {
		t.Fatalf("ticket_reads: %+v", v.ExternalRead)
	}
	// A body changed only from CRLF to LF is the same description; the
	// snapshot (its body's digest) is what changes.
	lf := first
	lf.Body = strings.ReplaceAll(body, "\r\n", "\n")
	lf.Snapshot.UpdatedAt = day0.Add(time.Minute)
	if needed, _ := RefreshNeeded(ticketOf(t, s, key), lf); !needed {
		t.Fatalf("a body's new digest needs a refresh")
	}
	rc, err := s.RefreshExternal(ctx, testOp("r1"), key, lf)
	want(t, rc, err, StatusDone, "")
	if got := opRows(t, s, "r1"); !slices.Equal(got, []string{"external_snapshot:set"}) {
		t.Fatalf("a CRLF-only change's rows: %v", got)
	}
}

// A read older by GitHub's clock than the one kept is stale_read, nothing
// written: a refresh's, a ticket read's and a link's.
func TestStaleReadDropped(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	key := mustImport(t, s, "imp", "CND", ghRead("I_1", 10, "Title", "Body"))
	old := ghRead("I_1", 5, "Older title", "Body")
	rc, err := s.RefreshExternal(ctx, testOp("r1"), key, old)
	want(t, rc, err, StatusFailed, RefusalStaleRead)
	if tk := ticketOf(t, s, key); tk.Title != "Title" {
		t.Fatalf("a stale read wrote %q", tk.Title)
	}
	if err := s.RecordTicketRead(ctx, key, old.Snapshot.Identity(), TicketRead{UpdatedAt: old.Snapshot.UpdatedAt}); !isRefusal(err, RefusalStaleRead) {
		t.Fatalf("a stale ticket read: %v", err)
	}
	rc, err = addLink(s, "l1", key, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/9"})
	want(t, rc, err, StatusDone, "")
	link := rc.ID
	if err := s.RecordRead(ctx, link, prRead("PR_9", 10, "open")); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRead(ctx, link, prRead("PR_9", 5, "merged")); !isRefusal(err, RefusalStaleRead) {
		t.Fatalf("a stale link read: %v", err)
	}
	if r, ok, err := s.LinkReadOf(ctx, link); err != nil || !ok || r.State != "open" {
		t.Fatalf("the link's read: %+v %v %v", r, ok, err)
	}
}

// prRead is a read of a pull request with the node id, updated at minute m
// of day0, in the state (merged: merged with its commit), with two checks.
func prRead(node string, m int, state string) LinkRead {
	r := LinkRead{System: "github", Host: "github.com", Kind: ExternalChange, NodeID: node, State: state, HeadSha: strings.Repeat("a", 40),
		UpdatedAt: day0.Add(time.Duration(m) * time.Minute), PullETag: `"p"`,
		Checks: []LinkCheck{{ID: "1", Name: "build", Status: "completed", Conclusion: "success", App: "github-actions", HasLog: true},
			{ID: "2", Name: "lint", Status: "in_progress"}}}
	if state == "merged" {
		r.Merged, r.MergedAt, r.MergeSha = true, day0.Add(time.Duration(m)*time.Minute), strings.Repeat("b", 40)
	}
	return r
}

// A refresh that changes a field GitHub owns moves the ticket's intent, so
// a revision approved before it is stale.
func TestRefreshMakesApprovalStale(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "Title", "Body"))
	plan := simplePlan(t, s, key)
	if _, stale := approvedOf(t, s, key); len(stale) != 1 || stale[0] != nil {
		t.Fatalf("the fresh approval: %v", stale)
	}
	rc, err := s.RefreshExternal(ctx, testOp("r1"), key, ghRead("I_1", 5, "Title", "A new body"))
	want(t, rc, err, StatusDone, "")
	ids, stale := approvedOf(t, s, key)
	if len(ids) != 1 || ids[0] != plan || stale[0] == nil || stale[0].Remedy != RemedyApprove {
		t.Fatalf("the approval after the refresh: %v %+v", ids, stale)
	}
}

// Taken over with no edit, a field gets an adopt row of the text it holds;
// given back after GitHub changed meanwhile, the fresh text is written with
// its snapshot, an observe row; given back with no read, or to the owner
// it has, is invalid.
func TestTakeOverAndGiveBack(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "Title", "Body"))
	rc, err := s.SetAuthority(ctx, testOp("t1"), AuthorityArgs{Key: key, Field: FieldDescription, Owner: OwnerDispatch}, nil)
	want(t, rc, err, StatusDone, "")
	if got := opRows(t, s, "t1"); !slices.Equal(got, []string{"authority:set", "description:adopt"}) {
		t.Fatalf("the take over's rows: %v", got)
	}
	if v := texts(t, s, "SELECT value || '|' || prior FROM changes WHERE op_id = 't1' AND op = 'adopt'"); !slices.Equal(v, []string{"Body|github"}) {
		t.Fatalf("the adopt row: %v", v)
	}
	rc, err = s.SetAuthority(ctx, testOp("t2"), AuthorityArgs{Key: key, Field: FieldDescription, Owner: OwnerDispatch}, nil)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, err = s.SetAuthority(ctx, testOp("g0"), AuthorityArgs{Key: key, Field: FieldDescription, Owner: "github"}, nil)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	mine := "Mine now"
	mustEdit(t, s, "e1", key, TicketEdit{Set: TicketSet{Description: &mine}})
	// GitHub's body changed while Dispatch owned it.
	fresh := ghRead("I_1", 7, "Title", "GitHub's new body")
	rc, err = s.SetAuthority(ctx, testOp("g1"), AuthorityArgs{Key: key, Field: FieldDescription, Owner: "github"}, &fresh)
	want(t, rc, err, StatusDone, "")
	tk := ticketOf(t, s, key)
	if tk.Description != "GitHub's new body" || snapshotOf(t, tk).BodySha256 != sha256Hex("GitHub's new body") ||
		ParseAuthority(string(tk.Authority)).Description != "github" {
		t.Fatalf("given back: %+v", tk)
	}
	if got := opRows(t, s, "g1"); !slices.Contains(got, "description:observe") || !slices.Contains(got, "external_snapshot:set") {
		t.Fatalf("the give back's rows: %v", got)
	}
	rc, err = s.SetAuthority(ctx, testOp("g2"), AuthorityArgs{Key: key, Field: FieldTitle, Owner: "bogus"}, nil)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, err = s.SetAuthority(ctx, testOp("g3"), AuthorityArgs{Key: key, Field: "status", Owner: OwnerDispatch}, nil)
	want(t, rc, err, StatusFailed, RefusalInvalid)
}

// A read is not a logged change: RecordRead, RecordReadError and
// RecordTicketRead add no changes row and no receipt, tell OnRead and not
// OnChange, and the ticket's read carries the link's summary.
func TestReadsAreNotLogged(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "Title", "Body"))
	rc, err := addLink(s, "l1", key, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/9"})
	want(t, rc, err, StatusDone, "")
	link := rc.ID
	changes := listen(s)
	var reads []ReadNotice
	s.OnRead(func(n ReadNotice) { reads = append(reads, n) })
	rows, receipts := countOf(t, s, "SELECT COUNT(*) FROM changes"), countOf(t, s, "SELECT COUNT(*) FROM ops")
	if err := s.RecordRead(ctx, link, prRead("PR_9", 1, "open")); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReadError(ctx, link, "GitHub said 502\x00 Bad Gateway"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTicketRead(ctx, key, "github:github.com:I_1", TicketRead{}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTicketReadError(ctx, key, "github:github.com:I_1", "rate limited"); err != nil {
		t.Fatal(err)
	}
	if n, m := countOf(t, s, "SELECT COUNT(*) FROM changes"), countOf(t, s, "SELECT COUNT(*) FROM ops"); n != rows || m != receipts || len(*changes) != 0 {
		t.Fatalf("reads logged: %d rows (was %d), %d receipts (was %d), %d OnChange calls", n, rows, m, receipts, len(*changes))
	}
	tid := ticketOf(t, s, key).ID
	if len(reads) != 4 || reads[0] != (ReadNotice{Ticket: tid, Project: "CND", Link: link, ReadAt: reads[0].ReadAt}) || reads[2].Link != "" || reads[0].ReadAt.IsZero() {
		t.Fatalf("OnRead: %+v", reads)
	}
	v, err := s.Ticket(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	sum, ok := v.LinkReads[link]
	if !ok || sum.State != "open" || sum.Checks != (ChecksSummary{Passed: 1, Pending: 1}) || sum.Error != "GitHub said 502 Bad Gateway" {
		t.Fatalf("the link's summary: %+v %v", sum, ok)
	}
	if v.ExternalRead == nil || v.ExternalRead.Error != "rate limited" {
		t.Fatalf("the ticket's read: %+v", v.ExternalRead)
	}
	// An incomplete set says so in its summary, never all passed.
	r := prRead("PR_9", 2, "open")
	r.Checks = r.Checks[:1]
	r.Incomplete = &ChecksIncomplete{Words: []string{"other apps' check runs need a classic token"}, Omitted: 3}
	if err := s.RecordRead(ctx, link, r); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Ticket(ctx, key)
	if c := v.LinkReads[link].Checks; c != (ChecksSummary{Passed: 1, Incomplete: true, Omitted: 3}) {
		t.Fatalf("an incomplete set's summary: %+v", c)
	}
	full, ok, err := s.LinkReadOf(ctx, link)
	if err != nil || !ok || len(full.Checks) != 1 || full.Incomplete == nil || full.Incomplete.Omitted != 3 || full.Error != "" {
		t.Fatalf("the link's whole read: %+v %v %v", full, ok, err)
	}
}

// A link removed drops its read in the remove's transaction, and a read
// for a link since removed is dropped (not_found).
func TestRemovedLinkDropsRead(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "T")
	rc, err := addLink(s, "l1", key, NewLink{Kind: LinkExternal, URL: "https://github.com/rocksolidlabs/conductor/issues/3"})
	want(t, rc, err, StatusDone, "")
	link := rc.ID
	read := prRead("I_3", 1, "open")
	read.Kind = ExternalIssue
	if err := s.RecordRead(ctx, link, read); err != nil {
		t.Fatal(err)
	}
	rc, err = s.RemoveLink(ctx, testOp("rm"), key, link)
	want(t, rc, err, StatusDone, "")
	if n := countOf(t, s, "SELECT COUNT(*) FROM link_reads WHERE link_id = ?", link); n != 0 {
		t.Fatalf("%d reads of a removed link", n)
	}
	if err := s.RecordRead(ctx, link, read); !isRefusal(err, RefusalNotFound) {
		t.Fatalf("a read after the remove: %v", err)
	}
	if _, ok, err := s.LinkReadOf(ctx, link); ok || err != nil {
		t.Fatalf("the removed link's read: %v %v", ok, err)
	}
	if v, _ := s.Ticket(ctx, key); len(v.LinkReads) != 0 {
		t.Fatalf("the ticket's reads: %+v", v.LinkReads)
	}
}

// The close policy's merged: met by a live delivery read merged at most
// five minutes ago, the close writing closed_on with the read; not by an
// old read, a pull request closed unmerged, or an external link read
// merged. In this row a project's policy refuses merged (the test sets it
// by hand, as a replica's row would).
func TestCloseRequiresMerged(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	rc, err := s.CreateProject(ctx, testOp("p0"), NewProject{Key: "NOP", Name: "No", CloseRequires: &[]string{RequireMerged}})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	newProject(t, s, "CND")
	rc, err = s.EditProject(ctx, testOp("p1"), "CND", ProjectEdit{CloseRequires: &[]string{RequireDelivery, RequireMerged}})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if _, err := s.db.Exec(`UPDATE projects SET close_requires = '["merged"]' WHERE key = 'CND'`); err != nil {
		t.Fatal(err)
	}
	done := TicketDone
	close := func(opID, key string) Receipt {
		t.Helper()
		rc, err := s.EditTicket(ctx, testOp(opID), key, TicketEdit{Set: TicketSet{Status: &done}})
		if err != nil {
			t.Fatal(err)
		}
		return rc
	}
	_, key := newTicket(t, s, "CND", "Ship it")
	if rc := close("c0", key); rc.Refusal != RefusalClosePolicy || !slices.Equal(rc.Missing, []string{RequireMerged}) {
		t.Fatalf("no delivery: %+v", rc)
	}
	// An external link read merged is no delivery.
	rc, err = addLink(s, "x1", key, NewLink{Kind: LinkExternal, URL: "https://github.com/rocksolidlabs/conductor/pull/8"})
	want(t, rc, err, StatusDone, "")
	if err := s.RecordRead(ctx, rc.ID, prRead("PR_8", 1, "merged")); err != nil {
		t.Fatal(err)
	}
	if rc := close("c1", key); rc.Refusal != RefusalClosePolicy {
		t.Fatalf("an external link: %+v", rc)
	}
	rc, err = addLink(s, "d1", key, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/9"})
	want(t, rc, err, StatusDone, "")
	delivery := rc.ID
	// Closed unmerged.
	if err := s.RecordRead(ctx, delivery, prRead("PR_9", 1, "closed")); err != nil {
		t.Fatal(err)
	}
	if rc := close("c2", key); rc.Refusal != RefusalClosePolicy || !strings.Contains(rc.Error, "is closed, not merged") {
		t.Fatalf("closed unmerged: %+v", rc)
	}
	// Merged, but read too long ago.
	if err := s.RecordRead(ctx, delivery, prRead("PR_9", 2, "merged")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE link_reads SET read_at = ? WHERE link_id = ?", stamp(time.Now().Add(-MaxMergedReadAge-time.Minute)), delivery); err != nil {
		t.Fatal(err)
	}
	if st, err := s.CloseState(ctx, key); err != nil || st.Met.Merged || !strings.Contains(st.MergedWhy, "read it again") {
		t.Fatalf("an old read's state: %+v %v", st, err)
	}
	if rc := close("c3", key); rc.Refusal != RefusalClosePolicy {
		t.Fatalf("an old read: %+v", rc)
	}
	// Read again now: met, and closed_on holds the read.
	if err := s.RecordRead(ctx, delivery, prRead("PR_9", 3, "merged")); err != nil {
		t.Fatal(err)
	}
	if rc := close("c4", key); rc.Status != StatusDone {
		t.Fatalf("met: %+v", rc)
	}
	var on ClosedOn
	tk, err := readTicket(ctx, s.db, key)
	if err != nil || json.Unmarshal(tk.ClosedOn, &on) != nil || on.LinkID != delivery || on.MergeSha != strings.Repeat("b", 40) ||
		on.ReadAt.IsZero() || strings.Contains(string(tk.ClosedOn), "https://") {
		t.Fatalf("closed_on: %s %v", tk.ClosedOn, err)
	}
	if got := opRows(t, s, "c4"); !slices.Contains(got, "closed_on:set") {
		t.Fatalf("the close's rows: %v", got)
	}
}

// An image of the manifest is made an attachment in one transaction with
// its manifest entry; a refusal keeps neither; an entry that names an
// attachment, or an image not in the manifest, is refused; a skip is
// recorded the same way.
func TestImportImageAtomic(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	read := ghRead("I_1", 0, "Title", "Body")
	key := mustImport(t, s, "imp", "CND", read)
	a1, a2 := read.Snapshot.Images[0].ID, read.Snapshot.Images[1].ID
	png := []byte("\x89PNG\r\n\x1a\n image")
	rc, err := s.ImportImage(ctx, testOp(DerivedOpID("imp", "image", a1, sha256Hex(string(png)))), key, a1,
		ImageUpload{Name: "github-1.png", ContentType: "image/png", Body: bytes.NewReader(png)})
	want(t, rc, err, StatusDone, "")
	tk := ticketOf(t, s, key)
	sn := snapshotOf(t, tk)
	if sn.Images[0].AttachmentID != rc.ID || sn.Images[0].SHA256 != sha256Hex(string(png)) || sn.Images[1].done() {
		t.Fatalf("the manifest: %+v", sn.Images)
	}
	if got := texts(t, s, "SELECT field || ':' || op FROM changes WHERE op_id = ? ORDER BY n", rc.OpID); !slices.Equal(got, []string{"attachments:add", "external_snapshot:set"}) {
		t.Fatalf("the image's rows: %v", got)
	}
	attachments := countOf(t, s, "SELECT COUNT(*) FROM attachments")
	// Done already: refused, nothing kept.
	rc, err = s.ImportImage(ctx, testOp("again"), key, a1, ImageUpload{Name: "x.png", ContentType: "image/png", Body: bytes.NewReader([]byte("other"))})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, err = s.ImportImage(ctx, testOp("stranger"), key, "https://github.com/user-attachments/assets/zz", ImageUpload{Name: "x.png", Body: bytes.NewReader(png)})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if n := countOf(t, s, "SELECT COUNT(*) FROM attachments"); n != attachments {
		t.Fatalf("%d attachments after the refusals, was %d", n, attachments)
	}
	if after := snapshotOf(t, ticketOf(t, s, key)); !slices.Equal(after.Images, sn.Images) {
		t.Fatalf("a refusal changed the manifest: %+v", after.Images)
	}
	// A skip: recorded, never fetched again, and no attachment.
	rc, err = s.ImportImage(ctx, testOp("skip"), key, a2, ImageUpload{Skipped: SkippedType})
	want(t, rc, err, StatusDone, "")
	if after := snapshotOf(t, ticketOf(t, s, key)); after.Images[1] != (SnapshotImage{ID: a2, Skipped: SkippedType}) {
		t.Fatalf("the skip: %+v", after.Images[1])
	}
	rc, err = s.ImportImage(ctx, testOp("skip2"), key, a2, ImageUpload{Skipped: SkippedSize})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	// A refresh keeps the done entries by image id, and an image the body no
	// longer has stays in the manifest.
	next := ghRead("I_1", 3, "Title", "Body")
	next.Snapshot.Images = []SnapshotImage{{ID: "https://github.com/user-attachments/assets/a3"}, {ID: a1}}
	rc, err = s.RefreshExternal(ctx, testOp("r1"), key, next)
	want(t, rc, err, StatusDone, "")
	got := snapshotOf(t, ticketOf(t, s, key)).Images
	if len(got) != 3 || got[0] != sn.Images[0] || got[1].Skipped != SkippedType || got[2] != (SnapshotImage{ID: "https://github.com/user-attachments/assets/a3"}) {
		t.Fatalf("the manifest after a refresh: %+v", got)
	}
}

// A resend of an import with the same op id is a replay, whatever its read
// now holds (a changed body, images reordered): never op_conflict, nothing
// written again; once its receipt is pruned, it is answered by the
// ticket's create row with the ticket's id and key, its content compared
// with the import's.
func TestImportManifestReplay(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	first := ghRead("I_1", 0, "Title", "Body")
	args := ImportArgs{Project: "CND", URL: first.Snapshot.URL}
	rc, err := s.ImportTicket(ctx, testOp("imp"), args, first)
	want(t, rc, err, StatusDone, "")
	rows := countOf(t, s, "SELECT COUNT(*) FROM changes")
	again := ghRead("I_1", 9, "Title changed", "Body changed")
	slices.Reverse(again.Snapshot.Images)
	if replay, found, err := s.Replay(ctx, testOp("imp"), IntentTicketImport, args); err != nil || !found || replay.Key != rc.Key {
		t.Fatalf("the replay: %+v %v %v", replay, found, err)
	}
	re, err := s.ImportTicket(ctx, testOp("imp"), args, again)
	if err != nil || re.Status != StatusDone || !re.Repeat || re.Key != rc.Key {
		t.Fatalf("the resend: %+v %v", re, err)
	}
	if n := countOf(t, s, "SELECT COUNT(*) FROM changes"); n != rows {
		t.Fatalf("the resend wrote %d rows", n-rows)
	}
	// Another request under the op id is a conflict.
	re, err = s.ImportTicket(ctx, testOp("imp"), ImportArgs{Project: "CND", URL: "other"}, again)
	want(t, re, err, StatusFailed, RefusalOpConflict)
	// Its receipt pruned, the resend finds the ticket again by its create
	// row (R1), never makes a second, and other content is still a
	// conflict.
	if _, err := s.db.Exec("DELETE FROM ops WHERE actor = 'test' AND id = 'imp'"); err != nil {
		t.Fatal(err)
	}
	tk := ticketOf(t, s, rc.Key)
	if replay, found, err := s.Replay(ctx, testOp("imp"), IntentTicketImport, args); err != nil || !found || replay.Status != StatusDone ||
		replay.ID != tk.ID || replay.Key != rc.Key {
		t.Fatalf("the replay once pruned: %+v %v %v", replay, found, err)
	}
	re, err = s.ImportTicket(ctx, testOp("imp"), args, again)
	if err != nil || re.Status != StatusDone || !re.Repeat || re.ID != tk.ID || re.Key != rc.Key {
		t.Fatalf("the resend once pruned: %+v %v", re, err)
	}
	if n := countOf(t, s, "SELECT COUNT(*) FROM changes"); n != rows {
		t.Fatalf("the resend once pruned wrote %d rows", n-rows)
	}
	re, err = s.ImportTicket(ctx, testOp("imp"), ImportArgs{Project: "CND", URL: "other"}, again)
	want(t, re, err, StatusFailed, RefusalOpConflict)
	// A ticket.create's op id resent as an import is another operation's.
	_, native := newTicket(t, s, "CND", "Native")
	_ = native
	if _, err := s.db.Exec("DELETE FROM ops WHERE actor = 'test'"); err != nil {
		t.Fatal(err)
	}
	var createOp string
	if err := s.db.QueryRow("SELECT op_id FROM changes WHERE entity = 'ticket' AND op = 'create' AND entity_id = ?", ticketOf(t, s, native).ID).Scan(&createOp); err != nil {
		t.Fatal(err)
	}
	re, err = s.ImportTicket(ctx, testOp(createOp), args, again)
	want(t, re, err, StatusFailed, RefusalOpConflict)
}

// A derived op id is "~" and 31 hex digits whatever its parent, matches
// OpIDPattern, differs for every phase and list of parts, and two lists of
// parts that join to one string give two ids.
func TestDerivedOpIDs(t *testing.T) {
	parent := strings.Repeat("Z", 32)
	seen := map[string][]string{}
	for _, parts := range [][]string{
		{"image", "https://github.com/user-attachments/assets/a1", sha256Hex("a")},
		{"image", "https://github.com/user-attachments/assets/a1", sha256Hex("b")},
		{"image", "https://github.com/user-attachments/assets/a2", sha256Hex("a")},
		{"settle", "3", sha256Hex("{}")},
		{"settle", "4", sha256Hex("{}")},
		{"close", "3", "begin"},
		{"a", "bc"}, {"ab", "c"}, {"abc"}, {"a", "b", "c"}, {""}, {},
	} {
		id := DerivedOpID(parent, parts...)
		if len(id) != 32 || id[0] != '~' || !OpIDPattern.MatchString(id) {
			t.Fatalf("%v: %q", parts, id)
		}
		if other, ok := seen[id]; ok {
			t.Fatalf("%v and %v give one id", parts, other)
		}
		seen[id] = parts
	}
	if DerivedOpID("ab", "c") == DerivedOpID("a", "bc") {
		t.Fatal("the parent and its parts are not length-prefixed")
	}
	if one, again := DerivedOpID("x", "y"), DerivedOpID("x", "y"); one != again {
		t.Fatal("one input gave two ids")
	}
}

// Query.External finds the tickets whose linked issue's page, or one of
// whose live links' URL, is the URL (the page compared as the store keeps
// an address, a pasted link's query and fragment left behind); a removed
// link's URL finds none; a URL past its bound is a bad query.
func TestQueryExternal(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	read := ghRead("I_1", 0, "Imported", "B")
	imported := mustImport(t, s, "imp", "CND", read)
	_, linked := newTicket(t, s, "CND", "Linked")
	pr := "https://github.com/rocksolidlabs/conductor/pull/5"
	rc, err := addLink(s, "l1", linked, NewLink{Kind: LinkDelivery, URL: pr})
	want(t, rc, err, StatusDone, "")
	_, gone := newTicket(t, s, "CND", "Gone")
	old := "https://github.com/rocksolidlabs/conductor/pull/4"
	rc, err = addLink(s, "l2", gone, NewLink{Kind: LinkDelivery, URL: old})
	want(t, rc, err, StatusDone, "")
	rc, err = s.RemoveLink(ctx, testOp("rm"), gone, rc.ID)
	want(t, rc, err, StatusDone, "")
	for url, keys := range map[string][]string{read.Snapshot.URL: {imported}, read.Snapshot.URL + "?notification_referrer_id=1#issuecomment-2": {imported},
		pr: {linked}, old: {}, "https://example.com/none": {}} {
		page, err := s.List(ctx, Query{External: url})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, tk := range page.Tickets {
			got = append(got, tk.Key)
		}
		if !slices.Equal(got, keys) && !(len(got) == 0 && len(keys) == 0) {
			t.Fatalf("%s finds %v, want %v", url, got, keys)
		}
	}
	if _, err := s.List(ctx, Query{External: strings.Repeat("a", MaxLinkURL+1)}); !errors.Is(err, ErrBadQuery) {
		t.Fatalf("a URL past its bound: %v", err)
	}
}
