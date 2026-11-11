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
receives a click there, the revision its tree held and whether that
revision was still changing, the revision the page says it serves where
it says one, and the source files the page itself
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
never a "workspace", a word kept for the paid tenant (the Program,
section 2.1, article 11). "Capture" is this item's record;
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
decision routes). So PR-1 to PR-9 build on `main` as it is and meet the
acceptance boundary on their own; PR-10 waits for RV1's PR-4 and PR-6,
and PR-11 for DR1's PR-4 and PR-7.

**The items this one meets, and who owns what they share.**

- **EN1** (`docs/plans/project-manifests.md`) owns the manifest, a
  tree's lifecycle, its receipts, its ports and `preview`. This item
  adds two names to what a tree's commands are given
  (`CONDUCTOR_PREVIEW_BRIDGE` and a per-tree secret,
  `CONDUCTOR_TREE_SECRET`, through `tree.names`, EN1's R8 rule) and a
  second owner of a tree beside the open attempt: a candidate's preview
  checkout (PR-7). EN1's tree steps (`resolveTree`, `prepareTree`,
  `treeReady`, `endTree`) belong to an attempt that is still open today
  (`errAttemptClosed`, `a.trees`, `PutTree` into `trees`); PR-7 gives
  them a tree owner, of which the open attempt is one (unchanged) and a
  preview the other, with its own rows, close ordering and port lease
  (below). EN1's R9 (the preview in a new tab) is lifted for the cases
  this plan proves (below); New tab stays.
- **DS2** (`docs/plans/dispatch-evidence.md`) owns attachments in a
  launch package. A capture sent to a ticket becomes two ordinary
  attachments; the package's format, bounds and pins are unchanged.
- **RV1** (`docs/plans/review-repair.md`) owns reviews, findings and
  the repair. PR-10 adds a finding kind, `capture`, by RV1's own rules
  (the request fingerprinted, the evidence filled by the server, never
  taken from the client), in the Dispatch schema version after RV1's,
  and pinned evidence files to RV1's change request (below).
- **DR1** (`docs/plans/decisions.md`) owns decision records. PR-11
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
  field classes to `internal/dispatch/rules.go`, with DS1's existing
  classes and their merge as they are, as DS1's plan lets each stream;
  the final merge rules for replicated Dispatch records stay CS1's to
  decide.

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
9. `CGO_ENABLED=0` stays: both stores stay `modernc.org/sqlite`; PNGs
   are checked with the standard library's `image/png`.
10. The repository contract holds: this plan before the code; race
    tests per package, vitest, Playwright and the desktop shell's tests
    for their surfaces; no new dependency, so `make notices` has nothing
    to regenerate. The bridge's messages are a protocol, so they touch
    the three places together (PR-2): `internal/proto/preview.go` (the
    version, every message type with its fields, the two size bounds),
    `web/app/utils/protocol.ts` (the same, which the bridge's gate and
    the workbench both parse by) and `docs/protocol.md`, with one
    fixture both languages must judge alike (below). `docs/protocol.md`
    gains too the HTTP rows, the capture document and the `dispatch`
    event's two entities, each with its bound.
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
  has and other engines do not); the track is kept for the draft's
  frames (the screenshot and the moment's checks, below) and stopped at
  Save, Escape or the pane's close, Chromium's sharing indicator showing
  meanwhile. Where neither is available, or the person declines,
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
  tab: the run's **preview page** opens (`/preview?run=&task=&attempt=`
  and `&seq=` for a candidate's checkout), which needs the run alone,
  never the attempt's session, so a candidate whose session the
  registry has collected opens there as a live attempt's does (a
  session page's `url` tab gains Capture too). The pane's header is
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
  "Save"`); a click selects it, Escape leaves. The arrow keys Up and
  Down step through the elements stacked under the pointer
  (`document.elementsFromPoint`), so a button under a transparent
  overlay is selected though the overlay is what a click reaches; the
  hit test still reports the overlay. Left and Right walk the DOM from
  the outlined element (its parent, its first element child), Shift+Up
  and Shift+Down its siblings, and Select by selector names one, so an
  element no hit test returns (`pointer-events: none`, an `inert`
  subtree) is selected too. The **capture editor**
  opens beside the pane: the screenshot (with the selection's box drawn
  over it, never burnt in), and the annotation: the person's note, the
  element's summary (tag, accessible name, a selector, its box), "a
  click at its centre reaches `div.overlay`" when something covers it,
  disabled and `aria-*` state, the source the page reports
  ("`web/app/components/SaveBar.vue` · Vue component `SaveBar`, checked
  in the worktree"), and, folded, its HTML, its neighbourhood and its
  computed styles as text. Select another element adds an annotation,
  up to 20; Draw a region marks a part of the screenshot with no
  element. **One capture is one moment.** The screenshot is taken at
  the first selection, and the draft keeps the moment it shows: the
  page's URL, the viewport and device mode with the frame's scroll
  offsets, the bridge's page epoch (below), the revision the page
  declares (below), and the label the pane read then (commit and
  content). Every later selection is checked against that moment before
  it joins the draft: the pane reads the label again, the bridge reports
  its URL, viewport, scroll, epoch and declared revision with the
  element, and, where there are pixels, a new frame is compared with the
  screenshot inside the boxes. When any of them differs (a client-side
  navigation, another device mode, a scroll, a redraw of the page, a
  worktree that moved, or pixels that no longer match: an animation, a
  hover, below),
  the element does not join: it starts a new draft with its own
  screenshot and moment ("the page changed since this screenshot: this
  element starts a new capture"), the first kept beside it to save. So
  no annotation's box is drawn over a screenshot of another moment, and
  no annotation names a revision its screenshot was not taken at; each
  element annotation keeps that moment, with the title, as its
  `observed`. Before Save, every part of the draft is the person's to
  change. After Save, the notes are
  edited, the annotations reordered or removed and regions drawn on the
  screenshot, until the capture is sent and after; an element observed
  later (a re-selection, another element of a page that moved on) is a
  new capture (Capture again), never new evidence under the first one's
  screenshot and revision.
- **Saving.** Save keeps the capture (Dispatch's store; its screenshot a
  file beside the attachments). The server records the revision itself
  at the save: the tree's commit, its changed files and a digest of
  what the tree holds (the tree id of its content, uncommitted changes
  included), whether the tree is a worktree or a candidate's checkout,
  and where the attempt ran; when that digest differs from the one the
  pane read at the selection it says "the worktree changed between the
  selection and the save", in the capture, its document and the ticket.
  What the page shows is kept apart from the tree: a dev server can
  still serve an older build after the files moved, so a page that
  declares the revision it loaded (below) is compared with the tree, and
  one that declares none is recorded "what the page shows is not
  confirmed against the tree".
- **Sending.** Send offers, today: **Create a ticket** (a project, a
  title prefilled from the first note; the description made from the
  annotations: the URL, the revision, each note with its element and
  source; the screenshot and the capture's document attached as
  `capture-<id>-r<rev>.png` and `.json`, the document alone when the
  capture has no screenshot), **Attach to a ticket** (the
  run's own ticket first, when it was launched from one; the same two
  files and a comment), and **Ask Brakeman** (a request of kind `ask`
  citing `capture:<id>`, with the person's question). Once RV1 is on
  `main`, **Request a change** (a review of the candidate shown, verdict
  Request changes, one capture finding per annotation, applied by
  Yardmaster as RV1's repair) and **Add to a review** (the same findings
  in the person's review draft); once DR1 is, **Attach to a decision**
  (a comment on a decision of the run, pending when offered). Every send freezes the
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
  first (`retakeOf`, sent by the client and checked by the server: the
  first may be of another run, as a ticket's repair is, below), so
  the person compares before and after side by side once the retake is
  sent: a retake is evidence of its run, not yet of the ticket, and
  reaches the ticket's Captures only as any capture does, by a send
  (Attach to a ticket, the run's own ticket offered first). The checkout ends on Close, after 30 minutes with no label read,
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
| `captures` | `id` (16 hex, `newID`), `rev` (from 1), `created_at`, `created_by` (the display name, else the actor), `actor`, `via` (`web`, `desktop`), `fingerprint`; the page: `url`, `title`, `viewport` (JSON, below), `selected_at`, `saved_at`; the origin, filled by the API: `origin` (`attempt`, `candidate`, `url`), `run_id`, `task_id`, `attempt_n`, `tree` (`worktree`, `preview`, empty for `url`), `commit_id`, `changed` (the tree's changed paths counted, all of them), `changed_paths` (JSON: the first of them within `MaxChangedPaths` and `MaxChangedPathsBytes`, below), `changed_cut` (0 or 1: some were left out), `content` (the tree id of what the tree held at the save, uncommitted changes and untracked files that are not ignored included, below), `mutable` (0 or 1), `candidate` (DS2's form `<run>/<task>/<n>@<commit>`, set when the tree is the candidate's checkout), `where_ran`, `scope` (JSON: `{crew, repo, project}` of the run at the save, the project of the ticket the run was launched from, kept so Brakeman's admission holds after the run has gone), `drift` (JSON: what the pane read at the selection, `{commit, content, changed, readAt}`, kept when the save's `content` or commit differs), `serving` (JSON: `{state, page}`: `declared` when the revision the page declares is the tree's at the save, `stale` when it names another, `unconfirmed` when it declares none or a commit alone over a tree with changes; `page` what it declared, below); the image: `image_sha256`, `image_width`, `image_height`, `image_bytes`, `image_how` (`desktop`, `tab`, `none`), `image_why` (why there is none), `image_scale`; `epoch` (the bridge's page epoch at the screenshot), `retake_of` (a capture id or empty), `retake_via` (`run`, `ticket` or `repo`, below), `deleted_at`, `annotations` (the pointer to the live annotation set: `{rev, set}`, the set's id) |
| `capture_annotations` | one **version** of an annotation, written once and never changed: `capture_id`, `sha256` (of the version's canonical JSON, its identity), `bytes`, `kind` (`element`, `region`), `note`, `rect` (JSON: `{x, y, width, height}` in the screenshot's pixels), `element` (JSON: the bridge's report, below, empty for a region), `observed` (JSON: `{at, url, title, viewport, epoch, revision, stable, label: {commit, content}}` as the bridge and the pane read them at the element's selection, the capture's moment, `stable` the pixels' verdict below, empty for a region), `sources` (JSON: the checked sources, below); PK `(capture_id, sha256)`: a version is content alone, with no actor, time or revision in it, so an edit that brings back earlier content names the earlier row, inserted once (`ON CONFLICT DO NOTHING` on its key: the same bytes by its hash) |
| `capture_annotation_sets` | one **revision** of a capture's annotations, written once and never changed: `capture_id`, `id` (16 hex, `newID`: the revision's identity), `rev` (from 1), `digest` (the SHA-256 of `members`: the content's, compared, never a key), `members` (JSON: the versions' `sha256`s in their order, at most 20, under 1.5 KiB), `at`, `actor`, `op_id`; PK `(capture_id, id)`, so a revert to an earlier set's members (A, then B, then A), an earlier order restored, and two replicas' identical edits are each a revision of their own with its actor and operation, and two replicas' sets of the same `rev` are both kept |
| `capture_sends` | a send's **input**, written once and never changed: `id` (16 hex), `capture_id`, `rev` (the capture's revision sent), `kind` (`ticket`, `brakeman`; `review` and `decision` reserved and refused by the store until PR-10 and PR-11 lift them), `asked` (JSON: the destination as the person named it: `{project, title}` or `{key}` for a ticket, a review or decision id), `digest` (the SHA-256 of the capture's document as sent, frozen: the document itself is the content file of that hash, never a second copy in the row), `request` (JSON, at most 8 KiB: the destination's input exactly as the target is asked, the Brakeman question and subjects or a decision's comment text, written before the target is asked and replayed whole by a recovery), `created_at`, `created_by`, `actor`, `op_id`, `fingerprint` |
| `capture_send_outcomes` | what became of a send, **appended, never changed**: `send_id`, `id` (16 hex), `kind` (`sent`, `refused`, `cancelled`), `target` (for `sent`: the ticket key, `br-<n>`, the review or the decision comment's id), `files` (JSON: the attachment ids it added), `why`, `at`, `actor`, `op_id`; PK `(send_id, id)`. A send's state is the projection below, never a column |
| `capture_send_cancels` | a person's cancel of a send, **written once, never changed**, committed before anything acts on it: `send_id`, `op_id`, `at`, `actor`; PK `(send_id, op_id)`. Whether it is done is read from the send's state and the desk, never kept (below) |
| `capture_files` | `capture_id`, `sha256`, `holder` (`capture`: the live capture's screenshot; or a send's id: the document it froze and the screenshot it carried); PK of the three: every content file a live capture or one of its sends holds, which the sweep keeps, as DS2's `launch_files` keeps a package's, and only while its holder is there (below) |

Indexes: `captures (run_id, task_id, attempt_n)`, `captures
(created_at)`, `capture_sends (capture_id, created_at)`,
`capture_send_outcomes (kind, target)`, `capture_files (sha256)`; none
unique beyond the keys (DS1's convention: no index refuses a replicated
row).

**The capture document** (`capture.go`, new, `conductor.capture/1`): the
JSON a send freezes and a ticket attachment carries, built from the rows
(`Document(id, rev)`), its shape in `docs/protocol.md`:
`{schema, id, rev, url, title, viewport, selectedAt, savedAt, origin:
{kind, run, task, attempt, tree, commit, changed, changedPaths, changedCut, content,
mutable, candidate, where, drift, serving}, image: {sha256, width, height, how,
why, scale}, annotations: [{k, kind, note, rect, element, observed,
sources}], retakeOf, retakeVia}`; `drift` is present, `{commit, content, changed,
readAt}` as the pane read them at the selection, whenever the save's
read differed, so the ticket's reader and the repairing agent see the
warning the record holds. `viewport` is `{mode
(fit, desktop, tablet, phone), width, height, scale, dpr, colorScheme
(light, dark), userAgent (at most 512 bytes), scrollX, scrollY}`. An element report is `{tag, id, classes,
role, name (accessible name), text, selector, box, attributes (name and
value pairs, `value` of every `input` dropped, a `password` field's
always), state: {disabled, hidden, ariaDisabled, ariaExpanded,
pointerEvents, inert, hovered (`:hover` at the read), motion (the
animations playing on it, its subtree and its ancestors, counted)}, hit: {self, selector, tag} (the element
`document.elementFromPoint` returns at the box's centre), html (its
outer HTML, cut), neighbourhood (its parent's outer HTML with each
sibling's subtree beyond depth 2 elided, cut), ancestors ([{tag, id,
classes, role}], innermost first), styles ([[property, value]] from a
fixed list), hints ([{how, file, line, column, component}]: what the
page's dev build says drew it)}`. `sources` are the hints the API
checked: `{path (relative to the tree), line, column, component, how,
found (in the tree at the save, or not), outside (the hint named a file
outside the tree), class (`tracked`: tracked by git at the tree's
commit; `untracked`; `ignored`; `vendor`: under a `node_modules`
directory, ignored or not)}`, ordered so that the innermost `tracked`
source comes first, then the other `tracked` ones innermost first, then
`untracked`, `ignored` and `vendor` ones: in a Nuxt app nearly every
control is a Nuxt UI component whose `.vue` file sits under
`node_modules` and carries `__file` in a dev build, and the first
source must be the app's own file, the one a repair edits.

**Bounds** (`model.go`, each tested at its edge as DS1's are):
`MaxCaptureAnnotations` 20; `MaxAnnotationNote` 4,000 characters;
`MaxCaptureURL` 2,048 bytes; `MaxCaptureTitle` 200 characters;
`MaxElementText` 1,024 bytes; `MaxSelector` 1,024 bytes;
`MaxElementName` 1,024 bytes (the tag, the accessible name, the id, the
role and the classes joined, each); `MaxElementAttributes` 32, a name
at most 64 bytes and a value at most 512; `MaxElementHTML` 12 KiB;
`MaxNeighbourhood` 24 KiB; `MaxAncestors` 12, each at most 512 bytes;
`MaxStyles` 64, each value at most 256 bytes; `MaxHints` 8, a file at
most 1,024 bytes and a component at most 256; `MaxChangedPaths` 200
and `MaxChangedPathsBytes` 16 KiB as encoded, the list cut at whichever
comes first (`verify.go`'s `bounded` rule: count and bytes), `changed`
still the whole count and `changed_cut` set; `MaxUserAgent` 512 bytes;
`MaxCaptureScope` 2 KiB; `MaxElementReport` 32 KiB as measured
(below). Every field the server fills has its bound too, so the page
part (below) is a sum of bounds, never of what a tree happens to hold.

The per-field bounds add up to more than `MaxElementReport` (about 78
KiB at every field's bound), so the bridge **cuts to fit, in a fixed
order, measuring after each step and stopping at the first that
fits**; every length a step cuts to is by the shared measure (the
field's encoded bytes, below), never by characters:

1. the neighbourhood, down to nothing;
2. the HTML, down to its opening tag;
3. the styles past the first 16 of the fixed list (the visibility, hit
   and box ones, which the state and the hit test lean on);
4. each attribute value to 64 bytes;
5. the HTML's opening tag to 512 bytes, then the HTML dropped;
6. the hints to the innermost 2, each file to its last 256 bytes and
   each component to 128;
7. the ancestors to the innermost 4;
8. the attributes to the first 8;
9. the tag, the text, the accessible name, the id, the role, the
   classes, the selector and the hit test's tag and selector each to
   256 bytes;
10. the styles to the first 8.

Steps 1 to 4 are not enough on their own: a 12 KiB opening tag beside
eight 1,024-byte hint files, twelve 512-byte ancestors and 32
attributes at 64-byte names and values passes 32 KiB once encoded,
every style removed. What step 10 leaves at worst is nine fields of 256
bytes, 8 styles of under 300, 8 attributes of 128, 4 ancestors of 512,
2 hints of under 450, the box, the state and the keys: under 12 KiB, so
the cut always ends within the bound, whatever each field held; the
bridge asserts it, and the server refuses a report past it. Each step
taken is marked in the report (`cut: [neighbourhood, html, styles,
attributes, tag, hints, ancestors, names, …]`). **One measure on both
sides**: the report's size is the
UTF-8 length of its JSON as Go writes it with HTML escaping off
(`json.Encoder.SetEscapeHTML(false)`, the server's `measure`), which the
bridge's `measure` (`report.ts`) computes the same way: `JSON.stringify`
of the report with lone surrogates first made U+FFFD (`toWellFormed`,
as Go's encoder does to invalid UTF-8) and U+2028 and U+2029 counted as
the six bytes Go writes for each. Two shared fixtures, written by the vitest and committed
(`internal/api/testdata/fullest-report.json`, a fake element at every
field's bound, and `largest-tag-report.json`, the shape above: a 12 KiB
opening tag, eight 1,024-byte hint files, twelve 512-byte ancestors, 32
attributes at their bounds, every string of six-byte characters), are
cut by the bridge to its measure, the second through step 10, and
accepted by the server at the same measure.

`MaxCaptureDocument` 2 MiB as encoded: 20 annotations each at most 32
KiB of report, a note of 4,000 characters at most 24,000 bytes encoded
(a character is at most 6 bytes as Go writes it), 8 sources of about
1.2 KiB and its rect, observation and keys within 1 KiB, about 66 KiB
each and 1.32 MiB for 20, beside a page part (URL, title, viewport,
origin with its changed paths at their 16 KiB, scope, image, drift,
serving)
within 32 KiB by the bounds above:
`TestTheFullestCaptureFitsItsDocument` builds one at every bound, notes
of six-byte characters and 200 changed paths of 4,000 bytes each
included, and creates, edits and sends it.
`MaxCaptureImage` 4 MiB, a PNG of at most 4,096 pixels a side, so a
capture's two files (6 MiB) fit DS2's 16 MiB of a package's
attachments beside others; `MaxCaptureSends` 50 per capture;
`MaxCaptures` 5,000 live in the store.
Every text is checked by DS1's field rules (`checkField`: no control
character but newline and tab in the long texts); HTML is text to the
store, shown as text everywhere and never rendered.

**Operations** (`captures.go`, new; each a typed operation with DS1's
`Op`, its receipt kept, a resend answered by `Replay` through the row's
`fingerprint` once the receipt is pruned; the fingerprint covers the
client's request as received, before the API scrubs it (below), never
what the server filled, as RV1's `ReviewSubmitArgs` does):

- `CreateCapture(ctx, op, NewCapture, CaptureFacts, image io.Reader)`:
  the image, when there is one, is written and hashed through
  `AddAttachment`'s upload path (temp file under `attachments/tmp`,
  synced, a lease committed, published under its hash, its name made
  durable) before anything names it; then one transaction writes the
  `captures` row, its annotations' versions and their first set, the
  `capture_files` row (holder `capture`) and the receipt, and releases
  the lease. `CaptureFacts` is what the API read
  (origin, commit, changed, mutable, candidate, where, the checked
  sources, the drift), stored with the rows and never fingerprinted.
- `EditCapture(ctx, op, id, expectedRev, []AnnotationEdit)`: the
  notes, the order, removals and regions drawn on the screenshot; an
  element annotation is named by its `k` and keeps its report,
  observation and sources as saved, and an edit that brings a new
  element report is refused `invalid` ("a new element is a new capture:
  Capture again"), so no later observation sits under the first one's
  screenshot, time and revision. An edit writes a new
  version of each annotation it changes (an unchanged one keeps its
  `sha256`, and one brought back to earlier content names that earlier
  version), a new set with an id of its own naming the versions in their
  order, and moves the capture's `annotations` pointer to it, in one
  transaction. Bumps
  `rev`; refused `stale` for a `rev` that is not the row's, `not_found`
  for a deleted capture, `bound` past `MaxAnnotationSets` (50 sets a
  capture: "Capture again").
- `DeleteCapture(ctx, op, id)`: tombstones (`deleted_at`) and, in the
  same transaction, removes what only the live capture held: its
  `capture_files` row of holder `capture` (the screenshot's pin) and its
  annotation versions and sets. So a capture deleted unsent leaves its
  tombstone and nothing the sweep must keep, and creating and deleting
  in turn never piles up files past `MaxCaptures`' worth. Its sends,
  their `capture_files` rows and the ticket attachments they made stay
  (those are the ticket's).
- `SendCapture(ctx, op, id, rev, Destination, SendFacts)`: refused
  `stale` unless `rev` is the capture's revision now (a person sends
  what they see). The document is built, measured and published as a
  content file through the upload path (lease first); one transaction
  then writes the send's input row (with its `request` for Brakeman,
  which leaves it pending), for a ticket its `sent` outcome with the
  target and the attachment ids, the `capture_files` rows the send holds
  (the document, and the screenshot when there is one), and
  for a ticket destination: a new ticket (`Destination{Project, Title}`,
  its description made by `ticketText`, within `MaxTicketDescription`,
  the drift's warning in it when the capture has one) or the existing
  one (`Destination{Key}`), the attachment rows naming the screenshot's
  and the document's hashes (no file copied: one content, one file),
  or, for a capture with no screenshot (`image_how` `none`: the person
  declined the tab capture, or no path was there), the document's row
  alone, the description and the comment saying "no screenshot: <why>",
  a comment naming the capture and its revision, and the ticket's `rev`
  bumped once with its change rows; refused `bound` when the ticket's 50
  attachments would be passed by the rows this send adds (two, or one
  without a screenshot). `MarkSent(op, id, target)`, `MarkRefused(op, id,
  why)` and `CancelSend(op, id)` append a send's outcome (each a typed
  operation with its receipt, a repeat answered by it), under the
  terminal rule below. `AskCancel(op, id)` records a person's cancel of
  a Brakeman send, its `capture_send_cancels` row and its receipt in one
  transaction, before anything acts on it (the API's Cancel, below):
  refused `stale` for a ticket send (made in one step, never pending)
  and for a `refused` one, answered by the state for one already
  `cancelled`.
- Reads: `Capture(id)` (with its annotations and sends), `Captures(by
  Run/Ticket/Recent, after, limit)` (a page, at most 100, newest first),
  `CaptureSends(kind, target)`, `PendingSends()`, `AskedCancels()` (the
  sends with a cancel, for the settlement to finish), `Document(id, rev)`
  (built from the set the pointer names, the only revision a send may
  name; an earlier revision is read from a send that froze it).
- **The change log.** Every change row a capture writes is within
  DS1's `MaxChangeValue` (64 KiB, refused by `ops.go`'s `record`) by
  construction, through that one writer: the create row holds the page
  part (within 32 KiB by the bounds); an annotation version's row holds
  its `sha256` and `bytes`, never its body (up to about 66 KiB, kept in
  its own row and named by its digest, as a plan revision's body is:
  `plans.go`'s `planCreated`); a set's row holds its `id`, `rev`,
  `digest` and `members` (under 1.5 KiB); the pointer's row its value
  and prior, `{rev, set}` each; a cancel's row its four fields; a send's input row its fields with the `request` (at
  most 8 KiB); an outcome's row its fields.
- `rules.go`: a capture's page, origin and image fields `immutable`;
  annotation versions and sets immutable rows, inserted and never
  changed, so two replicas' sets are both kept (keyed by their id, never
  by their content: a revert and two identical edits are each a revision
  of their own); a send's cancels immutable rows likewise; the
  capture's `annotations` pointer one `scalar` field under DS1's scalar
  rule as `rules.go` implements it (the later write's `at` wins; equal,
  the greater actor; equal, the greater `op_id`), so a merge picks one
  edit's set whole and never mixes two; no conflict is kept and none is
  promised. A send's input `immutable`; its outcomes a `log` (DS1's
  `ClassLog`, `KeepBoth`: every outcome kept), read through one
  projection, **`sendState`**, which every read uses: no outcome is
  `pending`; any `sent` outcome makes the send `sent`, with the earliest
  `sent`'s target and files (a destination that took the send took it,
  whatever else a replica recorded); else the earliest `refused` or
  `cancelled` by (`at`, actor, `op_id`), DS1's order. So the same
  outcomes give the same state in any order of arrival. Locally the
  **terminal rule** holds: an outcome is appended only to a pending
  send, with no exception: a cancel is an intent of its own, no outcome,
  and a send the desk admitted before the cancel's steps ran is still
  pending when they settle it `sent` (below). A delete is a tombstone. Each is a
  local schema decision CS1 may revise.
- `sweep.go`: `collectOrphans` keeps a hash any `capture_files` row
  names; a row is there only while its holder is (the live capture for
  holder `capture`, a send for its own), so a content file is kept
  exactly while a live capture, a send or an attachment names it, and a
  screenshot two captures share stays until both have let it go.
  `snapshot.go`: `SnapshotHashes` adds every `capture_files` hash, so
  C6's export carries the files and no released one. `OnChange` reports the entities
  `capture` and `capture_send`.

### `internal/crew` and `internal/crew/ledger`: the bridge's name, a candidate's preview checkout

- **The bridge's URL in a tree.** `Engine.SetPreviewBridge(url string)`
  (set once, from the server's listener:
  `http://127.0.0.1:<port>/preview-bridge.js`; `cli/runServe` tells the
  API its listen address once it listens, through a new
  `Server.SetListen(addr)` beside `SetReach`, and the API sets the
  engine's, as `server.go` sets its Runtime) and `tree.names`
  adds `CONDUCTOR_PREVIEW_BRIDGE` beside `CONDUCTOR_PREVIEW_URL` for a
  tree that has a preview. A tree without a preview, a server with no
  loopback listener, or a server whose embedded app has no bridge (a
  build without `make web-build`, `make dev`) gets none. The skill's
  text (`internal/agents/skill.go`) names it.
- **A secret of each tree** (PR-2): `tree.names` adds
  `CONDUCTOR_TREE_SECRET`, 32 bytes from `crypto/rand` as hex, minted
  when the tree is resolved and held only in the tree's memory, so two
  trees, and two opens of one candidate, never share one. It is given in
  the environment of the tree's commands and its session, never put into
  an argv by expansion (it is no placeholder of `expand`), so a step's
  receipt, which keeps the argv, never holds it; it is in no ledger row,
  event, task file, log line or `AttemptState`. A preview may name it
  literally, `${CONDUCTOR_TREE_SECRET}`, in its fragment (EN1's
  validation already lets any `${...}` that is not a placeholder pass as
  it is): it is stored, shown and given as `CONDUCTOR_PREVIEW_URL` with
  the name, and only the label route puts the value in, for the admin
  (below). It lets a tree's throwaway server have a credential no one
  outside the tree and the operator can know.
- **A tree's owner** (`steps.go`, PR-7). On `main`, `resolveTree`,
  `prepareTree`, `treeReady` and `endTree` take the attempt: they refuse
  a closed one (`errAttemptClosed` when `a.closedAt` is set), list the
  tree in `a.trees`, lease its ports to it until the attempt ends, write
  its row with `PutTree` into `trees` (key `run_id, task_id, n, kind`),
  and the service-exit handling clears only an attempt's worktree
  preview. A candidate's attempt is closed, and `endAttempt` has cleared
  its preview, so those functions refuse exactly this item's case. PR-7
  puts a small interface between them and the attempt, `treeOwner`:
  `closed()` (read under the run's lock, in the transaction that leases
  the ports, as `a.closedAt` is today), `own(t)` and `disown(t)` (the
  tree's place in its owner's list, its port lease held by that list),
  `putRow(ltx, row)` (the owner's ledger writer), and `serviceLost(t,
  why)` (what a service's exit after readiness does). The open attempt
  is one owner and behaves as today, every existing test of `steps.go`
  passing unchanged; a preview (`*preview`, `preview.go`) is the other:
  closed once its end is committed, listed in the engine's previews
  (never `a.trees`) and counted on its run (`r.previews`, beside
  `r.trees.n`, from the open's first transaction until its end's last,
  its teardown done), which the engine's `idle` reads (`run.go`), so
  `evict` never forgets a run whose preview is opening, serving or
  ending and `forget` never cancels the `r.teardown` its services run
  under (at most 2 previews a server, so at most two runs are held past
  their class's cap); its rows written to `previews` by `seq`
  (`Tx.OpenPreview`, `Tx.PreviewReady`, `Tx.EndPreview`, never
  `PutTree`, so the result checkout's `trees` row is never touched), its
  ports leased to its tree and released once its end is committed, and a
  lost service ending it (`why: service_exited`, its URL cleared in the
  ending's first transaction). `runStep`, `startService` and `runProbe`
  take the tree and the run as now; the receipts they write stay the
  attempt's (`checks`, its phase, the checkout's `dir`).
- **A candidate's preview checkout** (`preview.go`, new): `Engine.
  OpenPreview(ctx, run, task, n, by)` for an attempt, closed or open,
  that submitted a result whose commit is known, in a run this server's
  ledger owns and that is not continued elsewhere (`409 run_continued`
  as the task commands answer), whether or not the engine still holds
  the run (below): the run's project manifest is resolved
  for that commit (`resolveTree` with the preview as its owner, a `file`
  manifest read at the commit as EN1 reads it); the checkout is
  `<worktree>.preview`, added detached at the
  commit (`gitcli.AddDetachedWorktree`), or, when it is there already,
  reused only if `checkoutIntact` finds it at the commit with no tracked
  change (untracked files, a dev server's caches, are left as they are)
  and refused otherwise with the path and the changed files named:
  Conductor removes nothing it made (EN1's R7). Then EN1's
  `prepareTree` and `treeReady` run on it with the preview as owner:
  setup, services, readiness and preview, every step a receipt under the
  attempt (the `checks` table, its phase, `dir` the checkout's, the next
  `k`), readiness written to the `previews` row of its `seq`;
  `ClosePreview(run, task, n, seq)` and the ends below run EN1's
  `endTree` with the same owner: services stopped, cleanup run, ports
  released after the end's commit. Bounds: at
  most 2 preview checkouts open per server (`maxPreviews`, `429
  previews_busy` naming the open ones); one per candidate at a time; 20
  opens per candidate in the ledger. Ends: Close; 30 minutes with no read
  of its label (`previewIdle`, the pane reads it every 5 s); the run's
  stop; the server's shutdown, as every tree's. A checkout's services
  that the last server left running are reported at the next start,
  never signalled (EN1's rule).
- **A run the engine no longer holds** (PR-7). `Engine.Get` and
  `TaskOf` read `e.runs` alone, and the API reads an evicted run, or one
  finished under an earlier server of this ledger, from the ledger
  (`taskFromLedger`); but `resolveTree`, the Runtime (`execOf`), the
  teardown and `nextK` all take a `*run` and an `*attempt`. So
  `OpenPreview` for a run not in `e.runs` first builds a **preview-only
  context** from the durable records (`loadForPreview`): `ledger.Load`
  and `LoadImports` as `taskFromLedger` and `ContinueRun` read them; the
  crew's JSON for the run's manifest source, prefix and cwd, parsed as
  `continue.go` parses it; the attempt with its result's commit; its
  receipt numbers seeded from its `checks` rows (each id's largest `k`,
  so `nextK` goes on after them and no receipt overwrites another); the
  Runtime chosen by `runtimeFor(true, run)`, as a run's is, and recorded
  in the `previews` row's `where_ran`; and a teardown context of its
  own, cancelled only by its previews' ends and the server's shutdown.
  It schedules nothing: it is never put in `e.runs`, so `Get`, `TaskOf`,
  the scheduler, `evict`, `forget` and `ContinueRun` never see it, and
  no attempt, check or session starts in it; it is kept with its
  previews and dropped once its last one's end has committed. A run in
  `e.runs` is used as it is (above). The label, close and idle paths
  find a preview by `(run, task, n, seq)` in the engine's previews,
  whichever context holds its run, and `taskFromLedger`'s attempts are
  given their `Previewing` from there.
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
- **A test of the repository's own manifest** (PR-9):
  `TestTheRepositorysManifestIsValid` reads `conductor.manifest.json` at
  the module's root through the manifest's own parse and validation,
  expands it (`expandManifest`) with drawn ports and a state directory,
  and checks its preview against the loopback rule, its probes'
  timeouts, and that `${CONDUCTOR_TREE_SECRET}` appears in its preview
  and in no argv.

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
  The bridge answers only messages from those origins. When the
  embedded app has no `preview-bridge.js` (a server built without `make
  web-build`, or `make dev`'s, whose UI the Nuxt dev server serves), the
  route answers `404 no_bridge` ("this server was built without the web
  app's preview bridge: run make web-build") and the engine is given no
  bridge URL, so no tree is named one
  (`TestNoBridgeInTheEmbedIsA404AndNoName`).
- **The label**: `GET /api/runs/{run}/tasks/{task}/attempts/{n}/preview`
  (`readable`, as the attempt's steps are) → `{url, tree, seq?, commit,
  content, changed, changedPaths, mutable, state, candidate?, where,
  readAt}`: the attempt's worktree preview while EN1 offers it, or its
  open preview checkout's (`?seq=`). `url` is the preview with
  `${CONDUCTOR_TREE_SECRET}` replaced by the tree's secret for an admin
  caller, and with the fragment dropped for any other; the run page's
  New tab and the pane both open the admin's `url`. That is the only
  place the value leaves the server, and nothing durable keeps it:
  every capture write scrubs it (below). The revision is read
  now by `crew.TreeRevision(ctx, dir, index)` (`verify.go`, new, beside
  `checkoutIntact` and sharing its private-index reading, through
  `gitcli` with argv and the C locale, at most 5 s): `rev-parse HEAD`;
  then, in an index of the server's own for that tree (a file under the
  server's temporary directory, kept for the open preview's life so
  git's stat cache keeps a read near `git status`'s cost, and carrying
  no flag a command in the tree could set to hide a change: no
  `assume-unchanged`, no `skip-worktree`, `core.fileMode` as
  `checkoutIntact` sets it), `read-tree -m HEAD` at every read (a
  one-tree merge: the index becomes HEAD's entries, each keeping its
  cached stat data only where the file still matches HEAD, so nothing
  an earlier read staged survives into this one: an untracked file a
  later ignore rule covers leaves the label, as it leaves `git status`;
  only the changed and new files are hashed again), `add -A`
  (ignored paths aside), `write-tree`: `content` is that tree id, a
  digest of everything the tree holds that git would see, uncommitted
  edits and new files included; `changed` and `changedPaths` are
  `diff --cached --name-only -z HEAD` in that index: all of them
  counted, the list cut by `MaxChangedPaths` and `MaxChangedPathsBytes`
  (`changedCut` when it is). `mutable` is true for a worktree and for a checkout whose
  `content` is not its commit's tree. A read past 5 s answers the label
  with `content` empty and "the tree was not read in time". `404
  no_preview` when neither is open. A read of a checkout's label resets
  its idle clock. The pane reads the label at each selection too, so the
  `content` it sends with a capture is what the tree held when the
  element was selected. That is the tree on disk, never proof of what
  the frame renders (a dev server can serve an older build after the
  files moved, its rebuild slow or failed): the page's own word is kept
  apart, as `serving` (below).
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
| `POST /api/captures` | `multipart/form-data`: `capture` (JSON, at most 2 MiB: `{opId, from: {run, task, attempt, seq?} \| {url}, url, title, viewport, epoch, selectedAt, label (the pane's label read at the selection: commit, content, changed, readAt), annotations (each with its `observed`), via, imageWhy?, retakeOf?}`), `image` (optional, `image/png`, at most 4 MiB); the body at most 6 MiB + 64 KiB → `201 {receipt, capture}`; `200` the same for a repeat; `400 invalid` (a field past its bound, a URL whose origin is not the preview's, an annotation observed at another moment than the capture's, an image that is not a whole PNG or is past 4,096 pixels a side: `png.DecodeConfig` first, the header and the size before any pixel is allocated, then `png.Decode` of the whole bounded input, every chunk's CRC and every row of pixel data, so a valid header over cut or corrupt data is refused, one decode at a time per server so at most one 4,096-pixel image, 128 MiB at 16 bits a channel, is in memory; a `retakeOf` that is not an admissible capture, below); `404 no_preview` (the attempt's preview, or the checkout named, is not open now); `413 bound`; `409 expired` (the image's lease swept; the client sends again) |
| `GET /api/captures` | `?run=&task=&attempt=&ticket=&after=&limit=` (1 to 100) → `{captures, next?}`, newest first, each a summary (its first note cut to 200 characters, its label, its sends' targets) |
| `GET /api/captures/{id}` | the capture whole with its sends' summaries (at most 2.2 MiB) |
| `GET /api/captures/{id}/image` | the PNG, `Content-Type: image/png`, `nosniff`, `Content-Security-Policy: sandbox`, `X-Conductor-SHA256`; `404` without one |
| `GET /api/captures/{id}/sends/{send}` | the send with its frozen document, read from the content file its `digest` names; served for a deleted capture too (the tombstone hides the live capture, never what was sent) |
| `POST /api/captures/{id}/edit` | `{opId, expectedRev, annotations}` (2 MiB; an element annotation named by its `k`, no new report) → `{receipt, capture}`; `409 stale`; `400 invalid` for a new element report |
| `POST /api/captures/{id}/delete` | `{opId}` → `{receipt}` |
| `POST /api/captures/{id}/send` | `{opId, rev, to: {kind: "ticket", project?, title?, key?} \| {kind: "brakeman", question}}` (8 KiB) → `201 {receipt, send}`; `409 stale`, `404 not_found` (a ticket), `409 bound`; for Brakeman, every answer the desk gives, as it gives it: `409 brakeman_off`, `429 queue_full`, `429 budget_spent`, `503 brakeman_stuck`, `503 brakeman_not_owner` (the desk not yet recovered); `202 {receipt, send}` with the send `pending` for the last two, settled later (below) |
| `POST /api/captures/{id}/sends/{send}/cancel` | `{opId}` → `{receipt, send}` (the send with its state by `sendState` and its cancel's, `cancel: {asked, done}`); a repeat answered by its receipt; `409 stale` for a ticket send (made in one step: nothing is pending) or a `refused` one (its state named); a `sent` Brakeman send has its request cancelled at Brakeman, a `cancelled` one is answered as it is; the intent committed before any step, its steps serialized with the send's settlement and finished by it after a stop (below) |

  **What the server fills, never the client.** On a create, the API
  resolves `from`: for an attempt, the run's state now (the ledger and
  the engine), its preview URL (`AttemptState.Preview` or the open
  checkout's), and refuses a page `url` whose origin is not that
  preview's (`400 invalid`, "the page is http://127.0.0.1:5173, not this
  attempt's preview http://127.0.0.1:43117"); it reads the label as the
  label route does and records it; it records `drift` when the client's
  `label` (what the pane read at the selection) names another commit or
  another `content` (so a second edit of a file already changed, or
  another set of changed paths of the same count, is drift), or when
  either read has no `content` ("the revision at the selection is not
  known"); it makes each hint's file relative to the tree's directory
  (`filepath.Rel` after `filepath.Clean`, a hint outside the tree kept
  with `outside: true` and its path as reported, at most 1,024 bytes)
  and checks it exists through an `os.Root` opened on the tree's
  directory (`found`), the way `taskfile.go` writes through one, and
  classes it (`tracked` by `ls-files --error-unmatch` at the tree's
  commit, else `ignored` by `check-ignore`, else `untracked`; `vendor`
  for a path with a `node_modules` element) and orders the sources as
  the document says; for a `url` capture, none of this, and `origin`
  says `url`. **What the page serves** is the page's word, never the
  disk's: the bridge reports the revision a page declares it loaded
  (`<meta name="conductor-revision" content="<commit>[+<content>]">`,
  read at each answer, at most 128 bytes, hex with one optional `+`),
  and the API compares it with its own read at the save: the tree's
  commit, and its `content` when the tree has changes, equal:
  `serving.state` `declared`; another: `stale` ("the page reports
  a1b2c3d; the tree holds b2c3d4e"), in the capture, its document and
  the ticket as the drift is; none, or a commit alone over a tree with
  changes: `unconfirmed` ("what the page shows is not confirmed against
  the tree"). Conductor's own dev build declares none (Vite's hot
  updates change what it shows without a reload, so no value fixed at
  its start would stay true), so its captures say `unconfirmed`; a
  candidate is still named by `checkoutIntact`, the tree's fact, and the
  verification capture carries both. The candidate is set only for a preview checkout whose
  `checkoutIntact` (the engine's own comparison, a fresh private index
  that no `assume-unchanged` or `skip-worktree` flag can fool) finds
  HEAD at the candidate's commit and no tracked path changed, at the
  save; the label's `mutable` is for showing, the save's
  `checkoutIntact` decides. **A retake** names an admissible capture: a
  live one of the same repository (its recorded `scope.repo` is the new
  capture's run's), so a ticket's repair, which DS2's launch starts as a
  run of its own (`mintRunID` in `launches.go`) with its plan's task
  ids, retakes the capture that made the ticket; a `url` capture's
  retake is a `url` capture of the same page origin, and the two kinds
  never retake each other. The server records how the two are related,
  `retake_via`: `run` (the same run and task), `ticket` (the new
  capture's run was launched from a ticket that a `sent` send of the
  first one targets: DS2's `launches` row names the ticket), else
  `repo`. A deleted capture, another repository's, or a `url` capture of
  another origin is refused.
- **One capture is one moment** (the server's check): every element
  annotation's `observed` carries the capture's own URL, viewport with
  its scroll, `epoch`, declared `revision` and label (`commit`,
  `content`), the moment its
  screenshot shows; an annotation observed at another is refused `400
  invalid` ("annotation 3 was selected after the page changed: it is
  another capture"), whatever the client drew.
- **The tree's secret is scrubbed** before anything is stored: on a
  create, an edit and a send, the API replaces every occurrence of the
  capture's tree's `CONDUCTOR_TREE_SECRET` value with the literal
  `${CONDUCTOR_TREE_SECRET}` in every string the request carries (the
  page's URL, which the no-bridge fallback takes from the frame's `src`;
  the URL a page that keeps its fragment reports; each annotation's
  `observed`; every string of each element report, its attributes and
  HTML included; the notes; a Brakeman question), and only then checks
  and stores it. **The fingerprint is taken before the scrub**, of the
  request as received: the API hashes the decoded body re-encoded
  (`json.Marshal` of what was parsed, so key order and spacing do not
  count) with the image's SHA-256, and that digest, never the body, is
  the operation's content for `Replay` and the store's `fingerprint` (a
  32-byte random value inside a SHA-256 digest cannot be read back from
  it). **Every capture route replays first**, before it resolves the
  preview or reads the tree, as `dispatch.Replay` is meant to be called
  (a launch is replayed before it starts anything): a create, an edit or
  a send whose reply was lost is answered by its receipt after the tree
  has ended and its secret is gone, a different body under the same
  `opId` is `op_conflict` as ever, and only a new operation goes on to
  `404 no_preview`, the scrub and the checks. The document, the ticket's
  description and comment and a Brakeman request are made from those
  rows, so none holds the value. The value is hex (its URL-encoded form
  is itself), held in the tree's memory only while the tree is open, and
  a capture is created only while it is (`404 no_preview`); an edit or a
  send after the tree has ended has no value to find, and the credential
  ended with the tree's throwaway server. So no row, content file,
  event, log line, document, ticket or request holds a tree's secret.
- **Ask Brakeman** (`captureSendBrakeman`): the send's input row first,
  with its `request` (the person's question and the subjects, exactly as
  the desk will be asked), which leaves it `pending`; then a request of
  kind `ask` through the desk's own admission (`handleBrakemanAsk`'s
  path, factored as `askBrakeman(in)`, with the send's id as its `opId`,
  so a resend after a lost reply finds the same request through the
  desk's `ByOp`, and **pinned**, below), subjects `[capture:<id>/<send>]`
  (the frozen document this send holds, never the live capture) and the
  attempt's `attempt:` ref when there is one, the note the person's
  question; then `MarkSent(br-<n>)`, then the pin released. The desk's
  answers: admitted, `sent`; `brakeman_off`, `queue_full`,
  `budget_spent` to the person sending, `refused` with the desk's words
  (they see it and send again); `brakeman_not_owner` and
  `brakeman_stuck`, which pass by themselves, leave the send `pending`.
  `settleCaptureSends` asks again for each pending send from its stored
  `request`, never rebuilt: once the desk has recovered
  (`d.recovered`), then every 60 s while one is pending; there, every
  answer but an admission (`sent`) or `brakeman_off` (`refused`) keeps it
  `pending`, the editor showing "waiting for Brakeman: <the desk's
  words>" (the last try's answer, kept in the server's memory beside the
  send and never a row: nothing to merge, read again at the next try)
  with Cancel.
- **One step per send at a time.** Every step that can reach the desk
  for a send (the send route's first ask, a settlement's try, a
  cancel's steps) runs under that send's gate (`sendGates`, a lock per
  send id in the API, taken before the send's state is read and released
  after its outcome is committed) and reads `sendState` and the send's
  cancels again under it: a try that finds the send no longer pending,
  or a cancel recorded, asks nothing.
- **A cancel is kept before it acts.** **Cancel** (`POST
  …/sends/{send}/cancel`, the person's, idempotent by its `opId`) first
  commits the person's intent, `AskCancel` (its `capture_send_cancels`
  row and receipt, outside the gate, so a try holding the gate at the
  desk does not delay it), and only then acts, under the gate, through
  one function the settlement shares (`finishCancel`): a pending send
  whose op the desk has never seen (`ByOp(send id)`: none) gets
  `cancelled`; a pending one the desk admitted (a try whose `sent` was
  never committed, the server having stopped between the two) is
  settled `sent` with it ("admitted before the cancel") and its request
  cancelled; a `sent` one (a try
  that held the gate while the cancel waited) has its request
  cancelled. The request is cancelled through Brakeman's own cancel
  (`handleBrakemanCancel`'s path, factored as `cancelBrakeman`, its
  `opId` derived from the cancel's), which closes an open request and
  answers a closed one as it is. A cancel is **done** once its send is
  `cancelled` or its request is closed at the desk or pruned, read from
  those facts and never kept; until then `settleCaptureSends` finishes
  it (`AskedCancels`), once the desk has recovered and every 60 s, so a
  stop after the intent, or between `MarkSent` and the desk's cancel,
  is finished at the next start. The reply names the send's state and
  the cancel's (`cancel: {asked, done}`, and "the request had finished
  before the cancel reached it" when Brakeman answered first). So the
  person's Cancel ends the question whatever the gate's order and
  wherever the server stopped, and no request is left running behind it
  unknown.
- **The desk keeps the op while the send waits.** Brakeman's store
  keeps the newest `MaxOps` (1,000) op ids (`state.json`'s `Ops`,
  trimmed in `store.go`), and a send can stay pending past a thousand
  other requests. So a capture send's request is created **pinned**
  (`Request.Pin`, PR-6, in `internal/brakeman`): a pinned op ref is never
  trimmed until `Release(opID)`, at most `MaxPinnedOps` (1,000) of them
  (past it the desk answers `pins_full`, which keeps the send pending
  with those words), and `Prune` of a pinned op's request marks it
  pruned as today, the ref kept with the request's number. A settlement
  that finds the op's request settles the send `sent`; one that finds it
  pruned (`ByOp` answers a pinned pruned ref with its number and
  `ErrPruned`) settles it `sent` to that `br-<n>` ("since pruned at
  Brakeman") and never asks or retries again; only an op the desk has
  never seen is asked for. The pin is released after the `sent` commit;
  a release lost to a stop is made by the next settlement pass, which
  releases every pinned capture op whose send is terminal or gone
  (`Store.Pinned`). So the desk's idempotency lasts exactly as long as
  the send is pending.
- **Brakeman reads a capture.** `brakeman_refs.go`'s resolver resolves
  `capture:<id>` (the live capture: its revision and digest; `deleted`
  for a tombstone) and `capture:<id>/<send>` (that send's frozen
  document: its revision and digest, the same after an edit or a
  delete), and `brakeman_observe.go`'s read table gains `GET
  /api/captures/{id}` and `GET /api/captures/{id}/sends/{send}` under
  `brakeman.ClassDispatch`, each reply without the image's bytes (a turn
  reads text: the image's size and hash are named). **Admission** is the
  class and the scope, as for every ref (`refGrant`, the turn's snapshot
  intersected with the configuration now, read again at each read, so a
  narrowing takes effect at once): a capture's scope item is its
  origin's, never its sends' (the run, with its crew and repository,
  and the project of the ticket the run was launched from when there is
  one); a send's frozen document is admitted as its capture; a `url`
  capture has no run, repository or project, so only a grant that scopes
  none of those admits it; a capture whose run has gone is admitted by
  the run, crew, repository and project it recorded. A capture sent to
  tickets of several projects is not widened by them. `guide.go` gains
  the operations `capture_read` and `capture_send_read`, so the MCP
  server's tools and the guide's sections carry them.
- `events.go`: the `dispatch` event's entities `capture` and
  `capture_send`, inside `maxDispatchEach`'s arithmetic as RV1's are.
- `docs/protocol.md`: the HTTP rows above, the capture document, the
  bridge's messages (below), the two entities.

### `internal/brakeman`: the `capture` ref

`RefCapture = "capture"`, `capture:<id>` (16 hex digits: the live
capture) and `capture:<id>/<send>` (two of 16: that send's frozen
document), in `refForms` with their words and examples; `Ref.Capture`
holds the id and `Ref.Send` the send's; `ParseRef` and `String`
round-trip both (`TestACaptureRefParsesAndPrints`). The store gains
the pin (above): `Request.Pin`, `Store.Release(opID)`,
`Store.Pinned()`, `MaxPinnedOps`, the trim to `MaxOps` passing over
pinned refs, and `ByOp` answering a pinned pruned ref with its number.
Kinds whose
refs take any kind (an `ask`'s subjects, a `fact`'s evidence, a draft
ticket's `from`) take it with no other change.

### `web/app`: the bridge, the pane, the editor, the sends

- **The bridge** (`utils/previewBridge.entry.ts`, and its pure parts in
  `utils/previewBridge/report.ts`, `select.ts`, `hints.ts`, `gate.ts`):
  built as one classic script (`iife`) by Vite's library build. Nothing
  on `main` emits a fixed-path classic script into the built app (the
  Mermaid renderer is one only under the dev server; built, it is a
  worker-format asset with a hashed name), so PR-2 adds the step: a
  Nuxt build hook in `web/nuxt.config.ts` (`nitro:build:public-assets`,
  which `nuxt generate` runs) calls Vite's `build` with the library
  config (`lib.formats: ['iife']`, `fileName: () =>
  'preview-bridge.js'`, `emptyOutDir: false`) into Nitro's public output
  directory, so `make web-build` copies `preview-bridge.js` into
  `internal/web/dist` with the rest; the built script holds one marker,
  `"__CONDUCTOR_WORKBENCH_ORIGINS__"`, that the API replaces with the
  origins. `make web-build` ends by checking that
  `internal/web/dist/preview-bridge.js` exists and holds the marker once
  (a `Makefile` line that fails the build otherwise), and
  `TestTheBridgeIsServedWithItsHeaders` reads it from a fake embed. The
  dev server serves the same entry through the existing
  `vite:serverCreated` hook, as the Mermaid renderer's. It does nothing
  until a workbench origin asks. Selection mode draws its outline in a closed shadow root it adds
  to the page (no style of the page's touches it, none of its leaks
  out), takes pointer events in the capture phase while selecting and
  gives them back after; it reads, never writes, the page. It keeps a **page epoch**: a
  counter it bumps, at most once per animation frame, on a resize or a
  scroll of the frame's window and on any mutation a `MutationObserver`
  on the document reports (its own shadow host left out), and on every
  animation and transition event (`animationstart`, `animationiteration`,
  `animationend`, `animationcancel`, `transitionrun`, `transitionend`,
  `transitioncancel`, heard on the document in the capture phase); it says
  only that the page moved, never how. A page that redraws all the time
  moves its epoch between any two selections, so each of its elements is
  a capture of its own: one screenshot shows one moment. The epoch still
  cannot see all that changes what is drawn (a hover restyles an element
  with no event of either kind, and an animation moves between its
  events), so where there are pixels the pixels decide (the moment's
  check, below). Up and Down
  in selection mode step through `document.elementsFromPoint` at the
  pointer (its own shadow host left out), outlining each, so an element
  under an overlay, or under any element above it, is selected; the
  report's hit test is still `elementFromPoint` at the selected box's
  centre, so it names the overlay. An element no hit test returns
  (`pointer-events: none`, an `inert` subtree: CSSOM's
  `elementsFromPoint` lists hit-test targets only) is reached through the
  DOM instead: Left and Right walk it from the outlined element (its
  parent, its first element child) and Shift+Up and Shift+Down its
  previous and next element sibling, its own shadow host left out; the
  pane's Select by selector sends a `describe`, whose `described` starts
  or joins a draft as a `selected` does. The report's hit test is still
  the real one, so it names what a click there reaches. A `describe` by
  selector (Capture again) reaches a covered element the same way. The
  bridge hides its outline while it answers a selection, so no frame
  holds it. The report:
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
- **The messages** (`conductor-preview/1`, defined in
  `internal/proto/preview.go` and `web/app/utils/protocol.ts` and
  documented in `docs/protocol.md`, the three together): from the
  workbench, `hello {nonce}`, `select {nonce}`, `describe {nonce,
  selector}`, `look {nonce}`, `cancel {nonce}`; from the bridge, `ready
  {nonce, version, url, title, viewport, epoch, revision, hints: [how]}`,
  `selected {nonce, element, url, title, viewport, epoch, revision}`,
  `described {nonce, element | missing, url, title, viewport, epoch,
  revision}`, `looked {nonce, url, title, viewport, epoch, revision}`,
  `cancelled {nonce}`; a viewport carries the frame's scroll offsets, and
  `revision` is the page's declared one or empty (at most 128 bytes).
  Every answer carries the page's URL, title, viewport, epoch and
  declared revision read at that moment, never the
  handshake's: a client-side navigation does not reload the frame and
  the workbench cannot read the frame's URL, and a device mode changed
  after the handshake changes the viewport. The workbench sends `look`
  right after it takes a screenshot: when the epoch it answers is not
  the selection's, the page moved while the pixels were taken, and the
  screenshot is taken again, at most twice, then the draft is refused
  with words ("this page does not hold still long enough to capture").
  **Where there are pixels, the pixels decide.** With the screenshot the
  workbench takes a second frame and compares the two, at the frame's
  own scale before any encoding, inside the selected element's box; at
  every later selection it takes a frame again and compares it with the
  draft's screenshot inside the new element's box and every
  annotation's: all the same, the element joins and the frame is
  dropped; any box differing, the element starts a new draft whose
  screenshot is that frame. So a hover that restyled an earlier element,
  or an animation inside a box, never puts a report under pixels of
  another moment. An element whose box differed between the screenshot
  and the frame after it, or whose report counts animations playing
  (`motion`), is kept with `observed.stable` `false` and the words "this
  element was moving: its report is of a moment near the screenshot, not
  of it"; one whose box held is `true`. Without a screenshot (no source,
  or the person declined) no visual stability is claimed: `stable` is
  absent and the capture says so.
  Each is a
  plain object with `conductor: "preview/1"`. The bridge's gate
  (`gate.ts`) accepts a message only when its `source` is
  `window.parent`, its `origin` is one it was given, it is at most 4 KiB
  as measured (the largest, `describe`, holds a selector of at most
  1,024 bytes), it parses by its schema and, after the `hello`, it
  carries the nonce that `hello` gave; anything else is dropped without
  an answer. It posts only to `window.parent` with that origin as the
  target, never `*`. The workbench accepts one only from the
  frame's `contentWindow` at the preview's origin, with the nonce it
  sent, and drops one past 64 KiB as encoded or one that does not parse
  by its schema (`utils/capture.ts`'s `parseBridgeMessage`); a report in
  a message past the 32 KiB report bound by the shared measure is
  dropped too. One request
  is in flight at a time; a `hello` with no `ready` in 3 s means no
  bridge ("this page does not load Conductor's bridge: Capture takes the
  screenshot and your notes only. In the desktop app every page has
  it"). Both languages' parsers are held to one fixture,
  `internal/proto/testdata/preview-messages.json` (every message at its
  bound and one byte past, an unknown type, a missing or wrong nonce, a
  wrong `conductor` mark), which `TestPreviewMessagesFixture` reads
  through `proto.ParsePreviewMessage` and `protocol.test.ts` through
  `parsePreviewMessage`, each asserting the verdict the fixture records,
  so the Go and the TypeScript definitions cannot drift.
- **The pane** (`components/PreviewPane.vue`, new), hosted by the
  preview page (`pages/preview.vue`, new: `?run=&task=&attempt=` and
  `&seq=`), which reads the run and the label alone and never the
  attempt's session, so a retained candidate whose session the
  registry's GC removed opens as any other; the session page's
  `EditorArea.vue` keeps its `url` tab, which gains Capture: the frame (the existing sandbox
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
  the frame, each frame drawn to a canvas, the track kept for the draft
  and stopped at Save, Escape or the pane's close; else none. `frame()`
  gives the raw bitmap the moment's comparisons use; the screenshot is
  scaled and encoded from it.
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
  `diffSinceSent`: a region added, an annotation removed or moved, a note
  changed). After Save, the editor offers the notes, the order, removal
  and Draw a region; Select another element and a re-selection offer
  Capture again instead. `composables/useCaptures.ts` holds the routes,
  minting `opId`s and retrying a lost reply once with the same id, as
  `utils/tasks.ts` does.
- **Sends and the ticket page**: `components/CaptureSend.vue` (the
  destinations; Create a ticket's project and title; Attach to a
  ticket's picker, `TicketPicker.vue`, with the run's own ticket first;
  Ask Brakeman's question), `components/DispatchCaptures.vue` on
  `DispatchTicketPage.vue` (the captures sent to the ticket, and only
  those: a capture, a retake too, reaches a ticket by a send; each with
  its label, screenshot, annotations as sent, sends and Open), and
  `DispatchPlanPropose.vue` preselecting attachments named `capture-*`
  when the revision is not based on another.
- **Run page and candidates**: `RunTasks.vue`, the palette's task rows
  and `DispatchCandidates.vue` gain Open in Conductor (the preview page)
  and Preview this candidate (while `previewing` is open: Open, Close);
  the run page's New tab opens the label's admin `url`, so a preview
  that names the tree's secret opens with it.
- **Conductor's own app** (PR-9): `plugins/previewBridge.client.ts`
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

### `conductor.manifest.json` (PR-9, the repository's root)

The project manifest that serves Conductor itself in a tree:
prerequisites `go version` and `node --version`; `env` none; ports
`api` and `web`; setup `npm --prefix web ci`; services `env
CONDUCTOR_DATA_DIR=${CONDUCTOR_STATE_DIR}/data CONDUCTOR_RENDEZVOUS=0
go run ./cmd/conductor serve --dev --token-env CONDUCTOR_TREE_SECRET
--listen 127.0.0.1:${CONDUCTOR_PORT_API}` and `env
CONDUCTOR_DEV_API=127.0.0.1:${CONDUCTOR_PORT_API} npm --prefix web run
dev -- --port ${CONDUCTOR_PORT_WEB} --host 127.0.0.1` (argv arrays, no
shell); ready probes on both ports, each with `timeoutSeconds` 900 and
`intervalSeconds` 5: on a fresh tree `go run` compiles the whole server
and Nuxt's dev server builds on its first request, either of which can
pass EN1's default of 60 s on a loaded machine, and 900 s only bounds a
hang (EN1's limit is 3,600); preview
`http://127.0.0.1:${CONDUCTOR_PORT_WEB}/#token=${CONDUCTOR_TREE_SECRET}`.
No token is in the file: the inner server takes its workbench token
from the variable the flag names (`--token-env NAME`, new in PR-9,
`internal/cli`: the token is that variable's value, refused when it is
empty or unset, never printed), which is the tree's secret, minted for
that tree alone and given only in its environment; the pane and New tab
get the preview with the secret from the label's admin `url`. Another
page on the machine's loopback (another tree's preview, a project's dev
server), which `--dev`'s CORS for loopback origins lets reach the inner
server, cannot know it. The inner server runs on the loopback, with its
own data directory under the tree's state directory, and publishes
nothing (`CONDUCTOR_RENDEZVOUS=0`).

### After RV1 and DR1 (PR-10, PR-11)

- **A capture finding within RV1's bounds.** RV1 keeps a finding's
  message to 1,000 characters (the engine's `maxFindingText`), while an
  annotation holds a 4,000-character note and a 32 KiB report. So the
  finding RV1 stores and puts in a repair's `task.md` is a projection
  the server makes: the note's first 600 characters (cut on a word, with
  "…"), the element's summary (tag, accessible name, selector), the hit
  test when something covers it, and the first tracked source as the
  finding's path and line, within 1,000 characters; the whole of it
  travels as **evidence files** pinned to RV1's change request: the
  frozen document and the screenshot of the send (`capture-<id>-r<rev>
  .json` and `.png`), named in the request by reference, never carried
  as bytes. RV1 keeps a `ChangeRequest` inside the task's repair JSON
  and carries it through continuations, and the ledger holds that JSON
  to 64 KiB (`ledger/transition.go`'s `MaxPayload`), so the request
  gains `Evidence []EvidenceRef{Name, SHA256, Bytes, Send}` (the file's
  name, its hash and size, and the capture send whose `capture_files`
  rows pin it while the send exists, which is always: a send is never
  deleted), at most 8 refs (under 2 KiB, counted by `FitChangeRequest`)
  and 16 MiB of files in all per repair. The engine reads the bytes only
  when it writes an attempt's files, through a resolver the API sets as
  it sets the launch package's (`Engine.UseEvidenceFiles(open func(ctx,
  send, sha256 string) (io.ReadCloser, error))` beside
  `UsePackageFiles`, given Dispatch's `OpenSendFile`, which opens a
  content file only when that send's `capture_files` row names it;
  `internal/crew` never imports `internal/dispatch`), and copies each
  through the attempt's `os.Root` into `.conductor/evidence/`, checking
  its size and hash as it copies (a mismatch fails the write and tells
  the attempt, as a package attachment's does); `task.md` names them
  under each finding ("the capture:
  .conductor/evidence/capture-…-r2.json"). So a repair queued, retried,
  or continued after a restart (`continue.go`) reads the same files from
  the store, never from the server's memory. That is an extension of
  RV1's repair contract in `internal/crew` (the change request's refs,
  the resolver, `taskfile.go`) and is in PR-10's touches;
  `TestAMaximalCaptureFindingReachesTheRepair` sends a capture whose
  note and report are at their bounds through a real repair launch and
  reads the finding and both files in the attempt's tree, and
  `TestCaptureEvidenceSurvivesARestartAndAContinuation` queues a repair
  with its evidence, restarts the server before the attempt starts and
  continues the run: the attempt's tree holds both files at their
  hashes.
- **A decision send** follows the ordering above (the row and its text
  before DR1's call, settled from DR1's receipt for the send's `opId`).

### Docs

`docs/protocol.md` (each PR its rows), `docs/architecture.md` (a
section on previews and captures: the bridge, the two screenshot paths,
the record), `docs/repository-map.md` (the new files and the two schema
versions), `docs/features.md` (the entry, PR-9, extended by PR-10 and
PR-11), the skill's text (`internal/agents`: the capture files in a
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
  one transaction: the send's input and its `sent` outcome, the send's
  `capture_files` rows, the ticket (new or
  existing) with its attachment rows and comment, the receipt. The
  ticket attachment rows name the same hashes `capture_files` does, so
  the sweep keeps the files while either is live.
- **Send to Brakeman.** The send's input, with the request it will
  make (the person's question and the subjects), is committed before the
  desk is asked; the desk's request carries the send's id as its `opId`
  and is pinned; `sent` is appended after the desk answers and the pin
  released after that. A crash between the two is settled once the desk
  has recovered, by asking it again with the stored request and the same
  `opId` (found, found pruned or admitted: `sent`; Brakeman off:
  `refused` with its words; not yet the owner, stuck, the queue full,
  the budget spent or the pins full: still `pending`, asked again every
  60 s, the person able to cancel it). Every step for a send runs under
  its gate and reads its state and its cancels again there. A Cancel is
  committed (`capture_send_cancels`) before any step acts on it, a try
  that finds one asks nothing, and the settlement finishes every cancel
  not yet done, at start and every 60 s: a pending send with no request
  is `cancelled`, one whose request was admitted is settled `sent` and
  the request cancelled at Brakeman, a `sent` one's request cancelled;
  a cancel is done once the send is `cancelled` or its request is closed
  or pruned at the desk. So neither the gate's order nor a stop between
  two steps leaves a cancelled question running. No send is lost, none loses the person's question, none makes
  two requests however long it waits, and none is retried once its
  request is gone.
- **A send's state is one projection.** Its input never changes; its
  outcomes are appended, never changed; `sendState` reads them the same
  way in every replica and in any order of arrival (`sent` over the
  rest, else the earliest refusal or cancellation).
- **Send to a decision (PR-11).** The send row `pending`, with its
  `request` (the comment's text, frozen) and the comment's `opId` (the
  send's id), is committed before DR1's comment route is called; `sent`
  follows DR1's receipt. A crash before the comment is settled at start
  by calling DR1 again with the same `opId` and text; one after it by
  DR1's receipt for that `opId`, found, so no second comment is made.
  The lookup must last while the send is pending: where DR1's receipts
  are pruned, PR-11 finds the comment by the send's ref, which its text
  names (`capture:<id>/<send>`) and DR1 keeps with the decision, so no
  window ends the send's idempotency.
  DR1 lets a comment be made in every state of a decision, so a send
  that races an answer lands as a comment on the answered decision, as
  any comment does; the send route offers pending decisions and refuses
  nothing DR1 would take.
- **The revision is the server's.** `commit`, `content`, `changed`,
  `mutable`, `candidate`, `where_ran` and the sources' `found` and
  `class` are read by the API at the save, never taken from the client;
  the pane's label read at the selection is kept as `drift` only when it
  differs, and the document carries it. A capture never names a
  candidate unless its tree is that candidate's checkout and
  `checkoutIntact` finds it at the commit with no tracked path changed
  at the save.
- **A label read is the tree now.** Each read's `content` and changed
  paths are a function of the tree at that read alone: the private
  index's entries are reset to HEAD's at every read (`read-tree -m
  HEAD`), so nothing an earlier read staged survives into a later one
  (`TestANewIgnoreRuleLeavesTheLabel`).
- **What the page serves is the page's word.** The label and the save
  read the disk; what the frame renders is `serving`, `declared` only
  when the page's declared revision is the tree's, `stale` when it is
  another, else `unconfirmed`, never inferred from the disk
  (`TestTheServingRevisionIsThePagesWord`, and the Playwright page held
  on A while the tree is B).
- **One capture is one moment.** Every element of a capture was
  selected at the moment its screenshot shows (the URL, the viewport and
  scroll, the bridge's epoch, the label's commit and content): the
  workbench starts a new draft at any change, and the server refuses an
  annotation observed at another moment. Where there are pixels, every
  annotation's box is the same in the screenshot and in the frame taken
  at each later selection, or the element is another capture; an
  element seen moving is marked `stable: false`; without pixels no
  visual stability is claimed.
- **An annotation set is a revision, never its content.** A set is
  keyed by an id of its own and carries its `rev`, actor, time and
  operation; its content's digest is a column. A revert, an earlier
  order restored and two replicas' identical edits are each a set; a
  version, content alone, is shared by its hash
  (`TestAnAnnotationSetIsARevisionNotItsContent`).
- **A ticket's captures are its sends.** A capture appears on a ticket
  only through a send to it, frozen at its revision; a retake is linked
  to the first by `retakeOf` and reaches the ticket only when it is sent
  (`TestCapturesByTicketAreItsSends`).
- **No tree's secret is kept, and no retry needs it.** Every capture
  write is scrubbed of the tree's secret before it is checked or stored;
  nothing durable holds it. Its fingerprint is a digest of the request
  as received, and every route replays before it needs the tree, so a
  retry after the tree has ended is answered by its receipt
  (`TestALostReplyAfterTheTreeEndedIsReplayed`).
- **A cancel is kept before it acts.** A cancel's row is committed
  before any step for it, and the settlement finishes every cancel not
  done, so no gate order and no stop leaves a cancelled request running
  at Brakeman.
- **A file is kept while something holds it.** A content file a capture
  names is kept while the live capture (its screenshot), a send (what it
  froze and carried) or a ticket attachment names it; a delete releases
  what only the live capture held, in its own transaction.
- **What was sent stays.** A send's `digest`, the content file it
  names and its `request` never change, and `capture_files` keeps the
  file while the send exists; `EditCapture` writes only the live rows
  and the capture's `rev`, and never brings an element observed after
  the save; a send refused `stale` unless it names the revision now. A
  ticket attachment made by a send is the ticket's, removed only as any
  attachment is.
- **A candidate's preview checkout.** `preview.opened` (row `resolved`)
  is committed before the checkout is added or any step runs, in the
  transaction that leases the ports to the preview's tree; each step is
  a receipt before its process starts (EN1's order); `preview.ready`
  with the URL is committed to the row of its `seq` before the label
  offers it; the close transaction (`preview.ended`'s first half: the
  URL cleared, the preview owner closed) is committed before any service
  is signalled, so no label offers a URL a teardown is ending and no
  late step of that `seq` writes a row, and the port lease is released
  only once the end is committed (EN1's rule for trees). A service that
  exits after readiness ends its preview the same way (`why:
  service_exited`). The result checkout's `trees` row and the attempt's
  `a.trees` are never written by a preview. At most one open per
  candidate; `seq` only grows. Its run is held against eviction
  (`r.previews`) from the open's first transaction to the end's last,
  so nothing but its own end stops its services.
- **The bridge's messages.** The bridge answers only `window.parent` at
  an origin the server wrote into it (or the shell, for the desktop),
  with the nonce of its `hello`, a message at most 4 KiB, and posts only
  to that origin; the workbench accepts only the frame's window at the
  preview's origin with its nonce, at most 64 KiB. Nothing the page sends is executed or rendered as HTML
  by the workbench; HTML in a report is text.
- **Migration.** Dispatch: version 2 (or RV1's 3) to the next, the seven
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
- **Source maps.** The requirement asks for sources where
  instrumentation or source maps allow. The bridge reads instrumentation
  (`data-conductor-source`, Vue's `__file`, Svelte's `__svelte_meta`,
  React's `_debugSource`), which a dev build such as Vite's serves with
  modules unbundled, so the file named is the source itself. A source
  map maps a position in a generated script to its source, and a DOM
  element carries no such position a page's script can read: which
  function draws an element, or handles its click, is known to DevTools
  (`getEventListeners` is a console API, not a page API), not to the
  page. So in this item a source map has nothing to map from, and a page
  built without one of those dev hooks gets "the page gives no source"
  (arbitrary pages guarantee none). Mapping a handler or a bundled
  position back to its source needs the DevTools Protocol (out of scope
  above) or a graph of the code: it belongs with the enrichment of a
  capture with components and handlers, CG1's (the Code Context Graph),
  which joins on the repository-relative paths and commit a capture
  keeps.
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
  `main`**: PR-10 and PR-11.

## Tests per surface

Go, every PR that touches it: `make lint`, `go test -race -count=1
-timeout 30m ./...`, `make lint-static`. Web: `npm --prefix web run
typecheck`, `npm --prefix web test`, `make test-e2e` (the PR's spec
alone first while iterating, one Playwright run per worktree). Desktop:
`make desktop-test` (after `npm --prefix desktop ci`). The named tests:

- `internal/dispatch` (PR-1): `TestACaptureIsStoredWithItsImageOnce`
  (two captures of one image, one file), `TestCaptureBoundsAtTheirEdges`
  (every bound above at its edge and one past),
  `TestTheFullestCaptureFitsItsDocument` (every bound at once, notes of
  six-byte characters, created, edited and sent), `TestACaptureCrashBetweenFileAndRow`
  (the lease swept, no row), `TestAStaleEditIsRefused`,
  `TestAnEditRefusesANewElementReport`,
  `TestAnEditAfterASendKeepsWhatWasSent`, `TestAnEditWritesVersionsAndASet`
  (an unchanged annotation keeps its version; past `MaxAnnotationSets`
  refused), `TestTheFullestCaptureGoesThroughTheChangeLog` (a create,
  an edit of every note and a send at every bound through `record`: no
  change row past `MaxChangeValue`, the pointer's value and prior
  `{rev, set}`), `TestAnAnnotationSetIsARevisionNotItsContent` (a note
  A, then B, then A again: three sets, the first and third of one
  digest and two ids, each with its own actor and `op_id`, `Document` at
  r3 its own; an earlier order restored; two replicas' identical edits
  both kept, the pointer picking one whole), `TestSendToANewTicket`
  (the ticket, its description, two attachment rows naming the capture's
  hashes, the comment, one `rev` bump), `TestASendWithoutAScreenshotIsTheDocumentAlone`
  (Create a ticket and Attach to a ticket: one attachment row, the words
  "no screenshot", the attachment bound counting one),
  `TestTheDocumentCarriesTheDrift`, `TestASendKeepsItsDocumentAsAFileOnly`
  (the row holds the digest, the file the bytes, `capture_files` naming it),
  `TestSendToATicketPastItsAttachments`,
  `TestASendNamesTheRevisionNow`, `TestAPendingSendIsSettledOnce`,
  `TestSendOutcomesMergeByTheProjection` (pending beside sent, refused
  beside cancelled, sent beside cancelled, each in both orders of
  arrival: the same state), `TestATerminalSendTakesNoOtherOutcome`,
  `TestAPendingSendKeepsItsRequest`, `TestACancelIsKeptBeforeItActs`
  (`AskCancel`'s row and receipt; a ticket send and a refused one
  refused `stale`; a repeat answered by its receipt),
  `TestCapturesByTicketAreItsSends` (a retake of a capture sent to the
  ticket listed by `Captures(byTicket)` only once a send of it targets
  that ticket),
  `TestADeletedCaptureKeepsItsSends`, `TestADeletedUnsentCaptureReleasesItsImage`
  (swept after its grace and gone from `SnapshotHashes`; a screenshot
  another live capture shares kept; a sent capture's screenshot and
  document kept), `TestTheSweepKeepsACapturesFiles`,
  `TestSnapshotHashesNameCaptureFiles`, `TestMigrationToCapturesKeepsEveryRow`,
  `TestCaptureFieldClasses` (`rules.go`), `TestAnnotationsMergeAsOneScalar`
  (two replicas' edits of the same `rev`: both sets kept, the pointer
  of the later `at`, then actor, then `op_id`, wins, its set whole,
  never a mix), `TestReplayAnswersACaptureResend`.
- `internal/crew` (PR-2, PR-7, PR-9): `TestTheBridgeURLReachesServices`,
  `TestNoBridgeWithoutAPreview`, `TestTheTreeSecretIsInNoReceipt` (two
  trees get different secrets; each service sees its own in its
  environment; the value is in no step receipt, ledger row, event, task
  file, `AttemptState` or log line, and the preview keeps the name);
  `TestAClosedAttemptsCandidatePreviewOpens` (an attempt closed by its
  result, its preview cleared by `endAttempt`, previewed: the tree
  resolves, its steps run and it is ready),
  `TestAPreviewLeavesTheResultCheckoutsTreeRowAlone` (the `trees` row of
  kind `checkout` and `a.trees` unchanged by a preview's open, ready and
  end), `TestAPreviewServiceLostAfterReadyEndsIt` (`service_exited`, the
  URL cleared before the cleanup runs), `TestALateStepOfAnEndedPreviewWritesNothing`
  (a probe held at a gate past the end: no row of that `seq` written),
  `TestACandidatePreviewOpensAtItsCommit`
  (its steps' receipts under the attempt with the checkout's `dir`),
  `TestAPreviewIsEndedBeforeItsServicesAreSignalled` (a probe held at a
  gate), `TestAPreviewIdleIsEnded` (the engine's clock), `TestAPreviewEndsWithTheRun`,
  `TestAPreviewHoldsItsRunAgainstEviction` (a finished run's preview
  open while launches pass `maxRuns`: the run kept, its services alive,
  the label served; closed, the next launch evicts it),
  `TestAPreviewCheckoutWithChangesIsRefused`, `TestPreviewsAreBoundedPerServer`,
  `TestAPreviewLeftAtRestartIsReportedNotSignalled`,
  `TestAPreviewOpensForAnEvictedRun` (a finished run evicted past
  `maxRuns` before any preview: opened from the ledger, its receipts
  numbered after the attempt's last, `Get` still false, nothing
  scheduled, the idle end ending it), `TestAPreviewOpensForARunOfAnEarlierServer`
  (a run finished under a first server, a second over the same data
  directory: opened, its Runtime's `where_ran` in the row, ended at the
  shutdown),
  `TestAContinuedRunRefusesAPreview`; every existing test of `steps.go`
  passing unchanged with the attempt as the tree's owner;
  `TestTheRepositorysManifestIsValid` (PR-9).
- `internal/crew/ledger` (PR-7): `TestPreviewsTableMigration`,
  `TestAPreviewRowIsWrittenBeforeItsSteps`, `TestRecoverMarksAnOpenPreviewInterrupted`.
- `internal/brakeman` (PR-6): `TestACaptureRefParsesAndPrints`,
  `TestAPinnedOpOutlivesTheWindow` (a pinned op past 1,000 later
  requests: a repeat finds its request and never makes a second),
  `TestAPinnedPrunedOpAnswersItsNumber`, `TestPinsAreBounded`.
- `internal/proto` (PR-2): `TestPreviewMessagesFixture`.
- `internal/api`: PR-2 `TestTheBridgeNamesTheServersOrigins`,
  `TestTheBridgeIsServedWithItsHeaders`, `TestNoBridgeInTheEmbedIsA404AndNoName`;
  PR-3 `TestThePreviewLabelReadsTheTreeNow`
  (a file changed in the worktree between two reads),
  `TestANewIgnoreRuleLeavesTheLabel` (an untracked file counted by a
  first read; an ignore rule for it added, HEAD unmoved: the next read's
  changed paths name the `.gitignore` and not the file, its `content`
  the tree without it), `TestTheServingRevisionIsThePagesWord` (a page
  declaring commit A while the tree and both label reads are at B:
  `stale` naming A; the tree's own: `declared`; none, or a commit alone
  over a dirty tree: `unconfirmed`), `TestALostReplyAfterTheTreeEndedIsReplayed`
  (a create carrying the secret in its URL and a note, its reply lost,
  the preview closed: the same body again answered by its receipt, no
  `no_preview`; an edit and a Brakeman send with the secret pasted the
  same; another body under the same `opId` `op_conflict`; the secret in
  no byte of the store), `TestTheLabelGivesTheSecretToTheAdminAlone`,
  `TestACaptureTakesTheServersRevision`
  (a client's commit ignored), `TestTheTreeChangedBetweenSelectionAndSave`
  (`drift`), `TestASecondEditOfADirtyFileIsDrift` (the same file
  changed twice, same commit and count), `TestOtherPathsOfTheSameCountAreDrift`,
  `TestACaptureOfAnotherOriginIsRefused`, `TestACaptureWithoutAnOpenPreviewIsRefused`,
  `TestSourcesAreMadeRelativeAndChecked` (inside and found, inside and
  missing, outside, a `..` path), `TestSourcesAreClassedAndTheAppsFileFirst`
  (a library component under `node_modules` wrapping an app component:
  the app's tracked file first, the library's marked `vendor`),
  `TestTheBridgesFullestReportIsAccepted` (both shared fixtures, at the
  bridge's measure), `TestACaptureIsOneMoment` (an annotation observed
  at another URL, viewport, scroll, epoch or label refused),
  `TestATreeSecretIsScrubbedFromEveryCapture` (a page keeping
  `#token=<secret>` in its reported URL and in an observed one, in an
  `href` and in its HTML; the no-bridge fallback taking the frame's
  `src`; a note with the value pasted: the store's database file, every
  content file, the document, the ticket's description and the
  Brakeman request searched byte by byte, none holding it, the URLs
  keeping `${CONDUCTOR_TREE_SECRET}`), `TestLongChangedPathsAreCutByBytes`
  (200 paths of 4,000 bytes: the list within 16 KiB, `changed` 200,
  `changedCut`), `TestARetakeAcrossTheRepairLaunch` (the first capture
  on run 1's attempt sent to a ticket, the ticket's plan launched as run
  2 and its candidate captured with `retakeOf`: taken, `retake_via`
  `ticket`; the same run's: `run`; another repository's, a deleted one
  and a `url` capture's: refused), `TestAnImageThatIsNotAPNGIsRefused`
  (not a PNG; a valid header over pixel data cut short; a corrupt IDAT
  chunk),
  `TestAnImagePastItsSideIsRefused`, `TestCaptureRoutesAreAdminOnly`
  (a share link's controller, an agent's and a Brakeman turn's tokens
  refused), `TestTheImageIsServedSandboxed`; PR-6 `TestSendToBrakemanCitesTheCapture`
  (the frozen `capture:<id>/<send>` ref), `TestEveryDeskAnswerIsMapped`
  (off, queue full, budget spent, stuck, not the owner),
  `TestALostBrakemanSendIsSettledAtStart` (with a desk not yet recovered:
  the send stays pending, then is sent with the stored question, which
  differs from every note), `TestACancelHeldBeforeTheDeskCall` (a
  try held at a seam with the send's gate taken, just before the desk:
  the cancel's intent committed at once, its steps waiting for the gate;
  released, the request admitted and `sent`, then the cancel cancels it
  at Brakeman: one request in all, closed `cancelled`, the cancel
  `done`), `TestACancelSurvivesAStopBeforeTheDeskCancel` (a lost
  admission: the cancel commits its intent and `sent`, and a seam stops
  the server before `cancelBrakeman`; at the next start the settlement
  cancels the request, and the cancel sent again with its `opId` is
  answered by its receipt, `done`; a stop right after the intent
  instead: the next start's settlement makes the send `cancelled`),
  `TestACancelAfterTheAnswerIsDone` (the request answered before the
  cancel reached it: nothing closed, the cancel done, with the words),
  `TestACancelBeforeATryStopsIt` (the try finds `cancelled` and asks
  nothing), `TestACancelAfterALostAdmission` (admitted, the `sent`
  append failed through a seam: the cancel finds the request by its op,
  settles `sent` and cancels it), `TestACancelIsIdempotent`,
  `TestARecoveredSendOutlivesTheOpWindow` (admitted, `MarkSent` failed,
  1,001 other requests, then recovery: no second request, the send
  `sent`, the pin released), `TestAPrunedRequestSettlesTheSend` (`sent`,
  never asked again), `TestBrakemanReadsTheFrozenSendAcrossEditAndDelete`,
  `TestBrakemanReadsACaptureUnderItsDispatchClass`
  (refused without the class), `TestCaptureAdmissionFollowsTheScope`
  (a capture of a run outside the grant's projects, a `url` capture
  under a scoped grant, a scope narrowed between two reads),
  `TestTheGuideCarriesCaptureRead`; PR-8
  `TestPreviewCommandsHaveReceipts`, `TestAHiddenTrackedChangeIsNoCandidate`
  (a tracked file changed under `skip-worktree` in the checkout: `git
  status` clean, the capture names no candidate); PR-10
  `TestAMaximalCaptureFindingReachesTheRepair`,
  `TestCaptureEvidenceSurvivesARestartAndAContinuation`,
  `TestTheStoreAdmitsAReviewSend`; PR-11 `TestTheStoreAdmitsADecisionSend`,
  `TestADecisionSendIsACommentAndARow`, `TestADecisionSendLostBeforeItsComment`,
  `TestADecisionSendLostAfterItsComment` (one comment, the send `sent`),
  `TestASendRacingAnAnswerLandsAsAComment`.
- `internal/cli` (PR-9): `TestServeTakesItsTokenFromTheNamedVariable`
  (an empty or unset variable refused; the token printed nowhere).
- `internal/agents` (PR-6): the skill's golden with the capture files.
- vitest: PR-2 `web/app/utils/previewBridge/report.test.ts` (the cut at
  each bound on an element boundary, the cut order to the report's
  bound with every field at its own and through step 10 on the
  largest-tag shape, both fixtures written, the measure against Go's on
  `<`, `&`, U+2028 and a lone surrogate, the shared fixture written;
  input values and password fields never in the HTML, the styles list),
  `gate.test.ts` (with a fake parent and a foreign window: a message
  from another origin, from a window that is not the parent, without
  the nonce, past 4 KiB or of an unknown type dropped unanswered; every
  post addressed to the parent's origin, never `*`),
  `select.test.ts` (a selector
  found exactly once, an `id` preferred only when unique; Down stepping
  from an overlay to the button under it; Left, Right and Shift+Up and
  Down reaching a `pointer-events: none` button and an element of an
  `inert` subtree, neither in `elementsFromPoint`, the hit test naming
  what a click reaches; the epoch bumped once a frame by a mutation, a
  resize, a scroll, an animation event and a transition event, never by
  its own shadow root),
  `hints.test.ts` (Vue, Svelte, React 18 and `data-conductor-source`
  from fake elements; none), `web/app/utils/protocol.test.ts`
  (`parsePreviewMessage` against the shared fixture); PR-4 `web/app/utils/capture.test.ts`
  (`parseBridgeMessage`: the origin, the source, the nonce, a message
  past 64 KiB, an unknown type; the label's words; `diffSinceSent`),
  `web/app/composables/usePreviewBridge.test.ts` (the 3 s hello's words,
  one request in flight; a selection after a client-side navigation, a
  device-mode change, a scroll, a page mutation or a label whose content
  moved starting a new draft with its own screenshot and moment, never
  joining the first; a selection on the unchanged page joining it; a
  `looked` whose epoch moved retaking the screenshot, twice, then the
  words; through `previewSeam`'s frames, with no epoch change: a
  CSS-animated control whose box differs between the screenshot and the
  next frame kept `stable: false`, and a later selection while it moves
  starting a new draft; a hover-styled control whose box differs once
  the pointer has left it putting the next element in a new draft; the
  same pixels joining; Select by selector starting a draft as a
  selection does), `web/app/utils/screenshot.test.ts` (the scale
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
  agent works"; Capture, select a button under an overlay that covers
  the whole page by stepping Down to it, the hit test's words naming the
  overlay, a second element on the unchanged page joining the capture,
  a third after a client-side navigation starting a second capture (its
  URL the new one, its own screenshot), a region; a `pointer-events:
  none` button and an `inert` section selected by walking the DOM and by
  selector; a CSS-animated control and a hover-styled one, which change
  no DOM, each its own capture or `stable: false` as the pixels say;
  Save; the record read back with the server's commit and content; the
  fixture page, which declares the commit its service started at, held
  there while the spec commits B in the worktree without reloading it:
  both label reads B, the capture's `serving` `stale` naming A; the
  screenshot and frames through `previewSeam`, whose source in the spec
  is Playwright's own screenshot of the frame's box (`exposeFunction`),
  so the comparisons run on real pixels, since the tab capture's prompt
  is no test's; the page without the bridge saying so after 3 s); PR-6 `web/e2e/capture-send.spec.ts` (Create a
  ticket and its two attachments and description; an edit, "edited
  since", a second send of r2 beside r1's; Attach to the run's ticket;
  a capture saved with no screenshot sent as one attachment; Ask
  Brakeman with the stub's turn reading `capture_send_read`); PR-8
  `web/e2e/candidate-preview.spec.ts` (Preview this candidate after the
  attempt's session has ended and been collected, on the preview page,
  the label "as committed", Close, the receipts on the run page, a run
  stopped ending it); PR-9 `web/e2e/capture-repair.spec.ts` (the loop on a stub:
  capture, ticket, a plan revision with the capture attachments
  preselected, a launch, a stub worker that reads
  `.conductor/attachments/capture-*.json`, edits the file its first
  source names (the app's tracked file, a library component's under
  `node_modules` listed after it) and submits, Preview this candidate,
  Capture again, the new capture's `retakeOf` read back from the server
  as the first's id (the two captures in distinct runs, `retakeVia`
  `ticket`), the ticket's Captures showing the first alone, then Attach
  to a ticket with the run's ticket offered first, and the ticket's
  Captures showing both, the retake frozen at its revision); PR-10 `web/e2e/capture-review.spec.ts`;
  PR-11 `web/e2e/capture-decision.spec.ts`. Each spec waits on what the
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
   by DS2's code), and, after PR-10, as the text of a repair's findings
   in `task.md` with the capture's two files in `.conductor/evidence/`
   (written through the attempt's `os.Root`); nothing an attempt needs
   is only in the server's memory.
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

PR-1 to PR-9 build on `main` as it is (EN1, DS2 and Brakeman's requests
are there) and meet the acceptance boundary. PR-10 waits for RV1's PR-4
(the review routes) and PR-6 (the Review view and its draft), and PR-11
for DR1's PR-4 (the decision routes) and PR-7 (the decision card): each
names its schema version and the rows it adds when it is cut.

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with |
|---|---|---|---|---|---|---|
| PR-1a | dispatch: captures and their annotations (schema version 5, issue #789) | the seven tables (annotation versions and sets, send inputs, outcomes and cancels), indexes and migration, the bounds, the capture document, `CreateCapture`, `EditCapture`, `DeleteCapture` with its release, the reads of captures, `rules.go`'s classes of every new table, the sweep's and `SnapshotHashes`' files, `OnChange`'s entity `capture` | `internal/dispatch` | the `internal/dispatch` tests above but the sends' | none | PR-2 |
| PR-1b | dispatch: a capture's sends, their outcomes and cancels (issue #825) | `SendCapture` to a ticket with its attachments and comment and to Brakeman, `ticketText`, `MarkSent`, `MarkRefused`, `CancelSend`, `AskCancel`, `sendState`, the sends' reads, `OnChange`'s entity `capture_send`; no schema change | `internal/dispatch` | the sends' `internal/dispatch` tests above | PR-1a | PR-2 |
| PR-2 | bridge: the script, its build, its serving and its name in a tree; the tree's secret | `previewBridge.entry.ts` and `utils/previewBridge/` (`report.ts` with the cut order and the measure, `select.ts`, `hints.ts`, `gate.ts`), the Vite library build for the dev server and the Nuxt build hook for the built app, `make web-build`'s check, `previewbridge.go` with the origins and its `404 no_bridge`, `Server.SetListen` and its call in `runServe`, `Engine.SetPreviewBridge` and `tree.names` with `CONDUCTOR_PREVIEW_BRIDGE` and `CONDUCTOR_TREE_SECRET`, the skill's line, the bridge's messages in `internal/proto/preview.go` and `web/app/utils/protocol.ts` with their shared fixture, `docs/protocol.md`'s bridge messages, their bounds and the bridge's gate, and the route | `web/app/utils` (`protocol.ts` among them), `web/nuxt.config.ts`, `Makefile`, `internal/proto`, `internal/web`, `internal/api`, `internal/crew` (`steps.go`), `internal/cli` (`serve.go`), `internal/agents`, `docs/protocol.md` | the PR-2 vitest and Go tests above (`TestPreviewMessagesFixture` and `protocol.test.ts` among them) | none | PR-1a, PR-1b |
| PR-3 | api: the preview label, captures created, read, edited and deleted | `captures.go`'s routes but send, each replayed before it needs the tree with its fingerprint taken before the scrub, what the server fills (the sources' classes and order, the page's declared revision as `serving`, `retakeOf` and `retake_via`, the drift by `content`, the changed paths' byte cut, the one-moment check, the secret's scrub, the whole PNG's check), the label route with the admin's `url`, `crew.TreeRevision` beside `checkoutIntact` (its private index reset to HEAD's entries at every read), the shared report fixture, the image route, the `dispatch` event's entities, `docs/protocol.md`'s rows and the capture document | `internal/api`, `internal/crew` (`verify.go`), `docs/protocol.md` | the PR-3 `internal/api` tests above | PR-1a, PR-2 | none |
| PR-4 | web: the preview in the pane, selection, annotations, a capture saved | the preview page (`pages/preview.vue`), `PreviewPane`, the `url` tab's Capture, `usePreviewLabel`, `usePreviewBridge` and `previewSeam` (the moment's pixel check, Select by selector), `screenshot.ts` (the tab's path, its frames, and none), `CaptureEditor`, `CaptureAnnotation`, `CaptureElement`, `useCaptures`, `utils/capture.ts`, Open in Conductor and New tab's admin `url` on the run page and the palette, the fixture page and its service, `preview-capture.spec.ts` | `web/app/components`, `web/app/composables`, `web/app/utils`, `web/app/pages`, `web/e2e` | the PR-4 vitest files, `preview-capture.spec.ts` | PR-3 | PR-7 |
| PR-5 | desktop: the bridge in any frame and the frame's pixels | `desktop/src/preview.ts`, the two IPC channels and their guard, the preload's two functions, `utils/desktop.ts`'s mirror and `screenshot.ts`'s desktop path, `docs/architecture.md`'s paragraph | `desktop/src`, `web/app/utils`, `docs/architecture.md` | `preview.test.ts`, `ipc.test.ts`, `desktop.test.ts` | PR-4 | PR-6, PR-7 |
| PR-6 | sends: a ticket and Brakeman; the ticket's captures | the send route (ticket, with the document alone when there is no screenshot, and Brakeman, `askBrakeman` with every desk answer, pinned, `settleCaptureSends` from the stored request, the send gates, the cancel route and the settlement of every cancel not done, `finishCancel` and `cancelBrakeman`), the Brakeman store's pinned op refs, the `capture` refs (live and frozen) and their resolution, the observe reads with their admission and `capture_read` and `capture_send_read` in the guide, the skill's capture files, `CaptureSend`, `DispatchCaptures` on the ticket page, `DispatchPlanPropose`'s preselection, `capture-send.spec.ts`, `docs/protocol.md`'s send row | `internal/api`, `internal/brakeman`, `internal/agents`, `web/app/components`, `web/app/composables`, `web/e2e`, `docs/protocol.md` | the PR-6 Go tests, `capture-send.spec.ts` | PR-4, PR-1b | PR-5, PR-7 |
| PR-7 | crew: a tree's owner, and a candidate's preview checkout | `treeOwner` in `steps.go` (the open attempt as today; `resolveTree`, `prepareTree`, `treeReady`, `endTree` and the service-exit handling through it), the preview as the second owner, the ledger's `previews` table and migration (the next version), its writers and `Recover`, `Engine.OpenPreview`, `ClosePreview`, the idle end, the bound, the run held against eviction (`r.previews` in `idle`), the preview-only context of a run the engine no longer holds (`loadForPreview`), `AttemptState.Previewing` | `internal/crew`, `internal/crew/ledger` | the PR-7 `internal/crew` and `internal/crew/ledger` tests | PR-2, PR-3 | PR-4, PR-5, PR-6 |
| PR-8 | a candidate's preview opened, labelled and captured again | the two command routes and the label's `seq`, Preview this candidate on `RunTasks` and `DispatchCandidates` (opening the preview page), Capture again (`usePreviewBridge`'s `describe`, `useCaptures` and `utils/capture.ts` sending `retakeOf`), `candidate-preview.spec.ts`, `docs/protocol.md`'s rows and events | `internal/api`, `web/app/components`, `web/app/composables`, `web/app/utils`, `web/e2e`, `docs/protocol.md` | the PR-8 Go tests, `candidate-preview.spec.ts` | PR-5, PR-6, PR-7 | none |
| PR-9 | Conductor's own app, the loop end to end, the docs and the entry | `conductor.manifest.json` (the tree's secret, the probes' timeouts), `serve --token-env`, `TestTheRepositorysManifestIsValid`, `plugins/previewBridge.client.ts`, `nuxt.config.ts`'s `previewBridge` and `CONDUCTOR_DEV_API`, the stub worker's `STUB_CAPTURE_FIX`, `capture-repair.spec.ts`; `docs/architecture.md`, `docs/repository-map.md`, `README.md`'s line, `docs/features.md`'s entry | `conductor.manifest.json`, `internal/cli`, `internal/crew` (the manifest's test), `web/app/plugins`, `web/nuxt.config.ts`, `web/e2e`, `docs`, `README.md` | `capture-repair.spec.ts`, the PR-9 Go tests; the scripts suite is not touched | PR-8 | none |
| PR-10 | review: a capture as a finding, and Request a change (after RV1) | the finding kind `capture` with `capture_ref` (`{capture, send, k}`, the next Dispatch version), its binding by RV1's rules (the request fingerprinted, the finding's message projected by the server within RV1's 1,000 characters, its path and line the first tracked source), the evidence files pinned to RV1's change request by reference and written into the repair attempt through the resolver (`internal/crew`, above), the send kind `review` admitted by the store (PR-1b's refusal lifted), Request a change and Add to a review in `CaptureSend` and in RV1's draft, the capture finding's row in the Review view, `capture-review.spec.ts`, the docs and the entry's paragraph | `internal/dispatch`, `internal/api`, `internal/crew` (the change request's evidence refs, `UseEvidenceFiles`, `taskfile.go`), `web/app/components`, `web/app/composables`, `web/e2e`, `docs` | the PR-10 tests in `internal/dispatch`, `internal/crew` and `internal/api` (`TestACaptureFindingIsFilledByTheServer`, `TestRequestAChangeRepairsWithTheCapture`, `TestAMaximalCaptureFindingReachesTheRepair`, `TestCaptureEvidenceSurvivesARestartAndAContinuation`, `TestTheStoreAdmitsAReviewSend`), `capture-review.spec.ts` | PR-9; RV1's PR-4 and PR-6 | none |
| PR-11 | decision: a capture attached to a decision (after DR1) | the send kind `decision`, admitted by the store (PR-1b's refusal lifted in `internal/dispatch`) and sent through DR1's comment route (kind `comment`, the capture named in its text with its revision and the send's ref, the send's id its `opId`; the send's input this item's, `pending` with its text before the call and settled from DR1's receipt, or from the comment found by the send's ref where DR1's receipts are pruned), the decision card's captures from `capture_sends`, Attach to a decision in `CaptureSend` (pending decisions offered), `capture-decision.spec.ts`, the docs and the entry's last paragraph | `internal/dispatch`, `internal/api`, `web/app/components`, `web/e2e`, `docs` | `TestTheStoreAdmitsADecisionSend`, `TestADecisionSendIsACommentAndARow`, `TestADecisionSendLostBeforeItsComment`, `TestADecisionSendLostAfterItsComment`, `TestASendRacingAnAnswerLandsAsAComment`, `capture-decision.spec.ts` | PR-9; DR1's PR-4 and PR-7 | none |

Sizes, as the owner asked (2026-11-05: a row is a batch, 300 to 800
changed lines, at most about 1,500 with its tests): PR-1a about 1,850
lines and 1,050 of tests; PR-1b about 700 and 900; PR-2 about 650 and 500; PR-3 about 800 and 700; PR-4
about 800 and 550; PR-5 about 350 and 300; PR-6 about 800 and 700; PR-7
about 800 and 700, the owner seam about 250 of them, every existing
`steps.go` test kept as the proof that the attempt's path did not move;
PR-8 about 450 and 400; PR-9 about 450 and 400, with the docs; PR-10
about 700 and 650; PR-11 about 350 and 400.

The row PR-1 was cut into PR-1a and PR-1b on 2026-11-10, when its code
alone outgrew its estimate of about 800 lines three times over.

## Owner decisions

Dated 2026-11-05. Each is a default the owner can overturn with a dated
row in the Program's status log, its convention for a decision the owner
did not make (the Program, section 3.1); the reasoning is why it was
taken.

1. **What this item leaves to others.** Who answers a question for a
   team (Switchman's policy) and how the code graph is built (CG1's)
   are not touched: a capture names its actor as C2 gives it and keeps
   repository-relative sources at a named commit, which a graph can
   join on later. Pricing does not bear on a free item. How replicated
   Dispatch records merge is CS1's to decide; this item only declares
   its entities' classes with DS1's existing rules, as DS1's plan lets
   each stream: a capture's facts immutable, its annotations immutable
   versions and sets under one scalar pointer by DS1's last-writer rule,
   a send's input immutable and its outcomes a log read through one
   projection. None blocks this item.
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
   agent's session: that would leave no record, and an attempt's changes
   have one write path, a repair Yardmaster applies with a receipt; a
   second path beside it would let a change reach a tree that no ledger
   row explains.
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
    directory, publishing nothing, its token the tree's secret, minted
    per tree and never written in the file) and the web app's dev build
    loads the bridge from the tree's name; the built app never loads it.
    Chosen over a crew-only manifest in the by-hand notes, so every
    contributor and agent previews Conductor the same way.
13. **The bounds** above (20 annotations, a 32 KiB element report cut
    to fit in a fixed order and measured the same way by the bridge and
    the server, a 2 MiB document that holds every annotation at every
    bound, a 4 MiB PNG of at most 4,096 pixels a side, 2,048 on its long
    side as taken, 64 KiB bridge messages to the workbench and 4 KiB to
    the bridge, 5,000 captures), sized so that a capture's two files fit
    DS2's 16 MiB of attachments in a package beside others.
14. **Schema versions** are the next after `main`'s when each PR is
    cut (Dispatch 3 unless RV1's PR-1 is first; the ledger 8 unless
    DR1's PR-1 is first), and each PR says which.

## Acceptance

The boundary: a person picks out a control that is broken in
Conductor's own app, and the ticket made from that pick is enough for
another agent to repair it, with nobody describing the fault in words.
It is observed, not ticked:

- Once PR-9 is merged, on the owner's `conductor serve` (WSL) with the
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
  the browser), attaches it to the ticket (Attach to a ticket, the run's
  own ticket offered first), and reviews the evidence on the ticket
  page: both captures side by side, each as sent, the candidate's
  receipts.
- On Windows, the desktop app frames the preview served inside WSL with
  no step by the person; if it does not, that is a defect of this
  stream, fixed in it (the shell forwards the port itself, as
  `desktop/src/udp-forwarder.ts` forwards ICE), never a networking
  instruction.
- The observation, and what was missing, slow or wrong, goes into
  `docs/features.md` (PR-9 when it comes before that PR merges, else a
  later docs PR of the stream); a bug the stream caused is fixed in the
  stream, a defect outside it filed.
- Done means: PR-1 to PR-9 merged and the observation recorded with the
  owner's word that the agent repaired the control from the capture
  alone. PR-10 and PR-11 then complete the destinations; the Program's
  status row moves to Done when they are merged too and each has been
  seen once (a capture sent as Request changes and repaired; a capture
  on a pending decision answered), never on the table alone. It is part
  of the browser-captured ticket that the Program's third release must
  show before it ships (the Program, section 6).

What needs the owner: the review of this plan and its decisions;
cutting the eleven issues; the observation, whose agent runs on the
owner's subscription, on the WSL box with the Windows desktop app; the
merges.
