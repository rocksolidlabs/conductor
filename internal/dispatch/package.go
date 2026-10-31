package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
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
// builds.
const PackageVersion = 1

// Package is a launch's package, version PackageVersion: the ticket as it
// stood, the approved revision that launched it, the source references,
// the attachments (each with the file name it is written under in an
// attempt) and the comments the revision hands over, read at the launch,
// the constraints and decisions as the revision names them, and the
// launch itself. Encoded as the store encodes values (HTML escaping off),
// at most MaxPackage bytes.
type Package struct {
	Version     int                 `json:"version"`
	Ticket      PackageTicket       `json:"ticket"`
	Plan        PackagePlan         `json:"plan"`
	References  []PlanReference     `json:"references"`
	Attachments []PackageAttachment `json:"attachments"`
	Comments    []PackageComment    `json:"comments"`
	Constraints string              `json:"constraints"`
	Decisions   []PlanDecision      `json:"decisions"`
	Launch      PackageLaunch       `json:"launch"`
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
		Decisions:   body.Decisions,
		Launch:      launch,
	}
	if pkg.References == nil {
		pkg.References = []PlanReference{}
	}
	if pkg.Decisions == nil {
		pkg.Decisions = []PlanDecision{}
	}
	for i, a := range attachments {
		pkg.Attachments = append(pkg.Attachments, PackageAttachment{ID: a.ID, Name: a.Name, File: attachmentFile(i, a.Name), Size: a.Size, SHA256: a.SHA256, ContentType: a.ContentType})
	}
	for _, c := range comments {
		pkg.Comments = append(pkg.Comments, PackageComment{ID: c.ID, N: c.N, Author: c.Author, By: c.By, At: c.CreatedAt, Body: c.Body, SHA256: sha256Hex(c.Body)})
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
	return pt, nil
}

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
		{"the decisions are", pkg.Decisions}, {"the launch is", pkg.Launch},
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
