package hostagent

import (
	"context"
	"errors"

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

// keptActivity is an entry kept for the server, and whether it was sent
// (written, or perhaps written) on the current connection.
type keptActivity struct {
	m    proto.HostActivityMsg
	sent bool
}

// keepActivity numbers m and keeps it, the oldest going past the bound.
func (a *agent) keepActivity(m proto.HostActivityMsg) {
	a.actMu.Lock()
	defer a.actMu.Unlock()
	a.actSeq++
	m.Seq = a.actSeq
	a.actKept = append(a.actKept, keptActivity{m: m})
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
// next connection sends.
func (a *agent) sendKeptActivity() {
	a.actSendMu.Lock()
	defer a.actSendMu.Unlock()
	var last uint64
	a.mu.Lock()
	w := a.out
	a.mu.Unlock()
	for {
		a.actMu.Lock()
		i := slicesIndexUnsent(a.actKept)
		if i < 0 {
			a.actMu.Unlock()
			break
		}
		// The count of those left out goes with the first entry sent, and
		// stays with it should it be sent again.
		m, extra := a.actKept[i].m, a.actLeftOut
		m.LeftOut = min(m.LeftOut+extra, proto.MaxActivityLeftOut)
		a.actMu.Unlock()
		err := a.send(m)
		if errors.Is(err, errConnDown) || errors.Is(err, session.ErrSlowConsumer) {
			// Not written: sent in its turn later.
			break
		}
		a.actMu.Lock()
		// Written, or perhaps (a write that failed): sent, kept until taken.
		if j := keptIndex(a.actKept, m.Seq); j >= 0 {
			a.actKept[j].sent = true
			a.actKept[j].m.LeftOut = m.LeftOut
		}
		a.actLeftOut -= min(a.actLeftOut, extra)
		a.actMu.Unlock()
		last = m.Seq
		if err != nil {
			break
		}
	}
	if last > 0 {
		a.confirmActivity(w, last)
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

// confirmActivity has the server's taking of the entries sent on w up to
// upTo confirmed by a ping on w, one confirmation at a time (a later one
// asked meanwhile is made after it): answered while w is still the
// host's, they go.
func (a *agent) confirmActivity(w *controlWriter, upTo uint64) {
	a.actMu.Lock()
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
			to := a.actConfirmTo
			a.actMu.Unlock()
			taken := a.confirmOn(w)
			a.mu.Lock()
			same := a.out == w
			a.mu.Unlock()
			a.actMu.Lock()
			if taken && same {
				kept := a.actKept[:0]
				for _, k := range a.actKept {
					if !k.sent || k.m.Seq > to {
						kept = append(kept, k)
					}
				}
				a.actKept = kept
			}
			if !taken || !same || a.actConfirmTo == to {
				a.actConfirming = false
				a.actMu.Unlock()
				return
			}
			a.actMu.Unlock()
		}
	}()
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

// registeredActivity takes what a registration says of the kept entries:
// a server that does not say ActivitySeq is sent none of them, nor kept
// any for; one that resumed the session is sent every one again, in order
// (it drops those it took); one that did not is sent those never written,
// and the others are counted left out.
func (a *agent) registeredActivity(takes, resumed bool) {
	a.activitySeq.Store(takes)
	a.actMu.Lock()
	defer a.actMu.Unlock()
	if !takes {
		a.actKept, a.actLeftOut = nil, 0
		return
	}
	kept := a.actKept[:0]
	for _, k := range a.actKept {
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
