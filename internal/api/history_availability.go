package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/history"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Availability (docs/plans/history.md, "Availability", PR-4): whether
// what an entry names can still be opened, computed when a page is
// answered from what the server holds now, never stored. A page's is
// cheap: the registry, the ledger's rows without their logs (read once
// an attempt for the page), and a stat; no git. /api/history/entry adds
// the rest: whether a session can be resumed (by the resume routes' own
// rules, resumeCheck), whether a result commit is in its repository, the
// worktree's branch, and a log's size and hash.

// The availability states.
const (
	availLive         = "live"          // a session listed and running
	availListed       = "listed"        // ended, still listed: its page shows its last screen
	availRecord       = "record"        // only its record
	availLost         = "lost"          // only its record, which the server stopped observing while it ran
	availHostAway     = "host_away"     // a hosted session whose host is not connected now
	availForgotten    = "forgotten"     // its record is no longer kept
	availWorktree     = "worktree"      // an attempt's worktree is there
	availWorktreeGone = "worktree_gone" // it is not
	availElsewhere    = "elsewhere"     // an attempt that ran in another Runtime: not looked at from here
	availLog          = "log"           // a receipt whose log the ledger holds
	availLogDropped   = "log_dropped"   // the run's bound dropped its log: its hash and tail kept
	availPruned       = "pruned"        // the run's rows are gone, or the brief's request
	availDeleted      = "deleted"       // a ticket's or a comment's tombstone
	availKept         = "kept"          // the store holds it
)

// availabilityOf is an entry's availability as served: its state and its
// words; for a session's record entry on /api/history/entry, whether it
// can be resumed; and what else the entry route reads.
type availabilityOf struct {
	State  string        `json:"state"`
	Words  string        `json:"words"`
	Resume *resumeOffer  `json:"resume,omitempty"`
	Commit *commitInRepo `json:"commit,omitempty"`
	Branch string        `json:"branch,omitempty"`
	Log    *logKept      `json:"log,omitempty"`
}

// resumeOffer is whether Resume is offered, and why not or what it asks;
// Confirm says it needs the person's second yes (a lost record); URL is
// where the resume is instead (a crew member's run).
type resumeOffer struct {
	Offered bool   `json:"offered"`
	Why     string `json:"why,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
	URL     string `json:"url,omitempty"`
}

type commitInRepo struct {
	ID      string `json:"id"`
	Present bool   `json:"present"`
}

type logKept struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// availability answers availability for one reply, reading each run's
// rows once.
type availability struct {
	s        *Server
	ctx      context.Context
	ix       *history.Index
	attempts map[string][]ledger.AttemptRow
	checks   map[attemptID][]ledger.CheckRow
}

func (s *Server) newAvailability(ctx context.Context, ix *history.Index) *availability {
	return &availability{s: s, ctx: ctx, ix: ix, attempts: map[string][]ledger.AttemptRow{}, checks: map[attemptID][]ledger.CheckRow{}}
}

// listed says the server lists the session now.
func (av *availability) listed(id string) bool {
	_, ok := av.s.registry.Get(id)
	return ok
}

// of is an entry's availability; full adds what the entry route reads.
func (av *availability) of(e history.Entry, full bool) availabilityOf {
	r, err := brakeman.ParseRef(e.Ref)
	kind := e.Kind
	switch {
	case kind == "session" || strings.HasPrefix(kind, "session."):
		id := strings.TrimPrefix(e.Anchor, "session:")
		out := av.session(id, kind == "session", e)
		if full && kind == "session" {
			out.Resume = av.s.resumeOfRecord(av.ctx, av.ix, id)
		}
		return out
	case kind == "attempt" && err == nil:
		return av.attempt(r, e, full)
	case (kind == "check" || kind == "verify" || kind == "step") && err == nil:
		return av.check(r, full)
	case kind == "ticket" || kind == "comment":
		if e.Outcome == availDeleted {
			return availabilityOf{State: availDeleted, Words: "deleted"}
		}
		return availabilityOf{State: availKept, Words: "in Dispatch"}
	case kind == "brief.item" && err == nil:
		if _, serr := os.Stat(filepath.Join(av.s.store.Dir(), filepath.FromSlash(historyBriefDoc(r.Request)))); serr != nil {
			return availabilityOf{State: availPruned, Words: "pruned: Brakeman no longer keeps its brief"}
		}
		return availabilityOf{State: availKept, Words: "kept by Brakeman"}
	}
	return availabilityOf{State: availKept, Words: "kept"}
}

// session is a session's availability: listed (running or ended), else
// its record's (kept, lost while it ran, its host away), else forgotten.
func (av *availability) session(id string, record bool, e history.Entry) availabilityOf {
	if d, ok := av.s.registry.Get(id); ok {
		if !d.Info().Status.Ended() {
			return availabilityOf{State: availLive, Words: "live"}
		}
		return availabilityOf{State: availListed, Words: "ended, listed"}
	}
	if _, err := os.Stat(filepath.Join(av.s.store.Dir(), journal.Dir, journal.SessionsDir, id+".json")); err != nil {
		return availabilityOf{State: availForgotten, Words: "forgotten: its record is no longer kept"}
	}
	switch {
	case record && e.Outcome == journal.EndingLost:
		return availabilityOf{State: availLost, Words: "record only: the server stopped while it ran"}
	case record && (e.Outcome == journal.EndingHostAway || (e.Hosted && e.Outcome == "running")):
		return availabilityOf{State: availHostAway, Words: "host away"}
	}
	return availabilityOf{State: availRecord, Words: "record only"}
}

// attemptRows is a run's attempts, read once for the reply.
func (av *availability) attemptRows(run string) []ledger.AttemptRow {
	if as, ok := av.attempts[run]; ok {
		return as
	}
	var as []ledger.AttemptRow
	if ld := av.s.ledger; ld != nil {
		as, _ = ld.Attempts(av.ctx, run)
	}
	av.attempts[run] = as
	return as
}

// attempt is an attempt's: its worktree there or gone, by a stat, when it
// ran natively; one that ran in another Runtime is said as its row has it
// and not looked at from here.
func (av *availability) attempt(r brakeman.Ref, e history.Entry, full bool) availabilityOf {
	if e.Where != "" && e.Where != string(ext.WhereNative) {
		return availabilityOf{State: availElsewhere, Words: "on " + e.Where + ": not looked at from here"}
	}
	var row ledger.AttemptRow
	found := false
	for _, a := range av.attemptRows(r.Run) {
		if a.TaskID == r.Task && a.N == r.N {
			row, found = a, true
		}
	}
	out := availabilityOf{State: availWorktreeGone, Words: "worktree removed"}
	if found && row.Worktree != "" && filepath.IsAbs(row.Worktree) {
		if st, err := os.Stat(row.Worktree); err == nil && st.IsDir() {
			out = availabilityOf{State: availWorktree, Words: "worktree kept"}
		}
	}
	if full && found {
		ctx, cancel := context.WithTimeout(av.ctx, revisionTimeout)
		defer cancel()
		if out.State == availWorktree {
			if b, err := gitcli.Run(ctx, row.Worktree, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
				out.Branch = strings.TrimSpace(b)
			}
		}
		if objectID(row.ResultCommit) {
			dir := e.Tree
			if dir == "" {
				dir = e.Repo
			}
			if dir != "" {
				_, err := gitcli.Run(ctx, dir, "cat-file", "-e", row.ResultCommit+"^{commit}")
				out.Commit = &commitInRepo{ID: row.ResultCommit, Present: err == nil}
			}
		}
	}
	return out
}

// check is a receipt's: its log held, dropped by the run's bound, or the
// run's rows gone.
func (av *availability) check(r brakeman.Ref, full bool) availabilityOf {
	ld := av.s.ledger
	if ld == nil {
		return availabilityOf{State: availRecord, Words: "the ledger is not open"}
	}
	key := attemptID{r.Run, r.Task, r.N}
	rows, ok := av.checks[key]
	if !ok {
		rows, _ = ld.Checks(av.ctx, r.Run, r.Task, r.N, false)
		av.checks[key] = rows
	}
	for _, c := range rows {
		if c.ID != r.Check || c.K != r.K {
			continue
		}
		if c.LogDropped {
			return availabilityOf{State: availLogDropped, Words: "log dropped: its hash and tail kept"}
		}
		out := availabilityOf{State: availLog, Words: "log kept"}
		if full {
			if row, ok, err := ld.Check(av.ctx, r.Run, r.Task, r.N, r.Check, r.K); err == nil && ok {
				out.Log = &logKept{Size: int64(len(row.Log)), SHA256: row.LogSHA256}
				out.Words = fmt.Sprintf("log kept (%d bytes)", len(row.Log))
			}
		}
		return out
	}
	return availabilityOf{State: availPruned, Words: "pruned: the run's rows are gone"}
}

// resumeOfRecord is whether a session can be resumed from its record, by
// the resume routes' rules (resumeCheck) and the record's own: never a
// hosted session (its machine resumes it), a crew member (its run does),
// a managed attempt (its run continues) or a Brakeman turn; never a
// launch whose arguments were too long to keep; only in a Runtime this
// server still has; a lost record (its process's state unknown) only
// with the person's second yes.
func (s *Server) resumeOfRecord(ctx context.Context, ix *history.Index, id string) *resumeOffer {
	if s.sessionRecords == nil {
		return &resumeOffer{Why: "this server keeps no history"}
	}
	rec, _, err := s.sessionRecords.Get(id)
	if err != nil {
		return &resumeOffer{Why: "its record is no longer kept"}
	}
	return s.resumeOffered(ctx, ix, rec)
}

// resumeOffered is resumeOfRecord's answer for the record rec.
func (s *Server) resumeOffered(ctx context.Context, ix *history.Index, rec journal.Record) *resumeOffer {
	id := rec.ID
	no := func(why string) *resumeOffer { return &resumeOffer{Why: why} }
	if rec.Host.Hosted {
		return no(fmt.Sprintf("resume it on %s: `conductor host` resumes it there", cmpOr(rec.Host.Name, "its host")))
	}
	if run, ok := s.memberOf(ctx, ix, rec); ok {
		switch {
		case rec.Crew != nil && rec.Crew.Brakeman != "":
			return no("a Brakeman turn is never resumed: ask Brakeman again")
		case rec.Crew != nil && rec.Crew.Task != "":
			return &resumeOffer{Why: "a managed run continues from its run page", URL: "/runs/" + run}
		}
		return &resumeOffer{Why: "resume it as the run's member, from its run", URL: "/runs/" + run}
	}
	if rec.Launch == nil || !rec.Launch.ArgsKept {
		return no("its arguments were too long to keep")
	}
	if rec.Where != "" {
		if _, ok := s.runtimeAt(ext.Where(rec.Where)); !ok {
			return no(fmt.Sprintf("its Runtime %s is not on this server", rec.Where))
		}
	}
	if d, ok := s.registry.Get(id); ok && !d.Info().Status.Ended() {
		return no("the session is still running")
	}
	var as *session.AgentSession
	if rec.Agent.Session != nil {
		as = &session.AgentSession{ID: rec.Agent.Session.ID, Source: rec.Agent.Session.Source, Resumable: rec.Agent.Session.Resumable}
	}
	plan, aerr := s.resumeCheck(rec.Agent.ID, as, rec.Cwd)
	if aerr != nil {
		return no(aerr.Message)
	}
	if plan.id == "" {
		if plan.agent.Session.Empty() {
			return no(plan.agent.Name + " has no session recipe: nothing of its conversation can be resumed")
		}
		return no("nothing to resume yet: the agent had no turn")
	}
	if rec.Ending == journal.EndingLost || (!rec.LostAt.IsZero() && rec.EndedAt.IsZero()) {
		return &resumeOffer{Offered: true, Confirm: true, Why: fmt.Sprintf("the server stopped while this session ran on %s; its process may still be running", cmpOr(rec.Where, string(ext.WhereNative)))}
	}
	return &resumeOffer{Offered: true}
}

// memberOf is the run a session is a member of: by its record's crew, by
// the engine's runs, or by a run the index holds that names it (a record
// whose adoption the journal had not written when the server stopped).
func (s *Server) memberOf(ctx context.Context, ix *history.Index, rec journal.Record) (string, bool) {
	if rec.Crew != nil && rec.Crew.RunID != "" {
		return rec.Crew.RunID, true
	}
	if s.runs != nil {
		if run, _, ok := s.runs.MemberOf(rec.ID); ok {
			return run, true
		}
	}
	if ix != nil {
		ls, err := ix.Links(ctx, hSessionRef(rec.ID), history.MaxLinks)
		if err == nil {
			for _, l := range ls.In {
				if l.Rel != relSessionOf {
					continue
				}
				kind, rest, _ := strings.Cut(l.Ref, ":")
				if kind == "run" || kind == "attempt" {
					run, _, _ := strings.Cut(rest, "/")
					return run, true
				}
			}
		}
	}
	return "", false
}
