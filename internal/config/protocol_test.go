package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// An agent of the config, inline or in its catalog file, that declares a
// protocol Conductor does not speak stops startup (agentproto.Check), as an
// unknown adapter does; the one Conductor speaks, and {} (off), load.
func TestCatalogProtocolValidated(t *testing.T) {
	cfg := Defaults()
	cfg.Catalog = catalog.File{Agents: []catalog.Agent{{ID: "g", Name: "g", Command: []string{"g"}, Protocol: &catalog.ProtocolRecipe{Name: "nope"}}}}
	if _, err := cfg.LoadCatalog(); err == nil || !strings.Contains(err.Error(), `config catalog: agent g: protocol "nope" is not one Conductor speaks`) {
		t.Fatalf("inline: %v", err)
	}
	for _, r := range []*catalog.ProtocolRecipe{{Name: "claude-stream-json", Args: []string{"--effort=high"}}, {}, nil} {
		cfg.Catalog.Agents[0].Protocol = r
		cat, err := cfg.LoadCatalog()
		if err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
		if g, _ := cat.Get("g"); (g.Protocol == nil) != (r == nil) {
			t.Fatalf("%+v loaded as %+v", r, g.Protocol)
		}
	}
	// The shape is the catalog's: a bad name is refused before the set is
	// asked.
	cfg.Catalog.Agents[0].Protocol = &catalog.ProtocolRecipe{Name: "Bad Name"}
	if _, err := cfg.LoadCatalog(); err == nil || !strings.Contains(err.Error(), "protocol: name must match") {
		t.Fatalf("a bad name: %v", err)
	}

	path := filepath.Join(t.TempDir(), "agents.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"id":"h","name":"h","command":["h"],"protocol":{"name":"acp-v9"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = Defaults()
	cfg.CatalogPath = path
	if _, err := cfg.LoadCatalog(); err == nil || !strings.Contains(err.Error(), "catalog "+path+`: agent h: protocol "acp-v9" is not one Conductor speaks`) {
		t.Fatalf("catalog file: %v", err)
	}

	// The built-in Claude Code loads with its declaration.
	cat, err := Defaults().LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := cat.Get("claude"); c.Protocol == nil || c.Protocol.Name != "claude-stream-json" {
		t.Fatalf("Claude Code: %+v", c.Protocol)
	}
}
