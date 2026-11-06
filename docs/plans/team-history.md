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
  an argument. A teammate's server receives the cards of the projects the
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
ID1) and the paid-beta operations plan (Program item OP1), both final and
awaiting the owner. Where one of them changes before it is approved, the
approved text holds and the rows here that use it follow it. What this
item needs of the sync plan that it does not yet give is listed under
"Waiting on the sync plan" and carried by this plan's own rows until that
plan takes it; what it needs of identity and operations is built by this
plan's hosted-service rows (The PRs).

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
  (#616) and the History page PR-7 (#621). PR-5 waits for #615, PR-6 for
  #616 and PR-7 for #621, each named in its row.
- Not on `main`: **CS1** (Program item CS1, Not started; its plan in its
  final check). Its open-source rows give what other machines hold
  (`ext.Host.PutRemote`, `DeleteRemote`, `ClearRemote`, the data
  directory's `remote/`, `GET /api/remote`), the sync status
  (`ext.SyncStatus`, `GET /api/sync`) and the check of text that copies
  captured output. PR-6 and PR-8 wait for the first two (its row called
  OS-4 in `docs/plans/hosts.md`), PR-9 for the third and for the owner's
  answer below. Its hosted rows carry the records between machines: the
  hosted-service and official-build rows here wait for them.
- Not on `main`: **ID1** (identity and workspaces: people, workspaces,
  roles, devices). Nothing in the open-source rows needs it; the hosted
  rows read members, roles and devices from it as CS1 does.

So PR-1 to PR-4 (local skills and workflow templates, free and whole)
start now; PR-5 (history cards out) waits only for HS1; PR-6 to PR-8 wait
for CS1's remote records; PR-9 waits for the owner's answer on what counts
as terminal output (below); PR-10 to PR-13 wait for CS1's and ID1's
hosted rows.

**The open question this plan builds on, and how it is ordered around
it.** The sync plan holds an open question for the owner: what in a
stored record counts as terminal output. Its default, which this plan
follows: terminal output is what Conductor captured (a session's screen,
scrollback and ring, its activity log's words, a check's log, a history
record's words); none of it is ever sent, and any free text that copies a
long stretch of captured output is kept back as if it were output. The
alternative: free text an agent wrote is itself treated as terminal
output, since an agent writes from what its session printed. Under
either, nothing this plan sends before PR-9 carries a word of free text:
the cards of PR-5 hold identifiers, enumerations, times, numbers, commit
ids and digests only, and the skills of PR-1 to PR-8 are sent nowhere.
PR-9 adds the cards' titles and publishing, written for the answer given;
"Owner decisions", 2, says what changes under the alternative.

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
| The sync seam (`ext.ContinuityStore`, `ext.Doc`), the adapter's allowlist, export and restore | C6, `docs/plans/ext.md` | adds one computed source, `history`, beside the data, ledger and Dispatch documents (PR-5), and one allowlisted data path, a publish request (PR-9) |
| Remote records, their doors and files; the sync status | CS1 (its public plan `docs/plans/sync-seams.md`, its row OS-4) | reads them; adds the remote kinds `history` and `skill` and the door `PutSkillVersion` (PR-6, PR-8) if CS1 has not |
| What leaves a machine, field by field; the credential check and the check of copied output; scopes; revocation | CS1 | adds its own table for history cards and skill versions (The changes), held to CS1's rules; asks CS1 for what it lacks (Waiting on the sync plan) |
| When the page shows other machines, and the one line when it cannot | HS2, `docs/plans/hosts.md` (its PR-5) | follows the same rule for History and Skills; one function holds it, made by whichever of HS2's PR-5 and this plan's PR-6 merges first and read by the other (Waiting on the host overview) |
| A workspace's members, roles, devices, sign-ins | ID1 | reads names and roles; adds nothing to identity |
| The paid catalogue, lapse behaviour, export page, retention | OP1 | adds this item's features and its stored bytes in the hosted-service rows |
| A saved crew, its validation, its store, a run's crew as launched | Yardmaster, `internal/crew` | adds a crew's portable form and its `template` reference (PR-3); a run never changes because a crew did |
| The Conductor skill and where each agent reads skills | `internal/agents` | lists the Conductor skill read only; places shared skills beside it with the same "a file that is not ours is the person's" rule (PR-1) |
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
   carries the skill versions and what is installed; it leaves out the
   publish requests and the remote records (the provider's). A member
   removed from the workspace loses every card and offered version of it
   on their machines (CS1's clear), and what they installed stays theirs.
   No approval, answer or launch is made from any page here.
5. **Dependencies point one way.** No new module; `ext` gains records and
   one door, standard library only; the hosted service reads the `ext`
   types at a tag.
6. **No feature build tag.** None added.
7. **The free default is `main`.** With no provider, `local.Continuity`
   drops the cards like every other document (no branch asks whether a
   provider is there: the adapter delivers to whatever fills the slot),
   no remote record is ever written, and the pages are a plain build's.
   A registered provider adds rows; it removes nothing.
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
  first: "unit on build #2 passed at def5678 · on studio · Ada · attempt 2
  was handed build #1's failure". The row opens a **card page**
  (`/history/remote/<device>/<kind>/<id>`) that shows everything the card holds
  (agent, host, project and ticket, repository, commits, tasks, attempts,
  checks and outcomes, times, how it ended) and how to reach the session
  itself (below). The ticket and the commit are the links that go on: a
  shared ticket opens here, and the commit is in git.
- **Skills from the workspace.** A skill or template a member published
  shows as **Available from the workspace: release-notes v3 · owner Ada ·
  published by Ada, 2 days ago**, with its notes and its files; installed,
  it shows **Update available: v4** with the difference from the
  installed version and, when the person changed it, the difference from
  what is on disk too. Publishing (PR-9) shows every file once more before
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
leaves a machine, field by field") that holds, before PR-9, only
identifiers, enumerations, times, numbers, full commit ids and SHA-256
digests:

- **Leaves:** who (the sync service knows the sending device and its
  person; no card names them), the source (the agent's catalog id, name
  and version; the session's kind; whether a host ran it and that host's
  name; the Runtime's `where`), the project (the Dispatch project's key
  and the ticket's key) and the repository (the digest of its root
  commits), the revision (start and end commit ids; an attempt's start
  and result commits; a check's commit), the run's references (run,
  crew, task, attempt and check ids, the attempt a failed check was
  handed to), outcomes and states as enumerations, times, counts, and
  availability (how and when the session ended, or that it was not
  observed to end). From PR-9, titles (the session's name, the crew's,
  the member's, the branch), one line of at most 60 bytes each, under
  the reading the owner chooses.
- **Never leaves:** a session's transcript, screen, scrollback or
  output; its record's entries (the lines Conductor typed, the questions
  with their choices, how each was answered, the chat, the agent's
  reports and errors, its status words); what anyone typed into a
  terminal or a reply box; a question's answer, typed or picked; a file
  path, a folder, a repository's path or remote address; the launch's
  arguments, its variables' names and values, the agent's own session
  id; a check's argument list, log, tail or log digest; a task's
  instructions, a result's summary and issues, a review's verdict and
  findings, the members' messages, checkpoints and blockers; the OS
  user, the host instance, a machine id; anything of a Brakeman turn,
  whose cards are never made.

A card page on another machine therefore says what happened and where,
never what was said. **To open the session itself**, a person goes to the
machine that holds it, under that machine's rules: on one of their own
servers, the card page offers **Open studio** (the provider's link to the
device's Open, which signs them in as that server's admin), and names the
record's path there (`/history/sessions/<id>`); on a teammate's server,
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

**A session's card** (`ext.HistoryCard`, `kind: "session"`), from HS1's
record (`internal/journal`'s `Record`) and nothing else:

| Field | Taken from | Form and bound |
|---|---|---|
| `id`, `rev`, `at` | the record's `ID` and `Seq`; when the card was built | 16 hex; integer; time |
| `sessionKind` | `Kind` | one of the session kinds |
| `agent` | `Agent.ID`, `Agent.Name` (the catalog's), `Agent.Version` | catalog id pattern; one line, at most 60 bytes; at most 64 bytes of `[0-9A-Za-z.+_-]` |
| `hosted`, `hostName` | `Host.Hosted`, `Host.Name` (a hosted session's host only) | bool; one line, at most 64 bytes |
| `where` | `Where` | the Runtime name pattern, or empty for unknown |
| `project`, `ticket` | the Dispatch project and ticket the session belongs to (below) | Dispatch's key patterns |
| `repo` | the digest of the repository's root commits (below) | 64 hex, or empty |
| `start`, `end`, `startFrom` | `Start`, `End`, `StartFrom` | full commit ids; `launch`, `attempt`, `host` |
| `run`, `crew`, `task`, `attempt` | `Crew.RunID`, `Crew.CrewID`, `Crew.Task`, `Crew.Attempt` | the ledger's and the crew store's id patterns; integer |
| `resumedFrom` | `ResumedFrom` | 16 hex |
| `startedAt`, `endedAt`, `lostAt`, `ending`, `exit` | `StartedAt`, `EndedAt`, `LostAt`, how it ended | times; `exited`, `stopped`, `lost`, `host_away` or empty while live; the exit code |
| `questions`, `answered`, `files` | counts of the record's `question` and `answer` entries and of its `Files` rows | integers |

**A run's card** (`kind: "run"`), from the ledger, read in one
transaction (`ledger.CardRows`, PR-5):

| Field | Taken from | Form and bound |
|---|---|---|
| `id`, `rev`, `at` | the run's id and revision | the ledger's patterns |
| `crew`, `ticket`, `project`, `repo` | the run's crew id, its ticket (a run launched from a ticket's plan revision) and that ticket's project; the crew folder's repository digest | id patterns; 64 hex |
| `state`, `outcome`, `paused`, `continuedFrom`, `continuedBy`, `createdAt`, `closedAt` | C6's run pointer, as it is | as the pointer |
| `tasks[]`: `id`, `kind` | each task | the task id pattern; `work`, `review` |
| `tasks[].attempts[]`: `n`, `session`, `start`, `result`, `outcome`, `where`, `handed` | each attempt: its number, its session's id, its start and result commits, its outcome, where it ran, and the check receipt it was handed (`previous.check`, DS2's) as `<check id>/<k>` of the attempt before | integers, ids, commit ids, enumerations |
| `tasks[].checks[]`: `id`, `k`, `attempt`, `subject`, `commit`, `outcome`, `exit`, `at`, `where` | each check receipt: the check's id in the crew, its run number, its attempt, the SHA-256 of its argument list (HS1's `subject_key`), the commit it checked, passed or failed or timed out, the exit code, when, where | the task id pattern; integers; 64 hex; commit id; enumerations; time |
| `omitted` | checks left out by the bound | integer |

At most 128 tasks, 8 attempts a task (the newest) and 256 check receipts
(the newest, the rest counted in `omitted`); a run's card is at most 32
KiB as encoded and a session's at most 4 KiB (`ext.MaxHistoryCard`, 32
KiB). A card that would pass its bound drops its oldest receipts first.

**Titles** (PR-9, under the owner's answer; none before): the session's
`name`, the run's crew `crewName`, a member's `member`, and the record's
`branch`, each one line with its control characters removed and cut to
60 bytes at a character's end. Under the sync plan's default (which this
plan follows) all four leave: a single line of at most 60 bytes cannot
copy a 64-byte stretch or two whole lines of captured output, so the
default's rule cannot fire on any of them; each passes the official
build's credential check (a flagged one sent empty). Under the
alternative, only a session's `name` a person gave (renamed through the
workbench token, or a plain session launched through it with no crew)
leaves, and no crew, member or branch name does.

**Never leaves**, whatever the reading and whatever the owner answers:

| Kept on its machine | Why |
|---|---|
| the record's `Entries` (lines Conductor typed, questions and their choices, answers and how they ended, chat, reports, status words), `Dropped` | the session's words: captured or a person's |
| the session's screen, scrollback, ring, its activity log | terminal output |
| what a person typed into the terminal or a reply box, and any answer to a question, typed or picked | a person's words, which may be a credential (#673's finding) |
| `Files` paths, `Tools`, `Cwd`, `Repo`, `RepoCommon`, the repository's remote | paths and source structure; activity |
| `Launch` (arguments, their count and digest, variables' names and digest, yolo), `Resumes` | the person's command line and environment |
| `Agent.Session` (the agent's own session id and source) | names a file on that machine; resumable only there |
| `Host.User`, `Host.Owner`, a machine id | who and which process, beyond the device the service knows |
| a check's argument list, log, tail and log digest; a task's instructions; a result's summary and issues; a review's verdict and findings; messages; checkpoints; blockers; a manifest step's receipts; worktree paths; the crew as launched; the launch package | the run's words and configuration |
| every card of a Brakeman turn (a record whose `Crew.Brakeman` is set; a run whose crew is a turn's, `brakeman-<n>`), of a record or run kept here, of a switchyard, and of a server with `history.share` off | Brakeman reads under a grant on its person's machine; the person's choice |

**The project and the repository.** A card's `project` is the Dispatch
project of the run's ticket when the run was launched from one, else the
one project whose folder (`projects.cwd`) is in the same repository as the
record's (the repository identity HS1 already keeps, `RepoCommon`,
compared with the project folder's; none when no project or more than one
matches), read from a map the API keeps and refreshes on Dispatch's
project changes. A card's `repo` is the SHA-256 of the repository's root
commit ids, sorted and joined by newlines: the one identity of a
repository that is the same on every clone and is neither a path nor a
remote address. It is read once per repository (`gitcli.RootsOf`: `git
rev-list --max-parents=0 HEAD`, argv, the C locale, 5 s), kept in memory
and in `history/repokeys.json` (at most 1,000 repositories, by
`RepoCommon`, the oldest read let go), read again at most once an hour
after a failure, and only for a folder on this server (`where` native):
a hosted record's card has none, since the server cannot read its host's
repository.

**Who sees a card** is the sync service's rule, not the card's: a card
whose `project` is a project the workspace shares (CS1's share) goes to
every server of the workspace's members; every other card goes only to
its person's own other servers. The card names no scope, so a project
shared later brings its earlier cards to the workspace and one no longer
shared takes them back, by the service's own rule.

**Skills and templates.** What leaves is only what a person publishes
(PR-9): the version's files, their modes, its name, kind and digest, and
the person's notes. The folder it was imported from, the machine's places
and their modifications, and every other version stay here.

### `internal/skills` (new, PR-1): versions, what is installed, and where it is placed

A package of the skills and templates kept on this machine, the standard
library, `internal/store` and `internal/agents` (for the agents' skills
directories) only; no HTTP, no session, no process. The API is its caller.

**The data directory's `skills/`** (0700; files 0600), under its own owner
lock `skills/owner.lock` (tried once, then for `LockWait` as the journal
does; without it `ReadOnly()` says so and nothing is written):

```
skills/
  owner.lock
  versions/<digest>/version.json   one version's manifest
  versions/<digest>/files/<path>   its files, written once
  installed.json                   what this machine installed, and each place's state
  receipts.json                    the operations' receipts, the newest 500
  staging/.tmp-<random>/           a version or a placement being made
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
- a mode of 0644 or 0755, from the source file's owner execute bit; the
  page and the CLI list the executable files apart;
- a skill: a `SKILL.md` at the root, with a front matter whose `name` is
  the version's name and whose `description` is 1 to 1,024 bytes on one
  line (the convention the agents read); a workflow template: a
  `workflow.json` at the root, valid as a portable crew (PR-3), and at
  most a `README.md` beside it.

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
  received from the workspace: the same checks, the digest computed again
  from the bytes and refused when it differs (`ErrDigest`).
- `Get`, `List`, `File(digest, path) ([]byte, error)`: a file read through
  an `os.Root` of the version's `files/`, its SHA-256 checked against the
  manifest on every read; a mismatch is `ErrDamaged` and the version is
  listed `damaged` and never placed.
- `Prune()`: at most 200 names, and per name at most 5 versions besides
  the installed one and one waiting to be placed; the oldest neither
  installed nor waiting go first; at most 256 MiB in all.
- `Diff(a, b []byte) []Hunk` (`diff.go`): a line diff (Myers), each side
  at most 256 KiB and 10,000 lines; past it, "too large to compare" with
  both sizes and digests. The CLI prints it; the web shows both texts in
  the Files tab's diff view (`DiffEditor.vue`), no new dependency.

**Places** (`place.go`). A skill is placed in one or more of the agents'
skills directories, each a **target**: `agents.SkillDirs(home)` (PR-1
adds it to `internal/agents`) gives each directory once with the agents
that read it, from the adapters' `SkillPath`: `claude`
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
  the SHA-256 over what is on disk as the digest rule computes it),
  `missing`, `not_ours`, `waiting` (an operation waits: its sessions),
  `damaged` (the marker cannot be read).
- **Placing** writes the new folder as `<target>/.<name>.conductor-new-<random>`
  through an `os.Root` of the target directory, files then marker, then
  renames the folder in place `.<name>.conductor-old-<random>`, renames the
  new one to `<name>` and removes the old one. `installed.json` records
  each step before it is taken (`placing`, then `swapped`), so `Recover`
  at the next `Open` finishes a placement whose new folder is complete and
  undoes one whose is not; nothing is ever left half-written under
  `<name>`. Removing renames the folder to the old name and removes it,
  recorded the same way.
- **A modified place is the person's.** An update or a removal of a
  `modified` place is refused (`ErrModified`, naming the files) unless
  the operation carries `replace: {target, seen}` for it, the `seen`
  digest of what the person was shown; a place that changed again since
  is refused again (`ErrChanged`).
- **Never under a running session.** `Place` and `Remove` take
  `busy(target) []string`, the caller's answer of which running sessions
  read that directory. A busy target is not touched: its place goes
  `waiting` with those sessions' ids, and `Settle(busy)` (the caller's,
  when a session ends) places or removes every waiting one whose target
  is no longer busy, in the order they were asked. `Cancel(name)` ends a
  waiting operation, the place left as it was. Settling completes only an
  operation a person made; nothing else ever writes a place.

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
	Target  string   `json:"target"`  // claude | codex | agents | agy, or "config:<agent>" (below)
	Digest  string   `json:"digest"`  // what is placed there
	Step    string   `json:"step,omitempty"` // placing | swapped | removing, during a placement
	Waiting *Waiting `json:"waiting,omitempty"`
}
type Waiting struct {
	Op       string    `json:"op"`
	Digest   string    `json:"digest"` // "" for a removal
	Since    time.Time `json:"since"`
	Sessions []string  `json:"sessions"`
}
```

**Templates' crews** (`made.go`, PR-3): `skills/made.json` maps a crew id
to the template version it was made from (`{name, digest}`), so the crew
file's schema does not change and an older Conductor still reads every
crew. A crew deleted is dropped from it at the next read.

**Receipts.** Every write (import, install, update, remove, cancel, save
as template, new crew from template, apply, publish) is an operation with
the client's `op` id (`^[A-Za-z0-9_-]{8,64}$`); its receipt `{op, what,
name, digest, state, places, at, actor}` is kept (the newest 500), and the
same `op` sent again answers that receipt and does nothing (the
Program, section 1.5).

`internal/agents` (PR-1): `SkillDirs(home string) []SkillDir` (the
targets above, from each adapter's `SkillPath`, so a new adapter that
reads skills is a target with no change here), and `ConductorSkill()`
(the Conductor skill's text and SHA-256, for the page's read-only row).
An agent launched with a configuration directory of its own (`ConfigDirOf`,
#291) reads skills there: a skill installed for that agent's target is
placed there too, as target `config:<agent>`, at the session's launch,
before the agent starts and only when no other running session uses that
directory, by the same `Place`; the launch never waits for it.

### `internal/api`, `internal/cli` (PR-2): the routes, the sessions that hold a place, the CLI

`internal/api/skills.go` opens the store with the data directory (none
without one; never on a switchyard, which serves no workbench) and
answers, every route `requireAdmin` (the workbench token: a share link's
viewer, a session's own token and Brakeman's principal reach none), `404
skills_unavailable` without a data directory or on a switchyard, `409
skills_read_only` for a write while another server holds the lock:

| Route | Body → answer | Bounds |
|---|---|---|
| `GET /api/skills` | → `{skills: [Row], templates: [Row], conductor: {sha256, places}, targets: [{id, dir, agents}], readOnly?}`; a row: name, kind, the installed version (label, digest, origin), its places' states, the versions kept, a waiting operation with its sessions' names | reply at most 1 MiB |
| `GET /api/skill-versions/{digest}` | → the version's manifest | 16 KiB |
| `GET /api/skill-versions/{digest}/file?path=` | → `{path, mode, text}` | 256 KiB |
| `GET /api/skills/{name}/compare?from=&to=` | `from`: a digest or `disk:<target>`; `to`: a digest → per file `{path, change: added \| removed \| changed \| same, from?, to?}` with both texts | reply at most 2 MiB, `truncated` past it |
| `POST /api/skills/import` | `{op, path, kind, name?, label?, notes?}` → `201 {version, receipt}`; `400 invalid_skill` naming the file and the rule | body 4 KiB |
| `POST /api/skills/{name}/install` | `{op, digest, targets, expect?, replace?}` → `{receipt}`, each place `placed` or `waiting` with its sessions; `409 version_changed` when `expect` (the installed digest the person saw) is not the installed one; `409 place_modified`, `409 place_changed`, `409 skill_not_ours`, naming the place | body 8 KiB |
| `POST /api/skills/{name}/remove` | `{op, targets, replace?}` → `{receipt}` | body 8 KiB |
| `POST /api/skills/{name}/cancel` | `{op}` → `{receipt}` | body 1 KiB |
| `DELETE /api/skill-versions/{digest}` | → `204`; `409 version_in_use` while installed or waiting | |

Every body is read with `DisallowUnknownFields` and its limit. An update
is an install of another digest with `expect`.

**The sessions that hold a place** (`skillsBusy`): the running sessions
this server runs (the registry's server sessions, whatever their `where`:
a Runtime other than native is counted too, the cautious side) whose
catalog agent's adapter reads that target's directory, or whose
configuration directory is a `config:` target. A hosted session runs on
its host's machine and reads that machine's skills, not this one's, and
is not counted. `Settle` is called on a goroutine of the server's own,
coalesced, when the registry removes a session or a session's status
ends; never on the hook's goroutine. Sessions Conductor does not run (an
agent started in a shell) cannot be seen: the install dialog says that
those read the new files at their next skill load.

**The CLI**: `conductor skills list | show <name> | import <dir>
[--template] [--name] [--label] [--notes] | install <name> <label|digest>
[--agents claude,codex] | update <name> <label|digest> | remove <name> |
compare <name> [<from>] [<to>] | cancel <name>`, with the workbench
token, printing the receipt; `import` sends the folder's path, which the
server opens. Completion and usage text. There is no MCP tool and no
operating-guide row for agents: a skill is code an agent runs, and an
agent installing its own is out of bounds (Owner decisions, 13).

**The export** (`internal/export/leftout.go`): `skills/` is carried as
the data directory's other files are, versions and `installed.json` with
it; `skills/staging/` is `WhyTemporary` and `skills/owner.lock`
`WhyLock`. A restore installs the versions and the record of what was
installed, and places nothing: on a new machine every place shows
`missing` until the person installs it there (Owner decisions, 11).

`docs/protocol.md` gains a Skills section with every route and bound
above; `docs/repository-map.md` the `internal/skills` row.

### `internal/crew`, `internal/skills`, `internal/api`, `internal/cli` (PR-3): workflow templates

- **The portable crew** (`internal/crew/portable.go`): `type Portable
  struct` with the crew's name, goal, isolation, yolo, open-after-launch,
  view link time, members (name, agent id, prompt, arguments, start),
  tasks (id, member, kind, instructions, needs, checks), setup and an
  inline manifest; never the crew's `id`, `cwd`, `where`, `origin`,
  `createdAt`, `updatedAt` or `manifestRevision`. A manifest kept in the
  repository (`file: true`) stays a reference to the repository's
  `conductor.manifest.json`, which git already shares. `PortableOf(c)`,
  `FromPortable(p, cwd) (Crew, error)` (validated by the crew's own
  `Validate` once the folder is given), and `PortableDigest(p)` (SHA-256
  of its canonical JSON). A template's `workflow.json` is a `Portable`
  with `v: 1`, read with `DisallowUnknownFields`, at most the crew's own
  bound (`MaxEncoded`).
- `POST /api/crews/{id}/template` `{op, name, label?, notes?}` → `201
  {version}`: **Save as workflow template**, a version of kind `workflow`
  made from the crew's portable form (`skills.ImportBytes`, the same
  checks as a folder's).
- `POST /api/skills/{name}/crew` `{op, digest, cwd, name?}` → `201
  {crew}`: **New crew from template**: the folder through `resolveCwd`, a
  saved crew made by the store's `Create` from `FromPortable`, and the
  crew recorded in `skills/made.json`.
- `POST /api/skills/{name}/apply` `{op, crew, digest, expect}` → `{crew}`:
  a person applies a template version to a crew made from it: refused
  `409 crew_running` while a run of that crew is not final, `409
  crew_changed` when the crew's portable digest is not `expect` (what the
  person was shown), and otherwise the crew's portable fields replaced by
  the store's `Update`, its id, folder, `where` and `origin` kept. A run
  already made keeps the crew as it was launched, as every run does.
- `GET /api/skills` gives a template's row its crews: each crew made from
  it with `changed` (its portable digest differs from the version's) and
  the template version it was made from.
- `conductor skills template <crew> <name>`, `conductor skills crew <name>
  <cwd>`, `conductor skills apply <name> <crew>`.

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
  `SkillCompare.vue`, `TemplateCrewsList.vue`.
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
  `HistoryCheck`, `MaxHistoryCard` (32 KiB), and `Valid()`, which checks
  every string against its pattern and refuses any field PR-9 has not
  added. Standard library only.
- **A keyed computed source in the adapter** (`internal/api/sync.go`).
  The adapter today delivers what a store committed, read at delivery
  from where it was committed. A card has no commit of its own, so the
  adapter gains a source whose documents are built at delivery: store
  `history` (`syncHistory`), paths `session/<id>` and `run/<id>`, each
  with its build function (`historyCard(path)`). HS2's PR-5
  (`docs/plans/hosts.md`) adds one computed document of the same kind,
  the host summary; whichever of the two merges first makes the
  mechanism (a store name, marked keys, a build at delivery), and the
  other uses it (Waiting on the host overview, 2).
  - **Marked**: by the journal's `OnWrite(id, seq)` and `OnForget(id)`
    (PR-5 adds both to `internal/journal`, called after the write or the
    removal has committed, never waiting: the adapter's `mark` takes only
    its own lock), and by the ledger's `OnCommit` and `OnPrune`, whose
    callbacks the adapter already installs for the run pointer and which
    now mark the run's card beside it.
  - **Delivered**: built from the record as it then is (`journal.Get`) or
    from the ledger (`ledger.CardRows(ctx, run)`, PR-5, one read
    transaction over the run's tasks, attempts and check receipts); a
    record or run that is gone, kept here, a Brakeman turn's, or now out
    of the table's reach is `Deleted`. A card that fails its own `Valid`
    is never delivered: the adapter logs its id and delivers `Deleted`,
    and `TestEveryCardBuiltIsValid` holds that none does.
  - **Revisions**: a session card's `Rev` is the record's `Seq`, which
    never goes back (HS1's `history/state.json`); a run card's is the
    run's ledger revision, and a pruned run's `Deleted` takes the
    revision after its last, as C6's pointer does. Both rise with what
    they describe in every epoch.
  - **Replayed**: every replay lists, after the run pointers and before
    Dispatch's `log` doc, every record the journal holds and every run
    the ledger holds (paged by `syncPage`), so each epoch delivers the
    whole set and a card no longer listed is dropped by the store.
  - **Not registered** on a switchyard, without a data directory, while
    the journal is read only (the server that holds it sends), or with
    `history.share` off.
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
  repository map and its file (`history/repokeys.json`) in
  `internal/api/history_card.go`, with the project map.
- `ext/exttest`: the `Continuity` check's samples gain a `history` doc
  (a valid card), and `samples.go` a card at its bound.
- `docs/protocol.md`'s sync section: the `history` store, its paths, its
  revisions and the card's fields and bounds; `ext.Doc.Store`'s comment
  names `history`.

### `internal/history`, `internal/api`, `ext` (PR-6): other machines' cards in the index

- **The remote kind `history`.** CS1's remote records (`ext.Remote`,
  written by the provider through `Host.PutRemote` into
  `remote/<device>/<kind>-<id>.json`) gain the kind `history`, whose
  `Data` is an `ext.HistoryCard` as its server built it, `Valid` on
  arrival (a card that fails is refused by the door). If CS1's row has
  added it, this PR only reads it.
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
  only ("Claude Code session · build attempt 2 · CND-12", "unit on build
  #2 · passed at def5678"); its body is empty.
- **Search.** A new filter, `on:` (`on:here`, or a device's name; a comma
  for alternatives). `Similar` gains the scope `workspace`: the receipts
  of the same `subject_key` with the same `repo_key` on every origin,
  ranked as HS1 ranks them (passed before failed, newest first), this
  machine's first. The History routes' `Entry` gains `origin: {device,
  deviceName, person, reportedAt}` and the availability `remote` ("on
  studio · reported 14:05").
- **When other machines are shown** (`remoteShown()`): `sync` in the
  capabilities with the state `ready`, and the sync status current, or
  behind for less than 10 minutes, and current at least once in this
  server's life, and not signed out (HS2's rule, Waiting on the host
  overview, 1). Otherwise every search adds `origin = ''`, `on:` answers
  nothing for another device, the `workspace` scope is this machine's,
  and the reply's `remote: {state, since}` says why; nothing is deleted,
  so the rows return with the next current status, with no reindex.
- `GET /api/history/remote/{device}/{kind}/{id}` (admin): the card whole,
  with `deviceName`, `person`, `own` and `openURL` as the remote record
  carries them (Waiting on the sync plan, 2 and 3); `404` while not
  shown or gone.
- `docs/protocol.md`: the filter, the scope, the entry's `origin`, the
  route; `web/app/utils/protocol.ts` the types.

### `web/app` (PR-7): other machines in History

- The palette's history rows and the History page draw another machine's
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
  own server, **Open studio** (the `openURL`) and the record's path there;
  for a teammate's, no link; "studio last reported at 14:05".
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
  []SkillFile}`, `SkillManifest {Kind, Name, Label, Digest, Notes, Owner,
  Publisher, PublishedAt, Withdrawn, Files []SkillFileInfo}` (at most 16
  KiB), `SkillFile {Path, Mode, Text}`, `MaxSkillVersion` (1 MiB of
  files), `MaxSkillFiles` (64), `MaxSkillFile` (256 KiB), each with
  `Valid` holding PR-1's rules.
- **`Host.PutSkillVersion(ctx, v SkillVersion) error`**, a door for the
  sync store alone (any other provider's `Host` answers
  `ErrUnsupported`): the version handed to `skills.Store.Put` with
  `origin: workspace`, its digest computed again from the files and
  refused on a mismatch. It installs nothing.
- **The remote kind `skill`**: a version's `SkillManifest` and its state
  (`published`, `withdrawn`, or the answer to this server's own publish
  request, PR-9), at most 16 KiB. The Skills reply gains `available`:
  each workspace version whose files this server holds, by name, newest
  first, with its owner, publisher, time, notes and state, and for an
  installed name `update: {from, to}`; only while `remoteShown()`, and
  the reply's `remote: {state, since}` otherwise.
- **An update from the workspace** is an install of the newer digest
  with `expect`, exactly as a local one: the person's operation, shown
  first. A withdrawn version is not offered; one installed stays
  installed and says "withdrawn by its owner".
- The page: **Available from the workspace** under each section, the
  update banner on an installed row, the owner and publisher in every
  version's line, the one line when not shown.
- `ext/exttest`: `FakeHost` records `PutSkillVersion`; the samples gain a
  version at its bounds.
- `docs/protocol.md` and `protocol.ts`: the reply's `available`, the
  remote kind, the door's bounds.

### `internal/api`, `internal/skills`, `web/app`, `docs` (PR-9): publishing, the cards' titles, the features entry

Cut after the owner's answer to the sync plan's question and once CS1's
row that adds the check of copied output is on `main`.

- **Publish** (`POST /api/skills/{name}/publish` `{op, digest, notes}`,
  admin; `409 sync_unavailable` unless `remoteShown()`): writes the
  request, `skills/outbox/<digest>.json` (`{v, op, kind, name, digest,
  notes, files: [{path, mode, text}]}`, at most 1.25 MiB, at most 8
  waiting), which the adapter's allowlist gains
  (`^skills/outbox/[0-9a-f]{64}\.json$`) and delivers in parts as any
  document past `ext.MaxDocData`. Before it is written, every file and the
  notes pass the check of copied output under the reading the owner
  chose (`422 quotes_output`, naming the file and its line); the
  credential check is the official build's and the service's. The request
  is removed when a `skill` remote record answers its digest (published
  as `vN`, or refused with the service's words, shown on the page). A
  person may publish an installed place they modified: **Publish my
  changes** imports the place's folder as a local version, then publishes
  it. The publish dialog lists every file and its size once more, the
  notes, and "Visible to the members of <workspace>, and no one else."
- **Titles on the cards**, under the answer: the builder fills `name`,
  `crewName`, `member` and `branch` as the titles row above says, `Valid`
  admits them, and `TestTitlesFollowTheReading` holds the rule.
- `docs/features.md`: TS1's entry (what a plain build gains, what a
  workspace adds, each PR's checks, the by-hand record of Acceptance);
  `docs/repository-map.md`'s rows; `docs/protocol.md`'s publish route and
  the outbox path; `internal/export`'s `leftOut` names `skills/outbox/`
  (`WhyTemporary`: a request for the provider, never restored).

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
  workspace's export gains the cards; retention and deletion follow OP1's
  rules for stored records; the paid catalogue gains this item's feature
  for team history, which keeps what is stored readable through a lapse
  and refuses new cards; its stored bytes reported through OP1's usage
  contract; titles (from PR-9's tag) under the credential check, refused
  when flagged. Its versioned wire contract gains the card with its
  vectors: a card at every bound, every field of "Never leaves" planted
  as an unknown field and refused, a credential-like title refused.
- **PR-11, shared skills**: a store of versions per workspace, each
  immutable and named by its digest, recomputed on arrival; a name's
  owner is the person who first published it, and later versions are
  published by the owner or a workspace admin only, never by a reviewer
  or a guest; labels `v1`, `v2`, … given by the service in order; the same
  digest published again is answered with the version it is; every file
  and the notes under the credential check, a flagged version refused
  whole with the file and line named; withdraw by the owner or an admin;
  versions and their manifests delivered to the server devices of the
  workspace's members (`skill` records and the files), never listed,
  searched or served to anyone outside the workspace; at most 200 names
  and 20 versions a name kept per workspace; the export gains them; the
  paid catalogue gains this item's feature for shared skills, with the
  same lapse behaviour; its contract with vectors (a version at its
  bounds, a binary file refused, a digest mismatch refused, a publish by a
  reviewer refused).

### The official build's PRs (PR-12, PR-13)

In the official build's repository, in words. Both add to CS1's sync
provider and carry the outage and lapse tests (article 8).

- **PR-12, team history**: the provider takes `history` documents from
  the adapter, decodes each into its own type field by field (the table
  above; an unknown field refused and logged, never sent), runs the
  credential check on titles (sent empty when flagged), queues them in its
  outbox and sends them; hands received cards to `Host.PutRemote` with
  the device's name, the person's name, `own` and the device's open link,
  and deletes them as the service says; and reports nothing new in the
  sync status. Tests: the outage (the service unreachable mid-session:
  History's search answers `remote: unreachable` and this machine's rows
  unchanged, back with no restart), the lapse (the same with `lapsed`,
  nothing local removed), a card with a planted unknown field never sent,
  and a Playwright run of the official build against its fake sync
  service showing another machine's row in the palette, its card page,
  and the one line once the service is stopped.
- **PR-13, shared skills**: publish requests from the outbox document to
  the service, their answers as `skill` records; received versions handed
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
| `skills/versions/<digest>/` | one version: its manifest and files, written once by a rename of a staged directory | `skills.Store` (Import, Put) | yes |
| `skills/installed.json` | per name, the version installed and each place's digest, step and waiting operation | `skills.Store` | yes; a restore places nothing |
| `skills/made.json` | which crew was made from which template version | `skills.Store` (PR-3) | yes |
| `skills/receipts.json` | the newest 500 operations' receipts | `skills.Store` | yes |
| `skills/outbox/<digest>.json` | a publish request until the service answers it | the publish route (PR-9) | no (`WhyTemporary`) |
| `skills/staging/`, `skills/owner.lock` | a version or placement being made; the lock | `skills.Store` | no |
| `<target>/<name>/` in an agent's skills directory | a placed version's files and its marker | `skills.Store.Place`, only | no: outside the data directory |
| `history/share.json`, `history/repokeys.json` | records and runs kept here; repositories' root digests | the API (PR-5) | `share.json` yes; `repokeys.json` no (derived) |
| `history/index.sqlite`, schema version 2 | HS1's entries, and other machines' cards as entries | the indexer | no (HS1's rule) |
| `remote/<device>/history-*.json`, `remote/<device>/skill-*.json` | other machines' cards; the workspace's versions' manifests and states | the provider, through CS1's doors | no (CS1's rule) |
| the sync service | cards per device; versions per workspace | the hosted service | the workspace's export |

**Placing a skill** (one place; the steps recorded in `installed.json`
before each is taken):

```
(none) --install--> placing --(new folder complete)--> swapped --(old removed)--> placed
placed --install other digest--> [busy?] waiting --(Settle: not busy)--> placing --> ... --> placed
placed --remove--> [busy?] waiting --(Settle)--> removing --> (none)
waiting --cancel--> as it was before the operation
placed --(a file changed by hand)--> modified  (seen by Inspect; nothing written)
modified --install or remove without replace{seen}--> refused, place_modified
```

`Recover`, at `Open`: a place at `placing` whose new folder holds every
file and the marker is finished (`swapped`, then `placed`); one whose new
folder is short is undone (the new folder removed, the old one kept); one
at `swapped` removes its old folder. A `waiting` operation outlives a
restart and is settled at the next session end or at `Open`, against the
sessions running then.

**A history card** is marked when its record or run commits and built
when it is delivered, so the newest state goes out, and a record forgotten
between the mark and the delivery is delivered as `Deleted`. Within an
epoch the card's revisions rise (I4). A replay lists them all; a store
drops what a completed replay did not list (C6).

**A publish request** (PR-9): written to the outbox after the copied-output
check passed; delivered; answered by a `skill` remote record naming its
digest (`published` with its label, or `refused` with words); the outbox
file removed at the answer. Unanswered, it stays, at most 8, and the page
says "waiting for the sync service since 14:02".

**Conflict rules.** None are needed, and none are made: a card has one
writer, the server that holds the record or the run, and the service keeps
its newest revision per (device, kind, id) in an epoch; a version is
immutable, so two versions never merge, and two publishes of one name are
ordered by the service, which refuses one from a person who is neither the
name's owner nor a workspace admin. Dispatch's own rules (CS1's) are
untouched.

**Invariants**, each with the test that holds it:

- **I1, a card holds only the table.** No card field is read from a
  record's entries, files, tools, launch, resumes, folder, repository
  path, branch or name before PR-9, nor from its host's user or owner or
  the agent's own session; no run card field from a check's argument
  list, log, tail or digest, a task's instructions, a result's words, a
  review's findings, a message, a checkpoint or a blocker
  (`TestACardCarriesNothingTheRecordHeard`,
  `TestARunCardCarriesNoWordsOfTheRun`, `TestHistoryCardFieldsAreTheTable`).
- **I2, some things never leave.** No card of a Brakeman turn, of a record
  or run kept here, from a switchyard, from a read-only journal, or with
  `history.share` off (`TestBrakemanTurnsSendNoCard`,
  `TestKeptHereIsDeleted`, `TestShareOffSendsNoCard`).
- **I3, built at delivery.** A card is built from the record as it is
  when delivered; a forgotten or pruned record is `Deleted`
  (`TestForgottenRecordsCardIsDeleted`).
- **I4, revisions rise.** A session card's `Rev` is its record's `Seq` and
  a run card's its ledger revision; a pruned run's delete takes the next
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
- **I8, never under a running session.** A target a running session of
  this server reads is not written; its operation waits and is settled
  only when no such session runs (`TestPlacementWaitsForRunningSessions`,
  `TestSettleAfterTheLastSessionEnds`, `TestNewSessionKeepsItWaiting`).
- **I9, only a person's operation writes a place or a crew.** A received
  version, a settle and a restore change nothing installed by themselves
  (`TestReceivedVersionInstallsNothing`, `TestRestorePlacesNothing`); a
  template is applied to a crew only by a person, never to one whose run
  is not final (`TestApplyRefusedWhileRunning`, `TestApplyNeedsExpect`).
- **I10, no agent reaches skills.** A session's own token, a share link
  and Brakeman's principal are refused by every skills route; no MCP tool
  and no guide row exists (`TestSkillsRoutesAreAdminOnly`,
  `TestGuideHasNoSkillsRow`).
- **I11, a half placement never shows.** A crash at any step leaves the
  old version or the new one whole under `<name>`
  (`TestRecoverFinishesOrUndoes`, by a seam that stops after each step).
- **I12, an outage or a lapse removes nothing local.** History, versions,
  places and the outbox stay as they are (the official build's outage and
  lapse tests; `TestRemoteRowsHiddenThenBack` here).

**Migrations.** The history index goes to schema version 2: an index of
version 1 is cleared in place and rebuilt from the stores and the remote
files, HS1's rule for an index of another version; an older Conductor
that finds version 2 answers `other_version` and does not read it, as
HS1's readers do, and rebuilds it to version 1 when it next holds the
lock. `skills/` is new and has no earlier form. No ledger or Dispatch
schema changes (`ledger.CardRows` reads). No crew file changes: a crew's
template is kept in `skills/made.json`, so every older Conductor still
reads every crew.

**Protocol.** No frame or control message: `internal/proto` is not
touched. The HTTP routes (Skills, the history share route, the remote
card route, the filter and scope) are in `docs/protocol.md` with every
field and bound, and in `web/app/utils/protocol.ts` from the PR whose
page calls them, each with a test; the sync documents (`history` store,
`skills/outbox/` path) and the remote kinds (`history`, `skill`) in
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

**`internal/skills` (PR-1)**: `TestImportChecksEveryRule` (one case per
rule: count, size, path, case-only twins, link, device, binary, NUL,
control character, mode, a `SKILL.md` without its front matter or with
another name, a template without `workflow.json`), `TestDigestIsTheFiles`
(the same files under two labels and two folders are one digest; a mode
changed is another), `TestVersionIsImmutable`,
`TestDamagedVersionIsNeverPlaced`, `TestNotOursIsNeverTouched`,
`TestLinkedFolderIsNotOurs`, `TestModifiedNeedsTheSeenDigest`,
`TestPlacementWaitsForRunningSessions`, `TestSettleAfterTheLastSessionEnds`,
`TestNewSessionKeepsItWaiting`, `TestCancelLeavesThePlace`,
`TestRecoverFinishesOrUndoes` (a seam that stops after each step, then
`Open`), `TestReadOnlyWithoutTheLock`, `TestPruneKeepsInstalledAndWaiting`,
`TestReceiptAnswersTheSameOp`, `TestDiffBounds`.
**`internal/agents` (PR-1)**: `TestSkillDirsFromEveryAdapter` (every
adapter with a `SkillPath` gives its directory once).

**`internal/api` (PR-2)**: `TestSkillsRoutesAreAdminOnly`,
`TestImportOpensTheFolderOnce` (the folder through `openCwd`, a folder
outside the allowed roots refused, a link swapped in after the check not
followed), `TestInstallUpdateRemoveReceipts`, `TestInstallRefusesAStaleExpect`,
`TestBusyCountsTheAgentsThatReadTheTarget` (a Claude Code session holds
`claude`, a pi session holds `agents`, a hosted session holds nothing, a
session with its own configuration directory holds its `config:` target),
`TestSettleRunsOffTheHook`, `TestSkillsUnavailableOnASwitchyard`,
`TestGuideHasNoSkillsRow`, `TestExportCarriesVersionsAndRestorePlacesNothing`
(`internal/export` too); **`internal/cli`**: `TestSkillsCommands` (each
command's request and printed receipt), completion's rows.

**`internal/crew`, `internal/api` (PR-3)**: `TestPortableDropsTheMachine`
(id, folder, `where`, origin, times and manifest revision gone; a file
manifest kept as a reference), `TestFromPortableValidates`,
`TestTemplateRoundTrip` (save, new crew in another folder, the crews
equal but for the machine's fields), `TestApplyRefusedWhileRunning`,
`TestApplyNeedsExpect`, `TestMadeFromSurvivesAndForgetsDeletedCrews`,
`TestOlderCrewFilesUnchanged` (no crew file gains a field).

**`web/app` (PR-4)**: vitest `skills.test.ts` (each state's words and
color, which actions each row offers, the waiting line with names) and
`useSkills.test.ts` (an `op` id kept until its receipt, sent again on a
retry); Playwright `web/e2e/skills.spec.ts`, the e2e server's own HOME:
import a fixture skill folder made by the spec under a name of its own,
install it into `agents`, see **Placed**, change its `SKILL.md` on disk
and see **Modified here** with its difference, update to a second
imported version and be refused until **Replace my changes**, remove it;
save a crew the spec makes as a workflow template, make a crew from it
in another folder, change the crew and see **Changed**. The waiting state
needs a running session of an agent whose adapter reads a skills
directory, which the e2e's stub agent is not: it is held by the Go tests
above and by vitest, and the spec's header says so.

**PR-5 (`internal/api`, `internal/journal`, `internal/crew/ledger`,
`internal/gitcli`, `ext`)**: `TestACardCarriesNothingTheRecordHeard` (a
recording `ContinuityStore` registered; a session whose record holds a
marker in every entry kind, a typed answer sent through `ext.Host`'s
`Submit` with a question and through the host's `answer_as`, a marker in
its launch's arguments and variables' names, its folder, a file path, a
tool's name, its host's user and its agent's session id; across a
replay and every later delivery, no document's bytes hold a marker),
`TestARunCardCarriesNoWordsOfTheRun` (the same for a run: argv, log,
tail, instructions, result words, findings, a message, a checkpoint),
`TestHistoryCardFieldsAreTheTable` (`ext`: reflection over the card's
types against the table's patterns), `TestEveryCardBuiltIsValid`,
`TestHistoryCardRevisionsRise`, `TestForgottenRecordsCardIsDeleted`,
`TestBrakemanTurnsSendNoCard`, `TestKeptHereIsDeleted`,
`TestShareOffSendsNoCard`, `TestLocalContinuityDropsCards` (with nothing
registered the cards reach `local.Continuity`, which keeps nothing, and no
file of the data directory changes), `TestCardProjectAndRepo`
(a ticket's run, a plain session in a project's repository, two projects
in one repository giving none, a hosted record with no `repo`),
`TestRepoKeyIsTheRootCommits` (`internal/gitcli`: two clones, one key; a
repository with two roots), `TestCardBoundDropsOldestReceipts`;
`internal/journal`: `TestOnWriteAfterCommit`, `TestOnForgetAfterRemoval`;
`internal/crew/ledger`: `TestCardRowsOneTransaction`; `ext/exttest`: the
`Continuity` check's new sample passes against `local.Continuity` and the
fake.

**PR-6 (`internal/history`, `internal/api`, `ext`)**: `TestSchemaTwoRebuildsInPlace`
(a version 1 index cleared and rebuilt; a version 1 reader answering
`other_version`), `TestRemoteKindsAndClasses` (in
`TestClassComesFromTheKindAlone`'s table), `TestRemoteSourceFollowsTheDoors`
(put, delete and clear, each reflected at the next pass),
`TestRemoteCardRefusedWhenInvalid`, `TestOnFilter`,
`TestSimilarAcrossMachines` (the same argv digest in the same repository
on two devices, passed first; another repository's left out),
`TestRemoteRowsHiddenThenBack` (a fake sync status: current, unreachable,
lapsed, current again; nothing reindexed), `TestRemoteCardRoute`.

**`web/app` (PR-7)**: vitest `historyRemote.test.ts` (a remote row's
badges and words, the card page's sections, own and a teammate's, the
line for each state); Playwright `web/e2e/history-remote.spec.ts`: on a
plain build with `remote/` seeded with a card before the server starts,
no other machine's row appears in the palette or on the History page, no
**On every machine** is offered, and the card route answers `404`; the
section's presence is the official build's Playwright run (PR-12), since
only a registered provider makes it appear.

**PR-8 (`internal/skills`, `internal/api`, `ext`, `web/app`)**:
`TestPutSkillVersionOnlyForTheSyncStore`, `TestPutRefusesAWrongDigest`,
`TestReceivedVersionInstallsNothing`, `TestAvailableOnlyWhenShown`,
`TestWithdrawnStaysInstalled`; vitest for the available list and the
update banner; the Playwright spec extended: seeded `skill` records show
nothing on a plain build.

**PR-9 (`internal/api`, `internal/skills`, `web/app`)**:
`TestPublishWritesTheOutboxOnlyWhenShown`, `TestPublishRefusesCopiedOutput`
(a file quoting a check's log under the reading chosen),
`TestOutboxClearedByTheAnswer`, `TestOutboxBound`, `TestPublishMyChanges`,
`TestTitlesFollowTheReading` (each title's source and bound; under the
answer given, what leaves), and `TestACardCarriesNothingTheRecordHeard`
run again with titles on; vitest for the publish dialog.

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
2. **`session.Local` drives a `session.Process`.** Not touched: the
   sessions that hold a place are read from the registry's `Info`, never
   from a pid or a tty.
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
8. **`hostagent` is a launch site as much as `api`.** Not touched: skills
   are placed on the server's machine only, and a host's sessions hold no
   place there.

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
| PR-1 | open source | skills: versions, their checks and digest, places with their marker, modifications, never under a running session, recover | `internal/skills` (new), `internal/agents` | none (`main` as it is) | PR-5 | ~750 + 750 |
| PR-2 | open source | api, cli, export: the Skills routes, the sessions that hold a place, `conductor skills` | `internal/api`, `internal/cli`, `internal/export`, `docs` | PR-1 | none | ~650 + 700 |
| PR-3 | open source | crew, skills, api, cli: workflow templates (the portable crew, save as, new crew from, apply) | `internal/crew`, `internal/skills`, `internal/api`, `internal/cli`, `docs` | PR-2 | none | ~450 + 550 |
| PR-4 | open source | web: the Skills page, its install and compare dialogs, templates on the crew pages | `web/app`, `web/e2e` | PR-3 | PR-5 | ~800 + 600 |
| PR-5 | open source | api, journal, ledger, gitcli, ext: history cards to the sync store, `history.share`, kept here | `internal/api`, `internal/journal`, `internal/crew/ledger`, `internal/gitcli`, `internal/config`, `ext`, `ext/exttest`, `docs` | HS1's PR-3 (#615) on `main` (after #613 and #614) | PR-1, PR-4 | ~650 + 750 |
| PR-6 | open source | history, api, ext: other machines' cards in the index (schema 2), `on:`, `Similar` across machines, when they are shown | `internal/history`, `internal/api`, `ext`, `web/app/utils/protocol.ts`, `docs` | PR-5; HS1's PR-4 (#616); CS1's remote records and sync status on `main` | none | ~700 + 750 |
| PR-7 | open source | web: other machines in History, the card page, the one line, keep here | `web/app`, `web/e2e` | PR-6; HS1's PR-7 (#621) | none | ~550 + 550 |
| PR-8 | open source | skills, api, ext, web: the workspace's versions, `PutSkillVersion`, updates offered | `internal/skills`, `internal/api`, `ext`, `ext/exttest`, `web/app`, `web/e2e`, `docs` | PR-4, PR-7; CS1's remote records on `main` | none | ~650 + 650 |
| PR-9 | open source | api, skills, web, docs: publishing, the cards' titles, the features entry | `internal/api`, `internal/skills`, `internal/export`, `web/app`, `docs` | PR-8; the owner's answer on what counts as terminal output; CS1's check of copied output on `main` | none | ~600 + 650 |
| PR-10 | hosted service | team history: cards kept per device, scope by shared project, delivery with names, revocation, export, catalogue, contract | the sync service, its store and contract | CS1's service rows that serve remote records; ID1's members and devices; an open-source tag holding PR-6's `ext` types | none | ~600 + 700 |
| PR-11 | hosted service | shared skills: versions per workspace, owners, publish rules, delivery, export, catalogue, contract | the sync service, its store and contract | PR-10; an open-source tag holding PR-9's | none (same service) | ~650 + 700 |
| PR-12 | official build | the sync provider: cards out, cards in, the outage and lapse tests, the Playwright run | the sync provider | PR-10 deployed to staging; CS1's provider rows; the tag of PR-10 | PR-11 (other repository) | ~400 + 600 |
| PR-13 | official build | the sync provider: publish requests, received versions, the outage and lapse tests, the Playwright run | the sync provider | PR-11 deployed to staging; PR-12 | none | ~400 + 550 |

**PR-1**: `internal/skills` whole but for PR-3's `made.go` and PR-8's
`Put`: the version and its checks, the digest, the store with its lock,
staging and prune, the places with their marker, `Inspect`, `Place`,
`Remove`, `Settle`, `Cancel`, `Recover`, the receipts, the line diff;
`agents.SkillDirs` and `ConductorSkill`. No route, nothing calls it yet.
Tests: PR-1's above.

**PR-2**: the routes with their bounds; `skillsBusy` over the registry
and the catalog's adapters; `Settle` on session ends; the CLI and its
completion; the export's two rows; `docs/protocol.md`'s Skills section;
`docs/repository-map.md`'s row. After it, a person can import, install,
update, compare and remove skills from the CLI.

**PR-3**: `internal/crew/portable.go`, `skills.ImportBytes` and
`made.go`, the three routes, the CLI's three commands, the protocol
rows.

**PR-4**: the page, its dialogs, the crew page's two actions, the
navigation and the palette; `skills.spec.ts`. After it the free half of
the item is shipped; Acceptance part C can be observed.

**PR-5**: `ext.HistoryCard` and its `Valid`; the adapter's keyed source
(or HS2's computed source extended, whichever is on `main`), marked by
the journal's two new hooks and the ledger's; the card builder with its
project and repository maps; `ledger.CardRows`; `gitcli.RootsOf`;
`history.share` and the share route; the contract suite's sample; the
sync section of `docs/protocol.md`. Nothing is sent without a provider,
and nothing of PR-9's titles. HS1's PR-12 (#632, what a host reports)
touches `internal/journal`'s hosted fields: whichever merges second
rebases, never beside it.

**PR-6**: the remote kind `history` (or only its reading, if CS1's row
added it), the index's schema 2 and remote kinds, the remote source and
its hook on the doors, `on:`, `Similar`'s workspace scope,
`remoteShown()` (or HS2's, Waiting on the host overview), the entry's
`origin`, the card route, the protocol rows. HS2's PR-5 touches
`internal/api` and `ext` too: never beside it.

**PR-7**: the History page's and the palette's remote rows, the
**Machine** chip, **On every machine**, the card page, the one line, keep
here on the record and run pages, `history-remote.spec.ts`.

**PR-8**: `ext.SkillVersion` and its records, `Host.PutSkillVersion`,
the remote kind `skill`, `skills.Store.Put`, the reply's `available` and
its update, the page's Available sections and banner, the fakes; the
protocol rows.

**PR-9**: the publish route, the outbox and its allowlist row, the check
of copied output over files and notes, **Publish my changes**, the
cards' titles under the owner's answer, the publish dialog; the features
entry with Acceptance part C's record and the open-source half of parts A
and B; the repository map. This is the last open-source PR and ends the
item's open-source work.

**PR-10 to PR-13**: in their own repositories, in their own words and
checks; their bodies tick article 3 (each feature names the hosted
dependency that gates it: here, the sync service). They follow CS1's and
ID1's PR order and are cut when those rows are in their repositories'
issues. They come after the owner's answer in any case: they wait for
CS1's hosted rows, which wait for CS1's first open-source tag, which holds
CS1's check of copied output, which waits for the answer.

### Waiting on the sync plan

The sync plan (CS1's, under review in its own repository, in its final
check on 2026-11-06) leaves out what this item needs below; each is
stated as this item's own interface need, and the rows that need it wait
for it by name. Where the sync plan answers otherwise, this plan takes its
answer; where it does not take a need, this plan's own rows build it (the
open-source part in PR-6, PR-8 or PR-9, the hosted part in PR-10 to
PR-13). PR-1 to PR-5 depend on none of them.

1. **The remote kinds `history` (a card, at most 32 KiB) and `skill` (a
   version's manifest and state, at most 16 KiB), and the door
   `PutSkillVersion` (a version's files, at most 1 MiB).** At stake: PR-6,
   PR-8, PR-12, PR-13.
2. **A remote record names the person of the device that sent it** (a
   display name, from identity's feed), so a row says whose machine it
   came from. At stake: PR-6's `person`, PR-7's badge, PR-10.
3. **A remote record says whether it came from one of the receiving
   person's own servers, with the provider's link to open that server.**
   At stake: the card page's **Open studio**; until answered, no card
   offers it and every card says only where it is held.
4. **The field-by-field table gains history cards and skill versions as
   this plan's tables give them**, and the sync plan's choice that
   history stays on its machine reads: a record stays; its card leaves,
   by this plan's table. At stake: PR-10, PR-12.
5. **A card's scope follows its Dispatch project's share**, read by the
   service from the project's share state, so a card needs no scope of
   its own. At stake: PR-10.
6. **The provider passes this item's documents to its own builders**:
   the `history` store's documents, and the publish request (a data
   document of up to 1.25 MiB, delivered in parts), each decoded field by
   field. At stake: PR-12, PR-13.
7. **The check of copied output can be asked of any text by any route**,
   not only Dispatch's rows and decisions, so publishing checks a skill's
   files and notes with it. At stake: PR-9.

### Waiting on the host overview

The host overview (`docs/plans/hosts.md`, #677, in its last review round)
and this item both show other machines through sync and fall back to the
local view. HS2 owns the Hosts page, the `device` and `hosts` records'
use, and the host summary; this plan owns History's and Skills' remote
rows. Two pieces are shared:

1. **When other machines are shown** (`sync` ready; the status current,
   or behind for less than 10 minutes, current at least once in this
   server's life, not signed out) is one server function and one web
   function, made by whichever of HS2's PR-5 and this plan's PR-6 merges
   first, read by the other. At stake: PR-6, PR-7, PR-8.
2. **The adapter's computed documents**: HS2's one host summary and this
   plan's keyed cards share one mechanism (a store name, keys marked
   pending, a build at delivery), made by whichever of HS2's PR-5 and this
   plan's PR-5 merges first. At stake: PR-5.

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
   branch: one line, at most 60 bytes each) leave, since none can copy
   the default's stretch of output, and a published skill's files and
   notes pass the check of copied output. Under the alternative, only a
   session name a person gave leaves; crew, member and branch names stay;
   publishing is unchanged, since a person publishes every file after
   seeing it and is its writer for this rule. **The rows that change
   under the alternative**: PR-9 (`TestTitlesFollowTheReading` and the
   builder's titles), PR-10's title vectors, PR-12's title rule, and
   Acceptance part A's step 5 (a crew's name is not found on another
   machine). PR-1 to PR-8 hold under either.
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
10. **Who publishes**: a workspace's operators and admins; a name's owner
    is its first publisher, and later versions come from the owner or an
    admin; a reviewer or a guest publishes nothing. Withdrawing a version
    stops offering it and removes nothing from any machine.
11. **Who installs, and how**: the admin of the server whose agents will
    read it (the workbench token), by an operation that names the version
    by its digest, after seeing its files or its difference; never by
    itself, never at a version's arrival or a restore; a place that a
    running session of this server reads waits until those sessions end;
    a place changed by hand is replaced only when the person, shown the
    change, says so; a folder that is not Conductor's is never touched.
12. **No marketplace.** A version is seen only by the members of the
    workspace it was published in; nothing is listed, searched, rated,
    sold or installed from outside a local folder and that workspace.
13. **No agent installs or publishes a skill.** No MCP tool, no guide
    row, no grant for Brakeman: a skill is code an agent will run, and
    the person who runs the agent chooses it.
14. **A workflow template is a portable saved crew.** Without its id,
    folder, `where`, origin and times; with its tasks, checks and setup
    (shown in full before an install, and under the credential check at
    publishing); a manifest in the repository stays a reference to it.
    It is applied to a crew only by a person, never to a crew whose run
    is not final.
15. **Skills are placed on the server's own machine only**, for sessions
    this server runs natively; a host's machine and a Runtime's guest are
    a later item's.
16. **Names**: the page is **Skills** (`/skills`), with workflow
    templates as its second section; the part of history that leaves is
    a **history card**. "Template" alone stays the compare crew's word.

## Acceptance

The boundary (the Program, section 4.5, TS1): a teammate finds the
session that solved a related problem on another machine.

**Part A, team history, after PR-7, PR-10 and PR-12 and CS1**, by hand,
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
   #2 passed at <commit> · on mac · <owner>", with the attempt that was
   handed build #1's failure.
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
template runs. A reviewer member's publish is refused. Then the service
stopped: the update is hidden behind the one line and the installed skill
stays placed.

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
