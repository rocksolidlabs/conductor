package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Brakeman's folder and the agent's trust (#705). An agent keeps what a
// person trusts per folder and per account, and Claude Code keeps none for
// the home folder: there its trust question's answer holds for that one
// session, so a turn started in the home folder asked again every time.

// bkTrustScript is an agent that asks for trust as Claude Code does: until
// a person has trusted the folder it starts in under its account, it shows
// its trust question and reads the answer; what it trusts it keeps in its
// account's folder ($CLAUDE_CONFIG_DIR/trusted), one line a folder, and
// never the home folder (the %s), whose answer holds for its session
// alone. The answer it read is kept in answer-<request>.txt; then it goes
// on as bkScript does, which follows it (its env-<request>.txt written
// after).
const bkTrustScript = `d=$(pwd -P); f="$CLAUDE_CONFIG_DIR/trusted"; ` +
	`if ! grep -qxF "$d" "$f" 2>/dev/null; then ` +
	`printf ' Accessing workspace:\r\n\r\n %%s\r\n\r\n Is this a project you created or one you trust?\r\n\r\n ❯ 1. No, exit\r\n   2. Yes, I trust this folder\r\n' "$d"; ` +
	`IFS= read -r a; printf '%%s' "$a" > "answer-$CONDUCTOR_BRAKEMAN.txt"; ` +
	`case "$a" in *'[B') [ "$d" = '%s' ] || echo "$d" >> "$f";; *) exit 1;; esac; fi; `

// trustEnv is a Brakeman test server as the desktop app makes one: the
// home folder (home) the allowed root and the server's data directory
// under it, its catalog holding bktrust (bkTrustScript, with Claude
// Code's own trust question and answers), the desk knowing home as the
// home folder.
func trustEnv(t *testing.T) (b *bkEnv, home string) {
	t.Helper()
	home = realRoot(t, t.TempDir())
	claude, ok := catalog.Default().Get("claude")
	if !ok || claude.TrustPrompt == "" || len(claude.TrustAnswers) != 2 {
		t.Fatalf("Claude Code's built-in trust question: %+v", claude)
	}
	agent := catalog.Agent{
		ID: "bktrust", Name: "bktrust", Command: []string{"/bin/sh", "-c", fmt.Sprintf(bkTrustScript, home) + bkScript},
		Adapter: "claude", AllowArgs: true, TrustPrompt: claude.TrustPrompt, TrustAnswers: claude.TrustAnswers,
	}
	e := newTestEnvAgents(t, func(c *config.Config) {
		c.AllowedRoots = append(c.AllowedRoots, home)
		c.DataDir = filepath.Join(home, ".local", "share", "conductor", "data")
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), append(bkAgents(), agent))
	b = bkEnvOf(t, e)
	b.desk.homeDir.Store(&home)
	return b, home
}

// trustQuestion waits for the turn's session to show the agent's trust
// question with its answers, and answers it.
func trustQuestion(t *testing.T, l *session.Local) session.Attention {
	t.Helper()
	var a session.Attention
	if !waitFor(30*time.Second, func() bool {
		a = l.Info().Attention
		return a.State == session.AttentionNeedsInput && a.Source == session.SourceTrust && len(a.Options) == 2
	}) {
		t.Fatalf("the trust question never showed: %+v", a)
	}
	return a
}

// turnFile waits for the turn's agent to have written name in folder, and
// answers it.
func turnFile(t *testing.T, folder, name string) string {
	t.Helper()
	var got []byte
	if !waitFor(30*time.Second, func() bool {
		var err error
		got, err = os.ReadFile(filepath.Join(folder, name))
		return err == nil && len(got) > 0
	}) {
		t.Fatalf("%s was never written in %s", name, folder)
	}
	return string(got)
}

// TestATrustedFolderIsNotAskedAgain: with Brakeman's folder the home
// folder, a turn runs in the desk's own folder for its account, the
// request's stamp naming it; the first turn shows the agent's trust
// question under Needs you and nothing is typed into it until a person
// answers; the next turn, in the same folder, starts without the question.
func TestATrustedFolderIsNotAskedAgain(t *testing.T) {
	t.Parallel()
	b, home := trustEnv(t)
	account := realRoot(t, t.TempDir())
	b.enable(t, "bktrust", func(c map[string]any) { c["cwd"], c["account"] = home, account })
	want, err := b.desk.st.Folder(account)
	if err != nil {
		t.Fatal(err)
	}
	if want, err = filepath.EvalSymlinks(want); err != nil {
		t.Fatal(err)
	}

	first := b.briefing(t)
	_, _, l := b.turnSession(t, first)
	if got := b.request(t, first)["agent"].(map[string]any)["folder"]; got != want {
		t.Fatalf("the first turn's folder is %v, want %s (not the home folder %s)", got, want, home)
	}
	if info := l.Info(); info.Cwd != want {
		t.Fatalf("the first turn's session runs in %s, want %s", info.Cwd, want)
	}
	// It reads the home folder still, as it did when it ran there: Claude
	// Code's --add-dir makes it one of its working folders.
	if argv := l.Info().Command; !slices.Contains(argv, "--add-dir="+home) {
		t.Fatalf("the turn does not read the home folder: %q", argv)
	}
	a := trustQuestion(t, l)
	if _, err := os.Stat(filepath.Join(want, "answer-"+first+".txt")); !os.IsNotExist(err) {
		t.Fatalf("the agent read an answer before anyone gave one: %v", err)
	}
	yes := ""
	for _, o := range a.Options {
		if strings.HasPrefix(o.Label, "Yes") {
			yes = o.Input
		}
	}
	if err := l.AnswerAs(context.Background(), a.ID, yes, session.Actor{Name: "Nate"}); err != nil {
		t.Fatalf("the person's answer: %v", err)
	}
	turnFile(t, want, "env-"+first+".txt")
	if got := turnFile(t, want, "answer-"+first+".txt"); got != "\x1b[B" {
		t.Fatalf("the trust question took %q, not the person's answer alone", got)
	}
	b.expect(t, "POST", "/api/brakeman/requests/"+first+"/cancel", map[string]any{"opId": "c-1"}, http.StatusOK, "")
	b.slotFree(t)

	next := b.briefing(t)
	_, _, l = b.turnSession(t, next)
	if got := b.request(t, next)["agent"].(map[string]any)["folder"]; got != want {
		t.Fatalf("the next turn's folder is %v, want the first's %s", got, want)
	}
	turnFile(t, want, "env-"+next+".txt")
	if _, err := os.Stat(filepath.Join(want, "answer-"+next+".txt")); !os.IsNotExist(err) {
		t.Fatalf("the next turn asked for trust again: %v", err)
	}
	if a := l.Info().Attention; a.Source == session.SourceTrust {
		t.Fatalf("the next turn shows a trust question: %+v", a)
	}
}

// TestAnAgentWithNoReadSwitchRunsInItsFolder: an agent with no switch to
// read a folder besides its own (Codex: its --add-dir makes one writable)
// runs in the configured folder, the home folder included, reading
// nothing else through its argv; its trust question is the person's.
func TestAnAgentWithNoReadSwitchRunsInItsFolder(t *testing.T) {
	t.Parallel()
	b, home := trustEnv(t)
	b.enable(t, "bkcodex", func(c map[string]any) { c["cwd"] = home })
	id := b.briefing(t)
	_, _, l := b.turnSession(t, id)
	if got := b.request(t, id)["agent"].(map[string]any)["folder"]; got != home || l.Info().Cwd != home {
		t.Fatalf("the turn runs in %s, stamped %v, want the home folder %s", l.Info().Cwd, got, home)
	}
	if argv := l.Info().Command; slices.ContainsFunc(argv, func(a string) bool { return strings.HasPrefix(a, "--add-dir") }) {
		t.Fatalf("the turn was handed a folder through Codex's --add-dir, which makes it writable: %q", argv)
	}
	folders, _ := b.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, "")["folders"].(map[string]any)
	if folders["turn"] != home || fmt.Sprint(folders["reads"]) != "[]" {
		t.Fatalf("the state's folders %v, want %s reading nothing besides", folders, home)
	}
}

// TestATurnRunsInItsConfiguredFolder: a folder that is not the home
// folder is the one turns run in, as configured, its stamp naming it; and
// in the home folder, when the desk's own folder lies outside the allowed
// roots (a data directory placed elsewhere), the turn still runs, there.
func TestATurnRunsInItsConfiguredFolder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		home func(b *bkEnv) string
	}{
		{"another folder", func(*bkEnv) string { return realRoot(t, t.TempDir()) }},
		{"the home folder outside the data directory's roots", func(b *bkEnv) string { return b.root }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := brakemanEnv(t, nil)
			home := tc.home(b)
			b.desk.homeDir.Store(&home)
			b.enable(t, "bk", nil)
			id := b.briefing(t)
			_, _, l := b.turnSession(t, id)
			if got := b.request(t, id)["agent"].(map[string]any)["folder"]; got != b.root || l.Info().Cwd != b.root {
				t.Fatalf("the turn runs in %s, stamped %v, want %s", l.Info().Cwd, got, b.root)
			}
			turnFile(t, b.root, "env-"+id+".txt")
		})
	}
}
