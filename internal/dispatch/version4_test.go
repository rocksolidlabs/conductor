package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
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
	// A publication's (PR-5): what was prepared immutable, its send's
	// record scalar, every column classed but its rev.
	immutable := []string{"id", "ticket_id", "target", "target_kind", "target_url", "target_ref", "target_node_id", "target_space_id", "instance", "marker",
		"body", "post_sha256", "parts", "close_issue", "created_at", "created_by", "fingerprint"}
	for _, col := range columnsOf(t, s.db, "publications") {
		want := ClassScalar
		switch {
		case col == "rev":
			continue
		case slices.Contains(immutable, col):
			want = ClassImmutable
		}
		if c := Class(EntityPublication, col); c != want {
			t.Errorf("Class(publication, %s) = %q, want %q", col, c, want)
		}
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
	// Each case changes one part of a read that is accepted, and is
	// refused by its own check, its code and its words.
	if _, _, _, err := normalSnapshot(ghRead("I_1", 0, "T", "B")); err != nil {
		t.Fatalf("the read the cases change: %v", err)
	}
	for _, c := range []struct {
		name, code, words string
		mutate            func(*ExternalRead)
	}{
		{"a host past 253 bytes", RefusalInvalid, "is not a lower-case host name", func(r *ExternalRead) { r.Snapshot.Host = strings.Repeat("h", MaxExternalHost+1) }},
		{"an upper-case host", RefusalInvalid, `the host "GitHub.com" is not a lower-case host name`, func(r *ExternalRead) { r.Snapshot.Host = "GitHub.com" }},
		{"a node id of 101", RefusalInvalid, "the item's node id is not 1 to 100", func(r *ExternalRead) { r.Snapshot.NodeID = strings.Repeat("N", MaxNodeID+1) }},
		{"a node id with ':'", RefusalInvalid, "the item's node id is not 1 to 100", func(r *ExternalRead) { r.Snapshot.NodeID = "I:1" }},
		{"a remote id of 65", RefusalBound, "the item's database id is more than 64 bytes", func(r *ExternalRead) { r.Snapshot.DatabaseID = strings.Repeat("9", MaxRemoteID+1) }},
		{"an http URL", RefusalInvalid, "the item's page on github.com is not https", func(r *ExternalRead) { r.Snapshot.URL = "http://github.com/o/r/issues/1" }},
		{"no update time", RefusalInvalid, "the read names no time the item was updated", func(r *ExternalRead) { r.Snapshot.UpdatedAt = time.Time{} }},
		{"a short commit", RefusalInvalid, "the head commit is not a full commit id", func(r *ExternalRead) { r.Snapshot.HeadSha = "abc" }},
		{"a validator of 201", RefusalInvalid, "a read's validator is not at most 200 bytes", func(r *ExternalRead) { r.IssueETag = strings.Repeat("e", MaxValidator+1) }},
		{"version 2", RefusalInvalid, "the snapshot is version 2, not 1", func(r *ExternalRead) { r.Snapshot.V = 2 }},
	} {
		read := ghRead("I_1", 0, "T", "B")
		c.mutate(&read)
		if _, _, _, err := normalSnapshot(read); !isRefusal(err, c.code) || !strings.Contains(err.Error(), c.words) {
			t.Errorf("%s: %v, want %s naming %q", c.name, err, c.code, c.words)
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

// mainReplyPaths is every JSON key path of a ticket's read on main before
// this version (replyPaths of main's TicketView at 7c40e389): what main's
// own size test (internal/api, MaxTicketReply) measures.
var mainReplyPaths = []string{
	"acceptance", "acceptedAt", "acceptedBy", "acceptedCriteria", "acceptedFor", "acceptedNote", "approved", "approved[]", "approved[].approvedAt",
	"approved[].approvedBy", "approved[].approvedIntent", "approved[].approvedNote", "approved[].approvedRev", "approved[].base",
	"approved[].basedOn", "approved[].bytes", "approved[].createdAt", "approved[].createdBy", "approved[].crewId", "approved[].crewName",
	"approved[].crewUpdatedAt", "approved[].digest", "approved[].fingerprint", "approved[].id", "approved[].n", "approved[].rev", "approved[].stale",
	"approved[].stale.remedy", "approved[].stale.words", "approved[].state", "approved[].ticketId", "approved[].withdrawnAt",
	"approved[].withdrawnBy", "assignees", "assignees[]", "attachments", "attachments[]", "attachments[].contentType", "attachments[].createdAt",
	"attachments[].createdBy", "attachments[].deletedAt", "attachments[].id", "attachments[].name", "attachments[].sha256", "attachments[].size",
	"attachments[].ticketId", "authority", "blocked", "children", "children.tickets", "children.tickets[]", "children.tickets[].deletedAt",
	"children.tickets[].id", "children.tickets[].key", "children.tickets[].status", "children.tickets[].title", "children.total", "close",
	"close.met", "close.met.acceptance", "close.met.delivery", "close.met.verified", "close.missing", "close.missing[]", "close.requires",
	"close.requires[]", "close.verifiedWhy", "close.why", "closedAt", "comments", "comments[]", "comments[].author", "comments[].body",
	"comments[].by", "comments[].createdAt", "comments[].deletedAt", "comments[].editedAt", "comments[].id", "comments[].n", "comments[].rev",
	"comments[].ticketId", "createdAt", "createdBy", "deletedAt", "description", "externalId", "externalSnapshot", "externalUrl", "horizon", "id",
	"key", "labels", "labels[]", "launchCount", "links", "links[]", "links[].candidate", "links[].createdAt", "links[].createdBy",
	"links[].deletedAt", "links[].id", "links[].kind", "links[].note", "links[].ref", "links[].url", "links[].verified", "milestoneId", "number",
	"owner", "parent", "parent.deletedAt", "parent.id", "parent.key", "parent.status", "parent.title", "parentId", "priority", "projectId",
	"relations", "relationsTotal", "relations[]", "relations[].createdAt", "relations[].createdBy", "relations[].deletedAt", "relations[].fromId",
	"relations[].id", "relations[].kind", "relations[].other", "relations[].other.deletedAt", "relations[].other.id", "relations[].other.key",
	"relations[].other.status", "relations[].other.title", "relations[].toId", "rev", "source", "status", "title", "updatedAt",
}

// rowReplyParts is what this row adds to a ticket's read or now fills
// (Program item IN1a, PR-1): the paths it adds, and main's paths it fills
// that main left empty (source's longer value, the counterpart group).
// "close.requires[]" and "close.missing[]" are filled with merged, the
// requirement this version adds.
var rowReplyParts = []string{
	"owners", "externalRead", "linkReads", "closedOn", "close.mergedWhy", "close.met.merged",
	"source", "externalId", "externalUrl", "externalSnapshot", "authority", "close.requires[]", "close.missing[]",
	// PR-5: the newest publications and the count of older ones that need
	// a person.
	"publications", "publicationsNeedYou",
}

// replyPaths is every JSON key path of the type as encoding/json writes
// it: a field by its name, an embedded struct's fields as its own, a
// slice's element under "[]" and a map's value under "{}", a time and a
// raw message a value; each path once, a type met again on its own path
// not walked again.
func replyPaths(t reflect.Type) []string {
	var out []string
	var walk func(t reflect.Type, prefix string, on map[reflect.Type]bool)
	walk = func(t reflect.Type, prefix string, on map[reflect.Type]bool) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch {
		case t == reflect.TypeFor[time.Time]() || t == reflect.TypeFor[json.RawMessage]():
			return
		case t.Kind() == reflect.Slice:
			out = append(out, prefix+"[]")
			walk(t.Elem(), prefix+"[]", on)
		case t.Kind() == reflect.Map:
			out = append(out, prefix+"{}")
			walk(t.Elem(), prefix+"{}", on)
		case t.Kind() == reflect.Struct:
			if on[t] {
				return
			}
			on[t] = true
			defer delete(on, t)
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				switch {
				case !f.IsExported() || name == "-":
				case f.Anonymous && name == "":
					walk(f.Type, prefix, on)
				default:
					if name == "" {
						name = f.Name
					}
					p := name
					if prefix != "" {
						p = prefix + "." + name
					}
					out = append(out, p)
					walk(f.Type, p, on)
				}
			}
		}
	}
	walk(t, "", map[reflect.Type]bool{})
	slices.Sort(out)
	return slices.Compact(out)
}

// mainOf is a ticket's read, decoded as JSON with its numbers as written,
// as main's would hold it: the parts this row adds left out, source
// native, and merged out of the policy's requirements.
func mainOf(v map[string]any) map[string]any {
	out := maps.Clone(v)
	for _, k := range []string{"owners", "externalRead", "linkReads", "closedOn", "externalId", "externalUrl", "externalSnapshot", "authority", "publications",
		"publicationsNeedYou"} {
		delete(out, k)
	}
	out["source"] = TrackerNative
	c := maps.Clone(out["close"].(map[string]any))
	delete(c, "mergedWhy")
	met := maps.Clone(c["met"].(map[string]any))
	delete(met, "merged")
	c["met"] = met
	for _, k := range []string{"requires", "missing"} {
		c[k] = slices.DeleteFunc(slices.Clone(c[k].([]any)), func(r any) bool { return r == RequireMerged })
	}
	out["close"] = c
	return out
}

// filled reports whether every value the path names in the decoded reply
// is there, a text among them not empty ("[]" every element, "{}" every
// value, at least one each).
func filled(v any, path []string) bool {
	if len(path) == 0 {
		s, isText := v.(string)
		return v != nil && (!isText || s != "")
	}
	var each []any
	switch {
	case path[0] == "[]":
		l, _ := v.([]any)
		each = l
	case path[0] == "{}":
		m, _ := v.(map[string]any)
		for _, e := range m {
			each = append(each, e)
		}
	default:
		m, _ := v.(map[string]any)
		e, ok := m[path[0]]
		if !ok {
			return false
		}
		each = []any{e}
	}
	if len(each) == 0 {
		return false
	}
	for _, e := range each {
		if !filled(e, path[1:]) {
			return false
		}
	}
	return true
}

// splitPath is a reply path's steps: names, "[]" and "{}".
func splitPath(p string) []string {
	p = strings.NewReplacer("[]", ".[]", "{}", ".{}").Replace(p)
	return strings.Split(p, ".")
}

// What this version adds to a ticket's read, measured as the reply the
// route returns: the largest ticket's read (TicketView) with every part
// this row adds or fills at its bound and every text at its escaped worst
// as the route encodes it (encodeValue is writeDispatch's encoding: HTML
// escaping off, so '"' for a text bounded in bytes and U+2028 for one
// bounded in characters), less the same read as main's would hold it
// (mainOf). The parts are found from the reply's type: every key path of
// TicketView that main's read has not (mainReplyPaths) must be one of this
// row's (rowReplyParts) and filled in the read measured, so a part a later
// row adds fails here until it is built at its worst. The measure is under
// MaxTicketExternalParts, and that bound is the measure rounded up to a
// KiB or two, not a guess above it (the room the API's MaxTicketReply
// keeps for it depends on it); each summary is under MaxLinkReadSummary; a
// link's read past its bounds is refused.
func TestTicketReplyBound(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "T", "B"))
	tk := ticketOf(t, s, key)
	worstSn := worstSnapshot(t, worst)
	snapshot, err := encodeSnapshot(worstSn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTicketReadError(ctx, key, tk.ExternalID, worstWords(4*MaxReadError)); err != nil {
		t.Fatal(err)
	}
	// The identity, the owners and the source at their bounds: a system of
	// 16 bytes, a host of 253 and a node id of 100; the page the snapshot's,
	// MaxExternalURL bytes; closed_on a read at its bounds.
	system := strings.Repeat("s", 16)
	identity := ExternalIdentity(system, strings.Repeat("h", MaxExternalHost), strings.Repeat("N", MaxNodeID))
	if _, err := s.db.Exec("UPDATE ticket_reads SET external_id = ?, updated_at = ?, read_at = ?, error_at = ?, issue_etag = ?, pull_etag = ?, read_by = ? WHERE ticket_id = ?",
		identity, stamp(day0.Add(time.Nanosecond)), stamp(day0.Add(time.Nanosecond)), stamp(day0.Add(time.Nanosecond)), worst(MaxValidator), worst(MaxValidator),
		worstWords(MaxByName), tk.ID); err != nil {
		t.Fatal(err)
	}
	closedOn, err := encodeValue(ClosedOn{LinkID: strings.Repeat("f", IDLen), MergedAt: day0.Add(time.Nanosecond), MergeSha: strings.Repeat("b", 64),
		ReadAt: day0.Add(time.Nanosecond)})
	if err != nil {
		t.Fatal(err)
	}
	if len(worstSn.URL) != MaxExternalURL {
		t.Fatalf("the worst page is %d bytes", len(worstSn.URL))
	}
	if _, err := s.db.Exec("UPDATE tickets SET external_id = ?, authority = ?, source = ?, external_url = ?, external_snapshot = ?, closed_on = ? WHERE id = ?",
		identity, Authority{Of: authorityKey(identity), Title: system, Description: system}.encode(), system, worstSn.URL, snapshot, closedOn, tk.ID); err != nil {
		t.Fatal(err)
	}
	// The policy names merged (PR-4 offers it), unmet, its words at their
	// longest: each delivery read merged longer ago than a duration holds,
	// each delivery's page at MaxLinkURL.
	if _, err := s.db.Exec(`UPDATE projects SET close_requires = '["acceptance","delivery","verified","merged"]' WHERE key = 'CND'`); err != nil {
		t.Fatal(err)
	}
	var checks []LinkCheck
	for i := 0; i < MaxLinkChecks; i++ {
		checks = append(checks, LinkCheck{ID: strings.Repeat("9", 20), Name: "c", Status: "completed", Conclusion: "failure"})
	}
	for i := 0; i < MaxLinks; i++ {
		page := "https://github.com/o/r/pull/" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		rc, err := addLink(s, "l"+newID(), key, NewLink{Kind: LinkDelivery, URL: page + "/" + strings.Repeat("9", MaxLinkURL-len(page)-1)})
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
	if _, err := s.db.Exec("UPDATE link_reads SET read_at = ?", stamp(time.Date(1700, 1, 1, 0, 0, 0, 1, time.UTC))); err != nil {
		t.Fatal(err)
	}
	worstPublications(t, s, key, identity)
	v, err := s.Ticket(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.LinkReads) != MaxLinks {
		t.Fatalf("%d link reads", len(v.LinkReads))
	}
	var summary int
	for id, sum := range v.LinkReads {
		b, _ := encodeValue(sum)
		if len(b) > MaxLinkReadSummary {
			t.Fatalf("the summary of %s is %d bytes, more than %d", id, len(b), MaxLinkReadSummary)
		}
		summary = max(summary, len(b))
	}
	reply, err := encodeValue(v)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(reply))
	dec.UseNumber()
	var tree map[string]any
	if err := dec.Decode(&tree); err != nil {
		t.Fatal(err)
	}
	// The reply's parts, from its type: every one main's read has not is
	// this row's, and filled.
	for _, p := range replyPaths(reflect.TypeFor[TicketView]()) {
		if slices.Contains(mainReplyPaths, p) {
			continue
		}
		if !slices.ContainsFunc(rowReplyParts, func(part string) bool {
			return p == part || strings.HasPrefix(p, part+".") || strings.HasPrefix(p, part+"{}") || strings.HasPrefix(p, part+"[]")
		}) {
			t.Errorf("%s is a part of the ticket's read that neither main's measure nor this row's holds", p)
		}
	}
	for _, part := range rowReplyParts {
		for _, p := range replyPaths(reflect.TypeFor[TicketView]()) {
			if (p == part || strings.HasPrefix(p, part+".") || strings.HasPrefix(p, part+"{}") || strings.HasPrefix(p, part+"[]")) && !filled(tree, splitPath(p)) {
				t.Errorf("the read measured leaves %s empty", p)
			}
		}
	}
	if !slices.Contains(v.Close.Requires, RequireMerged) || !slices.Contains(v.Close.Missing, RequireMerged) {
		t.Errorf("the policy's merged is not in the read: %+v", v.Close)
	}
	again, err := encodeValue(tree)
	if err != nil {
		t.Fatal(err)
	}
	asMain, err := encodeValue(mainOf(tree))
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(reply) {
		t.Fatalf("the reply decoded and encoded again is %d bytes, not %d", len(again), len(reply))
	}
	parts := len(reply) - len(asMain)
	t.Logf("the new parts at their worst: %d bytes of a %d-byte read, their bound %d (the snapshot %d, a summary %d, the merged words %d)", parts, len(reply),
		MaxTicketExternalParts, len(v.ExternalSnapshot), summary, len(v.Close.MergedWhy))
	if parts > MaxTicketExternalParts || MaxTicketExternalParts-parts > 4<<10 {
		t.Fatalf("the new parts are %d bytes at their worst, their bound %d", parts, MaxTicketExternalParts)
	}
	// A read past its bounds is refused, nothing written: each case on a
	// link of its own that holds no read, the read it changes accepted
	// there after its refusal.
	_, other := newTicket(t, s, "CND", "Past its bounds")
	for i, c := range []struct {
		name, code, words string
		mutate            func(*LinkRead)
	}{
		{"101 checks", RefusalBound, "at most 100 checks in a read; this has 101", func(r *LinkRead) { r.Checks = append(checks, checks[0]) }},
		{"a check's name of 201", RefusalBound, "a check's name is more than 200 bytes",
			func(r *LinkRead) { r.Checks = []LinkCheck{{ID: "1", Name: strings.Repeat("n", MaxCheckName+1)}} }},
		{"a check with no id", RefusalInvalid, "check 0's id is not 1 to 64 bytes", func(r *LinkRead) { r.Checks = []LinkCheck{{Name: "n"}} }},
		{"nine reasons", RefusalInvalid, "an incomplete set of checks says why in 1 to 8 reasons",
			func(r *LinkRead) { r.Incomplete = &ChecksIncomplete{Words: strings.Split("a b c d e f g h i", " ")} }},
		{"a reason of 121", RefusalBound, "a reason the checks are incomplete is more than 120 bytes",
			func(r *LinkRead) {
				r.Incomplete = &ChecksIncomplete{Words: []string{strings.Repeat("w", MaxIncompleteWord+1)}}
			}},
		{"incomplete with no reason", RefusalInvalid, "an incomplete set of checks says why in 1 to 8 reasons",
			func(r *LinkRead) { r.Incomplete = &ChecksIncomplete{Omitted: 1} }},
		{"merged but open", RefusalInvalid, "an item is merged exactly when its state is merged", func(r *LinkRead) { r.State = "open" }},
		{"checks past 32 KiB encoded", RefusalBound, "the read's checks are", func(r *LinkRead) { r.Checks = bigChecks() }},
	} {
		rc, err := addLink(s, fmt.Sprint("bound", i), other, NewLink{Kind: LinkDelivery, URL: fmt.Sprint("https://github.com/o/r/pull/", 100+i)})
		want(t, rc, err, StatusDone, "")
		r := prRead("PR_1", 9, "merged")
		c.mutate(&r)
		if err := s.RecordRead(ctx, rc.ID, r); !isRefusal(err, c.code) || !strings.Contains(err.Error(), c.words) {
			t.Errorf("%s: %v, want %s naming %q", c.name, err, c.code, c.words)
		}
		if _, found, err := s.LinkReadOf(ctx, rc.ID); found || err != nil {
			t.Errorf("%s: a read was kept (%v)", c.name, err)
		}
		if err := s.RecordRead(ctx, rc.ID, prRead("PR_1", 9, "merged")); err != nil {
			t.Errorf("%s: the read it changes is refused: %v", c.name, err)
		}
	}
}

// worstPublications gives the ticket with the key (linked to identity)
// MaxTicketPublications+1 publications, each with every field at its
// bound and every text at its escaped worst (another instance's, so each
// is restored and needs a person), the read's newest ten and one older.
func worstPublications(t *testing.T, s *Store, key, identity string) {
	t.Helper()
	node := identity[strings.LastIndexByte(identity, ':')+1:]
	target := PublicationTarget{NodeID: node, SpaceID: "4000001", Kind: ExternalIssue, URL: "https://github.com/o/r/issues/1", Ref: "o/r#1"}
	page := "https://github.com/o/r/issues/" + strings.Repeat("9", MaxExternalURL-len("https://github.com/o/r/issues/"))
	for i := 0; i <= MaxTicketPublications; i++ {
		rc, err := s.PreparePublication(context.Background(), testOp(fmt.Sprint("worst", i)), PublicationArgs{Key: key, Target: TargetLinked, Body: "b",
			Parts: PublicationParts, CloseIssue: "not_planned"}, target, func(id string) string { return id + strings.Repeat(" ", (MaxPublicationMarker-IDLen)/3) })
		want(t, rc, err, StatusDone, "")
		at := stamp(day0.Add(time.Duration(i)*time.Second + time.Nanosecond))
		if _, err := s.db.Exec(`UPDATE publications SET target = ?, target_url = ?, target_ref = ?, target_space_id = ?, instance = 'another', state = ?, tries = ?,
			attempt = ?, find_since = ?, sent_as_id = ?, sent_as_login = ?, remote_id = ?, remote_url = ?, close_state = ?, close_tries = ?, close_error = ?, error = ?,
			sending_at = ?, sent_at = ?, sent_by = ?, created_at = ?, created_by = ?, rev = ? WHERE id = ?`,
			"link:"+strings.Repeat("f", IDLen), page, worst(MaxPublicationRef), worst(MaxRemoteID), PublicationUnconfirmed, MaxPublicationTries, MaxPublicationTries,
			at, worst(MaxRemoteID), worst(MaxSnapshotLogin), worst(MaxRemoteID), page, CloseClosing, MaxPublicationCloseTries, worstWords(MaxReadError),
			worstWords(MaxReadError), at, at, worstWords(MaxByName), at, worstWords(MaxByName), int64(1)<<62, rc.ID); err != nil {
			t.Fatal(err)
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
