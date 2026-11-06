package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// flushEvery is how often a dirty record is written at most, and how long
// after its end a record is kept in memory; a test makes it longer.
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

// idPattern is a session's id, a server session's and a hosted one's alike.
var idPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// The seams of the package's tests: takeLock is store.TakeOwnerLock;
// beforeWrite, when set, runs before each document is written with its
// name ("state.json", "sessions/<id>.json"), an error it answers the
// write's; beforeDelete the same before a record's file is deleted, and
// beforeLoad before a record's earlier file is read to be merged (merge),
// with its id.
var (
	takeLock     = store.TakeOwnerLock
	beforeWrite  func(name string) error
	beforeDelete func(name string) error
	beforeLoad   func(id string) error
)

// Meta is what the journal reads of a session as it is now (Current): its
// name, its agent's own session, its crew, and its end once it has one.
type Meta struct {
	Name         string
	AgentSession *AgentSession
	Crew         *Crew
	Ended        bool
	EndedAt      time.Time
	Ending       string // "exited (exit 0)", "stopped"
}

// Ending is how a session ended (End): an observed end with its time and
// words, or (Lost) the server no longer observing it.
type Ending struct {
	At   time.Time
	How  string
	Lost bool
}

// Options are Open's.
type Options struct {
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Log is where the journal says what it skipped; slog.Default when nil.
	Log *slog.Logger
	// Current answers a session as it is now, false for one the server
	// no longer holds. It is called on the writer's goroutine, never under
	// the journal's lock.
	Current func(id string) (Meta, bool)
	// EndCommit answers the commit a record's folder is at after its end,
	// "" for none or unknown; called on the writer's goroutine, several at
	// once (at most endReaders). ctx ends when Close gives up on the reads
	// (closeWait): an answer after that is not kept.
	EndCommit func(ctx context.Context, r Record) string
	// Denied says the session's deny list refuses path; such a file is
	// never written. Called on the writer's goroutine.
	Denied func(id, path string) bool
}

// Journal is the session records, opened by Open. Every method is safe
// for concurrent use; those a hook calls (Note, Chat, Touch, Tool,
// Changed, End and a hosted Begin) never wait on a disk.
type Journal struct {
	st, sess *store.Store
	o        Options
	log      *slog.Logger

	// wmu orders the disk's writes (a record and the state file before
	// it, a delete), taken before mu.
	wmu sync.Mutex
	mu  sync.Mutex
	// seq is the counter: the last Seq given an entry or a write; high
	// the mark state.json holds, past every Seq written. A write whose Seq
	// passes it writes state.json first, with a mark seqBlock further on,
	// so a Seq never goes back after a restart and the state file is
	// written once per seqBlock writes, not with each.
	seq      int64
	high     int64
	recs     map[string]*rec     // the records in memory: open, or ended a moment ago
	index    map[string]*summary // every record on disk or in memory
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

// rec is a record in memory.
type rec struct {
	r Record
	// dirty: changed since its last write; urgent: to be written at once
	// (a hosted record begun, an end, a crew taken); stale: its metadata
	// to be read through Current; needEnd: its end commit to be read;
	// load: its earlier file still to be read and what came meanwhile
	// merged into it (merge): a hosted record its host took up again, or
	// one a late hook brought back after it left memory.
	dirty, urgent, stale, needEnd, load bool
	// host is the registration a hosted record taken up again brings, kept
	// for its merge; loadWarned says its failed read was logged.
	host       *Record
	loadWarned bool
	touches    []touch
	// final is what the session was as it left the server, and denied its
	// deny list (Gone): read for the work still pending once the server no
	// longer lists it.
	final  *Meta
	denied func(path string) bool
	// endedAt is when its end was set here, for its eviction.
	endedAt time.Time
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
	// stopped is when the record stopped being open (its end, else when it
	// was last observed; an unreadable file's last write): Prune keeps the
	// latest. evicted: the record left memory here, so a late hook may
	// bring it back (held).
	stopped time.Time
	evicted bool
}

// Summary is a record's row in a list.
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

func summaryOf(r *Record, newer bool) *summary {
	s := &summary{Summary: Summary{ID: r.ID, Name: r.Name, Kind: r.Kind, Agent: r.Agent, Host: r.Host, Where: r.Where, Cwd: r.Cwd, Repo: r.Repo, Branch: r.Branch,
		Crew: r.Crew, StartedAt: r.StartedAt, EndedAt: r.EndedAt, LostAt: r.LostAt, Ending: r.Ending, Entries: len(r.Entries), Files: len(r.Files), Dropped: r.Dropped, Newer: newer},
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
	j := &Journal{st: h, sess: sess, o: o, log: o.Log, recs: map[string]*rec{}, index: map[string]*summary{},
		wake: make(chan struct{}, 1), flushes: make(chan chan struct{}), stop: make(chan struct{}), stopped: make(chan struct{}),
		done: make(chan struct{}), shut: make(chan struct{}), roRows: map[string]scanned{}}
	j.reads, j.stopReads = context.WithCancel(context.Background())
	if j.o.Now == nil {
		j.o.Now = time.Now
	}
	if j.log == nil {
		j.log = slog.Default()
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

// Begin keeps the record of a session as it is added: in memory at once,
// so the record exists before the session's first entry. A server
// session's record is written before Begin returns; a hosted one's (Host.
// Hosted) is handed to the writer, which writes it before anything else
// it holds, since its caller reads the host's connection. An id that has a
// record is refused (ErrExists) unless both are the same host's hosted
// session (the same Host.Owner) and the record is open or no longer
// observed (EndingHostAway, EndingLost): that record is taken up again,
// its entries and Seq kept, a status entry saying so.
func (j *Journal) Begin(r Record) error {
	if !idPattern.MatchString(r.ID) {
		return ErrBadID
	}
	r.V, r.Seq = V, 0
	r.EndedAt, r.LostAt, r.Ending, r.End, r.Reopened, r.Dropped = time.Time{}, time.Time{}, "", "", 0, 0
	r.Entries, r.Files, r.Tools, r.Resumes = []Entry{}, nil, nil, nil
	if r.StartedAt.IsZero() {
		r.StartedAt = j.o.Now()
	}
	r.StartedAt = r.StartedAt.UTC()
	fitRecord(&r)
	if r.Host.Hosted {
		return j.beginHosted(r)
	}
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.mu.Lock()
	if err := j.writable(); err != nil {
		j.mu.Unlock()
		return err
	}
	if _, ok := j.index[r.ID]; ok {
		j.mu.Unlock()
		j.log.Warn("a session's record was not begun: its id has one", "session", r.ID)
		return ErrExists
	}
	x := &rec{r: r, dirty: true}
	j.recs[r.ID] = x
	j.index[r.ID] = summaryOf(&x.r, false)
	j.mu.Unlock()
	return j.write(x)
}

// beginHosted is Begin of a hosted session's record: its file is left to
// the writer.
func (j *Journal) beginHosted(r Record) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.writable(); err != nil {
		return err
	}
	now := j.o.Now().UTC()
	if ex, ok := j.index[r.ID]; ok {
		if ex.Unreadable || !ex.Host.Hosted || ex.owner != r.Host.Owner || ex.Newer || !ex.EndedAt.IsZero() {
			j.log.Warn("a hosted session's record was not begun: its id has another", "session", r.ID)
			// No hook of this session brings that record back (held).
			if ex.evicted {
				cp := *ex
				cp.evicted = false
				j.index[r.ID] = &cp
			}
			return ErrExists
		}
		taken := Entry{At: now, Kind: KindStatus, Message: "taken up again by its host"}
		j.seq++
		taken.Seq = j.seq
		host := r
		host.Entries = nil
		if x, ok := j.recs[r.ID]; ok && !x.load {
			reopen(&x.r, r)
			x.r.Entries = insertEntry(x.r.Entries, taken)
			x.dirty, x.urgent, x.endedAt = true, true, time.Time{}
			j.index[r.ID] = summaryOf(&x.r, false)
			j.poke()
			return nil
		} else if ok {
			// Its file is still to be read (a late hook brought it back, or
			// the host registered before): taken up with it (merge).
			x.host = &host
			x.r.LostAt, x.r.Ending = time.Time{}, ""
			x.r.Entries = insertEntry(x.r.Entries, taken)
			x.dirty, x.urgent, x.endedAt = true, true, time.Time{}
		} else {
			r.Entries = []Entry{taken}
			j.recs[r.ID] = &rec{r: r, dirty: true, urgent: true, load: true, host: &host}
		}
		row := *ex
		row.open, row.LostAt, row.Ending, row.stopped, row.evicted = true, time.Time{}, "", time.Time{}, false
		j.index[r.ID] = &row
		j.poke()
		return nil
	}
	x := &rec{r: r, dirty: true, urgent: true}
	j.recs[r.ID] = x
	j.index[r.ID] = summaryOf(&x.r, false)
	j.poke()
	return nil
}

// reopen takes old up again for its host's new registration fresh: no
// longer lost, counted, what the host says of it now.
func reopen(old *Record, fresh Record) {
	old.LostAt, old.Ending = time.Time{}, ""
	old.Reopened++
	old.Name, old.Host.Name, old.Host.User, old.Branch, old.Cwd = fresh.Name, fresh.Host.Name, fresh.Host.User, fresh.Branch, fresh.Cwd
	if old.Agent.ID == "" {
		old.Agent = fresh.Agent
	}
}

// Abandon removes the record of a session that was never added (the
// registry refused it): from memory and from the disk.
func (j *Journal) Abandon(id string) {
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.mu.Lock()
	if j.writable() != nil {
		j.mu.Unlock()
		return
	}
	delete(j.recs, id)
	delete(j.index, id)
	j.mu.Unlock()
	if err := j.sess.Delete(id + ".json"); err != nil {
		j.log.Warn("an abandoned session's record was not removed", "session", id, "err", err)
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
func (j *Journal) Note(id string, a Activity) {
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
		j.Tool(id, a.Tool)
		return
	case actFile:
		if a.Op != "" && a.Path != "" {
			j.Touch(id, File{Path: a.Path, Op: a.Op, Count: 1, Last: a.At})
		}
		return
	default:
		return
	}
	e.At = a.At
	j.add(id, e)
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
func (j *Journal) Chat(id string, c ChatLine) {
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
	j.add(id, e)
}

// held is the record of id in memory for a hook, brought back when it
// left memory after its end (evicted): what a late callback carries is
// taken all the same, its file read and merged by the writer (load).
// Nil for an id this journal keeps no record of, or one it does not
// write. The caller holds mu.
func (j *Journal) held(id string) *rec {
	if j.writable() != nil {
		return nil
	}
	if x, ok := j.recs[id]; ok {
		return x
	}
	s, ok := j.index[id]
	if !ok || !s.evicted || s.Newer || s.Unreadable {
		return nil
	}
	x := &rec{r: Record{ID: id, EndedAt: s.EndedAt, LostAt: s.LostAt, Ending: s.Ending}, load: true, endedAt: j.o.Now()}
	j.recs[id] = x
	j.poke()
	return x
}

// add keeps e in the record of id, in its place by At then by arrival. An
// entry after the record's end is dropped and counted; one an earlier
// callback still carried is kept, also once the record has left memory.
func (j *Journal) add(id string, e Entry) {
	fitEntry(&e)
	if e.At.IsZero() {
		e.At = j.o.Now().UTC()
	}
	e.At = e.At.UTC()
	j.mu.Lock()
	defer j.mu.Unlock()
	x := j.held(id)
	if x == nil {
		return
	}
	if end := endOf(&x.r); !end.IsZero() && e.At.After(end) {
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
// row per path and op once the writer has asked Denied of it.
func (j *Journal) Touch(id string, f File) {
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
	x := j.held(id)
	if x == nil {
		return
	}
	if end := endOf(&x.r); (!end.IsZero() && f.Last.After(end)) || len(x.touches) >= maxHeld || len(t.path) > maxTouchPath {
		x.r.Dropped++
		x.dirty = true
		return
	}
	x.touches = append(x.touches, t)
	x.dirty = true
}

// Tool counts a tool use, at most MaxTools tools: a tool past them is not
// counted.
func (j *Journal) Tool(id, name string) {
	name = fitPath(name, MaxTool)
	if name == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	x := j.held(id)
	if x == nil {
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

// Changed marks the session's metadata stale: the writer reads it as it is
// then (Current), never from a snapshot a caller hands over, so a change
// that reached the hook late never overtakes a newer one. It never waits.
func (j *Journal) Changed(id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if x := j.held(id); x != nil {
		x.stale = true
		j.poke()
	}
}

// Gone keeps what a session was as it left the server (final) and its
// deny list (denied, nil for none): the writer reads them for the work it
// still holds of it (its metadata to read, its touches to ask of a deny
// list) once Current no longer answers it. It never waits.
func (j *Journal) Gone(id string, final Meta, denied func(path string) bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	x, ok := j.recs[id]
	if !ok || j.writable() != nil {
		return
	}
	x.final, x.denied, x.stale = &final, denied, true
	j.poke()
}

// End sets how the session ended, in memory, and has the writer write it
// at once (an observed end with its end commit read first); it never
// waits on a disk. An observed end is kept: once a record has one, a
// later End changes nothing; a lost one (Ending.Lost) only marks a record
// that has no end at all.
func (j *Journal) End(id string, end Ending) {
	if end.At.IsZero() {
		end.At = j.o.Now()
	}
	end.At = end.At.UTC()
	j.mu.Lock()
	defer j.mu.Unlock()
	// A record that left memory with the end this one would set changes
	// nothing, and is not brought back for it.
	if _, in := j.recs[id]; !in {
		if s, ok := j.index[id]; !ok || !s.EndedAt.IsZero() || (end.Lost && !s.LostAt.IsZero()) {
			return
		}
	}
	x := j.held(id)
	if x == nil {
		return
	}
	j.endLocked(x, end)
}

func (j *Journal) endLocked(x *rec, end Ending) {
	if !x.r.EndedAt.IsZero() {
		return
	}
	if end.Lost {
		if !x.r.LostAt.IsZero() {
			return
		}
		x.r.LostAt, x.r.Ending = end.At, cutBytes(end.How, maxField)
	} else {
		x.r.EndedAt, x.r.LostAt, x.r.Ending = end.At, time.Time{}, cutBytes(end.How, maxField)
		x.needEnd = true
	}
	x.dirty, x.urgent, x.endedAt = true, true, j.o.Now()
	if !x.load {
		j.index[x.r.ID] = summaryOf(&x.r, false)
	}
	j.poke()
}

// pass is one turn of the writer: the records whose earlier files are to
// be read merged with them, then the stale metadata read, then the end
// commits read and the touches asked of Denied and folded (collected after
// the metadata, which may end a record), then the records written (every
// dirty one with all, else the urgent ones), the ended ones let go of and
// the ended records past MaxRecords pruned.
func (j *Journal) pass(all bool) {
	for _, x := range j.collect(func(x *rec) bool { return x.load }) {
		j.merge(x)
	}
	for _, x := range j.collect(func(x *rec) bool {
		if x.stale && !x.load {
			x.stale = false
			return true
		}
		return false
	}) {
		j.readMeta(x)
	}
	j.endCommits(j.collect(func(x *rec) bool { return x.needEnd && !x.load }))
	for _, x := range j.collect(func(x *rec) bool { return len(x.touches) > 0 && !x.load }) {
		j.fold(x)
	}
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.mu.Lock()
	var due []*rec
	for id, x := range j.recs {
		if x.load || x.needEnd {
			continue
		}
		if x.urgent || (all && x.dirty) {
			due = append(due, x)
			continue
		}
		if !x.dirty && !x.stale && len(x.touches) == 0 && !x.endedAt.IsZero() && j.o.Now().Sub(x.endedAt) >= flushEvery {
			delete(j.recs, id)
			if s, ok := j.index[id]; ok {
				s.evicted = true
			}
		}
	}
	j.mu.Unlock()
	sort.Slice(due, func(a, b int) bool { return due[a].r.ID < due[b].r.ID })
	for _, x := range due {
		if err := j.write(x); err != nil {
			j.log.Warn("a session's record was not written; it is tried again", "session", x.r.ID, "err", err)
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

// readMeta reads the session of x as it is now and takes what changed: its
// name, its agent's own session, its crew (written at once when it had
// none) and its end. A session the server no longer lists is read as it
// left (Gone).
func (j *Journal) readMeta(x *rec) {
	m, ok := Meta{}, false
	if j.o.Current != nil {
		m, ok = j.o.Current(x.r.ID)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !ok && x.final != nil {
		m, ok = *x.final, true
	}
	if !ok || j.recs[x.r.ID] != x || x.load {
		return
	}
	r := &x.r
	if name := fitPath(m.Name, maxField); name != "" && name != r.Name {
		r.Name, x.dirty = name, true
	}
	if s := m.AgentSession; s != nil {
		c := AgentSession{ID: fitPath(s.ID, maxField), Source: cutBytes(s.Source, 16), Resumable: s.Resumable}
		if r.Agent.Session == nil || *r.Agent.Session != c {
			r.Agent.Session, x.dirty = &c, true
		}
	}
	if c := m.Crew; c != nil {
		before := r.Crew
		if before == nil || *before != *c {
			cp := *c
			r.Crew, x.dirty = &cp, true
			fitRecord(r)
			if before == nil {
				x.urgent = true
			}
		}
	}
	if m.Ended {
		j.endLocked(x, Ending{At: m.EndedAt, How: m.Ending})
	}
	j.index[r.ID] = summaryOf(r, false)
}

// merge reads the earlier file of a record held with load: a hosted record
// its host took up again (host), or one a late hook brought back after it
// left memory. What the file held comes first and what came meanwhile is
// put among it; an end observed meanwhile stands. A file that is not there
// begins a hosted record afresh and lets a late hook's go; one of a later
// version or one this version cannot decode is never written over, its
// row says which; any other failure to read it leaves the record to be
// read again at the next pass, unwritten.
func (j *Journal) merge(x *rec) {
	id := x.r.ID
	var old Record
	var newer bool
	var err error
	if f := beforeLoad; f != nil {
		err = f(id)
	}
	if err == nil {
		old, newer, err = j.load(id)
	}
	var bad *store.ParseError
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.recs[id] != x || !x.load {
		return
	}
	var base Record
	switch {
	case err == nil && newer:
		j.log.Warn("a session's record is of a later version; it is not written over", "session", id)
		delete(j.recs, id)
		j.index[id] = summaryOf(&old, true)
		return
	case err == nil:
		base = old
		if x.host != nil {
			if !old.EndedAt.IsZero() || !old.Host.Hosted || old.Host.Owner != x.host.Host.Owner {
				j.log.Warn("a hosted session's record was not taken up again: it is another's or ended", "session", id)
				delete(j.recs, id)
				j.index[id] = summaryOf(&old, false)
				return
			}
			reopen(&base, *x.host)
		}
	case errors.Is(err, ErrNotFound) && x.host != nil:
		base = *x.host
		base.Entries = []Entry{}
	case errors.Is(err, ErrNotFound):
		delete(j.recs, id)
		delete(j.index, id)
		return
	case errors.As(err, &bad):
		j.log.Warn("a session's record was not read; it is kept as it is, listed as unreadable", "session", id, "err", err)
		delete(j.recs, id)
		j.index[id] = unreadableRow(id, j.o.Now())
		return
	default:
		if !x.loadWarned {
			x.loadWarned = true
			j.log.Warn("a session's earlier record was not read; it is read again before anything is written", "session", id, "err", err)
		}
		return
	}
	fresh := x.r
	for _, e := range fresh.Entries {
		base.Entries = insertEntry(base.Entries, e)
	}
	base.Dropped += fresh.Dropped
	for k, n := range fresh.Tools {
		if base.Tools == nil {
			base.Tools = map[string]int{}
		}
		if _, known := base.Tools[k]; known || len(base.Tools) < MaxTools {
			base.Tools[k] += n
		}
	}
	for _, f := range fresh.Files {
		base.Files = foldFile(base.Files, f, &base.Dropped)
	}
	switch {
	case !base.EndedAt.IsZero():
	case !fresh.EndedAt.IsZero():
		base.EndedAt, base.LostAt, base.Ending = fresh.EndedAt, time.Time{}, fresh.Ending
	case !fresh.LostAt.IsZero() && base.LostAt.IsZero():
		base.LostAt, base.Ending = fresh.LostAt, fresh.Ending
	}
	x.r = base
	x.load, x.loadWarned = false, false
	if x.host != nil {
		x.dirty, x.urgent, x.host = true, true, nil
	}
	if x.dirty {
		j.index[id] = summaryOf(&x.r, false)
	}
}

// endCommits reads the end commits of the records ended, at most
// endReaders at once. Once Close has given up on them (reads ended,
// closeWait after it began) those not read are written without one, and
// an answer that comes later is not kept.
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
				j.endCommit(ctx, x)
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
		j.mu.Lock()
		for _, x := range ends {
			if x.needEnd {
				x.needEnd = false
				x.dirty, x.urgent = true, true
			}
		}
		j.mu.Unlock()
	}
}

// endCommit reads the end commit of an ended record.
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
	if !x.needEnd {
		return
	}
	x.needEnd = false
	x.r.End = fitPath(commit, 64)
	x.dirty, x.urgent = true, true
}

// fold asks Denied (the session's own deny list once it is gone) of each
// touch x holds, by the path as it came, and folds the rest into its files.
func (j *Journal) fold(x *rec) {
	j.mu.Lock()
	touches := x.touches
	x.touches = nil
	id := x.r.ID
	denied := x.denied
	j.mu.Unlock()
	if denied == nil && j.o.Denied != nil {
		denied = func(path string) bool { return j.o.Denied(id, path) }
	}
	kept := touches[:0]
	for _, t := range touches {
		if denied != nil && denied(t.path) {
			continue
		}
		kept = append(kept, t)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
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
	if j.recs[x.r.ID] != x {
		j.mu.Unlock()
		return nil
	}
	// Measured with the Seq it is written with, which may be longer.
	j.seq++
	x.r.Seq = j.seq
	bound(&x.r)
	r := cloneRecord(&x.r)
	x.dirty, x.urgent = false, false
	j.index[r.ID] = summaryOf(&x.r, false)
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
	if head.V > V {
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
	j.mu.Lock()
	if x, ok := j.recs[id]; ok && !x.load {
		r := cloneRecord(&x.r)
		j.mu.Unlock()
		bound(&r)
		return r, false, nil
	}
	j.mu.Unlock()
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

// forgetLocked lets go of the record id once its file is deleted; a file
// not deleted keeps it, listed and to be forgotten again. The caller holds
// wmu, so no write comes between.
func (j *Journal) forgetLocked(id string) error {
	if err := j.remove(id); err != nil {
		return err
	}
	j.mu.Lock()
	delete(j.recs, id)
	delete(j.index, id)
	j.mu.Unlock()
	return nil
}

// Forget deletes the record of id: refused for a live session (ErrLive)
// and for a record holding a resume's pending receipt (ErrResumePending).
func (j *Journal) Forget(id string) error {
	if !idPattern.MatchString(id) {
		return ErrNotFound
	}
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.mu.Lock()
	if err := j.writable(); err != nil {
		j.mu.Unlock()
		return err
	}
	s, ok := j.index[id]
	switch {
	case !ok:
		j.mu.Unlock()
		return ErrNotFound
	case s.open:
		j.mu.Unlock()
		return ErrLive
	case s.pending:
		j.mu.Unlock()
		return ErrResumePending
	}
	j.mu.Unlock()
	return j.forgetLocked(id)
}

// ForgetAll deletes every record that is neither live nor holds a pending
// receipt, and counts those it kept. Only a record whose file was deleted
// is counted forgotten; one that was not stays, and the first error is
// answered.
func (j *Journal) ForgetAll() (forgotten, kept int, err error) {
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.mu.Lock()
	if err := j.writable(); err != nil {
		j.mu.Unlock()
		return 0, 0, err
	}
	var gone []string
	for id, s := range j.index {
		if s.open || s.pending {
			kept++
			continue
		}
		gone = append(gone, id)
	}
	j.mu.Unlock()
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

// Prune deletes the ended records past MaxRecords, those that stopped
// being open longest ago first (by their end, else when they were last
// observed); a live one, one holding a pending receipt and one of a later
// version are never deleted and never counted.
func (j *Journal) Prune() {
	j.wmu.Lock()
	defer j.wmu.Unlock()
	j.pruneLocked()
}

// maxRecords is MaxRecords; a test makes it smaller.
var maxRecords = MaxRecords

// pruneLocked is Prune. The caller holds wmu.
func (j *Journal) pruneLocked() {
	j.mu.Lock()
	if j.writable() != nil {
		j.mu.Unlock()
		return
	}
	var ended []*summary
	for _, s := range j.index {
		if !s.open && !s.pending && !s.Newer {
			ended = append(ended, s)
		}
	}
	if len(ended) <= maxRecords {
		j.mu.Unlock()
		return
	}
	sort.Slice(ended, func(a, b int) bool {
		if x, y := ended[a].stopped, ended[b].stopped; !x.Equal(y) {
			return x.After(y)
		}
		return ended[a].ID > ended[b].ID
	})
	var gone []string
	for _, s := range ended[maxRecords:] {
		gone = append(gone, s.ID)
	}
	j.mu.Unlock()
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
// longer observed (a server session's EndingLost, a hosted one's
// EndingHostAway, LostAt its file's last write and EndedAt left empty),
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
			r.LostAt, r.Ending = f.mod.UTC(), EndingLost
			if r.Host.Hosted {
				r.Ending = EndingHostAway
			}
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
			x := &rec{r: r, dirty: true, endedAt: j.o.Now()}
			j.recs[r.ID] = x
			changed = append(changed, x)
		}
	}
	j.mu.Unlock()
	sort.Slice(changed, func(a, b int) bool { return changed[a].r.ID < changed[b].r.ID })
	for _, x := range changed {
		if err := j.write(x); err != nil {
			return fmt.Errorf("journal: recover %s: %w", x.r.ID, err)
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
