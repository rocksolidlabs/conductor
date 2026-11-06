# Browser evidence: an element selected in a preview, captured for a ticket (Program item PV1)

A person looking at an agent's work in a browser can see that a button
does nothing, but cannot hand that sight to anyone. Today they open the
attempt's preview in a new tab (EN1's Open preview), take a screenshot by
hand, open the devtools, guess which component drew the button, and write
a ticket in words. This plan makes the sight itself the evidence: in a
preview inside Conductor a person **selects an element**, writes a short
**annotation**, selects more if they like, and Conductor keeps a
**capture**: the page's URL, the viewport and device mode, a screenshot,
the selected HTML and its neighbourhood, the computed styles, what really
receives a click there, the revision the preview was serving and whether
that revision was still changing, and the source files the page itself
says drew the element, where the page is instrumented to say so. A
capture is a durable record in Dispatch's store; it is sent, as a frozen
revision, to a new or existing **ticket** (its screenshot and its
document become the ticket's attachments, which DS2's launch package
hands every attempt), to **Brakeman**, and, once the items that own them
are on `main`, to a **review** as a finding or a requested change, and
to a **decision**.

It is Program item PV1 (the Program, section 4.5), written against
`main` at 43abe4f7 (PR #656, 2026-11-05) after reading the workbench's URL
frame (`web/app/components/EditorArea.vue`, `FileBrowser.vue`,
`utils/editorTabs.ts`, `composables/useEditorTabs.ts`), EN1's preview
(`internal/crew/manifest.go`, `steps.go`: `tree.names`, `tree.ready`,
`AttemptState.Preview`; `web/app/components/RunTasks.vue`,
`utils/tasks.ts`), Dispatch's attachments, launches and package
(`internal/dispatch/attachments.go`, `sweep.go`, `launches.go`,
`package.go`, `snapshot.go`, `model.go`;
`internal/api/dispatch_attachments.go`), Brakeman's requests and refs
(`internal/brakeman/request.go`, `refs.go`, `kinds.go`;
`internal/api/brakeman.go`, `brakeman_refs.go`, `brakeman_observe.go`),
the Electron shell (`desktop/src/main.ts`, `window.ts`, `ipc.ts`,
`preload.ts`), the Mermaid frame's build hook (`web/nuxt.config.ts`), the
ledger's trees and step receipts (`internal/crew/ledger`), and the plans
of the items it meets (below). It is reviewed before any of its code; the
PRs at the end are cut into issues after the review, one issue, one
branch `issue-N`, one PR each (AGENTS.md).

The names follow the railroad (the Program, section 1.2): Conductor is
the application, Yardmaster the agent harness whose runs serve the
previews, Dispatch the ticketing whose tickets receive captures, Brakeman
the sidekick a capture can be asked about. A preview's tree is a
**worktree** (an attempt's) or a **preview checkout** (a candidate's),
never a "workspace" (decision D5). "Capture" is this item's record;
"annotation" is one selected element or region in it with the person's
note; "the bridge" is the small script that reads a page for Conductor.
EN1's project manifests are named as such, never "recipes".

**Prerequisites.** The Program gives PV1 two, EN1 and DS2, and both are
Done and on `main`: EN1's manifest, services, readiness and `preview`
(`docs/plans/project-manifests.md`, the run page's Open preview, #502)
and DS2's plan revisions, launch packages and pinned attachments
(`docs/plans/dispatch-evidence.md`). Brakeman's requests with subjects
(BK1's first slice, `docs/plans/brakeman.md`) are on `main` too. Two of
the item's destinations belong to items that are not on `main`: a
review finding and a requested change need RV1
(`docs/plans/review-repair.md`: plan merged, its PRs in implementation;
`main`'s Dispatch store is still schema version 2, with no `reviews`
table), and a decision needs DR1 (`docs/plans/decisions.md`: plan
merged, in implementation; `main`'s ledger is schema version 7, with no
decision routes). So PR-1 to PR-8 build on `main` as it is and meet the
acceptance boundary on their own; PR-9 waits for RV1's PR-4 and PR-6,
and PR-10 for DR1's PR-4 and PR-7.

**The items this one meets, and who owns what they share.**

- **EN1** (`docs/plans/project-manifests.md`) owns the manifest, a
  tree's lifecycle, its receipts, its ports and `preview`. This item
  adds one name to what a tree's commands are given
  (`CONDUCTOR_PREVIEW_BRIDGE`, through `tree.names`, EN1's R8 rule) and
  one kind of tree (a candidate's preview checkout, PR-7) made by EN1's
  own `prepareTree` and ended by its `endTree`. EN1's R9 (the preview in
  a new tab) is lifted for the cases this plan proves (below); New tab
  stays.
- **DS2** (`docs/plans/dispatch-evidence.md`) owns attachments in a
  launch package. A capture sent to a ticket becomes two ordinary
  attachments; the package's format, bounds and pins are unchanged.
- **RV1** (`docs/plans/review-repair.md`) owns reviews, findings and
  the repair. PR-9 adds a finding kind, `capture`, by RV1's own rules
  (the request fingerprinted, the evidence filled by the server, never
  taken from the client), in the Dispatch schema version after RV1's.
- **DR1** (`docs/plans/decisions.md`) owns decision records. PR-10
  changes nothing of the record: a capture reaches a decision through
  DR1's comment route, and the send is this item's row.
- **BK1** (`docs/plans/brakeman.md`) owns the ref grammar; this item
  adds the kind `capture:<id>` and its resolution.
- **CG1** (the Code Context Graph, paid) owns any enrichment of a
  capture with components, handlers, tests, ownership and decisions; the
  `ext.ContextProvider` seam (`docs/plans/ext.md`) is unchanged here.
  A capture keeps what a graph would join on: repository-relative source
  paths at a named commit.
- **CS1** (sync) will replicate Dispatch; this item adds its entities'
  field classes to `internal/dispatch/rules.go`, as DS1's plan lets each
  stream (open decision O10 stays CS1's).

## Articles it serves

1. No licence check: nothing here asks for a plan, a tier or a
   provider; the conditions are the admin token, Dispatch's store and,
   for a preview, a managed run's own.
2. Useful on its own: selecting, annotating, capturing, sending to a
   ticket or to Brakeman and previewing a candidate are free work on the
   local store and ledger, with no account and no network beyond the
   machine's loopback. The Program names local evidence among what the
   free build must do (the Program, section 2.1, article 2).
3. Nothing is gated by a flag: there is no paid part. The desktop
   shell's screenshot is a capability the page detects
   (`window.conductorDesktop.capturePreview` present), never a setting.
4. Stop, inspect, revoke, export and required approvals stay free and
   never degrade: every capture and send is a row of Dispatch's plain
   store with a receipt, its files content-addressed beside the
   attachments, carried whole by C6's export (`SnapshotHashes` names
   them); a candidate's preview checkout is ended by Close, by the run's
   stop and by the server's shutdown; nothing here approves anything.
5. Dependencies point one way: no new module; `internal/dispatch` still
   imports the standard library and `modernc.org/sqlite` alone;
   `internal/crew` never imports `internal/dispatch` (the API joins
   them). No new npm package: the bridge is built by Vite, which the web
   app already declares (`docs/dependencies.md`).
6. No feature build tag: a plain `go build` carries all of it.
7. The free defaults stand: no extension interface is called or
   changed. A capture's actor is C2's (`actorOf`); a preview checkout's
   commands go through the run's `ext.Runtime` like every tree's;
   Dispatch's new entities reach a registered sync store through the
   existing adapter (`internal/api/sync.go`).
8. Not touched: no hosted dependency exists in this item.
9. `CGO_ENABLED=0` stays: both stores stay `modernc.org/sqlite`; PNG
   headers are read with the standard library's `image/png`.
10. The repository contract holds: this plan before the code; race
    tests per package, vitest, Playwright and the desktop shell's tests
    for their surfaces; no new dependency, so `make notices` has nothing
    to regenerate; no frame or control message, so `internal/proto` and
    `web/app/utils/protocol.ts` are untouched, and `docs/protocol.md`
    gains the HTTP rows, the capture document, the bridge's messages and
    the `dispatch` event's two entities, each with its bound.
11. The railroad names keep their meaning: captures and their sends are
    Dispatch's records; runs, attempts, candidates, trees and receipts
    Yardmaster's; Brakeman is asked, never scheduled.
12. Execution stays behind one seam: a candidate's preview checkout is a
    tree whose steps and services go through the run's Runtime
    (`execFor`) and `internal/proc`; it starts no attempt and no
    session. "Boxcar constraints" below takes the eight one by one.

## Why and what happens

### What a browser lets the workbench read of a previewed page

The workbench is served from the Conductor server's origin (in the
desktop app, `http://127.0.0.1:<port>`; in a browser, whatever address
the person opened). An attempt's preview is on the server's loopback at
another port (`http://127.0.0.1:${CONDUCTOR_PORT_WEB}`, EN1's loopback
rule in `manifest.go`'s `loopbackURL`), so it is always a **different
origin** from the workbench, and the same-origin policy decides what the
workbench can do with it once it is framed:

- **It can** place the frame, size it, set its `src`, hear its `load`
  event (which fires for an error page too), and exchange messages with
  a script running in the page through `postMessage`, each side checking
  the other's origin.
- **It cannot** read the frame's document (`contentDocument` is `null`,
  `contentWindow.document` throws), its current URL after the page
  navigates (only the `src` it set), its title, its DOM, its computed
  styles, its console or its network; and it cannot get its pixels: no
  DOM API rasterizes a frame, a canvas cannot draw one, and a
  cross-origin image or frame taints anything that could.
- The existing frame's `sandbox="allow-scripts allow-same-origin
  allow-forms allow-popups"` keeps the framed page's own origin, so its
  storage and cookies work as in a tab (Conductor's own app keeps its
  token in `localStorage`, and an opaque origin would break it); it does
  not let the workbench read the page. Reading is decided by the two
  origins, and they differ by port.

So everything this item needs from the page's inside (its URL now, its
viewport, the element under the pointer, its HTML, neighbourhood,
styles, the element a click at its centre really reaches, the source
hints a dev build carries) needs **a script in the page**, the bridge.
Two things need more than the page's cooperation:

- **The pixels.** In the desktop app the shell's main process captures
  the window's own pixels (`webContents.capturePage(rect)`), the frame
  included, with no prompt. In a browser the only way is the person's
  permission to capture the tab (`navigator.mediaDevices.getDisplayMedia`
  with `preferCurrentTab`, cropped to the frame by Region Capture,
  `CropTarget.fromElement` and `MediaStreamTrack.cropTo`, which Chromium
  has and other engines do not); one frame is taken and the track
  stopped at once. Where neither is available, or the person declines,
  the capture is saved without a screenshot and says so.
- **A page that does not load the bridge.** The desktop shell's main
  process can run script in any frame of its own window, whatever its
  origin (`WebFrameMain.executeJavaScript`), so it puts the same bridge
  into any previewed page. A browser cannot: there a page must load the
  bridge itself (one script tag in its dev build, which this item adds to
  Conductor's own app), or it gets the frame, the screenshot and the
  annotations without element data, said in the capture.

Where the browser runs matters too. The preview is on the **server's**
loopback: a browser on the server's machine (the desktop app, or a
browser beside it) reaches it; a phone on the LAN or a viewer through a
share link reaches its own machine's loopback instead, where nothing
answers. Chromium treats `http://127.0.0.1` and `http://localhost` as
potentially trustworthy (W3C Secure Contexts), so a workbench opened
over TLS may still frame them; EN1 kept the preview out of the frame for
TLS (its R9), and this item frames it where it is reachable and keeps
New tab beside it. A remote preview is out of scope (below).

The smallest design that meets the requirements in both the browser and
the desktop app is therefore: **one bridge script, served by the
server**, loaded by the page in a browser and injected by the shell in
the desktop app; **one message protocol** between the bridge and the
workbench; **the pixels from the shell, or from the tab with the
person's permission**; and **the record, the revision and the source
check on the server**, which never takes them from the client. No proxy
(it would need a second origin per preview and would break dev servers
that use absolute paths and WebSockets), no headless browser on the
server (a new dependency, and a screen the person would drive through
screenshots).

### What a person sees after the item ships

- **A preview in Conductor.** A task row's Open preview (run page,
  palette, a ticket's run card) gains **Open in Conductor** beside New
  tab: the attempt's session page opens with a preview tab in the editor
  area (`kind: 'preview'`, beside the existing `url` tab). Its header is
  the **revision label**, read from the server every 5 s and at every
  run event: "attempt 2 of build · worktree at a1b2c3d with 3 changed
  files · changes as the agent works" (the warning colour: mutable work),
  or "candidate a1b2c3d of build · attempt 2 · as committed" for a
  candidate's preview checkout, or "a link · no revision" for a `url`
  tab. Beside it: the device mode (Fit the pane, Desktop 1280 × 800,
  Tablet 820 × 1180, Phone 390 × 844: the frame is that many CSS pixels,
  scaled down to fit), Refresh, New tab and **Capture**. The pane never
  frames the workbench's own origin.
- **Selecting.** Capture puts the page into selection mode (the bridge
  outlines the element under the pointer and names it: `button.save
  "Save"`); a click selects it, Escape leaves. The **capture editor**
  opens beside the pane: the screenshot (with the selection's box drawn
  over it, never burnt in), and the annotation: the person's note, the
  element's summary (tag, accessible name, a selector, its box), "a
  click at its centre reaches `div.overlay`" when something covers it,
  disabled and `aria-*` state, the source the page reports
  ("`web/app/components/SaveBar.vue` · Vue component `SaveBar`, checked
  in the worktree"), and, folded, its HTML, its neighbourhood and its
  computed styles as text. Select another element adds an annotation,
  up to 20; Draw a region marks a part of the screenshot with no
  element. Each annotation's note is edited, the annotations reordered
  or removed, until the capture is sent and after.
- **Saving.** Save keeps the capture (Dispatch's store; its screenshot a
  file beside the attachments). The server records the revision itself
  at the save: the tree's commit, its changed files, whether the tree is
  a worktree or a candidate's checkout, and where the attempt ran; when
  that differs from the label the pane showed at the selection it says
  "the worktree changed between the selection and the save".
- **Sending.** Send offers, today: **Create a ticket** (a project, a
  title prefilled from the first note; the description made from the
  annotations: the URL, the revision, each note with its element and
  source; the screenshot and the capture's document attached as
  `capture-<id>-r<rev>.png` and `.json`), **Attach to a ticket** (the
  run's own ticket first, when it was launched from one; the same two
  files and a comment), and **Ask Brakeman** (a request of kind `ask`
  citing `capture:<id>`, with the person's question). Once RV1 is on
  `main`, **Request a change** (a review of the candidate shown, verdict
  Request changes, one capture finding per annotation, applied by
  Yardmaster as RV1's repair) and **Add to a review** (the same findings
  in the person's review draft); once DR1 is, **Attach to a decision**
  (a comment on a pending decision of the run). Every send freezes the
  capture's revision as it was: the editor then says "sent as r2 to
  CND-14 · edited since: annotation 3's note", and sending again sends r3
  as a new send, never rewriting r2.
- **On the ticket.** The ticket page gains a **Captures** section: each
  capture sent to it with its revision label, its screenshot, its
  annotations as sent and the sends' receipts; Open the capture shows the
  live capture and what changed since. Propose a plan preselects the
  ticket's capture attachments to hand over (DS2's checkboxes), so the
  attempt that repairs it finds the screenshot and the document in
  `.conductor/attachments/` and the skill says how to read them.
- **A candidate's preview.** On a candidate (DS2's candidates, the run
  page's task row after a result), **Preview this candidate** makes a
  preview checkout at the candidate's commit with the run's project
  manifest (setup, services, readiness, preview; every step a receipt),
  opened in the pane with the label "candidate … · as committed". A
  capture there names the candidate it tested and the conditions (device
  mode, viewport, scale, the browser, the colour scheme): the
  verification screenshot. **Capture again** on a capture opens the same
  URL and device mode on a later candidate's preview, describes each
  element again by its selector, and saves a new capture linked to the
  first (`retakeOf`), so the person compares before and after side by
  side. The checkout ends on Close, after 30 minutes with no label read,
  at the run's stop, and at the server's shutdown, its services stopped
  and its cleanup run as EN1's trees end.
- **Conductor's own app.** The repository's `conductor.manifest.json`
  runs Conductor itself in a tree (its server on a port of the tree, its
  web app's dev server on another), and the web app's dev build loads
  the bridge when the tree names one, so the acceptance boundary is met
  on Conductor's own app in a browser as in the desktop app.

## The changes

### `internal/dispatch`: captures, annotations, sends (schema version 3, or the next after main's)

A capture is a Dispatch record: a person's evidence about work, which a
Workspace will share through CS1 (the ledger is never replicated), and
which a ticket, a review and a decision cite. Dispatch already keeps
content-addressed files with leases, a sweep and an export; a capture's
files use them. Dispatch does not know runs, trees or git: the API fills
those facts and the store keeps them.

**Tables** (`schema.go`, created by the migration and in the new
version's schema whole; the version is the one after `main`'s when the
PR is cut, 3 unless RV1's PR-1 has merged first, then 4, and the PR says
which):

| Table | Columns |
|---|---|
| `captures` | `id` (16 hex, `newID`), `rev` (from 1), `created_at`, `created_by` (the display name, else the actor), `actor`, `via` (`web`, `desktop`), `fingerprint`; the page: `url`, `title`, `viewport` (JSON, below), `selected_at`, `saved_at`; the origin, filled by the API: `origin` (`attempt`, `candidate`, `url`), `run_id`, `task_id`, `attempt_n`, `tree` (`worktree`, `preview`, empty for `url`), `commit_id`, `changed` (the tree's changed paths counted), `mutable` (0 or 1), `candidate` (DS2's form `<run>/<task>/<n>@<commit>`, set when the tree is the candidate's checkout), `where_ran`, `drift` (JSON: what the pane's label said at the selection, kept when the save's read differs); the image: `image_sha256`, `image_width`, `image_height`, `image_bytes`, `image_how` (`desktop`, `tab`, `none`), `image_scale`; `retake_of` (a capture id or empty), `deleted_at`, `annotations_rev` |
| `capture_annotations` | `capture_id`, `k` (1 to 20, its place), `kind` (`element`, `region`), `note`, `rect` (JSON: `{x, y, width, height}` in the screenshot's pixels), `element` (JSON: the bridge's report, below, empty for a region), `sources` (JSON: the checked sources, below); PK `(capture_id, k)` |
| `capture_sends` | `id` (16 hex), `capture_id`, `rev` (the capture's revision sent), `kind` (`ticket`, `brakeman`; `review` and `decision` reserved and refused until PR-9 and PR-10), `target` (a ticket key, `br-<n>`, a review or decision id), `body` (the capture's document as sent, frozen), `digest` (its SHA-256), `state` (`pending`, `sent`, `refused`), `why`, `files` (JSON: the attachment ids it added), `created_at`, `created_by`, `actor`, `op_id`, `fingerprint` |
| `capture_files` | `capture_id`, `sha256`; PK of the two: every content file a capture or one of its sends names (the screenshot, each sent document), which the sweep keeps, as DS2's `launch_files` keeps a package's |

Indexes: `captures (run_id, task_id, attempt_n)`, `captures
(created_at)`, `capture_sends (capture_id, created_at)`, `capture_sends
(kind, target)`; none unique beyond the keys (DS1's convention: no index
refuses a replicated row).

**The capture document** (`capture.go`, new, `conductor.capture/1`): the
JSON a send freezes and a ticket attachment carries, built from the rows
(`Document(id, rev)`), its shape in `docs/protocol.md`:
`{schema, id, rev, url, title, viewport, selectedAt, savedAt, origin:
{kind, run, task, attempt, tree, commit, changed, mutable, candidate,
where}, image: {sha256, width, height, how, scale}, annotations: [{k,
kind, note, rect, element, sources}], retakeOf}`. `viewport` is `{mode
(fit, desktop, tablet, phone), width, height, scale, dpr, colorScheme
(light, dark), userAgent}`. An element report is `{tag, id, classes,
role, name (accessible name), text, selector, box, attributes (name and
value pairs, `value` of every `input` dropped, a `password` field's
always), state: {disabled, hidden, ariaDisabled, ariaExpanded,
pointerEvents, inert}, hit: {self, selector, tag} (the element
`document.elementFromPoint` returns at the box's centre), html (its
outer HTML, cut), neighbourhood (its parent's outer HTML with each
sibling's subtree beyond depth 2 elided, cut), ancestors ([{tag, id,
classes, role}], innermost first), styles ([[property, value]] from a
fixed list), hints ([{how, file, line, column, component}]: what the
page's dev build says drew it)}`. `sources` are the hints the API
checked: `{path (relative to the tree), line, column, component, how,
found (in the tree at the save, or not), outside (the hint named a file
outside the tree)}`.

**Bounds** (`model.go`, each tested at its edge as DS1's are):
`MaxCaptureAnnotations` 20; `MaxAnnotationNote` 4,000 characters;
`MaxCaptureURL` 2,048 bytes; `MaxCaptureTitle` 200 characters;
`MaxElementText` 1,024 bytes; `MaxSelector` 1,024 bytes;
`MaxElementAttributes` 32, each value at most 512 bytes; `MaxElementHTML`
12 KiB; `MaxNeighbourhood` 24 KiB; `MaxAncestors` 12; `MaxStyles` 64,
each value at most 256 bytes; `MaxHints` 8, a file at most 1,024 bytes;
`MaxElementReport` 48 KiB as encoded (Go's JSON, its escapes counted);
`MaxCaptureDocument` 1 MiB as encoded (20 reports at their bound and the
page's part: `TestTheFullestCaptureFitsItsDocument` builds one);
`MaxCaptureImage` 4 MiB, a PNG of at most 4,096 pixels a side;
`MaxCaptureSends` 50 per capture; `MaxCaptures` 5,000 live in the store.
Every text is checked by DS1's field rules (`checkField`: no control
character but newline and tab in the long texts); HTML is text to the
store, shown as text everywhere and never rendered.

**Operations** (`captures.go`, new; each a typed operation with DS1's
`Op`, its receipt kept, a resend answered by `Replay` through the row's
`fingerprint` once the receipt is pruned; the fingerprint covers the
client's request, never what the server filled, as RV1's
`ReviewSubmitArgs` does):

- `CreateCapture(ctx, op, NewCapture, CaptureFacts, image io.Reader)`:
  the image, when there is one, is written and hashed through
  `AddAttachment`'s upload path (temp file under `attachments/tmp`,
  synced, a lease committed, published under its hash, its name made
  durable) before anything names it; then one transaction writes the
  `captures` row, its annotations, the `capture_files` row and the
  receipt, and releases the lease. `CaptureFacts` is what the API read
  (origin, commit, changed, mutable, candidate, where, the checked
  sources, the drift), stored with the rows and never fingerprinted.
- `EditCapture(ctx, op, id, expectedRev, []NewAnnotation)`: replaces the
  annotations whole (notes, order, removals, a re-selected element's
  report), bumps `rev`; refused `stale` for a `rev` that is not the
  row's, `not_found` for a deleted capture. A re-selected element's
  sources come checked from the API as at creation.
- `DeleteCapture(ctx, op, id)`: tombstones (`deleted_at`); its files stay
  while a send names them (the send's ticket attachments are the
  ticket's).
- `SendCapture(ctx, op, id, rev, Destination, SendFacts)`: refused
  `stale` unless `rev` is the capture's revision now (a person sends
  what they see). The document is built, measured and published as a
  content file through the upload path (lease first); one transaction
  then writes the send row (`sent` for a ticket, `pending` for
  Brakeman), the `capture_files` row of the document, and for a ticket
  destination: a new ticket (`Destination{Project, Title}`, its
  description made by `ticketText`, within `MaxTicketDescription`) or
  the existing one (`Destination{Key}`), two attachment rows naming the
  screenshot's and the document's hashes (no file copied: one content,
  one file), a comment naming the capture and its revision, and the
  ticket's `rev` bumped once with its change rows; refused `bound` when
  the ticket's 50 attachments would be passed. `MarkSent(id, target)` and
  `MarkRefused(id, why)` settle a pending send.
- Reads: `Capture(id)` (with its annotations and sends), `Captures(by
  Run/Ticket/Recent, after, limit)` (a page, at most 100, newest first),
  `CaptureSends(kind, target)`, `PendingSends()`, `Document(id, rev)`.
- `rules.go`: a capture's page, origin and image fields `immutable`; its
  annotations one `scalar` field, merged by `rev` (a replica's later
  revision wins, an equal one keeps both as a conflict for a person, as
  DS1's scalar rule says); sends `append`; a delete a tombstone. Each is
  a local schema decision CS1 may revise (open decision O10 stays CS1's).
- `sweep.go`: `collectOrphans` keeps a hash any `capture_files` row
  names. `snapshot.go`: `SnapshotHashes` adds every `capture_files` hash,
  so C6's export carries the files. `OnChange` reports the entities
  `capture` and `capture_send`.

### `internal/crew` and `internal/crew/ledger`: the bridge's name, a candidate's preview checkout

- **The bridge's URL in a tree.** `Engine.SetPreviewBridge(url string)`
  (set once, from the server's listener:
  `http://127.0.0.1:<port>/preview-bridge.js`; `cli/runServe` tells the
  API its listen address once it listens, through a new
  `Server.SetListen(addr)` beside `SetReach`, and the API sets the
  engine's, as `server.go` sets its Runtime) and `tree.names`
  adds `CONDUCTOR_PREVIEW_BRIDGE` beside `CONDUCTOR_PREVIEW_URL` for a
  tree that has a preview. A tree without a preview, or a server with no
  loopback listener, gets none. The skill's text (`internal/agents/
  skill.go`) names it.
- **A candidate's preview checkout** (`preview.go`, new): `Engine.
  OpenPreview(ctx, run, task, n, by)` for an attempt that submitted a
  result whose commit is known, in a run this server's ledger owns and
  that is not continued elsewhere (`409 run_continued` as the task
  commands answer): the run's project manifest is resolved for that
  commit (`resolveTree`, a `file` manifest read at the commit as EN1
  reads it); the checkout is `<worktree>.preview`, added detached at the
  commit (`gitcli.AddDetachedWorktree`), or, when it is there already,
  reused only if `checkoutIntact` finds it at the commit with no tracked
  change (untracked files, a dev server's caches, are left as they are)
  and refused otherwise with the path and the changed files named:
  Conductor removes nothing it made (EN1's R7). Then EN1's
  `prepareTree` runs on it as on a result's checkout, setup, services,
  readiness and preview, every step a receipt under the attempt (the
  `checks` table, its phase, `dir` the checkout's, the next `k`);
  `ClosePreview(run, task, n, seq)` and the ends below run EN1's
  `endTree`: services stopped, cleanup run, ports released. Bounds: at
  most 2 preview checkouts open per server (`maxPreviews`, `429
  previews_busy` naming the open ones); one per candidate at a time; 20
  opens per candidate in the ledger. Ends: Close; 30 minutes with no read
  of its label (`previewIdle`, the pane reads it every 5 s); the run's
  stop; the server's shutdown, as every tree's. A checkout's services
  that the last server left running are reported at the next start,
  never signalled (EN1's rule).
- **The ledger** (schema version 8, or the next after main's when the
  PR is cut: DR1's PR-1 takes 8, so this is 9 if that has merged, and
  the PR says which): a table `previews` (`run_id`, `task_id`, `n`,
  `seq` (from 1), `dir`, `commit_id`, `manifest`, `ports`, `preview`,
  `state` (`resolved`, `ready`, `ended`), `why`, `interrupted`,
  `opened_at`, `opened_by`, `ended_at`, `where_ran`; PK `(run_id,
  task_id, n, seq)`, foreign key to the attempt, cascade on the run's
  delete), created by the migration (a new table: no row of an older one
  is rewritten, the conservative rule); its writers (`Tx.OpenPreview`,
  `Tx.PreviewReady`, `Tx.EndPreview`), the reads, `Recover` reporting a
  preview `ready` or `resolved` at start as interrupted, the snapshot
  unchanged (the table is in the database). EN1's `trees` table is left
  for the engine's own trees; its key (`run_id, task_id, n, kind`) holds
  one tree of a kind per attempt, and a preview is opened many times.
  Events: `preview.opened`, `preview.ready`, `preview.ended` with
  `{task, attempt, seq, preview, why}`.
- `AttemptState` gains `Previewing` (`{seq, preview, state}` while a
  preview checkout of it is open), read with the run, so every page
  shows it.

### `internal/api`: the bridge, the label, the captures and their sends

- **The bridge** (`previewbridge.go`, new): `GET /preview-bridge.js`,
  unauthenticated (a script tag carries no header; it holds no secret),
  serves the built bridge (from the embedded app, `internal/web`) with
  the server's own workbench origins written into it: every origin the
  server answers the workbench on (its listen address, its loopback
  aliases `127.0.0.1`, `localhost` and `[::1]` at that port, the TLS
  listener's, `PublicURL`'s), at most 16, each at most 256 bytes.
  `Content-Type: text/javascript; charset=utf-8`, `Cache-Control:
  no-cache`, `Cross-Origin-Resource-Policy: cross-origin` (a dev server
  that sets COEP may still load it), `X-Content-Type-Options: nosniff`.
  The bridge answers only messages from those origins.
- **The label**: `GET /api/runs/{run}/tasks/{task}/attempts/{n}/preview`
  (`readable`, as the attempt's steps are) → `{url, tree, seq?, commit,
  changed, mutable, state, candidate?, where, readAt}`: the attempt's
  worktree preview while EN1 offers it, or its open preview checkout's
  (`?seq=`); `commit` and `changed` read now with `gitcli` (`rev-parse
  HEAD`, `status --porcelain=v1 -z`, argv, the C locale, at most 5 s and
  10,000 paths counted), `mutable` true for a worktree and for a checkout
  whose tracked files changed; `404 no_preview` when neither is open. A
  read of a checkout's label resets its idle clock.
- **A candidate's preview**: `POST
  /api/runs/{run}/tasks/{task}/attempts/{n}/preview` (admin, `{commandId}`
  → `202 {seq, state}`; `200` for a repeat; `409 not_candidate`, `409
  run_continued`, `409 preview_dirty`, `429 previews_busy`) and `POST
  …/preview/{seq}/close` (admin, `{commandId}` → `{state}`); each a
  command with its receipt in the ledger, as the task commands are.
- **Captures** (`captures.go`, new; admin only, `requireAdmin`; every
  JSON body read with `DisallowUnknownFields`):

| Route | Body → reply |
|---|---|
| `POST /api/captures` | `multipart/form-data`: `capture` (JSON, at most 1 MiB: `{opId, from: {run, task, attempt, seq?} \| {url}, url, title, viewport, selectedAt, label (the pane's last label: commit, changed, readAt), annotations, via}`), `image` (optional, `image/png`, at most 4 MiB); the body at most 5 MiB + 64 KiB → `201 {receipt, capture}`; `200` the same for a repeat; `400 invalid` (a field past its bound, a URL whose origin is not the preview's, an image that is not a PNG or is past 4,096 pixels a side, `image/png`'s `DecodeConfig` the judge); `404 no_preview` (the attempt's preview, or the checkout named, is not open now); `413 bound`; `409 expired` (the image's lease swept; the client sends again) |
| `GET /api/captures` | `?run=&task=&attempt=&ticket=&after=&limit=` (1 to 100) → `{captures, next?}`, newest first, each a summary (its first note cut to 200 characters, its label, its sends' targets) |
| `GET /api/captures/{id}` | the capture whole with its sends' summaries (at most 1.2 MiB) |
| `GET /api/captures/{id}/image` | the PNG, `Content-Type: image/png`, `nosniff`, `Content-Security-Policy: sandbox`, `X-Conductor-SHA256`; `404` without one |
| `GET /api/captures/{id}/sends/{send}` | the send with its frozen document |
| `POST /api/captures/{id}/edit` | `{opId, expectedRev, annotations}` (1 MiB) → `{receipt, capture}`; `409 stale` |
| `POST /api/captures/{id}/delete` | `{opId}` → `{receipt}` |
| `POST /api/captures/{id}/send` | `{opId, rev, to: {kind: "ticket", project?, title?, key?} \| {kind: "brakeman", question}}` (8 KiB) → `201 {receipt, send}`; `409 stale`, `409 brakeman_off`, `429 queue_full` (Brakeman's), `404 not_found` (a ticket), `409 bound` |

  **What the server fills, never the client.** On a create, the API
  resolves `from`: for an attempt, the run's state now (the ledger and
  the engine), its preview URL (`AttemptState.Preview` or the open
  checkout's), and refuses a page `url` whose origin is not that
  preview's (`400 invalid`, "the page is http://127.0.0.1:5173, not this
  attempt's preview http://127.0.0.1:43117"); it reads the label as the
  label route does and records it; it records `drift` when the client's
  `label` (what the pane showed at the selection) names another commit
  or count; it makes each hint's file relative to the tree's directory
  (`filepath.Rel` after `filepath.Clean`, a hint outside the tree kept
  with `outside: true` and its path as reported, at most 1,024 bytes)
  and checks it exists through an `os.Root` opened on the tree's
  directory (`found`), the way `taskfile.go` writes through one; for a
  `url` capture, none of this, and `origin` says `url`. The candidate is
  set only for a preview checkout, at its commit, whose tracked files are
  unchanged.
- **Ask Brakeman** (`captureSendBrakeman`): the send row `pending`
  first, then a request of kind `ask` through the desk's own admission
  (`handleBrakemanAsk`'s path, factored as `askBrakeman(in)`, with the
  send's id as its `opId`, so a resend after a lost reply finds the same
  request: the desk's `ByOp`), subjects `[capture:<id>]` and the
  attempt's `attempt:` ref when there is one, the note the person's
  question with the capture's revision named; then `MarkSent(br-<n>)`.
  A restart settles a pending send the same way (`settleCaptureSends`
  at start, after Dispatch and the desk are open).
- **Brakeman reads a capture.** `brakeman_refs.go`'s resolver resolves
  `capture:<id>` (its revision and digest; `deleted` for a tombstone),
  and `brakeman_observe.go`'s read table gains `GET /api/captures/{id}`
  under `brakeman.ClassDispatch` (a turn may read a capture where it may
  read tickets), its reply the capture without the image's bytes (a
  turn reads text: the image's size and hash are named). `guide.go`
  gains the operation `capture_read`, so the MCP server's tools and the
  guide's sections carry it.
- `events.go`: the `dispatch` event's entities `capture` and
  `capture_send`, inside `maxDispatchEach`'s arithmetic as RV1's are.
- `docs/protocol.md`: the HTTP rows above, the capture document, the
  bridge's messages (below), the two entities.

### `internal/brakeman`: the `capture` ref

`RefCapture = "capture"`, `capture:<id>` (16 hex digits), in `refForms`
with its words and example; `Ref.Capture` holds the id; `ParseRef` and
`String` round-trip it (`TestACaptureRefParsesAndPrints`). Kinds whose
refs take any kind (an `ask`'s subjects, a `fact`'s evidence, a draft
ticket's `from`) take it with no other change.

### `web/app`: the bridge, the pane, the editor, the sends

- **The bridge** (`utils/previewBridge.entry.ts`, and its pure parts in
  `utils/previewBridge/report.ts`, `select.ts`, `hints.ts`): built as one
  classic script (`iife`) by Vite's library build, as the Mermaid
  renderer is (`web/nuxt.config.ts`'s `vite:serverCreated` hook for the
  dev server; for the built app the same entry emitted at the fixed path
  `preview-bridge.js`, which `internal/web` embeds and the API serves
  with the origins written in). It does nothing until a workbench origin
  asks. Selection mode draws its outline in a closed shadow root it adds
  to the page (no style of the page's touches it, none of its leaks
  out), takes pointer events in the capture phase while selecting and
  gives them back after; it reads, never writes, the page. The report:
  the selector (`select.ts`: an `id` when unique, else the shortest
  chain of tag, stable classes and `:nth-of-type` that
  `querySelectorAll` finds exactly once, at most 1,024 bytes), the
  accessible name (`aria-label`, `aria-labelledby`'s text, a `label`'s,
  the text), the box (`getBoundingClientRect`), the hit test, the state,
  the cut HTML and neighbourhood (`outerHTML` of a clone whose `input`
  values and `password` fields are emptied first), the styles from a
  fixed list of 64 (`display`, `visibility`, `opacity`, `pointer-events`,
  `position`, `z-index`, `inset`, the box model, `overflow`, flex and
  grid, `color`, `background-color`, `font-*`, `cursor`, `transform`,
  `filter`, `content-visibility` and the like), and the hints
  (`hints.ts`): a `data-conductor-source="<file>:<line>:<column>"`
  attribute on the element or an ancestor (any build may add it); a Vue
  dev build's component chain (`__vueParentComponent`, its `type.__file`
  and `type.__name`, which `@vitejs/plugin-vue` sets in development);
  Svelte's `__svelte_meta.loc`; React's `_debugSource` (React 18 and
  before) through the element's fiber key. A page with none reports
  none, and the capture says "the page gives no source".
- **The messages** (`conductor-preview/1`, `docs/protocol.md`): from the
  workbench, `hello {nonce}`, `select {nonce}`, `describe {nonce,
  selector}`, `cancel {nonce}`; from the bridge, `ready {nonce, version,
  url, title, viewport, hints: [how]}`, `selected {nonce, element}`,
  `described {nonce, element | missing}`, `cancelled {nonce}`. Each is a
  plain object with `conductor: "preview/1"`; the bridge accepts one only
  from `window.parent` at an origin it was given and posts only to
  `window.parent` at that origin; the workbench accepts one only from the
  frame's `contentWindow` at the preview's origin, with the nonce it
  sent, and drops one past 64 KiB as encoded or one that does not parse
  by its schema (`utils/capture.ts`'s `parseBridgeMessage`). One request
  is in flight at a time; a `hello` with no `ready` in 3 s means no
  bridge ("this page does not load Conductor's bridge: Capture takes the
  screenshot and your notes only. In the desktop app every page has
  it").
- **The pane** (`components/PreviewPane.vue`, new; `utils/editorTabs.ts`
  gains the kind `preview` with `{run, task, attempt, seq?}`;
  `EditorArea.vue` renders it): the frame (the existing sandbox
  attribute, `referrerpolicy="no-referrer"`), the label
  (`composables/usePreviewLabel.ts`: the route every 5 s and at every
  `run` event for the run, stopped while the tab is hidden), the device
  mode, Refresh, New tab, Capture; a `url` tab gains Capture too, its
  label "a link · no revision". `composables/usePreviewBridge.ts` holds
  the handshake and the requests, with a seam for the tests
  (`previewSeam`: the screenshot source and the bridge's replies, as
  `viewerSeam` is one for the viewers). The pane refuses to frame the
  workbench's own origin ("this is Conductor itself: open it in a new
  tab").
- **The screenshot** (`utils/screenshot.ts`): the desktop shell's
  `capturePreview(rect)` when `window.conductorDesktop` has it; else, in
  a browser with `getDisplayMedia` and `CropTarget`, the tab cropped to
  the frame, one frame drawn to a canvas, the track stopped; else none.
  The image is scaled to at most 2,048 pixels on its long side and
  encoded as PNG (`canvas.toBlob`), its scale recorded; one past 4 MiB is
  scaled again by half, once, then dropped with words.
- **The editor** (`components/CaptureEditor.vue`, `CaptureAnnotation.vue`,
  `CaptureElement.vue`, new; a `USlideover` on wide screens, a page on a
  phone): the screenshot with each annotation's box drawn over it and
  numbered; each annotation's note (`UTextarea`), its element's summary,
  its hit test, its sources (each a link opening the file in the
  attempt's editor at its line when `found`), and its HTML,
  neighbourhood and styles folded, as text in `CodeBlock`; Select
  another element, Draw a region, reorder, remove, Save; once saved,
  Send ▾ and the sends with "edited since" (`utils/capture.ts`'s
  `diffSinceSent`: annotation added, removed, moved, note changed,
  element re-selected). `composables/useCaptures.ts` holds the routes,
  minting `opId`s and retrying a lost reply once with the same id, as
  `utils/tasks.ts` does.
- **Sends and the ticket page**: `components/CaptureSend.vue` (the
  destinations; Create a ticket's project and title; Attach to a
  ticket's picker, `TicketPicker.vue`, with the run's own ticket first;
  Ask Brakeman's question), `components/DispatchCaptures.vue` on
  `DispatchTicketPage.vue` (the captures sent to the ticket, each with
  its label, screenshot, annotations as sent, sends and Open), and
  `DispatchPlanPropose.vue` preselecting attachments named `capture-*`
  when the revision is not based on another.
- **Run page and candidates**: `RunTasks.vue`, the palette's task rows
  and `DispatchCandidates.vue` gain Open in Conductor and Preview this
  candidate (while `previewing` is open: Open, Close); the session page
  (`pages/sessions/[id].vue`) opens a preview tab from its query
  (`?preview=<task>/<n>[/<seq>]`).
- **Conductor's own app** (PR-8): `plugins/previewBridge.client.ts`
  (new) adds `<script src>` for `runtimeConfig.public.previewBridge`
  when `import.meta.dev` and it is set; `nuxt.config.ts` sets
  `previewBridge` from `CONDUCTOR_PREVIEW_BRIDGE` and lets `nitro.
  devProxy`'s target come from `CONDUCTOR_DEV_API` (default
  `127.0.0.1:8080`, as now), so the dev server of a tree proxies to that
  tree's own Conductor server. The built app never loads the bridge.

### `desktop/src`: the bridge in any frame, the frame's pixels

- `preview.ts` (new): `injectBridge(wc, frameOrigin)`: finds the frames
  of the workbench window's `mainFrame.framesInSubtree` whose origin is
  `frameOrigin` and whose parent is the workbench's main frame, fetches
  the bridge from the server's own `/preview-bridge.js` with the
  workbench's origin written in, and runs it in each with
  `WebFrameMain.executeJavaScript` (a frame that already has it answers
  its `hello` and the second run does nothing); refused for the
  workbench's own origin and for a frame that is not a loopback URL.
  `capturePreview(wc, rect)`: `webContents.capturePage(rect)` with the
  rect checked (integers, within the window's content size, at least 1
  pixel), the image resized to at most 2,048 pixels on its long side
  (`NativeImage.resize`), `toPNG()`, refused past 4 MiB; one call in
  flight per window.
- `ipc.ts`: `conductor:previewBridge` and `conductor:capturePreview`,
  behind the existing `guard` (the sender must be the workbench).
  `preload.ts`: `previewBridge(origin)` and `capturePreview(rect)` on
  `window.conductorDesktop`; `web/app/utils/desktop.ts` mirrors them.
- No new Electron permission and no webview: the frame stays the
  workbench's own iframe.

### `conductor.manifest.json` (PR-8, the repository's root)

The project manifest that serves Conductor itself in a tree:
prerequisites `go version` and `node --version`; `env` none; ports
`api` and `web`; setup `npm --prefix web ci`; services `env
CONDUCTOR_DATA_DIR=${CONDUCTOR_STATE_DIR}/data CONDUCTOR_RENDEZVOUS=0
CONDUCTOR_WORKBENCH_TOKEN=tree-preview go run ./cmd/conductor serve
--dev --listen 127.0.0.1:${CONDUCTOR_PORT_API}` and `env
CONDUCTOR_DEV_API=127.0.0.1:${CONDUCTOR_PORT_API} npm --prefix web run
dev -- --port ${CONDUCTOR_PORT_WEB} --host 127.0.0.1` (argv arrays, no
shell); ready probes on both ports; preview
`http://127.0.0.1:${CONDUCTOR_PORT_WEB}/#token=tree-preview`. The token
is the tree's own throwaway server's, on the loopback, with its own data
directory under the tree's state directory; it is never the operator's,
and the inner server publishes nothing (`CONDUCTOR_RENDEZVOUS=0`).

### Docs

`docs/protocol.md` (each PR its rows), `docs/architecture.md` (a
section on previews and captures: the bridge, the two screenshot paths,
the record), `docs/repository-map.md` (the new files and the two schema
versions), `docs/features.md` (the entry, PR-8, extended by PR-9 and
PR-10), the skill's text (`internal/agents`: the capture files in a
package and how to read the document).

## Events, orderings and invariants

**A capture's life.** `draft` in the browser only (the selection and
the notes before Save are the page's; nothing is a record before),
then `saved` (rev 1), then edited (rev n), sent any number of times
(each send freezing the rev it names), deleted (a tombstone; its sends
and the ticket attachments they made stay).

- **What is written before what (create).** The image file is written,
  synced, leased, published and its name made durable before the
  transaction that names it; the rows and the receipt are one
  transaction; the lease is released in it. A crash between the two
  leaves a leased file the sweep takes after its grace, never a row
  naming a missing file (Dispatch's attachment rule).
- **What is written before what (send to a ticket).** The document is
  built from the rows at `rev`, published as a file with its lease, then
  one transaction: the send row, `capture_files`, the ticket (new or
  existing) with its attachment rows and comment, the receipt. The
  ticket attachment rows name the same hashes `capture_files` does, so
  the sweep keeps the files while either is live.
- **Send to Brakeman.** The send row `pending` is committed before the
  desk is asked; the desk's request carries the send's id as its `opId`;
  `sent` is written after the desk answers. A crash between the two is
  settled at the next start by asking the desk with the same `opId`
  (found: `sent`; Brakeman off or the queue full: `refused` with its
  words). No send is lost and none makes two requests.
- **The revision is the server's.** `commit`, `changed`, `mutable`,
  `candidate`, `where_ran` and the sources' `found` are read by the API
  at the save, never taken from the client; the pane's label at the
  selection is kept as `drift` only when it differs. A capture never
  names a candidate unless its tree is that candidate's checkout at its
  commit with tracked files unchanged at the save.
- **What was sent stays.** A send's `body` and `digest` never change;
  `EditCapture` writes only the live rows and the capture's `rev`; a
  send refused `stale` unless it names the revision now. A ticket
  attachment made by a send is the ticket's, removed only as any
  attachment is.
- **A candidate's preview checkout.** `preview.opened` (row `resolved`)
  is committed before the checkout is added or any step runs; each step
  is a receipt before its process starts (EN1's order); `preview.ready`
  with the URL is committed before the label offers it; the close
  transaction (`preview.ended`'s first half: the URL cleared) is
  committed before any service is signalled, so no label offers a URL a
  teardown is ending, and the port lease is released only once the end
  is committed (EN1's rule for trees). At most one open per candidate;
  `seq` only grows.
- **The bridge's messages.** The bridge answers only `window.parent` at
  an origin the server wrote into it (or the shell, for the desktop);
  the workbench accepts only the frame's window at the preview's origin
  with its nonce. Nothing the page sends is executed or rendered as HTML
  by the workbench; HTML in a report is text.
- **Migration.** Dispatch: version 2 (or RV1's 3) to the next, the four
  tables and their indexes created, no row of an older table rewritten;
  a store at a newer version than the binary is refused as today. The
  ledger: the next version, the `previews` table created; the same
  refusal. Each has its test that an older store opens, migrates and
  keeps every row.
- **Replication (CS1, later).** The field classes above; no index
  refuses a replicated row; a capture's files travel as attachments do.

## Out of scope

- **A preview reachable from another machine** (a phone on the LAN, a
  viewer through a share link or the switchyard): it needs the server to
  carry the preview's traffic on an origin of its own per preview, which
  is EN1's out-of-scope item and a later one's; a capture is shown
  everywhere the ticket is, but taken only where the preview is
  reachable. On Windows the desktop app reaching a preview served inside
  WSL is not out of scope: it must work with no step by the person (see
  Acceptance).
- **A server-driven browser** (a headless Chromium on the server, the
  DevTools Protocol): a new runtime dependency and a screen driven by
  screenshots.
- **Device emulation beyond the frame's size**: touch, a phone's user
  agent, a device pixel ratio other than the browser's. The capture
  records what was used.
- **The page's console and network**: the bridge reads the DOM it was
  asked about, nothing the page logged or fetched.
- **Redaction** of a screenshot: a capture is the operator's, kept on
  their machine, sent where they send it; input values and password
  fields are never read.
- **Captures by a share link's controller or by an agent**: the routes
  are the admin's.
- **Screenshots taken by a manifest's `verify` steps** (an automated
  visual check): a later item; a person's capture of a candidate's
  preview is the verification screenshot here.
- **Graph enrichment** of a capture (components, handlers, tests,
  ownership, decisions): CG1's, through its own seam.
- **A capture of the workbench itself**: the pane refuses its own origin.
- **Review findings and decision attachments before RV1 and DR1 are on
  `main`**: PR-9 and PR-10.

## Tests per surface

Go, every PR that touches it: `make lint`, `go test -race -count=1
-timeout 30m ./...`, `make lint-static`. Web: `npm --prefix web run
typecheck`, `npm --prefix web test`, `make test-e2e` (the PR's spec
alone first while iterating, one Playwright run per worktree). Desktop:
`make desktop-test` (after `npm --prefix desktop ci`). The named tests:

- `internal/dispatch` (PR-1): `TestACaptureIsStoredWithItsImageOnce`
  (two captures of one image, one file), `TestCaptureBoundsAtTheirEdges`
  (every bound above at its edge and one past),
  `TestTheFullestCaptureFitsItsDocument`, `TestACaptureCrashBetweenFileAndRow`
  (the lease swept, no row), `TestAStaleEditIsRefused`,
  `TestAnEditAfterASendKeepsWhatWasSent`, `TestSendToANewTicket`
  (the ticket, its description, two attachment rows naming the capture's
  hashes, the comment, one `rev` bump), `TestSendToATicketPastItsAttachments`,
  `TestASendNamesTheRevisionNow`, `TestAPendingSendIsSettledOnce`,
  `TestADeletedCaptureKeepsItsSends`, `TestTheSweepKeepsACapturesFiles`,
  `TestSnapshotHashesNameCaptureFiles`, `TestMigrationToCapturesKeepsEveryRow`,
  `TestCaptureFieldClasses` (`rules.go`), `TestReplayAnswersACaptureResend`.
- `internal/crew` (PR-2, PR-7): `TestTheBridgeURLReachesServices`,
  `TestNoBridgeWithoutAPreview`; `TestACandidatePreviewOpensAtItsCommit`
  (its steps' receipts under the attempt with the checkout's `dir`),
  `TestAPreviewIsEndedBeforeItsServicesAreSignalled` (a probe held at a
  gate), `TestAPreviewIdleIsEnded` (the engine's clock), `TestAPreviewEndsWithTheRun`,
  `TestAPreviewCheckoutWithChangesIsRefused`, `TestPreviewsAreBoundedPerServer`,
  `TestAPreviewLeftAtRestartIsReportedNotSignalled`,
  `TestAContinuedRunRefusesAPreview`.
- `internal/crew/ledger` (PR-7): `TestPreviewsTableMigration`,
  `TestAPreviewRowIsWrittenBeforeItsSteps`, `TestRecoverMarksAnOpenPreviewInterrupted`.
- `internal/brakeman` (PR-6): `TestACaptureRefParsesAndPrints`.
- `internal/api`: PR-2 `TestTheBridgeNamesTheServersOrigins`,
  `TestTheBridgeIsServedWithItsHeaders`; PR-3 `TestThePreviewLabelReadsTheTreeNow`
  (a file changed in the worktree between two reads), `TestACaptureTakesTheServersRevision`
  (a client's commit ignored), `TestTheTreeChangedBetweenSelectionAndSave`
  (`drift`), `TestACaptureOfAnotherOriginIsRefused`, `TestACaptureWithoutAnOpenPreviewIsRefused`,
  `TestSourcesAreMadeRelativeAndChecked` (inside and found, inside and
  missing, outside, a `..` path), `TestAnImageThatIsNotAPNGIsRefused`,
  `TestAnImagePastItsSideIsRefused`, `TestCaptureRoutesAreAdminOnly`
  (a share link's controller, an agent's and a Brakeman turn's tokens
  refused), `TestTheImageIsServedSandboxed`; PR-6 `TestSendToBrakemanCitesTheCapture`,
  `TestALostBrakemanSendIsSettledAtStart`, `TestBrakemanReadsACaptureUnderItsDispatchClass`
  (refused without the class), `TestTheGuideCarriesCaptureRead`; PR-7
  `TestPreviewCommandsHaveReceipts`.
- `internal/agents` (PR-6): the skill's golden with the capture files.
- vitest: PR-2 `web/app/utils/previewBridge/report.test.ts` (the cut at
  each bound on an element boundary, input values and password fields
  never in the HTML, the styles list), `select.test.ts` (a selector
  found exactly once, an `id` preferred only when unique),
  `hints.test.ts` (Vue, Svelte, React 18 and `data-conductor-source`
  from fake elements; none); PR-4 `web/app/utils/capture.test.ts`
  (`parseBridgeMessage`: the origin, the source, the nonce, a message
  past 64 KiB, an unknown type; the label's words; `diffSinceSent`),
  `web/app/composables/usePreviewBridge.test.ts` (the 3 s hello's words,
  one request in flight), `web/app/utils/screenshot.test.ts` (the scale
  to 2,048, the second halving, the drop); PR-5
  `web/app/utils/desktop.test.ts` additions.
- desktop (PR-5): `desktop/src/preview.test.ts` (a frame found by its
  origin and parent with fake frames; the workbench's own origin and a
  non-loopback frame refused; the rect's checks; the resize and the
  4 MiB refusal; one call in flight), `ipc.test.ts` (the two channels
  refuse a sender that is not the workbench).
- Playwright: PR-4 `web/e2e/preview-capture.spec.ts` (a managed crew of
  stub workers whose manifest serves `web/e2e/fixtures/preview/` through
  a small `python3` service that writes `CONDUCTOR_PREVIEW_BRIDGE` into
  its page; Open in Conductor; the label "worktree … · changes as the
  agent works"; Capture, select a button covered by an overlay, the hit
  test's words, a second element, a region; Save; the record read back
  with the server's commit; the screenshot through `previewSeam`, since
  the tab capture's prompt is no test's; the page without the bridge
  saying so after 3 s); PR-6 `web/e2e/capture-send.spec.ts` (Create a
  ticket and its two attachments and description; an edit, "edited
  since", a second send of r2 beside r1's; Attach to the run's ticket;
  Ask Brakeman with the stub's turn reading `capture_read`); PR-7
  `web/e2e/candidate-preview.spec.ts` (Preview this candidate, the label
  "as committed", Close, the receipts on the run page, a run stopped
  ending it); PR-8 `web/e2e/capture-repair.spec.ts` (the loop on a stub:
  capture, ticket, a plan revision with the capture attachments
  preselected, a launch, a stub worker that reads
  `.conductor/attachments/capture-*.json`, edits the file its first
  source names and submits, Preview this candidate, Capture again, the
  ticket's Captures showing both); PR-9 `web/e2e/capture-review.spec.ts`;
  PR-10 `web/e2e/capture-decision.spec.ts`. Each spec waits on what the
  page draws and on the server's records (AGENTS.md, Tests that do not
  flake), never on a clock; `stopStrays` first; tiles and rows found by
  id.
- **By hand, what no test reaches**: the real tab capture prompt in
  Chromium and its crop to the frame; the desktop app's `capturePage`
  pixels on Linux, macOS and Windows; the desktop app on Windows
  framing a preview served inside WSL; Firefox and Safari framing a
  loopback preview (recorded, not required); a real agent repairing from
  a capture (Acceptance).

## Boxcar constraints

1. The engine starts an attempt only through `crew.Launcher`: honoured;
   a candidate's preview checkout starts no attempt and no session; the
   repair of a captured ticket is an attempt of DS2's launch like every
   one; Brakeman is asked through the desk, which launches its turn
   through `ext.Host` as today.
2. `session.Local` drives a `session.Process`: not touched; nothing
   here reaches a session, a pid or a tty, and nothing is typed into a
   session.
3. An attempt's context is files relative to its worktree: honoured; a
   capture reaches an attempt as two files of its launch package
   (`.conductor/attachments/`, written through the attempt's `os.Root`
   by DS2's code), and, after PR-9, as the text of a repair's findings
   in `task.md`; nothing an attempt needs is only in the server's memory.
4. The agent talks to the server over the network: honoured; the bridge
   is fetched over HTTP from the server's address, a Brakeman turn reads
   a capture over HTTP with its token, the preview is reached by its URL;
   no socket-only or in-process channel.
5. Checks go through `internal/proc`: honoured; a preview checkout's
   steps and services go through the run's Runtime (`execFor`) and
   `internal/proc` as every tree's do; the label's git reads are argv
   arrays through `internal/gitcli`, as the Files tab's are, and are no
   check.
6. Platform splits stay platform tags, the UI shows capabilities:
   honoured; nothing differs by build tag; the desktop's screenshot and
   injection are capabilities the page detects on
   `window.conductorDesktop`, never a plan or a flag.
7. Evidence rows carry `where`: honoured; a preview checkout's step
   receipts carry `where_ran` like every step's, the `previews` row has
   its own, and a capture copies the attempt's; nothing here assumes a
   native process (the preview is a URL, its tree a directory the
   server reads with git, which F's plan will say how to read in a
   guest).
8. `hostagent` is a launch site as much as `api`: not touched; managed
   runs and their previews run on the server; a hosted session's `url`
   tab may capture as any `url` tab does, with no revision.

## Open-core checklist

- [x] Article 1: no licence or plan check in the store, the engine, the
  routes, the pages or the shell; the conditions are the admin token, a
  managed run's own and Dispatch's.
- [x] Article 2: the whole of it, the preview in the pane, selection,
  annotations, captures, the candidate's preview, the sends to a ticket
  and to Brakeman (and to a review and a decision once those items are
  on `main`), ships in the open-source build with no account.
- [x] Article 3: nothing is gated; the desktop's screenshot is a
  capability of the shell, not a flag a fork could flip.
- [x] Article 4: captures and sends are rows of the plain store with
  receipts, exported whole by C6; a preview checkout ends on Close, on
  the run's stop and on shutdown; nothing here weakens an approval.

## The PRs

Each PR is one issue and one branch cut from `main`, a draft PR at its
first commit, the checks for its surfaces before every push, its body
ticking the open-core checklist (articles 1 to 4) and naming the agent
that made it; each leaves `main` working and green on its own (a store
operation without its route is reached by its tests, a route without its
page by the tests, a page without its later destinations offers the ones
there are). Parallel means the two touch no common Go package, no
common directory under `web/` or `desktop/`, and no common file under
`docs/` or at the root.

PR-1 to PR-8 build on `main` as it is (EN1, DS2 and Brakeman's requests
are there) and meet the acceptance boundary. PR-9 waits for RV1's PR-4
(the review routes) and PR-6 (the Review view and its draft), and PR-10
for DR1's PR-4 (the decision routes) and PR-7 (the decision card): each
names its schema version and the rows it adds when it is cut.

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with |
|---|---|---|---|---|---|---|
| PR-1 | dispatch: captures, annotations, sends and their files (schema version 3, or the next) | the four tables, indexes and migration, the bounds, the capture document, `CreateCapture`, `EditCapture`, `DeleteCapture`, `SendCapture` to a ticket with its attachments and comment, `MarkSent`, `MarkRefused`, the reads, `rules.go`'s classes, the sweep's and `SnapshotHashes`' files, `OnChange`'s entities | `internal/dispatch` | the `internal/dispatch` tests above | none | PR-2 |
| PR-2 | bridge: the script, its build, its serving and its name in a tree | `previewBridge.entry.ts` and `utils/previewBridge/`, the Vite library build for the dev server and the built app, `previewbridge.go` with the origins, `Server.SetListen` and its call in `runServe`, `Engine.SetPreviewBridge` and `tree.names`, the skill's line, `docs/protocol.md`'s bridge messages and route | `web/app/utils`, `web/nuxt.config.ts`, `internal/web`, `internal/api`, `internal/crew` (`steps.go`), `internal/cli` (`serve.go`), `internal/agents`, `docs/protocol.md` | the PR-2 vitest and Go tests above | none | PR-1 |
| PR-3 | api: the preview label, captures created, read, edited and deleted | `captures.go`'s routes but send, what the server fills, the label route, the image route, the `dispatch` event's entities, `docs/protocol.md`'s rows and the capture document | `internal/api`, `docs/protocol.md` | the PR-3 `internal/api` tests above | PR-1, PR-2 | none |
| PR-4 | web: the preview in the pane, selection, annotations, a capture saved | `kind: 'preview'` tabs, `PreviewPane`, `usePreviewLabel`, `usePreviewBridge` and `previewSeam`, `screenshot.ts` (the tab's path and none), `CaptureEditor`, `CaptureAnnotation`, `CaptureElement`, `useCaptures`, `utils/capture.ts`, Open in Conductor on the run page and the palette, the session page's `?preview=`, the fixture page and its service, `preview-capture.spec.ts` | `web/app/components`, `web/app/composables`, `web/app/utils`, `web/app/pages`, `web/e2e` | the PR-4 vitest files, `preview-capture.spec.ts` | PR-3 | none |
| PR-5 | desktop: the bridge in any frame and the frame's pixels | `desktop/src/preview.ts`, the two IPC channels and their guard, the preload's two functions, `utils/desktop.ts`'s mirror and `screenshot.ts`'s desktop path, `docs/architecture.md`'s paragraph | `desktop/src`, `web/app/utils`, `docs/architecture.md` | `preview.test.ts`, `ipc.test.ts`, `desktop.test.ts` | PR-4 | PR-6, PR-7 |
| PR-6 | sends: a ticket and Brakeman; the ticket's captures | the send route (ticket and Brakeman, `askBrakeman`, `settleCaptureSends`), the `capture` ref and its resolution, the observe read and `capture_read` in the guide, the skill's capture files, `CaptureSend`, `DispatchCaptures` on the ticket page, `DispatchPlanPropose`'s preselection, `capture-send.spec.ts`, `docs/protocol.md`'s send row | `internal/api`, `internal/brakeman`, `internal/agents`, `web/app/components`, `web/app/composables`, `web/e2e`, `docs/protocol.md` | the PR-6 Go tests, `capture-send.spec.ts` | PR-4 | PR-5 |
| PR-7 | crew: a candidate's preview checkout | the ledger's `previews` table and migration (the next version), its writers and `Recover`, `Engine.OpenPreview`, `ClosePreview`, the idle end, the bound, `AttemptState.Previewing`, the two command routes and the label's `seq`, Preview this candidate on `RunTasks` and `DispatchCandidates`, Capture again, `candidate-preview.spec.ts`, `docs/protocol.md`'s rows and events | `internal/crew`, `internal/crew/ledger`, `internal/api`, `web/app/components`, `web/e2e`, `docs/protocol.md` | the PR-7 Go tests, `candidate-preview.spec.ts` | PR-3, PR-4 | PR-5 |
| PR-8 | Conductor's own app, the loop end to end, the docs and the entry | `conductor.manifest.json`, `plugins/previewBridge.client.ts`, `nuxt.config.ts`'s `previewBridge` and `CONDUCTOR_DEV_API`, the stub worker's `STUB_CAPTURE_FIX`, `capture-repair.spec.ts`; `docs/architecture.md`, `docs/repository-map.md`, `README.md`'s line, `docs/features.md`'s entry | `conductor.manifest.json`, `web/app/plugins`, `web/nuxt.config.ts`, `web/e2e`, `docs`, `README.md` | `capture-repair.spec.ts`; the scripts suite is not touched | PR-5, PR-6, PR-7 | none |
| PR-9 | review: a capture as a finding, and Request a change (after RV1) | the finding kind `capture` with `capture_ref` (`{capture, rev, k}`, the next Dispatch version), its binding by RV1's rules (the request fingerprinted, the annotation's text, element and sources filled by the server), the finding's words in a repair's `task.md` through RV1's change request, the send kind `review`, Request a change and Add to a review in `CaptureSend` and in RV1's draft, the capture finding's row in the Review view, `capture-review.spec.ts`, the docs and the entry's paragraph | `internal/dispatch`, `internal/api`, `web/app/components`, `web/app/composables`, `web/e2e`, `docs` | the PR-9 tests in `internal/dispatch` and `internal/api` (`TestACaptureFindingIsFilledByTheServer`, `TestRequestAChangeRepairsWithTheCapture`), `capture-review.spec.ts` | PR-8; RV1's PR-4 and PR-6 | none |
| PR-10 | decision: a capture attached to a pending decision (after DR1) | the send kind `decision` (DR1's comment route, kind `comment`, the capture named in its text with its revision; the send row this item's), the decision card's captures from `capture_sends`, Attach to a decision in `CaptureSend`, `capture-decision.spec.ts`, the docs and the entry's last paragraph | `internal/api`, `web/app/components`, `web/e2e`, `docs` | `TestADecisionSendIsACommentAndARow`, `TestASendToAnAnsweredDecisionIsRefused`, `capture-decision.spec.ts` | PR-8; DR1's PR-4 and PR-7 | none |

Sizes, as the owner asked (2026-11-05: a row is a batch, 300 to 800
changed lines, at most about 1,500 with its tests): PR-1 about 750 lines
and 750 of tests; PR-2 about 550 and 400; PR-3 about 700 and 650; PR-4
about 800 and 500; PR-5 about 350 and 300; PR-6 about 700 and 550; PR-7
about 750 and 650; PR-8 about 400 and 350, with the docs; PR-9 about 550
and 500; PR-10 about 300 and 300.

## Owner decisions

Dated 2026-11-05. Each is a default the owner can overturn with a dated
row in the Program's status log, its convention for a decision the owner
did not make (the Program, section 3.1); the reasoning is why it was
taken.

1. **The open decisions named with this item.** O4 (SW1's human policy)
   and O5 (CG1's graph tooling) are not touched: a capture names its
   actor as C2 gives it and keeps repository-relative sources a graph
   can join on later. O8 (pricing) does not bear on a free item. O10
   (Dispatch's merge rules, CS1's) is extended as DS1's plan lets each
   stream: a capture's facts immutable, its annotations one scalar by
   revision, sends append only. None blocks this item.
2. **One bridge, loaded by the page in a browser and injected by the
   shell in the desktop app.** Chosen over a proxy on a second origin
   per preview (dev servers' absolute paths and WebSockets, a port and a
   certificate per preview, reach mapping) and over a headless browser
   on the server (a new dependency and a screen driven by screenshots).
   A browser gets element data only from a page that loads the bridge;
   the desktop app gets it from any page.
3. **The pixels from the shell, or from the tab with the person's
   permission, or none.** The desktop's `capturePage` with no prompt;
   in a browser Chromium's tab capture cropped to the frame; elsewhere
   no screenshot, said in the capture. Chosen over drawing the DOM into
   a canvas in the page (a library, wrong for cross-origin images and
   many CSS features) and over requiring the desktop app.
4. **Captures live in Dispatch's store.** They are a person's records
   about work, cited by tickets and reviews (both Dispatch's), shared
   later with a Workspace by CS1, and their files belong with the
   attachments' content-addressed files, sweep and export. Chosen over a
   store of their own (a second file store, export rule and sweep) and
   over the ledger (never replicated, and a capture is not execution).
5. **A capture is sent as files, not as a new package part.** A ticket
   gets the screenshot and the capture document as two ordinary
   attachments, so DS2's package carries them unchanged and the repairing
   agent reads them in `.conductor/attachments/`. Chosen over a new part
   of the launch package, which would change DS2's format and bounds for
   one kind of attachment.
6. **The revision is read by the server at the save.** Commit, changed
   files, mutability, candidate and the sources' presence are the
   server's reads; the pane's label at the selection is kept only when
   it differs (`drift`). Chosen over trusting the page or the workbench,
   and over refusing a save when the tree moved (the person saw what
   they saw; the record says when it was no longer that).
7. **A candidate's preview is a checkout of its own, opened by a
   person.** Its tree is EN1's machinery on `<worktree>.preview`, its
   rows a new `previews` table, at most 2 open per server, ended after
   30 minutes unread. Chosen over keeping an attempt's worktree serving
   after its result (EN1 clears its preview at the attempt's close, and
   the agent may still be in it) and over reusing the checks' checkout
   (it exists only while checks run). The table is new because EN1's
   `trees` key holds one tree of a kind per attempt and a preview opens
   many times; adding a table is the conservative migration.
8. **What was sent is frozen; a later edit is a new revision.** A send
   keeps its document and digest; the editor shows what changed since;
   sending again sends the new revision as a new send. Chosen over
   editing a sent capture in place (a ticket's agent would read words
   the person wrote after it launched) and over locking a capture once
   sent (the person keeps working on the same evidence).
9. **Request a change is RV1's Request changes, and only on a
   candidate.** The authorized change is the one Yardmaster applies as a
   repair with a receipt; a capture of a worktree still being worked
   offers Create a ticket instead. Chosen over typing into the working
   agent's session (no record, and the Program forbids a second write
   path, section 1.5).
10. **A decision gets a capture as a comment, and the send is this
    item's row.** DR1's record stays as DR1's plan makes it ("comments
    carry text, never refs"); the card lists the captures sent to it
    from `capture_sends`. Chosen over adding refs to DR1's comments,
    which would change its schema for one source.
11. **Privacy by construction.** The bridge empties every `input`'s
    value and every password field before it reads HTML, reads no
    storage, cookie, console or network, and answers only the
    workbench's origins; the HTML a report holds is shown as text.
12. **Conductor's own app is previewed from the repository's manifest.**
    `conductor.manifest.json` serves a throwaway Conductor (its own data
    directory and token, publishing nothing) and the web app's dev build
    loads the bridge from the tree's name; the built app never loads it.
    Chosen over a crew-only manifest in the by-hand notes, so every
    contributor and agent previews Conductor the same way.
13. **The bounds** above (20 annotations, a 48 KiB element report, a
    1 MiB document, a 4 MiB PNG of at most 4,096 pixels a side, 2,048 on
    its long side as taken, 64 KiB bridge messages, 5,000 captures),
    sized so that a capture's two files fit DS2's 16 MiB of attachments
    in a package beside others.
14. **Schema versions** are the next after `main`'s when each PR is
    cut (Dispatch 3 unless RV1's PR-1 is first; the ledger 8 unless
    DR1's PR-1 is first), and each PR says which.

## Acceptance

The boundary: selecting a broken control in Conductor's own app
produces a ticket another agent repairs without a verbal description.
It is observed, not ticked:

- Once PR-8 is merged, on the owner's `conductor serve` (WSL) with the
  desktop app on Windows, and again in Chromium on the same machine: a
  scratch branch of this repository where one control of the web app is
  broken on purpose (a Save button covered by a transparent overlay, or
  disabled by a wrong condition), committed, and a managed crew with a
  work task by a real agent on the owner's own subscription (Claude Code
  or Codex), whose project manifest is the repository's
  `conductor.manifest.json`.
- A first run's attempt serves the broken app; Open in Conductor shows
  it in the pane with the label "worktree … · changes as the agent
  works". The person selects the broken control, writes no more than
  "Broken" as the note, saves, and creates a ticket from the capture.
  The ticket shows the screenshot, the element, the hit test that names
  the overlay (or the disabled state), and the Vue component's file in
  the tree; its description is the generated one with no words of the
  person's beyond the note.
- A plan revision of the ticket with the capture attachments handed
  over is approved and launched (DS2); the attempt reads
  `.conductor/attachments/capture-*.json`, edits the file it names, and
  submits a candidate whose checks pass. The person previews the
  candidate (Preview this candidate, "as committed"), captures again
  (the verification screenshot naming the candidate, the device mode and
  the browser), and reviews the evidence on the ticket page: both
  captures side by side, the candidate's receipts.
- On Windows, the desktop app frames the preview served inside WSL with
  no step by the person; if it does not, that is a defect of this
  stream, fixed in it (the shell forwards the port itself, as
  `desktop/src/udp-forwarder.ts` forwards ICE), never a networking
  instruction.
- The observation, and what was missing, slow or wrong, goes into
  `docs/features.md` (PR-8 when it comes before that PR merges, else a
  later docs PR of the stream); a bug the stream caused is fixed in the
  stream, a defect outside it filed.
- Done means: PR-1 to PR-8 merged and the observation recorded with the
  owner's word that the agent repaired the control from the capture
  alone. PR-9 and PR-10 then complete the destinations; the Program's
  status row moves to Done when they are merged too and each has been
  seen once (a capture sent as Request changes and repaired; a capture
  on a pending decision answered), never on the table alone. It is part
  of the R3 gate's browser-captured ticket (the Program, section 6).

What needs the owner: the review of this plan and its decisions;
cutting the ten issues; the observation, whose agent runs on the
owner's subscription, on the WSL box with the Windows desktop app; the
merges.
