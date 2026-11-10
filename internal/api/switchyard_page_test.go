package api

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor"
	"github.com/rocksolidlabs/conductor/internal/certs"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

func getPage(t *testing.T, e *testEnv, p string) (int, string) {
	t.Helper()
	resp, err := e.client.Get(e.http.URL + p)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// A switchyard serves its own landing page at /, a 404 page for every
// workbench path, and the app only for joining and for its assets; a plain
// server serves the app as before.
func TestSwitchyardServesItsOwnPages(t *testing.T) {
	e := switchyardEnv(t, true)
	e.srv.web = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app:"+r.URL.Path) })
	code, body := getPage(t, e, "/")
	for _, want := range []string{"This is a Conductor switchyard.", "Were you sent a link?", "Looking for the workbench?", "Sharing from your machine?", "This server is up", "It mints and checks the links", "which can read it, when the network allows no direct path", "publish here only if you trust its operator", "conductor://", "Paste the workbench token", "No cookies."} {
		if code != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("landing: %d, missing %q in %.300s", code, want, body)
		}
	}
	if strings.Contains(body, adminToken) {
		t.Fatal("the landing page leaks the token")
	}
	for _, p := range []string{"/crews/users-api", "/sessions/abc", "/wall", "/yard", "/roundhouse", "/agents", "/events", "/settings", "/join", "/runs/x"} {
		code, body := getPage(t, e, p)
		if code != http.StatusNotFound || !strings.Contains(body, "The workbench isn't here.") || !strings.Contains(body, "404 · "+p) {
			t.Fatalf("%s: %d %.300s", p, code, body)
		}
	}
	for _, p := range []string{"/join/tok", "/paste", "/_nuxt/x.js", "/brand/conductor-mark.svg", "/favicon.svg", "/200.html"} {
		code, body := getPage(t, e, p)
		if code != http.StatusOK || body != "app:"+p {
			t.Fatalf("%s: %d %q", p, code, body)
		}
	}
	if resp, err := e.client.Post(e.http.URL+"/", "text/plain", strings.NewReader("x")); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /: %v %v", err, resp)
	}
	// The path shown on the 404 page is bounded.
	if code, body := getPage(t, e, "/"+strings.Repeat("a", 300)); code != http.StatusNotFound || !strings.Contains(body, "…") {
		t.Fatalf("long path: %d", code)
	}

	off := switchyardEnv(t, false)
	if _, body := getPage(t, off, "/"); !strings.Contains(body, "This server's relay is off") || !strings.Contains(body, "Off · direct connections only") {
		t.Fatalf("relay off landing: %.300s", body)
	}

	plain := newTestEnv(t, nil)
	if code, body := getPage(t, plain, "/"); code == http.StatusOK && strings.Contains(body, "This is a Conductor switchyard.") {
		t.Fatalf("a plain server served the switchyard page: %d", code)
	}
}

// The operator's figures: the route needs the workbench token, exists only
// on a switchyard, and counts live hosted sessions by host with their viewers.
func TestSwitchyardStatusCountsHostsAndViewers(t *testing.T) {
	e := switchyardEnv(t, true)
	if resp, _ := e.do("GET", "/api/switchyard/status", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	resp, out := e.do("GET", "/api/switchyard/status", adminToken, nil)
	if resp.StatusCode != http.StatusOK || out["hosts"] != 0.0 || out["sessions"] != 0.0 || out["relayBytesHour"] != 0.0 {
		t.Fatalf("empty: %d %v", resp.StatusCode, out)
	}
	host := dialFakeHost(t, e, "hosted-agent-token")
	resp, out = e.do("GET", "/api/switchyard/status", adminToken, nil)
	list, _ := out["hostList"].([]any)
	if resp.StatusCode != http.StatusOK || out["hosts"] != 1.0 || out["sessions"] != 1.0 || out["viewers"] != 0.0 || len(list) != 1 {
		t.Fatalf("one host: %d %v", resp.StatusCode, out)
	}
	if h := list[0].(map[string]any); h["name"] != "laptop" || h["sessions"] != 1.0 || h["since"] == nil {
		t.Fatalf("host row %v", h)
	}
	v := dialViewer(t, e, host.sessionID, host.link("view", "a friend"))
	v.hello(80, 24)
	v.expectControl(proto.CtlWelcome)
	host.expect(proto.HostViewerJoin)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, out = e.do("GET", "/api/switchyard/status", adminToken, nil)
		if out["viewers"] == 1.0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("viewer not counted: %v", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	plain := newTestEnv(t, nil)
	if resp, _ := plain.do("GET", "/api/switchyard/status", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("plain server: %d", resp.StatusCode)
	}
}

// The join lookup says whether the server is a switchyard, so the join page
// can name it.
func TestJoinSaysWhenTheServerIsASwitchyard(t *testing.T) {
	e := switchyardEnv(t, true)
	host := dialFakeHost(t, e, "hosted-agent-token")
	if _, out := e.do("GET", "/api/join/"+host.link("view", ""), "", nil); out["switchyard"] != true {
		t.Fatalf("join %v", out)
	}
}

func TestRelayMeterSumsTheLastHour(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m := newRelayMeter(func() time.Time { return now })
	m.add(100)
	now = now.Add(30 * time.Minute)
	m.add(50)
	if got := m.lastHour(); got != 150 {
		t.Fatalf("within the hour: %d", got)
	}
	now = now.Add(31 * time.Minute)
	if got := m.lastHour(); got != 50 {
		t.Fatalf("after the first fell out: %d", got)
	}
	now = now.Add(2 * time.Hour)
	if got := m.lastHour(); got != 0 {
		t.Fatalf("after a quiet two hours: %d", got)
	}
	var none *relayMeter
	none.add(1)
	if none.lastHour() != 0 {
		t.Fatal("nil meter")
	}
}

func TestSwitchyardPageWords(t *testing.T) {
	for d, want := range map[time.Duration]string{3 * time.Minute: "3 minutes", 1 * time.Minute: "1 minute", 4*time.Hour + 12*time.Minute: "4 hours 12 minutes", 12*24*time.Hour + 4*time.Hour: "12 days 4 hours", 25 * time.Hour: "1 day 1 hour"} {
		if got := uptimeWords(d); got != want {
			t.Errorf("uptimeWords(%s) = %q, want %q", d, got, want)
		}
	}
	for kb, want := range map[int]string{512: "512 KB/s", 1024: "1 MB/s", 8192: "8 MB/s", 1536: "1.5 MB/s"} {
		if got := kbpsWords(kb); got != want {
			t.Errorf("kbpsWords(%d) = %q, want %q", kb, got, want)
		}
	}
}

// pageCerts is a certificate manager in a chosen state, for the status card.
type pageCerts struct{ st certs.Status }

func (c pageCerts) Ready() bool                  { return c.st.Ready }
func (c pageCerts) Status() certs.Status         { return c.st }
func (c pageCerts) HTTP01(string) (string, bool) { return "", false }

// The status card's TLS line and the amber renewal warning, shown to everyone.
func TestSwitchyardLandingTLSLine(t *testing.T) {
	e := switchyardEnv(t, true)
	if _, body := getPage(t, e, "/"); !strings.Contains(body, "Off · plain http") {
		t.Fatalf("plain: %.300s", body)
	}
	renew := time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC)
	expire := time.Date(2026, 10, 6, 2, 14, 0, 0, time.UTC)
	e.srv.certs = pageCerts{certs.Status{Ready: true, RenewAt: renew, NotAfter: expire}}
	if _, body := getPage(t, e, "/"); !strings.Contains(body, "On · certificate renews Nov 2, 2026") || strings.Contains(body, `class="warnline"`) {
		t.Fatalf("ready: %.300s", body)
	}
	e.srv.certs = pageCerts{certs.Status{Ready: true, RenewAt: renew, NotAfter: expire, LastError: "acme: boom"}}
	if _, body := getPage(t, e, "/"); !strings.Contains(body, "Certificate renewal failed. The current certificate expires Oct 6, 02:14 UTC.") || !strings.Contains(body, "On · certificate expires Oct 6, 02:14 UTC") {
		t.Fatalf("failed renewal: %.300s", body)
	}
	e.srv.certs = pageCerts{certs.Status{LastError: "acme: boom", NextTry: expire}}
	if _, body := getPage(t, e, "/"); !strings.Contains(body, "Waiting for a certificate") || !strings.Contains(body, "the next try is at Oct 6, 02:14 UTC") {
		t.Fatalf("no certificate yet: %.300s", body)
	}
}

// The "Sharing from your machine?" card says what a publisher needs: nothing
// on a switchyard that admits open hosts, a host token on one that does not.
func TestSwitchyardLandingSaysWhatAPublisherNeeds(t *testing.T) {
	closed := switchyardEnv(t, true)
	if _, body := getPage(t, closed, "/"); !strings.Contains(body, "Ask the operator of this switchyard for a host token") || strings.Contains(body, "Nothing to configure") {
		t.Fatalf("token-gated: %.300s", body)
	}
	open := openSwitchyardEnv(t, nil)
	if _, body := getPage(t, open, "/"); !strings.Contains(body, "Nothing to configure") || strings.Contains(body, "Ask the operator of this switchyard for a host token") || !strings.Contains(body, "A host token is optional") {
		t.Fatalf("open: %.300s", body)
	}
}

// The pages' Get the desktop app link is switchyard.appURL, and the
// repository's releases page without it. One out of form that reached the
// server without Validate, which refuses it at load, is the default, said in
// the log once and without the value.
func TestSwitchyardPagesLinkTheAppURL(t *testing.T) {
	link := func(url string) string { return `<a class="btn" href="` + url + `">Get the desktop app</a>` }
	pages := func(e *testEnv, want string) {
		t.Helper()
		for _, p := range []string{"/", "/crews"} {
			if _, body := getPage(t, e, p); strings.Count(body, link(want)) != 1 || strings.Count(body, ">Get the desktop app</a>") != 1 {
				t.Fatalf("%s does not link %s: %.300s", p, want, body)
			}
		}
	}
	pages(switchyardEnv(t, true), "https://github.com/rocksolidlabs/conductor/releases")
	pages(newTestEnv(t, func(c *config.Config) {
		c.Switchyard.Enabled = true
		c.Switchyard.AppURL = "https://example.test/download/conductor"
	}), "https://example.test/download/conductor")

	logs := &logBuffer{}
	bad := newTestEnvLogging(t, func(c *config.Config) { c.Switchyard.Enabled = true }, slog.New(slog.NewTextHandler(logs, nil)))
	bad.srv.cfg.Switchyard.AppURL = "http://example.test/download?key=secret"
	pages(bad, "https://github.com/rocksolidlabs/conductor/releases")
	pages(bad, "https://github.com/rocksolidlabs/conductor/releases")
	if n := strings.Count(logs.String(), "switchyard.appURL is not an https URL"); n != 1 {
		t.Fatalf("said %d times:\n%s", n, logs.String())
	}
	if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "example.test") {
		t.Fatalf("the log shows the value: %s", logs.String())
	}
}

// The pages are dark always, whatever the system prefers.
func TestSwitchyardPagesAreDark(t *testing.T) {
	e := switchyardEnv(t, true)
	for _, p := range []string{"/", "/crews"} {
		_, body := getPage(t, e, p)
		for _, want := range []string{
			":root{--conductor-surface:#18181B;--conductor-panel:#18181B;--conductor-text:#FFFFFF;",
			`<meta name="color-scheme" content="dark">`,
			"--conductor-action:#9BB3A3",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s lacks %q", p, want)
			}
		}
		if strings.Contains(body, "prefers-color-scheme") {
			t.Fatalf("%s switches with the system: it is dark always", p)
		}
	}
}

// Every switchyard page ends with the sponsor kit's badge at the lower right
// of its footer: "Sponsored by" and the RockSolid Labs logo, linking to
// rocksolidlabs.io.
func TestSwitchyardPagesCarryTheCredit(t *testing.T) {
	e := switchyardEnv(t, true)
	for _, p := range []string{"/", "/crews"} {
		_, body := getPage(t, e, p)
		if !strings.Contains(body, `<a class="right badge" href="https://rocksolidlabs.io" data-credit><span>Sponsored by</span><img src="/sponsor/rocksolidlabs-logo-reversed.png" alt="RockSolid Labs"`) {
			t.Fatalf("%s has no sponsor badge", p)
		}
		if !strings.Contains(body, "· Apache-2.0 ·") {
			t.Fatalf("%s does not name the license", p)
		}
	}
}

// The third-party notices are served to anyone, by a workbench server and
// by a switchyard, whose footer links them.
func TestThirdPartyNoticesAreServedEverywhere(t *testing.T) {
	for _, sy := range []bool{false, true} {
		e := switchyardEnv(t, sy)
		resp, err := http.Get(e.http.URL + "/third-party-notices.txt")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" || string(b) != conductor.ThirdPartyNotices {
			t.Fatalf("switchyard %v: %d %q %.120q", sy, resp.StatusCode, resp.Header.Get("Content-Type"), b)
		}
		if !strings.Contains(string(b), "github.com/pion/webrtc/v4") {
			t.Fatalf("switchyard %v: the notices lack pion: %.200s", sy, b)
		}
	}
	_, body := getPage(t, switchyardEnv(t, true), "/")
	if !strings.Contains(body, `<a href="/third-party-notices.txt" data-notices>Third-party notices</a>`) {
		t.Fatal("the landing page's footer does not link the notices")
	}
}
