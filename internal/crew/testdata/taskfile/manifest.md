# Task "build" for lead (attempt 2 of 3)

Conductor runs the crew "Build crew" toward this goal:

Ship the parser, tested.

Your role: You lead the build.

## Instructions

Write the parser in parse.go & test it: <input> yields a tree.

## Where you are

Your worktree is `/repo/.conductor/worktrees/r-0123456789ab/build.2`, on the branch `crew/r-0123456789ab/build.2`, at the commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa (the run's base). The project manifest `conductor.manifest.json` as this commit holds it (the blob ffffffffffff) made the tree usable before your session started. Its ports are this tree's alone while the attempt runs: api 41001 (`CONDUCTOR_PORT_API`), web 41000 (`CONDUCTOR_PORT_WEB`); `CONDUCTOR_PORT_<NAME>` names each in the environment. Its services db, cache run beside your session, started and answering before it began, for as long as the attempt runs. The preview, what a person opens to look at the work, is http://127.0.0.1:41000/ (`CONDUCTOR_PREVIEW_URL`). The tree's state directory is `.conductor/state` in your working directory (`CONDUCTOR_STATE_DIR`): what the project's tools keep goes there, this tree's alone and out of your result as the rest of `.conductor/` is.

## The attempt before

Attempt 1 could not be made usable: it failed at setup install: npm ci: exit 1. The project manifest, or the host, failed there before its agent could act: it was not counted, and you start where it started.

## How to finish

Commit your work on this branch. Then submit once: the `task_result` tool with a summary, or `conductor task result --summary "..."`. Conductor takes the commit at HEAD; a worktree with changes not committed (apart from .conductor/) is refused, and so is a result that does not descend from every input.

A done or a handoff does not finish a task. A question goes through Conductor's ask, and a person answers it.
