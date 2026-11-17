package brakeman

import (
	"encoding/json"
	"strings"
	"testing"
)

// compareBriefJSON is a compare brief DecodeBrief takes: a fact citing the
// first candidate, then the table over columns with the five rows and an
// extra one, its evidence each candidate and a receipt.
func compareBriefJSON(columns ...string) map[string]any {
	cells := func(s string) []any {
		out := make([]any, len(columns))
		for i := range out {
			out[i] = s
		}
		return out
	}
	var rows []any
	for _, l := range append(append([]string{}, compareRows...), "Notes") {
		rows = append(rows, map[string]any{"label": l, "cells": cells(l + " here")})
	}
	var evidence, cols []any
	for _, c := range columns {
		cols = append(cols, c)
		evidence = append(evidence, "candidate:"+runA+"/"+c+"/1")
	}
	evidence = append(evidence, "check:"+runA+"/"+columns[0]+"/1/tests/1")
	return map[string]any{"schema": BriefSchema, "title": "Two approaches", "summary": "A reads simpler; B passed more checks.",
		"items": []any{
			map[string]any{"kind": ItemFact, "text": "approach-a passed its tests.", "evidence": []any{"candidate:" + runA + "/" + columns[0] + "/1"}},
			map[string]any{"kind": ItemComparison, "title": "The candidates", "columns": cols, "rows": rows, "evidence": evidence},
		}}
}

func decodeCompare(t *testing.T, v map[string]any) Brief {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	b, err := DecodeBrief(raw, allowAll, goodFacts())
	if err != nil {
		t.Fatalf("the brief: %v", err)
	}
	return b
}

// item is the brief's i-th item, as a JSON value to change.
func itemOf(v map[string]any, i int) map[string]any { return v["items"].([]any)[i].(map[string]any) }

var compareCommands = []string{"task_retry", "task_accept", "task_cancel", "task_recheck", "review_submit", "decision_respond"}

// TestACompareBriefNeedsItsTable: a compare request's brief holds one
// comparison/1 whose columns are the candidates in the block's order and
// whose rows hold the five labels, its evidence citing the candidates; a
// decision card and a proposal of a lane's command are refused; and the
// kind table's words say so.
func TestACompareBriefNeedsItsTable(t *testing.T) {
	shape := CompareShape{Columns: []string{"approach-a", "approach-b", "approach-c"}, Runs: []string{"team-0000000b", runA}, Commands: compareCommands}
	good := func() map[string]any { return compareBriefJSON(shape.Columns...) }
	if err := CheckCompareBrief(decodeCompare(t, good()), shape); err != nil {
		t.Fatalf("a whole compare brief: %v", err)
	}
	// The labels are matched without regard to case.
	v := good()
	itemOf(v, 1)["rows"].([]any)[0].(map[string]any)["label"] = "behaviour"
	if err := CheckCompareBrief(decodeCompare(t, v), shape); err != nil {
		t.Errorf("a label in lower case: %v", err)
	}

	// The columns: in the block's order, every candidate, no other.
	for _, cols := range [][]string{
		{"approach-b", "approach-a", "approach-c"},
		{"approach-a", "approach-b"},
		{"approach-a", "approach-b", "approach-c", "approach-d"},
		{"approach-a", "approach-b", "review-a"},
	} {
		inv := wantInvalid(t, CheckCompareBrief(decodeCompare(t, compareBriefJSON(cols...)), shape), "items[1].columns")
		if !strings.Contains(inv.Why, "approach-a, approach-b, approach-c") {
			t.Errorf("columns %v: %s", cols, inv.Why)
		}
	}

	// Each of the five rows missing in turn.
	for i, label := range compareRows {
		v := good()
		rows := itemOf(v, 1)["rows"].([]any)
		itemOf(v, 1)["rows"] = append(append([]any{}, rows[:i]...), rows[i+1:]...)
		inv := wantInvalid(t, CheckCompareBrief(decodeCompare(t, v), shape), "items[1].rows")
		if !strings.Contains(inv.Why, "no row labelled "+label) {
			t.Errorf("%s missing: %s", label, inv.Why)
		}
	}

	// No table, two tables.
	v = good()
	v["items"] = v["items"].([]any)[:1]
	wantInvalid(t, CheckCompareBrief(decodeCompare(t, v), shape), "items")
	v = good()
	v["items"] = append(v["items"].([]any), itemOf(good(), 1))
	wantInvalid(t, CheckCompareBrief(decodeCompare(t, v), shape), "items[2]")

	// The evidence cites a candidate, and names only the columns' tasks.
	v = good()
	itemOf(v, 1)["evidence"] = []any{"check:" + runA + "/approach-a/1/tests/1"}
	wantInvalid(t, CheckCompareBrief(decodeCompare(t, v), shape), "items[1].evidence")
	v = good()
	delete(itemOf(v, 1), "evidence")
	wantInvalid(t, CheckCompareBrief(decodeCompare(t, v), shape), "items[1].evidence")
	// Another comparison's candidates, of the same task ids, are not this
	// one's.
	other := "other-0000000c"
	for _, r := range []string{"candidate:" + runA + "/review-a/1", "check:" + runA + "/build/1/tests/1", "candidate:" + other + "/approach-a/1", "check:" + other + "/approach-a/1/tests/1"} {
		v = good()
		itemOf(v, 1)["evidence"] = append(itemOf(v, 1)["evidence"].([]any), r)
		wantInvalid(t, CheckCompareBrief(decodeCompare(t, v), shape), "items[1].evidence[4]")
	}

	// Every ref of every item that names a run names one of the
	// comparison's: each kind naming another run, in the table's evidence
	// or another item, is refused at its field; a brief: or decision: ref
	// is cited in none; the same refs naming the comparison's run are kept.
	sha := strings.Repeat("ab", 20)
	refsOf := func(run string) []string {
		return []string{"attempt:" + run + "/approach-a/1", "task:" + run + "/approach-a", "run:" + run, "commit:" + run + "/" + sha,
			"command:" + run + "/retry-1", "event:" + run + "/3", "message:" + run + "/2", "candidate:" + run + "/approach-b/1", "check:" + run + "/approach-b/1/tests/1"}
	}
	for _, r := range append(refsOf(other), "brief:br-1/0", "decision:dr-1", "capture:0a1b2c3d4e5f6071", "capture:0a1b2c3d4e5f6071/8192a3b4c5d6e7f8") {
		v = good()
		itemOf(v, 1)["evidence"] = append(itemOf(v, 1)["evidence"].([]any), r)
		wantInvalid(t, CheckCompareBrief(decodeCompare(t, v), shape), "items[1].evidence[4]")
		v = good()
		v["items"] = append(v["items"].([]any), map[string]any{"kind": ItemEvidence, "title": "elsewhere", "refs": []any{"candidate:" + runA + "/approach-a/1", r}})
		wantInvalid(t, CheckCompareBrief(decodeCompare(t, v), shape), "items[2].refs[1]")
		v = good()
		v["items"] = append(v["items"].([]any), map[string]any{"kind": ItemFact, "text": "elsewhere", "evidence": []any{r}})
		wantInvalid(t, CheckCompareBrief(decodeCompare(t, v), shape), "items[2].evidence[0]")
	}
	v = good()
	var mine []any
	for _, r := range refsOf(runA) {
		mine = append(mine, r)
	}
	itemOf(v, 1)["evidence"] = append(itemOf(v, 1)["evidence"].([]any), mine[:6]...)
	v["items"] = append(v["items"].([]any), map[string]any{"kind": ItemEvidence, "title": "here", "refs": mine},
		map[string]any{"kind": ItemFact, "text": "here", "evidence": mine[:9]})
	if err := CheckCompareBrief(decodeCompare(t, v), shape); err != nil {
		t.Errorf("refs of the comparison's own runs: %v", err)
	}
	// Every kind a ref can be is judged by the rule: it is in the rule's
	// table (held to the comparison's runs when it names one) or refused by
	// name, never both and never neither; and a form that names a run is
	// read with its run, which is what the rule holds to the comparison.
	for _, f := range RefForms() {
		byName := f.Kind == RefBrief || f.Kind == RefDecision || f.Kind == RefCapture
		if compareRefKinds[f.Kind] == byName {
			t.Errorf("%s: in the rule's table %v, refused by name %v: a kind is one or the other", f.Kind, compareRefKinds[f.Kind], byName)
		}
		r, err := ParseRef(f.Example)
		if err != nil {
			t.Fatalf("%s: its example: %v", f.Kind, err)
		}
		if named := strings.Contains(f.Form, "<run>"); named != (r.Run != "") {
			t.Errorf("%s: its form names a run %v, its ref holds one %v", f.Kind, named, r.Run != "")
		}
	}
	// A kind the table does not name is cited in no compare brief: one
	// added later is refused until the rule judges it.
	later := decodeCompare(t, good())
	later.Items[1].Comparison.Evidence = append(later.Items[1].Comparison.Evidence, Ref{Kind: "later"})
	wantInvalid(t, CheckCompareBrief(later, shape), "items[1].evidence[4]")

	// A decision card is refused, whatever it is about.
	v = good()
	v["items"] = append(v["items"].([]any), jsonValue(t, kinds[4].Example))
	inv := wantInvalid(t, CheckCompareBrief(decodeCompare(t, v), shape), "items[2]")
	if !strings.Contains(inv.Why, "the selection is the person's, on the comparison") {
		t.Errorf("a decision card: %s", inv.Why)
	}

	// A proposal of a lane's command is refused; another proposal stands.
	for _, op := range compareCommands {
		v = good()
		v["items"] = append(v["items"].([]any), map[string]any{"kind": ItemProposal, "text": "Retry it.", "op": op, "target": "task:" + runA + "/approach-a", "facts": []any{0}})
		wantInvalid(t, CheckCompareBrief(decodeCompare(t, v), shape), "items[2].op")
	}
	v = good()
	v["items"] = append(v["items"].([]any), map[string]any{"kind": ItemProposal, "text": "Note it on the ticket.", "op": "ticket_comment_admin", "target": "ticket:CND-1", "facts": []any{0}})
	if err := CheckCompareBrief(decodeCompare(t, v), shape); err != nil {
		t.Errorf("a proposal of another operation: %v", err)
	}

	// A comparison the API could not read holds every brief refused.
	wantInvalid(t, CheckCompareBrief(decodeCompare(t, good()), CompareShape{Commands: compareCommands}), "items")
	wantInvalid(t, CheckCompareBrief(decodeCompare(t, good()), CompareShape{Columns: shape.Columns, Commands: compareCommands}), "items")

	// The rule is in the words the guide gives and in brief_submit's schema.
	rule := RequestRules()[0]
	if rule.Kind != KindCompare {
		t.Fatalf("the rules: %v", RequestRules())
	}
	for _, label := range compareRows {
		if !strings.Contains(rule.Rule, label) {
			t.Errorf("the rule does not name %s: %s", label, rule.Rule)
		}
	}
	if !strings.Contains(strings.Join(Words(), "\n"), rule.Rule) {
		t.Error("the words do not say the compare rule")
	}
	enc, _ := json.Marshal(BriefJSONSchema(nil))
	want, _ := json.Marshal(rule.Rule)
	if !strings.Contains(string(enc), strings.Trim(string(want), `"`)) {
		t.Error("the schema's description does not say the compare rule")
	}
}
