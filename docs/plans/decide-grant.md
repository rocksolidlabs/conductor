# A decide grant and a pass taken from the fragment (issue #776)

Today an identity provider's principal is the operator (`ext.GrantAdmin`)
or a host (`ext.GrantHost`), nothing between. A person who should answer
one decision and see nothing else has no grant to hold, so a provider
could only make that person the operator. This plan adds the grant
between them: `ext.GrantDecide`, which answers a durable decision (DR1,
`docs/plans/decisions.md`) the operator has routed to it, and reads
nothing else but the list of `{id, rev}` of the open decisions routed to
it; the decide page, the decision page drawn for such a principal alone;
DR1's route operation taught to give a decision to the
grant, as one more of the route targets HM1 writes, and to take it back;
and the web app's way to take such a principal's credential, a pass,
from the URL fragment without ever sending it in a URL.

It is the row of issue #776, built as two PRs (The PRs: the server's
half for #776, the app's half for an issue cut when this plan merges),
written from the issue's words against
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
uses none of them, since the decide grant gives no live connection
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
   provider), whatever a page still shows.
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
10. **Contract.** This plan before its code, two PRs, each of one issue
    (#776's for the server, and the app's cut when this plan merges; The
    PRs), race tests, vitest and Playwright, the protocol triple together
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
2. The person is sent to `https://<server>/decide#pass=<pass>`, the
   decide page's address with a pass in its fragment, or to
   `/decide/dr-7#pass=<pass>`, which shows dr-7 first. How they are sent,
   and by what, is the provider module's business. The app takes the
   pass out of the address before the app's router reads it, keeps it
   for the tab, and asks `GET /api/me`, which answers the principal's name
   and grants.
3. A principal with the decide grant and not the admin grant sees the
   decide page alone, headed "Signed in as Ana": no sidebar, no sessions,
   no runs, no Dispatch, no inbox. The page reads which decisions await
   Ana through `GET /api/me/decisions`, the `{id, rev}` of each open
   decision routed to the grant among the server's newest 500 open
   decisions, newest first, and nothing else of them (The decide list,
   below). It reads each card through
   `GET /api/decisions/dr-7`, which answers a decide principal the decide
   view (a fixed projection, The decide view, below), at most four cards
   at once, and answers through `POST /api/decisions/dr-7/respond`. It
   reads the list and each open card again every 10 s while the tab is
   shown, and at once when it is shown again, and lets Ana answer only on
   a view read in the last 25 s; the decide grant gives no live
   connection. Those three and `GET /api/me` are the grant's whole
   surface.
4. Ana's answer is recorded under `user:<subject>` with the authority
   `{grant: decide, via: identity}`, rechecked with the provider after
   its commit, and applied by Yardmaster as any identity's answer is.
   Once dr-7 is answered, it leaves the list; Ana reads only the outcome
   of her own answer, by its address, and another holder that sent no
   response of its own reads nothing of it.
5. When the provider stops admitting the pass (its end, or a revocation),
   the page's next request is `401`; the page says "Your access to this server
   has ended. Open it again from your workspace page.", clears the pass,
   and keeps the answer Ana had not sent yet. A new Open brings a new
   pass.

A browser that already holds a workbench token for that origin keeps it:
the pass never replaces it, and the decide page's address then opens the
operator's page for dr-7 instead (the operator's decision page, for
`/decide` alone).

## The decide principal's surface

A grant adds its own surface and takes nothing away: a principal's
surface is the union of what each grant it holds gives. The decide grant
gives these four routes, a closed list, and nothing else. A principal
holding `decide` and not `admin`, whatever else it holds, is a **decide
principal**: these four answer it as the decide grant's, and every
other route answers it exactly as it answers the same principal without
the decide grant. A decide-only principal is answered as a request with
no credential (`401` under the rate limit for every route that needs
one, `GET /api/events`, the inbox's three routes, `GET /api/decisions`
and the operator's operations among them); one that also holds `host`
keeps the host connection exactly as today (`hostAllowed`, `/ws/host`).
A principal holding `admin` is the operator, whatever else it holds.
`deciding` authenticates first, as `launching` does on `main`
(`requireAdmin` around `launchingAny`): the switchyard's `403` is only
ever said to a principal the route admits.

| Grants held | `requireAdmin` (`p.admin`) | `hostAllowed` (`p.host`) | `GET /api/me/decisions` (`deciding`) | the read and the respond route as the decide grant's (`deciding`) | the list, the read and the respond route on a switchyard (`deciding`) | the decide page |
|---|---|---|---|---|---|---|
| `admin`, with or without others | yes | yes | no: `403 not_decide` (the operator reads `GET /api/decisions`) | no: the admin's, as today | `403 switchyard`, as `launching` answers it today | no: the operator's page |
| `host` | no | yes | no (`401`) | no (`401`) | `401`, as on `main` | no |
| `decide` | no | no | yes | yes | `403 switchyard` | yes |
| `host` and `decide` | no | yes | yes | yes | `403 switchyard` | yes |
| none (a share link, no credential) | no | no | no (`401`) | no (`401`) | `401`, as on `main` | no |

| Route | What it answers a decide principal | Why the person needs it |
|---|---|---|
| `GET /api/me` | `{subject, name, grants}` | the page's "Signed in as", and whether this pass is a decide pass |
| `GET /api/me/decisions` | the decide list: `{decisions: [{id, rev}], cut?}`, the open decisions whose entries admit it among the server's newest 500 open decisions, newest first, at most 200 and 16 KiB (The decide list, below) | which decisions await its answer, when it was sent to the decide page with no decision's address |
| `GET /api/decisions/{did}` | the decide view of one decision it is admitted to (below); `404 not_found` otherwise | the question it was routed, and later the outcome of its answer |
| `POST /api/decisions/{did}/respond` | DR1's response, judged by the decision's entries (`crew.Admits`); its reply the decide view and its own receipt | the answer itself |

No live connection is among them: the decide grant gives none, and the
decide page reads the list and its open cards again on an interval and
when shown again (The changes, `web/app`; Owner decisions, 16). So no
stream, no signal under the ledger's lock and no delivery is planned,
and `GET /api/events` stays `401` to a decide principal.

Asked of each further route the first draft gave the grant, whether the
issue's outcome ("a person who should answer one decision, and see
nothing else") needs it: the inbox list and item (`GET /api/inbox`,
`GET /api/inbox/item`), their counts and pages, the track route
(`GET /api/inbox/track`), a stream across decisions or of one,
Brakeman's card on an item, and evidence refs resolved through the
operator's resolver. It does not: each is a read computed over records
the person may not see, and returns other records, counts and pages. None
is given to the grant (Out of scope; Owner decisions, 1). What the person
does need, to find the decisions that await it when it comes with no
decision's address, is the issue's read model in its smallest form: the
decide list, a route of its own, each row judged by the one-decision
read's own admission, with no count beyond its entries, no page and no
cursor.

## The decide list

What `GET /api/me/decisions` answers a decide principal, and nothing
more: `proto.DecideList`, the issue's stream snapshot served as a read.

| Field | From | Why the answerer needs it |
|---|---|---|
| `decisions[].id` | the row's id | which decision to read (`GET /api/decisions/{did}`) |
| `decisions[].rev` | the row's `rev` | whether a card already read is of the decision as it is now |
| `cut?` | `true` when a 201st admitted decision was met, or the read reached its own bound first (below) | that more may await than it lists; omitted otherwise |

No title, question, run, ticket, session or person's name; no count
beyond the entries returned; no page and no cursor. At most 200
admitted decisions (`proto.MaxDecideListEntries`), as one reply of at most 16 KiB
(`proto.MaxDecideList`); the reply is one encoded value of whole
entries, never one split, and 200 at
their widest (an id of `dr-` and 19 digits, a `rev` of 19 digits: 58
bytes each with its comma) are 11,626 bytes with the envelope, so the
count is always the bound that cuts.

**Admission has one reader, and it is Go's.** No SQL of the list reads
a request or a policy: no JSON function is in its query. One ledger read,
`ledger.Ledger.OpenDecisionsNewest(ctx, limit int, visit
func(DecisionRow) bool) (more bool, err error)` (new), one statement in
one read transaction (`readSnapshot`, as `Decision` reads): `SELECT` the decision rows `WHERE
d.state = 'open' ORDER BY d.n DESC` (the row's number, the key every
decision read orders by, so the order is total), each row scanned by
`scanDecision`, as every read scans one, and handed to `visit` inside
that transaction, one at a time, newest first, nothing kept by the read.
It is bounded twice: at most `limit` rows (at most `ledger.MaxOpenScan`,
500, and 0 for that, as `ScopeDecisions` takes its limit), and at most
`ledger.MaxOpenScanBytes` (16 MiB) of them, the sum of the lengths of the
text `scanDecision` reads for each. A row past either bound is not handed,
and the read answers `more`: open decisions remain that it did not read.
`visit` answering false stops the read there, `more` false.

The route's `visit` judges each row with `crew.AdmissionOf` over
`ledger.DecisionRecord{DecisionRow: row}`, the judgement the one-decision
read makes (`decideAdmission`, I1), and keeps `{id, rev}` of the rows it
answers `AdmitOpen` for. A request `encoding/json` cannot decode, or a
policy it cannot read, gives no entry that admits a decide principal
(`crew.EntriesOf`), so such a
row is one more refused row, whatever SQLite would have made of its text,
and the rows after it are read on. An open decision's admission reads its
row alone (its state, its policy, its request's `for`): no response,
history or hold enters `AdmitOpen`, so judging the row is judging the
record (`TestAdmitsIsTheOneCheck` holds them equal), and the list and the
one-decision read cannot disagree, whatever members a request's text
repeats, capitalises or escapes.

**The limit and the cut count admitted rows only.** The reply holds the
first 200 admitted decisions the read hands, newest first; `visit` stops
the read at the 201st admitted one. `cut` is true when a 201st admitted
decision was met, or when the read answered `more` (its own bound reached
before the open decisions were exhausted); omitted otherwise. A refused
row never takes a place in the reply, and sets `cut` only by taking a
place in the read's bound (What remains, below).

**What remains, in words.** The one thing decisions that do not admit
the caller can change is how far back the list looks: it looks at the
newest 500 open decisions of the server (fewer when those rows hold more
than 16 MiB together), the operator's own among them. On a server with
more open decisions than that, a decide principal's list can miss an
older one that admits it, and then says `cut`; that decision's card is
still read by its address (`/decide/<did>`), which the read judges
alone. Nothing else about such decisions can change the reply: below the
bound, adding them leaves it byte for byte the same.

A read the ledger cannot make answers as `GET /api/decisions` answers one
(`taskError`), naming no decision.

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

The decide list's two fields are this table's first two rows, `id` and
`rev`, from the same columns, and nothing else of the table (The decide
list); `TestDecideViewIsTheTable` walks `proto.DecideEntry` against
them, so one table names every field a decide read returns.

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
its code (`decideWords`, at most `proto.MaxDecideRefusalWords`, 200
bytes, naming no one) and its
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

`crew.AdmissionOf(rec, who Admittee) Admission` is the one judgement,
over the record a route read; `Admission` is its answer, one of four:

- `AdmitWhole`: `who` holds the admin grant (the workbench token, or a
  provider's principal with `admin`): DR1's routes as today.
- `AdmitOpen`: `who` holds `decide`, an entry admits it (the route target is
  the decide grant) and the decision is `open`: the decide view whole.
- `AdmitOutcome`: `who` holds `decide`, an entry admits it, the decision is
  `answered` or final, and it has a response of its own: the outcome
  view.
- `AdmitNone`: anything else, and every decision that does not exist:
  `404 not_found` with the same body for both.

So admission ends:

- **with the answer**, for every holder of the grant without a response
  of its own: once dr-7 is answered, a later pass, or walking the `dr-N`
  ids, finds nothing, and the decide list no longer holds it (it lists
  open decisions only; the responder reads its outcome by the address);
- **with the take-back**, for everyone: while dr-7 is open, the
  operator's route to the operator takes the target off the grant, at
  the `expectedRev` it read, as every route is judged, held run or not,
  and one operation is always left for it (a route to a person or a role
  replaces the target too, through the held run, as HM1's does; The
  changes, `internal/crew`); the next list read holds it no more;
- **with the pass**, when the provider stops admitting it: the next
  request is `401`.

A take-back and an answer meet in one row, and whichever commits first
decides:

| Order | The operator sees | The person sees |
|---|---|---|
| take-back, then the response's transaction | the take-back's receipt (`rev` + 1) | its response refused `403 not_approver`, kept as its own receipt; its next read `404`: "dr-7 is no longer open to you." |
| the response's transaction, then the take-back | `409 stale_decision`: the take-back carries the `rev` it read, which the answer moved, and the transaction judges the revision before the state (`operate`'s `staleOp`); read again and sent at the answered `rev`, `409 decision_answered` (whom it is for changes only while it is open). The answer goes on to its recheck and application, and the operator may still cancel it while it is `answered` (DR1's cancel) | its receipt `accepted`; the outcome `waiting`, then `applied` or `not_used` |

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

**What an open page shows.** The server, not the page, refuses an
answer after a take-back, an answer or a pass's end: the response's
transaction judges the entries it reads, and every request asks the
provider. The page's rule is about what a person is shown, never about
what can be written, and no time is promised from a take-back to a
closed card. It is one property the page holds by itself: **the card can
be answered only on a fresh view.** The page marks each read with the
moment it began and keeps the mark of the last read whose reply the
card took;
while that mark is older than `DECIDE_FRESH_FOR` (25 s) the card is
**suspended**: its choices and a new answer disabled (an answer whose
outcome is not known is sent again whatever the view's age), the draft
and the answer sent kept, and the page says "Checking that dr-7 is still yours
to answer." The next read whose reply it takes makes it answerable
again; a `404` closes that card (the draft kept, the pass and the other cards
untouched) and a `401` closes the page (the pass it was sent with ended, every draft
kept). A card's reads go
one at a time, every `DECIDE_READ_EVERY` (10 s) while shown, each
aborted at `DECIDE_READ_WAIT` (10 s), so a read that succeeds at its
bound replaces the view before its mark is `DECIDE_READ_EVERY` +
`DECIDE_READ_WAIT` old, inside the window: one slow read does not
suspend the card. Reads that fail (a timeout, a `5xx`, no network)
suspend it once the mark passes the window, and it stays suspended until
one succeeds, however many fail.

Every reply a card meets, a read's or a respond's, passes one order
before it draws anything: the card keeps the highest `rev` it has shown
and whether it has reached an end for this person, and a reply of a lower
`rev`, or a read's reply that arrives after that end, is dropped. It draws
nothing and renews no freshness, so a slow read cannot reopen a card its
own answer closed (The changes, `web/app`: One order for a card).

The decide list is read under the same rule, with a mark of its own: one
read at a time, on the same interval, each bounded the same; while its
last good read is older than `DECIDE_FRESH_FOR` the list is suspended
(its rows drawn as last read, no card opened from them, the page saying
"Checking which decisions await you here."), and a `401` closes the
page. The list never makes a card answerable: each card is answerable
only on its own fresh view, since only the card's read judges that
decision whole. A list read whose `rev` for an open card is not the
card's view's reads that card at once; a decision that leaves the list
between two reads has its card read once more at once, which a refusal
closes (`404`, any unsent answer and the draft kept, as above) or the
outcome of its own answer replaces.

## The changes

### `ext`

- `identity.go`: `GrantDecide Grant = "decide"`, documented as "answer
  a decision routed to the grant, and read nothing else". `Grant.Valid`
  accepts it; `Principal.Valid`'s refusal words name the three ("is not
  admin, host or decide").
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
- `DecideView` (the table above), `DecideOption{Label, Tradeoffs}`,
  `DecideReceipt` (`own`'s entries, `responseId` among them),
  `MaxDecideView = 32 << 10`: the request's fields at most
  `ledger.MaxDecisionRequest` (16 KiB) as the ledger keeps them encoded,
  8 receipts of at most 256 bytes (a `responseId` of 32 bytes among
  them), the rest under 1 KiB.
- `DecideReply{Receipt DecideReceipt; Decision *DecideView}` and
  `DecideRefusal{Error{Code, Message}; Receipt *DecideReceipt}`, with
  `MaxDecideReply = MaxDecideView + 1<<10` and `MaxDecideRefusalWords =
  200`, the bound of `decideWords`: a name of its own, since the
  decision links' `MaxDecideWords` (500, `decisions.go`), which
  `lane.go` fits a `decide_refused`'s message to, is untouched.
- `DecideList{Decisions []DecideEntry; Cut bool}` (`json:"decisions"`,
  `json:"cut,omitempty"`), `DecideEntry{ID string; Rev int}` (`json:"id"`,
  `json:"rev"`), `MaxDecideListEntries = 200` and `MaxDecideList = 16 <<
  10`, the issue's bounds (The decide list: 200 entries at their widest
  are 11,626 bytes).
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
  target is the grant, read through `grantFor` (below), the one reader of
  the target; a policy it cannot read gives no entries, so it admits
  nobody, and a request `encoding/json` cannot decode gives no grant
  entry. `AdmissionOf(rec, who)`, answering the type
  `Admission` (`AdmitWhole`, `AdmitOpen`, `AdmitOutcome`, `AdmitNone`), is
  `Admits` over `EntriesOf` plus
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
  `grantFor(request) bool` reads the target, decoding the request with
  `encoding/json` as `checkRequest` does (false for text it cannot
  decode); `GrantDecideLabel` (`"grant:decide"`) is the one place the
  target's label is spelled. `requestFor` keeps
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
- `ledger/decisions.go`: `OpenDecisionsNewest(ctx, limit int, visit
  func(DecisionRow) bool) (more bool, err error)` (new), the decide list's
  one read (The decide list): one statement in one `readSnapshot`, its
  condition the state `open` alone (no JSON function), newest first by
  `n`, each row scanned by `scanDecision` and handed to `visit` inside the
  read; at most `limit` rows (`MaxOpenScan`, 500, for 0 or more) and
  `MaxOpenScanBytes` (16 MiB) of their text, taken from the package's
  `openScanBytes` so the ledger's own test lowers it, `more` when a row
  past either bound is left unread; `visit` false stops it. `visit`
  reads nothing of the ledger (its one connection is the read's): the
  list's is `crew.AdmissionOf` over the row, which reads nothing. No
  hook inside the read: the ledger has one connection, so a write waits
  for a read transaction to end, and a test holds a list read after it
  (`afterDecideList`, `internal/api`).
- No ledger schema change and no migration: the `for` is a field of the
  request's JSON, of the shape HM1's `forShape` checks (a label, no
  person). An older binary reading a grant target treats it as a label
  naming no person, checks only the admin grant, and so refuses a decide
  answer `not_approver`; it supersedes an identity's answer whose `boot`
  is another process's: both fail closed.

### `internal/api`

- `auth.go`'s `principal` gains `decide bool`; `identity.go`'s
  `grantPrincipal` sets it for `ext.GrantDecide` and sets `host` for
  `ext.GrantHost` as today, so a principal holding both is a host and a
  decide principal (The decide principal's surface). `requireAdmin` is
  unchanged, so a decide principal is `401` on every admin route, `GET
  /api/events` among them (its check is `p.admin`).
  `decideAdmission(rec, p principal) crew.Admission` (new) is
  `crew.AdmissionOf` over the record and the principal as an
  `Admittee`; the read and the respond route call it and nothing else,
  and the list calls it over each row the ledger's read hands its
  `visit`.
- `me.go` (new): `GET /api/me`, any authenticated principal:
  `proto.MeReply`, at most `proto.MaxMeReply`: the workbench token
  `["admin", "host"]`, a host token `["host"]`, a share link `[]`, a
  provider's principal its subject, name and grants; anything else
  `401` under the rate limit. Not refused on a switchyard (the workbench
  there is `["admin", "host"]` too; its lack of a role is `whoami`'s to
  say).
- `decisions.go`: the three decision routes of the surface (the list,
  the read, the respond route) are wrapped in
  `deciding` (new), which authenticates first, as `launching` does
  (`requireAdmin` around `launchingAny`, `server.go`): a principal that
  is neither the admin nor a decide principal (`p.decide && !p.admin`,
  whatever else it holds), a host-only one and no credential among them,
  `401` under the rate limit, as on `main`, switchyard or not; then, on a
  switchyard, `403 switchyard` for the admin and for a decide principal
  alike (`launchingAny`'s answer); else the admin exactly as
  `launching` passes it and a decide principal through to the handler.
  `GET /api/decisions` and the operator's operations stay `launching`.
  - `handleDecideList` (new), `GET /api/me/decisions`: for a decide
    principal, one `s.ledger.OpenDecisionsNewest(ctx, limit, visit)`
    (`limit` 0 in the product; a test seam, `decideScanLimit`, as the
    lane's `scopeLimit` stands in for `ledger.MaxScopeDecisions`, so a
    test reaches the bound with a few), whose
    `visit` keeps `{id: ledger.DecisionID(n), rev}` of each row
    `decideAdmission` answers `AdmitOpen` for, skips every other row, and
    answers false at the 201st admitted one
    (`proto.MaxDecideListEntries` + 1); its reply `proto.DecideList`, the
    first 200 kept, `cut` when a 201st was met or the read answered
    `more`, written with `writeJSON`; the count is the bound that cuts,
    and `TestDecideBounds` proves 200 entries at their widest
    fit `proto.MaxDecideList`, as `TestTheOperatorsDecisionListFitsItsBound`
    proves the operator's page. For the admin (whom
    `deciding` passes as `launching` does) `403 not_decide` ("this list
    is the decide grant's: the operator reads GET /api/decisions"),
    nothing read. A ledger read that fails answers as `GET
    /api/decisions` does (`taskError`). `afterDecideList` (a test hook,
    nil in the product, as `afterDecideRead` is) holds a list read
    between its ledger read and its reply.
  - `handleDecision`: for a decide principal, one read of the record
    whole (`s.ledger.Decision`), then `decideAdmission` over that record, then
    the reply built from that same record: `AdmitOpen` or `AdmitOutcome`,
    `decideView` (the version judged is the version written); `AdmitNone`,
    `404 {error: {code: "not_found", message: "no such decision"}}`,
    the body a decision that does not exist gets for a decide principal.
    `afterDecideRead` (a test hook, nil in the product) holds a read
    between its record read and its reply. The admin's read is as today.
  - `handleDecisionRespond`: for a decide principal, one read and
    `decideAdmission` first (`AdmitNone`: the same `404`, nothing written), then
    DR1's transaction, whose `Admits` over the entries it reads decides.
    Its reply is `proto.DecideReply`: its own receipt reduced (its
    `responseId` the body's), and the decide view of the record read
    again after the commit, judged again (omitted when that read is
    `AdmitNone`); a refusal `proto.DecideRefusal` with `decideWords(code)`.
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
- `server.go`: `GET /api/me` registered; `decisions.go`'s
  `routeDecisions`, where the decision routes are registered today, adds
  `GET /api/me/decisions` through `deciding` and registers the two
  decision routes of the surface through it.

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
  href}`, the URL with no fragment when it carries either key in any
  spelling, whether or not their values were taken (neither value
  taken when it holds both keys); `decideRedirect(path,
  hash, hasToken, hasPass)`, the middleware's rule as a pure function.
  `tokenFromFragment` (`utils/desktop.ts`) keeps its meaning and its 16
  to 256 characters.
- `composables/useIdentityPass.ts` (new): the pass store,
  `sessionStorage['conductor.identityPass']` (the tab's; in memory when
  storage is refused): `set` (a load's pass replaces the old one);
  `stored(pass)`, a later link's, which writes the tab's storage alone
  (what the page holds and sends stays as it is) and answers true only
  when the storage took a pass that differs from the one it held; and
  `end(refused)`, the pass a `401` refused, which empties the page's
  pass when it is that pass, removes the stored one only while the
  storage still holds that pass, and marks the tab's state `ended`. It
  never reads or writes `conductor.workbenchToken`. A pass is held no
  longer than its tab, and not past the first `401` it meets.
- `plugins/token-fragment.client.ts`: `takeCredentials(location.href)`,
  then `history.replaceState` with the URL it answers, before the app's
  router reads the address; then, at a load, the token into the
  workbench store as today, and the pass into the pass store (in
  memory too, where the browser refuses storage). A refused value is
  dropped from the address bar and stored nowhere. A navigation the browser
  starts (a load, a fragment change, back or forward) never lets the
  app's router read a fragment that carries either key, in any
  spelling: at a load the plugin runs before the router (`order: -25`,
  ahead of Nuxt's `nuxt:router` at `enforce: 'pre'`, -20); later, its
  capture listener of the window's `popstate`, registered in its setup
  before the router exists, takes the event first, rewrites the address
  without the fragment, ends the event there
  (`stopImmediatePropagation`: the router's listener does not run for
  it), and writes what the fragment carried to the browser's storage
  for it alone (`stored`: the tab's for a pass, the workbench's for the
  token), never to what the running page holds and sends; it loads the
  page again at the clean address (`location.reload()`) only when that
  write was taken and changed what the storage held. A fragment that
  takes nothing, one that carries the credential the storage already
  holds, and a write the browser refuses leave the page as it is,
  drafts and an answer held with it; a load drops what the page held
  (drafts, an answer whose outcome was not known). A fragment with no
  key, that can be read, is left to the router. A fragment that holds either key, its keys
  read as the browser reads them and again with each layer of escapes
  taken off, or that cannot be read so, is dropped whole, and one that
  holds both keys gives neither; the middleware below applies the same
  rule to every navigation. What is not closed: a navigation the app's
  own code starts with such a fragment is sent on by the guard, and the
  router's record of where it was sent from holds that fragment until
  the next navigation; no code of the app makes one. Where the browser
  refuses the tab's storage, a link opened in a tab that already shows
  the page is not taken: load it in a new tab.
- `middleware/decide.global.ts` (new), synchronous over the stores, and
  on the first navigation over `to.hash` too (`takeCredentials`), so it
  judges right whether it runs before or after the plugin: a workbench
  token held, `/decide/<did>` goes to the operator's
  `/decisions?item=decision:<did>` and `/decide` to `/decisions`; no
  workbench token and a pass held, the operator page's own address of
  one decision, `/decisions?item=decision:<did>`, goes to `/decide/<did>`
  (so a link that names a decision opens that decision whichever of the
  two a person holds; an `item` that is not a decision's goes to
  `/decide`), and every other path but `/decide/**` goes to `/decide`.
  Nothing of the
  workbench's layout or pages is set up before it has run, and it waits
  on no request.
- `composables/useDecide.ts` (new): the decide page's client and state,
  page-owned:
  - `me()`, `list()`, `read(did)`, `respond(did)`, each with
    `Authorization: Bearer <pass>`, each aborted after `DECIDE_READ_WAIT`
    (10 s); a `401` from any of them ends the pass it was sent with
    (`end(refused)`; `call` keeps the pass it sent) and closes the page
    (`ended`); a `404` of a card closes that card
    alone (`gone`), the pass and the other cards untouched; any other
    refusal is its code's words.
  - **The list and the cards.** `list()` reads the decide list; the
    page keeps one card's state per decision it has drawn (`view`,
    `draft`, `sent`, its marks), in the page's memory. At most
    `DECIDE_CARDS` (4) cards are **open** at once, each reading as
    below: the decision of the address first when there is one, then
    the list's newest; the list's other entries are rows ("dr-12 awaits
    your answer.") with Open, disabled while four are open ("Four are
    open: close one to open another."). A card offers Close only while
    it holds no draft and no answer whose outcome is not yet known, so
    no draft is ever put out of sight; a closed card's state is kept, so
    Open again draws it as it was and reads it at once. A card closed by
    a refusal (`gone`) or at a final outcome reads no more and is not
    counted among the four; Dismiss drops it and everything the page
    held for it: its reads, its freshness timer and its send on the
    way, each kept by the card object and never found again by the
    decision's id. A reply acts only on a card that is still the
    page's (the object in `page.cards`), checked after every await of a
    card's read and of a send beside the page's own; so a dismissed
    card can stop, end or change nothing, also when the same decision
    was opened again as a new card. A card that ended is kept until
    dismissed, and a closed card is kept and cannot be dismissed (Open
    draws it again), at most one card per decision the list named (and
    the address's). Open asks, at that moment, the one judgement of the
    list's freshness on both clocks (below), as the list's own opening
    of cards does, and opens no decision whose card has ended. A list
    reply of more than
    `MAX_DECIDE_LIST_ENTRIES` (200) entries is past its bound: a failed
    read, none of it kept. A list read whose `rev`
    for an open card differs from its view's reads that card at once; an
    open card whose decision is no longer listed is read once at once,
    whatever is due. An empty list, with no card open: "Nothing awaits
    your answer here." A list whose reply says `cut`: "More may await
    your answer than this page lists; a decision's own link opens it."
  - **Reads.** The list and each open card read on schedules of their
    own, each the same: one read at a time (one `list()`, one `read(did)`
    per card); the next is due
    `DECIDE_READ_EVERY` (10 s) after the last began while
    `document.visibilityState` is `visible` (none while hidden), and at
    once on `visibilitychange` to visible and on the window's `focus`; a
    read that falls due while one is out is not stacked, and starts the
    moment that one returns. A card's reads stop at `applied`,
    `not_used`, `ended`, `gone` or its Close, its read on the way
    aborted; the list's at `ended`. A read that falls due while the tab
    is hidden stays due until it is shown, and so does the list's
    second request when the tab was hidden while `/api/me` was out
    (nothing is said as failed for it). One answer is on its way at a
    time for the whole page: while any card is sending, no other card's
    send starts. So a tab has at most six requests out at once, the
    list's read, four cards' and one answer, well within the 16 of one
    subject's requests the server works at once (PR-1's third part,
    #868). The schedules are measured on the steady clock
    (`performance.now()`). A read refused `403` with a code (a server
    that serves no decisions to this pass), to `/api/me` or the list,
    says so in fixed words and stops that schedule. A read that fails
    before it ever succeeded says so in fixed words where its result
    would be shown and is tried again: who the pass is ("Your access
    could not be checked. Trying again."), the list (under "Signed in
    as"), and a card's first read ("dr-7 could not be read. Trying
    again."); the first success clears it. A read whose own signal was
    aborted (Close, Dismiss, the card's end, the page's stop) says
    nothing, a failure included. Once the page stops, every
    request on its way is aborted and no reply changes it. A `401` ends
    the page in the one function that sends the pass, before its body is
    read. A view is taken only when every field the card draws or a send
    reads has the type the protocol names (`isView`: its options a list
    of objects each with a text label, `createdAt` and the other words
    strings, `recommendation` an integer, `own` a list of receipts each
    with a text `responseId` and `status`); anything else is a read that
    failed, and nothing the card shows changes. A card draws a `type` only when it is one of the three the
    protocol names, and its "Asked" line only when `createdAt` parses;
    nothing a reply carries makes a card wider than the page.
  - **One order for a card, whichever reply brings it.** A card keeps
    `view`, the view it shows (the highest `rev` it has shown), and `end`,
    set once it has reached an end for this person (`applied`,
    `not_used`, `gone`, `ended`). A reply from either path, a read's or a
    respond's, is taken only through `replaces(view, reply, whole)`
    (`web/app/utils/decisions.ts`), the function the operator's card
    orders its record by on `main` (`DecisionRecordView.read` calls it with
    `whole` true, `.write` with `whole` false, `composables/useDecisions.ts`),
    called the same way: a read's reply with `whole` true (a card's reads
    go one at a time, so one of the same `rev` is the later one), a
    respond's with `whole` false. A reply it refuses (a lower `rev` than
    shown, or a respond's of the `rev` shown), and any read's reply that
    arrives once `end` is set, is dropped: it draws nothing, moves no mark
    and renews no freshness. So a read begun at open `rev` R that returns
    after the respond's reply drew the answered `rev` R + 1 draws nothing,
    and the card stays answered. A reload starts the card anew.
  - **Freshness** (What an open page shows). `freshAt` and `freshWall`,
    the moment the last read whose reply the card took began on the
    steady clock (`performance.now()`) and on the wall clock
    (`Date.now()`), taken before its request is sent; the card is fresh
    only while the time passed on each is between zero and
    `DECIDE_FRESH_FOR` (25 s), and `suspended` otherwise (the steady
    clock does not advance while a device sleeps, and the wall clock can
    be set: each alone can call an old view fresh), judged at every
    send, on a timer armed at each success for the moment its mark turns
    that old, and at once when the tab is shown again. A new answer is
    sent only on a fresh view; an answer whose outcome is not known is
    sent again whatever the view's age. Suspended, its choices and a new
    send are disabled (Send again of an answer held is offered as its row
    of the table below says), `draft` and `sent` kept; a respond's reply
    the card takes draws its view and renews no mark. A reply that is
    `taken` clears the card's marks, so no new answer is sent until a
    read made after it renews the view: the read on its way, begun
    before, is aborted, and one starts at once, whether or not the reply
    carried a view.
    The list keeps a `freshAt` and a `freshWall` of its own under the
    same rule, judged by one function at the moment a card is opened
    from it, by Open and by the list's own opening alike: while it is
    stale its rows are drawn as last read, Open disabled, no card opened
    from it, the page saying "Checking which decisions await you
    here."; it never makes a card answerable or not, which only the
    card's own mark does.
    Its timers go through `setTimeout`, so
    vitest's fake clock (`vi.useFakeTimers()`, as
    `useDispatchLive.test.ts` drives one) moves them; Playwright drives a
    read by the focus event (its 1.44.1 has no clock).
  - `view`, the last decide view the card's order took; a refused send
    never clears it.
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
    | `201`, or `200` repeat, whose receipt names `sent.responseId` | kept | cleared | the receipt; the card not fresh (its marks cleared, no new answer) until a read made after it, which starts at once, renews the view; then the outcome from that read | `TestDecideOwnCarriesTheResponseID` (Go); `useDecide.test.ts`: a receipt with no view, the read at once, a read that fails keeping it so |
    | a definite refusal (a `4xx` with a code that judges a response, `DECIDE_JUDGING` in `utils/decisions.ts`, the server's own list: `decision_answered`, `decision_closed`, `stale_decision`, `not_approver`, `response_conflict`, `not_held`, `stale_policy`, `invalid`, `decision_limit`) | kept | cleared: its id is done with (a kept refusal is replayed for it, another body refused `response_conflict`) | its code's words; the next Send is a new attempt, with a new `responseId` and the latest `view.rev` | `TestDecideOwnCarriesTheResponseID`: after a kept `stale_decision`, the same id with the new rev `response_conflict`, a new id accepted; `useDecide.test.ts`: a new id after a refusal |
    | no definite answer (anything else: no reply, an abort, a `5xx`, `503 decision_moved`, a `429 rate_limited`, a `4xx` with another code or none, a reply past its bound, a `2xx` whose receipt names another response id or none) | kept | kept as it was | the app's own fixed words, "Not known whether your answer to dr-7 was recorded: send it again."; for `rate_limited`, "The server is working on other requests of yours: send your answer to dr-7 again in a moment."; Send again sends `sent` unchanged, never the draft. One rule (`answerable`) says whether it is offered: the page alive, the card the page's own, nothing of the page being sent, and the decision not answered `404` to this pass; a final outcome a read shows (`applied`, `not_used`) does not withdraw it, and the server's reply judges the send (its own receipt settles it, a judging refusal releases it with its words) | `TestDecideOwnCarriesTheResponseID`: the same body again `200` repeat with the saved receipt; `useDecide.test.ts`: the retry's body equals the first, after each final outcome too |
    | a read whose `own` holds `sent.responseId` | kept | resolved by that receipt (accepted, or the refusal row above) | as that row | `useDecide.test.ts`: a receipt of another `responseId` (another tab of the same subject) resolves nothing |
    | the view not fresh (`suspended`) | kept | kept | "Checking that dr-7 is still yours to answer."; choices and a new send disabled until a read succeeds (Send again of an answer held stays as its row says) | `useDecide.test.ts` |
    | `401` | kept | kept | "Your access to this server has ended. Open it again from your workspace page."; every send disabled; the pass it was sent with ended | `decide-only.spec.ts` |
    | `404` | kept | kept | "dr-7 is no longer open to you."; that card's sends disabled, and of an answer held, "Whether your answer to dr-7 was recorded cannot be learned here." (the words beside a held answer never ask for what the card does not offer); the pass, the list and the other cards untouched | `useDecide.test.ts` |

    A receipt is matched to a send by its `responseId` alone, from the
    same read as the rest of the view; a newer receipt of another id
    says nothing of a send. A send whose receipt has left `own`'s eight
    is resolved by Send again, which the replay answers.
  - `whenIdle()`, the promise of the work under way, the vitest's seam.
- `pages/decide/[did].vue` and `pages/decide/index.vue` (new), layout
  `bare` (`definePageMeta`), so no sidebar, palette, Needs you pill or
  token dialog is drawn: one page, `/decide` and `/decide/<did>` alike,
  the address's decision drawn first. The header "Signed in as <name>"
  (`me()`); a pass whose grants hold `admin` or lack `decide`: "This
  page answers a decision routed to you; this pass is not for it." The
  open cards one under another, as the operator's decision page draws an
  item's card today (one column on a phone), then the list's other
  entries as rows with Open, then, with no entry and no card, "Nothing
  awaits your answer here." Each card from its `view`: question, context, the options with their tradeoffs and the
  recommendation, uncertainty and what it authorizes; the answer as its
  type asks (Nuxt UI's `URadioGroup`, `UTextarea`, `UButton`), not the
  operator's `DecisionRespond`, which carries Comment and Ask for more.
  The words, each a `UAlert` above the card, the draft left as it is:
  - `401`: "Your access to this server has ended. Open it again from
    your workspace page." Every send disabled, above the page.
  - the list `suspended`: "Checking which decisions await you here.",
    above the rows; Open disabled.
  - the list `cut`: "More may await your answer than this page lists; a
    decision's own link opens it.", above the rows.
  - `404` after a view was drawn: "dr-7 is no longer open to you."
  - `suspended`: "Checking that dr-7 is still yours to answer." Choices
    and a new send disabled; Send again of an answer held stays offered.
  - `unknown`: the app's own fixed words, "Not known whether your answer
    to dr-7 was recorded: send it again."; for `rate_limited`, "The
    server is working on other requests of yours: send your answer to
    dr-7 again in a moment."; once the card is no longer open to the
    person or the page has ended, "Whether your answer to dr-7 was
    recorded cannot be learned here."
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
  `DecideList` and `DecideEntry` with `MAX_DECIDE_LIST` and
  `MAX_DECIDE_LIST_ENTRIES`, `DECIDE_CARDS`, `DecideView`, `DecideOption`, `DecideReceipt`, `DecideReply`,
  `DecideRefusal` with their bounds, the `outcome` words,
  `DECIDE_READ_EVERY`, `DECIDE_READ_WAIT` and `DECIDE_FRESH_FOR`, the route's `grant`
  destination and `no_identity`, beside the existing decision rows.

### `internal/proto`, `web/app/utils/protocol.ts`, `docs/protocol.md`

The triple together: the types and bounds above in `internal/proto`
(`decide.go`), their mirror in `protocol.ts`, and in `docs/protocol.md`:

- the HTTP API's opening paragraph: the decide grant, the four routes
  that admit it, every other route answering it as without the grant
  (a decide-only principal as no credential, `401`), and no live
  connection given by it;
- the Decisions table: a `GET /api/me` row; a `GET /api/me/decisions`
  row (the decide grant's alone: `{decisions: [{id, rev}], cut?}`, the
  first `MaxDecideListEntries` (200) open decisions whose entries admit
  the caller, newest first, at most `MaxDecideList` (16 KiB), no other
  field, count, page or cursor; `cut` when a 201st admitted one was met
  or the read reached its bound first; in words, the one thing that
  remains: the list looks at the newest 500 open decisions of the server
  (`MaxOpenScan`, fewer when those rows pass 16 MiB), so on a server with
  more open decisions than that a decide principal's list can miss an
  older one, and says `cut`; the admin `403 not_decide`, a host-only
  principal and no credential `401`, on a switchyard too, an admitted
  principal there `403 switchyard`); `GET /api/decisions/{did}`'s
  decide view (its fields, `MaxDecideView`, the `404` for a decision not
  admitted); the respond row's decide reply and refusal
  (`MaxDecideReply`, `decideWords`); the route row's `grant`
  destination beside the shipped ones, the take-back, the operation kept
  for it, Brakeman's refusal and `409 no_identity`; the decide page's
  reads (the list's and each open card's, at most four cards open, each
  one at a time, every 10 s while shown, each bounded at 10 s) and its
  freshness window (a view not renewed for 25 s cannot be answered).
  The shipped bounds
  are kept: the record's read at most 2 MiB (`maxDecisionReply`), a
  response's body at most 32 KiB (`maxDecisionRespondBody`);
- the `GET /api/inbox/item` row: its answerer words naming the grant
  entry. The inbox's three rows otherwise unchanged: they stay `admin`,
  a decide principal `401` as no credential;
- the fragment key `pass` beside `token` (2 KiB of `[A-Za-z0-9._-]`,
  dropped from the address bar, kept for the tab, never sent in a URL).

### Docs

`docs/features.md`: the row's entry. `docs/repository-map.md`: the
`internal/api/decisions.go` row names the decide view, the decide list
and `decideAdmission`; the `ext` row names three grants.

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

**A decide principal's response.** The route's read and `decideAdmission`
(`404` when `AdmitNone`), then DR1's transaction, whose policy check is
`Admits` over the entries it reads: a take-back committed in between is
`not_approver`, kept; a take-back after the commit changes nothing,
refused `stale_decision` at the rev read before the answer and
`decision_answered` at the answered rev (Admission and its end). An identity's response to a
run not held is `not_held`, as today. After the commit,
`recheckResponse` asks the provider through the guard (bounded by
`ext.IdentityWait`) and calls `ConfirmAuthority` with `admitted` true
only for the same subject still holding the grant its authority names.
A provider that fails, is late or is away has not said yes: the decision
is superseded, the replacement opens for the operator alone, and the
person's next read says `not_used`.

**A decide principal's reads.** Each is one ledger read, judged and
built from that read: a decision's read one record read whole, the list
one bounded statement over the open rows, newest first, each row it
hands judged in Go inside that read by the same `AdmissionOf`; no reply
is written from a judgement of another read, and nothing is pushed. A list read and a
take-back meet in the ledger's one snapshot: a list read whose snapshot
was taken before the take-back committed lists the decision at the rev
it judged, and the next read does not; the card read that follows is
judged again and refused. The page asks again on its
interval and when shown again; the server keeps nothing for it between
requests.

**What a pass meets when it ends.** Every request asks the provider
(C2's rule), so the first request after the end is `401`. A pass a
provider no longer admits is never cached by the server.

Invariants:

- **I1, one judgement.** Every decision of whether a principal may read
  or answer a decision is `crew.AdmissionOf` (with `crew.Admits` over
  `crew.EntriesOf` inside it) over one record: the read, the list (over
  each row the ledger's bounded read hands it, in Go; no SQL of it reads
  a request or a policy), the respond route's check and the response's
  transaction (`Admits`). The recheck before application reads no
  record, since the entries cannot change once answered (I7); its
  answer is `Admits` over the entries the response was judged by, for
  the same subject holding the grant the response's authority names.
  `TestAdmitsIsTheOneCheck` holds all of them to one table.
- **I2, the four routes.** A decide principal is answered by every
  route outside the four exactly as it is without the decide grant (a
  decide-only one as a request with no credential),
  and is written no byte of a decision `crew.AdmissionOf` answers `AdmitNone` for,
  nor its id in a list.
- **I3, the table.** What a decide principal reads of a decision is
  `proto.DecideView`, every field named in The decide view's table,
  built from the one record its route read; what it reads of the
  decisions that admit it is `proto.DecideList`, `id` and `rev` of each
  and the `cut` flag, The decide list's table.
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
  /api/inbox/item`, `GET /api/inbox/track`) and the operator's decisions
  list (`GET /api/decisions`), their counts, pages and cursors; anything
  of a listed decision beyond its `id` and `rev` (its title, run, ticket,
  session or person); answered or final decisions in the list (the
  responder reads its outcome by the decision's address); a stream across
  decisions or of one (Owner decisions, 16); Brakeman's card; and evidence
  refs resolved for a decide principal. The decide list is the one read
  across decisions the grant gives (Owner decisions, 1).
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
held after its record read by `afterDecideRead`, a list read held
after its ledger read by `afterDecideList`, the page
through `useDecide().whenIdle()`, vitest's fake clock and a fake fetch
whose replies the test releases.

Go race, every one in PR-1 (`go test -race -count=1 -timeout 60m ./...`, with `make lint`,
`make lint-static`, `make vuln`):

- `ext`: `TestGrantDecideValid` (`decide` valid, alone and beside
  `admin` and `host`; given twice refused; an unknown grant's words name
  the three).
- `internal/proto`: `TestDecideBounds` (a `MeReply`, a `DecideView`, a
  `DecideReply` and a `DecideRefusal`, each with every field at its
  bound and of characters `encoding/json` escapes to six bytes, fits its
  constant; a `DecideList` of `MaxDecideListEntries` entries at their
  widest, ids and revs at their widths and `cut` set, fits
  `MaxDecideList`).
- `internal/crew`:
  - `TestAdmission` (the table: admin, decide, admin and decide, host and
    decide, none, a router,
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
    answered one, at the rev it had before the answer `stale_decision`
    and at the answered rev `decision_answered`, nothing changed; a route to a
    person replaces the grant).
  - `TestTakeBackKeepsItsSlot` (a decision with 30 operations: the route
    to the grant taken as the 31st, a comment then refused
    `decision_limit`, the take-back taken as the 32nd; one with 31: the
    route to the grant refused).
  - `TestDecideResponseUnderTheTarget` (a decide authority accepted with
    the grant's target, `not_approver` without it; a response held at
    `beforeRespond` while the take-back commits: `not_approver`, kept; a
    response committed first: the take-back sent at the rev both read
    `stale_decision`, sent again at the answered rev `decision_answered`,
    the answer applied).
  - `TestReplacementIsTheOperators` (an identity's answer not confirmed:
    the replacement's request has no `for`, and a decide principal is
    `AdmitNone` on it).
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
    too) asked with a decide-only pass, a host-and-decide pass, a
    host-only pass and no credential, the rate limit raised for every
    walk: the four routes (`GET /api/me`, `GET /api/me/decisions`, the
    read and the respond route) answer both decide principals alike; every
    other answers the decide-only pass exactly as no credential and the
    host-and-decide pass exactly as the host-only one (status and body),
    `GET /api/events`, the inbox's three routes, `GET /api/decisions` and
    `/ws/host` among them.
  - `TestMeAnswersEveryPrincipal`: workbench, host token, link, a
    provider's principal with each grant and with host and decide, none
    (`401`); a subject and a
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
    `DecideReceipt`'s fields by reflection against the table's names, and
    `DecideEntry`'s against its first two (`DecideList` holding only
    `decisions` and `cut`), so a field added without the table fails.
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
    `not_used`; a take-back sent at the answered rev while the recheck is
    held at the gate: `decision_answered`, and once released the answer
    applied.
  - `TestDecideReadsAskTheProvider` (for the issue's
    `TestPrincipalWatchWatchesDecide`, Owner decisions, 16): a pass the
    fake stops admitting (`Remove`) is `401` at the next list read, the
    next read and the next response, nothing cached; the decide grant
    gives no connection the recheck would watch.
  - `TestBrakemanRouteKeepsOffTheGrant`: a turn's route to the operator
    admitted at rev R and held at `atActSeam("pending")`, the operator's
    route to the grant committed (R + 1), the act released: `409
    stale_decision`, the target still the grant; a turn's route of a
    decision given to the grant, to any destination, `409 stale_target`;
    `to: grant` from a turn `400 invalid`.
  - `TestAdmitsIsTheOneCheck`: one table of records (target, state, own
    response) against admin, decide, admin and decide, host and decide,
    none and a router: the read's
    answer, the list's (a row listed exactly when `crew.AdmissionOf` is
    `AdmitOpen`, and `AdmissionOf` over the row alone the same as over
    the record whole for every open row), the respond route's check and
    the response's transaction agree with `crew.AdmissionOf` on every
    row; and, for each row with an accepted response, the recheck's
    `admitted` (`ConfirmAuthority`'s answer, the issue's fourth caller)
    for the principal the fake provider answers again is `crew.Admits`
    over the entries that response's transaction judged, restricted to
    the grant its authority names, so the recheck and the one function
    cannot disagree though the recheck reads no record (I7).
  - `TestDecideListCarriesOnlyAdmittedDecisions`: decisions given to the
    grant (open, answered by this subject and by another, cancelled),
    open ones not given to it (the operator's, an HM1 person's), one
    given and taken back, and a row given to the grant whose policy
    cannot be read; sessions started, activity, chat and other decisions
    changed meanwhile, each carrying a marker string: listed as a decide
    principal (alone, and with host), the reply, decoded with
    `DisallowUnknownFields` into `proto.DecideList`, holds exactly the
    open admitted ids at their current revs, newest first, no `cut`, and
    none of the markers.
  - `TestDecideListTakeBackBetweenReads`: dr-7 and dr-8 given to the
    grant; a list read held at `afterDecideList` while the operator takes
    dr-7 back, the read released: it lists dr-7 at the rev it judged;
    the next read lists dr-8 alone, and dr-7's read is `404`; dr-8
    answered by another holder: gone from the next list; a route to the
    grant of a third: in the next list at its new rev.
  - `TestDecideListBound`: 201 open decisions given to the grant (made
    in several runs, `MaxOpenPerRun` being 64): the 200 newest listed,
    `cut` true, the oldest absent; with 200, all listed and no `cut`; the
    reply within `proto.MaxDecideList`, each entry whole (the reply
    decodes).
  - `TestDecideListCountsAdmittedRowsOnly`, at the product's bound unless
    it says otherwise: one reply taken, then refused rows added below the
    bound (open decisions of the operator's and of an HM1 person's, and
    one given to the grant whose request is `{`, written into the test's
    ledger as the row's text, since `checkRequest` refuses it on every
    path that writes one): the reply byte for byte the same; 200
    admitted and one refused row: all 200 listed, no `cut`; 201 refused
    rows newer than one admitted row: that one still listed, no `cut`;
    the undecodable row among admitted ones: skipped, every other listed,
    the list read and the one-decision read both answering (the read
    `404`); with `decideScanLimit` at 10, ten refused rows newer than an
    admitted one: it is not listed and `cut` is true, the case the words
    of The decide list name.
  - `TestDecideListAndReadAgree`: requests given to the grant written as
    text the decoder takes but SQL reads otherwise, each into the test's
    ledger as the row's text: `for` given twice (a person's label, then
    the grant's, and the other order), its members capitalised (`"For"`,
    `"Label"`) and escaped (`"\u0066or"`): for each, the decide principal
    finds it in the list exactly when its one-decision read answers the
    decide view, and the read is `404` exactly when the list leaves it
    out.
  - `TestDecideListRefusesOthers`: the admin (the workbench token, and a
    provider's principal with `admin` and `decide`) `403 not_decide`, a
    host-only pass and no credential `401`; on a switchyard, on the list,
    the read and the respond route: a host-only pass and no credential
    the `401` they get on `main` (the body `requireAdmin` writes), the
    admin and both decide principals `403 switchyard`; the decide-only
    and the host-and-decide passes answered alike.
- `internal/crew/ledger`: `TestOpenDecisionsNewest` (only open rows,
  newest first by `n`, each handed as stored, an undecodable request
  among them; `visit` false stopping the read with `more` false; `limit`
  rows then `more` true when an open row is left, `more` false when none
  is; `openScanBytes` lowered: the row past it not handed, `more` true;
  a ledger at a version before the decision tables hands none).

The issue's three stream tests, as reads (PR-1), so nothing of them is dropped
(Owner decisions, 16):

| The issue's test | The read tests that carry its assertion | What each asserts of it |
|---|---|---|
| `TestDecisionStreamCarriesOnlyAdmittedDecisions` | `TestDecideListCarriesOnlyAdmittedDecisions` (the snapshot); `TestDecideReadCarriesNothingElse` (a decision's data) | the list holds the admitted open decisions alone, `{id, rev}` and nothing else, while sessions, activity, chat and other decisions change; a read of one holds nothing of anything else |
| `TestDecisionStreamPolicyChangedDuringDelivery` | `TestDecideListTakeBackBetweenReads`; `TestDecideReadJudgesTheRowItReturns` | a read in flight across a take-back answers what it judged, the next read has nothing of the decision, and its card read is refused |
| `TestPrincipalWatchWatchesDecide` | `TestDecideReadsAskTheProvider` | a pass the provider no longer admits is `401` at the next request of every route of the grant; no connection is given for a watch to hold |

Vitest, every file in PR-2 (`npm --prefix web test`, with `npm --prefix web run typecheck`):

- `utils/identityPass.test.ts`: a 2,048-character pass with dots taken,
  2,049 refused, a character outside the set refused; both keys dropped
  from the fragment, the rest kept, a refused value dropped too;
  `decideRedirect`'s rules (a workbench token held, a pass held, both,
  neither; the first navigation's fragment; with a pass alone,
  `/decisions?item=decision:<did>` to `/decide/<did>` and an `item` that
  is not a decision's to `/decide`).
- `composables/useIdentityPass.test.ts`: the pass in `sessionStorage`,
  never `localStorage`; a stored workbench token never replaced; a new
  pass replaces the old; after `end()` the store is empty; storage
  refused, in memory.
- `composables/useDecide.test.ts` (a fake fetch whose replies the test
  releases, `vi.useFakeTimers()`, `whenIdle()`): every request to one of
  the four routes, the pass in its header and in no URL; a read due
  10 s of the fake clock after the last began while visible, none while
  hidden, one at once on `visibilitychange` and on `focus`; a read
  aborted at 10 s; never two at once, and one that fell due while
  another was out started the moment that one returns; two orders: a
  read begun before a take-back (its stub's view still
  open) released after its next read fell due, the card answerable on it
  while its mark is fresh, the next read started at once and its `404`
  closing the card; and reads that each time out, the card suspended once
  the mark is 25 s old (choices and a new send disabled, the draft kept),
  answerable again when a read succeeds, closed by a `404` or a `401`;
  one read that succeeds at its 10 s bound never suspends the card; a
  `401` from any route ends the pass it was sent with and closes the page, every draft
  kept; a card's `404` closes that card alone, its draft kept, the pass,
  the list and the other cards going on; the list: read on its own
  interval, suspended once its mark is 25 s old (rows drawn as last
  read, Open disabled) and never making a card answerable; the address's
  decision opened first, then the newest, at most four open and Open
  disabled at four, Close offered only on a card with no draft and no
  answer of unknown outcome, a closed card reopened as it was; a listed
  `rev` that differs from an open card's view reading that card at once;
  a decision gone from the list between two reads read once at once and
  closed by its `404` with its draft kept, or drawn with its own
  answer's outcome; an empty list's words; a `cut` list's words, and
  none without `cut`; the card's one order: an
  open read on its way, the respond's reply (the answered view at `rev`
  R + 1) drawn, that read aborted and the next one answered with the
  open view at R: the card stays answered, no choice offered, its marks
  cleared; a read's reply of a lower `rev` than shown dropped, one of the
  same `rev` taken; a respond's reply of the `rev` shown dropped; a read's
  reply arriving after the card's `404`, and after `applied`, drawing
  nothing; each row of the send table (a new `responseId` per send,
  `sent` frozen and resent byte for byte after no reply, a new id after
  a definite refusal, a receipt of another id resolving nothing); each
  `outcome`'s words; an assignment's options drawn as the view gives
  them.
- `utils/decisions.test.ts`: a record whose request's `for` is `{label:
  "grant:decide"}`: `approverWords` says "the operator, or anyone
  holding the decide grant"; `forWords`, `answerAsWords` and
  `assignmentWords` draw no "for".
- `utils/passSites.test.ts`: only `plugins/token-fragment.client.ts` and
  `composables/useDecide.ts` import the pass store (the app's sources
  read through `import.meta.glob`).

Playwright, in PR-2 (`make test-e2e`, after `npm --prefix web run typecheck:e2e`),
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
  pass in its header, no response `401` until the spec answers one, no
  request to an events stream; `/decide` with no decision's address and
  a stubbed list of dr-7 and dr-8: both cards drawn, newest first, from
  their reads; `/decide/dr-7` with a list of dr-8 and dr-7: dr-7 drawn
  first; an empty list: "Nothing awaits your answer here."; a list read
  driven by a `focus` event that no longer holds dr-8: dr-8's card read
  once, its stubbed `404` closing it with its draft still in its field
  and dr-7 still answerable; an assignment answered "Not done"
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

Two PRs, one after the other, since the row at about 810 + 1,320 lines
is past one PR's size. The plan's parts split cleanly at the app's
screens: everything a principal can do through the server is reached by
Go tests on the real handlers, with no page, and the page reaches the
server only through the four routes' wire, which `internal/proto` and its
mirror in `protocol.ts` fix in the first PR. Each PR is one branch
(`issue-N`, cut from `main` once what it depends on has merged), a draft
PR at its first commit, the checks of every surface it touches before
each push, its body ticking the open-core checklist, and each leaves
`main` working and green on its own.

- **PR-1, issue #776**: the server's half. On `main` after it, a
  provider's decide principal can be routed a decision, list it, read it
  and answer it through the four routes, and the operator can give and
  take back the grant; a `#pass=` fragment is still only an unknown
  token to the app's screens, which are unchanged. It closes #776, whose
  vitest and Playwright parts move to PR-2's issue.
- **PR-2, an issue to be cut when this plan merges**: the app's half,
  which needs nothing of PR-1 but its routes and its wire types.

Each test, bound and docs line of this plan lands in exactly one of them:

| | PR-1 (#776) | PR-2 (the app's issue) |
|---|---|---|
| Code | `ext` (`GrantDecide`); `internal/proto` (`decide.go`); `internal/crew` (`decisions_admit.go`, `decisions_for.go`, the take-back, the operation kept for it, `respondTx`, `replaceDecision`) and `internal/crew/ledger` (`OpenDecisionsNewest`, `Tx.Operations`); `internal/api` (`principal.decide`, `decideAdmission`, `me.go`, `deciding`, the list, the read, the respond route, `authorityOf`, `recheckResponse`, `no_identity`, `answererOf`, `guide.go`, Brakeman's refusal, `server.go`); in `web/app/utils/protocol.ts`, the mirror of `internal/proto` alone (`GRANT_DECIDE`, `MeReply`, `DecideList`, `DecideEntry`, `DecideView`, `DecideOption`, `DecideReceipt`, `DecideReply`, `DecideRefusal`, the `outcome` words, the route's `grant` destination and `no_identity`) | `web/app`: `utils/identityPass.ts`, `composables/useIdentityPass.ts`, `plugins/token-fragment.client.ts`, `middleware/decide.global.ts`, `composables/useDecide.ts`, `pages/decide/index.vue` and `pages/decide/[did].vue`, `utils/decisions.ts`; in `protocol.ts`, the page's own constants (`DECIDE_CARDS`, `DECIDE_READ_EVERY`, `DECIDE_READ_WAIT`, `DECIDE_FRESH_FOR`) |
| Bounds | `MaxMeReply`, `MaxDecideView`, `MaxDecideReply`, `MaxDecideRefusalWords`, `MaxDecideList`, `MaxDecideListEntries` (with their `protocol.ts` mirrors), `ledger.MaxOpenScan`, `ledger.MaxOpenScanBytes`, the operation of `MaxDecisionOperations` kept for the take-back | `MAX_PASS`, the four cards, the read interval, the read bound and the freshness window |
| Tests | every Go test of Tests per surface (`ext`, `internal/proto`, `internal/crew`, `internal/api`, `internal/crew/ledger`) and the stream-test map | every vitest file of Tests per surface and `decide-only.spec.ts` |
| Docs | `docs/protocol.md`: the HTTP API's opening paragraph, the Decisions table's rows (`GET /api/me`, `GET /api/me/decisions`, the read, the respond route, the route) and the `GET /api/inbox/item` row; `docs/repository-map.md`'s rows | `docs/protocol.md`: the decide page's reads and freshness window, and the fragment key `pass`; `docs/features.md`, the row's entry |
| Checks | Go (`make lint`, `go test -race`, `make lint-static`, `make vuln`); the web checks for its `protocol.ts` change (`npm --prefix web run typecheck`, `npm --prefix web test`, the e2e build and `make test-e2e`), with no web test of its own | the web checks (`typecheck`, vitest, `typecheck:e2e`, `make web-build` and `make build-go`, `make test-e2e`); Go only as the e2e build needs it |

| PR | Title | Repository | Contains | Touches | Tests | Depends on | Parallel with | Size (code + tests) |
|---|---|---|---|---|---|---|---|---|
| PR-1 (#776) | ext, api: a decide grant, its four routes and the route to it | rocksolidlabs/conductor | the PR-1 column above | `ext`, `internal/proto`, `internal/crew`, `internal/api`, `web/app/utils/protocol.ts`, `docs` | the Go tests above | #775 (its PR merged); DR1, HM1 and MB0 on `main` | none: #775 shares `ext`, `internal/api`, `web/app` and `docs` | about 470 + 820 |
| PR-1, a third part (#868) | api: at most 16 of one decide principal's requests are worked at once, and a response is judged again once its body is read | rocksolidlabs/conductor | the four routes' count of a subject's requests worked at once (from the moment the server starts its own work on a request, for a response once its body has been read whole, for the list and the read once the route's gate and query check have passed and for `GET /api/me` once its gate has passed, until its reply has been made, before the reply's first byte is written), and the respond route's ask of the provider again for a response that has a place, the response counted first | `internal/api`, `docs` | `internal/api`'s tests of both | PR-1's routes (#847, merged) | PR-2 | about 60 + 300 |
| PR-2 (an issue cut when this plan merges) | web: the decide page and a pass taken from the fragment | rocksolidlabs/conductor | the PR-2 column above | `web/app`, `web/e2e`, `docs` | the vitest files and `decide-only.spec.ts` above | PR-1 (merged) | none: it follows PR-1 | about 340 + 500 |

A release that carries the decide grant is cut only once both have
merged: PR-1 alone gives the server's routes and no page to reach them.

## Owner decisions

Dated 2026-11-10. Each is a default for a choice the issue's words leave
open, or a narrowing of them, the fail-closed one, which the owner may
overturn. Review round 2 changed 1, 4, 5, 7, 8 and 12, withdrew 11 (its
stream is gone) and added 16 to 19; review round 3 changed 16 and added
20; after round 3 the lead changed 1, 12 and 16 (the list restored);
review round 4 changed 1 (the list's admission in Go, its limit and cut
of admitted rows) and 14 (authentication first); the final check said
in words what a person notices of 3, 12 and 17; the numbers are kept so
the earlier threads still point at the right one.

1. **Four routes: who it is, the list, one decision, its answer**
   (after round 3; rounds 1 and 2 had cut the list). The grant reaches
   `GET /api/me`, the decide list (`GET /api/me/decisions`), one
   decision's read and its respond route, and nothing else. The list is
   back because the issue gives a decide principal "the decision page's
   read model filtered to the decisions that admit it", and a person sent
   to the decide page with a pass and no decision's address, the ordinary
   way in, would otherwise find nothing to answer. It comes back in the
   smallest form that fails closed: the issue's own snapshot bound,
   `{id, rev}` of the open decisions that admit the caller, at most 200
   and 16 KiB, a `cut` flag, a route of its own. Round 4: admission has
   one reader, Go's: the list reads the server's newest 500 open
   decisions through a bounded ledger read with no JSON function in its
   SQL, and judges each row with the one-decision read's own
   `AdmissionOf`, so the two cannot disagree; its limit and its `cut`
   count admitted rows only. What remains, and the owner may weigh: on a
   server with more than 500 open decisions, a decide principal's list
   can miss an older one that admits it, and says `cut` (its address
   still reads it). Nothing else of decisions that do not admit the
   caller changes the reply: no field but the two, no count, page or
   cursor, no other record. The stream: 16.
2. **The decide view is a table.** A decide principal reads the
   request's own words, its state and its own receipts (each with its
   `responseId`), never the record whole, the refs, the history or
   another's receipt (The decide view). Its options are the engine's
   (`crew.DecisionOptions`), an assignment's two fixed ones among them.
3. **Admission ends with the answer.** Once a decision is answered, a
   holder of the grant reads only the outcome of its own answer, and one
   with no answer of its own reads nothing. What a person notices: an
   answered decision leaves the decide list, so the outcome of their
   answer is shown on the page that sent it and at the decision's own
   address (`/decide/<did>`), not in the list a later Open of `/decide`
   draws.
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
   of a decision `crew.AdmissionOf` answers `AdmitNone` for is `404 not_found`, with
   the body a decision that does not exist gets.
10. **`GET /api/me` for the chain's tokens**: the workbench token
    `admin` and `host`, a host token `host`, a link none.
11. **Withdrawn in round 2.** It said how the stream ended; there is no
    stream (16).
12. **The pass lives in the tab; the draft and the answer sent in the
    page.** The pass in `sessionStorage`, emptied at its first `401`
    (after round 3: a `404` closes one card and keeps the pass, since the
    list may hold others); the draft and the frozen `sent` body in the page's state,
    never in the browser's storage: a new Open loads the page anew, and
    at the decision's address reads the outcome of an answer sent before
    (3). What a person notices, against the issue's one store for the
    origin: when access ends, an answer not yet sent stays readable on
    that page, which can no longer send it, and the page a new Open
    brings starts without it, so the person enters it again there; a tab
    opened by hand, with no Open, holds no pass and shows the ended words.
13. **The workbench token wins.** With one stored, the pass is kept and
    not sent, and the decide address opens the operator's page.
14. **On a switchyard the decide routes are `403 switchyard` to an
    admitted principal, after authentication** (round 4), as
    `launching`'s are on `main` (`requireAdmin` around `launchingAny`):
    decisions are answered where the work runs. The admin and a decide
    principal get `403 switchyard`; a host-only principal and a request
    with no credential get the `401` they get on `main`.
15. **Playwright on the shipped build.** No test-only binary registers a
    fake provider; the page is proved against the real server's refusal
    and against stubbed replies, the server by Go tests with `exttest`.
16. **No live connection for a decide principal** (round 2). The issue
    names `GET /api/decisions/events` as "the only live connection a
    decide-only principal may open"; the decide grant gives none. The
    difference a person can weigh: with the issue's stream a decision
    routed, taken back or answered would show on an open page at once;
    here it shows at the page's next read, every 10 s while the page is
    shown, and while reads fail the page cannot be answered on what it
    last read. The issue's snapshot is kept, as the decide list read
    again on that interval (1). The decide page reads the list and each
    open card every 10 s while shown and at once
    when shown again, the server refuses a response after a take-back,
    an answer or a pass's end, and the page lets a person answer only on
    a view read in the last 25 s (What an open page shows). Round 3
    withdrew round 2's promised time from a take-back to a closed page: a
    read begun before the take-back can return late, and reads can keep
    failing. That removes the stream's caps, its signal under the ledger's lock and the race
    between a delivery's judgement and its write. The issue's stream
    tests become read tests (the table under Tests per surface):
    `TestDecisionStreamCarriesOnlyAdmittedDecisions` is
    `TestDecideListCarriesOnlyAdmittedDecisions` and
    `TestDecideReadCarriesNothingElse` (other decisions and the session,
    activity and chat changes among their markers),
    `TestDecisionStreamPolicyChangedDuringDelivery` is
    `TestDecideListTakeBackBetweenReads` and
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
    adds its rule there. What a person notices: a decision has one route
    target at a time, so giving it to the decide grant drops whom it was
    for (HM1's person or responsibility), and routing it to a person
    takes the grant back; the history shows each route, and the operator
    answers either way.
18. **One operation is kept for the take-back** (round 2). While a
    decision is given to the grant, the last of its 32 operations is the
    take-back's; a route to the grant needs two left.
19. **The outcome is one of four words** (round 2): `applied`,
    `waiting`, `not_used`, `refused` with a code, from the whole record,
    never a reason.
20. **A grant adds its own surface** (round 3). A principal's surface is
    the union of its grants'; with `host` beside `decide` it keeps the
    host connection and is shown the decide page, with `admin` it is the
    operator (The decide principal's surface).

## Acceptance

The acceptance boundary is observed in two steps, each when its PR's
checks pass in CI and locally. PR-1: `TestDecideGrantAdmitsDecisionRoutesOnly`
and the other `internal/api` tests show a principal with only the decide
grant answering the decisions it may through the four routes and
answered as no credential everywhere else, `GET /api/events` among them;
`TestDecideListCarriesOnlyAdmittedDecisions` and
`TestDecideReadCarriesNothingElse` show it listing the open decisions
that admit it and reading nothing else. PR-2: the vitest files and
`decide-only.spec.ts` show the pass taken from the fragment, dropped
from the address bar, kept for the tab and never sent in a URL, and the
decide page alone drawn for it. Done is both PRs merged with those
checks green. Nothing needs the owner but the merges; a live check with
a real identity provider is the provider module's.

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
> read of a decide principal (`crew.AdmissionOf`); no write changes the
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
unchanged; the decide list's row is new, after `GET /api/decisions`'s). The bounds are the shipped ones (`maxDecisionReply`,
`maxDecisionRespondBody`, `docs/protocol.md`'s Decisions table), and the
route row is `docs/protocol.md`'s with the grant added:

| Route | Who | Body → reply |
|---|---|---|
| `GET /api/me/decisions` | a principal with the decide grant and not the admin grant (the admin `403 not_decide`) | → `{decisions: [{id, rev}], cut?}`: the first 200 open decisions whose entries admit it, newest first, at most 16 KiB, each judged in Go by `crew.AdmissionOf`, the one-decision read's judgement, over a bounded read of the server's newest 500 open decisions (no JSON function in its SQL); `cut` when a 201st admitted one was met or the read reached its bound first, so on a server with more open decisions than that the list can miss an older one; no other field, count, page or cursor (`proto.DecideList`, `docs/plans/decide-grant.md`) |
| `GET /api/decisions/{did}` | admin, or a principal with the decide grant the decision admits (`crew.AdmissionOf`; `404 not_found` for one it does not) | the admin: `{decision, refs}`, the record whole and its evidence refs' resolutions, at most 2 MiB (`maxDecisionReply`: the record's parts at their bounds, its 32 receipts at `ledger.MaxDecisionReceipt`, 32 KiB each, and its refs at `maxDecisionRef`); a decide principal: the decide view alone (`proto.DecideView`, at most 32 KiB, `docs/plans/decide-grant.md`) |
| `POST /api/decisions/{did}/respond` | admin, or a principal with the decide grant the decision admits (`404 not_found` for one it does not) | `{responseId, choice?, text?, rationale?, expectedRev, byName?}` (at most 32 KiB, `maxDecisionRespondBody`: `text` at most 4,096 bytes and `rationale` 2 KiB, each as `encoding/json` sends it, escapes and all) → `201 {receipt, decision}`; `200` repeat; `409 decision_answered`, `decision_closed`, `stale_decision`, `stale_policy`, `response_conflict`, `not_held`; `403 not_approver`; `503 decision_moved`; a decide principal's reply its own receipt and the decide view (`proto.DecideReply`), a refusal's words its code's (`decideWords`) |
| `POST /api/decisions/{did}/route` | admin, or a Brakeman turn granted `route_decision` | `{opId, to: {kind: "operator"} \| {kind: "person", name} \| {kind: "role", name: <responsibility>} \| {kind: "grant", grant: "decide"}, note?, expectedRev}` → `{receipt, decision}` (a turn: `{receipt, error}`); `200` the same for a repeat; `409 op_conflict`, `stale_decision`; `429 decision_limit` (the operator's operations, comments, requests for more and routes together, at most 32 per decision, the last kept for the take-back while it is given to the grant; a Brakeman turn's routes, at most 4, apart from them). To the operator it changes no state, but takes back a decision given to the grant. To a person or a responsibility (HM1), resolved in the run's crew as launched (`400 invalid` for one it does not resolve), it changes whom the decision is for, and to the grant it gives the decision to the decide grant: only while it is `open` (`409 decision_answered`, `decision_closed` otherwise), at the `expectedRev` read (at least 1, judged before the state, so a route sent at a rev an answer moved is `stale_decision`), through the run that holds it (`409 not_held` otherwise; the take-back also through the ledger alone); it rewrites the request's `for`, writes the `routed` row with `for` and `forWas`, and bumps `rev`. To a person or a responsibility, who answers does not change: the operator; to the grant, anyone holding the decide grant answers too. To the grant: the admin's alone, `409 no_identity` on a build with no identity provider registered; a turn's `to: grant` is `400 invalid`, and a turn's route of a decision given to the grant `409 stale_target` |

The paragraph after the table:

> A share link's controller, an agent's token and a Brakeman turn's token
> are refused every admin route (`requireAdmin`'s `401 unauthorized`), so
> the free build's one approver is the operator; `403 not_approver` is the
> policy's refusal of a principal that got past it without the authority
> `local/1` names (a router's answer, below). A principal holding the
> decide grant and not the admin grant, which only a build with an
> identity provider registered can present, is admitted by `GET /api/me`,
> by the list of the open decisions it is admitted to (`GET
> /api/me/decisions`) and by the read and the response of a decision it
> is admitted to (`docs/plans/decide-grant.md`), and answered by every
> other route as
> it is without the decide grant (a decide-only one as a request with no
> credential, `401 unauthorized`).
