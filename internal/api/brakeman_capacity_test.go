package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
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
// that a window not reported is unknown, every state a view can carry with
// its words (the states' own table), that reading still shows the last
// reading, and what Brakeman may propose, never another account; the host
// section says per agent whether its usage is read and the version its
// reader was checked on.
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
		listed := map[string]string{}
		states, _ := sec["states"].(map[string]any)
		for state, words := range states {
			listed[state], _ = words.(string)
		}
		if len(capacity.States()) < 13 || len(listed) != len(capacity.States()) {
			t.Fatalf("%s's capacity section lists %d states of %d: %s", c.path, len(listed), len(capacity.States()), raw)
		}
		for _, s := range capacity.States() {
			if listed[s] != capacity.StateWords(s) {
				t.Errorf("%s's capacity section: %s's words %q, want %q", c.path, s, listed[s], capacity.StateWords(s))
			}
		}
		if r, _ := sec["reading"].(string); !strings.Contains(r, "still shows the last reading's figures") {
			t.Errorf("%s's capacity section on reading: %q", c.path, r)
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
// it, from the readings kept: the agent's own login for no folder and the
// registered account whose folder Brakeman's configuration names, each
// holding the reading made before the ask (its time, its windows), and
// still that reading once a newer one is kept; a folder not in Accounts
// as unlisted; never the folder.
func TestBrakemanRequestShowsItsView(t *testing.T) {
	ct := newCapTurn(t)
	other, codexFolder := realRoot(t, t.TempDir()), realRoot(t, t.TempDir())
	codexAcct := ct.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "codex", "label": "Codex work", "folder": codexFolder, "expectedRev": accountsRev(t, ct.testEnv)}, http.StatusCreated, "")["account"].(map[string]any)["id"].(string)
	timeOf := func(v any) time.Time {
		s, _ := v.(string)
		at, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatalf("a reading's time %v: %v", v, err)
		}
		return at
	}
	for _, tc := range []struct{ name, folder, account, state string }{
		{"no folder", "", capacity.DefaultID, capacity.StateOK},
		{"a registered account's folder", codexFolder, codexAcct, capacity.StateOK},
		{"a folder not in Accounts", other, "unlisted", capacity.StateUnlisted},
	} {
		var readAt time.Time
		if tc.state == capacity.StateOK {
			ct.clock.add(capacity.MinInterval)
			v := readNow(t, ct.testEnv, "bkcodex", tc.account, false)
			if v["state"] != capacity.StateOK {
				t.Fatalf("%s: the reading before the ask: %v", tc.name, v)
			}
			readAt = timeOf(v["readAt"])
		}
		ct.enable(t, "bkcodex", func(c map[string]any) { c["account"] = tc.folder })
		id := ct.briefing(t)
		for _, when := range []string{"as queued", "after a newer reading"} {
			if when != "as queued" {
				if tc.state != capacity.StateOK {
					break
				}
				ct.clock.add(capacity.MinInterval)
				if v := readNow(t, ct.testEnv, "bkcodex", tc.account, false); timeOf(v["readAt"]).Equal(readAt) {
					t.Fatalf("%s: no newer reading: %v", tc.name, v)
				}
			}
			req := ct.request(t, id)
			data := []byte(mustJSON(req["capacity"]))
			c, _ := req["capacity"].(map[string]any)
			if c == nil || c["agent"].(map[string]any)["id"] != "bkcodex" || c["account"].(map[string]any)["id"] != tc.account || c["state"] != tc.state ||
				strings.Contains(string(data), other) || strings.Contains(string(data), codexFolder) {
				t.Fatalf("%s, %s: the request's view: %s", tc.name, when, data)
			}
			if tc.state == capacity.StateOK {
				windows, _ := c["windows"].([]any)
				if !timeOf(c["readAt"]).Equal(readAt) || len(windows) != 1 || windows[0].(map[string]any)["percent"] != float64(40) {
					t.Fatalf("%s, %s: the request's view is not the reading made before the ask (%v): %s", tc.name, when, readAt, data)
				}
			}
			ct.leaks(t, tc.name, data)
		}
		ct.expect(t, "POST", "/api/brakeman/requests/"+id+"/cancel", map[string]any{"opId": "c-" + id}, http.StatusOK, "")
		ct.slotFree(t)
	}
}

// readTime is a view's readAt as JSON gives it.
func readTime(t *testing.T, v any) time.Time {
	t.Helper()
	s, _ := v.(string)
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("a reading's time %v: %v", v, err)
	}
	return at
}

// toldOnly fails t unless data, a reply to a turn, is {request} holding
// the fields a turn is told of its request (turnRequest) and no other:
// no capacity view, no reading.
func toldOnly(t *testing.T, what string, data []byte) {
	t.Helper()
	shape := map[string]bool{}
	for _, f := range receiptFields(reflect.TypeFor[turnRequest]()) {
		shape[f.Name] = true
	}
	var out map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil || len(out) != 1 || out["request"] == nil {
		t.Fatalf("%s: %v %.300s", what, err, data)
	}
	for k := range out["request"] {
		if !shape[k] || k == "capacity" {
			t.Fatalf("%s holds %q, no field a turn is told: %.600s", what, k, data)
		}
	}
	for _, s := range []string{`"readAt"`, `"windows"`, `"countedAt"`} {
		if strings.Contains(string(data), s) {
			t.Fatalf("%s holds %s: %.600s", what, s, data)
		}
	}
}

// TestATurnIsToldItsRequestWithoutItsView: the request route, a brief's
// submit and the same submit again tell a turn its request in the turn's
// fields alone, never the capacity view the request keeps, with the class
// off (a configuration saved before the class, loaded) and with it on;
// the turn's capacity read is refused with the class off; the admin's
// request routes carry the view.
func TestATurnIsToldItsRequestWithoutItsView(t *testing.T) {
	ct := newCapTurn(t)
	readNow(t, ct.testEnv, "bk", capacity.DefaultID, false)
	ct.enable(t, "bk", nil)
	path := filepath.Join(ct.srv.store.Dir(), brakeman.Dir, brakeman.ConfigFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	delete(saved["visibility"].(map[string]any), "capacity")
	if raw, err = json.Marshal(saved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if v := ct.config(t)["visibility"].(map[string]any); v["capacity"] != false {
		t.Fatalf("a configuration saved before the class: %v", v)
	}
	ct.req = ct.briefing(t)
	ct.sid, ct.tok, ct.turnLocal = ct.turnSession(t, ct.req)
	ct.waitState(t, ct.req, brakeman.StateRunning)
	for _, on := range []bool{false, true} {
		if on {
			ct.again(t, func(c map[string]any) { c["visibility"].(map[string]any)["capacity"] = true })
		}
		what := fmt.Sprintf("the class on %v", on)
		if c, _ := ct.request(t, ct.req)["capacity"].(map[string]any); c == nil || c["readAt"] == nil || c["ref"] == nil {
			t.Fatalf("%s: the admin's request route: %v", what, c)
		}
		if _, data, _ := ct.call(t, "GET", "/api/brakeman/requests", nil); !strings.Contains(string(data), `"capacity":{`) {
			t.Fatalf("%s: the admin's requests: %.300s", what, data)
		}
		want, code := http.StatusForbidden, "not_visible"
		if on {
			want, code = http.StatusOK, ""
		}
		ct.expectRead(t, viewPath("bk", capacity.DefaultID), want, code)
		status, data, _ := ct.callAs(t, ct.tok, "GET", "/api/sessions/"+ct.sid+"/brakeman/request", nil)
		if status != http.StatusOK {
			t.Fatalf("%s: the request route: %d %s", what, status, data)
		}
		toldOnly(t, what+": the request route", data)
		body := map[string]any{"opId": fmt.Sprintf("b-told-%v", on), "brief": factBrief("session:" + ct.sid)}
		for _, c := range []struct {
			name   string
			status int
		}{{"the submit", http.StatusCreated}, {"the same submit again", http.StatusOK}} {
			status, data, _ := ct.callAs(t, ct.tok, "POST", "/api/sessions/"+ct.sid+"/brakeman/brief", body)
			if status != c.status {
				t.Fatalf("%s: %s: %d %s", what, c.name, status, data)
			}
			toldOnly(t, what+": "+c.name, data)
		}
	}
}

// TestATurnsReadMovesNoReading: with the readings at their bound, a turn's
// read of the least recently asked key, its view and the listing, leaves
// that key the one the admin's next read drops, as without the turn's read.
func TestATurnsReadMovesNoReading(t *testing.T) {
	ct := newCapTurn(t)
	ct.turn(t, nil)
	least := capacity.Key{Agent: "claude-a", Account: ct.account}
	at := readTime(t, readNow(t, ct.testEnv, least.Agent, least.Account, false)["readAt"])
	cache := ct.srv.capacity.cache
	var next capacity.Key
	var nextAt time.Time
	for i := range capacity.MaxKeys - 1 {
		k := capacity.Key{Agent: fmt.Sprintf("fill-%d", i), Account: capacity.DefaultID}
		v := cache.Read(t.Context(), k, capacity.ReadOptions{})
		if v.ReadAt == nil {
			t.Fatalf("no reading of %v: %+v", k, v)
		}
		if i == 0 {
			next, nextAt = k, *v.ReadAt
		}
	}
	ct.expectRead(t, viewPath(least.Agent, least.Account), http.StatusOK, "")
	ct.expectRead(t, "/api/capacity", http.StatusOK, "")
	readNow(t, ct.testEnv, "codex-a", capacity.DefaultID, false)
	if _, ok := cache.Kept(least, at); ok {
		t.Fatal("the key the turn read is still kept: the turn's read moved it, and the admin's read dropped another")
	}
	if _, ok := cache.Kept(next, nextAt); !ok {
		t.Fatalf("%v, the next least recently asked, was dropped", next)
	}
}

// TestACitedReadingIsNotedAtItsOwnState: while a new read of a key is
// under way, a turn's read shows the last reading at the state reading;
// the read log notes its ref at the reading's own state, the one a brief
// citing it resolves to.
func TestACitedReadingIsNotedAtItsOwnState(t *testing.T) {
	ct := newCapTurn(t)
	readNow(t, ct.testEnv, "claude-a", ct.account, false)
	ct.turn(t, nil)
	gate, called := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)
	ct.claude.set(func(f *fakeUsage) { f.gate, f.called = gate, called })
	ct.clock.add(capacity.MinInterval)
	done := make(chan int, 1)
	go func() {
		status, _ := ct.send(adminToken, "POST", "/api/capacity/read", map[string]any{"agent": "claude-a", "account": ct.account})
		done <- status
	}()
	select {
	case <-called:
	case <-time.After(hangWait):
		t.Fatal("the read never reached the reader")
	}
	view := ct.expectRead(t, viewPath("claude-a", ct.account), http.StatusOK, "")
	ref, _ := view["ref"].(string)
	if view["state"] != capacity.StateReading || ref == "" {
		t.Fatalf("the view while a read is under way: %v", view)
	}
	rd, noted := ct.readOf(t, ref)
	ct.submitFact(t, ref)
	res := mustCapacityRef(t, ct, ref)
	seen, now := res["seen"].(map[string]any)["state"], res["now"].(map[string]any)["state"]
	if !noted || rd.State != capacity.StateOK || seen != rd.State || now != rd.State {
		t.Fatalf("noted %v at %q; resolved seen %v, now %v", noted, rd.State, seen, now)
	}
	open()
	select {
	case status := <-done:
		if status != http.StatusAccepted {
			t.Fatalf("the read: %d", status)
		}
	case <-time.After(hangWait):
		t.Fatal("the read never answered")
	}
}

// mustCapacityRef is the resolution of ref the request's brief keeps.
func mustCapacityRef(t *testing.T, ct *capTurn, ref string) map[string]any {
	t.Helper()
	refs, data := ct.capacityRefs(t)
	res := refs[ref]
	if res == nil {
		t.Fatalf("no %s in %.300s", ref, data)
	}
	return res
}

// waitOver makes a request's wait end when the test says: each wait ends
// once the channel over gives at its start is closed.
func (ct *capTurn) waitOver(over func() <-chan struct{}) {
	f := func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		ch := over()
		go func() {
			select {
			case <-ch:
				cancel()
			case <-ctx.Done():
			}
		}()
		return ctx, cancel
	}
	ct.srv.capacity.waitOver.Store(&f)
}

// heldLookups holds every look-up of program at a gate, counting each
// program's look-ups: called is told as one reaches the gate.
type heldLookups struct {
	mu     sync.Mutex
	count  map[string]int
	gate   chan struct{}
	called chan struct{}
	once   sync.Once
}

func holdLookups(t *testing.T, e *testEnv, program string) *heldLookups {
	t.Helper()
	h := &heldLookups{count: map[string]int{}, gate: make(chan struct{}), called: make(chan struct{}, 64)}
	t.Cleanup(h.open)
	look := func(p string) (string, error) {
		h.mu.Lock()
		h.count[p]++
		h.mu.Unlock()
		if p == program {
			select {
			case h.called <- struct{}{}:
			default:
			}
			<-h.gate
		}
		return p, nil
	}
	c := e.srv.lookups
	c.stubWait(hangWait)
	c.stub(look)
	// A look-up begun before the stub keeps its answer when it ends: once
	// none of those is under way (none is, or the one is the stub's) the
	// answers are cleared again, so every later look-up of program is held.
	if !waitFor(hangWait, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.inflight[program] == nil || h.of(program) > 0
	}) {
		t.Fatalf("a look-up of %s begun before the gate never ended", program)
	}
	c.stub(look)
	return h
}

// queuing waits until a request's view is being made: the view's slot is
// taken, its configuration read.
func (ct *capTurn) queuing(t *testing.T) {
	t.Helper()
	if !waitFor(hangWait, func() bool { return len(ct.srv.capacity.queuing) == 1 }) {
		t.Fatal("no request's view was being made")
	}
}

func (h *heldLookups) open() { h.once.Do(func() { close(h.gate) }) }

func (h *heldLookups) of(program string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count[program]
}

// reached waits for a look-up to reach the gate.
func (h *heldLookups) reached(t *testing.T) {
	t.Helper()
	select {
	case <-h.called:
	case <-time.After(hangWait):
		t.Fatal("no look-up reached the gate")
	}
}

// asked is an ask's answer: its status and request.
type asked struct {
	status int
	req    map[string]any
}

// askBriefing asks for a briefing under op as the admin, answering on the
// channel once the server has.
func (ct *capTurn) askBriefing(op string) <-chan asked {
	out := make(chan asked, 1)
	go func() {
		status, body := ct.send(adminToken, "POST", "/api/brakeman/requests", map[string]any{"opId": op, "kind": "briefing"})
		req, _ := body["request"].(map[string]any)
		out <- asked{status, req}
	}()
	return out
}

// answered is the request of an ask answered 201, failing t otherwise.
func answered(t *testing.T, what string, c <-chan asked) map[string]any {
	t.Helper()
	select {
	case a := <-c:
		if a.status != http.StatusCreated || a.req == nil {
			t.Fatalf("%s: %d %v", what, a.status, a.req)
		}
		return a.req
	case <-time.After(hangWait):
		t.Fatalf("%s never answered", what)
	}
	return nil
}

// noView fails t when req, as answered or as kept, keeps a view.
func (ct *capTurn) noView(t *testing.T, what string, req map[string]any) {
	t.Helper()
	if req["capacity"] != nil {
		t.Fatalf("%s keeps a view: %v", what, req["capacity"])
	}
	if kept := ct.request(t, req["id"].(string)); kept["capacity"] != nil {
		t.Fatalf("%s keeps a view: %v", what, kept["capacity"])
	}
}

// TestARequestsViewIsMadeBeforeAdmission: a request's view is made before
// the desk's lock is taken, within its one wait. An ask whose look-up is
// held is queued without a view once the wait is over, and a second ask
// beside it is answered meanwhile, queued without one; a configuration
// save beside a held ask is answered, and the view made for the
// configuration it replaced is not kept.
func TestARequestsViewIsMadeBeforeAdmission(t *testing.T) {
	ct := newCapTurn(t)
	ct.enable(t, "bk", nil)
	var mu sync.Mutex
	over := make(chan struct{})
	ct.waitOver(func() <-chan struct{} {
		mu.Lock()
		defer mu.Unlock()
		return over
	})
	ask, answer := ct.askBriefing, func(what string, c <-chan asked) map[string]any { t.Helper(); return answered(t, what, c) }
	noView := func(what string, req map[string]any) { t.Helper(); ct.noView(t, what, req) }

	held := holdLookups(t, ct.testEnv, "/bin/sh")
	first := ask("ask-held")
	ct.queuing(t)
	select {
	case a := <-first:
		t.Fatalf("the ask answered while its look-up is held and its wait is not over: %d %v", a.status, a.req)
	default:
	}
	noView("a second ask beside the held one", answer("a second ask beside the held one", ask("ask-beside")))
	mu.Lock()
	close(over)
	mu.Unlock()
	noView("the ask whose wait is over", answer("the ask whose wait is over", first))
	held.open()
	waitKept(t, ct.srv.lookups, "/bin/sh", hangWait)
	if !waitFor(hangWait, func() bool { return len(ct.srv.capacity.queuing) == 0 }) {
		t.Fatal("the view's slot was never given back")
	}

	mu.Lock()
	over = make(chan struct{})
	mu.Unlock()
	held = holdLookups(t, ct.testEnv, "/bin/sh")
	third := ask("ask-saved")
	ct.queuing(t)
	ct.configure(t, func(c map[string]any) { c["budget"].(map[string]any)["readsPerTurn"] = float64(299) })
	held.open()
	noView("an ask whose view was made for the configuration a save replaced", answer("the ask beside the save", third))
}

// capBrief is a briefing citing refs: a fact with the first ten, evidence
// panels with the rest.
func capBrief(refs []string) map[string]any {
	items := []any{map[string]any{"kind": brakeman.ItemFact, "text": "the readings", "evidence": refs[:10]}}
	for i := 10; i < len(refs); i += 50 {
		items = append(items, map[string]any{"kind": brakeman.ItemEvidence, "title": "readings", "refs": refs[i:min(i+50, len(refs))]})
	}
	return map[string]any{"schema": brakeman.BriefSchema, "title": "Capacity", "summary": "What the readings say.", "items": items}
}

// capRef is the ref of k's reading made at at.
func capRef(k capacity.Key, at time.Time) string {
	return brakeman.Ref{Kind: brakeman.RefCapacity, Agent: k.Agent, Account: k.Account, ReadAt: at.UTC().Format(time.RFC3339Nano)}.String()
}

// countLookups answers every look-up of a program from now on, found (the
// path itself) or not, counting them.
func countLookups(e *testEnv, found bool) *atomic.Int64 {
	n := &atomic.Int64{}
	e.srv.lookups.stub(func(p string) (string, error) {
		n.Add(1)
		if !found {
			return "", exec.ErrNotFound
		}
		return p, nil
	})
	return n
}

// jsonTime is a time as encoding/json writes one.
var jsonTime = regexp.MustCompile(`"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d))"`)

// inUTC fails t unless data holds a time and every time it holds is in
// UTC.
func inUTC(t *testing.T, what string, data []byte) {
	t.Helper()
	times := jsonTime.FindAllSubmatch(data, -1)
	if len(times) == 0 {
		t.Fatalf("%s holds no time: %.300s", what, data)
	}
	for _, m := range times {
		if !bytes.HasSuffix(m[1], []byte("Z")) {
			t.Errorf("%s writes %s, not in UTC", what, m[1])
		}
	}
}

// TestABriefsCapacityRefsLookNothingUp: a brief with as many distinct refs
// as a brief may hold, every one a capacity: ref, half of them naming
// readings the server holds (of agents in the catalog and of agents it no
// longer lists) and half readings it does not hold: no program is looked
// up for any, the accounts are read once for the brief, and the refs
// refused are exactly those whose readings the server does not hold.
func TestABriefsCapacityRefsLookNothingUp(t *testing.T) {
	ct := newCapTurn(t)
	ct.turn(t, nil)
	cache := ct.srv.capacity.cache
	half := brakeman.MaxBriefRefs / 2
	keys := []capacity.Key{{Agent: "claude-a", Account: ct.account}, {Agent: "claude-a", Account: capacity.DefaultID}, {Agent: "codex-a", Account: capacity.DefaultID}}
	for i := 0; len(keys)*capacity.KeepPerKey < half; i++ {
		keys = append(keys, capacity.Key{Agent: fmt.Sprintf("gone-%d", i), Account: capacity.DefaultID})
	}
	var refs []string
	for len(refs) < half {
		ct.clock.add(capacity.MinInterval)
		for _, k := range keys[:min(len(keys), half-len(refs))] {
			v := cache.Read(t.Context(), k, capacity.ReadOptions{})
			if v.ReadAt == nil {
				t.Fatalf("no reading of %v: %+v", k, v)
			}
			refs = append(refs, capRef(k, *v.ReadAt))
		}
	}
	never := map[string]bool{}
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := range brakeman.MaxBriefRefs - half {
		ref := "capacity:ghost/default@" + base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano)
		never[ref] = true
		refs = append(refs, ref)
	}
	looks := countLookups(ct.testEnv, true)
	reads := ct.srv.capacity.reads.Load()
	status, out := ct.submit(t, ct.sid, ct.tok, "b-half", capBrief(refs))
	refused := map[string]bool{}
	e, _ := out["error"].(map[string]any)
	list, _ := e["refs"].([]any)
	for _, b := range list {
		refused[b.(map[string]any)["ref"].(string)] = true
	}
	if status != http.StatusUnprocessableEntity || !maps.Equal(refused, never) {
		t.Fatalf("%d: %d refs refused, want the %d whose readings the server does not hold: %.300s", status, len(refused), len(never), mustJSON(out))
	}
	if n := looks.Load(); n != 0 {
		t.Errorf("%d look-ups of a program for a brief's capacity refs", n)
	}
	if n := ct.srv.capacity.reads.Load() - reads; n != 1 {
		t.Errorf("the accounts read %d times for one brief", n)
	}
}

// TestACapacityRefIsTheReadingHeld: a brief's capacity: ref resolves to
// the reading the server holds, whatever its account or its agent is now:
// a reading whose account was removed since names the account by its id
// alone; one whose agent's program is gone since, one whose key is being
// read again, and readings in the states that carry no version (signed
// out, key-paid, unqualified, error) each resolve at the reading's own
// state, windows and time, those with no version and no readBy. No program
// is looked up for them, the accounts are read once, and every time the
// snapshots and the turn's view write is in UTC.
func TestACapacityRefIsTheReadingHeld(t *testing.T) {
	ct := newCapTurn(t)
	reset := time.Now().Add(7 * 24 * time.Hour).In(time.FixedZone("east", 5*3600+1800)).Truncate(time.Second)
	for _, f := range []*fakeUsage{ct.claude, ct.codex} {
		f.set(func(f *fakeUsage) { f.report.Windows[0].ResetsAt = &reset })
	}
	gone := addAccount(t, ct.testEnv, "claude", "Gone")
	type cited struct {
		name, ref, state string
	}
	var all []cited
	read := func(name string, k capacity.Key, state string) cited {
		t.Helper()
		ct.clock.add(capacity.MinInterval)
		v := readNow(t, ct.testEnv, k.Agent, k.Account, false)
		if v["state"] != state {
			t.Fatalf("%s: the reading %v, want %s", name, v, state)
		}
		c := cited{name: name, ref: capRef(k, readTime(t, v["readAt"])), state: state}
		all = append(all, c)
		return c
	}
	removed := read("an account removed since", capacity.Key{Agent: "claude-a", Account: gone}, capacity.StateOK)
	programGone := read("an agent whose program is gone since", capacity.Key{Agent: "codex-a", Account: capacity.DefaultID}, capacity.StateOK)
	for _, state := range []string{agents.UsageSignedOut, agents.UsageKeyPaid, agents.UsageUnqualified, agents.UsageError} {
		ct.claude.set(func(f *fakeUsage) { f.report.State = state })
		read("a reading "+state, capacity.Key{Agent: "claude-a", Account: capacity.DefaultID}, state)
	}
	ct.claude.set(func(f *fakeUsage) { f.report.State = agents.UsageOK })
	underWay := read("a reading whose key is read again", capacity.Key{Agent: "claude-a", Account: ct.account}, capacity.StateOK)
	if status, _, out := ct.call(t, "DELETE", "/api/accounts/"+gone+"?expectedRev="+strconv.FormatInt(accountsRev(t, ct.testEnv), 10), nil); status != http.StatusNoContent {
		t.Fatalf("the account's remove: %d %v", status, out)
	}

	ct.turn(t, nil)
	_, view := ct.readRaw(t, viewPath("claude-a", ct.account))
	inUTC(t, "the turn's view", view)
	gate, called := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)
	ct.claude.set(func(f *fakeUsage) { f.gate, f.called = gate, called })
	ct.clock.add(capacity.MinInterval)
	done := make(chan int, 1)
	go func() {
		status, _ := ct.send(adminToken, "POST", "/api/capacity/read", map[string]any{"agent": "claude-a", "account": ct.account})
		done <- status
	}()
	select {
	case <-called:
	case <-time.After(hangWait):
		t.Fatal("the read never reached the reader")
	}

	looks := countLookups(ct.testEnv, false)
	reads := ct.srv.capacity.reads.Load()
	var refs []string
	for _, c := range all {
		refs = append(refs, c.ref)
	}
	if status, out := ct.submit(t, ct.sid, ct.tok, "b-held", factBrief(refs...)); status != http.StatusCreated {
		t.Fatalf("the brief: %d %v", status, out)
	}
	if n := looks.Load(); n != 0 {
		t.Errorf("%d look-ups of a program for the brief's capacity refs", n)
	}
	if n := ct.srv.capacity.reads.Load() - reads; n != 1 {
		t.Errorf("the accounts read %d times for one brief", n)
	}
	got, data := ct.capacityRefs(t)
	inUTC(t, "the brief's refs", data)
	for _, c := range all {
		res := got[c.ref]
		snap, _ := res["capacity"].(map[string]any)
		if snap == nil || snap["ref"] != c.ref || snap["state"] != c.state || res["now"].(map[string]any)["state"] != c.state ||
			!readTime(t, snap["readAt"]).Equal(readAtOf(t, c.ref)) || snap["words"] != capacity.StateWords(c.state) {
			t.Fatalf("%s: %s", c.name, mustJSON(res))
		}
		agent, _ := snap["agent"].(map[string]any)
		windows, _ := snap["windows"].([]any)
		if c.state == capacity.StateOK && (agent["version"] != "2.0.0" || snap["readBy"] == nil || len(windows) != 1) {
			t.Errorf("%s: the reading's version, readBy and window: %s", c.name, mustJSON(snap))
		}
		if c.state != capacity.StateOK && (agent["version"] != nil || snap["readBy"] != nil || len(windows) != 0) {
			t.Errorf("%s: a reading %s carries a version, a readBy or a window: %s", c.name, c.state, mustJSON(snap))
		}
	}
	if acct := got[removed.ref]["capacity"].(map[string]any)["account"].(map[string]any); acct["id"] != gone || acct["label"] != nil || acct["default"] != nil || acct["retired"] != nil {
		t.Errorf("%s: the account %v", removed.name, acct)
	}
	if agent := got[programGone.ref]["capacity"].(map[string]any)["agent"].(map[string]any); agent["name"] != "Codex A" || agent["adapter"] != "codex" {
		t.Errorf("%s: the agent %v", programGone.name, agent)
	}
	if acct := got[underWay.ref]["capacity"].(map[string]any)["account"].(map[string]any); acct["label"] != "Work" {
		t.Errorf("%s: the account %v", underWay.name, acct)
	}
	open()
	select {
	case status := <-done:
		if status != http.StatusAccepted {
			t.Fatalf("the read: %d", status)
		}
	case <-time.After(hangWait):
		t.Fatal("the read never answered")
	}
}

// TestACapacityRefWithTheAccountsUnreadable: with the accounts list
// unreadable, a brief's capacity: ref resolves to the reading the server
// holds, its account named by its id alone.
func TestACapacityRefWithTheAccountsUnreadable(t *testing.T) {
	ct := newCapTurn(t)
	k := capacity.Key{Agent: "claude-a", Account: ct.account}
	ref := capRef(k, readTime(t, readNow(t, ct.testEnv, k.Agent, k.Account, false)["readAt"]))
	ct.turn(t, nil)
	file := filepath.Join(ct.srv.store.Dir(), capacity.Dir, capacity.AccountsFile)
	kept, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("{not the accounts"), 0o600); err != nil {
		t.Fatal(err)
	}
	ct.submitFact(t, ref)
	if err := os.WriteFile(file, kept, 0o600); err != nil {
		t.Fatal(err)
	}
	snap, _ := mustCapacityRef(t, ct, ref)["capacity"].(map[string]any)
	acct, _ := snap["account"].(map[string]any)
	windows, _ := snap["windows"].([]any)
	if snap == nil || snap["state"] != capacity.StateOK || len(windows) != 1 || acct["id"] != ct.account || acct["label"] != nil || acct["default"] != nil || acct["retired"] != nil || acct["plan"] != "max" {
		t.Fatalf("the ref with the accounts unreadable: %s", mustJSON(snap))
	}
}

// TestATurnsReadNotesTheStateItsViewWasMadeWith: a turn's read of a view
// whose key is being read again notes the view's ref at its reading's own
// state, as the look that made the view answered it, also when the reading
// leaves the readings between the reply's making and the note.
func TestATurnsReadNotesTheStateItsViewWasMadeWith(t *testing.T) {
	ct := newCapTurn(t)
	k := capacity.Key{Agent: "claude-a", Account: ct.account}
	at := readTime(t, readNow(t, ct.testEnv, k.Agent, k.Account, false)["readAt"])
	ct.turn(t, nil)
	gate, called := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)
	ct.claude.set(func(f *fakeUsage) { f.gate, f.called = gate, called })
	ct.clock.add(capacity.MinInterval)
	done := make(chan int, 1)
	go func() {
		status, _ := ct.send(adminToken, "POST", "/api/capacity/read", map[string]any{"agent": k.Agent, "account": k.Account})
		done <- status
	}()
	select {
	case <-called:
	case <-time.After(hangWait):
		t.Fatal("the read never reached the reader")
	}
	cache := ct.srv.capacity.cache
	atObserve(t, "answered", func() {
		for i := range capacity.MaxKeys {
			cache.Read(context.Background(), capacity.Key{Agent: fmt.Sprintf("fill-%d", i), Account: capacity.DefaultID}, capacity.ReadOptions{})
		}
	})
	view := ct.expectRead(t, viewPath(k.Agent, k.Account), http.StatusOK, "")
	if _, held := cache.Kept(k, at); held {
		t.Fatal("the reading the view showed is still held")
	}
	rd, noted := ct.readOf(t, capRef(k, at))
	if view["state"] != capacity.StateReading || view["ref"] != capRef(k, at) || !noted || rd.State != capacity.StateOK {
		t.Fatalf("the view %v of %v; noted %v at %q", view["state"], view["ref"], noted, rd.State)
	}
	open()
	select {
	case status := <-done:
		if status != http.StatusAccepted {
			t.Fatalf("the read: %d", status)
		}
	case <-time.After(hangWait):
		t.Fatal("the read never answered")
	}
}

// TestQueuingARequestMovesNoReading: with the readings at their bound, a
// request queued for the least recently asked key keeps that key's reading
// as its view and leaves the key the one the admin's next read drops, as a
// turn's read does.
func TestQueuingARequestMovesNoReading(t *testing.T) {
	ct := newCapTurn(t)
	least := capacity.Key{Agent: "bkcodex", Account: capacity.DefaultID}
	at := readTime(t, readNow(t, ct.testEnv, least.Agent, least.Account, false)["readAt"])
	cache := ct.srv.capacity.cache
	var next capacity.Key
	var nextAt time.Time
	for i := range capacity.MaxKeys - 1 {
		k := capacity.Key{Agent: fmt.Sprintf("fill-%d", i), Account: capacity.DefaultID}
		v := cache.Read(t.Context(), k, capacity.ReadOptions{})
		if v.ReadAt == nil {
			t.Fatalf("no reading of %v: %+v", k, v)
		}
		if i == 0 {
			next, nextAt = k, *v.ReadAt
		}
	}
	ct.enable(t, least.Agent, nil)
	id := ct.briefing(t)
	if c, _ := ct.request(t, id)["capacity"].(map[string]any); c == nil || !readTime(t, c["readAt"]).Equal(at) {
		t.Fatalf("the request's view: %v", c)
	}
	readNow(t, ct.testEnv, "codex-a", capacity.DefaultID, false)
	if _, ok := cache.Kept(least, at); ok {
		t.Fatal("the key the request's view was made from is still held: queuing the request moved it, and the admin's read dropped another")
	}
	if _, ok := cache.Kept(next, nextAt); !ok {
		t.Fatalf("%v, the next least recently asked, was dropped", next)
	}
}

// TestAWakeRequestKeepsNoViewMadeForAnotherConfiguration: the background
// role's wake request, its view made for a configuration a save replaced
// before the request was admitted, keeps no view.
func TestAWakeRequestKeepsNoViewMadeForAnotherConfiguration(t *testing.T) {
	ct := newCapTurn(t)
	ct.background(t, "bk", nil)
	never := make(chan struct{})
	ct.waitOver(func() <-chan struct{} { return never })
	held := holdLookups(t, ct.testEnv, "/bin/sh")
	type fire struct {
		id  string
		err error
	}
	fired := make(chan fire, 1)
	go func() {
		id, err := roleRecords{d: ct.desk}.Fire(crew.Batch{ID: "wb-901", Wakes: []crew.BatchWake{}})
		fired <- fire{id, err}
	}()
	ct.queuing(t)
	held.reached(t)
	ct.configure(t, func(c map[string]any) { c["budget"].(map[string]any)["readsPerTurn"] = float64(299) })
	held.open()
	select {
	case f := <-fired:
		if f.err != nil || f.id == "" {
			t.Fatalf("the wake request: %q %v", f.id, f.err)
		}
		if c := ct.request(t, f.id)["capacity"]; c != nil {
			t.Fatalf("a wake request whose view was made for the configuration a save replaced keeps a view: %v", c)
		}
	case <-time.After(hangWait):
		t.Fatal("the wake request was never made")
	}
}

// TestARequestsWaitEndsWhileTheAccountsAreHeld: a request whose view's
// accounts read is held (an account's add under way in the store) is
// answered once its wait is over, with no view, while that read is still
// held; a second request queued meanwhile keeps no view either.
func TestARequestsWaitEndsWhileTheAccountsAreHeld(t *testing.T) {
	ct := newCapTurn(t)
	ct.enable(t, "bk", nil)
	over := make(chan struct{})
	ct.waitOver(func() <-chan struct{} { return over })
	entered, gate := make(chan struct{}), make(chan struct{})
	var once, opened sync.Once
	open := func() { opened.Do(func() { close(gate) }) }
	t.Cleanup(open)
	ct.srv.capacity.seam = func() { once.Do(func() { close(entered); <-gate }) }
	body := map[string]any{"adapter": "claude", "label": "Held", "folder": realRoot(t, t.TempDir()), "expectedRev": accountsRev(t, ct.testEnv)}
	added := make(chan int, 1)
	go func() {
		status, _ := ct.send(adminToken, "POST", "/api/accounts", body)
		added <- status
	}()
	select {
	case <-entered:
	case <-time.After(hangWait):
		t.Fatal("the add never reached the store")
	}
	reads := ct.srv.capacity.reads.Load()
	first := ct.askBriefing("ask-held")
	if !waitFor(hangWait, func() bool { return ct.srv.capacity.reads.Load() > reads }) {
		t.Fatal("the request's view never read the accounts")
	}
	select {
	case a := <-first:
		t.Fatalf("the ask answered while its accounts read is held and its wait is not over: %d %v", a.status, a.req)
	default:
	}
	ct.noView(t, "a second ask beside the held one", answered(t, "a second ask beside the held one", ct.askBriefing("ask-beside")))
	close(over)
	ct.noView(t, "the ask whose wait is over", answered(t, "the ask whose wait is over", first))
	select {
	case status := <-added:
		t.Fatalf("the add ended (%d) before its gate opened", status)
	default:
	}
	open()
	select {
	case status := <-added:
		if status != http.StatusCreated {
			t.Fatalf("the add: %d", status)
		}
	case <-time.After(hangWait):
		t.Fatal("the add never answered")
	}
	if !waitFor(hangWait, func() bool { return len(ct.srv.capacity.queuing) == 0 }) {
		t.Fatal("the view's slot was never given back")
	}
}

// TestABriefsReceiptIsTheTurns: the guide's receipt for a brief's submit
// names the fields a turn is told of its request, never the request as it
// is kept.
func TestABriefsReceiptIsTheTurns(t *testing.T) {
	ops := Operations()
	i := slices.IndexFunc(ops, func(o Operation) bool { return o.Name == "brief_submit" })
	if i < 0 {
		t.Fatal("no brief_submit")
	}
	receipts := guideReceipts()
	j := slices.IndexFunc(receipts, func(r guideReceipt) bool { return r.Name == ops[i].Receipt })
	if j < 0 {
		t.Fatalf("brief_submit's receipt %q is not in the receipts section", ops[i].Receipt)
	}
	names := func(fs []guideField) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Name)
		}
		return out
	}
	got, want := names(receipts[j].Fields), names(receiptFields(reflect.TypeFor[turnRequest]()))
	if !slices.Equal(got, want) || slices.Contains(got, "capacity") {
		t.Fatalf("brief_submit's receipt %s names %v, want the turn's %v", ops[i].Receipt, got, want)
	}
}
