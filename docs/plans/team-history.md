# Team history and skills: sessions found across a workspace's machines, and skills and workflow templates shared by version (Program item TS1)

Two things a team loses today because every Conductor server is its own
island. The first is **what was already done**: HS1 (`docs/plans/history.md`)
gives each machine a durable, searchable record of its own sessions, runs
and receipts, but a teammate on another machine, about to chase the same
failing check, cannot learn that someone already fixed it on theirs. The
second is **how the team works**: the instructions an agent follows (a
skill, a `SKILL.md` with its scripts) and the shape of a crew that worked
(a saved crew with its tasks and checks) are files on one person's
machine, copied by hand, changed by hand, and never known to be the same
or different on two machines.

This plan adds both, as Program item TS1 (the Program, section 4.5):

- **Team history.** With a workspace's sync registered (Program item CS1),
  each server sends a **history card** for each of its sessions and
  managed runs: who and where, which agent, which project and ticket,
  which repository and commits, which tasks, attempts and checks with
  their outcomes, and whether the thing can still be opened. Never a word
  the session printed, a line anyone typed, a question's answer, a path or
  an argument, and, until PR-9's titles, no name a person or an agent
  chose (a crew's, a task's, a check's, a host's). A teammate's server receives the cards of the projects the
  workspace shares and finds them in its own History (`~` in the palette,
  the History page), beside its own records and marked with the machine
  and the person that hold them. Opening the session itself stays the
  holding machine's business, under that machine's own access rules.
- **Shared skills and workflow templates.** A **skill** (a folder with a
  `SKILL.md` and its files) and a **workflow template** (a saved crew made
  portable) become **versions**: immutable, named by the SHA-256 of their
  files, with an owner and a publisher. A machine keeps the versions it
  has, installs one deliberately, shows what it changed by hand since, and
  is offered a newer version with the difference from the installed one.
  Nothing installs or updates by itself, and a skill never changes under a
  running session. The free build gets all of this for skills and
  templates it imports from a folder; a workspace's sync adds the versions
  its members publish. There is no marketplace: a version is seen only by
  the members of the one workspace it was published in.

It was written against `main` at `76d3e655` (2026-11-06) after reading
the session journal and the history index as HS1 plans and builds them
(`docs/plans/history.md`; its first rows open as pull requests #673 and
#669, branches `issue-613` and `issue-614`), the sync seam and its
adapter (`ext/continuity.go`, `internal/api/sync.go`, `docs/plans/ext.md`
C6), `GET /api/capabilities` (`internal/api/capabilities.go`, C9), the
extension registry and its contract suite (`ext`, `ext/local`,
`ext/exttest`), the open-core lint (`internal/opencore`), how the
Conductor skill reaches an agent today (`internal/agents/skill.go`,
`Adapter.SkillPath`, `InstallSkill` at every launch, `conductor skill`),
saved crews and their store (`internal/crew/crew.go`, `store.go`,
`examples.go`), project manifests (`docs/plans/project-manifests.md`,
`internal/crew/manifest.go`), the export (`internal/export`), the host
overview's plan as it stands on its pull request (`docs/plans/hosts.md`,
#677), and the compare-approaches crew's plan on its pull request
(`docs/plans/compare-crew.md`, #680), whose "template" is a crew generator
and not this item's. It is reviewed before any of its code; the PRs at the
end are cut into issues after the review, one issue, one branch `issue-N`,
one PR each (AGENTS.md).

**This plan follows the final text of the plans it builds on**: the sync
plan (Program item CS1, under review in its own repository; its public
part will be `docs/plans/sync-seams.md`), the identity plan (Program item
ID1) and the operations plan (Program item OP1), both final and
awaiting the owner. Where one of them changes before it is approved, the
approved text holds and the rows here that use it follow it. What this
item asked of the sync plan, and its answers, are listed under "What this
plan asked of the sync plan, and its answers": what the sync plan leaves
to this item is built by this plan's own rows; what it needs of identity
and operations is built by this plan's hosted-service rows (The PRs).

**What is on `main` and what is not.**

- On `main`: the sync seam (`ext.ContinuityStore`, `ext.Doc`) and C6's
  adapter, which already hands a registered sync store, after each local
  commit, an allowlist of documents (`catalog.json` masked, `crews/` and
  `runs/` documents, each run's pointer, a reference to each Dispatch
  change) and nothing else; `ext.Host`, a provider's one door into the
  app; `GET /api/capabilities`; the Conductor skill and its installer;
  saved crews, the example crews and project manifests; Dispatch with its
  projects and tickets (DS1, DS2); the export and its `leftOut` table.
  PR-1 to PR-4 build on these alone.
- Not on `main`: **HS1** (Program item HS1, In progress). Its session
  journal is PR-1 (issue #613, pull request #673), its index PR-2 (#614,
  pull request #669), the indexer PR-3 (#615), the search routes PR-4
  (#616), the History page PR-7 (#621) and Brakeman's history reads PR-11
  (#630). PR-5 waits for #615, PR-6 for #616, PR-7 for #621 and PR-9 for
  #630, each named in its row; history itself is not reshaped for this
  item.
- Not on `main`: **CS1** (Program item CS1, Not started; its plan in its
  final check). Its open-source rows give what other machines hold
  (`ext.Host.PutRemote`, `DeleteRemote`, `ClearRemote`, the data
  directory's `remote/`, `GET /api/remote`), the sync status
  (`ext.SyncStatus`, `GET /api/sync`) and the check of text that copies
  captured output (`outputGuard.Check(ctx, text)`, one function over one
  string, `clean`, `output` or `pending`). PR-6 and PR-8 wait for the
  first two (its row called OS-4 in `docs/plans/hosts.md`), PR-9 for the
  third and for the owner's answer below. Its hosted rows carry the
  records between machines: the hosted-service and official-build rows
  here wait for them. The sync plan answered this item's needs ("What
  this plan asked of the sync plan"): the remote kinds `history` and `skill`, the door
  `PutSkillVersion` and the provider's builders for them are this plan's
  own rows, which the sync plan's decoders leave room for (a kind they do
  not know is skipped, never an error; a reply may gain a field).
- Not on `main`: **ID1** (identity and workspaces: people, workspaces,
  roles, devices). Nothing in the open-source rows needs it; the hosted
  rows read members, roles and devices from it as CS1 does.

So PR-1 to PR-4 (local skills and workflow templates, free and whole)
start now; PR-5 (history cards out) waits only for HS1; PR-6 to PR-8 wait
for CS1's remote records (PR-8 also for HS2's PR-1, the machine key);
PR-9 waits for the owner's answer on what counts as terminal output
(below) and for HS1's PR-11 (#630); PR-10 to PR-13 wait for CS1's and
ID1's hosted rows and for an open-source tag holding PR-9.

**The open question this plan builds on, and how it is ordered around
it.** The sync plan holds an open question for the owner: what in a
stored record counts as terminal output. Its default, which this plan
follows: terminal output is what Conductor captured (a session's screen,
scrollback and ring, its activity log's words, a check's log, a history
record's words); none of it is ever sent, and any free text that copies a
long stretch of captured output is kept back as if it were output. The
alternative: free text an agent wrote is itself treated as terminal
output, since an agent writes from what its session printed. Under
either, nothing this plan sends before PR-9 depends on the answer, by one
invariant (I1, "What leaves a machine, field by field"): **before PR-9,
no byte of a card was chosen by a person or an agent.** Every string a
card holds is one Conductor made (a random id, a keyed reference, an
ordinal, a time, a count, a full commit id, a SHA-256 digest) or a member
of a closed set compiled into the binary (a session kind, an adapter, a
state), or a Dispatch key the sync plan already sends under its own
table, and only on a card of a project the workspace shares. A crew's id
(made from its name), a run's id (which begins with it), a task's and a
check's id, a member's name, a host's name and a catalog agent's name are
words, so they leave only as references or ordinals until PR-9. The
skills of PR-1 to PR-8 are sent nowhere. PR-9 adds the cards' titles and
publishing, written for the answer given; "Owner decisions", 2, says what
changes under the alternative.

**Names.** The railroad names keep their meaning (the Program, section
1.2): Conductor is the application, Yardmaster the agent harness that
runs a crew's tasks, Dispatch the ticketing, whose items are tickets,
Brakeman the sidekick. A **workspace** is the paid tenant (members,
roles, entitlements, decision D5); an attempt's checkout is a worktree,
never a workspace. "History" is HS1's word for the record and its search
and keeps that meaning; a **history card** is this plan's word for the
part of one history record or one run that leaves a machine. A **skill**
keeps the meaning the repository gives it (a `SKILL.md` an agent reads
from its skills directory, `internal/agents/skill.go`); the **Conductor
skill** is the one Conductor writes. A **workflow template** is a saved
crew without what ties it to one machine; it is not the compare crew's
generator (`docs/plans/compare-crew.md`, `POST
/api/crews/templates/compare`), nor a project manifest, which lives in
the repository (`conductor.manifest.json`) and is shared by git. A
**version** is one immutable set of a skill's or a template's files. The
**sync service** is CS1's hosted service; this plan names no other.
"Scheduling" stays the engine's word for what it does to a run's tasks;
nothing here schedules.

**Who owns what where this item meets others.**

| Shared piece | Owner | This plan |
|---|---|---|
| The session journal, its record and its routes; the history index, its kinds table, its search and `Similar`; the palette's `~` and the History page | HS1, `docs/plans/history.md` | reads a record's head through a closed table into a card (PR-5); adds the journal's write and forget hooks and the per-record share choice (PR-5); adds the kinds of other machines' cards, the index's schema version 2 with `repo_key` and the reserved `origin` filled, the `on:` filter and `Similar`'s workspace scope (PR-6); draws other machines' rows (PR-7). Each after the HS1 row it builds on |
| The sync seam (`ext.ContinuityStore`, `ext.Doc`), the adapter's allowlist, export and restore | C6, `docs/plans/ext.md` | registers two computed sources, `history` (PR-5) and `skills` (PR-9, the publish requests), beside the data, ledger and Dispatch documents |
| The adapter's computed sources (a store name, its keys, a build at delivery, its revision rule) | made by whichever of HS2's PR-5 (`docs/plans/hosts.md`, which adopted this at its final check) and this plan's PR-5 merges first, used by the other | the same shape in both plans: `a.computed(src)` with `paths`, `build(path) (body, deleted, err)` and the source's own revisions; `a.computedChanged(store, path)` marks a key |
| Remote records, their doors and files; the sync status | CS1 (its public plan `docs/plans/sync-seams.md`, its row OS-4) | reads them; adds the remote kinds `history` and `skill`, their decoders and bounds, and the door `PutSkillVersion` (PR-6, PR-8): the sync plan leaves these to this item's rows |
| What leaves a machine, field by field; the credential check and the check of copied output; scopes; revocation | CS1 | adds its own table for history cards and skill versions (The changes), held to CS1's rules and calling its `outputGuard.Check` over each title and each published file (the sync plan, Owner decisions 19: a record stays; its card may leave under this item's table) |
| When the page shows other machines, and the one line when it cannot (`remoteShown()` in `internal/api`, `utils/remoteShown.ts`) | HS2, `docs/plans/hosts.md` (its PR-5), which adopted the one function at its final check | reads it for History and Skills; made by whichever of HS2's PR-5 and this plan's PR-6 merges first, in HS2's shape, and read by the other |
| A workspace's members, roles, devices, sign-ins | ID1 | reads names and roles; adds nothing to identity |
| Lapse behaviour, export, retention of what the service stores | OP1 | the hosted rows follow its plan for stored records and lapse |
| A saved crew, its validation, its store, a run's crew as launched | Yardmaster, `internal/crew` | adds a crew's portable form and its `template` reference (PR-3); a run never changes because a crew did |
| The Conductor skill and where each agent reads skills | `internal/agents` | lists the Conductor skill read only; places shared skills beside it with the same "a file that is not ours is the person's" rule (PR-1b) |
| The compare crew's generator | CP1, `docs/plans/compare-crew.md` | untouched; a crew it made can be saved as a workflow template like any other |

## Articles it serves

The articles are the Program's (section 2.1).

1. **No premium code and no licence check.** Other machines' history and
   the workspace's skills appear when `sync` is in `GET /api/capabilities`
   and the sync status says current: asking whether a provider is
   registered and what it reports. Nothing reads a plan, a tier or an
   entitlement. Local skills and templates have no paid part.
2. **The open-source build is fully useful on its own.** A plain build
   shows exactly what HS1 gives it (this machine's history and what its
   hosts report) and gains, free and whole, versioned skills and workflow
   templates imported from a folder, installed deliberately, with their
   modifications and their differences shown.
3. **Paid features are gated by hosted dependencies.** A teammate's
   history and a published skill reach a machine only through the sync
   service; no flag, setting or build tag shows them, and a fork that set
   one would have nothing to show.
4. **Stop, inspect, revoke, export, required approvals.** History stays
   an inspect surface and adds no control over another machine. Export
   carries the skill versions this machine keeps and what is installed
   (with no operation still waiting); it leaves out the publish requests,
   the received versions and the remote records (the provider's). A member
   removed from the workspace, or a device revoked, loses every card and
   every received version on their machines (CS1's clear, which empties
   `skills/received/` with `remote/`), and what they installed, which was
   copied into their own versions at the install, stays theirs. No
   approval, answer or launch is made from any page here, and a template
   carries no yolo: a crew made from one asks for approvals unless its
   person turns that off on it.
5. **Dependencies point one way.** No new module; `ext` gains records and
   one door, standard library only; the hosted service reads the `ext`
   types at a tag.
6. **No feature build tag.** None added.
7. **The free default is `main`.** With no provider, the `history` and
   `skills` computed sources are not registered (the one question asked,
   article 1's: is a sync provider registered, `ext.Continuity()`), so no
   card is built, no repository's root commits are read, no file of the
   data directory changes for them, and no remote record is ever written;
   the pages are a plain build's. A registered provider adds rows; it
   removes nothing.
8. **An unreachable or lapsed provider answers as the free default.**
   Pages read the sync status from memory through the guard; anything but
   current hides other machines' rows behind one line, mid-session, and
   they return with the next current status with no restart. Nothing
   local is ever removed for an outage or a lapse: the history, the
   installed skills, the versions kept. Cards and publish requests are
   queued like every sync document and never waited on. The official
   build's rows carry the outage and lapse tests.
9. **`CGO_ENABLED=0`.** No cgo and no new database: the skills are files,
   the cards are built when delivered, the index is HS1's.
10. **The repository contract.** This plan before code; one issue, branch
    and PR per row; race tests per package; vitest and Playwright for the
    pages; no change to `internal/proto` (no frame or control message);
    the routes and the sync documents in `web/app/utils/protocol.ts` and
    `docs/protocol.md` together, each with its bound and test; no new
    dependency, so no `make notices`.
11. **Names.** "History" keeps HS1's meaning, "skill" the repository's;
    "workflow template" is told from the compare crew's generator and from
    a project manifest; "workspace" names the paid tenant only; nothing
    here is called graph, continuity or scheduling.
12. **Execution behind one seam.** Not touched: nothing here starts an
    attempt or a process for a run. A crew made from a template launches
    as every crew does, through the engine and `crew.Launcher`; placing a
    skill writes files and starts nothing. "Boxcar constraints" takes the
    eight one by one.

## Why and what happens

**Today.** A person who hits a failing check can ask HS1's History "did
another crew solve this" (`Similar`), but only among the runs of their
own machine. A teammate who fixed the same failure last week on their
laptop left a receipt, an attempt and a commit there, findable only by
them. And the skills that make an agent good at the team's work (a
release checklist, a migration recipe with its script) travel by chat and
copy, so two machines silently run two different versions, and an edit
someone made to try something out is never noticed.

**After this item ships, on a plain build** (no provider):

- **Skills** (`/skills`, a new page in the navigation, and **Go to
  Skills** in the palette) lists what this
  machine's agents read: the Conductor skill (built in, read only, its
  version the binary's), and every skill Conductor installed, each with
  its version, where it is placed (Claude Code's, Codex's, the shared
  `~/.agents/skills` and the Antigravity CLI's directories), and the state
  of each place: **Placed**, **Modified here** (a file changed by hand
  since; the difference shown), **Waiting for 2 sessions to end**,
  **Missing**, or **Not Conductor's** (a folder of the person's own under
  that name, which Conductor never writes over).
- **Import** takes a folder (a skill: its `SKILL.md` and files; a
  template: its `workflow.json`) and makes it a version: its files are
  shown, their sizes, which are executable, and the SHA-256 that names
  the version. **Install** asks which agents' directories to place it in,
  shows the files (or, for an update, the difference from the installed
  version, file by file, in the same diff view the Files tab uses), and
  says that its scripts run with the agent's permissions. A place a
  running Conductor session's agent reads from waits until those sessions
  end; the page names them.
- **Workflow templates**, the page's second section: **Save as workflow
  template** on a crew's page makes a version of that crew without its
  folder, its id or anything tied to the machine; **New crew from
  template** asks for a folder and makes an ordinary saved crew, which
  remembers the template version it came from. The page shows each
  template's crews and which of them were changed since.
- History is HS1's, unchanged.

**After this item ships, with a workspace's sync registered and current**
(the official build, signed in to a workspace, Program items ID1 and CS1):

- **History across the workspace.** In the palette, `~ unit failed` lists
  this machine's rows and, after them, other machines' rows of the
  projects the workspace shares, each marked **on studio · Ada** (the
  machine and the person) and **reported 14:05**. A failed check's **Find
  this elsewhere** gains **on every machine**: the same command (by the
  digest of its argument list, never the arguments) in the same
  repository (by the digest of its first commits, never a path), passed
  first: "check 1 of task 2 passed at def5678 · attempt 2 · on studio ·
  Ada · handed attempt 1's failure" (from PR-9, under the default
  reading, with the names: "unit on build #2 passed at def5678"). The
  row opens a **card page**
  (`/history/remote/<device>/<kind>/<id>`) that shows everything the card holds
  (agent, host, project and ticket, repository, commits, tasks, attempts,
  checks and outcomes, times, how it ended) and how to reach the session
  itself (below). The ticket and the commit are the links that go on: a
  shared ticket opens here, and the commit is in git.
- **Skills from the workspace.** A skill or template a member published
  shows as **Available from the workspace: release-notes v3 · owner Ada ·
  published by Ada, 2 days ago**, with its notes and its files; installed
  from the workspace, it shows **Update available: v4** with the
  difference from the installed version and, when the person changed it,
  the difference from what is on disk too. Only a later version of the
  same name in the same workspace, under the same owner, is an update: a
  version of that name beside a skill the person imported themselves, or
  under another owner, shows as **Another skill named release-notes, by
  Bob**, installed only as a replacement after all its files are shown.
  Publishing (PR-9) shows every file once more before
  it leaves and says who may see it: this workspace's members, no one
  else.
- **Unreachable or lapsed.** Other machines' rows and the workspace's
  versions are hidden behind one line ("Other machines' history is not
  shown: the sync service has not been reached since 14:02"), and the page
  is the plain build's. Nothing local is lost: this machine's history,
  its installed skills and its versions, the cards it has received (kept
  where the provider wrote them until the provider says otherwise). They
  come back with the next current status, with no restart.

**What leaves a machine, and what never does.** The heart of this item.
A history card is built from a closed table of fields (The changes, "What
leaves a machine, field by field") that holds, before PR-9, nothing a
person or an agent chose: Conductor's own random ids, keyed references
and ordinals, closed enumerations, times, counts, full commit ids and
SHA-256 digests, and the Dispatch keys of a shared project:

- **Leaves:** who (the sync service knows the sending device and its
  person; no card names them), the source (the agent's adapter, and its
  catalog id only when the binary's own catalog defines it; its version
  in a version's shape; the session's kind; whether a host ran it; the
  Runtime's `where`), the project (the Dispatch project's key and the
  ticket's key, only when the workspace shares the project) and the
  repository (the digest of its root commits), the revision (start and
  end commit ids; an attempt's start and result commits; a check's
  commit), the run's references (the run as a reference keyed by this
  server, never its id, which begins with the crew's name; tasks and
  checks by their ordinal in the crew as launched; attempt numbers; the
  check receipt a failed attempt's successor was handed, by the same
  references), each task's state and its standing result, outcomes and
  states as enumerations, times, counts, and availability (how and when
  the session ended, or that it was not observed to end). From PR-9,
  titles (the session's name a person or the launch gave, the crew's,
  the member's, the tasks' and checks' ids, the branch, a catalog agent's
  name), one line of at most 60 bytes each, each through the credential
  check and the check of copied output, under the reading the owner
  chooses.
- **Never leaves:** a session's transcript, screen, scrollback or
  output; its record's entries (the lines Conductor typed, the questions
  with their choices, how each was answered, the chat, the agent's
  reports and errors, its status words); what anyone typed into a
  terminal or a reply box; a question's answer, typed or picked; a file
  path, a folder, a repository's path or remote address; the launch's
  arguments, its variables' names and values, the agent's own session
  id; a check's argument list, log, tail or log digest; a task's
  instructions, a task's reason, a result's summary and issues, a
  review's verdict and findings, the members' messages, checkpoints and
  blockers; the OS user, the host's name, the host instance, a machine
  id; the session's working title (which the agent's terminal set, so
  output); anything of a Brakeman turn, whose cards are never made.

A card page on another machine therefore says what happened and where,
never what was said. **To open the session itself**, a person goes to the
machine that holds it, under that machine's rules: on one of their own
servers, the card page offers **Open studio**, a plain `https` address of
that server's own page for the record (no query, no fragment, no user
part, so no credential rides in it), given by the provider only when the
record came from one of the receiving person's own servers; following it
signs the person in at that server through its own identity check, as any
visit there does. It names the record's path there
(`/history/sessions/<id>`); on a teammate's server,
nothing opens it from here: the card names the person, and a live session
can be shared by its holder with an ordinary link, as today; while the
machine is off, nothing of the session can be opened at all, and the card
says when it last reported.

**Why no card can carry a question's answer.** HS1's first row was found
in review keeping a person's typed answer as a line Conductor typed, when
the answer reached the session through a provider's `Submit` or a host's
`answer_as` (pull request #673, its independent review's first finding).
Whatever the fix, a card is never made from a record's entries, its
files, its tools or its launch: it reads the head of the record through
one table, and `TestACardCarriesNothingTheRecordHeard` plants a typed
answer through both of those paths, and a marker in every other entry and
field, and finds none of them in any document the adapter hands a sync
store.

## The changes

### What leaves a machine, field by field

The one table the open-source card builder, the official build's client
and the sync service all hold to. The builder fills a card from this
table and nothing else; the card's `Valid` checks every string against
the pattern the table gives it, so a field added without a row fails its
test (`TestHistoryCardFieldsAreTheTable`); the official build's client
decodes the card into its own type field by field, and the service
decodes it again with unknown fields refused, as CS1 does with every
record.

**The rule, one invariant (I1).** Before PR-9, every string a card holds
is one of: an id Conductor made at random (a record's or a session's 16
hex); a **reference** Conductor made from an id that holds words (a run's
id begins with its crew's id, which is its name made lower case), the
first 16 hex of HMAC-SHA256 under this server's own card key,
`history/cardkey` (32 random bytes, 0600, made at the first card, never
sent, left out of the export as `WhySecret`), so a reference matches on
this server's cards and cannot be confirmed by guessing the name; an
**ordinal** (a task's place in the crew as the run launched it, a check's
place in its task); a time, a count, an exit code; a full commit id; a
SHA-256 digest; a member of a closed set compiled into the binary (a
session kind, an adapter id, a built-in catalog id, a state, an outcome,
a Runtime's registered name); or a Dispatch project or ticket key, sent
only on a card of a project the workspace shares, whose keys the sync
plan already sends to the workspace under its own table. Nothing a
person or an agent typed or named is in any other form.
`TestCardFromHostileValues` builds a session and a run whose every
free-words source holds a distinct marker (the crew's name and so its id
and the run's, every task id, check id and member name, the session's
name and working title, the branch, the folder and the repository's
remote, a catalog agent of the person's own with a marked id and name,
the agent's version output, the host's name and user, a project not
shared whose key is marked, a task's reason, a review's verdict) and
finds no marker in any document the adapter hands a recording sync
store, across a replay and every later delivery; and for every string
field, a card whose value is replaced by a marker fails `Valid`.

**Every field whose source holds words**, and what leaves of it:

| Source (who writes it) | Before PR-9 | From PR-9, default reading | From PR-9, the alternative |
|---|---|---|---|
| a crew's name, and its id (`slug(name)`, `internal/crew/store.go`); a person, an agent through `crew create`, or Brakeman's draft | nothing; the session card carries no crew field, the run card none | `crewName`, a title | nothing |
| a run's id (`<crew id>-<8 hex>`) | the run's reference (keyed, above) | the same reference | the same |
| a task's id, a check's id (`taskIDPattern`, 40 characters a person or an agent chose) | the ordinal | `taskName`, `checkName`, titles beside the ordinal | nothing |
| a member's name | nothing | `member`, a title | nothing |
| the session's name: given at launch or renamed by a person (`Launched.Name`, `Renamed`) | nothing | `name`, a title | `name`, only when a person gave it through the workbench token |
| the session's working title (set by the agent's terminal) | nothing | nothing: it is output | nothing |
| the branch (`session.GitBranch`, which an agent may have made) | nothing | `branch`, a title | nothing |
| a catalog agent's id and name, the person's own entries (`internal/catalog`) | the adapter id only (`claude`, `codex`, ...); a built-in entry's id | `agentName`, a title | nothing |
| the agent's version (the agent program's `--version` output) | `^[0-9]{1,6}(\.[0-9]{1,6}){0,3}([-+][0-9A-Za-z.]{1,16})?$`, at most 32 bytes; else empty | the same | the same |
| a host's name (`os.Hostname()` or the person's) and user | nothing: `hosted` says a host ran it; the device the service knows says where | nothing | nothing |
| the folder, the repository's path and remote, file paths | nothing; the repository as the digest of its root commits | nothing | nothing |
| a check's argument list | its SHA-256 (`subject`, below) | the same | the same |
| a Dispatch project's key and a ticket's key (a person chose the project key) | as they are, on a card of a shared project; empty otherwise | the same | the same |
| a task's reason, a review's verdict and findings, a result's words, instructions, messages, checkpoints, blockers, a Brakeman request | nothing | nothing | nothing |
| a record's entries, the screen, what anyone typed, an answer | nothing | nothing | nothing |

Every title (PR-9) is one line with its control characters removed, cut
to 60 bytes at a character's end, and leaves only when `outputGuard.Check`
answers `clean` for it and the official build's credential check does
not flag it; otherwise it is sent empty. `TestTitlesFollowTheReading`
walks this table's last two columns.

**Two digests that can be confirmed by guessing**, kept on purpose and
written down: a check's `subject` (the unkeyed SHA-256 of its argument
list, HS1's `subject_key`) and a card's `repo` (the SHA-256 of the
repository's root commits). Someone who holds a card can confirm a guess
(`go test ./...`, a public repository's first commit). They are kept
unkeyed because finding the same command in the same repository on
another machine is the item's purpose, and a key every member's server
holds would be held by every reader of the card anyway. Neither is
output or a person's words: an argument list is the crew's configuration
(which the crew's own document already carries to the person's own sync
store under C6), and root commits name a repository, not its content. A
digest of a failure's captured line stays declined (Owner decisions, 7).

**A session's card** (`ext.HistoryCard`, `kind: "session"`), from HS1's
record (`internal/journal`'s `Record`) and nothing else:

| Field | Taken from | Form and bound |
|---|---|---|
| `id`, `rev`, `at` | the record's `ID`; the source's revision (below); when the card was built | 16 hex; integer; time |
| `sessionKind` | `Kind` | one of the session kinds |
| `agent` | the adapter of `Agent.ID`'s catalog entry; `Agent.ID` itself only for an entry the binary's built-in catalog defines; `Agent.Version` | adapter id; built-in id or empty; the version shape above |
| `hosted` | `Host.Hosted` | bool |
| `where` | `Where` | a registered Runtime's name, or empty for unknown |
| `project`, `ticket` | the Dispatch project and ticket the session belongs to (below), when the workspace shares the project | Dispatch's key patterns, or empty |
| `repo` | the digest of the repository's root commits (below) | 64 hex, or empty |
| `start`, `end`, `startFrom` | `Start`, `End`, `StartFrom` | full commit ids; `launch`, `attempt`, `host` |
| `run`, `task`, `attempt` | `Crew.RunID` as its reference; the ordinal of `Crew.Task` in the run's crew as launched; `Crew.Attempt` | 16 hex; 1 to 128; integer |
| `resumedFrom` | `ResumedFrom` | 16 hex |
| `startedAt`, `endedAt`, `lostAt`, `ending`, `exit` | `StartedAt`, `EndedAt`, `LostAt`, how it ended | times; `exited`, `stopped`, `lost`, `host_away` or empty while live; the exit code |
| `questions`, `answered`, `files` | counts of the record's `question` and `answer` entries and of its `Files` rows | integers |

**A run's card** (`kind: "run"`), from the ledger, read in one
transaction (`ledger.CardRows`, PR-5):

| Field | Taken from | Form and bound |
|---|---|---|
| `id`, `rev`, `at` | the run's reference; the source's revision (below) | 16 hex; integer; time |
| `ticket`, `project`, `repo` | its ticket (a run launched from a ticket's plan revision) and that ticket's project, when the workspace shares it; the crew folder's repository digest | key patterns or empty; 64 hex |
| `state`, `outcome`, `paused`, `continuedFrom`, `continuedBy`, `createdAt`, `closedAt` | C6's run pointer, with the two run ids as references | enumerations; 16 hex; times |
| `tasks[]`: `n`, `kind`, `state`, `attempts`, `result`, `importedFrom`, `withdrawn` | each task in the crew's order: its ordinal, kind, `TaskRow.State`, attempts made, the standing result (`ResultAttempt` and `ResultCommit`), the continuation it was imported from (`ImportedFrom` as run reference, task ordinal, attempt), and the last attempt a withdrawal took back | 1 to 128; `work`, `review`; the ledger's task states; integers; `{attempt, commit}` or none; `{run, task, attempt}` or none; integer |
| `tasks[].tries[]`: `n`, `session`, `start`, `result`, `outcome`, `where`, `handed` | each attempt: its number, its session's id, its start and result commits, its outcome, where it ran, and the check receipt it was handed as the engine recorded it (DS2's `previous.check`, a `TaskCheck`): `{run, task, attempt, phase, check, k}` with the run as its reference and the task and check as ordinals, so a receipt of an uncounted attempt or of the run a continuation came from is named as it is | integers, ids, commit ids, enumerations |
| `tasks[].checks[]`: `check`, `k`, `attempt`, `phase`, `subject`, `commit`, `outcome`, `exit`, `at`, `where` | each check receipt: the check's ordinal in its task, its run number, its attempt and phase, the SHA-256 of its argument list, the commit it checked, passed or failed or timed out, the exit code, when, where | integers; 64 hex; commit id; enumerations; time |
| `omittedTries`, `omittedChecks` | attempts and receipts left out by the bound | integers |

**The bound.** A session's card is at most 4 KiB as encoded and a run's
at most 64 KiB (`ext.MaxHistoryCard`); field names are short and the
encoding compact. A run's card is filled in a fixed order until the next
item would pass the bound: the run's head, then every task's head (at
most 128, about 160 bytes each, so about 20 KiB at most), then attempts
newest first across the run, then receipts newest first; what does not
fit is counted in `omittedTries` and `omittedChecks`. A `handed` or
`importedFrom` reference stays valid when what it names was left out,
since it names the receipt or attempt, not a place in the card. The same
rows always give the same bytes. `TestCardBoundIsDeterministic` builds
the largest run the crew's bounds allow (128 tasks, every attempt and
every check receipt the engine can make) and finds a card within 64 KiB,
its counts exact, built twice the same.

**Titles** (PR-9, under the owner's answer; none before): the columns
above. Under the sync plan's default (which this plan follows) every
title in the third column leaves, each through `outputGuard.Check` and
the credential check: a single line of at most 60 bytes cannot copy a
64-byte stretch or two whole lines of captured output, and the check is
asked anyway. Under the alternative, only a session's `name` a person
gave (renamed through the workbench token, or a plain session launched
through it with no crew) leaves.

**Never leaves**, whatever the reading and whatever the owner answers:

| Kept on its machine | Why |
|---|---|
| the record's `Entries` (lines Conductor typed, questions and their choices, answers and how they ended, chat, reports, status words), `Dropped` | the session's words: captured or a person's |
| the session's screen, scrollback, ring, its activity log, its working title | terminal output |
| what a person typed into the terminal or a reply box, and any answer to a question, typed or picked | a person's words, which may be a credential (#673's finding) |
| `Files` paths, `Tools`, `Cwd`, `Repo`, `RepoCommon`, the repository's remote | paths and source structure; activity |
| `Launch` (arguments, their count and digest, variables' names and digest, yolo), `Resumes` | the person's command line and environment |
| `Agent.Session` (the agent's own session id and source) | names a file on that machine; resumable only there |
| `Host.Name`, `Host.User`, `Host.Owner`, a machine id | who and which machine, beyond the device the service knows |
| a check's argument list, log, tail and log digest; a task's instructions and reason; a result's summary and issues; a review's verdict and findings; messages; checkpoints; blockers; a manifest step's receipts; worktree paths; the crew as launched; the launch package | the run's words and configuration |
| every card of a Brakeman turn (a record whose `Crew.Brakeman` is set; a run whose recorded class is `brakeman`, `crew.ClassBrakeman`, never judged by its crew's id, since a person's crew may be saved as `brakeman-2`), of a record or run kept here, of a switchyard, and of a server with `history.share` off | Brakeman reads under a grant on its person's machine; the person's choice |

**The project and the repository.** A card's `project` is the Dispatch
project of the run's ticket when the run was launched from one, else the
one project whose folder (`projects.cwd`) is in the same repository as the
record's (the repository identity HS1 already keeps, `RepoCommon`,
compared with the project folder's; none when no project or more than one
matches), read from a map the API keeps and refreshes on Dispatch's
project changes; it is sent only when the workspace shares that project
(the sync plan's share, as the provider reports it), and empty otherwise.
A refresh of the map, and a change of a project's share, mark every card
whose project it changes (Revisions, PR-5). A card's `repo` is the
SHA-256 of the repository's root commit ids, sorted and joined by
newlines: the one identity of a repository that is the same on every
clone and is neither a path nor a remote address. It is read once per
repository (`gitcli.RootsOf`: `git rev-list --max-parents=0 HEAD`, argv,
the C locale, 5 s) when a card is first built for it, kept in memory only
(at most 1,000 repositories, by `RepoCommon`, the oldest read let go),
read again at most once an hour after a failure, and only for a folder on
this server (`where` native): a hosted record's card has none, since the
server cannot read its host's repository. Cards are built only while a
sync provider is registered (article 7), so a plain build never reads it.

**Who sees a card** is the sync service's rule, not the card's: a card
whose `project` is a project the workspace shares (CS1's share) goes to
every server of the workspace's members; every other card, its `project`
empty, goes only to its person's own other servers. The card names no
scope, so a project shared later brings its later cards to the workspace
(each card of it is marked and built again with its key) and one no
longer shared takes them back, by the service's own rule.

**Skills and templates.** What leaves is only what a person publishes
(PR-9): the version's files, their modes, its name, kind and digest, and
the person's notes, each file and the notes through `outputGuard.Check`
and the credential check. The folder it was imported from, the machine's
places and their modifications, and every other version stay here.

### `internal/skills` (new, PR-1 and PR-1b): versions, what is installed, and where it is placed

A package of the skills and templates kept on this machine, the standard
library, `internal/store` and `internal/agents` (for the agents' skills
directories) only; no HTTP, no session, no process. The API is its caller.

**The data directory's `skills/`** (0700; files 0600), under its own owner
lock `skills/owner.lock` (tried once, then for `LockWait` as the journal
does; without it `ReadOnly()` says so and nothing is written):

```
skills/
  owner.lock
  versions/<digest>/version.json   one version this machine keeps: its manifest
  versions/<digest>/files/<path>   its files, written once
  received/<digest>/...            a version the sync provider handed over (PR-8), the same layout;
                                   the provider's, not this machine's: cleared with remote/
  installed.json                   what this machine installed, each place's state, its placement journal
  made.json                        which crew was made from which template version (PR-3)
  receipts.json                    the operations' receipts, the newest 500
  outbox/<op>.json                 a publish request until the service answers it (PR-9)
  staging/.tmp-<random>/           a version being made
```

**A version** (`version.go`):

```go
type Version struct {
	V      int       `json:"v"`      // 1
	Kind   string    `json:"kind"`   // skill | workflow
	Name   string    `json:"name"`   // ^[a-z0-9][a-z0-9-]{0,39}$, never "conductor"
	Label  string    `json:"label"`  // ^[0-9A-Za-z][0-9A-Za-z.+-]{0,31}$: "local-3f2a91c0", or the workspace's "v3"
	Digest string    `json:"digest"` // SHA-256 over the canonical file list (below)
	Files  []File    `json:"files"`  // sorted by path
	Notes  string    `json:"notes,omitempty"` // at most 2 KiB, one paragraph: what changed
	Origin Origin    `json:"origin"`
	MadeAt time.Time `json:"madeAt"` // when this machine made or received it
}
type File struct {
	Path   string `json:"path"`
	Mode   uint32 `json:"mode"`   // 0644 or 0755
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Origin struct {
	From        string    `json:"from"`              // local | workspace
	Folder      string    `json:"folder,omitempty"`  // local: the folder imported, shown here, never sent
	Workspace   string    `json:"workspace,omitempty"` // workspace: the workspace's id, as the provider names it
	Owner       string    `json:"owner,omitempty"`   // workspace: the name's owner, as the service names them
	Publisher   string    `json:"publisher,omitempty"`
	PublishedAt time.Time `json:"publishedAt,omitzero"`
	Withdrawn   bool      `json:"withdrawn,omitempty"`
}
```

The **digest** is the SHA-256 of `skill\x00<name>\n` (or `workflow`) then,
per file in path order, `<path>\x00<mode in octal>\x00<size>\x00<sha256>\n`:
the files and their modes, never the label, the notes or the origin, so
the same files are one version wherever they come from, and a version is
named by what it holds.

**What a version may hold** (`check.go`, every rule checked on every file
before any byte is written, and again on a received version):

- at most 64 files, each at most 256 KiB, the version at most 1 MiB;
- a path of at most 200 bytes, at most 4 segments, each
  `^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$` (no leading dot, no `..`, no
  absolute path, no backslash); paths that differ only by case refused;
- regular files only: a link, a device, a socket or a pipe in the folder
  refuses the import, naming its path;
- text only: valid UTF-8 with no NUL and no control character but tab,
  line feed and carriage return. A skill is read and run by an agent and
  reviewed by a person, so every byte of it can be shown in a diff;
  binaries, archives and images are refused (`binary_file`, naming it);
- nothing that reads one way and runs another: the bidirectional
  controls U+202A to U+202E and U+2066 to U+2069, U+200E, U+200F and
  U+061C, the zero-width U+200B to U+200D, U+2060 and U+FEFF (anywhere,
  a leading byte-order mark included), and U+00AD are refused
  (`invisible_character`, naming the file, the line and the code point),
  so the text a diff shows is the text an agent runs (the Trojan Source
  pattern);
- a mode of 0644 or 0755, from the source file's owner execute bit; the
  page and the CLI list the executable files apart;
- a skill: a `SKILL.md` at the root, with a front matter whose `name` is
  the version's name and whose `description` is 1 to 1,024 bytes on one
  line (the convention the agents read); a workflow template: a
  `workflow.json` at the root, valid as a portable crew (PR-3), and at
  most a `README.md` beside it. A crew whose portable form passes 256 KiB
  is refused as a template (`template_too_large`, with its size): the
  crew's own bound (`crew.MaxEncoded`, 1 MiB) is larger, but every file a
  person installs must fit the diff view's 256 KiB to be read first.

**The sizes on every wire.** Every reply and every document that carries
a version is encoded with `SetEscapeHTML(false)`, so a text's JSON is at
most twice its bytes (tab, line feed, carriage return, quote and
backslash are two bytes each; every other control character is
refused), and a sync document or `ext` record carries a file's bytes as
base64, at most four thirds of them. So: a manifest (64 files, each a
path of at most 200 bytes of `[A-Za-z0-9._/-]`, a mode, a size and a
digest; the name, label, digest, origin and at most 2 KiB of notes) is at
most 27 KiB encoded, bound at 32 KiB everywhere it travels; a file's
reply at most 513 KiB; a publish request (the manifest and every file in
base64) at most 1.5 MiB. `TestWireBoundsHoldTheLargestVersion` encodes
the largest version the rules allow (64 files, paths at their bound,
every file of quotes and backslashes at 1 MiB in all, notes at their
bound) through every route, record and document and finds each within
its bound.

**Store** (`store.go`), each method safe for concurrent use:

- `Open(st *store.Store) (*Store, error)`: `skills/` and its lock; the
  staging directory emptied; `Recover` (below).
- `Import(ctx, dir *os.Root, kind, name, label, notes string, folder
  string) (Version, error)`: reads the folder through the root its caller
  opened (the API opens it through `openCwd`, so the folder is checked as
  a session's is and never opened again by path), checks it, writes the
  version in `staging/` and renames it to `versions/<digest>`; a digest
  already kept answers the version kept (an import of the same files
  twice is one version).
- `Put(ctx, v Version, files map[string][]byte) error` (PR-8): a version
  received from the workspace, written to `received/`, never to
  `versions/`: the same checks, the digest computed again from the bytes
  and refused when it differs (`ErrDigest`). `Retain(digest)` copies a
  received version into `versions/` (the first step of installing it, by
  a person's operation), and only then is it this machine's.
  `ClearReceived()` removes `received/` whole (the provider's clear and a
  revocation), and `Withdrawn(digest)` removes one received copy that is
  not waiting to be placed.
- `Get`, `List`, `File(digest, path) ([]byte, error)`: a file read through
  an `os.Root` of the version's `files/`, its SHA-256 checked against the
  manifest on every read; a mismatch is `ErrDamaged` and the version is
  listed `damaged` and never placed.
- `Prune()`: at most 200 names, per name at most 5 versions besides the
  pinned ones, and at most 256 MiB in all; the oldest unpinned go first. A
  version is **pinned** while anything refers to it: an install's
  digest, any place's digest (an update of Codex's place alone leaves
  Claude Code's on the older one), any waiting operation's digest and its
  `expect`, and every `made.json` entry (a template a crew was made from,
  which its **Changed** is computed against). A withdrawn workspace
  version that nothing pins goes at the next `Prune`. When the pinned
  versions alone fill the bounds, nothing pinned is removed: the next
  import or retain is refused (`409 skills_full`, with the bytes and
  names held) until the person removes something.
- `Diff(a, b []byte) []Hunk` (`diff.go`): a line diff (Myers), each side
  at most 256 KiB and 10,000 lines; past it, "too large to compare" with
  both sizes and digests. The CLI prints it; the web shows both texts in
  the Files tab's diff view (`DiffEditor.vue`), no new dependency.

**Places** (`place.go`, PR-1b). A skill is placed in one or more of the
agents' skills directories, each a **target**: `agents.SkillDirs(home)`
(PR-1b adds it to `internal/agents`) gives each directory once with the
agents that read it, from the adapters' `SkillPath`: `claude`
(`~/.claude/skills`, Claude Code), `codex` (`~/.codex/skills`), `agents`
(`~/.agents/skills`: pi, Goose, Cursor, Copilot, OpenCode, oh-my-pi, Amp,
DeepSeek Harness) and `agy` (`~/.gemini/antigravity-cli/skills`). A
template has no places.

- A placed skill is the folder `<target>/<name>/`: the version's files
  and a marker, `.conductor-skill.json`, `{v, kind, name, label, digest,
  files: {<path>: <sha256>}}`. A folder there without the marker is the
  person's own (as a `SKILL.md` without `conductor:skill` is today):
  never written, renamed or removed (`ErrNotOurs`). A `<target>/<name>`
  that is a link is treated the same way.
- `Inspect(name) []PlaceState`, for each target the skill is installed
  in: `placed` (every file is the marker's and the version's), `modified`
  (a file differs, was added or was removed since: the paths, and `seen`,
  the SHA-256 over what is on disk as the digest rule computes it, the
  marker left out), `missing`, `not_ours`, `waiting` (an operation waits:
  its sessions), `damaged` (the marker cannot be read).
- `Snapshot(target, name) (Version, error)`: the place's folder as it is
  on disk, the marker left out once it has been read and checked to be
  the marker (a `.conductor-skill.json` that is not one, or any other
  leading-dot path, is still refused), every other file through the
  import's checks: the version **Publish my changes** publishes (PR-9).
- **Holds: what a running session reads.** Placing and removing never
  rest on a snapshot of the registry, which a launch can pass between the
  look and the swap (`internal/api/sessions.go` starts the process before
  `registry.AddReserved` registers it). The store keeps, in memory, the
  **holds** on each target: `Hold(session string, targets []Target)
  (release func(), err error)`, taken by a launch before its process
  starts and released when the session ends, and `Hold` and every step
  of a placement take the store's place lock. A hold taken while a
  placement of that target is between its first and last rename waits
  for that placement to finish (a few renames, never a waiting
  operation's sessions); a placement asked while a target is held goes
  `waiting` with the holders' sessions. `Place` and `Remove` never
  consult anything else to decide busy. A `waiting` operation is settled
  by `Settle()` (the caller's, when a hold is released, and at `Open`)
  for every target no longer held, in the order they were asked.
  `Cancel(name)` ends a waiting operation, the place left as it was.
  Settling completes only an operation a person made; nothing else ever
  writes a place.
- **What a waiting operation keeps**, so a settle after a restart places
  exactly what the person approved: its `op` and actor, the digest to
  place, the installed digest the person saw (`expect`), and per target
  the `replace.seen` digest the person was shown, or none. `Settle`
  checks them again at the moment it places, under the place lock: the
  install's digest still `expect` (else `version_changed`), each place
  `placed` or `missing`, or `modified` with exactly the `seen` the person
  approved (else `place_modified` or `place_changed`), the version not
  damaged, and a workspace version not withdrawn (PR-8: `withdrawn`); a
  check that fails ends the operation unplaced, the place as it was, and
  the receipt says why.
- **A modified place is the person's.** An update or a removal of a
  `modified` place is refused (`ErrModified`, naming the files) unless
  the operation carries `replace: {target, seen}` for it, the `seen`
  digest of what the person was shown; a place that changed again since
  is refused again (`ErrChanged`).

**The placement journal.** One place's placement (or removal) records
each step in `installed.json`, written and synced before the step is
taken, with everything recovery needs: `{op, from, to, new, old, step}`,
`from` the digest there before (or none), `to` the digest to place (none
for a removal), `new` and `old` the two hidden folders' names
(`.<name>.conductor-new-<random>`, `.<name>.conductor-old-<random>`,
chosen before the first step, in the target directory, through an
`os.Root` of it). The steps, and what `Recover` at the next `Open` does
for a crash at each, judging by what is on disk, never by the step
alone:

| Step recorded | Then taken | A crash after recording it: `Recover` |
|---|---|---|
| `writing` | the new folder written, files then marker | the new folder removed; the place as it was; the operation ended (`interrupted`) |
| `old_away` | `<name>` renamed to `old` (none on a first placement) | `<name>` still the `from` version: the step did not happen, back to `writing`'s undo; `<name>` gone and `old` there: carry on to `new_in` |
| `new_in` | `new` renamed to `<name>` | `<name>` gone and `new` complete: rename it in; `<name>` the `to` version: carry on |
| `cleanup` | `old` removed | `old` removed if there; the place `placed` |
| `removing` (a removal) | `<name>` renamed to `old`, then `old` removed | `<name>` the `from` version: rename it away; `old` there: remove it; the place gone |

So no folder under `<name>` is ever half written: `<name>` is the old
version, the new one, or absent. It is absent only between the two
renames of one placement, and, after a crash in that gap, until
`Recover` at the next `Open` puts the new one in (I11). No session that
reads the target runs during either: the placement took place because no
hold was on it, and a launch's hold waits for the placement.

**`installed.json`**:

```go
type Installed struct {
	V      int                `json:"v"`
	Skills map[string]Install `json:"skills"` // by name
}
type Install struct {
	Kind    string    `json:"kind"`
	Digest  string    `json:"digest"` // the version installed (placed, or kept for a template)
	At      time.Time `json:"at"`
	By      string    `json:"by"`     // the actor of the operation
	Places  []Place   `json:"places,omitempty"`
}
type Place struct {
	Target  string   `json:"target"`  // claude | codex | agents | agy, or "config:<agent>:<16 hex>" (below)
	Dir     string   `json:"dir"`     // the target directory itself, as it was resolved when placed
	Digest  string   `json:"digest"`  // what is placed there
	Journal *Journal `json:"journal,omitempty"` // during a placement or removal (above)
	Waiting *Waiting `json:"waiting,omitempty"`
}
type Journal struct {
	Op, From, To, New, Old, Step string
}
type Waiting struct {
	Op       string            `json:"op"`
	By       string            `json:"by"`
	Digest   string            `json:"digest"`  // "" for a removal
	Expect   string            `json:"expect"`  // the installed digest the person saw
	Seen     string            `json:"seen,omitempty"` // replace.seen for this place, as approved
	Since    time.Time         `json:"since"`
	Sessions []string          `json:"sessions"`
}
```

**Templates' crews** (`made.go`, PR-3): `skills/made.json` maps a crew id
to the template version it was made from (`{name, digest, op}`), so the
crew file's schema does not change and an older Conductor still reads
every crew. A crew deleted is dropped from it at the next read.

**Receipts.** Every write (import, install, update, remove, cancel, save
as template, new crew from template, apply, publish, cancel publishing)
is an operation with the client's `op` id (`^[A-Za-z0-9_-]{8,64}$`); its
receipt `{op, what, name, digest, state, places, crew?, at, actor}` is
kept (the newest 500), and the same `op` sent again answers that receipt
and does nothing (the Program, section 1.5). A New crew from template's
receipt names the crew it made, and its replay guard outlives the
receipt's pruning: the crew itself carries the operation (PR-3).

`internal/agents` (PR-1b): `SkillDirs(home string) []SkillDir` (the
targets above, from each adapter's `SkillPath`, so a new adapter that
reads skills is a target with no change here), and `ConductorSkill()`
(the Conductor skill's text and SHA-256, for the page's read-only row).
An agent launched with a configuration directory of its own (`ConfigDirOf`,
#291, read from that launch's own environment) reads skills there, so a
target is a **directory**, never an agent alone: two Claude Code
sessions with two `CLAUDE_CONFIG_DIR`s are two targets,
`config:claude:<first 16 hex of the directory's SHA-256>`, each place
keeping its `Dir`. A launch computes its own targets from its own
environment, before its process starts, and takes its hold on exactly
those (`skillsTargets(agent, env)`); nothing is rebuilt later from the
catalog. A skill installed for that agent is placed in such a directory
at that launch, before the agent starts and before its hold, only when no
other hold is on that directory, by the same `Place`; the launch waits
only for those file writes.

### `internal/api`, `internal/cli` (PR-2): the routes, the sessions that hold a place, the CLI

`internal/api/skills.go` opens the store with the data directory (none
without one; never on a switchyard, which serves no workbench) and
answers `404 skills_unavailable` without a data directory or on a
switchyard, `409 skills_read_only` for a write while another server holds
the lock.

**Who may use these routes.** A skill is code an agent will run with its
person's permissions, so every write that places code, changes a crew or
sends a version (import, install, update, remove, cancel, delete a
version, save as template, new crew from template, apply, publish, cancel
publishing) is made by **this machine's own person, by a deliberate act**:
the workbench token of this server, and nothing else, whatever grant
another identity holds. The routes tell them apart by the principal
`authenticate` gives (`internal/api/auth.go`): a new `requireOwnPerson`
admits only the principal of the workbench token itself (`admin` set,
`subject` empty, not `yard`), and refuses `403 skills_own_person_only`
for an identity provider's principal even with `ext.GrantAdmin` (a
workspace admin, a teammate, or anyone ID1 gives the grant on this
server could otherwise place code under another member's agents), and
`401` for a share link's viewer of either role, a run link, a session's
own token (`CONDUCTOR_NOTIFY_TOKEN`), a host token or a hosted session's
host, Brakeman's principal, and a switchyard's workbench token. The
reads (`GET`) stay `requireAdmin`. The page's every write carries the
`op` and the digest the person was shown (and `expect` and `seen` where
they apply), so a write is the act of someone who saw what it does; the
CLI's writes refuse to run inside a Conductor session (its environment
holds `CONDUCTOR_SESSION_ID` or `CONDUCTOR_NOTIFY_TOKEN`: `conductor
skills: not from inside a session; run it in your own terminal`), so an
agent told to install a skill is told no by Conductor. That is a guard
against an agent following instructions, not a boundary: an agent runs
as its person and can write a skills folder itself, which the page then
shows as **Not Conductor's** or **Modified here**.
`TestSkillsWritesAreTheOwnPersonsOnly` walks every write route with each
of those principals (an identity principal with the admin grant, of
another subject and of any subject; one with the host grant; each link
kind; a session's token; a host token; Brakeman's principal; a
switchyard's token), each refused with its code and nothing written
under `skills/` or any target, and the workbench token admitted;
`TestSkillsCLIRefusesInsideASession`.

| Route | Body → answer | Bounds |
|---|---|---|
| `GET /api/skills` | → `{skills: [Row], templates: [Row], conductor: {sha256, places}, targets: [{id, dir, agents}], readOnly?}`; a row: name, kind, the installed version (label, digest, origin), its places' states, the versions kept, a waiting operation with its sessions' names | reply at most 1 MiB |
| `GET /api/skill-versions/{digest}` | → the version's manifest | 32 KiB |
| `GET /api/skill-versions/{digest}/file?path=` | → `{path, mode, text}` | 513 KiB (a 256 KiB file, escaped) |
| `GET /api/skills/{name}/compare?from=&to=` | `from`: a digest or `disk:<target>`; `to`: a digest → per file `{path, change: added \| removed \| changed \| same, from?, to?}` with both texts | reply at most 2 MiB, `truncated` past it |
| `POST /api/skills/import` | `{op, path, kind, name?, label?, notes?}` → `201 {version, receipt}`; `400 invalid_skill` naming the file and the rule | body 4 KiB |
| `POST /api/skills/{name}/install` | `{op, digest, targets, expect?, replace?}` → `{receipt}`, each place `placed` or `waiting` with its sessions; `409 version_changed` when `expect` (the installed digest the person saw) is not the installed one; `409 place_modified`, `409 place_changed`, `409 skill_not_ours`, naming the place | body 8 KiB |
| `POST /api/skills/{name}/remove` | `{op, targets, replace?}` → `{receipt}` | body 8 KiB |
| `POST /api/skills/{name}/cancel` | `{op}` → `{receipt}` | body 1 KiB |
| `DELETE /api/skill-versions/{digest}` | → `204`; `409 version_in_use` while pinned (Prune's rule) | |

Every body is read with `DisallowUnknownFields` and its limit, and every
reply encoded as "The sizes on every wire" says. An update is an install
of another digest with `expect`.

**The sessions that hold a place.** Each launch this server makes
(`internal/api/sessions.go`, and the engine's attempts through
`api.Server.Launch`, which reach the same code) computes its targets from
its own catalog agent's adapter and its own environment
(`skillsTargets`: the adapter's `SkillPath` directory and its
`config:` directory, if any) and takes `skills.Store.Hold` on them
**before its process starts**; the hold is released when the session
leaves the registry, or at once if the launch fails. Every server
session counts, whatever its `where`: a Runtime other than native is
counted too, the cautious side. **Hosted sessions** count too: a
`conductor host` can run on this server's own machine under the same
person and read the same skills directories (`docs/plans/hosts.md`,
"A host whose key is this server's own runs on this server's machine"),
so every hosted session whose agent's adapter reads a target holds it
from the moment the host registers it until it ends, the cautious side;
PR-8 narrows this to hosts whose machine key is this server's, once
HS2's PR-1 has put the key on `main`. A hosted session the host starts
during a placement's two renames can find the folder absent for that
moment; the server cannot hold what it does not launch, and the install
dialog says so. `Settle` is called on a goroutine of the server's own,
coalesced, when a hold is released; never on the hook's goroutine.
Sessions Conductor does not run (an agent started in a shell) cannot be
seen: the install dialog says that those read the new files at their
next skill load.

**The CLI**: `conductor skills list | show <name> | import <dir>
[--template] [--name] [--label] [--notes] | install <name> <label|digest>
[--agents claude,codex] | update <name> <label|digest> | remove <name> |
compare <name> [<from>] [<to>] | cancel <name>`, with the workbench
token, printing the receipt; `import` sends the folder's path, which the
server opens. Completion and usage text. The writes refuse to run inside
a session (above). There is no MCP tool and no operating-guide row for
agents: a skill is code an agent runs, and an agent installing its own
is out of bounds (Owner decisions, 13).

**The export** (`internal/export/leftout.go`): `skills/versions/`,
`made.json` and `receipts.json` are carried as the data directory's other
files are. `installed.json` is carried as `skills.ExportInstalled` makes
it, the way `catalog.json` is carried as `export.ResolveCatalog` makes it:
each install's digest and each place's target, `Dir` and digest, and no
`Journal` and no `Waiting`, so an archive taken while an install or a
removal waits holds no operation for a restore to settle.
`skills/received/` and `skills/outbox/` are `WhyProvider` (the
provider's, as `remote/` is), `skills/staging/` is `WhyTemporary` and
`skills/owner.lock` `WhyLock`. A restore installs the versions and the
record of what was installed, and places nothing: every place shows
`missing` (or `placed`, if the folder with its marker is already there)
until the person installs it there (Owner decisions, 11).
`TestRestoreOfAWaitingArchivePlacesNothing` takes an archive while an
install and a removal wait and restores it onto an idle machine: no
place written, no operation waiting.

`docs/protocol.md` gains a Skills section with every route and bound
above; `docs/repository-map.md` the `internal/skills` row.

### `internal/crew`, `internal/skills`, `internal/api`, `internal/cli` (PR-3): workflow templates

- **The portable crew** (`internal/crew/portable.go`): `type Portable
  struct` with the crew's name, goal, isolation, open-after-launch, view
  link time, members (name, agent id, prompt, arguments, start), tasks
  (id, member, kind, instructions, needs, checks), setup and an inline
  manifest; never the crew's `id`, `cwd`, `where`, `origin`, `yolo`,
  `createdAt`, `updatedAt` or `manifestRevision`. **No yolo travels**: a
  crew made from a template, or one a template is applied to, keeps the
  machine's default (approvals on) or the crew's own setting, and only its
  person turns it off, on the crew, as today (article 4: required
  approvals stay on, yolo off by default); `TestTemplateCarriesNoYolo`
  makes a template from a yolo crew and finds the new crew without it,
  and an applied version leaving the crew's own setting as it was. A
  manifest kept in the repository (`file: true`) stays a reference to the
  repository's `conductor.manifest.json`, which git already shares.
  `PortableOf(c)`, `FromPortable(p, cwd) (Crew, error)` (validated by the
  crew's own `Validate` once the folder is given), and `PortableDigest(p)`
  (SHA-256 of its canonical JSON). A template's `workflow.json` is a
  `Portable` with `v: 1`, read with `DisallowUnknownFields`, at most 256
  KiB (`template_too_large` past it, PR-1's rule; a valid crew above 256
  KiB is refused with its size, `TestTemplateTooLargeRefused`).
- `POST /api/crews/{id}/template` `{op, name, label?, notes?}` → `201
  {version}`: **Save as workflow template**, a version of kind `workflow`
  made from the crew's portable form (`skills.ImportBytes`, the same
  checks as a folder's).
- `POST /api/skills/{name}/crew` `{op, digest, cwd, name?}` → `201
  {crew, receipt}`: **New crew from template**, made once whatever
  crashes: the folder through `resolveCwd`; then the crew store's
  `CreateFrom(c, "template:" + op)`, which today takes only Brakeman's
  origins and PR-3 widens by one form (`^template:[A-Za-z0-9_-]{8,64}$`,
  and every reader of `Origin` that means Brakeman checks its prefix, as
  `TestBrakemanOriginsOnlyMeanBrakeman` holds), so the same `op` sent
  again, before or after a crash, finds the crew it made (or
  `ErrOriginDeleted` when a person deleted it since, answered `410
  crew_deleted`), never a second; then `made.json`, then the receipt,
  with the crew's id. A replay of an `op` whose receipt was pruned still
  finds its crew by its origin. `TestCrewFromTemplateOnceAcrossACrash`
  stops after `CreateFrom` and after `made.json` through a seam and sends
  the `op` again.
- `POST /api/skills/{name}/apply` `{op, crew, digest, expect}` → `{crew}`:
  a person applies a template version to a crew made from it. The check
  and the write are one step: `crew.Store.UpdateIf(id, check, c)` (PR-3)
  rereads the crew under the store's lock and calls `check(old)`, which
  refuses `409 crew_changed` when the stored crew's portable digest is not
  `expect` (what the person was shown), so an edit committed between the
  page's read and the apply is never written over. Against launches, the
  apply runs under `crew.Engine.WhileNoRun(crewID, f)` (PR-3), which holds
  the lock a launch of that crew takes before it reads the crew file (the
  seam is added where the launch takes none today) and refuses `409
  crew_running` while a run of that crew is not final: a launch then
  reads the crew wholly before the apply or wholly after it. The crew's
  portable fields are replaced, its id, folder, `where`, `origin` and yolo
  kept. A run already made keeps the crew as it was launched, as every
  run does. `TestApplyChecksExpectUnderTheLock` commits an edit between
  the route's read and the write through a seam; `TestApplyAgainstALaunch`
  holds a launch at a gate after its lock.
- **What a template will run is shown apart from its JSON.** New crew
  from template and Apply each open a dialog (PR-4) that lists, before the
  JSON and its difference, every member's agent and **arguments** (a
  skip-permissions flag among them, marked), every **setup** command and
  every task's **check** commands, each as its argument list, with "These
  commands run on this machine when the crew launches." The dialog sets
  no yolo.
- `GET /api/skills` gives a template's row its crews: each crew made from
  it with `changed` (its portable digest differs from the version's) and
  the template version it was made from.
- `conductor skills template <crew> <name>`, `conductor skills crew <name>
  <cwd>`, `conductor skills apply <name> <crew>`, each printing the
  arguments, setup and check commands first.

### `web/app` (PR-4): the Skills page

- `pages/skills.vue` (`/skills`): two sections, **Skills** and **Workflow
  templates**, each a table (Nuxt UI `UTable`) of rows; a skill's row
  shows its installed version (label, short digest, from where, owner),
  each place as a badge (**Placed** `success`, **Modified here**
  `warning`, **Waiting** `neutral` with "for 2 sessions to end" and their
  names linked, **Missing** `neutral`, **Not Conductor's** `neutral`,
  **Damaged** `error`) and its actions (Install, Update, Compare, Remove,
  Cancel). The Conductor skill is the first row, read only, with its
  places. A template's row shows its crews with **Changed** where so, and
  **New crew**.
- `SkillImportModal.vue` (a folder path, the kind, a label and notes),
  `SkillInstallModal.vue` (the targets as checkboxes, each with its
  state; the files, or for an update each changed file in `DiffEditor`;
  a modified place's own difference from the installed version, with
  **Replace my changes** off by default; the executable files listed;
  the line "This skill's files are instructions and scripts your agents
  will run with their own permissions. Read them before you install."),
  `SkillCompare.vue`, `TemplateCrewsList.vue`, `TemplateCrewModal.vue`
  (New crew from template and Apply: the members' agents and arguments,
  the setup commands and every check's commands listed apart, each as its
  argument list, a skip-permissions flag marked, before the JSON and its
  difference; the line "These commands run on this machine when the crew
  launches."; no yolo control).
- The crew page (`pages/crews/[[id]].vue`): **Save as workflow template**
  in its menu; the crews list's empty state and header: **New crew from
  template**.
- `utils/skills.ts` (pure: a row's states, words and colors; which
  actions a row offers; the waiting line), `composables/useSkills.ts`
  (the routes, an `op` id per action, kept until its receipt).
- The navigation (`utils/sidebar.ts`, `BottomBar.vue`) gains Skills, and
  the palette's Go to gains **Go to Skills**.
- `web/e2e/skills.spec.ts` (below).

### `internal/api`, `internal/journal`, `internal/crew/ledger`, `internal/gitcli`, `ext`, `ext/exttest` (PR-5): history cards to the sync store

- **`ext.HistoryCard`** (`ext/history.go`): the card of the tables above,
  `HistorySession`, `HistoryRun`, `HistoryTask`, `HistoryAttempt`,
  `HistoryCheck`, `HistoryCheckRef`, `MaxHistoryCard` (64 KiB), and
  `Valid()`, which checks every string against its pattern and refuses
  any field PR-9 has not added. Standard library only.
- **The card key and the references** (`internal/api/history_card.go`):
  `history/cardkey`, made at the first card, and `cardRef(kind, id)`, the
  keyed reference of "What leaves a machine, field by field"; the run's
  crew as launched gives the tasks' and checks' ordinals. The export's
  `leftOut` gains `history/cardkey` as `WhySecret`.
- **A keyed computed source in the adapter** (`internal/api/sync.go`).
  The adapter today delivers what a store committed, read at delivery
  from where it was committed. A card has no commit of its own, so it is
  a **computed source**, the one mechanism HS2's host summary uses too
  (`docs/plans/hosts.md`, its PR-5, which adopted this shape at its final
  check): registered once with `a.computed(src)`, its keys (`paths`,
  what a replay lists), its build at delivery (`build(path) (body,
  deleted, err)`) and its revision rule; a change marks one key pending
  through `a.computedChanged(store, path)`, which takes the adapter's own
  lock alone and never builds or waits. Whichever of HS2's PR-5 and this
  PR merges first makes the mechanism, with `TestSyncComputedSourceKeys`
  (a test source of two keys, each marked, built at delivery and sent
  alone, one built `deleted` and sent as a delete, both listed in a
  replay after the pointers), and the other only registers its source.
  This PR's source: store `history` (`syncHistory`), paths
  `session/<id>` and `run/<ref>`, built by `historyCard(path)`.
  - **Marked**: by the journal's `OnWrite(id, seq)` and `OnForget(id)`
    (PR-5 adds both to `internal/journal`, called after the write or the
    removal has committed, never waiting), by the ledger's `OnCommit` and
    `OnPrune`, whose callbacks the adapter already installs for the run
    pointer and which now mark the run's card beside it, by a change of
    `history/share.json` (the card it names), and by a change of the
    project map or of a project's share (every card whose `project` it
    changes, coalesced).
  - **Delivered**: built from the record as it then is (`journal.Get`) or
    from the ledger (`ledger.CardRows(ctx, run)`, PR-5, one read
    transaction over the run's tasks, attempts and check receipts); a
    record or run that is gone, kept here, a Brakeman turn's (by the
    record's `Crew.Brakeman` or the run's recorded class), or now out of
    the table's reach is `Deleted`. A card that fails its own `Valid` is
    never delivered: the adapter logs its id and delivers `Deleted`, and
    `TestEveryCardBuiltIsValid` holds that none does.
  - **Revisions**: a card's `Rev` is the source's own: one counter for
    the whole `history` source in this epoch, kept in memory, raised at
    every delivery of any of its keys, a build or a `Deleted`, and
    started again at 1 by a new epoch's replay, as HS2's summary's
    sequence is. So a card changed by what has no revision of its own (a
    record kept here, then shared again; a project's mapping or share)
    still rises, a `Deleted` and a later rebuild of the same key each
    take a newer revision, and nothing is discarded as stale by a store
    that orders by `Rev` (the adapter's own model in
    `internal/api/sync_test.go` does). The sync service orders by its
    provider's own counter and keeps `Rev` for display (follows the sync
    plan's final text). `TestHistoryCardRevisionsRise` keeps a card here,
    shares it again, remaps its project and changes the project's share,
    and finds each delivery newer than the last and none dropped by the
    adapter's model.
  - **Replayed**: every replay lists, after the run pointers and before
    Dispatch's `log` doc, every record the journal holds and every run
    the ledger holds (paged by `syncPage`), so each epoch delivers the
    whole set and a card no longer listed is dropped by the store.
  - **Registered only** while a sync provider is registered
    (`ext.Continuity()`, article 7), and not on a switchyard, without a
    data directory, while the journal is read only (the server that holds
    it sends), or with `history.share` off.
- **`history.share`** (`CONDUCTOR_HISTORY_SHARE`, `1`/`0`, on by
  default): whether this server sends history cards at all when a sync
  provider is registered; it changes nothing without one. Off at a
  restart: the next replay lists no card, so the store drops every one
  (C6's epochs).
- **Kept here.** `PUT /api/history/share` `{ref: "session:<id>" |
  "run:<id>", shared: bool}` (admin) → `204`: the ids kept here, in
  `history/share.json` (at most 4,096, a forgotten record's id removed),
  the card marked so its next delivery is `Deleted` (or built again). The
  record page and the run page show **Keep on this machine** (PR-7).
- `internal/gitcli`: `RootsOf(dir)` (the root commits, argv, 5 s). The
  repository map, in memory only, and the project map in
  `internal/api/history_card.go`.
- `ext/exttest`: the `Continuity` check's samples gain a `history` doc
  (a valid card), and `samples.go` a card at its bound.
- `docs/protocol.md`'s sync section: the `history` store, its paths, its
  revisions and the card's fields and bounds; `ext.Doc.Store`'s comment
  names `history`.

### `internal/history`, `internal/api`, `ext` (PR-6): other machines' cards in the index

- **The remote kind `history`.** CS1's remote records (`ext.Remote`,
  written by the provider through `Host.PutRemote` into
  `remote/<device>/<kind>-<id>.json`) gain the kind `history`, this
  plan's own (the sync plan leaves kinds to the items that add them and
  checks them in decoders, never in a schema), whose `Data` is an
  `ext.HistoryCard` as its server built it, `Valid` on arrival (a card
  that fails is refused by the door), and whose record carries, beside
  CS1's fields, `deviceName`, `person`, `own` and `openURL` (fields a
  reply may gain, the sync plan's rule; follows its final text).
- **`openURL` carries no credential.** It is honoured only when the
  record's `own` is true, and only when it is an `https` URL with no user
  part, no query and no fragment, whose host is the sending device's own
  address as the provider gives it beside the record, and whose path is
  `/` or `/history/sessions/<id>` of the card's record; anything else is
  dropped when the record is decoded, and the card shows no link. The
  sign-in happens at that server, through its own identity check of the
  person, never through anything the link carries.
  `TestOpenURLCarriesNoCredential`: a record whose `openURL` holds a query
  token, a user part, a fragment, another host or `http`, or whose `own`
  is false, shows no link.
- **The index's schema version 2** (`internal/history`): `entries` gains
  `repo_key TEXT NOT NULL DEFAULT ''`, `device_name TEXT NOT NULL DEFAULT
  ''` and `person TEXT NOT NULL DEFAULT ''`, and HS1's reserved `origin`
  holds the sending device's id for another machine's rows; an index of
  version 1 is cleared in place and rebuilt from the stores, HS1's rule
  for an index of another version (it is a cache). The kinds table gains
  `remote.session`, `remote.run` and `remote.attempt` (class `record`)
  and `remote.check` (class `checked`, its outcome the card's): a receipt
  another server made is still a receipt, marked with where it was made.
  This machine's rows gain `repo_key` from the repository map.
- **The remote source** (`internal/api/history_remote.go`): the indexer
  reads each `remote/<device>/history-*.json` as a source keyed
  `remote:<device>/<kind>/<id>`, its mark the card's `rev`; the provider's
  `PutRemote`, `DeleteRemote` and `ClearRemote` mark the source dirty
  through a hook on the door, and the indexer's reconcile drops sources
  whose files are gone. Entries: a session card is one `remote.session`
  entry; a run card a `remote.run`, an entry per attempt and per check,
  and the links the card holds (`handed`, `passed_after` within the
  card's run). An entry's title and summary are made of the card's fields
  only, with ordinals ("Claude Code session · task 2, attempt 2 · CND-12",
  "check 1 of task 2 · passed at def5678"), and from PR-9 with the titles
  the card carries; its body is empty.
- **Search.** A new filter, `on:` (`on:here`, or a device's name; a comma
  for alternatives). `Similar` gains the scope `workspace`: the receipts
  of the same `subject_key` with the same `repo_key` on every origin,
  ranked as HS1 ranks them (passed before failed, newest first), this
  machine's first. The History routes' `Entry` gains `origin: {device,
  deviceName, person, reportedAt}` and the availability `remote` ("on
  studio · reported 14:05").
- **Other machines' rows only when asked.** The index's search gains an
  option, `Remote bool`, off by default: every caller that does not ask
  gets this machine's rows only (`origin = ''`). The History routes ask
  for it for the workbench token and an identity principal with the
  admin grant; nothing else does. So Brakeman's reads (HS1's PR-11, #630,
  the `brakeman` principal's search under its grant), built on the
  search, never see another machine's card, and a `refURL` is never
  made for one; a later caller has to ask. `TestRemoteRowsOnlyWhenAsked`
  here, and `TestBrakemanSeesNoOtherMachine` in PR-9, which lands after
  #630.
- **When other machines are shown**: `remoteShown()` (`internal/api/
  remote_shown.go`), in HS2's shape (`{present, shown, state, since}`;
  `present` when `sync` is ready and the status has been current at
  least once in this server's life and is not signed out; `shown` when,
  besides, it is current or behind for less than 10 minutes), made here
  with `TestRemoteShown` unless HS2's PR-5 merged first, then only read.
  When not `shown`, every search adds `origin = ''`, `on:` answers
  nothing for another device, the `workspace` scope is this machine's,
  and the reply's `remote: {state, since}` says why; nothing is deleted,
  so the rows return with the next current status, with no reindex.
- `GET /api/history/remote/{device}/{kind}/{id}` (admin): the card whole,
  with `deviceName`, `person`, `own` and `openURL` (only when it passed
  the rule above); `404` while not shown or gone.
- `docs/protocol.md`: the filter, the scope, the entry's `origin`, the
  route, the remote kind; `web/app/utils/protocol.ts` the types.

### `web/app` (PR-7): other machines in History

- The palette's history rows and the History page (which ask for other
  machines' rows, PR-6) draw another machine's
  row with a second badge, **on studio · Ada**, after its class badge
  (Conversation never appears: no remote kind is `said`), and the detail
  line's host as the device name; the History page gains a **Machine**
  filter chip (This machine, each device by name).
- **Find this elsewhere** on a failed receipt gains **On every machine**
  (`scope=workspace`) beside HS1's **Find this in every repository**.
- **The card page**, `pages/history/remote/[device]/[kind]/[id].vue`: what
  the card holds, in the record page's layout; the ticket linked when the
  project is shared here; commits shown with **Copy**; "Held on studio
  (Ada's machine). Its record and its words stay there." and, for one's
  own server, **Open studio** (the `openURL`, only when it passed PR-6's
  rule, opened with `rel="noopener noreferrer"`) and the record's path
  there; for a teammate's, no link; "studio last reported at 14:05".
- **The one line** when `remote.state` is not current, at the top of the
  History page and in the palette's history group: "Other machines'
  history is not shown: the sync service is unreachable since 14:02" (or
  "the workspace's subscription has lapsed", or nothing at all while the
  provider is starting or signed out).
- The record page and the run page: **Keep on this machine** / **Share
  with the workspace's history**, shown only with `sync` in the
  capabilities, and the line "Shared as a card: who, where, what ran and
  how it ended; never what was said or printed."
- `utils/historyRemote.ts` (pure: a remote row's words, the line's words,
  the card page's sections) and its vitest; `web/e2e/history-remote.spec.ts`
  proves what a plain build shows (below).

### `internal/skills`, `internal/api`, `ext`, `ext/exttest`, `web/app` (PR-8): the workspace's versions

- **`ext.SkillVersion`** (`ext/skills.go`): `{Manifest SkillManifest; Files
  []SkillFile}`, `SkillManifest {Kind, Name, Label, Digest, Notes,
  Workspace, Owner, Publisher, PublishedAt, Withdrawn, Files
  []SkillFileInfo}` (at most 32 KiB encoded), `SkillFile {Path, Mode,
  Data}` with `Data` the file's bytes (base64 in JSON), `MaxSkillVersion`
  (1 MiB of files), `MaxSkillFiles` (64), `MaxSkillFile` (256 KiB), each
  with `Valid` holding PR-1's rules.
- **`Host.PutSkillVersion(ctx, v SkillVersion) error`**, this plan's own
  door (the sync plan leaves it to this item), for the sync store alone
  (any other provider's `Host` answers `ErrUnsupported`): the version
  handed to `skills.Store.Put`, which writes it to `skills/received/`
  with `origin: workspace` and the workspace's id, its digest computed
  again from the files and refused on a mismatch. It installs nothing and
  never writes `versions/`.
- **A received version is the provider's until a person installs it.**
  `skills/received/` is a cache like `remote/`: listed in `available`
  only while `remoteShown()`, read by the compare and file routes only
  then, never exported (`WhyProvider`), and emptied whole by the
  provider's clear (CS1's `ClearRemote`, called for a removed member, a
  lowered role or a revoked device: PR-8 hooks `ClearReceived` to it). An
  install of a received version first copies it into `versions/`
  (`Retain`), so what a person installed, and what they keep after
  uninstalling it, is theirs and stays through a clear; what was only
  offered goes. `TestClearRemovesReceivedVersionsButInstalled`.
- **The remote kind `skill`**, this plan's own: a version's
  `SkillManifest` and its state (`published`, `withdrawn`, or the answer
  to this server's own publish request, PR-9), at most 32 KiB. The Skills
  reply gains `available`: each received version, by name, newest first,
  with its owner, publisher, time, notes and state; only while
  `remoteShown()`, and the reply's `remote: {state, since}` otherwise.
- **An update is the same lineage.** `update: {from, to}` is offered on
  an installed row only when the installed version's origin is the
  workspace, its `Workspace` is the one the version came from, and the
  newer version has the same `Owner` (the service gives a name one owner
  in a workspace, its first publisher, and later versions of the name
  come only from that owner or an admin, PR-11). Any other version of the
  same name (beside a skill the person imported themselves, `origin:
  local`, or from another workspace or owner) is listed as **Another
  skill named release-notes, by Bob**, and installing it is a
  **replacement**: the install dialog shows every file in full, never a
  difference framed as an update. `TestUpdateOnlyWithinALineage`.
- **An update from the workspace** is an install of the newer digest
  with `expect`, exactly as a local one: the person's operation, shown
  first.
- **A withdrawn version.** Not offered; its received copy removed
  (`Withdrawn(digest)`) unless an operation waits to place it; a waiting
  placement of it is refused when it settles (`Settle`'s `withdrawn`,
  the place as it was, the receipt saying "withdrawn by its owner"); one
  installed stays installed and says "withdrawn by its owner"; a retained
  copy nothing pins goes at the next `Prune`.
  `TestSettleRefusesAWithdrawnVersion`, `TestWithdrawnPrunedUnlessPinned`.
- **Hosted sessions narrowed.** With HS2's PR-1 on `main`, a hosted
  session holds a target only when its host's machine key is this
  server's own (PR-2 counted every hosted session until then).
  `TestBusyCountsTheAgentsThatReadTheTarget` gains the case.
- The page: **Available from the workspace** under each section, the
  update banner on an installed row, **Another skill named ...** with
  **Install as a replacement**, the owner and publisher in every
  version's line, the one line when not shown (`utils/remoteShown.ts`).
- `ext/exttest`: `FakeHost` records `PutSkillVersion`; the samples gain a
  version at its bounds.
- `docs/protocol.md` and `protocol.ts`: the reply's `available`, the
  remote kind, the door's bounds.

### `internal/api`, `internal/skills`, `web/app`, `docs` (PR-9): publishing, the cards' titles, the features entry

Cut after the owner's answer to the sync plan's question, once CS1's row
that adds the check of copied output (`outputGuard.Check`) is on `main`,
and after HS1's PR-11 (#630).

- **Publish** (`POST /api/skills/{name}/publish` `{op, digest, notes}`,
  this machine's own person only, `requireOwnPerson`; `409
  sync_unavailable` unless `remoteShown()` says shown): writes the
  request, `skills/outbox/<op>.json` (`{v, op, workspace, subject, kind,
  name, digest, notes, files: [{path, mode, data}]}`, at most 1.5 MiB as
  "The sizes on every wire" says, at most 8 waiting). The request is
  **bound to what the person approved**: `workspace` and `subject` are
  the workspace and the person the sync status names at that moment
  (follows the sync plan's final text for what the status carries;
  without them publishing is not offered), and the dialog named that
  workspace. The provider sends a request only while it is signed in to
  that workspace as that person; otherwise it is held, and the page says
  "Approved for <workspace>; this server is now signed in elsewhere" with
  **Cancel publishing** (`POST /api/skills/{name}/publish/cancel {op}`,
  the outbox file removed). It is never sent to another workspace.
- **Delivered by its own computed source**, store `skills`
  (`syncSkills`), the mechanism of PR-5: its keys are the outbox's files
  (`paths` lists `skills/outbox/`), built at delivery from the file,
  marked when one is written or removed, and listed by every replay, so a
  request whose first delivery failed is found again after a restart or
  an overflow's replay with no later write; delivered in parts as any
  document past `ext.MaxDocData`. `TestOutboxRediscoveredAtReplay` leaves
  an unanswered request on disk, restarts, and sees it delivered.
- **Checked before it is written.** Every file and the notes pass
  `outputGuard.Check` under the reading the owner chose (`422
  quotes_output`, naming the file and its line, for `output`; `409
  check_pending` for `pending`); the credential check is the official
  build's and the service's.
- **Answered by the service.** A `skill` remote record names the
  request's `workspace` and `op` (not its digest alone): `published` as
  `vN`, or `refused` with the service's words, shown on the page; the
  outbox file is removed at the answer for that workspace and op only.
- **Publish my changes**: a person may publish an installed place they
  modified: the place's folder through `Snapshot` (the marker left out,
  every other file through the import's checks) as a local version,
  then published. `TestPublishMyChanges` publishes straight from a marked
  installation.
- The publish dialog lists every file and its size once more, the
  notes, and "Visible to the members of <workspace>, and no one else."
- **Titles on the cards**, under the answer: the builder fills `name`,
  `crewName`, `member`, `taskName`, `checkName`, `branch` and `agentName`
  as "What leaves a machine, field by field" says, each through
  `outputGuard.Check` and sent only when `clean`, `Valid` admits them,
  and `TestTitlesFollowTheReading` holds the rule.
- **Brakeman sees no other machine.** With #630 on `main`,
  `TestBrakemanSeesNoOtherMachine`: the `brakeman` principal's history
  reads, under a grant whose scope a remote card's project key would
  pass, return no remote row and no `refURL` for one (PR-6's search
  option, which #630 does not set).
- `docs/features.md`: TS1's entry (what a plain build gains, what a
  workspace adds, each PR's checks, the by-hand record of Acceptance);
  `docs/repository-map.md`'s rows; `docs/protocol.md`'s publish routes,
  the `skills` source and the outbox; `internal/export`'s `leftOut`
  names `skills/outbox/` (`WhyProvider`: a request for the provider,
  never restored).

### The hosted service's PRs (PR-10, PR-11)

In the hosted service's repository, in words (its files are private).
Both follow CS1's sync service and ID1's identity as their final text has
them, and reach no machine but through the sync provider.

- **PR-10, team history**: the sync service keeps history cards per
  sending device, decoded with unknown fields refused and every value
  checked against this plan's table; the newest `rev` of each card in an
  epoch, and a replay's end dropping what it did not list (C6's rule); a
  card whose project is shared with the workspace delivered to every
  server device of the workspace's members, every other card to its
  person's own server devices only; each delivered with the sending
  device's name and its person's display name, and whether it is the
  receiving person's own, as a remote record of kind `history`; a member
  removed, a role lowered or a device revoked takes their cards from
  every other machine and stops delivery to theirs (CS1's revocation); the
  workspace's export gains the cards; storage, retention, deletion and
  lapse follow OP1's plan for stored records (what is stored stays
  readable through a lapse, and new cards are refused); titles under the
  credential check, refused when flagged, and a card's project key kept
  only when the workspace shares that project. The person name and the
  `own` mark and its open link on a delivered record are fields a reply
  may gain (follows the sync plan's final text); the open link is a plain
  `https` address of the device's page, with no credential in it. Its
  versioned wire contract gains the card with its vectors: a card at
  every bound, every field of "Never leaves" and every name of "Every
  field whose source holds words" planted as an unknown field and
  refused, a credential-like title refused.
- **PR-11, shared skills**: a store of versions per workspace, each
  immutable and named by its digest, recomputed on arrival; a name's
  owner is the person who first published it, and later versions are
  published by the owner or a workspace admin only, never by a reviewer
  or a guest; labels `v1`, `v2`, … given by the service in order; the same
  digest published again is answered with the version it is; every file
  and the notes under the credential check, a flagged version refused
  whole with the file and line named; a publish request taken only from
  a device whose person is the request's `subject` in the request's
  `workspace`, its answer naming that workspace and `op`; **withdraw** by
  the name's owner or a workspace admin, signed in to the hosted service's
  own pages under ID1 (no route on a member's server withdraws), its
  `skill` record then `withdrawn` on every member's server; versions and
  their manifests delivered to the server devices of the workspace's
  members (`skill` records and the files), never listed, searched or
  served to anyone outside the workspace; at most 200 names and 20
  versions a name kept per workspace; the export gains them; storage and
  lapse as PR-10's; its contract with vectors (a version at its bounds,
  every file of quotes and backslashes at 1 MiB in all, a binary file
  refused, an invisible character refused, a digest mismatch refused, a
  publish by a reviewer refused, a request whose subject or workspace is
  not the sending device's refused).

### The official build's PRs (PR-12, PR-13)

In the official build's repository, in words. Both add to CS1's sync
provider and carry the outage and lapse tests (article 8).

- **PR-12, team history**: the provider takes `history` documents from
  the adapter, decodes each into its own type field by field (the table
  above; an unknown field refused and logged, never sent), runs the
  credential check on titles (sent empty when flagged), queues them in its
  outbox and sends them; hands received cards to `Host.PutRemote` with
  the device's name, the person's name, `own` and the device's open link
  (a plain `https` address, no credential), and deletes them as the
  service says; and reports nothing new in the sync status. Tests: the outage (the service unreachable mid-session:
  History's search answers `remote: unreachable` and this machine's rows
  unchanged, back with no restart), the lapse (the same with `lapsed`,
  nothing local removed), a card with a planted unknown field never sent,
  and a Playwright run of the official build against its fake sync
  service showing another machine's row in the palette, its card page,
  and the one line once the service is stopped.
- **PR-13, shared skills**: publish requests from the `skills` source to
  the service, each sent only while signed in to its `workspace` as its
  `subject` (held otherwise), their answers as `skill` records naming the
  workspace and `op`; a clear or a revocation empties the received
  versions with the remote records (`ClearReceived`); received versions handed
  to `Host.PutSkillVersion` (files fetched once per digest, never on a
  page's request) and their manifests to `PutRemote`; the outage and lapse
  tests (an update offered, then hidden behind the one line, with every
  installed skill still placed), and a Playwright run showing **Update
  available** with its difference, an install, and the publish dialog's
  answer.

## Events, orderings and invariants

No state machine of the engine, the ledger, a session or Dispatch
changes, and no frame or control message does. What is new: a store of
versions and places, the operation that places a skill, a sync source of
cards, two remote kinds and a door, a publish request, and the index's
second schema version.

**What each store holds.**

| Store | Holds | Written by | Exported |
|---|---|---|---|
| `skills/versions/<digest>/` | one version this machine keeps: its manifest and files, written once by a rename of a staged directory | `skills.Store` (Import, Retain) | yes |
| `skills/received/<digest>/` | a version the provider handed over, until a person installs it, it is withdrawn, or the provider clears | `skills.Store.Put` (PR-8), through `Host.PutSkillVersion` | no (`WhyProvider`) |
| `skills/installed.json` | per name, the version installed and each place's target, directory, digest, placement journal and waiting operation | `skills.Store` | as `skills.ExportInstalled` makes it: no journal, no waiting operation; a restore places nothing |
| `skills/made.json` | which crew was made from which template version | `skills.Store` (PR-3) | yes |
| `skills/receipts.json` | the newest 500 operations' receipts | `skills.Store` | yes |
| `skills/outbox/<op>.json` | a publish request, bound to its workspace and person, until the service answers it or the person cancels it | the publish routes (PR-9) | no (`WhyProvider`) |
| `skills/staging/`, `skills/owner.lock` | a version being made; the lock | `skills.Store` | no |
| holds (memory only) | which sessions read which targets, from before their process starts until they end | the launch, through `skills.Store.Hold` | no |
| `<target>/<name>/` in an agent's skills directory | a placed version's files and its marker | `skills.Store.Place`, only | no: outside the data directory |
| `history/share.json` | records and runs kept here | the API (PR-5) | yes |
| `history/cardkey` | the key of this server's card references | the API (PR-5), at the first card | no (`WhySecret`) |
| the repository map (memory only) | repositories' root digests | the API (PR-5), only while a sync provider is registered | no |
| `history/index.sqlite`, schema version 2 | HS1's entries, and other machines' cards as entries | the indexer | no (HS1's rule) |
| `remote/<device>/history-*.json`, `remote/<device>/skill-*.json` | other machines' cards; the workspace's versions' manifests and states | the provider, through CS1's doors | no (CS1's rule) |
| the sync service | cards per device; versions per workspace | the hosted service | the workspace's export |

**Placing a skill** (one place; each step recorded in `installed.json`'s
journal before it is taken, "The placement journal"):

```
(none) --install--> writing --> new_in --> cleanup --> placed
placed --install other digest--> [held?] waiting --(Settle: no hold, approval still true)--> writing --> old_away --> new_in --> cleanup --> placed
placed --remove--> [held?] waiting --(Settle)--> removing --> (none)
waiting --cancel--> as it was before the operation
waiting --(Settle: expect, seen or withdrawn no longer true)--> as it was; the receipt says why
placed --(a file changed by hand)--> modified  (seen by Inspect; nothing written)
modified --install or remove without replace{seen}--> refused, place_modified
launch --Hold(targets), before the process--> held --(session ends)--> released --> Settle
```

`Recover`, at `Open`, before any hold can be taken: for each place with
a journal, the table of "The placement journal", judged by what is on
disk. A `waiting` operation outlives a restart, with its approval
(`expect`, `seen`), and is settled when a hold is released or at
`Open`, against the holds then, and only if its approval is still
true.

**A history card** is marked when its record or run commits and built
when it is delivered, so the newest state goes out, and a record forgotten
between the mark and the delivery is delivered as `Deleted`. Within an
epoch the card's revisions rise (I4). A replay lists them all; a store
drops what a completed replay did not list (C6).

**A publish request** (PR-9): written to the outbox after the copied-output
check passed, with the workspace and person the person approved it for;
delivered through the `skills` source (listed by every replay), only
while the provider is signed in to that workspace as that person;
answered by a `skill` remote record naming its workspace and `op`
(`published` with its label, or `refused` with words); the outbox file
removed at that answer, or by **Cancel publishing**. Unanswered, it stays,
at most 8, and the page says "waiting for the sync service since 14:02".

**A received version** (PR-8): `Put` into `received/` (installs
nothing); listed while shown; installed only by a person's operation,
whose first step `Retain`s it into `versions/`; removed by a withdrawal
(unless an operation waits on it, which then refuses at its settle) or by
the provider's clear, which takes `received/` whole and leaves
`versions/`.

**Conflict rules.** None are needed, and none are made: a card has one
writer, the server that holds the record or the run, and the service keeps
its newest revision per (device, kind, id) in an epoch; a version is
immutable, so two versions never merge, and two publishes of one name are
ordered by the service, which refuses one from a person who is neither the
name's owner nor a workspace admin. Dispatch's own rules (CS1's) are
untouched.

**Invariants**, each with the test that holds it:

- **I1, before PR-9 no byte of a card was chosen by a person or an
  agent.** Every string is a random id, a keyed reference, an ordinal, a
  time, a count, a commit id, a digest, a member of a closed set compiled
  into the binary, or a shared project's Dispatch key; no card field is
  read from a record's entries, files, tools, launch, resumes, folder,
  repository path, branch, name or working title, its host's name, user
  or owner, the agent's own session, a crew's name or id, a run's id as
  written, a task's or check's id, a member's name or a catalog entry the
  person wrote; no run card field from a check's argument list, log, tail
  or digest, a task's instructions or reason, a result's words, a
  review's verdict or findings, a message, a checkpoint or a blocker
  (`TestCardFromHostileValues`, `TestACardCarriesNothingTheRecordHeard`,
  `TestARunCardCarriesNoWordsOfTheRun`, `TestHistoryCardFieldsAreTheTable`).
  From PR-9 the only additions are the titles, each through the checks
  (`TestTitlesFollowTheReading`).
- **I2, some things never leave.** No card of a Brakeman turn (by the
  record's mark or the run's recorded class, never a crew's id), of a
  record or run kept here, from a switchyard, from a read-only journal,
  with `history.share` off, or with no sync provider registered
  (`TestBrakemanTurnsSendNoCard`, with a person's crew saved as
  `brakeman-2` whose run does send one; `TestKeptHereIsDeleted`,
  `TestShareOffSendsNoCard`, `TestLocalContinuityBuildsNoCard`).
- **I3, built at delivery.** A card is built from the record as it is
  when delivered; a forgotten or pruned record is `Deleted`
  (`TestForgottenRecordsCardIsDeleted`).
- **I4, revisions rise.** Every delivery of the `history` source, a
  build or a `Deleted`, takes the next value of the source's one counter
  in its epoch, whatever changed the card (its record, its run, its
  share choice, its project's mapping or share)
  (`TestHistoryCardRevisionsRise`).
- **I5, hidden is not deleted.** While `remoteShown()` is false no other
  machine's row is served, and none is removed; they return with the next
  current status, with no reindex (`TestRemoteRowsHiddenThenBack`).
- **I6, a version is what its digest says.** Written once by a rename;
  every file's SHA-256 checked on every read; a received version whose
  digest does not match its files is refused (`TestVersionIsImmutable`,
  `TestDamagedVersionIsNeverPlaced`, `TestPutRefusesAWrongDigest`).
- **I7, a place is Conductor's only with its marker.** Never written into a
  folder without the marker, through a link, or over a modified place
  without the `seen` the person was shown (`TestNotOursIsNeverTouched`,
  `TestModifiedNeedsTheSeenDigest`, `TestLinkedFolderIsNotOurs`).
- **I8, never under a running session.** A target a session of this
  server reads is held from before its process starts until it ends, and
  a held target is not written; its operation waits and is settled only
  when no hold is on it; a hosted session holds the targets its agent
  reads (`TestPlacementWaitsForRunningSessions`,
  `TestSettleAfterTheLastSessionEnds`, `TestNewSessionKeepsItWaiting`,
  `TestLaunchBetweenLookAndSwap` and `TestLaunchBeforeRegistration`,
  each interleaving held at a gate).
- **I9, only a person's operation writes a place or a crew, and only as
  approved.** A received version, a settle and a restore change nothing
  installed by themselves (`TestReceivedVersionInstallsNothing`,
  `TestRestoreOfAWaitingArchivePlacesNothing`); a settle places only what
  its approval (`expect`, `seen`) still describes, after a restart too,
  and never a withdrawn version (`TestSettleRechecksWhatWasApproved`,
  `TestSettleRefusesAWithdrawnVersion`); a template is applied to a crew
  only by a person, never to one whose run is not final, and only over
  the crew the person saw (`TestApplyRefusedWhileRunning`,
  `TestApplyChecksExpectUnderTheLock`, `TestApplyAgainstALaunch`); a
  template carries no yolo (`TestTemplateCarriesNoYolo`); an update is
  offered only within a lineage (`TestUpdateOnlyWithinALineage`).
- **I10, only this machine's own person writes skills.** Every skills
  write admits the workbench token alone: a share link, a session's own
  token, a host token, Brakeman's principal and an identity provider's
  principal of any grant are refused; the CLI's writes refuse inside a
  session; no MCP tool and no guide row exists
  (`TestSkillsWritesAreTheOwnPersonsOnly`,
  `TestSkillsCLIRefusesInsideASession`, `TestGuideHasNoSkillsRow`).
- **I11, a half placement never shows.** `<name>` is the old version,
  the new one, or absent; absent only between one placement's two
  renames, or after a crash there until `Recover` at the next `Open`,
  and no session that reads the target runs in either
  (`TestRecoverAtEveryBoundary`, by a seam that stops before and after
  each recorded step, then `Open`).
- **I13, what is offered is the provider's; what is installed is the
  person's.** A received version lives in `received/`, never exported,
  emptied by the provider's clear; an install retains it in `versions/`
  first (`TestClearRemovesReceivedVersionsButInstalled`).
- **I14, a publish request goes only where it was approved.** It carries
  its workspace and person, is sent only while signed in as them, is
  answered by workspace and `op`, and is found again at every replay
  (`TestPublishBoundToItsWorkspace`, `TestOutboxRediscoveredAtReplay`).
- **I12, an outage or a lapse removes nothing local.** History, versions,
  places and the outbox stay as they are (the official build's outage and
  lapse tests; `TestRemoteRowsHiddenThenBack` here).

**Migrations.** The history index goes to schema version 2: an index of
version 1 is cleared in place and rebuilt from the stores and the remote
files, HS1's rule for an index of another version; an older Conductor
that finds version 2 answers `other_version` and does not read it, as
HS1's readers do, and rebuilds it to version 1 when it next holds the
lock. `skills/` is new and has no earlier form. No ledger or Dispatch
schema changes (`ledger.CardRows` reads). No crew file gains a field: a
crew's template is kept in `skills/made.json`. A crew made from a
template carries `origin: template:<op>` (PR-3's second origin form,
which keeps its making idempotent); a Conductor older than PR-3 refuses
that one crew as unreadable (its origin pattern), as it would any later
form, and reads every other crew as before.

**Protocol.** No frame or control message: `internal/proto` is not
touched. The HTTP routes (Skills, the history share route, the remote
card route, the filter and scope) are in `docs/protocol.md` with every
field and bound, and in `web/app/utils/protocol.ts` from the PR whose
page calls them, each with a test; the sync documents (the `history`
and `skills` computed sources) and the remote kinds (`history`, `skill`) in
`docs/protocol.md`'s sync section beside C6's; the `ext` records with
their `Valid` and their bounds; the hosted service's versioned wire
contract, in its own repository, gains the card and the version with
their vectors (PR-10, PR-11).

## Out of scope

- **A marketplace of any kind**: no public listing, no listing across
  workspaces, no search of other workspaces' skills, no ratings,
  downloads counts or payment, no install from a URL, a registry or a
  package manager. A version is seen by the members of the one workspace
  it was published in (the Program, section 7).
- Binary files, archives or images in a skill; a skill's dependencies
  installed by Conductor (a script that needs a tool says so in its
  `SKILL.md`).
- Updating or installing anything by itself, on a schedule, at a
  version's arrival or at a restore.
- Skills on a `conductor host` machine (its own server, if it runs one,
  installs them) and in a Runtime other than native: an attempt in a
  guest reads the guest's skills, which a later item may copy in (F).
- Skills a repository holds (`.claude/skills/` in a project): git shares
  them already, and Conductor leaves them alone.
- Sending any words of a session or a run: transcripts, output, typed
  lines, answers, reports, chat, file paths, arguments; a digest of a
  failure's line (Owner decisions, 7).
- Opening, resuming or continuing a session or a run on another machine
  from this one, and any cross-machine execution (the Program, sections
  1.5 and 7).
- Merging histories or conflict rules for them (each card has one writer).
- Signed versions (a person's signature on a version through identity): a
  later item, if review of a version by its digest is not enough.
- The compare crew's generator (CP1), project manifests (EN1), and the
  Conductor skill's own update at launch, which stay as they are.

## Tests per surface

Per `docs/checks.md`: every Go PR runs `make lint`, `go test -race
-count=1 -timeout 30m ./...` and `make lint-static`; every web PR
`npm --prefix web run typecheck`, `npm --prefix web test` and `make
test-e2e` (the new spec alone first). No Python and no workflow changes.
The tests each PR adds:

**`internal/skills` (PR-1, versions)**: `TestImportChecksEveryRule` (one
case per rule: count, size, path, case-only twins, link, device, binary,
NUL, control character, each bidirectional and zero-width code point and
a leading byte-order mark, mode, a `SKILL.md` without its front matter or
with another name, a template without `workflow.json`, a template past
256 KiB), `TestDigestIsTheFiles` (the same files under two labels and two
folders are one digest; a mode changed is another),
`TestVersionIsImmutable`, `TestDamagedVersionIsNeverPlaced`,
`TestReadOnlyWithoutTheLock`, `TestPrunePinsEveryReference` (a version
held only by one place's digest, one only by a waiting operation's
`expect`, one only by `made.json`: all kept; the bounds filled by pinned
versions: `skills_full`, nothing pinned removed),
`TestReceiptAnswersTheSameOp`, `TestDiffBounds`,
`TestWireBoundsHoldTheLargestVersion`.
**`internal/skills`, `internal/agents` (PR-1b, places)**:
`TestNotOursIsNeverTouched`, `TestLinkedFolderIsNotOurs`,
`TestModifiedNeedsTheSeenDigest`, `TestSnapshotLeavesOutTheMarker`,
`TestPlacementWaitsForRunningSessions`, `TestSettleAfterTheLastSessionEnds`,
`TestNewSessionKeepsItWaiting`, `TestHoldWaitsForAPlacementsRenames`,
`TestSettleRechecksWhatWasApproved` (a waiting update whose installed
digest changed, whose place was edited again after its `seen`, and the
same after a restart: each ended unplaced with its reason),
`TestCancelLeavesThePlace`, `TestRecoverAtEveryBoundary` (a seam that
stops before and after each recorded step of a placement, a first
placement and a removal, then `Open`: `<name>` the old or the new
version, never partial, every hidden folder gone), `TestConfigTargetsByDirectory`
(two `CLAUDE_CONFIG_DIR`s, two targets, each place keeping its
directory); `internal/agents`: `TestSkillDirsFromEveryAdapter` (every
adapter with a `SkillPath` gives its directory once).

**`internal/api` (PR-2)**: `TestSkillsWritesAreTheOwnPersonsOnly`,
`TestImportOpensTheFolderOnce` (the folder through `openCwd`, a folder
outside the allowed roots refused, a link swapped in after the check not
followed), `TestInstallUpdateRemoveReceipts`, `TestInstallRefusesAStaleExpect`,
`TestBusyCountsTheAgentsThatReadTheTarget` (a Claude Code session holds
`claude`, a pi session holds `agents`, a hosted session of a Claude Code
agent holds `claude`, a session with its own configuration directory
holds that directory's `config:` target and not another's),
`TestLaunchBetweenLookAndSwap` and `TestLaunchBeforeRegistration` (a
launch held at a gate after its hold and before `AddReserved`, and a
placement held at a gate between its look and its first rename: the
placement waits, or the launch waits for the renames),
`TestSettleRunsOffTheHook`, `TestSkillsUnavailableOnASwitchyard`,
`TestGuideHasNoSkillsRow`, `TestRestoreOfAWaitingArchivePlacesNothing`
(`internal/export` too); **`internal/cli`**: `TestSkillsCommands` (each
command's request and printed receipt), `TestSkillsCLIRefusesInsideASession`,
completion's rows.

**`internal/crew`, `internal/api` (PR-3)**: `TestPortableDropsTheMachine`
(id, folder, `where`, origin, yolo, times and manifest revision gone; a
file manifest kept as a reference), `TestFromPortableValidates`,
`TestTemplateRoundTrip` (save, new crew in another folder, the crews
equal but for the machine's fields), `TestTemplateCarriesNoYolo`,
`TestTemplateTooLargeRefused`, `TestCrewFromTemplateOnceAcrossACrash`,
`TestBrakemanOriginsOnlyMeanBrakeman`, `TestApplyRefusedWhileRunning`,
`TestApplyChecksExpectUnderTheLock`, `TestApplyAgainstALaunch`,
`TestMadeFromSurvivesAndForgetsDeletedCrews`, `TestOlderCrewFilesUnchanged`
(no crew file gains a field).

**`web/app` (PR-4)**: vitest `skills.test.ts` (each state's words and
color, which actions each row offers, the waiting line with names),
`useSkills.test.ts` (an `op` id kept until its receipt, sent again on a
retry) and `TemplateCrewModal.test.ts` (the members' arguments, setup and
check commands listed apart from the JSON, a skip-permissions flag
marked, no yolo control); Playwright `web/e2e/skills.spec.ts`, the e2e
server's own HOME: import a fixture skill folder made by the spec under
a name of its own, install it into `agents`, see **Placed**, change its
`SKILL.md` on disk and see **Modified here** with its difference, update
to a second imported version and be refused until **Replace my
changes**, remove it; **the waiting state**, with the e2e's stub agent
launched under Claude Code's adapter (`conductor.e2e.json` defines it,
and the adapter's `SkillPath` makes it hold `claude`): install into
`claude` while it runs and see **Waiting** naming it, **Cancel** and see
the place as it was, install again, stop the session, and see **Placed**,
each step waited on by what the page draws (the badge), never a clock;
save a crew the spec makes as a workflow template, make a crew from it in
another folder (the dialog lists its check commands), change the crew
and see **Changed**.

**PR-5 (`internal/api`, `internal/journal`, `internal/crew/ledger`,
`internal/gitcli`, `ext`)**: `TestCardFromHostileValues` (I1; above),
`TestACardCarriesNothingTheRecordHeard` (a recording `ContinuityStore`
registered; a session whose record holds a marker in every entry kind, a
typed answer sent through `ext.Host`'s `Submit` with a question and
through the host's `answer_as`, a marker in its launch's arguments and
variables' names, its folder, a file path, a tool's name, its host's name
and user and its agent's session id; across a replay and every later
delivery, no document's bytes hold a marker),
`TestARunCardCarriesNoWordsOfTheRun` (the same for a run: crew name and
id, task and check ids, member names, argv, log, tail, instructions, a
task's reason, result words, verdict and findings, a message, a
checkpoint), `TestHistoryCardFieldsAreTheTable` (`ext`: reflection over
the card's types against the table's patterns), `TestEveryCardBuiltIsValid`,
`TestRunCardCarriesTaskState` (a standing result, a result withdrawn
after its attempt passed, a result imported by a continuation),
`TestHandedNamesTheReceipt` (a receipt of an uncounted attempt and one of
the run a continuation came from, each named as the engine recorded it),
`TestCardBoundIsDeterministic`, `TestHistoryCardRevisionsRise`,
`TestForgottenRecordsCardIsDeleted`, `TestBrakemanTurnsSendNoCard`,
`TestKeptHereIsDeleted`, `TestShareOffSendsNoCard`,
`TestLocalContinuityBuildsNoCard` (with nothing registered, no source is
registered, no card is built, no `git` runs and no file of the data
directory changes), `TestCardProjectAndRepo` (a ticket's run, a plain
session in a project's repository, two projects in one repository giving
none, a project not shared giving none, a hosted record with no `repo`),
`TestRepoKeyIsTheRootCommits` (`internal/gitcli`: two clones, one key; a
repository with two roots), `TestSyncComputedSourceKeys` (if this PR
makes the mechanism); `internal/journal`: `TestOnWriteAfterCommit`,
`TestOnForgetAfterRemoval`; `internal/crew/ledger`:
`TestCardRowsOneTransaction`; `ext/exttest`: the `Continuity` check's new
sample passes against `local.Continuity` and the fake.

**PR-6 (`internal/history`, `internal/api`, `ext`)**: `TestSchemaTwoRebuildsInPlace`
(a version 1 index cleared and rebuilt; a version 1 reader answering
`other_version`), `TestRemoteKindsAndClasses` (in
`TestClassComesFromTheKindAlone`'s table), `TestRemoteSourceFollowsTheDoors`
(put, delete and clear, each reflected at the next pass),
`TestRemoteCardRefusedWhenInvalid`, `TestOpenURLCarriesNoCredential`,
`TestOnFilter`, `TestRemoteRowsOnlyWhenAsked`, `TestSimilarAcrossMachines`
(the same argv digest in the same repository on two devices, passed
first; another repository's left out), `TestRemoteRowsHiddenThenBack` (a
fake sync status: current, unreachable, lapsed, current again; nothing
reindexed), `TestRemoteShown` (if this PR makes it), `TestRemoteCardRoute`.

**`web/app` (PR-7)**: vitest `historyRemote.test.ts` (a remote row's
badges and words, the card page's sections, own and a teammate's, no
link for an `openURL` that failed the rule, the line for each state);
Playwright `web/e2e/history-remote.spec.ts`: on a plain build with
`remote/` seeded with a card before the server starts, no other
machine's row appears in the palette or on the History page, no **On
every machine** is offered, and the card route answers `404`; the
section's presence is the official build's Playwright run (PR-12), since
only a registered provider makes it appear.

**PR-8 (`internal/skills`, `internal/api`, `ext`, `web/app`)**:
`TestPutSkillVersionOnlyForTheSyncStore`, `TestPutRefusesAWrongDigest`,
`TestReceivedVersionInstallsNothing`, `TestAvailableOnlyWhenShown`,
`TestClearRemovesReceivedVersionsButInstalled` (a clear with one received
version installed, one installed then removed, one only offered: the
first two stay in `versions/`, the third is gone from every route and
the export), `TestUpdateOnlyWithinALineage` (a workspace version beside
a local import of the same name, and beside one of another owner: shown
as another skill, never as an update), `TestSettleRefusesAWithdrawnVersion`,
`TestWithdrawnPrunedUnlessPinned`, `TestWithdrawnStaysInstalled`;
`TestBusyCountsTheAgentsThatReadTheTarget`'s hosted case narrowed by the
machine key; vitest for the available list, the update banner and
**Another skill named ...**; the Playwright spec extended: seeded `skill`
records show nothing on a plain build.

**PR-9 (`internal/api`, `internal/skills`, `web/app`)**:
`TestPublishWritesTheOutboxOnlyWhenShown`, `TestPublishRefusesCopiedOutput`
(a file quoting a check's log under the reading chosen; a `pending`
verdict refused too), `TestPublishBoundToItsWorkspace` (signed in to
another workspace or as another person: held, never sent; cancelled:
removed), `TestOutboxRediscoveredAtReplay`, `TestOutboxClearedByTheAnswer`
(an answer for another workspace or `op` leaves it), `TestOutboxBound`,
`TestPublishMyChanges`, `TestTitlesFollowTheReading` (each title's
source and bound; a working title never; an `output` or `pending`
verdict sent empty; under the answer given, what leaves), and
`TestCardFromHostileValues` and `TestACardCarriesNothingTheRecordHeard`
run again with titles on (a marker only where its title row says it
leaves); `TestBrakemanSeesNoOtherMachine` (after #630); vitest for the
publish dialog and its held line.

**The hosted service (PR-10, PR-11)** and **the official build (PR-12,
PR-13)**: their tests are named in their rows above: the contract's
vectors, the scope and revocation rules, the outage and lapse tests, and
the Playwright runs that show what only a registered provider shows.

**By hand** (Acceptance): a real Claude Code session on each of two
machines, two people, a shared project; what tests cannot show is that
the card found is the one a teammate needed, that the commit it names
fixes their failure, and that a skill placed while a real agent runs is
not seen by that agent until it ends.

## Boxcar constraints

The eight of the Program, section 2.5:

1. **The engine starts an attempt only through `crew.Launcher`.** Not
   touched: a crew made from a template launches as any crew does.
2. **`session.Local` drives a `session.Process`.** Not touched: a
   launch takes its hold on the skills directories from its agent's
   adapter and its environment before it starts the process, and releases
   it when the session ends; nothing reads a pid or a tty.
3. **An attempt's context is files relative to its worktree.** Not
   touched. A skill lives in an agent's skills directory, not in an
   attempt's context; a task that needs one says so in its instructions.
4. **The agent talks to the server over the network.** Not touched; no
   agent route is added.
5. **Checks go through `internal/proc`.** Not touched: no check runs. The
   repository's root commits are read through `internal/gitcli` (argv).
6. **Platform splits stay platform tags.** None added; the page shows a
   capability (`sync`), never a plan.
7. **The ledger's evidence rows carry `where`.** A run card carries each
   attempt's and receipt's `where` as the ledger has it, and nothing
   assumes `native`; a hosted record's empty `where` stays unknown.
8. **`hostagent` is a launch site as much as `api`.** Not touched:
   skills are placed on the server's machine only; a hosted session, which
   may run on this machine, holds the directories its agent reads from
   the moment the host registers it, read on the server from what the
   host reports, with no change to `hostagent`.

## Open-core checklist

- [x] **1. No paid code, licence check or plan check.** Other machines'
  history and the workspace's skills show on `sync` in the capabilities
  and the provider's status; nothing reads a plan or an entitlement.
- [x] **2. Fully useful on its own.** A plain build has HS1's history
  whole and gains versioned skills and workflow templates, installed
  deliberately, with modifications and differences shown.
- [x] **3. Nothing paid behind a flag.** Team history and shared skills
  need the sync service's records; `history.share` only stops sending, it
  turns nothing paid on.
- [x] **4. Stop, inspect, revoke, export, required approvals free and
  whole.** History stays an inspect surface; the export carries the
  skills; removal from a workspace clears what it shared and leaves what
  was installed; nothing is approved, answered or launched from these
  pages.

## The PRs

Each PR is one issue and one branch `issue-N` from `main` (or a branch in
its own repository), cut once the PRs it depends on have merged; it
leaves its repository's `main` working and green on its own, ticks the
checklist above, names its checks (one not run named as such), and
updates the `docs/protocol.md` and `docs/repository-map.md` rows it
changes. Sizes are changed lines, code + tests, each a batch (the owner,
2026-11-05: 300 to 800 lines, at most about 1,500 with tests). "May run
beside" names only PRs that share no package or directory with it. HS1's
rows are named by their issues (#613 to #632); CS1's open-source rows by
what they add (its public plan, `docs/plans/sync-seams.md`, names them).

| PR | Repo | Title | Touches | Depends on | May run beside | Size |
|---|---|---|---|---|---|---|
| PR-1 | open source | skills: versions, their checks and digest, the store, prune with its pins, receipts, the line diff | `internal/skills` (new) | none (`main` as it is) | PR-5 | ~450 + 500 |
| PR-1b | open source | skills, agents: places with their marker, holds, the placement journal, settle with its approval, recover, `SkillDirs` | `internal/skills`, `internal/agents` | PR-1 | PR-5 | ~450 + 550 |
| PR-2 | open source | api, cli, export: the Skills routes and who may write, the holds at launch, `conductor skills` | `internal/api`, `internal/cli`, `internal/export`, `docs` | PR-1b | none | ~700 + 750 |
| PR-3 | open source | crew, skills, api, cli: workflow templates (the portable crew, save as, new crew from, apply) | `internal/crew`, `internal/skills`, `internal/api`, `internal/cli`, `docs` | PR-2 | none | ~550 + 650 |
| PR-4 | open source | web: the Skills page, its install, compare and template dialogs, templates on the crew pages | `web/app`, `web/e2e` | PR-3 | PR-5 | ~850 + 650 |
| PR-5 | open source | api, journal, ledger, gitcli, ext: history cards to the sync store, the computed sources, `history.share`, kept here | `internal/api`, `internal/journal`, `internal/crew/ledger`, `internal/gitcli`, `internal/config`, `internal/export`, `ext`, `ext/exttest`, `docs` | HS1's PR-3 (#615) on `main` (after #613 and #614); never beside HS2's PR-5 | PR-1, PR-1b, PR-4 | ~700 + 750 |
| PR-6 | open source | history, api, ext: other machines' cards in the index (schema 2), `on:`, `Similar` across machines, `remoteShown()` | `internal/history`, `internal/api`, `ext`, `web/app/utils/protocol.ts`, `docs` | PR-5; HS1's PR-4 (#616); CS1's remote records and sync status on `main`; never beside HS2's PR-5 | none | ~700 + 750 |
| PR-7 | open source | web: other machines in History, the card page, the one line, keep here | `web/app`, `web/e2e` | PR-6; HS1's PR-7 (#621) | none | ~550 + 550 |
| PR-8 | open source | skills, api, ext, web: the workspace's versions, received apart, `PutSkillVersion`, updates within a lineage | `internal/skills`, `internal/api`, `ext`, `ext/exttest`, `web/app`, `web/e2e`, `docs` | PR-4, PR-7; CS1's remote records on `main`; HS2's PR-1 (the machine key) on `main` | none | ~700 + 700 |
| PR-9 | open source | api, skills, web, docs: publishing, the cards' titles, the features entry | `internal/api`, `internal/skills`, `internal/export`, `web/app`, `docs` | PR-8; HS1's PR-11 (#630); the owner's answer on what counts as terminal output; CS1's check of copied output on `main` | none | ~650 + 700 |
| PR-10 | hosted service | team history: cards kept per device, scope by shared project, delivery with names, revocation, export, contract | the sync service, its store and contract | CS1's service rows that serve remote records; ID1's members and devices; an open-source tag holding PR-9's `ext` types (the titles) | none | ~600 + 700 |
| PR-11 | hosted service | shared skills: versions per workspace, owners, publish rules, withdraw, delivery, export, contract | the sync service, its store and contract | PR-10; an open-source tag holding PR-9's | none (same service) | ~650 + 700 |
| PR-12 | official build | the sync provider: cards out, cards in, the outage and lapse tests, the Playwright run | the sync provider | PR-10 deployed to staging; CS1's provider rows; an open-source tag holding PR-9's | PR-11 (other repository) | ~400 + 600 |
| PR-13 | official build | the sync provider: publish requests, received versions, the outage and lapse tests, the Playwright run | the sync provider | PR-11 deployed to staging; PR-12 | none | ~400 + 550 |

**PR-1**: `internal/skills`'s versions: the version and its checks, the
digest, the store with its lock, staging and prune with its pins, the
receipts, the line diff, the wire sizes. No place, no route, nothing
calls it yet. Tests: PR-1's above.

**PR-1b**: `internal/skills`'s places: the marker, `Inspect`,
`Snapshot`, the holds, `Place`, `Remove`, the placement journal and
`Recover`, `Settle` with its approval, `Cancel`; `agents.SkillDirs` and
`ConductorSkill`. Nothing calls it yet. Tests: PR-1b's above.

**PR-2**: the routes with their bounds and `requireOwnPerson`; the holds
taken at every launch before its process starts, and for hosted
sessions; `Settle` when a hold is released; the CLI with its refusal
inside a session, and its completion; the export's rows and
`skills.ExportInstalled`; `docs/protocol.md`'s Skills section;
`docs/repository-map.md`'s row. After it, a person can import, install,
update, compare and remove skills from the CLI.

**PR-3**: `internal/crew/portable.go` (no yolo), `CreateFrom`'s template
origin, `UpdateIf`, `Engine.WhileNoRun`, `skills.ImportBytes` and
`made.go`, the three routes, the CLI's three commands, the protocol
rows.

**PR-4**: the page, its dialogs (the template dialog with its commands
apart), the crew page's two actions, the navigation and the palette;
`skills.spec.ts` with the waiting state. After it the free half of the
item is shipped; Acceptance part C can be observed.

**PR-5**: `ext.HistoryCard` and its `Valid`; the card key and the
references; the adapter's computed sources (made here unless HS2's PR-5
merged first, then only registered) and the `history` source, marked by
the journal's two new hooks, the ledger's, the share choice and the
project map; its revisions; the card builder with its project and
repository maps; `ledger.CardRows`; `gitcli.RootsOf`; `history.share`
and the share route; the contract suite's sample; the sync section of
`docs/protocol.md`. Nothing is built or sent without a provider, and
nothing of PR-9's titles. HS1's PR-12 (#632, what a host reports)
touches `internal/journal`'s hosted fields: whichever merges second
rebases, never beside it.

**PR-6**: the remote kind `history` with its record's fields and the
`openURL` rule, the index's schema 2 and remote kinds, the remote source
and its hook on the doors, the search's `Remote` option, `on:`,
`Similar`'s workspace scope, `remoteShown()` (made here unless HS2's PR-5
merged first, then only read), the entry's `origin`, the card route, the
protocol rows. HS2's PR-5 touches `internal/api` and `ext` too: never
beside it.

**PR-7**: the History page's and the palette's remote rows, the
**Machine** chip, **On every machine**, the card page, the one line, keep
here on the record and run pages, `history-remote.spec.ts`.

**PR-8**: `ext.SkillVersion` and its records, `Host.PutSkillVersion`,
the remote kind `skill`, `skills.Store.Put` into `received/`, `Retain`,
`ClearReceived` on the provider's clear, withdrawal, the reply's
`available` and its lineage rule, the hosted sessions narrowed by the
machine key, the page's Available sections, banner and **Another skill
named ...**, the fakes; the protocol rows.

**PR-9**: the publish routes, the outbox bound to its workspace and
person, the `skills` computed source, `outputGuard.Check` over files,
notes and titles, **Publish my changes** through `Snapshot`, the cards'
titles under the owner's answer, the publish dialog,
`TestBrakemanSeesNoOtherMachine`; the features entry with Acceptance part
C's record and the open-source half of parts A and B; the repository
map. This is the last open-source PR and ends the item's open-source
work.

**PR-10 to PR-13**: in their own repositories, in their own words and
checks; their bodies tick article 3 (each feature names the hosted
dependency that gates it: here, the sync service). They follow CS1's and
ID1's PR order and are cut when those rows are in their repositories'
issues. They come after the owner's answer in any case: they wait for
CS1's hosted rows, which wait for CS1's first open-source tag, which holds
CS1's check of copied output, which waits for the answer.

### What this plan asked of the sync plan, and its answers

This item's needs were posted on the sync plan (CS1's, in its own
repository) and answered there on 2026-11-06. The sync plan keeps every
row of its first release as it was and leaves room for this item: a
client reads its replies ignoring fields it does not know, so a reply may
gain one; a record's kind is checked by its decoder, never by a database
constraint, and a provider skips a remote kind it does not know, counted,
never an error; its check of copied output is one function over one
string; and its decision that history stays on its machine now reads
that a record stays where it was made while this item may send its card
under its own table. Where the sync plan's shapes matter below, this plan
**follows the sync plan's final text**; it has one more review round.

1. **The remote kinds `history` (a card, at most 64 KiB) and `skill` (a
   version's manifest and state, at most 32 KiB), and the door
   `PutSkillVersion` (a version's files, at most 1 MiB).** Answered:
   this plan's own, in PR-6 and PR-8 (open source) and PR-12 and PR-13
   (the provider's decoders and builders), which the sync plan's
   decoders leave room for.
2. **A remote record names the person of the device that sent it.**
   Answered: a field a reply may gain, added by this plan's PR-10 and
   read by PR-6.
3. **A remote record says whether it came from one of the receiving
   person's own servers, with a link to open that server.** Answered as
   (2): this plan's PR-10 and PR-12 add `own` and the link, and PR-6
   holds the link to a plain `https` address with no credential in it.
   Until they are in, no card offers **Open studio**.
4. **The field-by-field table.** Answered: a record stays; its card may
   leave under this plan's table ("What leaves a machine, field by
   field"), with the check of copied output over its text.
5. **A card's scope follows its Dispatch project's share.** This plan's
   PR-10, reading the share the sync service already keeps.
6. **The provider passes this item's documents to its own builders.**
   This plan's PR-12 and PR-13.
7. **The check of copied output over any text.** Answered:
   `outputGuard.Check(ctx, text)` is one function over one string
   (`clean`, `output`, `pending`), which PR-9 calls over each title, each
   published file and the notes.

### What this plan shares with the host overview

The host overview (`docs/plans/hosts.md`, #677) and this item both show
other machines through sync and fall back to the local view. HS2 owns
the Hosts page, the `device` and `hosts` records' use, and the host
summary; this plan owns History's and Skills' remote rows. HS2 adopted
both shared pieces at its final check (af94db5c on its branch), with the
same shapes as here, so neither plan is reshaped by the other's order:

1. **When other machines are shown**: `remoteShown()` in `internal/api`
   (`{present, shown, state, since}`) and `utils/remoteShown.ts`
   (`remoteShown(view)` and `remoteWhy(view)`), made by whichever of
   HS2's PR-5 and this plan's PR-6 merges first, with `TestRemoteShown`
   and `utils/remoteShown.test.ts`, and read by the other. At stake:
   PR-6, PR-7, PR-8.
2. **The adapter's computed sources**: `a.computed(src)` with its keys,
   its build at delivery and its revision rule, and
   `a.computedChanged(store, path)`, made by whichever of HS2's PR-5 and
   this plan's PR-5 merges first, with `TestSyncComputedSourceKeys`; HS2
   registers `hosts` with one key, this plan `history` (PR-5) and
   `skills` (PR-9). At stake: PR-5, PR-9.

## Owner decisions

Dated 2026-11-06. Each is a default the owner may overturn with a row in
the Program's status log, its convention for a decision the owner did not
choose (the Program, section 3.1).

1. **No open decision of the Program's is this item's.** O4 (SW1's), O5
   (CG1's) and O8 (until the beta) are other items'; O10 (Dispatch's
   conflict rules) is CS1's, and this item needs none of its rules: a
   card has one writer and a version never changes.
2. **What counts as terminal output is the owner's question at CS1;
   this plan follows its default, and nothing before PR-9 depends on the
   answer.** Under the default, a card's titles (session, crew, member,
   task, check, branch and catalog agent names: one line, at most 60 bytes
   each, each through the check of copied output and the credential
   check) leave, since none can copy
   the default's stretch of output, and a published skill's files and
   notes pass the check of copied output. Under the alternative, only a
   session name a person gave leaves; crew, member and branch names stay;
   publishing is unchanged, since a person publishes every file after
   seeing it and is its writer for this rule. **The rows that change
   under the alternative**: PR-9 (`TestTitlesFollowTheReading` and the
   builder's titles: crew, task, check, member, branch and catalog agent
   names stay, and the remote rows keep their ordinals), PR-10's title
   vectors, PR-12's title rule, and Acceptance part A's step 5 (a crew's
   name is not found on another machine). PR-1 to PR-8 hold under either,
   by I1: before PR-9 no byte of a card was chosen by a person or an
   agent, so there is no free text for the answer to judge
   (`TestCardFromHostileValues`).
3. **A card goes to the workspace only when its project is shared.** A
   session or run of a project the workspace shares (CS1's share, a
   person's act) is the team's history; everything else of a person's
   goes only to their own other servers. Sending every card to every
   member was the alternative, declined: it would show a member's
   unrelated work, hours and machines to people they did not choose to
   share it with (HS2's decision 6 holds the same line).
4. **Sending is on with a provider, and each record or run can be kept
   here.** `history.share` defaults to on, because sharing a project is
   already the choice, and a card holds no words; **Keep on this machine**
   withdraws one card, the switch all of them. Brakeman's turns never
   send one: Brakeman reads under its person's grant.
5. **A session is opened only where it is held.** A card says where; one
   of the person's own servers is opened through the provider's link and
   that server's sign-in; a teammate's session is shared by its holder
   with an ordinary link while it is live; an ended one's record stays
   theirs; while the machine is off, nothing opens. Copying a record's
   words into the hosted service so a teammate could read them was the
   alternative, declined: it is the transcript this item must not move.
6. **Other machines' rows hide when the sync status is not current**, as
   HS2's decision 5, for the same reason: what cannot be refreshed
   misleads; the local rows are the plain build's, and nothing is
   deleted.
7. **The same failure is found by the same command, not by its words.**
   A receipt's argument list leaves only as its SHA-256 (HS1's
   `subject_key`), its repository as the digest of its root commits, and
   its outcome; a digest of the failure's line (which would find "the same
   failure" in another command) was the alternative, declined because a
   digest of a short captured line can be confirmed by guessing it, so it
   would carry output by another name. It can be proposed again with the
   owner's answer in hand.
8. **A repository is the digest of its root commits.** The same on every
   clone, neither a path nor a remote address (which can hold a token).
9. **A version is its files.** Immutable, named by the SHA-256 of its
   files and modes, text only, at most 64 files and 1 MiB, with a marker
   in each place it is put; a label is a name for people, never the
   identity.
10. **Who publishes and withdraws**: a version is published from a
    member's own server by that server's own person (the workbench token,
    as decision 11), for the workspace and as the person the dialog named;
    the service takes it from a workspace's operators and admins only; a
    name's owner is its first publisher, and later versions come from the
    owner or an admin; a reviewer or a guest publishes nothing. A version
    is withdrawn by the name's owner or a workspace admin, signed in to
    the hosted service's own pages; no route on a member's server
    withdraws. Withdrawing stops offering it, removes its received copies
    that no operation waits on, refuses a waiting placement of it, and
    leaves what a person installed in place, marked withdrawn.
11. **Who installs, updates, removes and applies, and how**: this
    machine's own person, by a deliberate act, and nobody else, whatever
    grant they hold: the server's workbench token, from its pages or its
    CLI in the person's own terminal. An identity provider's principal is
    refused every skills write even with the admin grant (a workspace
    admin, a teammate, anyone given the grant on this server), and so are
    a share link's viewer, a session's own token, a host token or a
    hosted session, and Brakeman (`requireOwnPerson`,
    `TestSkillsWritesAreTheOwnPersonsOnly`); the CLI refuses inside a
    session. Placing code under a person's agents is that person's act at
    their own machine, so a person signed in to this server through the
    identity provider reads the Skills page and installs nothing from it.
    Every write names the version by its digest, after its files or its
    difference were shown; never by itself, never at a version's arrival
    or a restore; a place that a session of this server (or a hosted
    session) reads waits until those sessions end, and is placed then
    only if what the person approved still holds; a place changed by hand
    is replaced only when the person, shown the change, says so; a folder
    that is not Conductor's is never touched.
12. **No marketplace.** A version is seen only by the members of the
    workspace it was published in; nothing is listed, searched, rated,
    sold or installed from outside a local folder and that workspace.
13. **No agent installs or publishes a skill.** No MCP tool, no guide
    row, no grant for Brakeman, no session token, and the CLI's writes
    refuse inside a session: a skill is code an agent will run, and the
    person who runs the agent chooses it. That guards against an agent
    following instructions; it is not a boundary against one, which runs
    as its person and can write a skills folder itself (the page then
    shows it as the person's own or modified).
14. **A workflow template is a portable saved crew.** Without its id,
    folder, `where`, origin, yolo and times; with its tasks, checks,
    members' arguments and setup, which New crew from template and Apply
    list apart from the JSON before anything is made (and which pass the
    credential check at publishing); a manifest in the repository stays a
    reference to it. A crew made from one asks for approvals unless its
    person turns that off on the crew. It is applied to a crew only by a
    person, over the crew the person saw, never to a crew whose run is
    not final.
15. **Skills are placed on the server's own machine only**, never under
    a session of this server or a session a host runs on this machine;
    a host's own machine and a Runtime's guest are a later item's.
16. **Names**: the page is **Skills** (`/skills`), with workflow
    templates as its second section; the part of history that leaves is
    a **history card**. "Template" alone stays the compare crew's word.

## Acceptance

The boundary (the Program, section 4.5, TS1): a teammate finds the
session that solved a related problem on another machine.

**Part A, team history, after PR-9, PR-10 and PR-12 and CS1**, by hand,
with two people in one workspace (the owner and a second member), each
signed in on their own server: the owner's on the Mac, the member's in
the desktop app on the Windows machine (its server inside WSL, with no
networking set up by anyone), a project shared with the workspace, and
the same repository cloned on both.

1. On the Mac, a managed run of a crew whose `unit` check fails at its
   first attempt; the second attempt is handed the failure and passes at
   a commit, which is pushed. During it, the owner answers one of the
   agent's questions by typing a marked string, and the session's chat
   holds another.
2. On the Windows machine, the member's own run of the same check fails.
   **Find this elsewhere → On every machine** lists, first, "unit on build
   #2 passed at <commit> · on mac · <owner>" (under the default reading;
   "check 1 of task 2" under the alternative), with the attempt that was
   handed attempt 1's failure.
3. The row opens its card page: the agent, the ticket (opened here, since
   the project is shared), the commits, the tasks and their outcomes, "held
   on mac (<owner>'s machine)", and no link into the Mac. The member
   fetches the commit with git and the check passes.
4. Nothing the Mac's session said or printed reached the Windows machine
   or the service: the marked strings of step 1 are not in the Windows
   server's `remote/` files, its index or the workspace's export.
5. Under the answer given: the session's name and the crew's are found
   with `~` on the Windows machine (the default), or only the name the
   owner gave (the alternative).
6. Mid-session, the sync service stopped by the owner in staging: on the
   Windows machine the History page shows the one line, the local rows
   are unchanged, and the Mac's rows return when the service does, with
   no restart; the same with the subscription lapsed in staging.

**Part B, shared skills and templates, after PR-11 and PR-13**: the owner
publishes a skill (a `SKILL.md` and a script) and a workflow template
from the Mac; on the Windows machine both show as available with their
files, owner and digest; the skill is installed into Claude Code while a
Claude Code session runs there, waits, and is placed when that session
ends; a hand edit shows as **Modified here**; the owner publishes a
second version, which shows as an update with its difference, and the
member's edit is replaced only when they say so; a crew made from the
template runs, its check commands listed before it was made and no
yolo on it. A reviewer member's publish is refused. The member, signed
in to the owner's server through the identity provider, sees its Skills
page and is refused every install there. Then the owner withdraws the
second version on the hosted service's pages: it is no longer offered,
and the member's installed copy of it stays, marked withdrawn. Then the
service stopped: the update is hidden behind the one line and the
installed skill stays placed.

**Part C, the free half, after PR-4**, on a plain build with nothing
registered: import, install, modification, update and removal of a skill
in the agents' directories of the LAN Linux box, the waiting state with a
real Claude Code session, and a workflow template saved and made a crew.

**What Done means.** TS1 is Done when parts A and B have been observed and
recorded in `docs/features.md`'s TS1 entry, not when its rows are merged;
part C is recorded by PR-9 and can be observed on its own. **What needs
the owner**: the review of this plan and of each PR; the answer to CS1's
question on terminal output before PR-9; a second member of a workspace,
the sign-ins and the staging services for parts A and B, and the lapse,
which only the owner can set in staging.

## Changed in review round 1

The independent review's fifteen findings and Codex round 1's
twenty-two, answered on #686:

- **What leaves, field by field** (review 1, Codex 4): one invariant, I1:
  before PR-9 no byte of a card was chosen by a person or an agent. A
  run is a keyed reference (its id begins with the crew's name), tasks
  and checks are ordinals, no crew field, no host name, a catalog agent
  as its adapter, the agent's version in a version's shape, a project's
  key only when the workspace shares it; the table "Every field whose
  source holds words" says for each what leaves before PR-9 and under
  each reading; `TestCardFromHostileValues` builds a card from hostile
  values. Owner decisions 2 names the rows that change under the
  alternative, and PR-1 to PR-8 now hold under either.
- **Who may write skills** (review 2): `requireOwnPerson`, the workbench
  token alone; an identity principal with the admin grant, links, a
  session's token, a host and Brakeman refused; the CLI refuses inside a
  session; withdraw only on the hosted service's pages; Owner decisions
  10, 11 and 13; `TestSkillsWritesAreTheOwnPersonsOnly`.
- **`openURL`** (review 3): a plain `https` address with no credential,
  honoured only on an `own` record, checked at decoding;
  `TestOpenURLCarriesNoCredential`.
- **The host overview** (review 4): HS2 adopted both shared pieces with
  this plan's shapes at its final check; the text now gives the shapes
  and the tests of the one who makes each.
- **Hosted sessions hold** (review 5) and **holds taken before the
  process starts** (Codex 5): the store's holds replace the registry
  snapshot; gate tests for both interleavings.
- **Updates within a lineage** (review 6): **Another skill named ...**
  and a replacement install; `TestUpdateOnlyWithinALineage`.
- **Received versions apart** (review 7, Codex 2): `skills/received/`,
  the provider's, cleared with `remote/`, never exported; an install
  retains first; article 4's text corrected.
- **Templates carry no yolo, and show their commands** (review 8): no
  yolo in the portable form; the template dialog lists arguments, setup
  and checks apart from the JSON.
- **Withdrawal** (review 9): `Settle` refuses a withdrawn version;
  received copies and unpinned retained ones pruned.
- **Guessable digests** (review 10): the trade-off for `subject` and
  `repo` written down beside the table.
- **Invisible characters** (review 11): bidirectional and zero-width
  code points refused (`invisible_character`).
- **No provider, no work** (review 12): the sources are registered only
  with a sync provider; the repository map in memory only, no
  `repokeys.json`; `TestLocalContinuityBuildsNoCard`.
- **Hosted rows say less** (review 13): the hosted rows follow
  OP1's plan for stored records and lapse.
- **Brakeman's history reads** (review 14): other machines' rows only for
  a search that asks; `TestBrakemanSeesNoOtherMachine` in PR-9, after
  #630.
- **PR-1 split** (review 15): PR-1 (versions) and PR-1b (places).
- **Publish requests** (Codex 1, 16, 20): bound to their workspace and
  person, answered by workspace and `op`, delivered by the `skills`
  computed source that every replay lists, **Publish my changes**
  through `Snapshot`, which leaves the marker out.
- **Card revisions** (Codex 3): one counter per source and epoch, raised
  by every delivery; share and project changes mark the cards.
- **Waiting operations keep their approval** (Codex 6): `expect` and
  `seen` kept and checked again at settle, after a restart too.
- **Configuration directories as targets** (Codex 7): a target is a
  directory, from each launch's own environment.
- **Export without operations** (Codex 8): `skills.ExportInstalled`, no
  journal and no waiting.
- **The placement journal** (Codex 9): both hidden folders and both
  digests recorded; recovery at every boundary; I11 stated exactly.
- **Prune's pins** (Codex 10): every place, waiting operation and
  template reference; `skills_full` when the pinned fill the bounds.
- **Template creation once** (Codex 11): `CreateFrom` with a
  `template:<op>` origin; **apply** under the crew store's lock and the
  engine's launch lock (Codex 12).
- **Run cards** (Codex 13, 14, 15): a 64 KiB card filled in a fixed order
  with omission counts; each task's state and standing result; a handed
  receipt named as the engine recorded it.
- **Sizes on every wire** (Codex 17): escaping and base64 accounted;
  manifests 32 KiB; requests 1.5 MiB. **Templates past 256 KiB** (Codex
  18) refused with their size.
- **Dependencies** (Codex 19): PR-10 and PR-12 wait for a tag holding
  PR-9; Acceptance A after PR-9.
- **Brakeman turns by class** (Codex 21), not by a crew id.
- **The waiting state in Playwright** (Codex 22), with the e2e's stub
  under Claude Code's adapter.
- **The sync plan's answers** (lead's note): "What this plan asked of the
  sync plan, and its answers" replaces the waiting list; the kinds, the
  door and the builders are this plan's rows; `outputGuard.Check`.
