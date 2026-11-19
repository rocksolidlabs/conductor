package dispatch

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// A revision proposed on version 8 has its view from its proposal: kept
// under its SHA-256 with the body's digest, named by the revision's
// view_digest, the view of its body exactly as the migration would make
// it, holding nothing of what stays on the machine; a view past its bound
// refuses the proposal and writes nothing.
func TestAProposedRevisionHasItsView(t *testing.T) {
	ctx := context.Background()
	s, tkID, key := planTicket(t)
	att := insertAttachment(t, s, tkID, 10)
	cm := comment(t, s, "view-comment", key, "Handed over.")
	body := testPlanBody("crew")
	body.Attachments = []string{att}
	body.Comments = []PlanComment{{ID: cm}}
	body.Decisions = []PlanDecision{{ID: "go", Question: "Go on?", Kind: DecisionApproval, Before: []string{"review"}}}
	id := propose(t, s, "view-propose", key, PlanRequest{Crew: "crew", Attachments: []string{att}, Comments: []string{cm}}, body)
	var stored, digest, viewDigest string
	if err := s.db.QueryRow("SELECT body, digest, view_digest FROM plans WHERE id = ?", id).Scan(&stored, &digest, &viewDigest); err != nil {
		t.Fatal(err)
	}
	if viewDigest == "" {
		t.Fatal("a revision proposed on version 8 has no view")
	}
	var view []byte
	var kept string
	if err := s.db.QueryRow("SELECT view, digest FROM plan_views WHERE view_digest = ?", viewDigest).Scan(&view, &kept); err != nil {
		t.Fatalf("its view: %v", err)
	}
	if sha256Hex(string(view)) != viewDigest || kept != digest {
		t.Fatalf("the view is named %s for %s, its SHA-256 %s, the body's digest %s", viewDigest, kept, sha256Hex(string(view)), digest)
	}
	made, ok, err := makePlanView(stored, digest, func(string) (string, error) { return "a-" + att, nil })
	if err != nil || !ok || string(view) != made {
		t.Fatalf("the view:\n got %s\nwant %s (%v %v)", view, made, ok, err)
	}
	for _, w := range []string{"/work/conductor", "--model", "--session-id", "\"argv\"", "\"command\"", "\"envNames\"", sha256Hex("A=1"), sha256Hex("selection of claude")} {
		if strings.Contains(string(view), w) {
			t.Errorf("the view holds %s: %s", w, view)
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM plans WHERE view_digest = ''"); n != 0 {
		t.Fatalf("%d revisions without a view", n)
	}

	old := maxPlanView
	maxPlanView = 100
	t.Cleanup(func() { maxPlanView = old })
	rc, err := s.ProposePlan(ctx, testOp("view-too-big"), key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	want(t, rc, err, StatusFailed, RefusalBound)
	if n := count(t, s, "SELECT COUNT(*) FROM plans"); n != 1 {
		t.Fatalf("%d revisions after the refusal", n)
	}
}

// viewFields is every field the view gives, by its type: the allow-list.
// A field added to a view type without being added here fails
// TestThePlanViewIsAnAllowList, and so does a body type put in the view's
// place.
var viewFields = map[string][]string{
	"revisionView":   {"v", "digest", "base", "crew", "agents", "references", "attachments", "comments", "constraints", "decisions", "note"},
	"viewCrew":       {"id", "name", "tasks"},
	"viewTask":       {"id", "kind", "member", "agent", "instructions", "needs"},
	"viewAttachment": {"id", "name"},
	"viewReference":  {"path", "start", "end", "note", "object", "kind"},
	"viewComment":    {"id", "sha256"},
	"viewDecision":   {"id", "question", "kind", "before", "for"},
}

// The view is an allow-list all the way down: every struct it holds is a
// view type of planview.go with exactly the fields viewFields names, never
// a body type, a map, an any or raw bytes; and a field the body gains in any of its objects (the
// body, its crew, a member, a task, a check, an agent, a reference, a
// comment, a decision, the manifest file) never reaches the view.
func TestThePlanViewIsAnAllowList(t *testing.T) {
	seen := map[string]bool{}
	var walk func(rt reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Slice || rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		switch rt.Kind() {
		case reflect.Map, reflect.Interface:
			// A map or an any copies whatever it is given.
			t.Errorf("the view holds %s, which names no fields", rt)
			return
		case reflect.Uint8:
			t.Errorf("the view holds raw bytes (%s)", rt)
			return
		}
		if rt.Kind() != reflect.Struct || seen[rt.Name()] {
			return
		}
		seen[rt.Name()] = true
		want, ok := viewFields[rt.Name()]
		if !ok {
			t.Errorf("the view holds %s, which is not a view type", rt)
			return
		}
		var got []string
		for i := range rt.NumField() {
			f := rt.Field(i)
			got = append(got, strings.Split(f.Tag.Get("json"), ",")[0])
			walk(f.Type)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s's fields %v, the allow-list %v", rt.Name(), got, want)
		}
	}
	walk(reflect.TypeOf(revisionView{}))
	if len(seen) != len(viewFields) {
		t.Errorf("the view's types %v, the allow-list's %d", seen, len(viewFields))
	}

	// A marker in a new field of every object of a body never leaks.
	body := testPlanBody("crew")
	body.Comments = []PlanComment{{ID: "c1", SHA256: sha256Hex("c")}}
	body.Decisions = []PlanDecision{{ID: "go", Question: "Go on?", Kind: DecisionApproval, Before: []string{"review"}, For: "role:lead"}}
	body.Attachments = []string{"a1"}
	body.ManifestFile = &PlanManifestFile{Path: "m.json", Blob: strings.Repeat("12", 20), Manifest: json.RawMessage(`{}`)}
	encoded, err := encodeValue(body)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal([]byte(encoded), &doc); err != nil {
		t.Fatal(err)
	}
	marked := 0
	var mark func(v any)
	mark = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for _, c := range v {
				mark(c)
			}
			v["addedLater"] = "LEAKED-MARKER"
			marked++
		case []any:
			for _, c := range v {
				mark(c)
			}
		}
	}
	mark(doc)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if marked < 12 {
		t.Fatalf("%d objects marked", marked)
	}
	view, ok, err := makePlanView(string(raw), sha256Hex(string(raw)), func(string) (string, error) { return "notes.txt", nil })
	if err != nil || !ok {
		t.Fatalf("the view: %v %v", ok, err)
	}
	if strings.Contains(view, "LEAKED-MARKER") || strings.Contains(view, "addedLater") {
		t.Fatalf("a field the body gained reached the view: %s", view)
	}
	if !strings.Contains(view, `"for":"role:lead"`) || !strings.Contains(view, `"name":"notes.txt"`) || !strings.Contains(view, `"note":"the intro"`) {
		t.Fatalf("the view lost what it gives: %s", view)
	}
}
