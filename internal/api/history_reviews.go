package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/history"
)

// RV1's review batches and findings in the history (docs/plans/history.md,
// Later PRs, PR-10): a ticket's reviews, read through Dispatch's own reads
// of them (Store.Reviews, Store.Review: what RV1's routes serve), are part
// of the ticket's source. Every review, finding and mark is a row of
// Dispatch's changes log naming its ticket, so the ticket's mark, its
// changes' last seq read before anything else, moves with each of them,
// and an entry is never older than its mark. The source holds the
// ticket's newest MaxTicketReviews reviews, one page of RV1's: an older
// review stays in the ticket's Review view, and the ticket's entry says
// the history holds only the newest (reviewsCut).
//
//   - A batch is review.batch (decided: the reviewer's Request changes,
//     Comment or Approve, recorded with its receipt), bound to its
//     candidate (its run, its commit, the candidate's name as the
//     revision) and its base (its full commit, in its summary), its words
//     its summary whole; reviews to the attempt it reviewed, and reopened
//     to the repair attempt once Yardmaster applied it.
//   - Each finding is review.finding (said: the person's words as RV1
//     keeps them, with the lines they saw and the resolution they asked
//     for), on the same candidate, its outcome its state now (its latest
//     mark's verdict, RV1's rule; a withdrawn one finding_withdrawn, an
//     outcome of its own, never a plan's withdrawal); reviews to the
//     attempt it was made on, a check finding cites its receipt.
//   - A finding whose state is resolved has an entry of its own,
//     review.resolved (checked), only when the ledger holds the
//     verification receipt of the candidate its resolving mark names: the
//     later candidate of another commit (a resolved mark is refused on the
//     finding's own commit), an ended attempt whose result it is, every
//     receipt of it passed, as candidate.verified holds it (hVerified).
//     It is linked resolves to the finding, which stays said, and cites
//     the candidate's verification. A person's resolved mark without that
//     receipt is said on the finding (resolved, not checked) and is no
//     entry of its own; one whose receipt is still on its way marks the
//     ticket U, so every reconcile reads it again until it is judged. A
//     finding reopened or withdrawn since has none: an entry whose fact is
//     gone leaves with it. What the ledger held of each resolution is part
//     of the ticket's mark (resolutionsMark), and a run whose ledger rows
//     are gone asks again for the tickets that mark names, as for its
//     decisions: a receipt pruned leaves the resolution said, not checked.
//
// RV1 owns a finding's identity across revisions and its state; the
// history shows them and adds no field.

// MaxTicketReviews is the most reviews of a ticket its source indexes,
// the newest: one page of RV1's (dispatch.ReviewsPerPage). At RV1's
// bounds (50 findings of a review, each resolved and verified) their
// entries are under MaxTicketReviewBytes, so a ticket's reviews never
// take the index's budget (TestATicketsReviewsAreBounded).
const (
	MaxTicketReviews     = dispatch.ReviewsPerPage
	MaxTicketReviewBytes = 24 << 20
)

// findingWithdrawnOutcome is a withdrawn finding's outcome: RV1's
// withdrawn, which a plan's withdrawal also is, said apart so that the
// rejected designs' outcomes (web/app/utils/history.ts) never take it.
const findingWithdrawnOutcome = "finding_withdrawn"

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

// readReviews reads the ticket's newest MaxTicketReviews reviews, each
// whole (its findings, each with its state and its latest mark), through
// Dispatch's own reads; cut says the ticket has older ones, which the
// history does not hold. A review gone between the page and its read is
// left out.
func readReviews(ctx context.Context, ds *dispatch.Store, key string) (out []dispatch.Review, cut bool, err error) {
	page, err := ds.Reviews(ctx, key, "")
	if err != nil {
		return nil, false, err
	}
	for _, s := range page.Reviews {
		if len(out) == MaxTicketReviews {
			return out, true, nil
		}
		r, err := ds.Review(ctx, s.ID)
		if errors.Is(err, dispatch.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		out = append(out, r)
	}
	return out, page.Next != "", nil
}

// resolutionCheck is what the ledger holds of the candidate a resolved
// mark names: its verification receipt (verified, its receipts' count),
// or why it holds none.
type resolutionCheck struct {
	verified bool
	receipts int
	why      string
}

// resolvedOn is the candidate a finding's resolving mark names: ok only
// for a finding resolved now by a mark on another commit than its own
// candidate's (RV1 refuses one on its own commit).
func resolvedOn(f dispatch.Finding, own dispatch.Candidate) (dispatch.Candidate, bool) {
	m := f.Latest
	if f.State != dispatch.FindingResolved || m == nil || m.Verdict != dispatch.FindingResolved {
		return dispatch.Candidate{}, false
	}
	mc, err := dispatch.ParseCandidate(m.Candidate)
	if err != nil || mc.Commit == own.Commit {
		return dispatch.Candidate{}, false
	}
	return mc, true
}

// readResolutions reads from the ledger the verification receipt of each
// candidate a resolved finding of the reviews names, as candidate.verified
// judges one (hVerified) and the delivery reads it (candidateVerified):
// the attempt read before its receipts, an ended attempt whose result is
// the candidate's commit, every receipt finished. waiting says one could
// not be judged yet (no ledger to read, its attempt not ended, a receipt
// still started or running): the ticket's mark then carries U.
func readResolutions(ctx context.Context, ld indexLedger, reviews []dispatch.Review) (out map[string]resolutionCheck, waiting bool, err error) {
	out = map[string]resolutionCheck{}
	attempts := map[string][]ledger.AttemptRow{}
	for _, r := range reviews {
		own, err := dispatch.ParseCandidate(r.Candidate)
		if err != nil {
			continue
		}
		for _, f := range r.Findings {
			mc, ok := resolvedOn(f, own)
			if _, seen := out[mc.String()]; !ok || seen {
				continue
			}
			if ld == nil {
				waiting = true
				continue
			}
			as, read := attempts[mc.Run]
			if !read {
				if as, err = ld.Attempts(ctx, mc.Run); errors.Is(err, ledger.ErrUnavailable) {
					waiting = true
					continue
				} else if err != nil {
					return nil, false, err
				}
				attempts[mc.Run] = as
			}
			i := slices.IndexFunc(as, func(a ledger.AttemptRow) bool { return a.TaskID == mc.Task && a.N == mc.Attempt })
			if i < 0 {
				out[mc.String()] = resolutionCheck{why: "the ledger holds no receipt of " + mc.Name()}
				continue
			}
			a := as[i]
			if !slices.Contains(attemptEnded, a.State) {
				waiting = true
				continue
			}
			checks, err := ld.Checks(ctx, mc.Run, mc.Task, mc.Attempt, false)
			if errors.Is(err, ledger.ErrUnavailable) {
				waiting = true
				continue
			} else if err != nil {
				return nil, false, err
			}
			cand := candidateOf("", ledger.TaskRow{}, a, checks, nil)
			if slices.ContainsFunc(cand.Receipts, func(c candidateReceipt) bool {
				return c.State == ledger.CheckStarted || c.State == ledger.CheckRunning
			}) {
				waiting = true
				continue
			}
			switch {
			case cand.Commit != mc.Commit:
				out[mc.String()] = resolutionCheck{why: mc.Name() + " is not that attempt's result"}
			case len(cand.Receipts) == 0:
				out[mc.String()] = resolutionCheck{why: mc.Name() + " has no receipt"}
			case !hVerified(a, cand, checks):
				out[mc.String()] = resolutionCheck{why: mc.Name() + "'s checks did not all pass"}
			default:
				out[mc.String()] = resolutionCheck{verified: true, receipts: len(cand.Receipts)}
			}
		}
	}
	return out, waiting, nil
}

// resolutionsMark is the part of a ticket's mark that what the ledger
// held of its resolutions makes, "" for none judged: /E<digest of each
// judgement: the candidate, so its run and attempt, and its receipt
// verified with its count, or why not> then /C<run> for each run those
// candidates are of, or /C* when they would not fit the mark's bound
// (history.MaxRef). A resolution's entry is built from those facts, so a
// prune that takes a receipt changes the mark the ticket's pass compares,
// and the runs named find the tickets a pruned run's receipts checked
// (markRunsResolutions).
func resolutionsMark(rs map[string]resolutionCheck) string {
	if len(rs) == 0 {
		return ""
	}
	keys := slices.Sorted(maps.Keys(rs))
	sum := sha256.New()
	var runs []string
	for _, k := range keys {
		rc := rs[k]
		for _, f := range []string{k, strconv.FormatBool(rc.verified), strconv.Itoa(rc.receipts), rc.why} {
			sum.Write([]byte(f))
			sum.Write([]byte{0})
		}
		if c, err := dispatch.ParseCandidate(k); err == nil && !slices.Contains(runs, c.Run) {
			runs = append(runs, c.Run)
		}
	}
	m := "/E" + hex.EncodeToString(sum.Sum(nil)[:16])
	slices.Sort(runs)
	named := ""
	for _, r := range runs {
		named += "/C" + r
	}
	// The ticket's own part (Q<seq>/V<n>) and /U take at most 30 bytes.
	if len(m)+len(named) > history.MaxRef-32 {
		named = "/C*"
	}
	return m + named
}

// markNamesRun says a ticket's mark was made of resolutions judged from
// run's ledger rows (or of too many runs to name).
func markNamesRun(mark, run string) bool {
	for _, part := range strings.Split(mark, "/") {
		if part == "C"+run || part == "C*" {
			return true
		}
	}
	return false
}

// markRunsResolutions marks dirty every ticket the index holds whose
// resolutions were judged from run's ledger rows, by their marks: a run
// whose rows are gone took its receipts with it, and each ticket's pass
// says its resolutions as the ledger now holds them (resolved, not
// checked, their entries gone). Called wherever a pruned run's decisions
// are asked for again (markRunsDecisions).
func (h *historyIndexer) markRunsResolutions(ctx context.Context, ix *history.Index, run string) error {
	for after := "ticket:"; ; {
		page, err := ix.Sources(ctx, after, history.MaxSourcesPage)
		if err != nil {
			return err
		}
		for _, s := range page {
			if !strings.HasPrefix(s.Key, "ticket:") {
				return nil
			}
			if markNamesRun(s.Mark, run) {
				h.mark(s.Key)
			}
		}
		if len(page) < history.MaxSourcesPage {
			return nil
		}
		after = page[len(page)-1].Key
	}
}

// verdictWords is a review's verdict in RV1's words.
var verdictWords = map[string]string{
	dispatch.VerdictRequestChanges: "Request changes",
	dispatch.VerdictComment:        "Comment",
	dispatch.VerdictApprove:        "Approve",
}

// reviewEntries is a ticket's reviews' entries and links, base the ticket's
// entries' (its anchor, project and key), resolutions what the ledger
// holds of each candidate a resolved finding names (readResolutions).
func reviewEntries(base history.Entry, key string, reviews []dispatch.Review, resolutions map[string]resolutionCheck) ([]history.Entry, []history.Link) {
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
		b.Summary = hSummary(strings.Join(hNonEmpty(prefixed("by ", who), fmt.Sprintf("%s (%s) against base %s", c.Name(), hShort(c.Commit), r.Base),
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
			if f.State == dispatch.FindingWithdrawn {
				x.Outcome = findingWithdrawnOutcome
			}
			mc, resolved := resolvedOn(f, c)
			rc, judged := resolutions[mc.String()]
			now := prefixed("now ", f.State)
			if resolved && judged && !rc.verified {
				now = fmt.Sprintf("now resolved on %s by %s, not checked: %s", mc.Name(), cmp.Or(f.Latest.CreatedBy, f.Latest.Actor), rc.why)
			}
			x.Title = hTitle(findingHead(f, c))
			x.Summary = hSummary(strings.Join(hNonEmpty(findingWhere(f), prefixed("by ", who), "in "+verdict+" of "+c.Name(), now, key), " · "))
			x.Body = history.Clip(findingBody(f), history.MaxLongBody)
			out = append(out, x)
			links = append(links, history.Link{From: x.Key, Rel: relReviews, To: reviewed})
			if ch := f.Check; ch != nil && ch.Run != "" && ch.Task != "" {
				links = append(links, history.Link{From: x.Key, Rel: relCites, To: hCheckRef(ch.Run, ch.Task, ch.Attempt, ch.ID, ch.K)})
			}

			if !resolved || !rc.verified {
				continue // none resolved on other code, or no receipt of it the ledger holds
			}
			m := f.Latest
			v := base
			v.Key = hResolvedRef(key, m.ID)
			v.Ref, v.Kind, v.At, v.Actor, v.Outcome = v.Key, "review.resolved", hAt(m.CreatedAt, x.At), hField(m.Actor), dispatch.FindingResolved
			v.Run, v.Commit, v.Revision = hField(mc.Run), mc.Commit, hField(mc.Name())
			v.Title = hTitle("Resolved on " + mc.Name() + ": " + findingHead(f, c))
			v.Summary = hSummary(strings.Join(hNonEmpty(prefixed("by ", cmp.Or(m.CreatedBy, m.Actor)),
				fmt.Sprintf("%s (%s) verified, %d receipts passed, made on %s (%s)", mc.Name(), hShort(mc.Commit), rc.receipts, c.Name(), hShort(c.Commit)), findingWhere(f), key), " · "))
			v.Body = hShortBody(m.Note)
			out = append(out, v)
			links = append(links, history.Link{From: v.Key, Rel: relResolves, To: x.Key},
				history.Link{From: v.Key, Rel: relCites, To: hCandidateRef(mc.Run, mc.Task, mc.Attempt)})
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
