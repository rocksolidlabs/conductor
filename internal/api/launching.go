package api

import (
	"context"
	"sync"
	"time"
)

// A server session's process starts before the registry holds the session:
// between the two the launch begins its record (a write synced to disk,
// history_journal.go), makes the session and registers it. An agent reports
// as soon as its process runs (a hook at its start, a stub's question), and
// a report the registry cannot place yet was answered 404 and lost: the
// notify client tries a 404 once. The launch marks its id from before its
// process starts until its return (registered, or failed), and a report
// that finds no session but the mark waits for the launch instead
// (reportTarget).

// launchWait bounds how long a report waits for a launch under way. It only
// bounds a hang: a launch registers its session once its record is written,
// and the notify client's own budget (5 s) ends its wait first.
const launchWait = 10 * time.Second

// startingSessions are the ids of the server sessions being launched, each with the
// channel its launch closes as it ends.
type startingSessions struct {
	mu  sync.Mutex
	ids map[string]chan struct{}
}

// begin marks id as launching; the func it returns ends the mark and wakes
// the reports waiting on it, once however often it is called.
func (l *startingSessions) begin(id string) (end func()) {
	ch := make(chan struct{})
	l.mu.Lock()
	if l.ids == nil {
		l.ids = map[string]chan struct{}{}
	}
	l.ids[id] = ch
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			if l.ids[id] == ch {
				delete(l.ids, id)
			}
			l.mu.Unlock()
			close(ch)
		})
	}
}

// under reports the channel of id's launch under way, nil with none.
func (l *startingSessions) under(id string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ids[id]
}

// awaitLaunch waits, while id's launch is under way, for it to end, the
// request to go or launchWait to pass; it reports whether there was a
// launch to wait for.
func (s *Server) awaitLaunch(ctx context.Context, id string) bool {
	ch := s.starting.under(id)
	if ch == nil {
		return false
	}
	s.launchSeam("reportWaits", id)
	t := time.NewTimer(launchWait)
	defer t.Stop()
	select {
	case <-ch:
	case <-ctx.Done():
	case <-t.C:
	}
	return true
}

// launchSeam runs the test's launch hook, when one is set, at stage of the
// launch of id: "started" once its process runs, before its record is
// begun and the registry holds it; "reportWaits" as a report for it waits.
func (s *Server) launchSeam(stage, id string) {
	if h := s.launchHook.Load(); h != nil {
		(*h)(stage, id)
	}
}
