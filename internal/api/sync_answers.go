package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Held answers handed in by the sync store (docs/plans/sync-seams.md,
// OS-3). The store hands in; the server judges: a held answer is never
// taken where the server's router confirms policies, never for a
// comparison's selection or a request kept on the machine, and elsewhere
// only once the identity provider's AdmitsAnswer admits its statement for
// this server, decision, response, rev, content and request part, with the
// grant the decision is opened to; then DR1's path whole records it, and
// it is applied only once AdmitsAnswer admits it again.

// RespondDecision hands in a held answer (ext.Host).
func (h *extHost) RespondDecision(ctx context.Context, r ext.HeldAnswer) (ext.AnswerOutcome, error) {
	if err := h.syncHost(); err != nil {
		return ext.AnswerOutcome{}, err
	}
	if err := r.Valid(); err != nil {
		return ext.AnswerOutcome{}, err
	}
	out, err := h.s.respondHeld(ctx, r)
	if err != nil {
		return ext.AnswerOutcome{}, err
	}
	if err := out.Valid(); err != nil {
		// A receipt past the outcome's bound: the outcome without it.
		out.Receipt = nil
	}
	return out, nil
}

// heldRefusal is a refusal of a held answer, nothing written.
func heldRefusal(code, format string, args ...any) ext.AnswerOutcome {
	return ext.AnswerOutcome{Status: ext.AnswerRefused, Code: code, Words: fmt.Sprintf(format, args...)}
}

// respondHeld judges r and, admitted, records it through the engine.
func (s *Server) respondHeld(ctx context.Context, r ext.HeldAnswer) (ext.AnswerOutcome, error) {
	if s.runs == nil || s.ledger == nil {
		return ext.AnswerOutcome{}, fmt.Errorf("%w: this server keeps no decisions", ext.ErrUnsupported)
	}
	n, _ := ledger.ParseDecisionID(r.Decision)
	rec, ok, err := s.ledger.Decision(ctx, n)
	if err != nil {
		return ext.AnswerOutcome{}, err
	}
	if !ok {
		return heldRefusal(crew.CodeNotFound, "no decision %s", r.Decision), nil
	}
	actor := ext.SubjectActor + r.Check.Subject
	resp := crew.Response{ResponseID: r.ResponseID, Choice: r.Choice, Text: r.Text, Rationale: r.Rationale, ExpectedRev: r.ExpectedRev}
	statement := r.Check.Statement
	sum := sha256.Sum256([]byte(statement))
	auth := crew.Authority{Via: crew.ViaSync, Subject: r.Check.Subject, Membership: r.Check.Membership, SignIn: r.Check.SignIn,
		AppDevice: r.Check.AppDevice, At: r.Check.At.Format(time.RFC3339Nano), StatementSHA256: hex.EncodeToString(sum[:]), Held: r.Held, Fence: r.Fence}
	if heldBefore(rec, actor, r.ResponseID) {
		// A hand-in of an answer the ledger holds already: answered from
		// its row by DR1's repeat (or refused response_conflict for another
		// body), nothing written and the provider not asked again, so a
		// repeat lands once whatever the provider says now.
		auth.Grant = string(heldGrant(rec))
		return s.recordHeld(ctx, r.Decision, resp, actor, auth, statement)
	}
	// Where the router confirms policies a held answer is never taken:
	// nothing written, the router and the identity provider not asked, the
	// refusal final.
	if s.runs.RoutesPolicies() {
		return heldRefusal(crew.CodeAnsweredAtServer, "%s is answered at its server, by the operator or the people its router names, at the server's own routes: an answer given elsewhere is not taken here", r.Decision), nil
	}
	doc, ok, err := s.decisionDoc(ctx, n)
	if err != nil {
		return ext.AnswerOutcome{}, err
	}
	if !ok {
		return heldRefusal(crew.CodeNotFound, "no decision %s", r.Decision), nil
	}
	if doc.Request == nil {
		// Its request never left: nothing elsewhere answers it.
		return heldRefusal(crew.CodeNotApprover, "%s's request is kept on its server: it is answered only there", r.Decision), nil
	}
	if r.Choice != nil && *r.Choice >= len(doc.Request.Options) {
		return heldRefusal(crew.CodeInvalid, "choice %d names no option of %s's %d", *r.Choice, r.Decision, len(doc.Request.Options)), nil
	}
	if crew.CleanText(r.Text) != r.Text || crew.CleanText(r.Rationale) != r.Rationale {
		// What is recorded is what was signed, byte for byte: the recheck at
		// application hashes the receipt's text again.
		return heldRefusal(crew.CodeInvalid, "a held answer's text and rationale carry no control character and no space at either end"), nil
	}
	check := r.Check
	check.ContentSHA256 = ext.AnswerContentSHA256(r.Choice, r.Text, r.Rationale)
	check.RequestSHA256 = doc.RequestSHA256
	// The hash admitted is kept with the answer: its recheck shows the
	// provider this one, whatever the document is by then.
	auth.RequestSHA256 = doc.RequestSHA256
	grant := heldGrant(rec)
	switch s.admitsAnswer(ctx, check, grant) {
	case ext.AdmitNo:
		return heldRefusal(crew.CodeNotApprover, "%s: the identity provider does not admit %s's answer", r.Decision, actor), nil
	case ext.AdmitUnknown:
		return ext.AnswerOutcome{Status: ext.AnswerUnknownAuthority, Words: fmt.Sprintf("%s: the identity provider cannot say now whether it admits %s's answer", r.Decision, actor)}, nil
	}
	auth.Grant = string(grant)
	return s.recordHeld(ctx, r.Decision, resp, actor, auth, statement)
}

// heldGrant is the grant a held answer to rec is asked for: decide for a
// decision opened to the decide grant, else admin (the operator's).
func heldGrant(rec ledger.DecisionRecord) ext.Grant {
	if rec.Origin != ledger.OriginCompare && crew.GivenToGrant(rec.Request) {
		return ext.GrantDecide
	}
	return ext.GrantAdmin
}

// heldBefore says rec holds a held answer of actor's under responseID.
func heldBefore(rec ledger.DecisionRecord, actor, responseID string) bool {
	for _, row := range rec.Responses {
		if row.Actor == actor && row.ResponseID == responseID && authorityVia(row.Authority) == crew.ViaSync {
			return true
		}
	}
	return false
}

// authorityVia is an authority's via.
func authorityVia(authority string) string {
	var a crew.Authority
	_ = json.Unmarshal([]byte(authority), &a)
	return a.Via
}

// recordHeld records a held answer through DR1's path and maps what it
// answered to the outcome, by its status, never by its Repeat.
func (s *Server) recordHeld(ctx context.Context, did string, resp crew.Response, actor string, auth crew.Authority, statement string) (ext.AnswerOutcome, error) {
	rc, err := s.runs.RespondHeld(ctx, did, resp, actor, auth, statement)
	var de *crew.DecisionError
	if err != nil && !errors.As(err, &de) {
		// Not a judgement (the ledger failed): the provider hands it in
		// again later.
		return ext.AnswerOutcome{}, err
	}
	out := ext.AnswerOutcome{Repeat: rc.Repeat}
	switch {
	case de == nil:
		out.Status = ext.AnswerRecorded
	case de.Code == crew.CodeNotHeld:
		out.Status = ext.AnswerNotHeld
	case de.Code == crew.CodeDecisionMoved:
		out.Status = ext.AnswerMoved
	default:
		out.Status, out.Code = ext.AnswerRefused, de.Code
	}
	out.Words, out.Receipt = s.shownReceipt(ctx, did, rc, out.Status, out.Code)
	return out, nil
}

// heldWords are an outcome's words where the receipt's own may not leave:
// fixed, naming the decision and what became of the answer.
func heldWords(did, status, code string) string {
	switch status {
	case ext.AnswerRecorded:
		return did + ": recorded"
	case ext.AnswerRefused:
		return did + ": refused, " + code
	case ext.AnswerNotHeld:
		return did + ": its run is not running here"
	case ext.AnswerMoved:
		return did + ": it moved to another run while it was answered"
	}
	return did
}

// shownReceipt is what the sync store is handed of a receipt: what the
// decision's document would show of the response, through its kept
// verdicts. Its words leave only when the response's row was kept and its
// words' verdict is clean (else heldWords); the text and rationale only of
// an accepted response, each only when clean (else the cut's words); none
// of the receipt's other words (a router's why, whom it was for).
func (s *Server) shownReceipt(ctx context.Context, did string, rc crew.ResponseReceipt, status, code string) (string, []byte) {
	words := heldWords(did, status, code)
	if rc.Decision == "" {
		return words, nil
	}
	n, _ := ledger.ParseDecisionID(did)
	kept, _, err := s.ledger.OutputVerdicts(ctx, n)
	if err != nil {
		kept = nil
	}
	clean := func(part, text string) bool { return text == "" || kept[part] == verdictClean }
	shown := crew.ResponseReceipt{Decision: rc.Decision, K: rc.K, Status: rc.Status, Code: rc.Code, Actor: rc.Actor, Choice: rc.Choice, At: rc.At,
		Rev: rc.Rev, Repeat: rc.Repeat, Kept: rc.Kept}
	if rc.K > 0 && rc.Kept == nil && clean(ledger.PartResponse(rc.K, "words"), rc.Words) {
		words = rc.Words
	}
	shown.Words = words
	if rc.Status == "accepted" && rc.K > 0 {
		shown.Text, shown.Rationale = rc.Text, rc.Rationale
		if !clean(ledger.PartResponse(rc.K, "text"), rc.Text) {
			shown.Text = outputCutWords
		}
		if !clean(ledger.PartResponse(rc.K, "rationale"), rc.Rationale) {
			shown.Rationale = outputCutWords
		}
	}
	b, _ := json.Marshal(shown)
	return words, b
}

// admitsAnswer asks the identity provider whether it admits a: through
// the guard, within ext.IdentityWait; a failure, a late answer or a busy
// guard is unknown; no provider, or one that cannot check a held answer,
// is no.
func (s *Server) admitsAnswer(ctx context.Context, a ext.AnswerCheck, g ext.Grant) ext.Admission {
	gi, ok := s.exts.identity.(guardIdentity)
	if !ok {
		return ext.AdmitNo
	}
	sc, ok := gi.p.(ext.SubjectChecker)
	if !ok {
		return ext.AdmitNo
	}
	if !live(gi.ready) {
		return ext.AdmitUnknown
	}
	adm, _ := extCallSent(ctx, s, kindIdentityAnswer, gi.name, ext.IdentityWait, ext.AdmitUnknown, func(adm ext.Admission) error {
		switch adm {
		case ext.AdmitNo, ext.AdmitYes, ext.AdmitUnknown:
			return nil
		}
		return fmt.Errorf("an admission of %d", adm)
	}, func(ctx context.Context) (ext.Admission, error) {
		return sc.AdmitsAnswer(ctx, a, g)
	})
	return adm
}

// kindIdentityAnswer is the guard's kind of an identity provider's
// AdmitsAnswer: slots of its own, so held answers never take a request's
// authentication's.
const kindIdentityAnswer = ext.KindIdentity + "-answer"

// SyncDelivery reads what the ledger holds for a held answer: its outcome
// as a repeat's, its fence, and whether there is one. It writes nothing.
func (h *extHost) SyncDelivery(ctx context.Context, decision, responseID string) (ext.AnswerOutcome, string, bool, error) {
	if err := h.syncHost(); err != nil {
		return ext.AnswerOutcome{}, "", false, err
	}
	s := h.s
	n, ok := ledger.ParseDecisionID(decision)
	if !ok {
		return ext.AnswerOutcome{}, "", false, fmt.Errorf("%w: %q is not a decision's id", ext.ErrRefused, decision)
	}
	if s.ledger == nil {
		return ext.AnswerOutcome{}, "", false, fmt.Errorf("%w: this server keeps no decisions", ext.ErrUnsupported)
	}
	rec, ok, err := s.ledger.Decision(ctx, n)
	if err != nil || !ok {
		return ext.AnswerOutcome{}, "", false, err
	}
	for _, row := range rec.Responses {
		var a crew.Authority
		if row.ResponseID != responseID || json.Unmarshal([]byte(row.Authority), &a) != nil || a.Via != crew.ViaSync {
			continue
		}
		var rc crew.ResponseReceipt
		_ = json.Unmarshal([]byte(row.Receipt), &rc)
		rc.Repeat, rc.K = true, row.K
		out := ext.AnswerOutcome{Status: ext.AnswerRecorded, Repeat: true}
		if row.Status != ledger.ResponseAccepted {
			out.Status, out.Code = ext.AnswerRefused, rc.Code
		}
		// What the document would show of it, as RespondDecision answers.
		out.Words, out.Receipt = s.shownReceipt(ctx, decision, rc, out.Status, out.Code)
		if out.Valid() != nil {
			out.Receipt = nil
		}
		return out, a.Fence, true, nil
	}
	return ext.AnswerOutcome{}, "", false, nil
}

// LastDecision is the highest decision number the ledger holds.
func (h *extHost) LastDecision(ctx context.Context) (int64, error) {
	if err := h.syncHost(); err != nil {
		return 0, err
	}
	if h.s.ledger == nil {
		return 0, fmt.Errorf("%w: this server keeps no decisions", ext.ErrUnsupported)
	}
	return h.s.ledger.LastDecision(ctx)
}

// The recheck at application of a held answer: AdmitsAnswer asked again
// from what the ledger keeps (its authority and the statement beside it),
// yes applied, no superseded (crew.Engine.ConfirmAuthority), unknown asked
// again after recheckFirst, doubling to recheckLast, while the decision is
// answered by that response and the server runs.
const (
	recheckFirst = time.Second
	recheckLast  = time.Minute
)

// recheckHeld starts the recheck of decision id's held answer k, on the
// server's background.
func (s *Server) recheckHeld(id string, k int) {
	ctx := s.extCtx
	if ctx == nil {
		return
	}
	s.track(func() { s.recheckLoop(ctx, id, k) })
}

// recheckLoop is recheckHeld's loop.
func (s *Server) recheckLoop(ctx context.Context, id string, k int) {
	wait := recheckFirst
	for {
		adm, done := s.recheckOnce(ctx, id, k)
		if done {
			return
		}
		switch adm {
		case ext.AdmitYes:
			s.runs.ConfirmAuthority(id, k, true)
			return
		case ext.AdmitNo:
			s.runs.ConfirmAuthority(id, k, false)
			return
		}
		after := s.recheckAfter
		if after == nil {
			after = time.After
		}
		select {
		case <-ctx.Done():
			return
		case <-after(wait):
		}
		wait = min(wait*2, recheckLast)
	}
}

// recheckOnce asks AdmitsAnswer about decision id's response k as the
// ledger keeps it; done when there is nothing to ask (the decision no
// longer answered by k, or not readable as a held answer).
func (s *Server) recheckOnce(ctx context.Context, id string, k int) (ext.Admission, bool) {
	n, _ := ledger.ParseDecisionID(id)
	rec, ok, err := s.ledger.Decision(ctx, n)
	if err != nil {
		return ext.AdmitUnknown, ctx.Err() != nil
	}
	if !ok || rec.State != ledger.DecisionAnswered {
		return 0, true
	}
	var row *ledger.ResponseRow
	for i := range rec.Responses {
		if rec.Responses[i].K == k && rec.Responses[i].Status == ledger.ResponseAccepted {
			row = &rec.Responses[i]
		}
	}
	if row == nil {
		return 0, true
	}
	var a crew.Authority
	var rc crew.ResponseReceipt
	if json.Unmarshal([]byte(row.Authority), &a) != nil || a.Via != crew.ViaSync || json.Unmarshal([]byte(row.Receipt), &rc) != nil {
		return 0, true
	}
	statement, ok, err := s.ledger.Statement(ctx, n, k)
	if err != nil {
		return ext.AdmitUnknown, ctx.Err() != nil
	}
	at, perr := time.Parse(time.RFC3339Nano, a.At)
	if !ok || perr != nil {
		// Nothing to show the provider: not admitted.
		return ext.AdmitNo, false
	}
	// What the ledger keeps, in the bytes the hand-in showed: the request
	// part's hash the door admitted, the time in the zone it was made in.
	check := ext.AnswerCheck{Subject: a.Subject, Membership: a.Membership, SignIn: a.SignIn, AppDevice: a.AppDevice, At: at, Statement: statement,
		Decision: id, ResponseID: row.ResponseID, ExpectedRev: rc.Rev - 1, ContentSHA256: ext.AnswerContentSHA256(rc.Choice, rc.Text, rc.Rationale),
		RequestSHA256: a.RequestSHA256}
	return s.admitsAnswer(ctx, check, ext.Grant(a.Grant)), false
}
