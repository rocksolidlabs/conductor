package api

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/config"
)

// yoloScript prints the arguments it was given and two variables, then waits.
var yoloScript = []string{"/bin/sh", "-c", `printf 'ARGS[%s] MODE[%s] AGENT[%s]\n' "$*" "$YOLO_MODE" "$AGENT_VAR"; exec /bin/cat`, "sh"}

// saveYoloAgent saves an agent running yoloScript with the given yolo recipe
// (none when nil) and adapter.
func (e *testEnv) saveYoloAgent(id, adapter string, yolo map[string]any) {
	e.t.Helper()
	body := agentBody(id)
	body["command"] = yoloScript
	body["allowArgs"] = true
	body["env"] = map[string]string{"AGENT_VAR": "mine", "YOLO_MODE": "careful"}
	if adapter != "" {
		body["adapter"] = adapter
		body["signal"] = map[string]any{"kind": "bell"}
	}
	if yolo != nil {
		body["yolo"] = yolo
	}
	e.save(body)
}

func commandOf(info map[string]any) []string {
	var out []string
	for _, a := range info["command"].([]any) {
		out = append(out, a.(string))
	}
	return out
}

// A launch with yolo on applies the agent's recipe: its arguments after the
// command and the user's, its environment over the agent's own, and the
// session says so; yolo false, or the server's default when the launch says
// nothing, leaves it out.
func TestCreateSessionAppliesTheYoloRecipe(t *testing.T) {
	for _, def := range []bool{false, true} {
		e := newTestEnv(t, func(c *config.Config) { c.Yolo = def })
		e.saveYoloAgent("bold", "", map[string]any{"args": []string{"--yes", "--all"}, "env": map[string]string{"YOLO_MODE": "auto"}})
		for _, tc := range []struct {
			yolo   any
			on     bool
			output string
		}{
			{nil, def, ""},
			{true, true, ""},
			{false, false, ""},
		} {
			req := map[string]any{"agentId": "bold", "args": []string{"--resume"}}
			if tc.yolo != nil {
				req["yolo"] = tc.yolo
			}
			resp, info := e.do("POST", "/api/sessions", adminToken, req)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("launch: %d %v", resp.StatusCode, info)
			}
			id := info["id"].(string)
			t.Cleanup(func() { _ = e.local(id).Stop(t.Context()) })
			want := append(slices.Clone(yoloScript), "--resume")
			out := "ARGS[--resume] MODE[careful] AGENT[mine]"
			if tc.on {
				want = append(want, "--yes", "--all")
				out = "ARGS[--resume --yes --all] MODE[auto] AGENT[mine]"
			}
			if got := commandOf(info); !slices.Equal(got, want) || (info["yolo"] == true) != tc.on {
				t.Fatalf("default %v, yolo %v: command %q, yolo %v", def, tc.yolo, got, info["yolo"])
			}
			c := dialViewer(t, e, id, adminToken)
			c.hello(80, 24)
			c.expectOutput(out)
		}
	}
}

// Yolo on for an agent with no recipe, or with an empty one, launches it as
// it would be without, with no badge and a notice in its activity.
func TestYoloWithoutARecipeLaunchesPlainlyWithANotice(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.Yolo = true })
	e.saveYoloAgent("plain", "", nil)
	e.saveYoloAgent("emptied", "", map[string]any{})
	for _, id := range []string{"plain", "emptied"} {
		info := e.launch(id, nil)
		if info["yolo"] == true || len(commandOf(info)) != len(yoloScript) {
			t.Fatalf("%s: %v", id, info)
		}
		found := false
		for _, entry := range e.local(info["id"].(string)).Activity() {
			found = found || strings.Contains(entry.Message, "has no yolo recipe")
		}
		if !found {
			t.Fatalf("%s: no notice", id)
		}
	}
}

// A recipe cannot set Conductor's own variables, and is bounded.
func TestYoloRecipeIsValidated(t *testing.T) {
	e := newTestEnv(t, nil)
	for _, yolo := range []map[string]any{
		{"env": map[string]string{"CONDUCTOR_NOTIFY_TOKEN": "x"}},
		{"env": map[string]string{"BAD NAME": "x"}},
		{"args": []string{""}},
		{"args": slices.Repeat([]string{"-y"}, 17)},
	} {
		body := agentBody("bad")
		body["yolo"] = yolo
		resp, out := e.do("POST", "/api/catalog", adminToken, body)
		if resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_agent" {
			t.Fatalf("%v: %d %v", yolo, resp.StatusCode, out)
		}
	}
}

// Codex's trust override goes with its yolo recipe, naming the launch's
// directory, and never without it.
func TestCodexTrustGoesWithYolo(t *testing.T) {
	e := newTestEnv(t, nil)
	e.saveYoloAgent("cx", "codex", map[string]any{"args": []string{"--bypass"}})
	on := e.do2(map[string]any{"agentId": "cx", "yolo": true})
	// Codex's launches also register Conductor's MCP server, last.
	mcp := agents.MCPFor("codex", filepath.Join(e.srv.cfg.DataDir, "hooks"), true)
	want := append(append(slices.Clone(yoloScript), "--bypass", "-c", `projects={"`+realRoot(t, e.root)+`"={trust_level="trusted"}}`), mcp...)
	if got := commandOf(on); !slices.Equal(got, want) {
		t.Fatalf("command %q, want %q", got, want)
	}
	off := e.do2(map[string]any{"agentId": "cx"})
	if got := commandOf(off); !slices.Equal(got, append(slices.Clone(yoloScript), mcp...)) {
		t.Fatalf("without yolo: %q", got)
	}
}

func (e *testEnv) do2(req map[string]any) map[string]any {
	e.t.Helper()
	resp, info := e.do("POST", "/api/sessions", adminToken, req)
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("launch: %d %v", resp.StatusCode, info)
	}
	id := info["id"].(string)
	e.t.Cleanup(func() { _ = e.local(id).Stop(e.t.Context()) })
	return info
}

// A crew's yolo choice is the run's, and every member follows it.
func TestACrewsYoloIsTheRuns(t *testing.T) {
	e := newTestEnv(t, nil)
	e.saveYoloAgent("bold", "", map[string]any{"args": []string{"--yes"}})
	id := e.sendCrew("POST", "/api/crews", map[string]any{
		"name": "Bold", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none", "yolo": true,
		"members": []any{map[string]any{"name": "a", "agentId": "bold", "prompt": "", "start": map[string]any{"when": "immediately"}}},
	}, http.StatusCreated)["id"].(string)
	resp, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, out)
	}
	run := out["run"].(map[string]any)
	sid := e.waitRunning(t, run["id"].(string), "a")
	if run["yolo"] != true || !e.local(sid).Info().Yolo {
		t.Fatalf("run %v, member yolo %v", run["yolo"], e.local(sid).Info().Yolo)
	}
	t.Cleanup(func() { _ = e.local(sid).Stop(t.Context()) })
}
