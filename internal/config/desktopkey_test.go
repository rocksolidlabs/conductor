package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The desktop app's key comes from the environment alone, into a Secret no
// print of the configuration carries; a config file may not name it; one
// of the wrong form stops the start in words that hold none of it.
func TestDesktopKeyFromTheEnvironmentAlone(t *testing.T) {
	const planted = "PLANTEDdesktopKEY_0123456789-abcdefWXYZ"
	t.Setenv("CONDUCTOR_DESKTOP_KEY", planted)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DesktopKey.Set() || cfg.DesktopKey.Reveal() != planted {
		t.Fatalf("the key %v", cfg.DesktopKey)
	}
	var out bytes.Buffer
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		fmt.Fprintf(&out, verb+"\n", cfg)
		fmt.Fprintf(&out, verb+"\n", cfg.DesktopKey)
	}
	b, _ := json.Marshal(cfg)
	out.Write(b)
	slog.New(slog.NewTextHandler(&out, nil)).Info("config", "config", cfg, "key", cfg.DesktopKey)
	if strings.Contains(out.String(), planted) || strings.Contains(out.String(), planted[7:20]) {
		t.Fatalf("a print of the configuration holds the key:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "key=set") {
		t.Fatalf("slog's form of the key is not set:\n%s", out.String())
	}
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"desktopKey": "`+planted+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("a config file naming the key: %v", err)
	}
	for _, bad := range []string{strings.Repeat("a", 31), strings.Repeat("a", 257), strings.Repeat("a", 31) + " ", strings.Repeat("a", 40) + ",", strings.Repeat("é", 32), strings.Repeat("a", 40) + "\n"} {
		t.Setenv("CONDUCTOR_DESKTOP_KEY", bad)
		_, err := Load("")
		if err == nil || !strings.Contains(err.Error(), "CONDUCTOR_DESKTOP_KEY must be 32 to 256 characters") || strings.Contains(err.Error(), strings.TrimSpace(bad)) {
			t.Errorf("key %.12q: %v", bad, err)
		}
	}
	for _, good := range []string{strings.Repeat("a", 32), strings.Repeat("Z9_-", 64)} {
		t.Setenv("CONDUCTOR_DESKTOP_KEY", good)
		if cfg, err := Load(""); err != nil || cfg.DesktopKey.Reveal() != good {
			t.Errorf("key of %d: %v", len(good), err)
		}
	}
	t.Setenv("CONDUCTOR_DESKTOP_KEY", "")
	if cfg, err := Load(""); err != nil || cfg.DesktopKey.Set() || cfg.DesktopKey.String() != "unset" {
		t.Fatalf("no key: %v %v", cfg.DesktopKey, err)
	}
}

// A root overlaps the data directory as DataDirOverlap judges each.
func TestOverlapsDataDir(t *testing.T) {
	dir := t.TempDir()
	c := &Config{DataDir: filepath.Join(dir, "data")}
	for root, want := range map[string]bool{dir: true, filepath.Join(dir, "data", "x"): true, c.DataDir: true, filepath.Join(dir, "other"): false} {
		if got := c.OverlapsDataDir(root); got != want {
			t.Errorf("%s: %v", root, got)
		}
	}
}
