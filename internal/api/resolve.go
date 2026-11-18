package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A resolved launch (ext.Host's ResolveLaunch, and a launch held to one by
// its digest): what a launch of a saved crew would run now, worked out by
// one function whose values a held launch hands the engine as they are,
// nothing read again between the compare and the run. The digest says the
// launch is unchanged since it was resolved, never that a person agreed
// to it: a matching one opens no approved launch and carries no grant.

// resolvedRun is a saved crew's launch as the server works it out: the
// crew as read (once), the crew as it would be launched (its folder and
// yolo resolved, its members bound, their reservations held until
// release), the engine's preparation of it at its base, and the record of
// it with its digest.
type resolvedRun struct {
	saved    crew.Crew
	launch   crew.Crew
	prepared *crew.Prepared
	record   ext.ResolvedLaunch
	release  func()
}

// afterResolveCompare, when a test sets it, runs in a held launch once its
// digest has been compared and before the engine is handed what was
// resolved: a change made there must not reach the run.
var afterResolveCompare func()

// resolveSaved resolves the launch of the saved crew crewID for the
// Dispatch project project ("" for none) at commit ("" for the launch
// folder's HEAD). The members are bound as a launch binds them; the caller
// calls release once it is done with them (a resolve at once, a launch once
// the run's record holds them). It runs no agent's installed or identity
// look-up and writes nothing. A refusal is an ext.Refusal (or
// ext.ErrNotFound, ext.ErrUnsupported); the engine's own errors are told
// by engineRefusal.
func (s *Server) resolveSaved(ctx context.Context, crewID, project, commit string) (*resolvedRun, error) {
	if s.crews == nil {
		return nil, fmt.Errorf("%w: no data directory is configured", ext.ErrUnsupported)
	}
	c, err := s.crews.Get(crewID)
	switch {
	case errors.Is(err, crew.ErrNotFound):
		return nil, fmt.Errorf("%w: no such crew", ext.ErrNotFound)
	case errors.Is(err, crew.ErrUnreadable) || errors.Is(err, crew.ErrInvalid):
		return nil, refusedCode(ext.RefusalInvalidCrew, "%v", err)
	case err != nil:
		s.log.Error("crew read failed", "crew", crewID, "err", err)
		return nil, refusedCode("crews_unreadable", "could not read the crews")
	}
	rr := &resolvedRun{saved: c, release: func() {}}
	rec := ext.ResolvedLaunch{Crew: c.ID, Name: c.Name, Managed: c.Managed(), Tasks: len(c.Tasks), Where: ext.RunsOnServer, Project: project,
		Manifest: ext.ResolvedManifest{Source: ext.ManifestNone}}
	if project != "" {
		if rec.ProjectCwd, err = s.projectFolder(ctx, project); err != nil {
			return nil, err
		}
	}
	if c.Where == crew.WhereHost {
		// A crew on a host: what it would run is the host's to work out;
		// its folder is the host's, said as saved. One saved with no
		// whole folder (its host's default) names none the record can
		// say: refused, the caller told why.
		if !filepath.IsAbs(c.Cwd) || len(c.Cwd) > ext.MaxFolder {
			return nil, refusedCode(ext.RefusalInvalidCwd, "a crew on a host resolves only with a whole folder of its own, and this one names none")
		}
		rec.Where, rec.Cwd = ext.RunsOnHost, c.Cwd
	} else {
		if err := c.Launchable(); err != nil {
			return nil, refusedCode(ext.RefusalInvalidCrew, "%v", err)
		}
		cwd, err := s.resolveCwd(cmp.Or(c.Cwd, s.cfg.DefaultCwd))
		if err != nil {
			return nil, refusedCode(ext.RefusalInvalidCwd, "%v", err)
		}
		rec.Cwd, rec.InProject = cwd, inProject(cwd, rec.ProjectCwd)
	}
	yolo := s.cfg.Yolo
	if c.Yolo != nil {
		yolo = *c.Yolo
	}
	cat := s.Catalog()
	for _, m := range c.Members {
		agent, ok := cat.Get(m.AgentID)
		if !ok {
			return nil, refusedCode(ext.RefusalInvalidCrew, "member %s: unknown agent %s", m.Name, m.AgentID)
		}
		rm := ext.ResolvedMember{Name: m.Name, Agent: m.AgentID, Start: m.Start.When, Yolo: yolo, Selection: agentSelection(agent, yolo).SHA256}
		if m.Start.When == crew.StartAfter {
			rm.After = m.Start.Member
		}
		rec.Members = append(rec.Members, rm)
	}
	if rec.Where == ext.RunsOnServer {
		launch := c
		launch.Cwd = rec.Cwd
		launch.Yolo = &yolo
		members, unbind, aerr := s.bindMembers(c.Members, yolo, nil)
		if aerr != nil {
			return nil, refusedCode(aerr.Code, "%s", aerr.Message)
		}
		launch.Members = members
		rr.launch, rr.release = launch, unbind
		// The account and the model each member would be bound to now:
		// pinned by the digest, named by the account's id and where its
		// login comes from, never by its folder or its login.
		for i, m := range members {
			if b := m.Bound; b != nil {
				rec.Members[i].Account, rec.Members[i].AccountSource = b.ID, b.Source
			}
			rec.Members[i].Model = m.Model
		}
		if commit != "" && !c.Managed() {
			rr.release()
			return nil, refusedCode(ext.RefusalInvalidRequest, "a start commit is a crew with tasks' base, and this crew has none")
		}
		if c.Managed() && s.ledger == nil {
			rr.release()
			return nil, refusedCode("ledger_unavailable", "the ledger could not be opened; see the server's log")
		}
		p, err := s.runs.PrepareLaunch(ctx, launch, commit)
		if err != nil {
			rr.release()
			return nil, engineRefusal(err)
		}
		rr.prepared = p
		source, revision, blob := p.Manifest()
		rec.Manifest = ext.ResolvedManifest{Source: source, Revision: revision, Blob: blob}
	}
	digest, ok := ext.ResolvedDigest(rec, crew.Revision(c))
	if !ok {
		rr.release()
		return nil, refusedCode(ext.RefusalResolvedTooLarge, "the resolved launch is past %d bytes", ext.MaxResolvedLaunch)
	}
	rec.Digest = digest
	rr.record = rec
	return rr, nil
}

// projectFolder is the Dispatch project's folder, resolved as a launch
// folder is: "" for a project with none. An unknown project is
// project_unknown, an archived one project_archived; a server with no
// Dispatch store answers ext.ErrUnsupported.
func (s *Server) projectFolder(ctx context.Context, key string) (string, error) {
	if s.dispatch == nil {
		return "", fmt.Errorf("%w: this server keeps no Dispatch store", ext.ErrUnsupported)
	}
	pv, err := s.dispatch.Project(ctx, key)
	switch {
	case errors.Is(err, dispatch.ErrNotFound):
		return "", refusedCode(ext.RefusalProjectUnknown, "no Dispatch project %s", key)
	case err != nil:
		s.log.Warn("a Dispatch project could not be read for a resolved launch", "project", key, "err", err)
		return "", refusedCode("dispatch_unavailable", "the Dispatch store could not be read")
	case !pv.Project.ArchivedAt.IsZero():
		return "", refusedCode(ext.RefusalProjectArchived, "the Dispatch project %s is archived", key)
	case pv.Project.Cwd == "":
		return "", nil
	}
	cwd, err := s.resolveCwd(pv.Project.Cwd)
	if err != nil {
		return "", refusedCode(ext.RefusalInvalidCwd, "the project's folder: %v", err)
	}
	return cwd, nil
}

// inProject reports whether the launch folder cwd is the project's folder
// or under it by a path separator: both real paths; false with no project
// folder.
func inProject(cwd, project string) bool {
	if project == "" || cwd == "" {
		return false
	}
	return cwd == project || strings.HasPrefix(cwd, strings.TrimSuffix(project, string(filepath.Separator))+string(filepath.Separator))
}

// engineRefusal is the engine's refusal of a resolve (PrepareLaunch) as a
// provider is told it.
func engineRefusal(err error) error {
	switch {
	case errors.Is(err, crew.ErrStartCommitMissing):
		return refusedCode(ext.RefusalStartCommitMissing, "%v", err)
	case errors.Is(err, crew.ErrInvalid):
		return refusedCode(ext.RefusalInvalidCrew, "%v", err)
	case errors.Is(err, crew.ErrNotRepo), errors.Is(err, crew.ErrNoGit):
		return refusedCode(ext.RefusalInvalidCwd, "%v", err)
	case errors.Is(err, crew.ErrNoLedger):
		return refusedCode("ledger_unavailable", "%v", err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	return refusedCode("launch_failed", "%v", err)
}

// ResolveLaunch answers what a launch of the saved crew r.Crew would run
// now, for the Dispatch project r.Project at r.Commit, and its digest. It
// keeps nothing: the members' reservations are let go before it answers.
func (h *extHost) ResolveLaunch(ctx context.Context, r ext.ResolveRequest) (ext.ResolvedLaunch, error) {
	if err := h.gone(); err != nil {
		return ext.ResolvedLaunch{}, err
	}
	switch {
	case h.s.cfg.Switchyard.Enabled:
		return ext.ResolvedLaunch{}, fmt.Errorf("%w: this server coordinates hosted sessions and launches none", ext.ErrUnsupported)
	case h.desk:
		return ext.ResolvedLaunch{}, fmt.Errorf("%w: the desk's host resolves no launch", ext.ErrUnsupported)
	case h.s.ledger == nil:
		return ext.ResolvedLaunch{}, fmt.Errorf("%w: this server keeps no ledger", ext.ErrUnsupported)
	case h.s.dispatch == nil:
		return ext.ResolvedLaunch{}, fmt.Errorf("%w: this server keeps no Dispatch store", ext.ErrUnsupported)
	}
	if err := r.Valid(); err != nil {
		return ext.ResolvedLaunch{}, refusedCode(ext.RefusalInvalidRequest, "%v", err)
	}
	bctx, end := h.bound(ctx)
	defer end()
	rr, err := h.s.resolveSaved(bctx, r.Crew, r.Project, r.Commit)
	if err != nil {
		if h.gone() != nil {
			return ext.ResolvedLaunch{}, context.Canceled
		}
		return ext.ResolvedLaunch{}, err
	}
	rr.release()
	rec := rr.record
	rec.Capacity = h.s.resolvedCapacity(bctx, rec)
	return rec, nil
}

// launchResolved launches what rr resolved, as launchSaved launches a
// crew: the crews.managed switch and the ledger's owner, the agents'
// installed and identity look-ups, admit last, then the engine is handed
// rr's crew and preparation as they are. It answers the run, its release
// (the engine's hold, the launch's context and the members'
// reservations) and the error, the engine's raw.
func (s *Server) launchResolved(ctx context.Context, rr *resolvedRun, label string, admit func() error, opts crew.LaunchOpts) (*crew.Run, func(), error) {
	c := rr.launch
	fail := func(err error) (*crew.Run, func(), error) {
		rr.release()
		return nil, func() {}, err
	}
	if rr.prepared == nil {
		return fail(refusedCode(ext.RefusalInvalidCrew, "a crew on a host is launched by its host"))
	}
	if err := crew.ValidateLabel(label); err != nil {
		return fail(refusedCode(ext.RefusalInvalidRequest, "%v", err))
	}
	if c.Managed() {
		if !s.cfg.ManagedCrews() {
			return fail(refusedCode("managed_off", "crews with tasks are off on this server (crews.managed)"))
		}
		if s.ledger == nil {
			return fail(refusedCode("ledger_unavailable", "the ledger could not be opened; see the server's log"))
		}
		if ro, why := s.ledger.ReadOnly(); ro {
			return fail(refusedCode("ledger_unavailable", "%s", why))
		}
	}
	cat := s.Catalog()
	if err := checkLaunch(rr.saved.Members, cat, s.installedFor(ctx, cat, rr.saved.Members), s.identityFor(ctx, cat, rr.saved.Members)); err != nil {
		return fail(refusedCode(ext.RefusalInvalidCrew, "%v", err))
	}
	if admit != nil {
		if err := admit(); err != nil {
			return fail(err)
		}
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runTimeout)
	run, release, err := s.runs.LaunchPrepared(rctx, c, rr.prepared, label, opts)
	return run, func() { release(); cancel(); rr.release() }, err
}

// heldLaunch is a provider's launch with a project, a start commit or a
// digest: resolved for the project at the start commit, compared with the
// digest when one is given (launch_changed on a difference, nothing
// started), and launched from what was resolved.
func (h *extHost) heldLaunch(ctx context.Context, r ext.LaunchRequest, origin func(rec ext.ResolvedLaunch) (crew.LaunchOpts, error)) (*crew.Run, crew.Crew, func(), error) {
	noop := func() {}
	rr, err := h.s.resolveSaved(ctx, r.Crew, r.Project, r.StartCommit)
	if err != nil {
		return nil, crew.Crew{}, noop, err
	}
	if r.ResolvedDigest != "" && rr.record.Digest != r.ResolvedDigest {
		rr.release()
		return nil, rr.saved, noop, refusedCode(ext.RefusalLaunchChanged, "the launch resolves to another digest than the one it was confirmed with: resolve it again")
	}
	if afterResolveCompare != nil {
		afterResolveCompare()
	}
	opts, err := origin(rr.record)
	if err != nil {
		rr.release()
		return nil, rr.saved, noop, err
	}
	run, release, err := h.s.launchResolved(ctx, rr, r.Label, h.gone, opts)
	return run, rr.saved, release, err
}

// heldLaunchError is a held launch's error as the provider is told it: its
// own refusal as it is, the engine's as engineRefusal says it.
func (h *extHost) heldLaunchError(crewID string, err error) error {
	var r *ext.Refusal
	switch {
	case errors.As(err, &r), errors.Is(err, ext.ErrNotFound), errors.Is(err, ext.ErrUnsupported), errors.Is(err, context.Canceled):
		return err
	}
	h.s.log.Warn("crew launch failed", "crew", crewID, "provider", h.name, "err", err)
	return engineRefusal(err)
}

// resolvedCapacity is the kept usage view of each distinct agent and
// account among the resolved members, in their order, at most
// ext.MaxCapacityViews: what the server keeps (capacity.Cache.Latest),
// reading nothing, asking no agent's tool, moving no key's place, and the
// program's look-ups asked only what they hold. A view that cannot be made
// is left out. Nothing of it is logged.
func (s *Server) resolvedCapacity(ctx context.Context, rec ext.ResolvedLaunch) []ext.CapacityView {
	d := s.capacity
	if d == nil || d.st == nil {
		return nil
	}
	latest := func(k capacity.Key) capacity.View {
		v, _ := d.cache.Latest(k)
		return v
	}
	cat := s.Catalog()
	seen := map[[2]string]bool{}
	var out []ext.CapacityView
	for _, m := range rec.Members {
		key := [2]string{m.Agent, m.Account}
		if m.Account == "" || seen[key] || len(out) == ext.MaxCapacityViews {
			continue
		}
		seen[key] = true
		agent, ok := cat.Get(m.Agent)
		if !ok {
			continue
		}
		v, aerr := d.viewOf(ctx, agent, capacityQuery{account: m.Account, model: m.Model, launch: "managed", held: true}, latest)
		if aerr != nil {
			continue
		}
		out = append(out, capacityViewOf(m.Agent, m.Account, v))
	}
	return out
}

// capacityViewOf is a kept view as a provider is handed it, each part held
// to ext's bounds: words cut, a state or a kind that is not a word left
// out, a percent within 0 to 100.
func capacityViewOf(agent, account string, v capacity.View) ext.CapacityView {
	out := ext.CapacityView{Agent: agent, Account: account, Label: cutRunes(v.Account.Label, ext.MaxAccountLabel-1), State: capacityWord(v.State),
		Fresh: v.Fresh, Running: v.Counts.Running, Started5h: v.Counts.Started5h, Started7d: v.Counts.Started7d}
	switch out.Fresh {
	case ext.CapacityFresh, ext.CapacityStale, ext.CapacityResetPassed:
	default:
		out.Fresh = ext.CapacityNone
	}
	if out.State == "" {
		out.State = "unknown"
	}
	if v.ReadAt != nil {
		out.ReadAt = v.ReadAt.UTC()
	}
	for _, w := range v.Windows {
		if len(out.Windows) == ext.MaxCapacityWindows {
			break
		}
		cw := ext.CapacityWindow{Kind: capacityWord(w.Kind), Name: cutBytes(w.Name, ext.MaxCapacityName), Minutes: max(w.Minutes, 0), Percent: min(max(w.Percent, 0), 100)}
		if w.ResetsAt != nil {
			cw.ResetsAt = w.ResetsAt.UTC()
		}
		switch w.Severity {
		case "normal", "warning", "critical":
			cw.Severity = w.Severity
		}
		out.Windows = append(out.Windows, cw)
	}
	figure := func(p *string) string {
		if p == nil {
			return ""
		}
		return cutBytes(*p, ext.MaxCapacityFigure)
	}
	for _, c := range v.Credits {
		if len(out.Credits) == ext.MaxCapacityCredits {
			break
		}
		out.Credits = append(out.Credits, ext.CapacityCredit{Kind: capacityWord(c.Kind), Enabled: c.Enabled, Used: figure(c.Used), Limit: figure(c.Limit),
			Balance: figure(c.Balance), Unlimited: c.Unlimited, Currency: cutBytes(c.Currency, ext.MaxCapacityWord)})
	}
	return out
}

// capacityWord is s when it is a word of a view (1 to ext.MaxCapacityWord
// of a-z, 0-9 and '_'), "" otherwise.
func capacityWord(s string) string {
	if s == "" || len(s) > ext.MaxCapacityWord {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return ""
		}
	}
	return s
}
