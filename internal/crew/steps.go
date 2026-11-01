package crew

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proc"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A managed run with a project manifest (manifest.go;
// docs/plans/project-manifests.md, EN1) makes every tree usable through
// it: an attempt's worktree before its session starts, and the checkout of
// its result before its checks. The manifest is resolved for the tree
// (resolveTree: the crew's, or conductor.manifest.json at the tree's
// commit), then its phases run (prepareTree: setup, services, ready), each
// step a receipt (runStep: a checks row of its phase, started, running and
// finished, with its log and the words of its failure). The run's
// prerequisites run once, before the first attempt, in the crew's
// directory (prerequisites), and again at each unpause. The checkout's
// verify steps run after the task's checks, as checks of the crew's
// (verify.go). When an attempt closes, however it closes, its trees end
// (endTree), each once, after the close is committed and the tree is
// quiet: their services stopped, then their cleanup steps run (cleanTree).
//
// A step that fails in a worktree is the host's or the manifest's: the
// attempt is not counted, and the dispatch pauses for a person
// (failUnusable). One that fails in the checkout of a result is the
// result's, counted as a failing check is. A worktree at a checkpoint the
// agent saved is the agent's: a failure there refuses the checkpoint,
// uncounted and unpaused, and the next attempt starts where the
// checkpointed one did.
//
// A tree's services (startService) run beside it from its services phase
// until it ends: each a foreground process of its own group, started
// through the tree's executor under the run's teardown context, stopped by
// the tree's end or the run's stop (SIGTERM to its group, SIGKILL after
// proc.ServeGrace), its end recorded once (service.ended). The ready probes
// (runProbe) then wait for them to answer, and tree.ready (treeReady)
// records the tree usable: the attempt's preview is its worktree's URL from
// then until the attempt's close, or until a service of the worktree exits
// on its own (watchService).
//
// A run without a manifest (no setup, no manifest) makes no tree and
// keeps no step receipt: it runs as it did before manifests.

// The prerequisites' states (run.prereq): pending once armed, running
// from its generation's first step, then passed or failed; interrupted
// when the commit that finished the run, or a stop, ended the generation
// before its verdict, or before it began (startPrerequisites), with none
// in flight. The pump dispatches only on passed.
const (
	prereqPending     = "pending"
	prereqRunning     = "running"
	prereqPassed      = "passed"
	prereqFailed      = "failed"
	prereqInterrupted = "interrupted"
)

// The default timeouts of the manifest's phases, in seconds, when a step
// gives none (a setup or verify step's is a check's, defaultCheckTimeout).
const (
	defaultPrerequisiteTimeout = 60
	defaultCleanupTimeout      = 60
)

// Sources of a manifest: the crew's, or the file at a commit.
const (
	sourceCrew = "crew"
	sourceFile = "file"
)

// resolvedManifest is a project manifest as a tree, or the run's
// prerequisites, ran it: the manifest and where it came from, the crew at
// its revision or the file as a blob.
type resolvedManifest struct {
	m        Manifest
	source   string
	revision int64
	digest   string
}

// identity is the manifest's name in a receipt: crew:<revision> or
// file:<blob>.
func (rm resolvedManifest) identity() string {
	if rm.source == sourceFile {
		return "file:" + rm.digest
	}
	return fmt.Sprintf("crew:%d", rm.revision)
}

// task is the manifest's identity as a task file names it.
func (rm resolvedManifest) task() *TaskManifest {
	tm := &TaskManifest{Source: rm.source}
	if rm.source == sourceFile {
		tm.Digest = rm.digest
	} else {
		tm.Revision = rm.revision
	}
	return tm
}

// manifestRow is the manifest at a run's base as the run's ledger row
// keeps it (ledger.RunRow's Manifest): where it comes from and its
// prerequisites' ids in order, which a run whose record is missing reports
// (Recovered) without reading the file at the base again.
type manifestRow struct {
	Source        string   `json:"source"`
	Revision      int64    `json:"revision,omitempty"`
	Digest        string   `json:"digest,omitempty"`
	Prerequisites []string `json:"prerequisites,omitempty"`
}

// row is the manifest as the run's ledger row keeps it (manifestRow).
func (rm resolvedManifest) row() string {
	mr := manifestRow{Source: rm.source}
	if rm.source == sourceFile {
		mr.Digest = rm.digest
	} else {
		mr.Revision = rm.revision
	}
	for _, ch := range rm.m.Prerequisites {
		mr.Prerequisites = append(mr.Prerequisites, ch.ID)
	}
	b, err := json.Marshal(mr)
	if err != nil {
		return ""
	}
	return string(b)
}

// tree is a directory the engine makes usable through the run's project
// manifest: an attempt's worktree, or the checkout of its result. Its
// manifest is the one resolved for it, expanded with its ports and its
// state directory (expandManifest); its ports are leased to it on the
// engine (e.ports) from its manifest.resolved until its tree.ended is
// committed (releaseEnded); its state directory is .conductor/state under
// its working directory; its preview is the manifest's, expanded, "" for
// none; allow is the manifest's env names the server approved
// (allowedEnv). All of that is set before the tree is the attempt's and
// never changed. busy is held from the change that makes it the
// attempt's until its caller has made it ready to work in (resolveTree),
// while one of its phases or checks runs, and in a worktree through its
// session's launch. Under e.mu:
// handed is set once it is handed to endTree, ending once its end begins
// or the run's stop ends its services (nothing holds it after, and no
// service starts on it: startService), ended once its end is recorded,
// and session names the session its end waits for, one being stopped (set
// by handTrees, or by the launch's record when the close came during the
// launch). starting is the service whose start is under way, from before
// its process is made until the executor hands it over, nil while none is
// (a tree's services start one at a time): a run's stop that comes
// meanwhile sees it and waits for it (stopServices). services are its
// services in the order they started, each appended as the executor hands
// it over, until its end has recorded theirs (endServices lets go of
// them); ready is set once its tree.ready is recorded; dead is the first
// of its services that exited on its own before that, and died is closed
// with it, for the phase that waits on a probe.
type tree struct {
	kind     string // ledger.TreeWorktree | ledger.TreeCheckout
	dir      string // the worktree or the checkout
	cwd      string // the crew's working directory in it
	commit   string
	man      resolvedManifest
	ports    map[string]int
	stateDir string
	preview  string
	allow    []string
	a        *attempt
	busy     sync.WaitGroup
	// handed, ending, ended, session, starting, services, ready, dead:
	// see above.
	handed, ending, ended bool
	session               string
	starting              *treeService
	services              []*treeService
	ready                 bool
	dead                  *treeService
	died                  chan struct{}
}

// service is a service process of a tree as the engine holds it: the
// tree's executor started it (treeExec.serve), and the engine sees its
// end (Done, Result) and stops it (Stop: SIGTERM to its group, SIGKILL
// after the grace or once ctx ends), and nothing of its pid but what its
// receipt records. *proc.Service is the native one.
type service interface {
	Done() <-chan struct{}
	Result() proc.Result
	Stop(ctx context.Context) error
}

// treeService is a service of a tree: its step, its receipt as put (running
// once its process exists), its handle and its output, as its row keeps it
// at its end. Under e.mu: stopping is why something is ending it (the
// tree's end, the run's stop, a start refused), "" while nothing is; ended
// is set once its end is recorded, or in the change that records it; row
// and svc are set as the executor hands it over. started is closed then,
// or once its start has failed (svc nil), and svc is read without e.mu
// after it (stopService). watched is closed once its watcher has returned
// (watchService), which starts once its running row's transaction has
// returned, so its end is never recorded before its start.
type treeService struct {
	ch       Check
	row      ledger.CheckRow
	svc      service
	log      *bytes.Buffer
	stopping string
	ended    bool
	started  chan struct{}
	watched  chan struct{}
}

// treeExec is everything a tree does on the machine that runs it: a
// command, run as a check is (run), a service started (serve), one try of
// a probe over HTTP (probe) and a port drawn for it (port). The native one
// runs proc.Run and proc.Serve, fetches a URL with a client of the probe's
// own (probeURL) and binds 127.0.0.1:0; a test replaces it whole, and the
// Runtime seam (C10) wraps it, so nothing of a tree reaches the machine
// but through it, and a probe or a port of a tree that is not on the
// server's machine is never the server's own loopback.
type treeExec struct {
	run   func(ctx context.Context, s proc.Spec) proc.Result
	serve func(ctx context.Context, s proc.Spec) (service, error)
	probe func(ctx context.Context, url string, interval time.Duration) (status int, err error)
	port  func(ctx context.Context) (int, error)
}

// nativeExec runs a tree's commands and services, probes its URLs and
// draws its ports, on this machine.
func nativeExec() treeExec {
	return treeExec{run: proc.Run, serve: nativeServe, probe: probeURL, port: freePort}
}

// nativeServe starts a service on this machine (proc.Serve).
func nativeServe(ctx context.Context, s proc.Spec) (service, error) {
	svc, err := proc.Serve(ctx, s)
	if err != nil {
		return nil, err
	}
	return svc, nil
}

// probeClient is the client of one try of a probe over HTTP: the probe's
// interval its timeout, no proxy and no connection kept, and no redirect
// followed (http.ErrUseLastResponse: a 3xx is the try's answer), so a
// service, or a manifest at a result commit, answering a redirect off the
// loopback makes the server fetch nothing there.
func probeClient(interval time.Duration) *http.Client {
	return &http.Client{
		Timeout:       interval,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// probeURL is one try of a probe over HTTP on this machine: one GET of url
// through a client of its own (probeClient), its status, the body closed
// unread, so nothing of it is kept.
func probeURL(ctx context.Context, url string, interval time.Duration) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := probeClient(interval).Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// freePort draws a port of this machine's loopback: a bind of 127.0.0.1:0,
// its number taken and the listener closed, so that the tree's own can
// bind it. Nothing holds the port on the host meanwhile; the engine's
// lease (e.ports) keeps two live trees of this server off one number.
func freePort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok || addr.Port == 0 {
		return 0, fmt.Errorf("the loopback gave no port: %v", l.Addr())
	}
	return addr.Port, nil
}

// maxPortDraws bounds the numbers drawn for one port of a tree: a number
// another live tree of this server holds is drawn again, at most so often.
const maxPortDraws = 16

// stateDirName is a tree's state directory in its .conductor.
const stateDirName = "state"

// SetEnvPassthrough gives the engine the server's envPassthrough (config
// envPassthrough, read once at the server's start): the host's variables a
// project manifest's env may pass on to its steps and sessions
// (allowedEnv). The manifest names what the project needs; the host
// decides what leaves it. Set it before the first launch; until then no
// name passes.
func (e *Engine) SetEnvPassthrough(names []string) {
	e.mu.Lock()
	e.envPassthrough = slices.Clone(names)
	e.mu.Unlock()
}

// allowedEnv approves a project manifest's env names, in order, for a
// tree's commands and session or for the run's prerequisites: each must be
// in the server's envPassthrough and set in the server's environment,
// which is read once, at its start (pty.ParentEnv is os.Environ), and never
// reloaded. The first that is not is the error, in words naming it: one
// the server does not allow, or one the host does not have, with what fixes
// that. Nothing of a value is read beyond its presence, and nothing of it
// is recorded.
func (e *Engine) allowedEnv(names []string) ([]string, error) {
	e.mu.Lock()
	allowed := e.envPassthrough
	e.mu.Unlock()
	var out []string
	for _, name := range names {
		if !slices.Contains(allowed, name) {
			return nil, fmt.Errorf("%s is not in the server's envPassthrough", name)
		}
		if _, ok := os.LookupEnv(name); !ok {
			return nil, fmt.Errorf("the host has no %s: set it where the server starts, start it again and continue the run", name)
		}
		out = append(out, name)
	}
	return out, nil
}

// portVar is the variable a tree's port is in: CONDUCTOR_PORT_<NAME>, the
// port's name in upper case.
func portVar(name string) string { return "CONDUCTOR_PORT_" + strings.ToUpper(name) }

// names is Conductor's own names of t as its session is given them
// (LaunchSpec.Inject): its ports, its state directory and its preview.
func (t *tree) names() map[string]string {
	env := map[string]string{"CONDUCTOR_STATE_DIR": t.stateDir}
	for name, n := range t.ports {
		env[portVar(name)] = strconv.Itoa(n)
	}
	if t.preview != "" {
		env["CONDUCTOR_PREVIEW_URL"] = t.preview
	}
	return env
}

// cmdEnv is what a command of a run is given beside the fixed allowlist
// (proc.Spec): allow, the project manifest's env names the server
// approved, and inject, Conductor's own names of the command's tree.
type cmdEnv struct {
	allow  []string
	inject map[string]string
}

// env is what a command run in t is given: its approved env names, and its
// names with the run, the task and the attempt, which a session has from
// its crew's names; nothing for no tree (a run without a manifest).
func (t *tree) env(runID string) cmdEnv {
	if t == nil {
		return cmdEnv{}
	}
	inject := t.names()
	inject["CONDUCTOR_RUN"], inject["CONDUCTOR_TASK"], inject["CONDUCTOR_ATTEMPT"] = runID, t.a.t.def.ID, strconv.Itoa(t.a.n)
	return cmdEnv{allow: t.allow, inject: inject}
}

// task is the tree's manifest as its task file names it: the identity, the
// ports by name, the preview and the services' ids.
func (t *tree) task() *TaskManifest {
	tm := t.man.task()
	tm.Ports = maps.Clone(t.ports)
	tm.Preview = t.preview
	for _, ch := range t.man.m.Services {
		tm.Services = append(tm.Services, ch.ID)
	}
	return tm
}

// flight counts goroutines in flight under e.mu and is waited for without
// it: a sync.WaitGroup must not take an Add while another goroutine waits
// on it at zero, and a tree may be handed over while a stop waits for the
// trees. zero is closed while n is 0.
type flight struct {
	n    int
	zero chan struct{}
}

// add counts one more. The caller holds e.mu.
func (f *flight) add() {
	if f.n == 0 {
		f.zero = make(chan struct{})
	}
	f.n++
}

// done counts one less. The caller holds e.mu.
func (f *flight) done() {
	if f.n--; f.n == 0 {
		close(f.zero)
	}
}

// idle is a channel closed once nothing is in flight. The caller holds
// e.mu, and waits on it without.
func (f *flight) idle() <-chan struct{} {
	if f.n == 0 {
		return closedChan
	}
	return f.zero
}

var closedChan = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

// waitTrees waits for r's trees being ended, or for ctx.
func (e *Engine) waitTrees(ctx context.Context, r *run) {
	e.mu.Lock()
	idle := r.trees.idle()
	e.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
	}
}

// hasManifest reports whether r has a project manifest: a crew's with
// something in it, a legacy setup lifted into one, or the file form. A run
// without one makes no tree.
func (r *run) hasManifest() bool { return r.managed && !r.manifest.empty() }

// stepTimeout is a step's timeout: its own, or its phase's default.
func stepTimeout(phase string, ch Check) time.Duration {
	if ch.TimeoutSeconds > 0 {
		return time.Duration(ch.TimeoutSeconds) * time.Second
	}
	switch phase {
	case ledger.PhasePrerequisite:
		return defaultPrerequisiteTimeout * time.Second
	case ledger.PhaseCleanup:
		return defaultCleanupTimeout * time.Second
	}
	return defaultCheckTimeout * time.Second
}

// errAttemptClosed says the attempt closed before its tree was resolved:
// nothing was made of it.
var errAttemptClosed = errors.New("the attempt closed")

// resolveAt is r's project manifest as a tree at commit has it: the crew's
// as launched, or, with the file form, conductor.manifest.json at commit,
// read and held to a manifest's rules (resolveManifest).
func (e *Engine) resolveAt(ctx context.Context, r *run, commit string) (resolvedManifest, error) {
	if !r.manifest.File {
		return resolvedManifest{m: r.manifest, source: sourceCrew, revision: r.manifestRev}, nil
	}
	rec, blob, err := resolveManifest(ctx, r.cwd, commit, r.prefix, r.plan)
	if err != nil {
		return resolvedManifest{}, err
	}
	return resolvedManifest{m: rec, source: sourceFile, digest: blob}, nil
}

// resolveTree makes the tree of kind at dir (the crew's directory cwd in
// it, at commit) attempt a's. In order: its manifest resolved (resolveAt);
// its env names approved (allowedEnv), so a manifest at any commit passes
// on no name the server's operator did not allow or the host does not
// have; its ports drawn (treeExec.port); the manifest expanded with them
// and the state directory (expandManifest) and held to its bounds. Then
// one transaction of the run's leases the ports to the tree and records
// manifest.resolved, manifest.ports (with ports or a preview) and the
// tree's row, resolved: a number another live tree of this server holds,
// or another of the tree's ports drew, is drawn again (at most
// maxPortDraws times a port) instead, and from the commit on the tree is
// the attempt's (a.trees), its ports leased until its tree.ended is
// committed. The transaction is ordered with the attempt's close: an
// attempt closed before it gets no tree (errAttemptClosed). The tree comes
// back held (busy) from that change on, so a close that comes before the
// caller has made it ready to work in (its state directory here, the task
// files and the phases after) ends it, and runs its cleanup, only once the
// caller lets go of it (t.busy.Done), which it does on every way out
// (Codex's review of #498). Last, its state directory is made
// (makeStateDir). Any other error is the manifest's, its stage "manifest",
// the failure attributed by the tree's rule: nothing is written or run in
// the tree before the transaction, and a tree whose state directory could
// not be made is returned with the error, held, the attempt's to end with
// it.
func (e *Engine) resolveTree(ctx context.Context, r *run, a *attempt, kind, dir, cwd, commit string) (*tree, error) {
	man, err := e.resolveAt(ctx, r, commit)
	if err != nil {
		return nil, err
	}
	allow, err := e.allowedEnv(man.m.Env)
	if err != nil {
		return nil, err
	}
	stateDir := filepath.Join(cwd, taskDir, stateDirName)
	ports := map[string]int{}
	draws := map[string]int{}
	draw := man.m.Ports
	for {
		for _, name := range draw {
			if draws[name] == maxPortDraws {
				return nil, fmt.Errorf("the port %s: %d numbers drawn, each another tree's", name, maxPortDraws)
			}
			draws[name]++
			n, err := e.exec.port(ctx)
			if err != nil {
				return nil, fmt.Errorf("the port %s could not be drawn: %w", name, err)
			}
			ports[name] = n
		}
		expanded, err := expandManifest(man.m, ports, stateDir)
		if err != nil {
			if man.source == sourceFile {
				err = fmt.Errorf("%s at %s: %w", r.prefix+manifestFile, short(commit), err)
			}
			return nil, err
		}
		t := &tree{kind: kind, dir: dir, cwd: cwd, commit: commit, man: resolvedManifest{m: expanded, source: man.source, revision: man.revision, digest: man.digest},
			ports: maps.Clone(ports), stateDir: stateDir, preview: expanded.Preview, allow: allow, a: a, died: make(chan struct{})}
		closed, held := false, false
		draw = nil
		err = e.transact(ctx, r, func() tx {
			if !a.closedAt.IsZero() {
				closed = true
				return tx{}
			}
			if draw = e.heldPorts(t); len(draw) > 0 {
				return tx{}
			}
			e.lease(t)
			// Held before it is the attempt's: no close finds it quiet
			// until the caller has made it ready to work in.
			t.busy.Add(1)
			held = true
			a.trees = append(a.trees, t)
			payload := map[string]any{"tree": kind, "task": a.t.def.ID, "attempt": a.n, "source": man.source, "env": slices.Clone(man.m.Env)}
			if man.source == sourceFile {
				payload["digest"] = man.digest
			} else {
				payload["revision"] = man.revision
			}
			out := ev("manifest.resolved", payload)
			if len(t.ports) > 0 || t.preview != "" {
				out.events = append(out.events, event{"manifest.ports", map[string]any{"tree": kind, "task": a.t.def.ID, "attempt": a.n, "ports": maps.Clone(t.ports), "preview": t.preview}})
			}
			row := t.row(r.id, ledger.TreeResolved)
			out.more = func(ltx LedgerTx) error { return ltx.PutTree(row) }
			return out
		}, func() {
			a.trees = slices.DeleteFunc(a.trees, func(x *tree) bool { return x == t })
			e.release(t)
			if held {
				held = false
				t.busy.Done()
			}
		})
		switch {
		case closed:
			return nil, errAttemptClosed
		case err != nil:
			return nil, fmt.Errorf("the ledger could not record the manifest: %w", err)
		case len(draw) > 0:
			continue // another tree's number: drawn again
		}
		if e.afterTreeResolved != nil {
			e.afterTreeResolved(kind)
		}
		if err := makeStateDir(cwd, kind == ledger.TreeCheckout); err != nil {
			return t, fmt.Errorf("the state directory %s/%s could not be made: %w", taskDir, stateDirName, err)
		}
		return t, nil
	}
}

// heldPorts is the names of t's ports, in the manifest's order, whose number
// another live tree of this server holds, or an earlier port of t drew
// too: to be drawn again. The caller holds e.mu.
func (e *Engine) heldPorts(t *tree) []string {
	var held []string
	seen := map[int]bool{}
	for _, name := range t.man.m.Ports {
		n := t.ports[name]
		if holder, ok := e.ports[n]; (ok && holder != t) || seen[n] {
			held = append(held, name)
		}
		seen[n] = true
	}
	return held
}

// lease leases t's ports to it, until its tree.ended is committed
// (releaseEnded). The caller holds e.mu, in the transaction's change that
// makes the tree the attempt's.
func (e *Engine) lease(t *tree) {
	if e.ports == nil {
		e.ports = map[int]*tree{}
	}
	for _, n := range t.ports {
		e.ports[n] = t
	}
}

// release lets go of the ports t holds. The caller holds e.mu: once the
// commit that wrote t's tree.ended is made (releaseEnded), or in the undo
// of the change that leased them.
func (e *Engine) release(t *tree) {
	for _, n := range t.ports {
		if e.ports[n] == t {
			delete(e.ports, n)
		}
	}
}

// releaseEnded lets go of the ports of the trees whose tree.ended the
// commit just made wrote: its own change's, or one the ledger refused
// before, which a refused change's events wait for the run's next commit
// to carry (transact). A number is so another tree's only once the ledger
// has its tree ended: never between the change and its commit, when
// transact has let e.mu go and another run's tree could draw it while the
// ledger still has this one resolved. One refused for good (the run never
// commits again) keeps its ports until the server exits, as the trees of
// a refused stop do. The caller holds e.mu, after the commit.
func (e *Engine) releaseEnded(r *run) {
	for _, t := range r.pendingReleases {
		e.release(t)
	}
	r.pendingReleases = nil
}

// makeStateDir makes a tree's state directory, .conductor/state under its
// working directory cwd, through a root at cwd as the task files are
// written (writeTaskFiles): .conductor made when missing and refused when it
// is there as anything but a directory, and the state directory the same.
// A checkout, which has no task files, gets .conductor's .gitignore beside
// it, so the comparisons after its steps and checks list nothing of it; a
// worktree's comes with its task files, written next. The directory is
// inside the tree, so a tree handed to a guest whole carries it.
func makeStateDir(cwd string, checkout bool) error {
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := mkdirIn(root, taskDir); err != nil {
		return err
	}
	if checkout {
		ignore := path.Join(taskDir, ".gitignore")
		if _, err := root.Lstat(ignore); errors.Is(err, fs.ErrNotExist) {
			if err := root.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return mkdirIn(root, path.Join(taskDir, stateDirName))
}

// mkdirIn makes the directory name in root: one there already is taken
// when it is a directory, refused when it is anything else (a link).
func mkdirIn(root *os.Root, name string) error {
	fi, err := root.Lstat(name)
	switch {
	case err == nil && !fi.IsDir():
		return fmt.Errorf("%s exists in the tree and is not a directory", name)
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return root.Mkdir(name, 0o755)
	}
	return err
}

// ending reports whether t's end has begun: its attempt closed, or the
// run's stop ended its services. A caller holding t (busy, from
// resolveTree) lets go of it and runs nothing more in it then.
func (e *Engine) ending(t *tree) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return t.ending
}

// prepareTree makes t usable, in order, stopping at the first failure:
// its setup phase, each step a receipt (runStep), in the tree's working
// directory; its services, each started and running before the next
// (startService); its ready probes, each run until it passes (runProbe);
// in a checkout, the tree compared with the result after each setup step,
// which ran alone in it, so a change is that step's, in its receipt
// (runStep), and once after the ready phase, the services having run
// beside the probes, so a change is the phase's (stage "ready"); then
// tree.ready recorded (treeReady), before the launch or the first check
// rests on the tree. A service that exits before then fails the tree at it
// ("service <id>"). It returns the stage of a failure ("setup <id>",
// "service <id>", "ready <id>", "ready") with its words, "" and nil once
// the tree is ready. The caller holds t (busy, from resolveTree).
func (e *Engine) prepareTree(ctx context.Context, r *run, a *attempt, t *tree) (string, error) {
	for _, ch := range t.man.m.Setup {
		if _, err := e.runStep(ctx, r, a, t, ledger.PhaseSetup, ch, 0); err != nil {
			return "setup " + ch.ID, err
		}
	}
	for _, ch := range t.man.m.Services {
		if dead := e.deadService(t); dead != nil {
			return "service " + dead.ch.ID, errors.New(beforeReadyWords(dead))
		}
		if err := e.startService(ctx, r, a, t, ch); err != nil {
			return "service " + ch.ID, err
		}
	}
	for _, p := range t.man.m.Ready {
		if stage, err := e.runProbe(ctx, r, a, t, p); err != nil {
			return stage, err
		}
	}
	if t.kind == ledger.TreeCheckout && (len(t.man.m.Services) > 0 || len(t.man.m.Ready) > 0) {
		moved, changed, _, err := checkoutIntact(ctx, t.dir, t.commit)
		if ctx.Err() != nil {
			return "ready", fmt.Errorf("the ready phase was interrupted: %w", ctx.Err())
		}
		if words := readyWords(t.man.m, moved, changed, err); words != "" {
			return "ready", errors.New(words)
		}
	}
	return e.treeReady(ctx, r, a, t)
}

// deadService is the first of t's services that exited on its own before
// the tree was ready, nil for none (exited).
func (e *Engine) deadService(t *tree) *treeService {
	e.mu.Lock()
	defer e.mu.Unlock()
	return t.exited()
}

// exited is the first of t's services that ended on its own before the
// tree was ready: the one its watcher recorded (t.dead), else one whose
// process has ended (Done) while nothing is ending it and its end is not
// recorded, its watcher not there yet, so the tree is never recorded
// ready over a service gone, whichever comes first, its watcher or the
// phase. The caller holds e.mu.
func (t *tree) exited() *treeService {
	if t.dead != nil {
		return t.dead
	}
	for _, ts := range t.services {
		if ts.stopping != "" || ts.ended {
			continue
		}
		select {
		case <-ts.svc.Done():
			return ts
		default:
		}
	}
	return nil
}

// foregroundRule is what a service that exits 0 is told, whenever it
// does: a service is a foreground process, and one that exits 0 is most
// often a daemonizer, whose daemon, outside its group, the tree neither
// stops nor reports.
const foregroundRule = "a service must stay in the foreground"

// beforeReadyWords says why a service that exited before its tree was
// ready fails the tree; one that exited 0 is told the rule
// (foregroundRule).
func beforeReadyWords(ts *treeService) string {
	res := ts.svc.Result()
	switch {
	case res.Signal != "":
		return fmt.Sprintf("%s was ended by a signal (%s) before the tree was ready", ts.ch.ID, res.Signal)
	case res.Exit == 0:
		return fmt.Sprintf("%s exited 0 before the tree was ready: %s", ts.ch.ID, foregroundRule)
	case res.Exit > 0:
		return fmt.Sprintf("%s exited %d before the tree was ready%s", ts.ch.ID, res.Exit, tailWords(lastOutput(res, checkTail)))
	}
	return fmt.Sprintf("%s ended before the tree was ready: %s", ts.ch.ID, session.CleanMessage(res.Err))
}

// serviceExit says how a service's process ended, in words: "exited 1",
// "was ended by a signal (killed)".
func serviceExit(res proc.Result) string {
	switch {
	case res.Signal != "":
		return fmt.Sprintf("was ended by a signal (%s)", res.Signal)
	case res.Exit >= 0:
		return fmt.Sprintf("exited %d", res.Exit)
	}
	return "ended"
}

// stepPayload is a step event's payload: its phase, id and k, its task,
// attempt and tree, and more.
func stepPayload(a *attempt, t *tree, phase, id string, k int, more map[string]any) map[string]any {
	p := map[string]any{"phase": phase, "id": id, "k": k, "task": a.t.def.ID, "attempt": a.n, "tree": t.kind}
	for key, v := range more {
		p[key] = v
	}
	return p
}

// startService starts one of t's services, its receipt a row of phase
// service: put started before anything runs (step.started); the start
// registered on the tree (t.starting) under e.mu before its process is
// made, unless the tree's end has begun (its end, or the run's stop),
// when none is made and the row is finished with why, so a run's stop
// either sees the start under way, and waits for it, or comes first and
// is never outrun by it (stopServices: Codex's review of #441, round 3);
// the process started through the tree's executor (treeExec.serve) with
// the tree's environment, under r.teardown and never the attempt's
// context, whose end would kill the service's group at the attempt's
// close before the tree's end could ask it to stop (a service ends by
// Stop, from endTree or the run's stop, and by its context only at the
// shutdown's cut-off); its handle on the tree under e.mu as the executor
// hands it over (started); then its row running with its pid, group and
// start time (step.running), committed before the next service starts,
// and the service watched from then on (watchService), so its end is
// recorded after its start. A tree whose end began meanwhile has the new
// service stopped at once, and a running row the ledger refuses stops it
// and reaps it: either fails the tree at the service, whose end the
// tree's end records. A command that cannot start is the service's
// failure, its row finished with why. The error is the failure's words.
func (e *Engine) startService(ctx context.Context, r *run, a *attempt, t *tree, ch Check) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s was not started: %w", ch.ID, err)
	}
	e.mu.Lock()
	k := a.nextK(ch.ID)
	e.mu.Unlock()
	row := ledger.CheckRow{RunID: r.id, TaskID: a.t.def.ID, N: a.n, ID: ch.ID, K: k, Phase: ledger.PhaseService, Manifest: t.man.identity(), Ports: maps.Clone(t.ports), Tries: 1,
		Argv: slices.Clone(ch.Argv), Dir: t.cwd, Subject: t.commit, State: ledger.CheckStarted, Exit: -1, StartedAt: time.Now().UTC()}
	if err := e.transact(ctx, r, func() tx {
		out := ev("step.started", stepPayload(a, t, ledger.PhaseService, ch.ID, k, map[string]any{"manifest": row.Manifest}))
		out.more = func(ltx LedgerTx) error { return ltx.PutCheck(row) }
		return out
	}, giveUp); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s was not started: %w", ch.ID, ctx.Err())
		}
		return fmt.Errorf("the ledger could not record the service %s: %w", ch.ID, err)
	}
	if e.beforeServe != nil {
		e.beforeServe(ch.ID)
	}
	log := &bytes.Buffer{}
	ts := &treeService{ch: ch, row: row, log: log, started: make(chan struct{}), watched: make(chan struct{})}
	e.mu.Lock()
	ending := t.ending
	if !ending {
		t.starting = ts
	}
	e.mu.Unlock()
	if ending {
		return e.serviceNotRun(r, a, t, row, ch.ID+" was not started: its tree's end had begun")
	}
	var p proc.Process
	env := t.env(r.id)
	svc, err := e.exec.serve(r.teardown, proc.Spec{Argv: slices.Clone(ch.Argv), Dir: t.cwd, Allow: slices.Clone(env.allow), Inject: maps.Clone(env.inject),
		Head: checkHead, Tail: checkTail, Log: log, LogLimit: ledger.MaxLog, Started: func(started proc.Process) { p = started }})
	if err != nil {
		e.mu.Lock()
		t.starting = nil
		close(ts.started)
		e.mu.Unlock()
		return e.serviceNotRun(r, a, t, row, fmt.Sprintf("%s could not run: %s", ch.ID, session.CleanMessage(err.Error())))
	}
	running := row
	running.State, running.PID, running.Group, running.ProcessStartedAt = ledger.CheckRunning, p.PID, p.Group, p.StartedAt
	e.mu.Lock()
	ts.row, ts.svc = running, svc
	t.starting = nil
	t.services = append(t.services, ts)
	close(ts.started)
	// The tree's end, or the run's stop (which marked it stopping), began
	// while the executor made the process.
	ending = t.ending
	if ending && ts.stopping == "" {
		ts.stopping = "its tree's end had begun as it started"
	}
	e.mu.Unlock()
	if ending {
		go e.watchService(r, a, t, ts)
		svc.Stop(r.teardown)
		<-ts.watched
		return fmt.Errorf("%s was stopped as it started: its tree's end had begun", ch.ID)
	}
	err = e.transact(context.Background(), r, func() tx {
		out := ev("step.running", stepPayload(a, t, ledger.PhaseService, ch.ID, k, map[string]any{"pid": p.PID, "group": p.Group}))
		out.more = func(ltx LedgerTx) error { return ltx.PutCheck(running) }
		return out
	}, nil)
	// Watched from here on: an end that came already is recorded after its
	// running row, never written over by it.
	go e.watchService(r, a, t, ts)
	if err != nil {
		e.mu.Lock()
		if ts.stopping == "" && !ts.ended {
			ts.stopping = "the ledger could not record its start"
		}
		e.mu.Unlock()
		svc.Stop(r.teardown)
		<-ts.watched
		return fmt.Errorf("the ledger could not record the service %s: %w", ch.ID, err)
	}
	return nil
}

// serviceNotRun finishes the row of a service of t whose process was never
// made, or could not start, with words (step.finished, exit -1), and
// returns them as the service's failure.
func (e *Engine) serviceNotRun(r *run, a *attempt, t *tree, row ledger.CheckRow, words string) error {
	fin := row
	fin.State, fin.FinishedAt, fin.Error = ledger.CheckFinished, time.Now().UTC(), words
	_ = e.transact(context.Background(), r, func() tx {
		out := ev("step.finished", stepPayload(a, t, ledger.PhaseService, row.ID, row.K, map[string]any{"exit": -1, "error": words}))
		out.more = func(ltx LedgerTx) error { return ltx.PutCheck(fin) }
		return out
	}, nil)
	return errors.New(words)
}

// watchService waits for a service of t to end. One that nothing is ending
// (the tree's end, the run's stop and a start refused record theirs) ended
// on its own, and its end is recorded here: service.ended with its exit,
// its row finished with its words. Before the tree is ready that is the
// tree's failure, and the phase waiting on a probe is told at once (t.dead,
// t.died). After it, the tree no longer answers whole: in a worktree the
// attempt's preview is withdrawn in the same transaction, and the run log
// says so; the attempt's state is untouched (its session goes on, and the
// tree's end stops the rest when the attempt closes). One that exited 0 is
// told the foreground rule either way, in its row, its service.ended and
// the run log's line: with no ready probe to wait on, a daemonizer's exit
// comes once the tree is in use.
func (e *Engine) watchService(r *run, a *attempt, t *tree, ts *treeService) {
	defer close(ts.watched)
	<-ts.svc.Done()
	if e.beforeServiceEnd != nil {
		e.beforeServiceEnd(ts.ch.ID)
	}
	res := ts.svc.Result()
	err := e.transact(context.Background(), r, func() tx {
		if ts.stopping != "" || ts.ended {
			return tx{}
		}
		ts.ended = true
		var words, why string
		switch {
		case r.teardown.Err() != nil:
			// Killed with its context: the shutdown's cut-off (Draining),
			// not an exit of its own; the tree answers no more all the same.
			why = shutdownCut
			if t.kind == ledger.TreeWorktree {
				a.preview = ""
			}
			if !t.ready && t.dead == nil {
				t.dead = ts
				close(t.died)
			}
		case !t.ready:
			words, why = beforeReadyWords(ts), "it exited before the tree was ready"
			if t.dead == nil {
				t.dead = ts
				close(t.died)
			}
		default:
			words, why = fmt.Sprintf("%s %s while the tree was in use%s", ts.ch.ID, serviceExit(res), tailWords(lastOutput(res, checkTail))), "it exited while the tree was in use"
			rule := ""
			if res.Exit == 0 {
				rule = ": " + foregroundRule
				words, why = ts.ch.ID+" exited 0 while the tree was in use"+rule, "it exited 0 while the tree was in use"+rule
			}
			whose := ""
			if t.kind == ledger.TreeCheckout {
				whose = "the checkout's "
			}
			withdrawn := ""
			if t.kind == ledger.TreeWorktree && a.preview != "" {
				a.preview, withdrawn = "", "; the preview is withdrawn"
			}
			r.note(session.ActivityError, "task %s: attempt %d: %sservice %s %s while the tree was in use%s%s", a.t.def.ID, a.n, whose, ts.ch.ID, serviceExit(res), rule, withdrawn)
		}
		fin := ts.finished(res, words)
		out := ev("service.ended", serviceEndedPayload(a, t, ts, res, why))
		out.more = func(ltx LedgerTx) error { return ltx.PutCheck(fin) }
		return out
	}, nil)
	if err != nil {
		e.afterTransact(r, err)
	}
}

// finished is ts's row once its process has ended as res says, with the
// words of a failure ("" for a service stopped): its exit or signal, its
// timing, its output's tail and its log (at most ledger.MaxLog, its SHA-256
// over the bytes kept).
func (ts *treeService) finished(res proc.Result, words string) ledger.CheckRow {
	fin := ts.row
	fin.State, fin.FinishedAt = ledger.CheckFinished, time.Now().UTC()
	fin.Exit, fin.Signal, fin.Killed, fin.Truncated, fin.Total = res.Exit, res.Signal, res.Killed, res.Truncated, res.Total
	fin.Log = bytes.Clone(ts.log.Bytes())
	sum := sha256.Sum256(fin.Log)
	fin.LogSHA256 = hex.EncodeToString(sum[:])
	fin.Tail = string(lastOutput(res, checkTail))
	fin.Error = session.CleanMessage(words)
	return fin
}

// serviceEndedPayload is a service.ended event's payload: the service, its
// tree, its exit or signal and why it ended.
func serviceEndedPayload(a *attempt, t *tree, ts *treeService, res proc.Result, why string) map[string]any {
	return map[string]any{"task": a.t.def.ID, "attempt": a.n, "tree": t.kind, "id": ts.ch.ID, "k": ts.row.K, "exit": res.Exit, "signal": res.Signal, "why": session.CleanMessage(why)}
}

// probeTimeout and probeInterval are a probe's timeout and interval: its
// own, or their defaults.
func probeTimeout(p Probe) time.Duration {
	if p.TimeoutSeconds > 0 {
		return time.Duration(p.TimeoutSeconds) * time.Second
	}
	return defaultProbeTimeout * time.Second
}

func probeInterval(p Probe) time.Duration {
	if p.IntervalSeconds > 0 {
		return time.Duration(p.IntervalSeconds) * time.Second
	}
	return defaultProbeInterval * time.Second
}

// maxProbeLog bounds what a probe over HTTP logs of its tries (probeLog).
const maxProbeLog = 32 << 10

// probeLogEnd is the room a probe's log keeps for its end: the line that
// counts the tries left out and the last try's line, at most a try's words
// (session.CleanMessage's bound) and its number.
const probeLogEnd = 1 << 10

// probeLog is the log of a probe's tries over HTTP, a line each, held to
// limit bytes: the first tries' lines while they fit in limit less
// probeLogEnd, then the last try's line alone, after a line counting the
// tries left out between; so the try that passed, or the last one before
// the probe's end, is always in its log.
type probeLog struct {
	limit   int
	head    bytes.Buffer
	last    string
	skipped int
}

// add logs a try's line.
func (l *probeLog) add(line string) {
	if l.last == "" && l.head.Len()+len(line) <= l.limit-probeLogEnd {
		l.head.WriteString(line)
		return
	}
	if l.last != "" {
		l.skipped++
	}
	l.last = line
}

// bytes is the log as kept: the first lines, the count of those left out,
// the last line, at most limit bytes.
func (l *probeLog) bytes() []byte {
	out := bytes.Clone(l.head.Bytes())
	if l.skipped > 0 {
		out = fmt.Appendf(out, "… %s not logged …\n", tryCount(l.skipped))
	}
	last := l.last
	if room := l.limit - len(out); len(last) > room {
		last = last[:max(room, 0)]
	}
	return append(out, last...)
}

// errServiceExited is why a probe's try was cut: a service of its tree
// exited (t.died).
var errServiceExited = errors.New("a service of the tree exited")

// runProbe runs one of t's ready probes until it passes, its receipt one row
// of phase ready with its tries counted, put started before the first try
// (step.started) and finished after the last (step.finished): a command
// (argv) run again (treeExec.run) every interval until it exits 0, or the
// URL fetched (treeExec.probe: one GET, no redirect followed) until it
// answers under 300. It fails at its timeout with the last try's output
// as its words. A service of the tree that exits meanwhile fails the tree
// at it at once ("service <id>"), a try under way cut, and the probe's row
// says so; one that had exited before its first try leaves the row with
// no try and no exit (tries 0, exit -1). It returns the stage and words of
// a failure.
func (e *Engine) runProbe(ctx context.Context, r *run, a *attempt, t *tree, p Probe) (string, error) {
	if err := ctx.Err(); err != nil {
		return "ready " + p.ID, fmt.Errorf("%s was not run: %w", p.ID, err)
	}
	e.mu.Lock()
	k := a.nextK(p.ID)
	e.mu.Unlock()
	row := ledger.CheckRow{RunID: r.id, TaskID: a.t.def.ID, N: a.n, ID: p.ID, K: k, Phase: ledger.PhaseReady, Manifest: t.man.identity(), Ports: maps.Clone(t.ports), Tries: 0,
		Argv: slices.Clone(p.Argv), URL: p.URL, Dir: t.cwd, Subject: t.commit, State: ledger.CheckStarted, Exit: -1, StartedAt: time.Now().UTC()}
	if err := e.transact(ctx, r, func() tx {
		out := ev("step.started", stepPayload(a, t, ledger.PhaseReady, p.ID, k, map[string]any{"manifest": row.Manifest}))
		out.more = func(ltx LedgerTx) error { return ltx.PutCheck(row) }
		return out
	}, giveUp); err != nil {
		if ctx.Err() != nil {
			return "ready " + p.ID, fmt.Errorf("%s was not run: %w", p.ID, ctx.Err())
		}
		return "ready " + p.ID, fmt.Errorf("the ledger could not record the probe %s: %w", p.ID, err)
	}
	timeout, interval := probeTimeout(p), probeInterval(p)
	deadline := time.Now().Add(timeout)
	// A try is cut once a service of the tree has exited (t.died): the tree
	// has failed at that service, and the try's answer would say nothing.
	tctx, cut := context.WithCancelCause(ctx)
	defer cut(nil)
	go func() {
		select {
		case <-t.died:
			cut(errServiceExited)
		case <-tctx.Done():
		}
	}()
	pctx, cancel := context.WithDeadline(tctx, deadline)
	defer cancel()
	env := t.env(r.id)
	var output bytes.Buffer
	lines := probeLog{limit: maxProbeLog}
	var last proc.Result
	var lastWords string
	tries, passed := 0, false
	var dead *treeService
	for {
		if dead = e.deadService(t); dead != nil {
			break
		}
		tries++
		if len(p.Argv) > 0 {
			output.Reset()
			last = e.runCommand(tctx, t.cwd, p.Argv, max(time.Until(deadline), time.Millisecond), env, &output, nil)
			passed = last.Exit == 0 && last.Err == "" && !last.TimedOut && !last.Canceled
			if !passed {
				lastWords = probeTryWords(last)
			}
		} else {
			status, err := e.exec.probe(pctx, p.URL, interval)
			passed = err == nil && status < 300
			line := fmt.Sprintf("try %d: answered %d %s\n", tries, status, http.StatusText(status))
			switch {
			case err != nil && errors.Is(context.Cause(tctx), errServiceExited):
				line = fmt.Sprintf("try %d: cut: %s\n", tries, errServiceExited)
			case err != nil && pctx.Err() != nil && lastWords != "":
				// Cut by the probe's own end: the try before it says why.
				line = fmt.Sprintf("try %d: cut at the timeout\n", tries)
			case err != nil:
				lastWords = session.CleanMessage(err.Error())
				line = fmt.Sprintf("try %d: %s\n", tries, lastWords)
			case !passed:
				lastWords = fmt.Sprintf("answered %d %s", status, http.StatusText(status))
			}
			lines.add(line)
		}
		if passed || ctx.Err() != nil || pctx.Err() != nil || !time.Now().Before(deadline) {
			break
		}
		wait := time.NewTimer(min(interval, time.Until(deadline)))
		select {
		case <-wait.C:
		case <-pctx.Done():
		case <-t.died:
		}
		wait.Stop()
		if dead = e.deadService(t); dead != nil {
			break
		}
		if ctx.Err() != nil || pctx.Err() != nil || !time.Now().Before(deadline) {
			// No try starts at the timeout: the last one's words stand.
			break
		}
	}
	if dead == nil && !passed {
		// A service that exited under the last try, which it cut or which
		// ran on to the probe's end, is why the probe did not pass.
		dead = e.deadService(t)
	}
	fin := row
	fin.State, fin.FinishedAt, fin.Tries = ledger.CheckFinished, time.Now().UTC(), tries
	switch {
	case len(p.Argv) > 0 && tries > 0:
		fin.PID, fin.Group, fin.ProcessStartedAt, fin.Exit, fin.Signal, fin.TimedOut, fin.Killed = last.Process.PID, last.Process.Group, last.Process.StartedAt, last.Exit, last.Signal, last.TimedOut, last.Killed
		fin.Truncated, fin.Total, fin.Tail = last.Truncated, last.Total, string(lastOutput(last, checkTail))
		fin.Log = bytes.Clone(output.Bytes())
	case len(p.Argv) == 0:
		if passed {
			fin.Exit = 0
		}
		fin.Log = lines.bytes()
	}
	sum := sha256.Sum256(fin.Log)
	fin.LogSHA256 = hex.EncodeToString(sum[:])
	var stage, words string
	interrupted := false
	switch {
	case dead != nil:
		stage, words = "service "+dead.ch.ID, beforeReadyWords(dead)
	case ctx.Err() != nil && !passed:
		interrupted = true
		stage, words = "ready "+p.ID, e.interruptedWhy(r, a, Check{ID: p.ID})
		fin.State = ledger.CheckInterrupted
	case !passed:
		stage, words = "ready "+p.ID, fmt.Sprintf("%s did not pass within %s (%s): %s", p.ID, timeout, tryCount(tries), lastWords)
	}
	fin.Error = session.CleanMessage(words)
	if err := e.transact(context.Background(), r, func() tx {
		out := ev("step.finished", stepPayload(a, t, ledger.PhaseReady, p.ID, k, map[string]any{"exit": fin.Exit, "tries": fin.Tries, "duration": fin.FinishedAt.Sub(fin.StartedAt).String(),
			"error": fin.Error, "interrupted": interrupted}))
		out.more = func(ltx LedgerTx) error { return ltx.PutCheck(fin) }
		return out
	}, nil); err != nil && words == "" {
		return "ready " + p.ID, fmt.Errorf("the ledger could not record the probe %s: %w", p.ID, err)
	}
	if words != "" {
		return stage, errors.New(fin.Error)
	}
	return "", nil
}

// probeTryWords says why a try of a probe's command did not pass.
func probeTryWords(res proc.Result) string {
	switch {
	case res.Err != "" && !res.TimedOut && !res.Canceled:
		return "could not run: " + session.CleanMessage(res.Err)
	case res.TimedOut:
		return "its last try was still running at the timeout" + tailWords(lastOutput(res, checkTail))
	case res.Canceled:
		return "cancelled"
	case res.Signal != "":
		return fmt.Sprintf("ended by a signal (%s)%s", res.Signal, tailWords(lastOutput(res, checkTail)))
	}
	return fmt.Sprintf("exited %d%s", res.Exit, tailWords(lastOutput(res, checkTail)))
}

// tryCount is n tries in words.
func tryCount(n int) string {
	if n == 1 {
		return "1 try"
	}
	return fmt.Sprintf("%d tries", n)
}

// readyWords says what the services and the probes of m did to the
// checkout as its ready phase ran, "" for nothing: the engine cannot tell
// a service's write from a probe's, so the words name them all.
func readyWords(m Manifest, moved bool, changed []string, err error) string {
	var who []string
	switch ids := checkIDs(m.Services); len(ids) {
	case 0:
	case 1:
		who = append(who, "the service "+ids[0]+" ran")
	default:
		who = append(who, "the services "+strings.Join(ids, ", ")+" ran")
	}
	var probes []string
	for _, p := range m.Ready {
		probes = append(probes, p.ID)
	}
	switch len(probes) {
	case 0:
	case 1:
		who = append(who, "the probe "+probes[0]+" was tried")
	default:
		who = append(who, "the probes "+strings.Join(probes, ", ")+" were tried")
	}
	while := strings.Join(who, " and ")
	switch {
	case err != nil:
		return fmt.Sprintf("the checkout could not be compared once %s: %s", while, session.CleanMessage(err.Error()))
	case moved:
		return "the checkout moved to another commit while " + while
	case len(changed) > 0:
		return fmt.Sprintf("tracked files changed while %s: %s", while, changedList(changed))
	}
	return ""
}

// checkIDs is the ids of checks, in order.
func checkIDs(checks []Check) []string {
	var ids []string
	for _, ch := range checks {
		ids = append(ids, ch.ID)
	}
	return ids
}

// treeReady records t usable (tree.ready, its row ready), before the launch
// or the first check rests on it. In a worktree with a preview, the
// attempt's preview is the tree's URL from this transaction on, until the
// attempt's close or a service of the worktree exiting on its own clears
// it. A service that exited on its own before this fails the tree at it
// instead, its watcher there yet or not (exited); an attempt closed
// meanwhile records nothing. It returns the stage and words of a failure.
func (e *Engine) treeReady(ctx context.Context, r *run, a *attempt, t *tree) (string, error) {
	var dead *treeService
	closed := false
	err := e.transact(ctx, r, func() tx {
		if !a.closedAt.IsZero() {
			closed = true
			return tx{}
		}
		if dead = t.exited(); dead != nil {
			return tx{}
		}
		t.ready = true
		if t.kind == ledger.TreeWorktree {
			a.preview = t.preview
		}
		out := ev("tree.ready", map[string]any{"tree": t.kind, "task": a.t.def.ID, "attempt": a.n, "preview": t.preview})
		row := t.row(r.id, ledger.TreeReady)
		out.more = func(ltx LedgerTx) error { return ltx.PutTree(row) }
		return out
	}, func() {
		t.ready = false
		if t.kind == ledger.TreeWorktree {
			a.preview = ""
		}
	})
	switch {
	case dead != nil:
		return "service " + dead.ch.ID, errors.New(beforeReadyWords(dead))
	case closed:
		return "ready", errAttemptClosed
	case err != nil && ctx.Err() != nil:
		return "ready", fmt.Errorf("the tree's readiness was not recorded: %w", ctx.Err())
	case err != nil:
		return "ready", fmt.Errorf("the ledger could not record the tree ready: %w", err)
	}
	return "", nil
}

// row is t's row, of the run runID, in the state given, as the ledger
// keeps it.
func (t *tree) row(runID, state string) ledger.TreeRow {
	return ledger.TreeRow{RunID: runID, TaskID: t.a.t.def.ID, N: t.a.n, Kind: t.kind, Dir: t.dir, Commit: t.commit, Manifest: t.man.identity(), Ports: maps.Clone(t.ports), Preview: t.preview, State: state}
}

// runStep runs one step of r's project manifest and keeps its receipt:
// with an attempt, a checks row of the phase in tree t (its k the
// attempt's next of the id), else a prerequisite's row of the run, in the
// crew's directory (its k the run's next of the id, gen its generation).
// The row is put started before the process (step.started), running with
// the pid once it exists (step.running) and finished with the exit, the
// timing, the head and tail, the log (at most ledger.MaxLog, its SHA-256
// over the bytes kept) and the words of its failure (step.finished). A
// setup step in a checkout is compared with the result before its finished
// row (it ran alone in the tree): a change is its failure, in its receipt
// as in the attempt's words. The error is those words, nil for a pass; a
// row the ledger refuses fails the step ("the ledger could not record the
// step"), a refused running row ending the process with its group first,
// so nothing runs that the ledger does not record (its row and event go
// with the next commit). A step whose context ended before it began is not
// run and puts no row; one whose context ends under it is killed with its
// group and recorded interrupted, with why. A prerequisite's first row
// marks the generation running, and its failure pauses the run's dispatch
// with its words, in the transaction of its finished row, when the
// generation is still the run's.
func (e *Engine) runStep(ctx context.Context, r *run, a *attempt, t *tree, phase string, ch Check, gen int) (proc.Result, error) {
	if err := ctx.Err(); err != nil {
		return proc.Result{Exit: -1, Canceled: true}, fmt.Errorf("%s was not run: %w", ch.ID, err)
	}
	man, dir, subject := r.baseMan, r.cwd, r.base
	env := t.env(r.id)
	if t != nil {
		man, dir, subject = t.man, t.cwd, t.commit
	} else {
		// A prerequisite: the base's env names, which its generation
		// approved before its first step.
		env.allow, _ = e.allowedEnv(man.m.Env)
	}
	e.mu.Lock()
	var k int
	if a != nil {
		k = a.nextK(ch.ID)
	} else {
		r.prereqK[ch.ID]++
		k = r.prereqK[ch.ID]
	}
	e.mu.Unlock()
	row := ledger.CheckRow{RunID: r.id, ID: ch.ID, K: k, Phase: phase, Manifest: man.identity(), Tries: 1, Argv: slices.Clone(ch.Argv), Dir: dir, Subject: subject, State: ledger.CheckStarted, Exit: -1, StartedAt: time.Now().UTC()}
	if t != nil {
		row.Ports = maps.Clone(t.ports)
	}
	payload := func(more map[string]any) map[string]any {
		p := map[string]any{"phase": phase, "id": ch.ID, "k": k}
		if a != nil {
			p["task"], p["attempt"], p["tree"] = a.t.def.ID, a.n, t.kind
		}
		for key, v := range more {
			p[key] = v
		}
		return p
	}
	if a != nil {
		row.TaskID, row.N = a.t.def.ID, a.n
	}
	put := func(c ledger.CheckRow) func(LedgerTx) error {
		if a != nil {
			return func(ltx LedgerTx) error { return ltx.PutCheck(c) }
		}
		p := prerequisiteRow(c)
		return func(ltx LedgerTx) error { return ltx.PutPrerequisite(p) }
	}
	// keep is the receipt as the run reports it, a prerequisite's. The
	// caller holds e.mu.
	keep := func(c ledger.CheckRow) {
		if a == nil {
			c.Log = nil
			r.prereqRows[ch.ID] = prereqReceipt{row: c, gen: gen}
		}
	}
	if err := e.transact(ctx, r, func() tx {
		if a == nil && gen == r.prereqGen && r.prereq == prereqPending {
			r.prereq = prereqRunning
		}
		keep(row)
		out := ev("step.started", payload(map[string]any{"manifest": row.Manifest}))
		out.more = put(row)
		return out
	}, giveUp); err != nil {
		if ctx.Err() != nil {
			return proc.Result{Exit: -1, Canceled: true}, fmt.Errorf("%s was not run: %w", ch.ID, ctx.Err())
		}
		return proc.Result{Exit: -1}, fmt.Errorf("the ledger could not record the step %s: %w", ch.ID, err)
	}
	var log bytes.Buffer
	// The step's own context: a running row the ledger refuses ends the
	// process under it (Started is called on this goroutine, before any
	// output is read).
	stepCtx, endStep := context.WithCancel(ctx)
	defer endStep()
	var refused error
	res := e.runCommand(stepCtx, dir, ch.Argv, stepTimeout(phase, ch), env, &log, func(p proc.Process) {
		running := row
		running.State, running.PID, running.Group, running.ProcessStartedAt = ledger.CheckRunning, p.PID, p.Group, p.StartedAt
		if err := e.transact(context.Background(), r, func() tx {
			keep(running)
			out := ev("step.running", payload(map[string]any{"pid": p.PID, "group": p.Group}))
			out.more = put(running)
			return out
		}, nil); err != nil {
			refused = err
			endStep()
		}
	})
	fin := row
	fin.FinishedAt = time.Now().UTC()
	fin.PID, fin.Group, fin.ProcessStartedAt = res.Process.PID, res.Process.Group, res.Process.StartedAt
	fin.Exit, fin.Signal, fin.TimedOut, fin.Killed = res.Exit, res.Signal, res.TimedOut, res.Killed
	fin.Truncated, fin.Total = res.Truncated, res.Total
	fin.Log = bytes.Clone(log.Bytes())
	sum := sha256.Sum256(fin.Log)
	fin.LogSHA256 = hex.EncodeToString(sum[:])
	fin.Tail = string(lastOutput(res, checkTail))
	fin.State = ledger.CheckFinished
	interrupted := ctx.Err() != nil
	var words string
	switch {
	case interrupted:
	case refused != nil:
		words = fmt.Sprintf("the ledger could not record the step %s: %v", ch.ID, refused)
	default:
		words = stepWords(phase, ch, res)
		if words == "" && t != nil && t.kind == ledger.TreeCheckout && phase == ledger.PhaseSetup {
			fin.Stable, words = checkoutAfter(ctx, t, phase, ch)
			interrupted = ctx.Err() != nil
		}
	}
	if interrupted {
		fin.State, fin.Stable = ledger.CheckInterrupted, false
		words = e.interruptedWhy(r, a, ch)
		if phase == ledger.PhaseCleanup {
			// Under the run's teardown, which only the shutdown's cut-off
			// ends (Draining).
			words = ch.ID + " was interrupted: " + shutdownCut
		}
	}
	fin.Error = session.CleanMessage(words)
	err := e.transact(context.Background(), r, func() tx {
		keep(fin)
		out := ev("step.finished", payload(map[string]any{"exit": res.Exit, "timedOut": res.TimedOut, "duration": res.Duration.String(), "error": fin.Error, "interrupted": interrupted}))
		if a == nil && !interrupted && words != "" && gen == r.prereqGen && !r.stopping && r.outcome == "" {
			r.prereq, r.prereqFailedGen = prereqFailed, gen
			out.events = append(out.events, e.pauseManifest(r, fmt.Sprintf("the prerequisite %s failed: %s", ch.ID, words))...)
		}
		out.more = put(fin)
		return out
	}, nil)
	switch {
	case interrupted:
		return res, stepInterrupted(fin.Error)
	case words != "":
		return res, errors.New(fin.Error)
	case err != nil:
		return res, fmt.Errorf("the ledger could not record the step %s: %w", ch.ID, err)
	}
	return res, nil
}

// errInterrupted is what errors.Is finds in runStep's error for a step its
// context ended under, the receipt it recorded saying interrupted
// (stepInterrupted), and in one its context had ended before it ran (not
// run, the context's error wrapped): a caller reads the step's own outcome
// from it, never its context again after the fact.
var errInterrupted = errors.New("interrupted")

// stepInterrupted is runStep's error for a step its context ended under:
// the receipt's words, errors.Is errInterrupted.
type stepInterrupted string

func (s stepInterrupted) Error() string        { return string(s) }
func (s stepInterrupted) Is(target error) bool { return target == errInterrupted }

// cutStep reports whether err, runStep's, says its step was cut by its
// context: interrupted under it, or not run because it had ended.
func cutStep(err error) bool {
	return errors.Is(err, errInterrupted) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// nextK is the next k of id on a: every row of the attempt takes the next
// number of its id, whatever its phase, so no row overwrites another of
// the same id. The caller holds e.mu.
func (a *attempt) nextK(id string) int {
	if a.ks == nil {
		a.ks = map[string]int{}
	}
	a.ks[id]++
	return a.ks[id]
}

// stepWords says why a step failed, "" for a pass.
func stepWords(phase string, ch Check, res proc.Result) string {
	switch {
	case res.Err != "" && !res.TimedOut && !res.Canceled:
		return fmt.Sprintf("%s could not run: %s", ch.ID, session.CleanMessage(res.Err))
	case res.TimedOut:
		return fmt.Sprintf("%s timed out after %s%s", ch.ID, stepTimeout(phase, ch), tailWords(lastOutput(res, checkTail)))
	case res.Canceled:
		return ch.ID + " was cancelled"
	case res.Exit != 0:
		return fmt.Sprintf("%s exited %d%s", ch.ID, res.Exit, tailWords(lastOutput(res, checkTail)))
	}
	return ""
}

// checkoutAfter compares the checkout t with its result after a step of
// phase that passed, alone in it: whether the checkout is as the result
// has it, and else the step's failure ("setup deps changed tracked files:
// …"), the words of its receipt and of the attempt alike.
func checkoutAfter(ctx context.Context, t *tree, phase string, ch Check) (stable bool, words string) {
	moved, changed, _, err := checkoutIntact(ctx, t.dir, t.commit)
	if w := intactWords(moved, changed, err); w != "" {
		return false, fmt.Sprintf("%s %s %s", phase, ch.ID, w)
	}
	return true, ""
}

// interruptedWhy says why a step's context ended under it: the run
// finished before a prerequisite ended, the run was stopped, or the
// attempt was cancelled or closed.
func (e *Engine) interruptedWhy(r *run, a *attempt, ch Check) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case a == nil && r.outcome != "":
		// The commit that finished the run ended the generation (a stop
		// after it found it ended).
		return fmt.Sprintf("%s was interrupted: the run finished before the prerequisite ended", ch.ID)
	case r.stopping || r.stoppedAt != nil:
		return fmt.Sprintf("%s was interrupted: the run was stopped", ch.ID)
	case a == nil:
		return fmt.Sprintf("%s was interrupted: a later run of the prerequisites took its place", ch.ID)
	case a.state == ledger.AttemptCancelled:
		return fmt.Sprintf("%s was interrupted: the attempt was cancelled", ch.ID)
	}
	return fmt.Sprintf("%s was interrupted: the attempt ended", ch.ID)
}

// prerequisiteRow is a prerequisite's receipt from the row runStep makes.
func prerequisiteRow(c ledger.CheckRow) ledger.PrerequisiteRow {
	return ledger.PrerequisiteRow{RunID: c.RunID, ID: c.ID, K: c.K, Manifest: c.Manifest, Error: c.Error, URL: c.URL, Argv: c.Argv, Dir: c.Dir, Subject: c.Subject,
		State: c.State, PID: c.PID, Group: c.Group, ProcessStartedAt: c.ProcessStartedAt, StartedAt: c.StartedAt, FinishedAt: c.FinishedAt, Exit: c.Exit, Signal: c.Signal,
		TimedOut: c.TimedOut, Killed: c.Killed, Stable: c.Stable, Truncated: c.Truncated, Total: c.Total, Log: c.Log, LogDropped: c.LogDropped, LogSHA256: c.LogSHA256, Tail: c.Tail}
}

// prereqReceipt is a prerequisite's latest receipt, as the run reports it,
// with the generation that made it.
type prereqReceipt struct {
	row ledger.CheckRow
	gen int
}

// hasPrerequisites reports whether the manifest has prerequisites to run
// before any tree: commands, or env names to check (allowedEnv).
func (rm resolvedManifest) hasPrerequisites() bool {
	return len(rm.m.Prerequisites) > 0 || len(rm.m.Env) > 0
}

// armPrerequisites sets r's prerequisites pending, to run once the
// transaction that arms them is committed (transact starts the
// generation), or passed when the run's manifest has none. The caller
// holds e.mu, in a transaction's change.
func (r *run) armPrerequisites() {
	if !r.baseMan.hasPrerequisites() {
		r.prereq = prereqPassed
		return
	}
	r.prereq, r.prereqStart = prereqPending, true
}

// rearmPrerequisites arms them again at an unpause, so a host fixed in
// between is seen, unless a generation is in flight (pending or running:
// its result stands), the run has none or it has finished. A generation
// whose last commit is in but whose goroutine has not ended yet starts the
// one armed here as it ends (prerequisites), one at a time. The caller
// holds e.mu, in a transaction's change.
func (r *run) rearmPrerequisites() {
	switch {
	case !r.baseMan.hasPrerequisites() || r.outcome != "":
	case r.prereq == prereqPassed || r.prereq == prereqFailed || r.prereq == prereqInterrupted:
		r.prereq, r.prereqStart = prereqPending, true
	}
}

// reopenPrerequisites arms them again as a retry or an acceptance reopens a
// finished run whose finishing commit ended a generation before its
// verdict, or before it began (interrupted; a continuation whose every
// task was carried ended is finished by the commit that arms its first):
// the pump dispatches only on passed, so the run would wait for good. A
// generation still ending as the run reopens runs them again itself as it
// ends (prerequisites); one that passed or failed stands. The caller holds
// e.mu, in a transaction's change.
func (r *run) reopenPrerequisites() {
	if r.prereq == prereqInterrupted && r.outcome == "" && !r.stopping {
		r.prereq, r.prereqStart = prereqPending, true
	}
}

// startPrerequisites starts a generation of r's prerequisites, armed and
// committed: one at a time (r.prereqs), none once the run has ended. One
// armed in a run that has ended by then (finished, as a continuation whose
// every task was carried ended is by the commit that arms it, or stopping)
// never begins: it is interrupted, none in flight, so nothing says it
// runs, and a retry that reopens the run arms it again
// (reopenPrerequisites); left pending, nothing would ever run it and the
// pump would wait for good (Codex's review of #422, round 3). The
// generation runs on a goroutine counted in r.starts, under a context of
// its own beneath r.ctx, which a commit that finishes the run cancels
// (settleRun). The caller holds e.mu.
func (e *Engine) startPrerequisites(r *run) {
	if r.prereqs > 0 || r.prereq != prereqPending {
		return
	}
	if r.stopping || r.ctx.Err() != nil || r.outcome != "" {
		r.prereq = prereqInterrupted
		return
	}
	r.prereqGen++
	gen := r.prereqGen
	ctx, cancel := context.WithCancel(r.ctx)
	r.prereqs++
	r.prereqCancel = cancel
	r.starts.Add(1)
	go e.prerequisites(ctx, cancel, r, gen)
}

// prerequisites runs r's prerequisites of generation gen: first the
// manifest's env names, each in the server's envPassthrough and set in its
// environment (allowedEnv), then its prerequisites' commands, in order, in
// the crew's directory, each a receipt (runStep) given the approved names;
// and records prerequisites.passed once every one passed, then dispatches.
// A name the server does not allow or the host does not have pauses the
// run's dispatch with its words, the second saying a restart fixes it (the
// server's environment is its start's, so an unpause alone finds it as it
// was), and no command runs; one command that fails pauses it with its
// words (runStep), and the rest do not run. A commit from a generation
// that is no longer the run's is dropped, its rows kept. The count of
// generations in flight drops once
// its last commit is in, so the run is held (idle) until then; a
// generation armed meanwhile (an unpause after this one's result) starts
// then. That end is taken under r.txMu, as a transaction's change is, so
// it reads the run as committed: never an unpause or a retry whose change
// is made and whose commit is under way, which the ledger may yet refuse
// and its undo put back. A generation that ends with no verdict (the
// commit that finished the run, or a stop, ended it under its steps) leaves
// them interrupted, or, in a run a retry or an acceptance has reopened
// since, pending, and starts them again.
func (e *Engine) prerequisites(ctx context.Context, cancel context.CancelFunc, r *run, gen int) {
	defer r.starts.Done()
	defer func() {
		cancel()
		if e.beforePrereqEnd != nil {
			e.beforePrereqEnd()
		}
		r.txMu.Lock()
		defer r.txMu.Unlock()
		e.mu.Lock()
		defer e.mu.Unlock()
		r.prereqs--
		if r.prereqGen == gen {
			r.prereqCancel = nil
			if r.prereq == prereqPending || r.prereq == prereqRunning {
				r.prereq = prereqInterrupted
				if r.outcome == "" && !r.stopping && r.ctx.Err() == nil {
					r.prereq = prereqPending
				}
			}
		}
		e.startPrerequisites(r)
		r.touch()
	}()
	if _, err := e.allowedEnv(r.baseMan.m.Env); err != nil {
		if ctx.Err() == nil {
			e.prerequisiteRefused(r, gen, "the prerequisites failed: "+err.Error())
		}
		return
	}
	for _, ch := range r.baseMan.m.Prerequisites {
		if _, err := e.runStep(ctx, r, nil, nil, ledger.PhasePrerequisite, ch, gen); err != nil {
			if ctx.Err() == nil {
				e.prerequisiteRefused(r, gen, fmt.Sprintf("the prerequisite %s failed: %v", ch.ID, err))
			}
			return
		}
	}
	passed := false
	err := e.transact(context.Background(), r, func() tx {
		if gen != r.prereqGen || r.stopping || ctx.Err() != nil {
			return tx{}
		}
		r.prereq, passed = prereqPassed, true
		r.note(session.ActivityStatus, "the prerequisites passed")
		return ev("prerequisites.passed", map[string]any{"manifest": r.baseMan.identity()})
	}, nil)
	if passed || err != nil {
		e.afterTransact(r, err)
	}
}

// prerequisiteRefused ends a generation that stopped with why but without
// its failure recorded with a step (an env name refused before any step,
// or a step's row the ledger refused): when it is still the run's, the
// prerequisites are failed and the dispatch pauses with why, so the run
// never waits on a generation that has ended, and an unpause runs them
// again. A
// generation whose failure the run took already (r.prereqFailedGen: its
// step's finished row took it, committed or ahead of a commit the ledger
// refused) is not failed again. An unpause since then may have armed
// the prerequisites again (pending, the generation still in flight), and
// failing it a second time would pause the run again over that unpause and
// leave the generation's end nothing armed to start (Codex's review of
// #422, round 4). The change is then empty, and its commit carries only
// what a refused one left for the next.
func (e *Engine) prerequisiteRefused(r *run, gen int, why string) {
	if e.beforePrereqRefused != nil {
		e.beforePrereqRefused()
	}
	terr := e.transact(context.Background(), r, func() tx {
		if gen != r.prereqGen || r.prereqFailedGen == gen || r.stopping || r.outcome != "" {
			return tx{}
		}
		r.prereq, r.prereqFailedGen = prereqFailed, gen
		return tx{events: e.pauseManifest(r, why)}
	}, nil)
	if terr != nil {
		e.afterTransact(r, terr)
	}
}

// pauseManifest pauses r's dispatch for a project manifest's failure: the
// pause and its words on the run (pausedWhy, written on its row with the
// pause), in the run log ("scheduling paused: <why>") and in the event. The
// caller holds e.mu, in a transaction's change.
func (e *Engine) pauseManifest(r *run, why string) []event {
	why = session.CleanMessage(why)
	if !r.paused || r.pausedWhy != why {
		r.paused, r.pausedWhy = true, why
		r.markRun()
	}
	r.note(session.ActivityError, "scheduling paused: %s", why)
	return []event{{"run.paused", map[string]any{"paused": true, "why": why}}}
}

// unusable is the task's last attempt when its worktree could not be made
// usable (a manifest step failed there, or the manifest could not be
// resolved): not counted, and the next attempt starts at next, where it
// started, or, when it refused a checkpoint the agent saved (checkpoint,
// on branch, saved by attempt checkpointN), where the checkpointed attempt
// started. prev is the counted attempt before it as its own task file
// named it, and prevResult that attempt's result, for a task file that
// has no counted attempt of its own run to name (a continuation's first).
type unusable struct {
	n                  int
	stage, err         string
	next               string
	checkpoint, branch string
	checkpointN        int
	prev               *TaskPrevious
	prevResult         string
}

// failUnusable closes a, whose worktree could not be made usable at stage,
// as failed and uncounted: the task gets one more attempt (extra) and is
// ready with why, and the next attempt starts where a started (unusable).
// A failure at a checkpoint the agent saved (a.start the task's checkpoint
// commit, one the agent made) refuses the checkpoint as a refused result
// is refused: the task's checkpoint is dropped, the next attempt starts
// where the checkpointed attempt started, and the run goes on. Any other
// pauses the run's dispatch with the same words (pauseManifest): a missing
// tool or a dead registry needs a person, and the task is not spent on
// it. The caller holds e.mu, in a transaction's change; a is open.
func (e *Engine) failUnusable(r *run, a *attempt, stage, errText string) []event {
	t := a.t
	cp := t.checkpoint
	// A package or an agent that is not the approved one is no fault of the
	// checkpoint's tree: the run pauses whatever the attempt started at.
	atCheckpoint := !launchStage(stage) && cp != nil && t.kept(cp.n) && cp.commit != "" && cp.commit != cp.start && a.start == cp.commit
	u := &unusable{n: a.n, stage: stage, err: errText, next: a.start, prev: previousOf(t, a)}
	if old := t.unusableBefore(a); old != nil {
		u.prevResult = old.prevResult
	}
	for _, x := range t.attempts {
		if x.n < a.n && t.kept(x.n) && !x.uncounted {
			u.prevResult = x.result
		}
	}
	e.endAttempt(r, a, ledger.AttemptFailed, stage, errText)
	a.uncounted = true
	e.withdrawBlocker(a, t.blocker)
	t.blocker = nil
	t.extra++
	payload := map[string]any{"task": t.def.ID, "attempt": a.n, "stage": stage, "error": errText, "state": a.state, "uncounted": true}
	why := fmt.Sprintf("attempt %d could not be made usable at %s: %s", a.n, stage, errText)
	switch stage {
	case "package":
		why = fmt.Sprintf("attempt %d could not be handed its ticket's package: %s", a.n, errText)
	case "agent":
		why = fmt.Sprintf("attempt %d could not start its agent: %s", a.n, errText)
	}
	if atCheckpoint {
		u.checkpoint, u.branch, u.checkpointN, u.next = cp.commit, cp.branch, cp.n, cp.start
		t.checkpoint = nil
		payload["checkpoint"] = cp.commit
		why = fmt.Sprintf("attempt %d could not be made usable at the checkpoint %s, at %s: %s", a.n, short(cp.commit), stage, errText)
	}
	t.unusable = u
	t.state, t.reason = ledger.TaskReady, why
	r.markTask(t)
	r.ready()
	r.note(session.ActivityStatus, "task %s: %s", t.def.ID, why)
	events := []event{{"attempt.failed", payload}}
	if !atCheckpoint {
		events = append(events, e.pauseManifest(r, why)...)
	}
	return events
}

// treeEnd is a tree handed to endTree, and why it ends.
type treeEnd struct {
	t   *tree
	why string
}

// handTrees queues a's trees for endTree, each once, as a's close is
// made: the commit that makes the close durable starts them (transact).
// A worktree whose session the close stops (a cancel, a withdrawal, a stop,
// a failure that stops it) waits for that session's end (t.session). One
// closed while its session is being launched has none yet: the launch
// holds the tree until it records the session it made, which it stops and
// names on the tree then (dispatch). The caller holds e.mu, in a
// transaction's change.
func (e *Engine) handTrees(r *run, a *attempt) {
	stopping := a.sessionID != "" && (r.stopping || slices.Contains(r.pendingStops, a.sessionID))
	why := fmt.Sprintf("attempt %d %s%s", a.n, a.state, suffix(a.stage, a.err))
	for _, t := range a.trees {
		if t.handed {
			continue
		}
		t.handed = true
		if stopping && t.kind == ledger.TreeWorktree {
			t.session = a.sessionID
		}
		r.pendingTrees = append(r.pendingTrees, treeEnd{t: t, why: why})
	}
}

// startTrees starts the ends a committed close handed over, each on a
// goroutine of its own counted in r.trees. The caller holds e.mu.
func (e *Engine) startTrees(r *run) {
	for _, h := range r.pendingTrees {
		r.trees.add()
		go e.endTree(r, h)
	}
	r.pendingTrees = nil
}

// endTree ends a tree, once, after its attempt's close is committed: under
// r.teardown, never the attempt's context (ended by then), it marks the
// tree ending, waits for it to be quiet (the phase, check or launch that
// held it returned; then the session being stopped ended, bounded by
// stopTimeout, read once the hold is let go: a launch the close came
// during names its session on the tree only then), stops its services
// (endServices: SIGTERM to each one's group, SIGKILL after the grace or at
// the shutdown's cut-off, each one's end recorded), runs its cleanup steps
// (cleanTree), then records tree.ended with the
// tree's row ended (with the first cleanup step that failed, and whether the
// shutdown's cut-off came before every one had run), its ports let go once
// that is committed (releaseEnded), so a number is the next tree's only
// once the ledger has its tree.ended. The rows are committed under a
// context of their own, so the cut-off, which ends r.teardown, leaves them
// their reserve (Draining). A refused commit is kept for the run's next
// (transact), the ports with it, and a row left not ended is concluded by
// the recovery at the next start, its cleanup not run.
func (e *Engine) endTree(r *run, h treeEnd) {
	t := h.t
	defer func() {
		e.mu.Lock()
		r.trees.done()
		r.touch()
		e.mu.Unlock()
	}()
	e.mu.Lock()
	t.ending = true
	e.mu.Unlock()
	t.busy.Wait()
	e.mu.Lock()
	sid := t.session
	e.mu.Unlock()
	if sid != "" {
		if l, ok := e.lookup(sid); ok {
			select {
			case <-l.Ended():
			case <-time.After(stopTimeout):
			case <-r.teardown.Done():
			}
		}
	}
	e.endServices(r.teardown, r, t, h.why)
	if e.beforeTreeEnd != nil {
		e.beforeTreeEnd(t.kind)
	}
	failed, cut := e.cleanTree(r, t)
	a := t.a
	_ = e.transact(context.Background(), r, func() tx {
		if t.ended {
			return tx{}
		}
		t.ended = true
		r.pendingReleases = append(r.pendingReleases, t)
		payload := map[string]any{"tree": t.kind, "task": a.t.def.ID, "attempt": a.n, "why": h.why}
		if failed != "" {
			payload["cleanupFailed"] = failed
		}
		if cut {
			payload["interrupted"] = true
		}
		out := ev("tree.ended", payload)
		row := t.row(r.id, ledger.TreeEnded)
		row.Why, row.Interrupted, row.EndedAt = session.CleanMessage(h.why), cut, time.Now().UTC()
		out.more = func(ltx LedgerTx) error { return ltx.PutTree(row) }
		return out
	}, nil)
}

// shutdownCut is why the server's shutdown cut-off (Draining) ended what a
// tree's teardown ran: a service still running, a cleanup step under way.
const shutdownCut = "the server's shutdown cut it"

// cleanTree runs t's cleanup steps, in order, once its services are
// stopped: each a receipt (runStep, phase cleanup) in the tree's working
// directory, with the tree's names, under r.teardown and bounded by its own
// timeout (60 s when it gives none). Every step runs whatever the one
// before it did, each undoing something of its own. A step that fails is a
// receipt and a line in the run log ("task build: attempt 2: cleanup db
// exited 1: …"), never a change to the attempt's state, which its close
// decided. The shutdown's cut-off ends r.teardown: the step under way is
// killed with its group and recorded interrupted (proc.Run returns once
// its group is reaped), and no step starts after it, which the run log
// says. It returns the first step that failed, "" for none, and whether
// the cut came before every step had run.
func (e *Engine) cleanTree(r *run, t *tree) (failed string, cut bool) {
	a := t.a
	whose := ""
	if t.kind == ledger.TreeCheckout {
		whose = "the checkout's "
	}
	steps := t.man.m.Cleanup
	for i, ch := range steps {
		if r.teardown.Err() != nil {
			e.mu.Lock()
			r.note(session.ActivityError, "task %s: attempt %d: %scleanup %s not run: %s", a.t.def.ID, a.n, whose, strings.Join(checkIDs(steps[i:]), ", "), shutdownCut)
			e.mu.Unlock()
			return failed, true
		}
		_, err := e.runStep(r.teardown, r, a, t, ledger.PhaseCleanup, ch, 0)
		if err == nil {
			continue
		}
		words := err.Error()
		if !strings.HasPrefix(words, ch.ID+" ") {
			words = ch.ID + ": " + words
		}
		e.mu.Lock()
		r.note(session.ActivityError, "task %s: attempt %d: %scleanup %s", a.t.def.ID, a.n, whose, words)
		e.mu.Unlock()
		// Cut or failed by what its receipt says, never by the teardown
		// read again now: a step that failed on its own just before the
		// cut-off is a failure (the independent review of #498).
		if cutStep(err) {
			cut = true
			continue // the next turn names what was not run
		}
		if failed == "" {
			failed = ch.ID
		}
	}
	return failed, cut
}

// stopFor marks ts as being ended for why (stopping), unless something
// ends it already, its end is recorded, or its process has ended on its
// own: that end is its watcher's to record, as its own (watchService),
// though the watcher has not got to it yet. The caller holds e.mu.
func (ts *treeService) stopFor(why string) {
	if ts.ended || ts.stopping != "" {
		return
	}
	select {
	case <-ts.svc.Done():
	default:
		ts.stopping = why
	}
}

// endServices stops t's services, each one nothing is ending yet marked
// stopping for why (stopFor), all at once under ctx (Stop: SIGTERM to the
// group, SIGKILL after proc.ServeGrace or once ctx ends), waits for their
// watchers, and records the end of each whose end is not recorded yet:
// service.ended with the exit or signal its stop gave it (the run's stop's,
// for one it stopped first) and why, its row finished, in one transaction.
// Then the tree lets go of them, their handles and their output.
func (e *Engine) endServices(ctx context.Context, r *run, t *tree, why string) {
	e.mu.Lock()
	svcs := slices.Clone(t.services)
	for _, ts := range svcs {
		ts.stopFor(why)
	}
	e.mu.Unlock()
	if len(svcs) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, ts := range svcs {
		wg.Go(func() { ts.svc.Stop(ctx) })
	}
	wg.Wait()
	for _, ts := range svcs {
		<-ts.watched
	}
	a := t.a
	err := e.transact(context.Background(), r, func() tx {
		var out tx
		var rows []ledger.CheckRow
		for _, ts := range svcs {
			if ts.ended {
				continue
			}
			ts.ended = true
			res := ts.svc.Result()
			rows = append(rows, ts.finished(res, ""))
			out.events = append(out.events, event{"service.ended", serviceEndedPayload(a, t, ts, res, ts.stopping)})
		}
		if len(rows) > 0 {
			out.more = func(ltx LedgerTx) error {
				for _, row := range rows {
					if err := ltx.PutCheck(row); err != nil {
						return err
					}
				}
				return nil
			}
		}
		return out
	}, nil)
	if err != nil {
		e.afterTransact(r, err)
	}
	// Their ends recorded, or kept for the run's next commit in rows that
	// carry copies of their logs (finished), and their watchers returned:
	// the tree lets go of them (Codex's review of #441, round 2). An ended
	// tree stays its attempt's as long as the engine holds the run, and
	// would keep each service's log, output and handle with it; nothing
	// reads them now, no phase or check holding a tree whose end has
	// begun.
	e.mu.Lock()
	t.services, t.dead = nil, nil
	e.mu.Unlock()
}

// stopServices ends the services of r's live trees, as a stop does before
// it records the run stopped: each tree not ended is marked ending first,
// so no service starts on it after (startService), each service nothing
// ends yet is marked stopping for the run's stop (stopFor), and so is the
// one whose start is under way (t.starting), its process made or being
// made and not yet handed over; their stops follow under the stop's
// context, beside the sessions', the one starting's once its start hands
// it over (stopService), so the stop returns under no process of its
// trees, whatever its context says (Codex's review of #441, round 3).
// Their ends are recorded by their trees' ends (endServices), once
// recordStop hands the trees over; a stop the ledger refuses hands none,
// and leaves no service running all the same. It returns the services to
// stop. The caller holds e.mu.
func (r *run) stopServices() []*treeService {
	var svcs []*treeService
	for _, tk := range r.tasks {
		for _, a := range tk.attempts {
			for _, t := range a.trees {
				if t.ended {
					continue
				}
				t.ending = true
				for _, ts := range t.services {
					ts.stopFor("the run was stopped")
					svcs = append(svcs, ts)
				}
				if ts := t.starting; ts != nil {
					// Nothing else ends it: it has no handle yet.
					if ts.stopping == "" {
						ts.stopping = "the run was stopped"
					}
					svcs = append(svcs, ts)
				}
			}
		}
	}
	return svcs
}

// stopService stops ts for the run's stop under ctx (Stop: SIGTERM to its
// group, SIGKILL after the grace or once ctx ends) and returns once its
// process is reaped. One whose start is under way (t.starting) is waited
// for until the executor hands it over, whatever ctx says: the executor
// returns once the process exists, and a stop that returned before then
// would leave the process to its start, which stops it alone after a
// whole grace (Codex's review of #441, round 3). One whose start failed
// has nothing to stop.
func (e *Engine) stopService(ctx context.Context, ts *treeService) {
	select {
	case <-ts.started:
	default:
		if e.awaitingStart != nil {
			e.awaitingStart(ts.ch.ID)
		}
		<-ts.started
	}
	if ts.svc != nil {
		ts.svc.Stop(ctx)
	}
}

// teardownReserve is what the server's shutdown keeps after the
// teardown's cut-off (Draining): proc.WaitDelay for the reaping of what
// the cut killed, and a second for the rows that record it.
const teardownReserve = proc.WaitDelay + time.Second

// Draining arms the teardown's cut-off for the server's shutdown, before
// the shutdown stops anything: ctx's deadline less teardownReserve, or
// ctx's end when it has no deadline. At the cut-off the teardown context
// of every managed run, held or made since, ends (each run's r.teardown is
// made beneath the engine's root): a service still running is killed with
// its group, and what a tree's end waits on under it gives way. So every
// tree's end is recorded, and every stop returns, before the shutdown's
// deadline, with the reserve left for the rows: a cut reached only after
// the stops would hold each stop to the whole deadline behind one slow
// service, and leave the rows no time.
func (e *Engine) Draining(ctx context.Context) {
	if ctx.Done() == nil {
		return // a context that never ends: no cut-off
	}
	var cut context.Context
	var cancel context.CancelFunc
	if d, ok := ctx.Deadline(); ok {
		cut, cancel = context.WithDeadline(ctx, d.Add(-teardownReserve))
	} else {
		cut, cancel = context.WithCancel(ctx)
	}
	go func() {
		defer cancel()
		<-cut.Done()
		if e.teardownCut != nil {
			e.teardownCut()
		}
	}()
}

// Drain waits, as long as ctx allows, for the trees being ended of every
// run the engine holds, a run stopped before the shutdown among them (one
// whose stop returned early, cleaning above zero): the server's shutdown
// calls it after it has stopped its runs, and before the ledger closes, so
// a tree's end is recorded in the ledger it ends in.
func (e *Engine) Drain(ctx context.Context) {
	for {
		e.mu.Lock()
		var idle <-chan struct{}
		for _, r := range e.runs {
			if r.trees.n > 0 {
				idle = r.trees.idle()
				break
			}
		}
		e.mu.Unlock()
		if idle == nil {
			return
		}
		select {
		case <-idle:
		case <-ctx.Done():
			return
		}
	}
}

// RunManifest is the run's project manifest as the run reports it: where
// it comes from (the crew's at its revision, or the file at the base, its
// blob) and how its prerequisites go.
type RunManifest struct {
	Source        string              `json:"source"`
	Revision      int64               `json:"revision,omitempty"`
	Digest        string              `json:"digest,omitempty"`
	Prerequisites []PrerequisiteState `json:"prerequisites,omitempty"`
}

// PrerequisiteState is a prerequisite of the run as it goes: its latest
// run's k (0 before its first), and its state: pending (not run in the
// generation), running (while its generation runs), passed, failed or
// interrupted, with its exit, its words and how long it took, in
// milliseconds.
type PrerequisiteState struct {
	ID       string `json:"id"`
	K        int    `json:"k,omitempty"`
	State    string `json:"state"`
	Exit     *int   `json:"exit,omitempty"`
	Error    string `json:"error,omitempty"`
	Duration int64  `json:"duration,omitempty"`
}

// manifestState is r's manifest as reported, nil without one. The caller
// holds e.mu.
func (r *run) manifestState() *RunManifest {
	if !r.hasManifest() {
		return nil
	}
	rm := &RunManifest{Source: r.baseMan.source, Revision: r.baseMan.revision, Digest: r.baseMan.digest}
	inFlight := r.prereq == prereqPending || r.prereq == prereqRunning
	for _, ch := range r.baseMan.m.Prerequisites {
		ps := PrerequisiteState{ID: ch.ID, State: "pending"}
		rc, ok := r.prereqRows[ch.ID]
		if ok {
			ps.K = rc.row.K
		}
		switch {
		case ok && rc.gen == r.prereqGen && rc.row.State == ledger.CheckFinished:
			exit := rc.row.Exit
			ps.Exit, ps.Error, ps.Duration = &exit, rc.row.Error, rc.row.FinishedAt.Sub(rc.row.StartedAt).Milliseconds()
			ps.State = "passed"
			if rc.row.Error != "" {
				ps.State = "failed"
			}
		case ok && rc.gen == r.prereqGen && rc.row.State == ledger.CheckInterrupted:
			ps.State, ps.Error = "interrupted", rc.row.Error
		case inFlight:
			ps.State = "running"
		}
		rm.Prerequisites = append(rm.Prerequisites, ps)
	}
	return rm
}

// StepState is a receipt as the task's read and the receipt routes report
// it: the row whole but for its log, whose size and SHA-256 it says. A
// probe over HTTP has its URL for a command; exit and finishedAt come once
// it ended; duration is in milliseconds.
type StepState struct {
	Phase      string         `json:"phase"`
	ID         string         `json:"id"`
	K          int            `json:"k"`
	State      string         `json:"state"`
	Argv       []string       `json:"argv,omitempty"`
	URL        string         `json:"url,omitempty"`
	Dir        string         `json:"dir,omitempty"`
	Subject    string         `json:"subject,omitempty"`
	Manifest   string         `json:"manifest,omitempty"`
	Exit       *int           `json:"exit,omitempty"`
	TimedOut   bool           `json:"timedOut,omitempty"`
	Duration   int64          `json:"duration,omitempty"`
	Error      string         `json:"error,omitempty"`
	Ports      map[string]int `json:"ports,omitempty"`
	Tries      int            `json:"tries"`
	StartedAt  time.Time      `json:"startedAt"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
	LogBytes   int64          `json:"logBytes"`
	LogDropped bool           `json:"logDropped,omitempty"`
	LogSHA256  string         `json:"logSha256,omitempty"`
}

// StepStates is an attempt's receipts as reported, by when each started.
func StepStates(rows []ledger.CheckRow) []StepState {
	out := make([]StepState, 0, len(rows))
	for _, c := range rows {
		out = append(out, stepState(c.Phase, c.ID, c.K, c.State, c.Argv, c.URL, c.Dir, c.Subject, c.Manifest, c.Exit, c.TimedOut, c.Error, c.Ports, c.Tries, c.StartedAt, c.FinishedAt, c.LogBytes, c.LogDropped, c.LogSHA256))
	}
	sortSteps(out)
	return out
}

// PrerequisiteStates is a run's prerequisites' receipts as reported, by
// when each started.
func PrerequisiteStates(rows []ledger.PrerequisiteRow) []StepState {
	out := make([]StepState, 0, len(rows))
	for _, c := range rows {
		out = append(out, stepState(ledger.PhasePrerequisite, c.ID, c.K, c.State, c.Argv, c.URL, c.Dir, c.Subject, c.Manifest, c.Exit, c.TimedOut, c.Error, nil, 1, c.StartedAt, c.FinishedAt, c.LogBytes, c.LogDropped, c.LogSHA256))
	}
	sortSteps(out)
	return out
}

func stepState(phase, id string, k int, state string, argv []string, url, dir, subject, manifest string, exit int, timedOut bool, errText string, ports map[string]int, tries int,
	started, finished time.Time, logBytes int64, dropped bool, sha string) StepState {
	if phase != ledger.PhaseReady {
		tries = max(tries, 1) // a probe's are its own, none when it ran none
	}
	st := StepState{Phase: phase, ID: id, K: k, State: state, Argv: argv, URL: url, Dir: dir, Subject: subject, Manifest: manifest, TimedOut: timedOut, Error: errText,
		Ports: ports, Tries: tries, StartedAt: started, LogBytes: logBytes, LogDropped: dropped, LogSHA256: sha}
	if !finished.IsZero() {
		at := finished
		st.FinishedAt = &at
		st.Duration = finished.Sub(started).Milliseconds()
		if state == ledger.CheckFinished {
			x := exit
			st.Exit = &x
		}
	}
	return st
}

func sortSteps(steps []StepState) {
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].StartedAt.Before(steps[j].StartedAt) })
}

// runCommand runs one command of a tree, a check or a step, in dir
// through the tree's executor: a process group of its own, no PTY, the
// environment without Conductor's tokens (env passes the manifest's
// approved names on and gives the tree's own names), the output's head and
// tail kept, the log to log, whole within ledger.MaxLog.
func (e *Engine) runCommand(ctx context.Context, dir string, argv []string, timeout time.Duration, env cmdEnv, log io.Writer, started func(proc.Process)) proc.Result {
	return e.exec.run(ctx, proc.Spec{Argv: slices.Clone(argv), Dir: dir, Allow: slices.Clone(env.allow), Inject: maps.Clone(env.inject), Timeout: timeout,
		Head: checkHead, Tail: checkTail, Log: log, LogLimit: ledger.MaxLog, Started: started})
}
