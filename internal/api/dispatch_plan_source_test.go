package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// sourceTicket imports item 41 of the fake tracker into CND (its
// description holding steps to reproduce) and adds an external link to
// it: the ticket's key, its linked item's identity, the steps' SHA-256 as
// its snapshot pins them, and the link's id.
func sourceTicket(t *testing.T, l *launchTest) (key, identity, reproSHA, linkID string) {
	t.Helper()
	l.srv.limiter = newRateLimiter(1e6, 1e6)
	f := fakeGitHub(t)
	useFake(l.srv, f)
	if status, data, _ := l.saveToken(t, "", plantedToken); status != http.StatusOK {
		t.Fatalf("the token: %d %s", status, data)
	}
	items := newGHItems(f)
	items.set(41, ghItem{Node: "I_kwDOitem41", Title: "It breaks", Updated: t0,
		Body: "It breaks on start.\r\n\r\n### Steps to reproduce\r\n\r\n1. Start it\r\n2. See it break\r\n"})
	tk := entityOf(t, l.importItem(t, "CND", "https://github.com/octo/app/issues/41", opID(), http.StatusCreated, nil), "ticket")
	key = tk["key"].(string)
	var sn struct {
		ReproSha256 string `json:"reproSha256"`
	}
	raw, _ := json.Marshal(tk["externalSnapshot"])
	if err := json.Unmarshal(raw, &sn); err != nil || sn.ReproSha256 == "" {
		t.Fatalf("the snapshot pins no steps: %s %v", raw, err)
	}
	out := l.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "external", "url": "https://example.com/trace/7", "note": "the trace"}, http.StatusCreated, "")
	linkID = out["receipt"].(map[string]any)["id"].(string)
	return key, tk["externalId"].(string), sn.ReproSha256, linkID
}

// The propose route takes the revision's source over HTTP: the steps to
// reproduce by the pin the proposal showed and the evidence links by
// their ids, kept in the revision's body as the store checked them; a pin
// that is not the snapshot's now is stale_plan, one with a part empty
// invalid, an evidence link of no ticket's not_found; a proposal without
// a source hands over nothing.
func TestTheProposeRouteTakesTheSource(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	key, identity, reproSHA, linkID := sourceTicket(t, l)
	src := map[string]any{"repro": map[string]any{"identity": identity, "sha256": reproSHA}, "evidence": []string{linkID}}
	id := entityOf(t, l.propose(t, key, map[string]any{"crew": l.crew.ID, "source": src}, http.StatusCreated, ""), "plan")["id"].(string)
	_, body, _ := l.planBodyOf(t, id)
	if s := body.Source; s == nil || s.Repro == nil || s.Repro.Identity != identity || s.Repro.SHA256 != reproSHA || len(s.Evidence) != 1 || s.Evidence[0] != linkID {
		t.Fatalf("the revision's source: %+v", body.Source)
	}
	plain := entityOf(t, l.propose(t, key, map[string]any{"crew": l.crew.ID}, http.StatusCreated, ""), "plan")["id"].(string)
	if _, body, _ := l.planBodyOf(t, plain); body.Source != nil {
		t.Fatalf("a proposal with no source hands over %+v", body.Source)
	}
	l.propose(t, key, map[string]any{"crew": l.crew.ID, "source": map[string]any{"repro": map[string]any{"identity": identity, "sha256": strings.Repeat("0", 64)}}}, http.StatusConflict, "stale_plan")
	l.propose(t, key, map[string]any{"crew": l.crew.ID, "source": map[string]any{"repro": map[string]any{"identity": identity, "sha256": ""}}}, http.StatusConflict, "invalid")
	l.propose(t, key, map[string]any{"crew": l.crew.ID, "source": map[string]any{"evidence": []string{"0123456789abcdef"}}}, http.StatusNotFound, "not_found")
	l.propose(t, "CND-1", map[string]any{"crew": l.crew.ID, "source": map[string]any{"repro": map[string]any{"identity": identity, "sha256": reproSHA}}}, http.StatusConflict, "invalid")
	l.propose(t, key, map[string]any{"crew": l.crew.ID, "source": map[string]any{"bogus": true}}, http.StatusBadRequest, "invalid_request")
}

// A launch from a ticket with a linked issue, its revision's source sent
// over HTTP: the attempt's ticket.md, read from its worktree, holds the
// Source part, the steps to reproduce inside their quote and the evidence
// link, and its package.json the pinned steps and the evidence.
func TestRunsPackageCarriesSource(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	key, identity, reproSHA, linkID := sourceTicket(t, l)
	src := map[string]any{"repro": map[string]any{"identity": identity, "sha256": reproSHA}, "evidence": []string{linkID}}
	plan := entityOf(t, l.propose(t, key, map[string]any{"crew": l.crew.ID, "source": src}, http.StatusCreated, ""), "plan")["id"].(string)
	l.expect(t, "POST", "/api/dispatch/plans/"+plan+"/approve", map[string]any{"opId": opID(), "byName": "Nate"}, http.StatusOK, "")
	out := l.expect(t, "POST", "/api/dispatch/tickets/"+key+"/launch", map[string]any{"opId": opID(), "plan": plan}, http.StatusCreated, "")
	runID := entityOf(t, out, "launch")["runId"].(string)
	runningAttempt(t, l.testEnv, runID, "build", 1)
	md := string(l.treeFile(t, runID, "build", 1, "ticket.md"))
	for _, want := range []string{"## Source\n", "## Steps to reproduce\n", "> 1. Start it\n", "> 2. See it break\n", "## Evidence links\n", "- <https://example.com/trace/7>: the trace\n"} {
		if !strings.Contains(md, want) {
			t.Fatalf("ticket.md lacks %q:\n%s", want, md)
		}
	}
	checkQuotedFile(t, "ticket.md", []byte(md))
	var pkg struct {
		Source struct {
			URL   string `json:"url"`
			Repro string `json:"repro"`
		} `json:"source"`
		Evidence []struct {
			URL string `json:"url"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(l.treeFile(t, runID, "build", 1, "package.json"), &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Source.URL != "https://github.com/octo/app/issues/41" || !strings.Contains(pkg.Source.Repro, "See it break") || len(pkg.Evidence) != 1 || pkg.Evidence[0].URL != "https://example.com/trace/7" {
		t.Fatalf("package.json: %+v", pkg)
	}
}
