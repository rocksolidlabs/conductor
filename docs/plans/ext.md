# The extension seams: the ext and app packages, and the eight interfaces wired in (Program items C1 to C9)

Status: proposed 2026-10-30, for the owner's review; the owner sets it to
Planned. Main at 293bd7f6 (PR #439, 2026-10-30). Every fact below about the
code was read from that commit on 2026-10-30; file, function and constant
names are main's.

This plan is Program items C1 to C9 (the Program, sections 2.4 and 4.2):
one public package of extension interfaces with their free defaults and a
registry, a public entry point for a `main` in another module, a lint that
keeps this repository free of private code, and the wiring of each
interface into the code it extends. Item C10, the Runtime seam, has a plan
of its own (`docs/plans/runtime.md`); this one gives Runtime its place in
the registry and nothing more. The plan is reviewed before any of its code;
the PRs at the end are cut into issues after the review, one issue, one
branch `issue-N`, one PR each (AGENTS.md).

The words used here:

- A **provider** is code outside this repository that implements one of
  the interfaces of the package `ext` and registers itself in its package's
  `init`. None exists in this repository, and none ever will (article 1).
- The **free default** of an interface is what main does today without
  it. The defaults live in `ext/local`.
- The **composition root** is `runServe` and `runHost` in
  `internal/cli`: the two places that read the registry, once, at start.
  (`conductor restore --provider` reads the sync slot alone, when a person
  runs it.)
- The **official app** is the Conductor desktop app and CLI built from the
  private premium repository with its providers compiled in (the Program's
  decision D16). A plain `make build` of this repository is the free build.
- A **seam** is the place in this repository's code where an interface is
  called: `authenticate` for identity, the event hub for notifications, and
  so on.

The names follow the Program, section 1.2: Conductor is the application,
Yardmaster the agent harness whose engine runs managed runs, Switchman the
team routing that a decision router provides (Program item SW1), the Code
Context Graph what a context provider provides (CG1), and "Workspace" only
the paid tenant. Two words the repository already uses keep their meaning
(article 11): "graph" is the crew and task view, so what a context provider
gives is *context* in docs and UI, never "graph"; "continuity" is a run
continued after a restart (`internal/crew/continue.go`), so the paid
feature behind `ContinuityStore` is called **sync** in docs, UI and the
capabilities route (its kind is `sync`), while the interface keeps the
Program's type name.

What changed since the Program's baseline (b6d017d) that this plan builds
on: the ledger is at schema version 5 (EN1's project manifests, #394), so
C10's `where` column is a question of schema 6 for that plan, and nothing
here changes a schema; Dispatch's store exists (`internal/dispatch`,
from DS1's issues #401 to #407) with `Store.OnChange`, which its own
comment reserves for the sync seam; and item B landed `CONTRIBUTING.md` with its
"Rules for code" section, which takes C7's line.

## Articles it serves

The twelve articles are the Program's (section 2.1). By number, what this
item does for each:

1. **Served.** The only question code in this repository asks about a
   provider is whether one is registered, and exactly one function asks
   it: `local.Fill`, which puts the free default in every empty slot of
   what the registry holds; the composition root calls it once, at start.
   Every seam then calls its interface without a condition. No licence,
   plan, tier or account is checked anywhere.
2. **Served.** A plain build registers nothing (a test of `cmd/conductor`
   asserts it), so every seam runs its free default, which is main's
   behaviour; the free build also gains `conductor export` and `conductor
   restore`.
3. **Served.** No config key, flag, environment variable or build tag
   turns a provider on: a provider exists only when another module's `main`
   compiles it in, and what it does then depends on its hosted service, the
   private repositories' concern.
4. **Served.** Export becomes a command and a route of the free build
   (C6). Stop, inspect and revoke are untouched and no provider is asked on
   their path. The decision router answers only through a question's
   claim, so the first response still wins, a held turn still refuses, and
   a route can type no key that is not one of the question's own choices.
5. **Served.** `internal/opencore`'s test fails CI when a file imports a
   module of the `rocksolidlabs` organization other than this one, when
   `go.mod` requires or replaces one, and when a file under `ext/` imports
   anything but the standard library and `ext` itself (so the private cloud
   repository can take `ext`'s types as its wire records without this
   repository's internals).
6. **Served.** The same lint refuses a build constraint naming anything
   but a platform, a Go version or one of the three test tiers (`recipes`,
   `pebble`, `network`).
7. **Served.** Each free default does what main does: recognise no
   token, route nothing, keep no copy, add no context, notify no one, add
   no ICE server, allow every entitlement. Each seam keeps main's behaviour
   first: the token chain before the identity provider, the configured ICE
   servers before a provider's, the webhooks before a provider's sinks. No
   file outside `ext/` the binary compiles names the entitlement source
   (C7, the same lint).
8. **Served** by three rules. First, the contract of every interface: a
   call answers from what the provider holds on this machine, never with a
   round trip to its service on the call's path; the provider talks to its
   service only on goroutines of its own, begun in `Start`, and keeps what
   it holds current there (`ext`'s package documentation says so, and the
   contract suite, C9, checks every call against it). The one exception is
   `ContinuityStore.Restore`, which is no call on any request's path: only
   `conductor restore --provider` makes it, at a person's word, with no
   server running, and it may fetch the archive from the provider's service
   on its own call, until it ends or the person stops it (C6). Second, the guard
   every provider call goes through, which bounds a provider that breaks
   the contract: a wait `ext` names (a bound of a hang, never what a normal
   answer takes), its panic recovered, its answer checked against `ext`'s
   bounds, and the free default answered on any failure, call by call,
   with nothing remembered about the failure, so a provider that recovers
   is used again at its next call and nothing restarts. Calls fed from
   queues never hold the code that queues them: a full queue drops its
   oldest (the sync store's pending set, which cannot, replays instead:
   C6). Third, start-up: `Start` returns at once, and one that errs,
   panics or is late is started again with backoff, never while an earlier
   try of it is still running, while its slots answer
   the free default, so a provider whose service was down when the server
   started is used once `Start` succeeds, with no restart (Host and Start).
   `GET /api/ice`, which anyone may call, never asks a provider, and a
   restore from a provider runs only when a person asks for it. The outage
   and lapse tests themselves are the private repositories' plans' (article
   8 asks each of them for one).
9. **Respected.** No new dependency and no cgo. Export snapshots the two
   SQLite databases with SQLite's `VACUUM INTO` through `modernc.org/sqlite`,
   already their driver (checked 2026-10-30 with v1.61.0, SQLite 3.54.1, on
   a read-only and on a read-write connection), and the archive is the
   standard library's `archive/zip`.
10. **Served.** This plan before the code; one issue per PR; race tests per
    package; the one protocol change (C5's `answer_as` host message and the
    register's `answerAs` field) touches `internal/proto`,
    `web/app/utils/protocol.ts` and `docs/protocol.md` together, with a
    size limit and a test; no dependency, so `make notices` has nothing to
    regenerate.
11. **Served.** The names above: context, never graph; sync, never
    continuity, for the paid feature; Switchman for the routing a provider
    gives; Workspace only for the paid tenant.
12. **Served as far as C1 reaches.** The registry has Runtime's slot and
    its `Where` type; its methods and its native default are C10's.
    Nothing in C1 to C9 starts a process: `ext.Host`'s launch is the
    engine's launch, which starts attempts only through `crew.Launcher`,
    and its submit goes through `Local.Submit` and the new
    `Local.AnswerAs`, the session's guarded writer.

## Why and what happens

Conductor is open core: this repository is the whole free product, and
paid features ship as providers compiled into the official app, each
backed by a hosted service (decisions D14 and D16). Nothing paid can be
built until the interfaces exist and are wired: identity (Program item ID1)
needs a place in authentication, mobile push (MB1) a notification sink,
the Code Context Graph (CG1) a way into an attempt's task, Switchman (SW1)
a way to route a question and apply its answer, sync (CS1) a hook after
every local commit, minted TURN credentials (CL1) the ICE sites, and the
official app's release (RL1) a `main` it can build from another module.
The premium repository's first PR (item P0) waits on C1 and C9.

The design is the one the Program fixes (section 2.4): eight interfaces in
a new public package, each with a free default and a registry in the style
of `database/sql`'s drivers; the composition root reads the registry once
and hands each provider to the code that calls it, as `SetPublisher` and
`SetPasteAnswerer` already give the server a dependency and
`certs.Options.Issuer` gives the certificate manager one. Two Go
facts fix where the code goes. Another module cannot import anything under
`internal/`, so the interfaces live in a top-level package `ext`. The
module's root package cannot hold the entry point, because
`internal/api/server.go` imports it for the third-party notices and
`internal/cli` imports `internal/api`: a root `Run` would be a cycle. So
the entry point is a second top-level package, `app`.

What a person sees once the item ships:

- **On the free build, nothing changes** in sessions, crews, runs,
  Dispatch, sharing, questions or webhooks. Settings → The app gains an
  **Extensions** row: "None: the open-source feature set". Two commands
  join the CLI: `conductor export` writes the data directory as one zip
  file (the catalog, the crews, the runs' records and the ledger, Dispatch with
  its attachments, a switchyard's links, the hook assets), and
  `conductor restore` puts one into an empty data directory on any
  machine, where the next `conductor serve` opens it as its own.
- **On the official app, signed in**, the row names the registered
  providers, and their behaviour shows where the seams are: a cloud
  account's token is accepted where a workbench or host token is, its
  subject in the receipts of what it did; attention reaches a phone; an
  attempt's task file has a Context section; a question is routed to the
  person who should answer it, and their answer is typed once, as a
  viewer's would be; every local save is followed by a copy to sync;
  viewers get minted TURN credentials. Signed out, or with the services
  unreachable, it is the free build, call by call.
- **A contributor** sees `ext/` as the extension API (its package
  documentation says how a provider is written, registered and started),
  `app` as the entry point, and a CI lint that keeps private code, feature
  tags and entitlement checks out.

How a provider plugs in, from the provider's side:

1. A package in another module implements, say, `ext.NotificationSink`,
   and calls `ext.RegisterNotificationSink(s)` in its `init`. It does no
   work in `init`: no goroutine, no file, no network.
2. That module's `main` blank-imports the package and calls `app.Main()`.
3. `conductor serve` starts. `runServe` calls `ext.Freeze()` (which
   closes the registry, so a later register panics, and returns what it
   holds), fills every empty slot with its free default (`local.Fill`),
   and hands the result to `api.New` (`api.WithExtensions`), which wraps
   every provider in the guard before the server's first write.
4. Before the server serves, every provider that implements `ext.Starter`
   gets `Start(ctx, host)`, its one door into the app: start a crew's run,
   type into a session or answer its question, send a notice, a directory
   of its own, a logger. `Start` returns at once; reaching its service is
   the work of the goroutines it begins, and a `Start` that fails is
   called again later.
5. From then on each seam calls its interface; the guard turns a late,
   failed, panicking or out-of-bounds answer into the free default's.

## The changes

### The packages, and who may import what

| Package | What it holds | May import |
|---|---|---|
| `ext` (new) | the eight interfaces, their records, their bounds and waits, `Host`, `Starter`, the registry | the standard library only |
| `ext/local` (new) | the free defaults, and `Fill` | the standard library and `ext` |
| `ext/exttest` (new, C9) | a fake per interface and the contract suite another module runs in its CI | the standard library (with `testing`), `ext`, `ext/local` |
| `app` (new) | `Run` and `Main`, the entry point of a `main` in this module or another | `internal/cli` |
| `internal/opencore` (new) | the lint of the open-core rules, as a test | the standard library |
| `internal/export` (new, C6) | the export archive: write, verify, restore | the standard library and this module's `internal/` |

`ext`, `ext/local`, `ext/exttest` and `app` are the module's public API;
every other package stays under `internal/`. The private repositories
import this module at a tag; a change to an `ext` type that removes or
renames a field, or changes a method, is a breaking change of that API and
is named in its PR's title.

### `ext`: the registry (PR-1)

`ext/registry.go`:

```go
// Provider is what every interface embeds: the name the logs, the guard's
// notes and GET /api/capabilities give the provider: 1 to 40 of a-z, 0-9
// and '-', beginning with a letter or a digit (ValidName).
type Provider interface{ Name() string }

// One value each.
func RegisterIdentity(p IdentityProvider)
func RegisterRouter(r DecisionRouter)
func RegisterContinuity(c ContinuityStore)
func RegisterEntitlements(e EntitlementSource)
func RegisterMedia(m MediaRoomProvider)
func RegisterRuntime(r Runtime)
// A list each, in the order of registration, at most MaxPerList.
func RegisterContextProvider(p ContextProvider)
func RegisterNotificationSink(s NotificationSink)

func Identity() (IdentityProvider, bool)
func Router() (DecisionRouter, bool)
func Continuity() (ContinuityStore, bool)
func Entitlements() (EntitlementSource, bool)
func Media() (MediaRoomProvider, bool)
func RuntimeProvider() (Runtime, bool) // a type named Runtime takes the shorter name
func ContextProviders() []ContextProvider
func NotificationSinks() []NotificationSink

// Registration is one registered provider: its kind and its name.
type Registration struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}
// Registered lists every registered provider, in the order of the kinds
// below and, within a list, of registration.
func Registered() []Registration

// Providers is what the registry holds, as the composition root reads it.
// The entitlement source has no typed field: no code of this repository
// asks it (C7). It is in All, as every registered provider is.
type Providers struct {
	Identity      IdentityProvider
	Router        DecisionRouter
	Context       []ContextProvider
	Notifications []NotificationSink
	Continuity    ContinuityStore
	Media         MediaRoomProvider
	Runtime       Runtime
	// All is every registered provider, the entitlement source's slot
	// among them, in Registered's order: what the server starts (Host and
	// Start) and GET /api/capabilities lists. A value is typed only as a
	// Provider, so a caller learns its kind and name, and whether it is a
	// Starter, and nothing it could gate a feature on.
	All []Entry
}
// Entry is one registered provider in one slot.
type Entry struct {
	Kind  string
	Name  string
	Value Provider
}
// Freeze closes the registry and returns what it holds. Every call after
// the first returns the same; a Register after it panics.
func Freeze() Providers

const MaxPerList = 16

// The kinds, as Registration and GET /api/capabilities name them.
const (
	KindIdentity      = "identity"
	KindDecisions     = "decisions"
	KindContext       = "context"
	KindNotifications = "notifications"
	KindSync          = "sync" // ContinuityStore: "continuity" is a run continued (article 11)
	KindEntitlements  = "entitlements"
	KindMedia         = "media"
	KindRuntime       = "runtime"
)
```

A `Register` panics on a nil interface, on a name `ValidName` refuses, on
a second provider in a one-value slot, on a name a list already holds, on
a list past `MaxPerList`, and after `Freeze`. One provider may register in
several slots under one name (a premium package that is both a router and
a sink), as one value: a name already registered in another slot is taken
only for the same value (its dynamic type comparable and the two `==`, as
a pointer is), and panics for a different or an incomparable one, so one
name is always one provider. It is then started once (Host and Start,
below). The registry is guarded by a mutex; `Registered`, the accessors
and `Freeze` return copies.

### `ext`: the eight interfaces and their records (PR-1)

Each record has JSON tags in the protocol's style (camelCase, `omitempty`
on what may be absent, `omitzero` on times), because the cloud repository
takes these types as its wire records. Each bound below is a constant of
`ext`, and each record a provider returns has a `Valid` method the guard
applies and a provider can call itself. Each wait below is the most the
server waits for one call; the guard applies it. A wait bounds a provider
that hangs: the contract of every method is to answer from what the
provider holds (article 8), so a provider that keeps it answers in
microseconds and the wait is never felt.

**Identity** (`ext/identity.go`; wired in C2):

```go
type IdentityProvider interface {
	Provider
	// Authenticate resolves a bearer token the server's own chain does not
	// know (the workbench token, the host tokens, the share links). ok
	// false: not the provider's. It answers from what it holds (a signature
	// checked against keys it keeps), never with a call per request; the
	// token is never logged or kept. Safe for concurrent use.
	Authenticate(ctx context.Context, token string) (p Principal, ok bool, err error)
}

// Principal is who presented a credential, or who acts (an answer's By,
// a launch's By): a subject, a display name and, from Authenticate, what
// it may do here.
type Principal struct {
	Subject string  `json:"subject"`          // 1 to MaxSubject bytes of printable ASCII, no space
	Name    string  `json:"name"`             // 1 to MaxPrincipalName characters, one line
	Grants  []Grant `json:"grants,omitempty"` // from Authenticate: at least one
}
type Grant string
const (
	GrantAdmin Grant = "admin" // what the workbench token may do
	GrantHost  Grant = "host"  // register a hosted session, as a host token
)
// SubjectActor is the prefix a principal's subject takes as the actor of
// the receipts it makes (C2): "user:" + Subject, at most 53 bytes, inside
// Dispatch's MaxActor (60) and never one of the actors this repository
// keeps for itself ("admin", "merge", "agent:<member>").
const SubjectActor = "user:"
const (
	MaxSubject       = 48
	MaxPrincipalName = 40 // proto.MaxNameLen, a viewer's display name
	IdentityWait     = time.Second
)
```

**Decisions** (`ext/decisions.go`; wired in C5):

```go
type DecisionRouter interface {
	Provider
	// Route is told, once per question, a question a session shows, and
	// answers at once whom it went to and why; it may carry an answer the
	// router already holds. It never waits for a person: an answer that
	// comes later goes through Host.Submit. Safe for concurrent use.
	Route(ctx context.Context, d Decision) (Route, error)
	// Resolved is told that a question Route was given no longer waits:
	// answered (by whoever), gone from the screen, or ended with its session.
	// Called in order, from one goroutine.
	Resolved(ctx context.Context, r Resolution)
}

// SessionRef names a session, with its crew reference when it is a
// member's or an attempt's.
type SessionRef struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	AgentID string `json:"agentId,omitempty"`
	Run     string `json:"run,omitempty"`
	Task    string `json:"task,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
}
// Decision is a question as the session shows it (session.Attention).
type Decision struct {
	ID      string           `json:"id"` // the question's id: what an answer names
	Session SessionRef       `json:"session"`
	Message string           `json:"message"`          // at most 500 bytes
	Kind    string           `json:"kind,omitempty"`   // permission | prompt | ""
	Source  string           `json:"source,omitempty"` // api, bell, osc, pattern, trust, question, …
	Options []DecisionOption `json:"options,omitempty"` // at most 6
	Plan    string           `json:"plan,omitempty"`    // the plan a question asks approval for
	Since   time.Time        `json:"since"`
}
type DecisionOption struct {
	Label string `json:"label"`
	Input string `json:"input"` // the keys that pick it: what an Answer's Input names
}
type Route struct {
	To     []Destination `json:"to,omitempty"`     // at most MaxDestinations
	Rule   string        `json:"rule,omitempty"`   // why, at most MaxRouteRule bytes
	Answer *Answer       `json:"answer,omitempty"` // an answer the router holds now
}
type Destination struct {
	Kind string `json:"kind"` // person | role | agent | crew
	Name string `json:"name"` // at most MaxDestinationName characters
}
// Answer is a choice or a line, and who gave it.
type Answer struct {
	Input string    `json:"input,omitempty"` // one of the decision's options' Input
	Text  string    `json:"text,omitempty"`  // a line of at most 4,096 bytes (proto.MaxSubmit)
	By    Principal `json:"by"`
}
type Resolution struct {
	ID      string     `json:"id"`
	Session SessionRef `json:"session"`
	Ended   bool       `json:"ended,omitempty"`
	At      time.Time  `json:"at"`
}
const (
	MaxDestinations    = 8
	MaxDestinationName = 60
	MaxRouteRule       = 200
	RouteWait          = 5 * time.Second
)
```

An empty `Route` is the free default: the people watching answer, and the
first answer wins, as today.

**Context** (`ext/context.go`; wired in C4):

```go
type ContextProvider interface {
	Provider
	// NoteTouch is told one file an agent touched, from the sessions' file
	// activity. Called in order, from one goroutine of the provider's own.
	NoteTouch(ctx context.Context, f TouchedFile)
	// Context answers the blocks an attempt about to start should read.
	// Safe for concurrent use.
	Context(ctx context.Context, q ContextQuery) ([]ContextBlock, error)
}
type TouchedFile struct {
	Session SessionRef `json:"session"`
	Path    string     `json:"path"` // absolute, as the session's touched index keys it
	Op      string     `json:"op"`   // read | edit | write | delete
	Tool    string     `json:"tool,omitempty"`
	By      string     `json:"by,omitempty"`
	At      time.Time  `json:"at"`
}
type ContextQuery struct {
	Run          string         `json:"run"`
	Crew         string         `json:"crew"`
	Task         string         `json:"task"`
	Attempt      int            `json:"attempt"`
	Kind         string         `json:"kind"` // work | review
	Goal         string         `json:"goal"`
	Instructions string         `json:"instructions"`
	Worktree     string         `json:"worktree"` // <cwd>/.conductor/worktrees/<run>/<task>.<n>
	Dir          string         `json:"dir"`      // the working directory inside it (the crew's prefix)
	Start        string         `json:"start"`    // the commit it starts at; a review's, the candidate
	Inputs       []ContextInput `json:"inputs,omitempty"`
}
type ContextInput struct {
	Task   string `json:"task"`
	Commit string `json:"commit"`
}
type ContextBlock struct {
	Title string `json:"title"`         // 1 to MaxContextTitle bytes, one line
	Ref   string `json:"ref,omitempty"` // a path or URL the block is about, at most MaxContextRef bytes
	Body  string `json:"body"`          // UTF-8 text, 1 to MaxContextBody bytes
}
const (
	MaxContextBlocks = 8       // a provider's, per attempt
	MaxContextTitle  = 120
	MaxContextRef    = 1024
	MaxContextBody   = 8 << 10
	MaxTaskContext   = 16 << 10 // every provider's blocks together, as a task file keeps them
	ContextWait      = 5 * time.Second
	TouchQueue       = 1024
)
```

**Notifications** (`ext/notify.go`; wired in C3):

```go
type NotificationSink interface {
	Provider
	// Events names what the sink takes: an event name (needs_input, done,
	// exit_nonzero, error, …) or an entry's own type (file, join, input, …,
	// or TypeProvider); nil takes every session entry. A provider's notice
	// reaches a sink only when TypeProvider is listed, and a provider with
	// such a sink cannot send notices (Host.Notify refuses): no provider
	// both takes and sends them, so notices never loop. Read once, at start.
	Events() []string
	// Notify delivers one notification. Called in order, from one goroutine
	// of the sink's own, with a context that ends after DeliveryWait.
	Notify(ctx context.Context, n Notification) error
}
// Notification is an activity entry of a session as the Events page and
// the webhooks type it, or a provider's notice (Host.Notify).
type Notification struct {
	Event   string     `json:"event,omitempty"` // the Events page's name; "" for an entry that has none
	Type    string     `json:"type"`            // the entry's own type, or TypeProvider
	At      time.Time  `json:"at"`
	Session SessionRef `json:"session"`
	State   string     `json:"state,omitempty"` // an attention entry's state
	Message string     `json:"message,omitempty"`
	URL     string     `json:"url,omitempty"`
	To      string     `json:"to,omitempty"`
	Tool    string     `json:"tool,omitempty"`
	Op      string     `json:"op,omitempty"`
	Path    string     `json:"path,omitempty"`
	By      string     `json:"by,omitempty"`
	ByName  string     `json:"byName,omitempty"`
	Origin  string     `json:"origin,omitempty"` // the provider that sent a notice
}
const (
	TypeProvider = "provider"
	SinkQueue    = 256 // as a webhook's queue
	DeliveryWait = 10 * time.Second
)
```

**Sync** (`ext/continuity.go`; wired in C6):

```go
type ContinuityStore interface {
	Provider
	// Saved and Deleted are told, after its local commit, of a document the
	// data directory now holds or no longer holds, with its bytes as they
	// are when it is delivered; each epoch begins with a replay of the whole
	// state (C6). Called in order, from one goroutine of the store's own,
	// with a context that ends after DeliveryWait. A nil answer
	// acknowledges the doc; an error, a panic or the context's end has the
	// document delivered again, whole, as it is then (C6).
	Saved(ctx context.Context, d Doc) error
	Deleted(ctx context.Context, d Doc) error
	// Restore writes this device's latest copy as an archive of `conductor
	// export`'s format, holding any part of data/ that the archive's checks
	// accept (C6); ErrNothingToRestore when it keeps none. Called only by
	// `conductor restore --provider`, with no server running and no Start:
	// r gives the provider its own directory and a logger, all it has then.
	// The one method not bound to answer from what the provider holds: it
	// may fetch the archive from its service on this call, streaming it
	// into w, and must end when ctx does (the person's Ctrl-C), returning
	// ctx's error; whatever it wrote is then thrown away.
	Restore(ctx context.Context, r RestoreRequest, w io.Writer) error
}
// RestoreRequest is what a provider's Restore is given on a machine where
// no server has started it.
type RestoreRequest struct {
	Dir string       // <dataDir>/ext/<name>, made 0700, empty on a fresh machine: the provider's sign-in goes there
	Log *slog.Logger // naming the provider
}
// Doc is one document's state after a local commit, or a replay's marker.
type Doc struct {
	Store  string    `json:"store"` // data | ledger | dispatch, or the markers replay | replayed
	Path   string    `json:"path"`  // a document's path in the data directory, a run's id, a Dispatch entity's kind/id or "log"
	Epoch  string    `json:"epoch"` // 32 hex characters, new at every replay: Rev orders docs of one epoch only
	Rev    int64     `json:"rev,omitempty"` // data: the data directory's commit sequence in this epoch; ledger: the run's revision; dispatch: the last change's position in its log
	At     time.Time `json:"at"`
	Size   int64     `json:"size,omitempty"`   // the whole document's bytes
	Offset int64     `json:"offset,omitempty"` // where Data begins in it: a document past MaxDocData comes in parts
	Data   []byte    `json:"data,omitempty"`   // at most MaxDocData bytes of the document as it is when delivered
}
var ErrNothingToRestore = errors.New("ext: nothing to restore")
const (
	MaxDocData  = 1 << 20
	SyncPending = 65_536 // documents waiting to be delivered; past it, a replay (C6)
)
```

**Entitlements** (`ext/entitlements.go`; the rule in C7):

```go
// EntitlementSource answers what the signed-in workspace may use of a paid
// feature. The providers of the private repositories ask it; no code of
// this repository does (CONTRIBUTING.md, Rules for code; the lint).
type EntitlementSource interface {
	Provider
	Entitled(ctx context.Context, feature string) (Entitlement, error)
}
type Entitlement struct {
	Allowed bool      `json:"allowed"`
	Limit   int64     `json:"limit,omitempty"` // 0: none
	Until   time.Time `json:"until,omitzero"`
	Why     string    `json:"why,omitempty"`
}
```

**Media** (`ext/media.go`; wired in C8):

```go
type MediaRoomProvider interface {
	Provider
	// ICEServers answers the servers one connection may use, beside the
	// configured ones: a hosted session's viewer, a host's registration, a
	// paste invite's answer. Safe for concurrent use.
	ICEServers(ctx context.Context, r ICERequest) ([]ICEServer, error)
	// Room answers how to join the room of a piece of work; ok false: none.
	Room(ctx context.Context, r RoomRequest) (RoomJoin, bool, error)
}
type ICERequest struct {
	Purpose string `json:"purpose"` // viewer | host | paste (GET /api/ice's offer never reaches a provider)
	Session string `json:"session,omitempty"`
	Role    string `json:"role,omitempty"`
	Viewer  string `json:"viewer,omitempty"`
}
type ICEServer struct {
	URLs       []string `json:"urls"` // stun:, stuns:, turn: or turns:; 1 to MaxICEURLs of at most MaxICEURL bytes
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"` // each at most MaxICESecret bytes
}
type RoomRequest struct {
	Kind string    `json:"kind"` // ticket | decision | review | run
	ID   string    `json:"id"`
	By   Principal `json:"by"`
}
type RoomJoin struct {
	URL       string    `json:"url"`
	Token     string    `json:"token,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
}
const (
	MaxICEServers = 8 // a provider's, per connection; the configured ones are not counted and never cut
	MaxICEURLs    = 4
	MaxICEURL     = 512
	MaxICESecret  = 256
	ICEWait       = 2 * time.Second
)
```

**Runtime** (`ext/runtime.go`; C10's): `type Runtime interface { Provider;
Where() Where }`, `type Where string`, `WhereNative Where = "native"` and
`Where.Valid` (1 to 32 of a-z, 0-9 and '-', beginning with a letter). Its
methods (an attempt's process, a check where its attempt ran) and its
native default are C10's to add, before anything implements it
(`docs/plans/runtime.md`); nothing in C1 to C9 calls it.

### `ext`: Host and Start (PR-1 declares them, PR-4 builds them)

```go
// Starter is a provider that needs the server: Start is called after the
// server is made and before it serves, with the provider's one door into
// the app. It returns at once, never waiting on its service (it begins
// goroutines that reach it), and it does not fail for an outage: an error
// is for what no retry mends (a bad configuration). One that errs, panics
// or has not returned within StartWait is called again, with a new ctx and
// a new Host, after a backoff, and only once the earlier try has returned:
// a provider never has two Starts running (Host and Start). ctx ends when
// the server shuts down or gives that try up, and the provider's
// goroutines end with it; the Host of a try given up refuses every call.
type Starter interface {
	Start(ctx context.Context, h Host) error
}
type Host interface {
	// Launch launches a saved crew as a run, as POST /api/crews/{id}/launch
	// does: through the engine, the one scheduler.
	Launch(ctx context.Context, r LaunchRequest) (RunRef, error)
	// Submit types a line into a server session, never into a question it
	// shows; or, with Question, answers that question with a choice (Input)
	// or a line (Text), once, as a viewer's reply would.
	Submit(ctx context.Context, r SubmitRequest) (SubmitResult, error)
	// Notify hands a notice to the notification sinks.
	Notify(ctx context.Context, n Notification) error
	// DataDir is a directory of the provider's own, <dataDir>/ext/<name>.
	DataDir() (string, error)
	// Log is the server's logger, naming the provider.
	Log() *slog.Logger
}
type LaunchRequest struct {
	Crew  string    `json:"crew"`
	Label string    `json:"label,omitempty"`
	By    Principal `json:"by"`
}
type RunRef struct {
	ID   string `json:"id"`
	Crew string `json:"crew"`
	URL  string `json:"url"`
}
type SubmitRequest struct {
	Session  string    `json:"session"`
	Question string    `json:"question,omitempty"`
	Input    string    `json:"input,omitempty"`
	Text     string    `json:"text,omitempty"`
	By       Principal `json:"by"`
}
type SubmitResult struct {
	Status string `json:"status"` // entered | pending (accepted, the program reading slowly) | sent (to a host)
}
var (
	ErrUnsupported      = errors.New("ext: not supported here")
	ErrNotFound         = errors.New("ext: not found")
	ErrQuestionAnswered = errors.New("ext: the question no longer waits")
	ErrRefused          = errors.New("ext: refused") // wrapped with the words why
)
const StartWait = 5 * time.Second
```

### `ext/local`: the free defaults (PR-2)

`Identity{}` recognises no token (`ok` false); `Router{}` answers the
empty route and is told nothing it keeps; `Continuity{}` keeps nothing and
restores `ErrNothingToRestore`; `AllowAll{}` answers every feature allowed,
no limit, "allowed: no entitlement source is registered"; `Media{}` adds no
ICE server and has no room. Context and notifications are lists, empty by
default, and need no default. Each is safe for concurrent use, has the name
`local`, and its doc comment says what main does without it. Runtime's
native default is C10's.

```go
// Fill is p with the free default in every slot nothing is registered in:
// the one place any code asks whether a provider is registered.
func Fill(p ext.Providers) ext.Providers
```

### `app` and `cmd/conductor` (PR-1)

```go
// Package app is Conductor's entry point for a main of this module or of
// another: the official app's main blank-imports what registers its
// providers, then calls Main.
package app
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) // cli.Run
func Main() // SIGINT and SIGTERM end ctx; Run(os.Args[1:]); the error printed; os.Exit
```

`cmd/conductor/main.go` becomes `func main() { app.Main() }`; what it did
moves into `Main` unchanged. `cmd/conductor/main_test.go` gains
`TestPlainBuildRegistersNothing`: its binary links every package `main`
links, every `init` has run, and `ext.Registered()` is empty. It lands
in PR-1, the stream's first, and runs in every PR's `go test ./...`
after it, so every PR of the stream carries it.
`docs/repository-map.md`'s "Adding things" gains **A provider**: implement
an `ext` interface in another module, register it in `init`, build a
`main` that blank-imports it and calls `app.Main`, start from `ext`'s
package documentation.

### The composition root, and the guard (PR-3)

`internal/cli/serve.go`: `api.New(cfg, cat, log, web, st,
api.WithExtensions(local.Fill(extensions())))`, where `extensions` is a
package variable holding `ext.Freeze` that a test replaces (as
`writeAssets` and `runHostAgent` are); when anything is registered, one
line at info names it (`extensions: identity cloud, notifications push`),
and a plain build logs nothing new. The providers go into `api.New`, not
after it, because `New` already writes: it saves `catalog.json` with
configured env values masked, migrates the crews, and opens the ledger,
whose recovery runs `Recover`'s transitions and `syncRecords`' record
writes before `New` returns (`internal/api/ledger.go`). The sync hooks
(C6) are installed from the option before the first of those writes, so
no commit of the start-up is missed. `internal/cli/host.go`:
`extensions()` too, so the registry is closed in host mode as well;
nothing is injected there until C10's Runtime.

`internal/api/extensions.go` (new): `New` gains a trailing `...Option`,
and `WithExtensions(p ext.Providers)` the one option of this item, so
every existing caller of `New`, the tests among them, compiles unchanged.
The server's slots are `local.Fill(ext.Providers{})` when no option sets
them, so a server made in a test without it is the free build;
`WithExtensions` takes a filled set (it asks nothing) and `New` wraps
every provider in the guard, defaults included, so no call site tells the
two apart. Queue-fed calls made during `New` wait in their queues, whose
drains begin once the provider's `Start` has succeeded (below; at once
`StartExtensions` runs for a provider that is no `Starter`); a queue that
fills meanwhile drops its oldest, and the sync store's pending set, which
drops nothing, replays the whole state once it starts (C6). And the guard
itself:

```go
// extCall runs fn for the provider name of kind within wait and answers
// its result, or fallback when fn errs, panics, returns what valid
// refuses, or has not returned by then (fn's context ends; its goroutine
// finishes on its own). At most extInFlight calls of one provider and
// kind run at once: past it, fallback at once. A failure is logged at
// warn once a minute per provider and kind, with how many since.
func extCall[T any](s *Server, kind, name string, wait time.Duration, fallback T, valid func(T) error, fn func(context.Context) (T, error)) T

const extInFlight = 16
```

Queue-fed calls (`Notify`, `NoteTouch`) go through `extQueue`: one ring
per provider and kind, filled without waiting (a full ring drops its
oldest, the first drop and every hundredth logged, as a webhook's queue
does), drained in order by one goroutine of its own, each call with a
context that ends after `DeliveryWait`, its panic recovered. Two kinds
never go through a ring, because a call they lose is never made again:
`Saved` and `Deleted` are fed from the sync adapter's pending set (C6),
and `Resolved` from the router's resolutions (C5), each drained the same
way (one goroutine, in order, `DeliveryWait`, panic recovered) and
neither dropping an entry; each is bounded by what it holds, not by
dropping. A provider that ignores its context holds only its own queue.
`Server.Shutdown` ends the goroutines and waits for them as it waits for
its others (`track`).

### Host and Start (PR-4)

`internal/api/exthost.go` (new): `(*Server).extHost(name) ext.Host`.

- `Launch` validates the request (a crew id `crew.ValidID` takes, a label
  `crew.ValidateLabel` takes, a valid `By`) and runs what
  `handleLaunchCrew` runs, moved into `(*Server).launchSaved(ctx, crewID,
  label)` for both: the managed checks, `Launchable`, `checkLaunch`,
  `resolveCwd`, yolo, `runs.LaunchNamed`. It notes in the run's log
  "launched by <By.Name> through <provider>" and answers `{id, crew, url}`
  (the run page's URL on the public base). A switchyard answers
  `ErrUnsupported`; a crew it does not hold, `ErrNotFound`.
- `Submit` takes a server session's line (`Text`, no `Question`) as
  `Local.Submit` with `UnlessWaiting` and `ByName` from `By`: a session
  that waits on a question is never typed into (`ErrRefused`, "the session
  asks a question"); a text answer (`Text` with `Question`) as
  `Local.Submit` with `Question`, the claim deciding as for a viewer's
  reply (`ErrQuestionAnswered` for a question that no longer waits).
  `Input` (a choice's keys) answers `ErrUnsupported` until C5 (PR-9); a
  hosted session, until PR-10. `pending` answers a line accepted whose
  program reads slowly (`session.ErrSubmitPending`).
- `Notify` validates the notice (`Message` at most 500 bytes, `URL` at
  most 2,048, one line each), sets `Type` to `TypeProvider`, `Origin` to
  the provider's name and `At`, and hands it to the sinks whose `Events`
  list `TypeProvider`; until C3 (PR-7) there are none, and it is logged at
  debug. A provider one of whose sinks lists `TypeProvider` gets a `Host`
  whose `Notify` answers `ErrUnsupported` ("a provider that takes
  provider notices cannot send them"), decided once at start from
  `Events`: a provider either sends notices or takes them, so no chain of
  providers can carry a notice back round, however many there are.
- `DataDir` is `<dataDir>/ext/<name>`, made 0700 through the store
  (`store.Sub`); `ErrUnsupported` without a data directory. Export leaves
  `ext/` out (it may hold a provider's credentials).
- `Log` is the server's logger with `provider=<name>`.

`(*Server).StartExtensions(ctx)` walks `Providers.All` and calls `Start`
once per distinct value that implements `ext.Starter` (one name is one
value, the registry's rule), the entitlement source's slot included, all
at once, each on its own goroutine with `StartWait`. Until a provider's
`Start` has succeeded, every slot it holds answers its free default and
its queues hold what reaches them. One that errs, panics or has not
returned by `StartWait` is logged (once a minute, as the guard logs), its
try is given up: the try's context is cancelled and the try's `Host`
revoked (each try gets a `Host` of its own, which answers every call
`context.Canceled` once its try is given up, so goroutines a given-up try
began cannot act). It is started again with a new context and a new
`Host` after a backoff, 1 s doubling to 5 minutes, for as long as the
server runs, and **never while an earlier try is unfinished**: each
provider has at most one `Start` running, and the next try begins only
after the last has returned and its backoff has passed. A `Start` that
ignores its cancelled context and never returns therefore holds one
goroutine for the server's life and no more; its provider stays
`starting` (its slots the default, logged once a minute), and nothing
calls it concurrently, which `Starter` never promises to bear. A late
`Start` that returns after its try was given up has its answer ignored.
Once one succeeds, its
slots are its own from their next call: a provider whose service was
down at start-up is used as soon as it is back, with no restart (article
8). `runServe` calls it after `api.New` and before the listeners serve,
with the maintenance context, which ends at shutdown and ends every try.
`conductor host` starts none: it has no `Host` to give.

### C2, identity at `authenticate` and `hostTokenOK` (PR-6)

`internal/api/auth.go`: `principal` gains `subject` and `name`. After the
chain (the workbench token, each host token, then `s.links.Resolve`),
`authenticate` asks the identity provider only for a token that resolve
answered `share.ErrUnknownToken` for: a link that is expired or revoked is
a link, refused as today, and never reaches a provider. The answer, through
`extCall` with `IdentityWait`, is taken only when `Principal.Valid` passes
and it has at least one grant: `GrantAdmin` makes the principal what the
workbench token makes it (`admin`, `yard` on a switchyard, `host`),
`GrantHost` a host's. Anything else is the empty principal, as an unknown
token is today, under the same rate limit. `hostTokenOK` becomes
`(*Server).hostAllowed(r, tok)`: the workbench token, the host tokens,
then the provider (a principal with either grant). The token is never logged;
a refused principal is logged at debug by its provider's name, never by
its token.

**The subject in receipts.** `principal.actor()` is `adminActor` ("admin")
for the workbench token and `ext.SubjectActor` + the principal's subject
(`user:<subject>`) otherwise. The prefix and `MaxSubject` (48) keep every
provider actor within Dispatch's `MaxActor` (60 bytes, `Op.check`'s
`ErrBadActor`) and the tags built from it, and apart from the actors
this repository keeps for itself: "admin", Dispatch's `ActorMerge`
("merge") and `agent:<member>`. No provider subject can be refused by
Dispatch or share another actor's receipts. `requireAdmin` puts the request's
principal in its context (`withPrincipal`), and `actorOf(r)` reads it. The
ledger's commands (`handlePause`, `handleCancelTask`, `handleRetryTask`,
`handleAcceptTask`), its receipt read (`handleGetCommand`), Dispatch's
operations (`dispatchOp.op` becomes `op(r)`, which its twenty-six callers
in `dispatch.go` and `dispatch_attachments.go` pass the request to) and
its receipt read (`handleDispatchOp`, `GET /api/dispatch/ops/{opId}`)
take `actorOf(r)` where they took `adminActor`. A receipt is found by the
actor that made it, as today; nothing stored changes shape.

### C3, notification sinks over the event hub; webhooks the first (PR-7)

`internal/api/notify.go` (new): `notifier`, the sinks in order, each with
its `extQueue` of `SinkQueue` notifications and its `Events` filter. On
main the webhooks' sink is added to the event hub first
(`startWebhooks`, `webhooks.go`, called in `New` before
`s.events.addSink(s.runs.OnActivity)`), and the hub's comment says so:
the webhooks, then the crew runs. The notifier takes the webhooks' place
in that order, added by `New` where `startWebhooks` is called today, so
the hub's sinks stay in main's order: the notifier (the webhooks first in
it), then the run engine's `OnActivity`. The notifier types each entry
once (`eventTypeOf`), looks the session up once (name, agent, crew
reference) when a sink wants it, and queues one `ext.Notification` for
every sink whose `Events` is nil or lists the entry's event name or its own
type: the rule a webhook applies (`webhook.eventFor`). A provider's
notice goes only to the sinks that list `TypeProvider` (Host and Start).

`internal/api/webhooks.go`: a configured webhook becomes the first kind of
sink: its `Events` are its configured events, and its `Notify` rebuilds
today's body from the notification (`webhookPayload{sessionId, session,
entry}`, the same struct, so the same bytes), sets `X-Conductor-Event` to
the event name when the webhook lists it and the entry's type otherwise,
signs and delivers once, as `deliver` does. The ring of `push`, `pop` and
`run` becomes `extQueue`, shared with the providers' sinks; the queue's
bound (256, the oldest dropped, the first drop and every hundredth logged),
the timeout, the address rule, the redirect rule and the order are
unchanged: the webhooks are still handed each entry before the run
engine, as on main. Within the notifier the webhooks come first, in
config order, then the registered sinks, in registration order.
`Host.Notify` reaches the provider sinks.

### C4, context in the task file and `task_context`; touches to the providers (PR-8)

`internal/crew/taskfile.go`: `TaskFile` gains `Context []TaskContext
json:"context,omitempty"`, `TaskContext{Source, Title, Ref, Body}`, and
`TaskMore` the same field. `taskMarkdown` writes, after "Inputs", a section
`## Context` that says the blocks are reference material a provider derived
and that the repository and the task are what holds, then each block as
`### <title> (from <source>)`, its ref, and its body inside a fence longer
than any run of backticks in it, so a block's text never reads as the
task's own words. `fitTaskFile` cuts context first, the last block first,
into `task-more.json` in full, with "the last N context blocks" in `cut`,
before the inputs' summaries. With no block the field is absent and the
section is not written: the files are byte for byte main's.

`internal/crew/task.go`: before `e.taskFile`, outside `e.mu`, the engine
asks its context function for the attempt (`Engine.SetContext(func(ctx,
ext.ContextQuery) []TaskContext)`, a function that answers nothing until
it is set). The query is built from the run and the attempt: run, crew,
task, attempt, kind, goal, instructions, worktree, directory, start and
inputs. The task files are still written through the worktree's
`os.Root`, so `task_context` (the route `GET /api/sessions/{id}/task` and
the MCP tool, which read `.conductor/task.md` and `task.json`) serves the
blocks as part of the task, with nothing kept only in the server's memory.

`internal/api/extcontext.go` (new): the context function asks every
context provider at once within one `ContextWait`, keeps a provider's
answer only when every block is `Valid`, takes at most `MaxContextBlocks`
of each, names each block's source by its provider, and drops, from the
last, blocks past `MaxTaskContext` together, noting in the run's log how
many it left out and whose. A late or failed provider adds nothing; the
attempt waits at most `ContextWait` for it, never fails for it. The touch
sink, an internal activity sink of the hub, takes the `file` entries of
server and hosted sessions, makes each path absolute by the session's own
rule (`session.TouchedAbs` for a server session; for a hosted one the rule
its touched index keys with, `hostAbs` in `internal/signal`, exported for
it), leaves out a server session's paths its deny list refuses (the rule
`TouchedFiles` applies, exported as `Local.Denied`), and queues one
`ext.TouchedFile` per provider (`TouchQueue`, the oldest dropped).

### C5, the decision router: `Local.AnswerAs`, the attention sink, `answer_as` (PR-9, PR-10)

**`Local.AnswerAs`** (`internal/session/local.go`, PR-9):
`AnswerAs(ctx, question, input string, by Actor) error`, `Actor{Subject,
Name}`. It writes a choice's keys as `AnswerInput` does, through the same
writer, guards and claim (`questionWaits`, `stillOnScreen`,
`claimQuestion`, `resolve`), with no subscription behind it: `input()`
takes who types as a small value (a subscription, or an actor with no
connection, which no link withdraws) instead of a `*Subscription`. The
keys must equal one of the shown question's choices (`Attention.Options`,
`Input`): no other keys are written for a route, ever. The resolve records
the actor's name as the answer's (`Answer.ByName`, the input entry, the
chat's "answered" line). A text answer stays `Local.Submit` with
`Question` and `ByName`. A held turn, a question no longer shown and a
question answered in the terminal refuse it as they refuse a viewer.

**The router** (`internal/api/router.go`, PR-9) watches the sessions'
attention itself, not their activity: a question can stop waiting with
nothing recorded (`Local.withdrawShownLocked` records no entry, and the
terminal may stay quiet after it). It is fed by the session change hooks
every attention change already calls, after the session's lock is let
go: `localChange` (a server session's `OnChange`) and the hosted hub's
`OnChange`, each of which marks the session's id dirty after its work of
today, never waiting. The dirty ids are a set, not a ring: an id is in it
at most once however often its session changes, so it holds at most one
entry per session the registry holds (`maxSessions`, at most 10,000, a
few hundred KB at worst), and nothing is ever dropped from it. A worker
takes an id out of the set before it reads the session, so a change made
while it works marks the id again and the session is read once more
after; a change is never lost, whatever the other sessions do meanwhile.
One worker per session id at a time (at most 16 sessions at once) reads
the session's attention
from the registry and keeps, per session, the questions it routed: their
ids, each `outstanding` or `resolved`, at most 64 per session (the
oldest resolved forgotten first), forgotten with the session. A routed id
is resolved only when the session neither shows it nor keeps it (below),
and a session shows again only a question it kept, so a resolved id never
returns and forgetting it routes nothing twice; an id resolved while
still shown (answered in the terminal, below) becomes forgettable only
once its session neither shows nor keeps it, so the ids not yet
forgettable are at most three per session (the one shown and the two a
session keeps), never forgotten. An id's
lifecycle is the router's own, never inferred from the id being new: a
question an agent asked and the work set aside comes back at the turn's
done with its first id (`TestAQuestionSetAsideByWorkShowsAgainAtTheDone`),
so an id already in the set is never routed again, whether it shows for
the first time since or again. When the session shows a question whose id
the set does not hold, the worker adds it as outstanding, builds the
`ext.Decision`, calls `Route` through `extCall` (`RouteWait`), and:

- an empty route records nothing: the free default, byte for byte;
- a route with destinations records one `status` entry in the session's
  activity, "routed: <kind> <name>, … (<rule>)", where the Events page and
  the activity tab show it;
- a route with an answer applies it once, through the function
  `Host.Submit` uses: `AnswerAs` for a choice, `Submit` with `Question` for
  a line. A person's answer that came first wins and the route's is
  refused (`ErrQuestionAnswered`, logged at debug).

Shown is not unanswered. A question answered by keys typed in the
terminal stays on screen, with its id and `needs_input`, until the
program redraws (`consumeLocked`; `attentionLocked` marks it
`Answered: "terminal"` at once, as
`TestAQuestionAnsweredByKeysInTheTerminalReadsAnsweredAtOnce` shows, and
a host sends the mark as `answered`), and the program may stay quiet
after. So a shown question so marked no longer waits: an id the set does
not hold that is first seen so marked is added as resolved and never
routed (nor told, since `Route` never had it), and an outstanding id that
reads so is resolved at once, without waiting for the screen.

The same worker, serialised per session, resolves each outstanding id
once, when the change it reads shows that the question no longer waits:
when the session shows it answered in the terminal (above), or neither
shows it nor keeps it (set aside in its turn, or held over by a held
turn). What a session
shows and what it keeps are one snapshot, never two reads: `session.Info`
gains `Kept []string` (`json:"-"`, never on the API), the ids of the
questions the session keeps without showing them (`own` while set aside,
`heldOver`), at most two, which `Local.Info()` fills in the same lock
acquisition as its `Attention` (`attentionLocked`). The router reads one
`Info` per change and judges from it alone, so a question restored
between two reads (A kept while B shows, then A shown at the done) can
never read as neither shown nor kept. For a hosted session the pair is
the host's `Info`, kept paired to the server: the host's forwarder
(`noteChange` holding the latest `Info`, `forwardChanges`,
`onLocalChange`) sends the attention and the `kept` of the one snapshot
it forwards in one message (`kept`, below), a retry resends the pair it
holds, never a newer `kept` beside an older attention, and the hub's
copy stores the two from that one message and answers them from its
`Info` under one lock. So a question set aside and shown again at the
done is one routed question, told `Resolved` once, at its real end, on
either kind of session and however many questions were routed between. The router
routes a hosted session only when its host's register said `answerAs`
(the hosts that send `kept`); an older host's questions are routed by no
one, and the people watching answer them, as on main. When the
session leaves the registry, each id still outstanding is resolved with
`Ended`.

*The resolutions drop nothing.* To resolve an id the worker marks it
`resolving` and appends its `Resolution` to the router's resolutions, a
first-in first-out list of its own (no ring: nothing is ever dropped
from it); one goroutine takes the oldest, calls `Resolved` with a
context that ends after `DeliveryWait`, its panic recovered, and only
when that call has returned (or its context ended) marks the id resolved
and lets its entry go: a resolution leaves the list only by being told,
once. A session that leaves the registry is forgotten, its resolutions
staying in the list until told. The bound is explicit and drops nothing:
the router holds at most `maxUnresolved` (1,024) ids routed and not yet
told, outstanding or `resolving`, server-wide; at the bound a newly shown
question is not routed (the people watching answer it, the free default
for that question) and is held as `skipped`, which, like an outstanding
id, is forgettable only once its session neither shows nor keeps it, so
it is not routed later either, and is never told; the skips are logged
at warn once a minute with how many. A `Resolved` that ignores its
context holds only this router's list, which stops growing at the bound.
`Host.Submit`'s `Input` lands here for server sessions.

**Hosted sessions** (PR-10): one new host control message, server to host.

```go
// HostAnswerAs (server → host): an answer a router or a provider holds for
// the question the host's session shows. The host applies it as its own
// viewers' answers are applied (Local.AnswerAs for Input, Local.Submit with
// Question for Text): once, the first answer winning. Sent only to a host
// whose register said AnswerAs; an older host ignores a type it does not
// know.
const HostAnswerAs = "answer_as"
type HostAnswerAsMsg struct {
	T         string `json:"t"`
	Question  string `json:"question"`            // ValidQuestionID: 1 to 32 bytes
	Input     string `json:"input,omitempty"`     // at most MaxAnswerInput (48) bytes
	Text      string `json:"text,omitempty"`      // at most MaxSubmit (4,096) bytes
	ByName    string `json:"byName"`              // 1 to MaxNameLen (40) characters
	BySubject string `json:"bySubject,omitempty"` // at most 48 bytes (ext.MaxSubject) of printable ASCII
}
func (m HostAnswerAsMsg) Valid() bool // the bounds, exactly one of Input and Text
// HostInfo gains:
AnswerAs bool `json:"answerAs,omitempty"` // the host takes answer_as, and sends kept
// HostAttentionMsg gains, host to server only, from a host that said AnswerAs:
// the ids of the questions its session keeps without showing them
// (Info.Kept: the agent's own question set aside, a question held over by
// a held turn), from the same Info as the message's attention, so the
// server's router can tell a question set aside from one that ended. An
// older server ignores it.
Kept []string `json:"kept,omitempty"` // at most MaxKept (2), each ValidQuestionID
const MaxKept = 2
```

Its size limit: about 4.4 KB at most, far under `MaxHostMessage` (64 KiB),
and `Valid` holds every field to its bound; `kept` adds at most 2 ids of
32 bytes to an attention message, and a message whose `kept` is past
`MaxKept` or holds an id `ValidQuestionID` refuses is dropped whole, as
any invalid host message is. Such a host sends its attention whenever what
it shows or what it keeps changes (a question set aside while the screen
shows another state changes only `kept`), and the server's copy, given an
attention equal to the one it shows, takes the new `kept` and records
nothing. The three places:
`internal/proto/hostmsg.go`; `web/app/utils/protocol.ts`
(`HostAnswerAsMessage`, `HOST_MAX_ANSWER_AS_SUBJECT`, `answerAs` on the
host's info, `kept` and `HOST_MAX_KEPT` on the attention message);
`docs/protocol.md`, Host control connection (the message's
row, the register's field and the attention message's `kept`).

**A switchyard never answers.** A switchyard, which someone else may run,
has no authority over the terminal today: `hostagent.Options.Switchyard`
takes no attention or activity from it, its stop never stops the
process, and `api`'s principal gives a switchyard's administrator no
terminal role. `answer_as` carries who answered, not a right to answer,
so it keeps that boundary: `internal/hostagent/agent.go` sets `AnswerAs`
in its register, and sends `kept` with its attention, only when `Options.Switchyard` is off (`conductor host`,
which publishes to the owner's own server, says it; a server's uplink to
a switchyard never does), and in switchyard mode a `HostAnswerAs` that
arrives anyway is dropped unread, as a `HostAttention` is, and logged. A
server in switchyard mode sends none: there `Host.Submit` for a hosted
session answers `ErrUnsupported` and the router routes only its own
server sessions. A routed answer reaches a hosted session only from the
owner's server, where the router and the providers run.

**Bounded on the host.** `hostagent` applies the message off the control
loop through one answer queue per connection: at most `answerQueue` (4)
messages wait, a fifth is dropped and logged (the first answer wins, so a
dropped later one loses nothing a person could not redo), and one
goroutine applies them in order, each under `submitDeadline()` (the
deadline `peer.typeIn` gives a viewer's reply), so a `Local.Submit` that
waits in `awaitTurn` behind a program not reading holds at most the
queue's four, never a goroutine per message. The queue and its goroutine
end with the control connection (its context), and a message waiting
then is dropped. An invalid message is dropped and logged; a valid one is
applied and its outcome logged at debug; the result reaches the server as
the host's attention and activity always do.
`internal/signal/hosted.go` keeps the flag from the register and gains
`(*HostedSession).AnswerAs(msg)`: `Tell` when the host takes it, else
`ext.ErrUnsupported`. `Host.Submit` and the router send a hosted session's
answers through it; their result is `sent`, and the host's next attention
says whether it was applied. A line with no question into a hosted session
stays `ErrUnsupported`: no message types plain text into a host's session.

### C6, sync hooks after every local commit; export and restore (PR-11, PR-12, PR-13)

**The hooks** (PR-11):

Each hook hands over its events in commit order, because calling an
observer outside the lock that serialises the commits does not: a commit
that lets the lock go can pause before its call while the next commits
and calls first (Dispatch's `Apply` already unlocks `s.mu` before
`s.notify`). So each hook hands an immutable event under that lock,
right after the commit, to the sync adapter, which marks the document
pending without waiting (its pending set, below), and the provider is
called from the adapter's own goroutine, outside every lock:

- `internal/store`: `(*Store).Observe(fn func(Event))` on the data
  directory's store, inherited by every `Sub` with its path prefix. The
  root store and its `Sub`s, each a `*Store` with its own `mu`, share one
  observer record holding a mutex and a sequence; a write takes that
  mutex around its rename or remove (the temp file is written and synced
  before, outside it), numbers the event from the sequence and queues it
  before letting the mutex go, so two saves of one document, through the
  root or through two `Sub`s, are queued in the order of their renames and
  numbered so. `Event{Op ("saved" | "deleted"), Path (slash path from the
  data directory), Seq}`; never on a failed write. No event carries bytes,
  so a plain file's bytes (the workbench token's) never leave the package;
  the adapter reads an allowlisted document's bytes when it delivers it,
  through `(*Store).OpenDoc(path)` (new: a regular JSON document only,
  opened read-only through the store's root). The data doc's `Rev` is
  `Seq`, so a store can order data docs itself too, within an epoch: the
  sequence lives in memory and starts again with every server, which is
  why every server's first delivery begins a new epoch with a replay
  (below).
- `internal/crew/ledger`: `OnCommit(func(RunPointer))`, called under
  `l.mu` right after each committed `Transition` (`Recover`'s are
  transitions too), for an observer that only queues;
  `RunPointer{ID, CrewID, State, Outcome, Paused, Revision, ContinuedFrom,
  ContinuedBy, CreatedAt, ClosedAt}`, read in the transaction by the
  statement that reads the new revision. `OnPrune(func(runID))` the same
  way after a committed `Prune`. Never on a failed commit (`beforeCommit`
  failing in a test proves it). `openLedger` takes the observer and
  installs it before `Recover` (the composition root, above).
- Dispatch: `Store.OnCommit(func([]Change))` (new), called under `s.mu`
  after each commit, for an observer that only queues; `OnChange`, which
  feeds the pages, is unchanged. The doc's `Rev` is the commit's last
  change's position in Dispatch's log, which the lock makes increasing.

The observers are installed in `api.New` before its first write (the
store's at its top, before `loadCatalog`'s save and the crews'
migration; the ledger's through `openLedger`; Dispatch's as it opens), so
the start-up's own commits (the masked catalog, the migrated crews,
`Recover`'s transitions and `syncRecords`' rewritten records) are told
like any other.

`internal/api/sync.go` (new) turns them into `ext.Doc`s for the sync
store. What reaches it is an allowlist:
`catalog.json`, `crews/<id>.json` and `runs/<id>.json` (`Store: "data"`);
a run's pointer from the ledger (`Store: "ledger"`, `Path` the run's id,
`Rev` its revision, `Data` the pointer as JSON: never the crew as
launched, the rows or the logs); a Dispatch entity a commit changed
(`Store: "dispatch"`, `Path` its kind and id, `Rev` the last change to it
in Dispatch's log, `Data` its project, ticket, kind and id).
`catalog.json` keeps saved agents'
env values in full (`keepMaskedEnv`, `internal/api/catalog.go`), which
`GET /api/catalog` shows only as `catalog.RedactedValue`; its doc carries
the catalog as that route masks it, every env value replaced by
`catalog.RedactedValue`, re-encoded by the adapter from the saved bytes,
so no env value reaches a sync store; a `catalog.json` that does not
decode is sent as a doc with no `Data`. The
workbench token file, `links/`, `tls/`, `hooks/` and `ext/` never reach
it.

*What is pending, not what happened.* The adapter keeps no queue of docs.
It keeps a pending set: one entry per document (store and path), holding
the last event's op and `Rev`, in the order the documents became pending;
a document already pending only has its entry updated. One goroutine of
its own takes the oldest entry and delivers the document **as it is
then**: a data document read through `OpenDoc` (gone: `Deleted`), a run's
pointer read from the ledger (`Ledger.Pointer(ctx, id)`, new, read-only;
pruned: `Deleted`), a Dispatch entity's reference. So the set never holds
bytes, holds at most one entry per document, and a document that changed
while it waited is delivered once, with its last state. A document larger
than `MaxDocData` (a run record may hold 4 MiB, `maxRecordSize`; the
catalog has no bound) is delivered whole, in parts: consecutive docs of
one path, `Rev` and epoch, each with at most `MaxDocData` bytes at its
`Offset` and the whole `Size`, read from one open file (one version, as
the store renames), so no document is ever cut and only one part is held
at a time.

*Acknowledged, or pending still.* An entry leaves the pending set only
when its document is acknowledged whole: `Deleted`, or every part's
`Saved`, answered nil. A call that errs, panics or outlives
`DeliveryWait` leaves the entry pending at the head of the set, logged
at warn once a minute, and the adapter tries it again after a backoff
(1 s doubling to 5 minutes, reset by a success; its timer from a clock
the tests drive), with the document as it is then and from its first
part: a parted document one of whose parts failed is delivered again
whole, every part of the new delivery read from one open file, so a
store that assembles a document's parts by path, `Rev` and epoch never
keeps a torn mix. Delivery stays in order, so nothing behind a failed
entry is delivered before it; later commits keep marking documents
pending meanwhile (one entry per document), and past `SyncPending` the
overflow replay applies as below. Recovery therefore needs no further
local write: a document that failed once and never changes again is
delivered when the provider next answers, and a provider whose service
was down (article 8) receives what waited, bounded, as soon as it is
back. A replay's `replayed` is an entry of its own, behind every
document the replay named, so it is told only once each of them has
been acknowledged in that epoch: a replay with a failed document never
ends as complete.

*Epochs and replays.* A replay hands the store the whole state: a
`Doc{Store: "replay"}` that begins a new epoch (a random `Epoch`, which
every doc after it carries), then every allowlisted data document, every
run pointer the ledger holds (`Ledger.Pointers(ctx, after, n)`, new,
read-only, paged), and one `Doc{Store: "dispatch", Path: "log", Rev:
<Dispatch's last change position>}` (`Store.Head`, new), each delivered
as above, interleaved with the live entries, then `Doc{Store:
"replayed"}`: a document the store holds from an earlier epoch that the
replay did not name is gone. A replay runs when the sync store's `Start`
first succeeds in a server's life, so every server lifetime is an epoch
of its own and a provider's cursor from an earlier lifetime is never
compared with this one's `Rev`s (the data directory's sequence starts
again with each server; the ledger's and Dispatch's positions are
durable, and the epoch covers them too); and when the pending set would
pass `SyncPending` entries, which then is emptied and replaced by one
replay in a new epoch. Nothing is dropped and no gap is left for the
store to fill: the initial state, an overflow, a restart and a document
too large for one doc each end in docs that hold the bytes. Reading
Dispatch's change rows from a log position, and applying another
machine's, is the sync item's own (CS1), proposed by its plan as the
change to Dispatch it needs.

**The archive and the route** (PR-12): `internal/export` (new):

```go
const Format = 1
// Manifest is conductor-export.json, the archive's last entry.
type Manifest struct {
	Format    int       `json:"format"`
	Version   string    `json:"version"` // the version that wrote it
	CreatedAt time.Time `json:"createdAt"`
	Files     []File    `json:"files"`
	Left      []Left    `json:"left,omitempty"` // what the export left out, and why
}
type File struct {
	Path   string `json:"path"` // under data/, slash separated
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Left struct{ Path, Why string } // secret | lock | temporary | provider | not a regular file
type Source struct {
	Dir      string // the data directory
	Snapshot func(ctx context.Context, rel, dst string) error // runs/managed.sqlite, dispatch/dispatch.sqlite
	Catalog  func() ([]byte, error) // catalog.json resolved against the configured catalog (below)
}
func Write(ctx context.Context, w io.Writer, src Source) (Manifest, error)
func Fresh(dir string) (bool, error) // holds none of catalog.json, crews/, crews.json, runs/, dispatch/, links/ (hooks/ and restore.lock aside)
// Reservation is restore.lock held: created exclusively by Reserve,
// removed by Release (below).
type Reservation struct{ /* the directory, the lock file */ }
func Reserve(dir string) (*Reservation, error)
func (r *Reservation) Release()
// Install checks and installs an archive into the reservation's directory,
// under the reservation its caller holds: it never takes or lets go of one.
func Install(ctx context.Context, res *Reservation, r io.ReaderAt, size int64) (Manifest, error)
// Restore is Reserve, Install and Release, for an archive already at hand.
func Restore(ctx context.Context, r io.ReaderAt, size int64, dir string) (Manifest, error)
const (
	MaxFiles    = 100_000
	MaxManifest = 32 << 20
)
```

The archive carries `data/` as the data directory holds it: the catalog,
the crews, the runs' records, the ledger and Dispatch's database (each a
`VACUUM INTO` snapshot, consistent while the server writes), the
attachment files Dispatch's snapshot names (below), a switchyard's `links/` (hashes, never tokens) and
`hooks/`. It leaves out, and lists in `left`: `workbench-token` and its
lock, `runs/owner.lock`, `restore.lock`, the databases' `-wal`, `-shm` and `-journal` files
(the snapshot holds what they did), `*.tmp`, `.probe-*`, Dispatch's
`attachments/tmp/`, `tls/` (the ACME account key and the certificates; a
new machine gets its own), `ext/` (a provider's own state), and anything
that is not a regular file or a directory. Repositories and worktrees are
the projects', not the data directory's, and are not in it.

**The catalog resolved.** `catalog.json` is an overlay on the configured
catalog, and an agent saved over a configured one stores
`catalog.RedactedValue` for every env value equal to the configured
agent's (`keepMaskedEnv` on save, `maskConfiguredEnv` at start) and omits
what it leaves to it (adapter, signal, site, yolo, trust, questions,
session, probe: `catalog.inherit`, which drops a masked key the
configured catalog lacks). The archive holds no configuration, so the
data directory's file would lose those values and fields on a machine
whose config differs. So `Write` takes the catalog from `Source.Catalog`
instead of the file: the overlay with every agent saved over a configured
one resolved against the configuration the source runs with, each masked
env value replaced by the configured value and each omitted field filled
from the configured agent (what `inherit` gives the running server), in
`catalog.json`'s format, so the archive's agents launch as the source's
did with no config at all. The server's export passes its own
configuration; the offline one (`--data-dir`) loads it as `serve` would
(`--config`, below). On a destination whose config holds the same agent
and the same values, the restored server's start masks the equal values
again (`maskConfiguredEnv`), so the agent follows that config from then
on, as an override saved in full by an earlier version does. Unlike a
sync store's copy, the archive therefore holds saved agents' env values,
and configured ones they inherit, in full, because a restored machine
must launch those agents as the first did: the archive is the person's
own file, written 0600, and `conductor export` says so on stderr ("the
archive holds your agents' env values, configured ones among them: keep
it as you keep the config"), as the README's Export and restore does.

**Run records before the ledger.** A run's JSON record is written after
its ledger transition, so the records may lag the ledger but never lead
it; `syncRecords` (`internal/api/ledger.go`) mends a record that lags its
ledger row or is missing, and a record ahead of the ledger is never
mended (it skips a record whose revision is at least the row's, and
`crew.Records.Save` refuses a lower revision). `Write` therefore copies
the runs' records (`runs/<id>.json`) first, then snapshots the ledger,
so a run that finishes or starts between the two is a record that lags
or is missing beside a newer ledger, which the restored server's first
`syncRecords` writes from the ledger; then Dispatch's snapshot and the
attachment files it names, then the other documents and files, then the
manifest.

**Attachments from the captured database.** A snapshot holds rows, not
the files they name: after it, an attachment can be removed and a second
Dispatch store's sweep at open can delete its file (the sequence
`TestAnAttachmentRemovedAndSweptWhileItOpensIsNotFound` exercises), so a
walk of `attachments/` would miss a file the snapshot's live row names.
So `Write` never walks `attachments/`: it reads from the snapshot itself
(`dispatch.SnapshotHashes(ctx, path)`, new: a read-only connection on the
snapshot file, the distinct SHA-256 of every attachment row whose
`deleted_at` is null) the files the captured database requires, and
copies each of them, verifying as it streams that the bytes hash to the
name (a file that does not fails the export, naming it). A required file
that is missing was removed and swept after the snapshot, which a later
snapshot no longer names (a remove tombstones the row before any sweep
may take the file, `attachments.go`): `Write` then takes Dispatch's
snapshot again and copies the files the new one requires that it has not
copied yet, at most three snapshots, and fails naming the hash if a file
the last one requires is still missing. Only the last snapshot goes into
the archive, so every live row it holds has its file there; a file an
earlier snapshot alone named is an orphan the restored store's first
sweep deletes (`TestTheSweepDeletesAnOrphanWhateverItsAge`). Files a
lease alone names (an upload in flight in the source's process) and
orphans are not copied. `Install` checks the same invariant on the way
in, for any archive (a provider's among them): before the renames it
reads the staged `dispatch/dispatch.sqlite`, when there is one, through
`SnapshotHashes` and refuses an archive missing a file one of its live
rows names.

**The destination reserved.** `Fresh` is a moment's answer, so a restore
reserves the data directory for its whole run: `Reserve` creates
`<dir>/restore.lock` with `O_CREATE|O_EXCL` (0600, holding the restoring
process's pid and start time) before the freshness check and removes it
after the last rename. `conductor serve` honours it: `runServe`, once it
has resolved the data directory and before `store.Open` or any write,
takes the same reservation itself and lets it go only after `api.New`
has returned, by which time the ledger holds its owner lock (on
platforms that have one) and the start-up's writes are made. So a
restore and a starting server exclude each other, and a running server is
refused by the restore through `ledger.Held` and `Fresh`. A second
restore, or a server, that finds `restore.lock` refuses with words naming
it: a restore runs, or one was cut short, and the file to remove to
start over. The check before the renames is repeated (`Fresh` again,
under the reservation), and it is not the only one: `Fresh` names six
places, and a fresh directory may hold other files, so the collision
check below is what keeps anything the archive holds from being renamed
over anything.

`Install` runs under a reservation its caller holds and passes in
(`Restore` takes one for it; `--provider` holds its own, below): it
refuses a reservation already released, never creates `restore.lock` and
never removes it, so the reservation is held without a gap for as long
as its caller holds it. It refuses a data directory that is not
`Fresh`, then checks the whole archive before it writes a byte: format 1, the manifest within its
bound, every entry listed with its size and SHA-256 and no other, every
path relative under `data/`, made of `[A-Za-z0-9._-]` elements (no `..`,
no backslash), no link, no duplicate, at most `MaxFiles`, and **no
reserved path**: whatever `Write` leaves out is refused on the way in,
by the one table both use (`export.leftOut`): `workbench-token` and its
lock, `restore.lock`, `runs/owner.lock`, a database's `-wal`, `-shm` or
`-journal`, `*.tmp`, `.probe-*`, `.restore-*`, `dispatch/attachments/tmp/`,
`tls/` and `ext/`, so an archive can neither plant a credential, a lock
or a provider's state nor name the restore's own files. It writes
everything into `<dir>/.restore-<random>` through an `os.Root` (files 0600,
directories 0700), syncs, then renames the entries into the data
directory. `hooks/` is the one entry a fresh directory may already hold
(`conductor host` writes the hook assets there, and `serve` writes them
anew at every start): the target's `hooks/` is renamed aside into the
staging directory first and removed with it, and the archive's takes its
place, so the rename never meets a directory in its way. **Nothing is
renamed over anything.** The install renames the staging directory's
top-level entries (`catalog.json`, `crews/`, `runs/`, `dispatch/`,
`links/`, `hooks/` and any other the archive holds) one by one; before
the first, under the reservation and after the second `Fresh`, every one
of those names is looked up in the data directory (`Lstat` through its
`os.Root`), and any that exists, other than `hooks/` moved aside as
above, refuses the restore, naming it, with nothing renamed: a fresh
directory holding an unrelated `config.json` keeps it unless the
archive holds one too, and then the restore stops before it is touched,
on Unix, where a rename would replace a file, as everywhere. Entries the
archive does not hold are never looked at or moved. A failure before the renames removes the staging directory and
leaves the data directory as it was; a crash during them leaves a partial
restore, which a second `restore` refuses as server data, its words naming
what to remove to start over. The next `conductor serve` opens what was
restored as its own:
the ledger's recovery marks what ran at the export interrupted (as after a
restart), Dispatch opens, the hook assets are written anew, a workbench
token is generated or configured as on any first start.

`internal/crew/ledger/snapshot.go` and `internal/dispatch/snapshot.go`
(new): `Snapshot(ctx, dst)` (`VACUUM INTO` on the open connection, under
the ledger's lock: transitions wait for it) and `SnapshotFile(ctx, path,
dst)` (a read-only connection, for an export with no server running);
`ledger.Held(dir) (bool, error)` tries the owner lock and lets it go.
`internal/api/export.go` (new): `GET /api/export`, admin (a switchyard's
too), streams the archive as `application/zip` with
`Content-Disposition: attachment; filename="conductor-export-<UTC time>.zip"`;
nothing is buffered whole.

**The commands** (PR-13), `internal/cli/export.go` (new):

```
conductor export [--server URL] [--token T] [--insecure] [--out FILE]
conductor export --data-dir DIR [--config FILE] [--out FILE]
conductor restore FILE [--config FILE] [--data-dir DIR]
conductor restore --provider [--config FILE] [--data-dir DIR]
```

`export` asks the running server (`--server`, else `CONDUCTOR_SERVER`,
else `http://localhost:8080`; the token as `conductor up` takes it, the
same refusal of a plain server on another machine without `--insecure`)
and writes `--out` (default `conductor-export-<UTC time>.zip` in the
current directory, 0600, never over an existing file). With `--data-dir`
it reads the directory itself, only when no server holds its ledger's
owner lock (refused otherwise, naming the server form), and resolves the
catalog against the configuration `config.Load(--config)` gives, as
`serve` would load it. `restore` takes
`--data-dir`, or else the data directory `conductor serve` would use,
resolved the way `runServe` resolves it: `config.Load(--config)` (which
applies `CONDUCTOR_DATA_DIR`), then `cfg.ResolveDataDir(--config)`, with
its legacy `conductor.d` rule and its notices, so a config's `dataDir`
is honoured. It prints the directory it chose before it writes, refuses
one that holds server data, and installs the archive. The desktop app
starts its server with its own `dataDir` (`<userData>/conductor` on
Windows and macOS, `~/.local/share/conductor/data` inside WSL,
`desktop/src/settings.ts`), which no default of the CLI can know: the
README's Export and restore names both paths, to be given as
`--data-dir`, with the app quit. `--provider` takes the reservation
(`Reserve`) and checks `Fresh` first, so no server starts while it
fetches, then asks the sync store
(`local.Fill(extensions()).Continuity`) for its copy (the free default
answers that there is none, and nothing changes) into a temporary file
beside the data directory, giving it `RestoreRequest{Dir:
<dataDir>/ext/<name>, Log}` (made 0700; a provider that needs a sign-in
keeps it there), then installs it with `Install` under that same
reservation, its checks included, and releases it only after the last
rename or the failure that ends it: one reservation, held continuously
from before the download to after the install, never let go between
them and never taken twice: the
one place a provider's `Restore` is called, with no server running and
no `Start`, at a person's word. It is the one provider call the
local-only rule does not bind (article 8, above): `Restore` may stream
the archive from its service on the call's own goroutine, for as long as
the download takes, with no wait of the guard's; the command's context
(SIGINT and SIGTERM end it, `app.Main`) is its context, and the command
prints the provider's name and the bytes written as they arrive, so a
person sees it work and can stop it. Whatever ends it short (an error,
ctx's end, a panic, recovered) removes the temporary file and releases
the reservation, the data directory as it was (`ext/<name>`, the
provider's sign-in, aside: `Fresh` does not count it, and a retry uses
it), and the command exits 1 with the provider's error. What its archive holds is the provider's
(CS1 defines it); the installer accepts any part of `data/` its checks
pass, and `serve` opens such a directory as it opens a new one, creating
what is missing (an empty ledger, an empty Dispatch). The usage text and
the completion table gain both commands; the README gains "Export and
restore" under Configuration.

### C7, the entitlement rule (PR-14)

`CONTRIBUTING.md`, Rules for code, gains: "Code in this repository never
asks the entitlement source (`ext.EntitlementSource`) whether a feature is
allowed: only the paid providers do, outside it; a free feature has no
entitlement to ask about." `internal/opencore` gains the rule
`entitlements`: outside `ext/`, in any `.go` file but a `_test.go` one, a
reference to `ext.EntitlementSource`,
`ext.Entitlement`, `ext.Entitlements`, `ext.RegisterEntitlements` or
`ext/local`'s `AllowAll`, or a call of a method named `Entitled`, is a
problem, which CI's go job fails on. A reference is not an import, so
this rule parses each file whole (`go/parser` with no mode limit, then
`go/ast`), unlike the rules of PR-5, which read imports and build
constraints only: it finds the selector expressions whose package is the
file's name for `…/conductor/ext` or `…/conductor/ext/local`, under any
import name (a dot import of either outside `ext/` is a problem too),
and the method calls named `Entitled`. A `_test.go` file is exempt
(amended in PR-14): the go command compiles it into a test binary only,
never into `conductor`, so it gates nothing; and the tests of the
composition root and the host (`cmd/conductor/main_test.go`,
`internal/api/exthost_test.go`) and PR-16's
`TestCapabilitiesNamesProviders` must name the entitlement source to test
it.

### C8, the four ICE sites through the media provider (PR-15)

`internal/api/media.go` (new): `(*Server).iceServersFor(ctx, r
ext.ICERequest) []proto.ICEServer`: every configured server first, as
today and uncounted (main bounds neither `iceServers` in the config,
`config.Validate` asking only that each entry has URLs, nor
`CONDUCTOR_ICE_SERVERS`, which makes one server per URL, so none of them
is ever cut), then the media provider's through `extCall` (`ICEWait`),
each `Valid`, at most `MaxICEServers` of the provider's; a late or failed
provider adds none. Three sites call it: the hosted viewer's welcome
(`ws_viewer.go`, purpose `viewer` with the session, role and viewer),
the host's `registered` (`ws_host.go`, `host`) and the paste answerer's
servers (`paste.go`'s `iceServers`, `paste`). The fourth, `GET /api/ice`
(`paste.go`'s `handleICE`), goes through `iceServersFor` too, as section
2.4 asks of all four, with the request `offer`, for which the function
never asks the provider: that route, which anyone may call, answers
exactly what main answers, the `stun:` and `stuns:` URLs of the
configured servers, so an unauthenticated request costs a
provider nothing and never carries a `turn:` URL, a user name or a
credential, the configured ones' or a provider's. `Room` has no
caller here: the room UI is Program item CL1's (its OSS UI plan); its
default answers no room, and the contract suite (C9) covers it.

### C9, `GET /api/capabilities`, the web app reading it, and `ext/exttest` (PR-16, PR-17)

**The route** (PR-16), `internal/api/capabilities.go` (new):
`GET /api/capabilities`, admin: `{"providers": [{"kind", "name",
"state"}]}`, one row per entry of `Providers.All` (the entitlement
source's slot among them, by kind and name only), `state` `ready` or
`starting` (its `Start` has not yet succeeded and is tried again: the
slot runs its default meanwhile); a plain build answers `{"providers":
[]}`. The reply is at most a few KB (eight kinds, at most 16 providers
per list).

**The web app** (PR-16): `web/app/composables/useCapabilities.ts`, read
after the first paint and kept while no entry is `starting`, forgotten
with the workbench token as the palette's caches are; while any entry is
`starting` it reads the route again, 2 s after the last reply doubling to
60 s, and stops once none is (a provider that never starts costs one
small request a minute while Settings is open), so a page opened while a
provider was still starting shows it `ready` when it is, with no reload
and no change of token; `web/app/utils/capabilities.ts`, pure:
`has(caps, kind)` and `extensionsWords(caps)` ("None: the open-source
feature set", or "identity: cloud · notifications: push", one still
starting "(starting)"). `AboutCard.vue` shows the row **Extensions**
(`data-about-row="extensions"`). This is the rule paid UIs follow: they
show when a capability is there, never because of a plan (open decision
O16, below).

**The contract suite** (PR-17), `ext/exttest`: a fake for each of the
eight interfaces and for `Host` (`FakeIdentity` mapping tokens to
principals, `FakeRouter` recording decisions and answering scripted
routes, `FakeContext` recording touches and answering scripted blocks,
`FakeSink` recording and optionally blocking, `FakeContinuity` recording
docs and restoring a scripted archive, `FakeEntitlements`, `FakeMedia`,
`FakeRuntime` answering a `Where` (Runtime's whole interface in C1; C10
adds its methods to the fake and the suite), and `FakeHost`, an
`ext.Host` that records every `Launch`, `Submit` and `Notify` with
scripted answers, gives a `t.TempDir()` as its `DataDir` and a logger
into the test's log, and can be `Close`d, after which it answers every
call `context.Canceled`), each safe for concurrent use; and one function
per interface that checks an implementation against the rules this plan
writes, each taking an `exttest.T` (the part of `*testing.T` it uses:
`Helper`, `Errorf`, `Fatalf`, `TempDir`), so a test can record what
fails: `Identity(t, n, p, known, unknown)`, `Router(t, n, r, d)`,
`Context(t, n, p, q)`, `Sink(t, n, s)`, `Continuity(t, n, c)`,
`Entitlements(t, n, e)`, `Media(t, n, m)`, `Runtime(t, n, r)`,
`Starter(t, n, s)`, `n` the `*exttest.Network` below, and `RunAll(t, n,
ext.Providers)`, which runs the function of every entry of
`Providers.All` (the entitlement source's among them: `exttest` is under
`ext/`, where the lint allows the name) and `Starter` for every value
that is one. Among what they check: a valid name; the contract of
article 8, that every call answers from what the provider holds, checked
by watching the provider's network rather than a clock. Each check
function takes the `*exttest.Network` the implementation under test was
built with (nil for one with no transport, as the defaults): its
`DialContext` and `Transport()` (an `http.RoundTripper`) are what the
provider is configured to reach its service through, and it records
every dial and request with the context it was given. The suite gives
each request-path call (every method but `Start`'s goroutines and
`Restore`) a context carrying a mark, and fails the check on any dial or
request made under a marked context: work for the call on its own path,
however fast it would answer. It then blocks the network (every dial and
request waits on a gate the suite holds shut) and makes the calls again:
each must return, from what the provider holds, with the gate still
shut. A provider that reaches its service on the call's path under some
other context waits on the gate and is caught when the guard's own wait
for that method ends (the product's cap, the test's timeout above it),
never by a threshold of the suite's. The premium repository runs the
suite with its hosted service unreachable as well as reachable. How long
a call takes is measured apart, by `exttest`'s benchmarks, never a check
that fails; nothing recognised for an empty token;
a valid principal with a grant whenever `ok`; a route valid for its
decision; at most `MaxContextBlocks` valid blocks; `Events` stable; a
valid `Where`; `Start` with a `FakeHost` returning while the host's
calls and the network are blocked (the gate shut until it has returned),
and taking a closed host's refusals without a panic once its context is
cancelled; calls from 32
goroutines at once under the race detector for what must be safe for
concurrent use; a call with a cancelled context returning, the network
blocked. `Continuity`'s `Restore` is checked apart, as the one call that
may use the network: it streams into its writer, and it returns ctx's
error once ctx ends while its download waits on the shut gate. Its package
documentation says how the premium repository runs it in CI.

### The lint: `internal/opencore` (PR-5)

`opencore.Check(root string) ([]Problem, error)` reads every `.go` file of
the module under `root` with `go/parser` (imports and build constraints
only, whatever their tags), skipping `testdata`, `node_modules`, `vendor`,
`internal/web/dist`, directories beginning with `.` or `_`, and any
directory with a `go.mod` of its own (a nested module: a worktree under
`.claude/` is both), and reads `go.mod`. The rules, each named in the
problem it reports with file and line:

- `private-module`: an import of a path under `github.com/rocksolidlabs/`
  that is not this module (`github.com/rocksolidlabs/conductor` or under
  it, compared by path elements, so `…/conductor-premium` is caught);
- `go-mod`: `go.mod` requires or replaces such a module;
- `feature-tag`: a `//go:build` expression naming a tag that is not a
  GOOS or GOARCH `go tool dist list` prints, `unix`, `cgo`, `gc`, `gccgo`,
  a `goN.N`, or a test tier (`recipes`, `pebble`, `network`);
- `ext-std`: a file under `ext/` importing anything but the standard
  library and packages under `ext/`;
- `entitlements` (PR-14), which, unlike the four above, parses each file
  whole and skips `_test.go` files (C7).

`TestModuleKeepsTheRules` runs `Check` on the module and wants nothing;
`TestEachRuleCatchesItsPlant` runs it on fixtures under `testdata/`, each
with one planted problem (an import of `…/conductor-premium/register`, a
`go.mod` that requires `…/conductor-cloud`, a `//go:build premium` file, a
file of `ext/` importing `internal/session`), and wants exactly that
problem; `TestPlatformTagsMatchTheToolchain` checks the known GOOS and
GOARCH list against `go tool dist list`. `make lint` runs
`TestModuleKeepsTheRules` after `go vet`, so `make lint`, `make preflight`
and CI's go job (which runs both `make lint` and the whole race suite)
fail on a planted import. No workflow changes.

### Docs

- `docs/repository-map.md`: rows for `ext` and `app` (PR-1), `ext/local`
  (PR-2), `internal/opencore` (PR-5), `ext/exttest` (PR-17), and
  "Adding things: A provider" (PR-1). The Program asks every PR of the
  stream for the `ext` and `app` rows: they are written whole when the
  packages land, describing all eight interfaces, and a later PR edits
  them only when it changes what a package holds.
- `docs/architecture.md`: the Packages table's rows for `ext` and `app`
  (PR-1) and `ext/local` (PR-2); a new section **Extensions** (PR-3: the registry,
  the composition root, the guard, the waits and queues; PR-4: Host and
  Start); each seam's PR adds its paragraph there, and C6's the Persistence
  section's lines on sync, export and restore.
- `docs/protocol.md`: C2's note on which tokens are admin and host and
  under which actor a receipt is kept (HTTP API); C4's `context` in
  `task.json` and the cut order (Managed runs); C5's `answer_as` and
  `answerAs` (Host control connection) and the routed answer (Answering a
  question); C6's `GET /api/export`; C8's `GET /api/ice` row and the
  welcome's and registered's ICE servers; C9's `GET /api/capabilities`.
- `docs/checks.md`: `make lint` names the open-core rules (PR-5, PR-14).
- `CONTRIBUTING.md`: C7's line. `README.md`: Export and restore (PR-13).
- `docs/features.md`: one entry, "The extension seams (Program items C1
  to C9)", beside item B's governance entry, so that every PR carries its
  part of it. PR-1 writes the entry's head and one paragraph per PR, each
  "planned" with the PR's title, separated by blank lines; each PR
  rewrites its own paragraph only, with what it shipped and its tests. Git
  merges two such edits of separate paragraphs without a conflict, and
  conflicts on edits of adjacent lines (checked 2026-10-30 on a two-branch
  fixture), so the PRs that may run in parallel do not collide there.
  PR-17, the stream's last, completes the entry with what was observed.

## Events, orderings and invariants

**The registry's life.** Registration happens in `init`, before `main`.
`ext.Freeze` closes it; `runServe` and `runHost` call it once (through
`extensions`), and a `Register` after it panics, so a provider can never
appear mid-run. The other commands (`notify`, `mcp`, `task`, `crew`, `up`,
`crews`, `hooks`, `export`, `restore` without `--provider`) neither read
nor start a provider: in the official app their processes link the
providers, which register and stay inert. `api.New` takes the providers
once (`WithExtensions`), before its first write and before `Handler` is
served; `StartExtensions` runs once, after it and before the listeners
serve; a slot whose provider has not yet started answers its default
until a later try of its `Start` succeeds, with no restart. At shutdown the maintenance context ends, which ends every provider's
`Start` context, and `Shutdown` waits for the queues' goroutines as for its
own (`track`). A test never registers into the global registry outside
`ext`'s own tests (which reset it through an `export_test.go` helper); the
composition root's tests replace `extensions`.

**The guard's invariants.** Every call of a provider goes through `extCall`
or an `extQueue`. A call never outlives its wait in the caller's eyes; a
panic never escapes; an answer out of bounds is never used; at most 16
direct calls of one provider and kind are in flight; a queue never makes
its producer wait. The guard keeps no state about failures beyond the
once-a-minute log, so a provider's next call after an outage is answered as
if none had happened: that is article 8's "resumes on its own". A `Start`
that failed is tried again with backoff, its abandoned try's context
cancelled and its `Host` revoked first, and only once that try has
returned: one provider never has two `Start`s running, so a provider
down at start-up resumes the same way and one that hangs in `Start`
holds one goroutine, never a growing number.

**Identity.** Order: workbench token, host tokens, share links, provider.
A token the chain knows never reaches the provider; a revoked or expired
link token is refused as a link. A principal's subject, as
`user:<subject>`, is the actor of every receipt it makes, at most 53
bytes, so Dispatch never refuses it; "admin", "merge" and
`agent:<member>` are this repository's alone and no provider actor can
equal one; the commands' idempotency stays keyed by actor and id, so a
provider's subject sees only its own receipts.

**Notifications.** The entry reaches, in order: the SSE clients, the
notifier (each webhook's queue, then each notification sink's), then the
run engine's `OnActivity`, the hub's sinks in main's order. A
sink's notifications arrive in the order the hub handed them over; a full
queue drops its oldest; no sink delays the hub, a session, the engine or
another sink. A provider's notice reaches only the sinks that list
`TypeProvider`, and a provider with such a sink cannot send notices, so
the providers that send notices and those that take them are apart and
no notice is ever handed back to a sender, through one provider or a
chain of them.

**Context.** For an attempt: the worktree made and checked, the project
manifest resolved, then the context asked (at most `ContextWait`), then
the task files written through the `os.Root`, then the `attempt.workspace`
event, setup and the launch, as today. The blocks are a view a provider
derived at that moment: never in the ledger, never a reason an attempt
fails or a task's state changes. A file touch is offered to the providers
after the session records it; denied paths never are.

**Decisions.** A question's life is unchanged (open, claimed by the writer
taking an answer's keys, resolved once they are written, the first answer
winning; no timer ends it). The router adds: a question is routed at most
once per server (the ids routed, kept per session, so a question set
aside and shown again with its id is not routed twice: an id is resolved
when its session shows it answered in the terminal or neither shows nor
keeps it, and is forgotten only once neither, so the ids the bound
forgets never show again); what a session shows and what it keeps are
read from one snapshot (`Info`, one lock acquisition; one host message),
never from two; a change of a session's attention is never lost to the
router (a set of dirty session ids, at most one per session, nothing
dropped), and a resolution is never lost after it (the router's
resolutions drop nothing; past `maxUnresolved` routed and untold ids a
new question is not routed instead), so every routed question is told
`Resolved` exactly once, however busy the other sessions were and
however slow the provider; a route's answer is
applied at most
once and only through the claim, so it is refused once a person answered,
and a person's answer is refused once the route's was taken; a route
types only the question's own choices or a line answering it; a held turn,
a withdrawn question and one answered in the terminal refuse a route as
they refuse a viewer; `Resolved` comes once per routed question, from the
attention changes themselves, so a question withdrawn with nothing
recorded is resolved all the same. A switchyard neither sends nor applies
`answer_as`, so it gains no way to type into a session. A hosted
session's answer travels in the host connection's server lane (`Tell`),
after `registered`, and the host applies it under its own claim: two
answers that cross (a viewer's over WebRTC and the route's) are ordered
there.

**Sync.** Every doc is handed over after its local commit: the store's
rename, the ledger's `COMMIT`, Dispatch's commit; a failed write hands
nothing. Each is marked pending under the lock that serialises its
commits (the data directory's observer mutex shared by the store and its
`Sub`s, `l.mu`, Dispatch's `s.mu`), and delivered with its state at
delivery, so a document's docs follow its commits and, within an epoch,
carry a `Rev` that increases with them; the pending set holds one entry
per document and drops nothing, and past its bound gives way to a
replay; an entry leaves it only when its whole document is acknowledged
(a failed one is tried again, in order, after a backoff, with no local
write needed), and `replayed` is told only after every document of its
replay was acknowledged. Every epoch begins with a replay of the whole state and ends it
with `replayed`, at each server's start and after an overflow, so a
store holds every allowlisted document's last state whatever it missed,
and no `Rev` of one server lifetime is compared with another's. The start-up's own commits are among them (the
hooks are installed before `api.New`'s first write). The
data directory stays the truth: nothing a sync store answers is ever
written back into it while a server runs, and `Restore` installs only into
a directory with no server data. No schema changes: the ledger stays at
version 5, Dispatch at its version, the data directory's layout as it is,
plus `ext/<name>/` for a provider's own state and `restore.lock` while a
restore or a starting server holds it.

**Export and restore.** The route copies the runs' records, then
snapshots the ledger, then Dispatch with the attachment files that
snapshot's live rows name (a snapshot taken again when one went
meanwhile), then reads the other documents and files, then writes the
manifest last, so every live attachment row in the archive has its file; a document written meanwhile
appears as one version or the other, never torn (the store renames), and
a run's record is never ahead of its ledger, which the restored server's
`syncRecords` mends. Restore reserves the data directory
(`restore.lock`, which a starting server takes too), checks everything
(no reserved path among the entries), writes everything into the staging
directory, syncs it, checks the directory is still fresh and that no
top-level name it will rename exists there (`hooks/` aside, moved aside
first), then renames into place, never over anything; nothing is written
into the data directory itself before every check has passed, and no
server starts on it until the reservation is let go.

**The protocol change.** `answer_as` is server to host only, and only to a
host whose register says `answerAs`, which a host publishing to a
switchyard never says. Its fields are bounded as an
`answer`'s are, plus a display name and a subject; the host refuses
anything outside them. `kept` is host to server only, on the attention
message, from a host that said `answerAs`: at most two question ids, an
attention message holding more or an invalid one dropped. Nothing else of the host connection, the viewer
protocol or the web client's frames changes; the web app sends nothing
new.

## Out of scope

- Every provider: identity and workspaces, push, the Code Context Graph,
  Switchman, sync, entitlements and billing, minted TURN and rooms, the
  scheduler (items ID1, MB1, CG1, SW1, CS1, OP1, CL1, AU1), and the two
  private scaffolds (P0, K0). They live in the private repositories.
- Runtime's methods, its native default, its wiring into `crew.Launcher`,
  `session.Process`, `proc.Run` and `hostagent`, and the ledger's `where`
  column (C10, `docs/plans/runtime.md`).
- The UI of any paid feature (a graph view, a scheduler page, routing
  screens, rooms): each comes with its item, rendered by this repository's
  web app from a capability (O16 below). Showing a route's destinations
  anywhere but the session's activity, durable decision records and the
  decision page (DR1, MB0).
- A grant narrower than admin or host (a reviewer's, for example): if ID1
  needs one, its plan proposes it as a change here.
- Reading Dispatch's change rows for replication, and applying another
  machine's changes (CS1's plan proposes the change to Dispatch it needs).
- A restore at start-up, or anything that waits on a provider before the
  server serves (article 8).
- An Export button in Settings (Owner decisions, E19).
- The release workflow's move to the premium repository (RL1).
- BK0's operating guide; this item gives it the capabilities route.

## Tests per surface

Every PR runs `make lint`, `go test -race -count=1 ./...` and `make
lint-static`; a PR that changes the web app also runs `npm --prefix web
run typecheck`, `npm --prefix web test` and `make test-e2e` (its spec alone
first while iterating), after `make web-install` in its worktree. No PR
changes the Python scripts, the desktop shell or a workflow, so the
scripts suite, the desktop checks and a workflow read are not needed
(`make preflight` runs all of them anyway). No dependency changes, so
`make notices` is not needed. Each test that guards a timing (a wait, a
queue, a race between two answers) waits on the mechanism, never on the
clock (AGENTS.md, Tests that do not flake), and is proven with
`-race -count=30` before its PR is marked ready.

**Go, by package:**

- `ext` (PR-1): `TestRegisterRefuses` (nil, a bad name, a second
  provider in a one-value slot, a repeated name in a list, a list past 16,
  a name another slot holds with a different or an incomparable value,
  after `Freeze`: each panics, with the slot named; the same pointer in
  two slots is taken); `TestRegisteredOrder`; `TestFreezeOnceAndCopies`;
  `TestAllListsEverySlot` (a provider registered only as the entitlement
  source is in `Providers.All` and `Registered`);
  `TestRegistryConcurrent` (registration and reads from 32 goroutines
  under `-race`); `TestRecordsRoundTrip` (every record through
  `encoding/json` with `DisallowUnknownFields`); `TestValid` (a table of
  each `Valid`: principal, route against its decision, context block, ICE
  server, notice, submit and launch requests, where).
- `ext/local` (PR-2): `TestDefaults` (each answers as main: no principal,
  the empty route, nothing kept, `ErrNothingToRestore`, allowed, no ICE
  server, no room) from 32 goroutines; `TestFill` (every empty slot gets
  its default, a registered provider and the lists are kept as they are).
- `app`, `cmd/conductor` (PR-1): `TestRunVersion`;
  `TestPlainBuildRegistersNothing`.
- `internal/cli` (PR-3, PR-4, PR-13): `TestServeReadsTheRegistryOnce`,
  `TestHostClosesTheRegistry`, `TestServeStartsProviders`;
  `TestExportThroughTheServer`, `TestExportOffline` (refused while the
  owner lock is held), `TestExportNeverOverwrites`, `TestExportSaysEnvIsKept`,
  `TestRestoreInstalls`, `TestRestoreRefusesServerData`,
  `TestRestoreDataDirAsServe` (a config's `dataDir`, `CONDUCTOR_DATA_DIR`,
  the legacy `conductor.d` and the default each chosen as `runServe`
  chooses, the directory printed), `TestRestoreReplacesHooks` (a fresh
  directory holding `conductor host`'s `hooks/`: installed, the
  archive's `hooks/` in place), `TestServeRefusesWhileRestoring` and
  `TestRestoreRefusesWhileServeStarts` (a restore paused before its
  renames through a seam: a `serve` and a second restore refused, nothing
  written; a `serve` paused inside its reservation: a restore refused),
  `TestExportOfflineResolvesCatalog` (`--data-dir` with `--config`: the
  inherited env value in the archive), `TestRestoreFromProviderStreams`
  (a fake whose `Restore` downloads the archive from an `httptest`
  server in chunks: installed, the bytes counted as they arrive),
  `TestRestoreFromProviderEndedShort` (the download failing halfway, and
  ctx cancelled while it waits on the server: the temporary file gone,
  the reservation released, the data directory as before, exit 1),
  `TestRestoreFromProviderHoldsTheReservation` (seams as the download
  starts, between the download and the install, and between two of the
  install's renames: at each, `restore.lock` the same file with the same
  pid and start time, a `serve` and a second restore refused; removed
  once, after the last rename),
  `TestRestoreFromProvider` (the default: nothing to restore, nothing
  changed; a fake's archive installed; a fake's partial archive, the
  catalog, a crew and a finished run's record with no database, installed
  and opened by `serve`, the crew and the run listed; the fake given its
  `RestoreRequest.Dir`, 0700).
- `internal/api`:
  - PR-3: `TestNewWithoutExtensionsIsTheFreeBuild`,
    `TestWithExtensionsKeepsDefaults`; `TestExtCallWait`,
    `TestExtCallPanic`, `TestExtCallInvalid`, `TestExtCallInFlight`;
    `TestExtQueueDropsOldest`, `TestExtQueueOrder`,
    `TestExtQueueShutdown`.
  - PR-4: `TestHostLaunch` (the same run as the route's; a managed crew;
    the note in the run's log; a switchyard refused),
    `TestHostSubmitLine` (never into a question), `TestHostSubmitAnswer`
    (first answer wins), `TestHostDataDir`, `TestStartOncePerValue` (one
    value in two slots started once; an entitlement-only provider
    started), `TestStartRetriedUntilItSucceeds` (an error, a panic, past
    `StartWait`: the default answered meanwhile, the given-up try's
    context cancelled before the next, the provider's slots its own from
    the call after a later try succeeds, in the same server; the backoff
    driven through a clock seam, never slept), `TestStartQueuesHeldUntilStarted`,
    `TestStartNeverOverlaps` (a `Start` gated shut that ignores its
    context: past `StartWait` its try is given up, its context cancelled
    and its `Host` refusing, and through any number of backoffs driven
    by the clock seam no second `Start` begins while the gate is shut;
    the gate opened, the next try begins only after it has returned;
    the provider listed `starting` meanwhile, one goroutine of it alive).
  - PR-6: `TestTokenChainUnchanged` (every kind of token, by header and
    by query, on a server and a switchyard, with no provider and with the
    default: the principal main gives), `TestProviderAskedLast` (a chain
    token and a revoked link never reach it), `TestProviderPrincipal`
    (admin and host grants; a switchyard's yard), `TestProviderPrincipalInReceipts`
    (a pause, a retry and a Dispatch create made with a fake's token: the
    ledger's command row and Dispatch's receipt and changes name
    `user:<subject>`; the workbench's still "admin"), `TestProviderHostRegistration`,
    `TestProviderRefused` (late, failing, panicking, a subject past 48
    bytes, no grant: 401 within `IdentityWait`),
    `TestProviderSubjectsNeverCollide` (subjects "admin", "merge" and
    "agent:x" kept as `user:admin`, `user:merge` and `user:agent:x`, apart
    from the workbench's, the merge's and a member's receipts; a 48-byte
    subject creates, edits and reads back a Dispatch record and a ledger
    command).
  - PR-7: `TestWebhooksUnchanged` (every existing `webhooks_test.go` test,
    unchanged, plus a table of entries whose bodies, headers and signatures
    equal main's construction, kept in the test), `TestSinkGetsEveryAttentionEvent`
    (10 sessions, 1,000 changes, each sink's in order), `TestBlockedSinkNeverHoldsTheHub`
    (a sink that never returns: the hub, the sessions, the webhooks and the
    other sinks go on; the drops counted), `TestSinkEventsFilter`,
    `TestProviderNoticesCannotLoop` (two providers, each a sink that
    lists `TypeProvider` and answers a notice with a notice of its own:
    both `Host.Notify`s refused, nothing delivered round; a sender and a
    taker: each notice delivered once, no echo; a sink with nil `Events`
    given no provider notice), `TestSinkPanicRecovered`,
    `TestSinksInMainsOrder` (the webhooks handed an entry before the run
    engine, as on main).
  - PR-8: `TestContextInTaskFile`, `TestContextBounds` (a block past its
    bound refused, nine blocks cut to eight, over `MaxTaskContext` the
    later dropped with the run log's note), `TestContextLateOrFailing`
    (the attempt starts within `ContextWait` with no block),
    `TestTaskContextServesTheBlocks` (the route's markdown has the
    section), `TestTouchesReachProviders` (server and hosted sessions,
    absolute paths, denied paths left out, a blocked provider dropping).
  - PR-9: `TestNoRouterHumansAnswer`, `TestRouteAppliedOnce` (a fake's
    answer typed once; `Route` called once per question though the entry
    repeats; the same answer again through `Host.Submit` refused),
    `TestPersonAnswersBeforeTheRoute`, `TestRouteRecorded` (a status entry
    for destinations, nothing for the empty route), `TestRouteBounds`
    (late, panicking, an answer whose input is no choice: nothing typed),
    `TestResolvedOnce` (answered, withdrawn, session ended),
    `TestRouteOncePerQuestionSetAside` (the session's own question A set
    aside by work, a bell's question B over it, A shown again at the
    done, as `TestAQuestionSetAsideByWorkShowsAgainAtTheDone`: one `Route`
    for A, one for B, `Resolved` for A once, at its answer),
    `TestResolvedOnWithdrawalWithNoActivity` (a status question withdrawn
    by the screen, no entry recorded, the terminal quiet after: exactly
    one `Resolved`), `TestResolvedAfterABurstOfOtherSessions` (a routed
    question, the 16 workers held in a gated `Route` by other sessions,
    changes from 1,000 more sessions, then the question withdrawn and its
    session quiet: once the gate opens, exactly one `Resolved` for it),
    `TestRouteOncePerQuestionSetAsidePastTheBound` (A set aside, 70
    questions routed and answered before A shows again at the done: A
    not routed again, `Resolved` once at its answer),
    `TestResolvedThroughABlockedConsumer` (a fake whose `Resolved` blocks
    on a gate at its first call, then 300 routed questions answered, more
    than any ring would hold: once the gate opens, every one told
    `Resolved` once, in order, none lost), `TestRouteSkippedAtTheBound`
    (the consumer held shut and `maxUnresolved` ids untold: the next
    question not routed, never told, and not routed after the gate opens;
    nothing past the bound held), `TestRouterNeverResolvesARestoredQuestion`
    (A set aside while B shows, then A shown again at the done, through a
    seam just after the router's read of the session: A not resolved,
    `Resolved` once at its answer), `TestResolvedWhenAnsweredInTheTerminal`
    (a routed question answered by keys typed in the terminal, the dialog
    still drawn and the program quiet after: `Resolved` once, with no
    further output), `TestAnsweredInTheTerminalNeverRouted` (a question
    consumed before the router's first read: no `Route`, no `Resolved`).
  - PR-10: `TestRoutedAnswerReachesAHostedSession` (a real host agent:
    applied once, the host's attention says so), `TestOlderHostUnsupported`
    (and its questions not routed), `TestHostedSetAsidePastTheBound` (a
    real host agent: A set aside, 70 questions routed between, A shown
    again: not routed again, `Resolved` once at its end),
    `TestHostedAnsweredInTheTerminalResolved` (a real host agent: a routed
    question answered in its terminal, the host quiet after: `Resolved`
    once, from the `answered` mark),
    `TestSwitchyardNeverRoutesAHostedSession` (a switchyard server:
    `Host.Submit` to a hosted session `ErrUnsupported`, no `answer_as`
    sent).
  - PR-11: `TestSyncAllowlist` (catalog, crews, records, pointers and
    commits reach a fake; the token file, `links/`, `tls/`, `hooks/` and
    `ext/` never), `TestSyncAfterLocalCommit` (the fake reads the file,
    the ledger's run row and Dispatch's receipt as each doc arrives and
    finds them committed), `TestSyncReplayAtStart` (a data directory
    with a catalog, crews, records and ledger runs made before the server
    starts: `replay`, each document once, the Dispatch `log` doc,
    `replayed`), `TestSyncOverflowReplays` (the fake's delivery held
    shut, more than `SyncPending` documents changed, one of them changed
    once more and quiet after: once opened, a new epoch's replay, that
    document with its last bytes, nothing missing), `TestSyncPendingOnce`
    (a document saved 100 times while delivery is held: delivered once,
    its last state), `TestSyncRetriesAFailedDelivery` (after the replay,
    the fake fails the one `Saved` of a document never written again, and
    the third part of a 4 MiB run record: with no further local write,
    the test's clock past the backoff, both delivered again whole, the
    record's four parts from one file), `TestSyncReplayedWaitsForEveryDoc`
    (the fake fails one document of the start-up replay: `replayed` not
    told until that document is acknowledged), `TestSyncLargeDocInParts` (a 4 MiB run record: four
    parts of one path, `Rev` and epoch whose bytes join to the file),
    `TestSyncEpochAcrossRestarts` (two server lifetimes against one fake
    that keeps its cursor: the second begins with a new epoch's replay, a
    record deleted while no server ran absent from it, and a save whose
    `Rev` equals one of the first lifetime's told apart by its epoch),
    `TestSyncDefaultKeepsNothing`,
    `TestSyncCatalogMasksEnv` (a saved agent's env value never in the
    doc, every value `catalog.RedactedValue`),
    `TestSyncStartupCommits` (a restart with an unfinished managed run
    and a catalog to mask: the recovery transition, the rewritten record
    and the masked catalog reach the fake, after its `Start`).
  - PR-12: `TestExportRoute` (admin only; a zip whose manifest and files
    match the data directory; a switchyard's links).
  - PR-15: `TestICEStaticUnchanged` (the welcome, `registered`, the paste
    answerer's servers and `GET /api/ice` equal main's for STUN only, for
    STUN with a TURN server and its credential, and for nine and twenty
    configured servers, every one kept, with no provider and with one
    adding eight), `TestICEOfferNeverAsksTheProvider` (a provider that
    records its calls: `GET /api/ice` makes none),
    `TestMediaServersReachViewersAndHosts`, `TestICENeverLeaksCredentials`,
    `TestMediaBounds`, `TestRoomDefault`.
  - PR-16: `TestCapabilitiesPlainBuild`, `TestCapabilitiesNamesProviders`
    (a provider still starting shown as `starting`, then `ready`; an
    entitlement-only provider listed by kind and name), admin only.
- `internal/crew` (PR-8): `TestTaskFileByteIdenticalWithoutContext`
  (goldens under `testdata/taskfile/` written by main's `fitTaskFile` in
  the PR's first commit, before the field exists: a work task, a review
  with a repair, several inputs, a checkpoint, a manifest, a cut file),
  `TestTaskFileContextSection` (the fence longer than the body's
  backticks), `TestContextCutFirst`, `TestEngineAsksContextOutsideTheLock`.
- `internal/session` (PR-9): `TestKept` (`Info.Kept`: the own question set aside and
  a held-over one listed, a shown one never, at most two),
  `TestKeptReadWithTheAttention` (A set aside and restored while `Info`
  is read in a loop: every snapshot shows A or keeps it, never neither;
  `-race -count=30`), `TestAnswerAsChoice`,
  `TestAnswerAsOnlyChoices`, `TestAnswerAsHeldTurn`,
  `TestAnswerAsAgainstAViewer` (the two at once, exactly one written,
  `-count=30`), `TestAnswerAsRecordsTheName`.
- `internal/proto` (PR-10): `TestHostAnswerAsBounds` (each field at and
  past its bound, exactly one of input and text, the largest message under
  `MaxHostMessage`), `TestRegisterAnswerAs`, `TestHostAttentionKeptBounds`
  (two ids taken, three or an invalid one refused, an older message
  without the field unchanged).
- `internal/hostagent` (PR-10): `TestAnswerAsApplied`,
  `TestAnswerAsInvalidDropped`, `TestAnswerAsNeverInTheControlLoop`,
  `TestAnswerAsQueueBounded` (a burst of text answers while the program
  reads nothing, stopped with SIGSTOP: at most four wait, the rest
  dropped and logged, one goroutine applying them, all ended with the
  connection), `TestSwitchyardAnswerAsRefused` (an uplink's register
  says no `answerAs`; a `HostAnswerAs` sent anyway types nothing; no
  `kept` sent), `TestKeptSent` (a question set aside while the screen
  shows another state: an attention message with only `kept` changed),
  `TestKeptSentWithItsAttention` (the forwarder held by a seam while A
  is restored and a retry is due: every attention message's `kept` and
  shown id from one `Info`, A in one or the other in each).
- `internal/signal` (PR-10): `TestHostedAnswerAs` (sent only to a host
  that said so), `TestHostedKept` (an attention equal to the shown one
  with a new `kept`: the kept ids taken, nothing recorded).
- `internal/store` (PR-11): `TestObserveAfterRename` (a failing rename:
  no event), `TestObserveInheritedBySub`, `TestPlainFileNoBytes`,
  `TestObserveInCommitOrder` (two writers of one document, through the
  root and through two `Sub`s, the first held after its rename by a
  seam while the second commits: queued in rename order, `Seq`
  increasing; `-race -count=30`).
- `internal/crew/ledger` (PR-11, PR-12): `TestOnCommitAfterCommit`
  (`beforeCommit` failing: no call; called once per commit, before the
  next transition commits), `TestOnCommitInOrder` (two transitions, the
  first held after its `COMMIT` by a seam: queued in commit order),
  `TestOnPrune`; `TestSnapshotWhileWriting` (50
  transitions during a snapshot; the copy passes `quick_check`),
  `TestSnapshotFileReadOnly`, `TestHeld`.
- `internal/dispatch` (PR-11, PR-12): `TestOnCommitInOrder` (two
  `Apply`s, the first held after its commit by a seam while the second
  commits: queued in commit order, `Rev` increasing); `TestSnapshot`,
  `TestSnapshotFile`, `TestSnapshotHashes` (live rows' hashes once each,
  a tombstoned row's left out).
- `internal/export` (PR-12): `TestRoundTrip` (a data directory with the
  catalog, crews, records, a finished managed run, a Dispatch project with
  a ticket, a comment and an attachment: exported, restored into an empty
  directory, opened by the ledger and Dispatch, every row and byte the
  same), `TestExportLeavesOut`, `TestRestoreRefuses` (not fresh, a hash or
  size that differs, an entry not listed, `..`, an absolute path, a
  backslash, a link, a duplicate, past `MaxFiles`), `TestRestoreStages`
  (a failure before the renames leaves nothing), `TestExportRecordsBeforeTheLedger`
  (through a seam between the records' copy and the ledger's snapshot,
  a run finished and a new run made: restored and served, every managed
  run's record equals its ledger's), `TestReserve` (exclusive; a second
  refused with the file named; released after the renames),
  `TestInstallUnderItsCallersReservation` (`Install` with a held
  reservation installs and leaves `restore.lock` in place for its caller
  to release; with a released one, refused before a byte is written),
  `TestExportKeepsCatalogEnv`, `TestExportResolvesInheritedEnv` (a
  configured agent with an env value, edited so that its saved override
  stores the mask and omits its adapter: exported, restored into a
  directory served with no config, the agent's env value and adapter the
  source's; restored where the config holds the same value, masked again
  at start), `TestRestoreRefusesReservedPaths` (an archive holding
  `restore.lock`, `tls/x`, `ext/p/x`, `workbench-token`, a `-wal` file, a
  `.tmp` file: each refused before a byte is written),
  `TestRestoreNeverRenamesOver` (a fresh directory holding an unrelated
  `config.json`: an archive with `data/config.json` refused before any
  rename, the file as it was; an archive without it installed, the file
  kept), `TestExportAttachmentSweptAfterTheSnapshot` (through a seam
  after Dispatch's snapshot, an attachment removed and a second store
  opened whose sweep deletes its file: the export takes the snapshot
  again and succeeds; restored and opened, every live attachment opens
  with its bytes, the removed one not found), `TestExportAttachmentStillMissing`
  (a live row's file deleted by hand: the export fails naming the hash,
  after three snapshots), `TestRestoreRefusesAMissingAttachment` (an
  archive whose database names a file it does not hold: refused before
  any rename).
- `internal/opencore` (PR-5, PR-14): `TestModuleKeepsTheRules`,
  `TestEachRuleCatchesItsPlant`, `TestPlatformTagsMatchTheToolchain`;
  PR-14 adds the `entitlements` fixture.
- `ext/exttest` (PR-17): `TestFakes` (each fake as documented,
  `FakeHost` and `FakeRuntime` among them);
  `TestSuitePassesEveryDefault` (`RunAll` over `ext/local`'s defaults);
  `TestSuitePassesEveryFake`; `TestSuiteCatchesABreach` (a provider that
  dials its service under the call's context, one that waits on its
  network on the call's path under a context of its own (the gate shut),
  one that says ok for an empty token, one that returns nine blocks, a
  `Start` that waits on its host: each failing check named, through a
  recording `exttest.T`), `TestRestoreMayStream` (a fake `Restore`
  streaming from an `httptest` server through the `Network`: passed;
  ended by ctx while the gate is shut: ctx's error).

**Web** (PR-10, PR-16): `web/app/utils/capabilities.test.ts` (the words for
none, some and a failed provider; `has`); `web/app/composables/useCapabilities.test.ts`
(fake timers and a stubbed fetch: a reply with a provider `starting`,
then one `ready`: read again, the words without "(starting)", and no
read after; nothing re-read while none is starting); Playwright `settings.spec.ts`'s
"in a browser, Settings says what the server is and who sponsors it" also
reads `[data-about-row="extensions"]`: "None: the open-source feature set"
on the e2e server, a plain build; and a test of its own that answers
`/api/capabilities` through `page.route`, first with a provider
`starting`, then `ready`, waits for the second request
(`page.waitForRequest`) and then for the row to read `ready`, with no
reload and the same token. PR-10 changes `protocol.ts`'s types
only; its typecheck, vitest and Playwright run as for any web change.

**By hand**, what tests cannot reach:

- C6, on two machines (the acceptance boundary): on the WSL box, a server
  with a crew, a finished managed run, a Dispatch project with tickets and
  an attachment; `conductor export`; the archive copied to the Mac (`ssh
  mac`) or the LAN box; `conductor restore` into an empty data directory
  there; `conductor serve` on it; in a browser, the crews, the run's
  Tasks view from the ledger, the tickets and the attachment's bytes as on
  the first machine. Recorded in the features entry with the commands and
  the versions.
- C1: `make build`, `bin/conductor serve` on a scratch data directory:
  the log has no extension line and the workbench works as before; the
  binary built from the PR-2 tree and from main before it give the same
  `conductor --help` and `conductor version`.

## Boxcar constraints

The eight constraints of the Program, section 2.5:

1. *Attempts start only through `crew.Launcher`, and a provider launches
   only through `ext.Host`.* Honoured: `Host.Launch` is the route's launch (`launchSaved`,
   `runs.LaunchNamed`), and the engine starts its attempts as today; no
   provider gets a way to start a process.
2. *A session drives its `session.Process`, and no code reaches past it.*
   Honoured: `AnswerAs` writes through the session's writer, as
   `AnswerInput` does; no new code touches a pid or a tty, and the one
   `syscall.Kill` stays in `internal/pty`.
3. *What an attempt needs is files in its worktree.* Honoured: context
   blocks are written into `.conductor/task.md` and `task.json` through
   the worktree's `os.Root` (and cut to `task-more.json`), nothing an
   attempt needs is kept only in memory, and `task_context` reads the
   files.
4. *An agent reaches the server by its address.* Not touched: no new
   channel between an agent and the server; `task_context` stays the HTTP
   route and the MCP tool over it.
5. *Checks run through `internal/proc` and record where.* Not touched:
   no check is run or changed.
6. *Platform code splits by platform tags, and the UI reads capabilities.*
   Honoured: the lint refuses any other tag; `GET
   /api/capabilities` says what is registered, and the web app reads it.
7. *Evidence rows get their `where` column in C10.* Not touched: the sync
   hook reads a run's pointer and adds no evidence; no schema changes.
8. *The host is a launch site too.* Honoured where this
   item reaches it: a routed answer reaches a hosted session through
   `answer_as`, handled in `hostagent` for `conductor host` and a server's
   uplink alike; Runtime's wiring in both is C10's.

## Open-core checklist

- [x] Article 1: no licence or plan check; the one question asked of a
  provider is whether it is registered, in one function (`local.Fill`).
- [x] Article 2: the free build registers nothing and runs every free
  default, which is main's behaviour, and gains export and restore.
- [x] Article 3: no flag, key or tag turns anything paid on; a provider
  exists only when another module compiles it in, and is the free default
  whenever its service does not answer.
- [x] Article 4: stop, inspect and revoke are untouched; export becomes a
  free command and route; answers keep their claim, first response wins,
  and a route types only a question's own choices.

## The PRs

Each PR is one issue and one branch `issue-N` cut from main, a draft PR
at its first commit, the checks of its surfaces before every push, its body
ticking the open-core checklist and naming the agent that made it; each
leaves main working and green on its own (a seam whose provider cannot be
registered yet runs its free default; what a later PR adds is refused with
words until then). Sizes are changed lines with tests, estimated from the
files they touch. **Parallel** means no common Go package, no common
directory under `web/`, and no common file under `docs/` or at the root,
`docs/features.md` aside, whose entry has a paragraph per PR precisely so
that parallel PRs merge (Docs, above). Item gating follows the Program,
section 2.2: an item's first PR waits until its prerequisite items are
Done, so C2 to C8's PRs wait for PR-1 to PR-5, and C9's for C2 to C8's.

| PR | Item | Title | Touches | Depends on | Parallel with | Size |
|---|---|---|---|---|---|---|
| PR-1 | C1 | ext, app: the registry, the eight interfaces and their records, the entry point; a plain build registers nothing | `ext`, `app`, `cmd/conductor`, `docs` | none | none | ~1,700 |
| PR-2 | C1 | ext/local: the free defaults and Fill | `ext/local`, `docs` | PR-1 | none | ~450 |
| PR-3 | C1 | cli, api: the composition root reads the registry once; every provider call guarded | `internal/cli`, `internal/api`, `docs` | PR-2 | PR-5 | ~850 |
| PR-4 | C1 | api, cli: ext.Host and the providers' Start | `internal/api`, `internal/cli`, `docs` | PR-3 | PR-5 | ~1,100 |
| PR-5 | C1 | opencore: the open-core rules as a lint, failing on a planted import | `internal/opencore`, `Makefile`, `docs` | PR-2 | PR-3, PR-4 | ~600 |
| PR-6 | C2 | api: the identity provider after the token chain; a principal's subject in receipts | `internal/api`, `docs` | PR-4, PR-5 | PR-13, PR-14 | ~850 |
| PR-7 | C3 | api: notification sinks over the event hub, the webhooks the first | `internal/api`, `docs` | PR-4, PR-5 | PR-13, PR-14 | ~1,000 |
| PR-8 | C4 | crew, api: context blocks in the task files; file touches to the context providers | `internal/crew`, `internal/api`, `internal/session`, `internal/signal`, `docs` | PR-4, PR-5 | PR-13, PR-14 | ~1,150 |
| PR-9 | C5 | session, api: Local.AnswerAs and the decision router over needs_input | `internal/session`, `internal/api`, `docs` | PR-4, PR-5 | PR-13, PR-14 | ~1,150 |
| PR-10 | C5 | proto, hostagent, signal, api, web: answer_as, a routed answer for a hosted session | `internal/proto`, `internal/hostagent`, `internal/signal`, `internal/api`, `web/app/utils`, `docs` | PR-9 | PR-13, PR-14 | ~750 |
| PR-11 | C6 | store, ledger, api: the sync hooks after every local commit | `internal/store`, `internal/crew/ledger`, `internal/api`, `docs` | PR-4, PR-5 | PR-14 | ~1,000 |
| PR-12 | C6 | export, ledger, dispatch, api: the export archive, GET /api/export and the restore installer | `internal/export`, `internal/crew/ledger`, `internal/dispatch`, `internal/api`, `docs` | PR-11 | PR-14 | ~1,350 |
| PR-13 | C6 | cli: conductor export and conductor restore | `internal/cli`, `README.md`, `docs` | PR-12 | PR-6, PR-7, PR-8, PR-9, PR-10, PR-14, PR-15 | ~650 |
| PR-14 | C7 | opencore, contributing: no entitlement call site outside ext | `internal/opencore`, `CONTRIBUTING.md`, `docs` | PR-4, PR-5 | PR-6 to PR-13, PR-15 | ~200 |
| PR-15 | C8 | api: the four ICE sites through the media provider; GET /api/ice keeps credentials | `internal/api`, `docs` | PR-4, PR-5 | PR-13, PR-14 | ~650 |
| PR-16 | C9 | api, web: GET /api/capabilities and the app card's Extensions row | `internal/api`, `web/app`, `web/e2e`, `docs` | PR-6, PR-7, PR-8, PR-10, PR-13, PR-14, PR-15 | none | ~400 |
| PR-17 | C9 | ext/exttest: a fake per interface and the contract suite; the stream's features entry completed | `ext/exttest`, `docs` | PR-16 | none | ~1,550 |

PR-6 to PR-12 and PR-15 share `internal/api`, so they go one after
another; their order is free (the table's is a suggestion), each needing
only C1. PR-13 and PR-14 touch no package they do.

**PR-1** (`ext`; docs): `ext.go` (package documentation: what a provider
is, how it registers, what it may do in `init` (register, nothing else),
the guard's rules from the provider's side, the stability of the API),
`registry.go`, `identity.go`, `decisions.go`, `context.go`, `notify.go`,
`continuity.go`, `entitlements.go`, `media.go`, `runtime.go`, `host.go`,
`valid.go`; `app`, and `main` through `app.Main`;
`TestPlainBuildRegistersNothing`, so the stream's first PR already
carries the test that a plain build's registry is empty; the `ext` and
`app` rows of `docs/repository-map.md` and of `docs/architecture.md`'s
Packages table, and "Adding things: A provider"; the features entry's
frame. Nothing calls `ext` yet; behaviour is main's: `main` does what it
did, from `app`. Tests: `ext`'s, `app`'s and `cmd/conductor`'s (above).
Go only.

**PR-2** (`ext/local`; docs): the defaults and `Fill`; the `ext/local`
rows. Tests: `ext/local`'s. Go only.

**PR-3** (`internal/cli`, `internal/api`; docs): `extensions`,
`local.Fill`'s call, `api.New`'s `WithExtensions`, `extCall`, `extQueue`,
the start-up line; no seam calls
anything yet, so every slot's default sits unused. The architecture's
Extensions section. Tests: PR-3's (above). Go only.

**PR-4** (`internal/api`, `internal/cli`; docs): `exthost.go`,
`launchSaved` out of `handleLaunchCrew` (the route unchanged, its tests
unchanged), `StartExtensions` and its call in `runServe`. `Submit`'s
`Input` and hosted sessions refused with words until PR-9 and PR-10;
`Notify`'s sinks none until PR-7. Tests: PR-4's. Go only.

**PR-5** (`internal/opencore`, `Makefile`; `docs/checks.md`,
`docs/repository-map.md`): the lint and its fixtures; `make lint` runs it.
C1's acceptance is observed once PR-1 to PR-5 are merged. Tests:
`internal/opencore`'s. Go only.

**PR-6** (`internal/api`; `docs/protocol.md`, `docs/architecture.md`):
C2. Tests: PR-6's. Go only.

**PR-7** (`internal/api`; `docs/architecture.md`): C3; `Host.Notify`
reaches the sinks. Tests: PR-7's, and every existing webhook test
unchanged. Go only.

**PR-8** (`internal/crew`, `internal/api`, `internal/session`,
`internal/signal`; `docs/protocol.md`, `docs/architecture.md`): C4. Its
first commit adds the task-file goldens from main's code, unchanged; the
next adds the field, and the goldens still pass. Tests: PR-8's. Go only.

**PR-9** (`internal/session`, `internal/api`; `docs/protocol.md`,
`docs/architecture.md`): C5 for server sessions; `Host.Submit`'s `Input`
lifted for them. Tests: PR-9's. Go only.

**PR-10** (`internal/proto`, `internal/hostagent`, `internal/signal`,
`internal/api`, `web/app/utils/protocol.ts`; `docs/protocol.md`): C5's
protocol change, in its three places, with its size limit and test.
Tests: PR-10's; Go and web checks.

**PR-11** (`internal/store`, `internal/crew/ledger`, `internal/api`;
`docs/architecture.md`): C6's hooks and the sync adapter. Tests: PR-11's.
Go only.

**PR-12** (`internal/export`, `internal/crew/ledger`, `internal/dispatch`,
`internal/api`; `docs/protocol.md`, `docs/architecture.md`): the archive,
the snapshots, the route, the installer. Tests: PR-12's. Go only.

**PR-13** (`internal/cli`; `README.md`): the two commands, the usage text,
the completion table. After it merges, the by-hand export and restore on a
second machine (C6's acceptance). Tests: PR-13's. Go only.

**PR-14** (`internal/opencore`; `CONTRIBUTING.md`, `docs/checks.md`): C7.
Tests: the `entitlements` fixture. Go only.

**PR-15** (`internal/api`; `docs/protocol.md`): C8. Tests: PR-15's. Go
only.

**PR-16** (`internal/api`, `web/app`, `web/e2e`; `docs/protocol.md`): the
route and the web app's first reader of it. Tests: PR-16's; Go and web
checks, `make test-e2e`.

**PR-17** (`ext/exttest`; `docs/repository-map.md`, `docs/features.md`):
the fakes and the suite, and, as the stream's last PR, the features entry
completed: each item's paragraph with what was observed (C6's by-hand
restore with its commands, machines and versions; the CI runs that showed
the empty registry; the suite passing against every default in this PR's
own CI). It waits for PR-16 so that it is last. Tests: PR-17's. Go
only.

## Owner decisions

Dated 2026-10-30. O9, O12 and O16 are open decisions of the Program
(section 3.2) that this plan answers; E1 to E21 are this plan's own
choices. Each is a default the owner can overturn with a dated row in the
Program's status log (its convention for a default, section 3.1); one
overturned is rewritten here before the PRs are cut. The owner records
the answers to O9, O12 and O16 in the status log, and E1, which departs
from section 2.4, as a row amending it.

- **O12, the package names.** `ext` and `app` at the module root, with
  `ext/local` for the defaults and `ext/exttest` for the suite. The split
  is forced (another module cannot import `internal/`, and the root package
  is in a cycle with `internal/api`), and the short names read well at the
  call site (`ext.RegisterNotificationSink`, `app.Main`).
- **O16, where the UI of paid features lives.** In this repository's web
  app, shown when `GET /api/capabilities` names the provider's kind, never
  because of a plan or a tier. One web build and one notices list stay,
  article 1 holds (the UI asks what is registered), and a fork that has no
  provider simply never shows those pages. C9 lands the route, the
  composable and the first reader; the pages come with their items.
- **O9, where Boxcar's code lives.** In this repository, under a `linux`
  platform tag, as the Program recommends: Boxcar is Apache-2.0 like this
  repository, a platform tag is allowed (the lint's `feature-tag` rule
  takes `linux`), and the host and the server gain it in one stream.
  Program item F's plan confirms it; nothing here depends on it.
- **E1, the identity provider after the token chain.** The Program says it
  is called first, with a refusal falling through to the chain. It is
  asked last here, for a token the chain does not know: the answer is the
  same for every token (a chain token is a local secret no provider
  issues), and the free path then never waits on a provider, which article
  8 asks. Section 2.4 is the specification and outranks this plan, so this
  reversal is proposed as a status-log row amending section 2.4's identity
  order, and C2's issue (PR-6) is not cut until the owner has recorded
  it; if the owner keeps the Program's order instead, PR-6 asks the
  provider first, its `ok` false falling through to the chain, and
  `TestProviderAskedLast` becomes its mirror.
- **E2, two grants.** `admin` (what the workbench token may do) and `host`
  (register a hosted session); a provider's principal maps onto authority
  that exists here. A narrower grant (a reviewer's) is ID1's to propose.
- **E3, the subject in receipts.** A provider principal's commands and
  Dispatch operations are kept under `user:<subject>`, its subject at most
  48 bytes, so the actor fits Dispatch's `MaxActor` (60) and never equals
  "admin" (the workbench token's), "merge" or `agent:<member>`: two actors
  never share receipts, and no valid subject is refused by Dispatch.
- **E4, Runtime's slot only.** C1 declares Runtime with `Where` so the
  registry has all eight slots; its methods and default are C10's, added
  before anything implements it, which keeps this plan out of
  `docs/plans/runtime.md`'s design.
- **E5, the fakes and the suite in C9.** The seams' PRs test with doubles
  of their own; `ext/exttest` comes once every seam's rules exist, as the
  Program places it.
- **E6, the lint as a Go test, with two rules more.** `go/parser` reads
  imports whatever a file's tags, which a text search cannot; besides the
  Program's import rule, it checks build tags (article 6's proof) and that
  `ext/` stays on the standard library (so the cloud repository can use
  its types). It runs in `make lint` and in CI's race suite, with no
  workflow change.
- **E7, one place that asks.** `local.Fill` is the only code that asks
  whether a slot is registered (the composition root and `restore
  --provider` call it). A provider whose `Start` fails answers the default
  in all its slots until a later try succeeds (backoff from 1 s to 5
  minutes, for the server's life), so it resumes with no restart (article
  8), and the capabilities route says `starting` meanwhile.
- **E8, no plain line into a hosted session.** `Host.Submit` types a line
  only into a server session; a hosted session takes routed answers
  (`answer_as`) and nothing else, so the protocol gains one message, not a
  remote typing channel.
- **E9, a route types only a question's choices.** A routed choice must be
  one of the shown question's options; arbitrary keys never travel through
  a router.
- **E10, ICE servers added, never replaced.** The configured servers come
  first, all of them, as main sends them, and a provider's follow, at most
  eight of its own; `GET /api/ice` answers the configured STUN URLs only,
  as main does, and never asks the provider.
- **E11, a provider's notice.** Typed `provider`, sent only to the sinks
  that list `TypeProvider`, and a provider with such a sink cannot send
  notices, so no notice loops between providers; a webhook cannot list it
  (the config's validation knows no such event), so webhooks behave as
  before.
- **E12, a route in the activity.** A route that names destinations is one
  `status` entry in the session's activity; the empty route records
  nothing, so the free build's activity is unchanged.
- **E13, what sync is told.** The catalog, the crews, the runs' records,
  each run's pointer from the ledger and each Dispatch commit's entities;
  never a token, a saved agent's env value (the catalog goes masked, as
  `GET /api/catalog` shows it), a share link's hash, a certificate, the
  hook assets or a provider's own directory. The bytes of a plain file
  never leave `internal/store`. Each document is delivered with its state
  when delivered, in parts past 1 MiB, and every epoch (each server's
  start, an overflow) begins with a replay of the whole state, so a store
  can always catch up from what it is given. Export, the person's own
  file, keeps the catalog whole, resolved against the configuration so it
  stands without it, and says so.
- **E14, export's contents and restore's target.** The data directory
  minus its secrets, locks, temporary files and providers' state, the two
  databases snapshotted; restore only into a data directory with no server
  data, reserved for the restore's whole run (`restore.lock`, which a
  starting server honours), into the directory `serve` would resolve,
  staged and renamed. The command is `restore`, not `import`: the
  harness already calls a result kept by a continuation "imported".
- **E15, a provider's restore only at a person's word.** `conductor restore
  --provider`, with no server running; never at start, where it would make
  the server wait on a service. `Restore` is given its own directory (it
  is never started there) and may return any part of `data/` the
  installer's checks pass; what it must hold to be useful is CS1's. It is
  the one call that may reach the service on its own path, for as long as
  its download takes, ended by the person and leaving nothing behind.
- **E16, capabilities for the admin.** The route needs the workbench token
  (BK0 may open it to an agent later); a provider whose `Start` has not yet
  succeeded is listed as `starting`, so Settings never claims a feature
  that is not there.
- **E17, the features entry's layout and the meaning of parallel.** One
  paragraph per PR, written in PR-1, so parallel PRs merge; parallel
  otherwise means no common package, web directory or doc file.
- **E18, export through the server, or offline when none runs.** The
  route snapshots consistently while the server writes; `--data-dir`
  serves a stopped server, and is refused while one holds the ledger.
- **E19, no Export button in Settings here.** The acceptance names the
  command; a button that downloads `GET /api/export` is one small PR the
  owner can ask for.
- **E20, provider names and list sizes.** 1 to 40 of a-z, 0-9 and '-', and
  at most 16 providers in a list, which bounds the capabilities reply and
  the hub's fan-out.
- **E21, the waits and queues.** Identity 1 s, a route 5 s, context 5 s,
  ICE 2 s, a start 5 s, a delivery 10 s (the suite checks that a call
  answers locally by its network, never by a time); queues of 256 for sinks (a
  webhook's), 1,024 for file touches, a pending set of 65,536 sync
  documents (a replay past it), a set of dirty sessions for the router
  (one entry per session, nothing dropped) with 16 routed at once, 4
  routed answers waiting on a host; one `Start` at a time per provider; 16
  direct calls in flight per provider and kind. They bound a hang, not a
  normal answer: a provider answers from what it holds (article 8), and
  the contract suite checks that it does.

No question is left open: each answer above lets the plan stand.

## Acceptance

Each item's boundary (the Program, section 4.2), and how it is observed;
none is ticked, each is seen:

- **C1.** PR-1 to PR-5 merged; on main, CI's go job green with every test
  of main passing unchanged in what it asserts, `TestPlainBuildRegistersNothing`
  green (the registry is empty), and `TestEachRuleCatchesItsPlant` green
  (the lint fails on the planted import, and `TestModuleKeepsTheRules`
  shows the tree passes); by hand, a plain build serving as before (Tests
  per surface).
- **C2.** `TestTokenChainUnchanged` (with no provider, every token's
  principal is main's) and `TestProviderPrincipalInReceipts` (the fake's
  subject in the ledger's and Dispatch's receipts) green on main.
- **C3.** Every existing webhook test and `TestWebhooksUnchanged` green;
  `TestSinkGetsEveryAttentionEvent` and `TestBlockedSinkNeverHoldsTheHub`
  green.
- **C4.** `TestTaskFileByteIdenticalWithoutContext` green against main's
  goldens; `TestContextInTaskFile` and `TestContextBounds` green.
- **C5.** `TestNoRouterHumansAnswer` and `TestPersonAnswersBeforeTheRoute`
  green (first response wins); `TestRouteAppliedOnce` and
  `TestRoutedAnswerReachesAHostedSession` green.
- **C6.** The by-hand export on one machine and restore on another (Tests
  per surface), recorded in the features entry; `TestRoundTrip` and
  `TestSyncAfterLocalCommit` green.
- **C7.** `TestModuleKeepsTheRules` with the `entitlements` rule green on
  main (no call site outside `ext` in a file the binary compiles), and
  its fixture caught.
- **C8.** `TestICEStaticUnchanged` and `TestMediaServersReachViewersAndHosts`
  green.
- **C9.** The Settings row in Playwright on the plain build, and in a
  browser by hand; `TestSuitePassesEveryDefault` green, the suite passing
  against every default.

Done for an item means its PRs merged and its observation recorded in the
features entry (PR-17 for the last ones); the Program's status row moves to
Done then, never on the table alone. Together C1 and C9 observe the two
clauses of the R0 gate this stream owns: a plain build of this repository
is the free binary and registers nothing, and every default passes the
contract suite.

What needs the owner: the review of this plan and its decisions (O9, O12
and O16 among them, and their rows in the status log, and E1's row
amending section 2.4's identity order, before C2's issue); cutting the
seventeen issues; a second machine for the restore check (the Mac over
`ssh mac`, or the LAN box); the merges.
