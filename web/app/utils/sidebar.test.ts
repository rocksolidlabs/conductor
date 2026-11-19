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
  blockRows,
  blockSubtitle,
  exitLine,
  focusRows,
  mayRefocusRow,
  moveFocus,
  narrowSidebar,
  NARROW_SIDEBAR_PX,
  openSidebarSize,
  panelRow,
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
  sectionOf,
  sectionPreview,
  sessionOpen,
  sidebarBounds,
  sidebarModel,
  writeFolds,
  writeSidebarMode,
  writeSidebarSize,
  type Folds,
  type KeyValueStore,
  type ItemRow,
  type RunBlock,
  type SessionRow,
  type SidebarItem,
} from './sidebar'
import type { Decision } from './decisions'
import type { NeedsItem } from './needsYou'

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

describe('the full sidebar\'s width (#437, #1022)', () => {
  const at1440 = sidebarBounds(1440)
  const OPEN = 17.5

  it('opens at 17.5 rem (280 px), the width at which a row is whole, on every screen; a drag goes from 16 rem to 26 % of the window, never below the minimum plus 5 rem', () => {
    // 17.5 rem: the narrow list's bound (a row's question and its choices in the row from it on); 16 rem: every row names itself whole.
    expect(SIDEBAR_SIZE).toEqual({ min: 16, open: 17.5, maxShare: 0.26, range: 5 })
    expect(SIDEBAR_SIZE.open * 16).toBe(NARROW_SIDEBAR_PX)
    expect(narrowSidebar(SIDEBAR_SIZE.open * 16)).toBe(false)
    expect(narrowSidebar(SIDEBAR_SIZE.min * 16)).toBe(true)
    // In px at 16 px to the rem: the minimum 256 everywhere, the maximum 336 (the minimum plus 80) at 1024 and 1280, then 374, 499,
    // 666 and 894; the opening width 280 within every one of them.
    const px = (w: number) => {
      const b = sidebarBounds(w)
      return { min: b.min * 16, open: openSidebarSize(b) * 16, max: Math.round(b.max * 16) }
    }
    expect(px(1024)).toEqual({ min: 256, open: 280, max: 336 })
    expect(px(1280)).toEqual({ min: 256, open: 280, max: 336 })
    expect(px(1440)).toEqual({ min: 256, open: 280, max: 374 })
    expect(px(1920)).toEqual({ min: 256, open: 280, max: 499 })
    expect(px(2560)).toEqual({ min: 256, open: 280, max: 666 })
    expect(px(3440)).toEqual({ min: 256, open: 280, max: 894 })
    for (const w of [0, 320, 640, 768, 1024, 1440, 1920, 2560, 3440, 7680]) {
      const b = sidebarBounds(w)
      expect(b.min).toBe(16)
      expect(b.max - b.min).toBeGreaterThanOrEqual(5)
      expect(openSidebarSize(b)).toBe(OPEN)
    }
    // A window too narrow for 26 % to reach that (below lg, where no sidebar shows), or one not measured: the minimum plus 5 rem.
    expect(sidebarBounds(640)).toEqual({ min: 16, max: 21 })
    expect(sidebarBounds(Number.NaN)).toEqual({ min: 16, max: 21 })
    // Bigger text, bigger rem: the minimum stays 16 rem, the maximum is the same 26 % of the window.
    expect(sidebarBounds(2560, 20).max * 20).toBeCloseTo(665.6, 0)
    expect(sidebarBounds(1440, 0)).toEqual({ min: 16, max: 21 })
  })

  it('draws the held width within the window\'s bounds, from the same numbers', () => {
    expect(SIDEBAR_WIDTH).toBe('clamp(16rem, var(--width), max(21rem, 26vw))')
  })

  it('reads no saved width as the opening width, and a saved one within the bounds of the window it opens in', () => {
    expect(SIDEBAR_SIZE_KEY).toBe('conductor.sidebar.width')
    expect(readSidebarSize(memory(), at1440)).toBe(OPEN)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '18.5' }), at1440)).toBe(18.5)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '16' }), at1440)).toBe(16)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '40' }), at1440)).toBe(23.4)
    // A width dragged in a 2560 px window, opened in a 1024 px one: its maximum.
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '40' }), sidebarBounds(1024))).toBe(21)
    // A width an earlier version saved at its 200 px minimum (a double-click on the handle, or a drag to it), or anything else
    // narrower than the minimum: the opening width, not the minimum; the saved value stays until the next drag (#1022).
    const earlier = memory({ [SIDEBAR_SIZE_KEY]: '12.5' })
    expect(readSidebarSize(earlier, at1440)).toBe(OPEN)
    expect(earlier.data.get(SIDEBAR_SIZE_KEY)).toBe('12.5')
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '15.99' }), at1440)).toBe(OPEN)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '3' }), at1440)).toBe(OPEN)
    // A width saved while the minimum was 16.75 rem (#706) is kept as saved.
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '16.75' }), at1440)).toBe(16.75)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: 'wide' }), at1440)).toBe(OPEN)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: '' }), at1440)).toBe(OPEN)
    expect(readSidebarSize(memory({ [SIDEBAR_SIZE_KEY]: 'Infinity' }), at1440)).toBe(OPEN)
  })

  it('reads a width saved before #437, in percent of the window, as that share of the window it opens in, once', () => {
    expect(LEGACY_SIDEBAR_SIZE_KEY).toBe('conductor.sidebar.size')
    // 22 % of a 1440 px window (90 rem) is 19.8 rem: read, kept under the new key, the old key gone.
    const s = memory({ [LEGACY_SIDEBAR_SIZE_KEY]: '22' })
    expect(readSidebarSize(s, at1440, 90)).toBe(19.8)
    expect([...s.data.entries()]).toEqual([[SIDEBAR_SIZE_KEY, '19.8']])
    expect(readSidebarSize(s, at1440, 90)).toBe(19.8)
    // 30 % is past 26 %: the window's maximum.
    expect(readSidebarSize(memory({ [LEGACY_SIDEBAR_SIZE_KEY]: '30' }), at1440, 90)).toBe(23.4)
    // 14 % of 1440 px (201.6 px) is narrower than the minimum: the opening width, nothing kept.
    const narrow = memory({ [LEGACY_SIDEBAR_SIZE_KEY]: '14' })
    expect(readSidebarSize(narrow, at1440, 90)).toBe(OPEN)
    expect(narrow.data.size).toBe(0)
    // Not a share, or no window to take it of: the opening width, the old key gone.
    for (const bad of ['', 'wide', '0', '-5', '150']) {
      const b = memory({ [LEGACY_SIDEBAR_SIZE_KEY]: bad })
      expect(readSidebarSize(b, at1440, 90), bad).toBe(OPEN)
      expect(b.data.size, bad).toBe(0)
    }
    const unmeasured = memory({ [LEGACY_SIDEBAR_SIZE_KEY]: '22' })
    expect(readSidebarSize(unmeasured, at1440)).toBe(OPEN)
    expect(unmeasured.data.size).toBe(0)
    // Both keys there (an older tab wrote the old one again): the old one goes, the new one holds.
    const both = memory({ [LEGACY_SIDEBAR_SIZE_KEY]: '22', [SIDEBAR_SIZE_KEY]: '20' })
    expect(readSidebarSize(both, at1440, 90)).toBe(20)
    expect([...both.data.entries()]).toEqual([[SIDEBAR_SIZE_KEY, '20']])
  })

  it('writes the width within the bounds, two decimals at most, and skips what is not a number', () => {
    const s = memory()
    writeSidebarSize(s, 17.123456, at1440)
    expect(s.data.get(SIDEBAR_SIZE_KEY)).toBe('17.12')
    expect(readSidebarSize(s, at1440)).toBe(17.12)
    writeSidebarSize(s, 99, at1440)
    expect(s.data.get(SIDEBAR_SIZE_KEY)).toBe('23.4')
    writeSidebarSize(s, 2, at1440)
    expect(s.data.get(SIDEBAR_SIZE_KEY)).toBe('16')
    writeSidebarSize(s, Number.NaN, at1440)
    expect(s.data.get(SIDEBAR_SIZE_KEY)).toBe('16')
  })

  it('is the opening width when storage throws, and writing never throws', () => {
    expect(readSidebarSize(broken, at1440, 90)).toBe(OPEN)
    expect(() => writeSidebarSize(broken, 20, at1440)).not.toThrow()
    // Removing the old key throws alone: the saved width still reads.
    const stubborn = { ...memory({ [LEGACY_SIDEBAR_SIZE_KEY]: '22', [SIDEBAR_SIZE_KEY]: '18' }), removeItem: () => { throw new Error('denied') } }
    expect(readSidebarSize(stubborn, at1440, 90)).toBe(18)
  })
})

describe('rowState', () => {
  it("is the session's status key: exited once ended, needs while it asks, working in a turn, finished after one, running otherwise", () => {
    expect(rowState(session({ status: 'exited' }))).toBe('exited')
    expect(rowState(session({ status: 'stopped', attention: { state: 'needs_input' } }))).toBe('exited')
    expect(rowState(session({ attention: { state: 'needs_input' } }))).toBe('needs')
    expect(rowState(session({ status: 'starting', attention: { state: 'needs_input' } }))).toBe('needs')
    expect(rowState(session({ attention: { state: 'working', source: 'api' }, turns: true }))).toBe('working')
    expect(rowState(session({ attention: { state: 'done', source: 'api' }, turns: true }))).toBe('finished')
    expect(rowState(session({ status: 'running' }))).toBe('running')
    expect(rowState(session({ status: 'starting' }))).toBe('running')
  })

  // The UX review's P4: each status key to its section; a host away both ways.
  it('places each key in its section, Working holding the working, the running and the starting', () => {
    const at = (over: Partial<SessionInfo>) => sectionOf(rowState(session(over)))
    expect(at({ attention: { state: 'needs_input' } })).toBe('needs')
    expect(at({ attention: { state: 'done', source: 'api' }, turns: true })).toBe('finished')
    expect(at({ attention: { state: 'working', source: 'api' }, turns: true })).toBe('working')
    expect(at({ status: 'running' })).toBe('working')
    expect(at({ status: 'starting' })).toBe('working')
    expect(at({ status: 'exited' })).toBe('exited')
    // A host away that asks stays in Needs you; one that does not ask is in Exited, said "host away".
    expect(at({ status: 'host_disconnected' as SessionInfo['status'], attention: { state: 'needs_input' } })).toBe('needs')
    expect(at({ status: 'host_disconnected' as SessionInfo['status'] })).toBe('exited')
    const away = sidebarModel([session({ id: 'away', status: 'host_disconnected' as SessionInfo['status'] })])
    expect((away.exited[0] as SessionRow).exitWord).toBe('host away')
    // Its line has no age: the listing does not say when the host went, and three hours since its start is not that.
    const now = Date.parse('2026-10-01T12:00:00Z')
    expect(exitLine(away.exited[0] as SessionRow, now)).toBe('host away')
    // An ended session says how long ago it ended.
    const ended = sidebarModel([session({ id: 'done', status: 'exited', exitCode: 1, endedAt: '2026-10-01T11:57:00Z' })])
    expect(exitLine(ended.exited[0] as SessionRow, now)).toBe('exit 1 · 3m ago')
  })
})

describe('sidebarModel', () => {
  const ids = (items: readonly SidebarItem[]) => items.map((it) => (it.kind === 'run' ? `${it.runId}[${it.members.map((m) => m.id).join(',')}]` : it.id))

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
    expect(ids(m.working)).toEqual(['alone'])
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
    expect(m.counts).toEqual({ needs: 1, finished: 0, working: 1, exited: 0 })
  })

  it('places a wholly exited run in Exited, a run of running members in Working, and names a forgotten run by its crew', () => {
    const list = [
      session({ id: 'a', crew: { runId: 'r1', crewId: 'docs', member: 'w' }, status: 'exited', endedAt: '2026-10-01T09:07:00Z' }),
      session({ id: 'b', crew: { runId: 'r1', crewId: 'docs', member: 'x' }, status: 'stopped', endedAt: '2026-10-01T09:08:00Z' }),
      session({ id: 'c', crew: { runId: 'r2', crewId: 'api', member: 'core' }, createdAt: '2026-10-01T09:02:00Z' }),
      session({ id: 'd', crew: { runId: 'r2', crewId: 'api', member: 'lead' }, status: 'starting', createdAt: '2026-10-01T09:01:00Z' }),
      session({ id: 'e', status: 'exited', endedAt: '2026-10-01T09:09:00Z' }),
    ]
    const m = sidebarModel(list, (id) => (id === 'r2' ? run({ id: 'r2', crewId: 'api', name: 'API', label: 'Monday run', members: [member('lead'), member('core')] }) : undefined))
    // Both members are running (no signal; starting takes its mark): the crew's order.
    expect(ids(m.working)).toEqual(['r2[d,c]'])
    expect((m.working[0] as RunBlock).title).toBe('Monday run')
    expect((m.working[0] as RunBlock).state).toBe('running')
    // A run the server forgot keeps its members in the order they came.
    expect(ids(m.exited)).toEqual(['e', 'r1[a,b]'])
    expect((m.exited[1] as RunBlock).title).toBe('docs')
    expect((m.exited[1] as RunBlock).agents).toBe(2)
    expect(m.counts).toEqual({ needs: 0, finished: 0, working: 2, exited: 3 })
  })

  it('tags a hosted session with its machine, under no heading, and never says "server" for it', () => {
    const list = [session({ id: 'h1', kind: 'hosted', hostName: 'priya-mbp' }), session({ id: 'h2', kind: 'hosted', hostName: '' }), session({ id: 'm' })]
    const m = sidebarModel(list)
    const rows = m.working as SessionRow[]
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
    expect(ids(m.working)).toEqual(['r-new', 'r-old'])
  })

  it('has empty sections and zero counts for no sessions', () => {
    expect(sidebarModel([])).toEqual({ needs: [], finished: [], working: [], exited: [], counts: { needs: 0, finished: 0, working: 0, exited: 0 } })
  })
})

// The UX review's P4: Finished apart from Working, in the order of how hard a thing waits on you.
describe('sidebarModel: Finished apart from Working (UX1 PR-14)', () => {
  const ids = (items: readonly SidebarItem[]) => items.map((it) => (it.kind === 'run' ? `${it.runId}[${it.members.map((m) => m.id).join(',')}]` : it.id))
  const at = (m: number) => `2026-10-01T09:${String(m).padStart(2, '0')}:00Z`
  const working = (id: string, over: Partial<SessionInfo> = {}) => session({ id, turns: true, attention: { state: 'working', source: 'api', since: at(1) }, ...over })
  const done = (id: string, m: number, over: Partial<SessionInfo> = {}) => session({ id, turns: true, attention: { state: 'done', source: 'api', since: at(m) }, ...over })
  const crew = (runId: string, member: string) => ({ crew: { runId, crewId: 'c', member } })

  it('gives the sections in their order, Finished the latest finished first', () => {
    const m = sidebarModel([
      session({ id: 'asks', attention: { state: 'needs_input', since: at(2) } }),
      done('early', 3),
      done('late', 9),
      working('busy'),
      session({ id: 'quiet' }),
      session({ id: 'gone', status: 'exited', endedAt: at(4) }),
    ])
    expect(Object.keys(m).filter((k) => k !== 'counts')).toEqual(['needs', 'finished', 'working', 'exited'])
    expect(ids(m.needs)).toEqual(['asks'])
    expect(ids(m.finished)).toEqual(['late', 'early'])
    expect(ids(m.working).sort()).toEqual(['busy', 'quiet'])
    expect(ids(m.exited)).toEqual(['gone'])
    expect(m.counts).toEqual({ needs: 1, finished: 2, working: 2, exited: 1 })
  })

  it('places a run of two working and two finished in Working once, its subtitle saying so', () => {
    const r1 = run({ members: [member('a'), member('b'), member('c'), member('d')] })
    const m = sidebarModel([working('a', crew('r1', 'a')), done('b', 5, crew('r1', 'b')), working('c', crew('r1', 'c')), done('d', 6, crew('r1', 'd'))], (id) => (id === 'r1' ? r1 : undefined))
    expect(ids(m.working)).toEqual(['r1[a,c,b,d]'])
    expect(m.finished).toEqual([])
    const block = m.working[0] as RunBlock
    expect(blockSubtitle(block)).toBe(`started ${new Date('2026-10-01T08:31:00Z').toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })} · 4 agents · 2 working · 2 finished`)
    // Its capsule on the rail says the same key as the section that holds it.
    expect(railModel(m, [], {}).items[0]!.status?.key).toBe('working')
    expect(m.counts.working).toBe(4)
  })

  it('places a run of finished members in Finished, its capsule finished too', () => {
    const r1 = run({ members: [member('a'), member('b')] })
    const m = sidebarModel([done('a', 5, crew('r1', 'a')), done('b', 6, crew('r1', 'b'))], (id) => (id === 'r1' ? r1 : undefined))
    expect(ids(m.finished)).toEqual(['r1[a,b]'])
    expect(m.working).toEqual([])
    expect(railModel(m, [], {}).items[0]!.status?.key).toBe('finished')
    // A run with a member that sends no signal is in Working: its busiest member is running.
    const mixed = sidebarModel([done('a', 5, crew('r1', 'a')), session({ id: 'b', ...crew('r1', 'b') })], (id) => (id === 'r1' ? r1 : undefined))
    expect(ids(mixed.working)).toEqual(['r1[b,a]'])
    expect(railModel(mixed, [], {}).items[0]!.status?.key).toBe('running')
  })

  it('lists the focus rows Finished before Working, none from a folded one', () => {
    const m = sidebarModel([working('w'), done('f', 5)])
    expect(focusRows(m, [], DEFAULT_FOLDS).map((r) => r.id)).toEqual(['s:f', 's:w'])
    expect(focusRows(m, [], { ...DEFAULT_FOLDS, finished: true }).map((r) => r.id)).toEqual(['s:w'])
    expect(sectionPreview(m.finished)).toEqual(['finished'])
    expect(sectionPreview(m.working)).toEqual(['working'])
  })
})

const T13 = (m: number) => `2026-10-01T09:${String(m).padStart(2, '0')}:00Z`
function decisionItem(id: string, runId: string, since: string, stopped = false): NeedsItem {
  const decision = { id, run: runId, openedIn: runId, origin: 'plan', type: 'approval', gate: 'ship', request: { question: `May ${id} go out?` }, policy: {}, state: 'open', rev: 1, createdAt: since } as unknown as Decision
  return { kind: 'decision', ref: `decision:${id}`, run: runId, since, decision, stopped }
}
function inboxNeed(kind: 'task' | 'plan', ref: string, since: string, run?: string): NeedsItem {
  return { kind, ref, run, since, summary: { ref, kind, since, title: `${kind} ${ref}`, brief: 'its brief', question: '', answerer: '', actions: [] } }
}

// The UX review's P3: Needs you lists every kind that waits on a person, a run that waits sits there whatever its members do.
describe('sidebarModel with the one list of what needs you (UX1 PR-13)', () => {
  const ids = (items: readonly SidebarItem[]) => items.map((it) => (it.kind === 'run' ? `${it.runId}[${[...it.items.map((x) => x.id), ...it.members.map((m) => m.id)].join(',')}]` : it.id))
  const working = (id: string, runId: string) => session({ id, crew: { runId, crewId: 'c', member: id }, attention: { state: 'working' } as SessionInfo['attention'] })

  it('puts a run whose members all work but whose task failed in Needs you, the task under its header before its members', () => {
    const list = [working('core', 'r1'), working('lead', 'r1'), session({ id: 'alone' })]
    const r1 = run({ members: [member('lead'), member('core')] })
    const task = inboxNeed('task', 'task:r1/build', T13(3), 'r1')
    const m = sidebarModel(list, (id) => (id === 'r1' ? r1 : undefined), [task])
    expect(ids(m.needs)).toEqual(['r1[i:task:r1/build,lead,core]'])
    expect(ids(m.working)).toEqual(['alone'])
    const block = m.needs[0] as RunBlock
    expect(block.state).toBe('needs')
    expect(block.needs).toBe(1)
    expect(block.items[0]).toEqual({ kind: 'item', id: 'i:task:r1/build', item: task })
    expect(m.counts.needs).toBe(1)
    // Its working members fold under one line: the task is what the block shows.
    expect(blockRows(block, 'needs')).toEqual({ members: [], more: { count: 2, words: 'and 2 more · 2 working' } })
    // Without the list the run is in Running, with no item rows.
    const before = sidebarModel(list, (id) => (id === 'r1' ? r1 : undefined))
    expect(ids(before.working)).toEqual(['alone', 'r1[lead,core]'])
    expect(before.needs).toEqual([])
  })

  it('puts a stopped run with an open decision in Needs you, its members drawn exited', () => {
    const list = [
      session({ id: 'a', crew: { runId: 'r2', crewId: 'c', member: 'a' }, status: 'stopped', endedAt: T13(5) }),
      session({ id: 'b', crew: { runId: 'r2', crewId: 'c', member: 'b' }, status: 'exited', exitCode: 0, endedAt: T13(6) }),
    ]
    const r2 = run({ id: 'r2', stoppedAt: T13(6), members: [member('a'), member('b')] })
    const m = sidebarModel(list, (id) => (id === 'r2' ? r2 : undefined), [decisionItem('dr-3', 'r2', T13(2), true)])
    expect(ids(m.needs)).toEqual(['r2[d:dr-3,a,b]'])
    expect(m.exited).toEqual([])
    const block = m.needs[0] as RunBlock
    expect(block.members.map((x) => x.state)).toEqual(['exited', 'exited'])
    expect(block.stoppedAt).toBe(T13(6))
    expect(m.counts).toEqual({ needs: 1, finished: 0, working: 0, exited: 0 })
  })

  it('lists a plan, and a decision whose run the store does not hold, as loose rows, newest first beside the questions', () => {
    const asker = session({ id: 'ask', attention: { state: 'needs_input', since: T13(4) } })
    const plan = inboxNeed('plan', 'plan:CND-4/1', T13(7))
    const lone = decisionItem('dr-9', 'gone', T13(1))
    const m = sidebarModel([asker], () => undefined, [plan, lone, { kind: 'question', ref: 'question:ask/q1', since: T13(4), session: asker }])
    // The question is the session's own row, never a second one.
    expect(ids(m.needs)).toEqual(['i:plan:CND-4/1', 'ask', 'd:dr-9'])
    expect((m.needs[0] as ItemRow).item).toBe(plan)
    expect(m.counts.needs).toBe(3)
    expect(focusRows(m, [], DEFAULT_FOLDS)).toEqual([
      { id: 'i:plan:CND-4/1', kind: 'item', ref: 'plan:CND-4/1' },
      { id: 's:ask', kind: 'session', sessionId: 'ask', runId: undefined, options: 0 },
      { id: 'd:dr-9', kind: 'decision', decision: 'dr-9' },
    ])
    expect(focusRows(m, [], { ...DEFAULT_FOLDS, needs: true })).toEqual([])
    expect(sectionPreview(m.needs)).toEqual(['needs', 'needs', 'needs'])
  })

  it("gives a focus row to each item of a run, after its header, in the list's order", () => {
    const list = [working('core', 'r1'), session({ id: 'rev', crew: { runId: 'r1', crewId: 'c', member: 'rev' }, attention: { state: 'needs_input', since: T13(9) } })]
    const r1 = run({ members: [member('rev'), member('core')] })
    const m = sidebarModel(list, (id) => (id === 'r1' ? r1 : undefined), [decisionItem('dr-1', 'r1', T13(1)), inboxNeed('task', 'task:r1/t', T13(2), 'r1')])
    expect(focusRows(m, [], DEFAULT_FOLDS).map((r) => r.id)).toEqual(['r:r1', 'd:dr-1', 'i:task:r1/t', 's:rev', 'm:r1'])
    expect((m.needs[0] as RunBlock).needs).toBe(3)
  })

  it('draws a run that waits with no session listed: its header and its items, no member rows', () => {
    const gate = decisionItem('dr-2', 'r-gate', T13(0))
    const aged = inboxNeed('task', 'task:r-old/t', T13(1), 'r-old')
    const runs: Record<string, RunInfo> = { 'r-gate': run({ id: 'r-gate', label: 'Gate run', members: [member('deploy')] }), 'r-old': run({ id: 'r-old', name: 'Old run', members: [member('a'), member('b')] }) }
    const m = sidebarModel([], (id) => runs[id], [gate, aged])
    expect(ids(m.needs)).toEqual(['r-old[i:task:r-old/t]', 'r-gate[d:dr-2]'])
    const block = m.needs[1] as RunBlock
    expect([block.title, block.agents, block.members, block.state]).toEqual(['Gate run', 1, [], 'needs'])
    expect(blockRows(block, 'needs')).toEqual({ members: [] })
    expect(focusRows(m, [], DEFAULT_FOLDS).map((r) => r.id)).toEqual(['r:r-old', 'i:task:r-old/t', 'r:r-gate', 'd:dr-2'])
    // The rail draws them as capsules too, amber.
    const rail = railModel(m, [], {})
    expect(rail.items.map((it) => [it.id, it.amber, it.status?.key])).toEqual([
      ['run:r-old', true, 'needs'],
      ['run:r-gate', true, 'needs'],
    ])
  })

  it("marks a waiting run's capsule amber on the rail, though its members work", () => {
    const list = [working('core', 'r1')]
    const r1 = run({ members: [member('core')] })
    const rail = railModel(sidebarModel(list, (id) => (id === 'r1' ? r1 : undefined), [inboxNeed('task', 'task:r1/build', T13(3), 'r1')]), [], {})
    expect(rail.items[0]).toMatchObject({ id: 'run:r1', amber: true, state: 'needs' })
    expect(rail.items[0]!.status?.key).toBe('needs')
    expect(rail.items[0]!.label).toContain('task needs you')
    expect(rail.needs).toBe(1)
    // Without the item the capsule is the members' alone.
    const quiet = railModel(sidebarModel(list, (id) => (id === 'r1' ? r1 : undefined)), [], {})
    expect(quiet.items[0]).toMatchObject({ amber: false, state: 'working' })
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
  it('lists the status keys of a section, a run by its members, six at most', () => {
    const m = sidebarModel([
      session({ id: 'a', attention: { state: 'needs_input' } }),
      session({ id: 'b', crew: { runId: 'r1', crewId: 'c', member: 'b' }, attention: { state: 'needs_input' } }),
      session({ id: 'c', crew: { runId: 'r1', crewId: 'c', member: 'c' }, attention: { state: 'working', source: 'api' }, turns: true }),
      session({ id: 'e', crew: { runId: 'r1', crewId: 'c', member: 'e' }, attention: { state: 'done', source: 'api' }, turns: true }),
      session({ id: 'f', crew: { runId: 'r1', crewId: 'c', member: 'f' } }),
      session({ id: 'd', crew: { runId: 'r1', crewId: 'c', member: 'd' }, status: 'exited' }),
    ])
    // Each state of the status table gives its own key: the folded squares are StatusMarks of these.
    expect(sectionPreview(m.needs).sort()).toEqual(['exited', 'finished', 'needs', 'needs', 'running', 'working'])
    expect(sectionPreview(Array.from({ length: 9 }, (_, i) => sidebarModel([session({ id: String(i) })]).working[0]!))).toHaveLength(6)
  })
})

describe('folds', () => {
  it('starts with Exited folded and the rest open, and reads what was saved, field by field', () => {
    expect(DEFAULT_FOLDS).toEqual({ needs: false, finished: false, working: false, shared: false, exited: true })
    expect(readFolds(memory())).toEqual(DEFAULT_FOLDS)
    expect(readFolds(memory({ [SIDEBAR_FOLDS_KEY]: JSON.stringify({ working: true, finished: true, exited: false }) }))).toEqual({ needs: false, finished: true, working: true, shared: false, exited: false })
    // A fold saved for Running, before it split, folds Working; Finished starts unfolded.
    expect(readFolds(memory({ [SIDEBAR_FOLDS_KEY]: JSON.stringify({ running: true, exited: false }) }))).toEqual({ needs: false, finished: false, working: true, shared: false, exited: false })
    expect(readFolds(memory({ [SIDEBAR_FOLDS_KEY]: JSON.stringify({ running: 'yes', other: true }) }))).toEqual(DEFAULT_FOLDS)
    expect(readFolds(memory({ [SIDEBAR_FOLDS_KEY]: '{not json' }))).toEqual(DEFAULT_FOLDS)
    expect(readFolds(memory({ [SIDEBAR_FOLDS_KEY]: '[]' }))).toEqual(DEFAULT_FOLDS)
    expect(readFolds(broken)).toEqual(DEFAULT_FOLDS)
  })

  it('writes the folds and never throws', () => {
    const s = memory()
    const folds: Folds = { needs: true, finished: false, working: true, shared: true, exited: true }
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
    expect(focusRows(m, shared, { needs: true, finished: true, working: true, shared: true, exited: true })).toEqual([])
  })

  // Codex's round 1 on #719: Needs you's decisions are rows a key lands on, so Tab or ↓ onto one moves the list's focus there and
  // Enter opens its card, not the session before it. A decision of a run the store does not hold is a loose row of Needs you.
  it("lists Needs you's loose decisions as rows, and none from a folded Needs you", () => {
    const withDecisions = sidebarModel(
      [
        session({ id: 'a', attention: { state: 'needs_input', since: '2026-10-01T09:09:00Z', options: [{ label: 'Yes', input: 'y\r' }] } }),
        session({ id: 'b', crew: { runId: 'r1', crewId: 'c', member: 'b' } }),
        session({ id: 'c', crew: { runId: 'r1', crewId: 'c', member: 'c' } }),
      ],
      () => undefined,
      [decisionItem('dr-1', 'gone', '2026-10-01T09:02:00Z'), decisionItem('dr-2', 'gone', '2026-10-01T09:01:00Z')],
    )
    const rows = focusRows(withDecisions, shared, DEFAULT_FOLDS)
    expect(rows.map((r) => r.id)).toEqual(['s:a', 'd:dr-1', 'd:dr-2', 'r:r1', 's:b', 's:c', 'j:j1'])
    expect(rows[1]).toEqual({ id: 'd:dr-1', kind: 'decision', decision: 'dr-1' })
    expect(moveFocus(rows, 's:a', 1)).toBe('d:dr-1')
    expect(focusRows(withDecisions, shared, { ...DEFAULT_FOLDS, needs: true }).map((r) => r.id)).not.toContain('d:dr-1')
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

describe('blockRows', () => {
  // A run of three: review asks, lead works (in a turn), docs finished its turn.
  const three = sidebarModel(
    [
      session({ id: 'review', crew: { runId: 'r3', crewId: 'c', member: 'review' }, attention: { state: 'needs_input', message: 'Trust this folder?' } }),
      session({ id: 'lead', crew: { runId: 'r3', crewId: 'c', member: 'lead' }, turns: true, attention: { state: 'working' } }),
      session({ id: 'docs', crew: { runId: 'r3', crewId: 'c', member: 'docs' }, turns: true, attention: { state: 'done', since: '2026-10-01T09:00:00Z' } }),
    ],
    () => run({ id: 'r3', members: [member('lead'), member('review'), member('docs')] }),
  ).needs[0] as RunBlock

  it('in Needs you shows the members that ask and one line for the rest, told by their status words', () => {
    const shown = blockRows(three, 'needs')
    expect(shown.members.map((m) => m.id)).toEqual(['review'])
    expect(shown.more).toEqual({ count: 2, words: 'and 2 more · 1 working · 1 finished' })
  })

  it('shows every member elsewhere, once unfolded, and when none or all of them ask', () => {
    expect(blockRows(three, 'working').members.map((m) => m.id)).toEqual(['review', 'lead', 'docs'])
    expect(blockRows(three, 'working').more).toBeUndefined()
    expect(blockRows(three, 'needs', true)).toEqual({ members: three.members })
    const calm = { members: three.members.filter((m) => m.state !== 'needs') }
    expect(blockRows(calm, 'needs')).toEqual({ members: calm.members })
    const asking = { members: three.members.filter((m) => m.state === 'needs') }
    expect(blockRows(asking, 'needs')).toEqual({ members: asking.members })
  })

  it("gives the line a row a key lands on after the asking members, and none for an unfolded run", () => {
    const model = sidebarModel(three.members.map((m) => m.session), () => run({ id: 'r3', members: [member('lead'), member('review'), member('docs')] }))
    expect(focusRows(model, [], DEFAULT_FOLDS).map((r) => r.id)).toEqual(['r:r3', 's:review', 'm:r3'])
    expect(focusRows(model, [], DEFAULT_FOLDS)[2]).toEqual({ id: 'm:r3', kind: 'more', runId: 'r3' })
    expect(focusRows(model, [], DEFAULT_FOLDS, new Set(['r3'])).map((r) => r.id)).toEqual(['r:r3', 's:review', 's:lead', 's:docs'])
  })
})

describe('narrowSidebar and panelRow', () => {
  it('is narrow below 280 px, never for the phone\'s list', () => {
    expect(NARROW_SIDEBAR_PX).toBe(280)
    expect(narrowSidebar(199.9)).toBe(true)
    expect(narrowSidebar(279.9)).toBe(true)
    expect(narrowSidebar(280)).toBe(false)
    expect(narrowSidebar(600)).toBe(false)
    expect(narrowSidebar(200, true)).toBe(false)
    expect(narrowSidebar(Number.NaN)).toBe(false)
  })

  const m = sidebarModel([
    session({ id: 'a', attention: { state: 'needs_input', message: 'Which branch?' } }),
    session({ id: 'b', attention: { state: 'needs_input', message: 'Ship it?', options: [{ label: 'Yes', input: 'y' }] } }),
    session({ id: 'c' }),
    session({ id: 'm', crew: { runId: 'r1', crewId: 'c', member: 'm' } }),
  ])
  const rows = new Map<string, SessionRow | RunBlock>()
  for (const it of [...m.needs, ...m.finished, ...m.working]) {
    if (it.kind === 'run') {
      rows.set(`r:${it.runId}`, it)
      for (const x of it.members) rows.set(`s:${x.id}`, x)
    } else if (it.kind === 'session') rows.set(`s:${it.id}`, it)
  }

  it('is the focused row while it asks, the pressed one over the focused, and none when wide or the row asks to stop', () => {
    expect(panelRow('s:a', null, rows, true)).toBe('s:a')
    expect(panelRow('s:c', null, rows, true)).toBeNull()
    expect(panelRow('r:r1', null, rows, true)).toBeNull()
    expect(panelRow('s:a', 's:b', rows, true)).toBe('s:b')
    expect(panelRow(null, null, rows, true)).toBeNull()
    expect(panelRow('s:a', 's:b', rows, false)).toBeNull()
    expect(panelRow('s:a', null, rows, true, 's:a')).toBeNull()
    expect(panelRow('s:gone', null, rows, true)).toBeNull()
  })
})

describe('mayRefocusRow (#596)', () => {
  // Stand-ins for the elements (the suite runs without a DOM): only their identity is read.
  const body = { name: 'body' } as unknown as Node
  const reply = { name: "a row's reply field, in the list" } as unknown as Node
  const stop = { name: "a row's Stop question, in the list" } as unknown as Node
  const dialog = { name: 'a dialog' } as unknown as Node

  it('gives a re-rendered row the focus back only when the focus was lost', () => {
    expect(mayRefocusRow(null, body, false)).toBe(true)
    expect(mayRefocusRow(body, body, false)).toBe(true)
  })

  it('never takes it from anything that holds it: a dialog opened from the row, or a reply field or Stop question in the list', () => {
    expect(mayRefocusRow(dialog, body, false)).toBe(false)
    expect(mayRefocusRow(reply, body, false)).toBe(false)
    expect(mayRefocusRow(stop, body, false)).toBe(false)
  })

  it('never takes it behind an open modal, even with the focus on nothing (a focused button gone disabled as it saves)', () => {
    expect(mayRefocusRow(body, body, true)).toBe(false)
    expect(mayRefocusRow(null, body, true)).toBe(false)
    expect(mayRefocusRow(reply, body, true)).toBe(false)
  })
})

describe('railModel', () => {
  it('draws the list in order: squares, a capsule holding its members, the shared squares, exited runs dashed and loose exited folded into +N', () => {
    const list = [
      session({ id: 'a', name: 'alone', createdAt: '2026-10-01T09:05:00Z', turns: true, attention: { state: 'done', source: 'api', since: '2026-10-01T09:06:00Z' } }),
      session({ id: 'b', name: 'core', crew: { runId: 'r1', crewId: 'api-sweep', member: 'core' }, createdAt: '2026-10-01T09:04:00Z' }),
      session({ id: 'c', name: 'waits', attention: { state: 'needs_input', since: '2026-10-01T09:06:00Z', message: 'Allow?' } }),
      session({ id: 'd', name: 'lead', crew: { runId: 'r1', crewId: 'api-sweep', member: 'lead' }, attention: { state: 'needs_input', since: '2026-10-01T09:07:00Z', message: 'Trust this folder?' }, createdAt: '2026-10-01T09:03:00Z' }),
      session({ id: 'e', name: 'other', crew: { runId: 'r2', crewId: 'docs', member: 'w' }, status: 'exited', endedAt: '2026-10-01T09:07:00Z' }),
      session({ id: 'h', name: 'laptop one', kind: 'hosted', hostName: 'laptop', status: 'starting' }),
      session({ id: 'x', name: 'old', status: 'exited', endedAt: '2026-10-01T09:01:00Z' }),
      session({ id: 'y', name: 'older', status: 'stopped', endedAt: '2026-10-01T09:00:00Z' }),
    ]
    const marks = { a: { type: 'done' as const, at: '2026-10-01T09:08:00Z', label: 'finished', color: 'neutral' as const, detail: '' }, x: { type: 'error' as const, at: '2026-10-01T09:08:00Z', label: 'error', color: 'error' as const, detail: '' } }
    const shared = [{ id: 'j1', token: 't', server: '', kind: 'session' as const, name: 'api-review', role: 'view' as const, host: 'switchyard.example.net', agentId: 'codex', addedAt: '2026-10-01T09:00:00Z' }]
    const now = Date.parse('2026-10-01T09:08:30Z')
    const rail = railModel(sidebarModel(list, (id) => (id === 'r1' ? run({ startedAt: '2026-10-01T08:31:00Z', members: [member('lead'), member('core')] }) : undefined)), shared, marks, {}, now)
    expect(rail.needs).toBe(2)
    expect(rail.news).toBe(2)
    expect(rail.exitedFolded).toBe(2)
    expect(rail.items.map((i) => `${i.shape}:${i.id}:${i.state}${i.dashed ? ':dashed' : ''}${i.tile ? ':' + i.tile : ''}${i.news ? ':news' + i.news : ''}`)).toEqual([
      'square:c:needs',
      'capsule:run:r1:needs',
      'square:a:finished:news1',
      'square:h:running:machine',
      'square:j:j1:running:share',
      'capsule:run:r2:exited:dashed',
    ])
    const capsule = rail.items[1]!
    expect(capsule.amber).toBe(true)
    expect(capsule.members!.map((m) => `${m.id}:${m.state}`)).toEqual(['d:needs', 'b:running'])
    expect(capsule.label).toMatch(/^API sweep · run started \d\d:\d\d · lead needs you · core running$/)
    // The corner marks: each square's status, the capsule its busiest member's, a share's what the entry knows (here nothing).
    expect(rail.items.map((i) => i.status?.key ?? '-')).toEqual(['needs', 'needs', 'finished', 'running', '-', 'exited'])
    expect(rail.items[0]!.label).toBe('waits · needs you · Allow?')
    // alone's finished turn is counted in its number, and said once: by its status's word, not again by its done mark's.
    expect(rail.items[2]!.label).toBe('alone · finished 2m ago')
    expect(rail.items[3]!.label).toBe('laptop one · starting · on laptop')
    expect(rail.items[4]!.label).toBe('api-review · view only · through switchyard.example.net')
    expect(rail.items[5]!.members![0]!.dashed).toBe(true)
    // Unread chat joins the bottom-right number and the words; a run counts its own chat beside its members'.
    const withChat = railModel(sidebarModel(list, (id) => (id === 'r1' ? run({ startedAt: '2026-10-01T08:31:00Z', members: [member('lead'), member('core')] }) : undefined)), shared, marks, { 'session:a': 3, 'session:b': 1, 'run:r1': 2 }, now)
    expect(withChat.news).toBe(4)
    expect(withChat.items[2]!.news).toBe(4)
    expect(withChat.items[2]!.label).toBe('alone · finished 2m ago · 3 unread in chat')
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
