package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// The sweep (docs/plans/dispatch.md, PR-7): it deletes what nothing
// names, never what a live row or a lease names whatever its age; it
// expires the leases in a transaction of its own committed before any
// file goes, so an upload paused across a second store's sweep either
// names a file still there or is refused expired. Nothing waits on the
// clock: an upload is held at its seam until the test releases it, and a
// grace is set, not waited out. Run with -count=30.

// held is an upload on a goroutine of its own, held at a seam until the
// test resumes it.
type held struct {
	release chan struct{}
	done    chan uploaded
}

// uploaded is what an upload answered.
type uploaded struct {
	rc  Receipt
	a   Attachment
	err error
}

// holdAt starts an upload of content to the ticket with the key under the
// op id, held at seam (&s.beforePublish or &s.beforeCommit), and returns
// once the upload is there; the seam is cleared for the uploads after it.
func holdAt(t *testing.T, s *Store, seam *func(), id, key string, content []byte) *held {
	t.Helper()
	reached := make(chan struct{})
	h := &held{release: make(chan struct{}), done: make(chan uploaded, 1)}
	*seam = func() {
		close(reached)
		<-h.release
	}
	go func() {
		rc, a, err := put(s, id, key, "held.txt", content)
		h.done <- uploaded{rc, a, err}
	}()
	select {
	case <-reached:
	case u := <-h.done:
		t.Fatalf("the upload ended before its seam: %+v", u)
	}
	*seam = nil
	return h
}

// resume releases the held upload and returns its answer.
func (h *held) resume() uploaded {
	close(h.release)
	return <-h.done
}

// noTrace fails the test unless the store holds nothing of the content:
// no attachment row naming it, no lease, no file of it published or in
// flight.
func noTrace(t *testing.T, s *Store, content []byte) {
	t.Helper()
	sum := sumOf(content)
	if n := count(t, s, "SELECT COUNT(*) FROM attachments WHERE sha256 = ?", sum); n != 0 {
		t.Fatalf("%d attachment rows name the content", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM uploads"); n != 0 {
		t.Fatalf("%d leases", n)
	}
	if exists(t, contentAt(s, sum)) || len(tempFiles(t, s)) != 0 {
		t.Fatalf("a file of the content is left: published %v, temp files %v", exists(t, contentAt(s, sum)), tempFiles(t, s))
	}
}

// An upload held at beforeCommit, its file published and its lease
// committed, while a second store opens on the directory and sweeps with
// an hour's grace, names a file still there once it commits: the lease
// names it. The same with content uploaded before, its only attachment
// removed, and its file's modification time set back a day once the held
// upload has published it, so both of the second store's sweeps see a file
// a day old that only the lease names: the lease, not the file's age,
// keeps it, and it opens whole after the commit.
func TestAnUploadHeldAcrossASecondStoresSweepNamesItsFile(t *testing.T) {
	ctx := context.Background()
	for _, reused := range []bool{false, true} {
		t.Run(fmt.Sprintf("reused=%v", reused), func(t *testing.T) {
			dir := storeDir(t)
			s := openStore(t, dir)
			newProject(t, s, "CND")
			_, key := newTicket(t, s, "CND", "Attached")
			content := []byte("held across a sweep")
			if reused {
				a := mustPut(t, s, "a0", key, "first.txt", content)
				rc, err := s.RemoveAttachment(ctx, testOp("r0"), key, a.ID)
				want(t, rc, err, StatusDone, "")
			}
			h := holdAt(t, s, &s.beforeCommit, "a1", key, content)
			if reused {
				// After the publication, which on unix renamed a new file
				// into the old one's place.
				day := time.Now().Add(-24 * time.Hour)
				if err := os.Chtimes(contentAt(s, sumOf(content)), day, day); err != nil {
					t.Fatal(err)
				}
			}
			second := openStore(t, dir)
			if err := second.SweepError(); err != nil {
				t.Fatalf("the second store's sweep at open: %v", err)
			}
			if err := second.sweep(ctx, time.Now()); err != nil {
				t.Fatal(err)
			}
			if !exists(t, contentAt(s, sumOf(content))) {
				t.Fatal("the sweep deleted a file a lease names")
			}
			u := h.resume()
			want(t, u.rc, u.err, StatusDone, "")
			if !bytes.Equal(readContent(t, s, u.a.ID), content) || !bytes.Equal(readContent(t, second, u.a.ID), content) {
				t.Fatal("the attachment did not open whole")
			}
		})
	}
}

// An upload held at beforeCommit, or at beforePublish, while a second
// store sweeps with its grace at zero, the lease older than it, is refused
// expired when it resumes: a failed receipt, kept, no attachment row and
// no file of its content, published or in flight, left in the directory.
// The same content uploaded again under a new op id is taken. The same
// with the second store's unlink deleting each file and then failing: the
// sweep returns the error, and the upload is still refused expired with no
// file of its content left, its lease's expiry having been committed
// before any unlink.
func TestAnUploadWhoseLeaseASweepExpiredIsRefusedExpired(t *testing.T) {
	ctx := context.Background()
	for _, at := range []string{"beforeCommit", "beforePublish"} {
		for _, failing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unlink failing=%v", at, failing), func(t *testing.T) {
				dir := storeDir(t)
				s := openStore(t, dir)
				newProject(t, s, "CND")
				_, key := newTicket(t, s, "CND", "Attached")
				content := []byte("held while its lease expires at " + at)
				seam := &s.beforeCommit
				if at == "beforePublish" {
					seam = &s.beforePublish
				}
				h := holdAt(t, s, seam, "a1", key, content)
				second := openStore(t, dir)
				second.sweepGrace = 0
				failure := errors.New("the disk refused, after the unlink")
				if failing {
					second.unlink = func(path string) error {
						if err := os.Remove(path); err != nil {
							return err
						}
						return failure
					}
				}
				if err := second.sweep(ctx, time.Now()); (err != nil) != failing || (failing && !errors.Is(err, failure)) {
					t.Fatalf("the sweep: %v", err)
				}
				u := h.resume()
				want(t, u.rc, u.err, StatusFailed, RefusalExpired)
				if u.a.ID != "" {
					t.Fatalf("an expired upload returned an attachment: %+v", u.a)
				}
				noTrace(t, s, content)
				if kept, found, err := s.Receipt(ctx, "test", "a1"); err != nil || !found || kept.Refusal != RefusalExpired {
					t.Fatalf("the refusal's receipt: %+v %v %v", kept, found, err)
				}
				a := mustPut(t, s, "a2", key, "again.txt", content)
				if !bytes.Equal(readContent(t, s, a.ID), content) {
					t.Fatal("the upload again did not open whole")
				}
			})
		}
	}
}

// A collection's transaction is the lock that orders it against an
// upload's lease, and its context ending does not end it: a collection
// held inside its first unlink, its context cancelled there, still holds
// the lock, so a second store's upload of the content it is deleting
// cannot take its lease and is refused busy, keeping nothing. Released,
// the collection deletes the file and stops at the cancellation before the
// next orphan, which the next sweep takes, and the same upload is then
// taken, its file whole. (database/sql rolls a transaction back when its
// context ends, which would release the lock with the unlink still to
// come: the upload would lease, publish over the file and commit, and the
// unlink delete the file its row names.)
func TestACancelledCollectionHoldsItsLockUntilItsUnlinkReturns(t *testing.T) {
	dir := storeDir(t)
	s := openStore(t, dir)
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "Attached")
	// The second store waits a quarter of a second for the write lock
	// before it is refused busy: time enough for the rollback that a
	// cancellation sets off on database/sql's goroutine to release it, in
	// a collection that let it.
	old := busyTimeout
	busyTimeout = 250 * time.Millisecond
	t.Cleanup(func() { busyTimeout = old })
	second := openStore(t, dir)
	// Two orphans, first listed first (by its hash), then after.
	first, after := []byte("collected first"), []byte("collected after")
	if sumOf(first) > sumOf(after) {
		first, after = after, first
	}
	for i, content := range [][]byte{first, after} {
		a := mustPut(t, s, opID("a", i), key, "orphan.txt", content)
		rc, err := s.RemoveAttachment(context.Background(), testOp(opID("r", i)), key, a.ID)
		want(t, rc, err, StatusDone, "")
	}
	reached, release := make(chan struct{}), make(chan struct{})
	s.unlink = func(path string) error {
		if path == contentAt(s, sumOf(first)) {
			close(reached)
			<-release
		}
		return os.Remove(path)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var collected error
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		collected = s.collectOrphans(ctx, time.Now())
	}()
	// Released before the stores close, whatever the test met.
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		resume()
		<-ended
	})
	select {
	case <-reached:
	case <-ended:
		t.Fatalf("the collection ended before its unlink: %v", collected)
	}
	cancel()
	// The sweeping store's one connection is the collection's: the test
	// reads through the second store until the collection has ended.
	if _, _, err := put(second, "b1", key, "again.txt", first); !isBusy(err) {
		t.Fatalf("the second store's upload, the cancelled collection held at its unlink: %v", err)
	}
	if temps := tempFiles(t, second); len(temps) != 0 || count(t, second, "SELECT COUNT(*) FROM uploads") != 0 {
		t.Fatalf("the refused upload kept temp files %v or a lease", temps)
	}
	resume()
	<-ended
	if !errors.Is(collected, context.Canceled) {
		t.Fatalf("the cancelled collection: %v", collected)
	}
	if exists(t, contentAt(s, sumOf(first))) || !exists(t, contentAt(s, sumOf(after))) {
		t.Fatalf("after the cancelled collection: the first orphan %v, the one after it %v",
			exists(t, contentAt(s, sumOf(first))), exists(t, contentAt(s, sumOf(after))))
	}
	a := mustPut(t, second, "b2", key, "again.txt", first)
	if !bytes.Equal(readContent(t, second, a.ID), first) || !bytes.Equal(readContent(t, s, a.ID), first) {
		t.Fatal("the upload after the collection did not open whole")
	}
	s.unlink = os.Remove
	if err := s.sweep(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if exists(t, contentAt(s, sumOf(after))) || !bytes.Equal(readContent(t, s, a.ID), first) {
		t.Fatal("the next sweep left the orphan after, or took the file a row names")
	}
}

// An upload held at beforePublish whose temp file a second store's
// collection takes, older than the grace by the time its name carries,
// while its lease, younger, stays (the upload's read took most of the
// grace before the lease was taken), is refused expired when it resumes:
// publish finds no file to publish, and no row ever names content that
// was never published. Its lease goes with the refusal.
func TestAnUploadWhoseTempFileASweepTookIsRefusedExpired(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "Attached")
	content := []byte("read for most of an hour")
	h := holdAt(t, s, &s.beforePublish, "a1", key, content)
	second := openStore(t, dir)
	if err := second.collectOrphans(ctx, time.Now().Add(2*defaultSweepGrace)); err != nil {
		t.Fatal(err)
	}
	if len(tempFiles(t, s)) != 0 || count(t, s, "SELECT COUNT(*) FROM uploads") != 1 {
		t.Fatalf("after the collection: temp files %v, %d leases", tempFiles(t, s), count(t, s, "SELECT COUNT(*) FROM uploads"))
	}
	u := h.resume()
	want(t, u.rc, u.err, StatusFailed, RefusalExpired)
	noTrace(t, s, content)
	mustPut(t, s, "a2", key, "again.txt", content)
}

// An upload held at beforeCommit while a second store expires the leases
// alone, its grace at zero, is refused expired when it resumes, its file
// left in the directory and named by no row; the next collection deletes
// it.
func TestAnExpiryAloneLeavesTheFileForTheNextCollection(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "Attached")
	content := []byte("expired, not yet collected")
	h := holdAt(t, s, &s.beforeCommit, "a1", key, content)
	second := openStore(t, dir)
	second.sweepGrace = 0
	if err := second.expireLeases(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	u := h.resume()
	want(t, u.rc, u.err, StatusFailed, RefusalExpired)
	sum := sumOf(content)
	if !exists(t, contentAt(s, sum)) || count(t, s, "SELECT COUNT(*) FROM attachments WHERE sha256 = ?", sum) != 0 || count(t, s, "SELECT COUNT(*) FROM uploads") != 0 {
		t.Fatal("after the expiry alone: the file gone, or a row naming it")
	}
	if err := second.collectOrphans(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	noTrace(t, s, content)
}

// A lease at least the grace old is a crashed upload's: the sweep drops it
// and deletes its file, which no live row names; a younger lease and its
// file stay. A temp file at least the grace old goes, by the time its name
// carries, and a younger one stays; a file of no name the store gives, in
// the uploads' directory or a shard, is left.
func TestTheSweepTakesWhatIsOlderThanItsGrace(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	now := time.Now()
	old, young := now.Add(-2*time.Hour), now.Add(-time.Minute)
	plant := func(content []byte, leased time.Time) string {
		t.Helper()
		sum := sumOf(content)
		if err := os.MkdirAll(filepath.Dir(contentAt(s, sum)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(contentAt(s, sum), content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("INSERT INTO uploads (id, sha256, created_at) VALUES (?, ?, ?)", newID(), sum, stamp(leased)); err != nil {
			t.Fatal(err)
		}
		return sum
	}
	crashed, inFlight := plant([]byte("a crashed upload's"), old), plant([]byte("an upload in flight"), young)
	tmp := filepath.Join(rootOf(s), uploadsDir)
	temp := func(made time.Time) string {
		t.Helper()
		path := filepath.Join(tmp, strconv.FormatInt(made.UnixNano(), 10)+"-123")
		if err := os.WriteFile(path, []byte("part"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	oldTemp, youngTemp := temp(old), temp(young)
	strangers := []string{filepath.Join(tmp, "not-an-upload"), filepath.Join(rootOf(s), "ab", "not-a-hash")}
	for _, p := range strangers {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.sweep(ctx, now); err != nil {
		t.Fatal(err)
	}
	if exists(t, contentAt(s, crashed)) || count(t, s, "SELECT COUNT(*) FROM uploads WHERE sha256 = ?", crashed) != 0 {
		t.Fatal("the crashed upload's lease or file stayed")
	}
	if !exists(t, contentAt(s, inFlight)) || count(t, s, "SELECT COUNT(*) FROM uploads WHERE sha256 = ?", inFlight) != 1 {
		t.Fatal("the upload in flight lost its lease or its file")
	}
	if exists(t, oldTemp) || !exists(t, youngTemp) {
		t.Fatalf("the temp files: the old %v, the young %v", exists(t, oldTemp), exists(t, youngTemp))
	}
	for _, p := range strangers {
		if !exists(t, p) {
			t.Fatalf("the sweep deleted %s, no file of the store's", p)
		}
	}
}

// The sweep deletes an orphan however young, and leaves a file a live row
// names and one a lease names: nothing reads a file's age but a temp
// file's.
func TestTheSweepDeletesAnOrphanWhateverItsAge(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	named := mustPut(t, s, "a1", key, "named.txt", []byte("named by a row"))
	leased := sumOf([]byte("named by a lease"))
	orphan := sumOf([]byte("named by nothing"))
	for sum, content := range map[string]string{leased: "named by a lease", orphan: "named by nothing"} {
		if err := os.MkdirAll(filepath.Dir(contentAt(s, sum)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(contentAt(s, sum), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("INSERT INTO uploads (id, sha256, created_at) VALUES (?, ?, ?)", newID(), leased, stamp(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := s.sweep(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if exists(t, contentAt(s, orphan)) || !exists(t, contentAt(s, leased)) || !exists(t, contentAt(s, named.SHA256)) {
		t.Fatalf("after the sweep: the orphan %v, the leased %v, the named %v",
			exists(t, contentAt(s, orphan)), exists(t, contentAt(s, leased)), exists(t, contentAt(s, named.SHA256)))
	}
}

// Open sweeps before it returns, so before any upload: an orphan and a
// crashed upload's lease left by an earlier server are gone. A file the
// sweep cannot delete is left, SweepError says why, and the store serves
// regardless; a store opened read-only sweeps nothing.
func TestOpenSweepsBeforeAnyUpload(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "Attached")
	a := mustPut(t, s, "a1", key, "gone.txt", []byte("removed before the restart"))
	rc, err := s.RemoveAttachment(ctx, testOp("r1"), key, a.ID)
	want(t, rc, err, StatusDone, "")
	crashed := sumOf([]byte("a crashed upload's"))
	if _, err := s.db.Exec("INSERT INTO uploads (id, sha256, created_at) VALUES (?, ?, ?)", newID(), crashed, stamp(time.Now().Add(-2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("the disk refused")
	again, err := openWith(dir, func(s *Store) {
		s.unlink = func(string) error { return failure }
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Close() })
	if err := again.SweepError(); !errors.Is(err, failure) || !exists(t, contentAt(again, a.SHA256)) {
		t.Fatalf("a sweep that cannot delete: %v", err)
	}
	if count(t, again, "SELECT COUNT(*) FROM uploads") != 0 {
		t.Fatal("the crashed upload's lease outlived the sweep at open")
	}
	mustPut(t, again, "a2", key, "after.txt", []byte("the store serves regardless"))
	again.Close()
	last := openStore(t, dir)
	if err := last.SweepError(); err != nil || exists(t, contentAt(last, a.SHA256)) {
		t.Fatalf("the next open's sweep: %v", err)
	}
	if _, err := last.db.Exec("UPDATE schema_version SET version = ?", Version+1); err != nil {
		t.Fatal(err)
	}
	orphan := sumOf([]byte("left by a newer server"))
	if err := os.MkdirAll(filepath.Dir(contentAt(last, orphan)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contentAt(last, orphan), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	last.Close()
	ro := openStore(t, dir)
	if readOnly, _ := ro.ReadOnly(); !readOnly || ro.SweepError() != nil || !exists(t, contentAt(ro, orphan)) {
		t.Fatalf("a read-only store swept: read-only %v, %v", readOnly, ro.SweepError())
	}
}

// An attachment removed between OpenAttachment's read of its row and its
// open of the file, the file then deleted by a second store's sweep at its
// open (the remove left it to the sweep, and no live row or lease names
// it), is not found, as it is when the remove comes first: the row, read
// again once the file is found missing, is removed. A live attachment
// whose file is deleted there by hand is still the store's error, never
// taken for a removal.
func TestAnAttachmentRemovedAndSweptWhileItOpensIsNotFound(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "Attached")
	removed := mustPut(t, s, "a1", key, "removed.txt", []byte("removed while it opens"))
	s.beforeOpen = func() {
		s.beforeOpen = nil
		rc, err := s.RemoveAttachment(ctx, testOp("r1"), key, removed.ID)
		want(t, rc, err, StatusDone, "")
		second := openStore(t, dir)
		if err := second.SweepError(); err != nil {
			t.Fatalf("the second store's sweep at open: %v", err)
		}
		if exists(t, contentAt(s, removed.SHA256)) {
			t.Fatal("the second store's sweep left the removed attachment's file")
		}
	}
	if _, f, err := s.OpenAttachment(ctx, removed.ID); !errors.Is(err, ErrNotFound) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("an attachment removed and swept while it opened: %v, want not found", err)
	}
	live := mustPut(t, s, "a2", key, "live.txt", []byte("lost while it opens"))
	s.beforeOpen = func() {
		s.beforeOpen = nil
		if err := os.Remove(contentAt(s, live.SHA256)); err != nil {
			t.Fatal(err)
		}
	}
	if _, f, err := s.OpenAttachment(ctx, live.ID); err == nil || errors.Is(err, ErrNotFound) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("a live attachment whose file went while it opened: %v, want the store's error", err)
	}
}

// A launch's package pins its attachments' content (Program item DS2): an
// attachment removed after a launch named it survives the sweep and opens
// through OpenLaunchFile, a second store's sweep at its open included;
// one removed and never launched is collected. OpenLaunchFile opens only
// a hash its launch's package names: another launch's, a live
// attachment's the package does not name, and a hash of no shape are not
// found.
func TestSweepKeepsALaunchsFiles(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	newProject(t, s, "CND")
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Attached", Acceptance: "- it is"})
	launched := mustPut(t, s, "a1", key, "launched.txt", []byte("handed over"))
	dropped := mustPut(t, s, "a2", key, "dropped.txt", []byte("never handed over"))
	live := mustPut(t, s, "a3", key, "live.txt", []byte("live but not handed over"))
	body := testPlanBody("crew")
	body.Attachments = []string{launched.ID}
	plan := approvedPlan(t, s, key, PlanRequest{Crew: "crew", Attachments: body.Attachments}, body, "")
	l := mustLaunch(t, s, "l1", key, plan, "")
	_, other := makeTicket(t, s, NewTicket{Project: "CND", Title: "Other"})
	otherLaunch := mustLaunch(t, s, "l2", other, simplePlan(t, s, other), "")
	for i, a := range []Attachment{launched, dropped} {
		rc, err := s.RemoveAttachment(ctx, testOp(opID("rm", i)), key, a.ID)
		want(t, rc, err, StatusDone, "")
	}
	if err := s.sweep(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !exists(t, contentAt(s, launched.SHA256)) || exists(t, contentAt(s, dropped.SHA256)) || !exists(t, contentAt(s, live.SHA256)) {
		t.Fatalf("after the sweep: the launched %v, the dropped %v, the live %v",
			exists(t, contentAt(s, launched.SHA256)), exists(t, contentAt(s, dropped.SHA256)), exists(t, contentAt(s, live.SHA256)))
	}
	// A second store's sweep, at its open, keeps it too.
	second := openStore(t, dir)
	if err := second.SweepError(); err != nil || !exists(t, contentAt(s, launched.SHA256)) {
		t.Fatalf("a second store's sweep: %v, the file there %v", err, exists(t, contentAt(s, launched.SHA256)))
	}
	f, err := s.OpenLaunchFile(ctx, l.ID, launched.SHA256)
	if err != nil {
		t.Fatalf("the launch's file: %v", err)
	}
	got, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(got) != "handed over" {
		t.Fatalf("the launch's file holds %q %v", got, err)
	}
	for _, c := range []struct{ launch, sum string }{
		{otherLaunch.ID, launched.SHA256}, {l.ID, live.SHA256}, {l.ID, dropped.SHA256}, {l.ID, "not a hash"}, {"not an id", launched.SHA256},
	} {
		if f, err := s.OpenLaunchFile(ctx, c.launch, c.sum); !errors.Is(err, ErrNotFound) {
			if f != nil {
				f.Close()
			}
			t.Errorf("launch %s, content %s: %v, want not found", c.launch, c.sum, err)
		}
	}
	// A pinned content missing is the store's error, never not found.
	if err := os.Remove(contentAt(s, launched.SHA256)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenLaunchFile(ctx, l.ID, launched.SHA256); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a pinned file missing: %v", err)
	}
}
