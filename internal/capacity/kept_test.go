package capacity

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// errSaveFault is the failure the save seam hands a save.
var errSaveFault = errors.New("the save seam failed this save")

// saveFault is the save seam: it fails the next saves of a document it is
// told to, and passes every other.
type saveFault struct {
	mu   sync.Mutex
	fail map[string]int // document -> saves still to fail
}

func (f *saveFault) check(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail[name] > 0 {
		f.fail[name]--
		return errSaveFault
	}
	return nil
}

// failNext fails the next n saves of the document name.
func (f *saveFault) failNext(name string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[name] += n
}

// faultSaves puts the save seam on both documents of s.
func faultSaves(s *Store) *saveFault {
	f := &saveFault{fail: map[string]int{}}
	s.mu.Lock()
	s.seam = f.check
	s.mu.Unlock()
	s.ver.mu.Lock()
	s.ver.seam = f.check
	s.ver.mu.Unlock()
	return f
}

// restarted is capacity/ as a server started now over a copy of dir would
// hold it, owner lock and all: the documents as they are on disk, opened and
// owned in a folder of their own, so the copy's Own writes nothing in dir.
func restarted(t *testing.T, dir string) *Store {
	t.Helper()
	cp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cp, Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{AccountsFile, VersionsFile} {
		b, err := os.ReadFile(filepath.Join(dir, Dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cp, Dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return openAt(t, cp, Options{})
}

// admits is Admit's answer for a record's state, as Admit decides it.
func admits(state string, qualified, check bool) bool {
	switch {
	case state == VersionTurned, state == VersionChecking:
		return false
	case qualified || state == VersionChecked:
		return true
	}
	return check
}

// noLooser holds what a restart would find to what memory holds: for each
// key, a server started now over dir admits no read and no check that v
// refuses. A version checking in memory is judged by a read of it alone: a
// restart concludes that check interrupted and the admin may check again,
// and a check is only ever asked of a version that is not qualified.
func noLooser(t *testing.T, dir string, v *Versions, keys ...string) {
	t.Helper()
	fresh := restarted(t, dir).Versions()
	for _, k := range keys {
		ad, ver, _ := strings.Cut(k, "@")
		mem, disk := v.State(ad, ver), fresh.State(ad, ver)
		for _, q := range []bool{false, true} {
			for _, c := range []bool{false, true} {
				if mem == VersionChecking && (c || q) {
					continue
				}
				if !admits(mem, q, c) && admits(disk, q, c) {
					t.Errorf("%s: memory holds %q and refuses (qualified %v, check %v); a restart finds %q and admits it", k, mem, q, c, disk)
				}
			}
		}
	}
}

// TestVersionsUnsavedOutcomeRetried: an outcome whose save failed is not
// final. A second outcome for the version, from the same admission or
// another, saves it before it answers success, so a restart refuses what
// memory refuses (review round 2, finding 1).
func TestVersionsUnsavedOutcomeRetried(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, Options{})
	f := faultSaves(s)
	v := s.Versions()
	a, err := v.Admit("claude", "2.1.600", true, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := v.Admit("claude", "2.1.600", true, false)
	if err != nil {
		t.Fatal(err)
	}
	f.failNext(VersionsFile, 2)
	err = a.Done(Turned)
	if code(err) != CodeNotKept || !strings.Contains(err.Error(), "not yet kept") {
		t.Fatalf("a Turned whose save failed: %v", err)
	}
	if got := v.State("claude", "2.1.600"); got != VersionTurned {
		t.Fatalf("in memory after the failed save: %q", got)
	}
	// The same admission's second call reports no success while the
	// outcome is held unsaved.
	if err := a.Done(Turned); code(err) != CodeNotKept {
		t.Fatalf("a second Done while the save still fails: %v", err)
	}
	// Another admission's outcome saves it before it answers.
	if err := b.Done(Turned); err != nil {
		t.Fatalf("the second admission's Turned: %v", err)
	}
	if got := stateOnDisk(t, dir, "claude@2.1.600"); got != VersionTurned {
		t.Fatalf("on disk after the second Turned answered success: %q", got)
	}
	if _, err := restarted(t, dir).Versions().Admit("claude", "2.1.600", true, false); code(err) != CodeUnqualified {
		t.Fatalf("a restart's read of the turned version: %v", err)
	}
	noLooser(t, dir, v, "claude@2.1.600")
}

// TestVersionsReloadAfterOwn: the owner's record is the one in memory. A
// Reload after Own is refused in words and changes nothing, so an outcome
// held unsaved is never replaced by the file's older state, and the
// explicit check it allows is admitted once the outcome is kept (review
// round 2, finding 2).
func TestVersionsReloadAfterOwn(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, Options{})
	f := faultSaves(s)
	v := s.Versions()
	a, err := v.Admit("claude", "2.1.610", false, true)
	if err != nil {
		t.Fatal(err)
	}
	f.failNext(VersionsFile, 1)
	if err := a.Done(Inconclusive); code(err) != CodeNotKept {
		t.Fatalf("an Inconclusive whose save failed: %v", err)
	}
	if got := stateOnDisk(t, dir, "claude@2.1.610"); got != VersionChecking {
		t.Fatalf("on disk after the failed save: %q", got)
	}
	err = v.Reload()
	if err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("a Reload after Own: %v", err)
	}
	if got := v.State("claude", "2.1.610"); got != VersionInterrupted {
		t.Fatalf("in memory after the Reload: %q", got)
	}
	// The explicit check is admitted: the held outcome is saved first.
	c, err := v.Admit("claude", "2.1.610", false, true)
	if err != nil {
		t.Fatalf("the admin's check after the reload: %v", err)
	}
	if err := c.Done(Local); err != nil {
		t.Fatal(err)
	}
	if got := stateOnDisk(t, dir, "claude@2.1.610"); got != VersionChecked {
		t.Fatalf("on disk after the check: %q", got)
	}
}

// TestSaveFailsAtEveryCall walks every exported call that writes a
// document or reports an outcome with a save that fails at its point (the
// save seam, not a broken disk). Each answers an error, never success;
// while the record holds what it could not save, no run is admitted; the
// next call that touches the record saves it; and after every call that
// answered, a restart admits nothing memory refuses.
func TestSaveFailsAtEveryCall(t *testing.T) {
	owned := func(t *testing.T) (string, *Store, *saveFault) {
		dir := t.TempDir()
		s := openAt(t, dir, Options{})
		return dir, s, faultSaves(s)
	}
	// heldRefuses: with the record's retry failing too, an unrelated read is
	// refused versions_not_kept; once the save passes, it is admitted.
	heldRefuses := func(t *testing.T, f *saveFault, v *Versions) {
		t.Helper()
		f.failNext(VersionsFile, 1)
		if _, err := v.Admit("claude", "9.9.9", true, false); code(err) != CodeNotKept {
			t.Fatalf("a read while an outcome is held unsaved: %v", err)
		}
		a, err := v.Admit("claude", "9.9.9", true, false)
		if err != nil {
			t.Fatalf("a read once the save passes: %v", err)
		}
		if err := a.Done(Local); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a check's admission", func(t *testing.T) {
		dir, s, f := owned(t)
		v := s.Versions()
		f.failNext(VersionsFile, 1)
		_, err := v.Admit("claude", "3.0.0", false, true)
		if err == nil || !strings.Contains(err.Error(), "not started") {
			t.Fatalf("a check whose record could not be saved: %v", err)
		}
		if got := v.State("claude", "3.0.0"); got != "" {
			t.Fatalf("in memory after the failed admission: %q", got)
		}
		noLooser(t, dir, v, "claude@3.0.0")
		a, err := v.Admit("claude", "3.0.0", false, true)
		if err != nil {
			t.Fatalf("the check asked again: %v", err)
		}
		if got := stateOnDisk(t, dir, "claude@3.0.0"); got != VersionChecking {
			t.Fatalf("on disk once admitted: %q", got)
		}
		noLooser(t, dir, v, "claude@3.0.0")
		if err := a.Done(Local); err != nil {
			t.Fatal(err)
		}
		noLooser(t, dir, v, "claude@3.0.0")
	})

	for o, want := range map[Outcome]string{Local: VersionChecked, Turned: VersionTurned, Inconclusive: VersionInterrupted} {
		t.Run("a check's outcome "+want, func(t *testing.T) {
			dir, s, f := owned(t)
			v := s.Versions()
			a, err := v.Admit("claude", "3.1.0", false, true)
			if err != nil {
				t.Fatal(err)
			}
			f.failNext(VersionsFile, 1)
			if err := a.Done(o); code(err) != CodeNotKept {
				t.Fatalf("an outcome whose save failed: %v", err)
			}
			if got := v.State("claude", "3.1.0"); got != want {
				t.Fatalf("in memory: %q", got)
			}
			heldRefuses(t, f, v)
			if got := stateOnDisk(t, dir, "claude@3.1.0"); got != want {
				t.Fatalf("on disk once the save passed: %q", got)
			}
			noLooser(t, dir, v, "claude@3.1.0", "claude@9.9.9")
		})
	}

	t.Run("a shared read's Turned", func(t *testing.T) {
		dir, s, f := owned(t)
		v := s.Versions()
		a, err := v.Admit("claude", "3.2.0", true, false)
		if err != nil {
			t.Fatal(err)
		}
		f.failNext(VersionsFile, 1)
		if err := a.Done(Turned); code(err) != CodeNotKept {
			t.Fatalf("a Turned whose save failed: %v", err)
		}
		heldRefuses(t, f, v)
		if got := stateOnDisk(t, dir, "claude@3.2.0"); got != VersionTurned {
			t.Fatalf("on disk once the save passed: %q", got)
		}
		noLooser(t, dir, v, "claude@3.2.0", "claude@9.9.9")
		// A shared read's Local or Inconclusive saves nothing and answers
		// success: memory holds nothing the file does not.
		for _, o := range []Outcome{Local, Inconclusive} {
			b, err := v.Admit("claude", "3.2.1", true, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Done(o); err != nil {
				t.Fatalf("a shared read's %v: %v", o, err)
			}
		}
	})

	t.Run("Own", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, Dir), 0o700); err != nil {
			t.Fatal(err)
		}
		doc := `{"schema":1,"checking":["claude@3.3.0"],"checked":[],"turned":[],"interrupted":[]}`
		if err := os.WriteFile(filepath.Join(dir, Dir, VersionsFile), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		s := openNotOwned(t, dir, Options{})
		f := faultSaves(s)
		f.failNext(VersionsFile, 1)
		if err := s.Own(); err == nil {
			t.Fatal("Own answered success with its save failed")
		}
		if _, err := s.Versions().Admit("claude", "3.3.0", false, true); code(err) != CodeNotOwner {
			t.Fatalf("a check after a failed Own: %v", err)
		}
		if got := stateOnDisk(t, dir, "claude@3.3.0"); got != VersionChecking {
			t.Fatalf("on disk after a failed Own: %q", got)
		}
		if err := s.Own(); err != nil {
			t.Fatal(err)
		}
		if got := stateOnDisk(t, dir, "claude@3.3.0"); got != VersionInterrupted {
			t.Fatalf("on disk after Own: %q", got)
		}
		noLooser(t, dir, s.Versions(), "claude@3.3.0")
	})

	t.Run("the accounts' writes", func(t *testing.T) {
		dir, s, f := owned(t)
		facts := newFacts()
		folder := t.TempDir()
		acc, rev, err := s.Add(facts, "claude", "work", folder, 0)
		if err != nil {
			t.Fatal(err)
		}
		writes := []struct {
			name string
			do   func(rev int64) (int64, error)
		}{
			{"Add", func(r int64) (int64, error) { _, r, err := s.Add(facts, "codex", "home", folder, r); return r, err }},
			{"Rename", func(r int64) (int64, error) { _, r, err := s.Rename(acc.ID, "office", r); return r, err }},
			{"SetDefault", func(r int64) (int64, error) { _, r, err := s.SetDefault("claude", acc.ID, r); return r, err }},
			{"Retire", func(r int64) (int64, error) { _, r, err := s.Retire(acc.ID, r); return r, err }},
			{"Remove", func(r int64) (int64, error) { return s.Remove(facts, acc.ID, r) }},
		}
		for _, w := range writes {
			before, err := os.ReadFile(filepath.Join(dir, Dir, AccountsFile))
			if err != nil {
				t.Fatal(err)
			}
			f.failNext(AccountsFile, 1)
			if _, err := w.do(rev); !errors.Is(err, errSaveFault) {
				t.Fatalf("%s with its save failed: %v", w.name, err)
			}
			// Memory holds nothing of it: the file, the Store and a restart
			// all hold the document as it was, at the same rev.
			if after, _ := os.ReadFile(filepath.Join(dir, Dir, AccountsFile)); string(after) != string(before) {
				t.Fatalf("%s wrote the file with its save failed", w.name)
			}
			for _, st := range []*Store{s, restarted(t, dir)} {
				d, err := st.Accounts()
				if err != nil || d.Rev != rev {
					t.Fatalf("%s: the document after the failed save: rev %d, %v", w.name, d.Rev, err)
				}
			}
			if rev, err = w.do(rev); err != nil {
				t.Fatalf("%s again at the same rev: %v", w.name, err)
			}
		}
	})
}
