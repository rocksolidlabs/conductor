package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
)

// Made-up secrets, of the forms the desktop app mints.
const (
	stdinToken = "PLANTEDstdinTOKEN0123456789abcdefWXYZ0123456789ab"
	stdinKey   = "PLANTEDstdinKEY_0123456789-abcdefWXYZ0123456789ab"
)

// secretsLine is the line the desktop app writes first on its server's
// stdin.
func secretsLine(token, key string) string {
	b, _ := json.Marshal(map[string]string{"workbenchToken": token, "desktopKey": key})
	return string(b) + "\n"
}

// waitHandshake reads the --print-listen line out, failing on the start's
// deadline or when serve returns first.
func waitHandshake(t *testing.T, sv *serving, out, logs *syncBuffer) handshake {
	t.Helper()
	deadline, _ := startDeadline(t)
	for !strings.Contains(out.String(), "\n") {
		if err, ok := sv.returned(); ok {
			t.Fatalf("serve returned: %v\n%s", err, logs.String())
		}
		select {
		case <-deadline:
			t.Fatalf("no handshake:\n%s", logs.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	var h handshake
	if err := json.Unmarshal([]byte(strings.SplitN(out.String(), "\n", 2)[0]), &h); err != nil {
		t.Fatalf("handshake %q: %v", out.String(), err)
	}
	return h
}

// whoami asks GET /api/whoami of the server at listen with token.
func whoami(t *testing.T, listen, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+listen+"/api/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// With --secrets-stdin the app's token and key come on the first line of
// stdin: the token is the server's (one in the environment is not), the
// key lets the app allow a folder (whoami folders app), the rest of stdin
// is still watched for its close, and neither the handshake nor the log
// holds either.
func TestServeTakesItsSecretsFromStdin(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", "an-environment-token-0123456789abcdef")
	dir := t.TempDir()
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, filepath.Join(dir, "state")))
	t.Cleanup(agents.ForgetBinary())
	in, feed := io.Pipe()
	var logs, out syncBuffer
	sv := startServing(t, []string{"--listen", "127.0.0.1:0", "--config", cfg, "--print-listen", "--secrets-stdin", "--exit-on-stdin-close"}, in, &out, &logs)
	go func() { _, _ = io.WriteString(feed, secretsLine(stdinToken, stdinKey)) }()
	h := waitHandshake(t, sv, &out, &logs)
	if status, me := whoami(t, h.Listen, stdinToken); status != http.StatusOK || me["folders"] != "app" {
		t.Fatalf("whoami with the token from stdin: %d %v", status, me)
	}
	if status, _ := whoami(t, h.Listen, "an-environment-token-0123456789abcdef"); status != http.StatusUnauthorized {
		t.Fatalf("the environment's token: %d", status)
	}
	// The rest of stdin is still the server's life line.
	feed.Close()
	deadline, _ := startDeadline(t)
	for {
		if _, ok := sv.returned(); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("serve did not stop when stdin closed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	for _, v := range []string{stdinToken, stdinKey} {
		if strings.Contains(out.String(), v) || strings.Contains(logs.String(), v) {
			t.Fatalf("a secret is in the handshake or the log:\n%s\n%s", out.String(), logs.String())
		}
	}
}

// A line with the token alone leaves the server with no key: no folder is
// allowed while it runs (whoami folders config).
func TestServeWithoutAKeyOnStdinAllowsNoFolder(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, filepath.Join(dir, "state")))
	t.Cleanup(agents.ForgetBinary())
	var logs, out syncBuffer
	sv := startServing(t, []string{"--listen", "127.0.0.1:0", "--config", cfg, "--print-listen", "--secrets-stdin"}, strings.NewReader(`{"workbenchToken":"`+stdinToken+`"}`+"\n"), &out, &logs)
	h := waitHandshake(t, sv, &out, &logs)
	if status, me := whoami(t, h.Listen, stdinToken); status != http.StatusOK || me["folders"] != "config" {
		t.Fatalf("whoami: %d %v", status, me)
	}
}

// The line is one JSON object with a token and a key of its form, bounded
// and waited for; anything else stops the start in words that hold none of
// it, and --token-env beside it is refused.
func TestServeRefusesALineOfSecretsItCannotTake(t *testing.T) {
	clearConductorEnv(t)
	old := secretsWait
	secretsWait = 2 * time.Second
	t.Cleanup(func() { secretsWait = old })
	never, _ := io.Pipe()
	for _, c := range []struct {
		name  string
		stdin io.Reader
		args  []string
		want  string
	}{
		{"a short key", strings.NewReader(secretsLine(stdinToken, "PLANTEDshort")), nil, "desktop key must be 32 to 256"},
		{"a key with a space", strings.NewReader(secretsLine(stdinToken, "PLANTED key 0123456789abcdefghijklmnop")), nil, "desktop key must be 32 to 256"},
		{"no token", strings.NewReader(`{"desktopKey":"` + stdinKey + `"}` + "\n"), nil, "holds no workbench token"},
		{"an unknown field", strings.NewReader(`{"workbenchToken":"` + stdinToken + `","PLANTEDextra":1}` + "\n"), nil, "not one JSON object"},
		{"two objects", strings.NewReader(`{"workbenchToken":"` + stdinToken + `"}{"workbenchToken":"PLANTEDx"}` + "\n"), nil, "not one JSON object"},
		{"a line too long", strings.NewReader(`{"workbenchToken":"PLANTED` + strings.Repeat("a", maxSecretsLine) + `"}` + "\n"), nil, "longer than 4096"},
		{"stdin ended first", strings.NewReader(`{"workbenchToken":"PLANTED`), nil, "stdin ended before"},
		{"no line in time", never, nil, "no line of secrets on stdin within"},
		{"with --token-env", strings.NewReader(secretsLine(stdinToken, stdinKey)), []string{"--token-env", "X"}, "give one"},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		var logs syncBuffer
		code, err := runServe(ctx, append([]string{"--listen", "127.0.0.1:0", "--secrets-stdin"}, c.args...), c.stdin, io.Discard, &logs)
		cancel()
		if code == 0 || err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "PLANTED") || strings.Contains(logs.String(), "PLANTED") {
			t.Errorf("%s: exit %d: %v\n%s", c.name, code, err, logs.String())
		}
	}
}

// No environment variable gives the server a desktop key: a server started
// by hand, with no app, takes its token as before and allows no folder.
func TestServeTakesNoKeyFromItsEnvironment(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", stdinToken)
	t.Setenv("CONDUCTOR_DESKTOP_KEY", stdinKey)
	dir := t.TempDir()
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, filepath.Join(dir, "state")))
	t.Cleanup(agents.ForgetBinary())
	var logs, out syncBuffer
	sv := startServing(t, []string{"--listen", "127.0.0.1:0", "--config", cfg, "--print-listen"}, strings.NewReader(""), &out, &logs)
	h := waitHandshake(t, sv, &out, &logs)
	if status, me := whoami(t, h.Listen, stdinToken); status != http.StatusOK || me["folders"] != "config" {
		t.Fatalf("whoami: %d %v", status, me)
	}
}

// readLine reads one line of r, for a child's handshake.
func readLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("a line: %q %v", line, err)
	}
	return line
}

// A server whose stdin has ended before it would print its handshake
// prints none, and shuts down: the end of stdin is watched before the
// handshake, which a parent that told it to stop while it started never
// gets. The seam holds the handshake until the end has been seen (the old
// order never sees it there, and prints).
func TestServePrintsNoHandshakeOnceStdinHasEnded(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, filepath.Join(dir, "state")))
	t.Cleanup(agents.ForgetBinary())
	deadline, wait := startDeadline(t)
	var seen bool
	beforeHandshake = func(ended <-chan struct{}) {
		select {
		case <-ended:
			seen = true
		case <-deadline:
		}
	}
	t.Cleanup(func() { beforeHandshake = nil })
	var logs, out syncBuffer
	ctx, cancel := context.WithTimeout(t.Context(), wait+30*time.Second)
	defer cancel()
	code, err := runServe(ctx, []string{"--listen", "127.0.0.1:0", "--config", cfg, "--print-listen", "--exit-on-stdin-close"}, strings.NewReader(""), &out, &logs)
	if err != nil || code != 0 {
		t.Fatalf("serve: exit %d: %v\n%s", code, err, logs.String())
	}
	if !seen {
		t.Fatalf("the end of stdin was not seen before the handshake:\n%s", logs.String())
	}
	if out.String() != "" {
		t.Fatalf("a handshake was printed after stdin ended: %q", out.String())
	}
}

// A switchyard takes no secrets on stdin: conductor switchyard has no such
// flag, and serve refuses it beside --switchyard.
func TestASwitchyardTakesNoSecretsOnStdin(t *testing.T) {
	clearConductorEnv(t)
	var logs syncBuffer
	if code, err := runSwitchyard(t.Context(), []string{"--secrets-stdin"}, strings.NewReader(""), io.Discard, &logs); code != 2 || err == nil {
		t.Fatalf("conductor switchyard --secrets-stdin: exit %d: %v", code, err)
	}
	code, err := runServe(t.Context(), []string{"--switchyard", "--secrets-stdin"}, strings.NewReader(secretsLine(stdinToken, stdinKey)), io.Discard, &logs)
	if code != 2 || err == nil || !strings.Contains(err.Error(), "a switchyard takes no secrets on stdin") {
		t.Fatalf("serve --switchyard --secrets-stdin: exit %d: %v", code, err)
	}
	// Turned on by the environment, or by the configuration file: refused all the same, nothing bound.
	dir := t.TempDir()
	file := filepath.Join(dir, "conductor.json")
	if err := os.WriteFile(file, []byte(`{"switchyard":{"enabled":true},"dataDir":`+strconv.Quote(filepath.Join(dir, "data"))+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"CONDUCTOR_SWITCHYARD=1", map[string]string{"CONDUCTOR_SWITCHYARD": "1", "CONDUCTOR_DATA_DIR": filepath.Join(dir, "data")}, []string{"--secrets-stdin", "--listen", "127.0.0.1:0"}},
		{"switchyard.enabled in the file", nil, []string{"--secrets-stdin", "--config", file, "--listen", "127.0.0.1:0"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			var out syncBuffer
			// The refusal comes before anything is bound; the bound ends a server that would run instead.
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			code, err := runServe(ctx, c.args, strings.NewReader(secretsLine(stdinToken, stdinKey)), &out, &logs)
			if code != 2 || err == nil || !strings.Contains(err.Error(), "a switchyard takes no secrets on stdin") {
				t.Fatalf("exit %d: %v", code, err)
			}
			if strings.Contains(out.String(), "listen") {
				t.Fatalf("a handshake was printed: %s", out.String())
			}
		})
	}
}
