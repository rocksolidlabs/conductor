package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// The journal's documents, under the data directory:
//
//	history/owner.lock          the owner lock (store.OwnerLock)
//	history/state.json          the mark past every Seq written (seqBlock on)
//	history/sessions/<id>.json  a session's record (Record), MaxRecord bytes
const (
	Dir         = "history"
	SessionsDir = "sessions"
	StateFile   = "state.json"
	Lock        = "owner.lock"
	// maxRead bounds a record read from disk: one of a later version may
	// be larger than this version writes.
	maxRead = 1 << 20
)

// Errors of the journal.
var (
	ErrNotFound      = errors.New("journal: no such record")
	ErrExists        = errors.New("journal: a record of this id exists")
	ErrLive          = errors.New("journal: the session is live")
	ErrResumePending = errors.New("journal: a resume of this record is being started")
	ErrNotOwner      = errors.New("journal: another server owns the session records")
	ErrClosed        = errors.New("journal: closed")
	ErrBadID         = errors.New("journal: not a session id")
	ErrBadCursor     = errors.New("journal: not a cursor of this list")
)

// LockWait is how long a journal that found the owner lock held keeps
// trying for it after Open, as the ledger and Brakeman's records do: a
// desktop restart overlaps the old server's shutdown.
var LockWait = 10 * time.Second

const lockTick = 250 * time.Millisecond

// flushEvery is the time between the writer's periodic passes, and how
// long after its end a record is kept in memory; a test makes it longer.
// It is no promise of what a kill loses: a pass can be delayed by its end
// commits' reads and by a write that fails.
var flushEvery = 5 * time.Second

// closeWait is how long Close gives the end commits still being read, all
// of them together: a record whose read has not returned by then is
// written without one (EndCommit, endCommits).
var closeWait = 3 * time.Second

// endReaders is how many end commits are read at once.
const endReaders = 8

// maxTouchPath bounds the path a touch holds until Denied is asked of it:
// the path as it came, never cut, so the deny list judges the path itself.
const maxTouchPath = 8 << 10

// maxHeld bounds what one record holds in memory between two writes: the
// entries and the touches not yet folded, past which the oldest go,
// counted.
const maxHeld = 4096

// idPattern is a session's id (session.NewID's 16 hex digits).
var idPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// The seams of the package's tests: takeLock is store.TakeOwnerLock;
// beforeWrite, when set, runs before each document is written with its
// name ("state.json", "sessions/<id>.json"), an error it answers the
// write's; beforeDelete the same before a record's file is deleted;
// beforeGet before Get reads a record's file.
var (
	takeLock     = store.TakeOwnerLock
	beforeWrite  func(name string) error
	beforeDelete func(name string) error
	beforeGet    func(id string)
)

// State is what is true of a session now, as its server reads it from the
// session itself (Settle): its name, its agent's own session, its crew,
// and its end once it has one. Final says the session has left the server:
// this is the last State of the instance, and it carries the end.
type State struct {
	Name         string
	AgentSession *AgentSession
	Crew         *Crew
	End          *Ending // nil while the session runs
	Final        bool
}

// Ending is how a session ended (End): an observed end with its time and
// words, or (Lost) the server no longer observing it. Commit is an
// observed end's commit as the session's own machine read it (a hosted
// session's host, StartFromHost's), written with the end in place of the
// writer's read (EndCommit); none, the writer reads it.
type Ending struct {
	At     time.Time
	How    string
	Lost   bool
	Commit string
}

// Options are Open's.
type Options struct {
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Log is where the journal says what it skipped; slog.Default when nil.
	Log *slog.Logger
	// EndCommit answers the commit a record's folder is at after its end,
	// "" for none or unknown; called on the writer's goroutine, several at
	// once (at most endReaders). ctx ends when Close gives up on the reads
	// (closeWait): an answer after that is not kept.
	EndCommit func(ctx context.Context, r Record) string
	// Denied says the server's deny list refuses path, which a server
	// session's files are judged by: such a file is never written. Called
	// on the writer's goroutine.
	Denied func(path string) bool
	// Removed is told the id of each record a removal (Forget, ForgetAll,
	// Prune) took out of memory, whether its file's delete then succeeded
	// or not: no end commit is asked of that record from then on (a read
	// already begun aside), so what the server keeps for that read it lets
	// go. Called by the removal with wmu held and no other lock of the
	// journal's; it must not call the journal.
	Removed func(id string)
	// maxRecords is the ended records kept, MaxRecords when nil: a test's
	// bound, the journal's own from Open.
	maxRecords *int
}

// Journal is the session records, opened by Open. Every method is safe
// for concurrent use; those a hook calls (Note, Chat, Touch, Tool, Settle
// never wait on a disk.
type Journal struct {
	st, sess *store.Store
	o        Options
	log      *slog.Logger
	// maxRecords is the ended records kept (Prune), set before the writer
	// starts; read with wmu held.
	maxRecords int

	// wmu orders the disk's writes (a record and the state file before
	// it, a delete), taken before mu.
	wmu sync.Mutex
	mu  sync.Mutex
	// seq is the counter: the last Seq given an entry or a write; high
	// the mark state.json holds, past every Seq written. A write whose Seq
	// passes it writes state.json first, with a mark seqBlock further on,
	// so a Seq never goes back after a restart and the state file is
	// written once per seqBlock writes, not with each.
	seq   int64
	high  int64
	recs  map[string]*rec     // the records in memory: open, or ended a moment ago
	index map[string]*summary // every record on disk or in memory
	// insts is the last instance number given (Instance).
	insts uint64
	// removing is the rows of the records a removal (Forget, ForgetAll,
	// Prune) chose and is deleting, wmu held, by id: taken out of recs and
	// index as it chose them, so no reader is handed one (forgetLocked).
	removing map[string]*summary
	// rmu orders a removal after the reads under way: Get holds it shared
	// while it reads, a removal holds it as it hides its records
	// (lockChoose), so none is handed out once they are hidden. Taken
	// after wmu, before mu.
	rmu      sync.RWMutex
	readOnly bool
	why      string
	closed   bool
	lock     *store.OwnerLock
	onOwned  func()
	owned    bool
	notified bool
	inOwned  sync.WaitGroup
	running  bool
	// reads is the end commits' reads' context, ended closeWait after
	// Close begins (endCommits).
	reads     context.Context
	stopReads context.CancelFunc
	// roMu guards roRows, a read-only journal's rows by file, each read
	// again only when its file's size or time changed (scan).
	roMu   sync.Mutex
	roRows map[string]scanned

	wake    chan struct{}
	flushes chan chan struct{}
	stop    chan struct{}
	stopped chan struct{}
	done    chan struct{}
	shut    chan struct{}
}

// rec is a record in memory: one instance of it (Instance).
type rec struct {
	// id is the record's id, never changed: what the writer's work names it
	// by outside mu. inst is its instance's number, 0 for a record no
	// session's hooks hold (one read at Open); both are read under mu.
	id   string
	inst uint64
	r    Record
	// dirty: changed since its last write; urgent: to be written at once
	// (an end, a crew taken); needEnd: its end commit to be read, which
	// keeps it in memory and never keeps its end from being written (the
	// package's rule on end commits); released: its session's last State
	// is in (State.Final), so it may leave memory once ended and written.
	dirty, urgent, needEnd, released bool
	touches                          []touch
	// state is the last State settled (Settle): its metadata is the
	// record's.
	state *State
	// endedAt is when its end was set here, for its eviction.
	endedAt time.Time
}

// Instance is one life of a record: what Begin made of it, or took up
// again, until a removal ends it. Begin answers it and every write names
// it, never an id alone: a write whose instance a removal ended is
// dropped, whatever record of its id there is by then (the package's
// rule). The zero Instance is none: every write drops it.
type Instance struct {
	id string
	n  uint64
}

// ID is the session id of the instance's record.
func (in Instance) ID() string { return in.id }

// row is x's row in the index, with its instance. The caller holds mu.
func (j *Journal) row(x *rec) *summary {
	s := summaryOf(&x.r, false)
	s.inst = x.inst
	return s
}

// newInst is a new instance's number. The caller holds mu.
func (j *Journal) newInst() uint64 {
	j.insts++
	return j.insts
}

// touch is a file touched, not yet asked of Denied: path is the path as it
// came, f the row it folds into, its path cut to MaxPath.
type touch struct {
	f    File
	path string
}

// summary is what the journal keeps of every record: its list row and
// what Begin, Forget and Prune decide by.
type summary struct {
	Summary
	open, pending bool
	owner         string
	// inst is the instance of the record whose hooks are taken (Instance),
	// 0 for none: a row read from its file at Open, put back after a failed
	// delete, of a later version or unreadable.
	inst uint64
	// stopped is when the record stopped being open (its end, else when it
	// was last observed; an unreadable file's last write): Prune keeps the
	// latest.
	stopped time.Time
}

// MaxSummary is the most a Summary takes as encoding/json writes it, HTML
// characters escaped as the API's replies escape them (six bytes for a
// '<' its bounds count as one), every field at its bound
// (TestASummaryAtEveryBoundFits): a page of a list is at most MaxPage of
// them.
const MaxSummary = 72 << 10

// Summary is a record's row in a list, its strings at the record's bounds
// (summaryOf).
type Summary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Agent     Agent     `json:"agent"`
	Host      Host      `json:"host"`
	Where     string    `json:"where,omitempty"`
	Cwd       string    `json:"cwd"`
	Repo      string    `json:"repo,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Crew      *Crew     `json:"crew,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitzero"`
	LostAt    time.Time `json:"lostAt,omitzero"`
	Ending    string    `json:"ending,omitempty"`
	Entries   int       `json:"entries"`
	Files     int       `json:"files"`
	Dropped   int       `json:"dropped,omitempty"`
	Newer     bool      `json:"newer,omitempty"`
	// Unreadable: a file in history/sessions/ this version cannot decode,
	// kept as it is: listed by its id alone (StartedAt its file's last
	// write), forgettable, counted toward MaxRecords, never written over.
	Unreadable bool `json:"unreadable,omitempty"`
}

// summaryOf is r's row, its strings cut to the record's bounds (fitRecord)
// whatever file it was read from (a later version's is read as it is), so
// that a row is at most MaxSummary bytes in a reply.
func summaryOf(r *Record, newer bool) *summary {
	h := Record{Name: r.Name, Kind: r.Kind, Agent: r.Agent, Host: r.Host, Where: r.Where, Cwd: r.Cwd, Repo: r.Repo, Branch: r.Branch, Crew: r.Crew, Ending: r.Ending}
	fitRecord(&h)
	s := &summary{Summary: Summary{ID: fitPath(r.ID, MaxID), Name: h.Name, Kind: h.Kind, Agent: h.Agent, Host: h.Host, Where: h.Where, Cwd: h.Cwd, Repo: h.Repo, Branch: h.Branch,
		Crew: h.Crew, StartedAt: r.StartedAt, EndedAt: r.EndedAt, LostAt: r.LostAt, Ending: h.Ending, Entries: len(r.Entries), Files: len(r.Files), Dropped: r.Dropped, Newer: newer},
		open: r.open() && !newer, pending: r.pending(), owner: r.Host.Owner, stopped: endOf(r)}
	s.Host.Owner = ""
	return s
}

// unreadableRow is the row of a record file this version cannot decode,
// last written at mod.
func unreadableRow(id string, mod time.Time) *summary {
	mod = mod.UTC()
	return &summary{Summary: Summary{ID: id, StartedAt: mod, Unreadable: true}, stopped: mod}
}

// fileID is the session id a record's file name holds, false for a name
// that is no record's.
func fileID(name string) (string, bool) {
	id, ok := strings.CutSuffix(name, ".json")
	return id, ok && idPattern.MatchString(id)
}

// Open takes history/ under the data directory st holds (0700), its
// sessions/ and its owner lock, history/owner.lock. Holding the lock it
// recovers what the last owner left (Recover) and writes; finding it held
// it opens read-only (ReadOnly: reads are served from the files, nothing
// is written) and keeps trying for LockWait, recovering once it holds it
// (OnOwned). Where locks are unsupported it stays read-only.
func Open(st *store.Store, o Options) (*Journal, error) {
	h, err := st.Sub(Dir)
	if err != nil {
		return nil, err
	}
	sess, err := h.Sub(SessionsDir)
	if err != nil {
		return nil, err
	}
	j := &Journal{st: h, sess: sess, o: o, log: o.Log, recs: map[string]*rec{}, index: map[string]*summary{}, removing: map[string]*summary{},
		wake: make(chan struct{}, 1), flushes: make(chan chan struct{}), stop: make(chan struct{}), stopped: make(chan struct{}),
		done: make(chan struct{}), shut: make(chan struct{}), roRows: map[string]scanned{}}
	j.reads, j.stopReads = context.WithCancel(context.Background())
	if j.o.Now == nil {
		j.o.Now = time.Now
	}
	if j.log == nil {
		j.log = slog.Default()
	}
	j.maxRecords = MaxRecords
	if o.maxRecords != nil {
		j.maxRecords = *o.maxRecords
	}
	lockPath := filepath.Join(h.Dir(), Lock)
	lock, why, held, err := takeLock(lockPath)
	wait := false
	switch {
	case err != nil:
		j.readOnly, j.why = true, err.Error()
	case lock == nil:
		j.readOnly, j.why = true, why
		if held {
			j.why = "waiting for the owner lock: " + why
			wait = true
		}
	default:
		j.lock = lock
		if err := j.recover(); err != nil {
			lock.Release()
			return nil, err
		}
		j.owned = true
		j.startWriter()
	}
	// Said before the waiter starts, which changes both under mu.
	if j.readOnly {
		j.log.Warn("the session records are read-only: this server's sessions are not recorded", "why", j.why)
	}
	if wait {
		go j.awaitLock(lockPath)
	}
	return j, nil
}

// awaitLock tries for the owner lock every lockTick for up to LockWait:
// each try under mu while the journal is open, so a Close waits for one
// under way and none follows. Once taken it recovers, starts the writer
// and runs OnOwned's function.
func (j *Journal) awaitLock(lockPath string) {
	deadline := time.Now().Add(LockWait)
	tick := time.NewTicker(lockTick)
	defer tick.Stop()
	for {
		select {
		case <-j.done:
			return
		case <-tick.C:
		}
		j.wmu.Lock()
		j.mu.Lock()
		if j.closed {
			j.mu.Unlock()
			j.wmu.Unlock()
			return
		}
		lock, why, held, err := takeLock(lockPath)
		switch {
		case err != nil:
			j.why = err.Error()
		case lock != nil:
			j.lock = lock
			j.mu.Unlock()
			err := j.recoverLocked()
			j.mu.Lock()
			if err != nil {
				j.why = "recovering the last owner's records: " + err.Error()
			} else {
				j.readOnly, j.why = false, ""
				j.owned = true
				j.startWriterLocked()
			}
			// Close may have begun while the records were recovered (mu let
			// go, wmu held): then the callback never runs, as Close may
			// already have waited for it.
			cb := j.onOwned
			run := cb != nil && !j.readOnly && !j.notified && !j.closed
			if run {
				j.notified = true
				j.inOwned.Add(1)
			}
			j.mu.Unlock()
			j.wmu.Unlock()
			if err == nil {
				j.log.Info("the session records are this server's now: its sessions are recorded")
			}
			if run {
				defer j.inOwned.Done()
				cb()
			}
			return
		case held && time.Now().Before(deadline):
			j.mu.Unlock()
			j.wmu.Unlock()
			continue
		default:
			j.why = why
		}
		j.mu.Unlock()
		j.wmu.Unlock()
		return
	}
}

// OnOwned sets what runs once the journal holds the owner lock, at once
// when it already does. It runs at most once, never after Close, and Close
// waits for it.
func (j *Journal) OnOwned(fn func()) {
	j.mu.Lock()
	j.onOwned = fn
	run := fn != nil && j.owned && !j.readOnly && !j.notified && !j.closed
	if run {
		j.notified = true
		j.inOwned.Add(1)
	}
	j.mu.Unlock()
	if run {
		defer j.inOwned.Done()
		fn()
	}
}

// ReadOnly reports whether the journal writes nothing, and why.
func (j *Journal) ReadOnly() (bool, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.readOnly, j.why
}

// Close writes every record still dirty, ends the writer and lets the
// owner lock go, once OnOwned's function has returned. Every later write
// is refused; a second Close waits for the first.
func (j *Journal) Close() error {
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		<-j.shut
		return nil
	}
	j.closed = true
	close(j.done)
	running := j.running
	j.mu.Unlock()
	// The end commits still to be read get closeWait from here, together
	// (endCommits); the writer's last pass writes the rest without one.
	giveUp := time.AfterFunc(closeWait, j.stopReads)
	defer giveUp.Stop()
	defer j.stopReads()
	j.inOwned.Wait()
	if running {
		close(j.stop)
		<-j.stopped
	}
	j.wmu.Lock()
	j.mu.Lock()
	defer close(j.shut)
	defer j.wmu.Unlock()
	defer j.mu.Unlock()
	if j.lock != nil {
		j.lock.Release()
		j.lock = nil
	}
	return nil
}

// writable is nil when the journal writes. The caller holds mu.
func (j *Journal) writable() error {
	switch {
	case j.closed:
		return ErrClosed
	case j.readOnly:
		return fmt.Errorf("%w: %s", ErrNotOwner, j.why)
	}
	return nil
}

func (j *Journal) startWriter() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.startWriterLocked()
}

func (j *Journal) startWriterLocked() {
	if j.running || j.closed {
		return
	}
	j.running = true
	go j.run()
}

// poke wakes the writer for what is urgent; it never waits.
func (j *Journal) poke() {
	select {
	case j.wake <- struct{}{}:
	default:
	}
}

// run is the writer: what is urgent at once, every dirty record each
// flushEvery and on Flush, every one at Close.
func (j *Journal) run() {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-j.stop:
			j.pass(true)
			close(j.stopped)
			return
		case req := <-j.flushes:
			j.pass(true)
			close(req)
		case <-j.wake:
			j.pass(false)
		case <-t.C:
			j.pass(true)
		}
	}
}

// Flush has the writer write every record that changed, now, and returns
// once it has; at once when the journal writes nothing.
func (j *Journal) Flush() {
	j.mu.Lock()
	running := j.running && !j.closed
	j.mu.Unlock()
	if !running {
		return
	}
	req := make(chan struct{})
	select {
	case j.flushes <- req:
		<-req
	case <-j.stopped:
	}
}

// Begin keeps the record of a session as it is added, and answers the
// instance its writes name (Instance): in memory at once, so the record
// exists before the session's first entry. A server session's record is
// written before Begin returns; should that write fail, the record is kept
// all the same, its instance answered, and the writer tries it again as
// any record it holds (Begin answers an error only for a record it did not
// keep). An id that has a record is refused (ErrExists): a server
// session's id is made once and never comes back; a hosted session's,
// which comes back, is begun by BeginHosted. A record's hosted fields
// (Host.Hosted, Host.Owner) are kept as given.
func (j *Journal) Begin(r Record) (Instance, error) {
	if !idPattern.MatchString(r.ID) {
		return Instance{}, ErrBadID
	}
	r.V, r.Seq = V, 0
	r.EndedAt, r.LostAt, r.Ending, r.End, r.Reopened, r.Dropped = time.Time{}, time.Time{}, "", "", 0, 0
	r.Entries, r.Files, r.Tools, r.Resumes = []Entry{}, nil, nil, nil
	if r.StartedAt.IsZero() {
		r.StartedAt = j.o.Now()
	}
	r.StartedAt = r.StartedAt.UTC()
	fitRecord(&r)
	// wmu first: a removal of this id under way ends before (forgetLocked).
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.mu.Lock()
	if err := j.writable(); err != nil {
		j.mu.Unlock()
		return Instance{}, err
	}
	if _, ok := j.index[r.ID]; ok {
		j.mu.Unlock()
		j.log.Warn("a session's record was not begun: its id has one", "session", r.ID)
		return Instance{}, ErrExists
	}
	x := &rec{id: r.ID, inst: j.newInst(), r: r, dirty: true}
	j.recs[r.ID] = x
	j.index[r.ID] = j.row(x)
	in := Instance{id: r.ID, n: x.inst}
	j.mu.Unlock()
	if err := j.write(x); err != nil {
		j.log.Warn("a session's record was not written as it began; it is tried again", "session", r.ID, "err", err)
	}
	return in, nil
}

// Abandon removes the record of a session that was never added (the
// registry refused it): from memory and from the disk, while in is still
// its record's instance. It returns once the delete is made or has failed;
// a failed one puts the row back from the file with no instance, as a
// removal's does (restoreLocked): listed lost, forgettable.
func (j *Journal) Abandon(in Instance) {
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.mu.Lock()
	if x := j.recs[in.id]; j.writable() != nil || in.n == 0 || x == nil || x.inst != in.n {
		j.mu.Unlock()
		return
	}
	delete(j.recs, in.id)
	delete(j.index, in.id)
	j.mu.Unlock()
	if err := j.remove(in.id); err != nil {
		j.log.Warn("an abandoned session's record was not removed; it is listed lost", "session", in.id, "err", err)
		j.restoreLocked(in.id)
	}
}

// Activity is an activity entry of a session, as the API hands it over:
// the session's own types and fields (session.ActivityEntry), a file's
// path made absolute.
type Activity struct {
	At                                       time.Time
	Type, By, ByName, Message, URL, To, Tool string
	Op, Path                                 string
}

// The activity types and marks the journal reads (session.Activity*,
// session.OpTyped).
const (
	actInput      = "input"
	actStatus     = "status"
	actProgress   = "progress"
	actArtifact   = "artifact"
	actHandoff    = "handoff"
	actError      = "error"
	actToolDenied = "tool_denied"
	actToolUse    = "tool_use"
	actFile       = "file"
	opTyped       = "typed"
)

// Note keeps what an activity entry says that is evidence of the work: a
// line Conductor typed (an input entry marked typed, with no subscriber),
// a report (progress, artifact, handoff, error, tool_denied), a status
// line; a tool use is counted (Tool), a file folded (Touch). Every other
// entry is not kept: an input entry a person's reply made, an attention
// entry (the question and its end come from the chat), who joined, left
// or came through a link.
func (j *Journal) Note(in Instance, a Activity) {
	var e Entry
	switch a.Type {
	case actInput:
		if a.Op != opTyped || a.By != "" {
			return
		}
		e = Entry{Kind: KindTyped, Message: a.Message, ByName: a.ByName}
	case actProgress, actArtifact, actHandoff, actError, actToolDenied:
		e = Entry{Kind: KindReport, Event: a.Type, Message: a.Message, URL: a.URL, To: a.To, Tool: a.Tool}
	case actStatus:
		e = Entry{Kind: KindStatus, Message: a.Message}
	case actToolUse:
		j.Tool(in, a.Tool)
		return
	case actFile:
		if a.Op != "" && a.Path != "" {
			j.Touch(in, File{Path: a.Path, Op: a.Op, Count: 1, Last: a.At})
		}
		return
	default:
		return
	}
	e.At = a.At
	j.add(in, e)
}

// ChatLine is a chat message of a session (session.ChatMessage) as the
// API hands it over, its choices' labels alone.
type ChatLine struct {
	ID             string
	At             time.Time
	Kind, Event    string
	ByName, ByRole string
	Text, Ref, To  string
	Choices        []string
}

// The chat's kinds, events and reasons the journal reads (proto.ChatKind*,
// session.ChatLeftTerminal).
const (
	chatMessage     = "message"
	chatSystem      = "system"
	chatSentToAgent = "sent_to_agent"
	chatQuestion    = "question"
	chatAnswered    = "answered"
	chatWithdrawn   = "withdrawn"
	leftTerminal    = "terminal"
)

// Chat keeps a line of the session's chat: the agent's question with its
// choices, how it ended (a choice's label, a line typed that the chat
// never repeats, keys typed in the terminal, the agent moving on), a
// message posted and a mark that one was sent to the agent. The chat's
// join and leave lines are not kept.
func (j *Journal) Chat(in Instance, c ChatLine) {
	e := Entry{At: c.At, ID: c.ID, ByName: c.ByName}
	switch c.Kind {
	case chatQuestion:
		e.Kind, e.Message, e.Choices = KindQuestion, c.Text, slices.Clone(c.Choices)
	case chatMessage:
		e.Kind, e.Message = KindChat, c.Text
	case chatSentToAgent:
		e.Kind, e.Event, e.Ref, e.To = KindChat, chatSentToAgent, c.Ref, c.To
	case chatSystem:
		switch c.Event {
		case chatAnswered:
			e.Kind, e.Ref, e.Outcome, e.Message = KindAnswer, c.Ref, OutcomeAnswered, c.Text
		case chatWithdrawn:
			e.Kind, e.Ref, e.Outcome = KindAnswer, c.Ref, OutcomeWithdrawn
			if c.Text == leftTerminal {
				e.Outcome = OutcomeAnsweredInTerminal
			}
		default:
			return
		}
	default:
		return
	}
	j.add(in, e)
}

// held is the record of a stream's instance (Note, Chat, Touch, Tool) in
// memory while it is open: the package's rule, which every write passes
// through. Nil for an instance a removal ended (whatever record of its id
// there is now), for the zero Instance, and when the journal writes
// nothing; with ended true for its record once its end is recorded, where
// what the stream brings is dropped and counted. The caller holds mu.
func (j *Journal) held(in Instance) (x *rec, ended bool) {
	if in.n == 0 || j.writable() != nil {
		return nil, false
	}
	x, ok := j.recs[in.id]
	if !ok || x.inst != in.n {
		return nil, false
	}
	return x, !x.r.open()
}

// add keeps e in the record of in, in its place by At then by arrival,
// while the record is open; once its end is recorded e is dropped and
// counted.
func (j *Journal) add(in Instance, e Entry) {
	fitEntry(&e)
	if e.At.IsZero() {
		e.At = j.o.Now().UTC()
	}
	e.At = e.At.UTC()
	j.mu.Lock()
	defer j.mu.Unlock()
	x, ended := j.held(in)
	if x == nil {
		return
	}
	if ended {
		x.r.Dropped++
		x.dirty = true
		return
	}
	j.seq++
	e.Seq = j.seq
	x.r.Entries = insertEntry(x.r.Entries, e)
	if len(x.r.Entries) > maxHeld {
		n := len(x.r.Entries) - maxHeld
		x.r.Entries = slices.Delete(x.r.Entries, 0, n)
		x.r.Dropped += n
	}
	x.dirty = true
}

// endOf is when the record's session ended or was last observed; zero
// while it is open.
func endOf(r *Record) time.Time {
	if !r.EndedAt.IsZero() {
		return r.EndedAt
	}
	return r.LostAt
}

// insertEntry puts e among entries, which are in order of At then Seq, in
// its place.
func insertEntry(entries []Entry, e Entry) []Entry {
	i := len(entries)
	for i > 0 && after(entries[i-1], e) {
		i--
	}
	return slices.Insert(entries, i, e)
}

// after says a comes after b: a later At, or the same and a later Seq.
func after(a, b Entry) bool {
	if !a.At.Equal(b.At) {
		return a.At.After(b.At)
	}
	return a.Seq > b.Seq
}

// Touch keeps that the session's agent touched a file: folded into one
// row per path and op once the writer has asked Denied of it; dropped
// and counted once the record's end is recorded.
func (j *Journal) Touch(in Instance, f File) {
	if f.Path == "" || f.Op == "" {
		return
	}
	// Denied is asked of the path as it came, the row keeps it cut: a cut
	// could leave out the part of the name a deny list covers.
	t := touch{path: f.Path}
	f.Path, f.Op = fitPath(f.Path, MaxPath), cutBytes(f.Op, 32)
	if f.Last.IsZero() {
		f.Last = j.o.Now()
	}
	f.Last, f.Count = f.Last.UTC(), max(f.Count, 1)
	t.f = f
	j.mu.Lock()
	defer j.mu.Unlock()
	x, ended := j.held(in)
	if x == nil {
		return
	}
	if ended || len(x.touches) >= maxHeld || len(t.path) > maxTouchPath {
		x.r.Dropped++
		x.dirty = true
		return
	}
	x.touches = append(x.touches, t)
	x.dirty = true
}

// Tool counts a tool use, at most MaxTools tools: a tool past them is not
// counted.
func (j *Journal) Tool(in Instance, name string) {
	name = fitPath(name, MaxTool)
	if name == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	x, ended := j.held(in)
	if x == nil || ended {
		return
	}
	if _, known := x.r.Tools[name]; !known && len(x.r.Tools) >= MaxTools {
		return
	}
	if x.r.Tools == nil {
		x.r.Tools = map[string]int{}
	}
	x.r.Tools[name]++
	x.dirty = true
}

// Settle makes the record of in say what st says of its session: its
// name, its agent's own session and its crew as they are now (a crew taken
// written at once), and its end once st has one (written at once; an
// observed end's commit is read by the writer and written when it is
// read, never a condition of the end's write: the package's rule on end
// commits). It is idempotent: a State
// settled again, or an older one after a newer, changes nothing an end
// recorded says, since an end, once recorded, stands (an observed end
// replaces only a lost one); what it reads of the metadata is the
// caller's to order. A State with Final is the instance's last: the
// record may leave memory once ended and written. It never waits on a
// disk.
func (j *Journal) Settle(in Instance, st State) {
	if e := st.End; e != nil {
		end := *e
		if end.At.IsZero() {
			end.At = j.o.Now()
		}
		end.At = end.At.UTC()
		st.End = &end
	}
	if a := st.AgentSession; a != nil {
		c := *a
		st.AgentSession = &c
	}
	if c := st.Crew; c != nil {
		cp := *c
		st.Crew = &cp
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if in.n == 0 || j.writable() != nil {
		return
	}
	x, ok := j.recs[in.id]
	if !ok || x.inst != in.n {
		return
	}
	x.state = &st
	j.applyState(x)
	if st.End != nil {
		j.endLocked(x, *st.End)
	}
	if st.Final {
		x.released = true
	}
}

// applyState takes the metadata of the last State settled into x's
// record: what changed of its name, its agent's own session, its crew (at
// once when it had none). The caller holds mu.
func (j *Journal) applyState(x *rec) {
	m := x.state
	if m == nil {
		return
	}
	r := &x.r
	changed := false
	if name := fitPath(m.Name, maxField); name != "" && name != r.Name {
		r.Name, changed = name, true
	}
	if s := m.AgentSession; s != nil {
		c := AgentSession{ID: fitPath(s.ID, maxField), Source: cutBytes(s.Source, 16), Resumable: s.Resumable}
		if r.Agent.Session == nil || *r.Agent.Session != c {
			r.Agent.Session, changed = &c, true
		}
	}
	if c := m.Crew; c != nil {
		before := r.Crew
		if before == nil || *before != *c {
			cp := *c
			r.Crew, changed = &cp, true
			fitRecord(r)
			if before == nil {
				x.urgent = true
				j.poke()
			}
		}
	}
	if changed {
		x.dirty = true
		if j.recs[x.id] == x {
			j.index[x.id] = j.row(x)
		}
	}
}

// endLocked records the end of x's session once: an observed end or a
// lost one, on a record that has no end; an end once recorded stands,
// whichever it was. An observed end also ends the questions still open in
// the record, as the session's end withdrew them (closeQuestions). The
// caller holds mu.
func (j *Journal) endLocked(x *rec, end Ending) {
	if !x.r.open() {
		return
	}
	if end.Lost {
		x.r.LostAt, x.r.Ending = end.At, cutBytes(end.How, maxField)
	} else {
		x.r.EndedAt, x.r.LostAt, x.r.Ending = end.At, time.Time{}, cutBytes(end.How, maxField)
		// A hosted record's end commit is its host's or none: the writer
		// never reads one for it on this machine.
		x.needEnd = !x.hosted()
		if end.Commit != "" {
			x.r.End, x.needEnd = fitPath(end.Commit, 64), false
		}
		j.closeQuestions(&x.r)
	}
	x.dirty, x.urgent, x.endedAt = true, true, j.o.Now()
	if j.recs[x.id] == x {
		j.index[x.id] = j.row(x)
	}
	j.poke()
}

// closeQuestions ends every question r holds with no answer after it as
// the session's end did (OutcomeWithdrawn: the agent moved on, as the
// session's own withdrawal says), at the end or at the question when the
// question is later: what the session told of a question as it ended may
// come after the end is recorded, and is then dropped (add). The caller
// holds mu.
func (j *Journal) closeQuestions(r *Record) {
	if r.EndedAt.IsZero() {
		return
	}
	open := map[string]time.Time{}
	var order []string
	for _, e := range r.Entries {
		switch {
		case e.Kind == KindQuestion && e.ID != "":
			if _, seen := open[e.ID]; !seen {
				order = append(order, e.ID)
			}
			open[e.ID] = e.At
		case e.Kind == KindAnswer && e.Ref != "":
			if _, seen := open[e.Ref]; seen {
				open[e.Ref] = time.Time{}
			}
		}
	}
	for _, id := range order {
		at := open[id]
		if at.IsZero() {
			continue
		}
		j.seq++
		r.Entries = insertEntry(r.Entries, Entry{Seq: j.seq, At: maxTime(at, r.EndedAt), Kind: KindAnswer, Ref: id, Outcome: OutcomeWithdrawn})
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// pass is one turn of the writer: the end commits read and the touches
// asked of Denied and folded, then the records written (every dirty one
// with all, else the urgent ones), the ended ones whose sessions are done
// with them (released, or none's: inst 0) let go of, and the ended records
// past MaxRecords pruned.
func (j *Journal) pass(all bool) {
	j.endCommits(j.collect(func(x *rec) bool { return x.needEnd }))
	for _, x := range j.collect(func(x *rec) bool { return len(x.touches) > 0 }) {
		j.fold(x)
	}
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.mu.Lock()
	var due []*rec
	for id, x := range j.recs {
		if x.urgent || (all && x.dirty) {
			due = append(due, x)
			continue
		}
		if (x.released || x.inst == 0) && !x.needEnd && !x.dirty && len(x.touches) == 0 && !x.endedAt.IsZero() && j.o.Now().Sub(x.endedAt) >= flushEvery {
			delete(j.recs, id)
		}
	}
	j.mu.Unlock()
	sort.Slice(due, func(a, b int) bool { return due[a].r.ID < due[b].r.ID })
	for _, x := range due {
		if err := j.write(x); err != nil {
			j.log.Warn("a session's record was not written; it is tried again", "session", x.id, "err", err)
		}
	}
	j.pruneLocked()
}

// collect answers the records in memory that pick takes, which it may
// change, under mu.
func (j *Journal) collect(pick func(*rec) bool) []*rec {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []*rec
	for _, x := range j.recs {
		if pick(x) {
			out = append(out, x)
		}
	}
	return out
}

// endCommits reads the end commits of the records ended, at most
// endReaders at once, and returns once they are read or Close has given up
// on them (reads ended, closeWait after it began). It changes no record's
// end: each end was settled dirty and urgent, and the writes that follow
// write it with the commit it has, none for a read not answered (the
// package's rule on end commits). An answer that comes once Close has
// given up is not kept (endCommit).
func (j *Journal) endCommits(ends []*rec) {
	if len(ends) == 0 {
		return
	}
	ctx := j.reads
	sem := make(chan struct{}, endReaders)
	var wg sync.WaitGroup
	for _, x := range ends {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				if ctx.Err() == nil {
					j.endCommit(ctx, x)
				}
			case <-ctx.Done():
			}
		})
	}
	read := make(chan struct{})
	go func() {
		wg.Wait()
		close(read)
	}()
	select {
	case <-read:
	case <-ctx.Done():
	}
}

// endCommit reads the end commit of an ended record and marks the record
// to be written with it. An answer that comes once Close has given up on
// the reads (ctx ended, endCommits) changes nothing: the read may outlast
// Close, but nothing it brings is kept.
func (j *Journal) endCommit(ctx context.Context, x *rec) {
	j.mu.Lock()
	r := x.r
	r.Entries, r.Files = nil, nil
	j.mu.Unlock()
	commit := ""
	if j.o.EndCommit != nil {
		commit = j.o.EndCommit(ctx, r)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !x.needEnd || ctx.Err() != nil {
		return
	}
	x.needEnd = false
	x.r.End = fitPath(commit, 64)
	x.dirty, x.urgent = true, true
}

// fold asks the server's deny list (Denied) of each touch a server
// session's record holds, by the path as it came, and folds the rest into
// its files; a hosted record's are folded as they came. What it
// judged is kept only
// while x is still the record in memory (the package's rule): a record
// removed while it folded takes nothing of it.
func (j *Journal) fold(x *rec) {
	j.mu.Lock()
	touches := x.touches
	x.touches = nil
	// A hosted record's paths are its host's, text: this machine's deny
	// list is never asked of them (the host leaves out what its own
	// refuses before it reports them).
	ask := j.o.Denied != nil && !x.hosted()
	j.mu.Unlock()
	kept := touches[:0:0]
	for _, t := range touches {
		if ask && j.o.Denied(t.path) {
			continue
		}
		kept = append(kept, t)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.recs[x.id] != x {
		return
	}
	for _, t := range kept {
		x.r.Files = foldFile(x.r.Files, t.f, &x.r.Dropped)
	}
	x.dirty = true
}

// foldFile counts f in files, one row per path and op, at most MaxFiles
// rows: the oldest by Last goes, counted in dropped.
func foldFile(files []File, f File, dropped *int) []File {
	for i := range files {
		if files[i].Path == f.Path && files[i].Op == f.Op {
			files[i].Count += f.Count
			if f.Last.After(files[i].Last) {
				files[i].Last = f.Last
			}
			return files
		}
	}
	files = append(files, f)
	if len(files) > MaxFiles {
		oldest := 0
		for i := range files {
			if files[i].Last.Before(files[oldest].Last) {
				oldest = i
			}
		}
		files = slices.Delete(files, oldest, oldest+1)
		*dropped++
	}
	return files
}

// seqBlock is how far past the Seq of the write that passes it the state
// file's mark is put.
const seqBlock = 1 << 12

// write writes x: its Seq from the counter, the state file first when the
// Seq passes its mark, then the record bounded to MaxRecord. The caller
// holds wmu, so the writes' marks go up in turn.
func (j *Journal) write(x *rec) error {
	j.mu.Lock()
	if j.recs[x.id] != x {
		j.mu.Unlock()
		return nil
	}
	// Measured with the Seq it is written with, which may be longer.
	j.seq++
	x.r.Seq, x.r.V = j.seq, versionOf(&x.r)
	bound(&x.r)
	r := cloneRecord(&x.r)
	x.dirty, x.urgent = false, false
	j.index[r.ID] = j.row(x)
	mark := int64(0)
	if r.Seq > j.high {
		mark = r.Seq + seqBlock
	}
	j.mu.Unlock()
	if mark > 0 {
		if err := j.saveState(mark); err != nil {
			j.redirty(x)
			return err
		}
		j.mu.Lock()
		j.high = mark
		j.mu.Unlock()
	}
	if f := beforeWrite; f != nil {
		if err := f(SessionsDir + "/" + r.ID + ".json"); err != nil {
			j.redirty(x)
			return err
		}
	}
	if err := j.sess.Save(r.ID+".json", &r); err != nil {
		j.redirty(x)
		return err
	}
	return nil
}

func (j *Journal) redirty(x *rec) {
	j.mu.Lock()
	x.dirty = true
	j.mu.Unlock()
}

// state is history/state.json: Seq is the mark past every Seq written.
type state struct {
	V   int   `json:"v"`
	Seq int64 `json:"seq"`
}

func (j *Journal) saveState(seq int64) error {
	if f := beforeWrite; f != nil {
		if err := f(StateFile); err != nil {
			return err
		}
	}
	return j.st.Save(StateFile, state{V: V, Seq: seq})
}

// bound brings r under MaxRecord as store.Encode writes it: its oldest
// entries first, then its oldest files, each counted in Dropped.
func bound(r *Record) {
	for range 64 {
		b, err := store.Encode(r)
		if err != nil || len(b) <= MaxRecord {
			return
		}
		excess := len(b) - MaxRecord
		switch {
		case len(r.Entries) > 0:
			n, cut := 0, 0
			for n < len(r.Entries) && cut < excess {
				cut += encodedLen(r.Entries[n]) + 8
				n++
			}
			r.Entries = slices.Delete(r.Entries, 0, n)
			r.Dropped += n
		case len(r.Files) > 0:
			slices.SortStableFunc(r.Files, func(a, b File) int { return a.Last.Compare(b.Last) })
			n, cut := 0, 0
			for n < len(r.Files) && cut < excess {
				cut += encodedLen(r.Files[n]) + 8
				n++
			}
			r.Files = slices.Delete(r.Files, 0, n)
			r.Dropped += n
		case len(r.Tools) > 0:
			r.Tools = nil
		default:
			return
		}
	}
}

func cloneRecord(r *Record) Record {
	c := *r
	c.Entries = slices.Clone(r.Entries)
	for i := range c.Entries {
		c.Entries[i].Choices = slices.Clone(c.Entries[i].Choices)
	}
	c.Files = slices.Clone(r.Files)
	if r.Tools != nil {
		c.Tools = make(map[string]int, len(r.Tools))
		for k, v := range r.Tools {
			c.Tools[k] = v
		}
	}
	c.Resumes = slices.Clone(r.Resumes)
	if r.Launch != nil {
		l := *r.Launch
		l.Args, l.EnvNames = slices.Clone(l.Args), slices.Clone(l.EnvNames)
		c.Launch = &l
	}
	if r.Crew != nil {
		x := *r.Crew
		c.Crew = &x
	}
	if r.Agent.Session != nil {
		x := *r.Agent.Session
		c.Agent.Session = &x
	}
	if c.Entries == nil {
		c.Entries = []Entry{}
	}
	return c
}

// load reads the record of id from its file: one of a later version
// decoded as far as this one knows it (newer), any other strictly.
func (j *Journal) load(id string) (Record, bool, error) {
	if !idPattern.MatchString(id) {
		return Record{}, false, ErrBadID
	}
	return loadFrom(j.sess, id+".json")
}

func loadFrom(sess *store.Store, name string) (Record, bool, error) {
	var raw json.RawMessage
	ok, err := sess.LoadLimit(name, &raw, maxRead)
	if err != nil {
		return Record{}, false, err
	}
	if !ok {
		return Record{}, false, ErrNotFound
	}
	var head struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Record{}, false, &store.ParseError{Name: name, Err: err}
	}
	var r Record
	if head.V > maxV {
		if err := json.Unmarshal(raw, &r); err != nil {
			return Record{}, false, &store.ParseError{Name: name, Err: err}
		}
		return r, true, nil
	}
	if err := store.DecodeStrict(raw, &r); err != nil {
		return Record{}, false, &store.ParseError{Name: name, Err: err}
	}
	if r.ID+".json" != name {
		return Record{}, false, &store.ParseError{Name: name, Err: errors.New("its id is not its name")}
	}
	if r.Entries == nil {
		r.Entries = []Entry{}
	}
	return r, false, nil
}

// Get answers the record of id as the journal holds it now, and whether
// it is of a later version, read as it is.
func (j *Journal) Get(id string) (Record, bool, error) {
	if !idPattern.MatchString(id) {
		return Record{}, false, ErrNotFound
	}
	// Held while it reads: a removal's delete waits for it (forgetLocked).
	j.rmu.RLock()
	defer j.rmu.RUnlock()
	j.mu.Lock()
	if x, ok := j.recs[id]; ok {
		r := cloneRecord(&x.r)
		j.mu.Unlock()
		bound(&r)
		return r, false, nil
	}
	_, removing := j.removing[id]
	j.mu.Unlock()
	if removing {
		return Record{}, false, ErrNotFound
	}
	if f := beforeGet; f != nil {
		f(id)
	}
	return j.load(id)
}

// Page is a page of the records' list: Next, when there are more, is the
// cursor of the next.
type Page struct {
	Records []Summary
	Next    string
}

// List answers the records, newest first by StartedAt (then by id), at
// most limit (1 to MaxPage) after the cursor after ("" for the first page).
// A journal that writes answers from what it keeps; a read-only one reads
// the files.
func (j *Journal) List(after string, limit int) (Page, error) {
	limit = min(max(limit, 1), MaxPage)
	var at time.Time
	var atID string
	if after != "" {
		ns, id, ok := strings.Cut(after, "-")
		n, err := strconv.ParseInt(ns, 10, 64)
		if !ok || err != nil || !idPattern.MatchString(id) {
			return Page{}, ErrBadCursor
		}
		at, atID = time.Unix(0, n).UTC(), id
	}
	var rows []Summary
	j.mu.Lock()
	ro := j.readOnly
	if !ro {
		for _, s := range j.index {
			rows = append(rows, s.Summary)
		}
	}
	j.mu.Unlock()
	if ro {
		var err error
		if rows, err = j.scan(); err != nil {
			return Page{}, err
		}
	}
	sort.Slice(rows, func(a, b int) bool { return newerRow(rows[a], rows[b]) })
	start := 0
	if after != "" {
		start = sort.Search(len(rows), func(i int) bool {
			return newerRow(Summary{StartedAt: at, ID: atID}, rows[i])
		})
	}
	end := min(start+limit, len(rows))
	page := Page{Records: slices.Clone(rows[start:end])}
	if end < len(rows) && end > start {
		last := rows[end-1]
		page.Next = strconv.FormatInt(last.StartedAt.UnixNano(), 10) + "-" + last.ID
	}
	if page.Records == nil {
		page.Records = []Summary{}
	}
	return page, nil
}

// newerRow says a lists before b: started later, or at the same time with
// the greater id.
func newerRow(a, b Summary) bool {
	if !a.StartedAt.Equal(b.StartedAt) {
		return a.StartedAt.After(b.StartedAt)
	}
	return a.ID > b.ID
}

// scanned is a read-only journal's row of a file, as of its size and time.
type scanned struct {
	size int64
	mod  time.Time
	row  Summary
}

// scan answers every record's row from the files, for a journal that does
// not write: a file is read only when it is new to it or its size or time
// changed since it was last read, so a page does not decode every record.
func (j *Journal) scan() ([]Summary, error) {
	docs, err := j.sess.List()
	if err != nil {
		return nil, err
	}
	j.roMu.Lock()
	defer j.roMu.Unlock()
	seen := make(map[string]bool, len(docs))
	rows := make([]Summary, 0, len(docs))
	for _, d := range docs {
		id, ok := fileID(d.Name)
		if !ok {
			continue
		}
		seen[d.Name] = true
		if c, ok := j.roRows[d.Name]; ok && c.size == d.Size && c.mod.Equal(d.ModTime) {
			rows = append(rows, c.row)
			continue
		}
		var row Summary
		r, newer, err := loadFrom(j.sess, d.Name)
		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case err != nil:
			row = unreadableRow(id, d.ModTime).Summary
		default:
			row = summaryOf(&r, newer).Summary
		}
		j.roRows[d.Name] = scanned{size: d.Size, mod: d.ModTime, row: row}
		rows = append(rows, row)
	}
	for name := range j.roRows {
		if !seen[name] {
			delete(j.roRows, name)
		}
	}
	return rows, nil
}

// remove deletes the file of the record id. The caller holds wmu.
func (j *Journal) remove(id string) error {
	if f := beforeDelete; f != nil {
		if err := f(SessionsDir + "/" + id + ".json"); err != nil {
			return err
		}
	}
	return j.sess.Delete(id + ".json")
}

// A record's states, and what a removal (Forget, ForgetAll, and Prune,
// which removes as they do) does in each; the package's doc says what
// orders a removal against every writer.
//
//   - open: its session runs (no end, not lost):
//     Forget refuses it (ErrLive), ForgetAll keeps it and counts it,
//     Prune never chooses it. A record holding a resume's pending receipt
//     is kept the same way (ErrResumePending).
//   - ended, whatever the writer is doing with it (reading its end
//     commit, folding its touches, writing it): chosen
//     under mu, its instance ended at once (choose): out of memory and the
//     index, so no reader is handed it and no hook or writer's work takes
//     anything to it from then on; its file deleted after any write under
//     way (wmu).
//   - removed: no record. Forget answers ErrNotFound.
//   - not removed: its delete failed. The removal answers the error; its
//     row is put back from its file, which holds what was last written,
//     with no instance (restoreLocked): a file with no end is listed lost
//     and that end written at the next pass. What its instance held
//     unwritten is dropped. It is forgettable again at once.

// lockChoose and unlockChoose hold what a removal chooses its records
// under: mu, and rmu taken first, so the reads under way (Get) end before
// a record is hidden and none begins until it is.
func (j *Journal) lockChoose() {
	j.rmu.Lock()
	j.mu.Lock()
}

func (j *Journal) unlockChoose() {
	j.mu.Unlock()
	j.rmu.Unlock()
}

// choose ends the instance of id for a removal that holds wmu: out of what
// a reader is handed (memory, the index), its row kept in removing. The
// caller holds mu.
func (j *Journal) choose(id string) {
	j.removing[id] = j.index[id]
	delete(j.recs, id)
	delete(j.index, id)
}

// forgetLocked deletes the file of the record id, which the caller chose
// (choose). A file not deleted is put back from the file with no instance
// (restoreLocked): nothing that its instance held or that a hook of it
// brings is written, and no end commit is read for it. Either way Removed
// is told. The caller holds wmu.
func (j *Journal) forgetLocked(id string) error {
	err := j.remove(id)
	j.mu.Lock()
	delete(j.removing, id)
	j.mu.Unlock()
	if err != nil {
		j.restoreLocked(id)
	}
	if j.o.Removed != nil {
		j.o.Removed(id)
	}
	return err
}

// restoreLocked puts back the row of id from its file, a delete of it
// having failed: with no instance, so by the rule for a record with no
// instance a file with no end is listed lost (LostAt its file's last
// write, Ending lost, no EndedAt), held in memory, dirty, and that end
// written at the next pass; a file with an end is listed as it says. A
// file that cannot be read is listed unreadable; one that is not there
// any more, not at all. The caller holds wmu.
func (j *Journal) restoreLocked(id string) {
	r, newer, err := j.load(id)
	var mod time.Time
	if fi, serr := os.Stat(filepath.Join(j.sess.Dir(), id+".json")); serr == nil {
		mod = fi.ModTime().UTC()
	} else if err == nil {
		mod = j.o.Now().UTC()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.index[id]; ok {
		return
	}
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		if mod.IsZero() {
			mod = j.o.Now().UTC()
		}
		j.index[id] = unreadableRow(id, mod)
	case newer:
		j.index[id] = summaryOf(&r, true)
	case r.open():
		r.LostAt, r.Ending = mod, lostEnding(&r)
		x := &rec{id: id, r: r, dirty: true, urgent: true, endedAt: j.o.Now()}
		j.recs[id] = x
		j.index[id] = j.row(x)
		j.poke()
	default:
		j.index[id] = summaryOf(&r, false)
	}
}

// Forget deletes the record of id: refused for a live session (ErrLive)
// and for a record holding a resume's pending receipt (ErrResumePending).
func (j *Journal) Forget(id string) error {
	if !idPattern.MatchString(id) {
		return ErrNotFound
	}
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.lockChoose()
	if err := j.writable(); err != nil {
		j.unlockChoose()
		return err
	}
	s, ok := j.index[id]
	switch {
	case !ok:
		j.unlockChoose()
		return ErrNotFound
	case s.open:
		j.unlockChoose()
		return ErrLive
	case s.pending:
		j.unlockChoose()
		return ErrResumePending
	}
	j.choose(id)
	j.unlockChoose()
	return j.forgetLocked(id)
}

// ForgetAll deletes every record that is neither live nor holds a pending
// receipt, and counts those it kept. Only a record whose file was deleted
// is counted forgotten; one that was not stays, and the first error is
// answered.
func (j *Journal) ForgetAll() (forgotten, kept int, err error) {
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.lockChoose()
	if err := j.writable(); err != nil {
		j.unlockChoose()
		return 0, 0, err
	}
	var gone []string
	for id, s := range j.index {
		if s.open || s.pending {
			kept++
			continue
		}
		gone = append(gone, id)
		j.choose(id)
	}
	j.unlockChoose()
	slices.Sort(gone)
	for _, id := range gone {
		if derr := j.forgetLocked(id); derr != nil {
			if err == nil {
				err = derr
			}
			continue
		}
		forgotten++
	}
	return forgotten, kept, err
}

// Prune deletes the ended records past MaxRecords: hosted sessions'
// records first, then the server's own sessions', each those that stopped
// being open longest ago first (by their end, else when they were last
// observed); a live one, one holding a pending receipt and one of a later
// version are never deleted and never counted.
func (j *Journal) Prune() {
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.pruneLocked()
}

// pruneLocked is Prune. The caller holds wmu.
func (j *Journal) pruneLocked() {
	j.lockChoose()
	if j.writable() != nil {
		j.unlockChoose()
		return
	}
	var ended []*summary
	for _, s := range j.index {
		if !s.open && !s.pending && !s.Newer {
			ended = append(ended, s)
		}
	}
	if len(ended) <= j.maxRecords {
		j.unlockChoose()
		return
	}
	// The kept come first: the server's own sessions' records before any
	// hosted one's, so a hosted record goes before any of the server's
	// own, whatever the number of them; then the latest first.
	sort.Slice(ended, func(a, b int) bool {
		if x, y := ended[a].Host.Hosted, ended[b].Host.Hosted; x != y {
			return y
		}
		if x, y := ended[a].stopped, ended[b].stopped; !x.Equal(y) {
			return x.After(y)
		}
		return ended[a].ID > ended[b].ID
	})
	var gone []string
	for _, s := range ended[j.maxRecords:] {
		gone = append(gone, s.ID)
		j.choose(s.ID)
	}
	j.unlockChoose()
	for _, id := range gone {
		if err := j.forgetLocked(id); err != nil {
			j.log.Warn("a pruned session's record was not removed; it is tried again", "session", id, "err", err)
		}
	}
}

// recover is Recover at Open, the lock held.
func (j *Journal) recover() error {
	j.wmu.Lock()
	defer j.wmu.Unlock()
	return j.recoverLocked()
}

// recoverLocked reads every record and the state file, run once the lock
// is held and before the first Begin: a record with no end is marked as no
// longer observed (EndingLost, LostAt its file's last write and EndedAt
// left empty),
// never as ended; a resume's pending receipt is settled by what the data
// directory shows (the allocated session's record there: started; none:
// uncertain); the counter is taken past every Seq kept; then the ended
// records past MaxRecords are pruned. The caller holds wmu.
func (j *Journal) recoverLocked() error {
	docs, err := j.sess.List()
	if err != nil {
		return err
	}
	type found struct {
		r     Record
		newer bool
		mod   time.Time
	}
	var all []found
	var unread []*summary
	ids := map[string]bool{}
	var maxSeq int64
	for _, d := range docs {
		r, newer, err := loadFrom(j.sess, d.Name)
		if err != nil {
			// Kept as it is, and in the index as unreadable: listed,
			// forgettable, counted toward MaxRecords, never written over.
			j.log.Warn("a session's record was not read; it is kept as it is, listed as unreadable", "file", d.Name, "err", err)
			if id, ok := fileID(d.Name); ok {
				unread = append(unread, unreadableRow(id, d.ModTime))
			}
			continue
		}
		all = append(all, found{r, newer, d.ModTime})
		ids[r.ID] = true
		maxSeq = max(maxSeq, r.Seq)
		for _, e := range r.Entries {
			maxSeq = max(maxSeq, e.Seq)
		}
	}
	var st state
	if _, err := j.st.Load(StateFile, &st); err != nil {
		j.log.Warn("the session records' state was not read; its counter is taken from the records", "err", err)
	}
	j.mu.Lock()
	j.seq, j.high = max(j.seq, st.Seq, maxSeq), st.Seq
	for _, s := range unread {
		j.index[s.ID] = s
	}
	var changed []*rec
	for _, f := range all {
		r := f.r
		if f.newer {
			j.index[r.ID] = summaryOf(&r, true)
			continue
		}
		dirty := false
		if r.open() {
			r.LostAt, r.Ending = f.mod.UTC(), lostEnding(&r)
			dirty = true
		}
		for i := range r.Resumes {
			x := &r.Resumes[i]
			if x.State != ResumePending {
				continue
			}
			if x.Session != "" && ids[x.Session] {
				x.State = ResumeStarted
			} else {
				x.State, x.Error = ResumeUncertain, "the server stopped while starting the session; its process may have started before its record was written"
			}
			dirty = true
		}
		j.index[r.ID] = summaryOf(&r, false)
		if dirty {
			x := &rec{id: r.ID, r: r, dirty: true, endedAt: j.o.Now()}
			j.recs[r.ID] = x
			changed = append(changed, x)
		}
	}
	j.mu.Unlock()
	sort.Slice(changed, func(a, b int) bool { return changed[a].r.ID < changed[b].r.ID })
	for _, x := range changed {
		if err := j.write(x); err != nil {
			return fmt.Errorf("journal: recover %s: %w", x.id, err)
		}
	}
	j.mu.Lock()
	ro := j.readOnly
	j.readOnly = false
	j.mu.Unlock()
	j.pruneLocked()
	j.mu.Lock()
	j.readOnly = ro
	j.mu.Unlock()
	return nil
}
