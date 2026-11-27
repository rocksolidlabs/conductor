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

// No environment variable and no config-file key gives the server the
// desktop app's key: the app hands it over on the server's stdin (serve
// --secrets-stdin). Its form, and its every print, are the Secret's.
func TestDesktopKeyComesFromNoVariableNorFile(t *testing.T) {
	const planted = "PLANTEDdesktopKEY_0123456789-abcdefWXYZ"
	t.Setenv("CONDUCTOR_DESKTOP_KEY", planted)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DesktopKey.Set() {
		t.Fatal("the environment gave the server a desktop key")
	}
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"desktopKey": "`+planted+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("a config file naming the key: %v", err)
	}
	cfg.DesktopKey = NewSecret(planted)
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
	for _, bad := range []string{strings.Repeat("a", 31), strings.Repeat("a", 257), strings.Repeat("a", 31) + " ", strings.Repeat("a", 40) + ",", strings.Repeat("é", 32), strings.Repeat("a", 40) + "\n"} {
		if DesktopKeyForm(bad) {
			t.Errorf("%.12q has the key's form", bad)
		}
	}
	for _, good := range []string{strings.Repeat("a", 32), strings.Repeat("Z9_-", 64)} {
		if !DesktopKeyForm(good) {
			t.Errorf("a key of %d has not the key's form", len(good))
		}
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

// A Secret compares itself in constant time and never matches an empty
// side: an unset key matches nothing, the empty string included.
func TestASecretComparesItself(t *testing.T) {
	const k = "PLANTEDdesktopKEY_0123456789-abcdefWXYZ"
	s := NewSecret(k)
	if !s.Equal(k) || s.Equal(k[:len(k)-1]) || s.Equal("") || s.Equal(k+"x") {
		t.Fatal("a set Secret compares wrongly")
	}
	var unset Secret
	if unset.Equal("") || unset.Equal(k) {
		t.Fatal("an unset Secret matches")
	}
}
