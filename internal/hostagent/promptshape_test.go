package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// shapeProgram draws its first argument and then writes every key it reads
// into the file its second names, as Codex reads keys.
const shapeProgram = `stty -icanon -echo; printf '%s' "$1"; exec /bin/cat > "$2"`

// shapeFrame is a whole synchronized frame of rows, drawn on the alternate
// screen at 80 x 24 as Codex draws one.
func shapeFrame(rows ...string) string {
	var b strings.Builder
	b.WriteString("\x1b[?1049h\x1b[?2026h\x1b[1;1H\x1b[J")
	for i, r := range rows {
		fmt.Fprintf(&b, "\x1b[%d;1H%s", i+1, r)
	}
	b.WriteString("\x1b[?2026l")
	return b.String()
}

var (
	// Codex's composer at rest, and its update dialog drawn with its footer
	// alone.
	shapeCodexPrompt = shapeFrame("  >_ OpenAI Codex (v0.163.0)", "", "› Ask Codex to do anything", "", "  ? for shortcuts")
	shapeCodexFooter = shapeFrame("", "  enter continue · esc skip")
	// A screen that is neither the prompt nor any question: plain output.
	shapeStarting = shapeFrame("", "  Starting up")
)

// shapeServer is the owner's own server with the built-in catalog, taking
// hostToken, ended as the test ends.
func shapeServer(t *testing.T) (*api.Server, *httptest.Server) {
	t.Helper()
	cfg := config.Defaults()
	cfg.WorkbenchToken = adminToken
	cfg.HostTokens = []string{hostToken}
	cfg.AllowedRoots = []string{t.TempDir()}
	cfg.Dev = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, catalog.Default(), discardLog, nil, nil)
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

// submitFromAPage connects a page to the hosted session id at base, waits
// until it is ready and its screen shows mark, submits text and answers
// the session's word for it: submit_ok, or the error.
func submitFromAPage(t *testing.T, base, id, mark, text string) map[string]any {
	t.Helper()
	v := dialViewer(t, base, id, adminToken)
	v.expectJSON(proto.TypeControl, proto.CtlWelcome)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
	v.send(relay)
	v.expectJSON(proto.TypeSignal, proto.SigRelayOK)
	v.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24}))
	v.expectJSON(proto.TypeControl, proto.CtlWelcome)
	// Ready, and the program's screen seen: the session feeds its screens
	// before it sends output to a viewer.
	var seen []byte
	ready := false
	for !ready || !strings.Contains(string(seen), mark) {
		f, err := v.read()
		if err != nil {
			t.Fatalf("waiting for the screen: %v (ready %v, seen %q)", err, ready, seen)
		}
		switch f.Type {
		case proto.TypeOutput, proto.TypeScrollback:
			seen = append(seen, f.Payload...)
		case proto.TypeControl:
			if strings.Contains(string(f.Payload), `"t":"`+proto.CtlReady+`"`) {
				ready = true
			}
		}
	}
	v.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: text, ReqID: "r1"}))
	for {
		f, err := v.read()
		if err != nil {
			t.Fatalf("waiting for the submit's word: %v", err)
		}
		if f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		if json.Unmarshal(f.Payload, &m) != nil || m["requestId"] != "r1" {
			continue
		}
		if m["t"] == proto.CtlSubmitOK || m["t"] == proto.CtlError {
			return m
		}
	}
}

// readsLine waits until the program read text and its Enter (the terminal
// turns the CR into a line feed).
func readsLine(t *testing.T, typed, text string) {
	t.Helper()
	waitFor(t, "the program reads "+text+" and its Enter", func() bool {
		b, _ := os.ReadFile(typed)
		return string(b) == text+"\n" || string(b) == text+"\r"
	})
}

// A hosted session of Codex, its host saying it takes the prompt shape,
// judges a line by Codex's prompt shape as the server's own session does,
// end to end from a page through the server: on Codex's update dialog
// drawn with its footer alone, and on a screen of plain output, the line
// is refused in the words of a screen that is not the prompt and the
// program reads nothing; at Codex's composer it is typed and entered.
func TestAHostedSessionTypesALineOnlyAtItsAgentsPrompt(t *testing.T) {
	_, hs := shapeServer(t)
	for _, tc := range []struct {
		name, screen, mark string
		typed              bool
	}{
		{"the update dialog's footer alone", shapeCodexFooter, "esc skip", false},
		{"plain output", shapeStarting, "Starting up", false},
		{"Codex's composer", shapeCodexPrompt, "Ask Codex", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			said := make(chan map[string]any, 8)
			p := registerProxy(t, hs.URL, func(map[string]any) {}, said)
			typed := filepath.Join(t.TempDir(), "typed")
			id := runFormHost(t, p.URL, Options{Token: hostToken, AgentID: "codex", Argv: []string{"/bin/sh", "-c", shapeProgram, "sh", tc.screen, typed}})
			if host := <-said; host["promptShape"] != true {
				t.Fatalf("the host's register said %v", host)
			}
			m := submitFromAPage(t, hs.URL, id, tc.mark, "1 fix the bug")
			if tc.typed {
				if m["t"] != proto.CtlSubmitOK {
					t.Fatalf("at the prompt: %v", m)
				}
				readsLine(t, typed, "1 fix the bug")
				return
			}
			if m["t"] != proto.CtlError || m["code"] != proto.ErrCodeNotSent || m["message"] != session.StartupNotAtPromptWords {
				t.Fatalf("the line's word %v, want it refused as not at the prompt", m)
			}
			if b, _ := os.ReadFile(typed); len(b) != 0 {
				t.Fatalf("the program read %q", b)
			}
		})
	}
}

// A host not sent the prompt shape launches as a host did before it was
// sent one: one of an earlier release (its register says nothing of it)
// with this server, and one of this release with a server of an earlier
// one (which ignores the field: the register reaches the server without
// it). Its session judges a line by the rule for an agent with no prompt
// shape, which types a line on a screen of plain output.
func TestAHostNotSentThePromptShapeLaunchesAsBefore(t *testing.T) {
	_, hs := shapeServer(t)
	for _, c := range []struct {
		name  string
		edit  func(map[string]any)
		opts  Options
		shape bool
	}{
		{"an older host", func(map[string]any) {}, Options{Token: hostToken, shapeUnsaid: true}, false},
		{"an older server", func(h map[string]any) { delete(h, "promptShape") }, Options{Token: hostToken}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			said := make(chan map[string]any, 8)
			p := registerProxy(t, hs.URL, c.edit, said)
			typed := filepath.Join(t.TempDir(), "typed")
			opts := c.opts
			opts.AgentID, opts.Argv = "codex", []string{"/bin/sh", "-c", shapeProgram, "sh", shapeStarting, typed}
			id := runFormHost(t, p.URL, opts)
			if host := <-said; (host["promptShape"] == true) != c.shape {
				t.Fatalf("the host's register said %v", host)
			}
			if m := submitFromAPage(t, hs.URL, id, "Starting up", "fix the bug"); m["t"] != proto.CtlSubmitOK {
				t.Fatalf("the line's word %v", m)
			}
			readsLine(t, typed, "fix the bug")
		})
	}
}

// registeredProxy stands between a host and the server at base and hands
// the host the server's registered with its recipe as edit leaves it,
// every other frame each way as it is.
func registeredProxy(t *testing.T, base string, edit func(recipe map[string]any)) *httptest.Server {
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
		if err != nil || upc.Write(ctx, typ, data) != nil {
			return
		}
		typ, data, err = upc.Read(ctx)
		if err != nil {
			return
		}
		var reg map[string]any
		if json.Unmarshal(data, &reg) == nil {
			if recipe, ok := reg["recipe"].(map[string]any); ok {
				edit(recipe)
				data, _ = json.Marshal(reg)
			}
		}
		if down.Write(ctx, typ, data) != nil {
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

// A recipe that carries a prompt shape the host does not take (one that
// does not compile, one past its bound, or a shape beside a question that
// does not compile) starts nothing: Run returns ErrRecipeRefused, in its
// fixed words, before the program starts, and the log says why. A recipe
// with no shape that does not compile is watched as none, as before, and
// the program starts.
func TestARecipeWithAShapeTheHostDoesNotTakeStartsNothing(t *testing.T) {
	_, hs := shapeServer(t)
	for _, c := range []struct {
		name    string
		edit    func(map[string]any)
		refused bool
	}{
		{"a shape that does not compile", func(r map[string]any) { r["promptShape"] = "(" }, true},
		{"a shape past its bound", func(r map[string]any) { r["promptShape"] = strings.Repeat("x", proto.MaxRecipePattern+1) }, true},
		{"a shape that matches an empty screen", func(r map[string]any) { r["promptShape"] = ".*" }, true},
		{"a shape beside a trust prompt that does not compile", func(r map[string]any) { r["trustPrompt"] = "(" }, true},
		{"no shape, a trust prompt that does not compile", func(r map[string]any) { delete(r, "promptShape"); r["trustPrompt"] = "(" }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := registeredProxy(t, hs.URL, c.edit)
			started := filepath.Join(t.TempDir(), "started")
			logs := &logRecorder{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := Run(ctx, Options{ServerURL: p.URL, Token: hostToken, Name: "shape test", HostName: "laptop", RelayOnly: true, AgentID: "codex",
					Argv: []string{"/bin/sh", "-c", `: > "$1"; exec /bin/cat`, "sh", started}, Log: slog.New(logs)})
				done <- err
			}()
			if c.refused {
				var err error
				select {
				case err = <-done:
				case <-time.After(15 * time.Second):
					t.Fatal("Run did not return")
				}
				if !errors.Is(err, ErrRecipeRefused) || err.Error() != "host: the server's recipe for the agent does not validate: nothing was started" {
					t.Fatalf("Run returned %v", err)
				}
				if _, err := os.Stat(started); !os.IsNotExist(err) {
					t.Fatalf("the program started: %v", err)
				}
				if logs.count("the server's recipe for the agent is refused; nothing is started") != 1 {
					t.Fatal("the log does not say why nothing started")
				}
				return
			}
			waitFor(t, "the program starts", func() bool {
				_, err := os.Stat(started)
				return err == nil
			})
			cancel()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("host did not stop")
			}
		})
	}
}

// pickProgram draws its first argument, reads one line (its Enter turned
// into a line feed by the terminal) into the file its third names, then
// draws its second and appends every key it reads after that to the file.
const pickProgram = `stty -icanon -echo; printf '%s' "$1"; IFS= read -r first; printf '%s\n' "$first" > "$3"; printf '%s' "$2"; exec /bin/cat >> "$3"`

// shapePick is a screen with a choice an Enter or a digit would pick,
// made of plain rows no catalog entry knows.
var shapePick = shapeFrame("  Choose a colour for the chart", "", "  › 1. Blue", "    2. Green", "    3. Red", "", "  Enter to choose · Esc to close")

// A hosted session of Codex, its host sent the prompt shape, types a line
// only at Codex's prompt for the whole of the session, end to end from a
// page through the server: its first line is typed and entered at the
// composer; then, with a choice on the screen and no prompt, a line is
// refused in the words of a screen that is not the agent's prompt and the
// program reads nothing more, no text and no Enter.
func TestAHostedSessionTypesEveryLineOnlyAtThePrompt(t *testing.T) {
	_, hs := shapeServer(t)
	said := make(chan map[string]any, 8)
	p := registerProxy(t, hs.URL, func(map[string]any) {}, said)
	typed := filepath.Join(t.TempDir(), "typed")
	id := runFormHost(t, p.URL, Options{Token: hostToken, AgentID: "codex", Argv: []string{"/bin/sh", "-c", pickProgram, "sh", shapeCodexPrompt, shapePick, typed}})
	if host := <-said; host["promptShape"] != true {
		t.Fatalf("the host's register said %v", host)
	}
	if m := submitFromAPage(t, hs.URL, id, "Ask Codex", "read the code"); m["t"] != proto.CtlSubmitOK {
		t.Fatalf("the first line at the prompt: %v", m)
	}
	readsLine(t, typed, "read the code")
	m := submitFromAPage(t, hs.URL, id, "Choose a colour", "1 fix the bug")
	if m["t"] != proto.CtlError || m["code"] != proto.ErrCodeNotSent || m["message"] != session.ErrNotAtPrompt.Error() {
		t.Fatalf("the line at the choice: %v, want it refused as not at the prompt", m)
	}
	if b, _ := os.ReadFile(typed); string(b) != "read the code\n" {
		t.Fatalf("the program read %q", b)
	}
}
