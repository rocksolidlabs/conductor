# Features

Scope notes for the workbench redesign. The source mockup is the
claude.ai/design project "Conductor Mockups" (screens 1a workbench, 1b wall,
1c runs, 1d launch, 1e share, 1f join, 1g carousel). This file records what is
being built, the decisions taken along the way, and what each round left
for a person to verify. The bugs known and what is deliberately
deferred are GitHub issues, worked on the project board (AGENTS.md, Issues
and the project board; [tasks-todo.md](tasks-todo.md) maps its old entries
to their issues); the "Deferred" lists under the rounds below are the
history of when each item was set aside.

## Delivered (2026-09-28)

Everything under Planned below shipped on the `workbench-redesign` branch.
The list is kept as the description of what exists; see `docs/protocol.md`
for the wire details.

## Delivered (2026-09-30)

- **Crews** (2a Crews, 2b Crew running). Saved teams of up to 12 agents with
  role prompts, start conditions and optional per-member git worktrees, run
  from the Crews page or `conductor up`; the run page has tiles, handoffs,
  broadcast, run links and stop. See the README (Crews) and
  `docs/protocol.md` (Crew runs); hosted crews and worktree cleanup are below
  under Future features.

## Delivered (2026-10-01)

- **Round 3**: the deferred items of rounds 1 and 2 and the crew storage of
  plan 1; then the sidebar rail, example crews, the working-directory picker
  with the git check, agent availability, shell completion and fullscreen on
  every page. See the README (Sidebar and keyboard shortcuts, Crews, Agent
  catalog, Shell completion) and `docs/protocol.md` (`GET /api/paths`,
  `GET /api/git/check`, `POST /api/crews/examples`, `available` and `site` on
  `GET /api/catalog`). The round 3 decisions below describe what exists.

## Planned

### Client only (no protocol change)

- **Status colours.** Needs-input is amber, running is green, idle and exited
  are grey. Terracotta stays on the brand mark only, per
  [design/brand.md](design/brand.md). JetBrains Mono for code and terminal
  chrome.
- **Workbench** (1a): the sidebar becomes the session list grouped into Needs
  you, Running and Exited, with a filter box. The selected session fills the
  main pane. A right-hand inspector has People, Files and Activity tabs; Files
  replaces the current slideover. The Sessions table page is removed.
- **Wall** (1b): header chips All / Needs you / Running; a queue panel that
  lists every session waiting for input with its attention message and a
  reply box that sends text plus Enter over a control connection.
- **Carousel** (1g): film strip of all sessions with a rotation progress bar,
  a "next up" line, and a "jumped here" note when follow mode moved. Follow
  mode holds for two intervals (at least 20 s), not forever, so a stray
  prompt cannot freeze the rotation.
- **Launch** (1d): "Runs on: Server / My machine". The latter shows the exact
  `conductor host` command, including the host token, and waits for a hosted
  session with that name to appear.
- **Share** (1e) and **Join** (1f): restyle. Join adds a display-name field.

### Needs protocol or server work

Each item touches `internal/proto`, `web/app/utils/protocol.ts` and
`docs/protocol.md` together.

- **Presence.** Display name per connection, a viewer roster (id, name, role,
  link label, since) in the viewers message, and a per-subscriber last-input
  time so "typing" is derived server side. Names are labels, not
  authentication; the UI says so.
- **Structured attention.** Optional `kind` and `options` on attention reports
  so quick-reply buttons render only when the prompt shape is known
  (Claude Code permission requests). Otherwise the UI shows a plain reply box.
  Buttons disable the moment the attention state changes.
- **Activity log.** Bounded per-session log of attention transitions, inputs
  during needs-input, link changes and joins. Feeds the Activity tab and the
  wall's Answered list.
- **Session metadata.** Git branch (read at launch for server sessions,
  reported by the host for hosted ones), host OS user, per-link viewer count,
  client-measured transport round-trip time.

## Round 2: crews, adding agents, events and hooks (planned 2026-09-29)

Source: claude.ai/design project "Conductor Mockups v2" (`Conductor Mockups
v2.dc.html`, screens 2a Crews, 2b Crew running, 2c Add agent, 2d Events).
Plans: `docs/superpowers/plans/2026-09-29-data-dir-and-catalog-editing.md`,
`2026-09-29-events-and-agent-adapters.md`, `2026-09-29-crews.md`, executed in
that order.

### Decisions

- **Persistence.** A writable data directory (`dataDir`, env
  `CONDUCTOR_DATA_DIR`, default `~/.conductor` since round 3, `conductor.d`
  next to the config file before) holds `catalog.json` (agents added or hidden
  from the UI), `crews/` (one file per crew), generated hook assets under
  `hooks/` and the Conductor skill. The config file stays read-only; the data
  directory overlays it.
- **Hook wiring.** Inject at launch wherever the agent's CLI allows it
  (Claude Code `--settings`, Codex `-c`, pi `--extension`, aider env vars,
  OpenCode `OPENCODE_CONFIG_DIR`); everything else gets an explicit
  "Install on this machine" that merges a Conductor block into the agent's own
  config, plus copyable snippets and `conductor hooks install <agent>` for
  hosts. Conductor never edits an agent's files without a click or a command.
- **Events are a superset of attention.** The per-session activity log
  already broadcasts join, leave, attention, input, link and status entries.
  Round 2 adds `progress`, `artifact`, `handoff`, `tool_use`, `tool_denied`
  and `error`, a `POST /api/sessions/{id}/events` route the agent token may
  call, `conductor notify --event`, and an admin SSE feed of every entry.
  Attention state (`needs_input`, `working`, `done`) is unchanged and still
  derived from the same reports.
- **Supported agents.** Events and one-click install exist only for agents
  with an adapter (table below). Any other catalog entry still works with
  bell/OSC, the new screen-pattern detector, or `conductor notify` called by
  hand. Gemini CLI is not added: it is superseded by Antigravity (`agy`).
- **Crews** (2a and 2b) shipped in full on 2026-09-30; see Delivered above.

### Agent adapter matrix

| Agent | Command | Waiting-for-input signal | Turn done | Tool events | Wiring | Verify before shipping | Yolo recipe (what it turns off) | Trust prompt | Session recipe (id; resume) |
|---|---|---|---|---|---|---|---|---|---|
| Claude Code | `claude` | `Notification`/`PermissionRequest` hooks (with options); `idle_prompt` (a minute at rest) is done, not a question (round 4) | `Stop` | `PostToolUse`, `PermissionDenied`, `SubagentStop` | launch: `--settings <hooks/claude.json>` | — | `--dangerously-skip-permissions`, `skipDangerousModePermissionPrompt` in the `--settings` file (every permission prompt; `.git`, `.claude` writable; deny rules hold; the trust question stays) — live, 2.1.287 | `Is this a project you created or one you trust?` — live; no per-launch trust: trust the repository once (its worktrees follow) | set `--session-id {uuid}`, then hook `session_id` (latest); `--resume {id}` — live |
| Codex CLI | `codex` | the bell (`tui.notification_method="bel"`), rung for every notification but `async-question` (`tui.notifications`, 0.161, 2026-10-14: a question Codex asks while its turn goes on rings nothing, #186), and the `PermissionRequest` hook; its window title says it works (`tui.terminal_title=["activity","run-state"]`: "⠋ Working" through a turn, "Ready" at rest, seen with 0.161 on 2026-10-14), read by the catalog's `workingTitle` as `working` (source `title`), and a wait raised while it says so (its own question it goes on from, a bell) gives way a second later (#186); its program status (OSC 7501, 0.163 and later: `working`, `blocked`, from 0.164 with the kind of what blocks it, and `working` while an async question is pending where the title says Action Required) retires the title once read, settles waits as the title did and raises one for a form no pattern knows (source `status`, #197); the end of a turn is done, not a question (round 4), and the bell it rings after its done is that turn's own, not a question, until the next Enter (2026-10-11); its command approval ("Would you like to run the following command?") read off the screen for the session's whole life, with its words and choices `y`, `p`, Esc (0.161, 2026-10-11) | `notify` (agent-turn-complete) → done; the hidden title thread's turn is ignored | experimental `hooks.json` (`features.hooks`), not on Windows | launch: `-c notify=[…]`, `-c tui.notification_method="bel"`, `-c tui.notifications=[…]`, `-c tui.terminal_title=["activity","run-state"]`; file: `~/.codex/config.toml` + `~/.codex/hooks.json` | `-c` array syntax; PermissionRequest hook shape | `--dangerously-bypass-approvals-and-sandbox` (every approval and the sandbox) and per launch `-c projects={"<repo>"={trust_level="trusted"}}` — live, 0.159.0 | `Trust this folder?` (Enter accepts and saves it) — live; its other answer is Quit in 0.161 (Down, Enter), which ends Codex, labelled "No, quit" | not settable; notify `thread-id` (lowest: the title thread reports too); `resume {id} -c tui.resume_cwd="session"`, same directory — live |
| Antigravity | `agy` | bell (no notification event) | `Stop` | `PreToolUse` (working; a `toolCall` without the `error` field) / `PostToolUse` (`toolCall.name`) | file: `~/.gemini/config/hooks.json` (global) or `.agents/hooks.json` (project) | exact global path | `--dangerously-skip-permissions` (tool permission requests; its trust dialog stays, trusted per exact path) — help | — | not settable; hook `conversationId` (installed hooks only); `--conversation {id}`, same directory — live (`-p`) |
| Copilot CLI | `copilot` | `notification` (`permission_prompt`) | `agentStop` | `userPromptSubmitted`/`preToolUse` (working), `postToolUse`, `errorOccurred` | file: `~/.copilot/hooks/conductor.json` (own file, no merge) | permission prompt key bindings for options | `--yolo`, `COPILOT_ALLOW_ALL=true` (every tool, path and URL; the env var also skips its trust question; deny rules win) — live, 1.0.59 | — (the recipe skips it) | set `--session-id {uuid}`, hook `sessionId`; `--session-id {id}` — live (the hook's `sessionId` not live) |
| Cursor CLI | `cursor-agent` | bell / pattern (CLI fires no waiting event) | `stop` | `beforeSubmitPrompt`/`preToolUse` (working), `postToolUse`, `afterFileEdit` | file: `~/.cursor/hooks.json` (merge) | CLI binary name; which events the CLI fires today | `--yolo --trust` (command and MCP approvals; `--trust` skips workspace trust) — docs | — (the recipe skips it) | not settable; hook `conversation_id` (installed hooks only); `--resume {id}`, same directory — docs |
| OpenCode | `opencode` | plugin `permission.asked`, `session.idle` | `session.idle` | `tool.execute.after`, `session.error` | launch: `OPENCODE_CONFIG_DIR=<hooks/opencode>` if additive, else file: `~/.config/opencode/plugins/conductor.ts` | whether `OPENCODE_CONFIG_DIR` adds to or replaces the default dir | `--auto` (every permission not explicitly denied) — docs | — | none this round (plugin capture deferred): Relaunch |
| pi | `pi` | `agent_end` | `agent_end` | `tool_call`/`tool_result` | launch: `--extension <hooks/pi-conductor.ts>` | — | none: pi has no permission system | — | set `--session-id {uuid}`; `--session-id {id}`, same directory — source |
| oh-my-pi | `omp` | as pi | as pi | as pi | file: `extensions:` entry in `~/.omp/agent/config.yml` | pi extension API compatibility (issue #2166) | `--yolo` (approvals unless a deny policy matches; its default already) — docs | — | none this round (plugin capture deferred): Relaunch |
| aider | `aider` | `--notifications-command` | same | none | launch: `AIDER_NOTIFICATIONS=true`, `AIDER_NOTIFICATIONS_COMMAND` | confirmations (`(Y)es/(N)o`) need the pattern detector | `--yes-always` (every confirmation, shell commands included) — docs | — | none this round (its handle is a chat-history file): Relaunch |
| Goose | `goose` | bell / pattern (no waiting event yet) | `Stop` | `UserPromptSubmit`/`PreToolUse` (working), `PostToolUse` | file: `~/.agents/plugins/conductor/hooks/hooks.json` | — | `GOOSE_MODE=auto` (tool approvals; its default already) — docs | — | set `session --name cdr-{uuid}`; `session --resume --name {id}`, same directory — source |
| Amp | `amp` | `agent.end` (in-process plugin) | `agent.end` | `tool.call`/`tool.result` | file: `~/.config/amp/plugins/conductor/` | plugin API surface | `--dangerously-allow-all` (every tool approval; the flag in the rebuilt CLI unconfirmed) — docs | — | none this round (plugin capture deferred): Relaunch |
| DeepSeek Harness | `dsh` (with the TUI plugin) | plugin events `question-asked`, `permission-requested` | `session completed/failed` | none | file: dsh plugin | developer preview; plugin API changes | `DSH_PERMISSION_MODE=danger-full-access` (its sandbox and every approval) — source | — | none (unverified): Relaunch |
| Shell / anything | any argv | bell / OSC / pattern | exit | none | — | — | none | — | none: Relaunch |

### Open verification (round 2)

- Each "Verify before shipping" cell above is a manual check against the
  real CLI before that adapter is marked supported on the Events page.
- The screen-pattern detector strips ANSI sequences and matches the last
  line after 500 ms of silence; TUIs that redraw continuously (spinners) may
  never go quiet. Ship it as opt-in per catalog entry.
- **Crews: Claude Code's workspace-trust dialog.** A member in a fresh
  worktree starts in a directory Claude Code has not seen, so it may open with
  its "trust this folder" dialog. The run engine types the role prompt once the
  session is ready (the agent waits for input, or its output has gone quiet) and
  cannot tell that dialog from the prompt, so the dialog may take the typed
  prompt. Launch a crew of `claude` members with worktrees in a repository
  Claude Code has not trusted and see whether the prompts land.
  Answered in round 4: the dialog appears even with the bypass flag, and the
  typed Enter answered it (Claude Code chose "No, exit"); the run now holds
  the prompt while the question shows (Crew runs in `docs/protocol.md`), and a
  worktree of a trusted repository asks nothing.
- **Crews: long one-line role prompts.** A role prompt is typed as one line,
  its line breaks and tabs made spaces as a handoff's or a broadcast's are,
  with a carriage return at the end, so no agent takes the second line of a
  prompt as a further message. What is left to check is whether each agent TUI
  that matters takes a long single line (a paragraph of a few thousand
  characters) as one message, rather than truncating it or treating a fast
  burst of input as a paste; bracketed paste stays the deferred fix for agents
  that need it.
  Answered in round 4: Codex took every one-write prompt as a paste (a
  newline, not Enter) and Claude Code any over 800 bytes; prompts, handoffs,
  broadcasts and replies now go in as a bracketed paste and a separate Enter.
- **Crews: end to end.** In a git checkout under the allowed roots, create the
  crew `api-sweep` with worktree isolation and the members `lead` (`claude`,
  immediately), `core` (`claude`, immediately) and `tests` (`shell`, after
  `core`), and launch it.
  - `git worktree list` shows the new worktrees; the lead and core terminals
    received their prompts; when core first reports done, the tests session
    starts.
  - From core's terminal run `conductor notify --event handoff --to tests
    --message "/v1/users ready"`: the tests terminal shows `Handoff from core:
    /v1/users ready`.
  - Tick two tiles and broadcast a line: it lands in both. Flag one member as
    needing input (`conductor notify --state needs_input` in its terminal) and
    broadcast again: it is skipped and named in the toast.
  - **Share crew**, open the link in a private window: every member tile is
    there, read-only.
  - `conductor up api-sweep` from a shell with `CONDUCTOR_WORKBENCH_TOKEN` set
    prints the run URL.

## Open verification

- **Claude Code's dialogs.** Their choices are read off the dialog (#288),
  checked in a PTY against Claude Code 2.1.304 on 2026-10-19: a digit picks
  at once in a tool's permission dialog and in AskUserQuestion, and the
  plan's "Tell Claude what to change" only takes the highlight into its
  feedback field. A later release that changes the footers or the
  highlight `❯` needs the Claude Code question in
  `internal/catalog/defaults.go` checked again (no agent runs in CI).

## Decisions

- **Theme is system wide.** One dark or light setting for the whole app. The
  mockup's dark wall next to a light workbench is not a per-screen theme; the
  wall follows the global mode.
- **No join preview.** The mockup showed a snippet of terminal output on the
  join page before the guest steps in. Removed: a share URL fetched by a link
  unfurler or crawler would expose session content without anyone choosing to
  join. The join page shows only the session name, agent, host and the role the
  link grants. Content appears only after the guest joins.
- **No Runs nav item** until there is something behind it.

## Future features

Collected here so they are not lost. None of these block the planned work.

- **Keyboard turn-taking.** A per-session mode (anyone with control, or take
  turns) with a single keyboard holder. Input from non-holders is rejected with
  an error; a control message takes or releases the keyboard; the People tab
  shows who holds it. Lives in `internal/session.Local` so server and host
  behave the same.
- **Hosted crews.** Members that run on `conductor host` (a developer's
  machine) instead of the server. The editor's **My machine** option is
  disabled and the server refuses to launch a crew saved with `where: host`.
  It needs the server to start sessions on a host, make worktrees there and
  type into a hosted session (prompts, handoffs, broadcast); none of that
  exists yet.
- **Automatic worktree cleanup.** Conductor never deletes a worktree or a
  branch, whether the run stops or the crew is deleted; they are removed by
  hand (`git worktree remove`, `git branch -d`). Removing a stopped run's
  worktrees and merged branches, after a confirmation, is future work.
- **Runs (orchestration).** Mockup screen 1c. A run is a spec
  (`conductor.run.yaml`) of stages: plan, parallel build steps in per-step
  worktrees, verify, and a human merge gate with named approvers. Every step
  is an ordinary session that can be opened, shared or taken over. The run
  page shows the stage graph, a Spec / Events / Artifacts inspector, and
  `on_needs_input: queue` routes step prompts into the wall queue. Presence
  and the activity log above are prerequisites for gates and approvers.

## Round 3: polish, examples and completion (planned 2026-10-01)

Decided with the user on 2026-10-01. Two plans, run in order: first close every
open deferred item from rounds 1 and 2, then the features below.

### Decisions

- **Sidebar rail.** Hiding the sidebar collapses it to a narrow icon rail
  instead of removing it. The rail keeps every function: the mark (artwork),
  a Launch button, the filter as a search icon that expands the sidebar, the
  nav entries as icons with tooltips, and the session list as agent avatars
  with the attention dot and the crew grouping (a thin label when a run is
  active). Clicking an avatar opens the session; the rail has an expand
  button; `meta+B` / Alt+B toggles rail and full width. The choice is
  persisted per browser as before. Narrow screens keep the slideover.
- **Example crews.** `conductor serve --examples` (env `CONDUCTOR_EXAMPLES` set
  to `1` or `true`) seeds example crews once: ids `example-todo-app`,
  `example-test-fixer`, `example-docs-writer`, `example-dependency-upgrade`; a
  crew whose id exists is left alone. Members use the `claude` and `codex`
  built-ins with real role prompts and start conditions (the todo app: a lead
  that plans, two builders after the lead, a tester after the second builder,
  since a start condition names one member; every member that starts after
  another begins by merging that member's branch,
  `crew/$CONDUCTOR_RUN/<member>`, because a worktree starts from `HEAD`); `cwd`
  is the server's default working directory; isolation `worktree`. The empty
  Crews page offers "Load the examples", which calls `POST /api/crews/examples`
  (admin, same seeding, same once-only rule). The examples are data, not
  built-ins: editing or deleting them is normal.
- **Working-directory autocomplete.** `GET /api/paths?prefix=<path>&limit=50`
  (admin) lists child directories of the longest existing directory in the
  prefix, confined to `allowedRoots` through the same resolution as
  `resolveCwd` (symlinks resolved, nothing outside a root), at most 50
  entries, hidden directories only when the typed segment starts with a dot,
  each entry with `git: {repo, commits}` so the picker shows which
  directories a worktree crew can use. The crew editor and the Launch dialog
  use one `DirInput` combobox fed by it (debounced, keyboard navigable).
  Enter takes an entry only once the highlight was moved (the arrows, or the
  pointer over an entry) since the text last changed: the list highlights its
  first entry by itself, and Enter on a complete path must keep it rather
  than take its first child. A prefix outside the roots is refused with a
  message that names the allowed roots, which the picker shows as it is. A
  listing cut short by its 5 s deadline, entries left unmarked, says
  `truncated`.
- **Git check.** `GET /api/git/check?cwd=<path>` (admin) answers
  `{inRepo, toplevel?, hasCommit, message}` with the same rules the launch
  uses (`git rev-parse` as argv, under the allowed roots). The crew editor
  shows the state beside the working directory and explains why Launch with
  worktree isolation would be refused; the launch handler's 409 stays as the
  authority.
- **CLI completion.** `conductor completion zsh|bash` prints a completion
  script: subcommands, flags and their values statically; crew ids for
  `conductor up` dynamically through `conductor crews --ids` (ids only, one
  per line, exit 0 and silent when the server is unreachable or the token is
  missing), using `CONDUCTOR_SERVER` and `CONDUCTOR_WORKBENCH_TOKEN` from the
  environment. `conductor completion install` appends one marked `source`
  line to `~/.zshrc` or `~/.bashrc` (idempotent; refuses a file, a link or a
  directory not owned by the user). No new dependency.
- **Fullscreen everywhere.** The document-fullscreen toggle that the wall and
  the carousel have (`useFullscreenToggle`, the `F` key, Alt+F in a terminal)
  moves to the layout: one button in every page header and one shortcut
  registration, so the session page, the crew views, the join page, Events,
  Agents and Crews all toggle it; the wall and carousel keep their buttons.
  `F` is ignored while an input or textarea has focus, as the other letter
  shortcuts already are, and while a select (a combobox or listbox, whose
  typeahead takes letters) has focus.
- **Agent availability.** `GET /api/catalog` reports `available` per agent (whether `command[0]` resolves on the server through the same check `POST /api/catalog/check` uses, re-evaluated per request and cached for 30 s; any program of that name on the server's PATH counts, so a namesake such as the Go migration tool `goose` makes the Goose agent show as installed, and a relative path such as `./agent.sh` counts as installed because it resolves in the session's directory, which the check reports as `unknown: "relative"`, resolved at launch; a lookup that does not answer within 2 s, or before the client goes away, counts as installed too and its answer is kept when it lands, so a `PATH` entry on a stalled mount cannot hold up the listing, the Launch dialog or a launch, whose spawn still fails if the program is missing) and `site` (the agent's website, a field on built-ins and an optional field on saved agents). The Agents page greys out an agent that is not installed and says "Not installed on <host>", and links to the site of any agent that has one; the Launch dialog's server tab lists only available agents and, when none is, says so with a link to the Agents page; the "My machine" tab lists every agent because availability there is the host's. The crew editor's agent select marks unavailable agents the same way and the launch route refuses a crew whose member agent is not installed with `invalid_crew` naming it.
- **Deferred items.** Every open item from the rounds 1 and 2 ledgers is
  closed in the first plan, whatever its kind. A triage on 2026-10-01 found
  five already closed and two obsolete; the 55 open ones are listed below
  with the decision taken where one was needed.

### Open verification (round 3)

- The example crews' role prompts in a live run against Claude Code and Codex:
  whether each `after` member's merge of its predecessor's branch works for
  each agent, and whether the todo app's tester finds core's branch when it
  waits for it. The git sequence was run by hand; no agent was.
- The completion scripts in a real zsh and bash session. They were driven
  through a pseudo-terminal with `zsh -f -i` and `bash --norc -i` (plain
  `compinit`), not `zsh -ic` or `bash -ic` with a user's own rc files or a
  framework such as oh-my-zsh, and `completion install` was run against scratch
  files, never a real `~/.zshrc`. bash 3.2 (the one macOS ships) is untested
  and may not support `source <(...)`.
- The built-in agents' `site` URLs (twelve, in `internal/catalog/defaults.go`),
  each opened in a browser by hand: they ship as best known and a test checks
  their shape only. On 2026-10-02 three were found moved and fixed (codex,
  goose, dsh, now pinned by the test); the other nine are still to open.
- Real fullscreen entry with the `F` key and the header button is unverified:
  headless Chromium does not grant it, so the headless checks emulated the
  Fullscreen API and counted the requests.
- The picker, the rail and the Launch dialog's server tab on a real phone (the
  slideover) and at 1024 px; they were only looked at in headless Chromium.
- The sidebar's width across a Nuxt UI upgrade. Nuxt UI 4.11.2 has no size
  model or event for `UDashboardSidebar`, so the width kept in localStorage
  (`conductor.sidebar.size`) is read from the `--width` style on the sidebar's
  root after a drag or a double-click, and the saved width starts it through
  `defaultSize`; the headless check of this passed at 4.11.2 only.
- Wording: the crew members table says "After X idle" where the engine and the
  example prompts say "reports done". It is the same condition; pick one.

### Deferred items to close (plan 1 of round 3)

Closed by `docs/superpowers/plans/2026-10-01-round3-deferred.md`.

Strict JSON and the store:
- `config.Load` and `catalog.ReadFile` reject trailing data after the first JSON value, like the store and webhooks do; the store comment then tells the truth.
- `store.Save` writes with `SetEscapeHTML(false)` so patterns and snippets stay hand-editable.
- The catalog lock is not held across the fsync or the response: an editor lock for writers, the catalog lock only for the snapshot swap.
- Store tests: a chmod 0500 write-probe case (skipped as root), a forced rename failure for the kept-previous-document test, a concurrent reader in the concurrent-save test; the end-to-end test stops assigning the server's catalog without the lock.

Catalog validation and API shape:
- `validate` bounds `cwd`, `icon` and `adapter` (shape `^[a-z0-9-]{1,32}$`), env key and value lengths and `envPassthrough` name length, and checks the adapter id against the registry for config files too.
- `Upsert` stores a clone; signal kinds become exported constants; `Clone` gets a value receiver.
- A saved override that omits `adapter` or `signal` inherits them from the built-in it replaces.
- An override stores only the env keys whose value the editor changed; unchanged keys follow the base agent, so a secret rotated in the config reaches the agent (closes the "pinned env" item).
- Catalog entries carry `source` (`built-in`, `config`, `saved`) so the Agents page says Hidden or Removed truthfully.
- Tests: launching an overridden built-in, hiding down to an empty catalog, deleting an overlay-only agent.

The add-agent form:
- The form logic (masked-env round trip, `signalOut`, errors, the id pattern from the server's rule) moves to `utils/agentForm.ts` with vitest; a typed `***` on a new row is caught in the form; the command check posts `command[0]` only; an unclosed quote is refused instead of chipped; `slugId` never ends in a dash; the signal cards use the Nuxt UI radio group with arrow keys; `ArgvInput` reuses the input ring classes.

File reads:
- The file-read deny list also covers, in the config file's directory (and beside its symlink-resolved target), every name that contains the config's base name, ignoring case (`.bak`, `~`, `.swp` and `#…#` editor copies); the catalog file gets the same rule.

Sessions and events:
- The viewer reader stops closing the sink under `Pump` on a host disconnect, and a departing host's viewer gets one error frame.
- The attention state travels with the entry from `Record` to `OnActivity` (closes the read-after-the-fact window), and the SSE `activity` event carries `state` for attention entries so the Events page stops inferring hosted state.
- `conductor notify` retries a 429 for attention words (bounded backoff within its 5 s budget).
- A hosted session with no host connected meters route reports like a connected one.
- The host's "no launch route" line is logged at warn so it shows with the local terminal attached.
- `oneLine`/`dropControl` reuse `CleanName`'s stripping; the Record and hook tests move to their own file; the bell test waits on a frame instead of polling.

Adapters and the CLI:
- `WriteAssets` warns instead of refusing to start on a chmod failure of an existing asset; `staleCommand` rewrites only commands whose program is named `conductor`; `withYAMLListItem` handles a BOM and refuses a flow-style root; the hooks dir records the version with the binary and the CLI warns on skew; the by-hand note prints only the step that needs the hand; the ownership check accepts the process's own home when it is writable; `payloadFlags` derives from the hooks table; the aider card says "nothing to install" under an unknown home.
- `merge.go` splits out `home.go`, the mappers move to `notify/mappers.go`, `injectHooks` to `hostagent/hooks.go`, the claude/cursor merge closures share a helper; the remaining clipboard calls and dark code blocks use the shared helper and component.
- A catalog icon missing from the client bundle renders a generic agent icon, not nothing.

Docs:
- README: the Events page shows the webhook host, not the path; protocol.md: a missing hosted attention state has two causes; README and protocol.md: what an override inherits.

Webhook address rule:
- 6to4, Teredo and SIIT forms are judged by their embedded IPv4 address; a `64:ff9b:1::/48` prefix of another length is refused with a clear message.

Crews, runs and links:
- `Launch` returns `ErrRunStopped` when a stop lands before the first reserve; the run being launched stays exempt from eviction until its view link is minted; the link creation and the run lookup happen under one check; the forget-time disconnect goroutine is tracked by `Shutdown`; revoking an already-revoked link records nothing.
- `inRepo` reports `rev-parse` failures other than "not a repository" (dubious ownership, permissions) with git's message.
- The crews page keeps a launch's shown-once link until it is shown, refreshes crews on every visit, retries a failed run-name fetch, and checks the holder's expiry when storing; the join page gets a `JoinCrewGrid` component; the run view's grid has no empty cell beside the feed; the long prompt placeholder is checked in a browser at narrow widths.
- Tests: a sync point in the handoff "nothing typed" check; `api_test.go` split by route family (crews and runs into `crews_test.go`), the duplicate 503 and crew-cap checks deduplicated, the duplicate-names test asserting "twice".

Data directory default (decided 2026-10-01):
- When neither `dataDir` nor `CONDUCTOR_DATA_DIR` is set, the data directory is `~/.conductor` (the home of the user running `conductor serve`), never next to the config file or in the working directory. For backward compatibility, when `~/.conductor` holds no server data (`catalog.json`, `crews/` or `crews.json`; a `hooks/` directory alone does not count) and the old default (`<config dir>/conductor.d` or `./conductor.d`) does, the server keeps using the old directory and logs a notice naming both paths and how to move. The old directory must be a real directory (not a symbolic link) owned by the server's user, since its `catalog.json` chooses the commands agents run; anything else under that name, when it would be kept, stops the server with an error naming it, its owner and `dataDir`/`CONDUCTOR_DATA_DIR`. The host's hooks directory moves to `~/.conductor/hooks` the same way (old `~/.local/state/conductor/hooks` kept when present). The Docker image keeps its explicit `CONDUCTOR_DATA_DIR`. README, the upgrade note and `conductor.example.json` follow.

Crew storage (replaces the single `crews.json` and the 50-crew cap):
- One file per crew at `<dataDir>/crews/<id>.json`, written atomically by the store, which gains `List` and `Delete`; a corrupt file names itself in the startup error and the other crews still load.
- `GET /api/crews?offset=0&limit=100` reads the directory and answers `{crews: [summaries], total}` (limit at most 500); `GET /api/crews/{id}` returns one crew in full for the editor; the Crews page pages through the list.
- No cap on the number of crews; the per-crew size cap stays as a sanity bound on one file and its request body, at 1 MiB.
- An existing `crews.json` is split into per-crew files on the first start and renamed `crews.json.migrated`.

Accepted as documented limitations (not changed): hosted crews stay future work.

## Round 4: crews in view, prompts that run, interactive tiles, yolo, e2e (planned 2026-10-01)

Asked by the user after running round 3 on 2026-10-01; decided the same day.
One plan, `docs/round4-plan.md`, built on the research in `docs/round4/`
(the prompt investigation, the terminal fill, the yolo flags, session
resume, and Codex's review of this section).

### Decisions

- **Runs on the Crews page.** Each crew in the list shows its runs (newest
  first, from `GET /api/runs`): the run's name and age, a status (running,
  needs input with a count, stopped, finished), the members as avatars with
  their status (pending, starting, running, needs input, ended), and a link
  to the crew view; the crew's badge keeps "Running" when any run is active.
  Runs stay in memory on the server as today (no persistence; the engine
  keeps at least the active runs and evicts idle ones past 100). A run's
  state is: running while a member is starting or running; needs input when
  a running member's session needs it; stopped after a stop; finished when
  every member ended and none is pending (a member's `done` report keeps it
  running — a done agent is idle, not gone); a member's error shows beside
  the state. Run changes that no session change carries (a member going
  from starting to running or failing to start, an all-manual launch, a stop
  of a sessionless run, eviction, a log entry) reach the browser through the
  existing `/api/events` stream as a bounded `run` event (`{id}` to re-read,
  or `{id, removed: true}`), coalesced on the client; the one live store
  keeps the runs, every page reads them, and an older reply never replaces a
  newer one. No polling anywhere, including the crew view's 10 s re-read,
  which goes.
- **The sidebar groups sessions by crew run everywhere.** The full sidebar
  does what the rail already does: inside each section (needs you, running,
  exited) the sessions with no run come first, then one group per run with a
  header naming the run (linking to the crew view) and its member sessions.
  The run-only variant on a member's session page and on the crew view stays.
  One grouping helper in `web/app/utils/sidebar.ts` serves both.
- **A typed prompt runs.** When the engine types a member's role prompt, a
  handoff or a broadcast into Claude Code or Codex, the agent runs it without
  a person pressing Enter. The investigation against the real CLIs (2026-10-01)
  found the cause: the text and its carriage return went out in one write, so
  each TUI read the return as part of a paste — Codex on every prompt (its
  paste-burst detector), Claude Code for text over about 800 characters or
  typed before its input is up (startup pauses of up to 0.95 s beat the
  one-second-quiet check). The fix verified end to end on both: the text as a
  bracketed paste, then the carriage return as a separate write 250 ms later;
  for Claude Code, no typing between its screens and one more Enter if the
  member has not reported working within 3 s. The mechanism is one serialised
  submission routine in
  `internal/session` used by every automatic typing path (the prompt,
  handoffs, broadcasts; a person's own keystrokes stay raw): it writes the
  text and the Enter as the agent needs them (bracketed paste and a separate
  carriage return after a short pause are the leading candidates — Codex's
  TUI turns an Enter inside a fast burst into a newline), holds no session
  or engine lock across the pause, rechecks that no prompt appeared in
  between, is cancellable, never re-sends a prompt after an uncertain
  result, and marks the member prompted when the Enter is written so a
  `done` from before it never starts a dependant. It is verified live
  against both agents on this machine before the plan closes, with an
  agent's response as the proof (not the echo): the initial prompt, a
  handoff and a broadcast, short and long, in a fresh worktree. A trust
  dialog is never typed into: in a never-trusted repository the typed Enter
  answered it (Claude Code chose "No, exit"; Codex chose "Trust and continue"
  and saved the trust), so the member's output is watched for each agent's
  dialog text, the prompt is held, the member shows "needs you" with the
  dialog's words, and typing resumes after a person answers; a worktree of a
  trusted repository shows no dialog, so the README says to trust the
  repository once (Codex can be trusted per launch through its config
  override when yolo is on; Claude Code has no safe equivalent). The web's
  reply boxes (the quick reply, the broadcast bar) go through the same
  routine so a person's reply runs too. A terminal's automatic report
  (xterm answering Codex's cursor-position query) no longer counts as input
  that clears a member's needs-input state. Whatever first-run or workspace-trust dialog
  an agent shows in a fresh worktree is handled so that the prompt is not lost;
  if that needs a per-agent setting (a pre-trusted directory, a flag), the
  adapter carries it and the README says so.
- **Tiles are interactive, and they fill their width.** On the wall and on
  the crew view, a member's tile is a live terminal a person can type into in
  place: clicking the tile focuses its terminal, keys go to the session, and
  the full view is still one action away (a button on the tile or a
  double-click). The investigation found why tiles leave 16–37 % of their
  width blank: a tile renders the PTY's grid scaled to fit (the PTY's aspect
  ratio, not the tile's), and its hello carries xterm's default 80×24, which
  the server applies for an admin viewer, so opening the wall resets every
  session to 80×24. Only more columns can fill a tile, so a tile fits its own
  pane and sizes the PTY the way the full view does: the PTY follows the
  latest viewer that attached or resized, tile or full view (latest controller
  wins, unchanged), a tile re-asserts its size when it becomes visible again
  (after the full view closes), and two viewers open at once alternate only
  when one attaches or resizes, never continuously. The scaled rendering
  stays for view-only links (the join page's run tiles when the link grants
  view), and a scaled viewer's hello carries no size — `cols: 0, rows: 0`
  means "take the PTY's size" (a protocol change in `internal/proto`,
  `web/app/utils/protocol.ts` and `docs/protocol.md`, with its limit and a
  test), so no viewer resets a session by accident again. Plain-key
  shortcuts pause while a tile's terminal has focus, as in the full view; the
  Alt chords still pass through. A headless check measures the unused width
  on the wall and the crew view at two viewport sizes (under one cell) and
  that a session seeded at a large size is sized by the tile that last
  fitted it, never reset to the 80×24 default; the hello rule is: `(0, 0)`
  means "follow the current size", anything else is two values in 1–500 as
  today, zero stays invalid in a resize message, and an older client's
  nonzero hello keeps its meaning. Hello sizing never grants control: the
  role still comes from the token on both transports.
- **A global yolo flag.** `yolo: true` in the config, `CONDUCTOR_YOLO=1` and
  `conductor serve --yolo` make every launched agent skip its permission
  prompts: each built-in carries its recipe (arguments appended to the
  command and environment set at launch, as researched per agent), a saved
  agent can edit it, and an agent with no recipe is launched as before with a
  notice. The server default can be overridden per launch (the Launch dialog
  and `POST /api/sessions` take `yolo`) and per crew (the crew editor; the
  members follow it). A session shows a "yolo" badge in its header, the wall
  tile and the sidebar when it runs that way. The recipe goes through the
  single launch path in `createLocalSession` as argv and env (argv arrays,
  never shell text; the recipe's env goes through the same allowlisted
  environment as the agent's own env, never the privileged inject map),
  bounded like the catalog's other fields, documented per agent in the
  adapter matrix with what each flag really disables (Codex's bypass also
  drops its sandbox). Inheritance: a launch or crew with no `yolo` follows
  the server default, `false` disables, `true` enables; a crew's effective
  choice is fixed when the run is created so members started later follow
  it; a saved agent that omits the recipe inherits its base's, an explicit
  empty recipe disables it, an explicit recipe replaces it whole. The badge
  means "Conductor applied this agent's recipe", not that no dialog can ever
  appear; an agent without a recipe gets a notice and no badge. Hosted
  sessions (`conductor host`) are out of this round's scope.
- **Playwright tests for the example crews.** An in-repo suite
  (`web/e2e/`, `@playwright/test` pinned to the version matching the cached
  Chromium, a `make test-e2e` target, a CI job that installs Chromium) starts
  a built server with its own data directory and a test config whose catalog
  overrides the `claude` and `codex` ids with a stub agent script: the stub
  prints a banner, reads the prompt line, echoes it, runs the merge the
  example prompts ask for (recognising only the expected `crew/<run>/<member>`
  targets built from validated ids, run as argv, never evaluating prompt
  text), commits a file on its branch, reports done
  through `"${CONDUCTOR_BIN:-conductor}" notify`, and answers handoffs the
  same way. The tests load the examples, launch the todo app in a scratch
  repository, and assert on the crew view, the Crews page (the run under its
  crew with member statuses), the sidebar grouping, the wall (typing into a
  tile reaches the session), and the feed (prompt typed and run, handoffs,
  starts in order, the merges succeeding), plus the other three examples
  launching. One live test, skipped unless `CONDUCTOR_E2E_LIVE=1` and the real
  `claude`/`codex` are on the PATH, launches a one-member crew per agent with
  a tiny prompt and asserts the agent answered without a person pressing
  Enter.
- **An ended session needs nothing.** When a session's process exits or is
  killed, its attention state is cleared at the same moment its status
  becomes exited or stopped (the needs-you count, the badges, the dots and
  the sidebar section follow; the activity log keeps the prompt's history).
  Today the state lingers, so a killed agent keeps saying "needs input".
- **Agent sessions are named and resumable.** Each built-in agent carries a
  session recipe alongside its yolo recipe: how Conductor sets the agent's
  own session identity at launch where the CLI allows it (Claude Code
  `--session-id`, Goose's named sessions, …), where to capture it when it
  cannot be set (the hook payloads Conductor already receives, or the
  agent's own session store), and the arguments that resume that identity.
  The identity is stored on the Conductor session (`agentSession`, in the
  session info and on the stream) and shown in the header. An exited or
  stopped session offers **Resume** (its header, the wall tile, the sidebar's
  exited section, and a crew member on the crew view): a new Conductor
  session with the same agent, name, working directory, arguments, yolo
  choice and crew membership (a member keeps its branch and worktree),
  launched with the agent's resume arguments for the stored identity; an
  agent without a recipe is relaunched plainly and the UI says so. Ids are
  validated per agent and passed as argv, never shell text; the per-agent
  facts come from the research report and are marked verified or from docs
  in the adapter matrix.
- **Broadcast goes to everyone by default.** On the crew view the broadcast
  bar sends to the members ticked beside their tiles; the selection started
  empty and was reset to empty on every re-read of the run, so the button
  always read "Send to 0". The selection now defaults to every member with a
  live session, a member that starts later is selected when it appears, an
  untick survives re-reads and run events, and the button's count is the
  number of selected members the server will type into (those waiting on a
  prompt are counted separately as "N waiting skipped", since the server
  skips them). The run view's reload never clears the selection.

### Open verification (round 4)

Round 5 gave each of these a test tier (`docs/round5-plan.md`, Phase B).
What runs where:

**In CI, against the stub agent (`make test-e2e`, web/e2e):**

- The interactive tiles (item 1): `tiles.spec.ts` measures every tile on the
  crew view and the wall (less than a cell beside its grid), types into a
  focused wall tile without leaving the wall, and sends the Alt chords to
  the page. `webgl-probe.spec.ts` records that Chromium 1117 headless has no
  WebGL2 whatever flags it is given (`--use-gl=angle` with
  `--use-angle=swiftshader`, `--enable-unsafe-swiftshader`, `--use-gl=egl`
  were tried), so the tiles are measured with the DOM renderer and
  `data-renderer` says which; the WebGL look in a real browser stays by
  hand.
- The trust hold (item 2): `trust.spec.ts`, with the `codex-untrusted` stub
  drawing "Trust this folder?": held with the question on the API and the
  Crews page, arrows keep it, Enter in the terminal answers it and the stub
  saves the trust as Codex does; with yolo the override makes the question
  not appear and nothing is saved; a broadcast into a held member waits.
- Resume (item 3): `resume.spec.ts`: a Claude-shaped session resumed with
  `--resume <id>` and its transcript, one without a turn relaunched afresh,
  a crew member resumed in its kept worktree and branch without a second
  prompt, a Codex-shaped session resumed by its captured thread, a gone
  working directory refused, and (item 8) an ended member of a stopped run
  resumed in place, and Resume run as a new run.
- Codex's hidden title thread (item 6): `title-thread.spec.ts`, with the
  stub reporting the title turn first: no false needs input, the user's
  thread captured, one done.
- The identity probe: `identity.spec.ts` (the stubs named by their version
  output, the goose impostor named and refused).

**Nightly, on the network and against freshly installed CLIs
(`.github/workflows/nightly.yml`):**

- The built-in sites (item 7): `make test-network`, each site answering
  within its own domain; passed on 2026-10-03 from this machine.
- The recipe flags (items 4 and 5, as far as a help text tells): `make
  test-recipes`, each CLI's `--version` matched by its adapter's probe and
  every flag of its yolo, session and injection recipes found in its help
  (or its subcommand's); the version and help outputs are uploaded so an
  unverified probe pattern can be closed with real text. On 2026-10-03 this
  machine identified Claude Code 2.1.288, Codex 0.159.0, Antigravity 1.2.14
  and Copilot 1.0.91 with every flag in their help, and named the goose on
  its PATH as the Go migrations tool.

**Live, with test accounts (`make test-live`, `.github/workflows/live.yml`,
the `live` environment holding `ANTHROPIC_API_KEY` and `OPENAI_API_KEY`):**

- `live.spec.ts`, for Claude Code and Codex: a typed prompt answered
  without a person pressing Enter (Codex's title thread raising no false
  needs input); the trust question held in a repository the agent has not
  seen and answered by Enter in its terminal (the live check of the
  `trustPrompt` words and of what Enter selects); yolo (Codex asks nothing
  and saves nothing; Claude Code still asks); a conversation resumed after
  its session ended, which still knows the word it was told. Round 4's own
  live run (`PASS live4`, the spec 2 of 2) passed on 2026-10-02 against
  Claude Code 2.1.287 and Codex 0.159.0.

**Still by hand:**

- The WebGL renderer's look in a real browser (headless Chromium has none).
- Antigravity's and DeepSeek Harness's recipes and trust dialogs (no
  automatable sign-in), Copilot's hook `sessionId`, and the recipes of the
  CLIs the nightly job cannot install.
- Codex's "Hooks need review" dialog on a machine where Conductor's
  `~/.codex/hooks.json` is not yet trusted: trust the hooks once in Codex
  before a crew run.
- The live tier on 2026-10-03 from this machine: Claude Code 2.1.288 passed
  the typed prompt and the yolo check; its trust question highlights "No,
  exit" first, so the trusting answer is Down then Enter (the spec presses
  that; Codex's is Enter alone). A resumed Claude Code member answered the
  word it was told (PINEAPPLE) once the broadcast waited for it to be ready.
  On 2026-10-04, after `codex login`, all four Codex rows passed against
  Codex 0.159.0, and the four Claude Code rows against 2.1.289 once the
  trust row waited for the run log's note (its first run read the log a
  moment before the engine wrote "solo asks").

### Deferred (round 4)

- Capturing the agent's session id through the plugin agents (OpenCode,
  oh-my-pi, pi's mid-process changes, Amp, DeepSeek Harness): their plugins
  would pass it to `conductor notify`; until then they relaunch.
- A resume record that outlives `exitedRetention` (a bounded store in the data
  directory), so that a session can be resumed after it leaves the list.
- Yolo and Resume for hosted sessions (`conductor host`).
- `conductor up --resume <run>` (the API has `POST /api/runs/{run}/resume`
  since round 5).
- aider's chat-history file as its session handle.

## Round 15: copy and paste, attributions, invites into the open window, and three checks (started 2026-10-10)

The plan is `docs/round15-plan.md`: the owner's pick of the todo, one pull
request each from main.

- **Copy and paste, as Windows Terminal has them.** Before, only xterm's own
  paste (Ctrl+V, ⌘V, Shift+Insert) and ⌘C on macOS worked. Now Ctrl+Shift+C
  and Ctrl+Insert copy the selection (Ctrl+Shift+C taken from Chrome's
  element picker even with nothing selected), Ctrl+C on a selection copies and
  clears it without the interrupt, and Ctrl+Shift+V and Shift+Insert paste
  through the browser's own paste into xterm's text area, so bracketed paste
  is kept and it works where the page cannot read the clipboard (plain HTTP).
  Right-click copies a selection and otherwise pastes (`navigator.clipboard`,
  or a note to use Ctrl+Shift+V where it cannot be read); Shift+right-click
  opens the terminal's menu (Copy, Paste, Select all, "Right-click pastes",
  kept per browser). A view link copies and never pastes. The policy is
  `utils/terminalClipboard.ts`; the `?` list gains "The terminal". Tests:
  vitest (every key and click case, the setting), Playwright
  `clipboard.spec.ts` on a real shell (a word double-clicked and copied with
  Ctrl+Shift+C; Ctrl+C on a selection copying with no `^C`; Ctrl+Shift+V and
  right-click pasting a command that runs; right-click on a selection copying;
  the menu and its toggle; a view link copying, its Paste disabled and its
  paste reaching nothing; three runs out of three). No server change.

- **Codex adversarial reviews, a rule.** The owner reviews with OpenAI's
  Codex plugin for Claude Code at work, and its review of this repository
  raised eight findings (their investigation is its own report). AGENTS.md
  gains "Reviews": every pull request gets `codex-companion.mjs
  adversarial-review` against `origin/main` with a focus on the risks it
  touches, after everything is committed (`--base` reads commits only), and
  again for commits made after it; each finding is reproduced or traced
  before it is fixed or refuted, and the pull request lists them. The plugin
  (1.0.6) was installed on this machine and its first review was of this
  change: three findings, all three confirmed in the plugin's code and taken
  (commit before reviewing, the plugin's root from the registry rather than
  the newest cached copy, and no reverse `/code-review` clause, which needs
  an open pull request and skips drafts and simple ones). Its second pass
  found the registry can list the plugin per project as well as for the
  user, so the lookup became `scripts/codex_review.py`: this repository's
  installation, else the user's, an ambiguous registry refused (unit tests
  with another project's entry listed first). The third pass found three
  more, taken: a linked worktree now finds its main checkout's installation
  (git's common directory), the registry follows `CLAUDE_CONFIG_DIR`, and
  `--cwd`/`-C` are refused, the review running where the script is run.
  The fourth found the plugin also takes `--C` and `-cwd`, and an inherited
  `GIT_DIR` (a git hook's) pointed every lookup at one repository: flags
  are now an allowlist (`--base`, `--scope`), git's repository-local
  variables are dropped, and the `GIT_DIR` test fails without the fix.
  Security findings, from a review or otherwise, stay out of this public
  repository until fixed (the owner's call): AGENTS.md says where they go.
- **An invite goes to the open window, not a reload.** Before, a
  `conductor://` invite the app received while its window was open loaded
  the join page in full, dropping whatever the window held. Now the main
  process sends it over the bridge (`conductor:invite`, with an id) once the
  page has said it listens (`conductor:inviteReady`, from the preload's
  `onInvite`, the sender checked against the main window), and the workbench
  routes to `/join/<token>?server=…` in place (`plugins/desktop-invite.client.ts`,
  the server as `joinServer` reads it: lower-case host, no default port).
  The page reports the invite taken (`conductor:inviteTaken`) only once the
  router has arrived; until then it stays pending, the latest winning, with
  a 10 s fallback that loads the join page in full (for a page from before
  the bridge, a refused invite, a failed navigation). While the main frame
  loads a document neither word counts, since it may come from the page
  being replaced; when loading stops the page is asked again
  (`conductor:inviteAsk`). The join page is keyed by token and server, and a
  chat thread from another server by its server too (`chatThreadKey`), so an
  in-place invite never shows or sends one server's state on another's
  page. Codex reviewed it four times: a late "ready" from the outgoing page
  and a host written in capitals or with `:443` could each lose an invite;
  the page said "taken" before its navigation landed; the same token on
  another server reused the old join page; and chat threads could cross
  servers. All fixed, the fourth pass approving. Tests: vitest
  (`InviteDelivery`: sent at once, held until ready, the latest wins, a late
  ready or take during a load ignored and asked again, a refused invite
  falling back, a stale id; parser-to-route round trips; `chatThreadKey`),
  Playwright `invite.spec.ts` (the invite routes in place and is taken,
  refused ones answer false, the same token for another server asks that
  server anew; the specs that use the join page pass), and the desktop
  smoke test (an `open-url` invite into the running app keeps the window's
  state).

- **THIRD_PARTY_NOTICES: the licences of what ships.** Generated by
  `scripts/notices.py` (standard library only; `make notices`) from what each
  build carries, not what builds it: the Go modules compiled into the
  binary (`go list -deps` for linux and darwin, amd64 and arm64, plus Go's
  own licence at the pinned toolchain), the npm packages in the web bundle
  (`web/build/bundledPackages.ts`, a Vite plugin listing the client
  bundle's modules and assets during `make web-build`), the desktop app's
  production packages and Electron, the Inter font and the Lucide icons.
  Five packages ship without their licence text; the upstream texts are
  kept in `scripts/notices/` (lazy-val's is the standard MIT text with its
  declared author, since no copy exists upstream). Notices no package
  licence carries are added: a source file's own comment when its copyright
  holder is not one its package's licence names, for every Go file compiled
  for the four platforms (the runtime's memmove from Inferno, the math
  package's Sun Microsystems notices, keywrap's author) and every package
  file the web bundle's code was built from, its workers' builds included
  (xterm.js crediting Fabrice Bellard, the marked copy inside Monaco, the
  JSON worker's glob crediting Nick Fitzgerald, the DOMPurify Monaco
  vendors, whose banner says "(c) Cure53" with no "Copyright"); 57 in all. Go runs at exactly
  the toolchain go.mod pins (`GOTOOLCHAIN`), checked, so a newer go on the
  PATH cannot scan its own sources under that name. And what a package
  builds from other projects (Oniguruma's COPYING for the wasm Shiki's
  engine inlines; DOMPurify's licence and the Codicons font's attribution
  under CC BY 4.0, both of which Monaco's own notices leave out;
  tm-themes' and tm-grammars' NOTICE files for the themes
  and grammars Shiki ships, every one a chunk of its own, taken at the
  versions Shiki 4.4.3 was built from and pinned to it, so an upgrade stops
  the script until they are taken again). Chromium's notices ship in the
  desktop app's resources, since electron-builder deletes them from a macOS
  app. Identical texts print once; the file is about 1.1 MB, most of it the
  grammars' notices. Codex's reviews found these gaps in six passes; each
  was confirmed in the files and is pinned by a Go test, the comment scan
  by Python fixtures (`scripts/notices_test.py`). The file is embedded (`notices.go`) and served at
  `/third-party-notices.txt` by every server and switchyard, linked from
  the switchyard pages' footer and Settings → the app's License row; it
  ships in the desktop packages (`extraResources`) and the Docker image
  (`/usr/share/doc/conductor/`). CI's go job checks it is current
  (`notices.py --check`); AGENTS.md says a dependency change regenerates
  it. Tests: Go (the direct modules named at their versions; served
  everywhere), vitest (`bundledPackages`, the About link), the desktop
  packaging test, Playwright (the link opens the text).

- **A right-click is the program's while it holds the mouse.** A probe
  with a program that asks for SGR mouse reports (as Codex's TUI does)
  showed clicks, drags and the wheel reported to it, as they should be,
  but a right-click reported and then pasted the clipboard into it as
  well: the terminal's own right-click (round 15's copy and paste) did
  not look at the mouse mode. Now, as in Windows Terminal, a right-click
  over a program that holds the mouse is the program's alone; Shift (with
  which xterm takes the mouse back) still opens the terminal's menu and
  Shift+drag still selects; a view link, whose reports reach nothing,
  copies and opens the menu as before; when the program lets go, a
  right-click pastes again. What a right-click does is decided by its
  press (`RightPress`): on Windows the menu event comes after the release,
  and a program that let go in between, or a Shift let up in between, no
  longer turns the click into a paste or swallows the menu. The
  keyboard's menu key always opens the menu (noted as it goes down, since
  Firefox on Windows sends its menu event like a click's). On a Mac
  nothing takes the mouse back (xterm's Option forcing also turns
  Option-click into cursor keys for the program), so a program that holds
  the mouse gets Shift+clicks too; ⌘C and ⌘V still copy and paste.
  Codex reviewed it seven times (the press deciding the click, the
  forcing key at the press, Mac Control-click, the keyboard's menu in
  Chromium and Firefox, an abandoned press, a racing spec), the seventh
  approving. Tests: vitest (`rightClick`, `RightPress`, `menuPress`,
  `menuFromKeyboard`, `forcesSelection`), Playwright `mouse.spec.ts` (a
  program logging what it reads: the right-click reported and nothing
  pasted, the wheel and a drag reported, Shift+right-click opening the
  menu and Shift+drag copying with the program hearing neither, the menu
  key after an abandoned press, a press and menu event sent apart with
  the program letting go between them, right-click pasting once it lets
  go; it fails on main's build at the paste, and each later step fails
  without its fix). The check with the real Codex and Claude Code in the
  installed app comes with the next release candidate.

- **The mouse with the real agents, and Shift+drag over a program that
  asks for every move.** Checked on 2026-10-11 in the installed v0.7.0-rc.7
  Windows app (its server in WSL) with Codex 0.161.0 and Claude Code
  2.1.296, driven with the system's own mouse events. Both agents now turn
  mouse reporting on (Claude Code's full-screen view too). The wheel
  scrolls each agent's own view, xterm's stays put; a click and a drag are
  the agent's (Codex draws its own selection); a right-click is the
  agent's, no menu and no paste of ours (Codex copies its own selection,
  "Copied 35 chars to host clipboard"; Claude Code pastes the clipboard
  itself, the page reading nothing); Shift+right-click opens the
  terminal's menu over both. In a shell with no mouse reporting a drag
  selects, a right-click copies and clears it, the next pastes. But a
  Shift+drag left no selection over either agent: both ask for every move
  (1003), xterm reports a move with no button held, Shift or not, and
  takes the report for typing, which clears the selection; the probe of
  `mouse.spec.ts` asked for presses and drags only. Now (#115), while a
  program asks for every move, a move with no button held is kept from it
  while Shift is held or a selection shows, after xterm's own listeners on
  its screen have seen it (link hover still works); a click is the
  program's and clears the selection, and its moves are the program's
  again. Codex reviewed it three times, the last two approving. Tests:
  vitest (`withholdsMove`, `pressClearsSelection`), Playwright (a probe
  asking for every move, and one in xterm's first encoding and on a view
  link; both fail on main's build).

- **Codex's hook trust lasts.** Checked live on 2026-10-10 with the real
  Codex 0.161 in a throwaway home (its own `CODEX_HOME`, a copy of the
  login, deleted afterwards; no Enter typed into a startup screen).
  Answering "Hooks need review" with trust writes one table per hook to
  Codex's `config.toml`, keyed by the hooks file, the event and the hook's
  place in it (`[hooks.state."<…>/.codex/hooks.json:stop:0:0"]`), holding a
  `trusted_hash` of that hook's definition. A second launch, and every one
  after, ran the hooks without asking. Changing a hook's definition (the
  test moved the `conductor` binary the command names) made Codex ask
  again, for that hook only. So the question comes once per machine, and
  again only when Conductor's hook command changes: a `conductor` binary at
  a new path (a build run from another checkout), or a release that changes
  the hooks Conductor writes. The desktop app runs its binary from one
  place across updates, so an update alone does not ask again. Nothing to
  build; the todo's entry is closed. Round 13's note that `codex exec` did
  not run the hooks after a trust was not looked at again: Conductor
  launches Codex interactively, and those launches keep the trust.

- **Hardening from the review and an audit.** The Codex review's findings
  (investigated before the round) and an audit by OpenAI's defensive-security
  model led to these changes, one pull request each. Each was reviewed by
  Codex until it approved, and each has Go tests (and vitest and Playwright
  where a person sees the change).
  - **Viewers on links** (#101). A link's role is checked again as a viewer
    attaches, under the lock a revoke takes. Every revoke, an expiry, a
    forgotten run, a switchyard run that no longer names a session and the
    kept-link sweep close the viewers attached through the link. A guest page
    whose link expired says so.
  - **Submissions on a host** (#100). A `conductor host` viewer's
    submissions wait in a bounded queue of their own, in order, each with its
    deadline from arrival.
  - **Files on a host** (#103). A `conductor host` session refuses the files
    of the server on its machine, as a server session does. It knows them
    from where `conductor serve` would find them: `~/.conductor`, the
    desktop app's directories and settings, `CONDUCTOR_DATA_DIR` and
    `CONDUCTOR_CATALOG_PATH`. A config file elsewhere comes with
    `--server-config`, and a data directory the Windows app moves for its
    WSL server with `CONDUCTOR_DATA_DIR` (README, the Files tab).
  - **Neovim** (#104). The editor's Neovim is kept for the owner's own
    windows. A link or an invite edits with the editor's keys and Save. On a
    host, a server's window is the owner's only with `--server-is-mine`, for
    a server on this machine. The Neovim child inherits no `CONDUCTOR_*` or
    loader variables.
  - **Logs** (#105). Request and panic log lines name a request by its route.
    A generated workbench token is kept in a 0600 file under the data
    directory, and its path, never its value, is shown.
  - **git** (#106). Conductor's own git calls start no program a repository
    configures (hooks, fsmonitor, pager, external diff, textconv, filters).
    Worktrees check out in two steps. `.git` is read only in the Files tab.
  - **The switchyard** (#108). It is told a published session's state, not
    its chat or activity, and less of its details. Its workbench token has no
    role on published sessions. Its stop ends the publication, not the
    session. `conductor host --link` mints a link over the host's connection.
    A fresh desktop install says its sessions are published, and the docs say
    what a switchyard is trusted with.
  - **Slow viewers** (#109). A client's transport closes on its own goroutine,
    never under the session's lock. Revokes and shutdowns wait for every
    client still closing, and those count toward the viewer cap. A viewer
    moving to the relay takes over its own slot. The registry asks sessions
    outside its lock. A file larger than about 960 KiB opens cut short and
    marked truncated.
  - **Relayed traffic** (#110). What hosted sessions' viewers send their
    hosts is bounded per source address (`viewerRelayKBps`) and in bytes in
    the host's and each viewer's queues. A save is waited for while its
    connection lives, and its answer marks saved only the text it sent.
  - **Desktop tests** (#107). They fetch Electron's binary before they start.
  - **Folders** (#112). A launch's working directory opens through the
    root it was checked against, so the folder a session starts in is the
    one the check named.
  - **Saving** (#113). A save asked for while another is on its way says
    so under the editor's tabs instead of passing silently; the next save,
    once that one is answered, saves what changed since.
  - **A relayed viewer's last frames** (#114). What a hosted session's
    viewer sent just before its connection closed reaches the host before
    its leave (a reply box sends, then closes): its connection is read for
    at most 2 s more once a write to it has failed, and nothing after a
    frame dropped then goes. It was #110's regression, found by the
    v0.7.0-rc.7 release run (a relayed submission's test, 4 in 120 under
    load before, 600 of 600 after).

- **v0.7.0-rc.7 in the app, and the sidebar's five tasks walked once.**
  Tagged on main at 45bd86f (#100–#113) on 2026-10-11; the switchyard runs
  it (rc.6 kept as `conductor.prev`), and the owner's Windows app was
  updated to it (the owner answered the installer's admin prompt). The
  mouse check is above. The five tasks of `docs/sidebar-tasks.md` were
  walked once in that app, as the script's newcomer would, with a
  throwaway crew of two Codex members and a Claude Code one. Each
  intended path held: Needs you on top with the run's block and the member
  asking first, answered from the row; Share on a member's row making and
  copying one view link in one step; the run header's Stop asking in the
  row and stopping every member; Crews → the crew → Runs → Open showing
  the earlier run; the words "Saved crew" and "plans … nothing runs until
  you launch one" against "run started 09:44 · 3 agents". What went wrong
  is in `docs/tasks-todo.md` (Bugs): a Codex member's command approval
  missed or shown as "terminal bell", Codex's turn-end bell leaving a
  member in Needs you, Codex's trust and update questions' words, and
  small layout slips. The watching, with two coworkers, stays the
  owner's.

- **Codex's questions** (#121). What the rc.7 app showed wrong with Codex
  0.161 is fixed. A command approval ("Would you like to run the following
  command?") is a question with its words and choices (`y`, `p`, Esc),
  however the hooks file is set up: questions an agent asks during its work
  (a catalog question marked `anytime`) are read off a model of the
  terminal's screen (`internal/session/screen.go`), since Codex redraws
  only the cells that change, and while one shows nothing is typed into it,
  neither a person's reply nor a crew's prompt (a handoff waits for the
  answer). A done is no longer undone by its own turn's bell, which Codex
  rings as it ends a turn; a new turn's bell still asks, unless it comes
  within half a second of that suppressed one (the bell's debounce; fixed
  in #170, below). The trust
  question's "No" says it quits. The update question reads "Update available
  0.161.0 → 0.162.1" and offers "Skip until next version" (the key `3`,
  checked against the real Codex in a throwaway home: it skips at once and
  never touches the install). Codex reviewed it ten times, the tenth
  approving. Tests: Go (the screen model with a fuzz target, about 25
  approval cases, the turn's bell, a crew prompt waiting for the answer),
  Playwright (`approval.spec.ts`, `trust.spec.ts`). Still to see by hand: a
  real approval in the next release candidate's app.
- **More hardening** (one pull request each, each reviewed by Codex until it
  approved, Go tests and, where a person sees it, vitest and Playwright):
  - **Kept links** (#117). A switchyard saves and deletes a kept link's file
    in one order, deletes one it could not at once on its next sweep, and
    tells the host a revoke is done only once the file is gone
    (`store_unavailable` until then). A connected host's links are never
    swept, however long it stays connected; an open host's links give their
    place in the address's count to a new link once their host has been gone
    five minutes, and only when that room is needed.
  - **A host's control connection** (#118). What a `conductor host` writes
    to its server goes out from one writer per connection: its own messages
    first, relayed viewers in turn, a viewer too slow to keep dropped alone,
    and a server that reads slowly (a switchyard's bound on what open hosts
    relay) never closes the connection.
  - **Files** (#119). A deny entry not on disk is matched by its path; a
    server session also refuses other Conductors' files at their usual
    places on the machine (`~/.conductor`, the desktop app's directory, its
    settings and the data directory they name, which the app passes as
    `CONDUCTOR_DESKTOP_DIR`), not every directory another server could be
    configured with; a view
    link reads nothing of `.git` nor git's settings and credential files; the
    Files status lists the folder's own working tree; a show reads the path
    from the commit its revision names.
  - **Run updates** (#120). A run's members carry a revision to the
    switchyard, which takes only newer ones; a failed sync is tried again.
  - **Writes to a program** (#122). Every write to a session's program
    (keys, a submission's paste and Enter, a crew's prompt) goes through one
    writer per session, in order, and a sender waits at most 10 s for it. A
    write not yet begun when that time runs out fails its sender alone, and
    once one has run out the next are refused at once; a submission whose
    text has begun is accepted all the same and finishes when the program
    reads again, keeping the session's turn until then. The host's own
    terminal keeps waiting for its program, as a terminal does. On a host, a viewer's keys and editor requests leave the frame loop
    for bounded queues. An editor's Neovim keeps to its request's time and is
    registered only while its viewer is attached.

- **v0.7.0-rc.8 in the app.** Tagged on main at 4c79c18 (#114–#123) on
  2026-10-12; the switchyard runs it (rc.7 kept as `conductor.prev`) and the
  owner's Windows app was updated to it. With the real Codex 0.161, a
  command approval was detected with nobody watching the session, with its
  three choices; answering it from the row worked, and the member stayed
  done after its turn. Codex's update question still read "Update now
  (runs" there, where the stub's reads its heading: issue #165.
- **The backlog on GitHub** (#171). The owner, 2026-10-13: the todo's
  entries became GitHub issues (#126–#163), worked on a kanban project board
  whose Status runs Backlog → Ready → In progress → In review → Done.
  AGENTS.md gains "Issues and the project board": every bug known and every
  feature set aside is an issue with its labels; a bug found while working
  is still fixed in that round and gets an issue; a card is Done, and its
  issue closed, once its fix is on main (`Fixes #N` in the pull request that
  finishes it, `Part of #N` closing nothing); security findings never
  become issues until their fix has shipped. `docs/tasks-todo.md` keeps
  only where each entry went; the board helper that moved a card went with #272.
- **A session's end gives its link viewers their last output** (#124). A
  hosted session's guests used to lose what the session printed last and
  were told their link was revoked (4403): the server ended the session's
  links by revoking them, and a revoked viewer is written nothing more. The
  end now has its own path: a viewer closed by the session's end is first
  written what was queued for it, then `session_ended` (4410), on hosted,
  published and server sessions alike; a revoke still writes nothing more
  and stops such a drain. A closed viewer's connection is cut 5 s after its
  close; a publication stays 5 s past its session's end so a revoke still
  reaches the switchyard. Codex reviewed it ten times, the tenth approving.
  Tests: Go (hosted, published and server sessions' last output, the drain
  stopped by a revoke, an expiry and a session leaving), vitest
  (`closedByTheEnd`), Playwright (`hostedend.spec.ts`). Still by hand: a
  guest on WebRTC in a real browser seeing the last output and "session
  ended".
- **A new turn's bell** (#170, issue #135). A bell is paced (one attention
  change per 500 ms), and the pace was stamped before the bell was weighed,
  so the bell that ends a turn, which changes nothing since #121, kept the
  next turn's first bell out when it followed within half a second. The
  pace now counts only bells that change something; a turn after a done
  begins at its first Enter or other state, and output read in the ended
  turn is its own, a notification split between reads included. Go tests.
- **Tests that wait for what they mean** (#177, issue #136). About twenty Go
  tests and two Playwright specs failed on a loaded machine; each was
  reproduced under load, traced, and fixed at its cause (most waited a
  fixed time for an event; `shortcuts.spec`'s G chords needed their two
  keys sent at once, Playwright's acknowledgement of each key taking up to
  a second under load). Three causes were the product's, each with an issue
  closed by it: a viewer let in as its session left was told 4404, not 4410
  (#173); a run chat message posted as a viewer attached to another member
  could miss it (#174); and Enter after a word the editor was suggesting
  took the suggestion and left the line unbroken (#175,
  `acceptSuggestionOnEnter: 'smart'`).
- **A link's going reaches a host, and a line typed for it holds the
  session** (#172). The owner's decision, 2026-10-12, stricter than #122's:
  a submission whose text was typed and whose link was then revoked or
  expired gets no Enter; the session's turn stays held, its view saying
  "Text from a link that was let go was typed and not entered: type in the
  terminal to continue", until a person types in the terminal (a terminal's
  own reports and the pointer's do not count) or the session ends; while it
  holds, the reply boxes take no reply, a question on the screen shows over
  it, and at most 64 submissions wait. A host now hears why a viewer left
  (`viewer_leave` with a `reason`) and when a link its viewers joined
  through goes (`link_gone`, timed on the server's clock), keeps one
  credential per such link, and withdraws what such a viewer asked for; a
  host that loses its server counts every such link gone. A host viewer's
  attaches run one at a time, each tied to the transport its hello came
  over. Codex reviewed it 21 times. Tests: about forty Go tests, vitest,
  and Playwright `held.spec.ts` (two tests), which also showed that CI's
  Chromium now draws WebGL2: a spec never reads terminal text from the
  page, only what the stub wrote, the API and `data-*` state.
- **An agent's own question outlasts its turn** (#193;
  `docs/plans/codex-needs-you.md`, Part B). An agent asks and then waits by
  ending its turn, and the turn's done replaced the question: it left Needs
  you, the sidebar, the Yard and the run's card just as the agent waited. A
  question the agent says is its own (`conductor notify --ask`, which the
  skill now uses, and the MCP tool `ask`) shows until it is answered: in the
  turn it was asked in, the agent's work (a `working` report, Codex's
  working title) and another wait set it aside, and that turn's done shows
  it again, the same question (its id and since, so a reply under way keeps
  its Enter); after the done it goes with a person's keys or reply, the
  agent's clear or next question, or the agent at work in a new turn. A
  question a reply or a person's keys reached is never set aside. A plain
  `--state needs_input` (OpenCode's, DeepSeek Harness's and aider's waits)
  gives way to done as before. A handoff typed while the question is set
  aside, its Enter after the done, is entered all the same and answers
  nothing. Hosted sessions carry the mark both ways (`attention` with
  `ask`). Tests: Go (the plan's orderings P1 to P29 in `internal/session`,
  the routes, the CLI and the MCP tool, the host, the crew's handoffs),
  Playwright `askwait.spec.ts` (a Claude Code stub's question on its row,
  the Yard and the run's card after its turn, answered from the row; a Codex
  stub showing Working, then the question at its done).
- **A question is answered once: first response wins** (#207, the rest of
  #193; `docs/plans/codex-needs-you.md`, "Who may answer a question: first
  response wins", with the owner's decisions of 2026-10-15). A question the
  session shows is open, claimed, resolved, consumed or withdrawn, and one
  function (`resolve`) records an answer. A reply naming the question (the
  row's 1–9, the bar's choice or line, the chat's choice: `answer` over the
  server's WebSocket, WebRTC or the relay) claims it as the writer takes
  its keys, a fresh token each time, and resolves it once the last byte is
  written, recorded by the sender's name (`lastAnswer`, the chat's
  "Answered by", an `input` entry). Meanwhile every other reply is refused
  as being answered, and afterwards as answered by that person (the result
  is kept by id for the last 64 questions, so a late click or a retry gets
  it), the page's toast saying which. No timer ends a question or a claim
  whose write began; only a write refused before it began, or a reply's
  line whose Enter is left out (a prompt or a startup question in its
  pause, its link gone), releases the claim, the session's turn then held
  with the question held over (Ctrl+U shows it open again; an Enter, or
  its choice, consumed). Keys typed in the terminal, a line naming no
  question (`submit`, a chat to the agent, a crew's prompt, a handoff, a
  broadcast) and the terminal's own reports answer nothing: they consume a
  question read off the screen whose answer they pick, an Enter among them
  withdraws a bell's, an OSC notification's or the pattern's wait, a
  person's keys withdraw the agent's own question set aside, and a question
  the agent reported stands until the agent moves on or a reply names it;
  nobody is recorded as answering in the terminal. A bell's or an OSC
  notification's wait is also withdrawn by the screen moving on: every
  session keeps a model of its screen, the baseline is its first quiet
  after the wait, and three rows changed by more than eight cells each
  withdraw it (`source:"screen"`). aider's notifications command reports
  `done`. Gone: the answer by stamp (`answered`, `claims`, `noteAnswering`,
  `answerReached`, `answerWhen`, `typedAt`). Tests: Go (`claim_test.go`:
  the brief's list, and one table of every clearing path through release
  and reclaim; the orderings S1 to S30 across `internal/session`, the
  server's WebSocket, the host's peer, the crew's handoffs and aider's
  assets), vitest (`reply.ts`'s words), Playwright `answered.spec.ts` (the
  refusal names who answered; keys in the terminal leave the question until
  the agent moves on; a page that joins after the answer shows who gave it;
  a bell's wait gone with the screen moving on, and with an Enter).

- **v0.7.0-rc.10 in the app** (2026-10-14, 64629ca). The switchyard runs
  it (rc.8 kept as `conductor.prev`; rc.9 was tagged but never deployed)
  and the owner's Windows app was updated to it. The by-hand check left by
  #124 passed: a guest in Chrome on a view link through the switchyard,
  connected over WebRTC directly, saw a hosted session's last output line,
  then "session ended" with its exit code, not "link revoked".
- **v0.7.0-rc.11 in the app** (2026-10-17, c3666d9). Tagged after #207,
  #216, #223, the notes (#215), the AGENTS.md rule (#244) and the four
  flaky-test fixes (#245, #246, #248, #249), with main green at the tag.
  Published as a prerelease, not a draft, on the owner's decision, so the
  desktop app's updater can find it; the switchyard runs it (rc.10 kept as
  `conductor.prev`; health 200, an open host re-registered in 4 s). The
  owner installed the Windows package by hand six minutes after it was
  published, so the rc.10 app's own update was not exercised; what the
  prerelease did show: the rc.11 app's start-up check answers "update not
  available (latest 0.7.0-rc.11)" where the draft releases had answered a
  `latest.yml` 404. The self-update from a running rc.11 app to the next
  prerelease is the check left for the next cut.
- **The historical review process** (#184, 2026-10-14; superseded for current
  work by the task flow in `AGENTS.md`, which #272 reduced to GitHub's own
  primitives). These were the owner's rules
  after review loops of 20 to 30 rounds. A change to a state machine, a protocol,
  concurrency or a security behaviour starts with `docs/plans/<branch>.md`
  (the events, their orders, the invariants, the scope, the tests),
  reviewed before code. A bug the change introduces, exposes or needs is
  fixed with it; an independent one becomes an issue and the owner says
  whether it is fixed now, in a pull request of its own. Review rounds are
  bounded: one for docs, two for a plan, three for code before the
  findings are sorted, five the stop where the owner decides. The review
  script refuses a branch behind its base, so a review is of what will
  merge, and changes that touch the same files are worked one after
  another.
- **A host's line waits for its server to be heard** (#182). Before it
  enters a line typed by a viewer who joined through a link, a `conductor
  host` requires its server to have been heard from within 15 s; it pings a
  quiet connection every 5 s and gives a connection up after 2 minutes
  unanswered, and the server pings every 5 s while a link someone joined
  through stands.
- **A relayed viewer that falls behind waits on its host** (#185). Past
  512 KiB queued for a relayed viewer, the server tells its host
  (`viewer_behind`), and the host holds that viewer's frames until it
  catches up, so the host's one connection carries what each relayed
  viewer takes; a hosted viewer's socket holds at most 128 KiB unsent on
  Linux.
- **The editor's Neovim takes a substitute** (#188, issues #164, #178–#180).
  Neovim previews `:%s/a/b` as its replacement begins and reports the
  preview with no changedtick, which the bridge could not decode, so it
  lost Neovim at the second `/`. The bridge now reads every notification
  itself, never relays a preview, and turns `inccommand` off. A Neovim lost
  rather than quit keeps its tab's text under a banner with Copy and
  Reopen; keys on their way settle before a tab is let go (`settled`);
  keys Neovim's full input buffer refuses are shown, not dropped.
- **A quick reply names the question it was shown for** (#192, issue #133).
  Every question has an id; a choice or a reply (the sidebar, the Yard, the
  reply bar, the chat, the number keys, a guest on a link) carries it, and
  one whose question is no longer waiting is refused, nothing typed, the
  next question kept. Codex's startup questions take only their own
  answers, read from the screen.
- **A question reads whole** (#191, issue #187): the sidebar's row, the
  Yard's card, the reply bar and a run's card wrap the question rather than
  cutting it to one line; the Yard's focus column scrolls on a short
  window.
- **Codex's bell and its state** (#194, #205, issue #186). Codex launches
  with no bell for a question it asks while it keeps working
  (`tui.notifications` without `async-question`) and with its state in its
  window title (`tui.terminal_title`): checked with the real Codex 0.161,
  the title reads "Starting", then "Working", then "Ready". A title that
  says Codex works is read as a `working` report, so a Codex member's
  stale Needs you gives way while it works; it never overrides a question
  on the screen, a held turn or the done of the turn just ended.
- **What a link's viewer sends passes the link's check** (#198). A
  keystroke, a save (each part, and again before its commit), a file read,
  an editor's open and a chat post from a viewer who joined through a link
  are acted on only while the link still opens the session; a refusal says
  `revoked` or `expired` in words the page shows. A write the program had
  begun to take when the link went still lands (the owner's decision).
- **A host's connection to its server** (#204, issue #183). The server's
  own messages to a host go ahead of the frames it relays, and nothing still
  queued for a link's viewers is written after that link's going; the host
  vouches for its server only by its own answered pings (the owner's
  stricter choice: while a server pauses its reading past the bound, a
  link's line waits within its own time and is then refused); a relayed
  save and an offer's answer run off the host's read loop.
- **A plain connection to another machine needs saying so** (#212).
  `conductor host` refuses a plain `http://` or `ws://` server that is not
  this machine unless `--insecure`, then warns; `conductor up` and
  `conductor crews` the same for a plain `http://` server (they take no
  `ws://`); a server's uplink needs `rendezvous.insecure`;
  the app's Settings refuse such a switchyard with the words why. Only
  lower-case `localhost` and loopback addresses count as this machine.
- **The terminal fills its pane** (#200, #209, issues #189, #201): the
  terminal's 12rem minimum is gone wherever it is shown (the editor
  column, wall tiles, the Roundhouse, run tiles, the paste page), so its
  last row and cursor stay inside a short pane.
- **Test races** (#199, #210, #222, #216, issues #190, #202, #211,
  #217–#219): `chat.spec` waits for a member's first turn to end before
  posting its question; the specs with a hosted session (`chat`,
  `hostedend`, `linkcheck`, `sidebar`) find it by the id its host
  registered and end it through the host before the host is killed; the
  wall test keeps its tile alone. Two more made deterministic after #207's
  merge (#245, #246, issues #242, #243): the queue test counts the
  goroutines its queue started rather than the process's, and
  `mouse.spec` measures a cell from the terminal's settled box and the
  grid the page draws (`data-cols`/`data-rows` on the terminal host).
- **A narrowed wall tile draws its rows on their rows** (#223, issue
  #221). xterm.js 6.0's reflow of a tile with no scrollback moved the
  screen one row past its buffer when the wall narrowed the tile with its
  cursor above the last row and longer lines above it, so the tile drew
  its rows rotated by one and its cursor off its row. Tiles keep a
  1000-row scrollback (a terminal is never made with none), fit without
  the scrollbar's gutter their pane hides, and the wheel over a tile
  scrolls its history, passing to the page at either end.
- **An agent's own question stays in Needs you after its turn ends**
  (#207, issue #193; the model in `docs/plans/codex-needs-you.md`). A
  question an agent asked (a hook's, `conductor notify --ask`'s, the MCP
  ask, Codex's bell) outlasts the done of the turn that asked it, until
  someone answers. First response wins: a question is open, claimed by the
  first answer on its way, resolved when that answer lands, consumed when
  keys typed at its dialog take it, withdrawn when the screen moves past
  it; only a resolve records an answer, raw keys naming no question answer
  none, and a claim whose write is refused or stranded goes back to open.
  Twelve Codex rounds; every finding reproduced with a test and fixed, but
  for round 12's three, which the owner set aside as issues #224–#226 (a
  startup dialog drawn before a stranded line's hold; a wrapped row
  rewritten after the screen grew; a synchronized frame ending on the
  alternate screen).
- **The work flow on GitHub's own primitives** (#252, #254, #273, #275,
  #283, issues #251, #272, #274, #282). The scripted issue workflow #252 and
  #254 added (helpers that claimed an issue and kept review receipts and
  budgets, a CI job checking them, skills and templates to match) gave way
  the next day to GitHub's own (#273, about 11,500 lines removed): one
  issue, one `issue-N` branch in a worktree of its own, one pull request, a
  draft from its first commit with `Fixes #N` or `Part of #N`, the checks of
  every affected surface before each push (`docs/checks.md`), every review
  comment answered, and the owner merges. `AGENTS.md` says it in a dozen
  lines, `CLAUDE.md` is `@AGENTS.md`, and `ci.yml` gains `required-checks`,
  a job that is green only when every other job passed. The workflow #273
  added to run Claude Code on ready pull requests went with #275: no
  Anthropic credential goes into CI. A test that fails a pull request's own
  checks is fixed in that pull request and named in its body; a flake seen
  once that blocks nothing is a line in the standing issue #281 (#283).
- **Checks: a Mac, two races, CI's checks run locally, the weekly runs**
  (#255, #267, #270, #276, #287, issues #256, #220, #269, #271, #286). On an
  Apple Silicon Mac 21 Go tests and 39 Playwright specs failed that pass on
  Linux; all but one came from the machine (`/var` and `/tmp` are links,
  APFS ignores case, the configuration directory is `~/Library/Application
  Support`, no Neovim, bash 3.2), and the tests and their harness now allow
  for most of it, the product unchanged. Not yet: one clipboard spec and
  three mouse specs still expect Windows and Linux keys (⌘C, a Shift+right
  click), which is the owner's call, and the last Bash change was checked
  on Linux, not in a full e2e run on the Mac. The e2e harness needs Bash 4 or later with
  a numeric microsecond clock, finds one on the PATH for its stub agents,
  and stops at once without one. The one Mac failure that was the product's
  was #285: with `ice.publicIp` set, a host's IPv6 addresses were still
  advertised beside it, since the rewrite rule replaces the address's own
  family alone; the peers now gather on that family alone (the mux shown
  through `familyMux`, the network types narrowed), and the mirror case, an
  IPv6 address given on an IPv4 machine, is tested on every machine. Two test races (`credential_test`'s begun write, the serve
  test's terminal) were reproduced under load and fixed in the tests. `make
  preflight` runs CI's checks on this machine, Linux, from the committed
  head, and writes a receipt with a log per step under the git common
  dir's `preflight/`; `--machines linux,mac,windows` runs the Mac's rows
  too (over `ssh mac`) and the Windows host's, and a list without `linux`
  runs only the machines it names. CI's darwin builds are not among the
  local steps. A failed step, an unreachable machine or a subset run makes
  a failed or partial receipt, never a silent skip. The weekly
  `nightly` and `live` workflows, which failed on both their runs, install
  pi and Amp from their new packages (`@earendil-works/pi-coding-agent`,
  `@ampcode/cli`), check Amp's `--dangerously-allow-all` by use since its
  `--help` leaves it out, and run the live tier on Ubuntu 22.04; the catalog
  names pi's new site, github.com/earendil-works/pi.
- **A host back from away clears a question answered meanwhile** (#259,
  issue #208). A `conductor host` now sends its session's attention on every
  connection it starts, the empty state included, in order with the
  session's other changes. The server keeps the attention it had when a host
  went, so a question answered at the host's own terminal while the host was
  disconnected stayed in the listing (the sidebar, the Yard, a switchyard's
  view) as Needs you until the attention next changed; it now leaves when
  the host is back. An attention retry under way when the connection is
  replaced tells the new connection. No protocol change: one more
  `attention` message, the empty one.
- **The Launch dialog's host command carries `--insecure` when the host
  needs it** (#260, issue #213). With the workbench open over plain
  `http://` from another machine, the "On this computer" tab's `conductor
  host --server …` command carries `--insecure`, without which the host
  refuses such a server (#212), and a notice under it says why: the
  session's output and its viewers' keys would travel in the clear and
  unverified, and a workbench served over https needs no flag. The dialog
  reads the address as the host does: `localhost` in lower case and loopback
  addresses are this machine, `LOCALHOST` and `127.1` are not.
- **The Files pane on a very short window** (#261, issue #203). At 1440×230
  the pane's section tabs, filter and breadcrumb took all its height, and
  the tree's rows were laid out under its foot, out of a click's reach. The
  tree keeps a few rows' height and the pane scrolls as a whole when the
  window is shorter than that; the Explorer's hint ("Paths the agent prints
  in the terminal are clickable") gives way below 360 px of window height.
- **Copilot, Cursor, Antigravity and Goose report working** (#262, issue
  #195). The four reported their turn's end and their tools' results and
  nothing in between, so a question one of them asked with `conductor notify
  --ask` or the MCP `ask` tool and then carried on from, or any other wait
  raised mid-turn, stood until the turn ended. Their hooks now report
  `working` from the pre-tool event and, where the agent has one, the
  prompt-submitted event (Copilot's already did), as Claude Code's
  `PreToolUse` does, and such a wait gives way while the agent works; the
  agents table names the events. Antigravity's payload names no event: its
  pre-tool call is told from the post-tool one by the `error` field, which
  only a tool that ran carries. These four install their hooks by hand, so
  hooks installed before this release report as before until installed
  again: Install on this machine on the Events page, or `conductor hooks
  install <adapter>`.
- **The Events page says what reports working** (#264, issue #206). On the
  Routing tab the `working` event reads "An agent is at work", and the line
  under it names what reports it: a prompt submitted, a tool about to run
  (the hooks), Codex's title saying Working. The line wraps rather than
  being cut to one.
- **A wrapped row rewritten after the window grew reads what was drawn**
  (#265, issue #225, one of the three set aside in #207's review). The
  screen model reads a wrapped row joined to the next up to the width it
  wrapped at, and a row rewritten in place after the window grew kept that
  join, so the cells newly drawn past it were dropped: a startup question
  still on the screen could be withdrawn after a held key, or a submission
  let into its dialog. A character other than a blank written at or past
  that width now ends the join; blanks repainting the growth's padding keep
  it.
- **A synchronized frame ends for both screens** (#266, issue #226, set
  aside in #207's review). The end of a synchronized update (`CSI ?2026h` …
  `CSI ?2026l`) laid out only the screen selected at that moment, so a frame
  that erased and redrew the main screen after a resize and ended on the
  alternate screen left the main screen counted as cut: a question the frame
  had erased (Codex's approval, with its outcome drawn) stayed, and typed
  text was refused until the program next erased the whole screen. The end
  now lays out every screen the frame erased whole, whichever is selected.
- **Codex's update question reads its heading** (#268, issue #165). In the
  rc.8 app Codex's update question read "Update now (runs": Codex draws it
  over its splash and writes only the cells that change, so the output's
  tail held "Updat available" and the heading matched nothing there. A
  startup question's words are now read off the session's screen model,
  where the heading reads whole ("Update available 0.163.0 → 0.164.0", from
  bytes Codex 0.163.0 wrote in a PTY), with its two choices, "Skip this
  update" and "Skip until next version". Every place that compares a reply
  or a key with the question reads the same words, so a key typed at it is
  noted as its answer and the question goes when Codex draws its next
  screen.
- **The Settings toast says when the server restarted** (#277, issue #214).
  The desktop Settings page's save toast said "Saved" whatever the save did.
  The app now says what became of it: "Saved; the server restarted" when the
  change restarted the server, shown also by the page that followed the
  restarted server to its new port, and "Saved" when nothing the server
  reads changed, the retry after a failed restart included (the settings
  were kept, so nothing restarts).
- **An Enter at a startup question keeps a stranded line's hold** (#278,
  issue #224, set aside in #207's review). With a reported question showing
  and a line whose Enter was left out waiting for its hold, an Enter typed
  at a startup question (trust, hooks review, update) drawn meanwhile was
  taken as entering that line: the hold went, the reported question was
  counted answered, and a later submission could join the stranded text.
  Keys that may reach a startup question now count as reaching the dialog,
  read off the screen model as one frame: the question's heading on a whole
  frame, a dialog's numbered choice row when the heading has scrolled off a
  short screen, or a frame half drawn or cut by a smaller window while the
  output's recent tail still holds a startup question drawn (a heading
  pushed out of that tail with the screen cut is a gap left open). A numbered list in a program's own
  startup output reads as a choice and keeps the hold, erring on the side of
  keeping it.
- **A submission's Enter waits while a startup question may show** (#279,
  issue #263). A submission's Enter follows a pause so that a question
  raised meanwhile keeps it out, but its guard asked only the session's
  attention, which a trust question drawn during the pause could not take
  while a question reported through the API showed: the Enter went into the
  trust dialog and picked what it highlighted. The guard now reads the
  screen as well, as #278 does, and the Enter stays out while a startup
  question may show. A frame cut or half drawn counts only while the
  output's recent tail still holds a startup question drawn, so a program
  that prints lines and never erases its screen whole (a crew's example
  launch) still gets its first prompt's Enter; a heading pushed out of that
  tail with the screen cut is the gap #279 leaves.
- **Codex's program status** (#280, issue #197). Codex writes the same
  window title, Action Required, for a question that stops its turn and for
  an async one it posts while its turn goes on, so a wait raised while an
  async question was pending (a hook's report, the agent's own question, an
  approval already answered) kept a Codex member in Needs you until the
  title said Working again, up to 30 s after the person had acted. Codex
  0.163 and later also declare their state in a program status (OSC 7501);
  once a session has read one, it is weighed in place of the title (the
  agents table has the states). With Codex 0.164, which reports `working`
  while an async question is pending, such a wait gives way a second later;
  with 0.163 it stays as under the title. A `blocked` status with its kind
  raises a wait in words of its own ("The agent asks for a permission", "The
  agent asks a question", "The agent needs you to sign in") where nothing
  with words of its own shows, so a form no pattern knows shows in Needs
  you. Hosted sessions read no status, as they read no title. The sequence
  is from Codex's source at 0.163.0 and 0.164.0-alpha.3, not a live capture.
- **A question answered in its terminal is offered nowhere** (#284, issue
  #258). A dialog answered by a key typed in its terminal stays a question
  while the program's dialog is on the screen, by design, and now carries
  `answered:"terminal"` from the moment of the key (on the session's info,
  the terminal's `attention` message and a host's report): the sidebar's
  row, the Yard's queue, the reply bar and the chat's choices drop it at
  once in every tab, the run view no longer counts the member as waiting,
  and a page whose reply is refused as answered drops the question it named
  without waiting for the stream. Found on the way and fixed: a dialog
  answered by a key is remembered by its words, so that its redraw is not a
  new question, and the memory went only when the screen settled, which the
  reply bar's rows (resizing the terminal around every question) and a
  program that never erases a grown screen whole (Codex) could put off for
  good, so the next approval with the same words, a turn later, was taken as
  answered on sight. The program's own word now ends the memory: its status
  or title saying it works, its hook or a report saying the turn is done.
- **A share link as a QR code** (#284, issue #144). The share dialog draws
  the link it made as a QR code, for a phone in the room to scan; the code
  holds the link alone. The encoder is the page's own
  (`web/app/utils/qr.ts`: byte mode, level M, versions 1 to 10), with no new
  dependency, and is checked against the codes `qrencode` 4.0.2 makes; no
  phone has scanned one yet.
- **Small things in the workbench** (#284, issues #137, #138, #167–#169,
  #257). A folder picked in the Launch dialog leaves the caret and the view
  at the end of its path. The Files tab's breadcrumb keeps the working
  directory's own folder, and in file mode the folders down to the file,
  whole; the folders above fold into one "…" crumb whose menu lists them (a
  pick copies that folder's path), and the full path is the row's tooltip.
  The Explorer's filter takes a path: with a slash it narrows the tree to
  that folder and what is under it, and a click on a folder clears it. The
  editor's Ask the agent and Comment box takes the editor's width, at most
  56rem, and wraps its heading and the file's location. From the rc.7 app's
  list: a long choice label is cut to its row, whole in its tooltip; a
  stop's Cancel and Stop sit below a row's words, not over them; a stopped
  run's header shows Resume and no Stop; the run page's name gives way last;
  a line Conductor typed (a crew's prompt, a broadcast) reads "sent" in the
  inspector, not "answered" (`op: typed` on its activity entry); a run's
  times are on the 24-hour clock everywhere. An exited row's Resume is no
  longer under the hover actions.
- **Claude Code's dialogs offer their own choices** (#292, issue #288).
  Every Claude Code permission request came with one fixed list, Yes `1`,
  Always for this session `2`, No `3`, whatever its dialog showed; in Claude
  Code 2.1.304 its No sent `3`, which in the Bash dialog switches the
  session to auto mode, and its Always sent `2`, which in the Write dialog
  switches it to accept edits. Claude Code's numbered dialogs (a tool's
  permission, the plan's approval, AskUserQuestion) are now questions asked
  during the work, as Codex's approvals are: read off the session's screen
  model, with submissions held while one shows, each choice its label and
  number as the dialog draws them each time it is asked, and a digit picking
  it at once. Only a dialog that ends the screen with one of Claude Code's
  footers counts, and AskUserQuestion's last page ("Ready to submit your
  answers?", Submit answers or Cancel), which has none, by its own words;
  so one quoted in the transcript or pasted into the prompt asks nothing. A choice that changes the permission mode (auto mode, accept
  edits, bypass permissions; `mode` on the choice in the protocol) gets no
  number key, shows outlined in the warning colour, and is never the
  highlighted first choice. While the highlight is on a field (the plan's
  "Tell Claude what to change", AskUserQuestion's "Type something."), no
  choices are offered and the page says to answer in the terminal. A reply
  is weighed against the dialog as asked: a dialog that changed is a new
  question that takes its own reply, and the same dialog twice is two
  questions. The hook's report carries no choices, and its words say what is
  asked ("Claude Code has a plan for you to approve", or AskUserQuestion's
  own question); a permission question with no choices is answered in the
  terminal. All of this is for the sessions the server launches: a hosted
  session (`conductor host`) reads none of these dialogs and holds no
  submission at them yet, so typed text and its Enter can reach one (#293). A label over 60 characters
  is cut at a word with an ellipsis. Codex reviewed the plan once and the
  code five times.
- **An update is said once, in the workbench** (#294, issue #290).
  electron-updater reports a downloaded update at every check that finds it,
  on start and every six hours, and the desktop app answered each with a
  dialog of its own, so a machine left running stacked them up. The app
  opens no dialog for an update now. Each version is said once, to the first
  workbench window that shows after the download (kept in the settings, so
  across starts too), in a toast with **Restart to update** and **Later**; a
  button in the sidebar's header stays until the update installs, and the
  tray's menu gains `Restart to update to <version>`, which shows the
  workbench and asks there. Whichever asks, a dialog first says what the
  restart ends: the server, and the sessions running on this computer ("if
  any" when they could not be counted). The restart goes ahead only for the
  version the updater still holds: a newer download or a failed one
  withdraws the offer, a restart asked during a check is put off with words
  that say so, and one that starts pauses the checks, stops the server,
  quits and installs; an install that fails quits all the same. An update
  that did not install (the app started again as the version before) is said
  once, with **Show the log**. A session started while the dialog stays open
  is not counted, and ends with the restart. Codex reviewed it ten times.
  Not yet seen: the app updating itself from one published release to the
  next with this code in it, for which rc.12 to the next prerelease is the
  first chance.
- **A server is named by its address alone** (#298). `rendezvous.server`
  (the config file or `CONDUCTOR_RENDEZVOUS_SERVER`) and `conductor host
  --server` (`CONDUCTOR_SERVER`) refuse a URL with a user name or password
  (`user:pass@`, or an `@` alone), a query or a fragment, an empty one
  included, publishing on or off; tokens have settings of their own
  (`rendezvous.token`, `--token`), as the refusal says. Such a
  `rendezvous.server` stops `conductor serve` at start, as a plain-http one
  does, and `conductor host` exits 2 before anything is dialed. `conductor
  host` reads `CONDUCTOR_SERVER` after its flags, as `conductor up` does, so
  its usage text (`--help`, a bad flag, no command) names no server from the
  environment. Logs, errors and replies show a server URL as its scheme,
  host and path, and a hosted session's hooks report to the server's scheme,
  host and path. A failed control dial is said in fixed words by its kind,
  with the host it was dialing: the name does not resolve, the connection
  was refused, the certificate is for another name, is signed by an
  authority this machine does not trust or has expired, it did not answer in
  TLS, the proxy the environment names asks for authentication. While
  publishing is on, the desktop app's Settings refuse such a switchyard
  (`https://@host` and an empty `?` or `#` among them) and one not written
  as the server reads a URL (a space around it, a backslash, a `%` outside
  an escape). A switchyard setting the server would refuse is kept as saved
  with the other settings, never handed to the server, publishing on or off,
  so nothing is published meanwhile, and Settings says why as it opens and
  before it saves. What stops working: a switchyard URL with `user:pass@`
  behind a reverse proxy that asks for Basic authentication (with no host
  token set) now gets the refusal at start; no such setup is known. Filed on
  the way: #296 (a `ws://` or `wss://` server gives a hosted session's hooks
  a notify URL they cannot post to) and #297 (a switchyard at
  `http://localhost:8080` is refused as the server itself).

## Round 14: the bugs the owner met in the rc.4 app (started 2026-10-09)

The bugs in `docs/tasks-todo.md` the owner found using the rc.4 desktop
app on 2026-10-08, one pull request each from main, tests at both ends,
then checked in the installed app (the owner helps with the install). The
order: the shared session's size; the Launch dialog's first listing; the
server log window; the fullscreen button in the app; the directory
picker's cut lists; the shortcuts table; the Yard's header at phone width;
the "error" badge on a shell crew member. The local echo's space came
first, found measuring the echo over the switchyard (round 13, PR #81).

- **A flaky Neovim tab test, and the stale status behind it.** The test
  for keeping a tab's Neovim (#80) failed once in a full local run. The
  editor area kept Neovim's status (the mode, the swap file banner, a
  question) from the editor last in front until the next one reported, so
  after a tab change the status said "normal" for a Neovim that was not
  listening yet; the test waited for that and typed into nothing.
  Reproduced on the old code under load (64 busy loops on this 64-core
  machine): one run in six lost "Ayy" and never saw the unsaved dot. Now
  the status starts empty whenever the editor in front changes (another
  tab, the other keymap) and the test waits for the tab's own report after
  switching back; under the same load, eight runs out of eight, and the
  whole Neovim spec five times out of five.
- **One window sizes a session.** Seen by the owner 2026-10-06: a crew
  shared with a coworker on a laptop shrank to the laptop's columns and
  rows on the owner's 34-inch screen, since the latest controller that
  attached or resized set the PTY's size for everyone. Decided then: the
  size belongs to one viewer, the sizer: one of the owner's own windows (a
  controller with no link: the workbench, a host's own terminal) takes it
  as it attaches while nobody holds it; any other controller's hello and
  resizes are kept as what its window would want and refused without a
  word (`ErrNotSizer`); `resize` with `take` (Fit to my window) makes a
  controller the sizer, and every viewer hears who in the `resize`
  frame's `by`, a hand-over at the same size included; when the sizer
  leaves, the owner's window that attached last takes it at its own size,
  else nobody holds it and the size stays. A viewer moving to the relay (a
  host's viewer whose WebRTC connection failed: the same id taking over
  its old connection, `AttachOptions.Replaces`) keeps the size in either
  order (#130): taken over where the old connection stands, the size stays
  with the id; the old connection's end seen first hands the size on as
  at any leave, and the relay's attach takes it back at its hello's size,
  unless someone took it with Fit to my window meanwhile, which stands.
  The welcome says `sizer` and
  `sizedBy`; a page talking to an older host (no `sizer`) keeps the old
  rule. In the browser a full view that does not hold the size shows the
  session's grid scaled to its pane (the run tiles' scale), a view-only
  link included, and a controller's view says "Sized by Jane · 212 × 54"
  with **Fit to my window** in the terminal's corner; tiles keep their own
  fit. Tests: Go `session` (the owner's window, a second window and a
  control link joining without a resize, their passive resizes refused, a
  take, a take back at the same size, the hand-over to the owner's other
  window at its size, then to nobody), `api` over the WebSocket (a control
  link's resize refused, its take heard by the workbench, the workbench's
  passive resize refused, its take), `hostagent` over the data channel (a
  link on a hosted session following the host terminal's size until its
  take), `proto` (the shapes, an older owner's welcome); vitest
  `terminalSizer.test.ts`; Playwright `sizing.spec.ts` with a 1440-wide
  workbench, a 900-wide laptop on a control link and a view link (the
  laptop scaled with the chip naming Nate, neither window's resize moving
  the size, Fit to my window and back, the view link scaled with no chip;
  three runs out of three; it fails on main's build at the laptop's chip).
- **The Launch dialog's long empty list on its first open.** Seen by the
  owner 2026-10-08 on the Windows app (the server in WSL): the first Launch
  agent after the app starts showed an empty grid for a long while. Two
  causes: GET /api/catalog started each installed agent's identity probe
  and waited for it (up to `probeWait`, 2 s) one agent after another, while
  its comment said they ran together, so a cold catalog waited up to two
  seconds per agent; and the dialog showed a blank grid until the reply
  came, every time it opened. Now the probes are asked for together and
  waited for together (2 s in all; a late one is `pending`), the server
  warms the catalog as it starts (`WarmCatalog`, the lookups and probes the
  first request would make), and the dialog says "Checking which agents
  are installed here…" while its first catalog is under way and, opened
  again, lists the last catalog at once while it refreshes behind. Tests:
  Go (six probes of 700 ms: one wait, not six, the late ones pending, it
  took 4.2 s waiting one by one; after the warm-up the request answers at
  once with none pending), vitest (`launchListNote`), Playwright
  `launch.spec.ts` with the catalog held back three seconds (the checking
  note within a second, never "no agents"; opened again, the list within
  half a second; it fails on main's dialog at the note).
- **The server log window was an empty dark box.** Seen by the owner
  2026-10-06 on Windows, from the tray's Server log; the same on every
  platform. The window was made with no preload, so `window.conductorLog`
  (from `log-preload.ts`, built but never attached) was undefined and no
  line reached the page. Its options are now `logWindowOptions(dir)` in
  `desktop/src/window.ts`, which names `log-preload.js`. Tests: desktop
  vitest (the preload named, the window isolated as every window), the
  Electron smoke clicking Server log in the menu and expecting the server's
  lines in the window (it saw an empty page without the preload). Locally
  the smoke runs on WSLg's display (`DISPLAY=:0`); the X server named by
  this shell's `DISPLAY` was not there.
- **The fullscreen button did nothing in the desktop app.** Seen by the
  owner 2026-10-08 on Windows: the maximize icon in every page header and
  the F key had no effect. The cause was the app's permission handler, which
  granted the pages the clipboard and nothing else, so the page's
  `requestFullscreen` was refused (the todo guessed at a bridge to the
  window's fullscreen; none is needed). `permissionAllowed` in
  `desktop/src/window.ts` grants `fullscreen` too; the page's own state
  (`fullscreenchange`) keeps the icon right. Tests: desktop vitest (the
  clipboard and fullscreen granted, media, geolocation, notifications and
  the rest refused), the Electron smoke clicking the header's button in
  and out of fullscreen (it stayed "Enter fullscreen" without the grant).
- **The folder lists looked whole when they were cut.** The owner,
  2026-10-08: the Launch dialog's working directory list stopped before
  the end of a large folder. The server already listed directories only
  and narrowed by what was typed before cutting at 50, and both pickers
  had a note, but a faint 11-pixel line ("More here than listed or
  marked") under the list that was easy to miss and said nothing of how
  many. GET /api/paths now says `more`, how many directories matching
  what was typed were left out, and `moreUnknown` when the folder held
  more entries than a listing reads; both pickers say "30 more folders
  here: type the start of a name to narrow the list." in readable text,
  and keep "some folders are not marked" for the case where only git's
  marks were cut short. Tests: Go (the count at the default limit and at
  5, a typed start narrowing to a whole list, `moreUnknown` past the scan
  cap), vitest (`moreNote`), Playwright `dirpicker.spec.ts` with 80
  folders (the field's note says 30 more and 50 are listed; typing
  "project-07" lists the ten, whole, and the note goes; the folder
  picker's note says the same).
- **The shortcuts table was too narrow to read.** The owner, 2026-10-08,
  on the Windows app: the `?` modal's three columns sat so close they were
  hard to tell apart. Each row was a grid of its own whose key columns took
  their own widths, so nothing lined up down the table, in a 464-pixel
  modal. Now every row shares one column template (the action, then two
  key columns of one fixed width, tinted so they read as columns), the
  modal is wider (`sm:max-w-3xl`), each group past the first names the
  columns again, and on a phone a row stacks, its two key sets named
  "Terminal" and "Outside". Tests: Playwright `shortcuts.spec.ts` at 1440
  (the modal wider than 650 pixels, every row's key columns starting and
  sized as the first row's, the keys clear of the action, the names
  repeated) and 390 (nothing runs off the side); it failed on the old
  table at the modal's width. No logic changed, so no vitest.
- **The Yard's focused header overlapped at phone width.** Seen 2026-10-08
  in a headless render at 390: the status, transport and viewer badges ran
  under Open page and Stop, and the title was gone, since the header kept
  every badge and button at every width. Below `sm` the transport and
  viewer badges now hide, Open page and Stop fold into a menu (Resume stays
  as an icon on an ended session), and the title truncates rather than
  vanishing, as the session page's header does. Tests: Playwright
  `yardheader.spec.ts` at 390 (the title shows; every visible piece of the
  header on screen and clear of every other; the menu's Open page and
  Stop, Stop stopping the session) and 1440 (the buttons in the header,
  no menu); on the old layout it found "WebSocket · 106 ms overlaps Open
  page". No logic changed, so no vitest.

- **A red "error" badge on a crew member with no error of its own.** Seen
  twice by the owner on 2026-10-08 in the rc.4 app: a shell member wore
  "error" right after its run launched, another after it was stopped, the
  Events page explained neither, and opening it cleared the badges. The
  app's server log that day had two `publish to the rendezvous failed`
  warnings, both `429`: the public switchyard admits open hosts (the app
  publishes every session, with no token) four live sessions and six
  registrations a minute per address, and a home's machines share one
  address, so a crew launched beside other sessions was refused. The
  failure was recorded as an `error` entry on the session ("an agent hit an
  error", red), and never tried again. Now a refused publication is tried
  again when the refusal passes with time (a 429: after 15 s, 30 s, 1 min,
  2 min and 2 min, or the `Retry-After` the switchyard sends, the session's
  end stopping it), the uplink keeps the switchyard's own words and code
  (`open_host_limit`, `rate_limited`) instead of "expected handshake
  response status code 101 but got 429", and the session's activity says
  so in status lines ("not shared through the switchyard yet: …; trying
  again", "shared through the switchyard after 3 tries"), which carry no
  badge; a link asked for meanwhile says the switchyard's words. Tests: Go
  `hostagent` (a 429 with its JSON and `Retry-After` read into a
  `RefusedError`, retryable; a 401 not), `api` (a switchyard of one open
  session per address: the second session noted, not errored, its link
  saying "trying again", shared once the first ends; a switchyard that is
  down noted the same way), vitest (the notes are no event), Playwright
  `publishlimit.spec.ts` with its own switchyard so limited (no error badge
  on either row, the Activity saying why, the second shared once the slot
  frees; on the old code its Activity held the bare 429 as an error).
  Still the operator's to weigh: the public switchyard's four sessions per
  address, or a host token for the owner's own machines.

- **Two flaky trust tests, one of them a real race.** On main after #85 the
  e2e failed once at `trust.spec.ts`'s Codex questions: "Update now" was never
  held. Cause: answering a startup question cleared the question watcher's
  screen after the answer was written, so a program that draws its next
  question at once (Codex's update question after its background-server one)
  could have it wiped before the watcher saw it, and the session waited
  unheld. The screen is now cleared before the answer is written; a session
  test reproduces it with a fake program that draws the next question inside
  the write (it failed three runs out of three before). Running the spec under
  load (64 busy loops) then found a test race at the first trust test: the
  member's held state and the run log's line are two updates, and a read fell
  between them; the test waits for both. Under the same load: 35 runs out of
  35. The Go suite under that load found a third: `TestLinksListReportsActiveViewers`
  read the link list the instant the viewer saw `ready`, which comes just
  before the viewer joins the roster the count is taken from; it waits for the
  count now (30 runs out of 30 under load). Also: the cli tests now run with
  the system's PATH only, since the
  catalog warm-up (#84) ran this machine's real Copilot, which went on writing
  into a test's temp HOME after the test ended (CI has no agents installed).

- **Checked in the installed rc.6 app (2026-10-09).** v0.7.0-rc.6 (8ad9ab1)
  installed on the Windows box (the owner answered the elevation prompt and
  finished the wizard) and driven through winctl-mcp with real keys and
  the app's debugging port. The first Launch agent after the app started
  listed six agents in 112 ms, the checking note showing first; a folder of
  80 listed 50 with "30 more folders here", "project-07" narrowing it to the
  ten; a real `?` opened the shortcuts table 704 pixels wide, its columns
  lined up; a real `F` took the window fullscreen and back; Server log
  showed the server's lines; the Yard's focused header at a 420-pixel page
  kept its title, the status, the menu and fullscreen, nothing overlapping;
  a second window of the owner's (a separate Chrome on the app's server)
  joined at 114 × 46 without moving it, showed "Sized by nater · 114 × 46",
  took the size with Fit to my window (132 × 26, the app then saying "Sized
  by Second window") and gave it back; in the Neovim editor real spaces
  echoed in 8–9 ms ahead of their acknowledgement, as letters do; and five
  shell sessions against the public switchyard's four per address left the
  fifth refused (`429 open_host_limit`) with no badge and its Activity
  saying why, refused again at 15 s and 30 s, and shared at the try a
  minute later once a session was stopped ("shared through the switchyard
  after 4 tries"). Seen meanwhile: the updater logs a long error while the
  newest release is a draft (its `latest.yml` is not public), and a shell
  session's first lines show `setopt`/`bindkey` from the owner's own
  `.bashrc`, neither Conductor's.

## Round 13: what round 12 set aside, built (started 2026-10-09)

The owner asked why five Files items had been moved to the todo instead of
built, and decided each (`docs/round13-plan.md`): Touched one row per file
with its count; files seen by git for every agent, labelled `git`; live
payload captures for the agents whose hooks name no files; a swap file
opened read-only with a banner; local echo always on in insert mode. While
planning, a bug: the `goose` on the owner's machine is the Go migrations
tool, and Conductor offered it as Goose.

- **G0, a program that only shares an agent's name is not the agent.** The
  identity probe already named the migrations tool an impostor, but the
  catalog still listed Goose as available, so the Launch dialog offered it
  and the Agents page showed it installed with a red "Not Goose". Now a
  known impostor makes the agent `available: false`: it leaves the Launch
  dialog, the Agents and Settings pages show it greyed as "Not installed",
  the tooltip naming the program that is there and what it printed, and
  `POST /api/sessions` refuses it (`not_the_agent`, from the probe's cached
  answer, so a launch never waits on a probe). `probe: false` on the agent
  launches it anyway. A verified probe that matches nothing (Claude Code
  printing its version some new way) is not treated so: the agent stays
  offered with its warning, since hiding it would make a real agent vanish
  after an update. Tests: the catalog and the launch refusal against a
  scripted impostor, a Claude entry printing something new still offered
  and noted, vitest for the badge's words, Playwright `identity.spec.ts`
  (the Goose stub greyed with the tooltip, absent from the Launch dialog,
  its launch refused).
- **G1, Touched keeps the whole session.** Touched was built in the
  browser from the activity entries the page saw: the replay's last 50, in
  a ring of 200 shared with every other entry, so a long session's early
  files vanished. The session now keeps an index beside the activity log,
  one entry per file (the latest op, every op seen, a count, the latest
  tool and agent, the first and latest times; up to 2,000 files, the least
  recently touched dropped first), filled from the `file` entries it
  records, so the server, `conductor host` and a published session all
  have it. A sixth file operation, `op: touched`, serves it under the file
  policy and the deny list, cut to the header's size. The Files pane asks
  for it when it shows and when Touched opens, and lays the live entries
  over it (an entry no newer than the index's latest touch of its file is
  not counted twice); an older session without the op falls back to the
  entries. Rows are one per file, as the owner chose: "Edit · claude ·
  08:33:12 · 4 times". On the way, a flaky save check led to a real bug:
  "Saved" shows for two seconds after a save, but a save within two seconds
  of another kept the first one's timer, which hid the new "Saved" almost
  at once; the timer now restarts. Tests: the index (one entry per file, a
  coalesced repeat not counted, eviction, the deny list, the size cut, the
  file request) and the op over the viewer WebSocket in Go; vitest for
  grouping and merging; Playwright `touched.spec.ts` (sixty files reported
  before the page opens all listed, past the replay, one with "2 times"),
  and `saving.spec.ts` ten runs out of ten.
- **G2a, files seen by git, for every agent.** A file an agent changed
  with no hook naming it (a shell redirect, a generator, an agent whose
  hooks report no paths) now reaches Touched. A server or `conductor host`
  session in a git work tree snapshots `git status` when it starts (what is
  dirty then is not the agent's) and looks again 1.5 s after a tool call or
  an attention change, a burst being one look; each file whose status, size
  or time moved, and that no other file entry named meanwhile (a hook's, an
  editor save), becomes a file entry with the tool `git`, worded "seen by
  git" in Touched, Activity and the feed: write for a new untracked file,
  delete for one gone, edit otherwise, nothing for a file only committed;
  at most 32 a look. A light status call (`gitcli.Porcelain`, no line
  counts) shares its parser with the Changes section's. Reads cannot be
  seen this way; the agents' own reads come with G2b. Tests: the comparison
  table (new, edited, deleted, committed, put back, a hook's file skipped)
  and a real repository (a generated file and an edited one seen, a hook's
  file and a file dirty before the session not, a later removal seen, a
  plain directory not watched) in Go; vitest for the words; Playwright
  `touched.spec.ts` (a file written with only a tool call reported shows
  as seen by git, the README dirty before the session does not).
- **G2b, files from the agents' own payloads.** Codex 0.161, Copilot CLI
  1.0.91 and Antigravity 1.2.14 each ran once in a scratch repository with
  a hook saving every payload, from a throwaway config home holding a copy
  of its login (deleted afterwards). The redacted payloads are the
  fixtures in `internal/notify/testdata`. They showed three bugs: Codex's
  apply_patch carries its patch in `tool_input.command`, which the mapper
  never read, so no Codex edit was ever reported; Copilot's arguments are
  an object the mapper ignored; and agy rejected Conductor's whole hook
  group, because a Stop entry must be a plain command hook, not a matcher
  group (its log: "command hook must specify 'command'"), so agy's hooks
  never ran. Now Codex's patch and its Bash tool, Copilot's view, edit,
  create and bash, and agy's view_file, write_to_file, the replace tools,
  delete_file and run_command name their files, and agy's hook file has the
  right layout. A conservative reading of a shell command (cat, head, tail,
  nl, bat, less and sed -n read; `>`, tee write; `>>`, tee -a, sed -i edit;
  nothing with a substitution or a variable; only files that exist) serves
  every agent's shell tool, Claude Code's Bash included. The capture also
  found that Codex runs hooks only once trusted, opening on a "Hooks need
  review" question: that is G2c. Tests: the shell reading's table, every
  captured payload mapped to the files that run touched, agy's installed
  layout, and Playwright `touched.spec.ts`, where the stub replays each
  agent's payloads through `conductor notify` (a fixed line, `stub tools
  <agent>`) and Touched lists the read and the write.
- **G2c, Codex's "Hooks need review" held like the trust question.** The
  G2b capture found that Codex 0.161 runs hooks only once trusted and opens
  on "Hooks need review" whenever its hooks are new or changed, with Review
  hooks highlighted: a crew member's prompt typed at launch would have
  opened the review. A digit moves the highlight and Enter picks it. The
  catalog now takes an agent's other startup questions (`questions`, each a
  prompt and its answers, at most 4; a saved agent inherits them), the
  session watches for all of them with one screen watcher and holds the one
  showing exactly as the trust question (source `trust`, typed text
  refused, the member's prompt held), and Codex's built-in entry has the
  question with Trust all and continue (`2`, Enter) and Continue without
  trusting (`3`, Enter). The agent form now carries the trust answers and
  the questions of the agent it edits. Tests: the catalog (Codex's entry,
  inheritance, refusals), the session (the second question's answers, the
  refusal, the answer, the trust question after it), a launch handing the
  questions to its session, vitest for the form, Playwright `trust.spec.ts`
  (the stub draws Codex's question behind a marker file; the member is held
  with the two choices; Trust all and continue answers 2 and the prompt
  runs). Open: whether Codex's trust lasts past a session.
- **G3, Neovim and swap files.** Opening a file another Vim held (or left
  a swap file for when it died) failed with Neovim's words. Now the bridge
  answers Neovim's SwapExists itself (open read-only) and reports the swap
  file with swapinfo's facts: its writer's process, user and host, whether
  that still runs on the session's machine (a zombie does not count),
  whether it holds unsaved changes. The editor shows a banner, as the owner
  chose, with Edit anyway, and once the writer is gone Recover and Delete
  the swap file; the choices are a fixed set the server maps to fixed
  commands (`nvim_swap`), and deleting removes only the regular `*.sw?`
  file Neovim reported. Two things found on the way: `:recover` replaces
  the buffer's text without a line event, so the bridge sends it whole, and
  Neovim stops following a buffer it reloads (`:e!`), so the bridge follows
  it again. Neovim's own questions (`:confirm q` over unsaved changes, a
  write over a file changed outside) now show their choices as buttons.
  Tests: against the real Neovim with its state and config in temporary
  folders (a running writer allows only Edit; one killed leaves a stale
  swap whose text is recovered, sent, then deleted; `:confirm q` arrives as
  a confirm message and Cancel keeps the editor), the relay dropping
  `nvim_swap` for view links, vitest for the banner's words and the confirm
  choices, Playwright `vim.spec.ts` (a Vim with the server's home writes
  its swap and is killed; the banner, Recover, `:w`, Delete; then
  `:confirm q` and Cancel).
- **G4, text with no key press under the Neovim keymap.** Only key presses
  reached Neovim: an input method's word, a dead key's character and
  dictation arrive as text input, which Monaco (read-only there) dropped
  with "Cannot edit in read-only editor". The editor now takes the text
  area's composition and input events on the way down (the capture phase on
  its own element, so Monaco never sees them) and sends the committed word
  or the inserted text as keys (`<` as `<lt>`, a line break as `<CR>`, in
  pieces under the 256-byte bound); a key an input method or a dead key is
  composing with is left to them. Tests: vitest for the text's keys and
  pieces; Go, the bridge typing composed and multibyte text as written;
  Playwright `vim.spec.ts` (`insertText` of "café ", then Chromium's
  `Input.imeSetComposition` of に and にほん and the commit of 日本: the line
  reads "café 日本 end", the composition never typed, no read-only message,
  `:w` writes it). By hand, not yet done: a real input method and a dead-key
  layout in the Windows app.
- **G5, the local echo in insert mode.** Every key round-tripped to the
  session's machine before it showed. Now each `nvim_input` carries a
  number, and once Neovim has handled the keys (and is not waiting for a
  command's next key) the bridge has Neovim `rpcnotify` an acknowledgement
  carrying the cursor, on the channel the buffer's changes ride, so it comes
  after them; the Go client runs notifications in order on one goroutine.
  In insert mode the page shows a plain character at once, laid over
  Neovim's line at the cursor, and holds a line change that comes while
  guesses show until its acknowledgement, so the two settle at once: the
  guesses the line holds are dropped, the rest laid over the new line.
  Found by the test: an acknowledgement can run early, because Neovim
  answers requests in the middle of a key when it waits (`:sleep`,
  `vim.wait`), and it covers later keys when those were handled too; so a
  page settles by the longest run of guesses Neovim's line holds, not by the
  number. A line Neovim made differently (an autopair, an abbreviation, a
  mapping) wins once and guessing stops until insert mode is left; a key
  that is not a plain character ends the guesses until it is handled; a
  held change shows as it is after 250 ms. A guess still pending after
  100 ms is underlined (a CSS animation delay, no timer), as the owner's
  choice of "always on" asked for nothing to configure. Tests: Go, the
  acknowledgement after the lines holding the typed text and none while
  `g` waits for its second key, against the real Neovim; vitest for the
  echo (guess, settle, a wrong guess, a key that ends the guesses, a
  timeout, an early and a late acknowledgement, a move of Neovim's own);
  Playwright `vim.spec.ts` with a Neovim config that busy-waits 300 ms per
  typed character and maps `(` to an autopair: "abc" shows within 250 ms,
  marked, settles undoubled, the autopair corrects once, the next
  character is Neovim's, `:w` writes "abc(x)end" (five runs out of five).
- **The local echo over the switchyard, measured (2026-10-09, rc.5).** A
  control link minted at switchyard.rslabs.net for a session in the
  Windows app, opened in a separate Chrome on the same PC, Neovim on,
  sixteen real keys typed through winctl-mcp, timed in the page (keydown to
  the letter in Monaco's view, keydown to Neovim's acknowledgement on the
  wire). Over WebRTC direct (2 ms): the letter 8 ms, the acknowledgement
  15 ms. Over the switchyard's relay (forced: WebRTC off in that page;
  35–51 ms): the letter 8.7 ms, the acknowledgement 40.8 ms (medians), so a
  letter shows as on the machine itself. Found by it: **a space was not
  guessed.** The space bar goes to Neovim as `<Space>`, which the echo took
  for a key that is not a plain character, so each space waited the round
  trip (38–40 ms) and, being a key that ends the guesses, held every
  letter typed after it until Neovim had it: at typing speed over a slow
  link, every word boundary stalled the echo. `<Space>` is now guessed as a
  space, as `<lt>` is `<`. Tests: vitest (`<Space>` guessable, a word after
  a space shown at once and settled), Playwright `vim.spec.ts` typing "ab c"
  into the slow Neovim (it showed "abend" without the fix; three runs out of
  three with it).
  By hand, not yet done: typing over the switchyard from the LAN box and a
  phone.
- **The Pebble test's port race, fixed.** `make test-pebble` picked ports
  by opening and closing a listener, then bound them later; on PR #71 CI's
  pebble job lost one ("bind: address already in use"). The two ports
  Pebble validates challenges against are now the test's own listeners,
  bound before Pebble starts and held until the test ends, handed to the
  TLS and HTTP challenge servers; Pebble's own directory and management
  ports, which only Pebble can bind, are tried again on fresh ones when a
  start fails with "address already in use" (at most five starts).
  `TestPebbleStartsAgainWhenItsPortIsTaken` hands the first start a port
  already in use and passes on the retry.
- **Goose, installed and captured.** The owner asked for the real Goose:
  Block's Goose 1.54.0 (the static musl build, its sha256 checked against
  GitHub's digest) is in `~/.local/bin/goose`, ahead of the migrations tool
  in `~/go/bin` on PATH, which stays. To capture its hooks with no account
  spent, Goose ran against a scripted OpenAI-compatible model on localhost:
  its own developer tools (`shell`, `write`, `edit`) ran and its hooks fired
  from a plugin laid out as Conductor installs it (`~/.agents/plugins/
  conductor/hooks/hooks.json`, no `plugin.json`), which works. Its payloads
  name `event`, `tool_name`, `tool_input` (`command`, or `path` relative to
  `working_dir`) and `last_assistant_message`; the mapper read the tool's
  name only. Now write writes and edit edits its path, shell names what its
  command plainly reads or writes, and Stop carries the last message. Goose
  1.54 prints its bare version (" 1.54.0"), which the probe did not know:
  it now does and is verified, as are Copilot's ("GitHub Copilot CLI
  1.0.94.") and agy's ("1.3.2") seen live the same day. Tests: the Goose
  payloads in the fixture test, the probe against the real and older version
  lines, Playwright `touched.spec.ts` replaying Goose's payloads through
  `conductor notify --goose-hook`; on this machine a server's catalog
  reports Goose 1.54.0 available and identified.
- **Tested in the Windows app, and what it found (2026-10-09).** The
  desktop app's own server binary (in WSL, `~/.local/share/conductor/bin`)
  was swapped for the new build, the app quit from its tray and relaunched
  with its debugging port, and winctl-mcp drove it: CDP for reading and
  clicking, SendInput for real keystrokes. Verified in the app: the Launch
  dialog offers the real Goose; the Explorer's filter finds a file three
  folders deep, unopened; a shell command typed for real writes a file and
  rings the bell, and Touched lists it "seen by git"; under the Neovim
  keymap real keystrokes with é and 日 type into Neovim and `:w` writes
  them; a stale swap left by a killed Neovim shows the banner (no process,
  unsaved changes), Recover reads its text in, `:w` keeps it, Delete
  removes it; `:confirm q` shows Yes, No, Cancel and Cancel keeps the tab;
  real Codex's "Trust this folder?" is held with its two choices. Found and
  fixed:
  - **Neovim keys were dropped past the 20th of a quick burst.** They
    shared the chat's bucket (10 a second, 20 at once): "typed for real:
    café" (exactly 20) landed and the rest of the line did not. Neovim's
    keys now have their own bound, 100 a second and 400 at once; a session
    test sends 60 keys at machine speed and Playwright types a 44-character
    sentence in one go.
  - **Codex's other startup questions were not held.** Beside a newer
    Codex background server (the desktop's, 0.162), Codex 0.161 first asks
    "Background server has incompatible feature settings" (a digit picks at
    once, Cancel highlighted, so an Enter ends Codex: the app's Codex
    session exited this way) and then "Update available" with Update now
    highlighted (an Enter runs `npm install -g @openai/codex`). Both are now
    in Codex's built-in questions, held like the trust question, with
    choices that carry no Enter ("1" or "3"; Escape to skip the update),
    and an input that is exactly one of the showing question's choices
    answers it with or without an Enter. While finding this, a probe of
    mine sent "1" and Enter to real Codex: the Enter landed on Update now
    and the interrupted npm install left `codex` missing; reinstalling
    `@openai/codex@0.161.0` restored it. Tests: the catalog (the three
    questions, no answer with an Enter, the update banner not mistaken for
    the question), the session (a choice answering without an Enter, a key
    that is not a choice leaving the question), Playwright `trust.spec.ts`
    (the stub draws both questions; the member is held; Run without the
    daemon answers "1", Skip this update answers Escape, never Update now;
    the prompt runs).
  - **A flaky sidebar test, and the focus it raced.** `sidebar.spec.ts`
    "the list takes the keys" failed once in CI (PR #77): it pressed Alt+S
    as soon as the reply bar showed, but the terminal takes the focus when
    its connection completes, which can come after, and took it back from
    the filter. The terminal now takes the focus on connect only when
    nothing else holds it (a person who moved to the filter, the chat or a
    field keeps their place), and the test waits for the terminal's focus
    before pressing Alt+S. Five runs out of five locally.
  - **The desktop window followed no restart.** The shell starts the
    server on `127.0.0.1:0`, so every start binds a fresh port, and nothing
    moved the window; after any restart (a
    crash, the health check, Server › Restart server) the window stayed on
    the dead port with a frozen page. The shell now moves the window to the
    new origin on the same page whenever the server is running again
    (`followOrigin`, leaving the splash and outside sites alone). Tests:
    vitest for `followOrigin`; the Electron smoke clicks Restart server in the
    menu and expects the window on a new origin, on the same page, signed in
    (it fails without the fix, the window left on the old port).
  - **A Neovim tab lost its changes not written to a swap file.** Seen
    closing the app's test editor: its swap file stayed behind. Every tab
    switch ended the Neovim of the tab leaving, as a lost connection ends it,
    so a buffer with changes not written was put in a swap file, and the
    next showing of the tab met the "swap file from a Vim that ended" banner
    with the text back as on disk; closing such a tab did the same without
    asking, since a Neovim tab never wore the unsaved dot. And Neovim's own
    `:q` left a swap file too: the session killed Neovim halfway through
    its exit (the context cancelled as soon as Neovim said it was leaving),
    before Neovim removed the file. Now Neovim reports its buffer's changes
    not written (`nvim_event` kind `modified`); the tab wears the dot and
    keeps its Neovim while another is in front, taken back as it was;
    closing it asks, Save writing through Neovim's `:w` and Don't save
    ending it as `:qa!` does (`nvim_close` `discard`), so its swap file goes;
    the keymap button waits while anything is unsaved (each keymap keeps its
    own text; switching dropped the other's unsaved edits, Monaco's too);
    `:q` is let finish before Neovim is reaped. A connection may keep 4
    editors (2 before), and the 8 per session are counted per session (one
    count for the whole server before). The bridge's tests run Neovim with
    their own state directory (they left swap files in the machine's).
    Tests: `internal/nvim` (modified through a change, `:w`, undo; Close
    keeps the swap file, Discard drops it), `internal/session` (the same
    through the session, `:q` leaving none, the bound per session),
    `internal/proto` (the shapes, an older owner reading `discard`), vitest
    `nvimHold.test.ts`, Playwright `vim.spec.ts` (a change survives another
    tab in front with no banner and the editing goes on; the dot and the
    keymap waiting; Don't save leaves the file and no swap file; Save writes
    through Neovim; `:q` leaves none). In the Windows app too, with this
    build as its server and real keys through winctl-mcp: a change typed in
    a.txt, b.txt opened and a.txt brought back showed the change with no
    banner and took more typing; the dot showed and the keymap waited; Don't
    save left the file as it was and no swap file; Save wrote through
    Neovim; `:q` closed the last tab with no swap file and no Neovim left.

## Round 12: the Files tab as four sections, and a Monaco editor beside the terminal (started 2026-10-08)

- **Markdown preview, Mermaid included (#166, 2026-10-21).** A `.md` or
  `.markdown` file in the editor gets a Preview toggle in the header
  (Alt+P in the editor, listed in the shortcuts modal): the file rendered
  with markdown-it (headings, lists, tables, task lists, code highlighted
  by shiki in the editor's theme, links), the choice kept per tab, what
  the editor holds previewed (unsaved changes included, as of the toggle).
  Raw HTML is parsed as text, never rendered (no script, no handler,
  no `javascript:` link); a web link opens outside the app; a relative
  link opens its file in a tab; a relative image loads through the
  session's file reads (the link's path decoded once, as markdown-it
  encodes it); an image on the web is not loaded (its alt text shows). A
  `mermaid` fence is drawn by Mermaid in the app's theme and again at a
  theme switch; a block it cannot draw keeps its source with the error
  above it. Mermaid never runs in the page: it runs in a sandboxed frame
  (`web/app/utils/mermaidFrame.ts`, made at the first diagram on the
  page), scripts only, so an opaque origin with none of the app's cookies
  or storage, under a Content Security Policy whose default is none, so no
  image, font, style sheet, connection or navigation can leave it, and only
  the renderer's own script loads, from the app's origin (built as a
  worker-format bundle, `mermaidFrame.entry.ts`, its packages recorded for
  the notices as a worker's are). There Mermaid runs with `securityLevel:
  strict`, errors kept out of the document, and a DOMPurify configuration
  forbidding every label element and attribute that loads or navigates
  (`mermaidRules.ts`; Mermaid's own forbids only `style`), labels as SVG
  text rather than HTML, and takes the page's requests one after another
  (Mermaid queues renders of its own accord, and a cleanup beside the next
  render would take its container away). The page gets the SVG as a
  string, scrubs it of anything that would load, navigate or run, and
  shows it as an image (an `img` with a data URL, sized by the viewBox):
  a browser loads nothing from inside an SVG it shows as an image, runs
  nothing in it and follows no link, whatever its CSS says, so the display
  is as closed as the frame (a scrub of CSS was tried and dropped: a custom
  property holding an `image-set()` is one of many spellings no scrub
  knows); a node Mermaid wrapped in an anchor for a `click … href` keeps
  its drawing as a group, the link gone. A scan of the source was tried
  first and dropped too: Mermaid loads an image node's URL while it
  measures it, and a quoted key or a JSON escape slips any regular
  expression. Under the dev server, where Vite serves modules import by
  import (which the frame, its origin opaque, could load only with a CORS
  allowance for every opaque origin), `nuxt.config.ts` bundles the same
  entry with Vite's library build as the dev server starts and serves it
  as one classic script at a path of its own; the frame's policy and
  script element are the same in both. A render pass overtaken by a later one or by the
  preview going stops at its next step; a change to the editor's model
  while previewing (a reload after a save conflict) refreshes the preview.
  A `[words]()` link with no destination is no link; a `#fragment` link
  scrolls to its heading in the preview (headings carry `md-` ids); neither
  navigates the page. Inside the text, Alt+P is an action of the Monaco
  editor on Markdown files (Monaco keeps a chord it sees from reaching the
  page); on the preview it is the area's key handler; it is kept from
  Neovim on a Markdown file (`keptByConductor`). The same component serves
  a guest's view-only editor and the Yard. Tests: vitest `markdown.test.ts`
  (the rendering, the sanitising, the links and images with their decoding,
  the heading ids, the Mermaid marks), `editorTabs.test.ts` (the toggle per
  tab), `nvimKeys.test.ts` (Alt+P kept); Playwright `markdown.spec.ts` (a
  README with a table, a task list, a script shown as text, an image on the
  web held, a flowchart drawn as an image the browser decoded, an image
  node failing in the frame, a url( in theme CSS and an HTML image in a
  label stripped, no request outside the page's origin, the frame
  sandboxed, an empty and a fragment link not navigating, a link opening
  its file, two previews switched between at once, the source back
  by Alt+P from the preview and the text, a reload during preview, and
  Neovim on where nvim is on PATH). Mermaid's SVG is not drawn in vitest's
  node environment, so the drawing is checked in the browser.

From the design screens 4a–4g and 5a–5c in "Conductor UI mockups" (the
owner designed them on 2026-10-08 from a brief written here). The plan with
its decisions and layers is `docs/round12-plan.md`: Monaco as the editor,
editing for controllers under a `fileEdit` setting, go-git for the git
features with the CLI wrapper kept where go-git is slow, file events from
the hooks first, layers merged one at a time (never a stack), and Vim keys
waiting on monaco-neovim-wasm's missing license.

### Decisions

- **A plan saved outside the folder opens in the editor (#289,
  2026-10-21).** Claude Code writes its plans under its config directory
  (`~/.claude/plans/<slug>.md`, or `plans` under `CLAUDE_CONFIG_DIR`;
  checked against 2.1.306), where the session's folder does not reach.
  The session opens that one directory as a second root (an `os.Root`
  held to the same checks as the working directory: links refused once
  resolved, the deny list by identity) the first time the agent reports a
  file in it, and the files the agent reports there are the session's
  plans: its hooks' file events (a delete forgets one), the plan its
  approval names (the ExitPlanMode hook's `planFilePath`, carried by the
  attention route's `plan`) and the path its dialog prints, once seen to
  be a file there. A plan opens in the editor from the approval's **Open
  plan** in the reply bar, from the Files tab's **Plans** group at the top
  of the Explorer (`op: plans`) and from the path the dialog prints in the
  terminal; it previews as Markdown, saves back through the same root,
  and Touched lists it by its whole path. Another session's plan in the
  directory, the credentials beside it and the rest of the config
  directory stay refused as any outside path; a viewer cannot name a plan
  into the session, since only the agent's token or an admin reports
  events. The directory comes from the agent's adapter and the environment
  its process was given (`agents.PlansDir`), never from a page; a host
  does the same for its agent, and a hosted session's plan travels to the
  host with the forwarded question. Design: `docs/plans/issue-289.md`.
- **F1, the Explorer (4a).** The Files tab opens on the working directory
  as a tree (`utils/fileTree.ts`: the root, folders listed on their first
  expansion through the same `file` request a directory took before, files
  with their size, folders first and names by code point without regard to
  case so every browser shows one order); the breadcrumb runs from the top
  of the path; the box above the tree is a filter over what is loaded (a
  folder that holds a match opens so the match shows) or, for a typed path
  (a slash in it, a leading dot or tilde, or `:line` at its end) and Enter,
  opens that file at that line, made absolute against the working
  directory; the hint that paths in the terminal are clickable is one line
  at the foot. A file still opens in the pane (the editor area is F2); a
  crumb or Back returns to the tree, opened down to that folder, fetching
  what it has not seen. The box moved out of the inspector into the pane,
  so the Yard's slide-over has it too. A root listing refused because the
  connection is not open yet is tried again a few times. No server change.
- **F2a, the editor area on the session page (4b, 4c, 4f).** Monaco
  0.57.0 (`utils/monaco.ts`: the editor, its workers as Vite worker files,
  and the two brand themes, in a chunk loaded with the first file;
  `CodeEditor.vue` one file at a time, read-only until F6, the line asked
  for shown in the middle and tinted for a moment). `EditorArea.vue` holds
  the tabs (`utils/editorTabs.ts`: one per file or URL, the next or the
  previous taking a closed one's place, Ctrl+Tab round the end, the fold,
  the split kept in `conductor.editor.split`), the header (the breadcrumb
  from the working directory, Ln/Col, copy path, open raw) and the states
  4f draws: an image on a checker with its size, a binary file with Open
  raw, a URL in the sandboxed frame with New tab, Refresh and Copy, a read
  refused with the words for view-only and for viewing off, a hosted
  session's machine away. The session page puts the area above the
  terminal, split by a bar dragged with pointer capture, the terminal
  keeping its minimum height; T and Alt+T fold it (`EDITOR_SHORTCUTS` in
  the modal, Alt+T handed back by a terminal); the Files pane opens files
  into it (`external`, the `open` emit) and stays the tree; "Preview in
  pane" for a URL the agent printed opens a URL tab. The symbol crumb the
  design shows ("ListUsers") waits for a language server Monaco does not
  have for Go; the Yard, the guest page and the phone are F2b.
- **F2b, the Yard, the guest page and the phone (4g).** The column a
  terminal shares with the editor is one component (`EditorColumn.vue`:
  the area, the split, the terminal in its slot, the reply bar in `bar`),
  and the files a page has open one composable (`useEditorTabs`: the tabs,
  how a path or a URL opens, the fold with T and Alt+T). The Yard's focused
  tile and a guest's join page use both, with the Files pane beside the
  editor in place of the slide-over (`FileViewer.vue` is gone); a
  view-only guest gets the pane marked read only and the editor read-only;
  the join reply carries the session's working directory so the guest's
  tree can root there (reads stay the session's to allow). On a phone the
  editor takes the column and the terminal folds to a bar at the foot that
  says what the agent is doing (the attention's words, else the agent) and
  brings the terminal back on a tap, the editor folding to its strip; the
  terminal stays mounted, so nothing reconnects. In the Yard, Esc folds the
  editor first and leaves the focus second. Found on the way: below lg the
  dashboard panels were at least a screen tall (the theme's `min-h-svh`)
  and ran under the bottom bar, clipping whatever sat at a panel's foot;
  the app config caps them there (`dashboardPanel.slots.root:
  max-lg:min-h-0`), so a phone's terminal bar and reply bar end above the
  bottom bar. And the Files pane's retry for a connection not open yet
  compared a raw root with its reactive proxy and never fired; the root is
  reactive from the start now, and a page whose terminal connects after
  the pane is retried too.
- **F3, Changes (4d).** The git reads ride the file request: `file_get`
  with `op: status` (against `base`, HEAD when empty) answers a FILE frame
  of kind `status` (the tree's top as its path, the branch, the base's id,
  the changes with their status letter and lines, the totals, truncated at
  500 or at the frame's header bound), `op: show` a FILE frame of kind
  `show` with the path's content at a revision (1 MiB at most); both under
  the session's file policy and deny list, so a hosted session's status
  comes from the developer's machine through the same channel.
  `internal/gitcli` runs the git binary (argv, the C locale; the crews'
  helper moved there): status by `git status --porcelain=v1 -z`, the lines
  by `git diff --numstat --no-renames`, an untracked file's lines counted
  from the file (a binary one as none), a revision's file by `git show`.
  go-git is kept for the commits (F5): its status hashes the working tree,
  which the owner ruled out. The Files pane gains its section switch
  (Explorer, Changes with its count); the Changes list with M, A, D and
  the lines; the footer "Refreshed as the agent works · 8s ago" with the
  totals; a refresh every five seconds while the section shows and once
  when the pane opens, for the Explorer's marks; "Not a git repository"
  with the words. A change opens as a `diff` tab (`DiffEditor.vue`,
  Monaco's diff editor, side by side or inline, read-only; the base's
  version through `show`, none for an added file, the working directory's
  through a read, none for a deleted one), the header saying what it is
  against, the status and the lines, and Open file. A crew member's
  changes against the run's base wait for the base to reach the page.
- **F4, Touched (4e).** A seventh event type, `file`: `op` (read, edit,
  write, delete) and `path` (≤ 1024 bytes) with `tool`, through the same
  activity ring, replay and frames as the rest (`proto.Activity` carries
  both), posted like any event (the API refuses one without its op and
  path). The hook mappers yield the files a tool call touched beside the
  call's own event (`notify.Request.Files`; Claude Code's input
  `file_path` or `notebook_path` by tool, Codex's `apply_patch` by the
  files its patch names, Cursor's `afterFileEdit`), and `Send` posts each
  as its own event; `conductor notify --event file --op … --path …` and
  the MCP `report` tool take it for agents without hooks, which the skill
  says. A repeat of the newest file entry (the same path and op within
  3 s) moves its time instead of adding a line (`CoalesceFile`), so a file
  saved forty times is one. The Files pane gains Touched (newest first,
  the op's icon, the folder and name, "Edit · codex · 08:33:12", a row
  opening the file) and dots on touched files in the Explorer, fed by the
  inspector's activity (the Yard's and the guest's panes have no activity
  yet, so no Touched there); the Activity tab and the Events page word it
  ("edited internal/api/users.go · Edit"), routed to the feed alone by
  default. The activity replay stays at 50 entries, so a long session's
  Touched starts from what the page saw; the hook bucket (20 a second)
  still bounds a burst. Verified against the real Claude Code (2.1.294)
  on 2026-10-08: its base settings had no PostToolUse hook at all (tool
  events are the catalog's opt-in `toolEvents`), so nothing reported a
  file; the base settings (`claude.json`, `claude-yolo.json`, the
  installed `~/.claude/settings.json`) now hook PostToolUse for the file
  tools alone (`matcher` Read|Edit|MultiEdit|NotebookEdit|Write) with
  `notify --claude-hook --files`, which posts the files and no tool call,
  and a tools launch keeps its every-call hook (the same Edit reported
  twice coalesces, the call's own event between them stepped over). The
  live tier (`live.spec.ts`, "reports the files it reads and writes
  through its hooks") checks the real agent's read and write land as
  file events; Codex's passes only where its `features.hooks` is on with
  Conductor's hooks.json, which this machine does not have.
- **By hand, F1–F4 on Windows (2026-10-08).** A dev server from main on
  the WSL box, opened in Chrome on the Windows host and driven through a
  desktop-control MCP (UI Automation, real mouse and keyboard, window
  screenshots), with the real Claude Code 2.1.295 in the trusted scratch
  repository, bypass permissions on. Seen and screenshotted: the Explorer
  tree with sizes and the breadcrumb; "internal/api/users.go:12" and
  Enter opening the file in Monaco at Ln 12 with Go highlighting, the
  breadcrumbs and the minimap; a second file as a second tab; the fold
  strip "2 files open · users.go README.md" and unfold; the split bar
  dragged 150 px (the editor from 495 to 644 px, the split kept); the
  tab's × closing it and the editor folding away. A typed prompt had the
  agent read and edit README.md: Changes listed it as M +1 −0 within
  seconds ("Refreshed as the agent works · 4s ago"), the Explorer marked
  it M with a touched dot, the diff opened side by side against HEAD and
  inline, Open file raised the file's tab; Touched listed "README.md
  Edit · agent · 16:29:33" above its Read, newest first; the Activity tab
  and the Events feed worded both, and the session's sidebar row carried
  no badge. Two notes: the filter box matches only what the tree has
  loaded (a "users" typed before `internal` was expanded found nothing;
  in the todo); Ctrl+Tab is a reserved shortcut in Chrome, so the tab
  cycle cannot be driven from a browser tab (it works in the desktop app
  and in the Playwright spec). The phone layout and the light theme were
  not part of this pass.
- **F5, Commits (4e).** Two more git reads ride the file request:
  `file_get` with `op: log` answers kind `log` (the branch, the commits on
  HEAD newest first with their subject, short id, author, committer time
  and first parent, at most 200), from the session's start (`since`, the
  session's `createdAt`, compared at whole seconds since git keeps no
  more) or, with `base`, after the base's merge base with HEAD (a crew
  member's run base, whatever the commit times); `op: commit` answers
  kind `commit` (the commit with its body, at most 4 KiB, and its files
  against the first parent with their lines, renames found with their old
  path in `from`, a file over 1 MiB listed without lines, at most 500).
  Both read with go-git (`internal/gitrepo`, v5.19.3, Apache 2.0; chosen
  by the owner for the Go git features): 200 commits logged in 37 ms and
  a 45-file merge's stats in 79 ms on this repository, a crew's linked
  worktree opened through its common directory; status and show stay with
  the git binary, which is faster for them. The Files pane gains Commits
  beside Changes with its count, "2 commits on main since 08:31", a
  commit opening in place to its body and files, a file opening as a
  diff tab "a1b2c3d users.go", "a1b2c3d vs its parent 9e8d7c6" (the
  parent's side read at the parent, a rename's at its old path, nothing
  for an added file or a root commit), refreshed every 10 s while it
  shows, and "Commits made here. Nothing is pushed from Conductor." The
  Yard's focused tile and the guest page open commits the same way. Tests:
  go-git against scratch repositories (since a time, after a base in a
  linked worktree with an old-dated commit, a root commit, modified,
  added, deleted, renamed and binary files, the totals, an unknown
  revision), the session's reads through the file policy (the deny list,
  not a repository, a flag as a revision), vitest for the rows, the words
  and a tab per commit, Playwright `commits.spec.ts`.
- **F6, Editing (4b, 4f).** A controller edits a file in Monaco and saves
  it with Save or Ctrl+S, on the session page, the Yard's focused tile and
  the guest page alike (one composable, `useEditorBridge`, now gives all
  three the save and the Neovim keymap). A save travels as FILE_WRITE
  frames (`0x08`, new): parts of at most 32 KiB in order under one request,
  the FILE frame's layout the other way, bounded at 1 MiB, one save at a
  time per connection, answered with a FILE frame of kind `written` or
  `error`. The owner checks the file against the read's sha256 (a read
  whole now carries `sha256` and `mtime`) and refuses with
  `changed_on_disk`, naming the newest file event that changed it ("Changed
  on disk since you opened it · 08:34:10 · codex (Edit). Saving would
  overwrite that."), unless forced; it writes atomically with the file's
  mode kept and records a `file` event `write` with tool `editor`, so
  Touched and Changes follow. The client: an unsaved tab's dot, Save
  (disabled until something changes) and "Saved", Compare (the disk and
  yours side by side), Reload and Save anyway, a prompt before an unsaved
  tab closes ("Save notes.txt?", Save, Don't save, Cancel), unsaved edits
  kept across switching tabs, the keymap switch held while a tab is
  unsaved. Policy: the control role on a session whose `fileEdit` is
  `control` (the setting F8 added); a truncated or binary file and a view
  guest stay read-only; the switchyard drops FILE_WRITE from view-role
  connections. Found on the way and fixed: Monaco 0.57 types through
  Chromium's EditContext element, a plain div the page's shortcut guard
  does not know, so single-key shortcuts fired while the cursor was in the
  editor (`g` then `a` opened Agents, `n` the Launch dialog), read-only
  included; both editors use Monaco's textarea now (`editContext: false`).
  Tests: the frame's round trip and bounds, the session's save (parts,
  mode, Activity, the conflict with who changed it, forced, deleted since,
  every refusal, editing off), a save over the viewer WebSocket and a view
  link refused, the relay dropping saves and Neovim for view viewers,
  vitest for the parts, the frame, the saved versions and the words,
  Playwright `saving.spec.ts` (type, dot, Ctrl+S, Touched; the agent's
  edit caught, Compare, Reload, Save anyway; the close prompt and Don't
  save; a view guest's Read only).
- **F7, Comment on a line (5a, 5b, 5c).** A selection in the editor shows
  a bar under its last line: Comment, Ask the agent (a controller's) and
  Copy (`path:from-to` and the lines); Ctrl+Shift+M and Ctrl+Shift+A open
  the composer from the keyboard, on the selection or the cursor's line.
  The composer, anchored there, shows the quote and takes the words (Enter
  sends, Esc closes); the post rides the chat with a new optional `quote`
  `{path, from, to, lines, cut}` (the path from the working directory, at
  most 12 lines of 200 bytes, control characters out; its last lines left
  out when a full text of quotes beside it would pass the 8 KiB frame, a
  worst case tested), kept with the message and in a run's record. Asked
  of the agent, the message is typed as the location, the lines as `> `
  lines and the words, at most 4096 bytes (`AgentText`), on a session and
  into a run's member alike. In the thread a quote card shows the location
  and the numbered lines; on a page with an editor (provided by the page,
  so every chat container shows it) Open goes to the range with it tinted
  for a moment, and the card reads the file (once per 15 s per path) to say
  "Lines moved since · now 5–6" (the nearest place the lines are now) or
  "Lines changed since". The session page brings the Chat tab forward on a
  comment (the sheet below xl); the guest page posts to its chat too (a
  view guest comments, a controller asks). Tests: the quote's bounds, the
  frame's worst case, the agent's text and its typing (Go), vitest for the
  quote, the location words and the relocation, Playwright
  `comments.spec.ts` (select, Comment, the card; Ctrl+Shift+A, the marker,
  the location typed into the agent; the lines moved on disk, the note,
  Open at the new range).
- **Round 12's remainder (2026-10-09).** The Explorer's filter reaches
  folders not opened yet: a fifth file operation, `op: find`, walks the
  working directory on the session's machine (at most 20,000 entries,
  3 s, 200 matches, `.git` and `node_modules` left out, the deny list
  checked on every folder and match) for files whose name holds the
  words, and the pane lists them under the tree, "In folders not opened
  yet", a row opening the file. Touched shows on the Yard's focused tile
  and the guest page too (their terminal's activity). Under the Neovim
  keymap the cursor sits right on lines with multibyte characters
  (Neovim's byte columns made UTF-16 ones, `byteColToUtf16`), and a
  bug found on the way: after each key the bridge asked for the cursor
  with an eval, which cancelled a command waiting for its next key, so
  `gg`, `f<char>` and the like sent a key at a time (as a browser sends
  them) did nothing and could hang the bridge; it now asks
  `nvim_get_mode` first and skips the eval while Neovim waits. Set aside,
  with their reasons, in the todo: a local echo for insert mode, input
  methods and dead keys under the Neovim keymap, the swap-file prompt,
  Touched before the replay's 50 entries, files from more agents. Tests:
  the find (names below, any case, the skips, the deny list, the bound),
  `gg` then `ft` a key at a time against the real Neovim, vitest for the
  found rows and the byte columns, Playwright `files-more.spec.ts` (the
  find past an unopened folder, node_modules left out, the file opened;
  Touched on the Yard's tile) and `vim.spec.ts` (the cursor past an é).
- **F8, Neovim in the editor (the real one, on the session's machine).**
  Chosen on 2026-10-08 over monaco-neovim-wasm (no license: public code
  without one grants viewing and forking on GitHub, not redistribution)
  and over monaco-vim: the editor's keys stay Monaco's until the **Keys**
  button in the tab strip is pressed, which is kept per browser
  (`conductor.editor.keymap`) and is off by default, so nobody who never
  asked for Vim keys meets one. With Neovim chosen, a file opened on the
  session page is held by `nvim --embed` on the machine that runs the
  session (the person's own config and plugins), started by the official
  Go client (`github.com/neovim/go-client`, Apache 2.0) with a UI attached
  for the mode, the command line and the messages, the buffer followed
  through `nvim_buf_attach`, the cursor through an autocmd. The wire is
  `nvim_open`, `nvim_input` (≤ 256 bytes, a per-connection bucket),
  `nvim_close` and `nvim_event` (the buffer's `lines` cut to the control
  bound, a line past 4 KiB cut and marked; `cursor`, `mode`, `cmdline`,
  `message`, `written`, `closed`, `error`), on the terminal's connection
  as file reads are, on the server and in `conductor host`; the switchyard
  drops the three from view-role connections. The policy is the new
  `fileEdit` setting (`control`, the default; `off`; `CONDUCTOR_FILE_EDIT`,
  `conductor host --file-edit`) on top of the control role and `fileView`,
  reported in the welcome as `fileEdit` and `nvim` (installed there), at
  most 2 editors per connection and 8 per session, closed with the
  connection. In Monaco the keys are intercepted and sent in Neovim
  notation (`utils/nvimKeys.ts`), the model follows the `lines` events
  (`utils/nvimLines.ts`), the cursor style follows the mode, a visual
  selection shows as Monaco's, a status line under the tabs shows
  `-- INSERT --`, the command line as typed and the messages; `:w` writes
  on that machine and lands in Activity as the person's `file` event
  `write` with tool `nvim`, so Changes and Touched follow; `:q` closes the
  tab; Ctrl+W goes to Neovim in that keymap. Where `nvim` is missing or
  the connection may not edit, the button's note says so and the keys
  stay Monaco's. Verified against Neovim 0.10.4 on this machine: the Go
  bridge (`internal/nvim`), the policy and bounds through the session
  (`internal/session/nvim_test.go`), the viewer WebSocket end to end
  (`TestNvimOverTheViewerWebSocket`: opened, the buffer, `dd` as a
  deletion, `:w` on disk, a view link refused with `nvim_unavailable`),
  22 vitest cases for the keys and the edits, and Playwright
  `vim.spec.ts` (off by default; the button; `dd`; insert mode with its
  words; `:w` on the command line, written on disk, the message, the
  Touched row with tool `nvim`; `:q` closing the tab; the choice kept
  across a reload and turned off again). CI installs Neovim 0.10.4 from
  the pinned release for the go and e2e jobs; without it the Go tests
  skip with a message and the spec is skipped. Left for later (the todo):
  the Yard's focused tile and the guest page, a local echo in insert mode,
  byte columns with multibyte text, a Settings row.
- **By hand, v0.7.0-rc.4 in the installed desktop app on Windows
  (2026-10-08).** rc.4 was cut from main (F1–F4), its installer put on the
  Windows host and run through the same desktop-control MCP: the welcome
  page with the lockup in the header and "Conductor 0.7.0-rc.4", the
  "Choose Installation Options" page naming the per-machine install it
  would upgrade, the elevation prompt (the person's click), the finish
  page with the lockup on forest and the sponsor badge in the sidebar.
  The app launched, put the rc.4 server in the WSL distribution and ran
  it; its page was driven through Electron's own debugging port
  (`--remote-debugging-port`, for one launch) with real keys and mouse
  for what a person types and hovers, screenshots of the window for what
  shows. Passed, each with a screenshot: **3b** two loose sessions (one
  asking), a run of three (one asking, one exited, folded "Exited · 1"
  with the count), the hosted session from this box with the laptop tag
  "lan-box", the Yard's count; **3c** hover (Share, Stop, More), the
  in-row stop confirm ("Stop migrate-db? … Cancel / Stop") and its
  Cancel, the run header's Share run, Stop run, Open run; **3c keys**
  `/` to the filter, ↓ to the first row, J and K over rows and the run
  block, Enter opening the row, X asking and Esc cancelling, S opening
  "Share lan-shell", R from a member opening its run; **3d** Ctrl+B to
  the rail with the counts, the run capsule holding its members, the
  laptop tile, "+2" exited, the tooltip "users api · run started 17:46 ·
  lead running · core running"; **3f** nothing dropped remains. **The
  trust question (rc.3)**: Claude Code 2.1.295 in an unseen folder shows
  the two answers on the reply bar, the row, the Yard count and the
  chat; "Yes" from the row and "Yes" from the chat each let it continue
  (no exit); "To agent" while it asks is refused with "the agent asks
  whether to trust the folder: answer with the question's choices, or in
  its terminal". **Chat 2a, 2b, 2g, 2h** with a second browser as Jane:
  names, roles and times; "To agent" typing `echo chat-to-agent-ok` into
  Claude Code with the marker "Sent to agent by nater"; the closed tab's
  count rising on her message and clearing on open; the pills on the
  session row and the run header while on the home page; "This session
  ended. The chat is read-only…" after a stop. **2e** the run's Chat
  beside Share, the scope menu "Chat only · everyone here reads it",
  "Also send to lead · waiting on a prompt: skipped" (disabled), "Also
  send to core · typed into its terminal", "Also send to review · ended";
  `echo run-chat-ok` typed into core with "Sent to core by nater"; a
  member's question in the run chat on its name. **2d** through the real
  switchyard (switchyard.rslabs.net, still rc.2): the view link's join
  card ("You'll be view only: you can watch and open files", the sponsor
  badge), joined as Guest: the terminal over "WebRTC direct", "hosted on
  naterdev-win", the chat with "You are view only: what you write reaches
  the people here, not the agent." and no agent actions. **The newline
  chords** Shift+Enter and Ctrl+Enter each added a line to a Claude Code
  prompt and Enter submitted the three lines. **G then Y** opened the
  Yard with the focus outside the terminal. **The Files tab in the app**:
  an image on the checker with "64 × 48" and Open raw, a binary file
  ("Binary file; nothing to show · 2.0 KiB. Open raw downloads it."), a
  link the shell printed clicked in the terminal and previewed in the
  pane (example.com in the sandboxed frame with New tab, Refresh, Copy),
  the Yard's focused tile opening go.mod in Monaco from its Files pane.
  Not verifiable by this harness: the Alt chords (the control server's
  key events carry no scan code, so `e.code` is empty and both xterm's
  pass-through and the shortcut matcher, which key on `e.code` under Alt,
  ignore them; a synthetic Alt+Y navigates, and a real keyboard sets the
  code) and the uninstaller's sidebar. Seen and not explained: an
  "error" badge on a shell member (lead at launch, review after a stop
  with exit 0) with no error line in the Events feed; in the todo. Two
  harness lessons: Enter or Esc sent to a focused terminal while Claude
  Code asks the trust question answers it ("No, exit" / cancel) and the
  session ends, so a keyboard pass must leave such a page first; a
  session row below the sidebar's fold cannot be hovered until scrolled
  into view.

## The trust question's answers as choices (2026-10-08)

The owner, testing rc.2: "the trust question kills the session if I answer
in the chat". Claude Code's trust dialog highlights "No, exit" (the live
test knew: its trusting answer is Down then Enter), and every typed path in
Conductor ends with Enter (the chat's "To agent", the reply boxes), while a
trust question carried no choices, so typing anything there picked "No,
exit" and Claude Code left. Now:

- **The catalog's `trustAnswers`** (`catalog.Answer`: a label and the keys
  that pick it, the trusting answer first, at most 6, inherited by a saved
  override like `trustPrompt`): Claude Code "Yes, I trust this folder" =
  Down then Enter and "No, exit" = Enter; Codex "Yes, trust this folder" =
  Enter and "No, continue without trusting" = Down then Enter. The session
  takes them as `Options.TrustAnswers`, and `fireTrust` puts them on the
  attention as its `options` (cleaned and bounded as any prompt's), so the
  reply bar, the sidebar's row, the Yard's card and the chat's question row
  show the two buttons and type the right keys, raw.
- **Typed text is refused while the question shows**: `Submit` by a person
  returns `ErrTrustQuestion` (`TrustQuestionWords`); the viewer and the host
  answer `submit`, `chat` to the agent and `chat_send` with `not_sent` and
  those words, which the chat row and the reply box show. Conductor's own
  typing (a crew's prompt, a handoff, a broadcast) already waited.
- Found on the way: Codex 0.159's own "update available" dialog (Enter
  continues with the update) is not detected as needing input, so a session
  sits on it silently; in `docs/tasks-todo.md`. `conductor host` watches no
  trust question yet, so a hosted session's stays the terminal's to answer.

Tests: the catalog's defaults and validation; the session's question
carrying its answers, Enter-by-text refused and the choice's keys answering;
the Codex stub's trust dialog in `trust.spec.ts` (the buttons on the page and
in the chat, "To agent" refused with the words, the chat's "Yes" trusting).

## After round 11: a newline in the prompt, the page chords, the installer's art and the sponsor badge (2026-10-07)

Four small things the owner asked for after the stack merged, in one pull
request from main (not stacked, each its own commit):

- **Shift+Enter and Ctrl+Enter are a newline in the agent's prompt.** The
  browser terminal sent a plain carriage return for Enter whatever the
  modifier, so both submitted (the bug the owner found on 2026-10-06, now
  off the todo list). `TerminalView`'s key handler takes the chord before
  xterm does and sends ESC CR (`NEWLINE_IN_PROMPT`, `utils/terminalKeys.ts`),
  what Claude Code's own `/terminal-setup` teaches VS Code and iTerm2 to
  send for Shift+Enter and what a terminal sends for Option+Enter: Claude
  Code reads it as a newline, Codex as Alt+Enter, a newline too. Plain
  Enter, Alt+Enter and Meta+Enter are untouched. vitest for the chord;
  Playwright types two chords and an Enter into the stub and finds the ESC
  at the end of each line in its transcript. Codex's reading is the live
  tier's to confirm (`make test-live`); the by-hand list names it.
- **The page chords go by each page's initial**: G then Y the Yard (was W),
  G then R the Roundhouse (was C), G then C Crews (was R); A and E as
  before; the Alt twins in a terminal follow (Alt+Y, Alt+R, Alt+C), and
  Alt+W reaches the agent again. vitest on the rows and the passthrough
  set; Playwright presses the three chords.
- **The installer's own artwork.** electron-builder's stock bitmaps (a blue
  laptop and a box) gave way to ours: `scripts/installer-art.mjs` renders
  `scripts/installer-art.html` headless at 1:1 and writes the 24-bit BMPs
  NSIS reads into `desktop/build/`: the welcome and finish pages' sidebar
  (164 × 314, the lockup on forest with an orange rule, the sponsor kit's
  badge "Sponsored by" + the RockSolid Labs logo at its foot) and the other
  pages' header (150 × 57, the lockup on the dialog's white). The
  uninstaller takes the same sidebar. A desktop vitest reads the bitmaps'
  headers (size, 24-bit, uncompressed) and the config naming them. The
  installer itself is a Windows runner's build: checked by eye on the
  next release candidate.
- **The sponsor badge on the switchyard's pages.** The footer's text line
  gave way to the kit's badge (1b) at the lower right: "Sponsored by" and
  the logo, linking to rocksolidlabs.io, on the landing page and the 404
  page (`switchyard_page.go`; the Go test and the switchyard e2e check it
  and that the logo is served beside the app). A switchyard's own join
  page shows the same badge under the card (light and dark logos), never
  inside the workbench.

## Round 11: the sidebar made simpler, and chat beside the terminal (2026-10-06)

From the design hand-off "Conductor UI.dc.html" (the 2-series is chat, the
3-series the sidebar; the 1-series is the app as built and stays), approved
by the owner on 2026-10-06. The plan: two tracks of pull requests, the
sidebar S1–S7 and chat C1–C5, each matched to its screen ids.

### Decisions

- **The sidebar lists sessions and runs** (3a, S1). A run is one block,
  kept whole, placed where its most urgent member is, its members by urgency
  then the crew's order, an exited member inside with Resume; the crew is
  not listed; a machine is a tag on a hosted session's row, not a heading;
  this server's own loose sessions say `server`, members say nothing (they
  run here); the path leaves the row for its tooltip (3f). Shared with you
  sits below your own sessions. Exited folds to one line. The model is
  `sidebarModel` in `web/app/utils/sidebar.ts`; the rail draws from it too.
- **Sections fold from their headers** (3c, S1), remembered per browser in
  `conductor.sidebar.folds`; a folded header keeps its count and up to six
  status squares; Exited starts folded; Needs you opens again by itself for a
  new prompt (`newlyNeedingInput`, the notifications' own rule), the first
  list a page sees priming the comparison so a reload reopens nothing.
- **No scoping on a run page** (3f, S1): the full list shows with the open
  run's block marked, its section unfolded and scrolled into view; the Crew
  box and the laptop headings are gone.
- The counts: Needs you is how many sessions need you, members included;
  Running is that section's live sessions; Exited is that section's.
- **Actions on a row** (3c, S2): Share, Stop and More appear on hover in
  place of the dot, outside the row's link (a link holds no button); a run
  header offers Share run, Stop run and Open run; a member has its own Share
  and Stop. Stop asks in the row, with the design's words
  (`stopQuestion`). A right-click or a touch long press (Reka's context
  menu) opens the same list as More (`rowMenuItems` in
  `utils/sidebarActions.ts`): Open, Open the run, Share…, Show in the Yard
  (`/yard?focus=<id>`), Stop…; a run header Open run, Share run, Stop run.
  One Share dialog serves every row, mounted on its target a tick before it
  opens so that it mints the link.
- **Answer in the row** (3b, 3c, S3): `rowPrompt` (`utils/sidebarActions.ts`)
  turns a row's prompt into choices (numbered 1..9, `UKbd`) or a reply
  field, and marks a hosted session whose host is away, where the controls
  wait disabled with "The host is away". `SidebarPrompt` sits below the
  row's link (a link holds no button). The answer takes the Yard's path,
  `useQuickReply` (a short-lived control connection, the relay for a hosted
  session), and its busy set, so the Yard's card and the row agree; the
  stream clears the prompt and the row moves to Running by itself. The
  field stops its keydown events, so the window's digit listener and, from
  S4, the list's keys never see what is typed.
- **The list's keys** (3c, S4): one `keydown` handler on `[data-session-list]`
  in `SessionSidebar.vue`, live while a row has the focus (`focused`, a row
  id, set by `focusin`; the filter's ↓ puts the first row there). ↑ ↓ or J K
  move by `moveFocus`, Enter opens, 1–9 answer the focused row's prompt
  through S3's path (digits are consumed even without a prompt), R opens a
  member's run, S shares, X asks in the row with the question's Stop button
  focused (Enter stops, Escape cancels), Escape leaves the list. Every key the
  list owns calls `preventDefault` and `stopPropagation`, so the Yard's J K
  and `QuickReplyBar`'s window digit listener (which also returns for a
  target inside `[data-session-list]`) never see it; a field in a row keeps
  its keys, Escape excepted. The stop question is one model
  (`confirmingId`): a hover Stop and the X key set the same one, and the
  row components take `confirming` as a `defineModel`. Focus is by row id,
  so a row that moves sections after an answer keeps it. `SIDEBAR_SHORTCUTS`
  ("The sidebar") lists the keys after Everywhere in the shortcuts modal;
  the `/` row moved into it. Links stay tabbable in the usual way: no roving
  tabindex, Tab walks the rows.
- **The header's menus and a foot of pages** (3b, S5): `SidebarHeaderMenus`
  beside the name holds Alerts (the two switches of `useAttentionSettings`)
  and your menu behind your initials, an amber chip while no workbench
  token is set (`accountItems` in `utils/sidebarActions.ts`: Your name…,
  Keyboard shortcuts, Toggle theme, Workbench token…, Forget token). The
  foot holds the pages alone; the six-icon `[data-sidebar-tools]` row is
  gone. On the rail the same two stack in the foot above the expand button.
  Your name is a small dialog (`[data-name-dialog]`, its input still labelled
  "Your name"); `WorkbenchTokenGate` stays the layout's, opened by the menu.
- **The managed runs' ledger in SQLite (#340, 2026-10-22).** The owner's
  choice for the harness (#334): `internal/crew/ledger`, the one importer of
  `modernc.org/sqlite` (SQLite translated to Go, so `CGO_ENABLED=0` stays),
  one database `runs/managed.sqlite` in the data directory, 0600, opened
  with WAL, a full sync (on darwin a full fsync), foreign keys and a busy
  timeout, one connection: a transition is one `BEGIN IMMEDIATE … COMMIT`
  that puts the rows, records the run's next event and answers a command,
  then bumps the run's revision. Schema v1: runs, tasks, attempts, checks
  (a log of at most 1 MiB with its SHA-256), commands (the receipts, keyed
  by run, actor and id: a repeat with the same content gets the same
  receipt, other content `command_conflict`, 4,096 per run), events (a
  gapless `seq`). `Recover` at start turns what the last owner left open
  into interrupted or uncertain and writes one `recovered` event per run,
  once. `Prune` deletes a closed run's rows when nothing cites it. The
  owner lock `runs/owner.lock` (flock, tried once at open and then in the
  background for 10 s, so a server replacing one still shutting down
  serves at once and takes the lock as it is let go): held by another
  server, unsupported by the filesystem, a database that fails its check or
  one a newer Conductor wrote all open the ledger read-only, logged, the
  legacy runs untouched. `Run.Managed` (omitempty) marks a managed run's
  record with its revision: the records never count or prune it, never
  mark it interrupted, never write an older revision over a newer one;
  `Records.Delete` is the owner's. `GET /api/runs/{run}/commands/{command}`
  reads a receipt without acting. Nothing writes to the ledger yet: a crew
  with tasks (row 4) and its dispatch (row 5) come next; the 503 for a
  managed launch and the snapshot rebuilt from the database come with
  them. Go, race: the ledger's schema, a newer schema and a damaged
  database read-only, the owner lock across two processes, a transition all
  or nothing with a commit made to fail, 50 goroutines' events gapless,
  receipts, recovery concluding once, pruning and what is not prunable,
  the bounds; the records' managed rules; the receipt route.
- **A run's record is never pruned while the engine holds the run (#335,
  2026-10-22).** `Records.Save` journals a run from its launch on and, on
  every save, prunes the oldest records past 500 by start time without
  asking whether a run is live: a long run's record, the oldest, was
  deleted and written again two seconds later, and a server that ended in
  between had no record of it. `prune` now leaves out the records of the
  runs the engine holds and never counts them (`Records.held`, set by
  `RecordEnds` from the engine's `held` set, a lock-free map written in
  `add` and `forget`); the check takes no engine lock, since the save a
  forget makes (`journalForgotten`) prunes under it. Go, race:
  `TestAHeldRunsRecordSurvivesThePrune`, with that save run inline under
  the lock.
- **A bounded runner for commands Conductor runs outside a PTY (#338,
  2026-10-22).** `internal/proc.Run(ctx, Spec)`: an argv array, never a
  shell line; an absolute directory; a required timeout (at most two hours)
  past which the process group is killed, as the probes kill theirs; the
  environment a session gets (`pty.BuildEnv`: the allowlist and the
  caller's `Allow`, `CONDUCTOR_*` and the blocked loaders out, a
  `CONDUCTOR_*` or loader name in `Env` refused before the start), then
  `TERM=dumb` and `NO_COLOR=1`; stdin from nothing; stdout and stderr
  together through a pipe of Conductor's own, the first 64 KiB and the
  last 16 KiB kept in memory with the total and `Truncated`, an optional
  log of the first 1 MiB, a dropped line and the last 256 KiB, written on
  a goroutine of its own so a writer that blocks holds nothing up (the log
  is cut, and `LogErr` says so); `Started` called with the pid, the group
  and the start time once the process exists and before any output is
  read; the result's exit code or signal, `TimedOut` (the run's own
  deadline) told from `Canceled` (the caller's context, however it ended),
  and `Killed` when a child the process left behind still held its pipe
  when the process exited. The group is killed when its leader has exited
  and before it is reaped (a `waitid` without reaping on Linux, a kqueue
  on macOS say so; a process gone before the watch was set up counts as
  exited; a watch that cannot be set up, or fails while the process runs,
  ends the command at once rather than invent an exit, named in `Err`):
  the exited process keeps its pid, and so the group's id,
  until it is reaped, so the group is still its own and no other's, and
  what the process left behind goes with it, its output redirected or not.
  No signal to the group is sent once the reaping has begun, and a cancel
  in flight finishes before it; the process itself is killed with the
  group, so a command that joined another group is still ended at the
  timeout. A child that left the group (setsid) and kept the output is
  given 2 s, then left alone, named in `Err`. `golang.org/x/sys` becomes a
  direct dependency for the two notifications (it was indirect already).
  No callers yet: the managed-run checks (#334, row 5) are the first. Go,
  race: exits and a signal, a bad spec refused, the group killed at the
  timeout, a cancel told from a timeout, a leftover killed after exit 0 and
  exit 3, head and tail and the log over 1 MiB, the environment stripped,
  `Started`'s pid.
- **A crew can define tasks: who does what, after which results, checked
  how (#342, 2026-10-22).** Row 4 of the agent harness (#334,
  `docs/plans/agent-harness-order.md`). A saved crew takes `tasks`, each
  `{id, member, kind: work | review, instructions, needs[], checks[{id,
  argv, timeoutSeconds}]}`, and a crew-level `setup[]` of commands run in
  every new attempt worktree; `Crew.Validate` holds them to the server's
  rules (ids `^[a-z0-9][a-z0-9_-]{0,39}$` and unique, 128 tasks, known
  members and needs with no cycle, a review needing one work task and
  having no checks, every member with a task, 8 checks of argv within the
  member-args bounds and a timeout of 1–3600 s, instructions of 8,000
  characters, isolation `worktree`, every member started by its tasks:
  `start.when: "task"`, refused in a crew without tasks). `session.CrewRef`
  gains `task` and `attempt`, set in an attempt's environment as
  `CONDUCTOR_TASK`, `CONDUCTOR_ATTEMPT` and `CONDUCTOR_TASK_FILE`
  (`crewInject`). A config key `crews.managed` (`CONDUCTOR_CREWS_MANAGED`,
  on by default). Until a run can dispatch tasks (rows 5 and 6) and
  continue after a restart (row 8), the server refuses on its own:
  `Launchable()` refuses a crew with tasks ("cannot be launched yet", `400
  invalid_crew`), and a managed run's record is not resumed, nor are its
  members started, added or resumed by hand (`409 managed_run`). The
  editor: a Coordination select (start rules | tasks) that makes the crew
  managed (worktrees, every member by its tasks) and shows a Tasks table
  (`CrewTasksTable`: id, member, kind, needs, instructions, checks in a
  popover with `ArgvInput` chips, a quoted argument one element; the Setup
  commands below), the members table losing its Starts column, the rules
  mirrored client-side (`taskProblems`) under each row and in the Save
  gate, Launch disabled with the reason. `docs/protocol.md` has the body
  and the bounds. Go, race: the validation table (`tasks_test.go`), the
  store round-trip with a legacy crew's JSON unchanged, `crewInject`, a
  managed crew saved then refused to launch, a start rule refused in it, a
  managed record refused by resume. vitest: `taskProblems` and the row
  problems, `toCrewInput` with tasks, `draftTask`, `tasksRenamed`, the task
  start's words. Playwright `tasks-editor.spec.ts`: a cycle blocks Save
  with its words; a check's quoted argument saves as one element; the
  saved crew's tasks as sent; Launch disabled and refused by the server.
- **A managed run dispatches tasks into attempt worktrees and takes their
  results (#344, 2026-10-23).** Row 5 of the agent harness (#334). A crew
  with tasks launches a managed run: its tasks are dispatched as attempts, in
  plan order as their needs have results, each a fresh member session in
  `<cwd>/.conductor/worktrees/<run>/<task>.<n>` on `crew/<run>/<task>.<n>`,
  made at the commit the task starts from (its first need's result, the run's
  base, or its own last attempt's result when that failed a check), with the
  task in `.conductor/task.md` and `task.json`, the crew's setup run first and
  one line typed that says where the task is and how to submit; four attempts
  in flight per run, three per task, one per member (its previous session let
  go first); a place among the server's sessions is reserved before anything
  is made (`session.Reservation`), and a refusal spends nothing. A result
  (`Engine.SubmitResult`) needs the worktree on the attempt's branch and
  clean apart from `.conductor/`, HEAD descending from every input (a
  review's HEAD the candidate's), no `.conductor` directory and no submodule
  in the tree; a work task's checks run in a detached checkout of the result
  commit after the crew's setup there, one at a time across the server, each
  recorded before it starts, with its process and with its outcome, and the
  checkout compared with the commit after each (content, mode, link target,
  through an index of its own; HEAD still the commit); a check that exits
  non-zero, times out, moves the checkout or changes tracked files fails the
  attempt at it. Every change of state is made and recorded in one
  transaction of the run's, the dispatch that rests on it after the commit:
  a run's transactions commit in order, each with the state as it was, a
  change the ledger refuses is undone or pauses the run's dispatch, and the
  end is reported once its commit is in; a submitted result is kept whole in
  the attempt's row (summary, issues, verdict, findings: the ledger's schema
  is version 2 for it, a version 1 database brought to it as it opens), and
  the checks' checkout refuses a crew directory the result turned into a
  link. A live managed run, or one with a change the ledger has not taken, is never
  evicted for room; a cancelled attempt's context and session end with
  whatever commit makes the cancel durable; the end of a
  managed run is told once the change that ended it is committed, and a stop
  the ledger refused is the stop's error. A done finishes nothing (one reminder), a handoff is noted
  and never typed, a session ending before its result fails its attempt.
  Every transition goes to the ledger before its side effect. The run reports
  `tasks`, `paused` and `outcome` and is finished by the outcome, not by its
  sessions ending; PauseRun, CancelTask and RetryTask drive it; Stop ends the
  attempts and waits for the checks. The engine's launch gate is lifted with a
  ledger; the API's stays until row 6 (the routes, tools and CLI). Tests: the
  engine with a fake ledger and fake sessions (`managed_test.go`), the
  registry's reservations, the web run-state mirror. Not here: the agent's
  routes and tools, the run page, recovery (rows 6–8).
- **An agent reads its task and submits its result; a review's findings
  lead to a bounded repair (#344, 2026-10-23).** Row 6 of the agent harness
  (#334), in the same PR as row 5. An attempt's session reads its task and
  submits its result with its own token: `GET /api/sessions/{id}/task`
  (`task.md` in pages of 32 KiB, with `task.json`) and `POST
  /api/sessions/{id}/task/result` (`201` once recorded, `200` for the same
  commit again, `409` with why and the attempt running on; the workbench
  token is refused, a result is always the agent's); the MCP tools
  `task_context` and `task_result`, `conductor task context|result`
  (`--summary`, `--issue`, `--commit`, `--verdict`, `--finding
  "SEVERITY[:PATH[:LINE]] MESSAGE"`, `--file` for strict JSON; loud outside
  a session, unlike `crew`), and the skill's "Your task" section. A review's
  `needs_changes` reopens the task it reviewed (`Engine.reopen`): the task
  has no result, its next attempt starts at the reviewed commit with the
  findings in its task file (`task.json`'s `repair`, "Findings to address"
  in `task.md`), and what rested on the result is withdrawn transitively
  (`withdraw`): the review and every dependent blocked again, their results
  dropped, an attempt in flight cancelled at stage `withdrawn` with its
  session stopped once recorded, none of it counted against the tasks; with
  no attempt left the task fails keeping the repair for a retry. Commands
  with receipts (`internal/crew/commands.go`): `POST /api/runs/{run}/pause|
  unpause` and `…/tasks/{task}/cancel|retry|accept` take `{commandId,
  expectedRevision?}` and answer `{receipt}` written in the change's own
  transaction (the same id again is the same receipt, `repeat: true`, also
  for a failure; other content `409 command_conflict`; a stale revision
  `409 stale_revision` and nothing kept); `Engine.PauseRun`, `CancelTask`
  and `RetryTask` share the changes; Accept takes a failed task's last
  submitted commit as its result. `GET /api/runs/{run}/tasks/{task}` gives
  every attempt with what it submitted. The API's launch gate is lifted:
  a crew with tasks launches when `crews.managed` is on and the ledger is
  the server's (`409 managed_off`, `503 ledger_unavailable`);
  `checks.maxConcurrent` (`CONDUCTOR_CHECKS_MAX_CONCURRENT`, 1) bounds the
  checks running at once. The e2e stub is a worker (`STUB_WORKER=mcp|cli`,
  catalog ids `worker` and `worker-cli`): it reads the task through
  `conductor mcp` on stdio or the CLI, commits `stub-<member>.txt` (and
  `stub-fixed.txt` once a finding or an attempt before asks), reviews
  `needs_changes` once then `approved`. Tests: the engine's review gate,
  withdrawals, receipts and Accept (`commands_test.go`), the routes
  (`api/tasks_test.go`, the launch gate included), `conductor task` and the
  tools (`cli/task_test.go`, `mcp_test.go`), the skill, and Playwright
  `tasks.spec.ts` (a check failing once, the repair, the dependent at the
  accepted commit, the run's outcome, no start by a done) and
  `tasks-review.spec.ts` (a review's needs_changes and the approved repair).
- **The run page shows tasks, their checks and results, and what to do next
  (#344, 2026-10-23).** Row 7 of the agent harness (#334), in the same PR as
  rows 5 and 6. A managed run's page has a **Tasks** tab first
  (`?view=tasks`; a legacy run asked for it gets the grid): one row per task
  in the crew's order (`RunTasks.vue`) with its state dot and badge, kind,
  member, attempt, what it does in words (`taskWords`: what it waits for,
  which attempt runs or checks, how it ended, why a ready task does not
  start), its result commit, and its actions (Open the attempt's session,
  Retry, Accept anyway…, Cancel…, Copy branch; `taskActions` says which
  apply); the selected task, kept in the URL as `?task=`, opens its result
  (summary, issues, findings) and every attempt with what it submitted
  (`GET /api/runs/{run}/tasks/{task}`). Cancel and Accept anyway ask first.
  Every command carries a receipt (`utils/tasks.ts`: the browser mints the
  id, a reply lost to the network is asked for once more with the same id,
  a refusal's words come from the receipt; `useTaskCommands` for the toasts).
  The header says `3/7 tasks`, `1 failed`, `2 running`, `1 checking` and
  `paused` instead of the member counts, and has a Pause / Resume button for
  the dispatch (a receipt too). The grid's tiles say the member's task
  (`build #2`). The graph gains a **Tasks | Members** switch on a managed run
  (Tasks unless switched; the choice kept with the graph's preferences): one
  node per task (`CrewGraphTaskNode.vue`: kind, id, state, member, attempt,
  words, Open) in columns by depth (`utils/taskGraph.ts`, the crew graph's
  geometry; a need the run has no task for, or a cycle, is a root with a
  warning), a dotted `needs` edge in the brand colour from each task to the
  tasks that need it (`data-graph-edge-kind="needs"`), the legend for both;
  a phone gets the tasks as a list, one after the tasks it needs. The palette:
  the run page's group gains a row per task (Enter shows it in the Tasks
  view; → opens its session, retries, accepts or cancels, the last two asked)
  and a Pause / Resume row; a run row's detail says `2/5 tasks · 1 failed ·
  paused` and offers Open the tasks. The web client gains `runTask`,
  `pauseRun` and `taskCommand`, `ApiError` the whole reply (`body`) for a
  refusal's receipt. Tests: vitest `tasks.test.ts` (counts, badges, words,
  actions, the id, the retry-once wrapper and the refusal) and
  `taskGraph.test.ts` (depths, warnings, layout both ways, edges, the list
  order); Playwright `tasks.spec.ts` gains the page: the rows and their
  states, the commit against `git rev-parse crew/<run>/build.2`, the header's
  `2/2 tasks`, the detail's attempts with the failed check and what was
  submitted, the tile's `build #2`, the graph's two task nodes and one needs
  edge and the switch to the members, the palette's task row; and a crew
  whose check always fails: `1 failed`, Pause and Resume with the badge,
  Accept anyway through the dialog finishing the run with attempt 3's commit
  and the dependent starting from it.
- **A managed run continues after a restart without repeating finished
  work (#344, 2026-10-23).** Row 8 of the agent harness (#334), in the same
  PR as rows 5–7. The server, taking the ledger's owner lock at start,
  concludes what the last one left open (`ledger.Recover`) and writes the
  record of every managed run whose record lags the ledger or is missing
  as the ledger has it (`crew.Recovered`, `syncRecords`: the run
  interrupted when the recovery found it running, each task as its row and
  its last attempt say, the sessions ended), so the run reads right until
  it is continued. `POST
  /api/runs/{run}/resume` on a managed run, held or recorded, makes a
  continuation (`Engine.ContinueRun`, `internal/crew/continue.go`): a new
  run of the crew as launched from the same base, the old named in
  `resumedFrom` and naming the new in `resumedBy` (the ledger's
  `continuedFrom`/`continuedBy`, written in the continuation's first
  transaction), each task taken up as its rows say: a result that stood
  kept by reference (`importedFrom`, no attempt, a dependent's task file
  naming the old run's branch), a failed or cancelled task as it was, a
  work task interrupted or stopped while its result was being verified
  given a session-less attempt that verifies the same commit again
  (`reverify`/`dispatchCandidate`, `result.reverified`, not counted), a
  task whose attempt's launch is uncertain failed with why until a retry,
  the rest ready or blocked; the attempts spent before count against the
  tasks (`task.prior`) and number the next attempt, so branches never
  collide. The web continues a managed run from its header (Continue) once
  it stopped or ended with an outcome, and says what was kept and what a
  continuation takes up. Tests: `crew/continue_test.go` (results kept with
  the old branch as input, fresh attempts numbered after the interrupted
  one, the rows naming each other, the candidate verified again without a
  session, the uncertain launch waiting for a retry, the refusals, the
  recovered record), `api/continue_test.go` (the resume route continuing
  a stopped managed run once, the live run refused), vitest for the
  words, and Playwright `tasks-resume.spec.ts`: a server of its own ended
  while a check runs, started again on the same data, the record
  interrupted, the continuation verifying the candidate again and the
  dependent starting from it, the old run naming the new, the pages.
- **Members of a managed run exchange durable messages (#344,
  2026-10-23).** Row 9 of the agent harness (#334), in the same PR as rows
  5–8. A member sends another a message (`POST /api/sessions/{id}/
  messages`, the MCP tool `message_send`, `conductor task send --to`):
  kept in the ledger (schema version 3: a `messages` table, with the
  checkpoint column of row 10; a version 2 database is brought to it) and
  typed into the recipient's session as one line, `Message from <from>
  (#n): …`, after its task line through the handoff queue once its attempt
  has one (`deliverMessages`, `handoff.seq`; a member not started yet gets
  it then; a continuation carries what was not delivered); the recipient
  reads its inbox (`GET …/inbox`, `inbox_read`, `conductor task inbox`)
  and acknowledges (`POST …/inbox/ack`, `inbox_ack`, `conductor task
  ack`), keeping at most 50 not acknowledged (`429 inbox_full`). A handoff
  reported in a managed run is a message to the member it names. The run
  log and the events (`message.sent`, `message.delivered`,
  `message.acked`) say so; `GET /api/runs/{run}/messages` lists them. The
  skill's "Messages, blockers and checkpoints" section says how. Tests:
  `crew/messages_test.go` (typed after the task line and recorded
  delivered, the inbox and its acknowledgement, the refusals, the full
  inbox, a handoff as a message, a continuation carrying a message),
  `api/messages_test.go`, `cli/task_test.go`, `mcp_test.go`, the skill
  test, and Playwright `tasks-messages.spec.ts` (the chatty worker's
  message reaching the writer once it has its task, read from its inbox).
- **A member reports a blocker and saves a checkpoint (#344,
  2026-10-23).** Row 10 of the agent harness (#334), in the same PR. An
  attempt reports what blocks it (`POST /api/sessions/{id}/task/blocked`,
  `task_blocked`, `conductor task blocked --reason --message`): the task
  carries the blocker (`blocker {reason, message, by, at}`; the Tasks view
  says "attempt n blocked by lead: reason"), the session asks the message
  as the agent's own question, and a `working` or `done` report of the
  attempt (`OnActivity` → `unblock`), or its result, clears it
  (`task.blocked`, `task.unblocked`). An attempt saves a checkpoint (`POST
  …/task/checkpoint`, `task_checkpoint`, `conductor task checkpoint
  --done --remaining --notes`): what is done, what remains and notes, tied
  to the commit its worktree is at, kept on the attempt and in the ledger
  (`attempts.checkpoint`, `task.checkpoint`); the task's next attempt reads
  it in its task file ("Where the last attempt got to"), and an attempt
  after one that ended without a result, or a continuation's first attempt
  at the task, starts at the checkpoint's commit (`startCommit`), so a
  crashed session's work is not lost. The Tasks view shows an attempt's
  checkpoint. Tests: `crew/checkpoints_test.go` (the blocker shown,
  cleared by a working report and by a result, refused on an ended
  attempt; the checkpoint kept in the row, the next attempt starting there
  with the section in its task file, a continuation's first attempt too),
  `api/messages_test.go`, `cli/task_test.go`, and the chatty worker's
  checkpoint in `tasks-messages.spec.ts`. The Codex review of rows 6–10
  (ten findings) led to: a command taken in a transaction of its own
  before it acts (its row `accepted`; the cap, a conflict and a repeat
  answered there with nothing changed), settled in a second, a stale
  revision kept as a failed receipt; a checkpoint bounded as kept (16 KiB)
  before anything changes, never a review's (its attempt always starts at
  its candidate), cleared on a dependent when its input is withdrawn;
  texts cleaned (`session.CleanMessage`) and bounded in bytes before they
  are kept or typed; the delivery intent recorded before a message is
  typed (`message.typing`), an uncertain delivery not typed again after a
  restart, a line the recipient's end cut off before it went in recorded as
  not typed (`message.untyped`) and typed into the next session; a continuation carrying repairs and allowances (`tasks.repair`,
  `tasks.extra`), every message under its own rows, and an import's source
  and summary through later continuations; delivery going on past the
  queue's room; a recovered record dropping a withdrawn result
  (`review_fixes_test.go`).
- **Yardmaster by hand: real crews on WSL and the Mac, and the Windows app's
  RC (#350, 2026-10-24).** Row 11 of the agent harness, named Yardmaster
  (#334). Three crews ran on the WSL box (Claude Code 2.1.309 and 2.1.310 on
  Opus 5.5, Codex 0.163.0; main at 2e88f38) and one on the Mac (Codex 0.163.0
  in every seat, since Claude Code there is not signed in), each a `build`
  work task with checks, a Codex `review` of it and a dependent `docs` task,
  in a scratch repository both agents trust, yolo on, driven through the API
  with the workbench token and read through the run page in headless Chromium.
  The first WSL crew finished in 2.5 minutes with every task succeeded: the
  builder read the checks from its task file and wrote the CHANGELOG the
  second check wanted before submitting (a check the task file names never
  fails once), the writer reported its blocker, messaged the reviewer, saved a
  checkpoint and submitted, and the Tasks view, the task detail, the graph and
  the grid said so. The second: a check that fails its first run (a script
  outside the worktree) failed attempt 1 and attempt 2 passed from attempt 1's
  commit; a task whose check always fails spent its three attempts (the agent
  wrote "Accepting it anyway is a decision for a person"); Pause answered a
  receipt, the same id again the same receipt with `repeat`, a stale revision
  `409 stale_revision`; Retry while paused made the task ready and its attempt
  waited; Unpause ran it; Accept anyway took attempt 4's commit and turned the
  run's outcome from failed to succeeded. Codex's "Update available" dialog
  held the reviewer's task line until Escape dismissed it as a person would,
  and the line and a waiting message went in. The third: the reviewer's
  needs_changes reopened `build` with its finding and withdrew the finished
  docs result; during the repair attempt the server was killed and started
  again on the same data within a second: the run read interrupted with the
  attempt so marked, Continue made a continuation that dispatched the repair
  as attempt 3 of 3 at the reviewed commit with the finding in its task file,
  and the review asked for changes a second time (NOTES.md began with `# Notes`,
  not `reviewed`): build had no attempt left, the run ended failed with the
  task's words saying "retry it to allow one more", Retry gave attempt 4, which
  fixed the line, the review approved on its third attempt, docs ran a third
  time from the approved commit and the run succeeded. Claude Code's own safeguard paused one builder session
  ("Session paused", two numbered choices); it showed as needing input and the
  digit pressed in its terminal continued it on Opus 4.8. On the Mac every task
  succeeded; the Codex writer used the CLI forms (`conductor task
  blocked|send|checkpoint|result`); the ledger's files are 0600 on APFS; the
  Codex builder ran the first-run check itself, so it passed at the server.
  Found and filed, none stopping a run: #351 (a task that needs a review
  starts at the run's base, the input commit empty), #352 (a message to a
  member with no attempt in flight is never typed though its session lives),
  #353 (a blocker's question stays asked after the result), #354 (Codex starts
  `conductor mcp` without the session's variables, so every Conductor tool
  fails under Codex and it falls back to the CLI; `mcp_servers.conductor.env_vars`
  fixes it, proven with `codex exec`), #355 (a failed check's output reaches
  neither the attempt's words nor the next task file, and the start commit is
  labelled the run's base). v0.7.0-rc.16 was published as a prerelease for the
  Windows app pass, which waits for the app's restart. Docs: the railroad
  names (Conductor, Yardmaster, Brakeman #356, Switchman) in the README,
  `docs/architecture.md`, `docs/repository-map.md` and AGENTS.md; the
  architecture's crew-runs section gains the managed run.
- **The extension seams: the `ext` and `app` packages, the free defaults
  and the eight interfaces wired in (Program items C1 to C9; #473 to #489,
  started 2026-10-31).** The plan is `docs/plans/ext.md` (PR #453), cut
  into seventeen PRs, one paragraph each below; each PR rewrites its own
  paragraph only, with what it shipped and its tests, and the last
  completes the entry with what was observed. Conductor is open core: this
  repository is the whole free product, and a paid feature is a provider
  that another module's `main` compiles in, backed by a hosted service.
  The free build registers nothing, so every seam runs its free default,
  which is what main did before the stream.

  PR-1 (#473, PR #514): `ext`, standard library only: the package
  documentation (what a provider is, that its `init` registers and does
  nothing else, how it starts, the guard's rules from its side, the API's
  stability); the registry (`Register…` for the six one-value slots and
  the two lists of at most `MaxPerList` (16), the accessors, `Registered`,
  `Freeze`, `Providers` with `All`, the eight kinds; a register panics on a
  nil value, a bad name, a second provider in a one-value slot, a repeated
  name in a list, a list past its bound, a name another value holds and
  after `Freeze`, and takes one value in several slots under one name);
  the eight interfaces and their records with JSON tags, bounds, waits and
  a `Valid` method on each record a provider hands the server (`RoomJoin`
  and `Entitlement` have none: no code of this repository takes them);
  `ICEServer.Valid` takes only STUN and TURN URIs WebRTC takes, and a TURN
  one only with a username and a credential, since one server Pion or a
  browser refuses fails the whole configuration; `Starter`,
  `Host`, their records and errors, declared for PR-4. `app` (`Run`,
  `Main`), and `cmd/conductor`'s `main` as `app.Main()`. Tests:
  `TestRegisterRefuses`, `TestRegisteredOrder`, `TestFreezeOnceAndCopies`,
  `TestAllListsEverySlot`, `TestRegistryConcurrent`,
  `TestRecordsRoundTrip`, `TestValid`, `TestRunVersion`,
  `TestPlainBuildRegistersNothing`, `TestExtBoundsAreTheServers` (each
  bound `ext` copies from `internal/proto` and `internal/crew` held to its
  source) and `TestExtICEServerValidAsPion` (a server `Valid` takes is one
  Pion takes). Nothing calls `ext` yet.

  PR-2 (#474): ext/local: the free defaults and Fill. Planned.

  PR-3 (#475): cli, api: the composition root reads the registry once; every
  provider call guarded. Planned.

  PR-4 (#476): api, cli: ext.Host and the providers' Start. Planned.

  PR-5 (#477): opencore: the open-core rules as a lint, failing on a planted
  import. Planned.

  PR-6 (#478): api: the identity provider after the token chain; a
  principal's subject in receipts. Planned.

  PR-7 (#479): api: notification sinks over the event hub, the webhooks the
  first. Planned.

  PR-8 (#480): crew, api: context blocks in the task files; file touches to
  the context providers. Planned.

  PR-9 (#481): session, api: Local.AnswerAs and the decision router over
  needs_input. Planned.

  PR-10 (#482): proto, hostagent, signal, api, web: answer_as, a routed
  answer for a hosted session. Planned.

  PR-11 (#483): store, ledger, api: the sync hooks after every local commit.
  Planned.

  PR-12 (#484): export, ledger, dispatch, api: the export archive, GET
  /api/export and the restore installer. Planned.

  PR-13 (#485): cli: conductor export and conductor restore. Planned.

  PR-14 (#486): opencore, contributing: no entitlement call site outside
  ext. Planned.

  PR-15 (#487): api: the four ICE sites through the media provider; GET
  /api/ice keeps credentials. Planned.

  PR-16 (#488): api, web: GET /api/capabilities and the app card's
  Extensions row. Planned.

  PR-17 (#489): ext/exttest: a fake per interface and the contract suite;
  the stream's features entry completed. Planned.
- **Governance: contributing under the DCO, the marks' owner, security
  reporting and the open-core pull request checklist (Program item B; #386
  and #387, 2026-10-28).** The plan is `docs/plans/governance.md` (PR #383).
  PR-1 (#386, PR #388, merged 2026-10-29 01:38Z): `CONTRIBUTING.md` (what to
  read first, who decides, issues and pull requests with the fork path for a
  contributor without write access, the sign-off and the repair of a branch
  that lacks it, the checks, the four open-core rules, the checklist, the
  rules for code, the licence); the `DCO` text (1.1, verbatim); `AGENTS.md`
  naming the maintainers, the owners of the `rocksolidlabs` organization,
  where it said the owner, with the sign-off bullet and the fork path; the
  pull request template's five boxes and its sign-off line;
  `scripts/dco_check.py` (every commit of `base..head`, merges included,
  carries a `Signed-off-by` with the author's name and address; exit 1 names
  each commit that does not and the fix, exit 2 a read that failed; it reads
  the repository named and its whole history, whatever the environment
  selects, cuts or rewrites) with its tests; the `dco` job in `ci.yml` among
  `required-checks`' needs; the preflight step, first; `docs/checks.md`.
  PR-2 (#387, PR #392): `TRADEMARKS.md` (the marks; their owner, RockSolid
  Labs, Inc.; what the licence does not grant; what is allowed without
  asking and what is not; how a fork renames itself and where the marks are
  written in; the spelling; the address), `SECURITY.md` (GitHub's private
  vulnerability reporting first and the mailbox second; what to include; an
  acknowledgement within seven days, the fix as an ordinary pull request
  that names the change and not the vulnerability, the advisory published
  when the fix has shipped, no bounty; the scope; the supported versions;
  the rule for everyone working here), `NOTICE`'s owner paragraph (twelve
  lines still; the packages and the image copy it unchanged), the issue
  chooser's "Report a vulnerability" link, and the pointers in
  `CONTRIBUTING.md`, `AGENTS.md`, the task template, `docs/design/brand.md`
  and `docs/repository-map.md`. Decisions (the plan, Owner decisions):
  "RockSolid Labs", and "RockSolid Labs, Inc." for the entity (O11); the
  check is a CI job, not the DCO GitHub App (O13); the maintainers are the
  organization's owners (B1); the certificate's text at the root (B2); the
  sign-off is the author's own, merges included, no bot exemption (B3);
  four rule boxes and the checks box, the sign-off as a line (B4);
  conductor@rocksolidlabs.io for the marks and as the security fallback
  (B5); an acknowledgement within seven days, no bounty (B6); the private
  repositories unnamed (B7); the railroad names of Conductor's parts
  neither claimed nor disclaimed (B8); the README left to stream A (B9);
  nothing on `main` re-signed (B10); two PRs in order (B11); the one
  force-push of the acceptance check (B12); the fork path (B13); the
  cutover by closing and reopening every open PR (B14). Observed:
  acceptance observation 1, the red half: the throwaway PR #389
  (`issue-386-unsigned`, one commit without `-s`) ran
  https://github.com/rocksolidlabs/conductor/actions/runs/40782459422 with
  `dco` red, its log naming the commit and the fix, and `required-checks`
  red. The green half, on 2026-10-29 at the owner's word: #389 was
  reopened first (a push to a closed pull request's branch fires no run),
  and its run
  https://github.com/rocksolidlabs/conductor/actions/runs/40902106218 was
  red again on the unsigned commit; then the one force-push B12
  authorizes (`git rebase --signoff origin/main`, `458482eb` to `f8f9f478`)
  ran https://github.com/rocksolidlabs/conductor/actions/runs/40902150072
  with `dco` and `required-checks` green, every job green. The green case
  on a real PR: PR #388's own run
  https://github.com/rocksolidlabs/conductor/actions/runs/40782335246,
  `dco` printing `3 commits signed off by their authors`, every job green.
  Observation 2: `TRADEMARKS.md` and `NOTICE` both name RockSolid Labs,
  Inc.; a maintainer's review approval on PR-2 approves the wording.
  Observation 3: PR-2's body is the first opened after PR-1 merged and
  carries the template's four rule boxes and the sign-off line.
  Observation 4: the throwaway PR #390 (`issue-386-before`, cut from
  `main` before the job) ran
  https://github.com/rocksolidlabs/conductor/actions/runs/40782472193 with
  no `dco` job at all; after the cutover (B14, 2026-10-29: every open pull
  request closed and reopened) its run
  https://github.com/rocksolidlabs/conductor/actions/runs/40877819815 has
  `dco` red, naming the commit (`184f920b`, no `Signed-off-by`) and the
  fix, and `required-checks` red. Neither throwaway merges; #390 was closed
  after its run. Private vulnerability reporting, off on 2026-10-28, was
  turned on by a maintainer and read back `enabled: true` on 2026-10-29,
  after PR-2 merged. `internal/crew`'s `TestAContinuation*` flaked at
  `TempDir` cleanup on the throwaway and plan runs (#281, one line each),
  until #391's fix, PR #421, merged.
- **The repository moved to rocksolidlabs/conductor (#380, 2026-10-27).** The
  GitHub transfer (the ruleset, the `live` environment, the workflows, the
  labels, the issues and the advisories came along; the old URLs answer 301)
  is followed by one PR: the module path and every import, the three `-X`
  ldflag sites (Makefile, Dockerfile, release.yml), the switchyard page's
  links, the web app's source link, the desktop app's releases URL and
  update-feed owner, the Linux package maintainer
  (`conductor@rocksolidlabs.io`), `release_draft.py`'s fallback repo and the
  two web fixtures that use the checkout's path; the historical plan and
  review docs keep their commit-pinned links. `scripts/preflight.py` and its
  test carry the project's copyright line. New: `make check-version`, run by
  CI after `make build-go` and by preflight, compares the binary's `version`
  line with the version and commit make injected, and the release workflow's
  Linux runner does the same for the linux-x64 package binary: the linker
  ignores a `-X` for a symbol that no longer exists, so a missed site would
  otherwise ship binaries that say `dev (unknown)`. Installed apps keep
  updating through GitHub's redirect; a repo or fork at the old name would
  delete it.
- **An example crew run by tasks (#377, 2026-10-26).** The four example
  crews all start by rules, so Yardmaster could be tried only with a crew
  built by hand. **Example: todo app, with tasks**
  (`example-todo-app-tasks`, listed beside the todo app so the two are
  compared) builds the same app in Python with its standard library, as five
  tasks: `plan` (the lead writes PLAN.md, with core's interface given
  exactly, the test layout and a `.gitignore` for Python's caches, so a
  tested worktree stays clean to submit; checked by `test -s PLAN.md`),
  `core` and `cli` at once from the plan (cli builds and tests
  `main(argv, store)` against a fake store, since core is not in its
  worktree), `tests` (needs both: its worktree starts at core's result and
  merges cli's; it adds the end-to-end tests and owns every fix, in either
  part, including what the review sends back), and `review`. The work tasks
  after the plan are checked by `python3 -m unittest discover -s tests -t
  .`. **Load the example crews** adds it for anyone who loaded the four
  before. The Crews list says a crew whose members start by their tasks is
  `4 · by tasks`, not `all at launch`. Its run on real agents is checked by
  hand, not in CI.
- **No release tag before its release is published (#375, 2026-10-26).**
  A pushed `v*` tag is in the repository's `releases.atom` at once, and
  electron-updater (6.8.9) takes the newest entry of the app's channel
  there: while rc.17's release was a draft, every app on the rc channel
  found no `latest.yml` (a 404, logged only) and so no update at all, not
  even to the newest published version. A release is now built by
  dispatching `release.yml` with its version (`gh workflow run release.yml
  --ref main -f version=0.7.0-rc.18`); no tag is pushed. The draft names
  `v<version>`, which does not exist yet, and the commit built
  (`target_commitish`); GitHub makes the tag there when the draft is
  published, so the feed lists only published releases. `release_draft.py`
  refuses a version that is not SemVer without build metadata, a tag that
  exists, and a draft for another commit or under the bare version, in
  `ensure` and `upload` alike, and `release-check` also fails on a tag
  pushed meanwhile and on a draft without the updater's three
  `latest*.yml`. The server binaries say `v<version>`, as `git describe`
  did on a tag. A dispatch without a version builds the packages as the
  run's artifacts only. `desktop/README.md` has the steps. The tag
  `v0.7.0-rc.17`, pushed before this, still lists a draft: the apps' check
  fails until a newer rc is published or that tag is deleted.
- **A tag's packages go into one draft release (#372, 2026-10-26).** The
  three package jobs published through electron-builder, which makes the
  tag's release when it finds none (on the first page of releases only),
  and two that found none at once each made a draft: rc.13's and rc.17's
  packages were split over two, put together by hand. electron-builder now
  only builds (`--publish never`, which still writes the `latest*.yml` and
  blockmaps). A `release` job makes the draft before the package jobs start
  (`scripts/release_draft.py ensure`: named the version, a draft, not a
  prerelease, as electron-builder made it; a rerun keeps it; a published
  release or two drafts on the tag fail the job), and each package job
  uploads its packages to that draft by its id (`upload`, which never makes
  a release and replaces what a failed run left). A `release-check` job at
  the end fails unless the tag has that one draft with every package the
  jobs built and each installer's blockmap. The Windows installer is named
  `Conductor-Setup-<version>.exe` where it is built, the name
  electron-builder gave it on the release. Runs for one tag queue rather
  than race.
- **A withdrawal takes back everything the task built on (part of #334,
  2026-10-25).** A review's needs_changes withdraws what rested on the
  task it reopens; the next start was derived in four places (the run's
  attempts, the checkpoint, a continuation's refusal, the walk back along
  the runs continued) and the withdrawal cleared only the checkpoint, so a
  dependent could start at its own result built on the old input and be
  refused. Every attempt of every task resting on the withdrawn result,
  transitively and whatever the task's state, is now taken back
  (`withdrawn` in the task's row, ledger schema 4): never a start, a
  checkpoint, a candidate verified again, Accept anyway's result or the
  task file's attempt before, in the run or any continuation; a
  dependent's own repair goes, the reopened task keeps its own; a task
  that failed or was cancelled stays so and a retry starts it at the new
  input. A dense graph is walked once. A result made in a run is that
  run's own, a continuation finds an imported result where its attempt is
  or fails the task for a retry, and takes no ancestor attempt past the
  row's count. A run once continued, or being continued, takes no command
  (`409 run_continued`): the mark is made under the run's transaction
  lock, so a change under way commits first and the continuation reads
  it; a run's row written later never clears its `continued_by`. A
  version 3 ledger is migrated conservatively (the owner's choice): the
  reopened tasks are found from the events, a needs_changes review
  attempt or an attempt withdrawn, and every task resting on one takes
  its highest attempt as withdrawn and loses its repair, as does every
  continuation's task whose ancestor's had an attempt withdrawn, and what
  needs it there (version 3 could start it on that work). Plan:
  `docs/plans/issue-334-withdrawal.md` (paths 1 to 8), with the rounds 8
  and 9 refusal code it holds back.
- **The ledger takes every change it is given (part of #334, 2026-10-25).**
  Found by Codex's reviews of the withdrawal plan, each one a way for a
  managed run to stall on main. A change whose commit failed with no undo
  had its rows carried by the next commit but not its events, so the
  ledger could hold a reopened review with no `task.reopened`: its events
  are now kept with its rows as the refusal comes, for the next commit to
  write first, whoever's (an undone change's, or one given up as a check
  never run, are dropped). An event the
  ledger would refuse for its size failed every commit after it: every
  event is measured as it is made and an oversized one becomes a note of
  its size, and `check.finished`'s path lists are bounded by bytes. A
  result is measured as the ledger keeps it (JSON can make a `<` six
  bytes): one larger than the attempt's row, or a review whose findings
  make a larger repair, is refused at submission with nothing changed,
  where it used to leave a row every later commit failed on. The task
  file is cut to fit (the inputs' summaries, the checkpoint, findings,
  the review's summary, the checks' commands, the inputs after the
  first, the attempt before's error past 2,000 characters, into
  `.conductor/task-more.json` in full), and a crew whose task's own text
  would leave no room (over 40 KiB as JSON), or that needs a task twice,
  is refused as it is saved, so no attempt fails at its task file for its
  size; `task.json` no longer escapes `<`. A command's receipt keeps the task's id, state, reason, attempts
  and result commit, not the whole task, so Accept anyway of a large
  result settles. Plan: `docs/plans/issue-334-withdrawal.md` (paths 9 to
  12), reviewed ten times by Codex.
- **Yardmaster's live-check defects fixed in the stream (#351 to #355, PR
  #369, 2026-10-24).** A task that needs a review starts at the commit the
  review reviewed; a message to a member whose task succeeded is typed into
  its idle session; a blocker's question leaves with the blocker (only that
  question, also the copy held over the turn, never a newer blocker's);
  Codex's `conductor mcp` gets the session's `CONDUCTOR_*` names through
  `mcp_servers.conductor.env_vars`; a short check failure's last 2 KiB
  reaches the attempt's words and the next task file, which says what its
  start commit is. Then main's CI caught one more: an attempt whose result
  could not be checked out (a link out of the tree) handed that result to
  the next attempt, which failed at its worktree at once and spent an
  attempt nobody ran. A result refused before its checks (at checkout, or
  by the crew's setup, also when a continuation verifies it again) now
  hands the next attempt where the refused one started, and its task file
  names the refused result's branch (PR #370). AGENTS.md: a bug in the stream is fixed
  in the stream; a test waits for a state that holds, never one passed
  through.
- **Every palette row does what it says (#332, 2026-10-22).** On the
  session page `showView('chat')` handed to `showChat()`, the header
  button's toggle, so **Open Chat** from the palette (and a session row's
  Open Chat on this page) collapsed an open chat. `showView` shows: the
  inspector on the view (the sheet below `xl`) and nothing when it is
  shown already; the header buttons keep their toggles. Then a sweep,
  `palette-rows.spec.ts`, running each row from Enter or a click and
  asserting its visible effect: the session page's Share, Copy a view
  link, the marks, Fold, the views (Open Chat twice stays open), the tab
  rows and Stop; the run page's broadcast field, views, chat, share and
  stop; the Yard's chips, the Roundhouse's switches and rotation, Crews'
  New crew, Events' refresh and tabs, Agents' Launch, Settings' Save; the
  session rows' → actions (the copies by their toasts, Open Files/Chat/
  Activity/People by `?tab=`, the line's field, Resume); the verbs `go`,
  `mark`, `share`, `answer`; the theme, sidebar and notification
  switches; the name, shortcuts and launch dialogs; the prefixes. What
  the sweep found is fixed here and named in the PR: the browser's
  Settings page (the server's file and a notice) offered a Save row that
  could never act; the group is the desktop app's alone now.
- **Close tabs to the left, to the right, others, all (#330, 2026-10-22).**
  As VS Code's tab menu: a context menu on an editor tab (right-click, or
  Shift+F10 or the Menu key on the focused tab: the tab fires the event
  itself, since Chromium on macOS fires none for them) with Close,
  Close others, Close to the right, Close to the left and Close all, each
  through the close that asks about unsaved changes (`requestCloseIds`,
  today's close-all generalised: unsaved among them ask through
  `closing.ids`, else close now); the same five as → actions on the
  palette's tab rows beside Close. `utils/editorTabs.ts`: `tabsToClose
  (state, id, scope)` names the ids in strip order and `closeTabs` closes
  them one after the other, the active tab moving to the nearest kept one
  (vitest). The Yard's and the join page's editors get it with the area.
  `tabs.spec.ts`: three files open, the menu on the middle one, Close to
  the right leaves two with the middle active; Close others leaves one;
  Close all with one unsaved file asks and Don't save closes them all.
- **The Keyboard shortcuts modal, a design pass (#328, 2026-10-22).**
  `ShortcutsModal.vue`: the box follows the window (`xl:max-w-5xl`, the
  key columns `12rem` at `xl`); the description is one line that stays
  clear of the close button; the key cells lose their tints and both
  columns centre their caps in cells of equal width, every cap `size="md"`,
  so the chords sit on one axis down the table; a column with no chord
  shows nothing (an empty cell labelled "none"), not a dot; the column
  names once in the header row, sticky over the scroll, the per-group
  repeats gone; Enter shows as the word and "then" between a chord's caps
  at the caps' text size. `chords.spec.ts` asserts the column names
  appear once, no cell holds a dot, each column's cells share one axis
  within a pixel, and the close button and the description do not
  overlap.
- **An exited row's arrow and its `…` in line (#331, 2026-10-22).** In
  `SidebarSessionRow.vue` the resume/relaunch arrow is a column beside the
  link, centred in the row, while the hover bar (`[data-row-actions]`:
  Share, Stop, More) sat at the row's top, so on an exited row the `…`
  came in a few pixels above the arrow, in a box of its own. On a
  resumable row the bar is centred vertically now (`top-1/2
  -translate-y-1/2`), so the `…` and the arrow share one centre and sit
  side by side at the row's edge; running rows keep the bar at the top
  beside their first line. `sidebar.spec.ts` measures the two boxes on a
  hovered exited row: one centre within a pixel, the `…` just left of the
  arrow, both inside the row.
- **The bar's Files and Chat only below `xl` (#329, 2026-10-22).** At `xl`
  the inspector rail (#301) has Explorer and Chat, with the chat's unread
  count on the rail, so the session page's header said both twice and
  crowded the bar. The two buttons are `xl:hidden`; below `xl`, where the
  rail is not on the page, they stay (the phone's chat sheet opens from
  the header as before, `chat.spec.ts`). `?tab=`, the palette's rows and
  the chords are unchanged. `inspector-rail.spec.ts` asserts the bar has
  neither at the suite's width and selects through the rail.
- **The session page never scrolls sideways (#325, 2026-10-22).** In the
  desktop app on rc.13 a horizontal scrollbar ran along the bottom of the
  session page at every width, with the inspector panel open or collapsed.
  The rail's count pill (`InspectorRail.vue`, `absolute -right-1.5`) stuck
  out past the rail by two pixels, nothing on the path clipped it, and the
  panel's body, which scrolls on its own, grew a scrollbar for them. The
  pill sits inside the rail now (`-right-0.5`) and the inspector's
  `aside` clips sideways (`overflow-x-clip`). The page's body keeps its
  own scroll: hiding its overflow would have put a phone's quick-reply
  choices out of reach, which run past the window there today (Codex's
  round; their wrapping is its own issue). `hscroll.spec.ts` measures the
  row's own width (so a new overhang still fails), the body's, the
  document's and every pill against the rail's edge, at a wide and a
  narrow window, with the panel open and collapsed and a file open.
- **A line typed from the palette is acknowledged, with an unless-waiting
  guard (#322, 2026-10-22).** A protocol change, in `internal/proto`,
  `web/app/utils/protocol.ts` and `docs/protocol.md` together: `submit`
  takes `reqId` (at most 32 bytes of printable ASCII, as `answer`'s;
  `Submit.Valid`) and `unlessWaiting`; with a `reqId` the owner answers
  `submit_ok{requestId}` once the line is entered, or `error{code,
  requestId}` (`not_sent` with the question's words when the guard held
  it back from a session that asks, nothing typed; `input_failed` when
  typed and not entered; `too_many_requests`, `session_ended`,
  `read_only`, `bad_frame`); without one nothing is answered, as before.
  The `welcome` says `submitAck: true`. `session.SubmitWord(reqID, res,
  err)` maps a submission's outcome to the word as `AnswerResult` does
  for an answer (`SubmitResult{}` with no error is the guard's hold:
  `not_sent`); the server's viewer connection (`ws_viewer.go`) and the
  host agent's peer (`peer.go`) both take the message, the guard looked
  at as the owner holds the session's turn (`Submission.UnlessWaiting`,
  the broadcast's), a line the program takes slowly told through
  `Submission.Finished`; the hosted owner on the server relays the frame
  to the host as it relays `answer`. Web: `TerminalTransport.submitAcked
  (text, { unlessWaiting })` settles true on `submit_ok`, a `ReplyError`
  with the owner's code on its refusal, false at once to an older owner
  (a plain `submit`); `useQuickReply().say` uses it and returns whether
  the line was typed, `not_sent` said in the broadcast's words; the
  palette's `say` (the `I` field and the `say` verb) then says **Typed
  into** (or **Sent to** for an older owner); a refused line (nothing
  typed: the session asks, a view link, the session ended, too many
  lines, past the bounds) waits in the row's field (`field.draft`,
  `sayDrafts`), opened with it again; an outcome not known (no word in
  time, the connection gone, a line typed and not entered) is said **not
  confirmed**, to look at the terminal before typing it again, the line
  not offered (Codex's round: the owner finishes a submission after its
  client goes, so a second try could type it twice). `useQuickReply().
  say` returns the outcome (`SayOutcome`) instead of throwing. The drafts
  (`utils/sayDrafts.ts`, vitest) settle by generation: an earlier send's
  late word never erases a newer refused line (whole-batch round); a line
  typed while one is already on its way to the session is not sent and
  said so, the generation untouched, so the one on its way still settles
  its own draft (second whole-batch round).
  Tests: `internal/proto/submit_test.go`, `internal/session/
  submitword_test.go`, `internal/api/submitack_test.go` (the welcome,
  submit_ok, the guard against an API-flagged prompt and the same line
  without it, an unnamed submit, the bounds, a view link),
  `internal/hostagent/submitack_test.go`; `transport/base.test.ts`;
  `palette-reach.spec.ts` (the typed word); `utils/sayWords.ts` (vitest)
  for the words of each outcome and the draft kept on a refusal. No
  Playwright drives the refusal: the palette offers no line to a session
  that asks (its row answers instead), and once the page hears a session
  came to ask, the row's field is gone with the line; the refusal reaches
  the page only when the owner hears before it does, which the Go tests
  of the guard cover.
- **The editor's own rows in the palette (#321, 2026-10-22).**
  `EditorArea.vue` exposes, beside `find`, `gotoLine` and `requestClose`,
  a reactive `palette` snapshot (`keymap`, `nvimOffered`, `nvimWhy`,
  `switchBlocked` with the keymap button's own words, `saveable`,
  `saveWhy`, `canAsk`, `hasSelection`, `activeId`) and `setKeymap`,
  `save` and `askAgent` (the comment composer on the selection, asking);
  `EditorColumn.vue` passes them through as it passes `requestClose`.
  With a file open, the session page's group (and the Yard's focused
  tile's) gains **Keys → Neovim** / **Keys → default** (disabled while a
  tab is unsaved or Neovim is not offered, the detail saying why),
  **Save** (`Ctrl+S`; disabled when nothing is dirty, the connection
  may not edit, or the editor is folded: its text is not at hand then,
  and a save would have written the text as loaded and lost the edit the
  kept model holds, Codex's round), **Close file** (`Ctrl+W`; through `requestClose`, so an
  unsaved file asks) and **Ask the agent about the selection**
  (`Ctrl+Shift+A`; disabled without a selection or when the agent cannot
  be asked). A guest's join page has no palette (its own layout), so it
  gets none. `palette.spec.ts`: a file open on a session page lists the
  four rows; Save from the palette writes the file, waits while the
  editor is folded and writes once it is back; Close file on a dirty tab
  asks; Keys → Neovim is disabled while a tab is dirty and, where Neovim
  is offered, switches the keymap and back.
- **Touched across every session, from the server's index (#304,
  2026-10-22).** `GET /api/touched` (admin, `internal/api/touched.go`)
  lists every listed session's touched files from the index each session
  keeps (`op: touched`): `{sessions[{id, name, agentId, kind, status, cwd,
  touched[…], truncated?}], truncated?}`, the sessions in the list's order,
  a session with nothing touched left out, a server session's deny list
  applied; `?limit=` per session (default 500, at most 2,000) and about
  2 MiB for the reply, a cut session marked and the reply too. A hosted
  session keeps no activity log on the server; `signal.HostedSession` now
  keeps a `session.TouchedIndex` (exported for it) fed by the `file`
  entries its host reports since it connected, the paths made absolute as
  the host's OS writes them (`hostAbs`: a drive letter or UNC prefix is
  absolute, a relative path under a Windows directory joins it with `\`).
  On the way: `session.ValidEventType` predated file events and left
  `file` out, so a host's file entries were dropped on the server and the
  server's on the host; it takes them now (the protocol says seven event
  types, thirteen in all). The palette's `:` group "Touched lately" (the
  tab's feed) becomes "Touched" (`usePaletteRows.touchedItems`): the
  server's index, read after the palette's first paint and cached a minute
  (`usePalette.loadTouched`), read again on a `file` event while the
  palette is open (one read in two seconds), with the feed's live entries
  laid over it (`utils/touchedIndex.ts`, pure: `mergeTouched`, one row per
  session and file, the most recently touched first, at most 2,000 rows;
  `absUnder`/`relUnder` keep a Windows directory's separator). A row's
  label is the path from its session's working directory, its detail the
  session, the op with its tool, the time and the count; a row of this
  page's session opens the file in its editor, another session's opens
  that page with `?open=<path>` (a line in `?line=<n>`: the path is taken
  whole, a name may end in `:digits`; whole-batch round), new on the
  session page and read as `?tab=` is (never written back). The cache goes
  with the workbench token (`forgetTouched`, the component watching the
  token; a reply on its way for the old token dropped; no rows without the
  token: whole-batch round). Tests: `internal/api/touched_test.go`
  (admin only, the order, the limit and its refusals, the deny list, the
  reply's budget, a hosted session's reported entries),
  `internal/signal/touched_test.go`, `utils/touchedIndex.test.ts`,
  `palette-touched.spec.ts` (a file touched before the tab opened found
  from the index, its row opening the other session's page with the file,
  the row on the file's own page). Codex's round: `?open=` is taken only
  once the terminal is connected (taken before, the editor's read failed
  and the tab kept the failure; the spec now reads the file's text), and
  `resolveTyped` keeps a Windows absolute path (a drive letter, a UNC
  prefix) and joins a relative one under a Windows directory with `\`
  (`fileTree.test.ts`). The plan: `docs/plans/touched-index.md`.
- **The breadcrumb strip scrolls to its end (#315, 2026-10-22).** In
  `FileBrowser.vue` the watch that scrolls the breadcrumb strip to its end
  read `crumbs` above the line declaring it; a watch's source runs as the
  watch is made, so it threw at every mount (a console `ReferenceError`,
  caught by Vue and logged) and a long path never showed its last folders.
  The watch sits below the computed now, and the nav scrolls itself as
  it mounts (`@vue:mounted` on it): the header holding the strip is
  conditional, so a view switch (Changes, then Explorer again) makes a
  new nav for the same path, and a pane opened on Changes has none at
  mount (Codex's round; a watch on the template ref did not fire for the
  remade nav). `crumbs.spec.ts` opens a working
  directory whose own folder's name overflows the strip and reads the
  strip's scroll at mount, after a switch to Changes and back, and on a
  pane opened on Changes first, and asserts no error of that kind reaches
  the console while the Files pane mounts.
- **The chords per system (#312, 2026-10-22).** `utils/chords.ts`
  (vitest): `detectSystem` from the user agent and the platform (as
  Nuxt UI's kbd reads `Macintosh;`), a per-system table for the rows
  whose chord differs (`chord` on a `ShortcutRow`: `leaveTerminal`,
  `copy`, `paste`), `rowsFor(group, system)` which `useShortcutsModal`
  applies to every group it lists (the modal and the palette's `?` rows
  show the chord of the system the page runs on), and `SYSTEM_NOTES`,
  what a system or a browser takes before the page sees it, shown under
  the modal's table for that system and surface (`data-shortcuts-notes`;
  the desktop app's rows leave the browser-only ones out). The one chord
  replaced: the terminal's leave-focus on the Yard and the Roundhouse is
  `Alt+Q` everywhere (`KeyQ` in the passthrough codes; Windows takes
  Alt+Esc to cycle its windows), with `Alt+Esc` still bound where the
  system leaves it. The sidebar's chord stays `meta+B` (⌘B on a Mac,
  Ctrl+B elsewhere, as shown): Win+B and Super+B are the system's and
  were never bound; Firefox's Ctrl+B (its bookmarks sidebar) is a note.
  The desktop app's Window menu is built without Close (Electron's
  `windowMenu` role bound it to Ctrl+W on Windows and Linux): the editor
  takes Ctrl+W, Ctrl+Tab and Ctrl+Shift+Tab in the app, a browser tab
  keeps them, said in the notes. A terminal hands Alt+Q back only on the
  Yard and the Roundhouse (`usePageAltCodes`), so a session page's
  terminal keeps it; the Mac's clipboard rows say `command`, shown as ⌘
  on every Apple user agent. The validation pass is
  the table in `docs/plans/ui-batch.md`: Linux Chromium driven by the
  specs (`chords.spec.ts`, `shortcuts.spec.ts`, `palette-keys.spec.ts`,
  `palette-reach.spec.ts`, `sidebar.spec.ts`); the Windows and Mac rows
  are not run here and say so.
- **The palette's reach (#303, 2026-10-22).** `]` and `[` (`Alt+]` /
  `Alt+[` from a terminal: `BracketRight` and `BracketLeft` joined the
  passthrough codes, taken by the layout's chord listener) step a cursor
  through `needingInput` in the sidebar's order, from the page's own
  session when it is one (`utils/needsCursor.ts`, `useNeedsCursor`), and
  show `NeedsYouPill.vue` at the top of the viewport for four seconds:
  the agent chip, the name, the question, "n of m", the choices as
  numbered buttons; `1–9` answer over `useQuickReply` without leaving the
  page, `Enter` opens, `Escape` dismisses, the pill goes by itself and
  with the question; its keys are read on the document while it shows,
  never from a text field or a terminal, and the Alt twins give the pill
  the focus so the next digits are its. `?tab=files|chat|activity|people`
  on the session page (read, not written: the rail item selected, the
  chat sheet below `xl`), `?view=` on the run page, `?filter=` on the
  Yard (`data-yard-filter` chips). `useLaunchModal().show({ agentId,
  cwd, args, yolo })` prefills the Launch dialog once its catalog is in:
  Launch <agent> here (each agent, in the page's directory), Launch
  another like this (the session's agent, directory, yolo and the
  arguments after the agent's own command), the `launch <agent> [in
  <dir>]` verb. `useQuickReply().say` types a line into a running session
  over `submit` on a short control connection (closed once the line is
  typed; the owner's Enter follows on its own), refused with the
  broadcast's words while the session waits on a prompt: `I` on a row
  opens the field, the `say <name> <text>` verb reads as a row. Answer
  all (`utils/needsCursor.ts` `sameQuestions`): the same question
  (message and choices) asked by several live sessions is one row at the
  top of Needs you, its choices answering every one, one send each, the
  toast counting them. `Stop every session that is done` asks with their
  names; `Clear the new-event badges` clears every mark. `G S` opens
  Settings in the desktop app (`Alt+,`, `Comma`); on a session page `S`
  shares and `X` opens the palette on its Stop row asking (`Alt+X` in
  its terminal, `KeyX`), listed with the editor's keys as the page's
  group (`SESSION_SHORTCUTS`). Playwright: `palette-reach.spec.ts`.
- **The command palette (#302, 2026-10-21).** `CommandPalette.vue` in the
  default layout, opened by `meta_k`, `/`, `alt_/` and `alt_s` (the two
  Alt chords from a terminal; `Slash` joined `ALT_PASSTHROUGH_CODES`, and
  `TerminalView` lets ⌘K through on a Mac) and the rail's search button:
  the sidebar's filter box is a plain filter now. A modal of 680 px at the
  top of the viewport with its own list (not `UCommandPalette`, which
  keeps the focus in its input and has no list focus, so the brief's rule
  that digits answer only while a row has the focus could not hold): the
  input, a mode chip, the groups, the rows in the sidebar's grammar (agent
  chip with the state dot, name, `agent · where · how long`, the prompt's
  message, its choices as numbered buttons or a reply field, held and away
  as the sidebar says them), the keys of the brief on the focused row
  (`J K ↵ 1–9 R S ⇧S X → ← ⌘↵ ⇧↵ Esc`; `I` waits for #303), a row's
  question asked in the row (`stopQuestion`'s words; Restart server names
  how many sessions end), a row's field in the row (a broadcast, a run's
  name). The pure parts in `utils/palette.ts` (vitest): `parsePrefix`,
  `parseVerb` (the twelve verbs; a name that matches more than one thing
  of the kinds the verb takes is a search), `rank` (Needs you first, the
  page next, matches, frecency within a group, the group's order),
  frecency in `conductor.palette.recent` (200 entries, decayed by the day,
  every read and write in try/catch). `utils/actions.ts` holds the one
  action system: `rowActions`, `accountActions`, `stopQuestion`,
  `toMenuItems`; `sidebarActions.ts`'s `rowMenuItems` and `accountItems`
  are views over them (vitest proves them item for item), and the palette
  renders the same descriptors under →. `usePalette` (open, query, show
  with a mode, `registerPage`, the crews and the catalog read after the
  first paint and cached, read again when a minute old) and
  `usePaletteRows` (every group and prefix from the live stores, the
  verbs, the handlers the sidebar and the pages have). Every page
  registers its group: the session page (Share…, Copy a view link now,
  Stop…, the three marks, Fit to my window when the sizer chip shows, Fold
  the editor, Open Files / Chat / Activity / People, the open tabs; under
  `:` the tabs, the changes and the commits the inspector polls, exposed
  by `SessionInspector`; under `@` the people and the links with Revoke),
  the run page (Broadcast… with "Send to N" and who is skipped, Add an
  agent…, the three views, Share run, the chat with its count, Stop run…
  or Resume as new run, the members with the one asking first, Start now
  and Resume where they apply; `CrewRunHeader` exposes its buttons), the
  Yard, the Roundhouse, Crews (Launch, Launch named…), Events (the tabs,
  Refresh, Install hooks for each adapter), Agents, Settings. The `?`
  modal lists the palette's keys as a group of its own. Playwright:
  `palette.spec.ts` and `palette-keys.spec.ts`; `sidebar.spec.ts` reaches
  the filter box by a click now. Decisions beyond the brief: a path under
  `:` or `open` needs a page with an editor (the session page, the Yard's
  focus, a join page), elsewhere the row says so; Open Files / Chat /
  Activity / People from another page open the session page alone until
  #303's `?tab=`; Shared with you offers Open and Forget (no Leave exists);
  `⌘P` and `⌘⇧P` are not taken (Ctrl+P is the browser's print); the
  Install hooks rows read the integrations when the palette opens, the one
  request besides the two cached lists, since nothing else knows them.
- **The Roundhouse's strip rearranged, and shown as thumbnails (#310,
  2026-10-21).** The chips in the strip at the foot (`CarouselStrip.vue`)
  can be rearranged by drag (HTML5 drag, no library) and by keyboard: a
  chip picked up with Space, moved with the arrows, dropped with Space or
  Enter, put back with Escape (`aria-grabbed`, a live region saying the
  move). The order is kept per browser with the Roundhouse's settings
  (`conductor.carousel.settings`, `order` by session id; the pure parts in
  `utils/carousel.ts`: `orderSessions`, `moveInOrder`, `dropIndex`,
  `keyMove`, `normalizeSettings`): the carousel's slides, stepping,
  autoplay and "Next up" follow it; follow mode still jumps to a waiting
  session first and goes on in the kept order; a session started since
  goes at the end, one that ended leaves it. A button in the strip's
  corner (`data-strip-view`) switches chips and thumbnails (`strip` in the
  same settings): a thumbnail is the slide's own screen as text
  (`TerminalView` exposes `snapshot()`; the strip reads the near slides'
  every second while thumbnails show), and the idle mark with the
  initials and the state for every other session, never a connection of
  its own (the second whole-batch round: a view of its own doubled the
  connections and took a viewer slot), so the Roundhouse's cost stays the
  current slide's and its neighbours'; a thumbnail is inert, and a
  read-only view types nothing. The picked chip
  is known by its session id, so a session ending mid-move moves nothing
  else. The
  Yard's tile order is a separate thing and unchanged.
- **The editor beside the terminal (#309, 2026-10-21).** The column the
  editor shares with the terminal (`EditorColumn.vue`) lays the two out
  stacked, as before, or side by side, the editor on the left and the
  terminal on the right, chosen with a button in the editor's tab strip
  (`data-editor-layout-switch`, shown at `lg` and up; below it the column
  is stacked whatever is kept, and the phone stays as it is). The bar
  between them drags either way and is a real separator
  (`role="separator"`, `aria-orientation`, focusable): the arrow keys along
  its axis move it a step of 2 %, Home and End take it to its ends. The
  terminal keeps a minimum width side by side (320 px, about forty
  columns) as it keeps its minimum height stacked, and its size reaches
  the agent as any resize does; a column too narrow for both (its own
  width, measured, not the window's) stays stacked and offers no switch,
  and the reply bar shrinks with the terminal's pane. The layout (`conductor.editor.layout`)
  and each layout's split (`conductor.editor.split`,
  `conductor.editor.split.side`) are kept per browser; the pure parts are
  `utils/editorLayout.ts` with vitest. The default stays stacked until the
  person switches.
- **The inspector rail (4h, #301, 2026-10-21).** One vertical rail of 44 px
  on the inspector's outer edge (`InspectorRail.vue`, its items, pills
  and tooltips pure in `utils/inspector.ts`) replaces the two tab rows:
  People, Explorer, Changes, Commits, Activity, Chat (when the owner takes
  chat), icon buttons with a tooltip to the left naming the view and its
  count, neutral count pills capped at `99+` (viewers, changed files,
  commits, unread chat; none on Explorer and Activity), the active item
  elevated with the 2 px primary bar of #314. Clicking the active item
  collapses the panel to the rail, whose pills still read; any item opens
  the panel on its view; the toggle at the rail's foot does the same and
  replaces the header's panel button (`conductor.inspector` keeps
  open/closed); the header's Files and Chat buttons select rail items with
  their toggle-if-shown behaviour. Each view opens with a two-line title,
  the bold name then the mono sub-line it computes. Touched is no longer
  a view: Activity carries the chips All · Files · Tools, Files listing
  the touched rows (`useTouched`, the `op: touched` index merged with the
  live `file` entries, its count on the chip), Tools the tool uses and
  refusals. `FileBrowser` lost its section row and takes `section` as a
  controlled model (`explorer | changes | commits`); its git polling moved
  into `useRepoStatus`, owned by the inspector (or a Files aside), which
  is the one behaviour change beyond layout: the status every 5 s while
  Changes shows and the log every 10 s while Commits shows, each every
  30 s otherwise so the pills stay right while the panel shows another
  view or is collapsed, nothing while the document is hidden, one request
  at a time per kind. The pane's reads at mount keep under the owner's
  four in-flight file requests (the touched index two seconds after the
  pane is up, the log after the status), and the tree's listing retries a
  `too_many_requests` as it retries a connection not open yet: CI's run
  on a slow relay found the pane empty otherwise. The Yard's focused aside and the join page's Files
  aside carry the three Files items on a rail of their own
  (`FilesPane.vue`), which does not collapse. Below `xl` nothing changes.
  No protocol change.
- **The open session told apart on the rail, two squares never alike
  (#314, 2026-10-21).** The square of the session open on the page carries
  the selection mark: a 2 px primary bar down the rail's edge beside its
  outer (left) side (`data-rail-current`, 8 px out in the rail's margin,
  clear of the corner badges), with the elevated background it had; the
  state dot stays top right and the news count bottom right, so the mark
  is another shape in another place and never reads as a state. Two
  squares never look the same: `railGlyphs` (`utils/sidebar.ts`, run by
  `railModel` over every square, a capsule's members included, exited
  ones too) gives a square whose agent's initials another square shares
  its folder's letters instead (`folderLetters`: the last element of its
  working directory, upper case; a link shared with you, which names no
  folder, keeps the initials), and then, across every square whatever its
  agent, an ordinal top left (the free corner) when the letters still
  match, in the rail's order. The tooltip stays the full label. The
  inspector rail (4h) uses the same bar for its active item.
- **The rail** (3d, S6): `railModel` (`utils/sidebar.ts`) draws the rail
  from the list's model, the links shared with you and the Events page's
  marks: `RailShape`s in the list's order, a square a session, a capsule a
  run holding its members' squares (`SidebarRailSquare`), the shared
  squares with a globe, exited runs dashed, loose exited sessions folded
  into `+N`; two counts on top (`counts.needs`, and how many sessions carry
  a mark). The corners: the state top right (the amber one following the
  Events page's Badge route as the rows' dot does), new events bottom right
  (one per session today: the Events page keeps one badge a session; C4
  folds the unread chat into the same number), the origin tile bottom left.
  Every shape's tooltip names it in words (`railRunLabel`: "users api · run
  started 08:31 · review needs you · core running · lead exited"). A capsule
  is a `div` with the play icon a link to the run and each member its own
  link (a link holds no link). `railGroups` and its types are gone.
- **The phone** (3e, S7): the list is the home screen, `pages/sessions/index.vue`
  (`SessionSidebar page`, `[data-session-list="page"]`), where `/` sends a
  phone (at lg and up the route does what the home does: the first session
  worth looking at); the layout renders the sidebar's own list only at lg and
  up (`useMedia`, the same composable as C2's), every page navbar has no
  hamburger (`:toggle="false"`), and `BottomBar` sits at the foot below lg:
  Sessions, Yard (the count), Crews, Events, More (a `UDrawer` with
  Roundhouse, Agents, Settings on the desktop app, `AlertSwitches` and
  `accountItems` as buttons; `NameDialog` and `AlertSwitches` are split out
  of `SidebarHeaderMenus` for it). The session, run and workbench join pages
  get **Back** to the list below lg (`[data-back-to-list]`). A prompt in the
  page's list takes full-width 44 px buttons (`SidebarPrompt big`). A touch
  long press (500 ms, `pointerType` touch) on a row or a run's header opens
  `SidebarRowSheet` (`UDrawer`, `rowMenuItems` as 44 px buttons, Cancel);
  the context menu is off for a coarse pointer and the row's callout
  suppressed; Stop asks again in the row. `viewport-fit=cover` lets the bar's
  padding read the safe area, and the dashboard group leaves it room below
  lg. No swipe actions.
- **Chat beside the terminal** (2a, 2b, 2h, C1). One chat per session,
  over the connection the terminal takes (`chat`, `chat_send`,
  `chat_history`; `welcome.chat` gates a client), so it works wherever a
  link works; through a switchyard the publishing machine sends the
  switchyard none of it, and a viewer relayed through the switchyard has it
  in relayed frames, which the switchyard can read. Every viewer may post; a
  controller's "To agent" and "Send to agent" type a message into the
  agent through `Submit` and leave a `sent_to_agent` marker. The session
  keeps 200 (`session.chatRing`), replays the newest that fit 128 KiB, and
  takes no post once ended. Chat lines are not activity entries: their own
  ring, frames and hook (`Options.OnChat`), no bucket shared with hook
  reports, no 500-byte cut, nothing in the run log or the webhooks. Chat
  frames are encoded without HTML escaping (`proto.MustControlRaw`) so a
  2 KiB message fits the 8 KiB control frame. Join and leave lines coalesce
  by name. Unread is the browser's own count, in memory for now; the
  sidebar's pills come with C4. A per-connection bucket of 10 a second,
  burst 20, is new: viewer connections had no message bound before.
- **Chat on a phone and on a guest's page** (2c, 2d, C2). No server
  change. Where the inspector is not rendered (below `xl`) the session
  page's header gets a Chat button with the unread count that opens the
  chat as a sheet: `UDrawer` from the bottom on a phone (snap points two
  thirds and full, no overlay, not modal, so the terminal stays live and
  usable behind it), `USlideover` from the right in between. The join page
  wires the same thread over the guest's own connection (so a switchyard
  guest has it too): a panel beside the terminal at `md` and up, which the
  header's button folds away, the sheet below; a view-only guest reads the
  2d note and sees no agent actions. A thread counts as open only while it
  is in front of the person (`useMedia` says whether the inspector is
  rendered), so a phone counts what the hidden inspector's tab would have
  swallowed. Nothing in a hello or welcome changed.
- **Run chat** (2e, 2f, C3). One thread per run across its members:
  `session.ChatRoom`, made by the engine's run, which every member session
  is made with (`Options.RunChat`) and joins at launch. Read and posted
  over any member's connection with scope `run`, so a run link's guest and
  a run published through a switchyard have it without the switchyard or
  a run socket knowing (option A of the plan; option B, a run socket, was
  rejected for that). A member's new viewer is replayed the run's chat
  after the session's; the people on any member make the roster
  (`chat_roster`, one row per name with the member they look at, bounded
  at 32 so a full list of the longest names fits a control frame; `count`
  says how many); join and leave lines are counted once across the
  members. The run page opens one quiet connection (`hello.chatOnly`) to a
  live member and moves it when that member ends: no scrollback or output,
  not a viewer of the session, no line in its chat, on the run's roster
  (`useRunChat`). A controller's `chat_send` with scope `run` types a kept
  message into the member it names as a broadcast does, nothing while it
  waits on a prompt, answered `not_sent{needs_input|not_running|unknown|
  no_enter}` (the broadcast's reasons, now `session.NotSent*`); the
  composer's menu (`scopeItems`) disables such a member with the broadcast
  bar's words before anything is sent. The run's chat goes into its record
  (`chat` on `GET /api/runs/{run}` and the record; lists strip it; 500
  worst-case messages stay under the 4 MiB record read) and stays open
  while the run is kept; a resumed run starts a new one. The `send` emit of
  the chat components now names where a message goes ('', `agent`, a
  member).
- **Unread elsewhere** (2g, C4). The admin stream carries `chat` events
  (`eventHub.chat`, from `Options.OnChat` and `Engine.OnRunChat`, as
  droppable as `activity`, reaching no sink), so the browser counts the
  chats it has no page on. `utils/chatUnread.ts` keeps the counts per
  thread in `conductor.chat.unread` with the thread's last opening and the
  last 64 message ids, so the same message from the connection and the
  stream counts once, nothing older than the opening counts, a reload
  recounts nothing seen, and another tab's writes are read back
  (`storage`). `ChatUnreadPill` sits on every session row and run header
  (neutral, never a status colour); the rail's bottom-right number is the
  Events badge plus the unread chat, a capsule's its members' plus the run's
  own, and the tooltip names both ("alone · running · done · 3 unread in
  chat"). A hosted session's chat stays on the host until C5.
- **Hosted chats on the stream** (2g for `conductor host`, C5). A host sends
  each message its session keeps as `chat{sessionId, message}` on its
  control connection (the local session's `OnChat` hook queues it on the
  same bounded forwarder as activity, one goroutine sends in order); the
  server's `HostedSession.HostChat` cleans it (the connection's session
  whatever the host named, the known kinds only, scope `session`, bounded
  ids, cleaned names, the text cleaned and cut to the bound, `to` only as
  `agent`) and the hub's `OnChat` hands it to `eventHub.chat`. A server
  publishing its sessions to a switchyard sends their chat up the same way
  (`Published.OnChat`), so the switchyard's stream shows it as a hosted
  session's. `HostedSession` stays a pass-through for the chat itself: the
  host keeps it and serves its viewers. Found by the chat tests on the way:
  a session's end revoked its links and closed their viewers as "link
  revoked" (4403), racing the status frame, so a guest on a view link could
  miss that the session ended; `DisconnectLink` now closes them as the
  session's end (4410) once it has ended, which the join page already takes
  as ended.
- **The agents' questions in the chat** (C6, the owner's fast follow-up:
  "can we have the agents send questions to the chat as well when input is
  needed?"). A session going `needs_input` keeps the attention's message
  and choices in its chat as a `question` from the agent (`By.Role`
  `agent`, `Options` the attention's, at most six; `askInChat`), and the
  answer, whoever's input cleared the prompt, as a `system` line with
  `event: answered` and `ref` the question's (`answeredInChat`; "Conductor"
  when nothing named the person). A run member's question and its answer
  go into the run's chat too, on the member (`tellRun`). The thread draws a
  question amber-tinted, its choices as buttons for a controller while it
  stands (`answeredQuestions` in `utils/chat.ts`), typing the choice's
  input as the quick-reply bar does: into the session on its page and a
  guest's, into the member from a run's chat (`useQuickReply`, which now
  reaches a link's server too, for the guest on a run link). A question
  counts as unread like any message; the answered line never does. A
  question of the longest message with six of the longest choices fits the
  control frame (`TestAQuestionFrameFits`).

### Verified (round 11)

- S7: `phone.spec.ts` at 390×844 with touch: `/` lands on the list page with
  the filter on top and no sidebar list or hamburger; the bar's five targets
  are 44 px and the Yard's count shows; a prompt's buttons are full width and
  44 px and one answers; a row opens its page and Back returns; a long press
  opens the sheet with Open, Share…, Show in the Yard, Stop…, Cancel, Stop
  asking again in the row, and a run's header's with Open run, Share run,
  Stop run…; More lists the pages, the alerts and your menu. By hand on a
  phone (the keyboard on a reply field, the safe area): pending.
- S6: `sidebar.spec.ts` collapses the sidebar and reads the two counts, a
  capsule holding two members with the amber play icon while review asks
  and its tooltip in words, the news corner on a session given a `done`
  attention, the machine tile on the hosted stub, the stopped run's capsule
  and members dashed, and `+N` opening the full sidebar on Exited; vitest
  covers `railModel` (the order, the counts, the capsule's members and
  label, the folded exited, every tooltip).
- S5: `sidebar.spec.ts` finds no `[data-sidebar-tools]`, opens Alerts from
  the header, lists the account menu's five items, sets a name through the
  dialog (the button then says whose menu it is), and reads the foot as the
  five pages; `theme.spec.ts` toggles the theme from the menu; vitest covers
  `accountItems`.
- S4: `sidebar.spec.ts` drives the keys: `/` then ↓ focuses the first row,
  J K move, Escape leaves; with a page open on one asking session and the
  other's row focused, `1` answers the row's session alone (the transcripts
  and the page's attention say so) and the row keeps the focus as it moves to
  Running; X asks and Escape takes it back, S opens the share dialog, Enter
  opens the row, R from an exited member opens its run. vitest covers the
  shortcuts group.
- S3: a choice clicked in the row (`choices.spec.ts`) and a line typed in
  the row's field (`sidebar.spec.ts`) reach the stub's transcript, clear the
  prompt and move the row to Running without leaving the page; `rowPrompt`
  in vitest, the host-away state among it.
- S2: the hover actions, the in-row stop question, the context menu and the
  run header's actions are Playwright-covered in `sidebar.spec.ts`
  (share from a row, Show in the Yard, a member stopped from its row, a run
  stopped from its header landing whole in Exited, a loose session stopped).
  By hand: the long press on a phone, pending.
- S1 rendered headless at 1440, dark and light, against 3b: the mix of two
  loose sessions (one asking), a run of three (one asking, one exited), a
  hosted session with its machine, Exited folded "· 4" with squares; on a
  run page the block is marked. By hand on the installed app: pending.
- C1 rendered headless at 1440 against 2a (dark and light, the hover
  action) and 2b (the closed tab's count). By hand: pending.
- C2 rendered headless at 390 against 2c (the header's count, the sheet
  open over the terminal, dark and light) and at 1440 against 2d (the bare
  guest page with the panel and the note). By hand on a phone (the keyboard
  pushing the composer up, the drag to full height): pending.
- C5: Go tests for the cleaning in `signal` (every kind, a bad role, no
  id, a bad time, the cut), for the fake host's message reaching the stream
  and a bogus kind costing nothing (`ws_e2e_test.go`), and for a real host
  whose relay viewer's post reaches the stream (`hostagent`); Playwright
  `chat.spec.ts` scenario 7 (a line said in a hosted session counts on
  another person's sidebar row).
- C4: Go tests for the `chat` event's shape and its reach (no sink) and
  for the stream carrying a session's and a run's chat; vitest for the
  store (counted once, never a system line or one's own, nothing older than
  the opening, bounded ids, the read and write) and the rail's unread;
  Playwright `chat.spec.ts` scenario 6 (two messages count on another
  person's row and rail corner, survive a reload, clear on opening and stay
  clear; a run's header counts the run's own chat).
- C3: Go tests for the room (fan-out across members, the replay order, the
  roster, joins coalesced, quiet connections, sends with every refusal),
  the roster's bound (proto) and the run chat over WebSockets with a run
  link, the record and the lists (`crews_test.go`); Playwright
  `chat.spec.ts` scenario 5 (two people on the run page, the composer's
  menu with review skipped and lead typed into, a guest on the run link
  with the view-only note, the run stopped). Rendered headless at 1440
  against 2e and 2f. By hand: a run published through the switchyard from
  the LAN box with a phone on its link, pending.

## Round 10: the startup splash, the sponsor credit, a folder button on every "Runs in" (2026-10-05)

Asked on 2026-10-05, with the round 9 checks: "whenever we show 'runs in' we
should have the same folder button to the right"; "the settings page we
should use the whole width"; a startup banner while the app starts, with the
logo and the build; a copyright; and "sponsored by RockSolid Labs" following
the RockSolid Labs Sponsor Kit (claude.ai/design).

### Decisions

- **The folder button is part of the directory field**, so the crew editor's
  "Runs in" and the launch dialog's working directory both have it. Its
  picker browses the allowed roots only (scope `roots`, no Home), opening on
  the field's text or, when empty, the server's default directory.
- **Settings uses the whole width**: two columns of cards from `xl`, the
  save row under them.
- **The splash** is a small frameless window shown as soon as Electron is
  ready, until the workbench window shows (or 20 s): the Conductor mark, the
  version, platform and Electron, what the app is doing (checking WSL,
  preparing the server, starting it, opening the workbench), the copyright,
  and the kit's "Sponsored by" badge. It is dark, the switchyard pages'
  palette, with no preload; its link opens in the system browser.
- **The credit, in the kit's words** ("Sponsored and maintained by RockSolid
  Labs", linking to rocksolidlabs.io), only where the kit puts it: the last
  row of Settings → The app (desktop and browser), the switchyard pages'
  footer, under the card of a guest's join page on a switchyard, the last
  line of `conductor version` and `--help`, the README, and the splash. Never
  in the workspace, a log or run output. The logos are RockSolid Labs' own,
  bundled (`web/public/sponsor`, `desktop/static/sponsor`), never fetched.
- **The copyright and the license.** "© 2026 the Conductor Authors and
  RockSolid Labs, Inc." (the owner's wording, 2026-10-06), on the splash, in
  Settings → The app and the README; the repository is under the Apache
  License 2.0 (`LICENSE`, with a `NOTICE` naming the holder and keeping the
  RockSolid Labs and Conductor marks out of the grant), named in Settings →
  The app, the switchyard pages' footer and the README.
- `GET /api/whoami` gains `version`, for The app card in a browser.
- **Tests at both ends, as a rule** (AGENTS.md, 2026-10-06): every feature
  ships with a Go test and a UI test (vitest for logic, Playwright for what a
  person sees); the by-hand list is only for what no harness reaches. With
  it, the browser test of a crew run link through the switchyard, deferred
  in round 9: a second server publishing to the spec's switchyard, Share on
  its run page saying "Works from anywhere", the link listing the members on
  this workbench, a member's terminal through the relay, and a revoke at
  home ending the link there (`web/e2e/switchyard.spec.ts`).
- **What is owed lives in one place**, `docs/tasks-todo.md`: the bugs known
  and the features set aside, by area, each item with the round that found
  or deferred it; the rounds' own "Deferred" lists stay as history.
- **Zoom in the desktop app** (reported on Windows: zoom in did nothing).
  Electron's zoom roles bind Ctrl+Plus, which needs Shift, and on Windows and
  Linux the page sees a key before the menu, where the terminal takes Ctrl+-
  as a control character. The app now takes Ctrl+= / Ctrl++ / numpad +,
  Ctrl+- and Ctrl+0 (Cmd on macOS) before the page sees them, and keeps the
  level in its settings, since the server's port, and with it Chromium's
  per-origin zoom, changes at every start.
- **A stopped run is recorded as stopped.** Stopping a run ends its members'
  sessions first, and the last one ending recorded the run as finished on a
  goroutine racing the stop's own record: about one stop in twenty kept
  "finished" (the CI flake in `TestRunRecordIsWrittenWhenARunEnds`, seen
  at 100 runs under race). A run being stopped is now recorded only by its
  stop, and the record saves keep the order their snapshots were taken in.

### Verified (round 10)

- 2026-10-06, by the owner on the installed v0.6.0-rc.6: the splash, the
  folder buttons and the full-width Settings page, the credit on the
  switchyard pages and in `conductor version`, and zoom (Ctrl+=, Ctrl+-,
  Ctrl+0, the View menu, the level kept) all passed.

## Round 9: the Events page, the switchyard dark, remote sessions in the sidebar, the WSL picker, and the leftovers of rounds 7 and 8 (2026-10-04)

Asked on 2026-10-04: the Events page from its design hand-off; the
switchyard pages dark as their design has them; "the settings in the
windows app opens the windows folders which makes zero sense"; remote
sessions in the sidebar "with an icon above similar to the crew icon", so an
invite no longer takes the app over; the Wall named the Yard and the
Carousel the Roundhouse; and the deferred items of rounds 7 and 8 the owner
picked.

### Decisions

- **The Events page** has three tabs: Feed (default: events by minute, a crew
  member named `<crew> / <member>`, Answer / Open run / Open on the row; beside
  it the last hour as bars, where events go, who reports), Routing (the ten
  events grouped by how loud they are, destinations as switches) and
  Integrations (a table, the snippet of an agent nothing wires open below).
  The bars are divs; with the Wall's donut gone too, `nuxt-charts` has no
  user and is dropped.
- **The switchyard's pages are dark always**, whatever the system prefers
  (the owner: "the switchyard UI should be the dark mode theme"); no toggle.
- **The Yard and the Roundhouse.** `/yard` and `/roundhouse`; `/wall` and
  `/carousel` redirect, a focus query kept; the keys stay G W and G C.
- **Remote sessions in the sidebar.** Hosted sessions sit under their machine
  (a laptop and its name) after this server's own and the crew runs. A link
  joined from the workbench is kept in the browser (`conductor.joined`, at most
  20, pinned to its server) and listed at the top under "Shared with you" with
  a globe; opening it joins at once, Leave keeps it, × forgets it, and it is
  looked at again every minute. The join page sits beside the sidebar when the
  page has a workbench token (the desktop app, an owner's browser), chosen
  once so a live terminal never remounts; a guest's page is unchanged and
  keeps nothing.
- **The folder picker browses the server.** `paths.browse: any`
  (`CONDUCTOR_PATHS_BROWSE`, which the desktop app sets) lets
  `GET /api/paths?scope=any` list outside the roots; Settings' picker is a
  workbench modal over it, so on Windows it lists the distribution's folders.
  The Electron dialog and the Windows path conversion go.
- **A run's own name** (`Run.label`, given at launch, `conductor up --name`,
  kept by a resume) and **filters on the Runs tab** (state chips, a time
  select, kept in the browser).
- **Render fixes:** handoff edges on an arc of their own, the Yard's
  attention as a slim stacked bar, the phone run header's counts on a strip.
- **The upgrade notice:** a settings file from before the notices existed
  owes the publishing notice, shown once as a toast; a fresh install owes none.
- **The switchyard:** the open hosts of one address share one relay bound;
  a host registers under an instance and a local id, which fix its session's
  id, so a switchyard restart gives it back and `registered.links` says which
  links survive; the switchyard keeps the links it mints for such hosts in
  `links/` (a join while the host is away answers `503 host_offline`); and a
  crew run's link is minted there (`link_run`, kept current by
  `link_run_update`).

### Verified (round 9)

- 2026-10-05, by the owner on the installed v0.6.0-rc.4 and the deployed
  switchyard: all nine by-hand checks passed. The upgrade notice, the
  Settings picker inside WSL, an invite beside the sidebar under "Shared with
  you", a `conductor host` session under its machine, a run link from a phone
  across a switchyard restart, the dark switchyard pages, the Events page and
  the Yard / Roundhouse names, the View / Control switch on a Share link, and
  a session that ends taking its links (no revoke error). The one follow-up
  (a folder button beside every "Runs in", Settings using the whole width)
  is round 10.

### Deferred

- An e2e test of a run link through the e2e switchyard (the Go loopback test
  covers the protocol and both servers).
- Pushing an invite to the open window over a bridge event instead of a
  full load; the join route answering a browser origin named in Settings;
  link tokens in the OS keychain; a page behind a machine's sidebar header.

## Round 8: saved crews and runs told apart (2026-10-04)

From the design hand-off "Crews Redesign": one page held both the saved crews
and their runs, a narrow list where each crew carried a "Running" badge and
its runs were unnamed rows ("3", "33s…"), beside an editor that filled the
screen. Nothing said which was the plan and which the thing running.

### Decisions

- **Two nouns, said every time.** A crew is a saved plan; a run is one
  launch of it. "Crew view" is the run page; "Launch 5 agents" is "Launch
  run"; "Stop all" is "Stop"; "Resume run" is "Resume as new run"; "Share
  crew" is "Share". A run is named by its crew and its start ("users api ·
  run started 08:31") with its short id beside it, never by its id alone.
- **Status belongs to runs.** A saved crew never says Running or Ready: its
  row says "1 live run" when one exists, leading to it, and its page shows a
  banner for each live run ("started 08:31 · 3 running · review needs you ·
  docs waits for Start now · Open run"). "Draft changes" became an
  unsaved-changes bar at the bottom of the crew's page that names what
  changed and, while a run is live, says the run keeps the version it
  launched with.
- **One Crews page, two sections.** `/crews` is the home: "Running now"
  (one card per live run: badge, the members as chips in the crew's shape
  with a status dot each, a line per member that asks with Answer beside it,
  up time, Stop, Open run) above "Saved crews" (a table: name and goal, the
  shape in miniature with words such as "5 · a chain of 3, 1 by hand, 1
  alone", where it runs, the last runs as bars, Edit, Launch run).
  `/crews/<id>` is one saved crew with Setup and Runs tabs; `/crews/new` the
  draft of a crew not saved yet (the server never derives the id `new`).
  The summary `GET /api/crews` lists carries the goal now.
- **Start rules in words.** "At launch", "After lead is done", "When you
  press Start", each with an icon, in the table's Starts column, on a graph
  node's last line and in the sentence beside the Members heading ("lead →
  core → tests start in a chain; docs waits for you; review starts at
  once"). Graph edges read "when done"; a member started by hand is drawn
  dashed; the column gap grew to 96 px so the label fits. "Runs on" (Server
  / My machine) left the editor: hosted crews come later, and the field
  said nothing until then. The switches of a launch (open the run page, a
  view link, yolo) sit behind the line under "Each run".
- **Stopped is not failed.** A member a stop cut off reads "not started"
  (never started, or `the run is stopped` as its error) or "stopped", in
  grey; red is kept for an error of the member's own, and a run with one
  is "failed" whether it was then stopped or not. The Runs tab's rows carry a
  note: who asks a question, what failed ("tests: exited 1"), how many a
  stop cut off ("3 never started"), who waits for Start now, else what
  changed ("+412 −58 on 5 branches").
- **The chart moved** out of the 240 px column into the Runs tab, drawn as
  plain bars (the last twelve runs; the median, how many needed an answer,
  how many failed) with an amber edge where someone had to answer; the home's
  table draws the same bars beside each crew. `nuxt-charts` no longer draws
  it (the Events page and the Wall still use it).
- **Leaving a share.** The join page's headers have Leave: the terminals go
  (their connections close with them), the card comes back with the name
  kept, and the link still joins. Asked on 2026-10-04: "in a shared view I
  only see the one screen, no way to leave the share".

### Open verification (round 8)

- By eye on the installed app: the Crews home with a run going and one that
  asks a question; a crew's page under a live run; the graph's "when done"
  labels at the new gap; the Runs tab with a failed run (a member whose
  program exits non-zero before its prompt).

### Deferred

- Hosted crews ("Runs on: my machine"), and with them the field.
- A run's name of its own (today the crew's name and the start time).
- Filtering the Runs tab (by state, by date) once a crew has hundreds.

## Round 7: the switchyard as the way sharing works, dark always, the Windows app seeing WSL (2026-10-04)

Asked after installing the first release candidate on Windows: "I want the
conductor app to start in dark mode. I want switchyard to be the default for
sharing! I want conductor to just work when I click share an agent. I want
the names as examples in the UI removed. Also what the hell does 'Runs on'";
and from the Windows app: Codex "not installed" though it is in WSL, and
Settings showing Windows paths.

### Decisions

- **Open hosts.** A switchyard with `switchyard.openHosts` registers a host
  that presents no token, under per-address limits: `openHostSessions` live
  sessions (4), `openHostRegistrationsPerMinute` (6), `openHostRelayKBps`
  (128). A wrong token is still refused; a host token marks a trusted machine
  outside the limits. The relay bound is per connection, so one address may
  relay `openHostSessions` times the bound; a quota per address is deferred.
  The public switchyard runs with open hosts on, `maxSessions` 500 and
  `maxViewersPerSession` 8 (hosted sessions take the viewer cap from the
  config now; the hub hard-coded 32).
- **The public switchyard by default.** Every server publishes to
  `https://switchyard.rslabs.net` (`config.DefaultRendezvousServer`) unless
  `rendezvous.server` names another, `rendezvous.enabled: false`
  (`CONDUCTOR_RENDEZVOUS=0`) turns it off, or the server is a switchyard. The
  token is optional. The desktop app's Settings has the switch, on by default,
  and keeps the four switchyard settings across restarts (they were dropped
  on read). Every test harness that launches sessions sets
  `CONDUCTOR_RENDEZVOUS=0` (AGENTS.md).
- **Links minted at the switchyard are the sharing machine's.** It keeps a
  record of each (dropped when the publication ends, or when a switchyard
  restart re-registered the session and lost them), lists them with the local
  ones (`remote: true`) and revokes them over the host protocol
  (`link_revoke` / `link_revoked`, `error{not_found}`).
- **One-click Share.** Opening the dialog makes a view link for two hours,
  copies it and says where it reaches: from anywhere through the switchyard
  that minted it; or why the session is not there (the server waits up to
  ten seconds for a publication still being made, and a link made meanwhile
  carries `rendezvous: {server, error}`); or what a local link reaches.
  Opening it again shows the link already made (a host may ask a switchyard
  for five links a minute). View is the one-click role: a link that lands on
  the clipboard must not hand out typing rights by accident; control is one
  click more. Crew links are made here, not at the switchyard.
- **Dark always.** The workbench opens dark whatever the OS prefers, under a
  storage key of its own; the theme button switches to light and that is
  kept.
- **No invented names** in placeholders; the launch dialog asks where the
  agent runs only when the workbench is served from another machine.
- **The Windows app and WSL** (E1, E2 of the plan): the server in WSL gets
  the person's own shell's PATH, imported through `$SHELL -ilc` as the native
  app does, so nvm's and npm's bins count; a program found under `/mnt/` is
  named and not used; the settings on Windows are WSL paths, picked inside
  the distribution.

### Open verification (round 7)

- The public switchyard with open hosts on: a `conductor host` with no token
  from a second machine registered and showed in the operator card
  (2026-10-04). Still to see: the desktop app on this PC with nothing
  configured, listed there; Share → one click → the link on a phone on mobile
  data saying "Works from anywhere"; a revoke from the dialog disconnecting
  the phone.
- The Windows installer after E1 and E2: Codex identified inside the
  distribution, `/home/<user>` paths in Settings, a session launched there.

### Deferred

- Run (crew) links through a switchyard.
- A relay quota per address rather than per connection.
- The switchyard persisting its links across restarts.
- A one-time notice in the desktop app on the upgrade that turns publishing on.

## Round 6: switchyard, invites the app opens itself, and ICE from WSL without mirrored networking (2026-10-03)

Asked the same day Round 5 landed. `docs/round6-plan.md` is the plan.

### Decisions

- **The person configures no Windows networking, ever.** Not mirrored
  networking (it changes WSL for Docker and every other tool), not
  `netsh interface portproxy`, not a `.wslconfig` edit: whatever must cross
  Hyper-V's NAT, the desktop app forwards itself. The desktop app on Windows runs a UDP
  forwarder (a NAT in user space) in front of the server in WSL, which puts
  every WebRTC connection on one UDP port (7877) and advertises the Windows
  LAN address; ICE then crosses one NAT, the router's. The installer adds
  the firewall rule when it can; Settings offers it through one elevated
  `netsh` with fixed arguments.
- **Switchyard is Conductor in a mode.** `conductor switchyard` keeps the
  signaling hub, the host route, the links, the join route, the events
  stream and the workbench, answers `403 switchyard` on everything that
  launches, and relays only when `switchyard.relay` is on. Its load is
  signaling and the relayed terminals; one small machine serves many.
- **An invite is a share link the app opens itself.** Every link reply
  carries `conductor://<host>/join/<token>`; the app registers the scheme
  and opens its own join page with `?server=`, which fetches the link from
  the server named (loopback origins answered across origins, the session
  WebSocket accepted from them) and connects there. A machine that publishes
  to a switchyard mints its links there over the host connection (`link`,
  `link_created`), so the URL and the invite are the switchyard's.
- **Round 5 stands.** A reachable server with a certificate is still the
  path for a machine with a public address; switchyard is the path for the
  rest.
- **Paste invites need no server at all.** `/paste` gathers a viewer's ICE
  candidates without trickle into a blob; the session's side answers with
  one (`POST /api/sessions/{id}/paste`, the Share dialog); the data channel
  then runs machine to machine. Most home-to-home pairs connect; carrier
  NAT and office networks do not, and there is no relay, so they take the
  switchyard. A public switchyard may bound what a host relays
  (`switchyard.relayKBps`).

- The switchyard serves no workbench (2026-10-04, from the design "Switchyard
  Pages"): `/` is a server-rendered landing page (what the server is, how a
  link looks, how a machine publishes to it, the status card: version,
  uptime, TLS and its renewal, the relay, the public address, the invite
  form; the operator's figures behind the workbench token), every workbench
  path is a 404 page pointing at the Conductor on one's own computer, and the
  app is served only for `/join/<token>` and `/paste`. The join page names
  the switchyard it was shared through. Both pages are Go templates with the
  brand tokens inlined, so they stand without the app built.
- Restarting the switchyard (2026-10-04, seen live): the publishing server
  retried resuming its old hosted session forever, refused with `4404` each
  time, until it was restarted. A host answered `4404` on a resume now
  registers afresh under a new id (tested against a fake switchyard that
  forgets); links minted before the restart are gone, since the switchyard
  keeps hosted sessions and links in memory. Persisting them is deferred.

### Open verification (round 6)

- Two desktop apps on two home networks through a switchyard on a VPS, one
  of them from WSL in NAT mode: the invite opens the app, the terminal
  connects direct (the transport badge says WebRTC) and falls back to the
  relay when a router refuses. The e2e suite proves the relay path on one
  machine; the direct path needs two networks.
- The installer's firewall rule on a per-user install (no administrator):
  the rule is left to Settings, which must add it with one prompt.
- The `conductor:` scheme registered by each installer (NSIS, dmg, deb,
  rpm, AppImage) and an invite clicked in a browser reaching a running app.

- 2026-10-04, a switchyard on a VPS (a Lightsail nano in Ohio, Ubuntu 24.04,
  a systemd service with a root-only env file, `reach.mode: manual`, the TLS
  listener on 443, Let's Encrypt's IP certificate issued in seven seconds):
  a server on a Linux machine on the home LAN published a shell session to
  it (`CONDUCTOR_RENDEZVOUS_*`, reach off, no TLS of its own); the link was
  minted at the switchyard (`remote: true`), the switchyard listed the
  session as hosted, and a browser on another machine (inside WSL, behind
  Hyper-V's NAT and the same UniFi router) joined it over **WebRTC direct**
  (69 ms) with typing reaching the shell. Both machines share one router,
  so a viewer on another network (a phone on mobile data) is still the
  proof that the path crosses two NATs; the relay fallback is not exercised
  yet. Later the same day the switchyard moved to a domain name
  (`tls.acme.domains`, `publicUrl` on the name, reach off): Let's Encrypt's
  ninety-day certificate came in seven seconds, and the publishing server,
  still dialing the address, failed its TLS handshake ("bad certificate")
  until its `rendezvous.server` was repointed at the name, which is the
  expected shape: a certificate for a name does not cover the address.

### Deferred

- The app forwards the TLS listener into WSL and maps the router from
  Windows (UPnP, PCP, NAT-PMP from the Windows side), so the public-address
  path of round 5 works from WSL with nothing configured by hand. Until
  then the phone test of a public link is done from a machine that is not
  WSL.
- Switchyard rosters and invite lifetimes beyond the share store's: who is
  online, invites that name a person.
- A relay quota per host on a switchyard, for a public one.

## Round 5: sharing without configuration, a test tier, agent identity, desktop, the skill and the crew graph (planned 2026-10-03)

Asked by the user after running round 4 on 2026-10-03; decided the same day.
One plan, `docs/round5-plan.md`; the design of the crew graph and the charts
is handed to Claude Design through `docs/design/crews-graph-brief.md` on the
branch `design/crews-graph`.

### Decisions

- **STUN and ICE connect the terminal channel, not the link.** STUN gives a
  browser or a `conductor host` its public address and port so ICE can form a
  direct UDP path once the join page has loaded from the server; it carries
  no data and makes no HTTP page reachable. What makes a link open from
  outside is a reachable server: a forwarded port, a public address or a
  proxy. Round 5 makes the first automatic.
- **Reach is automatic everywhere, for the TLS port only.** `reach.mode` is
  `auto` by default for `conductor serve` and the desktop app: the server
  asks STUN for its public address and maps external 443 to its TLS listener
  through UPnP IGD, then PCP, then NAT-PMP, all written against the RFCs with
  the standard library and tested against loopback fakes; it renews the lease,
  re-checks the address and unmaps on shutdown. The plain listener is never
  mapped: plain http never leaves the network. `off` turns it off, `manual`
  only discovers the address for a port the person forwarded. `GET /api/reach`
  reports the state and the Share dialog says what a link reaches.
- **No public link until the certificate is ready.** While the server has no
  certificate for the public address, links keep the address the request came
  through and the Share dialog says why. Precedence of a link's base: an
  explicit non-local `publicUrl`; the discovered `https://<ip>` while mapped
  and the certificate is ready; the request's address with forwarded headers
  honoured; `publicUrl`. The notify URL agents get stays local.
- **TLS through lego, ACME only.** `tls.acme` obtains a certificate from
  Let's Encrypt through `go-acme/lego` as a library: for the STUN-discovered IP
  address (an IP-address certificate, `shortlived` profile, six days, renewed
  at two thirds of its life, `tls-alpn-01` on the mapped 443) or for configured
  domains (`dns-01` through lego's providers, `http-01` on a mapped 80, or
  `tls-alpn-01`). `tls.certFile`/`keyFile` serve a pair of the person's own.
  Nothing self-signed, no fingerprint pinning, no redirect from plain http and
  no HSTS (the plain listener serves the desktop window and local hosts). The
  TLS listener is its own (`:8443` by default, mapped from 443). The
  certificate manager serves nothing before the first issuance, so a handshake
  fails cleanly rather than trusting a made-up certificate. Tested against
  Let's Encrypt's Pebble in CI.
- **Mapped is not verified.** The server's self-check through the public URL
  reports "ok" or "unverified", never "unreachable": a router that does not
  hairpin refuses the server's own request while a phone on mobile data gets
  through. A person on another network is the proof, and the open
  verification below asks for it.
- **Local sessions publish to a rendezvous.** A server with `rendezvous`
  configured publishes its sessions to a public Conductor through the host
  protocol, one session per host connection as today; links for them are
  minted on the rendezvous. TURN credential minting (coturn's static secret)
  is optional and last.
- **Every by-hand check gets a tier.** CI runs the stub-agent suites; a
  weekly nightly runs the built-in sites on the network and the recipe flags
  against freshly installed CLIs without accounts; a live tier with test
  accounts (Claude Code and Codex first, keys in a reviewed GitHub
  environment, cheapest models, one-word prompts) runs on a schedule or by
  hand. The stub agent grows an identity, a transcript per session id, a
  trust dialog and Codex- and Claude-shaped reports so the trust hold, resume,
  the title thread, the tiles and the keyboard are tested in CI. Antigravity
  and DeepSeek Harness stay by hand (no automatable sign-in).
- **Agents are identified by their version output.** Each adapter carries a
  probe (arguments and a pattern); the server runs it on the resolved program
  with a timeout and bounds, caches the result and reports `identity` on the
  catalog. The Agents page shows the name and version, names an impostor
  (`goose` on the PATH that is the Go migrations tool), and a crew with a
  misidentified member is refused at launch; a pending or failed probe refuses
  nothing. A saved agent without an adapter is not probed.
- **A stopped run resumes both ways.** Resume on an ended member of a stopped
  run reopens the run in place once its stop has completed; "Resume run"
  creates a new run of the crew in which every member with a resumable agent
  session continues its conversation in its kept worktree and branch, and the
  others start afresh under their start rules.
- **Desktop: Electron, with Windows through WSL2.** A `desktop/` shell packages
  the Go server (macOS dmg and zip, Linux deb, rpm and AppImage) and talks to it
  through a one-line JSON handshake on stdout and a stdin that ends the server
  when the shell dies; the workbench token lives in memory for the shell's run.
  Windows gets no native server: the installer bundles the Linux binary and runs
  it inside the user's WSL2 distribution, with a first-run screen that explains
  `wsl --install`. (Round 6 settled how WSL reaches out: the app forwards,
  the person configures no Windows networking.)
- **The skill's commands are also MCP tools.** `conductor mcp` serves
  report, set_state, ask, form_crew, add_member, run_status and link on
  stdio (JSON-RPC 2.0, one message a line); a server's launch registers it
  with Claude Code (`--mcp-config`) and Codex (`-c mcp_servers.conductor.…`),
  both verified against the CLIs on this machine; `agents.mcp` turns it off;
  `conductor host` does not register it yet.
- **The skill reaches every agent, and an agent can form a crew around its
  own session.** The skill is installed at launch for every adapter with a
  skills directory (a file carrying the `conductor:skill` marker, never the
  person's own), and every session gets `CONDUCTOR_SKILL`. A session's agent
  token gains a scoped grant: form a crew around its own session (its cwd and
  yolo, never more; the session becomes the first member), add a member to its
  run, read its run, mint a view-only link to itself; two crews per session
  per hour. An agent never holds the workbench token. `conductor crew create|add|
  status|link` wrap the routes; `--open` shows a toast with the run in the
  workbench rather than navigating anyone. A needs-input report may carry
  choices that viewers answer with one click.
- **A crew graph and charts, with run records.** The run page and the crew
  editor get a graph (Vue Flow): solid edges for "after X idle", dashed edges
  for handoffs as they happen, one parent per node enforced while editing. A
  run timeline draws each member's bar with its waits and handoffs. Runs that
  end are recorded in `dataDir/runs/` (never terminal output) so the Crews
  page charts a crew's last runs (Nuxt Charts); the Events page charts activity
  per minute; the Wall shows sessions by attention state. No chart with fewer
  than two points.

### Open verification (round 5)

- The crew graph, the run timeline and the charts against a design: there
  was no hand-off (the brief on `design/crews-graph` was never run through
  the design tool), so the owner updates the mockups by hand from the built
  pages. A rendered review on 2026-10-04 (every page at 1440 and 390, both
  themes) found: an "after" edge and a handoff edge between the same members
  share one path, so the count badge covers the label; the Crews card chart
  is cramped in the sidebar column; a stopped run lists never-started members
  as errors; the Events chart is empty on every page load (feed only); the
  Wall donut reads as a spinner; the phone run header truncates its title.
- The skills directories: Cursor, Copilot, OpenCode, oh-my-pi and Amp read
  `~/.agents/skills` and the Antigravity CLI `~/.gemini/antigravity-cli/skills`
  by their vendors' docs (read 2026-10-03); DeepSeek Harness reads
  `~/.agents/skills` by third-party guides only. Each agent, launched from
  Conductor, should list the `conductor` skill (`/skills` or its equivalent).
- A link `https://<public ip>/join/<token>` opened from a phone on mobile
  data: the padlock, the join page, the terminal live. The server's self-check
  cannot prove it. 2026-10-04, from a Linux machine on a home LAN (not WSL),
  `CONDUCTOR_TLS_ACME=1` with reach auto and no email: STUN found the public
  address, no gateway mapped the port (below), so no certificate was ordered,
  the TLS listener refused handshakes with "no certificate yet" and links
  stayed on the request address, as designed. The LAN path passed: a control
  link to a shell session, opened from another machine, joined over WebSocket
  and typing reached the shell. Open until a router maps or forwards 443.
- Router models beyond the loopback fakes: which answered UPnP IGD, PCP or
  NAT-PMP, and which refused (list them here with the firmware).
  - UniFi OS gateway (Ubiquiti), 2026-10-04: refused all three. No SSDP
    answer from an Internet Gateway Device (a raw M-SEARCH from the same
    machine saw only a Roku and a media device), PCP and NAT-PMP refused on
    5351. UPnP is off by default on UniFi and it has no NAT-PMP; turning
    UPnP on, or a manual forward with `reach.mode: manual`, is the way.
- IP-address certificates from the real Let's Encrypt: the rate limits and
  the renewal every few days over a week. Pebble stands in for it in CI
  (`make test-pebble`): an IP identifier over `tls-alpn-01` with a renewal,
  and a name over `tls-alpn-01` and `http-01`, all validated by Pebble
  against the listener, pass on 2026-10-03. 2026-10-04: issued for real on
  a VPS running `conductor switchyard` with `reach.mode: manual` and the
  TLS listener on 443: the order, the `tls-alpn-01` validation and the
  six-day certificate (renewal at two thirds) took seven seconds, with no
  account email. The first order went to the host's IPv6 address, which
  STUN over a plain "udp" dial returned on a dual-stack host; the reach
  lookups are IPv4 only now (`udp4`), and the second order was for the
  static IPv4 address, which `https://<ip>/api/health` verifies with the
  system trust store. The renewal over a week is still to be watched.
- The TLS listener offers HTTP/1.1 only. Whether the WebSocket routes work
  over HTTP/2 (RFC 8441 extended CONNECT in Go's server and the browsers)
  decides whether `h2` can be offered later.
- The first nightly run: every **verify** row of the plan closed with the real
  CLI's help and version output.
- The Windows installer on a machine with WSL2: the first-run screen without
  WSL, the server in the distribution, the agents found, a link from Settings.
  2026-10-04: the release workflow ran for the first time on the tag
  `v0.6.0-rc.1` and, after four rounds of runner-only fixes (Playwright's
  dependencies on Ubuntu 24.04, POSIX-only desktop tests on Windows, no
  `make` on the Windows runner, git 2.55's reading of info/exclude, empty
  signing secrets read as certificate paths, the package version from the
  tag), built every package on the three runners into a draft prerelease:
  dmg and zip for macOS, deb, rpm and AppImage for Linux, the NSIS
  installer for Windows, all unsigned. The installer's walk-through on this
  machine is still to do.
### Deferred (round 5)

- A multi-session host protocol (one `conductor host` serving several
  sessions); round 5 publishes one session per connection.
- TURN credential minting, unless a network needs it.
- `conductor mcp`, the same actions as an MCP server for agents without a
  skills directory.
- Signing and notarisation of the desktop builds until the secrets exist; the
  builds ship unsigned.
