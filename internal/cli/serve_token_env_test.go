package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
)

// serve --token-env NAME makes the named variable's value the workbench
// token: a tree's own Conductor takes the tree's secret so, since an argv
// cannot carry it. An unset or empty variable, or a name that is not a
// variable's (the empty one included), is refused before anything is
// written or bound, never falling back to a configured or a generated
// token. The value is printed, logged and kept nowhere: not in the
// --print-listen line, the log, the terminal, an error, or any file of the
// data directory; no error repeats what the flag was given, a token given
// by mistake as the name included; and it leaves the server's own
// environment, so nothing the server starts inherits it.
func TestServeTakesItsTokenFromTheNamedVariable(t *testing.T) {
	const name = "CONDUCTOR_TEST_TREE_SECRET"
	secret := "tree-secret-" + strings.Repeat("5e", 32)
	other := "configured-token-" + strings.Repeat("c0", 24)

	// A token given by mistake where its variable's name belongs: letters and digits, the form of a name.
	tokenAsName := "a" + strings.Repeat("5e", 31)

	t.Run("refused when unset, empty or not a name", func(t *testing.T) {
		for _, c := range []struct {
			why, flag string
			// env is the variable the case sets to set, or unsets when set is nil.
			env   string
			set   *string
			words string
		}{
			{"unset", name, name, nil, "--token-env: the variable it names is not set"},
			{"empty", name, name, new(string), "--token-env: the variable it names is empty"},
			{"the token given as the name", tokenAsName, tokenAsName, nil, "--token-env: the variable it names is not set"},
			{"the token given as the name, set empty", tokenAsName, tokenAsName, new(string), "--token-env: the variable it names is empty"},
			{"an empty name", "", name, &secret, "what it was given is not one"},
			{"not a name", name + "=" + secret, name, &secret, "what it was given is not one"},
			{"a name with a dash", "TREE-SECRET", name, &secret, "what it was given is not one"},
			{"a name that starts with a digit", "1SECRET", name, &secret, "what it was given is not one"},
		} {
			t.Run(c.why, func(t *testing.T) {
				clearConductorEnv(t)
				// A configured token is there to fall back to: the flag refuses rather than take it.
				t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", other)
				if c.set != nil {
					t.Setenv(c.env, *c.set)
				} else {
					t.Setenv(c.env, "")
					if err := os.Unsetenv(c.env); err != nil {
						t.Fatal(err)
					}
				}
				dir := t.TempDir()
				data := filepath.Join(dir, "state")
				cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, data))
				t.Cleanup(agents.ForgetBinary())
				var out, logs bytes.Buffer
				// A refusal returns at once; the bound only ends a server that, wrongly, went on to serve.
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				code, err := runServe(ctx, []string{"--listen", "127.0.0.1:0", "--config", cfg, "--print-listen", "--token-env", c.flag}, strings.NewReader(""), &out, &logs)
				if code != 2 || err == nil || !strings.Contains(err.Error(), c.words) {
					t.Fatalf("serve = %d, %v; want 2 and an error saying %q\n%s", code, err, c.words, logs.String())
				}
				for what, text := range map[string]string{"the error": err.Error(), "the log": logs.String(), "stdout": out.String()} {
					if strings.Contains(text, secret) || strings.Contains(text, other) || strings.Contains(text, tokenAsName) {
						t.Fatalf("%s holds a token: %q", what, text)
					}
				}
				if out.Len() != 0 {
					t.Fatalf("a refused server printed %q", out.String())
				}
				// Refused before the data directory was made, so before any token file.
				if _, err := os.Stat(data); !os.IsNotExist(err) {
					t.Fatalf("the data directory exists after the refusal: %v", err)
				}
			})
		}
	})

	t.Run("the token is the variable's, and goes nowhere", func(t *testing.T) {
		clearConductorEnv(t)
		t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", other)
		t.Setenv(name, secret)
		dir := t.TempDir()
		data := filepath.Join(dir, "state")
		cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, data))
		t.Cleanup(agents.ForgetBinary())
		// A token file an earlier server with a generated token left: this one keeps none, and drops it.
		if err := os.MkdirAll(data, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, workbenchTokenFile), []byte(strings.Repeat("0", 64)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var tty syncBuffer
		old := terminalOf
		t.Cleanup(func() { terminalOf = old })
		terminalOf = func(io.Writer) io.Writer { return &tty }
		var logs, out syncBuffer
		sv := startServing(t, []string{"--listen", "127.0.0.1:0", "--config", cfg, "--print-listen", "--token-env", name}, strings.NewReader(""), &out, &logs)
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
		if h.WorkbenchToken != "" {
			t.Fatal("the --print-listen line carries the workbench token")
		}
		if v, ok := os.LookupEnv(name); ok {
			t.Fatalf("the variable is still in the server's environment (%d bytes)", len(v))
		}
		// The variable's value opens the workbench; the configured token it overrides does not.
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		status := func(token string) int {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+h.Listen+"/api/history/sessions", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		if got := status(secret); got != http.StatusOK {
			t.Fatalf("the variable's token: %d", got)
		}
		if got := status(other); got != http.StatusUnauthorized {
			t.Fatalf("the configured token it overrides: %d", got)
		}
		if _, err := os.Stat(filepath.Join(data, workbenchTokenFile)); !os.IsNotExist(err) {
			t.Fatalf("a workbench token file is in the data directory: %v", err)
		}
		if err := sv.stop(); err != nil {
			t.Fatalf("serve: %v\n%s", err, logs.String())
		}
		if lines := logLines(logs.String(), "the workbench token is the value of the variable --token-env names"); len(lines) != 1 {
			t.Fatalf("no line naming the variable:\n%s", logs.String())
		}
		for what, text := range map[string]string{"the log": logs.String(), "stdout": out.String(), "the terminal": tty.String()} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s holds the token:\n%s", what, text)
			}
		}
		if strings.Contains(tty.String(), "Workbench token") {
			t.Fatalf("the terminal was told of a token: %q", tty.String())
		}
		// No file the server wrote holds the token.
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("%s holds the token", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
