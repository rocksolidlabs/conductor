# Contributing to Conductor

Conductor runs coding agents in real terminals, driven from the browser and
shared by link; Yardmaster, its agent harness, runs crews of them as managed
runs. This file is for anyone who wants to change it: what to read first,
who decides, how an issue becomes a pull request, the sign-off every commit
carries, the checks, the open-core rules and the licence.

## Before you start

The contract is `AGENTS.md`, read whole: its "Working an issue" section is
the process, and the rest is what every change keeps true. Then:

- `docs/repository-map.md` for where things are and how to add an agent, a
  route or a control message;
- `docs/checks.md` for the checks, by affected surface;
- `docs/plans/` for a change larger than one pull request: a plan there is
  reviewed before its code.

## Who decides

The maintainers are the owners of the `rocksolidlabs` GitHub organization
(today two accounts; the organization's `Owners` team lists them). A
maintainer merges, approves a plan, decides when a defect outside a stream
is worked, and decides a vulnerability's disclosure. The `ready` label on an
issue is the maintainers' go-ahead. Everything else is any contributor's.
`AGENTS.md` says "a maintainer" wherever it means this role.

## Issues and pull requests

GitHub Issues are the backlog. One issue, one branch `issue-N` cut from
`main`, one pull request: a draft pull request at the first commit;
`Fixes #N` finishes the issue and `Part of #N` leaves it open; the body as
the template lays it out, with who made it (a person or an agent, with a
session link when there is one); every review comment answered, fixed or
declined with why, and its thread resolved. Never two open issues at once.
The `ready` label is the go-ahead for everyone.

With write access, which the maintainers and the agents they run have,
`AGENTS.md`'s bullets apply as written: assign yourself, cut the branch on
origin with `gh issue develop N --name issue-N` (the branch on origin is the
claim), and keep the Project board current.

Without it, which is every contributor outside RockSolid Labs at first: say
on the issue that you are taking it, and a maintainer assigns it to you (the
assignment is the claim) and moves it on the board for you. Fork the
repository, cut `issue-N` from `main` (your fork's, brought up to date with
this repository's), and open the pull request from the fork, where CI runs
as on any other pull request, the `dco` job included (a first-time
contributor's first run may wait for a maintainer's approval, as GitHub has
it). Handing off, say so in a pull request comment, and a maintainer
unassigns you.

## Sign-off

Every commit is signed off. `git commit -s` adds the line

    Signed-off-by: Your Name <you@example.org>

to the message, and by adding it you certify the Developer Certificate of
Origin, whose text is in `DCO` at the root of this repository. In short: you
wrote the change, or have the right to submit it under this repository's
licence, and you know that the contribution and your sign-off are public and
kept. The name and address must be the commit author's, as `git config
user.name` and `user.email` give them. There is no contributor licence
agreement.

CI's `dco` job fails a pull request with a commit that lacks a matching
sign-off, naming the commit. Check before pushing:

    git fetch origin && python3 scripts/dco_check.py --base origin/main --head HEAD

The fetch is part of the check: it judges `origin/main..HEAD`, and an
`origin/main` behind GitHub's main, which only a fetch moves, puts main's
own commits in that range, which the check names and the fix below would
replay as the branch's. `make preflight` fetches `origin/main` and runs the
check first.

**A branch of your own commits, with no merge commit on it:**

    git rebase --signoff origin/main
    git push --force-with-lease

For the last commit alone, `git commit --amend -s --no-edit` (a merge at the
tip too: amending keeps its parents and its tree). The base is named on
purpose: a bare `git rebase --signoff` rebases onto the branch's own
upstream, which already holds the commits, and signs nothing.

**Someone else's commit.** `git rebase --signoff` and `git commit -s` sign
as the person running them (`user.name`, `user.email`), whatever the
commit's author: on someone else's commit they add a sign-off that is not
the author's, and the check still fails. A commit by someone else is theirs
to sign: the author runs the fix on the branch and pushes it, or writes the
line, `Signed-off-by: Name <address>` as their commits name them, on the
pull request, and whoever repairs the branch adds it to that commit's
message (`git rebase -i origin/main`, `reword`).

**A merge commit on the branch.** It is a commit of the pull request and is
checked like any other (`git merge --signoff` signs one; GitHub's "Update
branch" button makes one nobody signed), so rebase instead of merging. A
branch that has one anyway is repaired without losing what the merge
carried. A rebase drops the merge and replays the commits of both sides,
and what the merge held of its own goes with it: a conflict it resolved
comes back during the replay and stops the rebase to be resolved again; an
edit made in the merge itself is dropped without a word (`--rebase-merges`
is no help: it makes the merge again, without that edit and without a
sign-off). So note the tip, rebase in place, and compare:

    before=$(git rev-parse HEAD)
    git rebase --signoff $(git merge-base origin/main HEAD)
    git diff --stat $before HEAD

The diff prints nothing when nothing was lost. Otherwise

    git restore --source=$before --staged --worktree :/
    git commit -s

put back what the diff showed as a commit of its own, and the diff is
empty. Then the check above, and the push with `--force-with-lease`.

**An agent's commits** are the commits of the person running it: they carry
that person's sign-off. The agent's attribution lines (`Co-Authored-By`, a
session link) are something else and do not replace it.

## Checks before a push

`docs/checks.md` lists the checks by affected surface: Go race tests for a
server or CLI change, vitest and Playwright for the web app, both for a
change that spans both, the scripts suite for the Python tooling. Run them
before every push. The pull request's Verification section lists what ran
and what did not: a check that did not run is "not run", never "passed".
CI is the only gate, and a maintainer merges.

## The open-core rules

Conductor is open core. This repository is the whole free product, built
and useful on its own; RockSolid Labs builds paid features outside it, as
providers registered through extension points and as hosted services. Four
rules hold here, and the pull request template's first four boxes cite them
by number:

1. No paid code, licence check or plan check in this repository: the only
   gate it knows is whether a provider is registered.
2. The open-source build stays fully useful on its own: nothing free is
   removed or degraded.
3. Nothing paid hides behind a flag: a paid feature needs its hosted
   service, through a registered provider.
4. Stop, inspect, revoke, export and required approvals are free and never
   degrade.

A pull request that adds a licence check, a plan check, a flag for something
paid or a paid feature's code, or that removes or weakens a free safety
control, is declined whatever else it does. Paid work does not go here: it
is RockSolid Labs' private code and services, reached through the
registered providers. Write to conductor@rocksolidlabs.io to talk about it.

## The pull request checklist

The template puts five boxes and one line in every pull request body. Tick
a box only when it is true of the change:

- **Rule 1.** The diff adds no licence check, plan check or paid code; a
  behaviour behind a provider is reached by asking whether the provider is
  registered, and by nothing else.
- **Rule 2.** Nothing free is removed, hidden or made worse for a build
  with no provider registered.
- **Rule 3.** No flag, setting or build tag turns on anything paid.
- **Rule 4.** Stop, inspect, revoke, export and required approvals are
  untouched, or still free and whole.
- **The checks.** Every affected surface's checks ran and are listed under
  Verification, one that did not run named as such; a new dependency has
  its reason in the body, and `make notices` ran and its result is
  committed.

The line under the boxes says that every commit is signed off by its
author; the `dco` check proves it.

## Rules for code

`AGENTS.md` has them in full; the short list:

- Commands are argv arrays; never a shell string built from input.
- Tokens are compared with `share.Equal` and stored as hashes; query
  strings are never logged.
- Session working directories go through `resolveCwd` (a launch through
  `openCwd`); file reads and saves go through `session.ResolvePath` and the
  session's `os.Root`, never a path opened again.
- Per-connection bounds stay: read limits, queues, in-flight file requests.
- A protocol change touches `internal/proto`, `web/app/utils/protocol.ts`
  and `docs/protocol.md` together, and every new frame or message gets a
  size limit and a test.
- A new dependency needs its reason in the pull request and `make notices`,
  committed with it.
- Code in this repository never asks the entitlement source
  (`ext.EntitlementSource`) whether a feature is allowed: only the paid
  providers do, outside it; a free feature has no entitlement to ask about.
  `make lint` fails on a call site outside `ext/` (the `entitlements` rule
  of `internal/opencore`).
- Every shipped feature gets its `docs/features.md` entry.

## Licence, marks, security

Contributions are under the Apache License 2.0 (`LICENSE`), the licence of
this repository; there is no copyright assignment. The marks (the Conductor
name, the junction mark and the logos, and the RockSolid Labs name and
logos) are `TRADEMARKS.md`'s: it names their owner and says what may be done
with them without asking and what may not. A vulnerability is `SECURITY.md`'s:
it goes through the private channel named there, never an issue, a pull
request or a public comment.
