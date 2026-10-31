package ext

import (
	"context"
	"time"
)

// ContextProvider gives an attempt of a managed run context to read before
// it starts (what a Code Context Graph knows of its task), and is told the
// files the agents touched. The free default is no provider: an attempt's
// task file holds its task, its inputs and nothing more, and the touched
// index and git are the context there is.
type ContextProvider interface {
	Provider
	// NoteTouch is told one file an agent touched, from the sessions' file
	// activity. Called in order, from one goroutine of the provider's own.
	NoteTouch(ctx context.Context, f TouchedFile)
	// Context answers the blocks an attempt about to start should read.
	// Safe for concurrent use.
	Context(ctx context.Context, q ContextQuery) ([]ContextBlock, error)
}

// TouchedFile is one file an agent touched.
type TouchedFile struct {
	Session SessionRef `json:"session"`
	Path    string     `json:"path"` // absolute, as the session's touched index keys it
	Op      string     `json:"op"`   // read | edit | write | delete
	Tool    string     `json:"tool,omitempty"`
	By      string     `json:"by,omitempty"`
	At      time.Time  `json:"at"`
}

// ContextQuery is the attempt about to start.
type ContextQuery struct {
	Run          string         `json:"run"`
	Crew         string         `json:"crew"`
	Task         string         `json:"task"`
	Attempt      int            `json:"attempt"`
	Kind         string         `json:"kind"` // work | review
	Goal         string         `json:"goal"`
	Instructions string         `json:"instructions"`
	Worktree     string         `json:"worktree"` // <cwd>/.conductor/worktrees/<run>/<task>.<n>
	Dir          string         `json:"dir"`      // the working directory inside it (the crew's prefix)
	Start        string         `json:"start"`    // the commit it starts at; a review's, the candidate
	Inputs       []ContextInput `json:"inputs,omitempty"`
}

// ContextInput is one task's result an attempt starts from.
type ContextInput struct {
	Task   string `json:"task"`
	Commit string `json:"commit"`
}

// ContextBlock is one piece of context an attempt reads: reference
// material a provider derived, never the task's own words.
type ContextBlock struct {
	Title string `json:"title"`         // 1 to MaxContextTitle bytes, one line
	Ref   string `json:"ref,omitempty"` // a path or URL the block is about, at most MaxContextRef bytes
	Body  string `json:"body"`          // UTF-8 text, 1 to MaxContextBody bytes
}

const (
	MaxContextBlocks = 8 // a provider's, per attempt
	MaxContextTitle  = 120
	MaxContextRef    = 1024
	MaxContextBody   = 8 << 10
	MaxTaskContext   = 16 << 10 // every provider's blocks together, as a task file keeps them
	ContextWait      = 5 * time.Second
	TouchQueue       = 1024
)

// Valid checks b against its bounds: a title of one line of 1 to
// MaxContextTitle bytes, a ref of one line of at most MaxContextRef bytes,
// and a body of 1 to MaxContextBody bytes of UTF-8 text whose only control
// characters are tabs and line ends.
func (b ContextBlock) Valid() error {
	const r = "context block"
	if err := line(r, "title", b.Title, MaxContextTitle, true); err != nil {
		return err
	}
	if err := line(r, "ref", b.Ref, MaxContextRef, false); err != nil {
		return err
	}
	switch {
	case b.Body == "":
		return invalid(r, "body is empty")
	case len(b.Body) > MaxContextBody:
		return invalid(r, "body is %d bytes, past %d", len(b.Body), MaxContextBody)
	case !text(b.Body):
		return invalid(r, "body is not UTF-8 text")
	}
	return nil
}
