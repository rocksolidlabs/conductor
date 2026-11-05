package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/notify"
)

// conductor brakeman and Brakeman's three MCP tools (docs/plans/
// brakeman.md, PR-5). In a Brakeman turn's session (CONDUCTOR_BRAKEMAN,
// set by the server) the turn reads its request, reads what it may see and
// submits one brief, with the session's own token, as its MCP tools do for
// an agent with MCP. With the workbench token a person asks Brakeman and
// reads what it answered. Every route, schema and bound is the server's
// table's (api.Operations): observe sends each what to its own route on
// this side, and the server has no dispatcher of its own.

// envBrakeman names the request a Brakeman turn's session answers.
const envBrakeman = "CONDUCTOR_BRAKEMAN"

const brakemanUsage = `Usage:
In a Brakeman turn's session (CONDUCTOR_BRAKEMAN is set), with the session's token:
  conductor brakeman request [--json]       the request this turn answers, what it may see and its budget
  conductor brakeman observe WHAT [--KEY VALUE]...
                                            read one thing within the turn's scope and visibility:
                                            WHAT is changes, status, screen, overlaps, briefs, runs, run,
                                            task, steps, log, command, messages, projects, tickets,
                                            ticket, history, comments, sessions or touched, and each
                                            --KEY one of its route's parameters (conductor guide
                                            --section operations lists them under observe_WHAT)
  conductor brakeman brief --file F         submit the turn's one brief (brakeman.brief/1) from the
                                            JSON file F, - for stdin; the turn ends once it is kept
With the workbench token (--server, --token and --insecure as conductor dispatch takes them):
  conductor brakeman ask KIND [--subject REF]... [--note-file F] [--since T]
                                            ask Brakeman: KIND is briefing, explain, decision,
                                            draft_tickets, propose_crew, handoff, locate, overlaps or
                                            ask; REF a ref such as run:<run> or ticket:CND-4; F a
                                            Markdown file, - for stdin; T a time (2026-11-02T09:00:00Z,
                                            or 18:00 for the last one), or a duration back (12h)
  conductor brakeman status [--json]        whether Brakeman is on, the turn in progress, the queue and
                                            today's turns
  conductor brakeman list [--json]          the requests, newest first, one a line
  conductor brakeman show br-n [--json]     a request whole, with its brief
  conductor brakeman role on|off|pause|resume|status [--json]
                                            the background role: on wakes Brakeman on events and at
                                            its briefing times, off only when asked; pause holds it,
                                            resume ends the pause; status its state and its last
                                            wakes

A write whose reply is lost to the network is sent once more with the same
operation id, which the server answers without a second change. Outside a
Brakeman turn's session the session commands say so and exit 1.
`

// brakemanNow is the clock --since reads; a test sets it.
var brakemanNow = time.Now

// errNotBrakeman refuses Brakeman's tools and commands in a session that
// is not a Brakeman turn's.
var errNotBrakeman = fmt.Errorf("brakeman: this session is not a Brakeman turn's (%s is not set)", envBrakeman)

// brakemanClient is a Brakeman turn's session client: the session's own
// routes, and the server's root for the reads the turn shares with every
// client, on which it names its session (X-Conductor-Session).
type brakemanClient struct {
	*crewClient
	root, session string
}

// brakemanClientFromEnv is the turn's client: errNotInSession outside a
// session, errNotBrakeman in a session that is no turn.
func brakemanClientFromEnv(getenv func(string) string) (*brakemanClient, error) {
	c, err := crewClientFromEnv(getenv)
	if err != nil {
		return nil, errNotInSession
	}
	if getenv(envBrakeman) == "" {
		return nil, errNotBrakeman
	}
	root, sid, ok := strings.Cut(c.base, "/api/sessions/")
	if !ok || sid == "" || strings.Contains(sid, "/") {
		return nil, fmt.Errorf("brakeman: %s does not name a session's attention route", notify.EnvURL)
	}
	c.prefix = "brakeman"
	return &brakemanClient{crewClient: c, root: root, session: sid}, nil
}

// operation is the table's entry named name.
func operation(name string) (api.Operation, bool) {
	for _, op := range api.Operations() {
		if op.Name == name {
			return op, true
		}
	}
	return api.Operation{}, false
}

// opBound is a bound the table gives an operation, 0 for none.
func opBound(name, bound string) int {
	op, _ := operation(name)
	return op.Bounds[bound]
}

// wholeMiB is the smallest whole number of MiB above n, as doLimit takes it.
func wholeMiB(n int) int { return (n>>20 + 1) << 20 }

// tool runs one of Brakeman's MCP tools; what it returns is the text the
// agent reads.
func (c *brakemanClient) tool(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	switch name {
	case "brakeman_request":
		out, err := c.request(ctx)
		if err != nil {
			return "", err
		}
		return requestText(out, "observe", "brief_submit")
	case "observe":
		var a struct {
			What  string         `json:"what"`
			Query map[string]any `json:"query"`
		}
		if err := strictJSON(raw, &a); err != nil {
			return "", fmt.Errorf("observe: %w", err)
		}
		q := map[string]string{}
		for k, v := range a.Query {
			switch v.(type) {
			case string, float64, bool:
				q[k] = fmt.Sprint(v)
			default:
				return "", fmt.Errorf("observe: %s is a string", k)
			}
		}
		return c.observe(ctx, a.What, q)
	case "brief_submit":
		var a struct {
			OpID  string          `json:"opId"`
			Brief json.RawMessage `json:"brief"`
		}
		if err := strictJSON(raw, &a); err != nil {
			return "", fmt.Errorf("brief_submit: %w", err)
		}
		return c.submit(ctx, a.OpID, a.Brief)
	}
	return "", unknownTool(name)
}

// strictJSON decodes a tool's arguments, refusing a field it does not take.
func strictJSON(raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

// request reads the turn's request, the route's reply as it is.
func (c *brakemanClient) request(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := c.into(ctx, http.MethodGet, "/brakeman/request", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// requestText is the request as the agent reads it: one line that says
// what to do with it, read and submit naming how, then the request.
func requestText(raw json.RawMessage, read, submit string) (string, error) {
	var out struct {
		Request json.RawMessage `json:"request"`
	}
	var head struct{ ID, Kind, State string }
	if err := json.Unmarshal(raw, &out); err != nil || json.Unmarshal(out.Request, &head) != nil {
		return "", errors.New("brakeman: an unreadable request")
	}
	var b bytes.Buffer
	if err := json.Indent(&b, out.Request, "", "  "); err != nil {
		return "", errors.New("brakeman: an unreadable request")
	}
	return fmt.Sprintf("Brakeman request %s (%s), %s. Read what you need with %s, then submit one brief with %s.\n\n%s", head.ID, head.Kind, head.State, read, submit, b.String()), nil
}

// observe reads what, its query's keys the parameters of the route the
// table's observe_<what> names: a path's are put in its path, the rest
// in its query. The reply is the route's as it is, read up to the bound
// the table gives it (the server holds a turn's answer to it); a check's
// log is its text after its SHA-256. A failure to get a reply names the
// host and its cause, never the URL: its query is the turn's.
func (c *brakemanClient) observe(ctx context.Context, what string, query map[string]string) (string, error) {
	text, _, err := c.observeReply(ctx, what, query)
	return text, err
}

// observeReply is observe's text and whether it is JSON: a check's log
// is its bytes as the server gave them, trailing newlines and all, so it
// still matches its SHA-256.
func (c *brakemanClient) observeReply(ctx context.Context, what string, query map[string]string) (string, bool, error) {
	op, ok := operation("observe_" + what)
	if what == "" || !ok {
		return "", false, fmt.Errorf("observe: what is one of %s", strings.Join(observeWhats(), ", "))
	}
	props, _ := op.Input["properties"].(map[string]any)
	path, q := op.Path, url.Values{}
	for _, k := range slices.Sorted(maps.Keys(query)) {
		if _, ok := props[k]; !ok {
			return "", false, fmt.Errorf("observe %s takes %s", what, takes(props))
		}
		if p := "{" + k + "}"; strings.Contains(path, p) {
			path = strings.ReplaceAll(path, p, url.PathEscape(query[k]))
		} else {
			q.Set(k, query[k])
		}
	}
	if m := regexp.MustCompile(`\{([a-z]+)\}`).FindStringSubmatch(path); m != nil {
		return "", false, fmt.Errorf("observe %s needs %s", what, m[1])
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.root+path, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Conductor-Session", c.session)
	resp, err := c.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", false, fmt.Errorf("observe: cannot reach %s: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	limit := maxSessionReply
	if b := op.Bounds["reply"]; b > 0 {
		limit = wholeMiB(b)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return "", false, fmt.Errorf("observe: the reply was cut short: %v", stripURL(err))
	}
	if len(body) > limit {
		return "", false, fmt.Errorf("observe: the server's reply is larger than %d MiB", limit>>20)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error.Code != "" {
			return "", false, fmt.Errorf("observe: %s (%s)", e.Error.Message, e.Error.Code)
		}
		return "", false, fmt.Errorf("observe: the server answered %d", resp.StatusCode)
	}
	if ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); ct == "application/json" {
		return string(bytes.TrimRight(body, "\n")), true, nil
	}
	if sha := resp.Header.Get("X-Conductor-Log-SHA256"); sha != "" {
		return "sha256 " + sha + "\n\n" + string(body), false, nil
	}
	return string(body), false, nil
}

// stripURL is err without the URL a net/http error carries.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// observeWhats is every what observe reads, in the table's order.
func observeWhats() []string {
	var out []string
	for _, op := range api.Operations() {
		if w, ok := strings.CutPrefix(op.Name, "observe_"); ok {
			out = append(out, w)
		}
	}
	return out
}

// observeParams is every parameter an observe_<what> entry of the table
// takes, its path's and its query's, sorted: observe's --KEY flags.
func observeParams() []string {
	seen := map[string]bool{}
	for _, op := range api.Operations() {
		if !strings.HasPrefix(op.Name, "observe_") {
			continue
		}
		props, _ := op.Input["properties"].(map[string]any)
		for k := range props {
			seen[k] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// observeFlags is flags with each of observe's parameters (observeParams)
// that flags do not name already, as a flag taking a value: what
// completion offers after conductor brakeman observe WHAT.
func observeFlags(flags []flagSpec) []flagSpec {
	for _, k := range observeParams() {
		if !slices.ContainsFunc(flags, func(f flagSpec) bool { return f.name == "--"+k }) {
			flags = append(flags, flagSpec{name: "--" + k, kind: flagValue})
		}
	}
	return flags
}

// takes names a route's parameters, or says it takes none.
func takes(props map[string]any) string {
	if len(props) == 0 {
		return "no parameter"
	}
	return strings.Join(slices.Sorted(maps.Keys(props)), ", ")
}

// submit sends the turn's brief under opID, minted when empty, and once
// more with it when the reply is lost (askTwice): the server answers the
// same opId and brief from the brief it kept.
func (c *brakemanClient) submit(ctx context.Context, opID string, brief json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(brief)) == 0 || bytes.TrimSpace(brief)[0] != '{' {
		return "", errors.New("brief_submit needs a brief, a JSON object (brakeman.brief/1)")
	}
	if opID == "" {
		opID = newOpID()
	}
	var out struct {
		Request struct{ ID, State string } `json:"request"`
	}
	body := map[string]any{"opId": opID, "brief": brief}
	if err := askTwice(ctx, func(ctx context.Context) error { return c.into(ctx, http.MethodPost, "/brakeman/brief", body, &out) }); err != nil {
		return "", err
	}
	return fmt.Sprintf("brief kept: request %s is %s; your turn ends now", out.Request.ID, out.Request.State), nil
}

// runBrakeman is conductor brakeman. Exit 2 for a usage error, before any
// request; 1 outside a turn's session for its commands, or when the
// server refuses or cannot be asked.
func runBrakeman(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, brakemanUsage)
		if len(args) == 0 {
			return 2, nil
		}
		return 0, nil
	}
	sub, rest := args[0], args[1:]
	if sub == "observe" {
		return brakemanObserve(ctx, rest, stdout, stderr)
	}
	fs := flag.NewFlagSet("brakeman "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var server apiFlags
	var asJSON *bool
	var file, noteFile, since *string
	var subjects multiFlag
	switch sub {
	case "request":
		asJSON = fs.Bool("json", false, "print the server's reply as it is")
	case "brief":
		file = fs.String("file", "", "the brief, a JSON file (brakeman.brief/1), - for stdin (required)")
	case "ask", "status", "list", "show", "role":
		server = addAPIFlags(fs)
		if sub == "ask" {
			fs.Var(&subjects, "subject", "a subject, a ref such as run:<run> or ticket:CND-4 (repeatable)")
			noteFile = fs.String("note-file", "", "the note, a Markdown file, - for stdin")
			since = fs.String("since", "", "a briefing's start: a time, 18:00 for the last one, or a duration back such as 12h")
		} else {
			asJSON = fs.Bool("json", false, "print the server's reply as it is")
		}
	default:
		fmt.Fprint(stderr, brakemanUsage)
		return 2, fmt.Errorf("brakeman: unknown command %q", sub)
	}
	fs.Usage = func() {
		fmt.Fprint(stderr, brakemanUsage, "\nFlags:\n")
		fs.PrintDefaults()
	}
	// ask's kind, show's id and role's action come first; flags may
	// follow them.
	takesArg := sub == "ask" || sub == "show" || sub == "role"
	var arg string
	if takesArg && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		arg, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	pos := fs.Args()
	if arg == "" && len(pos) > 0 && takesArg {
		arg, pos = pos[0], pos[1:]
	}
	if len(pos) > 0 {
		return 2, fmt.Errorf("brakeman %s takes no more arguments: %q", sub, pos[0])
	}
	switch sub {
	case "request", "brief":
		if sub == "brief" && *file == "" {
			return 2, errors.New("brakeman brief needs --file")
		}
		c, err := brakemanClientFromEnv(os.Getenv)
		if errors.Is(err, errNotInSession) {
			return 1, fmt.Errorf("brakeman: not in a Conductor session (%s and %s are not set)", notify.EnvURL, notify.EnvToken)
		}
		if err != nil {
			return 1, err
		}
		if sub == "request" {
			raw, err := c.request(ctx)
			if err != nil {
				return 1, err
			}
			if *asJSON {
				_, err = stdout.Write(append(bytes.TrimRight(raw, "\n"), '\n'))
				return 0, err
			}
			text, err := requestText(raw, `"conductor brakeman observe WHAT"`, `"conductor brakeman brief --file F"`)
			if err != nil {
				return 1, err
			}
			fmt.Fprintln(stdout, text)
			return 0, nil
		}
		brief, err := readBounded(*file, stdin, opBound("brief_submit", "body"))
		if err != nil {
			return 1, fmt.Errorf("brakeman brief: %w", err)
		}
		text, err := c.submit(ctx, "", brief)
		if err != nil {
			return 1, err
		}
		fmt.Fprintln(stdout, text)
		return 0, nil
	case "ask":
		return brakemanAsk(ctx, server, arg, subjects, *noteFile, *since, stdin, stdout, stderr)
	case "show":
		if !regexp.MustCompile(`^br-[1-9][0-9]{0,17}$`).MatchString(arg) {
			return 2, errors.New("brakeman show needs a request's id, br-n")
		}
	case "role":
		if !slices.Contains(roleActions, arg) {
			return 2, fmt.Errorf("brakeman role needs one of %s", strings.Join(roleActions, ", "))
		}
	}
	client, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	switch sub {
	case "status":
		return brakemanStatus(ctx, client, *asJSON, stdout)
	case "list":
		return brakemanList(ctx, client, *asJSON, stdout)
	case "role":
		return brakemanRole(ctx, client, arg, *asJSON, stdout)
	}
	return brakemanShow(ctx, client, arg, *asJSON, stdout)
}

// readBounded reads the file named, "-" for stdin, of at most limit bytes.
func readBounded(name string, stdin io.Reader, limit int) ([]byte, error) {
	r := stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, fmt.Errorf("%s is larger than %d KiB", name, limit>>10)
	}
	return b, nil
}

// brakemanObserve is conductor brakeman observe WHAT [--KEY VALUE]...: the
// keys are the route's, so they are read as pairs, not as flags of ours.
func brakemanObserve(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stderr, brakemanUsage)
		return 0, nil
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return 2, fmt.Errorf("brakeman observe needs what to read: %s", strings.Join(observeWhats(), ", "))
	}
	what, query := args[0], map[string]string{}
	for i := 1; i < len(args); i++ {
		k, ok := strings.CutPrefix(args[i], "--")
		if !ok || k == "" {
			return 2, fmt.Errorf("brakeman observe takes --KEY VALUE pairs: %q", args[i])
		}
		k, v, eq := strings.Cut(k, "=")
		if !eq {
			if i+1 == len(args) {
				return 2, fmt.Errorf("brakeman observe: --%s needs a value", k)
			}
			i++
			v = args[i]
		}
		if _, dup := query[k]; dup {
			return 2, fmt.Errorf("brakeman observe: --%s given twice", k)
		}
		query[k] = v
	}
	c, err := brakemanClientFromEnv(os.Getenv)
	if errors.Is(err, errNotInSession) {
		return 1, fmt.Errorf("brakeman: not in a Conductor session (%s and %s are not set)", notify.EnvURL, notify.EnvToken)
	}
	if err != nil {
		return 1, err
	}
	text, isJSON, err := c.observeReply(ctx, what, query)
	if err != nil {
		return 1, err
	}
	if isJSON {
		// A JSON answer ends at its value: one line ends it here.
		text += "\n"
	}
	// A log is written as it came, so it still matches its SHA-256.
	_, err = io.WriteString(stdout, text)
	return 0, err
}

// parseSince reads --since: a time (RFC 3339, or a date and a time of
// the day, local), a time of the day alone (its last occurrence, local),
// or a duration back from now.
func parseSince(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("15:04", s, now.Location()); err == nil {
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if at.After(now) {
			at = at.AddDate(0, 0, -1)
		}
		return at, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("--since %q is not a time (2026-11-02T09:00:00Z, 2026-11-02 09:00, 18:00) or a duration back (12h)", s)
}

// brakemanAsk is conductor brakeman ask: the request kept, once, its
// operation id minted here and sent once more on a lost reply.
func brakemanAsk(ctx context.Context, server apiFlags, kind string, subjects []string, noteFile, since string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if !slices.Contains(brakeman.Kinds, kind) {
		return 2, fmt.Errorf("brakeman ask needs a kind: %s", strings.Join(brakeman.Kinds, ", "))
	}
	body := map[string]any{"opId": newOpID(), "kind": kind}
	if len(subjects) > 0 {
		body["subjects"] = subjects
	}
	if since != "" {
		t, err := parseSince(since, brakemanNow())
		if err != nil {
			return 2, err
		}
		body["since"] = t.Format(time.RFC3339)
	}
	if noteFile != "" {
		note, err := readBounded(noteFile, stdin, brakeman.MaxNote)
		if err != nil {
			return 2, fmt.Errorf("--note-file: %w", err)
		}
		body["note"] = string(note)
	}
	client, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	var out struct {
		Request brakeman.Request `json:"request"`
	}
	if err := askTwice(ctx, func(ctx context.Context) error {
		return client.doBody(ctx, http.MethodPost, "/api/brakeman/requests", body, &out)
	}); err != nil {
		return 1, fmt.Errorf("brakeman ask: %w", err)
	}
	fmt.Fprintf(stdout, "%s %s (%s): conductor brakeman show %s once it is briefed\n", out.Request.ID, out.Request.State, out.Request.Kind, out.Request.ID)
	return 0, nil
}

// brakemanStatusReply bounds GET /api/brakeman's reply: the configuration,
// the turn in progress and the queue, each request at its bound.
func brakemanStatusReply() int {
	return wholeMiB(brakeman.MaxConfigDoc + (opBound("brakeman_ask", "queue")+1)*brakeman.MaxRequestDoc + brakeman.MaxRoleDoc)
}

// brakemanStatus is conductor brakeman status.
func brakemanStatus(ctx context.Context, c *apiClient, asJSON bool, stdout io.Writer) (int, error) {
	var raw json.RawMessage
	if err := c.doLimit(ctx, http.MethodGet, "/api/brakeman", nil, &raw, brakemanStatusReply()); err != nil {
		return 1, fmt.Errorf("brakeman status: %w", err)
	}
	if asJSON {
		_, err := stdout.Write(append(raw, '\n'))
		return 0, err
	}
	var st struct {
		Config     brakeman.Config    `json:"config"`
		Current    *brakeman.Request  `json:"current"`
		Queue      []brakeman.Request `json:"queue"`
		TurnsToday int                `json:"turnsToday"`
		Warnings   []string           `json:"warnings"`
		ReadOnly   bool               `json:"readOnly"`
		Why        string             `json:"why"`
		Stuck      bool               `json:"stuck"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return 1, fmt.Errorf("brakeman status: an unreadable reply: %w", err)
	}
	if !st.Config.Enabled {
		fmt.Fprintln(stdout, "Brakeman is off: turn it on in Settings")
	} else {
		model := st.Config.Model
		if model == "" {
			model = "its default"
		}
		fmt.Fprintf(stdout, "Brakeman is on: agent %s, model %s, in %s\n", st.Config.AgentID, model, st.Config.Cwd)
	}
	for _, w := range st.Warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	if st.ReadOnly {
		fmt.Fprintf(stdout, "read-only: %s\n", st.Why)
	}
	if st.Stuck {
		fmt.Fprintln(stdout, "stuck: the last turn's run did not stop: stop it from its run page")
	}
	if st.Current != nil {
		fmt.Fprintf(stdout, "turn: %s\n", requestLine(*st.Current))
	} else {
		fmt.Fprintln(stdout, "turn: none")
	}
	for _, r := range st.Queue {
		fmt.Fprintf(stdout, "queued: %s\n", requestLine(r))
	}
	fmt.Fprintf(stdout, "today: %d of %d turns\n", st.TurnsToday, st.Config.Budget.TurnsPerDay)
	return 0, nil
}

// requestLine is a request on one line: its id, state, kind and time, its
// run and session once it has them, and why it ended.
func requestLine(r brakeman.Request) string {
	line := fmt.Sprintf("%s %s %s %s", r.ID, r.State, r.Kind, r.CreatedAt.Local().Format("2006-01-02 15:04"))
	if r.RunID != "" {
		line += " run " + r.RunID
	}
	if r.SessionID != "" {
		line += " session " + r.SessionID
	}
	if r.Why != "" {
		line += ": " + r.Why
	}
	return line
}

// brakemanListPage is how many requests list asks for a page at a time,
// and its reply's bound each at the request's. A test makes it smaller.
var brakemanListPage = 20

// brakemanList is conductor brakeman list: every page, newest first.
func brakemanList(ctx context.Context, c *apiClient, asJSON bool, stdout io.Writer) (int, error) {
	all := []json.RawMessage{}
	for after := ""; ; {
		path := fmt.Sprintf("/api/brakeman/requests?limit=%d", brakemanListPage)
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		var page struct {
			Requests []json.RawMessage `json:"requests"`
			Next     string            `json:"next"`
		}
		if err := c.doLimit(ctx, http.MethodGet, path, nil, &page, wholeMiB(brakemanListPage*brakeman.MaxRequestDoc)); err != nil {
			return 1, fmt.Errorf("brakeman list: %w", err)
		}
		all = append(all, page.Requests...)
		if page.Next == "" || page.Next == after {
			break
		}
		after = page.Next
	}
	if asJSON {
		b, err := encodeJSON(all)
		if err != nil {
			return 1, err
		}
		_, err = stdout.Write(append(b, '\n'))
		return 0, err
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "no requests")
	}
	for _, raw := range all {
		var r brakeman.Request
		if err := json.Unmarshal(raw, &r); err != nil {
			return 1, fmt.Errorf("brakeman list: an unreadable request: %w", err)
		}
		fmt.Fprintln(stdout, requestLine(r))
	}
	return 0, nil
}

// brakemanShow is conductor brakeman show br-n: the request, then its
// brief's title, reading and items, each whole: the words it leads with,
// then every other field of it in the brief's order.
func brakemanShow(ctx context.Context, c *apiClient, id string, asJSON bool, stdout io.Writer) (int, error) {
	var raw json.RawMessage
	if err := c.doLimit(ctx, http.MethodGet, "/api/brakeman/requests/"+id, nil, &raw, wholeMiB(opBound("brakeman_show", "reply"))); err != nil {
		return 1, fmt.Errorf("brakeman show: %w", err)
	}
	if asJSON {
		_, err := stdout.Write(append(raw, '\n'))
		return 0, err
	}
	var out struct {
		Request brakeman.Request `json:"request"`
		Brief   *struct {
			Title, Summary, Uncertainty string
			Items                       []json.RawMessage
		} `json:"brief"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 1, fmt.Errorf("brakeman show: an unreadable reply: %w", err)
	}
	r := out.Request
	fmt.Fprintln(stdout, requestLine(r))
	if r.Note != "" {
		fmt.Fprintf(stdout, "note: %s\n", r.Note)
	}
	if out.Brief == nil {
		fmt.Fprintln(stdout, "no brief yet")
		return 0, nil
	}
	b := out.Brief
	fmt.Fprintf(stdout, "\n# %s\n\nBrakeman's reading (its interpretation, by %s %s):\n%s\n", b.Title, r.Agent.AgentID, r.Agent.Version, b.Summary)
	if b.Uncertainty != "" {
		fmt.Fprintf(stdout, "\nUncertain: %s\n", b.Uncertainty)
	}
	for i, raw := range b.Items {
		fields, err := itemFields(raw)
		if err != nil {
			return 1, fmt.Errorf("brakeman show: item %d: %w", i, err)
		}
		fmt.Fprintf(stdout, "\n%d. %s", i, itemHead(fields))
		for _, f := range fields {
			if f.key == "kind" || f.lead {
				continue
			}
			text := fieldText(f.value)
			sep := " "
			if strings.HasPrefix(text, "\n") {
				sep = ""
			}
			fmt.Fprintf(stdout, "\n   %s:%s%s", f.key, sep, text)
		}
		fmt.Fprintln(stdout)
	}
	return 0, nil
}

// itemField is one field of a brief's item, in the item's order; lead
// marks the words the item leads with.
type itemField struct {
	key   string
	value json.RawMessage
	lead  bool
}

// itemFields is an item's fields in the order the brief holds them, the
// first of text, question, title and objective that is a string not
// empty marked as its lead.
func itemFields(raw json.RawMessage) ([]itemField, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("an item is not an object")
	}
	var fields []itemField
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		fields = append(fields, itemField{key: t.(string), value: v})
	}
	for _, k := range []string{"text", "question", "title", "objective"} {
		for i, f := range fields {
			var s string
			if f.key == k && json.Unmarshal(f.value, &s) == nil && s != "" {
				fields[i].lead = true
				return fields, nil
			}
		}
	}
	return fields, nil
}

// itemHead is an item's first line: its kind, and the words it leads
// with when it has them.
func itemHead(fields []itemField) string {
	var kind, lead string
	for _, f := range fields {
		switch {
		case f.key == "kind":
			_ = json.Unmarshal(f.value, &kind)
		case f.lead:
			_ = json.Unmarshal(f.value, &lead)
		}
	}
	if lead == "" {
		return kind
	}
	return kind + ": " + lead
}

// fieldText is a field's value as show prints it: a string as it is, a
// list of strings and numbers comma-separated, a list of objects one a
// line, anything else as JSON.
func fieldText(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return strings.ReplaceAll(s, "\n", "\n     ")
	}
	var list []json.RawMessage
	if json.Unmarshal(v, &list) == nil {
		parts := make([]string, len(list))
		plain := true
		for i, x := range list {
			var p any
			_ = json.Unmarshal(x, &p)
			switch p := p.(type) {
			case string:
				parts[i] = p
			case float64, bool:
				parts[i] = string(x)
			default:
				plain = false
				parts[i] = compactJSON(x)
			}
		}
		if plain {
			return strings.Join(parts, ", ")
		}
		return "\n     - " + strings.Join(parts, "\n     - ")
	}
	return compactJSON(v)
}

// compactJSON is v on one line, '<', '>' and '&' as they are.
func compactJSON(v json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, v); err != nil {
		return string(v)
	}
	return b.String()
}

// roleActions are conductor brakeman role's actions.
var roleActions = []string{"on", "off", "pause", "resume", "status"}

// roleStatusRows is how many of the role's last wakes status prints.
const roleStatusRows = 10

// brakemanRole is conductor brakeman role (docs/plans/brakeman.md,
// PR-15): on and off set the wake mode (background or invoked) at the
// configuration's revision, pause and resume the role's own route, and
// status the role with its last wakes; with --json each prints its
// route's reply as it came.
func brakemanRole(ctx context.Context, c *apiClient, action string, asJSON bool, stdout io.Writer) (int, error) {
	switch action {
	case "on", "off":
		var st struct {
			Config brakeman.Config `json:"config"`
		}
		if err := c.doLimit(ctx, http.MethodGet, "/api/brakeman", nil, &st, brakemanStatusReply()); err != nil {
			return 1, fmt.Errorf("brakeman role %s: %w", action, err)
		}
		cfg := st.Config
		cfg.Wake.Mode = brakeman.WakeInvoked
		if action == "on" {
			cfg.Wake.Mode = brakeman.WakeBackground
		}
		var raw json.RawMessage
		if err := c.doLimit(ctx, http.MethodPut, "/api/brakeman/config", map[string]any{"rev": cfg.Rev, "config": cfg}, &raw, wholeMiB(brakeman.MaxConfigDoc)); err != nil {
			return 1, fmt.Errorf("brakeman role %s: %w", action, err)
		}
		if asJSON {
			_, err := stdout.Write(append(raw, '\n'))
			return 0, err
		}
		var out struct {
			Config brakeman.Config `json:"config"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return 1, fmt.Errorf("brakeman role %s: an unreadable reply: %w", action, err)
		}
		switch {
		case action == "off":
			fmt.Fprintln(stdout, "the background role is off: Brakeman runs only when asked")
		case !out.Config.Enabled:
			fmt.Fprintln(stdout, "the background role is on, and Brakeman is off: nothing wakes it until it is turned on in Settings")
		default:
			fmt.Fprintf(stdout, "the background role is on: a window of %d s, a cooldown of %d min, %d turns and %d minutes a day\n",
				out.Config.Wake.WindowSeconds, out.Config.Wake.CooldownMinutes, out.Config.Budget.TurnsPerDay, out.Config.Budget.MinutesPerDay)
		}
		return 0, nil
	case "pause", "resume":
		var raw json.RawMessage
		if err := c.doLimit(ctx, http.MethodPost, "/api/brakeman/role", map[string]any{"action": action}, &raw, wholeMiB(brakeman.MaxRoleDoc)); err != nil {
			return 1, fmt.Errorf("brakeman role %s: %w", action, err)
		}
		if asJSON {
			_, err := stdout.Write(append(raw, '\n'))
			return 0, err
		}
		var out struct {
			Role crew.RoleRecord `json:"role"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return 1, fmt.Errorf("brakeman role %s: an unreadable reply: %w", action, err)
		}
		fmt.Fprintf(stdout, "the background role is %s\n", out.Role.State)
		return 0, nil
	}
	var raw json.RawMessage
	if err := c.doLimit(ctx, http.MethodGet, fmt.Sprintf("/api/brakeman/role?limit=%d", roleStatusRows), nil, &raw, wholeMiB(2*brakeman.MaxRoleDoc)); err != nil {
		return 1, fmt.Errorf("brakeman role status: %w", err)
	}
	if asJSON {
		_, err := stdout.Write(append(raw, '\n'))
		return 0, err
	}
	var out struct {
		Role struct {
			crew.RoleRecord
			MinutesToday int `json:"minutesToday"`
		} `json:"role"`
		Log []crew.RoleLogRow `json:"log"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 1, fmt.Errorf("brakeman role status: an unreadable reply: %w", err)
	}
	r := out.Role
	line := "role: " + r.State
	if r.Why != "" {
		line += ": " + r.Why
	}
	fmt.Fprintln(stdout, line)
	if r.State == crew.RoleUnavailable {
		if r.RetryAt.IsZero() {
			fmt.Fprintln(stdout, "tries again: on a person's request or the next briefing time")
		} else {
			fmt.Fprintf(stdout, "tries again: %s, or on a person's request or the next briefing time\n", r.RetryAt.Local().Format("2006-01-02 15:04"))
		}
	}
	if r.Open != nil {
		fmt.Fprintf(stdout, "gathering: %s, %d wakes, opened %s\n", r.Open.ID, batchCount(*r.Open), r.Open.OpenedAt.Local().Format("15:04:05"))
	}
	if r.Request != "" {
		fmt.Fprintf(stdout, "briefing: %s for %s\n", r.Request, r.Fired.ID)
	}
	if r.State == crew.RoleCooling {
		fmt.Fprintf(stdout, "cooling until %s\n", r.CoolUntil.Local().Format("15:04:05"))
	}
	fmt.Fprintf(stdout, "today: %d minutes\n", r.MinutesToday)
	for _, row := range out.Log {
		l := fmt.Sprintf("wake: %s %s", row.At.Local().Format("2006-01-02 15:04:05"), row.Source)
		if row.Subject != "" {
			l += " " + row.Subject
		}
		switch {
		case row.Held != "":
			l += " (" + row.Held + ")"
		case row.Request != "":
			l += " → " + row.Batch + " → " + row.Request
		case row.Batch != "":
			l += " → " + row.Batch
		}
		fmt.Fprintln(stdout, l)
	}
	return 0, nil
}

// batchCount is how many wakes a batch counts, its overflow too.
func batchCount(b crew.Batch) int {
	n := 0
	for _, w := range b.Wakes {
		n += w.Count
	}
	if b.Overflow != nil {
		n += b.Overflow.Count
	}
	return n
}
