# Task "merge" for lead (attempt 1 of 3)

Conductor runs the crew "Build crew" toward this goal:

Ship the parser, tested.

Your role: You lead the build.

## Instructions

Write the parser in parse.go & test it: <input> yields a tree.

## Where you are

Your worktree is `/repo/.conductor/worktrees/r-0123456789ab/merge.1`, on the branch `crew/r-0123456789ab/merge.1`, at the commit 1111111111111111111111111111111111111111 (task "lexer"'s result).

## Inputs

The results this task starts from:

- task "lexer", attempt 1: commit 1111111111111111111111111111111111111111 on the branch `crew/r-0123456789ab/lexer.1`: The lexer.
- task "parser", attempt 2: commit 2222222222222222222222222222222222222222 on the branch `crew/r-0123456789ab/parser.2`
- task "check", attempt 1: commit 2222222222222222222222222222222222222222, verdict approved: Approved.

Your worktree starts at the first; merge the branches of the others into it before you submit. Your result must descend from every input.

## How to finish

Commit your work on this branch. Then submit once: the `task_result` tool with a summary, or `conductor task result --summary "..."`. Conductor takes the commit at HEAD; a worktree with changes not committed (apart from .conductor/) is refused, and so is a result that does not descend from every input.

A done or a handoff does not finish a task. A question goes through Conductor's ask, and a person answers it.
