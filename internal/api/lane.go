package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// A decision link's lane (docs/plans/decision-inbox.md, PR-5;
// docs/protocol.md, Decision links): the decision inbox's items read and
// acted on by whoever holds a decision link, over the connection the link
// opens, a session's viewer (/ws/sessions/{id}) or a run's desk
// (/ws/runs/{run}/desk), the latter whether or not a member of the run
// lives.
//
// What one decision link allows, and nothing more:
//
//   - Which decisions: those of its scope alone. A session's link covers
//     that session's question and the task its attempt runs; a run's link
//     covers its members' questions and its tasks. A plan revision, another
//     session's question, another run's task, are in no link's scope.
//   - Which actions: those the item lists now (the inbox's actions), each
//     through the item's own operation (the question's claim, the engine's
//     command at the action's revision), which judges it as it would the
//     workbench's: an answer to a question no longer waiting is
//     question_answered, a command at a revision the run moved past
//     stale_revision. The reads: the scope's items, one item, the steps of
//     what was done through this link, and a bounded read of one evidence
//     entry in its scope (a check's log tail, a session's id).
//   - For how long, how many times: while the link stands. Every request
//     reads the link again (share.Store.Live), and the operation is checked
//     again right before it is taken, so a link revoked, expired or gone
//     (its session ended, its run forgotten, the server restarted: links
//     are in memory) refuses what comes after. An action is taken once per
//     opId: the same opId sent again is answered with the first receipt.
//   - As whom: the link's actor, link:<id> with its label, never the
//     workbench's; the steps read through it are its own receipts.
//
// What a decision link never allows: another decision (one of no
// session or run of its scope, a plan revision, an action the item does
// not list), through the lane keys typed into a terminal beyond a
// question's own choices or a line answering it, through its desk any
// terminal input or file read (the desk takes the lane's messages alone),
// an admin route (the token is a share token: every requireAdmin route
// and the inbox's refuse it), another run's desk, or a life longer than
// it was given. Each is a refusal here, never the page's hiding. Its
// connection to a session in its scope is a control link's, as a control
// link's always was (the session's terminal, and the files its policy
// lets a controller read): the lane adds the decisions to it, nothing
// else.

// laneActor is the actor of a decision link's receipts.
func laneActor(linkID string) string { return "link:" + linkID }

// laneSeams are the lane's seams for tests; nil outside them.
type laneSeams struct {
	// beforeDecide runs as a decide begins, before its link is read.
	beforeDecide func(linkID, opID string)
	// beforeReply runs once a decide is done, before its reply is sent.
	beforeReply func(reqID string, reply []byte)
}

// Lane is the Server as a decision link's lane (hostagent.Decisions).
type Lane struct{ s *Server }

// Lane is the server's lane, which a publisher hands its connections.
func (s *Server) Lane() *Lane { return &Lane{s: s} }

// laneScope is what a decision link covers, read as it stands.
type laneScope struct {
	link *share.Link
	// session is a session link's session; run a run link's run, or the
	// run of a session link's attempt, task that attempt's task.
	session, run, task string
}

// refusal is a decide_refused frame.
func refusal(reqID, code, words string) []byte {
	return proto.MustControl(proto.DecideRefused{T: proto.CtlDecideRefused, ReqID: reqID, Code: code, Message: fitText(words, proto.MaxDecideWords)})
}

// scope reads the decision link linkID as it stands: a refusal (its frame,
// naming reqID) for a link revoked, expired, gone or no decision link.
func (s *Server) scope(linkID, reqID string) (laneScope, []byte) {
	if linkID == "" || s.links == nil {
		return laneScope{}, refusal(reqID, proto.DecideNotDecisionLink, "this connection is no decision link's")
	}
	l, err := s.links.Live(linkID)
	switch {
	case errors.Is(err, share.ErrExpired):
		return laneScope{}, refusal(reqID, proto.DecideExpired, "the link has expired: nothing was done")
	case err != nil:
		return laneScope{}, refusal(reqID, proto.DecideRevoked, "the link no longer stands: nothing was done")
	case !l.Decide || l.Role != session.RoleControl || l.Group != nil:
		return laneScope{}, refusal(reqID, proto.DecideNotDecisionLink, "this link does not answer decisions")
	}
	sc := laneScope{link: l, run: l.RunID}
	if l.RunID == "" {
		sc.session = l.SessionID
		if d, ok := s.registry.Get(l.SessionID); ok {
			if c := d.Info().Crew; c != nil && c.Task != "" {
				sc.run, sc.task = c.RunID, c.Task
			}
		}
	}
	return sc, nil
}

// coversSession says whether the scope covers the session id: a session
// link's own, a run link's members'.
func (s *Server) coversSession(sc laneScope, id string) bool {
	if sc.session != "" {
		return id == sc.session
	}
	run, ok := s.runOf(id)
	return ok && run == sc.run
}

// coversTask says whether the scope covers the run's task: a run link's
// every task, a session link's attempt's.
func (sc laneScope) coversTask(run, task string) bool {
	return run != "" && run == sc.run && (sc.task == "" || sc.task == task) && (sc.session == "" || sc.task != "")
}

// covers says whether the scope covers the item ref names: a question of
// a session in it, a task of it; never anything else.
func (s *Server) covers(sc laneScope, ref brakeman.Ref) bool {
	switch ref.Kind {
	case brakeman.RefQuestion:
		return s.coversSession(sc, ref.Session)
	case brakeman.RefTask:
		return sc.coversTask(ref.Run, ref.Task)
	}
	return false
}

// itemRef parses an item's ref of the lane, in the scope: a refusal for
// one that does not parse or is not in it.
func (s *Server) itemRef(sc laneScope, reqID, v string) (brakeman.Ref, []byte) {
	ref, err := brakeman.ParseRef(v)
	if err != nil {
		return brakeman.Ref{}, refusal(reqID, proto.DecideInvalid, "ref is a question: or task: ref")
	}
	if !s.covers(sc, ref) {
		return brakeman.Ref{}, refusal(reqID, proto.DecideNotInScope, "this link does not cover "+ref.String())
	}
	return ref, nil
}

// laneItems is every open item in the scope, oldest first by (since, ref).
func (s *Server) laneItems(ctx context.Context, sc laneScope) []*inboxItem {
	rd := s.newInboxRead(ctx)
	var out []*inboxItem
	var infos []session.Info
	s.registry.Each(func(d session.Driver) { infos = append(infos, d.Info()) })
	for _, info := range infos {
		if s.coversSession(sc, info.ID) {
			if it, ok := rd.questionItem(info); ok {
				out = append(out, it)
			}
		}
	}
	if sc.run != "" && s.runs != nil {
		if run, ok := s.runs.Get(sc.run); ok && run.Managed != nil {
			for _, t := range run.Tasks {
				if t.State != "failed" || !sc.coversTask(run.ID, t.ID) {
					continue
				}
				if it, ok := rd.taskItem(run, t); ok {
					out = append(out, it)
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b *inboxItem) int { return a.before(inboxCursor{b.Since, b.Ref}) })
	return out
}

// decisionFrame is a TypeDecision frame of reqID with v as its field; a
// refusal for one past MaxDecision, which the bounds of what it carries
// never let happen.
func decisionFrame(reqID string, set func(*proto.Decision, json.RawMessage), v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return refusal(reqID, proto.DecideInvalid, "the reply could not be encoded")
	}
	d := proto.Decision{ReqID: reqID}
	set(&d, b)
	f, err := proto.EncodeDecision(d)
	if err != nil {
		return refusal(reqID, proto.DecideInvalid, "the reply passes its bound")
	}
	return f
}

// laneList is a page of the lane's list: at most proto.DecisionPage
// summaries after the cursor, next and total, and on the first page the
// link's Recent.
type laneList struct {
	Items  []inboxSummary `json:"items"`
	Next   string         `json:"next,omitempty"`
	Total  int            `json:"total"`
	At     time.Time      `json:"at"`
	Recent []share.Recent `json:"recent,omitempty"`
}

// lanePage is the page of all, in (since, ref) order, after the cursor:
// at most proto.DecisionPage, and the cursor after its last when more
// follow. An item that left since the page before is not there; one that
// came is, in its place.
func lanePage(all []*inboxItem, after inboxCursor) ([]*inboxItem, string) {
	page := slices.DeleteFunc(slices.Clone(all), func(it *inboxItem) bool { return !after.since.IsZero() && it.before(after) <= 0 })
	if len(page) > proto.DecisionPage {
		page = page[:proto.DecisionPage]
		return page, cursorOf(page[len(page)-1])
	}
	return page, ""
}

// List answers a page of the scope's items.
func (l *Lane) List(ctx context.Context, linkID, sessionID string, m proto.DecisionsList) []byte {
	s := l.s
	sc, no := s.scope(linkID, m.ReqID)
	if no != nil {
		return no
	}
	after, err := parseCursor(m.After)
	if err != nil {
		return refusal(m.ReqID, proto.DecideInvalid, err.Error())
	}
	all := s.laneItems(ctx, sc)
	out := laneList{Items: []inboxSummary{}, Total: len(all), At: time.Now().UTC()}
	page, next := lanePage(all, after)
	out.Next = next
	for _, it := range page {
		out.Items = append(out.Items, it.summary())
	}
	if m.After == "" {
		out.Recent = s.links.RecentOf(linkID)
	}
	return decisionFrame(m.ReqID, func(d *proto.Decision, b json.RawMessage) { d.List = b }, out)
}

// Read answers one item in the scope with its detail, open or as it is
// now; its evidence entries naming a session in the scope carry the id
// the holder opens it by.
func (l *Lane) Read(ctx context.Context, linkID, sessionID string, m proto.DecisionRead) []byte {
	s := l.s
	sc, no := s.scope(linkID, m.ReqID)
	if no != nil {
		return no
	}
	ref, no := s.itemRef(sc, m.ReqID, m.Ref)
	if no != nil {
		return no
	}
	it, err := s.inboxItemOf(ctx, ref)
	if errors.Is(err, errNoItem) {
		return refusal(m.ReqID, proto.DecideNotFound, "no such item")
	} else if err != nil {
		return refusal(m.ReqID, "unavailable", "the item could not be read")
	}
	for i, e := range it.Evidence {
		if r, err := brakeman.ParseRef(e.Ref); err == nil && r.Kind == brakeman.RefSession && s.coversSession(sc, r.Session) {
			if _, ok := s.registry.Get(r.Session); ok {
				it.Evidence[i].Session = r.Session
			}
		}
	}
	return decisionFrame(m.ReqID, func(d *proto.Decision, b json.RawMessage) { d.Item = b }, it)
}

// unknownSteps are four steps of which nothing is known but that the
// question was asked.
func unknownSteps(words string) []deliveryStep {
	return []deliveryStep{step(stepAvailable, stepDone, nil, "the session asked it", ""), step(stepRecorded, stepUnknown, nil, words, ""),
		step(stepValidated, stepUnknown, nil, words, ""), step(stepResumed, stepUnknown, nil, words, "")}
}

// laneTrack is a track's reply.
type laneTrack struct {
	Ref     string         `json:"ref"`
	Receipt string         `json:"receipt"`
	Attempt string         `json:"attempt"`
	Steps   []deliveryStep `json:"steps"`
}

// steps are the four steps of the action attempt on the item ref in the
// scope, receipt what its steps are read by: a question's id (attempt the
// opId its answer was sent with), a task command's id, the link's own.
func (s *Server) steps(ctx context.Context, sc laneScope, ref brakeman.Ref, receipt, attempt string) ([]deliveryStep, string, error) {
	switch ref.Kind {
	case brakeman.RefQuestion:
		if receipt != ref.Question {
			return nil, "a question's receipt is its own id", nil
		}
		d, ok := s.registry.Get(ref.Session)
		if !ok {
			return unknownSteps("the session is no longer listed: what it kept is gone"), "", nil
		}
		st, known := s.questionSteps(d, ref.Question, attempt)
		if !known {
			return unknownSteps("the session keeps no result of this question (it keeps its last 64)"), "", nil
		}
		return st, "", nil
	case brakeman.RefTask:
		if !crew.CommandIDPattern.MatchString(receipt) {
			return nil, "receipt is the command's id", nil
		}
		st, err := s.commandSteps(ctx, ref.Run, ref.Task, laneActor(sc.link.ID), receipt)
		return st, "", err
	}
	return nil, "ref is a question: or task: ref", nil
}

// Track answers the four steps of one action taken through this link.
func (l *Lane) Track(ctx context.Context, linkID, sessionID string, m proto.DecisionTrack) []byte {
	s := l.s
	sc, no := s.scope(linkID, m.ReqID)
	if no != nil {
		return no
	}
	ref, no := s.itemRef(sc, m.ReqID, m.Ref)
	if no != nil {
		return no
	}
	st, bad, err := s.steps(ctx, sc, ref, m.Receipt, m.Attempt)
	switch {
	case bad != "":
		return refusal(m.ReqID, proto.DecideInvalid, bad)
	case err != nil:
		return refusal(m.ReqID, "unavailable", "the receipt could not be read")
	}
	return decisionFrame(m.ReqID, func(d *proto.Decision, b json.RawMessage) { d.Track = b }, laneTrack{Ref: ref.String(), Receipt: m.Receipt, Attempt: m.Attempt, Steps: st})
}

// Record answers a part of a DR1 record: none is an item before DR1's
// records join the inbox (the plan's PR-3).
func (l *Lane) Record(ctx context.Context, linkID, sessionID string, m proto.DecisionRecord) []byte {
	if _, no := l.s.scope(linkID, m.ReqID); no != nil {
		return no
	}
	return refusal(m.ReqID, proto.DecideUnsupported, "durable decision records arrive with DR1")
}

// laneLogTail bounds a check log's tail an evidence read gives.
const laneLogTail = 4 << 10

// laneEvidence is an evidence read's reply: a check's log tail, or a
// session's id in the scope.
type laneEvidence struct {
	Ref     string `json:"ref"`
	Kind    string `json:"kind"` // log | session
	Tail    string `json:"tail,omitempty"`
	Cut     bool   `json:"cut,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Exit    int    `json:"exit"`
	Dropped bool   `json:"dropped,omitempty"`
	Session string `json:"session,omitempty"`
}

// tailOf is the last n bytes of b, begun on a rune's first byte.
func tailOf(b []byte, n int) ([]byte, bool) {
	if len(b) <= n {
		return b, false
	}
	b = b[len(b)-n:]
	for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.RuneStart(b[0]); i++ {
		b = b[1:]
	}
	return b, true
}

// Evidence answers a bounded read of one evidence entry, named by its own
// ref, of the item ref: a check of a task in the scope (its log's last 4
// KiB), or a session in it (its id); anything else is opened on the
// desktop or the LAN.
func (l *Lane) Evidence(ctx context.Context, linkID, sessionID string, m proto.DecisionEvidence) []byte {
	s := l.s
	sc, no := s.scope(linkID, m.ReqID)
	if no != nil {
		return no
	}
	if _, no := s.itemRef(sc, m.ReqID, m.Ref); no != nil {
		return no
	}
	ev, err := brakeman.ParseRef(m.Evidence)
	if err != nil {
		return refusal(m.ReqID, proto.DecideInvalid, "evidence is an evidence entry's ref")
	}
	switch ev.Kind {
	case brakeman.RefCheck:
		if !sc.coversTask(ev.Run, ev.Task) {
			return refusal(m.ReqID, proto.DecideNotInScope, "this link does not cover "+ev.String())
		}
		if s.ledger == nil {
			return refusal(m.ReqID, "unavailable", "there is no ledger to read the check from")
		}
		row, ok, err := s.ledger.Check(ctx, ev.Run, ev.Task, ev.N, ev.Check, ev.K)
		if err != nil {
			return refusal(m.ReqID, "unavailable", "the check could not be read")
		}
		if !ok {
			return refusal(m.ReqID, proto.DecideNotFound, "no such check")
		}
		out := laneEvidence{Ref: ev.String(), Kind: "log", Bytes: row.LogBytes, SHA256: row.LogSHA256, Exit: row.Exit, Dropped: row.LogDropped}
		log := row.Log
		if row.LogDropped {
			log = []byte(row.Tail)
		}
		tail, cut := tailOf(log, laneLogTail)
		out.Tail, out.Cut = string(tail), cut
		return decisionFrame(m.ReqID, func(d *proto.Decision, b json.RawMessage) { d.Evidence = b }, out)
	case brakeman.RefSession:
		if !s.coversSession(sc, ev.Session) {
			return refusal(m.ReqID, proto.DecideNotInScope, "this link does not cover "+ev.String())
		}
		if _, ok := s.registry.Get(ev.Session); !ok {
			return refusal(m.ReqID, proto.DecideNotFound, "the session is no longer listed")
		}
		return decisionFrame(m.ReqID, func(d *proto.Decision, b json.RawMessage) { d.Evidence = b }, laneEvidence{Ref: ev.String(), Kind: "session", Session: ev.Session})
	}
	return refusal(m.ReqID, proto.DecideUnsupported, "open it on the desktop or the LAN")
}

// laneOK is a decide_ok frame.
func laneOK(reqID, ref, receipt string, repeat bool) []byte {
	return proto.MustControl(proto.DecideOK{T: proto.CtlDecideOK, ReqID: reqID, Ref: ref, Receipt: receipt, Repeat: repeat})
}

// commandTarget is the task a command on the item ref acts on: the task
// itself, or, for a question's item (a blocker's question carries its
// task's commands), the task its session's attempt runs.
func (s *Server) commandTarget(ref brakeman.Ref) (run, task string, ok bool) {
	switch ref.Kind {
	case brakeman.RefTask:
		return ref.Run, ref.Task, true
	case brakeman.RefQuestion:
		if d, found := s.registry.Get(ref.Session); found {
			if c := d.Info().Crew; c != nil && c.Task != "" {
				return c.RunID, c.Task, true
			}
		}
	}
	return "", "", false
}

// isCommand says whether name is a task command the lane takes.
func isCommand(name string) bool {
	return name == crew.TaskRetry || name == crew.TaskAccept || name == crew.TaskCancel
}

// note keeps an action taken through the link in its Recent.
func (s *Server) note(linkID, ref, receipt, attempt string) {
	s.links.NoteRecent(linkID, share.Recent{Ref: ref, Receipt: receipt, Attempt: attempt, At: time.Now().UTC()})
	s.lanes.poke(linkID)
}

// commandReply is the reply to a task command's receipt (a repeat's too):
// decide_ok, or, for one the engine refused and kept as its receipt,
// decide_refused naming that receipt.
func commandReply(reqID, ref string, rc crew.Receipt, repeat bool) []byte {
	if rc.Status == ledger.CommandFailed {
		code := cmpOr(rc.Refusal, crew.RefusalTaskState)
		words := rc.Error
		if code == crew.RefusalStaleRevision {
			words = "the run moved on since the revision you read: nothing was done"
		}
		return proto.MustControl(proto.DecideRefused{T: proto.CtlDecideRefused, ReqID: reqID, Code: code, Message: fitText(words, proto.MaxDecideWords), Ref: ref, Receipt: rc.CommandID})
	}
	return laneOK(reqID, ref, rc.CommandID, repeat)
}

// repeat answers a decide whose opId the operation already holds for this
// link's actor with that first receipt, before the item's current actions
// are judged; nil when it holds none.
func (s *Server) repeat(ctx context.Context, sc laneScope, ref brakeman.Ref, m proto.Decide) []byte {
	switch {
	case m.Action == "answer" && ref.Kind == brakeman.RefQuestion:
		d, ok := s.registry.Get(ref.Session)
		if !ok {
			return nil
		}
		local, ok := d.(*session.Local)
		if !ok {
			return nil
		}
		if r, ok := local.QuestionResult(ref.Question); ok && r.ReqID == m.OpID {
			return laneOK(m.ReqID, ref.String(), ref.Question, true)
		}
	case isCommand(m.Action) && s.ledger != nil:
		run, task, ok := s.commandTarget(ref)
		if !ok {
			return nil
		}
		row, found, err := s.ledger.Receipt(ctx, run, laneActor(sc.link.ID), m.OpID)
		if err != nil || !found || row.Intent != m.Action {
			return nil
		}
		var rc crew.Receipt
		if json.Unmarshal([]byte(row.Receipt), &rc) != nil {
			rc = crew.Receipt{Status: row.Status}
		}
		rc.CommandID = m.OpID
		return commandReply(m.ReqID, "task:"+run+"/"+task, rc, true)
	}
	return nil
}

// Decide takes one action through the item's own operation, as the link.
func (l *Lane) Decide(ctx context.Context, linkID, sessionID string, m proto.Decide, answer func(context.Context, proto.Answer) error) []byte {
	s := l.s
	if seam := s.laneSeam; seam != nil && seam.beforeDecide != nil {
		seam.beforeDecide(linkID, m.OpID)
	}
	sc, no := s.scope(linkID, m.ReqID)
	if no != nil {
		return no
	}
	ref, no := s.itemRef(sc, m.ReqID, m.Ref)
	if no != nil {
		return no
	}
	if f := s.repeat(ctx, sc, ref, m); f != nil {
		return f
	}
	it, err := s.inboxItemOf(ctx, ref)
	if errors.Is(err, errNoItem) {
		return refusal(m.ReqID, proto.DecideNotFound, "no such item")
	} else if err != nil {
		return refusal(m.ReqID, "unavailable", "the item could not be read")
	}
	switch {
	case m.Action == "answer" && ref.Kind == brakeman.RefQuestion:
		return s.laneAnswer(ctx, linkID, sessionID, sc, ref, it, m, answer)
	case isCommand(m.Action):
		return s.laneCommand(ctx, linkID, sc, ref, it, m)
	}
	return refusal(m.ReqID, proto.DecideInvalid, "no such action on this item")
}

// laneAnswer answers the question ref with m's choice or line: through the
// connection's own answer when the question is its session's, else
// through the question's claim as the link (AnswerAs, Submit). Only an
// answer the item offers now is taken; the session judges the rest.
func (s *Server) laneAnswer(ctx context.Context, linkID, sessionID string, sc laneScope, ref brakeman.Ref, it *inboxItem, m proto.Decide, own func(context.Context, proto.Answer) error) []byte {
	if m.Revision != ref.Question || m.Choice != nil || m.Rationale != "" || (m.Input == "") == (m.Text == "") {
		return refusal(m.ReqID, proto.DecideInvalid, "an answer names its question's id as its revision and carries a choice's keys or a line")
	}
	offered := false
	for _, a := range it.Actions {
		if a.Name == "answer" && a.Expect == ref.Question && ((m.Input != "" && a.Input == m.Input) || (m.Text != "" && a.Text)) {
			offered = true
		}
	}
	if !offered {
		if it.Revision != ref.Question || !slices.ContainsFunc(it.Actions, func(a inboxAction) bool { return a.Name == "answer" }) {
			return refusal(m.ReqID, proto.DecideAnswered, "the question is no longer one an answer is taken for: nothing was typed")
		}
		return refusal(m.ReqID, proto.DecideInvalid, "not one of the question's choices: nothing was typed")
	}
	d, ok := s.registry.Get(ref.Session)
	if !ok {
		return refusal(m.ReqID, proto.DecideAnswered, "the session is no longer listed: nothing was typed")
	}
	local, isLocal := d.(*session.Local)
	if !isLocal {
		return refusal(m.ReqID, proto.DecideUnsupported, "a hosted session's question is answered where it runs")
	}
	// The link again, as the operation is taken.
	if _, no := s.scope(linkID, m.ReqID); no != nil {
		return no
	}
	var err error
	if ref.Session == sessionID && own != nil {
		err = own(ctx, proto.Answer{T: proto.CtlAnswer, ReqID: m.OpID, Question: ref.Question, Input: m.Input, Text: m.Text})
	} else {
		opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), submitTimeout)
		defer cancel()
		name := cmpOr(sc.link.Label, "a decision link")
		if m.Input != "" {
			err = local.AnswerAs(opCtx, ref.Question, m.Input, session.Actor{Name: name, ReqID: m.OpID, Withdrawn: ctx.Done()})
		} else {
			done := make(chan error, 1)
			_, err = local.Submit(opCtx, session.Submission{Text: m.Text, Question: ref.Question, ByName: name, ReqID: m.OpID, Withdrawn: ctx.Done(),
				Finished: func(res session.SubmitResult, e error) { done <- session.AnswerWord(res, e) }})
			if errors.Is(err, session.ErrSubmitPending) {
				select {
				case err = <-done:
				case <-opCtx.Done():
				}
			}
		}
	}
	if err != nil && !errors.Is(err, session.ErrSubmitPending) {
		code, words := proto.ErrCodeQuestionAnswered, "the question is no longer the one waiting: nothing was typed"
		switch {
		case errors.Is(err, session.ErrQuestionAnswered):
		case errors.Is(err, session.ErrNotAChoice):
			code, words = proto.DecideInvalid, err.Error()
		default:
			if c, w, ok := session.LinkRefusal(err); ok {
				code, words = c, w
			} else {
				code, words = "input_failed", "the answer could not be delivered: "+err.Error()
			}
		}
		return refusal(m.ReqID, code, words)
	}
	s.note(linkID, ref.String(), ref.Question, m.OpID)
	return laneOK(m.ReqID, ref.String(), ref.Question, false)
}

// laneCommand takes a task command the item offers now, at the revision
// the decide carries, as the link: the engine judges it (stale_revision
// for a run that moved on). One the item no longer offers is refused,
// nothing done.
func (s *Server) laneCommand(ctx context.Context, linkID string, sc laneScope, ref brakeman.Ref, it *inboxItem, m proto.Decide) []byte {
	if m.Input != "" || m.Text != "" || m.Choice != nil || m.Rationale != "" {
		return refusal(m.ReqID, proto.DecideInvalid, "a task command carries its revision alone")
	}
	rev, err := strconv.ParseInt(m.Revision, 10, 64)
	if err != nil || rev <= 0 {
		return refusal(m.ReqID, proto.DecideInvalid, "revision is the run's revision the action carried")
	}
	run, task, ok := s.commandTarget(ref)
	if !ok || !sc.coversTask(run, task) {
		return refusal(m.ReqID, proto.DecideNotInScope, "this link does not cover the task")
	}
	if !slices.ContainsFunc(it.Actions, func(a inboxAction) bool { return a.Name == m.Action }) {
		words := "the item no longer offers this: nothing was done"
		if it.Now != "" {
			words = "the item moved on (" + it.Now + "): nothing was done"
		}
		return refusal(m.ReqID, proto.DecideStaleRevision, words)
	}
	if _, no := s.scope(linkID, m.ReqID); no != nil {
		return no
	}
	// The command is the engine's whatever becomes of the connection: once
	// begun, its receipt stands, and the same opId asks for it again.
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	cmd := crew.Command{ID: m.OpID, ExpectedRevision: rev}
	var rc crew.Receipt
	switch m.Action {
	case crew.TaskRetry:
		rc, err = s.runs.Retry(opCtx, run, laneActor(linkID), cmd, task)
	case crew.TaskAccept:
		rc, err = s.runs.Accept(opCtx, run, laneActor(linkID), cmd, task)
	default:
		rc, err = s.runs.Cancel(opCtx, run, laneActor(linkID), cmd, task)
	}
	target := "task:" + run + "/" + task
	if rc.CommandID != "" {
		s.note(linkID, target, rc.CommandID, m.OpID)
		return commandReply(m.ReqID, target, rc, rc.Repeat)
	}
	code, words := "unavailable", "the command could not be taken"
	switch {
	case errors.Is(err, crew.ErrCommandConflict):
		code, words = proto.DecideInvalid, "this opId was used before with another action"
	case errors.Is(err, crew.ErrStaleRevision):
		code, words = proto.DecideStaleRevision, "the run moved on since the revision you read: nothing was done"
	case errors.Is(err, crew.ErrRunStopped), errors.Is(err, crew.ErrRunContinued), errors.Is(err, crew.ErrTaskState), errors.Is(err, crew.ErrRunNotFound), errors.Is(err, crew.ErrTaskNotFound):
		code, words = proto.DecideStaleRevision, "the item moved on: "+err.Error()
	case errors.Is(err, crew.ErrTooManyCommands):
		code, words = proto.DecideTooMany, err.Error()
	}
	return refusal(m.ReqID, code, words)
}

// laneHash is what the link's viewer sees, hashed: its scope's open items
// as summarized, their actions' revisions, and the steps of each entry of
// the link's Recent; "" for a link that no longer stands.
func (s *Server) laneHash(ctx context.Context, linkID string) string {
	sc, no := s.scope(linkID, "")
	if no != nil {
		return ""
	}
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, it := range s.laneItems(ctx, sc) {
		_ = enc.Encode(it.summary())
		for _, a := range it.Actions {
			_ = enc.Encode([]string{a.Name, a.Expect, a.Input})
		}
	}
	for _, r := range s.links.RecentOf(linkID) {
		if ref, err := brakeman.ParseRef(r.Ref); err == nil {
			st, _, _ := s.steps(ctx, sc, ref, r.Receipt, r.Attempt)
			_ = enc.Encode(st)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// laneSettle is how long a watch collects the events that follow one
// before it reads the scope again: a burst costs a read or two.
const laneSettle = 100 * time.Millisecond

// Watch calls changed whenever the hash of what the link's viewer sees
// differs from the last it saw, read again on each of the server's events
// (a session's, a run's, Dispatch's) and on each action noted in the
// link's Recent, until stop.
func (l *Lane) Watch(linkID, sessionID string, changed func()) (stop func()) {
	s := l.s
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ch := s.events.subscribe()
		defer func() { s.events.unsubscribe(ch) }()
		poked, unwatch := s.lanes.watch(linkID)
		defer unwatch()
		last := s.laneHash(ctx, linkID)
		settle := time.NewTimer(0)
		<-settle.C
		pending := false
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					// Dropped as a slow client: subscribed again, and the
					// scope read again as for an event.
					ch = s.events.subscribe()
				}
				if !pending {
					pending = true
					settle.Reset(laneSettle)
				}
			case <-poked:
				if !pending {
					pending = true
					settle.Reset(laneSettle)
				}
			case <-settle.C:
				pending = false
				if h := s.laneHash(ctx, linkID); h != last {
					last = h
					changed()
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// laneConn is one connection's side of the lane: the requests under way,
// at most proto.MaxDecideInFlight decides and
// proto.MaxDecisionReadsInFlight reads, each on a goroutine of its own,
// its reply sent as it is done; nil for a connection of no decision link,
// which is refused each request (not_decision_link).
type laneConn struct {
	lane              *Lane
	ctx               context.Context
	linkID, sessionID string
	send              func([]byte)
	answer            func(context.Context, proto.Answer) error
	decides, reads    atomic.Int32
	wg                sync.WaitGroup
	stopWatch         func()
	seq               atomic.Int64
}

// newLane opens the lane of a decision link's connection: its requests
// are served under ctx, replies sent through send, and decisions_changed
// once what it sees changes.
func (s *Server) newLane(ctx context.Context, linkID, sessionID string, send func([]byte), answer func(context.Context, proto.Answer) error) *laneConn {
	c := &laneConn{lane: s.Lane(), ctx: ctx, linkID: linkID, sessionID: sessionID, send: send, answer: answer}
	c.stopWatch = c.lane.Watch(linkID, sessionID, func() {
		send(proto.MustControl(proto.DecisionsChanged{T: proto.CtlDecisionsChanged, Seq: c.seq.Add(1)}))
	})
	return c
}

// end stops the watch and waits for the requests under way.
func (c *laneConn) end() {
	if c == nil {
		return
	}
	c.stopWatch()
	c.wg.Wait()
}

// control serves a lane control message, true when payload is one (a
// connection of no decision link refused not_decision_link); false
// leaves it to the connection's other controls.
func (c *laneConn) control(payload []byte, send func([]byte)) bool {
	t, err := proto.ParseHeader(payload)
	if err != nil {
		return false
	}
	msg, reqID, isLane, err := proto.DecodeLane(t, payload)
	switch {
	case !isLane:
		return false
	case c == nil:
		send(refusal(reqID, proto.DecideNotDecisionLink, "this connection is no decision link's"))
		return true
	case err != nil:
		send(refusal(reqID, proto.DecideInvalid, err.Error()))
		return true
	}
	if c.reads.Add(1) > proto.MaxDecisionReadsInFlight {
		c.reads.Add(-1)
		c.send(refusal(reqID, proto.DecideTooMany, "four reads are under way on this connection"))
		return true
	}
	c.wg.Go(func() {
		defer c.reads.Add(-1)
		var f []byte
		switch m := msg.(type) {
		case *proto.DecisionsList:
			f = c.lane.List(c.ctx, c.linkID, c.sessionID, *m)
		case *proto.DecisionRead:
			f = c.lane.Read(c.ctx, c.linkID, c.sessionID, *m)
		case *proto.DecisionTrack:
			f = c.lane.Track(c.ctx, c.linkID, c.sessionID, *m)
		case *proto.DecisionRecord:
			f = c.lane.Record(c.ctx, c.linkID, c.sessionID, *m)
		case *proto.DecisionEvidence:
			f = c.lane.Evidence(c.ctx, c.linkID, c.sessionID, *m)
		}
		c.send(f)
	})
	return true
}

// decide serves a TypeDecide frame.
func (c *laneConn) decide(payload []byte, send func([]byte)) {
	reqID := proto.DecideReqID(payload)
	if c == nil {
		send(refusal(reqID, proto.DecideNotDecisionLink, "this connection is no decision link's"))
		return
	}
	m, err := proto.DecodeDecide(payload)
	if err != nil {
		c.send(refusal(reqID, proto.DecideInvalid, err.Error()))
		return
	}
	if c.decides.Add(1) > proto.MaxDecideInFlight {
		c.decides.Add(-1)
		c.send(refusal(m.ReqID, proto.DecideTooMany, "four decisions are under way on this connection"))
		return
	}
	c.wg.Go(func() {
		defer c.decides.Add(-1)
		f := c.lane.Decide(c.ctx, c.linkID, c.sessionID, m, c.answer)
		if seam := c.lane.s.laneSeam; seam != nil && seam.beforeReply != nil {
			seam.beforeReply(m.ReqID, f)
		}
		c.send(f)
	})
}

// isDecisionLink says whether linkID is a decision link's, as it stands.
func (s *Server) isDecisionLink(linkID string) bool {
	if linkID == "" || s.links == nil {
		return false
	}
	l, err := s.links.Live(linkID)
	return err == nil && l.Decide
}

// ownAnswer is a server viewer's own answer, for its lane: as its `answer`
// message is taken (Server.answer), its word waited for as long as a
// line's submission takes at most; past that, or once the connection
// ends, it is accepted, its outcome left to its steps.
func (s *Server) ownAnswer(sub *session.Subscription, local *session.Local) func(context.Context, proto.Answer) error {
	return func(ctx context.Context, a proto.Answer) error {
		done := make(chan error, 1)
		err := s.answer(sub, local, a, func(e error) { done <- e })
		if !errors.Is(err, session.ErrSubmitPending) {
			return err
		}
		wait, cancel := context.WithTimeout(ctx, submitTimeout)
		defer cancel()
		select {
		case err := <-done:
			return err
		case <-wait.Done():
			return session.ErrSubmitPending
		}
	}
}

// laneState is the lane's state on this server: the connections of the
// run desks open, by their link, which a revoke, an expiry or the run's
// forgetting closes; and the watches of each link, woken when its Recent
// changes.
type laneState struct {
	mu      sync.Mutex
	conns   map[string]map[*wsSink]struct{}
	watches map[string]map[chan struct{}]struct{}
}

// watch is a channel woken when linkID's Recent changes, and its end.
func (d *laneState) watch(linkID string) (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.watches == nil {
		d.watches = map[string]map[chan struct{}]struct{}{}
	}
	if d.watches[linkID] == nil {
		d.watches[linkID] = map[chan struct{}]struct{}{}
	}
	d.watches[linkID][ch] = struct{}{}
	return ch, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		delete(d.watches[linkID], ch)
		if len(d.watches[linkID]) == 0 {
			delete(d.watches, linkID)
		}
	}
}

// poke wakes linkID's watches, none waited for.
func (d *laneState) poke(linkID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for ch := range d.watches[linkID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// maxDesks bounds the desks one link holds open at once, as a session
// bounds its viewers.
const maxDesks = 8

// add holds sink as one of linkID's desks; false past maxDesks.
func (d *laneState) add(linkID string, sink *wsSink) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.conns[linkID]) >= maxDesks {
		return false
	}
	if d.conns == nil {
		d.conns = map[string]map[*wsSink]struct{}{}
	}
	if d.conns[linkID] == nil {
		d.conns[linkID] = map[*wsSink]struct{}{}
	}
	d.conns[linkID][sink] = struct{}{}
	return true
}

func (d *laneState) remove(linkID string, sink *wsSink) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.conns[linkID], sink)
	if len(d.conns[linkID]) == 0 {
		delete(d.conns, linkID)
	}
}

// closeDesks closes the desks of the links named, each as its link stands
// (expired, else revoked), or every desk with why when no link is named.
func (s *Server) closeDesks(why error, linkIDs ...string) {
	s.lanes.mu.Lock()
	var todo []*wsSink
	var reasons []error
	for id, conns := range s.lanes.conns {
		if len(linkIDs) > 0 && !slices.Contains(linkIDs, id) {
			continue
		}
		reason := why
		if reason == nil {
			reason = session.ErrRevoked
			if _, err := s.links.Live(id); errors.Is(err, share.ErrExpired) {
				reason = session.ErrExpired
			}
		}
		for c := range conns {
			todo, reasons = append(todo, c), append(reasons, reason)
		}
	}
	s.lanes.mu.Unlock()
	for i, c := range todo {
		c.Close(reasons[i])
	}
}

// handleRunDesk serves a run's desk at the server (GET
// /ws/runs/{run}/desk?token=): a run decision link's lane, with no
// terminal, whether or not a member of the run lives. The client sends
// its hello; the desk's welcome says decisions and carries no terminal
// fields. A token that is no decision link of the run is refused as a
// viewer's is, by close code.
func (s *Server) handleRunDesk(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run")
	p := s.authenticate(r, true)
	ok := p.link != nil && p.link.RunID == runID && p.link.Decide && p.link.Group == nil && !s.cfg.Switchyard.Enabled
	if !ok && !s.limiter.allow(clientKey(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	hk := &hijackKeeper{ResponseWriter: w}
	c, err := websocket.Accept(hk, r, s.acceptOptions())
	if err != nil {
		s.log.Debug("websocket accept failed", "err", err)
		return
	}
	c.SetReadLimit(readLimit)
	if p.link == nil {
		c.Close(proto.CloseUnauthorized, "unauthorized")
		return
	}
	if !ok {
		c.Close(proto.CloseForbidden, "not a decision link of this run")
		return
	}
	if _, err := s.readHello(r.Context(), c); err != nil {
		return
	}
	linkID := p.link.ID
	sink := s.viewerSink(c, hk.conn)
	// Held before the link is read again: a revoke marks the link and then
	// closes its desks, so either the read sees the mark or the close sees
	// the desk.
	if !s.lanes.add(linkID, sink) {
		c.Close(proto.CloseTooManyViewers, "too many desks for this link")
		return
	}
	defer s.lanes.remove(linkID, sink)
	defer sink.cancel()
	if _, no := s.scope(linkID, ""); no != nil {
		s.closeDesks(nil, linkID)
		return
	}
	send := func(f []byte) {
		if sink.WriteFrame(f) != nil {
			sink.Close(nil)
		}
	}
	welcome := proto.Welcome{T: proto.CtlWelcome, Proto: proto.ProtoVersion, Role: string(session.RoleControl), Transport: proto.TransportWS, Decisions: true}
	if sink.WriteFrame(proto.MustControl(welcome)) != nil {
		return
	}
	lane := s.newLane(sink.ctx, linkID, "", send, nil)
	defer lane.end()
	go keepalive(sink.ctx, c, s.pingEvery)
	for {
		f, err := readFrame(sink.ctx, c, 0)
		if err != nil {
			if !errors.Is(err, context.Canceled) && websocket.CloseStatus(err) == -1 {
				sink.Close(nil)
			}
			return
		}
		switch f.Type {
		case proto.TypeDecide:
			lane.decide(f.Payload, send)
		case proto.TypeControl:
			if lane.control(f.Payload, send) {
				continue
			}
			t, _ := proto.ParseHeader(f.Payload)
			switch t {
			case proto.CtlPing:
				var m proto.Ping
				_ = json.Unmarshal(f.Payload, &m)
				send(proto.MustControl(proto.Ping{T: proto.CtlPong, TS: m.TS}))
			case proto.CtlHello:
			default:
				send(proto.NewError(proto.ErrCodeBadFrame, "a desk takes the lane's messages alone"))
				c.Close(proto.CloseProtocolError, "unexpected control message")
				return
			}
		default:
			send(proto.NewError(proto.ErrCodeBadFrame, "a desk takes the lane's messages alone"))
			c.Close(proto.CloseProtocolError, "unexpected frame type")
			return
		}
	}
}
