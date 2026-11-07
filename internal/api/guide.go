package api

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// The operating guide (docs/plans/brakeman.md, BK0's first form): the
// running server says to an agent, or to a client, what this build lets
// the caller do, read from the one table of Conductor's operations
// (Operations) that the MCP server's tools are listed from too
// (internal/cli, mcpTools), so the tools and the guide cannot drift. It
// is computed per caller, never cached, in sections: a small overview
// first, then each section on request (GET /api/guide and GET
// /api/sessions/{id}/guide, docs/protocol.md, Guide).

// Operation is one of Conductor's operations an agent or a client calls:
// its route, its MCP tool and CLI form when it has them, the JSON Schema
// of its body or the tool's arguments, and what it answers with.
type Operation struct {
	// Name is the operation's name, the MCP tool's when it has one.
	Name string `json:"name"`
	// Summary is the guide's line, at most maxOpSummary bytes as the
	// guide encodes it (a character JSON escapes counts as its escape).
	Summary string `json:"summary"`
	// Description is the MCP tool's description, verbatim, at most
	// maxOpDescription bytes as encoded, apart from Summary so neither
	// bound breaks the other. The guide's operations section leaves it out: tools/list
	// carries it.
	Description string `json:"description,omitempty"`
	// Scope is who reaches it: admin, session (any session), task (an
	// attempt of a managed run) or ticket (a member of a run linked to a
	// ticket) or observe (a Brakeman turn's session: its request and its
	// brief; PR-4's reads join it) or act (a Brakeman turn's session
	// granted the operation by the person, PR-13: the act tool and the
	// operations granted, each its own route's).
	Scope  string `json:"scope"`
	Method string `json:"method"`
	// Path is the route's pattern, as the mux takes it after the method.
	Path string `json:"path"`
	// MCP is the tool's name, "" for an operation with no tool.
	MCP string `json:"mcp,omitempty"`
	// CLI is the command that does it, its words and placeholders.
	CLI []string `json:"cli,omitempty"`
	// Input is the JSON Schema of the body or of the tool's arguments.
	Input map[string]any `json:"input"`
	// Receipt names the receipt it answers with: ledger (a run command's),
	// dispatch (a Dispatch operation's), or "" for none.
	Receipt string `json:"receipt,omitempty"`
	// Revision names the precondition the route itself enforces: "run"
	// (a command's expectedRevision, above 0), "ticket" (Dispatch's
	// expectedRev) or "decision" (a decision's expectedRev); "" for a
	// route that decodes no revision (a run's stop and resume), which a
	// proposal never gets a button for.
	Revision string `json:"revision,omitempty"`
	// Bounds are the body's and the reply's limits, in bytes.
	Bounds map[string]int `json:"bounds,omitempty"`
	// needs is what the server must allow for the operation to be listed.
	needs opNeeds
	// agentOnly says only the session's own token calls it: the admin's
	// answers 403 agent_only (a task's result, a brief). Its route asks
	// it (opAgentOnly), and the guide lists the operation to no admin.
	agentOnly bool
	// guideInput is Input as the guide's operations section gives it,
	// when another section carries the whole of it (brief_submit's
	// brief, the briefs section); nil for Input itself.
	guideInput map[string]any
}

// opNeeds is what an operation needs of the server and of its caller.
type opNeeds uint8

const (
	// needLauncher: a server that launches (not a switchyard).
	needLauncher opNeeds = 1 << iota
	// needSelfService: agents.selfService on, not a switchyard, and, for a
	// session caller, a server session (a hosted session's folder is on
	// its own machine).
	needSelfService
	// needRun: for a session caller, one that is a member of a run
	// (add_member answers 409 no_run otherwise, run_status 404 no_run).
	needRun
	// needNoRun: for a session caller, one in no run (form_crew answers
	// 409 in_a_run otherwise).
	needNoRun
	// needUnmanaged: for a session caller, one in no managed run, whose
	// members the run makes itself (add_member answers 409 managed_run
	// otherwise).
	needUnmanaged
	// needNotBrakeman: for a session caller, one that is not a Brakeman
	// turn's, which forms no crew and adds no member (403
	// brakeman_cannot).
	needNotBrakeman
)

// The guide's bounds (docs/protocol.md, Guide), measured by
// TestEachSectionIsBounded with every entry at its largest.
const (
	guideVersion     = 1
	maxOpSummary     = 300
	maxOpDescription = 1 << 10
	maxGuideOverview = 4 << 10
	maxGuideSection  = 64 << 10
	maxGuideWhole    = 256 << 10
	// maxGuideAgents bounds the host section's agents, the rest counted.
	maxGuideAgents = 64
	// guideLookupWait bounds how long the host section waits for the
	// agents' programs to be looked up; a probe is never run for it, only
	// read from the cache.
	guideLookupWait = 500 * time.Millisecond
)

// The guide's sections, in the overview's order, beside the overview.
var guideSections = []string{"operations", "limits", "host", "receipts", "providers", "briefs"}

// The guide's three lines, for every caller.
var guideRules = []string{
	"A fact needs evidence: name the record it rests on (a run, a task, a check receipt, a ticket, a session).",
	"Nothing an agent writes becomes work by itself: a person makes it work.",
	"A question goes to a person with ask; in a managed run, one whose answer the work must wait for goes with decision_request.",
}

// mcpObject and mcpString build the MCP tools' schemas, as tools/list
// has always given them.
func mcpObject(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func mcpString(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// DecisionListed bounds one record as the operator's decision lists give
// it (GET /api/decisions, a ticket's decisions; listedOf): a page of n of
// them is at most n times it and 1 KiB more (maxDecisionListPage), the
// bound conductor decision list reads a page under.
const DecisionListed = maxDecisionListed

// Operations is the table: every agent route and tool, the admin's
// operations a proposal may name, and the guide. The caller must not
// change the maps it holds.
func Operations() []Operation { return slices.Clone(operationsTable()) }

var operationsTable = sync.OnceValue(func() []Operation {
	str, obj := mcpString, mcpObject
	const sessions = "/api/sessions/{id}"
	return []Operation{
		// The MCP tools, in the order tools/list has always given them.
		{Name: "report", MCP: "report", Scope: "session", Method: http.MethodPost, Path: sessions + "/events",
			Summary:     "Report to the people watching: progress at a milestone, an artifact with its url, a handoff to a member, a tool used or refused, an error, a file touched. One line.",
			Description: "Report something you did to the people watching this session: progress on a long task (at milestones), an artifact such as a pull request (with its url), a handoff to another crew member (with to), a tool refused or an error. One short line.",
			CLI:         []string{"conductor", "notify", "--event", "EVENT", "--message", "TEXT"},
			Input: obj(map[string]any{
				"event":   map[string]any{"type": "string", "enum": []string{"progress", "artifact", "handoff", "tool_use", "tool_denied", "error", "file"}},
				"message": str("one line, at most " + strconv.Itoa(session.MaxAttentionMessage) + " bytes"),
				"url":     str("artifact: where it lives"),
				"to":      str("handoff: the member the work goes to"),
				"tool":    str("tool_use, tool_denied, error, file: the tool involved"),
				"op":      map[string]any{"type": "string", "enum": []string{"read", "edit", "write", "delete"}, "description": "file: what you did to it"},
				"path":    str("file: the file's path"),
			}, "event"),
			Bounds: map[string]int{"body": maxBody, "message": session.MaxAttentionMessage, "url": session.MaxEventURL}},
		{Name: "set_state", MCP: "set_state", Scope: "session", Method: http.MethodPost, Path: sessions + "/attention",
			Summary:     "Say you are working or done, or clear the state; a question is ask's.",
			Description: "Tell the people watching that you are working, done, or clear the state; use ask for a question.",
			CLI:         []string{"conductor", "notify", "--state", "working|done|clear"},
			Input:       obj(map[string]any{"state": map[string]any{"type": "string", "enum": []string{"working", "done", "clear"}}, "message": str("one line")}, "state"),
			Bounds:      map[string]int{"body": maxBody, "message": session.MaxAttentionMessage}},
		{Name: "ask", MCP: "ask", Scope: "session", Method: http.MethodPost, Path: sessions + "/attention",
			Summary:     "Ask the people watching for a decision you cannot make: the session needs input until someone answers in the terminal; at most 6 choices.",
			Description: "Ask the people watching for a decision you cannot make yourself; the session shows as needing input until someone answers in the terminal, also once your turn has ended. With choices (at most 6, each at most 40 bytes), name them in the terminal too: a click types the choice as a line. If you no longer need the answer, clear the question with set_state clear.",
			CLI:         []string{"conductor", "notify", "--ask", "--message", "TEXT", "--choices", "a|b"},
			Input:       obj(map[string]any{"message": str("the question, one line"), "choices": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": session.MaxAttentionOptions}}, "message"),
			Bounds:      map[string]int{"body": maxBody, "message": session.MaxAttentionMessage}},
		{Name: "form_crew", MCP: "form_crew", Scope: "session", Method: http.MethodPost, Path: sessions + "/crew", needs: needLauncher | needSelfService | needNoRun | needNotBrakeman,
			Summary:     "Form a crew around this session when the work splits into parts that run side by side: this session stays as one member. At most " + strconv.Itoa(selfCrewsPerHour) + " an hour.",
			Description: "Form a crew around this session when the work splits into parts that can run side by side: you stay as the member named by self (its agentId and prompt may be empty), the others get sessions of their own. At most " + strconv.Itoa(selfCrewsPerHour) + " an hour; never for work you can finish alone.",
			CLI:         []string{"conductor", "crew", "create", "CREW.json", "--self", "NAME"},
			Input: obj(map[string]any{
				"name":      str("the crew's name"),
				"goal":      str("the goal, reaching prompts as $GOAL"),
				"members":   map[string]any{"type": "array", "description": "{name, agentId, prompt, start: {when: immediately|after|manual, member?}}", "items": map[string]any{"type": "object"}},
				"self":      str("the member this session becomes"),
				"isolation": map[string]any{"type": "string", "enum": []string{"none", "worktree"}},
				"open":      map[string]any{"type": "boolean", "description": "offer the run in the workbench"},
			}, "name", "goal", "members", "self"),
			Bounds: map[string]int{"body": maxCrewBody}},
		{Name: "add_member", MCP: "add_member", Scope: "session", Method: http.MethodPost, Path: sessions + "/run/members", needs: needLauncher | needSelfService | needRun | needUnmanaged | needNotBrakeman,
			Summary:     "Add a member to this session's run.",
			Description: "Add a member to this session's run: {name, agentId, prompt, start}.",
			CLI:         []string{"conductor", "crew", "add", "MEMBER.json"},
			Input:       obj(map[string]any{"member": map[string]any{"type": "object"}}, "member"),
			Bounds:      map[string]int{"body": maxMemberBody}},
		{Name: "run_status", MCP: "run_status", Scope: "session", Method: http.MethodGet, Path: sessions + "/run", needs: needLauncher | needSelfService | needRun,
			Summary:     "This session's run and its members, with their states.",
			Description: "This session's run and its members, with their states.",
			CLI:         []string{"conductor", "crew", "status"},
			Input:       obj(map[string]any{})},
		{Name: "link", MCP: "link", Scope: "session", Method: http.MethodPost, Path: sessions + "/links/agent", needs: needSelfService,
			Summary:     "A view-only link to this session, for a pull request or a message: a day at most, five a day.",
			Description: "A view-only link to this session, for a pull request or a message; a day at most.",
			CLI:         []string{"conductor", "crew", "link", "--ttl", "2h", "--label", "TEXT"},
			Input:       obj(map[string]any{"ttlSeconds": map[string]any{"type": "integer"}, "label": str("what it is for")}),
			Bounds:      map[string]int{"body": maxBody, "ttlSeconds": int(selfLinkMaxTTL / time.Second)}},
		{Name: "task_context", MCP: "task_context", Scope: "task", Method: http.MethodGet, Path: sessions + "/task", needs: needLauncher,
			Summary:     "Your task, as an attempt of a managed run: the goal, the task, its inputs, the findings to address, its checks and how to submit; .conductor/task.md holds the same.",
			Description: "Your task, when this session is an attempt at a task of a managed run (CONDUCTOR_TASK is set): the goal, the task, where it starts and its inputs, findings to address, how it is checked and how to submit. The same text is .conductor/task.md in your working directory. Long tasks come in pages of 32 KiB: page 1 unless given.",
			CLI:         []string{"conductor", "task", "context"},
			Input:       obj(map[string]any{"page": map[string]any{"type": "integer", "minimum": 1, "description": "the page to read"}}),
			Bounds:      map[string]int{"page": taskPage}},
		{Name: "task_result", MCP: "task_result", Scope: "task", Method: http.MethodPost, Path: sessions + "/task/result", needs: needLauncher, agentOnly: true,
			Summary:     "Submit your attempt's result, once: a work task its clean commit and a summary; a review its verdict and findings. Conductor checks it and decides the task.",
			Description: "Submit your attempt's result, once. A work task: commit your work first (the worktree must be clean apart from .conductor/), then give a summary, the issues you know of, and the commit (HEAD unless given). A review: do not commit; give the verdict, approved or needs_changes, with findings {severity: blocker|major|minor|nit, path, line, message}. Conductor checks the commit and decides the task; a refusal says what to fix before you submit again. The same commit submitted twice is taken once. A done or a handoff does not finish a task; only this does.",
			CLI:         []string{"conductor", "task", "result", "--summary", "TEXT"},
			Input: obj(map[string]any{
				"summary":  str("what you did, one paragraph, at most 4000 characters"),
				"commit":   str("the result commit, the full id; HEAD when not given"),
				"issues":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 20, "description": "what is left or doubtful, one line each"},
				"verdict":  map[string]any{"type": "string", "enum": []string{"approved", "needs_changes"}, "description": "a review's verdict"},
				"findings": map[string]any{"type": "array", "maxItems": 50, "items": map[string]any{"type": "object", "properties": map[string]any{"severity": map[string]any{"type": "string", "enum": []string{"blocker", "major", "minor", "nit"}}, "path": str("the file"), "line": map[string]any{"type": "integer"}, "message": str("what to change")}, "required": []string{"severity", "message"}}, "description": "a review's findings"},
			}),
			Bounds: map[string]int{"body": maxBody}},
		{Name: "message_send", MCP: "message_send", Scope: "task", Method: http.MethodPost, Path: sessions + "/messages", needs: needLauncher,
			Summary:     "Send another member of your managed run a message, typed into its session once its attempt has its task.",
			Description: "In a managed run, send another member a message: kept with the run and typed into that member's session as one line (\"Message from <you> (#n): …\") once its attempt has its task; a member not started yet gets it then. A member keeps at most 50 messages not acknowledged. Use it for what the next task needs to know beyond your result; a result is still submitted with task_result.",
			CLI:         []string{"conductor", "task", "send", "--to", "MEMBER", "TEXT"},
			Input:       obj(map[string]any{"to": str("the member's name"), "text": str("the message, at most 4000 characters")}, "to", "text"),
			Bounds:      map[string]int{"body": maxBody}},
		{Name: "inbox_read", MCP: "inbox_read", Scope: "task", Method: http.MethodGet, Path: sessions + "/inbox", needs: needLauncher,
			Summary:     "Your messages from the other members of your managed run, in order, after a sequence number.",
			Description: "In a managed run, your messages from the other members, in order: each with its sequence number, who sent it and whether it was typed into your session. Pass after to read past what you have seen.",
			CLI:         []string{"conductor", "task", "inbox", "--after", "N"},
			Input:       obj(map[string]any{"after": map[string]any{"type": "integer", "minimum": 0, "description": "the last sequence number read; 0 for all"}})},
		{Name: "inbox_ack", MCP: "inbox_ack", Scope: "task", Method: http.MethodPost, Path: sessions + "/inbox/ack", needs: needLauncher,
			Summary:     "Acknowledge your messages up to a sequence number: they count no more against your inbox.",
			Description: "In a managed run, acknowledge your messages up to a sequence number: they count no more against your inbox.",
			CLI:         []string{"conductor", "task", "ack", "N"},
			Input:       obj(map[string]any{"upTo": map[string]any{"type": "integer", "minimum": 1}}, "upTo"),
			Bounds:      map[string]int{"body": maxBody}},
		{Name: "task_blocked", MCP: "task_blocked", Scope: "task", Method: http.MethodPost, Path: sessions + "/task/blocked", needs: needLauncher,
			Summary:     "Say what stops your attempt: the task shows as blocked and your session asks the people watching; working, done or a result clears it.",
			Description: "In a managed run, say what stops your attempt at its task: a missing credential, a decision, something to be done by hand. The people watching see the task as blocked with the reason, and your session asks them the message (else the reason) as your question; reporting working or done, or submitting, clears it.",
			CLI:         []string{"conductor", "task", "blocked", "--reason", "TEXT"},
			Input:       obj(map[string]any{"reason": str("what blocks you, one line of at most 500 characters"), "message": str("what you need from the people watching, at most 4000 characters")}, "reason"),
			Bounds:      map[string]int{"body": maxBody}},
		{Name: "task_checkpoint", MCP: "task_checkpoint", Scope: "task", Method: http.MethodPost, Path: sessions + "/task/checkpoint", needs: needLauncher,
			Summary:     "Save where your attempt got to, tied to your worktree's commit (commit first), for the next attempt at the task.",
			Description: "In a managed run, save where your attempt got to, tied to the commit your worktree is at (commit first): what is done, what remains, notes. The next attempt at the task reads it in its task file, and one after an attempt that ends without a result starts at that commit, so the work is not lost.",
			CLI:         []string{"conductor", "task", "checkpoint", "--done", "TEXT", "--remaining", "TEXT"},
			Input:       obj(map[string]any{"done": str("what is done, at most 4000 characters"), "remaining": str("what remains"), "notes": str("anything the next attempt should know")}, "done", "remaining"),
			Bounds:      map[string]int{"body": maxBody}},
		{Name: "decision_request", MCP: "decision_request", Scope: "task", Method: http.MethodPost, Path: sessions + "/decisions", needs: needLauncher,
			Summary:     "Ask the operator a decision your task must wait for: the reply comes at once with its id; end your turn or go on with other work, and submit no result until its answer is applied.",
			Description: "In a managed run, ask the operator a decision your work must wait for: a clarification, an approval (with options and what a yes authorizes) or an assignment, with the context, the alternatives and their tradeoffs, your recommendation and its uncertainty, and evidence refs. Save a checkpoint first. It replies at once with the decision's id and open: end your turn, or go on with work that does not depend on it; do not submit your result, or act on the answer, until it is applied. A decision is open (waiting), answered (an answer recorded, not yet applied: Yardmaster validates it first, and it may still be superseded or cancelled instead) or applied (the answer is yours to act on). Yardmaster applies the answer by typing it into this session as \"Decision dr-N (...) answered by ...\", or, if this session has ended, by writing it into your task's next attempt's task file, which starts at your last checkpoint. The same requestId again replies with the same decision as it is now.",
			CLI:         []string{"conductor", "task", "decide", "--type", "TYPE", "--question", "TEXT"},
			Input: obj(map[string]any{
				"requestId":      str("your id for this request, 1 to 32 of A-Z a-z 0-9 . _ ~ -: the same again answers the same decision"),
				"type":           map[string]any{"type": "string", "enum": []string{"clarification", "approval", "assignment"}},
				"question":       str("the question, one line of at most 500 characters"),
				"context":        str("what happened and why a person is needed, Markdown, at most 4000 characters"),
				"options":        map[string]any{"type": "array", "maxItems": 6, "items": obj(map[string]any{"label": str("at most 80 characters"), "tradeoffs": str("at most 1 KiB")}, "label"), "description": "the alternatives: 2 to 6 for an approval, none for an assignment"},
				"recommendation": map[string]any{"type": "integer", "minimum": -1, "description": "the option you recommend, from 0; -1 for none"},
				"uncertainty":    str("how sure you are, at most 1 KiB"),
				"authorizes":     str("what a yes allows, at most 1 KiB; an approval's is required"),
				"evidence":       map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "string"}, "description": "refs to the records it rests on (run:, task:, ticket:, file:, ...), at most 300 bytes each"},
				"expertise":      map[string]any{"type": "array", "maxItems": 4, "items": map[string]any{"type": "string"}, "description": "the expertise the answer needs, at most 40 characters each"},
				"subject":        obj(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"commit", "candidate", "artifact"}}, "commit": str("commit: your HEAD; candidate: its result commit, 7 hex digits at least"), "run": str("candidate: the run"), "task": str("candidate: the task"), "n": map[string]any{"type": "integer", "description": "candidate: the attempt"}, "path": str("artifact: the file, relative to your worktree, committed")}, "kind"),
				"supersedes":     str("dr-N, an open decision of your task this one takes the place of"),
			}, "type", "question"),
			Bounds: map[string]int{"body": maxDecisionRequestBody}},
		{Name: "decision_list", MCP: "decision_list", Scope: "task", Method: http.MethodGet, Path: sessions + "/decisions", needs: needLauncher,
			Summary:     "Your task's decisions, newest first, each a summary: its id, state, question and answer.",
			Description: "In a managed run, your task's decisions, newest first, at most 100 a page: each its id, type, state, rev, question and, once answered, the answer and why. Act on an answer only once its state is applied: answered is recorded, not yet applied. Pass after (the next a page gave) for the next page; decision_read reads one whole.",
			CLI:         []string{"conductor", "task", "decision"},
			Input:       obj(map[string]any{"after": str("dr-N: the page after it"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maxDecisionsListed}}),
			Bounds:      map[string]int{"reply": maxDecisionSummaryPage, "summary": maxDecisionSummary}},
		{Name: "decision_read", MCP: "decision_read", Scope: "task", Method: http.MethodGet, Path: sessions + "/decisions/{did}", needs: needLauncher,
			Summary:     "One of your task's decisions whole: the request, the responses, the history and the application.",
			Description: "In a managed run, one of your task's decisions whole: what was asked, its state, the responses with their receipts, its history and how Yardmaster applied the answer. Act on the answer only once its state is applied: answered is recorded, not yet applied.",
			CLI:         []string{"conductor", "task", "decision", "dr-N"},
			Input:       obj(map[string]any{"id": str("the decision's id, dr-N")}, "id"),
			Bounds:      map[string]int{"reply": maxDecisionReply}},
		{Name: "decision_withdraw", MCP: "decision_withdraw", Scope: "task", Method: http.MethodPost, Path: sessions + "/decisions/{did}/withdraw", needs: needLauncher,
			Summary:     "Withdraw an open decision you asked that you no longer need: your task waits for it no more.",
			Description: "In a managed run, withdraw an open decision you asked that you no longer need: it is cancelled and your task waits for it no more. An answered one cannot be withdrawn, and none once your run is being continued (not_held: the decision stays open in the run that continues it, and this attempt's work is over).",
			CLI:         []string{"conductor", "task", "withdraw", "dr-N"},
			Input:       obj(map[string]any{"id": str("the decision's id, dr-N")}, "id"),
			Bounds:      map[string]int{"body": maxDecisionAnswerBody}},
		{Name: "ticket_read", MCP: "ticket_read", Scope: "ticket", Method: http.MethodGet, Path: sessions + "/ticket", needs: needLauncher,
			Summary:     "The Dispatch ticket your run is linked to: what was asked and how it is accepted, with its last comments.",
			Description: "The Dispatch ticket your run is linked to, when a person linked it to one: its key and title, status, priority, owner, labels, description, acceptance criteria, close policy, links and last comments. Read it for what was asked and how it is accepted. A run belongs to one ticket at most; with none, the tool says so.",
			CLI:         []string{"conductor", "task", "ticket"},
			Input:       obj(map[string]any{}),
			Bounds:      map[string]int{"reply": MaxTicketReply}},
		{Name: "ticket_comment", MCP: "ticket_comment", Scope: "ticket", Method: http.MethodPost, Path: sessions + "/ticket/comments", needs: needLauncher, Receipt: "dispatch",
			Summary:     "Comment on the Dispatch ticket your run is linked to, as your run's member; you read the ticket and comment, nothing more.",
			Description: "Comment on the Dispatch ticket your run is linked to, for the people who own it: what you found, what you did, what is left, as Markdown of at most 16 KiB. The comment is your run member's. You read the ticket and comment on it; you cannot change it.",
			CLI:         []string{"conductor", "task", "ticket", "comment", "TEXT"},
			Input:       obj(map[string]any{"body": str("the comment, Markdown of at most 16 KiB")}, "body"),
			Bounds:      map[string]int{"body": maxBody, "comment": dispatch.MaxCommentBody}},
		// A Brakeman turn's request and brief (brakeman_session.go); their
		// tools are handled from PR-5.
		{Name: "brakeman_request", MCP: "brakeman_request", Scope: "observe", Method: http.MethodGet, Path: sessions + "/brakeman/request", needs: needLauncher,
			Summary:     "Your Brakeman request: its kind, subjects as resolved, note, since, the last briefing you may build on, what you may see and your budget left.",
			Description: "When this session is a Brakeman turn (CONDUCTOR_BRAKEMAN is set): the request you are to answer, its kind and subjects as resolved now, the note and since, the last briefing you may build on, the scope and visibility you may read under and what your budget has left. Read it first.",
			CLI:         []string{"conductor", "brakeman", "request"},
			Input:       obj(map[string]any{})},
		// Its input's brief is the brief's JSON Schema, made from
		// internal/brakeman's kind table (as the guide's briefs section's
		// words are), a draft crew's crew the body crew_create takes;
		// the operations section, which tools/list need not repeat,
		// points to the briefs section for it.
		{Name: "brief_submit", MCP: "brief_submit", Scope: "observe", Method: http.MethodPost, Path: sessions + "/brakeman/brief", needs: needLauncher, Receipt: "brakeman", agentOnly: true,
			Summary:     "Submit your turn's one brief (brakeman.brief/1): every fact names its evidence, every ref resolves in your scope; the same opId again answers it.",
			Description: "Submit your Brakeman turn's one brief, schema brakeman.brief/1: a title, your reading (summary), and at least one item (facts with evidence refs, interpretations naming facts, proposals naming an operation of the guide, outcomes naming a receipt, decision cards, drafts); a briefing holds at least one fact. The brief's input schema gives every item kind with its fields, bounds and an example, and the ref forms; the guide's briefs section says the same in words. Every ref must resolve inside your scope and visibility, and a revision you name must be the one you read; a refusal names what to fix. The same opId and brief again answer the kept one. Your turn ends once it is kept.",
			CLI:         []string{"conductor", "brakeman", "brief", "--file", "BRIEF.json"},
			Input:       obj(map[string]any{"opId": str("your id for this submission, 1 to 32 of A-Z a-z 0-9 . _ ~ -"), "brief": brakeman.BriefJSONSchema(bodySchema(crewInput{}))}, "brief"),
			guideInput:  obj(map[string]any{"opId": str("your id for this submission, 1 to 32 of A-Z a-z 0-9 . _ ~ -"), "brief": map[string]any{"type": "object", "description": "the brief, schema brakeman.brief/1: its JSON Schema and its shape in words are the guide's briefs section"}}, "brief"),
			Bounds:      map[string]int{"body": maxBriefBody, "brief": brakeman.MaxBrief, "refs": brakeman.MaxRefsDoc}},
		// What a Brakeman turn reads (brakeman_observe.go): the observe
		// tool sends each what to its own route, by the entries below
		// (observe_<what>), on the client side; the server has no
		// dispatcher of its own. A turn's session token names its session
		// in X-Conductor-Session on each.
		{Name: "observe", MCP: "observe", Scope: "observe", Method: http.MethodGet, Path: "/api/brakeman/changes", needs: needLauncher,
			Summary:     "Read what you may see as a Brakeman turn: what changed, a status, runs, tasks, receipts, logs, candidates, tickets, plan revisions, sessions, files, overlaps, earlier briefs.",
			Description: "When this session is a Brakeman turn: read one thing Conductor holds, within your scope and visibility, each answer counted against your reads. what names it (changes, status, screen, overlaps, briefs, runs, run, task, steps, log, command, messages, candidates, projects, tickets, ticket, history, comments, plans, sessions, touched) and query its parameters (run, task, n, id, k, command, key, session, since, cursor, limit, lines, q, after, before). Every item carries the ref a brief cites it by; each answer keeps its observe_<what> entry's bound.",
			CLI:         []string{"conductor", "brakeman", "observe", "WHAT", "--KEY", "VALUE"},
			Input: obj(map[string]any{
				"what":  map[string]any{"type": "string", "enum": observeWhats, "description": "what to read: the guide's observe_<what> operation names its route"},
				"query": map[string]any{"type": "object", "description": "the read's parameters, by name: the route's path parameters and its query", "additionalProperties": map[string]any{"type": "string"}},
			}, "what"),
			Bounds: map[string]int{"reply": maxTurnReply}},
		observeOp("changes", "/api/brakeman/changes", "What changed since a time, or since your last briefing's watermarks: runs, tasks with their status, questions, messages, Dispatch's changes; paged.", "since", "cursor", "limit"),
		observeOp("status", "/api/brakeman/status", "Each task's status, or a session's, as Conductor computes it: failed with its receipt, blocked, waiting on a person, quiet (no proof), running, succeeded, stopped.", "run", "session"),
		observeOp("screen", "/api/brakeman/screen", "A server session's screen's last lines, only with terminals visible; a hosted session's is unavailable.", "session", "lines"),
		observeOp("overlaps", "/api/brakeman/overlaps", "Paths changed in two or more live attempts' or sessions' work trees of one repository, with the trees' refs."),
		observeOp("briefs", "/api/brakeman/briefs", "Earlier briefs, newest first: title, summary and refs; one made under a wider scope or visibility is withheld.", "q", "after"),
		observeOp("runs", "/api/runs", "The runs in your scope, newest first, each with its ref, a page at a time: after (the page before's next) and limit (at most 100). A run past the page's bound alone comes short, its cut naming what it leaves out (tasks: read each with observe_task).", "after", "limit"),
		observeOp("run", "/api/runs/{run}", "A run whole, with its tasks, each with its ref; a run past the bound comes short, its cut naming what it leaves out (tasks: read each with observe_task)."),
		observeOp("task", "/api/runs/{run}/tasks/{task}", "A managed task with its attempts and their receipts."),
		observeOp("steps", "/api/runs/{run}/tasks/{task}/attempts/{n}/steps", "An attempt's receipts: each check's exit, subject and log SHA-256."),
		observeOp("log", "/api/runs/{run}/tasks/{task}/attempts/{n}/steps/{id}/{k}/log", "A check's log as text, its SHA-256 in X-Conductor-Log-SHA256; only with logs visible."),
		observeOp("command", "/api/runs/{run}/commands/{command}", "A run command's receipt."),
		observeOp("messages", "/api/runs/{run}/messages", "A managed run's members' messages, a page at a time: after (the page before's next) and limit (at most 200).", "after", "limit"),
		observeOp("candidates", "/api/runs/{run}/candidates", "A managed run's candidates: each task's attempts that submitted something, with the commit, whether it stands, and the receipts of the checks run against it, each with its ref; a page of tasks at a time (after), or one task (task) and its older ones (before).", "after", "task", "before"),
		observeOp("projects", "/api/dispatch/projects", "Dispatch's projects in your scope.", "archived", "limit", "cursor"),
		observeOp("tickets", "/api/dispatch/tickets", "A page of tickets in your scope, each with its ref and rev.", "project", "status", "q", "limit", "cursor"),
		observeOp("ticket", "/api/dispatch/tickets/{key}", "A ticket whole with its last comments, each with its ref and rev."),
		observeOp("history", "/api/dispatch/tickets/{key}/history", "A ticket's history, field by field, each change with the ref of its operation.", "after", "limit"),
		observeOp("comments", "/api/dispatch/tickets/{key}/comments", "A ticket's comments past the ones its read carries.", "after", "limit"),
		observeOp("plans", "/api/dispatch/tickets/{key}/plans", "A ticket's plan revisions, newest first, each with its ref, state and rev; the approved one (approved) and whether its approval still holds (holds); the launches made from them, each with its ref. A deleted ticket's are read deleted (deleted: true), its approval holding false."),
		observeOp("sessions", "/api/sessions", "The sessions in your scope: names, states, questions."),
		observeOp("touched", "/api/touched", "The files the sessions in your scope touched.", "limit"),
		// What a Brakeman turn may do once the person grants it
		// (brakeman_act.go, PR-12): the act tool sends each operation to
		// its own route, by the entries below (one per operation this
		// build serves, named as the grant is), on the client side, as
		// observe does; there is no act route of Brakeman's own. A turn's
		// session token names its session in X-Conductor-Session on each.
		// A turn's guide lists the tool and the operations its grant
		// holds, the snapshot and the configuration now together.
		{Name: "act", MCP: "act", Scope: "act", Method: http.MethodPost, Path: actRouteOf(brakeman.ActLaunchApprovedPlan), needs: needLauncher, Receipt: "dispatch",
			Summary:     "Run an operation the person granted you, through its own route: checked against the grant, your scope and its target's state, it leaves its receipt.",
			Description: "When this session is a Brakeman turn the person granted operations to: run one (operation names it: " + strings.Join(actOperations(), ", ") + ") through that operation's own route, with its path parameters in params and its body in body, as the guide's entry of that operation gives them; opId is minted when you give none, and the same opId again answers the receipt kept. It is refused unless the person grants it now and did when your turn began, its target is within your scope and visibility, and in the state the operation needs (an approved revision whose approval still holds; the ticket's revision you read as expectedRev). It runs as Brakeman and answers its route's own answer: the receipt, whose id is what it made (the launch's, the revision's), and receiptRef, the ref a brief cites the receipt by (op:brakeman/<the receipt's opId>, an outcome/1's receipt). Approving, answering, accepting and closing stay the person's: no grant does them.",
			CLI:         []string{"conductor", "brakeman", "act", "OPERATION", "--key", "KEY", "--body-file", "BODY.json"},
			Input: obj(map[string]any{
				"operation": map[string]any{"type": "string", "enum": actOperations(), "description": "the operation granted: the guide's entry of that name gives its route and its body"},
				"params":    map[string]any{"type": "object", "description": "the route's path parameters, by name (key: the ticket's key)", "additionalProperties": map[string]any{"type": "string"}},
				"body":      map[string]any{"type": "object", "description": "the route's body, as the operation's entry gives its schema, without opId"},
				"opId":      str("your id for this act, 1 to 32 of A-Z a-z 0-9 . _ ~ -; minted when absent"),
			}, "operation")},
		actEntry(brakeman.ActLaunchApprovedPlan, ticketLaunchBody{},
			"Launch a managed run from a ticket's plan revision the operator approved and whose approval still holds (plan: its id; expectedRev: the ticket's revision you read); the same launch for the same opId."),
		actEntry(brakeman.ActPrepareFollowup, planProposeBody{},
			"Propose a revision of a ticket's plan basedOn the approved one (its id), which the operator then approves or not: never approved by you."),
		// The guide itself.
		{Name: "guide", MCP: "guide", Scope: "session", Method: http.MethodGet, Path: sessions + "/guide",
			Summary:     "This guide, from the running server: the overview without a section, else the section named.",
			Description: "What this Conductor server lets you do here, from the server itself: without a section, an overview (the server's version, who you are, the operations you may call and the limits that bind you, the sections and their sizes); with one, that section: operations (what you may call, with its tool, command, route and input), limits, host, receipts, providers, or briefs (a Brakeman brief's shape: its item kinds, their fields and bounds, the ref forms, an example).",
			CLI:         []string{"conductor", "guide", "--section", "SECTION"},
			Input:       obj(map[string]any{"section": map[string]any{"type": "string", "enum": guideSections, "description": "the section to read; the overview when not given"}}),
			Bounds:      map[string]int{"overview": maxGuideOverview, "section": maxGuideSection}},
		{Name: "open_url", MCP: "open_url", Scope: "session", Method: http.MethodPost, Path: sessions + "/open", needs: needSelfService,
			Summary:     "Open a link meant for the person (an artifact, a pull request, a preview, a sign-in page) in the browser this agent's settings name: ten a minute.",
			Description: "Open a link meant for the person, such as an artifact, a pull request, a preview or a sign-in page, in the browser this agent's settings name, on the computer the desktop app runs on; with no desktop app, the workbench offers it. Answers what happened. Ten a minute; not for a link you only read yourself.",
			CLI:         []string{"conductor", "open", "URL"},
			Input:       obj(map[string]any{"url": str("the http:// or https:// link, at most 2048 bytes")}, "url"),
			Bounds:      map[string]int{"body": maxOpenBody, "url": MaxOpenURL}},
		{Name: "server_guide", Scope: "admin", Method: http.MethodGet, Path: "/api/guide",
			Summary: "The guide for the workbench token: every operation, the server's limits, host, receipts, providers and a brief's shape.",
			CLI:     []string{"conductor", "guide", "--server", "URL", "--section", "SECTION"},
			Input:   map[string]any{"type": "object", "description": "no body; ?section= names the section"},
			Bounds:  map[string]int{"overview": maxGuideOverview, "section": maxGuideSection}},
		// The admin's operations a proposal may name.
		runCommandOp("run_pause", "Pause a managed run's dispatch: nothing new starts; what runs goes on.", "/pause"),
		runCommandOp("run_unpause", "Let a paused managed run dispatch again.", "/unpause"),
		{Name: "run_stop", Scope: "admin", Method: http.MethodPost, Path: "/api/runs/{run}/stop", needs: needLauncher,
			Summary: "Stop a run: every member's session ends. It takes no revision: a stop always works.",
			Input:   map[string]any{"type": "object", "description": "no body"}},
		{Name: "run_resume", Scope: "admin", Method: http.MethodPost, Path: "/api/runs/{run}/resume", needs: needLauncher,
			Summary: "Resume a stopped run, or continue a managed one: its results kept, the rest dispatched again. It takes no revision.",
			CLI:     []string{"conductor", "up", "--resume", "RUN"},
			Input:   map[string]any{"type": "object", "description": "no body"}},
		taskCommandOp("task_cancel", "Cancel a task of a managed run: its attempt stops and the task ends cancelled.", "/cancel"),
		taskCommandOp("task_retry", "Retry a "+strings.Join(crew.RetryableStates(), " or ")+" task of a managed run: a new attempt starts.", "/retry"),
		taskCommandOp("task_accept", "Accept a task's submitted result although its checks did not pass.", "/accept"),
		{Name: "task_recheck", Scope: "admin", Method: http.MethodPost, Path: "/api/runs/{run}/tasks/{task}/recheck", Receipt: "ledger", Revision: "run", needs: needLauncher,
			Summary: "Check again a failed task's standing failed candidate (attempt and commit, as its run names it): the same commit verified again as a new attempt, in the newest run of its chain.",
			Input:   bodySchema(recheckBody{}), Bounds: map[string]int{"body": maxBody}},
		{Name: "diff", Scope: "admin", Method: http.MethodGet, Path: "/api/runs/{run}/tasks/{task}/attempts/{n}/diff", needs: needLauncher,
			Summary: "A candidate's files against a base (?base=: the run's by default, the attempt's start, or a result of the task in the run's chain), with their counts.",
			Input:   map[string]any{"type": "object", "description": "no body; ?base= names the base"},
			Bounds:  map[string]int{"reply": MaxDiffReply}},
		{Name: "diff_file", Scope: "admin", Method: http.MethodGet, Path: "/api/runs/{run}/tasks/{task}/attempts/{n}/diff/file", needs: needLauncher,
			Summary: "A file of a candidate's diff as text at one side (?path=, ?side=base|candidate, ?base=), its commit in X-Conductor-Commit.",
			Input:   map[string]any{"type": "object", "description": "no body; ?path=, ?side= and ?base= name the file"},
			Bounds:  map[string]int{"reply": gitcli.MaxShowBytes}},
		{Name: "repair_prepare", Scope: "admin", Method: http.MethodGet, Path: "/api/runs/{run}/tasks/{task}/attempts/{n}/steps/{id}/{k}/repair", needs: needLauncher,
			Summary: "A failed receipt as a prepared repair: a check finding with its class, the criteria, and whether Check again would be taken now (recheck).",
			Input:   map[string]any{"type": "object", "description": "no body"},
			Bounds:  map[string]int{"reply": MaxRepairReply}},
		{Name: "decision_respond", Scope: "admin", Method: http.MethodPost, Path: "/api/decisions/{did}/respond", needs: needLauncher, Receipt: "ledger", Revision: "decision",
			Summary: "Answer a decision as the operator: an option's index, a text, or both for a clarification, with a rationale and the rev read. Recorded, then applied by Yardmaster.",
			CLI:     []string{"conductor", "decision", "respond", "dr-N", "--choice", "N"},
			Input:   bodySchema(crew.Response{}),
			Bounds:  map[string]int{"body": maxDecisionAnswerBody, "reply": maxDecisionReply}},
		{Name: "decision_cancel", Scope: "admin", Method: http.MethodPost, Path: "/api/decisions/{did}/cancel", needs: needLauncher, Receipt: "ledger", Revision: "decision",
			Summary: "Cancel an open or answered decision with why: its tasks cancelled with it, or released when release says so.",
			CLI:     []string{"conductor", "decision", "cancel", "dr-N", "--why", "TEXT"},
			Input:   bodySchema(decisionCancelBody{}),
			Bounds:  map[string]int{"body": maxDecisionAnswerBody}},
		{Name: "decision_comment", Scope: "admin", Method: http.MethodPost, Path: "/api/decisions/{did}/comment", needs: needLauncher, Receipt: "ledger", Revision: "decision",
			Summary: "Comment on a decision, or ask its live asking attempt for more information (more_info, typed into its session).",
			Input:   bodySchema(decisionCommentBody{}),
			Bounds:  map[string]int{"body": maxDecisionAnswerBody}},
		{Name: "decision_route", Scope: "admin", Method: http.MethodPost, Path: "/api/decisions/{did}/route", needs: needLauncher, Receipt: "ledger", Revision: "decision",
			Summary: "Route a decision to a destination: under local/1, the operator.",
			Input:   bodySchema(decisionRouteBody{}),
			Bounds:  map[string]int{"body": maxDecisionAnswerBody}},
		{Name: "ticket_create", Scope: "admin", Method: http.MethodPost, Path: "/api/dispatch/tickets", needs: needLauncher, Receipt: "dispatch",
			Summary: "Make a Dispatch ticket in a project.",
			CLI:     []string{"conductor", "dispatch", "create", "--project", "KEY", "--title", "TITLE"},
			Input:   bodySchema(ticketCreateBody{}),
			Bounds:  map[string]int{"body": maxTicketBody, "reply": MaxTicketReply}},
		{Name: "ticket_edit", Scope: "admin", Method: http.MethodPost, Path: "/api/dispatch/tickets/{key}/edit", needs: needLauncher, Receipt: "dispatch", Revision: "ticket",
			Summary: "Change a ticket's fields in one operation, against the revision read (expectedRev).",
			CLI:     []string{"conductor", "dispatch", "edit", "KEY-n"},
			Input:   bodySchema(ticketEditBody{}),
			Bounds:  map[string]int{"body": maxTicketBody, "reply": MaxTicketReply}},
		{Name: "ticket_comment_admin", Scope: "admin", Method: http.MethodPost, Path: "/api/dispatch/tickets/{key}/comments", needs: needLauncher, Receipt: "dispatch", Revision: "ticket",
			Summary: "Comment on a ticket as the person, against the ticket's revision read (expectedRev).",
			CLI:     []string{"conductor", "dispatch", "comment", "KEY-n", "TEXT"},
			Input:   bodySchema(commentBody{}),
			Bounds:  map[string]int{"body": maxBody, "comment": dispatch.MaxCommentBody}},
		{Name: "ticket_link", Scope: "admin", Method: http.MethodPost, Path: "/api/dispatch/tickets/{key}/links", needs: needLauncher, Receipt: "dispatch", Revision: "ticket",
			Summary: "Link a ticket to a delivery, a run or an external page, against the ticket's revision read (expectedRev).",
			CLI:     []string{"conductor", "dispatch", "link", "KEY-n", "--kind", "delivery|run|external"},
			Input:   bodySchema(linkBody{}),
			Bounds:  map[string]int{"body": maxBody}},
		{Name: "crew_create", Scope: "admin", Method: http.MethodPost, Path: "/api/crews", needs: needLauncher,
			Summary: "Save a crew: its members, and for a managed run its tasks and checks. Saving launches nothing.",
			Input:   bodySchema(crewInput{}),
			Bounds:  map[string]int{"body": maxCrewBody, "crew": crew.MaxEncoded}},
		{Name: "crew_launch", Scope: "admin", Method: http.MethodPost, Path: "/api/crews/{id}/launch", needs: needLauncher,
			Summary: "Launch a saved crew as a run.",
			CLI:     []string{"conductor", "up", "CREW"},
			Input:   bodySchema(launchBody{}),
			Bounds:  map[string]int{"body": maxLaunchBody}},
		// Brakeman's desk, the admin's (brakeman.go).
		{Name: "brakeman_status", Scope: "admin", Method: http.MethodGet, Path: "/api/brakeman", needs: needLauncher,
			Summary: "Brakeman's configuration, the turn in progress, the queue and today's turns.",
			Input:   map[string]any{"type": "object", "description": "no body"}},
		{Name: "brakeman_config", Scope: "admin", Method: http.MethodPut, Path: "/api/brakeman/config", needs: needLauncher,
			Summary: "Save Brakeman's configuration against the revision read (rev); off cancels the turn in progress and the queue.",
			Input:   bodySchema(brakemanConfigBody{}),
			Bounds:  map[string]int{"body": brakeman.MaxConfigDoc}},
		{Name: "brakeman_ask", Scope: "admin", Method: http.MethodPost, Path: "/api/brakeman/requests", needs: needLauncher, Receipt: "brakeman",
			Summary: "Ask Brakeman: a request of a kind, with subjects, a note and a since; the same opId again answers it.",
			CLI:     []string{"conductor", "brakeman", "ask", "KIND"},
			Input:   bodySchema(brakemanAskBody{}),
			Bounds:  map[string]int{"body": brakeman.MaxNote + 8<<10, "queue": brakemanQueue}},
		{Name: "brakeman_requests", Scope: "admin", Method: http.MethodGet, Path: "/api/brakeman/requests", needs: needLauncher,
			Summary: "Brakeman's requests, newest first, a page at a time.",
			CLI:     []string{"conductor", "brakeman", "list"},
			Input:   map[string]any{"type": "object", "description": "no body; ?after= and ?limit= (at most 100)"}},
		{Name: "brakeman_show", Scope: "admin", Method: http.MethodGet, Path: "/api/brakeman/requests/{id}", needs: needLauncher,
			Summary: "A Brakeman request whole, with its brief.",
			CLI:     []string{"conductor", "brakeman", "show", "br-n"},
			Input:   map[string]any{"type": "object", "description": "no body"},
			Bounds:  map[string]int{"reply": maxBrakemanRequestReply}},
		{Name: "brakeman_refs", Scope: "admin", Method: http.MethodGet, Path: "/api/brakeman/requests/{id}/refs", needs: needLauncher,
			Summary: "A brief's refs as resolved: what the turn read of each and what it was as the brief was kept.",
			Input:   map[string]any{"type": "object", "description": "no body"},
			Bounds:  map[string]int{"reply": brakeman.MaxRefsDoc}},
		{Name: "brakeman_make", Scope: "admin", Method: http.MethodPost, Path: "/api/brakeman/requests/{id}/items/{i}/make", needs: needLauncher, Receipt: "dispatch",
			Summary: "Make a brief's draft ticket a ticket, or its draft crew a saved crew (never launched), as you edited it; the same opId again answers it.",
			Input:   map[string]any{"type": "object", "description": "{opId, by?, edit?}: your id for this make (1 to 32 of A-Z a-z 0-9 . _ ~ -), your display name, and the draft as you edited it, in the draft's own shape"},
			Bounds:  map[string]int{"body": maxMakeBody}},
		{Name: "brakeman_cancel", Scope: "admin", Method: http.MethodPost, Path: "/api/brakeman/requests/{id}/cancel", needs: needLauncher,
			Summary: "Cancel a Brakeman request: a queued one leaves the queue, a turn's run is stopped after the write.",
			Input:   bodySchema(brakemanCancelBody{})},
	}
})

// observeWhats is what the observe tool reads, each an observe_<what>
// operation of the table.
var observeWhats = []string{"changes", "status", "screen", "overlaps", "briefs", "runs", "run", "task", "steps", "log", "command", "messages", "candidates", "projects", "tickets", "ticket", "history", "comments", "plans", "sessions", "touched"}

// observeOp is one read of a Brakeman turn's: observe_<what>, its route,
// its query's parameters by name and its path's, and its answer's bound:
// maxObserveReply for Brakeman's own reads, a shared read's its route's
// own (sharedReadReply), else maxTurnReply, which a turn's read of any
// shared route is held to.
func observeOp(what, path, summary string, query ...string) Operation {
	bound := sharedReadReply(http.MethodGet + " " + path)
	if bound == 0 {
		bound = maxTurnReply
	}
	if strings.HasPrefix(path, "/api/brakeman/") {
		bound = maxObserveReply
	}
	props := map[string]any{}
	for _, m := range regexp.MustCompile(`\{([a-z]+)\}`).FindAllStringSubmatch(path, -1) {
		props[m[1]] = mcpString("the path's " + m[1])
	}
	for _, q := range query {
		props[q] = mcpString("?" + q + "=")
	}
	return Operation{Name: "observe_" + what, Scope: "observe", Method: http.MethodGet, Path: path, needs: needLauncher,
		Summary: summary, Input: mcpObject(props), Bounds: map[string]int{"reply": bound}}
}

// actOperations is the operations a grant may name that this build
// serves (actServed), in the closed list's order: the act tool's enum.
func actOperations() []string {
	var out []string
	for _, op := range brakeman.Grantable {
		if actServed(op) == nil {
			out = append(out, op)
		}
	}
	return out
}

// actRouteOf is the path of the route the granted operation op is sent
// to (actRoutes), "" for one this build serves no route of.
func actRouteOf(op string) string {
	for pattern, ar := range actRoutes {
		if ar.op == op {
			_, path, _ := strings.Cut(pattern, " ")
			return path
		}
	}
	return ""
}

// actEntry is a granted operation as the guide lists it: its own route
// (actRoutes) with the body that route decodes, its bound, the Dispatch
// receipt it answers with and the ticket's revision it checks.
func actEntry(op string, body any, summary string) Operation {
	var bound int64
	for _, ar := range actRoutes {
		if ar.op == op {
			bound = ar.bound
		}
	}
	return Operation{Name: op, Scope: "act", Method: http.MethodPost, Path: actRouteOf(op), needs: needLauncher, Receipt: "dispatch", Revision: "ticket",
		Summary: summary, Input: bodySchema(body), Bounds: map[string]int{"body": int(bound)}}
}

// runCommandOp is a run's command, with its receipt and revision.
func runCommandOp(name, summary, suffix string) Operation {
	return Operation{Name: name, Scope: "admin", Method: http.MethodPost, Path: "/api/runs/{run}" + suffix, Receipt: "ledger", Revision: "run", needs: needLauncher,
		Summary: summary, Input: bodySchema(crew.Command{}), Bounds: map[string]int{"body": maxBody}}
}

// taskCommandOp is a task's command, with its receipt and revision.
func taskCommandOp(name, summary, suffix string) Operation {
	return runCommandOp(name, summary, "/tasks/{task}"+suffix)
}

// bodySchema is the JSON Schema of a route's body, read from the type it
// decodes the way encoding/json reads it: its fields by their JSON names;
// an embedded struct's exported fields promoted among them, an unexported
// struct type's too (dispatchOp's opId, expectedRev and byName); a nested
// struct an object of its own fields and a list an array of its
// element's schema; a field without omitempty or omitzero required; and,
// as DisallowUnknownFields refuses one at every depth, no other property.
// TestEachBodySchemaIsWhatItsRouteDecodes holds it to encoding/json.
func bodySchema(v any) map[string]any { return structSchema(reflect.TypeOf(v)) }

// structSchema is a struct type's object schema.
func structSchema(t reflect.Type) map[string]any {
	props, required, depth := map[string]any{}, map[string]bool{}, map[string]int{}
	schemaFields(t, 0, props, required, depth)
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	var req []string
	for name, ok := range required {
		if ok {
			req = append(req, name)
		}
	}
	if len(req) > 0 {
		slices.Sort(req)
		out["required"] = req
	}
	return out
}

// schemaFields adds t's fields at depth d (an embedded struct's one
// deeper) to props: a name already there at a depth no greater keeps its
// field, as encoding/json's shallower field wins.
func schemaFields(t reflect.Type, d int, props map[string]any, required map[string]bool, depth map[string]int) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		// encoding/json promotes an embedded struct's exported fields
		// whether or not its type is exported; only a tag names it.
		if f.Anonymous && name == "" && ft.Kind() == reflect.Struct {
			schemaFields(ft, d+1, props, required, depth)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if have, ok := depth[name]; ok && have <= d {
			continue
		}
		depth[name] = d
		props[name] = valueSchema(f.Type)
		o := strings.Split(opts, ",")
		required[name] = !slices.Contains(o, "omitempty") && !slices.Contains(o, "omitzero")
	}
}

// valueSchema is the schema of a field's value: a struct's object, a
// list's array of its element, else its JSON type.
func valueSchema(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch typ := jsonType(t); {
	case typ == "array":
		return map[string]any{"type": typ, "items": valueSchema(t.Elem())}
	case typ == "object" && t.Kind() == reflect.Struct:
		return structSchema(t)
	default:
		return map[string]any{"type": typ}
	}
}

// textMarshaler is encoding.TextMarshaler's type.
var textMarshaler = reflect.TypeFor[encoding.TextMarshaler]()

// jsonType is the JSON Schema type a Go type encodes as.
func jsonType(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == reflect.TypeFor[time.Time]():
		return "string"
	case t.Kind() == reflect.Struct && (t.Implements(textMarshaler) || reflect.PointerTo(t).Implements(textMarshaler)):
		// encoding/json writes it as its text (a Brakeman ref).
		return "string"
	case t == reflect.TypeFor[json.RawMessage]():
		return "object"
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8:
		// encoding/json writes []byte as its base64 text.
		return "string"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	}
	return "object"
}

// guideField is one field of a receipt, as the receipts section names it.
type guideField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Optional bool   `json:"optional,omitempty"`
}

// receiptFields is a receipt type's fields, read from the type.
func receiptFields(t reflect.Type) []guideField {
	var out []guideField
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		out = append(out, guideField{Name: name, Type: jsonType(f.Type), Optional: strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero")})
	}
	return out
}

// guideCaller is who reads the guide and what reaches it.
type guideCaller struct {
	// Kind is admin or session.
	Kind    string `json:"kind"`
	Session string `json:"session,omitempty"`
	// Kinds are a session's: plain (in no run), run_member, attempt (of a
	// managed run), ticket_member (of a run linked to a ticket).
	Kinds  []string `json:"kinds,omitempty"`
	Run    string   `json:"run,omitempty"`
	Member string   `json:"member,omitempty"`
	// Scopes are the operations' scopes it reaches.
	Scopes []string `json:"scopes"`
	// local says the session is a server session (not hosted); managed
	// that its run is a managed one; brakeman that it is a Brakeman turn's.
	local, managed, brakeman bool
	// acts is a Brakeman turn's operations granted, the snapshot its turn
	// began under and the configuration now together (grantedActs): the
	// act scope lists the act tool and these alone.
	acts []string
	// budget is a Brakeman turn's, the snapshot its turn began under: the
	// overview's limits name its reads and its minutes. Zero when the
	// request cannot be read.
	budget brakeman.Budget
	// visible is what a Brakeman turn may see, its snapshot and the
	// configuration now together, as its reads are checked (refGrant):
	// an operation whose classes it sees none of is refused it (403
	// not_visible), and the guide lists it not. Nothing when the records
	// cannot be read.
	visible brakeman.Visibility
}

// adminCaller is the workbench token's: every scope.
func adminCaller() guideCaller {
	return guideCaller{Kind: "admin", Scopes: []string{"admin", "session", "task", "ticket", "observe", "act"}}
}

// sessionCaller is d's: a session, a run's member, an attempt, a member
// of a run a live ticket holds.
func (s *Server) sessionCaller(ctx context.Context, d session.Driver) guideCaller {
	id := d.Info().ID
	c := guideCaller{Kind: "session", Session: id, Scopes: []string{"session"}}
	_, c.local = d.(*session.Local)
	run, member, inRun := s.runs.MemberOf(id)
	if !inRun {
		c.Kinds = append(c.Kinds, "plain")
	} else {
		c.Kinds = append(c.Kinds, "run_member")
		c.Run, c.Member = run, member
		c.managed = s.runs.InManagedRun(id)
	}
	if _, _, _, ok := s.runs.AttemptOf(id); ok {
		c.Kinds = append(c.Kinds, "attempt")
		c.Scopes = append(c.Scopes, "task")
	}
	if ref := d.Info().Crew; ref != nil && ref.Brakeman != nil {
		// A Brakeman turn reads its request and submits its brief; it
		// reaches no ticket through its run, linked or not.
		c.Kinds = append(c.Kinds, "brakeman")
		c.Scopes = append(c.Scopes, "observe")
		c.brakeman = true
		if c.acts, c.budget, c.visible = s.turnGrant(ref.Brakeman.Request); len(c.acts) > 0 {
			c.Scopes = append(c.Scopes, "act")
		}
		return c
	}
	if inRun && s.dispatch != nil && !s.cfg.Switchyard.Enabled {
		if _, found, err := linkedTicket(ctx, s.dispatch, run); err == nil && found {
			c.Kinds = append(c.Kinds, "ticket_member")
			c.Scopes = append(c.Scopes, "ticket")
		}
	}
	return c
}

// turnGrant is the operations the request id's turn is granted and what
// it may see, its snapshot and the configuration now together (as its
// routes check them: grantedActs, refGrant); none of either when the
// records cannot be read or the request is closed (the guide then lists
// nothing they gate, and the routes refuse it as ever); and the budget
// its snapshot holds, zero when the request cannot be read.
func (s *Server) turnGrant(id string) ([]string, brakeman.Budget, brakeman.Visibility) {
	d := s.brakeman
	if d == nil {
		return nil, brakeman.Budget{}, brakeman.Visibility{}
	}
	req, err := d.st.Get(id)
	if err != nil {
		return nil, brakeman.Budget{}, brakeman.Visibility{}
	}
	if !req.Open() {
		return nil, req.Config.Budget, brakeman.Visibility{}
	}
	cfg, err := d.st.Config()
	if err != nil {
		return nil, req.Config.Budget, brakeman.Visibility{}
	}
	return grantedActs(req.Config.Permissions, cfg.Permissions), req.Config.Budget, bothVisible(req.Config.Visibility, cfg.Visibility)
}

// admits reports whether the caller c may call op on this server now:
// the scope it reaches and what the server allows (reaches), then what
// the operation's route refuses this caller for, from the same facts the
// route reads: the admin an operation only a session's own token calls
// (opAgentOnly: 403 agent_only), a Brakeman turn one whose classes it
// sees none of (opClasses, seesAny: 403 not_visible). The guide's
// operations section and the overview's may list are made by it, so an
// operation a caller would be refused is never listed to it, and one it
// may call never left out.
func (s *Server) admits(c guideCaller, op Operation) bool {
	if !s.reaches(c, op) {
		return false
	}
	if c.Kind == "admin" && opAgentOnly(op.Name) {
		return false
	}
	if c.brakeman && !seesAny(opClasses(op), c.visible.Has) {
		return false
	}
	return true
}

// opAgentOnly reports whether the operation named takes only the
// session's own token (Operation.agentOnly): its route asks this before
// it refuses the admin's, as the guide does before it lists it.
func opAgentOnly(name string) bool {
	ops := operationsTable()
	i := slices.IndexFunc(ops, func(o Operation) bool { return o.Name == name })
	return i >= 0 && ops[i].agentOnly
}

// actClasses are what an act's route needs a turn to see (admitAct): a
// grant gives no read.
var actClasses = []string{brakeman.ClassDispatch}

// ownReadClasses are the classes Brakeman's own reads (observeRoute) need
// a turn to see one of, by path: turnRead refuses a turn that sees none
// (403 not_visible) before the handler's own finer checks (a status of a
// session needs sessions, of a run runs).
var ownReadClasses = map[string][]string{
	"/api/brakeman/screen":   {brakeman.ClassTerminals},
	"/api/brakeman/status":   {brakeman.ClassRuns, brakeman.ClassSessions},
	"/api/brakeman/overlaps": {brakeman.ClassRuns, brakeman.ClassSessions},
}

// opClasses are the visibility classes op reads, one of which a Brakeman
// turn must see for its route to take it: a shared read's (its readSpec's
// class), Brakeman's own reads' (ownReadClasses), an act's (actClasses);
// nil for an operation no class gates.
func opClasses(op Operation) []string {
	switch {
	case op.Scope == "act":
		return actClasses
	case op.Scope != "observe" || op.Method != http.MethodGet:
		return nil
	}
	if spec, ok := sharedReadSpec(op.Method + " " + op.Path); ok && spec.class != "" {
		return []string{spec.class}
	}
	return ownReadClasses[op.Path]
}

// seesAny reports whether a turn that sees what sees answers sees one of
// classes; true for none.
func seesAny(classes []string, sees func(string) bool) bool {
	return len(classes) == 0 || slices.ContainsFunc(classes, sees)
}

// reaches reports whether the caller is given op on this server.
func (s *Server) reaches(c guideCaller, op Operation) bool {
	if !slices.Contains(c.Scopes, op.Scope) {
		return false
	}
	// A turn is given the act tool and the operations its grant holds,
	// never another; the admin sees them all.
	if op.Scope == "act" && c.Kind == "session" && op.Name != "act" && !slices.Contains(c.acts, op.Name) {
		return false
	}
	if op.needs&needLauncher != 0 && s.cfg.Switchyard.Enabled {
		return false
	}
	// A switchyard's sessions are all hosted, whose folders are on their
	// own machines: self-service reaches none of them.
	if op.needs&needSelfService != 0 && (!s.cfg.SelfService() || s.cfg.Switchyard.Enabled || (c.Kind == "session" && !c.local)) {
		return false
	}
	// A session in a run is not given what only one in none may do, nor
	// one in none what needs a run, nor one in a managed run what only
	// one in another may do; the admin is given all three.
	if c.Kind == "session" {
		inRun := slices.Contains(c.Kinds, "run_member")
		if (op.needs&needRun != 0 && !inRun) || (op.needs&needNoRun != 0 && inRun) || (op.needs&needUnmanaged != 0 && c.managed) || (op.needs&needNotBrakeman != 0 && c.brakeman) {
			return false
		}
	}
	return true
}

// guideLimit is one named bound that refuses a body.
type guideLimit struct {
	Name string `json:"name"`
	Min  int    `json:"min,omitempty"`
	Max  int    `json:"max,omitempty"`
	// Default is what 0 takes, for a limit that has one.
	Default int    `json:"default,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	Unit    string `json:"unit,omitempty"`
	About   string `json:"about"`
}

// guideLimits is the limits section, each read from the constant that
// enforces it.
func guideLimits() []guideLimit {
	b := crew.TheBounds()
	return []guideLimit{
		{Name: "task_id", Pattern: b.TaskIDPattern, About: "a task's and a check's id"},
		{Name: "crew_members", Max: b.Members, About: "members a crew"},
		{Name: "member_name", Max: b.MemberName, Pattern: b.MemberNamePattern, Unit: "characters", About: "a crew member's name"},
		{Name: "crew_tasks", Max: b.Tasks, About: "tasks a managed crew"},
		{Name: "task_checks", Max: b.ChecksPerTask, About: "checks a task"},
		{Name: "check_timeout", Min: b.CheckTimeoutMin, Max: b.CheckTimeoutMax, Default: b.CheckTimeoutDefault, Unit: "seconds", About: "a check's timeoutSeconds; 0 takes the default"},
		{Name: "result_summary", Max: b.Summary, Unit: "characters", About: "a task result's summary"},
		{Name: "result_issues", Max: b.Issues, About: "a task result's issues"},
		{Name: "result_findings", Max: b.Findings, About: "a review's findings"},
		{Name: "message", Max: b.MessageChars, Unit: "characters", About: "a message to another member"},
		{Name: "inbox", Max: b.Inbox, About: "messages a member keeps not acknowledged"},
		{Name: "report_message", Max: session.MaxAttentionMessage, Unit: "bytes", About: "a report's, a state's or a question's message, cut past it"},
		{Name: "comment", Max: dispatch.MaxCommentBody, Unit: "bytes", About: "a ticket comment's Markdown"},
		{Name: "ticket_title", Max: dispatch.MaxTicketTitle, Unit: "characters", About: "a ticket's title"},
		{Name: "ticket_description", Max: dispatch.MaxTicketDescription, Unit: "bytes", About: "a ticket's description"},
		{Name: "task_page", Max: taskPage, Unit: "bytes", About: "a page of a task file"},
		{Name: "self_crews", Max: selfCrewsPerHour, About: "crews a session forms an hour"},
		{Name: "self_links", Max: selfLinksPerDay, About: "view links a session mints a day"},
		{Name: "self_opens", Max: selfOpensPerMinute, About: "links a session opens a minute"},
		{Name: "brief", Max: brakeman.MaxBrief, Unit: "bytes", About: "a Brakeman brief as kept (store.Encode's form)"},
		{Name: "brief_items", Max: brakeman.MaxItems, About: "items a brief"},
		{Name: "brief_refs", Max: brakeman.MaxBriefRefs, About: "distinct refs a brief, across its items"},
		{Name: "brief_title", Max: brakeman.MaxTitle, Unit: "characters", About: "a brief's title"},
		{Name: "brief_summary", Max: brakeman.MaxSummary, Unit: "bytes", About: "a brief's summary, Brakeman's reading"},
		{Name: "brief_draft_tickets", Max: brakeman.MaxDraftTicket, About: "draft tickets a brief"},
		{Name: "brakeman_queue", Max: brakemanQueue, About: "Brakeman requests waiting"},
		{Name: "body", Max: maxBody, Unit: "bytes", About: "a JSON body, unless its route says more"},
	}
}

// guideStore is the state of the ledger or of Dispatch: owned (the
// ledger) or open (Dispatch), read_only with why, or absent.
type guideStore struct {
	State string `json:"state"`
	Why   string `json:"why,omitempty"`
}

// guideAgent is a catalog agent as the host section lists it.
type guideAgent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Adapter   string `json:"adapter,omitempty"`
	Installed bool   `json:"installed"`
	// Version is what its probe found, when the server knows it already.
	Version string `json:"version,omitempty"`
}

// guideHost is the host section.
type guideHost struct {
	OS           string     `json:"os"`
	Arch         string     `json:"arch"`
	Switchyard   bool       `json:"switchyard"`
	ManagedCrews bool       `json:"managedCrews"`
	SelfService  bool       `json:"selfService"`
	Ledger       guideStore `json:"ledger"`
	Dispatch     guideStore `json:"dispatch"`
	// ChecksMaxConcurrent is checks.maxConcurrent.
	ChecksMaxConcurrent int `json:"checksMaxConcurrent"`
	// Extensions is the kinds of provider GET /api/capabilities names
	// (C9), each once: what this build adds beside the open-source
	// feature set, [] for a plain build. The providers section has the
	// rows.
	Extensions []string `json:"extensions"`
	// Sessions is how many sessions the server lists, for the admin.
	Sessions *int         `json:"sessions,omitempty"`
	Agents   []guideAgent `json:"agents"`
	// MoreAgents counts the agents past maxGuideAgents.
	MoreAgents int `json:"moreAgents,omitempty"`
}

// cutText is s cut to at most n bytes on a rune boundary.
func cutText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// guideHostSection is the host section for the caller, caps C9's
// answer: a session caller sees no reasons and no session count, and
// only the agents it could name (with self-service, on a server that
// launches).
func (s *Server) guideHostSection(ctx context.Context, c guideCaller, caps capabilitiesReply) guideHost {
	admin := c.Kind == "admin"
	h := guideHost{OS: runtime.GOOS, Arch: runtime.GOARCH, Switchyard: s.cfg.Switchyard.Enabled, ManagedCrews: s.cfg.ManagedCrews(),
		SelfService: s.cfg.SelfService(), ChecksMaxConcurrent: s.cfg.CheckConcurrency(), Extensions: extensionKinds(caps), Agents: []guideAgent{}}
	h.Ledger = guideStore{State: "absent"}
	if s.ledger != nil {
		h.Ledger.State = "owned"
		if ro, why := s.ledger.ReadOnly(); ro {
			h.Ledger = guideStore{State: "read_only", Why: why}
		}
	}
	h.Dispatch = guideStore{State: "absent"}
	if s.dispatch != nil {
		h.Dispatch.State = "open"
		if ro, why := s.dispatch.ReadOnly(); ro {
			h.Dispatch = guideStore{State: "read_only", Why: why}
		}
	}
	if !admin {
		h.Ledger.Why, h.Dispatch.Why = "", ""
	} else {
		h.Ledger.Why, h.Dispatch.Why = cutText(h.Ledger.Why, 500), cutText(h.Dispatch.Why, 500)
		n := s.registry.Count()
		h.Sessions = &n
	}
	if s.cfg.Switchyard.Enabled || (!admin && (!s.cfg.SelfService() || !c.local)) {
		return h
	}
	s.catalogMu.Lock()
	cat := s.catalog
	s.catalogMu.Unlock()
	list := cat.List()
	if len(list) > maxGuideAgents {
		h.MoreAgents = len(list) - maxGuideAgents
		list = list[:maxGuideAgents]
	}
	programs := make([]string, 0, len(list))
	for _, a := range list {
		if len(a.Command) > 0 {
			programs = append(programs, a.Command[0])
		}
	}
	lctx, cancel := context.WithTimeout(ctx, guideLookupWait)
	defer cancel()
	answers := s.lookups.warmPaths(lctx, programs)
	for _, a := range list {
		g := guideAgent{ID: a.ID, Name: a.Name, Adapter: a.Adapter}
		if len(a.Command) > 0 {
			ans := answers[a.Command[0]]
			g.Installed = ans.installed
			if p := agents.ProbeFor(a.Adapter); ans.path != "" && a.Probed() && p != nil {
				if id, ok := s.probes.cached(resolvedCommand(a, ans.path), a.Adapter, a.Adapter, *p, a.Env); ok && id.Identified {
					g.Version = cutText(id.Version, 64)
				}
			}
		}
		h.Agents = append(h.Agents, g)
	}
	return h
}

// guideReceipt is a receipt's format.
type guideReceipt struct {
	Name   string       `json:"name"`
	About  string       `json:"about"`
	Fields []guideField `json:"fields"`
}

// guideReceipts is the receipts section: the formats a caller meets,
// read from their types.
func guideReceipts() []guideReceipt {
	return []guideReceipt{
		{Name: "brakeman", About: "a Brakeman request's, kept under its opId: the same opId again answers the same request, and the same brief's opId again the kept brief", Fields: receiptFields(reflect.TypeFor[brakeman.Request]())},
		{Name: "ledger", About: "a run command's (pause, unpause, cancel, retry, accept), kept in the ledger under its actor and commandId: the same command again answers the same receipt, repeat true; a decision's response and operation are kept there the same way, under responseId, commandId or opId", Fields: receiptFields(reflect.TypeFor[crew.Receipt]())},
		{Name: "dispatch", About: "a Dispatch operation's, kept under its actor and opId: the same opId again answers it, repeat true", Fields: receiptFields(reflect.TypeFor[dispatch.Receipt]())},
	}
}

// guideProviders is the providers section: GET /api/capabilities's
// answer (Program item C9), the same rows for the admin and for a
// session, and known, which the server always is now: a plain build's is
// known and empty. The guide reads the route's rows and nothing else of
// ext, and decides nothing from them (article 1): it names what is
// registered, never what a caller may do because of it.
type guideProviders struct {
	Known     bool            `json:"known"`
	Providers []capabilityRow `json:"providers"`
}

// guideProvidersOf is the providers section of C9's reply.
func guideProvidersOf(caps capabilitiesReply) guideProviders {
	return guideProviders{Known: true, Providers: caps.Providers}
}

// extensionKinds is the kinds C9's reply names, each once, in its order
// (the registry's): what the host section says this build adds beside
// the open-source feature set, never nil.
func extensionKinds(caps capabilitiesReply) []string {
	out := []string{}
	for _, r := range caps.Providers {
		if !slices.Contains(out, r.Kind) {
			out = append(out, r.Kind)
		}
	}
	return out
}

// guideOverview is the overview's content: who the caller is, what it
// may call (the operations section's names, each whole there) and the
// limits that bind it most (each whole in the limits section), the
// sections and their sizes, and the rules. A caller told only to call the
// guide learns there what it may do and within what, in a few lines.
type guideOverview struct {
	Caller guideCaller `json:"caller"`
	// May is the operations the caller may call, by name (an MCP tool's
	// name where it has one), in the table's order.
	May []string `json:"may"`
	// Limits are the headline bounds that bind the caller, one a line,
	// in words: its scopes' own, and a Brakeman turn's budget.
	Limits   []string           `json:"limits"`
	Sections []guideSectionSize `json:"sections"`
	Rules    []string           `json:"rules"`
}

// overviewLimitNames are the limits section's entries the overview names
// for a caller that reaches each scope, in this order, each once.
var overviewLimitNames = []struct {
	scope string
	names []string
}{
	{"admin", []string{"crew_members", "crew_tasks", "task_checks", "check_timeout", "ticket_title", "ticket_description"}},
	{"session", []string{"report_message", "self_crews", "self_links"}},
	{"task", []string{"message", "inbox", "result_summary", "result_issues"}},
	{"ticket", []string{"comment"}},
	{"observe", []string{"brief", "brief_items", "brief_refs", "brief_summary"}},
}

// overviewLimits is the headline bounds that bind c, in words: its
// scopes' entries of the limits section, a Brakeman turn's reads and
// minutes from the snapshot its turn began under, its acts a request
// with the act scope, and a JSON body's bound for everyone.
func overviewLimits(c guideCaller) []string {
	all := guideLimits()
	var out, seen []string
	add := func(name string) {
		if slices.Contains(seen, name) {
			return
		}
		if i := slices.IndexFunc(all, func(l guideLimit) bool { return l.Name == name }); i >= 0 {
			seen = append(seen, name)
			out = append(out, limitWords(all[i]))
		}
	}
	for _, g := range overviewLimitNames {
		if slices.Contains(c.Scopes, g.scope) {
			for _, n := range g.names {
				add(n)
			}
		}
	}
	if c.brakeman && c.budget.ReadsPerTurn > 0 {
		out = append(out, fmt.Sprintf("this turn: at most %d reads and %d minutes, then it ends", c.budget.ReadsPerTurn, c.budget.TurnMinutes))
	}
	if slices.Contains(c.Scopes, "act") {
		out = append(out, fmt.Sprintf("acts: at most %d a request, each only as the person granted it", brakeman.MaxActs))
	}
	add("body")
	return out
}

// limitWords is a limit in one line: its name, its bound and what it
// bounds.
func limitWords(l guideLimit) string {
	unit := ""
	if l.Unit != "" {
		unit = " " + l.Unit
	}
	switch {
	case l.Pattern != "":
		return fmt.Sprintf("%s: %s (%s)", l.Name, l.Pattern, l.About)
	case l.Min > 0:
		return fmt.Sprintf("%s: %d to %d%s (%s)", l.Name, l.Min, l.Max, unit, l.About)
	}
	return fmt.Sprintf("%s: at most %d%s (%s)", l.Name, l.Max, unit, l.About)
}

// guideSectionSize names a section and the size of its reply.
type guideSectionSize struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
}

// guideReplies is every reply of the guide for the caller, by section
// ("overview" among them), encoded: each section is computed from ops.
func (s *Server) guideReplies(ctx context.Context, c guideCaller, ops []Operation) (map[string][]byte, error) {
	return encodeGuide(c, s.guideContent(ctx, c, ops))
}

// guideContent is each section's content for the caller, by its name.
func (s *Server) guideContent(ctx context.Context, c guideCaller, ops []Operation) map[string]any {
	// C9's answer, read once for both sections that name it.
	caps := s.capabilities()
	reached := []Operation{}
	for _, op := range ops {
		if s.admits(c, op) {
			op.Description = ""
			if op.guideInput != nil {
				op.Input = op.guideInput
			}
			reached = append(reached, op)
		}
	}
	return map[string]any{
		"operations": reached,
		"limits":     guideLimits(),
		"host":       s.guideHostSection(ctx, c, caps),
		"receipts":   guideReceipts(),
		"providers":  guideProvidersOf(caps),
		"briefs":     guideBriefsSection(),
	}
}

// guideBriefs is the briefs section: a Brakeman brief's shape in words
// and as its JSON Schema (brief_submit's input's brief), both made from
// internal/brakeman's kind table, and the ref forms. It is the same for
// every caller: what a brief is, whoever reads it.
type guideBriefs struct {
	Schema     string             `json:"schema"`
	Words      []string           `json:"words"`
	Refs       []brakeman.RefForm `json:"refs"`
	JSONSchema map[string]any     `json:"jsonSchema"`
}

// guideBriefsSection is the briefs section.
func guideBriefsSection() guideBriefs {
	return guideBriefs{Schema: brakeman.BriefSchema, Words: brakeman.Words(), Refs: brakeman.RefForms(), JSONSchema: briefSubmitSchema()}
}

// briefSubmitSchema is the brief's JSON Schema as brief_submit's input
// gives it.
func briefSubmitSchema() map[string]any {
	op := operationsTable()[slices.IndexFunc(operationsTable(), func(o Operation) bool { return o.Name == "brief_submit" })]
	return op.Input["properties"].(map[string]any)["brief"].(map[string]any)
}

// encodeGuide is each section's reply and the overview naming their
// sizes, for the caller.
func encodeGuide(c guideCaller, content map[string]any) (map[string][]byte, error) {
	out := make(map[string][]byte, len(content)+1)
	ov := guideOverview{Caller: c, May: []string{}, Limits: overviewLimits(c), Rules: guideRules}
	if ops, ok := content["operations"].([]Operation); ok {
		for _, op := range ops {
			ov.May = append(ov.May, op.Name)
		}
	}
	for _, name := range guideSections {
		b, err := guideReply(name, content[name])
		if err != nil {
			return nil, err
		}
		out[name] = b
		ov.Sections = append(ov.Sections, guideSectionSize{Name: name, Bytes: len(b)})
	}
	b, err := guideReply("overview", ov)
	if err != nil {
		return nil, err
	}
	out["overview"] = b
	return out, nil
}

// guideReply is one reply: the guide's version, the build's, the
// section's name and its content under that name, with HTML escaping
// off so its bound is measured as written.
func guideReply(name string, content any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(map[string]any{"guide": guideVersion, "version": version.Version, "commit": version.Commit, "section": name, name: content})
	return b.Bytes(), err
}

// handleGuide answers the admin's guide: GET /api/guide?section=.
func (s *Server) handleGuide(w http.ResponseWriter, r *http.Request) {
	s.serveGuide(w, r, adminCaller())
}

// handleSessionGuide answers a session's guide, for its own agent token
// or the admin's: GET /api/sessions/{id}/guide?section=.
func (s *Server) handleSessionGuide(w http.ResponseWriter, r *http.Request) {
	d, ok := s.taskSession(w, r, false)
	if !ok {
		return
	}
	s.serveGuide(w, r, s.sessionCaller(r.Context(), d))
}

// serveGuide answers the overview, or the section ?section= names: 404
// no_section for another name, 400 invalid_request for another
// parameter.
func (s *Server) serveGuide(w http.ResponseWriter, r *http.Request, c guideCaller) {
	q, err := readQuery(r, "section")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	section, err := q.one("section")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if section == "" {
		section = "overview"
	}
	if section != "overview" && !slices.Contains(guideSections, section) {
		writeError(w, http.StatusNotFound, "no_section", "the guide's sections are "+strings.Join(guideSections, ", ")+"; without section, the overview")
		return
	}
	replies, err := s.guideReplies(r.Context(), c, operationsTable())
	if err != nil {
		s.log.Warn("guide", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "the guide could not be written")
		return
	}
	b := replies[section]
	limit := maxGuideSection
	if section == "overview" {
		limit = maxGuideOverview
	}
	if len(b) > limit {
		// TestEachSectionIsBounded holds every section to its bound with
		// every entry at its largest: this is a bug, said rather than sent.
		s.log.Warn("guide section past its bound", "section", section, "bytes", len(b))
		writeError(w, http.StatusInternalServerError, "guide_bound", "the guide's "+section+" is past its bound")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
