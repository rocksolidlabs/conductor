package dispatch

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// A launch's package (Program item DS2, docs/plans/dispatch-evidence.md,
// "The package"): what a run launched from a ticket is handed, built once
// from the rows inside the launch's transaction, so it is one state of the
// ticket, and never written again. Its SHA-256 is recorded on the launch,
// the engine checks its bytes against it, and the ledger keeps them with
// the run, so every attempt of the run and of its continuations gets the
// same ticket.md, package.json and attachments whatever the ticket became
// since. The crew is not in it: the run keeps the crew as launched, and
// the revision's body has it.

// PackageVersion is the version of the package's shape this binary
// builds. Version 2 (Program item IN1a) adds the source and the evidence;
// a package version 1 built keeps its version, and the engine reads both.
const PackageVersion = 2

// Package is a launch's package, version PackageVersion: the ticket as it
// stood, the approved revision that launched it, the source references,
// the attachments (each with the file name it is written under in an
// attempt) and the comments the revision hands over, read at the launch,
// the constraints and decisions as the revision names them, the launch
// itself, the ticket's linked item (Source, left out for none) and the
// links the revision names as evidence (left out for none), so a package
// with neither is version 1's but for its version. Encoded as the store
// encodes values (HTML escaping off), at most MaxPackage bytes.
type Package struct {
	Version     int                 `json:"version"`
	Ticket      PackageTicket       `json:"ticket"`
	Plan        PackagePlan         `json:"plan"`
	References  []PlanReference     `json:"references"`
	Attachments []PackageAttachment `json:"attachments"`
	Comments    []PackageComment    `json:"comments"`
	Constraints string              `json:"constraints"`
	Decisions   []PackageDecision   `json:"decisions"`
	Launch      PackageLaunch       `json:"launch"`
	Source      *PackageSource      `json:"source,omitempty"`
	Evidence    []PackageEvidence   `json:"evidence,omitempty"`
}

// PackageSource is the ticket's linked item at the launch: its system,
// host, page, reference (owner/repo#n) and node id, its state then, when
// the ticket last read it (zero for never), the SHA-256 of the snapshot
// the package was built from, the account that wrote it, which of the
// ticket's title and description a read of it wrote last (the text
// ticket.md quotes as written there: lastWrite), and its steps to
// reproduce when the revision hands them over. Unlinked says the ticket
// was unlinked before the launch with a text a read of the item wrote
// still in it: the source is then the item as that read left it. At most
// MaxPackageSource as encoded.
type PackageSource struct {
	Unlinked              bool      `json:"unlinked,omitempty"`
	System                string    `json:"system"`
	Host                  string    `json:"host"`
	URL                   string    `json:"url"`
	Ref                   string    `json:"ref"`
	NodeID                string    `json:"nodeId"`
	State                 string    `json:"state"`
	StateReason           string    `json:"stateReason,omitempty"`
	ReadAt                time.Time `json:"readAt,omitzero"`
	SnapshotSha256        string    `json:"snapshotSha256"`
	Author                string    `json:"author,omitempty"`
	TitleFromGitHub       bool      `json:"titleFromGitHub"`
	DescriptionFromGitHub bool      `json:"descriptionFromGitHub"`
	Repro                 string    `json:"repro,omitempty"`
}

// PackageEvidence is a link the revision names as evidence, read at the
// launch: its page, and its note cut to MaxEvidenceNote characters with
// "…".
type PackageEvidence struct {
	URL  string `json:"url"`
	Note string `json:"note"`
}

// PackageDecision is a decision of the revision as its package names it:
// its id, question, kind and the tasks it comes before, encoded as
// PlanDecision is without its for (as the package was before HM1). Whom it is for
// (PlanDecision.For, HM1) is not in the package: the engine is handed it
// from the revision's body beside the package (crew.Planned.For), so the
// package keeps version 1's shape, which every build reads, and an older
// build that continues a run of this one is never handed a label it would
// drop or refuse.
type PackageDecision struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Kind     string   `json:"kind"`
	Before   []string `json:"before"`
}

// PackageTicket is the ticket as it stood at the launch: its row's fields
// a run reads, its labels by name, its milestone, its parent and the live
// tickets that block it (at most MaxPackageBlockers, the open ones first,
// BlockersTotal counting them all), every reference read as live or not
// as the ticket's read takes it (a deleted label, milestone or parent is
// none), and its intent digest and criteria's SHA-256 then.
type PackageTicket struct {
	ID            string            `json:"id"`
	Key           string            `json:"key"`
	Rev           int64             `json:"rev"`
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	Acceptance    string            `json:"acceptance"`
	Status        string            `json:"status"`
	Priority      int               `json:"priority"`
	Horizon       string            `json:"horizon"`
	Owner         string            `json:"owner"`
	Assignees     []string          `json:"assignees"`
	Labels        []string          `json:"labels"`
	Milestone     *PackageMilestone `json:"milestone,omitempty"`
	Parent        *PackageRelated   `json:"parent,omitempty"`
	Blockers      []PackageRelated  `json:"blockers"`
	BlockersTotal int               `json:"blockersTotal"`
	Intent        string            `json:"intent"`
	Criteria      string            `json:"criteria"`
}

// PackageMilestone is a package's ticket's milestone: its name and its
// target date (YYYY-MM-DD, or none).
type PackageMilestone struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

// PackageRelated is a ticket a package's ticket names (its parent, a
// blocker): its key, title and status.
type PackageRelated struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// PackagePlan is the revision that launched the run: its id, its number,
// its body's digest, the revision it was based on, the approval (when,
// by whom, its note), the saved crew it was taken from, the base, the yolo
// choice fixed in it, and the agents its approval covers, each an agent's
// id and the SHA-256 of its selection as the body records it, which every
// attempt's session is checked against (the API's agentSelection).
type PackagePlan struct {
	ID         string         `json:"id"`
	N          int            `json:"n"`
	Digest     string         `json:"digest"`
	BasedOn    string         `json:"basedOn"`
	ApprovedAt time.Time      `json:"approvedAt"`
	ApprovedBy string         `json:"approvedBy"`
	Note       string         `json:"note"`
	Crew       PackageCrew    `json:"crew"`
	Base       string         `json:"base"`
	Yolo       bool           `json:"yolo"`
	Agents     []PackageAgent `json:"agents"`
}

// PackageCrew is the saved crew a revision was taken from: its id, its
// name, and when it was saved then.
type PackageCrew struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// PackageAgent is an agent a revision's approval covers: its id and the
// SHA-256 of its selection.
type PackageAgent struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

// PackageAttachment is an attachment the revision hands over, read at the
// launch: its id, its display name, File (the name it is written under in
// an attempt, derived and never the display name: attachmentFile), its
// size, its content's SHA-256 and its content type.
type PackageAttachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	File        string `json:"file"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"contentType"`
}

// PackageComment is a comment the revision hands over, read at the
// launch: its id, its number, its author and display name, when it was
// made, its body and the body's SHA-256 (the one the revision recorded,
// which the launch checked).
type PackageComment struct {
	ID     string    `json:"id"`
	N      int       `json:"n"`
	Author string    `json:"author"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	Body   string    `json:"body"`
	SHA256 string    `json:"sha256"`
}

// PackageLaunch is the launch: its id, the run id minted for it, the
// run's label, and when and by whom it was made.
type PackageLaunch struct {
	ID    string    `json:"id"`
	Run   string    `json:"run"`
	Label string    `json:"label"`
	At    time.Time `json:"at"`
	By    string    `json:"by"`
}

// attachmentFile is the name the attachment at place i (0-based) of a
// revision's attachments is written under in an attempt: NN-<portable>,
// NN its place from 01, and <portable> its display name with every byte
// outside [A-Za-z0-9._-] made '_', cut to MaxAttachmentFile bytes, its
// leading and trailing dots trimmed ('_' when nothing is left). The
// places make the names distinct whatever a file system folds (case,
// Unicode normalization); they are ASCII; none is a name Windows reserves
// (CON, aux.txt: none begins with two digits and a dash) or ends in a dot
// or a space. The engine checks the shape again before it opens anything.
func attachmentFile(i int, name string) string {
	b := []byte(name)
	for j, c := range b {
		if !portableByte(c) {
			b[j] = '_'
		}
	}
	if len(b) > MaxAttachmentFile {
		b = b[:MaxAttachmentFile]
	}
	portable := strings.Trim(string(b), ".")
	if portable == "" {
		portable = "_"
	}
	return fmt.Sprintf("%02d-%s", i+1, portable)
}

// portableByte reports whether c is in [A-Za-z0-9._-].
func portableByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
}

// planCrewYolo is what a package reads of a revision's crew beyond
// planCrew: its yolo choice, fixed in the body when the revision was made.
type planCrewYolo struct {
	Yolo bool `json:"yolo"`
}

// buildPackage builds the package of a launch inside its transaction:
// the ticket tk as it stands (its labels by name, its milestone, its
// parent and its blockers read now), the approved revision p and its
// body, the attachments and comments the body hands over (read and checked
// live by the caller, in the body's order), and the launch. It returns the
// package encoded, or bound (naming its largest part) past MaxPackage.
func (t *tx) buildPackage(tk Ticket, p PlanSummary, body PlanBody, crew planCrew, attachments []Attachment, comments []Comment, launch PackageLaunch) (string, error) {
	pt, err := packageTicket(t.ctx, t.tx, tk)
	if err != nil {
		return "", err
	}
	var yolo planCrewYolo
	if err := json.Unmarshal(body.Crew, &yolo); err != nil {
		return "", fmt.Errorf("dispatch: the crew of plan revision %s: %w", p.ID, err)
	}
	agents := make([]PackageAgent, 0, len(body.Agents))
	for _, a := range body.Agents {
		agents = append(agents, PackageAgent{ID: a.ID, SHA256: a.SHA256})
	}
	pkg := Package{
		Version: PackageVersion,
		Ticket:  pt,
		Plan: PackagePlan{
			ID: p.ID, N: p.N, Digest: p.Digest, BasedOn: p.BasedOn, ApprovedAt: p.ApprovedAt, ApprovedBy: p.ApprovedBy, Note: p.ApprovedNote,
			Crew: PackageCrew{ID: crew.ID, Name: crew.Name, UpdatedAt: crew.UpdatedAt.UTC()}, Base: body.Base, Yolo: yolo.Yolo, Agents: agents,
		},
		References:  body.References,
		Attachments: make([]PackageAttachment, 0, len(attachments)),
		Comments:    make([]PackageComment, 0, len(comments)),
		Constraints: body.Constraints,
		Decisions:   make([]PackageDecision, 0, len(body.Decisions)),
		Launch:      launch,
	}
	if pkg.References == nil {
		pkg.References = []PlanReference{}
	}
	for _, d := range body.Decisions {
		pkg.Decisions = append(pkg.Decisions, PackageDecision{ID: d.ID, Question: d.Question, Kind: d.Kind, Before: d.Before})
	}
	for i, a := range attachments {
		pkg.Attachments = append(pkg.Attachments, PackageAttachment{ID: a.ID, Name: a.Name, File: attachmentFile(i, a.Name), Size: a.Size, SHA256: a.SHA256, ContentType: a.ContentType})
	}
	for _, c := range comments {
		pkg.Comments = append(pkg.Comments, PackageComment{ID: c.ID, N: c.N, Author: c.Author, By: c.By, At: c.CreatedAt, Body: c.Body, SHA256: sha256Hex(c.Body)})
	}
	if pkg.Source, err = packageSource(t.ctx, t.tx, tk, body.Source); err != nil {
		return "", err
	}
	if pkg.Evidence, err = packageEvidence(t.ctx, t.tx, tk, body.Source); err != nil {
		return "", err
	}
	if err := checkParts(pkg); err != nil {
		return "", err
	}
	encoded, err := encodeValue(pkg)
	if err != nil {
		return "", err
	}
	if len(encoded) > MaxPackage {
		return "", packageBound(pkg, len(encoded))
	}
	return encoded, nil
}

// packageSource is the package's source. Whether the title and the
// description are flagged as written on a linked item's system is who
// last wrote each (lastWrite), whoever owns the field now: an adoption or
// an unlink keeps the text and changes nothing of it. For a linked ticket
// it is the item as the ticket's snapshot and read hold it, with the steps
// to reproduce when the revision hands them over (src.Repro, which the
// caller has checked are the snapshot's: sourceStale). For a ticket with
// no linked item whose title or description a read wrote last, it is the
// item as that read left it, Unlinked; for one with neither, nil. A
// snapshot that cannot be read is the store's error, never a package
// without its source.
func packageSource(ctx context.Context, q queryer, tk Ticket, src *PlanSource) (*PackageSource, error) {
	title, err := lastWrite(ctx, q, tk.ID, FieldTitle)
	if err != nil {
		return nil, err
	}
	description, err := lastWrite(ctx, q, tk.ID, FieldDescription)
	if err != nil {
		return nil, err
	}
	raw, unlinked := string(tk.ExternalSnapshot), tk.ExternalID == ""
	if unlinked {
		if raw = cmp.Or(title, description); raw == "" {
			return nil, nil
		}
	}
	sn, ok := ParseSnapshot(raw)
	if !ok {
		return nil, fmt.Errorf("dispatch: the snapshot of %s's linked item cannot be read", tk.Key)
	}
	ps := &PackageSource{
		System: sn.System, Host: sn.Host, URL: sn.URL, Ref: sn.Space + "#" + sn.ID, NodeID: sn.NodeID, State: sn.State, StateReason: sn.StateReason,
		SnapshotSha256: sha256Hex(raw), Author: sn.Author, Unlinked: unlinked,
		TitleFromGitHub: title != "" && tk.Title != "", DescriptionFromGitHub: description != "" && tk.Description != "",
	}
	if unlinked {
		return ps, nil
	}
	ps.URL = tk.ExternalURL
	if src != nil && src.Repro != nil {
		ps.Repro = sn.Repro
	}
	read, err := ticketReadOf(ctx, q, tk.ID)
	if err != nil {
		return nil, err
	}
	if read != nil && read.ExternalID == tk.ExternalID {
		ps.ReadAt = read.ReadAt
	}
	return ps, nil
}

// lastWrite is who last wrote the ticket's field, read from its own
// history in the log's one order: when it is a read of a linked item (an
// observe row: an import, a refresh, a field given back), the item's
// snapshot as the ticket held it at that read; "" when a person in
// Dispatch wrote it (a set row) or nothing did. An adopt row keeps the
// text and writes nothing, nor does an unlink.
func lastWrite(ctx context.Context, q queryer, ticketID, field string) (string, error) {
	var op, at, actor, opID string
	switch err := q.QueryRowContext(ctx, `SELECT op, at, actor, op_id FROM changes WHERE entity = ? AND entity_id = ? AND field = ? AND op IN (?, ?)
		ORDER BY at DESC, actor DESC, op_id DESC, n DESC LIMIT 1`, EntityTicket, ticketID, field, OpSet, OpObserve).Scan(&op, &at, &actor, &opID); {
	case isNoRows(err) || (err == nil && op != OpObserve):
		return "", nil
	case err != nil:
		return "", err
	}
	// The snapshot that read wrote, in its operation or before it.
	var snapshot string
	err := q.QueryRowContext(ctx, `SELECT value FROM changes WHERE entity = ? AND entity_id = ? AND field = 'external_snapshot' AND op = ? AND value != ''
		AND (at, actor, op_id) <= (?, ?, ?) ORDER BY at DESC, actor DESC, op_id DESC, n DESC LIMIT 1`, EntityTicket, ticketID, OpSet, at, actor, opID).Scan(&snapshot)
	if isNoRows(err) {
		return "", fmt.Errorf("dispatch: the read that wrote the ticket %s's %s has no snapshot in its history", ticketID, field)
	}
	return snapshot, err
}

// titleReadWrote reports whether a read of a linked item last wrote the
// title of the ticket with the key (lastWrite); false for a key not in
// the store.
func titleReadWrote(ctx context.Context, q queryer, key string) (bool, error) {
	var id string
	switch err := q.QueryRowContext(ctx, "SELECT id FROM tickets WHERE key = ?", key).Scan(&id); {
	case isNoRows(err):
		return false, nil
	case err != nil:
		return false, err
	}
	snapshot, err := lastWrite(ctx, q, id, FieldTitle)
	return snapshot != "", err
}

// checkParts holds the package's source and evidence to the rules the
// engine reads them by (internal/crew, validateSource and readPackage),
// so a launch Dispatch commits is never one the engine refuses: the
// source names its system, its page and each evidence link's are http or
// https URLs as a link's are (checkField's link.url), its state and state
// reason are a snapshot's words, and the source and the evidence as
// encoded are each at most their bound (MaxPackageSource,
// MaxPackageEvidenceJSON: bound, in words naming the part).
func checkParts(pkg Package) error {
	if s := pkg.Source; s != nil {
		if s.System == "" || s.URL == "" || !slices.Contains(externalStates, s.State) || !slices.Contains(externalReasons, s.StateReason) {
			return refuse(RefusalInvalid, "the linked item's snapshot names no system, no page or a state of its own: refresh it")
		}
		if err := checkField("link.url", "the linked item's page", s.URL); err != nil {
			return err
		}
	}
	for _, e := range pkg.Evidence {
		if err := checkField("link.url", "an evidence link's page", e.URL); err != nil {
			return err
		}
	}
	for _, p := range []struct {
		name string
		v    any
		max  int
	}{{"the source (the linked item and its steps to reproduce) is", pkg.Source, MaxPackageSource}, {"the evidence is", pkg.Evidence, MaxPackageEvidenceJSON}} {
		v, err := encodeValue(p.v)
		if err != nil {
			return err
		}
		if len(v) > p.max {
			return refuse(RefusalBound, "%s %s as the package writes it, more than the %s a package holds", p.name, sizeWords(len(v)), sizeWords(p.max))
		}
	}
	return nil
}

// packageEvidence is the links the revision names as evidence, in its
// order, each live (the caller has checked: sourceStale), whatever the
// ticket's linked item: each link's page (its ref when it has none) and
// its note cut to MaxEvidenceNote characters.
func packageEvidence(ctx context.Context, q queryer, tk Ticket, src *PlanSource) ([]PackageEvidence, error) {
	out := []PackageEvidence{}
	if src == nil {
		return out, nil
	}
	for _, id := range src.Evidence {
		var ref, page, note string
		if err := q.QueryRowContext(ctx, "SELECT ref, url, note FROM links WHERE id = ? AND ticket_id = ?", id, tk.ID).Scan(&ref, &page, &note); err != nil {
			return nil, err
		}
		if utf8.RuneCountInString(note) > MaxEvidenceNote {
			note = cutChars(note, MaxEvidenceNote-1) + "…"
		}
		out = append(out, PackageEvidence{URL: cmp.Or(page, ref), Note: note})
	}
	return out, nil
}

// packageTicket reads the package's part of the ticket tk inside the
// launch's transaction: its row as tk holds it, its assignees, its live
// labels by name, its live milestone, its live parent, and the live
// tickets that block it, the open ones first.
func packageTicket(ctx context.Context, q queryer, tk Ticket) (PackageTicket, error) {
	pt := PackageTicket{
		ID: tk.ID, Key: tk.Key, Rev: tk.Rev, Title: tk.Title, Description: tk.Description, Acceptance: tk.Acceptance, Status: tk.Status,
		Priority: tk.Priority, Horizon: tk.Horizon, Owner: tk.Owner, Intent: IntentDigest(tk), Criteria: criteriaHash(tk.Acceptance),
		Blockers: []PackageRelated{},
	}
	var err error
	if pt.Assignees, err = readTexts(ctx, q, "SELECT actor FROM ticket_assignees WHERE ticket_id = ? ORDER BY actor", tk.ID); err != nil {
		return PackageTicket{}, err
	}
	if pt.Labels, err = readTexts(ctx, q, `SELECT labels.name FROM ticket_labels JOIN labels ON labels.id = ticket_labels.label_id
		WHERE ticket_labels.ticket_id = ? AND labels.deleted_at = '' ORDER BY labels.name, labels.id`, tk.ID); err != nil {
		return PackageTicket{}, err
	}
	if tk.MilestoneID != "" {
		var m PackageMilestone
		switch err := q.QueryRowContext(ctx, "SELECT name, target FROM milestones WHERE id = ? AND deleted_at = ''", tk.MilestoneID).Scan(&m.Name, &m.Target); {
		case err == nil:
			pt.Milestone = &m
		case !isNoRows(err):
			return PackageTicket{}, err
		}
	}
	if tk.ParentID != "" {
		var r PackageRelated
		switch err := q.QueryRowContext(ctx, "SELECT key, title, status FROM tickets WHERE id = ? AND deleted_at = ''", tk.ParentID).Scan(&r.Key, &r.Title, &r.Status); {
		case err == nil:
			pt.Parent = &r
		case !isNoRows(err):
			return PackageTicket{}, err
		}
	}
	const blockers = `FROM tickets b WHERE b.deleted_at = '' AND b.id IN (SELECT from_id FROM relations WHERE to_id = ? AND kind = ? AND deleted_at = '')`
	rows, err := q.QueryContext(ctx, "SELECT b.key, b.title, b.status "+blockers+" ORDER BY b.status IN (?, ?), b.number LIMIT ?",
		tk.ID, RelationBlocks, TicketDone, TicketCancelled, MaxPackageBlockers)
	if err != nil {
		return PackageTicket{}, err
	}
	for rows.Next() {
		var r PackageRelated
		if err := rows.Scan(&r.Key, &r.Title, &r.Status); err != nil {
			rows.Close()
			return PackageTicket{}, err
		}
		pt.Blockers = append(pt.Blockers, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PackageTicket{}, err
	}
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) "+blockers, tk.ID, RelationBlocks).Scan(&pt.BlockersTotal); err != nil {
		return PackageTicket{}, err
	}
	// A related ticket's title a read of its linked item wrote last is
	// WithheldTitle: the package quotes no ticket's text but its own.
	if pt.Parent != nil {
		if w, err := titleReadWrote(ctx, q, pt.Parent.Key); err != nil {
			return PackageTicket{}, err
		} else if w {
			pt.Parent.Title = WithheldTitle
		}
	}
	for i, b := range pt.Blockers {
		if w, err := titleReadWrote(ctx, q, b.Key); err != nil {
			return PackageTicket{}, err
		} else if w {
			pt.Blockers[i].Title = WithheldTitle
		}
	}
	return pt, nil
}

// WithheldTitle stands, in a package, for the title of a related ticket
// (its parent, a blocker) that a read of the related ticket's linked item
// wrote last.
const WithheldTitle = "(a title written outside Conductor, not shown here)"

// packageBound is the bound refusal of a package of total bytes as
// encoded, past MaxPackage: its words name the package's largest part and
// its size ("the comments are 300.0 KiB of the package's 520.0 KiB").
func packageBound(pkg Package, total int) error {
	parts := []struct {
		name string
		v    any
	}{
		{"the ticket is", pkg.Ticket}, {"the plan revision is", pkg.Plan}, {"the references are", pkg.References},
		{"the attachments are", pkg.Attachments}, {"the comments are", pkg.Comments}, {"the constraints are", pkg.Constraints},
		{"the decisions are", pkg.Decisions}, {"the launch is", pkg.Launch}, {"the source is", pkg.Source}, {"the evidence is", pkg.Evidence},
	}
	largest, size := "", -1
	for _, p := range parts {
		v, err := encodeValue(p.v)
		if err != nil {
			return err
		}
		if len(v) > size {
			largest, size = p.name, len(v)
		}
	}
	return refuse(RefusalBound, "%s %s of the package's %s, more than the %s a package holds", largest, sizeWords(size), sizeWords(total), sizeWords(MaxPackage))
}
