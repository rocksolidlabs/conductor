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
`FakeHost`), `internal/api` (`auth.go`, `identity.go`, `extensions.go`'s
`guardIdentity`, `exthost.go`, `capabilities.go`, `guide.go`,
`ws_host.go`, `publish.go`), `internal/hostagent` (`agent.go`'s
`dialAndRegister`, `controlLoop` and `serveConn`, `uplink.go`),
`internal/cli` (`host.go`, `serve.go`), `internal/signal` (`hosted.go`),
`internal/proto/hostmsg.go`, `docs/protocol.md` (Host control
connection, HTTP API), `web/app/utils/protocol.ts`,
`web/app/utils/capabilities.ts` and `web/app/components/AboutCard.vue`.
It is reviewed before its code; its one PR is the issue's.

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
   credential never turns into an open host.
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
    the `host_token` message touches `internal/proto`,
    `web/app/utils/protocol.ts` and `docs/protocol.md` together, with its
    bound and tests.
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
- When the configured token is the provider's own (a one-time code), the
  switchyard never sees it. The uplink presents what the provider gives
  (a credential it holds), and when the provider gives nothing in time,
  presents nothing: the session is an open host, as a build with no
  token is, or is refused where the switchyard admits no open hosts.
- A host connection a provider admitted is asked about again every 30 s
  and closed `access revoked` once the provider no longer admits it.
- The provider's credential is renewed in place over the host
  connection (`host_token`), so hosted sessions do not drop and
  re-register when an hour-long pass is replaced.
- Settings → The app shows who the server is signed in as, beside the
  provider: "signed in as Ana · workspace Acme".

A build with no provider registered shows and does nothing new.

## The changes

All in this repository (`rocksolidlabs/conductor`).

### `ext`

`ext/host.go`. `Host` gains two methods, after `Log`:

```go
// HostToken is the configured host token of the binary that runs the
// provider: Settings' Host token (rendezvous.token) on a server that
// publishes to a switchyard, conductor host's --token
// (CONDUCTOR_HOST_TOKEN); "" for none, and on a switchyard. Only the
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
comment names the host's directory (below).

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
	// as the uplink dials it) for configured: ok false for nothing.
	UplinkToken(server, configured string) (token string, ok bool)
}

// IdentityDetail is an identity provider that says who the server is
// signed in as: one line of at most MaxIdentityDetail characters
// ("signed in as Ana · workspace Acme"), "" for nothing.
type IdentityDetail interface{ Detail() string }

const (
	MaxHostToken      = 2048 // bytes, proto.MaxHostToken
	MaxIdentityDetail = 80   // characters
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
// with (HostTokenMsg): {"t":"host_token","token":"…"}.
HostTokenType = "host_token"

type HostTokenMsg struct {
	T     string `json:"t"`
	Token string `json:"token"`
}

const (
	MaxHostToken        = 2048 // ext.MaxHostToken
	MaxHostTokenMessage = 2560 // the message, encoded
)

// ParseHostToken decodes a host_token, refusing a message past
// MaxHostTokenMessage before it is decoded, an unknown field, and a
// token that is empty, past MaxHostToken or not token68.
func ParseHostToken(data []byte) (string, error)
```

A token within its bound always fits the message's: token68 needs no
JSON escape. `web/app/utils/protocol.ts`: `HostTokenMsg { t:
'host_token'; token: string }`, `HOST_MAX_TOKEN = 2048`,
`HOST_MAX_TOKEN_MESSAGE = 2560`. `docs/protocol.md`, Host control
connection: the `host_token` row (who sends it, when, its bounds, what
the server does with it), and the recheck of a provider-admitted host
connection (closed `4403 access revoked`).

### `internal/api`

- `identity.go`. `hostAllowed` becomes `hostPrincipal(r, tok)
  (principal, bool)`, the same check returning the principal, so the host
  connection knows its subject. `principalWatch` is generalised: it takes
  the connection's credential (a `watchedToken`: the token and a
  generation, read under its own lock) and the grant the connection
  needs (`admin` for a viewer's WebSocket and the events stream, as
  today; `host` for a host connection), and applies a verdict only to the
  generation it asked about.
- `ws_host.go`. A host connection admitted through a provider's
  principal (`subject != ""`) keeps a `watchedToken` in its
  `hostConnState` and runs the watch: when the provider no longer admits
  it, the connection is closed `proto.CloseForbidden` (4403) `access
  revoked`. A connection of the workbench token, a host token or an open
  host has none and is never asked about. `handleHostMessage` gains
  `case proto.HostTokenType`: `hostToken(hc, data)` (below).
- `extensions.go`. `guardIdentity` gains `detail(ctx) (string, bool)`
  through `extCall` (kind `identity`, `ext.IdentityWait`; refused unless
  one line, 1 to `ext.MaxIdentityDetail` characters, no control
  character), asked only once the provider has started and only of one
  that is an `ext.IdentityDetail`.
- `capabilities.go`. `capabilityRow` gains `Detail string
  json:"detail,omitempty"`, set on the identity provider's row from
  `detail`; the route's comment says the identity provider is asked that
  one thing, through the guard. `capabilitiesReplyMax` (8 KiB) still
  holds (one detail of 80 characters is at most 320 bytes).
- `guide.go`. `guideProvidersOf` drops `Detail`: the guide's providers
  rows, which a session's agent reads, never say who the server is
  signed in as.
- `exthost.go`. `extHost` implements `HostToken` (`cfg.Rendezvous.Token`
  when `cfg.RendezvousServer()` is set, `""` otherwise and on a
  switchyard) and `Mode` (`ModeSwitchyard` when `cfg.Switchyard.Enabled`,
  else `ModeServer`). `extStarter` gains `tried`, closed once its first
  try of `Start` has returned, and the server gains `ProviderStarted(ctx,
  name) bool`: it waits for that (or ctx) and answers whether a try has
  succeeded.

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
  `Present(ctx) string`, what a registration presents (the table under
  Events), and `Renewed(ctx) (string, bool)`, UplinkToken's token when it
  answers one in time and `ext.ValidHostToken` takes it.
- `agent.go`. `Options` gains `Credential *UplinkCredential`.
  `dialAndRegister` asks `Present` before it dials, in place of reading
  `opts.Token`, and keeps what it presented (`a.presented`, under
  `a.mu`); no header when it is `""`. `serveConn` begins `renewToken`,
  which every 30 s (`uplinkTokenEvery`, behind a test seam,
  `opts.tokenAfter`) asks `Renewed` and, when it answers a token other
  than `a.presented`, sends `host_token` in the connection's own lane and
  makes it `a.presented`. A run's desk's connection does the same.
- `uplink.go`. `Uplink` gains `Credential` and passes it into every
  publication's and desk's `Options`.
- `exthost.go` (new). The host mode's provider: `StartIdentity(ctx, p,
  name, HostIdentity{Token, StateDir, Log}) (started func(ctx) bool, stop
  func())` supervises `Start` by the server's rules (`ext.StartWait`;
  backoff 1 s doubling to 5 minutes; never two tries at once; a given-up
  try's `Host` refusing every call `context.Canceled`), with an
  `ext.Host` whose `HostToken` is `--token`, `Mode` `ModeHost`, `DataDir`
  `<state>/ext/<name>` made 0700, `Log` the host's logger with
  `provider=<name>`, and `Launch`, `LaunchTurn`, `LaunchApproved`,
  `Submit`, `Notify` and `Decision` answering `ext.ErrUnsupported` (a host
  launches nothing and keeps no ledger). `stop` cancels the try and waits
  for it at most `ext.StartWait`.

### `internal/cli`

- `host.go`. `runHost` keeps `extensions()`, and when it holds an
  identity provider that is an `ext.Starter` starts it (`StartIdentity`)
  before `runHostAgent`, with the state directory
  `$XDG_STATE_HOME/conductor` (or `~/.local/state/conductor` when that is
  unset or not absolute), and stops it once the host has ended (deferred).
  `Options.Credential` is `NewUplinkCredential(p.Identity, name,
  *server, *token, started, log)`, `name` the provider's registered name. The comment that says nothing of the
  registry is used is replaced; the Runtime stays native.
- `serve.go`. `newUplink` sets `Credential: NewUplinkCredential(p.Identity,
  name, server, rv.Token, started, log)`, with `name` the identity
  provider's registered name (its `p.All` entry) and `started` calling
  `srv.ProviderStarted(ctx, name)`.

### `web/app` and `web/e2e`

- `utils/capabilities.ts`: `Capability.detail?: string`, kept by
  `asCapabilities` when a string of one line and at most 80 characters,
  dropped otherwise; `identityLine(caps)`: `"<name>: <detail>"` for a
  ready identity provider with a detail, `""` otherwise.
- `composables/useCapabilities.ts`: an `identity` computed from it.
- `components/AboutCard.vue`: a row `identity` labelled "Identity",
  after Extensions, when `identityLine` is not empty.
- `web/e2e/about-identity.spec.ts` (new), below.

### Docs

`docs/protocol.md` (the two rows above, and the capabilities route's
row: `detail`, the identity provider's, asked through the guard, never in
the guide's rows). `docs/features.md`: the row's entry, what a build
with a registered identity provider shows and that a build with none
shows nothing new.

## Events, orderings and invariants

**What a registration presents** (`UplinkCredential.Present`, both
modes, at every dial: `Publish`, `Run`, and every reconnect).

| `OwnsHostToken(configured)` | `UplinkToken(server, configured)` | Presented |
| --- | --- | --- |
| no provider, or none with the methods | not asked | the configured token (today) |
| false | ok, a valid token | that token |
| false | ok false, a refused answer, late, panic, busy, not started | the configured token (today) |
| true, late, panic or busy | ok, a valid token | that token |
| true, late, panic or busy | ok false, a refused answer, late, panic, busy, not started | nothing: an open host |

`OwnsHostToken` is asked without waiting for `Start` (it checks a form
and needs no service); `UplinkToken` is asked only once the provider has
started. The first ask of a process waits for the provider's first try
of `Start` to return (at most `ext.StartWait`), so a registration made
as the binary starts sees a provider that started at once; one not
started then presents no credential for an owned token. The publication
waits for this answer (at most `StartWait` plus `IdentityWait`); the
server's sessions run locally meanwhile, and `Publish` already runs off
the session's path (`publish.go`). The empty token on a switchyard
admits only an open host (`ws_host.go`: `open := tok == "" && …`), so a
refused credential never becomes one, and a switchyard that admits no
open hosts answers 401, which the uplink treats as any refused
registration.

**Renewal, host side.** Every 30 s each live control connection asks
`Renewed`. An answer ok with a token other than the one the connection
presented is sent as `host_token` and becomes the presented one; ok
false or a failure sends nothing (ending a credential is the
switchyard's recheck's to do). An open host's or a configured token's
connection whose provider now gives a token sends it too: the switchyard
closes it (not a renewal, below), and the host's ordinary reconnect
presents the provider's token. An older switchyard ignores the message
(`handleHostMessage`'s default); the host makes the token presented all
the same and never resends it.

**Renewal, switchyard side** (`hostToken`, inline in the connection's
read loop, one at a time, so it is ordered with the host's other
messages):

1. Past `proto.MaxHostTokenMessage`, an unknown field, a token empty,
   past 2 KiB or not token68 (`ParseHostToken`): close `4403 access
   revoked`.
2. A connection with no `watchedToken` (not admitted through a
   provider's principal): close `access revoked`.
3. Ask the provider through the guard (the recheck's kind, so its slots
   are not a request's), within `IdentityWait`. Admitted, the same
   subject, a principal with the host grant: the new token replaces the
   old under the `watchedToken`'s lock, its generation advanced; the next
   recheck asks about it. Anything else, an error, a late or busy answer
   among it: close `access revoked` (unlike the recheck, where a failure
   says nothing).

**Recheck** (`principalWatch`, host grant). Every 30 s
(`identityRecheckEvery`, `extAfter` the seam) it reads the token and its
generation, asks the provider within `IdentityWait`, and acts only if
the generation is unchanged when the answer comes: a verdict about a
token a renewal replaced is dropped. No principal, another subject or no
host grant closes the connection `4403 access revoked`; an error, a late
answer, a panic or a busy guard says nothing and the connection stays.

**Host mode, start and stop.** `runHost`: `extensions()`, then
`StartIdentity` (the first try begins), then `hostagent.Run`, whose
first `dialAndRegister` asks `Present`; when `Run` returns, the
provider's context ends and `stop` waits for its try. Only the identity
provider is started in host mode; every other registered provider is
left alone, as today.

**Invariants.**

- A configured token the provider owns, or one whose ownership could not
  be answered, never leaves the machine: in no header, in no
  `host_token`, in no log.
- A connection never changes subject: a renewal is taken only for the
  same subject with the host grant.
- What closes a host connection for its principal is said once, `4403
  access revoked`; the host's reconnect asks the provider again.
- No token is logged on either side; the guards log kinds and counts.
- Nothing is stored: no schema, no migration.

## Out of scope

- Moving a session refused at publish (an owned token with no
  credential, at a switchyard with no open hosts) when the provider later
  signs in: it is published again when the person next shares it, as any
  refused publication is.
- The contract suite (`ext/exttest`) checking `OwnsHostToken`,
  `UplinkToken` or `Detail`; only its words change.
- Starting any provider but the identity provider in host mode.
- What a provider does with the host token, the mode or its directory;
  #776's grant, in its own plan.

## Tests per surface

**Go race** (`go test -race -count=1 -timeout 60m ./...`; fake providers
and the code's seams, never the clock):

- `internal/hostagent` (`uplinkcred_test.go`, against a real `api`
  switchyard as `switchyard_test.go` builds it):
  `TestOwnedTokenNeverPresented`, subtests for the provider answering ok
  false, a refused answer (the issue's "error": the signature returns
  none), late (held past `IdentityWait` by a gate), panicking and not
  started, each with `OwnsHostToken` true and with it late and
  panicking: the switchyard's handler records every `Authorization`
  header, which never carries the configured token, and the session
  registers open (or is refused where open hosts are off);
  `TestHostTokenRenewalSameSubjectOnly` (the provider answers a new token
  through `tokenAfter`: one of the same subject keeps the connection and
  the next recheck asks the new one; another subject's closes it `access
  revoked` and the reconnect presents the new token);
  `TestHostTokenMessageBound` (a token at 2,048 token68 bytes is sent and
  its message is within 2,560; one past 2 KiB or with a character outside
  token68 is never sent and never presented). Also: no provider, and one
  that does not own the token, present the configured token as today.
- `internal/cli`: `TestHostStartsTheIdentityProvider` (its `Host`'s
  `Mode()` `host`, `HostToken()` the `--token`, `DataDir()` under
  `XDG_STATE_HOME/conductor/ext/<name>` set to a temporary directory,
  its context ended once the host has),
  `TestHostRegistersWithTheUplinkToken` (a test server sees the
  provider's token in the registration's header, not the configured).
- `internal/api`: `TestHostConnectionRechecked` (a provider-admitted
  host connection closed 4403 `access revoked` once revoked; kept on an
  error, late or panicking answer; a workbench-token, host-token and
  open-host connection never asked), `TestHostTokenRenewal` (same subject
  swapped; another subject, no host grant, an error, late, past the
  bound, on a connection not admitted by a provider: closed; a recheck in
  flight across a swap is dropped, held by a gate in the fake),
  `TestCapabilitiesDetail` (the detail on the identity row; none while
  starting, for an invalid, late or panicking answer, nor in the guide's
  rows; a plain build's reply unchanged; the reply's bound holds),
  `TestProviderToldItsMode` (`server` and `switchyard`, with `HostToken`
  the rendezvous token when publishing and `""` on a switchyard).
- `internal/proto`: `TestHostTokenMessage` (the bounds, the strict
  decode, a token at its bound within the message's), with the vitest row
  below the protocol triple's test.
- `ext`: `TestValidHostToken` in `records_test.go`; `ext/exttest`:
  `FakeHost`'s `HostToken` and `Mode` in `fakes_test.go`, and the
  suite's changed words in `suite_test.go`.

**Vitest** (`npm --prefix web test`): `utils/capabilities.test.ts`
(`asCapabilities` keeps a detail, drops one past 80 characters or of two
lines; `identityLine` for a ready identity provider, empty while
starting or with none: the About card's detail line);
`utils/protocol.test.ts` (the `host_token` row: its constants and a
message at the bound).

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
| PR-1 (#775) | ext, api, hostagent, cli: the host token, the uplink's credential failing closed, host connections rechecked and renewed (`ext.Host` gains `HostToken` and `Mode`) | rocksolidlabs/conductor | the six parts above, the `host_token` triple, the About card's Identity row, the features entry | `ext`, `ext/exttest`, `internal/api`, `internal/hostagent`, `internal/cli`, `internal/proto`, `web/app`, `web/e2e`, `docs` | the Go race tests, vitest and Playwright named above | C2 on `main` (merged) | none in this repository; #776 follows it | ~650 + 800 (code + tests) |

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
  guard refuses (ok with a token `ValidHostToken` refuses; a detail of
  two lines or past 80 characters).
- **IU3. The token's characters.** 1 to 2,048 bytes of RFC 6750's
  token68 characters, which a header carries as they are and JSON never
  escapes, so a token within its bound always fits the 2.5 KiB message.
- **IU4. Ownership is asked every time.** `OwnsHostToken` is asked at
  every registration, without waiting for `Start`, never cached; a late,
  panicking or busy answer counts as owned.
- **IU5. The first answer waits for the first try.** The first ask of a
  process waits for the provider's first try of `Start` (at most
  `ext.StartWait`); not started then, an owned token presents nothing.
- **IU6. One guarded credential for both modes.** `hostagent`'s
  `UplinkCredential` asks the provider for the server's uplink and for
  `conductor host` alike, by the server guard's rules, with one call in
  flight per method.
- **IU7. What a renewal is sent for.** `host_token` is sent every time
  `UplinkToken` answers a token other than the one the connection
  presented, every 30 s at most; on an open or configured connection that
  closes it, and the reconnect presents the provider's token.
- **IU8. A failing renewal closes.** The switchyard takes `host_token`
  inline, within `IdentityWait`; an error or a late answer closes the
  connection `access revoked`, as anything but a same-subject admission
  does.
- **IU9. A verdict applies to the credential it asked about.** A
  recheck's answer that comes after a renewal replaced the token is
  dropped.
- **IU10. The host's state directory.** `$XDG_STATE_HOME/conductor`, or
  `~/.local/state/conductor` (where older hosts kept their hooks), so
  the provider's directory is `<state>/ext/<name>`, 0700, shared by the
  user's `conductor host` processes on the machine, apart from any
  server's `<dataDir>/ext/<name>` and outside every data directory an
  export reads.
- **IU11. The server's host token.** `cfg.Rendezvous.Token` only while
  the server publishes to a switchyard; `""` otherwise and on a
  switchyard.
- **IU12. Where the detail shows.** In `GET /api/capabilities` (admin)
  and the About card's Identity row; never in the guide's providers
  rows, which a session's agent reads; asked through the guard at each
  request, only once the provider has started.

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
