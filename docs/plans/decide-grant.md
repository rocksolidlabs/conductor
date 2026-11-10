# A decide grant and a pass taken from the fragment (issue #776)

Today an identity provider's principal is the operator (`ext.GrantAdmin`)
or a host (`ext.GrantHost`), nothing between. A person who should answer
one decision and see nothing else has no grant to hold, so a provider
could only make that person the operator. This plan adds the grant
between them: `ext.GrantDecide`, which answers the durable decisions
(DR1, `docs/plans/decisions.md`) whose policy admits it, through the
decision page (MB0, `docs/plans/decision-inbox.md`), and reads nothing
else; a live stream of its own that carries those decisions alone; DR1's
route operation taught to give a decision to the grant; and the web
app's way to take such a principal's credential, a pass, from the URL
fragment without ever sending it in a URL.

It is the row of issue #776, written from the issue's words against
`main` at `175e0be5` (2026-11-10) after reading `ext/identity.go` and
`ext/valid.go`, `internal/api` (`auth.go`, `identity.go`,
`extensions.go`'s `guardIdentity`, `decisions.go`, `events.go`,
`inbox.go`, `inbox_decisions.go`, `brakeman_act_decision.go`),
`internal/crew`'s `decisions.go`, `decisions_wait.go` and
`decisions_for.go`, and `web/app` (`plugins/token-fragment.client.ts`,
`utils/desktop.ts`, `composables/useWorkbenchToken.ts`, `useApi.ts`,
`useAttention.ts`, `pages/decisions.vue`). The issue's words decide the
scope; where they leave a choice open, the fail-closed answer is taken
and listed under Owner decisions. The plan pull request also carries the
amendment the issue asks of `docs/plans/decisions.md` (DR1's route table
and its recheck), quoted whole below.

**Prerequisites.** On `main`: C2 (the identity provider seam,
`docs/plans/ext.md`), DR1 with every row (the records, their routes,
`authorityOf`, `ConfirmAuthority`, `principalWatch`'s use by the events
stream), HM1 (the route to a person, whose rules the route to the grant
copies) and MB0's run rows (the decision page and its read model,
`GET /api/inbox`). Not on `main`: issue #775, the stream's other
open-source row (the host token and the uplink's credential,
`docs/plans/identity-uplink.md`). Both rows change `ext/identity.go`,
`internal/api`'s identity checks, `docs/protocol.md` and
`web/app/utils/protocol.ts`, so this row is built after #775 merges and
on what it adds: `ext.Host.HostToken` and `Mode`, the provider's
`OwnsHostToken`, `UplinkToken` and `Detail`, the host connection's
recheck, the `host_token` message, the seam's words for an ended
admission, and the Playwright server with a fake identity provider that
its `about-identity.spec.ts` needs. That plan owns those pieces; this one
names them and restates none.

The names follow the railroad: Conductor is the application, Yardmaster
the harness that holds the wait and applies the answer, Switchman the
routing whose team layer is a later item. "Workspace" here is only the
words a person reads ("Open it again from your workspace page"): this
repository knows no workspace, and nothing in it says what registers an
identity provider. It only asks whether one is registered.

## Articles it serves

1. **No premium code.** The grant is a word of `ext`; the only check is
   whether an identity provider is registered (`409 no_identity` on the
   route to the grant), the registration fact `GET /api/capabilities`
   already reads.
2. **Free build fully useful.** A plain build registers no provider, so
   no principal holds the grant and no decision is routed to it; every
   route, page and stream answers as today.
3. **Gated by hosted dependencies.** Nothing here is a flag: a decide
   principal exists only when an identity provider registered through
   `ext` admits a token with the grant.
4. **Stop, inspect, revoke, export, approvals free.** The grant answers
   and reads only; stop, inspection, export and every other approval stay
   the operator's. The operator takes a grant back with a route to the
   operator, and a provider's revocation ends the principal's requests at
   once and its stream within the 30 s recheck.
5. **Dependencies one way.** No import changes; `ext` stays standard
   library only.
6. **No feature build tag.** None added.
7. **Free default is today's main.** With no provider every principal is
   one of today's; the decide paths are reached only by a provider's
   principal.
8. **Provider outage is the free default.** A provider that fails, is
   late or is away admits nothing new (the guard's answer, an unknown
   token: `401`), and a recheck it cannot answer ends no stream, as today;
   an answer not rechecked in time is superseded and the operator answers
   again (DR1's rule, unchanged).
9. **CGO off.** No change.
10. **Contract.** This plan before its code, one issue and one PR, race
    tests, vitest and Playwright, the protocol rows together.
11. **Names.** As above; the grant is `decide`, a word the repository
    does not already use for anything else.
12. **One execution seam.** Not touched: nothing here starts a process.

## Why and what happens

Without this row, a decision that should go to one person goes to the
operator, or the person is made the operator. After it, on a build with
an identity provider registered:

1. The operator routes a decision to the decide grant through DR1's
   route operation (`POST /api/decisions/{did}/route`, the guide's
   `decision_route`) with `to: {kind: "grant", grant: "decide"}`. The
   decision's policy now admits any principal holding the grant, its
   `rev` moves, and its history says who routed it.
2. The person opens the server from their workspace page, which sends
   them to `https://<server>/decisions#pass=<pass>`. The app takes the
   pass out of the address bar before anything else runs, keeps it in a
   store of its own, and asks `GET /api/me`, which answers the
   principal's name and grants.
3. A principal with the decide grant and not the admin grant sees the
   decision page alone, headed "Signed in as Ana": no sidebar, no
   sessions, no runs, no Dispatch. The page lists the decisions that
   admit Ana, reads each through DR1's route, answers through DR1's
   response route, and stays current through `GET /api/decisions/events`,
   which carries those decisions and nothing else.
4. Ana's answer is recorded under `user:<subject>` with the authority
   `{grant: decide, via: identity}`, rechecked with the provider after
   its commit, and applied by Yardmaster as any identity's answer is.
5. When the pass ends (the provider's 30 minutes, or a revocation), the
   next request is `401`, or the stream is closed `access revoked`; the
   page says "Your access to this server has ended. Open it again from
   your workspace page." and keeps the answer Ana had not sent yet. A new
   Open brings a new pass, which replaces the old one in its store.

A browser that already holds a workbench token for that origin keeps it:
the pass never replaces it, and a request carries the workbench token
when one is stored.

## The changes

### `ext`

- `identity.go`: `GrantDecide Grant = "decide"`, documented as "answer
  the durable decisions whose policy admits it, and read nothing else".
  `Grant.Valid` accepts it; `Principal.Valid`'s refusal words name the
  three ("is not admin, host or decide"). `IdentityProvider`'s comment,
  as #775 leaves it, gains the decide grant among what a recheck asks:
  a connection is closed when the answer is another subject or a
  principal without the grant that connection needs (admin for a viewer
  and `GET /api/events`; the grant it opened with for the decision
  stream).
- `ext/exttest`: nothing new; its identity check already takes any valid
  grant.

### `internal/crew`

- `decisions_admit.go` (new): the one judgement.
  `PolicyEntry{Kind string; Grant string}` (`json:"kind"`,
  `json:"grant,omitempty"`), the entries of a policy's `authority`;
  `Admittee{Grants []string; Via string; Subject string}`, who asks; and
  `Admits(entries []PolicyEntry, who Admittee) bool`: each entry admits
  by its own kind's rule, and one entry that does admits. `operator` admits
  `via` `workbench` with the admin grant, or `via` `identity` with the
  admin grant; `grant` admits `via` `identity` holding that grant;
  `router` is admitted by no entry; an unknown kind admits nobody. A
  later kind (SW1's subject entry) adds its rule here and nowhere else.
  `PolicyOf(raw string) ([]PolicyEntry, error)` reads a record's policy;
  a policy it cannot read admits nobody.
- `decisions.go`: `RespondDecision`'s check of the authority
  (`auth.Grant != grantAdmin`, today) becomes `!Admits(policy, auth as
  an Admittee)`, refusing `not_approver` with the words "dr-7 is not
  yours to answer: refused under local/1"; `applyTx`'s recheck of the
  responder's authority at application calls `Admits` the same way. The
  policy keeps its revision, `local/1`, so `stale_policy` keeps its
  meaning. `ConfirmAuthority(id, k, admitted)` keeps its signature: the
  route's recheck computes `admitted` with the same function (below).
- `decisions_wait.go`: `Destination` gains `Grant string`
  (`json:"grant,omitempty"`). `RouteDecision` takes `{kind: "grant",
  grant: "decide"}` (any other grant `invalid`): only while the decision
  is open, at its `expectedRev`, through the run that holds it (`not_held`
  otherwise, "continue it, then route it to the grant"), as
  `forRoute`'s person route is taken; in one transaction it adds the
  entry to the policy's `authority`, writes the `routed` row (`{to:
  [{kind: "grant", grant: "decide"}], rule: "local/1: the operator, or a
  principal with the decide grant", by}`) and bumps `rev`. A policy that
  already holds the entry is not changed: the row is written, `rev` is
  not bumped, and the words say "dr-7 already admits the decide grant". A
  route to the operator of an open decision whose `authority` holds a
  grant entry takes the entries out the same way (policy back to
  `policyJSON`, the row, `rev` + 1); to the operator of any other
  decision it changes nothing, as today. The policy stays under
  `ledger.MaxDecisionPolicy` (1 KiB): each entry is held once.
- The fresh decision that replaces an identity's answer not rechecked
  (`replaceDecision`) opens under `policyJSON`, the operator alone
  (Owner decisions, 6).
- `DecisionChange` (what `SQLLedgerTold` tells) gains `PolicyBefore`, the
  policy as the transaction found it, so the stream (below) can tell a
  change that took a principal out from one that never admitted it,
  from the change alone.
- No ledger schema change and no migration: `policy` and `authority` are
  JSON columns already. An older binary reading a policy with a grant
  entry checks only its revision and the admin grant, so it refuses a
  decide answer `not_approver`, and it supersedes an identity's answer
  whose `boot` is another process's: both fail closed.

### `internal/api`

- `identity.go`: `principal` gains `decide bool`; `grantPrincipal` sets
  it for `ext.GrantDecide`. `requireAdmin` is unchanged, so a
  decide-only principal is `401` on every admin route, `GET /api/events`
  among them (its check is `p.admin`). `admits(policy string, p
  principal) bool` (new) is `crew.Admits` over the record's policy and
  the principal as an `Admittee`; the inbox, the routes, the stream and
  the response's recheck call it and nothing else.
  `principalWatch(p, tok, need ext.Grant)` (new parameter; `need` is
  `GrantAdmin` for viewers and `GET /api/events`, as today) ends the
  connection when the recheck answers another subject or a principal
  without `need`. If #775 has already given the watch a grant to check
  for host connections, this row adds `decide` to the same parameter
  instead of a second one.
- `me.go` (new): `GET /api/me`, any authenticated principal: `{subject?,
  name?, grants}` (at most 512 bytes): the workbench token `["admin",
  "host"]`, a host token `["host"]`, a share link `[]`, a provider's
  principal its subject, name and grants; anything else `401` under the
  rate limit. Not refused on a switchyard (the workbench there is
  `["admin", "host"]` too; its lack of a role is `whoami`'s to say).
- `decisions.go`: the routes the grant may take go through `deciding`
  (new), which admits the admin exactly as `launching` does, and a
  decide principal only for `GET /api/decisions/{did}` and `POST
  /api/decisions/{did}/respond` of a decision `admits` lets it in
  (`404 not_found` otherwise, a decision it may not see answered as one
  that does not exist), `401` for every other principal. The response's
  transaction checks again with `Admits` (above), so a route that took
  the grant back between the two is `403 not_approver` with a kept
  receipt. `authorityOf` records the grant: `admin` when held, else
  `decide`, with `via: identity` and the subject. `recheckResponse`'s
  `admitted` becomes: the provider answered in time, the same subject,
  and `admits(the decision's policy as it is now, the principal the
  recheck answered)`. `handleDecisionRoute` refuses `to: grant` with
  `409 no_identity` ("no identity provider is registered: a decision is
  given to the decide grant only where one is") before the engine when
  `s.Registered()` holds no identity provider; the route stays the
  admin's.
- `guide.go`: `decision_route`'s description names the grant
  destination and `no_identity` (its schema is `Destination`'s, which
  gains `grant`).
- `brakeman_act_decision.go`: a Brakeman turn's `route_decision` act
  refuses `to: grant` (`400 invalid`) and a route to the operator of a
  decision whose policy holds a grant entry (`409 stale_target`, "dr-7
  is given to the decide grant: the operator may route it"), as it
  refuses to change whom a decision is for.
- `inbox.go`, `inbox_decisions.go`: `GET /api/inbox` and `GET
  /api/inbox/item` go through `inboxFor` (new; `inboxOnly` for the
  admin, the same switchyard refusal for everyone). For a decide
  principal the read model holds DR1's records alone (no session
  question, failed task, plan revision or Brakeman card), only those
  `admits` lets in, open and answered ones and Recent's closed ones; each
  item's actions are its response actions only (no comment, more
  information, cancel or route); an item of any other ref is `404
  not_found`. `GET /api/inbox/track` stays the admin's (`401`).
  `answererOf` names the grant entry ("the operator, or a person with the
  decide grant").
- `decision_stream.go` (new): `GET /api/decisions/events`, Server-Sent
  Events, the admin's or a decide principal's (`401` otherwise, under the
  rate limit), the token from the `Authorization` header alone
  (`presentedToken(r, false)`: never a query). It subscribes to a
  `decisionHub` of its own (not `eventHub`), then reads the snapshot,
  then writes `event: snapshot` with `[{id, rev}]` for the open and
  answered decisions that admit the principal at that moment, newest
  first, at most 200 and 16 KiB (`maxDecisionSnapshot`,
  `maxDecisionSnapshotBytes`), then drains what the hub queued. The hub
  is told in `decisionTold` (under the ledger's lock, after the commit,
  in the order of the commits) and judges each subscriber there, from
  the change: admitted after the change, `event: decision` with DR1's
  data (`{id, run, task?, state, rev}`, at most 256 bytes,
  `maxDecisionEvent`); admitted before it (`PolicyBefore`) and not after,
  one last `event: decision` with `{id, rev}` alone; neither, nothing.
  Each subscriber's queue holds 64 frames; a subscriber whose queue is
  full is ended, never passed over, so it misses no last `{id, rev}`
  silently (its reconnect's snapshot says what admits it then). Pings as
  `GET /api/events` sends them. `principalWatch(p, tok, need)` watches it
  with `need` the grant it opened with (admin when held, else decide);
  when it ends the stream, a frame under way is cut as `GET /api/events`
  cuts one, and otherwise one `event: revoked` with `{"reason":"access
  revoked"}` (at most 64 bytes) is written before the end. No session,
  activity, chat, run, Dispatch, Brakeman or open event is ever written
  to it: the hub has one producer, `decisionTold`. On a switchyard `403
  switchyard`, as the inbox.
- `server.go`: the two new routes registered; the decision routes and
  the inbox routes registered through `deciding` and `inboxFor`.

### `web/app`

- `utils/identityPass.ts` (new): `MAX_PASS = 2048`; `passFromFragment(
  hash)`, the fragment key `pass`, `^[A-Za-z0-9._-]{1,2048}$`, '' for
  anything else; `takeCredentials(href)`, which answers `{token, pass,
  href}`, the URL with both keys taken out of its fragment (the rest
  kept) whether or not their values were taken. `tokenFromFragment`
  (`utils/desktop.ts`) keeps its meaning and its 16 to 256 characters.
- `composables/useIdentityPass.ts` (new): the pass store,
  `localStorage['conductor.identityPass']` for that origin (in memory
  when storage is refused), `set` (a new pass replaces the old one),
  `clear`, and `ended` with `refused(sent)`, set by a `401` of a request
  that carried the pass still held. It never reads or writes
  `conductor.workbenchToken`.
- `plugins/token-fragment.client.ts`: `takeCredentials(location.href)`,
  then `history.replaceState` with the URL it answers, before anything
  else; then the token into the workbench store as today, and the pass
  into the pass store. A refused value is dropped from the address bar
  and stored nowhere.
- `composables/useCredential.ts` (new): the credential a request
  carries, the workbench token when one is stored, else the pass, and
  which it is. `useApi.ts` (`request`, `requestText`, `requestBlob`),
  `useAttention.ts`'s stream and `useDispatch.ts`'s upload take it from
  there; a `401` asks for the workbench token only when the request
  carried it (as today) and marks the pass `ended` when it carried the
  pass. Every credential goes in the `Authorization` header, never in a
  URL.
- `composables/useMe.ts` (new): `GET /api/me` once per credential;
  `decideOnly` is the grants holding `decide` and not `admin`.
- `middleware/decide-only.global.ts` (new): while `decideOnly`, every
  path but `/decisions` goes to `/decisions`; the page then uses the
  `bare` layout (`setPageLayout`), so no sidebar, palette or Needs you
  pill is drawn and `useAttention` is never started (it would open
  `GET /api/events`).
- `composables/useDecisionStream.ts` (new): the streaming fetch of
  `GET /api/decisions/events` with the credential in the header; on
  `snapshot` and on `decision` it has the inbox and the open card read
  again; on `revoked`, or a reconnect answered `401`, it marks the pass
  `ended`. `pages/decisions.vue` takes it in place of `useAttention`
  while `decideOnly`, and starts no `inbox.live.track()` then.
- `pages/decisions.vue`: while `decideOnly`, the header "Signed in as
  <name>" (from `useMe`); when the pass is `ended`, a `UAlert` with "Your
  access to this server has ended. Open it again from your workspace
  page." above the list, every send disabled, and the card's draft left
  as it is. `components/DecisionRespond.vue` gains `respondOnly`, which
  hides Comment and Ask for more; its draft is never cleared by a
  refusal.
- `utils/decisions.ts`: the policy's grant entry in the record's types,
  and "who may answer" naming it.
- `utils/protocol.ts`: `GRANT_DECIDE`, `MeReply`, the decision stream's
  `snapshot` (`DecisionStreamSnapshot`, with `MAX_DECISION_SNAPSHOT =
  200` and its 16 KiB), its `decision` data (DR1's, or `{id, rev}`
  alone) and `revoked` (64 bytes), the route's `grant` destination and
  `no_identity`, beside the existing `decision` event's rows.

### `web/e2e`

- `decide-only.spec.ts` (new), on the Playwright server with the fake
  identity provider #775's `about-identity.spec.ts` runs on. This row
  adds to that fake the passes its spec needs: a token of the form
  `e2e-pass.<subject>.<n>` (dots, so the new parser is exercised)
  admitted as `{subject, name, grants: [decide]}`, and, through the
  fake's seam, the end of one (its next `Authenticate` answers ok
  false), so a pass ends without a 30-minute wait.

### `internal/proto`, `web/app/utils/protocol.ts`, `docs/protocol.md`

No `internal/proto` change: the new wire is two HTTP routes and a
Server-Sent Events stream, whose bounds are constants in `internal/api`
as the `decision` event's are (`maxDecisionEvent`), with their
TypeScript mirror in `protocol.ts`. `docs/protocol.md`: the HTTP API's
opening paragraph gains the decide grant (what it admits, `401`
everywhere else, the recheck watching it on the decision stream); the
Decisions section gains `GET /api/me`, `GET /api/decisions/events` (its
events, bounds and order), the route's `grant` destination and `409
no_identity`, the response's and read's decide principal, and the
fragment key `pass` beside `token` (2 KiB of `[A-Za-z0-9._-]`, dropped
from the address bar, never sent in a URL).

### Docs

`docs/features.md`: the row's entry. `docs/repository-map.md`: the
`internal/api/decisions.go` row names the decision stream and `admits`;
the `ext` row names three grants.

## Events, orderings and invariants

**The pass.** The plugin reads the fragment, replaces the address bar's
URL with both keys taken out, and only then stores what it took; no
request is made before it runs (it is a client plugin, ahead of the
first page's setup). The pass leaves the browser only in an
`Authorization` header; the server reads no pass from a query (the
stream and every decide route call `presentedToken(r, false)`). A stored
workbench token is never written by this path, and while one is stored
the pass is kept and not sent.

**The route to the grant.** One transaction, in DR1's order for an
operation: the repeat look by `(decision, actor, opId)` first (its
receipt, `repeat: true`); then the state (`open`), `expectedRev` and the
run that holds it; then the policy rewritten, the `routed` row, `rev` +
1. The registration check (`no_identity`) is made before the
transaction and writes nothing. After the commit, `decisionTold` sends
DR1's `decision` event on `GET /api/events`, the notification, the
router's tell, and the decision stream's judgement, in that order, under
the ledger's lock: a decide principal's stream then gets `event:
decision` for the decision it was not admitted to a moment before. The
take-back is the same transaction with the entries removed; the stream
then sends that principal one `{id, rev}` and nothing more of it.

**A decide principal's response.** The route's `admits` (`404` when
not), then DR1's transaction, whose policy check is `Admits` over the
committed policy (a take-back in between: `not_approver`, kept); an
identity's response to a run not held is `not_held`, as today. After
the commit, `recheckResponse` asks the provider through the guard
(bounded by `ext.IdentityWait`) and calls `ConfirmAuthority` with
`admitted` true only for the same subject with a grant `admits` takes
under the policy as it stands then; `applyTx` checks `Admits` again
over the response's recorded authority. A provider that fails, is late
or is away has not said yes: the decision is superseded and the
replacement opens for the operator alone.

**The stream.** Subscribe, then read the snapshot, then write it, then
drain: nothing committed after the read is lost, and an event for a
decision the snapshot already lists is only read again. Each judgement
is made under the ledger's lock, after the commit, from the committed
row and `PolicyBefore`, so for one decision the stream's frames follow
the order of the commits and each is judged against the policy that
commit left. A revocation the provider answers in time ends the stream
(the cut, or `event: revoked` then the end); a recheck the provider does
not answer ends nothing.

**What a pass meets when it ends.** Every request asks the provider
(C2's rule), so the first request after the end is `401`; the stream
ends at its next recheck, within 30 s and `ext.IdentityWait`. A pass a
provider no longer admits is never cached by the server.

Invariants:

- **I1, one judgement.** Every decision of whether a principal may see,
  read, hear of or answer a decision is `crew.Admits`: the inbox, the two
  routes, the stream, the response's transaction, `recheckResponse` and
  `applyTx`. `TestAdmitsIsTheOneCheck` holds them to one table.
- **I2, nothing else.** A decide-only principal is answered `401` by
  every route outside `GET /api/me`, the inbox's list and item, the two
  decision routes and the decision stream, and is written no byte of a
  decision `Admits` refuses, but for the one `{id, rev}` of the change
  that took it out.
- **I3, the pass in no URL.** No request the app makes carries a pass or
  a token in its URL; the server reads neither from a query on these
  routes.
- **I4, the workbench token kept.** The pass path never writes
  `conductor.workbenchToken`.
- **I5, no schema change.** The ledger's tables and version are
  unchanged; an older binary fails closed on what this one writes.

## Out of scope

- Who issues a pass, its 30 minutes and the workspace page that opens
  the server: the provider's, which registers through `ext`; this
  repository only asks it.
- Any grant but `decide` in a route (a route to `host` or `admin` is
  `invalid`), a subject entry in a policy (SW1's, which adds its rule to
  `crew.Admits`), quorum, delegation and escalation.
- Comments, requests for more information, cancels and routes by a
  decide principal: the operator's.
- Decision links and their lane (MB0's PR-5 to PR-7): unchanged; a link
  holder is no principal of this grant.
- The host token, the uplink's credential, host connection rechecks and
  the provider's detail: #775 (`docs/plans/identity-uplink.md`).
- A draft kept across a new Open: the draft stays in the page only
  (Owner decisions, 11).

## Tests per surface

Go race (`go test -race -count=1 -timeout 60m ./...`, with `make lint`,
`make lint-static`, `make vuln`):

- `ext`: `TestGrantDecideValid` (`decide` valid, alone and beside
  `admin` and `host`; given twice refused; an unknown grant's words name
  the three).
- `internal/crew` (the engine half of the issue's route and recheck
  tests): `TestRouteToGrantAddsTheEntry` (open, at its rev, a `routed`
  row and `rev` + 1; answered, closed, a stale rev and a run not held
  refused with no row; a second route writes its row and leaves the
  policy and `rev`), `TestOperatorRouteTakesTheGrantBack`, and
  `TestDecideResponseUnderThePolicy` (a decide authority accepted under
  a policy with the entry and `not_approver` without it; `applyTx`
  applying it only while `Admits` takes it).
- `internal/api`, the issue's tests, in `decide_test.go` with
  `exttest`'s fake identity provider:
  `TestDecideGrantAdmitsDecisionRoutesOnly` (every registered route
  walked with a decide-only principal: `200` only for `GET /api/me`, the
  inbox's list and item, the two decision routes of an admitted
  decision and the stream; `404` for a decision not admitted; `401` for
  every other, `GET /api/events` among them),
  `TestMeAnswersEveryPrincipal` (workbench, host token, link, provider's
  principal with each grant, none: `401`),
  `TestRouteToGrantNeedsAProvider` (no provider registered: `409
  no_identity`, no row; registered: the entry and the row),
  `TestConfirmAuthorityAdmitsDecide` (the same subject with the decide
  grant admitted for a decision with the entry, refused for one without
  it, another subject refused, a provider late through the guard's gate:
  superseded), `TestPrincipalWatchWatchesDecide` (the stream of a decide
  principal ended when the recheck answers no decide grant or another
  subject, kept for an error or a late answer, the recheck driven
  through `extAfterOr`'s seam, never a 30 s wait),
  `TestDecisionStreamCarriesOnlyAdmittedDecisions` (sessions started,
  activity, chat and other decisions changed meanwhile: the snapshot and
  the events hold the admitted decisions alone, and their bounds hold at
  200), `TestDecisionStreamPolicyChangedDuringDelivery` (the principal
  taken out of a decision's authority between two changes: one last
  `{id, rev}`, then nothing of it, the second change held at a gate in
  `decisionTold` until the first is read), `TestAdmitsIsTheOneCheck`
  (one table of authorities, operator alone, operator and grant,
  unreadable, unknown kind, against admin, decide, both, none and a
  router: the read model, the two routes, the stream and
  `ConfirmAuthority`'s `admitted` agree with `crew.Admits` on every
  row).
- `docs/protocol.md`'s rows are checked by the tests above naming each
  bound (`maxDecisionSnapshot`, `maxDecisionSnapshotBytes`, the
  `revoked` frame, `GET /api/me`'s 512 bytes).

Vitest (`npm --prefix web test`, with `npm --prefix web run typecheck`):

- `utils/identityPass.test.ts`: a 2,048-character pass with dots taken,
  2,049 refused, a character outside the set refused; both keys dropped
  from the fragment, the rest kept, a refused value dropped too.
- `composables/useIdentityPass.test.ts`: a stored workbench token never
  replaced by a pass; a new pass replaces the old; `ended` only for a
  `401` of the pass still held.
- `composables/useApi.test.ts` (extended): the workbench token sent when
  stored, else the pass, always in the header.

Playwright (`make test-e2e`, after `npm --prefix web run typecheck:e2e`):

- `decide-only.spec.ts`: a pass from the fragment shows the decision
  page alone ("Signed in as …", no sidebar), with the decision routed to
  the grant and without the one that is not, the address bar without the
  pass, and no request URL holding it nor any request to `/api/events`;
  in a context that held a workbench token, the token is still
  `conductor.workbenchToken` after a pass arrives; a pass that ends,
  through the fake provider's seam, shows "Open it again" when the
  answer is sent, the draft still in its field.

By hand: nothing this repository can check that the tests cannot; a live
check with a real provider is made where one is registered.

Not affected, named as not run in the PR body: the scripts suite, `make
desktop-test`, `make test-pebble`, `make test-live`.

## Boxcar constraints

1. **Launches through `crew.Launcher`.** Not touched: nothing starts a
   run or a process.
2. **`session.Local` drives a `session.Process`.** Not touched.
3. **An attempt's context is files.** Not touched: a decide answer is
   delivered by DR1's application, unchanged.
4. **Agents talk over the network.** Not touched.
5. **Checks through `internal/proc`.** Not touched.
6. **Capabilities, never plans.** Honoured: the app shows the decide page
   from `GET /api/me`'s grants, what the principal may do, never a plan;
   the route to the grant asks only whether a provider is registered.
7. **Evidence rows and `where`.** Not touched: no evidence row changes.
8. **`hostagent` is a launch site.** Not touched: the grant answers
   decisions on the server that holds the ledger; a host is unchanged.

## Open-core checklist

- [x] Article 1: no premium code; the one check is an identity
  provider's registration (`no_identity`).
- [x] Article 2: a plain build has no provider and so no decide
  principal; it behaves as `main`.
- [x] Article 3: no flag; the grant exists only in a provider's answer.
- [x] Article 4: stop, inspect, revoke, export and approvals stay the
  operator's; the operator takes a grant back, and a revoked pass loses
  its requests at once and its stream within the recheck.

## The PRs

The one PR is issue #776's: one branch `issue-776`, cut from `main` once
#775's PR has merged, a draft PR at its first commit, the checks of every
surface before each push, its body ticking the open-core checklist. It
leaves `main` working and green on its own and carries the
`docs/features.md` entry.

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with |
|---|---|---|---|---|---|---|
| PR-1 (#776, ~700 + 800 lines) | ext, api, web: a decide grant and a pass taken from the fragment | `ext.GrantDecide`; `crew.Admits`, the policy check, the route to the grant and the take-back, `PolicyBefore`; `GET /api/me`, `deciding`, `inboxFor`, `GET /api/decisions/events` and its hub, `admits`, `principalWatch`'s grant, `authorityOf`, `recheckResponse`, `no_identity`, Brakeman's refusals; the pass parser, its store, the plugin, `useCredential`, `useMe`, the middleware, `useDecisionStream`, the page's decide-only view; the fake provider's passes; `docs/protocol.md`, `protocol.ts`, `docs/features.md`, `docs/repository-map.md` | `ext`, `internal/crew`, `internal/api`, `web/app`, `web/e2e`, `docs` | every test above | #775 (its PR merged); DR1 and MB0 on `main` | none: #775 shares `ext`, `internal/api`, `web/app` and `docs` |

## Owner decisions

Dated 2026-11-10. Each is a default for a choice the issue's words leave
open, the fail-closed one, which the owner may overturn.

1. **"A new revision" is the decision's `rev`.** The route to the grant
   bumps the record's `rev` and leaves the policy's revision `local/1`:
   `stale_policy` refuses any revision but the one in force, so a new
   policy revision would refuse every response, while `rev` + 1 makes a
   card read before the route `stale_decision`.
2. **The operator takes a grant back.** A route to the operator of an
   open decision whose policy holds a grant entry removes it (a new
   `rev`, a `routed` row). It is the one change in this repository that
   takes a principal out of a decision's authority, which the stream's
   last `{id, rev}` exists for; without it a grant once given could be
   withdrawn only by cancelling the decision.
3. **Taken only while open, at its rev, through the held run.** The route
   to the grant follows HM1's route to a person; once answered, nothing
   changes who may answer it.
4. **Brakeman neither gives nor takes the grant.** Its act refuses `to:
   grant` and a route to the operator of a decision given to the grant.
5. **Not admitted is not found.** A decide principal's read or response
   of a decision `Admits` refuses is `404 not_found`, as one that does
   not exist, so the routes say nothing of decisions it may not see.
6. **A replacement is the operator's.** The fresh decision that replaces
   an identity's answer not rechecked opens under the operator's policy
   alone; the operator routes it again if the person should answer it.
7. **The snapshot lists open and answered decisions**, newest first, the
   200 the bound holds; closed ones are the inbox's Recent.
8. **The decide read model is DR1's records with response actions
   only**; the track route stays the admin's.
9. **`GET /api/me` for the chain's tokens**: the workbench token `admin`
   and `host`, a host token `host`, a link none.
10. **The stream's close**: a frame under way is cut, as `GET
    /api/events` cuts one; otherwise one `event: revoked` with `access
    revoked` is written, then the end. A full queue ends the stream
    rather than skipping a frame.
11. **The draft stays in the page**, never in the browser's storage: a
    new Open reloads the page and the person types it again.
12. **The workbench token wins.** With one stored, the pass is kept and
    not sent, and a `401` of the workbench token never falls back to it.
13. **On a switchyard the decide routes are `403 switchyard`**, as the
    inbox's are: decisions are answered where the work runs.

## Acceptance

The acceptance boundary is observed when the PR's checks pass in CI and
locally: `TestDecideGrantAdmitsDecisionRoutesOnly` and the other
`internal/api` tests show a principal with only the decide grant
answering the decisions it may and refused everything else, `GET
/api/events` among them, and `TestDecisionStreamCarriesOnlyAdmittedDecisions`
shows it hearing of nothing else; the vitest files and
`decide-only.spec.ts` show the pass taken from the fragment, dropped
from the address bar and never sent in a URL. Done is the PR merged with
those checks green. Nothing needs the owner but the merge; a live check
with a real identity provider is made where one is registered.

## The amendment to docs/plans/decisions.md

This plan's pull request changes `docs/plans/decisions.md` in these
places, quoted as that file now reads.

The policy (The changes, the ledger):

> `policy` (JSON): `{revision: "local/1", approver: "operator", quorum: 1,
> authority: [{kind: "operator"}]}`. The only policy of this repository is
> `local/1`: one response from a principal its `authority` admits decides
> it. `{kind: "operator"}` admits the admin grant (the workbench token, or
> an identity provider's principal with `ext.GrantAdmin`); `{kind:
> "grant", grant: "decide"}`, which only a route to the grant adds
> (Routing, below), admits an identity provider's principal with
> `ext.GrantDecide` (`docs/plans/decide-grant.md`). One function,
> `crew.Admits`, judges every entry, for the response's transaction, the
> application's recheck and every read. The record keeps it so that a
> response and an application check the policy the decision was opened
> under against the one in force, and so that a later policy is a later
> revision of the same field.

A response's authority (the same section):

> A response's `authority` is what the policy checks, kept apart from its
> `actor`, the audit's words: `grant` (`admin`, else `decide`, or none), `via`
> (`workbench`, `identity` or `router`) and the identity's `subject`, all
> from the principal the request authenticated (`authorityOf` in
> `internal/api`, beside `actorOf`), never from a body; `boot` is the
> server process's id, minted at its start. A router's answer offered to
> the engine has `via: router` and no grant, so it can never pass for an
> admin's answer whose subject it shares.

The recheck (Applying, in `internal/crew`):

> The authority is rechecked from the response's `authority`, never from
> its actor: a `workbench` response holds (the operator's token is the
> server's, decision D4); a `router` one never got here; an `identity`
> one is applied only once the identity provider has admitted its token
> again after the response committed, since a provider may revoke a
> grant between the response's transaction and the application's with
> the server still running (what `principalWatch` rechecks a connection
> for). The response route is the only holder of the token, in its
> memory for that request and never in the ledger or a log: once the
> response has committed it asks the provider again through the guard
> (`guardIdentity.recheck`, bounded by `ext.IdentityWait`, as
> `principalWatch` asks) and hands the engine the answer,
> `Engine.ConfirmAuthority(id, k, admitted)`; until then
> `applyDecisions` leaves that decision `answered` and applies nothing of
> it. Admitted again as the same subject with a grant the decision's
> policy admits (the admin grant; or the decide grant, for a decision
> whose `authority` holds `{kind: "grant", grant: "decide"}`: `crew.Admits`,
> `docs/plans/decide-grant.md`): the
> application proceeds in its own transaction, which checks that `k` is
> still the decision's accepted response and `boot` this process's. Not
> admitted, or no answer in time (a provider that fails, is late or is
> busy has not said yes, and a gate is never released on what nobody
> said): the decision is superseded by a fresh one, the same request and
> holds ("user:nate's answer could not be rechecked; dr-9 asks again"),
> which always has its place among the run's bounds (A replacement always
> has its place, in the ledger, above),
> and the operator answers again, with the workbench token if the
> provider is away. One found after a restart (the crash fell between the
> response and its application; its `boot` another process's) has no
> recheck coming, since the token was never kept: superseded the same
> way. A run not held never takes an identity's response (`not_held`,
> above), so no such response waits for a continuation.

Routing (the operator's operations):

> **Routing.** `Engine.RouteDecision(ctx, id, opID, to, note, actor)`
> records a `routed` history row. DR1's only destination is `{kind:
> "operator"}` (a re-route to the operator, with a note: what Brakeman's
> `route_decision` grant does in BK2); any other kind is `invalid` until
> HM1 adds a person's label. It changes no state, but for the one case
> below. A route to `{kind: "grant", grant: "decide"}`
> (`docs/plans/decide-grant.md`) adds that entry to the decision's policy
> `authority`: refused `409 no_identity` when no identity provider is
> registered, and taken only while the decision is open, at the
> `expectedRev` its body carries and through the run that holds it, as a
> route to a person is (HM1, below); in one transaction it rewrites the
> policy, writes the `routed` row and bumps `rev`, so a card read before
> it is `stale_decision`. A route to the operator of an open decision
> whose `authority` holds a grant entry takes the entries out the same
> way (the policy back to `{kind: "operator"}` alone, a `routed` row,
> `rev` + 1).

The route table's rows (`internal/api`: the routes; the other rows are unchanged):

| Route | Who | Body → reply |
|---|---|---|
| `GET /api/decisions/{did}` | admin, or a principal with the decide grant the decision's policy admits (`404 not_found` for one it does not) | the record whole with its evidence's `refs` (at most 1 MiB as encoded, the sum of its parts at their bounds: the 16 KiB request, its 10 KiB evidence baseline, 32 receipts of at most 8 KiB (256 KiB), 256 history rows of at most 2 KiB of detail and 256 bytes of envelope (576 KiB), the 4 KiB application receipt and 20 ref resolutions of at most 2 KiB (40 KiB), 902 KiB in all; `TestTheFullestDecisionFitsItsBound` builds one with every part at its bound) |
| `POST /api/decisions/{did}/respond` | admin, or a principal with the decide grant the decision's policy admits (`404 not_found` for one it does not) | `{responseId, choice?, text?, rationale?, expectedRev, byName?}` (8 KiB) → `201 {receipt, decision}`; `200` repeat; `409 decision_answered`, `decision_closed`, `stale_decision`, `stale_policy`, `response_conflict`, `not_held`; `403 not_approver`; `503 decision_moved` |
| `POST /api/decisions/{did}/route` | admin | `{opId, to: {kind: "operator"} \| {kind: "grant", grant: "decide"}, note?, expectedRev}` → `{receipt, decision}`; `200` the same for a repeat; `409 op_conflict`; `409 no_identity` (a route to the grant on a build with no identity provider registered); `429 decision_limit` |

The paragraph after the table:

> A share link's controller, an agent's token and a Brakeman turn's token
> are refused every admin route (`requireAdmin`'s `401 unauthorized`), so
> the free build's one approver is the operator; `403 not_approver` is the
> policy's refusal of a principal that got past it without the authority
> `local/1` names (a router's answer, below). A principal holding the
> decide grant and not the admin grant, which only a build with an
> identity provider registered can present, is refused every route but
> the read and the response of a decision its policy admits and the
> decide-only routes of `docs/plans/decide-grant.md` (`401 unauthorized`,
> as today).
