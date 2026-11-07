# Task "build" for lead (attempt 4 of 4)

Conductor runs the crew "Build crew" toward this goal:

Ship the parser, tested.

Your role: You lead the build.

## Instructions

Write the parser in parse.go & test it: <input> yields a tree.

## Where you are

Your worktree is `/repo/.conductor/worktrees/r-0123456789ab/build.3`, on the branch `crew/r-0123456789ab/build.3`, at the commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa (where attempt 3 started: its result, which Nate asked changes of, was refused before its checks).

## Findings to address

Nate asked for changes to the commit cccccccccccccccccccccccccccccccccccccccc (review 4f1c2d3e5a6b7c8d). That commit was refused before its checks, so this attempt starts where its attempt 3 started: carry its work over and mend what the refusal names (The attempt before).

### Changes asked for

- `a1b2c3d4e5f60718` (major), `parse.go:14–16`, the candidate's side: An empty input panics here.

  ```
  func Parse(s string) *Tree {
  	return parse(s, 0)
  }
  ```

  Asked: Return an error for an empty input.

Say which you addressed with `addressed` in your result (each change's id, and a note); a person checks each one on your result.

## The attempt before

Attempt 3 failed at setup deps: deps exited 1. Its result, the commit cccccccccccccccccccccccccccccccccccccccc on the branch crew/r-0123456789ab/build.3, is not where you start: take from it what holds up, and fix what was refused

## How to finish

Commit your work on this branch. Then submit once: the `task_result` tool with a summary, or `conductor task result --summary "..."`. Conductor takes the commit at HEAD; a worktree with changes not committed (apart from .conductor/) is refused, and so is a result that does not descend from every input.

A done or a handoff does not finish a task. A question goes through Conductor's ask, and a person answers it.
