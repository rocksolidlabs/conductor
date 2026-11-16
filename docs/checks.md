# Checks by affected surface

`make preflight` runs every check CI runs, here, from the committed head, and
writes a receipt with a log per step under the git common dir's `preflight/`;
`python3 scripts/preflight.py --machines mac,windows` runs the Mac's (over
`ssh mac`, the revision pushed to its checkout) and the Windows host's (WSL
interop, a checkout under the Windows profile) rows too, never through GitHub.
It is how to see what CI will see before a pull request; CI stays the gate.

Run the narrow tests while iterating, then the complete checks for every affected surface. A change to the Python scripts runs their tests; it does not require unrelated Go, UI, desktop, network or hardware tests. A change spanning server and UI requires tests at both ends: Go race tests, vitest and Playwright. Runtime changes run the relevant runtime suites below. A skipped or unavailable check is a limitation, never a pass.


```bash
git fetch origin && python3 scripts/dco_check.py --base origin/main --head HEAD  # every commit of the branch signed off by its author (CI's dco job); the fetch so that origin/main is GitHub's
make lint                      # gofmt -l, go vet, the open-core rules (internal/opencore's TestModuleKeepsTheRules)
go test -race -count=1 -timeout 60m ./...   # includes PTY, WebSocket and pion loopback tests; its bound is explained below
npm --prefix web run typecheck
npm --prefix web test          # vitest (link detection)
make web-build                 # nuxt generate + copy into internal/web/dist
make build-go                  # embeds whatever is in internal/web/dist
make check-version             # the binary reports the version and commit make injected (a missed -X site would say dev (unknown))
make test-e2e                  # Playwright (web/e2e): builds both, then a server with stub agents in Chromium 1117
make test-pebble               # certificates end to end against Let's Encrypt's Pebble (go install'ed when missing)
make test-live                 # the real Claude Code and Codex (CONDUCTOR_E2E_LIVE_REPO, keys or logins); weekly in CI
make desktop-test              # the Electron shell: tsc --noEmit and vitest (desktop/)
make lint-static               # staticcheck, pinned
make vuln                      # govulncheck (network); the standard library's findings need the latest Go patch
make test-network              # the built-in agents' sites on the network (nightly)
make test-recipes              # the recipe, Brakeman and usage-reader flags against the CLIs on this machine (nightly; the usage reads need a login, and a Claude Code other than the checked version is read only when CONDUCTOR_USAGE_CHECK names it)
python3 scripts/brand_assets.py --check
python3 scripts/notices.py --check  # THIRD_PARTY_NOTICES current (after make web-build, npm ci in desktop/)
python3 -m unittest discover -s scripts -p '*_test.py'  # the scripts' own tests (dco_check.py, notices.py, preflight.py, release_draft.py)
```

The race tests' `-timeout 60m` catches a hang; it is not a speed target.
go test's default, ten minutes a package, is less than `internal/api` takes
on a machine shared with other runs: 17.7 minutes in a run of the whole
module on the 64-core Linux box at a load average of 77 to 160
(2026-10-30). On CI's runner it takes about thirty minutes (1553 to 1796 s
on main's green runs of 2026-11-08), and a run reached the old 30 m bound
with nothing hung (#281), so the bound is 60 m: above every honest run, a
hang bound only. A run left at the default fails for no defect. `make
test`, `ci.yml` and `scripts/preflight.py` pass the same bound, and
`scripts/preflight_test.py` holds them to it. CI's go job has its own
`timeout-minutes: 90`, the test bound plus its other steps (never a
multiple of it), so a hang still ends the job.

A file CI's jobs fetch from a release mirror is cached by its version (`actions/cache`) and its download retried for minutes: Neovim's tarball through `curl --retry-all-errors`, refetched when it is not a gzip, and Electron's binary through
`desktop/scripts/install-electron.mjs` (the desktop tests' `pretest`, six tries of electron's own installer), so a mirror's bad half hour fails one try, not the run (#281).
The browser's system packages (the `e2e` job, `live.yml`) come from the runner's apt mirror list: when the first mirror's name does not resolve, a step before the install takes out each mirror whose name does not, and uses kernel.org's Ubuntu mirror when none is left (apt still checks its indexes against the image's Ubuntu archive key); the install gets three tries a minute apart, each ended after ten minutes by a `timeout` run as root, so that it reaches apt (#281).

CI's `e2e` job runs the Playwright suite in two shards side by side, `e2e (1)` and
`e2e (2)`, so that a pull request waits for it about as long as for the go job.
Playwright gives a shard whole spec files (`fullyParallel` is off) and balances the two
by their counts of tests; each shard is a run of its own, whose global setup starts its
own shared server, and a failing shard uploads its report as `playwright-report-1` or
`playwright-report-2`. `npx playwright test --shard=1/2` in `web/` (after `make
web-build build-go`) runs one shard here; `make test-e2e` and `make preflight` still run
the whole suite on one machine. The job keeps its id, so `required-checks` fails when
either shard does; `scripts/preflight_test.py` holds the matrix to the shard count. Its
`timeout-minutes: 160` is a hang bound: the set-up steps' own caps (90 minutes) and the
longest whole-suite run (67, on 2026-11-15) together. On the pull request that cut the
shards (#955), shard 1 ran 284 tests in 32 minutes and shard 2 ran 280 in 27 (34 and 29
for each job), where the whole suite of 564 had taken one job 67.

A spec asserts layout by measures, never by pixels: `web/e2e/layout.ts`'s
helpers say the measure in the failure. What a person can see of an
element is the browser's answer, read by one function the helpers share:
drawn by `checkVisibility`, the visible box by an `IntersectionObserver`,
text by a `Range`, one box per line, what covers it by hit-testing.
`uncut`, `inside`, `noOverlap`, `aligned`, `rowsInView`, `topAtMost`,
`lines`, `noSideways` and `tapTarget` wait until one match is drawn
(failing with how many matched), measure after the fonts load and fail
when none is drawn at the measure; `atWidths` waits for the layout's
root at each size and names the size in a failure; `tabTo` waits for its
target and its start and fails when either matched nothing; `ageBy` and
`probeColour` wait for nothing, and `probeColour` fails on a value that
is no colour. Where the browser cannot say, a helper fails, saying what
to give it, or its doc comment states the limit. `layout.spec.ts` holds
each check of each helper with a page that fails without it, and copies
the app's own markup shapes with the defects seen on the built app.

`make lint` ends with the open-core rules (`internal/opencore`,
`TestModuleKeepsTheRules`; CONTRIBUTING.md, The open-core rules): no import
of a module of the `rocksolidlabs` organization other than this one
(`private-module`), none required or replaced in `go.mod` or a root
`go.work`, and no other module's tree brought in by a local replace or a
workspace `use` (`go-mod`), no build constraint naming anything but a
platform, a Go version or a test tier (`feature-tag`), nothing but the
standard library and `ext`'s own packages imported under `ext/` (`ext-std`),
no import of this module's package from a directory the lint does not
read, such as `testdata`, a `_` directory or a symlinked one
(`unread-package`; a symlinked `.go` file is read where its link stands),
and no entitlement call site outside `ext/` (`entitlements`: a file the
binary compiles, any but a `_test.go` one, naming `ext.EntitlementSource`,
`ext.Entitlement`, `ext.Entitlements`, `ext.RegisterEntitlements` or
`ext/local`'s `AllowAll` under any import name, dot-importing either
package, or calling a method named `Entitled`; this rule parses each file it
reads whole, the others read imports and build constraints only). A
problem names its rule, file and line; the race suite runs the same test, so CI's go job fails on it
twice over.

The e2e stub (`web/e2e/stub-agent.sh`) answers as Claude Code, Codex or an
impostor by `STUB_IDENTITY`, reports as its agent's hooks do by
`STUB_REPORT`, keeps a transcript per agent session, draws the trust
question with `STUB_TRUST_DIALOG=1` and Codex's "Hooks need review" when
`$HOME/.codex/stub-hooks-review` exists, and on the line `stub tools codex`
(or `copilot`, `agy`) replays that agent's captured hook payloads through
`conductor notify`; `web/e2e/conductor.e2e.json` wires it
with the real recipes. Run the narrowest package tests while iterating, then
the full set for affected surfaces before finishing. Every harness that launches sessions sets
`CONDUCTOR_RENDEZVOUS=0` (the cli tests' `clearConductorEnv`, `web/e2e/server.ts`):
a server publishes its sessions to the public switchyard by default, and a
test's must stay on the machine. A check that cannot run (no Docker, no network, no browser) is a
reported limitation, not a pass.
