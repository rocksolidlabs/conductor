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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/capacity"
)

// conductor accounts and conductor capacity (docs/plans/capacity.md,
// PR-3): the account references and the capacity view from the shell,
// through the admin's routes, with the server and the token conductor
// dispatch takes. A write reads the accounts' rev first and names it, so
// a write over a change made since is refused, not taken. Everything the
// two commands write, and their error, goes through printedOnly, a line at
// a time through printedLine; a reply's string is put through oneLine
// where it is printed too, so it stays on its line; --json is jsonText's.

const accountsUsage = `Usage:
  conductor accounts list [--json]
      every adapter's default login and default account, then the accounts
      added, one a line: id, adapter, label, folder, and the last reading
  conductor accounts add --agent ADAPTER --label WORDS --folder DIR
      add an account: the folder the agent's own login lives in (claude:
      CLAUDE_CONFIG_DIR, codex: CODEX_HOME); the folder never changes after
  conductor accounts rename ID WORDS
  conductor accounts retire ID
      kept in the accounts, out of every choice and default
  conductor accounts remove ID
      its id is never minted again
  conductor accounts default ADAPTER ID
      the adapter's default account: an account's id, or "default" for
      the agent's own login; the account a capacity view answers when
      none is named (no launch takes an account yet)

Every command takes --server, --token and --insecure as conductor dispatch
does; only the workbench token reaches these routes.
`

const capacityUsage = `Usage:
  conductor capacity [--agent ID] [--account ID] [--model NAME] [--json]
      the capacity view of an agent's catalog entry on an account (the
      agent's default when none is given), or, without --agent, of every
      installed agent on each of its accounts: the plan, each usage window
      the provider reported with its share used and reset ("not reported"
      for one it did not), its credit figures, this server's own counts
      (an estimate), and how fresh the reading is; every other state in
      words. Nothing is read: the view is what the server keeps. The
      listing is read in at most 64 pages, 8 MiB and one minute, or
      refused whole
  conductor capacity --agent ID [--account ID] --read [--check]
      read the agent's usage now through its own tool, at most once in
      five minutes; --check runs a Claude Code version not yet checked
      once (a check of a version that answers with a model turn records it,
      and it is never run again)

Every command takes --server, --token and --insecure as conductor dispatch
does.
`

// maxCapacityPages is how many pages of the listing conductor capacity
// reads, each at most maxListingPage bytes as they arrive (the route's
// page: 8 MiB in all), within listingTimeout. A listing past any of them,
// or whose next names a page already read, is refused whole, never cut.
const maxCapacityPages, maxListingPage = 64, 128 << 10

// listingTimeout is the walk's one deadline, set at its start by listingDeadline (a seam).
var listingTimeout = time.Minute
var listingDeadline = func(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, listingTimeout)
}

// accountsArgs is how many arguments each accounts command takes.
var accountsArgs = map[string]int{"list": 0, "add": 0, "rename": 2, "retire": 1, "remove": 1, "default": 2}

// listedAccounts is GET /api/accounts.
type listedAccounts struct {
	Owner    bool            `json:"owner"`
	Why      string          `json:"why"`
	Rev      int64           `json:"rev"`
	Accounts []listedAccount `json:"accounts"`
	Adapters []listedAdapter `json:"adapters"`
}

type listedAccount struct {
	capacity.Account
	Reading *accountReadingLine `json:"reading"`
}

type listedAdapter struct {
	Adapter string              `json:"adapter"`
	Name    string              `json:"name"`
	Default string              `json:"default"`
	Reading *accountReadingLine `json:"reading"`
}

type accountReadingLine struct {
	Agent  string    `json:"agent"`
	State  string    `json:"state"`
	ReadAt time.Time `json:"readAt"`
}

// accountWrite is the reply of every accounts write but a remove.
type accountWrite struct {
	Account  capacity.Account  `json:"account"`
	Defaults map[string]string `json:"defaults"`
	Rev      int64             `json:"rev"`
}

// capacityPageReply is a page of GET /api/capacity's listing.
type capacityPageReply struct {
	Views []json.RawMessage `json:"views"`
	Next  string            `json:"next"`
}

// capacityRefusal is the error envelope of these routes: the API's, with
// the field a refusal is about and what holds the account.
type capacityRefusal struct {
	Error struct {
		Code    string   `json:"code"`
		Message string   `json:"message"`
		Field   string   `json:"field"`
		Names   []string `json:"names"`
	} `json:"error"`
}

func (r *accountReadingLine) text() string {
	if r == nil {
		return "never read"
	}
	return fmt.Sprintf("%s, read %s by %s", oneLine(r.State), when(r.ReadAt), oneLine(r.Agent))
}

// refusalOf is err for a terminal: a refusal in the server's own words,
// its field and what holds the account named, every one of them through
// oneLine; any other error's text through oneLine too.
func refusalOf(err error) error {
	var rr replyRefusal
	var env capacityRefusal
	if !errors.As(err, &rr) || json.Unmarshal(rr.body, &env) != nil || env.Error.Code == "" {
		return errors.New(oneLine(err.Error()))
	}
	e := env.Error
	text := oneLine(e.Code)
	if e.Message != "" {
		text += ": " + oneLine(e.Message)
	}
	if e.Field != "" {
		text += " (the field " + oneLine(e.Field) + ")"
	}
	if len(e.Names) > 0 {
		text += "; held by " + oneLine(strings.Join(e.Names, ", "))
	}
	return errors.New(text)
}

// printed reports whether the two commands write r as it is: a letter, a
// mark, a number, punctuation or a symbol, by its category, or U+0020;
// printedLine is oneLine(s) with every other character U+FFFD. The
// commands' text is plain on purpose: a joiner in it is U+FFFD as well.
func printed(r rune) bool {
	return r == ' ' || unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S)
}
func printedLine(s string) string {
	return strings.Map(func(r rune) rune {
		if !printed(r) {
			return utf8.RuneError
		}
		return r
	}, oneLine(s))
}

// jsonText is the one way the package prints JSON: raw indented, or
// compact as the server's own replies are, with every character that is
// not printed and every byte that is not UTF-8 written as its JSON escape,
// so the text holds the same values and printed characters and the
// indent's newlines alone. A JSON text holds such characters only inside
// its strings, where the escape means the same.
func jsonText(raw []byte, indent bool) (string, error) {
	var buf bytes.Buffer
	raw = bytes.TrimSpace(raw)
	err := json.Compact(&buf, raw)
	if indent && err == nil {
		buf.Reset()
		err = json.Indent(&buf, raw, "", "  ")
	}
	if err != nil {
		return "", errors.New("the server's reply is not JSON")
	}
	var b strings.Builder
	for s := buf.Bytes(); len(s) > 0; {
		r, n := utf8.DecodeRune(s)
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\u%04x`, utf8.RuneError)
		case r != '\n' && !printed(r):
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%04x`, u)
			}
		default:
			b.Write(s[:n])
		}
		s = s[n:]
	}
	return b.String(), nil
}

// printReply prints raw as jsonText gives it, a line, answering the
// command's exit.
func printReply(w io.Writer, raw []byte, indent bool) (int, error) {
	text, err := jsonText(raw, indent)
	if err != nil {
		return 1, err
	}
	_, err = fmt.Fprintln(w, text)
	return 0, err
}

// termWriter writes to w a line at a time, each through printedLine: what
// the two commands write reaches the terminal as printed characters and
// newlines alone.
type termWriter struct {
	w    io.Writer
	part []byte
}

func (t *termWriter) Write(p []byte) (int, error) {
	t.part = append(t.part, p...)
	for {
		i := bytes.IndexByte(t.part, '\n')
		if i < 0 {
			return len(p), nil
		}
		if _, err := io.WriteString(t.w, printedLine(string(t.part[:i]))+"\n"); err != nil {
			return 0, err
		}
		t.part = t.part[i+1:]
	}
}

// printedOnly runs a command with its two streams behind termWriters and
// answers its exit, its error's text through printedLine: the flag
// parser's words, a usage error, the client's set-up and every reply all
// reach the terminal one way.
func printedOnly(stdout, stderr io.Writer, command func(stdout, stderr io.Writer) (int, error)) (int, error) {
	out, errOut := &termWriter{w: stdout}, &termWriter{w: stderr}
	code, err := command(out, errOut)
	for _, t := range []*termWriter{out, errOut} {
		io.WriteString(t.w, printedLine(string(t.part)))
	}
	if err != nil {
		err = errors.New(printedLine(err.Error()))
	}
	return code, err
}

// runAccounts is conductor accounts, through printedOnly.
func runAccounts(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	return printedOnly(stdout, stderr, func(out, errOut io.Writer) (int, error) { return accountsCmd(ctx, args, out, errOut) })
}

// runCapacity is conductor capacity, through printedOnly.
func runCapacity(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	return printedOnly(stdout, stderr, func(out, errOut io.Writer) (int, error) { return capacityCmd(ctx, args, out, errOut) })
}

// accountsCmd is conductor accounts. Exit 2 for a usage error, before any
// request; 1 when the server refuses or cannot be asked.
func accountsCmd(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, accountsUsage)
		if len(args) == 0 {
			return 2, nil
		}
		return 0, nil
	}
	sub, rest := args[0], args[1:]
	takes, known := accountsArgs[sub]
	if !known {
		fmt.Fprint(stderr, accountsUsage)
		return 2, fmt.Errorf("accounts: unknown command %q", sub)
	}
	fs := flag.NewFlagSet("accounts "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := addAPIFlags(fs)
	var asJSON *bool
	var adapter, label, folder *string
	switch sub {
	case "list":
		asJSON = fs.Bool("json", false, "print the server's reply")
	case "add":
		adapter = fs.String("agent", "", "the adapter: claude or codex")
		label = fs.String("label", "", "the account's label, 1 to 60 characters")
		folder = fs.String("folder", "", "the folder the agent's own login lives in, absolute")
	}
	fs.Usage = func() {
		fmt.Fprint(stderr, accountsUsage, "\nFlags of accounts "+sub+":\n")
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	if sub == "rename" && len(pos) > 2 {
		pos = []string{pos[0], strings.Join(pos[1:], " ")}
	}
	if len(pos) != takes {
		return 2, fmt.Errorf("accounts %s takes %d argument(s), not %d", sub, takes, len(pos))
	}
	if sub == "add" && (*adapter == "" || *label == "" || *folder == "") {
		return 2, errors.New("accounts add takes --agent, --label and --folder")
	}
	c, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	var raw json.RawMessage
	if err := c.doBody(ctx, http.MethodGet, "/api/accounts", nil, &raw); err != nil {
		return 1, refusalOf(err)
	}
	var doc listedAccounts
	if err := json.Unmarshal(raw, &doc); err != nil {
		return 1, errors.New("the server's accounts could not be read")
	}
	if sub == "list" {
		if *asJSON {
			return printReply(stdout, raw, true)
		}
		writeAccounts(stdout, doc)
		return 0, nil
	}
	rev := doc.Rev
	var out accountWrite
	switch sub {
	case "add":
		err = c.doBody(ctx, http.MethodPost, "/api/accounts", map[string]any{"adapter": *adapter, "label": *label, "folder": *folder, "expectedRev": rev}, &out)
	case "rename":
		err = c.doBody(ctx, http.MethodPatch, "/api/accounts/"+url.PathEscape(pos[0]), map[string]any{"label": pos[1], "expectedRev": rev}, &out)
	case "retire":
		err = c.doBody(ctx, http.MethodPost, "/api/accounts/"+url.PathEscape(pos[0])+"/retire", map[string]any{"expectedRev": rev}, &out)
	case "remove":
		err = c.doBody(ctx, http.MethodDelete, "/api/accounts/"+url.PathEscape(pos[0])+"?expectedRev="+strconv.FormatInt(rev, 10), nil, nil)
	case "default":
		err = c.doBody(ctx, http.MethodPut, "/api/accounts/defaults/"+url.PathEscape(pos[0]), map[string]any{"account": pos[1], "expectedRev": rev}, &out)
	}
	if err != nil {
		return 1, refusalOf(err)
	}
	a := out.Account
	switch sub {
	case "add":
		fmt.Fprintf(stdout, "added %s (%s, %s)\n", oneLine(a.ID), oneLine(a.Adapter), oneLine(a.Label))
	case "rename":
		fmt.Fprintf(stdout, "renamed %s to %s\n", oneLine(a.ID), oneLine(a.Label))
	case "retire":
		fmt.Fprintf(stdout, "retired %s\n", oneLine(a.ID))
	case "remove":
		fmt.Fprintf(stdout, "removed %s\n", oneLine(pos[0]))
	case "default":
		fmt.Fprintf(stdout, "the default of %s is %s\n", oneLine(pos[0]), oneLine(cmp.Or(out.Defaults[pos[0]], capacity.DefaultID)))
	}
	return 0, nil
}

// writeAccounts prints GET /api/accounts: whether this server changes the
// accounts, each adapter's default, then each account, a line each.
func writeAccounts(w io.Writer, doc listedAccounts) {
	if !doc.Owner {
		fmt.Fprintf(w, "this server does not own capacity/, so it changes no account: %s\n", oneLine(doc.Why))
	}
	for _, a := range doc.Adapters {
		fmt.Fprintf(w, "%s (%s): default %s; its own login: %s\n", oneLine(a.Name), oneLine(a.Adapter), oneLine(a.Default), a.Reading.text())
	}
	for _, a := range doc.Accounts {
		retired := ""
		if a.Retired {
			retired = " (retired)"
		}
		fmt.Fprintf(w, "%s  %s  %s%s  %s  %s\n", oneLine(a.ID), oneLine(a.Adapter), oneLine(a.Label), retired, oneLine(a.Folder), a.Reading.text())
	}
}

// capacityCmd is conductor capacity.
func capacityCmd(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("capacity", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := addAPIFlags(fs)
	agent := fs.String("agent", "", "the catalog agent's id")
	account := fs.String("account", "", "the account's id (default: the agent's default account)")
	model := fs.String("model", "", "the model a launch would ask for")
	read := fs.Bool("read", false, "read the agent's usage now (with --agent)")
	check := fs.Bool("check", false, "with --read: run a Claude Code version not yet checked, once")
	asJSON := fs.Bool("json", false, "print the server's reply")
	fs.Usage = func() {
		fmt.Fprint(stderr, capacityUsage, "\nFlags:\n")
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	switch {
	case len(pos) != 0:
		return 2, fmt.Errorf("capacity takes no argument: %q", pos[0])
	case (*read || *account != "" || *model != "") && *agent == "":
		return 2, errors.New("capacity: --read, --account and --model name one agent's view: give --agent")
	case *check && !*read:
		return 2, errors.New("capacity: --check goes with --read")
	case *read && *model != "":
		return 2, errors.New("capacity: a read is of an account, not a model: --model goes without --read")
	}
	c, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	views := []json.RawMessage{}
	switch {
	case *read:
		body := map[string]any{"agent": *agent}
		if *account != "" {
			body["account"] = *account
		}
		if *check {
			body["check"] = true
		}
		var v json.RawMessage
		if err := c.doBody(ctx, http.MethodPost, "/api/capacity/read", body, &v); err != nil {
			return 1, refusalOf(err)
		}
		views = append(views, v)
	case *agent != "":
		q := url.Values{"agent": {*agent}}
		if *account != "" {
			q.Set("account", *account)
		}
		if *model != "" {
			q.Set("model", *model)
		}
		var v json.RawMessage
		if err := c.doBody(ctx, http.MethodGet, "/api/capacity?"+q.Encode(), nil, &v); err != nil {
			return 1, refusalOf(err)
		}
		views = append(views, v)
	default:
		// The listing, page by page, until a page names no next, within
		// its bounds and its one deadline.
		lctx, cancel := listingDeadline(ctx)
		defer cancel()
		after, followed := "", map[string]bool{}
		for pages := 0; ; pages++ {
			if pages == maxCapacityPages {
				return 1, fmt.Errorf("capacity: the listing is longer than %d pages: ask for one agent with --agent", maxCapacityPages)
			}
			path := "/api/capacity"
			if after != "" {
				path += "?" + url.Values{"after": {after}}.Encode()
			}
			var raw json.RawMessage
			if err := c.doLimit(lctx, http.MethodGet, path, nil, &raw, maxListingPage); err != nil {
				if lctx.Err() != nil && ctx.Err() == nil {
					return 1, fmt.Errorf("capacity: the listing takes longer than %s: ask for one agent with --agent", listingTimeout)
				}
				return 1, refusalOf(err)
			}
			var page capacityPageReply
			if err := json.Unmarshal(raw, &page); err != nil {
				return 1, errors.New("capacity: the server's listing could not be read")
			}
			views = append(views, page.Views...)
			if page.Next == "" {
				break
			}
			if followed[page.Next] {
				return 1, errors.New("capacity: the server's listing does not move on: a page names as its next one already read")
			}
			followed[page.Next], after = true, page.Next
		}
	}
	if *asJSON {
		if *agent != "" {
			return printReply(stdout, views[0], true)
		}
		return printJSON(stdout, map[string]any{"views": views})
	}
	for i, raw := range views {
		var v capacity.View
		if err := json.Unmarshal(raw, &v); err != nil {
			return 1, errors.New("capacity: the server's view could not be read")
		}
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		writeView(stdout, v)
	}
	if len(views) == 0 {
		fmt.Fprintln(stdout, "no agent this server reads is installed")
	}
	return 0, nil
}

// expectedWindows are the windows each reader's provider may report, by
// kind, so a view says "not reported" for one it did not.
var expectedWindows = map[string][][2]string{
	"claude": {{"session", "5-hour"}, {"weekly_all", "Weekly, all models"}},
	"codex":  {{"primary", "Primary"}, {"secondary", "Secondary"}},
}

// writeView prints a view in words: every state says what it is, a
// window not reported is "not reported", never 0%, and this server's
// counts are marked as its estimate. A state, a host or a freshness this
// conductor does not know is said to be one, with its name.
func writeView(w io.Writer, v capacity.View) {
	acct := "the agent's own login (default)"
	if v.Account.ID != capacity.DefaultID {
		acct = oneLine(v.Account.Label) + " (" + oneLine(v.Account.ID) + ")"
	}
	if v.Account.Retired {
		acct += ", retired"
	}
	if v.Account.Default && v.Account.ID != capacity.DefaultID {
		acct += ", the default"
	}
	fmt.Fprintf(w, "%s (%s) on %s\n", oneLine(cmp.Or(v.Agent.Name, v.Agent.ID)), oneLine(v.Agent.ID), acct)
	fmt.Fprintf(w, "  model: %s\n", oneLine(cmp.Or(v.Model.Words, v.Model.Chosen, "the agent's default")))
	switch {
	case v.Host.Kind == capacity.HostNative:
		fmt.Fprintf(w, "  runs on this server (%s)\n", oneLine(v.Host.Name))
	case v.Host.Words != "":
		fmt.Fprintf(w, "  %s\n", oneLine(v.Host.Words))
	default:
		fmt.Fprintf(w, "  runs where this conductor does not know (%s)\n", oneLine(v.Host.Kind))
	}
	words := cmp.Or(v.Words, capacity.StateWords(v.State), "a state this conductor does not know")
	fmt.Fprintf(w, "  %s: %s\n", oneLine(v.State), oneLine(words))
	if v.State == capacity.StateOK {
		if v.Account.Plan != "" {
			fmt.Fprintf(w, "  plan: %s (as the tool reports it)\n", oneLine(v.Account.Plan))
		}
		seen := map[string]bool{}
		for _, win := range v.Windows {
			seen[win.Kind] = true
			line := fmt.Sprintf("  %s: %d%% used", oneLine(cmp.Or(win.Name, win.Kind)), win.Percent)
			if win.ResetsAt != nil {
				line += ", resets " + when(*win.ResetsAt)
			} else {
				line += ", reset not reported"
			}
			if win.Severity != "" && win.Severity != "normal" {
				line += " (" + oneLine(win.Severity) + ")"
			}
			fmt.Fprintln(w, line)
		}
		for _, e := range expectedWindows[v.Agent.Adapter] {
			if !seen[e[0]] {
				fmt.Fprintf(w, "  %s: not reported\n", e[1])
			}
		}
		if len(v.Windows) == 0 && len(expectedWindows[v.Agent.Adapter]) == 0 {
			fmt.Fprintln(w, "  usage windows: not reported")
		}
		for _, c := range v.Credits {
			fmt.Fprintf(w, "  %s (the provider's): %s\n", oneLine(c.Kind), creditText(c))
		}
		if l := v.Limits; l.Reached != "" || l.SpendControl != nil || l.OrdinaryAllowed != nil {
			fmt.Fprintf(w, "  the provider's limits: reached %s, spend control %s, ordinary use allowed %s\n", oneLine(cmp.Or(l.Reached, "not reported")), boolText(l.SpendControl), boolText(l.OrdinaryAllowed))
		}
	}
	since := ""
	if v.Counts.Since != nil {
		since = " since " + when(*v.Counts.Since)
	}
	fmt.Fprintf(w, "  this server's count (an estimate): %d running, %d started in 5 hours, %d in 7 days%s\n", v.Counts.Running, v.Counts.Started5h, v.Counts.Started7d, since)
	switch {
	case v.ReadAt != nil && v.ReadBy != nil:
		fmt.Fprintf(w, "  read %s by %s %s (checked on %s); %s\n", when(*v.ReadAt), oneLine(v.ReadBy.Program), oneLine(v.ReadBy.Version), oneLine(v.ReadBy.Qualified), freshText(v.Fresh))
	case v.ReadAt != nil:
		fmt.Fprintf(w, "  read %s; %s\n", when(*v.ReadAt), freshText(v.Fresh))
	}
	if v.NextRead != nil {
		fmt.Fprintf(w, "  next read after %s\n", when(*v.NextRead))
	}
}

func freshText(f string) string {
	switch f {
	case capacity.FreshNow:
		return "fresh"
	case capacity.FreshStale:
		return "stale"
	case capacity.FreshResetPassed:
		return "a reset has passed since this reading"
	case capacity.FreshNone, "":
		return "nothing read"
	}
	return "a freshness this conductor does not know (" + oneLine(f) + ")"
}

func creditText(c capacity.Credit) string {
	var parts []string
	if c.Enabled != nil {
		parts = append(parts, "enabled "+boolText(c.Enabled))
	}
	for _, f := range []struct {
		name string
		v    *string
	}{{"used", c.Used}, {"limit", c.Limit}, {"balance", c.Balance}} {
		if f.v != nil {
			parts = append(parts, f.name+" "+oneLine(*f.v))
		}
	}
	if c.Unlimited {
		parts = append(parts, "unlimited")
	}
	if c.Currency != "" {
		parts = append(parts, oneLine(c.Currency))
	}
	if len(parts) == 0 {
		return "not reported"
	}
	return strings.Join(parts, ", ")
}

func boolText(b *bool) string {
	switch {
	case b == nil:
		return "not reported"
	case *b:
		return "yes"
	}
	return "no"
}
