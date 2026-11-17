package crew

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
)

// A person's change request carries evidence files by reference
// (docs/plans/browser-evidence.md, "After RV1 and DR1", PR-10, #798): a
// finding of kind capture stands on a capture's send, whose frozen document
// and screenshot Dispatch's store keeps, and the request names each file by
// its name, its SHA-256, its size and the send that holds it
// (EvidenceRef), never its bytes. The request lives in the task's repair
// JSON in the ledger, so a repair attempt queued, retried, or made in a
// continuation after a restart is handed the same files, read again from
// the store through the opener the API gives the engine (UseEvidenceFiles,
// Dispatch's OpenSendFile: a send's file only when that send holds it).
// Each is copied through the attempt's root into .conductor/evidence/, its
// size and hash checked as it is copied: one that cannot be read, or whose
// size or hash is not the request's, is removed and fails the attempt at
// "evidence", uncounted, and pauses the run, as a package's attachment
// does. A name is held to the one form a capture's file takes
// (capture-<id>-r<rev>.json or .png), so nothing a request names can reach
// outside that directory, and the root refuses a path that would.

// EvidenceRef is one evidence file of a change request: its name in
// .conductor/evidence/, its SHA-256 (hex), its size in bytes, and the
// capture send whose store rows hold it.
type EvidenceRef struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Send   string `json:"send"`
}

// RequestedCapture is what a requested change of kind capture names: the
// capture, the send of it whose frozen document the change stands on, and
// the annotation's place k in that document.
type RequestedCapture struct {
	Capture string `json:"capture"`
	Send    string `json:"send"`
	K       int    `json:"k"`
}

// Bounds of a request's evidence: at most maxEvidence files (their refs
// well under 2 KiB of the repair's JSON), maxEvidenceBytes in all, as
// Dispatch bounds a package's attachments.
const (
	maxEvidence      = 8
	maxEvidenceBytes = 16 << 20
	// maxCaptureK is the highest annotation a capture holds (Dispatch's
	// MaxCaptureAnnotations).
	maxCaptureK = 20
	// evidenceDir is where an attempt's evidence files are, under
	// .conductor.
	evidenceDir = "evidence"
	// evidenceStage is the stage an attempt fails at, uncounted, when its
	// evidence files cannot be handed to it.
	evidenceStage = "evidence"
)

// evidenceName is the one form an evidence file's name takes: a capture's
// document or screenshot as a send carries it. storeID is the form of a
// Dispatch id (16 hex digits), which this package does not import.
var (
	evidenceName = regexp.MustCompile(`^capture-[0-9a-f]{16}-r[1-9][0-9]{0,17}\.(json|png)$`)
	storeID      = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// evidenceError is an evidence file that cannot be handed to an attempt:
// one that cannot be read, whose size or hash is not the request's, or
// whose name is not of the form. It fails the attempt at evidenceStage,
// uncounted, and pauses the run; an error writing into the worktree is the
// task files' ("task file").
type evidenceError struct{ err error }

func (e *evidenceError) Error() string { return e.err.Error() }
func (e *evidenceError) Unwrap() error { return e.err }

func evidenceErrorf(format string, args ...any) error {
	return &evidenceError{err: fmt.Errorf(format, args...)}
}

// UseEvidenceFiles gives the engine what reads a change request's evidence
// files: open returns the content whose SHA-256 (hex) is sha256 among the
// files the capture send with the id send holds, and nothing else. The API
// gives it Dispatch's store (OpenSendFile). Set it before the first
// launch; without it, a repair attempt whose request names evidence fails
// at "evidence", uncounted, and the run pauses.
func (e *Engine) UseEvidenceFiles(open func(ctx context.Context, send, sha256 string) (io.ReadCloser, error)) {
	e.openEvidenceFile = open
}

// validEvidence holds a request's evidence to its shape: at most
// maxEvidence refs, each named in the form, distinct as a file system that
// folds case sees them, with a SHA-256, a size of 1 byte or more and the
// refs' sizes within maxEvidenceBytes together, held by a send of the
// store's form that one of the request's capture changes stands on; and
// every capture change's send holds one of the refs, so no finding names
// a capture its attempt is not handed.
func validEvidence(cr ChangeRequest) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrBadChangeRequest}, args...)...)
	}
	if len(cr.Evidence) > maxEvidence {
		return bad("a request names at most %d evidence files", maxEvidence)
	}
	sends := map[string]bool{}
	for _, c := range cr.Changes {
		if c.Capture != nil {
			sends[c.Capture.Send] = true
		}
	}
	seen := map[string]bool{}
	held := map[string]bool{}
	var total int64
	for _, ev := range cr.Evidence {
		if !evidenceName.MatchString(ev.Name) {
			return bad("the evidence file %q is not named capture-<id>-r<rev>.json or .png", ev.Name)
		}
		if k := strings.ToLower(ev.Name); seen[k] {
			return bad("the evidence file %s is named twice", ev.Name)
		} else {
			seen[k] = true
		}
		if !isSHA256(ev.SHA256) || ev.Bytes < 1 || ev.Bytes > maxEvidenceBytes-total {
			return bad("the evidence file %s has no SHA-256, or its size is none or passes the request's %d MiB", ev.Name, maxEvidenceBytes>>20)
		}
		total += ev.Bytes
		if !storeID.MatchString(ev.Send) || !sends[ev.Send] {
			return bad("the evidence file %s is held by %q, a send none of the request's captures stands on", ev.Name, ev.Send)
		}
		held[ev.Send] = true
	}
	for s := range sends {
		if !held[s] {
			return bad("the capture send %s has no evidence file in the request", s)
		}
	}
	return nil
}

// validCapture holds a requested change of kind capture to its shape: its
// capture and send ids of the store's form, its annotation 1 to
// maxCaptureK; its path and line, when it has them, the first source the
// capture traced, one line (from equal to to); no side, lines or receipt.
func validCapture(c RequestedChange) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: change %s: "+format, append([]any{ErrBadChangeRequest, c.ID}, args...)...)
	}
	switch {
	case c.Kind != "capture" && c.Capture != nil:
		return bad("only a capture's change names a capture")
	case c.Kind != "capture":
		return nil
	case c.Capture == nil:
		return bad("a capture's change names its capture")
	case !storeID.MatchString(c.Capture.Capture) || !storeID.MatchString(c.Capture.Send) || c.Capture.K < 1 || c.Capture.K > maxCaptureK:
		return bad("its capture and send are ids of the store's, its annotation 1 to %d", maxCaptureK)
	case c.Side != "" || len(c.Lines) > 0 || c.Check != nil:
		return bad("a capture's change names no side, lines or receipt")
	case c.From != c.To || c.From > 0 && c.Path == "":
		return bad("a capture's change names one line of its path, or none")
	}
	return nil
}

// evidenceFor is the evidence files of the capture send a change names,
// in the request's order.
func evidenceFor(cr *ChangeRequest, send string) []EvidenceRef {
	var out []EvidenceRef
	for _, ev := range cr.Evidence {
		if ev.Send == send {
			out = append(out, ev)
		}
	}
	return out
}

// evidencePath is where an evidence file is in the attempt, relative to
// its working directory.
func evidencePath(name string) string { return path.Join(taskDir, evidenceDir, name) }

// writeEvidence copies a request's evidence files into .conductor/evidence
// under root, each from open, counted and hashed as it is copied, after
// their names are held to the form again: one that cannot be read, or
// whose size or SHA-256 is not the request's, is removed and is an
// evidenceError. The directory and each file are made, never written over.
func writeEvidence(ctx context.Context, root *os.Root, refs []EvidenceRef, open func(ctx context.Context, send, sha256 string) (io.ReadCloser, error)) error {
	if len(refs) == 0 {
		return nil
	}
	if err := checkEvidenceFiles(refs); err != nil {
		return err
	}
	if open == nil {
		return evidenceErrorf("Conductor has no store to read the request's %d evidence files from", len(refs))
	}
	if err := root.Mkdir(path.Join(taskDir, evidenceDir), 0o755); err != nil {
		return err
	}
	for _, ev := range refs {
		if err := copyEvidence(ctx, root, ev, open); err != nil {
			return err
		}
	}
	return nil
}

// checkEvidenceFiles holds the refs to their shape again (validEvidence),
// before anything is opened or written: an evidenceError when one is not.
func checkEvidenceFiles(refs []EvidenceRef) error {
	if len(refs) == 0 {
		return nil
	}
	if err := validEvidence(ChangeRequest{Evidence: refs, Changes: changesOf(refs)}); err != nil {
		return evidenceErrorf("%v", err)
	}
	return nil
}

// changesOf is one capture change per send the refs name, for
// validEvidence's check of the refs alone.
func changesOf(refs []EvidenceRef) []RequestedChange {
	var out []RequestedChange
	seen := map[string]bool{}
	for _, ev := range refs {
		if !seen[ev.Send] {
			seen[ev.Send] = true
			out = append(out, RequestedChange{Kind: "capture", Capture: &RequestedCapture{Send: ev.Send}})
		}
	}
	return out
}

// copyEvidence copies one evidence file to its place under root.
func copyEvidence(ctx context.Context, root *os.Root, ev EvidenceRef, open func(ctx context.Context, send, sha256 string) (io.ReadCloser, error)) (err error) {
	rc, err := open(ctx, ev.Send, ev.SHA256)
	if err != nil {
		return evidenceErrorf("the evidence file %s cannot be read from the ticket's store: %v", ev.Name, err)
	}
	defer rc.Close()
	name := evidencePath(ev.Name)
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s exists in the worktree already: the evidence file is not written over it", name)
		}
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = cerr
		}
		if err != nil {
			_ = root.Remove(name)
		}
	}()
	h := sha256.New()
	// One byte past the size, so a longer content is seen.
	src := &readErr{r: io.LimitReader(rc, ev.Bytes+1)}
	n, err := io.Copy(f, io.TeeReader(src, h))
	switch {
	case src.err != nil:
		return evidenceErrorf("the evidence file %s could not be read whole: %v", ev.Name, src.err)
	case err != nil:
		return err
	case n != ev.Bytes:
		return evidenceErrorf("the evidence file %s is %s bytes, not the %d its request names", ev.Name, sizeWords(n, ev.Bytes), ev.Bytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != ev.SHA256 {
		return evidenceErrorf("the evidence file %s's SHA-256 is %s, not the request's %s", ev.Name, got, ev.SHA256)
	}
	return nil
}
