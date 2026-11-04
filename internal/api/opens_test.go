package api

import (
	"bufio"
	"context"
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

// reply is a route's answer, read on another goroutine.
type reply struct {
	status int
	out    map[string]any
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
	if acts := openActivity(t, e, id); len(acts) != 1 || acts[0].Message != "link for edge (Profile 2) opened in the default browser: edge is not installed on that computer" {
		t.Fatalf("the activity %+v", acts)
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

// An offered link of an agent that names a browser keeps that browser and
// its profile in its activity entry, which holds only the message and the
// URL: with no desktop app, a link meant for Edge's Profile 2 says so.
func TestOfferedOpenRecordsTheBrowserAskedFor(t *testing.T) {
	e := openEnv(t, nil)
	id := e.createSession("edge-cat")
	tok := agentOf(t, e, id)
	resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "https://claude.ai/artifact/work"})
	if resp.StatusCode != http.StatusOK || out["outcome"] != OpenOffered || out["message"] != "offered in the workbench: no desktop app took it" {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	if acts := openActivity(t, e, id); len(acts) != 1 || acts[0].Message != "link for edge (Profile 2) offered in the workbench: no desktop app took it" || acts[0].URL != "https://claude.ai/artifact/work" {
		t.Fatalf("the activity %+v", acts)
	}
	// Every outcome's entry names the browser and its profile; one for an
	// agent that names none names no browser it was meant for.
	edge := &catalog.Browser{Name: catalog.BrowserEdge, Profile: "Profile 2"}
	for _, outcome := range []string{OpenedBrowser, OpenedDefault, OpenedMissing, OpenedFailed, OpenOffered, OpenHanded, openUnopened} {
		if w := activityWords(outcome, edge); !strings.Contains(w, "edge (Profile 2)") || !strings.HasPrefix(w, "link ") {
			t.Fatalf("%s: %q", outcome, w)
		}
		if w := activityWords(outcome, nil); strings.HasPrefix(w, "link for ") || w != "link "+openWords(outcome, nil) {
			t.Fatalf("%s for no browser: %q", outcome, w)
		}
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
	// Nothing in the product bounds how soon the server sees the stream
	// end, so nothing here does: the test's own timeout bounds a hang.
	for e.srv.events.desktopsStreaming() != 0 {
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

// A managed run's attempt and a Brakeman turn, background or not, are
// never opened at once: with a desktop app streaming, which would claim a
// pending link, their links are offered without a wait, whatever the wait;
// the first open event is the offer, and nothing is pending to claim.
func TestOpenOfAnAttemptOrABrakemanTurnIsOffered(t *testing.T) {
	for name, ref := range map[string]session.CrewRef{
		"an attempt":                 {RunID: "r1", CrewID: "c1", Member: "lead", Task: "build", Attempt: 1},
		"a background Brakeman turn": {CrewID: "c1", Member: "brakeman", Brakeman: &session.BrakemanMark{Request: "b1", Quiet: true}},
		"a Brakeman turn":            {CrewID: "c1", Member: "brakeman", Brakeman: &session.BrakemanMark{Request: "b2"}},
	} {
		t.Run(name, func(t *testing.T) {
			e := openEnv(t, nil)
			e.srv.opens.claimWait = time.Hour
			events := desktopStream(t, e)
			id := e.createSession("edge-cat")
			tok := agentOf(t, e, id)
			d, _ := e.srv.Registry().Get(id)
			d.(*session.Local).SetCrew(ref)
			resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "https://example.test/"})
			if resp.StatusCode != http.StatusOK || out["outcome"] != OpenOffered {
				t.Fatalf("%d %v", resp.StatusCode, out)
			}
			first := e.waitEvent(t, events, func(s string) bool { return strings.HasPrefix(s, "open ") })
			var ev openEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(first, "open ")), &ev); err != nil || ev.State != openOffered || ev.Browser == nil || ev.Browser.Name != "edge" {
				t.Fatalf("the first open event %s: %v", first, err)
			}
			if resp, _ := e.do("POST", "/api/opens/"+ev.ID+"/claim", adminToken, nil); resp.StatusCode != http.StatusNotFound {
				t.Fatalf("a claim of the offer: %d", resp.StatusCode)
			}
			// The entry keeps the browser the link was meant for.
			if acts := openActivity(t, e, id); len(acts) != 1 || acts[0].Message != "link for edge (Profile 2) offered in the workbench: no desktop app took it" {
				t.Fatalf("the activity %+v", acts)
			}
		})
	}
}

// A link the desktop app claimed and could not open (its bridge refused it)
// is told as offered: every workbench is offered it, and the agent and the
// activity are told so at once, not "handed" once the result's wait is
// over.
func TestOpenTheAppCouldNotOpenIsOffered(t *testing.T) {
	e := openEnv(t, nil)
	e.srv.opens.claimWait = time.Hour
	e.srv.opens.resultWait = time.Hour // only the result ends the wait
	events := desktopStream(t, e)
	id := e.createSession("edge-cat")
	tok := agentOf(t, e, id)
	const link = "https://claude.ai/artifact/0f1e2d3c"
	done := make(chan map[string]any, 1)
	go func() {
		_, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": link})
		done <- out
	}()
	pending := waitOpen(t, e, events, openPending)
	if resp, _ := e.do("POST", "/api/opens/"+pending.ID+"/claim", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the claim: %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/opens/"+pending.ID+"/result", adminToken, map[string]any{"outcome": OpenOffered}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the result: %d", resp.StatusCode)
	}
	offered := waitOpen(t, e, events, openOffered)
	if offered.ID != pending.ID || offered.URL != link || offered.Browser == nil || offered.Browser.Name != "edge" {
		t.Fatalf("the offer %+v after %+v", offered, pending)
	}
	const words = "offered in the workbench: the desktop app could not open it"
	if out := <-done; out["outcome"] != OpenOffered || out["message"] != words {
		t.Fatalf("the agent's answer %v", out)
	}
	if acts := openActivity(t, e, id); len(acts) != 1 || acts[0].Message != "link for edge (Profile 2) "+words || acts[0].URL != link {
		t.Fatalf("the activity %+v", acts)
	}
}

// A result the desktop app tells as the open's wait for it ends (its timer,
// or the agent's call gone) is the open's answer, never a 204 the open
// then ignores: told in the moment between the wait's end and the open's
// settling (the settling seam holds it there), an offered result offers
// the link and is answered so; a result told once the open has settled
// finds no open (404), which the app offers itself.
func TestOpenResultToldAsTheWaitEndsIsTheAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		outcome, answer, message, activity string
		offer                              bool
	}{
		"offered": {OpenOffered, OpenOffered, "offered in the workbench: the desktop app could not open it", "link for edge (Profile 2) offered in the workbench: the desktop app could not open it", true},
		"browser": {OpenedBrowser, OpenedBrowser, "opened in edge (Profile 2)", "link opened in edge (Profile 2)", false},
	} {
		t.Run(name, func(t *testing.T) {
			e := openEnv(t, nil)
			e.srv.opens.claimWait = time.Hour
			e.srv.opens.resultWait = 0 // the wait is over at once
			told := make(chan int, 1)
			e.srv.opens.settling = func(id string) {
				resp, _ := e.do("POST", "/api/opens/"+id+"/result", adminToken, map[string]any{"outcome": tc.outcome})
				told <- resp.StatusCode
			}
			events := desktopStream(t, e)
			id := e.createSession("edge-cat")
			tok := agentOf(t, e, id)
			done := make(chan map[string]any, 1)
			go func() {
				_, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "https://example.test/race"})
				done <- out
			}()
			pending := waitOpen(t, e, events, openPending)
			if resp, _ := e.do("POST", "/api/opens/"+pending.ID+"/claim", adminToken, nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("the claim: %d", resp.StatusCode)
			}
			if status := <-told; status != http.StatusNoContent {
				t.Fatalf("the result told as the wait ended: %d", status)
			}
			if out := <-done; out["outcome"] != tc.answer || out["message"] != tc.message {
				t.Fatalf("the agent's answer %v", out)
			}
			if tc.offer {
				if offered := waitOpen(t, e, events, openOffered); offered.ID != pending.ID {
					t.Fatalf("the offer %+v", offered)
				}
			}
			if acts := openActivity(t, e, id); len(acts) != 1 || acts[0].Message != tc.activity {
				t.Fatalf("the activity %+v", acts)
			}
		})
	}
	t.Run("after the settling", func(t *testing.T) {
		e := openEnv(t, nil)
		e.srv.opens.claimWait = time.Hour
		e.srv.opens.resultWait = 0
		events := desktopStream(t, e)
		id := e.createSession("edge-cat")
		tok := agentOf(t, e, id)
		done := make(chan map[string]any, 1)
		go func() {
			_, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "https://example.test/late"})
			done <- out
		}()
		pending := waitOpen(t, e, events, openPending)
		e.do("POST", "/api/opens/"+pending.ID+"/claim", adminToken, nil)
		if out := <-done; out["outcome"] != OpenHanded {
			t.Fatalf("the agent's answer %v", out)
		}
		if resp, _ := e.do("POST", "/api/opens/"+pending.ID+"/result", adminToken, map[string]any{"outcome": OpenOffered}); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("a result after the open settled: %d", resp.StatusCode)
		}
	})
}

// The agent's call gone (its context cancelled) while the app it handed the
// link to is about to tell: a result told before the open settles is its
// answer, one told after finds no open.
func TestOpenResultToldAsTheCallEndsIsTheAnswer(t *testing.T) {
	e := openEnv(t, nil)
	e.srv.opens.claimWait = time.Hour
	e.srv.opens.resultWait = time.Hour // only the call's end ends the wait
	told := make(chan int, 1)
	e.srv.opens.settling = func(id string) {
		resp, _ := e.do("POST", "/api/opens/"+id+"/result", adminToken, map[string]any{"outcome": OpenedDefault})
		told <- resp.StatusCode
	}
	events := desktopStream(t, e)
	id := e.createSession("edge-cat")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type answer struct {
		outcome string
		err     error
	}
	done := make(chan answer, 1)
	go func() {
		outcome, err := e.srv.open(ctx, id, "https://example.test/gone", nil, false)
		done <- answer{outcome, err}
	}()
	pending := waitOpen(t, e, events, openPending)
	if resp, _ := e.do("POST", "/api/opens/"+pending.ID+"/claim", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the claim: %d", resp.StatusCode)
	}
	cancel()
	if status := <-told; status != http.StatusNoContent {
		t.Fatalf("the result told as the call ended: %d", status)
	}
	if a := <-done; a.err != nil || a.outcome != OpenedDefault {
		t.Fatalf("the open's answer %q %v", a.outcome, a.err)
	}
	if resp, _ := e.do("POST", "/api/opens/"+pending.ID+"/result", adminToken, map[string]any{"outcome": OpenedDefault}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a result after the open settled: %d", resp.StatusCode)
	}
}

// settle takes a result tell left in the channel, whichever way the wait
// ended, and once it has run no result is taken.
func TestOpenDeskSettleTakesWhatWasTold(t *testing.T) {
	d := newOpenDesk()
	p, _ := d.add(openEvent{ID: "op-1"})
	d.take("op-1")
	if d.tell("op-1", OpenedBrowser) != http.StatusOK {
		t.Fatal("the result")
	}
	if outcome, ok := d.settle(p); !ok || outcome != OpenedBrowser {
		t.Fatalf("settled %q %v", outcome, ok)
	}
	q, _ := d.add(openEvent{ID: "op-2"})
	d.take("op-2")
	if _, ok := d.settle(q); ok {
		t.Fatal("settled a result never told")
	}
	if status := d.tell("op-2", OpenedBrowser); status != http.StatusNotFound {
		t.Fatalf("a result after the settling: %d", status)
	}
}

// missing and failed name the browser the agent's settings name: told of a
// link whose agent names none they are refused, and the outcome told after
// is the one the agent is answered.
func TestOpenResultMissingNeedsABrowser(t *testing.T) {
	e := openEnv(t, nil)
	e.srv.opens.claimWait = time.Hour
	e.srv.opens.resultWait = time.Hour
	events := desktopStream(t, e)
	id := e.createSession("cat")
	tok := agentOf(t, e, id)
	done := make(chan reply, 1)
	go func() {
		resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "https://example.test/"})
		done <- reply{resp.StatusCode, out}
	}()
	ev := waitOpen(t, e, events, openPending)
	if ev.Browser != nil {
		t.Fatalf("a browser for an agent that names none: %+v", ev)
	}
	e.do("POST", "/api/opens/"+ev.ID+"/claim", adminToken, nil)
	for _, outcome := range []string{OpenedMissing, OpenedFailed} {
		if resp, out := e.do("POST", "/api/opens/"+ev.ID+"/result", adminToken, map[string]any{"outcome": outcome}); resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_request" {
			t.Fatalf("%s for no browser: %d %v", outcome, resp.StatusCode, out)
		}
	}
	if resp, _ := e.do("POST", "/api/opens/"+ev.ID+"/result", adminToken, map[string]any{"outcome": OpenedDefault}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the result: %d", resp.StatusCode)
	}
	if r := <-done; r.status != http.StatusOK || r.out["outcome"] != OpenedDefault || r.out["message"] != "opened in the default browser" {
		t.Fatalf("the agent's answer: %d %v", r.status, r.out)
	}
	// The words never need a browser to be said.
	for _, outcome := range []string{OpenedBrowser, OpenedDefault, OpenedMissing, OpenedFailed, OpenOffered, OpenHanded, openUnopened} {
		if openWords(outcome, nil) == "" {
			t.Fatalf("no words for %s", outcome)
		}
	}
}

// An open is recorded however many events the agent reported before it: its
// entry spends none of the event bucket. The bucket earns a token every
// 50 ms, so the route's record may find one here; that the entry never asks
// is TestRecordServerSpendsNoToken's, in internal/session, with a bucket
// that earns none.
func TestOpenIsRecordedWithTheEventBucketSpent(t *testing.T) {
	e := openEnv(t, nil)
	id := e.createSession("cat")
	tok := agentOf(t, e, id)
	d, _ := e.srv.Registry().Get(id)
	local := d.(*session.Local)
	for i := 0; local.Record(session.ActivityEntry{Type: session.ActivityToolUse, Message: "tool " + itoa(i)}); i++ {
	}
	resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": "https://example.test/spent"})
	if resp.StatusCode != http.StatusOK || out["outcome"] != OpenOffered {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	if acts := openActivity(t, e, id); len(acts) != 1 || acts[0].URL != "https://example.test/spent" {
		t.Fatalf("the activity %+v", acts)
	}
}

// Every link the route takes is recorded with its URL: CleanEntry drops a
// URL whose entry passes a CONTROL frame, which & < > (six bytes each in
// JSON) do well within 2048 bytes, so the route refuses such a link
// (400 invalid_url, no open, nothing recorded)
// and records the longest it takes whole, for an agent that names a
// browser and for one that names none; a link of 2048 plain bytes is taken.
func TestOpenedURLIsKeptInTheActivity(t *testing.T) {
	for _, agent := range []string{"edge-cat", "cat"} {
		t.Run(agent, func(t *testing.T) {
			e := openEnv(t, nil)
			id := e.createSession(agent)
			tok := agentOf(t, e, id)
			var browser *catalog.Browser
			if a, ok := e.srv.Catalog().Get(agent); ok && a.Browser.Named() {
				browser = a.Browser
			}
			const base = "https://example.test/?"
			over := base + strings.Repeat("&", 2000)
			if resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": over}); resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_url" {
				t.Fatalf("a link its entry would drop: %d %v", resp.StatusCode, out)
			}
			if acts := openActivity(t, e, id); len(acts) != 0 {
				t.Fatalf("a refused link was recorded: %+v", acts)
			}
			// The longest run of & the route takes, found as it judges.
			n := 0
			for keptInActivity(base+strings.Repeat("&", n+1), browser) {
				n++
			}
			if n == 0 || len(base)+n >= len(over) {
				t.Fatalf("the boundary %d", n)
			}
			for _, link := range []string{base + strings.Repeat("&", n), "https://example.test/" + strings.Repeat("a", MaxOpenURL-len("https://example.test/"))} {
				resp, out := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": link})
				if resp.StatusCode != http.StatusOK || out["outcome"] != OpenOffered {
					t.Fatalf("a link of %d bytes: %d %v", len(link), resp.StatusCode, out)
				}
			}
			if resp, _ := e.do("POST", "/api/sessions/"+id+"/open", tok, map[string]any{"url": base + strings.Repeat("&", n+1)}); resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("one & past the boundary: %d", resp.StatusCode)
			}
			acts := openActivity(t, e, id)
			if len(acts) != 2 || acts[0].URL != base+strings.Repeat("&", n) || len(acts[1].URL) != MaxOpenURL {
				t.Fatalf("the activity %+v", acts)
			}
		})
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
// browserLauncher launches agents on a server of the config given and
// answers the BROWSER each launched with, if any.
func browserLauncher(t *testing.T, mutate func(*config.Config)) func(agent string) (string, bool) {
	t.Helper()
	seam := nativeSeam()
	e := newTestEnvAgents(t, mutate, slog.New(slog.NewTextHandler(io.Discard, nil)), openAgents, withNativeRuntime(seam))
	return func(agent string) (string, bool) {
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
}

func TestBrowserIsTheOpenerOnlyForAnAgentWithABrowser(t *testing.T) {
	t.Setenv("BROWSER", "/opt/servers/browser")
	browserOf := browserLauncher(t, nil)
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

// With agents.selfService off the open route refuses every open, so no
// agent gets Conductor's opener: an agent with a browser set opens links its
// own way, as one with none does.
func TestBrowserIsNotTheOpenerWithSelfServiceOff(t *testing.T) {
	off := false
	browserOf := browserLauncher(t, func(c *config.Config) { c.Agents.SelfService = &off })
	if v, set := browserOf("edge-cat"); set {
		t.Fatalf("edge-cat has BROWSER=%s with self-service off", v)
	}
	if v, set := browserOf("cat"); set {
		t.Fatalf("cat has BROWSER=%s", v)
	}
}
