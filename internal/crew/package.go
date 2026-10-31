package crew

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// A launch package (docs/plans/dispatch-evidence.md, Program item DS2): what
// a run launched from a Dispatch ticket's plan revision was handed, built by
// Dispatch in the launch's transaction, frozen and hashed. The engine keeps
// it byte for byte, on the run and in the ledger's row (RunRow.Package), so
// every attempt of the run and of its continuations is handed the same
// package; this is the engine's own reading of it: the header it needs (the
// ticket, the plan revision, the launch), the attachments to copy and what
// ticket.md renders. internal/crew never imports internal/dispatch: the
// shapes below are the package's as JSON, which the API hands over as
// bytes.

// PackageVersion is the version of the launch package this engine reads.
const PackageVersion = 1

// Bounds of a package's parts as the engine reads them, Dispatch's own
// (the plan's owner decision 8): a package past one was not built by
// Dispatch and is refused.
const (
	maxPackageAttachments = 8
	maxPackageAgents      = 12
	maxPackageReferences  = 20
	maxPackageComments    = 20
	maxPackageDecisions   = 10
	maxPackageBlockers    = 20
	// maxTicketRef bounds an id of the package's (ticket, plan, launch,
	// attachment, comment) and a ticket's key.
	maxTicketRef = 64
)

// Package is a launch package as JSON, version 1.
type Package struct {
	Version     int                 `json:"version"`
	Ticket      PackageTicket       `json:"ticket"`
	Plan        PackagePlan         `json:"plan"`
	References  []PackageReference  `json:"references,omitempty"`
	Attachments []PackageAttachment `json:"attachments,omitempty"`
	Comments    []PackageComment    `json:"comments,omitempty"`
	Constraints string              `json:"constraints,omitempty"`
	Decisions   []PackageDecision   `json:"decisions,omitempty"`
	Launch      PackageLaunch       `json:"launch"`
}

// PackageTicket is the ticket as it stood at the launch: its intent (the
// title, the description and the acceptance criteria, Intent their digest
// and Criteria the criteria's), its fields, and the live tickets that
// block it (at most 20 of BlockersTotal).
type PackageTicket struct {
	ID            string             `json:"id"`
	Key           string             `json:"key"`
	Rev           int64              `json:"rev"`
	Title         string             `json:"title"`
	Description   string             `json:"description,omitempty"`
	Acceptance    string             `json:"acceptance,omitempty"`
	Status        string             `json:"status,omitempty"`
	Priority      int                `json:"priority,omitempty"`
	Horizon       string             `json:"horizon,omitempty"`
	Owner         string             `json:"owner,omitempty"`
	Assignees     []string           `json:"assignees,omitempty"`
	Labels        []string           `json:"labels,omitempty"`
	Milestone     *PackageMilestone  `json:"milestone,omitempty"`
	Parent        *PackageTicketRef  `json:"parent,omitempty"`
	Blockers      []PackageTicketRef `json:"blockers,omitempty"`
	BlockersTotal int                `json:"blockersTotal,omitempty"`
	Intent        string             `json:"intent,omitempty"`
	Criteria      string             `json:"criteria,omitempty"`
}

// PackageMilestone is a ticket's milestone: its name and target date.
type PackageMilestone struct {
	Name   string `json:"name"`
	Target string `json:"target,omitempty"`
}

// PackageTicketRef is another ticket named by the package: a parent or a
// blocker, by its key, with its title and status.
type PackageTicketRef struct {
	Key    string `json:"key"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
}

// PackagePlan is the approved plan revision the run was launched from: its
// id, its number among the ticket's revisions, its body's digest, the
// approval, the saved crew it was taken from, its base and yolo choice,
// and the agents' approved selections by their SHA-256.
type PackagePlan struct {
	ID         string         `json:"id"`
	N          int            `json:"n"`
	Digest     string         `json:"digest"`
	BasedOn    string         `json:"basedOn,omitempty"`
	ApprovedAt time.Time      `json:"approvedAt,omitzero"`
	ApprovedBy string         `json:"approvedBy,omitempty"`
	Note       string         `json:"note,omitempty"`
	Crew       PackageCrew    `json:"crew"`
	Base       string         `json:"base"`
	Yolo       bool           `json:"yolo"`
	Agents     []PackageAgent `json:"agents,omitempty"`
}

// PackageCrew names the saved crew a plan revision was taken from.
type PackageCrew struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
}

// PackageAgent is an agent's approved selection: its catalog id and the
// SHA-256 of its entry's parts as the revision recorded them.
type PackageAgent struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

// PackageReference is a source reference: a path relative to the crew's
// working directory, an optional line range and note, and the git object
// it names at the base (a blob or a tree).
type PackageReference struct {
	Path   string `json:"path"`
	Start  int    `json:"start,omitempty"`
	End    int    `json:"end,omitempty"`
	Note   string `json:"note,omitempty"`
	Object string `json:"object"`
	Kind   string `json:"kind"`
}

// PackageAttachment is an attachment handed over: its display name, the
// name it is written under in the attempt (File, derived by Dispatch:
// NN-<portable>), its size, its content's SHA-256 and its type.
type PackageAttachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	File        string `json:"file"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"contentType,omitempty"`
}

// PackageComment is a comment handed over, its body with its SHA-256.
type PackageComment struct {
	ID     string    `json:"id"`
	N      int       `json:"n"`
	Author string    `json:"author,omitempty"`
	By     string    `json:"by,omitempty"`
	At     time.Time `json:"at,omitzero"`
	Body   string    `json:"body"`
	SHA256 string    `json:"sha256"`
}

// PackageDecision is a decision a person will make, and the tasks that
// wait on it.
type PackageDecision struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Kind     string   `json:"kind"`
	Before   []string `json:"before,omitempty"`
}

// PackageLaunch is the launch: its id, the run id Dispatch minted for it,
// the run's label, and when and by whom.
type PackageLaunch struct {
	ID    string    `json:"id"`
	Run   string    `json:"run"`
	Label string    `json:"label,omitempty"`
	At    time.Time `json:"at,omitzero"`
	By    string    `json:"by,omitempty"`
}

// RunTicket is a run's binding to the ticket it was launched from, read
// from its package: the ticket's id, key and revision as launched, the
// plan revision's id and number, the launch's id and the package's
// SHA-256. A continuation carries its source's.
type RunTicket struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Rev    int64  `json:"rev"`
	Plan   string `json:"plan"`
	PlanN  int    `json:"planN"`
	Launch string `json:"launch"`
	Digest string `json:"digest"`
}

// packageFile is an attachment's name in the attempt as Dispatch derives
// it: two digits, a dash and up to 64 portable bytes, never ending in a
// dot.
var packageFile = regexp.MustCompile(`^[0-9]{2}-[A-Za-z0-9._-]{1,64}$`)

// runPackage is a run's package as the engine keeps it: the bytes as
// launched, their SHA-256 (hex) and the package as read.
type runPackage struct {
	raw    string
	digest string
	p      Package
}

// packageDigest is the SHA-256 of a package's bytes, in lower-case hex.
func packageDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// readPackage reads a package's bytes: at most ledger.MaxPackage of them,
// one JSON object of PackageVersion whose header (the ticket, the plan
// revision, the launch) is whole and whose parts are within Dispatch's
// bounds, each attachment's file a derived name, distinct. It is decoded
// strictly (store.DecodeStrict): a field this reading does not name is
// refused, so a field Dispatch's builder names otherwise is a refused
// launch, never a part read as empty; a field added later comes with a
// new PackageVersion, which a version this engine does not read is
// refused as. An error matches ErrInvalid.
func readPackage(raw []byte) (*runPackage, error) {
	if len(raw) == 0 {
		return nil, invalidf("the package is empty")
	}
	if len(raw) > ledger.MaxPackage {
		return nil, invalidf("the package is %d bytes, more than %d", len(raw), ledger.MaxPackage)
	}
	// The version first, so a package of another one says so rather
	// than naming a field of it.
	var v struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, invalidf("the package cannot be read: %v", err)
	}
	if v.Version != PackageVersion {
		return nil, invalidf("the package is version %d: this Conductor reads version %d", v.Version, PackageVersion)
	}
	var p Package
	if err := store.DecodeStrict(raw, &p); err != nil {
		return nil, invalidf("the package cannot be read: %v", err)
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &runPackage{raw: string(raw), digest: packageDigest(raw), p: p}, nil
}

// validate holds a package to what the engine needs of it.
func (p Package) validate() error {
	if p.Version != PackageVersion {
		return invalidf("the package is version %d: this Conductor reads version %d", p.Version, PackageVersion)
	}
	for _, f := range []struct{ name, v string }{
		{"ticket.id", p.Ticket.ID}, {"ticket.key", p.Ticket.Key}, {"plan.id", p.Plan.ID}, {"launch.id", p.Launch.ID},
	} {
		if f.v == "" || len(f.v) > maxTicketRef || strings.ContainsFunc(f.v, isControl) {
			return invalidf("the package's %s must be 1 to %d bytes with no control character", f.name, maxTicketRef)
		}
	}
	if !ValidRunID(p.Launch.Run) {
		return invalidf("the package's launch.run %s is not a run id", quote(p.Launch.Run))
	}
	if p.Plan.N < 1 || p.Ticket.Rev < 0 {
		return invalidf("the package's plan.n must be 1 or more, and ticket.rev 0 or more")
	}
	switch {
	case len(p.Attachments) > maxPackageAttachments:
		return invalidf("the package hands over %d attachments, more than %d", len(p.Attachments), maxPackageAttachments)
	case len(p.Plan.Agents) > maxPackageAgents:
		return invalidf("the package names %d agents, more than %d", len(p.Plan.Agents), maxPackageAgents)
	case len(p.References) > maxPackageReferences:
		return invalidf("the package names %d references, more than %d", len(p.References), maxPackageReferences)
	case len(p.Comments) > maxPackageComments:
		return invalidf("the package hands over %d comments, more than %d", len(p.Comments), maxPackageComments)
	case len(p.Decisions) > maxPackageDecisions:
		return invalidf("the package names %d decisions, more than %d", len(p.Decisions), maxPackageDecisions)
	case len(p.Ticket.Blockers) > maxPackageBlockers:
		return invalidf("the package names %d blockers, more than %d", len(p.Ticket.Blockers), maxPackageBlockers)
	}
	for _, a := range p.Plan.Agents {
		if a.ID == "" || !isSHA256(a.SHA256) {
			return invalidf("the package's agent %s has no SHA-256 of its selection", quote(a.ID))
		}
	}
	files := map[string]bool{}
	for _, a := range p.Attachments {
		if !packageFile.MatchString(a.File) || strings.HasSuffix(a.File, ".") {
			return invalidf("the package's attachment %s is to be written as %s, which is not a derived name", quote(a.Name), quote(a.File))
		}
		// Distinct as a file system that folds case would see them.
		if k := strings.ToLower(a.File); files[k] {
			return invalidf("the package names the attachment file %s twice", quote(a.File))
		} else {
			files[k] = true
		}
		if a.Size < 0 || !isSHA256(a.SHA256) {
			return invalidf("the package's attachment %s has no size or SHA-256", quote(a.Name))
		}
	}
	return nil
}

// isSHA256 reports whether s is a SHA-256 in lower-case hex.
func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// ticket is the run's binding as its read shows it (Run.Ticket).
func (rp *runPackage) ticket() *RunTicket {
	if rp == nil {
		return nil
	}
	p := rp.p
	return &RunTicket{ID: p.Ticket.ID, Key: p.Ticket.Key, Rev: p.Ticket.Rev, Plan: p.Plan.ID, PlanN: p.Plan.N, Launch: p.Launch.ID, Digest: rp.digest}
}

// packageOfRow reads the package a run's ledger row keeps: nil for a run
// no ticket launched; an error for a package that cannot be read or is
// not the ticket's the row names.
func packageOfRow(row ledger.RunRow) (*runPackage, error) {
	if row.Package == "" && row.Ticket == "" {
		return nil, nil
	}
	rp, err := readPackage([]byte(row.Package))
	if err != nil {
		return nil, fmt.Errorf("run %s's package: %w", row.ID, err)
	}
	if rp.p.Ticket.ID != row.Ticket {
		return nil, invalidf("run %s's package is ticket %s's, its row names %s", row.ID, quote(rp.p.Ticket.ID), quote(row.Ticket))
	}
	return rp, nil
}

// agentSHA256 is the approved selection of the agent agentID as the
// package's plan revision recorded it (plan.agents), and whether there is
// one.
func (rp *runPackage) agentSHA256(agentID string) (string, bool) {
	for _, a := range rp.p.Plan.Agents {
		if a.ID == agentID {
			return a.SHA256, true
		}
	}
	return "", false
}

// ErrAgentChanged is a Launcher's refusal of an attempt's session whose
// agent's catalog entry, as the launcher would start it, is not the
// selection the run's plan revision approved (LaunchSpec.AgentSHA256),
// before any process exists. The engine fails the attempt at stage
// "agent", uncounted, and pauses the run's scheduling with the words: every
// attempt of that member would fail the same way until a person restores
// the entry, or revises the plan and launches a follow-up. A launcher that
// says more wraps it; a bare one is given the engine's words
// (agentChangedWords).
var ErrAgentChanged = errors.New("the agent's catalog entry is not the one its plan revision approved")

// agentChangedWords is what an attempt's ErrAgentChanged says: the
// launcher's own words when it gave any, else the agent and the plan
// revision.
func agentChangedWords(err error, agentID string, planN int) error {
	if err == ErrAgentChanged {
		return &wordsError{msg: fmt.Sprintf("the agent %s is not the one plan revision %d approved: its catalog entry changed since", agentID, planN), err: err}
	}
	return err
}

// wordsError says msg and is err.
type wordsError struct {
	msg string
	err error
}

func (w *wordsError) Error() string { return w.msg }
func (w *wordsError) Unwrap() error { return w.err }

// The package's files in an attempt (docs/plans/dispatch-evidence.md, "The
// package's files in each attempt"): beside the task files, through the
// same root at the attempt's working directory, .conductor/ticket.md (the
// ticket as launched, rendered here), .conductor/package.json (the
// package's bytes as launched) and each attachment as
// .conductor/attachments/<file>, the name Dispatch derived, copied from
// Dispatch's store through the opener the API gives the engine
// (UsePackageFiles), hashed and counted as it is copied. The .gitignore of
// "*" keeps them out of the result. Everything they come from is durable
// (the package in the ledger, the attachments pinned in Dispatch's store),
// so a retry made days later and a continuation's attempts get the same.
const (
	ticketFile      = "ticket.md"
	packageFileName = "package.json"
	attachmentsDir  = "attachments"
	// maxPackageAttachmentBytes bounds a package's attachments together,
	// Dispatch's own bound (the plan's owner decision 8).
	maxPackageAttachmentBytes = 16 << 20
	// maxTicketTitle is a ticket's title as task.md says it, in
	// characters, Dispatch's bound (ticket.md has the title whole).
	maxTicketTitle = 200
)

// UsePackageFiles gives the engine what reads a launch package's
// attachments: open returns the content of the attachment whose SHA-256
// (hex) is sha256 among those launch's package names, and nothing else.
// The API gives it Dispatch's store (OpenLaunchFile). Set it before the
// first launch; without it, an attempt of a run whose package hands over
// an attachment fails at stage "package", uncounted, and the run pauses.
func (e *Engine) UsePackageFiles(open func(ctx context.Context, launch, sha256 string) (io.ReadCloser, error)) {
	e.openPackageFile = open
}

// packageError is a package's file that cannot be handed to an attempt: an
// attachment that cannot be read, or whose size or hash is not the
// package's, or a file name that is not a derived one. It fails the
// attempt at stage "package", uncounted, and pauses the run (dispatch); an
// error writing into the worktree is the task files' ("task file").
type packageError struct{ err error }

func (p *packageError) Error() string { return p.err.Error() }
func (p *packageError) Unwrap() error { return p.err }

func packageErrorf(format string, args ...any) error {
	return &packageError{err: fmt.Errorf(format, args...)}
}

// packageFilePaths is where the package's files are in the attempt,
// relative to its working directory: ticket.md, package.json, then each
// attachment in the package's order (TaskTicket.Files).
func packageFilePaths(p Package) []string {
	out := []string{path.Join(taskDir, ticketFile), path.Join(taskDir, packageFileName)}
	for _, a := range p.Attachments {
		out = append(out, path.Join(taskDir, attachmentsDir, a.File))
	}
	return out
}

// checkPackageFiles holds the attachments' file names to the derived shape
// again, distinct as a file system that folds case sees them, and their
// sizes within the package's bound, before anything is opened or written.
func checkPackageFiles(p Package) error {
	seen := map[string]bool{}
	var total int64
	for _, a := range p.Attachments {
		if !packageFile.MatchString(a.File) || strings.HasSuffix(a.File, ".") {
			return packageErrorf("the attachment %s is to be written as %s, which is not a derived name", quote(a.Name), quote(a.File))
		}
		if k := strings.ToLower(a.File); seen[k] {
			return packageErrorf("the package names the attachment file %s twice", quote(a.File))
		} else {
			seen[k] = true
		}
		if !isSHA256(a.SHA256) || a.Size < 0 || a.Size > maxPackageAttachmentBytes-total {
			return packageErrorf("the attachment %s has no SHA-256, or its size passes the package's %d MiB", quote(a.Name), maxPackageAttachmentBytes>>20)
		}
		total += a.Size
	}
	return nil
}

// writeAttachments copies the package's attachments into
// .conductor/attachments under root, each from open, counted and hashed
// as it is copied: one that cannot be read, or whose size or SHA-256 is
// not the package's, is removed and is a packageError. The directory and
// each file are made, never written over (a fresh worktree has none).
func writeAttachments(ctx context.Context, root *os.Root, rp *runPackage, open func(ctx context.Context, launch, sha256 string) (io.ReadCloser, error)) error {
	if len(rp.p.Attachments) == 0 {
		return nil
	}
	if open == nil {
		return packageErrorf("Conductor has no store to read the package's %d attachments from", len(rp.p.Attachments))
	}
	dir := path.Join(taskDir, attachmentsDir)
	if err := root.Mkdir(dir, 0o755); err != nil {
		return err
	}
	for _, a := range rp.p.Attachments {
		if err := copyAttachment(ctx, root, path.Join(dir, a.File), a, rp.p.Launch.ID, open); err != nil {
			return err
		}
	}
	return nil
}

// readErr keeps the error of the reader it wraps, so a copy's read
// failure is told from its write's.
type readErr struct {
	r   io.Reader
	err error
}

func (r *readErr) Read(b []byte) (int, error) {
	n, err := r.r.Read(b)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

// copyAttachment copies one attachment to name under root.
func copyAttachment(ctx context.Context, root *os.Root, name string, a PackageAttachment, launch string, open func(ctx context.Context, launch, sha256 string) (io.ReadCloser, error)) (err error) {
	rc, err := open(ctx, launch, a.SHA256)
	if err != nil {
		return packageErrorf("the attachment %s cannot be read from the ticket's store: %v", quote(a.Name), err)
	}
	defer rc.Close()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
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
	src := &readErr{r: io.LimitReader(rc, a.Size+1)}
	n, err := io.Copy(f, io.TeeReader(src, h))
	switch {
	case src.err != nil:
		return packageErrorf("the attachment %s could not be read whole: %v", quote(a.Name), src.err)
	case err != nil:
		return err
	case n != a.Size:
		return packageErrorf("the attachment %s is %s bytes, not the %d its package names", quote(a.Name), sizeWords(n, a.Size), a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return packageErrorf("the attachment %s's SHA-256 is %s, not the package's %s", quote(a.Name), got, a.SHA256)
	}
	return nil
}

// sizeWords is a copied attachment's size in words: past the package's
// size, the copy stopped one byte after it.
func sizeWords(n, size int64) string {
	if n > size {
		return "more than " + strconv.FormatInt(size, 10)
	}
	return strconv.FormatInt(n, 10)
}

// ticketMarkdown is ticket.md: the ticket as it stood at the launch, the
// plan revision that launched the run with its approval, the ticket's
// fields, its description, acceptance criteria, constraints, the decisions
// a person makes, the references with their objects at the base, the
// attachments (each display name beside the file it was written as) and
// the comments handed over. An empty part is left out.
func ticketMarkdown(p Package) string {
	var b strings.Builder
	tk, pl := p.Ticket, p.Plan
	fmt.Fprintf(&b, "# %s: %s\n\n", tk.Key, flatLine(tk.Title))
	fmt.Fprintf(&b, "The ticket as it stood at its revision %d, when plan revision %d launched this run", tk.Rev, pl.N)
	if p.Launch.Run != "" {
		fmt.Fprintf(&b, " (%s", p.Launch.Run)
		if !p.Launch.At.IsZero() {
			fmt.Fprintf(&b, ", %s", p.Launch.At.UTC().Format(time.RFC3339))
		}
		if p.Launch.By != "" {
			fmt.Fprintf(&b, ", by %s", flatLine(p.Launch.By))
		}
		b.WriteString(")")
	}
	b.WriteString(". The run follows it as written here: a change made to the ticket since is a person's to decide.\n\n")
	approval := fmt.Sprintf("Plan revision %d", pl.N)
	if pl.ApprovedBy != "" {
		approval += " was approved by " + flatLine(pl.ApprovedBy)
		if !pl.ApprovedAt.IsZero() {
			approval += " at " + pl.ApprovedAt.UTC().Format(time.RFC3339)
		}
	} else if !pl.ApprovedAt.IsZero() {
		approval += " was approved at " + pl.ApprovedAt.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(&b, "%s; it runs the crew %q at the commit %s", approval, pl.Crew.Name, pl.Base)
	if pl.Yolo {
		b.WriteString(", its agents without their permission prompts")
	}
	b.WriteString(".")
	if n := strings.TrimSpace(pl.Note); n != "" {
		fmt.Fprintf(&b, " The approval's note:\n\n%s", quoteBlock(n))
	}
	b.WriteString("\n\n")
	var fields []string
	add := func(name, v string) {
		if v != "" {
			fields = append(fields, fmt.Sprintf("- %s: %s", name, v))
		}
	}
	add("Status", tk.Status)
	if tk.Priority > 0 {
		add("Priority", priorityWord(tk.Priority))
	}
	add("Horizon", tk.Horizon)
	add("Owner", flatLine(tk.Owner))
	add("Assignees", flatLine(strings.Join(tk.Assignees, ", ")))
	add("Labels", flatLine(strings.Join(tk.Labels, ", ")))
	if m := tk.Milestone; m != nil {
		v := flatLine(m.Name)
		if m.Target != "" {
			v += " (target " + m.Target + ")"
		}
		add("Milestone", v)
	}
	if pr := tk.Parent; pr != nil {
		add("Parent", ticketRefWords(*pr))
	}
	if len(tk.Blockers) > 0 {
		var bl []string
		for _, x := range tk.Blockers {
			bl = append(bl, ticketRefWords(x))
		}
		v := strings.Join(bl, "; ")
		if tk.BlockersTotal > len(tk.Blockers) {
			v += fmt.Sprintf("; %d more", tk.BlockersTotal-len(tk.Blockers))
		}
		add("Blocked by", v)
	}
	if len(fields) > 0 {
		b.WriteString(strings.Join(fields, "\n"))
		b.WriteString("\n\n")
	}
	section := func(title, body string) {
		if body = strings.TrimSpace(body); body != "" {
			fmt.Fprintf(&b, "## %s\n\n%s\n\n", title, body)
		}
	}
	section("Description", tk.Description)
	section("Acceptance criteria", tk.Acceptance)
	section("Constraints", p.Constraints)
	if len(p.Decisions) > 0 {
		b.WriteString("## Decisions a person makes\n\n")
		for _, d := range p.Decisions {
			fmt.Fprintf(&b, "- %s (%s): %s", d.ID, d.Kind, flatLine(d.Question))
			if len(d.Before) > 0 {
				fmt.Fprintf(&b, " Before: %s.", strings.Join(d.Before, ", "))
			}
			b.WriteString("\n")
		}
		b.WriteString("\nAsk a person for these; do not decide them yourself.\n\n")
	}
	if len(p.References) > 0 {
		fmt.Fprintf(&b, "## References\n\nPaths in the crew's working directory, each with the git object it names at the commit %s:\n\n", pl.Base)
		for _, r := range p.References {
			fmt.Fprintf(&b, "- `%s`", r.Path)
			if r.Start > 0 {
				fmt.Fprintf(&b, " lines %d", r.Start)
				if r.End > r.Start {
					fmt.Fprintf(&b, " to %d", r.End)
				}
			}
			fmt.Fprintf(&b, " (%s %s)", r.Kind, r.Object)
			if n := strings.TrimSpace(r.Note); n != "" {
				fmt.Fprintf(&b, ": %s", flatLine(n))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(p.Attachments) > 0 {
		fmt.Fprintf(&b, "## Attachments\n\nIn `%s/%s/`, each under the name it was written as:\n\n", taskDir, attachmentsDir)
		for _, a := range p.Attachments {
			fmt.Fprintf(&b, "- %q: `%s/%s/%s`, %d bytes", flatLine(a.Name), taskDir, attachmentsDir, a.File, a.Size)
			if a.ContentType != "" {
				fmt.Fprintf(&b, ", %s", flatLine(a.ContentType))
			}
			fmt.Fprintf(&b, ", SHA-256 %s\n", a.SHA256)
		}
		b.WriteString("\n")
	}
	if len(p.Comments) > 0 {
		b.WriteString("## Comments handed over\n\n")
		for _, c := range p.Comments {
			fmt.Fprintf(&b, "### Comment %d", c.N)
			if who := cmp.Or(c.By, c.Author); who != "" {
				fmt.Fprintf(&b, " by %s", flatLine(who))
			}
			if !c.At.IsZero() {
				fmt.Fprintf(&b, ", %s", c.At.UTC().Format(time.RFC3339))
			}
			fmt.Fprintf(&b, "\n\n%s\n\n", strings.TrimSpace(c.Body))
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// priorityWord is a ticket's priority in words, Dispatch's: 1 low to 4
// urgent.
func priorityWord(p int) string {
	if words := []string{"", "low", "medium", "high", "urgent"}; p >= 1 && p < len(words) {
		return words[p]
	}
	return strconv.Itoa(p)
}

// ticketRefWords is a parent or a blocker as ticket.md names it.
func ticketRefWords(r PackageTicketRef) string {
	v := r.Key
	if r.Title != "" {
		v += " " + strconv.Quote(flatLine(r.Title))
	}
	if r.Status != "" {
		v += " (" + r.Status + ")"
	}
	return v
}

// flatLine is s on one line: its line breaks and other control characters
// made spaces.
func flatLine(s string) string {
	return strings.Map(func(r rune) rune {
		if isControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
}

// cutTitle is a ticket's title as task.md says it: on one line and at
// most maxTicketTitle characters.
func cutTitle(s string) string {
	s = flatLine(s)
	if utf8.RuneCountInString(s) > maxTicketTitle {
		s = string([]rune(s)[:maxTicketTitle]) + "…"
	}
	return s
}

// quoteBlock is s as a Markdown block quote.
func quoteBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+l, " ")
	}
	return strings.Join(lines, "\n")
}
