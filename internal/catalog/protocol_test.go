package catalog

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// An agent's protocol recipe is held to the bounds of a yolo recipe: a
// name of an id's form, at most 16 arguments of 1 to 4096 bytes without
// NUL, no arguments without a name. {} turns an inherited recipe off; a
// saved override that says nothing takes the replaced agent's; a copy
// shares nothing with the agent it was made from. Claude Code's built-in
// entry declares claude-stream-json. (Whether a name is one Conductor
// speaks is agentproto.Check's, from config.LoadCatalog.)
func TestCatalogProtocolValidated(t *testing.T) {
	agent := func(r *ProtocolRecipe) Agent {
		return Agent{ID: "a", Name: "a", Command: []string{"a"}, Protocol: r}
	}
	sixteen := make([]string, 16)
	for i := range sixteen {
		sixteen[i] = strings.Repeat("x", 4096)
	}
	for _, r := range []*ProtocolRecipe{
		nil, {}, {Name: "claude-stream-json"}, {Name: "x", Args: sixteen}, {Name: strings.Repeat("a", 32)},
	} {
		if err := validate(agent(r)); err != nil {
			t.Errorf("%+v refused: %v", r, err)
		}
	}
	for _, r := range []*ProtocolRecipe{
		{Name: "Claude"},
		{Name: "claude stream"},
		{Name: strings.Repeat("a", 33)},
		{Name: "x", Args: append(slices.Clone(sixteen), "y")},
		{Name: "x", Args: []string{""}},
		{Name: "x", Args: []string{strings.Repeat("x", 4097)}},
		{Name: "x", Args: []string{"a\x00b"}},
		{Args: []string{"--x"}},
	} {
		err := validate(agent(r))
		if err == nil || !strings.Contains(err.Error(), "agent a: protocol:") {
			t.Errorf("%+v taken: %v", r, err)
		}
	}

	// The built-in.
	cat := Default()
	claude, _ := cat.Get("claude")
	if claude.Protocol == nil || claude.Protocol.Name != "claude-stream-json" || len(claude.Protocol.Args) != 0 {
		t.Fatalf("Claude Code's protocol: %+v", claude.Protocol)
	}
	for _, a := range cat.List() {
		if a.ID != "claude" && a.Protocol != nil {
			t.Errorf("%s declares a protocol before its row: %+v", a.ID, a.Protocol)
		}
	}

	// Inherited by an override that says nothing, turned off by {}.
	if err := cat.ApplyOverlay(Overlay{Agents: []Agent{{ID: "claude", Name: "Mine", Command: []string{"claude"}}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := cat.Get("claude"); got.Protocol.Off() || got.Protocol.Name != "claude-stream-json" {
		t.Fatalf("an override that says nothing: %+v", got.Protocol)
	}
	if err := cat.ApplyOverlay(Overlay{Agents: []Agent{{ID: "claude", Name: "Mine", Command: []string{"claude"}, Protocol: &ProtocolRecipe{}}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := cat.Get("claude"); got.Protocol == nil || !got.Protocol.Off() {
		t.Fatalf("an override with {}: %+v", got.Protocol)
	}
	if got := inherit(Agent{ID: "b", Name: "b", Command: []string{"b"}}, agent(&ProtocolRecipe{Name: "x", Args: []string{"--a"}}), true); got.Protocol == nil || got.Protocol.Name != "x" || !slices.Equal(got.Protocol.Args, []string{"--a"}) {
		t.Fatalf("inherit: %+v", got.Protocol)
	}

	// A copy (a clone, the catalog's deep copy) carries it and shares nothing.
	src := agent(&ProtocolRecipe{Name: "x", Args: []string{"--a"}})
	cp := src.Copy()
	if cp.Protocol == nil || cp.Protocol == src.Protocol || cp.Protocol.Name != "x" {
		t.Fatalf("copy: %+v", cp.Protocol)
	}
	cp.Protocol.Args[0] = "--changed"
	if src.Protocol.Args[0] != "--a" {
		t.Fatal("the copy shares its arguments with the agent")
	}
	var c Catalog
	if err := c.Upsert(src); err != nil {
		t.Fatal(err)
	}
	src.Protocol.Name = "changed"
	if got, _ := c.Clone().Get("a"); got.Protocol.Name != "x" {
		t.Fatalf("the catalog shares the recipe it was given: %+v", got.Protocol)
	}

	// On the wire it is protocol: {name, args}, absent when nil.
	b, _ := json.Marshal(agent(&ProtocolRecipe{Name: "x", Args: []string{"--a"}}))
	if !strings.Contains(string(b), `"protocol":{"name":"x","args":["--a"]}`) {
		t.Fatalf("json: %s", b)
	}
	if b, _ := json.Marshal(agent(nil)); strings.Contains(string(b), "protocol") {
		t.Fatalf("json without one: %s", b)
	}
}
