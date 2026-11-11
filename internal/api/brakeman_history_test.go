package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/history"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/session"
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

// agentReview adds to the fixture's team run a review task whose attempt,
// an agent reviewer's, submitted a finding, and answers the attempt's ref
// once the index holds the finding (kept by the attempt, in the ledger).
func agentReview(t *testing.T, f *obsFixture) string {
	t.Helper()
	res, _ := json.Marshal(crew.Result{Verdict: crew.VerdictNeedsChanges, Summary: "Not yet.", Findings: []crew.Finding{{Severity: "major", Path: "a.go", Line: 3, Message: "a nil map"}}})
	histTx(t, f.srv.ledger, f.run, func(tx *ledger.Tx) error {
		if err := tx.PutTask(ledger.TaskRow{ID: "review", Member: "lead", Kind: crew.TaskReview, State: ledger.TaskSucceeded, Attempts: 1}); err != nil {
			return err
		}
		rv := histAttempt("review", 1, ledger.AttemptSucceeded, "", commitID(1), "")
		rv.Result, rv.ClosedAt = string(res), histNow.Add(4*time.Minute)
		return tx.PutAttempt(rv)
	})
	ref := attemptKey(f.run, "review", 1)
	ix := histSettle(t, f.testEnv)
	until(t, "the agent reviewer's finding indexed", func() bool {
		_, err := ix.Entry(t.Context(), ref+"#finding/1")
		return err == nil
	})
	return ref
}

// linkedRun adds a run of the team crew, launched from the fixture's
// ticket and resumed from the other crew's run, and answers its ref once
// the index holds it with both links.
func linkedRun(t *testing.T, f *obsFixture) string {
	t.Helper()
	const run = "team-0000cccc"
	crewJSON, _ := json.Marshal(map[string]string{"id": "team", "cwd": f.root})
	histTx(t, f.srv.ledger, run, func(tx *ledger.Tx) error {
		return tx.PutRun(ledger.RunRow{ID: run, CrewID: "team", Crew: string(crewJSON), State: ledger.RunStopped, CreatedAt: tx.Now()})
	})
	if err := f.srv.records.Save(crew.Run{ID: run, CrewID: "team", Name: "team", Cwd: f.root, State: crew.RunStopped, ResumedFrom: f.other,
		Ticket: &crew.RunTicket{Key: f.ticket}, Managed: &crew.Managed{Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	histSettle(t, f.testEnv)
	until(t, "the run indexed with its links", func() bool {
		e := hsEntry(hsSearch(t, f.testEnv, "kind:run run:"+run), "run:"+run)
		refs := linkRefs(e)
		return slices.Contains(refs, "ticket:"+f.ticket) && slices.Contains(refs, "run:"+f.other)
	})
	return "run:" + run
}

// linkRefs is the ref of each link of a served entry.
func linkRefs(e map[string]any) []string {
	refs := []string{}
	ls, _ := e["links"].([]any)
	for _, l := range ls {
		refs = append(refs, l.(map[string]any)["ref"].(string))
	}
	return refs
}

// readClasses is each class the turn's log reads ref in.
func readClasses(t *testing.T, f *obsFixture, ref string) []string {
	t.Helper()
	var out []string
	for _, rd := range f.readLog(t).Reads {
		if rd.Ref.String() == ref && !slices.Contains(out, rd.Class) {
			out = append(out, rd.Class)
		}
	}
	return out
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

// A journal the server keeps and could not open is not an absent record:
// a session the registry does not list is answered store_unavailable, as
// an unopened ledger or Dispatch store is (TestAnUnopenedStoreIsAnError),
// never resolved to nothing or to deleted; a server that keeps no journal
// (a switchyard) holds no record of it, which is no error.
func TestAnUnopenedJournalIsAnError(t *testing.T) {
	id := session.NewID()
	e := newTestEnv(t, func(cfg *config.Config) {
		cfg.DataDir = filepath.Join(t.TempDir(), "data")
		if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		// The journal's folder is a file: the journal cannot be opened.
		if err := os.WriteFile(filepath.Join(cfg.DataDir, journal.Dir), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if e.srv.store == nil || e.srv.sessionRecords != nil {
		t.Fatal("the journal opened over a file, or the server keeps no data directory")
	}
	cfg := brakeman.DefaultConfig(e.root)
	_, err := e.srv.resolveBriefRefs(t.Context(), nil, brakeman.Request{ID: "br-1", Config: brakeman.SnapshotOf(cfg)},
		brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s", Items: []brakeman.Item{panel(ref(t, "session:"+id))}}, brakeman.ReadLog{}, cfg)
	var unopened *errRefStore
	var bad *badRefsError
	if !errors.As(err, &unopened) || errors.As(err, &bad) {
		t.Fatalf("a session ref with the journal unopened: %v", err)
	}
	if _, ok, err := (&refResolver{s: e.srv}).recordRef(id); ok || !errors.As(err, &unopened) {
		t.Fatalf("the record with the journal unopened: %v %v", ok, err)
	}

	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	if yard.srv.sessionRecords != nil {
		t.Fatal("a switchyard keeps a journal")
	}
	if _, ok, err := (&refResolver{s: yard.srv}).recordRef(id); ok || err != nil {
		t.Fatalf("the record on a server that keeps no journal: %v %v", ok, err)
	}
}

// What a turn reads of the history keeps to its grant, each refusal as
// the grant's other reads refuse: a run outside the scope, a class not
// visible (a check's receipt with logs off, whose log its text is), a
// project outside it, sessions off; a review's entries, an agent
// reviewer's among them, served under no grant; Brakeman's own runs and
// sessions left out whatever the line names; a turn that sees none of the
// classes refused at once; the admin's answer holds them all. No entry
// carries links.
func TestBrakemansHistoryKeepsToTheGrant(t *testing.T) {
	f := newObsFixture(t)
	check, otherCheck := receipts(t, f)
	review := agentReview(t, f)
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
		// A review's entries are read through the review routes: never
		// served here, whatever is visible.
		{name: "an agent reviewer's finding", line: "kind:review.finding,review.verdict", not: []string{review}},
		{name: "an agent reviewer's finding, dispatch not visible", mod: func(c map[string]any) { c["visibility"].(map[string]any)["dispatch"] = false },
			line: "kind:review.finding,review.verdict", not: []string{review}},
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
				if _, ok := m["links"]; ok {
					t.Errorf("an entry carries links: %v", m)
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
// runs' and the logs' classes (its text is its log), with no revision.
// (Its links are TestALinkOrAnchorOutsideTheGrantIsLeftOut's.)
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
}

// turnScope is the history's admission for the fixture's turn as it
// reads now.
func turnScope(t *testing.T, f *obsFixture) *historyScope {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/brakeman/history", nil)
	r.Header.Set(brakemanSessionHeader, f.sid)
	r.Header.Set("Authorization", "Bearer "+f.tok)
	tp, refused := f.srv.turnPrincipalOf(r)
	if refused != nil || tp == nil {
		t.Fatalf("the turn's principal: %v", refused)
	}
	return &historyScope{o: f.srv.newObserver(tp), done: map[brakeman.Ref]historyAdmission{}, noted: map[brakeman.Read]bool{}}
}

// A turn's entry is its own record's and carries no links: a ticket that
// runs were launched from (the admin's entry links them) is served to a
// turn of every scope with no links and nothing of those runs read, and
// to a turn scoped to a crew not at all, since its rows name no run of
// the crew. An anchor is served only where its record resolves within the
// grant, and read there.
func TestATurnsEntryCarriesNoLinks(t *testing.T) {
	f := newObsFixture(t)
	run := linkedRun(t, f)
	ticket := "ticket:" + f.ticket
	if admin := hsEntry(hsSearch(t, f.testEnv, "build fails"), ticket); !slices.Contains(linkRefs(admin), run) {
		t.Fatalf("the admin's ticket: %v", admin)
	}
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{} })
	out := f.expectRead(t, searchPath("build fails"), http.StatusOK, "")
	e := historyEntryOf(out, ticket)
	if e == nil {
		t.Fatalf("the ticket: %v", historyRefs(out))
	}
	if _, ok := e["links"]; ok {
		t.Fatalf("the ticket carries links: %v", e)
	}
	for _, r := range []string{run, "run:" + f.other} {
		if _, ok := f.readOf(t, r); ok {
			t.Fatalf("%s read through the ticket", r)
		}
	}
	f.again(t, func(c map[string]any) { c["scope"] = map[string]any{"crews": []string{"team"}} })
	if refs := historyRefs(f.expectRead(t, searchPath("build fails"), http.StatusOK, "")); slices.Contains(refs, ticket) {
		t.Fatalf("a ticket served to a turn scoped to a crew: %v", refs)
	}

	// An anchor outside the scope is left out, one inside it kept and read.
	hs := turnScope(t, f)
	own, other := attemptKey(f.run, "build", 1), attemptKey(f.other, "build", 1)
	entry := history.Entry{Kind: "attempt", Key: own, Ref: own}
	scope, cited, res, ok, err := hs.admit(t.Context(), entry)
	if err != nil || !ok {
		t.Fatalf("the team's attempt: %v %v", ok, err)
	}
	for _, c := range []struct {
		anchor string
		kept   bool
	}{{"run:" + f.other, false}, {"run:" + f.run, true}} {
		served := servedEntry{Kind: "attempt", Anchor: servedAnchor{Ref: c.anchor, Label: anchorLabel(c.anchor)}}
		te, reads, err := hs.turnEntry(t.Context(), served, entry, scope, cited, res)
		if err != nil {
			t.Fatal(err)
		}
		if (te.Anchor != nil) != c.kept {
			t.Errorf("the anchor %s: %+v", c.anchor, te.Anchor)
		}
		for _, rd := range reads {
			if rd.Ref.String() == "run:"+f.other || rd.Ref.String() == other {
				t.Errorf("a read of the other run: %+v", rd)
			}
		}
	}
}

// A file's ref names its session and the file: an anchor of one is served
// only to a grant that sees touched files as well as sessions.
func TestAFileAnchorNeedsTouched(t *testing.T) {
	f := newObsFixture(t)
	file := "file:" + f.plain + "/" + filepath.Join(f.root, "plan.go")
	if _, err := brakeman.ParseRef(file); err != nil {
		t.Fatalf("the file's ref: %v", err)
	}
	for _, touched := range []bool{false, true} {
		vis := func(c map[string]any) {
			c["scope"] = map[string]any{}
			c["visibility"] = map[string]any{"runs": true, "logs": true, "dispatch": true, "sessions": true, "touched": touched}
		}
		if f.sid == "" {
			f.turn(t, vis)
		} else {
			f.again(t, vis)
		}
		reads, ok, err := turnScope(t, f).refReads(t.Context(), file)
		if err != nil || ok != touched {
			t.Fatalf("touched %v: the file's anchor admitted %v, %v", touched, ok, err)
		}
		if ok && !slices.ContainsFunc(reads, func(rd brakeman.Read) bool { return rd.Class == brakeman.ClassTouched }) {
			t.Fatalf("the file's anchor read in %+v", reads)
		}
	}
}

// A plain session in a project's repository, with no ticket of it: a turn
// that sees Dispatch is served the project's key on the session's entry
// and the read is noted in Dispatch's class too, so a brief citing the
// session carries Dispatch in its footprint and a later turn with Dispatch
// off is given it withheld; that turn is served the session without the
// key, its read in the sessions' class alone, and may not filter by
// project or ticket.
func TestAProjectKeyIsReadWithDispatch(t *testing.T) {
	b := brakemanEnv(t, nil)
	dir := filepath.Join(b.root, "repo")
	gitInit(t, dir, true)
	resp, out := b.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": dir, "name": "turnstile project"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	b.stopAndWait(id)
	endedRecord(t, b.testEnv, id)
	histProject(t, b.testEnv, "PRJ", dir)
	sess := "session:" + id
	histSettle(t, b.testEnv)
	until(t, "the session's project indexed", func() bool {
		p, _ := hsEntry(hsSearch(t, b.testEnv, "turnstile"), sess)["project"].(map[string]any)
		return p["key"] == "PRJ"
	})
	f := &obsFixture{bkEnv: b}
	vis := func(dispatch bool) func(c map[string]any) {
		return func(c map[string]any) {
			c["scope"] = map[string]any{}
			c["visibility"] = map[string]any{"runs": true, "logs": true, "dispatch": dispatch, "sessions": true, "touched": true}
		}
	}
	f.turn(t, vis(true))
	e := historyEntryOf(f.expectRead(t, searchPath("turnstile"), http.StatusOK, ""), sess)
	if p, _ := e["project"].(map[string]any); p["key"] != "PRJ" {
		t.Fatalf("the session with Dispatch on: %v", e)
	}
	if got := readClasses(t, f, sess); !slices.Contains(got, brakeman.ClassDispatch) || !slices.Contains(got, brakeman.ClassSessions) {
		t.Fatalf("the session read in %v", got)
	}
	if status, kept := f.submit(t, f.sid, f.tok, "project", factBrief(sess)); status != http.StatusCreated {
		t.Fatalf("the brief: %d %v", status, kept)
	}
	early := f.req
	if r, _ := f.desk.st.Get(early); r.Footprint == nil || !slices.Contains(r.Footprint.Classes, brakeman.ClassDispatch) {
		t.Fatalf("the brief's footprint: %+v", r.Footprint)
	}
	f.again(t, vis(false))
	if got := f.briefsOf(t)[early]; got == nil || whole(got) {
		t.Fatalf("the brief with Dispatch off: %v", got)
	}
	e = historyEntryOf(f.expectRead(t, searchPath("turnstile"), http.StatusOK, ""), sess)
	if p, _ := e["project"].(map[string]any); e == nil || p["key"] != nil {
		t.Fatalf("the session with Dispatch off: %v", e)
	}
	if got := readClasses(t, f, sess); !slices.Equal(got, []string{brakeman.ClassSessions}) {
		t.Fatalf("the session read with Dispatch off in %v", got)
	}
	for _, line := range []string{"turnstile ticket:PRJ-1", "turnstile project:PRJ"} {
		f.expectRead(t, searchPath(line), http.StatusForbidden, "not_visible")
	}
}

// An earlier brief that keeps no footprint (a request kept before
// footprints were) is admitted on every class its configuration had on,
// and a read of it records the same classes: a turn reads its item
// through the history and keeps a brief citing it, whose footprint then
// holds logs, so a later turn with logs off is served neither brief; given
// whole, the earlier brief is read in those classes too.
func TestABriefWithoutAFootprintCarriesItsClasses(t *testing.T) {
	f := newObsFixture(t)
	all := []string{brakeman.ClassRuns, brakeman.ClassLogs, brakeman.ClassDispatch, brakeman.ClassSessions, brakeman.ClassTouched}
	vis := func(c map[string]any) {
		c["scope"] = map[string]any{}
		c["visibility"] = map[string]any{"runs": true, "logs": true, "dispatch": true, "sessions": true, "touched": true}
	}
	early := f.keepEarlier(t, brakeman.DefaultConfig(f.root), []brakeman.Item{fact("TestTurnstile failed in build", ref(t, "run:"+f.run))}, "TestTurnstile failed.",
		[]brakeman.Read{{Ref: ref(t, "run:"+f.run), Class: brakeman.ClassRuns, At: time.Now()}})
	// Kept as a request from before footprints: none.
	path := filepath.Join(f.srv.store.Dir(), brakeman.Dir, brakeman.RequestsDir, early+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "footprint")
	if raw, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".new", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	if r, err := f.desk.st.Get(early); err != nil || r.Footprint != nil || !r.Config.Visibility.Logs {
		t.Fatalf("the earlier request: %+v %v", r.Footprint, err)
	}
	indexed := func(item string) {
		t.Helper()
		ix := histSettle(t, f.testEnv)
		until(t, item+" indexed", func() bool {
			_, err := ix.Entry(t.Context(), item)
			return err == nil
		})
	}
	item := hBriefItemRef(early, 0)
	indexed(item)
	classesOf := func(ref string) []string {
		t.Helper()
		got := readClasses(t, f, ref)
		slices.Sort(got)
		return got
	}
	sorted := slices.Sorted(slices.Values(all))

	f.turn(t, vis)
	if refs := historyRefs(f.expectRead(t, searchPath("kind:brief.item"), http.StatusOK, "")); !slices.Contains(refs, item) {
		t.Fatalf("the earlier brief's item: %v", refs)
	}
	if got := classesOf(item); !slices.Equal(got, sorted) {
		t.Fatalf("the earlier brief's item read in %v, want %v", got, sorted)
	}
	f.submitFact(t, item)
	copied := f.req
	if r, _ := f.desk.st.Get(copied); r.Footprint == nil || !slices.Contains(r.Footprint.Classes, brakeman.ClassLogs) {
		t.Fatalf("the brief citing it: footprint %+v", r.Footprint)
	}
	copiedItem := hBriefItemRef(copied, 0)
	indexed(copiedItem)

	// Logs on: both are served, and the earlier brief given whole is read
	// in its configuration's classes.
	f.again(t, vis)
	if refs := historyRefs(f.expectRead(t, searchPath("kind:brief.item"), http.StatusOK, "")); !slices.Contains(refs, item) || !slices.Contains(refs, copiedItem) {
		t.Fatalf("both briefs with logs on: %v", refs)
	}
	f.again(t, vis)
	if got := f.briefsOf(t)[early]; !whole(got) {
		t.Fatalf("the earlier brief with logs on: %v", got)
	}
	if got := classesOf(item); !slices.Equal(got, sorted) {
		t.Fatalf("the earlier brief given whole read in %v, want %v", got, sorted)
	}

	// Logs off: neither.
	f.again(t, func(c map[string]any) {
		vis(c)
		c["visibility"].(map[string]any)["logs"] = false
	})
	if refs := historyRefs(f.expectRead(t, searchPath("kind:brief.item"), http.StatusOK, "")); slices.Contains(refs, item) || slices.Contains(refs, copiedItem) {
		t.Fatalf("a brief with logs off: %v", refs)
	}
	if got := f.briefsOf(t)[copied]; got == nil || whole(got) {
		t.Fatalf("the brief citing it with logs off: %v", got)
	}
}

// Every kind the index holds is decided once: served, with the classes a
// turn must see to read its entries and the record whose columns place its
// rows, or withheld, with why; no other kind is named. A kind added to the
// index is served to no turn until it is decided here.
func TestEveryHistoryKindIsDecided(t *testing.T) {
	kinds := history.Kinds()
	places := []string{placeSession, placeRun, placeTicket, placeBrief}
	for _, k := range kinds {
		hk, served := historyKinds[k]
		why, withheld := historyWithheld[k]
		switch {
		case served == withheld:
			t.Errorf("%s: served %v, withheld %v", k, served, withheld)
		case withheld && why == "":
			t.Errorf("%s: withheld with no why", k)
		case served && !slices.Contains(places, hk.place):
			t.Errorf("%s: placed by %q", k, hk.place)
		}
		for _, c := range hk.classes {
			if !slices.Contains(brakeman.Classes, c) {
				t.Errorf("%s: no class %q", k, c)
			}
		}
		if served && hk.place != placeBrief && len(hk.classes) == 0 {
			t.Errorf("%s: served with no class", k)
		}
	}
	for _, m := range []map[string]bool{kindSet(historyKinds), kindSet(historyWithheld)} {
		for k := range m {
			if !slices.Contains(kinds, k) {
				t.Errorf("%s: no kind of the index's", k)
			}
		}
	}
	for k := range historyKinds {
		if strings.HasPrefix(k, "review.") || k == "run" || k == "task" {
			t.Errorf("%s is served", k)
		}
	}
}

// kindSet is a map's keys as a set.
func kindSet[V any](m map[string]V) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// Every operation an act may be granted names the kinds whose text holds
// words it gives, and each is kept from every turn: withheld, a kind
// whose rows an act makes are left out by their actor, or a decision's,
// whose plan's gates are left out by their anchor.
func TestEveryActsWordsAreKeptFromTurns(t *testing.T) {
	if len(historyActWords) != len(brakeman.Grantable) {
		t.Fatalf("%d acts decided, %d grantable", len(historyActWords), len(brakeman.Grantable))
	}
	for _, op := range brakeman.Grantable {
		kinds, ok := historyActWords[op]
		if !ok {
			t.Errorf("%s: undecided", op)
		}
		for _, k := range kinds {
			_, withheld := historyWithheld[k]
			hk, served := historyKinds[k]
			switch {
			case !slices.Contains(history.Kinds(), k):
				t.Errorf("%s: %s is no kind of the index's", op, k)
			case served && hk.place != placeTicket && !slices.Contains(historyDecisionKinds, k):
				t.Errorf("%s: %s is served and not left out by its actor or its anchor", op, k)
			case !served && !withheld:
				t.Errorf("%s: %s undecided", op, k)
			}
		}
	}
	if !slices.Contains(historyActors, brakemanActor) || !slices.Contains(historyActors, brakemanBy) {
		t.Fatalf("an act's actors: %v", historyActors)
	}
}

// Every field of a turn's entry is decided (historyFields names it, or
// the struct it is in), and the table names no field the entry lacks; a
// project's key and a ticket are served and read only with Dispatch.
func TestEveryTurnFieldIsDecided(t *testing.T) {
	var paths []string
	var walk func(rt reflect.Type, prefix string)
	walk = func(rt reflect.Type, prefix string) {
		for i := range rt.NumField() {
			name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
			path := prefix + name
			paths = append(paths, path)
			if _, ok := historyFields[path]; !ok {
				if ft := rt.Field(i).Type; ft.Kind() == reflect.Struct && hasFieldsUnder(path) {
					walk(ft, path+".")
					continue
				}
				t.Errorf("%s: undecided", path)
			}
		}
	}
	walk(reflect.TypeOf(turnHistoryEntry{}), "")
	for k := range historyFields {
		if !slices.Contains(paths, k) {
			t.Errorf("%s: no field of the entry's", k)
		}
	}
	for _, sees := range []bool{false, true} {
		te := turnHistoryEntry{Ref: "attempt:team-0000aaaa/build/1", Ticket: "CND-1", Project: servedProject{Key: "CND", Repo: "/r/.git", Tree: "/r"}}
		read := emitFields(&te, func(c string) bool { return c != brakeman.ClassDispatch || sees })
		kept := te.Ticket == "CND-1" && te.Project.Key == "CND"
		if kept != sees || te.Project.Tree != "/r" || te.Ref == "" || slices.Contains(read, brakeman.ClassDispatch) != sees {
			t.Errorf("Dispatch seen %v: %+v, read %v", sees, te, read)
		}
	}
}

// A ref's record: a session's record its own scope; a run's part its
// run's; a ticket's comment and plan revision their ticket's; a decision's
// response, whose ref Brakeman's grammar does not name, is of its
// decision and cites that; any other such entry is of no record.
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
		{history.Entry{Kind: "delivery.verified", Key: "ticket:CND-1#delivery/l1", Ref: "ticket:CND-1", Ticket: "CND-1"}, "ticket:CND-1", "ticket:CND-1"},
		{history.Entry{Kind: "brief.item", Ref: "brief:br-4/1"}, "brief:br-4/1", "brief:br-4/1"},
	} {
		scope, cited, ok := entryScope(c.e)
		if !ok || scope.String() != c.scope || cited.String() != c.cited {
			t.Errorf("%s: %v %v %v, want %s %s", c.e.Ref, scope, cited, ok, c.scope, c.cited)
		}
	}
	for _, e := range []history.Entry{{Kind: "review.batch", Ref: "ticket:CND-1#review/r1"}, {Kind: "review.batch", Ref: "ticket:CND-1#review/r1", Ticket: "CND-1"}} {
		if _, _, ok := entryScope(e); ok {
			t.Errorf("an entry of no record a turn can be admitted to: %+v", e)
		}
	}
}

// A turn's history read is bounded and reads on: at most limit entries
// and maxObserveReply bytes a page, next reading on with nothing given
// twice and nothing skipped, newest first; and it reads only rows its
// grant covers: a comment of OTH, under 200 newer comments of CND, is
// served to a turn scoped to OTH by a read of every comment as by its own
// line, and still once more of CND's are indexed.
func TestAHistoryReadIsBoundedAndReadsOn(t *testing.T) {
	f := newObsFixture(t)
	beyond := "comment:" + f.otherTicket + "/" + doneOf(t)(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), f.otherTicket, "turnstile beyond")).ID
	const n = 200
	for i := range n {
		doneOf(t)(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), f.ticket, fmt.Sprintf("turnstile note %d", i)))
	}
	histSettle(t, f.testEnv)
	// The ticket's own comment and the n added, and the one of OTH.
	until(t, "the comments indexed", func() bool {
		total, _ := hsSearch(t, f.testEnv, "kind:comment project:CND")["total"].(float64)
		_, err := f.srv.history.index().Entry(t.Context(), beyond)
		return int(total) == n+1 && err == nil
	})
	// The admin's read of every comment, newest first: CND's n added,
	// then the one of OTH, the first row past a turn's bound.
	var order []string
	for after := ""; len(order) <= n; {
		q := url.Values{"q": {"kind:comment"}, "limit": {"100"}}
		if after != "" {
			q.Set("after", after)
		}
		status, out := hsGet(t, f.testEnv, "/api/history", q, adminToken)
		if status != http.StatusOK {
			t.Fatalf("the admin's search: %d %v", status, out)
		}
		order = append(order, hsKeys(out)...)
		next, _ := out["next"].(string)
		if next == "" {
			break
		}
		after = next
	}
	if len(order) <= n || order[n] != beyond || slices.ContainsFunc(order[:n], func(k string) bool { return !strings.HasPrefix(k, "comment:"+f.ticket+"/") }) {
		t.Fatalf("the comment of OTH is not the row right past the bound: %d rows, row %d %q", len(order), n, order[min(n, len(order)-1)])
	}
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
	if refs := historyRefs(f.expectRead(t, searchPath("kind:comment project:OTH"), http.StatusOK, "")); !slices.Contains(refs, beyond) {
		t.Fatalf("the comment of OTH is not admitted: %v", refs)
	}
	every := historyRefs(f.expectRead(t, searchPath("kind:comment"), http.StatusOK, ""))
	if !slices.Contains(every, beyond) {
		t.Fatalf("a read of every comment, %d of CND's newer: %v", n, every)
	}
	more := "comment:" + f.ticket + "/" + doneOf(t)(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), f.ticket, "turnstile one more")).ID
	ix := histSettle(t, f.testEnv)
	until(t, "one more comment indexed", func() bool {
		_, err := ix.Entry(t.Context(), more)
		return err == nil
	})
	if got := historyRefs(f.expectRead(t, searchPath("kind:comment"), http.StatusOK, "")); !slices.Equal(got, every) {
		t.Fatalf("one more comment outside the grant: %v, before %v", got, every)
	}
}

// A continuation names only what the reply showed. A turn scoped to CND
// reads comments of CND among newer ones of OTH: each page's next reads on
// after its last entry, is opaque (no key of the index's, the turn's own
// rows' or the others', and no cursor the search would take), is refused
// when altered, for another line or for another caller, and the read ends
// on a reply of no entries, which carries none.
func TestAContinuationNamesOnlyWhatTheReplyShowed(t *testing.T) {
	f := newObsFixture(t)
	done := doneOf(t)
	for i := range 3 {
		done(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), f.ticket, fmt.Sprintf("kept note %d", i)))
	}
	for i := range 30 {
		done(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), f.otherTicket, fmt.Sprintf("hidden note %d", i)))
	}
	histSettle(t, f.testEnv)
	var keys []string
	until(t, "the comments indexed", func() bool {
		keys = hsKeys(hsSearch(t, f.testEnv, "kind:comment"))
		return len(keys) == 34
	})
	f.turn(t, func(c map[string]any) {
		c["scope"] = map[string]any{"projects": []string{"CND"}}
		c["budget"].(map[string]any)["readsPerTurn"] = brakeman.MaxReadsPerTurn
	})
	const line = "kind:comment"
	seen := map[string]bool{}
	var first string
	for after, pages := "", 0; ; pages++ {
		extra := []string{"limit", "2"}
		if after != "" {
			extra = append(extra, "after", after)
		}
		out := f.expectRead(t, searchPath(line, extra...), http.StatusOK, "")
		refs := historyRefs(out)
		for _, r := range refs {
			if !strings.HasPrefix(r, "comment:"+f.ticket+"/") || seen[r] {
				t.Fatalf("page %d: %v", pages, refs)
			}
			seen[r] = true
		}
		next, _ := out["next"].(string)
		if len(refs) == 0 {
			if next != "" {
				t.Fatalf("a reply of no entries carries next %q", next)
			}
			break
		}
		if next == "" {
			t.Fatalf("page %d of %v carries no next", pages, refs)
		}
		raw, err := base64.RawURLEncoding.DecodeString(next)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		for _, k := range keys {
			if bytes.Contains(raw, []byte(k)) || strings.Contains(next, k) {
				t.Fatalf("next names %s", k)
			}
		}
		if bytes.Contains(raw, []byte(`"k"`)) {
			t.Fatalf("next is the index's cursor: %q", raw)
		}
		if first == "" {
			first = next
		}
		after = next
	}
	if len(seen) != 4 {
		t.Fatalf("%d of CND's 4 comments read: %v", len(seen), seen)
	}
	if status, out := hsGet(t, f.testEnv, "/api/history", url.Values{"q": {line}, "after": {first}}, adminToken); status != http.StatusBadRequest {
		t.Fatalf("the search took a continuation as its cursor: %d %v", status, out)
	}
	altered := []byte(first)
	altered[len(altered)/2] ^= 1
	f.expectRead(t, searchPath(line, "after", string(altered)), http.StatusBadRequest, "bad_cursor")
	f.expectRead(t, searchPath(line+" project:CND", "after", first), http.StatusBadRequest, "bad_cursor")
	if resp, body := f.raw(searchPath(line, "after", first), adminToken, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("the admin read on with the turn's next: %d %s", resp.StatusCode, body)
	}
}

// A page cut by its byte bound holds its next within it: comments whose
// titles escape to six bytes a character, read under bounds a few of them
// fill (the route's seam in place of maxObserveReply), a step at a time
// across more than an entry's size, so one of them leaves less room past
// the last entry than a next takes: each first page is cut by the bound
// and its whole body is within it; read on, every comment is read once.
func TestAPageCutByBytesKeepsItsBound(t *testing.T) {
	f := newObsFixture(t)
	const n, from, step = 30, 12 << 10, 64
	for i := range n {
		doneOf(t)(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), f.ticket, fmt.Sprintf("%02d %s", i, strings.Repeat("<>&", 70))))
	}
	histSettle(t, f.testEnv)
	until(t, "the comments indexed", func() bool {
		total, _ := hsSearch(t, f.testEnv, "kind:comment project:CND")["total"].(float64)
		return int(total) == n+1
	})
	f.turn(t, func(c map[string]any) { c["budget"].(map[string]any)["readsPerTurn"] = brakeman.MaxReadsPerTurn })
	page := func(bound int, after string) (map[string]any, []string) {
		t.Helper()
		f.srv.historyTurnReply.Store(int64(bound))
		extra := []string{"limit", fmt.Sprint(maxHistoryTurnPage)}
		if after != "" {
			extra = append(extra, "after", after)
		}
		resp, body := f.readRaw(t, searchPath("kind:comment project:CND", extra...))
		if resp.StatusCode != http.StatusOK || len(body) > bound {
			t.Fatalf("a page under %d bytes: %d, %d bytes", bound, resp.StatusCode, len(body))
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out, historyRefs(out)
	}
	// An entry is under 2 KiB: the steps cross one whole.
	for bound := from; bound <= from+2<<10; bound += step {
		out, refs := page(bound, "")
		if len(refs) == 0 || len(refs) >= maxHistoryTurnPage || out["next"] == nil {
			t.Fatalf("under %d bytes: %d entries, next %v", bound, len(refs), out["next"])
		}
	}
	seen := map[string]bool{}
	for after := ""; ; {
		out, refs := page(from, after)
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
}

// endedIn launches a plain session named name in dir, ends it, and
// answers its id once its record is ended.
func endedIn(t *testing.T, b *bkEnv, dir, name string) string {
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

// A record that cannot be read is left out of a turn's read as a refused
// one is, and the reply is the same 200 whatever matches it: records in two
// repositories, the server restarted, a turn scoped to one; a record of
// each made unreadable, the turn's reads of words only the other
// repository's record holds, of a prefix of them and of words none holds
// answer alike, and its read of its own repository's records answers the
// readable one.
func TestARecordThatCannotBeReadIsLeftOut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a record's mode does not keep root from reading it")
	}
	b := brakemanEnv(t, nil)
	inside, outside := filepath.Join(b.root, "inside"), filepath.Join(b.root, "outside")
	gitInit(t, inside, true)
	gitInit(t, outside, true)
	kept := endedIn(t, b, inside, "turnstile kept")
	damaged := endedIn(t, b, inside, "turnstile damaged")
	elsewhere := endedIn(t, b, outside, "turnstile zebracorn")
	rec, _, err := b.srv.sessionRecords.Get(kept)
	if err != nil || rec.Repo == "" {
		t.Fatalf("the record's repository: %+v %v", rec, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b.srv.Shutdown(ctx)
	again := &bkEnv{testEnv: b.restart()}
	again.desk = again.srv.brakeman
	again.srv.limiter = newRateLimiter(1e6, 1e6)
	histSettle(t, again.testEnv)
	until(t, "the records indexed", func() bool {
		keys := hsKeys(hsSearch(t, again.testEnv, "turnstile"))
		return slices.Contains(keys, "session:"+kept) && slices.Contains(keys, "session:"+damaged) && slices.Contains(keys, "session:"+elsewhere)
	})
	f := &obsFixture{bkEnv: again}
	f.turn(t, func(c map[string]any) {
		c["scope"] = map[string]any{"repos": []string{rec.Repo}}
		c["budget"].(map[string]any)["readsPerTurn"] = brakeman.MaxReadsPerTurn
	})
	lines := []string{"zebracorn", "zebr", "zebx", "qwertyuiop", "turnstile"}
	answers := func() map[string][]string {
		t.Helper()
		out := map[string][]string{}
		for _, line := range lines {
			out[line] = historyRefs(f.expectRead(t, searchPath(line), http.StatusOK, ""))
		}
		return out
	}
	before := answers()
	if !slices.Equal(before["turnstile"], []string{"session:" + damaged, "session:" + kept}) {
		t.Fatalf("the turn's records: %v", before["turnstile"])
	}
	for _, id := range []string{damaged, elsewhere} {
		path := filepath.Join(again.srv.cfg.DataDir, journal.Dir, journal.SessionsDir, id+".json")
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		if _, _, err := again.srv.sessionRecords.Get(id); err == nil || errors.Is(err, journal.ErrNotFound) {
			t.Fatalf("the record of %s is read: %v", id, err)
		}
	}
	after := answers()
	for _, line := range lines {
		want := before[line]
		if line == "turnstile" {
			want = []string{"session:" + kept}
		}
		if !slices.Equal(after[line], want) {
			t.Errorf("%q: %v, want %v", line, after[line], want)
		}
	}
}

// A turn's page is newest first, by its own rows alone: two comments of
// the turn's project, each matching a line by both its words, come back
// newest first, and in the same order however many rows outside the grant
// hold one of the words.
func TestATurnsPageIsNewestFirst(t *testing.T) {
	f := newObsFixture(t)
	done := doneOf(t)
	add := func(key, body string) string {
		return "comment:" + key + "/" + done(f.srv.dispatch.AddComment(t.Context(), adminOp("Ann"), key, body)).ID
	}
	older := add(f.ticket, "zork zork zork quux")
	newer := add(f.ticket, "zork quux quux quux")
	settled := func(n int) {
		t.Helper()
		histSettle(t, f.testEnv)
		until(t, "the comments indexed", func() bool {
			total, _ := hsSearch(t, f.testEnv, "kind:comment")["total"].(float64)
			return int(total) == n
		})
	}
	for range 15 {
		add(f.otherTicket, "quux")
	}
	settled(18)
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"projects": []string{"CND"}} })
	want := []string{newer, older}
	if got := historyRefs(f.expectRead(t, searchPath("zork quux"), http.StatusOK, "")); !slices.Equal(got, want) {
		t.Fatalf("the turn's page: %v, want %v", got, want)
	}
	for range 30 {
		add(f.otherTicket, "zork")
	}
	settled(48)
	if got := historyRefs(f.expectRead(t, searchPath("zork quux"), http.StatusOK, "")); !slices.Equal(got, want) {
		t.Fatalf("the turn's page with more rows outside the grant: %v, want %v", got, want)
	}
}

// A review's entries are read through the review routes and never served
// here: a person's review of a run's candidate with a finding resolved on
// a later candidate, and a review recorded through Brakeman's act, each
// in the admin's history, are served to no turn, whatever it sees and
// whatever its line names. A plan revision an act proposed is left out by
// its actor, beside a person's, which is served.
func TestReviewsAndActsWordsAreNotServed(t *testing.T) {
	b := brakemanEnv(t, nil)
	run := histReviewed(t, b.testEnv)
	v := histReview(t, b.testEnv, run)
	histMarkFinding(t, b.testEnv, run, v.Findings[0].ID, 2, dispatch.FindingResolved, "closed on logout")
	ds := b.srv.dispatch
	if _, _, err := ds.SubmitReview(t.Context(), dispatch.Op{Actor: brakemanActor, ID: opID(), By: brakemanBy}, "CND-1", dispatch.NewReview{
		Candidate: candName(run, "build", 2, commitID(2)), Base: commitID(0), Verdict: dispatch.VerdictComment, Summary: "The pelican handler holds.",
		Via: dispatch.ViaBrakeman}, dispatch.ReviewEvidence{}); err != nil {
		t.Fatal(err)
	}
	plan := func(op dispatch.Op, name string) string {
		t.Helper()
		crewJSON, _ := json.Marshal(map[string]any{"id": "c1", "name": name, "tasks": []any{map[string]any{"id": "build"}}})
		doneOf(t)(ds.ProposePlan(t.Context(), op, "CND-1", dispatch.PlanRequest{Crew: "c1"}, dispatch.PlanBody{Base: strings.Repeat("ab", 20), Crew: crewJSON}))
		plans, err := ds.Plans(t.Context(), "CND-1")
		if err != nil || len(plans) == 0 {
			t.Fatalf("the plans: %v %v", plans, err)
		}
		n := 0
		for _, p := range plans {
			n = max(n, p.N)
		}
		return hPlanRef("CND-1", n)
	}
	person := plan(adminOp("Ann"), "Pelican crew of Ann's")
	acted := plan(dispatch.Op{Actor: brakemanActor, ID: opID(), By: brakemanBy}, "Pelican crew of an act's")
	histSettle(t, b.testEnv)
	reviewKinds := []string{"review.batch", "review.finding", "review.resolved"}
	indexed := func() (kinds, plans []string) {
		for _, x := range hsSearch(t, b.testEnv, "kind:"+strings.Join(reviewKinds, ","))["entries"].([]any) {
			if k := x.(map[string]any)["kind"].(string); !slices.Contains(kinds, k) {
				kinds = append(kinds, k)
			}
		}
		return kinds, hsKeys(hsSearch(t, b.testEnv, "kind:plan"))
	}
	deadline := time.Now().Add(testWait)
	for kinds, plans := indexed(); len(kinds) != len(reviewKinds) || !slices.Contains(plans, person) || !slices.Contains(plans, acted); kinds, plans = indexed() {
		if time.Now().After(deadline) {
			t.Fatalf("the reviews and plans indexed: %v, %v (%s, %s)", kinds, plans, person, acted)
		}
		time.Sleep(2 * time.Millisecond)
	}
	f := &obsFixture{bkEnv: b}
	f.turn(t, func(c map[string]any) {
		c["scope"] = map[string]any{}
		c["visibility"] = map[string]any{"runs": true, "logs": true, "dispatch": true, "sessions": true, "touched": true}
	})
	for _, line := range []string{"pelican", "albatross", "kind:" + strings.Join(reviewKinds, ","), "kind:review.batch run:" + run} {
		for _, x := range f.expectRead(t, searchPath(line), http.StatusOK, "")["entries"].([]any) {
			if k := x.(map[string]any)["kind"].(string); strings.HasPrefix(k, "review.") {
				t.Errorf("%q: a review's entry is served: %v", line, x)
			}
		}
	}
	got := historyRefs(f.expectRead(t, searchPath("kind:plan"), http.StatusOK, ""))
	if !slices.Contains(got, person) || slices.Contains(got, acted) {
		t.Fatalf("the plan revisions: %v, a person's %s, an act's %s", got, person, acted)
	}
}

// A record keeps a folder past its bound cut, which can be its parent's: a
// session in a repository nested in another whose record keeps both its
// folder and its repository cut to the outer one admits nothing under a
// scope of repositories, the outer one's included; a scope of none
// admits it.
func TestAFolderAtTheJournalsBoundNamesNoRepository(t *testing.T) {
	b := brakemanEnv(t, nil)
	parent, child := longFolder(t, b.root)
	id := "0000000000000c02"
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	in, err := b.srv.sessionRecords.Begin(journal.Record{ID: id, Name: "long folder", Kind: "server", Where: "native", Cwd: child, Repo: child, StartedAt: at,
		Agent: journal.Agent{ID: "cat", Name: "Cat"}})
	if err != nil {
		t.Fatal(err)
	}
	b.srv.sessionRecords.Settle(in, journal.State{Name: "long folder", Final: true, End: &journal.Ending{At: at.Add(time.Minute), How: "stopped"}})
	b.srv.sessionRecords.Flush()
	if r, _, err := b.srv.sessionRecords.Get(id); err != nil || r.Cwd != parent || r.Repo != parent || !journal.CwdMayBeCut(r.Repo) {
		t.Fatalf("the record keeps %d bytes of the folder and %d of the repository: %v", len(r.Cwd), len(r.Repo), err)
	}
	brief := brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s", Items: []brakeman.Item{panel(ref(t, "session:"+id))}}
	for _, c := range []struct {
		repos []string
		ok    bool
	}{{[]string{parent}, false}, {[]string{child}, false}, {nil, true}} {
		cfg := brakeman.DefaultConfig(b.root)
		cfg.Scope.Repos = c.repos
		_, err := b.srv.resolveBriefRefs(t.Context(), nil, brakeman.Request{ID: "br-1", Config: brakeman.SnapshotOf(cfg)}, brief, brakeman.ReadLog{}, cfg)
		var bad *badRefsError
		if c.ok != (err == nil) || (!c.ok && (!errors.As(err, &bad) || !strings.Contains(err.Error(), "outside the request's scope"))) {
			t.Errorf("repos %v: %v", c.repos, err)
		}
	}
}
