package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A decision's evidence refs (docs/plans/decisions.md, The changes,
// internal/api, The evidence's refs). A request's refs, in Brakeman's ref
// syntax, are resolved once as it is taken, before RequestDecision, into
// the baseline the record keeps (what each record was when the question
// was asked), and again whenever the record is read whole, through the
// per-kind lookups a brief's refs are resolved by (refResolver.lookup and
// stateNow, brakeman_refs.go). At the request a ref is resolved under the
// asker's own grant, what its agent token reads of that kind of record
// everywhere else (decisionAsker): a ref outside it is refused as one that
// names nothing, whether or not its record exists, and nothing of it is
// looked up or kept. A decision's reader is the admin, so no grant
// narrows what the whole read resolves and no read log stands in for the
// baseline: a ref whose record changed before the card's first read still
// says changed.

// The changes a ref's resolution names against its baseline.
const (
	refChanged = "changed"
	// refUnread is a ref whose store could not be read when the question
	// was asked: nothing says what it was then.
	refUnread = "unread"
)

// decisionRef is one evidence ref's resolution, in the shape a brief's
// has (brakeman.Resolution): the record at the opening (seen, the
// baseline's) and now, and what changed between them ("" for nothing).
// At most maxDecisionRef bytes as encoded.
type decisionRef struct {
	Ref    brakeman.Ref      `json:"ref"`
	Seen   brakeman.RefState `json:"seen"`
	Now    brakeman.RefState `json:"now"`
	Change string            `json:"change,omitempty"`
}

// baselineEntry is one entry of a record's evidence baseline: the ref as
// the request named it and its record then, without its URL or time
// (ledger.MaxBaselineEntry).
type baselineEntry struct {
	Ref  string            `json:"ref"`
	Seen brakeman.RefState `json:"seen"`
}

// decisionResolver is the resolver of a decision's refs: no grant of its
// own (a request's refs are admitted first by the asker's,
// decisionBaseline; the whole read is the admin's), no read log,
// Brakeman's store for a brief's ref.
func (s *Server) decisionResolver() *refResolver {
	rr := &refResolver{s: s, at: time.Now().UTC(), seen: map[brakeman.Ref]brakeman.Read{}, runs: map[string]*refRun{}, tops: map[string]string{}}
	if s.brakeman != nil {
		rr.desk = s.brakeman.st
	}
	return rr
}

// refNow is ref's record now through the lookups a brief's refs use;
// false for a ref that names nothing. A store that cannot be read makes
// it unavailable.
func (rr *refResolver) refNow(ctx context.Context, ref brakeman.Ref) (brakeman.RefState, bool) {
	unread := brakeman.RefState{URL: refURL(ref), State: refUnavailable, At: rr.at}
	if ref.Kind == brakeman.RefBrief {
		res, why, err := rr.resolveBrief(ref)
		switch {
		case err != nil:
			return fitState(unread), true
		case why != "":
			return brakeman.RefState{}, false
		}
		return res.Now, true
	}
	f, err := rr.lookup(ctx, ref)
	if err != nil {
		return fitState(unread), true
	}
	if !f.found && !f.deleted && !f.unavailable {
		return brakeman.RefState{}, false
	}
	now, _ := rr.stateNow(ref, f)
	return fitState(now), true
}

// refusedRef is a request refused for one of its evidence refs.
func refusedRef(format string, args ...any) error {
	return &crew.DecisionError{Code: crew.CodeInvalid, Words: fmt.Sprintf(format, args...)}
}

// baselineSeam, when a test sets it, runs as a request's evidence ref is
// about to be resolved into its baseline: a test sees that a request sent
// again resolves nothing.
var baselineSeam atomic.Pointer[func(ref string)]

// decisionAsker is the attempt that asks a decision, as its agent token
// reads records everywhere else: its run (task_context names it); its
// task, with the task's attempts and their checks (its task file); the
// commits of its run (its worktree, its inputs); the messages its member's
// inbox holds (inbox_read); its own session and its question (the
// attention routes); and what its run's launch package hands its task
// file: the ticket, the comments the package carries, the plan revision
// and the launch. Nothing else: a run's commands and events, Dispatch's
// operations, a touched file (the touched index is the admin's), a brief,
// a review's candidate, and every record of another run, task, session or
// ticket.
type decisionAsker struct {
	run, task, member, session string
	pkg                        *crew.Package
}

// askerOf is the asker of a request session sid sends; false for a
// session that is no attempt.
func (s *Server) askerOf(ctx context.Context, sid string) (decisionAsker, bool, error) {
	run, task, _, ok := s.runs.AttemptOf(sid)
	if !ok {
		return decisionAsker{}, false, nil
	}
	a := decisionAsker{run: run, task: task, session: sid}
	if st, _, err := s.runs.TaskOf(run, task); err == nil {
		a.member = st.Member
	}
	if s.ledger != nil {
		row, found, err := s.ledger.Run(ctx, run)
		if err != nil {
			return decisionAsker{}, false, fmt.Errorf("the ledger's run %s: %w", run, err)
		}
		if found && row.Package != "" {
			var p crew.Package
			if json.Unmarshal([]byte(row.Package), &p) == nil && p.Ticket.ID == row.Ticket {
				a.pkg = &p
			}
		}
	}
	return a, true, nil
}

// admits reports whether the asker's token reads the record ref names
// (decisionAsker). Only a message is looked up, in the asker's own run,
// for its addressee; every other kind is judged by the ref alone.
func (a decisionAsker) admits(ctx context.Context, s *Server, ref brakeman.Ref) (bool, error) {
	key := ""
	if a.pkg != nil {
		key = a.pkg.Ticket.Key
	}
	switch ref.Kind {
	case brakeman.RefRun, brakeman.RefCommit:
		return ref.Run == a.run, nil
	case brakeman.RefTask, brakeman.RefAttempt, brakeman.RefCheck:
		return ref.Run == a.run && ref.Task == a.task, nil
	case brakeman.RefSession, brakeman.RefQuestion:
		return ref.Session == a.session, nil
	case brakeman.RefTicket:
		return key != "" && ref.Ticket == key, nil
	case brakeman.RefComment:
		return key != "" && ref.Ticket == key && slices.ContainsFunc(a.pkg.Comments, func(c crew.PackageComment) bool { return c.ID == ref.Comment }), nil
	case brakeman.RefPlan:
		return key != "" && ref.Ticket == key && ref.N == a.pkg.Plan.N, nil
	case brakeman.RefLaunch:
		return a.pkg != nil && a.pkg.Launch.ID != "" && ref.Launch == a.pkg.Launch.ID, nil
	case brakeman.RefMessage:
		if ref.Run != a.run || a.member == "" || s.ledger == nil {
			return false, nil
		}
		msgs, err := s.ledger.Messages(ctx, a.run)
		if err != nil {
			return false, fmt.Errorf("the ledger's messages of %s: %w", a.run, err)
		}
		return slices.ContainsFunc(msgs, func(m ledger.MessageRow) bool { return m.Seq == ref.Seq && m.To == a.member }), nil
	}
	return false, nil
}

// decisionBaseline is the baseline of the evidence refs of a request
// session sid sends, one entry per ref in its order, nil for a request
// with none (or with more than the record keeps, which the engine
// refuses). A ref that is not in Brakeman's syntax, names nothing, or lies
// outside the asker's grant (decisionAsker) refuses the request, the last
// two in the same words.
func (s *Server) decisionBaseline(ctx context.Context, sid string, evidence []string) (json.RawMessage, error) {
	if len(evidence) == 0 || len(evidence) > ledger.MaxDecisionEvidence {
		return nil, nil
	}
	asker, isAttempt, err := s.askerOf(ctx, sid)
	if err != nil {
		return nil, err
	}
	rr := s.decisionResolver()
	var out bytes.Buffer
	out.WriteByte('[')
	for i, raw := range evidence {
		if f := baselineSeam.Load(); f != nil {
			(*f)(raw)
		}
		ref, err := brakeman.ParseRef(raw)
		if err != nil {
			return nil, refusedRef("evidence[%d]: %v", i, err)
		}
		admitted := false
		if isAttempt {
			if admitted, err = asker.admits(ctx, s, ref); err != nil {
				return nil, err
			}
		}
		var now brakeman.RefState
		ok := false
		if admitted {
			now, ok = rr.refNow(ctx, ref)
		}
		if !ok {
			return nil, refusedRef("evidence[%d]: %s resolves to nothing", i, cutBytes(raw, 80))
		}
		entry, err := encodeEntry(baselineEntry{Ref: raw, Seen: brakeman.RefState{State: now.State, Rev: now.Rev, SHA256: now.SHA256}})
		if err == nil && len(entry) > ledger.MaxBaselineEntry {
			// The state alone, so the entry fits.
			entry, err = encodeEntry(baselineEntry{Ref: raw, Seen: brakeman.RefState{State: now.State}})
		}
		if err != nil {
			return nil, err
		}
		if len(entry) > ledger.MaxBaselineEntry {
			return nil, refusedRef("evidence[%d] is too long to keep", i)
		}
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(entry)
	}
	out.WriteByte(']')
	return out.Bytes(), nil
}

// encodeEntry is e as the baseline keeps it, HTML unescaped.
func encodeEntry(e baselineEntry) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// decisionRefs resolves a record's evidence baseline against the records
// now: one resolution per ref, in its order, never nil.
func (s *Server) decisionRefs(ctx context.Context, evidence json.RawMessage) []decisionRef {
	out := []decisionRef{}
	var entries []baselineEntry
	if len(evidence) == 0 || json.Unmarshal(evidence, &entries) != nil {
		return out
	}
	rr := s.decisionResolver()
	for _, e := range entries {
		ref, err := brakeman.ParseRef(e.Ref)
		if err != nil {
			continue
		}
		now, ok := rr.refNow(ctx, ref)
		if !ok {
			// Gone without a tombstone since the opening.
			now = brakeman.RefState{URL: refURL(ref), State: refDeleted, At: rr.at}
		}
		out = append(out, fitRef(decisionRef{Ref: ref, Seen: e.Seen, Now: now, Change: refChange(ref.Kind, e.Seen, now)}))
	}
	return out
}

// fitRef holds a resolution to maxDecisionRef bytes as writeJSON encodes
// it, escapes and all: past it, its URL now is dropped (the ref names the
// record, the URL only leads to it), and what is left is within the bound
// by construction (a ref of ledger.MaxEvidenceRef bytes, each escaped to
// six, and two snapshots at fitState's bounds, each escaped the same;
// TestTheFullestDecisionFitsItsBound).
func fitRef(r decisionRef) decisionRef {
	if b, err := json.Marshal(r); err == nil && len(b) < maxDecisionRef {
		return r
	}
	r.Now.URL = ""
	return r
}

// refChange is what changed of a ref of the kind between its baseline seen
// and now: unread when its store could not be read at the opening,
// deleted when it is gone since, changed when its state, revision or
// digest differ. A run's revision moves with every transition of the run,
// the decision's own opening among them, so a run: ref is changed only
// when its state is: what the asker cited is the run, not its ledger's
// count of transitions (the plan's card shows a live run's chip
// unchanged, decisions.spec.ts).
func refChange(kind string, seen, now brakeman.RefState) string {
	switch {
	case seen.State == refUnavailable:
		return refUnread
	case now.State == refDeleted && seen.State != refDeleted:
		return refDeleted
	case kind == brakeman.RefRun:
		if now.State != seen.State {
			return refChanged
		}
	case now.State != seen.State || now.Rev != seen.Rev || now.SHA256 != seen.SHA256:
		return refChanged
	}
	return ""
}
