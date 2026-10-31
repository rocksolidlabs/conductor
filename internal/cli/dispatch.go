package cli

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// conductor dispatch: Dispatch, Conductor's own work tracking
// (docs/plans/dispatch.md), from the shell, through the admin's routes
// as the workbench uses them, with the server and the token conductor
// crews takes. Reads are bounded by maxDispatchReply; the lists follow
// their pages to the end. Every write is one of the store's typed
// operations: it mints its operation's id, and a reply lost to the
// network is asked for once more with the same id, which the server
// answers from its receipt without a second change, as the workbench
// does. Labels and milestones are named by their names, which the
// project's read maps to their ids. Nothing here creates a project, or
// edits one: that is the workbench's.

const dispatchUsage = `Usage:
  conductor dispatch projects
      the projects, one a line: key, name, how many of its tickets are open and done
  conductor dispatch tickets [--project KEY] [--status S]... [--q TEXT] [--json]
      the tickets, one a line: key, status, priority, owner, title; the whole
      list, filtered by project, by status (any of those given) and by words in
      a ticket or its comments; --json prints them as one JSON array
  conductor dispatch show KEY-n
      a ticket whole: its fields, description, acceptance criteria, close
      policy, parent, children, relations, links, attachments and last comments
  conductor dispatch create --project KEY --title T [--description-file F] [--priority P]
                            [--owner NAME] [--label L]...
      make a ticket and print its key; F a file of Markdown, - for stdin
  conductor dispatch edit KEY-n [--status S] [--priority P] [--owner NAME] [--assignee +x|-x]...
                          [--label +l|-l]... [--milestone NAME] [--horizon H]
      change a ticket in one operation: + adds and - removes an assignee
      (person:NAME, agent:ID, or a person's NAME) or a label; "" clears the
      owner, the milestone or the horizon
  conductor dispatch comment KEY-n TEXT...
      comment on a ticket: TEXT is every word after the key as written, one
      that starts with - among them, so flags go before the key
  conductor dispatch accept KEY-n [--note TEXT]
      record the owner's acceptance of a ticket, for its owner and criteria now
  conductor dispatch link KEY-n --kind delivery|run|external [--url U] [--ref R]
      link a ticket: a delivery (a merged pull request, a release) or an
      external link names its --url, a run its id as --ref

Statuses: backlog, ready, in_progress, in_review, done, cancelled. Priorities:
none, low, medium, high, urgent, or 0 to 4. Horizons: now, next, later. Labels
and milestones by their names.

Every command takes --server, --token and --insecure as conductor crews does:
the server defaults to CONDUCTOR_SERVER, else http://localhost:8080, the token
to CONDUCTOR_WORKBENCH_TOKEN, and a plain http:// server on another machine is
refused unless --insecure says to go ahead. A write whose reply is lost to the
network is sent once more with the same operation id, which the server answers
without a second change.
`

// maxDispatchReply bounds what conductor dispatch reads of a reply: the
// smallest whole number of MiB above the largest reply any read it makes
// can carry, a ticket's (api.MaxTicketReply, an operation's reply on one
// among them) and a project's (api.MaxProjectReply, read for the names
// of its labels and milestones), which internal/api's tests measure; a
// page of 200 projects or tickets is far under it. Derived, never
// guessed: a reply bound raised there raises this.
const maxDispatchReply = (max(api.MaxTicketReply, api.MaxProjectReply)>>20 + 1) << 20

// dispatchPage is how many projects or tickets conductor dispatch asks
// for a page at a time: the routes' most. A variable so that a test can
// walk a few through several pages.
var dispatchPage = 200

// lostReply is a request whose reply never came: refused, cut or left
// unanswered by the network. A write with an operation id is sent once
// more (askTwice); the server answers the second from its receipt.
type lostReply struct{ err error }

func (l lostReply) Error() string { return l.err.Error() }
func (l lostReply) Unwrap() error { return l.err }

// askTwice runs send, and once more when its reply was lost and ctx is
// still live: what the workbench does with a write (sendCommand). A
// second loss is the error. Each attempt sends with the context it is
// handed: with ctx's deadline, the first has half of what is left of it,
// so a first reply left unanswered leaves the resend the other half (a
// budget no longer than one request's own timeout, ticket_comment's 60 s
// beside the session client's, would otherwise go to the first whole);
// the resend has the rest of ctx.
func askTwice(ctx context.Context, send func(context.Context) error) error {
	first, cancel := ctx, context.CancelFunc(func() {})
	if deadline, ok := ctx.Deadline(); ok {
		first, cancel = context.WithTimeout(ctx, time.Until(deadline)/2)
	}
	err := send(first)
	cancel()
	var lost lostReply
	if errors.As(err, &lost) && ctx.Err() == nil {
		err = send(ctx)
	}
	return err
}

// newOpID mints an operation's id: 16 lower-case hex characters from 8
// random bytes, as the workbench mints its command ids, within
// dispatch.OpIDPattern.
func newOpID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// encodeJSON is v as JSON with HTML escaping off, as the server writes
// Dispatch's replies: '<', '>' and '&' stay one byte, so a text field
// encodes to at most twice its bytes, which the routes' body bounds are
// measured at (a description of 64 KiB of '<' fits a ticket's 192 KiB).
func encodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// dispatchFlags is what conductor dispatch's commands take beside the
// server's; each command defines its own.
type dispatchFlags struct {
	project, title, descriptionFile, priority, owner *string
	status, q, milestone, horizon, note              *string
	kind, url, ref                                   *string
	json                                             *bool
	statuses, labels, assignees                      multiFlag
}

// dispatchArgs is how many arguments each command takes: none, a
// ticket's key, or a key and the text after it.
var dispatchArgs = map[string]string{
	"projects": "", "tickets": "", "create": "",
	"show": "KEY-n", "edit": "KEY-n", "accept": "KEY-n", "link": "KEY-n",
	"comment": "KEY-n TEXT...",
}

// runDispatch is conductor dispatch. Exit 2 for a usage error, before
// any request; 1 when the server refuses or cannot be asked.
func runDispatch(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, dispatchUsage)
		if len(args) == 0 {
			return 2, nil
		}
		return 0, nil
	}
	sub, rest := args[0], args[1:]
	takes, known := dispatchArgs[sub]
	if !known {
		fmt.Fprint(stderr, dispatchUsage)
		return 2, fmt.Errorf("dispatch: unknown command %q", sub)
	}
	fs := flag.NewFlagSet("dispatch "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := addAPIFlags(fs)
	var d dispatchFlags
	switch sub {
	case "tickets":
		d.project = fs.String("project", "", "a project's key: its tickets alone")
		fs.Var(&d.statuses, "status", "a status: the tickets in any of those given (repeatable)")
		d.q = fs.String("q", "", "words, each in the ticket or its comments, the last a prefix")
		d.json = fs.Bool("json", false, "print the tickets as one JSON array")
	case "create":
		d.project = fs.String("project", "", "the project's key (required)")
		d.title = fs.String("title", "", "the title (required)")
		d.descriptionFile = fs.String("description-file", "", "a file of Markdown for the description, - for stdin")
		d.priority = fs.String("priority", "", "none, low, medium, high or urgent, or 0 to 4")
		d.owner = fs.String("owner", "", "the accountable person's name")
		fs.Var(&d.labels, "label", "a label's name (repeatable)")
	case "edit":
		d.status = fs.String("status", "", "the status")
		d.priority = fs.String("priority", "", "none, low, medium, high or urgent, or 0 to 4")
		d.owner = fs.String("owner", "", `the accountable person's name, "" for none`)
		fs.Var(&d.assignees, "assignee", "+x adds and -x removes an assignee: person:NAME, agent:ID or a person's NAME (repeatable)")
		fs.Var(&d.labels, "label", "+l adds and -l removes a label, by its name (repeatable)")
		d.milestone = fs.String("milestone", "", `a milestone's name, "" for none`)
		d.horizon = fs.String("horizon", "", `now, next or later, "" for none`)
	case "accept":
		d.note = fs.String("note", "", "a note of the acceptance, Markdown")
	case "link":
		d.kind = fs.String("kind", "", "delivery, run or external (required)")
		d.url = fs.String("url", "", "the link's URL, http or https: a delivery's or an external link's")
		d.ref = fs.String("ref", "", "the link's reference: a run's id for a run link")
	}
	fs.Usage = func() {
		fmt.Fprint(stderr, dispatchUsage, "\nFlags of dispatch "+sub+":\n")
		fs.PrintDefaults()
	}
	// A comment's text is every word after its key, as written: a word
	// that starts with '-' (a Markdown list item, "-1 on this") is the
	// comment's, never a flag, so its flags go before the key.
	var pos []string
	var err error
	if sub == "comment" {
		err = fs.Parse(rest)
		pos = fs.Args()
	} else {
		pos, err = parseInterspersed(fs, rest)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	var key string
	switch takes {
	case "":
		if len(pos) != 0 {
			return 2, fmt.Errorf("dispatch %s takes no argument: %q", sub, pos[0])
		}
	case "KEY-n":
		if len(pos) != 1 {
			return 2, fmt.Errorf("dispatch %s takes a ticket's key, as CND-12", sub)
		}
	default:
		if len(pos) < 2 {
			return 2, fmt.Errorf("dispatch %s takes a ticket's key and the text", sub)
		}
	}
	if takes != "" {
		key = pos[0]
		if _, _, ok := dispatch.ParseKey(key); !ok {
			return 2, fmt.Errorf("dispatch %s: %q is no ticket's key: a project's key and a number, as CND-12", sub, key)
		}
	}
	w, err := d.write(sub, key, pos, given, stdin)
	if err != nil {
		return 2, fmt.Errorf("dispatch %s: %w", sub, err)
	}
	c, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	switch sub {
	case "projects":
		return dispatchProjects(ctx, c, stdout)
	case "tickets":
		return dispatchTickets(ctx, c, d, stdout)
	case "show":
		return dispatchShow(ctx, c, key, stdout)
	}
	return w.send(ctx, c, stdout)
}

// dispatchWrite is a write conductor dispatch makes: its route, its body
// (the operation's id added when it is sent), the names its flags gave
// for labels and a milestone, which the project's read turns into ids
// first, and what it prints from the reply.
type dispatchWrite struct {
	key, path string
	body      map[string]any
	// addLabels, removeLabels and milestone are names; milestoneSet says
	// a milestone was named, "" for none.
	addLabels, removeLabels []string
	milestone               string
	milestoneSet            bool
	print                   func(w io.Writer, out dispatchReply)
}

// dispatchReply is a write's reply: the receipt and what it acted on.
type dispatchReply struct {
	Receipt dispatch.Receipt     `json:"receipt"`
	Ticket  *dispatch.TicketView `json:"ticket"`
	Comment *dispatch.Comment    `json:"comment"`
}

// write checks a write's flags and arguments and builds it, before any
// request: its errors are usage errors. Reads build nothing.
func (d dispatchFlags) write(sub, key string, pos []string, given map[string]bool, stdin io.Reader) (*dispatchWrite, error) {
	ticketPath := "/api/dispatch/tickets/" + url.PathEscape(key)
	switch sub {
	case "tickets":
		for _, s := range d.statuses {
			if !slices.Contains(dispatch.Statuses, s) {
				return nil, fmt.Errorf("a status is one of %s: %q", strings.Join(dispatch.Statuses, ", "), s)
			}
		}
		if *d.project != "" && !dispatch.ValidKey(*d.project) {
			return nil, fmt.Errorf("--project is a project's key, as CND: %q", *d.project)
		}
		return nil, nil
	case "create":
		if !dispatch.ValidKey(*d.project) {
			return nil, errors.New("--project names the project by its key, as CND")
		}
		if strings.TrimSpace(*d.title) == "" {
			return nil, errors.New("--title is the ticket's title")
		}
		w := &dispatchWrite{key: *d.project, path: "/api/dispatch/tickets", body: map[string]any{"project": *d.project, "title": *d.title}, addLabels: d.labels}
		if *d.descriptionFile != "" {
			text, err := readDescription(*d.descriptionFile, stdin)
			if err != nil {
				return nil, err
			}
			w.body["description"] = text
		}
		if given["priority"] {
			p, err := parsePriority(*d.priority)
			if err != nil {
				return nil, err
			}
			w.body["priority"] = p
		}
		if *d.owner != "" {
			w.body["owner"] = *d.owner
		}
		w.print = func(o io.Writer, out dispatchReply) {
			fmt.Fprintln(o, createdKey(out))
		}
		return w, nil
	case "edit":
		w := &dispatchWrite{key: key, path: ticketPath + "/edit", body: map[string]any{}}
		set := map[string]any{}
		if given["status"] {
			if !slices.Contains(dispatch.Statuses, *d.status) {
				return nil, fmt.Errorf("a status is one of %s: %q", strings.Join(dispatch.Statuses, ", "), *d.status)
			}
			set["status"] = *d.status
		}
		if given["priority"] {
			p, err := parsePriority(*d.priority)
			if err != nil {
				return nil, err
			}
			set["priority"] = p
		}
		if given["owner"] {
			set["owner"] = *d.owner
		}
		if given["horizon"] {
			if !slices.Contains(dispatch.Horizons, *d.horizon) {
				return nil, fmt.Errorf(`a horizon is now, next, later, or "" for none: %q`, *d.horizon)
			}
			set["horizon"] = *d.horizon
		}
		if given["milestone"] {
			w.milestone, w.milestoneSet = *d.milestone, true
		}
		add, remove := map[string]any{}, map[string]any{}
		var addAssignees, removeAssignees []string
		for _, a := range d.assignees {
			sign, who := signed(a)
			if sign == 0 || who == "" {
				return nil, fmt.Errorf("--assignee takes +x to add or -x to remove: %q", a)
			}
			if sign > 0 {
				addAssignees = append(addAssignees, actorOf(who))
			} else {
				removeAssignees = append(removeAssignees, actorOf(who))
			}
		}
		for _, l := range d.labels {
			sign, name := signed(l)
			if sign == 0 || name == "" {
				return nil, fmt.Errorf("--label takes +l to add or -l to remove: %q", l)
			}
			if sign > 0 {
				w.addLabels = append(w.addLabels, name)
			} else {
				w.removeLabels = append(w.removeLabels, name)
			}
		}
		if len(addAssignees) > 0 {
			add["assignees"] = addAssignees
		}
		if len(removeAssignees) > 0 {
			remove["assignees"] = removeAssignees
		}
		if len(set) == 0 && len(add) == 0 && len(remove) == 0 && len(w.addLabels) == 0 && len(w.removeLabels) == 0 && !w.milestoneSet {
			return nil, errors.New("say what to change: --status, --priority, --owner, --assignee, --label, --milestone or --horizon")
		}
		w.body["set"], w.body["add"], w.body["remove"] = set, add, remove
		w.print = func(o io.Writer, out dispatchReply) {
			if out.Ticket != nil {
				fmt.Fprintln(o, ticketLine(out.Ticket.Key, out.Ticket.Status, out.Ticket.Priority, out.Ticket.Owner, out.Ticket.Title))
				return
			}
			fmt.Fprintf(o, "%s changed\n", key)
		}
		return w, nil
	case "comment":
		text := strings.Join(pos[1:], " ")
		if strings.TrimSpace(text) == "" {
			return nil, errors.New("the comment is empty")
		}
		return &dispatchWrite{key: key, path: ticketPath + "/comments", body: map[string]any{"body": text}, print: func(o io.Writer, out dispatchReply) {
			if out.Comment != nil {
				fmt.Fprintf(o, "comment #%d on %s\n", out.Comment.N, key)
				return
			}
			fmt.Fprintf(o, "commented on %s\n", key)
		}}, nil
	case "accept":
		body := map[string]any{}
		if *d.note != "" {
			body["note"] = *d.note
		}
		return &dispatchWrite{key: key, path: ticketPath + "/accept", body: body, print: func(o io.Writer, out dispatchReply) {
			if out.Ticket != nil {
				fmt.Fprintf(o, "%s accepted for %s\n", key, oneLine(out.Ticket.AcceptedFor))
				return
			}
			fmt.Fprintf(o, "%s accepted\n", key)
		}}, nil
	case "link":
		if !slices.Contains(dispatch.LinkKinds, *d.kind) {
			return nil, fmt.Errorf("--kind is delivery, run or external: %q", *d.kind)
		}
		switch {
		case *d.kind == dispatch.LinkRun && *d.ref == "":
			return nil, errors.New("a run link names its run: --ref <run id>")
		case *d.kind != dispatch.LinkRun && *d.url == "":
			return nil, fmt.Errorf("a %s link names its URL: --url", *d.kind)
		}
		body := map[string]any{"kind": *d.kind}
		if *d.url != "" {
			body["url"] = *d.url
		}
		if *d.ref != "" {
			body["ref"] = *d.ref
		}
		return &dispatchWrite{key: key, path: ticketPath + "/links", body: body, print: func(o io.Writer, out dispatchReply) {
			fmt.Fprintf(o, "%s linked: %s %s\n", key, *d.kind, oneLine(cmp.Or(*d.ref, *d.url)))
		}}, nil
	}
	return nil, nil
}

// send resolves the write's names, mints its operation's id and sends
// it, once more on a lost reply (askTwice), then prints its reply. A
// refusal is the server's code and words.
func (w *dispatchWrite) send(ctx context.Context, c *apiClient, stdout io.Writer) (int, error) {
	if len(w.addLabels) > 0 || len(w.removeLabels) > 0 || w.milestoneSet && w.milestone != "" {
		project, _, ok := dispatch.ParseKey(w.key)
		if !ok {
			project = w.key
		}
		pv, err := c.dispatchProject(ctx, project)
		if err != nil {
			return 1, err
		}
		add, err := labelIDs(pv, w.addLabels)
		if err != nil {
			return 1, err
		}
		remove, err := labelIDs(pv, w.removeLabels)
		if err != nil {
			return 1, err
		}
		if w.path == "/api/dispatch/tickets" {
			if len(add) > 0 {
				w.body["labels"] = add
			}
		} else {
			if len(add) > 0 {
				w.body["add"].(map[string]any)["labels"] = add
			}
			if len(remove) > 0 {
				w.body["remove"].(map[string]any)["labels"] = remove
			}
		}
		if w.milestoneSet && w.milestone != "" {
			id, err := milestoneID(pv, w.milestone)
			if err != nil {
				return 1, err
			}
			w.body["set"].(map[string]any)["milestone"] = id
		}
	}
	if w.milestoneSet && w.milestone == "" {
		w.body["set"].(map[string]any)["milestone"] = ""
	}
	for _, part := range []string{"set", "add", "remove"} {
		if m, ok := w.body[part].(map[string]any); ok && len(m) == 0 {
			delete(w.body, part)
		}
	}
	w.body["opId"] = newOpID()
	var out dispatchReply
	if err := askTwice(ctx, func(ctx context.Context) error {
		return c.doLimit(ctx, http.MethodPost, w.path, w.body, &out, maxDispatchReply)
	}); err != nil {
		return 1, err
	}
	w.print(stdout, out)
	return 0, nil
}

// createdKey is the key a create's reply names: the ticket's, else the
// receipt's.
func createdKey(out dispatchReply) string {
	if out.Ticket != nil && out.Ticket.Key != "" {
		return out.Ticket.Key
	}
	return out.Receipt.Key
}

// readDescription reads a ticket's description from the file named, or
// stdin for "-", at most dispatch.MaxTicketDescription bytes: past that
// the store would refuse it, so it is refused here before anything is
// sent.
func readDescription(name string, stdin io.Reader) (string, error) {
	r := stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, dispatch.MaxTicketDescription+1))
	if err != nil {
		return "", err
	}
	if len(b) > dispatch.MaxTicketDescription {
		return "", fmt.Errorf("%s: a description is at most %d KiB", name, dispatch.MaxTicketDescription>>10)
	}
	return string(b), nil
}

// parsePriority reads a priority: its word or its number, 0 (none) to 4
// (urgent).
func parsePriority(s string) (int, error) {
	if i := slices.Index(dispatch.Priorities, s); i >= 0 {
		return i, nil
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 0 && n <= dispatch.MaxPriority {
		return n, nil
	}
	return 0, fmt.Errorf("a priority is none, low, medium, high or urgent, or 0 to 4: %q", s)
}

// priorityWord is a priority's word, its number for one past the words.
func priorityWord(p int) string {
	if p >= 0 && p < len(dispatch.Priorities) {
		return dispatch.Priorities[p]
	}
	return strconv.Itoa(p)
}

// signed splits a set's member as a flag gives it: +x adds x (1), -x
// removes it (-1), anything else is neither (0).
func signed(s string) (int, string) {
	switch {
	case strings.HasPrefix(s, "+"):
		return 1, s[1:]
	case strings.HasPrefix(s, "-"):
		return -1, s[1:]
	}
	return 0, s
}

// actorOf is an assignee as the store names it: person:NAME or agent:ID
// as given, else a person's NAME.
func actorOf(who string) string {
	if strings.HasPrefix(who, "person:") || strings.HasPrefix(who, "agent:") {
		return who
	}
	return "person:" + who
}

// dispatchProject reads a project with its labels and milestones.
func (c *apiClient) dispatchProject(ctx context.Context, key string) (dispatch.ProjectView, error) {
	var v dispatch.ProjectView
	err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/projects/"+url.PathEscape(key), nil, &v, maxDispatchReply)
	return v, err
}

// byName finds the one row a name names among a project's: the rows of
// that name, else, when none is, those of that name whatever its case.
// Two rows of one name (a merge may bring a second label of a name, and
// milestones' names repeat) are refused rather than guessed between.
func byName(what, project, name string, names, ids []string) (string, error) {
	for _, fold := range []bool{false, true} {
		var found []string
		for i, n := range names {
			if n == name || fold && strings.EqualFold(n, name) {
				found = append(found, ids[i])
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil
		}
		return "", fmt.Errorf("%s has %d %ss named %q: rename one in the workbench", project, len(found), what, name)
	}
	return "", fmt.Errorf("%s has no %s named %q", project, what, name)
}

// labelIDs is the ids of the project's live labels with the names.
func labelIDs(pv dispatch.ProjectView, names []string) ([]string, error) {
	var all, ids, out []string
	for _, l := range pv.Labels {
		all, ids = append(all, l.Name), append(ids, l.ID)
	}
	for _, n := range names {
		id, err := byName("label", pv.Project.Key, n, all, ids)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// milestoneID is the id of the project's live milestone with the name.
func milestoneID(pv dispatch.ProjectView, name string) (string, error) {
	var all, ids []string
	for _, m := range pv.Milestones {
		all, ids = append(all, m.Name), append(ids, m.ID)
	}
	return byName("milestone", pv.Project.Key, name, all, ids)
}

// dispatchProjects prints every project, page by page: key, name, and how
// many of its tickets are open and done; external for a project whose
// tracker is another.
func dispatchProjects(ctx context.Context, c *apiClient, stdout io.Writer) (int, error) {
	var all []dispatch.ProjectSummary
	for cursor := ""; ; {
		q := url.Values{"limit": {strconv.Itoa(dispatchPage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page dispatch.ProjectPage
		if err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/projects?"+q.Encode(), nil, &page, maxDispatchReply); err != nil {
			return 1, err
		}
		all = append(all, page.Projects...)
		if page.Next == "" || page.Next == cursor || len(page.Projects) == 0 {
			break
		}
		cursor = page.Next
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "no projects")
		return 0, nil
	}
	for _, p := range all {
		line := fmt.Sprintf("%s\t%s\t%d open, %d done", oneLine(p.Key), oneLine(p.Name), p.Open, p.Done)
		if p.Tracker == dispatch.TrackerExternal {
			line += "\texternal"
		}
		fmt.Fprintln(stdout, line)
	}
	return 0, nil
}

// dispatchTickets prints every ticket of a list, page by page: key,
// status, priority, owner (- for none) and title, one a line; or, with
// --json, the summaries as one JSON array.
func dispatchTickets(ctx context.Context, c *apiClient, d dispatchFlags, stdout io.Writer) (int, error) {
	all := []dispatch.TicketSummary{}
	for cursor := ""; ; {
		q := url.Values{"limit": {strconv.Itoa(dispatchPage)}}
		if *d.project != "" {
			q.Set("project", *d.project)
		}
		for _, s := range d.statuses {
			q.Add("status", s)
		}
		if *d.q != "" {
			q.Set("q", *d.q)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page dispatch.Page
		if err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/tickets?"+q.Encode(), nil, &page, maxDispatchReply); err != nil {
			return 1, err
		}
		all = append(all, page.Tickets...)
		if page.Next == "" || page.Next == cursor || len(page.Tickets) == 0 {
			break
		}
		cursor = page.Next
	}
	if *d.json {
		b, err := encodeJSON(all)
		if err != nil {
			return 1, err
		}
		var indented bytes.Buffer
		if err := json.Indent(&indented, b, "", "  "); err != nil {
			return 1, err
		}
		fmt.Fprintln(stdout, indented.String())
		return 0, nil
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "no tickets")
		return 0, nil
	}
	for _, t := range all {
		fmt.Fprintln(stdout, ticketLine(t.Key, t.Status, t.Priority, t.Owner, t.Title))
	}
	return 0, nil
}

// ticketLine is a ticket as a list prints it: key, status, priority,
// owner (- for none) and title, separated by tabs.
func ticketLine(key, status string, priority int, owner, title string) string {
	if owner == "" {
		owner = "-"
	}
	return strings.Join([]string{oneLine(key), oneLine(status), priorityWord(priority), oneLine(owner), oneLine(title)}, "\t")
}

// dispatchShow prints a ticket whole (writeTicket), its labels and
// milestone by name from its project's read.
func dispatchShow(ctx context.Context, c *apiClient, key string, stdout io.Writer) (int, error) {
	var out struct {
		Ticket dispatch.TicketView `json:"ticket"`
	}
	if err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/tickets/"+url.PathEscape(key), nil, &out, maxDispatchReply); err != nil {
		return 1, err
	}
	project, _, _ := dispatch.ParseKey(key)
	pv, err := c.dispatchProject(ctx, project)
	if err != nil {
		return 1, err
	}
	writeTicket(stdout, out.Ticket, namesOf(pv.Labels, pv.Milestones))
	return 0, nil
}

// ticketNames is what a ticket's printout names by their ids: its labels
// and its milestone.
type ticketNames struct {
	labels, milestones map[string]string
}

// namesOf maps labels' and milestones' ids to their names.
func namesOf(labels []dispatch.Label, milestones []dispatch.Milestone) ticketNames {
	n := ticketNames{labels: map[string]string{}, milestones: map[string]string{}}
	for _, l := range labels {
		n.labels[l.ID] = l.Name
	}
	for _, m := range milestones {
		n.milestones[m.ID] = m.Name
	}
	return n
}

// oneLine is s for one line of a terminal: a newline or a tab a space,
// and every other control character U+FFFD, so nothing a reply carries
// is read by a terminal as a control sequence.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case unicode.IsControl(r):
			return utf8.RuneError
		}
		return r
	}, s)
}

// textBlock is s for a block of lines, Markdown as a ticket keeps it: its
// newlines and tabs kept and every other control character U+FFFD.
func textBlock(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return utf8.RuneError
		}
		return r
	}, s)
}

// when is a time as a printout gives it, to the minute in UTC.
func when(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// size is a file's size in bytes, KiB or MiB.
func size(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// writeTicket prints a ticket whole, as Markdown: its key and title, a
// list of its fields (status, priority, horizon, owner, assignees, labels
// and milestone by name, parent, whether a live ticket blocks it, its
// close policy, its acceptance, its deletion), then its description, its
// acceptance criteria, its children, its relations, its links, its
// attachments and the comments its read carries, each section only when
// it has something. What its read leaves out is said: the comments before
// the last ones, the relations and children past the first.
func writeTicket(w io.Writer, v dispatch.TicketView, names ticketNames) {
	fmt.Fprintf(w, "# %s: %s\n\n", oneLine(v.Key), oneLine(v.Title))
	field := func(name, value string) { fmt.Fprintf(w, "- %s: %s\n", name, value) }
	field("status", oneLine(v.Status))
	field("priority", priorityWord(v.Priority))
	if v.Horizon != "" {
		field("horizon", oneLine(v.Horizon))
	}
	if v.Owner != "" {
		field("owner", oneLine(v.Owner))
	}
	if len(v.Assignees) > 0 {
		field("assignees", oneLine(strings.Join(v.Assignees, ", ")))
	}
	if len(v.Labels) > 0 {
		labels := make([]string, len(v.Labels))
		for i, id := range v.Labels {
			labels[i] = cmp.Or(names.labels[id], id)
		}
		field("labels", oneLine(strings.Join(labels, ", ")))
	}
	if v.MilestoneID != "" {
		field("milestone", oneLine(cmp.Or(names.milestones[v.MilestoneID], v.MilestoneID)))
	}
	if p := v.Parent; p != nil {
		field("parent", relatedText(*p))
	}
	if v.Blocked {
		field("blocked", "a live ticket that is not done or cancelled blocks it")
	}
	if c := v.Close; len(c.Requires) > 0 {
		policy := strings.Join(c.Requires, " and ")
		if len(c.Missing) == 0 {
			policy += ", met"
		} else {
			policy += ", missing " + strings.Join(c.Missing, " and ")
		}
		if c.Why != "" {
			policy += " (" + c.Why + ")"
		}
		field("to close", oneLine(policy))
	}
	if !v.AcceptedAt.IsZero() {
		field("accepted", oneLine(fmt.Sprintf("by %s for %s, %s", v.AcceptedBy, v.AcceptedFor, when(v.AcceptedAt))))
	}
	if !v.DeletedAt.IsZero() {
		field("deleted", when(v.DeletedAt))
	}
	section := func(title, body string) {
		if strings.TrimSpace(body) != "" {
			fmt.Fprintf(w, "\n## %s\n\n%s\n", title, strings.TrimRight(textBlock(body), "\n"))
		}
	}
	section("Description", v.Description)
	section("Acceptance criteria", v.Acceptance)
	if v.AcceptedNote != "" {
		section("Acceptance note", v.AcceptedNote)
	}
	if n := v.Children.Total; n > 0 {
		var b strings.Builder
		for _, c := range v.Children.Tickets {
			fmt.Fprintf(&b, "- %s\n", relatedText(c))
		}
		if more := n - len(v.Children.Tickets); more > 0 {
			fmt.Fprintf(&b, "- and %d more\n", more)
		}
		section(fmt.Sprintf("Children (%d)", n), b.String())
	}
	if n := v.RelationsTotal; n > 0 {
		var b strings.Builder
		for _, r := range v.Relations {
			how := "relates to"
			switch {
			case r.Kind == dispatch.RelationBlocks && r.FromID == v.ID:
				how = "blocks"
			case r.Kind == dispatch.RelationBlocks:
				how = "blocked by"
			}
			fmt.Fprintf(&b, "- %s %s\n", how, relatedText(r.Other))
		}
		if more := n - len(v.Relations); more > 0 {
			fmt.Fprintf(&b, "- and %d more\n", more)
		}
		section(fmt.Sprintf("Relations (%d)", n), b.String())
	}
	if len(v.Links) > 0 {
		var b strings.Builder
		for _, l := range v.Links {
			fmt.Fprintf(&b, "- %s %s", oneLine(l.Kind), oneLine(l.Ref))
			if l.URL != "" && l.URL != l.Ref {
				fmt.Fprintf(&b, " %s", oneLine(l.URL))
			}
			b.WriteString("\n")
			if l.Note != "" {
				fmt.Fprintf(&b, "  %s\n", strings.ReplaceAll(textBlock(l.Note), "\n", "\n  "))
			}
		}
		section(fmt.Sprintf("Links (%d)", len(v.Links)), b.String())
	}
	if len(v.Attachments) > 0 {
		var b strings.Builder
		for _, a := range v.Attachments {
			fmt.Fprintf(&b, "- %s (%s, %s)\n", oneLine(a.Name), size(a.Size), oneLine(a.ContentType))
		}
		section(fmt.Sprintf("Attachments (%d)", len(v.Attachments)), b.String())
	}
	if len(v.Comments) > 0 {
		title := fmt.Sprintf("Comments (%d)", len(v.Comments))
		if total := v.Comments[len(v.Comments)-1].N; total > len(v.Comments) {
			title = fmt.Sprintf("Comments (the last %d of %d)", len(v.Comments), total)
		}
		var b strings.Builder
		for _, c := range v.Comments {
			if !c.DeletedAt.IsZero() {
				fmt.Fprintf(&b, "### #%d, deleted\n\n", c.N)
				continue
			}
			head := fmt.Sprintf("### #%d %s, %s", c.N, oneLine(cmp.Or(c.By, c.Author)), when(c.CreatedAt))
			if !c.EditedAt.IsZero() {
				head += ", edited"
			}
			fmt.Fprintf(&b, "%s\n\n%s\n\n", head, strings.TrimRight(textBlock(c.Body), "\n"))
		}
		section(title, b.String())
	}
}

// relatedText is a ticket at a reference's other end: its key, title and
// status, and that it is deleted when it is.
func relatedText(r dispatch.RelatedTicket) string {
	s := fmt.Sprintf("%s: %s (%s)", oneLine(r.Key), oneLine(r.Title), oneLine(r.Status))
	if !r.DeletedAt.IsZero() {
		s += ", deleted"
	}
	return s
}

// sessionTicket is what a session reads of its run's ticket (GET
// /api/sessions/{id}/ticket): the ticket, its labels' rows and its
// milestone's.
type sessionTicket struct {
	Ticket    dispatch.TicketView `json:"ticket"`
	Labels    []dispatch.Label    `json:"labels"`
	Milestone *dispatch.Milestone `json:"milestone"`
}

// readSessionTicket reads the ticket the session's run is linked to,
// through the session's own routes with its token: the reply as it came
// and as read.
func readSessionTicket(ctx context.Context, c *crewClient) (json.RawMessage, sessionTicket, error) {
	var raw json.RawMessage
	if err := c.into(ctx, http.MethodGet, "/ticket", nil, &raw); err != nil {
		return nil, sessionTicket{}, err
	}
	var st sessionTicket
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, sessionTicket{}, fmt.Errorf("%s: an unreadable ticket: %w", c.prefix, err)
	}
	return raw, st, nil
}

// sessionTicketText is the session's ticket as writeTicket prints it.
func sessionTicketText(st sessionTicket) string {
	var milestones []dispatch.Milestone
	if st.Milestone != nil {
		milestones = append(milestones, *st.Milestone)
	}
	var b strings.Builder
	writeTicket(&b, st.Ticket, namesOf(st.Labels, milestones))
	return b.String()
}

// commentOnSessionTicket comments on the ticket the session's run is
// linked to, as the session's member: the operation's id minted, the
// comment sent once more on a lost reply (askTwice), and the comment as
// the server kept it, with its number, or nil when the reply carries
// none: the comment was made, and the server could not read it back
// (writeOp's comment null), or its receipt was pruned before a resend.
func commentOnSessionTicket(ctx context.Context, c *crewClient, text string) (*dispatch.Comment, error) {
	body := map[string]any{"opId": newOpID(), "body": text}
	var out struct {
		Comment *dispatch.Comment `json:"comment"`
	}
	err := askTwice(ctx, func(ctx context.Context) error { return c.into(ctx, http.MethodPost, "/ticket/comments", body, &out) })
	return out.Comment, err
}

// sessionCommentText is what conductor task ticket comment and
// ticket_comment say of a comment made: its number when the reply
// carries the comment, else only that it was made.
func sessionCommentText(cm *dispatch.Comment) string {
	if cm == nil {
		return "commented on your ticket"
	}
	return fmt.Sprintf("comment #%d on your ticket", cm.N)
}
