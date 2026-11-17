package api

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The list's filters (BKUI, #993): kind, has, about and q narrow the
// admin's page of requests, each row carrying its brief's digest and,
// with about, how it matched.

const (
	listRun     = "fix-login-1a2b3c4d"
	listSession = "0123456789abcdef"
)

// listRef parses s or fails the test.
func listRef(t *testing.T, s string) brakeman.Ref {
	t.Helper()
	r, err := brakeman.ParseRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// listed keeps a request through the desk's store: asked, then, with
// items, started and briefed under the title and summary, its turn's own
// run and session set to run and session.
func (b *bkEnv) listed(t *testing.T, in brakeman.Request, title, summary string, items []brakeman.Item, run, sess string) string {
	t.Helper()
	st := b.desk.st
	in.OpID = fmt.Sprintf("list-%d", bkOps.Add(1))
	r, _, err := st.Create(in)
	if err != nil {
		t.Fatal(err)
	}
	if items == nil {
		return r.ID
	}
	if _, err := st.Start(r.ID, brakeman.DefaultConfig("/srv/work"), brakeman.Stamp{AgentID: "bk"}); err != nil {
		t.Fatal(err)
	}
	brief := brakeman.Brief{Schema: brakeman.BriefSchema, Title: title, Summary: summary, Items: items}
	if _, err := st.KeepBrief(r.ID, brakeman.Kept{OpID: "brief-1", Brief: brief, RunID: run, SessionID: sess}); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// listFact is a fact citing refs.
func listFact(t *testing.T, text string, refs ...string) brakeman.Item {
	var ev []brakeman.Ref
	for _, r := range refs {
		ev = append(ev, listRef(t, r))
	}
	return brakeman.Item{Kind: brakeman.ItemFact, Fact: &brakeman.Fact{Text: text, Evidence: ev}}
}

// listFixture is six requests of every shape the filters tell apart.
type listFixture struct {
	*bkEnv
	briefing, explain, drafts, wake, asked, locate string
}

func newListFixture(t *testing.T) *listFixture {
	t.Helper()
	b := brakemanEnv(t, nil)
	f := &listFixture{bkEnv: b}
	rec := 0
	decision := brakeman.Item{Kind: brakeman.ItemDecision, Decision: &brakeman.Decision{
		About: listRef(t, "task:"+listRun+"/build"), Happened: "Out of attempts.", WhyHuman: "A retry spends one.", Question: "Retry build?",
		Alternatives: []brakeman.Alternative{{Label: "Retry", Tradeoffs: "One more."}, {Label: "Leave it", Tradeoffs: "None."}}, Recommendation: &rec,
		Authorizes: "A fourth attempt.", Evidence: []brakeman.Ref{listRef(t, "run:"+listRun)},
	}}
	f.briefing = b.listed(t, brakeman.Request{Kind: brakeman.KindBriefing}, "Build is out of attempts", "Three failures, all on the Cache test.",
		[]brakeman.Item{listFact(t, "The run failed.", "run:"+listRun), decision}, "", "")
	proposal := brakeman.Item{Kind: brakeman.ItemProposal, Proposal: &brakeman.Proposal{Text: "Retry it.", Op: "task_retry", Target: listRef(t, "task:"+listRun+"/build"), ExpectedRevision: "3", Facts: []int{0}}}
	f.explain = b.listed(t, brakeman.Request{Kind: brakeman.KindExplain, Subjects: []brakeman.Ref{listRef(t, "task:"+listRun+"/build")}}, "Why build failed", "A flaky test.",
		[]brakeman.Item{listFact(t, "It failed.", "attempt:"+listRun+"/build/3"), proposal}, "", "")
	draft := brakeman.Item{Kind: brakeman.ItemDraftTicket, DraftTicket: &brakeman.DraftTicket{Project: "CND", Title: "Fix the cache", Priority: 2, From: []brakeman.Ref{listRef(t, "ticket:CND-1")}}}
	// Its turn's own run and session are the run and session the about
	// filter asks for: they never match.
	f.drafts = b.listed(t, brakeman.Request{Kind: brakeman.KindDraftTickets, Note: "Split the work in two."}, "Two tickets", "Two tickets to make.",
		[]brakeman.Item{listFact(t, "A ticket is open.", "ticket:CND-1"), draft}, listRun, listSession)
	f.wake = b.listed(t, brakeman.Request{Kind: brakeman.KindWake, Batch: "wb-1"}, "Overnight", "A ticket was made ready.",
		[]brakeman.Item{listFact(t, "Ready.", "ticket:CND-41")}, "", "")
	f.asked = b.listed(t, brakeman.Request{Kind: brakeman.KindAsk, Note: "Why does fix-login flake?"}, "", "", nil, "", "")
	f.locate = b.listed(t, brakeman.Request{Kind: brakeman.KindLocate, Note: "find the parser"}, "", "", nil, "", "")
	if _, err := b.desk.st.Update(f.locate, func(r *brakeman.Request) error {
		return r.To(brakeman.StateCancelled, "cancelled by the test", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// page reads one page of the list.
func (f *listFixture) page(t *testing.T, query string) (rows []map[string]any, next string) {
	t.Helper()
	status, data, out := f.call(t, "GET", "/api/brakeman/requests?"+query, nil)
	if status != http.StatusOK {
		t.Fatalf("?%s: %d %.300s", query, status, data)
	}
	for _, r := range out["requests"].([]any) {
		rows = append(rows, r.(map[string]any))
	}
	return rows, out["next"].(string)
}

// listIDs is the rows' ids, in order.
func listIDs(rows []map[string]any) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r["id"].(string))
	}
	return out
}

// TestRequestsFilterByKindHasAndSearch: each filter narrows the page to
// the requests it names, newest first, every row the request with its
// digest; filters combine; a page at a time skips nothing and repeats
// nothing, and a full last page leads to none.
func TestRequestsFilterByKindHasAndSearch(t *testing.T) {
	t.Parallel()
	f := newListFixture(t)
	all, next := f.page(t, "")
	if got, want := listIDs(all), []string{f.locate, f.asked, f.wake, f.drafts, f.explain, f.briefing}; !slices.Equal(got, want) || next != "" {
		t.Fatalf("unfiltered %v next %q, want %v", got, next, want)
	}
	for _, r := range all {
		_, has := r["digest"]
		if want := r["hasBrief"] == true; has != want {
			t.Errorf("%s: digest %v, hasBrief %v", r["id"], has, want)
		}
		if _, ok := r["match"]; ok {
			t.Errorf("%s: a match with no about", r["id"])
		}
		if r["kind"] == nil || r["state"] == nil || r["createdAt"] == nil {
			t.Errorf("%s: the request's own fields are missing: %v", r["id"], r)
		}
	}
	d := all[5]["digest"].(map[string]any)
	if d["title"] != "Build is out of attempts" || d["reading"] != "Three failures, all on the Cache test." || d["counts"].(map[string]any)["decisions"] != float64(1) {
		t.Fatalf("the briefing's digest %v", d)
	}
	for query, want := range map[string][]string{
		"kind=briefing,wake":         {f.wake, f.briefing},
		"kind=locate":                {f.locate},
		"has=decision":               {f.briefing},
		"has=proposal":               {f.explain},
		"has=draft":                  {f.drafts},
		"has=brief":                  {f.wake, f.drafts, f.explain, f.briefing},
		"q=CACHE":                    {f.briefing},
		"q=fix-login":                {f.asked, f.explain, f.briefing},
		"q=cnd-41":                   {f.wake},
		"q=" + f.drafts:              {f.drafts},
		"q=nothing+at+all":           nil,
		"kind=explain&q=flaky":       {f.explain},
		"kind=briefing&has=proposal": nil,
		"has=brief&q=" + url.QueryEscape("Two tickets"): {f.drafts},
	} {
		rows, next := f.page(t, query)
		if got := listIDs(rows); !slices.Equal(got, want) || next != "" {
			t.Errorf("?%s: %v next %q, want %v", query, got, next, want)
		}
	}
	// A page at a time.
	for _, query := range []string{"has=brief", "kind=briefing,wake", ""} {
		whole, _ := f.page(t, query)
		var paged []string
		after, pages := "", 0
		for {
			q := query + "&limit=2"
			if after != "" {
				q += "&after=" + after
			}
			rows, next := f.page(t, q)
			pages++
			paged = append(paged, listIDs(rows)...)
			if next == "" {
				break
			}
			if len(rows) != 2 || next != rows[len(rows)-1]["id"] || pages > 10 {
				t.Fatalf("?%s: page %d %v next %q", q, pages, listIDs(rows), next)
			}
			after = next
		}
		if !slices.Equal(paged, listIDs(whole)) {
			t.Errorf("?%s paged %v, whole %v", query, paged, listIDs(whole))
		}
		if wantPages := (len(whole) + 1) / 2; pages != max(wantPages, 1) {
			t.Errorf("?%s: %d pages for %d rows", query, pages, len(whole))
		}
	}
	// The request route carries the digest beside the brief.
	_, _, one := f.call(t, "GET", "/api/brakeman/requests/"+f.explain, nil)
	if dg, ok := one["digest"].(map[string]any); !ok || dg["title"] != "Why build failed" || one["brief"] == nil {
		t.Fatalf("the request route: %v", one)
	}
	_, _, none := f.call(t, "GET", "/api/brakeman/requests/"+f.asked, nil)
	if _, ok := none["digest"]; ok || none["brief"] != nil {
		t.Fatalf("a request with no brief: %v", none)
	}
}

// TestAboutMatchesSubjectsAndCitationsOnTheList: about lists what names
// the thing, a subject naming it or a part of it as subject (winning
// over a citation), a brief citing it as cited; a request whose own run
// and session are the thing, and that names and cites nothing of it,
// does not match.
func TestAboutMatchesSubjectsAndCitationsOnTheList(t *testing.T) {
	t.Parallel()
	f := newListFixture(t)
	match := func(rows []map[string]any) map[string]any {
		out := map[string]any{}
		for _, r := range rows {
			out[r["id"].(string)] = r["match"]
		}
		return out
	}
	for query, want := range map[string]map[string]any{
		"about=run:" + listRun:                         {f.explain: "subject", f.briefing: "cited"},
		"about=task:" + listRun + "/build":             {f.explain: "subject", f.briefing: "cited"},
		"about=task:" + listRun + "/deploy":            {},
		"about=session:" + listSession:                 {},
		"about=ticket:CND-41":                          {f.wake: "cited"},
		"about=ticket:CND-1":                           {f.drafts: "cited"},
		"about=run:checkout-0c1d2e3f":                  {},
		"about=run:" + listRun + "&has=decision":       {f.briefing: "cited"},
		"about=run:" + listRun + "&kind=explain":       {f.explain: "subject"},
		"about=run:" + listRun + "&kind=draft_tickets": {},
	} {
		rows, _ := f.page(t, query)
		if got := match(rows); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("?%s: %v, want %v", query, got, want)
		}
	}
}

// TestRequestsFilterRefusals: each parameter not what it takes, given
// twice, or one the route does not take is 400 invalid_request, and the
// bounds are held at their edge.
func TestRequestsFilterRefusals(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	for _, query := range []string{
		"kind=", "kind=nope", "kind=briefing,briefing", "kind=briefing,,wake", "kind=Briefing",
		"has=", "has=decisions", "has=decision&has=draft",
		"about=", "about=comment:CND-1/0123456789abcdef", "about=check:" + listRun + "/build/1/vet/1", "about=attempt:" + listRun + "/build/1",
		"about=question:" + listSession + "/" + listSession, "about=nonsense", "about=run:Nope",
		"about=ticket:CND-" + strings.Repeat("1", brakeman.MaxRef),
		"q=", "q=" + strings.Repeat("a", maxBrakemanSearch+1), "q=a%0Ab", "q=a%0Db", "q=%FF", "q=a&q=b",
		"limit=0", "limit=101", "limit=01", "limit=x", "after=x", "after=br-0", "after=br-1&after=br-2",
		"other=1",
	} {
		status, data, out := b.call(t, "GET", "/api/brakeman/requests?"+query, nil)
		if status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
			t.Errorf("?%.60s: %d %.200s", query, status, data)
		}
	}
	for _, query := range []string{
		"q=" + strings.Repeat("a", maxBrakemanSearch), "limit=100", "limit=1", "kind=" + strings.Join(append(slices.Clone(brakeman.Kinds), brakeman.KindWake), ","),
		"about=ticket:CND-41", "has=brief", "q=" + url.QueryEscape("é ; & %"),
	} {
		if status, data, _ := b.call(t, "GET", "/api/brakeman/requests?"+query, nil); status != http.StatusOK {
			t.Errorf("?%.60s: %d %.200s", query, status, data)
		}
	}
}

// TestRequestsFiltersAreTheAdminsAlone: the list, filtered or not, and a
// request's digest answer the admin alone: no credential, a wrong one,
// the host's token, a share link and a turn's own session token are each
// refused 401.
func TestRequestsFiltersAreTheAdminsAlone(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	_, turn, _ := b.turnSession(t, id)
	_, share, err := b.srv.links.Create("s1", session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/brakeman/requests",
		"/api/brakeman/requests?kind=briefing&has=brief&q=x&about=run:" + listRun,
		"/api/brakeman/requests?about=session:" + listSession,
		"/api/brakeman/requests/" + id,
	} {
		for _, tok := range []string{"", "wrong", "test-host-token", share, turn} {
			if status, data, _ := b.callAs(t, tok, "GET", path, nil); status != http.StatusUnauthorized {
				t.Errorf("%s with token %.12q: %d %.200s", path, tok, status, data)
			}
		}
		if status, data, _ := b.call(t, "GET", path, nil); status != http.StatusOK {
			t.Errorf("%s as the admin: %d %.200s", path, status, data)
		}
	}
}
