package session

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The Touched section's index (design 4e; round 13, G1): one entry per file
// the agent read, edited, wrote or deleted since the session started, kept
// beside the activity ring so a long session's early files are not lost
// when the ring (MaxActivity, shared with every other entry) or the replay
// (ActivityReplay) moves past them. It is filled from the `file` entries
// record() keeps, under the session's lock, and served by the file request
// `op: touched`.
const MaxTouched = 2000 // files kept; the least recently touched goes first

type touchedEntry struct {
	op, tool, by string
	ops          []string
	count        int
	first, last  time.Time
}

type TouchedIndex struct {
	m map[string]*touchedEntry
}

// touchedAbs is the path a file event names, made absolute under cwd when
// relative, cleaned: the key the index and the browser agree on.
func touchedAbs(cwd, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(cwd, p)
}

// note records one file event. coalesced says the activity log merged it
// into its newest entry (the same path and op within FileCoalesce): the
// time moves, the count does not.
func (x *TouchedIndex) note(abs, op, tool, by string, at time.Time, coalesced bool) {
	if abs == "" || op == "" {
		return
	}
	if x.m == nil {
		x.m = map[string]*touchedEntry{}
	}
	t := x.m[abs]
	if t == nil {
		if len(x.m) >= MaxTouched {
			x.evictOldest()
		}
		t = &touchedEntry{first: at}
		x.m[abs] = t
	}
	if at.After(t.last) {
		t.last = at
	}
	if coalesced && t.count > 0 && t.op == op {
		return
	}
	t.count++
	t.op, t.tool, t.by = op, tool, by
	if !slices.Contains(t.ops, op) {
		t.ops = append(t.ops, op)
	}
}

// evictOldest drops the least recently touched file.
func (x *TouchedIndex) evictOldest() {
	var oldest string
	var at time.Time
	for p, t := range x.m {
		if oldest == "" || t.last.Before(at) || (t.last.Equal(at) && p < oldest) {
			oldest, at = p, t.last
		}
	}
	delete(x.m, oldest)
}

// snapshot is every file, the most recently touched first (ties by path).
func (x *TouchedIndex) snapshot() []proto.TouchedFile {
	out := make([]proto.TouchedFile, 0, len(x.m))
	for p, t := range x.m {
		out = append(out, proto.TouchedFile{
			Path: p, Op: t.op, Ops: slices.Clone(t.ops), Count: t.count, Tool: t.tool, By: t.by,
			First: t.first.UTC().Format(time.RFC3339Nano), Last: t.last.UTC().Format(time.RFC3339Nano),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Last != out[j].Last {
			return out[i].Last > out[j].Last
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// noteTouched feeds the index from a file entry record() keeps; s.mu held.
func (s *Local) noteTouched(e ActivityEntry, coalesced bool) {
	at := e.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	s.touched.note(touchedAbs(s.info.Cwd, e.Path), e.Op, e.Tool, e.ByName, at, coalesced)
}

// TouchedPath answers the file request `op: touched`: a FILE header of
// kind `touched`, Path the working directory, Touched the files the agent
// touched (the most recently touched first, those the deny list refuses
// left out), Truncated when the header's size cut the list.
func (s *Local) TouchedPath(deny []string) proto.FileHeader {
	s.mu.Lock()
	list := s.touched.snapshot()
	cwd := s.info.Cwd
	s.mu.Unlock()
	h := proto.FileHeader{Path: cwd, Kind: "touched", Exists: true}
	budget := proto.MaxFileHeader - 4096
	used := 0
	for _, t := range list {
		if insideAny(t.Path, deny) {
			continue
		}
		b, err := json.Marshal(t)
		if err != nil {
			continue
		}
		if used+len(b)+1 > budget {
			h.Truncated = true
			break
		}
		used += len(b) + 1
		h.Touched = append(h.Touched, t)
	}
	return h
}

// TouchedAbs is the path a file event names, made absolute under cwd when
// relative, cleaned: the key the index and the browser agree on.
func TouchedAbs(cwd, p string) string { return touchedAbs(cwd, p) }

// Note records one touch of abs (an absolute path, TouchedAbs) at `at`,
// counted as a touch of its own: what a hosted session's index takes from
// the file entries its host reports (#304).
func (x *TouchedIndex) Note(abs, op, tool, by string, at time.Time) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	x.note(abs, op, tool, by, at, false)
}

// Snapshot is every file, the most recently touched first (ties by path).
func (x *TouchedIndex) Snapshot() []proto.TouchedFile { return x.snapshot() }

// TouchedFiles is the session's index as GET /api/touched lists it (#304):
// every file the agent touched, the most recently touched first, those the
// deny list refuses left out. A snapshot under the session's lock; the deny
// list is read outside it, as a file request reads it.
func (s *Local) TouchedFiles() []proto.TouchedFile {
	deny := s.fileDeny()
	s.mu.Lock()
	list := s.touched.snapshot()
	s.mu.Unlock()
	out := list[:0]
	for _, t := range list {
		if !insideAny(t.Path, deny) {
			out = append(out, t)
		}
	}
	return out
}

// Denied reports whether the session's deny list refuses path, an absolute
// path as the touched index keys it (TouchedAbs): the rule TouchedFiles
// applies, so what it leaves out of the index is never offered elsewhere
// either (the context providers, internal/api's extcontext.go). It reads
// the files the list names, outside the session's lock.
func (s *Local) Denied(path string) bool {
	return insideAny(path, s.fileDeny())
}
