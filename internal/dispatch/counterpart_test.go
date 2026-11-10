package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fork is a second replica of s: a copy of its database, opened with
// github, closed with the test. Its rows are s's until either writes.
func fork(t *testing.T, s *Store) *Store {
	t.Helper()
	dir := storeDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshot(context.Background(), filepath.Join(dir, File)); err != nil {
		t.Fatal(err)
	}
	r, err := OpenOptions(dir, Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// stateOf is the ticket's counterpart group and its two fields as the
// store holds them.
func stateOf(t *testing.T, s *Store, key string) CounterpartState {
	t.Helper()
	tk := ticketOf(t, s, key)
	return CounterpartState{ExternalID: tk.ExternalID, ExternalURL: tk.ExternalURL, ExternalSnapshot: string(tk.ExternalSnapshot),
		Authority: string(tk.Authority), Title: tk.Title, Description: tk.Description}
}

// mergeOf is what ReconcileCounterpart makes of the two replicas' rows of
// the ticket, failing the test unless both orders give one result.
func mergeOf(t *testing.T, a, b *Store, key string) CounterpartState {
	t.Helper()
	id := ticketOf(t, a, key).ID
	ra, rb := ticketRows(t, a, id), ticketRows(t, b, id)
	ab := ReconcileCounterpart(append(slices.Clone(ra), rb...))
	ba := ReconcileCounterpart(append(slices.Clone(rb), ra...))
	if ab != ba {
		t.Fatalf("the merge is not the same both ways:\n%+v\n%+v", ab, ba)
	}
	// Each replica's rows seen twice change nothing.
	if again := ReconcileCounterpart(append(append(slices.Clone(rb), ra...), append(slices.Clone(ra), rb...)...)); again != ab {
		t.Fatalf("the merge with every row twice:\n%+v\n%+v", again, ab)
	}
	return ab
}

// m1 fails the test unless ReconcileCounterpart over the store's own rows
// of the ticket, alone, merged with themselves, and merged with each
// prefix of themselves, gives the state the store holds (M1).
func m1(t *testing.T, s *Store, key, step string) {
	t.Helper()
	held := stateOf(t, s, key)
	rows := ticketRows(t, s, ticketOf(t, s, key).ID)
	for k := 0; k <= len(rows); k++ {
		if got := ReconcileCounterpart(append(slices.Clone(rows), rows[:k]...)); got != held {
			t.Fatalf("%s: the merge of the store's own rows (with its first %d again) is\n%+v\nnot what it holds\n%+v", step, k, got, held)
		}
	}
}

// refreshed refreshes the ticket with the read, failing the test unless it
// was done.
func refreshed(t *testing.T, s *Store, opID, key string, read ExternalRead) {
	t.Helper()
	rc, err := s.RefreshExternal(context.Background(), testOp(opID), key, read)
	want(t, rc, err, StatusDone, "")
}

// owned moves the field to the owner, failing the test unless done.
func owned(t *testing.T, s *Store, opID, key, field, owner string, read *ExternalRead) {
	t.Helper()
	rc, err := s.SetAuthority(context.Background(), testOp(opID), AuthorityArgs{Key: key, Field: field, Owner: owner}, read)
	want(t, rc, err, StatusDone, "")
}

// Each pair of operations on two replicas merges both ways to one result,
// the counterpart group decided as one (rules 1 to 5).
func TestCounterpartMerge(t *testing.T) {
	ctx := context.Background()
	imported := func(t *testing.T) (*Store, string) {
		s := linkedStore(t)
		newProject(t, s, "CND")
		return s, mustImport(t, s, "imp", "CND", ghRead("I_X", 0, "Title", "Body"))
	}
	t.Run("unlink against refresh", func(t *testing.T) {
		a, key := imported(t)
		b := fork(t, a)
		rc, err := a.UnlinkExternal(ctx, testOp("a-unlink"), key)
		want(t, rc, err, StatusDone, "")
		refreshed(t, b, "b-refresh", key, ghRead("I_X", 5, "Their new title", "Body"))
		got := mergeOf(t, a, b, key)
		if got != (CounterpartState{Title: "Title", Description: "Body"}) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("take over against refresh", func(t *testing.T) {
		a, key := imported(t)
		b := fork(t, a)
		owned(t, a, "a-take", key, FieldTitle, OwnerDispatch, nil)
		refreshed(t, b, "b-refresh", key, ghRead("I_X", 5, "Their new title", "Body"))
		got := mergeOf(t, a, b, key)
		if got.Title != "Title" || got.ExternalSnapshot != stateOf(t, b, key).ExternalSnapshot || got.Authority != stateOf(t, a, key).Authority {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("give back against a Dispatch edit", func(t *testing.T) {
		a, key := imported(t)
		owned(t, a, "take", key, FieldDescription, OwnerDispatch, nil)
		b := fork(t, a)
		fresh := ghRead("I_X", 5, "Title", "GitHub's body now")
		owned(t, a, "a-give", key, FieldDescription, "github", &fresh)
		mine := "B's edit"
		mustEdit(t, b, "b-edit", key, TicketEdit{Set: TicketSet{Description: &mine}})
		if got := mergeOf(t, a, b, key); got.Description != "GitHub's body now" || got != stateOf(t, a, key) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("relink to another item against a refresh of the first", func(t *testing.T) {
		a := linkedStore(t)
		newProject(t, a, "CND")
		_, key := makeTicket(t, a, NewTicket{Project: "CND", Title: "Native", Description: "Mine"})
		rc, err := a.LinkExternal(ctx, testOp("link-x"), LinkArgs{Key: key}, ghRead("I_X", 0, "X", "x"))
		want(t, rc, err, StatusDone, "")
		b := fork(t, a)
		rc, err = a.UnlinkExternal(ctx, testOp("a-unlink"), key)
		want(t, rc, err, StatusDone, "")
		y := ghRead("I_Y", 1, "Y", "y")
		y.Snapshot.URL = "https://github.com/rocksolidlabs/conductor/issues/700"
		rc, err = a.LinkExternal(ctx, testOp("a-link-y"), LinkArgs{Key: key}, y)
		want(t, rc, err, StatusDone, "")
		refreshed(t, b, "b-refresh", key, ghRead("I_X", 5, "X again", "x"))
		got := mergeOf(t, a, b, key)
		if got != stateOf(t, a, key) || got.ExternalID != "github:github.com:I_Y" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("two refreshes whose order is the reverse of GitHub's", func(t *testing.T) {
		a, key := imported(t)
		b := fork(t, a)
		refreshed(t, a, "a-refresh", key, ghRead("I_X", 10, "Newer", "Body"))
		refreshed(t, b, "b-refresh", key, ghRead("I_X", 5, "Older", "Body"))
		if got := mergeOf(t, a, b, key); got != stateOf(t, a, key) || got.Title != "Newer" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("an image's commit against a newer refresh made before it", func(t *testing.T) {
		a, key := imported(t)
		b := fork(t, a)
		refreshed(t, b, "b-refresh", key, ghRead("I_X", 5, "Title", "Body changed"))
		img := ghRead("I_X", 0, "", "").Snapshot.Images[0].ID
		rc, err := a.ImportImage(ctx, testOp("a-image"), key, img, ImageUpload{Name: "github-1.png", ContentType: "image/png", Body: bytes.NewReader([]byte("png"))})
		want(t, rc, err, StatusDone, "")
		got := mergeOf(t, a, b, key)
		sn, _ := ParseSnapshot(got.ExternalSnapshot)
		if !sn.UpdatedAt.Equal(day0.Add(5*time.Minute)) || sn.Images[0].AttachmentID != rc.ID || got.Description != "Body changed" {
			t.Fatalf("%+v", sn)
		}
	})
	t.Run("a manifest grown on both replicas", func(t *testing.T) {
		// A refreshes with x2 and makes it an attachment; B, never having
		// had x2, refreshes later with x3 and skips it. The merged manifest
		// keeps both done entries, so no attachment is left that no
		// manifest names.
		a, key := imported(t)
		b := fork(t, a)
		withImages := func(m int, ids ...string) ExternalRead {
			r := ghRead("I_X", m, "Title", "Body")
			r.Snapshot.Images = nil
			for _, id := range ids {
				r.Snapshot.Images = append(r.Snapshot.Images, SnapshotImage{ID: "https://github.com/user-attachments/assets/" + id})
			}
			return r
		}
		refreshed(t, a, "a-refresh", key, withImages(5, "a1", "x2"))
		rc, err := a.ImportImage(ctx, testOp("a-image"), key, "https://github.com/user-attachments/assets/x2",
			ImageUpload{Name: "github-1.png", ContentType: "image/png", Body: bytes.NewReader([]byte("x2"))})
		want(t, rc, err, StatusDone, "")
		x2 := rc.ID
		refreshed(t, b, "b-refresh", key, withImages(9, "a1", "x3"))
		rc, err = b.ImportImage(ctx, testOp("b-image"), key, "https://github.com/user-attachments/assets/x3", ImageUpload{Skipped: SkippedSize})
		want(t, rc, err, StatusDone, "")
		got := mergeOf(t, a, b, key)
		sn, _ := ParseSnapshot(got.ExternalSnapshot)
		byID := map[string]SnapshotImage{}
		for _, im := range sn.Images {
			byID[strings.TrimPrefix(im.ID, "https://github.com/user-attachments/assets/")] = im
		}
		if !sn.UpdatedAt.Equal(day0.Add(9*time.Minute)) || byID["x2"].AttachmentID != x2 || byID["x3"].Skipped != SkippedSize || len(sn.Images) != 4 {
			t.Fatalf("the merged manifest: %+v", sn.Images)
		}
	})
	t.Run("the same item linked again after the other replica took a field over", func(t *testing.T) {
		// A unlinks X; B takes X's title over; A edits its description and
		// links X again, every field Dispatch's. The relink's owners are the
		// last owner change of X, so B's older one does not hand A's
		// description back to GitHub.
		a, key := imported(t)
		b := fork(t, a)
		rc, err := a.UnlinkExternal(ctx, testOp("a-unlink"), key)
		want(t, rc, err, StatusDone, "")
		owned(t, b, "b-take", key, FieldTitle, OwnerDispatch, nil)
		mine := "A's own words"
		mustEdit(t, a, "a-edit", key, TicketEdit{Set: TicketSet{Description: &mine}})
		rc, err = a.LinkExternal(ctx, testOp("a-relink"), LinkArgs{Key: key}, ghRead("I_X", 3, "Title", "Body"))
		want(t, rc, err, StatusDone, "")
		got := mergeOf(t, a, b, key)
		if got.Description != mine || ParseAuthority(got.Authority).Owners() != (Owners{Title: OwnerDispatch, Description: OwnerDispatch}) || got != stateOf(t, a, key) {
			t.Fatalf("the merge: %+v\nA holds %+v", got, stateOf(t, a, key))
		}
	})
	t.Run("give back on one replica against a refresh past it on the other", func(t *testing.T) {
		a, key := imported(t)
		owned(t, a, "take", key, FieldDescription, OwnerDispatch, nil)
		b := fork(t, a)
		fresh := ghRead("I_X", 5, "Title", "Given back")
		owned(t, a, "a-give", key, FieldDescription, "github", &fresh)
		refreshed(t, b, "b-refresh", key, ghRead("I_X", 9, "Title", "Past it"))
		got := mergeOf(t, a, b, key)
		sn, _ := ParseSnapshot(got.ExternalSnapshot)
		if !sn.UpdatedAt.Equal(day0.Add(9*time.Minute)) || got.Description != "Given back" || !got.DropRead {
			t.Fatalf("%+v", got)
		}
	})
	// Sequential histories on one replica: each merged with itself and with
	// an empty replica is the replica's state.
	t.Run("a Dispatch text given back and taken over with no edit", func(t *testing.T) {
		s := linkedStore(t)
		newProject(t, s, "CND")
		_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Mine", Description: "My words"})
		rc, err := s.LinkExternal(ctx, testOp("link"), LinkArgs{Key: key}, ghRead("I_X", 0, "GitHub's", "Theirs"))
		want(t, rc, err, StatusDone, "")
		fresh := ghRead("I_X", 1, "GitHub's", "Theirs")
		owned(t, s, "give", key, FieldTitle, "github", &fresh)
		owned(t, s, "take", key, FieldTitle, OwnerDispatch, nil)
		m1(t, s, key, "given back and taken over")
		if got := stateOf(t, s, key); got.Title != "GitHub's" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("an imported ticket taken over, and one unlinked", func(t *testing.T) {
		s, key := imported(t)
		owned(t, s, "take", key, FieldTitle, OwnerDispatch, nil)
		m1(t, s, key, "taken over")
		rc, err := s.UnlinkExternal(ctx, testOp("unlink"), key)
		want(t, rc, err, StatusDone, "")
		m1(t, s, key, "unlinked")
		if got := stateOf(t, s, key); got.Title != "Title" || got.Description != "Body" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a title refresh then a labels-only refresh", func(t *testing.T) {
		s, key := imported(t)
		refreshed(t, s, "r1", key, ghRead("I_X", 1, "Second title", "Body"))
		labels := ghRead("I_X", 2, "Second title", "Body")
		labels.Snapshot.Labels = []string{"bug", "p0"}
		refreshed(t, s, "r2", key, labels)
		m1(t, s, key, "labels only")
		if got := stateOf(t, s, key); got.Title != "Second title" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a move then reads that wrote no URL", func(t *testing.T) {
		s, key := imported(t)
		moved := ghRead("I_X", 1, "Title", "Body")
		moved.Snapshot.URL = "https://github.com/rocksolidlabs/renamed/issues/612"
		refreshed(t, s, "r1", key, moved)
		after := moved
		after.Snapshot.UpdatedAt, after.Snapshot.Labels = day0.Add(2*time.Minute), []string{"later"}
		refreshed(t, s, "r2", key, after)
		m1(t, s, key, "moved")
		if got := stateOf(t, s, key); got.ExternalURL != moved.Snapshot.URL {
			t.Fatalf("%+v", got)
		}
	})
}

// M1: on one replica, random sequences of imports, links, unlinks,
// refreshes (whole, labels-only, moved, a CRLF-only body change, images
// added or gone), take overs, give backs, edits and image commits leave a
// store whose own rows, merged alone, with themselves or with any prefix of
// themselves, give the state it holds, after every step.
func TestCounterpartReplaysOneReplica(t *testing.T) {
	for seed := uint64(1); seed <= 12; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			replayOneReplica(t, rand.New(rand.NewPCG(seed, 765)), 30)
		})
	}
}

// Two replicas forked at a random step, each driven by its own random
// sequence against one GitHub, merge both ways to one state, every row
// seen twice changing nothing, and, when neither side linked or unlinked
// after the fork, the merged manifest names every done entry either
// replica's manifest holds while they fit MaxImportImages.
func TestCounterpartMergesTwoReplicas(t *testing.T) {
	for seed := uint64(1); seed <= 12; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, 780))
			g := newGitHub()
			a := linkedStore(t)
			newProject(t, a, "CND")
			key := g.start(t, r, a)
			for step := 0; step < r.IntN(8); step++ {
				g.step(t, r, a, key, fmt.Sprintf("s%d", step))
			}
			b := fork(t, a)
			relinked := false
			for step := 0; step < 12; step++ {
				s, side := a, "a"
				if r.IntN(2) == 0 {
					s, side = b, "b"
				}
				if name := g.step(t, r, s, key, fmt.Sprintf("%s%d", side, step)); name == "link" || name == "unlink" {
					relinked = true
				}
			}
			got := mergeOf(t, a, b, key)
			sn, ok := ParseSnapshot(got.ExternalSnapshot)
			if got.ExternalID == "" {
				if got.ExternalSnapshot != "" || got.ExternalURL != "" || got.Authority != "" {
					t.Fatalf("no item decided, yet %+v", got)
				}
				return
			}
			if !ok || sn.Identity() != got.ExternalID {
				t.Fatalf("the merged snapshot is not of %s: %+v", got.ExternalID, got)
			}
			done := map[string]bool{}
			for _, s := range []*Store{a, b} {
				tk := ticketOf(t, s, key)
				if tk.ExternalID != got.ExternalID {
					continue
				}
				for _, im := range snapshotOf(t, tk).Images {
					if im.done() {
						done[im.ID] = true
					}
				}
			}
			// A manifest is its link's: after an unlink or a link on either
			// side, the entries before it are another period's (rule 2).
			if relinked || len(done) > MaxImportImages {
				return
			}
			for id := range done {
				if !slices.ContainsFunc(sn.Images, func(im SnapshotImage) bool { return im.ID == id && im.done() }) {
					t.Fatalf("the merged manifest lost the done entry %s: %+v", id, sn.Images)
				}
			}
		})
	}
}

// item is a GitHub item as the random sequence keeps changing it.
type item struct {
	node, url, title, body string
	labels                 []string
	images                 []string
}

// gitHub is the GitHub a random sequence reads: its two items, and its
// clock in minutes of day0, which every read moves on.
type gitHub struct {
	minute int
	items  map[string]*item
	images int
}

// newGitHub is a GitHub of two items, X with two images and Y with one.
func newGitHub() *gitHub {
	return &gitHub{items: map[string]*item{
		"X": {node: "I_X", url: "https://github.com/rocksolidlabs/conductor/issues/612", title: "X", body: "x body", labels: []string{"bug"},
			images: []string{"https://github.com/user-attachments/assets/x1", "https://github.com/user-attachments/assets/x2"}},
		"Y": {node: "I_Y", url: "https://github.com/rocksolidlabs/conductor/issues/700", title: "Y", body: "y body",
			images: []string{"https://github.com/user-attachments/assets/y1"}},
	}}
}

// readOf is a read of the item now.
func (g *gitHub) readOf(it *item) ExternalRead {
	g.minute++
	read := ghRead(it.node, g.minute, it.title, it.body)
	read.Snapshot.URL, read.Snapshot.Labels = it.url, slices.Clone(it.labels)
	read.Snapshot.Images = nil
	for _, im := range it.images {
		read.Snapshot.Images = append(read.Snapshot.Images, SnapshotImage{ID: im})
	}
	return read
}

// start makes the sequence's ticket in the store: an import of X or a
// native ticket, at random.
func (g *gitHub) start(t *testing.T, r *rand.Rand, s *Store) string {
	t.Helper()
	if r.IntN(2) == 0 {
		return mustImport(t, s, "start", "CND", g.readOf(g.items["X"]))
	}
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Native", Description: "Native words"})
	return key
}

// replayOneReplica drives steps random operations on one ticket of one
// store and asserts M1 after each.
func replayOneReplica(t *testing.T, r *rand.Rand, steps int) {
	s := linkedStore(t)
	newProject(t, s, "CND")
	g := newGitHub()
	key := g.start(t, r, s)
	m1(t, s, key, "the start")
	for step := 0; step < steps; step++ {
		name := g.step(t, r, s, key, fmt.Sprintf("s%d", step))
		m1(t, s, key, fmt.Sprintf("step %d, %s", step, name))
	}
}

// step makes one random operation on the store's ticket under the op id,
// GitHub's items changing as the operation says, and names it.
func (g *gitHub) step(t *testing.T, r *rand.Rand, s *Store, key, op string) string {
	t.Helper()
	ctx := context.Background()
	tk := ticketOf(t, s, key)
	if tk.ExternalID == "" {
		it := g.items["X"]
		if r.IntN(2) == 0 {
			it = g.items["Y"]
		}
		rc, err := s.LinkExternal(ctx, testOp(op), LinkArgs{Key: key}, g.readOf(it))
		if err != nil || (rc.Status != StatusDone && rc.Refusal != RefusalStaleRead) {
			t.Fatalf("the link: %+v %v", rc, err)
		}
		return "link"
	}
	it := g.items["X"]
	if strings.HasSuffix(tk.ExternalID, "I_Y") {
		it = g.items["Y"]
	}
	a := ParseAuthority(string(tk.Authority))
	field := []string{FieldTitle, FieldDescription}[r.IntN(2)]
	var name string
	switch r.IntN(12) {
	case 0:
		name = "unlink"
		rc, err := s.UnlinkExternal(ctx, testOp(op), key)
		want(t, rc, err, StatusDone, "")
	case 1, 2:
		name = "a whole refresh"
		it.title, it.body = fmt.Sprintf("%s title %s", it.node, op), fmt.Sprintf("body %s\nline", op)
	case 3:
		name = "a labels-only refresh"
		it.labels = append(it.labels, "l"+op)
	case 4:
		name = "a move"
		it.url = fmt.Sprintf("https://github.com/rocksolidlabs/moved-%s/issues/612", op)
	case 5:
		name = "a CRLF-only change"
		if strings.Contains(it.body, "\r\n") {
			it.body = strings.ReplaceAll(it.body, "\r\n", "\n")
		} else {
			it.body = strings.ReplaceAll(it.body, "\n", "\r\n")
		}
	case 6:
		if a.owner(field) != "" {
			name = "a take over of the " + field
			owned(t, s, op, key, field, OwnerDispatch, nil)
		} else {
			name = "a give back of the " + field
			read := g.readOf(it)
			rc, err := s.SetAuthority(ctx, testOp(op), AuthorityArgs{Key: key, Field: field, Owner: "github"}, &read)
			if err != nil || (rc.Status != StatusDone && rc.Refusal != RefusalStaleRead) {
				t.Fatalf("the give back: %+v %v", rc, err)
			}
		}
	case 7:
		name = "an edit of the " + field
		text := "edited " + op
		set := TicketSet{Title: &text}
		if field == FieldDescription {
			set = TicketSet{Description: &text}
		}
		rc, err := s.EditTicket(ctx, testOp(op), key, TicketEdit{Set: set})
		if err != nil || (rc.Status != StatusDone && rc.Refusal != RefusalExternalOwned) {
			t.Fatalf("the edit: %+v %v", rc, err)
		}
	case 8:
		name = "an image added to the body"
		g.images++
		it.images = append(it.images, fmt.Sprintf("https://github.com/user-attachments/assets/n%d", g.images))
	case 9:
		name = "an image gone from the body"
		if len(it.images) > 0 {
			it.images = slices.Delete(slices.Clone(it.images), 0, 1)
		}
	default:
		name = "an image's commit"
		sn, _ := ParseSnapshot(string(tk.ExternalSnapshot))
		i := slices.IndexFunc(sn.Images, func(im SnapshotImage) bool { return !im.done() })
		if i < 0 {
			return "no image pending"
		}
		img := ImageUpload{Skipped: SkippedSize}
		if r.IntN(2) == 0 {
			img = ImageUpload{Name: "github.png", ContentType: "image/png", Body: bytes.NewReader([]byte(sn.Images[i].ID))}
		}
		rc, err := s.ImportImage(ctx, testOp(op), key, sn.Images[i].ID, img)
		want(t, rc, err, StatusDone, "")
	}
	if strings.Contains(name, "refresh") || strings.Contains(name, "body") || name == "a move" || name == "a CRLF-only change" {
		read := g.readOf(it)
		needed, err := RefreshNeeded(tk, read)
		switch {
		case err != nil:
			t.Fatal(err)
		case needed:
			refreshed(t, s, op, key, read)
		default:
			if err := s.RecordTicketRead(ctx, key, tk.ExternalID, TicketRead{UpdatedAt: read.Snapshot.UpdatedAt}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return name
}
