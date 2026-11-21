package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A decision as it leaves the machine (docs/plans/sync-seams.md, OS-3):
// the document the sync store is handed for each decision (store
// "decision", path dr-n), built from the ledger's record and its
// copied-output verdicts (decision_output): the same record and the same
// verdicts give the same bytes and the same requestSHA256, and a verdict
// settled later has the document delivered again at the same rev, which
// replaces the one the store holds. Every free-text part is judged before
// it leaves: an output-like question, label, uncertainty, authorizes or
// expertise, or any part still pending, keeps the whole request back
// (requestKept "output"), and an output-like context, tradeoffs, response
// text, rationale or words is cut (cut "output"). A decision's parts with
// no verdict kept are judged once, at its document's first build, and the
// verdicts kept; an agent's request whose question was never judged as it
// was written stays back.

// syncDecisionStore is the store of a decision's document (ext.Doc.Store).
const syncDecisionStore = "decision"

// outputCutWords is what stands in a part cut for quoting output.
const outputCutWords = "[output kept on this machine]"

// The bounds of a response's text in the document.
const (
	docOtherMax = 1 << 10 // each other response
	docWordsMax = 500     // a receipt's words
	docTextMax  = ext.MaxAnswerText
)

// docRequestShape is a request as the ledger's request column keeps it.
type docRequestShape struct {
	Question string `json:"question"`
	Context  string `json:"context,omitempty"`
	Options  []struct {
		Label     string `json:"label"`
		Tradeoffs string `json:"tradeoffs,omitempty"`
	} `json:"options,omitempty"`
	Recommendation *int                    `json:"recommendation,omitempty"`
	Uncertainty    string                  `json:"uncertainty,omitempty"`
	Authorizes     string                  `json:"authorizes,omitempty"`
	Evidence       []string                `json:"evidence,omitempty"`
	Expertise      []string                `json:"expertise,omitempty"`
	Subject        *ext.DecisionDocSubject `json:"subject,omitempty"`
	Supersedes     string                  `json:"supersedes,omitempty"`
}

// docReceipt is a response's receipt as the ledger keeps it.
type docReceipt struct {
	Code      string `json:"code"`
	Words     string `json:"words"`
	Choice    *int   `json:"choice"`
	Text      string `json:"text"`
	Rationale string `json:"rationale"`
}

// verdictsOf is decision n's verdicts: the ledger's (kept), and for a part
// it kept none of (written before the check) the check's now (judge).
type verdictsOf struct {
	kept  map[string]string
	judge func(text string) string
}

// of is a part's verdict: the kept one, else the check's now; "" text is
// clean.
func (v verdictsOf) of(part, text string) string {
	if text == "" {
		return verdictClean
	}
	if s, ok := v.kept[part]; ok {
		return s
	}
	// None kept of this part (written before the check): judged now.
	return v.judge(text)
}

// keeps says a verdict keeps its part on the machine: output, or not yet
// judged.
func keeps(verdict string) bool { return verdict != verdictClean }

// syncDecision builds the document of rec with its verdicts.
func syncDecision(rec ledger.DecisionRecord, v verdictsOf) (ext.DecisionDoc, error) {
	d := rec.DecisionRow
	doc := ext.DecisionDoc{ID: d.ID(), Run: d.CurrentRun, OpenedIn: d.RunID, Origin: d.Origin, Type: d.Type, Gate: d.Gate, Task: d.TaskID,
		Attempt: d.Attempt, Member: d.Member, Ticket: d.Ticket, Plan: d.Plan, PlanDigest: d.PlanDigest, State: d.State, Rev: d.Rev,
		SupersededBy: d.SupersededBy, CreatedAt: d.CreatedAt.UTC(), Policy: json.RawMessage(d.Policy)}
	if doc.Policy == nil || !json.Valid(doc.Policy) {
		doc.Policy = json.RawMessage("{}")
	}
	if !d.AnsweredAt.IsZero() {
		at := d.AnsweredAt.UTC()
		doc.AnsweredAt = &at
	}
	if !d.ClosedAt.IsZero() {
		at := d.ClosedAt.UTC()
		doc.ClosedAt = &at
	}
	doc.OpenedTo = ext.DecisionOpenedToOperator
	if d.Origin != ledger.OriginCompare && crew.GivenToGrant(d.Request) {
		doc.OpenedTo = ext.DecisionOpenedToDecide
	}
	doc.Why = v.cut(ledger.PartWhy, d.Why, nil)

	req, kept := docRequest(d, v)
	if kept {
		doc.RequestKept = ext.DecisionRequestKeptOutput
	} else {
		doc.Request = req
	}
	part, err := doc.RequestPart()
	if err != nil {
		return ext.DecisionDoc{}, err
	}
	if doc.RequestSHA256, err = part.SHA256(); err != nil {
		return ext.DecisionDoc{}, err
	}
	doc.Responses, doc.ResponsesOmitted = docResponsesOf(rec.Responses, v, len(crew.DecisionOptions(d.Type, d.Request)))
	doc.Applied, doc.States = docAppliedOf(rec, v)
	return doc, nil
}

// cut is a free-text part as the document gives it: whole when clean,
// else the cut's words, and the cut marked on mark.
func (v verdictsOf) cut(part, text string, mark *string) string {
	if !keeps(v.of(part, text)) {
		return text
	}
	if mark != nil {
		*mark = "output"
	}
	return outputCutWords
}

// docRequest is the request as the document gives it, and whether it is
// kept back: an output-like question, label, uncertainty, authorizes or
// expertise, any part still pending, or an agent's request whose question
// was never judged as it was written, keeps it all. So the request's form
// at a rev never changes once it has left: a part settled is whole or cut
// for good.
func docRequest(d ledger.DecisionRow, v verdictsOf) (*ext.DecisionDocRequest, bool) {
	var r docRequestShape
	if json.Unmarshal([]byte(d.Request), &r) != nil {
		return nil, true
	}
	if _, judged := v.kept[ledger.PartQuestion]; d.Origin == ledger.OriginAgent && !judged {
		// An agent's request never judged as it was written (opened before
		// the check, or with no sync store), whatever was judged of it
		// since: kept back whatever the check would find now.
		return nil, true
	}
	if keeps(v.of(ledger.PartQuestion, r.Question)) || keeps(v.of(ledger.PartUncertainty, r.Uncertainty)) ||
		keeps(v.of(ledger.PartAuthorizes, r.Authorizes)) || keeps(v.of(ledger.PartExpertise, strings.Join(r.Expertise, "\n"))) {
		return nil, true
	}
	for i, o := range r.Options {
		if keeps(v.of(ledger.PartLabel(i), o.Label)) || v.of(ledger.PartTradeoffs(i), o.Tradeoffs) == verdictPending {
			return nil, true
		}
	}
	if v.of(ledger.PartContext, r.Context) == verdictPending {
		return nil, true
	}
	out := &ext.DecisionDocRequest{Question: r.Question, Recommendation: r.Recommendation, Uncertainty: r.Uncertainty, Authorizes: r.Authorizes,
		Expertise: r.Expertise, Subject: r.Subject, Supersedes: r.Supersedes, Evidence: r.Evidence}
	out.Context = v.cut(ledger.PartContext, r.Context, &out.Cut)
	for i, o := range r.Options {
		out.Options = append(out.Options, ext.DecisionDocOption{Label: o.Label, Tradeoffs: v.cut(ledger.PartTradeoffs(i), o.Tradeoffs, &out.Cut)})
	}
	// An empty list is left out by its field's omitempty.
	// A recommendation of none (-1), or of no option the request has, is
	// left out.
	if rc := out.Recommendation; rc != nil && (*rc < 0 || *rc >= len(out.Options)) {
		out.Recommendation = nil
	}
	fitDocRequest(out)
	return out, false
}

// fitDocRequest cuts the request to MaxDecisionDocRequest: its context, then
// each option's tradeoffs, at a character's end, marked Cut "size".
func fitDocRequest(r *ext.DecisionDocRequest) {
	size := func() int { b, _ := json.Marshal(r); return len(b) }
	over := size() - ext.MaxDecisionDocRequest
	if over <= 0 {
		return
	}
	cutTo := func(s string, by int) string {
		n := max(len(s)-by, 0)
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		return s[:n]
	}
	r.Cut = "size"
	r.Context = cutTo(r.Context, over+16)
	for i := range r.Options {
		if over = size() - ext.MaxDecisionDocRequest; over <= 0 {
			return
		}
		r.Options[i].Tradeoffs = cutTo(r.Options[i].Tradeoffs, over+16)
	}
}

// docResponsesOf is the accepted response, with what was answered, and the
// newest others while they fit MaxDecisionDocResponses, and how many were
// left out. A response's choice is given only below the count of the
// decision type's options (an assignment's are its two fixed answers).
func docResponsesOf(rows []ledger.ResponseRow, v verdictsOf, options int) ([]ext.DecisionDocResponse, int) {
	one := func(r ledger.ResponseRow, accepted bool) ext.DecisionDocResponse {
		var rc docReceipt
		_ = json.Unmarshal([]byte(r.Receipt), &rc)
		var auth struct {
			Via string `json:"via"`
		}
		_ = json.Unmarshal([]byte(r.Authority), &auth)
		out := ext.DecisionDocResponse{K: r.K, Actor: r.Actor, Status: r.Status, ResponseID: r.ResponseID, At: r.At.UTC(), Via: auth.Via, Code: rc.Code}
		if rc.Choice != nil && *rc.Choice >= 0 && *rc.Choice < options {
			c := *rc.Choice
			out.Choice = &c
		}
		out.Words = cutDocBytes(v.cut(ledger.PartResponse(r.K, "words"), rc.Words, &out.Cut), docWordsMax)
		if accepted {
			out.Text = cutDocBytes(v.cut(ledger.PartResponse(r.K, "text"), rc.Text, &out.Cut), docTextMax)
			out.Rationale = cutDocBytes(v.cut(ledger.PartResponse(r.K, "rationale"), rc.Rationale, &out.Cut), ext.MaxAnswerRationale)
		}
		return out
	}
	var out []ext.DecisionDocResponse
	size := 0
	acc := -1
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Status == ledger.ResponseAccepted {
			acc = i
			break
		}
	}
	// The bound is held on the bytes that leave: each response as JSON
	// encodes it (escapes and all), and the list's brackets and commas.
	room := ext.MaxDecisionDocResponses - docListMarks
	if acc >= 0 {
		r := one(rows[acc], true)
		fitResponse(&r, room)
		out = append(out, r)
		b, _ := json.Marshal(r)
		size += len(b)
	}
	omitted, others := 0, 0
	for i := len(rows) - 1; i >= 0; i-- {
		if i == acc {
			continue
		}
		r := one(rows[i], false)
		b, _ := json.Marshal(r)
		if len(b) > docOtherMax || size+len(b) > room || others == docOthers {
			omitted++
			continue
		}
		size += len(b)
		others++
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b ext.DecisionDocResponse) int { return a.K - b.K })
	return out, omitted
}

// The responses' list: the accepted response and at most docOthers others,
// docListMarks bytes kept for the list's brackets and commas.
const (
	docOthers    = 8
	docListMarks = 16
)

// fitResponse cuts r's text, then its rationale, where a character ends,
// until r as JSON encodes it is at most max bytes, marked Cut "size".
func fitResponse(r *ext.DecisionDocResponse, limit int) {
	for _, field := range []*string{&r.Text, &r.Rationale} {
		b, _ := json.Marshal(r)
		over := len(b) - limit
		if over <= 0 {
			return
		}
		enc, _ := json.Marshal(*field)
		*field = crew.CutEncoded(*field, max(len(enc)-2-over, 0))
		if r.Cut == "" {
			r.Cut = "size"
		}
	}
}

// cutDocBytes is s cut to at most n bytes at a character's end.
func cutDocBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// docAppliedOf is the application and the four states as the record
// reports them.
func docAppliedOf(rec ledger.DecisionRecord, v verdictsOf) (*ext.DecisionDocApplied, ext.DecisionDocStates) {
	d := rec.DecisionRow
	st := ext.DecisionDocStates{Open: d.State == ledger.DecisionOpen}
	for i := len(rec.Responses) - 1; i >= 0; i-- {
		if r := rec.Responses[i]; r.Status == ledger.ResponseAccepted {
			var rc docReceipt
			_ = json.Unmarshal([]byte(r.Receipt), &rc)
			st.Recorded = &ext.DecisionDocState{At: r.At.UTC(), Words: cutDocBytes(v.cut(ledger.PartResponse(r.K, "words"), rc.Words, nil), docWordsMax)}
			break
		}
	}
	var applied *ext.DecisionDocApplied
	if d.Applied != "" {
		var a crew.Application
		if json.Unmarshal([]byte(d.Applied), &a) == nil {
			applied = &ext.DecisionDocApplied{At: a.At.UTC(), How: a.How, Run: a.Run, Task: a.Task, Attempt: a.Attempt, Response: a.Response,
				Words: cutDocBytes(v.cut(ledger.PartApplied, a.Words, nil), docWordsMax)}
			if a.Validated {
				st.Validated = &ext.DecisionDocState{At: a.At.UTC()}
			}
		}
	}
	if d.Final() {
		words := v.cut(ledger.PartWhy, d.Why, nil)
		at := d.ClosedAt.UTC()
		if applied != nil && d.State == ledger.DecisionApplied {
			words, at = applied.How, applied.At
		}
		st.Applied = &ext.DecisionDocState{At: at, Words: cutDocBytes(words, docWordsMax)}
	}
	return applied, st
}

// decisionDoc reads decision n from the ledger and builds its document;
// ok false for a decision the ledger no longer holds.
func (s *Server) decisionDoc(ctx context.Context, n int64) (ext.DecisionDoc, bool, error) {
	rec, ok, err := s.ledger.Decision(ctx, n)
	if err != nil || !ok {
		return ext.DecisionDoc{}, false, err
	}
	kept, _, err := s.ledger.OutputVerdicts(ctx, n)
	if err != nil {
		return ext.DecisionDoc{}, false, err
	}
	if kept, err = s.judgeOnce(ctx, rec, kept); err != nil {
		return ext.DecisionDoc{}, false, err
	}
	// Every part with text has its verdict kept now, but where it could
	// not be kept (a ledger another server holds): not yet judged.
	v := verdictsOf{kept: kept, judge: func(string) string { return verdictPending }}
	doc, err := syncDecision(rec, v)
	return doc, err == nil, err
}

// judgeOnce judges, once, each part of rec with text and no kept verdict
// (a decision written before the check, or with no sync store) and keeps
// what it found, never over a verdict there (a pending one stays for the
// settle loop), so a later build cuts what the first one cut. An agent's
// request whose question was never judged as it was written is left
// unjudged: it stays back whole. It answers the verdicts kept, read again.
func (s *Server) judgeOnce(ctx context.Context, rec ledger.DecisionRecord, kept map[string]string) (map[string]string, error) {
	agent := rec.Origin == ledger.OriginAgent
	_, questionJudged := kept[ledger.PartQuestion]
	found := map[string]string{}
	for part, text := range partsOf(rec) {
		if _, ok := kept[part]; ok || text == "" {
			continue
		}
		if requestPart(part) && agent && !questionJudged {
			continue
		}
		found[part] = s.copyCheck.Judge(ctx, text, agent && requestPart(part))
	}
	if len(found) == 0 {
		return kept, nil
	}
	if err := s.ledger.KeepOutput(ctx, rec.N, found); err != nil {
		if errors.Is(err, ledger.ErrReadOnly) {
			return kept, nil
		}
		return nil, err
	}
	again, _, err := s.ledger.OutputVerdicts(ctx, rec.N)
	return again, err
}

// partsOf is every free-text part of rec, by its name in decision_output.
func partsOf(rec ledger.DecisionRecord) map[string]string {
	var r docRequestShape
	_ = json.Unmarshal([]byte(rec.Request), &r)
	out := map[string]string{ledger.PartQuestion: r.Question, ledger.PartContext: r.Context, ledger.PartUncertainty: r.Uncertainty,
		ledger.PartAuthorizes: r.Authorizes, ledger.PartExpertise: strings.Join(r.Expertise, "\n"), ledger.PartWhy: rec.Why}
	for i, o := range r.Options {
		out[ledger.PartLabel(i)], out[ledger.PartTradeoffs(i)] = o.Label, o.Tradeoffs
	}
	var a crew.Application
	if json.Unmarshal([]byte(rec.Applied), &a) == nil {
		out[ledger.PartApplied] = a.Words
	}
	for _, resp := range rec.Responses {
		var rc docReceipt
		_ = json.Unmarshal([]byte(resp.Receipt), &rc)
		out[ledger.PartResponse(resp.K, "text")], out[ledger.PartResponse(resp.K, "rationale")], out[ledger.PartResponse(resp.K, "words")] = rc.Text, rc.Rationale, rc.Words
	}
	return out
}

// parseDecisionPath is a decision document's path, dr-n, as its number.
func parseDecisionPath(path string) (int64, error) {
	n, ok := ledger.ParseDecisionID(path)
	if !ok {
		return 0, fmt.Errorf("%q is not a decision's id", path)
	}
	return n, nil
}

// partText is the text of a decision's part as the ledger keeps it ("" for
// a part it does not hold).
func partText(rec ledger.DecisionRecord, part string) string {
	var r docRequestShape
	_ = json.Unmarshal([]byte(rec.Request), &r)
	switch part {
	case ledger.PartQuestion:
		return r.Question
	case ledger.PartContext:
		return r.Context
	case ledger.PartUncertainty:
		return r.Uncertainty
	case ledger.PartAuthorizes:
		return r.Authorizes
	case ledger.PartExpertise:
		return strings.Join(r.Expertise, "\n")
	case ledger.PartWhy:
		return rec.Why
	case ledger.PartApplied:
		var a crew.Application
		_ = json.Unmarshal([]byte(rec.Applied), &a)
		return a.Words
	}
	for i, o := range r.Options {
		switch part {
		case ledger.PartLabel(i):
			return o.Label
		case ledger.PartTradeoffs(i):
			return o.Tradeoffs
		}
	}
	for _, resp := range rec.Responses {
		var rc docReceipt
		_ = json.Unmarshal([]byte(resp.Receipt), &rc)
		switch part {
		case ledger.PartResponse(resp.K, "text"):
			return rc.Text
		case ledger.PartResponse(resp.K, "rationale"):
			return rc.Rationale
		case ledger.PartResponse(resp.K, "words"):
			return rc.Words
		}
	}
	return ""
}

// partWrittenAt is when the part was written, or a time before that: a
// response's part its response's time, the application's words its
// application's, and every other part (the request's, the why) the
// decision's opening. A part is output when a source was pruned after it.
func partWrittenAt(rec ledger.DecisionRecord, part string) time.Time {
	for _, resp := range rec.Responses {
		if strings.HasPrefix(part, fmt.Sprintf("response/%d/", resp.K)) && !resp.At.IsZero() {
			return resp.At
		}
	}
	if part == ledger.PartApplied {
		var a crew.Application
		if json.Unmarshal([]byte(rec.Applied), &a) == nil && !a.At.IsZero() {
			return a.At
		}
	}
	return rec.CreatedAt
}

// settleDecisionParts judges the ledger's pending decision parts again once
// the index is whole: output when a source was pruned after the decision
// was opened, else what the check finds now. It answers how many it
// settled and whether parts are left pending.
func (s *Server) settleDecisionParts(ctx context.Context) (int, bool) {
	if s.ledger == nil || s.copyCheck == nil || s.syncName() == "" {
		return 0, false
	}
	if !s.outputs.whole(ctx) {
		return 0, true
	}
	pending, err := s.ledger.PendingOutputs(ctx, 500)
	if err != nil || len(pending) == 0 {
		return 0, false
	}
	n := 0
	for _, p := range pending {
		rec, ok, err := s.ledger.Decision(ctx, p.N)
		if err != nil || !ok {
			continue
		}
		pruned, err := s.outputs.prunedSince(partWrittenAt(rec, p.Part))
		if err != nil {
			return n, true
		}
		v := verdictOutput
		if !pruned {
			v = s.copyCheck.Judge(ctx, partText(rec, p.Part), rec.Origin == ledger.OriginAgent && requestPart(p.Part))
		}
		if v == verdictPending {
			continue
		}
		if s.ledger.SettleOutput(ctx, p.N, p.Part, v) == nil {
			n++
		}
	}
	return n, n < len(pending)
}

// pokeSettle has the settle loop run now, never waiting: a part the
// ledger's check wrote pending (settleDue, set in the transaction) is
// judged as soon as its commit is told and the index has caught up with
// the feeds in flight, not at the next tick, so a source's prune a moment
// later does not make it output.
func (s *Server) pokeSettle(ns []int64) {
	if len(ns) == 0 || !s.settleDue.Swap(false) {
		return
	}
	select {
	case s.settleWake <- struct{}{}:
	default:
	}
}

// settleDecisionLoop judges the decisions' pending parts when a write left
// one pending (pokeSettle) and every settleEvery besides, until ctx ends.
// It goes on at once while a pass settles something, or while the index
// is behind a feed taken (the next pass's whole waits for it, at most
// indexCatchUp). A pass that settles nothing otherwise (a part judged
// pending again, a read or a write refused, an index that cannot be read)
// waits for the tick or the next wake: such a pass returns at once, and
// going on would spin.
func (s *Server) settleDecisionLoop(ctx context.Context) {
	every := settleEvery
	if s.settleTick > 0 {
		every = s.settleTick
	}
	t := time.NewTicker(every)
	defer t.Stop()
	pass := s.settleDecisionParts
	if s.settlePass != nil {
		pass = s.settlePass
	}
	for {
		if s.settleWaits != nil {
			s.settleWaits()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.settleWake:
		}
		for ctx.Err() == nil {
			n, left := pass(ctx)
			if !left || n == 0 && !s.outputs.behind() {
				break
			}
		}
	}
}

// requestPart says part is one of a request's (not a response's, the why or
// the application's words).
func requestPart(part string) bool {
	return !strings.HasPrefix(part, "response/") && part != ledger.PartWhy && part != ledger.PartApplied
}
