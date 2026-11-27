# Codex app-server fixtures

What `codex app-server --listen stdio://` says to the driver, one JSON-RPC
message a line (the `"jsonrpc":"2.0"` header left out, as the app-server page
says the wire leaves it), for the driver's tests (`codex_test.go`).

## The schema

`surface-0.167.2.json` is an extract of the schema Codex 0.167.2
(`CodexMinVersion`) generates for its app server, once with the stable
surface alone and once with the experimental one:

    codex app-server generate-json-schema --out DIR
    codex app-server generate-json-schema --experimental --out DIR

run with `CODEX_HOME` set to an empty folder of the run's own, so that no
configuration or sign-in of anyone's was read. It keeps, for each side, the
client's request methods with their parameters' names, the client's
notifications, the server's request methods, the fields of `clientInfo`, of
`initialize`'s capabilities and of a text input; and, from the stable side,
the values the server takes in an answer to a command's or a file change's
approval and to an MCP server's form. `TestCodexStableSurfaceOnly` holds
every line the driver writes to the stable side.

## Hand-built

No real run of Codex's app server was recorded for these: each is built by
hand from the stable schema's shapes and the app-server page's order of
messages (https://learn.chatgpt.com/docs/app-server.md: an `item/started`
before its approval request, `serverRequest/resolved` after it,
`item/completed` as "the authoritative state", `turn/completed` with
`completed`, `interrupted` or `failed`). The live look of row 10 records the
real ones at `CodexMinVersion` and replaces these where they differ.

- `hand-built-command.jsonl`: the start (the initialize answer, the thread
  resumed), a turn that runs a command (its output, its exit) and says what
  it found in two streamed pieces, a reasoning item, the thread's token use.
- `hand-built-file-change.jsonl`: a file change of two files (an update, an
  add) asked about and accepted, the request resolved after the answer; a
  command that fails; a second turn's use counted from the first's.
- `hand-built-declined-and-interrupt.jsonl`: a command asked about and
  declined; a turn interrupted while its words streamed.
- `hand-built-mcp-and-others.jsonl`: a tool of an MCP server's, an MCP
  server's form (declined), a tool call of the client's (none registered:
  refused), a subagent's thread's items (ignored), a warning and a method no
  driver reads, a web search.
- `hand-built-not-signed-in.jsonl`: a start whose first turn fails
  `unauthorized`.
- `hand-built-limit.jsonl`: a retried error, then a turn that fails
  `usageLimitExceeded`.

Every thread, turn and item id is a made-up one; every path is `/work` (the
repository) or `/codex-home`; the user agent is
`codex_cli_rs/0.167.2 (fixture)`. No fixture holds an account, a plan, an
email or a token (`TestCodexFixturesHoldNoAccount`).

Each `.events` file is what the driver told the session and, after
`--- written`, what it wrote back (`go test -run TestCodexFixturesReplay
-update` writes them).
