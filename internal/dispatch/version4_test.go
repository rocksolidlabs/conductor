package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The field rules of version 4 (rules.go): source and closed_on scalars,
// the four counterpart columns counterpart (two rows of one kept both, for
// ReconcileCounterpart), every column of link_reads, ticket_reads and
// store_instance derived; an observe row makes the ticket's search entry
// stale as a set does, an adopt row none.
func TestRulesClassesVersionFour(t *testing.T) {
	s := openStore(t, storeDir(t))
	for _, f := range []string{"source", "closed_on"} {
		if c := Class(EntityTicket, f); c != ClassScalar {
			t.Errorf("Class(ticket, %s) = %q", f, c)
		}
	}
	for _, f := range []string{"external_id", "external_url", "external_snapshot", "authority"} {
		if c := Class(EntityTicket, f); c != ClassCounterpart {
			t.Errorf("Class(ticket, %s) = %q", f, c)
		}
		a := ChangeRow{Entity: EntityTicket, EntityID: "t", Field: f, Op: OpSet, At: day0, Actor: "a", OpID: "1"}
		b := ChangeRow{Entity: EntityTicket, EntityID: "t", Field: f, Op: OpSet, At: day0.Add(time.Second), Actor: "b", OpID: "2"}
		if Merge(a, b) != KeepBoth || Merge(b, a) != KeepBoth {
			t.Errorf("two rows of %s: %v %v", f, Merge(a, b), Merge(b, a))
		}
	}
	for _, table := range []string{"link_reads", "ticket_reads", "store_instance"} {
		for _, col := range columnsOf(t, s.db, table) {
			if c := Class(table, col); c != ClassDerived {
				t.Errorf("Class(%s, %s) = %q", table, col, c)
			}
		}
	}
	if c := Class("link_read", "checks"); c != ClassDerived {
		t.Errorf("Class(link_read, checks) = %q", c)
	}
	// Every column of tickets is classed, closed_on among them.
	for _, col := range columnsOf(t, s.db, "tickets") {
		if col == "rev" || col == "updated_at" {
			continue
		}
		if Class(EntityTicket, col) == "" {
			t.Errorf("the ticket's %s has no class", col)
		}
	}
	for _, c := range []struct {
		op, field string
		entry     bool
	}{
		{OpObserve, "title", true}, {OpObserve, "description", true}, {OpAdopt, "title", false}, {OpAdopt, "description", false},
		{OpSet, "title", true}, {OpSet, "external_snapshot", false}, {OpObserve, "external_snapshot", false},
	} {
		_, ok := searchedBy(ChangeRow{Entity: EntityTicket, EntityID: "t", Field: c.field, Op: c.op})
		if ok != c.entry {
			t.Errorf("searchedBy(%s %s) = %v, want %v", c.op, c.field, ok, c.entry)
		}
	}
}

// worst is a text of n bytes at its escaped worst as the store and the API
// encode it (encodeValue, writeDispatch: Go's JSON with HTML escaping off):
// '"' is two bytes.
func worst(n int) string { return strings.Repeat(`"`, n) }

// worstWords is a text of n characters at its escaped worst as the store
// and the API encode it where the text is trimmed (a read's words, a
// reader's name): U+2028 (worstChars) between two letters, since a trim
// takes it off the ends.
func worstWords(n int) string { return "a" + strings.Repeat("\u2028", n-2) + "a" }

// worstSnapshot is a snapshot with every part at its bound, every text at
// its escaped worst by the encoding worst names, as normalSnapshot leaves
// it.
func worstSnapshot(t *testing.T, worst func(int) string) ExternalSnapshot {
	t.Helper()
	host := strings.Repeat("h", MaxExternalHost)
	labels, assignees := make([]string, MaxSnapshotLabels+5), make([]string, MaxSnapshotAssignees+5)
	for i := range labels {
		labels[i] = worst(MaxSnapshotLabel)
	}
	for i := range assignees {
		assignees[i] = worst(MaxSnapshotLogin)
	}
	// An image's id is an address the store keeps: scheme, host and path,
	// which JSON writes as they are.
	var images []SnapshotImage
	for i := 0; i < MaxImportImages+3; i++ {
		id := "https://" + host + "/" + string(rune('a'+i))
		images = append(images, SnapshotImage{ID: id + strings.Repeat("p", MaxImageID-len(id))})
	}
	url := "https://" + host + "/" + strings.Repeat("&", MaxExternalURL-len("https://")-len(host)-1)
	read := ExternalRead{
		Snapshot: ExternalSnapshot{V: SnapshotVersion, System: strings.Repeat("s", 16), Host: host, Space: worst(MaxSnapshotSpace), Kind: ExternalChange,
			ID: strings.Repeat("9", MaxSnapshotItemID), NodeID: strings.Repeat("N", MaxNodeID), DatabaseID: strings.Repeat("9", MaxRemoteID), URL: url,
			Title: worst(MaxSnapshotTitle), State: "merged", StateReason: "not_planned", Merged: true, MergedAt: day0, MergeSha: strings.Repeat("a", 64),
			HeadSha: strings.Repeat("b", 64), BaseRef: worst(MaxSnapshotName), Labels: labels, LabelsTotal: 1 << 30, Assignees: assignees,
			Milestone: worst(MaxSnapshotName), Author: worst(MaxSnapshotLogin), AuthorID: strings.Repeat("7", MaxRemoteID), CreatedAt: day0,
			UpdatedAt: day0, ClosedAt: day0, Repro: strings.Repeat(worst(79)+"\n", MaxRepro/40), BodyTruncated: true, Images: images},
		Body: worst(256 << 10),
	}
	sn, _, _, err := normalSnapshot(read)
	if err != nil {
		t.Fatal(err)
	}
	if len(sn.Images) != MaxImportImages || len(sn.Labels) != MaxSnapshotLabels || len(sn.Assignees) != MaxSnapshotAssignees || len(sn.Repro) > MaxRepro ||
		len(sn.Repro) < MaxRepro-80 {
		t.Fatalf("the snapshot was not cut to its bounds: %d images, %d labels, %d assignees, repro %d", len(sn.Images), len(sn.Labels), len(sn.Assignees), len(sn.Repro))
	}
	for i := range sn.Images {
		sn.Images[i].AttachmentID, sn.Images[i].SHA256, sn.Images[i].Skipped = strings.Repeat("f", IDLen), strings.Repeat("e", 64), SkippedType
	}
	return sn
}

// A snapshot with every part at its bound is under MaxExternalSnapshot as
// stored (the store's encoding) and fits a change row; one past its bound
// is refused; the identity's parts past their shapes are invalid.
func TestSnapshotBound(t *testing.T) {
	sn := worstSnapshot(t, worst)
	v, err := encodeSnapshot(sn)
	if err != nil {
		t.Fatalf("the worst snapshot: %v", err)
	}
	t.Logf("the worst snapshot is %d bytes as stored, its bound %d", len(v), MaxExternalSnapshot)
	if len(v) > MaxChangeValue {
		t.Fatalf("the snapshot does not fit a change row")
	}
	// Past the bound (a manifest a merge or a later build made longer).
	big := sn
	big.Images = append(big.Images, sn.Images...)
	big.Images = append(big.Images, sn.Images...)
	if _, err := encodeSnapshot(big); !isRefusal(err, RefusalBound) {
		t.Fatalf("a snapshot past its bound: %v", err)
	}
	for name, mutate := range map[string]func(*ExternalRead){
		"a host past 253 bytes": func(r *ExternalRead) { r.Snapshot.Host = strings.Repeat("h", MaxExternalHost+1) },
		"an upper-case host":    func(r *ExternalRead) { r.Snapshot.Host = "GitHub.com" },
		"a node id of 101":      func(r *ExternalRead) { r.Snapshot.NodeID = strings.Repeat("N", MaxNodeID+1) },
		"a node id with ':'":    func(r *ExternalRead) { r.Snapshot.NodeID = "I:1" },
		"a remote id of 65":     func(r *ExternalRead) { r.Snapshot.DatabaseID = strings.Repeat("9", MaxRemoteID+1) },
		"an http URL":           func(r *ExternalRead) { r.Snapshot.URL = "http://github.com/o/r/issues/1" },
		"no update time":        func(r *ExternalRead) { r.Snapshot.UpdatedAt = time.Time{} },
		"a short commit":        func(r *ExternalRead) { r.Snapshot.HeadSha = "abc" },
		"a validator of 201":    func(r *ExternalRead) { r.IssueETag = strings.Repeat("e", MaxValidator+1) },
		"version 2":             func(r *ExternalRead) { r.Snapshot.V = 2 },
	} {
		read := ghRead("I_1", 0, "T", "B")
		mutate(&read)
		if _, _, _, err := normalSnapshot(read); !isRefusal(err, RefusalInvalid) && !isRefusal(err, RefusalBound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A loopback host's http URL is the tests' fake's.
	read := ghRead("I_1", 0, "T", "B")
	read.Snapshot.Host, read.Snapshot.URL = "127.0.0.1:8099", "http://127.0.0.1:8099/o/r/issues/1"
	if _, _, _, err := normalSnapshot(read); err != nil {
		t.Fatalf("a loopback host: %v", err)
	}
	// Two reads of an unchanged item make one snapshot.
	a, _, _, _ := normalSnapshot(ghRead("I_1", 0, "T", "B"))
	b, _, _, _ := normalSnapshot(ghRead("I_1", 0, "T", "B"))
	va, _ := encodeSnapshot(a)
	vb, _ := encodeSnapshot(b)
	if va != vb {
		t.Fatal("two reads of one item made two snapshots")
	}
}

// The parts of a ticket's read this version adds (the snapshot, the
// owners, the linked issue's read, closed_on and a summary of each live
// link's read), each at its bound and at its escaped worst as the route
// encodes it (encodeValue is writeDispatch's encoding: HTML escaping off,
// so '"' for a text bounded in bytes and U+2028 for one bounded in
// characters), are under MaxTicketExternalParts, and that bound is the
// measure rounded up to a KiB or two, not a guess above it (the room the
// API's MaxTicketReply leaves the later rows depends on it); each summary is
// under MaxLinkReadSummary; a link's read past its bounds is refused.
func TestTicketReplyBound(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "T", "B"))
	tk := ticketOf(t, s, key)
	snapshot, err := encodeSnapshot(worstSnapshot(t, worst))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE tickets SET external_snapshot = ? WHERE id = ?", snapshot, tk.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTicketReadError(ctx, key, tk.ExternalID, worstWords(4*MaxReadError)); err != nil {
		t.Fatal(err)
	}
	// The identity and the owners at their bounds: a system of 16 bytes, a
	// host of 253 and a node id of 100.
	system := strings.Repeat("s", 16)
	identity := ExternalIdentity(system, strings.Repeat("h", MaxExternalHost), strings.Repeat("N", MaxNodeID))
	if _, err := s.db.Exec("UPDATE ticket_reads SET external_id = ?, updated_at = ?, read_at = ?, error_at = ?, issue_etag = ?, pull_etag = ?, read_by = ? WHERE ticket_id = ?",
		identity, stamp(day0.Add(time.Nanosecond)), stamp(day0.Add(time.Nanosecond)), stamp(day0.Add(time.Nanosecond)), worst(MaxValidator), worst(MaxValidator),
		worstWords(MaxByName), tk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE tickets SET external_id = ?, authority = ? WHERE id = ?", identity,
		Authority{Of: authorityKey(identity), Title: system, Description: system}.encode(), tk.ID); err != nil {
		t.Fatal(err)
	}
	var checks []LinkCheck
	for i := 0; i < MaxLinkChecks; i++ {
		checks = append(checks, LinkCheck{ID: strings.Repeat("9", 20), Name: "c", Status: "completed", Conclusion: "failure"})
	}
	for i := 0; i < MaxLinks; i++ {
		rc, err := addLink(s, "l"+newID(), key, NewLink{Kind: LinkDelivery, URL: "https://github.com/o/r/pull/" + strings.Repeat("9", 10) + string(rune('a'+i%26)) + string(rune('a'+i/26))})
		want(t, rc, err, StatusDone, "")
		r := prRead(strings.Repeat("N", MaxNodeID), 1, "merged")
		r.MergeSha, r.HeadSha, r.Checks = strings.Repeat("a", 64), strings.Repeat("b", 64), checks
		r.MergedAt, r.UpdatedAt = day0.Add(time.Nanosecond), day0.Add(time.Nanosecond)
		r.Incomplete = &ChecksIncomplete{Words: []string{"x"}, Omitted: 1 << 62}
		if err := s.RecordRead(ctx, rc.ID, r); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordReadError(ctx, rc.ID, worstWords(4*MaxReadError)); err != nil {
			t.Fatal(err)
		}
	}
	v, err := s.Ticket(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.LinkReads) != MaxLinks {
		t.Fatalf("%d link reads", len(v.LinkReads))
	}
	for id, sum := range v.LinkReads {
		b, _ := encodeValue(sum)
		if len(b) > MaxLinkReadSummary {
			t.Fatalf("the summary of %s is %d bytes, more than %d", id, len(b), MaxLinkReadSummary)
		}
	}
	closedOn, err := encodeValue(ClosedOn{LinkID: strings.Repeat("f", IDLen), MergedAt: day0.Add(time.Nanosecond), MergeSha: strings.Repeat("b", 64),
		ReadAt: day0.Add(time.Nanosecond)})
	if err != nil {
		t.Fatal(err)
	}
	parts, err := encodeValue(struct {
		Snapshot     json.RawMessage            `json:"externalSnapshot"`
		Authority    json.RawMessage            `json:"authority"`
		ClosedOn     json.RawMessage            `json:"closedOn"`
		Owners       *Owners                    `json:"owners"`
		ExternalRead *TicketRead                `json:"externalRead"`
		LinkReads    map[string]LinkReadSummary `json:"linkReads"`
	}{v.ExternalSnapshot, v.Authority, json.RawMessage(closedOn), v.Owners, v.ExternalRead, v.LinkReads})
	if err != nil {
		t.Fatal(err)
	}
	var summary int
	for _, sum := range v.LinkReads {
		b, _ := encodeValue(sum)
		summary = max(summary, len(b))
	}
	t.Logf("the new parts at their worst: %d bytes, their bound %d (the snapshot %d, a summary %d)", len(parts), MaxTicketExternalParts, len(v.ExternalSnapshot), summary)
	if len(parts) > MaxTicketExternalParts || MaxTicketExternalParts-len(parts) > 4<<10 {
		t.Fatalf("the new parts are %d bytes at their worst, their bound %d", len(parts), MaxTicketExternalParts)
	}
	// A read past its bounds is refused, nothing written.
	link := v.Links[0].ID
	for name, mutate := range map[string]func(*LinkRead){
		"101 checks":            func(r *LinkRead) { r.Checks = append(checks, checks[0]) },
		"a check's name of 201": func(r *LinkRead) { r.Checks = []LinkCheck{{ID: "1", Name: strings.Repeat("n", MaxCheckName+1)}} },
		"a check with no id":    func(r *LinkRead) { r.Checks = []LinkCheck{{Name: "n"}} },
		"nine reasons":          func(r *LinkRead) { r.Incomplete = &ChecksIncomplete{Words: strings.Split("a b c d e f g h i", " ")} },
		"a reason of 121": func(r *LinkRead) {
			r.Incomplete = &ChecksIncomplete{Words: []string{strings.Repeat("w", MaxIncompleteWord+1)}}
		},
		"incomplete with no reason":  func(r *LinkRead) { r.Incomplete = &ChecksIncomplete{Omitted: 1} },
		"merged but open":            func(r *LinkRead) { r.State = "open" },
		"checks past 32 KiB encoded": func(r *LinkRead) { r.Checks = bigChecks() },
	} {
		r := prRead("PR_1", 9, "merged")
		mutate(&r)
		if err := s.RecordRead(ctx, link, r); !isRefusal(err, RefusalBound) && !isRefusal(err, RefusalInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// bigChecks is 100 checks whose encoding passes MaxLinkChecksJSON.
func bigChecks() []LinkCheck {
	var out []LinkCheck
	for i := 0; i < MaxLinkChecks; i++ {
		out = append(out, LinkCheck{ID: "1", Name: strings.Repeat("n", MaxCheckName), App: strings.Repeat("a", MaxCheckName)})
	}
	return out
}
