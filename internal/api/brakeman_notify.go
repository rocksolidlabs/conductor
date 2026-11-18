package api

import (
	"encoding/json"
	"fmt"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/config"
)

// A background brief that holds a decision notifies. Every other turn of
// the background role stays silent (its session is quiet, notify.go), and
// so does every brief a person asked for: only as a new brief of the
// role's own wake request is kept, holding at least one decision card that
// none of the role's earlier background briefs held, the server tells the
// operator's open pages once (the brakeman_needs_you event, which a page
// turns into a browser notification and the chime under the person's
// switches) and queues one notification for the webhooks that list
// brakeman_needs_you. A person is told of a decision once, not again each
// time a later brief repeats it; nothing is kept for it, the earlier
// briefs are read. The words are Conductor's own and fixed: the request's
// id, a count and a path, never a word of the brief (its title, its
// reading, a decision's question or what it is about), so nothing an
// agent, a ticket or a repository wrote reaches a notification. No
// provider's sink is handed one.

const (
	// maxBrakemanNeedsYou bounds a brakeman_needs_you event's data: the
	// longest, an id of 18 digits and 100 decisions, is 46 bytes.
	maxBrakemanNeedsYou = 64
	// maxNeedsYouMessage bounds the webhook's message, one line.
	maxNeedsYouMessage = 96
	// maxNeedsYouURL bounds the webhook's url, a path on this server.
	maxNeedsYouURL = 40
	// needsYouEarlier is how many of the role's background briefs kept
	// before a new one its decisions are held against.
	needsYouEarlier = 20
	// needsYouLook bounds the requests looked through for them, newest
	// first, before the new one.
	needsYouLook = 2 * brakeman.MaxPage
)

// The words, Conductor's own; the app's tests read them from here.
const (
	needsYouOne  = "Brakeman's background brief %s holds 1 decision"
	needsYouMany = "Brakeman's background brief %s holds %d decisions"
	needsYouPath = "/brakeman?request="
)

// briefNeedsYou is how many decision cards a kept brief holds when it is
// one the server may notify of: a brief of the background role's own wake
// request, as the keep left it (briefed); 0 for any other.
func briefNeedsYou(req brakeman.Request, d brakeman.Digest) int {
	if req.Kind != brakeman.KindWake || req.State != brakeman.StateBriefed {
		return 0
	}
	return d.Counts.Decisions
}

// needsYouMessage is the webhook's message, and the page's title, for a
// brief id holding n decisions.
func needsYouMessage(id string, n int) string {
	if n == 1 {
		return fmt.Sprintf(needsYouOne, id)
	}
	return fmt.Sprintf(needsYouMany, id, n)
}

// briefNotification is the webhooks' notification of a background brief
// with n decisions: the fixed words, the page's path, no session.
func briefNotification(req brakeman.Request, n int) ext.Notification {
	return ext.Notification{Event: config.EventBrakemanNeedsYou, Type: config.EventBrakemanNeedsYou, At: req.BriefAt, State: brakeman.StateBriefed,
		Message: needsYouMessage(req.ID, n), URL: needsYouPath + req.ID}
}

// decisionKey names a decision card: what it is about at the revision it
// was written at. A card about the same subject at another revision is a
// new decision: the subject moved since.
func decisionKey(x *brakeman.Decision) string {
	return x.About.String() + "@" + x.SubjectRevision
}

// briefKept is told of a new brief, kept by its turn's submission (the one
// place a brief is kept): kept as the keep answered it and b the brief.
// When the brief notifies (briefNeedsYou) and holds a decision none of
// the role's earlier background briefs held (heldBefore), it sends the
// pages' event and queues the webhooks' notification, once, before the
// submission is answered. A list of the earlier requests that cannot be
// read notifies no one (an earlier brief that does not read is passed
// over, heldBefore): nothing is kept to send later, and a restart sends
// nothing.
func (d *brakemanDesk) briefKept(kept brakeman.Request, b brakeman.Brief) {
	n := briefNeedsYou(kept, brakeman.DigestOf(b))
	if n < 1 || n > brakeman.MaxItems {
		return
	}
	held, err := d.heldBefore(kept.ID)
	if err != nil {
		d.s.log.Warn("Brakeman's earlier requests could not be listed: the background brief notifies no one", "request", kept.ID, "err", err)
		return
	}
	fresh := false
	for _, it := range b.Items {
		if it.Kind == brakeman.ItemDecision && it.Decision != nil && !held[decisionKey(it.Decision)] {
			fresh = true
			break
		}
	}
	if !fresh {
		return
	}
	d.s.events.brakemanNeedsYou(kept.ID, n)
	if nt := d.s.notifier; nt != nil {
		nt.send(briefNotification(kept, n), false)
	}
}

// heldBefore is the decisions the role's background briefs kept before
// the request id held: of its newest needsYouEarlier with a brief, among
// the needsYouLook requests before it, its items alone (never its
// resolutions). Only a list that cannot be read is an error: a brief
// whose digest counts no decision is not read, and one whose digest or
// brief does not read (gone since it was listed, or damaged) is passed
// over as holding none, so a decision only it held is told again.
func (d *brakemanDesk) heldBefore(id string) (map[string]bool, error) {
	held := map[string]bool{}
	briefs, looked, after := 0, 0, id
	for looked < needsYouLook && briefs < needsYouEarlier {
		page, next, err := d.st.List(after, brakeman.MaxPage)
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			looked++
			if r.Kind != brakeman.KindWake || !r.HasBrief {
				continue
			}
			if briefs++; briefs > needsYouEarlier {
				break
			}
			if dg, err := d.st.Digest(r.ID); err != nil || dg == nil || dg.Counts.Decisions == 0 {
				continue
			}
			earlier, err := d.st.BriefAlone(r.ID)
			if err != nil {
				continue
			}
			for _, it := range earlier.Items {
				if it.Kind == brakeman.ItemDecision && it.Decision != nil {
					held[decisionKey(it.Decision)] = true
				}
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	return held, nil
}

// brakemanNeedsYouEvent is the data of a brakeman_needs_you event: the
// request's id and how many decisions its brief holds. It names no
// person and no word of the brief.
type brakemanNeedsYouEvent struct {
	ID        string `json:"id"`
	Decisions int    `json:"decisions"`
}

// brakemanNeedsYou queues a brakeman_needs_you event for every client, at
// most maxBrakemanNeedsYou bytes, for a count of 1 to brakeman.MaxItems,
// as droppable as brakeman_progress: a client past activityQueueLimit
// misses it and keeps its stream (the page alerts at most once, and only
// a page connected then). It bumps no count and reaches no sink.
func (h *eventHub) brakemanNeedsYou(id string, decisions int) {
	if decisions < 1 || decisions > brakeman.MaxItems {
		return
	}
	b, err := json.Marshal(brakemanNeedsYouEvent{ID: id, Decisions: decisions})
	if err != nil || len(b) > maxBrakemanNeedsYou {
		return
	}
	msg := []byte("event: brakeman_needs_you\ndata: " + string(b) + "\n\n")
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		if len(ch) >= activityQueueLimit {
			continue
		}
		select {
		case ch <- msg:
		default:
		}
	}
}
