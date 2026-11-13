# Task "review-b" for rev-b (attempt 1 of 3)

Conductor runs the crew "Compare the cache" toward this goal:

Make the user list fast.

Your role: You review candidate B of 3: you judge its result against the objective and the criteria, and change nothing.

## Instructions

Review this candidate's result at HEAD against the objective and each criterion your task file lists; run what you need to judge it, and change nothing. Approve when it meets the objective and the criteria; otherwise ask for changes, with a finding for each criterion it misses and each problem, naming its file and line.

## A comparison

You review approach-b, one of 3 candidates for this objective, each of which started at aaaaaaaaaaaa and worked alone. Judge its result against the objective and the criteria below and change nothing. Do not read, fetch or merge another candidate's branch or worktree: you review this one alone.

Its approach: drop the cache and memoise in the handler

The criteria it is compared on:

- no-deps: No new dependency.
- p99: p99 of the cache read under 2 ms in bench_test.go.

## Where you are

Your worktree is `/repo/.conductor/worktrees/r-0123456789ab/review-b.1`, on the branch `crew/r-0123456789ab/review-b.1`, at the commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb (task "approach-b"'s result).

## Inputs

The results this task starts from:

- task "approach-b", attempt 1: commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb on the branch `crew/r-0123456789ab/approach-b.1`: Memoised in the handler.

## Context

No derived context: a comparison's candidates and reviews read their task, the base and their own work.

## How to finish

Do not commit. Review the candidate at HEAD, the input's result. Then submit once: the `task_result` tool with the verdict `approved` or `needs_changes` and your findings (severity blocker, major, minor or nit; path; line; message), or `conductor task result --verdict <verdict> --finding ...`.

A done or a handoff does not finish a task. A question goes through Conductor's ask, and a person answers it.
