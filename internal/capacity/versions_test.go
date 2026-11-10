package capacity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// onDisk reads capacity/versions.json as it is now; none is no record.
func onDisk(t *testing.T, dir string) versionsDoc {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, Dir, VersionsFile))
	if errors.Is(err, os.ErrNotExist) {
		return versionsDoc{}
	}
	if err != nil {
		t.Fatal(err)
	}
	var d versionsDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func stateOnDisk(t *testing.T, dir, key string) string {
	t.Helper()
	d := onDisk(t, dir)
	for _, l := range []struct {
		list  []string
		state string
	}{{d.Checking, VersionChecking}, {d.Checked, VersionChecked}, {d.Turned, VersionTurned}, {d.Interrupted, VersionInterrupted}} {
		if slices.Contains(l.list, key) {
			return l.state
		}
	}
	return ""
}

func TestVersionsAdmitOnce(t *testing.T) {
	dir := t.TempDir()
	v := openAt(t, dir, Options{}).Versions()
	// Two accounts ask a check of one version at once.
	start := make(chan struct{})
	var wg sync.WaitGroup
	adm := make([]*Admission, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			<-start
			adm[i], errs[i] = v.Admit("claude", "2.1.320", false, true)
		})
	}
	close(start)
	wg.Wait()
	var held *Admission
	refused := 0
	for i := range 2 {
		switch {
		case errs[i] == nil:
			held = adm[i]
		case code(errs[i]) == CodeCheckUnderWay:
			refused++
		default:
			t.Fatalf("ask %d: %v", i, errs[i])
		}
	}
	if held == nil || refused != 1 {
		t.Fatalf("admitted %v, refused %d", held, refused)
	}
	// The admitted run is held: its record is checking on disk already,
	// before the program would start, and every ask meanwhile is refused.
	if got := stateOnDisk(t, dir, "claude@2.1.320"); got != VersionChecking {
		t.Fatalf("on disk while the check runs: %q", got)
	}
	if _, err := v.Admit("claude", "2.1.320", false, false); code(err) != CodeCheckUnderWay {
		t.Errorf("a read during the check: %v", err)
	}
	if err := held.Done(Local); err != nil {
		t.Fatal(err)
	}
	if err := held.Done(Turned); err != nil { // only the first call counts
		t.Fatal(err)
	}
	if got := stateOnDisk(t, dir, "claude@2.1.320"); got != VersionChecked {
		t.Fatalf("after Done(Local): %q", got)
	}
	// Admitted for every read after, sharing no record.
	a, err := v.Admit("claude", "2.1.320", false, false)
	if err != nil {
		t.Fatalf("a read of a checked version: %v", err)
	}
	b, err := v.Admit("claude", "2.1.320", false, false)
	if err != nil {
		t.Fatalf("a second read at once: %v", err)
	}
	_ = a.Done(Local)
	_ = b.Done(Inconclusive)
	if got := v.State("claude", "2.1.320"); got != VersionChecked {
		t.Errorf("after shared reads: %q", got)
	}
}

func TestVersionsInconclusiveCheck(t *testing.T) {
	dir := t.TempDir()
	v := openAt(t, dir, Options{}).Versions()
	a, err := v.Admit("claude", "2.1.321", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Done(Inconclusive); err != nil {
		t.Fatal(err)
	}
	if got := stateOnDisk(t, dir, "claude@2.1.321"); got != VersionInterrupted {
		t.Fatalf("on disk after Done(Inconclusive): %q", got)
	}
	_, err = v.Admit("claude", "2.1.321", false, false)
	if code(err) != CodeUnqualified || !strings.Contains(err.Error(), "a check of 2.1.321 was interrupted: check it again") {
		t.Fatalf("the next read without a check: %v", err)
	}
	// Opened again: still interrupted, still refused to a read.
	v = openAt(t, dir, Options{}).Versions()
	if got := v.State("claude", "2.1.321"); got != VersionInterrupted {
		t.Fatalf("after open: %q", got)
	}
	if _, err := v.Admit("claude", "2.1.321", false, false); code(err) != CodeUnqualified {
		t.Fatalf("a read after open: %v", err)
	}
	// The admin's check admits it once.
	a, err = v.Admit("claude", "2.1.321", false, true)
	if err != nil {
		t.Fatalf("the admin's check: %v", err)
	}
	if _, err := v.Admit("claude", "2.1.321", false, true); code(err) != CodeCheckUnderWay {
		t.Fatalf("a second check: %v", err)
	}
	_ = a.Done(Local)

	// An Inconclusive read of a checked version leaves it checked.
	a, _ = v.Admit("claude", "2.1.321", false, false)
	_ = a.Done(Inconclusive)
	if got := stateOnDisk(t, dir, "claude@2.1.321"); got != VersionChecked {
		t.Errorf("an inconclusive read of a checked version: %q", got)
	}
	// A Turned read of the qualified version records it turned, though it
	// had no record.
	a, err = v.Admit("claude", "2.1.318", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.State("claude", "2.1.318"); got != "" {
		t.Fatalf("a shared read made a record: %q", got)
	}
	_ = a.Done(Turned)
	if got := stateOnDisk(t, dir, "claude@2.1.318"); got != VersionTurned {
		t.Errorf("the qualified version after a turn: %q", got)
	}
	if _, err := v.Admit("claude", "2.1.318", true, false); code(err) != CodeUnqualified {
		t.Errorf("the qualified version read after its turn: %v", err)
	}
}

func TestVersionsInterrupted(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	// A check no process finished: its record is checking on disk.
	doc := `{"schema":1,"checking":["claude@2.1.322"],"checked":["claude@2.1.320"],"turned":[],"interrupted":[]}`
	path := filepath.Join(dir, Dir, VersionsFile)
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	// Opening writes nothing: a server without the owner lock may open the
	// folder while the owner's check is still running.
	s := openNotOwned(t, dir, Options{})
	if b, _ := os.ReadFile(path); string(b) != doc {
		t.Fatalf("Open wrote the record:\n%s", b)
	}
	if got := s.Versions().State("claude", "2.1.322"); got != VersionChecking {
		t.Fatalf("the record as read: %q", got)
	}
	if _, err := s.Versions().Admit("claude", "2.1.322", false, true); code(err) != CodeNotOwner {
		t.Fatalf("a check before Own: %v", err)
	}
	// The owner's Own concludes it: interrupted, on disk.
	if err := s.Own(); err != nil {
		t.Fatal(err)
	}
	v := s.Versions()
	if got := stateOnDisk(t, dir, "claude@2.1.322"); got != VersionInterrupted {
		t.Fatalf("on disk after Own: %q", got)
	}
	if got := v.State("claude", "2.1.320"); got != VersionChecked {
		t.Errorf("the checked record: %q", got)
	}
	if _, err := v.Admit("claude", "2.1.322", false, false); code(err) != CodeUnqualified {
		t.Fatalf("a read of an interrupted check: %v", err)
	}
	if _, err := v.Admit("claude", "2.1.322", false, true); err != nil {
		t.Fatalf("the admin's check: %v", err)
	}
	if got := stateOnDisk(t, dir, "claude@2.1.322"); got != VersionChecking {
		t.Errorf("on disk during the check: %q", got)
	}
}

func TestVersionsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	v := openAt(t, dir, Options{}).Versions()
	for ver, o := range map[string]Outcome{"2.1.330": Local, "2.1.331": Turned} {
		a, err := v.Admit("claude", ver, false, true)
		if err != nil {
			t.Fatal(err)
		}
		_ = a.Done(o)
	}
	v = openAt(t, dir, Options{}).Versions()
	if got := v.State("claude", "2.1.330"); got != VersionChecked {
		t.Errorf("checked after restart: %q", got)
	}
	if got := v.State("claude", "2.1.331"); got != VersionTurned {
		t.Errorf("turned after restart: %q", got)
	}
	if a, err := v.Admit("claude", "2.1.330", false, false); err != nil {
		t.Errorf("a checked version read after restart: %v", err)
	} else {
		_ = a.Done(Local)
	}
	for _, check := range []bool{false, true} {
		if _, err := v.Admit("claude", "2.1.331", true, check); code(err) != CodeUnqualified || !strings.Contains(err.Error(), "model turn") {
			t.Errorf("a turned version, check %v: %v", check, err)
		}
	}
	// Records are per adapter.
	if _, err := v.Admit("codex", "2.1.331", true, false); err != nil {
		t.Errorf("another adapter's version: %v", err)
	}
}

func TestVersionsTurnedNeverDropped(t *testing.T) {
	dir := t.TempDir()
	v := openAt(t, dir, Options{}).Versions()
	// Versions of the most bytes, so the document's bound is held at the
	// most records too.
	ver := func(kind string, i int) string {
		s := fmt.Sprintf("%s.%d.", kind, i)
		return s + strings.Repeat("9", MaxVersion-len(s))
	}
	check := func(version string, o Outcome) error {
		a, err := v.Admit("claude", version, false, true)
		if err != nil {
			return err
		}
		return a.Done(o)
	}
	for i := range 3 {
		if err := check(ver("t", i), Turned); err != nil {
			t.Fatal(err)
		}
	}
	for i := range MaxChecked + 1 {
		if err := check(ver("c", i), Local); err != nil {
			t.Fatal(err)
		}
	}
	d := onDisk(t, dir)
	if len(d.Checked) != MaxChecked || slices.Contains(d.Checked, "claude@"+ver("c", 0)) || d.Checked[0] != "claude@"+ver("c", 1) {
		t.Fatalf("checked kept: %d, first %q", len(d.Checked), d.Checked[0])
	}
	if len(d.Turned) != 3 {
		t.Fatalf("turned dropped: %v", d.Turned)
	}
	// Fill the held records: turned and interrupted, never dropped.
	for i := 3; i < MaxHeld-1; i++ {
		o := Turned
		if i%2 == 0 {
			o = Inconclusive
		}
		if err := check(ver("h", i), o); err != nil {
			t.Fatalf("held %d: %v", i, err)
		}
	}
	// One left: a shared read may still run; while it runs, it holds the
	// last place, so a check of a new version is refused.
	shared, err := v.Admit("claude", ver("c", 5), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := check(ver("n", 0), Local); code(err) != CodeVersionsFull {
		t.Fatalf("a check while the last place is held: %v", err)
	}
	_ = shared.Done(Turned)
	d = onDisk(t, dir)
	if held := len(d.Turned) + len(d.Interrupted) + len(d.Checking); held != MaxHeld {
		t.Fatalf("%d held records, want %d", held, MaxHeld)
	}
	for _, q := range []bool{false, true} {
		if _, err := v.Admit("claude", ver("n", 1), q, true); code(err) != CodeVersionsFull {
			t.Errorf("a new version, qualified %v, with the held records full: %v", q, err)
		}
	}
	// An interrupted version is recorded already: its check takes no new
	// place.
	if err := check(ver("h", 4), Local); err != nil {
		t.Errorf("checking an interrupted version again: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, Dir, VersionsFile))
	if err != nil || fi.Size() > MaxVersionsDoc || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the document: %v, %v", fi, err)
	}
	openAt(t, dir, Options{})
}

// TestVersionsTwoOpens: two servers over one data directory. Only the one
// that owns the folder writes the record; the other's open interrupts no
// check under way and its asks start nothing; and once it owns the folder
// (the first ended), it reads the turned record the first saved.
func TestVersionsTwoOpens(t *testing.T) {
	dir := t.TempDir()
	a := openAt(t, dir, Options{}).Versions()
	adm, err := a.Admit("claude", "2.1.400", false, true)
	if err != nil {
		t.Fatal(err)
	}
	b := openNotOwned(t, dir, Options{})
	if got := stateOnDisk(t, dir, "claude@2.1.400"); got != VersionChecking {
		t.Fatalf("after the second Open, on disk: %q", got)
	}
	if err := adm.Done(Turned); err != nil {
		t.Fatal(err)
	}
	for _, ver := range []string{"2.1.400", "2.1.401"} {
		if _, err := b.Versions().Admit("claude", ver, true, true); code(err) != CodeNotOwner {
			t.Fatalf("the second server's check of %s: %v", ver, err)
		}
	}
	if got := stateOnDisk(t, dir, "claude@2.1.400"); got != VersionTurned {
		t.Fatalf("after the second server's asks, on disk: %q", got)
	}
	// A reload shows the record as it is on disk, writing nothing.
	if err := b.Versions().Reload(); err != nil {
		t.Fatal(err)
	}
	if got := b.Versions().State("claude", "2.1.400"); got != VersionTurned {
		t.Fatalf("the second server's reload: %q", got)
	}
	// The first ends; the second takes the lock and owns the folder.
	if err := b.Own(); err != nil {
		t.Fatal(err)
	}
	for _, check := range []bool{false, true} {
		if _, err := b.Versions().Admit("claude", "2.1.400", true, check); code(err) != CodeUnqualified || !strings.Contains(err.Error(), "model turn") {
			t.Errorf("a turned version on the new owner, check %v: %v", check, err)
		}
	}
	c, err := b.Versions().Admit("claude", "2.1.401", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Done(Local); err != nil {
		t.Fatal(err)
	}
	if got := stateOnDisk(t, dir, "claude@2.1.400"); got != VersionTurned {
		t.Errorf("after the new owner's own check, on disk 2.1.400: %q", got)
	}
}

// TestVersionsTurnedTerminal: a turned record is never changed back. A
// checked version dropped from the record (the oldest past MaxChecked)
// while a shared read of it runs can be checked again; the shared read
// ends Turned, then the check ends Local: the version stays turned.
func TestVersionsTurnedTerminal(t *testing.T) {
	dir := t.TempDir()
	v := openAt(t, dir, Options{}).Versions()
	check := func(ver string, o Outcome) {
		t.Helper()
		a, err := v.Admit("claude", ver, false, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Done(o); err != nil {
			t.Fatal(err)
		}
	}
	for i := range MaxChecked {
		check(fmt.Sprintf("1.%d", i), Local)
	}
	shared, err := v.Admit("claude", "1.0", false, false)
	if err != nil {
		t.Fatal(err)
	}
	check(fmt.Sprintf("1.%d", MaxChecked), Local) // drops 1.0, the oldest
	if got := v.State("claude", "1.0"); got != "" {
		t.Fatalf("1.0 not dropped: %q", got)
	}
	exclusive, err := v.Admit("claude", "1.0", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := shared.Done(Turned); err != nil {
		t.Fatal(err)
	}
	if err := exclusive.Done(Local); err != nil {
		t.Fatal(err)
	}
	if got := stateOnDisk(t, dir, "claude@1.0"); got != VersionTurned {
		t.Fatalf("a turned record overwritten: %q", got)
	}
	// The other order and the other outcomes: turned stays turned.
	for _, o := range []Outcome{Local, Inconclusive} {
		ver := fmt.Sprintf("2.%d", o)
		a, err := v.Admit("claude", ver, true, false)
		if err != nil {
			t.Fatal(err)
		}
		b, err := v.Admit("claude", ver, true, false)
		if err != nil {
			t.Fatal(err)
		}
		_ = a.Done(Turned)
		_ = b.Done(o)
		if got := stateOnDisk(t, dir, "claude@"+ver); got != VersionTurned {
			t.Errorf("turned, then %v: %q", o, got)
		}
	}
	if _, err := v.Admit("claude", "1.0", false, true); code(err) != CodeUnqualified {
		t.Errorf("a check of the turned version: %v", err)
	}
}

// TestVersionsDocumentShape: a record that is not the document's shape or
// passes its counts cannot be read, at Open and at Own, and is left as it
// is: never opened as an empty record the next save replaces, never cut
// down to its bound.
func TestVersionsDocumentShape(t *testing.T) {
	list := func(prefix string, n int) string {
		var l []string
		for i := range n {
			l = append(l, fmt.Sprintf("%q", fmt.Sprintf("claude@%s.%d", prefix, i)))
		}
		return "[" + strings.Join(l, ",") + "]"
	}
	doc := func(checking, checked, turned, interrupted string) string {
		return fmt.Sprintf(`{"schema":1,"checking":%s,"checked":%s,"turned":%s,"interrupted":%s}`, checking, checked, turned, interrupted)
	}
	for name, body := range map[string]string{
		"null":             `null`,
		"empty object":     `{}`,
		"array":            `[]`,
		"number":           `7`,
		"schema only":      `{"schema":1}`,
		"a list null":      `{"schema":1,"checking":[],"checked":[],"turned":null,"interrupted":[]}`,
		"schema 0":         `{"schema":0,"checking":[],"checked":[],"turned":[],"interrupted":[]}`,
		"too many checked": doc("[]", list("c", MaxChecked+1), "[]", "[]"),
		"too many turned":  doc("[]", "[]", list("t", MaxHeld+1), "[]"),
		"too many held":    doc(list("k", 1), "[]", list("t", MaxHeld/2), list("i", MaxHeld/2)),
	} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, Dir), 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, Dir, VersionsFile)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		data, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Open(data, Options{}); err == nil || !strings.Contains(err.Error(), "cannot be read") {
			t.Errorf("%s: opened: %v", name, err)
		}
		if b, _ := os.ReadFile(path); string(b) != body {
			t.Errorf("%s: the file changed", name)
		}
	}
	// At the bounds exactly, the record is read.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	body := doc(list("k", 1), list("c", MaxChecked), list("t", MaxHeld/2), list("i", MaxHeld/2-1))
	if err := os.WriteFile(filepath.Join(dir, Dir, VersionsFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openAt(t, dir, Options{})
	if got := s.Versions().State("claude", "k.0"); got != VersionInterrupted {
		t.Errorf("a record at its bounds, after Own: %q", got)
	}
	// A file damaged after Open: Own refuses, writing nothing.
	dir = t.TempDir()
	s = openNotOwned(t, dir, Options{})
	path := filepath.Join(dir, Dir, VersionsFile)
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Own(); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("Own over a damaged record: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{}` {
		t.Errorf("Own wrote over a damaged record: %s", b)
	}
	if _, err := s.Versions().Admit("claude", "2.1.500", false, true); code(err) != CodeNotOwner {
		t.Errorf("a check after a failed Own: %v", err)
	}
}
