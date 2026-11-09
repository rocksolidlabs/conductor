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
//   - For how long, how many times: while the link stands (rule 1), once
//     per opId (rule 2), below. A link revoked, expired or gone (its
//     session ended, its run forgotten, the server restarted: links are in
//     memory) refuses what comes after.
//   - As whom: the link's actor, link:<id>, never the workbench's, its
//     label beside it (laneActorNote); the steps read through it are its
//     own receipts.
//
// Three rules hold every path to them, and each is enforced in one place:
//
//  1. Nothing is begun for a link after its revoke or its expiry has
//     marked it; an act begun before may land (share.Store.Hold). An act
//     is held from the check that the link is not revoked or expired,
//     made under the hold as the act is taken, until it has returned (an
//     answer's process write; a task command's commit by the engine). A
//     revoke and an expiry end the link through the same barrier (the
//     sweep, share.Store.ExpireDue; a desk's or a decision viewer's own
//     deadline, Expire): from its start no act is held, and at its commit
//     boundary, just before it is begun, an act asks once more whether
//     its link stands, fail-closed (session.Held.Stands before a write is
//     begun, crew.Command.Stands before a command's change), so one whose
//     link ended while it was held is never begun. The barrier then waits
//     for the acts begun before it, up to the store's HoldWait, and
//     returns, logging how many were still under way; the viewers are
//     closed after it. Nothing begun is undone: a process write the
//     program has not taken by then resolves no question of the link, but
//     its bytes land as the program reads them; a command the engine
//     committed stays committed. A command's change is committed by the
//     transaction that asked its link, or not at all: one the ledger
//     refuses is put back and settles withdrawn, never carried by a later
//     transaction. The hold is taken as the session's writer takes an
//     answer's keys or a line (session.Actor.Hold, Submission.Hold,
//     AttachOptions.Hold: holdLink) and around the engine's command
//     (laneCommand), never once before and used later. An answer queued,
//     in flight or asked again when the link ends is refused, never
//     begun, on every path: the first try, a repeat answered from the
//     link's record (replay, under the hold too), a twin from another
//     connection, a queued send. An action whose connection closed before
//     it began is never begun (the lane's context, checked under the
//     hold, and an own-session answer's queued write withdrawn with it);
//     one begun is the operation's, its receipt kept.
//  2. An opId names one operation (share.Store.ClaimOp, Decide): the link
//     keeps, by opId, the fingerprint of the whole request as it was sent
//     (decideFingerprint: its item, action and every argument), and a
//     repeat is answered from that record only when the request is the
//     same; the same opId with anything else is op_conflict, nothing done,
//     from any connection of the link, at once or later. A twin under way
//     on another connection is waited for and answered with its outcome.
//  3. The lane reads only the decisions' own records as the server keeps
//     them, in the link's scope: the inbox's items (the sessions'
//     attention, the engine's tasks), the ledger's receipts and check rows
//     under the link's actor, the sessions' kept results under the link's
//     own request ids (answerReqID). Nothing is resolved afresh on the
//     link's word: no screen, no file, no run or session outside the scope;
//     the lane reads no file at all (a session's files stay the viewer's,
//     through session.ResolvePath and the session's Folder, as before).
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

// linkAnswerName is the name a decision link's answer is kept under, on
// every path (its own connection's answer, its claim of another session's
// question): its label, or "a decision link" for a link with none.
func linkAnswerName(label string) string { return cmpOr(label, "a decision link") }

// laneActor is the actor of a decision link's receipts.
func laneActor(linkID string) string { return "link:" + linkID }

// laneActorNote is how a command taken through the link is noted in its
// run's activity, which the run's record keeps: the link's actor with its
// label, so a reader of the ledger's link:<id> finds whose link it was
// after the link is gone.
func laneActorNote(l *share.Link, action, task string) string {
	return action + " of " + task + " taken through the link " + linkLabelOr(l.Label) + " (" + laneActor(l.ID) + ")"
}

// answerReqID is the request id a link's answer is kept under in its
// question's result (session.Actor.ReqID, proto.Answer.ReqID): the
// decide's opId bound to the link, so another link's answer, or the
// workbench's, under the same opId is never this link's (rule 2), and a
// track reads the winner by it. Printable ASCII of MaxAnswerReqID bytes.
func answerReqID(linkID, opID string) string {
	sum := sha256.Sum256([]byte(linkID + "\x00" + opID))
	return "link-" + hex.EncodeToString(sum[:])[:proto.MaxAnswerReqID-5]
}

// decideFingerprint is the whole request a decide is, as it was sent and
// decoded strictly, before anything of it is cleaned or mapped: every
// field but its reqId, which names the request, not the operation (rule
// 2).
func decideFingerprint(m proto.Decide) string {
	m.ReqID = ""
	b, _ := json.Marshal(m)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// laneSeams are the lane's seams for tests; nil outside them.
type laneSeams struct {
	// beforeDecide runs as a decide begins, before its link is read.
	beforeDecide func(linkID, opID string)
	// beforeOperation runs once a decide's item is judged and the action
	// found offered, before the operation is asked (its link held).
	beforeOperation func(linkID, opID string)
	// atHold runs as an answer's write is taken, before its link is held
	// (holdLink): in the session's writer.
	atHold func(linkID string)
	// afterHold runs once an answer's write has its link held, before the
	// write's commit check (linkHeld.Stands) and the write: in the
	// session's writer.
	afterHold func(linkID string)
	// beforeRead runs as a lane read begins, on its goroutine.
	beforeRead func(reqID string)
	// twinWaits runs as a decide finds its opId under way on another
	// connection, before it waits for it.
	twinWaits func(linkID, opID string)
	// hashing runs as a link's watch reads its scope (laneHash).
	hashing func(linkID string)
	// beforeReply runs once a decide is done, before its reply is sent.
	beforeReply func(reqID string, reply []byte)
	// commandCtx is handed the cancel of a task command's own context as
	// the command begins (laneCommand), so a test can fail the engine's
	// transactions after the taking.
	commandCtx func(opID string, cancel context.CancelFunc)
	// lapse is handed a desk's or a decision viewer's expiry as it is
	// armed at its link's deadline (armLapse), so a test can make it come
	// at a moment of its choosing.
	lapse func(linkID string, fire func())
	// beforeDeskAdd runs as a desk is about to be registered (laneState.add),
	// its hello read.
	beforeDeskAdd func(linkID string)
	// recordRead runs once a decision_record has read its record whole
	// (the read its scope was judged on) and resolved a head's refs, with
	// the part asked and the rev read, before the part is cut.
	recordRead func(part string, rev int)
	// scopeLimit, when above 0, is the most DR1 records a scope's read
	// takes in place of ledger.MaxScopeDecisions, so a test reaches the
	// bound with a few.
	scopeLimit int
}

// holdLink is the hold a decision link's answer takes as the session's
// writer takes its keys or line (rule 1): the link checked with the write
// and held until the write has returned (its end waits for it up to the
// store's HoldWait), its refusal said as the session says a link's
// (ErrRevoked, ErrExpired).
func (s *Server) holdLink(linkID string) func() (session.Held, error) {
	return s.holdLinkWith(linkID, nil)
}

// holdLinkWith is holdLink with the connection's own check of its viewer
// (stands, nil for none: a publisher's peer's credential and vouch, which
// Decisions.Decide carries) asked with the link's at the write's commit
// boundary: either refusing, nothing is written.
func (s *Server) holdLinkWith(linkID string, stands func() error) func() (session.Held, error) {
	return func() (session.Held, error) {
		if seam := s.laneSeam; seam != nil && seam.atHold != nil {
			seam.atHold(linkID)
		}
		h, err := s.links.Hold(linkID)
		if err != nil {
			return nil, sessionRefusal(err)
		}
		if seam := s.laneSeam; seam != nil && seam.afterHold != nil {
			seam.afterHold(linkID)
		}
		return &linkHeld{s: s, h: h, stands: stands}, nil
	}
}

// sessionRefusal is a link's refusal as the session says it: ErrExpired
// for an expired link, ErrRevoked for any other; nil for none.
func sessionRefusal(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, share.ErrExpired):
		return session.ErrExpired
	}
	return session.ErrRevoked
}

// linkHeld is a link's hold as the session's writer holds it
// (session.Held): share.Held, its refusals said as the session's.
type linkHeld struct {
	s      *Server
	h      *share.Held
	stands func() error
}

func (l *linkHeld) Stands() error {
	if err := sessionRefusal(l.h.Stands()); err != nil {
		return err
	}
	if l.stands != nil {
		return l.stands()
	}
	return nil
}

func (l *linkHeld) Cancelled() <-chan struct{} { return l.h.Cancelled() }
func (l *linkHeld) Release()                   { l.h.Release() }

// linkRefusal is the refusal of a request whose link no longer stands.
func linkRefusal(reqID string, err error) []byte {
	if errors.Is(err, share.ErrExpired) || errors.Is(err, session.ErrExpired) {
		return refusal(reqID, proto.DecideExpired, "the link has expired: nothing was done")
	}
	return refusal(reqID, proto.DecideRevoked, "the link no longer stands: nothing was done")
}

// laneOutcome is what a link keeps of an operation it took, by its opId
// (share.Store.ClaimOp's Outcome): the same request again is answered
// from it. Run names a task command's run, whose receipt the ledger keeps
// under the link's actor; Code a refusal the operation gave and the link
// keeps (an answer another answer came before).
type laneOutcome struct {
	Ref     string `json:"ref"`
	Receipt string `json:"receipt,omitempty"`
	Run     string `json:"run,omitempty"`
	Code    string `json:"code,omitempty"`
	Words   string `json:"words,omitempty"`
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

// laneItems is every open item in the scope, oldest first by (since, ref),
// and the marks of every DR1 record in the scope whatever its state (for
// the hash, laneDecisionItems), with cut the words of a scope whose DR1
// records passed the read's bound ("" for none): an error for DR1's
// records that could not be read.
func (s *Server) laneItems(ctx context.Context, sc laneScope) (_ []*inboxItem, _ []string, cut string, _ error) {
	rd := s.newInboxRead(ctx)
	var out []*inboxItem
	var infos []session.Info
	s.registry.Each(func(d session.Driver) { infos = append(infos, d.Info()) })
	for _, info := range infos {
		if s.coversSession(sc, info.ID) {
			// A question that mirrors a record is the record's item.
			if it, ok := rd.questionItem(info); ok && !rd.mirrorsRecord(info.Attention.Decision) {
				out = append(out, laneTakes(it))
			}
		}
	}
	records, marks, cut, err := s.laneDecisionItems(ctx, rd, sc)
	if err != nil {
		return nil, nil, "", err
	}
	out = append(out, records...)
	if sc.run != "" && s.runs != nil {
		if run, ok := s.runs.Get(sc.run); ok && run.Managed != nil {
			for _, t := range run.Tasks {
				if t.State != "failed" || !sc.coversTask(run.ID, t.ID) {
					continue
				}
				if it, ok := rd.taskItem(run, t); ok {
					out = append(out, laneTakes(it))
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b *inboxItem) int { return a.before(inboxCursor{b.Since, b.Ref}) })
	return out, marks, cut, nil
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
// link's Recent. Truncated, with Cut its words, says the scope's DR1
// records passed the read's bound (laneDecisionItems): total then counts
// what was read, and the scope holds more.
type laneList struct {
	Items     []inboxSummary `json:"items"`
	Next      string         `json:"next,omitempty"`
	Total     int            `json:"total"`
	Truncated bool           `json:"truncated,omitempty"`
	Cut       string         `json:"cut,omitempty"`
	At        time.Time      `json:"at"`
	Recent    []share.Recent `json:"recent,omitempty"`
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
	all, _, cut, err := s.laneItems(ctx, sc)
	if err != nil {
		s.log.Warn("lane: the scope's items could not be read", "err", err)
		return refusal(m.ReqID, "unavailable", "the decisions could not be read")
	}
	out := laneList{Items: []inboxSummary{}, Total: len(all), Truncated: cut != "", Cut: cut, At: time.Now().UTC()}
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
	if rec, is, no := s.laneDecisionRef(ctx, sc, m.ReqID, m.Ref); is {
		if no != nil {
			return no
		}
		return s.readRecordItem(ctx, sc, m.ReqID, rec)
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
	laneTakes(it)
	for i, e := range it.Evidence {
		if r, err := brakeman.ParseRef(e.Ref); err == nil && r.Kind == brakeman.RefSession && s.coversSession(sc, r.Session) {
			it.Evidence[i].Session = s.laneSessionID(sc, r.Session)
		}
	}
	return decisionFrame(m.ReqID, func(d *proto.Decision, b json.RawMessage) { d.Item = b }, it)
}

// laneSessionID is the id a link's holder opens the session id by, ""
// for none: on this server its own id, while it is listed; through a
// switchyard (a decision link minted there, share.Link.Remote) the id the
// switchyard knows it by, its publication's (PublishedSession.CurrentID),
// while that publication stands, for this server's own id opens nothing
// there.
func (s *Server) laneSessionID(sc laneScope, id string) string {
	if _, ok := s.registry.Get(id); !ok {
		return ""
	}
	if sc.link == nil || !sc.link.Remote {
		return id
	}
	pub := s.publishedOf(id)
	if pub == nil || ended(pub.Done()) {
		return ""
	}
	return pub.CurrentID()
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
// scope, receipt what its steps are read by: a question's id, its kept
// result read by the link's own request id for attempt (answerReqID: the
// winner is this link's attempt, or another answer came first), a task
// command's id, its receipt the link's own (rule 3).
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
		st, known := s.questionSteps(d, ref.Question, answerReqID(sc.link.ID, attempt))
		if !known {
			return unknownSteps("the session keeps no result of this question (it keeps its last 64)"), "", nil
		}
		return st, "", nil
	case brakeman.RefTask:
		if !crew.CommandIDPattern.MatchString(receipt) {
			return nil, "receipt is the command's id", nil
		}
		// A task command's receipt is the decide's own opId (laneCommand):
		// the attempt it names is that one, so the receipt of one action
		// is never read for another.
		if attempt != receipt {
			return nil, "a task command's attempt is its receipt, the decide's opId", nil
		}
		// Read under the link's actor, and bound to the task it was taken
		// for (commandSteps: a receipt of another task's command reads as
		// none of this task's).
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
	if _, is, no := s.laneDecisionRef(ctx, sc, m.ReqID, m.Ref); is {
		if no != nil {
			return no
		}
		// Nothing is done to a record through a link: its four states are
		// the record's, read with it (decision_read, decision_record).
		return refusal(m.ReqID, proto.DecideUnsupported, "a durable decision's four states are its record's: read its item or its record")
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

// Record answers a part of a DR1 record in the scope, cut from one whole
// read of it at the rev the part names (lane_decisions.go).
func (l *Lane) Record(ctx context.Context, linkID, sessionID string, m proto.DecisionRecord) []byte {
	s := l.s
	sc, no := s.scope(linkID, m.ReqID)
	if no != nil {
		return no
	}
	rec, is, no := s.laneDecisionRef(ctx, sc, m.ReqID, m.Ref)
	switch {
	case !is:
		return refusal(m.ReqID, proto.DecideInvalid, "ref is a durable decision's, decision:dr-N")
	case no != nil:
		return no
	}
	return s.laneRecordFrame(ctx, m, rec)
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
	if _, is, no := s.laneDecisionRef(ctx, sc, m.ReqID, m.Ref); is {
		if no != nil {
			return no
		}
	} else if _, no := s.itemRef(sc, m.ReqID, m.Ref); no != nil {
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
		id := s.laneSessionID(sc, ev.Session)
		if id == "" {
			return refusal(m.ReqID, proto.DecideNotFound, "the session is not published at the switchyard now")
		}
		return decisionFrame(m.ReqID, func(d *proto.Decision, b json.RawMessage) { d.Evidence = b }, laneEvidence{Ref: ev.String(), Kind: "session", Session: id})
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

// laneTakes leaves on it the actions a link's lane takes (an answer and
// the task commands): Check again is the workbench's.
func laneTakes(it *inboxItem) *inboxItem {
	it.Actions = slices.DeleteFunc(it.Actions, func(a inboxAction) bool { return a.Name != "answer" && !isCommand(a.Name) })
	return it
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

// replay answers a decide whose opId the link holds, settled, for the same
// request (rule 2) from what the link kept of it: a task command from its
// receipt in the ledger under the link's actor, an answer from its
// outcome; repeat, before the item's current actions are judged. The link
// must still stand, checked under its hold (rule 1).
func (s *Server) replay(ctx context.Context, linkID string, m proto.Decide, raw string) []byte {
	held, err := s.links.Hold(linkID)
	if err != nil {
		return linkRefusal(m.ReqID, err)
	}
	defer held.Release()
	var o laneOutcome
	if json.Unmarshal([]byte(raw), &o) != nil {
		return refusal(m.ReqID, "unavailable", "what the link kept of this opId could not be read")
	}
	switch {
	case o.Run != "":
		if s.ledger == nil {
			return refusal(m.ReqID, "unavailable", "there is no ledger to read the receipt from")
		}
		row, found, err := s.ledger.Receipt(ctx, o.Run, laneActor(linkID), m.OpID)
		if err != nil || !found {
			return refusal(m.ReqID, "unavailable", "the receipt could not be read")
		}
		var rc crew.Receipt
		if json.Unmarshal([]byte(row.Receipt), &rc) != nil {
			rc = crew.Receipt{Status: row.Status}
		}
		rc.CommandID = m.OpID
		return commandReply(m.ReqID, o.Ref, rc, true)
	case o.Code != "":
		return proto.MustControl(proto.DecideRefused{T: proto.CtlDecideRefused, ReqID: m.ReqID, Code: o.Code, Message: fitText(o.Words, proto.MaxDecideWords), Ref: o.Ref, Receipt: o.Receipt})
	}
	return laneOK(m.ReqID, o.Ref, o.Receipt, true)
}

// Decide takes one action through the item's own operation, as the link.
// Its opId is claimed first, for the whole request (rule 2): the same
// request the link took before is answered from it (replay), one under way
// on another connection is waited for, and another request under it is
// op_conflict, nothing done.
func (l *Lane) Decide(ctx context.Context, linkID, sessionID string, m proto.Decide, answer func(context.Context, proto.Answer) error, stands func() error) []byte {
	s := l.s
	if seam := s.laneSeam; seam != nil && seam.beforeDecide != nil {
		seam.beforeDecide(linkID, m.OpID)
	}
	sc, no := s.scope(linkID, m.ReqID)
	if no != nil {
		return no
	}
	if _, is, no := s.laneDecisionRef(ctx, sc, m.ReqID, m.Ref); is {
		if no != nil {
			return no
		}
		return refuseRecordDecide(m)
	}
	ref, no := s.itemRef(sc, m.ReqID, m.Ref)
	if no != nil {
		return no
	}
	fp := decideFingerprint(m)
	for {
		op, mine, err := s.links.ClaimOp(linkID, m.OpID, fp)
		switch {
		case errors.Is(err, share.ErrOpConflict):
			return refusal(m.ReqID, proto.DecideOpConflict, "this opId was used before for another request: nothing was done")
		case errors.Is(err, share.ErrTooManyOps):
			return refusal(m.ReqID, proto.DecideTooMany, "this link has taken as many actions as it keeps: nothing was done")
		case err != nil:
			return linkRefusal(m.ReqID, err)
		case mine:
			return s.decideNew(ctx, linkID, sessionID, sc, ref, m, answer, stands)
		case op.Settled:
			return s.replay(ctx, linkID, m, op.Outcome)
		}
		if seam := s.laneSeam; seam != nil && seam.twinWaits != nil {
			seam.twinWaits(linkID, m.OpID)
		}
		select {
		case <-op.Done:
		case <-ctx.Done():
			return refusal(m.ReqID, "unavailable", "the connection closed: nothing was done")
		}
	}
}

// decideNew takes a decide whose opId it holds (Decide): kept for the link
// once the operation was asked and gave an outcome, let go when nothing was
// begun (refused before the operation, the connection gone), so the opId
// is free again.
func (s *Server) decideNew(ctx context.Context, linkID, sessionID string, sc laneScope, ref brakeman.Ref, m proto.Decide, answer func(context.Context, proto.Answer) error, stands func() error) []byte {
	var kept *laneOutcome
	defer func() {
		raw := ""
		if kept != nil {
			b, _ := json.Marshal(kept)
			raw = string(b)
		}
		s.links.SettleOp(linkID, m.OpID, kept != nil, raw)
	}()
	it, err := s.inboxItemOf(ctx, ref)
	if errors.Is(err, errNoItem) {
		return refusal(m.ReqID, proto.DecideNotFound, "no such item")
	} else if err != nil {
		return refusal(m.ReqID, "unavailable", "the item could not be read")
	}
	var f []byte
	switch {
	case m.Action == "answer" && ref.Kind == brakeman.RefQuestion:
		f, kept = s.laneAnswer(ctx, linkID, sessionID, sc, ref, it, m, answer, stands)
	case isCommand(m.Action):
		f, kept = s.laneCommand(ctx, linkID, sc, ref, it, m, stands)
	default:
		f = refusal(m.ReqID, proto.DecideInvalid, "no such action on this item")
	}
	return f
}

// beforeOperation runs the seam between an action's judgement and its
// operation.
func (s *Server) beforeOperation(linkID, opID string) {
	if seam := s.laneSeam; seam != nil && seam.beforeOperation != nil {
		seam.beforeOperation(linkID, opID)
	}
}

// begunGone refuses an action whose connection closed before it began:
// never begun (rule 1).
func begunGone(reqID string) []byte {
	return refusal(reqID, "unavailable", "the connection closed before the action began: nothing was done")
}

// laneAnswer answers the question ref with m's choice or line: through the
// connection's own answer when the question is its session's, else
// through the question's claim as the link (AnswerAs, Submit). Only an
// answer the item offers now is taken; the session judges the rest. The
// answer is kept under the link's own request id (answerReqID), and its
// keys or line are written only while the link stands, held as the
// session's writer takes them (holdLink, rule 1). An answer another answer
// came before is kept and noted in the link's Recent as the losing attempt
// it is, so its steps read "another answer first".
func (s *Server) laneAnswer(ctx context.Context, linkID, sessionID string, sc laneScope, ref brakeman.Ref, it *inboxItem, m proto.Decide, own func(context.Context, proto.Answer) error, stands func() error) ([]byte, *laneOutcome) {
	if m.Revision != ref.Question || m.Choice != nil || m.Rationale != "" || (m.Input == "") == (m.Text == "") {
		return refusal(m.ReqID, proto.DecideInvalid, "an answer names its question's id as its revision and carries a choice's keys or a line"), nil
	}
	lost := func(words string) ([]byte, *laneOutcome) {
		s.note(linkID, ref.String(), ref.Question, m.OpID)
		o := &laneOutcome{Ref: ref.String(), Receipt: ref.Question, Code: proto.DecideAnswered, Words: words}
		return proto.MustControl(proto.DecideRefused{T: proto.CtlDecideRefused, ReqID: m.ReqID, Code: o.Code, Message: fitText(words, proto.MaxDecideWords), Ref: o.Ref, Receipt: o.Receipt}), o
	}
	offered := false
	for _, a := range it.Actions {
		if a.Name == "answer" && a.Expect == ref.Question && ((m.Input != "" && a.Input == m.Input) || (m.Text != "" && a.Text)) {
			offered = true
		}
	}
	if !offered {
		if it.Revision != ref.Question || !slices.ContainsFunc(it.Actions, func(a inboxAction) bool { return a.Name == "answer" }) {
			return lost("the question is no longer one an answer is taken for: nothing was typed")
		}
		return refusal(m.ReqID, proto.DecideInvalid, "not one of the question's choices: nothing was typed"), nil
	}
	d, ok := s.registry.Get(ref.Session)
	if !ok {
		return lost("the session is no longer listed: nothing was typed")
	}
	local, isLocal := d.(*session.Local)
	if !isLocal {
		return refusal(m.ReqID, proto.DecideUnsupported, "a hosted session's question is answered where it runs"), nil
	}
	s.beforeOperation(linkID, m.OpID)
	// Read again here to refuse early; what decides is the hold the
	// session's writer takes with the write (holdLink).
	if _, no := s.scope(linkID, m.ReqID); no != nil {
		return no, nil
	}
	if stands != nil {
		if err := stands(); err != nil {
			return linkRefusal(m.ReqID, err), nil
		}
	}
	if ctx.Err() != nil {
		return begunGone(m.ReqID), nil
	}
	reqID := answerReqID(linkID, m.OpID)
	var err error
	if ref.Session == sessionID && own != nil {
		// The connection's own subscription holds the link as its writes
		// are taken (AttachOptions.Hold).
		err = own(ctx, proto.Answer{T: proto.CtlAnswer, ReqID: reqID, Question: ref.Question, Input: m.Input, Text: m.Text})
	} else {
		opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), submitTimeout)
		defer cancel()
		name := linkAnswerName(sc.link.Label)
		hold := s.holdLinkWith(linkID, stands)
		if m.Input != "" {
			err = local.AnswerAs(opCtx, ref.Question, m.Input, session.Actor{Name: name, ReqID: reqID, Withdrawn: ctx.Done(), Hold: hold})
		} else {
			done := make(chan error, 1)
			_, err = local.Submit(opCtx, session.Submission{Text: m.Text, Question: ref.Question, ByName: name, ReqID: reqID, Withdrawn: ctx.Done(), Hold: hold,
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
		switch {
		case errors.Is(err, session.ErrQuestionAnswered):
			return lost("the question is no longer the one waiting: nothing was typed")
		case errors.Is(err, session.ErrNotAChoice):
			return refusal(m.ReqID, proto.DecideInvalid, err.Error()), nil
		}
		if c, w, ok := session.LinkRefusal(err); ok {
			return refusal(m.ReqID, c, w), nil
		}
		return refusal(m.ReqID, "input_failed", "the answer could not be delivered: "+err.Error()), nil
	}
	s.note(linkID, ref.String(), ref.Question, m.OpID)
	return laneOK(m.ReqID, ref.String(), ref.Question, false), &laneOutcome{Ref: ref.String(), Receipt: ref.Question}
}

// laneCommand takes a task command the item offers now, at the revision
// the decide carries, as the link: the engine judges it (stale_revision
// for a run that moved on). One the item no longer offers is refused,
// nothing done. The link is held from the check that it stands until the
// engine has returned (rule 1), and asked once more at the command's
// commit boundary (crew.Command.Stands): a revoke or an expiry waits for a
// command past it, up to the store's HoldWait, and one whose link ended
// while it was held commits nothing (refused revoked or expired; once the
// engine had taken it, its receipt settled withdrawn). A change the ledger
// refuses is put back and its receipt settles withdrawn: it never lands
// later through another transaction. A command whose connection closed
// before it began is never begun. Once the engine has taken the command,
// the operation is the link's whatever its outcome (an error after the
// taking included): its opId stays bound to this request (rule 2), and a
// repeat reads its receipt.
func (s *Server) laneCommand(ctx context.Context, linkID string, sc laneScope, ref brakeman.Ref, it *inboxItem, m proto.Decide, stands func() error) ([]byte, *laneOutcome) {
	if m.Input != "" || m.Text != "" || m.Choice != nil || m.Rationale != "" {
		return refusal(m.ReqID, proto.DecideInvalid, "a task command carries its revision alone"), nil
	}
	rev, err := strconv.ParseInt(m.Revision, 10, 64)
	if err != nil || rev <= 0 {
		return refusal(m.ReqID, proto.DecideInvalid, "revision is the run's revision the action carried"), nil
	}
	run, task, ok := s.commandTarget(ref)
	if !ok || !sc.coversTask(run, task) {
		return refusal(m.ReqID, proto.DecideNotInScope, "this link does not cover the task"), nil
	}
	if !slices.ContainsFunc(it.Actions, func(a inboxAction) bool { return a.Name == m.Action }) {
		words := "the item no longer offers this: nothing was done"
		if it.Now != "" {
			words = "the item moved on (" + it.Now + "): nothing was done"
		}
		return refusal(m.ReqID, proto.DecideStaleRevision, words), nil
	}
	s.beforeOperation(linkID, m.OpID)
	held, err := s.links.Hold(linkID)
	if err != nil {
		return linkRefusal(m.ReqID, err), nil
	}
	defer held.Release()
	if stands != nil {
		if err := stands(); err != nil {
			return linkRefusal(m.ReqID, err), nil
		}
	}
	if ctx.Err() != nil {
		return begunGone(m.ReqID), nil
	}
	// Begun: the command is the engine's whatever becomes of the
	// connection; its receipt stands, and the same opId asks for it again.
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if seam := s.laneSeam; seam != nil && seam.commandCtx != nil {
		seam.commandCtx(m.OpID, cancel)
	}
	var ended error
	cmd := crew.Command{ID: m.OpID, ExpectedRevision: rev, Stands: func() error {
		if err := held.Stands(); err != nil {
			ended = err
			return err
		}
		// The connection's own check (a publisher's peer: its link held
		// there and its switchyard heard), at the same boundary.
		if stands != nil {
			if err := stands(); err != nil {
				ended = err
				return err
			}
		}
		return nil
	}}
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
		kept := &laneOutcome{Ref: target, Receipt: rc.CommandID, Run: run}
		if ended != nil && rc.Refusal == crew.RefusalWithdrawn {
			// Taken, and settled withdrawn at its commit: nothing changed.
			s.note(linkID, target, rc.CommandID, m.OpID)
			f := linkRefusal(m.ReqID, ended)
			return f, kept
		}
		if !rc.Repeat {
			s.runs.Note(run, session.ActivityLink, laneActorNote(held.Link, m.Action, task))
		}
		s.note(linkID, target, rc.CommandID, m.OpID)
		return commandReply(m.ReqID, target, rc, rc.Repeat), kept
	}
	if ended != nil {
		// Refused before the engine took it: nothing begun.
		return linkRefusal(m.ReqID, ended), nil
	}
	code, words := "unavailable", "the command could not be taken"
	switch {
	case errors.Is(err, crew.ErrCommandConflict):
		code, words = proto.DecideOpConflict, "this opId was used before for another request: nothing was done"
	case errors.Is(err, crew.ErrStaleRevision):
		code, words = proto.DecideStaleRevision, "the run moved on since the revision you read: nothing was done"
	case errors.Is(err, crew.ErrRunStopped), errors.Is(err, crew.ErrRunContinued), errors.Is(err, crew.ErrTaskState), errors.Is(err, crew.ErrRunNotFound), errors.Is(err, crew.ErrTaskNotFound):
		code, words = proto.DecideStaleRevision, "the item moved on: "+err.Error()
	case errors.Is(err, crew.ErrTooManyCommands):
		code, words = proto.DecideTooMany, err.Error()
	}
	if code != proto.DecideOpConflict {
		if f, kept := s.takenAnyway(ctx, linkID, run, task, target, m); kept != nil {
			return f, kept
		}
	}
	return refusal(m.ReqID, code, words), nil
}

// takenAnyway is the outcome of a command the engine answered with an
// error and no receipt, when the engine had taken it all the same (its
// change's commit refused after the taking, the change put back and the
// run paused for it): the ledger keeps a row under the link's actor for
// the opId, for this task's command, so the operation is the link's (rule
// 2), its opId bound to this request, and its receipt says what became of
// it (withdrawn, once the ledger records it). A ledger that cannot be read
// keeps it the link's as well, fail-closed: a later request under the
// opId is op_conflict, never a fresh command. nil when the engine took
// nothing (no row), or a row of another command holds the opId.
func (s *Server) takenAnyway(ctx context.Context, linkID, run, task, target string, m proto.Decide) ([]byte, *laneOutcome) {
	kept := &laneOutcome{Ref: target, Receipt: m.OpID, Run: run}
	if s.ledger == nil {
		return nil, nil
	}
	// Read on a context of its own: the command's may be what failed it.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	row, found, err := s.ledger.Receipt(rctx, run, laneActor(linkID), m.OpID)
	switch {
	case err != nil:
		return proto.MustControl(proto.DecideRefused{T: proto.CtlDecideRefused, ReqID: m.ReqID, Code: "unavailable",
			Message: "whether the engine took the command could not be read: this opId stays this request's", Ref: target, Receipt: m.OpID}), kept
	case !found || row.Intent != m.Action || !crew.TaskCommandOn(row, nil, task):
		return nil, nil
	}
	s.note(linkID, target, m.OpID, m.OpID)
	return proto.MustControl(proto.DecideRefused{T: proto.CtlDecideRefused, ReqID: m.ReqID, Code: "unavailable",
		Message: "the engine took the command and could not record its change: it was withdrawn, as its receipt says once the ledger records it", Ref: target, Receipt: m.OpID}), kept
}

// laneHash is what the link's viewer sees, hashed: its scope's open items
// as summarized, their actions' revisions, every DR1 record's state and
// mark in the scope whatever its state (of the read its items are of:
// lane_decisions.go), and the steps of each entry of the link's Recent;
// "" for a link that no longer stands. ok is false for a scope that could
// not be read, whose hash says nothing.
func (s *Server) laneHash(ctx context.Context, linkID string) (hash string, ok bool) {
	if seam := s.laneSeam; seam != nil && seam.hashing != nil {
		seam.hashing(linkID)
	}
	sc, no := s.scope(linkID, "")
	if no != nil {
		return "", true
	}
	items, marks, _, err := s.laneItems(ctx, sc)
	if err != nil {
		s.log.Warn("lane: the scope's items could not be read for its watch", "err", err)
		return "", false
	}
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, it := range items {
		_ = enc.Encode(it.summary())
		for _, a := range it.Actions {
			_ = enc.Encode([]string{a.Name, a.Expect, a.Input})
		}
	}
	for _, m := range marks {
		_ = enc.Encode(m)
	}
	for _, r := range s.links.RecentOf(linkID) {
		if ref, err := brakeman.ParseRef(r.Ref); err == nil {
			st, _, _ := s.steps(ctx, sc, ref, r.Receipt, r.Attempt)
			_ = enc.Encode(st)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// laneSettle is how long a watch collects the events that follow one
// before it reads the scope again: a burst costs a read or two.
const laneSettle = 100 * time.Millisecond

// Watch calls changed whenever the hash of what the link's viewer sees
// differs from the last it saw, read again on each of the server's events
// (a session's, a run's, Dispatch's) and on each action noted in the
// link's Recent, until stop. A link's connections share one watch, which
// reads the scope once per settle for all of them (laneState.links), and
// its first read is made before Watch returns, so a request the
// connection serves after it is never the baseline of a change it misses.
func (l *Lane) Watch(linkID, sessionID string, changed func()) (stop func()) {
	s := l.s
	wake := make(chan struct{}, 1)
	told := make(chan struct{})
	go func() {
		defer close(told)
		for range wake {
			changed()
		}
	}()
	if lw, first := s.lanes.join(linkID, wake); first {
		s.startWatch(linkID, lw)
	} else {
		<-lw.ready
	}
	return func() {
		if lw := s.lanes.leave(linkID, wake); lw != nil {
			lw.stop()
			<-lw.done
		}
		close(wake)
		<-told
	}
}

// startWatch makes lw's first read, subscribed to the server's events
// first, and then watches on a goroutine of its own until lw.stop.
func (s *Server) startWatch(linkID string, lw *linkWatch) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := s.events.subscribe()
	// A first read that failed is no baseline: the first that reads is
	// told as a change.
	last, ok := s.laneHash(ctx, linkID)
	if !ok {
		last = "unread"
	}
	s.lanes.mu.Lock()
	lw.stop = cancel
	s.lanes.mu.Unlock()
	close(lw.ready)
	go func() {
		defer close(lw.done)
		defer func() { s.events.unsubscribe(ch) }()
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
			case <-lw.poke:
				if !pending {
					pending = true
					settle.Reset(laneSettle)
				}
			case <-settle.C:
				pending = false
				// A read that failed keeps the last hash: the next event
				// reads again, and tells any change since that one.
				if h, ok := s.laneHash(ctx, linkID); ok && h != last {
					last = h
					s.lanes.fan(lw)
				}
			}
		}
	}()
}

// laneConn is one connection's side of the lane: the requests under way,
// at most proto.MaxDecideInFlight decides and
// proto.MaxDecisionReadsInFlight reads, each on a goroutine of its own,
// its reply sent as it is done; nil for a connection of no decision link,
// which is refused each request (not_decision_link). Its context ends
// with the connection, and first of all as end begins, on every way out.
type laneConn struct {
	lane              *Lane
	ctx               context.Context
	cancel            context.CancelFunc
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
	ctx, cancel := context.WithCancel(ctx)
	c := &laneConn{lane: s.Lane(), ctx: ctx, cancel: cancel, linkID: linkID, sessionID: sessionID, send: send, answer: answer}
	c.stopWatch = c.lane.Watch(linkID, sessionID, func() {
		send(proto.MustControl(proto.DecisionsChanged{T: proto.CtlDecisionsChanged, Seq: c.seq.Add(1)}))
	})
	return c
}

// end cancels the requests under way, stops the watch and waits for them:
// one not begun is never begun; one begun ends as its operation does.
func (c *laneConn) end() {
	if c == nil {
		return
	}
	c.cancel()
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
		if seam := c.lane.s.laneSeam; seam != nil && seam.beforeRead != nil {
			seam.beforeRead(reqID)
		}
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
		f := c.lane.Decide(c.ctx, c.linkID, c.sessionID, m, c.answer, nil)
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
		// The lane's context, which ends with the connection: an answer
		// queued and not yet begun is withdrawn as it ends (Server.answer).
		err := s.answer(ctx, sub, local, a, func(e error) { done <- e })
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
// forgetting closes; and the watch of each link, shared by its
// connections (Lane.Watch).
type laneState struct {
	mu    sync.Mutex
	conns map[string]map[*wsSink]struct{}
	links map[string]*linkWatch
	// shut is set as the server's shutdown closes every desk (shutDesks),
	// before it lists them: no desk is registered after (add).
	shut bool
}

// linkWatch is one link's watch: the connections it tells (their wakes,
// each told without waiting, a connection's own goroutine sending its
// decisions_changed), poked when the link's Recent changes. ready is
// closed once its first read is made; stop and done end it once its last
// connection leaves.
type linkWatch struct {
	subs  map[chan struct{}]struct{}
	poke  chan struct{}
	ready chan struct{}
	stop  context.CancelFunc
	done  chan struct{}
}

// join adds wake to linkID's watch: first is true when the watch is new,
// and its caller starts it (startWatch).
func (d *laneState) join(linkID string, wake chan struct{}) (*linkWatch, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.links == nil {
		d.links = map[string]*linkWatch{}
	}
	lw, ok := d.links[linkID]
	if !ok {
		lw = &linkWatch{subs: map[chan struct{}]struct{}{}, poke: make(chan struct{}, 1), ready: make(chan struct{}), done: make(chan struct{})}
		d.links[linkID] = lw
	}
	lw.subs[wake] = struct{}{}
	return lw, !ok
}

// leave takes wake out of linkID's watch: the watch, when wake was its
// last, for its caller to stop; nil otherwise. Nothing tells wake after.
func (d *laneState) leave(linkID string, wake chan struct{}) *linkWatch {
	d.mu.Lock()
	defer d.mu.Unlock()
	lw := d.links[linkID]
	if lw == nil {
		return nil
	}
	delete(lw.subs, wake)
	if len(lw.subs) > 0 {
		return nil
	}
	delete(d.links, linkID)
	return lw
}

// fan tells each connection of lw, none waited for.
func (d *laneState) fan(lw *linkWatch) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for wake := range lw.subs {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// poke wakes linkID's watch, not waited for.
func (d *laneState) poke(linkID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if lw := d.links[linkID]; lw != nil {
		select {
		case lw.poke <- struct{}{}:
		default:
		}
	}
}

// maxDesks bounds the desks one link holds open at once, as a session
// bounds its viewers.
const maxDesks = 8

// errTooManyDesks refuses a desk past maxDesks.
var errTooManyDesks = errors.New("too many desks for this link")

// add holds sink as one of linkID's desks: errTooManyDesks past maxDesks,
// errShuttingDown once the server's shutdown has closed the desks.
func (d *laneState) add(linkID string, sink *wsSink) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.shut {
		return errShuttingDown
	}
	if len(d.conns[linkID]) >= maxDesks {
		return errTooManyDesks
	}
	if d.conns == nil {
		d.conns = map[string]map[*wsSink]struct{}{}
	}
	if d.conns[linkID] == nil {
		d.conns[linkID] = map[*wsSink]struct{}{}
	}
	d.conns[linkID][sink] = struct{}{}
	return nil
}

func (d *laneState) remove(linkID string, sink *wsSink) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.conns[linkID], sink)
	if len(d.conns[linkID]) == 0 {
		delete(d.conns, linkID)
	}
}

// shutDesks closes every desk as the server shuts down: admission closed
// first, under the lock add takes, so a desk is either among those closed
// or refused as it registers.
func (s *Server) shutDesks() {
	s.lanes.mu.Lock()
	s.lanes.shut = true
	s.lanes.mu.Unlock()
	s.closeDesks(errShuttingDown)
}

// armLapse arms fire at at, a desk's or a decision viewer's link's
// deadline (handed to the lapse seam in tests); its stop disarms it.
func (s *Server) armLapse(linkID string, at time.Time, fire func()) (stop func()) {
	t := time.AfterFunc(time.Until(at), fire)
	if seam := s.laneSeam; seam != nil && seam.lapse != nil {
		seam.lapse(linkID, fire)
	}
	return func() { t.Stop() }
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
	// Each one's cut armed at once (closeWait), so a peer that reads
	// nothing holds none of them past it, and all closed together: the
	// whole takes one closeWait at most, whatever their number.
	var wg sync.WaitGroup
	for i, c := range todo {
		c.Closing()
		wg.Go(func() { c.Close(reasons[i]) })
	}
	wg.Wait()
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
	// the desk; the server's shutdown closes admission and then its desks,
	// so either this desk is among them or it is refused here.
	if seam := s.laneSeam; seam != nil && seam.beforeDeskAdd != nil {
		seam.beforeDeskAdd(linkID)
	}
	if err := s.lanes.add(linkID, sink); err != nil {
		if errors.Is(err, errShuttingDown) {
			sink.Close(errShuttingDown)
			return
		}
		c.Close(proto.CloseTooManyViewers, "too many desks for this link")
		return
	}
	defer s.lanes.remove(linkID, sink)
	defer sink.cancel()
	sc, no := s.scope(linkID, "")
	if no != nil {
		s.closeDesks(nil, linkID)
		return
	}
	// Closed at the link's expiry, not at the next sweep: the expiry made
	// then, through the link's barrier as a revoke is (share.Store.Expire:
	// nothing begun for the link after its mark, the acts begun before
	// waited for up to HoldWait, rule 1), and the desk closed after it.
	if at := sc.link.ExpiresAt; at != nil {
		defer s.armLapse(linkID, *at, func() {
			if s.links.Expire(linkID) {
				sink.Close(session.ErrExpired)
			}
		})()
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
