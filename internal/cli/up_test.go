package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A token no test output may contain.
const secretToken = "sekret-admin-token"

// request is what a stub server saw of one request.
type request struct {
	method, path, query, auth string
	body, contentType         string
}

// stubServer answers every request with status and body, and records the
// requests it gets.
func stubServer(t *testing.T, status int, body string) (*httptest.Server, *[]request) {
	t.Helper()
	var seen []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, request{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("Authorization"), string(b), r.Header.Get("Content-Type")})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// command is the signature of runUp and runCrews.
type command func(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error)

// runWith runs fn with args and returns what it printed.
func runWith(t *testing.T, fn command, args ...string) (code int, stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	code, err = fn(t.Context(), args, &out, &errOut)
	return code, out.String(), errOut.String(), err
}

func up(t *testing.T, args ...string) (int, string, string, error) {
	t.Helper()
	return runWith(t, runUp, args...)
}

func crews(t *testing.T, args ...string) (int, string, string, error) {
	t.Helper()
	return runWith(t, runCrews, args...)
}

// noSecret fails the test when secretToken is in anything a command
// printed.
func noSecret(t *testing.T, parts ...any) {
	t.Helper()
	noToken(t, secretToken, parts...)
}

// noToken fails the test when token is in anything a command printed.
func noToken(t *testing.T, token string, parts ...any) {
	t.Helper()
	if s, ok := tokenIn(token, parts...); ok {
		t.Fatalf("the token is in %q", s)
	}
}

// tokenIn is the first of parts (strings and errors) that holds token.
func tokenIn(token string, parts ...any) (string, bool) {
	for _, p := range parts {
		s := ""
		switch v := p.(type) {
		case string:
			s = v
		case error:
			if v != nil {
				s = v.Error()
			}
		}
		if strings.Contains(s, token) {
			return s, true
		}
	}
	return "", false
}

// The check finds the token it is given wherever it is planted, and only
// that one: a token another test uses is not secretToken.
func TestTokenInFindsThePlantedToken(t *testing.T) {
	for _, token := range []string{secretToken, exportToken} {
		for _, parts := range [][]any{
			{"wrote x\n", "conductor export: Bearer " + token + "\n", nil},
			{"", "", errors.New("export: GET /api/export?token=" + token)},
			{token},
		} {
			if _, ok := tokenIn(token, parts...); !ok {
				t.Fatalf("%s not found in %q", token, parts)
			}
		}
		if _, ok := tokenIn(token, "clean", "", error(nil)); ok {
			t.Fatalf("%s found in clean output", token)
		}
	}
	if _, ok := tokenIn(secretToken, "Bearer "+exportToken); ok {
		t.Fatal("secretToken matched exportToken")
	}
	if _, ok := tokenIn(exportToken, "Bearer "+exportToken); !ok {
		t.Fatal("exportToken missed")
	}
}

const launchReply = `{"run":{"id":"r-1","crewId":"crew-1","name":"Ship it","members":[{"name":"lead","sessionId":"s-1"}],"extra":{"unknown":true}}}`

// conductor up posts to the crew's launch route with the token as a bearer
// and prints the run and its URL, the server as given without a trailing slash.
func TestUpLaunchesAndPrintsRunURL(t *testing.T) {
	clearConductorEnv(t)
	srv, seen := stubServer(t, http.StatusCreated, launchReply)
	code, stdout, stderr, err := up(t, "crew-1", "--server", srv.URL+"/", "--token", secretToken)
	if code != 0 || err != nil {
		t.Fatalf("exit %d %v\nstdout:\n%s\nstderr:\n%s", code, err, stdout, stderr)
	}
	want := "run r-1\n" + srv.URL + "/runs/r-1\n"
	if stdout != want {
		t.Fatalf("stdout:\n%q\nwant:\n%q", stdout, want)
	}
	if len(*seen) != 1 {
		t.Fatalf("requests: %+v", *seen)
	}
	got := (*seen)[0]
	if got.method != http.MethodPost || got.path != "/api/crews/crew-1/launch" || got.auth != "Bearer "+secretToken || got.query != "" {
		t.Fatalf("request: %+v", got)
	}
	noSecret(t, stdout, stderr)
}

// A crew with a view link: the launch reply carries it, and conductor up
// prints its URL on a third line, as the server gave it. A URL that is not
// http(s), or carries control characters, is not printed.
func TestUpPrintsTheViewLink(t *testing.T) {
	clearConductorEnv(t)
	reply := `{"run":{"id":"r-1"},"viewLink":{"link":{"id":"l-1","runId":"r-1","role":"view"},"token":"v-tok","url":"https://conductor.example/join/v-tok"}}`
	srv, _ := stubServer(t, http.StatusCreated, reply)
	code, stdout, stderr, err := up(t, "crew-1", "--server", srv.URL, "--token", secretToken)
	want := "run r-1\n" + srv.URL + "/runs/r-1\nview https://conductor.example/join/v-tok\n"
	if code != 0 || err != nil || stdout != want || stderr != "" {
		t.Fatalf("exit %d %v\nstdout:\n%q\nwant:\n%q\nstderr:\n%s", code, err, stdout, want, stderr)
	}
	noSecret(t, stdout, stderr)

	for _, bad := range []string{`javascript:alert(1)`, `https://x.example/join/\u001b[31mred`, `/join/v-tok`} {
		srv, _ := stubServer(t, http.StatusCreated, `{"run":{"id":"r-1"},"viewLink":{"token":"v-tok","url":"`+bad+`"}}`)
		code, stdout, stderr, err := up(t, "crew-1", "--server", srv.URL, "--token", secretToken)
		if code != 0 || err != nil || stdout != "run r-1\n"+srv.URL+"/runs/r-1\n" || !strings.Contains(stderr, "view link") {
			t.Fatalf("%s: exit %d %v\nstdout:\n%q\nstderr:\n%s", bad, code, err, stdout, stderr)
		}
	}
}

// The server and the token come from the environment when no flag gives them,
// and a flag after the crew reads as well as one before it.
func TestUpEnvironmentAndFlagOrder(t *testing.T) {
	clearConductorEnv(t)
	srv, seen := stubServer(t, http.StatusCreated, launchReply)
	t.Setenv("CONDUCTOR_SERVER", srv.URL)
	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", secretToken)
	code, stdout, stderr, err := up(t, "crew-1")
	if code != 0 || err != nil || stdout != "run r-1\n"+srv.URL+"/runs/r-1\n" {
		t.Fatalf("exit %d %v\nstdout:\n%s\nstderr:\n%s", code, err, stdout, stderr)
	}
	if len(*seen) != 1 || (*seen)[0].auth != "Bearer "+secretToken {
		t.Fatalf("requests: %+v", *seen)
	}
	// A flag wins over the environment.
	code, _, _, err = up(t, "crew-2", "--token", "other")
	if code != 0 || err != nil || len(*seen) != 2 || (*seen)[1].auth != "Bearer other" || (*seen)[1].path != "/api/crews/crew-2/launch" {
		t.Fatalf("exit %d %v; requests: %+v", code, err, *seen)
	}
}

// A crew ID is one path segment, whatever it holds.
func TestUpEscapesTheCrewID(t *testing.T) {
	clearConductorEnv(t)
	srv, seen := stubServer(t, http.StatusCreated, launchReply)
	if code, _, _, err := up(t, "a/b?c#d", "--server", srv.URL, "--token", secretToken); code != 0 || err != nil {
		t.Fatalf("exit %d %v", code, err)
	}
	if got := (*seen)[0]; got.path != "/api/crews/a%2Fb%3Fc%23d/launch" || got.query != "" {
		t.Fatalf("request: %+v", got)
	}
}

// Without a token the command exits 2 before it sends anything.
func TestUpMissingToken(t *testing.T) {
	clearConductorEnv(t)
	srv, seen := stubServer(t, http.StatusCreated, launchReply)
	code, stdout, _, err := up(t, "crew-1", "--server", srv.URL)
	if code != 2 || err == nil || !strings.Contains(err.Error(), "CONDUCTOR_WORKBENCH_TOKEN") || stdout != "" {
		t.Fatalf("exit %d %v\nstdout:\n%s", code, err, stdout)
	}
	if len(*seen) != 0 {
		t.Fatalf("a request was sent: %+v", *seen)
	}
}

// A mistake on the command line exits 2 and never repeats the token.
func TestUpUsageErrors(t *testing.T) {
	clearConductorEnv(t)
	srv, seen := stubServer(t, http.StatusCreated, launchReply)
	for name, args := range map[string][]string{
		"no crew":        {"--server", srv.URL, "--token", secretToken},
		"two crews":      {"a", "b", "--server", srv.URL, "--token", secretToken},
		"empty crew":     {"", "--server", srv.URL, "--token", secretToken},
		"unknown flag":   {"a", "--nope", "--token", secretToken},
		"no scheme":      {"a", "--server", "localhost:8080", "--token", secretToken},
		"not http":       {"a", "--server", "ftp://" + secretToken + "@host", "--token", secretToken},
		"no host":        {"a", "--server", "http://", "--token", secretToken},
		"server query":   {"a", "--server", srv.URL + "/?x=1", "--token", secretToken},
		"server section": {"a", "--server", srv.URL + "/#x", "--token", secretToken},
	} {
		code, stdout, stderr, err := up(t, args...)
		if code != 2 || err == nil {
			t.Errorf("%s: exit %d %v", name, code, err)
		}
		noSecret(t, stdout, stderr, err)
	}
	if len(*seen) != 0 {
		t.Fatalf("a request was sent: %+v", *seen)
	}
}

// -h prints the usage and exits 0.
func TestUpHelp(t *testing.T) {
	clearConductorEnv(t)
	code, _, stderr, err := up(t, "-h")
	if code != 0 || err != nil || !strings.Contains(stderr, "conductor up") || !strings.Contains(stderr, "CONDUCTOR_WORKBENCH_TOKEN") {
		t.Fatalf("exit %d %v\n%s", code, err, stderr)
	}
	// The token's own default is never printed.
	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", secretToken)
	_, _, stderr, _ = up(t, "-h")
	noSecret(t, stderr)
}

// The API's error is the command's: code and message, exit 1, nothing on
// stdout.
func TestUpAPIError(t *testing.T) {
	clearConductorEnv(t)
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"envelope":      {http.StatusNotFound, `{"error":{"code":"not_found","message":"no such crew"}}`, "not_found: no such crew"},
		"unauthorized":  {http.StatusUnauthorized, `{"error":{"code":"unauthorized","message":"workbench token required"}}`, "unauthorized: workbench token required"},
		"not json":      {http.StatusBadGateway, `<html>bad gateway</html>`, "502"},
		"empty":         {http.StatusInternalServerError, ``, "500"},
		"redirect":      {http.StatusFound, ``, "302"},
		"success shape": {http.StatusCreated, `{"run":{"id":""}}`, "no run"},
		"garbled":       {http.StatusCreated, `not json`, "unreadable"},
	} {
		srv, _ := stubServer(t, tc.status, tc.body)
		code, stdout, stderr, err := up(t, "crew-1", "--server", srv.URL, "--token", secretToken)
		if code != 1 || err == nil || !strings.Contains(err.Error(), tc.want) || stdout != "" {
			t.Errorf("%s: exit %d %v\nstdout:\n%s\nstderr:\n%s", name, code, err, stdout, stderr)
			continue
		}
		noSecret(t, stdout, stderr, err)
	}
}

// A connection error names the server's host, not the path, the query or the
// token.
func TestUpConnectionError(t *testing.T) {
	clearConductorEnv(t)
	srv, _ := stubServer(t, http.StatusCreated, launchReply)
	host := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	code, stdout, stderr, err := up(t, "crew-1", "--server", "http://user:pw@"+host, "--token", secretToken)
	if code != 1 || err == nil || !strings.Contains(err.Error(), host) || stdout != "" {
		t.Fatalf("exit %d %v\nstdout:\n%s\nstderr:\n%s", code, err, stdout, stderr)
	}
	for _, leak := range []string{"/api/crews", "user", "pw", "crew-1"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("%q is in %q", leak, err)
		}
	}
	noSecret(t, stdout, stderr, err)
}

// A server that does not answer in time ends the command; up waits for the
// launch reply.
func TestUpTimeout(t *testing.T) {
	clearConductorEnv(t)
	old := requestTimeout
	requestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { requestTimeout = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(srv.Close)
	code, stdout, _, err := up(t, "crew-1", "--server", srv.URL, "--token", secretToken)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "no reply") || stdout != "" {
		t.Fatalf("exit %d %v\nstdout:\n%s", code, err, stdout)
	}
}

// A reply of more than 1 MiB is refused, not buffered.
func TestUpOversizedReply(t *testing.T) {
	clearConductorEnv(t)
	srv, _ := stubServer(t, http.StatusCreated, `{"run":{"id":"r-1"},"pad":"`+strings.Repeat("x", maxReply)+`"}`)
	code, stdout, _, err := up(t, "crew-1", "--server", srv.URL, "--token", secretToken)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "larger than") || stdout != "" {
		t.Fatalf("exit %d %v\nstdout:\n%s", code, err, stdout)
	}
}

// The token is sent to the server that was named and to no other: a redirect
// is an error, not followed.
func TestUpDoesNotFollowRedirects(t *testing.T) {
	clearConductorEnv(t)
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/crews/crew-1/launch", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	code, _, _, err := up(t, "crew-1", "--server", srv.URL, "--token", secretToken)
	if code != 1 || err == nil || elsewhere.Load() != 0 {
		t.Fatalf("exit %d %v; the redirect target got %d requests", code, err, elsewhere.Load())
	}
}

// --open hands the run's URL to the platform's opener; without an opener the
// URL is still printed and the launch still succeeds.
func TestUpOpen(t *testing.T) {
	clearConductorEnv(t)
	srv, _ := stubServer(t, http.StatusCreated, launchReply)
	var opened []string
	old := openBrowser
	t.Cleanup(func() { openBrowser = old })

	openBrowser = func(url string) error { opened = append(opened, url); return nil }
	want := "run r-1\n" + srv.URL + "/runs/r-1\n"
	code, stdout, stderr, err := up(t, "crew-1", "--server", srv.URL, "--token", secretToken, "--open")
	if code != 0 || err != nil || stdout != want || stderr != "" || len(opened) != 1 || opened[0] != srv.URL+"/runs/r-1" {
		t.Fatalf("exit %d %v\nstdout:\n%s\nstderr:\n%s\nopened: %v", code, err, stdout, stderr, opened)
	}

	// Not asked for: not opened.
	opened = nil
	if code, _, _, err := up(t, "crew-1", "--server", srv.URL, "--token", secretToken); code != 0 || err != nil || len(opened) != 0 {
		t.Fatalf("exit %d %v; opened: %v", code, err, opened)
	}

	// Neither xdg-open nor open: the URL is there to click.
	openBrowser = func(string) error { return errNoOpener }
	code, stdout, stderr, err = up(t, "crew-1", "--server", srv.URL, "--token", secretToken, "--open")
	if code != 0 || err != nil || stdout != want || !strings.Contains(stderr, "xdg-open") {
		t.Fatalf("no opener: exit %d %v\nstdout:\n%s\nstderr:\n%s", code, err, stdout, stderr)
	}

	// An opener that fails is a note, not a failed launch.
	openBrowser = func(string) error { return errors.New("boom") }
	code, stdout, stderr, err = up(t, "crew-1", "--server", srv.URL, "--token", secretToken, "--open")
	if code != 0 || err != nil || stdout != want || !strings.Contains(stderr, "boom") {
		t.Fatalf("failing opener: exit %d %v\nstdout:\n%s\nstderr:\n%s", code, err, stdout, stderr)
	}
}

// conductor crews prints one line per crew, in the server's order, and
// tolerates fields it does not know.
func TestCrewsList(t *testing.T) {
	clearConductorEnv(t)
	srv, seen := stubServer(t, http.StatusOK, `{"crews":[
		{"id":"c-1","name":"Ship it","goal":"g","future":{"a":1},"members":[{"name":"lead","agentId":"claude","prompt":"p","start":{"kind":"immediately"}},{"name":"review","agentId":"codex"}]},
		{"id":"c-2","name":"Solo","members":[{"name":"one","agentId":"claude"}]}
	],"more":true}`)
	code, stdout, stderr, err := crews(t, "--server", srv.URL, "--token", secretToken)
	if code != 0 || err != nil {
		t.Fatalf("exit %d %v\nstdout:\n%s\nstderr:\n%s", code, err, stdout, stderr)
	}
	want := "c-1\tShip it\t2 members: lead, review\nc-2\tSolo\t1 members: one\n"
	if stdout != want {
		t.Fatalf("stdout:\n%q\nwant:\n%q", stdout, want)
	}
	if len(*seen) != 1 || (*seen)[0] != (request{http.MethodGet, "/api/crews", "offset=0&limit=100", "Bearer " + secretToken, "", ""}) {
		t.Fatalf("requests: %+v", *seen)
	}
	noSecret(t, stdout, stderr)
}

// conductor crews asks for page after page until it has the total, and takes
// a page of more than 1 MiB: crews with long working directories.
func TestCrewsPagesThroughTheList(t *testing.T) {
	clearConductorEnv(t)
	var queries []string
	cwd := strings.Repeat("d", 12000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		var body strings.Builder
		body.WriteString(`{"total":101,"crews":[`)
		n := 100
		if r.URL.Query().Get("offset") == "100" {
			n = 1
		}
		for i := range n {
			if i > 0 {
				body.WriteString(",")
			}
			fmt.Fprintf(&body, `{"id":"c-%s-%d","name":"C","cwd":"%s","members":[]}`, r.URL.Query().Get("offset"), i, cwd)
		}
		body.WriteString("]}")
		w.Write([]byte(body.String()))
	}))
	t.Cleanup(srv.Close)
	code, stdout, stderr, err := crews(t, "--server", srv.URL, "--token", secretToken)
	if code != 0 || err != nil || strings.Count(stdout, "\n") != 101 || !strings.Contains(stdout, "c-100-0\t") {
		t.Fatalf("exit %d %v\nstderr:\n%s", code, err, stderr)
	}
	if !slices.Equal(queries, []string{"offset=0&limit=100", "offset=100&limit=100"}) {
		t.Fatalf("queries %v", queries)
	}
}

// A page of crews is still bounded: more than 4 MiB is refused.
func TestCrewsOversizedReply(t *testing.T) {
	clearConductorEnv(t)
	srv, _ := stubServer(t, http.StatusOK, `{"total":1,"crews":[],"pad":"`+strings.Repeat("x", maxCrewsReply)+`"}`)
	code, stdout, _, err := crews(t, "--server", srv.URL, "--token", secretToken)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "larger than 4 MiB") || stdout != "" {
		t.Fatalf("exit %d %v\nstdout:\n%s", code, err, stdout)
	}
}

func TestCrewsEmpty(t *testing.T) {
	clearConductorEnv(t)
	srv, _ := stubServer(t, http.StatusOK, `{"crews":[]}`)
	code, stdout, stderr, err := crews(t, "--server", srv.URL, "--token", secretToken)
	if code != 0 || err != nil || stdout != "no crews\n" {
		t.Fatalf("exit %d %v\nstdout:\n%q\nstderr:\n%s", code, err, stdout, stderr)
	}
}

func TestCrewsErrors(t *testing.T) {
	clearConductorEnv(t)
	if code, _, _, err := crews(t, "--server", "http://localhost:1"); code != 2 || err == nil || !strings.Contains(err.Error(), "CONDUCTOR_WORKBENCH_TOKEN") {
		t.Fatalf("no token: exit %d %v", code, err)
	}
	if code, _, _, err := crews(t, "extra", "--token", secretToken); code != 2 || err == nil {
		t.Fatalf("an argument: exit %d %v", code, err)
	}
	srv, _ := stubServer(t, http.StatusUnauthorized, `{"error":{"code":"unauthorized","message":"workbench token required"}}`)
	code, stdout, stderr, err := crews(t, "--server", srv.URL, "--token", secretToken)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "unauthorized: workbench token required") || stdout != "" {
		t.Fatalf("exit %d %v\nstdout:\n%s", code, err, stdout)
	}
	noSecret(t, stdout, stderr, err)
	srv, _ = stubServer(t, http.StatusOK, `{"crews":`)
	if code, _, _, err := crews(t, "--server", srv.URL, "--token", secretToken); code != 1 || err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("garbled: exit %d %v", code, err)
	}
}

// The two commands are reachable from Run and named in its help.
func TestRunDispatchesUpAndCrews(t *testing.T) {
	clearConductorEnv(t)
	var out, errOut bytes.Buffer
	if code, err := Run(t.Context(), []string{"help"}, nil, &out, &errOut); code != 0 || err != nil {
		t.Fatalf("help: exit %d %v", code, err)
	}
	for _, want := range []string{"conductor up <crew-id>", "conductor crews", "--ids", "conductor completion zsh|bash", "conductor completion install"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help does not name %q:\n%s", want, out.String())
		}
	}
	srv, _ := stubServer(t, http.StatusOK, `{"crews":[]}`)
	out.Reset()
	if code, err := Run(t.Context(), []string{"crews", "--server", srv.URL, "--token", secretToken}, nil, &out, &errOut); code != 0 || err != nil || out.String() != "no crews\n" {
		t.Fatalf("crews: exit %d %v\n%s", code, err, out.String())
	}
	if code, err := Run(t.Context(), []string{"up"}, nil, &out, &errOut); code != 2 || err == nil {
		t.Fatalf("up: exit %d %v", code, err)
	}
	out.Reset()
	if code, err := Run(t.Context(), []string{"completion", "zsh"}, nil, &out, &errOut); code != 0 || err != nil || !strings.HasPrefix(out.String(), "#compdef conductor\n") {
		t.Fatalf("completion: exit %d %v\n%s", code, err, out.String())
	}
}

func TestClientDropsUserinfoFromTheServerURL(t *testing.T) {
	server, token, insecure := "https://user:pw@example.test/", "tok", false
	c, err := apiFlags{server: &server, token: &token, insecure: &insecure}.client(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.base != "https://example.test" || strings.Contains(c.base, "pw") {
		t.Fatalf("base %q keeps the userinfo", c.base)
	}
}

// The admin client sends the workbench token with every request: to a plain
// server on another machine (--server or CONDUCTOR_SERVER) it is refused
// before any request, exit 2, with words that name the host alone and
// --insecure; --ids stays silent. One on this machine needs nothing.
func TestAdminClientRefusesPlainServerWithoutInsecure(t *testing.T) {
	clearConductorEnv(t)
	const want = "the server yard.example:8080 is plain http and not localhost or a loopback address: what this client sends and reads, the workbench token on every request among it, would travel in the clear and unverified; use https, or pass --insecure to go ahead"
	for _, fn := range []struct {
		name string
		run  func(*testing.T, ...string) (int, string, string, error)
		args []string
	}{{"up", up, []string{"crew-1"}}, {"crews", crews, nil}} {
		for _, server := range []string{"http://yard.example:8080", "http://user:pw@yard.example:8080/"} {
			code, stdout, stderr, err := fn.run(t, append(fn.args, "--server", server, "--token", secretToken)...)
			if code != 2 || err == nil || err.Error() != want || stdout != "" || stderr != "" {
				t.Errorf("%s %s: exit %d, %v\nstdout %q\nstderr %q", fn.name, server, code, err, stdout, stderr)
			}
			noSecret(t, stdout, stderr, err)
		}
		t.Setenv("CONDUCTOR_SERVER", "http://yard.example:8080")
		if code, _, _, err := fn.run(t, append(fn.args, "--token", secretToken)...); code != 2 || err == nil || err.Error() != want {
			t.Errorf("%s with CONDUCTOR_SERVER: exit %d, %v", fn.name, code, err)
		}
		t.Setenv("CONDUCTOR_SERVER", "")
	}
	if code, stdout, stderr, err := crews(t, "--server", "http://yard.example:8080", "--token", secretToken, "--ids"); code != 0 || err != nil || stdout != "" || stderr != "" {
		t.Errorf("--ids: exit %d, %v, %q, %q", code, err, stdout, stderr)
	}
	// On this machine: asked, and nothing said.
	srv, seen := stubServer(t, http.StatusOK, `{"crews":[],"total":0}`)
	local := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	for _, server := range []string{srv.URL, local} {
		if code, stdout, stderr, err := crews(t, "--server", server, "--token", secretToken); code != 0 || err != nil || stdout != "no crews\n" || stderr != "" {
			t.Errorf("%s: exit %d, %v, %q, %q", server, code, err, stdout, stderr)
		}
	}
	if len(*seen) != 2 {
		t.Fatalf("requests on this machine: %d", len(*seen))
	}
}

// With --insecure the admin client goes ahead to a plain server on another
// machine and says on stderr what travels in the clear; to one that is not
// plain it says nothing.
func TestAdminClientInsecurePrintsTheWarning(t *testing.T) {
	clearConductorEnv(t)
	server, token, insecure := "http://user:pw@yard.example:8080/", secretToken, true
	var stderr bytes.Buffer
	c, err := apiFlags{server: &server, token: &token, insecure: &insecure}.client(&stderr)
	if err != nil || c.base != "http://yard.example:8080" {
		t.Fatalf("client: %v %+v", err, c)
	}
	const want = "conductor: warning: --insecure: the server yard.example:8080 is plain http and not localhost or a loopback address: what this client sends and reads, the workbench token on every request among it, travels in the clear and unverified\n"
	if stderr.String() != want {
		t.Fatalf("stderr %q, want %q", stderr.String(), want)
	}
	noSecret(t, stderr.String())
	if strings.Contains(stderr.String(), "pw") {
		t.Fatalf("the warning repeats the password: %q", stderr.String())
	}
	stderr.Reset()
	https := "https://yard.example"
	if _, err := (apiFlags{server: &https, token: &token, insecure: &insecure}).client(&stderr); err != nil || stderr.Len() != 0 {
		t.Fatalf("https with --insecure: %v %q", err, stderr.String())
	}
	// Both commands take the flag; on this machine it changes nothing.
	srv, seen := stubServer(t, http.StatusOK, `{"crews":[],"total":0}`)
	if code, stdout, errOut, err := crews(t, "--server", srv.URL, "--token", secretToken, "--insecure"); code != 0 || err != nil || stdout != "no crews\n" || errOut != "" || len(*seen) != 1 {
		t.Fatalf("crews --insecure on this machine: exit %d, %v, %q, %q", code, err, stdout, errOut)
	}
	launch, _ := stubServer(t, http.StatusCreated, launchReply)
	if code, _, errOut, err := up(t, "crew-1", "--server", launch.URL, "--token", secretToken, "--insecure"); code != 0 || err != nil || errOut != "" {
		t.Fatalf("up --insecure on this machine: exit %d, %v, %q", code, err, errOut)
	}
}

// conductor crews --ids prints ids only, one per line, paging through the
// list as conductor crews does, and leaves out anything not shaped like a
// crew id (crew.ValidID): the output is fed to a shell's completion.
func TestCrewsIDsPrintsOnlyWellFormedIDs(t *testing.T) {
	clearConductorEnv(t)
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") == "0" {
			fmt.Fprint(w, `{"crews":[{"id":"alpha"},{"id":"bad id"},{"id":"evil\u001b[31m"},{"id":"$(rm)"},{"id":"-flag"}],"total":6}`)
			return
		}
		fmt.Fprint(w, `{"crews":[{"id":"beta-2"}],"total":6}`)
	}))
	t.Cleanup(srv.Close)
	code, stdout, stderr, err := crews(t, "--server", srv.URL, "--token", secretToken, "--ids")
	if code != 0 || err != nil || stdout != "alpha\nbeta-2\n" || stderr != "" {
		t.Fatalf("exit %d %v\nstdout: %q\nstderr: %q", code, err, stdout, stderr)
	}
	if !slices.Equal(pages, []string{"offset=0&limit=100", "offset=5&limit=100"}) {
		t.Fatalf("pages: %v", pages)
	}
}

// Without a token, without a server to reach, or refused, --ids prints
// nothing and exits 0: a completion must never put an error on the command
// line.
func TestCrewsIDsIsSilentWhenItCannotAsk(t *testing.T) {
	clearConductorEnv(t)
	code, stdout, stderr, err := crews(t, "--ids")
	if code != 0 || err != nil || stdout != "" || stderr != "" {
		t.Fatalf("no token: exit %d %v %q %q", code, err, stdout, stderr)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	code, stdout, stderr, err = crews(t, "--server", closed.URL, "--token", secretToken, "--ids")
	if code != 0 || err != nil || stdout != "" || stderr != "" {
		t.Fatalf("unreachable: exit %d %v %q %q", code, err, stdout, stderr)
	}
	srv, _ := stubServer(t, http.StatusUnauthorized, `{"error":{"code":"unauthorized","message":"bad token"}}`)
	code, stdout, stderr, err = crews(t, "--server", srv.URL, "--token", secretToken, "--ids")
	if code != 0 || err != nil || stdout != "" || stderr != "" {
		t.Fatalf("refused: exit %d %v %q %q", code, err, stdout, stderr)
	}
	noSecret(t, stdout, stderr, err)
}

// A server that does not answer is given idsTimeout in all (2 s), not a
// request's timeout: completion does not hang the shell. The test lowers it.
func TestCrewsIDsGivesUpQuickly(t *testing.T) {
	clearConductorEnv(t)
	if idsTimeout != 2*time.Second {
		t.Fatalf("idsTimeout is %v", idsTimeout)
	}
	was := idsTimeout
	idsTimeout = 200 * time.Millisecond
	t.Cleanup(func() { idsTimeout = was })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	start := time.Now()
	code, stdout, _, err := crews(t, "--server", srv.URL, "--token", secretToken, "--ids")
	if took := time.Since(start); code != 0 || err != nil || stdout != "" || took < idsTimeout || took > idsTimeout+time.Second {
		t.Fatalf("exit %d %v %q after %s", code, err, stdout, took)
	}
}

// A run launched with yolo says so on a line of its own.
func TestUpSaysWhenTheRunIsYolo(t *testing.T) {
	clearConductorEnv(t)
	srv, _ := stubServer(t, http.StatusCreated, `{"run":{"id":"r-1","yolo":true}}`)
	code, stdout, stderr, err := up(t, "crew-1", "--server", srv.URL, "--token", secretToken)
	want := "run r-1\n" + srv.URL + "/runs/r-1\nyolo: its members skip their permission prompts\n"
	if code != 0 || err != nil || stdout != want {
		t.Fatalf("exit %d %v\nstdout:\n%q\nwant:\n%q\nstderr:\n%s", code, err, stdout, want, stderr)
	}
}

// --name names the run: the launch carries it as the label; without it the
// launch has no body.
func TestUpSendsTheName(t *testing.T) {
	srv, seen := stubServer(t, http.StatusCreated, `{"run":{"id":"pair-0123abcd"}}`)
	var out, errOut strings.Builder
	if code, err := runUp(context.Background(), []string{"pair", "--server", srv.URL, "--token", "tok", "--name", " fix login "}, &out, &errOut); code != 0 || err != nil {
		t.Fatalf("up: %d %v %s", code, err, errOut.String())
	}
	if r := (*seen)[0]; r.body != `{"label":"fix login"}` || r.contentType != "application/json" {
		t.Fatalf("named launch sent %+v", r)
	}
	if code, err := runUp(context.Background(), []string{"pair", "--server", srv.URL, "--token", "tok"}, &out, &errOut); code != 0 || err != nil {
		t.Fatalf("up: %d %v", code, err)
	}
	if r := (*seen)[1]; r.body != "" || r.contentType != "" {
		t.Fatalf("unnamed launch sent %+v", r)
	}
}

// conductor up --resume posts to the run's resume route, with no body, and
// prints the new run and its URL as a launch does; a crew named beside it,
// or a --name (a resumed run keeps its name), is a usage error; the server's
// refusal (the run resumed already, naming the continuation) is the error.
func TestUpResumesARun(t *testing.T) {
	clearConductorEnv(t)
	srv, seen := stubServer(t, http.StatusCreated, `{"run":{"id":"r-2","crewId":"crew-1","name":"Ship it","resumedFrom":"r-1","yolo":true}}`)
	code, stdout, stderr, err := up(t, "--resume", "r-1", "--server", srv.URL, "--token", secretToken)
	if code != 0 || err != nil {
		t.Fatalf("exit %d %v\nstdout:\n%s\nstderr:\n%s", code, err, stdout, stderr)
	}
	if want := "run r-2\n" + srv.URL + "/runs/r-2\nyolo: its members skip their permission prompts\n"; stdout != want {
		t.Fatalf("stdout:\n%q\nwant:\n%q", stdout, want)
	}
	if len(*seen) != 1 || (*seen)[0].method != http.MethodPost || (*seen)[0].path != "/api/runs/r-1/resume" || (*seen)[0].body != "" {
		t.Fatalf("requests: %+v", *seen)
	}
	noSecret(t, stdout, stderr)
	for _, args := range [][]string{{"crew-1", "--resume", "r-1"}, {"--resume", "r-1", "--name", "again"}} {
		if code, _, _, err := up(t, append(args, "--server", srv.URL, "--token", secretToken)...); code != 2 || err == nil {
			t.Fatalf("%v: exit %d %v", args, code, err)
		}
	}
	refused, _ := stubServer(t, http.StatusConflict, `{"error":{"code":"already_resumed","message":"the run was resumed already, as r-2","run":"r-2"}}`)
	code, _, _, err = up(t, "--resume", "r-1", "--server", refused.URL, "--token", secretToken)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "resumed already") {
		t.Fatalf("refused: exit %d %v", code, err)
	}
}
