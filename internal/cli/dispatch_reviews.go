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
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// conductor dispatch review, reviews, findings and finding (Program item
// RV1, docs/plans/review-repair.md, PR-5): a person's review of a ticket's
// candidate from the shell, what the Review view does, through the
// admin's routes (internal/api/dispatch_reviews.go) with the server and
// the token conductor crews takes. A review is sent as one batch: its
// notes are a file of the route's findings, strict JSON. A request for
// changes to a run the server does not run is answered continuation_needed
// with what continuing it starts; without --continue those words are
// printed and nothing is sent again. Every write mints its operation's id
// and is asked for once more on a lost reply (askTwice), which the server
// answers from its receipt; the reads follow their pages to the end, each
// under maxDispatchReply.

// dispatchReviewArgs is the commands of this file, which runDispatch
// hands here.
var dispatchReviewArgs = map[string]bool{"review": true, "reviews": true, "findings": true, "finding": true}

// The verbs of conductor dispatch review (beside a ticket's key, which
// sends a review) and of conductor dispatch finding.
var (
	reviewVerbs  = []string{"show", "retry"}
	findingVerbs = []string{"marks", "mark"}
)

// reviewValued is the flags of this file's commands that take a value,
// which reviewVerb steps over to find the verb.
var reviewValued = map[string]bool{"server": true, "token": true, "candidate": true, "base": true, "verdict": true, "summary": true, "file": true, "state": true, "note": true}

// reviewRequestTimeout bounds a review's request and Request again's: the
// server's own bound on one (api.ReviewTimeout: its run's continuation
// and its two store writes), and a request's time beside it for the reply
// to come back.
var reviewRequestTimeout = api.ReviewTimeout + requestTimeout

// commitPattern is a commit in full, as the store takes a review's base.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// reviewVerb is the first word of args that is no flag and no flag's
// value ("" for none), and args without it.
func reviewVerb(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if i+1 < len(args) {
				return args[i+1], append(slices.Clone(args[:i]), args[i+2:]...)
			}
			return "", args
		case strings.HasPrefix(a, "-"):
			if name := strings.TrimLeft(a, "-"); reviewValued[name] {
				i++
			}
			continue
		}
		return a, append(slices.Clone(args[:i]), args[i+1:]...)
	}
	return "", args
}

// reviewFlags is what this file's commands take beside the server's; each
// command defines its own.
type reviewFlags struct {
	candidate, base, verdict, summary, file, state, note *string
	cont                                                 *bool
}

// runDispatchReviews is conductor dispatch review, reviews, findings and
// finding. Exit 2 for a usage error, before any request; 1 when the
// server refuses or cannot be asked.
func runDispatchReviews(ctx context.Context, sub string, rest []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	name := sub
	switch sub {
	case "review", "finding":
		verbs := reviewVerbs
		if sub == "finding" {
			verbs = findingVerbs
		}
		verb, others := reviewVerb(rest)
		switch {
		case verb == "help" || verb == "" && (slices.Contains(rest, "-h") || slices.Contains(rest, "--help")):
			fmt.Fprint(stderr, dispatchUsage)
			return 0, nil
		case slices.Contains(verbs, verb):
			name, rest = sub+" "+verb, others
		case sub == "finding":
			fmt.Fprint(stderr, dispatchUsage)
			return 2, errors.New("dispatch finding takes marks or mark")
		}
	}
	fs := flag.NewFlagSet("dispatch "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := addAPIFlags(fs)
	var f reviewFlags
	switch name {
	case "review":
		f.candidate = fs.String("candidate", "", "the candidate reviewed: RUN/TASK/N@COMMIT, an attempt's result commit in full (required)")
		f.base = fs.String("base", "", "the commit the diff was read against, in full: the run's base when not given")
		f.verdict = fs.String("verdict", "", "request_changes, comment or approve (required)")
		f.summary = fs.String("summary", "", "the review's summary")
		f.file = fs.String("file", "", "a file of the notes, a JSON array of the route's findings, - for stdin")
		f.cont = fs.Bool("continue", false, "a request for changes to a run the server does not run: continue it with the repair")
	case "review retry":
		f.cont = fs.Bool("continue", false, "continue the run the request goes to when the server does not run it")
	case "findings":
		f.state = fs.String("state", "", "open, resolved, withdrawn or all (all when not given)")
		f.candidate = fs.String("candidate", "", "where to locate their lines: RUN/TASK/N@COMMIT, which narrows them to its task")
	case "finding mark":
		f.candidate = fs.String("candidate", "", "the later candidate the finding is marked against: RUN/TASK/N@COMMIT (required)")
		f.verdict = fs.String("verdict", "", "resolved, open or withdrawn (required)")
		f.note = fs.String("note", "", "the mark's note")
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
	takesKey := name == "review" || name == "reviews" || name == "findings"
	switch {
	case len(pos) != 1 && takesKey:
		return 2, fmt.Errorf("dispatch %s takes a ticket's key, as CND-12", name)
	case len(pos) != 1:
		return 2, fmt.Errorf("dispatch %s takes a %s's id", name, sub)
	}
	arg := pos[0]
	if takesKey {
		if _, _, ok := dispatch.ParseKey(arg); !ok {
			return 2, fmt.Errorf("dispatch %s: %q is no ticket's key: a project's key and a number, as CND-12", name, arg)
		}
	} else if !dispatch.ValidID(arg) {
		return 2, fmt.Errorf("dispatch %s: %q is no %s's id: 16 lower-case hex characters", name, arg, sub)
	}
	var rv dispatch.NewReview
	var mark dispatch.NewMark
	switch name {
	case "review":
		if rv, err = f.review(stdin); err != nil {
			return 2, fmt.Errorf("dispatch review: %w", err)
		}
	case "findings":
		if s := *f.state; s != "" && s != "all" && !slices.Contains(dispatch.FindingStates, s) {
			return 2, fmt.Errorf("dispatch findings: --state is open, resolved, withdrawn or all: %q", s)
		}
		if *f.candidate != "" {
			if _, err := dispatch.ParseCandidate(*f.candidate); err != nil {
				return 2, fmt.Errorf("dispatch findings: --candidate is RUN/TASK/N@COMMIT, the commit in full: %q", *f.candidate)
			}
		}
	case "finding mark":
		if _, err := dispatch.ParseCandidate(*f.candidate); err != nil {
			return 2, fmt.Errorf("dispatch finding mark: --candidate is RUN/TASK/N@COMMIT, the commit in full: %q", *f.candidate)
		}
		if !slices.Contains(dispatch.FindingStates, *f.verdict) {
			return 2, fmt.Errorf("dispatch finding mark: --verdict is resolved, open or withdrawn: %q", *f.verdict)
		}
		mark = dispatch.NewMark{Candidate: *f.candidate, Verdict: *f.verdict, Note: *f.note, Via: dispatch.ViaCLI}
	}
	c, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	switch name {
	case "review":
		return dispatchReview(ctx, c, arg, rv, *f.cont, *f.base == "", stdout)
	case "review retry":
		return dispatchReviewRetry(ctx, c, arg, *f.cont, stdout)
	case "review show":
		return dispatchReviewShow(ctx, c, arg, stdout)
	case "reviews":
		return dispatchReviews(ctx, c, arg, stdout)
	case "findings":
		return dispatchFindings(ctx, c, arg, *f.state, *f.candidate, stdout)
	case "finding marks":
		return dispatchMarks(ctx, c, arg, stdout)
	}
	return dispatchMark(ctx, c, arg, mark, stdout)
}

// review checks a review's flags and reads its notes, before any request:
// its errors are usage errors. Its base is "" when --base is not given,
// for the run's base to be read.
func (f reviewFlags) review(stdin io.Reader) (dispatch.NewReview, error) {
	rv := dispatch.NewReview{Candidate: *f.candidate, Base: *f.base, Verdict: *f.verdict, Summary: *f.summary, Via: dispatch.ViaCLI, Findings: []dispatch.NewFinding{}}
	if _, err := dispatch.ParseCandidate(rv.Candidate); err != nil {
		return rv, fmt.Errorf("--candidate is RUN/TASK/N@COMMIT, the commit in full: %q", rv.Candidate)
	}
	if !slices.Contains(dispatch.ReviewVerdicts, rv.Verdict) {
		return rv, fmt.Errorf("--verdict is request_changes, comment or approve: %q", rv.Verdict)
	}
	if rv.Base != "" && !commitPattern.MatchString(rv.Base) {
		return rv, fmt.Errorf("--base is a commit in full: %q", rv.Base)
	}
	if *f.cont && rv.Verdict != dispatch.VerdictRequestChanges {
		return rv, errors.New("--continue goes with a request for changes alone: a comment or an approval applies nothing")
	}
	if *f.file != "" {
		if err := readOneJSON(*f.file, stdin, maxResultFile, &rv.Findings); err != nil {
			return rv, err
		}
		if rv.Findings == nil {
			rv.Findings = []dispatch.NewFinding{}
		}
	}
	if len(rv.Findings) == 0 && strings.TrimSpace(rv.Summary) == "" {
		return rv, errors.New("a review holds its notes (--file) or a summary")
	}
	return rv, nil
}

// reviewReply is a review's write's reply: the receipt and the review as
// it stands, nil when the server could not read it after its commit.
type reviewReply struct {
	Receipt dispatch.Receipt `json:"receipt"`
	Review  *dispatch.Review `json:"review"`
}

// continuationOf reads a continuation_needed answer: the run the request
// goes to, which the server does not run, and what continuing it starts.
func continuationOf(err error) (run, words string, ok bool) {
	var r replyRefusal
	if !errors.As(err, &r) || r.status != http.StatusConflict {
		return "", "", false
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Run   string `json:"run"`
		Words string `json:"words"`
	}
	if json.Unmarshal(r.body, &body) != nil || body.Error.Code != "continuation_needed" {
		return "", "", false
	}
	return body.Run, body.Words, true
}

// continuationNeeded prints a continuation_needed answer's words, and
// that nothing was sent again, and is the command's exit.
func continuationNeeded(stdout io.Writer, what, run, words string) (int, error) {
	fmt.Fprintf(stdout, "run %s is not running: %s\nnothing was %s: send it again with --continue to continue the run with the repair\n", oneLine(run), oneLine(words), what)
	return 1, fmt.Errorf("continuation_needed: run %s is not running; add --continue to continue it", oneLine(run))
}

// sendReview sends a review's write, once more on a lost reply with the
// same operation (askTwice), within reviewRequestTimeout each.
func sendReview(ctx context.Context, c *apiClient, path string, body any) (reviewReply, error) {
	var out reviewReply
	err := askTwice(ctx, func(ctx context.Context) error {
		return c.doWithin(ctx, reviewRequestTimeout, http.MethodPost, path, body, &out, maxDispatchReply)
	})
	return out, err
}

// dispatchReview sends a review of the ticket's candidate, its base the
// run's (the diff route's default) unless one was given, and prints what
// became of it.
func dispatchReview(ctx context.Context, c *apiClient, key string, rv dispatch.NewReview, cont, readBase bool, stdout io.Writer) (int, error) {
	if readBase {
		cand, _ := dispatch.ParseCandidate(rv.Candidate)
		var diff struct {
			Base string `json:"base"`
		}
		path := "/api/runs/" + url.PathEscape(cand.Run) + "/tasks/" + url.PathEscape(cand.Task) + "/attempts/" + strconv.Itoa(cand.Attempt) + "/diff"
		if err := c.doLimit(ctx, http.MethodGet, path, nil, &diff, maxDispatchReply); err != nil {
			return 1, fmt.Errorf("the run's base, from %s's diff: %w", cand.Name(), err)
		}
		if !commitPattern.MatchString(diff.Base) {
			return 1, fmt.Errorf("the run's base, from %s's diff: %q is no commit", cand.Name(), oneLine(diff.Base))
		}
		rv.Base = diff.Base
	}
	body := struct {
		OpID string `json:"opId"`
		dispatch.NewReview
		ContinueRun bool `json:"continueRun,omitempty"`
	}{newOpID(), rv, cont}
	out, err := sendReview(ctx, c, "/api/dispatch/tickets/"+url.PathEscape(key)+"/reviews", body)
	if run, words, ok := continuationOf(err); ok && !cont {
		return continuationNeeded(stdout, "recorded", run, words)
	}
	if err != nil {
		return 1, err
	}
	printReviewReply(stdout, key, "", out)
	return 0, nil
}

// dispatchReviewRetry is Request again: a refused request for changes
// applied as its next try.
func dispatchReviewRetry(ctx context.Context, c *apiClient, id string, cont bool, stdout io.Writer) (int, error) {
	body := map[string]any{"opId": newOpID()}
	if cont {
		body["continueRun"] = true
	}
	out, err := sendReview(ctx, c, "/api/dispatch/reviews/"+url.PathEscape(id)+"/retry", body)
	if run, words, ok := continuationOf(err); ok && !cont {
		return continuationNeeded(stdout, "sent", run, words)
	}
	if err != nil {
		return 1, err
	}
	printReviewReply(stdout, "", " sent again", out)
	return 0, nil
}

// printReviewReply prints a review's write's reply: the review and what
// became of it, or, when the server could not read it after its commit,
// the receipt and how to read it.
func printReviewReply(w io.Writer, key, sent string, out reviewReply) {
	v := out.Review
	if v == nil {
		fmt.Fprintf(w, "review %s%s: recorded, but the server could not read it back: conductor dispatch review show %s reads it\n", oneLine(out.Receipt.ID), sent, oneLine(out.Receipt.ID))
		return
	}
	fmt.Fprintf(w, "review %s of %s%s: %s of %s against %s, %s\n", oneLine(v.ID), oneLine(cmp.Or(v.TicketKey, key)), sent, oneLine(v.Verdict), oneLine(v.Candidate), short(oneLine(v.Base)), notesWords(len(v.Findings)))
	fmt.Fprintln(w, applicationWords(v.ReviewSummary))
}

// notesWords is a count of a review's notes in words.
func notesWords(n int) string {
	if n == 1 {
		return "1 note"
	}
	return strconv.Itoa(n) + " notes"
}

// applicationWords says, as separate facts, that a review was recorded
// and what Yardmaster did with it.
func applicationWords(v dispatch.ReviewSummary) string {
	a := v.Application
	try := ""
	if a.Try > 1 {
		try = fmt.Sprintf(" (try %d)", a.Try)
	}
	switch a.State {
	case dispatch.ApplicationNone:
		if v.Verdict == dispatch.VerdictApprove {
			return "recorded; an approval applies nothing: it accepts, delivers and closes nothing"
		}
		return "recorded; a comment applies nothing"
	case dispatch.ApplicationApplied:
		task := ""
		if c, err := dispatch.ParseCandidate(v.Candidate); err == nil {
			task = " " + c.Task
		}
		return fmt.Sprintf("recorded; applied%s: Yardmaster reopened%s in run %s, attempt %d to start (command %s, the run's revision %d)", try, task, oneLine(a.Run), a.Attempt, oneLine(a.Command), a.Revision)
	case dispatch.ApplicationRefused:
		return fmt.Sprintf("recorded; refused%s: %s; conductor dispatch review retry %s asks again", try, oneLine(a.Why), oneLine(v.ID))
	case dispatch.ApplicationPending:
		return fmt.Sprintf("recorded; pending%s: Yardmaster's answer is not recorded yet; conductor dispatch review show %s reads it again", try, oneLine(v.ID))
	}
	return "recorded; " + oneLine(a.State)
}

// countsWords is a review's findings counted by state.
func countsWords(n dispatch.FindingCounts) string {
	return fmt.Sprintf("%d open, %d resolved, %d withdrawn", n.Open, n.Resolved, n.Withdrawn)
}

// byWords is who made a record: the name it was made under, else its
// actor, and the client it said it was.
func byWords(name, actor, via string) string {
	out := oneLine(cmp.Or(name, actor, "admin"))
	if via != "" {
		out += " via " + oneLine(via)
	}
	return out
}

// dispatchReviews prints the ticket's reviews, newest first, one a line,
// every page.
func dispatchReviews(ctx context.Context, c *apiClient, key string, stdout io.Writer) (int, error) {
	n := 0
	for before := ""; ; {
		path := "/api/dispatch/tickets/" + url.PathEscape(key) + "/reviews"
		if before != "" {
			path += "?before=" + url.QueryEscape(before)
		}
		var page dispatch.ReviewsPage
		if err := c.doLimit(ctx, http.MethodGet, path, nil, &page, maxDispatchReply); err != nil {
			return 1, err
		}
		for _, v := range page.Reviews {
			n++
			fmt.Fprintln(stdout, strings.Join([]string{oneLine(v.ID), oneLine(v.Verdict), oneLine(v.Candidate), countsWords(v.Counts), when(v.CreatedAt) + " by " + byWords(v.CreatedBy, v.Actor, v.Via)}, "\t"))
			fmt.Fprintln(stdout, "  "+applicationWords(v))
		}
		if page.Next == "" || page.Next == before || len(page.Reviews) == 0 {
			break
		}
		before = page.Next
	}
	if n == 0 {
		fmt.Fprintf(stdout, "%s has no reviews\n", key)
	}
	return 0, nil
}

// dispatchReviewShow prints a review whole: its candidate, base, verdict,
// application, summary and findings, each with its latest mark.
func dispatchReviewShow(ctx context.Context, c *apiClient, id string, stdout io.Writer) (int, error) {
	var out struct {
		Review dispatch.Review `json:"review"`
	}
	if err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/reviews/"+url.PathEscape(id), nil, &out, maxDispatchReply); err != nil {
		return 1, err
	}
	v := out.Review
	fmt.Fprintf(stdout, "# review %s of %s\n\n", oneLine(v.ID), oneLine(v.TicketKey))
	for _, l := range [][2]string{
		{"candidate", v.Candidate}, {"base", v.Base}, {"verdict", v.Verdict},
		{"by", when(v.CreatedAt) + " by " + byWords(v.CreatedBy, v.Actor, v.Via)},
		{"application", applicationWords(v.ReviewSummary)}, {"findings", countsWords(v.Counts)},
	} {
		fmt.Fprintf(stdout, "- %s: %s\n", l[0], oneLine(l[1]))
	}
	if s := strings.TrimSpace(v.Summary); s != "" {
		fmt.Fprintf(stdout, "\n## Summary\n\n%s\n", textBlock(s))
	}
	if len(v.Findings) > 0 {
		fmt.Fprint(stdout, "\n## Findings\n\n")
	}
	for _, f := range v.Findings {
		writeFinding(stdout, f)
		if m := f.Latest; m != nil {
			fmt.Fprintf(stdout, "  latest of %d marks: %s\n", f.Marks, markWords(*m))
		}
	}
	return 0, nil
}

// writeFinding prints a finding's line, its words and the resolution
// asked for.
func writeFinding(w io.Writer, f dispatch.Finding) {
	kind := oneLine(f.Intent)
	if f.Severity != "" {
		kind += " " + oneLine(f.Severity)
	}
	fmt.Fprintln(w, strings.Join([]string{"- " + oneLine(f.ID), oneLine(f.State), kind, findingWhere(f), "on " + oneLine(f.Candidate)}, "\t"))
	if m := strings.TrimSpace(f.Message); m != "" {
		fmt.Fprintf(w, "  %s\n", oneLine(m))
	}
	if r := strings.TrimSpace(f.Resolution); r != "" {
		fmt.Fprintf(w, "  asked: %s\n", oneLine(r))
	}
}

// findingWhere says where a finding is: a path's lines on a side, a whole
// file, a check's receipt, or the candidate as a whole.
func findingWhere(f dispatch.Finding) string {
	switch f.Kind {
	case dispatch.FindingKindLine:
		lines := strconv.Itoa(f.From)
		if f.To > f.From {
			lines += "-" + strconv.Itoa(f.To)
		}
		return fmt.Sprintf("%s:%s (%s side)", oneLine(f.Path), lines, oneLine(cmp.Or(f.Side, dispatch.SideCandidate)))
	case dispatch.FindingKindFile:
		return oneLine(f.Path) + " (the file)"
	case dispatch.FindingKindCheck:
		if f.Check != nil {
			return "check " + oneLine(f.Check.Receipt)
		}
		return "a check"
	}
	return "the candidate"
}

// locatedView is where a finding's lines are on a candidate, as the
// server works it out (internal/api/review_locate.go).
type locatedView struct {
	State   string       `json:"state"`
	From    int          `json:"from"`
	To      int          `json:"to"`
	Path    string       `json:"path"`
	Located *locatedView `json:"located"`
	Why     string       `json:"why"`
}

// locatedWords is a located state in words; "" for none.
func locatedWords(l *locatedView) string {
	if l == nil {
		return ""
	}
	switch l.State {
	case "same":
		return "same lines"
	case "moved":
		return fmt.Sprintf("moved: now %d-%d", l.From, l.To)
	case "changed":
		return "the lines changed"
	case "file_gone":
		return "the file is gone"
	case "renamed":
		if inner := locatedWords(l.Located); inner != "" {
			return "renamed to " + oneLine(l.Path) + ", " + inner
		}
		return "renamed to " + oneLine(l.Path)
	case "unknown":
		return "not known: " + oneLine(l.Why)
	}
	return oneLine(l.State)
}

// rawLocated decodes a mark's located state; nil for none or one that is
// not one.
func rawLocated(raw json.RawMessage) *locatedView {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var l locatedView
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}
	return &l
}

// markWords is a mark in words: its verdict, the candidate, who made it
// and when, where the lines were, its note.
func markWords(m dispatch.Mark) string {
	out := fmt.Sprintf("%s on %s, %s by %s", oneLine(m.Verdict), oneLine(m.Candidate), when(m.CreatedAt), byWords(m.CreatedBy, m.Actor, m.Via))
	if l := locatedWords(rawLocated(m.Located)); l != "" {
		out += "; its lines there: " + l
	}
	if n := strings.TrimSpace(m.Note); n != "" {
		out += "; " + oneLine(n)
	}
	return out
}

// findingEntry is a finding as the ticket's findings page lists it: where
// its lines are on the candidate it was located on, and what the last
// repair attempts that named it said.
type findingEntry struct {
	dispatch.Finding
	LocatedOn string       `json:"locatedOn"`
	Located   *locatedView `json:"located"`
	Addressed []struct {
		Run     string `json:"run"`
		Attempt int    `json:"attempt"`
		Note    string `json:"note"`
	} `json:"addressed"`
}

// dispatchFindings prints the ticket's findings, every page: each with its
// state, where it is, the candidate it was made on, its words, where its
// lines are on the candidate located on, what the repair attempts said of
// it, and its last mark.
func dispatchFindings(ctx context.Context, c *apiClient, key, state, candidate string, stdout io.Writer) (int, error) {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if candidate != "" {
		q.Set("candidate", candidate)
	}
	n := 0
	for after := ""; ; {
		if after != "" {
			q.Set("after", after)
		}
		path := "/api/dispatch/tickets/" + url.PathEscape(key) + "/findings"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var page struct {
			Findings []findingEntry `json:"findings"`
			Next     string         `json:"next"`
		}
		if err := c.doLimit(ctx, http.MethodGet, path, nil, &page, maxDispatchReply); err != nil {
			return 1, err
		}
		for _, f := range page.Findings {
			n++
			writeFinding(stdout, f.Finding)
			if l := locatedWords(f.Located); l != "" {
				fmt.Fprintf(stdout, "  on %s: %s\n", oneLine(f.LocatedOn), l)
			}
			for _, a := range f.Addressed {
				says := "addressed"
				if n := strings.TrimSpace(a.Note); n != "" {
					says = oneLine(n)
				}
				fmt.Fprintf(stdout, "  the agent says (attempt %d of run %s): %s\n", a.Attempt, oneLine(a.Run), says)
			}
			if m := f.Latest; m != nil {
				fmt.Fprintf(stdout, "  last mark: %s\n", markWords(*m))
			}
		}
		if page.Next == "" || page.Next == after || len(page.Findings) == 0 {
			break
		}
		after = page.Next
	}
	if n == 0 {
		which := ""
		if state != "" && state != "all" {
			which = state + " "
		}
		fmt.Fprintf(stdout, "%s has no %sfindings\n", key, which)
	}
	return 0, nil
}

// dispatchMarks prints a finding's marks, oldest first, one a line, every
// page.
func dispatchMarks(ctx context.Context, c *apiClient, id string, stdout io.Writer) (int, error) {
	n := 0
	for after := ""; ; {
		path := "/api/dispatch/findings/" + url.PathEscape(id) + "/marks"
		if after != "" {
			path += "?after=" + url.QueryEscape(after)
		}
		var page dispatch.MarksPage
		if err := c.doLimit(ctx, http.MethodGet, path, nil, &page, maxDispatchReply); err != nil {
			return 1, err
		}
		for _, m := range page.Marks {
			n++
			fmt.Fprintln(stdout, oneLine(m.ID)+"\t"+markWords(m))
		}
		if page.Next == "" || page.Next == after || len(page.Marks) == 0 {
			break
		}
		after = page.Next
	}
	if n == 0 {
		fmt.Fprintf(stdout, "finding %s has no marks: it is open\n", id)
	}
	return 0, nil
}

// dispatchMark marks a finding against a later candidate and prints the
// mark, with where the server found its lines there.
func dispatchMark(ctx context.Context, c *apiClient, id string, mark dispatch.NewMark, stdout io.Writer) (int, error) {
	body := struct {
		OpID string `json:"opId"`
		dispatch.NewMark
	}{newOpID(), mark}
	var out struct {
		Receipt dispatch.Receipt `json:"receipt"`
		Mark    *dispatch.Mark   `json:"mark"`
	}
	if err := askTwice(ctx, func(ctx context.Context) error {
		return c.doLimit(ctx, http.MethodPost, "/api/dispatch/findings/"+url.PathEscape(id)+"/marks", body, &out, maxDispatchReply)
	}); err != nil {
		return 1, err
	}
	line := fmt.Sprintf("finding %s marked %s on %s", id, mark.Verdict, mark.Candidate)
	if out.Mark != nil {
		if l := locatedWords(rawLocated(out.Mark.Located)); l != "" {
			line += "; its lines there: " + l
		}
	}
	fmt.Fprintln(stdout, line)
	return 0, nil
}
