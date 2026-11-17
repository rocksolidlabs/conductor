import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

// A fake GitHub for the tracker specs (Program item IN1a, docs/plans/tracker-linking.md): one plain HTTP server on a loopback port, which
// a server started with CONDUCTOR_TRACKER_LOOPBACK=1 reads as a GitHub Enterprise Server on that host (REST under /api/v3, GraphQL at
// /api/graphql: internal/tracker/github/github.go, endpointFor). It serves every route the adapter calls (read.go, checks.go, comments.go,
// graphql.go, images.go) from items a spec scripts, in the shapes of the Go fake's fixtures (githubtest/fixtures, read from there so the
// two never drift) with the item's values put in; counts each request by route, holds a route at a gate a spec opens, keeps the comments
// posted, and records every request with whether it carried a token, so a spec can say where the token went. No real host, no real token.

const here = dirname(fileURLToPath(import.meta.url))
const fixtures = join(here, '..', '..', 'internal', 'tracker', 'github', 'githubtest', 'fixtures')
function fixture(name: string): Record<string, unknown> {
  return JSON.parse(readFileSync(join(fixtures, name), 'utf8')) as Record<string, unknown>
}

/** An image an item's body shows: served at `/_img/<n>/<path>` on the fake's host (with a signed query, as GitHub's private images are). */
export interface FakeImage {
  path: string
  type: string
  bytes: Buffer
  alt?: string
}

/** An issue or a pull request the fake serves. */
export interface FakeItem {
  n: number
  kind: 'issue' | 'pull'
  /** Its node id; `I_fake<n>` (an issue) or `PR_fake<n>` (a pull request) when not given. */
  node?: string
  title: string
  /** Its Markdown; its images are added to the rendered body (body_html) the server reads them from. */
  body: string
  state: 'open' | 'closed'
  stateReason?: 'completed' | 'not_planned' | null
  merged?: boolean
  mergedAt?: string
  mergeSha?: string
  headSha?: string
  labels?: string[]
  assignees?: string[]
  author?: string
  /** GitHub's updated time; when not given anew, a set moves it a minute past the last one, as GitHub's own does on a change. */
  updatedAt?: string
  images?: FakeImage[]
}

/** A check at a commit: a GitHub Actions job (`job`, with its log), another app's check run (`run`), or a commit status (`status`). */
export interface FakeCheck {
  kind: 'job' | 'run' | 'status'
  id: number
  name: string
  /** `completed`, `in_progress`, `queued` (a status: `pending` when it has no conclusion). */
  status: string
  conclusion?: string
  log?: string
}

/** A route a spec counts or holds. */
export type FakeRoute = 'user' | 'repo' | 'read' | 'pull' | 'checks' | 'log' | 'image' | 'comments' | 'resolve' | 'comment' | 'close'

/** A request the fake received: its route, method and path (never its query), and whether it carried a token. */
export interface FakeRequest {
  route: FakeRoute | 'other'
  method: string
  path: string
  /** The Authorization header's token, '' when none was sent. */
  token: string
}

export interface FakeGitHub {
  /** `127.0.0.1:<port>`: the token's host in Settings, and the host of every item's URL. */
  readonly host: string
  readonly owner: string
  readonly repo: string
  /** The repository's id, as its lookup answers it. */
  readonly repoId: number
  /** An item's page: http://127.0.0.1:<port>/<owner>/<repo>/issues/<n> (…/pull/<n> for a pull request). */
  url(n: number, kind?: 'issue' | 'pull'): string
  /** An item set or changed. */
  set(item: FakeItem): void
  /** The item as the fake holds it. */
  item(n: number): FakeItem | undefined
  /** The checks at a commit. */
  checks(sha: string, list: FakeCheck[]): void
  /** Holds a route's requests until `release` (or `stop`); `reached` resolves once one has arrived. */
  hold(route: FakeRoute): { reached: Promise<void>; release(): void }
  /** How many requests a route answered (or held). */
  count(route: FakeRoute): number
  /** Every request received, in order. */
  requests(): FakeRequest[]
  /** The comments posted on an item. */
  comments(n: number): Array<{ id: number; body: string; author: string }>
  /** A token GET /user (and every other route) answers 401 for. */
  refuse(token: string): void
  /** A token refused before is accepted again. */
  allow(token: string): void
  stop(): Promise<void>
}

/** An item as the fake holds it: its node id and times set. */
type Held = FakeItem & { node: string; updatedAt: string; createdAt: string; closedAt?: string }
/** A GraphQL request's parts. */
type GQL = { query?: string; variables?: Record<string, unknown> }

const REPO_ID = 4000001
/** The account every token the fake accepts belongs to (user.json's id). */
const USER_ID = 9000001

export async function fakeGitHub(o: { owner?: string; repo?: string; login?: string } = {}): Promise<FakeGitHub> {
  const owner = o.owner ?? 'octo-example'
  const repo = o.repo ?? 'widgets'
  const login = o.login ?? 'mona-example'
  const items = new Map<number, Held>()
  const checkSets = new Map<string, FakeCheck[]>()
  const posted = new Map<number, Array<{ id: number; body: string; author: string; createdAt: string }>>()
  const counts = new Map<FakeRoute, number>()
  const log: FakeRequest[] = []
  const refused = new Set<string>()
  const gates = new Map<FakeRoute, { open: Promise<void>; release: () => void; reached: Promise<void>; arrive: () => void }>()
  let nextComment = 7_000_000_001
  let host = ''
  // GitHub's times follow the real clock, an hour behind it, so the minutes each change moves them stay in the past: a comment's time is
  // then in the window a server lists from (a day before its first send), whatever day the spec runs.
  let clock = Math.floor(Date.now() / 1000) * 1000 - 3_600_000

  const pageURL = (n: number, kind: 'issue' | 'pull' = 'issue') => `http://${host}/${owner}/${repo}/${kind === 'pull' ? 'pull' : 'issues'}/${n}`
  const tick = () => new Date((clock += 60_000)).toISOString().replace(/\.\d{3}Z$/, 'Z')

  function bodyHTML(it: FakeItem): string {
    const imgs = (it.images ?? []).map((im, i) => `<p dir="auto"><img src="http://${host}/_img/${it.n}/${im.path}?jwt=fake.signed.${i}" alt="${im.alt ?? im.path}"></p>`)
    const text = it.body
      .split('\n')
      .map((l) => l.replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' })[c]!))
      .join('<br>\n')
    return `<p dir="auto">${text}</p>\n${imgs.join('\n')}`
  }

  function issueJSON(it: Held): Record<string, unknown> {
    const base = it.kind === 'pull' ? fixture('pull_issue.json') : fixture('issue.json')
    const person = (l: string, id: number) => ({ login: l, id, node_id: `U_fake${id}`, type: 'User', site_admin: false })
    return {
      ...base,
      url: `http://${host}/api/v3/repos/${owner}/${repo}/issues/${it.n}`,
      repository_url: `http://${host}/api/v3/repos/${owner}/${repo}`,
      html_url: pageURL(it.n, it.kind),
      id: 5_300_000_000 + it.n,
      node_id: it.node,
      number: it.n,
      title: it.title,
      user: person(it.author ?? 'hubot-example', 9_000_002),
      labels: (it.labels ?? []).map((name, i) => ({ id: 7_000_000 + i, name, color: 'd73a4a' })),
      state: it.state,
      state_reason: it.state === 'closed' ? (it.stateReason ?? 'completed') : null,
      assignee: null,
      assignees: (it.assignees ?? []).map((l, i) => person(l, 9_100_000 + i)),
      milestone: null,
      created_at: it.createdAt,
      updated_at: it.updatedAt,
      closed_at: it.state === 'closed' ? (it.closedAt ?? it.updatedAt) : null,
      body: it.body,
      body_html: bodyHTML(it),
      ...(it.kind === 'pull' ? { pull_request: { url: `http://${host}/api/v3/repos/${owner}/${repo}/pulls/${it.n}`, html_url: pageURL(it.n, 'pull'), merged_at: it.mergedAt ?? null } } : {}),
    }
  }

  function pullJSON(it: Held): Record<string, unknown> {
    const base = fixture('pull.json') as Record<string, Record<string, unknown>>
    return {
      ...base,
      id: 6_000_000 + it.n,
      node_id: it.node,
      html_url: pageURL(it.n, 'pull'),
      number: it.n,
      state: it.merged ? 'closed' : it.state,
      title: it.title,
      merged: !!it.merged,
      merged_at: it.merged ? (it.mergedAt ?? it.updatedAt) : null,
      merge_commit_sha: it.mergeSha ?? null,
      head: { ...base.head, sha: it.headSha ?? '1111111111111111111111111111111111111111' },
      base: { ...base.base, ref: 'main', repo: { ...(base.base!.repo as Record<string, unknown>), id: REPO_ID, full_name: `${owner}/${repo}` } },
      updated_at: it.updatedAt,
    }
  }

  function routeOf(method: string, path: string): { route: FakeRoute | 'other'; m?: RegExpExecArray } {
    const pats: Array<[FakeRoute, string, RegExp]> = [
      ['user', 'GET', /^\/api\/v3\/user$/],
      ['repo', 'GET', /^\/api\/v3\/repos\/([^/]+)\/([^/]+)$/],
      ['comments', 'GET', /^\/api\/v3\/repositories\/(\d+)\/issues\/(\d+)\/comments$/],
      ['read', 'GET', /^\/api\/v3\/repositories\/(\d+)\/issues\/(\d+)$/],
      ['pull', 'GET', /^\/api\/v3\/repositories\/(\d+)\/pulls\/(\d+)$/],
      ['checks', 'GET', /^\/api\/v3\/repositories\/(\d+)\/commits\/([0-9a-f]+)\/(status|check-runs)$/],
      ['checks', 'GET', /^\/api\/v3\/repositories\/(\d+)\/actions\/runs$/],
      ['checks', 'GET', /^\/api\/v3\/repositories\/(\d+)\/actions\/runs\/(\d+)\/jobs$/],
      ['log', 'GET', /^\/api\/v3\/repositories\/(\d+)\/actions\/jobs\/(\d+)\/logs$/],
      ['log', 'GET', /^\/_logs\/(\d+)$/],
      ['image', 'GET', /^\/_img\/(\d+)\/([A-Za-z0-9._-]+)$/],
      ['resolve', 'POST', /^\/api\/graphql$/],
    ]
    for (const [route, m, re] of pats) {
      const got = re.exec(path)
      if (got && m === method) return { route, m: got }
    }
    return { route: 'other' }
  }

  function send(res: ServerResponse, status: number, body?: unknown, headers: Record<string, string> = {}) {
    const text = body === undefined ? '' : typeof body === 'string' || Buffer.isBuffer(body) ? body : JSON.stringify(body)
    res.writeHead(status, { ...(body !== undefined && !Buffer.isBuffer(body) && typeof body !== 'string' ? { 'Content-Type': 'application/json; charset=utf-8' } : {}), ...headers })
    res.end(text)
  }
  const notFound = (res: ServerResponse) => send(res, 404, { message: 'Not Found', documentation_url: 'https://docs.github.com/rest', status: '404' })

  async function readBody(req: IncomingMessage): Promise<string> {
    const chunks: Buffer[] = []
    for await (const c of req) chunks.push(c as Buffer)
    return Buffer.concat(chunks).toString('utf8')
  }

  async function serve(req: IncomingMessage, res: ServerResponse) {
    const u = new URL(req.url ?? '/', `http://${host}`)
    const method = req.method ?? 'GET'
    const auth = String(req.headers.authorization ?? '')
    const token = auth.replace(/^(?:Bearer|token)\s+/i, '')
    const raw = method === 'POST' ? await readBody(req) : ''
    let { route, m } = routeOf(method, u.pathname)
    let gql: GQL = {}
    if (route === 'resolve') {
      try {
        gql = JSON.parse(raw) as GQL
      } catch {
        gql = {}
      }
      const q = String(gql.query ?? '').trim()
      if (q.startsWith('mutation AddComment')) route = 'comment'
      else if (q.startsWith('mutation CloseIssue')) route = 'close'
    }
    log.push({ route, method, path: u.pathname, token })
    if (route !== 'other') counts.set(route, (counts.get(route) ?? 0) + 1)
    const gate = route !== 'other' ? gates.get(route) : undefined
    if (gate) {
      gate.arrive()
      await gate.open
    }
    // The routes of the API host take the token; the signed ones (an image, a log's storage) are fetched with none, as GitHub's are.
    const signed = route === 'image' || (route === 'log' && u.pathname.startsWith('/_logs/'))
    if (!signed && (!token || refused.has(token))) return send(res, 401, { message: 'Bad credentials', documentation_url: 'https://docs.github.com/rest', status: '401' })
    switch (route) {
      case 'user':
        return send(res, 200, { ...fixture('user.json'), login, id: USER_ID }, { 'X-OAuth-Scopes': 'repo' })
      case 'repo':
        if (m![1]!.toLowerCase() !== owner.toLowerCase() || m![2]!.toLowerCase() !== repo.toLowerCase()) return notFound(res)
        return send(res, 200, { id: REPO_ID, node_id: 'R_fake', full_name: `${owner}/${repo}`, private: false })
      case 'read': {
        const it = Number(m![1]) === REPO_ID ? items.get(Number(m![2])) : undefined
        return it ? send(res, 200, issueJSON(it)) : notFound(res)
      }
      case 'pull': {
        const it = Number(m![1]) === REPO_ID ? items.get(Number(m![2])) : undefined
        return it && it.kind === 'pull' ? send(res, 200, pullJSON(it)) : notFound(res)
      }
      case 'checks':
        return serveChecks(res, u, m!)
      case 'log': {
        if (u.pathname.startsWith('/_logs/')) {
          const c = [...checkSets.values()].flat().find((x) => x.kind === 'job' && x.id === Number(m![1]))
          if (!c) return notFound(res)
          return send(res, 200, Buffer.from(c.log ?? ''), { 'Content-Type': 'text/plain' })
        }
        const c = [...checkSets.values()].flat().find((x) => x.kind === 'job' && x.id === Number(m![2]))
        if (!c) return notFound(res)
        return send(res, 302, undefined, { Location: `http://${host}/_logs/${c.id}?sig=fake` })
      }
      case 'image': {
        const it = items.get(Number(m![1]))
        const im = it?.images?.find((x) => x.path === m![2])
        if (!im) return notFound(res)
        return send(res, 200, im.bytes, { 'Content-Type': im.type, 'Content-Length': String(im.bytes.length) })
      }
      case 'comments': {
        const n = Number(m![2])
        const since = u.searchParams.get('since')
        const list = (posted.get(n) ?? []).filter((c) => !since || c.createdAt >= since)
        const page = Number(u.searchParams.get('page') ?? '1')
        const slice = page === 1 ? list : []
        return send(res, 200, slice.map((c) => ({ id: c.id, body: c.body, created_at: c.createdAt, updated_at: c.createdAt, user: { login: c.author, id: USER_ID } })))
      }
      case 'resolve':
        return serveResolve(res, gql)
      case 'comment':
        return serveComment(res, gql)
      case 'close':
        return serveClose(res, gql)
    }
    return notFound(res)
  }

  function serveChecks(res: ServerResponse, u: URL, m: RegExpExecArray) {
    if (Number(m[1]) !== REPO_ID) return notFound(res)
    const p = u.pathname
    if (p.endsWith('/status')) {
      const list = (checkSets.get(m[2]!) ?? []).filter((c) => c.kind === 'status')
      return send(res, 200, {
        state: list.some((c) => c.conclusion === 'failure') ? 'failure' : 'success',
        sha: m[2],
        total_count: list.length,
        statuses: list.map((c) => ({ id: c.id, state: c.conclusion ?? 'pending', context: c.name, target_url: `https://ci.example/builds/${c.id}`, created_at: '2026-11-01T09:00:00Z', updated_at: '2026-11-01T09:05:00Z' })),
      })
    }
    if (p.endsWith('/check-runs')) {
      const list = (checkSets.get(m[2]!) ?? []).filter((c) => c.kind === 'run')
      return send(res, 200, { total_count: list.length, check_runs: list.map((c) => check(c, m[2]!, 'some-ci')) })
    }
    if (p.endsWith('/actions/runs')) {
      const sha = u.searchParams.get('head_sha') ?? ''
      const jobs = (checkSets.get(sha) ?? []).filter((c) => c.kind === 'job')
      const runs = jobs.length ? [{ id: runOf(sha), name: 'CI', head_sha: sha, status: 'completed' }] : []
      return send(res, 200, { total_count: runs.length, workflow_runs: runs })
    }
    // A run's jobs: the jobs of the commit the run was listed for (the fake keeps one run a commit).
    const sha = [...checkSets.keys()].find((k) => runOf(k) === Number(m[2])) ?? ''
    const jobs = (checkSets.get(sha) ?? []).filter((c) => c.kind === 'job')
    return send(res, 200, { total_count: jobs.length, jobs: jobs.map((c) => ({ ...check(c, sha, 'github-actions'), run_id: runOf(sha), check_run_url: `http://${host}/api/v3/repos/${owner}/${repo}/check-runs/${c.id}` })) })
  }

  /** A commit's one workflow run, by the order its checks were set in. */
  const runOf = (sha: string) => 1_000_001 + [...checkSets.keys()].indexOf(sha)

  function check(c: FakeCheck, sha: string, slug: string): Record<string, unknown> {
    return {
      id: c.id,
      head_sha: sha,
      name: c.name,
      status: c.status,
      conclusion: c.conclusion ?? null,
      html_url: `http://${host}/${owner}/${repo}/runs/${c.id}`,
      details_url: null,
      started_at: '2026-11-01T09:00:00Z',
      completed_at: c.status === 'completed' ? '2026-11-01T09:05:00Z' : null,
      app: { slug },
    }
  }

  const byNode = (id: unknown) => [...items.values()].find((x) => x.node === id)
  const gqlNode = (it: Held) => ({
    __typename: it.kind === 'pull' ? 'PullRequest' : 'Issue',
    id: it.node,
    number: it.n,
    url: pageURL(it.n, it.kind),
    state: it.merged ? 'MERGED' : it.state === 'closed' ? 'CLOSED' : 'OPEN',
    repository: { fullDatabaseId: String(REPO_ID), nameWithOwner: `${owner}/${repo}` },
  })

  function serveResolve(res: ServerResponse, g: GQL) {
    const it = byNode(g.variables?.id)
    if (!it) return send(res, 200, { data: { node: null }, errors: [{ type: 'NOT_FOUND', path: ['node'], message: 'Could not resolve to a node' }] })
    return send(res, 200, { data: { node: gqlNode(it) } })
  }

  function serveComment(res: ServerResponse, g: GQL) {
    const it = byNode(g.variables?.subjectId)
    if (!it) return send(res, 200, { data: { addComment: null }, errors: [{ type: 'NOT_FOUND', path: ['addComment'], message: 'Could not resolve to a node' }] })
    const c = { id: nextComment++, body: String(g.variables?.body ?? ''), author: login, createdAt: tick() }
    posted.set(it.n, [...(posted.get(it.n) ?? []), c])
    return send(res, 200, {
      data: {
        addComment: {
          subject: { id: it.node },
          commentEdge: { node: { fullDatabaseId: String(c.id), url: `${pageURL(it.n, it.kind)}#issuecomment-${c.id}`, createdAt: c.createdAt, repository: { fullDatabaseId: String(REPO_ID) }, author: { login, databaseId: USER_ID } } },
        },
      },
    })
  }

  function serveClose(res: ServerResponse, g: GQL) {
    const it = byNode(g.variables?.issueId)
    if (!it || it.kind !== 'issue') return send(res, 200, { data: { closeIssue: null }, errors: [{ type: 'NOT_FOUND', path: ['closeIssue'], message: 'Could not resolve to an Issue' }] })
    it.state = 'closed'
    it.stateReason = g.variables?.stateReason === 'NOT_PLANNED' ? 'not_planned' : 'completed'
    it.updatedAt = tick()
    it.closedAt = it.updatedAt
    return send(res, 200, { data: { closeIssue: { issue: { id: it.node, state: 'CLOSED', stateReason: String(g.variables?.stateReason ?? 'COMPLETED'), repository: { fullDatabaseId: String(REPO_ID) } } } } })
  }

  const server: Server = createServer((req, res) => {
    serve(req, res).catch(() => {
      if (!res.headersSent) send(res, 500, { message: 'fake failure' })
      else res.destroy()
    })
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  host = `127.0.0.1:${(server.address() as AddressInfo).port}`

  return {
    host,
    owner,
    repo,
    repoId: REPO_ID,
    url: pageURL,
    set(item) {
      const was = items.get(item.n)
      const given = item.updatedAt && (!was || item.updatedAt > was.updatedAt) ? item.updatedAt : undefined
      const updatedAt = given ?? tick()
      if (given) clock = Math.max(clock, Date.parse(given))
      items.set(item.n, { ...item, node: item.node ?? was?.node ?? `${item.kind === 'pull' ? 'PR' : 'I'}_fake${item.n}`, updatedAt, createdAt: was?.createdAt ?? new Date(clock - 3_600_000).toISOString().replace(/\.\d{3}Z$/, 'Z'), closedAt: item.state === 'closed' ? (was?.closedAt ?? updatedAt) : undefined })
    },
    item: (n) => items.get(n),
    checks(sha, list) {
      checkSets.set(sha, list)
    },
    hold(route) {
      let release!: () => void
      let arrive!: () => void
      const open = new Promise<void>((r) => (release = r))
      const reached = new Promise<void>((r) => (arrive = r))
      const g = { open, release: () => release(), reached, arrive: () => arrive() }
      gates.set(route, g)
      return {
        reached,
        release: () => {
          gates.delete(route)
          g.release()
        },
      }
    },
    count: (route) => counts.get(route) ?? 0,
    requests: () => [...log],
    comments: (n) => (posted.get(n) ?? []).map(({ id, body, author }) => ({ id, body, author })),
    refuse(token) {
      refused.add(token)
    },
    allow(token) {
      refused.delete(token)
    },
    async stop() {
      for (const g of gates.values()) g.release()
      gates.clear()
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    },
  }
}
