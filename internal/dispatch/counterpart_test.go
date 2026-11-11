package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
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
	if !reflect.DeepEqual(ab, ba) {
		t.Fatalf("the merge is not the same both ways:\n%+v\n%+v", ab, ba)
	}
	// Each replica's rows seen twice change nothing.
	if again := ReconcileCounterpart(append(append(slices.Clone(rb), ra...), append(slices.Clone(ra), rb...)...)); !reflect.DeepEqual(again, ab) {
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
		if got := ReconcileCounterpart(append(slices.Clone(rows), rows[:k]...)); !reflect.DeepEqual(got, held) {
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
		if !reflect.DeepEqual(got, CounterpartState{Title: "Title", Description: "Body"}) {
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
		if got := mergeOf(t, a, b, key); got.Description != "GitHub's body now" || !reflect.DeepEqual(got, stateOf(t, a, key)) {
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
		if !reflect.DeepEqual(got, stateOf(t, a, key)) || got.ExternalID != "github:github.com:I_Y" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("two refreshes whose order is the reverse of GitHub's", func(t *testing.T) {
		a, key := imported(t)
		b := fork(t, a)
		refreshed(t, a, "a-refresh", key, ghRead("I_X", 10, "Newer", "Body"))
		refreshed(t, b, "b-refresh", key, ghRead("I_X", 5, "Older", "Body"))
		if got := mergeOf(t, a, b, key); !reflect.DeepEqual(got, stateOf(t, a, key)) || got.Title != "Newer" {
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
		if got.Description != mine || ParseAuthority(got.Authority).Owners() != (Owners{Title: OwnerDispatch, Description: OwnerDispatch}) || !reflect.DeepEqual(got, stateOf(t, a, key)) {
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

// completion is one way a try at an image of a manifest ends: an
// attachment made, a skip for its type or its size recorded, or a failure
// that records nothing (its context gone), the entry left pending.
type completion string

const (
	attached   completion = "attached"
	skipType   completion = "type"
	skipSize   completion = "size"
	failedTry  completion = "failed"
	imageAsset            = "https://github.com/user-attachments/assets/i"
)

// complete ends a try at the image of the ticket's manifest as c says,
// under the op id.
func complete(t *testing.T, s *Store, key, image, op string, c completion) {
	t.Helper()
	ctx := context.Background()
	switch c {
	case attached:
		rc, err := s.ImportImage(ctx, testOp(op), key, image, ImageUpload{Name: "github-1.png", ContentType: "image/png", Body: bytes.NewReader([]byte(op))})
		want(t, rc, err, StatusDone, "")
	case skipType, skipSize:
		rc, err := s.ImportImage(ctx, testOp(op), key, image, ImageUpload{Skipped: string(c)})
		want(t, rc, err, StatusDone, "")
	case failedTry:
		gone, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.ImportImage(gone, testOp(op), key, image, ImageUpload{Name: "github-1.png", ContentType: "image/png", Body: bytes.NewReader([]byte(op))}); err == nil {
			t.Fatalf("a try with its context gone was not refused")
		}
	}
}

// heldAttachments is every attachment of the ticket the store holds, live or
// removed, each with whether it is removed.
func heldAttachments(t *testing.T, s *Store, ticketID string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, row := range texts(t, s, "SELECT id || ' ' || (deleted_at != '') FROM attachments WHERE ticket_id = ?", ticketID) {
		id, removed, _ := strings.Cut(row, " ")
		out[id] = removed == "1"
	}
	return out
}

// everyKeptAttachmentNamed fails the test unless the merged state keeps
// the invariant of mergeManifest over the attachments the replicas hold
// (each replica's heldAttachments): an attachment is live in the merge when
// a replica holds it and none removed it (the store's rule for a set: its
// one add, named by no remove), and then every entry that names an
// attachment names a live one, no two entries name one, nothing removed
// is named or released, nothing named is released, and every live
// attachment is named or released, once. It gives the manifest by image
// id.
func everyKeptAttachmentNamed(t *testing.T, got CounterpartState, replicas ...map[string]bool) map[string]SnapshotImage {
	t.Helper()
	held, removed := map[string]bool{}, map[string]bool{}
	for _, r := range replicas {
		for att, gone := range r {
			held[att] = true
			removed[att] = removed[att] || gone
		}
	}
	live := map[string]bool{}
	for att := range held {
		if !removed[att] {
			live[att] = true
		}
	}
	released := map[string]bool{}
	for _, att := range releasedBy(got) {
		if !live[att] || released[att] {
			t.Fatalf("the merge releases %s, which is not live or released twice (removed %v, released %v)", att, removed[att], releasedBy(got))
		}
		released[att] = true
	}
	sn, ok := ParseSnapshot(got.ExternalSnapshot)
	if !ok {
		t.Fatalf("no merged snapshot: %+v", got)
	}
	byID := map[string]SnapshotImage{}
	named := map[string]bool{}
	for _, im := range sn.Images {
		if _, twice := byID[im.ID]; twice {
			t.Fatalf("the image %s has two entries: %+v", im.ID, sn.Images)
		}
		byID[im.ID] = im
		if im.AttachmentID == "" {
			continue
		}
		switch att := im.AttachmentID; {
		case !live[att]:
			t.Fatalf("the entry %+v names an attachment that is not live (removed %v)", im, removed[att])
		case named[att]:
			t.Fatalf("two entries name the attachment %s: %+v", att, sn.Images)
		case released[att]:
			t.Fatalf("the entry %+v names an attachment the merge releases", im)
		}
		named[im.AttachmentID] = true
	}
	for att := range live {
		if !named[att] && !released[att] {
			t.Fatalf("the merge keeps the attachment %s, which no entry names: %+v (released %v)", att, sn.Images, releasedBy(got))
		}
	}
	return byID
}

// removed is a try at an image that makes its attachment, then a removal
// of that attachment (attachment.remove), which puts the entry back to
// pending.
const removed completion = "removed"

// walkOps is the operations the walk gives an image on a replica: an
// import, a skip, a failed try and a removal of an imported attachment.
var walkOps = []completion{attached, skipType, failedTry, removed}

// forkRun is what a replica forked from the walk's base did and holds:
// its ticket's change rows, its attachments (heldAttachments), and the
// attachment it made for each image it imported, by image.
type forkRun struct {
	rows        []ChangeRow
	attachments map[string]bool
	made        map[int]string
}

// The invariant of mergeManifest holds whatever completions two or three
// replicas made for an image and in whichever order: every entry that
// names an attachment names a live one, nothing removed is named or
// released, nothing named is released, and every live attachment is named
// or released; an attachment is never replaced by a completion that made
// none, of two live attachments of one image the first made in the one
// order is the entry's, and an image whose only attachments were removed
// is pending unless a skip was recorded. Walked for every ordered pair and
// every ordered triple of the operations walkOps (so each multiset in every
// order), on two and three replicas forked from one base, for one, two and
// three images, image i taking the tuple p+i so every tuple meets every
// place. Replicas are independent once forked, so the walk forks one per
// place and tuple of operations, each place's made after every earlier
// place's (two places never share an instant), and a merge of any tuple is
// the union of its places' rows; each replica alone keeps the invariant
// too (m1). Then past MaxImportImages, where pending entries and skips
// are cut before an attachment, an attachment cut is released, and an
// attachment removed is neither named nor released.
func TestEveryKeptAttachmentIsNamed(t *testing.T) {
	importN := func(t *testing.T, n int) (*Store, string) {
		s := linkedStore(t)
		newProject(t, s, "CND")
		read := ghRead("I_X", 0, "Title", "Body")
		read.Snapshot.Images = nil
		for i := 0; i < n; i++ {
			read.Snapshot.Images = append(read.Snapshot.Images, SnapshotImage{ID: fmt.Sprint(imageAsset, i)})
		}
		return s, mustImport(t, s, "imp", "CND", read)
	}
	var pairs, triples [][]completion
	for _, x := range walkOps {
		for _, y := range walkOps {
			pairs = append(pairs, []completion{x, y})
			for _, z := range walkOps {
				triples = append(triples, []completion{x, y, z})
			}
		}
	}
	for _, n := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("%d images", n), func(t *testing.T) {
			t.Parallel()
			base, key := importN(t, n)
			id := ticketOf(t, base, key).ID
			// tupleAt is what the replica at place j does to each image
			// in the walk's case p of the combos.
			tupleAt := func(combos [][]completion, p, j int) []completion {
				var out []completion
				for i := 0; i < n; i++ {
					out = append(out, combos[(p+i)%len(combos)][j])
				}
				return out
			}
			runs := map[string]*forkRun{}
			name := func(j int, ops []completion) string { return fmt.Sprint(j, ops) }
			for j := 0; j < 3; j++ {
				need := map[string][]completion{}
				for _, combos := range [][][]completion{pairs, triples} {
					for p := range combos {
						if j < len(combos[p]) {
							ops := tupleAt(combos, p, j)
							need[name(j, ops)] = ops
						}
					}
				}
				// Every replica of place j makes its tries after every
				// replica of place j-1 has made its own.
				t.Run(fmt.Sprint("place ", j), func(t *testing.T) {
					for label, ops := range need {
						run := &forkRun{made: map[int]string{}}
						runs[label] = run
						t.Run(label, func(t *testing.T) {
							t.Parallel()
							f := fork(t, base)
							for i, c := range ops {
								image := fmt.Sprint(imageAsset, i)
								op := fmt.Sprintf("p%d-%d-%s", j, i, c)
								if c != removed {
									complete(t, f, key, image, op, c)
									if c == attached {
										run.made[i] = ticketAttachmentOf(t, f, key, image)
									}
									continue
								}
								complete(t, f, key, image, op, attached)
								att := ticketAttachmentOf(t, f, key, image)
								rc, err := f.RemoveAttachment(context.Background(), testOp(op+"-rm"), key, att)
								want(t, rc, err, StatusDone, "")
								if im := ticketAttachmentOf(t, f, key, image); im != "" {
									t.Fatalf("the entry still names %s after its removal", im)
								}
							}
							m1(t, f, key, label)
							run.rows = ticketRows(t, f, id)
							run.attachments = heldAttachments(t, f, id)
						})
					}
				})
			}
			if t.Failed() {
				return
			}
			for _, combos := range [][][]completion{pairs, triples} {
				for p := range combos {
					var places []*forkRun
					var rows [][]ChangeRow
					var held []map[string]bool
					for j := range combos[p] {
						r := runs[name(j, tupleAt(combos, p, j))]
						places = append(places, r)
						rows = append(rows, r.rows)
						held = append(held, r.attachments)
					}
					what := fmt.Sprintf("%d images, case %d of %d places", n, p, len(places))
					got := ReconcileCounterpart(slices.Concat(rows...))
					// The replicas' rows in another order, and each seen twice,
					// make the same merge.
					back := slices.Clone(rows)
					slices.Reverse(back)
					for _, again := range [][]ChangeRow{slices.Concat(back...), slices.Concat(slices.Concat(back...), slices.Concat(rows...))} {
						if other := ReconcileCounterpart(again); !reflect.DeepEqual(other, got) {
							t.Fatalf("%s: the merge depends on the rows' order:\n%+v\n%+v", what, got, other)
						}
					}
					byID := everyKeptAttachmentNamed(t, got, held...)
					for i := 0; i < n; i++ {
						ops := combos[(p+i)%len(combos)]
						im := byID[fmt.Sprint(imageAsset, i)]
						first := slices.Index(ops, attached)
						switch {
						case first >= 0:
							if want := places[first].made[i]; im.AttachmentID != want || im.Skipped != "" {
								t.Fatalf("%s, image %d (%v): the entry %+v, not the first live attachment made, %s", what, i, ops, im, want)
							}
						case slices.Contains(ops, skipType):
							if im.Skipped == "" || im.AttachmentID != "" {
								t.Fatalf("%s, image %d (%v): a skip was recorded, yet %+v", what, i, ops, im)
							}
						case im.done():
							t.Fatalf("%s, image %d (%v): nothing live or skipped was recorded, yet %+v", what, i, ops, im)
						}
					}
				}
			}
		})
	}
	// Past the bound: A and B each read six new images after they parted
	// and complete them; the union is past MaxImportImages. In the last
	// case B removes each of its six once made, and the attachment A made
	// of image 0 before they parted, which A keeps.
	withImages := func(m int, ids ...string) ExternalRead {
		r := ghRead("I_X", m, "Title", "Body")
		r.Snapshot.Images = nil
		for _, id := range ids {
			r.Snapshot.Images = append(r.Snapshot.Images, SnapshotImage{ID: imageAsset + id})
		}
		return r
	}
	for _, c := range []struct {
		name string
		b    completion
	}{{"B attached its six", attached}, {"B skipped its six", skipSize}, {"B's six pending", failedTry}, {"B removed its six and A's first", removed}} {
		t.Run("past the bound, "+c.name, func(t *testing.T) {
			t.Parallel()
			a, key := importN(t, 0)
			refreshed(t, a, "base", key, withImages(1, "0", "1"))
			complete(t, a, key, imageAsset+"0", "base-0", attached)
			b := fork(t, a)
			ids := func(side string) []string {
				out := []string{"0", "1"}
				for i := 0; i < 6; i++ {
					out = append(out, fmt.Sprint(side, i))
				}
				return out
			}
			refreshed(t, a, "a-refresh", key, withImages(5, ids("a")...))
			refreshed(t, b, "b-refresh", key, withImages(9, ids("b")...))
			for i := 0; i < 6; i++ {
				complete(t, a, key, fmt.Sprint(imageAsset, "a", i), fmt.Sprint("a-", i), attached)
				if c.b != removed {
					complete(t, b, key, fmt.Sprint(imageAsset, "b", i), fmt.Sprint("b-", i), c.b)
					continue
				}
				image := fmt.Sprint(imageAsset, "b", i)
				complete(t, b, key, image, fmt.Sprint("b-", i), attached)
				rc, err := b.RemoveAttachment(context.Background(), testOp(fmt.Sprint("b-rm-", i)), key, ticketAttachmentOf(t, b, key, image))
				want(t, rc, err, StatusDone, "")
			}
			if c.b == removed {
				rc, err := b.RemoveAttachment(context.Background(), testOp("b-rm-first"), key, ticketAttachmentOf(t, b, key, imageAsset+"0"))
				want(t, rc, err, StatusDone, "")
			}
			m1(t, a, key, "a")
			m1(t, b, key, "b")
			got := mergeOf(t, a, b, key)
			id := ticketOf(t, a, key).ID
			byID := everyKeptAttachmentNamed(t, got, heldAttachments(t, a, id), heldAttachments(t, b, id))
			if len(byID) != MaxImportImages {
				t.Fatalf("the merged manifest holds %d entries", len(byID))
			}
			// A's seven attachments are kept before any skip or pending
			// entry; with B's six attachments beside them, the latest made
			// are cut and released; with B's removals, A's six live ones
			// alone, and the image A's first was made of pending.
			kept := 0
			for _, im := range byID {
				if im.AttachmentID != "" {
					kept++
				}
			}
			if wantKept := map[completion]int{attached: MaxImportImages, skipSize: 7, failedTry: 7, removed: 6}[c.b]; kept != wantKept {
				t.Fatalf("%d entries name an attachment, not %d: %+v", kept, wantKept, byID)
			}
			if wantReleased := map[completion]int{attached: 5, skipSize: 0, failedTry: 0, removed: 0}[c.b]; len(releasedBy(got)) != wantReleased {
				t.Fatalf("%d attachments released, not %d", len(releasedBy(got)), wantReleased)
			}
		})
	}
}

// ticketAttachmentOf is the attachment the store's manifest names for the
// image.
func ticketAttachmentOf(t *testing.T, s *Store, key, image string) string {
	t.Helper()
	for _, im := range snapshotOf(t, ticketOf(t, s, key)).Images {
		if im.ID == image {
			return im.AttachmentID
		}
	}
	t.Fatalf("%s has no entry for %s", key, image)
	return ""
}

// releasedBy is the attachments the merged state releases.
func releasedBy(got CounterpartState) []string { return got.Release }
