package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The invariants of version 4 that every operation keeps, each tested over
// every operation that could break it (Program item IN1a, review round 1
// of #780).

// secrets are the marks the address tests put where an address may carry
// a credential: a signed URL's query, a fragment, a user and password.
var secrets = []string{"SECRET", "hunter2"}

// holdingSecrets is every table and column of the store's database whose
// text holds one of the secrets, as "table.column", but the links table
// and the changes rows of a link: DS1's link.add keeps a link's URL as the
// person gave it, which is not this version's to change.
func holdingSecrets(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, table := range texts(t, s, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'tickets_fts%' ORDER BY name") {
		if table == "links" {
			continue
		}
		for _, col := range columnsOf(t, s.db, table) {
			for _, secret := range secrets {
				where := fmt.Sprintf(`CAST("%s" AS TEXT) LIKE ?`, col)
				if table == "changes" {
					where += " AND field != 'links'"
				}
				if countOf(t, s, fmt.Sprintf(`SELECT COUNT(*) FROM "%s" WHERE %s`, table, where), "%"+secret+"%") > 0 {
					out = append(out, table+"."+col)
				}
			}
		}
	}
	return out
}

// noSecret fails the test when the text holds one of the secrets.
func noSecret(t *testing.T, what, text string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Errorf("%s holds %q: %s", what, secret, text)
		}
	}
}

// No address the store keeps carries a query, a fragment or a user: an
// item's page with any is refused, its words naming the host and never the
// address; an image's id keeps its scheme, host and path (a signed URL's
// query left behind), one with a user is left out of the manifest; a
// check's page with any is not kept; an image named with its query is the
// manifest's entry without it; a read's failure keeps the addresses in its
// words without them; the close policy's words name a delivery without
// them. Then every column of every table, and every receipt, is walked:
// none holds a secret.
func TestNoAddressCarriesAQuery(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	for name, url := range map[string]string{
		"a query":    "https://github.com/rocksolidlabs/conductor/issues/612?token=SECRET",
		"a fragment": "https://github.com/rocksolidlabs/conductor/issues/612#SECRET",
		"a user":     "https://bob:hunter2@github.com/rocksolidlabs/conductor/issues/612",
		"an empty ?": "https://github.com/rocksolidlabs/conductor/issues/612?",
	} {
		read := ghRead("I_Q", 0, "T", "B")
		read.Snapshot.URL = url
		rc, err := s.ImportTicket(ctx, testOp("q-"+strings.Map(func(r rune) rune {
			if r == ' ' || r == '?' {
				return '-'
			}
			return r
		}, name)), ImportArgs{Project: "CND", URL: url}, read)
		want(t, rc, err, StatusFailed, RefusalInvalid)
		noSecret(t, name+"'s refusal", rc.Error)
		if !strings.Contains(rc.Error, "the item's page on github.com carries a query, a fragment or a user name") {
			t.Errorf("%s: the words: %s", name, rc.Error)
		}
		if strings.Contains(rc.Error, "rocksolidlabs/conductor") {
			t.Errorf("%s: the words name the address, not its host: %s", name, rc.Error)
		}
	}
	// The read each case changed is accepted.
	mustImport(t, s, "q-whole", "CND", ghRead("I_Q", 0, "T", "B"))
	// An http page off a loopback host: the words name the host alone.
	read := ghRead("I_Q", 0, "T", "B")
	read.Snapshot.URL = "http://github.com/rocksolidlabs/conductor/issues/612"
	rc, err := s.ImportTicket(ctx, testOp("q-http"), ImportArgs{Project: "CND", URL: "x"}, read)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if strings.Contains(rc.Error, "rocksolidlabs") {
		t.Errorf("an http page's words: %s", rc.Error)
	}
	// Images: the query and the fragment left behind, a user's left out.
	read = ghRead("I_1", 0, "T", "B")
	read.Snapshot.Images = []SnapshotImage{
		{ID: "https://private-user-images.githubusercontent.com/1/a.png?jwt=SECRET"},
		{ID: "https://github.com/user-attachments/assets/b#SECRET"},
		{ID: "https://bob:hunter2@github.com/user-attachments/assets/c"},
		{ID: "https://github.com/user-attachments/assets/b"},
	}
	key := mustImport(t, s, "imp", "CND", read)
	got := snapshotOf(t, ticketOf(t, s, key)).Images
	if !slices.Equal(got, []SnapshotImage{{ID: "https://private-user-images.githubusercontent.com/1/a.png"}, {ID: "https://github.com/user-attachments/assets/b"}}) {
		t.Fatalf("the manifest: %+v", got)
	}
	// The API names an image by the URL it fetched: the entry without its
	// query is the one made done.
	png := []byte("\x89PNG\r\n\x1a\n image")
	rc, err = s.ImportImage(ctx, testOp("img"), key, "https://private-user-images.githubusercontent.com/1/a.png?jwt=SECRET",
		ImageUpload{Name: "github-1.png", ContentType: "image/png", Body: bytes.NewReader(png)})
	want(t, rc, err, StatusDone, "")
	if im := snapshotOf(t, ticketOf(t, s, key)).Images[0]; im.AttachmentID != rc.ID {
		t.Fatalf("the entry: %+v", im)
	}
	rc, err = s.ImportImage(ctx, testOp("img2"), key, "https://github.com/user-attachments/assets/zz?jwt=SECRET", ImageUpload{Skipped: SkippedType})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	noSecret(t, "a stranger image's refusal", rc.Error)
	// A check's page with a query, a fragment or a user is not kept.
	_, other := newTicket(t, s, "CND", "Delivered")
	rc, err = addLink(s, "d1", other, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/9?token=SECRET"})
	want(t, rc, err, StatusDone, "")
	link := rc.ID
	pr := prRead("PR_9", 1, "open")
	pr.Checks = []LinkCheck{
		{ID: "1", Name: "ci", Status: "completed", Conclusion: "success", URL: "https://ci.example.com/build/1?token=SECRET"},
		{ID: "2", Name: "ci", Status: "completed", Conclusion: "success", URL: "https://ci.example.com/build/2#SECRET"},
		{ID: "3", Name: "ci", Status: "completed", Conclusion: "success", URL: "https://bob:hunter2@ci.example.com/build/3"},
		{ID: "4", Name: "ci", Status: "completed", Conclusion: "success", URL: "https://ci.example.com/build/4"},
	}
	if err := s.RecordRead(ctx, link, pr); err != nil {
		t.Fatal(err)
	}
	full, _, err := s.LinkReadOf(ctx, link)
	if err != nil || full.Checks[0].URL != "" || full.Checks[1].URL != "" || full.Checks[2].URL != "" || full.Checks[3].URL != "https://ci.example.com/build/4" {
		t.Fatalf("the checks' pages: %+v %v", full.Checks, err)
	}
	// A read's failure keeps the addresses in its words without them.
	if err := s.RecordReadError(ctx, link, "GET https://api.github.com/repos/o/r/pulls/9?access_token=SECRET: 502"); err != nil {
		t.Fatal(err)
	}
	tk := ticketOf(t, s, key)
	if err := s.RecordTicketReadError(ctx, key, tk.ExternalID, "GET /repos/o/r/issues/612?access_token=SECRET#SECRET failed, see https://bob:hunter2@x.example.com/y"); err != nil {
		t.Fatal(err)
	}
	v, err := s.Ticket(ctx, key)
	if err != nil || v.ExternalRead == nil || !strings.Contains(v.ExternalRead.Error, "/repos/o/r/issues/612") {
		t.Fatalf("the ticket's read: %+v %v", v.ExternalRead, err)
	}
	noSecret(t, "the ticket's read error", v.ExternalRead.Error)
	// The close policy's words name a delivery without its query.
	if _, err := s.db.Exec(`UPDATE projects SET close_requires = '["merged"]' WHERE key = 'CND'`); err != nil {
		t.Fatal(err)
	}
	done := TicketDone
	rc, err = s.EditTicket(ctx, testOp("close"), other, TicketEdit{Set: TicketSet{Status: &done}})
	want(t, rc, err, StatusFailed, RefusalClosePolicy)
	noSecret(t, "the close policy's refusal", rc.Error)
	if !strings.Contains(rc.Error, "github.com/rocksolidlabs/conductor/pull/9") {
		t.Errorf("the close policy's words do not name the delivery: %s", rc.Error)
	}
	st, err := s.CloseState(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	noSecret(t, "the close state's words", st.MergedWhy)
	// The walk: nothing kept holds a secret.
	if held := holdingSecrets(t, s); len(held) > 0 {
		t.Fatalf("columns holding a secret: %v", held)
	}
}

// liveHolders is how many live tickets hold the identity as their linked
// issue.
func liveHolders(t *testing.T, s *Store, identity string) int {
	t.Helper()
	return countOf(t, s, "SELECT COUNT(*) FROM tickets WHERE external_id = ? AND deleted_at = ''", identity)
}

// One linked item, at most one live ticket: every operation that makes a
// ticket live or sets its link (an import, a link, a relink after an
// unlink, an undelete) is refused external_linked, naming the ticket that
// holds the item, when another live ticket holds it; a native create holds
// nothing. After every operation at most one live ticket holds the item.
func TestOneLiveTicketPerItem(t *testing.T) {
	ctx := context.Background()
	identity := "github:github.com:I_X"
	x := func(m int) ExternalRead { return ghRead("I_X", m, "X", "x") }
	for _, c := range []struct {
		name string
		// run makes the case's start and its operation, giving the
		// operation's receipt and the key of the ticket that holds X.
		run func(t *testing.T, s *Store) (Receipt, string)
	}{
		{"import", func(t *testing.T, s *Store) (Receipt, string) {
			holder := mustImport(t, s, "imp", "CND", x(0))
			rc, err := s.ImportTicket(ctx, testOp("op"), ImportArgs{Project: "CND", URL: "x"}, x(1))
			if err != nil {
				t.Fatal(err)
			}
			return rc, holder
		}},
		{"link", func(t *testing.T, s *Store) (Receipt, string) {
			holder := mustImport(t, s, "imp", "CND", x(0))
			_, other := newTicket(t, s, "CND", "Other")
			rc, err := s.LinkExternal(ctx, testOp("op"), LinkArgs{Key: other}, x(1))
			if err != nil {
				t.Fatal(err)
			}
			return rc, holder
		}},
		{"relink", func(t *testing.T, s *Store) (Receipt, string) {
			_, other := newTicket(t, s, "CND", "Other")
			rc, err := s.LinkExternal(ctx, testOp("l1"), LinkArgs{Key: other}, ghRead("I_Y", 0, "Y", "y"))
			want(t, rc, err, StatusDone, "")
			rc, err = s.UnlinkExternal(ctx, testOp("u1"), other)
			want(t, rc, err, StatusDone, "")
			holder := mustImport(t, s, "imp", "CND", x(0))
			rc, err = s.LinkExternal(ctx, testOp("op"), LinkArgs{Key: other}, x(1))
			if err != nil {
				t.Fatal(err)
			}
			return rc, holder
		}},
		{"undelete", func(t *testing.T, s *Store) (Receipt, string) {
			first := mustImport(t, s, "imp", "CND", x(0))
			rc, err := s.DeleteTicket(ctx, testOp("del"), first)
			want(t, rc, err, StatusDone, "")
			holder := mustImport(t, s, "imp2", "CND", x(1))
			rc, err = s.UndeleteTicket(ctx, testOp("op"), first)
			if err != nil {
				t.Fatal(err)
			}
			return rc, holder
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := linkedStore(t)
			newProject(t, s, "CND")
			rc, holder := c.run(t, s)
			if rc.Status != StatusFailed || rc.Refusal != RefusalExternalLinked || rc.Key != holder || !strings.Contains(rc.Error, holder) {
				t.Fatalf("the %s: %+v, want external_linked naming %s", c.name, rc, holder)
			}
			if n := liveHolders(t, s, identity); n != 1 {
				t.Fatalf("after the %s, %d live tickets hold the item", c.name, n)
			}
		})
	}
	// A native create holds nothing, and an undelete of a ticket whose item
	// no live ticket holds is done.
	s := linkedStore(t)
	newProject(t, s, "CND")
	key := mustImport(t, s, "imp", "CND", x(0))
	newTicket(t, s, "CND", "Native")
	rc, err := s.DeleteTicket(ctx, testOp("del"), key)
	want(t, rc, err, StatusDone, "")
	rc, err = s.UndeleteTicket(ctx, testOp("undel"), key)
	want(t, rc, err, StatusDone, "")
	if n := liveHolders(t, s, identity); n != 1 {
		t.Fatalf("%d live tickets hold the item", n)
	}
}

// A change GitHub made is a change: a refresh or a give back that writes a
// GitHub-owned title or description (observe rows) is named by
// ChangedSince for the run launched before it and by the words of the
// approval it made stale.
func TestGitHubChangesCountAsChanges(t *testing.T) {
	ctx := context.Background()
	t.Run("a refresh", func(t *testing.T) {
		s := linkedStore(t)
		newProject(t, s, "CND")
		key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "Title", "Body"))
		plan := simplePlan(t, s, key)
		l := mustLaunch(t, s, "l1", key, plan, "")
		refreshed(t, s, "r1", key, ghRead("I_1", 5, "A new title", "A new body"))
		got, err := s.ChangedSince(ctx, l.ID)
		if err != nil || !slices.Equal(got.Fields, []string{"title", "description"}) {
			t.Fatalf("ChangedSince after a refresh: %+v %v", got, err)
		}
		_, stale := approvedOf(t, s, key)
		if len(stale) != 1 || stale[0] == nil || stale[0].Words != "the title and the description changed since revision 1 was approved" {
			t.Fatalf("the stale words after a refresh: %+v", stale)
		}
	})
	t.Run("a give back", func(t *testing.T) {
		s := linkedStore(t)
		newProject(t, s, "CND")
		key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "Title", "Body"))
		owned(t, s, "take", key, FieldDescription, OwnerDispatch, nil)
		plan := simplePlan(t, s, key)
		l := mustLaunch(t, s, "l1", key, plan, "")
		fresh := ghRead("I_1", 5, "Title", "GitHub's body now")
		owned(t, s, "give", key, FieldDescription, "github", &fresh)
		got, err := s.ChangedSince(ctx, l.ID)
		if err != nil || !slices.Equal(got.Fields, []string{"description"}) {
			t.Fatalf("ChangedSince after a give back: %+v %v", got, err)
		}
		_, stale := approvedOf(t, s, key)
		if len(stale) != 1 || stale[0] == nil || stale[0].Words != "the description changed since revision 1 was approved" {
			t.Fatalf("the stale words after a give back: %+v", stale)
		}
	})
}

// A link's read is of the item the link was first read as: a read of
// another node id, host or system is refused (invalid) and the read kept
// stays, whatever its time; a link whose only row is a failure's takes its
// first read.
func TestLinkReadKeepsItsItem(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "Delivered")
	// Each case on a link of its own, first read as PR_9 after a failure;
	// the read the case changes is accepted there after its refusal.
	for i, c := range []struct {
		name   string
		mutate func(*LinkRead)
	}{
		{"another node id, newer", func(r *LinkRead) { r.NodeID = "PR_OTHER" }},
		{"another node id, older", func(r *LinkRead) { r.NodeID, r.UpdatedAt = "PR_OTHER", day0 }},
		{"another host", func(r *LinkRead) { r.Host = "ghe.example.com" }},
		{"another system", func(r *LinkRead) { r.System = "forge" }},
	} {
		rc, err := addLink(s, fmt.Sprint("d", i), key, NewLink{Kind: LinkDelivery, URL: fmt.Sprint("https://github.com/rocksolidlabs/conductor/pull/", 9+i)})
		want(t, rc, err, StatusDone, "")
		link := rc.ID
		if err := s.RecordReadError(ctx, link, "GitHub said 502"); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordRead(ctx, link, prRead("PR_9", 1, "open")); err != nil {
			t.Fatalf("the first read after a failure: %v", err)
		}
		r := prRead("PR_9", 9, "merged")
		c.mutate(&r)
		if err := s.RecordRead(ctx, link, r); !isRefusal(err, RefusalInvalid) || !strings.Contains(err.Error(), "the item the link was read as") {
			t.Errorf("%s: %v", c.name, err)
		}
		got, _, err := s.LinkReadOf(ctx, link)
		if err != nil || got.NodeID != "PR_9" || got.State != "open" || got.Merged {
			t.Fatalf("%s: the read kept: %+v %v", c.name, got, err)
		}
		if err := s.RecordRead(ctx, link, prRead("PR_9", 9, "merged")); err != nil {
			t.Errorf("%s: the read it changes is refused: %v", c.name, err)
		}
	}
}

// RecordRead normalises a copy of its read: the caller's checks keep their
// times as given, and two reads of one value recorded at once share
// nothing they write (the race detector's).
func TestRecordReadLeavesItsInputAlone(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "Delivered")
	var links []string
	for i := 0; i < 2; i++ {
		rc, err := addLink(s, fmt.Sprintf("d%d", i), key, NewLink{Kind: LinkDelivery, URL: fmt.Sprintf("https://github.com/rocksolidlabs/conductor/pull/%d", 9+i)})
		want(t, rc, err, StatusDone, "")
		links = append(links, rc.ID)
	}
	zone := time.FixedZone("UTC+5", 5*3600)
	r := prRead("PR_9", 1, "open")
	r.Checks[0].CompletedAt = day0.In(zone)
	if err := s.RecordRead(ctx, links[0], r); err != nil {
		t.Fatal(err)
	}
	if r.Checks[0].CompletedAt.Location() != zone {
		t.Fatalf("RecordRead changed its caller's check: %v", r.Checks[0].CompletedAt)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(links))
	shared := prRead("PR_10", 2, "open")
	shared.Checks[0].CompletedAt = day0.In(zone)
	for i, link := range links {
		wg.Add(1)
		go func() {
			defer wg.Done()
			read := shared
			if i == 0 {
				read.NodeID = "PR_9"
			}
			errs[i] = s.RecordRead(ctx, link, read)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	if got, _, _ := s.LinkReadOf(ctx, links[1]); got.Checks[0].CompletedAt.Location() != time.UTC || !got.Checks[0].CompletedAt.Equal(day0) {
		t.Fatalf("the read as kept: %v", got.Checks[0].CompletedAt)
	}
}

// A close gated by merged on a delivery whose URL is at its bound is done:
// closed_on names the link, whose URL its rows keep, and is at most
// MaxClosedOn at its worst as stored.
func TestGatedCloseOnALongDeliveryURL(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	if _, err := s.db.Exec(`UPDATE projects SET close_requires = '["merged"]' WHERE key = 'CND'`); err != nil {
		t.Fatal(err)
	}
	_, key := newTicket(t, s, "CND", "Ship it")
	url := "https://github.com/rocksolidlabs/conductor/pull/9/"
	url += strings.Repeat("p", MaxLinkURL-len(url))
	rc, err := addLink(s, "d1", key, NewLink{Kind: LinkDelivery, URL: url})
	want(t, rc, err, StatusDone, "")
	delivery := rc.ID
	r := prRead("PR_9", 1, "merged")
	r.MergeSha = strings.Repeat("b", 64)
	if err := s.RecordRead(ctx, delivery, r); err != nil {
		t.Fatal(err)
	}
	done := TicketDone
	rc, err = s.EditTicket(ctx, testOp("close"), key, TicketEdit{Set: TicketSet{Status: &done}})
	want(t, rc, err, StatusDone, "")
	tk, err := readTicket(ctx, s.db, key)
	if err != nil {
		t.Fatal(err)
	}
	var on map[string]any
	if json.Unmarshal(tk.ClosedOn, &on) != nil || on["linkId"] != delivery || len(tk.ClosedOn) > MaxClosedOn {
		t.Fatalf("closed_on: %s", tk.ClosedOn)
	}
	// Its worst: every part at its bound, as stored.
	worst, err := encodeValue(ClosedOn{LinkID: strings.Repeat("f", IDLen), MergedAt: day0.Add(time.Nanosecond), MergeSha: strings.Repeat("b", 64),
		ReadAt: day0.Add(time.Nanosecond)})
	if err != nil || len(worst) > MaxClosedOn {
		t.Fatalf("the worst closed_on is %d bytes, its bound %d (%v)", len(worst), MaxClosedOn, err)
	}
}
