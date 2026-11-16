package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The payloads of a live run of each agent (Codex 0.161.0, Copilot CLI
// 1.0.91, Antigravity 1.2.14, Goose 1.54.0 against a scripted model,
// captured 2026-10-09 in a scratch repository
// at /r, redacted; round 13, G2b), each tool call mapped to the files it
// touched. The run: cat README.md, create hello.txt with "hi", append
// "there" to it, make main.go print hello.
func TestMappersReadTheCapturedPayloads(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"README.md", "hello.txt", "main.go"} {
		// (Goose names its files relative to working_dir; the others absolute.)
		os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644)
	}
	p := func(f string) string { return filepath.Join(dir, f) }
	read, write, edit := func(f string) FileRef { return FileRef{"read", p(f)} }, func(f string) FileRef { return FileRef{"write", p(f)} }, func(f string) FileRef { return FileRef{"edit", p(f)} }
	cases := []struct {
		file   string
		event  string
		mapper func([]byte) (Request, bool)
		want   [][]FileRef
	}{
		{"codex-hooks.json", "PostToolUse", MapCodexHook, [][]FileRef{
			{read("README.md")}, {write("hello.txt")}, {edit("hello.txt")}, {read("main.go")}, {edit("main.go")},
		}},
		{"copilot-hooks.json", "postToolUse", MapCopilotHook, [][]FileRef{
			{read("README.md")}, nil, {write("hello.txt"), edit("hello.txt"), read("hello.txt")}, {read("main.go")}, {edit("main.go")},
		}},
		{"agy-hooks.json", "PostToolUse", MapAgyHook, [][]FileRef{
			nil, {read("README.md")}, {read("main.go")}, {write("hello.txt")}, {edit("hello.txt")}, {edit("main.go")}, {read("README.md")},
		}},
		// Goose 1.54.0, run against a scripted model (its own tools and hooks, round 13 follow-up).
		{"goose-hooks.json", "PostToolUse", MapGooseHook, [][]FileRef{
			{read("README.md")}, {write("hello.txt")}, {edit("hello.txt")}, {edit("main.go")},
		}},
	}
	for _, c := range cases {
		raw, err := os.ReadFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatal(err)
		}
		var hooks []struct {
			Event   string          `json:"event"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &hooks); err != nil {
			t.Fatal(err)
		}
		var got [][]FileRef
		for _, h := range hooks {
			if h.Event != c.event {
				continue
			}
			// The scratch repository's paths, quoted or inside a patch's text, made the test's directory.
			payload := strings.NewReplacer(`"/r/`, `"`+dir+`/`, `"/r"`, `"`+dir+`"`, ` /r/`, ` `+dir+`/`).Replace(string(h.Payload))
			req, ok := c.mapper([]byte(payload))
			if !ok || req.Event != "tool_use" || req.Tool == "" {
				t.Fatalf("%s: %s mapped to %+v %v", c.file, payload, req, ok)
			}
			got = append(got, req.Files)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got  %v\n want %v", c.file, got, c.want)
		}
	}
}

// The same captured runs: a tool about to run (and, where the agent reports
// it, a prompt submitted) is the agent working, as Claude Code's PreToolUse
// is (#195): the agent's question set aside during a turn, or a needs_input
// set mid-turn, gives way while it works, not only at the turn's end.
func TestMappersReadTheCapturedPreToolPayloadsAsWorking(t *testing.T) {
	cases := []struct {
		file   string
		events []string
		mapper func([]byte) (Request, bool)
		least  int
	}{
		{"codex-hooks.json", []string{"PreToolUse"}, MapCodexHook, 5},
		{"copilot-hooks.json", []string{"preToolUse", "userPromptSubmitted"}, MapCopilotHook, 6},
		{"agy-hooks.json", []string{"PreToolUse"}, MapAgyHook, 7},
		{"goose-hooks.json", []string{"PreToolUse", "UserPromptSubmit"}, MapGooseHook, 5},
	}
	for _, c := range cases {
		raw, err := os.ReadFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatal(err)
		}
		var hooks []struct {
			Event   string          `json:"event"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &hooks); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, h := range hooks {
			if !slices.Contains(c.events, h.Event) {
				continue
			}
			n++
			got, ok := c.mapper(h.Payload)
			if !ok || got.State != "working" || got.Event != "" {
				t.Errorf("%s %s: %+v %v, want working", c.file, h.Event, got, ok)
			}
		}
		if n < c.least {
			t.Errorf("%s: %d payloads of %v, want at least %d", c.file, n, c.events, c.least)
		}
	}
}

// Every captured Codex hook payload that maps to an attention state (the
// PreToolUse's working, the Stop's done) carries its turn's name, the
// payload's turn_id, so that the session tells a turn's end told twice
// (its Stop hook, then its notify program, whose turn-id is the same
// value) from the next turn's; an event (PostToolUse) carries none, its
// route having no such field.
func TestTheCapturedCodexHooksNameTheirTurn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var hooks []struct {
		Event   string `json:"event"`
		Payload struct {
			TurnID string `json:"turn_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &hooks); err != nil {
		t.Fatal(err)
	}
	var raws []struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &raws); err != nil {
		t.Fatal(err)
	}
	named := 0
	for i, h := range hooks {
		got, ok := MapCodexHook(raws[i].Payload)
		switch {
		case !ok:
		case got.Event != "":
			if got.TurnID != "" {
				t.Errorf("%s: the event %s names turn %q", h.Event, got.Event, got.TurnID)
			}
		case h.Payload.TurnID == "" || got.TurnID != h.Payload.TurnID:
			t.Errorf("%s: %+v names turn %q, want the payload's %q", h.Event, got, got.TurnID, h.Payload.TurnID)
		default:
			named++
		}
	}
	if named < 6 {
		t.Fatalf("%d reports named their turn, want the five PreToolUse and the Stop", named)
	}
}
