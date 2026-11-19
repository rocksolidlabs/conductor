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
