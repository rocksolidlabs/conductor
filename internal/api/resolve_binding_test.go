package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// The resolved launch's binding and the kept capacity view beside it
// (resolve.go): each member's account and model are pinned by the digest;
// the view is outside it, read from what the server keeps.

// syncBuffer is a log the server writes from many goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// boundEnv is a capacity server over the stub agents, its log kept, with a
// repository, a Dispatch project over it, an account of claude whose
// folder is the test's, and a crew of two tasks: lead on that account
// with the model opus, and writer on claude-p's default.
type boundEnv struct {
	*testEnv
	claude        *fakeUsage
	log           *syncBuffer
	crew, account string
	folder        string
}

func newBoundEnv(t *testing.T) *boundEnv {
	t.Helper()
	t.Setenv("HOME", realRoot(t, t.TempDir()))
	logs := &syncBuffer{}
	claude := newFakeUsage("claude")
	e := capEnv(t, nil, slog.New(slog.NewTextHandler(logs, nil)), claude, newFakeUsage("codex"), bindAgents(t.TempDir(), realRoot(t, t.TempDir())))
	e.gitRepo(t)
	e.newDispatchProject(t, "CND", map[string]any{"cwd": e.root})
	b := &boundEnv{testEnv: e, claude: claude, log: logs}
	be := &bindEnv{testEnv: e}
	b.account, b.folder = be.account(t, "claude", "Work account")
	b.crew = e.sendCrew("POST", "/api/crews", map[string]any{"name": "Bound", "goal": "g", "cwd": e.root, "where": "server", "isolation": "worktree",
		"members": []any{
			map[string]any{"name": "lead", "agentId": "claude-p", "prompt": "", "account": b.account, "model": "opus", "start": map[string]any{"when": "task"}},
			map[string]any{"name": "writer", "agentId": "claude-p", "prompt": "", "start": map[string]any{"when": "task"}},
		},
		"tasks": []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it."},
			map[string]any{"id": "docs", "member": "writer", "kind": "work", "instructions": "Write it.", "needs": []any{"build"}}}}, http.StatusCreated)["id"].(string)
	return b
}

// A resolve names each member's account by its id and where its login
// comes from, and its model: the crew's account for lead, with its model;
// the adapter's default, the tool's own login, for writer.
func TestResolveLaunchNamesTheBoundAccounts(t *testing.T) {
	b := newBoundEnv(t)
	rl := resolve(t, b.testEnv, b.crew, "")
	lead, writer := rl.Members[0], rl.Members[1]
	if lead.Account != b.account || lead.AccountSource != ext.AccountAccount || lead.Model != "opus" {
		t.Fatalf("lead: %+v", lead)
	}
	if writer.Account != "default" || writer.AccountSource != ext.AccountOwn || writer.Model != "" {
		t.Fatalf("writer: %+v", writer)
	}
}

// A changed default account changes the digest, and a launch held to the
// earlier digest is refused launch_changed; a removed account refuses the
// resolve and the launch both, nothing launched.
func TestBindingChangeChangesTheDigest(t *testing.T) {
	b := newBoundEnv(t)
	be := &bindEnv{testEnv: b.testEnv}
	rl := resolve(t, b.testEnv, b.crew, "")
	other, _ := be.account(t, "claude", "Other account")
	be.setDefault(t, "claude", other)
	moved := resolve(t, b.testEnv, b.crew, "")
	if moved.Digest == rl.Digest || moved.Members[1].Account != other {
		t.Fatalf("the default moved, the resolve did not: %+v", moved.Members)
	}
	h := testHost(b.srv, t.Context(), "example")
	if _, err := h.Launch(t.Context(), launchHeldTo(b.crew, "op-1", rl.Digest)); ext.RefusalCode(err) != ext.RefusalLaunchChanged {
		t.Fatalf("a launch held to the earlier default: %v", err)
	}
	// Removed at once: no resolve held a reservation of it.
	if status, out := be.removeAccount(t, b.account); status != http.StatusNoContent {
		t.Fatalf("remove the crew's account: %d %v", status, out)
	}
	if _, err := h.ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: b.crew, Project: "CND"}); !errorsIsRefused(err) {
		t.Fatalf("a resolve naming a removed account: %v", err)
	}
	if _, err := h.Launch(t.Context(), launchHeldTo(b.crew, "op-2", moved.Digest)); !errorsIsRefused(err) {
		t.Fatalf("a launch naming a removed account: %v", err)
	}
	if n := ledgerRuns(t, b.testEnv); n != 0 {
		t.Fatalf("%d runs", n)
	}
}

// errorsIsRefused reports whether err is a refusal with a code.
func errorsIsRefused(err error) bool { return ext.RefusalCode(err) != "" }

// The kept usage, its counts and its freshness, and time passing, change
// the view and never the digest; a restart's empty cache gives the same
// digest.
func TestCapacityChangesLeaveTheDigest(t *testing.T) {
	b := newBoundEnv(t)
	rl := resolve(t, b.testEnv, b.crew, "")
	readNow(t, b.testEnv, "claude-p", b.account, false)
	if !waitFor(testWait, func() bool {
		v := resolve(t, b.testEnv, b.crew, "")
		return len(v.Capacity) > 0 && v.Capacity[0].Fresh != ext.CapacityNone
	}) {
		t.Fatal("the resolve shows no kept reading after a read")
	}
	read := resolve(t, b.testEnv, b.crew, "")
	if read.Digest != rl.Digest || len(read.Capacity) == 0 {
		t.Fatalf("a reading changed the digest, or no view: %+v", read.Capacity)
	}
	shutdown(b.testEnv)
	again := resolve(t, b.testEnv.restart(), b.crew, "")
	if again.Digest != rl.Digest {
		t.Fatalf("after a restart: %s, was %s", again.Digest, rl.Digest)
	}
}

// A resolve reads no usage: the reader is asked nothing, however many
// resolves are made.
func TestResolveLaunchReadsNoUsage(t *testing.T) {
	b := newBoundEnv(t)
	calls, _ := b.claude.count()
	for range 5 {
		resolve(t, b.testEnv, b.crew, "")
	}
	if after, _ := b.claude.count(); after != calls {
		t.Fatalf("five resolves read usage %d times", after-calls)
	}
}

// The resolved launch, as encoded, holds no account's folder and no
// login's fingerprint; the server's log holds no label and no folder.
func TestResolvedLaunchCarriesNoFolderOrLogin(t *testing.T) {
	b := newBoundEnv(t)
	readNow(t, b.testEnv, "claude-p", b.account, false)
	rl := resolve(t, b.testEnv, b.crew, "")
	raw, err := json.Marshal(rl)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{b.folder, "D=[", "fingerprint"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("the resolved launch carries %q: %s", leak, raw)
		}
	}
	if strings.Contains(b.log.String(), "Work account") || strings.Contains(b.log.String(), b.folder) {
		t.Fatalf("the log names the account:\n%s", b.log.String())
	}
}
