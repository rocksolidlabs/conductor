package hostagent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
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
)

// formAgent is an agent asked Codex's questions of the built-in catalog,
// its form asking to allow a tool among them, and after them one that the
// form's screen shows too, with the one choice "Seen": a session that does
// not ask the form asks that one at the same look. (Codex's startup
// question of the background server makes room for it: an agent has 8
// questions at most.) Its own command draws the four-choice form and reads
// what it is typed.
func formAgent(t *testing.T) catalog.Agent {
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
	return catalog.Agent{ID: "former", Name: "former", Command: []string{"/bin/sh", "-c", `stty -icanon -echo; printf '%s' "$1"; exec /bin/cat >/dev/null`, "sh", hostedToolForm},
		Questions: append(qs, catalog.Question{Prompt: `enter to submit\W*esc to cancel\s*\z`, Words: `enter to submit`, Anytime: true, Answers: []catalog.Answer{{Label: "Seen", Input: "x"}}})}
}

// A hosted session is not given Codex's form asking to allow a tool of an
// MCP server (#716): a host is sent the recipe without it
// (agents.HostRecipe), and the form is answered in the host's terminal, as
// before it was a question. Through a real host, the four-choice form and
// the two-choice form each show the question that comes after the form's
// in the recipe, never the form with its choices; a server session of the
// same agent asks the form with its four.
func TestAHostedSessionIsNotAskedCodexsToolApprovalForm(t *testing.T) {
	cat, err := catalog.Load(catalog.File{Agents: []catalog.Agent{formAgent(t)}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.WorkbenchToken = adminToken
	cfg.HostTokens = []string{hostToken}
	root := t.TempDir()
	cfg.AllowedRoots = []string{root}
	cfg.DefaultCwd = root
	cfg.Dev = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, cat, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
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
	asked := func(d session.Driver, what string) session.Attention {
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

	for name, screen := range map[string]string{"four choices": hostedToolForm, "two choices": hostedToolFormOnce} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			registered := make(chan string, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, err := Run(ctx, Options{
					ServerURL: hs.URL, Token: hostToken, Name: "form test", HostName: "laptop", AgentID: "former",
					Argv:      []string{"/bin/sh", "-c", `stty -icanon -echo; printf '%s' "$1"; exec /bin/cat >/dev/null`, "sh", screen},
					RelayOnly: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
					Registered: func(id, _ string) { registered <- id },
				})
				if err != nil {
					t.Errorf("run: %v", err)
				}
			}()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
					t.Error("host did not stop")
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
			if a := asked(d, "hosted"); len(a.Options) != 1 || a.Options[0].Label != "Seen" {
				t.Fatalf("the hosted session asked %+v, want the question after the form's", a)
			}
		})
	}

	// The server's own session of the same agent asks the form, with its four choices.
	req, err := http.NewRequest("POST", hs.URL+"/api/sessions", strings.NewReader(`{"agentId":"former"}`))
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
	a := asked(d, "server")
	var got []string
	for _, o := range a.Options {
		got = append(got, o.Label)
	}
	if strings.Join(got, "|") != "Allow|Allow for this session|Always allow|Cancel" {
		t.Fatalf("the server's session asked %+v, want the form with its four choices", a)
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
