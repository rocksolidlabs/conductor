# Design brief: the crew graph, the run timeline and charts

This brief is for a design pass in Claude Design on three additions to the
workbench. It holds the prompt to give the design tool, the data the screens
draw from, the brand tokens, and the pages that exist today. The implementation
plan behind it (Round 5, Phase D) will build the graph with Vue Flow, the charts
with Nuxt Charts, and the timeline as a bespoke SVG component.

How to use it: link this repository (branch `design/crews-graph`) in the Claude
Design project that holds the Conductor Mockups, start from screen 1c "runs
(orchestration graph)", and paste the prompt below. Hand the result back with
`/design` in Claude Code against the same branch.

## The prompt

```text
You are designing three additions to Conductor, a browser workbench that runs coding agents
(Claude Code, Codex, Goose, Aider, …) in terminals and lets a saved team of them, a "crew",
work one goal. The repo is linked; read web/app/app.config.ts, web/app/assets/css/main.css,
docs/design/brand.md and docs/design/crews-graph-brief.md before drawing. Start from screen
1c "runs (orchestration graph)" of the Conductor Mockups file in this project and bring it
to what the code actually has now.

Facts to design against
- A crew has up to 12 members. Each member has ONE start rule: "immediately", "after <member>
  is idle" (it starts when that member first reports done after its prompt), or "manual".
  So the dependency picture is a forest: at most one parent per node, no cycles, several roots.
- While a run is live, members send each other "handoffs" (a message typed into a member that
  is already running). Handoffs never start anyone. They are events with from, to, time, text.
- A member in a run is pending, starting, running or ended, and may be "needs input" (amber).
  It has an agent (icon), a branch, a worktree, a diff (+added −removed lines), startedAt,
  endedAt, and an error text when it failed.
- A run is running, needs_input, stopped or finished; it has a goal, a cwd, a log of up to
  200 timestamped lines, and a count of members needing input.
- Brand: roles primary=forest (green), secondary=terracotta, neutral=zinc, warning=signal
  (amber), success=active (green), info=harbor (blue), error=brick. Status marks and words: as the status
  section of [the design system](system.md#status) says; terracotta is used ONLY on the brand
  mark, never as a status. One system-wide dark/light theme. Nuxt UI 4 components only;
  Lucide icons; JetBrains Mono for anything terminal-like.
- The graph will be built with Vue Flow (nodes are Vue components, edges are SVG paths,
  pan/zoom, fit-to-view). Charts will be built with Nuxt Charts (area, bar, line, donut);
  the run timeline is a bespoke SVG component. Design what those can render.

Deliverables (desktop 1440 wide and phone 390 wide, dark and light, every state)
1. Crew graph on the run page (/runs/<id>): a "Graph" view beside today's tile grid. Nodes =
   members (agent icon, name, status dot, needs-input badge, branch, +/− diff, "Start now" on
   manual/pending, Resume on ended). Solid edges = "after X idle" with a small label; dashed
   animated edges = handoffs seen during this run, with a count badge and a hover card
   (last message, time). Roots on the left, layered left-to-right. Selecting a node opens
   that member's terminal in a side panel or jumps to its tile (pick one, justify). Toolbar:
   fit, direction (LR/TB), legend, "show handoffs" toggle. Empty state for a crew with one
   member. Phone: the graph collapses to an ordered list with indent = depth and the same
   badges.
2. Graph mode in the crew editor (/crews/<id>): the same nodes without run state; dragging
   from node A to node B sets "B starts after A is idle"; a node may have at most one incoming
   edge (show what happens when a second is attempted); deleting an edge sets "immediately";
   a node menu switches to "manual". Keep the existing members table as the other mode; show
   the mode switch. Show validation (a cycle cannot be drawn; a member removed from a crew
   resets its dependents to "immediately").
3. Run timeline (a tab beside Graph): one row per member, bars from startedAt to endedAt or
   now, amber segments where the member was waiting for input, markers for handoffs
   (from-row to to-row), the run's "stopped" line. A hover reads the exact times.
4. Small charts, only where they say something: on the Crews page each crew card shows the
   last 20 runs as duration bars coloured by final state and a needs-input count; the Events
   page gets an activity-per-minute stacked area (attention, input, handoff, tool, error)
   over the last hour; the Wall header gets a donut of sessions by attention state. Say no
   to anything that would be decoration.
Give component specs (names, props, states), the colour mapping to the roles above, spacing
on the 4 px grid, and the interaction notes a developer needs. Do not restyle the rest of
the app.
```

## The data the screens draw from

These are the shapes the server reports today (`web/app/composables/useSessions.ts`,
`internal/crew/crew.go`, `internal/session/activity.go`). The graph and the
timeline are built from them; nothing needs inventing on the client.

### A crew (saved) and its members

```ts
interface CrewInfo {
  id: string
  name: string
  goal: string
  cwd: string
  where: 'server' | 'host'
  isolation: 'none' | 'worktree'      // worktree: one git worktree per member
  openAfterLaunch: boolean
  viewLinkTtlSeconds?: number
  yolo?: boolean
  members: Member[]                   // at most 12
  createdAt: string
  updatedAt: string
}

interface Member {
  name: string                        // ^[a-z0-9][a-z0-9._-]{0,39}$ (becomes a branch name)
  agentId: string                     // catalog id: claude, codex, goose, aider, …
  prompt: string                      // typed once the member is ready; $GOAL expands
  args?: string[]
  start: { when: 'immediately' | 'after' | 'manual'; member?: string }   // member only with after
}
```

Start rules: `immediately` starts at launch; `after` starts once the named member
first reports done after its prompt (the UI says "after X idle"); `manual` waits
for a Start now. A member waits for at most one other member, self-reference and
cycles are refused by the server, and removing a member resets its dependents to
`immediately`. Renaming a member renames the references to it.

### A run (a launch of a crew) and its members

```ts
interface RunInfo {
  id: string                          // <crew id>-<8 hex>
  crewId: string
  name: string
  goal: string
  cwd: string
  isolation: 'none' | 'worktree'
  startedAt: string
  stoppedAt?: string
  members: RunMember[]
  log: ActivityEntry[]                // oldest first, at most 200
  state: 'running' | 'needs_input' | 'stopped' | 'finished'
  needsInput: number                  // members waiting on a prompt
  yolo: boolean
}

interface RunMember {
  name: string
  agentId: string
  start: { when: 'immediately' | 'after' | 'manual'; member?: string }
  sessionId?: string                  // once started
  branch?: string                     // crew/<run>/<member>, with worktree isolation
  worktree?: string
  status: 'pending' | 'starting' | 'running' | 'ended'
  startedAt?: string
  endedAt?: string
  error?: string                      // why it ended before it ran
  needsInput?: boolean                // its session waits on a prompt
  agentSession?: { id: string; resumable: boolean; source: string }
  diff?: { added: number; removed: number }   // lines against the commit it began from
}
```

Runs live in the server's memory today (a restart forgets them). Phase D adds
run records on disk so the Crews page can chart the last runs of a crew.

### Activity entries (the run log, a session's activity, the Events feed)

```ts
interface ActivityEntry {
  at: string
  type: 'attention' | 'input' | 'join' | 'leave' | 'link' | 'status'
      | 'progress' | 'artifact' | 'handoff' | 'tool_use' | 'tool_denied' | 'error'
  by?: string                         // subscriber id
  byName?: string
  message?: string                    // ≤ 500 chars
  url?: string                        // artifact link
  to?: string                         // handoff target member
  tool?: string
}
```

A handoff is an `handoff` entry with `to`; the run log notes "handoff delivered
from a to b" when it is typed into the target. Attention changes of a session
arrive as `attention` entries with a state (`needs_input`, `working`, `done`,
`clear`) and a source (`bell`, `osc`, `hook`, `api`, `trust`, `resumed`).
Timeline segments come from those entries: a member "waits for input" from a
`needs_input` until the next `input`, `working` or `done`.

### Sessions (the Wall and the Events page)

A session has `createdAt`, `endedAt`, `exitCode`, `viewers` (a count),
`attention { state, since, source, kind }`, `lastAnswer` and `status`. The
client keeps one live store of them (`useAttention`) and a feed of the last 500
activity entries across sessions (`useEvents`).

## Brand tokens

Roles in `web/app/app.config.ts`: primary `forest`, secondary `terracotta`,
neutral `zinc`, warning `signal`, success `active`, info `harbor`, error `brick`.
The scales live in `web/app/assets/css/main.css` under `@theme` (50–950 each).
Anchors:

| Scale | 500 | Use |
|---|---|---|
| forest | `#4a6a5a` | primary actions, selected states |
| terracotta | `#d26b3f` | the brand mark only |
| signal | `#c98a1b` | needs input (amber) |
| active | `#3f8f5f` | running (green) |
| harbor | `#245d85` | info |
| brick | `#a1332f` | errors |
| zinc | Tailwind zinc | idle, ended, chrome |

The junction mark is artwork, never a status light. One system-wide dark/light
theme. Type: Nuxt UI defaults for the chrome, JetBrains Mono for anything that
mirrors a terminal (names of branches, diffs, session ids).

## What exists today (pages to compare against)

- `/crews/[id]`: a crew list on the left (status badge, member agent icons, cwd,
  last run) with `CrewRuns` rows under each crew (state badge, yolo badge, Open,
  Stop, member avatars with status dots); the `CrewEditor` on the right with a
  `CrewMembersTable` (one row per member: name, agent, prompt, args, a Starts
  select with Immediately / After X idle / Manual).
- `/runs/[run]`: an auto-fit grid of session tiles, one per member in run
  order; pending members are dashed placeholders ("starts once X is idle", Start
  now, Resume); tile footers show branch and diff; below the grid a text feed
  (`CrewFeed`) and the broadcast bar; a header (`CrewRunHeader`).
- `/events`: the event feed and routing (which events reach the queue).
- The Wall (`/wall`): tiles grouped by attention, a queue of sessions that need
  an answer.

Nothing draws a relationship between members today; the only wording is the
Starts select and the pending tile's label.

## What the implementation will use

- Graph: `@vue-flow/core` with custom node and edge components; layout by depth
  in the forest (column = depth, row = crew order), no external layout engine
  for ≤ 12 nodes; `@vue-flow/background` and `@vue-flow/controls` for the canvas
  chrome; pan, zoom, fit; the editor mode uses Vue Flow's connection validation
  to refuse a second parent, a self edge and a cycle.
- Timeline: a bespoke SVG component (rows, bars, amber wait segments, handoff
  markers, the stopped line), because it must follow live updates and the
  attention entries exactly.
- Charts: Nuxt Charts (`BarChart`, `AreaChart`, `DonutChart`), colours bound to
  the role scales through CSS variables, no chart drawn with fewer than two
  data points.
- Everything else: Nuxt UI 4 (`UCard`, `UBadge`, `UButton`, `UTabs`, `UTooltip`,
  `UPopover`), Lucide icons.
