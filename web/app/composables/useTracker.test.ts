import { describe, expect, it } from 'vitest'
import { ApiError } from './useApi'
import { trackerStore, type TrackerEntry } from './useTracker'

// The GitHub tokens' store (Program item IN1a, PR-8): a token is in one request's body, the save's, and nowhere else; the field it came
// from is emptied before the save is sent, whatever its answer; a refusal is the app's words, never the server's echo of the body; the
// hosts a reference may name follow the saved ones.

const TOKEN = 'ghp_vitestPlanted0123456789abcdefTOKEN'

function server(o: { refuse?: ApiError; inUse?: boolean } = {}) {
  const entries: TrackerEntry[] = []
  const bodies: unknown[] = []
  const request = async <T>(path: string, opts: { method?: string; body?: unknown } = {}): Promise<T> => {
    if (opts.body !== undefined) bodies.push(opts.body)
    if (path === '/api/trackers') return { github: [...entries] } as T
    if (path === '/api/trackers/github') {
      if (o.refuse) throw o.refuse
      const host = (opts.body as { host: string }).host
      const e: TrackerEntry = { host, login: 'mona-example', fingerprint: 'a1b2c3d4e5f6', source: 'settings', state: 'ok', savedAt: '2026-11-16T12:00:00Z' }
      entries.push(e)
      return { tracker: e } as T
    }
    if (path === '/api/trackers/github/remove') {
      if (o.inUse) return { removed: true, inUse: 'environment', message: 'the environment token is in use' } as T
      entries.splice(0, entries.length)
      return { removed: true } as T
    }
    throw new ApiError(404, 'not_found', 'no route')
  }
  return { request, bodies, entries }
}

describe("a GitHub token's save", () => {
  it('sends the token once, in the save’s body, empties the field first, and keeps it nowhere after', async () => {
    const s = server()
    const store = trackerStore(s.request)
    let field = TOKEN
    const out = await store.save('github.com', () => {
      const t = field
      field = ''
      return t
    })
    expect(out).toMatchObject({ ok: true })
    expect(field).toBe('')
    expect(s.bodies).toEqual([{ host: 'github.com', token: TOKEN }])
    expect(store.entries.value.map((e) => e.host)).toEqual(['github.com'])
    expect(JSON.stringify({ out, entries: store.entries.value, error: store.error.value })).not.toContain(TOKEN)
  })

  it('says a refusal in its own words, never the server’s, and the field is emptied all the same', async () => {
    // A server whose refusal echoed the token would still not have it said.
    const s = server({ refuse: new ApiError(422, 'token_refused', `GitHub refused ${TOKEN}`) })
    const store = trackerStore(s.request)
    let field = TOKEN
    const out = await store.save('github.com', () => {
      const t = field
      field = ''
      return t
    })
    expect(out.ok).toBe(false)
    expect(out.words).toContain('Settings')
    expect(out.words).not.toContain(TOKEN)
    expect(field).toBe('')
  })

  it('sends nothing for an empty field', async () => {
    const s = server()
    const out = await trackerStore(s.request).save('github.com', () => '')
    expect(out).toMatchObject({ ok: false, code: 'invalid_token' })
    expect(s.bodies).toEqual([])
  })

  it.each([
    ['too_many_hosts', 'four hosts'],
    ['environment', 'environment'],
    ['credentials_busy', 'try again'],
    ['rate_limited', 'try again later'],
    ['tracker_unavailable', 'could not be reached'],
  ])('words %s', async (code, words) => {
    const s = server({ refuse: new ApiError(409, code, 'the server said') })
    const out = await trackerStore(s.request).save('github.com', () => TOKEN)
    expect(out.ok).toBe(false)
    expect(out.words).toContain(words)
  })
})

describe("a GitHub token's remove", () => {
  it('says it stops Conductor’s use only, the token valid on GitHub until revoked there', async () => {
    const s = server()
    const store = trackerStore(s.request)
    await store.save('github.com', () => TOKEN)
    const out = await store.remove('github.com')
    expect(out.ok).toBe(true)
    expect(out.words).toContain('stays valid on GitHub until you revoke it there')
    expect(store.entries.value).toEqual([])
  })

  it("says the environment's token is still the one in use", async () => {
    const out = await trackerStore(server({ inUse: true }).request).remove('github.com')
    expect(out.words).toContain('environment')
  })
})

describe('the hosts a reference may name', () => {
  it('follow the saved hosts, a loopback one only where one is saved', async () => {
    const s = server()
    const store = trackerStore(s.request)
    expect(store.refOptions.value).toEqual({ hosts: [], loopback: false })
    await store.save('ghe.example.test', () => TOKEN)
    expect(store.refOptions.value).toEqual({ hosts: ['ghe.example.test'], loopback: false })
    await store.save('127.0.0.1:8123', () => TOKEN)
    expect(store.refOptions.value.loopback).toBe(true)
  })

  it('are none on a switchyard, which keeps no token', async () => {
    const store = trackerStore(async () => {
      throw new ApiError(403, 'switchyard', 'a switchyard')
    })
    await store.load()
    expect(store.available.value).toBe(false)
  })
})
