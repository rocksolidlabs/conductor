package ext

import (
	"context"
	"encoding/json"
	"time"
)

// A lookup (docs/plans/context-seams.md, CT-2): a question an attempt's
// agent asks a context provider while it works, through the server's
// tool context_lookup, listed only when a registered provider answers
// lookups. The agent names only the op and its words; which run, task,
// attempt, session and commits the question is about are the server's,
// from the asking session's own attempt and its worktree. A provider's
// answer is reference material: items each marked by how sure the
// provider is of them, and, for the op pack, blocks an agent is handed
// only once they are recorded against its attempt (CT-3).

// ContextLookup is an optional interface of a context provider: an
// attempt's agent asks it while it works. Safe for concurrent use. The
// server asks it within LookupWait through the guard, under a kind of
// its own, so a slow lookup never holds an attempt's start.
type ContextLookup interface {
	Lookup(ctx context.Context, r LookupRequest) (LookupAnswer, error)
}

// LookupRequest is one lookup: the agent's op and words, then what the
// server fills in from the asking session's attempt, never the agent.
type LookupRequest struct {
	Op     string `json:"op"`               // symbol | references | dependencies | impact | pack
	Query  string `json:"query,omitempty"`  // at most MaxLookupQuery; for pack, a pack mark or empty for the attempt's own
	Path   string `json:"path,omitempty"`   // at most MaxLookupPath, relative to the repository's top
	Symbol string `json:"symbol,omitempty"` // at most MaxLookupSymbol
	Limit  int    `json:"limit"`            // 1 to MaxLookupLimit
	// The server's, never the agent's:
	Run     string `json:"run"`
	Task    string `json:"task"`
	Attempt int    `json:"attempt"`
	Kind    string `json:"kind"` // work | review
	Session string `json:"session"`
	Start   string `json:"start"`          // the attempt's start
	Base    string `json:"base"`           // the run's base
	Head    string `json:"head,omitempty"` // the worktree's HEAD, "" when it could not be read
}

// LookupAnswer is a provider's answer to a lookup: its items, a late
// pack's blocks (op pack), what the answer was made from, whether there
// is more, and the provider's note.
type LookupAnswer struct {
	Items    []LookupItem   `json:"items,omitempty"`  // at most MaxLookupItems
	Blocks   []ContextBlock `json:"blocks,omitempty"` // op pack: at most MaxLookupBlocks, each with its Pack
	Coverage LookupCoverage `json:"coverage"`
	More     bool           `json:"more,omitempty"`
	Note     string         `json:"note,omitempty"` // at most MaxLookupNote, one line
}

// LookupItem is one thing a lookup found: a symbol, a file, a test or a
// package, where it is, how it relates to what was asked, how sure the
// provider is of that (Mark), why, and an excerpt.
type LookupItem struct {
	Kind     string `json:"kind"` // symbol | file | test | package
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Symbol   string `json:"symbol,omitempty"`
	Relation string `json:"relation,omitempty"` // calls | called_by | imports | imported_by | tests | changes
	Mark     string `json:"mark"`               // supported | inferred | unknown
	Why      string `json:"why,omitempty"`      // at most MaxLookupWhy, one line
	Excerpt  string `json:"excerpt,omitempty"`  // at most MaxLookupExcerpt
}

// LookupCoverage is what an answer was made from: the commit, the
// provider's own name for what it read, how many files it covered, left
// out and could not tell about, whether it is partial, and when what it
// answered from was read.
type LookupCoverage struct {
	Commit   string    `json:"commit"`             // full id
	Snapshot string    `json:"snapshot,omitempty"` // at most MaxContextPack, printable ASCII on one line
	Files    int       `json:"files"`
	Excluded int       `json:"excluded,omitempty"`
	Unknown  int       `json:"unknown,omitempty"`
	Partial  bool      `json:"partial,omitempty"`
	At       time.Time `json:"at,omitzero"`
}

const (
	MaxLookupQuery   = 200
	MaxLookupPath    = 1024
	MaxLookupSymbol  = 512
	MaxLookupLimit   = 200
	MaxLookupItems   = 200
	MaxLookupWhy     = 200
	MaxLookupExcerpt = 2 << 10
	MaxLookupNote    = 300
	MaxLookupBlocks  = 8
	MaxLookupAnswer  = 64 << 10 // the answer as encoded
	LookupWait       = 3 * time.Second
)

// The ops of a lookup.
const (
	LookupSymbol       = "symbol"
	LookupReferences   = "references"
	LookupDependencies = "dependencies"
	LookupImpact       = "impact"
	LookupPack         = "pack"
)

// The kinds of a lookup's item.
const (
	ItemSymbol  = "symbol"
	ItemFile    = "file"
	ItemTest    = "test"
	ItemPackage = "package"
)

// The relations of a lookup's item to what was asked.
const (
	RelationCalls      = "calls"
	RelationCalledBy   = "called_by"
	RelationImports    = "imports"
	RelationImportedBy = "imported_by"
	RelationTests      = "tests"
	RelationChanges    = "changes"
)

// The marks of a lookup's item: how sure the provider is of its
// relation. The server passes each through and adds none.
const (
	MarkSupported = "supported" // it read the relation in the code
	MarkInferred  = "inferred"  // it derived it
	MarkUnknown   = "unknown"   // it could not tell
)

// Valid checks r against its bounds: an op of the five; a query of one
// line of at most MaxLookupQuery bytes (for pack, a pack mark or none); a
// path inside the tree (TreePathOK) of at most MaxLookupPath bytes, or
// none; a symbol of one line of at most MaxLookupSymbol bytes; a limit
// from 1 to MaxLookupLimit; and the server's fields: a run id, a task id,
// an attempt from 1, a kind of work or review, a session id, full commit
// ids for the start and the base, and for the head a full id or none.
func (r LookupRequest) Valid() error {
	const rec = "lookup request"
	switch r.Op {
	case LookupSymbol, LookupReferences, LookupDependencies, LookupImpact, LookupPack:
	default:
		return invalid(rec, "op %q is not one of symbol, references, dependencies, impact and pack", r.Op)
	}
	if err := line(rec, "query", r.Query, MaxLookupQuery, false); err != nil {
		return err
	}
	if r.Op == LookupPack && r.Query != "" && !packMark(r.Query) {
		return invalid(rec, "a pack's query is a pack mark or none")
	}
	if r.Path != "" && (len(r.Path) > MaxLookupPath || !oneLine(r.Path) || !TreePathOK(r.Path)) {
		return invalid(rec, "path is not a path inside the tree of at most %d bytes", MaxLookupPath)
	}
	if err := line(rec, "symbol", r.Symbol, MaxLookupSymbol, false); err != nil {
		return err
	}
	switch {
	case r.Limit < 1 || r.Limit > MaxLookupLimit:
		return invalid(rec, "limit is not from 1 to %d", MaxLookupLimit)
	case !runID(r.Run):
		return invalid(rec, "run %q is not a run id", r.Run)
	case !taskID(r.Task):
		return invalid(rec, "task %q is not a task id", r.Task)
	case r.Attempt < 1:
		return invalid(rec, "attempt is below 1")
	case r.Kind != "work" && r.Kind != "review":
		return invalid(rec, "kind %q is not work or review", r.Kind)
	case r.Session == "" || len(r.Session) > MaxSessionRef || !printableASCII(r.Session):
		return invalid(rec, "session %q is not a session id", r.Session)
	case !commitID(r.Start):
		return invalid(rec, "start %q is not a full commit id", r.Start)
	case !commitID(r.Base):
		return invalid(rec, "base %q is not a full commit id", r.Base)
	case r.Head != "" && !commitID(r.Head):
		return invalid(rec, "head %q is not a full commit id", r.Head)
	}
	return nil
}

// Valid checks a against its bounds: at most MaxLookupItems items, each
// of a known kind, relation and mark, its path, symbol and why each one
// line of UTF-8 text with no control character within its bound, its
// line not below 0 and its excerpt UTF-8 text whose only control
// characters are tabs and line ends, of at most MaxLookupExcerpt bytes;
// at most MaxLookupBlocks blocks, each valid and carrying its pack mark;
// the coverage's commit a full id, its snapshot printable ASCII on one
// line as a pack mark is, or none, its counts not below 0; the note one
// line of at most MaxLookupNote bytes; and the answer as encoded at most
// MaxLookupAnswer bytes. Its words never quote what the answer holds:
// the server logs a refusal of a provider's answer.
func (a LookupAnswer) Valid() error {
	const rec = "lookup answer"
	switch {
	case len(a.Items) > MaxLookupItems:
		return invalid(rec, "%d items, past %d", len(a.Items), MaxLookupItems)
	case len(a.Blocks) > MaxLookupBlocks:
		return invalid(rec, "%d blocks, past %d", len(a.Blocks), MaxLookupBlocks)
	}
	for i, it := range a.Items {
		if err := it.valid(); err != nil {
			return invalid(rec, "item %d: %v", i, err)
		}
	}
	for i, b := range a.Blocks {
		if err := b.Valid(); err != nil {
			return invalid(rec, "block %d: %v", i, err)
		}
		if b.Pack == "" {
			return invalid(rec, "block %d carries no pack mark", i)
		}
	}
	c := a.Coverage
	switch {
	case !commitID(c.Commit):
		return invalid(rec, "coverage's commit is not a full commit id")
	case c.Snapshot != "" && !packMark(c.Snapshot):
		return invalid(rec, "coverage's snapshot is not 1 to %d bytes of printable ASCII with no space at either end", MaxContextPack)
	case c.Files < 0 || c.Excluded < 0 || c.Unknown < 0:
		return invalid(rec, "coverage counts below 0")
	}
	if err := line(rec, "note", a.Note, MaxLookupNote, false); err != nil {
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return invalid(rec, "%v", err)
	}
	if len(b) > MaxLookupAnswer {
		return invalid(rec, "%d bytes as encoded, past %d", len(b), MaxLookupAnswer)
	}
	return nil
}

func (it LookupItem) valid() error {
	const rec = "lookup item"
	switch it.Kind {
	case ItemSymbol, ItemFile, ItemTest, ItemPackage:
	default:
		return invalid(rec, "kind is not symbol, file, test or package")
	}
	switch it.Relation {
	case "", RelationCalls, RelationCalledBy, RelationImports, RelationImportedBy, RelationTests, RelationChanges:
	default:
		return invalid(rec, "relation is not one of the six")
	}
	switch it.Mark {
	case MarkSupported, MarkInferred, MarkUnknown:
	default:
		return invalid(rec, "mark is not supported, inferred or unknown")
	}
	if err := line(rec, "path", it.Path, MaxLookupPath, false); err != nil {
		return err
	}
	if err := line(rec, "symbol", it.Symbol, MaxLookupSymbol, false); err != nil {
		return err
	}
	if err := line(rec, "why", it.Why, MaxLookupWhy, false); err != nil {
		return err
	}
	switch {
	case it.Line < 0:
		return invalid(rec, "line is below 0")
	case len(it.Excerpt) > MaxLookupExcerpt:
		return invalid(rec, "excerpt is %d bytes, past %d", len(it.Excerpt), MaxLookupExcerpt)
	case !text(it.Excerpt):
		return invalid(rec, "excerpt is not UTF-8 text")
	}
	return nil
}
