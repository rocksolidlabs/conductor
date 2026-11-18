package ext_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// goldenLaunch is a resolved launch with every pinned field set, the
// digest's golden vector.
func goldenLaunch() ext.ResolvedLaunch {
	return ext.ResolvedLaunch{
		Crew: "nightly", Name: "Nightly <checks> & more", Managed: true, Tasks: 3, Where: ext.RunsOnServer, Project: "CND",
		ProjectCwd: "/src/cnd", Cwd: "/src/cnd/web", InProject: true,
		Manifest: ext.ResolvedManifest{Source: ext.ManifestFile, Blob: strings.Repeat("c", 40)},
		Members: []ext.ResolvedMember{
			{Name: "planner", Agent: "claude", Start: "task", Selection: strings.Repeat("d", 64)},
			{Name: "builder", Agent: "codex", Start: "task", Yolo: true, Selection: strings.Repeat("e", 64)},
		},
	}
}

// boundLaunch is goldenLaunch with each member's binding and a capacity
// view.
func boundLaunch() ext.ResolvedLaunch {
	r := goldenLaunch()
	r.Members[0].Account, r.Members[0].AccountSource, r.Members[0].Model = "default", ext.AccountOwn, ""
	r.Members[1].Account, r.Members[1].AccountSource, r.Members[1].Model = "a-abcdefgh2345", ext.AccountAccount, "gpt-5.4"
	on := true
	r.Capacity = []ext.CapacityView{{
		Agent: "codex", Account: "a-abcdefgh2345", Label: "Work", State: "ok", Fresh: ext.CapacityFresh,
		ReadAt: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC), Running: 1, Started5h: 2, Started7d: 3,
		Windows: []ext.CapacityWindow{{Kind: "primary", Name: "5-hour", Minutes: 300, Percent: 42, Severity: "normal"}},
		Credits: []ext.CapacityCredit{{Kind: "credits", Enabled: &on, Balance: "8.50", Currency: "USD"}},
	}}
	return r
}

// goldenCanonical is goldenLaunch as the digest encodes it: declared
// order, no HTML escaping, the digest empty.
var goldenCanonical = `{"crew":"nightly","name":"Nightly <checks> & more","managed":true,"tasks":3,"where":"server",` +
	`"project":"CND","projectCwd":"/src/cnd","cwd":"/src/cnd/web","inProject":true,` +
	`"manifest":{"source":"file","blob":"` + strings.Repeat("c", 40) + `"},` +
	`"members":[{"name":"planner","agent":"claude","start":"task","yolo":false,"selection":"` + strings.Repeat("d", 64) + `"},` +
	`{"name":"builder","agent":"codex","start":"task","yolo":true,"selection":"` + strings.Repeat("e", 64) + `"}],"digest":""}`

func TestResolvedDigest(t *testing.T) {
	rev := strings.Repeat("1", 64)
	// The golden vectors, worked out apart from the code: a change of the
	// encoding changes every digest a provider holds, and is a new prefix.
	for _, c := range []struct {
		name string
		r    ext.ResolvedLaunch
		rev  string
		want string
	}{
		{"every field", goldenLaunch(), rev, "22374238e7f4fe4e90af175fa6a4109e0d8354caa05abc5a8210d16f7c2fb469"},
		{"the zero record", ext.ResolvedLaunch{}, strings.Repeat("0", 64), "3d0f384e867cba7bc111df1b09ac0943b0bc220a3584d731a97335d3847b9a0d"},
		{"a member bound to an account", boundLaunch(), rev, "1ef7023435177b7ca5d84561c85bd8daa4f29ca1a3c55e25edfaf41f5f988589"},
	} {
		got, ok := ext.ResolvedDigest(c.r, c.rev)
		if !ok || got != c.want {
			t.Errorf("%s: ResolvedDigest = %s, %v; want %s", c.name, got, ok, c.want)
		}
	}
	sum := sha256.Sum256([]byte("conductor resolved launch 1\n" + rev + "\n" + goldenCanonical))
	if got, _ := ext.ResolvedDigest(goldenLaunch(), rev); got != hex.EncodeToString(sum[:]) {
		t.Errorf("the digest is not of the canonical encoding %s", goldenCanonical)
	}

	base, _ := ext.ResolvedDigest(goldenLaunch(), rev)
	digest := func(f func(*ext.ResolvedLaunch)) string {
		r := goldenLaunch()
		f(&r)
		d, ok := ext.ResolvedDigest(r, rev)
		if !ok {
			t.Fatalf("ResolvedDigest refused %+v", r)
		}
		return d
	}
	if d := digest(func(r *ext.ResolvedLaunch) { r.Digest = strings.Repeat("f", 64) }); d != base {
		t.Error("the digest changes with the record's own digest")
	}
	if d, _ := ext.ResolvedDigest(goldenLaunch(), strings.Repeat("2", 64)); d == base {
		t.Error("the digest does not change with the crew's revision")
	}
	if a, b := digest(func(r *ext.ResolvedLaunch) { r.Members = nil }), digest(func(r *ext.ResolvedLaunch) { r.Members = []ext.ResolvedMember{} }); a != b {
		t.Error("no members and an empty list of them give two digests")
	}

	// The capacity views never change it: their usage, counts, freshness
	// and time, none at all.
	bound, _ := ext.ResolvedDigest(boundLaunch(), rev)
	for name, f := range map[string]func(*ext.ResolvedLaunch){
		"none":   func(r *ext.ResolvedLaunch) { r.Capacity = nil },
		"usage":  func(r *ext.ResolvedLaunch) { r.Capacity[0].Windows[0].Percent = 99 },
		"counts": func(r *ext.ResolvedLaunch) { r.Capacity[0].Running, r.Capacity[0].Started7d = 4, 9 },
		"freshness": func(r *ext.ResolvedLaunch) {
			r.Capacity[0].Fresh, r.Capacity[0].ReadAt = ext.CapacityStale, time.Time{}
		},
		"another": func(r *ext.ResolvedLaunch) { r.Capacity = append(r.Capacity, r.Capacity[0]) },
	} {
		r := boundLaunch()
		f(&r)
		if d, _ := ext.ResolvedDigest(r, rev); d != bound {
			t.Errorf("a change of the capacity views (%s) changes the digest", name)
		}
	}

	// Every other field, in the record, its manifest and a member, changes
	// it: a field added to the record is pinned unless this test is told.
	unpinned := map[string]bool{"Digest": true, "Capacity": true}
	seen := 0
	for _, path := range fieldPaths(reflect.TypeOf(ext.ResolvedLaunch{}), nil) {
		if unpinned[path[0]] {
			continue
		}
		seen++
		if d := digest(func(r *ext.ResolvedLaunch) { change(t, reflect.ValueOf(r).Elem(), path) }); d == base {
			t.Errorf("a change of %s leaves the digest", strings.Join(path, "."))
		}
	}
	if seen < 21 {
		t.Errorf("only %d fields were changed", seen)
	}

	// Past its bound, no digest; a revision that is not one, none.
	if _, ok := ext.ResolvedDigest(goldenLaunch(), "HEAD"); ok {
		t.Error("a digest over a crew revision that is not one")
	}
	big := goldenLaunch()
	big.Cwd = "/" + strings.Repeat("a", ext.MaxResolvedLaunch)
	if _, ok := ext.ResolvedDigest(big, rev); ok {
		t.Error("a digest of a record past its bound")
	}
}

// fieldPaths is every leaf field of typ, a struct: a path of field names,
// a slice's first element named "0".
func fieldPaths(typ reflect.Type, prefix []string) [][]string {
	var out [][]string
	for i := range typ.NumField() {
		f := typ.Field(i)
		p := append(append([]string(nil), prefix...), f.Name)
		switch {
		case f.Type.Kind() == reflect.Struct && f.Type.String() != "time.Time":
			out = append(out, fieldPaths(f.Type, p)...)
		case f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.Struct:
			out = append(out, fieldPaths(f.Type.Elem(), append(p, "0"))...)
		default:
			out = append(out, p)
		}
	}
	return out
}

// change changes the field at path in v.
func change(t *testing.T, v reflect.Value, path []string) {
	for _, name := range path {
		if name == "0" {
			v = v.Index(0)
			continue
		}
		v = v.FieldByName(name)
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "x")
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		} else {
			v.Set(reflect.Zero(v.Type()))
		}
	default:
		t.Fatalf("no change of a %s at %v", v.Kind(), path)
	}
}

func TestResolveRequestBounds(t *testing.T) {
	for _, c := range []struct {
		name string
		r    ext.ResolveRequest
		ok   bool
	}{
		{"a crew", ext.ResolveRequest{Crew: "nightly"}, true},
		{"a crew for a project at a commit", ext.ResolveRequest{Crew: "nightly", Project: "CND", Commit: sha40}, true},
		{"a commit of 64 hex", ext.ResolveRequest{Crew: "nightly", Commit: sha64}, true},
		{"a project's key at its bound", ext.ResolveRequest{Crew: "nightly", Project: "C12345"}, true},
		{"a project's key past its bound", ext.ResolveRequest{Crew: "nightly", Project: "C123456"}, false},
		{"a project's key of one letter", ext.ResolveRequest{Crew: "nightly", Project: "C"}, false},
		{"a project's key in lower case", ext.ResolveRequest{Crew: "nightly", Project: "cnd"}, false},
		{"a project's key beginning with a digit", ext.ResolveRequest{Crew: "nightly", Project: "1CN"}, false},
		{"HEAD", ext.ResolveRequest{Crew: "nightly", Commit: "HEAD"}, false},
		{"HEAD~1", ext.ResolveRequest{Crew: "nightly", Commit: "HEAD~1"}, false},
		{"a short id", ext.ResolveRequest{Crew: "nightly", Commit: sha40[:12]}, false},
		{"a branch", ext.ResolveRequest{Crew: "nightly", Commit: "main"}, false},
		{"a commit in capitals", ext.ResolveRequest{Crew: "nightly", Commit: strings.ToUpper(sha40)}, false},
		{"no crew", ext.ResolveRequest{}, false},
		{"a crew that is no id", ext.ResolveRequest{Crew: "Nightly"}, false},
	} {
		if err := c.r.Valid(); (err == nil) != c.ok {
			t.Errorf("%s: Valid() = %v, want ok %v", c.name, err, c.ok)
		}
	}
}

func TestResolvedLaunchValid(t *testing.T) {
	abs := filepath.Join(os.TempDir(), "cnd")
	good := goldenLaunch()
	good.ProjectCwd, good.Cwd = abs, filepath.Join(abs, "web")
	edit := func(f func(*ext.ResolvedLaunch)) error {
		r := good
		r.Members = append([]ext.ResolvedMember(nil), good.Members...)
		f(&r)
		return r.Valid()
	}
	member := func(f func(*ext.ResolvedMember)) error { return edit(func(r *ext.ResolvedLaunch) { f(&r.Members[0]) }) }
	for _, c := range []struct {
		name string
		err  error
		ok   bool
	}{
		{"a resolved launch", good.Valid(), true},
		{"with its digest", edit(func(r *ext.ResolvedLaunch) { r.Digest = strings.Repeat("f", 64) }), true},
		{"a crew without tasks", edit(func(r *ext.ResolvedLaunch) { r.Managed, r.Tasks = false, 0 }), true},
		{"on a host", edit(func(r *ext.ResolvedLaunch) { r.Where = ext.RunsOnHost }), true},
		{"no project", edit(func(r *ext.ResolvedLaunch) { r.Project, r.ProjectCwd, r.InProject = "", "", false }), true},
		{"a project with no folder", edit(func(r *ext.ResolvedLaunch) { r.ProjectCwd, r.InProject = "", false }), true},
		{"the crew's manifest", edit(func(r *ext.ResolvedLaunch) { r.Manifest = ext.ResolvedManifest{Source: ext.ManifestCrew, Revision: 4} }), true},
		{"no manifest", edit(func(r *ext.ResolvedLaunch) { r.Manifest = ext.ResolvedManifest{Source: ext.ManifestNone} }), true},
		{"members at their bound", edit(func(r *ext.ResolvedLaunch) {
			for len(r.Members) < ext.MaxResolvedMembers {
				r.Members = append(r.Members, r.Members[0])
			}
		}), true},
		{"a member that waits for another", member(func(m *ext.ResolvedMember) { m.Start, m.After = "after", "builder" }), true},
		{"a member started by hand", member(func(m *ext.ResolvedMember) { m.Start = "manual" }), true},

		{"managed without tasks", edit(func(r *ext.ResolvedLaunch) { r.Tasks = 0 }), false},
		{"tasks and not managed", edit(func(r *ext.ResolvedLaunch) { r.Managed = false }), false},
		{"tasks past their bound", edit(func(r *ext.ResolvedLaunch) { r.Tasks = ext.MaxCrewTasks + 1 }), false},
		{"a name past its bound", edit(func(r *ext.ResolvedLaunch) { r.Name = strings.Repeat("a", ext.MaxCrewName+1) }), false},
		{"no name", edit(func(r *ext.ResolvedLaunch) { r.Name = "" }), false},
		{"where nowhere", edit(func(r *ext.ResolvedLaunch) { r.Where = "cloud" }), false},
		{"a project folder with no project", edit(func(r *ext.ResolvedLaunch) { r.Project = "" }), false},
		{"in a project with no folder", edit(func(r *ext.ResolvedLaunch) { r.ProjectCwd = "" }), false},
		{"a relative folder", edit(func(r *ext.ResolvedLaunch) { r.Cwd = "web" }), false},
		{"a relative project folder", edit(func(r *ext.ResolvedLaunch) { r.ProjectCwd = "cnd" }), false},
		{"no folder", edit(func(r *ext.ResolvedLaunch) { r.Cwd = "" }), false},
		{"no members", edit(func(r *ext.ResolvedLaunch) { r.Members = nil }), false},
		{"members past their bound", edit(func(r *ext.ResolvedLaunch) {
			for len(r.Members) <= ext.MaxResolvedMembers {
				r.Members = append(r.Members, r.Members[0])
			}
		}), false},
		{"a digest that is not one", edit(func(r *ext.ResolvedLaunch) { r.Digest = "abc" }), false},
		{"a manifest of no source", edit(func(r *ext.ResolvedLaunch) { r.Manifest = ext.ResolvedManifest{} }), false},
		{"a file's manifest without its blob", edit(func(r *ext.ResolvedLaunch) { r.Manifest = ext.ResolvedManifest{Source: ext.ManifestFile} }), false},
		{"a crew's manifest with a blob", edit(func(r *ext.ResolvedLaunch) { r.Manifest.Source = ext.ManifestCrew }), false},
		{"no manifest with a revision", edit(func(r *ext.ResolvedLaunch) { r.Manifest = ext.ResolvedManifest{Source: ext.ManifestNone, Revision: 1} }), false},
		{"a member's name in capitals", member(func(m *ext.ResolvedMember) { m.Name = "Planner" }), false},
		{"a member's name past its bound", member(func(m *ext.ResolvedMember) { m.Name = strings.Repeat("a", ext.MaxMemberName+1) }), false},
		{"a member's agent that is no id", member(func(m *ext.ResolvedMember) { m.Agent = "" }), false},
		{"a member's start of another kind", member(func(m *ext.ResolvedMember) { m.Start = "later" }), false},
		{"after with no member", member(func(m *ext.ResolvedMember) { m.Start = "after" }), false},
		{"a member waited for without after", member(func(m *ext.ResolvedMember) { m.After = "builder" }), false},
		{"a selection that is not one", member(func(m *ext.ResolvedMember) { m.Selection = "abc" }), false},
		{"a folder at its bound", edit(func(r *ext.ResolvedLaunch) { r.Cwd = "/" + strings.Repeat("a", ext.MaxFolder-1) }), true},
		{"two folders past its bound once escaped", edit(func(r *ext.ResolvedLaunch) {
			r.Cwd, r.ProjectCwd = "/"+strings.Repeat("\u2028", 1365), "/"+strings.Repeat("\u2028", 1365)
		}), false},
	} {
		if (c.err == nil) != c.ok {
			t.Errorf("%s: Valid() = %v, want ok %v", c.name, c.err, c.ok)
		}
	}
}

// TestAResolvedLaunchAtEveryBoundIsWithinItsReply holds the whole reply
// to MaxResolvedReply: the pinned part at its bound and every capacity
// view at its bounds, each field at its escaped worst (a byte the encoder
// writes as six, a character as six).
func TestAResolvedLaunchAtEveryBoundIsWithinItsReply(t *testing.T) {
	worst := func(n int) string { return strings.Repeat("<", n) }
	worstChars := func(n int) string { return strings.Repeat("\u2028", n) }
	on := true
	readAt := time.Date(2026, 11, 18, 9, 0, 0, 123456789, time.FixedZone("x", -12*3600))
	window := ext.CapacityWindow{
		Kind: worst(ext.MaxCapacityWord), Name: worst(ext.MaxCapacityName), Minutes: 1<<62 - 1, Percent: 100,
		ResetsAt: readAt, Severity: "critical",
	}
	credit := ext.CapacityCredit{
		Kind: worst(ext.MaxCapacityWord), Enabled: &on, Used: worst(ext.MaxCapacityFigure), Limit: worst(ext.MaxCapacityFigure),
		Balance: worst(ext.MaxCapacityFigure), Unlimited: true, Currency: worst(ext.MaxCapacityWord),
	}
	view := ext.CapacityView{
		Agent: strings.Repeat("a", ext.MaxAgentID), Account: "a-abcdefgh2345", Label: worstChars(ext.MaxAccountLabel),
		State: strings.Repeat("s", ext.MaxCapacityWord), Fresh: ext.CapacityResetPassed, ReadAt: readAt,
		Running: 1<<62 - 1, Started5h: 1<<62 - 1, Started7d: 1<<62 - 1,
	}
	for range ext.MaxCapacityWindows {
		view.Windows = append(view.Windows, window)
	}
	for range ext.MaxCapacityCredits {
		view.Credits = append(view.Credits, credit)
	}
	if err := view.Valid(); err != nil {
		t.Fatalf("the view at its bounds: %v", err)
	}
	r := boundLaunch()
	r.ProjectCwd, r.Cwd = filepath.Join(os.TempDir(), "p"), filepath.Join(os.TempDir(), "p")
	// The pinned part brought up to its bound by the folder.
	for {
		c := r
		if len(c.Cwd) < ext.MaxFolder {
			c.Cwd += "<"
		} else {
			c.ProjectCwd += "<"
		}
		if _, ok := ext.ResolvedDigest(c, strings.Repeat("1", 64)); !ok {
			break
		}
		r = c
	}
	r.Capacity = nil
	for range ext.MaxCapacityViews {
		r.Capacity = append(r.Capacity, view)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > ext.MaxResolvedReply {
		t.Fatalf("a resolved launch at every bound is %d bytes, past MaxResolvedReply %d", len(b), ext.MaxResolvedReply)
	}
	if err := r.Valid(); err != nil {
		t.Fatalf("a resolved launch at every bound: %v", err)
	}
	t.Logf("at every bound: %d bytes", len(b))

	// One view more, a window more or a credit more is refused.
	over := r
	over.Capacity = append(append([]ext.CapacityView(nil), r.Capacity...), view)
	if over.Valid() == nil {
		t.Error("a view past MaxCapacityViews was taken")
	}
	v := view
	v.Windows = append(append([]ext.CapacityWindow(nil), view.Windows...), window)
	if v.Valid() == nil {
		t.Error("a window past MaxCapacityWindows was taken")
	}
	v = view
	v.Credits = append(append([]ext.CapacityCredit(nil), view.Credits...), credit)
	if v.Valid() == nil {
		t.Error("a credit past MaxCapacityCredits was taken")
	}
}

func TestCapacityViewValid(t *testing.T) {
	good := boundLaunch().Capacity[0]
	edit := func(f func(*ext.CapacityView)) error {
		v := good
		v.Windows = append([]ext.CapacityWindow(nil), good.Windows...)
		v.Credits = append([]ext.CapacityCredit(nil), good.Credits...)
		f(&v)
		return v.Valid()
	}
	for _, c := range []struct {
		name string
		err  error
		ok   bool
	}{
		{"a view", good.Valid(), true},
		{"nothing read", ext.CapacityView{Agent: "codex", Account: "default", State: "never_read", Fresh: ext.CapacityNone}.Valid(), true},
		{"a label at its bound", edit(func(v *ext.CapacityView) { v.Label = strings.Repeat("é", ext.MaxAccountLabel) }), true},
		{"a label past its bound", edit(func(v *ext.CapacityView) { v.Label = strings.Repeat("a", ext.MaxAccountLabel+1) }), false},
		{"a label of two lines", edit(func(v *ext.CapacityView) { v.Label = "a\nb" }), false},
		{"an account that is no id", edit(func(v *ext.CapacityView) { v.Account = "/home/u/.codex" }), false},
		{"an account of a minted shape in capitals", edit(func(v *ext.CapacityView) { v.Account = "a-ABCDEFGH2345" }), false},
		{"an agent that is no id", edit(func(v *ext.CapacityView) { v.Agent = "" }), false},
		{"a state of words", edit(func(v *ext.CapacityView) { v.State = "signed out" }), false},
		{"a state past its bound", edit(func(v *ext.CapacityView) { v.State = strings.Repeat("s", ext.MaxCapacityWord+1) }), false},
		{"another freshness", edit(func(v *ext.CapacityView) { v.Fresh = "old" }), false},
		{"a count below 0", edit(func(v *ext.CapacityView) { v.Started5h = -1 }), false},
		{"a percent past 100", edit(func(v *ext.CapacityView) { v.Windows[0].Percent = 101 }), false},
		{"a percent below 0", edit(func(v *ext.CapacityView) { v.Windows[0].Percent = -1 }), false},
		{"minutes below 0", edit(func(v *ext.CapacityView) { v.Windows[0].Minutes = -1 }), false},
		{"another severity", edit(func(v *ext.CapacityView) { v.Windows[0].Severity = "high" }), false},
		{"a window without a kind", edit(func(v *ext.CapacityView) { v.Windows[0].Kind = "" }), false},
		{"a window's name past its bound", edit(func(v *ext.CapacityView) { v.Windows[0].Name = strings.Repeat("a", ext.MaxCapacityName+1) }), false},
		{"a figure past its bound", edit(func(v *ext.CapacityView) { v.Credits[0].Balance = strings.Repeat("9", ext.MaxCapacityFigure+1) }), false},
		{"a currency past its bound", edit(func(v *ext.CapacityView) { v.Credits[0].Currency = strings.Repeat("a", ext.MaxCapacityWord+1) }), false},
		{"a credit without a kind", edit(func(v *ext.CapacityView) { v.Credits[0].Kind = "" }), false},
	} {
		if (c.err == nil) != c.ok {
			t.Errorf("%s: Valid() = %v, want ok %v", c.name, c.err, c.ok)
		}
	}

	// A member's binding.
	member := func(f func(*ext.ResolvedMember)) error {
		r := boundLaunch()
		r.ProjectCwd, r.Cwd = filepath.Join(os.TempDir(), "p"), filepath.Join(os.TempDir(), "p")
		f(&r.Members[1])
		return r.Valid()
	}
	for _, c := range []struct {
		name string
		err  error
		ok   bool
	}{
		{"a member bound to an account", member(func(*ext.ResolvedMember) {}), true},
		{"a member with no binding", member(func(m *ext.ResolvedMember) { m.Account, m.AccountSource, m.Model = "", "", "" }), true},
		{"every source", errors.Join(
			member(func(m *ext.ResolvedMember) { m.Account, m.AccountSource = "default", ext.AccountOwn }),
			member(func(m *ext.ResolvedMember) { m.Account, m.AccountSource = "default", ext.AccountPinned }),
			member(func(m *ext.ResolvedMember) { m.Account, m.AccountSource = "default", ext.AccountLaunchEnv }),
		), true},
		{"an account without its source", member(func(m *ext.ResolvedMember) { m.AccountSource = "" }), false},
		{"a source without its account", member(func(m *ext.ResolvedMember) { m.Account = "" }), false},
		{"another source", member(func(m *ext.ResolvedMember) { m.AccountSource = "unlisted" }), false},
		{"an account's folder for its id", member(func(m *ext.ResolvedMember) { m.Account = "/home/u/.codex" }), false},
		{"a model at its bound", member(func(m *ext.ResolvedMember) { m.Model = strings.Repeat("m", ext.MaxModel) }), true},
		{"a model past its bound", member(func(m *ext.ResolvedMember) { m.Model = strings.Repeat("m", ext.MaxModel+1) }), false},
		{"a model with a space", member(func(m *ext.ResolvedMember) { m.Model = "gpt 5" }), false},
	} {
		if (c.err == nil) != c.ok {
			t.Errorf("%s: Valid() = %v, want ok %v", c.name, c.err, c.ok)
		}
	}
}
