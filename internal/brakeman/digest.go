package brakeman

import (
	"regexp"
	"strings"
	"unicode"
)

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

// DigestOf is a brief's digest: its title, the reading derived from its
// summary (readingOf), its items counted by kind, and the runs, tasks,
// sessions and tickets its items' refs name, first cited first.
func DigestOf(b Brief) Digest {
	d := Digest{Schema: DigestSchema, Title: b.Title}
	d.Reading, d.ReadingCut = readingOf(b.Summary)
	var a aboutLists
	for _, it := range b.Items {
		c := &d.Counts
		switch it.Kind {
		case ItemDecision:
			c.Decisions++
		case ItemProposal:
			c.Proposals++
		case ItemDraftTicket, ItemDraftCrew, ItemDraftPlan:
			c.Drafts++
		case ItemHandoff:
			c.Handoffs++
		case ItemFact:
			c.Facts++
		case ItemInterpretation:
			c.Interpretations++
		case ItemOutcome:
			c.Outcomes++
		case ItemOverlap:
			c.Overlaps++
		case ItemComparison:
			c.Comparisons++
		case ItemEvidence:
			c.Evidence++
		}
		for _, r := range it.Refs() {
			a.note(r)
		}
	}
	d.About = a.DigestAbout
	return d
}

// aboutLists gathers a digest's about, each list at most MaxDigestAbout,
// each entry once.
type aboutLists struct {
	DigestAbout
	seen map[string]bool
}

// note adds what r names: a run of the refs that name one, and its task
// for the refs that name a task; the session of a session, a question or
// a touched file; the ticket of a ticket, a comment or a plan revision.
func (a *aboutLists) note(r Ref) {
	switch r.Kind {
	case RefRun, RefCommand, RefEvent, RefMessage, RefCommit:
		a.add(&a.Runs, "run", r.Run)
	case RefTask, RefAttempt, RefCheck, RefCandidate:
		a.add(&a.Runs, "run", r.Run)
		a.add(&a.Tasks, "task", r.Run+"/"+r.Task)
	case RefSession, RefQuestion, RefFile:
		a.add(&a.Sessions, "session", r.Session)
	case RefTicket, RefComment, RefPlan:
		a.add(&a.Tickets, "ticket", r.Ticket)
	}
}

func (a *aboutLists) add(list *[]string, kind, v string) {
	if v == "" || len(*list) >= MaxDigestAbout {
		return
	}
	if a.seen == nil {
		a.seen = map[string]bool{}
	}
	if k := kind + ":" + v; !a.seen[k] {
		a.seen[k] = true
		*list = append(*list, v)
	}
}

// readingOf is the reading a row shows of a summary: its first paragraph
// that holds any text, as plain text (plainText), its line breaks made
// spaces; past MaxReading characters it is cut at the last space before
// the bound and "…" is added, so it is at most MaxReading characters
// with the mark, and cut says so. A summary with no text gives "". The
// brief keeps its summary whole: this is a derived copy only.
func readingOf(summary string) (reading string, cut bool) {
	var para []string
	flush := func() string {
		s := plainText(para)
		para = para[:0]
		return s
	}
	for _, line := range strings.Split(strings.ReplaceAll(summary, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			para = append(para, line)
			continue
		}
		if s := flush(); s != "" {
			reading = s
			break
		}
	}
	if reading == "" {
		reading = flush()
	}
	runes := []rune(reading)
	if len(runes) <= MaxReading {
		return reading, false
	}
	head := runes[:MaxReading-1]
	if i := lastSpace(head); i > 0 {
		head = head[:i]
	}
	return strings.TrimRight(string(head), " ") + "…", true
}

func lastSpace(rs []rune) int {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i] == ' ' {
			return i
		}
	}
	return -1
}

var (
	headingMark = regexp.MustCompile(`^#{1,6}(\s+|$)`)
	headingTail = regexp.MustCompile(`\s+#+$`)
	listMark    = regexp.MustCompile(`^([-*+]|[0-9]{1,9}[.)])\s+`)
	ruleLine    = regexp.MustCompile(`^([-*_]\s*){3,}$`)
)

// plainText is a paragraph's lines as one line of plain text: a heading's
// marks, a quote's and a list item's dropped, a rule or a code fence
// line left out, then its inline Markdown made text (inlineText) and its
// spaces made one.
func plainText(lines []string) string {
	var parts []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") || ruleLine.MatchString(l) {
			continue
		}
		for strings.HasPrefix(l, ">") {
			l = strings.TrimSpace(l[1:])
		}
		if m := headingMark.FindString(l); m != "" {
			l = headingTail.ReplaceAllString(l[len(m):], "")
		}
		l = listMark.ReplaceAllString(l, "")
		parts = append(parts, l)
	}
	return strings.Join(strings.Fields(inlineText(strings.Join(parts, " "))), " ")
}

// inlineText makes a line's inline Markdown plain text: a link or an
// image is its text, a code span its content, an autolink its address;
// emphasis and strike-through marks are dropped where they mark (beside
// a word on one side), and kept where they are words of their own (a
// lone "*" between spaces, an underscore inside a name); a backslash
// escape is the character it escapes.
func inlineText(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\' && i+1 < len(rs) && rs[i+1] < 0x80 && (unicode.IsPunct(rs[i+1]) || unicode.IsSymbol(rs[i+1])):
			b.WriteRune(rs[i+1])
			i++
		case c == '`':
			for i+1 < len(rs) && rs[i+1] == '`' {
				i++
			}
		case c == '!' && i+1 < len(rs) && rs[i+1] == '[':
			// An image is its alt text, as a link is its text.
			if _, _, ok := linkAt(rs, i+1); !ok {
				b.WriteRune(c)
			}
		case c == '[':
			if text, end, ok := linkAt(rs, i); ok {
				b.WriteString(inlineText(text))
				i = end
				continue
			}
			b.WriteRune(c)
		case c == '<':
			if j := indexRune(rs[i+1:], '>'); j > 0 {
				inner := string(rs[i+1 : i+1+j])
				if !strings.ContainsAny(inner, " \t") && (strings.Contains(inner, "://") || strings.Contains(inner, "@")) {
					b.WriteString(inner)
					i += j + 1
					continue
				}
			}
			b.WriteRune(c)
		case c == '*' || c == '_' || c == '~':
			j := i
			for j+1 < len(rs) && rs[j+1] == c {
				j++
			}
			run := string(rs[i : j+1])
			if c == '~' && j == i || !marks(rs, i, j, c) {
				b.WriteString(run)
			}
			i = j
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// linkAt reads a link at rs[i] == '[': its text up to the matching ']',
// then a "(destination)" or a "[label]"; end is the index of the link's
// last rune.
func linkAt(rs []rune, i int) (text string, end int, ok bool) {
	depth := 0
	for j := i; j < len(rs); j++ {
		switch rs[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth > 0 {
				continue
			}
			if j+1 < len(rs) && (rs[j+1] == '(' || rs[j+1] == '[') {
				closer := ')'
				if rs[j+1] == '[' {
					closer = ']'
				}
				if k := indexRune(rs[j+2:], closer); k >= 0 {
					return string(rs[i+1 : j]), j + 2 + k, true
				}
			}
			return "", 0, false
		}
	}
	return "", 0, false
}

func indexRune(rs []rune, c rune) int {
	for i, r := range rs {
		if r == c {
			return i
		}
	}
	return -1
}

// marks reports whether the run rs[i..j] of c marks emphasis: beside a
// word on one side at least, and for an underscore not inside a word.
func marks(rs []rune, i, j int, c rune) bool {
	word := func(k int) bool {
		return k >= 0 && k < len(rs) && (unicode.IsLetter(rs[k]) || unicode.IsDigit(rs[k]))
	}
	space := func(k int) bool { return k < 0 || k >= len(rs) || unicode.IsSpace(rs[k]) }
	if space(i-1) && space(j+1) {
		return false
	}
	if c == '_' && word(i-1) && word(j+1) {
		return false
	}
	return true
}
