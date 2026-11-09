package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// DR1's records on a decision link's lane (docs/plans/decision-inbox.md,
// PR-3). A run's link covers the records its run holds, a session's link
// those of its attempt's task (asked by it or holding it), each open or
// answered as an item of the scope's list. DR1's policy, local/1, takes
// a response from a principal with the admin grant alone, and DR1's
// routes refuse a share link's holder, so a link shows a record and its
// four states with no action, and refuses every decide on one
// (unsupported, in linkCannotAnswer's words), until DR1's plan accepts a
// decision link's actor; decideRecord is the operation such a decide
// would take, the same as DR1's routes, field for field. The record
// itself is read in parts (head with its refs, responses, history), each
// cut from one whole read of the record and carrying the mark of the
// record it was cut from: a part asked at another mark is record_moved,
// with the new one. The mark is the record's rev and how many responses
// and history rows it keeps (recordMark): DR1 moves the rev with each
// change of the record's state or its operations, but writes a refused
// response and its delivery's rows (delivery.typing, delivery.entered)
// without moving it, so the rev alone does not mark the record whole.
//
// One rule keeps a link's view of a record of one moment: a record, what
// it holds and its mark, as the lane serves them and as its notification
// hash is made of them, come from ONE read of the ledger. The scope's
// records, every one whatever its state, with what each holds and its
// mark, are one read (ledger.ScopeDecisions): the open and answered are
// the items, and the hash carries every record's state and mark, so any
// change of a record in the scope, final ones included (a comment, a
// request for more information, a refused or late response, a delivery's
// row), sends decisions_changed; an item's record and what it holds are
// of one read (ledger.Decision's Held), never a second read of the holds;
// a part is cut from the very read its scope was judged on, never from a
// second one. A read that fails serves nothing and changes no hash: the
// lane answers unavailable, and its watch keeps the last hash it saw.

// linkCannotAnswer are the words of a record on a link.
const linkCannotAnswer = "Answer it on the LAN or the desktop: a link cannot answer a durable decision yet"

// The lane's pages of a record's parts. A responses part holds as many
// receipts as fit proto.MaxDecision beside the rest of its frame, each at
// ledger.MaxDecisionReceipt, so the page follows the receipt's bound
// (8 KiB receipts give 12 a page, 32 KiB ones 3) rather than a number
// that holds at one bound alone.
const (
	// recordAnswerRoom bounds the answer the first responses page
	// carries, encoded: an option's label of ledger.MaxOptionLabel
	// characters, ": " and a text of proto.MaxSubmit bytes, each escaping
	// to at most six bytes.
	recordAnswerRoom = 6 * (ledger.MaxOptionLabel + len(": ") + proto.MaxSubmit)
	// recordPartRoom bounds the rest of a responses part's frame: the
	// answer, and 1 KiB for the frame's envelope (its request's id of
	// proto.MaxDecisionReqID) and the part's ref, name, mark, next and
	// total.
	recordPartRoom = recordAnswerRoom + 1<<10
	// recordReceiptRoom is one receipt as the part carries it: the
	// ledger's bound, its number (k, set as it is read: `,"k":` and 19
	// digits) and the comma after it.
	recordReceiptRoom = ledger.MaxDecisionReceipt + 32
	// recordResponsesPage is how many receipts a responses part holds.
	recordResponsesPage = (proto.MaxDecision - recordPartRoom) / recordReceiptRoom
	recordHistoryPage   = 32
)

// laneDecisionItems are DR1's records in the scope (a run link's run's, a
// session link's attempt's task's), every one whatever its state, all of
// one read of the ledger: the open and answered ones as the lane's items
// (no action, the words why, what each holds of the same read), and
// marks, each record's ref, state and mark (its rev, its responses and
// its history counted), for the link's notification hash: a final record,
// which the link still reads (decision_record), changes it as an open one
// does. A failed read is an error, never a scope with fewer records.
func (s *Server) laneDecisionItems(ctx context.Context, rd *inboxRead, sc laneScope) (items []*inboxItem, marks []string, err error) {
	if s.ledger == nil || sc.run == "" || (sc.session != "" && sc.task == "") {
		return nil, nil, nil
	}
	rows, err := s.ledger.ScopeDecisions(ctx, sc.run, sc.task)
	if err != nil {
		return nil, nil, fmt.Errorf("the decisions of run %s: %w", sc.run, err)
	}
	for _, row := range rows {
		marks = append(marks, ledger.DecisionRef(row.N)+" "+row.State+" "+recordMarkOf(row.Rev, row.Responses, row.History))
		if slices.Contains(inboxStates, row.State) {
			items = append(items, laneRecordItem(rd.decisionItem(row.DecisionRow, row.Held, nil)))
		}
	}
	return items, marks, nil
}

// laneRecordItem is a record's item as a link shows it: its actions taken
// away, saying where it is answered.
func laneRecordItem(it *inboxItem) *inboxItem {
	if it.State == ledger.DecisionOpen || it.State == ledger.DecisionAnswered {
		it.Now = fitText(linkCannotAnswer+cmpPrefix("; ", it.Now), 500)
	}
	it.Actions = []inboxAction{}
	return it
}

// laneRecord reads record n when the scope covers it: the run that holds
// it is a run link's, or a session link's attempt's run with the record
// its task's. A refusal (naming reqID) for one out of the scope or none.
func (s *Server) laneRecord(ctx context.Context, sc laneScope, reqID string, n int64) (ledger.DecisionRecord, []byte) {
	out := refusal(reqID, proto.DecideNotInScope, "this link does not cover "+ledger.DecisionRef(n))
	if sc.run == "" || (sc.session != "" && sc.task == "") {
		// A session link whose session runs no attempt covers no record.
		return ledger.DecisionRecord{}, out
	}
	if s.ledger == nil {
		return ledger.DecisionRecord{}, refusal(reqID, proto.DecideNotFound, "this server keeps no ledger, so no decisions")
	}
	var rec ledger.DecisionRecord
	var found bool
	var err error
	if sc.task != "" {
		rec, found, err = s.ledger.TaskDecision(ctx, n, sc.run, sc.task)
	} else {
		rec, found, err = s.ledger.Decision(ctx, n)
	}
	switch {
	case err != nil:
		return ledger.DecisionRecord{}, refusal(reqID, "unavailable", "the decision could not be read")
	case !found && sc.task != "":
		// Of another task, or none: either way not this link's.
		return ledger.DecisionRecord{}, out
	case !found:
		return ledger.DecisionRecord{}, refusal(reqID, proto.DecideNotFound, "no such decision")
	case rec.CurrentRun != sc.run:
		return ledger.DecisionRecord{}, out
	}
	return rec, nil
}

// laneDecisionRef reads v as a record's ref in the scope: is false for a
// ref of another kind; a refusal for one that names no decision or one
// out of the scope.
func (s *Server) laneDecisionRef(ctx context.Context, sc laneScope, reqID, v string) (rec ledger.DecisionRecord, is bool, no []byte) {
	n, is, err := decisionRefN(v)
	if !is {
		return ledger.DecisionRecord{}, false, nil
	}
	if err != nil {
		return ledger.DecisionRecord{}, true, refusal(reqID, proto.DecideInvalid, err.Error())
	}
	rec, no = s.laneRecord(ctx, sc, reqID, n)
	return rec, true, no
}

// readRecordItem answers a decision_read of a record in the scope, read
// with what it holds (laneDecisionRef's one read).
func (s *Server) readRecordItem(ctx context.Context, sc laneScope, reqID string, rec ledger.DecisionRecord) []byte {
	it := laneRecordItem(s.newInboxRead(ctx).decisionItem(rec.DecisionRow, rec.Held, &rec))
	for i, e := range it.Evidence {
		if id, ok := sessionOfRef(e.Ref); ok && s.coversSession(sc, id) {
			it.Evidence[i].Session = s.laneSessionID(sc, id)
		}
	}
	return decisionFrame(reqID, func(d *proto.Decision, b json.RawMessage) { d.Item = b }, it)
}

// sessionOfRef is the session a session: ref names.
func sessionOfRef(ref string) (string, bool) {
	id, ok := strings.CutPrefix(ref, "session:")
	return id, ok && id != ""
}

// recordMark is the mark of a record as read: its rev, then its
// responses' count in two digits and its history rows' in three (at most
// MaxDecisionResponses and MaxDecisionHistory, each only ever added to),
// digits only, as a decision_record's rev is.
func recordMark(d crew.Decision) string { return recordMarkOf(d.Rev, len(d.Responses), len(d.History)) }

// recordMarkOf is the mark of a record at rev keeping that many responses and
// history rows.
func recordMarkOf(rev, responses, history int) string {
	return fmt.Sprintf("%d%02d%03d", rev, responses, history)
}

// laneRecordPart is a decision_record's reply: one part of a record at
// its mark (Rev, recordMark's). The head is the record without its responses, its history
// and its answer, with its evidence's refs resolved in the same request;
// the responses' first page carries the answer (in words, from the
// accepted response). Responses and history are newest first, a page at
// a time, next the offset of the next page; total how many the part
// holds. Assembled whole at one rev, the parts are the record
// GET /api/decisions/{did} gives at that mark, with its refs.
type laneRecordPart struct {
	Ref       string                 `json:"ref"`
	Part      string                 `json:"part"`
	Rev       string                 `json:"rev"`
	Decision  *crew.Decision         `json:"decision,omitempty"`
	Refs      []decisionRef          `json:"refs,omitempty"`
	Answer    string                 `json:"answer,omitempty"`
	Responses []crew.ResponseReceipt `json:"responses,omitempty"`
	History   []crew.DecisionEntry   `json:"history,omitempty"`
	Next      string                 `json:"next,omitempty"`
	Total     int                    `json:"total"`
}

// recordPart cuts the part m asks of d, the record read whole once, with
// refs its evidence's refs resolved in the same request (for the head):
// a part at another mark than d's is record_moved (code and words), and
// a page cursor that is no offset of the part's invalid.
func recordPart(d crew.Decision, refs []decisionRef, part, rev, after string) (laneRecordPart, string, string) {
	now := recordMark(d)
	out := laneRecordPart{Ref: ledger.DecisionRef(mustN(d.ID)), Part: part, Rev: now}
	if part != proto.RecordHead && rev != now {
		return out, proto.DecideRecordMoved, fmt.Sprintf("%s changed since its head was read: it is at rev %d (its mark %s); read it again from its head", d.ID, d.Rev, now)
	}
	page := func(n, size int) (int, int, bool) {
		from := 0
		if after != "" {
			v, err := strconv.Atoi(after)
			if err != nil || v < 0 || v > n || strconv.Itoa(v) != after {
				return 0, 0, false
			}
			from = v
		}
		return from, min(from+size, n), true
	}
	switch part {
	case proto.RecordHead:
		head := d
		head.Responses, head.History, head.Answer = nil, nil, ""
		out.Decision, out.Refs = &head, refs
	case proto.RecordResponses:
		newest := slices.Clone(d.Responses)
		slices.Reverse(newest)
		from, to, ok := page(len(newest), recordResponsesPage)
		if !ok {
			return out, proto.DecideInvalid, "after is the next a page of the responses gave"
		}
		out.Responses, out.Total = newest[from:to], len(newest)
		if from == 0 {
			out.Answer = d.Answer
		}
		if to < len(newest) {
			out.Next = strconv.Itoa(to)
		}
	case proto.RecordHistory:
		newest := slices.Clone(d.History)
		slices.Reverse(newest)
		from, to, ok := page(len(newest), recordHistoryPage)
		if !ok {
			return out, proto.DecideInvalid, "after is the next a page of the history gave"
		}
		out.History, out.Total = newest[from:to], len(newest)
		if to < len(newest) {
			out.Next = strconv.Itoa(to)
		}
	}
	return out, "", ""
}

// mustN is the n of a decision's id, 0 for none.
func mustN(id string) int64 {
	n, _ := ledger.ParseDecisionID(id)
	return n
}

// laneRecordFrame answers a decision_record: the part of rec, the record
// read whole once and judged in the scope on that same read
// (laneDecisionRef), reported as DR1's route reports it, the head's refs
// resolved in the same request as the route resolves them. No second
// read is made: a record that moves out of the scope after the read is
// served as it was when it was in it, and the next part asked at its mark
// is record_moved or not_in_scope.
func (s *Server) laneRecordFrame(ctx context.Context, m proto.DecisionRecord, rec ledger.DecisionRecord) []byte {
	if s.runs == nil {
		return refusal(m.ReqID, proto.DecideNotFound, "this server runs no crews, so no decisions")
	}
	d := s.runs.DecisionOf(rec)
	var refs []decisionRef
	if m.Part == proto.RecordHead {
		// Resolved at once after the read, as the route resolves them.
		refs = s.decisionRefs(ctx, d.Evidence)
	}
	if seam := s.laneSeam; seam != nil && seam.recordRead != nil {
		seam.recordRead(m.Part, d.Rev)
	}
	part, code, words := recordPart(d, refs, m.Part, m.Rev, m.After)
	if code != "" {
		f := proto.DecideRefused{T: proto.CtlDecideRefused, ReqID: m.ReqID, Code: code, Message: fitText(words, proto.MaxDecideWords)}
		if code == proto.DecideRecordMoved {
			f.Rev = part.Rev
		}
		return proto.MustControl(f)
	}
	return decisionFrame(m.ReqID, func(dd *proto.Decision, b json.RawMessage) { dd.Record = b }, part)
}

// refuseRecordDecide answers a decide on a record in the scope: an input
// (terminal keys, a question's alone) is invalid; any other is refused
// unsupported, nothing done, until DR1 accepts a decision link's actor.
func refuseRecordDecide(m proto.Decide) []byte {
	if m.Input != "" {
		return refusal(m.ReqID, proto.DecideInvalid, "a durable decision takes no terminal keys: input is a question's alone")
	}
	return refusal(m.ReqID, proto.DecideUnsupported, linkCannotAnswer+": nothing was done")
}

// errRecordDecide refuses a decide that does not map onto DR1's
// operation for its action.
var errRecordDecide = errors.New("invalid decide on a durable decision")

// recordResponse maps a decide on a record onto DR1's respond body,
// field for field: responseId the opId, choice, text and rationale as
// sent, expectedRev the revision. An approve names choice 0, a reject
// choice 1 (an approval's Approve and Reject are its options); input
// (terminal keys) and ticketRev are no field of DR1's.
func recordResponse(m proto.Decide) (crew.Response, error) {
	rev, err := strconv.Atoi(m.Revision)
	switch {
	case err != nil || rev < 1 || strconv.Itoa(rev) != m.Revision:
		return crew.Response{}, errors.New("revision is the record's rev the action carried")
	case m.Input != "" || m.TicketRev != 0:
		return crew.Response{}, errors.New("a durable decision takes no terminal keys and no ticket revision")
	case m.Action == "approve" && (m.Choice == nil || *m.Choice != 0), m.Action == "reject" && (m.Choice == nil || *m.Choice != 1):
		return crew.Response{}, errors.New("an approve is choice 0 and a reject choice 1")
	}
	return crew.Response{ResponseID: m.OpID, Choice: m.Choice, Text: m.Text, Rationale: m.Rationale, ExpectedRev: rev}, nil
}

// decideRecord takes a decide on DR1's record did through DR1's own
// operation for its action, as actor with auth, the same the routes call:
// answer, approve and reject a response (RespondDecision), more_info and
// comment a comment's (CommentDecision). Its receipt is the tracking key
// the action's steps are read by (decisionTrack), the opId under its
// kind: response:<opId> or comment:<opId>; repeat says DR1 held it
// already. A refusal is DR1's (crew.DecisionError, response_conflict
// among them), or errRecordDecide for a decide that does not map.
func (s *Server) decideRecord(ctx context.Context, did string, m proto.Decide, actor string, auth crew.Authority) (receipt string, repeat bool, err error) {
	switch m.Action {
	case "answer", "approve", "reject":
		resp, err := recordResponse(m)
		if err != nil {
			return "", false, errors.Join(errRecordDecide, err)
		}
		rc, err := s.runs.RespondDecision(ctx, did, resp, actor, auth)
		if err != nil {
			return "", false, err
		}
		return trackResponse + m.OpID, rc.Repeat, nil
	case crew.MoreInfoKind, crew.CommentKind:
		rev, err := strconv.Atoi(m.Revision)
		if err != nil || rev < 1 || m.Input != "" || m.Choice != nil || m.Rationale != "" || m.TicketRev != 0 {
			return "", false, errors.Join(errRecordDecide, errors.New("a comment carries its text and the record's rev alone"))
		}
		rc, err := s.runs.CommentDecision(ctx, did, m.OpID, rev, m.Action, m.Text, actor)
		if err != nil {
			return "", false, err
		}
		return trackComment + m.OpID, rc.Repeat, nil
	}
	return "", false, errors.Join(errRecordDecide, errors.New("no such action on a durable decision"))
}
