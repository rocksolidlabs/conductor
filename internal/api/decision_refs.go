package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
// stateNow, brakeman_refs.go). A decision's reader is the admin, so no
// Brakeman grant narrows what it resolves and no read log stands in for
// the baseline: a ref whose record changed before the card's first read
// still says changed.

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

// decisionResolver is the resolver of a decision's refs: no grant, no
// read log, Brakeman's store for a brief's ref.
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

// decisionBaseline is the baseline of a request's evidence refs, one
// entry per ref in its order, nil for a request with none (or with more
// than the record keeps, which the engine refuses). A ref that is not in
// Brakeman's syntax, or names nothing, refuses the request.
func (s *Server) decisionBaseline(ctx context.Context, evidence []string) (json.RawMessage, error) {
	if len(evidence) == 0 || len(evidence) > ledger.MaxDecisionEvidence {
		return nil, nil
	}
	rr := s.decisionResolver()
	var out bytes.Buffer
	out.WriteByte('[')
	for i, raw := range evidence {
		ref, err := brakeman.ParseRef(raw)
		if err != nil {
			return nil, refusedRef("evidence[%d]: %v", i, err)
		}
		now, ok := rr.refNow(ctx, ref)
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
		out = append(out, decisionRef{Ref: ref, Seen: e.Seen, Now: now, Change: refChange(e.Seen, now)})
	}
	return out
}

// refChange is what changed of a ref between its baseline seen and now:
// unread when its store could not be read at the opening, deleted when it
// is gone since, changed when its state, revision or digest differ.
func refChange(seen, now brakeman.RefState) string {
	switch {
	case seen.State == refUnavailable:
		return refUnread
	case now.State == refDeleted && seen.State != refDeleted:
		return refDeleted
	case now.State != seen.State || now.Rev != seen.Rev || now.SHA256 != seen.SHA256:
		return refChanged
	}
	return ""
}
