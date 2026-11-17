package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The opened marks (BKUI, #993): each credential's actor keeps its own
// count of briefs not yet opened, on the server, so every browser holding
// one credential shows one number.

// Two identity principals beside the workbench token.
const (
	openedAnn = "cloud-ann-token"
	openedBob = "cloud-bob-token"
)

// openedEnv is a Brakeman server with an identity provider knowing ann
// and bob, on a clock of the test's.
func openedEnv(t *testing.T) (*bkEnv, *clock) {
	t.Helper()
	id := &tokenIdentity{known: map[string]ext.Principal{openedAnn: cloudAdmin("ann"), openedBob: cloudAdmin("bob")}}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), bkAgents(), withIdentity(id))
	b := bkEnvOf(t, e)
	return b, b.fakeClock()
}

// briefed keeps a briefed request through the desk's store, a minute on.
func (b *bkEnv) briefed(t *testing.T, c *clock) string {
	t.Helper()
	c.add(time.Minute)
	return b.listed(t, brakeman.Request{Kind: brakeman.KindBriefing}, "Overnight", "One run failed.", []brakeman.Item{listFact(t, "It failed.", "run:"+listRun)}, "", "")
}

// unreadAs is tok's count as the GET answers it.
func (b *bkEnv) unreadAs(t *testing.T, tok string) []string {
	t.Helper()
	status, data, out := b.callAs(t, tok, "GET", "/api/brakeman/opened", nil)
	if status != http.StatusOK {
		t.Fatalf("GET opened: %d %.200s", status, data)
	}
	var ids []string
	for _, id := range out["unread"].([]any) {
		ids = append(ids, id.(string))
	}
	if _, err := time.Parse(time.RFC3339Nano, out["begun"].(string)); err != nil {
		t.Fatalf("begun %v", out["begun"])
	}
	return ids
}

// markAs marks ids opened as tok.
func (b *bkEnv) markAs(t *testing.T, tok string, ids ...string) []string {
	t.Helper()
	status, data, out := b.callAs(t, tok, "POST", "/api/brakeman/opened", map[string]any{"ids": ids})
	if status != http.StatusOK {
		t.Fatalf("POST opened: %d %.200s", status, data)
	}
	var unread []string
	for _, id := range out["unread"].([]any) {
		unread = append(unread, id.(string))
	}
	return unread
}

// TestOpenedMarksArePerActor: the workbench token and each identity
// principal keep counts of their own; each begins at its first read.
func TestOpenedMarksArePerActor(t *testing.T) {
	t.Parallel()
	b, c := openedEnv(t)
	for _, tok := range []string{adminToken, openedAnn, openedBob} {
		if got := b.unreadAs(t, tok); len(got) != 0 {
			t.Fatalf("a first read: %v", got)
		}
	}
	x, y := b.briefed(t, c), b.briefed(t, c)
	if got := b.markAs(t, openedAnn, x); !slices.Equal(got, []string{y}) {
		t.Fatalf("ann after marking %s: %v", x, got)
	}
	if got := b.unreadAs(t, adminToken); !slices.Equal(got, []string{y, x}) {
		t.Fatalf("the workbench token's count moved with ann's: %v", got)
	}
	if got := b.markAs(t, adminToken, y); !slices.Equal(got, []string{x}) {
		t.Fatalf("the workbench token after marking %s: %v", y, got)
	}
	if got := b.unreadAs(t, openedBob); !slices.Equal(got, []string{y, x}) {
		t.Fatalf("bob's count moved: %v", got)
	}
	if got := b.unreadAs(t, openedAnn); !slices.Equal(got, []string{y}) {
		t.Fatalf("ann's count: %v", got)
	}
}

// TestTheSameTokenCountsOnce: two clients holding one credential share
// one count: one marks, and the other's next read has it, with the
// event that tells it to read again, which names no one.
func TestTheSameTokenCountsOnce(t *testing.T) {
	t.Parallel()
	b, c := openedEnv(t)
	b.unreadAs(t, openedAnn)
	x := b.briefed(t, c)
	events := b.sse(t)
	if got := b.unreadAs(t, openedAnn); !slices.Equal(got, []string{x}) {
		t.Fatalf("the phone's count %v", got)
	}
	b.markAs(t, openedAnn, x) // the laptop
	if got := b.unreadAs(t, openedAnn); len(got) != 0 {
		t.Fatalf("the phone's count after the laptop opened it: %v", got)
	}
	ev := b.waitEvent(t, events, func(s string) bool { return strings.HasPrefix(s, "brakeman_opened ") })
	if ev != "brakeman_opened {}" {
		t.Fatalf("the event %q", ev)
	}
}

// TestTheFirstReadBeginsTheCountOnTheRoute: briefs kept before an
// actor's first read count as opened; a mark before any read begins the
// count too.
func TestTheFirstReadBeginsTheCountOnTheRoute(t *testing.T) {
	t.Parallel()
	b, c := openedEnv(t)
	old := b.briefed(t, c)
	c.add(time.Minute)
	if got := b.unreadAs(t, adminToken); len(got) != 0 {
		t.Fatalf("the first read: %v", got)
	}
	if got := b.markAs(t, openedBob, old); len(got) != 0 {
		t.Fatalf("bob's first call, a mark: %v", got)
	}
	x := b.briefed(t, c)
	for _, tok := range []string{adminToken, openedBob} {
		if got := b.unreadAs(t, tok); !slices.Equal(got, []string{x}) {
			t.Fatalf("%.10s: %v", tok, got)
		}
	}
}

// TestOpenedRefusals: a body that is not {ids} of 1 to 100 request ids
// within 4 KiB, or a query, is 400 invalid_request; a server that does
// not own Brakeman's records answers 503 brakeman_not_owner and writes
// nothing.
func TestOpenedRefusals(t *testing.T) {
	t.Parallel()
	b, c := openedEnv(t)
	x := b.briefed(t, c)
	hundred := slices.Repeat([]string{x}, brakeman.MaxOpenedMark)
	for _, body := range []any{
		[]byte(`{}`), []byte(`{"ids":[]}`), []byte(`{"ids":["br-0"]}`), []byte(`{"ids":["x"]}`), []byte(`{"ids":[1]}`),
		[]byte(`{"ids":["` + x + `"],"actor":"user:bob"}`), []byte(`not json`), []byte(`{"ids":["` + x + `"]}{}`),
		map[string]any{"ids": append(slices.Clone(hundred), x)},
		[]byte(`{"ids":["` + x + `"],"pad":"` + strings.Repeat("p", maxOpenedBody) + `"}`),
	} {
		status, data, out := b.callAs(t, adminToken, "POST", "/api/brakeman/opened", body)
		if status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
			t.Errorf("%.80s: %d %.200s", fmt.Sprint(body), status, data)
		}
	}
	// 100 ids of the longest form fit the body.
	long := slices.Repeat([]string{"br-" + strings.Repeat("9", 18)}, brakeman.MaxOpenedMark)
	if enc, _ := json.Marshal(map[string]any{"ids": long}); len(enc) > maxOpenedBody {
		t.Fatalf("100 of the longest ids take %d bytes", len(enc))
	}
	if status, data, _ := b.callAs(t, adminToken, "POST", "/api/brakeman/opened", map[string]any{"ids": long}); status != http.StatusOK {
		t.Fatalf("100 of the longest ids: %d %.200s", status, data)
	}
	for _, method := range []string{"GET", "POST"} {
		var body any
		if method == "POST" {
			body = map[string]any{"ids": []string{x}}
		}
		if status, data, out := b.callAs(t, adminToken, method, "/api/brakeman/opened?actor=user:bob", body); status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
			t.Errorf("%s with a query: %d %.200s", method, status, data)
		}
	}
	second := b.restart()
	for _, method := range []string{"GET", "POST"} {
		var body any
		if method == "POST" {
			body = map[string]any{"ids": []string{x}}
		}
		if status, data, out := second.callAs(t, adminToken, method, "/api/brakeman/opened", body); status != http.StatusServiceUnavailable || errorCode(out) != "brakeman_not_owner" {
			t.Errorf("%s on the second server: %d %.200s", method, status, data)
		}
	}
}

// TestOpenedIsTheAdminsAlone: no credential, a wrong one, the host's
// token, a share link and a turn's own session token are refused 401,
// and none of them begins a count.
func TestOpenedIsTheAdminsAlone(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	_, turn, _ := b.turnSession(t, id)
	_, share, err := b.srv.links.Create("s1", session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"", "wrong", "test-host-token", share, turn} {
		for _, method := range []string{"GET", "POST"} {
			var body any
			if method == "POST" {
				body = map[string]any{"ids": []string{id}}
			}
			if status, data, _ := b.callAs(t, tok, method, "/api/brakeman/opened", body); status != http.StatusUnauthorized {
				t.Errorf("%s with token %.12q: %d %.200s", method, tok, status, data)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(b.srv.cfg.DataDir, brakeman.Dir, brakeman.OpenedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused call began a count: %v", err)
	}
}

// TestTheOpenedEventNamesNoActor: a mark that changes something sends
// brakeman_opened with {} alone, within its bound; one that changes
// nothing sends none.
func TestTheOpenedEventNamesNoActor(t *testing.T) {
	t.Parallel()
	b, c := openedEnv(t)
	b.unreadAs(t, openedAnn)
	x := b.briefed(t, c)
	events := b.sse(t)
	b.markAs(t, openedAnn, x)
	b.markAs(t, openedAnn, x) // nothing new: no event
	y := b.briefed(t, c)
	b.markAs(t, openedAnn, y)
	// A sentinel after the three marks: the stream is in order, so every
	// event the marks sent is read before it.
	b.srv.events.brakeman("br-424242", brakeman.StateQueued)
	var got []string
	for done := false; !done; {
		select {
		case ev := <-events:
			if strings.HasPrefix(ev, "brakeman_opened ") {
				got = append(got, ev)
			}
			done = strings.Contains(ev, "br-424242")
		case <-time.After(30 * time.Second):
			t.Fatalf("the events %v", got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("three marks, two of them changes, sent %v", got)
	}
	for _, ev := range got {
		data := strings.TrimPrefix(ev, "brakeman_opened ")
		if data != "{}" || len(data) > maxBrakemanOpenedEvent || strings.Contains(ev, "ann") {
			t.Fatalf("the event %q", ev)
		}
	}
}
