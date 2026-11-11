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
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/capacity"
)

// conductor accounts and conductor capacity (docs/plans/capacity.md,
// PR-3): the account references and the capacity view from the shell,
// through the admin's routes, with the server and the token conductor
// dispatch takes. A write reads the accounts' rev first and names it, so
// a write over a change made since is refused, not taken.

const accountsUsage = `Usage:
  conductor accounts list [--json]
      every adapter's default login and default account, then the accounts
      added, one a line: id, adapter, label, folder, and the last reading
  conductor accounts add --agent ADAPTER --label WORDS --folder DIR
      add an account: the folder the agent's own login lives in (claude:
      CLAUDE_CONFIG_DIR, codex: CODEX_HOME); the folder never changes after
  conductor accounts rename ID WORDS
  conductor accounts retire ID
      kept for what is bound to it, out of every choice and default
  conductor accounts remove ID
      refused while anything is bound to it
  conductor accounts default ADAPTER ID
      the adapter's default account: an account's id, or "default" for
      the agent's own login

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
      words. Nothing is read: the view is what the server keeps.
  conductor capacity --agent ID [--account ID] --read [--check]
      read the agent's usage now through its own tool, at most once in
      five minutes; --check runs a Claude Code version not yet checked
      once (a check of a version that answers with a model turn records it,
      and it is never run again)

Every command takes --server, --token and --insecure as conductor dispatch
does.
`

// maxCapacityCLIReply bounds what the commands read of a reply: a whole
// MiB above the routes' own bounds (a page of views, the accounts).
const maxCapacityCLIReply = 1 << 20

// accountsArgs is how many arguments each accounts command takes.
var accountsArgs = map[string]int{"list": 0, "add": 0, "rename": 2, "retire": 1, "remove": 1, "default": 2}

// listedAccounts is GET /api/accounts.
type listedAccounts struct {
	Owner    bool   `json:"owner"`
	Why      string `json:"why"`
	Rev      int64  `json:"rev"`
	Accounts []struct {
		capacity.Account
		Reading *accountReadingLine `json:"reading"`
	} `json:"accounts"`
	Adapters []struct {
		Adapter string              `json:"adapter"`
		Name    string              `json:"name"`
		Default string              `json:"default"`
		Reading *accountReadingLine `json:"reading"`
	} `json:"adapters"`
}

type accountReadingLine struct {
	Agent  string    `json:"agent"`
	State  string    `json:"state"`
	ReadAt time.Time `json:"readAt"`
}

func (r *accountReadingLine) text() string {
	if r == nil {
		return "never read"
	}
	return fmt.Sprintf("%s, read %s by %s", r.State, when(r.ReadAt), r.Agent)
}

// runAccounts is conductor accounts. Exit 2 for a usage error, before any
// request; 1 when the server refuses or cannot be asked.
func runAccounts(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
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
	var doc listedAccounts
	if err := c.doLimit(ctx, http.MethodGet, "/api/accounts", nil, &doc, maxCapacityCLIReply); err != nil {
		return 1, err
	}
	rev := doc.Rev
	var out struct {
		Account  capacity.Account  `json:"account"`
		Defaults map[string]string `json:"defaults"`
	}
	switch sub {
	case "list":
		if *asJSON {
			b, _ := json.MarshalIndent(doc, "", "  ")
			fmt.Fprintln(stdout, string(b))
			return 0, nil
		}
		if !doc.Owner {
			fmt.Fprintf(stdout, "this server does not own capacity/, so it changes no account: %s\n", doc.Why)
		}
		for _, a := range doc.Adapters {
			fmt.Fprintf(stdout, "%s (%s): default %s; its own login: %s\n", a.Name, a.Adapter, a.Default, a.Reading.text())
		}
		for _, a := range doc.Accounts {
			retired := ""
			if a.Retired {
				retired = " (retired)"
			}
			fmt.Fprintf(stdout, "%s  %s  %s%s  %s  %s\n", a.ID, a.Adapter, oneLine(a.Label), retired, a.Folder, a.Reading.text())
		}
		return 0, nil
	case "add":
		err = c.doLimit(ctx, http.MethodPost, "/api/accounts", map[string]any{"adapter": *adapter, "label": *label, "folder": *folder, "expectedRev": rev}, &out, maxCapacityCLIReply)
	case "rename":
		err = c.doLimit(ctx, http.MethodPatch, "/api/accounts/"+url.PathEscape(pos[0]), map[string]any{"label": pos[1], "expectedRev": rev}, &out, maxCapacityCLIReply)
	case "retire":
		err = c.doLimit(ctx, http.MethodPost, "/api/accounts/"+url.PathEscape(pos[0])+"/retire", map[string]any{"expectedRev": rev}, &out, maxCapacityCLIReply)
	case "remove":
		err = c.doLimit(ctx, http.MethodDelete, "/api/accounts/"+url.PathEscape(pos[0])+"?expectedRev="+strconv.FormatInt(rev, 10), nil, nil, maxCapacityCLIReply)
	case "default":
		err = c.doLimit(ctx, http.MethodPut, "/api/accounts/defaults/"+url.PathEscape(pos[0]), map[string]any{"account": pos[1], "expectedRev": rev}, &out, maxCapacityCLIReply)
	}
	if err != nil {
		return 1, err
	}
	switch sub {
	case "add":
		fmt.Fprintf(stdout, "added %s (%s, %s)\n", out.Account.ID, out.Account.Adapter, oneLine(out.Account.Label))
	case "rename":
		fmt.Fprintf(stdout, "renamed %s to %s\n", out.Account.ID, oneLine(out.Account.Label))
	case "retire":
		fmt.Fprintf(stdout, "retired %s\n", out.Account.ID)
	case "remove":
		fmt.Fprintf(stdout, "removed %s\n", pos[0])
	case "default":
		fmt.Fprintf(stdout, "the default of %s is %s\n", pos[0], pos[1])
	}
	return 0, nil
}

// runCapacity is conductor capacity.
func runCapacity(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
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
	var views []json.RawMessage
	switch {
	case *read:
		var v json.RawMessage
		if err := c.doLimit(ctx, http.MethodPost, "/api/capacity/read", map[string]any{"agent": *agent, "account": *account, "check": *check}, &v, maxCapacityCLIReply); err != nil {
			return 1, err
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
		if err := c.doLimit(ctx, http.MethodGet, "/api/capacity?"+q.Encode(), nil, &v, maxCapacityCLIReply); err != nil {
			return 1, err
		}
		views = append(views, v)
	default:
		after := ""
		for {
			var page struct {
				Views []json.RawMessage `json:"views"`
				Next  string            `json:"next"`
			}
			if err := c.doLimit(ctx, http.MethodGet, "/api/capacity?after="+url.QueryEscape(after), nil, &page, maxCapacityCLIReply); err != nil {
				return 1, err
			}
			views = append(views, page.Views...)
			if page.Next == "" || page.Next <= after {
				break
			}
			after = page.Next
		}
	}
	if *asJSON {
		if *agent != "" {
			fmt.Fprintln(stdout, string(views[0]))
			return 0, nil
		}
		b, _ := json.Marshal(map[string]any{"views": views})
		fmt.Fprintln(stdout, string(b))
		return 0, nil
	}
	for i, raw := range views {
		var v capacity.View
		if err := json.Unmarshal(raw, &v); err != nil {
			return 1, fmt.Errorf("capacity: the server's view could not be read: %w", err)
		}
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		writeView(stdout, v)
	}
	return 0, nil
}

// expectedWindows are the windows each reader's provider may report, by
// kind, so a view says "not reported" for one it did not.
var expectedWindows = map[string][][2]string{
	"claude": {{"session", "5-hour"}, {"weekly_all", "Weekly, all models"}},
	"codex":  {{"primary", "Primary window"}, {"secondary", "Secondary window"}},
}

// writeView prints a view in words: every state says what it is, a
// window not reported is "not reported", never 0%, and this server's
// counts are marked as its estimate.
func writeView(w io.Writer, v capacity.View) {
	acct := "the agent's own login (default)"
	if v.Account.ID != capacity.DefaultID {
		acct = oneLine(v.Account.Label) + " (" + v.Account.ID + ")"
	}
	if v.Account.Retired {
		acct += ", retired"
	}
	if v.Account.Default && v.Account.ID != capacity.DefaultID {
		acct += ", the default"
	}
	name := v.Agent.Name
	if name == "" {
		name = v.Agent.ID
	}
	fmt.Fprintf(w, "%s (%s) on %s\n", oneLine(name), v.Agent.ID, acct)
	model := v.Model.Words
	if model == "" {
		model = "the agent's default"
	}
	fmt.Fprintf(w, "  model: %s\n", oneLine(model))
	switch v.Host.Kind {
	case capacity.HostNative:
		fmt.Fprintf(w, "  runs on this server (%s)\n", oneLine(v.Host.Name))
	default:
		fmt.Fprintf(w, "  %s\n", oneLine(v.Host.Words))
	}
	words := v.Words
	if words == "" {
		words = capacity.StateWords(v.State)
	}
	fmt.Fprintf(w, "  %s: %s\n", v.State, oneLine(words))
	if v.State == capacity.StateOK {
		if v.Account.Plan != "" {
			fmt.Fprintf(w, "  plan: %s (as the tool reports it)\n", oneLine(v.Account.Plan))
		}
		seen := map[string]bool{}
		for _, win := range v.Windows {
			seen[win.Kind] = true
			line := fmt.Sprintf("  %s: %d%% used", oneLine(win.Name), win.Percent)
			if win.ResetsAt != nil {
				line += ", resets " + when(*win.ResetsAt)
			} else {
				line += ", reset not reported"
			}
			if win.Severity != "" && win.Severity != "normal" {
				line += " (" + win.Severity + ")"
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
			fmt.Fprintf(w, "  %s (the provider's): %s\n", c.Kind, creditText(c))
		}
		if l := v.Limits; l.Reached != "" || l.SpendControl != nil || l.OrdinaryAllowed != nil {
			fmt.Fprintf(w, "  the provider's limits: reached %s, spend control %s, ordinary use allowed %s\n", orNotReported(l.Reached), boolText(l.SpendControl), boolText(l.OrdinaryAllowed))
		}
	}
	since := ""
	if v.Counts.Since != nil {
		since = " since " + when(*v.Counts.Since)
	}
	fmt.Fprintf(w, "  this server's count (an estimate): %d running, %d started in 5 hours, %d in 7 days%s\n", v.Counts.Running, v.Counts.Started5h, v.Counts.Started7d, since)
	switch {
	case v.ReadAt != nil && v.ReadBy != nil:
		fmt.Fprintf(w, "  read %s by %s %s (checked on %s); %s\n", when(*v.ReadAt), v.ReadBy.Program, v.ReadBy.Version, v.ReadBy.Qualified, freshText(v.Fresh))
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
	}
	return "nothing read"
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
			parts = append(parts, f.name+" "+*f.v)
		}
	}
	if c.Unlimited {
		parts = append(parts, "unlimited")
	}
	if c.Currency != "" {
		parts = append(parts, c.Currency)
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

func orNotReported(s string) string {
	if s == "" {
		return "not reported"
	}
	return s
}
