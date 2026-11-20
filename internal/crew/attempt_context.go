package crew

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The context each attempt was given (docs/plans/context-seams.md, CT-3):
// every block a context provider gave an attempt, at its start (phase
// task) or by a lookup (phase lookup), recorded before the agent has it.
// The ledger keeps a row per block (its source, title, ref, pack mark,
// body's SHA-256 and size: attempt_context), never its text; the text is
// in the data directory's context file, runs/context/<run>/<task>.<n>.json,
// which conductor export carries and nothing else hands on. Every write of
// an attempt's context runs, from its read of the file to its commit,
// under the attempt's own lock: read the file; keep the entries whose seq
// the ledger holds (a body with no row, left by a crash between the
// rename and the commit, is dropped); add the new blocks at the next
// seqs; write, sync and rename; commit the rows. So a row never names a
// body the file lacks, and a body with no row is never shown.

// contextFileVersion is the context file's version.
const contextFileVersion = 1

// maxContextFile bounds a context file as encoded: every body at its
// largest (ext.MaxTaskContext of the task's blocks, and
// ledger.MaxContextLookupRows of ext.MaxContextBody) with every byte
// escaped, and every other field of every entry
// (ledger.MaxContextTaskRows + ledger.MaxContextLookupRows of them) at its
// largest, escaped the same way. A block's text holds no control
// character but a tab or a line end, and a title, a ref or a mark none:
// JSON escapes each of their bytes in two at most (a quote, a backslash,
// a tab, a line end; U+2028 and U+2029, three bytes each, in six).
const maxContextFile = 2*(ext.MaxTaskContext+ledger.MaxContextLookupRows*ext.MaxContextBody) +
	(ledger.MaxContextTaskRows+ledger.MaxContextLookupRows)*(2*(ext.MaxContextTitle+ext.MaxContextRef+ext.MaxContextPack+ext.MaxProviderName)+128)

// contextEntry is one block of a context file.
type contextEntry struct {
	Seq    int    `json:"seq"`
	Phase  string `json:"phase"`
	Source string `json:"source"`
	Title  string `json:"title"`
	Ref    string `json:"ref,omitempty"`
	Pack   string `json:"pack,omitempty"`
	Body   string `json:"body"`
}

// contextFile is a context file as it is written.
type contextFile struct {
	V      int            `json:"v"`
	Blocks []contextEntry `json:"blocks"`
}

// ContextFiles is the data directory's context files: its runs folder
// opened as a root, every file read and written through it.
type ContextFiles struct{ root *os.Root }

// OpenContextFiles opens the runs folder runsDir (the ledger's) for the
// context files.
func OpenContextFiles(runsDir string) (*ContextFiles, error) {
	root, err := os.OpenRoot(runsDir)
	if err != nil {
		return nil, err
	}
	return &ContextFiles{root: root}, nil
}

// Close closes the root.
func (c *ContextFiles) Close() error { return c.root.Close() }

// contextPath is the folder and the file of an attempt's context, refused
// for a run or a task id not of its shape.
func contextPath(runID, taskID string, n int) (dir, file string, err error) {
	if !ValidRunID(runID) || !ValidTaskID(taskID) || n < 1 {
		return "", "", fmt.Errorf("context: %q/%q/%d names no attempt", runID, taskID, n)
	}
	dir = path.Join("context", runID)
	return dir, path.Join(dir, taskID+"."+strconv.Itoa(n)+".json"), nil
}

// read reads an attempt's context file, an empty one when there is none.
func (c *ContextFiles) read(runID, taskID string, n int) (contextFile, error) {
	_, name, err := contextPath(runID, taskID, n)
	if err != nil {
		return contextFile{}, err
	}
	f, err := c.root.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return contextFile{V: contextFileVersion}, nil
	}
	if err != nil {
		return contextFile{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxContextFile+1))
	if err != nil {
		return contextFile{}, err
	}
	if len(b) > maxContextFile {
		return contextFile{}, fmt.Errorf("context: %s is past %d bytes", name, maxContextFile)
	}
	var out contextFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return contextFile{}, fmt.Errorf("context: %s: %w", name, err)
	}
	if out.V != contextFileVersion {
		return contextFile{}, fmt.Errorf("context: %s is version %d", name, out.V)
	}
	return out, nil
}

// write replaces an attempt's context file with f: written to its .tmp,
// synced, renamed over the file, the folder synced.
func (c *ContextFiles) write(runID, taskID string, n int, f contextFile) error {
	dir, name, err := contextPath(runID, taskID, n)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return err
	}
	if b.Len() > maxContextFile {
		return fmt.Errorf("context: %s would be %d bytes, past %d", name, b.Len(), maxContextFile)
	}
	if err := c.root.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := name + ".tmp"
	w, err := c.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := w.Write(b.Bytes()); err != nil {
		w.Close()
		return err
	}
	if err := w.Sync(); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := c.root.Rename(tmp, name); err != nil {
		return err
	}
	d, err := c.root.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Body answers the text of the block seq of an attempt, when the file
// holds it and its SHA-256 is sum (the row's); ok false for a body the
// file lacks or whose digest is not the row's.
func (c *ContextFiles) Body(runID, taskID string, n, seq int, sum string) (body string, ok bool, err error) {
	f, err := c.read(runID, taskID, n)
	if err != nil {
		return "", false, err
	}
	for _, e := range f.Blocks {
		if e.Seq == seq {
			return e.Body, digest(e.Body) == sum, nil
		}
	}
	return "", false, nil
}

// digest is a body's SHA-256 in lower-case hex.
func digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// UseContextFiles makes c where the engine records the context each
// attempt is given; until it is set, nothing is recorded (an engine
// without a data directory).
func (e *Engine) UseContextFiles(c *ContextFiles) {
	e.mu.Lock()
	e.contextFiles = c
	e.mu.Unlock()
}

// recordsContext reports whether the engine records the context each
// attempt is given.
func (e *Engine) recordsContext() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.contextFiles != nil
}

// ErrContextNotRecorded is a block of context that could not be recorded
// against its attempt, and so is not given.
var ErrContextNotRecorded = errors.New("context: it could not be recorded")

// recordContext writes the blocks an attempt of r is given in phase into
// its context file, after the entries the ledger holds, and answers the
// rows its caller commits; the caller holds a.ctxMu from before this call
// until that commit. It reads the ledger and the file, and writes the file
// alone: no state of the engine changes.
func (e *Engine) recordContext(ctx context.Context, r *run, a *attempt, phase string, blocks []TaskContext) ([]ledger.AttemptContextRow, error) {
	e.mu.Lock()
	files, seam := e.contextFiles, e.contextRead
	runID, taskID, n := r.id, a.t.def.ID, a.n
	e.mu.Unlock()
	if files == nil {
		return nil, fmt.Errorf("%w: this server keeps no context files", ErrContextNotRecorded)
	}
	held, err := e.ledger.AttemptContext(ctx, runID, taskID)
	if err != nil {
		return nil, err
	}
	rows := held[n]
	f, err := files.read(runID, taskID, n)
	if err != nil {
		return nil, err
	}
	// Only what a committed row names: a body with no row is a crash's,
	// between a rename and its commit.
	byRow := map[int]bool{}
	last := 0
	for _, row := range rows {
		byRow[row.Seq] = true
		last = max(last, row.Seq)
	}
	f.V = contextFileVersion
	f.Blocks = slices.DeleteFunc(f.Blocks, func(e contextEntry) bool { return !byRow[e.Seq] })
	if seam != nil {
		seam(phase)
	}
	at := time.Now().UTC()
	var add []ledger.AttemptContextRow
	for i, b := range blocks {
		seq := last + 1 + i
		f.Blocks = append(f.Blocks, contextEntry{Seq: seq, Phase: phase, Source: b.Source, Title: b.Title, Ref: b.Ref, Pack: b.Pack, Body: b.Body})
		add = append(add, ledger.AttemptContextRow{RunID: runID, TaskID: taskID, N: n, Seq: seq, Phase: phase, Source: b.Source, Title: b.Title,
			Ref: b.Ref, Pack: b.Pack, SHA256: digest(b.Body), Bytes: len(b.Body), At: at})
	}
	if err := files.write(runID, taskID, n, f); err != nil {
		return nil, err
	}
	return add, nil
}

// ContextEntry is one block of context an attempt was given, as a task's
// read shows it: its seq, its phase (task, at its start; lookup, while it
// worked), the provider, its title, what it is about, the provider's pack
// mark, its body's SHA-256 and size, and when it was recorded; never its
// text, which the body route reads.
type ContextEntry struct {
	Seq    int       `json:"seq"`
	Phase  string    `json:"phase"`
	Source string    `json:"source"`
	Title  string    `json:"title"`
	Ref    string    `json:"ref,omitempty"`
	Pack   string    `json:"pack,omitempty"`
	SHA256 string    `json:"sha256"`
	Bytes  int       `json:"bytes"`
	At     time.Time `json:"at"`
}

// ContextEntries is the rows as a task's read shows them.
func ContextEntries(rows []ledger.AttemptContextRow) []ContextEntry {
	if len(rows) == 0 {
		return nil
	}
	out := make([]ContextEntry, len(rows))
	for i, r := range rows {
		out[i] = ContextEntry{Seq: r.Seq, Phase: r.Phase, Source: r.Source, Title: r.Title, Ref: r.Ref, Pack: r.Pack, SHA256: r.SHA256, Bytes: r.Bytes, At: r.At}
	}
	return out
}

// maxLookupContext bounds the blocks lookups may give one attempt.
const maxLookupContext = ledger.MaxContextLookupRows

// RecordLookupContext records the blocks a lookup's answer gives the
// attempt n of the run's task, from the provider source, before they are
// handed to its agent: the file written and the rows committed in a
// transaction of their own, under the attempt's lock. It answers
// ErrContextNotRecorded when they could not be (no context files, the
// attempt closed, its lookup rows full, the write or the commit failed),
// and then nothing of them is given.
func (e *Engine) RecordLookupContext(ctx context.Context, runID, taskID string, n int, source string, blocks []ext.ContextBlock) error {
	if len(blocks) == 0 {
		return nil
	}
	e.mu.Lock()
	r, ok := e.runs[runID]
	var a *attempt
	if ok && r.managed {
		if t := r.task(taskID); t != nil {
			for _, o := range t.attempts {
				if o.n == n {
					a = o
				}
			}
		}
	}
	e.mu.Unlock()
	if a == nil {
		return fmt.Errorf("%w: no such attempt", ErrContextNotRecorded)
	}
	a.ctxMu.Lock()
	defer a.ctxMu.Unlock()
	e.mu.Lock()
	closed := !a.closedAt.IsZero()
	e.mu.Unlock()
	if closed {
		return fmt.Errorf("%w: the attempt has closed", ErrContextNotRecorded)
	}
	held, err := e.ledger.AttemptContext(ctx, runID, taskID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrContextNotRecorded, err)
	}
	lookups := 0
	for _, row := range held[n] {
		if row.Phase == ledger.PhaseLookup {
			lookups++
		}
	}
	if lookups+len(blocks) > maxLookupContext || len(blocks) > ledger.MaxContextLookupOne {
		return fmt.Errorf("%w: the attempt's lookups have given %d blocks, %d at most", ErrContextNotRecorded, lookups, maxLookupContext)
	}
	tcs := make([]TaskContext, len(blocks))
	for i, b := range blocks {
		tcs[i] = TaskContext{Source: source, Title: b.Title, Ref: b.Ref, Body: b.Body, Pack: b.Pack}
	}
	rows, err := e.recordContext(ctx, r, a, ledger.PhaseLookup, tcs)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrContextNotRecorded, err)
	}
	e.mu.Lock()
	commit := e.contextCommit
	e.mu.Unlock()
	if commit != nil {
		if err := commit(); err != nil {
			return fmt.Errorf("%w: %v", ErrContextNotRecorded, err)
		}
	}
	if err := e.transact(ctx, r, func() tx {
		return tx{more: func(ltx LedgerTx) error { return ltx.AddAttemptContext(rows) }}
	}, giveUp); err != nil {
		return fmt.Errorf("%w: %v", ErrContextNotRecorded, err)
	}
	return nil
}
