package capacity

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// The record of versions' bounds.
const (
	MaxVersionsDoc = 16 << 10
	MaxChecked     = 64 // checked records, the oldest dropped
	MaxHeld        = 64 // checking, turned and interrupted records, never dropped
	MaxVersion     = 40 // bytes of a version
)

// The states of a version's record.
const (
	VersionChecking    = "checking"    // a check admitted and not yet done
	VersionChecked     = "checked"     // a run of it was seen answering with no turn
	VersionTurned      = "turned"      // a run of it took a model turn: never run again
	VersionInterrupted = "interrupted" // a check ended without an answer: only the admin's check admits it
)

// versionPattern is a version as a probe parses it, without "@", which
// separates it from its adapter in the document.
var versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,39}$`)

// Outcome is what one run proved, as the reader reports it
// (internal/agents' Outcome, which this package does not import).
type Outcome int

// The outcomes of a run.
const (
	Inconclusive Outcome = iota // the zero value: proves nothing
	Local                       // a result seen saying no turn was taken
	Turned                      // a result seen saying a model turn was taken
)

// versionsDoc is capacity/versions.json: per state, the records as
// "<adapter>@<version>", oldest first.
type versionsDoc struct {
	Schema      int      `json:"schema"`
	Checking    []string `json:"checking"`
	Checked     []string `json:"checked"`
	Turned      []string `json:"turned"`
	Interrupted []string `json:"interrupted"`
}

// versionsWire is the document as it is read: every member is required,
// so a file of null, {}, or a list left null is not the record's shape
// and cannot be read, never an empty record the next save replaces.
type versionsWire struct {
	Schema      *int      `json:"schema"`
	Checking    *[]string `json:"checking"`
	Checked     *[]string `json:"checked"`
	Turned      *[]string `json:"turned"`
	Interrupted *[]string `json:"interrupted"`
}

const versionsSchema = 1

// record is one entry of the record of versions.
type record struct {
	key   string // adapter@version
	state string
}

// Versions is the durable record of which versions of an agent a usage
// read may start (docs/plans/capacity.md, Qualification): one record per
// adapter and version, kept in order of its last change. Every admission
// and every outcome is one transition under its lock (versionsMu in the
// plan), saved before it answers. Only the owner writes it: until
// Store.Own, Admit is refused accounts_not_owner and nothing is saved. A
// turned record is terminal: no later outcome, check or reload changes or
// forgets it.
//
// What is in memory and not on disk is known to be so. An outcome whose
// save failed is held in memory, where this server keeps to it, and the
// record says, under its lock, that it holds what the file does not
// (unsaved). While it does, no Admit and no Done answers success: each
// saves the record first, and when that fails too it is refused
// versions_not_kept and admits nothing. A Reload never replaces the
// owner's record. So a version whose outcome is unsaved is started by no
// one, and once a call has answered success a restart admits nothing
// memory refuses.
type Versions struct {
	st *store.Store

	mu      sync.Mutex
	owned   bool                    // Store.Own has run: admissions and saves are this server's
	recs    []record                // oldest change first
	pending int                     // shared admissions whose Turned would add a held record
	unsaved bool                    // recs hold a change the file does not; only ever true once owned
	seam    func(name string) error // this package's tests: asked before each save, its error the save's
}

func cannotRead(why string, a ...any) error {
	return fmt.Errorf("capacity: %s cannot be read: %s", VersionsFile, fmt.Sprintf(why, a...))
}

// loadVersions reads the record as it is on disk, bounded, strict, of the
// record's shape and within its counts (MaxChecked, MaxHeld); a missing
// one is no record. It writes nothing.
func loadVersions(st *store.Store) ([]record, error) {
	var d versionsWire
	ok, err := st.LoadLimit(VersionsFile, &d, MaxVersionsDoc)
	if err != nil {
		return nil, fmt.Errorf("capacity: %s cannot be read: %w", VersionsFile, err)
	}
	if !ok {
		return nil, nil
	}
	if d.Schema == nil || d.Checking == nil || d.Checked == nil || d.Turned == nil || d.Interrupted == nil {
		return nil, cannotRead("not the record's shape (schema and the four lists checking, checked, turned and interrupted)")
	}
	if *d.Schema != versionsSchema {
		return nil, cannotRead("schema %d, not %d", *d.Schema, versionsSchema)
	}
	if n := len(*d.Checked); n > MaxChecked {
		return nil, cannotRead("%d checked records, at most %d", n, MaxChecked)
	}
	if n := len(*d.Checking) + len(*d.Turned) + len(*d.Interrupted); n > MaxHeld {
		return nil, cannotRead("%d checking, turned and interrupted records, at most %d", n, MaxHeld)
	}
	var recs []record
	seen := map[string]bool{}
	// The lists keep each state's order; a list's records go behind the
	// earlier lists', the order a save made them in.
	for _, l := range []struct {
		list  []string
		state string
	}{{*d.Checked, VersionChecked}, {*d.Turned, VersionTurned}, {*d.Interrupted, VersionInterrupted}, {*d.Checking, VersionChecking}} {
		for _, k := range l.list {
			ad, ver, ok := strings.Cut(k, "@")
			if !ok || !adapterPattern.MatchString(ad) || !versionPattern.MatchString(ver) || seen[k] {
				return nil, cannotRead("%q", k)
			}
			seen[k] = true
			recs = append(recs, record{key: k, state: l.state})
		}
	}
	return recs, nil
}

// openVersions reads the record to check it, and writes nothing: the
// recovery of a check no process finished is the owner's (own).
func openVersions(st *store.Store) (*Versions, error) {
	recs, err := loadVersions(st)
	if err != nil {
		return nil, err
	}
	return &Versions{st: st, recs: recs}, nil
}

// Reload reads the record again as it is on disk, writing nothing: what a
// server that does not own the folder shows. A turned record already known
// stays turned, whatever the file says. Once Own has run it is refused, in
// words, and changes nothing: the owner's record is the one in memory, an
// outcome it holds unsaved is saved by its next Admit or Done, and the
// file only ever lags it.
func (v *Versions) Reload() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.owned {
		return fmt.Errorf("capacity: %s is read again only by a server that does not own capacity/: this one owns it, and its record in memory is the record", VersionsFile)
	}
	_, err := v.reloadLocked()
	return err
}

// reloadLocked replaces the records with the file's, keeping every turned
// one known; added says a turned record was kept that the file lacks. The
// caller holds v.mu, and the record is not owned yet, so memory holds
// nothing unsaved that the file's could replace.
func (v *Versions) reloadLocked() (added bool, err error) {
	recs, err := loadVersions(v.st)
	if err != nil {
		return false, err
	}
	for _, r := range v.recs {
		if r.state != VersionTurned {
			continue
		}
		i := slices.IndexFunc(recs, func(o record) bool { return o.key == r.key })
		switch {
		case i < 0:
			recs, added = append(recs, r), true
		case recs[i].state != VersionTurned:
			recs[i].state, added = VersionTurned, true
		}
	}
	v.recs = recs
	return added, nil
}

// own is Store.Own's part: the record read again, every checking record
// (a check no process finished: a crash, a kill) made interrupted and
// saved, and the record this server's from then on. It runs once.
func (v *Versions) own() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.owned {
		return nil
	}
	changed, err := v.reloadLocked()
	if err != nil {
		return err
	}
	for i := range v.recs {
		if v.recs[i].state == VersionChecking {
			v.recs[i].state, changed = VersionInterrupted, true
		}
	}
	if changed {
		if err := v.save(); err != nil {
			return fmt.Errorf("capacity: the checks no process finished could not be saved interrupted in %s, so the folder is not owned: %w", VersionsFile, err)
		}
	}
	v.owned = true
	return nil
}

// NotKeptWords begin the words of a versions_not_kept refusal.
const NotKeptWords = "an outcome is held in memory and not yet kept in capacity/versions.json"

// keepLocked saves the record when memory holds what the file does not,
// and answers versions_not_kept, in words, when that save fails; the
// caller holds v.mu.
func (v *Versions) keepLocked() error {
	if !v.unsaved {
		return nil
	}
	if err := v.save(); err != nil {
		return &Error{Code: CodeNotKept, Words: fmt.Sprintf("%s (%v): this server keeps to it and starts no read until it is kept; the next admission or outcome saves it again", NotKeptWords, err)}
	}
	v.unsaved = false
	return nil
}

// save writes the record whole, measured first; the caller holds v.mu.
func (v *Versions) save() error {
	d := versionsDoc{Schema: versionsSchema, Checking: []string{}, Checked: []string{}, Turned: []string{}, Interrupted: []string{}}
	for _, r := range v.recs {
		switch r.state {
		case VersionChecking:
			d.Checking = append(d.Checking, r.key)
		case VersionChecked:
			d.Checked = append(d.Checked, r.key)
		case VersionTurned:
			d.Turned = append(d.Turned, r.key)
		case VersionInterrupted:
			d.Interrupted = append(d.Interrupted, r.key)
		}
	}
	b, err := store.Encode(d)
	if err != nil {
		return err
	}
	if len(b) > MaxVersionsDoc {
		return fmt.Errorf("capacity: %s would pass %d bytes", VersionsFile, MaxVersionsDoc)
	}
	if v.seam != nil {
		if err := v.seam(VersionsFile); err != nil {
			return err
		}
	}
	return v.st.Save(VersionsFile, d)
}

func (v *Versions) find(key string) int {
	return slices.IndexFunc(v.recs, func(r record) bool { return r.key == key })
}

// held counts the records never dropped.
func (v *Versions) held() int {
	n := 0
	for _, r := range v.recs {
		if r.state != VersionChecked {
			n++
		}
	}
	return n
}

// set gives key its state, as its newest change, and drops the oldest
// checked records past MaxChecked; the caller holds v.mu and saves when it
// answers true. A turned record is terminal: set leaves it as it is and
// answers false.
func (v *Versions) set(key, state string) bool {
	if i := v.find(key); i >= 0 {
		if v.recs[i].state == VersionTurned {
			return false
		}
		v.recs = slices.Delete(v.recs, i, i+1)
	}
	v.recs = append(v.recs, record{key: key, state: state})
	checked := 0
	for _, r := range v.recs {
		if r.state == VersionChecked {
			checked++
		}
	}
	for i := 0; checked > MaxChecked && i < len(v.recs); {
		if v.recs[i].state == VersionChecked {
			v.recs = slices.Delete(v.recs, i, i+1)
			checked--
			continue
		}
		i++
	}
	return true
}

// State answers the record of adapter's version: one of the Version
// states, or "" when it has none.
func (v *Versions) State(adapter, version string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if i := v.find(adapter + "@" + version); i >= 0 {
		return v.recs[i].state
	}
	return ""
}

func unqualified(words string, a ...any) error {
	return &Error{Code: CodeUnqualified, Words: fmt.Sprintf(words, a...)}
}

// Admit asks leave to start one run of adapter's program at version,
// whichever account asks. qualified says the version lies from the
// reader's MinVersion to its Qualified version (the caller compares);
// check is the admin's explicit check.
//
// A server that does not own the folder (no Store.Own) is refused
// accounts_not_owner and starts nothing: it does not hold the record a
// run's outcome must be saved in. A record holding an outcome not yet
// saved is saved first, and while that fails every ask is refused
// versions_not_kept and starts nothing. A turned version is refused, even
// to a check. A qualified or checked
// version is admitted for every read, sharing no record. Any other is
// refused unless check: then, when it has no record or an interrupted
// one, it becomes checking and is saved before Admit returns, so the
// record is on disk before the program starts, and a second ask while it
// is checking is refused check_under_way. A check that would add a
// record never dropped once MaxHeld of them are kept, or could be by the
// runs admitted, is refused versions_full rather than forgetting one.
func (v *Versions) Admit(adapter, version string, qualified, check bool) (*Admission, error) {
	if !adapterPattern.MatchString(adapter) || !versionPattern.MatchString(version) {
		return nil, unqualified("no version this reader can record (%.40q)", version)
	}
	key := adapter + "@" + version
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.owned {
		return nil, notOwner()
	}
	if err := v.keepLocked(); err != nil {
		return nil, err
	}
	state := ""
	if i := v.find(key); i >= 0 {
		state = v.recs[i].state
	}
	full := func() error {
		return &Error{Code: CodeVersionsFull, Words: fmt.Sprintf("%d versions are recorded as checking, turned or interrupted: no other version is started until one is cleared", MaxHeld)}
	}
	switch {
	case state == VersionTurned:
		return nil, unqualified("a run of %s took a model turn: it is not started again", version)
	case state == VersionChecking:
		return nil, &Error{Code: CodeCheckUnderWay, Words: fmt.Sprintf("a check of %s is under way: wait for it to end", version)}
	case qualified || state == VersionChecked:
		// A Turned outcome of this run adds a held record: count it.
		if v.held()+v.pending >= MaxHeld {
			return nil, full()
		}
		v.pending++
		return &Admission{v: v, key: key}, nil
	case !check && state == VersionInterrupted:
		return nil, unqualified("a check of %s was interrupted: check it again", version)
	case !check:
		return nil, unqualified("%s has not been checked: check this version to read it", version)
	}
	if state == "" && v.held()+v.pending >= MaxHeld {
		return nil, full()
	}
	prev := slices.Clone(v.recs)
	_ = v.set(key, VersionChecking) // not turned: refused above
	if err := v.save(); err != nil {
		v.recs = prev // memory as the file is: nothing held
		return nil, fmt.Errorf("capacity: the check of %s could not be saved in %s, so it is not started: %w", version, VersionsFile, err)
	}
	return &Admission{v: v, key: key, exclusive: true}, nil
}

// Admission is one run's leave to start, from Admit. Done is called once,
// with what the run's own output proved, and again while it answers
// versions_not_kept.
type Admission struct {
	v         *Versions
	key       string
	exclusive bool // a check: the version is checking until Done
	done      bool // under v.mu: the outcome is in the record
}

// Done records the run's outcome and saves it before it answers success.
// For a check, Local makes the version checked, Turned turned and
// Inconclusive interrupted. For a read shared with others, Turned records
// the version turned and Local or Inconclusive leave its record as it is.
// A version recorded turned meanwhile (by another run admitted while this
// one ran) stays turned, whatever this run proved. Only the first call's
// outcome counts. When the record then holds anything the file does not
// (this outcome, or another's whose save failed), it is saved first; when
// that fails, Done answers versions_not_kept: the outcome is held in
// memory, which this server keeps to and admits nothing past, and the next
// Admit or Done (this one called again included) saves it.
func (a *Admission) Done(o Outcome) error {
	v := a.v
	v.mu.Lock()
	defer v.mu.Unlock()
	if !a.done {
		a.done = true
		if !a.exclusive {
			v.pending--
		}
		state := ""
		switch {
		case o == Turned:
			state = VersionTurned
		case !a.exclusive:
		case o == Local:
			state = VersionChecked
		default:
			state = VersionInterrupted
		}
		if state != "" && v.set(a.key, state) {
			v.unsaved = true
		}
	}
	return v.keepLocked()
}
