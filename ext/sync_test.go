package ext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A decision's request part (docs/plans/sync-seams.md, the request part):
// the target's identifiers with the request, hashed as canonical JSON
// (keys sorted, no space, no HTML escaping); a request kept back is absent
// from the document and from the part, the mark said, and the hash is over
// exactly that; a document with both, or neither, or another mark has no
// part.
func TestAKeptRequestIsLeftOutOfThePart(t *testing.T) {
	rec := 1
	doc := DecisionDoc{ID: "dr-7", Run: "r2", OpenedIn: "r1", Origin: DecisionOriginAgent, Type: "approval", Task: "build", Attempt: 2,
		Request: &DecisionDocRequest{Question: "Ship <it>?", Options: []DecisionDocOption{{Label: "Yes"}, {Label: "No", Tradeoffs: "wait & see"}},
			Recommendation: &rec}}
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

	part, err := doc.RequestPart()
	if err != nil {
		t.Fatal(err)
	}
	got, err := part.SHA256()
	want := `{"attempt":2,"decision":"dr-7","openedIn":"r1","origin":"agent","request":{"options":[{"label":"Yes"},{"label":"No","tradeoffs":"wait & see"}],"question":"Ship <it>?","recommendation":1},"task":"build","type":"approval"}`
	if err != nil || got != sum(want) {
		t.Fatalf("the part's hash %s %v, want the hash of %s", got, err, want)
	}

	doc.Request, doc.RequestKept = nil, DecisionRequestKeptOutput
	part, err = doc.RequestPart()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(part)
	if err != nil || strings.Contains(string(b), `"request"`) {
		t.Fatalf("a kept request's part: %s %v", b, err)
	}
	got, err = part.SHA256()
	want = `{"attempt":2,"decision":"dr-7","openedIn":"r1","origin":"agent","task":"build","type":"approval"}`
	if err != nil || got != sum(want) {
		t.Fatalf("a kept request's hash %s %v, want the hash of %s", got, err, want)
	}
	b, err = json.Marshal(doc)
	if err != nil || strings.Contains(string(b), `"request"`) || !strings.Contains(string(b), `"requestKept":"output"`) || !strings.Contains(string(b), `"requestSHA256"`) {
		t.Fatalf("a kept request's document: %s %v", b, err)
	}

	for _, bad := range []DecisionDoc{
		{ID: "dr-8", Request: &DecisionDocRequest{Question: "Q"}, RequestKept: DecisionRequestKeptOutput},
		{ID: "dr-9"},
		{ID: "dr-10", RequestKept: "size"},
	} {
		if _, err := bad.RequestPart(); !errors.Is(err, ErrRefused) {
			t.Errorf("%s's part: %v", bad.ID, err)
		}
	}
}

// A row from another replica within its bounds is valid; each bound
// broken is refused.
func TestDispatchChangeValid(t *testing.T) {
	good := DispatchChange{Entity: "ticket", EntityID: "0123456789abcdef", Field: "title", Op: "set", Value: "Title", Actor: "admin", By: "Ada",
		OpID: "op-1", N: 0, Origin: "a1", Refs: []string{"ticket:0123456789abcdef", "label:fedcba9876543210"}}
	if err := good.Valid(); err != nil {
		t.Fatalf("a good row: %v", err)
	}
	for name, mutate := range map[string]func(*DispatchChange){
		"no entity":        func(c *DispatchChange) { c.Entity = "" },
		"a bad op id":      func(c *DispatchChange) { c.OpID = "op 1" },
		"no origin":        func(c *DispatchChange) { c.Origin = "" },
		"an origin's case": func(c *DispatchChange) { c.Origin = "A1" },
		"a recovery's tag": func(c *DispatchChange) { c.RecoveredBy = "too-long-tag" },
		"a long actor":     func(c *DispatchChange) { c.Actor = strings.Repeat("a", MaxDispatchActor+1) },
		"a qualified actor": func(c *DispatchChange) {
			c.Actor = "@a1/admin"
		},
		"a long by":          func(c *DispatchChange) { c.By = strings.Repeat("é", MaxDispatchBy+1) },
		"a long value":       func(c *DispatchChange) { c.Value = strings.Repeat("v", MaxDispatchValue+1) },
		"a long prior":       func(c *DispatchChange) { c.Prior = strings.Repeat("v", MaxDispatchValue+1) },
		"too many refs":      func(c *DispatchChange) { c.Refs = []string{"a:1", "a:2", "a:3", "a:4"} },
		"a ref of no kind":   func(c *DispatchChange) { c.Refs = []string{"0123"} },
		"a control in an id": func(c *DispatchChange) { c.EntityID = "abc\x07" },
		"a bad view digest":  func(c *DispatchChange) { c.ViewDigest = "abc" },
		"a negative place":   func(c *DispatchChange) { c.N = -1 },
	} {
		c := good
		c.Refs = append([]string(nil), good.Refs...)
		mutate(&c)
		if err := c.Valid(); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !ValidTag("a1") || ValidTag("") || ValidTag("abcdefghi") || ValidTag("a-1") {
		t.Fatal("ValidTag")
	}
}
