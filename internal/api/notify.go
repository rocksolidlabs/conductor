package api

import (
	"context"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Notifications: the notifier is the event hub's one sink for everything
// that takes the sessions' activity elsewhere. Its sinks are, in order, the
// webhooks of the config, in config order, then the providers' notification
// sinks (ext.NotificationSink), in registration order; New adds it to the
// hub where the webhooks' sink always was, so the hub's sinks stay as they
// were: the notifier, then the run engine's OnActivity. The notifier types
// each entry once (eventTypeOf), looks its session up once, and only when
// a sink wants the entry, builds one ext.Notification from them and queues
// it for every sink that takes it: a sink whose Events is nil takes every
// session entry, any other the entries whose event name or own type it
// lists, the rule a webhook applies (webhook.eventFor). A provider's notice
// (Host.Notify) reaches only the sinks that list ext.TypeProvider, never a
// webhook (a webhook cannot list it: config.ValidWebhookEvent knows no such
// event), and a provider with such a sink may not send one, so no notice is
// handed back round. Every sink has a queue of its own (an extQueue of
// ext.SinkQueue), filled without waiting, its oldest dropped when it is
// full, so no sink delays the hub, a session, the engine or another sink.
// A server with no webhook and no sink adds no notifier: the free build's
// hub is as it was.

// notifier hands the hub's entries and the providers' notices to the
// webhooks and the providers' sinks.
type notifier struct {
	registry *session.Registry
	hooks    *webhookSender // the webhooks, first; its ctx ends both kinds' queueing
	sinks    []*guardSink   // then the providers' sinks
}

// startNotifier starts the webhooks of hooks and makes the notifier, with
// the providers' sinks the server holds behind the guard after them, a sink
// of the event hub when it has a sink at all. New calls it where the
// webhooks' sink was added before, ahead of the run engine's.
func (s *Server) startNotifier(hooks []config.Webhook) *webhookSender {
	ws := startWebhooks(hooks, s.log)
	nt := &notifier{registry: s.registry, hooks: ws}
	for _, sink := range s.exts.sinks {
		if g, ok := sink.(*guardSink); ok {
			nt.sinks = append(nt.sinks, g)
		}
	}
	s.notifier = nt
	if len(ws.hooks) > 0 || len(nt.sinks) > 0 {
		s.events.addSink(nt.offer)
	}
	return ws
}

// offer is the notifier's activity sink. It runs on the goroutine that
// recorded e, so it never waits: it types e once, looks the session up and
// builds the notification only once a sink wants it, builds the webhooks'
// body once for every webhook that wants it, and queues. Once the webhooks
// are closed (Shutdown) it queues nothing.
func (nt *notifier) offer(sessionID string, e session.ActivityEntry, state session.AttentionState) {
	if nt.hooks.ctx.Err() != nil {
		return
	}
	typ := eventTypeOf(e, state)
	var (
		n     ext.Notification
		built bool
	)
	note := func() ext.Notification {
		if !built {
			n, built = nt.notification(sessionID, e, typ, state), true
		}
		return n
	}
	var body []byte
	for _, w := range nt.hooks.hooks {
		event := w.eventFor(typ, e.Type)
		if event == "" {
			continue
		}
		if body == nil {
			b, err := webhookBody(note())
			if err != nil {
				break
			}
			body = b
		}
		w.queue.push(webhookDelivery{event: event, body: body})
	}
	for _, g := range nt.sinks {
		if g.mayTake(typ, e.Type) {
			g.queue.push(note())
		}
	}
}

// notification is the notification of e, an entry of the session
// sessionID that the Events page types typ ("" for none), with the state
// an attention entry records: the session's name, agent and crew
// reference as the registry holds them now (none for a session it does
// not hold), and every field of the entry.
func (nt *notifier) notification(sessionID string, e session.ActivityEntry, typ string, state session.AttentionState) ext.Notification {
	ref := ext.SessionRef{ID: sessionID}
	if d, ok := nt.registry.Get(sessionID); ok {
		info := d.Info()
		ref.Name, ref.AgentID = info.Name, info.AgentID
		if c := info.Crew; c != nil {
			ref.Run, ref.Task, ref.Attempt = c.RunID, c.Task, c.Attempt
		}
	}
	return ext.Notification{
		Event:   typ,
		Type:    e.Type,
		At:      e.At,
		Session: ref,
		State:   string(eventState(e, state)),
		Message: e.Message,
		URL:     e.URL,
		To:      e.To,
		Tool:    e.Tool,
		Op:      e.Op,
		Path:    e.Path,
		By:      e.By,
		ByName:  e.ByName,
	}
}

// notice queues n, a provider's notice (Host.Notify has typed it
// ext.TypeProvider), for every provider sink that may take it, and reports
// whether one may. No webhook is handed one.
func (nt *notifier) notice(n ext.Notification) bool {
	if nt.hooks.ctx.Err() != nil {
		return false
	}
	taken := false
	for _, g := range nt.sinks {
		if g.mayTake(n.Event, n.Type) {
			g.queue.push(n)
			taken = true
		}
	}
	return taken
}

// deliverNotice hands a provider's notice to the sinks that list
// ext.TypeProvider (notifier.notice); with none, it is logged at debug.
func (s *Server) deliverNotice(n ext.Notification) {
	if s.notifier == nil || !s.notifier.notice(n) {
		s.log.Debug("provider notice: no sink takes it", "provider", n.Origin, "bytes", len(n.Message))
	}
}

// sinkFilter is what a sink takes, as its Events said, read once.
type sinkFilter struct {
	all   bool // Events was nil: every session entry
	names map[string]bool
}

// newSinkFilter is the filter of a sink whose Events answered evs.
func newSinkFilter(evs []string) *sinkFilter {
	f := &sinkFilter{all: evs == nil, names: map[string]bool{}}
	for _, ev := range evs {
		f.names[ev] = true
	}
	return f
}

// takes reports whether the sink takes an entry of type own that the
// Events page types event ("" for none): a provider's notice (own
// ext.TypeProvider) only when it lists ext.TypeProvider, a session entry
// when it takes every entry, or lists event or own.
func (f *sinkFilter) takes(event, own string) bool {
	if own == ext.TypeProvider {
		return f.names[ext.TypeProvider]
	}
	return f.all || (event != "" && f.names[event]) || f.names[own]
}

// mayTake reports whether the notification of an entry of type own, typed
// event, is queued for the sink: when the sink takes it, or while its
// Events has not been read yet (the delivery then decides, deliver). It
// never asks the provider, so it never waits.
func (g *guardSink) mayTake(event, own string) bool {
	f := g.known.Load()
	return f == nil || f.takes(event, own)
}

// deliver is the sink's queue's delivery (a direct one, called with the
// drain's context): n, when the sink takes it, through extDeliver. A
// notification queued before the sink's Events was read is decided here,
// on the queue's own goroutine, which reads Events (once, eventsWithin,
// within ext.StartWait) if nothing has yet; one the sink does not take is
// let go, delivered to no one. Events is read before the delivery's
// context starts, so the time it takes never comes out of Notify's
// ext.DeliveryWait.
func (g *guardSink) deliver(ctx context.Context, n ext.Notification) error {
	f := g.known.Load()
	if f == nil {
		g.eventsWithin(ctx)
		f = g.known.Load()
	}
	if !f.takes(n.Event, n.Type) {
		return nil
	}
	return extDeliver(ctx, g.s, ext.KindNotifications, g.name, func(ctx context.Context) error { return g.p.Notify(ctx, n) })
}
