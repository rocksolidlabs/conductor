package hostagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// What the host reports of its session for the session's record on the
// owner's own server (docs/plans/history.md, PR-12; proto.HostReport): the
// host is the launch site, so it reads its session's launch on its own
// machine, through its own Runtime, as the server does for its sessions.
// Where is always its Runtime's. Only when that Runtime is native, and
// before the process starts, does it read its folder's repository and
// commit (one rev-parse, the C locale, revisionTimeout), in the folder it
// opened for the launch (gitcli.OpenedPath), and its agent's version (its
// adapter's probe, agents.RunProbe, of the agent's own program alone); a
// Runtime elsewhere runs on a tree this machine does not observe, so it
// reports its where and no git fact. Its end commit, read the same way
// once the process has ended and kept only when the repository there is
// still the launch's (gitcli.LaunchRepo), goes in a session_info before
// the status that ends the session, to a server whose registered said it
// takes one. A switchyard is told none of it. What it reports is what it
// read of the thing it launched, or nothing.

// revisionTimeout bounds each of the host's git reads.
const revisionTimeout = 2 * time.Second

// readRevision is the git read of a folder's revision: Options' seam, or
// gitcli's.
func (a *agent) readRevision(ctx context.Context, dir string) (gitcli.Revision, error) {
	if a.opts.revision != nil {
		return a.opts.revision(ctx, dir)
	}
	return gitcli.RevisionOf(ctx, dir)
}

// probeVersion is the agent's version, "" for none: its adapter's probe of
// the launched program alone (Options' seam runs it in tests), never of
// the launch's arguments, which would run the launch again; and only when
// that program is the agent's own (agentProgram): a wrapper's answer (npx,
// a shell, an interpreter) is its own version, never kept as the agent's.
func (a *agent) probeVersion(ctx context.Context) string {
	p := agents.ProbeFor(a.opts.Adapter)
	if p == nil || len(a.opts.Argv) == 0 || !agentProgram(a.opts.Adapter, a.opts.Argv[0]) {
		return ""
	}
	run := agents.RunProbe
	if a.opts.probe != nil {
		run = a.opts.probe
	}
	if res := run(ctx, a.opts.Argv[:1], *p, nil); res.Identified {
		return res.Version
	}
	return ""
}

// agentProgram says program is the adapter's agent itself: its name, less
// a Windows launcher's extension, is the program a built-in launch of that
// adapter names (catalog.Default: claude, codex, opencode...).
func agentProgram(adapter, program string) bool {
	name := strings.ToLower(filepath.Base(program))
	for _, suffix := range []string{".exe", ".cmd", ".bat"} {
		name = strings.TrimSuffix(name, suffix)
	}
	for _, c := range catalog.Default().List() {
		if c.Adapter == adapter && len(c.Command) > 0 && strings.ToLower(filepath.Base(c.Command[0])) == name {
			return true
		}
	}
	return false
}

// readReport is what the host reports of its session, read before its
// process starts (Run), in folder, the one the process starts in: nothing
// for a switchyard; its Runtime's where; and for a native one, the
// revision (read through the folder as opened, its repository held for the
// end's read: launchDir, launchRepo) and the version, read side by side. A
// read that fails, times out, finds the folder's path no longer naming the
// folder opened or is past its bounds (proto.HostReport.Check) leaves what
// it read out: not reported.
func (a *agent) readReport(ctx context.Context, folder *session.Folder) proto.HostReport {
	if a.opts.Switchyard {
		return proto.HostReport{}
	}
	r := proto.HostReport{Where: string(a.opts.Runtime.Where())}
	if a.opts.Runtime.Where() != ext.WhereNative {
		return r
	}
	var wg sync.WaitGroup
	version := ""
	wg.Go(func() { version = a.probeVersion(ctx) })
	if dir, err := folder.Dir(); err == nil {
		a.launchDir = dir
	}
	at, still := gitcli.OpenedPath(a.launchDir, a.dir)
	rctx, cancel := context.WithTimeout(ctx, revisionTimeout)
	defer cancel()
	rev, err := a.readRevision(rctx, at)
	wg.Wait()
	r.AgentVersion = version
	if err == nil && still() && rev.Common != "" {
		if a.launchRepo = gitcli.OpenLaunchRepo(rev.Common); a.launchRepo != nil {
			r.Repo, r.RepoCommon, r.StartCommit = rev.Top, rev.Common, rev.Commit
		}
	}
	if (proto.HostReport{AgentVersion: r.AgentVersion}).Check() != nil {
		r.AgentVersion = ""
	}
	if (proto.HostReport{Repo: r.Repo, RepoCommon: r.RepoCommon, StartCommit: r.StartCommit}).Check() != nil {
		r.Repo, r.RepoCommon, r.StartCommit = "", "", ""
	}
	if r.Check() != nil {
		r = proto.HostReport{}
	}
	if r.RepoCommon == "" {
		// No repository reported at the start: none's end is either.
		a.launchRepo.Close()
		a.launchRepo = nil
	}
	return r
}

// endRevision is the commit the host's folder is at once its process has
// ended, read once (native only, not for a switchyard), through the folder
// opened for the launch and kept only when the repository found there is
// the launch's (launchRepo, the file system's identity of its common
// directory): a folder moved with another repository put at its path, or
// its repository made again, lends no commit. "" when it was not read.
func (a *agent) endRevision(ctx context.Context) string {
	a.endOnce.Do(func() {
		if rt := a.opts.Runtime; a.opts.Switchyard || rt == nil || rt.Where() != ext.WhereNative || a.launchRepo == nil {
			return
		}
		at, still := gitcli.OpenedPath(a.launchDir, a.dir)
		rctx, cancel := context.WithTimeout(ctx, revisionTimeout)
		defer cancel()
		if rev, err := a.readRevision(rctx, at); err == nil && still() && a.launchRepo.Is(rev.Common) && proto.ValidCommit(rev.Commit) {
			a.endCommit = rev.Commit
		}
	})
	return a.endCommit
}

// letLaunchGo ends the end's read (one under way finishes first; none
// starts after) and closes the folder and the repository held for it, as
// Run returns.
func (a *agent) letLaunchGo() {
	a.endOnce.Do(func() {})
	if a.launchDir != nil {
		a.launchDir.Close()
	}
	a.launchRepo.Close()
}

// takesSessionInfo says the server of the current connection takes
// session_info (registered.sessionInfo), and it is not a switchyard.
func (a *agent) takesSessionInfo() bool { return a.sessionInfo.Load() && !a.opts.Switchyard }

// sendSessionInfo sends w's server what the host read of its session at
// its end (endCommit), ahead of the status that ends it.
func (a *agent) sendSessionInfo(w *controlWriter, sessionID, endCommit string) {
	if b, err := json.Marshal(proto.HostSessionInfoMsg{T: proto.HostSessionInfo, SessionID: sessionID, EndCommit: endCommit}); err == nil {
		_ = w.sendControl(websocket.MessageText, b)
	}
}
