# Task "check" for lead (attempt 1 of 3)

Conductor runs the crew "Build crew" toward this goal:

Ship the parser, tested.

## Instructions

Write the parser in parse.go & test it: <input> yields a tree.

## Where you are

Your worktree is `/repo/.conductor/worktrees/r-0123456789ab/check.1`, on the branch `crew/r-0123456789ab/check.1`, at the commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb (task "build"'s result).

## Inputs

The results this task starts from:

- task "build", attempt 2: commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb on the branch `crew/r-0123456789ab/build.2`: The parser and its tests.

## How to finish

Do not commit. Review the candidate at HEAD, the input's result. Then submit once: the `task_result` tool with the verdict `approved` or `needs_changes` and your findings (severity blocker, major, minor or nit; path; line; message), or `conductor task result --verdict <verdict> --finding ...`.

A done or a handoff does not finish a task. A question goes through Conductor's ask, and a person answers it.
