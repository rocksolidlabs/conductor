import { describe, expect, it } from 'vitest'
import { MAX_FOLDER_CHECKS, TRIAL_MEMORY_KEY, checkFor, readTrialMemory, rememberTrial, rememberedAgents, trialFromRecord, withTrial } from './trialMemory'

/** A store in memory, as localStorage is. */
function store(init: Record<string, string> = {}) {
  const m = new Map(Object.entries(init))
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), raw: m }
}

const catalog = [
  { id: 'shell', name: 'Shell' },
  { id: 'claude', name: 'Claude Code' },
  { id: 'codex', name: 'Codex' },
  { id: 'goose', name: 'Goose' },
  { id: 'gone', name: 'Gone', available: false },
]

describe("what New agent trial keeps of the last trial", () => {
  it('keeps the agents, the folder and its Check line, and nothing else of the trial', () => {
    const s = store()
    rememberTrial(s, { agents: ['claude', 'codex'], folder: ' /srv/app ', check: ' npm test ' })
    const kept = JSON.parse(s.raw.get(TRIAL_MEMORY_KEY)!)
    expect(kept).toEqual({ agents: ['claude', 'codex'], folder: '/srv/app', checks: [{ folder: '/srv/app', check: 'npm test' }] })
    // No arguments, account, name, approach or instructions: the stored value holds only these three keys.
    expect(Object.keys(kept).sort()).toEqual(['agents', 'checks', 'folder'])
    expect(s.raw.get(TRIAL_MEMORY_KEY)).not.toMatch(/--|args|account|name|approach|told/)
  })

  it('keeps the Check line of the last 20 folders, the most recent first, one a folder', () => {
    let m = null as ReturnType<typeof withTrial> | null
    for (let i = 0; i < MAX_FOLDER_CHECKS + 5; i++) m = withTrial(m, { agents: ['claude', 'codex'], folder: `/srv/${i}`, check: `make ${i}` })
    expect(m!.checks).toHaveLength(MAX_FOLDER_CHECKS)
    expect(m!.checks[0]).toEqual({ folder: '/srv/24', check: 'make 24' })
    expect(checkFor(m, '/srv/4')).toBe('')
    m = withTrial(m, { agents: ['claude', 'codex'], folder: '/srv/10', check: 'make ten' })
    expect(m.checks.filter((c) => c.folder === '/srv/10')).toEqual([{ folder: '/srv/10', check: 'make ten' }])
    expect(checkFor(m, ' /srv/10 ')).toBe('make ten')
  })

  it('reads a store that throws, holds nothing, or holds something else as none, and a write to one that throws passes', () => {
    const throws = { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }
    expect(readTrialMemory(throws)).toBeNull()
    expect(() => rememberTrial(throws, { agents: ['claude'], folder: '/x', check: '' })).not.toThrow()
    expect(readTrialMemory(null)).toBeNull()
    expect(readTrialMemory(store())).toBeNull()
    expect(readTrialMemory(store({ [TRIAL_MEMORY_KEY]: '{' }))).toBeNull()
    expect(readTrialMemory(store({ [TRIAL_MEMORY_KEY]: JSON.stringify({ agents: 'claude', folder: '/x', checks: [] }) }))).toBeNull()
    expect(readTrialMemory(store({ [TRIAL_MEMORY_KEY]: JSON.stringify({ agents: ['claude'], folder: '/x', checks: [{ folder: 1 }, { folder: '/x', check: 'make' }] }) }))).toEqual({
      agents: ['claude'],
      folder: '/x',
      checks: [{ folder: '/x', check: 'make' }],
    })
  })

  it('starts a new draft from the kept agents still installed, one gone replaced by an installed one not in the trial', () => {
    const m = withTrial(null, { agents: ['claude', 'gone', 'codex'], folder: '/x', check: '' })
    expect(rememberedAgents(m, catalog)).toEqual(['claude', 'goose', 'codex'])
    expect(rememberedAgents(withTrial(null, { agents: ['codex', 'claude'], folder: '/x', check: '' }), catalog)).toEqual(['codex', 'claude'])
    // Nothing usable: the form's own starting agents then.
    expect(rememberedAgents(null, catalog)).toBeNull()
    expect(rememberedAgents(withTrial(null, { agents: ['gone', 'gone'], folder: '/x', check: '' }), [catalog[0]!, catalog[1]!, catalog[4]!])).toBeNull()
  })
})

describe("the server's record of the last trial", () => {
  it('is the memory, each Check line written out from its words, and none for anything else', () => {
    expect(trialFromRecord({ agents: ['claude', 'codex'], folder: '/repo', checks: [{ folder: '/repo', check: ['go', 'test', './...'] }, { folder: '/b', check: ['npm', 'run', 'the tests'] }, { folder: '/c', check: [] }] })).toEqual({
      agents: ['claude', 'codex'],
      folder: '/repo',
      checks: [
        { folder: '/repo', check: 'go test ./...' },
        { folder: '/b', check: "npm run 'the tests'" },
        { folder: '/c', check: '' },
      ],
    })
    for (const bad of [null, undefined, 'x', { agents: 'claude', folder: '', checks: [] }, { agents: [], folder: 1, checks: [] }, { agents: [], folder: '' }]) expect(trialFromRecord(bad)).toBeNull()
    expect(trialFromRecord({ agents: [], folder: '', checks: [{ folder: '/a', check: [1] }, null] })).toEqual({ agents: [], folder: '', checks: [] })
  })
})
