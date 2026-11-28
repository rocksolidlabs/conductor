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
control fields, every `rate_limit_event`, and how the use was billed and
served: each `modelUsage` entry's `provider`, `costBasis` and
`contextWindow`, and the prompt cache's split by lifetime, `cache_creation`
and `cacheCreationEphemeral*`), the person's own settings (their
hooks' events and output, commands, skills, agents, plugins, memory paths,
MCP servers other than the run's stubs, and tools other than a few built-in
ones, the display setting `thinking_display`), and the undocumented copies
of each request (`wire_*`). Every path
of the machine is `/work` (the repository) or `/tools` (the stubs); every
id the service minted (session, message, tool call, request, event) is a
fixed made-up one of the same shape; every `timestamp` is one fixed time;
the process id is 12345; thinking signatures are `fixture-signature`.
Model names, token counts and cost figures are as recorded.
`TestFixturesHoldNoAccount` refuses every one of these fields, any key that
begins `fast_mode` or `rate_limit`, an email-shaped string and a path of a
home, temporary, mounted or drive-letter folder, should one come back.

## Hand-built

Built by hand from the shapes Claude Code's documentation gives
(https://code.claude.com/docs/en/agent-sdk/typescript.md, Message Types;
https://code.claude.com/docs/en/headless.md), not recorded:

- `hand-built-background.jsonl`: background work (`task_started`,
  `background_tasks_changed`, `task_updated`, `task_notification`, an
  ambient task). The run's 150-second MCP call was not moved to the
  background in print mode, so no recorded run showed these events.
- `hand-built-expand.jsonl` (row 12 of `docs/plans/chat-or-terminal.md`):
  three messages written without `client_composed`, built from the shapes
  of a recorded run of Claude Code 2.1.332 that was not taken through this
  driver, with every word, id and path made up: an `@notes.txt` the
  program attached and the model answered with no tool call; `/compact`,
  which the program ran itself (its `<local-command-stdout>` replay under
  an id of its own before the message's replay, and a result with no model
  turn); and a command the session cannot run, answered by the program's
  own sentence in a synthetic assistant line, with no replay of the
  message and a result naming it (`user_message_uuid`); and a local
  command the program runs (`/context`): a synthetic assistant line
  carrying `local_command_run` and `local_command_source` with the
  command's output, then the message's replay with its command tags, then
  a result naming the command (`local_command`, `num_turns` 0). They
  follow the shapes of a real run of Claude Code 2.1.332 through this
  driver: every message's `command_lifecycle` lines (queued and started
  before its `system/init`, completed after its result for some
  messages, not all), `system/init` before every message with its
  `terminal_slash_commands` and `user_message_uuids`, a model turn's
  `system/status` `requesting` before the replay and its assistant line's
  `user_message_uuids` and `request_id`, the person's own hooks running in
  the middle of `/compact` (`hook_started`, `hook_response`) and the
  boundary's `logical_parent_uuid`, the unavailable command's
  `local_command_source`, a `timestamp` on every user and assistant line,
  and the results' further keys. What the rule above removes from a
  recorded line (`rate_limit_event`, the account's fields,
  `credential_expires_at`) arrives in the real stream too and is kept out
  here by that rule: the driver reads none of it.
- `hand-built-commands.jsonl` (row 12): the agent's own commands as the
  recorded run's shapes give them, with a made-up list in this project's
  own words, never a person's list or the program's own descriptions: the
  initialize answer's `commands` (name, description, argument hint,
  aliases, builtin), `system/init`'s `slash_commands` names, and a
  `system/commands_changed` carrying the whole new list. As in the real
  stream, every element carries `argumentHint` (an empty string where it
  has none), `builtin` is true or absent, never false, `aliases` is on
  those that have any, and the init carries `terminal_slash_commands` and
  `user_message_uuids`.
- `hand-built-not-signed-in.jsonl`: a start whose first answer says the
  agent is not signed in (`error: "authentication_failed"`, a result with
  `is_error` and `api_error_status` 401). A real run of it would need the
  program signed out, which the run never did.
