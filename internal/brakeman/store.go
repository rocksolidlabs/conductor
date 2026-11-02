// Package brakeman is the record of Brakeman, Conductor's sidekick agent
// (docs/plans/brakeman.md, Program items BK0 to BK3): its configuration,
// the requests people make of it, the briefs its turns hand back and the
// evidence references they cite, kept as JSON documents in the data
// directory's brakeman/ (0700). Like internal/dispatch it has no HTTP, no
// session and no process: the API, the CLI and the tests are its callers.
// It imports the standard library and internal/store alone; what it
// cannot check itself (an agent, a folder, a crew's body) it asks of
// Facts, which internal/api implements, so it never imports internal/crew
// and internal/crew never imports it.
//
// The documents, each measured as store.Encode writes it before any
// write, so what is written always loads back:
//
//	config.json                    the configuration (Config)
//	state.json                     the last number, the op ids, the day's charges
//	owner.lock                     the owner lock (store.OwnerLock)
//	requests/br-<n>.json           a request (Request), 256 KiB
//	requests/br-<n>-brief.json     its brief (Brief), 256 KiB
//	requests/br-<n>-refs.json      the brief's resolutions, 4 MiB
//	requests/br-<n>-reads.json     the turn's read log, 2 MiB
//	requests/br-<n>-make-<i>.json  a pending make's input, 4 MiB
//
// A request's siblings are written before the request write that names
// them, and removed after the request when it is pruned; at Open, siblings
// a request does not name (a crash between the two writes) are removed.
package brakeman

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// The store's document names and bounds.
const (
	Dir          = "brakeman"
	RequestsDir  = "requests"
	ConfigFile   = "config.json"
	StateFile    = "state.json"
	Lock         = "owner.lock"
	MaxConfigDoc = 64 << 10
	MaxStateDoc  = 1 << 20
	MaxRefsDoc   = 4 << 20
	MaxReadsDoc  = 2 << 20
	MaxMakeDoc   = 4 << 20
	MaxReadLog   = 5000 // entries of a read log
	MaxOps       = 1000 // op ids state.json keeps
	Keep         = 500  // closed requests Prune keeps
	MaxPage      = 100  // requests a List page
	ChargeDays   = 8    // local days state.json keeps charges for
	MaxURL       = 1 << 10
	MaxRefState  = 32
)

// Errors of the store.
var (
	ErrNotFound    = errors.New("brakeman: no such request")
	ErrNotOwner    = errors.New("brakeman: another server owns Brakeman's records")
	ErrStale       = errors.New("brakeman: stale_revision: the configuration changed since it was read")
	ErrOpConflict  = errors.New("brakeman: op_conflict: the op id names a request that asked something else")
	ErrBriefExists = errors.New("brakeman: brief_exists: the request has a brief")
	ErrBound       = errors.New("brakeman: bound")
	ErrClosed      = errors.New("brakeman: the request is closed")
	ErrLaterSchema = errors.New("brakeman: a record of a later schema, kept as it is")
	ErrOverBudget  = errors.New("brakeman: over_budget: the day's turns are spent")
	ErrNotQueued   = errors.New("brakeman: the request is not queued")
	ErrNotTurn     = errors.New("brakeman: the request is not starting or running")
	ErrDamaged     = errors.New("brakeman: a request that does not parse, kept as it is")
	ErrPruned      = errors.New("brakeman: pruned: the op id's request was made and has since been pruned")
)

// ErrStateDamaged is a state.json that does not parse, is incomplete, or
// is behind the requests kept: no number is given from it.
var ErrStateDamaged = errors.New("brakeman: state.json is damaged or behind the requests, kept as it is")

// LockWait is how long a store that found the owner lock held keeps
// trying for it after Open, as the ledger does (ledger.LockWait): a
// desktop restart overlaps the old server's shutdown.
var LockWait = 10 * time.Second

// lockTick is how often it tries.
const lockTick = 250 * time.Millisecond

// Options are Open's.
type Options struct {
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Location is the zone of the day a charge is counted in; time.Local
	// when nil.
	Location *time.Location
	// DefaultCwd is the folder a configuration nobody saved names.
	DefaultCwd string
	// Log is where a record skipped is said; slog.Default when nil.
	Log *slog.Logger
}

// Store is Brakeman's records, opened by Open. Every write is under one
// mutex, a request read, changed and written in turn.
type Store struct {
	st, reqs *store.Store
	now      func() time.Time
	loc      *time.Location
	cwd      string
	log      *slog.Logger

	mu       sync.Mutex
	closed   bool
	readOnly bool
	why      string
	lock     *store.OwnerLock
	done     chan struct{}
	// shut is closed once a Close has waited for OnOwned's function and
	// let the lock go: every Close returns only then.
	shut     chan struct{}
	onOwned  func()
	owned    bool // the lock was taken after Open
	notified bool
	// inOwned counts OnOwned's function while it runs: Close waits for it
	// before it lets the lock go, so the recovery it runs never outlives
	// the store's ownership.
	inOwned sync.WaitGroup
	// skipped is the records of a later schema already logged.
	skipped map[string]bool
}

// keepNewest is Keep; a test makes it smaller.
var keepNewest = Keep

// takeLock is store.TakeOwnerLock; the tests make it fail.
var takeLock = store.TakeOwnerLock

// beforeWrite, when a test sets it, runs before each document is written
// with its name ("state.json", "requests/br-1.json"); an error it
// answers is the write's.
var beforeWrite func(name string) error

// Open takes brakeman/ under the data directory st holds (0700) and its
// owner lock, brakeman/owner.lock, and reaches no other store. Once it
// holds the lock it concludes what the last owner left open in Brakeman's
// own documents: a starting or running request is interrupted, with why,
// and orphan siblings are removed; a queued one stays queued. A store
// that finds the lock held opens read-only (every write answers
// ErrNotOwner with the holder's words), keeps trying for it for
// LockWait, and concludes only once it holds it (OnOwned). Where locks are
// unsupported it stays read-only, as the ledger does.
func Open(st *store.Store, o Options) (*Store, error) {
	b, err := st.Sub(Dir)
	if err != nil {
		return nil, err
	}
	reqs, err := b.Sub(RequestsDir)
	if err != nil {
		return nil, err
	}
	s := &Store{st: b, reqs: reqs, now: o.Now, loc: o.Location, cwd: o.DefaultCwd, log: o.Log, done: make(chan struct{}), shut: make(chan struct{}), skipped: map[string]bool{}}
	if s.now == nil {
		s.now = time.Now
	}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	lockPath := filepath.Join(b.Dir(), Lock)
	lock, why, held, err := takeLock(lockPath)
	switch {
	case err != nil:
		s.readOnly, s.why = true, err.Error()
	case lock == nil:
		s.readOnly, s.why = true, why
		if held {
			s.why = "waiting for the owner lock: " + why
			go s.awaitLock(lockPath)
		}
	default:
		s.lock = lock
		if err := s.conclude(); err != nil {
			s.lock.Release()
			return nil, err
		}
	}
	return s, nil
}

// awaitLock tries for the owner lock every lockTick for up to LockWait,
// as the ledger's awaitLock does: each try under mu and only while the
// store is open, so a Close waits for one under way and none follows.
// Once taken it concludes what the last owner left and runs OnOwned's
// function.
func (s *Store) awaitLock(lockPath string) {
	deadline := time.Now().Add(LockWait)
	tick := time.NewTicker(lockTick)
	defer tick.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-tick.C:
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		lock, why, held, err := takeLock(lockPath)
		switch {
		case err != nil:
			s.why = err.Error()
		case lock != nil:
			s.lock, s.readOnly, s.why = lock, false, ""
			if err := s.concludeLocked(); err != nil {
				s.readOnly, s.why = true, "concluding the last owner's requests: "+err.Error()
			}
			s.owned = true
			cb := s.onOwned
			run := cb != nil && !s.readOnly && !s.notified
			if run {
				s.notified = true
				s.inOwned.Add(1)
			}
			s.mu.Unlock()
			if run {
				defer s.inOwned.Done()
				cb()
			}
			return
		case held && time.Now().Before(deadline):
			s.mu.Unlock()
			continue
		default:
			s.why = why
		}
		s.mu.Unlock()
		return
	}
}

// OnOwned sets what runs once the store has taken the owner lock after
// Open: the desk recovers what it holds of other stores then. A lock taken
// before the call runs fn at once; it runs at most once, never after
// Close, and Close waits for it to return before it lets the lock go, so
// fn must not call Close.
func (s *Store) OnOwned(fn func()) {
	s.mu.Lock()
	s.onOwned = fn
	run := fn != nil && s.owned && !s.readOnly && !s.notified && !s.closed
	if run {
		s.notified = true
		s.inOwned.Add(1)
	}
	s.mu.Unlock()
	if run {
		defer s.inOwned.Done()
		fn()
	}
}

// ReadOnly reports whether the store refuses writes, and why.
func (s *Store) ReadOnly() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readOnly, s.why
}

// Close ends the wait for the owner lock and refuses every write from
// then on; it waits for OnOwned's function to return, if it runs, and
// only then releases the lock, so what that function does under the
// store's ownership is done before another server can take it. Every
// Close returns only then, a second one called meanwhile too.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		if f := closeWaits; f != nil {
			f()
		}
		<-s.shut
		return nil
	}
	s.closed = true
	close(s.done)
	s.mu.Unlock()
	// No function starts once closed is set (both starts check it under
	// mu), so the wait sees every one that did.
	s.inOwned.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	defer close(s.shut)
	if s.lock != nil {
		s.lock.Release()
		s.lock = nil
	}
	return nil
}

// closeWaits, when a test sets it, runs as a Close that found the store
// already closing begins to wait for the first.
var closeWaits func()

// writable answers why a write is refused. The caller holds mu.
func (s *Store) writable() error {
	switch {
	case s.closed:
		return errors.New("brakeman: closed")
	case s.readOnly:
		return fmt.Errorf("%w: %s", ErrNotOwner, s.why)
	}
	return nil
}

func (s *Store) conclude() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.concludeLocked()
}

// concludeLocked turns what the last owner left starting or running into
// interrupted and removes orphan siblings. It completes nothing of
// another store's (Pending lists that for the desk). The caller holds mu
// and the lock.
func (s *Store) concludeLocked() error {
	entries, err := s.reqs.List()
	if err != nil {
		return err
	}
	now := s.now()
	named := map[string]*Request{}
	for _, e := range entries {
		id, ok := requestName(e.Name)
		if !ok {
			continue
		}
		r, err := s.loadLocked(id)
		if skippable(err) {
			named[id] = nil // kept as it is, siblings and all
			continue
		}
		if err != nil {
			return err
		}
		if r.State == StateStarting || r.State == StateRunning {
			if err := r.To(StateInterrupted, "the server stopped while the turn was "+r.State, now); err != nil {
				return err
			}
			if err := s.saveRequestLocked(r); err != nil {
				return err
			}
		}
		named[id] = &r
	}
	for _, e := range entries {
		id, sib, ok := siblingName(e.Name)
		if !ok {
			continue
		}
		r, has := named[id]
		if has && r == nil {
			continue
		}
		if has && r.names(sib) {
			continue
		}
		if err := s.removeLocked(e.Name); err != nil {
			return err
		}
	}
	return nil
}

// names reports whether the request names its sibling: its brief and
// refs once it has a brief, its read log once it began, a make's input
// while it holds the item's entry.
func (r *Request) names(sib string) bool {
	switch {
	case sib == "brief" || sib == "refs":
		return r.HasBrief
	case sib == "reads":
		return r.State != StateQueued
	case strings.HasPrefix(sib, "make-"):
		i, err := strconv.Atoi(strings.TrimPrefix(sib, "make-"))
		return err == nil && slices.ContainsFunc(r.Made, func(m Made) bool { return m.Item == i })
	}
	return false
}

var (
	requestFile = regexp.MustCompile(`^(br-[1-9][0-9]{0,17})\.json$`)
	siblingFile = regexp.MustCompile(`^(br-[1-9][0-9]{0,17})-(brief|refs|reads|make-(?:0|[1-9][0-9]?))\.json$`)
)

// requestName is the request id of a request's document name.
func requestName(name string) (string, bool) {
	m := requestFile.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// siblingName is the request id and the sibling of a sibling's name.
func siblingName(name string) (id, sib string, ok bool) {
	m := siblingFile.FindStringSubmatch(name)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// The names of a request's documents.
func requestDoc(id string) string     { return id + ".json" }
func briefDoc(id string) string       { return id + "-brief.json" }
func refsDoc(id string) string        { return id + "-refs.json" }
func readsDoc(id string) string       { return id + "-reads.json" }
func makeDoc(id string, i int) string { return id + "-make-" + strconv.Itoa(i) + ".json" }
func requestNumber(id string) int64 {
	n, _ := strconv.ParseInt(strings.TrimPrefix(id, "br-"), 10, 64)
	return n
}
func requestID(n int64) string          { return "br-" + strconv.FormatInt(n, 10) }
func ceilMinutes(d time.Duration) int   { return int(math.Ceil(d.Minutes())) }
func (s *Store) day(t time.Time) string { return t.In(s.loc).Format(time.DateOnly) }

// entrySize is what an entry of a request's list takes in the request as
// encode writes it: at the list's depth, with <, > and & as they are (not
// json.MarshalIndent's \u003c), and its indent.
func entrySize(v any) int {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("    ", "  ")
	_ = enc.Encode(v)
	return len(bytes.TrimSuffix(b.Bytes(), []byte("\n"))) + 4
}

// write puts doc in place of name in st after measuring it against
// limit: refused past it with nothing written. The caller holds mu.
func (s *Store) write(st *store.Store, name string, v any, limit int) error {
	b, err := encode(v)
	if err != nil {
		return err
	}
	if len(b) > limit {
		return fmt.Errorf("%w: %s would be %d bytes, at most %d", ErrBound, name, len(b), limit)
	}
	if f := beforeWrite; f != nil {
		path := name
		if st == s.reqs {
			path = RequestsDir + "/" + name
		}
		if err := f(path); err != nil {
			return err
		}
	}
	return st.Save(name, v)
}

// removeLocked deletes a document of requests/.
func (s *Store) removeLocked(name string) error {
	if f := beforeWrite; f != nil {
		if err := f(RequestsDir + "/" + name); err != nil {
			return err
		}
	}
	return s.reqs.Delete(name)
}

// Config answers the configuration; before anyone saved one, the
// defaults, written then when the store may write.
func (s *Store) Config() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Config
	ok, err := s.st.LoadLimit(ConfigFile, &c, MaxConfigDoc)
	if err != nil {
		return Config{}, err
	}
	if ok {
		return c, nil
	}
	c = DefaultConfig(s.cwd)
	c.Rev, c.UpdatedAt = 1, s.now().UTC()
	if s.writable() == nil {
		if err := s.write(s.st, ConfigFile, c, MaxConfigDoc); err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

// SaveConfig saves c, which names the rev it read (ErrStale otherwise),
// after Validate; it bumps the rev and stamps updatedAt.
func (s *Store) SaveConfig(c Config, f Facts) (Config, error) {
	if err := Validate(c, f); err != nil {
		return Config{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return Config{}, err
	}
	var cur Config
	ok, err := s.st.LoadLimit(ConfigFile, &cur, MaxConfigDoc)
	if err != nil {
		return Config{}, err
	}
	if !ok {
		cur.Rev = 1 // the defaults' rev, as Config answers them
	}
	if c.Rev != cur.Rev {
		return Config{}, fmt.Errorf("%w: it is at rev %d, the save names %d", ErrStale, cur.Rev, c.Rev)
	}
	c.Rev, c.UpdatedAt = cur.Rev+1, s.now().UTC()
	if err := s.write(s.st, ConfigFile, c, MaxConfigDoc); err != nil {
		return Config{}, err
	}
	return c, nil
}

// state is brakeman/state.json: the last number given, the op ids of the
// newest MaxOps requests, and the day's charges.
type state struct {
	Schema int     `json:"schema"`
	Last   int64   `json:"last"`
	Ops    []opRef `json:"ops,omitempty"`
	// Days holds, for each of the last ChargeDays local days, a charge a
	// request that started that day, by its id.
	Days map[string]map[string]Charge `json:"days,omitempty"`
}

// opRef is an op id and its request: the hex SHA-256 of what was asked
// (askSum), and whether Prune has removed the request, so a repeat of a
// pruned request is told from a creation whose file never landed.
type opRef struct {
	OpID   string `json:"opId"`
	ID     string `json:"id"`
	Sum    string `json:"sum"`
	Pruned bool   `json:"pruned,omitempty"`
}

// askSum is the hex SHA-256 of what a request asks, as sameAsk compares
// it: its kind, subjects, note and since.
func askSum(r Request) string {
	b, _ := json.Marshal(struct {
		Kind     string    `json:"kind"`
		Subjects []string  `json:"subjects"`
		Note     string    `json:"note"`
		Since    time.Time `json:"since"`
	}{r.Kind, refStrings(r.Subjects), r.Note, r.Since.UTC()})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func refStrings(refs []Ref) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.String())
	}
	return out
}

// Charge is a turn's minutes, reserved at its start and settled after it
// closes (never more than reserved).
type Charge struct {
	Reserved int  `json:"reserved"`
	Settled  *int `json:"settled,omitempty"`
}

// minutes is what the charge counts: its settled minutes, else its
// reservation.
func (c Charge) minutes() int {
	if c.Settled != nil {
		return *c.Settled
	}
	return c.Reserved
}

// loadState reads state.json. The caller holds mu.
func (s *Store) loadState() (state, error) {
	var raw json.RawMessage
	ok, err := s.st.LoadLimit(StateFile, &raw, MaxStateDoc)
	if err != nil {
		return state{}, err
	}
	if !ok {
		return state{Schema: Schema}, nil
	}
	var head struct {
		Schema int `json:"schema"`
	}
	if json.Unmarshal(raw, &head) == nil && head.Schema > Schema {
		return state{}, fmt.Errorf("%w: %s is schema %d", ErrLaterSchema, StateFile, head.Schema)
	}
	var st state
	if err := store.DecodeStrict(raw, &st); err != nil {
		return state{}, fmt.Errorf("%w: parse %s: %w", ErrStateDamaged, StateFile, err)
	}
	if err := st.check(); err != nil {
		return state{}, fmt.Errorf("%w: %s: %v", ErrStateDamaged, StateFile, err)
	}
	return st, nil
}

var sumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// check refuses a state.json that decodes but is none this build wrote:
// null, {} or one without its schema or its last number. Every state.json
// is written by or after a Create, so its last number is at least 1 and
// every op id and charge in it names a request numbered at most that.
// Create would otherwise number from 1 again, over requests kept.
func (st state) check() error {
	if st.Schema != Schema {
		return fmt.Errorf("schema %d, want %d", st.Schema, Schema)
	}
	if st.Last < 1 || !requestIDPattern.MatchString(requestID(st.Last)) {
		return fmt.Errorf("no last number (%d)", st.Last)
	}
	given := func(id string) bool { return requestIDPattern.MatchString(id) && requestNumber(id) <= st.Last }
	for i, o := range st.Ops {
		if !opIDPattern.MatchString(o.OpID) || !given(o.ID) || !sumPattern.MatchString(o.Sum) {
			return fmt.Errorf("ops[%d] is no op id, request at most br-%d and sum", i, st.Last)
		}
	}
	for d, m := range st.Days {
		if _, err := time.Parse(time.DateOnly, d); err != nil {
			return fmt.Errorf("days: %q is no day", d)
		}
		for id, c := range m {
			if !given(id) || c.Reserved < 0 || c.Settled != nil && (*c.Settled < 0 || *c.Settled > c.Reserved) {
				return fmt.Errorf("days[%s]: %q is no charge of a request at most br-%d", d, id, st.Last)
			}
		}
	}
	return nil
}

// saveState writes state.json, its days past ChargeDays dropped. The
// caller holds mu.
func (s *Store) saveState(st state) error {
	st.Schema = Schema
	if len(st.Ops) > MaxOps {
		st.Ops = st.Ops[len(st.Ops)-MaxOps:]
	}
	oldest := s.day(s.now().AddDate(0, 0, -(ChargeDays - 1)))
	for d := range st.Days {
		if d < oldest {
			delete(st.Days, d)
		}
	}
	return s.write(s.st, StateFile, st, MaxStateDoc)
}

// loadLocked reads the request id: ErrNotFound when it is not there,
// ErrLaterSchema (logged once) for one of a later schema. The caller
// holds mu.
func (s *Store) loadLocked(id string) (Request, error) {
	if !requestIDPattern.MatchString(id) {
		return Request{}, ErrNotFound
	}
	name := requestDoc(id)
	b, err := s.readDoc(name, MaxRequestDoc)
	if errors.Is(err, os.ErrNotExist) {
		return Request{}, ErrNotFound
	}
	// A document read whole that is not JSON (empty, cut short) is damaged,
	// as one that is JSON of the wrong shape is below; any other error is the
	// disk's, and is said.
	var pe *store.ParseError
	if errors.As(err, &pe) {
		return Request{}, s.damaged(id, name, err)
	}
	if err != nil {
		return Request{}, err
	}
	var head struct {
		Schema int `json:"schema"`
	}
	if json.Unmarshal(b, &head) == nil && head.Schema > Schema {
		if !s.skipped[id] {
			s.skipped[id] = true
			s.log.Warn("brakeman: a request of a later schema is skipped and kept as it is", "request", id, "schema", head.Schema)
		}
		return Request{}, fmt.Errorf("%w: %s is schema %d", ErrLaterSchema, id, head.Schema)
	}
	var r Request
	if err := store.DecodeStrict(b, &r); err != nil {
		return Request{}, s.damaged(id, name, err)
	}
	if err := r.checkRecord(id); err != nil {
		return Request{}, s.damaged(id, name, err)
	}
	return r, nil
}

// checkRecord refuses a request document that decodes but is no request
// this build wrote under id: null, {} or one missing what Create gives
// every request (its schema, its id, its time, a state, an ask its kind
// takes). Open would otherwise read it as a request that names no
// sibling, and remove its siblings.
func (r Request) checkRecord(id string) error {
	switch {
	case r.Schema != Schema:
		return fmt.Errorf("schema %d, want %d", r.Schema, Schema)
	case r.ID != id:
		return fmt.Errorf("its id is %q, its name %s", r.ID, id)
	case !slices.Contains(States, r.State):
		return fmt.Errorf("no state %q", r.State)
	case r.CreatedAt.IsZero():
		return errors.New("no createdAt")
	}
	ask := Request{OpID: r.OpID, Kind: r.Kind, Subjects: r.Subjects, Note: r.Note, Since: r.Since, By: r.By}
	return ask.checkAsked()
}

// damaged says once that the request id does not parse and answers
// ErrDamaged. The caller holds mu.
func (s *Store) damaged(id, name string, err error) error {
	if !s.skipped[id] {
		s.skipped[id] = true
		s.log.Error("brakeman: a request that does not parse is skipped and kept as it is", "request", id, "err", err)
	}
	return fmt.Errorf("%w: %s: %v", ErrDamaged, name, err)
}

// skippable reports a request a walk over them passes by: gone meanwhile,
// of a later schema, or damaged, each kept as it is (and said once).
func skippable(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrLaterSchema) || errors.Is(err, ErrDamaged)
}

// readDoc reads a document of requests/ through LoadLimit's checks (a
// regular file, at most limit bytes), as raw bytes.
func (s *Store) readDoc(name string, limit int64) ([]byte, error) {
	var raw json.RawMessage
	ok, err := s.reqs.LoadLimit(name, &raw, limit)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, os.ErrNotExist
	}
	return raw, nil
}

// saveRequestLocked measures r and writes it: its lists and fields within
// their bounds, the rest within MaxRequestRest and the whole within
// MaxRequestDoc, or nothing is written. The caller holds mu.
func (s *Store) saveRequestLocked(r Request) error {
	if err := measureRequest(r); err != nil {
		return err
	}
	return s.write(s.reqs, requestDoc(r.ID), r, MaxRequestDoc)
}

// measureRequest holds r to its bounds as it would be written: its lists
// and fields, the rest within MaxRequestRest, the whole within
// MaxRequestDoc.
func measureRequest(r Request) error {
	if err := r.checkEntries(); err != nil {
		return err
	}
	rest := r
	rest.Made, rest.Edges, rest.Acts = nil, nil, nil
	b, err := encode(rest)
	if err != nil {
		return err
	}
	if len(b) > MaxRequestRest {
		return fmt.Errorf("%w: %s without its lists would be %d bytes, at most %d", ErrBound, r.ID, len(b), MaxRequestRest)
	}
	if b, err = encode(r); err != nil {
		return err
	}
	if len(b) > MaxRequestDoc {
		return fmt.Errorf("%w: %s would be %d bytes, at most %d", ErrBound, r.ID, len(b), MaxRequestDoc)
	}
	return nil
}

// Create keeps a new request: in's op id, kind, subjects, note, since and
// by, checked by its kind. Its number is written to state.json before the
// request's own file, so a number is never reused, even after a crash
// between the two. An op id state.json still holds answers its request
// when in asks the same (created false) and ErrOpConflict when it does
// not; once its request is pruned, ErrPruned when in asks the same and
// ErrOpConflict when it does not, so an op id never makes two requests.
// Only an op id whose request never landed makes a new one. The request
// is queued.
func (s *Store) Create(in Request) (r Request, created bool, err error) {
	ask := Request{OpID: in.OpID, Kind: in.Kind, Subjects: in.Subjects, Note: in.Note, Since: in.Since, By: in.By}
	if err := ask.checkAsked(); err != nil {
		return Request{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return Request{}, false, err
	}
	st, err := s.loadState()
	if err != nil {
		return Request{}, false, err
	}
	for i := len(st.Ops) - 1; i >= 0; i-- {
		if st.Ops[i].OpID != ask.OpID {
			continue
		}
		prev, err := s.loadLocked(st.Ops[i].ID)
		if errors.Is(err, ErrNotFound) && st.Ops[i].Pruned {
			if st.Ops[i].Sum != askSum(ask) {
				return Request{}, false, fmt.Errorf("%w: %s, since pruned", ErrOpConflict, st.Ops[i].ID)
			}
			return Request{}, false, fmt.Errorf("%w: %s", ErrPruned, st.Ops[i].ID)
		}
		if errors.Is(err, ErrNotFound) {
			break // its file never landed: a new request
		}
		if err != nil {
			return Request{}, false, err
		}
		if !sameAsk(prev, ask) {
			return Request{}, false, fmt.Errorf("%w: %s", ErrOpConflict, prev.ID)
		}
		return prev, false, nil
	}
	st.Last++
	// The number is new to state.json; a request kept under it says
	// state.json is behind the requests, and nothing is written over it.
	if _, err := os.Lstat(filepath.Join(s.reqs.Dir(), requestDoc(requestID(st.Last)))); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return Request{}, false, err
		}
		return Request{}, false, fmt.Errorf("%w: its last number is %d and %s is kept", ErrStateDamaged, st.Last-1, requestDoc(requestID(st.Last)))
	}
	r = ask
	r.Schema, r.ID, r.CreatedAt, r.State = Schema, requestID(st.Last), s.now().UTC(), StateQueued
	if !r.Since.IsZero() {
		r.Since = r.Since.UTC()
	}
	st.Ops = slices.DeleteFunc(st.Ops, func(o opRef) bool { return o.OpID == ask.OpID })
	st.Ops = append(st.Ops, opRef{OpID: ask.OpID, ID: r.ID, Sum: askSum(ask)})
	if err := s.saveState(st); err != nil {
		return Request{}, false, err
	}
	if err := s.saveRequestLocked(r); err != nil {
		return Request{}, false, err
	}
	return r, true, nil
}

// ByOp answers the request an op id names, when state.json still holds
// it.
func (s *Store) ByOp(opID string) (Request, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.loadState()
	if err != nil {
		return Request{}, false, err
	}
	for i := len(st.Ops) - 1; i >= 0; i-- {
		if st.Ops[i].OpID == opID {
			r, err := s.loadLocked(st.Ops[i].ID)
			if errors.Is(err, ErrNotFound) {
				return Request{}, false, nil
			}
			return r, err == nil, err
		}
	}
	return Request{}, false, nil
}

// Get answers the request id.
func (s *Store) Get(id string) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(id)
}

// ids is every request's id, newest first. The caller holds mu.
func (s *Store) ids() ([]string, error) {
	entries, err := s.reqs.List()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if id, ok := requestName(e.Name); ok {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return int(requestNumber(b) - requestNumber(a)) })
	return out, nil
}

// List answers up to limit requests (at most MaxPage), newest first,
// after the request after ("" for the newest); one of a later schema is
// skipped. next is the id to ask after for the rest, "" for none.
func (s *Store) List(after string, limit int) (page []Request, next string, err error) {
	if limit <= 0 || limit > MaxPage {
		limit = MaxPage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, err := s.ids()
	if err != nil {
		return nil, "", err
	}
	if after != "" {
		n := requestNumber(after)
		ids = slices.DeleteFunc(ids, func(id string) bool { return requestNumber(id) >= n })
	}
	for i, id := range ids {
		if len(page) == limit {
			return page, ids[i-1], nil
		}
		r, err := s.loadLocked(id)
		if skippable(err) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		page = append(page, r)
	}
	return page, "", nil
}

// Update reads the request id, changes it with fn and writes it, under
// the store's one mutex, so each change is atomic. A change of state goes
// through Request.To; a closed request changes only in its made list, its
// edges, its acts' outcomes, its stray run and an empty run or session
// id; the id, the schema and what was asked never change. The change is
// measured before it is written (ErrBound past a bound, nothing written).
// When it closes a turn, the turn's charge is settled. fn's error is
// Update's, with nothing written.
func (s *Store) Update(id string, fn func(*Request) error) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(id, fn)
}

func (s *Store) updateLocked(id string, fn func(*Request) error) (Request, error) {
	if err := s.writable(); err != nil {
		return Request{}, err
	}
	before, err := s.loadLocked(id)
	if err != nil {
		return Request{}, err
	}
	after, err := clone(before)
	if err != nil {
		return Request{}, err
	}
	if err := fn(&after); err != nil {
		return Request{}, err
	}
	if after.ID != before.ID || after.Schema != before.Schema || after.OpID != before.OpID || !sameAsk(after, before) || after.By != before.By || !after.CreatedAt.Equal(before.CreatedAt) {
		return Request{}, fmt.Errorf("brakeman: %s: what was asked never changes", id)
	}
	if after.State != before.State && !slices.Contains(transitions[before.State], after.State) {
		return Request{}, fmt.Errorf("%w: %s to %s", ErrTransition, before.State, after.State)
	}
	if !before.Open() {
		if err := closedChange(before, after); err != nil {
			return Request{}, err
		}
	} else if Closed(after.State) && after.ClosedAt.IsZero() {
		after.ClosedAt = s.now().UTC()
	}
	if err := s.saveRequestLocked(after); err != nil {
		return Request{}, err
	}
	if (before.State == StateStarting || before.State == StateRunning) && Closed(after.State) {
		if err := s.settleLocked(after); err != nil {
			return after, err
		}
	}
	return after, nil
}

// clone is a deep copy of r.
func clone(r Request) (Request, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return Request{}, err
	}
	var out Request
	return out, json.Unmarshal(b, &out)
}

// Start takes the queued request id as a turn starting at the
// configuration c, in two writes in this order: state.json with the
// request's charge in its start day's map (reserved: c's TurnMinutes),
// then the request starting, its deadline its start plus the reservation,
// with c's snapshot and the agent's stamp. The starting request is
// measured before either write, so a start past a bound (ErrBound)
// leaves no charge and the request queued. A charge already written for
// the request (a crash between the two) is the same entry, never a
// second; one written on an earlier day (a crash before midnight) is
// moved to the day the turn starts, whose turns it must fit. A request
// whose start would pass the day's turns is closed over_budget and
// ErrOverBudget answered.
func (s *Store) Start(id string, c Config, stamp Stamp) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return Request{}, err
	}
	r, err := s.loadLocked(id)
	if err != nil {
		return Request{}, err
	}
	if r.State != StateQueued {
		return r, fmt.Errorf("%w: %s is %s", ErrNotQueued, id, r.State)
	}
	now := s.now()
	st, err := s.loadState()
	if err != nil {
		return Request{}, err
	}
	day := s.day(now)
	chargedOn, charge, charged := chargeOf(st, id)
	if !charged {
		charge = Charge{Reserved: c.Budget.TurnMinutes}
	}
	starting := func(r *Request) error {
		if err := r.To(StateStarting, "", now); err != nil {
			return err
		}
		r.Deadline = now.UTC().Add(time.Duration(charge.Reserved) * time.Minute)
		r.Config, r.Agent = SnapshotOf(c), stamp
		return nil
	}
	// The request as it will be written, measured before the charge: a
	// start that could not be written charges nothing.
	next, err := clone(r)
	if err != nil {
		return Request{}, err
	}
	if err := starting(&next); err != nil {
		return Request{}, err
	}
	if err := measureRequest(next); err != nil {
		return Request{}, err
	}
	if chargedOn != day {
		// A new charge, or one an earlier day holds: today's turns decide.
		if len(st.Days[day]) >= c.Budget.TurnsPerDay {
			r, err := s.updateLocked(id, func(r *Request) error {
				return r.To(StateOverBudget, fmt.Sprintf("the day's %d turns were spent", c.Budget.TurnsPerDay), now)
			})
			if err != nil {
				return Request{}, err
			}
			return r, ErrOverBudget
		}
		if charged {
			delete(st.Days[chargedOn], id)
			if len(st.Days[chargedOn]) == 0 {
				delete(st.Days, chargedOn)
			}
		}
		if st.Days == nil {
			st.Days = map[string]map[string]Charge{}
		}
		if st.Days[day] == nil {
			st.Days[day] = map[string]Charge{}
		}
		st.Days[day][id] = charge
		if err := s.saveState(st); err != nil {
			return Request{}, err
		}
	}
	if f := afterCharge; f != nil {
		if err := f(); err != nil {
			return Request{}, err
		}
	}
	return s.updateLocked(id, starting)
}

// afterCharge, when a test sets it, runs between Start's two writes; an
// error it answers stops Start there, as a crash would.
var afterCharge func() error

// chargeOf finds the request's charge in any day state.json keeps.
func chargeOf(st state, id string) (day string, c Charge, ok bool) {
	for d, m := range st.Days {
		if c, ok := m[id]; ok {
			return d, c, true
		}
	}
	return "", Charge{}, false
}

// settleLocked writes the minutes a closed turn ran, rounded up and never
// more than its reservation, into its charge. The caller holds mu.
func (s *Store) settleLocked(r Request) error {
	st, err := s.loadState()
	if err != nil {
		return err
	}
	day, c, ok := chargeOf(st, r.ID)
	if !ok || c.Settled != nil {
		return nil
	}
	start := r.Deadline.Add(-time.Duration(c.Reserved) * time.Minute)
	m := max(0, min(c.Reserved, ceilMinutes(r.ClosedAt.Sub(start))))
	c.Settled = &m
	st.Days[day][r.ID] = c
	return s.saveState(st)
}

// TurnsToday counts the turns charged to the local day of now: once a
// turn's charge is written, whatever became of its request.
func (s *Store) TurnsToday(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.loadState()
	if err != nil {
		return 0, err
	}
	return len(st.Days[s.day(now)]), nil
}

// MinutesToday sums the minutes charged to the local day of now: each
// turn's settled minutes, or its reservation while it is unsettled.
func (s *Store) MinutesToday(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.loadState()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range st.Days[s.day(now)] {
		n += c.minutes()
	}
	return n, nil
}

// pinned reports whether a closed request must outlive Prune: it holds a
// pending made claim, an edge whose two ends are tickets and whose
// relation is not yet recorded, or a pending act.
func (r *Request) pinned() bool {
	if slices.ContainsFunc(r.Made, func(m Made) bool { return m.State == MadePending }) {
		return true
	}
	if slices.ContainsFunc(r.Acts, func(a Act) bool { return a.State == "pending" }) {
		return true
	}
	ticket := func(item int) bool {
		return slices.ContainsFunc(r.Made, func(m Made) bool { return m.Item == item && m.State == MadeDone && m.Key != "" })
	}
	return slices.ContainsFunc(r.Edges, func(e Edge) bool { return e.State == EdgePending && ticket(e.From) && ticket(e.To) })
}

// Prune keeps the newest Keep requests and every one that is open or
// pinned, and removes the rest: the request's document first, then its
// siblings. One of a later schema is kept. It answers how many it
// removed. It runs under Update's mutex, so a claim either lands on a
// request it keeps or finds the request gone (ErrNotFound).
func (s *Store) Prune() (int, error) { return s.PruneKeeping(keepNewest) }

// PruneKeeping is Prune keeping the newest keep requests (none below 0)
// besides the open and pinned ones: the desk's, which keeps Keep and
// whose tests lower it.
func (s *Store) PruneKeeping(keep int) (int, error) {
	keep = max(keep, 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return 0, err
	}
	ids, err := s.ids()
	if err != nil {
		return 0, err
	}
	entries, err := s.reqs.List()
	if err != nil {
		return 0, err
	}
	var gone []string
	for i, id := range ids {
		if i < keep {
			continue
		}
		r, err := s.loadLocked(id)
		if skippable(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if r.Open() || r.pinned() {
			continue
		}
		gone = append(gone, id)
	}
	// The op ids of the requests to go are marked pruned before any file
	// goes, so a repeat of one is never taken for a request whose file
	// never landed; a crash between leaves a marked request that is still
	// there, answered as it is, and removed by the next Prune.
	if len(gone) > 0 {
		st, err := s.loadState()
		if err != nil {
			return 0, err
		}
		marked := false
		for i := range st.Ops {
			if !st.Ops[i].Pruned && slices.Contains(gone, st.Ops[i].ID) {
				st.Ops[i].Pruned, marked = true, true
			}
		}
		if marked {
			if err := s.saveState(st); err != nil {
				return 0, err
			}
		}
	}
	removed := 0
	for _, id := range gone {
		if err := s.removeLocked(requestDoc(id)); err != nil {
			return removed, err
		}
		for _, e := range entries {
			if sid, _, ok := siblingName(e.Name); ok && sid == id {
				if err := s.removeLocked(e.Name); err != nil {
					return removed, err
				}
			}
		}
		removed++
	}
	return removed, nil
}

// PendingMake is a make the desk completes (PR-4): a made claim still
// pending, or an edge whose two ends are tickets and whose relation is
// not yet recorded.
type PendingMake struct {
	Request string
	Made    *Made // a claim pending
	Edge    *Edge // an edge to record
}

// Pending lists what Brakeman's records hold of other stores unfinished:
// the made claims and edges the desk's recovery completes over Dispatch
// and the crew store. Open completes none of them.
func (s *Store) Pending() ([]PendingMake, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, err := s.ids()
	if err != nil {
		return nil, err
	}
	var out []PendingMake
	for _, id := range slices.Backward(ids) {
		r, err := s.loadLocked(id)
		if skippable(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for i := range r.Made {
			if r.Made[i].State == MadePending {
				out = append(out, PendingMake{Request: id, Made: &r.Made[i]})
			}
		}
		ticket := func(item int) bool {
			return slices.ContainsFunc(r.Made, func(m Made) bool { return m.Item == item && m.State == MadeDone && m.Key != "" })
		}
		for i := range r.Edges {
			if e := r.Edges[i]; e.State == EdgePending && ticket(e.From) && ticket(e.To) {
				out = append(out, PendingMake{Request: id, Edge: &r.Edges[i]})
			}
		}
	}
	return out, nil
}

// Resolution is one ref of a kept brief: what the turn read of it (Seen;
// State "unobserved" for a ref it never read) and what it was as the
// brief was kept (Now).
type Resolution struct {
	Ref  Ref      `json:"ref"`
	Seen RefState `json:"seen"`
	Now  RefState `json:"now"`
}

// RefState is a ref's record at one time.
type RefState struct {
	URL    string    `json:"url,omitempty"`    // at most MaxURL bytes
	State  string    `json:"state,omitempty"`  // at most MaxRefState bytes
	Rev    string    `json:"rev,omitempty"`    // at most MaxRevision bytes
	SHA256 string    `json:"sha256,omitempty"` // 64 hex digits
	At     time.Time `json:"at,omitzero"`
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (r RefState) check(field string) error {
	switch {
	case len(r.URL) > MaxURL || strings.ContainsFunc(r.URL, isControl):
		return fmt.Errorf("%w: %s.url at most %d bytes", ErrBound, field, MaxURL)
	case len(r.State) > MaxRefState:
		return fmt.Errorf("%w: %s.state at most %d bytes", ErrBound, field, MaxRefState)
	case len(r.Rev) > MaxRevision:
		return fmt.Errorf("%w: %s.rev at most %d bytes", ErrBound, field, MaxRevision)
	case r.SHA256 != "" && !sha256Pattern.MatchString(r.SHA256):
		return fmt.Errorf("%w: %s.sha256 is 64 hex digits", ErrBound, field)
	}
	return nil
}

// Kept is a brief to keep: the op id it came with, the brief (checked by
// CheckBrief), its resolutions (one per distinct ref), the run and
// session the closing write knows, and the server's coverage of what the
// turn read (PR-4 gives it). The footprint is not the caller's: KeepBrief
// reads it off the turn's read log.
type Kept struct {
	OpID      string
	Brief     Brief
	Refs      []Resolution
	RunID     string
	SessionID string
	Coverage  *Coverage
}

// KeepBrief keeps a request's brief: the brief and its resolutions
// measured (ErrBound past MaxBrief or MaxRefsDoc, nothing written) and
// written, then the request briefed, naming them, with its coverage and
// its footprint, the classes its read log used read under the same mutex
// as the briefed write, so no read noted before the brief is left out of
// it. A request with a brief answers the same op id and brief with
// itself, and any other with ErrBriefExists, whatever its state; only a
// starting or running one takes a new brief (ErrNotTurn).
func (s *Store) KeepBrief(id string, k Kept) (Request, error) {
	if !opIDPattern.MatchString(k.OpID) {
		return Request{}, invalidBrief("opId", "must match %s", opIDPattern)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return Request{}, err
	}
	r, err := s.loadLocked(id)
	if err != nil {
		return Request{}, err
	}
	bb, err := encode(k.Brief)
	if err != nil {
		return Request{}, err
	}
	if r.HasBrief {
		kept, err := s.readDoc(briefDoc(id), MaxBrief)
		if err == nil && r.BriefOpID == k.OpID && bytes.Equal(bytes.TrimSpace(kept), bytes.TrimSpace(bb)) {
			return r, nil
		}
		return Request{}, fmt.Errorf("%w: %s", ErrBriefExists, id)
	}
	if r.State != StateStarting && r.State != StateRunning {
		return Request{}, fmt.Errorf("%w: %s is %s", ErrNotTurn, id, r.State)
	}
	if len(bb) > MaxBrief {
		return Request{}, fmt.Errorf("%w: the brief would be %d bytes as kept, at most %d", ErrBound, len(bb), MaxBrief)
	}
	seen := map[Ref]bool{}
	for i, res := range k.Refs {
		field := fmt.Sprintf("refs[%d]", i)
		if res.Ref.IsZero() || seen[res.Ref] {
			return Request{}, fmt.Errorf("%w: %s: one resolution per distinct ref", ErrBound, field)
		}
		seen[res.Ref] = true
		if err := res.Seen.check(field + ".seen"); err != nil {
			return Request{}, err
		}
		if err := res.Now.check(field + ".now"); err != nil {
			return Request{}, err
		}
	}
	if len(k.Refs) > MaxBriefRefs {
		return Request{}, fmt.Errorf("%w: %d refs, at most %d", ErrBound, len(k.Refs), MaxBriefRefs)
	}
	refs := k.Refs
	if refs == nil {
		refs = []Resolution{}
	}
	rb, err := encode(refs)
	if err != nil {
		return Request{}, err
	}
	if len(rb) > MaxRefsDoc {
		return Request{}, fmt.Errorf("%w: the brief's resolutions would be %d bytes, at most %d", ErrBound, len(rb), MaxRefsDoc)
	}
	var log ReadLog
	if _, err := s.reqs.LoadLimit(readsDoc(id), &log, MaxReadsDoc); err != nil {
		return Request{}, err
	}
	now := s.now()
	next, err := clone(r)
	if err != nil {
		return Request{}, err
	}
	if err := next.To(StateBriefed, "", now); err != nil {
		return Request{}, err
	}
	next.HasBrief, next.BriefOpID, next.BriefAt = true, k.OpID, now.UTC()
	next.Footprint, next.Coverage = footprintOf(log, r.Config.Visibility), k.Coverage
	if next.RunID == "" {
		next.RunID = k.RunID
	}
	if next.SessionID == "" {
		next.SessionID = k.SessionID
	}
	// The request is measured before its siblings are written, so a
	// request that could not be written leaves no sibling either.
	if err := measureRequest(next); err != nil {
		return Request{}, err
	}
	if err := s.write(s.reqs, briefDoc(id), k.Brief, MaxBrief); err != nil {
		return Request{}, err
	}
	if err := s.write(s.reqs, refsDoc(id), refs, MaxRefsDoc); err != nil {
		return Request{}, err
	}
	if err := s.saveRequestLocked(next); err != nil {
		return Request{}, err
	}
	if err := s.settleLocked(next); err != nil {
		return next, err
	}
	return next, nil
}

// footprintOf is the visibility classes a read log used, in Classes'
// order; a log that dropped entries used, for all anyone can tell, every
// class the snapshot v had on.
func footprintOf(log ReadLog, v Visibility) *Footprint {
	used := map[string]bool{}
	for _, rd := range log.Reads {
		used[rd.Class] = true
	}
	fp := &Footprint{Classes: []string{}}
	for _, c := range Classes {
		if used[c] || (log.Dropped > 0 && v.Has(c)) {
			fp.Classes = append(fp.Classes, c)
		}
	}
	return fp
}

// Brief answers a request's kept brief and its resolutions. A brief or
// resolutions the request names but that are gone (another server pruned
// the request after it was read) answer ErrNotFound, never an empty brief.
func (s *Store) Brief(id string) (Brief, []Resolution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.loadLocked(id)
	if err != nil {
		return Brief{}, nil, err
	}
	if !r.HasBrief {
		return Brief{}, nil, fmt.Errorf("%w: %s has no brief", ErrNotFound, id)
	}
	var b Brief
	ok, err := s.reqs.LoadLimit(briefDoc(id), &b, MaxBrief)
	if err != nil {
		return Brief{}, nil, err
	}
	if !ok {
		return Brief{}, nil, fmt.Errorf("%w: %s names %s, which is gone", ErrNotFound, id, briefDoc(id))
	}
	var refs []Resolution
	if ok, err = s.reqs.LoadLimit(refsDoc(id), &refs, MaxRefsDoc); err != nil {
		return Brief{}, nil, err
	}
	if !ok {
		return Brief{}, nil, fmt.Errorf("%w: %s names %s, which is gone", ErrNotFound, id, refsDoc(id))
	}
	return b, refs, nil
}

// Read is one answer a turn read: the ref it is cited by, its visibility
// class, its revision or state, and when.
type Read struct {
	Ref   Ref       `json:"ref"`
	Class string    `json:"class"`
	Rev   string    `json:"rev,omitempty"`
	State string    `json:"state,omitempty"`
	At    time.Time `json:"at"`
}

// ReadLog is a turn's read log: its newest reads, and how many older ones
// were dropped to keep it within MaxReadLog entries and MaxReadsDoc bytes.
type ReadLog struct {
	Reads   []Read `json:"reads"`
	Dropped int    `json:"dropped,omitempty"`
}

// NoteReads appends reads to the read log of the starting or running
// request id, the oldest dropped first (and counted) past MaxReadLog
// entries or MaxReadsDoc bytes, and adds them to the request's count.
func (s *Store) NoteReads(id string, reads ...Read) (ReadLog, error) {
	return s.noteReads(id, len(reads), reads)
}

// NoteAnswer is NoteReads for one answer a turn read: its items noted in
// the read log, and one read counted against the turn's ReadsPerTurn
// whatever the answer held (none, or a page of 200).
func (s *Store) NoteAnswer(id string, reads ...Read) (ReadLog, error) {
	return s.noteReads(id, 1, reads)
}

func (s *Store) noteReads(id string, count int, reads []Read) (ReadLog, error) {
	for i, rd := range reads {
		if rd.Ref.IsZero() || !slices.Contains(Classes, rd.Class) || len(rd.Rev) > MaxRevision || len(rd.State) > MaxRefState {
			return ReadLog{}, fmt.Errorf("%w: reads[%d]: a ref, a class and a revision or state within their bounds", ErrBound, i)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return ReadLog{}, err
	}
	r, err := s.loadLocked(id)
	if err != nil {
		return ReadLog{}, err
	}
	if r.State != StateStarting && r.State != StateRunning {
		return ReadLog{}, fmt.Errorf("%w: %s is %s", ErrNotTurn, id, r.State)
	}
	var log ReadLog
	if _, err := s.reqs.LoadLimit(readsDoc(id), &log, MaxReadsDoc); err != nil {
		return ReadLog{}, err
	}
	for i := range reads {
		reads[i].At = reads[i].At.UTC()
	}
	log.Reads = append(log.Reads, reads...)
	if n := len(log.Reads) - MaxReadLog; n > 0 {
		log.Reads, log.Dropped = log.Reads[n:], log.Dropped+n
	}
	for {
		b, err := encode(log)
		if err != nil {
			return ReadLog{}, err
		}
		if len(b) <= MaxReadsDoc {
			break
		}
		// Drop a tenth of what is over, at least one, and measure again.
		over := len(b) - MaxReadsDoc
		n := max(1, min(len(log.Reads), over/entrySize(log.Reads[0])+1))
		log.Reads, log.Dropped = log.Reads[n:], log.Dropped+n
	}
	if len(reads) > 0 {
		if err := s.write(s.reqs, readsDoc(id), log, MaxReadsDoc); err != nil {
			return ReadLog{}, err
		}
	}
	if _, err := s.updateLocked(id, func(r *Request) error {
		r.Reads += count
		return nil
	}); err != nil {
		return ReadLog{}, err
	}
	return log, nil
}

// Reads answers a request's read log.
func (s *Store) Reads(id string) (ReadLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.loadLocked(id); err != nil {
		return ReadLog{}, err
	}
	var log ReadLog
	_, err := s.reqs.LoadLimit(readsDoc(id), &log, MaxReadsDoc)
	return log, err
}

// PutMakeInput writes a make's input, as the person edited it, beside the
// request id, before the claim that names it (Update): refused past
// MaxMakeDoc with nothing written. A claim standing over another input is
// the desk's to refuse first.
func (s *Store) PutMakeInput(id string, item int, input json.RawMessage) error {
	if item < 0 || item >= MaxItems {
		return fmt.Errorf("%w: item %d", ErrBound, item)
	}
	if !json.Valid(input) {
		return errors.New("brakeman: a make's input is JSON")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return err
	}
	if _, err := s.loadLocked(id); err != nil {
		return err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, input); err != nil {
		return err
	}
	return s.write(s.reqs, makeDoc(id, item), json.RawMessage(compact.Bytes()), MaxMakeDoc)
}

// MakeInput answers a make's input.
func (s *Store) MakeInput(id string, item int) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readDoc(makeDoc(id, item), MaxMakeDoc)
}
