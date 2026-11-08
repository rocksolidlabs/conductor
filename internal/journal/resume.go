package journal

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
)

// A resume from a record (docs/plans/history.md, "Resume from a record")
// is an operation with a durable receipt in the record (Record.Resumes):
// reserved pending under the record's lock, with the id of the session to
// come, and written before anything is checked or launched
// (ReserveResume); settled started or failed once the launch is known
// (SettleResume), or, after a stop, by Recover from what the data
// directory shows. One command id, one receipt: the id sent again answers
// it. At most MaxResumes receipts a record, and none is ever let go to
// make room: a receipt goes only with its record, which a pending one
// keeps (Forget, ForgetAll and Prune refuse or skip it).

// Errors of a resume's receipt.
var (
	ErrResumeLimit = errors.New("journal: the record holds as many resumes' receipts as it may")
	ErrNewer       = errors.New("journal: the record is of a later version, read as it is and never written")
)

// ReserveResume looks up the receipt of want.CommandID in the record of
// id, under the record's lock (wmu, which every removal takes): a receipt
// of that id is answered as it is (fresh false), whatever happened to its
// launch. Else, past MaxResumes receipts, ErrResumeLimit with nothing
// reserved; else want is reserved, pending, with the session id it names
// (allocated by the caller for the session to come), and written before
// ReserveResume returns (fresh true). A write that fails takes the
// reservation back and answers its error. It answers the record as it was
// read, the reservation in it, for the caller's checks.
func (j *Journal) ReserveResume(id string, want Resume) (rec Record, receipt Resume, fresh bool, err error) {
	if !idPattern.MatchString(id) {
		return Record{}, Resume{}, false, ErrNotFound
	}
	want.State, want.Code, want.Error = ResumePending, "", ""
	if want.At.IsZero() {
		want.At = j.o.Now()
	}
	want.At = want.At.UTC()
	j.wmu.Lock()
	defer j.wmu.Unlock()
	x, err := j.takeLocked(id)
	if err != nil {
		return Record{}, Resume{}, false, err
	}
	j.mu.Lock()
	if i := slices.IndexFunc(x.r.Resumes, func(r Resume) bool { return r.CommandID == want.CommandID }); i >= 0 {
		receipt, rec = x.r.Resumes[i], cloneRecord(&x.r)
		j.mu.Unlock()
		return rec, receipt, false, nil
	}
	if len(x.r.Resumes) >= MaxResumes {
		j.mu.Unlock()
		return Record{}, Resume{}, false, ErrResumeLimit
	}
	probe := Record{Resumes: []Resume{want}}
	fitRecord(&probe)
	want = probe.Resumes[0]
	x.r.Resumes = append(x.r.Resumes, want)
	j.index[id] = j.row(x)
	rec = cloneRecord(&x.r)
	j.mu.Unlock()
	if err := j.write(x); err != nil {
		// Not durable: nothing may run under it. The writer writes the
		// record as it was.
		j.mu.Lock()
		if j.recs[id] == x {
			x.r.Resumes = slices.DeleteFunc(x.r.Resumes, func(r Resume) bool { return r.CommandID == want.CommandID })
			x.dirty = true
			j.index[id] = j.row(x)
		}
		j.mu.Unlock()
		return Record{}, Resume{}, false, err
	}
	return rec, want, true, nil
}

// SettleResume settles the pending receipt of commandID in the record of
// id as got says (its State, ResumeStarted or ResumeFailed, its Session,
// Code and Error) and writes it before it returns. A receipt no longer
// pending is left as it is. It answers the receipt as the record holds it
// now, every field cut to its bound (fitRecord), so that its caller
// answers what a later look-up of the same command id answers; got so cut
// when the record holds no such receipt or cannot be read. A write that
// fails leaves the receipt settled in memory for the writer to write, and
// answers the error; a journal that no longer writes (closed, read-only)
// answers why, the receipt pending on disk for the next start's Recover to
// settle.
func (j *Journal) SettleResume(id, commandID string, got Resume) (Resume, error) {
	got.CommandID = commandID
	probe := Record{Resumes: []Resume{got}}
	fitRecord(&probe)
	fitted := probe.Resumes[0]
	if !idPattern.MatchString(id) {
		return fitted, ErrNotFound
	}
	j.wmu.Lock()
	defer j.wmu.Unlock()
	x, err := j.takeLocked(id)
	if err != nil {
		return fitted, err
	}
	j.mu.Lock()
	i := slices.IndexFunc(x.r.Resumes, func(r Resume) bool { return r.CommandID == commandID })
	if i < 0 {
		j.mu.Unlock()
		return fitted, nil
	}
	r := &x.r.Resumes[i]
	if r.State != ResumePending {
		stored := *r
		j.mu.Unlock()
		return stored, nil
	}
	r.State, r.Code, r.Error = fitted.State, fitted.Code, fitted.Error
	if fitted.Session != "" {
		r.Session = fitted.Session
	}
	stored := *r
	x.dirty = true
	j.index[id] = j.row(x)
	j.mu.Unlock()
	return stored, j.write(x)
}

// takeLocked is the record of id in memory, read from its file first when
// it is not there (an ended record the writer let go): held then with no
// instance, as Open holds a record it recovered, and let go by the writer
// once written and quiet. A file with no end is, by the rule for a record
// with no instance, ended lost. The caller holds wmu, so no removal runs.
func (j *Journal) takeLocked(id string) (*rec, error) {
	j.mu.Lock()
	if err := j.writable(); err != nil {
		j.mu.Unlock()
		return nil, err
	}
	if x, ok := j.recs[id]; ok {
		j.mu.Unlock()
		return x, nil
	}
	s, ok := j.index[id]
	switch {
	case !ok:
		j.mu.Unlock()
		return nil, ErrNotFound
	case s.Newer:
		j.mu.Unlock()
		return nil, ErrNewer
	}
	j.mu.Unlock()
	r, newer, err := j.load(id)
	if err != nil {
		return nil, err
	}
	if newer {
		return nil, ErrNewer
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if x, ok := j.recs[id]; ok {
		return x, nil
	}
	if _, ok := j.index[id]; !ok {
		return nil, ErrNotFound
	}
	x := &rec{id: id, r: r, endedAt: j.o.Now()}
	if r.open() {
		mod := j.o.Now().UTC()
		if fi, err := os.Stat(filepath.Join(j.sess.Dir(), id+".json")); err == nil {
			mod = fi.ModTime().UTC()
		}
		x.r.LostAt, x.r.Ending = mod, EndingLost
		x.dirty, x.urgent = true, true
	}
	j.recs[id] = x
	j.index[id] = j.row(x)
	return x, nil
}
