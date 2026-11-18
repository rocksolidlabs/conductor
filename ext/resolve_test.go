package ext_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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

	// Every other field, in the record, its manifest and a member, changes
	// it: a field added to the record is pinned unless this test is told.
	unpinned := map[string]bool{"Digest": true}
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
	if seen < 18 {
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
			r.Cwd, r.ProjectCwd = "/"+strings.Repeat(" ", 1365), "/"+strings.Repeat(" ", 1365)
		}), false},
	} {
		if (c.err == nil) != c.ok {
			t.Errorf("%s: Valid() = %v, want ok %v", c.name, c.err, c.ok)
		}
	}
}
