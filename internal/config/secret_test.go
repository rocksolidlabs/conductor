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

	"github.com/rocksolidlabs/conductor/internal/pty"
)

// The environment's GitHub token is read at start into a Secret, and no
// form of the configuration carries it: every fmt verb, slog's text and
// JSON handlers and encoding/json give "set" (or nothing), a config file
// may not name it, and no child process's environment the server builds
// holds it. Its host defaults to github.com and is held to the tracker's
// host rule, the token to its form, and neither error names the token.
func TestGitHubTokenFromEnvironment(t *testing.T) {
	const planted = "ghp_PLANTEDconfigTOKEN0123456789abcdefWXYZ"
	t.Setenv("CONDUCTOR_GITHUB_TOKEN", planted)
	t.Setenv("CONDUCTOR_GITHUB_HOST", "")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GitHubToken.Set() || cfg.GitHubToken.Reveal() != planted || cfg.GitHubHost != "github.com" {
		t.Fatalf("the token %v for %q", cfg.GitHubToken, cfg.GitHubHost)
	}
	var out bytes.Buffer
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		fmt.Fprintf(&out, verb+"\n", cfg)
		fmt.Fprintf(&out, verb+"\n", *cfg)
		fmt.Fprintf(&out, verb+"\n", cfg.GitHubToken)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out.Write(b)
	b, _ = json.Marshal(cfg.GitHubToken)
	out.Write(b)
	slog.New(slog.NewTextHandler(&out, nil)).Info("config", "config", cfg, "token", cfg.GitHubToken)
	slog.New(slog.NewJSONHandler(&out, nil)).Info("config", "config", cfg, "token", cfg.GitHubToken)
	printed := out.String()
	if strings.Contains(printed, planted) || strings.Contains(printed, planted[4:16]) {
		t.Fatalf("a print of the configuration holds the token:\n%s", printed)
	}
	if !strings.Contains(printed, `token=set`) || !strings.Contains(printed, `"token":"set"`) {
		t.Fatalf("slog's form of the token is not set:\n%s", printed)
	}

	// A child's environment, as every launch builds it (pty.BuildEnv,
	// which proc and gitcli use too), never holds it.
	for _, kv := range pty.BuildEnv(os.Environ(), []string{"CONDUCTOR_GITHUB_TOKEN"}, map[string]string{"CONDUCTOR_GITHUB_TOKEN": planted}, nil) {
		if strings.Contains(kv, planted) {
			t.Fatalf("a child's environment holds %s", kv)
		}
	}

	// No file holds it: a config file naming it is refused.
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"gitHubToken": "x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("a config file naming the token: %v", err)
	}

	// Its form and its host are checked, and the errors say only that.
	for _, c := range []struct{ tok, host, want string }{
		{"has space", "", "visible ASCII"},
		{strings.Repeat("a", MaxTrackerToken+1), "", "visible ASCII"},
		{planted, "127.0.0.1", "CONDUCTOR_GITHUB_HOST"},
		{planted, "GitHub.com", "CONDUCTOR_GITHUB_HOST"},
		// The two variables swapped: the host variable holding a made-up
		// token (and a 40-hex one, a single label): refused, and the
		// start's error holds nothing of the value.
		{"github.com", planted, "CONDUCTOR_GITHUB_HOST"},
		{"github.com", "d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f7a8b9c0d", "CONDUCTOR_GITHUB_HOST"},
	} {
		t.Setenv("CONDUCTOR_GITHUB_TOKEN", c.tok)
		t.Setenv("CONDUCTOR_GITHUB_HOST", c.host)
		_, err := Load("")
		if err == nil || !strings.Contains(err.Error(), c.want) || (c.tok != "github.com" && strings.Contains(err.Error(), c.tok)) || (c.host != "" && strings.Contains(err.Error(), c.host)) || strings.Contains(err.Error(), planted[4:16]) {
			t.Errorf("token %.12q host %.12q: %v", c.tok, c.host, err)
		}
	}
	t.Setenv("CONDUCTOR_GITHUB_TOKEN", planted)
	t.Setenv("CONDUCTOR_GITHUB_HOST", "ghe.example.com")
	if cfg, err := Load(""); err != nil || cfg.GitHubHost != "ghe.example.com" {
		t.Fatalf("an Enterprise Server host: %v", err)
	}
	t.Setenv("CONDUCTOR_GITHUB_HOST", "127.0.0.1:9")
	t.Setenv("CONDUCTOR_TRACKER_LOOPBACK", "1")
	if cfg, err := Load(""); err != nil || !cfg.TrackerLoopback {
		t.Fatalf("a loopback host in loopback mode: %v", err)
	}
	t.Setenv("CONDUCTOR_GITHUB_TOKEN", "")
	t.Setenv("CONDUCTOR_GITHUB_HOST", "")
	if cfg, err := Load(""); err != nil || cfg.GitHubToken.Set() || cfg.GitHubToken.String() != "unset" {
		t.Fatalf("no token: %v %v", cfg.GitHubToken, err)
	}
}
