package ext

import (
	"context"
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
