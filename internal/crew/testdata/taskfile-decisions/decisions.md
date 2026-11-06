# Task "build" for lead (attempt 2 of 3)

Conductor runs the crew "Build crew" toward this goal:

Ship the parser, tested.

Your role: You lead the build.

## Instructions

Write the parser in parse.go & test it: <input> yields a tree.

## Where you are

Your worktree is `/repo/.conductor/worktrees/r-0123456789ab/build.2`, on the branch `crew/r-0123456789ab/build.2`, at the commit cccccccccccccccccccccccccccccccccccccccc (attempt 1's checkpoint).

## Decisions

The decisions of this task, asked by its attempts or holding it:

- dr-1 (approval), applied: Merge the schema change? Nate answered: Approve. Why: the migration is reversible.
- dr-2 (clarification, the plan's colour), applied: Which colour? Nate answered: blue.
- dr-3 (approval), superseded: Ship build's first result? dr-3 was superseded: the candidate is now 9f2c1ab, attempt 3; ask again if you still need it. Its answer is not current: never act on it.
- dr-4 (clarification), open: Which index? Conductor holds this task until the operator's answer is applied.

## Where the last attempt got to

Its checkpoint, at the commit cccccccccccccccccccccccccccccccccccccccc, where this attempt starts:

- Done: The lexer.
- Remaining: The parser.
- Notes: Keep the tokens small.

## The attempt before

Attempt 1 failed at session: the session ended

## How to finish

Commit your work on this branch. Then submit once: the `task_result` tool with a summary, or `conductor task result --summary "..."`. Conductor takes the commit at HEAD; a worktree with changes not committed (apart from .conductor/) is refused, and so is a result that does not descend from every input.

A done or a handoff does not finish a task. A question goes through Conductor's ask, and a person answers it.
