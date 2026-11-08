# Conductor repository contract

Conductor runs coding agents in PTYs and shares browser terminals. One Go module, one Nuxt app, one binary. Read `docs/repository-map.md` for architecture and extension points; `docs/dependencies.md` before changing dependencies. These are on-demand references, not startup imports.

- Test every affected surface. Server/CLI changes get Go race tests; UI behavior gets vitest and Playwright. Changes spanning both require both. Python tooling runs the scripts suite. See `docs/checks.md`; report unavailable checks accurately.
- Protocol changes touch three places together: `internal/proto`,
  `web/app/utils/protocol.ts` and `docs/protocol.md`. Every new frame or message
  gets a size limit and a test.
- Stdlib first: `net/http` mux with method patterns, `log/slog`, `encoding/json`
  with `DisallowUnknownFields`. New Go dependencies need a reason in the PR.
- A dependency added, removed or upgraded (go.mod, `web/` or `desktop/`
  packages) regenerates `THIRD_PARTY_NOTICES` with `make notices` (after
  `npm ci` in `desktop/`), committed with it; CI checks it is current. A
  package that ships without its licence text gets the upstream text in
  `scripts/notices/`.
- Every commit is signed off (`git commit -s`): the line certifies the
  Developer Certificate of Origin in `DCO`, and its name and address are
  the commit author's. CI's `dco` job fails a pull request with a commit
  that lacks it. `git rebase --signoff origin/main` fixes a branch of your
  own commits; `CONTRIBUTING.md` (Sign-off) says how, and how a merge
  commit or another author's commit is repaired.
- Commands are argv arrays. Never build a shell string from user input.
- Compare tokens with `share.Equal`; store only hashes; never log query strings.
- Server session working directories go through `resolveCwd` (a launch
  through `openCwd`, which opens the folder it checks); file reads and saves
  go through `session.ResolvePath` and then the session's `session.Folder`
  (an `os.Root`), never a path opened again. Do not bypass either.
- Keep per-connection bounds (read limits, queues, in-flight file requests).
- UI components come from Nuxt UI; use the brand palette in `web/app/app.config.ts`
  and `docs/design/brand.md`. The junction mark is artwork, never a status light.
- Names follow the railroad: Conductor is the application, Yardmaster the
  agent harness (a crew with tasks runs as its managed run), Dispatch the
  ticketing and project tracking component (its items are tickets; what
  the engine does to a run's tasks is "scheduling" where people read it),
  Brakeman the sidekick agent to come, Switchman the task router to come.
  Docs, UI text and issues use these names.
- Do not commit `internal/web/dist` contents (only `.gitkeep`), `web/.nuxt` or
  `web/.output`. Commit `web/package-lock.json`.
- **Windows and WSL: the person configures no networking, ever.** Never ask
  for `netsh interface portproxy`, `networkingMode=mirrored` in
  `.wslconfig`, a `.wslconfig` edit, or any other manual step to expose the
  server inside WSL. Whatever must cross Hyper-V's NAT, the desktop app
  forwards itself (today UDP for ICE, `desktop/src/udp-forwarder.ts`;
  forwarding the TLS listener and mapping the router from Windows are the
  app's job too, not yet done). Docs, setup screens, Settings text, test
  instructions and by-hand checklists follow this; a check that would need
  such a step on WSL is done from a machine that is not WSL, or waits for the
  app. The maintainers have said this twice; do not raise it again.

## Working an issue

GitHub Issues are the backlog; the Project board is a view of them. One
issue, one branch, one pull request, and CI is the only gate. The
maintainers are the owners of the `rocksolidlabs` GitHub organization
(`CONTRIBUTING.md`, Who decides); they merge and decide. The assignment,
branch-on-origin and Project board bullets below are the maintainers' and
the agents they run, who have write access; a contributor without it
follows `CONTRIBUTING.md`'s fork path, and a maintainer assigns the issue
and moves the board for them.

- Take the issue you were given, or else the top unassigned issue labelled
  `ready`. Assign yourself. Never hold two open issues at once.
- Work on the branch `issue-N`, cut from `main` and linked to the issue
  (`gh issue develop N --name issue-N`), in a worktree of its own. The
  branch on origin is the claim: one that already exists is someone's work,
  so ask before touching it.
- Open a draft PR at the first commit, and tick the template's checklist.
  `Fixes #N` finishes the issue; `Part of #N` leaves it open. Say in the PR
  body which agent or person made it, with a session link when there is one.
- Keep the Project board current: an issue goes on it when it is filed
  (Backlog, or Ready), moves to In progress when its branch is cut and to
  In review when its PR is pushed; Done follows the merge by itself. New
  issues do not land on the board on their own.
- Before every push run the checks for each affected surface
  (`docs/checks.md`). Report a check that did not run as not run, never as
  passed. Mark the PR ready when it is complete and green.
- Answer every review comment: fix it, or reply why not, then resolve the
  thread. A maintainer merges. Merged is done, and GitHub closes the issue.
- Blocked or handing off: push, leave one PR comment saying what is left,
  and unassign yourself.
- Work a maintainer asks for directly gets an issue first (search for an
  existing one).
- **A bug in the stream you are working is fixed in that stream, never
  filed and left.** The stream is the whole piece of work, not the PR in
  front of you: every PR of a multi-PR stream (the harness's twelve rows),
  and a by-hand or live check of it. A bug the stream's own code caused,
  found by any of its PRs, its reviews or its checks, is fixed before the
  stream is called done: in the PR that found it, or in a fix PR of the
  stream right after it. A check that finds bugs is not finished when it
  files them. (2026-10-24: the harness's live check found five defects in
  the harness and filed them as #351 to #355 instead of fixing them.)
- A product defect outside the stream, one the stream's code did not
  cause and does not need, gets its own issue, and a maintainer decides when.
- A test that fails in the PR's own checks is fixed in that PR and named in
  its body under its own heading; no issue. A flake seen once that blocks
  nothing goes as one line into the single standing flaky-tests issue
  (#281), fixed when someone is in that file anyway; no issue per flake.
- Vulnerabilities go in a GitHub Draft Security Advisory, never in an
  issue, PR, commit message or doc, until a maintainer decides on disclosure
  (`SECURITY.md`).

## Tests that do not flake

Every flake on the 2026-10-24 list had one of these causes. A test that
fails now and then is work, not a note: fix it, and if the cause is new,
add its rule here.

- Wait on the mechanism, never on the clock. A sleep, a wall-time draw
  ("300 ms before the look's budget ends") or a fixed bound that is nobody's
  cap is what a loaded machine misses. Poll for the state the test needs,
  drive the step through a seam the code offers (a fake clock, a hook, a
  gate), and when a bound is unavoidable make it the sum of the caps the
  code itself applies, with the test's timeout above it.
- Two timers set for one deadline fire in no set order. Seeing one fire
  (a viewer's lapse closing it) says nothing of the other (its link's
  lapse telling the host): set the deadline far off and call what the
  timers call, in the order the test needs.
- The product's bound is not the test's. A 3 s probe limit or a 10 s kill
  wait is right for the product and wrong for a test that only needs the
  thing to happen: give the test its own wait where the product's is not
  what is tested, or make the product's bound generous where it only bounds
  a hang.
- Order what must be ordered. Keys that must stay unconfirmed go in after
  the process is stopped (SIGSTOP), not raced against a kill. A check made
  at once after an action ("nothing served yet") holds the step the action
  woke at a gate in the fake until it has looked (the fake issuer's `hold`
  in `internal/certs`): a loaded machine lets that step finish first. A
  status that may arrive before or after an answer must give the same
  picture either way, in the product, not in the stub's ordering.
- A pid of 0 is no process; a file not yet written is not evidence. Wait for
  the pid file, the swap file, the record before judging it.
- A page not yet drawn passes every "hidden", and a key it does not yet
  listen for is lost. After a `goto`, wait for something the listener's
  own component draws (the layout's sidebar root for the layout's keys)
  before the first key, press it once, and give its effect the expect's
  bound; a second press on a clock only hides the first one's loss, and a
  toggle pressed again closes what was only slow to open.
- A test's timeout bounds a hang, not its steps. A long walk whose every
  step has its own bound is spent by a loaded box with every step passing,
  whatever fixed timeout it has: each step raises the test's timeout by
  its own waits' caps as it takes them (`testInfo.setTimeout`, the
  `budget` of `palette-rows.spec.ts`), with the config's timeout above
  their sum, never a multiple of the config's.
- Count only what the test made. A spec that counts sessions, tiles or list
  rows sees what an earlier spec left behind and what the server's sweeps
  removed: end the strays first (`stopStrays` in `web/e2e/fixtures.ts`) and
  look for the thing by its id or name, never by a list's length.
- What a child keeps under HOME is shared by every process of the same
  HOME. A test that gives each child its own `XDG_STATE_HOME` hides what
  the product does where it gives none: the e2e's one server HOME then
  holds one file for every spec's children (2026-10-31: Neovim's ShaDa,
  written by several as they left, unreadable, E576 on every later
  `:edit`). The product gives such a child state of its own or none
  (`-i NONE`), and a test proves it with the shared HOME, not around it.
- A listing is a moment, not a promise. A folder other processes write
  holds files they replace by renaming a `.new` over them: listed, such a
  file can be gone by its read, and the ENOENT fails a test that had no
  business opening it. Read the files the test made, by their id, or by
  their kind (a stub's `<id>.txt`, appended in place) when the id is not
  known yet.
- A frame that must be dropped, a dialog that must be drawn inside a
  budget: read the moment off a seam the code exposes (`viewerSeam`,
  `beforeEnter`) rather than guessing it from a stall or a sleep. A stall
  long enough to be sure trips the product's own timeouts (the host writer's
  15 s reset) and fails for a new reason.
- One Playwright run per worktree at a time: concurrent runs clobber
  `web/test-results` and fail each other with false timeouts. Reproduce a
  timing flake by loading the box with something else (a Go race package
  beside the spec), not with a second Playwright run.
- A transaction bound to a context that a stop cancels is refused by the
  stop, not by the store; an engine that reads every refusal as a store
  failure pauses runs that were only stopped. Check the context before
  judging the error.
- Wait for a state that holds, never one passed through. A poll for
  "attempts == 2" misses an attempt that fails in a millisecond; wait for
  the thing itself (attempt 2, running) and assert how it got there. A
  state that only flashes by in a test is often a product bug: here an
  attempt that failed at once because it was handed a commit known to be
  broken.
- A starting value is not a result. A threshold the starting state
  already meets (cols above 60 on a session born 80 x 24) passes before
  anything happened, and a settle that compares a read with one made a
  moment before settles at once. Wait for the step that makes the value
  (the window attached: the session counts it as a viewer) and compare
  with what the actor itself computed (the page's drawn grid), on two
  reads an interval apart.
- A run's log is not its ledger, and neither is the rest of the engine's
  view. The engine makes a change (its log line, a task's state, an
  attempt's) inside the change and commits its rows and events after it,
  outside the lock: a test that saw the change in the view (a log line,
  `waitTask`, `attemptFailed`) and reads the ledger at once can read it
  before the commit. Wait on the ledger for what the ledger must hold
  (`attemptWhen`, `taskWhen`), every row and event the test then reads.
  The session works the same way: it changes the attention
  inside its lock and records the change's entry after it (`recordOwn`),
  so a state seen showing says nothing of its entry. Wait for the entry
  before reading or counting the activity log (`logged`, `lastEntryIs`).
  A message sent from inside a step is not the step done: the session
  sends a viewer its welcome inside `AttachWith`, before the host's peer
  holds the subscription (`awaitSub` in `internal/hostagent` waits for it).
- End what a test started before its directories go. An engine a test
  abandons (the server that ended, before a continuation) keeps reacting:
  when the fake launcher ended its processes at cleanup, it failed their
  attempts and made the next attempt's worktree in a repository
  `t.TempDir` was removing (`.git: directory not empty`, #391). The fake
  launcher ends its engine's runs, recording nothing, before the first of
  its processes (`endRuns` in `internal/crew`); anything else a test leaves
  running is ended the same way, before the cleanup that removes what it
  writes to.
- Servers over one data directory hand its owner lock on: a later one
  waits for it and takes it when the first ends. End the later ones first
  (cleanups run last in, first out, so `serve` registers each one's end
  after the first's), or the later one opens the ledger as its owner while
  `t.TempDir` removes it (`data/runs: directory not empty`, #281). And a
  Close returns only once nothing it started can still write: the ledger's
  wait could try for the lock once more after Close and make `owner.lock`
  again; its tries are made under the ledger's lock now.
- What a test finds left behind has an owner: find it before judging.
  A swap file still there after `:q` named another Vim, still running,
  in the banner of the next open; it was a Neovim the page had asked for
  a tab left before the Neovim opened, which nothing closed (2026-10-31,
  #429's review). The pid in the file, the frames the page sent and the
  processes still alive say whose it is; a longer wait would only hide it.
- Check what must last again after a redraw the test causes. A check made
  only as a view first appears races the page's own redraws (a clock
  tick, a live update): the run page's graph dropped a task's needs edge
  at every redraw after its first (Vue Flow ran the editor's connection
  check on every edge it was handed, #281), and the spec that read the
  edge on arrival failed only when the page's 5 s tick came first. Switch
  a mode or toggle a setting, then check again, so a bug that only a
  redraw shows fails every run.
- Judge by the state at the moment of judging, not by what a wait last
  saw. A wait that ended on its timer, or a slow step after it, leaves a
  variable saying "still open" after the thing closed: a stopped service's
  `/proc` listing outlasted its member's shutdown on a loaded machine, and
  the output, closed during the listing, was reported held (`Killed`,
  #281). Read the channel or the file again where the answer is made, and
  let the test hold the event back past the judgement through a seam
  (`outputRead`, `settled` in `internal/proc`), so the old code fails
  every run. An answer drawn from more than one fact (a kill sent, the
  group found empty, the output open) reads them all at once, under the
  lock the events that change them take, and an event beside the wait is
  noted, under that lock, with what it did when it happened (the kill with
  whether it took a holder, `lifecycle.judge`). Read one by one, an event
  between two reads gives a picture neither order would. A goroutine's
  view of a thing is not the thing: a copier not yet at its end of file
  says nothing of who writes the pipe, so ask the kernel (the pipe's
  hang-up, `writersGone`). A fact read before a slow step (a `/proc`
  listing) is read again after it. A flake a whole
  loaded box will not show comes back when the test and a few busy loops
  share a few cores (`taskset -c 0-3`).
- Mark what you read with what you saw before reading it. A poll that
  records a file's time (a version, a cursor) after its read can record a
  change the read never saw, and no later poll sees a change: the
  certificate files' check read a certificate whose key was not yet
  written, failed, then stat'd the pair and recorded the key's time
  (#281). Take the mark first and record it only for a read that loaded;
  a test changes the thing between the read and the record through a
  seam (`afterRead` in `internal/certs`), and pokes or calls each check
  itself instead of racing a ticker.
- A test's waits fit in its timeout, and a slow step is waited on by
  what ends it. A paste the server hands Neovim frame by frame took the
  read loop 90 s at a load average of 110 though nothing used a tenth of
  that in CPU, and the test, its own waits adding up past the suite's
  120 s, ran out at whichever step was slow that day (`vim.spec.ts`,
  #281). Wait for the server to have answered the frames (a ping behind
  them, `serverCaughtUp`), never for a word to stay the same over a few
  seconds; where nothing in the product caps the step, its bound only
  bounds a hang, and `test.setTimeout` is above that bound and the rest.
- A frame a client has written is not a frame the server has read. A
  send returns once the bytes are in the client's socket; the server's
  read loop may take them much later, after what another connection sent
  meanwhile. A test that needs one connection's frames queued ahead of
  another's waits for the server's read after them (`viewerSeam.read`
  with `wsClient.sent`, `viewerReads.await`), and holds the writer that
  would drain them (`writerGate`) rather than trusting a full socket.
- A frame the session has sent is not a frame the viewer has. The session
  queues it, and the subscription's own goroutine hands it to the sink
  later: a refusal sent before `NvimInput` returned was not yet in a test's
  sink when it looked at once, and the test, finding it only beside the
  next keys' refusal, judged the wrong one (#281). Wait until the viewer has
  been handed all that was sent (`drained`) before judging what it holds,
  the absence of a frame too. And a test's sink is a viewer like any other,
  held to the session's queue bound: read the frames it has not read yet,
  outside its lock. A test that read every frame again each round under
  that lock held it for 11 s on a loaded machine, and the session dropped
  the sink as a slow consumer, its editor with it ("file access denied").
- Keep what decides ahead of what is shown, and wait on what decides.
  The output pump sent each chunk to the viewers before it fed the screen
  a key is routed by (`startupMayShow`): a viewer that answered the
  dialog at once (a routed answer, a crew's client) met a screen without
  it, and its Enter went to the text. A test that saw the dialog in the
  watcher's tail, fed just before that screen, and typed found the same
  gap. Feed the state that routes a viewer's answer before the viewer has
  the output, prove it with a stall after the send (`afterBroadcast`),
  and when one state is kept twice, wait on the copy the code decides by.
- A control that only hurries what the page does by itself races it.
  Read it again beside a read the page asks again on its own (a failed
  re-read, retried with backoff) can vanish before the click lands, and
  the click then waits for a button that never comes back
  (`dispatch-plan.spec.ts`, #467). Press it until the state it leads to
  is shown (`readAgain`), never once and blind.
- A menu that has closed is not gone. A select menu's content stays
  mounted through its closing animation and keeps the focus in its
  search field: text filled into the next field in that window lands in
  the menu's search and opens it again, and the field stays empty
  (`dispatch-plan.spec.ts`, #467). After picking an option, wait for the
  listbox to be gone (`toHaveCount(0)`) before typing, and assert the
  field holds what was typed (`toHaveValue`) before submitting.
- What a process printed is not kept for a reader however late it comes.
  On macOS the last close of a terminal's slave side waits about 0.6 s for
  the master to read what is queued, then discards it: a child that printed
  and exited at once, read by a test that a loaded machine held back that
  long, gave an empty read and an end of file (`PWD []`, #281). The product
  keeps the terminal open itself until the process is reaped (`keepsTTY`
  in `internal/pty`), so the exit waits for its reader. A test of what a
  short-lived child printed proves it by holding its reader back past the
  system's own bound once the child is exiting (`exiting`, `pastDrain`),
  and runs a program directly, not under bash, whose exit on macOS waits
  for its reader whatever the parent holds and so hides the loss.
- A terminal's bytes are the kernel's, not the program's. The tty
  translates a newline to CR LF, and macOS's writes the CR again when its
  output queue fills between the two: a reader slower than the writer
  gets `30000\r\r\n` for seq's `30000\n` (62 in 30,000 lines on the Mac
  with a reader pausing 0.5 ms per 256 bytes, every line there; none on
  Linux, none for a reader that keeps up), and a check for the exact
  `\n30000\r\n` failed whenever one landed on the line it looked for
  (`expectTheEnd`, #281). Compare what the program printed with the
  carriage returns taken out (`printed` and `printedUntil` in
  `internal/api`, `lines` in `ext/local`), and keep exact bytes for tests
  of the bytes themselves.
- A port picked is not a port held, and a number free for one protocol
  is not free for the other. The Pebble tier's fake DNS took a UDP port
  from the kernel and asked for the same number over TCP, which a TCP
  socket already held: a connection an earlier test left in TIME_WAIT, a
  client's outbound port. Linux refuses a listener there, SO_REUSEADDR or
  not (0.8 % of picks on a busy machine, 4 % with a thousand loopback
  clients; "bind: address already in use", #281). Bind what you need and
  hand over the bound socket; where two binds must share a number, bind
  both before using either and pick again when the second is refused
  (`dnsPair` in `internal/certs`); where a program takes only an address,
  hold the port until it starts and start again on a loss
  (`pebblePorts`).
- A read the page keeps one of in flight is bounded, or one request that
  never answers holds every later read behind it. A spec that cut the
  events stream with a route and removed it left one inbox read on the
  page unanswered (the page's last route gone while the request was held
  in it), and the live inbox, its next reads queued behind that one, never
  showed what the stream's return brought (`decision-page.spec.ts`, CI on
  #684). The inbox gives up on a read past `INBOX_READ_WAIT_MS`, and the
  spec keeps a route on the page across the step it judges.
- A test's git repository does no maintenance of its own. Apple Git's
  `git commit` (2.54 on the Mac) starts a detached `git maintenance run
  --auto` that packs the loose objects and deletes them under the test's
  reads (go-git lists a repository's packs once per opening: `object not
  found`) and is still writing when `t.TempDir` removes the directory
  (`directory not empty`); Linux's git did neither, so the test passed
  there 30 of 30 and failed on the Mac 10 of 10 (#711). A test repository
  sets `maintenance.auto=false` and `gc.auto=0` (`scratch` in
  `internal/gitrepo`), as `internal/gitcli` does for every git Conductor
  runs; and a server read that meets a repack opens the repository again
  and reads once more (`read` in `internal/gitrepo`), since a run's
  repository is repacked by the agent's own git.
- A reply is judged by what its request carried, not by the moment it
  lands. A page opened with a wrong token sends every read with it; the
  gate opened on the first refusal and closed on the right token's save,
  and a refusal of the wrong one still on its way opened it again over
  the right one, on a loaded box only (`brakeman-act.spec.ts`, #281). The
  token a request was sent with goes with its refusal
  (`useWorkbenchToken().refused`), and a test holds one such reply with
  `page.route` until after the change, so the old code fails every run.
- A test's knob is a field of the thing it configures, never a package
  variable a running goroutine reads. A test that lowered the journal's
  bound of records kept, a package variable, after its journal had opened
  wrote it while the journal's writer, in a pass of its own, read it; and
  the cleanup that put it back ran before the journal's Close, registered
  ahead of it (`TestARemovalTellsRemoved`, a DATA RACE, #281). Set the
  knob as the thing starts (an option it reads at its start,
  `Options.maxRecords`), and where a test must change it on the way,
  change it under the lock its readers hold (`setMaxRecords`).
- A check's own setup is part of the check. A file it fetches is cached
  and its download retried, so a mirror's bad half hour fails nothing but
  itself: GitHub's release downloads answered 504 for thirty minutes and
  one try of Neovim's tarball and Electron's binary failed four jobs of an
  hour-long run, twice (2026-11-08, #281; `docs/checks.md` says how).
- A port probed free is not a port owned, and a health answer at it is
  not the child's: readiness is the answer the child alone can give, its
  own token accepted. Every worktree's e2e suite on a machine picks from
  the same hundred ports, and a server another run started there between
  the probe and the bind answered `/api/health` for a child that never got
  the port; the spec's first authenticated request met that stranger's
  token, a 401 (`switchyard.spec.ts`, 2026-11-08, #281). `startServer`
  asks `/api/whoami` with the child's token, and a refusal moves it to
  another port (`server-port.spec.ts` hands it a held one).
- Prove a flake fix the way it failed: `go test -race -count=30` (on the
  Mac too when it flaked there), `npx playwright test <spec> --repeat-each
  10`, and once more under load.
