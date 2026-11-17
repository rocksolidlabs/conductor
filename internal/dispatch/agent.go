package dispatch

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/quoted"
)

// A ticket as an agent reads it (Program item IN1a,
// docs/plans/tracker-linking.md): text that a read of a linked item wrote
// reaches an agent only inside the mark internal/quoted writes, or not at
// all. Whether a text is quoted is the launch package's rule (readWrote):
// a title or a description is quoted when a read of a linked item ever
// wrote exactly that text into that field, or when the linked item's
// snapshot holds exactly it now; the quote names the item only in the
// second case, and otherwise says "an item outside Conductor". The text
// decides, never who owns the field: a person's edit, of one character
// even, makes the text the person's, and an unlink leaves a text a read
// wrote quoted. The admin's reads are not these: they hand every text as
// the store keeps it.
//
// The agents' reads are the session's read of its ticket (ForAgent), a
// comparison lane's read at its launch (AtLaunch), and Brakeman's shared
// reads of a ticket (ForAgent), of the list (TitlesForAgent), of a
// ticket's history (ChangesForAgent) and History's search
// (EntryWithheld).

// WithheldText stands, in what an agent reads of a ticket's history, for a
// value that a read of a linked item wrote or that cannot be told from
// one: a title or a description a read wrote, a snapshot, a text a field
// was taken over with. The ticket's own read quotes such a text while it
// is the ticket's.
const WithheldText = "(a text written outside Conductor, not shown here)"

// The words a quote of each field names its text by, as ticket.md's are.
const (
	quoteTitle       = "The title"
	quoteDescription = "The description"
	quoteRepro       = "The steps to reproduce"
)

// itemSource is where a text of the ticket tk was written, for the quote:
// its linked item as its snapshot names it when that item holds the text
// (ofItem) and the ticket is linked, else a source not known
// (quoted.Source{}), never another item's reference or author.
func itemSource(tk Ticket, ofItem bool) quoted.Source {
	sn, ok := ParseSnapshot(string(tk.ExternalSnapshot))
	if !ofItem || !ok || tk.ExternalID == "" {
		return quoted.Source{}
	}
	return quoted.Source{On: quoted.FromSystem(sn.System), Ref: sn.Space + "#" + sn.ID, Author: sn.Author}
}

// agentTicket is the ticket row tk as an agent reads it: its title as a
// quoted line and its description as a quoted block when a read wrote
// them (readWrote), and its snapshot as agentSnapshotOf makes it.
func agentTicket(ctx context.Context, q queryer, tk Ticket) (Ticket, error) {
	title, titleOfItem, err := readWrote(ctx, q, tk, FieldTitle)
	if err != nil {
		return Ticket{}, err
	}
	description, descriptionOfItem, err := readWrote(ctx, q, tk, FieldDescription)
	if err != nil {
		return Ticket{}, err
	}
	out := tk
	if title {
		out.Title = quoted.Line(itemSource(tk, titleOfItem), quoteTitle, tk.Title)
	}
	if description {
		out.Description = quoted.Block(itemSource(tk, descriptionOfItem), quoteDescription, tk.Description)
	}
	out.ExternalSnapshot = agentSnapshotOf(tk)
	return out, nil
}

// agentSnapshot is a linked item's snapshot as an agent reads it: where
// the item is (its page is the ticket's externalUrl), its kind, its state
// and its times, the digests and sizes
// of its texts, how many labels it has, the accounts that opened it and
// are assigned to it when each is an account's login, and its steps to
// reproduce quoted as a block. Nothing else of what was written on the
// item is in it: no title, label, milestone, base branch or image
// address. Each field is named here, so a field the snapshot gains is
// never handed on by default.
type agentSnapshot struct {
	V                 int       `json:"v"`
	System            string    `json:"system"`
	Host              string    `json:"host"`
	Space             string    `json:"space"`
	Kind              string    `json:"kind"`
	ID                string    `json:"id"`
	NodeID            string    `json:"nodeId"`
	BodySha256        string    `json:"bodySha256"`
	BodyBytes         int       `json:"bodyBytes"`
	BodyTruncated     bool      `json:"bodyTruncated,omitempty"`
	State             string    `json:"state"`
	StateReason       string    `json:"stateReason,omitempty"`
	Merged            bool      `json:"merged,omitempty"`
	MergedAt          time.Time `json:"mergedAt,omitzero"`
	MergeSha          string    `json:"mergeSha,omitempty"`
	HeadSha           string    `json:"headSha,omitempty"`
	LabelsTotal       int       `json:"labelsTotal"`
	Assignees         []string  `json:"assignees"`
	Author            string    `json:"author,omitempty"`
	CreatedAt         time.Time `json:"createdAt,omitzero"`
	UpdatedAt         time.Time `json:"updatedAt"`
	ClosedAt          time.Time `json:"closedAt,omitzero"`
	Repro             string    `json:"repro,omitempty"`
	ReproSha256       string    `json:"reproSha256,omitempty"`
	DescriptionSha256 string    `json:"descriptionSha256"`
}

// agentSnapshotOf is the ticket's snapshot as an agent reads it
// (agentSnapshot), nil for a ticket with none or one that cannot be read:
// each word of where the item is held to the characters a quote's
// reference is (quoted.Source.Clean), each account to a login's, every
// other word to its vocabulary, and the steps to reproduce quoted with
// the item as their source.
func agentSnapshotOf(tk Ticket) json.RawMessage {
	sn, ok := ParseSnapshot(string(tk.ExternalSnapshot))
	if !ok {
		return nil
	}
	word := func(s string) string {
		if s == "" {
			return ""
		}
		if c := (quoted.Source{Ref: s}).Clean().Ref; c != (quoted.Source{}).Clean().Ref {
			return c
		}
		return ""
	}
	pattern := func(ok bool, s string) string {
		if ok {
			return s
		}
		return ""
	}
	out := agentSnapshot{
		V: sn.V, System: pattern(SystemPattern.MatchString(sn.System), sn.System), Host: pattern(hostPattern.MatchString(sn.Host), sn.Host),
		Space: word(sn.Space), Kind: pattern(sn.Kind == ExternalIssue || sn.Kind == ExternalChange, sn.Kind), ID: word(sn.ID),
		NodeID: pattern(nodeIDPattern.MatchString(sn.NodeID), sn.NodeID), BodySha256: pattern(isHexDigest(sn.BodySha256), sn.BodySha256),
		BodyBytes: sn.BodyBytes, BodyTruncated: sn.BodyTruncated, State: pattern(slices.Contains(externalStates, sn.State), sn.State),
		StateReason: pattern(slices.Contains(externalReasons, sn.StateReason), sn.StateReason), Merged: sn.Merged, MergedAt: sn.MergedAt,
		MergeSha: pattern(objectIDPattern.MatchString(sn.MergeSha), sn.MergeSha), HeadSha: pattern(objectIDPattern.MatchString(sn.HeadSha), sn.HeadSha),
		LabelsTotal: max(sn.LabelsTotal, len(sn.Labels)), Assignees: []string{}, Author: loginOf(sn.Author), CreatedAt: sn.CreatedAt,
		UpdatedAt: sn.UpdatedAt, ClosedAt: sn.ClosedAt, ReproSha256: pattern(isHexDigest(sn.ReproSha256), sn.ReproSha256),
		DescriptionSha256: pattern(isHexDigest(sn.DescriptionSha256), sn.DescriptionSha256),
	}
	for _, a := range sn.Assignees {
		if a = loginOf(a); a != "" {
			out.Assignees = append(out.Assignees, a)
		}
	}
	if sn.Repro != "" {
		out.Repro = quoted.Block(itemSource(tk, true), quoteRepro, sn.Repro)
	}
	b, err := encodeValue(out)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}

// isHexDigest reports whether s is a SHA-256 in lower-case hex.
func isHexDigest(s string) bool {
	return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}

// ForAgent is the ticket v, as the store's Ticket read it, as an agent
// reads it, decided in one read transaction: its row through agentTicket
// (a title and a description a read wrote quoted, the snapshot as
// agentSnapshot holds it), its linked issue's read without the
// validators GitHub sent, and the title of each related ticket (its
// parent, its children, its relations' other ends) that a read wrote
// WithheldTitle, each decided by the title v holds, not by one read
// after it. Every other part is v's as it is.
func (s *Store) ForAgent(ctx context.Context, v TicketView) (TicketView, error) {
	var out TicketView
	err := s.read(ctx, func(q queryer) error {
		var err error
		out, err = forAgent(ctx, q, v)
		return err
	})
	return answer(out, err)
}

// forAgent is ForAgent inside a read.
func forAgent(ctx context.Context, q queryer, v TicketView) (TicketView, error) {
	tk, err := agentTicket(ctx, q, v.Ticket)
	if err != nil {
		return TicketView{}, err
	}
	v.Ticket = tk
	if r := v.ExternalRead; r != nil {
		c := *r
		c.IssueETag, c.PullETag = "", ""
		v.ExternalRead = &c
	}
	related := func(r RelatedTicket) (RelatedTicket, error) {
		w, err := relatedTitleWrote(ctx, q, r.ID, r.Title)
		if w {
			r.Title = WithheldTitle
		}
		return r, err
	}
	if v.Parent != nil {
		p, err := related(*v.Parent)
		if err != nil {
			return TicketView{}, err
		}
		v.Parent = &p
	}
	v.Children.Tickets = slices.Clone(v.Children.Tickets)
	for i, c := range v.Children.Tickets {
		if v.Children.Tickets[i], err = related(c); err != nil {
			return TicketView{}, err
		}
	}
	v.Relations = slices.Clone(v.Relations)
	for i, r := range v.Relations {
		if v.Relations[i].Other, err = related(r.Other); err != nil {
			return TicketView{}, err
		}
	}
	return v, nil
}

// relatedTitleWrote reports whether title, a related ticket's as a read
// gave it, is one a read of a linked item wrote into the title of the
// ticket with the id (readWrote of that ticket holding title); true for
// an id the store does not hold, whose writer cannot be told.
func relatedTitleWrote(ctx context.Context, q queryer, id, title string) (bool, error) {
	if title == "" {
		return false, nil
	}
	tk, found, err := linkOf(ctx, q, id)
	if err != nil || !found {
		return !found, err
	}
	tk.Title = title
	w, _, err := readWrote(ctx, q, tk, FieldTitle)
	return w, err
}

// linkOf is the ticket with the id as readWrote reads it: its id, its
// linked item's identity and its snapshot; found false for none.
func linkOf(ctx context.Context, q queryer, id string) (tk Ticket, found bool, err error) {
	var snapshot string
	switch err := q.QueryRowContext(ctx, "SELECT id, external_id, external_snapshot FROM tickets WHERE id = ?", id).Scan(&tk.ID, &tk.ExternalID, &snapshot); {
	case isNoRows(err):
		return Ticket{}, false, nil
	case err != nil:
		return Ticket{}, false, err
	}
	tk.ExternalSnapshot = rawJSON(snapshot)
	return tk, true, nil
}

// TitlesForAgent is a page of the list's rows as an agent reads them,
// decided in one read transaction: the title of each that a read wrote
// (readWrote of its ticket holding the row's title) a quoted line, as its
// own read quotes it; a row whose ticket the store does not hold has its
// title WithheldTitle. Every other field is the row's.
func (s *Store) TitlesForAgent(ctx context.Context, rows []TicketSummary) ([]TicketSummary, error) {
	out := slices.Clone(rows)
	err := s.read(ctx, func(q queryer) error {
		for i, r := range out {
			if r.Title == "" {
				continue
			}
			tk, found, err := linkOf(ctx, q, r.ID)
			if err != nil {
				return err
			}
			if !found {
				out[i].Title = WithheldTitle
				continue
			}
			tk.Title = r.Title
			w, ofItem, err := readWrote(ctx, q, tk, FieldTitle)
			if err != nil {
				return err
			}
			if w {
				out[i].Title = quoted.Line(itemSource(tk, ofItem), quoteTitle, r.Title)
			}
		}
		return nil
	})
	return answer(out, err)
}

// ChangesForAgent is a page of the history of the ticket with the key as
// an agent reads it, decided in one read transaction: of the ticket's own
// rows, a snapshot's value and prior, a taken-over field's text (an adopt
// row's value), a read's text (an observe row's value) and every title or
// description that a read wrote (readWrote of the ticket holding it) are
// WithheldText; so is a publication's body in its create row
// (publicationForAgent); every other row and field is the page's.
// ErrNotFound when there is no such ticket.
//
// The other rows a ticket's history holds carry no text a read of a linked
// item wrote: a comment's, a link's note, a plan revision's crew, notes and
// pins, a launch's label (the key alone for a quoted title), a review's
// and a finding's words and marks, a capture's and its send's, an
// attachment's name (an imported image's is github-<n>.<ext>) are a
// person's, an agent's or Conductor's; a publication's later rows are its
// state, its tries, its ids, its login, its addresses and Conductor's own
// words; the ticket's other fields are ids, vocabularies, times and a
// person's text.
func (s *Store) ChangesForAgent(ctx context.Context, key string, rows []ChangeRow) ([]ChangeRow, error) {
	out := slices.Clone(rows)
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		wrote := func(field, text string) (bool, error) {
			c := tk
			c.Title, c.Description = text, text
			w, _, err := readWrote(ctx, q, c, field)
			return w, err
		}
		for i, c := range out {
			if c.Entity == EntityPublication && c.Op == OpCreate {
				out[i].Value = publicationForAgent(c.Value)
				continue
			}
			if c.Entity != EntityTicket {
				continue
			}
			value, prior := false, false
			switch c.Field {
			case "external_snapshot":
				value, prior = true, true
			case FieldTitle, FieldDescription:
				value = c.Op == OpObserve || c.Op == OpAdopt
				if !value {
					if value, err = wrote(c.Field, c.Value); err != nil {
						return err
					}
				}
				if c.Op != OpAdopt {
					// An adopt row's prior is the system that owned the
					// field, not a text.
					if prior, err = wrote(c.Field, c.Prior); err != nil {
						return err
					}
				}
			}
			if value && c.Value != "" {
				out[i].Value = WithheldText
			}
			if prior && c.Prior != "" {
				out[i].Prior = WithheldText
			}
		}
		return nil
	})
	return answer(out, err)
}

// EntryWithheld says what an agent's read of History's entry of the
// ticket with the key, made at the ticket's revision rev ("rev <n>", as
// the entry names it), withholds: its title when a read wrote the title,
// its words (the description it is searched by) when a read wrote the
// description, both when the entry is of another revision than the
// ticket's now or the ticket is not in the store, since the text the
// entry holds cannot then be told; neither for a ticket no read of a
// linked item ever wrote into and that has no linked item now.
func (s *Store) EntryWithheld(ctx context.Context, key, rev string) (title, words bool, err error) {
	err = s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			title, words = true, true
			return nil
		}
		if err != nil {
			return err
		}
		touched := tk.ExternalID != ""
		if !touched {
			if err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM changes WHERE entity = ? AND entity_id = ? AND op IN (?, ?))",
				EntityTicket, tk.ID, OpObserve, OpAdopt).Scan(&touched); err != nil {
				return err
			}
		}
		if !touched {
			return nil
		}
		if n, ok := strings.CutPrefix(rev, "rev "); !ok || n != strconv.FormatInt(tk.Rev, 10) {
			title, words = true, true
			return nil
		}
		if title, _, err = readWrote(ctx, q, tk, FieldTitle); err != nil {
			return err
		}
		words, _, err = readWrote(ctx, q, tk, FieldDescription)
		return err
	})
	return title, words, err
}

// launchedForAgent is the title and the description of a launch's
// package (pt, with its source src, nil for a version 1 package or one
// whose ticket has none) as an agent reads them, the lane's ticket: each
// quoted when the package's source says a read wrote it, or when a read
// wrote that text into the field of the ticket tk as the store holds it
// now (readWrote), so a package that cannot say (version 1's) is judged
// by the text; the quote names the package's item only when the source
// says the item held that text at the launch, as ticket.md's quote does,
// else a source not known.
func launchedForAgent(ctx context.Context, q queryer, tk Ticket, pt PackageTicket, src *PackageSource) (title, description string, err error) {
	title, description = pt.Title, pt.Description
	for _, f := range []struct {
		field, what string
		text        *string
		flagged     bool
		ofItem      bool
	}{
		{FieldTitle, quoteTitle, &title, src != nil && src.TitleFromGitHub, src != nil && src.TitleFromItem},
		{FieldDescription, quoteDescription, &description, src != nil && src.DescriptionFromGitHub, src != nil && src.DescriptionFromItem},
	} {
		w := f.flagged
		if !w {
			c := tk
			c.Title, c.Description = *f.text, *f.text
			if w, _, err = readWrote(ctx, q, c, f.field); err != nil {
				return "", "", err
			}
		}
		if !w {
			continue
		}
		var qs quoted.Source
		if f.ofItem && !src.Unlinked {
			qs = quoted.Source{On: quoted.FromSystem(src.System), Ref: src.Ref, Author: src.Author}
		}
		if f.field == FieldTitle {
			*f.text = quoted.Line(qs, f.what, *f.text)
		} else {
			*f.text = quoted.Block(qs, f.what, *f.text)
		}
	}
	return title, description, nil
}

// publicationForAgent is a publication's create row's value as an agent
// reads it: its body WithheldText, since a body holds text a person chose
// from the parts the server offered (the key part is the ticket's title,
// which a read may have written) and the store cannot tell which; every
// other field (its target, its state, its parts' names, its login) the
// row's. A value that does not read as a publication is WithheldText
// whole.
func publicationForAgent(value string) string {
	var p Publication
	if err := json.Unmarshal([]byte(value), &p); err != nil {
		return WithheldText
	}
	if p.Body != "" {
		p.Body = WithheldText
	}
	b, err := encodeValue(p)
	if err != nil {
		return WithheldText
	}
	return b
}
