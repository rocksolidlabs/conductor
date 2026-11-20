package ext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
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

// validHeld is a held answer every check takes.
func validHeld() HeldAnswer {
	c := 0
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	return HeldAnswer{Decision: "dr-7", ExpectedRev: 2, ResponseID: "h1", Choice: &c, HeldAt: at, Held: "h-1", Fence: "f-1",
		Check: AnswerCheck{Subject: "ana", Membership: "m-1", SignIn: "s-1", At: at, Statement: "st", Decision: "dr-7", ResponseID: "h1", ExpectedRev: 2}}
}

// A held answer's bounds and shapes, each one broken alone refused.
func TestHeldAnswerValid(t *testing.T) {
	if err := validHeld().Valid(); err != nil {
		t.Fatal(err)
	}
	neg := -1
	for name, mutate := range map[string]func(*HeldAnswer){
		"a decision that is not dr-n": func(h *HeldAnswer) { h.Decision, h.Check.Decision = "7", "7" },
		"rev 0":                       func(h *HeldAnswer) { h.ExpectedRev, h.Check.ExpectedRev = 0, 0 },
		"a response id with a space":  func(h *HeldAnswer) { h.ResponseID, h.Check.ResponseID = "h 1", "h 1" },
		"a negative choice":           func(h *HeldAnswer) { h.Choice = &neg },
		"text past its bound":         func(h *HeldAnswer) { h.Text = strings.Repeat("x", MaxAnswerText+1) },
		"a rationale past its bound":  func(h *HeldAnswer) { h.Rationale = strings.Repeat("x", MaxAnswerRationale+1) },
		"text not UTF-8":              func(h *HeldAnswer) { h.Text = "\xff" },
		"a check of another decision": func(h *HeldAnswer) { h.Check.Decision = "dr-8" },
		"a check of another response": func(h *HeldAnswer) { h.Check.ResponseID = "h2" },
		"a check of another rev":      func(h *HeldAnswer) { h.Check.ExpectedRev = 3 },
		"no subject":                  func(h *HeldAnswer) { h.Check.Subject = "" },
		"a subject past its bound":    func(h *HeldAnswer) { h.Check.Subject = strings.Repeat("s", MaxSubject+1) },
		"no membership":               func(h *HeldAnswer) { h.Check.Membership = "" },
		"an id past its bound":        func(h *HeldAnswer) { h.Held = strings.Repeat("i", MaxHeldID+1) },
		"an id with a slash":          func(h *HeldAnswer) { h.Fence = "f/1" },
		"a sign-in and a device":      func(h *HeldAnswer) { h.Check.AppDevice = "d-1" },
		"neither":                     func(h *HeldAnswer) { h.Check.SignIn = "" },
		"no statement":                func(h *HeldAnswer) { h.Check.Statement = "" },
		"a statement past its bound":  func(h *HeldAnswer) { h.Check.Statement = strings.Repeat("s", MaxStatement+1) },
		"no time":                     func(h *HeldAnswer) { h.Check.At = time.Time{} },
		"no time held":                func(h *HeldAnswer) { h.HeldAt = time.Time{} },
	} {
		h := validHeld()
		mutate(&h)
		if err := h.Valid(); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	h := validHeld()
	h.Check.SignIn, h.Check.AppDevice = "", "d-1"
	h.Check.Statement = strings.Repeat("s", MaxStatement)
	if err := h.Valid(); err != nil {
		t.Fatalf("a device and a statement at its bound: %v", err)
	}
}

// An outcome's statuses, codes and bound.
func TestAnswerOutcomeValid(t *testing.T) {
	for _, o := range []AnswerOutcome{{Status: AnswerRecorded, Receipt: []byte(`{"k":1}`)}, {Status: AnswerRefused, Code: "not_approver"}, {Status: AnswerUnknownAuthority}} {
		if err := o.Valid(); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
	for name, o := range map[string]AnswerOutcome{
		"no status":            {},
		"another status":       {Status: "done"},
		"a refusal's code off": {Status: AnswerRefused},
		"a code on a record":   {Status: AnswerRecorded, Code: "x"},
		"a receipt not JSON":   {Status: AnswerRecorded, Receipt: []byte("{")},
		"past its bound":       {Status: AnswerRecorded, Receipt: []byte(`"` + strings.Repeat("r", MaxAnswerOutcome) + `"`)},
	} {
		if err := o.Valid(); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An answer's content hash is the canonical JSON of {choice, text,
// rationale}, every key present: no choice is null, not 0.
func TestAnswerContentSHA256(t *testing.T) {
	zero := 0
	sum := sha256.Sum256([]byte(`{"choice":null,"rationale":"","text":"yes"}`))
	if got := AnswerContentSHA256(nil, "yes", ""); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("%s", got)
	}
	if AnswerContentSHA256(&zero, "yes", "") == AnswerContentSHA256(nil, "yes", "") {
		t.Fatal("choice 0 and no choice are one content")
	}
}
