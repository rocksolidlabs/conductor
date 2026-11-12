package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Walks of the package's own source (its tests left out), as the
// repository's other walks read it (trackers_walk_test.go): what decodes a
// crew's member, and what decides a launched session's adapter.

// apiSource parses the package's source files, the tests left out, and
// any extra files given as source text (a walk's own fixtures).
func apiSource(t *testing.T, extra ...string) []*ast.File {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	for i, src := range extra {
		f, err := parser.ParseFile(fset, "fixture"+itoa(i)+".go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

// declName is a function's name as the walks list it: Type.Name for a
// method.
func declName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	typ := fd.Recv.List[0].Type
	if s, ok := typ.(*ast.StarExpr); ok {
		typ = s.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// funcs is every function of files, by declName, with a function that
// fixtures declare again replacing the package's own.
func funcs(files []*ast.File) map[string]*ast.FuncDecl {
	out := map[string]*ast.FuncDecl{}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				out[declName(fd)] = fd
			}
		}
	}
	return out
}

// mentionsMember says the type e holds a crew's member: crew.Member, or
// crew.Crew, which holds them, through a field, a slice, a pointer or a
// map's value, or a type of the package's that does (holds).
func mentionsMember(e ast.Expr, holds map[string]bool) bool {
	switch e := e.(type) {
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		return ok && pkg.Name == "crew" && (e.Sel.Name == "Member" || e.Sel.Name == "Crew")
	case *ast.Ident:
		return holds[e.Name]
	case *ast.StarExpr:
		return mentionsMember(e.X, holds)
	case *ast.ArrayType:
		return mentionsMember(e.Elt, holds)
	case *ast.MapType:
		return mentionsMember(e.Value, holds)
	case *ast.StructType:
		return slices.ContainsFunc(e.Fields.List, func(f *ast.Field) bool { return mentionsMember(f.Type, holds) })
	}
	return false
}

// memberDecoders is every function of files that decodes bytes into a
// value holding a crew's member, in the spellings it reads: a call of
// json.Unmarshal, store's decoders, a Decoder's Decode, the package's
// decodeJSON, decodeJSONLimit or decodeOptionalJSON (a call of
// decodeAgentMembers goes through the one decoder, and is not one), whose
// target is a variable (declared with its type, or given a composite
// literal or new(T), with or without a declared type), a parameter or a
// field of a type that holds one (a target of no named type, such as a
// parameter of any, counts when a parameter of the function holds one).
// It is an early signal for those spellings, not a proof that no other
// exists: each route's own test holds the rule.
func memberDecoders(files []*ast.File) []string {
	specs := map[string]ast.Expr{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				specs[ts.Name.Name] = ts.Type
			}
			return true
		})
	}
	holds := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for name, typ := range specs {
			if !holds[name] && mentionsMember(typ, holds) {
				holds[name], changed = true, true
			}
		}
	}
	// fieldType is the type of field name of the package's struct type
	// typ (an embedded field by its type's name).
	fieldType := func(typ ast.Expr, name string) ast.Expr {
		if s, ok := typ.(*ast.StarExpr); ok {
			typ = s.X
		}
		id, ok := typ.(*ast.Ident)
		if !ok {
			return nil
		}
		st, ok := specs[id.Name].(*ast.StructType)
		if !ok {
			return nil
		}
		for _, f := range st.Fields.List {
			for _, n := range f.Names {
				if n.Name == name {
					return f.Type
				}
			}
			if len(f.Names) == 0 {
				if sel, ok := f.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
					return f.Type
				}
				if id, ok := f.Type.(*ast.Ident); ok && id.Name == name {
					return f.Type
				}
			}
		}
		return nil
	}
	var out []string
	for name, fd := range funcs(files) {
		vars := map[string]ast.Expr{}
		params := false
		for _, l := range []*ast.FieldList{fd.Recv, fd.Type.Params} {
			if l == nil {
				continue
			}
			for _, f := range l.List {
				for _, n := range f.Names {
					vars[n.Name] = f.Type
				}
				params = params || mentionsMember(f.Type, holds)
			}
		}
		// valueType is the type a variable's value gives it: a composite
		// literal's, its address taken or not, or new(T)'s T.
		valueType := func(v ast.Expr) ast.Expr {
			if u, ok := v.(*ast.UnaryExpr); ok && u.Op == token.AND {
				v = u.X
			}
			switch v := v.(type) {
			case *ast.CompositeLit:
				return v.Type
			case *ast.CallExpr:
				if fn, ok := v.Fun.(*ast.Ident); ok && fn.Name == "new" && len(v.Args) == 1 {
					return v.Args[0]
				}
			}
			return nil
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ValueSpec:
				for i, id := range n.Names {
					switch {
					case n.Type != nil:
						vars[id.Name] = n.Type
					case i < len(n.Values) && valueType(n.Values[i]) != nil:
						vars[id.Name] = valueType(n.Values[i])
					}
				}
			case *ast.AssignStmt:
				for i, rhs := range n.Rhs {
					if typ := valueType(rhs); typ != nil && i < len(n.Lhs) {
						if id, ok := n.Lhs[i].(*ast.Ident); ok {
							vars[id.Name] = typ
						}
					}
				}
			}
			return true
		})
		found := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || found {
				return !found
			}
			target := decodeTarget(c)
			if u, ok := target.(*ast.UnaryExpr); ok && u.Op == token.AND {
				target = u.X
			}
			var typ ast.Expr
			switch x := target.(type) {
			case *ast.Ident:
				typ = vars[x.Name]
			case *ast.SelectorExpr:
				if base, ok := x.X.(*ast.Ident); ok {
					typ = fieldType(vars[base.Name], x.Sel.Name)
				}
			case nil:
				return true
			}
			_, named := typ.(*ast.Ident)
			_, sel := typ.(*ast.SelectorExpr)
			_, star := typ.(*ast.StarExpr)
			_, arr := typ.(*ast.ArrayType)
			found = mentionsMember(typ, holds) || (!named && !sel && !star && !arr && params)
			return !found
		})
		if found {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// decodeTarget is the argument a decoding call decodes into, nil for a
// call that decodes nothing.
func decodeTarget(c *ast.CallExpr) ast.Expr {
	arg := func(i int) ast.Expr {
		if i < len(c.Args) {
			return c.Args[i]
		}
		return nil
	}
	switch fn := c.Fun.(type) {
	case *ast.SelectorExpr:
		pkg, _ := fn.X.(*ast.Ident)
		switch {
		case pkg != nil && pkg.Name == "json" && fn.Sel.Name == "Unmarshal",
			pkg != nil && pkg.Name == "store" && strings.HasPrefix(fn.Sel.Name, "Decode"):
			return arg(1)
		case fn.Sel.Name == "Decode":
			return arg(0)
		}
	case *ast.Ident:
		switch fn.Name {
		case "decodeJSON", "decodeJSONLimit", "decodeOptionalJSON":
			return arg(2)
		}
	}
	return nil
}

// adminOnly is the functions of files only the admin's token reaches: a
// handler every route of which is registered behind the admin's own gate
// (requireAdmin, or launching, which holds it), and a function every use
// of which, its calls and its values, is inside such a function. An
// exported method is never one: another package may call it through an
// interface (Brakeman's facts), out of this walk's sight.
func adminOnly(files []*ast.File) map[string]bool {
	all := funcs(files)
	short := map[string][]string{}
	// uses[f] are the functions that name f (its calls and its values),
	// "route:admin" or "route:open" for a route registration of it, and
	// "route:open" for an exported method.
	uses := map[string][]string{}
	for name, fd := range all {
		short[fd.Name.Name] = append(short[fd.Name.Name], name)
		if fd.Recv != nil && ast.IsExported(fd.Name.Name) {
			uses[name] = append(uses[name], "route:open")
		}
	}
	for name, fd := range all {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "HandleFunc" && len(c.Args) == 2 {
					gate, h := "route:open", c.Args[1]
					for {
						call, ok := h.(*ast.CallExpr)
						if !ok || len(call.Args) == 0 {
							break
						}
						if w, ok := call.Fun.(*ast.SelectorExpr); ok && (w.Sel.Name == "requireAdmin" || w.Sel.Name == "launching") {
							gate = "route:admin"
						}
						h = call.Args[0]
					}
					if hs, ok := h.(*ast.SelectorExpr); ok {
						for _, f := range short[hs.Sel.Name] {
							uses[f] = append(uses[f], gate)
						}
						return false
					}
				}
			}
			var ref string
			switch x := n.(type) {
			case *ast.SelectorExpr:
				ref = x.Sel.Name
			case *ast.Ident:
				ref = x.Name
			}
			for _, f := range short[ref] {
				if f != name {
					uses[f] = append(uses[f], name)
				}
			}
			return true
		})
	}
	memo := map[string]int{} // 1 admin only, 2 not, 3 being judged
	var judge func(string) bool
	judge = func(f string) bool {
		switch memo[f] {
		case 1:
			return true
		case 2, 3:
			return false
		}
		memo[f] = 3
		ok := len(uses[f]) > 0
		for _, u := range uses[f] {
			ok = ok && (u == "route:admin" || (u != "route:open" && judge(u)))
		}
		memo[f] = 2
		if ok {
			memo[f] = 1
		}
		return ok
	}
	out := map[string]bool{}
	for f := range all {
		if judge(f) {
			out[f] = true
		}
	}
	return out
}

// sessionView says e is a session's view of itself: X.Launched() or
// X.Info(), which name the agent id it was launched from.
func sessionView(e ast.Expr) bool {
	c, ok := e.(*ast.CallExpr)
	if !ok || len(c.Args) != 0 {
		return false
	}
	sel, ok := c.Fun.(*ast.SelectorExpr)
	return ok && (sel.Sel.Name == "Launched" || sel.Sel.Name == "Info")
}

// adapterByCatalog is every function of files that decides by the
// catalog's entry of a launched session's agent id which adapter that
// session runs under, in the spelling it reads: a lookup (X.Get) of the
// AgentID of a session's Launched() or Info() (called there, held in a
// variable, or a parameter of type session.Launched or session.Info),
// assigned to a variable whose Adapter the function reads. It is an early
// signal for that spelling, not a proof that no other exists: each route's
// own test holds the rule.
func adapterByCatalog(files []*ast.File) []string {
	var out []string
	for name, fd := range funcs(files) {
		views := map[string]bool{}
		for _, f := range fd.Type.Params.List {
			if sel, ok := f.Type.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Launched" || sel.Sel.Name == "Info") {
				for _, n := range f.Names {
					views[n.Name] = true
				}
			}
		}
		entries := map[string]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, rhs := range as.Rhs {
				id, _ := as.Lhs[min(i, len(as.Lhs)-1)].(*ast.Ident)
				if id == nil {
					continue
				}
				if sessionView(rhs) {
					views[id.Name] = true
				}
				c, ok := rhs.(*ast.CallExpr)
				if !ok || len(c.Args) != 1 {
					continue
				}
				fn, ok := c.Fun.(*ast.SelectorExpr)
				arg, isSel := c.Args[0].(*ast.SelectorExpr)
				if !ok || fn.Sel.Name != "Get" || !isSel || arg.Sel.Name != "AgentID" {
					continue
				}
				if v, ok := arg.X.(*ast.Ident); (ok && views[v.Name]) || sessionView(arg.X) {
					entries[id.Name] = true
				}
			}
			return true
		})
		found := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Adapter" {
				if id, ok := sel.X.(*ast.Ident); ok && entries[id.Name] {
					found = true
				}
			}
			return !found
		})
		if found {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// A launched session's adapter is the one it was started as
// (Launched.Adapter), everywhere: no function decides it by the catalog's
// entry of the session's agent id. The clone's choice as it was made
// before, from the source's entry, is found.
func TestLaunchedAdapterNotFromTheCatalog(t *testing.T) {
	if got := adapterByCatalog(apiSource(t)); len(got) > 0 {
		t.Errorf("a launched session's adapter decided by its catalog entry: %v", got)
	}
	const before = `package api
func (s *Server) handleCloneSession(w http.ResponseWriter, r *http.Request) {
	launched := local.Launched()
	src, _ := s.Catalog().Get(launched.AgentID)
	agent, known := s.Catalog().Get(next.AgentID)
	if known && src.Adapter == agent.Adapter {
		next.Account = launched.Account.ID
	}
}
`
	if got := adapterByCatalog(apiSource(t, before)); !slices.Contains(got, "Server.handleCloneSession") {
		t.Errorf("the clone deciding by the source's entry is not found: %v", got)
	}
}

// keptDecoders decode a crew's members from what the server itself wrote
// and keeps (the ledger's crew as launched, a run's record, a plan
// revision approved), never from bytes an agent sends: each with where it
// reads from.
var keptDecoders = map[string]string{
	"Server.candidateVerified":   "the ledger's crew as launched",
	"Server.continuationWords":   "the ledger's crew as launched",
	"Server.handleRunCandidates": "the ledger's crew as launched",
	"Server.launchPlanned":       "an approved plan revision, made from a saved crew",
	"Server.restoreLaunchPeople": "an approved plan revision, made from a saved crew",
	"availability.runFolder":     "the ledger's crew as launched",
	"crewCwd":                    "the ledger's crew as launched",
	"fitRequest":                 "the ledger's crew as launched",
	"locator.repoOf":             "the ledger's crew as launched",
	"refResolver.candidate":      "the ledger's crew as launched",
	"rowAgents":                  "the ledger's crew as launched",
	"runPlaceOf":                 "the ledger's crew as launched",
}

// An agent's members are decoded in one place: every function of the
// package that decodes a crew's member from bytes is decodeAgentMembers,
// which refuses a member naming an account or a binding, or one only the
// admin's token reaches, or one that reads what the server keeps
// (keptDecoders). A decoder added on a path an agent reaches, in a
// spelling the walk reads (memberDecoders), fails it: so does Brakeman's
// draft check as it was before it went through decodeAgentMembers, its
// variable declared with its type, made by new(T) or given a literal.
func TestAgentMembersDecodedInOnePlace(t *testing.T) {
	stray := func(files []*ast.File) []string {
		admin := adminOnly(files)
		var out []string
		for _, f := range memberDecoders(files) {
			if _, kept := keptDecoders[f]; f != "decodeAgentMembers" && !kept && !admin[f] {
				out = append(out, f)
			}
		}
		return out
	}
	if got := stray(apiSource(t)); len(got) > 0 {
		t.Errorf("decoders of a crew's member an agent's path reaches, not through decodeAgentMembers: %v", got)
	}
	for f := range keptDecoders {
		if !slices.Contains(memberDecoders(apiSource(t)), f) {
			t.Errorf("%s is listed as a decoder of what the server keeps and decodes no member", f)
		}
	}
	// Brakeman's draft check as it decoded the draft before, and the same
	// decoder with its variable made by new(T) or declared with a value and
	// no type: each a stray.
	for spelling, decl := range map[string]string{
		"var in T":      "var in crewInput\n\tif err := store.DecodeStrict(body, &in); err != nil {",
		"in := new(T)":  "in := new(crewInput)\n\tif err := store.DecodeStrict(body, in); err != nil {",
		"var in = T{}":  "var in = crewInput{}\n\tif err := store.DecodeStrict(body, &in); err != nil {",
		"var in = &T{}": "var in = &crewInput{}\n\tif err := store.DecodeStrict(body, in); err != nil {",
	} {
		before := `package api
func (f brakemanFacts) CheckCrew(body json.RawMessage) error {
	` + decl + `
		return err
	}
	c := in.crew()
	if err := c.ValidateSave(); err != nil {
		return err
	}
	return c.CheckAgents(f.s.Catalog())
}
`
		if got := stray(apiSource(t, before)); !slices.Contains(got, "brakemanFacts.CheckCrew") {
			t.Errorf("the draft check decoding its own crew (%s) is not found: %v", spelling, got)
		}
	}
}
