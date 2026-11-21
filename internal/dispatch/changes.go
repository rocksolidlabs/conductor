package dispatch

import (
	"context"
)

// Reading the log whole, for a sync provider (docs/plans/sync-seams.md,
// OS-1a): every row with its value, its prior, its origin and its
// verdicts, in seq order, local and remote alike.

// MaxChangesPage bounds a page of ChangesFrom, in rows (ext's
// MaxDispatchBatch); MaxChangesBytes the rows' values and priors together.
const (
	MaxChangesPage  = 1000
	MaxChangesBytes = 4 << 20
)

// changeColumns is a change row whole as scanChange reads it.
const changeColumns = `seq, entity, entity_id, ticket_id, project_id, field, op, value, prior, at, actor, by, op_id, n,
	origin, recovered_by, remote, output, prior_output`

// scanChange reads a row of changeColumns.
func scanChange(row interface{ Scan(...any) error }) (ChangeRow, error) {
	var c ChangeRow
	var at string
	var remote int
	err := row.Scan(&c.Seq, &c.Entity, &c.EntityID, &c.TicketID, &c.ProjectID, &c.Field, &c.Op, &c.Value, &c.Prior, &at, &c.Actor, &c.By,
		&c.OpID, &c.N, &c.Origin, &c.RecoveredBy, &remote, &c.Output, &c.PriorOutput)
	c.At, c.Remote = unstamp(at), remote == 1
	return c, err
}

// readChanges reads the rows a query of changeColumns gives.
func readChanges(ctx context.Context, q queryer, query string, args ...any) ([]ChangeRow, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+changeColumns+" FROM changes "+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChangeRow
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChangesFrom reads the rows that replicate (Replicates) after the
// position after (0 for the first), at most limit (1 to MaxChangesPage)
// and MaxChangesBytes of values and priors, in seq order, local and
// remote alike, and the position to read from next, past the rows it
// skipped. Each row as another replica takes it: its actor raw and its
// origin beside it (a row of another replica's, stored qualified, given
// back as its origin wrote it), a remove's prior naming each tag with its
// actor canonical (Canon), and a plan's create naming its view
// (ViewDigest). A database before version 8 has no origins and is not
// read. ErrBadQuery for a limit or an after out of range.
func (s *Store) ChangesFrom(ctx context.Context, after int64, limit int) ([]ChangeRow, int64, error) {
	if limit < 1 || limit > MaxChangesPage || after < 0 {
		return nil, 0, badQuery("a page of changes is 1 to %d after a seq of 0 or more, not %d after %d", MaxChangesPage, limit, after)
	}
	out := []ChangeRow{}
	next := after
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < syncSchema {
			return nil
		}
		rows, err := readChanges(ctx, q, "WHERE seq > ? ORDER BY seq LIMIT ?", after, limit)
		if err != nil {
			return err
		}
		size := 0
		for _, c := range rows {
			if !Replicates(c) {
				next = c.Seq
				continue
			}
			size += len(c.Value) + len(c.Prior)
			if len(out) > 0 && size > MaxChangesBytes {
				break
			}
			c.Actor, _ = rawActor(c.Actor)
			if c.Op == OpRemove {
				c.Prior = EncodeTags(priorTags(c.Prior, c.Origin))
			}
			if c.Entity == EntityPlan && c.Op == OpCreate {
				if err := q.QueryRowContext(ctx, "SELECT view_digest FROM plans WHERE id = ?", c.EntityID).Scan(&c.ViewDigest); err != nil && !isNoRows(err) {
					return err
				}
			}
			out = append(out, c)
			next = c.Seq
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, next, nil
}

// RevisionView is a revision's view by its view digest (planview.go):
// what of the revision another machine is given. ErrNotFound for none.
func (s *Store) RevisionView(ctx context.Context, viewDigest string) ([]byte, error) {
	var view []byte
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < syncSchema {
			return ErrNotFound
		}
		err := q.QueryRowContext(ctx, "SELECT view FROM plan_views WHERE view_digest = ?", viewDigest).Scan(&view)
		if isNoRows(err) {
			return ErrNotFound
		}
		return err
	})
	return view, err
}
