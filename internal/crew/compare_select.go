package crew

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A comparison's selection (docs/plans/compare-crew.md, PR-6a): one DR1
// record of origin compare, which Yardmaster opens in the change that
// finds every lane of a compare run ended, its options the lanes'
// candidates with a standing result, each naming its exact commit, and
// None of these. The operator answers it, with a rationale; the answer is
// applied as how selected, and from then on only the selected lane takes
// a task command. The record stays so only while every lane has ended and
// shows the candidate its option names (I8): any change to a lane before
// the application supersedes it, in that change, and a new one opens once
// every lane has ended again. A stop, a shutdown and a restart leave it as
// it was; the operator's cancel is refused for it, so only an answer, None
// of these among them, or a lane's change ends it.

// AppliedSelection is the applied selection of a comparison's chain: its
// record, and the selected candidate, or None for None of these.
type AppliedSelection struct {
	Decision string `json:"decision"`
	None     bool   `json:"none,omitempty"`
	Task     string `json:"task,omitempty"`
	Run      string `json:"run,omitempty"`
	Attempt  int    `json:"attempt,omitempty"`
	Commit   string `json:"commit,omitempty"`
}

// selectState is what a compare run keeps of its selection, under e.mu.
type selectState struct {
	// applied is the chain's applied selection (this run's application,
	// or the one a continuation read from the ledger); nil before.
	applied *AppliedSelection
	// tried says the lanes as they stand were looked at with every lane
	// ended, and a selection opened or not: nothing opens again until a
	// lane runs again or a selection is superseded.
	tried bool
	// last is the id of the selection last superseded, which the next
	// one's request names (supersedes).
	last string
	// stepping is set while the compare step settles the run: the one
	// place a compare run with no applied selection derives its outcome.
	stepping bool
	// ended is why the comparison ended with no selection opened ("" when
	// one opened, or none was looked for).
	ended string
	// measured is the comparison read ahead of a change that may open the
	// selection (measureAhead), nil before.
	measured *aheadMeasure
}

// aheadMeasure is the comparison as the ledger held it with the run at
// revision rev: the comparison of the state the next change starts from,
// for as long as no change of the run commits after it (measuredNow).
type aheadMeasure struct {
	rev int64
	cmp Comparison
}

// The selection's words (the plan's, Which record and Opening).
const (
	selectAuthorizes = "Continuing the selected candidate into review and repair, and delivering it. Final: the candidates not selected are kept and cannot be continued; a new comparison tries them again."
	noneOfThese      = "None of these"
	noCandidateWords = "no candidate has a result: nothing to select"
	// shortCommit is how much of a commit an option's label names.
	shortCommit = 7
)

// noRoomWords say why a comparison with candidates opened no selection:
// the run has no room for one more decision.
func noRoomWords() string {
	return fmt.Sprintf("this run has recorded %s decisions: no room for a selection: continue the run to select in its continuation, or launch a new comparison", thousands(ledger.MaxDecisionsPerRun))
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// selectionSubject is a selection's subject and its options' (the
// ledger's subject shape): the comparison, {kind: comparison, run, base,
// digest}, or a candidate, {kind: candidate, run, task, n, commit}.
type selectionSubject struct {
	Kind   string `json:"kind"`
	Run    string `json:"run"`
	Task   string `json:"task,omitempty"`
	N      int    `json:"n,omitempty"`
	Commit string `json:"commit,omitempty"`
	Base   string `json:"base,omitempty"`
	Digest string `json:"digest,omitempty"`
}

type selectionOption struct {
	Label     string            `json:"label"`
	Tradeoffs string            `json:"tradeoffs,omitempty"`
	Subject   *selectionSubject `json:"subject,omitempty"`
}

// selectionBody is a selection's request as the ledger's request column
// keeps it.
type selectionBody struct {
	Question       string            `json:"question"`
	Context        string            `json:"context,omitempty"`
	Options        []selectionOption `json:"options"`
	Recommendation *int              `json:"recommendation"`
	Authorizes     string            `json:"authorizes"`
	Subject        *selectionSubject `json:"subject"`
	Supersedes     string            `json:"supersedes,omitempty"`
	For            *DecisionFor      `json:"for,omitempty"`
}

// selectionCands is the candidate each option of a selection's request
// names, in order, nil for None of these.
func selectionCands(request string) []*SettledCandidate {
	var b selectionBody
	_ = json.Unmarshal([]byte(request), &b)
	out := make([]*SettledCandidate, len(b.Options))
	for i, o := range b.Options {
		if s := o.Subject; s != nil && s.Kind == "candidate" {
			out[i] = &SettledCandidate{Task: s.Task, Run: s.Run, Attempt: s.N, Commit: s.Commit}
		}
	}
	return out
}

// selectionLanes reads the lanes as they stand: whether every lane has
// ended (its candidate task finished, its review finished or doomed), and
// the lanes' candidates with a standing result, in the block's order,
// each named by the run its attempt ran in (an imported one's source).
// The caller holds e.mu.
func (r *run) selectionLanes() (ended bool, opts []SettledCandidate) {
	if r.compare == nil {
		return false, nil
	}
	// The lanes as the settling reads them (compare_step.go): one rule of
	// a lane's end and of its shown candidate.
	ended = true
	for _, cd := range r.compare.Candidates {
		if !r.laneEnded(cd.Task) {
			ended = false
		}
	}
	for _, c := range r.settledSet() {
		if c.Commit != "" {
			opts = append(opts, c)
		}
	}
	return ended, opts
}

// waitingSelection is the run's selection that is open, or answered and
// not applied; nil for none. The caller holds e.mu.
func (r *run) waitingSelection() *decision {
	for _, d := range r.decisions {
		if d.origin == ledger.OriginCompare && d.waits() {
			return d
		}
	}
	return nil
}

// selectionHolds says a compare run's outcome waits: no selection of its
// chain is applied, and either one is open or answered, or the compare
// step has not yet settled the change (settleRun outside the step). The
// caller holds e.mu.
func (r *run) selectionHolds() bool {
	if r.compare == nil || r.sel.applied != nil {
		return false
	}
	return !r.sel.stepping || r.waitingSelection() != nil
}

// SelectionApplied says the chain's selection is applied (with a
// candidate or None of these): the lanes settle no more. The caller holds
// e.mu.
func (r *run) selectionApplied() bool { return r.sel.applied != nil }

// sameCands says a selection's options name the candidates opts are.
func sameCands(cands []*SettledCandidate, opts []SettledCandidate) bool {
	var named []SettledCandidate
	for _, c := range cands {
		if c != nil {
			named = append(named, *c)
		}
	}
	return slices.Equal(named, opts)
}

// laneMoved is what changed a selection's lanes, in words: a lane that has
// not ended, or one whose candidate is not its option's.
func (r *run) laneMoved(d *decision, opts []SettledCandidate) string {
	for _, cd := range r.compare.Candidates {
		if !r.laneEnded(cd.Task) {
			return fmt.Sprintf("%s runs again", cd.Task)
		}
	}
	for _, cd := range r.compare.Candidates {
		var was, now *SettledCandidate
		for _, c := range d.cands {
			if c != nil && c.Task == cd.Task {
				was = c
			}
		}
		for i := range opts {
			if opts[i].Task == cd.Task {
				now = &opts[i]
			}
		}
		switch {
		case was == nil && now != nil:
			return fmt.Sprintf("%s now has a result at %s", cd.Task, shortOf(now.Commit))
		case was != nil && now == nil:
			return fmt.Sprintf("%s no longer has a result", cd.Task)
		case was != nil && *was != *now:
			return fmt.Sprintf("%s now shows %s, attempt %d", cd.Task, shortOf(now.Commit), now.Attempt)
		}
	}
	return "a lane changed"
}

func shortOf(commit string) string { return commit[:min(len(commit), shortCommit)] }

// selectStep keeps a compare run's selection current, first in the
// compare step of every change of the run: an open or answered selection
// whose lanes are not as its options name them (a lane not ended, or one
// showing another candidate) is superseded in this change, an answered
// one keeping its response. The caller holds e.mu, in a transaction's
// change.
func (e *Engine) selectStep(r *run) {
	if r.compare == nil || r.sel.applied != nil {
		return
	}
	ended, opts := r.selectionLanes()
	if !ended {
		r.sel.tried = false
	}
	d := r.waitingSelection()
	if d == nil || d.n == 0 || ended && sameCands(d.cands, opts) {
		return
	}
	moved := r.laneMoved(d, opts)
	why := fmt.Sprintf("%s: %s no longer lists what is compared", moved, d.id())
	e.closeDecision(r, d, ledger.DecisionSuperseded, why, "yardmaster", decisionOp{}, map[string]any{"moved": moved}, "")
	r.sel.last, r.sel.tried = d.id(), false
}

// selectEnd ends the compare step of every change of a compare run: the
// selection opened when every lane has ended and none of the chain is
// open, answered or applied, then the run settled, the one place a compare
// run with no applied selection derives its outcome, so a change that
// closes the comparison (a settling that opens none) records the run's end
// in that change, and one that opens a selection records none. The caller
// holds e.mu, in a transaction's change.
func (e *Engine) selectEnd(r *run) {
	if r.compare == nil {
		return
	}
	e.openSelection(r)
	was := r.outcome
	r.sel.stepping = true
	e.settleRun(r)
	r.sel.stepping = false
	if was == "" && r.outcome != "" {
		// After the change's own events and its settling's.
		r.cmp.events = append(r.cmp.events, event{"run.finished", map[string]any{"outcome": r.outcome}})
	}
}

// openSelection opens the comparison's selection in the change under way,
// when every lane has ended, the lanes have not been looked at as they
// stand, and no selection of the chain is open, answered or applied: with
// a candidate and room for it (decisionRoom), one record, else none, the
// comparison ended with none and its words said. The caller holds e.mu, in
// a transaction's change.
func (e *Engine) openSelection(r *run) {
	if r.sel.applied != nil || r.sel.tried {
		return
	}
	ended, opts := r.selectionLanes()
	if !ended {
		return
	}
	r.sel.tried = true
	if r.waitingSelection() != nil {
		return
	}
	_, keeps := e.ledger.(decisionReader)
	switch {
	case len(opts) == 0:
		r.sel.ended = noCandidateWords
	case !keeps:
		r.sel.ended = "the ledger keeps no decisions: nothing to select"
	case !r.decisionRoom(1):
		r.sel.ended = noRoomWords()
	default:
		r.sel.ended = ""
	}
	if r.sel.ended != "" {
		r.note(session.ActivityStatus, "the comparison ended with no candidate selected: %s", r.sel.ended)
		return
	}
	body, cands := r.selectionBody(opts)
	d := &decision{origin: ledger.OriginCompare, typ: ledger.DecisionApproval, question: body.Question, state: ledger.DecisionOpen, rev: 1, cands: cands}
	for _, o := range body.Options {
		d.options = append(d.options, o.Label)
	}
	r.decisions = append(r.decisions, d)
	r.dec.opened++
	raw, _ := json.Marshal(body)
	sum := sha256.Sum256(raw)
	row := ledger.DecisionRow{Origin: ledger.OriginCompare, Type: ledger.DecisionApproval, Fingerprint: hex.EncodeToString(sum[:]), Request: string(raw), Policy: policyJSON}
	if r.pkg != nil {
		row.Ticket, row.Plan, row.PlanDigest = r.pkg.p.Ticket.ID, r.pkg.p.Plan.ID, r.pkg.p.Plan.Digest
	}
	supersedes, launched := body.Supersedes, body.Subject.Run
	var opened int64
	r.later(func(ltx LedgerTx) error {
		dtx, err := decisionsOf(ltx)
		if err != nil {
			return err
		}
		got, _, err := dtx.OpenDecision(row)
		if err != nil {
			return err
		}
		opened = got.N
		r.opened(d, got.N)
		if _, _, err := dtx.History(got.N, ledger.HistoryRow{Kind: ledger.HistoryOpened, By: "yardmaster", Detail: historyDetail(map[string]any{"comparison": launched, "options": len(cands), "follows": supersedes})}); err != nil {
			return err
		}
		if _, _, err := dtx.History(got.N, ledger.HistoryRow{Kind: ledger.HistoryRouted, By: policyLocal, Detail: routedDetail(body.For)}); err != nil {
			return err
		}
		return ltx.Event("decision.opened", map[string]any{"decision": got.ID(), "origin": ledger.OriginCompare, "type": ledger.DecisionApproval, "follows": supersedes})
	}, func() {
		d.n = opened
		r.note(session.ActivityStatus, "every lane has ended: the selection %s is open for the operator", d.id())
	})
}

// selectionBody is the selection's request over the lanes' candidates
// opts: the question, one option per candidate and None of these, the
// comparison as its subject with the digest of the options' subjects,
// recommending none, what an answer authorizes and the comparison in
// words; and the candidate each option names. The caller holds e.mu.
func (r *run) selectionBody(opts []SettledCandidate) (selectionBody, []*SettledCandidate) {
	what := "this comparison"
	if r.pkg != nil {
		what = cmp.Or(r.pkg.p.Ticket.Key, r.pkg.p.Ticket.ID, what)
	}
	none := -1
	body := selectionBody{Question: fmt.Sprintf("Which approach does %s continue with?", what), Recommendation: &none, Authorizes: selectAuthorizes, Supersedes: r.sel.last}
	// Whom it is for: the block's selector over the crew as launched
	// (HM1), checked at save; one that names no one now is the
	// operator's, as every selection is answered by the operator.
	if f, err := r.resolveFor(r.compare.Selector); err == nil {
		body.For = f
	}
	if utf8.RuneCountInString(body.Question) > ledger.MaxDecisionQuestion {
		body.Question = string([]rune(body.Question)[:ledger.MaxDecisionQuestion])
	}
	var cands []*SettledCandidate
	var subjects []*selectionSubject
	for _, c := range opts {
		s := &selectionSubject{Kind: "candidate", Run: c.Run, Task: c.Task, N: c.Attempt, Commit: c.Commit}
		label := fmt.Sprintf("%s at %s", c.Task, shortOf(c.Commit))
		if utf8.RuneCountInString(label) > ledger.MaxOptionLabel {
			label = string([]rune(label)[:ledger.MaxOptionLabel])
		}
		body.Options = append(body.Options, selectionOption{Label: label, Tradeoffs: r.tradeoffs(c), Subject: s})
		c := c
		cands = append(cands, &c)
		subjects = append(subjects, s)
	}
	body.Options = append(body.Options, selectionOption{Label: noneOfThese, Tradeoffs: "Ends the comparison with no candidate selected."})
	cands = append(cands, nil)
	raw, _ := json.Marshal(subjects)
	sum := sha256.Sum256(raw)
	body.Subject = &selectionSubject{Kind: "comparison", Run: r.launchedRun(), Base: r.base, Digest: hex.EncodeToString(sum[:])}
	body.Context = r.selectionContext(opts)
	return body, cands
}

// launchedRun is the comparison's launched run: the oldest of the run's
// chain. The caller holds e.mu.
func (r *run) launchedRun() string {
	if n := len(r.dec.chain); n > 0 {
		return r.dec.chain[n-1]
	}
	return r.id
}

// measureAhead reads the comparison before a change that may open the
// selection: one that may end the last lane still running (task's, a
// candidate or a review of one), with no selection of the chain applied.
// The opening's words are the comparison's verdicts (tradeoffs), and the
// comparison is git and the ledger, which no change reads under e.mu; so
// it is read here, before the change, and kept with the run's revision it
// was read at, which is taken first: a change of the run committed after
// it leaves the read unused (measuredNow). The caller holds no lock.
func (e *Engine) measureAhead(ctx context.Context, r *run, task string) {
	if r.compare == nil {
		return
	}
	e.mu.Lock()
	lane := cmp.Or(r.lanes[task], task)
	want := r.sel.applied == nil && !r.carried()
	for _, cd := range r.compare.Candidates {
		if cd.Task != lane && !r.laneEnded(cd.Task) {
			want = false
		}
	}
	rev := r.revision
	e.mu.Unlock()
	if !want {
		return
	}
	c, revs, err := e.comparisonAt(ctx, r.id)
	if err != nil || revs[r.id] != rev {
		return
	}
	e.mu.Lock()
	if r.revision == rev {
		r.sel.measured = &aheadMeasure{rev: rev, cmp: c}
	}
	e.mu.Unlock()
}

// carried says the run holds a change the ledger has not written (a
// failed commit's, carried to the next): the state is ahead of the
// ledger's rows at the run's revision. The caller holds e.mu.
func (r *run) carried() bool {
	return len(r.pendingEvents) > 0 || len(r.pendingMore) > 0 || len(r.pendingLast) > 0
}

// measuredNow is the comparison read ahead (measureAhead) while it is the
// comparison of the state the change under way started from: nothing of
// the run committed or carried since; nil otherwise. The caller holds
// e.mu.
func (r *run) measuredNow() *Comparison {
	m := r.sel.measured
	if m == nil || m.rev != r.revision || r.carried() {
		return nil
	}
	return &m.cmp
}

// notCompared is said of a candidate's checks when the comparison was not
// read before the opening (a continuation's first change, a stop, a change
// another one overtook): what it holds of them is the comparison's.
const notCompared = "not compared when this opened: the comparison says whether its checks compare"

// tradeoffs are an option's facts in words: the attempt, then what the
// comparison says of each of the candidate's checks (its outcome and the
// row's verdict, with why in the comparison's words), and whether a person
// accepted it after a failed check; with no comparison read ahead, that
// its checks passed and were not compared. The caller holds e.mu.
func (r *run) tradeoffs(c SettledCandidate) string {
	words := fmt.Sprintf("attempt %d", c.Attempt)
	if c.Run != r.id {
		words += " of " + c.Run
	}
	accepted := false
	if t := r.task(c.Task); t != nil && strings.HasPrefix(t.reason, "accepted by ") {
		accepted = true
	}
	checks, measured := "", false
	if m := r.measuredNow(); m != nil {
		checks, measured = comparedWords(*m, c)
	}
	switch {
	case measured && accepted:
		words += "; " + checks + "; accepted after a failed check"
	case measured:
		words += "; " + checks
	case accepted:
		words += "; accepted after a failed check; " + notCompared
	default:
		words += "; its checks passed, " + notCompared
	}
	return cutEncoded(words, ledger.MaxOptionTradeoffs)
}

// comparedWords is what the comparison c says of the candidate's checks, row
// by row: "check ok: passed, comparable"; ok false when c does not show
// that candidate in its lane (another attempt or commit).
func comparedWords(c Comparison, sc SettledCandidate) (string, bool) {
	i := slices.IndexFunc(c.Candidates, func(cc ComparedCandidate) bool { return cc.Task == sc.Task })
	if i < 0 {
		return "", false
	}
	if cc := c.Candidates[i]; cc.Run != sc.Run || cc.Attempt != sc.Attempt || cc.Commit != sc.Commit {
		return "", false
	}
	if len(c.Checks) == 0 {
		return "no check is compared", true
	}
	var rows []string
	for _, row := range c.Checks {
		outcome := "not run"
		if j := slices.IndexFunc(row.Cells, func(cell CheckCell) bool { return cell.Task == sc.Task }); j >= 0 {
			outcome = cellWords(row.Cells[j])
		}
		rows = append(rows, fmt.Sprintf("%s %s: %s, %s", row.Phase, row.ID, outcome, verdictWords(row)))
	}
	return strings.Join(rows, "; "), true
}

// cellWords is a candidate's run of one check in words.
func cellWords(cell CheckCell) string {
	switch {
	case cell.State == CellNotRun:
		return "not run"
	case cell.State == CellCancelled:
		return "cancelled"
	case cell.State != ledger.CheckFinished:
		return fmt.Sprintf("did not finish (%s)", cell.State)
	case cell.TimedOut:
		return "timed out"
	case cell.Signal != "":
		return fmt.Sprintf("failed (%s)", cell.Signal)
	case cell.Exit != nil && *cell.Exit != 0:
		return fmt.Sprintf("failed (exit %d)", *cell.Exit)
	}
	return "passed"
}

// verdictWords is a check row's verdict in words, why in the comparison's
// own words: "comparable"; "not comparable (approach-a changed check.sh,
// which the check runs)"; "comparable only if their test changes are fair
// (approach-a changed x_test.go, a test file; approach-b changed none)".
func verdictWords(row ComparedCheck) string {
	switch row.Verdict {
	case VerdictComparable:
		return "comparable"
	case VerdictNotComparable:
		if len(row.Why) == 0 {
			return "not comparable"
		}
		return "not comparable (" + strings.Join(row.Why, "; ") + ")"
	}
	var why []string
	for _, w := range row.Why {
		if head, tail, ok := strings.Cut(w, ": "); ok {
			w = head + " (" + tail + ")"
		}
		why = append(why, w)
	}
	if len(why) == 0 {
		return "not known to be comparable"
	}
	return strings.Join(why, "; ")
}

// selectionContext is the comparison at the opening in words, within the
// ledger's bound: the base, the criteria, and each lane's candidate with
// what the comparison says of its checks, or why it has none and what the
// comparison says of its last commit's. The caller holds e.mu.
func (r *run) selectionContext(opts []SettledCandidate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Every candidate started at %s.", shortOf(r.base))
	if len(r.compare.Criteria) > 0 {
		b.WriteString(" Criteria:")
		for _, cr := range r.compare.Criteria {
			fmt.Fprintf(&b, " %s: %s;", cr.ID, cr.Text)
		}
	}
	m := r.measuredNow()
	for _, cd := range r.compare.Candidates {
		i := slices.IndexFunc(opts, func(c SettledCandidate) bool { return c.Task == cd.Task })
		t := r.task(cd.Task)
		switch {
		case i >= 0:
			fmt.Fprintf(&b, " %s: %s, %s.", cd.Task, shortOf(opts[i].Commit), r.tradeoffs(opts[i]))
		case t != nil:
			fmt.Fprintf(&b, " %s: no result (%s)", cd.Task, t.state)
			if last, ok := lastCompared(m, cd.Task); ok {
				if words, ok := comparedWords(*m, last); ok {
					fmt.Fprintf(&b, "; at %s, attempt %d: %s", shortOf(last.Commit), last.Attempt, words)
				}
			}
			b.WriteString(".")
		}
	}
	out := strings.TrimSpace(oneLine.Replace(b.String()))
	if utf8.RuneCountInString(out) > ledger.MaxDecisionContext {
		out = string([]rune(out)[:ledger.MaxDecisionContext])
	}
	return out
}

// lastCompared is the commit the comparison m shows in the lane of task,
// for a lane with no standing result: its last submitted one; ok false
// for none, or no comparison.
func lastCompared(m *Comparison, task string) (SettledCandidate, bool) {
	if m == nil {
		return SettledCandidate{}, false
	}
	i := slices.IndexFunc(m.Candidates, func(cc ComparedCandidate) bool { return cc.Task == task })
	if i < 0 || m.Candidates[i].Commit == "" {
		return SettledCandidate{}, false
	}
	cc := m.Candidates[i]
	return SettledCandidate{Task: task, Run: cc.Run, Attempt: cc.Attempt, Commit: cc.Commit}, true
}

// selectionApplication is a selection's application: the selected
// candidate, or None of these. The caller holds e.mu, in the application's
// change.
func selectionApplication(d *decision, ans *accepted, app *Application) AppliedSelection {
	sel := AppliedSelection{Decision: d.id()}
	if ans.choice != nil && *ans.choice >= 0 && *ans.choice < len(d.cands) && d.cands[*ans.choice] != nil {
		c := *d.cands[*ans.choice]
		sel.Task, sel.Run, sel.Attempt, sel.Commit = c.Task, c.Run, c.Attempt, c.Commit
		app.Selected, app.Task, app.Attempt = &c, c.Task, c.Attempt
		app.Words = fmt.Sprintf("%s selected at %s", c.Task, shortOf(c.Commit))
	} else {
		sel.None, app.None = true, true
		app.Words = "None of these: no candidate selected"
	}
	app.How = HowSelected
	return sel
}

// selectionOf is a chain's applied selection as a record keeps it, nil
// when the record is no applied selection.
func selectionOf(row ledger.DecisionRow) *AppliedSelection {
	if row.Origin != ledger.OriginCompare || row.State != ledger.DecisionApplied {
		return nil
	}
	app := applicationOf(row.Applied)
	if app.How != HowSelected {
		return nil
	}
	sel := &AppliedSelection{Decision: row.ID(), None: app.None}
	if c := app.Selected; c != nil && !app.None {
		sel.Task, sel.Run, sel.Attempt, sel.Commit = c.Task, c.Run, c.Attempt, c.Commit
	} else {
		sel.None = true
	}
	return sel
}

// The states of a comparison (the plan's A comparison's states), derived
// from the chain and its records, never stored.
const (
	CompareRunning   = "running"
	CompareSelecting = "selecting"
	CompareAnswered  = "answered"
	CompareSelected  = "selected"
	CompareNone      = "none"
	CompareEnded     = "ended"
)

// ComparisonState is where a comparison stands: its state and the record
// of its newest selection ("" for none), with the selection when applied.
type ComparisonState struct {
	State    string            `json:"state"`
	Decision string            `json:"decision,omitempty"`
	Selected *AppliedSelection `json:"selected,omitempty"`
}

// compareChain is the chain runID belongs to, launched run first, read
// from the ledger: back along continued_from, then on along continued_by,
// each way at most maxCompareChain runs.
func compareChain(ctx context.Context, cr decisionLedger, runID string) ([]ledger.RunRow, error) {
	first, ok, err := cr.RunRow(ctx, runID)
	if err != nil || !ok {
		return nil, cmpErr(err, fmt.Errorf("%w: %s", ErrRunNotFound, runID))
	}
	chain := []ledger.RunRow{first}
	seen := map[string]bool{runID: true}
	for prev := first.ContinuedFrom; prev != "" && !seen[prev] && len(chain) < maxCompareChain; {
		seen[prev] = true
		row, ok, err := cr.RunRow(ctx, prev)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		chain = append([]ledger.RunRow{row}, chain...)
		prev = row.ContinuedFrom
	}
	for next, n := first.ContinuedBy, 0; next != "" && !seen[next] && n < maxCompareChain; n++ {
		seen[next] = true
		row, ok, err := cr.RunRow(ctx, next)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		chain = append(chain, row)
		next = row.ContinuedBy
	}
	return chain, nil
}

// SelectedLane is the applied selection of the chain runID belongs to,
// read from the ledger (so it answers for a run the engine no longer
// holds): ok false when none is applied; None true for None of these.
func (e *Engine) SelectedLane(ctx context.Context, runID string) (sel AppliedSelection, ok bool, err error) {
	st, err := e.ComparisonStateOf(ctx, runID)
	if err != nil || st.Selected == nil {
		return AppliedSelection{}, false, err
	}
	return *st.Selected, true, nil
}

// ComparisonStateOf is the state of the comparison of the chain runID
// belongs to, read from the ledger: selected or none once its selection
// is applied; selecting or answered while its newest selection is open or
// answered; else running while the chain's newest run has no outcome, and
// ended once it has one (a settling that opened none, or a run that ended
// unsettled).
func (e *Engine) ComparisonStateOf(ctx context.Context, runID string) (ComparisonState, error) {
	cr, ok := e.ledger.(decisionLedger)
	if !ok {
		return ComparisonState{}, errNoDecisions
	}
	chain, err := compareChain(ctx, cr, runID)
	if err != nil {
		return ComparisonState{}, err
	}
	var newest ledger.DecisionRow
	for _, run := range chain {
		rows, err := allDecisions(ctx, cr, ledger.DecisionFilter{CurrentRun: run.ID})
		if err != nil {
			return ComparisonState{}, err
		}
		for _, row := range rows {
			if row.Origin == ledger.OriginCompare && row.N > newest.N {
				newest = row
			}
		}
	}
	last := chain[len(chain)-1]
	st := ComparisonState{State: CompareRunning}
	if newest.N != 0 {
		st.Decision = newest.ID()
	}
	switch {
	case newest.State == ledger.DecisionApplied:
		st.Selected = selectionOf(newest)
		st.State = CompareSelected
		if st.Selected == nil || st.Selected.None {
			st.State = CompareNone
		}
	case newest.State == ledger.DecisionOpen:
		st.State = CompareSelecting
	case newest.State == ledger.DecisionAnswered:
		st.State = CompareAnswered
	case last.Outcome != "":
		st.State = CompareEnded
	}
	return st, nil
}
