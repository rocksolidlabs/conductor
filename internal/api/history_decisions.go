package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/history"
)

// DR1's decision records in the history (docs/plans/history.md, Later
// PRs, PR-9): a source decision:<id> per decision request the ledger
// keeps, read through the ledger's own read of the record and DR1's own
// reading of it (crew.DecisionOf), so the index shows what DR1 serves and
// adds no field to it. The request is a decision entry (a record: its
// question, its subject's revision and commit, its scope); each response
// the decision accepted is a decision.response entry (decided: its
// responder and rationale, its outcome the option chosen), linked to the
// request it answers and, once Yardmaster applied it, to what it was
// applied to (applied). A refused response decided nothing and is not
// indexed; its receipt stays on the decision's card. Whether a request
// is still open, answered, applied, superseded (a stale subject among the
// reasons) or cancelled is its availability, read from the ledger when a
// page is answered.
//
// What is indexed of a decision is what the ledger held at one read of
// it, and its mark is a digest of what its entries were built from (its
// row and its run's place, as read for them), so an entry is never older
// than its mark. A decision changes only inside a transition of the run
// that holds it (its current run), and what its entries take from that
// run changes only with the run's own record or rows, so the run's pass,
// which every such change asks for, compares each of the run's decisions'
// marks with what it reads now (markDecisions) before it commits its own
// mark: a scan that fails is made again by the pass retried. A run whose
// ledger rows are gone, its record kept or not, asks again of the
// decisions it held or opened (markRunsDecisions), each read again or
// removed, and the catch-up lists every decision the ledger keeps.

// The links of a decision's entries.
const (
	relAnswers = "answers" // a response to the request it answers
	relApplied = "applied" // an applied response to the attempt, task or run it was applied to
)

// hDecisionRef is a decision request's ref and its source's key,
// decision:dr-<n>; hResponseRef a response's, decision:dr-<n>/<k>, k the
// response's number among the decision's (DR1's receipt dr-<n>/<k>).
func hDecisionRef(id string) string { return "decision:" + id }
func hResponseRef(id string, k int) string {
	return hDecisionRef(id) + "/" + strconv.Itoa(k)
}

// decisionOfRef is the decision a decision entry's ref names: the request's
// or one of its responses'.
func decisionOfRef(ref string) (id string, n int64, ok bool) {
	rest, ok := strings.CutPrefix(ref, "decision:")
	if !ok {
		return "", 0, false
	}
	id, _, _ = strings.Cut(rest, "/")
	n, ok = ledger.ParseDecisionID(id)
	return id, n, ok
}

// isDecisionKind says an entry is a decision's: its request or a response.
func isDecisionKind(kind string) bool { return kind == "decision" || kind == "decision.response" }

// decisionStamp is what a decision's entries take from its row, which
// every change of them moves: its state, rev, the run holding it, its
// words, its successor, its application, its times and its request (an
// accepted response is written with the answer that moves its state).
func decisionStamp(d ledger.DecisionRow) string {
	sum := sha256.New()
	for _, f := range []string{d.State, strconv.Itoa(d.Rev), d.CurrentRun, d.Why, d.SupersededBy, d.Applied,
		d.AnsweredAt.UTC().Format(time.RFC3339Nano), d.ClosedAt.UTC().Format(time.RFC3339Nano), d.Request} {
		sum.Write([]byte(f))
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil)[:16])
}

// placeStamp is what a decision's entries take from the place of the run
// that holds it, as they are built from it (decisionEntries): the run's
// six fields (its machine, project, repository, tree, crew and ticket),
// the asking task's agent and the time an entry falls back to.
func placeStamp(p runPlace, d ledger.DecisionRow) string {
	agent := ""
	if d.Origin == ledger.OriginAgent {
		agent = p.agents[d.TaskID]
	}
	sum := sha256.New()
	for _, f := range []string{p.run, p.host, p.project, p.repo, p.tree, p.crew, p.ticket, agent, p.fallback.UTC().Format(time.RFC3339Nano)} {
		sum.Write([]byte(f))
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil)[:16])
}

// decisionMark is a decision source's mark, of what its entries are built
// from: S<stamp of its row>/P<stamp of its run's place>/C<current run>/O<run
// it was opened in>, then U when that run's repository was not known. The
// runs are named so that a run pruned finds the decisions it held or
// opened.
func decisionMark(d ledger.DecisionRow, p runPlace) string {
	m := "S" + decisionStamp(d) + "/P" + placeStamp(p, d) + "/C" + d.CurrentRun + "/O" + d.RunID
	if p.repoUnknown {
		m += "/U"
	}
	return m
}

// markOfRun says a decision's mark names run as the one holding it or the
// one that opened it.
func markOfRun(mark, run string) bool {
	for _, part := range strings.Split(mark, "/") {
		if part == "C"+run || part == "O"+run {
			return true
		}
	}
	return false
}

// indexDecision reads decision id through the ledger: gone, it is removed;
// else its entries are built under its current run's place and replaced
// when its mark moved. With no ledger to read, what the index holds of it
// stays: nothing says it is gone.
func (h *historyIndexer) indexDecision(ctx context.Context, ix *history.Index, id string) error {
	key := hDecisionRef(id)
	n, ok := ledger.ParseDecisionID(id)
	if !ok {
		return ix.Remove(ctx, key)
	}
	ld := h.currentLedger()
	if ld == nil {
		return nil
	}
	rec, found, err := ld.Decision(ctx, n)
	switch {
	case errors.Is(err, ledger.ErrUnavailable):
		h.setNoLedger(true)
		return nil
	case err != nil:
		return err
	case !found:
		delete(h.repoTries, key)
		return ix.Remove(ctx, key)
	}
	p, err := h.decisionPlace(ctx, ld, key, rec.CurrentRun)
	if err != nil {
		return err
	}
	mark := decisionMark(rec.DecisionRow, p)
	if old, ok, err := ix.Mark(ctx, key); err != nil {
		return err
	} else if ok && old == mark {
		return nil
	}
	entries, links := decisionEntries(p, crew.DecisionOf(rec), receiptFors(rec))
	return ix.Replace(ctx, history.Source{Key: key, Mark: mark, Generation: h.gen}, entries, links)
}

// decisionPlace is what a decision's entries take from the run that holds
// it, as that run's own pass places it: its crew, its ticket, its
// machine, its folder's repository and project, its tasks' agents. A run
// the ledger no longer has (pruned meanwhile) leaves the place bare.
func (h *historyIndexer) decisionPlace(ctx context.Context, ld indexLedger, key, run string) (runPlace, error) {
	cwd, err := h.runCwd(ctx, ld, run)
	if err != nil {
		return runPlace{}, err
	}
	repo, unknown, err := h.repoFor(ctx, key, cwd)
	if err != nil {
		return runPlace{}, err
	}
	project := ""
	if repo.common != "" {
		project = h.projectsOf(ctx).byRepo[repo.common]
	}
	var rec *crew.Run
	if b, ok, err := h.readRunRecord(run); err != nil {
		return runPlace{}, err
	} else if ok {
		var r crew.Run
		if json.Unmarshal(b, &r) == nil && r.ID == run {
			rec = &r
		}
	}
	var rows *runRows
	if row, ok, err := ld.Run(ctx, run); err != nil {
		return runPlace{}, err
	} else if ok {
		tasks, err := ld.Tasks(ctx, run)
		if err != nil {
			return runPlace{}, err
		}
		rows = &runRows{row: row, tasks: tasks}
	}
	p := runPlaceOf(run, h.host, repo, project, rec, rows)
	p.repoUnknown = unknown
	return p, nil
}

// markDecisions marks dirty each decision the run holds whose mark the
// index does not hold as it is now: its row changed, or the run's place
// did. Called from the run's pass, which every commit of the run and every
// change of its record asks for, before the pass commits the run's mark.
func (h *historyIndexer) markDecisions(ctx context.Context, ix *history.Index, ld indexLedger, p runPlace) error {
	for before := int64(0); ; {
		rows, err := ld.Decisions(ctx, ledger.DecisionFilter{CurrentRun: p.run, Before: before, Limit: ledger.MaxDecisionsPage})
		if errors.Is(err, ledger.ErrUnavailable) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, d := range rows {
			key := hDecisionRef(d.ID())
			old, ok, err := ix.Mark(ctx, key)
			if err != nil {
				return err
			}
			if !ok || old != decisionMark(d, p) {
				h.mark(key)
			}
		}
		if len(rows) < ledger.MaxDecisionsPage {
			return nil
		}
		before = rows[len(rows)-1].N
	}
}

// markRunsDecisions marks dirty every decision the index holds that run
// held or opened, by their marks: a run pruned took the final decisions it
// opened with it and handed back those an ancestor opened to the run it
// continued, and each pass reads which.
func (h *historyIndexer) markRunsDecisions(ctx context.Context, ix *history.Index, run string) error {
	for after := "decision:"; ; {
		page, err := ix.Sources(ctx, after, history.MaxSourcesPage)
		if err != nil {
			return err
		}
		for _, s := range page {
			if !strings.HasPrefix(s.Key, "decision:") {
				return nil
			}
			if markOfRun(s.Mark, run) {
				h.mark(s.Key)
			}
		}
		if len(page) < history.MaxSourcesPage {
			return nil
		}
		after = page[len(page)-1].Key
	}
}

// decisionSources is every decision the ledger keeps, as source keys,
// newest first.
func decisionSources(ctx context.Context, ld indexLedger) ([]string, error) {
	var keys []string
	for before := int64(0); ; {
		rows, err := ld.Decisions(ctx, ledger.DecisionFilter{Before: before, Limit: ledger.MaxDecisionsPage})
		if err != nil {
			return nil, err
		}
		for _, d := range rows {
			keys = append(keys, hDecisionRef(d.ID()))
		}
		if len(rows) < ledger.MaxDecisionsPage {
			return keys, nil
		}
		before = rows[len(rows)-1].N
	}
}

// decisionAnchor is the ref a decision's entries are grouped under: its
// asking task in the run that holds it, or that run for a plan's gate.
func decisionAnchor(d crew.Decision) string {
	if d.Asker != nil && d.Asker.Task != "" {
		return hTaskRef(d.Run, d.Asker.Task)
	}
	return hRunRef(d.Run)
}

// recordFor is whom a decision is for as its record keeps it (HM1): the
// person its request named when it opened and the responsibility they
// hold for it, or the person an accepted response answered for, its
// receipt's.
type recordFor struct {
	Person         string `json:"person"`
	Responsibility string `json:"responsibility,omitempty"`
}

// words is whom f is for in DR1's words: "ana, the architecture owner",
// or "ana" for a person named by name; "" for none.
func (f *recordFor) words() string {
	if f == nil || f.Person == "" {
		return ""
	}
	if w, ok := crew.ResponsibilityWords[f.Responsibility]; ok {
		return f.Person + ", the " + w
	}
	return f.Person
}

// keptRequest is a request as the ledger keeps it: DR1's request, its for
// the person the opening resolved (an object), never the label as sent.
type keptRequest struct {
	crew.DecisionRequest
	For *recordFor `json:"for,omitempty"`
}

// receiptFors is whom each response answered for, by its number, as its
// receipt in the ledger keeps it.
func receiptFors(rec ledger.DecisionRecord) map[int]*recordFor {
	out := map[int]*recordFor{}
	for _, rr := range rec.Responses {
		var b struct {
			For *recordFor `json:"for"`
		}
		if json.Unmarshal([]byte(rr.Receipt), &b) == nil && b.For != nil && b.For.Person != "" {
			out[rr.K] = b.For
		}
	}
	return out
}

// decisionEntries is a decision's entries: its request, as a record, and
// each response it accepted, as a decision, with their links; fors is whom
// each response answered for, by its number.
func decisionEntries(p runPlace, d crew.Decision, fors map[int]*recordFor) ([]history.Entry, []history.Link) {
	var req keptRequest
	_ = json.Unmarshal(d.Request, &req)
	ref := hDecisionRef(d.ID)
	base := p.base()
	base.Anchor = decisionAnchor(d)
	base.Revision, base.Commit = subjectRevision(req.Subject)
	if d.Asker != nil {
		base.Agent = hField(p.agents[d.Asker.Task])
	}
	e := base
	e.Key, e.Ref, e.Kind, e.At, e.Outcome, e.Actor = ref, ref, "decision", hAt(d.CreatedAt, p.fallback), d.State, hField(d.Member)
	e.Title = hTitle(cmp.Or(req.Question, d.ID))
	e.Summary = hSummary(strings.Join(hNonEmpty(d.ID+" "+d.Type, askedWords(d), prefixed("for ", req.For.words()), subjectWords(req.Subject), authorizesWords(req.Authorizes)), " · "))
	e.Body = history.Clip(requestBody(req.DecisionRequest, crew.DecisionOptions(d.Type, string(d.Request))), history.MaxLongBody)
	out := []history.Entry{e}
	var links []history.Link
	options := crew.DecisionOptions(d.Type, string(d.Request))
	for _, rc := range d.Responses {
		if rc.Status != ledger.ResponseAccepted || rc.K < 1 {
			continue // a refusal decided nothing: its receipt stays on the card
		}
		rref := hResponseRef(d.ID, rc.K)
		x := base
		// The actor is the response row's (DecisionOf), never one the index
		// supplies.
		x.Key, x.Ref, x.Kind, x.At, x.Actor = rref, rref, "decision.response", hAt(rc.At, e.At), hField(rc.Actor)
		x.Outcome = responseOutcome(d, rc)
		x.Title = hTitle(responseTitle(d.ID, answerOfReceipt(rc, options), cmp.Or(rc.ByName, rc.Actor), fors[rc.K].words()))
		x.Summary = hSummary(d.ID + " asked: " + req.Question)
		x.Body = history.Clip(strings.Join(hNonEmpty(rc.Text, prefixed("Why: ", rc.Rationale)), "\n"), history.MaxLongBody)
		out = append(out, x)
		links = append(links, history.Link{From: rref, Rel: relAnswers, To: ref})
		if app := d.Applied; app != nil && app.Response == rc.K {
			for _, to := range appliedTo(*app) {
				links = append(links, history.Link{From: rref, Rel: relApplied, To: to})
			}
		}
	}
	return out, dedupLinks(links)
}

// responseTitle is a response in DR1's words: "dr-1: Reject, by Nate for
// ana, the architecture owner", each part said only when its record holds
// it.
func responseTitle(id, answer, by, forWords string) string {
	who := ""
	if by != "" {
		who = "by " + by
	}
	if forWords != "" {
		who = strings.TrimSpace(who + " for " + forWords)
	}
	t := id + ": " + cmp.Or(answer, "answered")
	if who != "" {
		t += ", " + who
	}
	return t
}

// responseOutcome is a response's outcome, the option it chose in the
// search's words: a gate's approval approved or rejected (DR1's
// rejection, which cancels the tasks it held), an assignment done or
// not_done, anything else answered (its words, the option's label among
// them, are in its title).
func responseOutcome(d crew.Decision, rc crew.ResponseReceipt) string {
	if rc.Choice == nil {
		return "answered"
	}
	switch {
	case d.Type == ledger.DecisionAssignment && *rc.Choice == 1:
		return "not_done"
	case d.Type == ledger.DecisionAssignment:
		return "done"
	case d.Type == ledger.DecisionApproval && d.Origin == ledger.OriginPlan && *rc.Choice == 1:
		return "rejected"
	case d.Type == ledger.DecisionApproval && d.Origin == ledger.OriginPlan && *rc.Choice == 0:
		return "approved"
	}
	return "answered"
}

// answerOfReceipt is a response in words, as DR1 says it: the chosen
// option's label, then its text.
func answerOfReceipt(rc crew.ResponseReceipt, options []string) string {
	answer := ""
	if rc.Choice != nil && *rc.Choice >= 0 && *rc.Choice < len(options) {
		answer = options[*rc.Choice]
	}
	if rc.Text != "" {
		if answer != "" {
			answer += ": "
		}
		answer += rc.Text
	}
	return answer
}

// appliedTo is what an application receipt says the answer was applied
// to: the asking attempt (typed into it or put in its task's next file),
// a gate's tasks (released or cancelled), else its run (noted).
func appliedTo(app crew.Application) []string {
	switch {
	case app.Run == "":
		return nil
	case app.Task != "" && app.Attempt > 0:
		return []string{attemptKey(app.Run, app.Task, app.Attempt)}
	case app.Task != "":
		return []string{hTaskRef(app.Run, app.Task)}
	case len(app.Tasks) > 0:
		out := make([]string, 0, len(app.Tasks))
		for _, t := range app.Tasks {
			out = append(out, hTaskRef(app.Run, t))
		}
		return out
	}
	return []string{hRunRef(app.Run)}
}

// subjectRevision is a decision's subject as the index's revision and
// commit: a commit, a candidate (its task and number), a file at a commit,
// a plan revision.
func subjectRevision(s *crew.DecisionSubject) (revision, commit string) {
	if s == nil {
		return "", ""
	}
	commit = hFitID(s.Commit)
	switch s.Kind {
	case "candidate":
		if s.Task != "" {
			revision = fmt.Sprintf("%s #%d", s.Task, s.N)
		}
	case "plan":
		revision = s.Plan
	}
	return hField(revision), commit
}

// subjectWords is a decision's subject in a few words.
func subjectWords(s *crew.DecisionSubject) string {
	if s == nil {
		return ""
	}
	switch s.Kind {
	case "commit":
		return "about commit " + hShort(s.Commit)
	case "candidate":
		return fmt.Sprintf("about %s #%d's result %s", s.Task, s.N, hShort(s.Commit))
	case "artifact":
		return fmt.Sprintf("about %s at %s", s.Path, hShort(s.Commit))
	case "plan":
		return "about plan revision " + s.Plan
	}
	return ""
}

// askedWords say who asked: a task's attempt, or a plan's gate.
func askedWords(d crew.Decision) string {
	switch {
	case d.Asker != nil:
		return fmt.Sprintf("asked by %s #%d", d.Asker.Task, d.Asker.Attempt)
	case d.Gate != "":
		return "the plan's gate " + d.Gate
	}
	return ""
}

func authorizesWords(s string) string { return prefixed("a yes authorizes: ", s) }

func prefixed(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}

// requestBody is a request's words as the index searches them: its
// context, its options with their tradeoffs (the recommended one said),
// its uncertainty, what a yes authorizes and the expertise it asks for.
func requestBody(req crew.DecisionRequest, options []string) string {
	var b strings.Builder
	line := func(s string) {
		if s != "" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	line(req.Context)
	for i, label := range options {
		o := fmt.Sprintf("%d. %s", i+1, label)
		if i < len(req.Options) && req.Options[i].Tradeoffs != "" {
			o += ": " + req.Options[i].Tradeoffs
		}
		if req.Recommendation != nil && *req.Recommendation == i {
			o += " (recommended)"
		}
		line(o)
	}
	line(prefixed("Uncertainty: ", req.Uncertainty))
	line(prefixed("A yes authorizes: ", req.Authorizes))
	line(prefixed("Expertise: ", strings.Join(req.Expertise, ", ")))
	return strings.TrimSuffix(b.String(), "\n")
}

// decisionPageURL is the page a decision opens on: its card over the run
// that holds it, at its asking task (the notification's URL too).
func decisionPageURL(run, task, id string) string {
	u := "/runs/" + url.PathEscape(run) + "?view=tasks"
	if task != "" {
		u += "&task=" + url.QueryEscape(task)
	}
	return u + "&decision=" + url.QueryEscape(id)
}

// decisionEntryURL is a decision entry's page, from the entry alone: its
// run and the task its anchor names.
func decisionEntryURL(e history.Entry) string {
	id, _, ok := decisionOfRef(e.Ref)
	if !ok || e.Run == "" {
		return ""
	}
	task := ""
	if rest, ok := strings.CutPrefix(e.Anchor, "task:"+e.Run+"/"); ok {
		task = rest
	}
	return decisionPageURL(e.Run, task, id)
}

// decisionURL is the page of the decision entry a link's end names, by its
// key: the entry as the index holds it, read once for the reply ("" when
// it holds none). A link's end carries no run of its own.
func (av *availability) decisionURL(key string) string {
	if u, ok := av.decisionURLs[key]; ok {
		return u
	}
	u := ""
	if av.ix != nil {
		if h, err := av.ix.Entry(av.ctx, key); err == nil && isDecisionKind(h.Kind) {
			u = decisionEntryURL(h.Entry)
		}
	}
	av.decisionURLs[key] = u
	return u
}

// decision is a decision entry's availability: its state now, by the
// ledger's own read once for the reply (the request's and its
// responses'): open, answered, applied, superseded (by its successor,
// a stale subject among the reasons, in DR1's words), cancelled or
// expired; pruned once the ledger no longer keeps it; unknown when the
// ledger could not be read.
func (av *availability) decision(ref string) availabilityOf {
	id, n, ok := decisionOfRef(ref)
	if !ok {
		return availabilityOf{State: availKept, Words: "kept"}
	}
	if out, ok := av.decisions[n]; ok {
		return out
	}
	out := availabilityOf{State: availUnknown, Words: "the ledger is not open here"}
	if av.ld != nil {
		rec, found, err := av.ld.Decision(av.ctx, n)
		switch answerOf(found, err) {
		case answerNo:
			out = availabilityOf{State: availPruned, Words: "pruned: the ledger no longer keeps " + id}
		case answerYes:
			out = decisionState(rec.DecisionRow)
		default:
			out = availabilityOf{State: availUnknown, Words: "the ledger could not be read; try again"}
		}
	}
	av.decisions[n] = out
	return out
}

// maxDecisionWords bounds a decision's availability words: DR1's why is
// at most 2 KiB.
const maxDecisionWords = 300

// decisionState is a decision's state now in words.
func decisionState(d ledger.DecisionRow) availabilityOf {
	words := ""
	switch d.State {
	case ledger.DecisionOpen:
		words = "open: waiting for its answer"
	case ledger.DecisionAnswered:
		words = "answered; Yardmaster has not applied it yet"
	case ledger.DecisionApplied:
		words = "applied by Yardmaster"
	case ledger.DecisionSuperseded:
		words = "superseded"
		if d.SupersededBy != "" {
			words += " by " + d.SupersededBy
		}
		words += prefixed(": ", d.Why)
	default:
		words = d.State + prefixed(": ", d.Why)
	}
	return availabilityOf{State: d.State, Words: history.Clip(words, maxDecisionWords)}
}
