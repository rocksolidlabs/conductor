package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// DR1's records in the decision inbox (docs/plans/decision-inbox.md,
// PR-3). DR1 owns the record, its store, its routes, every response and
// comment and their delivery, its receipts, its event and its four states;
// the inbox reads the record and names DR1's routes for every action,
// adding no route for a record. An item of a record is named
// "decision:<id>" (Brakeman's form) and is open while the record is open;
// an answered one, its response recorded and not yet applied, is listed
// beside them as a tracking item until DR1 gives it a final state, then
// it is in Recent (closed in the last day) for every viewer. A question
// that mirrors an open or answered record (its attention's decision) is
// that record's item: the question's own is not listed. The item does not
// carry the record (DR1's read is up to 1 MiB): the workbench reads it
// from DR1's route, a decision link through the lane's decision_record,
// in parts of one revision (lane_decisions.go).

// inboxDecision is a DR1 record's kind of item.
const inboxDecision = "decision"

// The inbox's bounds of DR1's records: Recent's age and length, and the
// fourth step's name (DR1's fourth state, applied, which is no work
// resumed: Yardmaster applied the answer).
const (
	inboxRecentAge = 24 * time.Hour
	inboxRecent    = 50
	stepApplied    = "applied"
)

// inboxStates are the states of a record that is an item: open, and
// answered (tracking).
var inboxStates = []string{ledger.DecisionOpen, ledger.DecisionAnswered}

// decisionRefN reads a decision item's ref, "decision:dr-<n>": ok false
// for a ref of another kind, an error for one of this kind that names no
// decision.
func decisionRefN(v string) (n int64, ok bool, err error) {
	id, is := strings.CutPrefix(v, ledger.DecisionRefPrefix)
	if !is {
		return 0, false, nil
	}
	n, valid := ledger.ParseDecisionID(id)
	if !valid {
		return 0, true, fmt.Errorf("%q is not a decision's ref, decision:dr-N", fitText(v, 80))
	}
	return n, true, nil
}

// recordRequest is what the inbox reads of a record's request: its
// question, options, what a yes authorizes, its evidence, and whom it is
// for (HM1, when the record names it).
type recordRequest struct {
	Question   string                `json:"question"`
	Options    []crew.DecisionOption `json:"options"`
	Authorizes string                `json:"authorizes"`
	Evidence   []string              `json:"evidence"`
	For        *struct {
		Label          string `json:"label"`
		Person         string `json:"person"`
		Responsibility string `json:"responsibility"`
	} `json:"for"`
}

// requestOf is row's request as the inbox reads it.
func requestOf(row ledger.DecisionRow) recordRequest {
	var r recordRequest
	_ = json.Unmarshal([]byte(row.Request), &r)
	return r
}

// answererOf is who answers a record: the operator (decision D4), for
// the person it names when it names one, with that person's
// responsibility in HM1's words (crew.ResponsibilityWords; one not of the
// four is left out), as DR1's card says it (approverWords,
// web/app/utils/decisions.ts): a label that named no one is the operator
// alone.
func answererOf(r recordRequest) string {
	f := r.For
	if f != nil && f.Label == crew.GrantDecideLabel && f.Person == "" {
		// Given to the decide grant (docs/plans/decide-grant.md).
		return inboxAnswerer + ", or anyone holding the decide grant"
	}
	if f == nil || f.Person == "" {
		return inboxAnswerer
	}
	if w, ok := crew.ResponsibilityWords[f.Responsibility]; ok {
		return fitText(fmt.Sprintf("%s, for %s (%s)", inboxAnswerer, f.Person, w), 120)
	}
	return fitText(inboxAnswerer+", for "+f.Person, 120)
}

// optionLabels are a record's options as a response names them by
// index: an assignment's two fixed answers.
func optionLabels(row ledger.DecisionRow, r recordRequest) []string {
	if row.Type == ledger.DecisionAssignment {
		return []string{"Done", "Not done"}
	}
	out := make([]string, 0, len(r.Options))
	for _, o := range r.Options {
		out = append(out, o.Label)
	}
	return out
}

// typeWord is a record's type as a title starts.
func typeWord(typ string) string {
	switch typ {
	case ledger.DecisionApproval:
		return "Approval"
	case ledger.DecisionAssignment:
		return "Assignment"
	}
	return "Clarification"
}

// inboxRun is a run as one read of the inbox found it: false for none.
type inboxRun struct {
	run crew.Run
	ok  bool
}

// runOf is the run id as this read found it, read once: from the engine
// or its record. What a record holds is never read here: it is read with
// the record (ledger.InboxDecision's and DecisionRecord's Held).
func (rd *inboxRead) runOf(id string) inboxRun {
	if r, seen := rd.decisionRuns[id]; seen {
		return r
	}
	if rd.decisionRuns == nil {
		rd.decisionRuns = map[string]inboxRun{}
	}
	var r inboxRun
	r.run, r.ok = rd.s.runOrRecord(id)
	rd.decisionRuns[id] = r
	return r
}

// runHeld says whether the engine holds the run and it goes on (not
// stopped, not continued by another): an answered record of it is
// applied next, else when the run is continued.
func (s *Server) runHeld(id string) bool {
	if s.runs == nil {
		return false
	}
	r, ok := s.runs.Get(id)
	return ok && r.StoppedAt == nil && r.ResumedBy == ""
}

// askedWhere says who asked the record: a plan's gate, or an agent's
// attempt.
func (rd *inboxRead) askedWhere(row ledger.DecisionRow, run inboxRun) string {
	if row.Origin == ledger.OriginPlan {
		w := "the plan's gate " + row.Gate
		if run.ok && run.run.Ticket != nil {
			w += " of " + run.run.Ticket.Key
		}
		return w
	}
	agent := "the agent"
	if d, ok := rd.s.registry.Get(row.SessionID); ok && row.SessionID != "" {
		agent = rd.s.agentName(d.Info())
	}
	return fmt.Sprintf("%s in attempt %s.%d", agent, row.TaskID, row.Attempt)
}

// decisionItem is the item of DR1's record row and held, the tasks it
// holds, both as one read of the ledger found them (one read, so what a
// response authorizes and what waits for it are said of the rev it is
// taken at: a continuation that moves the record and its holds is seen
// whole or not at all); rec, when given, is the whole record of the same
// read, whose four states the item carries (its delivery). Its actions
// name DR1's routes: a response through respond, more information and a
// comment through comment, each at the record's rev; only an open record
// is answered, and an answered one takes a comment alone. A card of
// Brakeman's about the item's ref fills what it fills of any item.
func (rd *inboxRead) decisionItem(row ledger.DecisionRow, held []string, rec *ledger.DecisionRecord) *inboxItem {
	if seam := rd.s.decisionItemSeam; seam != nil {
		seam(row.N)
	}
	req := requestOf(row)
	id, ref, rev := row.ID(), ledger.DecisionRef(row.N), strconv.Itoa(row.Rev)
	run := rd.runOf(row.CurrentRun)
	it := &inboxItem{Ref: ref, Subject: ref, Kind: inboxDecision, Since: row.CreatedAt, Question: req.Question, Answerer: answererOf(req),
		Revision: rev, State: row.State, BriefBy: "conductor", Actions: []inboxAction{}}
	runName := row.CurrentRun
	if run.ok {
		runName = run.run.Name
	}
	where := rd.askedWhere(row, run)
	it.Title = fitText(fmt.Sprintf("%s %s: %s", typeWord(row.Type), id, req.Question), 200)
	brief := fmt.Sprintf("%s asks %s, a durable decision of run %s", capitalize(where), id, runName)
	if run.ok && run.run.Ticket != nil {
		brief += " for " + run.run.Ticket.Key
	}
	it.Brief = fitText(brief, 2<<10)
	rd.decisionBlocking(it, row, run, held, where)
	rd.decisionEvidence(it, row, run, req)
	switch row.State {
	case ledger.DecisionOpen:
		rd.decisionActions(it, row, req, held, where)
	case ledger.DecisionAnswered:
		it.Now = fitText(id+" is answered and not yet applied: "+answeredWaits(rd.s.runHeld(row.CurrentRun)), 500)
		rd.commentAction(it, id, rev)
	default:
		it.Now = fitText(fmt.Sprintf("%s is %s%s", id, row.State, cmpPrefix("; ", row.Why)), 500)
	}
	if rec != nil {
		it.Delivery = recordSteps(*rec, rd.s.runHeld(row.CurrentRun))
	}
	rd.withCard(it)
	return it
}

// capitalize is s with its first letter upper case: its first rune whole,
// the rest of its bytes as they are.
func capitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// cmpPrefix is p and s, or "" for an empty s.
func cmpPrefix(p, s string) string {
	if s == "" {
		return ""
	}
	return p + s
}

// answeredWaits says what an answered record waits for: Yardmaster's
// application, at once while the engine holds its run, else once the run
// is continued.
func answeredWaits(held bool) string {
	if held {
		return "Yardmaster applies it next"
	}
	return "it waits for its run to be continued, which applies it"
}

// decisionBlocking says what waits for the record: the tasks it holds,
// their dependents, the asking attempt, and the run's end.
func (rd *inboxRead) decisionBlocking(it *inboxItem, row ledger.DecisionRow, run inboxRun, held []string, where string) {
	id := row.ID()
	// Each task once, before the item's bound counts what it cannot keep:
	// held tasks share dependents.
	var tasks []string
	add := func(t string) {
		if !slices.Contains(tasks, t) {
			tasks = append(tasks, t)
		}
	}
	for _, t := range held {
		add(t)
	}
	for _, t := range held {
		for _, d := range dependents(run.run, t) {
			add(d)
		}
	}
	if row.Origin == ledger.OriginAgent && row.TaskID != "" {
		add(row.TaskID)
	}
	for _, t := range tasks {
		it.block("task:" + row.CurrentRun + "/" + t)
	}
	it.block("run:" + row.CurrentRun)
	var words string
	switch {
	case len(held) > 0:
		words = fmt.Sprintf("Yardmaster holds %s until %s is answered and applied", strings.Join(held, ", "), id)
	case row.Origin == ledger.OriginAgent:
		words = fmt.Sprintf("%s waits for %s's answer", capitalize(where), id)
	default:
		words = "Nothing is held for " + id
	}
	if row.State == ledger.DecisionAnswered {
		words += "; it is answered, and what it holds waits for Yardmaster to apply it"
	}
	it.Blocking.Words = fitText(words, 500)
}

// decisionEvidence lists the record's run, its asking task, attempt and
// session, its ticket, then the evidence its request cites.
func (rd *inboxRead) decisionEvidence(it *inboxItem, row ledger.DecisionRow, run inboxRun, req recordRequest) {
	it.addRef("run:"+row.CurrentRun, "the run "+cmpOr(run.run.Name, row.CurrentRun))
	if row.TaskID != "" {
		it.addRef("task:"+row.CurrentRun+"/"+row.TaskID, "the task "+row.TaskID)
		if row.Attempt > 0 {
			it.addRef(fmt.Sprintf("attempt:%s/%s/%d", row.RunID, row.TaskID, row.Attempt), fmt.Sprintf("attempt %d, which asked", row.Attempt))
		}
	}
	if row.SessionID != "" {
		if d, ok := rd.s.registry.Get(row.SessionID); ok {
			it.addRef("session:"+row.SessionID, "the session "+d.Info().Name+"'s terminal")
		}
	}
	if run.ok && run.run.Ticket != nil {
		it.addRef("ticket:"+run.run.Ticket.Key, run.run.Ticket.Key)
	}
	for _, e := range req.Evidence {
		it.addRef(e, "the decision's evidence")
	}
}

// decisionActions are an open record's actions by its type, each through
// DR1's route at the record's rev: an option (an approval's Approve and
// Reject by name), a clarification's line, more information from the
// asking agent, and a comment.
func (rd *inboxRead) decisionActions(it *inboxItem, row ledger.DecisionRow, req recordRequest, held []string, where string) {
	id, rev := row.ID(), strconv.Itoa(row.Rev)
	route := http.MethodPost + " /api/decisions/" + id + "/respond"
	authorizes := cmpPrefix("; what it authorizes: ", req.Authorizes)
	live := rd.s.runs != nil && rd.s.runs.AskerLive(row)
	if rd.askerLive != nil {
		live = rd.askerLive(row)
	}
	applies := func(choice *int) string {
		switch {
		case row.Origin == ledger.OriginAgent && row.TaskID != "" && live:
			return fmt.Sprintf("Yardmaster then types the answer into attempt %s.%d's session", row.TaskID, row.Attempt)
		case row.Origin == ledger.OriginAgent && row.TaskID != "":
			// The asking attempt has ended: DR1 carries the answer in the
			// task's next attempt's task file.
			return fmt.Sprintf("attempt %s.%d has ended, so %s's next attempt reads the answer in its task file", row.TaskID, row.Attempt, row.TaskID)
		case len(held) == 0:
			return "Yardmaster then notes it; nothing is held for it"
		case choice != nil && *choice == 1 && (row.Type == ledger.DecisionApproval || row.Type == ledger.DecisionAssignment):
			return "Yardmaster then cancels " + strings.Join(held, ", ")
		}
		return "Yardmaster then lets " + strings.Join(held, ", ") + " start"
	}
	for i, label := range optionLabels(row, req) {
		c := i
		name := "answer"
		if row.Type == ledger.DecisionApproval && i < 2 {
			name = []string{"approve", "reject"}[i]
		}
		it.addAction(inboxAction{Name: name, Label: label, Choice: &c, Expect: rev, Route: route, Confirm: row.Type != ledger.DecisionClarification,
			Scope: fmt.Sprintf("Answers %s '%s' at its rev %s, as the operator: %s%s", id, label, rev, applies(&c), authorizes)})
	}
	if row.Type == ledger.DecisionClarification {
		it.addAction(inboxAction{Name: "answer", Label: "Reply with words", Text: true, Expect: rev, Route: route,
			Scope: fmt.Sprintf("Answers %s with the words you write at its rev %s, as the operator: %s%s", id, rev, applies(nil), authorizes)})
	}
	if live {
		// Only the asking attempt's live session takes it (DR1 refuses it
		// no_recipient once that attempt has ended).
		it.addAction(inboxAction{Name: "more_info", Label: "Ask for more information", Text: true, Expect: rev, Route: http.MethodPost + " /api/decisions/" + id + "/comment",
			Scope: fmt.Sprintf("Types your question into %s's session and keeps %s open; its rev moves past %s, so an answer made before it is refused stale", where, id, rev)})
	}
	rd.commentAction(it, id, rev)
}

// commentAction adds a comment on the record, through DR1's comment route.
func (rd *inboxRead) commentAction(it *inboxItem, id, rev string) {
	it.addAction(inboxAction{Name: "comment", Label: "Comment", Text: true, Expect: rev, Route: http.MethodPost + " /api/decisions/" + id + "/comment",
		Scope: fmt.Sprintf("Keeps your comment on %s's record at its rev %s; nothing else changes, and its rev moves on", id, rev)})
}

// recordSteps are DR1's four states of rec, as its record reports them
// (DR1's card draws the same, web/app/utils/decisions.ts decisionWords),
// each its own step, never merged: available (asked), recorded (the
// response accepted), validated (Yardmaster checked it again as it
// applied it) and applied (or why not). held says whether the engine
// holds its run and it goes on (answeredWaits).
func recordSteps(rec ledger.DecisionRecord, held bool) []deliveryStep {
	id, ref := rec.ID(), ledger.DecisionRef(rec.N)
	final := rec.Final()
	out := []deliveryStep{step(stepAvailable, stepDone, timeOf(rec.CreatedAt), "asked: "+id+" was opened", ref)}
	var accepted *ledger.ResponseRow
	for i := range rec.Responses {
		if rec.Responses[i].Status == ledger.ResponseAccepted {
			accepted = &rec.Responses[i]
		}
	}
	var app *crew.Application
	if rec.Applied != "" {
		var a crew.Application
		if json.Unmarshal([]byte(rec.Applied), &a) == nil {
			app = &a
		}
	}
	switch {
	case accepted != nil:
		var rc crew.ResponseReceipt
		_ = json.Unmarshal([]byte(accepted.Receipt), &rc)
		out = append(out, step(stepRecorded, stepDone, timeOf(accepted.At), cmpOr(rc.Words, "answered"), fmt.Sprintf("%s/%d", id, accepted.K)))
	case final:
		out = append(out, step(stepRecorded, stepNotApplicable, nil, "no response was recorded: "+id+" is "+rec.State+cmpPrefix(": ", rec.Why), ""))
	default:
		out = append(out, step(stepRecorded, stepWaiting, nil, "waits for the operator's answer", ""))
	}
	switch {
	case app != nil:
		receipt := fmt.Sprintf("%s/%d", id, app.Response)
		out = append(out, step(stepValidated, stepDone, timeOf(app.At), "Yardmaster checked the subject, the policy and the asker again", receipt))
		words := app.Words
		if rec.State == ledger.DecisionSuperseded {
			words += "; then superseded" + cmpPrefix(": ", rec.Why)
		}
		out = append(out, step(stepApplied, stepDone, timeOf(app.At), words, receipt+" applied"))
	case final:
		why := id + " is " + rec.State + cmpPrefix(": ", rec.Why)
		if rec.SupersededBy != "" {
			why += "; " + rec.SupersededBy + " takes its place"
		}
		out = append(out, step(stepValidated, stepNotApplicable, nil, "not validated: "+why, ""), step(stepApplied, stepNotApplicable, nil, "not applied: "+why, ""))
	case accepted != nil:
		out = append(out, step(stepValidated, stepWaiting, nil, answeredWaits(held), ""), step(stepApplied, stepWaiting, nil, "not yet", ""))
	default:
		out = append(out, step(stepValidated, stepWaiting, nil, "not yet: nothing is answered", ""), step(stepApplied, stepWaiting, nil, "not yet", ""))
	}
	return out
}

// mirrorsRecord says whether the session's question mirrors a record DR1
// holds: the record is the item (open or answered in the list, closed in
// Recent), so the question's own is not listed, however long the screen
// still shows it. A record not found, or not read, leaves the question
// listed.
func (rd *inboxRead) mirrorsRecord(did string) bool {
	if did == "" || rd.s.ledger == nil {
		return false
	}
	n, ok := ledger.ParseDecisionID(did)
	if !ok {
		return false
	}
	_, found, err := rd.s.ledger.Decision(rd.ctx, n)
	return err == nil && found
}

// openDecisions is a page of DR1's records in the inbox's states after the
// cursor, at most limit, as items without their delivery, and their total
// and how many of them are answered (tracking); none without a ledger.
func (rd *inboxRead) openDecisions(after inboxCursor, limit int) ([]*inboxItem, int, int, error) {
	ld := rd.s.ledger
	if ld == nil {
		return nil, 0, 0, nil
	}
	// The page, its total and the answered among them are of one read, so
	// a record answered meanwhile is counted open in both or answered in
	// both, never open in one and answered in the other.
	page, _, total, answered, err := ld.InboxDecisions(rd.ctx, inboxStates, ledger.InboxCursor{Since: after.since, Ref: after.ref}, limit)
	if err != nil {
		return nil, 0, 0, err
	}
	out := make([]*inboxItem, 0, len(page))
	for _, row := range page {
		out = append(out, rd.decisionItem(row.DecisionRow, row.Held, nil))
	}
	return out, total, answered, nil
}

// recentDecisions are DR1's records closed in the last inboxRecentAge, the
// latest first, at most inboxRecent, as summaries.
func (rd *inboxRead) recentDecisions() ([]inboxSummary, error) {
	if rd.s.ledger == nil {
		return nil, nil
	}
	rows, err := rd.s.ledger.ClosedDecisions(rd.ctx, time.Now().Add(-inboxRecentAge), inboxRecent)
	if err != nil {
		return nil, err
	}
	out := make([]inboxSummary, 0, len(rows))
	for _, row := range rows {
		s := rd.decisionItem(row.DecisionRow, row.Held, nil).summary()
		s.Closed = timeOf(row.ClosedAt)
		out = append(out, s)
	}
	return out, nil
}

// decisionItemOf is the item of record n, open or as it is now, with
// its four states: errNoItem for none.
func (s *Server) decisionItemOf(ctx context.Context, n int64) (*inboxItem, error) {
	if s.ledger == nil {
		return nil, errNoItem
	}
	rec, found, err := s.ledger.Decision(ctx, n)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errNoItem
	}
	return s.newInboxRead(ctx).decisionItem(rec.DecisionRow, rec.Held, &rec), nil
}

// A record's tracking key names what it tracks by its kind and id: DR1
// keeps a response's responseId and a comment's opId (a comment's or a
// request for more information's) apart, each unique in its own space, so
// the one id may name a response and a comment both, and a key never
// reads one as the other.
const (
	trackResponse = "response:"
	trackComment  = "comment:"
)

// decisionReceipt reads a record's tracking key, "response:<responseId>"
// or "comment:<opId>": the kind's prefix and the id, ok false for any
// other.
func decisionReceipt(v string) (kind, id string, ok bool) {
	for _, k := range []string{trackResponse, trackComment} {
		if id, is := strings.CutPrefix(v, k); is && crew.CommandIDPattern.MatchString(id) {
			return k, id, true
		}
	}
	return "", "", false
}

// decisionTrack is the four steps of the action actor took on record n
// under the key's kind and id: a response's (accepted, the record's four
// states; refused, recorded refused with DR1's words), a comment's or a
// request for more information's (recorded at its history row; nothing
// to validate or apply: the record stays as it was), each looked for
// among its kind alone. An id the record keeps nothing of under actor, of
// that kind, reads recorded unknown.
func (s *Server) decisionTrack(ctx context.Context, n int64, actor, kind, id string) ([]deliveryStep, error) {
	if s.ledger == nil {
		return nil, errNoItem
	}
	rec, found, err := s.ledger.Decision(ctx, n)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errNoItem
	}
	all := recordSteps(rec, s.runHeld(rec.CurrentRun))
	avail := all[0]
	for _, r := range rec.Responses {
		if kind != trackResponse || r.Actor != actor || r.ResponseID != id {
			continue
		}
		if r.Status == ledger.ResponseAccepted {
			return all, nil
		}
		var rc crew.ResponseReceipt
		_ = json.Unmarshal([]byte(r.Receipt), &rc)
		words := cmpOr(rc.Words, "refused")
		if rc.Code != "" {
			words += " (" + rc.Code + ")"
		}
		receipt := fmt.Sprintf("%s/%d", rec.ID(), r.K)
		return []deliveryStep{avail, step(stepRecorded, stepRefused, timeOf(r.At), words, receipt),
			step(stepValidated, stepNotApplicable, nil, "refused: nothing to validate", ""), step(stepApplied, stepNotApplicable, nil, "refused: nothing was applied", "")}, nil
	}
	for _, h := range rec.History {
		if kind != trackComment || h.By != actor || h.OpID != id || (h.Kind != ledger.HistoryCommented && h.Kind != ledger.HistoryInfoRequested) {
			continue
		}
		var dt struct {
			Words string `json:"words"`
		}
		_ = json.Unmarshal([]byte(h.Detail), &dt)
		then := "a comment changes nothing: " + rec.ID() + " is as it was"
		if h.Kind == ledger.HistoryInfoRequested {
			then = "a request for more information keeps " + rec.ID() + " open: the agent answers by asking again"
		}
		receipt := fmt.Sprintf("%s/history/%d", rec.ID(), h.Seq)
		return []deliveryStep{avail, step(stepRecorded, stepDone, timeOf(h.At), cmpOr(dt.Words, h.Kind), receipt),
			step(stepValidated, stepNotApplicable, nil, then, ""), step(stepApplied, stepNotApplicable, nil, then, "")}, nil
	}
	none := "the record keeps no response of yours under this id"
	if kind == trackComment {
		none = "the record keeps no comment of yours under this id"
	}
	return []deliveryStep{avail, step(stepRecorded, stepUnknown, nil, none, ""), step(stepValidated, stepUnknown, nil, none, ""), step(stepApplied, stepUnknown, nil, none, "")}, nil
}

// handleDecisionItem answers the item of a decision's ref
// (handleInboxItem's part for DR1's records).
func (s *Server) handleDecisionItem(w http.ResponseWriter, r *http.Request, n int64) {
	it, err := s.decisionItemOf(r.Context(), n)
	switch {
	case errors.Is(err, errNoItem):
		writeError(w, http.StatusNotFound, "not_found", "no such item")
	case err != nil:
		s.log.Warn("inbox: a decision could not be read", "decision", ledger.DecisionID(n), "err", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the item could not be read")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"item": it})
	}
}

// handleDecisionTrack answers the steps of an action on a decision
// (handleInboxTrack's part for DR1's records): receipt the tracking key,
// response:<responseId> or comment:<opId>, read under the caller's actor.
func (s *Server) handleDecisionTrack(w http.ResponseWriter, r *http.Request, n int64, receipt string) {
	kind, id, ok := decisionReceipt(receipt)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "receipt is response:<responseId> or comment:<opId>")
		return
	}
	steps, err := s.decisionTrack(r.Context(), n, actorOf(r), kind, id)
	switch {
	case errors.Is(err, errNoItem):
		writeError(w, http.StatusNotFound, "not_found", "no such decision")
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the decision's record could not be read")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ref": ledger.DecisionRef(n), "receipt": receipt, "steps": steps})
	}
}
