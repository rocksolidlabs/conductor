# Task "build" for lead (attempt 2 of 3)

Conductor runs the crew "Build crew" toward this goal:

Ship the parser, tested.

Your role: You lead the build.

## Instructions

Write the parser in parse.go & test it: <input> yields a tree.

## Where you are

Your worktree is `/repo/.conductor/worktrees/r-0123456789ab/build.2`, on the branch `crew/r-0123456789ab/build.2`, at the commit dddddddddddddddddddddddddddddddddddddddd (task "design"'s result). The crew works in `svc` inside it; that is your working directory.

## Inputs

The results this task starts from:

- task "design", attempt 1: commit dddddddddddddddddddddddddddddddddddddddd on the branch `crew/r-0123456789ab/design.1`: The grammar, in grammar.md.

## Checks

After you submit, Conductor runs these in a fresh checkout of your result commit. Each must exit 0 without changing tracked files:

- test: `go test ./...` (600s)
- vet: `go vet ./...`

## The attempt before

Attempt 1 failed at check test: exit 1: parse_test.go:12: want a tree

## How to finish

Commit your work on this branch. Then submit once: the `task_result` tool with a summary, or `conductor task result --summary "..."`. Conductor takes the commit at HEAD; a worktree with changes not committed (apart from .conductor/) is refused, and so is a result that does not descend from every input.

A done or a handoff does not finish a task. A question goes through Conductor's ask, and a person answers it.
