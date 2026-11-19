# A notification that names its record, the open decisions' refs, and the phone shell with its bridge (MB1 OSS-0a, OSS-0b; issue #998)

Status: proposed 2026-11-18, for review before any code of its rows.
Main at `f10eb297`; every fact below about the code was read from that
commit on 2026-11-18, and file, function and constant names are main's.
This plan is the public text of the open-source rows of Program item
MB1, mobile decisions: a phone that is told a durable decision waits,
opens it whole and answers it. It is in two parts, each reviewed and
merged before the code of the rows it plans:

- **Part one** (OSS-0a, for the row OSS-2a): `ext.Notification.Ref`,
  the record a notification is about, in one form, `decision:<dr-n>`;
  set by DR1's decision notification and checked where the server builds
  it; and `ext.Host.OpenDecisionRefs`, the refs of the decisions open in
  the server's ledger, for a notification sink that missed some. It adds
  one line to `docs/plans/ext.md`'s C3 record.
- **Part two** (OSS-0b, for the rows OSS-1a, OSS-1b, OSS-2b and OSS-3):
  the phone shell, `mobile/`, a Capacitor frame around the workbench's
  own screens beside the desktop shell (`desktop/`), with a native key
  plugin that signs only what a native policy builds, a store with
  bounds, a versioned bridge `window.conductorShell` (v1) the workbench
  reads, its free default (open a Conductor server by its address), the
  shell's own screens, and the decision page drawn from an inbox a
  provider supplies through the bridge.

The words used here:

- A **provider** is code outside this repository, as `docs/plans/ext.md`
  says. Part one's provider is a notification sink (`ext.NotificationSink`)
  in a Conductor server. Part two's is a **shell provider**: a script a
  packaging of the shell places in its assets, which registers with the
  bridge and supplies sign-in, notifications and an inbox of decisions.
  This repository holds neither, names neither and checks nothing about
  either beyond whether one is registered.
- A **packaging** is a build of the shell someone makes for themselves
  from this repository: their app id, their signing, their push
  entitlement, and optionally a shell provider and its native signing
  policy. This repository publishes no binary of the shell; its CI builds
  it unsigned as a check, as it builds the desktop shell.
- The **free default** is what the shell does with no provider: Open a
  server, which opens a Conductor server's own pages (MB0's decision page
  on the LAN, a decision link through a switchyard) in the in-app browser.

The names follow the railroad: Conductor is the application, Yardmaster
the harness that applies an answer, Switchman the routing a decision
router provides. Issue #160 (a Capacitor app) is where the shell was
asked for; the part of it left after this item is the workbench's other
screens in an installed frame (Out of scope).

**Prerequisites, all on `main`.** C3 (`docs/plans/ext.md`: the notifier
and its sinks, `internal/api/notify.go`), DR1 (`docs/plans/decisions.md`:
the decision record, its notification after each committed change,
`decisionNotification` in `internal/api/decisions.go`, and its card and
answer components, `DecisionCard.vue` and `DecisionRespond.vue`), MB0
(`docs/plans/decision-inbox.md`: the decision page, the join page's
Decide with its evidence seam `REF_ACCESS`, the four delivery steps) and
the decide grant (`docs/plans/decide-grant.md`: the decide page and its
bounded read of the open decisions). Nothing here waits for another
row of another item.

## Articles it serves

The twelve articles are the Program's (section 2.1).

1. **No paid code.** Part one adds a field and a read to `ext`; part
   two adds a generic shell, a bridge and a page that reads from what a
   shell supplies. None names a service, a contract of one, a route of
   one or a plan. The key plugin signs only what a native policy builds,
   and the open-source build has no policy, so the format of anything a
   service asks the phone to sign is not known here. The web app asks
   only whether a bridge with a provider is present, as it asks whether
   a provider is registered.
2. **The open-source build is fully useful.** A build with no sink and no
   shell is `main`: the notifier, the webhooks (byte for byte), browser
   notifications, the decision page, the decide page and decision links
   behave as they do. The shell built from this repository with no
   provider opens a Conductor server's own decision page by its address,
   on the LAN or through a self-hosted switchyard, with no account.
3. **Paid features are gated by hosted dependencies.** No flag, setting
   or tag turns anything on: a sink exists only when one is registered,
   and the shell's inbox only when a packaging places a provider.
4. **Stop, inspect, revoke, export, approvals free.** A notification
   never answers anything, and the shell registers no notification
   action. An answer from the phone reaches a run only through DR1's
   respond path on the server whose ledger holds it, with DR1's recheck.
   The workbench token, the decision page, decision links and `conductor
   export` are untouched.
5. **Dependencies point one way.** `ext` stays standard library only.
   `mobile/` is a directory of this repository with its own npm
   packages; no Go module is added and the open-core lint is unchanged.
6. **No feature build tag.** None is added. iOS and Android are
   platforms of `mobile/`, not features.
7. **The free default is what `main` does.** No sink registered: the
   notifier's sinks are the webhooks, as today. No bridge: the workbench
   reads the server's routes as today. `Ref` is additive and reaches no
   webhook.
8. **A provider's outage is the free default, mid-session.** A sink that
   fails or hangs is already bounded by C3's queue and wait; `Host`'s
   new read answers from the ledger and never waits on a provider. In
   the shell, a provider that cannot be reached leaves Open a server
   working in every state and the last inbox it showed readable with its
   time; nothing is sent.
9. **CGO off.** No Go change compiles for the phone; the shell is
   TypeScript and Swift (and Kotlin for the second platform).
10. **The repository contract.** This plan before its code, its rows
    grouped into pull requests as `AGENTS.md` says (The PRs), Go race
    tests for part one, vitest and Playwright
    for the web rows, the shell's own vitest, Playwright and XCTest jobs,
    the notices regenerated with each new npm package. No
    `internal/proto` frame changes (Events, Protocol).
11. **Names keep their meaning.** The frame is the **shell** (as the
    desktop one is), its interface to the workbench the **bridge**, what
    a packaging adds the **provider** and the **signing policy**, a
    notification's record its **ref** (the word Brakeman and the decision
    inbox already use for `decision:dr-<n>`).
12. **Execution behind one seam.** Not touched: nothing here starts,
    stops or runs a process.

## Why and what happens

A person away from the desk should understand and resolve a decision
that waits for them. MB0 gives the free half: the decision page in a
phone's browser, on the LAN or through a switchyard's decision link,
while the phone can reach the owner's server. What is missing is the
phone being told, and an installed way in that is not a browser tab
someone has to remember. A registered provider can supply both; this
item gives it the open-source pieces it needs and nothing more:

**Part one, on the server.** A sink that tells a phone "a decision
waits" must say which decision without forwarding the notification's
words: today an `ext.Notification` carries the question as `Message`,
the run page's address as `URL` and the session, and nothing that names
the record as a stable key. After OSS-2a every decision notification
carries `Ref: "decision:dr-7"`, the same string Brakeman and the decision
inbox use for that record, and nothing else changes: a sink that wants
to send only the record's name and its state can, and the webhooks'
bodies stay byte for byte as they are. A sink that lost notifications
(its own outage, a restart) asks `Host.OpenDecisionRefs` for the refs of
every decision still open, in pages, and tells again what it must; it
reads nothing else of them through that call.

**Part two, on the phone.** After OSS-1a and OSS-1b, anyone with a Mac
can build the shell from this repository (`make mobile-install
mobile-sync mobile-ios`) and run it in the simulator, or on their own
phone under their own signing. With no provider it opens on **Open a
server**: a field for a Conductor server's address or a decision link,
opened in the in-app browser, `https://`, or `http://` with the words
"This address is not encrypted: use it only on a network you trust",
and the last five addresses opened. That is MB0's free phone path in an
installed frame.

A packaging that places a provider gets, from the same build:

- **Signing in.** The shell opens on the provider's sign-in screen: a
  code in large type, **Open the sign-in page** (the provider's page, in
  the in-app browser) and the time left. The key the provider binds is
  made on the phone by the shell's key plugin and never leaves it.
- **Being told.** The provider's service sends a notification to the
  phone through the platform's push; what it shows is the provider's
  business, and the shell adds no action to it. Tapping it opens the
  shell on `/shell/open?h=<handle>`: the shell asks the provider which
  decision the handle names, reads it at that moment, and draws it.
- **Opening.** The decision is drawn by DR1's own card (`DecisionCard.vue`)
  with the four delivery steps exactly as the provider gives them,
  never merged: *Request available*, *Response recorded*, *Received and
  validated by the execution owner*, *Work resumed*. Its evidence chips
  show their ref and say it is opened on the desktop.
- **Answering.** The person picks, types or dictates an answer in DR1's
  own `DecisionRespond.vue` and presses **Answer**. The shell reads the
  decision again; if it changed it says "This decision changed after
  you wrote your answer: read it again" and sends nothing; otherwise the
  provider asks the key plugin for a signature, and the plugin asks for
  Face ID or the passcode ("Confirm your answer in Conductor") before it
  signs anything. A cancelled check sends nothing. A second press is the
  same answer, never a second one.
- **Offline.** An answer made with no network waits as a draft: "Not
  sent: you are offline. It will be checked and sent when you are back
  online, if the decision is still as you read it", and on return it is
  read again and sent once after the same confirmation. A send whose
  outcome is not known locks the draft ("Checking whether your answer
  arrived") until the provider says whether it arrived.
- **Settings.** Who is signed in, notifications with their state and
  **Send a test notification**, **Open a server** in every state, and
  **Sign out**, which the provider makes take effect on the phone at
  once, even offline.

What a person does not see change: a server without a sink and a browser
without a shell behave exactly as on `main`.

## The changes

### Part one: `ext` (OSS-2a)

`ext/notify.go`:

```go
type Notification struct {
	// ... every field as on main ...
	Origin  string     `json:"origin,omitempty"` // the provider that sent a notice
	Ref     string     `json:"ref,omitempty"`    // the record it is about: decision:dr-<n> (DecisionRef), or ""
}

const (
	// MaxNotificationRef bounds Ref in bytes: "decision:" and a decision's
	// id of at most MaxDecisionRef bytes.
	MaxNotificationRef = 9 + MaxDecisionRef
	// MaxOpenDecisionRefs bounds a page of Host.OpenDecisionRefs.
	MaxOpenDecisionRefs = 256
)

// DecisionRef is the ref of the durable decision id ("dr-<n>"):
// "decision:dr-<n>", the form Brakeman's refs and the decision inbox
// read. It does not check id; ParseDecisionRef does.
func DecisionRef(id string) string

// ParseDecisionRef is the decision's id of ref, false for a ref that is
// not exactly decision:dr-<n>: n of 1 to 18 digits with no leading zero
// (^decision:dr-[1-9][0-9]{0,17}$), so at most MaxNotificationRef bytes.
func ParseDecisionRef(ref string) (id string, ok bool)
```

- **One form in v1.** `Ref` is empty or `decision:dr-<n>`. Its grammar is
  exactly the one Brakeman's `decision:<dr-n>` ref already reads on
  `main` (`internal/brakeman/refs.go`, `decisionIDPattern`) and the one
  `ledger.DecisionRef` writes (`internal/crew/ledger/inbox.go`); `ext`
  cannot import either, so the form is written again in `ext` and a test
  in `internal/brakeman` holds the two equal (Tests). Another kind of
  record gets a form of its own in a later row, never a list in one ref.
- **`Notification.Valid`** (a provider's notice, what `Host.Notify` is
  handed) gains one check: `Ref` empty or one `ParseDecisionRef` takes;
  any other refused with the field named, as the other fields are.
- The JSON field is `ref`, `omitempty`: a notification without one
  marshals as on `main`.

`ext/host.go`, `Host` gains one method:

```go
	// OpenDecisionRefs reads the refs (DecisionRef) of the durable
	// decisions open in this server's ledger now, in id order, the first
	// after after's decision ("" for the first), at most
	// MaxOpenDecisionRefs; next is the last ref of a page that more
	// follow, "" for the last page. It reads nothing else of them: a
	// provider that needs a decision's state reads it with Decision. Each
	// page is read alone: a decision opened or closed between two pages
	// may be missed or listed by the walk (its own notification tells it).
	// It answers ErrRefused for an after that is neither "" nor a ref
	// ParseDecisionRef takes, and ErrUnsupported where the server keeps no
	// ledger (a switchyard, conductor host).
	OpenDecisionRefs(ctx context.Context, after string) (refs []string, next string, err error)
```

Any provider with a `Host` may call it (a notification sink above all:
it is how a sink that missed notifications finds what is still open). It
answers from the ledger, never from a provider, within the Host's own
bound on a call. It is not the decision router's `Host.OpenDecisions`
(`docs/plans/decision-policy.md`), which hands the registered router
alone each open record whole with its policy, 64 a page: this read hands
any provider the refs alone, 256 a page.

`ext/exttest`:

- `FakeHost.OpenDecisionRefs` refuses a malformed `after` with
  `ext.ErrRefused` as the server does, and otherwise answers what
  `OnOpenDecisionRefs(fn func(after string) ([]string, string, error))`
  set, or no refs (the fake holds no ledger); it records the call as the
  fake's other calls do (`tell`).
- The `Sink` check's `notices` hands a sink that lists `decision` (or
  takes every entry) a decision notification as the server types it:
  `Event` and `Type` `decision`, `State` `open`, `Ref`
  `decision:dr-1`, a question as `Message`, a run page's address as
  `URL`. Today such a sink is handed an entry of type `decision` with no
  event name, which the server never sends.

`docs/plans/ext.md` (this plan's PR): C3's record of `Notification`
gains the `Ref` line. `docs/repository-map.md` (OSS-2a): the `ext` row's
`NotificationSink` names `Ref`, and its `Host` names `OpenDecisionRefs`.

### Part one: `internal/api`, `internal/crew/ledger`, `internal/hostagent` (OSS-2a)

- **The decision notification sets it.** `decisionNotification`
  (`internal/api/decisions.go`) sets `Ref` to the row's ref beside its
  `State`, through a package seam `decisionRefOf` (`ext.DecisionRef` by
  default) a test replaces. Nothing else of the notification changes.
- **Checked where the notifier takes it, before any queue.**
  `notifier.decision` checks `Ref` with `ext.ParseDecisionRef` before it
  queues the notification for any webhook or sink: a malformed one is a
  bug of the server's own, logged at error (with its length, never its
  text) and emptied, and the notification is queued without it. The
  notifier gains the server's logger for it (`startNotifier`).
- **No attention entry sets it.** `notifier.notification` (a session's
  activity entry) leaves `Ref` empty: no sink needs one there, and
  nothing else changes.
- **The webhooks are unchanged.** `webhookBody` rebuilds the body from
  the entry's fields (`webhookPayload`, `notificationEntry`), which do not
  include `Ref`, so a webhook's bytes and its `X-Conductor-Event` stay
  what they are on `main`.
- **`extHost.OpenDecisionRefs`** (`internal/api/exthost.go`): the Host of
  a try given up refuses as its other calls do (`gone`); a switchyard or
  a server with no ledger answers `ErrUnsupported`; an `after` neither
  empty nor a ref `ParseDecisionRef` takes is `ErrRefused` (its text cut
  as `Decision` cuts it); otherwise it reads, within `h.bound(ctx)`,
  `ledger.OpenDecisionsAfter(ctx, n, ext.MaxOpenDecisionRefs)` and answers
  each `n` as `ext.DecisionRef(ledger.DecisionID(n))`, with `next` the
  last of them when more follow. A ledger error (its `ErrUnavailable`
  among them) is answered as it is, so the provider tries again later.
- **`ledger.OpenDecisionsAfter(ctx, after int64, limit int) (ns []int64,
  more bool, err error)`** (`internal/crew/ledger/decisions.go`): the
  open decisions' numbers greater than `after`, ascending, at most
  `limit` (at most `MaxOpenDecisionRefs`'s 256 as the caller passes it,
  0 for that), `more` when others follow. One statement, `SELECT n FROM
  decisions INDEXED BY decisions_open WHERE state = 'open' AND n > ?
  ORDER BY n LIMIT ?` (the limit plus one), over the open decisions'
  partial index `decisions_open` (`ON decisions(n) WHERE state =
  'open'`), which serves an ascending walk as it serves
  `OpenDecisionsNewest`'s descending one. It judges the index exactly as
  `OpenDecisionsNewest` does (the same check, shared as one helper):
  where the index cannot be made, or the snapshot holds none of that
  name or another definition, it answers `ErrUnavailable` and no number;
  a database older than the decision tables hands none. It reads the
  number alone: no request, policy or other column. **No schema change**:
  the read uses the index the ledger already makes, whatever its version
  when this row is built.
- **`hostExtHost.OpenDecisionRefs`** (`internal/hostagent/exthost.go`):
  `ErrUnsupported`, as its `Decision` (a host keeps no ledger), and
  `context.Canceled` once its try has ended.

Nothing is stored: `Ref` is a Go field the server sets in memory and a
sink is handed. No route, no `internal/proto` type and no web page
changes in part one.

### Part two: the shell, `mobile/` (OSS-1a)

`mobile/` sits beside `desktop/` and is built by its own CI jobs. It
holds the Capacitor project, the iOS app, two native plugins, the
bridge, and the scripts that copy the workbench's bundle in.

**Packages** (`mobile/package.json`, `mobile/package-lock.json`, each
pinned exactly, each with its reason in the PR and its row in
`docs/dependencies.md`): `@capacitor/core`, `@capacitor/cli` and
`@capacitor/ios` (the shell itself), `@capacitor/push-notifications`
(the platform's push token and the notification taps), `@capacitor/app`
(opened links, resume and pause), `@capacitor/browser` (the in-app
browser, for Open a server and a provider's sign-in page), all MIT; as
dev dependencies the versions `desktop/` already pins of `typescript`,
`vitest`, `@types/node` and `@playwright/test`. `scripts/notices.py`
gains `mobile/` as it has `desktop/` (the shipped packages, `npm ls
--omit=dev`, after `npm ci` in `mobile/`), so `make notices` and its
`--check` cover them; `AGENTS.md`'s notices bullet and `docs/checks.md`
say `npm ci` in `mobile/` beside `desktop/`.

**`mobile/capacitor.config.ts`**: the app id `dev.conductor.mobile` (the
development id: a packaging that signs its own build sets its own),
`webDir` the folder `make mobile-sync` fills (`mobile/www`, ignored by
git), and navigation inside the web view allowed to nothing but the
bundle (`server.allowNavigation` empty). The shell's own openings (Open a
server, a provider's sign-in page) go to the in-app browser through
`@capacitor/browser`; any other navigation away from the bundle (a link
tapped in a decision's context) is handed by Capacitor to the system
browser, outside the shell, and never loads in the shell's page.

**`mobile/ios/`**: the Xcode project Capacitor generates (with the Swift
Package Manager where the pinned Capacitor offers it, so the build needs
no Ruby), minimum iOS 16, committed as generated and then edited only as
below:

- The `AppDelegate` forwards the push token and its failure to the push
  plugin, as Capacitor's template says, and covers the window with a
  plain view in the background colour while the app is not active, so
  the app switcher's snapshot never holds a card.
- The app's bridge view controller (a `CAPBridgeViewController` subclass)
  registers the two plugins below (`capacitorDidLoad`) and answers every
  media capture request of the web view `.deny` (`WKUIDelegate`'s
  `requestMediaCapturePermissionFor`), so no page in the shell can open a
  microphone or a camera.
- `Info.plist`: no background mode (an alert notification needs none),
  `NSFaceIDUsageDescription` "Conductor asks for Face ID to confirm an
  answer you send." (without it iOS refuses Face ID to the app), and no
  microphone, camera, location or other usage description.
- No push entitlement, no associated domains and no signing in the
  repository's project: CI builds it unsigned for the simulator.
  Whoever builds for a device adds their own team, app id, push
  entitlement and, for links they want the shell opened with, their own
  associated domains; a provider's service needs them to reach the
  phone, and the shell works without them.
- `.gitignore` gains `mobile/www/` and `mobile/node_modules/`, as it holds
  the desktop shell's and the bundle's.

**`mobile/ios/App/Plugins/ConductorKey.swift`** (about 250 lines), the
native key plugin. It knows no service, no contract and no route.

- **One key**: an Ed25519 key (CryptoKit's `Curve25519.Signing`), kept
  raw as one Keychain item of its own with
  `kSecAttrAccessibleWhenUnlockedThisDeviceOnly` (the app has no
  background mode, so nothing signs while the phone is locked; never
  synced to another device, never in a backup). Its state is one of
  `none`, `unbound` (made, not yet bound to an id), `bound` (an id kept
  beside it) and `ending` (kept only to end what it was bound to).
- **Its calls**, each answering a code on refusal:
  - `create(until)`: only from `none`; refused `key_ending` while a key is
    `ending` and `key_exists` while one is `unbound` or `bound`, so a new
    sign-in never replaces a key whose end is unconfirmed. It makes the
    key `unbound` with `until` (the time after which an unbound key is
    removed) and answers the public key (32 bytes, base64url).
  - `publicKey()`, `state()` (the state, the id once bound, `until`).
  - `bind(id)`: `unbound` to `bound` with the id (1 to 128 bytes of
    printable ASCII).
  - `ending()`: `bound` to `ending`. `remove()`: the key and its id gone,
    state `none`.
  - `sign(request)`: below.
- **It signs only what a policy builds.** `sign` hands the request (a
  JSON object of at most 1 KiB, refused `too_large` past it), the key's
  state and its id to the native **signing policy**: an object of a
  class the packaging adds, exposed to the Objective-C runtime under the
  fixed name `ConductorKeySigningPolicy` (`@objc(ConductorKeySigningPolicy)`)
  and conforming to the plugin's `KeySigningPolicy` protocol, found once
  at start (`NSClassFromString`). The open-source build has none. The
  policy answers either a refusal (a code and words, passed back as
  they are) or a plan:

  ```swift
  public struct SigningPlan {
      public let message: Data          // the bytes to sign
      public let presence: Bool         // the person confirms first
      public let whileEnding: Bool      // an ending key may sign it
      public let finish: (Data) -> String // the signature (64 bytes) to the string returned
  }
  public protocol KeySigningPolicy: AnyObject {
      init()
      func plan(request: [String: Any], state: String, id: String?) -> Result<SigningPlan, KeyPolicyRefusal>
  }
  ```

  The plugin never signs bytes handed in from the web view, signs
  nothing without a policy (`no_policy`), signs with an `ending` key only
  a plan marked `whileEnding` (`key_ending` otherwise), and signs nothing
  from `none` or past an `unbound` key's `until`. Whenever a plan asks
  for presence, the plugin itself makes the check, for that one call: an
  `LAContext` evaluation of `.deviceOwnerAuthentication` (Face ID, Touch
  ID or the passcode) with the fixed reason "Confirm your answer in
  Conductor"; cancelled or failed, it answers `presence_refused` and signs
  nothing. The policy cannot sign: it only builds. The private key never
  leaves the plugin and never enters the web view.
- **At start, before anything else runs**, the plugin removes a key whose
  marker file under Application Support (`conductor/key.marker`, written
  with the key) is gone (an earlier install's: iOS keeps Keychain items
  after an app is deleted, and a new install's container is empty), and
  an `unbound` key past its `until`.

**`mobile/ios/App/Plugins/ConductorStore.swift`** (about 150 lines), the
shell's storage, the only place on the phone the shell keeps anything of
a decision or a draft:

- One file per record under Application Support, `conductor/<area>/`,
  each written to a `.new` file and renamed over the old one, so a write
  interrupted before its rename leaves the old record whole; the folder
  excluded from backups (`NSURLIsExcludedFromBackupKey`) and every file
  `NSFileProtectionComplete` (unreadable while the phone is locked).
- Three areas with their own bounds. **drafts**: at most 32 records of
  at most 12 KiB each, never evicted; a write past either bound is
  refused (`full`, `too_large`) and reported, never shown as kept.
  **cache**: at most 1 MiB in all, its oldest records evicted first to
  make room; a record past 1 MiB is refused. **servers**: the shell's
  own, Open a server's last five addresses (at most 2,048 bytes each),
  never evicted by the cache and never handed to a provider.
- Calls: `put(area, key, data)`, `get(area, key)` (the data and its
  `savedAt`), `list(area)` (keys with `savedAt`), `remove(area, key)` and
  `clear()`, which empties the drafts and cache areas and the shell's
  HTTP caches (`URLCache.shared`, and the web view's `WKWebsiteDataStore`
  data for the bundle's origin) and leaves the servers area: a
  provider's sign-out does not forget the person's own addresses, which
  Open a server's **Forget these addresses** empties. A key is 1 to 64
  of `a-z`, `0-9`, `-`, `_`, `.`. The store a provider is handed reaches
  the drafts and cache areas only.
- Neither Capacitor's Preferences (UserDefaults) nor the web view's own
  storage holds anything of a card or a draft in the shell.

**`mobile/src/bridge.ts`**, compiled by `tsc` into one classic script,
`mobile/www/shell/bridge.js`, which `make mobile-sync` loads in the
bundle's `index.html` before the workbench's entry; no bundler is added:
it reaches the plugins through the `Capacitor` global that Capacitor's
native runtime defines in the page before its scripts (`Capacitor.Plugins`;
if the pinned Capacitor no longer offers it, a bundler is a new
dependency with its reason). It builds, once:

```ts
window.conductorShell = Object.freeze({
  v: 1,
  platform,                       // 'ios' | 'android' | 'web' (no native runtime: a browser, as the boot spec runs it)
  push: { permission, request, onTap },   // onTap(cb): each tap's data, handed on unchanged
  links: { onOpen },              // onOpen(cb): each address the shell is opened with
  openServer,                     // openServer(address): { ok: true } | { ok: false, words }
  servers: { recent, forget },    // recent(): the last five addresses opened, newest first; forget(): none kept
  provider,                       // the provider's ShellProvider, or null
  register,                       // register(factory): a provider module's one call
})
```

and nothing else: no key, no store, no push token, no HTTP call.

- **A provider module** is any script a packaging places at
  `providers/index.js` in the shell's assets; `make mobile-sync` loads it
  after `bridge.js` and before the workbench when the file is there. It
  registers by calling `conductorShell.register(factory)` once; a second
  call, or a call after the workbench has started, is refused (a console
  line, the first factory kept). The shell calls `factory({key, store,
  push, http})` at once, handing the key plugin's whole interface
  (`create`, `publicKey`, `bind`, `sign`, `ending`, `remove`, `state`),
  the store, the platform's push token with its changes
  (`push.token()`, `push.onToken(cb)`), and the native HTTP call
  (Capacitor's, which the web view's content security policy does not
  govern; its request's body sent exactly as given, its reply's body
  handed over as its text exactly as received, never parsed, at most
  1 MiB, so a provider can verify a signature or a hash over the exact
  bytes; a reply that is not text refused `unreadable`) to that factory
  alone; what it returns is `provider` (`null`
  with no module, or a factory that throws, with a console line).
- **`openServer(address)`** opens the address in the in-app browser
  (`@capacitor/browser`): `https://`, or `http://` (as a Conductor server
  on the LAN serves by default, `internal/config`'s `:8080`), the latter
  answered with the words "This address is not encrypted: use it only on
  a network you trust"; every other scheme (`javascript:`, `file:`,
  `data:`, a custom one) and an address of more than 2,048 bytes or with
  a control character refused with words. An address opened is kept in
  the store's servers area as the five most recent (`servers.recent`).
- **The page's content security policy.** `make mobile-sync` writes into
  the copied `index.html` a `<meta http-equiv="Content-Security-Policy">`
  of `script-src 'self' 'wasm-unsafe-eval'` and the SHA-256 hash of each
  inline script the bundle's `index.html` holds, computed by
  `mobile/scripts/csp.mjs` from that file's own scripts, and
  `connect-src` bounding every connection a page opens: `'none'` by
  default, so a shell with no provider connects to nothing, or the https
  origins a provider's build names (`make mobile-sync
  MOBILE_CONNECT=https://…`; each exactly an https origin, anything else
  refused by the sync). A provider's own calls go through the native HTTP
  call, which the policy does not govern; the bound keeps the page itself
  from reaching a server's API whatever its code does. On `main`
  (built at `f10eb297`) those are three: the import map, the colour mode
  and the runtime configuration; the bundle's JSON data block is not a
  script the browser runs and takes no hash. `csp.mjs` fails the sync on
  what a hash cannot admit in `index.html`: an inline event handler
  attribute or a `javascript:` address.
  - **`'wasm-unsafe-eval'`, and why.** DR1's card draws a decision's
    context through `MarkdownPreview.vue`, which highlights a fenced code
    block with shiki's default engine, compiled from WebAssembly
    (`WebAssembly.instantiate`); without this source the web view refuses
    the compile, every code block stays plain and a violation is
    reported. It admits compiling WebAssembly only: it does not admit
    `eval`, `new Function` or a string timer, which the policy refuses
    (no `'unsafe-eval'`), and WebKit has honoured it since iOS 16, the
    shell's minimum.
  - **No text scan for `eval`.** The bundle holds the word in text that
    no page of the shell runs (Monaco's TypeScript worker carries the
    declaration `declare function eval(x: string): any;`), and the
    shell's pages load no Monaco worker. Whether the shell's pages need
    anything the policy refuses is checked by running them under it:
    the boot checks open the shell's screens and a decision with a code
    block and a diagram under the real policy and fail on any
    `securitypolicyviolation` (Tests).
  - **What else runs.** Mermaid's diagrams are drawn in MarkdownPreview's
    sandboxed frame (`utils/mermaidFrame.ts`), whose `srcdoc` inherits
    the shell's policy beside its own and loads its one script by the
    app's own address, which `'self'` admits. Styles are not restricted
    by the policy.
  DR1's and MB0's components draw a decision's words as text, or as
  Markdown that `MarkdownPreview.vue` sanitises (raw HTML parsed as text,
  scripted links refused), so an agent's words cannot become a script.
  A script in the web view could still reach Capacitor's plugin
  registry; what it would get is what the policy builds, and a
  signature marked for presence only behind the person's Face ID or
  passcode, which is why the plugin makes the presence check and not the
  bridge or the provider.

**`Makefile`**: `mobile-install` (`npm ci` in `mobile/`), `mobile-sync`
(the bundle copied from `internal/web/dist` into `mobile/www`, the
`bridge.js` and provider script tags and the policy written into its
`index.html` by `csp.mjs`, then `npx cap sync`; needs `make web-build`
first), `mobile-test` (`tsc --noEmit`, vitest and, from OSS-1b, the boot
spec), `mobile-ios` (an unsigned simulator build, `xcodebuild -sdk
iphonesimulator CODE_SIGNING_ALLOWED=NO`, then `xcodebuild test` of the
plugins' XCTest cases and, from OSS-1b, the boot UI test, on a
simulator).

**`.github/workflows/ci.yml`**: a `mobile` job (ubuntu: Node 22, `make
web-build`, then `make mobile-install mobile-sync mobile-test`) and a
`mobile-ios` job (macOS: the same sync, then `make mobile-ios`), both
under `required-checks`. Neither signs or uploads anything.

**`docs/repository-map.md`**: the `mobile/` row beside `desktop/`.

### Part two: the bridge's types and the shell's screens, `web/app` (OSS-1b)

**`web/app/utils/shell.ts`**: the bridge's types and its reader,
`shellBridge()`: `null` outside a shell; a bridge whose `v` is not 1
ignored, with one console line. **Bridge v1 is complete here**: OSS-1b
lands every type, `ShellInbox`, `ShellItem`, `ShellDrafts` and
`ShellDraft` included, so a provider and its fakes implement the whole
interface from the start, and OSS-2b adds behaviour, never a type; `v`
stays 1. It names nothing of any service.

```ts
interface ConductorShell {
  v: 1
  platform: 'ios' | 'android' | 'web'
  push: {
    permission(): Promise<'granted' | 'denied' | 'prompt'>
    request(): Promise<'granted' | 'denied'>
    onTap(cb: (data: Record<string, string>) => void): () => void
  }
  links: { onOpen(cb: (url: string) => void): () => void }
  openServer(address: string): Promise<{ ok: true; words?: string } | { ok: false; words: string }>
  servers: { recent(): Promise<string[]>; forget(): Promise<void> }   // at most 5, newest first
  provider: ShellProvider | null
  register(factory: unknown): void               // a provider module's; the workbench never calls it
}

/** A refusal any provider call may reject with: a code the shell knows, or the provider's own, and its words. */
interface ShellRefusal { code: string; words: string }

interface ShellProvider {
  name: string                                   // one line, at most 40 characters
  state(): Promise<ShellState>
  onState(cb: (s: ShellState) => void): () => void
  signIn(): Promise<ShellSignIn>
  cancelSignIn(): Promise<void>
  signOut(): Promise<void>                       // takes effect on the phone at once; ends what it can when it can (ending)
  enableNotifications(): Promise<'on' | 'denied'>
  test(): Promise<{ words: string }>             // a test notification to this phone
  route(url: string): Promise<string | null>     // an opened link to an item's id, or null when it is not the provider's
  inbox: ShellInbox                              // behaviour in OSS-2b
  drafts: ShellDrafts                            // behaviour in OSS-2b
}

interface ShellState {
  signedIn: boolean
  who?: string                                   // at most 80 characters
  notifications: 'on' | 'off' | 'denied' | 'unknown'
  lastNotifiedAt?: string                        // RFC 3339
  ending?: boolean                               // signed out here, its end not yet confirmed
  words?: string                                 // at most 500: what the provider says of its state (cannot be reached, cannot answer now)
}

type ShellSignIn =
  | { code: string; page: string; expiresAt: string }   // code at most 16 characters; page https://, at most 200 bytes
  | { words: string }                                    // why no sign-in can begin now

interface ShellDrafts {                          // the current sign-in's drafts, kept by the provider
  get(id: string): Promise<ShellDraft | null>
  list(): Promise<ShellDraft[]>                  // at most 32
  put(d: ShellDraft): Promise<{ ok: true } | { ok: false; code: 'full' | 'too_large' | 'not_saved' | 'signed_out'; words: string }>
  remove(id: string): Promise<void>
}

interface ShellDraft {                           // at most 12 KiB as JSON
  id: string                                     // the item's id
  responseId: string                             // minted once, when the draft begins
  rev: number                                    // the decision's rev it was written against
  choice?: number
  text?: string
  rationale?: string
  state: 'editing' | 'waiting' | 'unknown'
  h?: string                                     // set by the provider with state 'unknown', before a send begins: what it sends, hashed in its own form
  savedAt: string
}

interface ShellInbox {
  list(after?: string): Promise<{ items: ShellItem[]; next?: string; asOf: string }>
  item(id: string): Promise<ShellItem>           // rejects ShellRefusal: not_visible, unreachable, offline
  respond(id: string, a: { responseId: string; expectedRev: number; choice?: number; text?: string; rationale?: string }):
    Promise<{ item?: ShellItem; outcome: 'kept' | 'same' | 'refused' | 'unknown'; code?: string; words?: string }>
  answerOf(id: string, responseId: string):      // this person's answer under that responseId, in any state
    Promise<{ state: string; words: string; h: string } | { state: 'none' } | { code: 'unreachable'; words: string }>
  resolve(handle: string): Promise<{ id: string } | { code: 'unknown_handle' | 'unreachable'; words: string }>
  watch(cb: (id: string) => void): () => void    // while the app is in the foreground only
}

interface ShellItem {
  id: string                                     // the provider's id of the item, at most 64 bytes
  decision: Decision                             // DR1's record (utils/decisions.ts) as the provider holds it: history, comments and evidence contents may be absent
  responsesOmitted?: number                      // responses the provider holds and does not hand
  rows: DeliveryStep[]                           // MB0's four steps (utils/inbox.ts), exactly as given
  own?: { responseId: string; state: string; words: string }   // this person's latest answer, if any
  owner: { name: string; reportedAt?: string }   // the server that holds the decision, as the provider last heard from it
  asOf: string
  offline: boolean                               // drawn from what the provider kept, not read now
  elsewhere?: string                             // words for where it is answered when the provider takes no answer from the phone: no Answer is drawn
}
```

A draft belongs to the sign-in it was written in: a provider stamps it
with its sign-in, `get` and `list` answer only the current one's, a `put`
after a sign-out answers `signed_out`, and a sign-out removes every
draft. Its `state` is in the draft, so a restart finds an answer waiting
to send still waiting and a send that began, whatever became of it,
locked until `answerOf` settles it.

Every string a provider hands the screens is bounded where `shell.ts`
reads it (`name` 40 characters, `who` 80, `words` and every `words` of a
reply or refusal 500, a code 16, a sign-in page `https://` of at most 200
bytes, a refusal's `code` 32 bytes of `a-z` and `_`), cut or refused
there, and drawn as text, never as markup. A `ShellItem`'s `decision` is
read as the decision page reads a server's record.

**`web/app/middleware/shell.global.ts`**: inside a shell (`shellBridge()`
not null), every address but `/shell` and its children goes to `/shell`,
so nothing of the workbench's layout, sidebar, token dialog or event
stream is set up; outside a shell, `/shell` and its children go to `/`.
It is synchronous over the bridge, as `decide.global.ts` is over its
stores.

**`web/app/layouts/shell.vue`** and **`web/app/pages/shell/index.vue`,
`signin.vue`, `settings.vue`**:

- **No provider**: `/shell` is Open a server, the whole first screen.
- **With a provider**: `/shell` is the sign-in screen while it reports
  `signedIn` false, and the inbox (OSS-2b) once signed in, with Settings
  a tap away. **Sign in**: "Sign in to <name>", the code in large type,
  **Open the sign-in page** (the page in the in-app browser), the time
  left, Cancel; a `{words}` answer shows its words and **Try again**.
  **Settings**: who, notifications with their state and the last one
  received, **Turn on notifications** (`enableNotifications`), **Send a
  test notification** (shows `test()`'s words), **Sign out**, and while
  `ending`: "Signed out on this phone; <name> will be told when it can be
  reached".
- **Open a server** is a link on the sign-in screen and a row of
  Settings in every state the provider reports (signed out, signed in,
  with its words that it cannot be reached, with its words that it
  cannot answer now), and it never calls the provider: the field, Open,
  the words of `openServer`'s answer, the last five addresses, and
  **Forget these addresses**.
- The layout follows the system text size (`font: -apple-system-body`
  and relative units), keeps 44 px targets, labels every control for
  VoiceOver, uses Nuxt UI and the brand palette, and passes the design
  rules (`designRules.test.ts`) and the contrast walk as they are.

**The checks that start the bundle** land with OSS-1b, which brings
the screens they look for: `mobile/e2e/boot.spec.ts` and the XCUITest
`ShellBootsUnderItsPolicyTests`, added to `mobile-test` and to `make
mobile-ios`. **`docs/repository-map.md`**: `web/app/utils/shell.ts`, the
shell's layout, middleware and pages.

### Part two: the decision page from a shell's inbox (OSS-2b)

**The page through the seams the components already have, and one they
gain.**

- **Evidence: `REF_ACCESS`, as the join page.** On `main`, MB0's join
  page draws DR1's `DecisionCard.vue` from a record that is not the
  server's routes and provides `REF_ACCESS` (`utils/refAccess.ts`) so its
  chips and receipts read through the link. The shell's decision page
  provides its own `shellRefAccess()`: `route` answers `{ words: 'open it
  on the desktop' }` for every ref, `read` answers `null`, and `runHeld`
  answers `undefined` (not known), so no chip, no receipt and no
  `useDecisionRunInfo` calls `useApi` in a shell where no server answers.
- **The answer: one seam in `DecisionRespond.vue`.** On `main`,
  `DecisionRespond.vue` posts its answer and its comment through `useApi`
  itself (`respondDecision`, `commentDecision`), and the join page draws
  no controls (a link cannot answer a DR1 record yet). OSS-2b gives it
  one injected seam, `DECISION_ANSWERING` (`utils/answering.ts`):

  ```ts
  interface DecisionAnswering {
    /** What it carries: absent, every control. A control it does not carry is drawn disabled with these words. */
    supports?: { respond?: true | string; comment?: true | string; more_info?: true | string }
    /** An answer sent, waiting, refused or not known: the component's success path runs on 'kept' with a receipt alone. */
    respond(id: string, body: ResponseBody): Promise<{ outcome: 'kept' | 'refused' | 'waiting' | 'unknown'; receipt?: ResponseReceipt; decision?: Decision; words?: string }>
    comment(id: string, c: { kind: 'comment' | 'more_info'; text: string; expectedRev: number }): Promise<{ receipt: OpReceipt; decision: Decision }>
    /** The answer as typed, kept and restored by the page that provides it: the choice, the text and the rationale. */
    draft?: { initial(id: string): { choice?: number; text?: string; rationale?: string } | null; changed(id: string, f: { choice?: number; text?: string; rationale?: string }): void }
    /** Reactive: the answer's lock, its words and the actions it offers; null while the answer may be edited and sent. */
    state?(id: string): { lock: boolean; words?: string; actions?: ('cancel' | 'confirm')[] } | null
    cancel?(id: string): void    // the 'cancel' action: a waiting answer back to editing
    confirm?(id: string): void   // the 'confirm' action: a waiting answer, or one that did not arrive, sent through checking
  }
  ```

  With nothing injected, the answering is today's: the server's routes
  through `useApi` (a success answered `kept` with its receipt, a
  refusal as today), every control carried, no draft and no state, so
  the operator's decision page, the run's card and MB0's pages are
  unchanged. `DecisionRespond.vue` gains these rules and no others:
  - a control the provided answering does not carry is drawn disabled
    with its words, as the component already disables Ask for more when
    the asker is not live;
  - it clears its pending choice, picked option, text and rationale, and
    calls `take`, only on `outcome: 'kept'` with a receipt; any other
    outcome keeps the content as typed, shows the reply's words, and
    calls no `draft.changed`;
  - while `state(id)` holds `lock`, its choices, text, rationale, Back
    and Answer are disabled and it calls no `draft.changed`; it draws the
    state's words, and Cancel and **Confirm to send your answer** when
    `actions` names them, calling `cancel(id)` and `confirm(id)`. A page
    opened on a draft that `initial` restores while `state` reports a
    lock (a restart in `waiting` or `unknown`) is drawn locked from the
    first frame.
  `DecisionCard.vue` is not edited.
- **The shell's answering**, `utils/transport/shell.ts`, beside
  `utils/transport/decide.ts`: `respond` hands the body to the answer's
  state machine (`answer.ts`, below), which calls `ShellInbox.respond`
  field for field with the draft's `responseId` (never the one
  `responseBody` mints) and the draft's `rev`, which is always the rev
  last drawn to the person (a draft is rebased when a changed decision
  is drawn, Events); it answers the machine's outcome (`kept`, `refused`,
  `waiting`, `unknown`) and words. `state`, `cancel` and `confirm` are
  the machine's for the item (lock in `checking`, `sending`, `waiting`
  and `unknown`). `supports` carries `respond` alone and disables Comment
  and Ask for more with "Comment on the desktop: this phone sends
  answers, not comments". `draft` reads and writes `ShellDrafts`.
- **What a provider's record may lack.** A `ShellItem.decision` may hold
  no history rows, no comments and no evidence contents. The card draws
  those as DR1's card draws a record that has none yet (no history
  section, no comments); `responsesOmitted` is a count line the shell's
  page draws under the card ("3 earlier answers are not shown here");
  evidence chips show their ref and the `REF_ACCESS` words.

**`web/app/pages/shell/decisions/[id].vue`** draws, for one item: the
offline line when `offline` ("As of 14:02, offline"), DR1's `DecisionCard`
from `item.decision`, MB0's `InboxDelivery` with `item.rows` exactly as
given (the page derives no row of its own, so the four stay separate and
*Work resumed* is never shown before the provider's row says it), the
owner's name and when it last reported, then `DecisionRespond` under the
shell's answering only when the item has no `elsewhere`; with
`elsewhere` its words and no Answer. **The inbox screen**
(`pages/shell/index.vue`, signed in) lists the items as `list` gives them,
in its order, each as the join page's list draws a row (`INBOX_ROW_CLASS`,
`rowLine`, the decision's kind and "Answered, not yet applied" while its
rows say so), with "As of 14:02" and the offline line; it refreshes on
`watch` while the app is in the foreground and stops in the background
(no connection is held open by the shell).

**Drafts and their revalidation** (`web/app/utils/drafts.ts`,
`web/app/utils/answer.ts`):

- An answer being written is a draft kept through `ShellDrafts` (in a
  shell, the provider's, over the store's drafts area; outside one,
  `drafts.ts`'s implementation of the same interface over the browser's
  local storage, under the same bounds) under the item's id, with the
  `rev` it was written against and one `responseId` minted when the
  draft begins, for at most 7 days (a draft older is removed at read),
  removed when its answer is kept or the provider signs out.
- **A draft is rebased when a changed decision is drawn.** When Answer
  finds the decision at another `rev` (or a refused send returns it at
  one), the page draws the decision as it is now with "This decision
  changed after you wrote your answer: read it again", and the draft is
  stored again at the drawn `rev` with a new `responseId` and its content
  kept, state `editing`: the next Answer is checked against the rev the
  person has now read. A new `responseId`, because the old one may
  already have reached the provider with a refused send. A `put` refused
  then leaves the draft as it was and shows its words.
- A draft whose `put` was refused shows the refusal's words ("Not saved
  on this phone") and is never shown as kept or waiting.
- What **Answer** does and what the screen shows after every step is the
  answer's state machine (Events), which `answer.ts` implements over
  `ShellInbox` and `ShellDrafts` and which names no service. An outcome
  not known locks the draft until `answerOf`, the person's answer read
  by its `responseId`, shows it arrived or not: `own` is the person's
  latest answer, which may be an earlier one or another phone's, so only
  that read says whether this submission arrived.
- The phone does not withdraw an answer once sent: withdrawing, where a
  provider offers it, is done on its own pages.

**The routes into the page**:

- A notification tap (`push.onTap`, data `h`) goes to
  `/shell/open?h=<handle>`, then `inbox.resolve(handle)`, then the
  item. A tap whose data has no `h`, or an `h` of more than 256 bytes,
  opens the inbox.
- A link the shell is opened with (`links.onOpen`) goes to
  `provider.route(url)`, then the item; a link the provider does not
  know (`null`) opens the inbox, and with no provider, Open a server with
  the address filled in and not opened.
- Each failure has its own words, over the inbox: `unknown_handle` "This
  notification is no longer current: here is what is waiting";
  `unreachable` "<name> cannot be reached: here is the last inbox, as of
  14:02" (the inbox as the provider last listed it, with `offline`); an
  item the person may not see (`not_visible`) "This decision is not one
  you can see here".

**`docs/features.md`**: the phone shell's entry, with the last code row
of the first platform: what it does with no provider (Open a server),
what a provider adds (sign-in, notifications, the inbox, answering with
Face ID or the passcode), how it is built (`make mobile-*`, unsigned,
anyone's own signing for a device), naming no provider.

### Part two: the second platform (OSS-3, a later row)

`mobile/android/`, `ConductorKey.kt` (an Ed25519 key in the Android
Keystore from Android 13; before it, an Ed25519 key sealed by a Keystore
AES-GCM key, since the platform's Keystore has no Ed25519 there; the same
states, calls, policy rule and presence check through `BiometricPrompt`
with device credential allowed), the store's Android side (the app's
no-backup files directory, the same areas and bounds), the platform's
push token and taps through the same push plugin, a `mobile-android` CI
job (a debug build and an emulator start), `docs/features.md`'s line.
The bridge, the screens and every provider interface are unchanged.

## Events, orderings and invariants

### Part one

- **Order.** `decisionTold` runs under the ledger's lock once a
  transition commits, in commit order: for each change, the decision
  event, then the notification (its `Ref` set by `decisionNotification`
  and checked by `notifier.decision` before any queue), then the router.
  `Ref` adds no step and no wait.
- **One form.** Every `Ref` a sink is handed is empty or one
  `ext.ParseDecisionRef` takes: the server's own are checked before their
  queue, a provider's notice by `Valid` before `Host.Notify` takes it.
- **The webhooks' bytes.** A webhook's body is `webhookPayload` rebuilt
  from the entry's fields, which hold no `Ref`.
- **The refs' read.** `OpenDecisionRefs` reads one snapshot per page,
  holds nothing across pages, and hands numbers alone: no request, policy
  or state but open. A walk over pages is a set of moments, not one: a
  sink that needs a decision's state now reads it with `Host.Decision`.

### The shell

- **Start.** The key plugin's start-up removals run before any web view
  script; `bridge.js` builds `window.conductorShell` before the provider
  module runs; the provider module calls `register` before the
  workbench's entry runs; the workbench reads `shellBridge()` once, at
  its start, and the middleware keeps it on `/shell`.
- **What the bridge holds.** The workbench can reach the provider's
  interface and nothing the factory was handed: no key, store, token or
  HTTP call.
- **A write.** Each record of the store is written to `.new` and renamed
  over the old one; a crash leaves the old or the new, never part.
- **Sign-out.** The provider clears its drafts and its cache areas and
  the HTTP caches (`store.clear()`) before it answers `signOut`, offline
  too; its end of what the key was bound to follows when it can (`ending`).

### On the phone: an answer, from the tap to the record

One item on one phone. The states are the draft's (`ShellDraft.state`, so
a restart resumes them) and the screen's; `answer.ts` implements the
table over `ShellInbox` and `ShellDrafts`, and a provider maps its
service's answers onto these events. Every state begins from a read made
at that moment; the rows past `kept` are the provider's, shown and never
derived.

| From | Event | To | What the phone shows | Test |
|---|---|---|---|---|
| (none) | tapped: `/shell/open?h=` | `reading` | the phone's own unlock, then the item read now (`resolve`, then `item`) | `shell-decisions.spec.ts` |
| (none) | the handle unknown; the item not one the person may see; the provider unreachable | the inbox | each with its words (Routes) | `shell-decisions.spec.ts` |
| `reading` | the person picks, types or dictates | `editing` | the draft kept (`ShellDrafts.put`); a refused `put` shows "Not saved on this phone" and the draft is never shown as kept | `drafts.test.ts` |
| `editing` | Answer | `checking` | Answer disabled, "Checking the decision"; the item read again | `answer.test.ts` |
| `checking`, `sending` | Answer again (a second tap) | the same | nothing new: the press is ignored, and any send carries the draft's one `responseId` | `answer.test.ts`, `shell-decisions.spec.ts` |
| `checking` | the read: open at the draft's `rev`, its *Response recorded* row not done by another answer, no `elsewhere` | `sending` | "Sending"; the provider's respond asks the key plugin, and the system's sheet asks "Confirm your answer in Conductor" | `answer.test.ts` |
| `checking` | the read: the `rev` changed, or the decision superseded | `editing` | the decision as it is now, "This decision changed after you wrote your answer: read it again", a superseded one linking to its successor; nothing sent | `answer.test.ts` |
| `editing`, after that row | the changed decision drawn at its new `rev` | `editing` | the draft stored again at the drawn `rev` with a new `responseId` and its content kept (a rebase, Drafts); the next Answer is checked against that `rev`; a refused `put` leaves the draft as it was, with its words | `answer.test.ts` |
| `checking` | the read: *Response recorded* done by another answer (not `own`'s, or `own` of another `responseId`), or the decision not open | `editing`, Answer disabled while it holds | that row's words, or the record's words (DR1's `decisionWords`); the draft kept | `answer.test.ts` |
| `checking` | the read: the item has `elsewhere` | `editing`, no Answer | the item's `elsewhere` words; the draft kept, nothing sent | `answer.test.ts` |
| `checking` | the read fails `offline` or `unreachable` | `waiting` | "Not sent: you are offline. It will be checked and sent when you are back online, if the decision is still as you read it" (or "<name> cannot be reached" in its place), and Cancel | `answer.test.ts` |
| `waiting` | the app in the foreground and online (resume, or the browser's `online`) | `waiting`, Confirm offered | "Back online: confirm to send your answer", **Confirm to send your answer** and Cancel (`state` actions `confirm`, `cancel`) | `answer.test.ts` |
| `waiting` | Confirm | `checking` | as `checking`; nothing is sent without this press after a wait | `answer.test.ts` |
| `waiting` | Cancel | `editing` | the draft, no longer waiting | `answer.test.ts` |
| `sending` | `refused`, code `presence_refused` | `editing`, or `waiting` for a send begun from `waiting` | "Not sent: confirm with Face ID or the passcode to answer"; nothing signed or sent; a send begun from `waiting` waits again with Confirm offered | `answer.test.ts`, `ConductorKeyTests` |
| `sending` | `refused`, code `not_saved` (the provider could not store the draft `unknown` before its send) | `editing` | "Not saved on this phone: nothing was sent" | `answer.test.ts` |
| `sending` | `kept` or `same` | `kept` | the returned item's rows, and a `kept` reply's words above them when it has any; the draft removed | `answer.test.ts` |
| `sending` | `refused`, any other code | `editing` | the reply's words, and its item (when it has one) judged again as at `checking` (a changed `rev` rebases the draft); the draft stored `editing` again | `answer.test.ts` |
| `sending` | `unknown` (a timeout, a lost reply) | `unknown` | "Checking whether your answer arrived"; the draft, stored `unknown` by the provider before its send, locked: not editable, not sendable | `answer.test.ts` |
| a restart | the draft found `unknown` | `unknown` | "Checking whether your answer arrived", then `answerOf` | `answer.test.ts` (a crash after the provider's store and before its request; and after its service kept the answer, before the reply) |
| `unknown` | `answerOf` finds it, with the draft's `h` | `kept` | its rows; the draft removed | `answer.test.ts` |
| `unknown` | `answerOf` says `none` | `unknown`, Confirm offered | "Your answer did not arrive: confirm to send it again"; the content stays locked; **Confirm to send your answer** (`state` action `confirm`) goes through `checking` with the same `responseId` and content; a change found there rebases the draft (a new `responseId`, the content kept) and nothing is sent | `answer.test.ts` |
| `unknown` | `answerOf` finds it with another `h` (no path makes one: the draft was locked) | `kept` | its rows and "An earlier answer from this phone was kept"; the draft removed | `answer.test.ts` |
| `unknown` | `answerOf` unreachable | `unknown` | still locked, asked again at each resume | `answer.test.ts` |
| `kept` | the rows move (`watch` in the foreground, or a read) | `kept` | the rows as given; a refusal after it with its words, and Answer again while the decision is open (a new draft) | `shell-decisions.spec.ts` |
| any | the provider's state `signedIn` false | signed out | the sign-in screen with the provider's words; no card or draft drawn | `shell.spec.ts` |

Its invariants, each a property `answer.test.ts` checks over every row:

- **A1, one `responseId` per draft, and only the draft's content under
  it.** A tap, a retry, a send again after `none` and a send after
  waiting carry the draft's one `responseId`, and no edit is possible
  once a send has begun and not settled (the answering's `state` holds
  `lock` in `checking`, `sending`, `waiting` and `unknown`, and the
  component disables its inputs and writes no draft while it does). A
  rebase begins a new `responseId` for content the person may edit
  again; nothing sent under the old one is sent again under the new
  one without a fresh check. The provider's `respond`
  contract: after the person's confirmation and before anything leaves
  the phone, it stores the draft `unknown` with its `h`, and if that
  store fails it sends nothing and answers `not_saved`; so a crash before
  or after its service keeps the answer restarts locked, and only
  `answerOf` unlocks it.
- **A2, nothing is sent without a read in the same press**, and no
  signature for an answer without the key plugin's presence check when
  the policy asks for it.
- **A3, the phone shows the provider's rows and derives none.**
- **A4, a reply belongs to its sign-in**: after a sign-out, nothing
  started before it writes a card, a draft or a state (the provider's
  drafts are stamped with their sign-in, and the page drops a reply for
  an item it no longer shows).

### On the phone: the cases the shell draws

| Case | What decides | What the person sees | Test |
|---|---|---|---|
| A second tap on Answer | the draft's one `responseId` | one answer, its rows | `answer.test.ts`, `shell-decisions.spec.ts` |
| Another person answered first | the item's *Response recorded* row, or a `refused` reply naming it | that row's words; the draft kept, not sent | `answer.test.ts`, `shell-decisions.spec.ts` |
| The decision changed while a draft was open | the item's `rev` against the draft's | the decision as it is now, "…read it again"; the draft kept | `answer.test.ts`, `shell-decisions.spec.ts` |
| A decision the provider takes no answer for on the phone | the item's `elsewhere` | the card whole, its words, no Answer | `answer.test.ts`, `shell-decisions.spec.ts` |
| The phone offline, then back | the read's `offline`, the resume | cards "as of 14:02, offline"; an answer waiting, Confirm offered on return, then checked and sent once after the confirmation | `answer.test.ts`, `shell-decisions.spec.ts` |
| Face ID or the passcode cancelled | the key plugin's presence check | "Not sent: confirm with Face ID or the passcode to answer"; the draft kept | `ConductorKeyTests`, `answer.test.ts` |
| A send whose outcome is not known | `answerOf` | "Checking whether your answer arrived"; locked until the read | `answer.test.ts` |
| A notification no longer current | `resolve`'s `unknown_handle` | the inbox, "This notification is no longer current: here is what is waiting" | `shell-decisions.spec.ts` |
| A notification tapped with the provider unreachable | `resolve`'s `unreachable` | the last inbox, "<name> cannot be reached: here is the last inbox, as of 14:02" | `shell-decisions.spec.ts` |
| Signed out, or the provider's sign-in ended | `ShellState.signedIn` | the sign-in screen with its words; Open a server still there | `shell.spec.ts` |
| No provider at all | the bridge's `provider` null | Open a server, and nothing else | `shell.spec.ts`, `boot.spec.ts`, `ShellBootsUnderItsPolicyTests` |

### What the phone holds, and what empties it

- **The key**: a Keychain item, this device only, while unlocked, never
  synced or backed up; removed by `remove()` (the provider's, once what
  it was bound to has ended), and at the start of a reinstalled app (its
  marker gone); `ending` until then, signing only what the policy marks
  for ending, and no new key made meanwhile.
- **The store's drafts and cache areas**: under Application Support,
  excluded from backups, `NSFileProtectionComplete`; drafts at most 7
  days old; emptied by `clear()`.
- **HTTP caches**: `URLCache.shared` and the web view's data store for the
  bundle's origin, emptied by `clear()`.
- **The app switcher's snapshot**: a plain cover while the app is not
  active, so it never holds a card.
- **The push token**: the platform's, held by the push plugin in memory
  and handed to the provider; nothing of it in the store.
- **The servers area**: Open a server's last five addresses, the
  shell's own, never handed to a provider; kept through a provider's
  sign-out, emptied by **Forget these addresses**.
- **The web view's own storage**: nothing of a card or a draft in a shell.

### Protocol

No `internal/proto` frame, `web/app/utils/protocol.ts` row or
`docs/protocol.md` row changes: the shell holds no connection to a
Conductor server of its own (Open a server is the in-app browser, a page
of that server), the bridge is a JavaScript interface inside the app,
versioned `v: 1` and documented here and in `docs/repository-map.md`, and
`ext.Notification.Ref` is not sent to browsers or webhooks. No schema of
the ledger or of Dispatch's store changes.

## Out of scope

- The provider, its service, its sign-in, how it reaches a phone, what a
  notification it sends shows, and anything it stores: a provider's own.
- Publishing a build of the shell. This repository publishes no binary;
  CI builds it unsigned as a check, and whoever installs it on a device
  signs it under their own team and app id.
- Notification actions (answering from a lock screen), rich
  notifications and notification service extensions: every decision
  opens its complete card (Owner decisions, 3).
- Web push and an installable web app (MB0's plan leaves them out too).
- A ref for anything but a durable decision; a list of decisions in one
  ref.
- Withdrawing an answer from the phone.
- Proof to a service that an answer was confirmed with Face ID or the
  passcode (a Secure Enclave key, an attestation): the key is Ed25519,
  which the Secure Enclave does not offer (Owner decisions, 5).
- The microphone and the camera in the shell: refused until a later row
  with its own plan (Owner decisions, 9).
- The workbench's other screens in the shell (sessions, terminals, the
  chat): issue #160's remaining part. The shell opens a server's own
  page in the in-app browser.
- iPad and Mac Catalyst layouts beyond what the responsive page gives;
  more than one provider or one sign-in per install.
- A badge count from a service: a provider may set the badge from the
  inbox it reads.
- Android in OSS-1a and OSS-1b: OSS-3, a later row.

## Tests per surface

Every timed thing is driven through a seam, never waited for: the
notifier's `decisionRefOf`, the ledger read held by its test, the fake
native layer's calls released by the test, vitest's fake clock, a fake
provider whose replies the test releases, and Playwright's injected
bridge.

**Go race, OSS-2a** (`go test -race -count=1` of the packages, with
`make lint`, `make lint-static`; `internal/api`'s new tests and every test
of the files changed):

- `ext`: `TestDecisionRefForm` (`decision:dr-7` and
  `decision:dr-999999999999999999` taken, with their ids; `decision:7`,
  `decision:dr-07`, `decision:dr-0`, `decision:dr-1234567890123456789`
  (19 digits), `decisions:dr-2,dr-7`, `decision:dr-7 ` with a space,
  `question:x/y`, an empty one and two lines refused; `DecisionRef` and
  `ParseDecisionRef` round trip); `TestNotificationRefValid` (a notice
  with no ref and with a valid one taken, with each refused form refused
  and the field named); `TestNotificationRefOmittedFromJSON` (a
  notification with no ref marshals as it did, byte for byte).
- `ext/exttest`: `TestSinkCheckHandsADecisionWithItsRef` (a sink listing
  `decision`, and one taking every entry, is handed a notification typed
  as the server types it, with `Ref` `decision:dr-1`);
  `TestFakeHostOpenDecisionRefs` (a malformed `after` refused
  `ErrRefused`; what `OnOpenDecisionRefs` set answered; none by default;
  the call recorded).
- `internal/api`: `TestDecisionNotificationNamesItsRecord` (a decision
  opened, answered and closed: each notification a fake sink takes has
  `Ref` the decision's ref and its state; nothing else of it changed);
  `TestAttentionEntryHasNoRef` (a session's question reaches the fake
  sink with no ref); `TestServerNotificationRefChecked` (a malformed ref
  built through `decisionRefOf` reaches no sink's queue and no webhook's:
  the notification is delivered with `Ref` empty, and one error line
  holds its length and not its text); `TestWebhookBodyUnchangedByRef` (a
  webhook listing `decision` receives the bytes and event header a
  notification without `Ref` gives); `TestOpenDecisionRefsPagesEveryOpenRecord`
  (300 open decisions and 20 closed among them: two pages of 256 and 44
  in id order, `next` the 256th's ref then "", every open one once, no
  closed one, nothing but refs); `TestOpenDecisionRefsRefusals` (an
  `after` that is no ref `ErrRefused`; a switchyard `ErrUnsupported`; a
  Host of a try given up refuses as its other calls; a ledger without its
  open index answers its `ErrUnavailable`).
- `internal/crew/ledger`: `TestOpenDecisionsAfter` (ascending from
  `after`, `limit` and `more`, closed ones absent, the number alone read;
  no index, or another index of the name, `ErrUnavailable` with none
  handed; a database older than the decision tables none).
- `internal/hostagent`: `TestHostExtHostKeepsNoLedger` (new: the host's
  `ext.Host` answers `Decision` and `OpenDecisionRefs` `ErrUnsupported`,
  then `context.Canceled` once its try has ended).
- `internal/brakeman`: `TestDecisionRefIsBrakemansForm` (over a table of
  refs, `ext.ParseDecisionRef` takes exactly those `ParseRef` reads as a
  decision's, and `DecisionRef` writes what `Ref.String` writes).

**The shell's own, OSS-1a** (`make mobile-test`; vitest in `mobile/test`
over a fake native layer):

- `mobile/test/bridge.test.ts`: the bridge built before a provider
  module and the workbench; `register` once only (a second call refused
  and the first factory kept; a call after the workbench started
  refused); the factory alone handed the key, the store, the token and
  HTTP; `window.conductorShell` holding none of them (every property of
  the bridge walked, deeply, and no module under `web/app` imports
  anything of `mobile/`); a tap's data handed on unchanged; `openServer`
  taking `https://` and `http://` (the latter with its words) and
  refusing `javascript:`, `file:`, `data:`, a custom scheme, an address
  past 2,048 bytes and one with a control character; `servers.recent`
  the last five, newest first, kept through a cache eviction, and none
  after `servers.forget`; a factory that throws leaves `provider`
  null.
- `mobile/test/store.test.ts` (over a fake file layer): writing past the
  cache's bound evicts the oldest cache records and never a draft; a
  draft past its bound, and a 33rd draft, refused and reported, never
  shown as kept; a write interrupted before its rename leaves the old
  record whole; `clear()` leaves no file of the drafts and cache areas,
  empties the HTTP caches and leaves the servers area; the store handed
  to a provider cannot reach the servers area; a key outside its
  alphabet refused.
- `mobile/test/csp.test.ts`: the real `make web-build` output syncs
  and its `index.html` carries the policy (`script-src 'self'
  'wasm-unsafe-eval'`) with every inline script's hash and no other, no
  inline script left unhashed (the bundle's Monaco worker, which holds
  the text `eval(`, does not fail it); an `index.html` given an inline
  event handler or a `javascript:` address fails `csp.mjs`; an extra
  inline script is hashed like the others; the script tags of
  `bridge.js` and of a provider module stand before the entry, the
  latter only when the file is there.
- XCTest on the simulator job (`make mobile-ios`):
  `ConductorKeyTests`, with a test policy of the tests' own that names no
  service: nothing signed without a policy (`no_policy`); only the plan's
  bytes signed (the signature verified over them), and no call signs
  bytes handed in; presence asked whenever the plan asks (an
  `LAContext` stand-in the test drives) and nothing signed when it is
  cancelled or fails (`presence_refused`); an `ending` key signing only a
  plan marked `whileEnding`; `create` refused while a key is `ending`,
  `unbound` or `bound`; at start, a key whose marker is gone removed, and
  an `unbound` one past its `until`; the Keychain item's accessibility
  class. `ConductorStoreTests`: the files' protection class, the backup
  exclusion, the rename. `ShellInfoPlistTests`: `NSFaceIDUsageDescription`
  present with its words; no microphone, camera or location description;
  no background mode; every media capture request denied.
- CI's simulator build of the unsigned app.

**The web, OSS-1b** (`npm --prefix web run typecheck`, `typecheck:e2e`,
vitest whole; the e2e build and the specs named):

- `web/app/utils/shell.test.ts`: the reader (`null` outside a shell, a
  `v` other than 1 ignored with its line); every provider string bounded
  where it is read (each bound, one past it, a control character, markup
  drawn as text); a fake provider type-checked against the whole v1
  interface.
- `web/e2e/shell.spec.ts` (Playwright, the bridge injected by
  `page.addInitScript`, at 390 x 844 with touch and at 1440 x 900, both
  themes): no provider shows Open a server alone and opens an address
  through the bridge, with the `http://` words and a refusal's words;
  with a fake provider signed out, signed in, with its words that it
  cannot be reached and with its words that it cannot answer now, Open a
  server is reachable and opens the address without calling the provider
  (the fake counts its calls); the sign-in code, its page and its expiry;
  Settings' state, the test notification's words, Sign out and the
  "will be told" words while `ending`; outside a shell `/shell` goes to
  `/`; inside one, `/` goes to `/shell`; no request to `/api/` is made in
  a shell (the page's requests watched); the layout's measures
  (`web/e2e/layout.ts`: nothing overflows, `noSideways`, every target at
  least `TAP_MIN`) at the largest text size; and the contrast walk
  (`web/e2e/contrast.ts`) over each shell screen with nothing added to
  its known list.
- `mobile/e2e/boot.spec.ts` (Playwright over the synced `mobile/www`,
  served by the spec with that same policy as a header and no injected
  bridge): the bundle's own start draws the shell's first screen with no
  provider (Open a server), the colour mode applied, and no
  `securitypolicyviolation` event reported. An injected bridge cannot
  stand in for this: it is the bundle's start that is checked. From
  OSS-2b, a second case: a sync of the same bundle with a test provider
  module (`mobile/e2e/fixtures/provider.js`, the tests' own, naming no
  service: signed in, one item whose decision's context holds a fenced
  `go` block and a Mermaid diagram) opens `/shell/decisions/<id>` under
  the same policy; the code block is highlighted (`pre.shiki`), the
  diagram drawn in its frame, and no `securitypolicyviolation` is
  reported. Without `'wasm-unsafe-eval'` the block stays plain and a
  violation fires, which fails it. (In a browser with no native runtime
  the bridge hands the factory a key and a store that refuse every call,
  `no_native`; the fixture uses neither.)
- `ShellBootsUnderItsPolicyTests` (XCUITest on the simulator job): the
  built app with no provider draws Open a server under its real policy,
  and the page's violation listener (an external script of the test's)
  reports none; from OSS-2b, the same fixture provider's decision is
  drawn in WebKit with its code block highlighted and no violation.

**The web, OSS-2b**:

- `web/app/utils/shell-transport.test.ts`: every control the reused
  `DecisionRespond.vue` can draw (a clarification, an approval and an
  assignment), with the shell's answering: the answer's controls mapped
  onto `respond` field for field with the draft's `responseId` and
  `rev`; every outcome and code passed back; Comment and Ask for more
  drawn disabled with the answering's words and never reaching it; the
  draft's fields restored from `initial` and written through `changed`;
  a `respond` answering `waiting` leaves the choice, text and rationale
  as typed, makes no `draft.changed` call, disables the inputs and draws
  Cancel; one answering `unknown` keeps the content locked with no
  enabled Answer; a draft `initial` restores while `state` reports a
  lock is drawn locked from the first frame, and `confirm` and `cancel`
  are called by their buttons; only `kept` with a receipt clears the
  fields; and the component with nothing injected as on `main` (its
  routes through `useApi`, every control, a success clearing the fields
  as today).
- `web/app/utils/shell-ref-access.test.ts`: a chip, a receipt and the
  card's run line under the shell's `REF_ACCESS` make no `useApi` call
  and show the words.
- `web/app/utils/answer.test.ts`: every row of the answer's state
  machine, in the table's order, over a fake provider, with what the
  screen shows after each; A1 to A4 as properties over every row; a
  restart between any two steps resuming from the draft's state; the
  rebase: a fake provider holding the item at rev 4 and a draft at rev
  3, a first Answer ends in `editing` with "read it again" and no
  `respond`, and a second Answer with the item still at rev 4 sends
  exactly one `respond` with `expectedRev` 4 and a `responseId` other
  than the old draft's; after `waiting`, nothing is sent on resume until
  Confirm.
- `web/app/utils/drafts.test.ts`: through `ShellDrafts`, a draft written,
  then read back by a new provider instance over the same fake store (a
  restart) with its `responseId`, `rev` and state; each refusal code
  shown with its words and never as kept; another sign-in's drafts
  invisible; a `put` after sign-out refused; a sign-out removing every
  draft; a draft older than 7 days removed at read; and `drafts.ts`'s
  browser implementation of the same interface over local storage held
  to the same cases.
- `web/e2e/shell-decisions.spec.ts` (Playwright with a fake provider):
  the four rows shown as given and never merged; a card from a record
  with no history, comments or evidence contents drawn whole, with the
  chips' words and the omitted responses' line; an item with `elsewhere`
  shows its words and no Answer; a double tap on Answer sends one
  `respond`; another answer recorded shows its words and keeps the
  draft; a changed decision shows the new text before any send; an
  offline answer waits and is sent once on Confirm after the fake's resume; an
  unknown handle, an unreachable provider and an item not visible each
  open the inbox with their own words; a link the provider routes opens
  its item and one it does not opens the inbox; the page's measures at
  390 x 844 and the contrast walk, both themes. And the page outside a
  shell still reads the server's routes: `decision-page.spec.ts`,
  `decision-page-durable.spec.ts`, `decisions.spec.ts`, `decide-only.spec.ts`
  and the join page's specs pass unchanged.

**OSS-3**: `mobile/test` for the Android parts it adds, the Gradle build
and the emulator start in `mobile-android`.

**By hand** (what tests cannot cover), on a Mac with Xcode, from a clean
checkout: `make web-build mobile-install mobile-sync mobile-ios`; the
simulator app with no provider opens Open a server; a Conductor server on
the Mac (`http://localhost:8080`) opens in the in-app browser with the
unencrypted words, and its decision page answers there with the
workbench token; the app switcher shows the cover; VoiceOver reads Open a
server's controls in order; the largest Dynamic Type size keeps every
action reachable. On a device under the builder's own signing, the same,
and a server on the LAN by its address. With a provider, the rest of the
phone's checks are that provider's.

## Boxcar constraints

1. **Launches through `crew.Launcher`.** Not touched: nothing starts a
   run or a process.
2. **`session.Local` drives a `session.Process`.** Not touched.
3. **An attempt's context is files.** Not touched: an answer from the
   phone is applied by DR1's application, unchanged.
4. **Agents talk over the network.** Not touched; the shell talks to no
   agent.
5. **Checks through `internal/proc`.** Not touched.
6. **Platforms are platform tags; a provider registers where it can run;
   the UI shows a capability.** Honoured: the web app shows the shell's
   provider by its presence, never by a plan; iOS and Android are
   platforms of `mobile/`, and no Go build tag is added.
7. **Evidence rows carry `where`.** Not touched: no evidence is written;
   a decision's evidence reaches the phone as its refs only.
8. **`hostagent` is a launch site.** Touched only by its `Host`, which
   answers the new read `ErrUnsupported`: a host keeps no ledger.

## Open-core checklist

- [x] **1.** No paid code, licence check or plan check: `Ref` and the
  refs' read are `ext` words any provider may use, and the shell's only
  question is whether a packaging placed a provider.
- [x] **2.** A build with no sink and no shell is `main`; the shell with
  no provider opens a server's own decision page.
- [x] **3.** Nothing behind a flag: a notification sink, a shell provider
  and a signing policy exist only when someone outside this repository
  supplies them.
- [x] **4.** Stop, inspect, revoke, export and approvals stay as they are;
  a notification answers nothing; an answer from the phone is DR1's
  answer with DR1's recheck.

## The PRs

The rows are units of work; how rows are grouped into pull requests
follows `AGENTS.md` (Working an issue) at the time they are built. Each
row runs the checks of every surface it touches, ticks the open-core
checklist and cites this plan, and leaves `main` working and green on
its own: part one's rows are reached by Go tests with fake sinks, and
the shell's rows build and run with no provider. **No store step**: no
row changes the schema of the ledger or of Dispatch's store (OSS-2a's
read uses the ledger's existing index), so no row ships a migration
ahead of its code. Sizes are changed lines, code and tests.

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with | Size |
|---|---|---|---|---|---|---|---|
| OSS-0a | plans: a notification that names its record, and the open decisions' refs | this plan's part one, the line it adds to `docs/plans/ext.md` | `docs/plans` | none (docs) | none | every row | docs ~150 |
| OSS-0b | plans: the phone shell and its bridge | this plan's part two | `docs/plans` | none (docs) | OSS-0a | every code row | docs ~350 |
| OSS-2a | ext, api: a notification names its record; the open decisions' refs for a sink | `ext.Notification.Ref`, `ext.DecisionRef`, `ext.ParseDecisionRef`, the two bounds, `Valid`'s check; `Host.OpenDecisionRefs` in `internal/api` (with `ledger.OpenDecisionsAfter`) and `internal/hostagent`; `exttest`'s fake and its sink check; `docs/repository-map.md`'s two rows | `ext`, `ext/exttest`, `internal/api`, `internal/crew/ledger`, `internal/hostagent`, `internal/brakeman` (a test), `docs` | the Go tests above | OSS-0a; DR1's decision notification (on `main`) | OSS-1a, OSS-1b | ~200 + 300 |
| OSS-1a | mobile: the iOS shell, its native key and store, its bridge | `mobile/` (the Capacitor project, the iOS app with its cover and its media capture refused, `Info.plist`, `ConductorKey.swift` with `KeySigningPolicy` and no policy, `ConductorStore.swift`, their XCTest cases, `bridge.ts` and the factory with the key's whole interface, `csp.mjs`), the `mobile-*` targets, CI's `mobile` and `mobile-ios` jobs under `required-checks`, the notices over `mobile/`, `docs/dependencies.md`, `docs/checks.md`, `AGENTS.md`'s notices words, `docs/repository-map.md`'s row | `mobile`, `.github/workflows`, `scripts`, `Makefile`, `.gitignore`, `THIRD_PARTY_NOTICES`, `AGENTS.md`, `docs` | `bridge.test.ts`, `store.test.ts`, `csp.test.ts`, `ConductorKeyTests`, `ConductorStoreTests`, `ShellInfoPlistTests`, the simulator build | OSS-0b | OSS-2a | ~750 + 750, the generated Xcode project aside |
| OSS-1b | web: the bridge's types and the shell's own screens | `web/app/utils/shell.ts` (bridge v1 whole), `middleware/shell.global.ts`, `layouts/shell.vue`, `pages/shell/{index,signin,settings}.vue` with Open a server in every state; `mobile/e2e/boot.spec.ts` and `ShellBootsUnderItsPolicyTests`, added to `mobile-test` and `mobile-ios`; `docs/repository-map.md` | `web/app`, `web/e2e`, `mobile`, `docs` | `shell.test.ts`, `shell.spec.ts`, `boot.spec.ts`, `ShellBootsUnderItsPolicyTests` | OSS-1a | OSS-2a | ~450 + 450 |
| OSS-2b | web: the decision page from a shell's inbox, drafts revalidated, the handle's and the link's routes; the features entry | `utils/answering.ts` and its one seam in `DecisionRespond.vue`; the shell's `REF_ACCESS` and `utils/transport/shell.ts`; `utils/drafts.ts` and `utils/answer.ts`; `pages/shell/decisions/[id].vue`, the shell's inbox screen, `/shell/open`, `links.onOpen` through `route`; the boot checks' decision case and its fixture provider; `docs/features.md` | `web/app`, `web/e2e`, `mobile`, `docs` | the OSS-2b vitest and Playwright above | OSS-1b, OSS-2a | none | ~650 + 750 |
| OSS-3 | mobile: the Android shell (a later row) | `mobile/android`, `ConductorKey.kt`, the store's Android side, the platform's push token and taps, CI's `mobile-android` job, `docs/features.md`'s line | `mobile`, `.github/workflows`, `docs` | `mobile/test`, the Gradle build and the emulator start | OSS-2b | none | ~600 + 350 |

OSS-2a needs nothing of the shell and the shell's rows nothing of
OSS-2a until OSS-2b, which joins them; OSS-2b is the last code row of
the first platform and adds the `docs/features.md` entry.

## Owner decisions

Dated 2026-11-18. Each is a default the owner may overturn; the
reasoning is why it was taken. The first was put to the owner as a
question and **stands at its default** until the owner answers
otherwise; the rest are defaults of this plan.

1. **The bridge carries an inbox and drafts, and the workbench's own
   decision page draws a provider's inbox** (the default; the question
   put to the owner). Its alternative: a shell that only opens a
   provider's own pages in its web view, with no inbox, no drafts and no
   answer drawn by the workbench. *For the default*: the screens are the
   workbench's, with DR1's card, MB0's four rows and their accessibility;
   a provider that cannot be reached leaves the last inbox readable; an
   answer made offline waits as a draft and is checked before it is
   sent. *Against*: bridge v1 is larger (`ShellInbox`, `ShellDrafts`), and
   `DecisionRespond.vue` gains one seam. Under the alternative, OSS-0b,
   OSS-1a and OSS-1b shrink to a shell that opens one address its
   packaging sets, keeps the key plugin with a policy and no presence
   check, and has no inbox or draft types, and OSS-2b is dropped; OSS-0a
   and OSS-2a, and the shell's free default, are the same under both.
2. **iOS is the first platform; Android follows with no change to the
   bridge** (OSS-3). iOS is the stricter platform for lock-screen
   privacy and for a key the person must confirm, so what passes there
   carries; MB0's own phone checks name iOS Safari first.
3. **No notification actions.** The shell registers no category or
   action, so every decision, whatever its impact, is answered from its
   complete card; quick answers would need a policy of what is
   low-impact, which no record holds.
4. **The key signs only what a native policy builds, and the presence
   check is the plugin's.** The open-source shell may not know the
   format of anything a service asks it to sign (article 1), and a
   signature that must
   carry the person's confirmation cannot be left to a script in the web
   view, which a page could forge; the policy builds, the plugin signs.
5. **An Ed25519 key in the Keychain, this device only, while unlocked,
   signed with natively.** The Secure Enclave offers P-256 only; a raw
   CryptoKit key kept as a Keychain item of this device signs nothing
   while the phone is locked and never leaves the plugin. The cost: no
   service can prove the confirmation happened (Out of scope).
6. **Drafts stay on the phone and are checked before sending.** A draft
   keeps the `rev` it was written against and one `responseId`; it is
   sent only after a fresh read shows the decision unchanged, and an
   answer made offline is sent on return only after the person presses
   Confirm, under the same check and the same confirmation as a first
   send. Where anything changed, nothing is sent: the changed decision
   is drawn, the draft is rebased to the rev drawn with a new
   `responseId`, and the person answers again from what they now read.
7. **One provider and one sign-in per install.** A second provider would
   need a picker and a key per provider for a case nobody has asked for.
8. **Only a durable decision's notification carries a ref.** A session's
   question that is not a DR1 record has no record a phone could answer
   through a provider; its phone path stays MB0's decision links. A ref
   has one form; another kind of record gets its own in a later row, and
   one ref never names several records.
9. **No microphone or camera in the shell.** No usage description, and
   every media capture request refused, until a later row with its own
   plan changes both.
10. **A decision a provider does not take an answer for on the phone is
    shown whole with no Answer** (`elsewhere`, with the provider's
    words): a decision routed to others, or one the provider cannot take
    an answer for now, is answered where its words say.
11. **Open a server takes `http://` with words.** A Conductor server on
    the LAN serves plain `http://` by default (MB0's LAN path); refusing
    it would leave the free default useless on the LAN, and the words
    say what it means.
12. **The open decisions' refs in id order, in pages of 256.** A sink
    walks them with a cursor that survives decisions opening and closing
    between pages, and reads no more than it needs; the ledger's partial
    index of open decisions serves the walk with no schema change.

## Acceptance

Observed in three steps, each when its rows' checks pass in CI and
locally. **OSS-2a**: `TestDecisionNotificationNamesItsRecord`,
`TestWebhookBodyUnchangedByRef` and `TestOpenDecisionRefsPagesEveryOpenRecord`
show a fake sink handed each decision's ref with the webhooks' bytes
unchanged, and every open decision's ref read in pages and nothing else.
**OSS-1a and OSS-1b**: CI's `mobile` and `mobile-ios` jobs build the shell
unsigned and pass its vitest, Playwright and XCTest cases, the boot
checks among them, and the by-hand steps above show the simulator app
with no provider opening a Conductor server's decision page by its
address. **OSS-2b**: the vitest files and `shell-decisions.spec.ts` show
the decision page drawn from a fake provider's inbox with the four rows
as given, an answer checked before it is sent, sent once, kept offline as
a draft and settled after an unknown outcome, and the decision page
outside a shell unchanged. Done is the rows merged with those checks
green. A live check with a real provider is that provider's.
