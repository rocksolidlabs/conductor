package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// hostedCodexApproval is Codex 0.161's command approval as it draws it on
// an 80 x 24 screen (internal/api's router tests draw the same).
const hostedCodexApproval = "\x1b[?2026h\x1b[12;1H\x1b[J\x1b[13;3H\x1b[1mWould you like to run the following command?\x1b[22m" +
	"\x1b[15;3HEnvironment: local\x1b[17;3HReason: May I rerun the curl command with network access?" +
	"\x1b[19;3H\x1b[2m$\x1b[22m curl -sI https://example.com" +
	"\x1b[20;1H\x1b[36m› 1. Yes, proceed (\x1b[1my\x1b[22m)" +
	"\x1b[21;3H\x1b[39m2. Yes, and don't ask again for commands that start with" +
	"\x1b[22;6H`curl -sI https://example.com` (\x1b[1mp\x1b[22m)" +
	"\x1b[23;3H3. No, and tell Codex what to do differently (\x1b[1mesc\x1b[22m)" +
	"\x1b[24;3H\x1b[2mPress enter to confirm or esc to cancel\x1b[22m\x1b[?2026l"

// A hosted session of an agent the server's catalog knows watches what a
// server session of it watches (#293): given Codex's recipe at its
// registration, it raises Codex's command approval with the dialog's three
// choices, refuses a line typed into the session while the dialog shows (a
// handoff's, a broadcast's, a reply's: its Enter would approve the
// command), and takes a choice's keys alone.
func TestHostedSessionWatchesTheServersRecipe(t *testing.T) {
	srv, hs := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	typed := filepath.Join(t.TempDir(), "typed")
	registered := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := Run(ctx, Options{
			ServerURL: hs.URL,
			Token:     hostToken,
			Name:      "recipe test",
			HostName:  "laptop",
			// The agent's id alone: no adapter, so nothing is added to the command and no hook is written. The
			// program reads key by key, as Codex does, into a file.
			AgentID:   "codex",
			Argv:      []string{"/bin/sh", "-c", `stty -icanon -echo; printf '%s' "$1"; exec /bin/cat > "$2"`, "sh", hostedCodexApproval, typed},
			RelayOnly: true,
			Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			Registered: func(id, _ string) {
				registered <- id
			},
		})
		if err != nil {
			t.Errorf("run: %v", err)
		}
	}()
	var id string
	select {
	case id = <-registered:
	case <-time.After(10 * time.Second):
		t.Fatal("host did not register")
	}
	d, ok := srv.Registry().Get(id)
	if !ok {
		t.Fatal("hosted session not listed")
	}
	// The question as the server shows it: Codex's approval, its words, its three choices.
	deadline := time.Now().Add(15 * time.Second)
	for d.Info().Attention.State != session.AttentionNeedsInput || len(d.Info().Attention.Options) != 3 {
		if time.Now().After(deadline) {
			t.Fatalf("server attention %+v, want the approval with its three choices", d.Info().Attention)
		}
		time.Sleep(20 * time.Millisecond)
	}
	att := d.Info().Attention
	if att.Source != session.SourceQuestion || att.ID == "" {
		t.Fatalf("attention %+v, want a question asked on the screen", att)
	}
	labels := []string{att.Options[0].Label, att.Options[1].Label, att.Options[2].Label}
	if labels[0] != "Yes, proceed" || labels[1] != "Yes, and don't ask again" || labels[2] != "No, tell Codex what to do differently" {
		t.Fatalf("choices %q", labels)
	}

	v := dialViewer(t, hs.URL, id, adminToken)
	v.expectJSON(proto.TypeControl, proto.CtlWelcome)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
	v.send(relay)
	v.expectJSON(proto.TypeSignal, proto.SigRelayOK)
	v.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24}))
	v.expectJSON(proto.TypeControl, proto.CtlWelcome)
	v.expectJSON(proto.TypeControl, proto.CtlReady)

	// A line typed into the session while the dialog shows is held: refused, nothing written.
	v.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "the handoff", ReqID: "r1"}))
	if m := v.expectJSON(proto.TypeControl, proto.CtlError); m["code"] != proto.ErrCodeNotSent || m["requestId"] != "r1" || m["message"] != session.QuestionWords {
		t.Fatalf("the handoff's answer %v, want it held for the question", m)
	}
	// The first choice's keys alone answer it; the line held never reached the program before them.
	v.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "r2", Question: att.ID, Input: "y"}))
	if m := v.expectJSON(proto.TypeControl, proto.CtlAnswerOK); m["requestId"] != "r2" {
		t.Fatalf("answer %v", m)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(typed)
		if len(b) > 0 {
			if string(b) != "y" {
				t.Fatalf("the program read %q, want the choice's keys alone", b)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the choice's keys never reached the program")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("host did not stop")
	}
}

// Codex's form asking to allow a tool of an MCP server (#716), 80 columns
// wide as Codex 0.163.0 to 0.169.0 lay it out: its four choices, and its
// two (the server's approval mode set to prompt).
var (
	hostedToolForm = strings.Join([]string{
		"› Look up how sessions work",
		"",
		"  Field 1/1",
		`  Allow the conductor MCP server to run tool "guide"?`,
		"  › 1. Allow                   Run the tool and continue",
		"    2. Allow for this session  Run the tool and remember this choice for this",
		"                               session",
		"    3. Always allow            Run the tool and remember this choice for",
		"                               future tool calls",
		"    4. Cancel                  Cancel this tool call",
		"",
		"  enter to submit | esc to cancel",
	}, "\n")
	hostedToolFormOnce = strings.Join([]string{
		"› Look up how sessions work",
		"",
		"  Field 1/1",
		`  Allow the docs MCP server to run tool "search"?`,
		"",
		"  query: session attention",
		"",
		"  › 1. Allow   Run the tool and continue",
		"    2. Cancel  Cancel this tool call",
		"",
		"  enter to submit | esc to cancel",
	}, "\n")
	// hostedToolFormNot is the four-choice form without its description
	// column: not plainly the one form, which no session asks.
	hostedToolFormNot = strings.Join([]string{
		"› Look up how sessions work",
		"",
		"  Field 1/1",
		`  Allow the conductor MCP server to run tool "guide"?`,
		"  › 1. Allow",
		"    2. Allow for this session",
		"    3. Always allow",
		"    4. Cancel",
		"",
		"  enter to submit | esc to cancel",
	}, "\n")
)

// formProgram draws its first argument and writes what it reads, key by
// key as Codex reads, into the file its second names.
const formProgram = `stty -icanon -echo; printf '%s' "$1"; exec /bin/cat > "$2"`

// formScreens are the agents of formServer's catalog, each with the
// screen its program draws: the four-choice form, the two-choice form, and
// the four-choice form not plainly the form.
var formScreens = []struct{ agent, screen string }{
	{"former", hostedToolForm},
	{"former-once", hostedToolFormOnce},
	{"former-not", hostedToolFormNot},
}

// formAgent is an agent asked Codex's questions of the built-in catalog,
// its form asking to allow a tool among them, and after them one that the
// form's screen shows too, with the one choice "Seen": a session that does
// not ask the form asks that one at the same look. (Codex's startup
// question of the background server makes room for it: an agent has 8
// questions at most.) Its own command draws screen and reads what it is
// typed.
func formAgent(t *testing.T, id, screen string) catalog.Agent {
	t.Helper()
	codex, ok := catalog.Default().Get("codex")
	if !ok {
		t.Fatal("no codex in the default catalog")
	}
	var qs []catalog.Question
	for _, q := range codex.Questions {
		if !strings.Contains(q.Prompt, "background") {
			qs = append(qs, q)
		}
	}
	return catalog.Agent{ID: id, Name: id, Command: []string{"/bin/sh", "-c", formProgram, "sh", screen, "/dev/null"},
		Questions: append(qs, catalog.Question{Prompt: `enter to submit\W*esc to cancel\s*\z`, Words: `enter to submit`, Anytime: true, Answers: []catalog.Answer{{Label: "Seen", Input: "x"}}})}
}

// formServer is a server whose catalog holds formScreens' agents: the
// owner's own, taking hostToken, or a switchyard taking open hosts.
func formServer(t *testing.T, switchyard bool) (*api.Server, *httptest.Server) {
	t.Helper()
	var list []catalog.Agent
	for _, f := range formScreens {
		list = append(list, formAgent(t, f.agent, f.screen))
	}
	cat, err := catalog.Load(catalog.File{Agents: list})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.WorkbenchToken = adminToken
	root := t.TempDir()
	cfg.AllowedRoots = []string{root}
	cfg.DefaultCwd = root
	cfg.Dev = true
	if switchyard {
		cfg.Switchyard.Enabled = true
		cfg.Switchyard.OpenHosts = true
	} else {
		cfg.HostTokens = []string{hostToken}
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, cat, discardLog, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		hs.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	return srv, hs
}

// runFormHost runs a real host of opts against the server at serverURL
// until the test ends, and answers the id it registered under.
func runFormHost(t *testing.T, serverURL string, opts Options) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	registered := make(chan string, 1)
	done := make(chan struct{})
	opts.ServerURL, opts.Name, opts.HostName, opts.RelayOnly = serverURL, "form test", "laptop", true
	if opts.Log == nil {
		opts.Log = discardLog
	}
	opts.Registered = func(id, _ string) {
		select {
		case registered <- id:
		default:
		}
	}
	go func() {
		defer close(done)
		if _, err := Run(ctx, opts); err != nil {
			t.Errorf("run: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("host did not stop")
		}
	})
	select {
	case id := <-registered:
		return id
	case <-time.After(10 * time.Second):
		t.Fatal("host did not register")
	}
	return ""
}

// askedQuestion waits for d to ask a question on its screen and answers it.
func askedQuestion(t *testing.T, d session.Driver, what string) session.Attention {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if a := d.Info().Attention; a.State == session.AttentionNeedsInput && a.Source == session.SourceQuestion && a.ID != "" {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: attention %+v, want a question", what, d.Info().Attention)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// serverSession starts the server's own session of agentID, stopped as the
// test ends.
func serverSession(t *testing.T, srv *api.Server, base, agentID string) session.Driver {
	t.Helper()
	req, err := http.NewRequest("POST", base+"/api/sessions", strings.NewReader(`{"agentId":"`+agentID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID string `json:"id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session: %d %v", resp.StatusCode, err)
	}
	d, ok := srv.Registry().Get(created.ID)
	if !ok {
		t.Fatal("server session not listed")
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })
	return d
}

// registerProxy stands between a host and the server at base: it hands the
// server the host's register with its host object as edit leaves it, tells
// said what the host's register said of itself, and passes every other
// frame each way as it is (any other request through a reverse proxy).
func registerProxy(t *testing.T, base string, edit func(host map[string]any), said chan<- map[string]any) *httptest.Server {
	t.Helper()
	up, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(up)
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/host" {
			rp.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		down, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer down.CloseNow()
		down.SetReadLimit(-1)
		header := http.Header{}
		if auth := r.Header.Get("Authorization"); auth != "" {
			header.Set("Authorization", auth)
		}
		upc, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/ws/host", &websocket.DialOptions{HTTPHeader: header})
		if err != nil {
			return
		}
		defer upc.CloseNow()
		upc.SetReadLimit(-1)
		typ, data, err := down.Read(ctx)
		if err != nil {
			return
		}
		var reg map[string]any
		if json.Unmarshal(data, &reg) == nil {
			if host, ok := reg["host"].(map[string]any); ok {
				select {
				case said <- maps.Clone(host):
				default:
				}
				edit(host)
				data, _ = json.Marshal(reg)
			}
		}
		if upc.Write(ctx, typ, data) != nil {
			return
		}
		pipe := func(from, to *websocket.Conn) {
			defer cancel()
			for {
				typ, b, err := from.Read(ctx)
				if err != nil || to.Write(ctx, typ, b) != nil {
					return
				}
			}
		}
		go pipe(upc, down)
		pipe(down, upc)
	}))
	t.Cleanup(p.Close)
	return p
}

// A host that says it reads forms (proto.HostInfo.Forms, #970) is sent
// Codex's form asking to allow a tool of an MCP server in its recipe, and
// its session asks it exactly as the server's own session of the same
// agent asks it: through a real host, the four-choice form, the two-choice
// form and a screen that is not plainly the form each show the words,
// choices, keys and mode marks a server session shows (the last, no form:
// the question after it). On the four-choice form the mode answers are
// marked so (a page gives them no number key and never offers one first),
// a routed answer (answer_as) of keys that are no choice's is refused and
// writes nothing, and a page's choice reaches the program, its keys alone.
func TestAHostThatReadsFormsIsAskedCodexsToolApprovalForm(t *testing.T) {
	srv, hs := formServer(t, false)
	for _, f := range formScreens {
		t.Run(f.agent, func(t *testing.T) {
			want := askedQuestion(t, serverSession(t, srv, hs.URL, f.agent), "server")
			logs := &logRecorder{}
			typed := filepath.Join(t.TempDir(), "typed")
			id := runFormHost(t, hs.URL, Options{Token: hostToken, AgentID: f.agent, Argv: []string{"/bin/sh", "-c", formProgram, "sh", f.screen, typed}, Log: slog.New(logs)})
			d, ok := srv.Registry().Get(id)
			if !ok {
				t.Fatal("hosted session not listed")
			}
			got := askedQuestion(t, d, "hosted")
			if got.Message != want.Message || got.Kind != want.Kind || !slices.Equal(got.Options, want.Options) {
				t.Fatalf("the hosted session asked %+v,\nthe server's %+v", got, want)
			}
			var labels []string
			var modes []bool
			for _, o := range got.Options {
				labels, modes = append(labels, o.Label), append(modes, o.Mode)
			}
			switch f.agent {
			case "former-once":
				if strings.Join(labels, "|") != "Allow|Cancel" || slices.Contains(modes, true) {
					t.Fatalf("the two-choice form asked %+v", got.Options)
				}
				return
			case "former-not":
				if strings.Join(labels, "|") != "Seen" {
					t.Fatalf("a screen not plainly the form asked %+v", got.Options)
				}
				return
			}
			if strings.Join(labels, "|") != "Allow|Allow for this session|Always allow|Cancel" || !slices.Equal(modes, []bool{false, true, true, false}) {
				t.Fatalf("the four-choice form asked %+v", got.Options)
			}

			// A routed answer of keys that are no choice's: refused, nothing written.
			hosted, ok := d.(*signal.HostedSession)
			if !ok {
				t.Fatalf("hosted session is a %T", d)
			}
			if err := hosted.AnswerAs(proto.HostAnswerAsMsg{Question: got.ID, Input: "9", ByName: "Ada"}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the routed answer applied", func() bool { return len(logs.outcomes()) == 1 })
			if o := logs.outcomes(); !errors.Is(o[0], session.ErrNotAChoice) {
				t.Fatalf("the routed answer of no choice's keys: %v", o[0])
			}

			// A page's choice: its keys alone reach the program.
			v := dialViewer(t, hs.URL, id, adminToken)
			v.expectJSON(proto.TypeControl, proto.CtlWelcome)
			relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
			v.send(relay)
			v.expectJSON(proto.TypeSignal, proto.SigRelayOK)
			v.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24}))
			v.expectJSON(proto.TypeControl, proto.CtlWelcome)
			v.expectJSON(proto.TypeControl, proto.CtlReady)
			allow := got.Options[0].Input
			v.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "r1", Question: got.ID, Input: allow}))
			if m := v.expectJSON(proto.TypeControl, proto.CtlAnswerOK); m["requestId"] != "r1" {
				t.Fatalf("answer %v", m)
			}
			waitFor(t, "the choice's keys reach the program", func() bool {
				b, _ := os.ReadFile(typed)
				return len(b) > 0
			})
			if b, _ := os.ReadFile(typed); string(b) != allow {
				t.Fatalf("the program read %q, want the choice's keys %q alone", b, allow)
			}
		})
	}
}

// A host not sent the forms asks none: given the recipe without them, the
// bytes a host was sent before forms were a question, its session shows,
// on the four-choice form and the two-choice form, the question that comes
// after the form's in the recipe, never the form or its choices. So does a
// host of an earlier release (its register says nothing of forms) with
// this server; a host of this release with a server of an earlier one
// (which ignores the field: the register reaches the server without it);
// and a host that says it to a switchyard, which never sends the forms.
// Each register is read on its way, so that each case is the one it names.
func TestAHostNotSentFormsIsNotAskedThem(t *testing.T) {
	srv, hs := formServer(t, false)
	yardSrv, yard := formServer(t, true)
	pass := func(map[string]any) {}
	for _, c := range []struct {
		name  string
		srv   *api.Server
		base  string
		edit  func(map[string]any)
		opts  Options
		forms bool
	}{
		{"an older host", srv, hs.URL, pass, Options{Token: hostToken, formsUnsaid: true}, false},
		{"an older server", srv, hs.URL, func(h map[string]any) { delete(h, "forms") }, Options{Token: hostToken}, true},
		{"a switchyard", yardSrv, yard.URL, pass, Options{}, true},
	} {
		for _, f := range formScreens[:2] {
			t.Run(c.name+", "+f.agent, func(t *testing.T) {
				said := make(chan map[string]any, 8)
				p := registerProxy(t, c.base, c.edit, said)
				opts := c.opts
				opts.AgentID, opts.Argv = f.agent, []string{"/bin/sh", "-c", formProgram, "sh", f.screen, "/dev/null"}
				id := runFormHost(t, p.URL, opts)
				if host := <-said; (host["forms"] == true) != c.forms {
					t.Fatalf("the host's register said %v", host)
				}
				d, ok := c.srv.Registry().Get(id)
				if !ok {
					t.Fatal("hosted session not listed")
				}
				if a := askedQuestion(t, d, "hosted"); len(a.Options) != 1 || a.Options[0].Label != "Seen" {
					t.Fatalf("the hosted session asked %+v, want the question after the form's", a)
				}
			})
		}
	}
}

// A host says it reads forms to the owner's own server alone, beside
// answerAs: never to a switchyard (the uplink), and not when it leaves the
// field out as an earlier release does.
func TestTheRegistrationSaysFormsToTheOwnersServerAlone(t *testing.T) {
	if reg := (&agent{opts: Options{}}).registration(80, 24); !reg.Host.Forms || !reg.Host.AnswerAs {
		t.Fatalf("to the owner's own server %+v", reg.Host)
	}
	if reg := (&agent{opts: Options{Switchyard: true}}).registration(80, 24); reg.Host.Forms {
		t.Fatalf("to a switchyard %+v", reg.Host)
	}
	if reg := (&agent{opts: Options{formsUnsaid: true}}).registration(80, 24); reg.Host.Forms || !reg.Host.AnswerAs {
		t.Fatalf("as an earlier release %+v", reg.Host)
	}
}

// The host compiles the recipe the server sent as the server compiles its
// own sessions' (#293); a host publishing to a switchyard (the uplink)
// watches none of it, and one past its bounds is refused whole, the session
// then working as it did without one.
func TestHostWatchesTheRecipeItIsGiven(t *testing.T) {
	codex, ok := catalog.Default().Get("codex")
	if !ok {
		t.Fatal("no codex in the default catalog")
	}
	r := agents.Recipe(codex)
	a := &agent{opts: Options{AgentID: "codex"}, log: discardLog}
	if w := a.watchOf(nil); w.TrustPattern != nil || len(w.Questions) != 0 {
		t.Fatalf("no recipe watched %+v", w)
	}
	w := a.watchOf(r)
	if w.TrustPattern == nil || !w.TrustPattern.MatchString("Trust this folder?") || w.WorkingTitle == nil || !w.WorkingTitle.MatchString("⠋ Working") || len(w.Questions) != len(codex.Questions) || len(w.TrustAnswers) != 2 {
		t.Fatalf("watch %+v", w)
	}
	a.opts.Switchyard = true
	if w := a.watchOf(r); w.TrustPattern != nil || len(w.Questions) != 0 {
		t.Fatalf("a switchyard's recipe was watched: %+v", w)
	}
	a.opts.Switchyard = false
	bad := *r
	bad.Questions = append(slices.Clone(bad.Questions), make([]proto.RecipeQuestion, proto.MaxRecipeQuestions)...)
	if w := a.watchOf(&bad); w.TrustPattern != nil || len(w.Questions) != 0 {
		t.Fatalf("a recipe past its bounds was watched: %+v", w)
	}
	broken := *r
	broken.TrustPrompt = "("
	if w := a.watchOf(&broken); w.TrustPattern != nil || len(w.Questions) != 0 {
		t.Fatalf("a recipe that does not compile was watched: %+v", w)
	}
}
