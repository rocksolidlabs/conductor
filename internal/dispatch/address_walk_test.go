package dispatch

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The walk of every text this version keeps (Program item IN1a, review
// round 2 of #780): no text the store keeps about a tracker carries an
// address's query, fragment or user. The walk finds the texts itself: the
// text columns of what version 4 adds, each with its decision below, and
// every string field of the records the new operations take, each
// written with a marker address and every table, the ticket's and the
// link's reads and an export read back for it. A column or a field added
// later with no decision fails: version 5's columns, and any added after
// them, in TestEveryVersionFiveTextHasADecision.

// markers are the addresses the walk writes: one with a user, a query and
// a fragment, and a short one for a text bounded under the first.
var markers = []string{"https://bob:hunter2@x.example.com/p?token=SECRET#SECRET", "s://h/?SECRET"}

// How the store keeps a text of the walk.
const (
	// keptOpaque: an id, a state, a validator, a digest or a place the
	// store holds as it is, refused (invalid, bound) when it holds an
	// address (holdsAddress, or a pattern no address meets).
	keptOpaque = "opaque"
	// keptAddress: an address, through admitAddress (refused, or kept as
	// scheme, host and path).
	keptAddress = "address"
	// keptWords: words, through addressFree.
	keptWords = "words"
	// keptClosed: one of a closed set of words Conductor chose.
	keptClosed = "closed"
	// keptDerived: the store's own (an id it minted, a time, a digest,
	// what normalSnapshot makes of the read), never the input's text.
	keptDerived = "derived"
	// keptContent: the item's own words, as the person wrote them on the
	// tracker (its title, body, steps, labels, logins, milestone, branch)
	// or as a person gives them to an operation of DS1's (a ticket's
	// owner, assignees, labels, milestone): kept as written, as a ticket's
	// description keeps a link a person pasted.
	keptContent = "content"
	// keptNone: no operation of this row writes it (publications: PR-5).
	keptNone = "none"
)

// columnKept is the decision of every text column version 4 adds or gives
// a meaning to: its new tables' and the ticket's counterpart group and
// closed_on.
var columnKept = map[string]string{
	"link_reads.link_id": keptDerived, "link_reads.ticket_id": keptDerived, "link_reads.system": keptOpaque, "link_reads.host": keptOpaque,
	"link_reads.kind": keptClosed, "link_reads.node_id": keptOpaque, "link_reads.state": keptClosed, "link_reads.merged_at": keptDerived,
	"link_reads.merge_sha": keptOpaque, "link_reads.head_sha": keptOpaque,
	// checks: ids and states opaque, names words, pages addresses.
	"link_reads.checks": keptWords, "link_reads.checks_incomplete": keptWords, "link_reads.updated_at": keptDerived, "link_reads.read_at": keptDerived,
	"link_reads.issue_etag": keptOpaque, "link_reads.pull_etag": keptOpaque, "link_reads.error": keptWords, "link_reads.error_at": keptDerived,
	"ticket_reads.ticket_id": keptDerived, "ticket_reads.external_id": keptOpaque, "ticket_reads.updated_at": keptDerived,
	"ticket_reads.read_at": keptDerived, "ticket_reads.read_by": keptWords, "ticket_reads.issue_etag": keptOpaque, "ticket_reads.pull_etag": keptOpaque,
	"ticket_reads.error": keptWords, "ticket_reads.error_at": keptDerived,
	"store_instance.id": keptDerived,
	"tickets.source":    keptOpaque, "tickets.external_id": keptOpaque, "tickets.external_url": keptAddress,
	// The snapshot: the item's words, its addresses, its ids.
	"tickets.external_snapshot": keptContent, "tickets.authority": keptClosed, "tickets.closed_on": keptDerived,
	// The publications: PR-5's operations write them and decide each.
	"publications.id": keptNone, "publications.ticket_id": keptNone, "publications.target": keptNone, "publications.target_kind": keptNone,
	"publications.target_url": keptNone, "publications.target_ref": keptNone, "publications.target_node_id": keptNone,
	"publications.target_space_id": keptNone, "publications.instance": keptNone, "publications.marker": keptNone, "publications.body": keptNone,
	"publications.post_sha256": keptNone, "publications.parts": keptNone, "publications.close_issue": keptNone, "publications.state": keptNone,
	"publications.find_since": keptNone, "publications.sent_as_id": keptNone, "publications.sent_as_login": keptNone,
	"publications.remote_id": keptNone, "publications.remote_url": keptNone, "publications.close_state": keptNone, "publications.close_error": keptNone,
	"publications.error": keptNone, "publications.sending_at": keptNone, "publications.sent_at": keptNone, "publications.sent_by": keptNone,
	"publications.created_at": keptNone, "publications.created_by": keptNone, "publications.fingerprint": keptNone,
}

// fieldKept is the decision of every string field of the records the new
// operations take, by its path in the walk's record.
var fieldKept = map[string]string{
	"Args.Project": keptOpaque, "Args.URL": keptDerived, "Args.Status": keptClosed, "Args.Horizon": keptClosed, "Args.Owner": keptContent,
	"Args.Assignees[]": keptContent, "Args.Labels[]": keptOpaque, "Args.Milestone": keptOpaque,
	"Link.Key": keptOpaque, "Link.URL": keptDerived,
	"Authority.Key": keptOpaque, "Authority.Field": keptClosed, "Authority.Owner": keptClosed,
	"Read.Snapshot.System": keptOpaque, "Read.Snapshot.Host": keptOpaque, "Read.Snapshot.Space": keptOpaque, "Read.Snapshot.Kind": keptClosed,
	"Read.Snapshot.ID": keptOpaque, "Read.Snapshot.NodeID": keptOpaque, "Read.Snapshot.DatabaseID": keptOpaque, "Read.Snapshot.URL": keptAddress,
	"Read.Snapshot.Title": keptContent, "Read.Snapshot.BodySha256": keptDerived, "Read.Snapshot.State": keptClosed,
	"Read.Snapshot.StateReason": keptClosed, "Read.Snapshot.MergeSha": keptOpaque, "Read.Snapshot.HeadSha": keptOpaque,
	"Read.Snapshot.BaseRef": keptContent, "Read.Snapshot.Labels[]": keptContent, "Read.Snapshot.Assignees[]": keptContent,
	"Read.Snapshot.Milestone": keptContent, "Read.Snapshot.Author": keptContent, "Read.Snapshot.AuthorID": keptOpaque,
	"Read.Snapshot.Repro": keptContent, "Read.Snapshot.ReproSha256": keptDerived, "Read.Snapshot.DescriptionSha256": keptDerived,
	"Read.Snapshot.Images[].ID": keptAddress, "Read.Snapshot.Images[].AttachmentID": keptDerived, "Read.Snapshot.Images[].SHA256": keptDerived,
	"Read.Snapshot.Images[].Skipped": keptDerived, "Read.Body": keptContent, "Read.IssueETag": keptOpaque, "Read.PullETag": keptOpaque,
	"Image": keptAddress, "Upload.Name": keptOpaque, "Upload.ContentType": keptClosed, "Upload.Skipped": keptClosed,
	"LinkRead.System": keptOpaque, "LinkRead.Host": keptOpaque, "LinkRead.Kind": keptClosed, "LinkRead.NodeID": keptOpaque,
	"LinkRead.State": keptClosed, "LinkRead.MergeSha": keptOpaque, "LinkRead.HeadSha": keptOpaque, "LinkRead.Checks[].ID": keptOpaque,
	"LinkRead.Checks[].Name": keptWords, "LinkRead.Checks[].Status": keptOpaque, "LinkRead.Checks[].Conclusion": keptOpaque,
	"LinkRead.Checks[].URL": keptAddress, "LinkRead.Checks[].App": keptWords, "LinkRead.Incomplete.Words[]": keptWords,
	"LinkRead.IssueETag": keptOpaque, "LinkRead.PullETag": keptOpaque, "LinkRead.Error": keptDerived,
	"TicketRead.ExternalID": keptDerived, "TicketRead.ReadBy": keptWords, "TicketRead.IssueETag": keptOpaque, "TicketRead.PullETag": keptOpaque,
	"TicketRead.Error": keptDerived, "Identity": keptOpaque, "Words": keptWords,
}

// stringPaths is every string field of the type, each as the names that
// lead to it ("[]" for a slice's element), a time and an interface
// holding none.
func stringPaths(t reflect.Type, prefix []string) [][]string {
	switch t.Kind() {
	case reflect.Pointer:
		return stringPaths(t.Elem(), prefix)
	case reflect.String:
		return [][]string{slices.Clone(prefix)}
	case reflect.Slice:
		return stringPaths(t.Elem(), append(slices.Clone(prefix), "[]"))
	case reflect.Struct:
		if t == reflect.TypeFor[time.Time]() {
			return nil
		}
		var out [][]string
		for i := 0; i < t.NumField(); i++ {
			if f := t.Field(i); f.IsExported() {
				out = append(out, stringPaths(f.Type, append(slices.Clone(prefix), f.Name))...)
			}
		}
		return out
	}
	return nil
}

// pathName is a path as fieldKept names it.
func pathName(path []string) string { return strings.ReplaceAll(strings.Join(path, "."), ".[]", "[]") }

// setPath sets the string the path leads to in v (a slice's first element,
// one made when there is none) to s.
func setPath(v reflect.Value, path []string, s string) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}
	switch {
	case len(path) == 0:
		v.SetString(s)
	case path[0] == "[]":
		if v.Len() == 0 {
			v.Set(reflect.Append(v, reflect.Zero(v.Type().Elem())))
		}
		setPath(v.Index(0), path[1:], s)
	default:
		setPath(v.FieldByName(path[0]), path[1:], s)
	}
}

// walkStore is the walk's store: a project, an imported ticket (key), a
// native ticket with a delivery link read once (native, link) and a native
// ticket to link (free); the import's image a1 pending.
type walkStore struct {
	s                       *Store
	key, native, free, link string
}

func newWalkStore(t *testing.T) walkStore {
	t.Helper()
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	w := walkStore{s: s, key: mustImport(t, s, "imp", "CND", ghRead("I_X", 0, "Title", "Body"))}
	_, w.native = newTicket(t, s, "CND", "Delivered")
	_, w.free = newTicket(t, s, "CND", "Free")
	rc, err := addLink(s, "d1", w.native, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/9"})
	want(t, rc, err, StatusDone, "")
	w.link = rc.ID
	if err := s.RecordRead(ctx, w.link, prRead("PR_9", 1, "open")); err != nil {
		t.Fatal(err)
	}
	return w
}

// walkWriter is one new operation of the walk: its record (a pointer to a
// struct whose fields are the operation's string inputs, made afresh) and
// how it runs, giving its refusal's words when it was refused.
type walkWriter struct {
	name   string
	record func() any
	run    func(t *testing.T, w walkStore, record any) (refused string)
}

// refusalOf is the words of a receipt's or an error's refusal, failing the
// test on any other error.
func refusalOf(t *testing.T, rc Receipt, err error) string {
	t.Helper()
	var ref *Refusal
	switch {
	case errors.As(err, &ref):
		return ref.Error()
	case err != nil && strings.HasPrefix(err.Error(), "dispatch: "):
		return err.Error()
	case err != nil:
		t.Fatal(err)
	case rc.Status == StatusFailed:
		return rc.Refusal + ": " + rc.Error
	}
	return ""
}

var walkWriters = []walkWriter{
	{"import", func() any {
		return &struct {
			Args ImportArgs
			Read ExternalRead
		}{ImportArgs{Project: "CND", URL: "https://github.com/rocksolidlabs/conductor/issues/613", Status: TicketReady, Assignees: []string{"person:alice"}}, ghRead("I_W", 0, "Title", "Body")}
	}, func(t *testing.T, w walkStore, record any) string {
		r := record.(*struct {
			Args ImportArgs
			Read ExternalRead
		})
		rc, err := w.s.ImportTicket(context.Background(), testOp("walk"), r.Args, r.Read)
		return refusalOf(t, rc, err)
	}},
	{"link", func() any {
		return &struct {
			Link LinkArgs
			Read ExternalRead
		}{LinkArgs{URL: "https://github.com/rocksolidlabs/conductor/issues/614"}, ghRead("I_L", 0, "Title", "Body")}
	}, func(t *testing.T, w walkStore, record any) string {
		r := record.(*struct {
			Link LinkArgs
			Read ExternalRead
		})
		if r.Link.Key == "" {
			r.Link.Key = w.free
		}
		rc, err := w.s.LinkExternal(context.Background(), testOp("walk"), r.Link, r.Read)
		return refusalOf(t, rc, err)
	}},
	{"refresh", func() any {
		return &struct{ Read ExternalRead }{ghRead("I_X", 5, "Title", "Body")}
	}, func(t *testing.T, w walkStore, record any) string {
		r := record.(*struct{ Read ExternalRead })
		rc, err := w.s.RefreshExternal(context.Background(), testOp("walk"), w.key, r.Read)
		return refusalOf(t, rc, err)
	}},
	{"give back", func() any {
		return &struct {
			Authority AuthorityArgs
			Read      ExternalRead
		}{AuthorityArgs{Field: FieldTitle, Owner: "github"}, ghRead("I_X", 6, "Title", "Body")}
	}, func(t *testing.T, w walkStore, record any) string {
		r := record.(*struct {
			Authority AuthorityArgs
			Read      ExternalRead
		})
		owned(t, w.s, "take", w.key, FieldTitle, OwnerDispatch, nil)
		if r.Authority.Key == "" {
			r.Authority.Key = w.key
		}
		rc, err := w.s.SetAuthority(context.Background(), testOp("walk"), r.Authority, &r.Read)
		return refusalOf(t, rc, err)
	}},
	{"an image's attachment", func() any {
		return &struct {
			Image  string
			Upload ImageUpload
		}{"https://github.com/user-attachments/assets/a1", ImageUpload{Name: "github-1.png", ContentType: "image/png", Body: bytes.NewReader([]byte("png"))}}
	}, func(t *testing.T, w walkStore, record any) string {
		r := record.(*struct {
			Image  string
			Upload ImageUpload
		})
		rc, err := w.s.ImportImage(context.Background(), testOp("walk"), w.key, r.Image, r.Upload)
		return refusalOf(t, rc, err)
	}},
	{"an image's skip", func() any {
		return &struct {
			Image  string
			Upload ImageUpload
		}{"https://github.com/user-attachments/assets/a1", ImageUpload{Skipped: SkippedType}}
	}, func(t *testing.T, w walkStore, record any) string {
		r := record.(*struct {
			Image  string
			Upload ImageUpload
		})
		rc, err := w.s.ImportImage(context.Background(), testOp("walk"), w.key, r.Image, r.Upload)
		return refusalOf(t, rc, err)
	}},
	{"a link's read", func() any {
		r := prRead("PR_9", 2, "merged")
		r.Checks[0].URL = "https://github.com/rocksolidlabs/conductor/actions/runs/1"
		r.Incomplete = &ChecksIncomplete{Words: []string{"other apps' check runs need a classic token"}, Omitted: 1}
		return &struct{ LinkRead LinkRead }{r}
	}, func(t *testing.T, w walkStore, record any) string {
		return refusalOf(t, Receipt{}, w.s.RecordRead(context.Background(), w.link, record.(*struct{ LinkRead LinkRead }).LinkRead))
	}},
	{"a link's failed read", func() any {
		return &struct{ Words string }{"GET /repos/o/r/pulls/9: 502"}
	}, func(t *testing.T, w walkStore, record any) string {
		return refusalOf(t, Receipt{}, w.s.RecordReadError(context.Background(), w.link, record.(*struct{ Words string }).Words))
	}},
	{"the linked issue's read", func() any {
		return &struct {
			Identity   string
			TicketRead TicketRead
		}{"github:github.com:I_X", TicketRead{UpdatedAt: day0.Add(7 * time.Minute), ReadBy: "alice", IssueETag: `"e"`}}
	}, func(t *testing.T, w walkStore, record any) string {
		r := record.(*struct {
			Identity   string
			TicketRead TicketRead
		})
		return refusalOf(t, Receipt{}, w.s.RecordTicketRead(context.Background(), w.key, r.Identity, r.TicketRead))
	}},
	{"the linked issue's failed read", func() any {
		return &struct {
			Identity string
			Words    string
		}{"github:github.com:I_X", "GET /repos/o/r/issues/612: 502"}
	}, func(t *testing.T, w walkStore, record any) string {
		r := record.(*struct {
			Identity string
			Words    string
		})
		return refusalOf(t, Receipt{}, w.s.RecordTicketReadError(context.Background(), w.key, r.Identity, r.Words))
	}},
}

// holding is every place of the store that holds the secret: each table's
// column ("table.column"), the reads of the walk's tickets and link, and
// the store's export.
func holding(t *testing.T, w walkStore, secret string) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	for _, table := range texts(t, w.s, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'tickets_fts%' ORDER BY name") {
		for _, col := range columnsOf(t, w.s.db, table) {
			if countOf(t, w.s, fmt.Sprintf(`SELECT COUNT(*) FROM "%s" WHERE CAST("%s" AS TEXT) LIKE ?`, table, col), "%"+secret+"%") > 0 {
				out = append(out, table+"."+col)
			}
		}
	}
	for _, key := range []string{w.key, w.native, w.free} {
		v, err := w.s.Ticket(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := encodeValue(v); strings.Contains(b, secret) {
			out = append(out, "the read of "+key)
		}
	}
	r, _, err := w.s.LinkReadOf(ctx, w.link)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := encodeValue(r); strings.Contains(b, secret) {
		out = append(out, "the link's read")
	}
	dst := filepath.Join(t.TempDir(), "export.sqlite")
	if err := w.s.Snapshot(ctx, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dst); err != nil || bytes.Contains(b, []byte(secret)) {
		out = append(out, fmt.Sprintf("the export (%v)", err))
	}
	return out
}

// secretsOf is the secrets a marker carries.
func secretsOf(marker string) []string {
	var out []string
	for _, s := range secrets {
		if strings.Contains(marker, s) {
			out = append(out, s)
		}
	}
	return out
}

// Every text column version 4 adds or gives a meaning to has a decision:
// the new tables (found as the tables a fresh store has that the version 3
// fixture lacks) and the ticket's columns this version writes (closed_on,
// found the same way, and the counterpart group DS1 reserved).
func TestEveryVersionFourTextHasADecision(t *testing.T) {
	s := linkedStore(t)
	old := openRaw(t, filepath.Join(fixtureV3(t), File))
	tablesOf := func(db *sql.DB) []string { return tablesOfDB(t, db) }
	textColumns := func(db *sql.DB, table string) []string { return textColumnsOf(t, db, table) }
	v4 := openRaw(t, filepath.Join(fixtureV4(t), File))
	v4Tables := tablesOf(v4)
	found := map[string]bool{}
	oldTables := tablesOf(old)
	for _, table := range tablesOf(s.db) {
		// A table version 4 does not have is a later version's, decided by
		// that version's walk (TestEveryVersionFiveTextHasADecision).
		if !slices.Contains(v4Tables, table) {
			continue
		}
		cols := textColumns(s.db, table)
		if slices.Contains(oldTables, table) {
			oldCols := textColumns(old, table)
			var added []string
			for _, c := range cols {
				if !slices.Contains(oldCols, c) || (table == "tickets" && slices.Contains([]string{"source", "external_id", "external_url", "external_snapshot", "authority"}, c)) {
					added = append(added, c)
				}
			}
			cols = added
		}
		for _, c := range cols {
			found[table+"."+c] = true
			if _, ok := columnKept[table+"."+c]; !ok {
				t.Errorf("the text column %s.%s has no decision in columnKept", table, c)
			}
		}
	}
	for c := range columnKept {
		if !found[c] {
			t.Errorf("columnKept names %s, which is not a text column version 4 adds", c)
		}
	}
}

// tablesOfDB is the tables of the database, SQLite's and the FTS index's
// own left out.
func tablesOfDB(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return dbTexts(t, db, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'tickets_fts%'")
}

// textColumnsOf is the text columns of the table.
func textColumnsOf(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	return dbTexts(t, db, "SELECT name FROM pragma_table_info(?) WHERE type = 'TEXT'", table)
}

// dbTexts is the query's one column of text.
func dbTexts(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

// How version 5 keeps a text (the captures, Program item PV1), beside
// keptDerived, keptClosed, keptOpaque (an id or a hex of a pattern no
// address meets) and keptNone (no operation of this row writes it: the
// sends' tables, #825's).
const (
	// keptEvidence: what the page, its bridge or the person gave (the
	// page's address and title, the viewport, the element's report and
	// moment, a note, why there is no screenshot, the pane's read), kept
	// as given within its field rule: it is the evidence. An address among
	// them (captures.url) is kept whole, its query and fragment with it,
	// bounded and checked as an address (capture.url, kindURL), and no log
	// line prints it (the captures' code logs nothing).
	keptEvidence = "evidence"
	// keptFacts: what the API read at the save (the run's scope, the
	// tree's changed paths, the sources), kept as handed in within the
	// field rules.
	keptFacts = "facts"
	// keptOperator: the operation's actor and display name, as every
	// row of the store keeps them (op.actor, op.by).
	keptOperator = "operator"
)

// version5Kept is the decision of every text column version 5 adds.
var version5Kept = map[string]string{
	"captures.id": keptDerived, "captures.created_at": keptDerived, "captures.created_by": keptOperator, "captures.actor": keptOperator,
	"captures.via": keptClosed, "captures.fingerprint": keptDerived, "captures.url": keptEvidence, "captures.title": keptEvidence,
	"captures.viewport": keptEvidence, "captures.selected_at": keptEvidence, "captures.saved_at": keptDerived, "captures.origin": keptClosed,
	"captures.run_id": keptOpaque, "captures.task_id": keptOpaque, "captures.tree": keptClosed, "captures.commit_id": keptOpaque,
	"captures.changed_paths": keptFacts, "captures.content": keptOpaque, "captures.candidate": keptOpaque, "captures.where_ran": keptOpaque,
	"captures.scope": keptFacts, "captures.drift": keptEvidence, "captures.serving": keptDerived, "captures.image_sha256": keptDerived,
	"captures.image_how": keptClosed, "captures.image_why": keptEvidence, "captures.retake_of": keptOpaque, "captures.retake_via": keptClosed,
	"captures.deleted_at": keptDerived, "captures.annotations": keptDerived,
	"capture_annotations.capture_id": keptDerived, "capture_annotations.sha256": keptDerived, "capture_annotations.kind": keptClosed,
	"capture_annotations.note": keptEvidence, "capture_annotations.rect": keptEvidence, "capture_annotations.element": keptEvidence,
	"capture_annotations.observed": keptEvidence, "capture_annotations.sources": keptFacts,
	"capture_annotation_sets.capture_id": keptDerived, "capture_annotation_sets.id": keptDerived, "capture_annotation_sets.digest": keptDerived,
	"capture_annotation_sets.members": keptDerived, "capture_annotation_sets.at": keptDerived, "capture_annotation_sets.actor": keptOperator,
	"capture_annotation_sets.op_id": keptOpaque, "capture_files.capture_id": keptDerived, "capture_files.sha256": keptDerived,
	"capture_files.holder": keptDerived,
	// The sends' tables: #825's operations write them and decide each.
	"capture_sends.id": keptNone, "capture_sends.capture_id": keptNone, "capture_sends.kind": keptNone, "capture_sends.asked": keptNone,
	"capture_sends.digest": keptNone, "capture_sends.request": keptNone, "capture_sends.created_at": keptNone, "capture_sends.created_by": keptNone,
	"capture_sends.actor": keptNone, "capture_sends.op_id": keptNone, "capture_sends.fingerprint": keptNone,
	"capture_send_outcomes.send_id": keptNone, "capture_send_outcomes.id": keptNone, "capture_send_outcomes.kind": keptNone,
	"capture_send_outcomes.target": keptNone, "capture_send_outcomes.files": keptNone, "capture_send_outcomes.why": keptNone,
	"capture_send_outcomes.at": keptNone, "capture_send_outcomes.actor": keptNone, "capture_send_outcomes.op_id": keptNone,
	"capture_send_cancels.send_id": keptNone, "capture_send_cancels.op_id": keptNone, "capture_send_cancels.at": keptNone,
	"capture_send_cancels.actor": keptNone,
}

// Every text column version 5 adds has a decision (version5Kept): the
// tables and the columns a fresh store has that the version 4 fixture
// lacks, so a column added later with no decision fails here. A capture's
// address is the evidence: kept as the page gave it, its query and
// fragment with it, bounded and checked as an address, and printed by no
// log line.
func TestEveryVersionFiveTextHasADecision(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	old := openRaw(t, filepath.Join(fixtureV4(t), File))
	oldTables := tablesOfDB(t, old)
	found := map[string]bool{}
	for _, table := range tablesOfDB(t, s.db) {
		cols := textColumnsOf(t, s.db, table)
		if slices.Contains(oldTables, table) {
			oldCols := textColumnsOf(t, old, table)
			cols = slices.DeleteFunc(cols, func(c string) bool { return slices.Contains(oldCols, c) })
		}
		for _, c := range cols {
			found[table+"."+c] = true
			if _, ok := version5Kept[table+"."+c]; !ok {
				t.Errorf("the text column %s.%s has no decision in version5Kept", table, c)
			}
		}
	}
	for c := range version5Kept {
		if !found[c] {
			t.Errorf("version5Kept names %s, which is not a text column version 5 adds", c)
		}
	}
	// The address as the page gave it.
	n, f := testCapture(false)
	n.URL = markers[0]
	moment(&n)
	id := mustCreate(t, s, "address", n, f, nil)
	if c, err := s.Capture(ctx, id); err != nil || c.URL != markers[0] {
		t.Fatalf("the address kept: %q %v", c.URL, err)
	}
	for _, bad := range []string{"ftp://h/x", "http:///nohost", "http://h/" + strings.Repeat("a", MaxCaptureURL)} {
		n.URL = bad
		moment(&n)
		if _, _, err := checkCapture(n, f, false); err == nil {
			t.Errorf("the address %.40q is taken", bad)
		}
	}
	// No log line: the captures' code imports no logger.
	fset := token.NewFileSet()
	for _, name := range []string{"capture.go", "captures.go"} {
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if p := strings.Trim(imp.Path.Value, `"`); p == "log" || p == "log/slog" {
				t.Errorf("%s imports %s: a capture's address must be printed by no log line", name, p)
			}
		}
	}
}

// No text the store keeps carries an address's query, fragment or user:
// each string field of each new operation's record (found by its type),
// set to each marker in an otherwise good record and written, leaves no
// secret anywhere the store keeps or answers (every column of every table,
// the tickets' and the link's reads, an export), and no refusal's words
// hold one, but where its decision is the item's own words, which are kept
// as written (keptContent: the walk asks nothing of them). Every field has
// a decision; publications stays empty (no operation of this row writes
// it).
func TestNoTextKeepsAnAddressesQuery(t *testing.T) {
	for _, wr := range walkWriters {
		// The record as made is a good one: the operation does it.
		if refused := wr.run(t, newWalkStore(t), wr.record()); refused != "" {
			t.Fatalf("%s: the walk's own record is refused: %s", wr.name, refused)
		}
		for _, path := range stringPaths(reflect.TypeOf(wr.record()), nil) {
			name := pathName(path)
			kept, ok := fieldKept[name]
			if !ok {
				t.Errorf("%s: the field %s has no decision in fieldKept", wr.name, name)
				continue
			}
			for _, marker := range markers {
				t.Run(wr.name+"/"+name+"/"+marker, func(t *testing.T) {
					t.Parallel()
					w := newWalkStore(t)
					record := wr.record()
					setPath(reflect.ValueOf(record), path, marker)
					refused := wr.run(t, w, record)
					for _, secret := range secretsOf(marker) {
						if strings.Contains(refused, secret) {
							t.Errorf("the refusal's words hold %q: %s", secret, refused)
						}
						if held := holding(t, w, secret); kept != keptContent && len(held) > 0 {
							t.Errorf("%q is held by %v (refused: %q)", secret, held, refused)
						}
					}
					if n := countOf(t, w.s, "SELECT COUNT(*) FROM publications"); n != 0 {
						t.Errorf("%d publications written", n)
					}
				})
			}
		}
	}
}
