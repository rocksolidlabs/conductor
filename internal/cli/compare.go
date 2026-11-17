package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A comparison from the shell (docs/plans/compare-crew.md, Program item
// CP1, PR-3): conductor crews compare saves a comparison's crew from its
// template; conductor dispatch comparison prints a comparison as measured;
// conductor dispatch launch takes --candidates, the count a comparison's
// revision starts, which the launch repeats (dispatchLaunch prints the
// statement and exits 2 without it).

// maxCompareSpec bounds a comparison's spec as read from a file or stdin:
// the template route's own bound. maxCompareCrewReply bounds the crew it
// answers: a crew file's bound, twice for its escapes and its revision.
const (
	maxCompareSpec      = 256 << 10
	maxCompareCrewReply = 2 * crew.MaxEncoded
)

const crewsCompareUsage = `Usage:
  conductor crews compare SPEC.json|- [--server URL] [--token T] [--insecure]
      save a comparison's crew from its template (POST /api/crews/templates/compare):
      SPEC.json, or - for stdin, a JSON object {name, cwd, goal, instructions,
      criteria, checks?, manifest?, candidates, reviews?, reviewAgent?,
      brakeman?, yolo?} of at most 256 KiB; prints the crew's id and its tasks.
      It launches nothing: a comparison is launched from a ticket's approved
      plan revision (conductor dispatch launch KEY-n --candidates N)
`

// runCrewsCompare is conductor crews compare. Exit 2 for a usage error, a
// spec that cannot be read and one past its bound, before any request; 1
// when the server refuses or cannot be asked.
func runCrewsCompare(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("crews compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := addAPIFlags(fs)
	fs.Usage = func() {
		fmt.Fprint(stderr, crewsCompareUsage)
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	if len(pos) != 1 {
		fs.Usage()
		return 2, errors.New("crews compare takes one spec: a file, or - for stdin")
	}
	var r io.Reader = stdin
	if pos[0] != "-" {
		f, err := os.Open(pos[0])
		if err != nil {
			return 2, fmt.Errorf("crews compare: %w", err)
		}
		defer f.Close()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxCompareSpec+1))
	switch {
	case err != nil:
		return 2, fmt.Errorf("crews compare: reading the spec: %w", err)
	case len(raw) > maxCompareSpec:
		return 2, fmt.Errorf("crews compare: the spec is more than %d bytes", maxCompareSpec)
	case !json.Valid(raw):
		return 2, errors.New("crews compare: the spec is not JSON")
	}
	c, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	var out struct {
		Crew crew.Crew `json:"crew"`
	}
	if err := c.doLimit(ctx, http.MethodPost, "/api/crews/templates/compare", json.RawMessage(raw), &out, maxCompareCrewReply); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "crew %s %s\n", oneLine(out.Crew.ID), strconv.Quote(oneLine(out.Crew.Name)))
	for _, t := range out.Crew.Tasks {
		line := "  " + oneLine(t.ID) + "  " + oneLine(t.Kind) + " by " + oneLine(t.Member)
		if len(t.Needs) > 0 {
			line += " after " + oneLine(strings.Join(t.Needs, ", "))
		}
		fmt.Fprintln(stdout, line)
	}
	return 0, nil
}

// comparisonView is a comparison's read as the CLI prints it.
type comparisonView struct {
	crew.Comparison
	Independence struct {
		NotObserved string `json:"notObserved"`
	} `json:"independence"`
}

// dispatchComparison is conductor dispatch comparison: the comparison of
// the run (any run of its chain), else the ticket's newest one, as text or
// with asJSON the route's reply as it came.
func dispatchComparison(ctx context.Context, c *apiClient, key, run string, asJSON bool, stdout io.Writer) (int, error) {
	if run == "" {
		var err error
		if run, err = newestComparison(ctx, c, key); err != nil {
			return 1, err
		}
	}
	var raw json.RawMessage
	if err := c.doLimit(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(run)+"/comparison", nil, &raw, crew.MaxComparison); err != nil {
		return 1, err
	}
	if asJSON {
		// Through jsonText, as every reply the CLI prints as JSON: no
		// control character of the server's reaches the terminal.
		text, err := jsonText(raw, false)
		if err != nil {
			return 1, err
		}
		_, err = fmt.Fprintln(stdout, text)
		return 0, err
	}
	var v comparisonView
	if err := json.Unmarshal(raw, &v); err != nil {
		return 1, fmt.Errorf("the comparison from %s: %w", c.host, err)
	}
	writeComparison(stdout, key, v)
	return 0, nil
}

// newestComparison is the run of the ticket's newest started launch from
// a comparison's revision, from the ticket's revisions (each comparison's
// with its compare part) and its runs, newest first.
func newestComparison(ctx context.Context, c *apiClient, key string) (string, error) {
	plans, err := ticketPlans(ctx, c, key)
	if err != nil {
		return "", err
	}
	compared := map[string]bool{}
	for _, p := range plans {
		if p.Compare != nil {
			compared[p.ID] = true
		}
	}
	if len(compared) > 0 {
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
				return "", err
			}
			for _, e := range page.Entries {
				// A launch that did not start (failed, or still pending)
				// has no run to read: the newest one that started is
				// the ticket's comparison.
				if e.Kind == "launch" && e.Launch != nil && e.Launch.State == dispatch.LaunchStarted && compared[e.Launch.PlanID] && e.Launch.RunID != "" {
					return e.Launch.RunID, nil
				}
			}
			if page.Next == "" || page.Next == cursor || len(page.Entries) == 0 {
				break
			}
			cursor = page.Next
		}
	}
	return "", fmt.Errorf("%s has launched no comparison: name a run of one with --run", key)
}

// writeComparison prints a comparison: its base, objective and criteria,
// a column per candidate (its result and what was measured of it), each
// check's row with each candidate's cell, its verdict and its reasons, and
// what independence is not observed. Every text the server sent is put on
// one line, control characters gone.
func writeComparison(w io.Writer, key string, v comparisonView) {
	head := "comparison of " + oneLine(v.Run) + " at " + short(oneLine(v.Base))
	if key != "" {
		head = oneLine(key) + ": " + head
	}
	if v.Settled {
		head += ", settled"
	} else {
		head += ", not settled"
	}
	fmt.Fprintln(w, head)
	fmt.Fprintln(w, "objective: "+oneLine(v.Objective))
	fmt.Fprintln(w, "criteria:")
	for _, cr := range v.Criteria {
		fmt.Fprintln(w, "  "+oneLine(cr.ID)+": "+oneLine(cr.Text))
	}
	fmt.Fprintln(w, "candidates:")
	for _, cd := range v.Candidates {
		result := "no result"
		if cd.Commit != "" {
			result = "attempt " + strconv.Itoa(cd.Attempt) + " at " + short(oneLine(cd.Commit))
		}
		line := tabbed(oneLine(cd.Task), oneLine(cd.Member)+" on "+oneLine(cd.Agent), oneLine(cd.State), result, strconv.Itoa(cd.Attempts)+" attempts")
		if cd.Selectable {
			line += "\tselectable"
		}
		if cd.AcceptedAnyway {
			line += "\taccepted anyway"
		}
		fmt.Fprintln(w, "  "+line)
		if a := strings.TrimSpace(cd.Approach); a != "" {
			fmt.Fprintln(w, "    approach: "+oneLine(a))
		}
		if s := cd.Size; s != nil {
			if s.NotMeasured != "" {
				fmt.Fprintln(w, "    size: "+oneLine(s.NotMeasured))
			} else {
				fmt.Fprintf(w, "    size: %d files, +%d -%d\n", s.Files, s.Added, s.Removed)
			}
		}
		if d := cd.Deps; d != nil {
			fmt.Fprintf(w, "    dependencies: %d files, %d modules changed\n", len(d.Files), len(d.Modules))
		}
		for _, sc := range cd.Shared {
			fmt.Fprintf(w, "    shares %d commits with %s%s\n", sc.Count, oneLine(sc.With), noteOf(sc.Note))
		}
		for _, sc := range cd.Equivalent {
			fmt.Fprintf(w, "    %d equivalent patches with %s%s\n", sc.Count, oneLine(sc.With), noteOf(sc.Note))
		}
		if cd.ManifestChanged {
			fmt.Fprintln(w, "    changes conductor.manifest.json: runs its own checks")
		}
		if cd.Unavailable != "" {
			fmt.Fprintln(w, "    unavailable: "+oneLine(cd.Unavailable))
		}
	}
	fmt.Fprintln(w, "checks:")
	if len(v.Checks) == 0 {
		fmt.Fprintln(w, "  none yet")
	}
	for _, ch := range v.Checks {
		fmt.Fprintln(w, "  "+tabbed(oneLine(ch.Phase)+"/"+oneLine(ch.ID), oneLine(ch.Verdict)))
		for _, cell := range ch.Cells {
			fmt.Fprintln(w, "    "+tabbed(oneLine(cell.Task), cellWords(cell)))
		}
		for _, why := range ch.Why {
			fmt.Fprintln(w, "    why: "+oneLine(why))
		}
	}
	if v.Independence.NotObserved != "" {
		fmt.Fprintln(w, "not observed: "+oneLine(v.Independence.NotObserved))
	}
}

// cellWords is a check's cell in words: its state, and how it ended.
func cellWords(c crew.CheckCell) string {
	words := oneLine(c.State)
	if c.NotRun != "" {
		words += " (" + oneLine(c.NotRun) + ")"
	}
	switch {
	case c.TimedOut:
		words += ", timed out"
	case c.Signal != "":
		words += ", " + oneLine(c.Signal)
	case c.Exit != nil:
		words += ", exit " + strconv.Itoa(*c.Exit)
	}
	if c.Attempt > 0 {
		words += ", attempt " + strconv.Itoa(c.Attempt)
	}
	return words
}

// noteOf is a note in parentheses, "" for none.
func noteOf(note string) string {
	if note == "" {
		return ""
	}
	return " (" + oneLine(note) + ")"
}

// planCompareEntry is a comparison's revision's statement as the plan
// reads give it (compare).
type planCompareEntry struct {
	Candidates   int  `json:"candidates"`
	Reviews      int  `json:"reviews"`
	ChecksAtBase int  `json:"checksAtBase"`
	BrakemanTurn bool `json:"brakemanTurn"`
}

// compareStatement is what the launch of a comparison's revision starts,
// in the plan's words: the candidates and their agents, the reviews'
// sessions as up to one after each candidate that succeeds, each
// candidate worked as a task of its own crew with the refunds and Retries
// that add to it, the checks at the base and a Brakeman turn; never a
// total.
func compareStatement(pc planCompareEntry, agents []string) string {
	n := strconv.Itoa(pc.Candidates)
	var b strings.Builder
	fmt.Fprintf(&b, "This comparison starts %s candidates", n)
	if len(agents) > 0 {
		fmt.Fprintf(&b, " (%s)", oneLine(strings.Join(agents, ", ")))
	}
	fmt.Fprintf(&b, ": %s agent sessions, once the plan's decisions and the project's prerequisites let the work start", n)
	if pc.Reviews > 0 {
		fmt.Fprintf(&b, "; then an agent review of each candidate that succeeds: up to %d more sessions, none for a candidate that fails or is cancelled", pc.Reviews)
	}
	fmt.Fprintf(&b, "; then what %s runs of one candidate's crew would take, since each candidate is worked as a task of its own crew is (each attempt a session of its own; 3 counted attempts a task, and a review's request for changes, an attempt that could not start and each Retry you give add more)", n)
	fmt.Fprintf(&b, "; %s on each submitted candidate attempt (the task's checks and conductor.manifest.json's verify steps at the base; a candidate that changes the manifest runs its own)", checksWords(pc.ChecksAtBase))
	if pc.BrakemanTurn {
		b.WriteString("; 1 Brakeman turn on your Brakeman account, when all candidates are done")
	}
	b.WriteString(". Usage per account is not shown in this build.")
	return b.String()
}

// compareAgents is the agent of each candidate of a revision's crew, in
// the block's order, from the revision's body.
func compareAgents(body json.RawMessage) []string {
	var b struct {
		Crew crew.Crew `json:"crew"`
	}
	if json.Unmarshal(body, &b) != nil || b.Crew.Compare == nil {
		return nil
	}
	agentOf := map[string]string{}
	for _, m := range b.Crew.Members {
		agentOf[m.Name] = m.AgentID
	}
	var out []string
	for _, cd := range b.Crew.Compare.Candidates {
		for _, t := range b.Crew.Tasks {
			if t.ID == cd.Task {
				out = append(out, agentOf[t.Member])
			}
		}
	}
	return out
}
