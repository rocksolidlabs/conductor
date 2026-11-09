package history

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The bounds of an entry (docs/plans/history.md, "Entries in"), in bytes
// of UTF-8. Replace refuses an entry past one (ErrBound); a source cuts
// its texts to them first (Clip, CheckSummary, CheckLog).
const (
	MaxTitle     = 200
	MaxSummary   = 1 << 10
	MaxRef       = 512 // a key, a ref, an anchor, a link's end, a source's key and mark
	MaxField     = 512 // every other field: the host, the project, the revision, an agent, an actor, ...
	MaxShortBody = 4 << 10
	MaxLongBody  = 16 << 10
	// MaxCheckBody is a check's body: its log, cut by CheckLog.
	MaxCheckBody = 16384

	// checkHead and checkTail are what CheckLog keeps of a longer log,
	// either side of logCut: 4,096 + 5 + 12,283 = MaxCheckBody.
	checkHead = 4096
	checkTail = MaxCheckBody - checkHead - len(logCut)
	logCut    = "\n…\n"
)

// replacement is what an invalid byte sequence becomes (U+FFFD).
const replacement = string(utf8.RuneError)

// The budget: at most MaxEntries entries and MaxBytes of their text, the
// oldest by when they happened dropped first (put.go, budget).
const (
	MaxEntries = 200_000
	MaxBytes   = 256 << 20
)

// bodyBound is the bound of an entry's body by its kind: 16 KiB for a
// submitted result, a ticket's description and criteria, a comment, a
// brief's item, a plan revision, a decision request (its context, its
// options with their tradeoffs, its uncertainty and what a yes
// authorizes: the ledger keeps the request in at most 16 KiB) and a
// decision's response (its text of at most 4 KiB and its rationale of at
// most 2 KiB, which a short body would cut); a check's, a verify step's
// and a failed step's log at MaxCheckBody; 4 KiB for the rest (a
// session's entry, a message, a checkpoint, a blocker, a finding, a
// verdict, an act's note).
func bodyBound(kind string) int {
	switch kind {
	case "attempt.result", "ticket", "comment", "brief.item", "plan", "decision", "decision.response":
		return MaxLongBody
	case "check", "verify", "step":
		return MaxCheckBody
	}
	return MaxShortBody
}

// Clip is s as valid UTF-8 (each run of invalid bytes replaced by
// U+FFFD) cut at a rune boundary to at most n bytes.
func Clip(s string, n int) string {
	s = strings.ToValidUTF8(s, replacement)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// clipTail is the last at most n bytes of s, valid UTF-8, starting at a
// rune boundary.
func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// CheckSummary is a check's summary: its argv joined by spaces, cut at a
// rune boundary with "… (N arguments)" when longer than MaxSummary.
func CheckSummary(argv []string) string {
	s := strings.ToValidUTF8(strings.Join(argv, " "), replacement)
	if len(s) <= MaxSummary {
		return s
	}
	suffix := fmt.Sprintf("… (%d arguments)", len(argv))
	return Clip(s, MaxSummary-len(suffix)) + suffix
}

// CheckLog is a failed check's body, its log alone: a log of at most
// MaxCheckBody bytes whole; a longer one its first 4,096 bytes, "\n…\n"
// and its last 12,283, each slice cut back to a rune boundary (so never
// longer). A check that passed is indexed by its ledger tail alone,
// Clip'ed to MaxCheckBody.
func CheckLog(log string) string {
	log = strings.ToValidUTF8(log, replacement)
	if len(log) <= MaxCheckBody {
		return log
	}
	return Clip(log, checkHead) + logCut + clipTail(log, checkTail)
}
