Fixes #N <!-- or: Part of #N, when the issue stays open -->

## What changed and why

## Verification

<!-- The commands run and their results, per affected surface. A check that did not run is listed as not run, never as passed. -->

## Checklist

- [ ] The Codex adversarial review ran, and Verification names the head it read, its findings and their answers (AGENTS.md, Working an issue; a maintainer runs it for a contributor's pull request)
- [ ] No paid code, licence check or plan check: the only gate this repository knows is whether a provider is registered (CONTRIBUTING.md, open-core rule 1)
- [ ] The open-source build stays fully useful on its own: nothing free is removed or degraded (rule 2)
- [ ] Nothing paid hides behind a flag: a paid feature needs its hosted service, through a registered provider (rule 3)
- [ ] Stop, inspect, revoke, export and required approvals are untouched, or still free and whole (rule 4)
- [ ] The checks of every affected surface ran and are listed under Verification, one not run named as such; a new dependency has its reason and `make notices` ran (docs/checks.md)

Every commit is signed off by its author (`git commit -s`; CONTRIBUTING.md, Sign-off): the `dco` check proves it.

<!-- Made by (agent or person), with a session link when there is one. -->
