package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A ticket's comments are its discussion, a log of the field rules: each
// comment a row of its own with an id, never merged and never lost, its
// body the one field an edit sets (the scalar rule), a delete a
// tombstone. A comment's number on the page, N, is its 1-based place
// among its ticket's comments ordered by (created_at, author, id),
// derived at read and never stored, so a comment a replica brings takes
// its place without renumbering a row; a comment is addressed by its id.
// A tombstone keeps its place, so a delete renumbers nothing.

// MaxCommentPage bounds a page of comments a read gives.
const MaxCommentPage = 200

// commentCreated is a comment's create row's value: what never changes
// (the field rules' immutable class). Its body is a set row of its own
// after it, as every other field of a create is.
type commentCreated struct {
	ID        string    `json:"id"`
	TicketID  string    `json:"ticketId"`
	Author    string    `json:"author"`
	By        string    `json:"by"`
	CreatedAt time.Time `json:"createdAt"`
}

// AddComment comments on the live ticket with the key (comment.add): the
// body Markdown of 1 to MaxCommentBody bytes, the author the operation's
// actor (admin, or agent:<member>) and By its display name. A ticket
// holds at most MaxComments, its tombstones among them (bound), since a
// deleted comment keeps its place. One create row records what never
// changes, then one set row the body. A deleted ticket is not_found; an
// expected revision is the ticket's; an archived project refuses it
// (archived). The receipt carries the comment's id and its rev, 1.
func (s *Store) AddComment(ctx context.Context, op Op, key, body string) (Receipt, error) {
	args := struct {
		Key  string `json:"key"`
		Body string `json:"body"`
	}{key, body}
	return s.Apply(ctx, op, "comment.add", args, func(t *tx) error { return t.addComment(key, body) })
}

// ErrNoRunTicket is AddRunComment's answer when no live ticket's live run
// link names the run: nothing is written, no receipt among it, so the
// same id sent once a ticket holds the run comments on it (the API's 404
// no_ticket).
var ErrNoRunTicket = errors.New("dispatch: no live ticket is linked to the run")

// AddRunComment comments, as AddComment does, on the live ticket the
// run's live run link names (RunTicket's live answer), found inside the
// operation's transaction, after its receipt is looked up: an id answered
// before answers its receipt (a repeat, or op_conflict for other
// content, its failed receipts' as well as its done ones') wherever the
// run's link is now, since the content is the run and the body and never
// the ticket; a new id goes to the ticket that holds the run when it
// commits, so no link moved or removed between a lookup and the write can
// send it elsewhere. No live ticket holding the run is ErrNoRunTicket,
// with nothing written. The ticket a done operation commented on is
// OpTicket's, the first row of its log naming it, so a repeat names it
// too, its receipt pruned or its ticket deleted since: a read after the
// commit, which a caller makes as it reads the comment back, so that a
// failure of it (the operation's deadline spent) loses no receipt.
func (s *Store) AddRunComment(ctx context.Context, op Op, run, body string) (Receipt, error) {
	args := struct {
		Run  string `json:"run"`
		Body string `json:"body"`
	}{run, body}
	return s.Apply(ctx, op, "comment.add", args, func(t *tx) error {
		tk, found, err := runTicket(t.ctx, t.tx, run)
		if err != nil {
			return err
		}
		if !found || !tk.DeletedAt.IsZero() {
			return ErrNoRunTicket
		}
		return t.addComment(tk.Key, body)
	})
}

// addComment is comment.add's change: the comment on the live ticket
// with the key, by the operation's actor and display name.
func (t *tx) addComment(key, body string) error {
	tk, _, err := t.ticket(key, false)
	if err != nil {
		return err
	}
	if err := checkField("comment.body", "the comment", body); err != nil {
		return err
	}
	var n int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM comments WHERE ticket_id = ?", tk.ID).Scan(&n); err != nil {
		return err
	}
	if err := checkCount("comments", n+1, MaxComments); err != nil {
		return err
	}
	id := newID()
	if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO comments (id, ticket_id, author, by, body, created_at) VALUES (?, ?, ?, ?, '', ?)",
		id, tk.ID, t.op.Actor, t.op.By, stamp(t.now)); err != nil {
		return err
	}
	t.created(id, "")
	value, err := encodeValue(commentCreated{ID: id, TicketID: tk.ID, Author: t.op.Actor, By: t.op.By, CreatedAt: t.now})
	if err != nil {
		return err
	}
	base := ChangeRow{Entity: EntityComment, EntityID: id, TicketID: tk.ID, ProjectID: tk.ProjectID}
	create := base
	create.Op, create.Value = OpCreate, value
	if err := t.record(create); err != nil {
		return err
	}
	return t.setScalars("comments", base, []fieldValue{{"body", ""}}, []fieldValue{{"body", body}})
}

// EditComment replaces the body of the comment with the id on the live
// ticket with the key (comment.edit): a set row of the body, its prior
// the body it replaces, kept in the history, and one of edited_at. A
// body the comment holds already changes nothing and is refused invalid.
// The comment missing, deleted or another ticket's is not_found; an
// expected revision is the comment's.
func (s *Store) EditComment(ctx context.Context, op Op, key, id, body string) (Receipt, error) {
	args := struct {
		Key  string `json:"key"`
		ID   string `json:"id"`
		Body string `json:"body"`
	}{key, id, body}
	return s.Apply(ctx, op, "comment.edit", args, func(t *tx) error {
		c, err := t.comment(key, id)
		if err != nil {
			return err
		}
		if err := checkField("comment.body", "the comment", body); err != nil {
			return err
		}
		if body == c.Body {
			return refuse(RefusalInvalid, "the comment holds that body already")
		}
		before, err := t.scalars("comments", c.ID, []string{"body", "edited_at"})
		if err != nil {
			return err
		}
		return t.setScalars("comments", c.base, before, []fieldValue{{"body", body}, {"edited_at", stamp(t.now)}})
	})
}

// DeleteComment deletes the comment with the id on the live ticket with
// the key (comment.delete): a tombstone, its deleted_at set, its row and
// its history kept, and its place among the ticket's comments with it.
// The comment missing, deleted already or another ticket's is not_found;
// an expected revision is the comment's.
func (s *Store) DeleteComment(ctx context.Context, op Op, key, id string) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}{key, id}
	return s.Apply(ctx, op, "comment.delete", args, func(t *tx) error {
		c, err := t.comment(key, id)
		if err != nil {
			return err
		}
		return t.setScalars("comments", c.base, []fieldValue{{"deleted_at", ""}}, []fieldValue{{"deleted_at", stamp(t.now)}})
	})
}

// opComment is a comment as an operation on it finds it, with the base of
// its change rows.
type opComment struct {
	Comment
	base ChangeRow
}

// comment reads the live comment with the id of the live ticket with the
// key inside the operation, in the order the store's rules ask: the
// ticket not_found when missing or deleted; the comment not_found when
// missing, another ticket's or deleted; then the revision expected, the
// comment's; then archived when the ticket's project is.
func (t *tx) comment(key, id string) (opComment, error) {
	tk, err := t.ticketRow(key, false)
	if err != nil {
		return opComment{}, err
	}
	c, err := scanComment(t.tx.QueryRowContext(t.ctx, "SELECT "+commentColumns+", 0 FROM comments WHERE id = ?", id))
	switch {
	case isNoRows(err):
		return opComment{}, refuse(RefusalNotFound, "no comment %s", id)
	case err != nil:
		return opComment{}, err
	case c.TicketID != tk.ID:
		return opComment{}, refuse(RefusalNotFound, "the comment %s is not %s's", id, tk.Key)
	case !c.DeletedAt.IsZero():
		return opComment{}, refuse(RefusalNotFound, "the comment %s is deleted", id)
	}
	if err := t.expect(c.Rev); err != nil {
		return opComment{}, err
	}
	if _, err := t.open(tk); err != nil {
		return opComment{}, err
	}
	return opComment{c, ChangeRow{Entity: EntityComment, EntityID: c.ID, TicketID: tk.ID, ProjectID: tk.ProjectID}}, nil
}

// Comments reads the comments of the ticket with the key, deleted or not,
// each with its number: those numbered after after, in their order, at
// most limit (1 to MaxCommentPage). Tombstones are among them, their
// DeletedAt set, so the numbers have no gap. ErrNotFound when there is no
// such ticket.
func (s *Store) Comments(ctx context.Context, key string, after, limit int) ([]Comment, error) {
	if limit < 1 || limit > MaxCommentPage || after < 0 {
		return nil, fmt.Errorf("dispatch: a page of comments is 1 to %d after a number of 0 or more, not %d after %d", MaxCommentPage, limit, after)
	}
	var out []Comment
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = commentsOf(ctx, q, tk.ID, after, limit)
		return err
	})
	return out, err
}

// Comment reads the comment with the id of the ticket with the key, with
// its number, deleted or not. ErrNotFound when the ticket has no such
// comment.
func (s *Store) Comment(ctx context.Context, key, id string) (Comment, error) {
	var out Comment
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = scanComment(q.QueryRowContext(ctx, commentsNumbered("SELECT id, n FROM numbered WHERE id = ?"), tk.ID, id))
		if isNoRows(err) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

// numberedComments is the numbers of a ticket's comments, each comment's
// id and its number n, the ticket's id its one argument. It selects no
// body: a ticket may hold MaxComments comments of MaxCommentBody bytes,
// and a window that carried them would sort them all to number them.
const numberedComments = "SELECT id, ROW_NUMBER() OVER (ORDER BY created_at, author, id) AS n FROM comments WHERE ticket_id = ?"

// commentsNumbered is the statement of a read of the comments pick
// chooses, each whole with its number, in their order: pick selects id
// and n from numbered, a ticket's comments numbered (numberedComments),
// its arguments after the ticket's id. Only the comments it chooses are
// read with their bodies (BenchmarkTicketRead).
func commentsNumbered(pick string) string {
	return "WITH numbered AS (" + numberedComments + "), picked AS (" + pick + ") SELECT " + pickedColumns +
		", picked.n FROM picked JOIN comments c ON c.id = picked.id ORDER BY picked.n"
}

// pickedColumns is commentColumns of the comments c, as commentsNumbered
// reads them.
var pickedColumns = "c." + strings.ReplaceAll(commentColumns, ", ", ", c.")

// commentsOf reads the comments of the ticket with the id numbered after
// after, in their order, at most limit, inside q's transaction.
func commentsOf(ctx context.Context, q queryer, ticketID string, after, limit int) ([]Comment, error) {
	rows, err := q.QueryContext(ctx, commentsNumbered("SELECT id, n FROM numbered WHERE n > ? ORDER BY n LIMIT ?"), ticketID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// commentColumns is a comment's row as scanComment reads it, its number
// after them.
const commentColumns = "id, ticket_id, author, by, body, created_at, edited_at, deleted_at, rev"

// scanComment reads a comment's row and its number.
func scanComment(row interface{ Scan(...any) error }) (Comment, error) {
	var c Comment
	var created, edited, deleted string
	if err := row.Scan(&c.ID, &c.TicketID, &c.Author, &c.By, &c.Body, &created, &edited, &deleted, &c.Rev, &c.N); err != nil {
		return Comment{}, err
	}
	c.CreatedAt, c.EditedAt, c.DeletedAt = unstamp(created), unstamp(edited), unstamp(deleted)
	return c, nil
}
