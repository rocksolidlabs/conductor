package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/notify"
)

const taskUsage = `Usage:
  conductor task context [--page N] [--json]   print this attempt's task (.conductor/task.md), or its JSON
  conductor task result [--summary TEXT] [--commit SHA] [--issue TEXT]...
                        [--verdict approved|needs_changes] [--finding "SEVERITY[:PATH[:LINE]] MESSAGE"]...
                        [--file result.json|-]  submit this attempt's result, once
  conductor task send --to MEMBER TEXT...     send another member a message (typed into its session once it has its task)
  conductor task inbox [--after N] [--json]    your messages from the other members, after a sequence number
  conductor task ack N                         acknowledge your messages up to N
  conductor task blocked --reason TEXT [--message TEXT]
                                               say what stops your attempt; the people watching see it and are asked
  conductor task checkpoint --done TEXT --remaining TEXT [--notes TEXT]
                                               save where you got to, tied to your worktree's commit (commit first)
  conductor task ticket [--json]               print the Dispatch ticket your run is linked to, or its JSON
  conductor task ticket comment TEXT...        comment on that ticket, as your run's member

In a managed run, the session is one attempt at one task (CONDUCTOR_TASK).
A work task's result is its commit (HEAD unless --commit names it): commit
first, the worktree must be clean apart from .conductor/; say what you did
with --summary and what is left with --issue. A review's result is its
verdict with --finding (severity blocker, major, minor or nit); it commits
nothing. --file reads the result as JSON instead: {"summary", "commit",
"issues": [], "verdict", "findings": [{"severity", "path", "line",
"message"}]}; "-" reads stdin. A refused result says what to fix; the same
commit submitted twice is taken once. Outside a Conductor session, or in a
session that is no attempt, both commands say so and exit 1.
`

// maxResultFile bounds what --file reads.
const maxResultFile = 1 << 20

// multiFlag collects a flag given more than once.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ", ") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// runTask is conductor task: context reads the attempt's task, result
// submits its result. Exit 2 for a usage error, 1 outside a session or
// when the server refuses.
func runTask(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stderr, taskUsage)
		if len(args) == 0 {
			return 2, nil
		}
		return 0, nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "context", "result", "send", "inbox", "ack", "blocked", "checkpoint", "ticket":
	default:
		fmt.Fprint(stderr, taskUsage)
		return 2, fmt.Errorf("task: unknown command %q", sub)
	}
	fs := flag.NewFlagSet("task "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	page := fs.Int("page", 0, "context: the page to print (all unless given)")
	asJSON := fs.Bool("json", false, "context, inbox, ticket: print the task (task.json), the messages or the ticket as JSON")
	summary := fs.String("summary", "", "result: what you did")
	commit := fs.String("commit", "", "result: the result commit (HEAD unless given)")
	verdict := fs.String("verdict", "", "result: a review's verdict, approved or needs_changes")
	file := fs.String("file", "", "result: read the result as JSON from this file, or - for stdin")
	var issues, findings multiFlag
	fs.Var(&issues, "issue", "result: an issue left or doubtful (repeatable)")
	fs.Var(&findings, "finding", `result: a review's finding, "SEVERITY[:PATH[:LINE]] MESSAGE" (repeatable)`)
	to := fs.String("to", "", "send: the member the message goes to")
	after := fs.Int64("after", 0, "inbox: the last sequence number read")
	reason := fs.String("reason", "", "blocked: what stops you, one line")
	message := fs.String("message", "", "blocked: what you need from the people watching")
	done := fs.String("done", "", "checkpoint: what is done")
	remaining := fs.String("remaining", "", "checkpoint: what remains")
	notes := fs.String("notes", "", "checkpoint: anything the next attempt should know")
	fs.Usage = func() {
		fmt.Fprint(stderr, taskUsage, "\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	switch sub {
	case "send", "ack":
		if fs.NArg() == 0 {
			return 2, fmt.Errorf("task %s needs %s", sub, map[string]string{"send": "the text", "ack": "a sequence number"}[sub])
		}
	case "ticket":
		switch {
		case fs.NArg() == 0:
		case fs.Arg(0) != "comment":
			return 2, fmt.Errorf("task ticket takes nothing, or comment and the text: %q", fs.Arg(0))
		case strings.TrimSpace(strings.Join(fs.Args()[1:], " ")) == "":
			return 2, errors.New("task ticket comment needs the text")
		}
	default:
		if fs.NArg() > 0 {
			return 2, fmt.Errorf("task %s takes no argument: %q", sub, fs.Arg(0))
		}
	}
	c, err := crewClientFromEnv(os.Getenv)
	if err != nil {
		return 1, fmt.Errorf("task: not in a Conductor session (%s and %s are not set)", notify.EnvURL, notify.EnvToken)
	}
	c.prefix = "task"
	switch sub {
	case "context":
		return taskContext(ctx, c, *page, *asJSON, stdout)
	case "ticket":
		if fs.NArg() > 0 {
			cm, err := commentOnSessionTicket(ctx, c, strings.Join(fs.Args()[1:], " "))
			if err != nil {
				return 1, err
			}
			fmt.Fprintln(stdout, sessionCommentText(cm))
			return 0, nil
		}
		raw, st, err := readSessionTicket(ctx, c)
		if err != nil {
			return 1, err
		}
		if *asJSON {
			var b bytes.Buffer
			if err := json.Indent(&b, raw, "", "  "); err != nil {
				return 1, err
			}
			fmt.Fprintln(stdout, b.String())
			return 0, nil
		}
		fmt.Fprint(stdout, sessionTicketText(st))
		return 0, nil
	case "send":
		if *to == "" {
			return 2, errors.New("task send: --to names the member")
		}
		out, err := c.call(ctx, http.MethodPost, "/messages", map[string]any{"to": *to, "text": strings.Join(fs.Args(), " ")})
		if err != nil {
			return 1, err
		}
		m, _ := out["message"].(map[string]any)
		fmt.Fprintf(stdout, "sent #%v to %v\n", m["seq"], m["to"])
		return 0, nil
	case "inbox":
		path := "/inbox"
		if *after > 0 {
			path += "?after=" + strconv.FormatInt(*after, 10)
		}
		out, err := c.call(ctx, http.MethodGet, path, nil)
		if err != nil {
			return 1, err
		}
		if *asJSON {
			b, err := json.MarshalIndent(out["messages"], "", "  ")
			if err != nil {
				return 1, err
			}
			fmt.Fprintln(stdout, string(b))
			return 0, nil
		}
		fmt.Fprintln(stdout, inboxText(out))
		return 0, nil
	case "ack":
		n, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil || n < 1 {
			return 2, fmt.Errorf("task ack: a sequence number from 1, not %q", fs.Arg(0))
		}
		out, err := c.call(ctx, http.MethodPost, "/inbox/ack", map[string]any{"upTo": n})
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "acknowledged %v\n", out["acked"])
		return 0, nil
	case "blocked":
		if *reason == "" {
			return 2, errors.New("task blocked: --reason says what stops you")
		}
		out, err := c.call(ctx, http.MethodPost, "/task/blocked", map[string]any{"reason": *reason, "message": *message})
		if err != nil {
			return 1, err
		}
		task, _ := out["task"].(map[string]any)
		fmt.Fprintf(stdout, "task %v shows as blocked; report working, or submit, once you go on\n", task["id"])
		return 0, nil
	case "checkpoint":
		if *done == "" || *remaining == "" {
			return 2, errors.New("task checkpoint: --done and --remaining say where you got to")
		}
		out, err := c.call(ctx, http.MethodPost, "/task/checkpoint", map[string]any{"done": *done, "remaining": *remaining, "notes": *notes})
		if err != nil {
			return 1, err
		}
		a, _ := out["attempt"].(map[string]any)
		cp, _ := a["checkpoint"].(map[string]any)
		fmt.Fprintf(stdout, "checkpoint saved at %v\n", cp["commit"])
		return 0, nil
	default:
		var res crew.Result
		if *file != "" {
			if *summary != "" || *commit != "" || *verdict != "" || len(issues) > 0 || len(findings) > 0 {
				return 2, errors.New("task result: give --file or the flags, not both")
			}
			if err := readResultFile(*file, stdin, &res); err != nil {
				return 2, fmt.Errorf("task result: %w", err)
			}
		} else {
			res = crew.Result{Summary: *summary, Commit: *commit, Issues: issues, Verdict: *verdict}
			for _, f := range findings {
				fd, err := parseFinding(f)
				if err != nil {
					return 2, fmt.Errorf("task result: %w", err)
				}
				res.Findings = append(res.Findings, fd)
			}
		}
		out, err := c.call(ctx, http.MethodPost, "/task/result", res)
		if err != nil {
			return 1, err
		}
		task, _ := out["task"].(map[string]any)
		fmt.Fprintf(stdout, "submitted: task %v is %v\n", task["id"], task["state"])
		return 0, nil
	}
}

// taskContext prints the task: every page in order, or the one asked for;
// asJSON prints task.json instead.
func taskContext(ctx context.Context, c *crewClient, page int, asJSON bool, stdout io.Writer) (int, error) {
	path := "/task"
	if page > 0 {
		path += "?page=" + strconv.Itoa(page)
	}
	out, err := c.call(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 1, err
	}
	if asJSON {
		b, err := json.MarshalIndent(out["task"], "", "  ")
		if err != nil {
			return 1, err
		}
		fmt.Fprintln(stdout, string(b))
		return 0, nil
	}
	text, _ := out["markdown"].(string)
	fmt.Fprint(stdout, text)
	pages, _ := out["pages"].(float64)
	for n := 2; page == 0 && n <= int(pages); n++ {
		more, err := c.call(ctx, http.MethodGet, "/task?page="+strconv.Itoa(n), nil)
		if err != nil {
			return 1, err
		}
		text, _ = more["markdown"].(string)
		fmt.Fprint(stdout, text)
	}
	return 0, nil
}

// readResultFile reads a result as strict JSON from the file, or stdin
// for "-".
func readResultFile(file string, stdin io.Reader, res *crew.Result) error {
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
	if err := dec.Decode(res); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}

// parseFinding reads "SEVERITY[:PATH[:LINE]] MESSAGE".
func parseFinding(s string) (crew.Finding, error) {
	head, msg, ok := strings.Cut(strings.TrimSpace(s), " ")
	msg = strings.TrimSpace(msg)
	if !ok || msg == "" {
		return crew.Finding{}, fmt.Errorf("a finding is \"SEVERITY[:PATH[:LINE]] MESSAGE\": %q", s)
	}
	fd := crew.Finding{Message: msg}
	parts := strings.Split(head, ":")
	fd.Severity = parts[0]
	switch len(parts) {
	case 1:
	case 2:
		fd.Path = parts[1]
	case 3:
		fd.Path = parts[1]
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 1 {
			return crew.Finding{}, fmt.Errorf("a finding's line is a number from 1: %q", s)
		}
		fd.Line = n
	default:
		return crew.Finding{}, fmt.Errorf("a finding is \"SEVERITY[:PATH[:LINE]] MESSAGE\": %q", s)
	}
	switch fd.Severity {
	case "blocker", "major", "minor", "nit":
	default:
		return crew.Finding{}, fmt.Errorf("a finding's severity is blocker, major, minor or nit: %q", s)
	}
	return fd, nil
}
