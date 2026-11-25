// Package agentproto holds the protocol way's drivers: for each agent that
// may run it, the agent's own documented structured interface, spoken over
// the pipes of a child the session started (session.Protocol). It holds the
// closed set of protocol names a catalog may declare (Names, Check) and,
// for each name, the driver and its gate: the oldest version it was
// qualified against and the shape it checks at every start.
//
// It imports internal/session and internal/catalog; neither imports it.
package agentproto

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// ClaudeStreamJSON is Claude Code's print mode with stream-json in and out.
const ClaudeStreamJSON = "claude-stream-json"

// Names are the protocols a catalog's agent may declare, in the order they
// were added.
func Names() []string { return []string{ClaudeStreamJSON} }

// Check reports an error when r names a protocol Conductor does not speak;
// nil and the empty recipe ({}, the protocol off) pass. The recipe's shape
// is the catalog's to check (catalog.Load, Catalog.Upsert).
func Check(r *catalog.ProtocolRecipe) error {
	if r.Off() {
		return nil
	}
	if !slices.Contains(Names(), r.Name) {
		return fmt.Errorf("protocol %q is not one Conductor speaks (it speaks %s)", r.Name, strings.Join(Names(), ", "))
	}
	return nil
}

// Moves says whether a session of the agent the protocol name drives may
// move between its terminal and its chat (docs/plans/chat-or-terminal.md,
// design 9): the driver's word, never the operator's, true only once
// resuming one conversation across the two ways is established for its
// agent by its documentation and a by-hand observation. A name Conductor
// does not speak moves nothing.
func Moves(name string) bool {
	switch name {
	case ClaudeStreamJSON:
		return claudeMoves
	}
	return false
}

// claudeMoves is Claude Code's Moves: set on the by-hand observation of both
// directions with the real agent, recorded in row 5's As built note.
const claudeMoves = true

// Qualified says whether a probed version of the agent may run the
// protocol name: at or above its driver's MinVersion. A version the probe
// did not find ("") is not qualified, and the session stays in its
// terminal.
func Qualified(name, version string) bool {
	switch name {
	case ClaudeStreamJSON:
		return versionAtLeast(version, ClaudeMinVersion)
	}
	return false
}

// versionAtLeast says whether v, a dotted version of numbers ("2.1.331",
// a suffix after a '-' or a space ignored), is at or above min. A version
// that is not of that form is not.
func versionAtLeast(v, min string) bool {
	a, ok := versionParts(v)
	if !ok {
		return false
	}
	b, _ := versionParts(min)
	for i := range max(len(a), len(b)) {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return true
}

// versionParts reads "2.1.331" (or "2.1.331-beta", "2.1.331 (Claude
// Code)") as its numbers: at least two, at most four, each of at most six
// digits.
func versionParts(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	if i := strings.IndexAny(v, "- +("); i >= 0 {
		v = v[:i]
	}
	fields := strings.Split(v, ".")
	if len(fields) < 2 || len(fields) > 4 {
		return nil, false
	}
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		if f == "" || len(f) > 6 || strings.Trim(f, "0123456789") != "" {
			return nil, false
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}
