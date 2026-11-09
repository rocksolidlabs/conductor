package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// conductor dispatch plans, plan, launch and runs (Program item DS2,
// docs/plans/dispatch-evidence.md, PR-13): a ticket's plan revisions,
// proposed from a saved crew, approved by the operator and withdrawn; a
// managed run launched from the approved one; and the ticket's runs, every
// launch, every run linked by hand and their continuations, with what
// changed of the ticket since each launch. Through the admin's routes as
// the workbench uses them, with the server and the token conductor crews
// takes. A revision is named by its number (3, or r3, as the lists print
// it) or by its id; the CLI reads the ticket's revisions to find it, so a
// number is the ticket's own. Every write mints its operation's id and is
// asked for once more on a lost reply (askTwice; a launch, within
// launchTries sends in all), which the server answers from its receipt: a
// launch resent never makes a second run. The reads go
// through maxDispatchReply, which holds a revision with its body.

// dispatchPlannedArgs is the commands of this file, which runDispatch
// hands here.
var dispatchPlannedArgs = map[string]bool{"plans": true, "plan": true, "launch": true, "runs": true}

// planVerbs is what conductor dispatch plan does.
var planVerbs = []string{"show", "propose", "approve", "withdraw"}

// launchRequestTimeout bounds a launch's request: the server's own bound
// on a launch (api.LaunchTimeout: its run's start and its two store
// writes), and a request's time beside it for the reply to come back.
var launchRequestTimeout = api.LaunchTimeout + requestTimeout

// launchTries is how many times a launch is sent in all, the same
// operation each time: again at once when its reply was lost, again after
// launchResendWait when the server answers it still pending (503
// ledger_unavailable carrying the launch). A resend the server takes
// waits behind the request starting the run; one it cannot take yet
// (taking over the ledger) is answered at once.
var (
	launchTries      = 3
	launchResendWait = 2 * time.Second
)

// plannedFlags is what this file's commands take beside the server's;
// each command defines its own.
type plannedFlags struct {
	crew, base, basedOn, constraintsFile, note *string
	plan, label                                *string
	references, attachments, comments          multiFlag
	decisions, decisionFor                     multiFlag
}

// revisionRef names a plan revision: by its number among its ticket's
// (n, from 1) or by its id.
type revisionRef struct {
	n  int
	id string
}

// String is the reference as a person gave it.
func (r revisionRef) String() string {
	if r.id != "" {
		return r.id
	}
	return "r" + strconv.Itoa(r.n)
}

// parseRevision reads a revision's reference: its id (16 lower-case hex
// characters), or its number, 3 or r3.
func parseRevision(s string) (revisionRef, error) {
	if dispatch.ValidID(s) {
		return revisionRef{id: s}, nil
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(s, "r")); err == nil && n >= 1 && !strings.HasPrefix(strings.TrimPrefix(s, "r"), "+") {
		return revisionRef{n: n}, nil
	}
	return revisionRef{}, fmt.Errorf("a plan revision is its number (3 or r3) or its id: %q", s)
}

// planVerb is the verb of conductor dispatch plan, the first word of its
// arguments that is no flag and no value of the server's flags (--server
// and --token take one, given before the verb as conductor dispatch takes
// them after any command), and the arguments without it; "" for none. A
// help flag before any verb is the verb, so it prints the usage.
func planVerb(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			return a, args[i+1:]
		case a == "--":
			if i+1 < len(args) {
				return args[i+1], append(slices.Clone(args[:i]), args[i+2:]...)
			}
			return "", args
		case strings.HasPrefix(a, "-"):
			if name := strings.TrimLeft(a, "-"); name == "server" || name == "token" {
				i++
			}
			continue
		}
		return a, append(slices.Clone(args[:i]), args[i+1:]...)
	}
	return "", args
}

// runDispatchPlanned is conductor dispatch plans, plan, launch and runs.
// Exit 2 for a usage error, before any request; 1 when the server refuses
// or cannot be asked.
func runDispatchPlanned(ctx context.Context, sub string, rest []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	name := sub
	if sub == "plan" {
		verb, others := planVerb(rest)
		switch {
		case verb == "-h" || verb == "--help" || verb == "help":
			fmt.Fprint(stderr, dispatchUsage)
			return 0, nil
		case !slices.Contains(planVerbs, verb):
			fmt.Fprint(stderr, dispatchUsage)
			return 2, errors.New("dispatch plan takes show, propose, approve or withdraw")
		}
		name, rest = "plan "+verb, others
	}
	fs := flag.NewFlagSet("dispatch "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := addAPIFlags(fs)
	var f plannedFlags
	switch name {
	case "plan propose":
		f.crew = fs.String("crew", "", "the saved crew's id: a crew with tasks (required)")
		f.base = fs.String("base", "", "the commit the work starts from: a branch, a tag, HEAD or a commit (HEAD when not given)")
		f.basedOn = fs.String("based-on", "", "the revision this one revises: its number or its id")
		fs.Var(&f.references, "reference", "a source reference, PATH[:START[-END]][=NOTE], relative to the crew's folder (repeatable)")
		fs.Var(&f.attachments, "attachment", "an attachment of the ticket to hand over, by its name or its id (repeatable)")
		fs.Var(&f.comments, "comment", "a comment of the ticket to hand over, by its number (repeatable)")
		f.constraintsFile = fs.String("constraints-file", "", "a file of Markdown for the constraints, - for stdin")
		fs.Var(&f.decisions, "decision", "a decision a person will make, ID:KIND:QUESTION[@TASK,...], KIND approval, clarification or assignment (repeatable)")
		fs.Var(&f.decisionFor, "decision-for", "whom a decision is for, ID=role:RESP (architecture, product, security, acceptance) or ID=person:NAME of the crew (repeatable)")
		f.note = fs.String("note", "", "why this revision, Markdown")
	case "plan approve":
		f.note = fs.String("note", "", "the approval's note, the rationale, Markdown")
	case "launch":
		f.plan = fs.String("plan", "", "the revision to launch from, its number or its id: the approved one when not given")
		f.label = fs.String("label", "", "the run's name: the ticket's key and title when not given")
	}
	fs.Usage = func() {
		fmt.Fprint(stderr, dispatchUsage, "\nFlags of dispatch "+name+":\n")
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	if len(pos) == 0 {
		return 2, fmt.Errorf("dispatch %s takes a ticket's key, as CND-12", name)
	}
	key := pos[0]
	if _, _, ok := dispatch.ParseKey(key); !ok {
		return 2, fmt.Errorf("dispatch %s: %q is no ticket's key: a project's key and a number, as CND-12", name, key)
	}
	var rev *revisionRef
	switch name {
	case "plan show":
		if len(pos) > 2 {
			return 2, fmt.Errorf("dispatch plan show takes a ticket's key and at most a revision: %q", pos[2])
		}
	case "plan approve", "plan withdraw":
		if len(pos) != 2 {
			return 2, fmt.Errorf("dispatch %s takes a ticket's key and a revision's number, as CND-12 3", name)
		}
	default:
		if len(pos) != 1 {
			return 2, fmt.Errorf("dispatch %s takes a ticket's key alone: %q", name, pos[1])
		}
	}
	if len(pos) == 2 {
		r, err := parseRevision(pos[1])
		if err != nil {
			return 2, fmt.Errorf("dispatch %s: %w", name, err)
		}
		rev = &r
	}
	if name == "launch" && *f.plan != "" {
		r, err := parseRevision(*f.plan)
		if err != nil {
			return 2, fmt.Errorf("dispatch launch: --plan: %w", err)
		}
		rev = &r
	}
	var p *proposal
	if name == "plan propose" {
		if p, err = f.proposal(stdin); err != nil {
			return 2, fmt.Errorf("dispatch plan propose: %w", err)
		}
	}
	c, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	switch name {
	case "plans":
		return dispatchPlans(ctx, c, key, stdout)
	case "plan show":
		return dispatchPlanShow(ctx, c, key, rev, stdout)
	case "plan propose":
		return p.send(ctx, c, key, stdout)
	case "plan approve", "plan withdraw":
		return dispatchPlanAct(ctx, c, key, *rev, strings.TrimPrefix(name, "plan "), f.note, stdout)
	case "launch":
		return dispatchLaunch(ctx, c, key, rev, *f.label, stdout)
	}
	return dispatchRuns(ctx, c, key, stdout)
}

// planEntry is a plan revision as the routes answer it: the store's
// summary with its number and staleness, whether the saved crew was edited
// or removed since (CrewChanged), whether an agent resolves otherwise in
// the catalog now (AgentsChanged), the checks it expects, and, on a
// revision's own read, its body.
type planEntry struct {
	dispatch.PlanSummary
	CrewChanged    bool            `json:"crewChanged"`
	AgentsChanged  bool            `json:"agentsChanged"`
	ExpectedChecks int             `json:"expectedChecks"`
	Body           json.RawMessage `json:"body,omitempty"`
}

// ticketPlans reads the ticket's revisions, newest first.
func ticketPlans(ctx context.Context, c *apiClient, key string) ([]planEntry, error) {
	var out struct {
		Plans []planEntry `json:"plans"`
	}
	err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/tickets/"+url.PathEscape(key)+"/plans", nil, &out, maxDispatchReply)
	return out.Plans, err
}

// findRevision is the revision of the ticket's that ref names.
func findRevision(key string, plans []planEntry, ref revisionRef) (planEntry, error) {
	for _, p := range plans {
		if ref.id != "" && p.ID == ref.id || ref.id == "" && p.N == ref.n {
			return p, nil
		}
	}
	return planEntry{}, fmt.Errorf("%s has no plan revision %s", key, ref)
}

// approvedRevision is the ticket's approved revision: an error when it has
// none, and when a merge left two, which a person names with --plan.
func approvedRevision(key string, plans []planEntry) (planEntry, error) {
	var approved []planEntry
	var names []string
	for _, p := range plans {
		if p.State == dispatch.PlanApproved {
			approved = append(approved, p)
			names = append(names, "r"+strconv.Itoa(p.N))
		}
	}
	switch len(approved) {
	case 0:
		return planEntry{}, fmt.Errorf("%s has no approved plan revision: approve one first (conductor dispatch plan approve %s N)", key, key)
	case 1:
		return approved[0], nil
	}
	return planEntry{}, fmt.Errorf("%s has %d approved plan revisions, %s: name one with --plan", key, len(approved), strings.Join(names, " and "))
}

// remedyWords is a stale approval's remedy as words.
func remedyWords(remedy string) string {
	if remedy == dispatch.RemedyApprove {
		return "approve it again"
	}
	return "revise it"
}

// short is a commit id cut to 12 characters, as a list prints it.
func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// checksWords is a count of checks in words.
func checksWords(n int) string {
	if n == 1 {
		return "1 check"
	}
	return strconv.Itoa(n) + " checks"
}

// planState is a revision's state with its approval or withdrawal, and
// why an approval no longer holds, with its remedy, and what the reads
// say changed since: the words a list's line and a revision's header use.
func planState(p planEntry) []string {
	out := []string{oneLine(p.State)}
	switch {
	case !p.ApprovedAt.IsZero() && p.State != dispatch.PlanWithdrawn:
		out = append(out, oneLine(fmt.Sprintf("approved by %s %s at the ticket's revision %d", cmp.Or(p.ApprovedBy, "admin"), when(p.ApprovedAt), p.ApprovedRev)))
	case !p.WithdrawnAt.IsZero():
		out = append(out, oneLine(fmt.Sprintf("withdrawn by %s %s", cmp.Or(p.WithdrawnBy, "admin"), when(p.WithdrawnAt))))
	}
	if p.Stale != nil {
		out = append(out, oneLine("stale: "+p.Stale.Words+": "+remedyWords(p.Stale.Remedy)))
	}
	if p.CrewChanged {
		out = append(out, "the saved crew was edited or removed since: revise it to take it as it is now")
	}
	if p.AgentsChanged {
		out = append(out, "an agent resolves otherwise in the catalog now: revise it")
	}
	return out
}

// dispatchPlans prints the ticket's revisions, newest first, one a line:
// rN, id, the crew, the base, the expected checks, then the state, the
// approval and what no longer holds.
func dispatchPlans(ctx context.Context, c *apiClient, key string, stdout io.Writer) (int, error) {
	plans, err := ticketPlans(ctx, c, key)
	if err != nil {
		return 1, err
	}
	if len(plans) == 0 {
		fmt.Fprintf(stdout, "%s has no plan revisions\n", key)
		return 0, nil
	}
	for _, p := range plans {
		line := []string{"r" + strconv.Itoa(p.N), p.ID, oneLine(p.CrewName), short(p.Base), checksWords(p.ExpectedChecks)}
		fmt.Fprintln(stdout, strings.Join(append(line, planState(p)...), "\t"))
	}
	return 0, nil
}

// planBodyView is what a revision's printout reads of its body: the crew
// as the crew store keeps it, decoded with the engine's own types.
type planBodyView struct {
	Base         string                   `json:"base"`
	Crew         crew.Crew                `json:"crew"`
	Agents       []dispatch.PlanAgent     `json:"agents"`
	ManifestFile *planManifestFileView    `json:"manifestFile"`
	References   []dispatch.PlanReference `json:"references"`
	Attachments  []string                 `json:"attachments"`
	Comments     []dispatch.PlanComment   `json:"comments"`
	Constraints  string                   `json:"constraints"`
	Decisions    []dispatch.PlanDecision  `json:"decisions"`
	Note         string                   `json:"note"`
}

// planManifestFileView is a file-form project manifest at the base.
type planManifestFileView struct {
	Path     string        `json:"path"`
	Blob     string        `json:"blob"`
	Manifest crew.Manifest `json:"manifest"`
}

// dispatchPlanShow prints a revision whole: the one named, else the
// approved one, else the newest; its attachments by name from the ticket's
// read and its comments by number (commentNumbers).
func dispatchPlanShow(ctx context.Context, c *apiClient, key string, ref *revisionRef, stdout io.Writer) (int, error) {
	plans, err := ticketPlans(ctx, c, key)
	if err != nil {
		return 1, err
	}
	var p planEntry
	switch {
	case ref != nil:
		p, err = findRevision(key, plans, *ref)
	case len(plans) == 0:
		err = fmt.Errorf("%s has no plan revisions", key)
	default:
		if p, err = approvedRevision(key, plans); err != nil {
			p, err = plans[0], nil
		}
	}
	if err != nil {
		return 1, err
	}
	var out struct {
		Plan planEntry `json:"plan"`
	}
	if err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/plans/"+url.PathEscape(p.ID), nil, &out, maxDispatchReply); err != nil {
		return 1, err
	}
	var body planBodyView
	if err := json.Unmarshal(out.Plan.Body, &body); err != nil {
		return 1, fmt.Errorf("the plan revision's body from %s: %w", c.host, err)
	}
	var tv struct {
		Ticket dispatch.TicketView `json:"ticket"`
	}
	if err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/tickets/"+url.PathEscape(key), nil, &tv, maxDispatchReply); err != nil {
		return 1, err
	}
	numbers, err := commentNumbers(ctx, c, key, body.Comments, tv.Ticket.Comments)
	if err != nil {
		return 1, err
	}
	writePlan(stdout, key, plans, out.Plan, body, tv.Ticket, numbers)
	return 0, nil
}

// commentNumbers is the number of each comment the revision hands over,
// by its id: from the ticket's read, which carries its last
// dispatch.MaxTicketComments, and for an older one from the comments
// route, page by page from the first until each is found (a ticket holds
// at most dispatch.MaxComments). An id found nowhere has no number.
func commentNumbers(ctx context.Context, c *apiClient, key string, handed []dispatch.PlanComment, recent []dispatch.Comment) (map[string]int, error) {
	numbers := map[string]int{}
	for _, cm := range recent {
		numbers[cm.ID] = cm.N
	}
	missing := 0
	for _, cm := range handed {
		if _, ok := numbers[cm.ID]; !ok {
			missing++
		}
	}
	for after := 0; missing > 0; {
		var page struct {
			Comments []dispatch.Comment `json:"comments"`
		}
		q := url.Values{"after": {strconv.Itoa(after)}, "limit": {strconv.Itoa(dispatch.MaxTicketComments)}}
		if err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/tickets/"+url.PathEscape(key)+"/comments?"+q.Encode(), nil, &page, maxDispatchReply); err != nil {
			return nil, err
		}
		next := after
		for _, cm := range page.Comments {
			if _, ok := numbers[cm.ID]; !ok && slices.ContainsFunc(handed, func(h dispatch.PlanComment) bool { return h.ID == cm.ID }) {
				missing--
			}
			numbers[cm.ID] = cm.N
			next = max(next, cm.N)
		}
		if next == after {
			break
		}
		after = next
	}
	return numbers, nil
}

// argvText is an argv as a line shows it: each word as it is when it holds
// no space or quote, else quoted.
func argvText(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n'\"\\") {
			words[i] = strconv.Quote(a)
		} else {
			words[i] = a
		}
	}
	return oneLine(strings.Join(words, " "))
}

// writePlan prints a revision whole, as Markdown: its header (the ticket
// and its number, its state, approval and staleness, the crew, the base,
// the yolo choice said plainly, the checks it expects), the approval's
// note, the crew's goal, the members with their agents, arguments and
// prompts, the agents as the catalog resolved them, the tasks with their
// member, kind, needs, instructions and checks, every phase of the project
// manifest (writeManifest), the references, what it hands over (attachments by name,
// comments by number), the constraints, the decisions a person makes and
// its note: every text the attempts' agents are handed.
func writePlan(w io.Writer, key string, plans []planEntry, p planEntry, b planBodyView, tv dispatch.TicketView, numbers map[string]int) {
	fmt.Fprintf(w, "# %s plan revision %d (r%d)\n\n", oneLine(key), p.N, p.N)
	field := func(name, value string) { fmt.Fprintf(w, "- %s: %s\n", name, value) }
	field("id", oneLine(p.ID))
	for i, s := range planState(p) {
		if i == 0 {
			field("state", s)
			continue
		}
		fmt.Fprintf(w, "  %s\n", s)
	}
	if p.BasedOn != "" {
		based := oneLine(p.BasedOn)
		for _, o := range plans {
			if o.ID == p.BasedOn {
				based = "r" + strconv.Itoa(o.N)
			}
		}
		field("revises", based)
	}
	field("crew", oneLine(fmt.Sprintf("%s (%s), as saved %s", p.CrewName, p.CrewID, when(p.CrewUpdatedAt))))
	field("base", oneLine(b.Base))
	switch {
	case b.Crew.Yolo == nil:
		field("yolo", "the server's default at the launch")
	case *b.Crew.Yolo:
		field("yolo", "on: its members run without their permission prompts")
	default:
		field("yolo", "off: its members ask before they act")
	}
	if b.Crew.Cwd != "" {
		field("folder", oneLine(b.Crew.Cwd))
	}
	field("expected checks", strconv.Itoa(p.ExpectedChecks))
	field("made", oneLine(fmt.Sprintf("by %s %s", p.CreatedBy, when(p.CreatedAt))))
	section := func(title, body string) {
		if strings.TrimSpace(body) != "" {
			fmt.Fprintf(w, "\n## %s\n\n%s\n", title, strings.TrimRight(textBlock(body), "\n"))
		}
	}
	if p.ApprovedNote != "" {
		section("Approval note", p.ApprovedNote)
	}
	section("Goal", b.Crew.Goal)
	var lines strings.Builder
	for _, m := range b.Crew.Members {
		fmt.Fprintf(&lines, "- %s: %s", oneLine(m.Name), oneLine(m.AgentID))
		if len(m.Args) > 0 {
			fmt.Fprintf(&lines, " %s", argvText(m.Args))
		}
		lines.WriteString("\n")
		indentedText(&lines, "prompt", m.Prompt)
	}
	section("Members", lines.String())
	lines.Reset()
	for _, a := range b.Agents {
		fmt.Fprintf(&lines, "- %s: %s\n", oneLine(a.ID), argvText(a.Command))
		if len(a.StartArgs) > 0 {
			fmt.Fprintf(&lines, "  start: %s\n", argvText(a.StartArgs))
		}
		if len(a.YoloArgs) > 0 {
			fmt.Fprintf(&lines, "  yolo: %s\n", argvText(a.YoloArgs))
		}
		if len(a.EnvNames) > 0 {
			fmt.Fprintf(&lines, "  environment: %s\n", oneLine(strings.Join(a.EnvNames, ", ")))
		}
	}
	section("Agents", lines.String())
	lines.Reset()
	for _, t := range b.Crew.Tasks {
		fmt.Fprintf(&lines, "- %s: %s, by %s", oneLine(t.ID), oneLine(t.Kind), oneLine(t.Member))
		if len(t.Needs) > 0 {
			fmt.Fprintf(&lines, ", after %s", oneLine(strings.Join(t.Needs, ", ")))
		}
		lines.WriteString("\n")
		indentedText(&lines, "instructions", t.Instructions)
		for _, ch := range t.Checks {
			fmt.Fprintf(&lines, "  check %s: %s\n", oneLine(ch.ID), argvText(ch.Argv))
		}
	}
	section("Tasks", lines.String())
	lines.Reset()
	var m crew.Manifest
	if b.Crew.Manifest != nil {
		m = *b.Crew.Manifest
	}
	if b.ManifestFile != nil {
		fmt.Fprintf(&lines, "- from %s, blob %s at the base\n", oneLine(b.ManifestFile.Path), short(b.ManifestFile.Blob))
		m = b.ManifestFile.Manifest
	}
	writeManifest(&lines, m, b.Crew.Setup)
	section("Project manifest", lines.String())
	lines.Reset()
	for _, r := range b.References {
		fmt.Fprintf(&lines, "- %s (%s %s)", oneLine(referenceText(r.Path, r.Start, r.End)), oneLine(r.Kind), short(r.Object))
		if r.Note != "" {
			fmt.Fprintf(&lines, ": %s", oneLine(r.Note))
		}
		lines.WriteString("\n")
	}
	section("References", lines.String())
	lines.Reset()
	names := map[string]string{}
	for _, a := range tv.Attachments {
		names[a.ID] = a.Name
	}
	for _, id := range b.Attachments {
		fmt.Fprintf(&lines, "- attachment %s\n", oneLine(cmp.Or(names[id], id)))
	}
	for _, cm := range b.Comments {
		if n, ok := numbers[cm.ID]; ok {
			fmt.Fprintf(&lines, "- comment #%d\n", n)
		} else {
			fmt.Fprintf(&lines, "- comment %s\n", oneLine(cm.ID))
		}
	}
	section("Handed over", lines.String())
	section("Constraints", b.Constraints)
	lines.Reset()
	for _, d := range b.Decisions {
		fmt.Fprintf(&lines, "- %s (%s): %s", oneLine(d.ID), oneLine(d.Kind), oneLine(d.Question))
		if len(d.Before) > 0 {
			fmt.Fprintf(&lines, "; before %s", oneLine(strings.Join(d.Before, ", ")))
		}
		if d.For != "" {
			fmt.Fprintf(&lines, "; for %s", oneLine(d.For))
		}
		lines.WriteString("\n")
	}
	if lines.Len() > 0 {
		lines.WriteString("\nA run launched from this revision holds the tasks each names until the operator answers it.\n")
	}
	section("Decisions a person makes", lines.String())
	section("Note", b.Note)
}

// writeManifest writes every phase of a project manifest the engine runs,
// in its order, each step with its timeout when it sets one: the
// prerequisites, the setup (a crew's legacy Setup is that phase, as
// effectiveManifest reads it), the services, the ready probes with their
// interval and timeout, the preview, the host's environment names, the
// ports, the verify steps and the cleanup.
func writeManifest(w *strings.Builder, m crew.Manifest, legacySetup []crew.Check) {
	steps := func(phase string, checks []crew.Check) {
		for _, ch := range checks {
			fmt.Fprintf(w, "- %s %s: %s", phase, oneLine(ch.ID), argvText(ch.Argv))
			if ch.TimeoutSeconds > 0 {
				fmt.Fprintf(w, " (timeout %d s)", ch.TimeoutSeconds)
			}
			w.WriteString("\n")
		}
	}
	steps("prerequisite", m.Prerequisites)
	steps("setup", m.Setup)
	steps("setup", legacySetup)
	steps("service", m.Services)
	for _, p := range m.Ready {
		target := argvText(p.Argv)
		if p.URL != "" {
			target = oneLine(p.URL)
		}
		fmt.Fprintf(w, "- ready %s: %s", oneLine(p.ID), target)
		var bounds []string
		if p.IntervalSeconds > 0 {
			bounds = append(bounds, fmt.Sprintf("every %d s", p.IntervalSeconds))
		}
		if p.TimeoutSeconds > 0 {
			bounds = append(bounds, fmt.Sprintf("timeout %d s", p.TimeoutSeconds))
		}
		if len(bounds) > 0 {
			fmt.Fprintf(w, " (%s)", strings.Join(bounds, ", "))
		}
		w.WriteString("\n")
	}
	if m.Preview != "" {
		fmt.Fprintf(w, "- preview: %s\n", oneLine(m.Preview))
	}
	if len(m.Env) > 0 {
		fmt.Fprintf(w, "- environment: %s\n", oneLine(strings.Join(m.Env, ", ")))
	}
	if len(m.Ports) > 0 {
		fmt.Fprintf(w, "- ports: %s\n", oneLine(strings.Join(m.Ports, ", ")))
	}
	steps("verify", m.Verify)
	steps("cleanup", m.Cleanup)
}

// indentedText writes a saved text under a list item: its label, then its
// lines indented under it, a terminal's control characters but newlines
// and tabs made U+FFFD (textBlock); nothing for an empty text.
func indentedText(w *strings.Builder, label, text string) {
	text = strings.TrimRight(textBlock(text), "\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	fmt.Fprintf(w, "  %s:\n", label)
	for _, l := range strings.Split(text, "\n") {
		if l == "" {
			w.WriteString("\n")
			continue
		}
		fmt.Fprintf(w, "    %s\n", l)
	}
}

// referenceText is a reference as its flag says it: path, path:12 or
// path:12-30.
func referenceText(path string, start, end int) string {
	switch {
	case start > 0 && end > start:
		return fmt.Sprintf("%s:%d-%d", path, start, end)
	case start > 0:
		return fmt.Sprintf("%s:%d", path, start)
	}
	return path
}

// referenceRange is a reference's line range after its path: :12 or
// :12-30.
var referenceRange = regexp.MustCompile(`^(.*):([0-9]+)(?:-([0-9]+))?$`)

// windowsAbsolute is a path that starts with a drive: C:\ or C:/.
var windowsAbsolute = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// parseReference reads a source reference as the ticket page's field
// reads it (web/app/utils/dispatchPlans.ts, parseReference): path,
// path:12 or path:12-30, each with =note after it. The note runs from the
// first '='; the line range is the digits after the path's last ':'. The
// path is relative to the crew's folder: never absolute, never leaving it,
// at most dispatch.MaxReferencePath bytes; a range starts at 1 and ends at
// its start or after; a note is at most dispatch.MaxReferenceNote
// characters. The server resolves the path at the base.
func parseReference(text string) (dispatch.PlanReferenceRequest, error) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return dispatch.PlanReferenceRequest{}, errors.New("a reference names a path")
	}
	left, note, _ := strings.Cut(raw, "=")
	left, note = strings.TrimSpace(left), strings.TrimSpace(note)
	path := left
	var start, end int
	if m := referenceRange.FindStringSubmatch(left); m != nil {
		var err error
		path = m[1]
		if start, err = strconv.Atoi(m[2]); err != nil {
			return dispatch.PlanReferenceRequest{}, fmt.Errorf("%s: a line number too large", left)
		}
		if m[3] != "" {
			if end, err = strconv.Atoi(m[3]); err != nil {
				return dispatch.PlanReferenceRequest{}, fmt.Errorf("%s: a line number too large", left)
			}
		}
		if start < 1 {
			return dispatch.PlanReferenceRequest{}, fmt.Errorf("%s: lines are numbered from 1", path)
		}
		if m[3] != "" && end < start {
			return dispatch.PlanReferenceRequest{}, fmt.Errorf("%s: the range %d-%d ends before it starts", path, start, end)
		}
	}
	for strings.HasPrefix(path, "./") {
		path = strings.TrimLeft(strings.TrimPrefix(path, "./"), "/")
	}
	path = strings.TrimRight(path, "/")
	switch {
	case path == "":
		return dispatch.PlanReferenceRequest{}, errors.New("a reference names a path before its lines")
	case strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) || windowsAbsolute.MatchString(path):
		return dispatch.PlanReferenceRequest{}, fmt.Errorf("%s: a reference is relative to the crew's folder, never absolute", path)
	case slices.Contains(strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }), ".."):
		return dispatch.PlanReferenceRequest{}, fmt.Errorf("%s: a reference never leaves the crew's folder", path)
	case strings.ContainsFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return dispatch.PlanReferenceRequest{}, fmt.Errorf("%q: a path holds no control character", path)
	case len(path) > dispatch.MaxReferencePath:
		return dispatch.PlanReferenceRequest{}, fmt.Errorf("the path is longer than %d bytes", dispatch.MaxReferencePath)
	case utf8.RuneCountInString(note) > dispatch.MaxReferenceNote:
		return dispatch.PlanReferenceRequest{}, fmt.Errorf("the note on %s is longer than %d characters", path, dispatch.MaxReferenceNote)
	}
	r := dispatch.PlanReferenceRequest{Path: path, Start: start, Note: note}
	if end != start {
		r.End = end
	}
	return r, nil
}

// decisionTasks is the tasks a decision names after its last '@': task
// ids, separated by commas.
var decisionTasks = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}(,[a-z0-9][a-z0-9_-]{0,39})*$`)

// parseDecision reads a decision a person will make: ID:KIND:QUESTION,
// and when the text holds an '@', the tasks that wait on it after the
// last one, separated by commas (a question that holds an '@' of its own
// ends with one, @ alone for no task). The store checks the id's shape,
// the question's bound and that each task is the crew's.
func parseDecision(text string) (dispatch.PlanDecision, error) {
	id, rest, ok := strings.Cut(text, ":")
	kind, question, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || id == "" {
		return dispatch.PlanDecision{}, fmt.Errorf("a decision is ID:KIND:QUESTION[@TASK,...]: %q", text)
	}
	if kind != dispatch.DecisionApproval && kind != dispatch.DecisionClarification && kind != dispatch.DecisionAssignment {
		return dispatch.PlanDecision{}, fmt.Errorf("a decision's kind is approval, clarification or assignment: %q", kind)
	}
	d := dispatch.PlanDecision{ID: id, Kind: kind, Before: []string{}}
	if i := strings.LastIndexByte(question, '@'); i >= 0 {
		tasks := question[i+1:]
		question = question[:i]
		if tasks != "" {
			if !decisionTasks.MatchString(tasks) {
				return dispatch.PlanDecision{}, fmt.Errorf("a decision's tasks after its last @ are task ids, separated by commas: %q", tasks)
			}
			d.Before = strings.Split(tasks, ",")
		}
	}
	d.Question = strings.TrimSpace(question)
	if d.Question == "" {
		return dispatch.PlanDecision{}, fmt.Errorf("decision %s asks no question", id)
	}
	return d, nil
}

// decisionFor sets whom a decision is for from ID=LABEL: the decision
// named ID among ds (a --decision), the label role:RESP or person:NAME,
// once per decision. The server judges whether the crew resolves it.
func decisionFor(ds []dispatch.PlanDecision, text string) error {
	id, label, ok := strings.Cut(text, "=")
	if !ok || id == "" || label == "" {
		return fmt.Errorf("whom a decision is for is ID=role:RESP or ID=person:NAME: %q", text)
	}
	if !strings.HasPrefix(label, dispatch.DecisionForRole) && !strings.HasPrefix(label, dispatch.DecisionForPerson) {
		return fmt.Errorf("decision %s's label is role:RESP or person:NAME: %q", id, label)
	}
	i := slices.IndexFunc(ds, func(d dispatch.PlanDecision) bool { return d.ID == id })
	switch {
	case i < 0:
		return fmt.Errorf("no --decision is named %s", id)
	case ds[i].For != "":
		return fmt.Errorf("decision %s is for %s already", id, ds[i].For)
	}
	ds[i].For = label
	return nil
}

// proposal is a plan revision to propose: the request with what the flags
// give as they are, and the names the ticket's reads turn into ids before
// it is sent (the revision it revises, the attachments and the comments).
type proposal struct {
	req         dispatch.PlanRequest
	basedOn     *revisionRef
	attachments []string
	comments    []int
}

// proposal checks the flags of plan propose and builds the proposal,
// before any request: its errors are usage errors.
func (f plannedFlags) proposal(stdin io.Reader) (*proposal, error) {
	p := &proposal{req: dispatch.PlanRequest{Crew: strings.TrimSpace(*f.crew), Base: *f.base, Note: *f.note}}
	if p.req.Crew == "" {
		return nil, errors.New("--crew names the saved crew by its id (conductor crews lists them)")
	}
	if *f.basedOn != "" {
		r, err := parseRevision(*f.basedOn)
		if err != nil {
			return nil, fmt.Errorf("--based-on: %w", err)
		}
		p.basedOn = &r
	}
	for _, text := range f.references {
		r, err := parseReference(text)
		if err != nil {
			return nil, fmt.Errorf("--reference: %w", err)
		}
		p.req.References = append(p.req.References, r)
	}
	for _, a := range f.attachments {
		if strings.TrimSpace(a) == "" {
			return nil, errors.New("--attachment names an attachment of the ticket")
		}
		p.attachments = append(p.attachments, a)
	}
	for _, c := range f.comments {
		n, err := strconv.Atoi(strings.TrimPrefix(c, "#"))
		if err != nil || n < 1 {
			return nil, fmt.Errorf("--comment is a comment's number, from 1: %q", c)
		}
		p.comments = append(p.comments, n)
	}
	for _, text := range f.decisions {
		d, err := parseDecision(text)
		if err != nil {
			return nil, fmt.Errorf("--decision: %w", err)
		}
		p.req.Decisions = append(p.req.Decisions, d)
	}
	for _, text := range f.decisionFor {
		if err := decisionFor(p.req.Decisions, text); err != nil {
			return nil, fmt.Errorf("--decision-for: %w", err)
		}
	}
	if *f.constraintsFile != "" {
		text, err := readMarkdownFile(*f.constraintsFile, stdin, dispatch.MaxPlanConstraints, "the constraints")
		if err != nil {
			return nil, err
		}
		p.req.Constraints = text
	}
	return p, nil
}

// readMarkdownFile reads the file named, or stdin for "-", at most max
// bytes: past that the store would refuse it, so it is refused here before
// anything is sent.
func readMarkdownFile(name string, stdin io.Reader, max int, what string) (string, error) {
	r := stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return "", err
	}
	if len(b) > max {
		return "", fmt.Errorf("%s: %s are at most %d KiB", name, what, max>>10)
	}
	return string(b), nil
}

// send turns the proposal's names into ids from the ticket's reads (the
// revision it revises from its revisions, the attachments from the
// ticket, each comment from the ticket's comments by its number), mints
// the operation's id and sends it, once more on a lost reply, then prints
// the revision made.
func (p *proposal) send(ctx context.Context, c *apiClient, key string, stdout io.Writer) (int, error) {
	if p.basedOn != nil {
		plans, err := ticketPlans(ctx, c, key)
		if err != nil {
			return 1, err
		}
		based, err := findRevision(key, plans, *p.basedOn)
		if err != nil {
			return 1, err
		}
		p.req.BasedOn = based.ID
	}
	if len(p.attachments) > 0 {
		var tv struct {
			Ticket dispatch.TicketView `json:"ticket"`
		}
		if err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/tickets/"+url.PathEscape(key), nil, &tv, maxDispatchReply); err != nil {
			return 1, err
		}
		for _, name := range p.attachments {
			id, err := attachmentID(key, tv.Ticket.Attachments, name)
			if err != nil {
				return 1, err
			}
			p.req.Attachments = append(p.req.Attachments, id)
		}
	}
	for _, n := range p.comments {
		id, err := commentID(ctx, c, key, n)
		if err != nil {
			return 1, err
		}
		p.req.Comments = append(p.req.Comments, id)
	}
	body := struct {
		OpID string `json:"opId"`
		dispatch.PlanRequest
	}{newOpID(), p.req}
	var out struct {
		Receipt dispatch.Receipt `json:"receipt"`
		Plan    *planEntry       `json:"plan"`
	}
	if err := askTwice(ctx, func(ctx context.Context) error {
		return c.doLimit(ctx, http.MethodPost, "/api/dispatch/tickets/"+url.PathEscape(key)+"/plans", body, &out, maxDispatchReply)
	}); err != nil {
		return 1, err
	}
	if out.Plan == nil {
		fmt.Fprintf(stdout, "%s: plan revision %s proposed\n", key, oneLine(out.Receipt.ID))
		return 0, nil
	}
	fmt.Fprintf(stdout, "%s r%d proposed: %s, crew %s at %s, %s expected\napprove it with: conductor dispatch plan approve %s %d\n",
		key, out.Plan.N, out.Plan.ID, oneLine(out.Plan.CrewName), short(out.Plan.Base), checksWords(out.Plan.ExpectedChecks), key, out.Plan.N)
	return 0, nil
}

// attachmentID is the id of the ticket's attachment the name names: its
// id itself, else the attachments of that name, else of that name whatever
// its case. Two of one name (a ticket may hold Spec.png twice) are refused
// rather than guessed between.
func attachmentID(key string, attachments []dispatch.Attachment, name string) (string, error) {
	for _, a := range attachments {
		if a.ID == name {
			return a.ID, nil
		}
	}
	for _, fold := range []bool{false, true} {
		var found []string
		for _, a := range attachments {
			if a.Name == name || fold && strings.EqualFold(a.Name, name) {
				found = append(found, a.ID)
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil
		}
		return "", fmt.Errorf("%s has %d attachments named %q: name the one by its id (%s)", key, len(found), name, strings.Join(found, ", "))
	}
	return "", fmt.Errorf("%s has no attachment named %q", key, name)
}

// commentID is the id of the ticket's comment numbered n, read from the
// ticket's comments; a deleted one is refused.
func commentID(ctx context.Context, c *apiClient, key string, n int) (string, error) {
	var out struct {
		Comments []dispatch.Comment `json:"comments"`
	}
	q := url.Values{"after": {strconv.Itoa(n - 1)}, "limit": {"1"}}
	if err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/tickets/"+url.PathEscape(key)+"/comments?"+q.Encode(), nil, &out, maxDispatchReply); err != nil {
		return "", err
	}
	for _, cm := range out.Comments {
		if cm.N != n {
			continue
		}
		if !cm.DeletedAt.IsZero() {
			return "", fmt.Errorf("comment #%d of %s is deleted", n, key)
		}
		return cm.ID, nil
	}
	return "", fmt.Errorf("%s has no comment #%d", key, n)
}

// dispatchPlanAct approves or withdraws the ticket's revision ref names:
// its operation's id minted, sent once more on a lost reply; an
// approval's note, the rationale, sent when given.
func dispatchPlanAct(ctx context.Context, c *apiClient, key string, ref revisionRef, verb string, note *string, stdout io.Writer) (int, error) {
	plans, err := ticketPlans(ctx, c, key)
	if err != nil {
		return 1, err
	}
	p, err := findRevision(key, plans, ref)
	if err != nil {
		return 1, err
	}
	body := map[string]any{"opId": newOpID()}
	if note != nil && *note != "" {
		body["note"] = *note
	}
	var out struct {
		Plan *planEntry `json:"plan"`
	}
	if err := askTwice(ctx, func(ctx context.Context) error {
		return c.doLimit(ctx, http.MethodPost, "/api/dispatch/plans/"+url.PathEscape(p.ID)+"/"+verb, body, &out, maxDispatchReply)
	}); err != nil {
		return 1, err
	}
	state := map[string]string{"approve": dispatch.PlanApproved, "withdraw": dispatch.PlanWithdrawn}[verb]
	if out.Plan != nil {
		state = out.Plan.State
	}
	fmt.Fprintf(stdout, "%s r%d %s\n", key, p.N, oneLine(state))
	return 0, nil
}

// dispatchLaunch starts a managed run from the ticket's revision: the one
// named, else the approved one. The operation's id is minted and the
// request sent again on a lost reply, at once, and on a launch the server
// answers still pending, after a wait: the server answers a resend from
// its receipt, the same launch and never a second run. Every send counts
// against launchTries; each request waits as long as the server's own
// bound on a launch. Once the server has named a pending launch, an exit
// that does not settle it (still pending after the last try, a resend
// unanswered, interrupted, or refused without its outcome, as a 401 or a
// 429 is) names the launch and where it shows, never advice to launch
// again, which would be a second launch. It prints the launch, the run
// and the run's page; a launch that failed is the server's words, its
// launch kept failed (conductor dispatch runs lists it).
func dispatchLaunch(ctx context.Context, c *apiClient, key string, ref *revisionRef, label string, stdout io.Writer) (int, error) {
	plans, err := ticketPlans(ctx, c, key)
	if err != nil {
		return 1, err
	}
	var p planEntry
	if ref != nil {
		p, err = findRevision(key, plans, *ref)
	} else {
		p, err = approvedRevision(key, plans)
	}
	if err != nil {
		return 1, err
	}
	body := map[string]any{"opId": newOpID(), "plan": p.ID}
	if label != "" {
		body["label"] = label
	}
	var out struct {
		Launch dispatch.LaunchSummary `json:"launch"`
		Run    *struct {
			ID    string `json:"id"`
			Label string `json:"label"`
			Yolo  bool   `json:"yolo"`
		} `json:"run"`
	}
	// known is the launch a pending answer named: once the server has
	// one, every exit that leaves it unsettled names it.
	var known *dispatch.LaunchSummary
	for sends := 1; ; sends++ {
		err := c.doWithin(ctx, launchRequestTimeout, http.MethodPost, "/api/dispatch/tickets/"+url.PathEscape(key)+"/launch", body, &out, maxDispatchReply)
		if err == nil {
			break
		}
		var lost lostReply
		if errors.As(err, &lost) && ctx.Err() == nil && sends < launchTries {
			continue
		}
		pending, ok := pendingLaunch(err)
		if !ok {
			if known != nil && !failedLaunch(err) {
				var r replyRefusal
				switch {
				case ctx.Err() != nil:
					return 1, fmt.Errorf("%s: launch %s from r%d was pending, interrupted while it was sent again: conductor dispatch runs %s shows it and its run; launching again would be a second launch", key, oneLine(known.ID), p.N, key)
				case errors.As(err, &r):
					return 1, fmt.Errorf("%s: launch %s from r%d was pending, and sending it again was answered without its outcome (%w): conductor dispatch runs %s shows it and its run; launching again would be a second launch", key, oneLine(known.ID), p.N, err, key)
				}
				return 1, fmt.Errorf("%s: launch %s from r%d was pending, and sending it again had no answer (%w): conductor dispatch runs %s shows it and its run; launching again would be a second launch", key, oneLine(known.ID), p.N, err, key)
			}
			return 1, err
		}
		known = &pending
		if sends >= launchTries {
			return 1, fmt.Errorf("%s: launch %s from r%d is still pending after %d tries: the server settles it at its next start if no request does before; conductor dispatch runs %s shows it and its run; launching again would be a second launch",
				key, oneLine(pending.ID), p.N, sends, key)
		}
		t := time.NewTimer(launchResendWait)
		select {
		case <-ctx.Done():
			t.Stop()
			return 1, fmt.Errorf("%s: launch %s from r%d is still pending, interrupted: conductor dispatch runs %s shows it and its run; launching again would be a second launch", key, oneLine(pending.ID), p.N, key)
		case <-t.C:
		}
	}
	run := cmp.Or(out.Launch.RunID, func() string {
		if out.Run != nil {
			return out.Run.ID
		}
		return ""
	}())
	if run == "" {
		return 1, fmt.Errorf("the reply from %s names no run", c.host)
	}
	fmt.Fprintf(stdout, "%s launched from r%d: launch %s, run %s %s\n%s\n", key, p.N, oneLine(out.Launch.ID), run, strconv.Quote(oneLine(out.Launch.Label)), c.base+"/runs/"+url.PathEscape(run))
	if out.Run != nil && out.Run.Yolo {
		fmt.Fprintln(stdout, "yolo: its members skip their permission prompts")
	}
	return 0, nil
}

// launchRefusal is the launch a refusal with the status and the error
// code carries in its body; false for another error, status or code, a
// body that cannot be read or one that names no launch.
func launchRefusal(err error, status int, code string) (dispatch.LaunchSummary, bool) {
	var r replyRefusal
	if !errors.As(err, &r) || r.status != status {
		return dispatch.LaunchSummary{}, false
	}
	var b struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Launch *dispatch.LaunchSummary `json:"launch"`
	}
	if json.Unmarshal(r.body, &b) != nil || b.Error.Code != code || b.Launch == nil {
		return dispatch.LaunchSummary{}, false
	}
	return *b.Launch, true
}

// pendingLaunch is the launch a refusal carries when it is one still
// pending: a 503 ledger_unavailable whose body names a pending launch,
// which the same operation sent again settles (the ticket page's
// launchOutcome reads it so too).
func pendingLaunch(err error) (dispatch.LaunchSummary, bool) {
	l, ok := launchRefusal(err, http.StatusServiceUnavailable, "ledger_unavailable")
	return l, ok && l.State == dispatch.LaunchPending
}

// failedLaunch is whether a refusal settles a launch as failed: a 409
// launch_failed whose body names the launch failed, the launch handler's
// answer to the operation. Any other refusal of a resend (a 401 or a 429
// from in front of the handler, a 5xx) says nothing of the launch.
func failedLaunch(err error) bool {
	l, ok := launchRefusal(err, http.StatusConflict, "launch_failed")
	return ok && l.State == dispatch.LaunchFailed
}

// runEntryView is an entry of a ticket's runs as the route answers it
// (internal/api, dispatch_evidence.go): a launch with its run once it has
// one and what changed since; a run linked by hand; or another run of a
// chain the ticket holds, with the run it continues.
type runEntryView struct {
	Kind      string                  `json:"kind"`
	Launch    *dispatch.LaunchSummary `json:"launch"`
	Link      *dispatch.Link          `json:"link"`
	Run       *runSummaryView         `json:"run"`
	Changed   *dispatch.LaunchChanges `json:"changed"`
	Contested *struct {
		Keys  []string `json:"keys"`
		Words string   `json:"words"`
	} `json:"contested"`
	Continues string `json:"continues"`
}

// runSummaryView is a run as a ticket's runs summarize it.
type runSummaryView struct {
	ID          string          `json:"id"`
	Label       string          `json:"label"`
	State       string          `json:"state"`
	Outcome     string          `json:"outcome"`
	Paused      bool            `json:"paused"`
	StartedAt   time.Time       `json:"startedAt"`
	StoppedAt   *time.Time      `json:"stoppedAt"`
	Record      bool            `json:"record"`
	ResumedFrom string          `json:"resumedFrom"`
	ResumedBy   string          `json:"resumedBy"`
	Ticket      *crew.RunTicket `json:"ticket"`
	Members     []struct {
		Name      string `json:"name"`
		SessionID string `json:"sessionId"`
		Status    string `json:"status"`
	} `json:"members"`
	Tasks     map[string]int `json:"tasks"`
	Questions int            `json:"questions"`
	Blockers  []struct {
		Task   string    `json:"task"`
		Reason string    `json:"reason"`
		By     string    `json:"by"`
		At     time.Time `json:"at"`
	} `json:"blockers"`
}

// dispatchRuns prints every entry of the ticket's runs, page by page,
// newest first: each launch with its revision, state and run, each run
// linked by hand, each other run of a chain the ticket holds with the run
// it continues; each run's state, tasks, questions and blockers; what
// changed of the ticket since a launch; a contested chain's words.
func dispatchRuns(ctx context.Context, c *apiClient, key string, stdout io.Writer) (int, error) {
	var all []runEntryView
	for cursor := ""; ; {
		path := "/api/dispatch/tickets/" + url.PathEscape(key) + "/runs"
		if cursor != "" {
			path += "?" + url.Values{"before": {cursor}}.Encode()
		}
		var page struct {
			Entries []runEntryView `json:"entries"`
			Next    string         `json:"next"`
		}
		if err := c.doLimit(ctx, http.MethodGet, path, nil, &page, maxDispatchReply); err != nil {
			return 1, err
		}
		all = append(all, page.Entries...)
		if page.Next == "" || page.Next == cursor || len(page.Entries) == 0 {
			break
		}
		cursor = page.Next
	}
	if len(all) == 0 {
		fmt.Fprintf(stdout, "%s has no runs\n", key)
		return 0, nil
	}
	for _, e := range all {
		writeRunEntry(stdout, e)
	}
	return 0, nil
}

// writeRunEntry prints an entry of a ticket's runs: its head line, then
// its run's, each further line indented. The entry's kind says what it
// is: a continuation (kind run) carries the launch or the link that holds
// its chain too, and is printed as the run it is, with the run it
// continues.
func writeRunEntry(w io.Writer, e runEntryView) {
	switch {
	case e.Kind == "launch" && e.Launch != nil:
		l := e.Launch
		fmt.Fprintln(w, tabbed("launch "+l.ID, "r"+strconv.Itoa(l.PlanN), l.State, when(l.CreatedAt)+" by "+cmp.Or(l.CreatedBy, "admin"), strconv.Quote(l.Label)))
		switch l.State {
		case dispatch.LaunchFailed:
			fmt.Fprintf(w, "  failed: %s\n", oneLine(l.Why))
		case dispatch.LaunchPending:
			fmt.Fprintf(w, "  pending: its run %s has not been seen to start yet\n", l.RunID)
		}
		for _, a := range l.Agents {
			fmt.Fprintf(w, "  member %s: %s %s\n", oneLine(a.Member), oneLine(a.Agent), oneLine(cmp.Or(a.Version, "(no version reported)")))
		}
		if e.Changed != nil {
			fmt.Fprintf(w, "  %s\n", oneLine(changedWords(l.TicketRev, *e.Changed)))
		}
	case e.Kind == "link" && e.Link != nil:
		fmt.Fprintln(w, tabbed("linked run "+e.Link.Ref, "by "+cmp.Or(e.Link.CreatedBy, "admin")+" "+when(e.Link.CreatedAt)))
	default:
		head := []string{"run"}
		if e.Run != nil {
			head[0] += " " + e.Run.ID
		}
		if e.Continues != "" {
			head = append(head, "continues "+e.Continues)
		}
		fmt.Fprintln(w, tabbed(head...))
	}
	if e.Contested != nil {
		fmt.Fprintf(w, "  contested with %s: %s\n", oneLine(strings.Join(e.Contested.Keys, ", ")), oneLine(e.Contested.Words))
	}
	if r := e.Run; r != nil {
		writeRunSummary(w, *r)
	}
}

// writeRunSummary prints a run's lines under its entry: its id, label and
// state, its tasks counted by state, the questions its members waited on
// when the reply was made, its blockers.
func writeRunSummary(w io.Writer, r runSummaryView) {
	state := r.State
	if r.Outcome != "" {
		state += " (" + r.Outcome + ")"
	}
	if r.Paused {
		state += ", paused"
	}
	started := "started " + when(r.StartedAt)
	if r.StoppedAt != nil {
		started += ", stopped " + when(*r.StoppedAt)
	}
	parts := []string{"  run " + r.ID, state, strconv.Quote(r.Label), started}
	if r.Record {
		parts = append(parts, "from its record")
	}
	fmt.Fprintln(w, tabbed(parts...))
	if len(r.Tasks) > 0 {
		states := make([]string, 0, len(r.Tasks))
		for s := range r.Tasks {
			states = append(states, s)
		}
		slices.Sort(states)
		parts := make([]string, len(states))
		for i, s := range states {
			parts[i] = fmt.Sprintf("%d %s", r.Tasks[s], s)
		}
		fmt.Fprintf(w, "    tasks: %s\n", oneLine(strings.Join(parts, ", ")))
	}
	if r.Questions > 0 {
		fmt.Fprintf(w, "    questions: %d of its members wait on a person\n", r.Questions)
	}
	for _, b := range r.Blockers {
		fmt.Fprintf(w, "    blocked %s: %s (%s, %s)\n", oneLine(b.Task), oneLine(b.Reason), oneLine(b.By), when(b.At))
	}
}

// tabbed is the parts of a line, each for one line of a terminal
// (oneLine), separated by tabs.
func tabbed(parts ...string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = oneLine(p)
	}
	return strings.Join(out, "\t")
}

// changedWords says what changed of a ticket since a launch made at its
// revision rev: the intent fields edited, the comments added, the
// revision now; or that nothing did.
func changedWords(rev int64, c dispatch.LaunchChanges) string {
	if c.Rev == rev && len(c.Fields) == 0 && c.Comments == 0 {
		return fmt.Sprintf("unchanged since the launch, at revision %d", rev)
	}
	what := "what was asked is unchanged"
	if len(c.Fields) > 0 {
		words := make([]string, len(c.Fields))
		for i, f := range c.Fields {
			words[i] = map[string]string{"title": "the title", "description": "the description", "acceptance": "the acceptance criteria"}[f]
			if words[i] == "" {
				words[i] = "the " + f
			}
		}
		what = joinWords(words) + " changed"
	}
	comments := "no comment since"
	switch c.Comments {
	case 0:
	case 1:
		comments = "1 comment since"
	default:
		comments = fmt.Sprintf("%d comments since", c.Comments)
	}
	return fmt.Sprintf("now at revision %d: %s; %s", c.Rev, what, comments)
}

// joinWords joins words as a sentence does: a, b and c.
func joinWords(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// sessionLaunchView is the launch a session's run carries in its package,
// as the session's ticket route answers it: the launch, the ticket's
// revision it was launched at, the plan revision, the package's digest
// and what changed since (nil when the store cannot tell: a launch a
// replica made).
type sessionLaunchView struct {
	ID   string `json:"id"`
	Rev  int64  `json:"rev"`
	Plan struct {
		ID string `json:"id"`
		N  int    `json:"n"`
	} `json:"plan"`
	Digest  string                  `json:"digest"`
	Changed *dispatch.LaunchChanges `json:"changed"`
}

// launchText is what conductor task ticket and ticket_read say first of a
// run launched from its ticket: the revision it was launched at, the plan
// revision and where the package is; what changed since; and that the run
// follows the ticket as launched, a change being a person's to decide.
func launchText(key string, l sessionLaunchView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "As launched: %s at revision %d, plan revision %d, in .conductor/ticket.md (the package: .conductor/package.json, the attachments in .conductor/attachments/).", oneLine(key), l.Rev, l.Plan.N)
	switch c := l.Changed; {
	case c == nil:
		b.WriteString(" Whether it changed since cannot be read here.")
	case c.Rev == l.Rev && len(c.Fields) == 0 && c.Comments == 0:
		b.WriteString(" It is unchanged since.")
	default:
		s := changedWords(l.Rev, *c)
		b.WriteString(" " + strings.ToUpper(s[:1]) + s[1:] + ".")
	}
	b.WriteString(" Your run follows the ticket as launched: a change that matters to your work is a person's to decide, so tell them (ticket_comment, or task_blocked when it stops you), never act on it alone.")
	return b.String()
}

// candidateWords is what a delivery link's printout says of the candidate
// it names: its verification as the link recorded it, from the ticket the
// reply carries; "" for no candidate.
func candidateWords(tv *dispatch.TicketView, candidate string) string {
	if candidate == "" {
		return ""
	}
	s := ", candidate " + oneLine(candidate)
	if tv == nil {
		return s
	}
	for _, l := range tv.Links {
		if l.Candidate == candidate && l.DeletedAt.IsZero() {
			return s + verifiedWords(l.Verified)
		}
	}
	return s
}

// verifiedWords is a delivery's recorded verification receipt in words:
// whether its candidate passed its checks, and when it did not, how it fell
// short as the server's dispatch.shortfall tells it: no check, a receipt
// that did not pass (unfinished, a non-zero exit, unstable), else every
// check passed but its attempt did not succeed (one stopped, say).
func verifiedWords(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v dispatch.Verified
	if err := json.Unmarshal(raw, &v); err != nil {
		return ", its verification unreadable"
	}
	switch {
	case v.Passed:
		return fmt.Sprintf(", verified: %s passed", checksWords(len(v.Receipts)))
	case len(v.Receipts) == 0:
		return ", not verified: it ran no check"
	case slices.ContainsFunc(v.Receipts, func(r dispatch.VerifiedReceipt) bool { return r.Exit == nil || *r.Exit != 0 || !r.Stable }):
		return ", not verified: its checks did not all pass"
	}
	return fmt.Sprintf(", not verified: %s passed, but its attempt did not succeed", checksWords(len(v.Receipts)))
}
