# Design tools for agents

Two outside tools can help an agent, or a person, with Conductor's
screens: uxui-principles, an MCP server of design principles and
checklists, and Impeccable, a design skill for coding agents. Neither is
part of Conductor or ships with it, and neither is needed: an agent
without them works from the design system alone.

An agent never installs either tool, adds a server to any configuration,
or turns a hook on because of this page: those are a person's own
choices, made by the person.

What binds is the repository's own: [system.md](system.md) (the rules
every screen follows, with the checks that hold them),
[brand.md](brand.md) (the brand) and [PRODUCT.md](../../PRODUCT.md) (who
the screens are for). Where a tool's advice differs from them, they win.
[DESIGN.md](../../DESIGN.md) at the root carries the same design system in
the form outside tools read; it is generated (`make design-md`), never
edited by hand.

## uxui-principles

`@uxuiprinciples/mcp-server`, from uxuiprinciples.com: a local MCP server
whose data ships inside its package. The package is under the MIT
licence; its publisher's terms of use cover the principles' text (below).
It names principles by id (`cognitive-load`, `hicks-law`,
`progressive-disclosure` and some two hundred more), flows with one
checklist for before design and one for before shipping (settings,
navigation, forms, data tables, search, notifications, empty states and
others), and smells: the common ways a screen goes wrong.

The tools worth calling here:

- `get_flow` and `get_flow_principles` for the flow a change touches
  (`settings` for a card of the settings page, `navigation` for the
  sidebar);
- `generate_checklist` with `phase` set to `preflight` before designing,
  and to `postflight` before finishing;
- `get_principle` and `search_principles` for a principle, by its id or by
  a word;
- `detect_smell` and `suggest_fix` for a screen described in words.

### What it sends

Nothing of a screen, as the repository starts it. The first start runs
`npx -y`, which fetches the package and about ninety packages it depends
on (the MCP SDK and what that brings: an HTTP framework, a process
spawner, a JSON Schema validator and others) from npm, each by the range
it declares, with no lockfile; npx can ask npm's registry again at a
later start. The server itself reads its own data and makes no network
call. Two tools, `audit_interface` and `check_requirements`, need the
publisher's paid key and send the screen's description to its service.
The repository's entry sets that key, `UXUI_API_KEY`, to nothing, so they
refuse, even for a person who has the key in their environment.

`.mcp.json` pins the server at 1.3.0. Only the server is pinned; what it
depends on comes by range, so a first run on a new machine can get newer
versions of any of them. It is not a dependency of Conductor: nothing of
it ships, and it is not in `THIRD_PARTY_NOTICES`. A new version is a pull
request that changes `.mcp.json`, this file and `docs/dependencies.md`
together.

### Its text stays its own

Its publisher's terms do not allow its text to be copied into an
open-source repository, or summaries or cheat sheets of it to be made. So
anything in this repository (a doc, a code comment, an issue, a pull
request's body, a commit message) names a principle, a checklist or a
smell by its id and says in its own words what follows for the screen at
hand. It never pastes the tool's sentences.

### In Claude Code

`.mcp.json` at the root names the server `uxui-principles`. In an
interactive session Claude Code asks each person once before it starts a
server that a repository names: approve it, or reject it. The choice is
kept in `.claude/settings.local.json`, which git ignores here;
`claude mcp reset-project-choices` asks again. A session that cannot ask
(`claude -p`, the Agent SDK) starts it without asking. To keep it off,
reject it, turn it off in `/mcp`, or start a session with
`--strict-mcp-config`. Subagents get its tools with the rest.

Claude Code remembers an approval by the server's name, not by what it
runs: a branch that changes the command under the same name starts the
new command without asking. Read the diff of `.mcp.json` before you start
an agent on someone else's branch; a change to it is reviewed as a
boundary (`AGENTS.md`).

### In other agents

Each starts the same command, `npx -y @uxuiprinciples/mcp-server@1.3.0`,
with `UXUI_API_KEY` set to nothing. It needs Node 18 or later.

- VS Code with Copilot reads `.mcp.json` at the workspace root as it is.
- Cursor: add the `uxui-principles` entry of `.mcp.json` under
  `mcpServers` in `~/.cursor/mcp.json`, or in `.cursor/mcp.json` in your
  checkout, which git ignores here.
- Codex, in `~/.codex/config.toml` (or `.codex/config.toml` in your
  checkout, which git ignores here):

```toml
[mcp_servers.uxui-principles]
command = "npx"
args = ["-y", "@uxuiprinciples/mcp-server@1.3.0"]
startup_timeout_sec = 60

[mcp_servers.uxui-principles.env]
UXUI_API_KEY = ""
```

The first start fetches the package, which can take longer than Codex's
default wait of 10 seconds; hence the 60. To see that it started, ask your
agent to list the server's tools: `get_flow` and `generate_checklist` are
among them (in Codex, `codex mcp list` names the server).

A person who wants the paid tools adds a server of their own, under
another name, at their user level, and keeps what it sends in mind.

## Impeccable

Impeccable (github.com/pbakaus/impeccable, Apache-2.0) is a skill for
coding agents: design guidance in markdown (critique, audit, layout, type,
and an Operate mode for app screens such as Conductor's), an optional hook
that runs its detector after each edit, and an engine, a binary that its
commands download from the project's GitHub releases on first use. It
reads a project's `PRODUCT.md` and `DESIGN.md`. It is not an MCP server of
design guidance: its MCP mode passes requests on to its publisher's
account service and needs an account, so `.mcp.json` does not name it.

The repository holds no copy of it and installs nothing of it. It holds
what the skill reads, for a person who adds it: `PRODUCT.md`, `DESIGN.md`,
and `.impeccable/config.json`. That file turns its telemetry off for
everyone in this repository and its daily update check off when it runs
from the root (`IMPECCABLE_NO_UPDATE_CHECK=1` stops the check anywhere),
keeps its hook off until a person turns it on, and allows Inter (its
detector calls Inter an overused face; here it is the brand's). Everything
else it writes under `.impeccable/` stays out of git.

### What it sends

- The engine, once a version, from its GitHub releases, checked against a
  list of hashes that ships beside it.
- A request to impeccable.style when it starts a new screen or visual
  direction; with telemetry off it is still sent, marked not to be
  recorded.
- Requests to impeccable.style from its install, update and help
  commands, and for the pictures on its pages of choices.
- With telemetry on, which this repository turns off: the option a person
  chose after a round of choices.
- With an account, which nothing here asks for: reports, design health
  and files sent to the account.
- When its text asks for them: fonts from Google Fonts, images from
  OpenAI's API (only with an OpenAI key in the environment), and a page
  loaded in the installed Chrome or Edge.

### Adding it for yourself

Install it for your own user, never into this checkout:
`npx impeccable@4.11.1 install --scope=global --providers=codex` (or
`claude`, `cursor`: the agents you use; it needs Node 22.18 or later).
Open it in your agent with `$impeccable` in Codex, `/impeccable` in
Claude Code and Cursor. Skip the README's `init` step: `PRODUCT.md` is
here already. Then, in this repository:

- Its hook runs the engine after every edit, outside your agent's
  approval, with your environment and your network. It stays off in this
  repository; to turn it on for yourself, put
  `{"hook": {"enabled": true}}` in `.impeccable/config.local.json`, which
  git ignores.
- When it tells you which files to commit, commit none of them. Here only
  a reviewed change to `.impeccable/config.json` is committed; leave out a
  `buildPath` or a live-mode setting it writes there.
- Leave off the account sign-in, and the MCP server it offers after it.
- Before you start its live mode, set `IMPECCABLE_LIVE_COPY_AGENT=chat`
  (or `off`): otherwise its Apply can start Codex or Claude Code with
  their permission checks off. Never commit the plugin or the policy
  change it adds to the app for live mode.
- Its commands tell an agent to do some things without asking: fix its own
  findings, start its subagents, write ignores into the shared config.
  Here an agent asks first, and a change to `.impeccable/config.json` is a
  pull request like any other.
- If it offers to write `DESIGN.md`, decline: the file is generated, so
  change its sources and run `make design-md`. `PRODUCT.md` changes as any
  doc does, in a pull request.

### Where its advice and ours differ

Its guidance is general, and Conductor's screens are an app that people
use all day. Where they differ, ours holds:

- It flags Inter and asks for a face fetched and served from the project;
  Inter is the brand's face, and no remote font service is needed
  ([brand.md](brand.md)).
- Some of its commands fold rare or advanced things away and keep a single
  action in front; here how often a thing is used decides whether it
  folds, a settings page folds nothing, and Yolo stays in view
  ([What stays in view](system.md#what-stays-in-view)).
- It asks for bolder colour, motion and a memorable touch; Conductor's
  screens are calm and flat, forest marks the one main action, and amber
  and green are for states.
- It may propose new visual directions; Conductor has a design system, and
  a change refines it from inside.
- It checks at 1440 and 390 pixels wide; check the daily window of about
  2000 x 810 too ([Who the screens are for](system.md#who-the-screens-are-for)).
- It picks light or dark by the kind of product; the app opens dark, by
  decision.
- Its type-scale rule wants larger steps than a dense app draws; the app's
  sizes are Tailwind's.

Copying its files into this repository would need its licence, its notices
and a mark on each file changed, and would bring instructions that pull
against the rules above, so it is not copied.
