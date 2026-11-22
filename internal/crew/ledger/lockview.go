package ledger

import (
	"errors"
	"io/fs"
	"path/filepath"
	"time"
)

// LockView is the owner lock as this ledger saw it at At, read under the
// ledger's lock (Lock). It is a moment, not a promise: the lock can change
// hands the instant after, so a caller says what it was, never acts on it.
// Every write of the ledger checks for itself.
//
// While Owned, Why is the ledger's reason for its own database when
// ReadOnly (a damaged file, one a newer Conductor wrote, one the server
// may not write), and empty otherwise. While not Owned, Why is empty and
// exactly one of the rest says why: WaitingSince (a wait for the lock,
// begun at Open, still tries), GaveUpAt (the ledger stopped trying: its
// wait ended with the lock still held, or the lock file could not be
// opened or locked), NoLocks (the filesystem, or the platform, has no
// locks) or Closed. None of them quotes another process: who holds the
// lock is Probe's to say, at the moment it is asked.
type LockView struct {
	At       time.Time
	Owned    bool
	ReadOnly bool
	// Reason and Readable say, while Owned and ReadOnly, why the ledger
	// reads its database only, of a closed set (ReadOnlyCannotWrite,
	// ReadOnlyCannotOpen, ReadOnlyNewerOrDamaged; "" for none of them), and
	// whether it can still read it: words a page may show, unlike Why,
	// which may name the database's path.
	Reason       ReadOnlyReason
	Readable     bool
	Why          string
	WaitingSince time.Time
	GaveUpAt     time.Time
	NoLocks      bool
	Closed       bool
}

// Lock reads the owner lock's state as this ledger holds it (LockView).
func (l *Ledger) Lock() LockView {
	l.mu.Lock()
	defer l.mu.Unlock()
	v := LockView{At: time.Now().UTC(), ReadOnly: l.readOnly}
	switch {
	case l.closed:
		v.Closed, v.ReadOnly = true, true
	case l.lock != nil:
		v.Owned = true
		if l.readOnly {
			v.Why, v.Reason, v.Readable = l.why, l.roReason, l.roReadable
		}
	default:
		v.WaitingSince, v.GaveUpAt, v.NoLocks = l.waitingSince, l.gaveUpAt, l.noLocks
	}
	return v
}

// ReadOnlyReason is why a ledger that owns the owner lock reads its
// database only.
type ReadOnlyReason string

// The reasons a ledger that owns the lock reads its database only: it
// would not take a write, it would not open for writing, or a newer
// Conductor wrote it or it is damaged.
const (
	ReadOnlyCannotWrite    ReadOnlyReason = "cannot_write"
	ReadOnlyCannotOpen     ReadOnlyReason = "cannot_open"
	ReadOnlyNewerOrDamaged ReadOnlyReason = "newer_or_damaged"
)

// ProbeLineMax bounds the holder's line a probe reads, in bytes.
const ProbeLineMax = 200

// ProbeState is what one try for the owner lock found.
type ProbeState string

// The states a probe finds the owner lock in.
const (
	ProbeHeld    ProbeState = "held"
	ProbeFree    ProbeState = "free"
	ProbeNoLocks ProbeState = "no_locks"
)

// ProbeResult is what Probe's one try found at At: a moment, not a
// promise. Line is the first line the holder wrote in the lock file, read
// in that same try and only when State is ProbeHeld; "" when it is longer
// than ProbeLineMax or not yet written. The holder writes its pid and the
// time it took the lock there; nothing checks either, so a caller shows
// Line as what the holder wrote, if at all.
type ProbeResult struct {
	At    time.Time
	State ProbeState
	Line  string
}

// probeLock is probeOwnerLock; the tests replace it.
var probeLock = probeOwnerLock

// Probe tells who holds the owner lock of the ledger under dir (the runs
// directory), for a process that does not own it: one try, let go at
// once when the lock is free, that never makes the lock file or the
// directory, writes nothing and opens no database. A held lock is left as
// it is, and the try is made in its turn with this process's ledgers'
// (lockTries), so a ledger here that waits for the lock never loses a try
// to it. An error is for a lock file that cannot be opened or locked: a
// missing one (fs.ErrNotExist), one the process may not read
// (fs.ErrPermission), or another trouble; who holds the lock is then not
// known.
func Probe(dir string) (ProbeResult, error) {
	lockTries.lock()
	held, line, noLocks, err := probeLock(filepath.Join(dir, Lock))
	lockTries.unlock()
	r := ProbeResult{At: time.Now().UTC()}
	switch {
	case err != nil:
		return r, err
	case held:
		r.State, r.Line = ProbeHeld, line
	case noLocks:
		r.State = ProbeNoLocks
	default:
		r.State = ProbeFree
	}
	return r, nil
}

// Held reports whether a server holds the owner lock of the ledger under
// dir (the runs directory), by Probe, for a command that must not run
// beside a server: a directory with no lock file is held by nobody, and
// on a platform or a filesystem with no locks it answers false, since
// nothing can be told there.
func Held(dir string) (bool, error) {
	r, err := Probe(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return r.State == ProbeHeld, nil
}
