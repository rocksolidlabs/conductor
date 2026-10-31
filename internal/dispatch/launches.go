package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A ticket's launches (Program item DS2, docs/plans/dispatch-evidence.md,
// "Operations" and "A launch"): a run is launched from a ticket only from
// its approved revision whose approval holds, and what it is handed is
// frozen and hashed as its package (package.go). A launch is recorded
// before it runs, pending, under a run id the store mints, so neither a
// resend nor a restart makes a second run: the API starts the run after
// the commit, through the engine, and records the launch started or
// failed by what the ledger holds. Nothing here starts anything. A
// launch's rows are entity launch, naming its ticket and project, and
// bump the launch's rev, never the ticket's, so launching never makes a
// ticket's edit stale.

// The states of a launch: pending when made, then started (its run's
// first ledger transaction committed) or failed (with why); nothing else.
const (
	LaunchPending = "pending"
	LaunchStarted = "started"
	LaunchFailed  = "failed"
)

// LaunchStates is a launch's states in their order.
var LaunchStates = []string{LaunchPending, LaunchStarted, LaunchFailed}

// The intents of a launch's outcome (its making's is IntentTicketLaunch).
const (
	IntentLaunchStarted = "launch.started"
	IntentLaunchFailed  = "launch.failed"
)

// ActorServer is the actor a launch's outcome is recorded under, whoever
// records it (the launch route, or the reconcile at start), with the op
// id <launch>.started or <launch>.failed: the first commit stands, and the
// other is answered by its receipt.
const ActorServer = "server"

// LaunchArgs is ticket.launch's content, its fingerprint: the ticket's
// key, the revision's id and the label as the client sent them (empty for
// the default). Replay takes it.
type LaunchArgs struct {
	Key   string `json:"key"`
	Plan  string `json:"plan"`
	Label string `json:"label,omitempty"`
}

// LaunchAgent is a member of a launched run with its agent and the
// version the agent's identity probe reported when the run started ("" for
// none), at most MaxLaunchAgentVersion bytes (a longer one is cut).
type LaunchAgent struct {
	Member  string `json:"member"`
	Agent   string `json:"agent"`
	Version string `json:"version"`
}

// LaunchSummary is a launch as a list shows it, never with its package:
// its row, the key of its ticket and PlanN, its revision's number, both
// derived at read.
type LaunchSummary struct {
	ID            string        `json:"id"`
	TicketID      string        `json:"ticketId"`
	TicketKey     string        `json:"ticketKey"`
	PlanID        string        `json:"planId"`
	PlanN         int           `json:"planN"`
	RunID         string        `json:"runId"`
	Label         string        `json:"label"`
	TicketRev     int64         `json:"ticketRev"`
	Intent        string        `json:"intent"`
	Criteria      string        `json:"criteria"`
	PackageSHA256 string        `json:"packageSha256"`
	State         string        `json:"state"`
	Why           string        `json:"why,omitempty"`
	Agents        []LaunchAgent `json:"agents"`
	CreatedAt     time.Time     `json:"createdAt"`
	CreatedBy     string        `json:"createdBy"`
	Fingerprint   string        `json:"fingerprint"`
	StartedAt     time.Time     `json:"startedAt,omitzero"`
	Rev           int64         `json:"rev"`
}

// LaunchView is a launch read whole: its summary and its package, as the
// bytes it is kept as.
type LaunchView struct {
	LaunchSummary
	Package json.RawMessage `json:"package"`
}

// LaunchChanges is what changed of a launch's ticket since the launch
// (ChangedSince): the intent fields (title, description, acceptance) set
// after the launch whose value now differs from the package's, the live
// comments added since, and the ticket's rev now.
type LaunchChanges struct {
	Fields   []string `json:"fields"`
	Comments int      `json:"comments"`
	Rev      int64    `json:"rev"`
}

// launchCreated is a launch's create row's value, its summary as made:
// what never changes (the field rules' immutable class), never the
// package, which is content named by its digest and kept beside the row
// that names it.
type launchCreated struct {
	ID            string    `json:"id"`
	TicketID      string    `json:"ticketId"`
	PlanID        string    `json:"planId"`
	RunID         string    `json:"runId"`
	Label         string    `json:"label"`
	TicketRev     int64     `json:"ticketRev"`
	Intent        string    `json:"intent"`
	Criteria      string    `json:"criteria"`
	PackageSHA256 string    `json:"packageSha256"`
	Fingerprint   string    `json:"fingerprint"`
	CreatedAt     time.Time `json:"createdAt"`
	CreatedBy     string    `json:"createdBy"`
}

// memberPattern is a crew member's name (internal/crew's
// memberNamePattern; the store imports nothing of the engine's).
var memberPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)

// Launch launches a run from the plan revision with the id of the live
// ticket with the key (ticket.launch), label the run's own name (at most
// MaxLaunchLabel characters; empty for the ticket's key and title, cut at
// a word). The expected revision is the ticket's, so what launches is what
// the person saw (stale_revision otherwise). In the transaction: the
// ticket live (not_found) and open (invalid: "CND-12 is done: reopen it to
// launch"), its project not archived (archived); the revision the
// ticket's (not_found) and approved (invalid, with its state's words); the
// approval holding, its intent the ticket's now (stale_plan, naming what
// changed, the remedy an approval again or a new revision); every
// attachment and comment it hands over still live and each comment's body
// the one recorded (stale_plan, naming the first gone or edited, the
// remedy a new revision, since the recorded digest is the immutable
// body's); at most MaxLaunches launches (bound). Then the package is built
// from the rows (buildPackage, bound past MaxPackage), the run id minted
// (the crew's id, '-', 8 hex characters from crypto/rand, the engine's
// shape, no other launch naming it), the launch written pending with its
// package's SHA-256 and the operation's fingerprint, one launch_files row
// per attachment it names (which the sweep keeps), and one create row
// whose value is the launch's summary without the package. Nothing is
// started: the receipt's id is the launch's, which the caller reads it by
// (LaunchByID), and the API starts the run after the commit.
func (s *Store) Launch(ctx context.Context, op Op, key, planID, label string) (Receipt, error) {
	return s.Apply(ctx, op, IntentTicketLaunch, LaunchArgs{Key: key, Plan: planID, Label: label}, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		if closed(tk.Status) {
			return refuse(RefusalInvalid, "%s is %s: reopen it to launch", tk.Key, tk.Status)
		}
		p, err := scanPlan(t.tx.QueryRowContext(t.ctx, "SELECT "+planColumns+" FROM plans WHERE id = ?", planID))
		switch {
		case isNoRows(err):
			return refuse(RefusalNotFound, "no plan revision %s", planID)
		case err != nil:
			return err
		case p.TicketID != tk.ID:
			return refuse(RefusalNotFound, "the plan revision %s is not %s's", planID, tk.Key)
		}
		if p.N, err = planNumber(t.ctx, t.tx, p); err != nil {
			return err
		}
		if err := t.launchable(tk, p); err != nil {
			return err
		}
		if p.ApprovedIntent != IntentDigest(tk) {
			words, err := intentWords(t.ctx, t.tx, tk, p)
			if err != nil {
				return err
			}
			return refuse(RefusalStalePlan, "%s: approve it again or revise it", words)
		}
		var raw string
		if err := t.tx.QueryRowContext(t.ctx, "SELECT body FROM plans WHERE id = ?", p.ID).Scan(&raw); err != nil {
			return err
		}
		var body PlanBody
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			return fmt.Errorf("dispatch: the body of plan revision %s: %w", p.ID, err)
		}
		crew, err := readPlanCrew(body.Crew, p.CrewID)
		if err != nil {
			return err
		}
		attachments, err := t.launchAttachments(tk, p, body.Attachments)
		if err != nil {
			return err
		}
		if words, stale, err := commentsStale(t.ctx, t.tx, tk, p, body.Comments); err != nil {
			return err
		} else if stale {
			return refuse(RefusalStalePlan, "%s: revise it", words)
		}
		comments, err := t.launchComments(tk, body.Comments)
		if err != nil {
			return err
		}
		var n int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM launches WHERE ticket_id = ?", tk.ID).Scan(&n); err != nil {
			return err
		}
		if err := checkCount("launches of a ticket", n+1, MaxLaunches); err != nil {
			return err
		}
		if label == "" {
			label = defaultLabel(tk.Key, tk.Title)
		}
		if err := checkField("launch.label", "the label", label); err != nil {
			return err
		}
		run, err := t.mintRunID(crew.ID)
		if err != nil {
			return err
		}
		l := launchCreated{
			ID: newID(), TicketID: tk.ID, PlanID: p.ID, RunID: run, Label: label, TicketRev: tk.Rev, Intent: IntentDigest(tk),
			Criteria: criteriaHash(tk.Acceptance), Fingerprint: t.fp, CreatedAt: t.now, CreatedBy: t.op.author(),
		}
		pkg, err := t.buildPackage(tk, p, body, crew, attachments, comments, PackageLaunch{ID: l.ID, Run: run, Label: label, At: t.now, By: l.CreatedBy})
		if err != nil {
			return err
		}
		l.PackageSHA256 = sha256Hex(pkg)
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO launches (id, ticket_id, plan_id, run_id, label, ticket_rev, intent, criteria, package,
			package_sha256, state, created_at, created_by, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			l.ID, l.TicketID, l.PlanID, l.RunID, l.Label, l.TicketRev, l.Intent, l.Criteria, pkg, l.PackageSHA256, LaunchPending,
			stamp(l.CreatedAt), l.CreatedBy, l.Fingerprint); err != nil {
			return err
		}
		// One pin per content the package names: two attachments of one
		// content are one file.
		for _, a := range attachments {
			if _, err := t.tx.ExecContext(t.ctx, "INSERT OR IGNORE INTO launch_files (launch_id, sha256) VALUES (?, ?)", l.ID, a.SHA256); err != nil {
				return err
			}
		}
		t.created(l.ID, "")
		value, err := encodeValue(l)
		if err != nil {
			return err
		}
		return t.record(ChangeRow{Entity: EntityLaunch, EntityID: l.ID, TicketID: tk.ID, ProjectID: tk.ProjectID, Op: OpCreate, Value: value})
	})
}

// launchable refuses a launch from the revision p of the ticket tk unless
// it is approved, in its state's words: proposed, superseded (by the
// ticket's newest approved revision, when there is one) or withdrawn.
func (t *tx) launchable(tk Ticket, p PlanSummary) error {
	switch p.State {
	case PlanApproved:
		return nil
	case PlanProposed:
		return refuse(RefusalInvalid, "revision %d is proposed: approve it first", p.N)
	case PlanWithdrawn:
		return refuse(RefusalInvalid, "revision %d is withdrawn", p.N)
	case PlanSuperseded:
		by, err := scanPlan(t.tx.QueryRowContext(t.ctx, "SELECT "+planColumns+" FROM plans WHERE ticket_id = ? AND state = ? ORDER BY created_at DESC, id DESC LIMIT 1",
			tk.ID, PlanApproved))
		if isNoRows(err) {
			return refuse(RefusalInvalid, "revision %d is superseded: approve it again to launch from it", p.N)
		}
		if err != nil {
			return err
		}
		if by.N, err = planNumber(t.ctx, t.tx, by); err != nil {
			return err
		}
		return refuse(RefusalInvalid, "revision %d is superseded by revision %d: launch from revision %d, or approve revision %d again", p.N, by.N, by.N, p.N)
	}
	return refuse(RefusalInvalid, "revision %d is %s", p.N, p.State)
}

// launchAttachments reads the attachments a revision hands over, in its
// order, each a live attachment of the ticket still (stale_plan naming the
// first gone, the remedy a new revision).
func (t *tx) launchAttachments(tk Ticket, p PlanSummary, ids []string) ([]Attachment, error) {
	out := make([]Attachment, 0, len(ids))
	for _, id := range ids {
		a, err := scanAttachment(t.tx.QueryRowContext(t.ctx, "SELECT "+attachmentColumns+" FROM attachments WHERE id = ?", id))
		switch {
		case isNoRows(err) || (err == nil && a.TicketID != tk.ID):
			return nil, refuse(RefusalStalePlan, "the attachment %s handed over is gone since revision %d was made: revise it", id, p.N)
		case err != nil:
			return nil, err
		case !a.DeletedAt.IsZero():
			return nil, refuse(RefusalStalePlan, "the attachment %s was removed since revision %d was made: revise it", a.Name, p.N)
		}
		out = append(out, a)
	}
	return out, nil
}

// launchComments reads the comments a revision hands over, in its order,
// each whole with its number; the caller has checked them live and as the
// revision recorded them (commentsStale).
func (t *tx) launchComments(tk Ticket, in []PlanComment) ([]Comment, error) {
	out := make([]Comment, 0, len(in))
	for _, c := range in {
		got, err := scanComment(t.tx.QueryRowContext(t.ctx, "SELECT "+pickedColumns+", numbered.n FROM comments c JOIN ("+numberedComments+") numbered ON numbered.id = c.id WHERE c.id = ?",
			tk.ID, c.ID))
		if err != nil {
			return nil, err
		}
		out = append(out, got)
	}
	return out, nil
}

// defaultLabel is a launch's label when none is given: the ticket's key
// and title, cut to MaxLaunchLabel characters at a word.
func defaultLabel(key, title string) string {
	s := key + " " + title
	r := []rune(s)
	if len(r) <= MaxLaunchLabel {
		return s
	}
	cut := string(r[:MaxLaunchLabel])
	if i := strings.LastIndexByte(cut, ' '); i > len(key) {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ")
}

// runSuffix is a run id's last part: 8 hex characters from crypto/rand,
// the engine's shape.
func runSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("dispatch: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// maxRunMints is how many run ids a launch mints before it gives up: one
// is all but always enough (no other launch names it, and the engine knows
// no run of it).
const maxRunMints = 8

// mintRunID mints the run id of a launch of the crew with the id: the
// crew's id, '-', and a suffix (the tx's runSuffix, else runSuffix), one
// no other launch names and, when the store has sight of the engine, no
// run the engine knows, minted again when it is. Unique by this rule and
// by the ledger's insert, never by an index, which could refuse a
// replicated row.
func (t *tx) mintRunID(crew string) (string, error) {
	suffix := t.runSuffix
	if suffix == nil {
		suffix = runSuffix
	}
	for range maxRunMints {
		id := crew + "-" + suffix()
		var n int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM launches WHERE run_id = ?", id).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 && (t.runExists == nil || !t.runExists(id)) {
			return id, nil
		}
	}
	return "", fmt.Errorf("dispatch: no free run id for the crew %s after %d tries", crew, maxRunMints)
}

// launchIDArgs is a launch's outcome's content: the launch's id alone, so
// a second record of it (the launch route's and the reconcile's) is
// answered by the first's receipt whatever it carries.
type launchIDArgs struct {
	ID string `json:"id"`
}

// StartedLaunch records the pending launch with the id started
// (launch.started), its run's first ledger transaction committed: state
// started, started_at, and agents, each member's agent and the version its
// identity probe reported (at most MaxPlanAgents, each a member's name and
// an agent's id, a version past MaxLaunchAgentVersion bytes cut). The
// operation is the actor server's under the op id <id>.started, whatever
// op names (its display name is kept), so it is the same whoever records
// it: the first commit stands and the next is its receipt's repeat. A
// launch started or failed already is invalid; none is not_found. It is
// recorded whatever became of the ticket and its project since, since
// the run exists. ErrNotFound for an id not of an id's shape.
func (s *Store) StartedLaunch(ctx context.Context, op Op, id string, agents []LaunchAgent) (Receipt, error) {
	if !ValidID(id) {
		return Receipt{}, ErrNotFound
	}
	op.Actor, op.ID = ActorServer, id+".started"
	return s.Apply(ctx, op, IntentLaunchStarted, launchIDArgs{ID: id}, func(t *tx) error {
		base, before, err := t.pendingLaunch(id)
		if err != nil {
			return err
		}
		value, err := launchAgentsValue(agents)
		if err != nil {
			return err
		}
		return t.setScalars("launches", base, before, []fieldValue{{"state", LaunchStarted}, {"started_at", stamp(t.now)}, {"agents", value}})
	})
}

// FailedLaunch records the pending launch with the id failed
// (launch.failed), with why, in at most MaxLaunchWhy characters (cut, its
// control characters but newlines and tabs made spaces; empty is "the run
// could not be started"): state failed and why. As StartedLaunch, the
// operation is the actor server's under the op id <id>.failed, whatever
// op names; a launch started or failed already is invalid; none is
// not_found.
func (s *Store) FailedLaunch(ctx context.Context, op Op, id, why string) (Receipt, error) {
	if !ValidID(id) {
		return Receipt{}, ErrNotFound
	}
	op.Actor, op.ID = ActorServer, id+".failed"
	return s.Apply(ctx, op, IntentLaunchFailed, launchIDArgs{ID: id}, func(t *tx) error {
		base, before, err := t.pendingLaunch(id)
		if err != nil {
			return err
		}
		return t.setScalars("launches", base, before, []fieldValue{{"state", LaunchFailed}, {"why", launchWhy(why)}})
	})
}

// pendingLaunch reads the launch with the id inside its outcome's
// operation: not_found when there is none; the revision expected, the
// launch's; invalid unless it is pending. It gives the base of its rows
// (entity launch, its ticket and project) and its scalars as they are.
func (t *tx) pendingLaunch(id string) (ChangeRow, []fieldValue, error) {
	var ticketID, state string
	var rev int64
	switch err := t.tx.QueryRowContext(t.ctx, "SELECT ticket_id, state, rev FROM launches WHERE id = ?", id).Scan(&ticketID, &state, &rev); {
	case isNoRows(err):
		return ChangeRow{}, nil, refuse(RefusalNotFound, "no launch %s", id)
	case err != nil:
		return ChangeRow{}, nil, err
	}
	if err := t.expect(rev); err != nil {
		return ChangeRow{}, nil, err
	}
	if state != LaunchPending {
		return ChangeRow{}, nil, refuse(RefusalInvalid, "the launch %s is %s already", id, state)
	}
	var projectID string
	if err := t.tx.QueryRowContext(t.ctx, "SELECT project_id FROM tickets WHERE id = ?", ticketID).Scan(&projectID); err != nil {
		return ChangeRow{}, nil, err
	}
	before, err := t.scalars("launches", id, []string{"state", "started_at", "agents", "why"})
	if err != nil {
		return ChangeRow{}, nil, err
	}
	return ChangeRow{Entity: EntityLaunch, EntityID: id, TicketID: ticketID, ProjectID: projectID}, before, nil
}

// launchAgentsValue checks a started launch's agents and gives them as
// their column holds them: at most MaxPlanAgents (bound), each a member's
// name and an agent's id (invalid otherwise), each version made text
// (fitText) and cut to MaxLaunchAgentVersion bytes, the whole at most
// MaxLaunchAgents as encoded (bound).
func launchAgentsValue(agents []LaunchAgent) (string, error) {
	if err := checkCount("agents of a launch", len(agents), MaxPlanAgents); err != nil {
		return "", err
	}
	out := make([]LaunchAgent, 0, len(agents))
	for _, a := range agents {
		switch {
		case !memberPattern.MatchString(a.Member):
			return "", refuse(RefusalInvalid, "the member %q is not a crew member's name", a.Member)
		case !agentIDPattern.MatchString(a.Agent):
			return "", refuse(RefusalInvalid, "the agent %q is not a catalog agent's id", a.Agent)
		}
		a.Version = cutBytes(fitText(a.Version, false), MaxLaunchAgentVersion)
		if err := checkField("launch.agent_version", "the agent "+a.Agent+"'s version", a.Version); err != nil {
			return "", err
		}
		out = append(out, a)
	}
	value, err := encodeValue(out)
	if err != nil {
		return "", err
	}
	if len(value) > MaxLaunchAgents {
		return "", refuse(RefusalBound, "the launch's agents are %d bytes, more than %d", len(value), MaxLaunchAgents)
	}
	return value, nil
}

// launchWhy is why a launch failed as its column holds it: text (fitText,
// newlines and tabs kept), cut to MaxLaunchWhy characters, "the run could
// not be started" for none.
func launchWhy(why string) string {
	why = strings.TrimSpace(fitText(why, true))
	if why == "" {
		return "the run could not be started"
	}
	if r := []rune(why); len(r) > MaxLaunchWhy {
		why = string(r[:MaxLaunchWhy-1]) + "…"
	}
	return why
}

// fitText is s as valid UTF-8 with its control characters made spaces,
// newlines and tabs kept when markdown is set: words the server gives,
// which a refusal must not lose.
func fitText(s string, markdown bool) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(c rune) rune {
		if unicode.IsControl(c) && (!markdown || (c != '\n' && c != '\t')) {
			return ' '
		}
		return c
	}, s)
}

// cutBytes is s cut to at most n bytes at a character's boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// launchColumns is a launch's row as scanLaunch reads it, from launches l
// joined to its ticket t: its ticket's key and its revision's number
// beside it, its package not among them.
const launchColumns = `l.id, l.ticket_id, t.key, l.plan_id, (SELECT COUNT(*) FROM plans o JOIN plans p ON p.id = l.plan_id WHERE o.ticket_id = p.ticket_id
	AND (o.created_at < p.created_at OR (o.created_at = p.created_at AND o.id <= p.id))), l.run_id, l.label, l.ticket_rev, l.intent, l.criteria,
	l.package_sha256, l.state, l.why, l.agents, l.created_at, l.created_by, l.fingerprint, l.started_at, l.rev`

// launchFrom is the launches joined to their tickets, as launchColumns
// reads them.
const launchFrom = " FROM launches l JOIN tickets t ON t.id = l.ticket_id"

// scanLaunch reads a launch's row (launchColumns).
func scanLaunch(row interface{ Scan(...any) error }) (LaunchSummary, error) {
	var l LaunchSummary
	var agents, created, started string
	if err := row.Scan(&l.ID, &l.TicketID, &l.TicketKey, &l.PlanID, &l.PlanN, &l.RunID, &l.Label, &l.TicketRev, &l.Intent, &l.Criteria,
		&l.PackageSHA256, &l.State, &l.Why, &agents, &created, &l.CreatedBy, &l.Fingerprint, &started, &l.Rev); err != nil {
		return LaunchSummary{}, err
	}
	if err := json.Unmarshal([]byte(agents), &l.Agents); err != nil {
		return LaunchSummary{}, fmt.Errorf("dispatch: the agents of launch %s: %w", l.ID, err)
	}
	if l.Agents == nil {
		l.Agents = []LaunchAgent{}
	}
	l.CreatedAt, l.StartedAt = unstamp(created), unstamp(started)
	return l, nil
}

// scanLaunches reads the launches a query of launchColumns gives.
func scanLaunches(ctx context.Context, q queryer, query string, args ...any) ([]LaunchSummary, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LaunchSummary{}
	for rows.Next() {
		l, err := scanLaunch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Launches reads the launches of the ticket with the key, deleted or not,
// newest first, at most limit (MaxLaunches when limit is not 1 to it):
// each one's summary, never its package. ErrNotFound when there is no
// such ticket; a version 1 database, read as it was found, has none.
func (s *Store) Launches(ctx context.Context, key string, limit int) ([]LaunchSummary, error) {
	if limit <= 0 || limit > MaxLaunches {
		limit = MaxLaunches
	}
	var out []LaunchSummary
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if schemaOf(q) < 2 {
			out = []LaunchSummary{}
			return nil
		}
		out, err = scanLaunches(ctx, q, "SELECT "+launchColumns+launchFrom+" WHERE l.ticket_id = ? ORDER BY l.created_at DESC, l.id DESC LIMIT ?", tk.ID, limit)
		return err
	})
	return out, err
}

// LaunchByID reads the launch with the id, whole: its summary and its
// package as the bytes it is kept as. ErrNotFound when there is none.
func (s *Store) LaunchByID(ctx context.Context, id string) (LaunchView, error) {
	var out LaunchView
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < 2 {
			return ErrNotFound
		}
		l, err := scanLaunch(q.QueryRowContext(ctx, "SELECT "+launchColumns+launchFrom+" WHERE l.id = ?", id))
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var pkg string
		if err := q.QueryRowContext(ctx, "SELECT package FROM launches WHERE id = ?", id).Scan(&pkg); err != nil {
			return err
		}
		out = LaunchView{LaunchSummary: l, Package: json.RawMessage(pkg)}
		return nil
	})
	return out, err
}

// PendingLaunches reads every launch still pending, oldest first: what
// the API's reconcile at start settles by what the ledger holds. A
// version 1 database, read as it was found, has none.
func (s *Store) PendingLaunches(ctx context.Context) ([]LaunchSummary, error) {
	var out []LaunchSummary
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < 2 {
			out = []LaunchSummary{}
			return nil
		}
		var err error
		out, err = scanLaunches(ctx, q, "SELECT "+launchColumns+launchFrom+" WHERE l.state = ? ORDER BY l.created_at, l.id", LaunchPending)
		return err
	})
	return out, err
}

// ChangedSince reads what changed of the ticket of the launch with the id
// since the launch (LaunchChanges): the intent fields set in the ticket's
// change rows after the launch's create row, by the local seq, whose value
// now is not the package's (a field edited and edited back is not named);
// the comments added since that are live; and the ticket's rev now. A run
// keeps working from its package whatever this says. ErrNotFound when
// there is no such launch.
func (s *Store) ChangedSince(ctx context.Context, id string) (LaunchChanges, error) {
	var out LaunchChanges
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < 2 {
			return ErrNotFound
		}
		var ticketID, pkg string
		switch err := q.QueryRowContext(ctx, "SELECT ticket_id, package FROM launches WHERE id = ?", id).Scan(&ticketID, &pkg); {
		case isNoRows(err):
			return ErrNotFound
		case err != nil:
			return err
		}
		var launched struct {
			Ticket PackageTicket `json:"ticket"`
		}
		if err := json.Unmarshal([]byte(pkg), &launched); err != nil {
			return fmt.Errorf("dispatch: the package of launch %s: %w", id, err)
		}
		var since int64
		if err := q.QueryRowContext(ctx, "SELECT COALESCE(MIN(seq), 0) FROM changes WHERE entity = ? AND entity_id = ? AND op = ?",
			EntityLaunch, id, OpCreate).Scan(&since); err != nil {
			return err
		}
		tk, err := ticketByID(ctx, q, ticketID)
		if err != nil {
			return err
		}
		set, err := readTexts(ctx, q, "SELECT DISTINCT field FROM changes WHERE entity = ? AND entity_id = ? AND op = ? AND seq > ? AND field IN ('title', 'description', 'acceptance')",
			EntityTicket, ticketID, OpSet, since)
		if err != nil {
			return err
		}
		then := map[string]string{"title": launched.Ticket.Title, "description": launched.Ticket.Description, "acceptance": launched.Ticket.Acceptance}
		now := map[string]string{"title": tk.Title, "description": tk.Description, "acceptance": tk.Acceptance}
		out = LaunchChanges{Fields: []string{}, Rev: tk.Rev}
		for _, f := range intentFields {
			if slices.Contains(set, f.field) && then[f.field] != now[f.field] {
				out.Fields = append(out.Fields, f.field)
			}
		}
		return q.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments WHERE ticket_id = ? AND deleted_at = '' AND id IN
			(SELECT entity_id FROM changes WHERE entity = ? AND op = ? AND ticket_id = ? AND seq > ?)`,
			ticketID, EntityComment, OpCreate, ticketID, since).Scan(&out.Comments)
	})
	if errors.Is(err, ErrNotFound) {
		return LaunchChanges{}, ErrNotFound
	}
	return out, err
}
