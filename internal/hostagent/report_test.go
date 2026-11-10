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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/journal"
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
		Runtime: commitAtStart{local.Native(), t}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		probe: func(_ context.Context, argv []string, _ string) string {
			return "9.9.9 (" + filepath.Base(argv[0]) + ")"
		}})
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
	if r.Agent.Version != "9.9.9 (sh)" || !r.Host.Hosted || r.Host.Owner == "" {
		t.Fatalf("the record's agent and host: %+v %+v", r.Agent, r.Host)
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
		probe: func(context.Context, []string, string) string { asked.Add(1); return "1.0" }})
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
