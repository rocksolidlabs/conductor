package crew

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// crewJSON is a crew with no members as crews.json of an older Conductor holds it.
func crewJSON(id, name string) string {
	return `{"id": "` + id + `", "name": "` + name + `", "where": "server", "isolation": "none", "members": []}`
}

// files returns the names in the crews directory of the data directory st.
func files(t *testing.T, st *store.Store) []string {
	t.Helper()
	des, err := os.ReadDir(filepath.Join(st.Dir(), "crews"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	return out
}

func ids(sums []Summary) []string {
	var out []string
	for _, s := range sums {
		out = append(out, s.ID)
	}
	return out
}

// Each crew is a file of its own, crews/<id>.json, as the store writes every
// document; a delete removes it.
func TestEachCrewIsAFile(t *testing.T) {
	s, st := newStore(t)
	for _, c := range []Crew{validCrew("zeta", "Zeta"), validCrew("alpha", "alpha")} {
		if err := s.Put(c); err != nil {
			t.Fatal(err)
		}
	}
	if got := files(t, st); !slices.Equal(got, []string{"alpha.json", "zeta.json"}) {
		t.Fatalf("files %v", got)
	}
	want, _ := store.Encode(validCrew("zeta", "Zeta"))
	if b, err := os.ReadFile(filepath.Join(st.Dir(), "crews", "zeta.json")); err != nil || string(b) != string(want) {
		t.Fatalf("zeta.json: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Join(st.Dir(), "crews.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("crews.json: %v", err)
	}
	if ok, err := s.Delete("zeta"); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if got := files(t, st); !slices.Equal(got, []string{"alpha.json"}) {
		t.Fatalf("after delete %v", got)
	}
	if ok, err := s.Delete("zeta"); ok || err != nil {
		t.Fatalf("second delete: %v %v", ok, err)
	}
	if ok, err := s.Delete("../x"); ok || err != nil {
		t.Fatalf("a bad id: %v %v", ok, err)
	}
}

// The list is read from the directory: by name ignoring case, then ID, a page
// at a time, with the total. A summary carries what the list shows and not
// the prompts. A file added, changed or removed by hand shows at the next
// listing.
// A crew named "new" gets another id: /crews/new is the workbench's page for a
// crew not saved yet.
func TestCreateNeverDerivesTheReservedID(t *testing.T) {
	s, _ := newStore(t)
	c, err := s.Create(validCrew("", "New"))
	if err != nil || c.ID != "new-2" {
		t.Fatalf("a crew named New got the id %q, %v", c.ID, err)
	}
	if c, err = s.Create(validCrew("", "new!")); err != nil || c.ID != "new-3" {
		t.Fatalf("the next got %q, %v", c.ID, err)
	}
}

// A crew a person names Brakeman is saved as brakeman-2: brakeman is a
// Brakeman turn's crew id, which no derived id takes. One saved under it
// before keeps it.
func TestBrakemanIsATakenID(t *testing.T) {
	s, _ := newStore(t)
	c, err := s.Create(validCrew("", "Brakeman"))
	if err != nil || c.ID != "brakeman-2" {
		t.Fatalf("a crew named Brakeman got the id %q, %v", c.ID, err)
	}
	if err := s.Put(validCrew("brakeman", "Brakeman")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("brakeman"); err != nil || got.ID != "brakeman" {
		t.Fatalf("a crew saved as brakeman before: %+v %v", got, err)
	}
}

func TestListPagesThroughTheDirectory(t *testing.T) {
	s, st := newStore(t)
	for i := range 7 {
		if err := s.Put(validCrew(fmt.Sprintf("c%d", i), fmt.Sprintf("Crew %d", 6-i))); err != nil {
			t.Fatal(err)
		}
	}
	page, total, err := s.List(0, 3)
	if err != nil || total != 7 || !slices.Equal(ids(page), []string{"c6", "c5", "c4"}) {
		t.Fatalf("first page %v of %d: %v", ids(page), total, err)
	}
	if page, total, _ = s.List(6, 3); total != 7 || !slices.Equal(ids(page), []string{"c0"}) {
		t.Fatalf("last page %v of %d", ids(page), total)
	}
	if page, total, _ = s.List(9, 3); total != 7 || len(page) != 0 {
		t.Fatalf("past the end %v of %d", ids(page), total)
	}
	page, _, _ = s.List(0, 1)
	want := Summary{ID: "c6", Name: "Crew 0", Goal: "ship /v1/users", Cwd: "/srv/api", Where: "server", Isolation: "worktree",
		Members: []MemberSummary{{Name: "lead", AgentID: "claude", Start: Start{When: "immediately"}}, {Name: "tests", AgentID: "shell", Start: Start{When: "after", Member: "lead"}}}, UpdatedAt: validCrew("", "").UpdatedAt}
	if !reflect.DeepEqual(page[0], want) {
		t.Fatalf("summary %+v, want %+v", page[0], want)
	}
	if b, _ := json.Marshal(page[0]); strings.Contains(string(b), "prompt") {
		t.Fatalf("a summary carries the prompts: %s", b)
	}
	dir := filepath.Join(st.Dir(), "crews")
	for id, name := range map[string]string{"hand": "AAA by hand", "c0": "ZZZ renamed by hand"} {
		b, _ := store.Encode(validCrew(id, name))
		if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(dir, "c3.json")); err != nil {
		t.Fatal(err)
	}
	page, total, _ = s.List(0, 10)
	if got := ids(page); total != 7 || got[0] != "hand" || got[6] != "c0" || slices.Contains(got, "c3") {
		t.Fatalf("after hand edits %v of %d", got, total)
	}
}

// A crew file that cannot be used is named at startup, left out of the list,
// and never overwritten: a new crew of the same name takes another ID, an
// update is refused, a delete removes it. The other crews load.
func TestACorruptCrewFileIsSkippedAndNeverOverwritten(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(st.Dir(), "crews")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	good, _ := store.Encode(validCrew("good", "Good"))
	for name, body := range map[string]string{
		"good.json":    string(good),
		"broken.json":  "{oops",
		"renamed.json": strings.Replace(string(good), `"id": "good"`, `"id": "other"`, 1),
		"huge.json":    `{"id": "huge", "name": "` + strings.Repeat("x", MaxEncoded) + `"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, problems, err := NewStore(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"broken.json", "renamed.json", "huge.json"} {
		if !slices.ContainsFunc(problems, func(p error) bool { return strings.Contains(p.Error(), filepath.Join(dir, name)) }) {
			t.Errorf("no problem names %s: %v", name, problems)
		}
	}
	if len(problems) != 3 {
		t.Fatalf("problems %v", problems)
	}
	if page, total, _ := s.List(0, 10); total != 1 || page[0].ID != "good" {
		t.Fatalf("list %v of %d", ids(page), total)
	}
	if _, err := s.Get("broken"); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("get: %v", err)
	}
	if c, err := s.Create(validCrew("", "Broken")); err != nil || c.ID != "broken-2" {
		t.Fatalf("create: %q %v", c.ID, err)
	}
	if _, err := s.Update("broken", validCrew("", "Fixed")); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("update: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "broken.json")); string(b) != "{oops" {
		t.Fatalf("broken.json was overwritten: %q", b)
	}
	if ok, err := s.Delete("broken"); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "broken.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("broken.json: %v", err)
	}
}

// crews.json of an older Conductor moves to one file per crew on the first
// start and is renamed crews.json.migrated; the next start changes nothing.
func TestMigrationSplitsCrewsJSONOnce(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(st.Dir(), "crews.json")
	legacy := `{"crews": [` + crewJSON("alpha", "Alpha") + `, ` + crewJSON("beta", "Beta") + `]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, problems, err := NewStore(st)
	if err != nil || len(problems) != 0 {
		t.Fatalf("NewStore: %v %v", problems, err)
	}
	if got := files(t, st); !slices.Equal(got, []string{"alpha.json", "beta.json"}) {
		t.Fatalf("files %v", got)
	}
	if b, err := os.ReadFile(path + ".migrated"); err != nil || string(b) != legacy {
		t.Fatalf("crews.json.migrated: %v %q", err, b)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("crews.json: %v", err)
	}
	if _, total, _ := s.List(0, 10); total != 2 {
		t.Fatalf("%d crews", total)
	}
	reopen(t, st)
	if _, err := os.Stat(path + ".migrated.2"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("migrated twice")
	}
}

// A start that stopped halfway through the move (some files written,
// crews.json still there) finishes it: every crew of crews.json, none twice,
// the crews made since kept, and an earlier crews.json.migrated kept too.
func TestMigrationFinishesAfterAnInterruptedStart(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(st.Dir(), "crews.json")
	legacy := `{"crews": [` + crewJSON("alpha", "Alpha") + `, ` + crewJSON("beta", "Beta") + `]}`
	for p, body := range map[string]string{path: legacy, path + ".migrated": "an earlier move"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := st.Sub("crews")
	if err != nil {
		t.Fatal(err)
	}
	var alpha Crew
	if err := json.Unmarshal([]byte(crewJSON("alpha", "Alpha")), &alpha); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Crew{"alpha.json": alpha.canonical(), "gamma.json": validCrew("gamma", "Gamma")} {
		if err := sub.Save(name, c); err != nil {
			t.Fatal(err)
		}
	}
	s := reopen(t, st)
	if got := files(t, st); !slices.Equal(got, []string{"alpha.json", "beta.json", "gamma.json"}) {
		t.Fatalf("files %v", got)
	}
	if _, total, _ := s.List(0, 10); total != 3 {
		t.Fatalf("%d crews", total)
	}
	for p, want := range map[string]string{path + ".migrated": "an earlier move", path + ".migrated.2": legacy} {
		if b, err := os.ReadFile(p); err != nil || string(b) != want {
			t.Fatalf("%s: %v %q", p, err, b)
		}
	}
}

// Two servers that start on one data directory both move crews.json, and
// write the same crew files: the one that renames it second finds it gone.
// That is the other's move, finished, and no reason to stop.
func TestMigrationGoesOnWhenAnotherServerRenamedCrewsJSON(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(st.Dir(), "crews.json")
	legacy := `{"crews": [` + crewJSON("alpha", "Alpha") + `, ` + crewJSON("beta", "Beta") + `]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rename = os.Rename })
	rename = func(from, to string) error {
		// The other server renames it first, to the same free name.
		if err := os.Rename(from, to); err != nil {
			return err
		}
		return os.Rename(from, to)
	}
	s, problems, err := NewStore(st)
	if err != nil || len(problems) != 0 {
		t.Fatalf("NewStore: %v %v", problems, err)
	}
	if got := files(t, st); !slices.Equal(got, []string{"alpha.json", "beta.json"}) {
		t.Fatalf("files %v", got)
	}
	if b, err := os.ReadFile(path + ".migrated"); err != nil || string(b) != legacy {
		t.Fatalf("crews.json.migrated: %v %q", err, b)
	}
	if _, total, _ := s.List(0, 10); total != 2 {
		t.Fatalf("%d crews", total)
	}
	// Any other failure to rename still stops startup.
	if err := os.Rename(path+".migrated", path); err != nil {
		t.Fatal(err)
	}
	rename = func(string, string) error { return &fs.PathError{Op: "rename", Path: path, Err: fs.ErrPermission} }
	if _, _, err := NewStore(st); err == nil || !strings.Contains(err.Error(), "could not be renamed") {
		t.Fatalf("NewStore: %v", err)
	}
}

// crews.json that cannot be used stops startup, as it always did, naming its
// path, and nothing is moved: a store that started without it would lose it.
func TestMigrationRefusesAMalformedCrewsJSON(t *testing.T) {
	cases := []struct{ name, file, want string }{
		{"not JSON", `{oops`, "parse crews.json"},
		{"empty file", ``, "empty document"},
		{"unknown field", `{"crews": [], "bogus": true}`, `"bogus"`},
		{"invalid crew", `{"crews": [` + crewJSON("ok", "x") + `, {"id": "bad", "name": "x", "where": "server", "isolation": "none", "members": [{"name": "Lead!", "agentId": "a", "prompt": "", "start": {"when": "manual"}}]}]}`, "crews[1]"},
		{"invalid id", `{"crews": [` + crewJSON("Bad Id", "x") + `]}`, "crews[0]"},
		{"id used twice", `{"crews": [` + crewJSON("x", "x") + `, ` + crewJSON("x", "y") + `]}`, "crews[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(st.Dir(), "crews.json")
			if err := os.WriteFile(path, []byte(tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			s, _, err := NewStore(st)
			if err == nil || s != nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewStore: %v, want an error naming %s and %q", err, path, tc.want)
			}
			if b, _ := os.ReadFile(path); string(b) != tc.file {
				t.Fatalf("crews.json changed: %q", b)
			}
			if entries, _ := os.ReadDir(filepath.Join(st.Dir(), "crews")); len(entries) != 0 {
				t.Fatalf("moved %v", entries)
			}
		})
	}
}

// There is no limit on the number of crews.
func TestStoreHoldsMoreThan50Crews(t *testing.T) {
	s, _ := newStore(t)
	for i := range 60 {
		if _, err := s.Create(validCrew("", fmt.Sprintf("Crew %02d", i))); err != nil {
			t.Fatalf("crew %d: %v", i, err)
		}
	}
	if _, err := s.Duplicate("crew-00"); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := s.List(0, 1); total != 61 {
		t.Fatalf("%d crews", total)
	}
}

// The move never overwrites a crew file. A crew of crews.json whose file
// exists and holds something else (a crew made or edited since) is not
// moved: a notice names both files, the file stays as it was, and
// crews.json.migrated keeps the crew whole. The next start has nothing to
// say.
func TestMigrationNeverOverwritesACrewFile(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(st.Dir(), "crews.json")
	legacy := `{"crews": [` + crewJSON("alpha", "Alpha from crews.json") + `, ` + crewJSON("beta", "Beta") + `]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	sub, err := st.Sub("crews")
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.Save("alpha.json", validCrew("alpha", "Alpha made since")); err != nil {
		t.Fatal(err)
	}
	alphaFile := filepath.Join(sub.Dir(), "alpha.json")
	before, err := os.ReadFile(alphaFile)
	if err != nil {
		t.Fatal(err)
	}
	s, problems, err := NewStore(st)
	if err != nil || len(problems) != 1 {
		t.Fatalf("NewStore: %v %v", problems, err)
	}
	for _, want := range []string{path, alphaFile, path + ".migrated"} {
		if !strings.Contains(problems[0].Error(), want) {
			t.Errorf("notice %q does not name %s", problems[0], want)
		}
	}
	if b, _ := os.ReadFile(alphaFile); string(b) != string(before) {
		t.Fatalf("alpha.json was overwritten:\n%s", b)
	}
	if c, err := s.Get("alpha"); err != nil || c.Name != "Alpha made since" {
		t.Fatalf("alpha: %q %v", c.Name, err)
	}
	if c, err := s.Get("beta"); err != nil || c.Name != "Beta" {
		t.Fatalf("beta: %q %v", c.Name, err)
	}
	if b, err := os.ReadFile(path + ".migrated"); err != nil || string(b) != legacy {
		t.Fatalf("crews.json.migrated: %v %q", err, b)
	}
	reopen(t, st)
}

// A crew file that is a symbolic link is never followed: Get and Update
// refuse it as unusable, the list leaves it out, a new crew does not take its
// ID, and a delete removes the link and not what it points to.
func TestALinkedCrewFileIsNotFollowed(t *testing.T) {
	s, st := newStore(t)
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	b, _ := store.Encode(validCrew("linked", "Linked"))
	if err := os.WriteFile(target, b, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(st.Dir(), "crews", "linked.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("linked"); !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("get: %v", err)
	}
	if _, err := s.Update("linked", validCrew("", "Changed")); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("update: %v", err)
	}
	if _, total, _ := s.List(0, 10); total != 0 {
		t.Fatalf("%d crews listed", total)
	}
	if c, err := s.Create(validCrew("", "Linked")); err != nil || c.ID != "linked-2" {
		t.Fatalf("create: %q %v", c.ID, err)
	}
	if ok, err := s.Delete("linked"); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the link: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != string(b) {
		t.Fatalf("the file the link named: %v %q", err, got)
	}
}

// A save is listed at once, even when the file keeps its size and its time
// (a rename to a name of the same length within the clock's tick): commit
// drops the summary the list cached for the file, and Delete does too.
func TestASaveIsListedAtOnce(t *testing.T) {
	s, st := newStore(t)
	path := filepath.Join(st.Dir(), "crews", "c.json")
	if err := s.Put(validCrew("c", "Alpha")); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	name := func() string {
		t.Helper()
		page, _, err := s.List(0, 1)
		if err != nil || len(page) != 1 {
			t.Fatalf("list: %v %v", page, err)
		}
		return page[0].Name
	}
	// saveAs puts the crew c under another name of five letters and gives
	// its file the first save's time back.
	saveAs := func(n string) {
		t.Helper()
		if err := s.Put(validCrew("c", n)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, first.ModTime(), first.ModTime()); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(path); fi.Size() != first.Size() {
			t.Fatalf("sizes %d and %d", fi.Size(), first.Size())
		}
	}
	if got := name(); got != "Alpha" {
		t.Fatalf("listed %q", got)
	}
	saveAs("Bravo")
	if got := name(); got != "Bravo" {
		t.Fatalf("listed %q after the save", got)
	}
	// A file put back by hand after a delete, with the same size and time,
	// is read again: Delete dropped the summary too.
	if ok, err := s.Delete("c"); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	b, _ := store.Encode(validCrew("c", "Carol"))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, first.ModTime(), first.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := name(); got != "Carol" {
		t.Fatalf("listed %q after a delete and a file put back by hand", got)
	}
}

// Get reads a crew while it is saved and never takes it for unusable: Get
// and a save take the store's lock, so the read never meets the rename (no
// store.ErrChanged at all), and the crew read is the one or the other.
func TestGetRacingAPutNeverSeesAnUnusableFile(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Put(validCrew("c", "Crew 0")); err != nil {
		t.Fatal(err)
	}
	real := loadLimit
	t.Cleanup(func() { loadLimit = real })
	var changed atomic.Int32
	loadLimit = func(st *store.Store, name string, v any, limit int64) (bool, error) {
		ok, err := real(st, name, v, limit)
		if errors.Is(err, store.ErrChanged) {
			changed.Add(1)
		}
		return ok, err
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := s.Put(validCrew("c", fmt.Sprintf("Crew %d", i%2))); err != nil {
				t.Error(err)
				return
			}
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; i < 3000 && time.Now().Before(deadline); i++ {
		if c, err := s.Get("c"); err != nil || c.ID != "c" {
			t.Errorf("get %d: %q %v", i, c.ID, err)
			break
		}
	}
	close(stop)
	wg.Wait()
	if n := changed.Load(); n != 0 {
		t.Errorf("a read met a save %d times", n)
	}
}

// A file that changes as it is read, another renamed into its place between
// the check and the open (a save by hand, say), is read again, once: it
// counts as unusable only when it changes again.
func TestReadRetriesAFileThatChangedOnce(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Put(validCrew("c", "Crew")); err != nil {
		t.Fatal(err)
	}
	real := loadLimit
	t.Cleanup(func() { loadLimit = real })
	changes := 0
	loadLimit = func(st *store.Store, name string, v any, limit int64) (bool, error) {
		if changes > 0 {
			changes--
			return false, fmt.Errorf("store: %s %w", name, store.ErrChanged)
		}
		return real(st, name, v, limit)
	}
	changes = 1
	if c, err := s.Get("c"); err != nil || c.Name != "Crew" {
		t.Fatalf("changed once: %q %v", c.Name, err)
	}
	changes = 2
	if _, err := s.Get("c"); !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "changed while it was opened") {
		t.Fatalf("changed twice: %v", err)
	}
}

// A file the store cannot read for now (its mode, say) is a read failure, not
// an unusable crew, and is not remembered as one: startup names it, a listing
// leaves it out, total included, and still answers, Get says it cannot read
// it, and once the file can be read again the crew is listed, though the
// file kept its size and time.
func TestAFileUnreadableForNowIsLeftOutOfOneListing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 0000")
	}
	_, st := newStore(t)
	sub, err := st.Sub("crews")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []Crew{validCrew("b", "Bravo"), validCrew("c", "Charlie")} {
		if err := sub.Save(c.ID+".json", c); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(sub.Dir(), "c.json")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	s, problems, err := NewStore(st)
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0].Error(), path) || errors.Is(problems[0], ErrUnreadable) {
		t.Fatalf("NewStore: %v %v", problems, err)
	}
	if page, total, err := s.List(0, 10); err != nil || total != 1 || !slices.Equal(ids(page), []string{"b"}) {
		t.Fatalf("list: %v of %d, %v", ids(page), total, err)
	}
	if _, err := s.Get("c"); err == nil || errors.Is(err, ErrUnreadable) || errors.Is(err, ErrNotFound) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("get: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if page, total, err := s.List(0, 10); err != nil || total != 2 || !slices.Equal(ids(page), []string{"b", "c"}) {
		t.Fatalf("list once readable: %v of %d, %v", ids(page), total, err)
	}
}

// freeName stops at an error other than "not there": it does not loop.
func TestFreeNameReportsWhatItCannotCheck(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Under a regular file, Lstat fails with "not a directory".
	if name, err := freeName(filepath.Join(file, "crews.json.migrated")); err == nil {
		t.Fatalf("freeName: %q", name)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.migrated"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if name, err := freeName(filepath.Join(dir, "x.migrated")); err != nil || name != filepath.Join(dir, "x.migrated.2") {
		t.Fatalf("freeName: %q %v", name, err)
	}
}

// storedKeys returns the top-level keys of the crew's file.
func storedKeys(t *testing.T, st *store.Store, id string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(st.Dir(), "crews", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	return keys
}

// The store counts a crew's manifest changes itself, whatever a save
// carries: a changed manifest is one more, an unchanged one keeps its
// number, a removal is a change the counter keeps, and a manifest put on
// again, after a restart too, is one past it, never 1 again.
func TestTheStoreCountsTheManifestsRevisions(t *testing.T) {
	s, st := newStore(t)
	first := manifestCrew()
	first.ManifestRevision = 9
	created, err := s.Create(first)
	if err != nil {
		t.Fatal(err)
	}
	if created.ManifestRevision != 1 {
		t.Fatalf("a new crew with a manifest: revision %d", created.ManifestRevision)
	}
	id := created.ID
	update := func(c Crew, want int64) Crew {
		t.Helper()
		got, err := s.Update(id, c)
		if err != nil {
			t.Fatal(err)
		}
		if got.ManifestRevision != want {
			t.Fatalf("revision %d, want %d", got.ManifestRevision, want)
		}
		read, err := s.Get(id)
		if err != nil || read.ManifestRevision != want {
			t.Fatalf("read back: %d %v", read.ManifestRevision, err)
		}
		return got
	}
	// Another field changed: the manifest keeps its number, and a number the
	// value carries is not the store's.
	c := created
	c.Goal, c.ManifestRevision = "another goal", 99
	update(c, 1)
	// The manifest changed.
	c.Manifest = fullManifest()
	c.Manifest.Setup[0].Argv = []string{"npm", "install"}
	update(c, 2)
	update(c, 2)
	// Taken off: a change, and the counter stays on the crew.
	c.Manifest = nil
	update(c, 3)
	if keys := storedKeys(t, st, id); string(keys["manifestRevision"]) != "3" || keys["manifest"] != nil {
		t.Fatalf("the file after the removal: %v", keys)
	}
	update(c, 3)
	// Put on again after a restart, the first manifest's content included.
	s = reopen(t, st)
	c.Manifest = fullManifest()
	update(c, 4)
	c.Manifest = &Manifest{}
	update(c, 5)
	if keys := storedKeys(t, st, id); keys["manifest"] != nil {
		t.Fatalf("an empty manifest is kept: %s", keys["manifest"])
	}
}

// A legacy crew's file stays as it is through a save that changes nothing
// of its setup: no manifest and no counter. Lifted into the manifest's setup
// phase, the setup is the same manifest, still 0.
func TestALegacySetupKeepsItsFileAndItsRevision(t *testing.T) {
	s, st := newStore(t)
	legacy := managedCrew()
	legacy.ID = "legacy"
	if err := s.Put(legacy); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	got.Goal = "a new goal"
	if saved, err := s.Update("legacy", got); err != nil || saved.ManifestRevision != 0 {
		t.Fatalf("a legacy save: %v %v", saved.ManifestRevision, err)
	}
	keys := storedKeys(t, st, "legacy")
	if keys["manifest"] != nil || keys["manifestRevision"] != nil || keys["setup"] == nil {
		t.Fatalf("the legacy file after a save: %v", keys)
	}
	lifted := got
	lifted.Manifest, lifted.Setup = &Manifest{Setup: got.Setup}, nil
	if saved, err := s.Update("legacy", lifted); err != nil || saved.ManifestRevision != 0 || saved.Manifest == nil || len(saved.Setup) != 0 {
		t.Fatalf("the lifted setup: %+v %v", saved, err)
	}
	if keys := storedKeys(t, st, "legacy"); keys["manifestRevision"] != nil || keys["setup"] != nil || keys["manifest"] == nil {
		t.Fatalf("the lifted file: %v", keys)
	}
	lifted.Manifest = &Manifest{Setup: []Check{{ID: "deps", Argv: []string{"npm", "install"}}}}
	if saved, err := s.Update("legacy", lifted); err != nil || saved.ManifestRevision != 1 {
		t.Fatalf("a changed setup: %v %v", saved.ManifestRevision, err)
	}
	// A counter below zero is not the store's: refused.
	bad := legacy
	bad.ManifestRevision = -1
	if err := s.Put(bad); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "manifestRevision must not be negative") {
		t.Fatalf("a negative revision: %v", err)
	}
}

// A new crew, made, copied or seeded, starts at 1 with a manifest (a legacy
// setup counts) and at 0 without.
func TestANewCrewsRevisionStartsAtOneWithAManifest(t *testing.T) {
	s, _ := newStore(t)
	for _, tc := range []struct {
		name string
		c    Crew
		want int64
	}{
		{"a manifest", manifestCrew(), 1},
		{"a legacy setup", managedCrew(), 1},
		{"tasks alone", func() Crew { c := managedCrew(); c.Setup = nil; return c }(), 0},
		{"an empty manifest", func() Crew { c := managedCrew(); c.Setup, c.Manifest = nil, &Manifest{}; return c }(), 0},
		{"no tasks", validCrew("x", "Plain"), 0},
	} {
		got, err := s.Create(tc.c)
		if err != nil || got.ManifestRevision != tc.want {
			t.Errorf("create, %s: %d %v", tc.name, got.ManifestRevision, err)
			continue
		}
		if tc.name == "an empty manifest" && got.Manifest != nil {
			t.Errorf("an empty manifest is kept: %+v", got.Manifest)
		}
	}
	src, err := s.Create(manifestCrew())
	if err != nil {
		t.Fatal(err)
	}
	src.Manifest.Setup[0].ID = "install"
	if src, err = s.Update(src.ID, src); err != nil || src.ManifestRevision != 2 {
		t.Fatalf("the source: %d %v", src.ManifestRevision, err)
	}
	if dup, err := s.Duplicate(src.ID); err != nil || dup.ManifestRevision != 1 || dup.Manifest == nil || dup.Manifest.Setup[0].ID != "install" {
		t.Fatalf("a copy: %+v %v", dup, err)
	}
	seeded := manifestCrew()
	seeded.ID, seeded.ManifestRevision = "seeded", 7
	plain := validCrew("seeded-plain", "Plain")
	plain.ManifestRevision = 7
	if added, _, err := s.Seed([]Crew{seeded, plain}); err != nil || len(added) != 2 {
		t.Fatalf("seed: %v %v", added, err)
	}
	if got, err := s.Get("seeded"); err != nil || got.ManifestRevision != 1 {
		t.Fatalf("a seeded manifest: %d %v", got.ManifestRevision, err)
	}
	if got, err := s.Get("seeded-plain"); err != nil || got.ManifestRevision != 0 {
		t.Fatalf("a seeded crew without one: %d %v", got.ManifestRevision, err)
	}
}

// The store's copies share no manifest with what went in or came out.
func TestTheStoresCopiesShareNoManifest(t *testing.T) {
	s, _ := newStore(t)
	c := manifestCrew()
	created, err := s.Create(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Manifest.Setup[0].Argv[0] = "changed"
	created.Manifest.Ready[0].Argv[0] = "changed"
	got, err := s.Get(created.ID)
	if err != nil || got.Manifest.Setup[0].Argv[0] != "npm" || got.Manifest.Ready[0].Argv[0] != "pg_isready" || created.Manifest == c.Manifest {
		t.Fatalf("the stored manifest: %+v %v", got.Manifest, err)
	}
}
