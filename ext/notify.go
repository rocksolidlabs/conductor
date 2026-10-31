package ext

import (
	"context"
	"time"
)

// NotificationSink takes the sessions' activity and delivers it elsewhere
// (mobile push, for one). The free default is no sink: the browsers' event
// stream and the webhooks are where activity goes.
type NotificationSink interface {
	Provider
	// Events names what the sink takes: an event name (needs_input, done,
	// exit_nonzero, error, …) or an entry's own type (file, join, input, …,
	// or TypeProvider); nil takes every session entry. A provider's notice
	// reaches a sink only when TypeProvider is listed, and a provider with
	// such a sink cannot send notices (Host.Notify refuses): no provider
	// both takes and sends them, so notices never loop. Read once, at start.
	Events() []string
	// Notify delivers one notification. Called in order, from one goroutine
	// of the sink's own, with a context that ends after DeliveryWait.
	Notify(ctx context.Context, n Notification) error
}

// Notification is an activity entry of a session as the Events page and
// the webhooks type it, or a provider's notice (Host.Notify).
type Notification struct {
	Event   string     `json:"event,omitempty"` // the Events page's name; "" for an entry that has none
	Type    string     `json:"type"`            // the entry's own type, or TypeProvider
	At      time.Time  `json:"at"`
	Session SessionRef `json:"session"`
	State   string     `json:"state,omitempty"` // an attention entry's state
	Message string     `json:"message,omitempty"`
	URL     string     `json:"url,omitempty"`
	To      string     `json:"to,omitempty"`
	Tool    string     `json:"tool,omitempty"`
	Op      string     `json:"op,omitempty"`
	Path    string     `json:"path,omitempty"`
	By      string     `json:"by,omitempty"`
	ByName  string     `json:"byName,omitempty"`
	Origin  string     `json:"origin,omitempty"` // the provider that sent a notice
}

const (
	TypeProvider     = "provider"
	SinkQueue        = 256 // as a webhook's queue
	DeliveryWait     = 10 * time.Second
	MaxNoticeMessage = 500  // a notice's Message, in bytes, as an attention message
	MaxNoticeURL     = 2048 // a notice's URL, in bytes
)

// Valid checks n as a provider's notice, what Host.Notify is handed: a
// message of one line of 1 to MaxNoticeMessage bytes, a URL of one line of
// at most MaxNoticeURL bytes, and every other text field one line of at
// most MaxNoticeMessage bytes. Type, Origin and At are the server's to set.
func (n Notification) Valid() error {
	const r = "notice"
	if err := line(r, "message", n.Message, MaxNoticeMessage, true); err != nil {
		return err
	}
	if err := line(r, "url", n.URL, MaxNoticeURL, false); err != nil {
		return err
	}
	for _, f := range []struct{ field, s string }{
		{"event", n.Event}, {"type", n.Type}, {"state", n.State}, {"to", n.To},
		{"tool", n.Tool}, {"op", n.Op}, {"path", n.Path}, {"by", n.By},
		{"byName", n.ByName}, {"origin", n.Origin},
		{"session.id", n.Session.ID}, {"session.name", n.Session.Name},
		{"session.agentId", n.Session.AgentID}, {"session.run", n.Session.Run},
		{"session.task", n.Session.Task},
	} {
		if err := line(r, f.field, f.s, MaxNoticeMessage, false); err != nil {
			return err
		}
	}
	return nil
}
