package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
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
	// ticket); Brakeman's scopes come with its own entries.
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
	// (a command's expectedRevision, above 0) or "ticket" (Dispatch's
	// expectedRev); "" for a route that decodes no revision (a run's stop
	// and resume), which a proposal never gets a button for.
	Revision string `json:"revision,omitempty"`
	// Bounds are the body's and the reply's limits, in bytes.
	Bounds map[string]int `json:"bounds,omitempty"`
	// needs is what the server must allow for the operation to be listed.
	needs opNeeds
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
var guideSections = []string{"operations", "limits", "host", "receipts", "providers"}

// The guide's three lines, for every caller.
var guideRules = []string{
	"A fact needs evidence: name the record it rests on (a run, a task, a check receipt, a ticket, a session).",
	"Nothing an agent writes becomes work by itself: a person makes it work.",
	"A question goes to a person with ask.",
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
				"message": str("one line, at most 500 characters"),
				"url":     str("artifact: where it lives"),
				"to":      str("handoff: the member the work goes to"),
				"tool":    str("tool_use, tool_denied, error, file: the tool involved"),
				"op":      map[string]any{"type": "string", "enum": []string{"read", "edit", "write", "delete"}, "description": "file: what you did to it"},
				"path":    str("file: the file's path"),
			}, "event"),
			Bounds: map[string]int{"body": maxBody, "message": session.MaxAttentionMessage}},
		{Name: "set_state", MCP: "set_state", Scope: "session", Method: http.MethodPost, Path: sessions + "/attention",
			Summary:     "Say you are working or done, or clear the state; a question is ask's.",
			Description: "Tell the people watching that you are working, done, or clear the state; use ask for a question.",
			CLI:         []string{"conductor", "notify", "--state", "working|done|clear"},
			Input:       obj(map[string]any{"state": map[string]any{"type": "string", "enum": []string{"working", "done", "clear"}}, "message": str("one line")}, "state"),
			Bounds:      map[string]int{"body": maxBody, "message": session.MaxAttentionMessage}},
		{Name: "ask", MCP: "ask", Scope: "session", Method: http.MethodPost, Path: sessions + "/attention",
			Summary:     "Ask the people watching for a decision you cannot make: the session needs input until someone answers in the terminal; at most 6 choices.",
			Description: "Ask the people watching for a decision you cannot make yourself; the session shows as needing input until someone answers in the terminal, also once your turn has ended. With choices (at most 6, each at most 40 characters), name them in the terminal too: a click types the choice as a line. If you no longer need the answer, clear the question with set_state clear.",
			CLI:         []string{"conductor", "notify", "--ask", "--message", "TEXT", "--choices", "a|b"},
			Input:       obj(map[string]any{"message": str("the question, one line"), "choices": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 6}}, "message"),
			Bounds:      map[string]int{"body": maxBody, "message": session.MaxAttentionMessage}},
		{Name: "form_crew", MCP: "form_crew", Scope: "session", Method: http.MethodPost, Path: sessions + "/crew", needs: needLauncher | needSelfService | needNoRun,
			Summary:     "Form a crew around this session when the work splits into parts that run side by side: this session stays as one member. Twice an hour at most.",
			Description: "Form a crew around this session when the work splits into parts that can run side by side: you stay as the member named by self (its agentId and prompt may be empty), the others get sessions of their own. At most 2 a session; never for work you can finish alone.",
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
		{Name: "add_member", MCP: "add_member", Scope: "session", Method: http.MethodPost, Path: sessions + "/run/members", needs: needLauncher | needSelfService | needRun | needUnmanaged,
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
		{Name: "task_result", MCP: "task_result", Scope: "task", Method: http.MethodPost, Path: sessions + "/task/result", needs: needLauncher,
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
		// The guide itself.
		{Name: "guide", MCP: "guide", Scope: "session", Method: http.MethodGet, Path: sessions + "/guide",
			Summary:     "This guide, from the running server: the overview without a section, else the section named.",
			Description: "What this Conductor server lets you do here, from the server itself: without a section, an overview (the server's version, who you are and what you may do, the sections and their sizes); with one, that section: operations (what you may call, with its tool, command, route and input), limits, host, receipts or providers.",
			CLI:         []string{"conductor", "guide", "--section", "SECTION"},
			Input:       obj(map[string]any{"section": map[string]any{"type": "string", "enum": guideSections, "description": "the section to read; the overview when not given"}}),
			Bounds:      map[string]int{"overview": maxGuideOverview, "section": maxGuideSection}},
		{Name: "server_guide", Scope: "admin", Method: http.MethodGet, Path: "/api/guide",
			Summary: "The guide for the workbench token: every operation, the server's limits, host, receipts and providers.",
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
	}
})

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

// jsonType is the JSON Schema type a Go type encodes as.
func jsonType(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == reflect.TypeFor[time.Time]():
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
	// that its run is a managed one.
	local, managed bool
}

// adminCaller is the workbench token's: every scope.
func adminCaller() guideCaller {
	return guideCaller{Kind: "admin", Scopes: []string{"admin", "session", "task", "ticket"}}
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
	if inRun && s.dispatch != nil && !s.cfg.Switchyard.Enabled {
		if _, found, err := linkedTicket(ctx, s.dispatch, run); err == nil && found {
			c.Kinds = append(c.Kinds, "ticket_member")
			c.Scopes = append(c.Scopes, "ticket")
		}
	}
	return c
}

// reaches reports whether the caller is given op on this server.
func (s *Server) reaches(c guideCaller, op Operation) bool {
	if !slices.Contains(c.Scopes, op.Scope) {
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
		if (op.needs&needRun != 0 && !inRun) || (op.needs&needNoRun != 0 && inRun) || (op.needs&needUnmanaged != 0 && c.managed) {
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

// guideHostSection is the host section for the caller: a session caller
// sees no reasons and no session count, and only the agents it could
// name (with self-service, on a server that launches).
func (s *Server) guideHostSection(ctx context.Context, c guideCaller) guideHost {
	admin := c.Kind == "admin"
	h := guideHost{OS: runtime.GOOS, Arch: runtime.GOARCH, Switchyard: s.cfg.Switchyard.Enabled, ManagedCrews: s.cfg.ManagedCrews(),
		SelfService: s.cfg.SelfService(), ChecksMaxConcurrent: s.cfg.CheckConcurrency(), Agents: []guideAgent{}}
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
// read from their types. A Brakeman request's arrives with its routes.
func guideReceipts() []guideReceipt {
	return []guideReceipt{
		{Name: "ledger", About: "a run command's (pause, unpause, cancel, retry, accept), kept in the ledger under its actor and commandId: the same command again answers the same receipt, repeat true", Fields: receiptFields(reflect.TypeFor[crew.Receipt]())},
		{Name: "dispatch", About: "a Dispatch operation's, kept under its actor and opId: the same opId again answers it, repeat true", Fields: receiptFields(reflect.TypeFor[dispatch.Receipt]())},
	}
}

// guideProviders is the providers section: unknown until the server
// says which providers it has registered (Program item C9).
type guideProviders struct {
	Known     bool     `json:"known"`
	Providers []string `json:"providers"`
}

// guideOverview is the overview's content.
type guideOverview struct {
	Caller   guideCaller        `json:"caller"`
	Sections []guideSectionSize `json:"sections"`
	Rules    []string           `json:"rules"`
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
	reached := []Operation{}
	for _, op := range ops {
		if s.reaches(c, op) {
			op.Description = ""
			reached = append(reached, op)
		}
	}
	return map[string]any{
		"operations": reached,
		"limits":     guideLimits(),
		"host":       s.guideHostSection(ctx, c),
		"receipts":   guideReceipts(),
		"providers":  guideProviders{Providers: []string{}},
	}
}

// encodeGuide is each section's reply and the overview naming their
// sizes, for the caller.
func encodeGuide(c guideCaller, content map[string]any) (map[string][]byte, error) {
	out := make(map[string][]byte, len(content)+1)
	ov := guideOverview{Caller: c, Rules: guideRules}
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
