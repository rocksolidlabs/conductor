package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestOnlyTheViewersSubmitSetsExpand holds I19's one place (docs/plans/
// chat-or-terminal.md, row 12): the word that sends a chat's line without
// the as-written mark is set in exactly two places, the server's own
// viewer socket (a submit's expand into Submission.Expand, and the socket's
// own mark, AttachOptions.ViewerSocket) and the session's one line that
// builds the queued message (Message.Expand), and read only where it is
// honoured: the session's submitProtocol and the driver's Send. A file
// that sets or reads Expand or ViewerSocket anywhere else (a crew's line,
// a peer's submit, a run's broadcast, an extension) fails it, and so does
// a second set in an allowed file. Every Go file outside tests in the
// module is walked, from its root (the app's screens, the desktop app, the
// phone shells, node_modules and testdata aside); a positional literal of a
// Submission, a Message or an AttachOptions, and a field named by its name
// (reflection: FieldByName), fail it anywhere, since they set the word
// without naming it.
func TestOnlyTheViewersSubmitSetsExpand(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	type use struct{ file, name, how string }
	count := map[use]int{}
	fset := token.NewFileSet()
	walked := 0
	skip := map[string]bool{"testdata": true, "node_modules": true, "web": true, "desktop": true, "mobile": true, ".git": true, ".claude": true}
	var unseen []string
	for _, top := range []string{"."} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != root && (skip[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			walked++
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			watched := func(n string) bool { return n == "Expand" || n == "ViewerSocket" }
			// What is set: a composite literal's key, an assignment's target.
			setAt := map[ast.Node]bool{}
			called := map[ast.Node]bool{}
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.KeyValueExpr:
					if k, ok := n.Key.(*ast.Ident); ok && watched(k.Name) {
						count[use{rel, k.Name, "set"}]++
					}
				case *ast.AssignStmt:
					for _, l := range n.Lhs {
						if sel, ok := l.(*ast.SelectorExpr); ok && watched(sel.Sel.Name) {
							setAt[sel] = true
							count[use{rel, sel.Sel.Name, "set"}]++
						}
					}
				case *ast.CallExpr:
					called[n.Fun] = true
					// A field named by its name: reflection sets it unseen.
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "FieldByName" && len(n.Args) == 1 {
						if lit, ok := n.Args[0].(*ast.BasicLit); ok && (strings.Contains(lit.Value, "Expand") || strings.Contains(lit.Value, "ViewerSocket")) {
							unseen = append(unseen, rel+": FieldByName("+lit.Value+")")
						}
					}
				case *ast.CompositeLit:
					// A positional literal of the three types sets the word
					// by its place alone.
					if name := typeName(n.Type); (name == "Submission" || name == "Message" || name == "AttachOptions") && len(n.Elts) > 0 {
						if _, keyed := n.Elts[0].(*ast.KeyValueExpr); !keyed {
							unseen = append(unseen, rel+": a positional "+name+" literal")
						}
					}
				}
				return true
			})
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && watched(sel.Sel.Name) && !setAt[sel] && !called[sel] {
					count[use{rel, sel.Sel.Name, "read"}]++
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if walked < 500 {
		t.Fatalf("walked %d files: the walk did not find the tree", walked)
	}
	if len(unseen) > 0 {
		t.Fatalf("Expand or ViewerSocket can be set where the walk cannot count it:\n%s", strings.Join(unseen, "\n"))
	}
	want := map[use]int{
		{"internal/api/ws_viewer.go", "Expand", "set"}:        1, // Submission{..., Expand: m.Expand}
		{"internal/api/ws_viewer.go", "Expand", "read"}:       1, // m.Expand, the submit's word
		{"internal/api/ws_viewer.go", "ViewerSocket", "set"}:  1, // AttachOptions{..., ViewerSocket: true}
		{"internal/session/local.go", "ViewerSocket", "read"}: 1, // AttachWith keeps it on the subscription
		{"internal/session/protocol.go", "Expand", "read"}:    2, // sub.Expand judged; the entry's Expanded from the message
		{"internal/session/protocol.go", "Expand", "set"}:     1, // q.msg.Expand = true, the one line
		{"internal/agentproto/claude.go", "Expand", "read"}:   1, // Send leaves client_composed out
		{"internal/hostagent/peer.go", "Expand", "set"}:       1, // passed on as it came: the session refuses it there
		{"internal/hostagent/peer.go", "Expand", "read"}:      1, // m.Expand, never dropped
		{"internal/session/submit.go", "Expand", "read"}:      1, // a terminal refuses it
	}
	if !maps.Equal(count, want) {
		var got []string
		for _, u := range slices.SortedFunc(maps.Keys(count), func(a, b use) int {
			return strings.Compare(a.file+a.name+a.how, b.file+b.name+b.how)
		}) {
			got = append(got, u.file+" "+u.name+" "+u.how+" x"+strconv.Itoa(count[u]))
		}
		t.Fatalf("Expand and ViewerSocket are set or read outside their places, or not where they must be:\n%s", strings.Join(got, "\n"))
	}
}

// typeName is the last name of a composite literal's type (session.X, X).
func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}
