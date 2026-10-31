# Repository map


| Path | Responsibility |
|---|---|
| `cmd/conductor` | the free build's `main`: `app.Main` with no provider registered (`serve`, `switchyard`, `host`, `notify`, `crew`, `mcp`, `hooks`, `skill`, `up`, `crews`, `completion`, `version`); `TestPlainBuildRegistersNothing` holds that a plain build registers nothing |
| `app` | the public entry point (`Run`, a wrapper over `cli.Run`; `Main`, SIGINT and SIGTERM ending its context, the error printed, the exit code) for this module's `main` and for a `main` in another module that blank-imports its providers first; imports `internal/cli` only |
| `ext` | the public extension API (`docs/plans/ext.md`), standard library only: the eight interfaces a provider in another module implements, each with its records, bounds and waits: `IdentityProvider` (a token the server's own chain does not know, as a `Principal` with grants), `DecisionRouter` (Switchman: whom a question goes to, `Route`, and that it no longer waits, `Resolved`), `ContextProvider` (context blocks for an attempt's task, and the files the agents touched), `NotificationSink` (the sessions' activity delivered elsewhere), `ContinuityStore` (sync: every document after its local commit, and a restore on another device), `EntitlementSource` (what a workspace may use of a paid feature; no code of this repository asks it), `MediaRoomProvider` (ICE servers beside the configured ones, and rooms) and `Runtime` (where an attempt runs; its `Where` only, its methods the Runtime plan's); a `Valid` method on each record a provider hands the server (`RoomJoin` and `Entitlement` have none: no code of this repository takes them); `Starter` and `Host`, a provider's one door into the app; the registry in the style of `database/sql`'s drivers (`Register…` in a provider's `init`, panicking on a nil value, a bad name, a second provider in a one-value slot, a repeated name in a list, a list past `MaxPerList`, a name held by another value, and after `Freeze`; `Registered`, `Freeze`, `Providers.All`). Nothing in this repository implements or registers a provider |
| `internal/cli` | flags, help and completion text; no business logic |
| `internal/config`, `internal/catalog` | JSON config with `CONDUCTOR_*` overrides; argv-based agent catalog with each agent's yolo, trust and session recipes |
| `internal/store` | atomic JSON documents in the data directory (`dataDir`) |
| `internal/agents` | per-agent hook adapters: assets under `dataDir/hooks`, launch injection, on-demand install into the agent's own config, the Conductor skill and where each agent reads it (`SkillPath`, installed at launch), hook payload mappers |
| `internal/crew` | crews (saved teams of agents) and their runs: model, persistence in `crews/<id>.json`, run engine. Yardmaster, Conductor's agent harness (#334), runs a crew with tasks as a managed run: `task.go` dispatches a crew's tasks as attempts (a fresh session in a worktree at the commit the task starts from) and takes their results, `verify.go` runs a result's checks in a detached checkout, one at a time across the server, `taskfile.go` writes the attempt's `.conductor/task.md` and `task.json`, `commands.go` is a person's commands with receipts (pause, cancel, retry, accept) and an attempt's task as read (row 6); a review's `needs_changes` reopens the task it reviewed and withdraws what rested on its result; `continue.go` continues a run after a restart (row 8); `messages.go` carries the members' messages (row 9) and `checkpoints.go` an attempt's blocker and checkpoints (row 10); every transition goes to the ledger before its side effect. |
| `internal/crew/ledger` | the managed runs' ledger (#334): one SQLite database per data directory, `runs/managed.sqlite`, an importer of `modernc.org/sqlite` (Dispatch's store is the other); a run's transitions as transactions, its events in sequence, its commands' receipts, its checks' evidence and its project manifests' step receipts (each with its phase), the prerequisites' receipts, each tree's lifecycle, the members' messages and the attempts' checkpoints, and a run launched from a Dispatch ticket's plan revision with the ticket's id and its launch package byte for byte, which its continuations carry (schema version 6); every new run's first row an insert that refuses an id the ledger has (`CreateRun`); the owner lock `runs/owner.lock`; recovery at start |
| `internal/dispatch` | Dispatch, Conductor's own work tracking (`docs/plans/dispatch.md`): projects, tickets with stable keys (`CND-12`), their discussion and their history in one SQLite database of its own, `dispatch/dispatch.sqlite` in the data directory, local first; every write a typed operation with a client-minted id and a kept receipt (`Apply`), one `changes` row per field set, added or removed, the entity's `rev` bumped once; `rules.go` holds the field classes and merge rules replication (CS1) will apply to that log; a ticket's attachments are files beside it, one per content by its SHA-256 in `dispatch/attachments/` (`attachments.go`, published per platform), deleted only by the sweep at open (`sweep.go`); no HTTP, no session, no process: the API, the CLI and the tests are its callers |
| `internal/gitcli`, `internal/gitrepo` | git for the Files tab: the working tree's status and a file at a revision through the git binary (argv, the C locale); the commit log and a commit's files through go-git (no binary needed; a crew's linked worktree opens through its common directory) |
| `internal/nvim` | the editor's Neovim: one `nvim --embed` per open file on the machine that runs the session, driven over msgpack-rpc (the official Go client); the buffer's changes, the cursor, the mode, the command line and the messages to a handler, keys in; `internal/session/nvim.go` puts it on the terminal's connection under the `fileEdit` policy |
| `internal/proto` | binary framing and JSON messages |
| `internal/pty`, `internal/session` | process lifecycle; ring buffer, fan-out, roles, resize policy, bounded file reads, viewer roster, activity log, attention, submissions (a paste, then Enter: the one way Conductor types into a session), the agent's own session |
| `internal/proc` | one command outside a PTY, bounded: an argv array (never a shell), a directory, a timeout, the output's head and tail, the process group ended with it; the runner of the managed-run checks (#334) and of a project manifest's steps (`Run`); and a service (`Serve`): a command that runs until it is stopped, `SIGTERM` to its group and `SIGKILL` after a grace, a project manifest's services (EN1) |
| `internal/reach` | how the server is reached from outside: the public address by STUN, the TLS port mapped on the gateway (UPnP IGD, PCP, NAT-PMP), the self-check; mapped is never reported as reachable |
| `internal/certs` | the TLS listener's certificate: from Let's Encrypt through lego (the public address or domains; tls-alpn-01, http-01, dns-01), kept under `dataDir/tls`, renewed on its own; or from files; nothing served before the first issuance |
| `internal/share`, `internal/signal`, `internal/api` | share tokens; hosted-session brokering; HTTP + WebSocket surface |
| `internal/hostagent` | `conductor host`: pion WebRTC peers, relay sink, local terminal; the ICE UDP mux and address rewrite; the session's side of a paste invite (`AnswerPaste`) |
| `internal/paste` | the paste-invite blob (`cpi1.`: an SDP with every candidate, deflated, base64url), shared by the server and `web/app/utils/paste.ts` |
| `internal/notify` | `conductor notify`: attention and event reports from inside a session; hook payload mappers (Claude Code, Codex, agy, Copilot, Cursor, Goose), the agent's own session id among what they read |
| `internal/web` | embedded SPA (`internal/web/dist`, generated, never hand-edited) |
| `web/` | Nuxt 4 + @nuxt/ui 4 + xterm 6 workbench |
| `desktop/` | the Electron shell: starts `conductor serve` with a minted token and the settings it keeps, the handshake, health and restarts, the bridge the workbench reads the token from, WSL 2 on Windows |
| `THIRD_PARTY_NOTICES`, `notices.go`, `scripts/notices.py` | the licences of what ships, generated (never hand-edited) from the binary's Go modules, the web bundle's npm packages (`web/build/bundledPackages.ts` lists them during the build) and the desktop app's; the root package embeds it and the server serves it at `/third-party-notices.txt` |
| `CONTRIBUTING.md`, `DCO`, `TRADEMARKS.md`, `SECURITY.md`, `NOTICE`, `scripts/dco_check.py` | how to contribute (the sign-off, the open-core rules, the pull request checklist, the maintainers), the certificate's text (the Developer Certificate of Origin 1.1, verbatim), the marks and their owner (RockSolid Labs, Inc.; what a fork may do without asking and what it may not; `NOTICE`, which the packages and the image carry, points at it), how to report a vulnerability (GitHub's private vulnerability reporting, never an issue), and the check CI's `dco` job and `make preflight` run: every commit of the branch signed off by its author |
| `docs/` | `protocol.md`, `architecture.md`, `features.md` (what each round decided and verified), `plans/` (a change's plan, reviewed before its code; see Reviews), `design/brand.md`; the bugs known and the features set aside are GitHub issues (see Issues and the project board) |

`internal/session.Local` is shared by the server and the host. Anything that
changes what a viewer sees belongs there, not in a transport. Attention
(bell/OSC detection, agent reports, clearing on input) lives in
`internal/session/attention.go` and `Local`; the browser keeps one live
session store in `web/app/composables/useAttention.ts` (streaming fetch of
`/api/events`), which every page reads instead of polling on its own.


## Adding things

- **A provider**: implement an `ext` interface in another module (this
  repository holds none), register it in its package's `init` with the
  matching `ext.Register…` and do nothing else there, and build a `main`
  that blank-imports that package and calls `app.Main`. Start from `ext`'s
  package documentation: how a provider registers, starts and answers (from
  what it holds, never with a round trip on the call's path), and the
  guard's waits and bounds. No flag, key, environment variable or build tag
  turns one on.
- **An agent**: add a catalog entry in config (or `internal/catalog/defaults.go`
  for built-ins) with `id`, `name`, `command` and `allowArgs`.
- **An API route**: handler in `internal/api`, auth via `requireAdmin` or
  `authenticate`, a test in `internal/api` (`api_test.go` or the route family's
  `*_test.go`), and the client call in `web/app/composables/useSessions.ts`.
- **A control message**: struct in `internal/proto/control.go`, dispatch in
  `ws_viewer.go` and `hostagent/peer.go`, TypeScript type in `protocol.ts`,
  handling in `transport/base.ts`, and a row in `docs/protocol.md`.
