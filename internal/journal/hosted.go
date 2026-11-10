package journal

import (
	"errors"
	"time"
)

// A hosted session's record (docs/plans/history.md, PR-12): what its host
// reports of it (Where, the agent's version, its repository and start
// commit, StartFromHost; its end commit with its end, Ending.Commit), and
// nothing of the server's machine. A hosted session's id comes back, the
// same after its host reconnects past the hub's grace and after its server
// restarts (signal.HostedID), so one record may have several instances:
// BeginHosted takes up the record its host's earlier registration left.

// StartFromHost says a record's start commit is the one its host read
// before its process started (Record.StartFrom).
const StartFromHost = "host"

// Reopening is the status line a record taken up again by its host keeps.
const Reopening = "taken up again by its host"

// lostEnding is how a record found open with no instance is ended: host
// away for a hosted one (its host is not connected here), lost for any
// other.
func lostEnding(r *Record) string {
	if r.Host.Hosted {
		return EndingHostAway
	}
	return EndingLost
}

// BeginHosted keeps the record of a hosted session as its hub adds it
// (Host.Hosted, its host instance's hash in Host.Owner), and answers its
// instance. An id with no record is begun as Begin begins one. A record of
// the id that is a hosted one of the same Owner, let go without an
// observed end (host away, lost: never ended, ErrExists for one that was)
// and held by no live instance, is taken up again: a new instance, its end
// cleared, Reopened counted and a status line saying so (Reopening), its
// entries, files, tools and head kept, what r reports filling only what it
// lacks (its host reads them once, before its process starts); written
// before BeginHosted returns, as Begin writes. Any other record of the id
// is refused (ErrExists): a server session's, another host's, one with a
// live instance, one of a later version.
func (j *Journal) BeginHosted(r Record) (Instance, error) {
	if !r.Host.Hosted || r.Host.Owner == "" {
		return j.Begin(r)
	}
	if !idPattern.MatchString(r.ID) {
		return Instance{}, ErrBadID
	}
	j.wmu.Lock()
	x, err := j.takeLocked(r.ID)
	if errors.Is(err, ErrNotFound) {
		j.wmu.Unlock()
		return j.Begin(r)
	}
	defer j.wmu.Unlock()
	if errors.Is(err, ErrNewer) {
		return Instance{}, ErrExists
	}
	if err != nil {
		return Instance{}, err
	}
	j.mu.Lock()
	o := &x.r
	if !o.Host.Hosted || o.Host.Owner != r.Host.Owner || !o.EndedAt.IsZero() || o.open() || (x.inst != 0 && !x.released) || x.needEnd {
		j.mu.Unlock()
		j.log.Warn("a hosted session's record was not taken up again: its id has another", "session", r.ID)
		return Instance{}, ErrExists
	}
	x.inst = j.newInst()
	x.released, x.state, x.endedAt = false, nil, time.Time{}
	o.LostAt, o.Ending = time.Time{}, ""
	o.Reopened++
	if o.Where == "" {
		o.Where = r.Where
	}
	if o.Agent.Version == "" {
		o.Agent.Version = r.Agent.Version
	}
	if o.Repo == "" && o.Start == "" {
		o.Repo, o.RepoCommon, o.Start, o.StartFrom = r.Repo, r.RepoCommon, r.Start, r.StartFrom
	}
	fitRecord(o)
	j.seq++
	o.Entries = insertEntry(o.Entries, Entry{Seq: j.seq, At: j.o.Now().UTC(), Kind: KindStatus, Message: Reopening})
	x.dirty = true
	j.index[r.ID] = j.row(x)
	in := Instance{id: r.ID, n: x.inst}
	j.mu.Unlock()
	if err := j.write(x); err != nil {
		j.log.Warn("a hosted session's record was not written as it was taken up again; it is tried again", "session", r.ID, "err", err)
	}
	return in, nil
}
