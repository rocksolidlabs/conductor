package hostagent

import (
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

const (
	dcLowThreshold = 512 << 10
	dcHighWater    = 1 << 20
	dcHardLimit    = 4 << 20
	dcSendTimeout  = 30 * time.Second
	// dcDrainWait bounds the wait for a channel to send what it holds
	// before its peer connection is closed (dcSink.drain): long enough for
	// a backlog under the high-water mark on any path a viewer is on.
	dcDrainWait = 3 * time.Second
)

// dcSink writes frames to a WebRTC data channel with backpressure. Frames
// larger than the browser message limit are fragmented into chunks.
type dcSink struct {
	dc     *webrtc.DataChannel
	log    *slog.Logger
	low    chan struct{}
	closed atomic.Bool
	msgID  atomic.Uint32
	once   sync.Once
}

func newDCSink(dc *webrtc.DataChannel, log *slog.Logger) *dcSink {
	s := &dcSink{dc: dc, log: log, low: make(chan struct{}, 1)}
	dc.SetBufferedAmountLowThreshold(dcLowThreshold)
	dc.OnBufferedAmountLow(func() {
		select {
		case s.low <- struct{}{}:
		default:
		}
	})
	return s
}

// Transport labels welcome messages sent through this sink.
func (s *dcSink) Transport() string { return proto.TransportWebRTC }

// drain waits until the channel holds nothing unsent, it is no longer
// open, or until: closing the peer connection aborts the association, and
// what the channel still holds then (an error the viewer is owed, the
// output before it) would be lost with it.
func (s *dcSink) drain(until time.Time) {
	for s.dc.BufferedAmount() > 0 && !s.closed.Load() && s.dc.ReadyState() == webrtc.DataChannelStateOpen {
		left := time.Until(until)
		if left <= 0 {
			return
		}
		select {
		case <-s.low:
		case <-time.After(min(left, 10*time.Millisecond)):
		}
	}
}

func (s *dcSink) WriteFrame(frame []byte) error {
	if s.closed.Load() {
		return errors.New("data channel closed")
	}
	parts := proto.Chunk(uint16(s.msgID.Add(1)), frame)
	// One deadline for the whole frame, not one per part: a peer that drains
	// a part just before each wait ran out would stretch a frame of many
	// parts, and a client the session dropped holds its slot until its
	// write returns (session.Sink).
	until := time.Now().Add(dcSendTimeout)
	for _, part := range parts {
		if err := s.waitForRoom(until); err != nil {
			return err
		}
		if err := s.dc.Send(part); err != nil {
			return err
		}
	}
	return nil
}

// buffered returns the bytes the data channel has taken that the other end has
// not acknowledged yet, which closing the connection drops. It is zero for a
// channel that is not open.
func (s *dcSink) buffered() uint64 {
	if s.closed.Load() || s.dc.ReadyState() != webrtc.DataChannelStateOpen {
		return 0
	}
	return s.dc.BufferedAmount()
}

func (s *dcSink) waitForRoom(until time.Time) error {
	return waitForRoom(until, s.dc.BufferedAmount, func() bool {
		// The sink's Close comes after the write (session.Sink), so the wait
		// looks for the channel's own close too: the peer letting go of its
		// viewer, or the viewer closing its channel. A closed channel keeps
		// counting what it held, so the count alone would wait it out.
		return !s.closed.Load() && s.dc.ReadyState() == webrtc.DataChannelStateOpen
	}, s.low)
}

// waitForRoom waits until the channel holds no more than its mark, its
// buffered count over the hard limit, the channel no longer open, or until.
func waitForRoom(until time.Time, buffered func() uint64, open func() bool, low <-chan struct{}) error {
	for {
		n := buffered()
		if n > dcHardLimit {
			return errors.New("data channel backlog too large")
		}
		if n <= dcHighWater {
			return nil
		}
		left := time.Until(until)
		if left <= 0 {
			return errors.New("data channel send timeout")
		}
		select {
		case <-low:
		case <-time.After(min(left, 250*time.Millisecond)):
		}
		if !open() {
			return errors.New("data channel closed")
		}
	}
}

func (s *dcSink) Close(reason error) {
	s.once.Do(func() {
		s.closed.Store(true)
		if reason != nil {
			if code := errorCode(reason); code != "" {
				_ = s.dc.Send(proto.NewError(code, reason.Error()))
			}
		}
		_ = s.dc.Close()
	})
}

// relayRoom is whether the server has room for more of a relayed viewer's
// frames: none while it says the viewer is behind (viewer_behind), when it
// holds more of them, unwritten to the viewer, than a viewer that keeps up
// would. The viewer's frames then wait here until it catches up, as a
// WebRTC viewer's wait while its data channel holds too much (dcSink),
// rather than on the connection the other viewers' frames share. Its zero
// value has room.
type relayRoom struct {
	mu sync.Mutex
	// caughtUp is closed as the viewer catches up; nil while it is not
	// behind.
	caughtUp chan struct{}
}

// set records what the server said of the viewer: behind or caught up.
func (r *relayRoom) set(behind bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case behind && r.caughtUp == nil:
		r.caughtUp = make(chan struct{})
	case !behind && r.caughtUp != nil:
		close(r.caughtUp)
		r.caughtUp = nil
	}
}

// wait returns once the viewer is not behind, or ended closes (the
// session's end: its last frames go as before, whether the viewer is behind
// or not, and the end's drain bounds them): session.ErrSlowConsumer when it
// still is at until, errViewerGone when gone closes first.
func (r *relayRoom) wait(until time.Time, gone, ended <-chan struct{}) error {
	for {
		r.mu.Lock()
		caughtUp := r.caughtUp
		r.mu.Unlock()
		if caughtUp == nil {
			return nil
		}
		left := time.Until(until)
		if left <= 0 {
			return session.ErrSlowConsumer
		}
		t := time.NewTimer(left)
		select {
		case <-caughtUp:
			t.Stop()
		case <-ended:
			t.Stop()
			return nil
		case <-gone:
			t.Stop()
			return errViewerGone
		case <-t.C:
			return session.ErrSlowConsumer
		}
	}
}

// relaySink writes frames through the server relay, on the host's control
// connection, each in its turn among the other relayed viewers' (agent's
// sendRelay), once the server has room for it (room): a frame that waits
// longer than relayTurnWait for both ends the viewer's subscription as a
// slow consumer, the viewer alone.
type relaySink struct {
	a        *agent
	viewerID string
	// room is the viewer's (peer.room), nil for always.
	room *relayRoom
	once sync.Once
	// gone is closed when the peer lets go of the viewer (let): a frame
	// waiting for its turn is withdrawn then. Nil never closes.
	gone     chan struct{}
	goneOnce sync.Once
}

func newRelaySink(a *agent, viewerID string) *relaySink {
	return &relaySink{a: a, viewerID: viewerID, gone: make(chan struct{})}
}

// let has the sink stop waiting on the viewer's behalf.
func (s *relaySink) let() {
	if s.gone != nil {
		s.goneOnce.Do(func() { close(s.gone) })
	}
}

// Transport labels welcome messages sent through this sink.
func (s *relaySink) Transport() string { return proto.TransportRelay }

func (s *relaySink) WriteFrame(frame []byte) error {
	turn := s.a.relayTurn()
	until := time.Now().Add(turn)
	if s.room != nil {
		if err := s.room.wait(until, s.gone, s.a.sessionEnded()); err != nil {
			return err
		}
	}
	return s.a.sendRelayWithin(s.viewerID, frame, s.gone, time.Until(until))
}

// Close tells the viewer why, when its turn comes in time, and then the
// server that the viewer is closed, however long that waits while the
// connection lasts: a close the server missed would leave the viewer
// attached there, to a session that no longer serves it. It runs on the
// viewer's own goroutine (session.Sink).
func (s *relaySink) Close(reason error) {
	s.once.Do(func() {
		if reason != nil {
			// The viewer's last frame, whether the server has room or not:
			// a few bytes, which tell the viewer why.
			if code := errorCode(reason); code != "" {
				_ = s.a.sendRelay(s.viewerID, proto.NewError(code, reason.Error()), s.gone)
			}
			_ = s.a.tell(proto.ViewerRef{T: proto.HostViewerClose, ViewerID: s.viewerID}, s.gone)
		}
	})
}

func errorCode(reason error) string {
	switch reason.Error() {
	case "session: link revoked":
		return proto.ErrCodeRevoked
	case "session: slow consumer":
		return proto.ErrCodeSlowConsumer
	}
	return ""
}
