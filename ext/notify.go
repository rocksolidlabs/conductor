package ext

import (
	"context"
	"strings"
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
	Ref     string     `json:"ref,omitempty"`    // the record it is about: decision:dr-<n> (DecisionRef), or ""
	// Recipient is the subject of the one person a notice is meant for, ""
	// for none: such a notice reaches only a sink that is a RecipientSink
	// answering true, never another (a webhook among them).
	Recipient string `json:"recipient,omitempty"`
	// Key is a provider's stable id of one notice, "" for none: a sink that
	// keeps its own outbox drops a notice handed to it twice.
	Key string `json:"key,omitempty"`
}

const (
	TypeProvider     = "provider"
	SinkQueue        = 256 // as a webhook's queue
	DeliveryWait     = 10 * time.Second
	MaxNoticeMessage = 500  // a notice's Message, in bytes, as an attention message
	MaxNoticeURL     = 2048 // a notice's URL, in bytes
	MaxNoticeKey     = 64   // a notice's Key, in bytes of printable ASCII
	// MaxNotificationRef bounds Ref in bytes: "decision:" and a decision's
	// id of at most MaxDecisionRef bytes.
	MaxNotificationRef = len(decisionRefPrefix) + MaxDecisionRef
	// MaxOpenDecisionRefs bounds a page of Host.OpenDecisionRefs.
	MaxOpenDecisionRefs = 256
)

// decisionRefPrefix begins every decision's ref.
const decisionRefPrefix = "decision:"

// DecisionRef is the ref of the durable decision id ("dr-<n>"):
// "decision:dr-<n>", the form Brakeman's refs and the decision inbox
// read. It does not check id; ParseDecisionRef does.
func DecisionRef(id string) string { return decisionRefPrefix + id }

// ParseDecisionRef is the decision's id of ref, false for a ref that is
// not exactly decision:dr-<n>: n of 1 to 18 digits with no leading zero
// (^decision:dr-[1-9][0-9]{0,17}$), so at most MaxNotificationRef bytes.
func ParseDecisionRef(ref string) (id string, ok bool) {
	id, ok = strings.CutPrefix(ref, decisionRefPrefix)
	if !ok {
		return "", false
	}
	digits, ok := strings.CutPrefix(id, "dr-")
	if !ok || len(digits) < 1 || len(digits) > 18 || digits[0] == '0' {
		return "", false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return "", false
		}
	}
	return id, true
}

// Valid checks n as a provider's notice, what Host.Notify is handed: a
// message of one line of 1 to MaxNoticeMessage bytes, a URL of one line of
// at most MaxNoticeURL bytes, and every other text field one line of at
// most MaxNoticeMessage bytes; Ref empty or one ParseDecisionRef takes; a
// recipient of at most MaxSubject bytes and a key of at most MaxNoticeKey
// bytes, each printable ASCII without spaces, or none. Type, Origin and At
// are the server's to set.
func (n Notification) Valid() error {
	const r = "notice"
	if _, ok := ParseDecisionRef(n.Ref); n.Ref != "" && !ok {
		return invalid(r, "ref is not decision:dr-<n> (%d bytes)", len(n.Ref))
	}
	if n.Recipient != "" {
		if err := printable(r, "recipient", n.Recipient, 1, MaxSubject); err != nil {
			return err
		}
	}
	if n.Key != "" {
		if err := printable(r, "key", n.Key, 1, MaxNoticeKey); err != nil {
			return err
		}
	}
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
