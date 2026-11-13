package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Capacity (docs/plans/capacity.md, Program item AC1, PR-3): the account
// references and the readings of internal/capacity behind the admin's
// routes, the capacity event, and the readers of internal/agents wired to
// the record of versions. Every route is the admin's and a launch route's
// (a switchyard answers 403 switchyard). What an account's view holds is
// the admin's alone: no reply but these routes', no event but capacity's
// and no log line carries an account's label, folder or figures, and a
// reader's words are Conductor's own (internal/agents). A Brakeman turn
// reads GET /api/capacity under its Capacity class (PR-6), each view as
// brakeman.CapacityOf makes it: labels and figures, never a folder.

// The routes' bounds, and the owner lock's wait.
const (
	maxAccountsReply = capacity.MaxAccountsDoc + 4<<10 // GET /api/accounts: the document's 64 KiB, then the readings and adapters beside it
	maxCapacityView  = 32 << 10                        // one view
	maxCapacityPage  = 128 << 10                       // a page of the listing
	maxCapacityLimit = 16                              // views a page holds at most
	maxCapacityEvent = 512                             // a capacity event's data
	maxCapacityBody  = 1 << 10                         // POST /api/capacity/read's body
	maxStartsPerKey  = 512                             // the start times a key's counts keep
	maxCountKeys     = 256                             // keys the counts keep
	// An account write's body is bounded by its largest valid spelling:
	// each text field at its limit with every character escaped as JSON
	// may escape it (an ASCII byte as \u00XX, 6 bytes; a character past the
	// BMP, 4 bytes of UTF-8, as a surrogate pair, 12), plus 256 bytes for
	// the names, the punctuation and expectedRev. A rename's, a retire's
	// and a default's hold no folder.
	maxEscapedLabel    = 12 * capacity.MaxLabel
	maxAccountBody     = 256 + 6*32 + maxEscapedLabel + 6*capacity.MaxFolder // an add: adapter, label, folder (25,744 bytes)
	maxAccountEditBody = 256 + maxEscapedLabel                               // every other write (976 bytes)
)

var (
	capacityLockWait = 10 * time.Second
	capacityLockTick = 250 * time.Millisecond
	// capacityClock, when a test sets it, is the readings' clock of a
	// server opened after (capacity.CacheOptions.Clock).
	capacityClock atomic.Pointer[capacity.Clock]
)

// ownerOnlyReaders are the adapters whose reads are admitted from the
// record of versions, so only the server that owns capacity/ starts their
// program at all (One owner): Claude Code's.
var ownerOnlyReaders = map[string]bool{"claude": true}

// authEnv is, per adapter, the variables that set where its login comes
// from besides its ConfigDirEnv: a launch whose own additions set one is
// env_set, its usage not read.
var authEnv = map[string][]string{
	"claude": {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"},
	"codex":  {"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"},
}

// modelPattern is a model's name, as Brakeman's configuration holds one.
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,99}$`)

// capacityDesk is the server's capacity: the accounts and versions
// (capacity.Store), the readings (capacity.Cache), the owner lock, and
// this server's counts of the sessions it started.
type capacityDesk struct {
	s      *Server
	st     *capacity.Store
	cache  *capacity.Cache
	runDir string
	since  time.Time

	mu       sync.Mutex
	owned    bool
	why      string
	lock     *store.OwnerLock
	closed   bool
	done     chan struct{}
	waiting  sync.WaitGroup
	starts   map[capacity.Key][]time.Time
	lastRead map[string]accountReading // account id, or "<adapter>:default"

	// queuing is the one slot a request's view is made in (queuedView).
	queuing chan struct{}
	// reads counts the accounts reads made for views and snapshots
	// (accounts).
	reads atomic.Int64

	// The tests' seams: seam runs inside an add's write, under the store's
	// lock (Facts.CheckAccountDir); closing as close takes the store's gate;
	// waitOver, when set, makes the context of a request's wait (wait),
	// which then ends when the test says, never by the clock.
	seam, closing func()
	waitOver      atomic.Pointer[func() (context.Context, context.CancelFunc)]
}

// queuedCapacityWait is the one wait a request's view is made within as
// it is queued, every step of it (docs/protocol.md, Brakeman).
const queuedCapacityWait = 500 * time.Millisecond

// accounts reads the accounts list for a view or a snapshot, counting the
// read (reads, which the tests count).
func (d *capacityDesk) accounts() (capacity.Accounts, error) {
	d.reads.Add(1)
	return d.st.Accounts()
}

// wait is the context of a request's wait: it ends after
// queuedCapacityWait, or as a test's waitOver makes it.
func (d *capacityDesk) wait() (context.Context, context.CancelFunc) {
	if f := d.waitOver.Load(); f != nil {
		return (*f)()
	}
	return context.WithTimeout(context.Background(), queuedCapacityWait)
}

// accountReading is an account's last reading as GET /api/accounts shows it.
type accountReading struct {
	Agent  string    `json:"agent"`
	State  string    `json:"state"`
	ReadAt time.Time `json:"readAt"`
}

// openCapacity opens capacity/ and takes its owner lock, waiting for it
// as Brakeman's records wait; nil without a data directory or on a
// switchyard. A document that cannot be read leaves the routes answering
// that, in words, to the admin alone: the log says only that it happened.
func (s *Server) openCapacity(st *store.Store) *capacityDesk {
	if st == nil || s.cfg.Switchyard.Enabled {
		return nil
	}
	cs, err := capacity.Open(st, capacity.Options{})
	if err != nil {
		s.log.Warn("capacity/ could not be opened: the accounts and capacity routes answer why")
		return &capacityDesk{s: s, why: err.Error(), closed: true}
	}
	d := &capacityDesk{s: s, st: cs, since: time.Now(), done: make(chan struct{}), starts: map[capacity.Key][]time.Time{}, lastRead: map[string]accountReading{}, queuing: make(chan struct{}, 1)}
	if sub, err := st.Sub(capacity.Dir); err == nil {
		if run, err := sub.Sub("run"); err == nil {
			d.runDir = run.Dir()
		}
	}
	var clock capacity.Clock
	if c := capacityClock.Load(); c != nil {
		clock = *c
	}
	d.cache = capacity.NewCache(capacity.CacheOptions{Clock: clock, Read: d.read, OnRead: d.onRead})
	path := filepath.Join(st.Dir(), capacity.Dir, "owner.lock")
	lock, why, held, err := store.TakeOwnerLock(path)
	switch {
	case err != nil:
		d.why = "the owner lock: " + err.Error()
	case lock != nil:
		d.take(lock)
	default:
		d.why = why
		if held {
			d.waiting.Add(1)
			go d.awaitLock(path)
		}
	}
	return d
}

// take makes d the owner once it holds lock (Store.Own); an Own that fails
// leaves it not owning, the lock let go.
func (d *capacityDesk) take(lock *store.OwnerLock) {
	if err := d.st.Own(); err != nil {
		lock.Release()
		d.why = "capacity/ could not be owned: " + err.Error()
		return
	}
	d.lock, d.owned, d.why = lock, true, ""
}

// awaitLock tries for the owner lock every capacityLockTick for up to
// capacityLockWait, each try under mu while d is open.
func (d *capacityDesk) awaitLock(path string) {
	defer d.waiting.Done()
	deadline := time.Now().Add(capacityLockWait)
	tick := time.NewTicker(capacityLockTick)
	defer tick.Stop()
	for {
		select {
		case <-d.done:
			return
		case <-tick.C:
		}
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return
		}
		lock, why, held, err := store.TakeOwnerLock(path)
		switch {
		case err != nil:
			d.why = "the owner lock: " + err.Error()
		case lock != nil:
			d.take(lock)
		case held && time.Now().Before(deadline):
			d.mu.Unlock()
			continue
		default:
			d.why = why
		}
		d.mu.Unlock()
		return
	}
}

// ownership reports whether d owns capacity/, and why not.
func (d *capacityDesk) ownership() (bool, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.owned, d.why
}

// close ends the reads (Cache.Close: none starts after, none under way is
// left) and the wait for the lock, then gives the store's ownership up
// (Store.Disown, which waits for a write under way: every later one is
// refused accounts_not_owner, as on a server that never owned), and only
// then lets the lock go. A second call does nothing.
func (d *capacityDesk) close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	close(d.done)
	d.mu.Unlock()
	d.cache.Close()
	d.waiting.Wait()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lock != nil {
		if d.closing != nil {
			d.closing()
		}
		d.st.Disown()
		d.lock.Release()
		d.lock = nil
	}
	d.owned = false
}

// started counts a server session of agent started now, bound to account
// (its binding's id: "default" for the agent's own login).
func (d *capacityDesk) started(agent, account string) {
	if d == nil || d.starts == nil {
		return
	}
	k := capacity.Key{Agent: agent, Account: account}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.starts[k]; !ok && len(d.starts) >= maxCountKeys {
		var oldest capacity.Key
		var at time.Time
		for key, ts := range d.starts {
			if last := ts[len(ts)-1]; at.IsZero() || last.Before(at) {
				oldest, at = key, last
			}
		}
		delete(d.starts, oldest)
	}
	ts := append(d.starts[k], time.Now())
	if n := len(ts) - maxStartsPerKey; n > 0 {
		ts = slices.Delete(ts, 0, n)
	}
	d.starts[k] = ts
}

// counts is this server's estimate for k: the server sessions running of
// its agent counted on its account (Launched.Counted) and those started in
// the last 5 hours and 7 days, since this server started.
func (d *capacityDesk) counts(k capacity.Key) capacity.Counts {
	since := d.since
	c := capacity.Counts{Since: &since}
	for _, info := range d.s.registry.List() {
		if info.Kind != session.KindServer || info.AgentID != k.Agent || info.Status.Ended() {
			continue
		}
		if l, ok := d.s.lookupLocal(info.ID); ok && l.Launched().Counted == k.Account {
			c.Running++
		}
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, t := range d.starts[k] {
		if now.Sub(t) < 5*time.Hour {
			c.Started5h++
		}
		if now.Sub(t) < 7*24*time.Hour {
			c.Started7d++
		}
	}
	return c
}

// onRead is told each reading kept: its account's last reading is noted
// for GET /api/accounts, then the capacity event goes out. The note is
// made under mu, which a remove takes to delete its account's, and only
// for an account the document holds at that moment, or an adapter's own
// login as the read filed it when it started (read: an entry that pinned
// no other, its adapter then), never from the catalog as it is now; every
// note naming neither is dropped then. So the map holds at most what can
// exist, MaxAccounts and an entry per adapter with a ConfigDirEnv, and a
// reading of a live account always has its room.
func (d *capacityDesk) onRead(k capacity.Key, v capacity.View) {
	if v.ReadAt == nil {
		return
	}
	d.mu.Lock()
	if doc, err := d.st.Accounts(); err == nil {
		live := map[string]bool{}
		for _, acct := range doc.Accounts {
			live[acct.ID] = true
		}
		for key := range d.lastRead {
			if !live[key] && !strings.HasSuffix(key, ":"+capacity.DefaultID) {
				delete(d.lastRead, key)
			}
		}
		key := k.Account
		if key == capacity.DefaultID {
			key = v.Filed
		}
		if live[k.Account] || (k.Account == capacity.DefaultID && v.Filed != "") {
			d.lastRead[key] = accountReading{Agent: k.Agent, State: v.State, ReadAt: *v.ReadAt}
		}
	}
	d.mu.Unlock()
	d.s.events.capacity(k, v.State, *v.ReadAt)
}

// capacityEvent is the data of a capacity event: the key whose reading
// landed, its state and when it was read, for a page to read the view
// again. It names no label, folder or figure.
type capacityEvent struct {
	Agent   string    `json:"agent"`
	Account string    `json:"account"`
	State   string    `json:"state"`
	ReadAt  time.Time `json:"readAt"`
}

// capacity sends a capacity event, at most maxCapacityEvent bytes (an
// agent id is at most 32 bytes, an account id 14, a state 13: never
// near it); like a run event it is state a page reads again, so a client
// that cannot keep up is dropped (broadcast). Past the bound it is
// dropped, never cut.
func (h *eventHub) capacity(k capacity.Key, state string, at time.Time) {
	b, err := json.Marshal(capacityEvent{Agent: k.Agent, Account: k.Account, State: state, ReadAt: at})
	if err != nil || len(b) > maxCapacityEvent {
		return
	}
	h.broadcast([]byte("event: capacity\ndata: " + string(b) + "\n\n"))
}

// agentEnv is the part of an agent's environment its entry and the server
// give every launch of it: the passthrough names (the server's, then the
// agent's) and the variables (the adapter's, then the agent's own). A
// launch adds its own on top (sessions.go); a usage read adds none
// (readEnv), so the two cannot drift.
func (s *Server) agentEnv(agent catalog.Agent, adapterEnv map[string]string) ([]string, map[string]string) {
	passthrough := append(slices.Clone(s.cfg.EnvPassthrough), agent.EnvPassthrough...)
	env := maps.Clone(adapterEnv)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, agent.Env)
	return passthrough, env
}

// readEnv is the environment a read of agent on the account with folder
// ("" for its own login) runs with: a session's (agentEnv over the
// server's, as pty.BuildEnv builds it), then the folder as the adapter's
// ConfigDirEnv; and the hex SHA-256 of the adapter and it, which a reading
// keeps, so a reading by another adapter is not this entry's (MarkEnv).
func (s *Server) readEnv(agent catalog.Agent, folder string) ([]string, string) {
	_, adapterEnv := agents.InjectFor(agent.Adapter, agents.HooksDir(s.cfg.DataDir), agent.EffectiveSignal(), false)
	passthrough, env := s.agentEnv(agent, adapterEnv)
	if ad, ok := agents.Get(agent.Adapter); ok && folder != "" && ad.ConfigDirEnv != "" {
		env[ad.ConfigDirEnv] = folder
	}
	built := pty.BuildEnv(pty.ParentEnv(), passthrough, env, nil)
	sum := sha256.Sum256([]byte(agent.Adapter + "\x00" + strings.Join(built, "\x00")))
	return built, hex.EncodeToString(sum[:])
}

// pinned is the variable by which agent's entry, or the server's
// passthrough, pins its login: its adapter's ConfigDirEnv or one of its
// login variables (authEnv). Such an agent has its default account only,
// its own login, in every form of the question (a view, the listing, a
// read), and its readings are not its adapter's own login's. "" for none.
func (s *Server) pinned(agent catalog.Agent) string {
	built, _ := s.readEnv(agent, "")
	return pinnedIn(agent.Adapter, built)
}

// pinnedIn is the variable of built, an environment as readEnv builds it,
// that pins adapter's login: its ConfigDirEnv or a login variable; "" for
// none.
func pinnedIn(adapter string, built []string) string {
	ad, _ := agents.Get(adapter)
	lookup := pty.Lookup(built)
	for _, k := range append([]string{ad.ConfigDirEnv}, authEnv[adapter]...) {
		if k != "" && lookup(k) != "" {
			return k
		}
	}
	return ""
}

// yoloSetsLogin says agent's yolo recipe sets its adapter's ConfigDirEnv
// or one of its login variables.
func yoloSetsLogin(agent catalog.Agent) bool {
	if agent.Yolo.Empty() {
		return false
	}
	ad, _ := agents.Get(agent.Adapter)
	for k := range agent.Yolo.Env {
		if (ad.ConfigDirEnv != "" && k == ad.ConfigDirEnv) || slices.Contains(authEnv[agent.Adapter], k) {
			return true
		}
	}
	return false
}

// usageReader is the reader of adapter: the registry's, or the test's
// (usageReaders).
func (s *Server) usageReader(adapter string) *agents.UsageReader {
	if s.usageReaders != nil {
		return s.usageReaders[adapter]
	}
	ad, ok := agents.Get(adapter)
	if !ok {
		return nil
	}
	return ad.Usage
}

// versionBefore compares two dotted versions number by number, as the
// readers do; a part that is not a number counts as 0.
func versionBefore(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// admission is a capacity.Admission as a reader takes one: its Done's
// refusal (an outcome not saved) is kept for the read's answer.
type admission struct {
	a   *capacity.Admission
	mu  sync.Mutex
	err error
}

func (a *admission) Done(o agents.Outcome) {
	out := capacity.Inconclusive
	switch o {
	case agents.Local:
		out = capacity.Local
	case agents.Turned:
		out = capacity.Turned
	}
	err := a.a.Done(out)
	a.mu.Lock()
	a.err = err
	a.mu.Unlock()
}

// read is the cache's reader: one read of k through the agent's own tool,
// admitted from the record of versions. Its words are Conductor's own,
// never the tool's.
func (d *capacityDesk) read(ctx context.Context, k capacity.Key, in capacity.Intent) capacity.Reading {
	failed := func(words string) capacity.Reading {
		return capacity.Reading{State: capacity.StateError, Words: words}
	}
	agent, ok := d.s.Catalog().Get(k.Agent)
	if !ok {
		return failed("no agent " + k.Agent + " in the catalog")
	}
	rd := d.s.usageReader(agent.Adapter)
	if rd == nil || rd.Read == nil {
		return failed(capacity.StateWords(capacity.StateUnsupported))
	}
	if len(agent.Command) == 0 || relativePath(agent.Command[0]) {
		return failed(relativeWords)
	}
	if owned, why := d.ownership(); ownerOnlyReaders[agent.Adapter] && !owned {
		return failed(notOwnerWords(agent, why))
	}
	folder := ""
	if k.Account != capacity.DefaultID {
		doc, err := d.st.Accounts()
		if err != nil {
			return failed("the accounts could not be read")
		}
		i := slices.IndexFunc(doc.Accounts, func(a capacity.Account) bool { return a.ID == k.Account })
		if i < 0 || doc.Accounts[i].Adapter != agent.Adapter {
			return failed("the account is not in Accounts any more")
		}
		folder = doc.Accounts[i].Folder
	}
	env, hash := d.s.readEnv(agent, folder)
	// Filed now, from the environment this read runs with: its adapter's
	// own login when it reads the default through an entry pinning none.
	filed := ""
	if ad, _ := agents.Get(agent.Adapter); k.Account == capacity.DefaultID && ad.ConfigDirEnv != "" && pinnedIn(agent.Adapter, env) == "" {
		filed = agent.Adapter + ":" + capacity.DefaultID
	}
	var adm *admission
	var refused error
	input := agents.UsageInput{Command: agent.Command, Env: env, Dir: d.runDir, Admit: func(version string) (agents.Admission, error) {
		qualified := !versionBefore(version, rd.MinVersion) && !versionBefore(rd.Qualified, version)
		a, err := d.st.Versions().Admit(agent.Adapter, version, qualified, in.Check())
		if err != nil {
			refused = err
			var ce *capacity.Error
			if errors.As(err, &ce) {
				return nil, errors.New(ce.Words)
			}
			return nil, errors.New("the check could not be recorded, so nothing was started")
		}
		adm = &admission{a: a}
		return adm, nil
	}}
	rep := rd.Read(ctx, input)
	r := capacity.Reading{State: rep.State, Words: rep.Words, Plan: rep.Plan, Program: agentProgram(agent), Version: rep.Version, Qualified: rd.Qualified, Env: hash}
	var ce *capacity.Error
	if errors.As(refused, &ce) && (ce.Code == capacity.CodeNotKept || ce.Code == capacity.CodeNotOwner) {
		r = capacity.Reading{State: capacity.StateError, Words: ce.Words, Env: hash}
	}
	if adm != nil {
		adm.mu.Lock()
		err := adm.err
		adm.mu.Unlock()
		if errors.As(err, &ce) {
			r = capacity.Reading{State: capacity.StateError, Words: ce.Words, Env: hash}
		} else if err != nil {
			r = capacity.Reading{State: capacity.StateError, Words: "the outcome of the run could not be recorded", Env: hash}
		}
	}
	r.Filed = filed
	if r.State != capacity.StateOK {
		return r
	}
	for _, w := range rep.Windows {
		r.Windows = append(r.Windows, capacity.Window{Kind: w.Kind, Name: w.Name, Minutes: w.Minutes, Percent: w.Percent, ResetsAt: w.ResetsAt, Severity: w.Severity, Active: w.Active, Limit: w.Limit})
	}
	for _, c := range rep.Credits {
		r.Credits = append(r.Credits, capacity.Credit{Kind: c.Kind, Enabled: c.Enabled, Used: c.Used, Limit: c.Limit, Balance: c.Balance, Unlimited: c.Unlimited, Currency: c.Currency})
	}
	r.Limits = capacity.Limits{Reached: rep.Limits.Reached, SpendControl: rep.Limits.SpendControl, OrdinaryAllowed: rep.Limits.OrdinaryAllowed}
	return r
}

// relativeWords are a not_installed view's words for an entry whose command
// is a path relative to a working folder.
const relativeWords = "this agent's command is a path relative to the folder a session runs in: the server reads only a program it finds itself"

// capacityReadable says the server resolves agent's command as the
// identity probe does: a program it finds installed, never a command given
// as a relative path, which is not looked up (relativePath). A view of any
// other is not_installed, the listing leaves it out, and no read runs it.
// held (a turn's read) takes what the look-ups hold (lookupCache.held).
func (s *Server) capacityReadable(ctx context.Context, agent catalog.Agent, held bool) bool {
	switch {
	case len(agent.Command) == 0 || relativePath(agent.Command[0]):
		return false
	case held:
		return s.lookups.held(agent.Command[0]).installed
	}
	return s.lookups.installed(ctx, agent.Command[0])
}

// agentProgram is the name of agent's program for people: its adapter's.
func agentProgram(agent catalog.Agent) string {
	if ad, ok := agents.Get(agent.Adapter); ok {
		return ad.Name
	}
	return agent.Name
}

// notOwnerWords is the view's words for an owner-only reader on a server
// that does not own capacity/.
func notOwnerWords(agent catalog.Agent, why string) string {
	if why == "" {
		why = "this server does not hold capacity/owner.lock"
	}
	return ownerWhy("another server over this data directory reads " + agentProgram(agent) + "'s usage: " + why)
}

// capacityQuery is what a view is asked for.
type capacityQuery struct {
	account, model, launch string
	yolo                   bool
	// held is a turn's read: the program's look-ups are asked only what
	// they hold (lookupCache.held), none started and none waited for.
	held bool
}

// The launches a view may name: a session (and a legacy crew's member,
// which runs as one), a managed run's attempt, a session on another
// computer.
var viewLaunches = []string{"session", "legacy", "managed", "remote"}

// viewOf answers the view of agent on q's account for q's launch, reading
// nothing; read, when set, is asked for the reading instead of the cache's
// View (POST /api/capacity/read, a turn's read: Cache.Latest). A state no
// read gives (unsupported, not installed, remote, env_set, not_owner) is
// answered without asking either.
func (d *capacityDesk) viewOf(ctx context.Context, agent catalog.Agent, q capacityQuery, read func(capacity.Key) capacity.View) (capacity.View, *apiError) {
	ad, hasAd := agents.Get(agent.Adapter)
	var doc capacity.Accounts
	if d.st != nil {
		var err error
		if doc, err = d.accounts(); err != nil {
			return capacity.View{}, newAPIError(http.StatusServiceUnavailable, "capacity_unavailable", "the accounts could not be read")
		}
	}
	if q.account != "" && !capacity.ValidID(q.account) {
		return capacity.View{}, newAPIError(http.StatusBadRequest, "invalid_account", "an account is default or a- and 12 of a-z 2-7")
	}
	// The account none is named for: an entry that pins its login has
	// that login alone, any other its adapter's default.
	pinned := d.s.pinned(agent)
	id, def := q.account, capacity.DefaultOf(doc, agent.Adapter)
	if pinned != "" {
		def = capacity.DefaultID
	}
	if id == "" {
		id = def
	}
	account := capacity.ViewAccount{ID: id, Default: id == def}
	if id != capacity.DefaultID {
		i := slices.IndexFunc(doc.Accounts, func(a capacity.Account) bool { return a.ID == id && a.Adapter == agent.Adapter })
		if i < 0 {
			return capacity.View{}, newAPIError(http.StatusBadRequest, "invalid_account", "no account "+id+" of "+agent.Adapter)
		}
		account.Label, account.Retired = doc.Accounts[i].Label, doc.Accounts[i].Retired
	}
	if q.model != "" && (!modelPattern.MatchString(q.model) || !hasAd || ad.ModelArgs == nil) {
		return capacity.View{}, newAPIError(http.StatusBadRequest, "invalid_request", "a model is 1 to 100 of A-Z a-z 0-9 . _ : / @ -, for an agent that takes one")
	}
	model := capacity.ViewModel{Chosen: q.model, Words: q.model}
	if q.model == "" {
		model.Words = "the agent's default"
	}
	setsLogin := q.yolo && yoloSetsLogin(agent)
	host := capacity.ViewHost{Kind: capacity.HostNative}
	host.Name, _ = os.Hostname()
	switch {
	case q.launch == "remote":
		host = capacity.ViewHost{Kind: capacity.HostRemote, Words: capacity.StateWords(capacity.StateRemote)}
	case q.launch == "managed" && id == capacity.DefaultID && pinned == "" && !setsLogin && d.s.offered.Where() != ext.WhereNative:
		host.Kind, host.Words = capacity.HostUndetermined, "this server's login: if "+d.s.offered.Name()+" takes the run, its usage there is unknown"
	}
	k := capacity.Key{Agent: agent.ID, Account: id}
	state := ""
	owned, why := d.ownership()
	rd := d.s.usageReader(agent.Adapter)
	switch {
	case host.Kind == capacity.HostRemote:
		state = capacity.StateRemote
	case rd == nil || rd.Read == nil:
		state = capacity.StateUnsupported
	case !d.s.capacityReadable(ctx, agent, q.held):
		state = capacity.StateNotInstalled
	case setsLogin || (pinned != "" && id != capacity.DefaultID):
		state = capacity.StateEnvSet
	case ownerOnlyReaders[agent.Adapter] && !owned:
		state = capacity.StateNotOwner
	}
	var v capacity.View
	switch {
	case state != "":
		v = capacity.View{State: state, Words: capacity.StateWords(state), Windows: []capacity.Window{}, Credits: []capacity.Credit{}, Fresh: capacity.FreshNone}
		if state == capacity.StateNotOwner {
			v.Words = notOwnerWords(agent, why)
		}
		if state == capacity.StateNotInstalled && len(agent.Command) > 0 && relativePath(agent.Command[0]) {
			v.Words = relativeWords
		}
		if state == capacity.StateEnvSet && pinned != "" && !setsLogin {
			v.Words = "this agent's entry sets " + pinned + ": it uses that login, its default account only"
		}
	case read != nil:
		v = read(k)
	default:
		v = d.cache.View(k)
	}
	if state == "" && v.State != capacity.StateReading {
		folder := ""
		if id != capacity.DefaultID {
			folder = doc.Accounts[slices.IndexFunc(doc.Accounts, func(a capacity.Account) bool { return a.ID == id })].Folder
		}
		_, hash := d.s.readEnv(agent, folder)
		v.MarkEnv(hash)
	}
	plan := v.Account.Plan
	v.Agent = capacity.ViewAgent{ID: agent.ID, Name: agent.Name, Adapter: agent.Adapter, Version: v.Agent.Version}
	if v.Agent.Version == "" {
		if idn, ok := d.s.cachedIdentityOf(agent, q.held); ok && idn.Identified {
			v.Agent.Version = idn.Version
		}
	}
	account.Plan = plan
	v.Account, v.Model, v.Host, v.Counts = account, model, host, d.counts(k)
	v.EntryLogin = pinned // for every account asked: no choice of one moves this agent's login
	return v, nil
}

// capacityAvailable answers the routes' refusal when the server keeps no
// capacity/ (no data directory) or could not open it.
func (s *Server) capacityAvailable(w http.ResponseWriter) bool {
	switch d := s.capacity; {
	case d == nil:
		refuse(w, http.StatusServiceUnavailable, "capacity_unavailable", "this server has no data directory, so it keeps no accounts")
	case d.st == nil:
		refuse(w, http.StatusServiceUnavailable, "capacity_unavailable", d.why)
	default:
		return true
	}
	return false
}

// writeBodyError answers a body that did not decode: 413 past its bound,
// else 400 with the decoder's words (a field it does not know, a type).
func writeBodyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBodyTooLarge) {
		refuse(w, http.StatusRequestEntityTooLarge, "bound", err.Error())
		return
	}
	refuse(w, http.StatusBadRequest, "invalid_request", err.Error())
}

// writeCapacityJSON answers v, encoded as the store encodes the document
// (no HTML escaping, so a label's '<' is one byte in both), refused when
// it passes bound.
func writeCapacityJSON(w http.ResponseWriter, status int, v any, bound int) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil || buf.Len() > bound {
		writeError(w, http.StatusInternalServerError, "bound", "the reply would pass its bound")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// handleCapacity answers GET /api/capacity: with agent, one view; without,
// a page of every installed agent's views, one for each of its accounts.
// Neither reads anything. A Brakeman turn's read (its Capacity class,
// brakemanReads) is answered each view as brakeman.CapacityOf makes it,
// from what the look-ups of programs hold (capacityQuery.held), and a
// server with no capacity in fixed words.
func (s *Server) handleCapacity(w http.ResponseWriter, r *http.Request) {
	notes := turnNotesOf(r)
	turn := notes != nil
	if turn && (s.capacity == nil || s.capacity.st == nil) {
		refuse(w, http.StatusServiceUnavailable, "capacity_unavailable", "this server keeps no capacity it can read")
		return
	}
	if !s.capacityAvailable(w) {
		return
	}
	q, ok := capacityQueryOf(w, r)
	if !ok {
		return
	}
	vq := capacityQuery{account: q.Get("account"), model: q.Get("model"), launch: q.Get("launch"), yolo: q.Get("yolo") == "1" || q.Get("yolo") == "true", held: turn}
	if vq.launch == "" {
		vq.launch = "session"
	}
	if !slices.Contains(viewLaunches, vq.launch) {
		refuse(w, http.StatusBadRequest, "invalid_request", "launch is one of "+strings.Join(viewLaunches, ", "))
		return
	}
	if id := q.Get("agent"); id != "" {
		agent, ok := s.capacityAgent(w, id)
		if !ok {
			return
		}
		v, aerr := s.capacity.viewOf(r.Context(), agent, vq, s.capacityLook(notes))
		if aerr != nil {
			refuse(w, aerr.status, aerr.Code, aerr.Message)
			return
		}
		writeCapacityJSON(w, http.StatusOK, capacityReply(turn, v), maxCapacityView)
		return
	}
	limit := maxCapacityLimit
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > maxCapacityLimit {
			refuse(w, http.StatusBadRequest, "invalid_request", "limit is 1 to "+strconv.Itoa(maxCapacityLimit))
			return
		}
		limit = n
	}
	if vq.account != "" || vq.model != "" {
		refuse(w, http.StatusBadRequest, "invalid_request", "account and model name one agent's view: give agent too")
		return
	}
	after := q.Get("after")
	if agent, account, ok := strings.Cut(after, "/"); after != "" && (!ok || !catalog.ValidID(agent) || !capacity.ValidID(account)) {
		refuse(w, http.StatusBadRequest, "invalid_request", "after is the next a page gave: an agent's id, /, an account's")
		return
	}
	s.capacityPage(w, r, vq, after, limit, notes)
}

// capacityReply is v as the caller reads it: a Brakeman turn (turn) as
// brakeman.CapacityOf makes it, its counts counted now; the admin whole.
func capacityReply(turn bool, v capacity.View) any {
	if turn {
		return brakeman.CapacityOf(v, time.Now())
	}
	return v
}

// capacityLook is how the route looks a view's reading up for its caller:
// for a Brakeman turn (notes) with Cache.Latest, which is no ask of the key
// and moves no key's place, noting the state of the reading the view shows
// as that same look answers it; for the admin as the cache's View, an ask.
func (s *Server) capacityLook(notes turnNotes) func(capacity.Key) capacity.View {
	if notes == nil {
		return nil
	}
	return func(k capacity.Key) (v capacity.View) {
		v, notes[k] = s.capacity.cache.Latest(k)
		return v
	}
}

// capacityQueryOf is r's query as the repository's bounded parser reads it
// (parseQuery: a ';' is part of a value), false having refused one it
// cannot read in its fixed words, so no question is answered as another.
func capacityQueryOf(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	q, err := parseQuery(r.URL.RawQuery)
	if err != nil {
		refuse(w, http.StatusBadRequest, "invalid_request", err.Error())
		return nil, false
	}
	return q, true
}

// capacityAgent is the catalog's agent id, false having refused an id not
// of an agent id's form (in words that do not repeat it) or not there.
func (s *Server) capacityAgent(w http.ResponseWriter, id string) (catalog.Agent, bool) {
	if !catalog.ValidID(id) {
		refuse(w, http.StatusBadRequest, "invalid_request", "an agent's id: 1 to 32 of a-z 0-9 -")
		return catalog.Agent{}, false
	}
	agent, ok := s.Catalog().Get(id)
	if !ok {
		refuse(w, http.StatusNotFound, "not_found", "no such agent")
	}
	return agent, ok
}

// capacityPage answers a page of the listing: the views of each installed
// agent (by id) for each of its accounts (by id) after the key after
// names ("agent/account"), until limit or until the next would pass
// maxCapacityPage, but at least one; next names the last key given when
// more follow. Each view is as capacityReply gives it to the caller.
func (s *Server) capacityPage(w http.ResponseWriter, r *http.Request, vq capacityQuery, after string, limit int, notes turnNotes) {
	turn := notes != nil
	doc, err := s.capacity.accounts()
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "capacity_unavailable", "the accounts could not be read")
		return
	}
	var keys []capacity.Key
	for _, agent := range s.Catalog().List() {
		if !s.capacityReadable(r.Context(), agent, vq.held) {
			continue
		}
		keys = append(keys, capacity.Key{Agent: agent.ID, Account: capacity.DefaultID})
		if s.pinned(agent) != "" {
			continue
		}
		for _, a := range doc.Accounts {
			if a.Adapter == agent.Adapter && !a.Retired {
				keys = append(keys, capacity.Key{Agent: agent.ID, Account: a.ID})
			}
		}
	}
	slices.SortFunc(keys, func(a, b capacity.Key) int { return strings.Compare(a.Agent+"/"+a.Account, b.Agent+"/"+b.Account) })
	page := struct {
		Views []json.RawMessage `json:"views"`
		Next  string            `json:"next,omitempty"`
	}{Views: []json.RawMessage{}}
	size := len(`{"views":[],"next":""}`) + 64
	for i, k := range keys {
		if after != "" && k.Agent+"/"+k.Account <= after {
			continue
		}
		if len(page.Views) == limit {
			page.Next = keys[i-1].Agent + "/" + keys[i-1].Account
			break
		}
		agent, _ := s.Catalog().Get(k.Agent)
		q := vq // the page's own question, the account alone replaced
		q.account = k.Account
		v, aerr := s.capacity.viewOf(r.Context(), agent, q, s.capacityLook(notes))
		if aerr != nil {
			continue
		}
		b, err := json.Marshal(capacityReply(turn, v))
		if err != nil || len(b) > maxCapacityView {
			continue
		}
		if len(page.Views) > 0 && size+len(b)+1 > maxCapacityPage {
			page.Next = keys[i-1].Agent + "/" + keys[i-1].Account
			break
		}
		size += len(b) + 1
		page.Views = append(page.Views, b)
	}
	writeCapacityJSON(w, http.StatusOK, page, maxCapacityPage)
}

// snapshot is what a brief's capacity: ref resolves to, as Brakeman reads
// it (brakeman.CapacityOf), made from kept, the reading the readings hold
// (Cache.Kept: its state, plan, windows, credits, limits, time, what read
// it and its version, none when it carries none), with the agent by its
// catalog entry (id, name, adapter), the account by its id, label and
// retired mark as doc lists them (doc nil: the accounts could not be
// read), and this server's counts of the key, counted at now. It names no
// host and no default mark. Whatever the agent or the account is now,
// nothing is read or looked up for it: an agent or account gone since is
// named by its id alone.
func (d *capacityDesk) snapshot(kept capacity.View, doc *capacity.Accounts, now time.Time) brakeman.Capacity {
	k := capacity.Key{Agent: kept.Agent.ID, Account: kept.Account.ID}
	v := kept
	if agent, ok := d.s.Catalog().Get(k.Agent); ok {
		v.Agent.Name, v.Agent.Adapter = agent.Name, agent.Adapter
	}
	if doc != nil {
		if i := slices.IndexFunc(doc.Accounts, func(a capacity.Account) bool { return a.ID == k.Account }); i >= 0 {
			v.Account.Label, v.Account.Retired = doc.Accounts[i].Label, doc.Accounts[i].Retired
		}
	}
	v.Account.Default = false
	v.Counts = d.counts(k)
	return brakeman.CapacityOf(v, now)
}

// queuedView is the view a request asked now keeps (queuedCapacity) and
// the configuration's rev it was made for: made before the desk's lock is
// taken, in a slot of its own, within queuedCapacityWait, which covers
// every step of it. nil past the wait, while another request's view is
// being made, with Brakeman off, or on a server that keeps no capacity.
// The request keeps it only when admitted under that rev.
func (s *Server) queuedView() (*brakeman.Capacity, int64) {
	d := s.capacity
	cfg, err := s.brakeman.st.Config()
	if d == nil || d.st == nil || err != nil || !cfg.Enabled {
		return nil, 0
	}
	select {
	case d.queuing <- struct{}{}:
	default:
		return nil, 0
	}
	ctx, cancel := d.wait()
	defer cancel()
	made := make(chan *brakeman.Capacity, 1)
	go func() {
		defer func() { <-d.queuing }()
		made <- s.queuedCapacity(ctx, cfg)
	}()
	select {
	case c := <-made:
		if ctx.Err() == nil {
			return c, cfg.Rev
		}
	case <-ctx.Done():
	}
	return nil, 0
}

// queuedCapacity is the view a request queued under cfg keeps, as Brakeman
// reads it: of the configured agent on the account its configured folder
// is (none: the agent's own login; else the adapter's registered account
// that holds the folder, looked up as the turn's binding looks it up,
// boundFolder; else a folder not in Accounts, unlisted, never read), from
// the readings kept, looked up as a turn's read looks them up
// (Cache.Latest: reading nothing, asking none, so no key's place moves);
// nil on a server that keeps no capacity, for an agent not in the catalog,
// or while the accounts or the view cannot be read. Its program's look-up
// ends with ctx.
func (s *Server) queuedCapacity(ctx context.Context, cfg brakeman.Config) *brakeman.Capacity {
	d := s.capacity
	agent, ok := s.Catalog().Get(cfg.AgentID)
	if d == nil || d.st == nil || !ok {
		return nil
	}
	account := capacity.DefaultID
	if cfg.Account != "" {
		doc, err := d.accounts()
		if err != nil {
			return nil
		}
		account = session.SourceUnlisted
		if i := slices.IndexFunc(doc.Accounts, func(a capacity.Account) bool { return a.Adapter == agent.Adapter && boundFolder(a, cfg.Account) }); i >= 0 {
			account = doc.Accounts[i].ID
		}
	}
	v := capacity.View{Agent: capacity.ViewAgent{ID: agent.ID, Name: agent.Name, Adapter: agent.Adapter}, Account: capacity.ViewAccount{ID: account},
		Host: capacity.ViewHost{Kind: capacity.HostNative}, State: capacity.StateUnlisted, Fresh: capacity.FreshNone,
		Counts: d.counts(capacity.Key{Agent: agent.ID, Account: account})}
	if account != session.SourceUnlisted {
		var aerr *apiError
		latest := func(k capacity.Key) capacity.View {
			v, _ := d.cache.Latest(k)
			return v
		}
		if v, aerr = d.viewOf(ctx, agent, capacityQuery{account: account, launch: "session"}, latest); aerr != nil {
			return nil
		}
	}
	c := brakeman.CapacityOf(v, time.Now())
	return &c
}

// capacityReadBody is POST /api/capacity/read's body.
type capacityReadBody struct {
	Agent   string `json:"agent"`
	Account string `json:"account,omitempty"`
	Check   bool   `json:"check,omitempty"`
}

// handleCapacityRead answers POST /api/capacity/read: the one route that
// starts a read, the admin's, answered 202 with the view, reading or read
// within the cache's wait; a key read inside MinInterval answers the kept
// reading with nextReadAfter. A check (an owner-only reader's) asks a
// version not yet recorded checked to be run once.
func (s *Server) handleCapacityRead(w http.ResponseWriter, r *http.Request) {
	if !s.capacityAvailable(w) {
		return
	}
	var body capacityReadBody
	if err := decodeJSONLimit(w, r, &body, maxCapacityBody); err != nil {
		writeBodyError(w, err)
		return
	}
	agent, ok := s.capacityAgent(w, body.Agent)
	if !ok {
		return
	}
	if body.Check {
		if !ownerOnlyReaders[agent.Adapter] {
			refuse(w, http.StatusBadRequest, "invalid_request", "only Claude Code's reader is checked: "+agentProgram(agent)+"'s reads cost no turn")
			return
		}
		if owned, _ := s.capacity.ownership(); !owned {
			refuse(w, http.StatusConflict, capacity.CodeNotOwner, capacity.NotOwnerWords)
			return
		}
	}
	read := func(k capacity.Key) capacity.View {
		return s.capacity.cache.Read(r.Context(), k, capacity.ReadOptions{Check: body.Check})
	}
	v, aerr := s.capacity.viewOf(r.Context(), agent, capacityQuery{account: body.Account, launch: "session"}, read)
	if aerr != nil {
		refuse(w, aerr.status, aerr.Code, aerr.Message)
		return
	}
	writeCapacityJSON(w, http.StatusAccepted, v, maxCapacityView)
}

// routeCapacity registers the accounts and capacity routes, the admin's
// launch routes.
func (s *Server) routeCapacity(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/accounts", s.launching(s.handleAccounts))
	mux.HandleFunc("POST /api/accounts", s.launching(s.handleAddAccount))
	mux.HandleFunc("PATCH /api/accounts/{id}", s.launching(s.handleRenameAccount))
	mux.HandleFunc("POST /api/accounts/{id}/retire", s.launching(s.handleRetireAccount))
	mux.HandleFunc("DELETE /api/accounts/{id}", s.launching(s.handleRemoveAccount))
	mux.HandleFunc("PUT /api/accounts/defaults/{adapter}", s.launching(s.handleAccountDefault))
	// The admin's, and a Brakeman turn's under its Capacity class.
	mux.HandleFunc("GET /api/capacity", s.readable("GET /api/capacity", s.launchingAny(s.handleCapacity)))
	mux.HandleFunc("POST /api/capacity/read", s.launching(s.handleCapacityRead))
}
