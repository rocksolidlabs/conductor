package crew

import (
	"context"
	"fmt"
	"slices"

	"github.com/rocksolidlabs/conductor/ext"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Held answers (docs/plans/sync-seams.md, OS-3): an answer made elsewhere,
// handed in by the sync store's door (internal/api) once the identity
// provider's AdmitsAnswer admitted it, recorded through DR1's path whole
// under the actor user:<subject> and an authority via sync, counted as an
// identity's, and applied only once AdmitsAnswer admits it again from what
// the ledger keeps.

// CodeAnsweredAtServer refuses a held answer where its server's router
// confirms policies: it is answered at the server, by the operator or the
// people its router names, never by an answer given elsewhere
// (docs/plans/decision-policy.md, Responding, Held answers).
const CodeAnsweredAtServer = "answered_at_server"

// answeredAtServerWords are CodeAnsweredAtServer's fixed words.
func answeredAtServerWords(id string) string {
	return fmt.Sprintf("%s is answered at its server, by the operator or the people its router names, at the server's own routes: an answer given elsewhere is not taken here", id)
}

// RespondHeld records a held answer: RespondDecision with auth via sync and
// its statement kept beside the row it writes, accepted or a kept
// refusal. The caller (the sync store's door) has had AdmitsAnswer admit
// it; DR1's rules judge it after, as an identity's answer. It hands in no
// confirmation of the router's: where a router confirms policies, it is
// RespondHeldWith that takes a held answer.
func (e *Engine) RespondHeld(ctx context.Context, id string, resp Response, actor string, auth Authority, statement string) (ResponseReceipt, error) {
	return e.RespondHeldWith(ctx, id, resp, actor, auth, statement, nil)
}

// RespondHeldWith is RespondHeld with the router's confirmation the door
// asked for the record (ConfirmFor's, nil for none asked), which the
// response is judged with in its transaction as a live answer to a record
// under the server's own policy is (judgePolicy).
func (e *Engine) RespondHeldWith(ctx context.Context, id string, resp Response, actor string, auth Authority, statement string, conf *ext.Confirmation) (ResponseReceipt, error) {
	if auth.Via != ViaSync || statement == "" {
		return ResponseReceipt{}, refuse(CodeInvalid, "a held answer comes via sync with its statement")
	}
	if conf != nil {
		c := *conf
		c.Groups = slices.Clone(conf.Groups)
		conf = &c
	}
	return e.respond(ctx, id, resp, actor, auth, policyIn{routes: e.RoutesPolicies(), conf: conf, statement: statement, counted: e.takesCounted()})
}

// heldRefused judges a held answer to an open record before the router's
// policy is (judgePolicy): done says it answered, a kept refusal in rc or
// an unkept one as err. While a router that confirms policies is
// registered, a record under a router's policy takes none (the sync
// store's door refuses them before; this is the second guard): refused
// answered_at_server, kept, unless the door asked the router about another
// policy than the record holds now (a policy written since its ask), which
// is stale_policy, unkept, for the provider to hand in again; a record
// under the server's own policy is judged as a live answer to it, with the
// confirmation the door asked (none asked is stale_policy, unkept: never
// taken as not in force). A comparison's selection, the operator's alone,
// takes none at all, answered only at this server.
func heldRefused(rec ledger.DecisionRecord, pol policyIn, rc *ResponseReceipt) (done bool, err error) {
	id := rec.ID()
	p, routed := routedOf(rec.Policy)
	switch {
	case pol.routes && routed && pol.conf != nil && pol.conf.Revision != "" && pol.conf.Revision != p.Revision:
		return true, refuse(CodeStalePolicy, "%s's policy was written since this answer was confirmed: try again", id)
	case pol.routes && routed:
		rc.Code, rc.Words = CodeAnsweredAtServer, answeredAtServerWords(id)
	case rec.Origin == ledger.OriginCompare:
		rc.Code, rc.Words = CodeNotApprover, fmt.Sprintf("%s is a trial's selection: it is answered only at this server", id)
	case pol.routes && pol.conf == nil:
		return true, refuse(CodeStalePolicy, "%s's policy was not confirmed for this answer: try again", id)
	default:
		return false, nil
	}
	return true, nil
}

// SetRecheck gives the engine what asks AdmitsAnswer again of a held
// answer that waits for its recheck (decision id's accepted response k):
// it answers through ConfirmAuthority, later, and asks again itself while
// the provider cannot say. Each answer is handed over once in a process.
func (e *Engine) SetRecheck(fn func(id string, k int)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.recheck = fn
}

// askRecheck hands the held answers ks to the recheck, those not handed
// over before. The caller holds no lock.
func (e *Engine) askRecheck(ks []confirmKey) {
	if len(ks) == 0 {
		return
	}
	e.mu.Lock()
	fn := e.recheck
	var out []confirmKey
	if fn != nil {
		if e.asked == nil {
			e.asked = map[confirmKey]bool{}
		}
		for _, k := range ks {
			if !e.asked[k] {
				e.asked[k] = true
				out = append(out, k)
			}
		}
	}
	e.mu.Unlock()
	for _, k := range out {
		fn(ledger.DecisionID(k.n), k.k)
	}
}

// MaxWaitingWords bounds a decision's Waiting.
const MaxWaitingWords = 256

// SetRecheckWaiting has decision id's view say, while its held answer k's
// recheck cannot be made yet, that it waits and why; "" for no longer.
// ConfirmAuthority's answer clears it.
func (e *Engine) SetRecheckWaiting(id string, k int, words string) {
	n, ok := ledger.ParseDecisionID(id)
	if !ok {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	key := confirmKey{n, k}
	if words == "" {
		delete(e.waiting, key)
		return
	}
	if e.waiting == nil {
		e.waiting = map[confirmKey]string{}
	}
	e.waiting[key] = cutWordsTo(words, MaxWaitingWords)
}

// recheckWaiting is what rec's view says of its accepted response's
// recheck, "" for nothing.
func (e *Engine) recheckWaiting(rec ledger.DecisionRecord) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(rec.Responses) - 1; i >= 0; i-- {
		if r := rec.Responses[i]; r.Status == ledger.ResponseAccepted {
			return e.waiting[confirmKey{rec.N, r.K}]
		}
	}
	return ""
}
