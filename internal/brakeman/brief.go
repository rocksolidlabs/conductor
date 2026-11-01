package brakeman

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// BriefSchema is the one schema of a brief this build keeps.
const BriefSchema = "brakeman.brief/1"

// Brief is what a turn hands back (docs/plans/brakeman.md, A brief). It
// carries no asOf: what it covers is the server's record of the turn's
// reads (Request.Coverage), never the agent's word.
type Brief struct {
	Schema      string `json:"schema"`
	Title       string `json:"title"`                 // at most MaxTitle characters
	Summary     string `json:"summary"`               // Brakeman's reading, Markdown, at most MaxSummary bytes
	Uncertainty string `json:"uncertainty,omitempty"` // at most MaxUncertainty bytes
	Items       []Item `json:"items"`                 // at most MaxItems
}

// A brief's bounds. Text bounds are bytes but for the ones named in
// characters (a title, a label), which are runes.
const (
	MaxBrief       = 256 << 10 // the brief as kept, store.Encode's form
	MaxItems       = 100
	MaxTitle       = 200 // characters
	MaxSummary     = 4 << 10
	MaxUncertainty = 2 << 10
	MaxBriefRefs   = 500 // distinct refs across a brief's items
	MaxDraftTicket = 20  // draft tickets a brief
	MaxLabel       = 80  // characters
	MaxArgs        = 4 << 10
	MaxCrewBody    = 64 << 10
	MaxRevision    = 64
	MaxPathRef     = MaxPath
)

// The kinds of an item.
const (
	ItemFact           = "fact/1"
	ItemInterpretation = "interpretation/1"
	ItemProposal       = "proposal/1"
	ItemOutcome        = "outcome/1"
	ItemDecision       = "decision/1"
	ItemComparison     = "comparison/1"
	ItemEvidence       = "evidence/1"
	ItemDraftTicket    = "draft_ticket/1"
	ItemDraftCrew      = "draft_crew/1"
	ItemHandoff        = "handoff/1"
	ItemOverlap        = "overlap/1"
)

// Item is one item of a brief: its kind and that kind's own fields, one
// of the pointers below (the one its kind names). In JSON it is one
// object, the kind beside its fields; a field of another kind is refused.
type Item struct {
	Kind           string
	Fact           *Fact
	Interpretation *Interpretation
	Proposal       *Proposal
	Outcome        *Outcome
	Decision       *Decision
	Comparison     *Comparison
	Evidence       *EvidencePanel
	DraftTicket    *DraftTicket
	DraftCrew      *DraftCrew
	Handoff        *Handoff
	Overlap        *Overlap
}

// Fact is something true, with the evidence it rests on.
type Fact struct {
	Text     string `json:"text"`     // at most 1 KiB
	Evidence []Ref  `json:"evidence"` // 1 to 10
}

// Interpretation is Brakeman's reading of facts of the same brief.
type Interpretation struct {
	Text  string `json:"text"`  // at most 2 KiB
	Facts []int  `json:"facts"` // indexes of fact items, at least one
}

// Proposal names an operation Conductor can run.
type Proposal struct {
	Text   string          `json:"text"`           // at most 2 KiB
	Op     string          `json:"op"`             // an operation the guide lists
	Target Ref             `json:"target"`         // what it acts on
	Args   json.RawMessage `json:"args,omitempty"` // a JSON object, at most 4 KiB
	// ExpectedRevision is the target's revision as the turn read it,
	// required where the operation's route checks one (PR-3b checks it
	// against the guide and the read).
	ExpectedRevision string `json:"expectedRevision,omitempty"`
	Facts            []int  `json:"facts,omitempty"`
}

// Outcome is a verified outcome: it names a receipt.
type Outcome struct {
	Text    string `json:"text"`    // at most 1 KiB
	Receipt Ref    `json:"receipt"` // a check:, command: or op: ref
}

// Decision is a decision card. Conductor renders its controls from the
// live record of About, never from these fields.
type Decision struct {
	About           Ref           `json:"about"` // a question:, task: or ticket: ref
	Happened        string        `json:"happened"`
	WhyHuman        string        `json:"whyHuman"`
	Question        string        `json:"question"`              // at most 500 bytes
	Alternatives    []Alternative `json:"alternatives"`          // 2 to 6
	Recommendation  *int          `json:"recommendation"`        // an alternative's index, or -1 for none
	Uncertainty     string        `json:"uncertainty,omitempty"` // at most 1 KiB
	Blocked         []Ref         `json:"blocked,omitempty"`     // at most 20
	Authorizes      string        `json:"authorizes"`            // at most 1 KiB
	Evidence        []Ref         `json:"evidence"`              // 1 to 20
	SubjectRevision string        `json:"subjectRevision,omitempty"`
}

// Alternative is one answer a decision card offers.
type Alternative struct {
	Label     string `json:"label"`     // at most 80 characters
	Tradeoffs string `json:"tradeoffs"` // at most 1 KiB
}

// Comparison is a table.
type Comparison struct {
	Title    string   `json:"title"`
	Columns  []string `json:"columns"` // 2 to 6
	Rows     []Row    `json:"rows"`    // 1 to 20
	Evidence []Ref    `json:"evidence,omitempty"`
}

// Row is a comparison's row: a label and one cell a column.
type Row struct {
	Label string   `json:"label"`
	Cells []string `json:"cells"` // at most 500 bytes each
}

// EvidencePanel is a panel of evidence.
type EvidencePanel struct {
	Title string `json:"title"`
	Refs  []Ref  `json:"refs"` // 1 to 50
}

// DraftTicket is a ticket a person may make work.
type DraftTicket struct {
	Project     string `json:"project"` // a project's key
	Title       string `json:"title"`   // at most 200 characters
	Description string `json:"description,omitempty"`
	Acceptance  string `json:"acceptance,omitempty"`
	Priority    int    `json:"priority,omitempty"` // 0 to 4
	Blocks      []int  `json:"blocks,omitempty"`   // indexes of other draft tickets of the brief
	From        []Ref  `json:"from,omitempty"`
}

// DraftCrew is a crew a person may save.
type DraftCrew struct {
	Crew  json.RawMessage `json:"crew"`            // the body POST /api/crews takes
	Gates []Gate          `json:"gates,omitempty"` // at most 10
	From  []Ref           `json:"from,omitempty"`
}

// Gate is a decision a person makes before a draft crew's task.
type Gate struct {
	Task     string `json:"task"`
	Decision string `json:"decision"` // at most 500 bytes
}

// Handoff is a handoff package, built from Conductor's records; never an
// agent's conversation.
type Handoff struct {
	Objective   string   `json:"objective"`
	Constraints string   `json:"constraints,omitempty"`
	Decisions   []string `json:"decisions,omitempty"` // at most 20
	Revision    *Ref     `json:"revision,omitempty"`  // a commit: ref
	Questions   []string `json:"questions,omitempty"` // at most 20
	Evidence    []Ref    `json:"evidence,omitempty"`
}

// Overlap is a set of paths changed in two or more trees.
type Overlap struct {
	Paths    []string `json:"paths"`   // 1 to 50
	Between  []Ref    `json:"between"` // 2 to 8 refs of attempts or sessions
	Evidence []Ref    `json:"evidence,omitempty"`
}

// fields reports whether the item holds its own kind's fields (own) and
// how many kinds' fields it holds (n): a well-formed item holds its own
// and no other.
func (it Item) fields() (own bool, n int) {
	for _, f := range []struct {
		kind string
		set  bool
	}{
		{ItemFact, it.Fact != nil},
		{ItemInterpretation, it.Interpretation != nil},
		{ItemProposal, it.Proposal != nil},
		{ItemOutcome, it.Outcome != nil},
		{ItemDecision, it.Decision != nil},
		{ItemComparison, it.Comparison != nil},
		{ItemEvidence, it.Evidence != nil},
		{ItemDraftTicket, it.DraftTicket != nil},
		{ItemDraftCrew, it.DraftCrew != nil},
		{ItemHandoff, it.Handoff != nil},
		{ItemOverlap, it.Overlap != nil},
	} {
		if f.set {
			n++
			own = own || f.kind == it.Kind
		}
	}
	return own, n
}

// body is the item's own fields, by its kind.
func (it Item) body() (any, error) {
	var v any
	switch it.Kind {
	case ItemFact:
		v = it.Fact
	case ItemInterpretation:
		v = it.Interpretation
	case ItemProposal:
		v = it.Proposal
	case ItemOutcome:
		v = it.Outcome
	case ItemDecision:
		v = it.Decision
	case ItemComparison:
		v = it.Comparison
	case ItemEvidence:
		v = it.Evidence
	case ItemDraftTicket:
		v = it.DraftTicket
	case ItemDraftCrew:
		v = it.DraftCrew
	case ItemHandoff:
		v = it.Handoff
	case ItemOverlap:
		v = it.Overlap
	default:
		return nil, fmt.Errorf("no item kind %q", it.Kind)
	}
	return v, nil
}

// MarshalJSON writes the item as one object: its kind, then its fields.
func (it Item) MarshalJSON() ([]byte, error) {
	v, err := it.body()
	if err != nil {
		return nil, err
	}
	// As store.Encode writes: <, > and & as they are, so a text's bound is
	// not passed by its escaping.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if !bytes.HasPrefix(b, []byte("{")) {
		return nil, fmt.Errorf("item %s has no fields", it.Kind)
	}
	kind, _ := json.Marshal(it.Kind)
	out := append([]byte(`{"kind":`), kind...)
	if len(b) > 2 {
		out = append(out, ',')
	}
	return append(out, b[1:]...), nil
}

// UnmarshalJSON reads one object: its kind, then that kind's fields,
// strictly, so a field of another kind is refused.
func (it *Item) UnmarshalJSON(b []byte) error {
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	*it = Item{Kind: head.Kind}
	var dst any
	switch head.Kind {
	case ItemFact:
		it.Fact = &Fact{}
		dst = it.Fact
	case ItemInterpretation:
		it.Interpretation = &Interpretation{}
		dst = it.Interpretation
	case ItemProposal:
		it.Proposal = &Proposal{}
		dst = it.Proposal
	case ItemOutcome:
		it.Outcome = &Outcome{}
		dst = it.Outcome
	case ItemDecision:
		it.Decision = &Decision{}
		dst = it.Decision
	case ItemComparison:
		it.Comparison = &Comparison{}
		dst = it.Comparison
	case ItemEvidence:
		it.Evidence = &EvidencePanel{}
		dst = it.Evidence
	case ItemDraftTicket:
		it.DraftTicket = &DraftTicket{}
		dst = it.DraftTicket
	case ItemDraftCrew:
		it.DraftCrew = &DraftCrew{}
		dst = it.DraftCrew
	case ItemHandoff:
		it.Handoff = &Handoff{}
		dst = it.Handoff
	case ItemOverlap:
		it.Overlap = &Overlap{}
		dst = it.Overlap
	default:
		return fmt.Errorf("no item kind %q", head.Kind)
	}
	// The kind is read above; the strict decode takes the rest, written
	// back with <, > and & as they are: json.Marshal would escape them in
	// the raw fields kept as they came (a proposal's args, a draft crew's
	// body), six bytes for one, and a body within its bound would be
	// measured past it.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	delete(fields, "kind")
	var rest bytes.Buffer
	enc := json.NewEncoder(&rest)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(fields); err != nil {
		return err
	}
	return store.DecodeStrict(rest.Bytes(), dst)
}

// isControl reports a character a kept text never holds: a C0 control
// but newline and tab, DEL, or a C1 control.
func isControl(r rune) bool {
	return (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r < 0xa0)
}

// cleanText is s without its control characters but newline and tab, as
// a session's message is cleaned, and with invalid UTF-8 made U+FFFD,
// trimmed of the spaces around it. Unlike session.CleanMessage it never
// cuts: a field past its bound is refused, never shortened.
func cleanText(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, s))
}

// ErrBriefAsOf is the refusal of a brief that says what it covers.
var ErrBriefAsOf = errors.New("brakeman: a brief carries no asOf: what it covers is the server's record of the turn's reads")

// DecodeBrief reads a brief as a session sent it, strictly (an unknown
// field, a field of another item's kind, an asOf: each refused), cleans
// its text and checks it (CheckBrief). The error is an *Invalid with the
// code invalid_brief, or one that wraps ErrBound.
func DecodeBrief(body []byte, p Permissions, f Facts) (Brief, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return Brief{}, invalidBrief("brief", "not a JSON object: %v", err)
	}
	if _, ok := top["asOf"]; ok {
		return Brief{}, &Invalid{Code: CodeInvalidBrief, Field: "asOf", Why: ErrBriefAsOf.Error()}
	}
	var b Brief
	if err := store.DecodeStrict(body, &b); err != nil {
		return Brief{}, invalidBrief("brief", "%v", err)
	}
	return b, CheckBrief(&b, p, f)
}

func invalidBrief(field, why string, a ...any) error {
	return &Invalid{Code: CodeInvalidBrief, Field: field, Why: fmt.Sprintf(why, a...)}
}

// CheckBrief cleans b's text in place and holds every field to its kind's
// rules and bounds (docs/plans/brakeman.md, A brief): a fact names
// evidence, an interpretation names facts of the brief, a proposal names
// an operation, an outcome names a receipt; draft tickets block without a
// cycle; a draft crew passes f.CheckCrew; an item p does not allow is
// refused; at most MaxBriefRefs distinct refs; and the brief, as kept, at
// most MaxBrief bytes. A field past its bound is refused with its name and
// bound, never cut.
func CheckBrief(b *Brief, p Permissions, f Facts) error {
	if b.Schema != BriefSchema {
		return invalidBrief("schema", "must be %q", BriefSchema)
	}
	b.Title, b.Summary, b.Uncertainty = cleanText(b.Title), cleanText(b.Summary), cleanText(b.Uncertainty)
	if err := chars("title", b.Title, MaxTitle, true, invalidBrief); err != nil {
		return err
	}
	if err := size("summary", b.Summary, MaxSummary, true, invalidBrief); err != nil {
		return err
	}
	if err := size("uncertainty", b.Uncertainty, MaxUncertainty, false, invalidBrief); err != nil {
		return err
	}
	if len(b.Items) > MaxItems {
		return invalidBrief("items", "at most %d", MaxItems)
	}
	tickets := 0
	refs := map[Ref]bool{}
	for i := range b.Items {
		it := &b.Items[i]
		field := fmt.Sprintf("items[%d]", i)
		if err := checkItem(b, i, it, field, p, f); err != nil {
			return err
		}
		if it.Kind == ItemDraftTicket {
			tickets++
		}
		for _, r := range it.Refs() {
			refs[r] = true
		}
	}
	if tickets > MaxDraftTicket {
		return invalidBrief("items", "at most %d draft tickets", MaxDraftTicket)
	}
	if len(refs) > MaxBriefRefs {
		return invalidBrief("items", "%d distinct refs, at most %d", len(refs), MaxBriefRefs)
	}
	if err := blocksAcyclic(b); err != nil {
		return err
	}
	enc, err := encode(b)
	if err != nil {
		return invalidBrief("brief", "%v", err)
	}
	if len(enc) > MaxBrief {
		return fmt.Errorf("%w: the brief is %d bytes as kept, at most %d", ErrBound, len(enc), MaxBrief)
	}
	return nil
}

// Refs is every ref the item names, in its fields' order.
func (it Item) Refs() []Ref {
	var out []Ref
	switch it.Kind {
	case ItemFact:
		out = append(out, it.Fact.Evidence...)
	case ItemProposal:
		out = append(out, it.Proposal.Target)
	case ItemOutcome:
		out = append(out, it.Outcome.Receipt)
	case ItemDecision:
		d := it.Decision
		out = append(append(append(out, d.About), d.Blocked...), d.Evidence...)
	case ItemComparison:
		out = append(out, it.Comparison.Evidence...)
	case ItemEvidence:
		out = append(out, it.Evidence.Refs...)
	case ItemDraftTicket:
		out = append(out, it.DraftTicket.From...)
	case ItemDraftCrew:
		out = append(out, it.DraftCrew.From...)
	case ItemHandoff:
		h := it.Handoff
		if h.Revision != nil {
			out = append(out, *h.Revision)
		}
		out = append(out, h.Evidence...)
	case ItemOverlap:
		out = append(append(out, it.Overlap.Between...), it.Overlap.Evidence...)
	}
	return out
}

// size refuses s past max bytes, or empty when required.
func size(field, s string, max int, required bool, bad func(string, string, ...any) error) error {
	switch {
	case required && s == "":
		return bad(field, "is required")
	case len(s) > max:
		return bad(field, "%d bytes, at most %d", len(s), max)
	}
	return nil
}

// chars refuses s past max characters, or empty when required.
func chars(field, s string, max int, required bool, bad func(string, string, ...any) error) error {
	switch n := utf8.RuneCountInString(s); {
	case required && s == "":
		return bad(field, "is required")
	case n > max:
		return bad(field, "%d characters, at most %d", n, max)
	}
	return nil
}

// refList refuses a list of refs outside min to max, a zero ref, or one
// of a kind not in kinds (nil for any).
func refList(field string, refs []Ref, min, max int, kinds ...string) error {
	if len(refs) < min || len(refs) > max {
		if min == 0 {
			return invalidBrief(field, "at most %d refs", max)
		}
		return invalidBrief(field, "%d to %d refs", min, max)
	}
	for i, r := range refs {
		if err := oneRef(fmt.Sprintf("%s[%d]", field, i), r, kinds...); err != nil {
			return err
		}
	}
	return nil
}

func oneRef(field string, r Ref, kinds ...string) error {
	if r.IsZero() {
		return invalidBrief(field, "is required")
	}
	if len(kinds) > 0 && !slices.Contains(kinds, r.Kind) {
		return invalidBrief(field, "a %s ref, not %s", strings.Join(kinds, ", "), r.Kind)
	}
	if err := r.Valid(); err != nil {
		return invalidBrief(field, "%v", err)
	}
	return nil
}

// texts cleans each of list in place and refuses more than max entries, an
// empty one, or one past each bytes.
func texts(field string, list []string, max, each int) error {
	if len(list) > max {
		return invalidBrief(field, "at most %d", max)
	}
	for i := range list {
		list[i] = cleanText(list[i])
		if err := size(fmt.Sprintf("%s[%d]", field, i), list[i], each, true, invalidBrief); err != nil {
			return err
		}
	}
	return nil
}

// checkItem checks item i of b by its kind's rules.
func checkItem(b *Brief, i int, it *Item, field string, p Permissions, f Facts) error {
	if _, err := it.body(); err != nil {
		return invalidBrief(field+".kind", "%v", err)
	}
	// body answers a typed nil for a kind whose fields are missing, never a
	// nil interface: the kind's own pointer is what is checked.
	switch own, n := it.fields(); {
	case !own:
		return invalidBrief(field, "a %s item has no fields", it.Kind)
	case n > 1:
		return invalidBrief(field, "a %s item holds another kind's fields", it.Kind)
	}
	switch it.Kind {
	case ItemDraftTicket, ItemDraftCrew, ItemHandoff:
		if !p.Draft {
			return invalidBrief(field+".kind", "Brakeman may not draft here: a %s item is refused", it.Kind)
		}
	case ItemProposal, ItemDecision:
		if !p.Propose {
			return invalidBrief(field+".kind", "Brakeman may not propose here: a %s item is refused", it.Kind)
		}
	}
	facts := func(name string, idx []int, required bool) error {
		if required && len(idx) == 0 {
			return invalidBrief(field+"."+name, "names at least one fact")
		}
		if len(idx) > MaxItems {
			return invalidBrief(field+"."+name, "at most %d", MaxItems)
		}
		for j, k := range idx {
			if k < 0 || k >= len(b.Items) || b.Items[k].Kind != ItemFact {
				return invalidBrief(fmt.Sprintf("%s.%s[%d]", field, name, j), "%d is no fact of this brief", k)
			}
		}
		return nil
	}
	switch it.Kind {
	case ItemFact:
		x := it.Fact
		x.Text = cleanText(x.Text)
		if err := size(field+".text", x.Text, 1<<10, true, invalidBrief); err != nil {
			return err
		}
		if len(x.Evidence) == 0 {
			return invalidBrief(field+".evidence", "a fact names its evidence: one ref at least")
		}
		return refList(field+".evidence", x.Evidence, 1, 10)
	case ItemInterpretation:
		x := it.Interpretation
		x.Text = cleanText(x.Text)
		if err := size(field+".text", x.Text, 2<<10, true, invalidBrief); err != nil {
			return err
		}
		return facts("facts", x.Facts, true)
	case ItemProposal:
		x := it.Proposal
		x.Text = cleanText(x.Text)
		if err := size(field+".text", x.Text, 2<<10, true, invalidBrief); err != nil {
			return err
		}
		if !operationPattern.MatchString(x.Op) {
			return invalidBrief(field+".op", "an operation's name, matching %s", operationPattern)
		}
		if err := oneRef(field+".target", x.Target); err != nil {
			return err
		}
		if len(x.Args) > 0 {
			if len(x.Args) > MaxArgs {
				return invalidBrief(field+".args", "%d bytes, at most %d", len(x.Args), MaxArgs)
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(x.Args, &obj); err != nil || obj == nil {
				return invalidBrief(field+".args", "a JSON object")
			}
		}
		if err := revision(field+".expectedRevision", x.ExpectedRevision); err != nil {
			return err
		}
		return facts("facts", x.Facts, false)
	case ItemOutcome:
		x := it.Outcome
		x.Text = cleanText(x.Text)
		if err := size(field+".text", x.Text, 1<<10, true, invalidBrief); err != nil {
			return err
		}
		if x.Receipt.IsZero() {
			return invalidBrief(field+".receipt", "a verified outcome names a receipt, or it is a fact or an interpretation")
		}
		return oneRef(field+".receipt", x.Receipt, RefCheck, RefCommand, RefOp)
	case ItemDecision:
		return checkDecision(field, it.Decision)
	case ItemComparison:
		x := it.Comparison
		x.Title = cleanText(x.Title)
		if err := chars(field+".title", x.Title, MaxTitle, true, invalidBrief); err != nil {
			return err
		}
		if len(x.Columns) < 2 || len(x.Columns) > 6 {
			return invalidBrief(field+".columns", "2 to 6")
		}
		for j := range x.Columns {
			x.Columns[j] = cleanText(x.Columns[j])
			if err := chars(fmt.Sprintf("%s.columns[%d]", field, j), x.Columns[j], MaxLabel, true, invalidBrief); err != nil {
				return err
			}
		}
		if len(x.Rows) < 1 || len(x.Rows) > 20 {
			return invalidBrief(field+".rows", "1 to 20")
		}
		for j := range x.Rows {
			r := &x.Rows[j]
			rf := fmt.Sprintf("%s.rows[%d]", field, j)
			r.Label = cleanText(r.Label)
			if err := chars(rf+".label", r.Label, MaxTitle, true, invalidBrief); err != nil {
				return err
			}
			if len(r.Cells) != len(x.Columns) {
				return invalidBrief(rf+".cells", "one cell a column: %d", len(x.Columns))
			}
			for k := range r.Cells {
				r.Cells[k] = cleanText(r.Cells[k])
				if err := size(fmt.Sprintf("%s.cells[%d]", rf, k), r.Cells[k], 500, false, invalidBrief); err != nil {
					return err
				}
			}
		}
		return refList(field+".evidence", x.Evidence, 0, 20)
	case ItemEvidence:
		x := it.Evidence
		x.Title = cleanText(x.Title)
		if err := chars(field+".title", x.Title, MaxTitle, true, invalidBrief); err != nil {
			return err
		}
		return refList(field+".refs", x.Refs, 1, 50)
	case ItemDraftTicket:
		x := it.DraftTicket
		x.Title, x.Description, x.Acceptance = cleanText(x.Title), cleanText(x.Description), cleanText(x.Acceptance)
		switch {
		case !projectKeyPattern.MatchString(x.Project):
			return invalidBrief(field+".project", "a project's key, matching %s", projectKeyPattern)
		case x.Priority < 0 || x.Priority > 4:
			return invalidBrief(field+".priority", "0 to 4")
		}
		if err := chars(field+".title", x.Title, MaxTitle, true, invalidBrief); err != nil {
			return err
		}
		if err := size(field+".description", x.Description, 16<<10, false, invalidBrief); err != nil {
			return err
		}
		if err := size(field+".acceptance", x.Acceptance, 8<<10, false, invalidBrief); err != nil {
			return err
		}
		if len(x.Blocks) > MaxDraftTicket {
			return invalidBrief(field+".blocks", "at most %d", MaxDraftTicket)
		}
		for j, k := range x.Blocks {
			if k == i || k < 0 || k >= len(b.Items) || b.Items[k].Kind != ItemDraftTicket || slices.Contains(x.Blocks[:j], k) {
				return invalidBrief(fmt.Sprintf("%s.blocks[%d]", field, j), "%d is no other draft ticket of this brief", k)
			}
		}
		return refList(field+".from", x.From, 0, 10)
	case ItemDraftCrew:
		x := it.DraftCrew
		var obj map[string]json.RawMessage
		switch {
		case len(x.Crew) > MaxCrewBody:
			return invalidBrief(field+".crew", "%d bytes, at most %d", len(x.Crew), MaxCrewBody)
		case json.Unmarshal(x.Crew, &obj) != nil || obj == nil:
			return invalidBrief(field+".crew", "a crew's body, a JSON object")
		case len(x.Gates) > 10:
			return invalidBrief(field+".gates", "at most 10")
		}
		for j := range x.Gates {
			g := &x.Gates[j]
			gf := fmt.Sprintf("%s.gates[%d]", field, j)
			g.Decision = cleanText(g.Decision)
			if !taskIDPattern.MatchString(g.Task) {
				return invalidBrief(gf+".task", "a task id, matching %s", taskIDPattern)
			}
			if err := size(gf+".decision", g.Decision, 500, true, invalidBrief); err != nil {
				return err
			}
		}
		if err := refList(field+".from", x.From, 0, 10); err != nil {
			return err
		}
		if err := f.CheckCrew(x.Crew); err != nil {
			return invalidBrief(field+".crew", "%v", err)
		}
		return nil
	case ItemHandoff:
		x := it.Handoff
		x.Objective, x.Constraints = cleanText(x.Objective), cleanText(x.Constraints)
		if err := size(field+".objective", x.Objective, 2<<10, true, invalidBrief); err != nil {
			return err
		}
		if err := size(field+".constraints", x.Constraints, 4<<10, false, invalidBrief); err != nil {
			return err
		}
		if err := texts(field+".decisions", x.Decisions, 20, 500); err != nil {
			return err
		}
		if err := texts(field+".questions", x.Questions, 20, 500); err != nil {
			return err
		}
		if x.Revision != nil {
			if err := oneRef(field+".revision", *x.Revision, RefCommit); err != nil {
				return err
			}
		}
		return refList(field+".evidence", x.Evidence, 0, 20)
	case ItemOverlap:
		x := it.Overlap
		if len(x.Paths) < 1 || len(x.Paths) > 50 {
			return invalidBrief(field+".paths", "1 to 50")
		}
		for j, pth := range x.Paths {
			if pth == "" || len(pth) > MaxPathRef || !utf8.ValidString(pth) || strings.ContainsFunc(pth, isControl) || strings.ContainsAny(pth, "\n\t") {
				return invalidBrief(fmt.Sprintf("%s.paths[%d]", field, j), "a path of at most %d bytes with no control character", MaxPathRef)
			}
		}
		if err := refList(field+".between", x.Between, 2, 8, RefAttempt, RefSession); err != nil {
			return err
		}
		return refList(field+".evidence", x.Evidence, 0, 20)
	}
	return nil
}

// operationPattern is an operation's name as the guide lists it.
var operationPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// revision refuses a revision past MaxRevision bytes or with a character
// outside [A-Za-z0-9._:-].
func revision(field, rev string) error {
	if len(rev) > MaxRevision {
		return invalidBrief(field, "at most %d bytes", MaxRevision)
	}
	for _, c := range rev {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return invalidBrief(field, "letters, digits and ._:- only")
		}
	}
	return nil
}

// checkDecision holds a decision card whole: what it is about, what
// happened, why a person is needed, the question, two to six alternatives
// with their tradeoffs, a recommendation (or -1), what answering
// authorizes and the evidence.
func checkDecision(field string, x *Decision) error {
	x.Happened, x.WhyHuman, x.Question = cleanText(x.Happened), cleanText(x.WhyHuman), cleanText(x.Question)
	x.Uncertainty, x.Authorizes = cleanText(x.Uncertainty), cleanText(x.Authorizes)
	if err := oneRef(field+".about", x.About, RefQuestion, RefTask, RefTicket); err != nil {
		return err
	}
	for _, t := range []struct {
		name     string
		s        string
		max      int
		required bool
	}{
		{"happened", x.Happened, 2 << 10, true},
		{"whyHuman", x.WhyHuman, 1 << 10, true},
		{"question", x.Question, 500, true},
		{"uncertainty", x.Uncertainty, 1 << 10, false},
		{"authorizes", x.Authorizes, 1 << 10, true},
	} {
		if err := size(field+"."+t.name, t.s, t.max, t.required, invalidBrief); err != nil {
			return err
		}
	}
	if len(x.Alternatives) < 2 || len(x.Alternatives) > 6 {
		return invalidBrief(field+".alternatives", "2 to 6")
	}
	for j := range x.Alternatives {
		a := &x.Alternatives[j]
		af := fmt.Sprintf("%s.alternatives[%d]", field, j)
		a.Label, a.Tradeoffs = cleanText(a.Label), cleanText(a.Tradeoffs)
		if err := chars(af+".label", a.Label, MaxLabel, true, invalidBrief); err != nil {
			return err
		}
		if err := size(af+".tradeoffs", a.Tradeoffs, 1<<10, true, invalidBrief); err != nil {
			return err
		}
	}
	if x.Recommendation == nil {
		return invalidBrief(field+".recommendation", "is required: an alternative's index, or -1 for none")
	}
	if r := *x.Recommendation; r < -1 || r >= len(x.Alternatives) {
		return invalidBrief(field+".recommendation", "an alternative's index, or -1 for none")
	}
	if err := refList(field+".blocked", x.Blocked, 0, 20); err != nil {
		return err
	}
	if err := refList(field+".evidence", x.Evidence, 1, 20); err != nil {
		return err
	}
	return revision(field+".subjectRevision", x.SubjectRevision)
}

// blocksAcyclic refuses draft tickets whose blocks make a cycle.
func blocksAcyclic(b *Brief) error {
	const (
		white = iota
		grey
		black
	)
	color := make([]int, len(b.Items))
	var visit func(i int) bool
	visit = func(i int) bool {
		color[i] = grey
		for _, k := range b.Items[i].DraftTicket.Blocks {
			switch color[k] {
			case grey:
				return false
			case white:
				if !visit(k) {
					return false
				}
			}
		}
		color[i] = black
		return true
	}
	for i, it := range b.Items {
		if it.Kind == ItemDraftTicket && color[i] == white && !visit(i) {
			return invalidBrief(fmt.Sprintf("items[%d].blocks", i), "draft tickets block one another in a cycle")
		}
	}
	return nil
}

// encode is v as the store writes it (store.Encode).
func encode(v any) ([]byte, error) { return store.Encode(v) }
