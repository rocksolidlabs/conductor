package hostagent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// What the host reports of its session for the session's record on the
// owner's own server (docs/plans/history.md, PR-12; proto.HostReport): the
// host is the launch site, so it reads its session's launch on its own
// machine, through its own Runtime, as the server does for its sessions.
// Where is always its Runtime's. Only when that Runtime is native, and
// before the process starts, does it read its folder's repository and
// commit (one rev-parse, the C locale, revisionTimeout) and its agent's
// version (its adapter's probe, agents.RunProbe); a Runtime elsewhere runs
// on a tree this machine does not observe, so it reports its where and no
// git fact. Its end commit, read the same way once the process has ended,
// goes in a session_info before the status that ends the session, to a
// server whose registered said it takes one. A switchyard is told none of
// it.

// revisionTimeout bounds each of the host's git reads.
const revisionTimeout = 2 * time.Second

// readRevision is the git read of a folder's revision; probeVersion the
// agent's version, "" for none: Options' seams, or gitcli's and the
// adapter's probe.
func (a *agent) readRevision(ctx context.Context, dir string) (gitcli.Revision, error) {
	if a.opts.revision != nil {
		return a.opts.revision(ctx, dir)
	}
	return gitcli.RevisionOf(ctx, dir)
}

func (a *agent) probeVersion(ctx context.Context) string {
	if a.opts.probe != nil {
		return a.opts.probe(ctx, a.opts.Argv, a.opts.Adapter)
	}
	p := agents.ProbeFor(a.opts.Adapter)
	if p == nil || len(a.opts.Argv) == 0 {
		return ""
	}
	if res := agents.RunProbe(ctx, a.opts.Argv[:1], *p, nil); res.Identified {
		return res.Version
	}
	return ""
}

// readReport is what the host reports of its session, read before its
// process starts (Run): nothing for a switchyard; its Runtime's where; and
// for a native one, the revision and the version, read side by side. A
// read that fails, times out or is past its bounds (proto.HostReport.Check)
// leaves what it read out: not reported.
func (a *agent) readReport(ctx context.Context) proto.HostReport {
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
	rctx, cancel := context.WithTimeout(ctx, revisionTimeout)
	defer cancel()
	rev, err := a.readRevision(rctx, a.dir)
	wg.Wait()
	r.AgentVersion = version
	if err == nil {
		r.Repo, r.RepoCommon, r.StartCommit = rev.Top, rev.Common, rev.Commit
	}
	if (proto.HostReport{AgentVersion: r.AgentVersion}).Check() != nil {
		r.AgentVersion = ""
	}
	if (proto.HostReport{Repo: r.Repo, RepoCommon: r.RepoCommon, StartCommit: r.StartCommit}).Check() != nil {
		r.Repo, r.RepoCommon, r.StartCommit = "", "", ""
	}
	if r.Check() != nil {
		return proto.HostReport{}
	}
	return r
}

// endRevision is the commit the host's folder is at once its process has
// ended, read once (native only, not for a switchyard): "" when it was not
// read.
func (a *agent) endRevision(ctx context.Context) string {
	a.endOnce.Do(func() {
		if rt := a.opts.Runtime; a.opts.Switchyard || rt == nil || rt.Where() != ext.WhereNative {
			return
		}
		rctx, cancel := context.WithTimeout(ctx, revisionTimeout)
		defer cancel()
		if rev, err := a.readRevision(rctx, a.dir); err == nil && proto.ValidCommit(rev.Commit) {
			a.endCommit = rev.Commit
		}
	})
	return a.endCommit
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
