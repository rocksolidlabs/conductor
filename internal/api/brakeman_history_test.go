package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/history"
)

// Brakeman reads history within its grant (docs/plans/history.md, PR-11):
// GET /api/brakeman/history answers the History search for a turn, each
// entry admitted as Brakeman's refs are, and a session the registry no
// longer lists resolves from its record. Every wait is on the indexer
// (histSettle), the journal (endedRecord) or the desk's records, never on
// a clock.

// historyRefs is each entry's ref of a turn's history read, in order.
func historyRefs(out map[string]any) []string {
	refs := []string{}
	es, _ := out["entries"].([]any)
	for _, x := range es {
		refs = append(refs, x.(map[string]any)["ref"].(string))
	}
	return refs
}

// historyEntryOf is the entry of a turn's history read whose ref is ref,
// nil for none.
func historyEntryOf(out map[string]any, ref string) map[string]any {
	es, _ := out["entries"].([]any)
	for _, x := range es {
		if m := x.(map[string]any); m["ref"] == ref {
			return m
		}
	}
	return nil
}

// receipts adds to build in each of the fixture's runs a second attempt
// whose check failed with TestTurnstile in its log (a new attempt: an
// ended attempt's receipts are read once by the index), and answers their
// refs (team's, other's) once the index holds them.
func receipts(t *testing.T, f *obsFixture) (string, string) {
	t.Helper()
	log := "--- FAIL: TestTurnstile (0.02s)\n"
	for _, run := range []string{f.run, f.other} {
		histTx(t, f.srv.ledger, run, func(tx *ledger.Tx) error {
			a := histAttempt("build", 2, ledger.AttemptFailed, "", commitID(1), "")
			a.ClosedAt = histNow.Add(3 * time.Minute)
			if err := tx.PutAttempt(a); err != nil {
				return err
			}
			return tx.PutCheck(histReceipt("build", 2, "unit", 1, commitID(1), 1, log))
		})
	}
	team, other := "check:"+f.run+"/build/2/unit/1", "check:"+f.other+"/build/2/unit/1"
	// A run read a moment ago is read again once its interval has passed
	// (minRunInterval): the index is asked until it holds both.
	ix := histSettle(t, f.testEnv)
	until(t, "the receipts indexed", func() bool {
		for _, k := range []string{team, other} {
			if _, err := ix.Entry(t.Context(), k); err != nil {
				return false
			}
		}
		return true
	})
	return team, other
}

// searchPath is the turn's history read of line.
func searchPath(line string, extra ...string) string {
	q := url.Values{"q": {line}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return "/api/brakeman/history?" + q.Encode()
}

// A plain session in a repository, ended, and the server restarted: a turn
// whose grant is scoped to that repository finds the session's record in
// the history, cites it, and the citation resolves from the record within
// the scope, its read in the turn's log as the record's and its URL the
// record's page. A record in another repository is in no answer of the
// turn's, and a brief citing it is refused as any ref outside the grant is.
func TestABriefCitesARecordOnlySessionAfterARestart(t *testing.T) {
	b := brakemanEnv(t, nil)
	inside, outside := filepath.Join(b.root, "inside"), filepath.Join(b.root, "outside")
	gitInit(t, inside, true)
	gitInit(t, outside, true)
	launch := func(dir, name string) string {
		t.Helper()
		resp, out := b.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": dir, "name": name})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("launch in %s: %d %v", dir, resp.StatusCode, out)
		}
		id := out["id"].(string)
		b.stopAndWait(id)
		endedRecord(t, b.testEnv, id)
		return id
	}
	in := launch(inside, "turnstile inside")
	out := launch(outside, "turnstile outside")
	rec, _, err := b.srv.sessionRecords.Get(in)
	if err != nil || rec.Repo == "" {
		t.Fatalf("the record's repository: %+v %v", rec, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b.srv.Shutdown(ctx)
	again := &bkEnv{testEnv: b.restart()}
	again.desk = again.srv.brakeman
	again.srv.limiter = newRateLimiter(1e6, 1e6)
	for _, id := range []string{in, out} {
		if _, ok := again.srv.registry.Get(id); ok {
			t.Fatalf("%s is listed after the restart", id)
		}
	}
	histSettle(t, again.testEnv)
	if keys := hsKeys(hsSearch(t, again.testEnv, "turnstile")); !slices.Contains(keys, "session:"+in) || !slices.Contains(keys, "session:"+out) {
		t.Fatalf("the admin's search finds both records: %v", keys)
	}

	f := &obsFixture{bkEnv: again}
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"repos": []string{rec.Repo}} })
	got := f.expectRead(t, searchPath("turnstile"), http.StatusOK, "")
	if refs := historyRefs(got); !slices.Equal(refs, []string{"session:" + in}) {
		t.Fatalf("the turn's history: %v", refs)
	}
	e := historyEntryOf(got, "session:"+in)
	if e["url"] != "/history/sessions/"+in || availOf(e)["state"] != availRecord || e["key"] != nil {
		t.Fatalf("the record's entry: %v", e)
	}
	if rd, ok := f.readOf(t, "session:"+in); !ok || rd.State != availRecord || rd.Class != brakeman.ClassSessions || rd.Rev != "" {
		t.Fatalf("the read logged: %+v %v", rd, ok)
	}
	if _, ok := f.readOf(t, "session:"+out); ok {
		t.Fatal("the other repository's record was read")
	}

	status, refused := f.submit(t, f.sid, f.tok, "outside", factBrief("session:"+out))
	if status != http.StatusUnprocessableEntity || errorCode(refused) != "bad_refs" || !strings.Contains(fmt.Sprint(refused), "outside the request's scope") {
		t.Fatalf("a brief citing the other repository's record: %d %v", status, refused)
	}
	if status, kept := f.submit(t, f.sid, f.tok, "inside", factBrief("session:"+in)); status != http.StatusCreated {
		t.Fatalf("the brief: %d %v", status, kept)
	}
	_, res, err := f.desk.st.Brief(f.req)
	if err != nil || len(res) != 1 {
		t.Fatalf("the resolutions: %v %v", res, err)
	}
	r := res[0]
	page := "/history/sessions/" + in
	if r.Ref.String() != "session:"+in || r.Now.State != availRecord || r.Now.URL != page || r.Seen.State != availRecord || r.Seen.URL != page {
		t.Fatalf("the citation: %+v", r)
	}
}

// What a turn reads of the history keeps to its grant, each refusal as
// the grant's other reads refuse: a run outside the scope, a class not
// visible (a check's receipt with logs off, whose log its text is), a
// project outside it, sessions off; Brakeman's own runs and sessions
// left out whatever the line names; a turn
// that sees none of the classes refused at once; the admin's answer
// holds them all.
func TestBrakemansHistoryKeepsToTheGrant(t *testing.T) {
	f := newObsFixture(t)
	check, otherCheck := receipts(t, f)
	admin := hsSearch(t, f.testEnv, "TestTurnstile")
	var adminRefs []string
	for _, x := range admin["entries"].([]any) {
		adminRefs = append(adminRefs, x.(map[string]any)["source"].(map[string]any)["ref"].(string))
	}
	if !slices.Contains(adminRefs, check) || !slices.Contains(adminRefs, otherCheck) {
		t.Fatalf("the admin's search: %v", adminRefs)
	}
	f.turn(t, nil)
	for _, c := range []struct {
		name      string
		mod       func(c map[string]any)
		line      string
		want, not []string
		status    int
		code      string
	}{
		{name: "everything", line: "TestTurnstile", want: []string{check, otherCheck}},
		{name: "a scope of one crew", mod: func(c map[string]any) { c["scope"] = map[string]any{"crews": []string{"team"}} },
			line: "TestTurnstile", want: []string{check}, not: []string{otherCheck}},
		{name: "logs not visible", mod: func(c map[string]any) { c["visibility"].(map[string]any)["logs"] = false },
			line: "TestTurnstile", not: []string{check, otherCheck}},
		{name: "a ticket in its project", mod: func(c map[string]any) { c["scope"] = map[string]any{"projects": []string{"CND"}} },
			line: "build fails", want: []string{"ticket:" + f.ticket}},
		{name: "a ticket outside its project", mod: func(c map[string]any) { c["scope"] = map[string]any{"projects": []string{"OTH"}} },
			line: "build fails", not: []string{"ticket:" + f.ticket}},
		{name: "dispatch not visible", mod: func(c map[string]any) { c["visibility"].(map[string]any)["dispatch"] = false },
			line: "build fails", not: []string{"ticket:" + f.ticket}},
		{name: "nothing visible", mod: func(c map[string]any) {
			v := c["visibility"].(map[string]any)
			for _, k := range []string{"runs", "logs", "dispatch", "sessions", "touched"} {
				v[k] = false
			}
		}, line: "TestTurnstile", status: http.StatusForbidden, code: "not_visible"},
		{name: "a page past its bound", line: "TestIt limit", status: http.StatusBadRequest, code: "bad_query"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f.again(t, func(cfg map[string]any) {
				cfg["scope"] = map[string]any{}
				cfg["visibility"] = map[string]any{"runs": true, "logs": true, "dispatch": true, "sessions": true, "touched": true}
				if c.mod != nil {
					c.mod(cfg)
				}
			})
			path := searchPath(c.line)
			if c.name == "a page past its bound" {
				path = searchPath("TestTurnstile", "limit", fmt.Sprint(maxHistoryTurnPage+1))
			}
			status := c.status
			if status == 0 {
				status = http.StatusOK
			}
			out := f.expectRead(t, path, status, c.code)
			if status != http.StatusOK {
				return
			}
			refs := historyRefs(out)
			for _, w := range c.want {
				if !slices.Contains(refs, w) {
					t.Errorf("%s is not served: %v", w, refs)
				}
			}
			for _, n := range c.not {
				if slices.Contains(refs, n) {
					t.Errorf("%s is served: %v", n, refs)
				}
				if _, ok := f.readOf(t, n); ok {
					t.Errorf("%s is in the read log", n)
				}
			}
			for _, k := range []string{"total", "totalCapped"} {
				if out[k] != nil {
					t.Errorf("the answer counts the index's rows: %s %v", k, out[k])
				}
			}
			for _, x := range out["entries"].([]any) {
				m := x.(map[string]any)
				if m["key"] != nil || m["repeats"] != nil {
					t.Errorf("an entry names the index's rows: %v", m)
				}
				if strings.HasPrefix(m["ref"].(string), "run:brakeman-") {
					t.Errorf("Brakeman's own run: %v", m["ref"])
				}
			}
			if ix, _ := out["indexing"].(map[string]any); len(ix) != 1 {
				t.Errorf("the indexing says more than its state: %v", ix)
			}
		})
	}
	// The turns above are Brakeman's own runs and sessions in the
	// history now, which the admin finds by their crew: a turn reads none
	// of them, whatever its line names (an earlier turn's reports were
	// read under its own grant).
	t.Run("Brakeman's own", func(t *testing.T) {
		f.again(t, nil)
		histSettle(t, f.testEnv)
		admin := hsSearch(t, f.testEnv, "crew:brakeman")
		if keys := hsKeys(admin); !slices.ContainsFunc(keys, func(k string) bool { return strings.HasPrefix(k, "session:") }) {
			t.Fatalf("the admin finds no turn's session: %v", keys)
		}
		if refs := historyRefs(f.expectRead(t, searchPath("crew:brakeman"), http.StatusOK, "")); len(refs) != 0 {
			t.Fatalf("a turn reads Brakeman's own: %v", refs)
		}
	})
}

// A check's receipt read through the history is in the read log in the
// runs' and the logs' classes (its text is its log), with no revision;
// its links are those within the grant.
func TestAHistoryReadIsNotedInItsClasses(t *testing.T) {
	f := newObsFixture(t)
	ref, _ := receipts(t, f)
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"crews": []string{"team"}} })
	out := f.expectRead(t, searchPath("TestTurnstile kind:check"), http.StatusOK, "")
	e := historyEntryOf(out, ref)
	if e == nil {
		t.Fatalf("the receipt: %v", historyRefs(out))
	}
	classes := map[string]bool{}
	for _, rd := range f.readLog(t).Reads {
		if rd.Ref.String() == ref {
			classes[rd.Class] = true
			if rd.Rev != "" {
				t.Errorf("a history read with a revision: %+v", rd)
			}
		}
	}
	if !classes[brakeman.ClassRuns] || !classes[brakeman.ClassLogs] {
		t.Fatalf("the receipt's classes noted: %v", classes)
	}
	for _, l := range e["links"].([]any) {
		if r := l.(map[string]any)["ref"].(string); strings.Contains(r, f.other) {
			t.Fatalf("a link outside the scope: %v", r)
		}
	}
}

// Every kind the index holds names the classes a turn must see to read
// its entries, and no other kind is named: a kind added to the index is
// served to no turn until it is given its classes here.
func TestEveryHistoryKindHasItsClasses(t *testing.T) {
	kinds := history.Kinds()
	for _, k := range kinds {
		classes, ok := historyReadClasses[k]
		if !ok {
			t.Errorf("%s: no classes", k)
		}
		for _, c := range classes {
			if !slices.Contains(brakeman.Classes, c) {
				t.Errorf("%s: no class %q", k, c)
			}
		}
	}
	for k := range historyReadClasses {
		if !slices.Contains(kinds, k) {
			t.Errorf("%s: no kind of the index's", k)
		}
	}
}

// A ref's record: a session's record its own scope; a run's part its
// run's; a ticket's comment and plan revision their ticket's; an entry
// whose ref Brakeman's grammar does not name is of its ticket or its
// decision, and cites that.
func TestAnEntryIsOfItsRecord(t *testing.T) {
	for _, c := range []struct {
		e            history.Entry
		scope, cited string
	}{
		{history.Entry{Kind: "session.chat", Ref: "session:0123456789abcdef"}, "session:0123456789abcdef", "session:0123456789abcdef"},
		{history.Entry{Kind: "session.question", Ref: "question:0123456789abcdef/fedcba9876543210"}, "session:0123456789abcdef", "question:0123456789abcdef/fedcba9876543210"},
		{history.Entry{Kind: "check", Ref: "check:team-0000aaaa/build/1/test/1"}, "run:team-0000aaaa", "check:team-0000aaaa/build/1/test/1"},
		{history.Entry{Kind: "comment", Ref: "comment:CND-1/0123456789abcdef"}, "ticket:CND-1", "comment:CND-1/0123456789abcdef"},
		{history.Entry{Kind: "decision.response", Ref: "decision:dr-3/1"}, "decision:dr-3", "decision:dr-3"},
		{history.Entry{Kind: "review.batch", Key: "ticket:CND-1#review/r1", Ref: "ticket:CND-1#review/r1", Ticket: "CND-1"}, "ticket:CND-1", "ticket:CND-1"},
		{history.Entry{Kind: "brief.item", Ref: "brief:br-4/1"}, "brief:br-4/1", "brief:br-4/1"},
	} {
		scope, cited, ok := entryScope(c.e)
		if !ok || scope.String() != c.scope || cited.String() != c.cited {
			t.Errorf("%s: %v %v %v, want %s %s", c.e.Ref, scope, cited, ok, c.scope, c.cited)
		}
	}
	if _, _, ok := entryScope(history.Entry{Kind: "review.batch", Ref: "ticket:CND-1#review/r1"}); ok {
		t.Error("an entry of no record a turn can be admitted to")
	}
}

// A turn's history read is bounded and reads on: at most limit entries
// and maxObserveReply bytes a page, next reading on with nothing given
// twice and nothing skipped (a search of filters alone, newest first: a
// search of words is ranked, and a source indexed between two pages may
// move a row's score, as the plan says of the search's own pages); and a
// read whose rows the grant admits none of looks at maxHistoryTurnScan of
// them and answers next, never the whole index at once.
func TestAHistoryReadIsBoundedAndReadsOn(t *testing.T) {
	f := newObsFixture(t)
	const n = maxHistoryTurnScan + 50
	for i := range n {
		doneOf(t)(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), f.ticket, fmt.Sprintf("turnstile note %d", i)))
	}
	histSettle(t, f.testEnv)
	// The ticket's own comment and the n added.
	until(t, "the comments indexed", func() bool {
		total, _ := hsSearch(t, f.testEnv, "kind:comment project:CND")["total"].(float64)
		return int(total) == n+1
	})
	f.turn(t, func(c map[string]any) { c["budget"].(map[string]any)["readsPerTurn"] = brakeman.MaxReadsPerTurn })
	seen := map[string]bool{}
	for after := ""; ; {
		extra := []string{"limit", fmt.Sprint(maxHistoryTurnPage)}
		if after != "" {
			extra = append(extra, "after", after)
		}
		resp, body := f.readRaw(t, searchPath("kind:comment project:CND", extra...))
		if resp.StatusCode != http.StatusOK || len(body) > maxObserveReply {
			t.Fatalf("a page: %d, %d bytes", resp.StatusCode, len(body))
		}
		_, out := f.read(t, searchPath("kind:comment project:CND", extra...))
		refs := historyRefs(out)
		if len(refs) > maxHistoryTurnPage {
			t.Fatalf("a page of %d", len(refs))
		}
		for _, r := range refs {
			if seen[r] {
				t.Fatalf("%s given twice", r)
			}
			seen[r] = true
		}
		next, _ := out["next"].(string)
		if next == "" {
			break
		}
		after = next
	}
	if len(seen) != n+1 {
		t.Fatalf("%d comments read, want %d", len(seen), n+1)
	}
	f.again(t, func(c map[string]any) { c["scope"] = map[string]any{"projects": []string{"OTH"}} })
	out := f.expectRead(t, searchPath("kind:comment project:CND"), http.StatusOK, "")
	if refs := historyRefs(out); len(refs) != 0 || out["next"] == nil {
		t.Fatalf("a read the grant admits none of: %v, next %v", refs, out["next"])
	}
}
