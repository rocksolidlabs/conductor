# Agent Client Protocol fixtures

What an agent's ACP server (protocol 1) says to the client, one JSON-RPC
message a line, for the client's tests (`acp_test.go`).

## Hand-built

No real run of any agent's ACP server was recorded for these: each is built
by hand from the protocol's schema (https://agentclientprotocol.com/protocol/v1/schema.md)
and its pages on tool calls, permissions and cancellation. Row 11's live look
records each agent's own at its version and adds them beside these.

- `hand-built-turn.jsonl`: a turn of the session taken up: the user's
  message echoed, a thought, a plan, an execute step with its output, an
  edit of a file (its old and new text) and a file written whole (no old
  text), a step that fails, the agent's words in two chunks, another
  session's update and the context's use (ignored), an update no client
  reads, the prompt's answer `end_turn`, and words after the turn (no
  turn's).
- `hand-built-permission.jsonl`: an execute step asked about with
  OpenCode's option ids (`once`, `always`, `reject`: ids unlike their
  kinds), allowed by the `allow_once` option's own id; the agent's fs and
  terminal requests (refused, the client advertising neither); the
  prompt's answer `max_turn_requests`.

Every id is a made-up one; every path is `/work`. No fixture holds an
account, an email or a token.

Each `.events` file is what the client told the session and, after
`--- written`, what it wrote back (`go test -run TestACPFixturesReplay
-update` writes them).
