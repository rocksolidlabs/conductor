package api

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// The context providers' seam (Program item C4): what an attempt of a
// managed run reads besides its task, and the files the agents touch.
//
// An attempt about to start is asked about once (contextFor, the engine's
// context function): every provider at once, each through the guard within
// ext.ContextWait, so the attempt waits at most that long and never fails
// for a provider late, failing or out of bounds, which adds nothing. What
// they answer goes into the attempt's task files, written through its
// worktree's os.Root, and nowhere else: never the ledger, never a reason a
// task's state changes.
//
// A file entry of a server or a hosted session is offered to every
// provider after the session recorded it (noteTouches, a sink of the event
// hub), its path made absolute by the session's own rule, into each
// provider's queue of ext.TouchQueue (the oldest dropped), never making
// the session wait. A server session's path its deny list refuses is never
// offered: the list is read as the touch is delivered, on the provider's
// own goroutine, so the stat it takes is never the recording goroutine's.

// wireContext gives the engine its context function and, when the server
// holds a context provider, the hub its touch sink, after the run engine's
// (a server with none adds no sink: the hub's are main's, as the notifier
// leaves them; notify.go). New calls it once the engine and the hub exist.
func (s *Server) wireContext() {
	s.runs.SetContext(s.contextFor)
	if len(s.exts.context) > 0 {
		s.events.addSink(s.noteTouches)
	}
}

// contextFor answers the context blocks for the attempt q: every provider
// asked at once (each answer checked by the guard: at most
// ext.MaxContextBlocks, each block ContextBlock.Valid, or none), in
// registration order, each block named by its provider, and, when they
// pass ext.MaxTaskContext together as a task file keeps them, the last
// left out until they fit, the run's log saying how many and whose (in
// more than one note when one would be cut: packNotes).
func (s *Server) contextFor(ctx context.Context, q ext.ContextQuery) []crew.TaskContext {
	providers := s.exts.context
	answers := make([][]ext.ContextBlock, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Go(func() { answers[i], _ = p.Context(ctx, q) })
	}
	wg.Wait()
	var blocks []crew.TaskContext
	size := 0
	for i, bs := range answers {
		for _, b := range bs {
			c := crew.TaskContext{Source: providers[i].Name(), Title: b.Title, Ref: b.Ref, Body: b.Body}
			blocks = append(blocks, c)
			size += crew.TaskContextBytes(c)
		}
	}
	dropped := map[string]int{}
	var order []string
	for size > ext.MaxTaskContext {
		last := blocks[len(blocks)-1]
		blocks = blocks[:len(blocks)-1]
		size -= crew.TaskContextBytes(last)
		if dropped[last.Source] == 0 {
			order = append(order, last.Source)
		}
		dropped[last.Source]++
	}
	if len(order) > 0 {
		n := 0
		var whose []string
		for i := len(order) - 1; i >= 0; i-- { // registration order
			n += dropped[order[i]]
			whose = append(whose, fmt.Sprintf("%s %d", order[i], dropped[order[i]]))
		}
		head := fmt.Sprintf("context for %s #%d: %d blocks left out past %d KiB together", q.Task, q.Attempt, n, ext.MaxTaskContext>>10)
		more := fmt.Sprintf("context for %s #%d: left out also", q.Task, q.Attempt)
		for _, m := range packNotes(head, more, whose) {
			s.runs.Note(q.Run, session.ActivityStatus, m)
		}
	}
	return blocks
}

// packNotes is the run-log notes that say head and then every item of
// whose, "head (a, b)", in as few notes as fit session.MaxAttentionMessage
// (what a note keeps of its message) each, a note after the first saying
// more in place of head. head and more are short (a task id, an attempt and
// a count) and an item is a provider's name and a count, so every note
// holds at least one item and none is cut.
func packNotes(head, more string, whose []string) []string {
	var notes []string
	var b strings.Builder
	b.WriteString(head + " (")
	n := 0 // items in b
	for _, w := range whose {
		if n > 0 && b.Len()+len(", ")+len(w)+len(")") > session.MaxAttentionMessage {
			notes = append(notes, b.String()+")")
			b.Reset()
			b.WriteString(more + " (")
			n = 0
		}
		if n > 0 {
			b.WriteString(", ")
		}
		b.WriteString(w)
		n++
	}
	return append(notes, b.String()+")")
}

// touch is a file a session's agent touched, as a context provider's queue
// holds it: the file, and for a server session whether its deny list
// refuses the path, asked once, by the first provider's delivery, for all.
type touch struct {
	f      ext.TouchedFile
	denied func() bool // nil: nothing to refuse it (a hosted session's)
}

// noteTouches is the hub's sink of file touches: a file entry of a server
// or a hosted session, offered to every context provider. It never waits:
// the session is looked up and the path made absolute here, cheaply, and
// the deny list is read at delivery (touch.denied).
func (s *Server) noteTouches(sessionID string, e session.ActivityEntry, _ session.AttentionState) {
	if e.Type != session.ActivityFile || e.Op == "" || e.Path == "" {
		return
	}
	var t *touch
	for _, g := range s.exts.context {
		if t == nil {
			if t = s.touchOf(sessionID, e); t == nil {
				return
			}
		}
		g.touches.push(*t)
	}
}

// touchOf is the touch e names in the session sessionID: its path made
// absolute as the session's touched index keys it (session.TouchedAbs for
// a server session, signal.HostAbs for a hosted one, whose host may run
// another OS); nil for a session the registry no longer holds.
func (s *Server) touchOf(sessionID string, e session.ActivityEntry) *touch {
	d, ok := s.registry.Get(sessionID)
	if !ok {
		return nil
	}
	info := d.Info()
	at := e.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	t := &touch{f: ext.TouchedFile{Session: contextSessionRef(info), Op: e.Op, Tool: e.Tool, By: e.ByName, At: at}}
	switch drv := d.(type) {
	case *session.Local:
		t.f.Path = session.TouchedAbs(info.Cwd, e.Path)
		path := t.f.Path
		t.denied = sync.OnceValue(func() bool { return drv.Denied(path) })
	case *signal.HostedSession:
		t.f.Path = signal.HostAbs(info.Cwd, e.Path)
	default:
		return nil
	}
	return t
}

// contextSessionRef is the session as a context provider is told of it:
// its id, name and agent, and its crew reference when it is a member's or
// an attempt's.
func contextSessionRef(info session.Info) ext.SessionRef {
	ref := ext.SessionRef{ID: info.ID, Name: info.Name, AgentID: info.AgentID}
	if c := info.Crew; c != nil {
		ref.Run, ref.Task, ref.Attempt = c.RunID, c.Task, c.Attempt
	}
	return ref
}

// deliverTouch hands t to the provider c unless t is a path its server
// session's deny list refuses.
func deliverTouch(ctx context.Context, c ext.ContextProvider, t touch) error {
	if t.denied != nil && t.denied() {
		return nil
	}
	c.NoteTouch(ctx, t.f)
	return nil
}
