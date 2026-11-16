import { describe, expect, it } from 'vitest'
import type { JoinInfo } from '~/composables/useSessions'
import { filesRoot, JOINED_KEY, JOINED_MAX, joinedFromInfo, joinedOpen, joinedPath, joinedStatusOf, patchJoined, readJoined, removeJoined, upsertJoined, writeJoined, type JoinedEntry } from './joined'

const memory = (init: Record<string, string> = {}) => {
  const data = new Map(Object.entries(init))
  return { data, getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v), removeItem: (k: string) => void data.delete(k) }
}
const TOKEN = 'abcdefghijklmnop0123'
const entry = (p: Partial<JoinedEntry> = {}): Omit<JoinedEntry, 'id' | 'addedAt'> => ({ token: TOKEN, server: 'https://switchyard.example.net', kind: 'session', name: 'review', role: 'view', host: 'switchyard.example.net', ...p })
let n = 0
const ids = () => `id${++n}`

describe('the joined links', () => {
  it('round-trips through storage, dropping what cannot be used', () => {
    const s = memory()
    const list = upsertJoined([], entry(), 1000, ids)
    writeJoined(s, list)
    expect(readJoined(s)).toEqual(list)
    s.setItem(JOINED_KEY, '{not json')
    expect(readJoined(s)).toEqual([])
    s.setItem(JOINED_KEY, '{"a":1}')
    expect(readJoined(s)).toEqual([])
    const good = { ...list[0], extra: 'dropped' }
    s.setItem(JOINED_KEY, JSON.stringify([good, { ...list[0], token: 'short' }, { ...list[0], server: 'ftp://x' }, { ...list[0], role: 'admin' }, null]))
    expect(readJoined(s)).toEqual(list)
    expect(readJoined({ getItem: () => { throw new Error('denied') }, setItem: () => {}, removeItem: () => {} })).toEqual([])
  })

  it('replaces the entry of the same link in place, and over the bound drops the revoked before the oldest', () => {
    let list = upsertJoined([], entry(), 1000, ids)
    const first = list[0]!
    list = upsertJoined(list, entry({ name: 'renamed' }), 2000, ids)
    expect(list).toHaveLength(1)
    expect(list[0]).toMatchObject({ id: first.id, addedAt: first.addedAt, name: 'renamed' })
    list = []
    for (let i = 0; i < JOINED_MAX; i++) list = upsertJoined(list, entry({ token: `${TOKEN}${String(i).padStart(3, '0')}` }), 10_000 + i, ids)
    list = patchJoined(list, list[5]!.id, { lastStatus: 'revoked' })
    const revoked = list[5]!.id
    list = upsertJoined(list, entry({ token: `${TOKEN}new` }), 99_999, ids)
    expect(list).toHaveLength(JOINED_MAX)
    expect(list.some((e) => e.id === revoked)).toBe(false)
    const oldest = list.at(-1)!.id
    list = upsertJoined(list, entry({ token: `${TOKEN}newer` }), 100_000, ids)
    expect(list.some((e) => e.id === oldest)).toBe(false)
    expect(removeJoined(list, list[0]!.id)).toHaveLength(JOINED_MAX - 1)
  })

  it('reads a join reply and the errors of a look at the link', () => {
    const session: JoinInfo = { role: 'control', session: { id: 's', name: 'review', agentId: 'codex', kind: 'hosted', status: 'running', cols: 80, rows: 24, hostName: 'laptop' } }
    expect(joinedFromInfo(session)).toEqual({ kind: 'session', name: 'review', role: 'control', agentId: 'codex', sessionKind: 'hosted', hostedOn: 'laptop', sessionStatus: 'running' })
    const run: JoinInfo = { role: 'view', run: { id: 'r', name: 'users api', members: [{ name: 'a', agentId: 'claude', status: 'running' }] } }
    expect(joinedFromInfo(run)).toEqual({ kind: 'run', name: 'users api', role: 'view', members: 1 })
    expect(joinedStatusOf({ status: 404, code: 'revoked' })).toBe('revoked')
    expect(joinedStatusOf({ status: 404, code: 'expired' })).toBe('revoked')
    expect(joinedStatusOf({ status: 404, code: 'session_gone' })).toBe('gone')
    expect(joinedStatusOf({ status: 0, code: 'network' })).toBe('unreachable')
    expect(joinedStatusOf({ status: 429, code: 'rate_limited' })).toBeUndefined()
  })

  it('opens an entry at its join page', () => {
    expect(joinedPath({ token: TOKEN, server: '' })).toBe(`/join/${TOKEN}`)
    expect(joinedPath({ token: TOKEN, server: 'https://s.example' })).toBe(`/join/${TOKEN}?server=https%3A%2F%2Fs.example`)
    expect(joinedOpen(`/join/${TOKEN}`, TOKEN)).toBe(true)
    expect(joinedOpen('/yard', TOKEN)).toBe(false)
  })
})

describe('the Files root of a joined session', () => {
  it('takes the folder the session\'s own welcome names over the one the switchyard lists', () => {
    expect(filesRoot('acme-billing', '/home/nate/code/acme-billing')).toBe('/home/nate/code/acme-billing')
    expect(filesRoot('/srv/old-listing', '/srv/repo')).toBe('/srv/repo')
  })
  it('roots nothing at a last element alone, and keeps a whole path from an older owner', () => {
    expect(filesRoot('acme-billing', undefined)).toBeUndefined()
    expect(filesRoot('acme-billing', '')).toBeUndefined()
    expect(filesRoot('/home/nate/code', undefined)).toBe('/home/nate/code')
    expect(filesRoot(undefined, undefined)).toBeUndefined()
  })
})
