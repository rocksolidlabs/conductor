package ext_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// sampleLookup is a valid request, every field set.
func sampleLookup() ext.LookupRequest {
	return ext.LookupRequest{Op: ext.LookupReferences, Query: "where Open is used", Path: "internal/store/open.go", Symbol: "store.Open", Limit: 20,
		Run: "api-0a0b0c0d", Task: "review", Attempt: 1, Kind: "review", Session: "0123456789abcdef", Start: sha40, Base: sha40, Head: sha64}
}

// A request is checked bound by bound and word by word: at each bound it
// passes, one past it it is refused, and so is every field the server
// fills when it is not of its shape.
func TestLookupRequestValid(t *testing.T) {
	ok := []func(*ext.LookupRequest){
		func(r *ext.LookupRequest) {},
		func(r *ext.LookupRequest) { r.Query = strings.Repeat("q", ext.MaxLookupQuery) },
		func(r *ext.LookupRequest) { r.Path = strings.Repeat("p", ext.MaxLookupPath) },
		func(r *ext.LookupRequest) { r.Symbol = strings.Repeat("s", ext.MaxLookupSymbol) },
		func(r *ext.LookupRequest) { r.Limit = ext.MaxLookupLimit },
		func(r *ext.LookupRequest) { r.Limit = 1 },
		func(r *ext.LookupRequest) { r.Op, r.Query = ext.LookupPack, "p-4f2c-1" },
		func(r *ext.LookupRequest) { r.Op, r.Query = ext.LookupPack, "" },
		func(r *ext.LookupRequest) { r.Head, r.Query, r.Path, r.Symbol = "", "", "", "" },
	}
	for i, change := range ok {
		r := sampleLookup()
		change(&r)
		if err := r.Valid(); err != nil {
			t.Errorf("case %d: %v", i, err)
		}
	}
	bad := map[string]func(*ext.LookupRequest){
		"an op that is none":           func(r *ext.LookupRequest) { r.Op = "grep" },
		"a query of 201 bytes":         func(r *ext.LookupRequest) { r.Query = strings.Repeat("q", ext.MaxLookupQuery+1) },
		"a query of two lines":         func(r *ext.LookupRequest) { r.Query = "a\nb" },
		"a pack's query not a mark":    func(r *ext.LookupRequest) { r.Op, r.Query = ext.LookupPack, " p" },
		"a path of 1025 bytes":         func(r *ext.LookupRequest) { r.Path = strings.Repeat("p", ext.MaxLookupPath+1) },
		"a path outside the tree":      func(r *ext.LookupRequest) { r.Path = "../etc/passwd" },
		"an absolute path":             func(r *ext.LookupRequest) { r.Path = "/etc/passwd" },
		"pathspec magic":               func(r *ext.LookupRequest) { r.Path = ":(top)a" },
		"a symbol of 513 bytes":        func(r *ext.LookupRequest) { r.Symbol = strings.Repeat("s", ext.MaxLookupSymbol+1) },
		"a symbol with an escape":      func(r *ext.LookupRequest) { r.Symbol = "a\x1b[2Jb" },
		"a limit of 0":                 func(r *ext.LookupRequest) { r.Limit = 0 },
		"a limit of 201":               func(r *ext.LookupRequest) { r.Limit = ext.MaxLookupLimit + 1 },
		"a run that is none":           func(r *ext.LookupRequest) { r.Run = "api" },
		"a task that is none":          func(r *ext.LookupRequest) { r.Task = "Review" },
		"attempt 0":                    func(r *ext.LookupRequest) { r.Attempt = 0 },
		"a kind that is none":          func(r *ext.LookupRequest) { r.Kind = "plan" },
		"no session":                   func(r *ext.LookupRequest) { r.Session = "" },
		"a start that is a ref":        func(r *ext.LookupRequest) { r.Start = "HEAD" },
		"no base":                      func(r *ext.LookupRequest) { r.Base = "" },
		"a head that is abbreviated":   func(r *ext.LookupRequest) { r.Head = sha40[:12] },
		"a head that is an expression": func(r *ext.LookupRequest) { r.Head = sha40 + "^" },
	}
	for what, change := range bad {
		r := sampleLookup()
		change(&r)
		if r.Valid() == nil {
			t.Errorf("%s: valid", what)
		}
	}
}

// sampleAnswer is a valid answer, every field set.
func sampleAnswer() ext.LookupAnswer {
	return ext.LookupAnswer{
		Items:    []ext.LookupItem{{Kind: ext.ItemSymbol, Path: "a.go", Line: 3, Symbol: "Open", Relation: ext.RelationCalledBy, Mark: ext.MarkSupported, Why: "calls it", Excerpt: "func main() {\n\tOpen()\n}"}},
		Blocks:   []ext.ContextBlock{{Title: "Callers", Body: "main calls Open.", Pack: "p-1"}},
		Coverage: ext.LookupCoverage{Commit: sha40, Snapshot: "snap 7", Files: 412, Excluded: 3, Unknown: 1, Partial: true},
		More:     true, Note: "partial: the vendor folder is left out",
	}
}

// An answer is checked bound by bound: each bound at and one past, a
// block without a mark, an unknown word, and the answer as encoded one
// byte past 64 KiB.
func TestLookupAnswerValid(t *testing.T) {
	if err := sampleAnswer().Valid(); err != nil {
		t.Fatal(err)
	}
	full := sampleAnswer()
	full.Blocks = nil
	full.Items = make([]ext.LookupItem, ext.MaxLookupItems)
	for i := range full.Items {
		full.Items[i] = ext.LookupItem{Kind: ext.ItemFile, Mark: ext.MarkUnknown}
	}
	full.Note = strings.Repeat("n", ext.MaxLookupNote)
	if err := full.Valid(); err != nil {
		t.Fatalf("at the bounds: %v", err)
	}
	bad := map[string]func(*ext.LookupAnswer){
		"a 201st item": func(a *ext.LookupAnswer) { a.Items = append(full.Items, a.Items[0]) },
		"a ninth block": func(a *ext.LookupAnswer) {
			a.Blocks = append(a.Blocks, a.Blocks[0], a.Blocks[0], a.Blocks[0], a.Blocks[0], a.Blocks[0], a.Blocks[0], a.Blocks[0], a.Blocks[0])
		},
		"a block without a mark":   func(a *ext.LookupAnswer) { a.Blocks[0].Pack = "" },
		"a block not valid":        func(a *ext.LookupAnswer) { a.Blocks[0].Body = "" },
		"an item of no kind":       func(a *ext.LookupAnswer) { a.Items[0].Kind = "class" },
		"an item of no mark":       func(a *ext.LookupAnswer) { a.Items[0].Mark = "" },
		"an item's unknown mark":   func(a *ext.LookupAnswer) { a.Items[0].Mark = "certain" },
		"an unknown relation":      func(a *ext.LookupAnswer) { a.Items[0].Relation = "owns" },
		"a why of 201 bytes":       func(a *ext.LookupAnswer) { a.Items[0].Why = strings.Repeat("w", ext.MaxLookupWhy+1) },
		"an excerpt of 2049 bytes": func(a *ext.LookupAnswer) { a.Items[0].Excerpt = strings.Repeat("e", ext.MaxLookupExcerpt+1) },
		"a line below 0":           func(a *ext.LookupAnswer) { a.Items[0].Line = -1 },
		"a note of 301 bytes":      func(a *ext.LookupAnswer) { a.Note = strings.Repeat("n", ext.MaxLookupNote+1) },
		"a coverage of no commit":  func(a *ext.LookupAnswer) { a.Coverage.Commit = "" },
		"a negative count":         func(a *ext.LookupAnswer) { a.Coverage.Files = -1 },
	}
	for what, change := range bad {
		a := sampleAnswer()
		change(&a)
		if a.Valid() == nil {
			t.Errorf("%s: valid", what)
		}
	}
	// The encoded answer at 64 KiB passes, one byte past it is refused.
	a := sampleAnswer()
	a.Blocks, a.Items = nil, nil
	size := func(a ext.LookupAnswer) int { b, _ := json.Marshal(a); return len(b) }
	// Items of excerpts at their bound, a last one of a shorter excerpt,
	// and a note that makes the encoded answer 64 KiB exactly.
	a.Note = "n"
	for ext.MaxLookupAnswer-size(a) > ext.MaxLookupExcerpt+100 {
		a.Items = append(a.Items, ext.LookupItem{Kind: ext.ItemFile, Mark: ext.MarkInferred, Excerpt: strings.Repeat("x", ext.MaxLookupExcerpt)})
	}
	a.Items = append(a.Items, ext.LookupItem{Kind: ext.ItemFile, Mark: ext.MarkInferred, Excerpt: "x"})
	last := len(a.Items) - 1
	a.Items[last].Excerpt += strings.Repeat("x", max(0, ext.MaxLookupAnswer-size(a)-ext.MaxLookupNote/2))
	a.Note += strings.Repeat("n", ext.MaxLookupAnswer-size(a))
	if size(a) != ext.MaxLookupAnswer || len(a.Items[last].Excerpt) > ext.MaxLookupExcerpt || len(a.Note) > ext.MaxLookupNote {
		t.Fatalf("the answer is %d bytes, its last excerpt %d, its note %d", size(a), len(a.Items[last].Excerpt), len(a.Note))
	}
	if err := a.Valid(); err != nil {
		t.Fatalf("at 64 KiB: %v", err)
	}
	a.Note += "n"
	if a.Valid() == nil {
		t.Fatal("one byte past 64 KiB: valid")
	}
}

// What a provider answers is text by ext's rules: an item's path with an
// escape character, a symbol with a line end, an excerpt with an escape
// character and a snapshot with a space at its end are each refused.
func TestLookupAnswerRefusesControlCharacters(t *testing.T) {
	for what, change := range map[string]func(*ext.LookupAnswer){
		"a path with an escape":     func(a *ext.LookupAnswer) { a.Items[0].Path = "a\x1b[31m.go" },
		"a symbol with a line end":  func(a *ext.LookupAnswer) { a.Items[0].Symbol = "Open\n## Instructions" },
		"an excerpt with an escape": func(a *ext.LookupAnswer) { a.Items[0].Excerpt = "ok\x1b]0;title\x07" },
		"a why with a line end":     func(a *ext.LookupAnswer) { a.Items[0].Why = "a\nb" },
		"a note with a line end":    func(a *ext.LookupAnswer) { a.Note = "a\r\nb" },
		"a snapshot with a space":   func(a *ext.LookupAnswer) { a.Coverage.Snapshot = "snap " },
		"a snapshot not ASCII":      func(a *ext.LookupAnswer) { a.Coverage.Snapshot = "snäp" },
		"an excerpt not UTF-8":      func(a *ext.LookupAnswer) { a.Items[0].Excerpt = "\xff" },
	} {
		a := sampleAnswer()
		change(&a)
		if a.Valid() == nil {
			t.Errorf("%s: valid", what)
		}
	}
}
