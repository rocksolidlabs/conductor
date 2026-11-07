# Task "build" for lead (attempt 3 of 3)

Conductor runs the crew "Build crew" toward this goal:

Ship the parser, tested.

Your role: You lead the build.

## Instructions

Write the parser in parse.go & test it: <input> yields a tree.

## Where you are

Your worktree is `/repo/.conductor/worktrees/r-0123456789ab/build.3`, on the branch `crew/r-0123456789ab/build.3`, at the commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb (the commit Nate asked changes of, in review 4f1c2d3e5a6b7c8d).

## Findings to address

Nate asked for changes to the commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb (review 4f1c2d3e5a6b7c8d, against the base 9999999999999999999999999999999999999999), where this attempt starts. Their summary: Two gaps in the parser, and a failing test.

### Changes asked for

- `a1b2c3d4e5f60718` (major), `parse.go:14–16`, the candidate's side: An empty input panics here.

  ```
  func Parse(s string) *Tree {
  	return parse(s, 0)
  }
  ```

  Asked: Return an error for an empty input.

- `b2c3d4e5f6071829` (nit), `parse.go:3`, the base's side (lines the candidate removed): Keep this comment.

  ```
  // Parse parses `s`.
  ```

- `c3d4e5f60718293a` (blocker), the check `test`: The test fails on an empty input.

  Its receipt in the ledger is r-0123456789ab/build/2/check/test/1, on the commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb (exit 1); Conductor classed it code, as the person did. Its whole log is `.conductor/previous/check-test.log`.

  They quote:

  ```
  panic: index out of range
  ```

  Its last output:

  ```
  --- FAIL: TestEmpty
  panic: index out of range
  ```

### Questions to answer in your summary

- `d4e5f60718293a4b`, `grammar.md`, the whole file, the candidate's side: Is the grammar final?

### Notes, for context

- `e5f60718293a4b5c`, the candidate as a whole: The lexer lands next week.

Say which you addressed with `addressed` in your result (each change's id, and a note); a person checks each one on your result.

## The attempt before

Attempt 2 failed at check test: test exited 1

The check test failed on the commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb, where you start: it exited 1. Its receipt in the ledger is r-0123456789ab/build/2/check/test/1.

Its last output:

```
--- FAIL: TestEmpty
```

Its whole log, 120 bytes as the ledger keeps it (SHA-256 ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff), is `.conductor/previous/check-test.log`.

## How to finish

Commit your work on this branch. Then submit once: the `task_result` tool with a summary, or `conductor task result --summary "..."`. Conductor takes the commit at HEAD; a worktree with changes not committed (apart from .conductor/) is refused, and so is a result that does not descend from every input.

A done or a handoff does not finish a task. A question goes through Conductor's ask, and a person answers it.
