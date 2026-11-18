package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A comparison from the shell (PR-3): conductor crews compare, conductor
// dispatch launch --candidates and conductor dispatch comparison, against
// a real server and a real repository (newPlannedAPI).

// compareSpecJSON is a comparison of two candidates on ready-cat in repo,
// each reviewed.
func compareSpecJSON(t *testing.T, repo string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"name": "Compare the cache", "cwd": repo, "goal": "Make the lookup fast.", "instructions": "Make it fast.",
		"criteria":   []map[string]any{{"id": "fast", "text": "The lookup is fast."}},
		"checks":     []map[string]any{{"id": "ok", "argv": []string{"true"}}},
		"candidates": []map[string]any{{"agentId": "ready-cat", "approach": "key the cache by locale"}, {"agentId": "ready-cat"}},
		"reviews":    true, "reviewAgent": "ready-cat"})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// crewsCompare runs conductor crews compare with the args against the
// server, stdin given: its exit, its output and its error.
func (a *plannedAPI) crewsCompare(t *testing.T, stdin string, args ...string) (int, string, error) {
	t.Helper()
	var out bytes.Buffer
	code, err := Run(t.Context(), append([]string{"crews", "compare", "--server", a.hs.URL, "--token", dispatchToken}, args...), strings.NewReader(stdin), &out, &bytes.Buffer{})
	return code, out.String(), err
}

var compareCrewLine = regexp.MustCompile(`^crew ([a-z0-9-]+) "Compare the cache"\n`)

// conductor crews compare saves a comparison's crew from a spec file or
// stdin and prints its id and tasks; the server's refusal is exit 1 in its
// words; a spec that is not JSON, one past 256 KiB and no spec at all are
// exit 2, nothing sent.
func TestCrewsCompare(t *testing.T) {
	a := newPlannedAPI(t)
	spec := filepath.Join(a.root, "spec.json")
	if err := os.WriteFile(spec, []byte(compareSpecJSON(t, a.repo)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{spec}, {"-"}} {
		code, out, err := a.crewsCompare(t, compareSpecJSON(t, a.repo), args...)
		if code != 0 || err != nil || !compareCrewLine.MatchString(out) {
			t.Fatalf("crews compare %v: %d %v %q", args, code, err, out)
		}
		for _, task := range []string{"  approach-a  work by cand-a\n", "  approach-b  work by cand-b\n", "  review-a  review by rev-a after approach-a\n", "  review-b  review by rev-b after approach-b\n"} {
			if !strings.Contains(out, task) {
				t.Fatalf("crews compare %v: no %q in %q", args, task, out)
			}
		}
	}
	one := strings.Replace(compareSpecJSON(t, a.repo), `,{"agentId":"ready-cat"}`, "", 1)
	if code, _, err := a.crewsCompare(t, one, "-"); code != 1 || err == nil || !strings.Contains(err.Error(), "2 to 4 candidates, not 1") {
		t.Fatalf("one candidate: %d %v", code, err)
	}
	sent := a.count()
	for name, tc := range map[string]struct {
		stdin string
		args  []string
	}{"not JSON": {"{", []string{"-"}}, "past 256 KiB": {strings.Repeat(" ", maxCompareSpec) + "{}", []string{"-"}}, "no spec": {"", nil}, "two specs": {"", []string{spec, spec}}} {
		if code, _, err := a.crewsCompare(t, tc.stdin, tc.args...); code != 2 || err == nil {
			t.Fatalf("%s: %d %v", name, code, err)
		}
	}
	if n := a.count(); n != sent {
		t.Fatalf("%d requests sent for the usage errors", n-sent)
	}
}

// comparedTicket makes a comparison's crew through the CLI and an approved
// revision of it for CND-1, and returns the crew's id.
func (a *plannedAPI) comparedTicket(t *testing.T) string {
	t.Helper()
	code, out, err := a.crewsCompare(t, compareSpecJSON(t, a.repo), "-")
	if code != 0 || err != nil {
		t.Fatalf("crews compare: %d %v", code, err)
	}
	id := compareCrewLine.FindStringSubmatch(out)[1]
	a.mustDispatch(t, "plan", "propose", "CND-1", "--crew", id)
	a.mustDispatch(t, "plan", "approve", "CND-1", "1")
	return id
}

// conductor dispatch launch of a comparison's revision without
// --candidates, or with another count, prints what the launch starts (the
// candidates and their agents, up to one review session each, each
// candidate worked as a crew's task, the checks at the base, no total)
// and exits 2 with nothing sent; with the count it launches. --candidates
// on an ordinary revision is exit 2, nothing sent.
func TestDispatchLaunchCandidates(t *testing.T) {
	a := newPlannedAPI(t)
	a.comparedTicket(t)
	for _, args := range [][]string{{"CND-1"}, {"CND-1", "--candidates", "3"}} {
		from := a.count()
		code, out, _, err := a.dispatchCmd(t, "", "launch", args...)
		if code != 2 || err == nil || !strings.Contains(err.Error(), "send --candidates 2 to launch them") {
			t.Fatalf("launch %v: %d %v", args, code, err)
		}
		want := "This comparison starts 2 candidates (ready-cat, ready-cat): 2 agent sessions, once the plan's decisions and the project's prerequisites let the work start; " +
			"then an agent review of each candidate that succeeds: up to 2 more sessions, none for a candidate that fails or is cancelled; " +
			"then what 2 runs of one candidate's crew would take, since each candidate is worked as a task of its own crew is"
		if !strings.HasPrefix(out, want) || !strings.Contains(out, "1 check on each submitted candidate attempt (the task's checks; the plan holds no manifest, so no verify steps)") || strings.Contains(out, "Brakeman") || strings.Contains(strings.ToLower(out), "total") {
			t.Fatalf("launch %v printed %q", args, out)
		}
		if p := a.posts(from, "/api/dispatch/tickets/CND-1/launch"); len(p) != 0 {
			t.Fatalf("launch %v sent %d launches", args, len(p))
		}
	}
	out := a.mustDispatch(t, "launch", "CND-1", "--candidates", "2")
	if !strings.HasPrefix(out, "CND-1 launched from r1: launch ") {
		t.Fatalf("the launch: %q", out)
	}
	// An ordinary revision takes no count.
	a.mustDispatch(t, "plan", "propose", "CND-1", "--crew", a.crew)
	a.mustDispatch(t, "plan", "approve", "CND-1", "2")
	from := a.count()
	if code, _, _, err := a.dispatchCmd(t, "", "launch", "CND-1", "--plan", "2", "--candidates", "1"); code != 2 || err == nil || !strings.Contains(err.Error(), "r2 is no comparison") {
		t.Fatalf("a count on an ordinary revision: %d %v", code, err)
	}
	if p := a.posts(from, "/api/dispatch/tickets/CND-1/launch"); len(p) != 0 {
		t.Fatal("a launch was sent")
	}
}

var launchedRun = regexp.MustCompile(`, run ([a-z0-9-]+) "`)

// conductor dispatch comparison prints the ticket's newest comparison when
// --run is not given, any run of its chain's with it (a continuation's
// names the launched run), as text (its base, criteria, a column per
// candidate, the checks, what is not observed) or with --json the
// server's reply.
func TestDispatchComparison(t *testing.T) {
	a := newPlannedAPI(t)
	a.comparedTicket(t)
	first := launchedRun.FindStringSubmatch(a.mustDispatch(t, "launch", "CND-1", "--candidates", "2"))[1]
	second := launchedRun.FindStringSubmatch(a.mustDispatch(t, "launch", "CND-1", "--candidates", "2"))[1]
	text := a.mustDispatch(t, "comparison", "CND-1")
	if !strings.HasPrefix(text, "CND-1: comparison of "+second+" at ") || !strings.Contains(text, "objective: Make the lookup fast.\n") || !strings.Contains(text, "  fast: The lookup is fast.\n") ||
		!strings.Contains(text, "  approach-a\tcand-a on ready-cat") || !strings.Contains(text, "    approach: key the cache by locale\n") || !strings.Contains(text, "\nnot observed: ") {
		t.Fatalf("the newest comparison:\n%s", text)
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(a.mustDispatch(t, "comparison", "CND-1", "--run", first, "--json")), &reply); err != nil || reply["run"] != first || reply["independence"] == nil {
		t.Fatalf("--json of the first: %v %v", reply, err)
	}
	// A continuation of the first reads the first's comparison.
	a.admin(t, "POST", "/api/runs/"+first+"/stop", nil, http.StatusOK)
	next := a.admin(t, "POST", "/api/runs/"+first+"/resume", nil, http.StatusCreated)["run"].(map[string]any)["id"].(string)
	if text := a.mustDispatch(t, "comparison", "CND-1", "--run", next); !strings.HasPrefix(text, "CND-1: comparison of "+first+" at ") {
		t.Fatalf("the continuation's comparison:\n%s", text)
	}
	if code, _, _, err := a.dispatchCmd(t, "", "comparison", "CND-1", "--run", "not a run"); code != 2 || err == nil {
		t.Fatalf("a bad run: %d %v", code, err)
	}
}

// Without --run, conductor dispatch comparison reads the ticket's newest
// comparison launch that started: a newer one that failed (its run id
// minted, no run made) is passed over.
func TestDispatchComparisonPicksAStartedLaunch(t *testing.T) {
	clearConductorEnv(t)
	plan := strings.Repeat("ab", 8)
	plans, _ := encodeJSON(map[string]any{"plans": []any{map[string]any{"id": plan, "n": 1, "state": dispatch.PlanApproved, "compare": map[string]any{"candidates": 2}}}})
	launch := func(run, state string) map[string]any {
		return map[string]any{"kind": "launch", "launch": dispatch.LaunchSummary{ID: run + "-l", PlanID: plan, PlanN: 1, RunID: run, State: state}}
	}
	runs, _ := encodeJSON(map[string]any{"entries": []any{launch("cmp-0000000b", dispatch.LaunchFailed), launch("cmp-0000000a", dispatch.LaunchStarted)}})
	var asked []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/dispatch/tickets/CND-1/plans", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(plans) })
	mux.HandleFunc("GET /api/dispatch/tickets/CND-1/runs", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(runs) })
	mux.HandleFunc("GET /api/runs/{run}/comparison", func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.PathValue("run"))
		if r.PathValue("run") != "cmp-0000000a" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no such run"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"run":"cmp-0000000a","base":"` + strings.Repeat("1", 40) + `","objective":"o","criteria":[],"candidates":[],"checks":[],"settled":false,"independence":{"notObserved":"n"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	code, err := runDispatch(t.Context(), []string{"comparison", "CND-1", "--server", srv.URL, "--token", "t"}, strings.NewReader(""), &out, &bytes.Buffer{})
	if code != 0 || err != nil || !strings.HasPrefix(out.String(), "CND-1: comparison of cmp-0000000a at ") || len(asked) != 1 {
		t.Fatalf("comparison: %d %v %q, asked %v", code, err, out.String(), asked)
	}
}

// The launch's statement names the verify steps the revision's manifest
// holds: none for a plan with no manifest, the crew's own, or the file's
// at the base, which a candidate that changes it runs its own of.
func TestTheCompareStatementNamesTheManifestHeld(t *testing.T) {
	pc := planCompareEntry{Candidates: 2, ChecksAtBase: 1}
	for _, tc := range []struct {
		name, body, want string
	}{
		{"none", `{"crew":{"name":"x"}}`, "1 check on each submitted candidate attempt (the task's checks; the plan holds no manifest, so no verify steps)"},
		{"the crew's", `{"crew":{"manifest":{"verify":[{"id":"vet","argv":["true"]}]}}}`, "1 check on each submitted candidate attempt (the task's checks and the crew's manifest's verify steps)"},
		{"the file", `{"crew":{"manifest":{"file":true}},"manifestFile":{"path":"conductor.manifest.json"}}`,
			"1 check on each submitted candidate attempt (the task's checks and conductor.manifest.json's verify steps at the base; a candidate that changes the manifest runs its own)"},
		{"a body not read", `{`, "conductor.manifest.json's verify steps at the base"},
	} {
		if got := compareStatement(pc, nil, compareManifest([]byte(tc.body))); !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q has no %q", tc.name, got, tc.want)
		}
	}
	if got := compareStatement(pc, nil, compareManifest([]byte(`{"crew":{}}`))); strings.Contains(got, "manifest.json") {
		t.Errorf("a plan with no manifest names the file: %q", got)
	}
}
