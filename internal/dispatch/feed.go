package dispatch

import (
	"context"
	"time"
)

// The change feed: every project's changes in the order they were made,
// for a reader that follows the store as a whole (Brakeman's changes,
// docs/plans/brakeman.md, PR-4). A ticket's updatedAt does not move for a
// comment added to it or edited, so a reader that wants what changed reads
// the changes table by seq, as History does for one ticket.

// MaxFeedPage bounds a page of the change feed, in changes.
const MaxFeedPage = 5000

// FeedChange is one row of the change feed: a ChangeRow without its value
// and prior text (a description's whole), with the keys of the ticket and
// the project it is of instead of their ids, "" for neither.
type FeedChange struct {
	Seq      int64     `json:"seq"`
	Entity   string    `json:"entity"`
	EntityID string    `json:"entityId"`
	Ticket   string    `json:"ticket,omitempty"`
	Project  string    `json:"project,omitempty"`
	Field    string    `json:"field,omitempty"`
	Op       string    `json:"op"`
	At       time.Time `json:"at"`
	Actor    string    `json:"actor"`
	By       string    `json:"by,omitempty"`
}

// ChangesAfter reads the changes of every project after the seq after (0
// for the first), at most limit of them (1 to MaxFeedPage), in seq order:
// a comment added to or edited on an old ticket among them. ErrBadQuery
// for a limit or an after out of range.
func (s *Store) ChangesAfter(ctx context.Context, after int64, limit int) ([]FeedChange, error) {
	if limit < 1 || limit > MaxFeedPage || after < 0 {
		return nil, badQuery("a page of changes is 1 to %d after a seq of 0 or more, not %d after %d", MaxFeedPage, limit, after)
	}
	out := []FeedChange{}
	err := s.read(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `SELECT c.seq, c.entity, c.entity_id, COALESCE(t.key, ''), COALESCE(p.key, ''), c.field, c.op, c.at, c.actor, c.by
			FROM changes c LEFT JOIN tickets t ON t.id = c.ticket_id LEFT JOIN projects p ON p.id = c.project_id
			WHERE c.seq > ? ORDER BY c.seq LIMIT ?`, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c FeedChange
			var at string
			if err := rows.Scan(&c.Seq, &c.Entity, &c.EntityID, &c.Ticket, &c.Project, &c.Field, &c.Op, &at, &c.Actor, &c.By); err != nil {
				return err
			}
			c.At = unstamp(at)
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
