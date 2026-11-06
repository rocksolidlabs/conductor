# Tracker linking: GitHub issues and pull requests on Dispatch tickets with your own token, and outcomes published on purpose (Program item IN1a)

A Dispatch ticket today is Conductor's own record: a project may say it
keeps an external tracker (DS1's `tracker: external` with a URL), and a
ticket may carry a delivery link to a pull request, but nothing reads
GitHub. The issue a ticket came from is copied by hand, its screenshots
saved and attached by hand, a delivery's merged state is a person's word,
and the outcome goes back to the issue as a comment a person writes in
the browser. This plan gives Dispatch a **link to GitHub with a token
the person supplies**: an issue or a pull request **imported** as a ticket
or **linked** to one, the system, the external ids and a **snapshot** of
what GitHub said kept on the ticket, **which system owns each shared
field** recorded and enforced, the issue's images and its steps to
reproduce carried into the **launch package**, a linked pull request's
merged state and checks **read** for the close policy and for a
candidate's review, and an outcome **published** to GitHub as one comment,
composed and previewed by the person, sent once. Nothing is written to
GitHub by anything else, and nothing is synchronised both ways.

It is Program item IN1a (the Program, section 4.3, and decision D10),
written against `main` at 43abe4f7 (PR #656, 2026-11-05) after reading
Dispatch's store (`internal/dispatch`, schema version 2: the reserved
`source`, `external_id`, `external_url`, `external_snapshot` and
`authority` columns of `tickets`, `links.go`, `rules.go`, `package.go`,
`acceptance.go`, `delivery.go`), the engine's reading of a launch package
(`internal/crew/package.go`), the API's Dispatch routes
(`internal/api/dispatch*.go`), the sync adapter (`internal/api/sync.go`),
the export's left-out table (`internal/export/leftout.go`), the data
directory store (`internal/store`), the configuration's deny list
(`internal/config/deny.go`), git for the Files tab (`internal/gitcli`,
`internal/gitrepo`), the operating guide (`internal/api/guide.go`), the
CLI's `conductor dispatch` (`internal/cli/dispatch*.go`) and the pages
(`web/app/pages/dispatch`, `DispatchTicketPage.vue`, `DispatchLinks.vue`,
`DispatchClosePolicy.vue`, `DispatchPlanPropose.vue`,
`web/app/pages/settings.vue`), and the plans they came from:
`docs/plans/dispatch.md` (DS1), `docs/plans/dispatch-evidence.md` (DS2),
`docs/plans/review-repair.md` (RV1), `docs/plans/decisions.md` (DR1 and
HM1), `docs/plans/brakeman.md` (BK0 to BK3) and `docs/plans/ext.md` (C1
to C9). It is reviewed before any of its code; the PRs at the end are cut
into issues after the review, one issue, one branch `issue-N`, one PR
each (AGENTS.md).

The names follow the railroad (the Program, section 1.2): Conductor is
the application, Dispatch its ticketing, whose items are tickets,
Yardmaster the harness that runs a crew's tasks, Brakeman the sidekick.
"GitHub issue" and "pull request" are GitHub's words for GitHub's records;
a ticket's **linked issue** is the one GitHub item a ticket mirrors (its
counterpart, kept in the ticket's reserved columns), and its **links** are
DS1's links, some of which point at GitHub pull requests. A
**publication** is one comment Conductor posts to GitHub for a person. A
**token** here is always the person's own GitHub token; this item has no
hosted service, no OAuth application and no shared credential: those are
Program item IN1b, a later item in the hosted services' repository, which
says so wherever a reader might expect more.

What is on `main` and what is not. Both prerequisites are Done and on
`main`: DS1 (the store, its reserved columns and the close policy) and DS2
(plan revisions, launches, the package, delivery's candidate). RV1 is in
implementation: its PR-1 takes Dispatch's schema version 3 (reviews,
findings, marks) and its check finding reserves `source: external` for
this item. So this plan's store PR takes the next version after whatever
`main` holds when it is cut, written here as **version 4** on the
expectation that RV1's PR-1 merges first; and the one PR that turns a
pull request's CI job into a review's failed-check request (PR-10) waits
on RV1's PRs that serve check findings and the Review view. Every other PR
builds on what is on `main` today.

## Articles it serves

1. No licence check: linking, importing, reading and publishing ask for
   no plan, tier or provider; the only condition is that the person has
   saved a GitHub token, which is the person's own credential, not
   Conductor's entitlement.
2. Useful on its own: the Program puts own-token GitHub linking in the
   free tier (decision D10, the Program, section 5), and every part of it
   ships in the open-source build with no account; a person who never
   saves a token loses nothing Dispatch does today.
3. Nothing is gated by a flag: there is no paid part and no switch; the
   managed connectors of IN1b are a different credential source, not a
   feature this build hides.
4. Stop, inspect, revoke, export and required approvals stay free and
   never degrade: a publication is a person's explicit act with a
   receipt, never a side effect; removing the token in Settings stops
   every GitHub call at once (revoke); every record this item adds is a
   row of Dispatch's plain store, exported whole by C6's export, which
   leaves the token itself out; stop, pause, cancel, retry and accept are
   the run's own, unchanged.
5. Dependencies point one way: no new module. `internal/tracker` imports
   the standard library and `golang.org/x/net/html`, a package of a module
   `go.mod` already requires; `internal/dispatch` still imports the
   standard library and `modernc.org/sqlite` alone, and neither imports
   the other (the API joins them).
6. No feature build tag: a plain `go build` carries all of it.
7. The free defaults stand: no extension interface is called and none is
   added. The actor of every operation is the one C2's identity seam
   already gives (`admin`, or `user:<subject>`); Dispatch's new entities
   reach a registered sync store through the existing adapter
   (`api/sync.go`, one reference per entity a commit changed), and the
   token never does. IN1b will add hosted credentials and other systems
   through a seam of its own plan; it adds, and never removes, this
   item's own-token path.
8. Not touched as written (no hosted dependency exists here), and kept in
   spirit: GitHub unreachable, rate-limited or refusing the token makes a
   read or a publication fail with words; no local state is lost, no
   route waits on GitHub past its own bound, and nothing else in
   Conductor waits on it at all.
9. `CGO_ENABLED=0` stays: Dispatch's store stays `modernc.org/sqlite`,
   and GitHub is reached over `net/http`.
10. The repository contract holds: this plan before the code; race tests
    per package; vitest and Playwright for the pages; no new module, so
    `make notices` has nothing to regenerate (each PR's body says so);
    no frame or control message, so `internal/proto` and
    `web/app/utils/protocol.ts` are untouched, and `docs/protocol.md`
    gains the HTTP rows, the launch package's version 2 and the
    `dispatch` event's new entities, each with its bound.
11. The railroad names keep their meaning: tickets, links and
    publications are Dispatch's; runs, candidates and receipts
    Yardmaster's; "linked issue", "publication" and "token" are told
    apart above, and "workspace" is never used here for anything.
12. Execution stays behind one seam: this item starts no process and no
    attempt; what it adds to an attempt arrives as files of the launch
    package through DS2's path ("Boxcar constraints" takes the eight one
    by one).

## Why and what happens

The Program's settled directions say Dispatch supports projects that keep
an external tracker (the Program, section 1.3), and its work model keeps
ticket status, process state, task outcome, verification, acceptance and
delivery as separate facts (section 1.4). A team that tracks its work in
GitHub issues should be able to bring one issue into Dispatch, run a crew
on it, and tell the issue what came of it, without Conductor ever
deciding on its own to change or say anything on GitHub, and without a
private comment, a review finding or a piece of the plan's context ending
up on a public issue because some status moved.

After this item ships, a person sees:

- **Settings, "GitHub".** A card with a field for a token (a
  fine-grained token with read access to issues, pull requests, checks
  and actions, and write access to issues, or a classic token with
  `repo`), the host (`github.com`, or a GitHub Enterprise Server address),
  "Check and save", and then the account it belongs to, when it was
  saved, a fingerprint (never a character of the token) and Remove. A
  token given to the server in `CONDUCTOR_GITHUB_TOKEN` shows as "from the
  environment" and cannot be removed here.
- **Import.** On a project, "Import from GitHub" (also a row of the
  palette's `>` group, beside "New ticket") takes an issue's or a pull
  request's URL (or `owner/repo#12`, the repository defaulting to the
  project's tracker URL, or else its folder's `origin` remote) and shows
  what will be made: the title, the description, the images found, the
  steps to reproduce found, GitHub's state, labels and assignees (shown,
  not mapped), and the ticket's fields that GitHub will own. Import makes
  the ticket (`CND-31`), downloads the images as attachments, and the
  ticket page says "Imported from rocksolidlabs/conductor#612".
- **Link.** On an existing ticket, "Link a GitHub issue" sets its linked
  issue without changing a field; the page then shows GitHub's title and
  state beside the ticket's own.
- **The GitHub panel** on the ticket page: the linked issue, GitHub's
  state ("Open", "Closed as completed", "Merged") shown apart from the
  ticket's status, when it was last read, Refresh, the field owners
  ("Title: GitHub · Description: GitHub", each with "Take over" or "Give
  back to GitHub"), and, where a Dispatch-owned field differs from
  GitHub's, "GitHub's description differs" with a link to it. A field
  GitHub owns is not editable here: its editor says "edit it on GitHub,
  then Refresh, or take it over". The ticket's status never moves because
  GitHub's did; a refresh that finds the issue closed says so, and the
  person decides.
- **Links read.** A link to a GitHub pull request or issue shows what
  GitHub says of it: open, closed or merged (with when and the merge
  commit), and for a pull request its checks at its head commit (passed,
  failed, pending, each with its name). A project's close policy may
  require **merged**: a live delivery link whose pull request GitHub says
  is merged, read again when the person closes.
- **The package.** A plan revision's proposal offers "Steps to
  reproduce" (on by default when the linked issue has them), the
  imported images among the attachments to hand over, and the ticket's
  external links as evidence references; an attempt's `ticket.md` then
  has a "Source" part (the issue's URL, GitHub's state at the launch, the
  snapshot's digest), a "Steps to reproduce" part and the evidence links,
  and the images are files in `.conductor/attachments/`.
- **Publish.** "Publish to GitHub" on the ticket opens a composer: the
  target (the linked issue, or a linked pull request), the person's own
  outcome text (empty to start), parts the person ticks (the delivery
  links' URLs; the names and results of the delivered candidate's checks;
  the ticket's key; "Close the issue as completed"), and a preview of the
  exact Markdown that will be posted, the hidden marker included and
  said. Publish posts it once; the ticket shows "Published to #612 by
  Alice at 14:02" with the comment's link. A lost reply, a double click
  or a server restart in the middle never posts twice: the server looks
  for its marker on GitHub before it would ever send again, and a
  publication whose fate it cannot learn is shown as such, never sent
  again by itself.
- **Review (once RV1 serves it).** A delivery pull request's failed CI
  job appears beside the Yardmaster check receipts in RV1's failed-check
  panel, and a repair can be prepared from it with the job's name, its
  link, the commit it tested and the last 2 KiB of its log.

From the CLI: `conductor trackers` (status, `set` reading the token from
standard input, `remove`), `conductor dispatch import URL --project KEY`,
`conductor dispatch link-issue KEY-n URL`, `conductor dispatch refresh
KEY-n`, `conductor dispatch authority KEY-n title|description
github|dispatch` and `conductor dispatch publish KEY-n` (which prints the
preview and posts only with `--send` and the preview's digest).

## The changes

### `internal/tracker` (new): the adapter contract

One package, system-neutral, no HTTP server, no store: what a tracker
is to Conductor, shaped so GitLab and Linear can be adapters later (IN1b,
decision D20) without a change to Dispatch or the API. Files:
`tracker.go` (the types and errors), `ref.go` (URL parsing), `repro.go`
(the steps to reproduce), `images.go` (image references and the host
rule), each with its test.

- `Ref{System, Host, Space, Kind, ID string}`: `System` `github` (the only
  one here; IN1b adds `gitlab` and `linear`), `Host` (`github.com` or a
  GitHub Enterprise Server host name, lower case, no port but in tests),
  `Space` (a repository's `owner/name`, at most 200 bytes; a GitLab
  project path or a Linear team key later), `Kind` `issue` or `change` (a
  pull request; a merge request later), `ID` (the number as text, at most
  20 bytes; a Linear identifier later). `Ref.String()` is
  `owner/name#12`; `Ref.Key()` is `github:<host>/<space>/<kind>/<id>`.
- `Item`: what a read gives: `Ref`, `NodeID` (the system's own stable id,
  at most 100 bytes: GitHub's node id), `DatabaseID`, `URL`, `Title` (at
  most 1,024 bytes), `Body` (at most 256 KiB, the system's Markdown),
  `BodyTruncated`, `State` (`open`, `closed`, `merged`), `StateReason`
  (`completed`, `not_planned`, `reopened`, `''`), `Labels` (at most 50
  names), `Assignees` (at most 20 account names), `Milestone`, `Author`,
  `CreatedAt`, `UpdatedAt`, `ClosedAt`, `Merged`, `MergedAt`, `MergeSHA`,
  `HeadSHA`, `BaseRef`, `Images` (at most 32 `ImageRef`), `ETag`.
- `ImageRef{ID, Alt, fetch}`: `ID` the image's URL with its query and
  fragment removed (what is stored and shown; a signed URL's query is a
  credential and is never stored or logged), `fetch` the URL to download
  it now, held in memory only for the read that found it.
- `Check{ID, Name, Status, Conclusion, URL, StartedAt, CompletedAt,
  HeadSHA, App, HasLog}`: one CI job on a change at a commit, at most 100
  per read.
- `Comment{ID, URL, CreatedAt}`; `Account{Login, ID, Scopes}`.
- `Adapter`, the contract:

  ```go
  type Adapter interface {
      System() string
      Parse(raw string, defaultSpace string) (Ref, error)
      Whoami(ctx context.Context, c Credential) (Account, error)
      Read(ctx context.Context, c Credential, r Ref, etag string) (Item, bool, error) // bool: not modified
      Image(ctx context.Context, c Credential, img ImageRef, max int64) (io.ReadCloser, string, error)
      Checks(ctx context.Context, c Credential, r Ref, sha string) ([]Check, error)
      CheckLog(ctx context.Context, c Credential, r Ref, check Check, tail int) ([]byte, error)
      Comment(ctx context.Context, c Credential, r Ref, body string) (Comment, error)
      FindComment(ctx context.Context, c Credential, r Ref, marker string, since time.Time, author string) (Comment, Found, error)
      Close(ctx context.Context, c Credential, r Ref, reason string) error
  }
  ```

  `Found` is `yes`, `no` (the whole window was read and the marker is not
  in it) or `unknown` (the window was not read whole: a page past the
  bound, a failure). `Credential` is an opaque value the adapter alone
  opens (`tracker.Token(host, secret)` makes one; its `String` and its
  `fmt` and `slog` forms print `token(<fingerprint>)`), so no caller can
  log a secret by printing what it holds.
- Errors, each a typed value the API maps to one HTTP answer:
  `ErrNotFound` (or no access: GitHub answers 404 for both),
  `ErrUnauthorized` (the token is refused), `ErrForbidden` (the token
  lacks a permission; its words name the one GitHub's answer names),
  `ErrRateLimited{Reset}`, `ErrMoved{To Ref}` (the item answered from a
  redirect has another node id than the one kept), `ErrUnavailable` (a
  network failure, a timeout, a 5xx), `ErrUnsupported`, `ErrBound` (an
  answer past its bound). Every error's text carries the method, the
  path without its query and the status, never a header or a query.
- `Repro(body string) (string, bool)`: the steps to reproduce from an
  item's Markdown: the first ATX heading (levels 1 to 4) whose text,
  lower-cased and trimmed of punctuation, is one of `steps to reproduce`,
  `reproduction steps`, `repro steps`, `how to reproduce`, `to
  reproduce`, `reproduction`, `repro` (GitHub's issue forms make such a
  heading of a form field's label), and its text up to the next heading
  of the same or a higher level, trimmed, at most 8 KiB (cut at a line
  end, `…` said); none found is `"", false`.
- `ImageHost(system, host, u *url.URL) bool`: the hosts an image may be
  downloaded from, `https` only: for `github.com`, the hosts GitHub
  serves issue images from (`private-user-images.githubusercontent.com`,
  `user-images.githubusercontent.com`, `camo.githubusercontent.com`,
  `objects.githubusercontent.com`, and `github.com` under
  `/user-attachments/assets/`), the list confirmed against fixtures PR-2
  captures from a public and a private repository and named in its body;
  for an Enterprise Server host, that host and `media.<host>`. A redirect
  is followed only to such a host. No other host is ever fetched: an
  issue's text cannot make the server request an address of its
  network.

The registry is a value the API makes, `tracker.Adapters{"github":
github.New(opts)}`; nothing registers itself, and no `ext` type is
involved (Owner decisions, 10).

### `internal/tracker/github` (new): the GitHub adapter

GitHub's REST API over `net/http` and `encoding/json`, no client library
(Owner decisions, 3). Files: `github.go` (the client, its options and its
bounds), `read.go`, `images.go`, `checks.go`, `comments.go`, and
`githubtest/` (a fake GitHub over `httptest`, serving the routes below
from scripted items, counting every write by route, holding a request on
a gate a test opens, and serving fixtures captured from github.com with
their hosts kept), each with its test.

- The base is `https://api.github.com` for `github.com` and
  `https://<host>/api/v3` for an Enterprise Server host. An `http` base is
  refused unless the host is a loopback address and the server runs with
  `CONDUCTOR_TRACKER_LOOPBACK=1`, which the tests and the e2e harness set
  and nothing else does.
- The client is its own `http.Client`: a 15 s timeout a request (60 s
  for an image), `CheckRedirect` refusing a redirect to another scheme,
  past 5 hops, or to a host that is neither the API host nor an image
  host, and stripping `Authorization` from every redirect that leaves the
  API host (an image's signed URL is fetched with no token at all); every
  request sends `Accept: application/vnd.github+json`,
  `X-GitHub-Api-Version: 2022-11-28` and `User-Agent:
  Conductor/<version>`.
- Every answer body is read through `io.LimitReader` at its bound, past
  which it is `ErrBound`: an item 1 MiB (its `body_html` included), a page
  of comments or checks 2 MiB, an image `max` (16 MiB, DS1's
  `MaxAttachmentSize`), a job's log the last `tail` bytes of at most
  8 MiB read.
- `Read` asks `GET /repos/{owner}/{repo}/issues/{n}` (a pull request is an
  issue to that route, and `pull_request` says so) with `Accept:
  application/vnd.github.full+json`, so the answer carries `body` and
  `body_html`; a pull request is then read at `GET
  /repos/{owner}/{repo}/pulls/{n}` for `merged`, `merged_at`,
  `merge_commit_sha`, `head.sha` and `base.ref`. A `304` to an
  `If-None-Match` is "not modified". A `301` (a renamed or transferred
  repository, an issue moved) is followed once, and the node id compared
  with the one the caller holds: the same is the item at its new place
  (the caller records the new URL); another is `ErrMoved`. Images are the
  `<img src>` of `body_html` (parsed with `golang.org/x/net/html`, at most
  32, each kept only when `ImageHost` admits it), since a private
  repository's images are reachable only through the short-lived signed
  URLs GitHub puts in the rendered body.
- `Checks` reads `GET /repos/{owner}/{repo}/commits/{sha}/check-runs`
  (`per_page=100`, one page) and `GET …/commits/{sha}/status` (the
  combined statuses, as checks without logs); `CheckLog` reads a GitHub
  Actions job's log (`GET /repos/{owner}/{repo}/actions/jobs/{id}/logs`,
  a redirect to a signed URL fetched without the token) and keeps its last
  `tail` bytes; a check of another app has no log (`ErrUnsupported`).
- `Comment` posts `POST /repos/{owner}/{repo}/issues/{n}/comments`;
  `FindComment` lists `GET …/issues/{n}/comments?since=<since>&per_page=100`,
  at most 10 pages, for a comment by `author` whose body ends with the
  marker; `Close` sends `PATCH /repos/{owner}/{repo}/issues/{n}` with
  `state: closed` and the reason.
- Rate limits: a `403` or `429` with `X-RateLimit-Remaining: 0` or
  `Retry-After` is `ErrRateLimited` with the reset time; the adapter never
  retries by itself, and nothing in Conductor polls.

### `internal/dispatch`: the linked issue, field owners, reads and publications (schema version 4)

The package still has no HTTP, no session and no process; it never
imports `internal/tracker`: the API hands it plain values. New files
`external.go` (the linked issue, the field owners, refresh), `reads.go`
(the links' reads) and `publications.go`, each with its test; `schema.go`,
`model.go`, `ops.go`, `rules.go`, `tickets.go`, `query.go`,
`acceptance.go`, `package.go` and `plans.go` extended.

**Schema version 4** (the version after RV1's 3; whichever of the two
merges second takes the next number, and the migration table follows).
`migrations[3]` brings a version 3 database to version 4 in the `BEGIN
IMMEDIATE` transaction that reads the version, as DS1 set the shape; it
creates two tables and three indexes, adds one column, and reads and
rewrites no row (every version 3 ticket has `source = 'native'` and the
other reserved columns empty, which is true of all of them: nothing wrote
them), so nothing is derived and no `migrate` row is written; a test
opens a version 3 database made by the version 3 code and reads every row
back unchanged.

The reserved columns of `tickets`, filled here as DS1 reserved them:

| Column | Meaning from version 4 |
|---|---|
| `source` | where the ticket was made: `native` (made in Dispatch), `github` (imported) |
| `external_id` | the linked issue's node id (`I_kwDO…`, `PR_kwDO…`), at most 100 bytes of `[A-Za-z0-9_=-]`; `''` when none |
| `external_url` | the linked issue's page (`https://github.com/<owner>/<repo>/issues/<n>` or `…/pull/<n>`), at most 2 KiB, `https`; `''` when none |
| `external_snapshot` | JSON, at most 24 KiB as encoded: what GitHub said at the last read (below); `''` when none |
| `authority` | JSON object of field to owner, at most 256 bytes: `{"title":"github","description":"github"}`; a field not named is Dispatch's; `''` is every field Dispatch's |

The snapshot (`model.go`, `ExternalSnapshot`, version 1): `{v, system,
host, space, kind, id, nodeId, databaseId, url, title (GitHub's whole,
at most 1,024 bytes), bodySha256 (of GitHub's raw body), bodyBytes,
bodyTruncated, state, stateReason, merged, mergedAt, mergeSha, headSha,
baseRef, labels, assignees, milestone, author, createdAt, updatedAt,
closedAt, repro (at most 8 KiB), images ([{id, attachmentId, sha256}],
at most 32), readAt, readBy, etag}`. It holds GitHub's body only by its
digest: the body is the description when GitHub owns it, and a page
showing a Dispatch-owned description beside GitHub's links to GitHub's
rather than keeping a second copy, so a snapshot fits a change row
(`MaxChangeValue`, 64 KiB) with room, and `model_test.go` builds one at
every bound and measures it.

New column, `tickets.closed_on` (JSON, at most 1 KiB, `''` by default):
when a close met the `merged` requirement, the read it rested on
(`{linkId, url, mergedAt, mergeSha, readAt}`), so the fact a gated close
used travels in the log as DS1's gated class requires.

New tables:

| Table | Columns (bounds in `model.go`) |
|---|---|
| `link_reads` | `link_id` PK (a live link of the ticket's, kind `delivery` or `external`), `ticket_id`, `system`, `kind` (`issue`, `change`), `node_id`, `state`, `merged` (0 or 1), `merged_at`, `merge_sha`, `head_sha`, `checks` (JSON, at most 32 KiB: at most 100 `{id, name, status, conclusion, url, app, hasLog, completedAt}` at the head commit), `read_at`, `etag`, `error` (at most 500 characters: the last read's failure in words), `error_at`: a cache of the last read of a linked GitHub item. It is derived and local: no `changes` row, no `rev`, no receipt, never replicated (its class is `derived` below); a link removed drops its row in the same transaction |
| `publications` | `id` PK (16 hex), `ticket_id`, `target` (`linked` for the ticket's linked issue, or `link:<id>` for a link to a GitHub issue or pull request), `target_url`, `target_ref` (`owner/repo#n`), `body` (Markdown, at most 16 KiB: exactly what the person approved, the marker not included), `body_sha256`, `parts` (JSON, at most 1 KiB: which offered parts the person ticked, for the record), `close_issue` (`''`, `completed`, `not_planned`), `state` (`draft`, `sending`, `sent`, `unsent`, `failed`, `discarded`), `tries` (at most `MaxPublicationTries`, 5), `remote_id`, `remote_url`, `closed_remote` (0 or 1), `error` (at most 500 characters), `sending_at`, `sent_at`, `sent_by`, `created_at`, `created_by`, `fingerprint` (the preparing operation's), `rev`; at most 100 per ticket |

Indexes: `tickets(external_id)`, `link_reads(ticket_id)`,
`publications(ticket_id, created_at)`, `publications(state)`. No new
unique index: "one ticket per linked issue" is a rule checked on a local
write, as DS1 keeps every uniqueness a replicated add could trip.

**Operations** (`external.go`, `publications.go`; refusals as DS1's, with
two added: `external_linked`, a GitHub item another live ticket already
has as its linked issue, naming that ticket's key; and `external_owned`, an
edit of a field GitHub owns):

- `ticket.import`, `ImportTicket(ctx, op, project, ImportArgs)`: makes a
  ticket of `project` with `source: github`, the title (GitHub's, cut to
  DS1's 200 characters at a character boundary with `…`, the snapshot
  keeping the whole), the description (GitHub's body with `\r\n` made
  `\n` and every other control character but `\t` dropped, cut to DS1's
  64 KiB at a line end with a last line saying the rest is on GitHub;
  `bodyTruncated` set), `external_id`, `external_url`,
  `external_snapshot` and `authority` `{"title":"github","description":
  "github"}`, in one operation with one `changes` row per field as every
  create writes; refused `external_linked` when a live ticket holds that
  node id, `archived` for an archived project. Status, priority, owner,
  labels, assignees and milestone are Dispatch's defaults or what the
  request names: GitHub's labels and assignees are in the snapshot,
  never mapped (Owner decisions, 4).
- `ticket.link_external`, `LinkExternal(ctx, op, key, LinkArgs)`: sets a
  live ticket's `external_id`, `external_url`, `external_snapshot`, and
  `authority` `''` (every field stays Dispatch's: the ticket was written
  here), one `set` row each; `expectedRev` the ticket's; `external_linked`
  as above; a ticket that has a linked issue is `invalid` ("CND-12 is
  linked to #612: unlink it first").
- `ticket.unlink_external`, `UnlinkExternal(ctx, op, key)`: empties the
  four columns (`source` stays what it was: where the ticket was made),
  one `set` row each; the snapshot's last value stays in the history.
- `ticket.refresh`, `RefreshExternal(ctx, op, key, SnapshotArgs)`: the
  API has read GitHub and hands the new snapshot with GitHub's title and
  body; in the transaction the node id must be the kept one (`invalid`
  otherwise: the API checked it, this is the store's own guard),
  `external_snapshot` is set when its content changed (a snapshot that
  differs only in `readAt` and `etag` is written in place without a
  `changes` row, so a refresh that finds nothing new adds nothing to the
  history), `external_url` when GitHub moved the item, and each field the
  authority gives GitHub (`title`, `description`) is set to GitHub's when
  it differs, one `set` row each, as any edit: the intent digest moves
  with it, so a plan revision approved before goes stale exactly as DS2
  says, and a run already launched keeps its package. A field Dispatch
  owns is never written by a refresh.
- `ticket.authority`, `SetAuthority(ctx, op, key, field, owner)`: `field`
  `title` or `description`, `owner` `github` or `dispatch`; to
  `dispatch` sets the authority only; to `github` sets the authority and,
  in the same operation, the field to the snapshot's value (the title;
  the description only when the snapshot's `bodySha256` is that of a body
  the request carries, which the API read from GitHub a moment before:
  the store never keeps GitHub's body twice), so the history shows the
  local text replaced. A ticket with no linked issue is `invalid`.
- `ticket.edit` gains one rule: setting a field the authority gives
  GitHub is `external_owned` ("the title is GitHub's: edit it on GitHub
  and refresh, or take it over"), checked in the operation's transaction.
- `RecordRead(ctx, linkID, LinkRead)` and `RecordReadError(ctx, linkID,
  words)`: write a `link_reads` row in a transaction of its own, outside
  `Apply` (no receipt, no `changes` row), and call `OnChange` with a
  change of entity `link_read` so the page hears of it.
- The close policy gains `merged` (`RequireMerged`, beside DS1's
  `acceptance` and `delivery` and DS2's `verified`): met by a live
  `delivery` link whose `link_reads` row says `merged` and whose `read_at`
  is not older than `MaxMergedReadAge` (5 minutes, so the close route's
  own read is what counts); the close writes `closed_on` with the read it
  used. Unmet, `close_policy` names `merged` in `missing`.
- `publication.prepare`, `PreparePublication(ctx, op, key, PublicationArgs)`:
  a `draft` row with the target, the body as the person composed it and
  its digest, the ticked parts and `close_issue`; a target that is not
  the ticket's linked issue or one of its live links to a GitHub item is
  `invalid`; `bound` past 16 KiB or 100 per ticket.
- `publication.send`, `BeginSend(ctx, op, id, bodySha256)`: `expectedRev`
  the publication's; the digest must be the stored body's (`stale_revision`
  otherwise: what is sent is what was previewed); state `draft`,
  `unsent` or `failed` becomes `sending` with `sending_at`, `sent_by` and
  `tries` + 1 (`bound` at 5); state `sending` or `sent` is `invalid` with
  its words ("already sent: #612's comment 2153"). The commit is before
  any request to GitHub.
- `publication.settle`, `SettleSend(ctx, op, id, Outcome)`: `sending`
  becomes `sent` (with `remote_id`, `remote_url`, `sent_at`), `failed`
  (a refusal GitHub gave before it could have posted, with the words) or
  `unsent` (the marker looked for over the whole window and not found);
  the API alone calls it, with its own op id (`<send op>~s`). `closed_remote`
  is set by a second settle once the close is answered.
- `publication.discard`: a `draft`, `unsent` or `failed` one becomes
  `discarded`.

Every new intent keeps DS1's shape: one transaction, the receipt looked
up first, `expectedRev` compared, one `changes` row per field, `rev`
bumped, the receipt kept, then `OnChange`. The `changes` log gains the
entity `publication` (its rows name their ticket and project, as a
comment's do, and bump no ticket `rev`, so publishing never makes a
ticket's edit stale), and the ticket's fields above.

**Queries.** `Query.External` (a URL, at most 2 KiB): the tickets whose
`external_url`, or one of whose live links' `url`, is that URL (the
palette's search for a pasted GitHub link); `Ticket` reads carry the
snapshot, the authority, each link's read and the publications (the
newest 20, `MaxTicketPublications`), which the reply bound
(`MaxTicketReply` in `internal/api`) is measured again with, at their
bounds; `Publications(ctx, key, cursor)` pages the rest.

**Field rules** (`rules.go`): `Class` gains the new fields, the
`publication` entity and one class:

| Field | Class | Why |
|---|---|---|
| `source`, `external_id`, `external_url`, `external_snapshot`, `authority`, `closed_on` | scalar (DS1 already lists the reserved five) | last writer wins per field; two replicas' refreshes keep the later read |
| a publication's `id`, `ticket_id`, `target`, `body`, `body_sha256`, `parts`, `close_issue`, `created_at`, `created_by` | immutable | a publication's content never changes once prepared |
| a publication's `state`, `tries`, `remote_id`, `remote_url`, `closed_remote`, `error`, `sending_at`, `sent_at`, `sent_by` | scalar | written by the replica that sends; CS1 decides whether a replica may send another's draft (here, only the replica that prepared it can, being the only one) |
| `link_reads` (every column) | derived (new) | a cache of GitHub: never a `changes` row, never shipped; each replica reads GitHub itself |

"One ticket per linked issue" is a rule of the local write; two replicas
that each import the same issue make two tickets, which CS1's plan
folds or shows, as DS1 leaves its other cross-replica cases to CS1.

**The package, version 2** (`package.go`, `plans.go`; DS2's plan owns the
package and its rules, and this item adds one part under them).
`PackageVersion` becomes 2; a package built by version 1 code stays
version 1 and is read as it is. `Package.Source`, present when the ticket
has a linked issue at the launch: `{system, url, ref, nodeId, state,
stateReason, readAt, snapshotSha256, repro (when the revision chose it),
evidence ([{url, note}], at most 20: the ticket's live `external` and
`delivery` links the revision chose)}`, at most 32 KiB. A plan
revision's body gains `source: {repro: bool, evidence: [link ids], at
most 20}`, fingerprinted with the rest, so the approval covers what is
handed over; a link chosen and removed since is `stale_plan` at the
launch, as DS2's handed-over comments are. The imported images are
ordinary attachments, chosen through DS2's existing `attachments` (at
most 8). `MaxPackage` (512 KiB) is unchanged and still measured with
every part at its bound.

### `internal/crew`: the package's version 2 read

`internal/crew/package.go`: `PackageVersion` 2 with version 1 still read;
`Package.Source` parsed with its bounds (a package past one was not built
by Dispatch and is refused, as today); `ticketMarkdown` gains "## Source"
(the URL, GitHub's state at the launch, the snapshot's digest and a line
saying GitHub may have changed since and the run follows this package),
"## Steps to reproduce" and "## Evidence links". The ledger keeps the
package byte for byte as it does; no ledger schema change.

### `internal/gitrepo`: a folder's remote, read from its configuration

`RemoteURL(ctx, dir, name string) (string, error)`: the first URL of the
named remote in the repository's configuration, through go-git's
`Config()` (no git process, no transport, nothing fetched), for the
import dialog's default repository. The folder is a project's `cwd`,
resolved through `resolveCwd` by the API before it is opened, as every
path a session or a launch uses is. `tracker.Parse` turns
`git@github.com:owner/repo.git`, `ssh://git@github.com/owner/repo` and
`https://github.com/owner/repo(.git)` into a repository.

### `internal/config` and `internal/export`: the token kept and left out

- `CONDUCTOR_GITHUB_TOKEN` (and `CONDUCTOR_GITHUB_HOST`, default
  `github.com`), read at start by `applyEnv` into a field of the
  configuration that is never written to a file, never served by any
  route and never logged (`Config`'s `slog` form says `set` or `unset`).
  There is no configuration file key for it: a token in `conductor.json`
  would be one more file holding it.
- `CONDUCTOR_TRACKER_LOOPBACK=1` admits an `http` loopback host (tests
  and the e2e harness only).
- `internal/export/leftout.go` gains `{dir: "credentials", why:
  WhySecret}`: an archive never carries the token, and a restore refuses
  an archive that has the directory.

### `internal/api`: the token, import, link, refresh, reads, publish

**The token** (`trackers.go`). Kept in `<dataDir>/credentials/github.json`
(the directory 0700, the file 0600, written through `store.Sub
("credentials")` by its atomic rename), one entry per host, at most 4
hosts: `{host, token, fingerprint, login, accountId, scopes, savedAt}`.
`fingerprint` is the first 12 hex characters of the token's SHA-256, what
the page shows. It is the person's credential for another service, so it
is kept usable, as the workbench token and the switchyard token are kept
in their files, not as a hash; AGENTS.md's hash rule is for tokens
Conductor checks, and here the one comparison made (whether a token sent
is the one already kept, so saving it again changes nothing) compares the
two SHA-256 values with `share.Equal`. The data directory is refused to
every session's file reads and to the file routes already
(`config.FileDenyFunc`); an agent's process runs as the same OS user and
could open the file itself, as it can the workbench token's, the trust
model DS1 states and item F's guests will tighten. The sync adapter's
allowlist (`api/sync.go`: `catalog.json`, `crews/`, `runs/`) does not
name `credentials/`, and a test holds it so.

| Route | Body or query | Answer |
|---|---|---|
| `GET /api/trackers` | none | `{github: [{host, login, fingerprint, scopes, savedAt, source: settings|environment, state: ok|refused|unchecked, checkedAt}]}`, never a token |
| `POST /api/trackers/github` | `{host, token}` (at most 4 KiB; a token of 1 to 255 bytes of printable ASCII) | checks it with `Whoami` (15 s), then saves: `200 {tracker}`; `422 token_refused` with GitHub's words when the check fails, nothing saved; `409 environment` for the host the environment gives |
| `POST /api/trackers/github/remove` | `{host}` | removes the entry; every later call for that host answers `412 no_token` |

The token is read from the file for each call (a remove takes effect at
the next request, with no cache to empty), wrapped at once in a
`tracker.Credential`, and never placed in a log attribute, an error, a
reply, an event or a URL. Every slog line the GitHub paths write names
the route, the ref and the status. A test captures every log line of a
whole import, refresh, read and publication and fails on the token's
bytes, its base64, or any `?` after a URL.

**Import, link, refresh, authority, reads** (`dispatch_external.go`), all
the admin's (Dispatch's rule; a Brakeman turn reads them through the
ticket's read, `readable`, and calls none), each a switchyard's 403:

| Route | Body | What it does |
|---|---|---|
| `POST /api/dispatch/external/preview` | `{url, project?}` | reads the item and answers what an import would make (`title`, `description`'s first 2 KiB and its length, `images`, `repro`, `state`, `labels`, `assignees`, the owners, the ticket already linked to it if any); writes nothing |
| `POST /api/dispatch/projects/{key}/import` | `{opId, byName, url, status?, priority?, owner?, horizon?, labels?, images?: bool (default true)}` | reads the item (one read, bounded), runs `ticket.import`, then downloads each admitted image (at most 8, `MaxImportImages`, each through DS1's attachment path, `attachment.add` under the op id `<opId>~i<n>`, named `github-<n>.<ext>` by its sniffed type, `image/png`, `image/jpeg`, `image/gif` or `image/webp`, anything else skipped and said), then writes the snapshot's `images` (a `ticket.refresh` under `<opId>~r`); answers `201 {receipt, ticket, images: [{id, attachmentId?, skipped?}]}`. A resend with the same `opId` repeats each receipt and downloads only what is missing, so an import cut by a crash or a lost reply is finished by sending it again |
| `POST /api/dispatch/tickets/{key}/external` | `{opId, expectedRev, byName, url}` | reads the item and runs `ticket.link_external` |
| `POST /api/dispatch/tickets/{key}/external/delete` | `{opId, expectedRev, byName}` | `ticket.unlink_external` |
| `POST /api/dispatch/tickets/{key}/external/refresh` | `{opId, byName, images?: bool}` | reads the item with the snapshot's `etag` (`304` writes nothing but `readAt`), checks the node id (`409 moved` with the item it found), runs `ticket.refresh`, downloads images new since (within the ticket's attachment bound) |
| `POST /api/dispatch/tickets/{key}/external/authority` | `{opId, expectedRev, byName, field, owner}` | to `github` reads the item first (the body must be GitHub's now), then `ticket.authority` |
| `POST /api/dispatch/tickets/{key}/links/{id}/read` | `{}` | reads a link's GitHub item (and, for a pull request, its checks at its head commit) and records it (`RecordRead`); `200 {read}` |

A `link.add` whose URL `tracker.Parse` takes for a host with a token
reads it once after the operation commits, in the background of the
request (bounded, one at a time per ticket), and records the read; a host
with no token records nothing and the link is as DS1 made it. A close
(`ticket.edit` setting `done`) of a ticket whose project requires
`merged` first reads every live delivery link to a GitHub pull request
(at most 4 at once, each bounded by the client's 15 s, the whole by
`dispatchWriteTimeout`), records them, then runs the edit, whose
transaction checks the reads. The ticket page's open asks for a refresh
when the snapshot's `readAt` is older than 10 minutes (Owner decisions,
5); nothing else reads GitHub by itself: no timer, no poller, no webhook.

Errors: `ErrNotFound` is `404 external_not_found` ("GitHub has no issue
#612 in rocksolidlabs/conductor, or the token cannot see it"),
`ErrUnauthorized` `401 token_refused` (the Settings card shows the token
refused), `ErrForbidden` `403 token_permission` with the permission
named, `ErrRateLimited` `429 rate_limited` with `Retry-After`, `ErrMoved`
`409 moved`, `ErrUnavailable` `502 tracker_unavailable`, `ErrBound` `502
tracker_bound`, no token for the host `412 no_token`, a URL no adapter
takes `400 not_a_tracker_url`. Each leaves the store as it was, but the
read's error words on a link's `link_reads` row.

**Publish** (`dispatch_publish.go`), the admin's alone: no Brakeman grant
names it, `actable` never wraps it, and an agent's session token is
refused (Owner decisions, 8).

| Route | Body | What it does |
|---|---|---|
| `GET /api/dispatch/tickets/{key}/publish/offer` | none | the parts the composer may offer, each as the exact Markdown it would insert: `deliveries` (the live delivery links' URLs and notes), `checks` (the delivered candidate's verification receipt: each check's name and passed or failed, no output), `key` (the ticket's key and title), and the targets; never a comment, a finding, a plan's text, a constraint, an attachment or a brief |
| `POST /api/dispatch/tickets/{key}/publications` | `{opId, byName, target, body, parts, closeIssue}` | `publication.prepare`; `201 {receipt, publication, preview}`, `preview` the exact bytes to be posted (the body, a blank line, the marker `<!-- conductor-publication:<id> -->`) and its SHA-256 |
| `POST /api/dispatch/publications/{id}/send` | `{opId, expectedRev, byName, bodySha256}` | below; `200 {receipt, publication}` |
| `POST /api/dispatch/publications/{id}/check` | `{}` | settles a `sending` one (below) now |
| `POST /api/dispatch/publications/{id}/discard` | `{opId, expectedRev, byName}` | `publication.discard` |

Send: `BeginSend` commits `sending`; then `Comment` posts the preview's
bytes. A `201` is settled `sent` with the comment's id and URL; then, if
`closeIssue`, `Close` is sent (idempotent on GitHub's side) and its
answer settled as `closed_remote`, a refused close saying so on the
publication without undoing the comment. A refusal that proves nothing
was posted (`401`, `403`, `404`, `410`, `422`) is settled `failed` with
its words. Anything else (a timeout, a reset connection, a `5xx`, the
server stopping) leaves the row `sending`, and the settle runs:
`FindComment` for the marker by the token's own login since
`sending_at` minus a minute; `yes` is `sent`; `no` is `unsent` only once
`sending_at` is at least `settleQuiet` (30 s) old, so a comment GitHub
had accepted but not yet listed is not taken for absent; `unknown` stays
`sending` and says why. The server settles every `sending` row at start
(`settlePublications`, after the store opens, one at a time, never
sending), and the page's Check button settles one. Nothing ever sends a
publication again but the person's Send on an `unsent` or `failed` one,
which goes through `BeginSend` again with the same marker, so even a
second send whose first had in fact landed is found by the settle that
precedes a resend: `BeginSend` on a row whose last settle was `unknown`
is refused until it is `unsent`.

**The guide and the events.** `guide.go` gains the rows
`tracker_status`, `tracker_set` (CLI only: no MCP tool takes a token),
`ticket_import`, `ticket_link_issue`, `ticket_refresh`,
`ticket_authority`, `link_read`, `publication_prepare`,
`publication_send` (Scope `admin`, no MCP tool: a person sends; Owner
decisions, 8) and `publication_check`, each with its bounds, so the
operating guide, the CLI's help and Brakeman's reading of what this build
supports come from the one table. The `dispatch` event gains the entities
`publication` and `link_read`, within the event's existing per-entity
bound (`maxDispatchEach`), measured again in `events_test.go`.

**What an attempt reads.** `GET /api/sessions/{id}/ticket` (`ticket_read`)
shows the linked issue's URL, GitHub's state and `readAt` beside the
ticket, as the admin's read does, and nothing of the token or the
publications' bodies; the package is what the run follows.

### `internal/cli`, `internal/agents`

`conductor trackers` (lists `GET /api/trackers`), `conductor trackers
set [--host H]` (reads the token from standard input, never from a flag
or an argument, which another user's `ps` can read, and refuses a
terminal echo by reading with `term.ReadPassword` when standard input is
a terminal), `conductor trackers remove [--host H]`; `conductor dispatch
import URL --project KEY [--no-images]`, `link-issue KEY-n URL`,
`unlink-issue KEY-n`, `refresh KEY-n`, `authority KEY-n FIELD
github|dispatch`, `read KEY-n LINK-ID`, `publish KEY-n --target
linked|LINK-ID [--body-file F] [--with deliveries,checks,key]
[--close completed|not_planned]` (prepares and prints the preview with its
digest), `publish send PUBLICATION-ID --digest SHA256`, `publish check
PUBLICATION-ID`; `conductor dispatch plan propose` gains `--repro` and
`--evidence LINK-ID` (repeatable). The skill (`internal/agents/skill.go`)
says an agent may read the ticket's linked issue in its package and in
`ticket_read`, and that publishing to GitHub is the person's: no tool of
the agent's posts there. Completion and usage text for each.

### `web/app`: Settings, import, the GitHub panel, links, publish

- `components/SettingsGitHub.vue` (a card in `pages/settings.vue`,
  `data-settings-section="github"`): the field (a password input, never
  echoed back), the host, Check and save, the saved account, the
  fingerprint, Remove; the words for an environment token.
- `components/DispatchImport.vue` (a modal from the project page's
  header, the new-ticket menu and the palette's `>` row "Import from
  GitHub"): the URL field, the preview (`…/external/preview`), the
  ticket's own fields, Import.
- `components/DispatchExternal.vue` (on `DispatchTicketPage.vue`, under
  the title): the linked issue, GitHub's state in its own chip, never the
  status's colour, `readAt` with Refresh, the owners with Take over and
  Give back to GitHub, "GitHub's description differs", the images'
  attachments, the repro steps folded; Link a GitHub issue and Unlink.
  The title and description editors are read-only where GitHub owns them,
  with the words above.
- `DispatchLinks.vue`: a GitHub link's read (`open`, `closed`, `merged`
  with its time; a pull request's checks as a row of chips; the read's
  age and Read again; the error's words).
- `DispatchClosePolicy.vue` and `DispatchProjectSettings.vue`: `merged`
  offered and explained ("a delivery pull request GitHub says is
  merged").
- `components/DispatchPublish.vue` (a modal from the GitHub panel and
  from a GitHub link's menu): the target, the outcome text, the offered
  parts as checkboxes inserting their exact Markdown, Close the issue,
  the preview of the exact bytes with the marker said in words, Publish;
  then the publication's state; `DispatchPublications.vue` lists them on
  the ticket page with Check, Send again (an `unsent` or `failed` one)
  and Discard.
- `DispatchPlanPropose.vue` and `DispatchPlanBody.vue`: Steps to
  reproduce and the evidence links in the proposal and in a revision's
  body; the run page's header (`pages/runs/[run].vue`) adds "· GitHub
  owner/repo#612" after "Launched from CND-12 · plan r2" when the package
  has a source.
- `composables/useTracker.ts`; `utils/tracker.ts` (a GitHub URL's parts,
  as `tracker.Parse` reads them; the owners' words; a read's age) and
  `utils/publication.ts` (the offered parts' insertion, the preview's
  bytes, the states' words).
- The palette: the `>` row "Import from GitHub"; a GitHub URL pasted into
  the search finds the ticket linked to it (`Query.External`). DS1's
  rule stands: no new prefix and no new verb.
- The e2e harness (`web/e2e/fake-github.ts`): a fake GitHub on loopback
  serving the routes the adapter uses from scripted items, counting every
  write, which `web/e2e/server.ts` starts and points the server at with
  `CONDUCTOR_TRACKER_LOOPBACK=1`.

### Docs

`docs/protocol.md`: the Dispatch section's rows for every route above
with its body bound (64 KiB but 16 KiB plus the envelope for a
publication), its answers and refusals; the launch package's version 2
and `ticket.md`'s new parts; the `dispatch` event's `publication` and
`link_read`. `docs/architecture.md` (Dispatch: GitHub linking),
`docs/repository-map.md` (`internal/tracker`, `internal/tracker/github`,
the store's version 4), `README.md` (Dispatch: GitHub), and the
`docs/features.md` entry. `SECURITY.md` is not changed: the token's
handling is described in `docs/architecture.md`.

## Events, orderings and invariants

**What each store holds.** Dispatch's database holds the linked issue
(the four reserved columns), the field owners, the snapshot, the links'
reads (a cache), the publications and their states. The data directory's
`credentials/github.json` holds the token, nothing else holds it, and no
record of Dispatch, the ledger, the sync store or an export ever carries
it. GitHub holds the issue, the pull request and, once sent, the comment.
The ledger holds the package byte for byte, as DS2 made it.

**Reads.** A read is a request to GitHub, then one transaction. Nothing
holds a database transaction across a network call. A read's result is
written only if the ticket (or link) it was asked for still holds the
same linked item when the transaction runs (the node id compared in the
transaction); a link removed meanwhile drops the read. Two refreshes of
one ticket at once each run their own transaction; the later read
commits last and wins the scalar fields, which is what "last writer wins"
gives; a refresh is never merged field by field with another.

**Field owners.** A field GitHub owns is written by `ticket.refresh` and
`ticket.authority` (giving it back) alone; `ticket.edit` refuses it
(`external_owned`). A field Dispatch owns is never written by a refresh.
Labels, assignees, milestone, owner, priority, horizon and status are
always Dispatch's (Owner decisions, 4). So:

- I1. The ticket's status, its acceptance and its close are never set by
  anything GitHub said. A refresh that finds the issue closed writes the
  snapshot, and the page shows it; it changes no Dispatch status.
- I2. A refresh never writes a Dispatch-owned field, whatever GitHub
  says.
- I3. A refresh that changes a GitHub-owned title or description moves
  the ticket's intent digest, so an approval made before is stale (DS2's
  `stale_plan` at a launch) and a run already launched keeps its package
  and shows "the ticket changed after this run launched" (DS2's card),
  never an update of the run (the Program, section 1.5).

**Publications: the state machine.**

| From | Event | To | Written before what |
|---|---|---|---|
| (none) | `publication.prepare` | `draft` | the row and its receipt, committed; nothing sent |
| `draft`, `unsent`, `failed` | `publication.send` (`BeginSend`) with the previewed digest | `sending` (`tries` + 1) | committed before the request to GitHub |
| `sending` | GitHub's `201` | `sent` | the request answered; then the settle's commit |
| `sending` | a refusal that proves nothing was posted | `failed` | as above |
| `sending` | a settle that finds the marker | `sent` | the comments listed; then the commit |
| `sending` | a settle that read the whole window, no marker, at least 30 s after `sending_at` | `unsent` | as above |
| `sending` | a settle that could not read the whole window | `sending` (its words updated) | nothing changes state |
| `sent` | the close answered | `sent`, `closed_remote` set | the close's request answered; then the commit |
| `draft`, `unsent`, `failed` | `publication.discard` | `discarded` | committed |

- P1. **Exactly one comment per publication.** The request to post is
  made only after `sending` is committed, and the state leaves `sending`
  only through a settle. A crash, a lost reply or a timeout leaves
  `sending`; the server never posts from `sending`; a person's Send needs
  `unsent` or `failed`, and `unsent` needs a complete listing that lacks
  the marker, made at least `settleQuiet` after the send began. The
  marker is the publication's id, unique, posted with the body, so a
  comment found with it is that publication's and no other's.
- P2. **Only on purpose.** The only path that posts or closes on GitHub
  is `POST /api/dispatch/publications/{id}/send` with the admin's token,
  the publication's revision and the digest of the preview the person
  saw. No status change, close, acceptance, run's end, candidate,
  delivery link, refresh, import, Brakeman turn, agent tool, webhook or
  start of the server sends anything: the settle at start lists comments
  and never posts. `TestNothingPublishedBySideEffect` drives each of
  those against the fake GitHub and asserts its write count is zero.
- P3. **Only what was chosen.** The body is the person's text and the
  parts the person ticked, each inserted as the exact Markdown the offer
  showed; the server adds the marker and nothing else, and refuses a send
  whose digest is not the stored body's. The offer never contains a
  comment, a review finding, a plan's constraints or note, a decision, an
  attachment, a context excerpt, a brief or a check's output.
  `TestPublicationCarriesOnlyChosen` fills a ticket with a private comment,
  a finding, constraints, an attachment and a brief, publishes with every
  part ticked, and asserts the posted bytes contain none of them.
- P4. A publication's content never changes once prepared (immutable
  fields); a corrected text is a new publication, the old one discarded.

**Imports.** An import is several operations, each with its receipt: the
ticket (`opId`), each image (`<opId>~i<n>`), the snapshot's images
(`<opId>~r`). The ticket is committed before any image is downloaded; an
image's file is published before its row (DS1's attachment order); a
resend completes what is missing and repeats the rest. An import whose
first operation is refused (`external_linked`) downloads nothing.

**Links' reads.** A read is written after the link's add commits; a link
removed drops its read in the remove's transaction; a read for a link
since removed is dropped. The close with `merged` reads first and checks
in its own transaction that the read it relies on is the link's current
one and at most 5 minutes old, and records it in `closed_on`.

**The token.** Saved only after GitHub accepted it (`Whoami`), by an
atomic rename; removed by deleting the entry; read per call. A call in
flight when the token is removed finishes with the token it read; the
next is `412 no_token`.

**Migration.** Version 3 to 4 as described under the schema: two
tables, three indexes, one column with its default, no row read or
rewritten, no `migrate` row; the conservative rule (nothing an older
version kept is dropped or reinterpreted) holds, and a version 4
database opened by version 3 code opens read-only, as DS1's rule for a
newer database says. The package's version 2 is read by the engine
alongside version 1; a run launched before the upgrade keeps its version
1 package and its continuations carry it unchanged.

## Out of scope

- Hosted sign-in to GitHub (an OAuth or GitHub App installation), a
  credential shared by a team, GitLab, Linear or any other tracker: IN1b,
  a Team item in the hosted services' repository, whose plan adds the
  seam its provider registers through (Owner decisions, 10).
- Synchronising both ways, or any write to GitHub other than a
  publication's comment and its optional close: no title, body, label,
  assignee, milestone or state is pushed; no GitHub label or assignee is
  mapped onto Dispatch's (the Program, section 7, keeps broad
  bidirectional synchronisation out of the near term).
- Watching GitHub: webhooks, a poller, a timer, notifications from
  GitHub. Reads happen when a person asks or opens the page.
- Bulk import (a whole repository's or a query's issues), import of
  comments or of a pull request's review comments, a ticket's comments
  published as GitHub comments.
- Brakeman publishing, or a grant that would let it: BK2's closed list of
  grants (`docs/plans/brakeman.md`) does not gain one. Brakeman reads a
  ticket's linked issue and links' reads through the ticket's read, as it
  reads the rest of the ticket.
- Running CI: a pull request's checks are read, never started, retried
  or cancelled; a failed CI job becomes a failed-check request only
  through RV1's review (`docs/plans/review-repair.md` owns the request's
  shape and the Review view; this item owns what an external job is and
  how it is read).
- Opening, pushing or merging a pull request from Conductor (RV1 and DS2
  leave merging to people; so does this item).
- Replicating the new fields between machines (CS1 applies the classes
  written here).
- A `closed on GitHub` close requirement: GitHub's state stays a fact
  shown beside Dispatch's status, never a condition of it.

## Tests per surface

Every Go PR runs `make lint`, `go test -race -count=1 -timeout 30m ./...`
and `make lint-static`; every web PR `npm --prefix web run typecheck`,
`npm --prefix web test` and `make test-e2e` (the relevant spec alone
first while iterating); a PR that changes nothing under `go.mod` or
`web/package*.json` regenerates no notices and says so. The named tests
each PR adds:

- `internal/tracker`: `TestParseGitHubURLs` (issue, pull, `owner/repo#n`
  with a default space, Enterprise Server hosts, the remote forms, and the
  refused shapes: another host, a query, a fragment, a path past the
  number, `http` without the loopback switch), `TestRepro` (every heading
  form, levels, the next heading's end, the 8 KiB cut at a line end,
  none), `TestImageHost` (every admitted host, `http` refused, a look-alike
  host refused, a path off `/user-attachments/assets/` on `github.com`
  refused), `TestCredentialNeverPrinted` (`%v`, `%+v`, `%#v`, `slog` text
  and JSON handlers).
- `internal/tracker/github`, against `githubtest`: `TestReadIssue` and
  `TestReadPull` (the fields, `body_html`'s images, the captured
  fixtures), `TestReadNotModified`, `TestReadFollowsRename` and
  `TestReadMovedRefused`, `TestErrorsMapped` (401, 403 with and without a
  rate limit, 404, 410, 422, 429, 5xx, a timeout through the fake's gate,
  a reset connection), `TestBodiesBounded` (each bound plus one byte),
  `TestTokenOnlyToTheAPIHost` (a redirect to an image host and a log
  host carries no `Authorization`; a redirect to any other host is
  refused; the fake records every request's headers),
  `TestErrorsCarryNoQuery`, `TestChecksAtCommit` (check runs and
  statuses), `TestCheckLogTail`, `TestCommentAndFind` (the marker on page
  1 and on page 7, by another author ignored, eleven pages `unknown`),
  `TestCloseReason`, `TestNoRetry` (a refused request is made once).
- `internal/dispatch`: `TestMigrationThreeToFour` (a version 3 database
  made by the version 3 code, every row read back unchanged, the version
  4 shape read back from `sqlite_master`), `TestSchemaIndexes` (the list
  extended), `TestImportTicket`, `TestImportCutsTitleAndBody`,
  `TestExternalLinkedOnce`, `TestLinkAndUnlink`,
  `TestEditRefusedWhereGitHubOwns`, `TestRefreshWritesOnlyGitHubFields`,
  `TestRefreshNothingNewWritesNoChange`, `TestRefreshMakesApprovalStale`,
  `TestTakeOverAndGiveBack`, `TestReadsAreNotLogged` (no `changes` row, no
  receipt, `OnChange` told), `TestRemovedLinkDropsRead`,
  `TestCloseRequiresMerged` (met, an old read, a closed-unmerged pull
  request, an external link instead of a delivery, `closed_on` written),
  `TestPublicationStates` (every transition of the table and every
  refused one), `TestSendNeedsThePreviewedDigest`,
  `TestPublicationTriesBound`, `TestQueryExternal`,
  `TestRulesClassesVersionFour` (every new field's class; `link_reads`
  derived), `TestSnapshotBound` and `TestTicketReplyBound` (the new parts
  at their bounds, measured as DS1 measures), `TestPackageVersionTwo`
  (the source part, the revision's choice, `stale_plan` for a chosen link
  removed, `MaxPackage` measured).
- `internal/crew`: `TestPackageVersionTwoRead`, `TestPackageVersionOneStillRead`,
  `TestTicketMarkdownSource` (the three parts; a golden file),
  `TestPackageSourceBounds`.
- `internal/gitrepo`: `TestRemoteURL` (a repository's `origin` and a named
  remote; a worktree's common directory; no git process started, which
  `gitclitest`'s recorder asserts).
- `internal/config`, `internal/export`: `TestGitHubTokenFromEnvironment`
  (never in the file, the `slog` form `set`), `TestLeftOutCredentials` (an
  export of a data directory with a token carries no `credentials/`, and
  an archive carrying one is refused by `Install`).
- `internal/api`: `TestTrackerTokenSavedNeverServed` (every route's
  answer and every event scanned for the token), `TestTrackerTokenChecked`
  (refused, saved, the same token again unchanged through `share.Equal`),
  `TestTrackerTokenNotSynced` (a registered fake sync store sees every
  document of an import, a refresh and a publication and never
  `credentials/`), `TestTrackerTokenNeverLogged` (every log line of the
  whole flow), `TestTrackerRemoveTakesEffect`, `TestImportRoute`,
  `TestImportImages` (the bound of 8, the types, a skipped SVG, a signed
  URL's query stored nowhere), `TestImportResendCompletes` (a crash after
  the ticket, before the images, through a seam), `TestRefreshRoute`,
  `TestRefreshMovedRefused`, `TestLinkReadOnAdd`, `TestCloseReadsMerged`,
  `TestPublishOnce` (double send, concurrent sends), `TestPublishAcrossCrash`
  (the fake holds the post on its gate, the server stops, starts again,
  the settle finds the marker: one comment, `sent`, no second post),
  `TestPublishUnknownThenUnsent` (the fake drops the post; the settle
  waits `settleQuiet` through the server's clock seam, then `unsent`; Send
  again posts once), `TestPublishCloseRefusedKeepsComment`,
  `TestNothingPublishedBySideEffect` (P2's list),
  `TestPublicationCarriesOnlyChosen` (P3),
  `TestPublishRefusesBrakemanAndSessions` (a Brakeman turn's principal and
  an attempt's session token are refused on every write route of this
  item), `TestTrackerErrorsMapped`, `TestGuideRowsTrackers`,
  `TestDispatchEventEntities` (the bound measured with the two entities),
  `TestRunsPackageCarriesSource` (a launch from a ticket with a linked
  issue; the attempt's `ticket.md` read from its worktree).
- `internal/cli`: `TestTrackersSetReadsStdin` (no flag takes a token; the
  argv of the process holds none), `TestDispatchImportCommand`,
  `TestDispatchPublishPreviewThenSend` (no send without `--digest`; the
  wrong digest refused), the completion and usage goldens.
- `internal/agents`: the skill's golden.
- vitest: `utils/tracker.test.ts` (the URL parts, the same table as
  `TestParseGitHubURLs`), `utils/publication.test.ts` (each part's
  insertion; the preview's bytes; nothing offered but the parts),
  `composables/useTracker.test.ts`.
- Playwright, against `fake-github.ts`: `tracker-import.spec.ts` (the
  token saved in Settings and never shown again; Import an issue with two
  images and steps to reproduce from the palette; the GitHub panel; the
  title editor read-only; the fake's title changed, Refresh, the title
  follows and the history says so; Take over; GitHub's issue closed,
  Refresh, the ticket's status unchanged and GitHub's chip "Closed"),
  `tracker-links.spec.ts` (a delivery link to a pull request read as
  open, then merged; the project's policy `merged`; Close refused, then
  met), `tracker-publish.spec.ts` (compose, preview with the marker said,
  Publish twice quickly: the fake counts one comment; reload shows
  Published; a held post and Check), `tracker-package.spec.ts` (propose
  with Steps to reproduce and an evidence link, approve, launch with the
  stub agent, the attempt's `ticket.md` has the parts).
- PR-10 adds `TestExternalCheckRef` (`internal/dispatch`: `source:
  external` accepted with its shape), `TestPrepareFromExternalJob` and
  `TestExternalJobNotTheCandidates` (`internal/api`: a job at another
  commit than the delivered candidate's is refused, as RV1 refuses a
  receipt of another attempt), and `review-external.spec.ts`.
- By hand, what tests cannot cover: GitHub itself. A fine-grained token
  on a scratch repository and on a private one, against github.com:
  Settings accepts it and refuses a revoked one; an issue with a pasted
  screenshot imports with its image from the private repository;
  a renamed repository is followed; a pull request merged is read
  merged; a publication posts one comment and closes the issue; the
  server's log over the whole session holds no token and no signed URL's
  query (`grep` for the fingerprint's token prefix, `jwt=` and
  `X-Amz-`); and from the Windows app's own server the same, since its
  server reaches GitHub from inside WSL (no networking is asked of the
  person there; the request is an ordinary outgoing HTTPS request).

## Boxcar constraints

1. The engine starts an attempt only through `crew.Launcher`: not
   touched; this item starts no attempt and no process. A run launched
   from a ticket with a linked issue launches exactly as DS2 launches.
2. `session.Local` drives a `session.Process`: not touched; nothing here
   reaches a session, a pid or a tty.
3. An attempt's context is files relative to its worktree: honoured and
   extended; the source, the steps to reproduce and the evidence links
   are parts of the package, written as `ticket.md` and `package.json`,
   and the images as files in `.conductor/attachments/`, through the
   attempt's `os.Root` as DS2 writes them; nothing an attempt needs is
   only in the server's memory, and nothing an attempt needs is on GitHub
   (the package holds what was read).
4. The agent talks to the server over the network: not touched; an
   agent never reaches GitHub through Conductor and is given no token.
5. Checks go through `internal/proc`: not touched; a CI job read from
   GitHub is evidence another system made, kept with `source: external`
   and never run, and the GitHub calls are HTTP requests, not checks.
6. Platform splits stay platform tags, the UI shows capabilities: not
   touched; nothing here differs by platform or depends on a provider.
7. Evidence rows gain `where` in C10: honoured; this item adds no ledger
   evidence row; an external job is told apart by its `source`, never by
   a `where` value of the Runtime's, and nothing assumes a native
   process.
8. `hostagent` is a launch site as much as `api`: not touched; the
   package reaches an attempt as files, wherever the attempt runs.

## Open-core checklist

- [x] Article 1: no licence or plan check in `internal/tracker`,
  `internal/dispatch`, the routes or the pages; the condition is the
  person's own token.
- [x] Article 2: linking, importing, reading, the package's source and
  publishing ship whole in the open-source build with no account (the
  Program puts own-token GitHub linking in the free tier, decision D10).
- [x] Article 3: nothing is gated; there is no flag a fork could flip.
- [x] Article 4: a publication is a person's explicit act with a
  receipt; Remove revokes the token at once; the records are rows of the
  plain store, exported whole (the token left out); stop, inspect,
  revoke and the required approvals are unchanged.

## The PRs

Each PR is one issue and one branch cut from `main`, a draft PR at its
first commit, the checks for its surfaces before every push, its body
ticking the open-core checklist (articles 1 to 4) and naming the agent
that made it; each leaves `main` working and green on its own (a store
operation with no route yet is reachable from its tests; a route with no
page from the CLI and the guide). Every PR is in this repository.

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with |
|---|---|---|---|---|---|---|
| PR-1 | dispatch: the linked issue, field owners, links' reads and the merged close (schema version 4) | the migration 3 to 4 (two tables, three indexes, `closed_on`), the reserved columns' meaning, `ExternalSnapshot` and its bound, `ticket.import`, `ticket.link_external`, `ticket.unlink_external`, `ticket.refresh`, `ticket.authority`, `ticket.edit`'s `external_owned`, `external_linked`, `RecordRead`, `RequireMerged` and `closed_on`, `Query.External`, the ticket read's new parts, the field classes and `derived` in `rules.go` | `internal/dispatch` | the `internal/dispatch` tests above but the publications' and the package's | none (RV1's PR-1 only for the version number: whichever merges second takes the next) | PR-2, PR-3 |
| PR-2 | tracker: the adapter contract and the GitHub adapter | `internal/tracker` (types, errors, `Parse`, `Repro`, `ImageHost`, `Credential`), `internal/tracker/github` (the client, reads, images, checks and logs, comments, find, close, rate limits, redirects), `githubtest` with the captured fixtures | `internal/tracker` | the `internal/tracker` and `internal/tracker/github` tests above | none | PR-1, PR-6 |
| PR-3 | api: the GitHub token, kept and left out | `trackers.go` (the three routes, the file, `Whoami` before saving, per-call reads, `share.Equal`), `CONDUCTOR_GITHUB_TOKEN`, `CONDUCTOR_GITHUB_HOST` and `CONDUCTOR_TRACKER_LOOPBACK` in `internal/config`, the left-out row, `gitrepo.RemoteURL`, the guide's `tracker_status` and `tracker_set` rows, `docs/protocol.md`'s rows | `internal/api`, `internal/config`, `internal/export`, `internal/gitrepo`, `docs` | `TestTrackerToken*`, `TestGitHubTokenFromEnvironment`, `TestLeftOutCredentials`, `TestRemoteURL` | PR-2 | PR-1 |
| PR-4 | api: import, link, refresh, field owners and links' reads | `dispatch_external.go` (the preview, import with its images and resend, link, unlink, refresh, authority, a link's read, the read after `link.add`, the close's reads for `merged`), the error mapping, the ticket read's new parts in `MaxTicketReply`, `ticket_read`'s linked issue, the `link_read` event entity, the guide's rows, `docs/protocol.md`'s rows | `internal/api`, `docs` | `TestImport*`, `TestRefresh*`, `TestLinkReadOnAdd`, `TestCloseReadsMerged`, `TestTrackerErrorsMapped`, `TestDispatchEventEntities` (`link_read`), the guide's golden | PR-1, PR-2, PR-3 | none |
| PR-5 | dispatch, api: publications sent once | `publications.go` (the table's operations, states and bounds, `publication` in `rules.go` and the log), `dispatch_publish.go` (the offer, prepare, send, settle, check, discard, `settlePublications` at start, the close), the `publication` event entity, the guide's rows, `docs/protocol.md`'s rows | `internal/dispatch`, `internal/api`, `docs` | `TestPublication*`, `TestSendNeedsThePreviewedDigest`, `TestPublish*`, `TestNothingPublishedBySideEffect`, `TestPublicationCarriesOnlyChosen` | PR-4 | PR-8 |
| PR-6 | dispatch, crew: the package's source, steps to reproduce and evidence (package version 2) | `Package.Source`, the revision body's `source` choice and its `stale_plan`, `PackageVersion` 2 in both packages with version 1 read, `ticketMarkdown`'s three parts, `docs/protocol.md`'s package rows | `internal/dispatch`, `internal/crew`, `docs` | `TestPackageVersionTwo`, `TestPackageVersion*Read`, `TestTicketMarkdownSource`, `TestPackageSourceBounds` | PR-1 | PR-2 |
| PR-7 | cli, agents, api: the commands, the plan's choices over HTTP and the skill | `conductor trackers` and its `set` from standard input, `dispatch import`, `link-issue`, `unlink-issue`, `refresh`, `authority`, `read`, `publish`, `publish send`, `publish check`, `plan propose --repro --evidence`, the propose route's `source` field (`dispatch_plans.go`), `TestRunsPackageCarriesSource`, the skill's text, usage and completion | `internal/cli`, `internal/agents`, `internal/api` (`dispatch_plans.go` and `guide.go` only) | the `internal/cli` and `internal/agents` tests above, `TestRunsPackageCarriesSource` | PR-4, PR-5, PR-6 | PR-8 |
| PR-8 | web: GitHub in Settings, import, the GitHub panel and links' reads | `SettingsGitHub`, `DispatchImport`, `DispatchExternal`, `DispatchLinks`' reads, `merged` in the close policy and the project's settings, the editors read-only where GitHub owns, the palette's row and URL search, `useTracker`, `utils/tracker.ts`, `fake-github.ts` and the harness, `tracker-import.spec.ts`, `tracker-links.spec.ts` | `web/app/components`, `web/app/composables`, `web/app/utils`, `web/app/pages`, `web/e2e` | `utils/tracker.test.ts`, `useTracker.test.ts`, the two specs | PR-4 | PR-5, PR-7 |
| PR-9 | web, docs: publish, the package's choices, the run page; the features entry | `DispatchPublish`, `DispatchPublications`, `utils/publication.ts`, Steps to reproduce and evidence in `DispatchPlanPropose` and `DispatchPlanBody`, the run page's header, `tracker-publish.spec.ts`, `tracker-package.spec.ts`; `docs/architecture.md`, `docs/repository-map.md`, `README.md`, the `docs/features.md` entry for PR-1 to PR-9 | `web/app/components`, `web/app/utils`, `web/app/pages`, `web/e2e`, `docs`, `README.md` | `utils/publication.test.ts`, the two specs | PR-5, PR-7, PR-8 | none |
| PR-10 | dispatch, api, web: a delivery pull request's CI job as a failed-check request | `check_ref`'s `source: external` defined and accepted (`{source, receipt: "github:<host>/<repo>/check-runs/<id>", name, conclusion, url, headSha, app, tail, chosen, quote}`, the API filling it from a fresh read, refusing a job not at the delivered candidate's commit), the prepared request from an external job, RV1's failed-check panel listing a delivery's external jobs, `review-external.spec.ts`; the features entry completed | `internal/dispatch`, `internal/api`, `web/app/components`, `web/e2e`, `docs` | `TestExternalCheckRef`, `TestPrepareFromExternalJob`, `TestExternalJobNotTheCandidates`, `review-external.spec.ts` | PR-4, PR-9; RV1's PR-1, PR-3, PR-4 and PR-7 (`docs/plans/review-repair.md`) | none |

PR-1 to PR-9 build on what `main` has today (DS1, DS2) and wait on no
other item but for PR-1's schema number. PR-10 is the one that waits on
another item: RV1 owns the check finding's shape, the prepared
failed-check request and the Review view, and reserved `source:
external` for this plan; it is cut into its issue now and started once
RV1's four PRs have merged. PR-9 adds the `docs/features.md` entry for
what PR-1 to PR-9 ship; PR-10, the last, completes that entry with the
external jobs, so the entry is whole when the last PR merges.

Sizes, as the owner asked (2026-11-05: a row is a batch, 300 to 800
changed lines, at most about 1,500 with its tests): PR-1 about 700 lines
and 800 of tests; PR-2 about 750 and 700 (the fixtures counted apart, as
data); PR-3 about 400 and 450; PR-4 about 750 and 700; PR-5 about 650
and 750; PR-6 about 350 and 450; PR-7 about 500 and 400; PR-8 about 800
and 450; PR-9 about 650 and 400, with the docs; PR-10 about 450 and 450.

## Owner decisions

Dated 2026-11-05. None of the Program's open decisions (section 3.2)
blocks this item: O4, O5 and O8 belong to SW1, CG1 and the beta, and O10
to CS1, which applies the field classes written here. Each decision below
is a default the owner can overturn with a dated row in the Program's
status log, its convention for a decision the owner did not make (the
Program, section 3.1); the reasoning is why it was taken. No question is
left open.

1. **The token is pasted in Settings or given in the environment, and
   kept in `credentials/github.json`.** Settings is where a person
   already gives the server what it needs, and the environment serves a
   headless `conductor serve`; a file of its own, left out of every
   export and never named by the sync allowlist, keeps the one secret in
   one place.
2. **Conductor never reads another program's credentials** (the `gh`
   CLI's token, a git credential helper, a keychain). The Program's
   requirement is a token the user supplies; taking one the person did not
   hand over is the kind of side effect this item exists to refuse.
3. **GitHub's REST API over `net/http`, no client library**, and
   `golang.org/x/net/html` (a module already required) to read a
   rendered body's images. AGENTS.md's stdlib-first rule, and the dozen
   routes used do not justify a new module; `make notices` is unchanged.
4. **Only the title and the description have an owner to choose.**
   Imported, GitHub owns both; linked, Dispatch owns both; the person
   moves either. Status, labels, assignees, milestone and owner are always
   Dispatch's, GitHub's shown from the snapshot: mapping logins to people
   and labels to labels is the start of the synchronisation the Program
   keeps out, and a ticket's status is execution's, never GitHub's
   (decision D10's consequence and the work model's separate facts).
5. **Reads are on demand, and on opening a ticket page whose snapshot is
   older than 10 minutes.** No poller and no webhook: a webhook needs an
   address GitHub can reach, which a person's machine often is not, and
   the on-demand read answers every question the page asks.
6. **At most 8 images an import, each at most 16 MiB, PNG, JPEG, GIF or
   WebP, from GitHub's own image hosts only.** Eight is the package's own
   bound on attachments handed over and DS1's 16 MiB its file bound;
   SVG is refused as DS1 refuses inline SVG; the host list is what keeps
   an issue's text from steering the server's requests.
7. **Steps to reproduce are the issue's own section, found by its
   heading, at most 8 KiB, handed over when the revision says so (on by
   default when found).** Issue forms make such a heading, so the common
   case needs no person; the person's choice in the revision keeps the
   approval covering what is handed over.
8. **Publishing is a person's alone, composed and previewed, sent once,
   closing the issue only when ticked (off by default), and the ticket's
   key included only when ticked.** No Brakeman grant, no MCP tool and no
   agent route posts to GitHub; this is the acceptance boundary's "on
   purpose" and "nothing private leaks as a side effect" made literal. The
   hidden marker is the publication's random id, which says nothing of
   the ticket.
9. **`merged` is offered as a close requirement; the defaults are
   unchanged** (`acceptance` for a native project, none for an external
   one, as DS1 decided). A person chooses to tie a close to GitHub; the
   default does not.
10. **The adapter contract lives in `internal/tracker`, system-neutral,
    and no `ext` interface is added.** IN1b's plan lifts it into an `ext`
    seam with `ext`'s own types when a hosted connector exists to register
    there, which adds systems and credentials beside this item's own-token
    path and never removes it (article 7); a seam with no provider to
    register would be the first in `ext` with nothing behind it.
11. **One token per host, at most four hosts; `github.com` and GitHub
    Enterprise Server by its address, `https` only.** Most people use
    one; a few use github.com beside a company server; four bounds the
    file.
12. **A pull request may be imported as a ticket as an issue is** (its
    kind `change`), since the shapes are the same and reviewing an
    outside contribution is work a ticket can hold.
13. **External CI jobs are read only for a ticket's delivery links to
    pull requests, at the pull request's head commit, with a log tail
    only from GitHub Actions.** The job a review cares about is the
    delivered candidate's; other apps' checks give their name, result and
    link, which is what GitHub offers for them.

## Acceptance

The acceptance boundary (the Program, section 4.3): a linked GitHub issue
becomes a ticket, its outcome is published once, on purpose, and nothing
private leaks as a side effect. It is observed by hand against github.com
with the owner's own token, on the open-source build with no account,
after PR-9 merges (PR-10's part once RV1's PRs and PR-10 have):

1. In Settings the owner saves a fine-grained token for a repository of
   their choosing; the card shows the account and never the token again;
   `credentials/github.json` is the only file of the data directory that
   holds it, and an export made then has no `credentials/`.
2. An issue of that repository with a pasted screenshot and a "Steps to
   reproduce" section is imported from the palette: the ticket has
   GitHub's title and description owned by GitHub, the image as an
   attachment, the steps in the GitHub panel, GitHub's labels shown and
   not mapped.
3. A plan revision with the steps and the image is proposed, approved
   and launched with a real agent; the attempt's `ticket.md` has the
   Source and Steps to reproduce parts and the image is in
   `.conductor/attachments/`.
4. The issue's title is edited on GitHub; Refresh brings it in, the
   approved revision is shown stale, and the running run keeps its
   package. GitHub's issue is closed and reopened on GitHub; the ticket's
   status never moves.
5. The candidate is delivered as a pull request, linked as a delivery,
   merged on GitHub; with the project's policy `merged`, Close is refused
   before the merge and met after it.
6. The ticket holds a private comment, the plan's constraints and, once
   RV1 is on `main`, a review finding. The owner publishes an outcome
   with the delivery link ticked, presses Publish twice and reloads: the
   issue on GitHub has exactly one comment from the owner's account, and
   its text is the preview's; the issue's timeline shows nothing else
   Conductor did, and none of the private texts is anywhere on GitHub.
7. The server's log over the whole session is searched for the token,
   `jwt=` and `X-Amz-`, and holds none.

Done means: PR-1 to PR-10 merged; the seven observations above made and
recorded in the `docs/features.md` entry with the build they were made
on; any defect they find fixed in this stream before Done (AGENTS.md). The
owner is needed for the token, the repository and the judgement of
step 6's texts; the rest of the observation an agent can drive with the
owner's token in the server's environment, never in a file of the
repository or a log.
