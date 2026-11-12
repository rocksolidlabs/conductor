package api

import (
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// The model is one promise the server keeps: a model a launch names goes
// to the adapter's model arguments after the launch's own, and the launch's
// own arguments (a request's, a crew member's) may not choose one beside
// it. Each form the adapter's list names (ChoosesModel) is refused, for a
// session and for a member at its admission. That is the whole of it: an
// entry's command, its yolo recipe and its fresh session's recipe are the
// catalog's own configuration and are not read for a model, so a launch
// naming a model beside one of them that chooses another is started with
// the launch's model passed after the launch's arguments, and which of the
// two the tool takes is the tool's.
func TestModelJudgedInTheLaunchsOwnArguments(t *testing.T) {
	e := newBindEnv(t, nil)
	off := false
	list := e.srv.Catalog().List()
	stub := func(id, adapter string) catalog.Agent {
		return catalog.Agent{ID: id, Name: id, Adapter: adapter, Command: []string{"/bin/sh", "-c", loginProbe}, Env: map[string]string{"PROBE_DIR": e.probes}, AllowArgs: true, Probe: &off}
	}
	yolo := stub("codex-yolo-model", "codex")
	yolo.Yolo = &catalog.Yolo{Args: []string{"--model=gpt-x"}}
	command := stub("claude-command-model", "claude")
	command.Command = append(command.Command, "--model=sonnet")
	start := stub("claude-start-model", "claude")
	start.Session = &catalog.SessionRecipe{ResumeArgs: []string{"--resume", "{id}"}, StartArgs: []string{"--session-id", "{id}", "--model", "sonnet"}, NewID: "uuid", IDPattern: "^[0-9a-f-]+$"}
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: append(list, yolo, command, start)})
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(cat)
	forms := map[string][][]string{
		"claude-p": {{"--model", "sonnet"}, {"--verbose", "--model=sonnet"}},
		"codex-p": {{"--model", "o3"}, {"--model=o3"}, {"-m", "o3"}, {"-mo3"}, {"-c", "model=o3"}, {"-c", ` model = "o3"`},
			{"--config", "model=o3"}, {"--config=model=\"o3\""}, {"-cmodel=o3"}, {"-c=model=o3"}},
	}
	n := 0
	for agent, lines := range forms {
		for _, args := range lines {
			e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": agent, "model": "m-1", "args": args}, http.StatusBadRequest, "invalid_request")
			n++
			id := e.sendCrew("POST", "/api/crews", map[string]any{"name": "Model " + strconv.Itoa(n), "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none",
				"members": []any{map[string]any{"name": "lead", "agentId": agent, "prompt": "", "model": "m-1", "args": args, "start": map[string]any{"when": "immediately"}}}}, http.StatusCreated)["id"].(string)
			if status, data, out := e.call(t, "POST", "/api/crews/"+id+"/launch", nil); status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
				t.Errorf("%s: a member's model beside %q: %d %s", agent, args, status, data)
			}
		}
	}
	// A configuration override of another key is no model.
	started := func(agent string, body map[string]any, model string) {
		t.Helper()
		sid := e.launch(t, agent, body)
		e.probe(t, sid)
		l, _ := e.srv.lookupLocal(sid)
		if !slices.Contains(l.Info().Command, "--model="+model) || l.Launched().Model != model {
			t.Errorf("%s: started as %q, model %q; want --model=%s passed", agent, l.Info().Command, l.Launched().Model, model)
		}
	}
	started("codex-p", map[string]any{"model": "gpt-y", "args": []string{"-c", "model_reasoning_effort=high"}}, "gpt-y")
	// The catalog's own configuration is not read for a model: the entry's
	// command, a yolo recipe's arguments and a fresh session's recipe that
	// choose one, each beside a launch's model, start with it passed.
	started("claude-command-model", map[string]any{"model": "m-2"}, "m-2")
	started("codex-yolo-model", map[string]any{"model": "m-2", "yolo": true}, "m-2")
	started("claude-start-model", map[string]any{"model": "m-2"}, "m-2")
}
