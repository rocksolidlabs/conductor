# A decide grant and a pass taken from the fragment (issue #776)

Today an identity provider's principal is the operator (`ext.GrantAdmin`)
or a host (`ext.GrantHost`), nothing between. A person who should answer
one decision and see nothing else has no grant to hold, so a provider
could only make that person the operator. This plan adds the grant
between them: `ext.GrantDecide`, which answers a durable decision (DR1,
`docs/plans/decisions.md`) the operator has routed to it, and reads
nothing else; the decide page, one decision's page drawn for such a
principal alone; DR1's route operation taught to give a decision to the
grant and to take it back; and the web app's way to take such a
principal's credential, a pass, from the URL fragment without ever
sending it in a URL.

It is the row of issue #776, written from the issue's words against
`main` at `175e0be5` (2026-11-10) after reading `ext/identity.go` and
`ext/valid.go`, `ext/exttest/fakes.go`, `internal/api` (`auth.go`,
`identity.go`, `extensions.go`, `decisions.go`, `decision_refs.go`,
`events.go`, `inbox.go`, `inbox_decisions.go`,
`brakeman_act_decision.go`, `server.go`), `internal/crew`'s
`decisions.go` and `decisions_wait.go`, `internal/proto/decisions.go`,
and `web/app` (`app.vue`, `layouts/`, `plugins/token-fragment.client.ts`,
`utils/desktop.ts`, `composables/useWorkbenchToken.ts`, `useApi.ts`,
`useInbox.ts`, `useDecisions.ts`, `useInboxActs.ts`, `useBrakeman.ts`,
`useTerminalTransport.ts`, `pages/decisions.vue`, `pages/yard.vue`,
`pages/sessions/[id].vue`). The issue's words decide the scope; where
they leave a choice open, or where a narrower surface meets the issue's
outcome with less to leak, the fail-closed answer is taken and listed
under Owner decisions. The plan pull request also carries the amendment
the issue asks of `docs/plans/decisions.md` (DR1's route table and its
recheck), quoted whole below.

**Prerequisites.** On `main`: C2 (the identity provider seam,
`docs/plans/ext.md`), DR1 with every row (the records, their routes,
`authorityOf`, `ConfirmAuthority`, `principalWatch`'s use by the events
stream), HM1 (the route to a person, whose rules the route to the grant
copies) and MB0's run rows (the operator's decision page). Not on
`main`: issue #775, the stream's other open-source row (the host token
and the uplink's credential, `docs/plans/identity-uplink.md`). Both rows
change `ext/identity.go`, `internal/api`'s identity checks,
`docs/protocol.md` and `web/app/utils/protocol.ts`, so this row is built
after #775 merges and on what it adds: `principalWatch` taking the grant
a connection needs and the token it watches (#775's `watchedToken`), and
the seam's words for an ended admission. That plan owns those pieces;
this one names them and restates none.

The names follow the railroad: Conductor is the application, Yardmaster
the harness that holds the wait and applies the answer, Switchman the
routing whose team layer is a later item. "Workspace page" here is only
the words a person reads ("Open it again from your workspace page"), the
issue's: this repository knows no workspace, and nothing in it says what
registers an identity provider, how it issues a pass, how long a pass
lasts or how a person is sent to the decide page. It only asks whether a
provider is registered and asks the provider about a token. The rest is
the business of the module that registers a provider.

## Articles it serves

1. **No premium code.** The grant is a word of `ext`; the only check is
   whether an identity provider is registered (`409 no_identity` on the
   route to the grant), the registration fact `GET /api/capabilities`
   already reads.
2. **Free build fully useful.** A plain build registers no provider, so
   no principal holds the grant and no decision is routed to it; every
   route, page and stream answers as today, and a pass in a fragment is
   an unknown token (`401`).
3. **Gated by hosted dependencies.** Nothing here is a flag: a decide
   principal exists only when an identity provider registered through
   `ext` admits a token with the grant.
4. **Stop, inspect, revoke, export, approvals free.** The grant answers
   one routed decision and reads its own outcome; stop, inspection,
   export and every other approval stay the operator's. The operator
   takes the grant back from a decision in every state, whether or not
   its run is held (the take-back, below), and a provider's revocation
   ends the principal's requests at once and its stream within the
   recheck (`identityRecheckEvery`, 30 s, and `ext.IdentityWait`).
5. **Dependencies one way.** No import changes; `ext` stays standard
   library only.
6. **No feature build tag.** None added; no test-only binary either
   (Tests, Playwright).
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
    tests, vitest and Playwright, the protocol triple together
    (`internal/proto`, `protocol.ts`, `docs/protocol.md`), each new
    message with its bound and its test.
11. **Names.** As above; the grant is `decide`, a word the repository
    does not already use for anything else.
12. **One execution seam.** Not touched: nothing here starts a process.

## Why and what happens

Without this row, a decision that should go to one person goes to the
operator, or the person is made the operator. After it, on a build with
an identity provider registered:

1. The operator routes decision dr-7 to the decide grant through DR1's
   route operation (`POST /api/decisions/{did}/route`, the guide's
   `decision_route`) with `to: {kind: "grant", grant: "decide"}`. From
   then on dr-7's policy admits **anyone** the provider gives the decide
   grant, not one person: the route's words and "who may answer" say so.
   Its `rev` moves, and its history says who routed it.
2. The person is sent to `https://<server>/decide/dr-7#pass=<pass>`, the
   decide page's address with a pass in its fragment. How they are sent,
   and by what, is the provider module's business. The app takes the
   pass out of the address bar before anything else runs, keeps it for
   the tab, and asks `GET /api/me`, which answers the principal's name
   and grants.
3. A principal with the decide grant and not the admin grant sees the
   decide page alone, headed "Signed in as Ana": no sidebar, no sessions,
   no runs, no Dispatch, no inbox. The page reads dr-7 through
   `GET /api/decisions/dr-7`, which answers a decide principal the decide
   view (a fixed projection, The decide view, below), answers through
   `POST /api/decisions/dr-7/respond`, and hears of dr-7's changes
   through `GET /api/decisions/dr-7/events`. Those three and
   `GET /api/me` are the grant's whole surface.
4. Ana's answer is recorded under `user:<subject>` with the authority
   `{grant: decide, via: identity}`, rechecked with the provider after
   its commit, and applied by Yardmaster as any identity's answer is.
   Once dr-7 is answered, Ana reads only the outcome of her own answer,
   and another holder of the grant reads nothing of it.
5. When the provider stops admitting the pass (its end, or a revocation),
   the next request is `401` and the stream ends at its next recheck; the
   page says "Your access to this server has ended. Open it again from
   your workspace page.", clears the pass, and keeps the answer Ana had
   not sent yet. A new Open brings a new pass.

A browser that already holds a workbench token for that origin keeps it:
the pass never replaces it, and the decide page's address then opens the
operator's page for dr-7 instead.

## The decide principal's surface

A closed list. A principal holding `decide` and not `admin` is admitted
by these four routes and by nothing else; every other route answers it
exactly as it answers a request with no credential (`401` under the rate
limit for every route that needs one, `GET /api/events`, the inbox's
three routes, `GET /api/decisions` and the operator's operations among
them).

| Route | What it answers a decide principal | Why the person needs it |
|---|---|---|
| `GET /api/me` | `{subject, name, grants}` | the page's "Signed in as", and whether this pass is a decide pass |
| `GET /api/decisions/{did}` | the decide view of one decision it is admitted to (below); `404 not_found` otherwise | the question it was routed, and later the outcome of its answer |
| `POST /api/decisions/{did}/respond` | DR1's response, judged by the policy; its reply the decide view and its own receipt | the answer itself |
| `GET /api/decisions/{did}/events` | Server-Sent Events of that one decision: `{id, state, rev}` while admitted, one last `{id, rev}` when not | what tells an open page the decision changed, or that its admission ended |

Asked of each further route the first draft gave the grant, whether the
issue's outcome ("a person who should answer one decision, and see
nothing else") needs it: the inbox list and item (`GET /api/inbox`,
`GET /api/inbox/item`), their counts and pages, the track route
(`GET /api/inbox/track`), a stream across decisions, Brakeman's card on
an item, and evidence refs resolved through the operator's resolver. It
does not: a person sent to one decision's address needs none of them, and
each is a read computed over records the person may not see. None is
given to the grant (Out of scope; Owner decisions, 1). With no list,
there is no count, page or cursor to compute over admitted rows.

## The decide view

What `GET /api/decisions/{did}` answers a decide principal, what the
respond route answers it beside its receipt, and nothing more: one Go
type, `proto.DecideView`, built in one function, `decideView(d
crew.Decision, subject string, a crew.Admission) proto.DecideView`, from
the one record the route read (The changes, `internal/api`). Nothing in
it is resolved through another record: `decisionRefs` and the inbox's
item builders are never called for a decide principal.

| Field | From | Why the answerer needs it |
|---|---|---|
| `id` | the record's id | names the decision in the respond route |
| `rev` | the record's `rev` | the `expectedRev` of the answer |
| `state` | the record's state | whether it can still be answered |
| `type` | the record's type | which form the answer takes (an option, approve or reject, a line) |
| `question` | the request's `question` | what is asked |
| `context` | the request's `context` | the asker's words for why |
| `options[{label, tradeoffs}]` | the request's `options` | what to choose from |
| `recommendation?` | the request's `recommendation` | the option the asker recommends |
| `uncertainty?` | the request's `uncertainty` | what the asker is unsure of |
| `authorizes?` | the request's `authorizes` | what an answer lets happen |
| `createdAt` | the record's `createdAt` | when it was asked |
| `own[{k, status, code?, rev, at, choice?}]` | the record's responses whose actor is `user:<this subject>`, the newest 8 | what became of its own answers; never another responder's |
| `outcome?` | computed from the state and `own` (below) | the outcome of its own answer, which the page says in words |

While the decision is `open`, the view holds every field. Once it is
`answered` or final (applied, superseded, cancelled or expired), it holds
`id`, `rev`, `state`, `own` and `outcome` alone. `outcome` is one of
`recorded` (answered, the accepted response its own), `applied` (applied,
its own), `not_used` (superseded after its own accepted response: the
recheck did not admit it, and the replacement is the operator's),
`answered_elsewhere` (answered or applied, the accepted response
another's; who is never said) and `closed` (cancelled, expired, or
superseded with no accepted response of its own).

Left out, each on purpose: the run, `openedIn`, `origin`, `gate`,
`asker`, `member`, `ticket`, `plan`, `planDigest`, `requestId`, the
request's `evidence`, `expertise`, `subject` (a commit, a run, a task, a
path) and `for` (HM1's label), the refs and their resolutions, `policy`,
`why`, `supersededBy` (a decision the principal is not admitted to),
`answer` (it names who answered), every other responder's receipt,
`applied` (it names the run and the task), the history (the operator's
comments, notes and routes, the router's rows), Brakeman's card, and the
inbox item's run, ticket and session names and its blocking. A receipt's
`words`, `actor`, `byName`, `text` and `rationale` are left out of `own`
too: the engine's words name other people (`answeredWords` names who
answered first), and the page says each code in words of its own.

A refusal the respond route answers a decide principal carries
`{error: {code, message}, receipt?}`, its `message` the fixed words of
its code (`decideWords`, at most 200 bytes, naming no one) and its
`receipt` its own, reduced as `own` is: never the engine's words.

## Admission and its end

`crew.Admission(d Decision, who Admittee) Admission` is the one
judgement, over the record a route read:

- `Whole`: `who` holds the admin grant (the workbench token, or a
  provider's principal with `admin`): DR1's routes as today.
- `Open`: `who` holds `decide`, a policy entry admits it (`crew.Admits`,
  below) and the decision is `open`: the decide view whole.
- `Outcome`: `who` holds `decide`, a policy entry admits it, the decision
  is `answered` or final, and `own` is not empty: the outcome view.
- `None`: anything else, and every decision that does not exist:
  `404 not_found` with the same body for both.

So admission ends:

- **with the answer**, for every holder of the grant without a response
  of its own: once dr-7 is answered, a later pass, or walking the `dr-N`
  ids, finds nothing;
- **with the take-back**, for everyone: the operator's route to the
  operator takes the grant entry out of the policy in every state, open,
  answered or final, held run or not, whatever `rev` the operator read
  (it only narrows who may read; it never changes the answer), and the
  next read, response or delivery of each holder is `None`;
- **with the pass**, when the provider stops admitting it: the next
  request is `401`, and the stream ends at its next recheck
  (`principalWatch`, within `identityRecheckEvery` and
  `ext.IdentityWait`).

A take-back after the response committed and before `recheckResponse`
asked the provider makes that answer not admitted, so the decision is
superseded and the replacement is the operator's (DR1's rule); a
take-back after the confirmation changes who may read the decision, not
its application.

What an open page does when its admission ends: the stream sends one
last `{id, rev}` and ends (the take-back, the answer for a principal with
no response of its own), or is cut (the pass); the page reads the
decision once more and says "dr-7 is no longer open to you." on a `404`,
or the ended words on a `401`, with the draft kept either way. Both
happen as soon as the stream's next delivery or the recheck has run: no
timer of the page's own is involved.

## The changes

### `ext`

- `identity.go`: `GrantDecide Grant = "decide"`, documented as "answer
  a decision routed to the grant, and read nothing else". `Grant.Valid`
  accepts it; `Principal.Valid`'s refusal words name the three ("is not
  admin, host or decide"). `IdentityProvider`'s comment, as #775 leaves
  it, gains the decide grant among what a recheck asks: a connection is
  closed when the answer is another subject or a principal without the
  grant that connection needs (`decide` for a decide principal's
  decision stream).
- `ext/exttest`: nothing new; `FakeIdentity.Add` already takes any valid
  principal, so its tests' passes hold `decide` once `Grant.Valid` does.

### `internal/proto`

`decide.go` (new), the wire of the four routes as Go types with their
bounds, beside the decision links' (`decisions.go`):

- `MeReply{Subject, Name string; Grants []string}` (`subject` and `name`
  omitted when empty) and `MaxMeReply = 1 << 10`: a subject of
  `ext.MaxSubject` (48) bytes and a name of `ext.MaxPrincipalName` (40)
  characters, each a character `encoding/json` escapes to six bytes
  (`<`, `>`, `&`, U+2028), are 528 bytes, the three grants and the rest
  under 100.
- `DecideView` (the table above), `DecideReceipt` (`own`'s entries),
  `MaxDecideView = 32 << 10`: the request's fields at most
  `ledger.MaxDecisionRequest` (16 KiB) as the ledger keeps them encoded,
  8 receipts of at most 256 bytes, the rest under 1 KiB.
- `DecideReply{Receipt DecideReceipt; Decision *DecideView}` and
  `DecideRefusal{Error{Code, Message}; Receipt *DecideReceipt}`, with
  `MaxDecideReply = MaxDecideView + 1<<10` and `MaxDecideWords = 200`.
- `DecisionStreamEvent{ID string; State string; Rev int}` (`state`
  omitted on the last), `MaxDecisionStreamEvent = 128`.
- `MaxDecisionStreams = 64` (in all) and
  `MaxDecisionStreamsPerSubject = 4` (an identity's subject; the
  workbench token is one subject of its own).
- `OutcomeRecorded`, `OutcomeApplied`, `OutcomeNotUsed`,
  `OutcomeAnsweredElsewhere`, `OutcomeClosed`, the `outcome` codes.

### `internal/crew`

- `decisions_admit.go` (new): `PolicyEntry{Kind string; Grant string}`
  (`json:"kind"`, `json:"grant,omitempty"`), the entries of a policy's
  `authority`; `Admittee{Grants []string; Via string; Subject string}`,
  who asks; `Admits(entries []PolicyEntry, who Admittee) bool`: each
  entry admits by its own kind's rule, and one entry that does admits.
  `operator` admits `via` `workbench` with the admin grant, or `via`
  `identity` with the admin grant; `grant` admits `via` `identity`
  holding that grant; `router` is admitted by no entry; an unknown kind
  admits nobody. A later kind (SW1's subject entry) adds its rule here and
  nowhere else. `PolicyOf(raw) ([]PolicyEntry, error)` reads a record's
  policy; a policy it cannot read admits nobody. `Admission(d Decision,
  who Admittee) Admission` (Admission and its end, above) is `Admits`
  plus the state and the principal's own responses, over one record.
- `decisions.go`: `RespondDecision`'s check of the authority
  (`auth.Grant != grantAdmin`, today) becomes `!Admits(policy, auth as
  an Admittee)`, refusing `not_approver` ("dr-7 is not yours to answer:
  refused under local/1") over the policy its transaction read. The
  policy keeps its revision, `local/1`, so `stale_policy` keeps its
  meaning. `applyTx` is unchanged: it checks, as today, that an
  identity's answer was confirmed by this process (`ConfirmAuthority`)
  and that its `boot` is this process's; the grant is judged in the
  response's transaction and again in `recheckResponse` (below), and no
  route widens a decision's policy once it is answered, so `applyTx` has
  nothing further to judge.
- `decisions_wait.go`: `Destination` gains `Grant string`
  (`json:"grant,omitempty"`). `RouteDecision` takes `{kind: "grant",
  grant: "decide"}` (any other grant `invalid`): only while the decision
  is open, at its `expectedRev`, through the run that holds it (`not_held`
  otherwise, "continue it, then route it to the grant"), as `forRoute`'s
  person route is taken; in one transaction it adds the entry to the
  policy's `authority`, writes the `routed` row (`{to: [{kind: "grant",
  grant: "decide"}], rule: "local/1: the operator, or anyone holding the
  decide grant", by}`) and bumps `rev`. A policy that already holds the
  entry is not changed: the row is written, `rev` is not bumped, and the
  words say "dr-7 already admits the decide grant". **The take-back**: a
  route to the operator of a decision whose `authority` holds a grant
  entry takes the entries out (policy back to `policyJSON`), writes the
  row and bumps `rev`, in every state, through the held run or the
  ledger alone (`operate`'s `held` and `notHeld` both), whatever
  `expectedRev` it names; to the operator of any other decision it
  changes nothing, as today. The policy stays under
  `ledger.MaxDecisionPolicy` (1 KiB): each entry is held once.
- The fresh decision that replaces an identity's answer not rechecked
  (`replaceDecision`) opens under `policyJSON`, the operator alone
  (Owner decisions, 7).
- No ledger schema change and no migration: `policy` and `authority` are
  JSON columns already. An older binary reading a policy with a grant
  entry checks only its revision and the admin grant, so it refuses a
  decide answer `not_approver`, and it supersedes an identity's answer
  whose `boot` is another process's: both fail closed.

### `internal/api`

- `identity.go`: `principal` gains `decide bool`; `grantPrincipal` sets
  it for `ext.GrantDecide`. `requireAdmin` is unchanged, so a
  decide-only principal is `401` on every admin route, `GET /api/events`
  among them (its check is `p.admin`). `admission(d crew.Decision, p
  principal) crew.Admission` (new) is `crew.Admission` over the record
  and the principal as an `Admittee`; the read, the respond route, the
  stream and `recheckResponse` call it and nothing else. The decision
  stream's watch is #775's `principalWatch` with the grant `decide` (or
  `admin` for an admin's stream).
- `me.go` (new): `GET /api/me`, any authenticated principal:
  `proto.MeReply`, at most `proto.MaxMeReply`: the workbench token
  `["admin", "host"]`, a host token `["host"]`, a share link `[]`, a
  provider's principal its subject, name and grants; anything else
  `401` under the rate limit. Not refused on a switchyard (the workbench
  there is `["admin", "host"]` too; its lack of a role is `whoami`'s to
  say).
- `decisions.go`: the three decision routes of the surface are wrapped
  in `deciding` (new): the admin exactly as `launching` (so `403
  switchyard` on a switchyard for everyone), a decide-only principal
  through to the handler, `401` for every other principal under the rate
  limit. `GET /api/decisions` and the operator's operations stay
  `launching`.
  - `handleDecision`: one read of the record (`s.runs.Decision`), then
    `admission` over that record, then the reply built from that same
    record: `Whole`, `{decision, refs}` as today; `Open` or `Outcome`,
    `decideView` (the version judged is the version written); `None`,
    `404 {error: {code: "not_found", message: "no such decision"}}`,
    the body a decision that does not exist gets for a decide principal.
  - `handleDecisionRespond`: for a decide principal, one read and
    `admission` first (`None`: the same `404`, nothing written), then
    DR1's transaction, whose `Admits` over the policy it reads decides
    (a take-back in between: `403 not_approver`, its receipt kept). Its
    reply is `proto.DecideReply`: its own receipt reduced, and the
    decide view of the record read again after the commit, judged again
    (omitted when that read is `None`); a refusal `proto.DecideRefusal`
    with `decideWords(code)`.
  - `authorityOf` records the grant: `admin` when held, else `decide`,
    with `via: identity` and the subject.
  - `recheckResponse`'s `admitted` becomes: the provider answered in time,
    the same subject, and `admission` of the record read after the
    recheck, under the principal the recheck answered, is not `None`.
  - `handleDecisionRoute` refuses `to: grant` with `409 no_identity`
    ("no identity provider is registered: a decision is given to the
    decide grant only where one is") before the engine when `s.exts`
    holds no identity provider (`Registered()` lists none of kind
    identity); the route stays the admin's.
- `decision_stream.go` (new): `GET /api/decisions/{did}/events`,
  Server-Sent Events of one decision, through `deciding`, the token from
  the `Authorization` header alone (`presentedToken(r, false)`: never a
  query).
  - **Bounds first**: at most `proto.MaxDecisionStreams` open in all and
    `proto.MaxDecisionStreamsPerSubject` per subject, counted under the
    hub's mutex; past either, `429 {error: {code: "stream_limit"}}`
    before any read.
  - **Open**: subscribe (a one-slot signal channel keyed by the
    decision's id), then read the record and judge `admission` (`None`:
    `404`, unsubscribed, nothing written), then `200` and
    `event: decision` with `{id, state, rev}` of that read.
  - **Told**: `decisionTold`, under the ledger's lock after the commit,
    calls `decisionStreams.changed(id)`, which takes the hub's mutex,
    finds that id's subscribers in a map and does a non-blocking send on
    each one-slot channel (a signal already pending stands for both). It
    parses nothing and judges nothing: the work under the ledger's lock is
    one map lookup and at most `MaxDecisionStreams` sends.
  - **Delivered**: the stream's own goroutine, woken by its signal, reads
    the record (outside the ledger's lock), judges `admission` over that
    read, and writes from that same read: `{id, state, rev}` when
    admitted and its `rev` is past the last written; one last
    `event: decision` with `{id, rev}` alone and the end when `None`; the
    end after writing a final state. So a change queued before a
    take-back and delivered after it is judged by the record as the
    take-back left it: nothing of it is written but that last `{id,
    rev}`. `decisionStreamBeforeRead` (a test hook, nil in the product)
    holds a delivery between its signal and its read.
  - **Kept**: pings as `GET /api/events` sends them. `principalWatch`
    watches the token with the grant the stream opened with; when it ends
    the stream, it cuts a write under way and ends the context as
    `handleEvents` does (a write deadline already passed, set while the
    handler runs, then the cancel). No frame is written for that end: the
    page learns it from its next request's `401`.
  - Nothing else is ever written to it: its one producer is the record of
    its own decision.
- `inbox_decisions.go`: `answererOf` takes the record's policy too and
  names the grant entry ("the operator, or anyone holding the decide
  grant"); the inbox stays the admin's (`inboxOnly`, unchanged).
- `guide.go`: `decision_route`'s description names the grant
  destination, the take-back and `no_identity` (its schema is
  `Destination`'s, which gains `grant`).
- `brakeman_act_decision.go`: a Brakeman turn's `route_decision` act
  refuses `to: grant` (`400 invalid`) and a route to the operator of a
  decision whose policy holds a grant entry (`409 stale_target`, "dr-7
  is given to the decide grant: the operator may route it"), as it
  refuses to change whom a decision is for.
- `server.go`: `GET /api/me` and the stream registered; the three
  decision routes registered through `deciding`.

### `web/app`

Every site that holds or sends a credential, and what it does while a
pass is held:

| Site | Today | With this row |
|---|---|---|
| `plugins/token-fragment.client.ts` | takes `#token=` into the workbench store | takes `#token=` as today and `#pass=` into the pass store, both dropped from the address bar first |
| `composables/useDecide.ts` (new) | | the only code that sends the pass, in the `Authorization` header, to the four routes and nowhere else |
| `app.vue` (`attention.start`, `identity.ensureDefault`) | started once a workbench token is held | unchanged: never started on a pass |
| `layouts/default.vue` (`useAttention`, `useInbox`, `useBrakeman`, the token dialog) | the workbench's frame | unchanged; never drawn while a pass is held without a workbench token (the middleware, below) |
| `composables/useApi.ts` | the workbench token in the header; a `401` asks for it | unchanged: never carries the pass, never touches the pass store |
| `composables/useInbox.ts` (`ready: () => !!admin.token.value`), `useDecisions.ts` (no reads without the workbench token) | gated on the workbench token | unchanged; mounted only by the workbench's pages, never on the decide page |
| `pages/decisions.vue` (`useBrakeman`, `useInboxActs`'s `trackNow`, `useTaskCommands`) | the operator's decision page | unchanged; never mounted while a pass is held without a workbench token |
| `useTerminalTransport.ts:28,41`, `pages/yard.vue:199`, `pages/sessions/[id].vue:353` | the workbench token in a URL (`?token=`, `&token=`) | unchanged: they read `admin.token.value` and never the pass store |

- `utils/identityPass.ts` (new): `MAX_PASS = 2048`; `passFromFragment(
  hash)`, the fragment key `pass`, `^[A-Za-z0-9._-]{1,2048}$`, '' for
  anything else; `takeCredentials(href)`, which answers `{token, pass,
  href}`, the URL with both keys taken out of its fragment (the rest
  kept) whether or not their values were taken; `decideRedirect(path,
  hash, hasToken, hasPass)`, the middleware's rule as a pure function.
  `tokenFromFragment` (`utils/desktop.ts`) keeps its meaning and its 16
  to 256 characters.
- `composables/useIdentityPass.ts` (new): the pass store,
  `sessionStorage['conductor.identityPass']` (the tab's; in memory when
  storage is refused): `set` (a new pass replaces the old one), `end()`,
  which empties the store and marks the tab's state `ended`. It never
  reads or writes `conductor.workbenchToken`. A pass is held no longer
  than its tab, and not past the first `401` it meets.
- `plugins/token-fragment.client.ts`: `takeCredentials(location.href)`,
  then `history.replaceState` with the URL it answers, before anything
  else; then the token into the workbench store as today, and the pass
  into the pass store. A refused value is dropped from the address bar
  and stored nowhere.
- `middleware/decide.global.ts` (new), synchronous over the stores, and
  on the first navigation over `to.hash` too (`takeCredentials`), so it
  judges right whether it runs before or after the plugin: a workbench
  token held, `/decide/<did>` goes to the operator's
  `/decisions?item=decision:<did>`; no workbench token and a pass held,
  every path but `/decide/**` goes to `/decide`. Nothing of the
  workbench's layout or pages is set up before it has run, and it waits
  on no request.
- `composables/useDecide.ts` (new): the decide page's client and state,
  page-owned:
  - `me()`, `read(did)`, `respond(did)`, `stream(did)`, each with
    `Authorization: Bearer <pass>`; a `401` calls the pass store's
    `end()`; a `404` sets `gone` and leaves the pass; any other refusal
    is its code's words.
  - `view`, the last decide view read; a refused read never clears it,
    so the card stays drawn.
  - `draft`, `{choice?, text, rationale, responseId}`, its `responseId`
    minted with the draft: no refusal clears it, only a reply whose
    receipt is the draft's (`201`, or `200` repeat).
  - `sent`: `unknown` when a send got no reply (the network, a `5xx`),
    until a read shows a newer receipt in `own` or the same draft sent
    again (the same `responseId`) is answered, which DR1 answers from its
    receipt (`200`, repeat) and never records twice.
  - On a stream frame, one read; on the stream's end, one read, and the
    stream opened again when that read is admitted and not final, after
    1 s, doubling to 30 s while opens fail.
  - `whenIdle()`, the promise of the work under way, the vitest's seam.
- `pages/decide/[did].vue` and `pages/decide/index.vue` (new), layout
  `bare` (`definePageMeta`), so no sidebar, palette, Needs you pill or
  token dialog is drawn. The header "Signed in as <name>" (`me()`); a
  pass whose grants hold `admin` or lack `decide`: "This page answers a
  decision routed to you; this pass is not for it." The card from
  `view`: question, context, the options with their tradeoffs and the
  recommendation, uncertainty and what it authorizes; the answer as its
  type asks (Nuxt UI's `URadioGroup`, `UTextarea`, `UButton`), not the
  operator's `DecisionRespond`, which carries Comment and Ask for more.
  The words, each a `UAlert` above the card, the draft left as it is:
  - `401`: "Your access to this server has ended. Open it again from
    your workspace page." Every send disabled.
  - `404` after a view was drawn: "dr-7 is no longer open to you."
  - `unknown`: "Not known whether your answer to dr-7 was recorded. Send
    it again: the same answer is never recorded twice."
  - `recorded`: "Recorded: your answer to dr-7."; `applied`: "Your answer
    to dr-7 was applied."; `not_used`: "Your answer to dr-7 could not be
    confirmed, so it was not used. The operator will answer it.";
    `answered_elsewhere`: "dr-7 was answered before your answer reached
    it."; `closed`: "dr-7 was closed before your answer."
  - No pass (`/decide` alone, or a reload after the end): the `401`
    words.
- `utils/decisions.ts`: the policy's grant entry in the record's types,
  and "who may answer" naming it ("the operator, or anyone holding the
  decide grant").
- `utils/protocol.ts`: `GRANT_DECIDE`, `MeReply` with `MAX_ME_REPLY`,
  `DecideView`, `DecideReceipt`, `DecideReply`, `DecideRefusal` with
  their bounds, `DecisionStreamEvent` with `MAX_DECISION_STREAM_EVENT`,
  the stream caps, the `outcome` codes, the route's `grant` destination,
  `no_identity` and `stream_limit`, beside the existing decision rows.

### `internal/proto`, `web/app/utils/protocol.ts`, `docs/protocol.md`

The triple together: the types and bounds above in `internal/proto`
(`decide.go`), their mirror in `protocol.ts`, and in `docs/protocol.md`:

- the HTTP API's opening paragraph: the decide grant, the four routes
  that admit it, every other route answering it as no credential
  (`401`), the recheck watching it on the decision stream;
- the Decisions table: a `GET /api/me` row; `GET /api/decisions/{did}`'s
  decide view (its fields, `MaxDecideView`, the `404` for a decision not
  admitted); the respond row's decide reply and refusal
  (`MaxDecideReply`, `decideWords`); a `GET /api/decisions/{did}/events`
  row (its frames, `MaxDecisionStreamEvent`, the caps and `429
  stream_limit`, its order and its end); the route row's `grant`
  destination, the take-back and `409 no_identity`. The shipped bounds
  are kept: the record's read at most 2 MiB (`maxDecisionReply`), a
  response's body at most 32 KiB (`maxDecisionRespondBody`);
- the `GET /api/inbox/item` row: its answerer words naming the grant
  entry. The inbox's three rows otherwise unchanged: they stay `admin`,
  a decide principal `401` as no credential;
- the fragment key `pass` beside `token` (2 KiB of `[A-Za-z0-9._-]`,
  dropped from the address bar, kept for the tab, never sent in a URL).

### Docs

`docs/features.md`: the row's entry. `docs/repository-map.md`: the
`internal/api/decisions.go` row names the decide view, the decision
stream and `admission`; the `ext` row names three grants.

## Events, orderings and invariants

**The pass.** The plugin reads the fragment, replaces the address bar's
URL with both keys taken out, and only then stores what it took; no
request is made before it runs (it is a client plugin, ahead of the first
page's setup), and the middleware judges from the fragment itself if it
runs first. The pass leaves the browser only in an `Authorization` header
`useDecide` sets; the server reads no pass from a query (the stream and
every decide route call `presentedToken(r, false)`). A stored workbench
token is never written by this path, and while one is stored the pass is
kept and not sent. The pass is never logged, by the server (the guard
logs a provider's name, never a token) or by the page.

**The route to the grant and the take-back.** One transaction each, in
DR1's order for an operation: the repeat look by `(decision, actor,
opId)` first (its receipt, `repeat: true`); then, for the route to the
grant, the state (`open`), `expectedRev` and the held run; then the
policy rewritten, the `routed` row, `rev` + 1. The take-back checks no
state, rev or hold. The registration check (`no_identity`) is made before
the transaction and writes nothing. After the commit `decisionTold` sends
DR1's `decision` event on `GET /api/events`, the notification, the
router's tell and the decision streams' signal, in that order, under the
ledger's lock.

**A decide principal's response.** The route's read and `admission`
(`404` when `None`), then DR1's transaction, whose policy check is
`Admits` over the policy it reads (a take-back in between:
`not_approver`, kept); an identity's response to a run not held is
`not_held`, as today. After the commit, `recheckResponse` asks the
provider through the guard (bounded by `ext.IdentityWait`) and calls
`ConfirmAuthority` with `admitted` true only for the same subject whose
`admission` of the record as it is then is not `None`. A provider that
fails, is late or is away has not said yes: the decision is superseded,
the replacement opens for the operator alone, and the person's stream
delivers the superseded state, whose view says `not_used`.

**The stream.** Subscribe, then read, then write the first frame: a
change committed after the read signals the channel, so nothing is lost.
Every frame is written from the read made for it, after the commit it
follows, judged against the policy that read found: the version judged
is the version written, and a frame for a change made before a take-back
but delivered after it is never written. The ledger's lock covers the
signal alone.

**What a pass meets when it ends.** Every request asks the provider
(C2's rule), so the first request after the end is `401`; the stream ends
at its next recheck. A pass a provider no longer admits is never cached
by the server.

Invariants:

- **I1, one judgement.** Every decision of whether a principal may read,
  hear of or answer a decision is `crew.Admission` (with `crew.Admits`
  inside it) over one record: the read, the respond route's check, the
  response's transaction (`Admits`), `recheckResponse` and the stream's
  open and each delivery. `TestAdmitsIsTheOneCheck` holds them to one
  table.
- **I2, the four routes.** A decide-only principal is answered by every
  route outside the four exactly as a request with no credential is, and
  is written no byte of a decision `Admission` answers `None` for, but
  for the one `{id, rev}` of the change that ended its admission.
- **I3, the table.** What a decide principal reads of a decision is
  `proto.DecideView`, every field named in The decide view's table, built
  from the one record its route read.
- **I4, the pass in no URL.** The pass is read only by the plugin and
  `useDecide`, and sent only in an `Authorization` header; the server
  reads it from no query. (The workbench token keeps the URLs it is in
  today: the WebSockets' `?token=` and the raw file links.)
- **I5, the workbench token kept.** The pass path never writes
  `conductor.workbenchToken`.
- **I6, no schema change.** The ledger's tables and version are
  unchanged; an older binary fails closed on what this one writes.

## Out of scope

- Whatever issues a pass, how long it lasts, and how a person is sent to
  the decide page: the module that registers a provider; this repository
  only asks the provider about a token.
- The decide grant on the inbox (`GET /api/inbox`, `GET
  /api/inbox/item`, `GET /api/inbox/track`), its counts and pages, a list
  of the decisions that admit a principal, a stream across decisions,
  Brakeman's card, and evidence refs resolved for a decide principal: a
  person answers the one decision its address names (Owner decisions,
  1). A later row that wants a list plans it with the grant as a
  condition of its query.
- A pass as the operator's credential on the workbench's screens: they
  keep the workbench token.
- Any grant but `decide` in a route (a route to `host` or `admin` is
  `invalid`), a subject entry in a policy (SW1's, which adds its rule to
  `crew.Admits`), quorum, delegation and escalation.
- Comments, requests for more information, cancels and routes by a
  decide principal: the operator's.
- Decision links and their lane (MB0's PR-5 to PR-7): unchanged; a link
  holder is no principal of this grant.
- The host token, the uplink's credential, host connection rechecks and
  the provider's detail: #775 (`docs/plans/identity-uplink.md`).
- A draft kept across a new Open: the draft stays in the page (Owner
  decisions, 12).

## Tests per surface

Every timed thing is driven through a seam, never waited for: the
recheck through `extAfterOr`'s seam (`s.extAfter`), a delivery through
`decisionStreamBeforeRead`, the provider's answer through `exttest`'s
`FakeIdentity` (`Add`, `Remove`, `SetErr`) and the guard's gate, the page
through `useDecide().whenIdle()` and a fake fetch whose replies the test
releases.

Go race (`go test -race -count=1 -timeout 60m ./...`, with `make lint`,
`make lint-static`, `make vuln`):

- `ext`: `TestGrantDecideValid` (`decide` valid, alone and beside
  `admin` and `host`; given twice refused; an unknown grant's words name
  the three).
- `internal/proto`: `TestDecideBounds` (a `MeReply`, a `DecideView`, a
  `DecideReply`, a `DecideRefusal` and a `DecisionStreamEvent`, each with
  every field at its bound and of characters `encoding/json` escapes to
  six bytes, fits its constant).
- `internal/crew`: `TestAdmission` (the table: admin, decide, both,
  none, a router, against a policy with and without the entry, an
  unreadable one and an unknown kind, in each state, with and without a
  response of its own); `TestRouteToGrantAddsTheEntry` (open, at its
  rev, a `routed` row and `rev` + 1; answered, closed, a stale rev and a
  run not held refused with no row; a second route writes its row and
  leaves the policy and `rev`); `TestOperatorRouteTakesTheGrantBack`
  (open held, open not held, answered and applied: the entry out, a row,
  `rev` + 1, at a stale `expectedRev` too); `TestDecideResponseUnderThePolicy`
  (a decide authority accepted under a policy with the entry,
  `not_approver` without it, and after a take-back committed between the
  route's read and the transaction).
- `internal/api`, in `decide_test.go`, with `exttest.NewFakeIdentity`
  registered:
  - `TestDecideGrantAdmitsDecisionRoutesOnly`: every pattern the package
    registers (found by scanning its non-test sources for `HandleFunc`
    patterns, the constants among them, so a route added later is walked
    too) asked with a decide-only pass and with no credential, the rate
    limit raised for both walks: the four routes answer the decide
    principal; every other answers it exactly as it answers no
    credential (status and body), `GET /api/events`, the inbox's three
    routes and `GET /api/decisions` among them.
  - `TestMeAnswersEveryPrincipal`: workbench, host token, link, a
    provider's principal with each grant, none (`401`); a subject and a
    name of characters that escape to six bytes fit `MaxMeReply`.
  - `TestRouteToGrantNeedsAProvider`: no provider registered, `409
    no_identity`, no row; registered, the entry and the row.
  - `TestDecideReadCarriesNothingElse`: a decision whose request cites a
    ticket, a session and a file, with an operator's comment in its
    history, another subject's refused response, a Brakeman card on its
    subject and an HM1 label, each carrying a marker string: read as a
    decide principal, open and then answered, the reply holds none of the
    markers, nor the run's id, nor the other subject.
  - `TestDecideViewIsTheTable`: walks `proto.DecideView`'s and
    `DecideReceipt`'s fields by reflection against the table's names, so
    a field added without the table fails.
  - `TestDecideReadJudgesTheRowItReturns`: the read held after its read
    of the record (a hook), the grant taken back, the read released: the
    reply is the view of the rev it judged; the next read is `404`.
  - `TestDecideAdmissionEnds`: answered by another, a holder with no
    response of its own `404`, the responder the outcome view alone;
    after a take-back of an answered decision and of an open one whose
    run is not held, both `404`.
  - `TestDecideRefusalsNameNobody`: `decision_answered`, `stale_decision`
    and `not_approver` answered to a decide principal carry
    `decideWords` and its own receipt reduced, never the other
    responder's name or subject.
  - `TestConfirmAuthorityAdmitsDecide`: the same subject with the decide
    grant admitted for a decision with the entry, refused for one without
    it (a take-back before the recheck: superseded, the view `not_used`),
    another subject refused, a provider late through the guard's gate:
    superseded.
  - `TestPrincipalWatchWatchesDecide`: a decide principal's stream ended
    when the recheck answers no decide grant or another subject, kept for
    an error or a late answer, the recheck driven through `extAfterOr`'s
    seam; the reconnect `401`.
  - `TestDecisionStreamCarriesOnlyAdmittedDecisions`: sessions started,
    activity, chat and other decisions changed meanwhile, other
    decisions routed to the grant among them: the stream of dr-7 writes
    dr-7's frames alone, each at most `MaxDecisionStreamEvent`.
  - `TestDecisionStreamPolicyChangedDuringDelivery`: a delivery held at
    `decisionStreamBeforeRead`, an operator's comment committed (a
    change), then the take-back, then the hold released: one `{id, rev}`
    of the take-back's rev, no frame of the comment's rev, the end.
  - `TestDecisionStreamAtItsCap`: `MaxDecisionStreamsPerSubject` streams
    of one subject open, the next `429 stream_limit` before any read;
    another subject's opens until `MaxDecisionStreams`; one ended frees
    its place.
  - `TestAdmitsIsTheOneCheck`: one table of records (policy, state, own
    response) against admin, decide, both, none and a router: the read's
    answer, the respond route's check, the stream's open and
    `recheckResponse`'s `admitted` agree with `crew.Admission` on every
    row.

Vitest (`npm --prefix web test`, with `npm --prefix web run typecheck`):

- `utils/identityPass.test.ts`: a 2,048-character pass with dots taken,
  2,049 refused, a character outside the set refused; both keys dropped
  from the fragment, the rest kept, a refused value dropped too;
  `decideRedirect`'s rules (a workbench token held, a pass held, both,
  neither; the first navigation's fragment).
- `composables/useIdentityPass.test.ts`: the pass in `sessionStorage`,
  never `localStorage`; a stored workbench token never replaced; a new
  pass replaces the old; after `end()` the store is empty; storage
  refused, in memory.
- `composables/useDecide.test.ts` (a fake fetch whose replies the test
  releases, `whenIdle()`): every request to one of the four routes, the
  pass in its header and in no URL; a `401` ends the pass, a `404` sets
  `gone` and keeps it; the draft and its `responseId` kept through a
  refused read, a refused send and a `401`; a send with no reply is
  `unknown` until a read shows its receipt, or the same `responseId`
  sent again is answered `200`; each `outcome` code's words; a stream
  frame makes one read, a stream's end one read and a reopen.
- `utils/passSites.test.ts`: only `plugins/token-fragment.client.ts` and
  `composables/useDecide.ts` import the pass store (the app's sources
  read through `import.meta.glob`).

Playwright (`make test-e2e`, after `npm --prefix web run typecheck:e2e`),
`decide-only.spec.ts`, on the shipped build `make test-e2e` already
builds. That build registers no provider, and nothing in it may be
switched on to admit a pass (articles 1 to 3), so:

- **Against the server as shipped**: `/decide/dr-1#pass=<a pass with
  dots>` in a context with no workbench token: the address bar without
  the pass, `GET /api/me` answered `401` by the real server, the ended
  words, the store empty; no request URL holds the pass and no request
  goes to `/api/events`.
- **The page against the four routes stubbed in the browser**
  (`page.route`, as `brakeman.spec.ts` and `decisions-gate.spec.ts` stub
  replies): the decide page alone ("Signed in as Ana", no sidebar, the
  token dialog never opened), every request one of the four with the
  pass in its header, no response `401` until the spec answers one; an
  answer recorded; a send whose reply the stub aborts, "Not known
  whether your answer to dr-7 was recorded", then the same answer sent
  again answered from its receipt; the stub's `401` to a send: the ended
  words with the draft still in its field; `not_used` drawn from a
  stubbed view.
- **A workbench token held**: `/decide/dr-7#pass=…` keeps
  `conductor.workbenchToken`, sends the pass nowhere and opens the
  operator's `/decisions?item=decision:dr-7`.

Not covered end to end, and why: a pass the real server admits, in a
browser. It needs a provider registered in the server the browser talks
to; the shipped build has none, and a test-only binary that registers
`exttest`'s fake would be machinery for one spec. Each half is covered
on its own against the one contract, `internal/proto`'s types and
`protocol.ts`'s mirror: the server half by the Go tests above on the real
handlers with `FakeIdentity`, the page half by the stubbed spec and the
vitest on the real composables. A live check with a real provider is the
provider module's.

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
- [x] Article 3: no flag and no test-only binary; the grant exists only
  in a provider's answer.
- [x] Article 4: stop, inspect, revoke, export and approvals stay the
  operator's; the operator takes the grant back in every state, and a
  revoked pass loses its requests at once and its stream within the
  recheck.

## The PRs

The one PR is issue #776's: one branch `issue-776`, cut from `main` once
#775's PR has merged, a draft PR at its first commit, the checks of every
surface before each push, its body ticking the open-core checklist. It
leaves `main` working and green on its own and carries the
`docs/features.md` entry.

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with |
|---|---|---|---|---|---|---|
| PR-1 (#776, ~700 + 800 lines) | ext, api, web: a decide grant and a pass taken from the fragment | `ext.GrantDecide`; `proto`'s decide types and bounds; `crew.Admits`, `crew.Admission`, the policy check, the route to the grant and the take-back; `GET /api/me`, `deciding`, the decide view and words, `GET /api/decisions/{did}/events` and its hub, `admission`, `authorityOf`, `recheckResponse`, `no_identity`, `answererOf`, Brakeman's refusals; the pass parser, its store, the plugin, the middleware, `useDecide`, the decide page; `docs/protocol.md`, `protocol.ts`, `docs/features.md`, `docs/repository-map.md` | `ext`, `internal/proto`, `internal/crew`, `internal/api`, `web/app`, `web/e2e`, `docs` | every test above | #775 (its PR merged); DR1 and MB0 on `main` | none: #775 shares `ext`, `internal/api`, `web/app` and `docs` |

## Owner decisions

Dated 2026-11-10. Each is a default for a choice the issue's words leave
open, or a narrowing of them, the fail-closed one, which the owner may
overturn.

1. **Four routes, one decision.** The grant reaches `GET /api/me`, one
   decision's read, its respond route and its stream, and nothing else.
   The issue's "decision page's read model filtered to the decisions
   that admit it" and its stream across decisions are not built: a person
   answers the decision its address names (`/decide/<did>`), and a read
   computed over every record and narrowed afterwards is where a grant
   leaks (its order, its bound, its count, its errors, the records it
   links). The issue's stream becomes one decision's,
   `GET /api/decisions/{did}/events`, its tests kept.
2. **The decide view is a table.** A decide principal reads the
   request's own words, its state and its own receipts, never the
   record whole, the refs, the history or another's receipt (The decide
   view).
3. **Admission ends with the answer.** Once a decision is answered, a
   holder of the grant reads only the outcome of its own answer, and one
   with no answer of its own reads nothing.
4. **The take-back works in every state.** A route to the operator of a
   decision whose policy holds a grant entry removes it, open, answered
   or final, held run or not, at any `expectedRev`: it only narrows.
5. **"A new revision" is the decision's `rev`.** The route to the grant
   bumps the record's `rev` and leaves the policy's revision `local/1`:
   `stale_policy` refuses any revision but the one in force, so a new
   policy revision would refuse every response, while `rev` + 1 makes a
   card read before the route `stale_decision`.
6. **Given only while open, at its rev, through the held run.** The
   route to the grant follows HM1's route to a person.
7. **A replacement is the operator's.** The fresh decision that replaces
   an identity's answer not rechecked opens under the operator's policy
   alone; the operator routes it again if a holder of the grant should
   answer it, and the person's page says `not_used`.
8. **Brakeman neither gives nor takes the grant.** Its act refuses `to:
   grant` and a route to the operator of a decision given to the grant.
9. **Not admitted is not found.** A decide principal's read, response or
   stream of a decision `Admission` answers `None` for is `404
   not_found`, with the body a decision that does not exist gets.
10. **`GET /api/me` for the chain's tokens**: the workbench token
    `admin` and `host`, a host token `host`, a link none.
11. **The stream's end writes no frame.** A recheck that ends it cuts it
    as `GET /api/events` is cut; the page learns it from its next
    request's `401`. A take-back or an answer ends it with one last
    `{id, rev}`.
12. **The pass lives in the tab; the draft in the page.** The pass in
    `sessionStorage`, emptied at its first `401`; the draft and its
    `responseId` in the page's state, never in the browser's storage: a
    new Open reloads the page, which reads the outcome of an answer sent
    before.
13. **The workbench token wins.** With one stored, the pass is kept and
    not sent, and the decide address opens the operator's page.
14. **On a switchyard the decide routes are `403 switchyard`**, as
    `launching`'s are: decisions are answered where the work runs.
15. **Playwright on the shipped build.** No test-only binary registers a
    fake provider; the page is proved against the real server's refusal
    and against stubbed replies, the server by Go tests with `exttest`.

## Acceptance

The acceptance boundary is observed when the PR's checks pass in CI and
locally: `TestDecideGrantAdmitsDecisionRoutesOnly` and the other
`internal/api` tests show a principal with only the decide grant
answering the decision it may through the four routes and answered as no
credential everywhere else, `GET /api/events` among them;
`TestDecideReadCarriesNothingElse` and
`TestDecisionStreamCarriesOnlyAdmittedDecisions` show it reading and
hearing of nothing else; the vitest files and `decide-only.spec.ts` show
the pass taken from the fragment, dropped from the address bar, kept for
the tab and never sent in a URL. Done is the PR merged with those checks
green. Nothing needs the owner but the merge; a live check with a real
identity provider is the provider module's.

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
> (Routing, below), admits anyone an identity provider gives
> `ext.GrantDecide` (`docs/plans/decide-grant.md`). One function,
> `crew.Admits`, judges every entry, for the response's transaction, the
> response's recheck and every read (`crew.Admission`). The record keeps
> it so that a response and an application check the policy the decision
> was opened under against the one in force, and so that a later policy
> is a later revision of the same field.

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
> policy, as it is at the recheck, admits (the admin grant; or the decide
> grant, for a decision whose `authority` holds `{kind: "grant", grant:
> "decide"}`: `crew.Admission`, `docs/plans/decide-grant.md`): the
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
> HM1 adds a person's label. It changes no state, but for the two cases
> below. A route to `{kind: "grant", grant: "decide"}`
> (`docs/plans/decide-grant.md`) adds that entry to the decision's policy
> `authority`, which then admits anyone holding the decide grant, not one
> person: refused `409 no_identity` when no identity provider is
> registered, and taken only while the decision is open, at the
> `expectedRev` its body carries and through the run that holds it, as a
> route to a person is (HM1, below); in one transaction it rewrites the
> policy, writes the `routed` row and bumps `rev`, so a card read before
> it is `stale_decision`. A route to the operator of a decision whose
> `authority` holds a grant entry takes the entries out (the policy back
> to `{kind: "operator"}` alone, a `routed` row, `rev` + 1) in every
> state, through the held run or the ledger alone, whatever `expectedRev`
> it names: it narrows who may read the decision and never changes an
> answer already confirmed.

The route table's rows (`internal/api`: the routes; the other rows are
unchanged). The bounds are the shipped ones (`maxDecisionReply`,
`maxDecisionRespondBody`, `docs/protocol.md`'s Decisions table):

| Route | Who | Body → reply |
|---|---|---|
| `GET /api/decisions/{did}` | admin, or a principal with the decide grant the decision admits (`crew.Admission`; `404 not_found` for one it does not) | the admin: `{decision, refs}`, the record whole and its evidence refs' resolutions, at most 2 MiB (`maxDecisionReply`: the record's parts at their bounds, its 32 receipts at `ledger.MaxDecisionReceipt`, 32 KiB each, and its refs at `maxDecisionRef`); a decide principal: the decide view alone (`proto.DecideView`, at most 32 KiB, `docs/plans/decide-grant.md`) |
| `POST /api/decisions/{did}/respond` | admin, or a principal with the decide grant the decision admits (`404 not_found` for one it does not) | `{responseId, choice?, text?, rationale?, expectedRev, byName?}` (at most 32 KiB, `maxDecisionRespondBody`: `text` at most 4,096 bytes and `rationale` 2 KiB, each as `encoding/json` sends it, escapes and all) → `201 {receipt, decision}`; `200` repeat; `409 decision_answered`, `decision_closed`, `stale_decision`, `stale_policy`, `response_conflict`, `not_held`; `403 not_approver`; `503 decision_moved`; a decide principal's reply its own receipt and the decide view (`proto.DecideReply`), a refusal's words its code's (`decideWords`) |
| `POST /api/decisions/{did}/route` | admin | `{opId, to: {kind: "operator"} \| {kind: "grant", grant: "decide"}, note?, expectedRev}` → `{receipt, decision}`; `200` the same for a repeat; `409 op_conflict`; `409 no_identity` (a route to the grant on a build with no identity provider registered); `429 decision_limit` |

The paragraph after the table:

> A share link's controller, an agent's token and a Brakeman turn's token
> are refused every admin route (`requireAdmin`'s `401 unauthorized`), so
> the free build's one approver is the operator; `403 not_approver` is the
> policy's refusal of a principal that got past it without the authority
> `local/1` names (a router's answer, below). A principal holding the
> decide grant and not the admin grant, which only a build with an
> identity provider registered can present, is admitted by `GET /api/me`
> and by the read, the response and the stream of a decision it is
> admitted to (`docs/plans/decide-grant.md`), and answered by every other
> route as a request with no credential is (`401 unauthorized`).
