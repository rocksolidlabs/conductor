# Checks by affected surface

`make preflight` runs every check CI runs, here, from the committed head, and
writes a receipt with a log per step under the git common dir's `preflight/`;
`python3 scripts/preflight.py --machines mac,windows` runs the Mac's (over
`ssh mac`, the revision pushed to its checkout) and the Windows host's (WSL
interop, a checkout under the Windows profile) rows too, never through GitHub.
It is how to see what CI will see before a pull request; CI stays the gate.
Two jobs are CI's alone (`CI_ONLY_JOBS` in `scripts/preflight.py`): `mobile-ios`,
the phone shell's iOS app built and started on a simulator, which needs Xcode,
and `mobile-android`, its Android app built and started on an emulator, which
needs a JDK 21, the Android SDK and a machine that runs an emulator; no
receipt counts either as run.

Run the narrow tests while iterating, then the complete checks for every affected surface. A change to the Python scripts runs their tests; it does not require unrelated Go, UI, desktop, network or hardware tests. A change spanning server and UI requires tests at both ends: Go race tests, vitest and Playwright. Runtime changes run the relevant runtime suites below. A skipped or unavailable check is a limitation, never a pass.


```bash
git fetch origin && python3 scripts/dco_check.py --base origin/main --head HEAD  # every commit of the branch signed off by its author (CI's dco job); the fetch so that origin/main is GitHub's
make lint                      # gofmt -l, go vet, the open-core rules (internal/opencore's TestModuleKeepsTheRules)
go test -race -count=1 -timeout 90m ./...   # includes PTY, WebSocket and pion loopback tests; its bound is explained below
npm --prefix web run typecheck
npm --prefix web test          # vitest (link detection)
make web-build                 # nuxt generate + copy into internal/web/dist
make build-go                  # embeds whatever is in internal/web/dist
make check-version             # the binary reports the version and commit make injected (a missed -X site would say dev (unknown))
make test-e2e                  # Playwright (web/e2e): builds both, then a server with stub agents in Chromium 1117
make test-pebble               # certificates end to end against Let's Encrypt's Pebble (go install'ed when missing)
make test-live                 # the real Claude Code and Codex (CONDUCTOR_E2E_LIVE_REPO, keys or logins); weekly in CI
make desktop-test              # the Electron shell: tsc --noEmit and vitest (desktop/)
make mobile-test               # the phone shell's bridge: tsc --noEmit, its vitest (mobile/test, run by the app's vitest too) and its boot spec (after make web-build)
make mobile-native-test        # the phone shell's native key, store and plugin calls: swift test in mobile/ios/ConductorNative (a Mac; Swift Testing, no Xcode project needed; CI's mobile-ios job)
make mobile-install mobile-sync mobile-ios  # the iOS app built for the simulator, unsigned, and its boot check (ShellBootsUnderItsPolicyTests) run on one (a Mac with Xcode, after make web-build; CI's mobile-ios job)
make mobile-ios-policy         # a Release build with the sample signing policy (mobile/ios/fixtures) copied into a packaging's place holds the class ConductorKeySigningPolicy, read from the executable; the default build holds none (after make mobile-ios; CI's mobile-ios job)
make mobile-install mobile-sync mobile-android  # the Android app held to what it must not ship (scripts/android_project.py --runtime), then its debug build, its JVM tests (the key, the store, the native HTTP call and the gates' decisions) and its boot check's build (a JDK 21 and the Android SDK, after make web-build; CI's mobile-android job)
make mobile-android-image mobile-android-boot  # the emulator's image (three tries), then the boot check (ShellBootsUnderItsPolicyTest) on a running emulator, its log printed after (CI's mobile-android job starts the emulator for it)
make lint-static               # staticcheck, pinned
make vuln                      # govulncheck (network); the standard library's findings need the latest Go patch
make test-network              # the built-in agents' sites on the network (nightly)
make test-recipes              # the recipe, Brakeman and usage-reader flags against the CLIs on this machine (nightly; the usage reads need a login, and a Claude Code other than the checked version is read only when CONDUCTOR_USAGE_CHECK names it)
python3 scripts/brand_assets.py --check
python3 scripts/notices.py --check  # THIRD_PARTY_NOTICES current (after make web-build, npm ci in desktop/ and mobile/)
python3 -m unittest discover -s scripts -p '*_test.py'  # the scripts' own tests (dco_check.py, notices.py, preflight.py, release_draft.py, design_drift.py, live_setup.py, go_test_groups.py, ios_project.py and android_project.py: the phone shell's iOS and Android projects held to what they must not ship; ios_boot.py and android_boot.py: their fetches tried again)
```

`npm --prefix web test` runs the design system's rule checks among the
app's tests: `web/app/utils/designRules.test.ts` reads the app's sources
and `docs/design/system.md` and fails when a rule is broken (an icon-only
button with no tooltip, a cut whose box does not set its width, a state
mapped to a status colour outside `utils/status.ts`, a zinc grey) or when
the file and the code disagree (a rule with no mark, the Status table
against `STATUS_LOOK`, the floors against `CONTRAST_FLOORS`). Each rule of
`system.md` names its checks.

`make design-capture` is not a check: it is a maintainer's step at a
release, on the draft release's checklist. It builds the app and the
server, runs `web/e2e/look.spec.ts` with `CONDUCTOR_E2E_LOOK=1` (skipped
otherwise, as `live.spec.ts` is; `typecheck:e2e` still checks it), which
writes every screen of the seeded day in both themes and sizes to
`web/test-results/look/` with the computed values in `look.json`, then
prints what drifted against `docs/design/system.json`
(`scripts/design_drift.py`; `--check` exits 1 on drift, `--write` writes
the new baseline). The screens are never committed; the baseline is,
values only (`docs/design/system.md`, What a pull request cannot do).

The race tests' `-timeout 90m` catches a hang; it is not a speed target.
go test's default, ten minutes a package, is less than `internal/api` takes
on a machine shared with other runs: 17.7 minutes in a run of the whole
module on the 64-core Linux box at a load average of 77 to 160
(2026-10-30). On CI's runner it takes about thirty minutes (1553 to 1796 s
on main's green runs of 2026-11-08, 3,129 s on 2026-11-18), and a run
reached the old 30 m bound with nothing hung (#281), so the bound became
60 m. On 2026-11-19 the whole run on the Mac took 3,260 s, `internal/api`
3,238 s of it, one binary for the whole package there (CI deals it into
five groups), 54 of the 60 minutes before the walk's tests were added, so
the bound is 90 m: above every honest run, a hang bound only. A run left
at the default fails for no defect. `make test`, `scripts/preflight.py` and every group of CI's
`go-race` job pass the same bound, and `scripts/preflight_test.py` holds
them to it. Each `go-race` job has its own `timeout-minutes: 120`, the test
bound plus its other steps (never a multiple of it), so a hang still ends
the job.

CI runs the race tests as several jobs side by side, since on its four-core
runner the one command took 57 minutes (main, 2026-11-18). The `go-race`
job is a matrix of eight groups, `go-race (api-1)` to `go-race (api-5)`,
`go-race (crew)`, `go-race (dispatch)` and `go-race (rest)`:
`internal/api`'s tests dealt by name into five groups (a test's group is
the SHA-256 of its name modulo five, so a test added or removed moves no
other), `internal/crew` with its ledger, `internal/dispatch`, and every
other package of `go list ./...` together. Each runs
`go test -race -count=1 -timeout 90m`, the whole module's flags; an `api`
group adds `-run '^(TestA|TestB|...)$'`, which selects top-level tests,
each with its subtests, and runs the package's `TestMain` as any `-run`
does. `scripts/go_test_groups.py` makes each group's command and prints its
tests before it runs them. The `go` job (lint, staticcheck, the build, the
notices) runs `python3 scripts/go_test_groups.py check`, which fails the
run when the matrix does not list every group once, when a package of `go
list ./...` is in no group or in two, or when a test of `internal/api` is
selected by no group's `-run` or by two, as `go test -list` with each
group's expression answers: Go's own matching. Benchmarks run in no group,
as before (none is run without `-bench`); the repository has no examples,
and its fuzz targets (`internal/quoted`, `internal/session`) run their seed
corpus in `rest`. To run one group here as CI does:
`python3 scripts/go_test_groups.py run api-3` (`command api-3` prints the
command, `groups` lists them). A developer's own command stays the whole
module, `go test -race -count=1 -timeout 90m ./...` or `make test`.

A file CI's jobs fetch from a release mirror is cached by its version (`actions/cache`) and its download retried for minutes: Neovim's tarball through `curl --retry-all-errors`, refetched when it is not a gzip, and Electron's binary through
`desktop/scripts/install-electron.mjs` (the desktop tests' `pretest`, six tries of electron's own installer), so a mirror's bad half hour fails one try, not the run (#281). The `mobile-ios` job's Swift packages (the native runtime's package and its two binary frameworks, from GitHub) are cached on the manifest's hash and resolved in three tries a minute apart, each under five minutes (`make mobile-ios-resolve`, `scripts/ios_boot.py`), before the build. The `mobile-android` job's Gradle distribution and packages are cached by `gradle/actions/setup-gradle`, and its build and the emulator's system image are tried three times a minute apart when a try fails for a fetch (a download refused, cut or timed out, a name not resolved), each under its own bound, while a try that fails for its code fails at once (`scripts/android_boot.py`).
The browser's system packages (the `e2e` job, `live.yml`) come from the runner's apt mirror list: when the first mirror's name does not resolve, a step before the install takes out each mirror whose name does not, and uses kernel.org's Ubuntu mirror when none is left (apt still checks its indexes against the image's Ubuntu archive key); the install gets three tries a minute apart, each ended after ten minutes by a `timeout` run as root, so that it reaches apt (#281).

CI's `e2e` job runs the Playwright suite in six shards side by side, `e2e (1)` to
`e2e (6)`, so that a pull request waits for it about as long as for the race tests.
Playwright gives a shard whole spec files (`fullyParallel` is off), in order, cut by
their counts of tests, not their times; each shard is a run of its own, whose global
setup starts its own shared server, and a failing shard uploads its report as
`playwright-report-<k>`. `npx playwright test --shard=3/6` in `web/` (after `make
web-build build-go`) runs one shard here; `make test-e2e` and `make preflight` still run
the whole suite on one machine. The job keeps its id, so `required-checks` fails when
any shard does; `scripts/preflight_test.py` holds the matrix to the shard count. Its
`timeout-minutes: 160` is a hang bound: the set-up steps' own caps (90 minutes) and the
longest shard measured (66, a half of the suite, on 2026-11-18) together. On the pull
request that cut two shards (#955), shard 1 ran 284 tests in 32 minutes and shard 2 ran
280 in 27, where the whole suite of 564 had taken one job 67; by 2026-11-18 the suite was
974 tests, and its two shards took 65 and 46 minutes. Cut six ways, that run's per-test
times give shards of 23, 20, 20, 13, 18 and 14 minutes.

`required-checks`, the one check branch protection requires, needs every
other job and fails when any of them failed, was cancelled or was skipped.
A matrix job's result is one result for all its cells: when one shard or
group fails, the job's result is `failure` (pull requests' runs of 2026-11-15 and
2026-11-17, where one `e2e` shard failed and the other passed, failed
`required-checks`).
`scripts/preflight_test.py` holds its needs to the workflow's jobs.

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

Contrast is held twice, by one table of floors (`web/app/utils/contrast.ts`,
`CONTRAST_FLOORS`). `themeContrast.test.ts` (vitest) resolves every token in
both themes from Nuxt UI's own plugin and stylesheet and `main.css`, and holds
each text token on each surface. `web/e2e/contrast.ts` walks the text a page
draws: each text over its real ground, tints, opacity and what is painted
under it counted. `contrast.spec.ts` runs that walk on six pages of the
seeded day, in both themes, at 1440 and at 390, and fails on a text under its
floor. It also fails on an entry of `contrast-known.ts` that its walk no longer
meets: the row that fixes a place deletes its entry.

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
problem names its rule, file and line; the race suite runs the same test, so CI fails on it
twice over (the go job's `make lint`, and go-race's `rest` group).

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

The seeded day (`web/e2e/day.ts`, the plan `docs/plans/ux-review.md`) is a
day of work on a server of its own, started at a worker's first test that
asks for the `day` fixture and built in named parts on demand: `states` (a
session in each state: asking, working, finished, running with no hooks,
exited), `dispatch` (projects CND and OPS, fourteen tickets), `waiting`
(an item of each kind the inbox holds), `runs` (Ledger migration's six
tasks, Release notes succeeded), `long` (a name, a label and a title at
their bounds) and `busy` (all of them, and eighteen live sessions). The stub
holds its states with no timer (`STUB_TURN`, a question, a check that exits
1). Two rules: the day is read-only, and a measure of how much fits is made
on `busy`, so that it measures the same page whatever ran before it. The
`day` fixture holds the first at each test's end (`guard` in `day.ts`) by
one rule: what the day's server lists is the same as the day's start and
its parts' builds left it. One snapshot holds every session (name, status,
exit code, agent, folder, run and member, its attention whole), every run
(name, label, state, outcome, each member's status and session, each
task's state, attempts and the decisions that hold it), the inbox's items,
Dispatch (people, projects, labels, milestones, saved views, every
project's tickets with their status, labels, owner, priority, assignees and
revision, each plan's state), the crews and the share links. The guard
names each item added or gone and each field changed, from what to what,
and ends the sessions and runs a test added. Left out, as what moves by
itself: every time and a run's log; a session's viewers and size, which
every page attached moves; a member's diff, which the server counts again
in the background (it moved within a minute on an idle day); and what
follows a field compared (a run's needsInput and ledger revision, a
member's attempt, branch and worktree). Not guarded: a browser context opened by hand (not `dayPage`) and left
attached, which views until the worker ends (viewers come and go with
every page, so the guard counts none; `dayPage`'s are closed before it);
what is typed into a day session's terminal and its screen, which the API
does not list (a line into a finished session starts a turn, which is
named); a ticket's comments, attachments and links, which the ticket list
does not carry (one that moves the ticket's revision is named); the
activity log and history, which every test that looks at the day appends
to; and the server's settings, catalog, webhooks and alert rules and the
day's repository's files and branches, which no spec on the day may
write (a spec that must starts a server of its own, `ownServer`).
`dayPage` opens the day only in a test that asked for `day`. A shard that asks for the day builds its own (a shard is a run
with a worker of its own): on CI's runner `busy` takes about 17 s
(2026-11-16), `waiting` alone about 8 s (2026-11-15, when a spec last
asked for it alone). On a 64-core development machine (2026-11-16):
`day.spec.ts`'s proofs of the guard start ten days of their own, about
33 s in all (two of them build `runs` or `waiting`, about 10 s each),
and one guard, one snapshot of the busy day, is 53 reads in 0.1 to
0.15 s at a load average of 4 to 20. To look at it by hand,
`CONDUCTOR_E2E_DAY_OPEN=1 npx playwright test day.spec.ts -g open` (from
`web/`, after `make web-build build-go`) prints its address and the path of
a file only its owner reads that holds its token, and holds it until Ctrl+C.
`dayPage` and `useTheme` (`web/e2e/fixtures.ts`) set the colour mode the way
the app keeps it (`conductor-color-mode`): a `colorScheme` context does not
switch this app, which opens dark whatever the system prefers.
