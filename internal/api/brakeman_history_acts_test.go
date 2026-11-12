package api

import (
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// actBodies is the body each operation an act may be granted is decoded
// as by its route (actArgs, turnRoute).
var actBodies = map[string]reflect.Type{
	brakeman.ActLaunchApprovedPlan: reflect.TypeOf(ticketLaunchBody{}),
	brakeman.ActPrepareFollowup:    reflect.TypeOf(planProposeBody{}),
	brakeman.ActRequestRepair:      reflect.TypeOf(reviewSubmitBody{}),
	brakeman.ActRouteDecision:      reflect.TypeOf(decisionRouteBody{}),
}

// actWords is, by operation and by each text field of its body (its JSON
// path, [] for an item of a list), how the walk below writes it: "planted"
// when it carries words of the turn's own, else why it carries none (a
// value the operation resolves to a record, or one of a closed set). A
// field of no entry fails the test.
var actWords = map[string]map[string]string{
	brakeman.ActLaunchApprovedPlan: {
		"opId": "the act's op id", "byName": "planted", "plan": "a plan revision the operation resolves", "label": "planted",
	},
	brakeman.ActPrepareFollowup: {
		"opId": "the act's op id", "byName": "planted", "crew": "a saved crew the operation resolves", "base": "a commit the operation resolves",
		"basedOn": "a plan revision the operation resolves", "references[].path": "a file the operation resolves at the base",
		"references[].note": "planted", "attachments[]": "an attachment's hash the operation resolves", "comments[]": "a comment the operation resolves",
		"constraints": "planted", "decisions[].id": "planted", "decisions[].question": "planted", "decisions[].kind": "one of the decision kinds",
		"decisions[].before[]": "the plan's tasks", "decisions[].for": "a person or a responsibility of the crew's",
		"note": "planted",
	},
	brakeman.ActRequestRepair: {
		"opId": "the act's op id", "byName": "planted", "candidate": "a candidate the operation resolves", "base": "a commit the operation resolves",
		"verdict": "request_changes alone", "summary": "planted", "via": "the server's alone (refused when sent)",
		"findings[].kind": "one of the finding kinds", "findings[].intent": "one of the intents", "findings[].severity": "one of the severities",
		"findings[].path": "a file of the candidate's", "findings[].side": "candidate or base", "findings[].lines[]": "planted",
		"findings[].message": "planted", "findings[].resolution": "planted", "findings[].check.source": "the ledger alone",
		"findings[].check.receipt": "a receipt the operation resolves", "findings[].check.chosen": "one of the classes",
		"findings[].check.quote": "planted",
	},
	brakeman.ActRouteDecision: {
		"opId": "the act's op id", "to.kind": "operator, person or role", "to.name": "a person or a responsibility of the crew's", "to.grant": "a turn's route to a grant is refused (decide-grant.md)", "note": "planted",
	},
}

// textFields is every text field of rt by its JSON path, embedded structs
// read as their fields.
func textFields(rt reflect.Type, prefix string) []string {
	var out []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		ft := f.Type
		if f.Anonymous && name == "" {
			out = append(out, textFields(ft, prefix)...)
			continue
		}
		if name == "-" || !f.IsExported() {
			continue
		}
		path := prefix + name
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.String:
			out = append(out, path)
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.String:
			out = append(out, path+"[]")
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			out = append(out, textFields(ft.Elem(), path+"[].")...)
		case ft.Kind() == reflect.Struct:
			out = append(out, textFields(ft, path+".")...)
		}
	}
	return out
}

// Every operation an act may be granted is decided field by field: each
// text field of its body carries words the turn wrote (planted below) or
// names why it carries none, and the table names no field the body lacks.
func TestEveryActsFieldIsDecided(t *testing.T) {
	if len(actBodies) != len(brakeman.Grantable) || len(actWords) != len(brakeman.Grantable) {
		t.Fatalf("%d bodies, %d tables, %d grantable", len(actBodies), len(actWords), len(brakeman.Grantable))
	}
	for _, op := range brakeman.Grantable {
		rt, ok := actBodies[op]
		if !ok {
			t.Errorf("%s: no body", op)
			continue
		}
		fields := textFields(rt, "")
		for _, p := range fields {
			if actWords[op][p] == "" {
				t.Errorf("%s: %s undecided", op, p)
			}
		}
		for p := range actWords[op] {
			if !slices.Contains(fields, p) {
				t.Errorf("%s: %s is no field of its body", op, p)
			}
		}
	}
}

// No entry a turn's history serves carries words an act wrote, however
// many records they were copied through. Each planted field of each act
// (actWords) holds a word of its own, and each act is taken through what
// follows it: a launch with its label; a follow-up revision proposed,
// approved and launched to its gate; that gate's decision routed with a
// note; a repair requested of a candidate, its lines and its check's quote
// recorded through the act's operation under its actor. Once the history
// holds them (the operator's search finds the gate's question, the label
// and the review's summary), a turn of every class, under each shape of
// scope, reading each word, the empty line and every kind it may be
// served, is served none of them.
func TestNoActsWordsReachATurn(t *testing.T) {
	grant := []string{brakeman.ActLaunchApprovedPlan, brakeman.ActPrepareFollowup, brakeman.ActRequestRepair, brakeman.ActRouteDecision}
	every := func(scope map[string]any) func(c map[string]any) {
		return func(c map[string]any) {
			c["scope"] = scope
			c["visibility"] = map[string]any{"runs": true, "logs": true, "dispatch": true, "sessions": true, "touched": true, "terminals": true}
			c["budget"].(map[string]any)["readsPerTurn"] = brakeman.MaxReadsPerTurn
		}
	}
	f := newActFixture(t, grant, every(map[string]any{}))
	words := map[string]string{}
	word := func(op, field string) string {
		w := "quokka" + strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' {
				return r
			}
			return -1
		}, strings.ToLower(op+field))
		words[op+" "+field] = w
		return w
	}
	planted := func(op string) {
		for p, how := range actWords[op] {
			if how == "planted" && words[op+" "+p] == "" {
				t.Errorf("%s: %s is planted by no step", op, p)
			}
		}
	}

	// The repair, of a candidate of a run the server runs.
	run, c := succeededCandidate(t, f.launchTest, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	const repair = brakeman.ActRequestRepair
	line := lineOf("a.txt", "candidate", 1, 1, word(repair, "findings[].lines[]"))
	line["message"], line["resolution"] = word(repair, "findings[].message"), word(repair, "findings[].resolution")
	if status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/reviews", map[string]any{"opId": "w-1", "candidate": candName(run, "build", 1, c),
		"base": f.head, "verdict": dispatch.VerdictRequestChanges, "summary": word(repair, "summary"), "byName": word(repair, "byName"),
		"findings": []any{line}}); status != http.StatusCreated {
		t.Fatalf("the repair: %d %v", status, out)
	}
	// A check finding's quote, through the act's operation under its actor:
	// the act asks it of a failing receipt of the candidate's attempt.
	quote, failed := word(repair, "findings[].check.quote"), 1
	if _, _, err := f.srv.dispatch.SubmitReview(t.Context(), dispatch.Op{Actor: brakemanActor, ID: opID(), By: brakemanBy}, "CND-1", dispatch.NewReview{
		Candidate: candName(run, "build", 1, c), Base: f.head, Verdict: dispatch.VerdictComment, Via: dispatch.ViaBrakeman,
		Findings: []dispatch.NewFinding{{Kind: "check", Intent: "change", Severity: "major", Message: "It fails.", Check: &dispatch.CheckRequest{Source: "ledger", Receipt: run + "/build/1/check/unit/1", Chosen: "code", Quote: quote}}},
	}, dispatch.ReviewEvidence{Findings: []dispatch.FindingEvidence{{K: 1, Check: &dispatch.CheckRef{Source: "ledger", Receipt: run + "/build/1/check/unit/1", Run: run, Task: "build", Attempt: 1, Phase: "check", ID: "unit", K: 1, Subject: "s", Argv: []string{"go", "test"}, Exit: &failed, State: "finished", Where: "native", Quote: quote, Chosen: "code", Class: "code"}}}}); err != nil {
		t.Fatalf("the check finding: %v", err)
	}
	planted(repair)

	// The launch, with its label.
	const launch = brakeman.ActLaunchApprovedPlan
	if status, out := f.launchAct(t, map[string]any{"opId": "w-2", "expectedRev": f.ticketRev(t), "label": word(launch, "label"), "byName": word(launch, "byName")}); status != http.StatusCreated {
		t.Fatalf("the launch: %d %v", status, out)
	}
	planted(launch)

	// The follow-up, approved and launched to its gate.
	const follow = brakeman.ActPrepareFollowup
	status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": "w-3", "crew": f.crew.ID, "basedOn": f.plan,
		"note": word(follow, "note"), "constraints": word(follow, "constraints"), "byName": word(follow, "byName"),
		"references": []any{map[string]any{"path": "README", "note": word(follow, "references[].note")}},
		"decisions":  []any{map[string]any{"id": word(follow, "decisions[].id"), "question": word(follow, "decisions[].question") + " may it ship?", "kind": "approval", "before": []any{"build"}}}})
	if status != http.StatusCreated {
		t.Fatalf("the follow-up: %d %v", status, out)
	}
	planted(follow)
	revision := entityOf(t, out, "plan")["id"].(string)
	f.expect(t, "POST", "/api/dispatch/plans/"+revision+"/approve", map[string]any{"opId": opID(), "byName": "Nate", "note": "Go."}, http.StatusOK, "")
	gated := entityOf(t, f.launch(t, f.testEnv, map[string]any{"plan": revision}, http.StatusCreated, ""), "launch")["runId"].(string)
	var gate string
	waitUntil(t, "the gate open", func() bool {
		for _, task := range runOf(t, f.testEnv, gated)["tasks"].([]any) {
			hs, _ := task.(map[string]any)["holds"].([]any)
			for _, h := range hs {
				if m := h.(map[string]any); m["state"] == "open" {
					gate, _ = m["id"].(string)
				}
			}
		}
		return gate != ""
	})

	// The gate's decision, routed with a note.
	const route = brakeman.ActRouteDecision
	n, _ := ledger.ParseDecisionID(gate)
	rec, ok, err := f.srv.ledger.Decision(t.Context(), n)
	if err != nil || !ok {
		t.Fatalf("%s: %v %v", gate, ok, err)
	}
	rev := rec.Rev
	if status, out := f.asTurn(t, http.MethodPost, "/api/decisions/"+gate+"/route", map[string]any{"opId": "w-4", "expectedRev": rev,
		"to": map[string]any{"kind": "operator"}, "note": word(route, "note")}); status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("the route: %d %v", status, out)
	}
	planted(route)

	// The history holds the words that reach it.
	histSettle(t, f.testEnv)
	for _, w := range []string{words[follow+" decisions[].question"], words[launch+" label"], words[repair+" summary"]} {
		until(t, w+" in the operator's history", func() bool { return len(hsKeys(hsSearch(t, f.testEnv, w))) > 0 })
	}
	lines := []string{"", "quokka"}
	for _, w := range words {
		lines = append(lines, w)
	}
	for _, k := range slices.Sorted(maps.Keys(historyKinds)) {
		lines = append(lines, "kind:"+k)
	}
	sort.Strings(lines[2:])
	for _, scope := range []map[string]any{
		{},
		{"crews": []string{f.crew.ID}},
		{"runs": []string{gated}},
		{"projects": []string{"CND"}},
		{"repos": []string{f.root}},
	} {
		f.endTurn(t)
		f.turn(t, grant, every(scope))
		for _, l := range lines {
			status, out := f.asTurn(t, http.MethodGet, searchPath(l, "limit", "20"), nil)
			if status != http.StatusOK {
				t.Fatalf("%v %q: %d %v", scope, l, status, out)
			}
			body, _ := json.Marshal(out["entries"])
			for field, w := range words {
				if strings.Contains(strings.ToLower(string(body)), w) {
					t.Errorf("scope %v, line %q: the words of %s are served: %s", scope, l, field, body)
				}
			}
		}
	}
}
