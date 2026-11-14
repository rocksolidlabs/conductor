package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// crewWrite is an assignment's left side that writes a session's crew
// reference: the whole Info, its Crew, or a field of the reference.
var crewWrite = regexp.MustCompile(`(\.info|\.Crew|\.Crew\.[A-Za-z]+|\.Lane|\.Lane\.[A-Za-z]+)$`)

// crewWriters is every function of the package that writes a session's
// crew reference, by its name ("Recv.Name" for a method).
func crewWriters(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var out []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			fname := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				fname = strings.TrimPrefix(types.ExprString(fn.Recv.List[0].Type), "*") + "." + fname
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range as.Lhs {
					if crewWrite.MatchString(types.ExprString(lhs)) && !slices.Contains(out, fname) {
						out = append(out, fname)
					}
				}
				return true
			})
		}
	}
	slices.Sort(out)
	return out
}

// A session's lane, given at its launch, is never replaced: SetCrew is
// the one function of the package that writes a crew reference, and on a
// session whose reference carries a lane it sets nothing and says so,
// whatever reference it is handed; on any other session it sets the
// reference handed.
func TestALanesCrewReferenceIsNeverReplaced(t *testing.T) {
	if got := crewWriters(t); !slices.Equal(got, []string{"Local.SetCrew"}) {
		t.Fatalf("the functions that write a crew reference are %v: each must keep a lane as SetCrew does", got)
	}
	ref := CrewRef{RunID: "crew-0000000b", CrewID: "compare", Member: "cand-b", Task: "approach-b", Attempt: 1, Lane: &LaneMark{Candidate: "approach-b"}}
	given := ref
	p := newFakeProc()
	s := NewLocal(Info{ID: "lane", Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24, Crew: &given}, p, Options{ScrollbackBytes: 4096})
	t.Cleanup(func() { p.exit() })
	for _, next := range []CrewRef{
		{RunID: "crew-0000000c", CrewID: "mine", Member: "me"},
		{RunID: ref.RunID, CrewID: ref.CrewID, Member: ref.Member},
		{RunID: ref.RunID, CrewID: ref.CrewID, Member: "cand-a", Lane: &LaneMark{Candidate: "approach-a"}},
		{RunID: ref.RunID, CrewID: ref.CrewID, Member: ref.Member, Lane: &LaneMark{}},
	} {
		if s.SetCrew(next) {
			t.Errorf("SetCrew(%+v) set a reference on a lane's session", next)
		}
		if got := s.Info().Crew; got == nil || !reflect.DeepEqual(*got, ref) || *got.Lane != *ref.Lane {
			t.Fatalf("after SetCrew(%+v) the reference is %+v", next, got)
		}
	}
	plain, _ := newLocal(t, t.TempDir())
	adopted := CrewRef{RunID: "crew-0000000d", CrewID: "mine", Member: "me"}
	if !plain.SetCrew(adopted) || !reflect.DeepEqual(*plain.Info().Crew, adopted) {
		t.Fatalf("a session of no lane: %+v", plain.Info().Crew)
	}
	if !plain.SetCrew(CrewRef{RunID: "crew-0000000e", CrewID: "other", Member: "me"}) || plain.Info().Crew.RunID != "crew-0000000e" {
		t.Fatalf("a session of no lane adopted again: %+v", plain.Info().Crew)
	}
}
