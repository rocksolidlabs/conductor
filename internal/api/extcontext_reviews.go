package api

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/quoted"
)

// contextReviews are the people's reviews (Dispatch's, RV1) a context
// provider is told of for the attempt q, of a run launched from a ticket:
//
//   - for a review, the reviews of the candidates of the task it reviews
//     (its inputs' tasks) made in the run or a run it continues, the newest
//     ext.MaxContextReviews that found anything, in the order they were made;
//   - for a work attempt that repairs a person's request, the review the
//     request is, alone, as the engine holds the repair its task file
//     carries (crew.Engine.RepairReview), so its findings are the same ones.
//
// Each finding goes by its id, its state, and the file and first line it
// names when it names a file inside the tree a provider may be shown
// (treePathOK; no line for the base's side); a review's words, a finding's, who made them
// and the link of either are never handed. A read that fails gives no
// review, and the attempt is asked about as before reviews were named.
func (s *Server) contextReviews(ctx context.Context, q ext.ContextQuery) []ext.ContextReview {
	ds := s.dispatch
	if ds == nil || q.Ticket == nil {
		return nil
	}
	tasks := map[string]bool{}
	repair := ""
	if q.Kind == crew.TaskReview {
		for _, in := range q.Inputs {
			tasks[in.Task] = true
		}
	} else if repair = s.runs.RepairReview(q.Run, q.Task); repair != "" {
		tasks[q.Task] = true
	}
	if len(tasks) == 0 {
		return nil
	}
	chain, err := s.ledgerChain(ctx, q.Run)
	if err != nil {
		return nil
	}
	of := func(candidate string) bool {
		c, err := dispatch.ParseCandidate(candidate)
		return err == nil && tasks[c.Task] && slices.Contains(chain, c.Run)
	}
	var ids []string
	if repair != "" {
		ids = []string{repair}
	} else {
		// Newest first, a page at a time, until the bound: a ticket keeps
		// at most dispatch.MaxReviews.
		for before := ""; len(ids) < ext.MaxContextReviews; {
			page, err := ds.Reviews(ctx, q.Ticket.Key, before)
			if err != nil {
				return nil
			}
			for _, rv := range page.Reviews {
				if n := rv.Counts; n.Open+n.Resolved+n.Withdrawn > 0 && of(rv.Candidate) && len(ids) < ext.MaxContextReviews {
					ids = append(ids, rv.ID)
				}
			}
			if page.Next == "" {
				break
			}
			before = page.Next
		}
		slices.Reverse(ids)
	}
	pathOK := treePathOK(s, q.Worktree)
	var out []ext.ContextReview
	for _, id := range ids {
		rv, err := ds.Review(ctx, id)
		if err != nil {
			return nil
		}
		if rv.TicketKey != q.Ticket.Key || !of(rv.Candidate) {
			continue
		}
		if cr := contextReview(rv, pathOK); len(cr.Findings) > 0 {
			out = append(out, cr)
		}
	}
	return out
}

// treePathOK is what a finding's path must be for a query to name it:
// a path inside a commit's tree (relative, no empty, '.' or '..' part),
// one plain line of at most ext.MaxContextRef bytes, and one the reads of
// the attempt's tree hand a provider (treeDenied, judged under the
// worktree as it resolves now). A worktree that does not resolve passes
// no path.
func treePathOK(s *Server, worktree string) func(path string) bool {
	top, err := filepath.EvalSymlinks(worktree)
	if err != nil || !filepath.IsAbs(top) {
		return func(string) bool { return false }
	}
	denied := s.treeDenied(top)
	return func(p string) bool {
		return validTreePath(p) && len(p) <= ext.MaxContextRef && quoted.Plain(p) && !denied(p)
	}
}

// contextReview is rv as a query names it: its id and its findings in
// their order (a review keeps at most ext.MaxContextReviewFindings), each
// by its id and state, with its path when pathOK takes it and, for a
// finding on the candidate's side, its first line within
// ext.MaxContextLine (a base side's line is of the base's file, so none is
// handed). A finding whose path is refused is named without path or line.
func contextReview(rv dispatch.Review, pathOK func(string) bool) ext.ContextReview {
	cr := ext.ContextReview{ID: rv.ID}
	for _, f := range rv.Findings[:min(len(rv.Findings), ext.MaxContextReviewFindings)] {
		cf := ext.ContextReviewFinding{ID: f.ID, State: f.State}
		if f.Path != "" && pathOK(f.Path) {
			cf.Path = f.Path
			if f.Side != dispatch.SideBase && f.From > 0 && f.From <= ext.MaxContextLine {
				cf.Line = f.From
			}
		}
		cr.Findings = append(cr.Findings, cf)
	}
	return cr
}
