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
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/history"
	"github.com/rocksolidlabs/conductor/internal/journal"
)

// conductor history: the history (docs/plans/history.md, Program item
// HS1) from the shell, through the admin's routes as the History page uses
// them, with the server and the token conductor dispatch takes: the
// search and its entries, the session records, Forget, a resume from a
// record and a rebuild of the index. A resume mints its command id and,
// a reply lost to the network, sends it once more with the same id, which
// the server answers from the record's receipt without a second session.

const historyUsage = `Usage:
  conductor history search WORDS... [--limit N] [--json]
      the history's entries whose text holds the words, best first: a table of
      each entry's class, kind and title and its six fields (its source, its
      host, its project, its revision, when, and whether it is still there);
      the palette's filters work here (class:checked outcome:failed since:7d)
  conductor history show KEY [--json]
      one entry by its key, with its links each way and its availability
  conductor history similar KEY [--everywhere] [--json]
      the receipts of the entry's command in other runs of its repository, or
      of every repository with --everywhere
  conductor history sessions [--json]
      the session records, newest first, one a line: id, start, agent, how it
      ended, name and folder
  conductor history session ID [--json]
      a session's record whole: its agent and conversation, host, folder,
      commits, launch, crew, resumes and the evidence Conductor kept
  conductor history forget ID | --all
      forget a session's record, or every record neither live nor being resumed
  conductor history resume ID [--without-env] [--confirm-lost]
      resume the session's agent conversation from its record, after a restart
      too; --without-env resumes a launch whose variables no record keeps
      without them, --confirm-lost one the server stopped observing while it
      ran, whose process may still be running
  conductor history reindex
      rebuild the history's index from the stores

Every command takes --server, --token and --insecure as conductor dispatch
does. A resume whose reply is lost to the network is sent once more with the
same command id, which the server answers from the record's receipt without
starting a second session.
`

// maxHistoryCLIReply bounds what conductor history reads of a reply: the
// smallest whole number of MiB above the search routes' bound
// (api.MaxHistoryReply), which a record of this version
// (journal.MaxRecord, six bytes for each HTML character escaped) is under;
// a page of the records has its own (maxHistorySessionsReply).
const maxHistoryCLIReply = (max(api.MaxHistoryReply, journal.MaxRecord)>>20 + 1) << 20

// maxHistorySessionsReply bounds what conductor history sessions reads of
// a page of the session records: a whole page (journal.MaxPage rows), each
// row at its most as the server writes it (journal.MaxSummary, HTML
// characters escaped), and the page's cursor and flags.
const maxHistorySessionsReply = journal.MaxPage*journal.MaxSummary + 4<<10

// historyPage is how many records conductor history sessions asks for a
// page at a time: the route's most.
var historyPage = journal.MaxPage

// historyResumeTries is how many times a resume is sent in all while the
// server answers it still being started (409 resume_pending), with
// historyResumeWait between; a reply lost to the network is sent once
// more within each (askTwice).
var (
	historyResumeTries = 3
	historyResumeWait  = 2 * time.Second
)

// historyArgs is what each command takes: no argument, an entry's key,
// a record's id, or words.
var historyArgs = map[string]string{
	"search": "WORDS", "show": "KEY", "similar": "KEY", "sessions": "", "session": "ID", "forget": "ID", "resume": "ID", "reindex": "",
}

// runHistory is conductor history. Exit 2 for a usage error, before any
// request; 1 when the server refuses or cannot be asked.
func runHistory(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, historyUsage)
		if len(args) == 0 {
			return 2, nil
		}
		return 0, nil
	}
	sub, rest := args[0], args[1:]
	takes, known := historyArgs[sub]
	if !known {
		fmt.Fprint(stderr, historyUsage)
		return 2, fmt.Errorf("history: unknown command %q", sub)
	}
	fs := flag.NewFlagSet("history "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := addAPIFlags(fs)
	var (
		asJSON, everywhere, all, withoutEnv, confirmLost *bool
		limit                                            *int
	)
	switch sub {
	case "search":
		limit = fs.Int("limit", 20, "how many entries, 1 to "+strconv.Itoa(history.MaxLimit))
		asJSON = fs.Bool("json", false, "print the server's reply")
	case "show", "sessions", "session":
		asJSON = fs.Bool("json", false, "print the server's reply")
	case "similar":
		everywhere = fs.Bool("everywhere", false, "every repository's runs, not only the entry's")
		asJSON = fs.Bool("json", false, "print the server's reply")
	case "forget":
		all = fs.Bool("all", false, "every record neither live nor being resumed")
	case "resume":
		withoutEnv = fs.Bool("without-env", false, "resume a launch that had variables without them: no record keeps their values")
		confirmLost = fs.Bool("confirm-lost", false, "resume a session the server stopped observing while it ran, whose process may still be running")
	}
	fs.Usage = func() {
		fmt.Fprint(stderr, historyUsage, "\nFlags of history "+sub+":\n")
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	switch {
	case takes == "" && len(pos) != 0:
		return 2, fmt.Errorf("history %s takes no argument: %q", sub, pos[0])
	case takes == "WORDS" && len(pos) == 0:
		return 2, fmt.Errorf("history %s takes the words to search for", sub)
	case sub == "forget" && *all && len(pos) != 0:
		return 2, errors.New("history forget takes a record's id or --all, not both")
	case sub == "forget" && *all:
	case (takes == "KEY" || takes == "ID") && len(pos) != 1:
		return 2, fmt.Errorf("history %s takes one %s", sub, map[string]string{"KEY": "entry's key", "ID": "record's id (a session's 16 hex digits)"}[takes])
	}
	if takes == "ID" && len(pos) == 1 && !validSessionID(pos[0]) {
		return 2, fmt.Errorf("history %s: %q is no session's id: 16 hex digits", sub, pos[0])
	}
	if takes == "KEY" && len(pos[0]) > history.MaxRef {
		return 2, fmt.Errorf("history %s: an entry's key is at most %d bytes", sub, history.MaxRef)
	}
	if limit != nil && (*limit < 1 || *limit > history.MaxLimit) {
		return 2, fmt.Errorf("history search: --limit is 1 to %d", history.MaxLimit)
	}
	c, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	switch sub {
	case "search":
		q := url.Values{"q": {strings.Join(pos, " ")}, "limit": {strconv.Itoa(*limit)}}
		return historyEntries(ctx, c, "/api/history?"+q.Encode(), *asJSON, stdout)
	case "show":
		return historyShow(ctx, c, pos[0], *asJSON, stdout)
	case "similar":
		q := url.Values{"key": {pos[0]}}
		if *everywhere {
			q.Set("scope", history.ScopeAll)
		}
		return historyEntries(ctx, c, "/api/history/similar?"+q.Encode(), *asJSON, stdout)
	case "sessions":
		return historySessions(ctx, c, *asJSON, stdout)
	case "session":
		return historySession(ctx, c, pos[0], *asJSON, stdout)
	case "forget":
		return historyForget(ctx, c, pos, stdout)
	case "resume":
		return historyResume(ctx, c, pos[0], *withoutEnv, *confirmLost, stdout)
	}
	var out struct {
		Indexing json.RawMessage `json:"indexing"`
	}
	if err := c.doLimit(ctx, http.MethodPost, "/api/history/reindex", map[string]any{}, &out, maxHistoryCLIReply); err != nil {
		return 1, err
	}
	fmt.Fprintln(stdout, "the history's index is being rebuilt from the stores")
	return 0, nil
}

// validSessionID says id is a session's id: 16 lower-case hex digits.
func validSessionID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// historyEntry is an entry as the search routes serve it: what it is,
// its six fields, its page and its links.
type historyEntry struct {
	Key     string    `json:"key"`
	Kind    string    `json:"kind"`
	Class   string    `json:"class"`
	Outcome string    `json:"outcome"`
	Title   string    `json:"title"`
	Snippet string    `json:"snippet"`
	At      time.Time `json:"at"`
	Source  struct {
		Label string `json:"label"`
		Ref   string `json:"ref"`
	} `json:"source"`
	Host struct {
		Name   string `json:"name"`
		User   string `json:"user"`
		Hosted bool   `json:"hosted"`
		Where  string `json:"where"`
	} `json:"host"`
	Project struct {
		Key  string `json:"key"`
		Repo string `json:"repo"`
	} `json:"project"`
	Revision struct {
		Label  string `json:"label"`
		Commit string `json:"commit"`
	} `json:"revision"`
	URL     string `json:"url"`
	Repeats struct {
		Count   int `json:"count"`
		Anchors int `json:"anchors"`
	} `json:"repeats"`
	Links []struct {
		Rel     string `json:"rel"`
		Dir     string `json:"dir"`
		Label   string `json:"label"`
		Key     string `json:"key"`
		Class   string `json:"class"`
		Outcome string `json:"outcome"`
	} `json:"links"`
	Availability struct {
		State  string `json:"state"`
		Words  string `json:"words"`
		Resume *struct {
			Offered string `json:"offered"`
			Why     string `json:"why"`
			Confirm bool   `json:"confirm"`
		} `json:"resume"`
	} `json:"availability"`
}

// classWord is an entry's class as the palette's badge says it: what was
// said, decided or checked (✓ only for a receipt that passed), or a
// record.
func classWord(class, outcome string) string {
	switch class {
	case history.ClassSaid:
		return "Conversation"
	case history.ClassDecided:
		return "Decision"
	case history.ClassChecked:
		if outcome == "passed" {
			return "Checked ✓"
		}
		return "Checked ✗"
	}
	return "record"
}

// hostWord is an entry's host: its name, a hosted one's user's "via
// conductor host", and the Runtime it ran in when not native.
func (e historyEntry) hostWord() string {
	h := e.Host.Name
	if e.Host.Hosted && e.Host.User != "" {
		h = e.Host.User + "@" + h + " via conductor host"
	}
	if e.Host.Where != "" && e.Host.Where != "native" {
		h += " on " + e.Host.Where
	}
	return cellOr(h)
}

// cellOr is s in a table's cell: tabs and line breaks as spaces, "-" for
// nothing.
func cellOr(s string) string {
	s = strings.Join(strings.Fields(oneLine(s)), " ")
	if s == "" {
		return "-"
	}
	return s
}

// historyEntries prints a search's or a similar's entries as a table:
// the class, the kind, the title, and the six fields; with asJSON the
// server's reply.
func historyEntries(ctx context.Context, c *apiClient, path string, asJSON bool, stdout io.Writer) (int, error) {
	var raw json.RawMessage
	if err := c.doLimit(ctx, http.MethodGet, path, nil, &raw, maxHistoryCLIReply); err != nil {
		return 1, err
	}
	if asJSON {
		return printReply(stdout, raw, false)
	}
	var out struct {
		Entries     []historyEntry `json:"entries"`
		Total       int            `json:"total"`
		TotalCapped bool           `json:"totalCapped"`
		Cut         string         `json:"cut"`
		Next        string         `json:"next"`
		Indexing    struct {
			State string `json:"state"`
		} `json:"indexing"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 1, fmt.Errorf("unreadable reply from %s: %w", c.host, err)
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CLASS\tKIND\tTITLE\tSOURCE\tHOST\tPROJECT\tREVISION\tWHEN\tAVAILABILITY")
	for _, e := range out.Entries {
		project := cmp.Or(e.Project.Key, e.Project.Repo)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", classWord(e.Class, e.Outcome), cellOr(e.Kind), cellOr(e.Title), cellOr(e.Source.Label),
			e.hostWord(), cellOr(project), cellOr(e.Revision.Label), when(e.At), cellOr(e.Availability.Words))
	}
	if err := tw.Flush(); err != nil {
		return 1, err
	}
	switch {
	case out.Total > len(out.Entries) && out.TotalCapped:
		fmt.Fprintf(stdout, "%d of more than %d entries\n", len(out.Entries), out.Total)
	case out.Total > len(out.Entries):
		fmt.Fprintf(stdout, "%d of %d entries\n", len(out.Entries), out.Total)
	}
	if words := cutWords(out.Cut); words != "" {
		fmt.Fprintln(stdout, words)
	}
	if out.Indexing.State != "" && out.Indexing.State != "ready" {
		fmt.Fprintf(stdout, "the history is %s: an entry may be missing yet\n", out.Indexing.State)
	}
	return 0, nil
}

// cutWords says which of the search's bounds it stopped at (cut, the
// reply's cut), "" for none: what was read, so that a search that found
// fewer entries, or none, is not taken for one that read every match.
func cutWords(cut string) string {
	switch cut {
	case history.CutScanned:
		return fmt.Sprintf("the search stopped at its scan bound: its words match more than %d entries, and only the %d most recently indexed were read, newest first and unranked; with filters, older matches are neither counted nor shown: narrow the words", history.MaxScanned, history.MaxScanned)
	case history.CutRanked:
		return fmt.Sprintf("the search stopped at its ranking bound: more than %d entries matched, and only the %d most recently indexed were ranked; narrow the words or the filters", history.MaxRanked, history.MaxRanked)
	}
	return ""
}

// historyShow prints one entry: its line of the table's fields, its
// snippet, its page, its links and whether it can be resumed.
func historyShow(ctx context.Context, c *apiClient, key string, asJSON bool, stdout io.Writer) (int, error) {
	var raw json.RawMessage
	if err := c.doLimit(ctx, http.MethodGet, "/api/history/entry?"+url.Values{"key": {key}}.Encode(), nil, &raw, maxHistoryCLIReply); err != nil {
		return 1, err
	}
	if asJSON {
		return printReply(stdout, raw, false)
	}
	var out struct {
		Entry historyEntry `json:"entry"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 1, fmt.Errorf("unreadable reply from %s: %w", c.host, err)
	}
	e := out.Entry
	fmt.Fprintf(stdout, "%s · %s · %s\n", classWord(e.Class, e.Outcome), e.Kind, oneLine(e.Title))
	fmt.Fprintf(stdout, "key:          %s\n", e.Key)
	fmt.Fprintf(stdout, "source:       %s\n", cellOr(e.Source.Label))
	fmt.Fprintf(stdout, "host:         %s\n", e.hostWord())
	fmt.Fprintf(stdout, "project:      %s\n", cellOr(cmp.Or(e.Project.Key, e.Project.Repo)))
	fmt.Fprintf(stdout, "revision:     %s\n", cellOr(cmp.Or(e.Revision.Commit, e.Revision.Label)))
	fmt.Fprintf(stdout, "when:         %s\n", when(e.At))
	fmt.Fprintf(stdout, "availability: %s\n", cellOr(e.Availability.Words))
	if r := e.Availability.Resume; r != nil {
		switch {
		case r.Offered == "yes" && r.Confirm:
			fmt.Fprintf(stdout, "resume:       offered, with --confirm-lost: %s\n", r.Why)
		case r.Offered == "yes":
			fmt.Fprintln(stdout, "resume:       offered")
		default:
			fmt.Fprintf(stdout, "resume:       %s: %s\n", r.Offered, cellOr(r.Why))
		}
	}
	if e.URL != "" {
		fmt.Fprintf(stdout, "page:         %s%s\n", c.base, e.URL)
	}
	if e.Repeats.Count > 1 {
		fmt.Fprintf(stdout, "said in %d places\n", e.Repeats.Anchors)
	}
	if e.Snippet != "" {
		fmt.Fprintf(stdout, "\n%s\n", textBlock(e.Snippet))
	}
	if len(e.Links) > 0 {
		fmt.Fprintln(stdout, "\nlinks:")
		for _, l := range e.Links {
			what := cellOr(l.Label)
			if l.Class != "" {
				what = classWord(l.Class, l.Outcome) + " " + what
			}
			fmt.Fprintf(stdout, "  %s %s %s\n", l.Dir, l.Rel, what)
		}
	}
	return 0, nil
}

// historySessions prints every session record, a page at a time, one a
// line; with asJSON one JSON array of the rows.
func historySessions(ctx context.Context, c *apiClient, asJSON bool, stdout io.Writer) (int, error) {
	var rows []journal.Summary
	after := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(historyPage)}}
		if after != "" {
			q.Set("after", after)
		}
		var out struct {
			Records  []journal.Summary `json:"records"`
			Next     string            `json:"next"`
			ReadOnly bool              `json:"readOnly"`
		}
		if err := c.doLimit(ctx, http.MethodGet, "/api/history/sessions?"+q.Encode(), nil, &out, maxHistorySessionsReply); err != nil {
			return 1, err
		}
		rows = append(rows, out.Records...)
		if out.Next == "" || len(out.Records) == 0 {
			break
		}
		after = out.Next
	}
	if asJSON {
		b, err := encodeJSON(rows)
		if err != nil {
			return 1, err
		}
		return printReply(stdout, b, false)
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTARTED\tAGENT\tENDING\tNAME\tFOLDER")
	for _, r := range rows {
		ending := r.Ending
		if ending == "" && r.EndedAt.IsZero() && r.LostAt.IsZero() && !r.Unreadable {
			ending = "live"
		}
		if r.Unreadable {
			ending = "unreadable"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, when(r.StartedAt), cellOr(cmp.Or(r.Agent.Name, r.Agent.ID)), cellOr(ending), cellOr(r.Name), cellOr(r.Cwd))
	}
	if err := tw.Flush(); err != nil {
		return 1, err
	}
	return 0, nil
}

// historySession prints a session's record whole.
func historySession(ctx context.Context, c *apiClient, id string, asJSON bool, stdout io.Writer) (int, error) {
	var raw json.RawMessage
	if err := c.doLimit(ctx, http.MethodGet, "/api/history/sessions/"+id, nil, &raw, maxHistoryCLIReply); err != nil {
		return 1, err
	}
	if asJSON {
		return printReply(stdout, raw, false)
	}
	var out struct {
		Record journal.Record `json:"record"`
		Live   bool           `json:"live"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 1, fmt.Errorf("unreadable reply from %s: %w", c.host, err)
	}
	writeRecord(stdout, out.Record, out.Live)
	return 0, nil
}

// writeRecord writes a session's record as conductor history session
// prints it, every string of it through oneLine.
func writeRecord(w io.Writer, r journal.Record, live bool) {
	fmt.Fprintf(w, "%s  %s\n", oneLine(r.ID), oneLine(r.Name))
	agent := oneLine(cmp.Or(r.Agent.Name, r.Agent.ID))
	if r.Agent.Version != "" {
		agent += " " + oneLine(r.Agent.Version)
	}
	fmt.Fprintf(w, "agent:      %s\n", agent)
	if s := r.Agent.Session; s != nil {
		resumable := "not resumable yet"
		if s.Resumable {
			resumable = "resumable"
		}
		fmt.Fprintf(w, "its session: %s (%s, %s)\n", oneLine(s.ID), oneLine(cmp.Or(s.Source, "-")), resumable)
	}
	host := oneLine(cmp.Or(r.Host.Name, "-"))
	if r.Host.User != "" {
		host = oneLine(r.Host.User) + "@" + host
	}
	fmt.Fprintf(w, "host:       %s on %s\n", host, oneLine(cmp.Or(r.Where, "an unknown Runtime")))
	fmt.Fprintf(w, "folder:     %s\n", oneLine(r.Cwd))
	if r.Repo != "" {
		fmt.Fprintf(w, "repository: %s on %s\n", oneLine(r.Repo), oneLine(cmp.Or(r.Branch, "no branch")))
	}
	if r.Start != "" {
		fmt.Fprintf(w, "start:      %s (%s)\n", oneLine(r.Start), oneLine(r.StartFrom))
	}
	if r.End != "" {
		fmt.Fprintf(w, "end:        %s\n", oneLine(r.End))
	}
	if l := r.Launch; l != nil {
		args := strings.Join(l.Args, " ")
		if !l.ArgsKept {
			args = fmt.Sprintf("%d arguments, too long to keep", l.ArgCount)
		}
		fmt.Fprintf(w, "launch:     %s", oneLine(cmp.Or(args, "no arguments")))
		if l.Yolo {
			fmt.Fprint(w, ", yolo")
		}
		if n := max(l.EnvCount, len(l.EnvNames)); n > 0 {
			fmt.Fprintf(w, ", variables %s (their values never kept)", oneLine(envNames(l.EnvNames, n)))
		}
		fmt.Fprintln(w)
	}
	if cr := r.Crew; cr != nil {
		fmt.Fprintf(w, "crew:       run %s", oneLine(cr.RunID))
		if cr.Member != "" {
			fmt.Fprintf(w, ", member %s", oneLine(cr.Member))
		}
		if cr.Task != "" {
			fmt.Fprintf(w, ", task %s attempt %d", oneLine(cr.Task), cr.Attempt)
		}
		fmt.Fprintln(w)
	}
	if r.ResumedFrom != "" {
		fmt.Fprintf(w, "resumed from: %s\n", oneLine(r.ResumedFrom))
	}
	fmt.Fprintf(w, "started:    %s\n", when(r.StartedAt))
	switch {
	case live:
		fmt.Fprintln(w, "running")
	case !r.EndedAt.IsZero():
		fmt.Fprintf(w, "ended:      %s, %s\n", when(r.EndedAt), oneLine(r.Ending))
	case !r.LostAt.IsZero():
		fmt.Fprintf(w, "not observed since %s; whether its process ended is not known\n", when(r.LostAt))
	}
	for _, x := range r.Resumes {
		line := fmt.Sprintf("resume %s by %s at %s: %s", oneLine(x.CommandID), oneLine(cmp.Or(x.Actor, "-")), when(x.At), oneLine(x.State))
		if x.Session != "" && x.State == journal.ResumeStarted {
			line += " as " + oneLine(x.Session)
		}
		if x.Error != "" {
			line += ": " + oneLine(x.Error)
		}
		fmt.Fprintln(w, line)
	}
	if len(r.Entries) > 0 {
		fmt.Fprintln(w, "\nwhat Conductor kept:")
		for _, e := range r.Entries {
			text := cmp.Or(e.Message, e.URL, e.Tool, e.Outcome)
			if e.Kind == journal.KindAnswer {
				text = cmp.Or(e.Outcome, "answered")
				if e.Message != "" {
					text += ": " + e.Message
				}
			}
			kind := e.Kind
			if e.Event != "" {
				kind += " " + e.Event
			}
			fmt.Fprintf(w, "  %s  %s  %s\n", e.At.UTC().Format("15:04:05"), oneLine(kind), oneLine(text))
		}
	}
	if len(r.Files) > 0 {
		fmt.Fprintf(w, "\nfiles: %d\n", len(r.Files))
		for _, f := range r.Files {
			fmt.Fprintf(w, "  %s %s ×%d\n", oneLine(f.Op), oneLine(f.Path), f.Count)
		}
	}
	if r.Dropped > 0 {
		fmt.Fprintf(w, "%d entries and files let go past the record's bound\n", r.Dropped)
	}
}

// envNames names a launch's variables: their names, and how many more.
func envNames(names []string, n int) string {
	if n > len(names) {
		return fmt.Sprintf("%s and %d more", cmp.Or(strings.Join(names, ", "), "none named"), n-len(names))
	}
	return strings.Join(names, ", ")
}

// historyForget forgets a record, or every one (pos empty: --all).
func historyForget(ctx context.Context, c *apiClient, pos []string, stdout io.Writer) (int, error) {
	if len(pos) == 1 {
		if err := c.doLimit(ctx, http.MethodDelete, "/api/history/sessions/"+pos[0], nil, nil, maxHistoryCLIReply); err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "forgot %s\n", pos[0])
		return 0, nil
	}
	var out struct {
		Forgotten int `json:"forgotten"`
		Kept      int `json:"kept"`
	}
	if err := c.doLimit(ctx, http.MethodDelete, "/api/history/sessions", nil, &out, maxHistoryCLIReply); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "forgot %d records; kept %d (live, or being resumed)\n", out.Forgotten, out.Kept)
	return 0, nil
}

// historyResume resumes a record's session under a fresh command id,
// sent once more with that id when its reply is lost (askTwice), and
// asked again while the server answers it still being started.
func historyResume(ctx context.Context, c *apiClient, id string, withoutEnv, confirmLost bool, stdout io.Writer) (int, error) {
	body := map[string]any{"commandId": newOpID()}
	if withoutEnv {
		body["withoutEnv"] = true
	}
	if confirmLost {
		body["confirmLost"] = true
	}
	var out struct {
		Session *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"session"`
		Notice  string         `json:"notice"`
		Receipt journal.Resume `json:"receipt"`
	}
	var err error
	for try := 1; ; try++ {
		err = askTwice(ctx, func(ctx context.Context) error {
			return c.doLimit(ctx, http.MethodPost, "/api/history/sessions/"+id+"/resume", body, &out, maxHistoryCLIReply)
		})
		var refusal replyRefusal
		if try >= historyResumeTries || !errors.As(err, &refusal) || refusal.status != http.StatusConflict || !strings.HasPrefix(refusal.Error(), "resume_pending:") {
			break
		}
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case <-time.After(historyResumeWait):
		}
	}
	if err != nil {
		return 1, err
	}
	sid := out.Receipt.Session
	name := ""
	if out.Session != nil {
		sid, name = out.Session.ID, out.Session.Name
	}
	line := "resumed " + id + " as " + sid
	if name != "" {
		line += " (" + oneLine(name) + ")"
	}
	fmt.Fprintln(stdout, line)
	if out.Notice != "" {
		fmt.Fprintln(stdout, out.Notice)
	}
	fmt.Fprintf(stdout, "%s/sessions/%s\n", c.base, sid)
	return 0, nil
}
