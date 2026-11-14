package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/notify"
)

const taskUsage = `Usage:
  conductor task context [--page N] [--json]   print this attempt's task (.conductor/task.md), or its JSON
  conductor task result [--summary TEXT] [--commit SHA] [--issue TEXT]...
                        [--verdict approved|needs_changes] [--finding "SEVERITY[:PATH[:LINE]] MESSAGE"]...
                        [--addressed ID[=NOTE]]... [--file result.json|-]
                                               submit this attempt's result, once
  conductor task send --to MEMBER TEXT...     send another member a message (typed into its session once it has its task)
  conductor task inbox [--after N] [--json]    your messages from the other members, after a sequence number
  conductor task ack N                         acknowledge your messages up to N
  conductor task blocked --reason TEXT [--message TEXT]
                                               say what stops your attempt; the people watching see it and are asked
  conductor task checkpoint --done TEXT --remaining TEXT [--notes TEXT]
                                               save where you got to, tied to your worktree's commit (commit first)
  conductor task ticket [--json]               print the Dispatch ticket your run was launched from or is linked to, or its JSON
  conductor task ticket comment TEXT...        comment on that ticket, as your run's member
  conductor task decide --type clarification|approval|assignment --question TEXT [--context TEXT]
                        [--option "LABEL[: TRADEOFFS]"]... [--recommend N] [--uncertainty TEXT]
                        [--authorizes TEXT] [--evidence REF]... [--subject SUBJECT]
                        [--supersedes dr-N] [--for role:RESP|person:NAME]
                        [--file request.json|-]
                                               ask the operator a decision your task waits for;
                                               answered at once with its id, dr-N, and open
  conductor task decision [dr-N] [--json]      your task's decisions, newest first, or one whole
  conductor task withdraw dr-N                 withdraw an open decision you asked

In a managed run, the session is one attempt at one task (CONDUCTOR_TASK).
A work task's result is its commit (HEAD unless --commit names it): commit
first, the worktree must be clean apart from .conductor/; say what you did
with --summary and what is left with --issue. A review's result is its
verdict with --finding (severity blocker, major, minor or nit); it commits
nothing. A repair of a person's requested changes names each change it
addressed with --addressed, the change's id as task.md gives it and a note
of what was done: a claim a person checks against your result, never the
change's resolution. --file reads the result as JSON instead: {"summary",
"commit", "issues": [], "verdict", "findings": [{"severity", "path",
"line", "message"}], "addressed": [{"id", "note"}]}; "-" reads stdin. A
refused result says what to fix; the same commit submitted twice is taken
once.

A decision is a question a person must answer before your work goes on
(docs/protocol.md, Decisions). dr-N is its id: task decision, task withdraw
and --supersedes name it, for your own task's decisions only (another
task's id is answered as one no decision has). Before you ask, a work task
commits and saves a checkpoint; a review commits nothing and saves no
checkpoint (task checkpoint refuses one), and asks about its candidate as
it is. Do not submit your result until it is applied. Its states: open
and answered (the response recorded, not yet applied) wait; applied goes
on with its answer; superseded, cancelled and expired mean no answer will
come. What is typed into this session about it, or written into your
task's next attempt's task file when this session has ended:
  "Decision dr-N (TYPE) answered by NAME: ANSWER. Why: WHY. (receipt dr-N/K)"
      applied (Why when a rationale was given; K the response's number):
      do what it says, even against your recommendation
  "The operator asks about dr-N: TEXT"
      ask again with what was asked in --context and --supersedes dr-N
  "Decision dr-N was cancelled by the operator, not approved: WHY"
      a no: do not do what it asked about; submit, naming it with --issue
  "dr-N was superseded: WHAT MOVED; ask again if you still need it", or
  "Decision dr-N's answer was not applied: WHAT MOVED"
      what it was about changed: never act on its answer; ask again
An approval has 2 to 6 options and says what a yes --authorizes; an
assignment has none (it is answered Done or Not done); --recommend and an
answer name an option by its number from 0, in the order given. --subject
is what it is about: commit (your HEAD, the default), artifact:PATH (a file
committed in your worktree, by its path in it), or
candidate:RUN/TASK/N@COMMIT (the result attempt N of TASK submitted, its
commit). --evidence cites a record of your own work by its ref: run:RUN,
task:RUN/TASK, attempt:RUN/TASK/N, check:RUN/TASK/N/CHECK/K (your task's),
commit:RUN/SHA (the full id, in your run's repository), session:SESSION,
question:SESSION/ID (this session's), message:RUN/SEQ (one to you), and in a
run launched from a ticket ticket:KEY-n, plan:KEY-n/REV (REV the number of
the plan revision that launched it, not an attempt's), comment:KEY-n/ID and
launch:ID (as its launch package names them); RUN, TASK and SESSION are
CONDUCTOR_RUN, CONDUCTOR_TASK and CONDUCTOR_SESSION_ID; N is the number of
the attempt the record belongs to (CONDUCTOR_ATTEMPT for yours; a check's
is an earlier attempt's, its checks run after it submits), CHECK the
check's id and K which run of it on that attempt, from 1. Any other ref, a
file: among them, and one that names no record refuse the request as one
that "resolves to nothing". --file reads the request as JSON instead, as
decision_request takes it: at most 1 MiB, one value and nothing after it
but white space, no field it does not take; a requestId in it, sent again,
replies with the same decision. A decide that failed may still have opened
its decision: list them with task decision before you ask again. A
withdrawal refused not_held means your run was continued: the decision
stays open in the run that continues this one; stop there, as this
attempt's work is over.

Outside a Conductor session, or in a session that is no attempt, every
command says so and exits 1.
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
	case "context", "result", "send", "inbox", "ack", "blocked", "checkpoint", "ticket", "decide", "decision", "withdraw":
	default:
		fmt.Fprint(stderr, taskUsage)
		return 2, fmt.Errorf("task: unknown command %q", sub)
	}
	fs := flag.NewFlagSet("task "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	page := fs.Int("page", 0, "context: the page to print (all unless given)")
	asJSON := fs.Bool("json", false, "context, inbox, ticket, decision: print the task (task.json), the messages, the ticket or the decisions as JSON")
	summary := fs.String("summary", "", "result: what you did")
	commit := fs.String("commit", "", "result: the result commit (HEAD unless given)")
	verdict := fs.String("verdict", "", "result: a review's verdict, approved or needs_changes")
	file := fs.String("file", "", "result, decide: read the result, or the decision's request, as JSON from this file, or - for stdin")
	var issues, findings, addressed multiFlag
	fs.Var(&issues, "issue", "result: an issue left or doubtful (repeatable)")
	fs.Var(&addressed, "addressed", "result: a person's requested change you addressed, ID[=NOTE], its id as task.md gives it (repeatable)")
	fs.Var(&findings, "finding", `result: a review's finding, "SEVERITY[:PATH[:LINE]] MESSAGE" (repeatable)`)
	to := fs.String("to", "", "send: the member the message goes to")
	after := fs.Int64("after", 0, "inbox: the last sequence number read")
	reason := fs.String("reason", "", "blocked: what stops you, one line")
	message := fs.String("message", "", "blocked: what you need from the people watching")
	done := fs.String("done", "", "checkpoint: what is done")
	remaining := fs.String("remaining", "", "checkpoint: what remains")
	notes := fs.String("notes", "", "checkpoint: anything the next attempt should know")
	var options, evidence multiFlag
	fs.Var(&options, "option", `decide: an alternative, "LABEL[: TRADEOFFS]", numbered from 0 in the order given (repeatable)`)
	fs.Var(&evidence, "evidence", "decide: a ref to a record of your own work the question rests on, as task:RUN/TASK or commit:RUN/SHA; any other resolves to nothing (repeatable; the usage lists them)")
	decide := decideFlags{
		typ:         fs.String("type", "", "decide: clarification, approval or assignment"),
		question:    fs.String("question", "", "decide: the question, one line of at most 500 characters"),
		context:     fs.String("context", "", "decide: what happened and why a person is needed, Markdown"),
		recommend:   fs.Int("recommend", -1, "decide: the option you recommend, by its number from 0"),
		uncertainty: fs.String("uncertainty", "", "decide: how sure you are"),
		authorizes:  fs.String("authorizes", "", "decide: what a yes allows (an approval's is required)"),
		subject:     fs.String("subject", "", "decide: commit (your HEAD, the default), artifact:PATH or candidate:RUN/TASK/N@COMMIT"),
		supersedes:  fs.String("supersedes", "", "decide: dr-N, an open decision of your task this one takes the place of"),
		forLabel:    fs.String("for", "", `decide: whom it is for, role:RESP or person:NAME, as your task file's "Who answers for this work" names them`),
		file:        file, options: &options, evidence: &evidence,
	}
	fs.Usage = func() {
		fmt.Fprint(stderr, taskUsage, "\nFlags:\n")
		fs.PrintDefaults()
	}
	// A decision's id may come before the flags or after them (task
	// decision dr-7 --json); fs.Args() is then the id alone.
	var err error
	if sub == "decision" || sub == "withdraw" {
		var pos []string
		if pos, err = parseInterspersed(fs, rest); err == nil {
			err = fs.Parse(append([]string{"--"}, pos...))
		}
	} else {
		err = fs.Parse(rest)
	}
	if err != nil {
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
	case "decision", "withdraw":
		if n := fs.NArg(); n > 1 || n == 0 && sub == "withdraw" {
			return 2, fmt.Errorf("task %s takes a decision's id, dr-N", sub)
		}
		if _, ok := ledger.ParseDecisionID(fs.Arg(0)); fs.NArg() == 1 && !ok {
			return 2, fmt.Errorf("task %s: %q is no decision's id: dr and its number, as dr-7", sub, fs.Arg(0))
		}
	default:
		if fs.NArg() > 0 {
			return 2, fmt.Errorf("task %s takes no argument: %q", sub, fs.Arg(0))
		}
	}
	var req crew.DecisionRequest
	if sub == "decide" {
		if req, err = decideRequest(decide, stdin); err != nil {
			return 2, fmt.Errorf("task decide: %w", err)
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
	case "decide":
		return taskDecide(ctx, c, req, stdout)
	case "decision":
		if fs.NArg() == 1 {
			return taskDecisionRead(ctx, c, fs.Arg(0), *asJSON, stdout)
		}
		return taskDecisions(ctx, c, *asJSON, stdout)
	case "withdraw":
		return taskWithdraw(ctx, c, fs.Arg(0), stdout)
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
			return printReply(stdout, raw, true)
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
			return printJSON(stdout, out["messages"])
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
			if *summary != "" || *commit != "" || *verdict != "" || len(issues) > 0 || len(findings) > 0 || len(addressed) > 0 {
				return 2, errors.New("task result: give --file or the flags, not both")
			}
			if err := readOneJSON(*file, stdin, maxResultFile, &res); err != nil {
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
			for _, a := range addressed {
				id, note, _ := strings.Cut(a, "=")
				if id = strings.TrimSpace(id); id == "" {
					return 2, fmt.Errorf("task result: --addressed is ID[=NOTE], the change's id first: %q", a)
				}
				res.Addressed = append(res.Addressed, crew.Addressed{ID: id, Note: strings.TrimSpace(note)})
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
		return printJSON(stdout, out["task"])
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
