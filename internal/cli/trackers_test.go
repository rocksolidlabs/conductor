package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

// handlerServer serves h on a loopback address for t: a stand-in for a
// Conductor server whose answers a test writes.
func handlerServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	return hs.URL
}

// decodeBody decodes a request's JSON body into v.
func decodeBody(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// A token the fake GitHub takes, one it refuses, and a word that is
// given where a token must never be (an argument, a flag's value).
const (
	ghToken   = "ghp_TESTTOKENtakenBYtheFAKE0123456789"
	ghRefused = "ghp_TESTTOKENrefusedBYtheFAKE987654"
	ghArgv    = "ghp_TESTTOKENgivenASanARGUMENT00000"
)

// githubFake is a fake GitHub on a loopback host the test server reads
// in loopback mode: its account route takes ghToken alone. It counts the
// account's reads and keeps the Authorization headers it was sent.
type githubFake struct {
	*githubtest.Server
	host string
	mu   sync.Mutex
	auth []string
}

func (g *githubFake) sent() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.auth)
}

// newTrackerAPI is the test server in loopback mode and the fake GitHub
// on its loopback host, with the workbench token in the environment as
// conductor trackers set reads it.
func newTrackerAPI(t *testing.T) (*dispatchAPI, *githubFake) {
	t.Helper()
	a := newDispatchAPIConfig(t, func(c *config.Config) { c.TrackerLoopback = true })
	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", dispatchToken)
	g := &githubFake{Server: githubtest.New(t)}
	g.host = g.PlainHost()
	g.Handle("GET", g.host, "/api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		g.mu.Lock()
		g.auth = append(g.auth, auth)
		g.mu.Unlock()
		if auth != "Bearer "+ghToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.Header().Set("X-OAuth-Scopes", "repo, read:org")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(githubtest.Fixture("user.json"))
	})
	return a, g
}

// trackersCmd runs conductor trackers with args against a's server; stdin
// is its input.
func (a *dispatchAPI) trackersCmd(t *testing.T, stdin string, args ...string) (int, string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	code, err := Run(t.Context(), append(append([]string{"trackers"}, args...), "--server", a.hs.URL), strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String(), err
}

// noneHolds fails t when any of words is in what a command printed or
// returned.
func noneHolds(t *testing.T, what string, words []string, out, errOut string, err error) {
	t.Helper()
	all := out + "\n" + errOut
	if err != nil {
		all += "\n" + err.Error()
	}
	for _, w := range words {
		if w != "" && strings.Contains(all, w) {
			t.Fatalf("%s: a token is in what it printed:\n%s", what, all)
		}
	}
}

// conductor trackers set reads the token from standard input alone: no
// flag of it takes a token (it has no --token, so its command line holds
// none of any kind), an argument or a --token is refused without being
// printed back, a pipe's line end is not the token's, and an empty,
// two-line or ill-formed input is refused before anything is sent. The
// token kept is listed by its account and fingerprint, never itself, and
// a token GitHub refuses is answered in words that do not hold it.
func TestTrackersSetReadsStdin(t *testing.T) {
	a, g := newTrackerAPI(t)
	secrets := []string{ghToken, ghRefused, ghArgv}

	var help bytes.Buffer
	if code, err := Run(t.Context(), []string{"trackers", "set", "-h"}, strings.NewReader(""), &bytes.Buffer{}, &help); code != 0 || err != nil {
		t.Fatalf("set -h: %d %v", code, err)
	}
	var flags []string
	for _, m := range regexp.MustCompile(`(?m)^  -([a-z][a-z0-9-]*)`).FindAllStringSubmatch(help.String(), -1) {
		flags = append(flags, m[1])
	}
	if slices.Sort(flags); !slices.Equal(flags, []string{"host", "insecure", "server"}) {
		t.Fatalf("trackers set takes the flags %q: none may take a token", flags)
	}

	for _, args := range [][]string{{"set", ghArgv}, {"set", "--host", g.host, ghArgv}, {"set", "--token", ghArgv}, {"set", "--token=" + ghArgv}, {ghArgv}, {"remove", ghArgv}} {
		code, out, errOut, err := a.trackersCmd(t, ghToken+"\n", args...)
		if code != 2 || err == nil {
			t.Fatalf("%q: exit %d %v", args, code, err)
		}
		noneHolds(t, strings.Join(args, " "), secrets, out, errOut, err)
	}
	for name, stdin := range map[string]string{
		"empty": "", "a line end alone": "\n", "two lines": ghToken + "\n" + ghToken + "\n", "a space": "ghp_two words\n", "a control byte": "ghp_\x1b[2J\n",
		"256 bytes": strings.Repeat("a", config.MaxTrackerToken+1) + "\n", "past what is read": strings.Repeat("a", maxTrackerInput+1),
	} {
		code, out, errOut, err := a.trackersCmd(t, stdin, "set", "--host", g.host)
		if code != 2 || err == nil {
			t.Fatalf("%s: exit %d %v", name, code, err)
		}
		noneHolds(t, name, append(secrets, strings.TrimSpace(stdin)), out, errOut, err)
	}
	if n := len(g.sent()); n != 0 {
		t.Fatalf("a refused input reached GitHub %d times", n)
	}

	code, out, errOut, err := a.trackersCmd(t, ghToken+"\n", "set", "--host", g.host)
	if code != 0 || err != nil || !strings.Contains(out, g.host+": kept") || !strings.Contains(out, "@") || !strings.Contains(out, "fingerprint ") {
		t.Fatalf("set: %d %v\n%s%s", code, err, out, errOut)
	}
	noneHolds(t, "set", secrets, out, errOut, err)
	if sent := g.sent(); len(sent) != 1 || sent[0] != "Bearer "+ghToken {
		t.Fatalf("GitHub was sent %q: the line end is the pipe's, not the token's", sent)
	}
	code, out, errOut, err = a.trackersCmd(t, ghToken+"\r\n", "set", "--host", g.host)
	if code != 0 || err != nil || !strings.Contains(out, "nothing changed") {
		t.Fatalf("set again from a CRLF pipe: %d %v\n%s%s", code, err, out, errOut)
	}
	code, out, errOut, err = a.trackersCmd(t, ghRefused+"\n", "set", "--host", g.host)
	if code != 1 || err == nil || !strings.HasPrefix(err.Error(), "token_refused: ") {
		t.Fatalf("a refused token: %d %v", code, err)
	}
	noneHolds(t, "a refused token", secrets, out, errOut, err)

	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", dispatchToken)
	code, out, errOut, err = a.trackersCmd(t, "", "--token", dispatchToken)
	if code != 0 || err != nil || !strings.HasPrefix(out, g.host+"  @") || !strings.Contains(out, "settings  taken by GitHub") {
		t.Fatalf("the list: %d %v\n%s%s", code, err, out, errOut)
	}
	noneHolds(t, "the list", secrets, out, errOut, err)

	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", "")
	if code, _, _, err := a.trackersCmd(t, ghToken+"\n", "set", "--host", g.host); code != 2 || err == nil || !strings.Contains(err.Error(), "CONDUCTOR_WORKBENCH_TOKEN") {
		t.Fatalf("set with no workbench token in the environment: %d %v", code, err)
	}
	// A host with no token kept (a mistyped one, the github.com default)
	// removes nothing and says so, naming the hosts that are kept.
	for _, args := range [][]string{{"remove", "--host", "other.example"}, {"remove"}} {
		code, out, _, err = a.trackersCmd(t, "", append(args, "--token", dispatchToken)...)
		if code != 0 || err != nil || strings.Contains(out, ": removed") || !strings.Contains(out, ": no token was kept for it; nothing removed (a token is kept for "+g.host+")") {
			t.Fatalf("%q: %d %v\n%s", args, code, err, out)
		}
	}
	if _, out, _, _ = a.trackersCmd(t, "", "--token", dispatchToken); !strings.HasPrefix(out, g.host+"  @") {
		t.Fatalf("the list after removing nothing:\n%s", out)
	}
	code, out, _, err = a.trackersCmd(t, "", "remove", "--host", g.host, "--token", dispatchToken)
	if code != 0 || err != nil || !strings.Contains(out, g.host+": removed") {
		t.Fatalf("remove: %d %v\n%s", code, err, out)
	}
	if _, out, _, _ = a.trackersCmd(t, "", "--token", dispatchToken); !strings.HasPrefix(out, "no GitHub token is kept") {
		t.Fatalf("the list after the remove:\n%s", out)
	}
	code, out, _, err = a.trackersCmd(t, "", "remove", "--host", g.host, "--token", dispatchToken)
	if code != 0 || err != nil || strings.Contains(out, ": removed") || !strings.Contains(out, g.host+": no token was kept for it; nothing removed (no token is kept for any host)") {
		t.Fatalf("a second remove: %d %v\n%s", code, err, out)
	}
}

// A reply that held the token, as no server answer does, is not printed:
// the command says so in its own words.
func TestTrackersSetPrintsNoReplyThatHoldsTheToken(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", dispatchToken)
	for _, reply := range []struct {
		status int
		body   string
	}{
		{http.StatusUnprocessableEntity, `{"error":{"code":"token_refused","message":"refused ` + ghToken + `"}}`},
		{http.StatusOK, `{"tracker":{"host":"github.com","login":"` + ghToken + `","fingerprint":"abc","scopes":[],"source":"settings","state":"ok"}}`},
	} {
		hs := handlerServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(reply.status)
			_, _ = w.Write([]byte(reply.body))
		})
		var out, errb bytes.Buffer
		code, err := Run(t.Context(), []string{"trackers", "set", "--server", hs}, strings.NewReader(ghToken+"\n"), &out, &errb)
		if code != 1 || err == nil {
			t.Fatalf("%d: exit %d %v", reply.status, code, err)
		}
		noneHolds(t, "a reply holding the token", []string{ghToken}, out.String(), errb.String(), err)
	}
}
