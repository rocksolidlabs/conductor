package crew

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
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

// crewsDir is the directory of the data directory that holds one file per
// crew, <id>.json.
const crewsDir = "crews"

// maxID bounds an ID. Derived IDs are at most 40 characters from the name
// plus a suffix; duplicates of duplicates are cut to stay within it.
const maxID = 64

// idPattern is what a crew ID looks like: a derived ID always matches it, and
// a hand-edited file is held to it. With ".json" it is a store document name.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidID reports whether id has the shape of a crew ID (idPattern), for code
// that handles IDs the store did not make: conductor crews --ids prints only
// these, since a shell's completion reads what it prints.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Errors a Store returns besides validation and save errors.
var (
	ErrNotFound = errors.New("no such crew")
	// ErrUnreadable is a crew whose file cannot be used: not JSON, a field a
	// crew does not have, an invalid crew, another crew's ID, more than
	// MaxEncoded bytes, a symbolic link. The error that wraps it says why and
	// names the file, not the directory. Such a file is left alone: never
	// listed, never overwritten, and deleted only on request.
	ErrUnreadable = errors.New("the crew's file cannot be used")
	// ErrWrite wraps a save or a delete that failed: the file is as it was.
	// A failure to read the crews directory or a file does not wrap it, so
	// the API can say which of the two failed.
	ErrWrite = errors.New("the crew's file could not be written")
)

// Summary is a crew as GET /api/crews lists it: what the Crews page's list
// and conductor crews show, without the prompts.
type Summary struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Goal      string          `json:"goal"`
	Cwd       string          `json:"cwd"`
	Where     string          `json:"where"`
	Isolation string          `json:"isolation"`
	Members   []MemberSummary `json:"members"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// MemberSummary is a member as a Summary lists it: its name, agent and
// start rule (the Crews page draws the crew's shape from the rules).
type MemberSummary struct {
	Name    string `json:"name"`
	AgentID string `json:"agentId"`
	Start   Start  `json:"start"`
}

func (c Crew) summary() Summary {
	members := make([]MemberSummary, 0, len(c.Members))
	for _, m := range c.Members {
		members = append(members, MemberSummary{Name: m.Name, AgentID: m.AgentID, Start: m.Start})
	}
	return Summary{ID: c.ID, Name: c.Name, Goal: c.Goal, Cwd: c.Cwd, Where: c.Where, Isolation: c.Isolation, Members: members, UpdatedAt: c.UpdatedAt}
}

// Store keeps the saved crews, one file each in crews/ of the data directory,
// each written whole by the store. The list is read from the directory, so a
// file added, changed or removed by hand shows at the next listing. Every
// change is checked in the form it is saved in (name trimmed, ID and times
// set), and a failed save changes nothing. Crews go in and come out as copies.
type Store struct {
	st  *store.Store // crews/
	now func() time.Time

	// mu serialises the writers, so that a derived ID is chosen and taken in
	// one step, and the readers with them, so that a read never meets a save
	// of this server halfway; it guards sums.
	mu sync.Mutex
	// sums caches each file's summary by document name, with the size and
	// time the file had when it was read: one whose size or time changed is
	// read again. commit and Delete drop the entry of the file they change,
	// so a save that keeps the size and the time (a rename to a name of the
	// same length within the clock's tick) is listed at once; a hand edit
	// that keeps both shows once either changes. Only what the file holds is
	// cached: a crew, or why it cannot be used. A failure to read the file
	// (its mode, say) may pass: it is not cached, so the file is left out of
	// that listing and read again at the next.
	sums map[string]cached
}

type cached struct {
	size int64
	mod  time.Time
	sum  Summary
	err  error // why the file is left out of the list
}

// loadLimit is store.Store.LoadLimit, which a test replaces.
var loadLimit = (*store.Store).LoadLimit

// NewStore opens the crews of the data directory st: crews/ in it, made 0700
// when missing, after the crews.json of an older Conductor is moved there
// (migrate). It reads every crew file once. problems lists, each naming its
// files, what was left as it was: a crew of crews.json that was not moved
// because its file exists and holds something else (crews.json.migrated
// keeps it), and a crew file that cannot be used or read (left out; the
// other crews load). err is fatal: a crews.json that cannot be moved, or a
// crews directory that cannot be made or read.
func NewStore(st *store.Store) (s *Store, problems []error, err error) {
	dir, err := st.Sub(crewsDir)
	if err != nil {
		return nil, nil, err
	}
	notices, err := migrate(st, dir)
	if err != nil {
		return nil, nil, err
	}
	s = &Store{st: dir, now: func() time.Time { return time.Now().UTC() }, sums: map[string]cached{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.st.List()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", dir.Dir(), err)
	}
	problems = notices
	for _, e := range entries {
		if c := s.summaryOf(e); c.err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", filepath.Join(dir.Dir(), e.Name), c.err))
		}
	}
	return s, problems, nil
}

// summaryOf returns the summary of the file e, cached and read again when its
// size or time changed, or in err why the file is left out of the list. Only
// what the file holds is cached (a crew, ErrUnreadable, ErrNotFound): a
// failure to read the file (its mode, say) may pass, so the file is left out
// of this listing only and read again at the next. The caller holds s.mu.
func (s *Store) summaryOf(e store.Entry) cached {
	if c, ok := s.sums[e.Name]; ok && c.size == e.Size && c.mod.Equal(e.ModTime) {
		return c
	}
	c := cached{size: e.Size, mod: e.ModTime}
	crew, err := s.read(strings.TrimSuffix(e.Name, ".json"))
	switch {
	case err == nil:
		c.sum = crew.summary()
	case errors.Is(err, ErrUnreadable), errors.Is(err, ErrNotFound):
		c.err = err
	default:
		delete(s.sums, e.Name)
		c.err = err
		return c
	}
	s.sums[e.Name] = c
	return c
}

// read loads the crew with the given ID from its file and checks it as the
// store saves it: a regular file (not a link), strict JSON of at most
// MaxEncoded bytes, a valid crew, the ID its file is named for. ErrNotFound
// when there is no file, an error wrapping ErrUnreadable when it cannot be
// used. A failure of the file system itself (a *fs.PathError, which names
// the data directory) is neither: it is a read failure. A file another was
// renamed over as it was read (store.ErrChanged) is read again, once, and
// counts as unusable only when that happens again. The caller holds s.mu, so
// a save of this server never meets the read; a hand-made one may.
func (s *Store) read(id string) (Crew, error) {
	if !idPattern.MatchString(id) {
		return Crew{}, ErrNotFound
	}
	var c Crew
	ok, err := loadLimit(s.st, id+".json", &c, MaxEncoded)
	if errors.Is(err, store.ErrChanged) {
		c = Crew{}
		ok, err = loadLimit(s.st, id+".json", &c, MaxEncoded)
	}
	var pe *fs.PathError
	switch {
	case errors.As(err, &pe):
		return Crew{}, fmt.Errorf("read %s.json: %w", id, err)
	case err != nil:
		return Crew{}, fmt.Errorf("%w: %w", ErrUnreadable, err)
	case !ok:
		return Crew{}, ErrNotFound
	}
	c = c.canonical()
	if c.ID != id {
		return Crew{}, fmt.Errorf("%w: it holds the id %s", ErrUnreadable, quote(c.ID))
	}
	if err := c.validateWithID(); err != nil {
		return Crew{}, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	return c, nil
}

// List returns the summaries of the crews ordered by name, ignoring case,
// then by ID: those from offset on, at most limit of them, and how many there
// are in all. The directory is read each time. A file that cannot be used is
// left out and counts in no total, and so is one that cannot be read for now,
// in this listing only.
func (s *Store) List(offset, limit int) ([]Summary, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.st.List()
	if err != nil {
		return nil, 0, err
	}
	seen := make(map[string]bool, len(entries))
	all := make([]Summary, 0, len(entries))
	for _, e := range entries {
		seen[e.Name] = true
		if c := s.summaryOf(e); c.err == nil {
			all = append(all, c.sum)
		}
	}
	for name := range s.sums {
		if !seen[name] {
			delete(s.sums, name)
		}
	}
	slices.SortFunc(all, func(a, b Summary) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.ID, b.ID))
	})
	total := len(all)
	from := min(max(offset, 0), total)
	to := min(from+max(limit, 0), total)
	page := slices.Clone(all[from:to])
	for i := range page {
		page[i].Members = slices.Clone(page[i].Members)
	}
	return page, total, nil
}

// Get returns the crew with the given ID, read from its file: ErrNotFound when
// there is none, an error wrapping ErrUnreadable when it cannot be used, a
// symbolic link included (store.LoadLimit does not follow one).
func (s *Store) Get(id string) (Crew, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(id)
}

// Put validates c and saves it under c.ID, adding it or replacing the crew
// with that ID. It stores c as it is, times included, only its name trimmed;
// Create, Update and Duplicate are the operations that set the ID and the
// times.
func (s *Store) Put(c Crew) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit(c)
}

// Create saves c as a new crew. Its ID comes from its name, lower case with
// every other run of characters a dash and at most 40 of them ("crew" when
// nothing is left), then -2, -3 and so on when a file has it already, usable
// or not. CreatedAt and UpdatedAt are now, and ManifestRevision 1 when the
// crew has a project manifest (a legacy setup counts), else 0. The ID, times
// and revision c holds are ignored.
func (s *Store) Create(c Crew) (Crew, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	taken, err := s.taken()
	if err != nil {
		return Crew{}, err
	}
	// The workbench edits a crew not saved yet at /crews/new: no crew takes
	// that id; nor Brakeman's, a turn's crew (ClassBrakeman), so a crew a
	// person names Brakeman is saved as brakeman-2. One saved under it
	// before keeps it.
	taken[reservedID] = true
	taken[ClassBrakeman] = true
	c.ID = freeID(taken, cmp.Or(slug(c.Name), "crew"), "")
	c.CreatedAt = s.now()
	c.UpdatedAt = c.CreatedAt
	c.ManifestRevision = manifestRevision(nil, c)
	if err := s.commit(c); err != nil {
		return Crew{}, err
	}
	return c.canonical(), nil
}

// Update replaces the crew with the given ID by c. The ID and CreatedAt stay
// as they were, whatever c holds, and UpdatedAt is now. ManifestRevision is
// the stored crew's, one more when the project manifest changed (a removal
// included), whatever c holds (manifestRevision). A file that cannot be used
// is not replaced (ErrUnreadable).
func (s *Store) Update(id string, c Crew) (Crew, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.read(id)
	if err != nil {
		return Crew{}, err
	}
	c.ID, c.CreatedAt, c.UpdatedAt = id, old.CreatedAt, s.now()
	c.ManifestRevision = manifestRevision(&old, c)
	if err := s.commit(c); err != nil {
		return Crew{}, err
	}
	return c.canonical(), nil
}

// Duplicate saves a copy of the crew with the given ID as a new crew: its ID
// is <id>-copy (then <id>-copy-2 and so on), its name "<name> copy", its
// CreatedAt and UpdatedAt now, and its ManifestRevision a new crew's.
func (s *Store) Duplicate(id string) (Crew, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, err := s.read(id)
	if err != nil {
		return Crew{}, err
	}
	taken, err := s.taken()
	if err != nil {
		return Crew{}, err
	}
	c := src.clone()
	c.ID = freeID(taken, id, "-copy")
	c.Name = copyName(src.Name)
	c.CreatedAt = s.now()
	c.UpdatedAt = c.CreatedAt
	c.ManifestRevision = manifestRevision(nil, c)
	if err := s.commit(c); err != nil {
		return Crew{}, err
	}
	return c.canonical(), nil
}

// Delete removes the crew with the given ID, its file whatever it holds, and
// reports whether there was one.
func (s *Store) Delete(id string) (bool, error) {
	if !idPattern.MatchString(id) {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Lstat, so that a link is there to delete whatever it names.
	_, err := os.Lstat(filepath.Join(s.st.Dir(), id+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.st.Delete(id + ".json"); err != nil {
		return false, fmt.Errorf("%w: delete %s.json: %w", ErrWrite, id, err)
	}
	delete(s.sums, id+".json")
	return true, nil
}

// Seed saves the crews of list whose ids have no file in the crews
// directory, each with a new crew's ManifestRevision, and returns the ids it
// added and the ids it skipped, each in list's order. A file with the id,
// usable or not (taken), is left alone whatever it holds: seeding twice
// changes nothing, a crew edited after a seed stays as the editor left
// it, and a file the store cannot read is never overwritten. The first
// crew that cannot be saved stops the seed, naming it; the ones saved
// before it stay.
func (s *Store) Seed(list []Crew) (added, skipped []string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	taken, err := s.taken()
	if err != nil {
		return nil, nil, err
	}
	added, skipped = []string{}, []string{}
	for _, c := range list {
		if taken[c.ID] {
			skipped = append(skipped, c.ID)
			continue
		}
		c.ManifestRevision = manifestRevision(nil, c)
		if err := s.commit(c); err != nil {
			return added, skipped, fmt.Errorf("seed %s: %w", c.ID, err)
		}
		taken[c.ID] = true
		added = append(added, c.ID)
	}
	return added, skipped, nil
}

// commit checks c as it will be saved, then writes its file and drops the
// summary the list cached for it. An invalid crew is refused, and a failed
// write (ErrWrite) leaves the file as it was. The caller holds s.mu.
func (s *Store) commit(c Crew) error {
	c = c.canonical()
	if err := c.validateWithID(); err != nil {
		return err
	}
	if err := c.ValidateSave(); err != nil {
		return err
	}
	if err := s.st.Save(c.ID+".json", c); err != nil {
		return fmt.Errorf("%w: save %s.json: %w", ErrWrite, c.ID, err)
	}
	delete(s.sums, c.ID+".json")
	return nil
}

// taken returns the IDs that have an entry in the crews directory, usable or
// not, a link included, so that no new crew takes the name of a file the
// store cannot read. It reads the directory itself, since store.List leaves
// links out. The caller holds s.mu.
func (s *Store) taken() (map[string]bool, error) {
	des, err := os.ReadDir(s.st.Dir())
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(des))
	for _, de := range des {
		if id, ok := strings.CutSuffix(de.Name(), ".json"); ok {
			out[id] = true
		}
	}
	return out, nil
}

// freeID returns stem+tag, or stem+tag+"-2", stem+tag+"-3" and so on, the
// first that taken does not hold, with stem cut so that the ID stays within
// maxID. stem starts with a letter or digit, as slugs and IDs do.
func freeID(taken map[string]bool, stem, tag string) string {
	for n := 1; ; n++ {
		end := tag
		if n > 1 {
			end += "-" + strconv.Itoa(n)
		}
		id := strings.TrimRight(stem[:min(len(stem), maxID-len(end))], "-") + end
		if !taken[id] {
			return id
		}
	}
}

// validateWithID is Validate plus the ID, which the store keys crews by.
func (c Crew) validateWithID() error {
	if !idPattern.MatchString(c.ID) {
		return invalidf("id %s must match %s", quote(c.ID), idPattern)
	}
	return c.Validate()
}

// canonical returns the form of c the store checks and keeps: a copy sharing
// no slice with c, its name without surrounding space, its members listed
// as [] rather than null when there are none, and a project manifest with
// nothing in it left out, as a crew without one.
func (c Crew) canonical() Crew {
	c = c.clone()
	c.Name = strings.TrimSpace(c.Name)
	if c.Members == nil {
		c.Members = []Member{}
	}
	if c.Manifest != nil && c.Manifest.empty() {
		c.Manifest = nil
	}
	return c
}

// maxSlug bounds the part of an ID that comes from the name.
const maxSlug = 40

// reservedID is the id no derived ID takes: the workbench's page for a crew
// not saved yet (/crews/new).
const reservedID = "new"

// slug derives an ID from a crew name by the rule the web's slugId follows:
// lower case, every run of characters other than a-z and 0-9 one dash, no
// dash at either end, at most 40 characters. It is empty when nothing usable
// is left.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	out := b.String()
	return strings.TrimRight(out[:min(len(out), maxSlug)], "-")
}

// copyName is the name of a duplicate: "<name> copy", with name cut so that
// it stays within maxName characters.
func copyName(name string) string {
	const suffix = " copy" // ASCII: as many characters as bytes
	r := []rune(name)
	return strings.TrimSpace(string(r[:min(len(r), maxName-len(suffix))])) + suffix
}
