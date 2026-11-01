# Task "build" for lead (attempt 3 of 3)

Conductor runs the crew "Build crew" toward this goal:

Ship the parser, tested.

Your role: You lead the build.

## Instructions

Write the parser in parse.go & test it: <input> yields a tree.

## Where you are

Your worktree is `/repo/.conductor/worktrees/r-0123456789ab/build.3`, on the branch `crew/r-0123456789ab/build.3`, at the commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb (the commit review "check" asked changes of).

## Findings to address

Review "check" (attempt 1) asked for changes to the commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb, where this attempt starts: Two gaps in the tests..

- major parse.go:40: An empty input panics.
- nit: Name the error.

Address each finding, or say in your summary why not; the review runs again on your result.

## The attempt before

Attempt 2 succeeded

## How to finish

Commit your work on this branch. Then submit once: the `task_result` tool with a summary, or `conductor task result --summary "..."`. Conductor takes the commit at HEAD; a worktree with changes not committed (apart from .conductor/) is refused, and so is a result that does not descend from every input.

A done or a handoff does not finish a task. A question goes through Conductor's ask, and a person answers it.
