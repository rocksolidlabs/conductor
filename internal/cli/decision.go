package cli

import (
	"bytes"
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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// Durable decisions from the shell (Program item DR1,
// docs/plans/decisions.md, The changes, internal/cli): an attempt's agent
// asks, reads and withdraws its task's decisions with conductor task
// decide, decision and withdraw, on its own session's routes; the
// operator lists, reads, answers and cancels them with conductor decision,
// on the admin's routes, with the server and the token conductor dispatch
// takes. Every write mints its id (requestId, responseId, commandId) and a
// reply lost to the network is asked for once more with the same id, which
// the server answers from its receipt without a second change. An answer
// and a cancel read the decision's rev first and send it, so an answer to
// a decision that changed since it was read is refused, not taken.

const decisionUsage = `Usage:
  conductor decision list [--state S] [--run RUN] [--ticket KEY-n] [--json]
      the decisions, newest first, one a line: id, state, rev, type, where
      it waits (run and task, or the plan's gate) and question; the whole
      list, filtered by state, by the run that holds them and by the
      Dispatch ticket their run was launched from
  conductor decision show dr-N [--json]
      a decision whole: the question, its options numbered from 0, the
      recommendation, the subject, the evidence and what changed of it
      since it was asked, the responses with their receipts, how
      Yardmaster applied the answer, and its history
  conductor decision respond dr-N (--choice N | --text TEXT) [--why TEXT]
      answer it as the operator: an option by its number from 0, as show
      numbers them (an approval's and an assignment's answer), a text (a
      clarification's, which may also pick an option), and the rationale;
      the decision's rev is read first and sent, so an answer to one that
      changed since is refused; prints the receipt, dr-N/K (the K-th
      response to dr-N), and where the decision is now
  conductor decision cancel dr-N --why TEXT [--release]
      cancel an open or answered decision: the tasks it holds are
      cancelled with it, or released to run without its answer with
      --release

dr-N is a decision's id, as every command and page names it. States: open,
answered, applied, superseded, cancelled, expired.

Every command takes --server, --token and --insecure as conductor dispatch
does: the server defaults to CONDUCTOR_SERVER, else http://localhost:8080,
the token to CONDUCTOR_WORKBENCH_TOKEN; only the operator (the workbench
token) answers a decision. A write whose reply is lost to the network is
sent once more with the same id, which the server answers without a second
change.
`

// decisionStates is every state a decision can be in, as --state takes
// them.
var decisionStates = []string{ledger.DecisionOpen, ledger.DecisionAnswered, ledger.DecisionApplied, ledger.DecisionSuperseded, ledger.DecisionCancelled, ledger.DecisionExpired}

// decisionsPage is how many records conductor decision list asks for a
// page at a time: each is at most a decision's whole reply (its history
// left out), so a page is at most that many of them. A variable so that
// a test can walk a few through several pages.
var decisionsPage = 10

// decisionReply bounds a read of one decision whole, above the server's
// own bound (decision_read's reply in the guide's table, which the
// admin's read with its refs and an answer's reply are under too), and a
// page of the list is at most decisionsPage of them: derived from the
// table, never guessed.
var decisionReply = wholeMiB(opBound("decision_read", "reply"))

// decisionView is a decision's request as its record keeps it.
type decisionView struct {
	Question       string                `json:"question"`
	Context        string                `json:"context"`
	Options        []crew.DecisionOption `json:"options"`
	Recommendation *int                  `json:"recommendation"`
	Uncertainty    string                `json:"uncertainty"`
	Authorizes     string                `json:"authorizes"`
	Evidence       []string              `json:"evidence"`
	Expertise      []string              `json:"expertise"`
	Subject        *crew.DecisionSubject `json:"subject"`
	Supersedes     string                `json:"supersedes"`
}

// decisionRef is one evidence ref of the admin's read, resolved: what its
// record was when the question was asked, what it is now, and the change.
type decisionRef struct {
	Ref    string `json:"ref"`
	Seen   refAt  `json:"seen"`
	Now    refAt  `json:"now"`
	Change string `json:"change"`
}

type refAt struct {
	State string `json:"state"`
	Rev   string `json:"rev"`
}

// decisionSummary is a decision as an attempt's list gives it
// (GET /api/sessions/{id}/decisions).
type decisionSummary struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Gate         string `json:"gate"`
	State        string `json:"state"`
	Rev          int    `json:"rev"`
	Question     string `json:"question"`
	Answer       string `json:"answer"`
	Why          string `json:"why"`
	SupersededBy string `json:"supersededBy"`
	Cut          bool   `json:"cut"`
}

// pendingWords is what a request's answer says: the decision's id and
// state, and what to do while it waits (the same as decision_request's
// tool says).
func pendingWords(id, state any) string {
	return fmt.Sprintf("%v %v. Pending. End your turn, or go on with work that does not depend on it; do not submit your result until it is answered. "+
		"The answer is typed into this session as 'Decision %v … answered', or, if this session has ended, written into your task's next attempt's task file, "+
		"which starts at your last checkpoint: save one before you end your turn.", id, state, id)
}

// withdrawnWords is what a withdrawal's answer says.
func withdrawnWords(id, state any) string {
	return fmt.Sprintf("%v is %v: your task waits for it no more", id, state)
}

// parseOption reads --option "LABEL[: TRADEOFFS]": the label up to the
// first ": ", the tradeoffs after it.
func parseOption(s string) (crew.DecisionOption, error) {
	label, trade, _ := strings.Cut(s, ": ")
	o := crew.DecisionOption{Label: strings.TrimSpace(label), Tradeoffs: strings.TrimSpace(trade)}
	if o.Label == "" {
		return o, fmt.Errorf(`an option is "LABEL[: TRADEOFFS]", its label not empty: %q`, s)
	}
	return o, nil
}

// parseSubject reads --subject: commit (the attempt's HEAD) or
// commit:SHA, candidate:RUN/TASK/N@COMMIT (a result attempt N of the
// task submitted, its commit 7 hex digits at least), or artifact:PATH (a
// file committed in the worktree, relative to it).
func parseSubject(s string) (*crew.DecisionSubject, error) {
	bad := fmt.Errorf("a subject is commit, commit:SHA, candidate:RUN/TASK/N@COMMIT or artifact:PATH: %q", s)
	kind, rest, _ := strings.Cut(s, ":")
	switch kind {
	case "commit":
		return &crew.DecisionSubject{Kind: "commit", Commit: rest}, nil
	case "artifact":
		if rest == "" {
			return nil, bad
		}
		return &crew.DecisionSubject{Kind: "artifact", Path: rest}, nil
	case "candidate":
		where, commit, ok := strings.Cut(rest, "@")
		parts := strings.Split(where, "/")
		if !ok || len(parts) != 3 || parts[0] == "" || parts[1] == "" || len(commit) < 7 {
			return nil, bad
		}
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 1 {
			return nil, bad
		}
		return &crew.DecisionSubject{Kind: "candidate", Run: parts[0], Task: parts[1], N: n, Commit: commit}, nil
	}
	return nil, bad
}

// decideRequest is task decide's request, from --file or the flags; its
// errors are usage errors.
func decideRequest(f decideFlags, stdin io.Reader) (crew.DecisionRequest, error) {
	var req crew.DecisionRequest
	if *f.file != "" {
		if f.given() {
			return req, errors.New("give --file or the flags, not both")
		}
		if err := readStrictJSON(*f.file, stdin, &req); err != nil {
			return req, err
		}
		return req, nil
	}
	if *f.typ == "" || *f.question == "" {
		return req, errors.New("--type (clarification, approval or assignment) and --question say what you ask")
	}
	req = crew.DecisionRequest{Type: *f.typ, Question: *f.question, Context: *f.context, Uncertainty: *f.uncertainty,
		Authorizes: *f.authorizes, Evidence: []string(*f.evidence), Supersedes: *f.supersedes}
	for _, o := range *f.options {
		opt, err := parseOption(o)
		if err != nil {
			return req, err
		}
		req.Options = append(req.Options, opt)
	}
	if *f.recommend != -1 {
		if *f.recommend < -1 || *f.recommend >= len(req.Options) {
			return req, fmt.Errorf("--recommend is an option's number from 0, of the %d given: %d", len(req.Options), *f.recommend)
		}
		req.Recommendation = f.recommend
	}
	if *f.subject != "" {
		s, err := parseSubject(*f.subject)
		if err != nil {
			return req, err
		}
		req.Subject = s
	}
	if req.Supersedes != "" {
		if _, ok := ledger.ParseDecisionID(req.Supersedes); !ok {
			return req, fmt.Errorf("--supersedes is a decision's id, dr-N: %q", req.Supersedes)
		}
	}
	return req, nil
}

// decideFlags is task decide's flags, on the task command's flag set.
type decideFlags struct {
	typ, question, context, uncertainty, authorizes, subject, supersedes, file *string
	recommend                                                                  *int
	options, evidence                                                          *multiFlag
}

// given reports whether any flag of a request was given beside --file.
func (f decideFlags) given() bool {
	return *f.typ != "" || *f.question != "" || *f.context != "" || *f.uncertainty != "" || *f.authorizes != "" || *f.subject != "" ||
		*f.supersedes != "" || *f.recommend != -1 || len(*f.options) > 0 || len(*f.evidence) > 0
}

// readStrictJSON reads the file named, or stdin for "-", as strict JSON
// into v: an unknown field is refused, as the server refuses it.
func readStrictJSON(file string, stdin io.Reader, v any) error {
	var r io.Reader = stdin
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	dec := json.NewDecoder(io.LimitReader(r, maxResultFile))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}

// taskDecide sends the attempt's decision request, its requestId minted
// when the file names none, once more with it on a lost reply; it prints
// the decision's id and state with what to do while it waits.
func taskDecide(ctx context.Context, c *crewClient, req crew.DecisionRequest, stdout io.Writer) (int, error) {
	if req.RequestID == "" {
		req.RequestID = newOpID()
	}
	var out struct {
		Decision crew.Decision `json:"decision"`
	}
	if err := askTwice(ctx, func(ctx context.Context) error { return c.into(ctx, http.MethodPost, "/decisions", req, &out) }); err != nil {
		return 1, err
	}
	fmt.Fprintln(stdout, pendingWords(out.Decision.ID, out.Decision.State))
	return 0, nil
}

// taskDecisions lists the attempt's task's decisions, newest first,
// following the pages to the end: one a line, or their summaries as one
// JSON array.
func taskDecisions(ctx context.Context, c *crewClient, asJSON bool, stdout io.Writer) (int, error) {
	var all []json.RawMessage
	after := ""
	for {
		path := "/decisions"
		if after != "" {
			path += "?after=" + url.QueryEscape(after)
		}
		var page struct {
			Decisions []json.RawMessage `json:"decisions"`
			Next      string            `json:"next"`
		}
		if err := c.into(ctx, http.MethodGet, path, nil, &page); err != nil {
			return 1, err
		}
		all = append(all, page.Decisions...)
		if !nextPage(after, page.Next) {
			break
		}
		after = page.Next
	}
	if asJSON {
		return printJSON(stdout, all)
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "your task has no decisions")
		return 0, nil
	}
	for _, raw := range all {
		var s decisionSummary
		if err := json.Unmarshal(raw, &s); err != nil {
			return 1, fmt.Errorf("task: an unreadable decision: %w", err)
		}
		fmt.Fprint(stdout, summaryText(s))
	}
	return 0, nil
}

// nextPage reports whether a list goes on after the page that named next,
// read after after: a next that is no decision's id, or does not come
// before after, ends it, so a server that names the same page again is not
// asked for it forever.
func nextPage(after, next string) bool {
	n, ok := ledger.ParseDecisionID(next)
	if !ok {
		return false
	}
	if after == "" {
		return true
	}
	a, _ := ledger.ParseDecisionID(after)
	return n < a
}

// summaryText is a summary as task decision lists it: its id, type, state
// and rev with its question, then its answer and why, each on a line of
// its own.
func summaryText(s decisionSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, %s, rev %d", s.ID, s.Type, s.State, s.Rev)
	if s.Gate != "" {
		fmt.Fprintf(&b, ", the plan's %s", oneLine(s.Gate))
	}
	fmt.Fprintf(&b, "): %s\n", oneLine(s.Question))
	if s.Answer != "" {
		fmt.Fprintf(&b, "  answered: %s\n", oneLine(s.Answer))
	}
	if s.Why != "" {
		fmt.Fprintf(&b, "  why: %s\n", oneLine(s.Why))
	}
	if s.SupersededBy != "" {
		fmt.Fprintf(&b, "  superseded by %s\n", s.SupersededBy)
	}
	if s.Cut {
		fmt.Fprintf(&b, "  (cut: task decision %s reads it whole)\n", s.ID)
	}
	return b.String()
}

// printJSON prints v indented.
func printJSON(stdout io.Writer, v any) (int, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return 1, err
	}
	fmt.Fprintln(stdout, string(b))
	return 0, nil
}

// taskDecisionRead prints one of the attempt's task's decisions whole, or
// its JSON.
func taskDecisionRead(ctx context.Context, c *crewClient, id string, asJSON bool, stdout io.Writer) (int, error) {
	var raw json.RawMessage
	if err := c.into(ctx, http.MethodGet, "/decisions/"+id, nil, &raw); err != nil {
		return 1, err
	}
	var out struct {
		Decision crew.Decision `json:"decision"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 1, fmt.Errorf("task: an unreadable decision: %w", err)
	}
	if asJSON {
		return printJSON(stdout, out.Decision)
	}
	fmt.Fprint(stdout, decisionText(out.Decision, nil, false))
	return 0, nil
}

// taskWithdraw withdraws an open decision the attempt asked.
func taskWithdraw(ctx context.Context, c *crewClient, id string, stdout io.Writer) (int, error) {
	var out struct {
		Decision crew.Decision `json:"decision"`
	}
	if err := c.into(ctx, http.MethodPost, "/decisions/"+id+"/withdraw", map[string]any{}, &out); err != nil {
		return 1, err
	}
	fmt.Fprintln(stdout, withdrawnWords(out.Decision.ID, out.Decision.State))
	return 0, nil
}

// decisionText is a decision in words: what was asked, its options
// numbered from 0 as an answer names them, the subject and the evidence
// (with what changed of each since, from refs, for the admin's read),
// the answer, the responses with their receipts (dr-N/K), the
// application, and, with history, the history.
func decisionText(d crew.Decision, refs []decisionRef, history bool) string {
	var v decisionView
	_ = json.Unmarshal(d.Request, &v)
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s, %s, rev %d\n", d.ID, d.Type, d.State, d.Rev)
	switch {
	case d.Gate != "":
		fmt.Fprintf(&b, "asked by: the plan's decision %s, in run %s\n", oneLine(d.Gate), d.Run)
	case d.Asker != nil:
		live := ""
		if d.Asker.Live {
			live = ", live"
		}
		fmt.Fprintf(&b, "asked by: task %s's attempt %d%s, in run %s\n", d.Asker.Task, d.Asker.Attempt, live, d.Run)
	}
	if d.Ticket != "" {
		fmt.Fprintf(&b, "ticket: %s\n", d.Ticket)
	}
	fmt.Fprintf(&b, "question: %s\n", oneLine(v.Question))
	if v.Context != "" {
		fmt.Fprintf(&b, "context:\n%s\n", indent(v.Context))
	}
	options := v.Options
	if d.Type == ledger.DecisionAssignment {
		options = []crew.DecisionOption{{Label: "Done"}, {Label: "Not done"}}
	}
	if len(options) > 0 {
		b.WriteString("options (answer with the number):\n")
		for i, o := range options {
			fmt.Fprintf(&b, "  %d. %s", i, oneLine(o.Label))
			if o.Tradeoffs != "" {
				fmt.Fprintf(&b, ": %s", oneLine(o.Tradeoffs))
			}
			if v.Recommendation != nil && *v.Recommendation == i {
				b.WriteString(" (recommended)")
			}
			b.WriteByte('\n')
		}
	}
	for _, f := range []struct{ name, text string }{{"uncertainty", v.Uncertainty}, {"authorizes", v.Authorizes}} {
		if f.text != "" {
			fmt.Fprintf(&b, "%s: %s\n", f.name, oneLine(f.text))
		}
	}
	if len(v.Expertise) > 0 {
		fmt.Fprintf(&b, "expertise: %s\n", oneLine(strings.Join(v.Expertise, ", ")))
	}
	if s := subjectWords(v.Subject); s != "" {
		fmt.Fprintf(&b, "subject: %s\n", s)
	}
	if v.Supersedes != "" {
		fmt.Fprintf(&b, "supersedes: %s\n", v.Supersedes)
	}
	if len(v.Evidence) > 0 {
		b.WriteString("evidence:\n")
		for i, ref := range v.Evidence {
			fmt.Fprintf(&b, "  %s", oneLine(ref))
			if i < len(refs) {
				b.WriteString(refWords(refs[i]))
			}
			b.WriteByte('\n')
		}
	}
	if d.Answer != "" {
		fmt.Fprintf(&b, "answer: %s\n", oneLine(d.Answer))
	}
	if len(d.Responses) > 0 {
		b.WriteString("responses:\n")
		for _, r := range d.Responses {
			fmt.Fprintf(&b, "  %s/%d %s by %s at %s: %s\n", d.ID, r.K, r.Status, oneLine(cmp.Or(r.ByName, r.Actor)), r.At.UTC().Format(time.RFC3339), oneLine(r.Words))
		}
	}
	if a := d.Applied; a != nil {
		fmt.Fprintf(&b, "applied: %s (%s, response %s/%d, %s)\n", oneLine(a.Words), a.How, d.ID, a.Response, a.At.UTC().Format(time.RFC3339))
	}
	if d.Why != "" {
		fmt.Fprintf(&b, "why: %s\n", oneLine(d.Why))
	}
	if d.SupersededBy != "" {
		fmt.Fprintf(&b, "superseded by: %s\n", d.SupersededBy)
	}
	if history && len(d.History) > 0 {
		b.WriteString("history:\n")
		for _, h := range d.History {
			fmt.Fprintf(&b, "  %d %s %s", h.Seq, h.At.UTC().Format(time.RFC3339), h.Kind)
			if h.By != "" {
				fmt.Fprintf(&b, " by %s", oneLine(h.By))
			}
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// indent is text with each line indented by two spaces, its control
// characters shown as such.
func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + oneLine(l)
	}
	return strings.Join(lines, "\n")
}

// subjectWords is a subject in words: the commit, the candidate or the
// artifact with its commit and digest.
func subjectWords(s *crew.DecisionSubject) string {
	if s == nil {
		return ""
	}
	switch s.Kind {
	case "commit":
		return "commit " + oneLine(s.Commit)
	case "candidate":
		return fmt.Sprintf("candidate %s/%s/%d at %s", oneLine(s.Run), oneLine(s.Task), s.N, oneLine(s.Commit))
	case "artifact":
		return fmt.Sprintf("artifact %s at %s (sha256 %s)", oneLine(s.Path), oneLine(s.Commit), oneLine(s.SHA256))
	case "plan":
		return fmt.Sprintf("plan revision %s (digest %s)", oneLine(s.Plan), oneLine(s.Digest))
	}
	return oneLine(s.Kind)
}

// refWords is what changed of an evidence ref since the question was
// asked, "" for nothing.
func refWords(r decisionRef) string {
	at := func(a refAt) string {
		if a.Rev != "" {
			return a.State + " at " + a.Rev
		}
		return a.State
	}
	switch r.Change {
	case "":
		return ""
	case "unread":
		return " (could not be read when it was asked; now " + oneLine(at(r.Now)) + ")"
	}
	return fmt.Sprintf(" (%s: was %s, now %s)", oneLine(r.Change), oneLine(at(r.Seen)), oneLine(at(r.Now)))
}

// runDecision is conductor decision, the operator's. Exit 2 for a usage
// error, before any request; 1 when the server refuses or cannot be
// asked.
func runDecision(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, decisionUsage)
		if len(args) == 0 {
			return 2, nil
		}
		return 0, nil
	}
	sub, rest := args[0], args[1:]
	if !slices.Contains([]string{"list", "show", "respond", "cancel"}, sub) {
		fmt.Fprint(stderr, decisionUsage)
		return 2, fmt.Errorf("decision: unknown command %q", sub)
	}
	fs := flag.NewFlagSet("decision "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := addAPIFlags(fs)
	var (
		state, run, ticket, text, why *string
		choice                        *int
		asJSON, release               *bool
	)
	switch sub {
	case "list":
		state = fs.String("state", "", "a state: the decisions in it alone ("+strings.Join(decisionStates, ", ")+")")
		run = fs.String("run", "", "a run's id: the decisions it holds")
		ticket = fs.String("ticket", "", "a Dispatch ticket's key, as CND-12: the decisions of the runs launched from it")
		asJSON = fs.Bool("json", false, "print the decisions as one JSON array")
	case "show":
		asJSON = fs.Bool("json", false, "print the server's reply: the decision and its evidence's refs")
	case "respond":
		choice = fs.Int("choice", -1, "the option chosen, by its number from 0 as show numbers them")
		text = fs.String("text", "", "the answer in words: a clarification's")
		why = fs.String("why", "", "the rationale, kept with the answer and typed with it")
	case "cancel":
		why = fs.String("why", "", "why it is cancelled (required)")
		release = fs.Bool("release", false, "release the tasks it holds to run without its answer, instead of cancelling them")
	}
	fs.Usage = func() {
		fmt.Fprint(stderr, decisionUsage, "\nFlags of decision "+sub+":\n")
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	var id string
	if sub == "list" {
		if len(pos) != 0 {
			return 2, fmt.Errorf("decision list takes no argument: %q", pos[0])
		}
		if *state != "" && !slices.Contains(decisionStates, *state) {
			return 2, fmt.Errorf("decision list: a state is one of %s: %q", strings.Join(decisionStates, ", "), *state)
		}
		if *run != "" && !crew.ValidRunID(*run) {
			return 2, fmt.Errorf("decision list: --run is a run's id: %q", *run)
		}
		if _, _, ok := dispatch.ParseKey(*ticket); *ticket != "" && !ok {
			return 2, fmt.Errorf("decision list: --ticket is a ticket's key, as CND-12: %q", *ticket)
		}
	} else {
		if len(pos) != 1 {
			return 2, fmt.Errorf("decision %s takes a decision's id, dr-N", sub)
		}
		id = pos[0]
		if _, ok := ledger.ParseDecisionID(id); !ok {
			return 2, fmt.Errorf("decision %s: %q is no decision's id: dr and its number, as dr-7", sub, id)
		}
	}
	switch {
	case sub == "respond" && *choice == -1 && *text == "":
		return 2, errors.New("decision respond: answer with --choice N (an option's number from 0), --text TEXT, or both for a clarification")
	case sub == "respond" && *choice < -1:
		return 2, fmt.Errorf("decision respond: --choice is an option's number from 0: %d", *choice)
	case sub == "cancel" && strings.TrimSpace(*why) == "":
		return 2, errors.New("decision cancel: --why says why it is cancelled")
	}
	c, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	switch sub {
	case "list":
		return decisionList(ctx, c, *state, *run, *ticket, *asJSON, stdout)
	case "show":
		return decisionShow(ctx, c, id, *asJSON, stdout)
	case "respond":
		resp := crew.Response{Text: *text, Rationale: *why}
		if *choice >= 0 {
			resp.Choice = choice
		}
		return decisionRespond(ctx, c, id, resp, stdout)
	}
	return decisionCancel(ctx, c, id, *why, *release, stdout)
}

// adminDecision reads a decision whole through the admin's route: the
// reply as sent, the decision and its evidence's refs.
func adminDecision(ctx context.Context, c *apiClient, id string) (json.RawMessage, crew.Decision, []decisionRef, error) {
	var raw json.RawMessage
	if err := c.doLimit(ctx, http.MethodGet, "/api/decisions/"+url.PathEscape(id), nil, &raw, decisionReply); err != nil {
		return nil, crew.Decision{}, nil, err
	}
	var out struct {
		Decision crew.Decision `json:"decision"`
		Refs     []decisionRef `json:"refs"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, crew.Decision{}, nil, fmt.Errorf("an unreadable decision: %w", err)
	}
	return raw, out.Decision, out.Refs, nil
}

// decisionList lists the decisions the filters name, following the pages
// to the end: one a line, or one JSON array.
func decisionList(ctx context.Context, c *apiClient, state, run, ticket string, asJSON bool, stdout io.Writer) (int, error) {
	var all []crew.Decision
	after := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(decisionsPage)}}
		for k, v := range map[string]string{"state": state, "run": run, "ticket": ticket, "after": after} {
			if v != "" {
				q.Set(k, v)
			}
		}
		var page struct {
			Decisions []crew.Decision `json:"decisions"`
			Next      string          `json:"next"`
		}
		if err := c.doLimit(ctx, http.MethodGet, "/api/decisions?"+q.Encode(), nil, &page, wholeMiB(decisionsPage*opBound("decision_read", "reply"))); err != nil {
			return 1, err
		}
		all = append(all, page.Decisions...)
		if !nextPage(after, page.Next) {
			break
		}
		after = page.Next
	}
	if asJSON {
		if all == nil {
			all = []crew.Decision{}
		}
		return printJSON(stdout, all)
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "no decisions")
		return 0, nil
	}
	for _, d := range all {
		var v decisionView
		_ = json.Unmarshal(d.Request, &v)
		where := "run " + d.Run
		switch {
		case d.Gate != "":
			where += ", the plan's " + oneLine(d.Gate)
		case d.Asker != nil:
			where += ", task " + d.Asker.Task
		}
		fmt.Fprintf(stdout, "%s\t%s\trev %d\t%s\t%s\t%s\n", d.ID, d.State, d.Rev, d.Type, where, oneLine(v.Question))
	}
	return 0, nil
}

// decisionShow prints a decision whole, with what changed of its
// evidence and its history, or the server's reply as JSON.
func decisionShow(ctx context.Context, c *apiClient, id string, asJSON bool, stdout io.Writer) (int, error) {
	raw, d, refs, err := adminDecision(ctx, c, id)
	if err != nil {
		return 1, err
	}
	if asJSON {
		var b bytes.Buffer
		if err := json.Indent(&b, raw, "", "  "); err != nil {
			return 1, err
		}
		fmt.Fprintln(stdout, b.String())
		return 0, nil
	}
	fmt.Fprint(stdout, decisionText(d, refs, true))
	return 0, nil
}

// decisionRespond reads the decision's rev, then sends the operator's
// answer against it with a minted responseId (once more with it on a lost
// reply), and prints the receipt and where the decision is now.
func decisionRespond(ctx context.Context, c *apiClient, id string, resp crew.Response, stdout io.Writer) (int, error) {
	_, d, _, err := adminDecision(ctx, c, id)
	if err != nil {
		return 1, err
	}
	resp.ResponseID, resp.ExpectedRev = newOpID(), d.Rev
	var out struct {
		Receipt  crew.ResponseReceipt `json:"receipt"`
		Decision crew.Decision        `json:"decision"`
	}
	path := "/api/decisions/" + url.PathEscape(id) + "/respond"
	if err := askTwice(ctx, func(ctx context.Context) error {
		return c.doLimit(ctx, http.MethodPost, path, resp, &out, decisionReply)
	}); err != nil {
		return 1, err
	}
	rc := out.Receipt
	fmt.Fprintf(stdout, "%s (receipt %s/%d, rev %d)\n", oneLine(rc.Words), cmp.Or(rc.Decision, id), rc.K, rc.Rev)
	fmt.Fprintln(stdout, nowWords(out.Decision))
	return 0, nil
}

// decisionCancel reads the decision's rev, then cancels it against it
// with a minted commandId (once more with it on a lost reply), and prints
// the receipt and where the decision is now.
func decisionCancel(ctx context.Context, c *apiClient, id, why string, release bool, stdout io.Writer) (int, error) {
	_, d, _, err := adminDecision(ctx, c, id)
	if err != nil {
		return 1, err
	}
	body := map[string]any{"commandId": newOpID(), "why": why, "expectedRev": d.Rev}
	if release {
		body["release"] = true
	}
	var out struct {
		Receipt  crew.OpReceipt `json:"receipt"`
		Decision crew.Decision  `json:"decision"`
	}
	path := "/api/decisions/" + url.PathEscape(id) + "/cancel"
	if err := askTwice(ctx, func(ctx context.Context) error {
		return c.doLimit(ctx, http.MethodPost, path, body, &out, decisionReply)
	}); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "%s (rev %d)\n", oneLine(out.Receipt.Words), out.Receipt.Rev)
	fmt.Fprintln(stdout, nowWords(out.Decision))
	return 0, nil
}

// nowWords is where a decision is now: its state and rev, and the
// application once there is one.
func nowWords(d crew.Decision) string {
	s := fmt.Sprintf("%s is %s, rev %d", d.ID, d.State, d.Rev)
	if d.Applied != nil {
		s += "; applied: " + oneLine(d.Applied.Words)
	}
	return s
}
