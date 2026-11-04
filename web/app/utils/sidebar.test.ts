import { describe, expect, it } from 'vitest'
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import { DEFAULT_ROUTES } from './events'
import {
  DEFAULT_FOLDS,
  LEGACY_SIDEBAR_KEY,
  SIDEBAR_FOLDS_KEY,
  SIDEBAR_KEY,
  LEGACY_SIDEBAR_SIZE_KEY,
  SIDEBAR_SIZE,
  SIDEBAR_SIZE_KEY,
  SIDEBAR_WIDTH,
  blockSubtitle,
  focusRows,
  moveFocus,
  needsDotShown,
  railModel,
  railGlyphs,
  folderLetters,
  readFolds,
  readSidebarMode,
  readSidebarSize,
  reopenNeeds,
  rowMeta,
  rowState,
  runOpen,
  sectionPreview,
  sessionOpen,
  sidebarBounds,
  sidebarModel,
  writeFolds,
  writeSidebarMode,
  writeSidebarSize,
  type Folds,
  type KeyValueStore,
  type RunBlock,
  type SessionRow,
} from './sidebar'

function memory(initial: Record<string, string> = {}): KeyValueStore & { data: Map<string, string> } {
  const data = new Map(Object.entries(initial))
  return { data, getItem: (k) => data.get(k) ?? null, setItem: (k, v) => void data.set(k, v), removeItem: (k) => void data.delete(k) }
}

const broken: KeyValueStore = {
  getItem: () => {
    throw new Error('blocked')
  },
  setItem: () => {
    throw new Error('blocked')
  },
  removeItem: () => {},
}

const session = (over: Partial<SessionInfo>): SessionInfo =>
  ({ id: 'id', name: 'name', kind: 'server', agentId: 'claude', command: [], cwd: '/w', status: 'running', cols: 80, rows: 24, viewers: 0, createdAt: '2026-10-01T09:00:00Z', ...over }) as SessionInfo

const run = (over: Partial<RunInfo>): RunInfo =>
  ({ id: 'r1', crewId: 'api-sweep', name: 'API sweep', goal: '', cwd: '/w', isolation: 'none', startedAt: '2026-10-01T08:31:00Z', members: [], log: [], state: 'running', needsInput: 0, yolo: false, ...over }) as RunInfo

const member = (name: string, agentId = 'claude'): RunInfo['members'][number] => ({ name, agentId, start: { when: 'immediately' }, status: 'running' })

describe('readSidebarMode', () => {
  it('reads the saved mode', () => {
    expect(readSidebarMode(memory({ [SIDEBAR_KEY]: 'rail' }))).toBe('rail')
    expect(readSidebarMode(memory({ [SIDEBAR_KEY]: 'full' }))).toBe('full')
    expect(readSidebarMode(memory())).toBe('full')
    expect(readSidebarMode(memory({ [SIDEBAR_KEY]: 'sideways' }))).toBe('full')
  })

  it('moves an old hidden flag to the new key: hidden became the rail', () => {
    const s = memory({ [LEGACY_SIDEBAR_KEY]: '1' })
    expect(readSidebarMode(s)).toBe('rail')
    expect(s.data.get(SIDEBAR_KEY)).toBe('rail')
    expect(s.data.has(LEGACY_SIDEBAR_KEY)).toBe(false)
    const shown = memory({ [LEGACY_SIDEBAR_KEY]: '0' })
    expect(readSidebarMode(shown)).toBe('full')
    expect(shown.data.get(SIDEBAR_KEY)).toBe('full')
    // The new key wins over an old one left beside it.
    expect(readSidebarMode(memory({ [SIDEBAR_KEY]: 'full', [LEGACY_SIDEBAR_KEY]: '1' }))).toBe('full')
  })

  it('is full when storage throws, and writing never throws', () => {
    expect(readSidebarMode(broken)).toBe('full')
    expect(() => writeSidebarMode(broken, 'rail')).not.toThrow()
    const s = memory()
    writeSidebarMode(s, 'rail')
    expect(readSidebarMode(s)).toBe('rail')
  })

  it('keeps a migrated choice when the new key cannot be written, and the old key for a later try', () => {
    const s = memory({ [LEGACY_SIDEBAR_KEY]: '1' })
    const full: KeyValueStore = {
      getItem: s.getItem,
      setItem: () => {
        throw new Error('quota')
      },
      removeItem: s.removeItem,
    }
    expect(readSidebarMode(full)).toBe('rail')
    expect(s.data.get(LEGACY_SIDEBAR_KEY)).toBe('1')
    expect(s.data.has(SIDEBAR_KEY)).toBe(false)
  })
})

describe('the sidebars share', () => {
  it('knows the page that is open', () => {
    expect(sessionOpen('/sessions/a', 'a')).toBe(true)
    expect(sessionOpen('/sessions/ab', 'a')).toBe(false)
    expect(sessionOpen('/runs/a', 'a')).toBe(false)
    expect(runOpen('/runs/r1', 'r1')).toBe(true)
    expect(runOpen('/sessions/r1', 'r1')).toBe(false)
  })

  it('shows the amber needs-you dot as the Events page routes needs_input to the Badge', () => {
    expect(needsDotShown(DEFAULT_ROUTES)).toBe(true)
    expect(needsDotShown({ ...DEFAULT_ROUTES, needs_input: { ...DEFAULT_ROUTES.needs_input, badge: false } })).toBe(false)
  })
})

describe('the full sidebar\'s width (#437)', () => {
  const at1440 = sidebarBounds(1440)

  it('opens at 12.5 rem (200 px) on every screen, and a drag goes up to 26 % of the window, never below the minimum', () => {
    expect(SIDEBAR_SIZE).toEqual({ min: 12.5, maxShare: 0.26 })
    // In px at 16 px to the rem: the minimum 200 everywhere, the maximum 266, 374, 499, 665 and 894.
    const px = (w: number) => {
      const b = sidebarBounds(w)
      return { min: b.min * 16, max: Math.round(b.max * 16) }
    }
    expect(px(1024)).toEqual({ min: 200, max: 266 })
    expect(px(1440)).toEqual({ min: 200, max: 374 })
    expect(px(1920)).toEqual({ min: 200, max: 499 })
    expect(px(2560)).toEqual({ min: 200, max: 666 })
    expect(px(3440)).toEqual({ min: 200, max: 894 })
    for (const w of [0, 320, 640, 768, 1024, 1440, 1920, 2560, 3440, 7680]) {
      const b = sidebarBounds(w)
      expect(b.min).toBe(12.5)
      expect(b.min).toBeLessThanOrEqual(b.max)
    }
    // A window too narrow for 26 % to reach the minimum (below lg, where no sidebar shows), or one not measured: both bounds the minimum.
    expect(sidebarBounds(640)).toEqual({ min: 12.5, max: 12.5 })
    expect(sidebarBounds(Number.NaN)).toEqual({ min: 12.5, max: 12.5 })
    // Bigger text, bigger rem: the minimum stays 12.5 rem, the maximum is the same 26 % of the window.
    expect(sidebarBounds(1440, 20).max * 20).toBeCloseTo(374.4, 0)
    expect(sidebarBounds(1440, 0)).toEqual({ min: 12.5, max: 12.5 })
  })

  it('draws the held width within the window\'s bounds, from the same numbers', () => {
    expect(SIDEBAR_WIDTH).toBe('clamp(12.5rem, var(--width), max(12.5rem, 26vw))')
  })

  it('reads no saved width as the minimum, and a saved one within the bounds of the window it opens in', () => {
    expect(SIDEBAR_SIZE_KEY).toBe('conductor.sidebar.width')
    expect(readSidebarSize(memory(), at1440)).toBe(12.5)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '18.5' }), at1440)).toBe(18.5)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '40' }), at1440)).toBe(23.4)
    // A width dragged in a 2560 px window, opened in a 1024 px one: its maximum.
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '40' }), sidebarBounds(1024))).toBe(16.64)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '3' }), at1440)).toBe(12.5)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: 'wide' }), at1440)).toBe(12.5)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '' }), at1440)).toBe(12.5)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: 'Infinity' }), at1440)).toBe(12.5)
  })

  it('drops a width saved before #437 once: the minimum, the old key gone, and a drag after that kept', () => {
    const s = memory({ [LEGACY_SIDEBAR_SIZE_KEY]: '22' })
    expect(LEGACY_SIDEBAR_SIZE_KEY).toBe('conductor.sidebar.size')
    expect(readSidebarSize(s, at1440)).toBe(12.5)
    expect(s.data.has(LEGACY_SIDEBAR_SIZE_KEY)).toBe(false)
    writeSidebarSize(s, 20, at1440)
    expect(readSidebarSize(s, at1440)).toBe(20)
    expect([...s.data.keys()]).toEqual([SIDEBAR_SIZE_KEY])
    // Both keys there (an older tab wrote the old one again): the old one goes, the new one holds.
    s.data.set(LEGACY_SIDEBAR_SIZE_KEY, '22')
    expect(readSidebarSize(s, at1440)).toBe(20)
    expect(s.data.has(LEGACY_SIDEBAR_SIZE_KEY)).toBe(false)
  })

  it('writes the width within the bounds, two decimals at most, and skips what is not a number', () => {
    const s = memory()
    writeSidebarSize(s, 15.123456, at1440)
    expect(s.data.get(SIDEBAR_SIZE_KEY)).toBe('15.12')
    expect(readSidebarSize(s, at1440)).toBe(15.12)
    writeSidebarSize(s, 99, at1440)
    expect(s.data.get(SIDEBAR_SIZE_KEY)).toBe('23.4')
    writeSidebarSize(s, 2, at1440)
    expect(s.data.get(SIDEBAR_SIZE_KEY)).toBe('12.5')
    writeSidebarSize(s, Number.NaN, at1440)
    expect(s.data.get(SIDEBAR_SIZE_KEY)).toBe('12.5')
  })

  it('is the minimum when storage throws, and writing never throws', () => {
    expect(readSidebarSize(broken, at1440)).toBe(12.5)
    expect(() => writeSidebarSize(broken, 20, at1440)).not.toThrow()
    // Removing the old key throws alone: the saved width still reads.
    const stubborn = { ...memory({ [LEGACY_SIDEBAR_SIZE_KEY]: '22', [SIDEBAR_SIZE_KEY]: '16' }), removeItem: () => { throw new Error('denied') } }
    expect(readSidebarSize(stubborn, at1440)).toBe(16)
  })
})

describe('rowState', () => {
  it('is exited once ended, needs while waiting on input, running while the process runs, idle otherwise', () => {
    expect(rowState(session({ status: 'exited' }))).toBe('exited')
    expect(rowState(session({ status: 'stopped', attention: { state: 'needs_input' } }))).toBe('exited')
    expect(rowState(session({ attention: { state: 'needs_input' } }))).toBe('needs')
    expect(rowState(session({ status: 'starting', attention: { state: 'needs_input' } }))).toBe('needs')
    expect(rowState(session({ status: 'running' }))).toBe('running')
    expect(rowState(session({ status: 'starting' }))).toBe('idle')
    expect(rowState(session({ status: 'host_disconnected' as SessionInfo['status'] }))).toBe('idle')
  })
})

describe('sidebarModel', () => {
  const ids = (items: readonly (SessionRow | RunBlock)[]) => items.map((it) => (it.kind === 'run' ? `${it.runId}[${it.members.map((m) => m.id).join(',')}]` : it.id))

  it('keeps a run whole where its most urgent member is, the members by urgency then the crew order', () => {
    const list = [
      session({ id: 'core', crew: { runId: 'r1', crewId: 'api-sweep', member: 'core' }, createdAt: '2026-10-01T09:04:00Z' }),
      session({ id: 'review', crew: { runId: 'r1', crewId: 'api-sweep', member: 'review' }, status: 'starting', attention: { state: 'needs_input', since: '2026-10-01T09:06:00Z', message: 'Trust this folder?' } }),
      session({ id: 'lead', crew: { runId: 'r1', crewId: 'api-sweep', member: 'lead' }, status: 'exited', exitCode: 0, endedAt: '2026-10-01T09:07:00Z' }),
      session({ id: 'alone', createdAt: '2026-10-01T09:05:00Z' }),
    ]
    const r1 = run({ members: [member('lead'), member('core'), member('tests', 'codex'), member('review', 'codex-untrusted')] })
    const m = sidebarModel(list, (id) => (id === 'r1' ? r1 : undefined))
    expect(ids(m.needs)).toEqual(['r1[review,core,lead]'])
    expect(ids(m.running)).toEqual(['alone'])
    expect(m.exited).toEqual([])
    const block = m.needs[0] as RunBlock
    expect(block.title).toBe('API sweep')
    expect(block.agents).toBe(4)
    expect(block.state).toBe('needs')
    expect(block.needs).toBe(1)
    expect(block.members.map((x) => x.state)).toEqual(['needs', 'running', 'exited'])
    expect(block.members[0]!.prompt).toEqual({ message: 'Trust this folder?', options: [], kind: undefined, since: '2026-10-01T09:06:00Z' })
    expect(block.members[2]!.exitWord).toBe('exit 0')
    // The counts: who needs you, the Running section's live sessions, the Exited section's.
    expect(m.counts).toEqual({ needs: 1, running: 1, exited: 0 })
  })

  it('places a wholly exited run in Exited, a run of running members in Running, and names a forgotten run by its crew', () => {
    const list = [
      session({ id: 'a', crew: { runId: 'r1', crewId: 'docs', member: 'w' }, status: 'exited', endedAt: '2026-10-01T09:07:00Z' }),
      session({ id: 'b', crew: { runId: 'r1', crewId: 'docs', member: 'x' }, status: 'stopped', endedAt: '2026-10-01T09:08:00Z' }),
      session({ id: 'c', crew: { runId: 'r2', crewId: 'api', member: 'core' }, createdAt: '2026-10-01T09:02:00Z' }),
      session({ id: 'd', crew: { runId: 'r2', crewId: 'api', member: 'lead' }, status: 'starting', createdAt: '2026-10-01T09:01:00Z' }),
      session({ id: 'e', status: 'exited', endedAt: '2026-10-01T09:09:00Z' }),
    ]
    const m = sidebarModel(list, (id) => (id === 'r2' ? run({ id: 'r2', crewId: 'api', name: 'API', label: 'Monday run', members: [member('lead'), member('core')] }) : undefined))
    expect(ids(m.running)).toEqual(['r2[c,d]'])
    expect((m.running[0] as RunBlock).title).toBe('Monday run')
    expect((m.running[0] as RunBlock).state).toBe('running')
    // A run the server forgot keeps its members in the order they came.
    expect(ids(m.exited)).toEqual(['e', 'r1[a,b]'])
    expect((m.exited[1] as RunBlock).title).toBe('docs')
    expect((m.exited[1] as RunBlock).agents).toBe(2)
    expect(m.counts).toEqual({ needs: 0, running: 2, exited: 3 })
  })

  it('tags a hosted session with its machine, under no heading, and never says "server" for it', () => {
    const list = [session({ id: 'h1', kind: 'hosted', hostName: 'priya-mbp' }), session({ id: 'h2', kind: 'hosted', hostName: '' }), session({ id: 'm' })]
    const m = sidebarModel(list)
    const rows = m.running as SessionRow[]
    expect(rows.map((r) => [r.id, r.machine])).toEqual([
      ['h1', 'priya-mbp'],
      ['h2', 'unnamed host'],
      ['m', undefined],
    ])
    expect(rowMeta(list[0]!, false, Date.parse('2026-10-01T09:12:00Z'))).toEqual(['priya-mbp', 'claude', '12m'])
    expect(rowMeta(list[2]!, false, Date.parse('2026-10-01T09:12:00Z'))).toEqual(['claude', 'server', '12m'])
    expect(rowMeta(list[2]!, true, Date.parse('2026-10-01T09:12:00Z'))).toEqual(['claude', '12m'])
    expect(rowMeta(session({ viewers: 2 }), false)).toEqual(['claude', 'server', '2 here'])
  })

  it('orders a section loose rows first, then blocks, each by its newest signal', () => {
    const list = [
      session({ id: 'old', attention: { state: 'needs_input', since: '2026-10-01T09:01:00Z' } }),
      session({ id: 'new', attention: { state: 'needs_input', since: '2026-10-01T09:09:00Z' } }),
      session({ id: 'b1', crew: { runId: 'r1', crewId: 'one', member: 'a' }, attention: { state: 'needs_input', since: '2026-10-01T09:05:00Z' } }),
      session({ id: 'b2', crew: { runId: 'r2', crewId: 'two', member: 'a' }, attention: { state: 'needs_input', since: '2026-10-01T09:08:00Z' } }),
      session({ id: 'r-old', createdAt: '2026-10-01T09:00:00Z' }),
      session({ id: 'r-new', createdAt: '2026-10-01T09:10:00Z' }),
    ]
    const m = sidebarModel(list)
    expect(ids(m.needs)).toEqual(['new', 'old', 'r2[b2]', 'r1[b1]'])
    expect(ids(m.running)).toEqual(['r-new', 'r-old'])
  })

  it('has empty sections and zero counts for no sessions', () => {
    expect(sidebarModel([])).toEqual({ needs: [], running: [], exited: [], counts: { needs: 0, running: 0, exited: 0 } })
  })
})

describe('blockSubtitle', () => {
  const at = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })
  it('says when the run started or stopped, and how many agents', () => {
    expect(blockSubtitle({ startedAt: '2026-10-01T08:31:00Z', agents: 3 })).toBe(`started ${at('2026-10-01T08:31:00Z')} · 3 agents`)
    expect(blockSubtitle({ startedAt: '2026-10-01T08:31:00Z', stoppedAt: '2026-10-01T08:40:00Z', agents: 1 })).toBe(`stopped ${at('2026-10-01T08:40:00Z')} · 1 agent`)
    expect(blockSubtitle({ agents: 2 })).toBe('2 agents')
    expect(blockSubtitle({ startedAt: 'never', agents: 2 })).toBe('started  · 2 agents')
  })
})

describe('sectionPreview', () => {
  it('lists the states of a section, a run by its members, six at most', () => {
    const m = sidebarModel([
      session({ id: 'a', attention: { state: 'needs_input' } }),
      session({ id: 'b', crew: { runId: 'r1', crewId: 'c', member: 'b' }, attention: { state: 'needs_input' } }),
      session({ id: 'c', crew: { runId: 'r1', crewId: 'c', member: 'c' } }),
      session({ id: 'd', crew: { runId: 'r1', crewId: 'c', member: 'd' }, status: 'exited' }),
    ])
    expect(sectionPreview(m.needs)).toEqual(['needs', 'needs', 'running', 'exited'])
    expect(sectionPreview(Array.from({ length: 9 }, (_, i) => sidebarModel([session({ id: String(i) })]).running[0]!))).toHaveLength(6)
  })
})

describe('folds', () => {
  it('starts with Exited folded and the rest open, and reads what was saved, field by field', () => {
    expect(DEFAULT_FOLDS).toEqual({ needs: false, running: false, shared: false, exited: true })
    expect(readFolds(memory())).toEqual(DEFAULT_FOLDS)
    expect(readFolds(memory({ [SIDEBAR_FOLDS_KEY]: JSON.stringify({ running: true, exited: false }) }))).toEqual({ needs: false, running: true, shared: false, exited: false })
    expect(readFolds(memory({ [SIDEBAR_FOLDS_KEY]: JSON.stringify({ running: 'yes', other: true }) }))).toEqual(DEFAULT_FOLDS)
    expect(readFolds(memory({ [SIDEBAR_FOLDS_KEY]: '{not json' }))).toEqual(DEFAULT_FOLDS)
    expect(readFolds(memory({ [SIDEBAR_FOLDS_KEY]: '[]' }))).toEqual(DEFAULT_FOLDS)
    expect(readFolds(broken)).toEqual(DEFAULT_FOLDS)
  })

  it('writes the folds and never throws', () => {
    const s = memory()
    const folds: Folds = { needs: true, running: false, shared: true, exited: true }
    writeFolds(s, folds)
    expect(readFolds(s)).toEqual(folds)
    expect(() => writeFolds(broken, folds)).not.toThrow()
  })

  it('reopens Needs you for a new prompt and otherwise leaves the folds alone', () => {
    const folded: Folds = { ...DEFAULT_FOLDS, needs: true }
    expect(reopenNeeds(folded, [])).toBe(folded)
    expect(reopenNeeds(folded, [session({ attention: { state: 'needs_input' } })])).toEqual({ ...folded, needs: false })
    const open: Folds = { ...DEFAULT_FOLDS }
    expect(reopenNeeds(open, [session({ attention: { state: 'needs_input' } })])).toBe(open)
  })
})

describe('focusRows and moveFocus', () => {
  const m = sidebarModel([
    session({ id: 'a', attention: { state: 'needs_input', options: [{ label: 'Yes', input: 'y\r' }] } }),
    session({ id: 'b', crew: { runId: 'r1', crewId: 'c', member: 'b' } }),
    session({ id: 'c', crew: { runId: 'r1', crewId: 'c', member: 'c' } }),
    session({ id: 'd', status: 'exited' }),
  ])
  const shared = [{ id: 'j1' }]

  it('lists the rows a key can land on, in order, a run header among them, and nothing from a folded section', () => {
    const rows = focusRows(m, shared, { ...DEFAULT_FOLDS, exited: false })
    expect(rows.map((r) => r.id)).toEqual(['s:a', 'r:r1', 's:b', 's:c', 'j:j1', 's:d'])
    expect(rows[0]).toEqual({ id: 's:a', kind: 'session', sessionId: 'a', runId: undefined, options: 1 })
    expect(rows[2]).toEqual({ id: 's:b', kind: 'session', sessionId: 'b', runId: 'r1', options: 0 })
    expect(focusRows(m, shared, DEFAULT_FOLDS).map((r) => r.id)).toEqual(['s:a', 'r:r1', 's:b', 's:c', 'j:j1'])
    expect(focusRows(m, shared, { needs: true, running: true, shared: true, exited: true })).toEqual([])
  })

  it('moves between rows, clamped at the ends, and enters the list at the first or the last', () => {
    const rows = focusRows(m, shared, DEFAULT_FOLDS)
    expect(moveFocus(rows, null, 1)).toBe('s:a')
    expect(moveFocus(rows, null, -1)).toBe('j:j1')
    expect(moveFocus(rows, 's:a', 1)).toBe('r:r1')
    expect(moveFocus(rows, 'r:r1', -1)).toBe('s:a')
    expect(moveFocus(rows, 's:a', -1)).toBe('s:a')
    expect(moveFocus(rows, 'j:j1', 1)).toBe('j:j1')
    expect(moveFocus(rows, 'gone', 1)).toBe('s:a')
    expect(moveFocus([], 's:a', 1)).toBeNull()
  })
})

describe('railModel', () => {
  it('draws the list in order: squares, a capsule holding its members, the shared squares, exited runs dashed and loose exited folded into +N', () => {
    const list = [
      session({ id: 'a', name: 'alone', createdAt: '2026-10-01T09:05:00Z' }),
      session({ id: 'b', name: 'core', crew: { runId: 'r1', crewId: 'api-sweep', member: 'core' }, createdAt: '2026-10-01T09:04:00Z' }),
      session({ id: 'c', name: 'waits', attention: { state: 'needs_input', since: '2026-10-01T09:06:00Z', message: 'Allow?' } }),
      session({ id: 'd', name: 'lead', crew: { runId: 'r1', crewId: 'api-sweep', member: 'lead' }, attention: { state: 'needs_input', since: '2026-10-01T09:07:00Z', message: 'Trust this folder?' }, createdAt: '2026-10-01T09:03:00Z' }),
      session({ id: 'e', name: 'other', crew: { runId: 'r2', crewId: 'docs', member: 'w' }, status: 'exited', endedAt: '2026-10-01T09:07:00Z' }),
      session({ id: 'h', name: 'laptop one', kind: 'hosted', hostName: 'laptop', status: 'starting' }),
      session({ id: 'x', name: 'old', status: 'exited', endedAt: '2026-10-01T09:01:00Z' }),
      session({ id: 'y', name: 'older', status: 'stopped', endedAt: '2026-10-01T09:00:00Z' }),
    ]
    const marks = { a: { type: 'done' as const, at: '2026-10-01T09:08:00Z', label: 'done', color: 'success' as const, detail: '' }, x: { type: 'error' as const, at: '2026-10-01T09:08:00Z', label: 'error', color: 'error' as const, detail: '' } }
    const shared = [{ id: 'j1', token: 't', server: '', kind: 'session' as const, name: 'api-review', role: 'view' as const, host: 'switchyard.example.net', agentId: 'codex', addedAt: '2026-10-01T09:00:00Z' }]
    const rail = railModel(sidebarModel(list, (id) => (id === 'r1' ? run({ startedAt: '2026-10-01T08:31:00Z', members: [member('lead'), member('core')] }) : undefined)), shared, marks)
    expect(rail.needs).toBe(2)
    expect(rail.news).toBe(2)
    expect(rail.exitedFolded).toBe(2)
    expect(rail.items.map((i) => `${i.shape}:${i.id}:${i.state}${i.dashed ? ':dashed' : ''}${i.tile ? ':' + i.tile : ''}${i.news ? ':news' + i.news : ''}`)).toEqual([
      'square:c:needs',
      'capsule:run:r1:needs',
      'square:a:running:news1',
      'square:h:idle:machine',
      'square:j:j1:idle:share',
      'capsule:run:r2:exited:dashed',
    ])
    const capsule = rail.items[1]!
    expect(capsule.amber).toBe(true)
    expect(capsule.members!.map((m) => `${m.id}:${m.state}`)).toEqual(['d:needs', 'b:running'])
    expect(capsule.label).toMatch(/^API sweep · run started \d\d:\d\d · lead needs you · core running$/)
    expect(rail.items[0]!.label).toBe('waits · needs you · Allow?')
    expect(rail.items[2]!.label).toBe('alone · running · done')
    expect(rail.items[3]!.label).toBe('laptop one · idle · on laptop')
    expect(rail.items[4]!.label).toBe('api-review · view only · through switchyard.example.net')
    expect(rail.items[5]!.members![0]!.dashed).toBe(true)
    // Unread chat joins the bottom-right number and the words; a run counts its own chat beside its members'.
    const withChat = railModel(sidebarModel(list, (id) => (id === 'r1' ? run({ startedAt: '2026-10-01T08:31:00Z', members: [member('lead'), member('core')] }) : undefined)), shared, marks, { 'session:a': 3, 'session:b': 1, 'run:r1': 2 })
    expect(withChat.news).toBe(4)
    expect(withChat.items[2]!.news).toBe(4)
    expect(withChat.items[2]!.label).toBe('alone · running · done · 3 unread in chat')
    expect(withChat.items[1]!.news).toBe(3)
    expect(withChat.items[1]!.chat).toBe(2)
    expect(withChat.items[1]!.label).toMatch(/· core running · 2 unread in chat$/)
    expect(withChat.items[1]!.members![1]!.news).toBe(1)
  })
})

describe('railGlyphs', () => {
  it("shows the agent's initials when no other square shares them", () => {
    const rail = railModel(sidebarModel([session({ id: 'a', agentId: 'claude', cwd: '/home/dev/conductor' }), session({ id: 'x', agentId: 'codex', cwd: '/home/dev/conductor' })]), [], {})
    expect(rail.items.map((i) => `${i.id}:${i.glyph}${i.ordinal ? ':' + i.ordinal : ''}`)).toEqual(['a:CC', 'x:CX'])
  })

  it("shows the folders' letters when two squares share an agent, and ordinals when the folders match too", () => {
    const list = [
      session({ id: 'a', agentId: 'claude', cwd: '/home/dev/conductor' }),
      session({ id: 'b', agentId: 'claude', cwd: '/home/dev/boxcar/' }),
      session({ id: 'c', agentId: 'claude', cwd: 'C:\\Users\\dev\\conductor' }),
      session({ id: 'd', agentId: 'claude', cwd: '/home/dev/conductor', crew: { runId: 'r1', crewId: 'api-sweep', member: 'lead' } }),
      session({ id: 'x', agentId: 'codex', cwd: '/home/dev/conductor' }),
    ]
    const rail = railModel(sidebarModel(list, (id) => (id === 'r1' ? run({ members: [member('lead')] }) : undefined)), [], {})
    const flat = rail.items.flatMap((i) => (i.shape === 'capsule' ? i.members! : [i]))
    // The rail's order: the loose running sessions, then the run's capsule with its member; the ordinals follow it.
    expect(flat.map((i) => `${i.id}:${i.glyph}${i.ordinal ? ':' + i.ordinal : ''}`)).toEqual(['a:CO:1', 'b:BO', 'c:CO:2', 'x:CX', 'd:CO:3'])
    // A capsule carries no letters of its own.
    expect(rail.items.find((i) => i.shape === 'capsule')!.glyph).toBeUndefined()
  })

  it('resolves letters that match across agents: Claude Code and Codex both in conductor and boxcar', () => {
    const list = [
      session({ id: 'a', agentId: 'claude', cwd: '/home/dev/conductor' }),
      session({ id: 'b', agentId: 'claude', cwd: '/home/dev/boxcar' }),
      session({ id: 'c', agentId: 'codex', cwd: '/home/dev/conductor' }),
      session({ id: 'd', agentId: 'codex', cwd: '/home/dev/boxcar' }),
    ]
    const rail = railModel(sidebarModel(list), [], {})
    expect(rail.items.map((i) => `${i.id}:${i.glyph}${i.ordinal ? ':' + i.ordinal : ''}`)).toEqual(['a:CO:1', 'b:BO:1', 'c:CO:2', 'd:BO:2'])
  })

  it('counts an exited run member as a square that could match', () => {
    const list = [
      session({ id: 'a', agentId: 'codex', cwd: '/home/dev/conductor' }),
      session({ id: 'm', agentId: 'codex', cwd: '/home/dev/review', crew: { runId: 'r1', crewId: 'api-sweep', member: 'review' }, status: 'exited', endedAt: '2026-10-01T09:07:00Z' }),
    ]
    const rail = railModel(sidebarModel(list, (id) => (id === 'r1' ? run({ members: [member('review', 'codex')] }) : undefined)), [], {})
    const flat = rail.items.flatMap((i) => (i.shape === 'capsule' ? i.members! : [i]))
    expect(flat.map((i) => `${i.id}:${i.glyph}${i.ordinal ? ':' + i.ordinal : ''}`)).toEqual(['a:CO', 'm:RE'])
  })

  it('keeps the agent initials for a link shared with you, which names no folder, and gives it an ordinal against a session of the same agent', () => {
    const shared = [{ id: 'j1', token: 't', server: '', kind: 'session' as const, name: 'api-review', role: 'view' as const, host: 'switchyard.example.net', agentId: 'claude', addedAt: '2026-10-01T09:00:00Z' }]
    const rail = railModel(sidebarModel([session({ id: 'a', agentId: 'claude', cwd: '/home/dev/conductor' })]), shared, {})
    expect(rail.items.map((i) => `${i.id}:${i.glyph}${i.ordinal ? ':' + i.ordinal : ''}`)).toEqual(['a:CO', 'j:j1:CC'])
    // Two shared links of one agent, neither naming a folder: ordinals.
    const twice = railModel(sidebarModel([]), [shared[0]!, { ...shared[0]!, id: 'j2', name: 'docs-review' }], {})
    expect(twice.items.map((i) => `${i.id}:${i.glyph}${i.ordinal ? ':' + i.ordinal : ''}`)).toEqual(['j:j1:CC:1', 'j:j2:CC:2'])
    const items = [...rail.items]
    railGlyphs(items)
    expect(items.map((i) => i.ordinal)).toEqual([undefined, undefined])
  })

  it('folderLetters takes the last element of a path, either slash, upper case', () => {
    expect(folderLetters('/home/dev/conductor')).toBe('CO')
    expect(folderLetters('/home/dev/conductor/')).toBe('CO')
    expect(folderLetters('C:\\Users\\dev\\boxcar')).toBe('BO')
    expect(folderLetters('/x')).toBe('X')
    expect(folderLetters('')).toBe('?')
  })
})
