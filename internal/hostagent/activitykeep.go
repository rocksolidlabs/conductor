package hostagent

import (
	"context"
	"errors"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The activity entries a host's session records reach its server on
// `activity`, as they are recorded. To a server whose registered said
// ActivitySeq (docs/protocol.md, The host's activity while away), the host
// also keeps them until it knows the server took them, so that the entries
// recorded while its connection is down, and those sent as a connection
// ended, reach the server when the next connection starts, in order and
// each once:
//
//   - each entry is numbered (proto.HostActivityMsg.Seq), from 1 and one
//     more for each, for the life of the host's process, and kept;
//   - it is sent in its turn; one that was certainly not written (no
//     connection, or no turn in time, withdrawn) stays to be sent;
//   - once the server has answered a ping the host sent after it on the
//     same connection, it is taken (the server reads in order) and goes;
//   - as a connection starts on a session the server resumed (the same
//     session, which keeps the last number it took), every entry kept is
//     sent again in order and the server drops the ones it took; on one it
//     did not resume (it forgot the session, a restart), the entries sent
//     before are not sent again, for they may have been taken, and are
//     counted left out;
//   - past maxActivityKept the oldest goes, counted left out; the count
//     goes with the next entry sent (LeftOut), and the server records that
//     entries may be missing there.
//
// To an older server nothing is kept: an entry goes once, as before.

// maxActivityKept bounds the entries a host keeps for its server.
const maxActivityKept = 256

// keptActivity is an entry kept for the server: whether it was sent
// (written, or perhaps written) and on which connection (on, the
// registration's number, actEpoch), and whether it is late: recorded
// before the connection that carries it began, or sent again, which the
// server records and shows but offers no one as a live event.
type keptActivity struct {
	m    proto.HostActivityMsg
	sent bool
	on   uint64
	late bool
}

// keepActivity numbers m and keeps it, the oldest going past the bound. It
// is late unless the code can show it was made on the current connection:
// handed over on this registration (made, madeOn) and stamped (at; none
// counts as earlier) no earlier than the registration began. An entry recorded while the host was
// away and handed to its sender only after the connection came back (the
// forwarder behind a burst, a slow goroutine) is late, whichever goroutine
// reaches here first; in doubt, it is late and pages no one.
func (a *agent) keepActivity(m proto.HostActivityMsg, made uint64, at time.Time) {
	a.actMu.Lock()
	defer a.actMu.Unlock()
	a.actSeq++
	m.Seq = a.actSeq
	late := made != a.actEpoch || at.Before(a.actBegan)
	a.actKept = append(a.actKept, keptActivity{m: m, late: late})
	if len(a.actKept) > a.keptBound() {
		a.actLeftOut = min(a.actLeftOut+1+a.actKept[0].m.LeftOut, proto.MaxActivityLeftOut)
		a.actKept = a.actKept[1:]
	}
}

func (a *agent) keptBound() int {
	if a.actBound > 0 {
		return a.actBound
	}
	return maxActivityKept
}

// sendKeptActivity sends the kept entries not yet sent on the current
// connection, in order, one sender at a time, the count of those left out
// with the first; then has the server's taking of them confirmed. It stops
// at an entry that was certainly not written, which the next entry or the
// next connection sends. An entry whose send returns after a registration
// began (the connection it went on ended meanwhile) is taken as that
// registration takes the entries sent before it: sent again to a session
// the server resumed, counted left out by one it did not.
func (a *agent) sendKeptActivity() {
	a.actSendMu.Lock()
	defer a.actSendMu.Unlock()
	var last, lastOn uint64
	var lastW *controlWriter
	for {
		a.actMu.Lock()
		i := slicesIndexUnsent(a.actKept)
		if i < 0 {
			a.actMu.Unlock()
			break
		}
		// The count of those left out goes with the first entry sent, and
		// stays with it should it be sent again.
		k, extra, epoch := a.actKept[i], a.actLeftOut, a.actEpoch
		m := k.m
		m.LeftOut = min(m.LeftOut+extra, proto.MaxActivityLeftOut)
		m.Late = k.late
		a.actMu.Unlock()
		// The writer of this registration's connection: a connection ends
		// (its writer let go) before the next registration begins.
		a.mu.Lock()
		w := a.out
		a.mu.Unlock()
		err := a.send(m)
		if errors.Is(err, errConnDown) || errors.Is(err, session.ErrSlowConsumer) {
			// Not written: sent in its turn later.
			break
		}
		a.actMu.Lock()
		a.actLeftOut -= min(a.actLeftOut, extra)
		if j := keptIndex(a.actKept, m.Seq); j >= 0 {
			a.actKept[j].m.LeftOut = m.LeftOut
			switch {
			case a.actEpoch == epoch:
				// Written, or perhaps (a write that failed): sent, kept
				// until taken.
				a.actKept[j].sent, a.actKept[j].on = true, epoch
			case a.actResumed:
				// Written on a connection that ended as it went: sent
				// again, late, on the resumed session's.
				a.actKept[j].late = true
			default:
				// Perhaps taken by a server that did not resume the
				// session: not sent again, counted left out.
				a.actLeftOut = min(a.actLeftOut+1+m.LeftOut, proto.MaxActivityLeftOut)
				a.actKept = append(a.actKept[:j], a.actKept[j+1:]...)
			}
		}
		current := a.actEpoch == epoch
		a.actMu.Unlock()
		if current {
			last, lastOn, lastW = m.Seq, epoch, w
		}
		if err != nil {
			break
		}
	}
	if last > 0 {
		a.confirmActivity(lastW, lastOn, last)
	}
}

// slicesIndexUnsent is the index of the first kept entry not sent, -1 for
// none.
func slicesIndexUnsent(kept []keptActivity) int {
	for i, k := range kept {
		if !k.sent {
			return i
		}
	}
	return -1
}

// keptIndex is the index of the kept entry numbered seq, -1 for none.
func keptIndex(kept []keptActivity, seq uint64) int {
	for i, k := range kept {
		if k.m.Seq == seq {
			return i
		}
	}
	return -1
}

// confirmActivity has the server's taking of the entries sent on w (the
// connection of registration on) up to upTo confirmed by a ping on w, one
// confirmation at a time: one asked meanwhile, on the same connection or a
// later one, is made after it, on its own connection. Answered while that
// connection is still the host's, the entries sent on it up to upTo go.
func (a *agent) confirmActivity(w *controlWriter, on, upTo uint64) {
	a.actMu.Lock()
	if on != a.actConfirmOn {
		a.actConfirmOn, a.actConfirmW, a.actConfirmTo = on, w, 0
	}
	a.actConfirmTo = max(a.actConfirmTo, upTo)
	if a.actConfirming {
		a.actMu.Unlock()
		return
	}
	a.actConfirming = true
	a.actMu.Unlock()
	go func() {
		for {
			a.actMu.Lock()
			to, on, w := a.actConfirmTo, a.actConfirmOn, a.actConfirmW
			a.actMu.Unlock()
			taken := a.confirmOn(w)
			a.mu.Lock()
			same := a.out == w
			a.mu.Unlock()
			a.actMu.Lock()
			if taken && same && a.actEpoch == on {
				kept := a.actKept[:0]
				for _, k := range a.actKept {
					if !k.sent || k.on != on || k.m.Seq > to {
						kept = append(kept, k)
					}
				}
				a.actKept = kept
			}
			if a.actConfirmTo == to && a.actConfirmOn == on {
				a.actConfirming = false
				a.actMu.Unlock()
				return
			}
			a.actMu.Unlock()
		}
	}()
}

// madeOn is the registration the host is on (actEpoch), for an entry the
// session hands over now: an entry is live only when it is kept on the same
// one (keepActivity).
func (a *agent) madeOn() uint64 {
	a.actMu.Lock()
	defer a.actMu.Unlock()
	return a.actEpoch
}

// confirmOn says whether the server answered a ping sent on w; tests
// replace it (actConfirm).
func (a *agent) confirmOn(w *controlWriter) bool {
	if a.actConfirm != nil {
		return a.actConfirm()
	}
	if w == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(w.ctx, a.heardWithin())
	defer cancel()
	return w.c.Ping(ctx) == nil
}

// registeredActivity takes what a registration says of the kept entries,
// as the registration's connection is about to begin (its number,
// actEpoch, one more): a server that does not say ActivitySeq is sent none
// of them, nor kept any for; one that resumed the session is sent every
// one again, in order (it drops those it took); one that did not is sent
// those never written, and the others are counted left out. Every entry
// kept now was recorded before this connection, or is sent again: late.
func (a *agent) registeredActivity(takes, resumed bool) {
	a.activitySeq.Store(takes)
	a.actMu.Lock()
	defer a.actMu.Unlock()
	a.actEpoch++
	a.actBegan = time.Now()
	a.actResumed = resumed
	if !takes {
		a.actKept, a.actLeftOut = nil, 0
		return
	}
	kept := a.actKept[:0]
	for _, k := range a.actKept {
		k.late = true
		switch {
		case !k.sent:
			kept = append(kept, k)
		case resumed:
			k.sent = false
			kept = append(kept, k)
		default:
			a.actLeftOut = min(a.actLeftOut+1+k.m.LeftOut, proto.MaxActivityLeftOut)
		}
	}
	a.actKept = kept
}
