package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/history"
)

// RV1's review batches and findings in the history (docs/plans/history.md,
// Later PRs, PR-10): a ticket's reviews, read through Dispatch's own reads
// of them (Store.Reviews, Store.Review: what RV1's routes serve), are part
// of the ticket's source. Every review, finding and mark is a row of
// Dispatch's changes log naming its ticket, so the ticket's mark, its
// changes' last seq read before anything else, moves with each of them,
// and an entry is never older than its mark.
//
//   - A batch is review.batch (decided: the reviewer's Request changes,
//     Comment or Approve, recorded with its receipt), bound to its
//     candidate (its run, its commit, the candidate's name as the
//     revision) and its base (said in its summary), its words its summary
//     whole; reviews to the attempt it reviewed, and reopened to the
//     repair attempt once Yardmaster applied it.
//   - Each finding is review.finding (said: the person's words as RV1
//     keeps them, with the lines they saw and the resolution they asked
//     for), on the same candidate, its outcome its state now (its latest
//     mark's verdict, RV1's rule); reviews to the attempt it was made on, a
//     check finding cites its receipt.
//   - A finding whose state is resolved has an entry of its own,
//     review.resolved (checked), from the mark that resolved it: the later
//     candidate of another commit it was verified on, as RV1's mark says
//     (a resolved mark is refused on the finding's own commit), linked
//     resolves to the finding, which stays said. A finding reopened or
//     withdrawn since has none: an entry whose fact is gone leaves with it.
//
// RV1 owns a finding's identity across revisions and its state; the
// history shows them and adds no field.

// The links of a review's entries beside reviews, reopened and cites.
const relResolves = "resolves" // a finding's verified resolution to the finding

// The keys of a review's entries, each its own ref: a review's id, a
// finding's and a mark's are Dispatch's ids, never reused.
func hReviewRef(ticket, id string) string   { return hTicketRef(ticket) + "#review/" + id }
func hFindingRef(ticket, id string) string  { return hTicketRef(ticket) + "#finding/" + id }
func hResolvedRef(ticket, id string) string { return hTicketRef(ticket) + "#resolved/" + id }

// isReviewKind says an entry is a review's: its batch, a finding of it (a
// person's, from Dispatch: an agent reviewer's finding is an attempt's)
// or a finding's verified resolution.
func isReviewKind(e history.Entry) bool {
	switch e.Kind {
	case "review.batch", "review.resolved":
		return true
	case "review.finding":
		return strings.HasPrefix(e.Key, "ticket:")
	}
	return false
}

// readReviews reads every review of the ticket, newest first, each whole
// (its findings, each with its state and its latest mark), through
// Dispatch's own reads. A review gone between the page and its read is
// left out.
func readReviews(ctx context.Context, ds *dispatch.Store, key string) ([]dispatch.Review, error) {
	var out []dispatch.Review
	for before := ""; ; {
		page, err := ds.Reviews(ctx, key, before)
		if err != nil {
			return nil, err
		}
		for _, s := range page.Reviews {
			r, err := ds.Review(ctx, s.ID)
			if errors.Is(err, dispatch.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		if page.Next == "" {
			return out, nil
		}
		before = page.Next
	}
}

// verdictWords is a review's verdict in RV1's words.
var verdictWords = map[string]string{
	dispatch.VerdictRequestChanges: "Request changes",
	dispatch.VerdictComment:        "Comment",
	dispatch.VerdictApprove:        "Approve",
}

// reviewEntries is a ticket's reviews' entries and links, base the ticket's
// entries' (its anchor, project and key).
func reviewEntries(base history.Entry, key string, reviews []dispatch.Review) ([]history.Entry, []history.Link) {
	var out []history.Entry
	var links []history.Link
	for _, r := range reviews {
		c, err := dispatch.ParseCandidate(r.Candidate)
		if err != nil {
			continue
		}
		who := cmp.Or(r.CreatedBy, r.Actor)
		reviewed := attemptKey(c.Run, c.Task, c.Attempt)
		on := base
		on.Run, on.Commit, on.Revision = hField(c.Run), c.Commit, hField(c.Name())

		b := on
		b.Key = hReviewRef(key, r.ID)
		b.Ref, b.Kind, b.At, b.Actor, b.Outcome = b.Key, "review.batch", hAt(r.CreatedAt, base.At), hField(r.Actor), hField(r.Verdict)
		verdict := cmp.Or(verdictWords[r.Verdict], r.Verdict)
		// The title is cut from the batch alone: its verdict, its
		// candidate and its summary; who sent it leads the summary.
		b.Title = hTitle(verdict + " on " + c.Name() + prefixed(": ", r.Summary))
		b.Summary = hSummary(strings.Join(hNonEmpty(prefixed("by ", who), fmt.Sprintf("%s (%s) against base %s", c.Name(), hShort(c.Commit), hShort(r.Base)),
			countsWords(len(r.Findings), r.Counts), applicationWords(c, r.Application), key), " · "))
		b.Body = history.Clip(r.Summary, history.MaxLongBody)
		out = append(out, b)
		links = append(links, history.Link{From: b.Key, Rel: relReviews, To: reviewed})
		if a := r.Application; a.State == dispatch.ApplicationApplied && a.Run != "" && a.Attempt > 0 {
			links = append(links, history.Link{From: b.Key, Rel: relReopened, To: attemptKey(a.Run, c.Task, a.Attempt)})
		}

		for _, f := range r.Findings {
			x := on
			x.Key = hFindingRef(key, f.ID)
			x.Ref, x.Kind, x.At, x.Actor, x.Outcome = x.Key, "review.finding", hAt(f.CreatedAt, b.At), b.Actor, hField(f.State)
			x.Title = hTitle(findingHead(f, c))
			x.Summary = hSummary(strings.Join(hNonEmpty(findingWhere(f), prefixed("by ", who), "in "+verdict+" of "+c.Name(), prefixed("now ", f.State), key), " · "))
			x.Body = history.Clip(findingBody(f), history.MaxLongBody)
			out = append(out, x)
			links = append(links, history.Link{From: x.Key, Rel: relReviews, To: reviewed})
			if ch := f.Check; ch != nil && ch.Run != "" && ch.Task != "" {
				links = append(links, history.Link{From: x.Key, Rel: relCites, To: hCheckRef(ch.Run, ch.Task, ch.Attempt, ch.ID, ch.K)})
			}

			m := f.Latest
			if f.State != dispatch.FindingResolved || m == nil || m.Verdict != dispatch.FindingResolved {
				continue
			}
			mc, err := dispatch.ParseCandidate(m.Candidate)
			if err != nil || mc.Commit == c.Commit {
				continue // RV1 refuses a resolution on the finding's own code: none is verified
			}
			v := base
			v.Key = hResolvedRef(key, m.ID)
			v.Ref, v.Kind, v.At, v.Actor, v.Outcome = v.Key, "review.resolved", hAt(m.CreatedAt, x.At), hField(m.Actor), dispatch.FindingResolved
			v.Run, v.Commit, v.Revision = hField(mc.Run), mc.Commit, hField(mc.Name())
			v.Title = hTitle("Resolved on " + mc.Name() + ": " + findingHead(f, c))
			v.Summary = hSummary(strings.Join(hNonEmpty(prefixed("by ", cmp.Or(m.CreatedBy, m.Actor)),
				fmt.Sprintf("%s (%s), made on %s (%s)", mc.Name(), hShort(mc.Commit), c.Name(), hShort(c.Commit)), findingWhere(f), key), " · "))
			v.Body = hShortBody(m.Note)
			out = append(out, v)
			links = append(links, history.Link{From: v.Key, Rel: relResolves, To: x.Key})
		}
	}
	return out, dedupLinks(links)
}

// countsWords is a batch's findings by state: "3 findings: 2 open, 1
// resolved".
func countsWords(n int, c dispatch.FindingCounts) string {
	if n == 0 {
		return "no findings"
	}
	var parts []string
	for _, p := range []struct {
		n    int
		word string
	}{{c.Open, "open"}, {c.Resolved, "resolved"}, {c.Withdrawn, "withdrawn"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.word))
		}
	}
	noun := "findings"
	if n == 1 {
		noun = "finding"
	}
	return fmt.Sprintf("%d %s", n, noun) + prefixed(": ", strings.Join(parts, ", "))
}

// applicationWords is what became of a request for changes, in RV1's
// words: none for a comment or an approval.
func applicationWords(c dispatch.Candidate, a dispatch.ReviewApplication) string {
	switch a.State {
	case dispatch.ApplicationPending:
		return "pending: Yardmaster has not applied it yet"
	case dispatch.ApplicationApplied:
		return fmt.Sprintf("applied: %s#%d starts at %s in %s", c.Task, a.Attempt, hShort(c.Commit), a.Run)
	case dispatch.ApplicationRefused:
		return "refused" + prefixed(": ", hFirstLine(a.Why))
	}
	return ""
}

// findingHead is a finding's subject: its severity or intent, then its
// words (a check finding with none names its receipt).
func findingHead(f dispatch.Finding, c dispatch.Candidate) string {
	head := cmp.Or(f.Severity, f.Intent)
	words := f.Message
	if words == "" && f.Check != nil {
		words = fmt.Sprintf("the check %s failed on %s", f.Check.ID, c.Name())
	}
	return strings.Join(hNonEmpty(head, words), ": ")
}

// findingWhere is where a finding is on its candidate: its lines (on the
// base's side for a removed line), its file, its check's receipt, or the
// candidate as a whole.
func findingWhere(f dispatch.Finding) string {
	switch f.Kind {
	case dispatch.FindingKindLine:
		w := f.Path + ":" + strconv.Itoa(f.From)
		if f.To > f.From {
			w += "–" + strconv.Itoa(f.To)
		}
		if f.Side == dispatch.SideBase {
			w += " on the base"
		}
		return w
	case dispatch.FindingKindFile:
		return "the file " + f.Path
	case dispatch.FindingKindCheck:
		if f.Check != nil {
			return "the receipt " + f.Check.Receipt
		}
	}
	return "the candidate as a whole"
}

// findingBody is a finding's words as the index searches them, whole: the
// person's words, the resolution they asked for, the lines as they saw
// them, and for a check finding its receipt, the class the server worked
// out and the person's, and the person's quote of its output (never the
// ledger's tail: the receipt's own entry holds its log).
func findingBody(f dispatch.Finding) string {
	lines := hNonEmpty(f.Message, prefixed("Asked: ", f.Resolution))
	lines = append(lines, f.Lines...)
	if ch := f.Check; ch != nil {
		lines = append(lines, strings.Join(hNonEmpty("Receipt: "+ch.Receipt, prefixed("class ", ch.Class), prefixed("chosen ", ch.Chosen)), ", "))
		lines = append(lines, hNonEmpty(prefixed("Quote: ", ch.Quote))...)
	}
	return strings.Join(lines, "\n")
}

// reviewCandidate is the candidate a review's entry is on, from the entry
// alone: its run, its revision (the candidate's name, <task>#<n>) and its
// commit.
func reviewCandidate(e history.Entry) (dispatch.Candidate, bool) {
	task, n, ok := strings.Cut(e.Revision, "#")
	if !ok {
		return dispatch.Candidate{}, false
	}
	k, err := strconv.Atoi(n)
	if err != nil {
		return dispatch.Candidate{}, false
	}
	c, err := dispatch.ParseCandidate(dispatch.Candidate{Run: e.Run, Task: task, Attempt: k, Commit: e.Commit}.String())
	return c, err == nil
}

// reviewEntryURL is a review entry's page: its ticket's Review view at the
// candidate it is on (a resolution at the candidate it was verified on),
// a finding and a resolution at the findings.
func reviewEntryURL(e history.Entry) string {
	u := ticketURL(e.Ticket)
	if u == "" {
		return ""
	}
	u += "?view=review"
	if c, ok := reviewCandidate(e); ok {
		u += "&candidate=" + url.QueryEscape(c.String())
	}
	if e.Kind != "review.batch" {
		u += "&focus=findings"
	}
	return u
}
