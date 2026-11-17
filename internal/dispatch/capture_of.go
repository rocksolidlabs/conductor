package dispatch

import "context"

// CaptureOf is what a capture was taken of, as its row recorded it at
// the save, deleted or not: its origin's kind (OriginAttempt or
// OriginURL), the run, task and attempt of an attempt's, and its scope
// (the run's crew, repository and project then). A read that admits a
// capture by what it is of (a Brakeman turn's) reads this, never its
// sends' targets, and a delete leaves it as it was.
type CaptureOf struct {
	Kind    string
	Run     string
	Task    string
	Attempt int
	Scope   CaptureScope
	Deleted bool
}

// CaptureOf reads what the capture with the id is of: ErrNotFound when
// there is none (or the database is older than captureSchema).
func (s *Store) CaptureOf(ctx context.Context, id string) (CaptureOf, error) {
	var of CaptureOf
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < captureSchema {
			return ErrNotFound
		}
		c, err := scanCapture(q.QueryRowContext(ctx, "SELECT "+captureColumns+" FROM captures WHERE id = ?", id))
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		o := c.Origin
		of = CaptureOf{Kind: o.Kind, Run: o.Run, Task: o.Task, Attempt: o.Attempt, Scope: c.Scope, Deleted: !c.DeletedAt.IsZero()}
		return nil
	})
	return answer(of, err)
}
