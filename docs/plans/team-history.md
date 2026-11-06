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
  an argument, and no name a person or an agent chose (a crew's, a task's,
  a check's, a session's, a host's, a branch's): a field that is not
  certainly free of such words does not leave. A teammate's server receives the cards of the projects the
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
  Nothing installs or updates by itself, a version is installed only when
  it is certainly what its owner published and the person reviewed, and
  a skill is never placed under a session this server runs. The free build gets all of this for skills and
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
This plan relies on nothing the sync plan's fourth review round removed
or narrowed (what it holds across restores of its service and copies
of a data directory): only its remote records and their doors, its
sync status with this server's own device, a project's share and its
one check of copied output, which that round kept as they were.

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
  string, `clean`, `output` or `pending`), and before them the sharing of
  a Dispatch project (its row OS-1b: a project's `shared` workspace and
  `share_state` in the Dispatch store). PR-6 and PR-8 wait for the
  first two (its row called OS-4 in `docs/plans/hosts.md`, which comes
  after OS-1b), PR-9 for the third and for the owner's answer below. Its hosted rows carry the
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
for CS1's remote records; PR-9 (publishing) waits for the owner's answer
on what counts as terminal output (below) and for HS1's PR-11 (#630);
PR-10 to PR-13 wait for CS1's and ID1's hosted rows and for an
open-source tag holding PR-9.

**The open question this plan builds on, and how it is ordered around
it.** The sync plan holds an open question for the owner: what in a
stored record counts as terminal output. Its default, which this plan
follows: terminal output is what Conductor captured (a session's screen,
scrollback and ring, its activity log's words, a check's log, a history
record's words); none of it is ever sent, and any free text that copies a
long stretch of captured output is kept back as if it were output. The
alternative: free text an agent wrote is itself treated as terminal
output, since an agent writes from what its session printed. No card
depends on the answer, by one invariant (I1, "What leaves a machine,
field by field"): **no byte of a card was chosen by a person or an
agent.** Every string a card holds is one Conductor made (a random id, a
keyed reference, an ordinal, a time, a count, a full commit id, a
SHA-256 digest) or a member of a closed set compiled into the binary (a
session kind, an adapter, a state), or, from PR-6, a Dispatch key the
sync plan already sends to the same workspace under its own table, and
only on a card of a project the workspace shares. A crew's id (made from
its name), a run's id (which begins with it), a task's and a check's id,
a member's name, a session's name, a branch, a host's name and a catalog
agent's name are words, so they leave only as references or ordinals,
never as titles. The skills of PR-1 to PR-8 are sent nowhere. PR-9 adds
publishing, a person's deliberate act over files they were shown, whose
check of copied output is written for the answer given; "Owner
decisions", 2, says what changes under the alternative.

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
| The session journal, its record and its routes; the history index, its kinds table, its budget, its search, `Entry`, `Links` and `Similar`; the palette's `~` and the History page | HS1, `docs/plans/history.md` | reads a record's head through a closed table into a card (PR-5); adds the journal's write and forget hooks and the per-record share choice (PR-5); adds the kinds of other machines' cards, the index's schema version 2 with `repo_key` and the reserved `origin` filled, a budget of their own for other machines' entries, the `Remote` option on every read, the `on:` filter and `Similar`'s workspace scope (PR-6); draws other machines' rows (PR-7). Each after the HS1 row it builds on |
| The sync seam (`ext.ContinuityStore`, `ext.Doc`), the adapter's allowlist, export and restore | C6, `docs/plans/ext.md` | registers two computed sources, `history` (PR-5) and `skills` (PR-9, the publish requests), beside the data, ledger and Dispatch documents |
| The adapter's computed sources (a store name, its keys, a build at delivery, its revision rule) | made by whichever of HS2's PR-5 (`docs/plans/hosts.md`, which adopted this at its final check) and this plan's PR-5 merges first, used by the other | the same shape in both plans: `a.computed(src)` with `paths`, `build(path) (body, deleted, err)` and the source's own revisions; `a.computedChanged(store, path)` marks a key |
| Remote records, their doors and files; the sync status | CS1 (its public plan `docs/plans/sync-seams.md`, its row OS-4) | reads them; adds the remote kinds `history` and `skill`, their decoders and bounds, and the door `PutSkillVersion` (PR-6, PR-8): the sync plan leaves these to this item's rows |
| What leaves a machine, field by field; the credential check and the check of copied output; scopes; revocation | CS1 | adds its own table for history cards and skill versions (The changes), held to CS1's rules and calling its `outputGuard.Check` over each published file and the notes (the sync plan, Owner decisions 19: a record stays; its card may leave under this item's table) |
| A Dispatch project's share (`project.share`, its `shared` workspace and `share_state`) | CS1's OS-1b | reads it when a card is built, from PR-6; adds nothing to it |
| The sync status (`ext.SyncStatus`, `SyncReport`) | CS1's OS-4 | reads it; PR-9 adds one field, `Enrolment` (below), filled by the official build's provider (PR-13) |
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
   (with no placement journal); it leaves out the publish requests,
   the received versions and the remote records (the provider's). A member
   removed from the workspace, or a device revoked, loses every card and
   every received version on their machines (CS1's clear, which empties
   `skills/received/` with `remote/`), and what they installed, which was
   copied into their own versions at the install, stays theirs. No
   approval, answer or launch is made from any page here, and a template
   carries no yolo and no member's arguments (where an approval bypass
   would ride): a crew made from one asks for approvals unless its person
   turns that off on it.
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
  since; the difference shown), **In use by 2 sessions** (sessions of
  this server whose agents read that directory), **Missing**, or **Not
  Conductor's** (a folder of the person's own under that name, which
  Conductor never writes over).
- **Import** takes a folder (a skill: its `SKILL.md` and files; a
  template: its `workflow.json`) and makes it a version: its files are
  shown, their sizes, which are executable, and the SHA-256 that names
  the version. **Install** asks which agents' directories to place it in,
  shows the files (or, for an update, the difference from the installed
  version, file by file, in the same diff view the Files tab uses), and
  says that its scripts run with the agent's permissions. A directory a
  running session of this server reads from is not written: the install
  is refused there, naming those sessions, and the person installs again
  once they end. The dialog says what Conductor cannot see (an agent
  started in a shell, a host's sessions, another Conductor server under
  the same home), which read the new files at their next skill load.
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
  Ada · handed attempt 1's failure" (ordinals, never the check's or the
  task's name, which a person chose). The
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
  A workspace version is installed only while the sync status is current
  and the service's record says it is published, by an owner still in
  the workspace, with the digest of the files on this machine; otherwise
  the install is refused and the page says which of these it could not
  confirm. Publishing (PR-9) shows every file once more before
  it leaves and says who may see it: the members of the workspace this
  server is signed in to as the dialog names it, no one else.
- **Unreachable or lapsed.** Other machines' rows and the workspace's
  versions are hidden behind one line ("Other machines' history is not
  shown: the sync service has not been reached since 14:02"), and the page
  is the plain build's. Nothing local is lost: this machine's history,
  its installed skills and its versions, the cards it has received (kept
  where the provider wrote them until the provider says otherwise). They
  come back with the next current status, with no restart.

**What leaves a machine, and what never does.** The heart of this item.
A history card is built from a closed table of fields (The changes, "What
leaves a machine, field by field") that holds nothing a person or an
agent chose: Conductor's own random ids, keyed references and ordinals,
closed enumerations, times, counts, full commit ids and SHA-256 digests,
and, from PR-6, the Dispatch keys of a project the workspace already
shares:

- **Leaves:** who (the sync service knows the sending device and its
  person; no card names them), the source (the agent's adapter, and its
  catalog id only when the binary's own catalog defines it; its version
  when it is digits and dots alone; the session's kind; whether a host ran it; the
  Runtime's `where`), the project (from PR-6, the Dispatch project's key and the
  ticket's key, only while the workspace shares the project) and the
  repository (the digest of its root commits), the revision (start and
  end commit ids; an attempt's start and result commits; a check's
  commit), the run's references (the run as a reference keyed by this
  server, never its id, which begins with the crew's name; tasks and
  checks by their ordinal in the crew as launched; attempt numbers; the
  check receipt a failed attempt's successor was handed, by the same
  references), each task's state and its standing result, outcomes and
  states as enumerations, times, counts, and availability (how and when
  the session ended, or that it was not observed to end).
- **Never leaves:** any name a person or an agent chose (the session's
  name, the crew's, a member's, the tasks' and checks' ids, the branch, a
  catalog agent of the person's own); a session's transcript, screen, scrollback or
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
field, and finds none of them in any document of the `history` source
the adapter hands a sync store. (C6's own documents, a crew's and a
run's, carry their crew's words to the person's own sync store under
C6's table, as they do on `main`; they are held by C6's tests, not by
this one.)

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

**The rule, one invariant (I1).** Every string a card holds is one of: an id Conductor made at random (a record's or a session's 16
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
a Runtime's registered name); or, from PR-6, a Dispatch project or ticket
key, sent only on a card of a project the workspace shares at the
moment the card is built, whose keys the sync plan already sends to the
same workspace under its own table, so the card tells its readers
nothing they do not already hold. Nothing a person or an agent typed or
named is in any other form, and a field whose source is not certainly
free of such words does not leave at all: the card goes without it. No
row of this plan adds a title. `TestCardFromHostileValues` builds a session and a run whose every
free-words source holds a distinct marker (the crew's name and so its id
and the run's, every task id, check id and member name, the session's
name and working title, the branch, the folder and the repository's
remote, a catalog agent of the person's own with a marked id and name,
the agent's version output with a marked suffix, the host's name and user, a project not
shared whose key is marked, a task's reason, a review's verdict) and
finds no marker in any document of the `history` source the adapter
hands a recording sync store, across a replay and every later delivery;
and for every string field, a card whose value is replaced by a marker
fails `Valid`.

**Every field whose source holds words**, and what leaves of it:

| Source (who writes it) | What leaves |
|---|---|
| a crew's name, and its id (`slug(name)`, `internal/crew/store.go`); a person, an agent through `crew create`, or Brakeman's draft | nothing; the session card carries no crew field, the run card none |
| a run's id (`<crew id>-<8 hex>`) | the run's reference (keyed, above) |
| a task's id, a check's id (`taskIDPattern`, 40 characters a person or an agent chose) | the ordinal |
| a member's name | nothing |
| the session's name: given at launch or renamed by a person (`Launched.Name`, `Renamed`) | nothing |
| the session's working title (set by the agent's terminal) | nothing: it is output |
| the branch (`session.GitBranch`, which an agent may have made) | nothing |
| a catalog agent's id and name, the person's own entries (`internal/catalog`) | the adapter id only (`claude`, `codex`, ...); a built-in entry's id |
| the agent's version (the agent program's `--version` output) | `^[0-9]{1,6}(\.[0-9]{1,6}){0,3}$`, digits and dots alone; anything else (a suffix, a word) empty |
| a host's name (`os.Hostname()` or the person's) and user | nothing: `hosted` says a host ran it; the device the service knows says where |
| the folder, the repository's path and remote, file paths | nothing; the repository as the digest of its root commits |
| a check's argument list | its SHA-256 (`subject`, below) |
| a Dispatch project's key and a ticket's key (a person chose the project key) | from PR-6, as they are, on a card of a project shared at the build; empty otherwise, and empty in PR-5 |
| a task's reason, a review's verdict and findings, a result's words, instructions, messages, checkpoints, blockers, a Brakeman request | nothing |
| a record's entries, the screen, what anyone typed, an answer | nothing |

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
| `project`, `ticket` | from PR-6: the Dispatch project and ticket the session belongs to (below), when the workspace shares the project at the build | Dispatch's key patterns, or empty |
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
| `ticket`, `project`, `repo` | from PR-6, its ticket (a run launched from a ticket's plan revision) and that ticket's project, when the workspace shares it at the build; the crew folder's repository digest | key patterns or empty; 64 hex |
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

**No titles.** A card carries no name in words under either reading of
the owner's question: a crew's, a task's, a check's, a member's, a
session's name and a branch are free words a person or an agent chose,
and no check can make them certainly free of a credential or a copied
line, so a teammate's row reads "check 1 of task 2" and the card page
names the record's ordinals. A teammate who needs a name asks its
holder, who can open the record.

**Never leaves**, whatever the owner answers:

| Kept on its machine | Why |
|---|---|
| the record's `Entries` (lines Conductor typed, questions and their choices, answers and how they ended, chat, reports, status words), `Dropped` | the session's words: captured or a person's |
| the session's name, the crew's name and id, members' names, tasks' and checks' ids, the branch, a catalog entry of the person's own | names in free words a person or an agent chose |
| the session's screen, scrollback, ring, its activity log, its working title | terminal output |
| what a person typed into the terminal or a reply box, and any answer to a question, typed or picked | a person's words, which may be a credential (#673's finding) |
| `Files` paths, `Tools`, `Cwd`, `Repo`, `RepoCommon`, the repository's remote | paths and source structure; activity |
| `Launch` (arguments, their count and digest, variables' names and digest, yolo), `Resumes` | the person's command line and environment |
| `Agent.Session` (the agent's own session id and source) | names a file on that machine; resumable only there |
| `Host.Name`, `Host.User`, `Host.Owner`, a machine id | who and which machine, beyond the device the service knows |
| a check's argument list, log, tail and log digest; a task's instructions and reason; a result's summary and issues; a review's verdict and findings; messages; checkpoints; blockers; a manifest step's receipts; worktree paths; the crew as launched; the launch package | the run's words and configuration |
| every card of a Brakeman turn (a record whose `Crew.Brakeman` is set; a run whose recorded class is `brakeman`, `crew.ClassBrakeman`, never judged by its crew's id, since a person's crew may be saved as `brakeman-2`), of a record or run kept here, of a switchyard, and of a server with `history.share` off | Brakeman reads under a grant on its person's machine; the person's choice |

**The project and the repository.** In PR-5 every card's `project` and
`ticket` are empty. From PR-6 (after CS1's OS-1b, which puts a project's
share in the Dispatch store, and its OS-4), a card's `project` is the
Dispatch project of the run's ticket when the run was launched from one,
else the one project whose folder (`projects.cwd`) is in the same
repository as the record's (the repository identity HS1 already keeps,
`RepoCommon`, compared with the project folder's; none when no project or
more than one matches). It is read **when the card is built**, from the
Dispatch store in the same read, and kept only when that project is
shared with the workspace this server syncs (its `shared` workspace set
and its `share_state` empty: the share confirmed, not pending, completing,
dormant or no longer shared); in every other case, and whenever the read
fails, the card goes with `project` and `ticket` empty. A build therefore
never sends a key the store did not show as shared at that moment.
Changes of a share reach cards already sent by marks: every commit of the
Dispatch store that changes a project's share (a share, its confirmation
or refusal, a rekey, `Dormant`, `Unshared`), seen through the commit hook
the adapter already installs for its Dispatch change references (C6),
marks every card whose repository maps to that project, coalesced, so
the next delivery adds or drops the key; and the sync service drops a
card's key from what it delivers to others once the project is no longer
shared (PR-10, its own rule), so neither side alone holds the line.
`TestCardProjectFollowsTheShare` (PR-6): a project shared, confirmed,
pending, dormant and no longer shared, and a share removed after its
cards were sent: the key only while confirmed, every change delivered
again.

A card's `repo` is the SHA-256 of the repository's root commit ids,
sorted and joined by newlines, kept only when it is certainly the
repository's one identity: the repository is not shallow (`git
rev-parse --is-shallow-repository` answers `false`, so a shallow
boundary is never taken for a root) and HEAD reaches exactly one root
commit (`git rev-list --max-parents=0 HEAD` gives one line). A shallow
clone, a history whose HEAD reaches two roots, and any failure give
`repo` empty: the card is found by its other fields and not matched
across machines, an inconvenience, never a wrong match. It is read per
repository (`gitcli.RootsOf`, both commands as argv, the C locale, 5 s
each) when a card is built for it, kept in memory only (at most 1,000
repositories, by `RepoCommon`, the oldest read let go) for at most an
hour, success or failure, then read again, so a clone deepened or a
second root merged since changes its key within the hour; and only for
a folder on this server (`where` native): a hosted record's card has
none, since the server cannot read its host's repository. Cards are
built only while a sync provider is registered (article 7), so a plain
build never reads it.

**Who sees a card** is the sync service's rule, not the card's: a card
whose `project` is a project the workspace shares (CS1's share) goes to
every server of the workspace's members; every other card, its `project`
empty, goes only to its person's own other servers. The card names no
scope, so a project shared later brings its later cards to the workspace
(each card of it is marked and built again with its key) and one no
longer shared takes them back, by the service's own rule. When in doubt
the card's `project` is empty, so the doubtful case is the narrower
audience.

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
  versions/<digest>/version.json   one version this machine keeps: its content's manifest, nothing of where it came from
  versions/<digest>/files/<path>   its files, written once
  received/<digest>/...            a version's content the sync provider handed over (PR-8), the same layout;
                                   the provider's, not this machine's: cleared with remote/
  installed.json                   what this machine installed, from which source, each place's state, its placement journal
  made.json                        which crew was made from which template version (PR-3)
  receipts.json                    the operations' receipts (below)
  outbox/<op>.json                 a publish request until the service answers it (PR-9)
  staging/.tmp-<random>/           a version being made
```

**A version** (`version.go`) is content and nothing else:

```go
type Version struct {
	V      int       `json:"v"`      // 1
	Kind   string    `json:"kind"`   // skill | workflow
	Name   string    `json:"name"`   // ^[a-z0-9][a-z0-9-]{0,39}$, never "conductor"
	Digest string    `json:"digest"` // SHA-256 over the canonical file list (below)
	Files  []File    `json:"files"`  // sorted by path
	MadeAt time.Time `json:"madeAt"` // when this machine first kept these files
}
type File struct {
	Path   string `json:"path"`
	Mode   uint32 `json:"mode"`   // 0644 or 0755
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
```

The **digest** is the SHA-256 of `skill\x00<name>\n` (or `workflow`) then,
per file in path order, `<path>\x00<mode in octal>\x00<size>\x00<sha256>\n`:
the files and their modes, never a label, notes or where they came from,
so the same files are one version wherever they come from, and a version
is named by what it holds.

**Where a version came from is not the version's.** The same files can be
imported here and published in two workspaces under two owners, so
provenance is kept apart from content, by the one that needs it:

- a **workspace publication** is the service's `skill` record (PR-8),
  keyed by its workspace, name and label (`v3`), with its owner,
  publisher, time, notes, digest and state; it lives in `remote/` (the
  provider's) and is never written into `versions/`;
- an **install** records the source the person chose (`Install.Source`,
  below): `local` (the import's receipt keeps the folder it came from), or
  `{workspace, name, label, owner}` of the publication they installed;
- a local version has no label of its own: it is shown as `local-` and
  the digest's first 8 hex.

So one content copy serves every source, an update is judged by the
install's source and never by the digest (PR-8), and a withdrawal names
a publication (workspace, name, label), never a digest another
publication may share. `TestOneDigestManySources` (PR-8): the same files
imported here and published in two workspaces: one copy in `versions/`,
three sources; an install from workspace A is offered A's next version
only, and a withdrawal in B leaves A's install unmarked.

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
  line (the convention the agents read). PR-1 takes kind `skill` alone
  and refuses `workflow` (`kind_unsupported`). A workflow template
  (PR-3): a `workflow.json` at the root and at most a `README.md` beside
  it, the `workflow.json` passed to a validator the store's caller gives
  (`skills.Options.Workflow func([]byte) error`; the API passes
  `crew.ValidatePortable`), so `internal/skills` never imports
  `internal/crew`, and a store opened without one refuses `workflow`
  as PR-1 does. A crew whose portable form passes 256 KiB
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
digest; the name, label, digest, workspace, owner, publisher and at most
2 KiB of notes, as a publication's record carries them) is at
most 27 KiB encoded, bound at 32 KiB everywhere it travels; a file's
reply at most 513 KiB; a publish request (the manifest and every file in
base64) at most 1.5 MiB. `TestWireBoundsHoldTheLargestVersion` encodes
the largest version the rules allow (64 files, paths at their bound,
every file of quotes and backslashes at 1 MiB in all, notes at their
bound) through every route, record and document and finds each within
its bound.

**Store** (`store.go`), each method safe for concurrent use:

- `Open(st *store.Store, o Options) (*Store, error)`: `skills/` and its
  lock; the staging directory emptied; `Recover` (below).
- `Import(ctx, dir *os.Root, kind, name string) (Version, error)`: reads
  the folder through the root its caller
  opened (the API opens it through `openCwd`, so the folder is checked as
  a session's is and never opened again by path), checks it, writes the
  version in `staging/` and renames it to `versions/<digest>`; a digest
  already kept answers the version kept (an import of the same files
  twice is one version). The folder's path is kept in the import's
  receipt, shown here, never sent.
- `Put(ctx, v Version, files map[string][]byte) error` (PR-8): a
  version's content received from the workspace, written to `received/`,
  never to `versions/`: the same checks, the digest computed again from
  the bytes and refused when it differs (`ErrDigest`). `Retain(digest)`
  copies a received version's content into `versions/` (the first step
  of installing it, by a person's operation), and only then is it this
  machine's. `ClearReceived()` removes `received/` whole (the provider's
  clear and a revocation). A received copy that no `published` record
  names any longer is offered to no one (PR-8) and goes at the next
  `Prune`.
- `Get`, `List`, `File(digest, path) ([]byte, error)`: a file read through
  an `os.Root` of the version's `files/`, its SHA-256 checked against the
  manifest on every read; a mismatch is `ErrDamaged` and the version is
  listed `damaged` and never placed.
- `Prune()`: at most 200 names, per name at most 5 versions besides the
  pinned ones, and at most 256 MiB in all; the oldest unpinned go first. A
  version is **pinned** while anything refers to it: an install's
  digest, any place's digest (an update of Codex's place alone leaves
  Claude Code's on the older one), and every `made.json` entry (a
  template a crew was made from, which its **Changed** is computed
  against). A withdrawn workspace version that nothing pins goes at the
  next `Prune`. When the pinned
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
template has no places. **A target is a physical directory**: two paths
that reach one directory (a link, a configuration directory pointed at
another target's, two adapters sharing one) are one target, judged by
`os.SameFile` on the directories as they are at the moment of the
judgement, never by their spelling (below).

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
  marker left out), `missing`, `not_ours`, `damaged` (the marker cannot
  be read); and, beside the state, `inUse`: the sessions of this server
  that hold the target now.
- `Snapshot(target, name) (Version, error)`: the place's folder as it is
  on disk, the marker left out once it has been read and checked to be
  the marker (a `.conductor-skill.json` that is not one, or any other
  leading-dot path, is still refused), every other file through the
  import's checks: the version **Publish my changes** publishes (PR-9).
- **Holds: what a running session of this server reads.** Placing and
  removing never rest on a snapshot of the registry, which a launch can
  pass between the look and the swap (`internal/api/sessions.go` starts
  the process before `registry.AddReserved` registers it). The store
  keeps, in memory, the **holds**: `Hold(session string, dirs []string)
  (release func(), err error)`, taken by a launch of this server before
  its process starts and released when that process has ended (or at
  once if the launch fails), each held directory opened and kept as the
  `os.FileInfo` it had when the hold was taken; `Hold` and every step of
  a placement take the store's place lock. A hold taken while a
  placement of that directory is between its first and last rename waits
  for that placement to finish (a few renames). **A placement asked while
  any hold is on the target** (by `os.SameFile` between the target
  directory and each held directory) **is refused**, `ErrInUse` with the
  holders' sessions: nothing waits, nothing is settled later, and the
  person installs again once those sessions end. A held directory that
  did not exist when the hold was taken is held by its cleaned absolute
  path, and a target directory that does not exist yet is created by a
  placement only while no hold of any kind is on the store. `Place` and
  `Remove` never consult anything else to decide busy, and complete in
  the request that asked for them or not at all.
- **What this cannot see, and the promise it narrows to.** Holds are
  this server's own launches, in its own memory: they end with the
  server, and they know nothing of a hosted session (a host whose link
  dropped keeps its session running while the server's registry lets it
  go), of an agent started in a shell, of a session of another Conductor
  server under the same home, or of an agent an earlier server left
  running. So the promise is **never under a session this server
  launched and runs now**, not "never under any running agent". The
  install dialog says so in one line ("Agents Conductor did not start
  here, a host's sessions among them, read the new files at their next
  skill load"), the person installs knowing it, and the swap is still
  two renames, so whatever reads the directory sees the old version, the
  new one, or, for that moment only, none (I11).
- **A modified place is the person's.** An update or a removal of a
  `modified` place is refused (`ErrModified`, naming the files) unless
  the operation carries `replace: {target, seen}` for it, the `seen`
  digest of what the person was shown, compared under the place lock
  with what is on disk at that moment; a place that changed again since
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
`Recover` at the next `Open` puts the new one in (I11). No session this
server launched reads the target during either: the placement took
place because no hold was on it, and a launch's hold waits for the
placement's renames.

**`installed.json`**:

```go
type Installed struct {
	V      int                `json:"v"`
	Skills map[string]Install `json:"skills"` // by name
}
type Install struct {
	Kind    string    `json:"kind"`
	Digest  string    `json:"digest"` // the version installed (placed, or kept for a template)
	Source  Source    `json:"source"` // where the person installed it from
	At      time.Time `json:"at"`
	By      string    `json:"by"`     // the actor of the operation
	Places  []Place   `json:"places,omitempty"`
}
type Source struct {
	From      string `json:"from"`                // local | workspace
	Workspace string `json:"workspace,omitempty"` // workspace: the workspace's id, as the provider names it
	Label     string `json:"label,omitempty"`     // workspace: the publication's label, "v3"
	Owner     string `json:"owner,omitempty"`     // workspace: the name's owner, as the service named them at the install
}
type Place struct {
	Target  string   `json:"target"`  // claude | codex | agents | agy, or "dir:<16 hex>" (below)
	Dir     string   `json:"dir"`     // the target directory itself, as it was resolved when placed
	Digest  string   `json:"digest"`  // what is placed there
	Journal *Journal `json:"journal,omitempty"` // during a placement or removal (above)
}
type Journal struct {
	Op, From, To, New, Old, Step string
}
```

**Templates' crews** (`made.go`, PR-3): `skills/made.json` maps a crew id
to the template version it was made from (`{name, digest, op}`), so the
crew file's schema does not change and an older Conductor still reads
every crew. A crew deleted is dropped from it at the next read.

**Receipts.** Every write (import, install, update, remove, delete a
version, save as template, new crew from template, apply, publish,
cancel publishing) is an operation with the client's `op` id,
`<unix seconds, 10 digits>-<16 to 48 of [A-Za-z0-9_-]>`, so an id says
when it was made. Its receipt `{op, what, name, digest, state, places,
crew?, fingerprint, at, actor}` has three states and two transitions:
`started` (written and synced before the operation's first side
effect, with `fingerprint`, the SHA-256 of the request's route and
canonical body) → `done` or `failed` (with its reason), written after
the last. The rules, each failing closed:

- **The same `op` again** answers its receipt and does nothing (the
  Program, section 1.5); with another fingerprint it is `409 op_reused`
  and does nothing.
- **An `op` whose receipt may have been pruned is refused.** Receipts
  are kept at least 7 days and at most 10,000 (a write that would pass
  10,000 receipts younger than 7 days is refused, `429
  too_many_operations`); an `op` whose time is more than 7 days old, or
  more than a day ahead of the server's clock, is refused `409
  op_expired` before anything is looked up. So a replay of an old
  install after an uninstall installs nothing: either its receipt is
  still kept and answers, or its time is past the window and it is
  refused.
- **A crash between a side effect and its receipt.** `Recover`, at
  `Open`, judges each `started` receipt by what is on disk, never by the
  receipt alone, and writes `done` or `failed (interrupted)`: an import
  or a save as template is `done` if `versions/<digest>` is complete
  (the digest is recorded once staged, before the rename); an install,
  update or removal follows its places' placement journals (above),
  `done` when every place it named holds its `to`; an apply is `done`
  if the crew's portable digest is the version's, else `failed`; a new
  crew from template is found by its origin (PR-3); a delete of a
  version is `done` if the version is gone; a publish is `done` if its
  outbox file exists. A replay then answers that receipt.

`TestReplayAfterPruneRefused` (an install's receipt pruned by age, the
skill removed, the old `op` sent again: `op_expired`, nothing placed),
`TestReceiptAcrossACrash` (each operation stopped through a seam between
its side effect and its receipt, then `Open` and the same `op` again:
the original answer, never a second effect), `TestOpReusedRefused`.

`internal/agents` (PR-1b): `SkillDirs(home string) []SkillDir` (the
targets above, from each adapter's `SkillPath`, so a new adapter that
reads skills is a target with no change here), and `ConductorSkill()`
(the Conductor skill's text and SHA-256, for the page's read-only row).
An agent launched with a configuration directory of its own (`ConfigDirOf`,
#291, read from that launch's own environment) reads skills there, so
the directories a launch holds are computed from that launch's own
environment, before its process starts (`skillsDirs(agent, env)`: the
adapter's `SkillPath` directory and, with a configuration directory,
its skills directory), and nothing is rebuilt later from the catalog.
Holds and placements compare **directories, not names**: each directory
is cleaned and its links resolved (`filepath.EvalSymlinks`) where it
exists, and two are the same target when `os.SameFile` says so, so
`CLAUDE_CONFIG_DIR=$HOME/.agents` holds the `agents` target, a link to
`~/.claude` holds `claude`, and two adapters or two configuration
variables naming one directory hold one target. A configuration
directory that is no built-in target is listed as a target of its own,
`dir:<first 16 hex of the resolved path's SHA-256>` with its `Dir`,
once a launch has used it; a skill is placed there only by a person's
install into that target, never at a launch. `TestOneDirectoryOneTarget`
(a configuration directory set to `~/.agents`, a link to `~/.claude`,
two adapters on one directory: one target each time, and a placement
into it refused while any of those sessions runs).

### `internal/api`, `internal/cli` (PR-2): the routes, the sessions that hold a place, the CLI

`internal/api/skills.go` opens the store with the data directory (none
without one; never on a switchyard, which serves no workbench) and
answers `404 skills_unavailable` without a data directory or on a
switchyard, `409 skills_read_only` for a write while another server holds
the lock.

**Who may use these routes.** A skill is code an agent will run with its
person's permissions, so every write that places code, changes a crew or
sends a version (import, install, update, remove, delete a
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
| `GET /api/skills` | → `{skills: [Row], templates: [Row], conductor: {sha256, places}, targets: [{id, dir, agents}], readOnly?}`; a row: name, kind, the installed version (its digest, and its source with the label), its places' states with the sessions of this server that hold each, the versions kept | reply at most 1 MiB |
| `GET /api/skill-versions/{digest}` | → the version's manifest | 32 KiB |
| `GET /api/skill-versions/{digest}/file?path=` | → `{path, mode, text}` | 513 KiB (a 256 KiB file, escaped) |
| `GET /api/skills/{name}/compare?from=&to=` | `from`: a digest or `disk:<target>`; `to`: a digest → per file `{path, change: added \| removed \| changed \| same, from?, to?}` with both texts | reply at most 2 MiB, `truncated` past it |
| `POST /api/skills/import` | `{op, path, kind, name?}` → `201 {version, receipt}`; `400 invalid_skill` naming the file and the rule | body 4 KiB |
| `POST /api/skills/{name}/install` | `{op, digest, targets, expect?, replace?}` → `{receipt}`, every place `placed`, or nothing placed: `409 skill_in_use` naming each held target and its sessions; `409 version_changed` when `expect` (the installed digest the person saw) is not the installed one; `409 place_modified`, `409 place_changed`, `409 skill_not_ours`, naming the place | body 8 KiB |
| `POST /api/skills/{name}/remove` | `{op, targets, replace?}` → `{receipt}`; `409 skill_in_use` as install | body 8 KiB |
| `DELETE /api/skill-versions/{digest}?op=` | → `204`; `409 version_in_use` while pinned (Prune's rule) | |

Every body is read with `DisallowUnknownFields` and its limit, and every
reply encoded as "The sizes on every wire" says. An update is an install
of another digest with `expect`. An install or a removal is judged whole
before anything is written: every target it names is checked for holds,
modification and ownership under the place lock, and one refusal places
nothing anywhere.

**The sessions that hold a place.** Each launch this server makes
(`internal/api/sessions.go`, and the engine's attempts through
`api.Server.Launch`, which reach the same code) computes the directories
its agent reads from its own catalog agent's adapter and its own
environment (`skillsDirs`, above) and takes `skills.Store.Hold` on them
**before its process starts**; the hold is released when the process
has ended (the session's exit, observed by the session itself, not its
leaving the registry), or at once if the launch fails. Every session of
this server counts, whatever its `where`: a Runtime other than native is
counted too, the cautious side. **Hosted sessions do not hold**: the
server does not launch them and cannot know when they end (a host whose
link dropped keeps its sessions running after the registry lets them
go), so the promise for them is the dialog's line, not a hold
("What this cannot see", above). Sessions Conductor does not run (an
agent started in a shell) cannot be seen either.

**The CLI**: `conductor skills list | show <name> | import <dir>
[--template] [--name] | install <name> <label|digest>
[--agents claude,codex] | update <name> <label|digest> | remove <name> |
compare <name> [<from>] [<to>]`, with the workbench
token, printing the receipt (an install refused for sessions in use
names them); `import` sends the folder's path, which the
server opens. Completion and usage text. The writes refuse to run inside
a session (above). There is no MCP tool and no operating-guide row for
agents: a skill is code an agent runs, and an agent installing its own
is out of bounds (Owner decisions, 13).

**The export** (`internal/export/leftout.go`): `skills/versions/`,
`made.json` and `receipts.json` are carried as the data directory's other
files are. `installed.json` is carried as `skills.ExportInstalled` makes
it, the way `catalog.json` is carried as `export.ResolveCatalog` makes it:
each install's digest and source and each place's target, `Dir` and
digest, and no `Journal`. `skills/received/` and `skills/outbox/` are
`WhyProvider` (the provider's, as `remote/` is), `skills/staging/` is
`WhyTemporary` and `skills/owner.lock` `WhyLock`. A restore installs the
versions and the record of what was installed, and places nothing: every
place shows `missing` (or `placed`, if the folder with its marker is
already there) until the person installs it there (Owner decisions, 11).
`TestRestorePlacesNothing` takes an archive during a placement (its
journal on disk) and restores it onto an idle machine: no place written,
no journal carried.

`docs/protocol.md` gains a Skills section with every route and bound
above; `docs/repository-map.md` the `internal/skills` row.

### `internal/crew`, `internal/skills`, `internal/api`, `internal/cli` (PR-3): workflow templates

- **The portable crew** (`internal/crew/portable.go`): `type Portable
  struct` with the crew's name, goal, isolation, open-after-launch, view
  link time, members (name, agent id, prompt, start), tasks
  (id, member, kind, instructions, needs, checks), setup, and a manifest
  only as a reference to the repository's (`file: true`); never the
  crew's `id`, `cwd`, `where`, `origin`, `yolo`, members' **arguments**,
  an **inline manifest**, `createdAt`, `updatedAt` or
  `manifestRevision`. **No approval bypass travels**: a
  crew made from a template, or one a template is applied to, keeps the
  machine's default (approvals on) or the crew's own setting, and only its
  person turns it off, on the crew, as today (article 4: required
  approvals stay on, yolo off by default). A member's arguments are where
  a bypass rides without the yolo field (`--dangerously-skip-permissions`,
  `--dangerously-bypass-approvals-and-sandbox`, `internal/catalog`'s
  defaults, appended by `internal/api/sessions.go` whatever the yolo
  choice) and no list of flags is certainly complete across agents, so
  no member's arguments travel at all: a crew made from a template has
  none, and the person adds what they want on the crew page; an apply
  keeps the crew's own arguments. The same for an inline manifest, whose
  prerequisites, services, verify and cleanup phases are commands of
  their own (`internal/crew/manifest.go`): it does not travel, so every
  command a template holds is a setup command or a task's check, both
  listed by the dialog below; a manifest kept in the repository (`file:
  true`) stays a reference to the repository's `conductor.manifest.json`,
  which git already shares and the dialog names. **Save as workflow
  template** says what was left out ("Members' arguments and the inline
  project manifest stay with this crew"). A `workflow.json` that holds
  `args`, `yolo` or an inline manifest is refused by
  `DisallowUnknownFields` (`invalid_skill`). `TestTemplateCarriesNoYolo`
  makes a template from a yolo crew whose members carry each bypass
  argument and an inline manifest, makes a crew from it and launches it
  through a recording launcher: the launched argv holds no bypass
  argument and the crew no yolo and no manifest phase; an applied
  version leaves the crew's own setting and arguments as they were.
  `PortableOf(c)`, `FromPortable(p, cwd) (Crew, error)` (validated by the
  crew's own `Validate` once the folder is given), and `PortableDigest(p)`
  (SHA-256 of its canonical JSON). A template's `workflow.json` is a
  `Portable` with `v: 1`, read with `DisallowUnknownFields`, at most 256
  KiB (`template_too_large` past it, PR-1's rule; a valid crew above 256
  KiB is refused with its size, `TestTemplateTooLargeRefused`).
- `POST /api/crews/{id}/template` `{op, name}` → `201
  {version, leftOut}`: **Save as workflow template**, a version of kind `workflow`
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
  JSON and its difference, every member's agent, every **setup** command
  and every task's **check** commands, each as its argument list (a
  portable crew holds no other command), and, when the crew refers to
  the repository's manifest, "This crew also runs the repository's
  conductor.manifest.json", with "These commands run on this machine when
  the crew launches." The dialog sets no yolo and no argument.
  `TestTemplateCommandsAreAllListed` (PR-3, over the route that feeds the
  dialog): a template whose every command is in setup or checks lists
  each of them, and a template that tries to carry a command elsewhere
  is refused.
- `GET /api/skills` gives a template's row its crews: each crew made from
  it with `changed` (its portable digest differs from the version's) and
  the template version it was made from.
- `conductor skills template <crew> <name>`, `conductor skills crew <name>
  <cwd>`, `conductor skills apply <name> <crew>`, each printing the
  setup and check commands first.

### `web/app` (PR-4): the Skills page

- `pages/skills.vue` (`/skills`): two sections, **Skills** and **Workflow
  templates**, each a table (Nuxt UI `UTable`) of rows; a skill's row
  shows its installed version (short digest, its source: local, or the
  workspace's label and owner),
  each place as a badge (**Placed** `success`, **Modified here**
  `warning`, **Missing** `neutral`, **Not Conductor's** `neutral`,
  **Damaged** `error`), beside it **In use by 2 sessions** with their
  names linked when sessions of this server hold it, and its actions
  (Install, Update, Compare, Remove). The Conductor skill is the first row, read only, with its
  places. A template's row shows its crews with **Changed** where so, and
  **New crew**.
- `SkillImportModal.vue` (a folder path and the kind),
  `SkillInstallModal.vue` (the targets as checkboxes, each with its
  state, a target in use unchecked and disabled with its sessions named;
  the files, or for an update each changed file in `DiffEditor`;
  a modified place's own difference from the installed version, with
  **Replace my changes** off by default; the executable files listed;
  the line "This skill's files are instructions and scripts your agents
  will run with their own permissions. Read them before you install."
  and the line on what Conductor cannot see; a refusal for sessions in
  use shown with their names and **Install** offered again),
  `SkillCompare.vue`, `TemplateCrewsList.vue`, `TemplateCrewModal.vue`
  (New crew from template and Apply: the members' agents, the setup
  commands and every check's commands listed apart, each as its argument
  list, and the repository manifest's line when the crew refers to it,
  before the JSON and its difference; the line "These commands run on
  this machine when the crew launches."; no yolo or argument control).
- The crew page (`pages/crews/[[id]].vue`): **Save as workflow template**
  in its menu; the crews list's empty state and header: **New crew from
  template**.
- `utils/skills.ts` (pure: a row's states, words and colors; which
  actions a row offers; the in-use line), `composables/useSkills.ts`
  (the routes, an `op` id per action in the receipts' form, kept until
  its receipt).
- The navigation (`utils/sidebar.ts`, `BottomBar.vue`) gains Skills, and
  the palette's Go to gains **Go to Skills**.
- `web/e2e/skills.spec.ts` (below).

### `internal/api`, `internal/journal`, `internal/crew/ledger`, `internal/gitcli`, `ext`, `ext/exttest` (PR-5): history cards to the sync store

- **`ext.HistoryCard`** (`ext/history.go`): the card of the tables above,
  `HistorySession`, `HistoryRun`, `HistoryTask`, `HistoryAttempt`,
  `HistoryCheck`, `HistoryCheckRef`, `MaxHistoryCard` (64 KiB), and
  `Valid()`, which checks every string against its pattern and refuses
  any field the table does not name. Standard library only.
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
    pointer and which now mark the run's card beside it, and by a change
    of `history/share.json` (the card it names). PR-6 adds the marks of
    a project's share ("The project and the repository").
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
    record kept here, then shared again; from PR-6, a project's share)
    still rises, a `Deleted` and a later rebuild of the same key each
    take a newer revision, and nothing is discarded as stale by a store
    that orders by `Rev` (the adapter's own model in
    `internal/api/sync_test.go` does). The sync service orders by its
    provider's own counter and keeps `Rev` for display (follows the sync
    plan's final text). `Rev` is never a durable mark: it starts again
    each epoch, so nothing that receives a card judges it unchanged by
    its `Rev` (PR-6's remote source marks a card by its bytes).
    `TestHistoryCardRevisionsRise` keeps a card here, shares it again
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
- `internal/gitcli`: `RootsOf(dir)` (whether the repository is shallow,
  and the root commits HEAD reaches; argv, 5 s each; the key only for a
  full clone with one root, "The project and the repository"). The
  repository map, in memory only, in `internal/api/history_card.go`.
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
- **Other machines' entries never evict this machine's.** HS1's budget
  (200,000 entries and 256 MiB of text, the oldest dropped first,
  `docs/plans/history.md`, "The budget") counts this machine's entries
  alone (`origin = ''`), as it does before this PR. Other machines'
  entries have a budget of their own beside it, 50,000 entries and 64
  MiB of text, counted over `origin <> ''`, and a `Replace` of a remote
  source that would pass it drops the oldest remote entries (by the
  card's report time), never a local one; a local `Replace` never drops
  a remote entry either. So the index's file may hold both budgets at
  once, and during an outage, when other machines' rows are hidden, this
  machine's search holds exactly what a plain build's would.
  `TestRemoteNeverEvictsLocal` (the local budget full, remote cards
  arriving past theirs: every local entry stays, the oldest remote ones
  go, and the reverse).
- **The remote source** (`internal/api/history_remote.go`): the indexer
  reads each `remote/<device>/history-*.json` as a source keyed
  `remote:<device>/<kind>/<id>`, **its mark the SHA-256 of the file's
  bytes** as read: the whole record, the card and the record's own
  fields (`deviceName`, `person`, `own`, `openURL`). A card's `rev`
  starts again every epoch of its sender (PR-5), so it is never the
  mark: any change of content or envelope gives another mark, and the
  startup catch-up, which compares marks, reads again a file whose
  indexing a crash interrupted. The provider's
  `PutRemote`, `DeleteRemote` and `ClearRemote` mark the source dirty
  through a hook on the door, and the indexer's reconcile drops sources
  whose files are gone. `TestRemoteMarkSurvivesASenderEpoch` (a card at
  `rev` 1 indexed; the sender's new epoch sends other content at `rev`
  1; the receiver stopped through a seam after `PutRemote` and before the
  indexing, then started: the new content is indexed; a device name
  changed alone is indexed again). Entries: a session card is one `remote.session`
  entry; a run card a `remote.run`, an entry per attempt and per check,
  and the links the card holds (`handed`, `passed_after` within the
  card's run). An entry's title and summary are made of the card's fields
  only, with ordinals ("Claude Code session · task 2, attempt 2 · CND-12",
  "check 1 of task 2 · passed at def5678"); its body is empty.
- **The project key on cards** ("The project and the repository"): from
  this PR the builder fills `project` and `ticket` from the Dispatch
  store's share at the build, and the Dispatch store's commits that
  change a project's share mark the cards it maps,
  `TestCardProjectFollowsTheShare`.
- **Search.** A new filter, `on:` (`on:here`, or a device's name; a comma
  for alternatives). `Similar` gains the scope `workspace`: the receipts
  of the same `subject_key` with the same `repo_key` on every origin,
  ranked as HS1 ranks them (passed before failed, newest first), this
  machine's first. The History routes' `Entry` gains `origin: {device,
  deviceName, person, reportedAt}` and the availability `remote` ("on
  studio · reported 14:05").
- **Other machines' rows only when asked, on every read.** Every read of
  the index gains one option, `Remote bool`, off by default: `Search`,
  `Entry`, `Links` (both ends of a link), `Similar` and the lookup of
  `Similar`'s seed. A read without it never returns a row whose `origin`
  is not `''`, a link to one, or a seed that is one (`entry_gone`, as for
  a key the index no longer holds): the rule is in the index's queries,
  not in each route. The History routes (`GET /api/history`,
  `/api/history/entry?key=`, `/api/history/similar`, and the card route
  below) set it only for the workbench token or an identity principal
  with the admin grant, **and** only while `remoteShown().shown`;
  nothing else sets it. So a remote key a page kept from before an
  outage, a lapse or a sign-out answers `404` on every route while other
  machines are not shown, and Brakeman's reads (HS1's PR-11, #630, the
  `brakeman` principal's search under its grant) never see another
  machine's card and never make a `refURL` for one; a later caller has
  to ask. `TestRemoteRowsOnlyWhenAsked` (each read without the option),
  `TestRemoteKeyHiddenOnEveryRead` (a remote entry's key, a link to it
  from a local entry and a `Similar` seeded by it, through each route,
  while unreachable, lapsed and signed out: none answers it) here, and
  `TestBrakemanSeesNoOtherMachine` in PR-9, which lands after #630.
- **When other machines are shown**: `remoteShown()` (`internal/api/
  remote_shown.go`), in HS2's shape (`{present, shown, state, since}`;
  `present` when `sync` is ready and the status has been current at
  least once in this server's life and is not signed out; `shown` when,
  besides, it is current or behind for less than 10 minutes), made here
  with `TestRemoteShown` unless HS2's PR-5 merged first, then only read.
  When not `shown`, no read sets `Remote` (above), `on:` answers
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
  (any other provider's `Host` answers `ErrUnsupported`): the version's
  content handed to `skills.Store.Put`, which writes it to
  `skills/received/<digest>`, its digest computed again from the files
  and refused on a mismatch. Nothing of where it came from is written
  there: the publication is the `skill` record (below). It installs
  nothing and never writes `versions/`.
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
- **The remote kind `skill`**, this plan's own: one **publication**,
  keyed by its workspace, name and label (`v3`): the version's
  `SkillManifest` (its digest among it) and its state, `published`,
  `withdrawn` (by its owner or an admin), `publisher_left` (its owner or
  its publisher is no longer a member of the workspace, PR-11), or the
  answer to this server's own publish request (PR-9); at most 32 KiB.
  Two publications of the same files are two records over one digest.
  The Skills reply gains `available`: each `published` publication, by
  name, newest first, with its owner, publisher, time, notes and state;
  only while `remoteShown()`, and the reply's `remote: {state, since}`
  otherwise.
- **A workspace version is installed only when it is certainly the one
  published.** An install whose source is a publication is checked at
  the person's act, under the place lock, and refused `409
  source_unconfirmed` with the reason the page shows, never deferred:
  the sync status not `current` at that moment (`sync_not_current`: a
  status merely behind is not enough); no `skill` record for that
  workspace, name and label, or one whose state is not `published`
  (`not_published`, naming `withdrawn` or `publisher_left`); its digest
  not the digest the person was shown, or the content in `received/`
  not that digest when read (`digest_mismatch`). A version already
  retained in `versions/` is installed from a publication only under the
  same check: the source is what is checked, not the bytes alone. The
  source recorded is the one the person chose (`Install.Source`), and
  nothing is ever installed, updated or removed for a person by a
  record's arrival.
- **An update is the same lineage.** `update: {from, to}` is offered on
  an installed row only when the install's source is a publication of
  a workspace, and a `published` record of the same workspace and name,
  with the same owner as the install's source, has a later label (the
  service gives a name one owner in a workspace, its first publisher,
  and later versions of the name come only from that owner or an admin,
  PR-11). Any other version of the same name (beside a skill the person
  imported themselves, a `local` source, or from another workspace or
  owner, even with the same digest) is listed as **Another skill named
  release-notes, by Bob**, and installing it is a **replacement**: the
  install dialog shows every file in full, never a difference framed as
  an update. `TestUpdateOnlyWithinALineage`, `TestOneDigestManySources`
  (above).
- **An update from the workspace** is an install of the newer digest
  with `expect`, exactly as a local one: the person's operation, shown
  first, under the check above.
- **A withdrawn version, or one whose publisher left.** Not offered and
  not installable (above); one installed stays installed, as the
  person's, and its row says "withdrawn by its owner" (or "its publisher
  left the workspace") while a record of its own source says so, judged
  by its workspace, name and label, never by its digest, so another
  publication of the same files is not marked; while other machines are
  not shown the row says nothing of it. A received copy that no
  `published` record names, and a retained copy nothing pins, go at the
  next `Prune`. `TestInstallRefusesAnUnconfirmedSource` (each reason,
  and a version withdrawn between the dialog and the act),
  `TestWithdrawnPrunedUnlessPinned`, `TestWithdrawnStaysInstalled`.
- The page: **Available from the workspace** under each section, the
  update banner on an installed row, **Another skill named ...** with
  **Install as a replacement**, the owner and publisher in every
  version's line, the one line when not shown (`utils/remoteShown.ts`).
- `ext/exttest`: `FakeHost` records `PutSkillVersion`; the samples gain a
  version at its bounds.
- `docs/protocol.md` and `protocol.ts`: the reply's `available`, the
  remote kind, the door's bounds.

### `internal/api`, `internal/skills`, `ext`, `web/app`, `docs` (PR-9): publishing, the features entry

Cut after the owner's answer to the sync plan's question, once CS1's row
that adds the check of copied output (`outputGuard.Check`) is on `main`,
and after HS1's PR-11 (#630).

- **The sign-in a publish is approved under.** The sync plan's status
  (`ext.SyncReport`, follows its final text) names this server's device
  and its state, not the workspace or the person it is signed in as. PR-9
  adds, as this plan's own fields, `Enrolment` (an opaque string the
  provider changes whenever this server's workspace, its person or its
  enrolment changes, and empty while not signed in) and `WorkspaceName`
  (what the dialog shows); PR-13 fills them. With either empty,
  publishing is not offered.
- **Publish** (`POST /api/skills/{name}/publish` `{op, digest, notes,
  enrolment}`, this machine's own person only, `requireOwnPerson`). The
  dialog shows the workspace's name and carries the `enrolment` it was
  drawn with; the route reads the status once and refuses before writing
  anything: `409 sync_unavailable` unless the state is `current`, `409
  sync_changed` when the body's `enrolment` is not the status's now (the
  sign-in changed between the dialog and the act: the dialog is drawn
  again for the new one), and the checks below. Only then it writes the
  request, `skills/outbox/<op>.json` (`{v, op, enrolment, kind, name,
  digest, notes, files: [{path, mode, data}]}`, at most 1.5 MiB as "The
  sizes on every wire" says, at most 8 waiting). **A request is sent only
  under the sign-in it was approved under, and is never held across
  another**: the `skills` source's build compares its `enrolment` with
  the status's at every delivery, the provider compares it again before
  sending (PR-13) and the service takes it only from a device whose
  current enrolment it is (PR-11); at the first mismatch the request is
  removed unsent, its receipt `failed (sign_in_changed)`, and the page
  says "Not sent: this server's sign-in changed after you approved it.
  Publish again to send it to <the new workspace>." **Cancel publishing**
  (`POST /api/skills/{name}/publish/cancel {op}`) removes a request not
  yet answered. `TestPublishRefusesAChangedSignIn` (the enrolment
  changed between the dialog and the act: `409 sync_changed`, no
  outbox file), `TestOutboxDroppedAtASignInChange` (a request on disk,
  the enrolment changed: removed unsent, never delivered).
- **Delivered by its own computed source**, store `skills`
  (`syncSkills`), the mechanism of PR-5: its keys are the outbox's files
  (`paths` lists `skills/outbox/`), built at delivery from the file,
  marked when one is written or removed, and listed by every replay, so a
  request whose first delivery failed is found again after a restart or
  an overflow's replay with no later write, under the same sign-in; delivered in parts as any
  document past `ext.MaxDocData`. `TestOutboxRediscoveredAtReplay` leaves
  an unanswered request on disk, restarts, and sees it delivered.
- **Checked before it is written.** Every file and the notes pass
  `outputGuard.Check` under the reading the owner chose (`422
  quotes_output`, naming the file and its line, for `output`; `409
  check_pending` for `pending`); the credential check is the official
  build's and the service's.
- **Answered by the service.** A `skill` remote record names the
  request's `enrolment` and `op` (not its digest alone): `published` as
  `vN`, or `refused` with the service's words, shown on the page; the
  outbox file is removed at the answer for that enrolment and op only.
- **Publish my changes**: a person may publish an installed place they
  modified: the place's folder through `Snapshot` (the marker left out,
  every other file through the import's checks) as a local version,
  then published. `TestPublishMyChanges` publishes straight from a marked
  installation.
- The publish dialog lists every file and its size once more, the
  notes, and "Visible to the members of <workspace>, and no one else."
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
  readable through a lapse, and new cards are refused); a card's project
  key kept, and the card delivered beyond its person's own devices, only
  while the workspace shares that project, the key dropped from what it
  delivers once the share ends. The person name and the
  `own` mark and its open link on a delivered record are fields a reply
  may gain (follows the sync plan's final text); the open link is a plain
  `https` address of the device's page, with no credential in it. Its
  versioned wire contract gains the card with its vectors: a card at
  every bound, every field of "Never leaves" and every name of "Every
  field whose source holds words" (a title among them) planted as an
  unknown field and refused.
- **PR-11, shared skills**: a store of versions per workspace, each
  immutable and named by its digest, recomputed on arrival; a name's
  owner is the person who first published it, and later versions are
  published by the owner or a workspace admin only, never by a reviewer
  or a guest; labels `v1`, `v2`, … given by the service in order; the same
  digest published again is answered with the version it is; every file
  and the notes under the credential check, a flagged version refused
  whole with the file and line named; a publish request taken only from
  a device whose current enrolment is the request's `enrolment`, and
  published in that enrolment's workspace as its person, its answer
  naming that enrolment and `op`; a name's publications turned
  `publisher_left` when their owner or publisher leaves the workspace;
  **withdraw** by
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
  publish by a reviewer refused, a request whose enrolment is not the
  sending device's current one refused, a publication whose publisher
  left turned `publisher_left`).

### The official build's PRs (PR-12, PR-13)

In the official build's repository, in words. Both add to CS1's sync
provider and carry the outage and lapse tests (article 8).

- **PR-12, team history**: the provider takes `history` documents from
  the adapter, decodes each into its own type field by field (the table
  above; an unknown field refused and logged, never sent), queues them
  in its outbox and sends them; hands received cards to `Host.PutRemote` with
  the device's name, the person's name, `own` and the device's open link
  (a plain `https` address, no credential), and deletes them as the
  service says; and reports nothing new in the sync status. Tests: the outage (the service unreachable mid-session:
  History's search answers `remote: unreachable` and this machine's rows
  unchanged, back with no restart), the lapse (the same with `lapsed`,
  nothing local removed), a card with a planted unknown field never sent,
  and a Playwright run of the official build against its fake sync
  service showing another machine's row in the palette, its card page,
  and the one line once the service is stopped.
- **PR-13, shared skills**: `SyncReport`'s `Enrolment` and
  `WorkspaceName` filled; publish requests from the `skills` source to
  the service, each sent only while the provider's enrolment is the
  request's (removed unsent otherwise, never held), their answers as
  `skill` records naming the enrolment and `op`; a clear or a revocation empties the received
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
| `skills/versions/<digest>/` | one version's content this machine keeps: its manifest and files, written once by a rename of a staged directory; nothing of where it came from | `skills.Store` (Import, Retain) | yes |
| `skills/received/<digest>/` | a version's content the provider handed over, until a person installs it, no `published` record names it (then pruned), or the provider clears | `skills.Store.Put` (PR-8), through `Host.PutSkillVersion` | no (`WhyProvider`) |
| `skills/installed.json` | per name, the version installed, the source the person chose, and each place's target, directory, digest and placement journal | `skills.Store` | as `skills.ExportInstalled` makes it: no journal; a restore places nothing |
| `skills/made.json` | which crew was made from which template version | `skills.Store` (PR-3) | yes |
| `skills/receipts.json` | the operations' receipts, `started`, `done` or `failed`, kept at least 7 days and at most 10,000 | `skills.Store` | yes |
| `skills/outbox/<op>.json` | a publish request, bound to the enrolment it was approved under, until the service answers it, the person cancels it, or the enrolment changes | the publish routes (PR-9) | no (`WhyProvider`) |
| `skills/staging/`, `skills/owner.lock` | a version being made; the lock | `skills.Store` | no |
| holds (memory only) | which sessions of this server read which directories, from before their process starts until it has ended | this server's launches, through `skills.Store.Hold` | no |
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
(none) --install--> [held, source unconfirmed?] refused, nothing written
(none) --install--> writing --> new_in --> cleanup --> placed
placed --install other digest--> [held, expect, seen or source not true?] refused, the place as it was
placed --install other digest--> writing --> old_away --> new_in --> cleanup --> placed
placed --remove--> [held or seen not true?] refused | removing --> (none)
placed --(a file changed by hand)--> modified  (seen by Inspect; nothing written)
modified --install or remove without replace{seen}--> refused, place_modified
launch --Hold(dirs), before the process--> held --(its process has ended)--> released
receipt: started --(last side effect)--> done | failed;  started --(Recover, judged on disk)--> done | failed (interrupted)
```

Every check happens at the person's act, under the place lock, and the
operation completes in that request or does nothing; no operation waits
and nothing is settled later. `Recover`, at `Open`, before any hold can
be taken: for each place with a journal, the table of "The placement
journal", judged by what is on disk; then each `started` receipt, judged
as "Receipts" says.

**A history card** is marked when its record or run commits and built
when it is delivered, so the newest state goes out, and a record forgotten
between the mark and the delivery is delivered as `Deleted`. Within an
epoch the card's revisions rise (I4). A replay lists them all; a store
drops what a completed replay did not list (C6).

**A publish request** (PR-9): written to the outbox after the copied-output
check passed and only when the dialog's enrolment is the status's,
carrying that enrolment; delivered through the `skills` source (listed
by every replay) only while the enrolment is unchanged, and removed
unsent at the first change (`failed (sign_in_changed)`); answered by a
`skill` remote record naming its enrolment and `op` (`published` with
its label, or `refused` with words); the outbox file removed at that
answer, or by **Cancel publishing**. Unanswered, it stays, at most 8,
and the page says "waiting for the sync service since 14:02".

**A received version** (PR-8): `Put` into `received/` (installs
nothing); offered while shown and a `published` record names it;
installed only by a person's operation that passes the source check,
whose first step `Retain`s it into `versions/`; pruned once no
`published` record names it, and taken whole by the provider's clear,
which leaves `versions/`.

**Conflict rules.** None are needed, and none are made: a card has one
writer, the server that holds the record or the run, and the service keeps
its newest revision per (device, kind, id) in an epoch; a version is
immutable, so two versions never merge, and two publishes of one name are
ordered by the service, which refuses one from a person who is neither the
name's owner nor a workspace admin. Dispatch's own rules (CS1's) are
untouched.

**Invariants**, each with the test that holds it:

- **I1, no byte of a card was chosen by a person or an agent.** Every
  string is a random id, a keyed reference, an ordinal, a time, a count,
  a commit id, a digest, a member of a closed set compiled into the
  binary, or, from PR-6, the Dispatch key of a project shared at the
  build; no card field is read from a record's entries, files, tools,
  launch, resumes, folder, repository path, branch, name or working
  title, its host's name, user or owner, the agent's own session, a
  crew's name or id, a run's id as written, a task's or check's id, a
  member's name or a catalog entry the person wrote; no run card field
  from a check's argument list, log, tail or digest, a task's
  instructions or reason, a result's words, a review's verdict or
  findings, a message, a checkpoint or a blocker; a field not certainly
  free of words goes empty; no row adds a title
  (`TestCardFromHostileValues`, `TestACardCarriesNothingTheRecordHeard`,
  `TestARunCardCarriesNoWordsOfTheRun`, `TestHistoryCardFieldsAreTheTable`,
  each over the `history` source's documents).
- **I2, some things never leave.** No card of a Brakeman turn (by the
  record's mark or the run's recorded class, never a crew's id), of a
  record or run kept here, from a switchyard, from a read-only journal,
  with `history.share` off, or with no sync provider registered
  (`TestBrakemanTurnsSendNoCard`, with a person's crew saved as
  `brakeman-2` whose run does send one; `TestKeptHereIsDeleted`,
  `TestShareOffSendsNoCard`, `TestLocalContinuityBuildsNoCard`); no
  project key on a card whose project is not shared, confirmed, at its
  build (`TestCardProjectFollowsTheShare`).
- **I3, built at delivery.** A card is built from the record as it is
  when delivered; a forgotten or pruned record is `Deleted`
  (`TestForgottenRecordsCardIsDeleted`).
- **I4, revisions rise within an epoch, and are no mark.** Every delivery
  of the `history` source, a build or a `Deleted`, takes the next value
  of the source's one counter in its epoch, whatever changed the card
  (its record, its run, its share choice, its project's share)
  (`TestHistoryCardRevisionsRise`); a receiver marks a card by its bytes,
  never its `rev` (`TestRemoteMarkSurvivesASenderEpoch`).
- **I5, hidden is not deleted, and hidden on every read.** While
  `remoteShown()` is false no read of the index returns another
  machine's row, a link to one or a seed that is one, and none is
  removed; they return with the next current status, with no reindex
  (`TestRemoteRowsHiddenThenBack`, `TestRemoteKeyHiddenOnEveryRead`).
  Other machines' entries never evict this machine's
  (`TestRemoteNeverEvictsLocal`).
- **I6, a version is what its digest says, and holds nothing else.**
  Written once by a rename; every file's SHA-256 checked on every read;
  a received version whose digest does not match its files is refused;
  where a version came from is the install's source and the service's
  record, never the version's (`TestVersionIsImmutable`,
  `TestDamagedVersionIsNeverPlaced`, `TestPutRefusesAWrongDigest`,
  `TestOneDigestManySources`).
- **I7, a place is Conductor's only with its marker.** Never written into a
  folder without the marker, through a link, or over a modified place
  without the `seen` the person was shown (`TestNotOursIsNeverTouched`,
  `TestModifiedNeedsTheSeenDigest`, `TestLinkedFolderIsNotOurs`).
- **I8, never under a session this server launched and runs now.** A
  directory a session of this server reads is held from before its
  process starts until its process has ended; a placement into a target
  that is the same directory (`os.SameFile`) as a held one is refused,
  never deferred (`TestPlacementRefusedWhileHeld`,
  `TestOneDirectoryOneTarget`, `TestHoldWaitsForAPlacementsRenames`,
  `TestLaunchBetweenLookAndSwap` and `TestLaunchBeforeRegistration`,
  each interleaving held at a gate). Sessions this server did not launch
  are not held, and the dialog says so.
- **I9, only a person's operation writes a place or a crew, and only as
  approved, at the moment of approval.** A received version, a record's
  arrival and a restore change nothing installed by themselves
  (`TestReceivedVersionInstallsNothing`, `TestRestorePlacesNothing`);
  every check (`expect`, `seen`, holds, the source) is made at the act
  and the operation completes then or not at all; a workspace version is
  installed only while its publication is confirmed (`TestInstallRefusesAnUnconfirmedSource`);
  a template is applied to a crew only by a person, never to one whose
  run is not final, and only over the crew the person saw
  (`TestApplyRefusedWhileRunning`, `TestApplyChecksExpectUnderTheLock`,
  `TestApplyAgainstALaunch`); a template carries no yolo, no member's
  arguments and no inline manifest (`TestTemplateCarriesNoYolo`,
  `TestTemplateCommandsAreAllListed`); an update is offered only within
  a lineage (`TestUpdateOnlyWithinALineage`).
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
  and no session this server launched reads the target in either
  (`TestRecoverAtEveryBoundary`, by a seam that stops before and after
  each recorded step, then `Open`).
- **I12, an outage or a lapse removes nothing local.** History, versions,
  places and the outbox stay as they are (the official build's outage and
  lapse tests; `TestRemoteRowsHiddenThenBack` here).
- **I13, what is offered is the provider's; what is installed is the
  person's.** A received version lives in `received/`, never exported,
  emptied by the provider's clear; an install retains it in `versions/`
  first (`TestClearRemovesReceivedVersionsButInstalled`).
- **I14, a publish request goes only under the sign-in it was approved
  under.** It carries the enrolment the dialog showed, is refused at
  once when the status's differs, is sent only while it is unchanged and
  removed unsent at the first change, is answered by enrolment and `op`,
  and is found again at every replay (`TestPublishRefusesAChangedSignIn`,
  `TestOutboxDroppedAtASignInChange`, `TestOutboxRediscoveredAtReplay`).
- **I15, an operation happens once, or is refused.** Its receipt is
  `started` before its first side effect and `done` or `failed` after
  its last; a replay answers the receipt; a receipt `started` at a crash
  is judged on disk at `Open`; another body under the same `op` is
  `op_reused`; an `op` older than the receipts' 7 days is `op_expired`,
  so no pruned receipt lets an operation run twice
  (`TestReceiptAcrossACrash`, `TestReplayAfterPruneRefused`,
  `TestOpReusedRefused`, `TestCrewFromTemplateOnceAcrossACrash`).

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
  version's arrival or at a restore, or later on a person's behalf: an
  operation the person made completes in their request or not at all.
- Holding a skills directory for sessions this server did not launch (a
  host's sessions, an agent started in a shell, another Conductor server
  under the same home): Conductor cannot know when those end, so it
  holds nothing for them and the install dialog says so.
- Names in words on a history card (a session's, a crew's, a task's, a
  check's, a member's, a branch): a teammate sees ordinals and asks the
  holder.
- Members' arguments and an inline project manifest in a workflow
  template.
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
with another name, kind `workflow` refused with no validator given),
`TestDigestIsTheFiles` (the same files from two folders are one digest;
a mode changed is another), `TestVersionIsImmutable`,
`TestDamagedVersionIsNeverPlaced`, `TestReadOnlyWithoutTheLock`,
`TestPrunePinsEveryReference` (a version held only by one place's
digest, one only by `made.json`: both kept; the bounds filled by pinned
versions: `skills_full`, nothing pinned removed),
`TestReceiptAnswersTheSameOp`, `TestOpReusedRefused`,
`TestReplayAfterPruneRefused`, `TestReceiptAcrossACrash` (for import),
`TestDiffBounds`, `TestWireBoundsHoldTheLargestVersion`.
**`internal/skills`, `internal/agents` (PR-1b, places)**:
`TestNotOursIsNeverTouched`, `TestLinkedFolderIsNotOurs`,
`TestModifiedNeedsTheSeenDigest`, `TestSnapshotLeavesOutTheMarker`,
`TestPlacementRefusedWhileHeld` (a hold on the target: the placement
refused naming its session, nothing written; the hold released: placed),
`TestHoldWaitsForAPlacementsRenames`, `TestOneDirectoryOneTarget`,
`TestRecoverAtEveryBoundary` (a seam that stops before and after each
recorded step of a placement, a first placement and a removal, then
`Open`: `<name>` the old or the new version, never partial, every hidden
folder gone, the receipt `done` or `failed (interrupted)`);
`internal/agents`: `TestSkillDirsFromEveryAdapter` (every adapter with a
`SkillPath` gives its directory once).

**`internal/api` (PR-2)**: `TestSkillsWritesAreTheOwnPersonsOnly`,
`TestImportOpensTheFolderOnce` (the folder through `openCwd`, a folder
outside the allowed roots refused, a link swapped in after the check not
followed), `TestInstallUpdateRemoveReceipts`, `TestInstallRefusesAStaleExpect`,
`TestInstallIsWholeOrNothing` (two targets, one held: nothing placed in
either), `TestHoldsCountTheAgentsThatReadTheTarget` (a Claude Code
session holds `claude`, a pi session holds `agents`, a session with its
own configuration directory holds that directory and not another's, one
whose configuration directory is `~/.agents` holds `agents`, a hosted
session holds nothing, a hold released only once the process has
ended), `TestLaunchBetweenLookAndSwap` and `TestLaunchBeforeRegistration`
(a launch held at a gate after its hold and before `AddReserved`, and a
placement held at a gate between its look and its first rename: the
placement is refused, or the launch waits for the renames),
`TestSkillsUnavailableOnASwitchyard`, `TestGuideHasNoSkillsRow`,
`TestRestorePlacesNothing` (`internal/export` too); **`internal/cli`**:
`TestSkillsCommands` (each command's request and printed receipt, an
in-use refusal naming its sessions), `TestSkillsCLIRefusesInsideASession`,
completion's rows.

**`internal/crew`, `internal/skills`, `internal/api` (PR-3)**:
`TestPortableDropsTheMachine` (id, folder, `where`, origin, yolo,
members' arguments, an inline manifest, times and manifest revision
gone; a file manifest kept as a reference), `TestFromPortableValidates`,
`TestWorkflowNeedsTheValidator` (`internal/skills` with and without the
caller's validator; no import of `internal/crew`),
`TestTemplateRoundTrip` (save, new crew in another folder, the crews
equal but for the machine's fields), `TestTemplateCarriesNoYolo` (the
launched argv), `TestTemplateCommandsAreAllListed`,
`TestTemplateTooLargeRefused`, `TestCrewFromTemplateOnceAcrossACrash`,
`TestBrakemanOriginsOnlyMeanBrakeman`, `TestApplyRefusedWhileRunning`,
`TestApplyChecksExpectUnderTheLock`, `TestApplyAgainstALaunch`,
`TestReceiptAcrossACrash` (for apply and save as template),
`TestMadeFromSurvivesAndForgetsDeletedCrews`, `TestOlderCrewFilesUnchanged`
(no crew file gains a field).

**`web/app` (PR-4)**: vitest `skills.test.ts` (each state's words and
color, which actions each row offers, the in-use line with names),
`useSkills.test.ts` (an `op` id in the receipts' form kept until its
receipt, sent again on a retry) and `TemplateCrewModal.test.ts` (the
setup and check commands listed apart from the JSON, the manifest's line
when the crew refers to it, no yolo or argument control); Playwright
`web/e2e/skills.spec.ts`, the e2e server's own HOME: import a fixture
skill folder made by the spec under a name of its own, install it into
`agents`, see **Placed**, change its `SKILL.md` on disk and see
**Modified here** with its difference, update to a second imported
version and be refused until **Replace my changes**, remove it; **the
in-use refusal**, with the e2e's stub agent launched under Claude Code's
adapter (`conductor.e2e.json` defines it, and the adapter's `SkillPath`
makes it hold `claude`): install into `claude` while it runs and see the
refusal naming it and the place as it was, stop the session, install
again and see **Placed**, each step waited on by what the page draws
(the refusal, the badge), never a clock; save a crew the spec makes as
a workflow template, make a crew from it in another folder (the dialog
lists its check commands), change the crew and see **Changed**.

**PR-5 (`internal/api`, `internal/journal`, `internal/crew/ledger`,
`internal/gitcli`, `ext`)**: `TestCardFromHostileValues` (I1; above),
`TestACardCarriesNothingTheRecordHeard` (a recording `ContinuityStore`
registered; a session whose record holds a marker in every entry kind, a
typed answer sent through `ext.Host`'s `Submit` with a question and
through the host's `answer_as`, a marker in its launch's arguments and
variables' names, its folder, a file path, a tool's name, its host's name
and user and its agent's session id; across a replay and every later
delivery, no document of the `history` source holds a marker),
`TestARunCardCarriesNoWordsOfTheRun` (the same for a run's card: crew
name and id, task and check ids, member names, argv, log, tail,
instructions, a task's reason, result words, verdict and findings, a
message, a checkpoint, none in a `history` document; C6's own `crews/`
and `runs/` documents keep their own contract and their own tests),
`TestHistoryCardFieldsAreTheTable` (`ext`: reflection over the card's
types against the table's patterns), `TestEveryCardBuiltIsValid`,
`TestRunCardCarriesTaskState` (a standing result, a result withdrawn
after its attempt passed, a result imported by a continuation),
`TestHandedNamesTheReceipt` (a receipt of an uncounted attempt and one of
the run a continuation came from, each named as the engine recorded it),
`TestCardBoundIsDeterministic`, `TestHistoryCardRevisionsRise`,
`TestForgottenRecordsCardIsDeleted`, `TestBrakemanTurnsSendNoCard`,
`TestKeptHereIsDeleted`, `TestShareOffSendsNoCard`,
`TestLocalContinuityBuildsNoCard` (with nothing registered, no source is
registered, no card is built, no `git` runs and no file of the data
directory changes), `TestCardRepoAndNoProjectYet` (a plain session in a
repository: its `repo`; every card's `project` and `ticket` empty; a
hosted record with no `repo`), `TestRepoKeyIsTheRootCommits`
(`internal/gitcli`: two full clones, one key; a shallow clone of the
same repository, no key, and its key once deepened, at the next read; a
repository whose HEAD reaches two roots, no key), `TestSyncComputedSourceKeys` (if this PR
makes the mechanism); `internal/journal`: `TestOnWriteAfterCommit`,
`TestOnForgetAfterRemoval`; `internal/crew/ledger`:
`TestCardRowsOneTransaction`; `ext/exttest`: the `Continuity` check's new
sample passes against `local.Continuity` and the fake.

**PR-6 (`internal/history`, `internal/api`, `ext`)**: `TestSchemaTwoRebuildsInPlace`
(a version 1 index cleared and rebuilt; a version 1 reader answering
`other_version`), `TestRemoteKindsAndClasses` (in
`TestClassComesFromTheKindAlone`'s table), `TestRemoteSourceFollowsTheDoors`
(put, delete and clear, each reflected at the next pass),
`TestRemoteMarkSurvivesASenderEpoch`, `TestRemoteNeverEvictsLocal`,
`TestRemoteCardRefusedWhenInvalid`, `TestOpenURLCarriesNoCredential`,
`TestOnFilter`, `TestRemoteRowsOnlyWhenAsked`,
`TestRemoteKeyHiddenOnEveryRead`, `TestSimilarAcrossMachines`
(the same argv digest in the same repository on two devices, passed
first; another repository's left out), `TestRemoteRowsHiddenThenBack` (a
fake sync status: current, unreachable, lapsed, current again; nothing
reindexed), `TestCardProjectFollowsTheShare`, `TestRemoteShown` (if this
PR makes it), `TestRemoteCardRoute`.

**`web/app` (PR-7)**: vitest `historyRemote.test.ts` (a remote row's
badges and words, the card page's sections, own and a teammate's, no
link for an `openURL` that failed the rule, the line for each state);
Playwright `web/e2e/history-remote.spec.ts`: on a plain build with
`remote/` seeded with a card before the server starts, no other
machine's row appears in the palette or on the History page, no **On
every machine** is offered, and the card route and the entry route for
the seeded card's key answer `404`; the section's presence is the
official build's Playwright run (PR-12), since only a registered
provider makes it appear.

**PR-8 (`internal/skills`, `internal/api`, `ext`, `web/app`)**:
`TestPutSkillVersionOnlyForTheSyncStore`, `TestPutRefusesAWrongDigest`,
`TestReceivedVersionInstallsNothing`, `TestAvailableOnlyWhenShown`,
`TestClearRemovesReceivedVersionsButInstalled` (a clear with one received
version installed, one installed then removed, one only offered: the
first two stay in `versions/`, the third is gone from every route and
the export), `TestOneDigestManySources`, `TestUpdateOnlyWithinALineage`
(a workspace version beside a local import of the same name, and beside
one of another owner: shown as another skill, never as an update),
`TestInstallRefusesAnUnconfirmedSource`,
`TestWithdrawnPrunedUnlessPinned`, `TestWithdrawnStaysInstalled`; vitest
for the available list, the update banner, **Another skill named ...**
and each refusal's words; the Playwright spec extended: seeded `skill`
records show nothing on a plain build.

**PR-9 (`internal/api`, `internal/skills`, `ext`, `web/app`)**:
`TestPublishWritesTheOutboxOnlyWhenCurrent`, `TestPublishRefusesCopiedOutput`
(a file quoting a check's log under the reading chosen; a `pending`
verdict refused too), `TestPublishRefusesAChangedSignIn`,
`TestOutboxDroppedAtASignInChange`, `TestOutboxRediscoveredAtReplay`,
`TestOutboxClearedByTheAnswer` (an answer for another enrolment or `op`
leaves it), `TestOutboxBound`, `TestPublishMyChanges`,
`TestReceiptAcrossACrash` (for publish); `TestBrakemanSeesNoOtherMachine`
(after #630); vitest for the publish dialog, its workspace line and the
"Not sent" line.

**The hosted service (PR-10, PR-11)** and **the official build (PR-12,
PR-13)**: their tests are named in their rows above: the contract's
vectors, the scope and revocation rules, the outage and lapse tests, and
the Playwright runs that show what only a registered provider shows.

**By hand** (Acceptance): a real Claude Code session on each of two
machines, two people, a shared project; what tests cannot show is that
the card found is the one a teammate needed, that the commit it names
fixes their failure, and that an install asked while a real agent of
this server runs is refused naming it and made once it ends.

## Boxcar constraints

The eight of the Program, section 2.5:

1. **The engine starts an attempt only through `crew.Launcher`.** Not
   touched: a crew made from a template launches as any crew does.
2. **`session.Local` drives a `session.Process`.** Not touched: a
   launch takes its hold on the skills directories from its agent's
   adapter and its environment before it starts the process, and releases
   it when the session reports its process ended; nothing reads a pid or a tty.
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
   skills are placed on the server's machine only, and a hosted session
   holds nothing (the server cannot know when it ends); the install
   dialog says so. No change to `hostagent`.

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
| PR-1 | open source | skills: versions of kind `skill`, their checks and digest, the store, prune with its pins, receipts with their states and window, the line diff | `internal/skills` (new) | none (`main` as it is) | PR-5 | ~450 + 500 |
| PR-1b | open source | skills, agents: places with their marker, holds by directory, refusal while held, the placement journal, recover, `SkillDirs` | `internal/skills`, `internal/agents` | PR-1 | PR-5 | ~400 + 500 |
| PR-2 | open source | api, cli, export: the Skills routes and who may write, the holds at launch, `conductor skills` | `internal/api`, `internal/cli`, `internal/export`, `docs` | PR-1b | none | ~700 + 750 |
| PR-3 | open source | crew, skills, api, cli: workflow templates (the portable crew without arguments or inline manifest, kind `workflow` through the caller's validator, save as, new crew from, apply) | `internal/crew`, `internal/skills`, `internal/api`, `internal/cli`, `docs` | PR-2 | none | ~550 + 650 |
| PR-4 | open source | web: the Skills page, its install, compare and template dialogs, templates on the crew pages | `web/app`, `web/e2e` | PR-3 | PR-5 | ~850 + 650 |
| PR-5 | open source | api, journal, ledger, gitcli, ext: history cards to the sync store (no project key yet), the computed sources, `history.share`, kept here | `internal/api`, `internal/journal`, `internal/crew/ledger`, `internal/gitcli`, `internal/config`, `internal/export`, `ext`, `ext/exttest`, `docs` | HS1's PR-3 (#615) on `main` (after #613 and #614); never beside HS2's PR-5 | PR-1, PR-1b, PR-4 | ~650 + 700 |
| PR-6 | open source | history, api, ext: other machines' cards in the index (schema 2, their own budget, `Remote` on every read, marks by bytes), `on:`, `Similar` across machines, `remoteShown()`, the project key from the share | `internal/history`, `internal/api`, `ext`, `web/app/utils/protocol.ts`, `docs` | PR-5; HS1's PR-4 (#616); CS1's sharing (OS-1b) and its remote records and sync status (OS-4) on `main`; never beside HS2's PR-5 | none | ~750 + 800 |
| PR-7 | open source | web: other machines in History, the card page, the one line, keep here | `web/app`, `web/e2e` | PR-6; HS1's PR-7 (#621) | none | ~550 + 550 |
| PR-8 | open source | skills, api, ext, web: the workspace's versions, received apart, publications as records, `PutSkillVersion`, the source check at install, updates within a lineage | `internal/skills`, `internal/api`, `ext`, `ext/exttest`, `web/app`, `web/e2e`, `docs` | PR-4, PR-7; CS1's remote records on `main` | none | ~700 + 700 |
| PR-9 | open source | api, skills, ext, web, docs: publishing bound to its enrolment, the features entry | `internal/api`, `internal/skills`, `internal/export`, `ext`, `web/app`, `docs` | PR-8; HS1's PR-11 (#630); the owner's answer on what counts as terminal output; CS1's check of copied output on `main` | none | ~550 + 650 |
| PR-10 | hosted service | team history: cards kept per device, scope by shared project, delivery with names, revocation, export, contract | the sync service, its store and contract | CS1's service rows that serve remote records; ID1's members and devices; an open-source tag holding PR-9's `ext` types | none | ~600 + 700 |
| PR-11 | hosted service | shared skills: versions per workspace, owners, publish rules, withdraw, delivery, export, contract | the sync service, its store and contract | PR-10; an open-source tag holding PR-9's | none (same service) | ~650 + 700 |
| PR-12 | official build | the sync provider: cards out, cards in, the outage and lapse tests, the Playwright run | the sync provider | PR-10 deployed to staging; CS1's provider rows; an open-source tag holding PR-9's | PR-11 (other repository) | ~400 + 600 |
| PR-13 | official build | the sync provider: publish requests, received versions, the outage and lapse tests, the Playwright run | the sync provider | PR-11 deployed to staging; PR-12 | none | ~400 + 550 |

**PR-1**: `internal/skills`'s versions of kind `skill`: the version and
its checks, the digest, the store with its lock, staging and prune with
its pins, the receipts with their states, fingerprints and window, the
line diff, the wire sizes. No place, no route, no template, nothing
calls it yet. Tests: PR-1's above.

**PR-1b**: `internal/skills`'s places: the marker, `Inspect`,
`Snapshot`, the holds by directory and the refusal while held, `Place`,
`Remove`, the placement journal and `Recover`; `agents.SkillDirs` and
`ConductorSkill`. Nothing calls it yet. Tests: PR-1b's above.

**PR-2**: the routes with their bounds and `requireOwnPerson`; the holds
taken at every launch of this server before its process starts and
released when it has ended; the CLI with its refusal
inside a session, and its completion; the export's rows and
`skills.ExportInstalled`; `docs/protocol.md`'s Skills section;
`docs/repository-map.md`'s row. After it, a person can import, install,
update, compare and remove skills from the CLI.

**PR-3**: `internal/crew/portable.go` (no yolo, no members' arguments,
no inline manifest) and `ValidatePortable`, kind `workflow` in
`internal/skills` through the caller's validator, `CreateFrom`'s
template origin, `UpdateIf`, `Engine.WhileNoRun`, `skills.ImportBytes`
and `made.go`, the three routes, the CLI's three commands, the protocol
rows.

**PR-4**: the page, its dialogs (the template dialog with its commands
apart), the crew page's two actions, the navigation and the palette;
`skills.spec.ts` with the in-use refusal. After it the free half of the
item is shipped; Acceptance part C can be observed.

**PR-5**: `ext.HistoryCard` and its `Valid`; the card key and the
references; the adapter's computed sources (made here unless HS2's PR-5
merged first, then only registered) and the `history` source, marked by
the journal's two new hooks, the ledger's and the share choice; its
revisions; the card builder with its repository map and no project key;
`ledger.CardRows`; `gitcli.RootsOf` with its shallow and single-root
rule; `history.share` and the share route; the contract suite's sample;
the sync section of `docs/protocol.md`. Nothing is built or sent without
a provider, and no name in words is ever on a card. HS1's PR-12 (#632, what a host reports)
touches `internal/journal`'s hosted fields: whichever merges second
rebases, never beside it.

**PR-6**: the remote kind `history` with its record's fields and the
`openURL` rule, the index's schema 2 and remote kinds, other machines'
budget, the remote source marked by its bytes and its hook on the
doors, the `Remote` option on every read, `on:`, `Similar`'s workspace
scope, `remoteShown()` (made here unless HS2's PR-5 merged first, then
only read), the entry's `origin`, the card route, the project and ticket
keys from the Dispatch store's share with the marks of its changes, the
protocol rows. HS2's PR-5 touches `internal/api` and `ext` too: never
beside it.

**PR-7**: the History page's and the palette's remote rows, the
**Machine** chip, **On every machine**, the card page, the one line, keep
here on the record and run pages, `history-remote.spec.ts`.

**PR-8**: `ext.SkillVersion` and its records, `Host.PutSkillVersion`,
the remote kind `skill` as publications, `skills.Store.Put` into
`received/` (content only), `Retain`, `ClearReceived` on the provider's
clear, `Install.Source` and the source check at install, withdrawal and
a publisher who left, the reply's `available` and its lineage rule, the
page's Available sections, banner, refusals and **Another skill named
...**, the fakes; the protocol rows.

**PR-9**: `SyncReport`'s `Enrolment` and `WorkspaceName`, the publish
routes, the outbox bound to its enrolment and dropped at a change, the
`skills` computed source, `outputGuard.Check` over files and notes,
**Publish my changes** through `Snapshot`, the publish dialog,
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
   field"); a card holds no free text (I1), so the check of copied
   output is asked of published files and notes only.
5. **A card's scope follows its Dispatch project's share.** This plan's
   PR-10, reading the share the sync service already keeps.
6. **The provider passes this item's documents to its own builders.**
   This plan's PR-12 and PR-13.
7. **The check of copied output over any text.** Answered:
   `outputGuard.Check(ctx, text)` is one function over one string
   (`clean`, `output`, `pending`), which PR-9 calls over each published
   file and the notes (a card carries no title to check).

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
   this plan follows its default, and no card depends on the answer.**
   A card carries no name in words under either reading (I1: no byte of
   a card was chosen by a person or an agent, `TestCardFromHostileValues`),
   since no check makes a free name certainly free of a credential or a
   copied line; so there is no free text on a card for the answer to
   judge. The answer governs publishing alone: under the default a
   published skill's files and notes pass the check of copied output;
   under the alternative publishing is unchanged, since a person
   publishes every file after seeing it and is its writer for this rule.
   **No row changes under the alternative.**
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
    as decision 11), for the workspace and as the person it is signed in
    as when the dialog was drawn (its enrolment), and is never sent under
    another sign-in: a change removes it unsent;
    the service takes it from a workspace's operators and admins only; a
    name's owner is its first publisher, and later versions come from the
    owner or an admin; a reviewer or a guest publishes nothing. A version
    is withdrawn by the name's owner or a workspace admin, signed in to
    the hosted service's own pages; no route on a member's server
    withdraws. Withdrawing, or its owner or publisher leaving the
    workspace, stops offering it and refuses any install of it, and
    leaves what a person installed in place, marked withdrawn. A
    workspace version is installed only while the sync status is current
    and the service's record confirms it published with the digest of the
    files here; anything less is refused, and the page says which.
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
    or a restore, never later on the person's behalf: every check is made
    at the act, which completes then or does nothing; a directory a
    session this server launched reads is refused while that session
    runs, and the person installs again once it ends; sessions this
    server did not launch (a host's, a shell's, another server's) are not
    held, and the dialog says so; a place changed by hand
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
    folder, `where`, origin, yolo, members' arguments, an inline project
    manifest and times; with its tasks, checks and setup, which New crew
    from template and Apply list apart from the JSON before anything is
    made (and which pass the credential check at publishing); a manifest
    in the repository stays a reference to it, named by the dialog.
    Arguments do not travel because an approval bypass rides in them and
    no list of such flags is certainly complete; the person adds a
    crew's arguments on its page. A crew made from one asks for approvals unless its
    person turns that off on the crew. It is applied to a crew only by a
    person, over the crew the person saw, never to a crew whose run is
    not final.
15. **Skills are placed on the server's own machine only**, never under
    a session this server launched and runs now; a host's own machine
    and a Runtime's guest are a later item's, and a host's sessions on
    this machine are not held (decision 11).
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
   **Find this elsewhere → On every machine** lists, first, "check 1 of
   task 2 passed at <commit> · on mac · <owner>", with the attempt that
   was handed attempt 1's failure.
3. The row opens its card page: the agent, the ticket (opened here, since
   the project is shared), the commits, the tasks and their outcomes, "held
   on mac (<owner>'s machine)", and no link into the Mac. The member
   fetches the commit with git and the check passes.
4. Nothing the Mac's session said or printed reached the Windows machine
   or the service as history: the marked strings of step 1 are not in
   the Windows server's `remote/<device>/history-*.json` files, its index
   or the cards of the workspace's export.
5. Neither the session's name nor the crew's is found with `~` on the
   Windows machine, and neither is in its `history` remote files.
6. Mid-session, the sync service stopped by the owner in staging: on the
   Windows machine the History page shows the one line, the local rows
   are unchanged, and the Mac's rows return when the service does, with
   no restart; the same with the subscription lapsed in staging.

**Part B, shared skills and templates, after PR-11 and PR-13**: the owner
publishes a skill (a `SKILL.md` and a script) and a workflow template
from the Mac; on the Windows machine both show as available with their
files, owner and digest; an install into Claude Code while a Claude
Code session of that server runs is refused naming it, and made once
that session ends; a hand edit shows as **Modified here**; the owner publishes a
second version, which shows as an update with its difference, and the
member's edit is replaced only when they say so; a crew made from the
template runs, its check commands listed before it was made and no
yolo and no member's arguments on it. A reviewer member's publish is
refused; a publish dialog left open while the owner's server signs in
to another workspace is refused at its act. The member, signed
in to the owner's server through the identity provider, sees its Skills
page and is refused every install there. Then the owner withdraws the
second version on the hosted service's pages: it is no longer offered
and can no longer be installed, and the member's installed copy of it
stays, marked withdrawn. Then the
service stopped: the update is hidden behind the one line and the
installed skill stays placed.

**Part C, the free half, after PR-4**, on a plain build with nothing
registered: import, install, modification, update and removal of a skill
in the agents' directories of the LAN Linux box, the in-use refusal with
a real Claude Code session, and a workflow template saved and made a crew.

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

## Changed in review round 2

Codex round 2's fourteen findings, answered on #686. Six said a round-1
fix did not hold; rather than add machinery to repair each case, this
round takes one rule per promise and fails closed. **A field of a card
that is not certainly free of words does not leave; a skill version that
is not certainly the one published and reviewed is not installed; an
operation happens at the person's act or not at all.** A teammate
missing a card, a name or an install is an inconvenience; a credential
on a card or code placed without its person is the defect.

**Mechanisms removed.**

- **Waiting placements**: the `waiting` state, `Settle`, `Cancel`, the
  cancel route and CLI command, the approval a waiting operation kept
  (`expect`, `seen`) and its check at settle, a settle after a restart,
  and `TestSettleRechecksWhatWasApproved`,
  `TestSettleAfterTheLastSessionEnds`, `TestNewSessionKeepsItWaiting`,
  `TestCancelLeavesThePlace`, `TestSettleRunsOffTheHook`,
  `TestSettleRefusesAWithdrawnVersion`. An install into a directory a
  session of this server reads is refused, naming the sessions
  (`TestPlacementRefusedWhileHeld`).
- **Holds for hosted sessions** and their narrowing by HS2's machine key
  (PR-8's dependency on HS2's PR-1 goes with it).
- **Placement at a launch** into a configuration directory: such a
  directory is a target a person installs into.
- **Titles on cards** (PR-9's `name`, `crewName`, `member`, `taskName`,
  `checkName`, `branch`, `agentName`), `TestTitlesFollowTheReading`, the
  two reading-dependent columns of the field table, and the title rules
  of PR-10 and PR-12.
- **Provenance inside a version** (`Label`, `Notes`, `Origin` with
  `Withdrawn` in `version.json`) and `Withdrawn(digest)`.
- **Holding a publish request** across another sign-in, with its "signed
  in elsewhere" line.
- **Members' arguments and an inline manifest** in a workflow template.

**Promises narrowed.**

- A skill is never placed under a session **this server launched and
  runs now**, no longer "under any running session": hosted sessions,
  shell agents and other servers' sessions are named in the dialog as
  unseen (Codex 3).
- A card never carries a name in words, under either reading of the
  owner's question; teammates see ordinals.
- A card's `repo` is set only for a full clone whose HEAD reaches one
  root (Codex 13); its `project` only for a project shared, confirmed,
  at the build, and not before PR-6 (Codex 11); the agent's version only
  as digits and dots.
- A workflow template carries no member's arguments and no inline
  manifest (Codex 5, 6).
- A workspace version installs only while the status is current and its
  publication is confirmed published with the digest here (Codex
  2).
- A publish goes only under the enrolment the dialog showed, and is
  removed unsent at any change (Codex 1).
- A replay older than 7 days is refused rather than answered (Codex 12).

**The findings.**

- **1, publication bound to the dialog's sign-in**: `SyncReport` gains
  `Enrolment` and `WorkspaceName` (this plan's own fields, PR-9, filled
  by PR-13); the body carries the dialog's `enrolment`, refused `409
  sync_changed` when it differs; the outbox, the provider and the service
  each compare it; I14; `TestPublishRefusesAChangedSignIn`,
  `TestOutboxDroppedAtASignInChange`.
- **2, content apart from provenance**: a version is content only;
  publications are the service's `skill` records keyed by workspace, name
  and label; an install records its `Source`; updates and withdrawal are
  judged by source; `TestOneDigestManySources`.
- **3, holds across processes and host lifetimes**: holds are this
  server's own launches only, released when the process has ended; a
  held target refuses; the rest is the dialog's line (above).
- **4, one directory one target**: targets compared by `os.SameFile` on
  resolved directories, `dir:<hash>` for others; `TestOneDirectoryOneTarget`.
- **5, approval bypass in arguments**: no member's arguments travel;
  `TestTemplateCarriesNoYolo` checks the launched argv.
- **6, manifest commands**: no inline manifest travels, so setup and
  checks are every command; a repository manifest is named;
  `TestTemplateCommandsAreAllListed`.
- **7, remote marks**: the SHA-256 of the remote file's bytes;
  `TestRemoteMarkSurvivesASenderEpoch`.
- **8, remote eviction**: other machines' entries have a budget of
  their own and never evict this machine's; `TestRemoteNeverEvictsLocal`.
- **9, direct reads**: `Remote` on every index read (`Entry`, `Links`,
  `Similar` and its seed), set only while shown;
  `TestRemoteKeyHiddenOnEveryRead`.
- **10, PR-1's forward dependency**: PR-1 takes skills only; PR-3 adds
  `workflow` through a validator the caller gives, so `internal/skills`
  never imports `internal/crew`.
- **11, the project share**: no project key in PR-5; PR-6, after CS1's
  OS-1b and OS-4, reads the share at the build and marks cards on its
  changes, removal included; `TestCardProjectFollowsTheShare`.
- **12, replay after pruning and crashes**: receipts `started`, `done`,
  `failed`, a fingerprint, a 7-day window on timed `op` ids, recovery
  judged on disk; I15.
- **13, repository roots**: shallow and multi-root repositories give no
  key; the cache lasts an hour.
- **14, the no-words tests**: scoped to the `history` source's
  documents; C6's crew and run documents keep their own contract.
