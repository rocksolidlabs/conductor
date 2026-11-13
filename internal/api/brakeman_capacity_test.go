package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// Brakeman reads capacity (docs/plans/capacity.md, PR-6): a turn reads the
// capacity view under its Capacity class, as brakeman.CapacityOf makes it,
// from the readings the server keeps, never starting one; a brief that
// cites a reading keeps it as the turn read it. No agent's tool runs: the
// readers are the capacity tests' fakes, the turn Brakeman's scripted
// agent, and the readings' clock the test's own.

// turnClock is the readings' clock: Now moves only by add, and After
// never fires, so a read is answered once it is kept, whatever the time.
type turnClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *turnClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *turnClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

func (c *turnClock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// capTurn is a server with Brakeman's scripted agents beside the capacity
// tests' agents, its readings on clock, and a Claude Code account, Work,
// on folder.
type capTurn struct {
	*obsFixture
	claude, codex   *fakeUsage
	clock           *turnClock
	account, folder string
}

func newCapTurn(t *testing.T, extra ...catalog.Agent) *capTurn {
	t.Helper()
	ct := &capTurn{claude: newFakeUsage("claude"), codex: newFakeUsage("codex"), clock: &turnClock{now: time.Now()}}
	var clock capacity.Clock = ct.clock
	capacityClock.Store(&clock)
	t.Cleanup(func() { capacityClock.Store(nil) })
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), append(append(slices.Clone(capAgents), bkAgents()...), extra...), capReaders(ct.claude, ct.codex))
	capacityClock.Store(nil)
	ct.obsFixture = &obsFixture{bkEnv: bkEnvOf(t, e)}
	ct.folder = realRoot(t, t.TempDir())
	out := ct.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "claude", "label": "Work", "folder": ct.folder, "expectedRev": accountsRev(t, e)}, http.StatusCreated, "")
	ct.account = out["account"].(map[string]any)["id"].(string)
	return ct
}

// capacityOff is a configuration change that turns the Capacity class off.
func capacityOff(c map[string]any) { c["visibility"].(map[string]any)["capacity"] = false }

// viewPath is the path of agent's view on account.
func viewPath(agent, account string) string {
	return "/api/capacity?agent=" + agent + "&account=" + account
}

// evict reads agent on account KeepPerKey times more, MinInterval apart,
// so the reading cited at at has left the readings, and fails unless it
// has.
func (ct *capTurn) evict(t *testing.T, agent, account string, at time.Time) {
	t.Helper()
	for range capacity.KeepPerKey {
		ct.clock.add(capacity.MinInterval)
		readNow(t, ct.testEnv, agent, account, false)
	}
	if _, ok := ct.srv.capacity.cache.Kept(capacity.Key{Agent: agent, Account: account}, at); ok {
		t.Fatalf("the reading of %s on %s at %v is still kept", agent, account, at)
	}
}

// leaks fails t when body holds the account's folder, the server's name,
// the server's data directory or the variable an entry sets its login by.
func (ct *capTurn) leaks(t *testing.T, what string, body []byte) {
	t.Helper()
	leaks := []string{ct.folder, ct.srv.store.Dir(), "entryLogin", `"host":{`}
	if host, _ := os.Hostname(); len(host) >= 4 {
		leaks = append(leaks, host)
	}
	for _, l := range leaks {
		if strings.Contains(string(body), l) {
			t.Fatalf("%s holds %q: %.400s", what, l, body)
		}
	}
}

// TestBrakemanReadsCapacity: a turn reads the capacity view only under its
// Capacity class, as brakeman.CapacityOf makes it: labels and figures with
// the ref of the reading, no folder, no server's name, no variable; its
// read noted in the capacity class; nothing read by its read, and a read
// asked with its token refused.
func TestBrakemanReadsCapacity(t *testing.T) {
	ct := newCapTurn(t)
	readNow(t, ct.testEnv, "claude-a", ct.account, false)
	readNow(t, ct.testEnv, "claude-a", capacity.DefaultID, false)
	reads, _ := ct.claude.count()

	ct.turn(t, capacityOff)
	ct.expectRead(t, viewPath("claude-a", ct.account), http.StatusForbidden, "not_visible")
	ct.expectRead(t, "/api/capacity", http.StatusForbidden, "not_visible")

	ct.again(t, func(c map[string]any) { c["visibility"].(map[string]any)["capacity"] = true })
	resp, body := ct.readRaw(t, viewPath("claude-a", ct.account))
	var one map[string]any
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &one) != nil {
		t.Fatalf("the view: %d %s", resp.StatusCode, body)
	}
	ct.leaks(t, "the view", body)
	acct, _ := one["account"].(map[string]any)
	windows, _ := one["windows"].([]any)
	ref, _ := one["ref"].(string)
	if one["state"] != capacity.StateOK || one["host"] != capacity.HostNative || acct["label"] != "Work" || acct["plan"] != "max" || len(windows) != 1 ||
		!strings.HasPrefix(ref, "capacity:claude-a/"+ct.account+"@") || one["words"] != capacity.StateWords(capacity.StateOK) {
		t.Fatalf("the view: %s", body)
	}
	if rd, ok := ct.readOf(t, ref); !ok || rd.Class != brakeman.ClassCapacity || rd.State != capacity.StateOK {
		t.Fatalf("the read log's read of %s: %+v %v", ref, rd, ok)
	}
	resp, body = ct.readRaw(t, "/api/capacity")
	var page struct {
		Views []map[string]any `json:"views"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &page) != nil || len(page.Views) < 2 {
		t.Fatalf("the listing: %d %s", resp.StatusCode, body)
	}
	ct.leaks(t, "the listing", body)
	for _, v := range page.Views {
		if r, _ := v["ref"].(string); r != "" {
			if _, ok := ct.readOf(t, r); !ok {
				t.Errorf("the listing's %s is not in the read log", r)
			}
		}
	}
	if status, data, _ := ct.callAs(t, ct.tok, "POST", "/api/capacity/read", map[string]any{"agent": "claude-a"}); status != http.StatusUnauthorized {
		t.Fatalf("a read asked with the turn's token: %d %s", status, data)
	}
	if now, _ := ct.claude.count(); now != reads {
		t.Fatalf("the turn's reads started %d reads", now-reads)
	}
}

// capacityRefs is the request's resolved refs of the capacity: kind, by
// ref, as the admin's refs route answers them.
func (ct *capTurn) capacityRefs(t *testing.T) (map[string]map[string]any, []byte) {
	t.Helper()
	_, data, out := ct.call(t, "GET", "/api/brakeman/requests/"+ct.req+"/refs", nil)
	got := map[string]map[string]any{}
	for _, r := range out["refs"].([]any) {
		res := r.(map[string]any)
		if ref := res["ref"].(string); strings.HasPrefix(ref, "capacity:") {
			got[ref] = res
		}
	}
	return got, data
}

// readAtOf is the reading time ref names.
func readAtOf(t *testing.T, ref string) time.Time {
	t.Helper()
	r, err := brakeman.ParseRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339Nano, r.ReadAt)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// TestBriefCopiesCapacityRef: a brief citing a reading the turn read keeps
// it as the turn read it, from the server's readings and not the turn's
// text: once the reading has left the readings, the brief's ref still has
// its windows, its plan, readBy, the counts with their time and their
// estimate mark, and no folder.
func TestBriefCopiesCapacityRef(t *testing.T) {
	ct := newCapTurn(t)
	readNow(t, ct.testEnv, "claude-a", ct.account, false)
	ct.turn(t, nil)
	ref := ct.expectRead(t, viewPath("claude-a", ct.account), http.StatusOK, "")["ref"].(string)
	ct.submitFact(t, ref)
	ct.evict(t, "claude-a", ct.account, readAtOf(t, ref))
	refs, data := ct.capacityRefs(t)
	ct.leaks(t, "the refs", data)
	res := refs[ref]
	c, _ := res["capacity"].(map[string]any)
	windows, _ := c["windows"].([]any)
	readBy, _ := c["readBy"].(map[string]any)
	counts, _ := c["counts"].(map[string]any)
	if res["now"].(map[string]any)["state"] != capacity.StateOK || res["seen"].(map[string]any)["state"] != capacity.StateOK || len(windows) != 1 ||
		windows[0].(map[string]any)["percent"] != float64(40) || c["account"].(map[string]any)["plan"] != "max" || readBy["qualified"] != "2.0.0" ||
		counts["estimate"] != true || counts["source"] != capacity.SourceConductor || c["countedAt"] == nil || c["ref"] != ref {
		t.Fatalf("the brief's ref: %s", mustJSON(res))
	}
}

// TestBriefCapacityRefWithoutWindows: a Codex reading with ordinary use
// not allowed and no window, a Codex reading with credits only, and a
// Claude Code reading signed out: once each has left the readings, each
// ref still holds its limits (false kept, an absent one absent), its
// credits, its state and that state's words; no folder in any.
func TestBriefCapacityRefWithoutWindows(t *testing.T) {
	ct := newCapTurn(t)
	codexAcct := ct.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "codex", "label": "Codex work", "folder": ct.folder, "expectedRev": accountsRev(t, ct.testEnv)}, http.StatusCreated, "")["account"].(map[string]any)["id"].(string)
	no, yes, balance := false, true, "1250"
	ct.codex.set(func(f *fakeUsage) {
		f.report = agents.UsageReport{State: agents.UsageOK, Plan: "pro", Limits: agents.UsageLimits{Reached: "rate_limit", SpendControl: &yes, OrdinaryAllowed: &no}}
	})
	readNow(t, ct.testEnv, "codex-a", capacity.DefaultID, false)
	ct.codex.set(func(f *fakeUsage) {
		f.report = agents.UsageReport{State: agents.UsageOK, Plan: "pro", Credits: []agents.UsageCredit{{Kind: "credits", Balance: &balance}}}
	})
	readNow(t, ct.testEnv, "codex-a", codexAcct, false)
	ct.claude.set(func(f *fakeUsage) {
		f.report = agents.UsageReport{State: agents.UsageSignedOut, Words: "not signed in to Claude Code"}
	})
	readNow(t, ct.testEnv, "claude-a", capacity.DefaultID, false)
	ct.turn(t, nil)
	keys := [][2]string{{"codex-a", capacity.DefaultID}, {"codex-a", codexAcct}, {"claude-a", capacity.DefaultID}}
	var cited []string
	for _, k := range keys {
		cited = append(cited, ct.expectRead(t, viewPath(k[0], k[1]), http.StatusOK, "")["ref"].(string))
	}
	if status, out := ct.submit(t, ct.sid, ct.tok, "b-three", factBrief(cited...)); status != http.StatusCreated {
		t.Fatalf("the brief: %d %v", status, out)
	}
	for i, k := range keys {
		ct.evict(t, k[0], k[1], readAtOf(t, cited[i]))
	}
	refs, data := ct.capacityRefs(t)
	ct.leaks(t, "the refs", data)
	limits := refs[cited[0]]["capacity"].(map[string]any)
	if l := limits["limits"].(map[string]any); l["ordinaryAllowed"] != false || l["spendControl"] != true || l["reached"] != "rate_limit" || len(limits["windows"].([]any)) != 0 {
		t.Errorf("the limits alone: %s", mustJSON(limits))
	}
	credits := refs[cited[1]]["capacity"].(map[string]any)
	cr, _ := credits["credits"].([]any)
	if l := credits["limits"].(map[string]any); len(cr) != 1 || cr[0].(map[string]any)["balance"] != "1250" || l["ordinaryAllowed"] != nil || len(credits["windows"].([]any)) != 0 {
		t.Errorf("the credits alone: %s", mustJSON(credits))
	}
	out := refs[cited[2]]["capacity"].(map[string]any)
	if out["state"] != capacity.StateSignedOut || out["words"] != capacity.StateWords(capacity.StateSignedOut) || out["readAt"] == nil {
		t.Errorf("the reading signed out: %s", mustJSON(out))
	}
}

// A cited reading that has left the readings by the time the brief is
// kept resolves unavailable beside what the turn read of it, with no
// reading kept; one the turn never read and the server does not keep
// resolves to nothing, and the brief is refused naming it.
func TestACapacityRefLeftTheReadingsIsUnavailable(t *testing.T) {
	ct := newCapTurn(t)
	readNow(t, ct.testEnv, "claude-a", ct.account, false)
	ct.turn(t, nil)
	ref := ct.expectRead(t, viewPath("claude-a", ct.account), http.StatusOK, "")["ref"].(string)
	ct.evict(t, "claude-a", ct.account, readAtOf(t, ref))
	never := "capacity:claude-a/default@2026-11-11T03:00:00Z"
	if status, out := ct.submit(t, ct.sid, ct.tok, "b-gone", factBrief(never)); status != http.StatusUnprocessableEntity || !strings.Contains(mustJSON(out), "resolves to nothing") {
		t.Fatalf("a reading never read nor kept: %d %v", status, out)
	}
	ct.submitFact(t, ref)
	refs, _ := ct.capacityRefs(t)
	res := refs[ref]
	if res["now"].(map[string]any)["state"] != refUnavailable || res["seen"].(map[string]any)["state"] != capacity.StateOK || res["capacity"] != nil {
		t.Fatalf("the reading gone: %s", mustJSON(res))
	}
}

// TestGuideObserveCapacity: the guide's observe_capacity entry names GET
// /api/capacity, its parameters and the route's bound, the observe tool
// takes capacity, the read needs the Capacity class, and a turn's guide
// lists the entry only while the turn sees the class.
func TestGuideObserveCapacity(t *testing.T) {
	ops := Operations()
	i := slices.IndexFunc(ops, func(o Operation) bool { return o.Name == "observe_capacity" })
	if i < 0 {
		t.Fatal("no observe_capacity")
	}
	op := ops[i]
	props := op.Input["properties"].(map[string]any)
	if op.Method != http.MethodGet || op.Path != "/api/capacity" || op.Bounds["reply"] != maxCapacityPage || len(props) != 5 ||
		props["agent"] == nil || props["account"] == nil || props["launch"] == nil || props["after"] == nil || props["limit"] == nil ||
		!strings.Contains(op.Summary, "never a new one") {
		t.Fatalf("observe_capacity: %s", mustJSON(op))
	}
	if !slices.Contains(observeWhats, "capacity") || !slices.Equal(opClasses(op), []string{brakeman.ClassCapacity}) {
		t.Fatalf("the observe tool's whats %v, the entry's classes %v", observeWhats, opClasses(op))
	}
	ct := newCapTurn(t)
	listed := func() bool {
		_, _, out := guideOf(t, ct.testEnv, ct.tok, "/api/sessions/"+ct.sid+"/guide?section=operations")
		return slices.Contains(opNames(t, out), "observe_capacity")
	}
	ct.turn(t, capacityOff)
	if listed() {
		t.Fatal("listed to a turn that does not see capacity")
	}
	ct.again(t, func(c map[string]any) { c["visibility"].(map[string]any)["capacity"] = true })
	if !listed() {
		t.Fatal("not listed to a turn that sees capacity")
	}
}

// TestGuideCapacity: the guide's capacity section says what a view holds,
// that a window not reported is unknown, and what Brakeman may propose,
// never another account; the host section says per agent whether its
// usage is read and the version its reader was checked on.
func TestGuideCapacity(t *testing.T) {
	ct := newCapTurn(t, catalog.Agent{ID: "goose-a", Name: "Goose A", Adapter: "goose", Command: []string{"/bin/cat"}})
	ct.turn(t, nil)
	for _, c := range []struct{ tok, path string }{{adminToken, "/api/guide"}, {ct.tok, "/api/sessions/" + ct.sid + "/guide"}} {
		status, raw, out := guideOf(t, ct.testEnv, c.tok, c.path+"?section=capacity")
		sec, _ := out["capacity"].(map[string]any)
		if status != http.StatusOK || sec == nil || !strings.Contains(string(raw), "never zero") || !strings.Contains(string(raw), "Never propose another account of the same provider") ||
			!strings.Contains(string(raw), "capacity:<agent>/<account>@<readAt>") || !strings.Contains(string(raw), "launches, reserves and binds nothing") {
			t.Fatalf("%s's capacity section: %d %s", c.path, status, raw)
		}
		_, _, out = guideOf(t, ct.testEnv, c.tok, c.path+"?section=host")
		usage := map[string]string{}
		for _, a := range out["host"].(map[string]any)["agents"].([]any) {
			ag := a.(map[string]any)
			q, _ := ag["qualified"].(string)
			usage[ag["id"].(string)] = ag["usage"].(string) + "/" + q
		}
		if usage["claude-a"] != "reads/2.0.0" || usage["codex-a"] != "reads/2.0.0" || usage["goose-a"] != "unknown/" {
			t.Fatalf("%s's host section's usage: %v", c.path, usage)
		}
	}
}

// TestBrakemanRequestShowsItsView: a request keeps, as it is queued, the
// view of the agent and account its turn would run on, as a turn reads
// it: the agent's own login for no folder, the registered account whose
// folder Brakeman's configuration names, and a folder not in Accounts as
// unlisted; never the folder.
func TestBrakemanRequestShowsItsView(t *testing.T) {
	ct := newCapTurn(t)
	other := realRoot(t, t.TempDir())
	for _, tc := range []struct{ name, folder, account, state string }{
		{"no folder", "", capacity.DefaultID, capacity.StateNeverRead},
		{"a registered account's folder", ct.folder, ct.account, capacity.StateNeverRead},
		{"a folder not in Accounts", other, "unlisted", capacity.StateUnlisted},
	} {
		ct.enable(t, "bk", func(c map[string]any) { c["account"] = tc.folder })
		id := ct.briefing(t)
		req := ct.request(t, id)
		data := []byte(mustJSON(req["capacity"]))
		c, _ := req["capacity"].(map[string]any)
		if c == nil || c["agent"].(map[string]any)["id"] != "bk" || c["account"].(map[string]any)["id"] != tc.account || c["state"] != tc.state ||
			strings.Contains(string(data), other) {
			t.Fatalf("%s: the request's view: %s", tc.name, data)
		}
		ct.leaks(t, tc.name, data)
		ct.expect(t, "POST", "/api/brakeman/requests/"+id+"/cancel", map[string]any{"opId": "c-" + id}, http.StatusOK, "")
		ct.slotFree(t)
	}
}
