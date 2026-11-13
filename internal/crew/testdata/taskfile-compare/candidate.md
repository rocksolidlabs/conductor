# Task "approach-b" for cand-b (attempt 1 of 3)

Conductor runs the crew "Compare the cache" toward this goal:

Make the user list fast.

Your role: You are candidate B of 3 for this objective. You work alone, from the run's base, in a worktree of your own: another candidate's work is never an input of yours.

## Instructions

Make GET /v1/users answer from a cache.

## A comparison

You are approach-b, one of 3 candidates for this objective, each starting at aaaaaaaaaaaa and working alone. Do not read, fetch or merge another candidate's branch or worktree; nothing of theirs is an input of yours.

Your approach: drop the cache and memoise in the handler

The criteria your result is compared on:

- no-deps: No new dependency.
- p99: p99 of the cache read under 2 ms in bench_test.go.

## Where you are

Your worktree is `/repo/.conductor/worktrees/r-0123456789ab/approach-b.1`, on the branch `crew/r-0123456789ab/approach-b.1`, at the commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa (the run's base).

## Context

No derived context: a comparison's candidates and reviews read their task, the base and their own work.

## Checks

After you submit, Conductor runs these in a fresh checkout of your result commit. Each must exit 0 without changing tracked files:

- ok: `true`

## How to finish

Commit your work on this branch. Then submit once: the `task_result` tool with a summary, or `conductor task result --summary "..."`. Conductor takes the commit at HEAD; a worktree with changes not committed (apart from .conductor/) is refused, and so is a result that does not descend from every input.

A done or a handoff does not finish a task. A question goes through Conductor's ask, and a person answers it.
