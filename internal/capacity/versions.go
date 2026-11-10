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
// plan), saved before it answers.
type Versions struct {
	st *store.Store

	mu      sync.Mutex
	recs    []record // oldest change first
	pending int      // shared admissions whose Turned would add a held record
}

// openVersions reads the record and, since a checking record at open is a
// check no process finished, makes every one interrupted and saves that.
func openVersions(st *store.Store) (*Versions, error) {
	v := &Versions{st: st}
	var d versionsDoc
	ok, err := st.LoadLimit(VersionsFile, &d, MaxVersionsDoc)
	if err != nil {
		return nil, err
	}
	if !ok {
		return v, nil
	}
	if d.Schema != versionsSchema {
		return nil, fmt.Errorf("capacity: %s: schema %d, not %d", VersionsFile, d.Schema, versionsSchema)
	}
	seen := map[string]bool{}
	add := func(list []string, state string) error {
		for _, k := range list {
			ad, ver, ok := strings.Cut(k, "@")
			if !ok || !adapterPattern.MatchString(ad) || !versionPattern.MatchString(ver) || seen[k] {
				return fmt.Errorf("capacity: %s: %q", VersionsFile, k)
			}
			seen[k] = true
			v.recs = append(v.recs, record{key: k, state: state})
		}
		return nil
	}
	// The lists keep each state's order; a list's records go behind the
	// earlier lists', the order a save made them in.
	for _, l := range []struct {
		list  []string
		state string
	}{{d.Checked, VersionChecked}, {d.Turned, VersionTurned}, {d.Interrupted, VersionInterrupted}, {d.Checking, VersionChecking}} {
		if err := add(l.list, l.state); err != nil {
			return nil, err
		}
	}
	if len(d.Checking) == 0 {
		return v, nil
	}
	for i := range v.recs {
		if v.recs[i].state == VersionChecking {
			v.recs[i].state = VersionInterrupted
		}
	}
	if err := v.save(); err != nil {
		return nil, err
	}
	return v, nil
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
// checked records past MaxChecked; the caller holds v.mu and saves.
func (v *Versions) set(key, state string) {
	if i := v.find(key); i >= 0 {
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
// A turned version is refused, even to a check. A qualified or checked
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
	v.set(key, VersionChecking)
	if err := v.save(); err != nil {
		v.recs = prev
		return nil, err
	}
	return &Admission{v: v, key: key, exclusive: true}, nil
}

// Admission is one run's leave to start, from Admit. Done is called
// exactly once, with what the run's own output proved.
type Admission struct {
	v         *Versions
	key       string
	exclusive bool // a check: the version is checking until Done
	once      sync.Once
}

// Done records the run's outcome and saves it before it returns. For a
// check, Local makes the version checked, Turned turned and Inconclusive
// interrupted. For a read shared with others, Turned records the version
// turned and Local or Inconclusive leave its record as it is. Only the
// first call counts; the record in memory holds the outcome even when the
// save fails, and the error says the save failed.
func (a *Admission) Done(o Outcome) error {
	var err error
	a.once.Do(func() {
		v := a.v
		v.mu.Lock()
		defer v.mu.Unlock()
		if !a.exclusive {
			v.pending--
		}
		var state string
		switch {
		case o == Turned:
			state = VersionTurned
		case !a.exclusive:
			return
		case o == Local:
			state = VersionChecked
		default:
			state = VersionInterrupted
		}
		v.set(a.key, state)
		err = v.save()
	})
	return err
}
