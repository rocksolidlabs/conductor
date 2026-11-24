# Claude Code stream-json fixtures

What `claude -p --input-format stream-json --output-format stream-json
--verbose --include-partial-messages --replay-user-messages` wrote, one JSON
object a line, for the driver's tests (`claude_test.go`).

## Recorded

Recorded from one real run of Claude Code 2.1.331 (`ClaudeMinVersion`), in
a throwaway git repository holding a two-line `notes.txt`, with one-line
prompts about that file. Seven messages were sent in all, over four
processes on one conversation (the later three resumed it with `--resume`).
Each message was written with a `uuid` of the form
`c0ffee00-0000-4000-8000-00000000000N` and `client_composed: true`; the
initialize request's id is written here as `conductor-init-1` and the
interrupt's as `conductor-interrupt-2`, the ids the driver gives them.

- `read-and-command.jsonl`: the start (the initialize answer, the init), a
  turn that reads `notes.txt` with the Read tool and says its first line
  (words, a tool step with its result, a result with usage), and a turn that
  runs `wc -l notes.txt`.
- `denied-and-command.jsonl`: a turn in `acceptEdits` mode whose first
  command the permission rules denied (`system/permission_denied`, an error
  result) and whose second ran.
- `edit.jsonl`: a turn in `acceptEdits` mode that changes a line with the
  Edit tool (a file edit with its structured patch: one line added, one
  removed).
- `mcp-and-interrupt.jsonl`: with two stub MCP servers of the run's own, a
  call the idle check ended after 30 seconds (an error result), a call that
  answered after 150 seconds (`tool_progress` heartbeats every 30), and a
  turn ended by the interrupt control request (its receipt, an aborted
  assistant message, an `error_during_execution` result).

Removed from every recorded line: every field that describes the account,
its organisation, its plan or its sign-in (the initialize answer's
`account`, `apiKeySource`, `credential_expires_at`, the fast mode and remote
control fields, every `rate_limit_event`), the person's own settings (their
hooks' events and output, commands, skills, agents, plugins, memory paths,
MCP servers other than the run's stubs, and tools other than a few built-in
ones), and the undocumented copies of each request (`wire_*`). Every path
of the machine is `/work` (the repository) or `/tools` (the stubs); every
id the service minted (session, message, tool call, request, event) is a
fixed made-up one of the same shape; every `timestamp` is one fixed time;
the process id is 12345; thinking signatures are `fixture-signature`.
Model names, usage and cost figures are as recorded.

## Hand-built

Built by hand from the shapes Claude Code's documentation gives
(https://code.claude.com/docs/en/agent-sdk/typescript.md, Message Types;
https://code.claude.com/docs/en/headless.md), not recorded:

- `hand-built-background.jsonl`: background work (`task_started`,
  `background_tasks_changed`, `task_updated`, `task_notification`, an
  ambient task). The run's 150-second MCP call was not moved to the
  background in print mode, so no recorded run showed these events.
- `hand-built-not-signed-in.jsonl`: a start whose first answer says the
  agent is not signed in (`error: "authentication_failed"`, a result with
  `is_error` and `api_error_status` 401). A real run of it would need the
  program signed out, which the run never did.
