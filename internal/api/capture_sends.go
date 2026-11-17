package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A capture's sends over HTTP (Program item PV1, docs/plans/browser-
// evidence.md, PR-6): a capture at the revision the person sees sent to a
// ticket (a new one or one by its key) or to Brakeman, a send's cancel,
// and a send read with the document it froze. Every write is the admin's;
// the read is the admin's and a Brakeman turn's shared read
// (brakemanReads), held to the turn's grant.
//
// A send to a ticket is the store's one step (dispatch.SendCapture):
// sent, or refused. A send to Brakeman is two: the send's input, with the
// request the desk is to be asked (the person's question, and the
// attempt's ref when the capture is of one), is committed pending first;
// then the desk is asked under the send's id as its op id, pinned
// (brakeman.Request.Pin), its subjects the send's own frozen document
// (capture:<id>/<send>) before the stored ones; then sent with the
// request's id (MarkSent), then the pin let go. Every step that can reach
// the desk for a send runs under the send's gate (sendGates) and reads the
// send's state and cancels again there. What a step did not finish (a
// stop between two, an answer of the desk that passes by itself) is
// finished by the settlement (settleCaptureSends), from the stored
// request and never from the capture as it is now, once the desk has
// recovered and every minute after.
//
// What a sent send of a deleted capture keeps (the settlement's word,
// sends.go): everything it held, the document it froze and the
// screenshot it carried, for as long as the store keeps the send. A
// frozen send is read the same after its capture's edit or delete, and a
// repair's evidence is read from it (#798); the settlement releases
// nothing a sent send holds.

const (
	// maxCaptureSendBody bounds a send's body, the store's bound of the
	// request it may keep.
	maxCaptureSendBody = dispatch.MaxSendRequest
	// maxSendQuestion bounds a question to Brakeman, in characters.
	maxSendQuestion = 4000
	// maxSendWaiting bounds the desk's words a pending send waits on.
	maxSendWaiting = 300
	// captureSendsEvery is how often the settlement looks again.
	captureSendsEvery = time.Minute
	// captureSendRead is the frozen send's read, a shared read.
	captureSendRead = "GET /api/captures/{id}/sends/{send}"
)

// routeCaptureSends adds a capture's send routes, each the admin's and a
// switchyard's 403; the frozen send's read is a Brakeman turn's too.
func (s *Server) routeCaptureSends(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/captures/{id}/send", s.requireAdmin(s.launchingAny(s.handleCaptureSend)))
	mux.HandleFunc("POST /api/captures/{id}/sends/{send}/cancel", s.requireAdmin(s.launchingAny(s.handleCaptureSendCancel)))
	mux.HandleFunc(captureSendRead, s.readable(captureSendRead, s.launchingAny(s.handleCaptureSendRead)))
	s.routeCaptureDecisions(mux)
}

// The bodies a send route takes.
type (
	// captureSendTo is where a send goes: a new ticket (project, title),
	// an existing one (key), Brakeman (question), or a decision record
	// (decision, its dr-<n>).
	captureSendTo struct {
		Kind     string `json:"kind"`
		Project  string `json:"project,omitempty"`
		Title    string `json:"title,omitempty"`
		Key      string `json:"key,omitempty"`
		Question string `json:"question,omitempty"`
		Decision string `json:"decision,omitempty"`
		// A send to a review's (capture_review.go): Request a change and
		// Add to a review.
		Candidate   string `json:"candidate,omitempty"`
		Verdict     string `json:"verdict,omitempty"`
		Ks          []int  `json:"ks,omitempty"`
		Draft       bool   `json:"draft,omitempty"`
		Severity    string `json:"severity,omitempty"`
		Summary     string `json:"summary,omitempty"`
		ContinueRun bool   `json:"continueRun,omitempty"`
	}
	captureSendBody struct {
		OpID   string        `json:"opId"`
		ByName string        `json:"byName,omitempty"`
		Rev    int64         `json:"rev"`
		To     captureSendTo `json:"to"`
	}
	captureCancelBody struct {
		OpID   string `json:"opId"`
		ByName string `json:"byName,omitempty"`
	}
)

// sendRequest is a send to Brakeman's request as the send keeps it
// (dispatch.SendFacts.Request) and the desk is asked it, whole, by every
// try: an ask, the subjects after the send's own frozen document (which
// names the send's id, and goes first), the person's question as its
// note, and their display name.
type sendRequest struct {
	Kind     string         `json:"kind"`
	Subjects []brakeman.Ref `json:"subjects"`
	Note     string         `json:"note"`
	By       string         `json:"by,omitempty"`
}

// sendReply is a send as a send route answers it: the store's, with the
// desk's words a pending one waits on (memory alone, the last try's).
type sendReply struct {
	dispatch.CaptureSend
	Waiting string `json:"waiting,omitempty"`
}

// captureSends is the server's part of the sends: the gates, the desk's
// words each pending send waits on, the settlement's loop and a test's
// seam.
type captureSends struct {
	gates sendGates
	mu    sync.Mutex
	// waiting is the desk's words of the last try of each pending send.
	waiting map[string]string
	wake    chan struct{}
	stop    context.CancelFunc
	done    chan struct{}
	once    sync.Once
	// seam, when a test sets it, runs at each step of a send: "try" under
	// the gate before the desk is asked, "admitted" once it admitted and
	// before the send is sent, "intent" once a cancel is committed and
	// before its gate is taken, "cancel" under the gate before the
	// cancel's steps, "desk_cancel" before the request is cancelled at the
	// desk. An error ends the step there, as a stop would.
	seam atomic.Pointer[func(stage, send string) error]
}

// at runs the test's seam at stage for the send.
func (c *captureSends) at(stage, send string) error {
	if f := c.seam.Load(); f != nil {
		return (*f)(stage, send)
	}
	return nil
}

// wait keeps the desk's words a pending send waits on.
func (c *captureSends) wait(send, words string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waiting == nil {
		c.waiting = map[string]string{}
	}
	c.waiting[send] = cutBytes(words, maxSendWaiting)
}

// settled forgets the words of a send no longer pending.
func (c *captureSends) settled(send string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.waiting, send)
}

// waitingOf is the words a pending send waits on, "" for none.
func (c *captureSends) waitingOf(sv dispatch.CaptureSend) string {
	if sv.State != dispatch.SendPending {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waiting[sv.ID]
}

// sendGates is a lock per send id: taken before a step reads the send's
// state, let go once its outcome is committed.
type sendGates struct {
	mu   sync.Mutex
	held map[string]*sendGate
}

type sendGate struct {
	ch   chan struct{}
	refs int
}

// lock takes the send's gate, waiting as long as ctx allows; the func it
// gives lets it go.
func (g *sendGates) lock(ctx context.Context, id string) (func(), error) {
	g.mu.Lock()
	if g.held == nil {
		g.held = map[string]*sendGate{}
	}
	sg := g.held[id]
	if sg == nil {
		sg = &sendGate{ch: make(chan struct{}, 1)}
		g.held[id] = sg
	}
	sg.refs++
	g.mu.Unlock()
	select {
	case sg.ch <- struct{}{}:
	case <-ctx.Done():
		g.drop(id, sg)
		return nil, ctx.Err()
	}
	return func() {
		<-sg.ch
		g.drop(id, sg)
	}, nil
}

func (g *sendGates) drop(id string, sg *sendGate) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if sg.refs--; sg.refs == 0 {
		delete(g.held, id)
	}
}

// queued is how many steps hold or wait for the send's gate.
func (g *sendGates) queued(id string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if sg := g.held[id]; sg != nil {
		return sg.refs
	}
	return 0
}

// readCaptureSend is a send's gate: one JSON object of at most
// maxCaptureSendBody, its question to Brakeman 1 to maxSendQuestion
// characters and none for another kind, and the digest of what was
// decoded but its operation, taken before anything is scrubbed.
func readCaptureSend(w http.ResponseWriter, r *http.Request) (captureRequest[captureSendBody], *apiError) {
	var req captureRequest[captureSendBody]
	b, aerr := readJSONBody(w, r, maxCaptureSendBody)
	if aerr != nil && aerr.Code == "bound" {
		aerr = newAPIError(http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("a send is at most %d KiB", maxCaptureSendBody>>10))
	}
	if aerr == nil {
		aerr = decodeCapture(b, &req.body)
	}
	if aerr == nil {
		req.op, aerr = opOf(r, req.body.OpID, req.body.ByName)
	}
	if aerr != nil {
		return req, aerr
	}
	to := req.body.To
	switch {
	case req.body.Rev < 1:
		return req, badCapture("rev is the capture's revision you send")
	case to.Kind == dispatch.SendBrakeman && (to.Question == "" || utf8.RuneCountInString(to.Question) > maxSendQuestion):
		return req, badCapture(fmt.Sprintf("a send to Brakeman asks a question of 1 to %d characters", maxSendQuestion))
	case to.Kind != dispatch.SendBrakeman && to.Question != "":
		return req, badCapture("only a send to Brakeman asks a question")
	case to.Kind != dispatch.SendReview && (to.Candidate != "" || to.Verdict != "" || len(to.Ks) > 0 || to.Draft || to.Severity != "" || to.Summary != "" || to.ContinueRun):
		return req, badCapture("only a send to a review names a candidate, annotations, a draft, a verdict, a severity, a summary or the word to continue")
	case to.Kind == dispatch.SendReview && utf8.RuneCountInString(to.Summary) > dispatch.MaxReviewSummary:
		return req, badCapture(fmt.Sprintf("a summary is at most %d characters", dispatch.MaxReviewSummary))
	}
	content := req.body
	content.OpID, content.ByName = "", ""
	var err error
	if req.digest, err = dispatch.RequestDigest(content, ""); err != nil {
		return req, badCapture("the send could not be encoded")
	}
	return req, nil
}

// handleCaptureSend sends a capture at rev (capture.send): the gate, the
// body scrubbed of every tree's secret held now, a resend answered by its
// receipt, the destination made (a Brakeman send's request from the
// capture's attempt and the question), and the store's send. A Brakeman
// send is then tried at the desk (trySend). 201 {receipt, send} for a
// ticket's, and a Brakeman send the desk admitted; 202 for a Brakeman send
// left pending, send.waiting the desk's words; 200 for a repeat; the
// store's refusal as answerCapture answers it; the desk's refusal of a
// first try (409 brakeman_off, 429 queue_full, 429 budget_spent) with
// {error, receipt, send}, the send refused with the desk's words.
func (s *Server) handleCaptureSend(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	id, ok := captureID(w, r)
	if !ok {
		return
	}
	req, aerr := readCaptureSend(w, r)
	secrets := s.liveSecrets()
	if aerr == nil {
		aerr = keptAsSent(secrets, req.body.OpID, id)
	}
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	scrubSecrets(&req.body, secrets)
	req.op.By = req.body.ByName
	ctx, cancel := opContext(r)
	defer cancel()
	rc, found, err := ds.Replay(ctx, req.op, dispatch.IntentCaptureSend, captureArgs{ID: id, Received: req.digest})
	if err == nil && req.body.To.Kind == dispatch.SendReview {
		s.captureSendToReview(w, ctx, ds, req.op, req.digest, id, req.body, rc, found)
		return
	}
	if err == nil && !found {
		to := req.body.To
		dest := dispatch.Destination{Kind: to.Kind, Project: to.Project, Title: to.Title, Key: to.Key, Decision: to.Decision}
		var facts dispatch.SendFacts
		switch to.Kind {
		case dispatch.SendBrakeman:
			if facts.Request, err = s.sendRequestOf(ctx, ds, id, to.Question, req.op.By); err != nil {
				s.dispatchError(w, "capture", err)
				return
			}
		case dispatch.SendDecision:
			facts.Request = decisionRequestOf(to.Decision, id, req.body.Rev)
		}
		rc, err = ds.SendCapture(ctx, req.op, req.digest, id, req.body.Rev, dest, facts)
	}
	if err != nil || rc.Status != dispatch.StatusDone {
		s.answerCapture(w, ctx, ds, rc, err, true, "-")
		return
	}
	sv, err := ds.CaptureSend(ctx, rc.ID)
	if err != nil {
		s.dispatchError(w, "send", err)
		return
	}
	var refusal *apiError
	if sv.State == dispatch.SendPending && (sv.Kind == dispatch.SendBrakeman || sv.Kind == dispatch.SendDecision) {
		try := s.trySend
		if sv.Kind == dispatch.SendDecision {
			try = s.tryDecision
		}
		if refusal, err = try(ctx, sv.ID, !rc.Repeat); err != nil {
			s.log.Warn("a capture's send is kept pending: it is settled later", "send", sv.ID, "kind", sv.Kind, "err", err)
		}
		if again, err := ds.CaptureSend(ctx, rc.ID); err == nil {
			sv = again
		}
	}
	reply := map[string]any{"receipt": rc, "send": s.sendReplyOf(sv)}
	status := http.StatusOK
	switch {
	case refusal != nil:
		reply["error"] = refusal
		status = refusal.status
	case rc.Repeat:
	case sv.State == dispatch.SendPending:
		status = http.StatusAccepted
	default:
		status = http.StatusCreated
	}
	writeDispatch(w, status, reply)
}

// sendReplyOf is sv as a send route answers it.
func (s *Server) sendReplyOf(sv dispatch.CaptureSend) sendReply {
	return sendReply{CaptureSend: sv, Waiting: s.sends.waitingOf(sv)}
}

// sendRequestOf is the request a send to Brakeman keeps and the desk is
// asked: the question (scrubbed already) as its note, the capture's
// attempt's ref when it is of one, and the display name when Brakeman's
// bound takes it. A capture not found gives none: the store refuses the
// send not_found.
func (s *Server) sendRequestOf(ctx context.Context, ds *dispatch.Store, id, question, by string) (json.RawMessage, error) {
	req := sendRequest{Kind: brakeman.KindAsk, Subjects: []brakeman.Ref{}, Note: question}
	if len(by) <= brakeman.MaxBy && !strings.ContainsAny(by, "\n\t") {
		req.By = by
	}
	of, err := ds.CaptureOf(ctx, id)
	switch {
	case errors.Is(err, dispatch.ErrNotFound):
	case err != nil:
		return nil, err
	case of.Kind == dispatch.OriginAttempt:
		if a := (brakeman.Ref{Kind: brakeman.RefAttempt, Run: of.Run, Task: of.Task, N: of.Attempt}); a.Valid() == nil {
			req.Subjects = append(req.Subjects, a)
		}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimSpace(b.Bytes())), nil
}

// askOf is the desk's request of a pending send to Brakeman, from its
// stored request and its own id alone: never from the capture as it is
// now.
func askOf(sv dispatch.CaptureSend) (brakeman.Request, error) {
	var req sendRequest
	dec := json.NewDecoder(bytes.NewReader(sv.Request))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Kind != brakeman.KindAsk {
		return brakeman.Request{}, errors.New("the send's stored request is not an ask")
	}
	frozen := brakeman.Ref{Kind: brakeman.RefCapture, Capture: sv.Capture, Send: sv.ID}
	return brakeman.Request{OpID: sv.ID, Kind: req.Kind, Subjects: append([]brakeman.Ref{frozen}, req.Subjects...), Note: req.Note, By: req.By, Pin: true}, nil
}

// sendOp is the operation of a step the server makes on a send, under
// the send's own actor and an id derived from the send's and the step,
// so the same step again is answered by its receipt.
func sendOp(sv dispatch.CaptureSend, step string) dispatch.Op {
	return dispatch.Op{Actor: sv.Actor, ID: "send." + sv.ID + "." + step}
}

// permanentRefusal reports a desk's answer that no later try changes,
// which refuses the send: Brakeman off, and on the route's first try
// the queue full or the day's budget spent (the person sees it and sends
// again).
func permanentRefusal(aerr *apiError, first bool) bool {
	switch aerr.Code {
	case "brakeman_off":
		return true
	case "queue_full", "budget_spent":
		return first
	}
	return false
}

// deskWords is the words of a desk's error a pending send waits on.
func deskWords(err error) string {
	switch {
	case errors.Is(err, brakeman.ErrNotOwner):
		return "Brakeman's records are not this server's yet"
	case errors.Is(err, brakeman.ErrPinsFull):
		return fmt.Sprintf("Brakeman holds %d requests that wait for their sends already", brakeman.MaxPinnedOps)
	case errors.Is(err, errDeskClosing):
		return "the server is shutting down"
	}
	return "Brakeman's records could not be read or written"
}

// trySend asks the desk for the pending send to Brakeman with the id,
// under its gate: nothing when the send is no longer pending or a cancel
// of it is asked (the cancel's steps finish it). The desk's own record
// first: a request it holds under the send's id makes the send sent to
// it, one it pruned sent to its number, and only an op id the desk has
// never seen is asked, from the stored request (askOf). Admitted, the send
// is sent and its pin let go; Brakeman off, and on the route's first try
// (first) the queue full or the budget spent, refused with the desk's
// words, which it answers; any other answer keeps it pending with them.
// An error is a step not finished (a store's, or a test's stop).
func (s *Server) trySend(ctx context.Context, id string, first bool) (*apiError, error) {
	unlock, err := s.sends.gates.lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	ds := s.dispatch
	sv, err := ds.CaptureSend(ctx, id)
	if err != nil {
		return nil, err
	}
	if sv.Kind != dispatch.SendBrakeman || sv.State != dispatch.SendPending || len(sv.Cancels) > 0 {
		if sv.State != dispatch.SendPending {
			s.sends.settled(id)
		}
		return nil, nil
	}
	if err := s.sends.at("try", id); err != nil {
		return nil, err
	}
	d := s.brakeman
	switch {
	case d == nil:
		s.sends.wait(id, "Brakeman needs a data directory")
		return nil, nil
	case d.recovering.Load():
		s.sends.wait(id, "Brakeman's records are being recovered")
		return nil, nil
	}
	held, found, err := d.st.ByOp(id)
	switch {
	case errors.Is(err, brakeman.ErrPruned):
		return nil, s.sendSent(ctx, d, sv, held.ID)
	case err != nil:
		s.sends.wait(id, deskWords(err))
		return nil, nil
	case found:
		return nil, s.sendSent(ctx, d, sv, held.ID)
	}
	in, err := askOf(sv)
	if err != nil {
		return nil, s.sendRefused(ctx, sv, err.Error())
	}
	req, _, aerr, err := s.askBrakeman(d, in)
	var invalid *brakeman.Invalid
	switch {
	case aerr == nil && err == nil:
		return nil, s.sendSent(ctx, d, sv, req.ID)
	case aerr != nil && permanentRefusal(aerr, first):
		return aerr, s.sendRefused(ctx, sv, aerr.Message)
	case aerr != nil:
		s.sends.wait(id, aerr.Message)
	case errors.As(err, &invalid), errors.Is(err, brakeman.ErrOpConflict), errors.Is(err, brakeman.ErrPruned):
		// What the desk never takes, however often it is asked.
		return nil, s.sendRefused(ctx, sv, "Brakeman refused the request: "+strings.TrimPrefix(err.Error(), "brakeman: "))
	default:
		s.sends.wait(id, deskWords(err))
	}
	return nil, nil
}

// sendSent records the pending send sv as sent to the desk's request
// target and lets its pin go: a pin a stop kept is let go by the next
// settlement's pass.
func (s *Server) sendSent(ctx context.Context, d *brakemanDesk, sv dispatch.CaptureSend, target string) error {
	if err := s.sends.at("admitted", sv.ID); err != nil {
		return err
	}
	rc, err := s.dispatch.MarkSent(ctx, sendOp(sv, "sent"), sv.ID, target)
	if err != nil {
		return err
	}
	if rc.Status != dispatch.StatusDone && rc.Refusal != dispatch.RefusalStaleRevision {
		return fmt.Errorf("the send could not be recorded sent: %s", rc.Error)
	}
	s.sends.settled(sv.ID)
	if err := d.st.Release(sv.ID); err != nil {
		s.log.Warn("a capture's send's request stays pinned at Brakeman: the next settlement lets it go", "send", sv.ID, "err", err)
	}
	return nil
}

// sendRefused records the pending send sv as refused with the desk's
// words.
func (s *Server) sendRefused(ctx context.Context, sv dispatch.CaptureSend, why string) error {
	rc, err := s.dispatch.MarkRefused(ctx, sendOp(sv, "refused"), sv.ID, why)
	if err != nil {
		return err
	}
	if rc.Status != dispatch.StatusDone && rc.Refusal != dispatch.RefusalStaleRevision {
		return fmt.Errorf("the send could not be recorded refused: %s", rc.Error)
	}
	s.sends.settled(sv.ID)
	return nil
}

// cancelState is a cancel as its route answers it: asked, whether it is
// done (its send cancelled or refused, or its request closed or pruned at
// the desk, the close recorded), and words when the desk had answered
// before it.
type cancelState struct {
	Asked bool   `json:"asked"`
	Done  bool   `json:"done"`
	Words string `json:"words,omitempty"`
}

// finishedFirst is a cancel's words when the request had closed before it.
const finishedFirst = "the request had finished before the cancel reached it"

// finishCancel finishes the cancel asked of the send with the id, under
// its gate, the cancel route's step and the settlement's: a pending send
// whose op id the desk never saw is cancelled; one the desk admitted (a
// try whose sent was never committed) is sent with it ("admitted before
// the cancel") and goes on as a sent one; one it pruned is sent to its
// number, and done; a sent one has its request cancelled at the desk
// (cancelBrakeman), and the close is recorded (CloseCancel). Done false
// for a step the desk cannot take now (not this server's yet, the server
// stopping): the settlement finishes it. An error is a step not finished.
func (s *Server) finishCancel(ctx context.Context, id string) (cancelState, error) {
	st := cancelState{Asked: true}
	unlock, err := s.sends.gates.lock(ctx, id)
	if err != nil {
		return st, err
	}
	defer unlock()
	ds, d := s.dispatch, s.brakeman
	sv, err := ds.CaptureSend(ctx, id)
	switch {
	case err != nil:
		return st, err
	case len(sv.Cancels) == 0:
		return cancelState{}, nil
	case sv.State == dispatch.SendRefused, sv.State == dispatch.SendCancelled:
		s.sends.settled(id)
		st.Done = true
		return st, nil
	}
	if err := s.sends.at("cancel", id); err != nil {
		return st, err
	}
	if sv.State == dispatch.SendPending {
		var held brakeman.Request
		found, pruned := false, false
		if d != nil {
			held, found, err = d.st.ByOp(id)
			if pruned = errors.Is(err, brakeman.ErrPruned); err != nil && !pruned {
				return st, nil
			}
		}
		if !found && !pruned {
			rc, err := ds.CancelSend(ctx, sendOp(sv, "cancelled"), id)
			if err != nil {
				return st, err
			}
			if rc.Status != dispatch.StatusDone && rc.Refusal != dispatch.RefusalStaleRevision {
				return st, fmt.Errorf("the send could not be recorded cancelled: %s", rc.Error)
			}
			s.sends.settled(id)
			st.Done = true
			return st, nil
		}
		// Admitted before the cancel: sent with its request, which the
		// steps below cancel.
		if err := s.sendSent(ctx, d, sv, held.ID); err != nil {
			return st, err
		}
		if sv, err = ds.CaptureSend(ctx, id); err != nil {
			return st, err
		}
	}
	if d == nil || sv.State != dispatch.SendSent {
		return st, nil
	}
	if err := s.sends.at("desk_cancel", id); err != nil {
		return st, err
	}
	req, closed, err := d.cancelBrakeman(sv.Target)
	switch {
	case errors.Is(err, brakeman.ErrNotFound):
		// Pruned at the desk: closed long before.
		st.Words = finishedFirst
	case err != nil:
		return st, nil
	case !closed && req.State != brakeman.StateCancelled:
		st.Words = finishedFirst
	}
	if err := ds.CloseCancel(ctx, id); err != nil {
		var ref *dispatch.Refusal
		if !errors.As(err, &ref) {
			return st, err
		}
	}
	st.Done = true
	return st, nil
}

// handleCaptureSendCancel cancels a send to Brakeman (capture_send.cancel):
// the person's intent committed first (AskCancel, its receipt; a repeat
// answered by it), outside the gate, then its steps under the gate
// (finishCancel). 200 {receipt, send, cancel: {asked, done, words?}}; the
// store's refusal with {error, receipt, send}: 409 stale_revision for a
// send to a ticket (made in its one step) and a refused or cancelled one,
// its state named; 404 not_found for a send that is not the capture's.
func (s *Server) handleCaptureSendCancel(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	id, ok := captureID(w, r)
	if !ok {
		return
	}
	sendID := r.PathValue("send")
	if !dispatch.ValidID(sendID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "a send's id is 16 lower-case hex characters")
		return
	}
	var req captureRequest[captureCancelBody]
	b, aerr := readJSONBody(w, r, maxBody)
	if aerr == nil {
		aerr = decodeCapture(b, &req.body)
	}
	if aerr == nil {
		req.op, aerr = opOf(r, req.body.OpID, req.body.ByName)
	}
	secrets := s.liveSecrets()
	if aerr == nil {
		aerr = keptAsSent(secrets, req.body.OpID, id)
	}
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	scrubSecrets(&req.body, secrets)
	req.op.By = req.body.ByName
	ctx, cancel := opContext(r)
	defer cancel()
	sv, err := ds.CaptureSend(ctx, sendID)
	if err == nil && sv.Capture != id {
		err = dispatch.ErrNotFound
	}
	if err != nil {
		s.dispatchError(w, "send", err)
		return
	}
	if sv.Kind == dispatch.SendDecision {
		// A comment on a decision is DR1's to keep: once asked it is made,
		// or refused, and nothing of it runs on.
		writeError(w, http.StatusConflict, dispatch.RefusalStaleRevision, "a send to a decision is a comment: nothing of it is under way to cancel")
		return
	}
	rc, err := ds.AskCancel(ctx, req.op, sendID)
	if err != nil {
		s.dispatchError(w, "send", err)
		return
	}
	if rc.Status != dispatch.StatusDone {
		if rc.Refusal == dispatch.RefusalReadOnly {
			writeError(w, http.StatusServiceUnavailable, "dispatch_unavailable", "Dispatch is read-only: "+rc.Error)
			return
		}
		writeDispatch(w, refusalStatus(rc.Refusal), map[string]any{"error": apiError{Code: rc.Refusal, Message: rc.Error}, "receipt": rc, "send": s.sendReplyOf(sv)})
		return
	}
	st := cancelState{Asked: true}
	if err := s.sends.at("intent", sendID); err == nil {
		if st, err = s.finishCancel(ctx, sendID); err != nil {
			s.log.Warn("a capture's send's cancel is kept: the settlement finishes it", "send", sendID, "err", err)
			st = cancelState{Asked: true}
		}
	}
	if again, err := ds.CaptureSend(ctx, sendID); err == nil {
		sv = again
	}
	writeDispatch(w, http.StatusOK, map[string]any{"receipt": rc, "send": s.sendReplyOf(sv), "cancel": st})
}

// handleCaptureSendRead answers a send of the capture with the document it
// froze, read from the content file its digest names (the same after the
// capture's edit or delete): {send, document}. 404 not_found for a send
// that is not the capture's, and for a document no longer kept (a send
// that ended unsent after its capture's delete).
func (s *Server) handleCaptureSendRead(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	id, ok := captureID(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	sendID := r.PathValue("send")
	if !dispatch.ValidID(sendID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "a send's id is 16 lower-case hex characters")
		return
	}
	ctx := r.Context()
	sv, err := ds.CaptureSend(ctx, sendID)
	if err == nil && sv.Capture != id {
		err = dispatch.ErrNotFound
	}
	if err != nil {
		s.dispatchError(w, "send", err)
		return
	}
	doc, err := frozenDocument(ctx, ds, sv)
	if errors.Is(err, dispatch.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "the document this send froze is no longer kept")
		return
	}
	if err != nil {
		s.dispatchError(w, "send", err)
		return
	}
	writeDispatch(w, http.StatusOK, map[string]any{"send": s.sendReplyOf(sv), "document": doc})
}

// frozenDocument reads the document the send froze, whole and at its
// digest: an error for a file that is not.
func frozenDocument(ctx context.Context, ds *dispatch.Store, sv dispatch.CaptureSend) (json.RawMessage, error) {
	f, err := ds.OpenSendFile(ctx, sv.ID, sv.Digest)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, dispatch.MaxCaptureDocument+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if len(b) > dispatch.MaxCaptureDocument || hex.EncodeToString(sum[:]) != sv.Digest || !json.Valid(b) {
		return nil, fmt.Errorf("the document of send %s is not the one its digest names", sv.ID)
	}
	return json.RawMessage(b), nil
}

// startCaptureSends starts the settlement's loop: a pass at once, once the
// desk has recovered (pokeCaptureSends) and every captureSendsEvery,
// ended before the stores close (stopCaptureSends).
func (s *Server) startCaptureSends() {
	c := &s.sends
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.wake, c.stop, c.done = make(chan struct{}, 1), cancel, make(chan struct{})
	wake, done := c.wake, c.done
	c.mu.Unlock()
	wake <- struct{}{}
	go func() {
		defer close(done)
		tick := time.NewTicker(captureSendsEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-tick.C:
			}
			s.settleCaptureSends(ctx)
		}
	}()
}

// pokeCaptureSends makes the settlement look again.
func (s *Server) pokeCaptureSends() {
	c := &s.sends
	c.mu.Lock()
	wake := c.wake
	c.mu.Unlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

// stopCaptureSends ends the settlement and waits for its pass, once: no
// send is tried and no cancel finished in this process after it (the next
// start's settlement finishes them).
func (s *Server) stopCaptureSends() {
	c := &s.sends
	c.once.Do(func() {
		c.mu.Lock()
		stop, done := c.stop, c.done
		c.mu.Unlock()
		if stop == nil {
			return
		}
		stop()
		<-done
	})
}

// settleCaptureSends is one pass of the settlement: every pending send to
// a decision tried again (tryDecision); and while the desk holds its
// records (recovered) and is not closing, every pending send to Brakeman
// tried again from its stored request (trySend; a send with a cancel
// asked is the cancel's), every cancel not done finished (finishCancel),
// and every pinned op of a send that is no longer pending, or is gone,
// let go at the desk. A page that cannot be read ends the
// pass; the next one reads it again.
func (s *Server) settleCaptureSends(ctx context.Context) {
	ds, d := s.dispatch, s.brakeman
	if ds == nil {
		return
	}
	// The sends to a decision are DR1's to answer, the desk's to Brakeman.
	desk := d != nil && d.recovered.Load() && !d.recovering.Load() && !d.closing.Load()
	walk := func(page func(after string) (dispatch.SendsPage, error), each func(dispatch.CaptureSend)) {
		for after := ""; ctx.Err() == nil; {
			p, err := page(after)
			if err != nil {
				s.log.Warn("the capture sends to settle could not be read; the next pass reads them again", "err", err)
				return
			}
			for _, sv := range p.Sends {
				if ctx.Err() == nil {
					each(sv)
				}
			}
			if after = p.Next; after == "" {
				return
			}
		}
	}
	walk(func(after string) (dispatch.SendsPage, error) {
		return ds.PendingSends(ctx, after, dispatch.MaxCapturePage)
	}, func(sv dispatch.CaptureSend) {
		var err error
		switch {
		case len(sv.Cancels) > 0:
			return
		case sv.Kind == dispatch.SendDecision:
			_, err = s.tryDecision(ctx, sv.ID, false)
		case sv.Kind == dispatch.SendBrakeman && desk:
			_, err = s.trySend(ctx, sv.ID, false)
		}
		if err != nil && ctx.Err() == nil {
			s.log.Warn("a capture's send is kept pending: the next pass tries it again", "send", sv.ID, "kind", sv.Kind, "err", err)
		}
	})
	if !desk {
		return
	}
	walk(func(after string) (dispatch.SendsPage, error) {
		return ds.AskedCancels(ctx, after, dispatch.MaxCapturePage)
	}, func(sv dispatch.CaptureSend) {
		if _, err := s.finishCancel(ctx, sv.ID); err != nil && ctx.Err() == nil {
			s.log.Warn("a capture's send's cancel is kept: the next pass finishes it", "send", sv.ID, "err", err)
		}
	})
	pins, err := d.st.Pinned()
	if err != nil {
		s.log.Warn("Brakeman's pinned requests could not be read; the next pass reads them again", "err", err)
		return
	}
	for _, p := range pins {
		if ctx.Err() != nil || !dispatch.ValidID(p.OpID) {
			continue
		}
		sv, err := ds.CaptureSend(ctx, p.OpID)
		if (err == nil && sv.State != dispatch.SendPending) || errors.Is(err, dispatch.ErrNotFound) {
			if err := d.st.Release(p.OpID); err != nil {
				s.log.Warn("a capture's send's request stays pinned at Brakeman: the next pass lets it go", "send", p.OpID, "err", err)
			}
		}
	}
}

// ---- a Brakeman turn's reads of a capture

// admitCapturePath admits a turn's read of the capture its path names
// (and of a send of it) as what the capture is of (captureItem), read
// again at each read; a capture there is none of is left to the route,
// which answers it not found.
func admitCapturePath(r *http.Request, o *observer) (bool, error) {
	id := r.PathValue("id")
	if !dispatch.ValidID(id) {
		return true, nil
	}
	ds, err := o.rr.dispatch()
	if err != nil || ds == nil {
		return ds == nil && err == nil, err
	}
	item, found, err := o.rr.captureItem(r.Context(), ds, id)
	if err != nil || !found {
		return err == nil, err
	}
	return o.grant.admits(item), nil
}

// turnSendKeys are the members of a send a turn reads: what it is and
// where it stands, never whom it was asked of, its request, its outcomes'
// targets or its cancels.
var turnSendKeys = []string{"id", "capture", "rev", "kind", "digest", "createdAt", "state"}

// turnSend is a send cut to turnSendKeys, with its ref.
func turnSend(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, k := range turnSendKeys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	var head struct {
		ID      string `json:"id"`
		Capture string `json:"capture"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, err
	}
	ref, _ := json.Marshal((brakeman.Ref{Kind: brakeman.RefCapture, Capture: head.Capture, Send: head.ID}).String())
	out["ref"] = ref
	return out, nil
}

// filterCapture is GET /api/captures/{id} for a turn: noted at its state
// (live, or deleted for its tombstone) and revision, with its ref; its
// sends cut to what each is (turnSend). The screenshot is named by its
// size and hash alone, as the route answers it.
func filterCapture(r *http.Request, o *observer, body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	var head struct {
		ID        string          `json:"id"`
		Rev       int64           `json:"rev"`
		DeletedAt string          `json:"deletedAt"`
		Sends     json.RawMessage `json:"sends"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return nil, err
	}
	ref := brakeman.Ref{Kind: brakeman.RefCapture, Capture: head.ID}
	state := captureLive
	if head.DeletedAt != "" {
		state = refDeleted
	}
	o.note(ref, brakeman.ClassDispatch, state, fmt.Sprint(head.Rev))
	if len(head.Sends) > 0 {
		var sends []json.RawMessage
		if err := json.Unmarshal(head.Sends, &sends); err != nil {
			return nil, err
		}
		cut := make([]map[string]json.RawMessage, 0, len(sends))
		for _, sv := range sends {
			t, err := turnSend(sv)
			if err != nil {
				return nil, err
			}
			cut = append(cut, t)
		}
		b, err := json.Marshal(cut)
		if err != nil {
			return nil, err
		}
		m["sends"] = b
	}
	m["ref"], _ = json.Marshal(ref.String())
	return encodeTurnReply(m)
}

// filterCaptureSend is a frozen send's read for a turn: the send cut to
// what it is (turnSend) and the document as it was sent, noted at the
// send's state, the revision it froze and the document's digest.
func filterCaptureSend(r *http.Request, o *observer, body []byte) ([]byte, error) {
	var in struct {
		Send     json.RawMessage `json:"send"`
		Document json.RawMessage `json:"document"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	var head struct {
		ID      string `json:"id"`
		Capture string `json:"capture"`
		Rev     int64  `json:"rev"`
		Digest  string `json:"digest"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal(in.Send, &head); err != nil {
		return nil, err
	}
	ref := brakeman.Ref{Kind: brakeman.RefCapture, Capture: head.Capture, Send: head.ID}
	o.noteHashed(ref.String(), brakeman.ClassDispatch, head.State, fmt.Sprint(head.Rev), head.Digest)
	send, err := turnSend(in.Send)
	if err != nil {
		return nil, err
	}
	sb, err := json.Marshal(send)
	if err != nil {
		return nil, err
	}
	rb, _ := json.Marshal(ref.String())
	return encodeTurnReply(map[string]json.RawMessage{"send": sb, "document": in.Document, "ref": rb})
}

// encodeTurnReply is a turn's reply of m, HTML escaping off as the route
// writes its own.
func encodeTurnReply(m map[string]json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
