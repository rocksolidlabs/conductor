import { describe, expect, it } from 'vitest'
import { AGENTS_KEPT, LAST_LAUNCH_KEY, NAMES_KEPT, SHELL_ID, afterLaunch, readLast, withRecord, writeLast, type LastLaunch } from './lastLaunch'

// What Launch agent remembers of the last launch (UX3 R29): read as the dialog opens, written after a launch the server took.

function store(init: Record<string, string> = {}) {
  const m = new Map(Object.entries(init))
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), raw: () => m.get(LAST_LAUNCH_KEY) }
}

const launched = (over: Partial<Parameters<typeof afterLaunch>[1]> = {}) => ({ agent: 'claude', folder: '/home/ada/code/shop', model: '', moreOpen: false, ...over })

describe('the memory of the last launch', () => {
  it('reads empty from storage that throws, holds nothing, or holds something it did not write', () => {
    const throwing = { getItem: () => { throw new Error('blocked') } }
    expect(readLast(throwing)).toEqual({ models: {} })
    expect(readLast(null)).toEqual({ models: {} })
    expect(readLast(store())).toEqual({ models: {} })
    expect(readLast(store({ [LAST_LAUNCH_KEY]: 'not json' }))).toEqual({ models: {} })
    expect(readLast(store({ [LAST_LAUNCH_KEY]: '[1,2]' }))).toEqual({ models: {} })
    expect(readLast(store({ [LAST_LAUNCH_KEY]: '{"agent":7,"folder":{},"moreOpen":"yes","models":[]}' }))).toEqual({ models: {} })
    expect(readLast(store({ [LAST_LAUNCH_KEY]: '{"models":{"claude":{"model":3,"names":["opus",5,"",  "opus"]}}}' }))).toEqual({ models: { claude: { model: '', names: ['opus'] } } })
  })

  it('keeps the agent, the folder, the fold and each agent its own model, and reads back what it wrote', () => {
    const s = store()
    let v: LastLaunch = afterLaunch(readLast(s), launched({ model: 'opus', moreOpen: true }))
    v = afterLaunch(v, launched({ agent: 'codex', folder: '/srv/api', model: 'o3' }))
    writeLast(s, v)
    const back = readLast(s)
    expect(back).toEqual(v)
    expect(back.agent).toBe('codex')
    expect(back.folder).toBe('/srv/api')
    expect(back.moreOpen).toBe(false)
    expect(back.models.claude).toEqual({ model: 'opus', names: ['opus'] })
    expect(back.models.codex).toEqual({ model: 'o3', names: ['o3'] })
  })

  it('keeps the last five model names typed, newest first, each once, and an empty model leaves them', () => {
    let v: LastLaunch = { models: {} }
    for (const m of ['a', 'b', 'c', 'a', 'd', 'e', 'f']) v = afterLaunch(v, launched({ model: m }))
    expect(v.models.claude!.names).toEqual(['f', 'e', 'd', 'a', 'c'])
    expect(v.models.claude!.names).toHaveLength(NAMES_KEPT)
    v = afterLaunch(v, launched({ model: '' }))
    expect(v.models.claude).toEqual({ model: '', names: ['f', 'e', 'd', 'a', 'c'] })
  })

  it('never keeps the built-in shell as the agent to pick, and keeps a bounded count of agents', () => {
    let v = afterLaunch({ models: {} }, launched({ agent: 'claude' }))
    v = afterLaunch(v, launched({ agent: SHELL_ID }))
    expect(v.agent).toBe('claude')
    expect(readLast(store({ [LAST_LAUNCH_KEY]: JSON.stringify({ agent: SHELL_ID, models: {} }) })).agent).toBeUndefined()
    for (let i = 0; i <= AGENTS_KEPT; i++) v = afterLaunch(v, launched({ agent: `agent-${i}` }))
    expect(Object.keys(v.models)).toHaveLength(AGENTS_KEPT)
    expect(v.models['agent-0']).toBeUndefined()
  })

  it('holds no arguments, task, name, account or Yolo, whatever the launch carried', () => {
    const v = afterLaunch({ models: {} }, { ...launched({ model: 'opus' }), args: '--key sk-secret', message: 'fix it', name: 'mine', account: 'work', yolo: true } as never)
    const s = store()
    writeLast(s, v)
    const raw = s.raw() ?? ''
    for (const never of ['sk-secret', 'fix it', 'mine', 'work', 'yolo', 'args', 'message', 'account']) expect(raw, never).not.toContain(never)
  })

  it('is told nothing by storage that refuses a write', () => {
    const refusing = { getItem: () => null, setItem: () => { throw new Error('quota') } }
    expect(() => writeLast(refusing, { models: {} })).not.toThrow()
  })
})

describe("the server's record of the last launch (R89)", () => {
  const local: LastLaunch = { agent: 'codex', folder: '/old', models: { codex: { model: 'o3', names: ['o3'] } }, moreOpen: true }
  it("wins for the agent, the folder and the models, and leaves the fold this viewer's own", () => {
    const rec = { v: 1, agent: 'claude', folder: '/srv/api', models: { claude: { model: 'opus', names: ['opus', 'sonnet'] } }, order: ['claude'] }
    expect(withRecord(local, rec)).toEqual({ agent: 'claude', folder: '/srv/api', models: { claude: { model: 'opus', names: ['opus', 'sonnet'] } }, moreOpen: true })
    expect(withRecord({ models: {} }, rec).moreOpen).toBeUndefined()
  })
  it('leaves the memory as it is with no record, an empty one, or one it cannot read', () => {
    expect(withRecord(local, null)).toBe(local)
    expect(withRecord(local, { models: {} })).toBe(local)
    expect(withRecord(local, { agent: 'shell', models: {} })).toBe(local)
    expect(withRecord(local, { models: { claude: { model: 3, names: 'x' } } })).toEqual({ models: { claude: { model: '', names: [] } }, moreOpen: true })
  })
})

