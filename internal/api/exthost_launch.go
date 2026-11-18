package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A provider's launch and its runs (ext.Host's Launch with an Origin, Run,
// RunByOp and StopRun). The ledger's run_origin row decides: a run is the
// provider's when its row names the provider and carries an operation id,
// so a person's run, another provider's and a person's continuation of
// the provider's own run are no run of its. The record is a cache and
// grants nothing; a subject's link is never logged.

// launchOrigin is Launch for a request with an Origin, checked by Valid: a
// retry of an operation id the provider's ledger rows hold answers the run
// that row names, whatever else it asks, and launches nothing. Else the
// subject's reference must be one the server knows (invalid_subject) and
// the earlier run one this provider launched (prior_unknown), and the
// crew one with tasks (not_managed); the run's origin row is written with
// its first row. A second launch of the operation id that met the first's
// row in its first transaction is discarded whole, and answered the
// first's run.
func (h *extHost) launchOrigin(ctx context.Context, r ext.LaunchRequest) (ext.RunRef, error) {
	if h.desk {
		return ext.RunRef{}, fmt.Errorf("%w: the desk's host launches no run with an origin", ext.ErrUnsupported)
	}
	if h.s.ledger == nil {
		return ext.RunRef{}, refusedCode("ledger_unavailable", "the ledger could not be opened; see the server's log")
	}
	if ref, ok, err := h.runOfOp(ctx, r.OpID); err != nil || ok {
		return ref, err
	}
	if err := knownSubject(r.Subject.Ref); err != nil {
		return ext.RunRef{}, err
	}
	if r.Subject.Prior != "" {
		row, ok, err := h.s.ledger.Origin(ctx, r.Subject.Prior)
		if err != nil {
			return ext.RunRef{}, h.ledgerError(ctx, err)
		}
		if !ok || !h.owns(row) {
			return ext.RunRef{}, refusedCode(ext.RefusalPriorUnknown, "the earlier run is not one this provider launched")
		}
	}
	origin := &ledger.OriginRow{Provider: h.name, OpID: r.OpID, Ref: r.Origin.Ref, Label: r.Origin.Label,
		Subject: r.Subject.Ref, SubjectURL: r.Subject.URL, Prior: r.Subject.Prior, LimitSeconds: int(r.StopAfter / time.Second),
		Project: r.Project, StartCommit: r.StartCommit, ResolvedDigest: r.ResolvedDigest}
	bctx, end := h.bound(ctx)
	defer end()
	var (
		run     *crew.Run
		c       crew.Crew
		release func()
		err     error
		held    = r.Project != "" || r.ResolvedDigest != "" || r.StartCommit != ""
	)
	if held {
		// Resolved for its project at its start commit, held to its
		// digest, and launched from what was resolved (resolve.go).
		run, c, release, err = h.heldLaunch(bctx, r, func(rec ext.ResolvedLaunch) (crew.LaunchOpts, error) {
			if r.ResolvedDigest != "" {
				// The selections the resolved launch confirmed, one per
				// agent: every attempt is held to them.
				byAgent := map[string]string{}
				for _, m := range rec.Members {
					byAgent[m.Agent] = m.Selection
				}
				agents, err := crew.ConfirmedAgents(byAgent)
				if err != nil {
					return crew.LaunchOpts{}, err
				}
				origin.Agents = agents
			}
			return crew.LaunchOpts{Origin: origin}, nil
		})
	} else {
		run, c, release, err = h.s.launchSaved(bctx, r.Crew, r.Label, "", h.gone, crew.LaunchOpts{Origin: origin})
	}
	defer release()
	if errors.Is(err, ledger.ErrOpExists) {
		// Launched at once by another call with the operation id: that
		// one's first row came first, and this run is gone.
		if ref, ok, lerr := h.runOfOp(context.WithoutCancel(ctx), r.OpID); lerr != nil || ok {
			return ref, lerr
		}
	}
	if err != nil {
		if h.gone() != nil {
			return ext.RunRef{}, context.Canceled
		}
		if held {
			return ext.RunRef{}, h.heldLaunchError(r.Crew, err)
		}
		return ext.RunRef{}, h.launchError(r.Crew, err)
	}
	h.s.runs.Note(run.ID, session.ActivityStatus, "launched by "+r.By.Name+" through "+h.name)
	h.s.log.Info("crew launched", "crew", c.ID, "run", run.ID, "provider", h.name)
	return h.runRef(run.ID, c.ID), nil
}

// runOfOp is the provider's run of the operation id, as a launch answers
// it; ok is false for none.
func (h *extHost) runOfOp(ctx context.Context, opID string) (ext.RunRef, bool, error) {
	row, ok, err := h.s.ledger.RunByOp(ctx, h.name, opID)
	if err != nil {
		return ext.RunRef{}, false, h.ledgerError(ctx, err)
	}
	if !ok {
		return ext.RunRef{}, false, nil
	}
	run, found, err := h.s.ledger.Run(ctx, row.RunID)
	if err != nil {
		return ext.RunRef{}, false, h.ledgerError(ctx, err)
	}
	crewID := ""
	if found {
		crewID = run.CrewID
	}
	return h.runRef(row.RunID, crewID), true, nil
}

// runRef is a run as a launch answers it: its id, its crew and its page.
func (h *extHost) runRef(runID, crewID string) ext.RunRef {
	return ext.RunRef{ID: runID, Crew: crewID, URL: h.s.publicBase(nil) + "/runs/" + url.PathEscape(runID)}
}

// knownSubject refuses a subject reference in no form the server knows
// (invalid_subject): a change: or a job: reference (held to its pattern by
// Valid), or one of Brakeman's. An empty one is no subject.
func knownSubject(ref string) error {
	if ref == "" {
		return nil
	}
	kind, _, _ := strings.Cut(ref, ":")
	if kind == "change" || kind == "job" {
		return nil
	}
	if _, err := brakeman.ParseRef(ref); err != nil {
		return refusedCode(ext.RefusalInvalidSubject, "the subject's reference is in no form this server knows")
	}
	return nil
}

// owns reports whether the origin row is a run this provider launched: its
// name and an operation id. A continuation's row names the provider and no
// operation id: a person's run.
func (h *extHost) owns(row ledger.OriginRow) bool {
	return row.Provider == h.name && row.OpID != ""
}

// ledgerError is a failure of the ledger's read as the provider is told it:
// the call's own end when its context has ended (a try given up), which
// is no failure of the ledger's and is not logged as one.
func (h *extHost) ledgerError(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	h.s.log.Warn("the ledger could not be read for a provider", "provider", h.name, "err", err)
	return refusedCode("ledger_unavailable", "the ledger could not be read; see the server's log")
}

// doors checks what every one of the three run doors needs: the try, a
// server that launches (not a switchyard) and keeps a ledger, and a
// provider's Host (not the desk's).
func (h *extHost) doors() error {
	if err := h.gone(); err != nil {
		return err
	}
	switch {
	case h.s.cfg.Switchyard.Enabled:
		return fmt.Errorf("%w: this server coordinates hosted sessions and runs none", ext.ErrUnsupported)
	case h.desk:
		return fmt.Errorf("%w: the desk's host reads and stops no provider's run", ext.ErrUnsupported)
	case h.s.ledger == nil:
		return fmt.Errorf("%w: this server keeps no ledger", ext.ErrUnsupported)
	}
	return nil
}

// Run reads a run this provider launched: the engine's view while it
// holds the run, else the record recovered over the ledger (an
// interrupted run among them). A run whose ledger row does not name the
// provider with an operation id is ext.ErrNotFound, as no run is.
func (h *extHost) Run(ctx context.Context, id string) (ext.RunStatus, error) {
	if err := h.doors(); err != nil {
		return ext.RunStatus{}, err
	}
	if !ext.ValidRunID(id) {
		return ext.RunStatus{}, refusedCode(ext.RefusalInvalidRequest, "%q is not a run's id", cutBytes(id, ext.MaxRunID))
	}
	row, ok, err := h.s.ledger.Origin(ctx, id)
	if err != nil {
		return ext.RunStatus{}, h.ledgerError(ctx, err)
	}
	if !ok || !h.owns(row) {
		return ext.RunStatus{}, fmt.Errorf("%w: no such run of this provider", ext.ErrNotFound)
	}
	return h.status(ctx, row)
}

// RunByOp finds the run this provider's launch with the operation id made,
// and answers it as Run does. It never launches and writes nothing.
func (h *extHost) RunByOp(ctx context.Context, opID string) (ext.RunStatus, error) {
	if err := h.doors(); err != nil {
		return ext.RunStatus{}, err
	}
	if !ext.ValidOpID(opID) {
		return ext.RunStatus{}, refusedCode(ext.RefusalInvalidRequest, "%q is not an operation's id", cutBytes(opID, ext.MaxOpID))
	}
	row, ok, err := h.s.ledger.RunByOp(ctx, h.name, opID)
	if err != nil {
		return ext.RunStatus{}, h.ledgerError(ctx, err)
	}
	if !ok {
		return ext.RunStatus{}, fmt.Errorf("%w: no run of this operation id", ext.ErrNotFound)
	}
	return h.status(ctx, row)
}

// StopRun stops a run this provider launched, as POST
// /api/runs/{run}/stop does, its log saying who through which provider and
// why. A run the engine does not run (stopped, finished, interrupted, or
// let go) answers ext.ErrNotRunning and nothing changes: the look and the
// stop are one step of the engine's (crew.Engine.StopIfRunning). A stop
// handed to the engine runs to its end though the try is given up.
func (h *extHost) StopRun(ctx context.Context, r ext.StopRequest) error {
	if err := h.doors(); err != nil {
		return err
	}
	if err := r.Valid(); err != nil {
		return refusedCode(ext.RefusalInvalidRequest, "%v", err)
	}
	row, ok, err := h.s.ledger.Origin(ctx, r.Run)
	if err != nil {
		return h.ledgerError(ctx, err)
	}
	if !ok || !h.owns(row) {
		return fmt.Errorf("%w: no such run of this provider", ext.ErrNotFound)
	}
	if err := h.gone(); err != nil {
		return err
	}
	if ro, why := h.s.ledger.ReadOnly(); ro {
		// Another server owns the ledger and runs the run: this one can
		// neither stop it nor say it is not running.
		return refusedCode("ledger_unavailable", "%s", why)
	}
	note := "stopped by " + r.By.Name + " through " + h.name
	if r.Why != "" {
		note += ": " + r.Why
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	switch err := h.s.runs.StopIfRunning(sctx, r.Run, note); {
	case errors.Is(err, crew.ErrRunNotFound), errors.Is(err, crew.ErrRunNotRunning):
		return ext.ErrNotRunning
	case err != nil:
		h.s.log.Warn("run stop: a session did not stop cleanly", "run", r.Run, "provider", h.name, "err", err)
	}
	h.s.log.Info("run stopped", "run", r.Run, "provider", h.name)
	return nil
}

// status is the run of the provider's origin row as Run answers it.
func (h *extHost) status(ctx context.Context, row ledger.OriginRow) (ext.RunStatus, error) {
	run, ok := h.s.runs.Get(row.RunID)
	if !ok {
		var err error
		if run, ok, err = h.recovered(ctx, row.RunID); err != nil {
			return ext.RunStatus{}, err
		}
		if !ok {
			return ext.RunStatus{}, fmt.Errorf("%w: no such run of this provider", ext.ErrNotFound)
		}
	}
	st := ext.RunStatus{ID: row.RunID, Crew: run.CrewID, State: extRunState(run.State), Outcome: run.Outcome, Paused: run.Paused, NeedsInput: run.NeedsInput,
		StartedAt: run.StartedAt.UTC(),
		Origin: ext.RunOrigin{OpID: row.OpID, Ref: row.Ref, Label: row.Label, Subject: ext.Subject{Ref: row.Subject, URL: row.SubjectURL, Prior: row.Prior},
			Project: row.Project, StartCommit: row.StartCommit, ResolvedDigest: row.ResolvedDigest, Deadline: row.Deadline}}
	if run.StoppedAt != nil {
		st.StoppedAt = run.StoppedAt.UTC()
	}
	return st, nil
}

// recovered is a run the engine does not hold, as its record and the
// ledger say it (crew.Recovered): ok is false for a run the ledger has no
// row of.
func (h *extHost) recovered(ctx context.Context, runID string) (crew.Run, bool, error) {
	from, ok, err := crew.SQLLedger(h.s.ledger).Load(ctx, runID)
	if err != nil {
		return crew.Run{}, false, h.ledgerError(ctx, err)
	}
	if !ok {
		return crew.Run{}, false, nil
	}
	var rec crew.Run
	if h.s.records != nil {
		if got, found, err := h.s.records.Get(runID); err == nil && found {
			rec = got
		}
	}
	return crew.Recovered(rec, from), true, nil
}

// extRunState is a run's state as ext names it.
func extRunState(state string) string {
	switch state {
	case crew.RunNeedsInput:
		return ext.RunNeedsInput
	case crew.RunStopped:
		return ext.RunStopped
	case crew.RunFinished:
		return ext.RunFinished
	case crew.RunInterrupted:
		return ext.RunInterrupted
	}
	return ext.RunRunning
}
