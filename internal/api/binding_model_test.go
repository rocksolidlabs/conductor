package api

import (
	"net/http"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// A model is chosen in one place, judged over the whole command line the
// launch is started with, by what the adapter says chooses one: Codex's
// short flag and its configuration override in the launch's arguments,
// the long flag in a yolo recipe's arguments and in the entry's command,
// each beside a model, are refused; a crew member the same.
func TestModelChosenOnceOverTheCommandLine(t *testing.T) {
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
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: append(list, yolo, command)})
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(cat)
	for _, body := range []map[string]any{
		{"agentId": "codex-p", "model": "gpt-y", "args": []string{"-m", "o3"}},
		{"agentId": "codex-p", "model": "gpt-y", "args": []string{"-mo3"}},
		{"agentId": "codex-p", "model": "gpt-y", "args": []string{"-c", "model=o3"}},
		{"agentId": "codex-p", "model": "gpt-y", "args": []string{"--config=model=\"o3\""}},
		{"agentId": "codex-yolo-model", "model": "gpt-y", "yolo": true},
		{"agentId": "claude-command-model", "model": "opus"},
	} {
		e.expect(t, "POST", "/api/sessions", body, http.StatusBadRequest, "invalid_request")
	}
	// A configuration override of another key is no model.
	sid := e.launch(t, "codex-p", map[string]any{"model": "gpt-y", "args": []string{"-c", "model_reasoning_effort=high"}})
	e.probe(t, sid)
	id := e.sendCrew("POST", "/api/crews", map[string]any{"name": "Model", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none",
		"members": []any{map[string]any{"name": "lead", "agentId": "codex-p", "prompt": "", "model": "gpt-y", "args": []string{"-m", "o3"}, "start": map[string]any{"when": "immediately"}}}}, http.StatusCreated)["id"].(string)
	if status, data, out := e.call(t, "POST", "/api/crews/"+id+"/launch", nil); status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Fatalf("a member's model beside -m: %d %s", status, data)
	}
}
