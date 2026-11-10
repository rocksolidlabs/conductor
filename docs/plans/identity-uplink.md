# The identity provider and the uplink: the host token, a credential that fails closed, host connections rechecked and renewed (#775)

Since the extension seams (`docs/plans/ext.md`, C2) an identity provider
registered through `ext` is asked about a bearer token the server's own
chain does not know (`internal/api/identity.go`, `providerPrincipal`),
through the guard, and a viewer's WebSocket or the events stream it
admitted is asked about again every 30 s (`principalWatch`). Four things
are still missing for a provider that signs a machine in. It cannot
learn the account the person entered: the host token of Settings (the
configured switchyard token, `cfg.Rendezvous.Token`) reaches only the
uplink (`internal/cli/serve.go`, `newUplink`), and `conductor host`
freezes the registry and uses none of it (`internal/cli/host.go`). It
cannot choose what the uplink presents, so a token that is an account's
one-time code would be shown to the switchyard as it is. A host
connection it admitted is checked once, at registration, and never
again. And the server cannot say who it is signed in as.

This plan is the row of issue #775 and implements exactly its six parts
and its tests. It was written against `main` at 175e0be5 (PR #763,
2026-11-10) after reading `ext` (`identity.go`, `host.go`,
`registry.go`), `ext/exttest` (`checks.go`'s `Identity`, `host.go`'s
`FakeHost`), `internal/api` (`auth.go`'s `tokenPrincipal`,
`identity.go`, `extensions.go`'s `guardIdentity` and `extFailed`,
`exthost.go`'s `tryStart`, `capabilities.go`, `guide.go`, `ws_host.go`,
`desk_host.go`'s `deskMessage`, `publish.go`), `internal/hostagent`
(`agent.go`'s `dialAndRegister`, `controlLoop`, `serveConn`, `connLost`
and `RefusedError`, `uplink.go`, `desk.go`), `internal/cli` (`host.go`,
`serve.go`), `internal/signal` (`hub.go`'s `Register`, `hosted.go`'s
`HostDisconnected`), `internal/config/deny.go`,
`internal/proto/hostmsg.go`, `docs/protocol.md` (Host control
connection, HTTP API), `web/app/utils/protocol.ts`,
`web/app/utils/capabilities.ts`, `web/app/composables/useCapabilities.ts`
and `web/app/components/AboutCard.vue`. It is reviewed before its code;
its one PR is the issue's.

The names follow the railroad: Conductor is the application, the
Switchyard the server in its hosted mode that admits hosts; "workspace"
appears only in an example detail, where it names the paid tenant.

**What is on `main` and what is not.** Every prerequisite is on `main`:
C2's `IdentityProvider`, its guard and the 30 s recheck (`identity.go`),
`ext.Starter` and the server's supervision of it (`exthost.go`), `GET
/api/capabilities` and the About card's Extensions row (C9). Nothing this
row needs waits on another item. Issue #776 (`docs/plans/decide-grant.md`,
the stream's other open-source row) follows this row and shares `ext`,
`internal/api`, `web/app` and `docs`: this plan owns `ext.Host.HostToken`
and `Mode`, the optional identity methods and the capabilities row's
`detail`; #776 builds on them and is cut after this PR merges.

## Articles it serves

1. No licence check: the only question asked is whether an identity
   provider is registered and has the optional methods. The repository
   learns no token prefix: the provider says what it owns
   (`OwnsHostToken`).
2. Useful on its own: a plain build registers nothing, presents the
   configured token as today, rechecks nothing (no connection has a
   provider's principal) and shows no detail.
3. Paid features gated by hosted dependencies: nothing here is a paid
   feature; what the provider presents comes from what its service gave
   it, and no flag in this repository changes that.
4. Revoke strengthened, never weakened: a host connection a provider no
   longer admits is closed within the recheck's period, as a viewer's
   is; stop, inspect, export and approvals are untouched; a refused
   credential never turns into an open host's admission (what a revoked
   host's session becomes is stated under Events, Recheck).
5. Dependencies one way: `ext` stays standard library only; no new
   module, no import of a private one; the open-core lint
   (`internal/opencore`) is unchanged and passes.
6. No feature build tag: none is added.
7. The free default is what `main` does: with no provider, or one
   without the optional methods, or one that does not own the configured
   token, the uplink presents the configured token exactly as today.
8. A provider unreachable answers the free default: every ask goes
   through a guard (a wait, a recovered panic, the default on any
   failure, nothing remembered), and the seam's words now say that a
   provider whose admission has ended answers ok false, the free default
   mid-session, while an error is only for a moment it cannot tell.
9. `CGO_ENABLED=0`, cross-compiled: unchanged; no store is added.
10. The repository contract: this plan, one issue, one branch, one PR;
    the `host_token` message and `registered`'s `renews` touch
    `internal/proto`, `web/app/utils/protocol.ts` and `docs/protocol.md`
    together, with their bounds and tests.
11. Names: Switchyard keeps its meaning; "workspace" only in the detail's
    example, as the paid tenant.
12. Execution behind one seam: not touched; `conductor host` still
    starts its command through the native Runtime, whatever is
    registered.

## Why and what happens

A person signs a machine in by putting what their identity service gave
them into Settings' Host token (or `conductor host --token`). Only the
machine's owner writes that field, which is not true of anything a
browser presents, so it is the root of trust for signing a server in.
After this row:

- The identity provider is handed that token and told what kind of
  binary runs it (`server`, `switchyard` or `host`), in `conductor serve`
  and in `conductor host` alike; `conductor host` starts the provider.
  No other provider is handed the token.
- When the configured token is the provider's own (a one-time code), the
  switchyard never sees it. The uplink presents what the provider gives
  (a credential it holds), and when the provider gives nothing in time,
  presents nothing: the session is an open host, as a build with no
  token is, or is refused where the switchyard admits no open hosts (a
  refused server publication is owed and the next Share publishes it; a
  refused `conductor host` tries again for a bounded time).
- A host connection a provider admitted is asked about again every 30 s
  and closed `access revoked` once the provider no longer admits it.
- When the provider replaces a short-lived credential, the replacement
  is handed to the switchyard over the live host connection
  (`host_token`), so hosted sessions and their viewers do not drop.
- Settings → The app shows who the server is signed in as, beside the
  provider: "signed in as Ana · workspace Acme".

A build with no provider registered shows and does nothing new.

## Renewal: a message on the live connection, not a reconnect

Before adding a message, the narrower design was weighed: no
`host_token`, the host closing its connection and registering again
with the new credential before the old one lapses, and the switchyard
checking the credential at registration and at the recheck only. It
would meet "rechecked and renewed" with fewer orderings, and the host
agent does survive a reconnect. It is not taken, because a reconnect is
not invisible to anyone watching the session:

- On the host, a control connection's end (`serveConn`'s deferred
  `connLost`) closes every viewer's peer (`closeAllPeers`), withdraws
  the connection's routed answers and what link viewers asked for and is
  still under way (`creds.lost`), and `controlLoop` waits its backoff
  before it dials again.
- On the switchyard, `HostedSession.HostDisconnected` closes every
  viewer with `host_disconnected` and marks the session
  `host_disconnected` until the host is back.

So every replacement of the credential would cut every viewer of every
hosted session off, which is the drop part 4 of the issue exists to
prevent. The message stays, made as narrow as it can be:

- It is sent only where the switchyard said, in `registered`, that it
  takes it (`renews`, set only on a connection admitted through a
  provider's principal, a session's or a desk's). An older switchyard
  never says so, never rechecks a host connection, and is never sent
  one; nor is a desk's connection on it closed for one.
- The switchyard checks a renewal against its own chain before any
  provider sees it, bounds it per connection, asks through slots of its
  own, and takes it under the same per-connection turn as the recheck,
  so a renewal and a recheck never overlap (no generations, no verdict
  to drop).
- The provider says how early it answers a replacement
  (`ext.UplinkTokenLead`), so the host's 30 s ask always comes before a
  recheck can find the old credential lapsed.

What it leaves out is listed under Out of scope.

## The changes

All in this repository (`rocksolidlabs/conductor`).

### `ext`

`ext/host.go`. `Host` gains two methods, after `Log`:

```go
// HostToken is the configured host token of the binary that runs the
// provider: Settings' Host token (rendezvous.token) on a server that
// publishes to a switchyard, conductor host's --token
// (CONDUCTOR_HOST_TOKEN); "" for none, on a switchyard, and on the Host
// of every provider but the one in the identity slot. Only the
// machine's owner writes it. It is never logged.
HostToken() string
// Mode is the composition root that runs the provider: ModeServer,
// ModeSwitchyard or ModeHost, so a provider enrols as the kind of
// device its binary is.
Mode() string
```

with `ModeServer = "server"`, `ModeSwitchyard = "switchyard"`, `ModeHost
= "host"`. `Starter`'s comment says Start is called in host mode too,
for the identity provider alone, before the host registers. `DataDir`'s
comment names the host's directory (below) and says that in host mode
it is shared by every `conductor host` process of the user on the
machine, which may run at once, each with its own Start: the host takes
no lock and gives no exclusivity, so a provider that keeps state one
process must own (a one-time code redeemed once) takes its own lock in
that directory.

`ext/identity.go`. Two optional interfaces beside `IdentityProvider`,
and the bounds:

```go
// UplinkIdentity is an identity provider that chooses what the uplink
// presents to a switchyard. Both are answered from what the provider
// holds, never with a round trip, and are asked through a guard.
type UplinkIdentity interface {
	// OwnsHostToken says configured, the host token, is the provider's
	// own: a pure check of its form. A late or panicking answer counts as
	// owned.
	OwnsHostToken(configured string) bool
	// UplinkToken is what to present to the switchyard server (its URL,
	// as the uplink dials it) for configured: ok false for nothing. When
	// the provider replaces a credential, it answers the replacement at
	// least UplinkTokenLead before the one it replaces stops being
	// admitted. It never answers configured itself when it owns it.
	UplinkToken(server, configured string) (token string, ok bool)
}

// IdentityDetail is an identity provider that says who the server is
// signed in as: one line of at most MaxIdentityDetail characters
// ("signed in as Ana · workspace Acme"), "" for nothing.
type IdentityDetail interface{ Detail() string }

const (
	MaxHostToken      = 2048 // bytes, proto.MaxHostToken
	MaxIdentityDetail = 80   // characters (runes)
	// UplinkTokenLead is how long before a credential stops being
	// admitted its replacement is answered: the host asks every 30 s and
	// the switchyard takes a renewal within two IdentityWaits, so this
	// leaves the rest for the connection's own queue.
	UplinkTokenLead = 2 * time.Minute
)

// ValidHostToken reports whether t can be presented: 1 to MaxHostToken
// bytes of RFC 6750's token68 characters (A-Z a-z 0-9 - . _ ~ + / =).
func ValidHostToken(t string) bool
```

`IdentityProvider`'s comment (part 6 of the issue) says: a provider
whose admission has ended (its lease with its service past, its
workspace lapsed, its own registration revoked) answers ok false, the
free default, which ends the token's connections at the next recheck; an
error is only for a moment it cannot tell, and ends none.

`ext/exttest`. `checks.go`'s `Identity` doc and its finding's words say
the same; its check with the network just blocked is unchanged (a
provider then still holds a current lease, and ok false there is still a
finding). `host.go`: `FakeHost` gains `HostToken`, `Mode` and the
setters `SetHostToken(string)`, `SetMode(string)` (default `""` and
`ModeServer`). The suite does not check the optional methods (out of
scope).

### `internal/proto` and its two companions

`internal/proto/hostmsg.go`: host to server,

```go
// HostTokenType renews the credential a host connection was admitted
// with (HostTokenMsg): {"t":"host_token","token":"…"}. Sent only on a
// connection whose registered said Renews.
HostTokenType = "host_token"

type HostTokenMsg struct {
	T     string `json:"t"`
	Token string `json:"token"`
}

const (
	MaxHostToken        = 2048 // ext.MaxHostToken
	MaxHostTokenMessage = 2560 // the message, encoded
	// HostTokenEvery is the least time between two host_token on one
	// connection; the host sends one at most every 30 s.
	HostTokenEvery = 15 * time.Second
)

// ParseHostToken decodes a host_token, refusing a message past
// MaxHostTokenMessage before it is decoded, an unknown field, and a
// token that is empty, past MaxHostToken or not token68.
func ParseHostToken(data []byte) (string, error)
```

and `Registered` gains `Renews bool json:"renews,omitempty"`: the
switchyard rechecks this connection's credential and takes `host_token`
on it (absent from an older switchyard, and on a connection of the
workbench token, a host token or an open host).

A token within its bound always fits the message's: token68 needs no
JSON escape. `web/app/utils/protocol.ts`: `HostTokenMsg { t:
'host_token'; token: string }`, `HOST_MAX_TOKEN = 2048`,
`HOST_MAX_TOKEN_MESSAGE = 2560`, `HOST_TOKEN_EVERY_MS = 15_000`, and
`HostRegisteredRenews { renews?: boolean }` beside
`HostRegisteredDecisions`. `docs/protocol.md`, Host control connection:
the `host_token` row (who sends it, only after `renews`, its bounds, the
one-per-15 s bound, what the server does with it), `registered`'s
`renews`, and the recheck of a provider-admitted host connection (closed
`4403 access revoked`).

### `internal/api`

- `auth.go`. `tokenPrincipal`'s first three steps (the workbench token,
  each host token, a link, an expired or revoked one included) become
  `chainPrincipal(tok) (principal, bool)`, `known` true for any token
  the chain knows; `tokenPrincipal` calls it and asks the provider only
  when it is false, as today.
- `identity.go`. `hostAllowed` becomes `hostPrincipal(r, tok)
  (principal, bool)`, the same check returning the principal, so the host
  connection knows its subject. A connection's credential is a
  `watchedToken`: the token and a mutex that is the connection's
  credential turn. `principalWatch` is generalised: it takes the
  `*watchedToken` and the grant the connection needs (`admin` for a
  viewer's WebSocket and the events stream, as today, whose token never
  changes; `host` for a host connection), and holds the turn from its ask
  until its verdict is applied, so it always asks about the token current
  when it has the turn and no renewal runs meanwhile.
- `ws_host.go`. A host connection admitted through a provider's
  principal (`subject != ""`), a session's or a desk's, keeps a
  `watchedToken` in its `hostConnState`, says `renews` in its
  `registered`, and runs the watch: when the provider no longer admits
  it, the connection is closed `proto.CloseForbidden` (4403) `access
  revoked`. A connection of the workbench token, a host token or an open
  host has none, says no `renews` and is never asked about.
  `handleHostMessage` gains `case proto.HostTokenType`: `hostToken(hc,
  data)` (Events, below). `hostConnState` gains `tokenAt`, when its last
  `host_token` came (the server's clock seam, `extClock`).
- `desk_host.go`. `deskMessage` admits `proto.HostTokenType`.
- `extensions.go`. A renewal's ask is a kind of its own,
  `kindIdentityRenew` (`identity-renew`), with its own `extInFlight`
  slots, so renewals never take the recheck's or a request's slots, and
  `extFailed` never quotes its words, as for `identity` and
  `identity-recheck`. `guardIdentity` gains `renew(ctx, tok)` (as
  `recheck`, in that kind) and `detail(ctx) (string, bool)` through
  `extCall` (kind `identity`, `ext.IdentityWait`; refused unless one
  line, 1 to `ext.MaxIdentityDetail` runes, no control character), asked
  only once the provider has started and only of one that is an
  `ext.IdentityDetail`. `extFailed` quotes no text for a failed Start of
  the provider in the identity slot (kind `start`, that name): its error
  or panic may carry the host token it was handed.
- `capabilities.go`. `capabilities(withDetail bool)`; `capabilityRow`
  gains `Detail string json:"detail,omitempty"`, set on the identity
  provider's row from `detail` only when `withDetail`, which the route
  passes and the guide does not. The route's comment says the identity
  provider is asked that one thing, through the guard.
  `capabilitiesReplyMax` (8 KiB) still holds (one detail of 80 runes is
  at most 320 bytes).
- `guide.go`. `guideContent` calls `capabilities(false)`: the guide's
  providers rows, which a session's agent reads, never say who the
  server is signed in as, and reading the guide asks no provider.
- `exthost.go`. `extHost` implements `HostToken` (`cfg.Rendezvous.Token`
  when `cfg.RendezvousServer()` is set and the Host is the identity
  slot's provider's, by name; `""` otherwise, on every other provider's
  Host and on a switchyard) and `Mode` (`ModeSwitchyard` when
  `cfg.Switchyard.Enabled`, else `ModeServer`). `extStarter` gains
  `tried`, closed when its first try of `Start` has returned or been
  given up (`tryStart`'s `ext.StartWait`, the server's clock seam
  `extAfter`), whichever comes first. The server gains
  `ProviderStarted(ctx, name) bool`: for a provider that is no
  `ext.Starter` it answers true at once; otherwise it waits for `tried`
  (or ctx) and answers whether a try has succeeded.
- `publish.go`. A publication refused because the credential withheld an
  owned token (an error with `CredentialWithheld() bool` true, read as
  `Retryable` is) is owed, as one the switchyard stopped is
  (`pubStopped`, whose comment now says "owed"): the next Share
  (`republish`) publishes the same running session again.

### `internal/hostagent`

- `uplinkcred.go` (new). `UplinkCredential`, the one guarded door to the
  provider's `OwnsHostToken` and `UplinkToken`, used by both composition
  roots: `NewUplinkCredential(p ext.IdentityProvider, name, server,
  configured string, started func(context.Context) bool, log)`, nil when
  `p` is no `ext.UplinkIdentity` (the configured token, as today). Its
  guard follows the server's rules (`ext.IdentityWait`, a recovered
  panic, the default on any failure, failures logged at most once a
  minute by kind and count, never the provider's words or the token);
  concurrent asks of one method share one call in flight, and an ask
  while that call is late answers the default at once. Its methods:
  `Present(ctx) (token string, withheld bool)`, what a registration
  presents (the table under Events), bounded by a deadline of its own,
  `ext.StartWait` plus two `ext.IdentityWait`, whatever ctx its caller
  has (ctx can only shorten it), `withheld` true when it presents nothing
  for a token the provider owns or may own; and `Renewed(ctx) (string,
  bool)`, UplinkToken's token when it answers one in time that
  `ext.ValidHostToken` takes. Both refuse an answer equal to the
  configured token (`share.Equal`) whenever `OwnsHostToken` said true or
  could not be answered, as a failure.
- `agent.go`. `Options` gains `Credential *UplinkCredential` and, for
  tests, `after func(time.Duration) <-chan time.Time` (nil is
  `time.After`), the clock of the waits below. `dialAndRegister` asks
  `Present` before it dials, in place of reading `opts.Token`, and keeps
  what it presented (`a.presented`, under `a.mu`); no header when it is
  `""`; a refusal of a withheld registration is a `RefusedError` with
  `Withheld` true (`CredentialWithheld`). `Run`'s first registration,
  when refused so with 401, is tried again with backoff (1 s doubling to
  `ReconnectMax`, through `after`), each try asking `Present` anew, for
  at most `firstCredentialWait` (2 minutes) while the provider signs in;
  past it, `Run` returns the refusal as today. `serveConn` begins
  `renewToken`, which every 30 s (`uplinkTokenEvery`, through `after`)
  asks `Renewed`: on a connection whose `registered` said `renews`, an
  answer other than `a.presented` is sent as `host_token` in the
  connection's own lane and becomes `a.presented`; on one that presented
  nothing, an answer closes the connection (normal closure) so
  `controlLoop`'s reconnect presents it; on any other (a configured
  token's, or one at a switchyard that did not say `renews`) nothing is
  sent, and the next reconnect presents what `Present` then answers. A
  run's desk's connection does the same (its `serveConn`).
- `uplink.go`. `Uplink` gains `Credential` and passes it into every
  publication's and desk's `Options` (`options`, which `OpenDesk` uses).
- `exthost.go` (new). The host mode's provider: `StartIdentity(ctx, p,
  name, HostIdentity{Token, StateDir, Log, after})` returns `started
  func(ctx) bool` and `stop func()`, and supervises `Start` by the
  server's rules (`ext.StartWait`; backoff 1 s doubling to 5 minutes;
  never two tries at once; a given-up try's `Host` refusing every call
  `context.Canceled`). Its first try's outcome closes `tried` at the try's
  return or its give-up at `StartWait`, and `started` waits for that as
  `ProviderStarted` does (true at once for a provider that is no
  `ext.Starter`). Its `ext.Host` has `HostToken` the `--token`, `Mode`
  `ModeHost`, `DataDir` `<state>/ext/<name>` made 0700, `Log` the host's
  logger with `provider=<name>`, and `Launch`, `LaunchTurn`,
  `LaunchApproved`, `Submit`, `Notify` and `Decision` answering
  `ext.ErrUnsupported` (a host launches nothing and keeps no ledger). A
  failed try is logged by its kind and count alone, never its error's or
  panic's text. `stop` cancels the try and waits for it at most
  `ext.StartWait`.

### `internal/config`

`deny.go`. `HostStateDir() (string, error)`: `$XDG_STATE_HOME/conductor`
when that is set and absolute, else `~/.local/state/conductor`, the one
place `conductor host` keeps its provider's directory. `machineFiles`
(a server's sessions) and `LocalFileDeny` (a hosted session's) both
name it as a denied directory, so no session's file read, save or editor
reaches what a provider keeps there, with the links on the way followed
as for every other entry (`withTargets`).

### `internal/cli`

- `host.go`. `runHost` keeps `extensions()`, and when it holds an
  identity provider that is an `ext.Starter` starts it (`StartIdentity`)
  before `runHostAgent`, with `config.HostStateDir()`, and stops it once
  the host has ended (deferred). `Options.Credential` is
  `NewUplinkCredential(p.Identity, name, *server, *token, started, log)`,
  `name` the provider's registered name. The comment that says nothing of
  the registry is used is replaced; the Runtime stays native.
- `serve.go`. `newUplink` sets `Credential: NewUplinkCredential(p.Identity,
  name, server, rv.Token, started, log)`, with `name` the identity
  provider's registered name (its `p.All` entry) and `started` calling
  `srv.ProviderStarted(ctx, name)`.

### `web/app` and `web/e2e`

- `utils/capabilities.ts`: `Capability.detail?: string`, kept by
  `asCapabilities` when a string of one line and at most 80 characters
  counted in code points (`[...s].length`, as Go counts runes), dropped
  otherwise; `identityLine(caps)`: `"<name>: <detail>"` for a ready
  identity provider with a detail, `""` otherwise.
- `composables/useCapabilities.ts`: an `identity` computed from it. The
  detail changes while every provider stays `ready` (a sign-in that
  completes after Start, a sign-out), so `CapabilitiesPoll` reads the
  route once more for each reader that attaches after the last reply (the
  About card mounted again), and while a reader shows and a ready identity
  provider has no detail it re-reads on the starting provider's schedule
  (2 s doubling to 60 s), no more once a detail is there.
- `components/AboutCard.vue`: a row `identity` labelled "Identity",
  after Extensions, when `identityLine` is not empty.
- `web/e2e/about-identity.spec.ts` (new), below.

### Docs

`docs/protocol.md` (the rows above, and the capabilities route's row:
`detail`, the identity provider's, asked through the guard, never in the
guide's rows). `docs/features.md`: the row's entry, what a build with a
registered identity provider shows and that a build with none shows
nothing new.

## Events, orderings and invariants

**What a registration presents** (`UplinkCredential.Present`, both
modes, at every dial: `Publish`, `OpenDesk`, `Run`, and every
reconnect).

| `OwnsHostToken(configured)` | `UplinkToken(server, configured)` | Presented |
| --- | --- | --- |
| no provider, or none with the methods | not asked | the configured token (today) |
| false | ok, a valid token | that token |
| false | ok false, a refused answer, late, panic, busy, not started | the configured token (today) |
| true, late, panic or busy | ok, a valid token other than the configured | that token |
| true, late, panic or busy | ok false, a refused answer, the configured token echoed, late, panic, busy, not started | nothing: withheld |

`OwnsHostToken` is asked without waiting for `Start` (it checks a form
and needs no service); `UplinkToken` is asked only once the provider has
started. The first ask of a process waits for the provider's first try
of `Start` to return or be given up, at most `ext.StartWait` (at once
for a provider that needs no Start), so a registration made as the
binary starts sees a provider that started at once; one not started then
presents nothing for an owned token. `Present` is bounded by its own
deadline, `StartWait` plus two `IdentityWait`s, in host mode as on the
server, whatever context its caller holds. The server's sessions run
locally meanwhile, and `Publish` already runs off the session's path
(`publish.go`). The empty token on a switchyard admits only an open host
(`ws_host.go`: `open := tok == "" && …`), so a withheld credential never
becomes an admission of its own, and a switchyard that admits no open
hosts answers 401:

- On the server, the publication is owed (`publish.go`), its activity
  says why, and the next Share publishes the same running session again,
  asking `Present` anew.
- In host mode, `Run`'s first registration is tried again with backoff
  for at most `firstCredentialWait` (2 minutes), each try asking
  `Present` anew, so a provider that redeems its code after `Start`
  returned is waited for; the configured code is presented in no try.
  Past the bound, `conductor host` exits with the refusal, as today.

**Renewal, host side.** Every 30 s each live control connection asks
`Renewed`. On a connection whose `registered` said `renews`, an answer
other than the one the connection presented is sent as `host_token` and
becomes the presented one; ok false or a failure sends nothing (ending a
credential is the switchyard's recheck's to do). On a connection that
presented nothing (an open host), an answer closes it and the reconnect
presents the provider's token. On any other connection (a configured
token's; any at a switchyard that did not say `renews`, which rechecks
no host connection) nothing is sent: the next reconnect presents what
`Present` answers then. A desk's connection is a connection like the
others.

**Renewal, switchyard side** (`hostToken`, inline in the connection's
read loop, one at a time, so it is ordered with the host's other
messages; every failure closes `4403 access revoked`, as the issue says,
and the host's reconnect presents its new token):

1. Past `proto.MaxHostTokenMessage`, an unknown field, a token empty,
   past 2 KiB or not token68 (`ParseHostToken`): closed.
2. A connection with no `watchedToken` (not admitted through a
   provider's principal; it was never told `renews`): closed.
3. Less than `proto.HostTokenEvery` (15 s, the server's clock seam)
   after this connection's last `host_token`: closed, nothing asked. The
   host sends one at most every 30 s.
4. A token the server's own chain knows (`chainPrincipal`: the workbench
   token, a host token, a link, expired or revoked included): closed,
   and the provider is never asked about it, as C2 keeps every such
   token from it.
5. With the connection's credential turn held (waiting at most for one
   recheck's ask, `IdentityWait`), the provider is asked through the
   guard in `kindIdentityRenew`'s slots, within `IdentityWait`. Admitted,
   the same subject, a principal with the host grant: the new token
   replaces the old, and the turn is given back; the next recheck asks
   about it. Anything else, an error, a late or busy answer among it:
   closed (unlike the recheck, where a failure says nothing). So a
   `host_token` holds its connection's read loop at most two
   `IdentityWait`s.

**Recheck** (`principalWatch`, host grant). Every 30 s
(`identityRecheckEvery`, `extAfter` the seam) it takes the connection's
credential turn (waiting at most for one renewal's ask), asks the
provider about the token current then within `IdentityWait`, applies its
verdict and gives the turn back. No principal, another subject or no host
grant closes the connection `4403 access revoked`; an error, a late
answer, a panic or a busy guard says nothing and the connection stays.
A renewal and a recheck of one connection never overlap, so a verdict
always concerns the token it asked about, and a renewal still being
asked about when the old token lapses is answered before the recheck
asks about the new one.

**Two timers, one deadline.** The host's 30 s ask and the switchyard's
30 s recheck run on their own timers against one deadline, the old
credential's end. The provider answers a replacement at least
`ext.UplinkTokenLead` (2 minutes) before that end, so the host has sent
it, and the switchyard has taken it, before any recheck can find the old
one lapsed. A provider that answers later than that may see the
connection closed by a recheck and reconnect with the new token: one
drop, fail-closed, never an admission it should not have.

**A revoked host's session.** On `4403 access revoked` the host
reconnects and asks `Present` again. With an owned token and the
provider saying ok false, nothing is presented: where the switchyard
admits open hosts, `Hub.Register` resumes the same session by its secret
(`internal/signal/hub.go`), with its id, its links and its viewers, now
as an open host under the open-host limits (the address's sessions, its
relay bucket, its links), with nothing a provider granted; where it
admits none, the registration is refused 401 and the host keeps trying
with `controlLoop`'s backoff, its viewers waiting as for any host away.
That is the free default (article 8): a revoked credential takes away
what the provider granted and nothing a build with no token has.

**Host mode, start and stop.** `runHost`: `extensions()`, then
`StartIdentity` (the first try begins), then `hostagent.Run`, whose
first `dialAndRegister` asks `Present`; when `Run` returns, the
provider's context ends and `stop` waits for its try. Only the identity
provider is started in host mode; every other registered provider is
left alone, as today. Several `conductor host` processes may start the
provider at once over the one `<state>/ext/<name>`; `DataDir`'s comment
says so.

**Invariants.**

- A configured token the provider owns, or one whose ownership could not
  be answered, never leaves the machine: in no header, in no
  `host_token`, in no log, also when the provider answers it back.
- No token the server's own chain knows reaches a provider through a
  renewal.
- Only the identity slot's provider is handed the host token.
- A connection never changes subject: a renewal is taken only for the
  same subject with the host grant.
- What closes a host connection for its principal or its renewal is said
  once, `4403 access revoked`; the host's reconnect asks the provider
  again.
- No token is logged on either side; the guards log kinds and counts,
  and an identity provider's failed Start is logged without its text.
- Nothing is stored: no schema, no migration.

## Out of scope

- A renewal in place at a switchyard that does not say `renews` (an
  older one): it rechecks no host connection, so none is needed; the
  next reconnect presents the current credential.
- Renewing a connection admitted with the workbench token, a host token
  or as an open host in place: an open host's connection reconnects once
  its provider gives a token; the others keep what admitted them.
- Binding a hosted session to the kind of admission that registered it:
  a revoked host's session resumes as an open host where open hosts are
  admitted (Events), as the free default.
- Republishing a server session refused for a withheld credential
  without a Share: it is owed, and the next Share publishes it.
- The contract suite (`ext/exttest`) checking `OwnsHostToken`,
  `UplinkToken` or `Detail`; only its words change.
- Starting any provider but the identity provider in host mode.
- What a provider does with the host token, the mode or its directory,
  how long its credentials last and how it renews them with its service:
  the module that registers a provider decides; #776's grant, in its own
  plan.

## Tests per surface

**Go race** (`go test -race -count=1 -timeout 60m ./...`; fake providers
and the code's seams, never the clock):

- `internal/hostagent` (`uplinkcred_test.go`, against a real `api`
  switchyard as `switchyard_test.go` builds it, `api.New` with
  `api.WithExtensions` and a fake identity provider; the host's waits
  through `Options.after`): `TestOwnedTokenNeverPresented`, subtests for
  the provider answering ok false, a refused answer (the issue's "error":
  the signature returns none), the configured token echoed, late (held
  past `IdentityWait` by a gate), panicking, not started, and a Start
  that never returns (its give-up fired through `StartIdentity`'s
  `after`, `Present` answering within its own deadline), each with
  `OwnsHostToken` true and with it late and panicking: the switchyard's
  handler records every `Authorization` header, which never carries the
  configured token, and the session registers open (or is refused where
  open hosts are off); and "credential after the first ask": the
  provider answers ok false first and a token after, at a switchyard
  with no open hosts, and `Run`'s retry (fired through `after`) presents
  the token, never the code. `TestHostTokenRenewalSameSubjectOnly`, the
  host's half (the provider answers a new token at a tick fired through
  `after`): of the same subject, the switchyard's provider is asked about
  it once and a `link` request sent after it is answered on the same
  connection (the read loop takes them in order, so the renewal was
  taken), and the next tick with the same answer sends nothing (a second
  `link` round trip, then the provider's asks counted); of another
  subject, the connection is closed 4403 and the reconnect's header
  carries the new token; on an open host's connection, an answer closes
  it and the reconnect presents the token; against a stub switchyard
  whose `registered` has no `renews`, nothing is sent, for a session and
  for a desk (`OpenDesk`). `TestHostTokenMessageBound` (a token at 2,048
  token68 bytes is sent and its message is within 2,560; one past 2 KiB
  or with a character outside token68 is never sent and never
  presented). Also: no provider, and one that does not own the token,
  present the configured token as today; a hosted session's file read
  and save under `config.HostStateDir()` are refused.
- `internal/cli`: `TestHostStartsTheIdentityProvider` (its `Host`'s
  `Mode()` `host`, `HostToken()` the `--token`, `DataDir()` under
  `XDG_STATE_HOME/conductor/ext/<name>` set to a temporary directory,
  its context ended once the host has; two host-mode starts over one
  state directory both start, with one `DataDir`, neither refused; a
  Start that errs and one that panics with the configured token in their
  words, which the host's log never holds),
  `TestHostRegistersWithTheUplinkToken` (a test server sees the
  provider's token in the registration's header, not the configured).
- `internal/config`: `TestHostStateDirDenied` (`FileDeny` and
  `LocalFileDeny` name `HostStateDir()`, with `XDG_STATE_HOME` set and
  unset, and its target when a link leads to it).
- `internal/api`: `TestHostConnectionRechecked` (a provider-admitted
  host connection, a session's and a desk's, says `renews` and is closed
  4403 `access revoked` once revoked; kept on an error, late or panicking
  answer; a workbench-token, host-token and open-host connection says no
  `renews` and is never asked; after a revocation, the host's reconnect
  with nothing resumes the session as an open host where open hosts are
  admitted, same id, and is refused 401 where not).
  `TestHostTokenRenewal` (the switchyard's half, rechecks fired through
  `extAfter`, the provider's asks held by gates in the fake): same
  subject swapped, and the next recheck asks about the new token; on a
  desk's connection too; another subject, no host grant, an error,
  late, busy, past the bound, a second `host_token` within 15 s of the
  first (the clock seam `extClock`; one 15 s later is taken), on a
  connection not admitted by a provider: closed; a token of the server's
  own chain (the workbench token, a host token, a live, an expired and a
  revoked link): closed, the fake never asked about it; both orders of
  the two timers: a recheck first while the old token is still admitted,
  then the renewal, then a recheck after the old one is revoked in the
  fake (the connection stays), and a renewal whose ask is held by a gate
  while a recheck fires, the test waiting for the recheck to wait for the
  connection's turn (a hook, `hostCredSeam.waiting`) before it opens the
  gate, the old token's refusal never asked and the connection kept).
  `TestCapabilitiesDetail` (the detail on the identity row; none while
  starting, for an invalid, late or panicking answer; a detail of 80
  runes kept, 81 refused; reading the guide asks the fake nothing and
  its rows hold no detail; a plain build's reply unchanged; the reply's
  bound holds). `TestProviderToldItsMode` (`server` and `switchyard`,
  with `HostToken` the rendezvous token on the identity provider's Host
  when publishing, `""` on a second provider's Host and on a switchyard;
  `ProviderStarted` true at once for a provider without Start, and for a
  Start that never returns false once its try is given up, through
  `extAfter`). `TestIdentityStartNotQuoted` (a Start of the identity
  slot's provider that errs and one that panics with the host token in
  their words: the log holds neither). `TestPublishOwedWithheld` (a
  publisher refusing with a withheld credential: owed, the Share
  dialog's `republish` true, and a Share once the fake has a credential
  publishes the same running session).
- `internal/proto`: `TestHostTokenMessage` (the bounds, the strict
  decode, a token at its bound within the message's, `registered`'s
  `renews` omitted when false), with the vitest row below the protocol
  triple's test.
- `ext`: `TestValidHostToken` in `records_test.go`; `ext/exttest`:
  `FakeHost`'s `HostToken` and `Mode` in `fakes_test.go`, and the
  suite's changed words in `suite_test.go`.

**Vitest** (`npm --prefix web test`): `utils/capabilities.test.ts`
(`asCapabilities` keeps a detail, keeps one of 80 astral characters,
drops one of 81 code points or of two lines; `identityLine` for a ready
identity provider, empty while starting or with none: the About card's
detail line); `composables/useCapabilities.test.ts` (a reader attached
again reads the route again and shows a detail that appeared, changed or
went; a ready identity provider with no detail is re-read on the
schedule until one comes, then no more); `utils/protocol.test.ts` (the
`host_token` row: its constants and a message at the bound;
`HostRegisteredRenews`).

**Playwright** (`make test-e2e`): `about-identity.spec.ts` routes `GET
/api/capabilities` to an identity provider with the detail "signed in as
Ana · workspace Acme" and sees the About card's Identity row show it; the
plain reply shows no Identity row.

**Other checks** before the push, per `docs/checks.md`: the sign-off
check, `make lint`, `make lint-static`, `make vuln`, `npm --prefix web
run typecheck`, `make web-build` then `make build-go` and `make
check-version`, `npm --prefix web run typecheck:e2e`. Not affected and
named as not run: the scripts suite, `make desktop-test`, `make
test-pebble`, `make test-live`.

**By hand.** No provider is registered in this repository, so nothing
here can be seen working with a real one; the fake providers above stand
in. A by-hand check with a real provider belongs to the module that
registers one.

## Boxcar constraints

1. Attempts start only through `crew.Launcher`: not touched; the host's
   `Host.Launch` answers `ErrUnsupported`.
2. `session.Local` drives a `session.Process`: not touched.
3. An attempt's context is files: not touched.
4. The agent reaches the server over the network: not touched; the host
   connection stays an address-based WebSocket.
5. Checks through `internal/proc`: not touched.
6. Platform splits stay platform tags: none added; the capabilities row
   says what is registered, the UI shows a capability, never a plan.
7. Evidence rows' `where`: not touched.
8. `hostagent` is a launch site as much as `api`: this row wires the
   identity seam into both composition roots (`serve` and `host`), and
   leaves the Runtime native in host mode as `docs/plans/runtime.md`
   decided.

## Open-core checklist

- [x] Article 1: no premium code, no licence check; the only branch is
  whether an identity provider is registered and has the optional
  methods.
- [x] Article 2: a plain build behaves as `main` does (its tests pass
  unchanged; the capabilities reply is `{"providers": []}`).
- [x] Article 3: no paid feature is gated here, and nothing a fork could
  flip turns one on.
- [x] Article 4: revocation reaches host connections; stop, inspect,
  export and approvals are untouched.

## The PRs

| PR | Title | Repository | Contains | Touches | Tests | Depends on | Parallel with | Size |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| PR-1 (#775) | ext, api, hostagent, cli: the host token, the uplink's credential failing closed, host connections rechecked and renewed (`ext.Host` gains `HostToken` and `Mode`) | rocksolidlabs/conductor | the six parts above, the `host_token` triple and `registered`'s `renews`, the host state directory denied, the About card's Identity row, the features entry | `ext`, `ext/exttest`, `internal/api`, `internal/hostagent`, `internal/cli`, `internal/config`, `internal/proto`, `web/app`, `web/e2e`, `docs` | the Go race tests, vitest and Playwright named above | C2 on `main` (merged) | none in this repository; #776 follows it | ~700 + 900 (code + tests) |

Adding two methods to `ext.Host` breaks an implementation of `Host`
outside this module (a provider's own test fake), so the PR's title
names it, as `ext`'s Stability section asks. The PR carries the
`docs/features.md` entry.

## Owner decisions

Dated 2026-11-10. The issue's words leave these open; each takes the
fail-closed answer as a default the owner can overturn.

- **IU1. The message's key.** Every host message carries its type in
  `t`, so the issue's `{type: "host_token", token}` is
  `{"t":"host_token","token":"…"}`.
- **IU2. The signatures stay the issue's.** `UplinkToken` and `Detail`
  return no error; the "error" the issue's test names is an answer the
  guard refuses (ok with a token `ValidHostToken` refuses, or the owned
  configured token echoed; a detail of two lines or past 80 runes).
- **IU3. The token's characters.** 1 to 2,048 bytes of RFC 6750's
  token68 characters, which a header carries as they are and JSON never
  escapes, so a token within its bound always fits the 2.5 KiB message.
- **IU4. Ownership is asked every time.** `OwnsHostToken` is asked at
  every registration, without waiting for `Start`, never cached; a late,
  panicking or busy answer counts as owned.
- **IU5. The first answer waits for the first try.** The first ask of a
  process waits for the provider's first try of `Start` to return or be
  given up (at most `ext.StartWait`, at once with no Start); not started
  then, an owned token presents nothing. `Present` has its own deadline,
  `StartWait` plus two `IdentityWait`s.
- **IU6. One guarded credential for both modes.** `hostagent`'s
  `UplinkCredential` asks the provider for the server's uplink and for
  `conductor host` alike, by the server guard's rules, with one call in
  flight per method.
- **IU7. A message, only where it is taken.** Renewal in place
  (`host_token`) because a reconnect drops every viewer (the section
  above); sent only on a connection whose `registered` says `renews`, at
  most every 30 s; an open host's connection reconnects instead once its
  provider gives a token.
- **IU8. A failing renewal closes.** The switchyard takes `host_token`
  inline, within `IdentityWait`, in slots of its own; an error, a late
  or busy answer, a token of the server's own chain, or one within 15 s
  of the last closes the connection `access revoked`, as anything but a
  same-subject admission does.
- **IU9. One credential turn per connection.** A renewal and a recheck
  of one connection take turns, each holding it from its ask to its
  verdict, so a verdict always concerns the token current when it asked.
- **IU10. The host's state directory.** `$XDG_STATE_HOME/conductor`, or
  `~/.local/state/conductor` (where older hosts kept their hooks), so
  the provider's directory is `<state>/ext/<name>`, 0700, shared without
  a lock by the user's `conductor host` processes on the machine, apart
  from any server's `<dataDir>/ext/<name>`, outside every data directory
  an export reads, and denied to every session's files (`internal/config`).
- **IU11. The server's host token.** `cfg.Rendezvous.Token` only while
  the server publishes to a switchyard, and only on the identity slot's
  provider's Host; `""` otherwise and on a switchyard.
- **IU12. Where the detail shows.** In `GET /api/capabilities` (admin)
  and the About card's Identity row; never in the guide's providers
  rows, which a session's agent reads, and the guide asks no provider;
  asked through the guard at each request of the route, only once the
  provider has started; read again by the app at each opening of the
  card, and while a ready provider shows none.
- **IU13. The renewal's lead.** A replacement credential is answered at
  least `ext.UplinkTokenLead` (2 minutes) before the one it replaces
  lapses; a later one may cost one reconnect.
- **IU14. A revoked host's session.** It resumes as an open host where
  open hosts are admitted (same id, links and viewers, the open-host
  limits) and is refused where not; it is not bound to the kind of
  admission that registered it.
- **IU15. A withheld first registration.** On the server the
  publication is owed and the next Share publishes it; `conductor host`
  tries again for at most 2 minutes, then exits as today.

## Acceptance

The acceptance boundary is observed when the PR's checks pass in CI (the
Go race tests per package, vitest and Playwright named above) and its
`docs/features.md` entry records what a build with a registered identity
provider shows: the configured token handed to the provider, the
uplink's choice and its fail-closed table, host connections rechecked
and renewed, and the About card's Identity row. "Nothing changes for a
build with none" is observed by the plain build's existing tests passing
unchanged, the capabilities reply staying `{"providers": []}` and
`TestOwnedTokenNeverPresented`'s no-provider case presenting the
configured token. Done is the PR merged with those. Nothing waits on the
owner's setup; a maintainer merges.
