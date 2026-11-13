// Package capacity is what Conductor knows of the logins its agents use
// and of their usage (docs/plans/capacity.md, Program item AC1): account
// references kept in capacity/accounts.json, the record of the agent
// versions a usage read may start kept in capacity/versions.json, and the
// readings of an agent's usage, kept in memory only (Cache) and shown as a
// View. Like internal/brakeman it has no HTTP, no session and no process:
// it imports the standard library and internal/store alone, and what it
// cannot know (an adapter, a folder, what is bound to an account) it asks
// of Facts, which internal/api implements. internal/crew never imports it.
//
// An account is a reference, never a credential: an adapter, a label and
// the folder the agent's own login lives in, under an id the server mints.
// Nothing in the folder is read, and no email, organisation, key, token or
// provider id is ever kept: Account has no field that could hold one.
//
// The documents, each measured as store.Encode writes it before a write
// and bounded before a read, so what is written always loads back:
//
//	capacity/                 0700
//	capacity/accounts.json    the accounts (Accounts), 0600, MaxAccountsDoc
//	capacity/versions.json    the versions (Versions), 0600, MaxVersionsDoc
//
// A document is its shape or it cannot be read: a file that is not the
// document (null, {}, a list, a number, a member missing), past its bound
// or past its counts is an error in words, never an empty document a
// later write replaces. One server writes them: the one that holds
// capacity/owner.lock. Open reads and checks, and writes nothing; the
// holder of the lock calls Store.Own once it holds it, which concludes a
// check no process finished and lets the Store write. Until then every
// write, every Reserve of a registered account and every Versions.Admit is
// refused accounts_not_owner. From Own on, the owner's record of versions
// is the one in memory, and Versions.Reload, for a server that does not own
// the folder, is refused. The accounts document holds nothing in memory: a
// write that could not be saved changed nothing.
package capacity

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// The documents' names and the accounts' bounds.
const (
	Dir            = "capacity"
	AccountsFile   = "accounts.json"
	VersionsFile   = "versions.json"
	MaxAccountsDoc = 64 << 10
	MaxAccounts    = 32   // accounts in the document, retired ones included
	MaxRemoved     = 256  // ids kept in Removed
	MaxLabel       = 60   // characters of a label
	MaxFolder      = 4096 // bytes of a folder's path
	MaxBound       = 10   // what an account_in_use refusal names
	// DefaultID is the account every adapter has without its being
	// stored: the agent's own login on this server, no folder.
	DefaultID = "default"
)

// idPattern is a minted id: "a-" and 12 characters of the lower-case
// base32 alphabet.
var idPattern = regexp.MustCompile(`^a-[a-z2-7]{12}$`)

// adapterPattern bounds an adapter's name as the catalog's adapters are named.
var adapterPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Accounts is capacity/accounts.json.
type Accounts struct {
	Rev      int64             `json:"rev"`
	Accounts []Account         `json:"accounts"`
	Defaults map[string]string `json:"defaults,omitempty"` // adapter -> account id; absent is "default"
	Removed  []string          `json:"removed,omitempty"`  // ids removed, never minted again; at most MaxRemoved
}

// Account is a reference to an agent's own login on this server: the
// folder its adapter's ConfigDirEnv is set to. It holds nothing the login
// itself holds.
type Account struct {
	ID        string    `json:"id"`      // minted by the server: "a-" and 12 base32 characters; never "default"
	Adapter   string    `json:"adapter"` // an adapter with a ConfigDirEnv
	Label     string    `json:"label"`   // 1 to MaxLabel characters
	Folder    string    `json:"folder"`  // absolute, at most MaxFolder bytes; fixed once added
	Retired   bool      `json:"retired,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Facts answers what internal/capacity cannot know itself. internal/api
// implements it. Store calls it under the accounts' lock, so it must not
// call the Store.
type Facts interface {
	// ConfigDirEnv answers the environment variable the adapter's login
	// folder is moved with ("" for an adapter that has none); ok false
	// when there is no such adapter.
	ConfigDirEnv(adapter string) (env string, ok bool)
	// CheckAccountDir checks an account's folder as Brakeman's account
	// folder is checked: a directory the server's user owns. The path is
	// absolute, clean and bounded already.
	CheckAccountDir(dir string) error
	// Bound names, in words, what the server lists bound to the account
	// a, by its id or by its folder: a session, an engine run, a ledger
	// run running or interrupted. None is nil. Reservations are counted by
	// the Store itself.
	Bound(a Account) []string
}

// The codes of an Error.
const (
	CodeInvalidAccount = "invalid_account"
	CodeStaleRev       = "stale_rev"
	CodeInUse          = "account_in_use"
	CodeCheckUnderWay  = "check_under_way"
	CodeVersionsFull   = "versions_full"
	CodeUnqualified    = "unqualified"
	CodeNotOwner       = "accounts_not_owner"
	CodeNotKept        = "versions_not_kept"
)

// Error is a refusal, with the code a route answers, the field it is
// about ("" for none), why in words, and for account_in_use what holds
// the account (at most MaxBound).
type Error struct {
	Code  string
	Field string
	Words string
	Names []string
}

func (e *Error) Error() string {
	s := e.Code + ": " + e.Words
	if e.Field != "" {
		s = e.Code + ": " + e.Field + ": " + e.Words
	}
	if len(e.Names) > 0 {
		s += " (" + strings.Join(e.Names, ", ") + ")"
	}
	return s
}

// NotOwnerWords are the words of an accounts_not_owner refusal.
const NotOwnerWords = "this server does not own capacity/ (another server over this data directory holds capacity/owner.lock, or this platform has no owner lock): it writes neither the accounts nor the record of versions, and starts no read that must be recorded there"

func notOwner() error { return &Error{Code: CodeNotOwner, Words: NotOwnerWords} }

func invalid(field, words string, a ...any) error {
	return &Error{Code: CodeInvalidAccount, Field: field, Words: fmt.Sprintf(words, a...)}
}

// Validate checks an account as an add makes it: its id minted, never
// "default"; an adapter that exists and has a ConfigDirEnv; a label of 1
// to MaxLabel characters with no control character; an absolute, clean
// folder of at most MaxFolder bytes that Facts accepts.
func Validate(a Account, f Facts) error {
	if err := checkShape(a); err != nil {
		return err
	}
	env, ok := f.ConfigDirEnv(a.Adapter)
	switch {
	case !ok:
		return invalid("adapter", "no such adapter: %s", a.Adapter)
	case env == "":
		return invalid("adapter", "%s cannot move its login folder, so it has its default login only", a.Adapter)
	}
	if err := f.CheckAccountDir(a.Folder); err != nil {
		return invalid("folder", "%v", err)
	}
	return nil
}

// checkShape is what Validate checks without Facts, which a document
// read back is held to as well.
func checkShape(a Account) error {
	switch {
	case a.ID == DefaultID:
		return invalid("id", "%q is every adapter's own login and is never an account's id", DefaultID)
	case !idPattern.MatchString(a.ID):
		return invalid("id", "an id the server minted (a- and 12 base32 characters)")
	case !adapterPattern.MatchString(a.Adapter):
		return invalid("adapter", "an adapter's name")
	case a.CreatedAt.IsZero():
		return invalid("createdAt", "the time the account was added")
	}
	if err := checkLabel(a.Label); err != nil {
		return err
	}
	return checkFolder(a.Folder)
}

func checkLabel(l string) error {
	n := utf8.RuneCountInString(l)
	switch {
	case !utf8.ValidString(l) || strings.ContainsFunc(l, unicode.IsControl):
		return invalid("label", "UTF-8 with no control character")
	case strings.TrimSpace(l) == "" || n > MaxLabel:
		return invalid("label", "1 to %d characters", MaxLabel)
	}
	return nil
}

func checkFolder(p string) error {
	switch {
	case len(p) > MaxFolder:
		return invalid("folder", "at most %d bytes", MaxFolder)
	case !utf8.ValidString(p) || strings.ContainsFunc(p, unicode.IsControl):
		return invalid("folder", "UTF-8 with no control character")
	case !filepath.IsAbs(p):
		return invalid("folder", "an absolute path")
	case filepath.Clean(p) != p:
		return invalid("folder", "a clean path (%s)", filepath.Clean(p))
	}
	return nil
}

// checkDoc holds a document read back, or about to be written, to its
// shape and bounds.
func checkDoc(d *Accounts) error {
	if d.Rev < 0 {
		return errors.New("a negative rev")
	}
	if len(d.Accounts) > MaxAccounts {
		return fmt.Errorf("more than %d accounts", MaxAccounts)
	}
	if len(d.Removed) > MaxRemoved {
		return fmt.Errorf("more than %d removed ids", MaxRemoved)
	}
	seen := map[string]bool{}
	for i, a := range d.Accounts {
		if err := checkShape(a); err != nil {
			return fmt.Errorf("accounts[%d]: %w", i, err)
		}
		if seen[a.ID] {
			return fmt.Errorf("accounts[%d]: %s is named twice", i, a.ID)
		}
		seen[a.ID] = true
	}
	for i, id := range d.Removed {
		if !idPattern.MatchString(id) || seen[id] {
			return fmt.Errorf("removed[%d]: %q is not an id removed", i, id)
		}
		seen[id] = true
	}
	for ad, id := range d.Defaults {
		if !adapterPattern.MatchString(ad) || (id != DefaultID && !idPattern.MatchString(id)) {
			return fmt.Errorf("defaults: %q: %q", ad, id)
		}
	}
	return nil
}

// Options are what Open takes. Zero values are the real clock and
// crypto/rand.
type Options struct {
	Now  func() time.Time
	Rand io.Reader // where minted ids come from
}

// Store is capacity/: the accounts document and its reservations, and
// the record of versions (Versions). Its lock (accountsMu in the plan) is
// held by every write, by Reserve, and by Remove while it asks Facts.
type Store struct {
	st   *store.Store
	now  func() time.Time
	rand io.Reader
	ver  *Versions

	mu       sync.Mutex
	owned    bool                    // Own has run
	reserved map[string]int          // account id -> launches under way
	seam     func(name string) error // this package's tests: asked before each save, its error the save's
}

// Open opens capacity/ in the data directory (made 0700) and reads both
// documents to check them. It writes nothing, and the Store it answers
// does not own the folder: every write, every Reserve of a registered
// account and every Versions.Admit is refused accounts_not_owner until
// Own. A document that does not load (past its bound, not its shape, not
// strict JSON, past its counts) is an error that says it cannot be read,
// never an empty document, so no later write overwrites it.
func Open(data *store.Store, o Options) (*Store, error) {
	st, err := data.Sub(Dir)
	if err != nil {
		return nil, err
	}
	s := &Store{st: st, now: o.Now, rand: o.Rand, reserved: map[string]int{}}
	if s.now == nil {
		s.now = time.Now
	}
	if s.rand == nil {
		s.rand = rand.Reader
	}
	if _, err := s.load(); err != nil {
		return nil, err
	}
	if s.ver, err = openVersions(st); err != nil {
		return nil, err
	}
	return s, nil
}

// Own is the call the server that holds capacity/owner.lock makes once it
// holds it (at its start, or when it takes the lock after waiting for
// it): it reads both documents again, makes every checking record of the
// record of versions interrupted (a check no process finished) and saves
// that, and from then on lets this Store write. Nothing else in the
// package writes before it. A second call does nothing. An error leaves
// the Store not owning, and its documents as they were.
func (s *Store) Own() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owned {
		return nil
	}
	if _, err := s.load(); err != nil {
		return err
	}
	if err := s.ver.own(); err != nil {
		return err
	}
	s.owned = true
	return nil
}

// Disown is Own's inverse, the call a server makes before it lets
// capacity/owner.lock go: under the Store's lock, so a write under way
// ends first, and from then on every write, Reserve and Admit is refused
// accounts_not_owner, as before Own, and no outcome is saved.
func (s *Store) Disown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owned = false
	s.ver.mu.Lock()
	defer s.ver.mu.Unlock()
	// An outcome whose save failed is tried once more while this Store
	// still owns the record; failing again, it goes with the process.
	_ = s.ver.keepLocked()
	s.ver.owned = false
}

// Folders answers the folder of every account in the accounts document of
// the data directory dataDir, retired ones included: the document read and
// judged as the Store reads it (loadAccounts), with no lock and no record of
// versions, but for a member it does not know, which is passed over so that
// a later version's document still names its folders. A document that is
// not there answers none; one the Store would refuse is an error.
func Folders(dataDir string) ([]string, error) {
	d, err := loadAccounts(store.At(filepath.Join(dataDir, Dir)), json.Unmarshal)
	var out []string
	for _, a := range d.Accounts {
		out = append(out, a.Folder)
	}
	return out, err
}

// ValidID says id has an account id's form: "default", or a minted one.
func ValidID(id string) bool { return id == DefaultID || idPattern.MatchString(id) }

// Versions is the record of versions in the same folder.
func (s *Store) Versions() *Versions { return s.ver }

// accountsWire is the document as it is read: rev and accounts are
// required, so a file of null or {} is not the document's shape.
type accountsWire struct {
	Rev      *int64            `json:"rev"`
	Accounts *[]Account        `json:"accounts"`
	Defaults map[string]string `json:"defaults,omitempty"`
	Removed  []string          `json:"removed,omitempty"`
}

// load reads the document, bounded and strict; a missing one is rev 0
// with no account. One that exists is the document this package writes or
// cannot be read: an object with a rev of 1 or more (every write bumps it
// from 0) and an accounts list, within its counts (checkDoc).
func (s *Store) load() (Accounts, error) { return loadAccounts(s.st, store.DecodeStrict) }

// loadAccounts is load from st, its bytes decoded by decode.
func loadAccounts(st *store.Store, decode func([]byte, any) error) (Accounts, error) {
	cannot := func(err error) error { return fmt.Errorf("capacity: %s cannot be read: %w", AccountsFile, err) }
	var raw json.RawMessage
	var w accountsWire
	ok, err := st.LoadLimit(AccountsFile, &raw, MaxAccountsDoc)
	if err == nil && ok {
		if err = decode(raw, &w); err != nil {
			err = &store.ParseError{Name: AccountsFile, Err: err}
		}
	}
	if err != nil {
		return Accounts{}, cannot(err)
	}
	if !ok {
		return Accounts{Accounts: []Account{}}, nil
	}
	switch {
	case w.Rev == nil || w.Accounts == nil:
		return Accounts{}, cannot(errors.New("not the accounts document's shape (an object with rev and an accounts list)"))
	case *w.Rev < 1:
		return Accounts{}, cannot(fmt.Errorf("rev %d: every document this server writes is at rev 1 or more", *w.Rev))
	}
	d := Accounts{Rev: *w.Rev, Accounts: *w.Accounts, Defaults: w.Defaults, Removed: w.Removed}
	if err := checkDoc(&d); err != nil {
		return Accounts{}, cannot(err)
	}
	return d, nil
}

// Accounts answers the document as it is on disk.
func (s *Store) Accounts() (Accounts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// update is every write: under the lock, read the document, check the
// rev the caller read, let change make the new one, bump the rev once,
// check it and its encoded size, and save it before answering.
func (s *Store) update(expectedRev int64, change func(d *Accounts) error) (Accounts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.owned {
		return Accounts{}, notOwner()
	}
	d, err := s.load()
	if err != nil {
		return Accounts{}, err
	}
	if d.Rev != expectedRev {
		return Accounts{}, &Error{Code: CodeStaleRev, Words: fmt.Sprintf("the accounts changed since they were read: they are at rev %d, the write names %d", d.Rev, expectedRev)}
	}
	if err := change(&d); err != nil {
		return Accounts{}, err
	}
	d.Rev++
	if err := checkDoc(&d); err != nil {
		return Accounts{}, invalid("", "%v", err)
	}
	b, err := store.Encode(d)
	if err != nil {
		return Accounts{}, err
	}
	if len(b) > MaxAccountsDoc {
		return Accounts{}, invalid("", "the accounts would pass %d bytes", MaxAccountsDoc)
	}
	if s.seam != nil {
		if err := s.seam(AccountsFile); err != nil {
			return Accounts{}, err
		}
	}
	if err := s.st.Save(AccountsFile, d); err != nil {
		return Accounts{}, err
	}
	return d, nil
}

func find(d *Accounts, id string) (int, error) {
	i := slices.IndexFunc(d.Accounts, func(a Account) bool { return a.ID == id })
	if i < 0 {
		return -1, invalid("id", "no such account: %s", id)
	}
	return i, nil
}

// Add adds an account under an id the server mints, never one the
// document holds or has removed, and never taken from the label.
func (s *Store) Add(f Facts, adapter, label, folder string, expectedRev int64) (Account, int64, error) {
	var out Account
	d, err := s.update(expectedRev, func(d *Accounts) error {
		if len(d.Accounts) >= MaxAccounts {
			return invalid("", "at most %d accounts, retired ones included", MaxAccounts)
		}
		id, err := s.mint(d)
		if err != nil {
			return err
		}
		a := Account{ID: id, Adapter: adapter, Label: label, Folder: folder, CreatedAt: s.now().UTC()}
		if err := Validate(a, f); err != nil {
			return err
		}
		for _, o := range d.Accounts {
			if o.Adapter == adapter && o.Folder == folder {
				return invalid("folder", "the account %q of %s has this folder already", o.Label, adapter)
			}
		}
		d.Accounts = append(d.Accounts, a)
		out = a
		return nil
	})
	if err != nil {
		return Account{}, 0, err
	}
	return out, d.Rev, nil
}

// mintTries bounds the draws of an id; each is 60 random bits, so a
// second draw is already rare.
const mintTries = 8

var base32 = "abcdefghijklmnopqrstuvwxyz234567"

// mint draws an id the document has never held: not an account's, not a
// removed one's, and never "default".
func (s *Store) mint(d *Accounts) (string, error) {
	taken := map[string]bool{}
	for _, a := range d.Accounts {
		taken[a.ID] = true
	}
	for _, id := range d.Removed {
		taken[id] = true
	}
	var b [12]byte
	for range mintTries {
		if _, err := io.ReadFull(s.rand, b[:]); err != nil {
			return "", fmt.Errorf("capacity: mint an id: %w", err)
		}
		for i := range b {
			b[i] = base32[b[i]%32]
		}
		if id := "a-" + string(b[:]); !taken[id] {
			return id, nil
		}
	}
	return "", errors.New("capacity: no new id after several draws")
}

// Rename changes an account's label. Nothing changes its folder.
func (s *Store) Rename(id, label string, expectedRev int64) (Account, int64, error) {
	var out Account
	d, err := s.update(expectedRev, func(d *Accounts) error {
		i, err := find(d, id)
		if err != nil {
			return err
		}
		if err := checkLabel(label); err != nil {
			return err
		}
		d.Accounts[i].Label = label
		out = d.Accounts[i]
		return nil
	})
	if err != nil {
		return Account{}, 0, err
	}
	return out, d.Rev, nil
}

// Retire keeps the account for what is bound to it and leaves it out of
// every choice and default.
func (s *Store) Retire(id string, expectedRev int64) (Account, int64, error) {
	var out Account
	d, err := s.update(expectedRev, func(d *Accounts) error {
		i, err := find(d, id)
		if err != nil {
			return err
		}
		d.Accounts[i].Retired = true
		out = d.Accounts[i]
		return nil
	})
	if err != nil {
		return Account{}, 0, err
	}
	return out, d.Rev, nil
}

// Remove deletes the account and keeps its id in Removed, so it is never
// minted again. It is refused while a launch holds a reservation on it or
// Facts names anything bound to it.
func (s *Store) Remove(f Facts, id string, expectedRev int64) (int64, error) {
	d, err := s.update(expectedRev, func(d *Accounts) error {
		i, err := find(d, id)
		if err != nil {
			return err
		}
		var names []string
		if n := s.reserved[id]; n > 0 {
			names = append(names, fmt.Sprintf("%d launch(es) under way", n))
		}
		names = append(names, f.Bound(d.Accounts[i])...)
		if len(names) > 0 {
			return &Error{Code: CodeInUse, Words: "the account is in use: stop what is bound to it, or retire it", Names: names[:min(len(names), MaxBound)]}
		}
		if len(d.Removed) >= MaxRemoved {
			return invalid("id", "%d accounts have been removed already: retire this one instead", MaxRemoved)
		}
		d.Accounts = slices.Delete(d.Accounts, i, i+1)
		d.Removed = append(d.Removed, id)
		for ad, def := range d.Defaults {
			if def == id {
				delete(d.Defaults, ad)
			}
		}
		if len(d.Defaults) == 0 {
			d.Defaults = nil
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return d.Rev, nil
}

// SetDefault makes id the adapter's default: an account of that adapter
// that is not retired, or "default" for the agent's own login.
func (s *Store) SetDefault(adapter, id string, expectedRev int64) (map[string]string, int64, error) {
	d, err := s.update(expectedRev, func(d *Accounts) error {
		if !adapterPattern.MatchString(adapter) {
			return invalid("adapter", "an adapter's name")
		}
		if id == DefaultID {
			delete(d.Defaults, adapter)
			if len(d.Defaults) == 0 {
				d.Defaults = nil
			}
			return nil
		}
		i, err := find(d, id)
		if err != nil {
			return err
		}
		switch a := d.Accounts[i]; {
		case a.Adapter != adapter:
			return invalid("account", "%s is an account of %s, not of %s", id, a.Adapter, adapter)
		case a.Retired:
			return invalid("account", "%s is retired", id)
		}
		if d.Defaults == nil {
			d.Defaults = map[string]string{}
		}
		d.Defaults[adapter] = id
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return maps.Clone(d.Defaults), d.Rev, nil
}

// DefaultOf answers the adapter's default account id in a document:
// "default" when none is set or the one set is retired or gone.
func DefaultOf(d Accounts, adapter string) string {
	id, ok := d.Defaults[adapter]
	if !ok {
		return DefaultID
	}
	for _, a := range d.Accounts {
		if a.ID == id && a.Adapter == adapter && !a.Retired {
			return id
		}
	}
	return DefaultID
}

// Reserve takes a reservation on an account for a launch that resolved
// it, under the accounts' lock, so Remove is refused until release is
// called (once the session is registered or the run's record holds the
// binding, or the launch failed; a second call does nothing). The account
// must exist and not be retired, and the Store own the folder (Own): a
// server that does not cannot see a removal, so it binds no registered
// account. "default" needs no reservation: it answers a zero Account and
// a release that does nothing.
func (s *Store) Reserve(id string) (Account, func(), error) { return s.reserve(id, false) }

// Hold is Reserve for a launch made again under a binding (a resume): a
// retired account, kept for what is bound to it, is held as well.
func (s *Store) Hold(id string) (Account, func(), error) { return s.reserve(id, true) }

// HoldOf is Hold for each account pick names in the accounts as they are
// under the accounts' lock, so no Remove comes between the pick and the
// reservations; an id the document does not hold is passed over. It
// answers the ids it holds.
func (s *Store) HoldOf(pick func(Accounts) []string) ([]string, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.owned {
		return nil, nil, notOwner()
	}
	d, err := s.load()
	if err != nil {
		return nil, nil, err
	}
	var ids []string
	for _, id := range pick(d) {
		if _, err := find(&d, id); err == nil {
			s.reserved[id]++
			ids = append(ids, id)
		}
	}
	return ids, s.releaser(ids), nil
}

func (s *Store) reserve(id string, retired bool) (Account, func(), error) {
	if id == DefaultID {
		return Account{}, func() {}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.owned {
		return Account{}, nil, notOwner()
	}
	d, err := s.load()
	if err != nil {
		return Account{}, nil, err
	}
	i, err := find(&d, id)
	if err != nil {
		return Account{}, nil, err
	}
	a := d.Accounts[i]
	if a.Retired && !retired {
		return Account{}, nil, invalid("account", "%s is retired", id)
	}
	s.reserved[id]++
	return a, s.releaser([]string{id}), nil
}

// releaser lets go, once, of a reservation on each of ids.
func (s *Store) releaser(ids []string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, id := range ids {
				if s.reserved[id]--; s.reserved[id] <= 0 {
					delete(s.reserved, id)
				}
			}
		})
	}
}
