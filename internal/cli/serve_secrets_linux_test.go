package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestServeSecretsChild is the server a test starts as a process of its
// own, as the desktop app starts it: its arguments the app's, its
// configuration the file the variable names. It does nothing in a run of
// the package.
func TestServeSecretsChild(t *testing.T) {
	cfg := os.Getenv("CONDUCTOR_TEST_SERVE_CHILD")
	if cfg == "" {
		t.Skip("run by TestAServerProcessTakesItsSecretsFromStdin")
	}
	code, err := runServe(context.Background(), []string{"--listen", "127.0.0.1:0", "--config", cfg, "--print-listen", "--exit-on-stdin-close", "--secrets-stdin"}, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

// A server started as a process of its own takes its token and key from
// the first line of its stdin, across the process boundary, as the desktop
// app starts it: its handshake names its own process, it answers with the
// token from the line and allows a folder with the key, and its log holds
// neither. (That the app hands the server no secret in its environment or
// its arguments is the app's own tests' to prove: desktop/test, and
// desktop/e2e/allow.spec.ts, which reads the real server's.)
func TestAServerProcessTakesItsSecretsFromStdin(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, filepath.Join(dir, "state")))
	cmd := exec.Command(os.Args[0], "-test.run=^TestServeSecretsChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CONDUCTOR_TEST_SERVE_CHILD="+cfg)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		_ = cmd.Wait()
	})
	if _, err := io.WriteString(in, secretsLine(stdinToken, stdinKey)); err != nil {
		t.Fatal(err)
	}
	var h handshake
	if err := json.Unmarshal([]byte(readLine(t, bufio.NewReader(stdout))), &h); err != nil {
		t.Fatalf("handshake: %v\n%s", err, logs.String())
	}
	if h.PID != cmd.Process.Pid {
		t.Fatalf("the handshake's pid %d is not the child's %d", h.PID, cmd.Process.Pid)
	}
	if status, me := whoami(t, h.Listen, stdinToken); status != http.StatusOK || me["folders"] != "app" {
		t.Fatalf("whoami: %d %v\n%s", status, me, logs.String())
	}
	in.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the child: %v\n%s", err, logs.String())
	}
	if strings.Contains(logs.String(), stdinToken) || strings.Contains(logs.String(), stdinKey) {
		t.Fatal("a secret is in the server's log")
	}
}
