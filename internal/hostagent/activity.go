package hostagent

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// activityQueue bounds the entries waiting for the control connection.
const activityQueue = 256

// activityForwarder carries the activity entries of the local session, and
// its chat messages, to the server. The session calls its OnActivity and
// OnChat hooks on the goroutine that recorded the entry or kept the message,
// which may be the one reading the process or the one serving a viewer, and
// that goroutine must not wait for a network write. So the hooks only queue,
// and one goroutine sends, in the order queued. The queue is bounded: past
// it entries are dropped, which costs the admin stream a line and nothing
// else, because the session keeps its own log and its own chat.
type activityForwarder struct {
	queue    chan forwarded
	send     func(session.ActivityEntry, session.AttentionState, uint64)
	sendChat func(session.ChatMessage)
	log      *slog.Logger
	pending  atomic.Int64 // entries queued or being sent
	dropped  atomic.Uint64
}

// forwarded is an entry waiting for the connection, with the attention state
// it records when it is an attention entry: the session hands the state on
// with the entry (set with the entry's stamp), for by the time the entry is
// sent the session may be in another. made is the registration the host was
// on as the session handed the entry over (agent.madeOn): what says, when
// the entry is kept, whether it was made before the connection carrying it
// began. Or a chat message (chat set).
type forwarded struct {
	entry session.ActivityEntry
	state session.AttentionState
	made  uint64
	chat  *session.ChatMessage
}

func newActivityForwarder(send func(session.ActivityEntry, session.AttentionState, uint64), sendChat func(session.ChatMessage), log *slog.Logger) *activityForwarder {
	return &activityForwarder{queue: make(chan forwarded, activityQueue), send: send, sendChat: sendChat, log: log}
}

// push queues e, with the attention state it records and the registration
// it was made on, without waiting. It reports false, and drops e, when the
// queue is full.
func (f *activityForwarder) push(e session.ActivityEntry, state session.AttentionState, made uint64) bool {
	return f.queueOne(forwarded{entry: e, state: state, made: made})
}

// pushChat queues a chat message the session kept, the same way.
func (f *activityForwarder) pushChat(m session.ChatMessage) bool {
	return f.queueOne(forwarded{chat: &m})
}

func (f *activityForwarder) queueOne(q forwarded) bool {
	f.pending.Add(1) // before the entry is visible, so idle never misses it
	select {
	case f.queue <- q:
		return true
	default:
	}
	f.pending.Add(-1)
	if n := f.dropped.Add(1); n == 1 || n%100 == 0 {
		f.log.Debug("activity not forwarded: the queue for the server is full", "dropped", n)
	}
	return false
}

// run sends queued entries until ctx ends.
func (f *activityForwarder) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case q := <-f.queue:
			if q.chat != nil {
				f.sendChat(*q.chat)
			} else {
				f.send(q.entry, q.state, q.made)
			}
			f.pending.Add(-1)
		}
	}
}

// idle reports whether every entry queued so far has been sent.
func (f *activityForwarder) idle() bool { return f.pending.Load() == 0 }
