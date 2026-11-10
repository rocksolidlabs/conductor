# A decide grant and a pass taken from the fragment (issue #776)

Today an identity provider's principal is the operator (`ext.GrantAdmin`)
or a host (`ext.GrantHost`), nothing between. A person who should answer
one decision and see nothing else has no grant to hold, so a provider
could only make that person the operator. This plan adds the grant
between them: `ext.GrantDecide`, which answers a durable decision (DR1,
`docs/plans/decisions.md`) the operator has routed to it, and reads
nothing else; the decide page, one decision's page drawn for such a
principal alone; DR1's route operation taught to give a decision to the
grant, as one more of the route targets HM1 writes, and to take it back;
and the web app's way to take such a principal's credential, a pass,
from the URL fragment without ever sending it in a URL.

It is the row of issue #776, written from the issue's words against
`main` at `175e0be5` (2026-11-10) after reading `ext/identity.go` and
`ext/valid.go`, `ext/exttest/fakes.go`, `internal/api` (`auth.go`,
`identity.go`, `extensions.go`, `decisions.go`, `decision_refs.go`,
`events.go`, `inbox.go`, `inbox_decisions.go`,
`brakeman_act_decision.go`, `lane_decisions.go`, `server.go`),
`internal/crew`'s `decisions.go`, `decisions_wait.go`, `decisions_for.go`
and `ledger/decisions.go`, `internal/proto/decisions.go`,
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
`authorityOf`, `ConfirmAuthority`), HM1 (the route to a person: the
request's `for`, written by `ledger.Tx.RouteFor`, which the route to
the grant writes too) and MB0's run rows (the operator's decision page).
Not on `main`: issue #775, the stream's other open-source row (the host token
and the uplink's credential, `docs/plans/identity-uplink.md`). Both rows
change `ext/identity.go`, `internal/api`'s identity checks,
`docs/protocol.md` and `web/app/utils/protocol.ts`, so this row is built
after #775 merges, on those files as it leaves them. That plan owns the
connection rechecks (`principalWatch`, its `watchedToken`); this row
uses none of them, since a decide principal opens no live connection
(Owner decisions, 16).

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
   takes the grant back from an open decision at any time, held run or
   not, and one operation is always kept for it (the take-back, below);
   once the decision is answered no holder but the answerer reads it, and
   the operator's cancel and the provider's revocation still end it. A
   revoked pass is refused at its next request (every request asks the
   provider), and an open page within one read interval and one read.
5. **Dependencies one way.** No import changes; `ext` stays standard
   library only.
6. **No feature build tag.** None added; no test-only binary either
   (Tests, Playwright).
7. **Free default is today's main.** With no provider every principal is
   one of today's; the decide paths are reached only by a provider's
   principal.
8. **Provider outage is the free default.** A provider that fails, is
   late or is away admits nothing new (the guard's answer, an unknown
   token: `401`); an answer not rechecked in time is superseded and the
   operator answers again (DR1's rule, unchanged).
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
   then on dr-7's route target (the request's `for`, where HM1's route
   to a person writes) is the decide grant, which admits **anyone** the
   provider gives the decide grant, not one person: the route's words and
   "who may answer" say so. Its `rev` moves, and its history says who
   routed it. The policy column is not written.
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
   view (a fixed projection, The decide view, below), and answers
   through `POST /api/decisions/dr-7/respond`. It reads dr-7 again every
   10 s while the tab is shown, and at once when it is shown again; it
   opens no live connection. Those two and `GET /api/me` are the grant's
   whole surface.
4. Ana's answer is recorded under `user:<subject>` with the authority
   `{grant: decide, via: identity}`, rechecked with the provider after
   its commit, and applied by Yardmaster as any identity's answer is.
   Once dr-7 is answered, Ana reads only the outcome of her own answer,
   and another holder of the grant reads nothing of it.
5. When the provider stops admitting the pass (its end, or a revocation),
   the next request is `401`, at most one interval and one read after it
   (Admission and its end); the page says "Your access to this server
   has ended. Open it again from your workspace page.", clears the pass,
   and keeps the answer Ana had not sent yet. A new Open brings a new
   pass.

A browser that already holds a workbench token for that origin keeps it:
the pass never replaces it, and the decide page's address then opens the
operator's page for dr-7 instead.

## The decide principal's surface

A closed list. A principal holding `decide` and not `admin` is admitted
by these three routes and by nothing else; every other route answers it
exactly as it answers a request with no credential (`401` under the rate
limit for every route that needs one, `GET /api/events`, the inbox's
three routes, `GET /api/decisions` and the operator's operations among
them).

| Route | What it answers a decide principal | Why the person needs it |
|---|---|---|
| `GET /api/me` | `{subject, name, grants}` | the page's "Signed in as", and whether this pass is a decide pass |
| `GET /api/decisions/{did}` | the decide view of one decision it is admitted to (below); `404 not_found` otherwise | the question it was routed, and later the outcome of its answer |
| `POST /api/decisions/{did}/respond` | DR1's response, judged by the decision's entries (`crew.Admits`); its reply the decide view and its own receipt | the answer itself |

No live connection is among them: the decide page reads its one decision
again on an interval and when shown again, and each read is judged
fresh (The changes, `web/app`; Owner decisions, 16). So no stream, no
signal under the ledger's lock and no delivery is planned, and
`GET /api/events` stays `401` to the grant.

Asked of each further route the first draft gave the grant, whether the
issue's outcome ("a person who should answer one decision, and see
nothing else") needs it: the inbox list and item (`GET /api/inbox`,
`GET /api/inbox/item`), their counts and pages, the track route
(`GET /api/inbox/track`), a stream across decisions or of one,
Brakeman's card on an item, and evidence refs resolved through the
operator's resolver. It does not: a person sent to one decision's
address needs none of them, and each is a read computed over records the
person may not see. None is
given to the grant (Out of scope; Owner decisions, 1). With no list,
there is no count, page or cursor to compute over admitted rows.

## The decide view

What `GET /api/decisions/{did}` answers a decide principal, what the
respond route answers it beside its receipt, and nothing more: one Go
type, `proto.DecideView`, built in one function, `decideView(rec
ledger.DecisionRecord, actor string, a crew.Admission)
proto.DecideView`, from the one record the route read whole
(`s.ledger.Decision`, as the decision lane's `laneRecord` reads one;
The changes, `internal/api`). Nothing in it is resolved through another
record: `decisionRefs` and the inbox's item builders are never called
for a decide principal.

| Field | From | Why the answerer needs it |
|---|---|---|
| `id` | the record's id | names the decision in the respond route |
| `rev` | the record's `rev` | the `expectedRev` of the answer |
| `state` | the record's state (while `open` only) | whether it can still be answered |
| `type` | the record's type | which form the answer takes (an option, approve or reject, a line) |
| `question` | the request's `question` | what is asked |
| `context` | the request's `context` | the asker's words for why |
| `options[{label, tradeoffs?}]` | the labels `crew.DecisionOptions(type, request)` answers, the one place the engine makes them (`optionsOf`, which `answerFits` counts: an assignment's fixed "Done" and "Not done" at 0 and 1, else the request's options in order), each with the request's `tradeoffs` at the same index (none for an assignment) | what to choose from, at the index the answer's `choice` names |
| `recommendation?` | the request's `recommendation` | the option the asker recommends |
| `uncertainty?` | the request's `uncertainty` | what the asker is unsure of |
| `authorizes?` | the request's `authorizes` | what an answer lets happen |
| `createdAt` | the record's `createdAt` | when it was asked |
| `own[{k, responseId, status, code?, rev, at, choice?}]` | the record's response rows whose actor is `user:<this subject>`, the newest 8, each with its row's `responseId` (the ledger's `ResponseRow.ResponseID`, from the same read) | what became of its own answers, each matched to the send that made it; never another responder's |
| `outcome?` | `crew.OutcomeOf(rec, actor)`, over the record whole (below) | the outcome of its own answer, which the page says in words |

While the decision is `open`, the view holds every field. Once it is
`answered` or final, it holds `id`, `rev`, `own` and `outcome` alone: no
`state`, since which final state it is would say why, and the outcome
says what the person needs.

`outcome` is one of four words, computed at the read from the whole
record (every response row, the accepted response, the application
receipt), never from `own`'s eight or from the state alone, and never
naming a reason:

| `outcome` | When | The page says |
|---|---|---|
| `applied` | the record's application receipt names its own accepted response (`Application.Response` is that row's `k`), whatever the state is now (an approval applied and then superseded by a new candidate stays `applied`) | "Your answer to dr-7 was applied." |
| `waiting` | its own response is the accepted one, the decision `answered`, no application | "Your answer to dr-7 was recorded." |
| `not_used` | its own response is the accepted one, the decision final, and no application names it (its recheck did not admit it, its subject moved before the application, or it was cancelled: the view never says which) | "Your answer to dr-7 was not used." |
| `refused` (with `code`) | it has responses and none is accepted: `code` the newest one's, one of DR1's refusal codes (`decision_answered`, `decision_closed`, `stale_decision`, `stale_policy`, `not_approver`, `response_conflict`, `not_held`, `invalid`), each naming no one | its code's words (`decideWords`) |

No outcome while it is open and no response of its own is kept.

Left out, each on purpose: the run, `openedIn`, `origin`, `gate`,
`asker`, `member`, `ticket`, `plan`, `planDigest`, `requestId`, the
request's `evidence`, `expertise`, `subject` (a commit, a run, a task, a
path) and `for` (HM1's label, or the grant), the refs and their
resolutions, `policy`, `why`, `supersededBy` (a decision the principal
is not admitted to), `answer` (it names who answered), every other
responder's receipt, `applied` (it names the run and the task), the
history (the operator's comments, notes and routes, the router's rows),
Brakeman's card, and the inbox item's run, ticket and session names and
its blocking. A receipt's `words`, `actor`, `byName`, `text` and
`rationale` are left out of `own` too: the engine's words name other
people (`answeredWords` names who answered first), and the page says
each code in words of its own.

A refusal the respond route answers a decide principal carries
`{error: {code, message}, receipt?}`, its `message` the fixed words of
its code (`decideWords`, at most 200 bytes, naming no one) and its
`receipt` its own, reduced as `own` is: never the engine's words.

## Admission and its end

A decision's **entries** are what `crew.Admits` judges:
`crew.EntriesOf(rec)` is its policy's `authority` (`[{kind:
"operator"}]`, as every decision opens) and, when its route target is
the decide grant (the request's `for` is `{label: "grant:decide"}`, no
person), `{kind: "grant", grant: "decide"}` beside it. The policy column
itself is never written (`ledger.Tx.PutDecision` keeps it as opened);
the route target is HM1's stored field, written only by
`ledger.Tx.RouteFor`, which refuses a decision that is not open
(`internal/crew/ledger/decisions.go`, `RouteFor`). So **a decision's
entries change only while it is open**, and the entries a response was
judged by in its transaction are the entries at its recheck and at its
application.

`crew.Admission(rec, who Admittee) Admission` is the one judgement,
over the record a route read:

- `Whole`: `who` holds the admin grant (the workbench token, or a
  provider's principal with `admin`): DR1's routes as today.
- `Open`: `who` holds `decide`, an entry admits it (the route target is
  the decide grant) and the decision is `open`: the decide view whole.
- `Outcome`: `who` holds `decide`, an entry admits it, the decision is
  `answered` or final, and it has a response of its own: the outcome
  view.
- `None`: anything else, and every decision that does not exist:
  `404 not_found` with the same body for both.

So admission ends:

- **with the answer**, for every holder of the grant without a response
  of its own: once dr-7 is answered, a later pass, or walking the `dr-N`
  ids, finds nothing;
- **with the take-back**, for everyone: while dr-7 is open, the
  operator's route to the operator takes the target off the grant, at
  the `expectedRev` it read, as every route is judged, held run or not,
  and one operation is always left for it (a route to a person or a role
  replaces the target too, through the held run, as HM1's does; The
  changes, `internal/crew`);
- **with the pass**, when the provider stops admitting it: the next
  request is `401`.

A take-back and an answer meet in one row, and whichever commits first
decides:

| Order | The operator sees | The person sees |
|---|---|---|
| take-back, then the response's transaction | the take-back's receipt (`rev` + 1) | its response refused `403 not_approver`, kept as its own receipt; its next read `404`: "dr-7 is no longer open to you." |
| the response's transaction, then the take-back | `409 decision_answered`, the answer's words (whom it is for changes only while it is open); the answer goes on to its recheck and application, and the operator may still cancel it while it is `answered` (DR1's cancel) | its receipt `accepted`; the outcome `waiting`, then `applied` or `not_used` |

The respond route's transaction reads the row's entries itself
(`respondTx`, `crew.Admits`), so the read before it decides nothing; the
test holds the response between its read and its transaction with the
engine's `beforeRespond` hook and commits the take-back there.

Once answered, nothing a route does changes the entries, so the
confirmation needs no read of the record: `recheckResponse` asks the
provider through the guard, after the commit, and `ConfirmAuthority`
(which records the answer in the engine's memory under `e.mu`; it has
no transaction of its own) is told `admitted` only for the same subject
still holding the grant the response's authority names (`admin`, or
`decide`). The application's transaction (`applyTx`) is unchanged: it
supersedes an identity's answer not confirmed by this process, as
today.

**Other ends, whatever the count of operations**: the operator answers
dr-7 itself, or cancels it (DR1's cancel, open or answered, has the
final row's own place in the history and no operation slot); a stopped
run takes no identity's answer (`409 not_held`), and the take-back
works through the ledger alone while it is stopped; the provider ends
the pass. Stop, inspect and revoke are untouched (article 4).

**What an open page sees.** The page reads dr-7 every 10 s while the tab
is shown, and once at once when it is shown again (The changes,
`web/app`). So a page open when the take-back, the answer or the pass's
end commits shows the question at most one interval and one read longer
(10 s and a read bounded at 10 s: 20 s), and a response it sends
meanwhile is judged by the server fresh and refused. A refused read
(`401`, `404`) empties the pass and closes the card, the draft kept.

## The changes

### `ext`

- `identity.go`: `GrantDecide Grant = "decide"`, documented as "answer
  a decision routed to the grant, and read nothing else". `Grant.Valid`
  accepts it; `Principal.Valid`'s refusal words name the three ("is not
  admin, host or decide").
- `ext/exttest`: nothing new; `FakeIdentity.Add` already takes any valid
  principal, so its tests' passes hold `decide` once `Grant.Valid` does.

### `internal/proto`

`decide.go` (new), the wire of the three routes as Go types with their
bounds, beside the decision links' (`decisions.go`):

- `MeReply{Subject, Name string; Grants []string}` (`subject` and `name`
  omitted when empty) and `MaxMeReply = 1 << 10`: a subject of
  `ext.MaxSubject` (48) bytes and a name of `ext.MaxPrincipalName` (40)
  characters, each a character `encoding/json` escapes to six bytes
  (`<`, `>`, `&`, U+2028), are 528 bytes, the three grants and the rest
  under 100.
- `DecideView` (the table above), `DecideOption{Label, Tradeoffs}`,
  `DecideReceipt` (`own`'s entries, `responseId` among them),
  `MaxDecideView = 32 << 10`: the request's fields at most
  `ledger.MaxDecisionRequest` (16 KiB) as the ledger keeps them encoded,
  8 receipts of at most 256 bytes (a `responseId` of 32 bytes among
  them), the rest under 1 KiB.
- `DecideReply{Receipt DecideReceipt; Decision *DecideView}` and
  `DecideRefusal{Error{Code, Message}; Receipt *DecideReceipt}`, with
  `MaxDecideReply = MaxDecideView + 1<<10` and `MaxDecideWords = 200`.
- `OutcomeApplied`, `OutcomeWaiting`, `OutcomeNotUsed`,
  `OutcomeRefused`, the `outcome` words.

### `internal/crew`

- `decisions_admit.go` (new): `PolicyEntry{Kind string; Grant string}`
  (`json:"kind"`, `json:"grant,omitempty"`); `Admittee{Grants []string;
  Via string; Subject string}`, who asks; `Admits(entries []PolicyEntry,
  who Admittee) bool`: each entry admits by its own kind's rule, and one
  entry that does admits. `operator` admits `via` `workbench` with the
  admin grant, or `via` `identity` with the admin grant; `grant` admits
  `via` `identity` holding that grant; `router` is admitted by no entry;
  an unknown kind admits nobody. A later kind (SW1's subject entry) adds
  its rule here and nowhere else. `EntriesOf(rec)` (Admission and its
  end): the policy's `authority`, and the grant's entry when the route
  target is the grant; a policy it cannot read gives no entries, so it
  admits nobody. `Admission(rec, who)` is `Admits` over `EntriesOf` plus
  the state and the principal's own responses, over one record.
  `OutcomeOf(rec, actor) (outcome, code string)`: the four words of The
  decide view, from the record whole (`answerOf` for the accepted
  response, the application receipt's `Response`, every response row of
  `actor`).
- `decisions_for.go`: the decide grant is one more route target in the
  request's `for`, the field HM1's route writes. `DestGrant = "grant"`;
  `Destination` gains `Grant string` (`json:"grant,omitempty"`);
  `routeLabel` takes `{kind: "grant", grant: "decide"}` alone, no name,
  as the label `grant:decide` (any other grant, or a name, `invalid`).
  `forRoute`, for that label, makes `DecisionFor{Label: "grant:decide"}`
  without `Holder` (no person), its words "anyone holding the decide
  grant", its rule "local/1: the operator, or anyone holding the decide
  grant", its `to` the operator and `{kind: "grant", grant: "decide"}`;
  everything else is HM1's route as it is: only while the decision is
  open (`decision_answered` or `decision_closed` otherwise, no row), at
  the `expectedRev` read (at least 1), through the held run (`not_held`
  otherwise, "continue it, then route it to the grant"), the request's
  `for` rewritten by `ledger.Tx.RouteFor` in the operation's
  transaction, the `routed` row with `for` and `forWas`, `rev` + 1. A
  route to the grant of a decision already given to it is what HM1's
  route to the person a decision is already for is: a row and `rev` + 1.
  `grantFor(request) bool` reads the target; `requestFor` keeps
  answering nil for a `for` with no person, so HM1's readers (the
  receipt's `for`, the task file, `GET /api/decisions?for=`, a gate's
  label carried to its next decision) treat a grant target as for no
  person. `resolveFor` and `Holder` take only `person:` and `role:`, so
  no opening writes `grant:decide`; `gateFor`'s fallback, which keeps a
  label it cannot resolve as given, drops a `grant:` one.
- **The take-back** (`RouteDecision`): a route to the operator of a
  decision whose target is the grant goes through `forRoute` with no
  target, so `RouteFor` writes the request without its `for`, the row
  says `forWas` the grant, and `rev` + 1: only while open, at the
  `expectedRev` it names (judged in its transaction by `operate`'s
  `staleOp`, as every route is), through the held run **or the ledger
  alone** (its `notHeld` takes the same edit, which HM1's person route
  refuses while open). A route to a person or a role of such a decision
  replaces the target, as HM1's route does, so it takes the grant back
  too. A route to the operator of any other decision changes nothing, as
  today.
- **One operation is kept for the take-back** (`operate`'s `write`, in
  the operation's transaction): an operation after which the decision's
  target is the decide grant (the route to the grant, and every comment,
  request for more or route while it is given to it, the take-back
  excepted) is refused `429 decision_limit` ("the last operation of dr-7
  is kept for taking the decide grant back") when it would take the last
  of `MaxDecisionOperations`, counted as `Tx.History` counts the
  operator's group (`ledger.Tx.Operations(n)`, new, that same query). So
  a route to the grant is refused unless one slot is left after it, and
  the take-back always has one. A Brakeman turn's routes count in their
  own group and never reach such a decision (`internal/api`, below).
- `decisions.go`: `respondTx`'s check of the authority (`auth.Grant !=
  grantAdmin`, today) becomes `!Admits(EntriesOf(rec), auth as an
  Admittee)`, over the record its transaction reads, refusing
  `not_approver` ("dr-7 is not yours to answer: refused under local/1").
  The policy keeps its revision, `local/1`, so `stale_policy` keeps its
  meaning. `applyTx` is unchanged (Admission and its end: the entries
  cannot change once answered). `replaceDecision`, which copies the
  answered row whole, drops a grant target from the request it copies,
  so the replacement is the operator's alone (Owner decisions, 7).
- No ledger schema change and no migration: the `for` is a field of the
  request's JSON, of the shape HM1's `forShape` checks (a label, no
  person). An older binary reading a grant target treats it as a label
  naming no person, checks only the admin grant, and so refuses a decide
  answer `not_approver`; it supersedes an identity's answer whose `boot`
  is another process's: both fail closed.

### `internal/api`

- `identity.go`: `principal` gains `decide bool`; `grantPrincipal` sets
  it for `ext.GrantDecide`. `requireAdmin` is unchanged, so a
  decide-only principal is `401` on every admin route, `GET /api/events`
  among them (its check is `p.admin`). `admission(rec, p principal)
  crew.Admission` (new) is `crew.Admission` over the record and the
  principal as an `Admittee`; the read and the respond route call it and
  nothing else.
- `me.go` (new): `GET /api/me`, any authenticated principal:
  `proto.MeReply`, at most `proto.MaxMeReply`: the workbench token
  `["admin", "host"]`, a host token `["host"]`, a share link `[]`, a
  provider's principal its subject, name and grants; anything else
  `401` under the rate limit. Not refused on a switchyard (the workbench
  there is `["admin", "host"]` too; its lack of a role is `whoami`'s to
  say).
- `decisions.go`: the two decision routes of the surface are wrapped in
  `deciding` (new): the admin exactly as `launching` (so `403
  switchyard` on a switchyard for everyone), a decide-only principal
  through to the handler, `401` for every other principal under the rate
  limit. `GET /api/decisions` and the operator's operations stay
  `launching`.
  - `handleDecision`: for a decide principal, one read of the record
    whole (`s.ledger.Decision`), then `admission` over that record, then
    the reply built from that same record: `Open` or `Outcome`,
    `decideView` (the version judged is the version written); `None`,
    `404 {error: {code: "not_found", message: "no such decision"}}`,
    the body a decision that does not exist gets for a decide principal.
    `afterDecideRead` (a test hook, nil in the product) holds a read
    between its record read and its reply. The admin's read is as today.
  - `handleDecisionRespond`: for a decide principal, one read and
    `admission` first (`None`: the same `404`, nothing written), then
    DR1's transaction, whose `Admits` over the entries it reads decides.
    Its reply is `proto.DecideReply`: its own receipt reduced (its
    `responseId` the body's), and the decide view of the record read
    again after the commit, judged again (omitted when that read is
    `None`); a refusal `proto.DecideRefusal` with `decideWords(code)`.
  - `authorityOf` records the grant: `admin` when held, else `decide`,
    with `via: identity` and the subject.
  - `recheckResponse`'s `admitted` becomes: the provider answered in
    time, the same subject, holding the grant the response's authority
    names (`admin`, or `decide`). It reads no record.
  - `handleDecisionRoute` refuses `to: grant` with `409 no_identity`
    ("no identity provider is registered: a decision is given to the
    decide grant only where one is") before the engine when `s.exts`
    holds no identity provider (`Registered()` lists none of kind
    identity); the route stays the admin's.
- `inbox_decisions.go`: `answererOf` reads the record's route target too
  and names the grant ("the operator, or anyone holding the decide
  grant"); the inbox stays the admin's (`inboxOnly`, unchanged).
- `guide.go`: `decision_route`'s description names the grant
  destination, the take-back and `no_identity` (its schema is
  `Destination`'s, which gains `grant`).
- `brakeman_act_decision.go`: `admitVisibleRoute` refuses `to: grant`
  (`400 invalid`) and any route of a decision whose target is the grant
  (`409 stale_target`, "dr-7 is given to the decide grant: the operator
  may route it"), beside its refusal to change whom a decision is for.
  That check reads before the act runs, and needs nothing new to hold: a
  turn's route always carries an `expectedRev` of at least 1
  (`routeActArgs`), `admitVisibleRoute` checks it against its read, and
  `RouteDecision` judges it again in the operation's transaction
  (`operate`'s `staleOp`). The route to the grant bumps `rev`, so a turn's
  route admitted before it is `409 stale_decision` after it.
- `server.go`: `GET /api/me` registered; the two decision routes
  registered through `deciding`.

### `web/app`

Every site that holds or sends a credential, and what it does while a
pass is held:

| Site | Today | With this row |
|---|---|---|
| `plugins/token-fragment.client.ts` | takes `#token=` into the workbench store | takes `#token=` as today and `#pass=` into the pass store, both dropped from the address bar first |
| `composables/useDecide.ts` (new) | | the only code that sends the pass, in the `Authorization` header, to the three routes and nowhere else |
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
  - `me()`, `read(did)`, `respond(did)`, each with `Authorization:
    Bearer <pass>`, each aborted after `DECIDE_READ_WAIT` (10 s); a `401`
    or a `404` empties the pass store (`end()`) and closes the card
    (`ended` or `gone`); any other refusal is its code's words.
  - **Reads.** `read(did)` every `DECIDE_READ_EVERY` (10 s) while
    `document.visibilityState` is `visible`, none while hidden, and one at
    once on `visibilitychange` to visible and on the window's `focus`;
    one read at a time, a tick that finds one under way skipped. Each
    read is judged by the server fresh; the reads stop at `applied`,
    `not_used`, `ended` or `gone`. Its timers go through `setTimeout`, so
    vitest's fake clock (`vi.useFakeTimers()`, as
    `useDispatchLive.test.ts` drives one) moves them; Playwright drives a
    read by the focus event (its 1.44.1 has no clock).
  - `view`, the last decide view read; a refused send never clears it.
  - **What the page keeps of an answer**, one row per moment, against
    DR1's replay rules as they are on `main` (`replayed`, called by
    `RespondDecision` before any judgement and again in `respondTx`: the
    same actor and `responseId` with the same body, fingerprinted whole,
    `expectedRev` included, is answered from its saved row, an accepted
    one `200` repeat and a kept refusal refused again in its own words;
    the same id with another body is `409 response_conflict`):

    | Moment | `draft` (editable) | `sent` (immutable) | What the page shows and does | Test |
    |---|---|---|---|---|
    | the person edits | `{choice?, text, rationale}`, as typed | none | the form | `useDecide.test.ts`: the draft kept through a refused read |
    | the person sends | kept | `{responseId, choice?, text?, rationale?, expectedRev}`: a new `responseId` minted for this send, the draft's fields and the `rev` of `view`, frozen as sent | "Sending"; Send disabled | the body sent is `sent`, byte for byte |
    | `201`, or `200` repeat | kept | cleared | the receipt; then the outcome from the next read | `TestDecideOwnCarriesTheResponseID` (Go); `useDecide.test.ts` |
    | a definite refusal (a `4xx` with a code: `stale_decision`, `decision_answered`, `not_approver`, `response_conflict`, `invalid`, ...) | kept | cleared: its id is done with (a kept refusal is replayed for it, another body refused `response_conflict`) | its code's words; the next Send is a new attempt, with a new `responseId` and the latest `view.rev` | `TestDecideOwnCarriesTheResponseID`: after a kept `stale_decision`, the same id with the new rev `response_conflict`, a new id accepted; `useDecide.test.ts`: a new id after a refusal |
    | no definite answer (no reply, an abort, a `5xx`, `503 decision_moved`) | kept | kept as it was | "Not known whether your answer to dr-7 was recorded. Send it again: the same answer is never recorded twice."; Send again sends `sent` unchanged, never the draft | `TestDecideOwnCarriesTheResponseID`: the same body again `200` repeat with the saved receipt; `useDecide.test.ts`: the retry's body equals the first |
    | a read whose `own` holds `sent.responseId` | kept | resolved by that receipt (accepted, or the refusal row above) | as that row | `useDecide.test.ts`: a receipt of another `responseId` (another tab of the same subject) resolves nothing |
    | `401` | kept | kept | "Your access to this server has ended. Open it again from your workspace page."; every send disabled; the pass emptied | `decide-only.spec.ts` |
    | `404` | kept | kept | "dr-7 is no longer open to you."; every send disabled; the pass emptied | `useDecide.test.ts` |

    A receipt is matched to a send by its `responseId` alone, from the
    same read as the rest of the view; a newer receipt of another id
    says nothing of a send. A send whose receipt has left `own`'s eight
    is resolved by Send again, which the replay answers.
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
  - the outcome's words (The decide view): `waiting`, `applied`,
    `not_used`, and `refused` by its code (`decision_answered`: "dr-7 was
    answered before your answer reached it."; `decision_closed`: "dr-7
    was closed before your answer."; `stale_decision`: "dr-7 changed
    since you read it: read it again and answer.").
  - The options as `view.options` gives them, by index: the page makes
    none of its own, so an assignment's "Done" and "Not done" are the
    server's (`crew.DecisionOptions`).
  - No pass (`/decide` alone, or a reload after the end): the `401`
    words.
- `utils/decisions.ts`: the grant's target in the record's types (the
  request's `for` as `{label: "grant:decide"}`, no person), "who may
  answer" naming it ("the operator, or anyone holding the decide
  grant"), and no "for <person>" drawn for it.
- `utils/protocol.ts`: `GRANT_DECIDE`, `MeReply` with `MAX_ME_REPLY`,
  `DecideView`, `DecideOption`, `DecideReceipt`, `DecideReply`,
  `DecideRefusal` with their bounds, the `outcome` words,
  `DECIDE_READ_EVERY` and `DECIDE_READ_WAIT`, the route's `grant`
  destination and `no_identity`, beside the existing decision rows.

### `internal/proto`, `web/app/utils/protocol.ts`, `docs/protocol.md`

The triple together: the types and bounds above in `internal/proto`
(`decide.go`), their mirror in `protocol.ts`, and in `docs/protocol.md`:

- the HTTP API's opening paragraph: the decide grant, the three routes
  that admit it, every other route answering it as no credential
  (`401`), and no live connection for it;
- the Decisions table: a `GET /api/me` row; `GET /api/decisions/{did}`'s
  decide view (its fields, `MaxDecideView`, the `404` for a decision not
  admitted); the respond row's decide reply and refusal
  (`MaxDecideReply`, `decideWords`); the route row's `grant`
  destination beside the shipped ones, the take-back, the operation kept
  for it, Brakeman's refusal and `409 no_identity`; the decide page's
  reads (every 10 s while shown, each bounded at 10 s). The shipped bounds
  are kept: the record's read at most 2 MiB (`maxDecisionReply`), a
  response's body at most 32 KiB (`maxDecisionRespondBody`);
- the `GET /api/inbox/item` row: its answerer words naming the grant
  entry. The inbox's three rows otherwise unchanged: they stay `admin`,
  a decide principal `401` as no credential;
- the fragment key `pass` beside `token` (2 KiB of `[A-Za-z0-9._-]`,
  dropped from the address bar, kept for the tab, never sent in a URL).

### Docs

`docs/features.md`: the row's entry. `docs/repository-map.md`: the
`internal/api/decisions.go` row names the decide view and `admission`;
the `ext` row names three grants.

## Events, orderings and invariants

**The pass.** The plugin reads the fragment, replaces the address bar's
URL with both keys taken out, and only then stores what it took; no
request is made before it runs (it is a client plugin, ahead of the first
page's setup), and the middleware judges from the fragment itself if it
runs first. The pass leaves the browser only in an `Authorization` header
`useDecide` sets; the server reads no pass from a query (every decide
route calls `presentedToken(r, false)`). A stored workbench
token is never written by this path, and while one is stored the pass is
kept and not sent. The pass is never logged, by the server (the guard
logs a provider's name, never a token) or by the page.

**The route to the grant and the take-back.** Each is one of DR1's
operations, in its order: the repeat look by `(decision, actor, opId)`
first (its receipt, `repeat: true`); then, in one transaction, the row
judged (its run, the `expectedRev` read, `staleOp`), the state (`open`,
`forRoute`'s edit judged against the row), the operations left (one kept
for the take-back), then the request's `for` rewritten (`RouteFor`), the
`routed` row and `rev` + 1. The route to the grant is made through the
held run only; the take-back through the held run or the ledger alone.
The registration check (`no_identity`) is made before the transaction
and writes nothing. After the commit `decisionTold` sends DR1's
`decision` event on `GET /api/events`, the notification and the
router's tell, as for every route.

**A decide principal's response.** The route's read and `admission`
(`404` when `None`), then DR1's transaction, whose policy check is
`Admits` over the entries it reads: a take-back committed in between is
`not_approver`, kept; a take-back after the commit is `decision_answered`
and changes nothing (Admission and its end). An identity's response to a
run not held is `not_held`, as today. After the commit,
`recheckResponse` asks the provider through the guard (bounded by
`ext.IdentityWait`) and calls `ConfirmAuthority` with `admitted` true
only for the same subject still holding the grant its authority names.
A provider that fails, is late or is away has not said yes: the decision
is superseded, the replacement opens for the operator alone, and the
person's next read says `not_used`.

**A decide principal's reads.** Each is one record read, judged and
built from that read: no frame or reply is written from a judgement of
another read, and nothing is pushed. The page asks again on its
interval and when shown again; the server keeps nothing for it between
requests.

**What a pass meets when it ends.** Every request asks the provider
(C2's rule), so the first request after the end is `401`. A pass a
provider no longer admits is never cached by the server.

Invariants:

- **I1, one judgement.** Every decision of whether a principal may read
  or answer a decision is `crew.Admission` (with `crew.Admits` over
  `crew.EntriesOf` inside it) over one record: the read, the respond
  route's check and the response's transaction (`Admits`).
  `TestAdmitsIsTheOneCheck` holds them to one table.
- **I2, the three routes.** A decide-only principal is answered by every
  route outside the three exactly as a request with no credential is,
  and is written no byte of a decision `Admission` answers `None` for.
- **I3, the table.** What a decide principal reads of a decision is
  `proto.DecideView`, every field named in The decide view's table,
  built from the one record its route read.
- **I4, the pass in no URL.** The pass is read only by the plugin and
  `useDecide`, and sent only in an `Authorization` header; the server
  reads it from no query. (The workbench token keeps the URLs it is in
  today: the WebSockets' `?token=` and the raw file links.)
- **I5, the workbench token kept.** The pass path never writes
  `conductor.workbenchToken`.
- **I6, no schema change.** The ledger's tables and version are
  unchanged, and the policy column is written by no new code; an older
  binary fails closed on what this one writes.
- **I7, entries change only while open.** The grant's entry is the
  route target, which only `RouteFor` writes, and only while the
  decision is open; so the entries a response is judged by are the
  entries at its recheck and its application.
- **I8, the take-back has its slot.** While a decision is given to the
  grant, one of its `MaxDecisionOperations` is free, and only the
  take-back may take it.

## Out of scope

- Whatever issues a pass, how long it lasts, and how a person is sent to
  the decide page: the module that registers a provider; this repository
  only asks the provider about a token.
- The decide grant on the inbox (`GET /api/inbox`, `GET
  /api/inbox/item`, `GET /api/inbox/track`), its counts and pages, a list
  of the decisions that admit a principal, a stream across decisions or
  of one (Owner decisions, 16), Brakeman's card, and evidence refs
  resolved for a decide principal: a
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
- A take-back once a decision is answered: whom it is for changes only
  while it is open (HM1's `RouteFor`), so the answer goes to its recheck
  and application; the operator cancels an answered decision with DR1's
  cancel instead, and no other holder reads it by then (Owner
  decisions, 4).

## Tests per surface

Every timed thing is driven through a seam, never waited for: a response
held between its read and its transaction by the engine's
`beforeRespond`, a Brakeman route held by the act seam
(`atActSeam("pending")`), the provider's answer through `exttest`'s
`FakeIdentity` (`Add`, `Remove`, `SetErr`) and the guard's gate, a read
held after its record read by `afterDecideRead`, the page
through `useDecide().whenIdle()`, vitest's fake clock and a fake fetch
whose replies the test releases.

Go race (`go test -race -count=1 -timeout 60m ./...`, with `make lint`,
`make lint-static`, `make vuln`):

- `ext`: `TestGrantDecideValid` (`decide` valid, alone and beside
  `admin` and `host`; given twice refused; an unknown grant's words name
  the three).
- `internal/proto`: `TestDecideBounds` (a `MeReply`, a `DecideView`, a
  `DecideReply` and a `DecideRefusal`, each with every field at its
  bound and of characters `encoding/json` escapes to six bytes, fits its
  constant).
- `internal/crew`:
  - `TestAdmission` (the table: admin, decide, both, none, a router,
    against a record whose target is and is not the grant, an unreadable
    policy and an unknown kind, in each state, with and without a
    response of its own).
  - `TestRouteToGrantSetsTheTarget` (open and held, at its rev: the
    request's `for` the grant, a `routed` row with `for` and `forWas`,
    `rev` + 1, the policy column byte for byte the opening's; answered,
    closed, a stale rev and a run not held refused with no row; a route
    to `admin` or with a name `invalid`; `gateFor` drops a `grant:`
    label).
  - `TestTakeBackWhileOpen` (a route to the operator of a decision given
    to the grant, held and not held: no `for`, a row, `rev` + 1; at a
    stale `expectedRev` `stale_decision` and nothing changed; of an
    answered one `decision_answered` and nothing changed; a route to a
    person replaces the grant).
  - `TestTakeBackKeepsItsSlot` (a decision with 30 operations: the route
    to the grant taken as the 31st, a comment then refused
    `decision_limit`, the take-back taken as the 32nd; one with 31: the
    route to the grant refused).
  - `TestDecideResponseUnderTheTarget` (a decide authority accepted with
    the grant's target, `not_approver` without it; a response held at
    `beforeRespond` while the take-back commits: `not_approver`, kept; a
    response committed first: the take-back `decision_answered`, the
    answer applied).
  - `TestReplacementIsTheOperators` (an identity's answer not confirmed:
    the replacement's request has no `for`, and a decide principal is
    `None` on it).
  - `TestOutcomeOf` (`waiting`; `applied`; an approval applied and then
    superseded by a new candidate still `applied`; `not_used` by a failed
    confirmation, and `not_used` by its subject moving before the
    application, each without a reason; `refused` with its newest code;
    an accepted response of its own found after eight later refusals of
    its own).
- `internal/api`, in `decide_test.go`, with `exttest.NewFakeIdentity`
  registered:
  - `TestDecideGrantAdmitsDecisionRoutesOnly`: every pattern the package
    registers (found by scanning its non-test sources for `HandleFunc`
    patterns, the constants among them, so a route added later is walked
    too) asked with a decide-only pass and with no credential, the rate
    limit raised for both walks: the three routes answer the decide
    principal; every other answers it exactly as it answers no
    credential (status and body), `GET /api/events`, the inbox's three
    routes and `GET /api/decisions` among them.
  - `TestMeAnswersEveryPrincipal`: workbench, host token, link, a
    provider's principal with each grant, none (`401`); a subject and a
    name of characters that escape to six bytes fit `MaxMeReply`.
  - `TestRouteToGrantNeedsAProvider`: no provider registered, `409
    no_identity`, no row; registered, the target and the row.
  - `TestDecideReadCarriesNothingElse`: a decision whose request cites a
    ticket, a session and a file, with an operator's comment in its
    history, another subject's refused response, a Brakeman card on its
    subject and an HM1 label before the grant, each carrying a marker
    string; sessions started, activity, chat and other decisions changed
    meanwhile, other decisions given to the grant among them: read as a
    decide principal, open and then answered, the reply holds none of the
    markers, nor the run's id, nor the other subject, nor another
    decision.
  - `TestDecideViewIsTheTable`: walks `proto.DecideView`'s and
    `DecideReceipt`'s fields by reflection against the table's names, so
    a field added without the table fails.
  - `TestDecideViewOfAnAssignment`: an assignment given to the grant
    reads "Done" at 0 and "Not done" at 1 (the labels
    `crew.DecisionOptions` gives); a choice of 1 is accepted.
  - `TestDecideReadJudgesTheRowItReturns`: the read held after its read
    of the record (a hook), the grant taken back and a comment committed,
    the read released: the reply is the view of the rev it judged; the
    next read is `404`.
  - `TestDecideAdmissionEnds`: answered by another, a holder with no
    response of its own `404`, the responder the outcome view alone; a
    take-back of an open one whose run is not held, through the ledger
    alone: `404` for every holder.
  - `TestDecideOwnCarriesTheResponseID`: two sends of one subject with
    two ids (two tabs), each receipt in `own` under its own
    `responseId`; the same body sent again `200` repeat with its saved
    receipt; after a kept `stale_decision`, the same id with the new
    `expectedRev` `409 response_conflict` and a new id accepted.
  - `TestDecideRefusalsNameNobody`: `decision_answered`, `stale_decision`
    and `not_approver` answered to a decide principal carry
    `decideWords` and its own receipt reduced, never the other
    responder's name or subject.
  - `TestConfirmAuthorityAdmitsDecide`: the same subject still holding
    the decide grant admitted and applied; the provider's principal
    changed to one without it before the recheck, another subject, and a
    provider late through the guard's gate: superseded, the view
    `not_used`; a take-back sent while the recheck is held at the gate:
    `decision_answered`, and once released the answer applied.
  - `TestDecideReadsAskTheProvider` (for the issue's
    `TestPrincipalWatchWatchesDecide`, Owner decisions, 16): a pass the
    fake stops admitting (`Remove`) is `401` at the next read and the
    next response, nothing cached; a decide principal holds no
    connection the recheck would watch.
  - `TestBrakemanRouteKeepsOffTheGrant`: a turn's route to the operator
    admitted at rev R and held at `atActSeam("pending")`, the operator's
    route to the grant committed (R + 1), the act released: `409
    stale_decision`, the target still the grant; a turn's route of a
    decision given to the grant, to any destination, `409 stale_target`;
    `to: grant` from a turn `400 invalid`.
  - `TestAdmitsIsTheOneCheck`: one table of records (target, state, own
    response) against admin, decide, both, none and a router: the read's
    answer, the respond route's check and the response's transaction
    agree with `crew.Admission` on every row.

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
  releases, `vi.useFakeTimers()`, `whenIdle()`): every request to one of
  the three routes, the pass in its header and in no URL; a read every
  10 s of the fake clock while visible, none while hidden, one at once
  on `visibilitychange` and on `focus`, one at a time; a read aborted at
  10 s; a `401` and a `404` each empty the pass and close the card, the
  draft kept; each row of the send table (a new `responseId` per send,
  `sent` frozen and resent byte for byte after no reply, a new id after
  a definite refusal, a receipt of another id resolving nothing); each
  `outcome`'s words; an assignment's options drawn as the view gives
  them.
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
- **The page against the three routes stubbed in the browser**
  (`page.route`, as `brakeman.spec.ts` and `decisions-gate.spec.ts` stub
  replies): the decide page alone ("Signed in as Ana", no sidebar, the
  token dialog never opened), every request one of the three with the
  pass in its header, no response `401` until the spec answers one, no
  request to an events stream; an assignment answered "Not done"
  (choice 1); a send whose reply the stub aborts, "Not known whether
  your answer to dr-7 was recorded", then Send again whose body the spec
  asserts equal to the first, answered from its receipt; a read driven
  by a `focus` event the spec dispatches (no clock is waited on) whose
  stub answers `404`: "dr-7 is no longer open to you."; the stub's `401`
  to a send: the ended words with the draft still in its field;
  `not_used` drawn from a stubbed view.
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
  operator's; the operator takes the grant back from an open decision
  with a slot always kept for it, cancels an answered one, and a revoked
  pass loses its next request.

## The PRs

The one PR is issue #776's: one branch `issue-776`, cut from `main` once
#775's PR has merged, a draft PR at its first commit, the checks of every
surface before each push, its body ticking the open-core checklist. It
leaves `main` working and green on its own and carries the
`docs/features.md` entry.

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with |
|---|---|---|---|---|---|---|
| PR-1 (#776, ~550 + 700 lines) | ext, api, web: a decide grant and a pass taken from the fragment | `ext.GrantDecide`; `proto`'s decide types and bounds; `crew.Admits`, `crew.EntriesOf`, `crew.Admission`, `crew.OutcomeOf`, the grant as a route target (`routeLabel`, `forRoute`, `gateFor`), the take-back, the operation kept for it (`ledger.Tx.Operations`), `respondTx`'s check, `replaceDecision`'s drop; `GET /api/me`, `deciding`, the decide view and words, `admission`, `authorityOf`, `recheckResponse`, `no_identity`, `answererOf`, Brakeman's refusal; the pass parser, its store, the plugin, the middleware, `useDecide` and its reads, the decide page; `docs/protocol.md`, `protocol.ts`, `docs/features.md`, `docs/repository-map.md` | `ext`, `internal/proto`, `internal/crew`, `internal/api`, `web/app`, `web/e2e`, `docs` | every test above | #775 (its PR merged); DR1, HM1 and MB0 on `main` | none: #775 shares `ext`, `internal/api`, `web/app` and `docs` |

## Owner decisions

Dated 2026-11-10. Each is a default for a choice the issue's words leave
open, or a narrowing of them, the fail-closed one, which the owner may
overturn. Review round 2 changed 1, 4, 5, 7, 8 and 12, withdrew 11 (its
stream is gone) and added 16 to 19; the numbers are kept so the earlier
threads still point at the right one.

1. **Three routes, one decision.** The grant reaches `GET /api/me`, one
   decision's read and its respond route, and nothing else. The issue's
   "decision page's read model filtered to the decisions that admit it"
   and its stream are not built: a person answers the decision its
   address names (`/decide/<did>`), and a read computed over every record
   and narrowed afterwards is where a grant leaks (its order, its bound,
   its count, its errors, the records it links). The stream: 16.
2. **The decide view is a table.** A decide principal reads the
   request's own words, its state and its own receipts (each with its
   `responseId`), never the record whole, the refs, the history or
   another's receipt (The decide view). Its options are the engine's
   (`crew.DecisionOptions`), an assignment's two fixed ones among them.
3. **Admission ends with the answer.** Once a decision is answered, a
   holder of the grant reads only the outcome of its own answer, and one
   with no answer of its own reads nothing.
4. **The take-back is a route, while the decision is open** (round 2;
   round 1 had it in every state at any rev). It is a route to the
   operator (or to a person or a role), at the `expectedRev` read, held
   run or not, with one operation kept for it (18). The grant's target is
   HM1's `for`, which changes only while a decision is open, so once
   answered nothing takes it back: the answer goes to its recheck and its
   application, which the operator may stop with DR1's cancel while it
   is `answered`, and no other holder reads the decision by then.
5. **"A new revision" is the decision's `rev`.** The route to the grant
   bumps the record's `rev`; the policy keeps `local/1` and is not
   written (17): `stale_policy` refuses any revision but the one in
   force, so a new policy revision would refuse every response, while
   `rev` + 1 makes a card read before the route `stale_decision`.
6. **Given only while open, at its rev, through the held run.** The
   route to the grant is HM1's route to a person, with another target.
7. **A replacement is the operator's.** The fresh decision that replaces
   an identity's answer not rechecked opens with no grant target (round
   2: `replaceDecision` copies the row whole, so it drops it); the
   operator routes it again if a holder of the grant should answer it,
   and the person's page says `not_used`.
8. **Brakeman neither gives nor takes the grant.** Its act refuses `to:
   grant` and any route of a decision given to the grant; a turn's route
   admitted before the grant is refused `stale_decision` by the
   transaction's rev check, which every turn's route already carries
   (round 2: no new restriction is needed in the engine).
9. **Not admitted is not found.** A decide principal's read or response
   of a decision `Admission` answers `None` for is `404 not_found`, with
   the body a decision that does not exist gets.
10. **`GET /api/me` for the chain's tokens**: the workbench token
    `admin` and `host`, a host token `host`, a link none.
11. **Withdrawn in round 2.** It said how the stream ended; there is no
    stream (16).
12. **The pass lives in the tab; the draft and the answer sent in the
    page.** The pass in `sessionStorage`, emptied at its first `401` or
    `404`; the draft and the frozen `sent` body in the page's state,
    never in the browser's storage: a new Open reloads the page, which
    reads the outcome of an answer sent before.
13. **The workbench token wins.** With one stored, the pass is kept and
    not sent, and the decide address opens the operator's page.
14. **On a switchyard the decide routes are `403 switchyard`**, as
    `launching`'s are: decisions are answered where the work runs.
15. **Playwright on the shipped build.** No test-only binary registers a
    fake provider; the page is proved against the real server's refusal
    and against stubbed replies, the server by Go tests with `exttest`.
16. **No live connection for a decide principal** (round 2). The issue
    names `GET /api/decisions/events` as "the only live connection a
    decide-only principal may open"; this row opens none. The decide page
    reads its one decision every 10 s while shown and at once when shown
    again, each read judged fresh, so an open page outlives a take-back,
    an answer or a pass's end by at most one interval and one read
    (20 s), and a response sent meanwhile is refused. That removes the
    stream's caps, its signal under the ledger's lock and the race
    between a delivery's judgement and its write. The issue's stream
    tests become read tests:
    `TestDecisionStreamCarriesOnlyAdmittedDecisions` is
    `TestDecideReadCarriesNothingElse` (other decisions and the session,
    activity and chat changes among its markers),
    `TestDecisionStreamPolicyChangedDuringDelivery` is
    `TestDecideReadJudgesTheRowItReturns`, and
    `TestPrincipalWatchWatchesDecide` is `TestDecideReadsAskTheProvider`.
    A later row may add a stream on this one's judgement.
17. **The grant is a route target** (round 2). The issue's route "adds
    `{kind: "grant", grant: "decide"}` to the decision's policy
    authority as a new revision"; this row keeps that entry as the route
    target in the request's `for`, written by HM1's operation and its
    ledger method (`RouteFor`), and `crew.Admits` judges the policy's
    entries with the target's beside them (`crew.EntriesOf`). No ledger
    write changes a policy today (`PutDecision` keeps it), so this adds
    no writer, no new bound and no new history row; `admits(authority,
    principal)` stays one function over entries, and SW1's subject entry
    adds its rule there.
18. **One operation is kept for the take-back** (round 2). While a
    decision is given to the grant, the last of its 32 operations is the
    take-back's; a route to the grant needs two left.
19. **The outcome is one of four words** (round 2): `applied`,
    `waiting`, `not_used`, `refused` with a code, from the whole record,
    never a reason.

## Acceptance

The acceptance boundary is observed when the PR's checks pass in CI and
locally: `TestDecideGrantAdmitsDecisionRoutesOnly` and the other
`internal/api` tests show a principal with only the decide grant
answering the decision it may through the three routes and answered as
no credential everywhere else, `GET /api/events` among them;
`TestDecideReadCarriesNothingElse` shows it reading nothing else; the
vitest files and `decide-only.spec.ts` show
the pass taken from the fragment, dropped from the address bar, kept for
the tab and never sent in a URL. Done is the PR merged with those checks
green. Nothing needs the owner but the merge; a live check with a real
identity provider is the provider module's.

## The amendment to docs/plans/decisions.md

This plan's pull request changes `docs/plans/decisions.md` in these
places, quoted as that file now reads. It adds the grant beside every
destination and caller DR1 and HM1 ship (the operator, a person, a
responsibility, a Brakeman turn granted `route_decision`, as
`docs/protocol.md`'s route row has them) and replaces none.

The policy (The changes, the ledger):

> `policy` (JSON): `{revision: "local/1", approver: "operator", quorum: 1,
> authority: [{kind: "operator"}]}`. The only policy of this repository is
> `local/1`: one response from a principal the decision's entries admit
> decides it. Its entries are the policy's `authority`, whose `{kind:
> "operator"}` admits the admin grant (the workbench token, or an identity
> provider's principal with `ext.GrantAdmin`), and, while the decision's
> route target is the decide grant (Routing, below: the request's `for`,
> never the policy), `{kind: "grant", grant: "decide"}` beside it, which
> admits anyone an identity provider gives `ext.GrantDecide`
> (`docs/plans/decide-grant.md`). One function, `crew.Admits`, judges the
> entries (`crew.EntriesOf`), for the response's transaction and every
> read of a decide principal (`crew.Admission`); no write changes the
> policy. The record keeps it so that a response and an application check
> the policy the decision was opened under against the one in force, and
> so that a later policy is a later revision of the same field.

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
> it. Admitted again as the same subject holding the grant the response's
> authority names (the admin grant; or the decide grant, which the
> decision's entries admitted in the response's transaction and which no
> route changes once the decision is answered:
> `docs/plans/decide-grant.md`): the application proceeds in its own
> transaction, which checks that `k` is still the decision's accepted
> response and `boot` this process's. Not
> admitted, or no answer in time (a provider that fails, is late or is
> busy has not said yes, and a gate is never released on what nobody
> said): the decision is superseded by a fresh one, the same request (less
> a decide-grant target: `docs/plans/decide-grant.md`) and holds
> ("user:nate's answer could not be rechecked; dr-9 asks again"),
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
> HM1 adds a person's label. It changes no state. HM1's routes to a
> person and to a responsibility keep their rules (HM1, below; the route
> row of the table). A route to `{kind: "grant", grant: "decide"}`
> (`docs/plans/decide-grant.md`) is one more target of HM1's route: refused
> `409 no_identity` when no identity provider is registered, and otherwise
> taken as a route to a person is, only while the decision is open, at the
> `expectedRev` its body carries and through the run that holds it; in one
> transaction the request's `for` is rewritten (`Tx.RouteFor`) to `{label:
> "grant:decide"}`, the `routed` row written and `rev` bumped, so a card
> read before it is `stale_decision`. The decision then admits anyone
> holding the decide grant beside the operator, not one person; the policy
> is not written. A route to the operator of a decision given to the grant
> takes it back (the `for` rewritten without it, a `routed` row, `rev` +
> 1): only while it is open, at its `expectedRev`, through the held run or
> the ledger alone; a route of it to a person or a responsibility, made as
> HM1's is, replaces it. While a decision is given to the grant, the last
> of its `MaxDecisionOperations` is kept for that take-back. A Brakeman
> turn's route refuses `to: grant` and any route of a decision given to
> the grant.

The route table's rows (`internal/api`: the routes; the other rows are
unchanged). The bounds are the shipped ones (`maxDecisionReply`,
`maxDecisionRespondBody`, `docs/protocol.md`'s Decisions table), and the
route row is `docs/protocol.md`'s with the grant added:

| Route | Who | Body → reply |
|---|---|---|
| `GET /api/decisions/{did}` | admin, or a principal with the decide grant the decision admits (`crew.Admission`; `404 not_found` for one it does not) | the admin: `{decision, refs}`, the record whole and its evidence refs' resolutions, at most 2 MiB (`maxDecisionReply`: the record's parts at their bounds, its 32 receipts at `ledger.MaxDecisionReceipt`, 32 KiB each, and its refs at `maxDecisionRef`); a decide principal: the decide view alone (`proto.DecideView`, at most 32 KiB, `docs/plans/decide-grant.md`) |
| `POST /api/decisions/{did}/respond` | admin, or a principal with the decide grant the decision admits (`404 not_found` for one it does not) | `{responseId, choice?, text?, rationale?, expectedRev, byName?}` (at most 32 KiB, `maxDecisionRespondBody`: `text` at most 4,096 bytes and `rationale` 2 KiB, each as `encoding/json` sends it, escapes and all) → `201 {receipt, decision}`; `200` repeat; `409 decision_answered`, `decision_closed`, `stale_decision`, `stale_policy`, `response_conflict`, `not_held`; `403 not_approver`; `503 decision_moved`; a decide principal's reply its own receipt and the decide view (`proto.DecideReply`), a refusal's words its code's (`decideWords`) |
| `POST /api/decisions/{did}/route` | admin, or a Brakeman turn granted `route_decision` | `{opId, to: {kind: "operator"} \| {kind: "person", name} \| {kind: "role", name: <responsibility>} \| {kind: "grant", grant: "decide"}, note?, expectedRev}` → `{receipt, decision}` (a turn: `{receipt, error}`); `200` the same for a repeat; `409 op_conflict`, `stale_decision`; `429 decision_limit` (the operator's operations, comments, requests for more and routes together, at most 32 per decision, the last kept for the take-back while it is given to the grant; a Brakeman turn's routes, at most 4, apart from them). To the operator it changes no state, but takes back a decision given to the grant. To a person or a responsibility (HM1), resolved in the run's crew as launched (`400 invalid` for one it does not resolve), it changes whom the decision is for, and to the grant it gives the decision to the decide grant: only while it is `open` (`409 decision_answered`, `decision_closed` otherwise), at the `expectedRev` read (at least 1), through the run that holds it (`409 not_held` otherwise; the take-back also through the ledger alone); it rewrites the request's `for`, writes the `routed` row with `for` and `forWas`, and bumps `rev`. To a person or a responsibility, who answers does not change: the operator; to the grant, anyone holding the decide grant answers too. To the grant: the admin's alone, `409 no_identity` on a build with no identity provider registered; a turn's `to: grant` is `400 invalid`, and a turn's route of a decision given to the grant `409 stale_target` |

The paragraph after the table:

> A share link's controller, an agent's token and a Brakeman turn's token
> are refused every admin route (`requireAdmin`'s `401 unauthorized`), so
> the free build's one approver is the operator; `403 not_approver` is the
> policy's refusal of a principal that got past it without the authority
> `local/1` names (a router's answer, below). A principal holding the
> decide grant and not the admin grant, which only a build with an
> identity provider registered can present, is admitted by `GET /api/me`
> and by the read and the response of a decision it is admitted to
> (`docs/plans/decide-grant.md`), and answered by every other route as a
> request with no credential is (`401 unauthorized`).
