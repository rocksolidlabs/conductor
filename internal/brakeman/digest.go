package brakeman

// A digest is what a list's row says of a kept brief without opening it:
// the brief's title, a short reading derived from its summary, how many
// items of each kind it holds and the runs, tasks, sessions and tickets
// its items cite. The server makes it as the brief is kept and keeps it
// as a sibling document of its own, requests/br-<n>-digest.json, which an
// older build neither reads nor removes: the request's own document and
// the brief's do not change by a byte.

// The digest's schema and bounds.
const (
	DigestSchema   = 1
	MaxReading     = 280      // characters (runes), the derived reading
	MaxDigestAbout = 20       // each about list
	MaxDigestDoc   = 16 << 10 // the sibling document
)

// Digest is a kept brief's digest.
type Digest struct {
	Schema     int          `json:"schema"`
	Title      string       `json:"title"`   // the brief's title, at most MaxTitle characters
	Reading    string       `json:"reading"` // at most MaxReading characters, ending "…" when cut
	ReadingCut bool         `json:"readingCut,omitempty"`
	Counts     DigestCounts `json:"counts"`
	About      DigestAbout  `json:"about"`
}

// DigestCounts are a brief's items by kind, each 0 to MaxItems, omitted
// when 0.
type DigestCounts struct {
	Decisions       int `json:"decisions,omitempty"`
	Proposals       int `json:"proposals,omitempty"`
	Drafts          int `json:"drafts,omitempty"` // draft_ticket, draft_crew and draft_plan
	Handoffs        int `json:"handoffs,omitempty"`
	Facts           int `json:"facts,omitempty"`
	Interpretations int `json:"interpretations,omitempty"`
	Outcomes        int `json:"outcomes,omitempty"`
	Overlaps        int `json:"overlaps,omitempty"`
	Comparisons     int `json:"comparisons,omitempty"`
	Evidence        int `json:"evidence,omitempty"`
}

// DigestAbout is what a brief's items cite, first cited first, each list
// at most MaxDigestAbout, omitted when empty.
type DigestAbout struct {
	Runs     []string `json:"runs,omitempty"`     // run ids
	Tasks    []string `json:"tasks,omitempty"`    // "<run>/<task>"
	Sessions []string `json:"sessions,omitempty"` // 16 hex digits
	Tickets  []string `json:"tickets,omitempty"`  // KEY-n
}
