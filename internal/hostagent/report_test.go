package hostagent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// What a host reports of its session reaches the session's record on the
// owner's server (docs/plans/history.md, PR-12).

// recordingServer is startServer's server with a data directory, so it
// keeps session records; ended before its directory goes.
func recordingServer(t *testing.T) *httptest.Server {
	t.Helper()
	data := t.TempDir()
	cfg := config.Defaults()
	cfg.WorkbenchToken, cfg.HostTokens, cfg.AllowedRoots, cfg.Dev, cfg.DataDir = adminToken, []string{hostToken}, []string{t.TempDir()}, true, data
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, catalog.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, st)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		hs.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	return hs
}

// endedRecord is the record of session id on the server at base once its
// end is in it.
func endedRecord(t *testing.T, base, id string) journal.Record {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		req, _ := http.NewRequest("GET", base+"/api/history/sessions/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		var got struct{ Record journal.Record }
		if resp, err := http.DefaultClient.Do(req); err == nil {
			err = json.NewDecoder(resp.Body).Decode(&got)
			resp.Body.Close()
			if err == nil && resp.StatusCode == http.StatusOK && !got.Record.EndedAt.IsZero() {
				return got.Record
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the record of %s has no end", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitAtStart is the native Runtime with a commit made in the folder as
// the process is started: what the host read before the start is the
// commit before it.
type commitAtStart struct {
	ext.Runtime
	t *testing.T
}

func (c commitAtStart) StartTerminal(ctx context.Context, s ext.TerminalSpec) (ext.Terminal, error) {
	git(c.t, s.Dir, "commit", "-q", "--allow-empty", "-m", "the agent's first commit")
	return c.Runtime.StartTerminal(ctx, s)
}

// A native host reads its folder's repository and commit, and its agent's
// version, before its process starts, and its commit again once it ended:
// the record holds them as the host reported them, its start the commit
// before the one made at the start, its end that one.
func TestTheHostReadsItsLaunchRevisionBeforeItStarts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	srv := recordingServer(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "before")
	before := git(t, dir, "rev-parse", "HEAD")
	want, err := gitcli.RevisionOf(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, Options{ServerURL: srv.URL, Token: hostToken, RelayOnly: true, Name: "native", Argv: []string{"/bin/sh", "-c", "exit 0"}, Dir: dir,
		Runtime: commitAtStart{local.Native(), t}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	after := git(t, dir, "rev-parse", "HEAD")
	r := endedRecord(t, srv.URL, res.SessionID)
	if r.Where != "native" || r.Repo != want.Top || r.RepoCommon != want.Common || r.Start != before || r.StartFrom != journal.StartFromHost {
		t.Fatalf("the record's launch: where %q repo %q common %q start %q from %q (want %q, %q, %q)", r.Where, r.Repo, r.RepoCommon, r.Start, r.StartFrom, want.Top, want.Common, before)
	}
	if after == before || r.End != after {
		t.Fatalf("the record's end commit %q, the folder's %q after the start's %q", r.End, after, before)
	}
	// A shell is no agent's program: its version is not the agent's
	// (TestTheAgentsVersionIsTheAgents).
	if r.Agent.Version != "" || !r.Host.Hosted || r.Host.Owner == "" {
		t.Fatalf("the record's agent and host: %+v %+v", r.Agent, r.Host)
	}
}

// The end commit is the launch repository's, read through the folder the
// host opened for the launch and kept only when the repository there is
// still the launch's: a folder moved away with another repository made at
// its path has its own folder's commit where the system reads a folder
// through its handle (Linux) and none elsewhere, never the other's; a
// repository made again at the same path has none.
func TestTheHostsEndCommitIsItsLaunchRepositorys(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	srv := recordingServer(t)
	for _, c := range []struct {
		name   string
		change func(t *testing.T, dir string)
		own    bool
	}{
		{"its folder moved, another put at its path", func(t *testing.T, dir string) {
			if err := os.Rename(dir, dir+".was"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}, runtime.GOOS == "linux"},
		{"its repository made again at the same path", func(t *testing.T, dir string) {
			if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "w")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			git(t, dir, "init", "-q")
			git(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
			procs := make(chan *sessiontest.FakeProc, 1)
			fake := &sessiontest.FakeRuntime{Place: ext.WhereNative, Terminal: func(ext.TerminalSpec) (*sessiontest.FakeProc, error) {
				p := sessiontest.NewFakeProc()
				procs <- p
				return p, nil
			}}
			heads := make(chan [2]string, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			res, err := Run(ctx, Options{ServerURL: srv.URL, Token: hostToken, RelayOnly: true, Name: "moved", Argv: []string{"/bin/agent"}, Dir: dir, Runtime: fake,
				Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
				Registered: func(string, string) {
					go func() {
						p := <-procs
						git(t, dir, "commit", "-q", "--allow-empty", "-m", "during the session")
						head := git(t, dir, "rev-parse", "HEAD")
						c.change(t, dir)
						git(t, dir, "init", "-q")
						git(t, dir, "commit", "-q", "--allow-empty", "-m", "another repository")
						heads <- [2]string{head, git(t, dir, "rev-parse", "HEAD")}
						p.End(0)
					}()
				}})
			if err != nil {
				t.Fatal(err)
			}
			h := <-heads
			want := ""
			if c.own {
				want = h[0]
			}
			if r := endedRecord(t, srv.URL, res.SessionID); r.End != want || r.End == h[1] {
				t.Fatalf("the end commit %q, want %q (the other repository's %s)", r.End, want, h[1])
			}
		})
	}
}

// The agent's version is the agent's: the probe runs the launched program
// alone, never the launch's arguments, and only when that program is the
// agent's own; a wrapper (npx, a shell) answers for itself, so its version
// is never kept as the agent's.
func TestTheAgentsVersionIsTheAgents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in programs are shell scripts")
	}
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	stand := func(name, version string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho \"$@\" >> "+args+"\necho "+version+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	npx, opencode := stand("npx", "10.9.2"), stand("opencode", "1.4.0")
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{npx, "opencode-ai"}, ""},
		{[]string{"/bin/sh", "-c", opencode}, ""},
		{[]string{opencode, "--model", "x", "run", "a prompt"}, "1.4.0"},
	} {
		a := &agent{opts: Options{Adapter: "opencode", Argv: c.argv}}
		if got := a.probeVersion(t.Context()); got != c.want {
			t.Errorf("%q: the version %q, want %q", c.argv, got, c.want)
		}
	}
	ran, _ := os.ReadFile(args)
	if strings.TrimSpace(string(ran)) != "--version" {
		t.Fatalf("the stand-ins were run with %q", ran)
	}
}

// What the host's own deny list refuses of its session's files it leaves
// out of what it reports: a file entry there is not sent, one elsewhere is,
// and every other entry is sent as before.
func TestTheHostLeavesOutWhatItsDenyListRefuses(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "conductor-data")
	var sent []proto.HostActivityMsg
	a := &agent{opts: Options{FileDeny: func() []string { return []string{secret} }}, dir: dir, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1"}
	a.sendHook = func(v any) { sent = append(sent, v.(proto.HostActivityMsg)) }
	for _, e := range []session.ActivityEntry{
		{Type: session.ActivityFile, Op: "read", Path: filepath.Join(secret, "state.json")},
		{Type: session.ActivityFile, Op: "read", Path: "conductor-data/config.json"},
		{Type: session.ActivityFile, Op: "edit", Path: "main.go"},
		{Type: session.ActivityProgress, Message: "built"},
	} {
		a.sendActivity(e, "")
	}
	if len(sent) != 2 || sent[0].Entry.Path != "main.go" || sent[1].Entry.Message != "built" {
		t.Fatalf("sent %+v", sent)
	}
}

// A host whose Runtime is not native reports that where and no git fact:
// its git reader and its probe are never asked, and the record's Where is
// that one, with no repository, commit or version.
func TestANonNativeHostReportsItsWhereAndNoGit(t *testing.T) {
	srv := recordingServer(t)
	var asked atomic.Int32
	procs := make(chan *sessiontest.FakeProc, 1)
	fake := &sessiontest.FakeRuntime{Place: "vm", Terminal: func(ext.TerminalSpec) (*sessiontest.FakeProc, error) {
		p := sessiontest.NewFakeProc()
		procs <- p
		return p, nil
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, Options{ServerURL: srv.URL, Token: hostToken, RelayOnly: true, Name: "vm", Argv: []string{"/bin/agent"}, Dir: t.TempDir(), Runtime: fake,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registered: func(string, string) { go func() { (<-procs).End(0) }() },
		revision: func(context.Context, string) (gitcli.Revision, error) {
			asked.Add(1)
			return gitcli.Revision{Top: "/w", Common: "/w/.git", Commit: strings.Repeat("a", 40)}, nil
		},
		probe: func(context.Context, []string, agents.Probe, map[string]string) agents.ProbeResult {
			asked.Add(1)
			return agents.ProbeResult{Ran: true, Identified: true, Version: "1.0"}
		}})
	if err != nil {
		t.Fatal(err)
	}
	r := endedRecord(t, srv.URL, res.SessionID)
	if n := asked.Load(); n != 0 {
		t.Fatalf("a host on vm asked its git reader or its probe %d times", n)
	}
	if r.Where != "vm" || r.Repo != "" || r.RepoCommon != "" || r.Start != "" || r.StartFrom != "" || r.End != "" || r.Agent.Version != "" {
		t.Fatalf("the record of a host on vm: %+v", r)
	}
}
