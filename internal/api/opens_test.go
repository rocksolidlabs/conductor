package api

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// openAgents are a cat whose links open in Edge's Profile 2, one in
// Firefox with a BROWSER of its own, and one in Brave with BROWSER passed
// through from the server.
var openAgents = []catalog.Agent{
	{ID: "edge-cat", Name: "edge cat", Command: []string{"/bin/cat"}, Browser: &catalog.Browser{Name: catalog.BrowserEdge, Profile: "Profile 2"}},
	{ID: "own-cat", Name: "own cat", Command: []string{"/bin/cat"}, Browser: &catalog.Browser{Name: catalog.BrowserFirefox}, Env: map[string]string{"BROWSER": "/usr/local/bin/mine"}},
	{ID: "pass-cat", Name: "pass cat", Command: []string{"/bin/cat"}, Browser: &catalog.Browser{Name: catalog.BrowserBrave}, EnvPassthrough: []string{"BROWSER"}},
}

func openEnv(t *testing.T, mutate func(*config.Config)) *testEnv {
	t.Helper()
	return newTestEnvAgents(t, mutate, slog.New(slog.NewTextHandler(io.Discard, nil)), openAgents)
}

// desktopStream opens GET /api/events as a desktop app's workbench does
// (desktopHeader), counted once it returns, and streams its events as sse
// does.
func desktopStream(t *testing.T, e *testEnv) <-chan string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), "GET", e.http.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set(desktopHeader, "1")
	resp, err := e.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the desktop's stream: %v %v", resp, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	events := make(chan string, 4096)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				select {
				case events <- name + " " + strings.TrimPrefix(line, "data: "):
				case <-t.Context().Done():
					return
				}
			}
		}
	}()
	return events
}

// waitOpen waits for an open event of the state given, decoded.
func waitOpen(t *testing.T, e *testEnv, events <-chan string, state string) openEvent {
	t.Helper()
	ev := e.waitEvent(t, events, func(s string) bool {
		return strings.HasPrefix(s, "open ") && strings.Contains(s, `"state":"`+state+`"`)
	})
	var o openEvent
	if err := json.Unmarshal([]byte(strings.TrimPrefix(ev, "open ")), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

// openActivity is the session's artifact entries.
func openActivity(t *testing.T, e *testEnv, id string) []session.ActivityEntry {
	t.Helper()
	d, _ := e.srv.Registry().Get(id)
	var out []session.ActivityEntry
	for _, en := range d.(*session.Local).Activity() {
		if en.Type == session.ActivityArtifact {
			out = append(out, en)
		}
	}
	return out
}

// A desktop app claims the open it is told of, once, and says what it did:
// the agent's call answers that, with its browser, and the session's
// activity records it with the URL.
func TestOpenIsClaimedByOneDesktopAppAndAnswered(t *testing.T) {
	e := openEnv(t, nil)
	e.srv.opens.claimWait = time.Hour // only the claim ends the wait
	events := desktopStream(t, e)
	id := e.createSession("edge-cat")
	tok := agentOf(t, e, id)
	const link = "https://claude.ai/artifact/0f1e2d3c"
	type reply struct {
		status int
		out    map[string]any
	}
	done := make(chan reply, 1)
	go func() {
		resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": link})
		done <- reply{resp.StatusCode, out}
	}()
	ev := waitOpen(t, e, events, openPending)
	if ev.SessionID != id || ev.URL != link || ev.Browser == nil || ev.Browser.Name != "edge" || ev.Browser.Profile != "Profile 2" || !strings.HasPrefix(ev.ID, "op-") {
		t.Fatalf("the pending event %+v", ev)
	}
	// Only the workbench's token claims; the first claim has it, a second is refused.
	if resp, _ := e.do("POST", "/api/opens/"+ev.ID+"/claim", tok, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a claim with the agent's token: %d", resp.StatusCode)
	}
	resp, out := e.do("POST", "/api/opens/"+ev.ID+"/claim", adminToken, nil)
	if resp.StatusCode != http.StatusOK || out["open"].(map[string]any)["url"] != link {
		t.Fatalf("the claim: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("POST", "/api/opens/"+ev.ID+"/claim", adminToken, nil); resp.StatusCode != http.StatusConflict || errorCode(out) != "open_taken" {
		t.Fatalf("a second claim: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.do("POST", "/api/opens/"+ev.ID+"/result", adminToken, map[string]any{"outcome": "sideways"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown outcome: %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/opens/"+ev.ID+"/result", adminToken, map[string]any{"outcome": OpenedBrowser}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the result: %d", resp.StatusCode)
	}
	r := <-done
	if r.status != http.StatusOK || r.out["outcome"] != OpenedBrowser || r.out["message"] != "opened in edge (Profile 2)" {
		t.Fatalf("the agent's answer: %d %v", r.status, r.out)
	}
	if b := r.out["browser"].(map[string]any); b["name"] != "edge" || b["profile"] != "Profile 2" {
		t.Fatalf("the answer's browser %v", r.out)
	}
	// The open is gone once answered.
	if resp, _ := e.do("POST", "/api/opens/"+ev.ID+"/result", adminToken, map[string]any{"outcome": OpenedBrowser}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a result after the answer: %d", resp.StatusCode)
	}
	acts := openActivity(t, e, id)
	if len(acts) != 1 || acts[0].URL != link || acts[0].Message != "link opened in edge (Profile 2)" {
		t.Fatalf("the activity %+v", acts)
	}
}

// The browser named not being installed on the app's computer is said.
func TestOpenSaysTheBrowserIsMissing(t *testing.T) {
	e := openEnv(t, nil)
	e.srv.opens.claimWait = time.Hour
	events := desktopStream(t, e)
	id := e.createSession("edge-cat")
	tok := agentOf(t, e, id)
	done := make(chan map[string]any, 1)
	go func() {
		_, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "https://github.com/rocksolidlabs/conductor/pull/1"})
		done <- out
	}()
	ev := waitOpen(t, e, events, openPending)
	e.do("POST", "/api/opens/"+ev.ID+"/claim", adminToken, nil)
	e.do("POST", "/api/opens/"+ev.ID+"/result", adminToken, map[string]any{"outcome": OpenedMissing})
	if out := <-done; out["outcome"] != OpenedMissing || out["message"] != "opened in the default browser: edge is not installed on that computer" {
		t.Fatalf("the answer %v", out)
	}
}

// With no desktop app streaming the events, an open is offered in the
// workbench at once, with no wait and no pending event; the answer and the
// activity say it.
func TestOpenWithNoDesktopAppIsOfferedAtOnce(t *testing.T) {
	e := openEnv(t, nil)
	e.srv.opens.claimWait = time.Hour // an open that waited would not answer
	events := e.sse(t)
	id := e.createSession("cat")
	tok := agentOf(t, e, id)
	resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "http://localhost:3000/preview"})
	if resp.StatusCode != http.StatusOK || out["outcome"] != OpenOffered || out["message"] != "offered in the workbench: no desktop app took it" || out["browser"] != nil {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	ev := e.waitEvent(t, events, func(s string) bool { return strings.HasPrefix(s, "open ") })
	if !strings.Contains(ev, `"state":"offered"`) || !strings.Contains(ev, `"url":"http://localhost:3000/preview"`) || strings.Contains(ev, `"browser"`) {
		t.Fatalf("the first open event %s", ev)
	}
	if acts := openActivity(t, e, id); len(acts) != 1 || acts[0].Message != "link offered in the workbench: no desktop app took it" || acts[0].URL != "http://localhost:3000/preview" {
		t.Fatalf("the activity %+v", acts)
	}
}

// A desktop app that streams and does not claim (an app that is not
// looking) has the open offered once the wait is over: the offer follows
// the pending event, and a later claim is refused.
func TestOpenUnclaimedIsOffered(t *testing.T) {
	e := openEnv(t, nil)
	e.srv.opens.claimWait = 0
	events := desktopStream(t, e)
	id := e.createSession("edge-cat")
	tok := agentOf(t, e, id)
	resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "https://example.test/x"})
	if resp.StatusCode != http.StatusOK || out["outcome"] != OpenOffered {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	pending := waitOpen(t, e, events, openPending)
	offered := waitOpen(t, e, events, openOffered)
	if offered.ID != pending.ID || offered.URL != "https://example.test/x" || offered.Browser == nil || offered.Browser.Name != "edge" {
		t.Fatalf("the offer %+v after %+v", offered, pending)
	}
	if resp, _ := e.do("POST", "/api/opens/"+pending.ID+"/claim", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a claim after the offer: %d", resp.StatusCode)
	}
}

// The desktop app's stream counts while it streams, and no longer once it
// ends.
func TestDesktopStreamIsCounted(t *testing.T) {
	e := openEnv(t, nil)
	_ = e.sse(t)
	if n := e.srv.events.desktopsStreaming(); n != 0 {
		t.Fatalf("a plain stream counted: %d", n)
	}
	req, _ := http.NewRequestWithContext(t.Context(), "GET", e.http.URL+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set(desktopHeader, "1")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.srv.events.desktopsStreaming(); n != 1 {
		t.Fatalf("the desktop's stream: %d", n)
	}
	resp.Body.Close()
	deadline := time.Now().Add(10 * time.Second)
	for e.srv.events.desktopsStreaming() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the ended stream is still counted")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A claim that comes once the open is offered (the desk's own race) is
// refused, and an offer after a claim is not made.
func TestOpenDeskClaimAndOfferExcludeEachOther(t *testing.T) {
	d := newOpenDesk()
	p, _ := d.add(openEvent{ID: "op-1"})
	if !d.offerUnclaimed(p) {
		t.Fatal("an unclaimed open was not offered")
	}
	if _, status := d.take("op-1"); status != http.StatusConflict {
		t.Fatalf("a claim after the offer: %d", status)
	}
	q, _ := d.add(openEvent{ID: "op-2"})
	if _, status := d.take("op-2"); status != http.StatusOK {
		t.Fatalf("the claim: %d", status)
	}
	if d.offerUnclaimed(q) {
		t.Fatal("a claimed open was offered")
	}
	if d.tell("op-2", OpenedDefault) != http.StatusOK || d.tell("op-2", OpenedDefault) != http.StatusConflict {
		t.Fatal("a result told twice")
	}
	for i := range maxPendingOpens - 2 {
		if _, err := d.add(openEvent{ID: "op-x" + itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.add(openEvent{ID: "op-over"}); err != errTooManyOpens {
		t.Fatalf("past the bound: %v", err)
	}
}

// A managed run's attempt is never opened at once: its links are offered
// without a wait for a desktop app, whatever the wait.
func TestOpenOfAnAttemptIsOffered(t *testing.T) {
	e := openEnv(t, nil)
	e.srv.opens.claimWait = time.Hour
	events := e.sse(t)
	id := e.createSession("edge-cat")
	tok := agentOf(t, e, id)
	d, _ := e.srv.Registry().Get(id)
	d.(*session.Local).SetCrew(session.CrewRef{RunID: "r1", CrewID: "c1", Member: "lead", Task: "build", Attempt: 1})
	resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "https://example.test/"})
	if resp.StatusCode != http.StatusOK || out["outcome"] != OpenOffered {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	if ev := waitOpen(t, e, events, openOffered); ev.Browser == nil || ev.Browser.Name != "edge" {
		t.Fatalf("the offer %+v", ev)
	}
}

// The route takes the session's own agent token or the workbench token,
// never another session's, a share token or none; it takes an http(s) URL
// of at most 2048 bytes with no white space, quote or control character;
// it refuses a hosted session; it follows agents.selfService; and it opens
// ten links a minute, then 429.
func TestOpenRouteTokensURLAndBounds(t *testing.T) {
	e := openEnv(t, nil)
	e.srv.opens.claimWait = 0
	id := e.createSession("cat")
	tok := agentOf(t, e, id)
	other := e.createSession("cat")
	otherTok := agentOf(t, e, other)
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	shareTok := lo["token"].(string)
	ok := map[string]any{"url": "https://example.test/"}
	for name, bad := range map[string]string{"no token": "", "another session's agent": otherTok, "a share token": shareTok, "a host token": "test-host-token"} {
		if resp, _ := e.do("POST", "/api/sessions/"+id+"/open", bad, ok); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	for _, u := range []string{"", "ftp://example.test/", "javascript:alert(1)", "file:///etc/passwd", "https://", "https://example.test/a b", "https://example.test/\"x", "https://example.test/'x", "https://example.test/\x01", "https://example.test/ ", "https://example.test/" + strings.Repeat("a", MaxOpenURL)} {
		if resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": u}); resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_url" {
			t.Errorf("%q: %d %v", u, resp.StatusCode, out)
		}
	}
	if resp, _ := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "https://example.test/", "browser": "edge"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a browser in the body: %d", resp.StatusCode)
	}
	// The longest URL taken, by the admin, which counts against the session too.
	long := "https://example.test/" + strings.Repeat("a", MaxOpenURL-len("https://example.test/"))
	if resp, out := e.do("POST", "/api/sessions/"+id+"/open", adminToken, map[string]any{"url": long}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the longest URL: %d %v", resp.StatusCode, out)
	}
	for i := range selfOpensPerMinute - 1 {
		if resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, ok); resp.StatusCode != http.StatusOK {
			t.Fatalf("open %d of the minute's ten: %d %v", i+2, resp.StatusCode, out)
		}
	}
	if resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, ok); resp.StatusCode != http.StatusTooManyRequests || errorCode(out) != "rate_limited" {
		t.Fatalf("the eleventh: %d %v", resp.StatusCode, out)
	}
	// Another session has a minute of its own.
	if resp, _ := e.do("POST", "/api/sessions/"+other+"/open", otherTok, ok); resp.StatusCode != http.StatusOK {
		t.Fatalf("another session: %d", resp.StatusCode)
	}
	host := dialFakeHost(t, e, "hosted-agent-token")
	if resp, out := e.do("POST", "/api/sessions/"+host.sessionID+"/open", "hosted-agent-token", ok); resp.StatusCode != http.StatusBadRequest || errorCode(out) != "hosted_session" {
		t.Fatalf("a hosted session: %d %v", resp.StatusCode, out)
	}
}

// The minute's bound with a clock of its own: ten, then none, then ten again.
func TestOpenIsBoundedPerMinute(t *testing.T) {
	c := newSelfCounters()
	now := time.Now()
	c.now = func() time.Time { return now }
	for i := range selfOpensPerMinute {
		if !c.take(c.opens, "s1", selfOpensPerMinute, time.Minute) {
			t.Fatalf("open %d refused", i)
		}
	}
	if c.take(c.opens, "s1", selfOpensPerMinute, time.Minute) {
		t.Fatal("an eleventh in the minute")
	}
	now = now.Add(61 * time.Second)
	if !c.take(c.opens, "s1", selfOpensPerMinute, time.Minute) {
		t.Fatal("a minute later")
	}
}

func TestOpenFollowsSelfService(t *testing.T) {
	off := false
	e := openEnv(t, func(c *config.Config) { c.Agents.SelfService = &off })
	id := e.createSession("cat")
	tok := agentOf(t, e, id)
	if resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "https://example.test/"}); resp.StatusCode != http.StatusForbidden || errorCode(out) != "self_service_off" {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
}

// The longest open event there can be is within its bound, so none is
// dropped: a URL of the most escapes and the longest profile.
func TestOpenEventIsBounded(t *testing.T) {
	ev := openEvent{ID: "op-0123456789abcdef", SessionID: strings.Repeat("s", 64), URL: "https://e.test/" + strings.Repeat("<", MaxOpenURL-len("https://e.test/")), Browser: &catalog.Browser{Name: catalog.BrowserChrome, Profile: strings.Repeat(" ", catalog.MaxBrowserProfile)}, State: openPending}
	b, err := json.Marshal(ev)
	if err != nil || len(b) > maxOpenEvent {
		t.Fatalf("the longest open event is %d bytes, past %d: %v", len(b), maxOpenEvent, err)
	}
}

// BROWSER names Conductor's opener for an agent with a browser set, and for
// no other; an agent's own BROWSER, from its env or passed through, wins.
func TestBrowserIsTheOpenerOnlyForAnAgentWithABrowser(t *testing.T) {
	t.Setenv("BROWSER", "/opt/servers/browser")
	seam := nativeSeam()
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), openAgents, withNativeRuntime(seam))
	browserOf := func(agent string) (string, bool) {
		t.Helper()
		resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": agent})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: %d %v", agent, resp.StatusCode, out)
		}
		id := out["id"].(string)
		t.Cleanup(func() { e.stopAndWait(id) })
		calls := seam.Asked("StartTerminal")
		env := calls[len(calls)-1].Spec.(ext.TerminalSpec).Env
		var vals []string
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "BROWSER="); ok {
				vals = append(vals, v)
			}
		}
		if len(vals) > 1 {
			t.Fatalf("%s has %d BROWSERs: %q", agent, len(vals), vals)
		}
		if len(vals) == 0 {
			return "", false
		}
		return vals[0], true
	}
	if v, set := browserOf("cat"); set {
		t.Fatalf("an agent with no browser has BROWSER=%s", v)
	}
	opener, set := browserOf("edge-cat")
	if !set || !agents.OpenerPathOK(opener) || strings.ContainsAny(opener, " :") {
		t.Fatalf("edge-cat's BROWSER %q", opener)
	}
	script, err := os.ReadFile(opener)
	if err != nil || !strings.Contains(string(script), ` open -- "$1"`) {
		t.Fatalf("the opener %q: %v", script, err)
	}
	if fi, err := os.Stat(opener); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the opener's mode: %v %v", fi, err)
	}
	if v, _ := browserOf("own-cat"); v != "/usr/local/bin/mine" {
		t.Fatalf("own-cat's BROWSER %q", v)
	}
	if v, _ := browserOf("pass-cat"); v != "/opt/servers/browser" {
		t.Fatalf("pass-cat's BROWSER %q", v)
	}

}
