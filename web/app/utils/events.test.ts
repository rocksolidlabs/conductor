import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SessionInfo } from '~/composables/useSessions'
import type { ActivityEntry, AttentionState } from '~/utils/protocol'
import {
  DEFAULT_ROUTES,
  EVENT_INFO,
  EVENT_TYPES,
  EntryHold,
  appendRing,
  attentionSettled,
  entryIcon,
  eventsRedirect,
  eventAlert,
  eventDetail,
  eventTypeOf,
  feedTime,
  followJump,
  linkableUrl,
  liveEvent,
  markOf,
  parseRoutes,
  pruneMarks,
  routeEntry,
  webhookHosts,
  type EventState,
  type EventType,
  type RouteRow,
} from './events'

function session(state: AttentionState, message?: string): SessionInfo {
  return {
    id: 's1',
    name: 'api-sweep',
    kind: 'server',
    agentId: 'claude',
    command: ['claude'],
    cwd: '/',
    status: 'running',
    cols: 80,
    rows: 24,
    viewers: 0,
    attention: { state, message },
    createdAt: '2026-09-29T00:00:00Z',
  }
}

function entry(type: ActivityEntry['type'], fields: Partial<ActivityEntry> = {}): ActivityEntry {
  return { at: '2026-09-29T12:00:00Z', type, ...fields }
}

const ALL_TYPES = ['needs_input', 'done', 'working', 'tool_denied', 'progress', 'artifact', 'handoff', 'error', 'exit_nonzero', 'tool_use', 'file']

describe('DEFAULT_ROUTES', () => {
  it('has a row with all four routes for every event type', () => {
    expect(Object.keys(DEFAULT_ROUTES).sort()).toEqual([...ALL_TYPES].sort())
    expect([...EVENT_TYPES].sort()).toEqual([...ALL_TYPES].sort())
    for (const t of EVENT_TYPES) expect(Object.keys(DEFAULT_ROUTES[t]).sort()).toEqual(['badge', 'browser', 'feed', 'wall'])
  })

  it('mirrors the mockup matrix', () => {
    const on = (badge: boolean, browser: boolean, wall: boolean, feed: boolean) => ({ badge, browser, wall, feed })
    expect(DEFAULT_ROUTES.needs_input).toEqual(on(true, true, true, true))
    expect(DEFAULT_ROUTES.done).toEqual(on(true, false, false, true))
    expect(DEFAULT_ROUTES.working).toEqual(on(false, false, false, true))
    expect(DEFAULT_ROUTES.tool_denied).toEqual(on(true, true, false, true))
    expect(DEFAULT_ROUTES.progress).toEqual(on(false, false, false, true))
    expect(DEFAULT_ROUTES.artifact).toEqual(on(false, true, false, true))
    expect(DEFAULT_ROUTES.handoff).toEqual(on(false, false, true, true))
    expect(DEFAULT_ROUTES.error).toEqual(on(true, true, false, true))
    expect(DEFAULT_ROUTES.exit_nonzero).toEqual(on(true, true, false, true))
    expect(DEFAULT_ROUTES.tool_use).toEqual(on(false, false, false, true))
  })
})

describe('eventTypeOf', () => {
  it('maps an attention entry by the state of its session', () => {
    expect(eventTypeOf(entry('attention', { message: 'Claude needs your permission to use Bash' }), session('needs_input'))).toBe('needs_input')
    expect(eventTypeOf(entry('attention', { message: 'All tests pass' }), session('done', 'All tests pass'))).toBe('done')
  })

  it('reads the state from an attention entry recorded without a message', () => {
    // The session records the state itself when the report had no message.
    expect(eventTypeOf(entry('attention', { message: 'working' }))).toBe('working')
    expect(eventTypeOf(entry('file', { op: 'edit', path: 'internal/api/users.go', tool: 'Edit' }))).toBe('file')
    expect(eventTypeOf(entry('attention', { message: 'done' }), session(''))).toBe('done')
    expect(eventTypeOf(entry('attention', { message: 'needs_input' }), session('working', 'compiling'))).toBe('needs_input')
  })

  it('prefers the session when its attention is the one the entry records', () => {
    expect(eventTypeOf(entry('attention', { message: 'done' }), session('needs_input', 'done'))).toBe('needs_input')
  })

  it('gives no type to an attention entry it cannot place', () => {
    expect(eventTypeOf(entry('attention', { message: 'Waiting' }))).toBeNull()
    expect(eventTypeOf(entry('attention', { message: 'Waiting' }), session(''))).toBeNull()
  })

  it('maps an exit with a non-zero code to exit_nonzero, and nothing else of a status', () => {
    expect(eventTypeOf(entry('status', { message: 'exited (exit 1)' }))).toBe('exit_nonzero')
    expect(eventTypeOf(entry('status', { message: 'exited (exit -1)' }))).toBe('exit_nonzero')
    expect(eventTypeOf(entry('status', { message: 'exited (exit 0)' }))).toBeNull()
    expect(eventTypeOf(entry('status', { message: 'exited' }))).toBeNull()
    // An admin's Stop ends the process with a signal: not a failure of the agent.
    expect(eventTypeOf(entry('status', { message: 'stopped (exit 143)' }))).toBeNull()
    expect(eventTypeOf(entry('status', { message: 'running' }))).toBeNull()
  })

  it('maps the agent event types to themselves and the roster types to nothing', () => {
    for (const t of ['progress', 'artifact', 'handoff', 'tool_use', 'tool_denied', 'error'] as const) expect(eventTypeOf(entry(t))).toBe(t)
    for (const t of ['join', 'leave', 'input', 'link'] as const) expect(eventTypeOf(entry(t, { message: 'x' }))).toBeNull()
  })
})

describe('attentionSettled', () => {
  it('holds only an attention entry whose state the session does not show yet', () => {
    expect(attentionSettled(entry('progress', { message: '4/7' }))).toBe(true)
    expect(attentionSettled(entry('attention', { message: 'done' }))).toBe(true)
    expect(attentionSettled(entry('attention', { message: 'Approve?' }), session('needs_input', 'Approve?'))).toBe(true)
    expect(attentionSettled(entry('attention', { message: 'Approve?' }), session('working'))).toBe(false)
    expect(attentionSettled(entry('attention', { message: 'Approve?' }))).toBe(false)
  })
})

describe('an entry that carries its state', () => {
  it('is that state, whatever the session shows, and waits for nothing', () => {
    const e = entry('attention', { message: 'Allow Bash?', state: 'needs_input' })
    expect(eventTypeOf(e, session('working', 'compiling'))).toBe('needs_input')
    expect(eventTypeOf(e)).toBe('needs_input')
    expect(attentionSettled(e, session(''))).toBe(true)
  })
})

describe('linkableUrl', () => {
  it('links only http(s) URLs up to 2048 bytes', () => {
    expect(linkableUrl('javascript:alert(1)')).toBeNull()
    expect(linkableUrl('https://x')).toBe('https://x')
    expect(linkableUrl(`https://example.com/${'a'.repeat(2980)}`)).toBeNull()
    expect(linkableUrl('http://example.com/pr/1')).toBe('http://example.com/pr/1')
  })

  it('refuses other schemes, paths, blanks and whitespace', () => {
    for (const u of ['JavaScript:alert(1)', 'data:text/html,hi', 'file:///etc/passwd', 'ftp://x', '/srv/app/report.html', 'report.html', '', ' https://x', 'https://x y', 'https://x\n']) {
      expect(linkableUrl(u)).toBeNull()
    }
    expect(linkableUrl()).toBeNull()
  })

  it('counts bytes, not characters', () => {
    const base = 'https://example.com/'
    expect(linkableUrl(base + 'a'.repeat(2048 - base.length))).not.toBeNull()
    expect(linkableUrl(base + 'a'.repeat(2049 - base.length))).toBeNull()
    expect(linkableUrl(base + 'é'.repeat(1015))).toBeNull() // 1015 × 2 bytes + 20
  })
})

describe('parseRoutes', () => {
  it('falls back to the defaults for missing, broken or foreign data', () => {
    for (const raw of [null, '', 'not json', '[]', '3', 'null']) expect(parseRoutes(raw)).toEqual(DEFAULT_ROUTES)
    const r = parseRoutes(null)
    r.done.browser = true
    expect(DEFAULT_ROUTES.done.browser).toBe(false)
  })

  it('keeps stored booleans, ignores unknown keys and fills in the rest', () => {
    const r = parseRoutes(JSON.stringify({ done: { browser: true, feed: false, extra: true }, bogus: { badge: true }, working: 'x', tool_use: { badge: 'yes' } }))
    expect(r.done).toEqual({ badge: true, browser: true, wall: false, feed: false })
    expect(r.working).toEqual(DEFAULT_ROUTES.working)
    expect(r.tool_use).toEqual(DEFAULT_ROUTES.tool_use)
    expect(Object.keys(r).sort()).toEqual([...ALL_TYPES].sort())
  })
})

describe('appendRing', () => {
  it('keeps the newest entries up to the limit', () => {
    let ring: number[] = []
    for (let i = 1; i <= 7; i++) ring = appendRing(ring, i, 5)
    expect(ring).toEqual([3, 4, 5, 6, 7])
  })
})

describe('markOf', () => {
  it('labels a session badge and colours it by severity', () => {
    expect(markOf('done', entry('attention', { message: 'done' }))).toMatchObject({ label: 'finished', color: 'neutral' })
    expect(markOf('tool_denied', entry('tool_denied', { tool: 'Bash' }))).toMatchObject({ label: 'denied', color: 'error' })
    expect(markOf('error', entry('error', { message: 'boom' }))).toMatchObject({ label: 'error', color: 'error' })
    expect(markOf('exit_nonzero', entry('status', { message: 'exited (exit 1)' }))).toMatchObject({ label: 'exit 1', color: 'error' })
    expect(markOf('artifact', entry('artifact', { url: 'https://x' }))).toMatchObject({ label: 'artifact', color: 'info' })
  })
})

describe('eventDetail', () => {
  it('says what an entry adds to its type, leaving an artifact URL to its link', () => {
    expect(eventDetail(entry('handoff', { to: 'tests', message: '/v1/users done' }))).toBe('to tests: /v1/users done')
    expect(eventDetail(entry('tool_denied', { tool: 'Bash', message: 'rm -rf /' }))).toBe('Bash: rm -rf /')
    expect(eventDetail(entry('artifact', { url: 'https://x', message: 'PR 212' }))).toBe('PR 212')
    expect(eventDetail(entry('artifact', { url: 'https://x' }))).toBe('')
    expect(eventDetail(entry('attention', { message: 'needs_input' }))).toBe('')
    expect(eventDetail(entry('attention', { message: 'Approve the edit?' }))).toBe('Approve the edit?')
    expect(eventDetail(entry('file', { op: 'write', path: 'README.md', tool: 'Write' }))).toBe('wrote README.md · Write')
    // A file the session found changed in git, with no hook naming it (round 13, G2a).
    expect(eventDetail(entry('file', { op: 'write', path: 'gen.txt', tool: 'git' }))).toBe('wrote gen.txt · seen by git')
  })
})

describe('entryIcon', () => {
  it('gives events their type\'s icon and colour and the roster rows their own', () => {
    expect(entryIcon(entry('artifact', { url: 'https://x' }))).toEqual({ icon: EVENT_INFO.artifact.icon, color: 'info' })
    expect(entryIcon(entry('tool_denied'))).toEqual({ icon: EVENT_INFO.tool_denied.icon, color: 'error' })
    expect(entryIcon(entry('status', { message: 'exited (exit 2)' }))).toEqual({ icon: EVENT_INFO.exit_nonzero.icon, color: 'error' })
    expect(entryIcon(entry('attention', { message: 'done' }))).toEqual({ icon: EVENT_INFO.done.icon, color: 'neutral' })
    expect(entryIcon(entry('status', { message: 'exited (exit 0)' })).color).toBe('neutral')
    expect(entryIcon(entry('attention', { message: 'Approve the edit?' })).icon).toBe('i-lucide-bell')
    const roster = ['join', 'leave', 'input', 'link'].map((t) => entryIcon(entry(t as ActivityEntry['type'])).icon)
    expect(new Set(roster).size).toBe(4)
  })
})

describe('EntryHold', () => {
  let sessions: Map<string, SessionInfo>
  let released: string[]
  let hold: EntryHold

  beforeEach(() => {
    vi.useFakeTimers()
    sessions = new Map([['s1', session('working')], ['s2', session('')]])
    released = []
    hold = new EntryHold(
      (id) => sessions.get(id),
      (id, e) => released.push(`${id}:${e.type}:${e.message ?? ''}`),
      2000,
    )
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('passes an entry it can type now straight through', () => {
    hold.push('s1', entry('progress', { message: '4/7' }))
    hold.push('s1', entry('attention', { message: 'done' }))
    expect(released).toEqual(['s1:progress:4/7', 's1:attention:done'])
  })

  it('holds an attention entry until its session shows it, and later entries of that session behind it, in order', () => {
    hold.push('s1', entry('attention', { message: 'Approve?' }))
    hold.push('s1', entry('tool_use', { message: 'Bash' }))
    hold.push('s2', entry('progress', { message: 'other session' }))
    expect(released).toEqual(['s2:progress:other session'])
    hold.settle('s1') // a change that does not carry it yet
    expect(released).toHaveLength(1)
    sessions.set('s1', session('needs_input', 'Approve?'))
    hold.settle('s1')
    expect(released).toEqual(['s2:progress:other session', 's1:attention:Approve?', 's1:tool_use:Bash'])
  })

  it('settles every session at once after a snapshot', () => {
    hold.push('s1', entry('attention', { message: 'Approve?' }))
    hold.push('s2', entry('attention', { message: 'Pick one' }))
    sessions.set('s1', session('needs_input', 'Approve?'))
    sessions.set('s2', session('needs_input', 'Pick one'))
    hold.settle()
    expect(released.sort()).toEqual(['s1:attention:Approve?', 's2:attention:Pick one'])
  })

  it('lets a held entry go after two seconds with whatever the session shows', () => {
    hold.push('s1', entry('attention', { message: 'Approve?' }))
    vi.advanceTimersByTime(1999)
    expect(released).toEqual([])
    vi.advanceTimersByTime(1)
    expect(released).toEqual(['s1:attention:Approve?'])
  })

  it('bounds each entry by its own arrival, not by the one ahead of it', () => {
    hold.push('s1', entry('attention', { message: 'first' }))
    vi.advanceTimersByTime(1500)
    hold.push('s1', entry('attention', { message: 'second' }))
    vi.advanceTimersByTime(500)
    expect(released).toEqual(['s1:attention:first'])
    vi.advanceTimersByTime(1500)
    expect(released).toEqual(['s1:attention:first', 's1:attention:second'])
  })

  it('releases what a removed session held at once', () => {
    hold.push('s1', entry('attention', { message: 'Approve?' }))
    hold.push('s1', entry('progress', { message: 'x' }))
    hold.forget('s1')
    expect(released).toEqual(['s1:attention:Approve?', 's1:progress:x'])
    vi.advanceTimersByTime(5000)
    expect(released).toHaveLength(2)
  })
})

describe('EntryHold.retain and clear', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('releases what sessions outside a snapshot held and keeps the rest', () => {
    const released: string[] = []
    const hold = new EntryHold(() => undefined, (id, e) => released.push(`${id}:${e.message}`), 2000)
    hold.push('gone', entry('attention', { message: 'Approve?' }))
    hold.push('kept', entry('attention', { message: 'Pick one' }))
    hold.retain((id) => id === 'kept')
    expect(released).toEqual(['gone:Approve?'])
    vi.advanceTimersByTime(2000)
    expect(released).toEqual(['gone:Approve?', 'kept:Pick one'])
  })

  it('drops everything it holds, timers included, on clear', () => {
    const released: string[] = []
    const hold = new EntryHold(() => undefined, (id) => released.push(id), 2000)
    hold.push('a', entry('attention', { message: 'Approve?' }))
    hold.push('a', entry('progress', { message: 'x' }))
    hold.clear()
    vi.advanceTimersByTime(5000)
    expect(released).toEqual([])
    hold.push('a', entry('progress', { message: 'y' }))
    expect(released).toEqual(['a'])
  })
})

function routes(changes: Partial<Record<EventType, Partial<RouteRow>>> = {}): Record<EventType, RouteRow> {
  const r = parseRoutes(null)
  for (const [t, row] of Object.entries(changes)) r[t as EventType] = { ...r[t as EventType], ...row }
  return r
}

const empty: EventState = { feed: [], marks: {} }

describe('routeEntry', () => {
  it('adds a feed line with its time, detail and link computed once', () => {
    const { state, type } = routeEntry(empty, 's1', entry('artifact', { url: 'https://x/pr/1', message: 'PR 1' }), session(''), routes(), 7)
    expect(type).toBe('artifact')
    expect(state.feed).toHaveLength(1)
    expect(state.feed[0]).toMatchObject({ sessionId: 's1', event: 'artifact', sessionName: 'api-sweep', seq: 7, detail: 'PR 1', link: 'https://x/pr/1', time: feedTime('2026-09-29T12:00:00Z') })
    const bad = routeEntry(empty, 's1', entry('artifact', { url: 'javascript:alert(1)' }), undefined, routes(), 8)
    expect(bad.state.feed[0]).toMatchObject({ link: null, sessionName: 's1' })
  })

  it('keeps a type out of the feed when its Feed route is off, and caps the ring', () => {
    const off = routeEntry(empty, 's1', entry('tool_use', { tool: 'Bash' }), undefined, routes({ tool_use: { feed: false } }), 1)
    expect(off.type).toBe('tool_use')
    expect(off.state.feed).toBe(empty.feed)
    let state = empty
    for (let i = 0; i < 5; i++) state = routeEntry(state, 's1', entry('progress', { message: String(i) }), undefined, routes(), i, 3).state
    expect(state.feed.map((e) => e.message)).toEqual(['2', '3', '4'])
  })

  it('sets the session badge for a type routed to Badge and leaves it alone otherwise', () => {
    const denied = routeEntry(empty, 's1', entry('tool_denied', { tool: 'Bash' }), undefined, routes(), 1).state
    expect(denied.marks.s1).toMatchObject({ type: 'tool_denied', label: 'denied', color: 'error' })
    const progress = routeEntry(denied, 's1', entry('progress', { message: '4/7' }), undefined, routes(), 2).state
    expect(progress.marks).toBe(denied.marks)
    const done = routeEntry(progress, 's1', entry('attention', { message: 'done' }), undefined, routes(), 3).state
    expect(done.marks.s1).toMatchObject({ type: 'done', label: 'finished', color: 'neutral' })
  })

  it('clears the badge on working and needs_input, whatever their Badge route says', () => {
    const marked = routeEntry(empty, 's1', entry('error', { message: 'boom' }), undefined, routes(), 1).state
    const working = routeEntry(marked, 's1', entry('attention', { message: 'working' }), undefined, routes({ working: { badge: true } }), 2).state
    expect(working.marks).toEqual({})
    const again = routeEntry(marked, 's1', entry('attention', { message: 'needs_input' }), undefined, routes(), 3).state
    expect(again.marks).toEqual({})
  })

  it('hears no late entry as a live event', () => {
    expect(liveEvent(entry('attention', { message: 'needs_input', state: 'needs_input', late: true }))).toBe(false)
    expect(liveEvent(entry('attention', { message: 'needs_input', state: 'needs_input' }))).toBe(true)
  })

  it('shows a late entry in the feed alone: it sets and clears no badge', () => {
    const marked = routeEntry(empty, 's1', entry('error', { message: 'boom' }), undefined, routes(), 1).state
    const late = routeEntry(marked, 's1', entry('attention', { message: 'needs_input', late: true }), undefined, routes(), 2)
    expect(late.type).toBe('needs_input')
    expect(late.state.feed.map((e) => e.event)).toEqual(['error', 'needs_input'])
    expect(late.state.marks).toBe(marked.marks)
    const denied = routeEntry(empty, 's1', entry('tool_denied', { tool: 'Bash', late: true }), undefined, routes(), 3).state
    expect(denied.marks).toEqual({})
    expect(denied.feed).toHaveLength(1)
  })

  it('does nothing for an entry that is not an event', () => {
    const r = routeEntry(empty, 's1', entry('join', { byName: 'Priya' }), undefined, routes(), 1)
    expect(r.type).toBeNull()
    expect(r.state).toBe(empty)
  })
})

describe('pruneMarks', () => {
  const marks = {
    a: { type: 'done' as const, at: 't', label: 'finished', color: 'neutral' as const, detail: '' },
    b: { type: 'error' as const, at: 't', label: 'error', color: 'error' as const, detail: '' },
  }
  it('drops the badges of a type whose Badge route is off', () => {
    expect(Object.keys(pruneMarks(marks, routes({ done: { badge: false } })))).toEqual(['b'])
  })
  it('drops the badges of sessions that are gone', () => {
    expect(Object.keys(pruneMarks(marks, routes(), (id) => id === 'a'))).toEqual(['a'])
  })
  it('returns the same object when nothing goes', () => {
    expect(pruneMarks(marks, routes())).toBe(marks)
  })
})

describe('followJump', () => {
  const ctx = { follow: true, routes: routes(), typing: false, holding: false, activeIds: ['a', 'b', 'c'], selected: 0 }
  it('moves the Roundhouse to the session of an event routed to Roundhouse jump', () => {
    expect(followJump({ type: 'handoff', sessionId: 'c' }, ctx)).toBe(2)
  })
  it('stays put while it holds on a session that needs input', () => {
    expect(followJump({ type: 'handoff', sessionId: 'c' }, { ...ctx, holding: true })).toBeNull()
  })
  it('stays put while someone types, with follow off, or for a type not routed to Roundhouse jump', () => {
    expect(followJump({ type: 'handoff', sessionId: 'c' }, { ...ctx, typing: true })).toBeNull()
    expect(followJump({ type: 'handoff', sessionId: 'c' }, { ...ctx, follow: false })).toBeNull()
    expect(followJump({ type: 'progress', sessionId: 'c' }, ctx)).toBeNull()
    expect(followJump({ type: 'progress', sessionId: 'c' }, { ...ctx, routes: routes({ progress: { wall: true } }) })).toBe(2)
  })
  it('leaves needs_input to the state-driven jump and ignores sessions it does not show', () => {
    expect(followJump({ type: 'needs_input', sessionId: 'c' }, ctx)).toBeNull()
    expect(followJump({ type: 'handoff', sessionId: 'zz' }, ctx)).toBeNull()
    expect(followJump({ type: 'handoff', sessionId: 'a' }, ctx)).toBeNull()
  })
})

describe('eventAlert', () => {
  const s = session('')
  it('alerts for a type routed to This device, with a readable title and the detail', () => {
    expect(eventAlert({ sessionId: 's1', session: s, type: 'artifact', entry: entry('artifact', { url: 'https://x/pr/1' }) }, routes())).toEqual({
      title: 'api-sweep: artifact',
      body: 'https://x/pr/1',
      tag: 'conductor-s1-artifact',
    })
    expect(eventAlert({ sessionId: 's1', type: 'tool_denied', entry: entry('tool_denied', { tool: 'Bash', message: 'rm -rf /' }) }, routes())).toMatchObject({ title: 's1: tool denied', body: 'Bash: rm -rf /' })
    expect(eventAlert({ sessionId: 's1', session: s, type: 'exit_nonzero', entry: entry('status', { message: 'exited (exit 3)' }) }, routes())).toMatchObject({ title: 'api-sweep: exit 3' })
    expect(eventAlert({ sessionId: 's1', session: s, type: 'done', entry: entry('attention', { message: 'done' }) }, routes({ done: { browser: true } }))).toMatchObject({ title: 'api-sweep: done', body: EVENT_INFO.done.source })
  })
  it('stays quiet for a type not routed to This device and for needs_input, which the session state alerts', () => {
    expect(eventAlert({ sessionId: 's1', type: 'progress', entry: entry('progress') }, routes())).toBeNull()
    expect(eventAlert({ sessionId: 's1', type: 'artifact', entry: entry('artifact') }, routes({ artifact: { browser: false } }))).toBeNull()
    expect(eventAlert({ sessionId: 's1', type: 'needs_input', entry: entry('attention', { message: 'needs_input' }) }, routes())).toBeNull()
  })
})

describe('feedTime', () => {
  it('formats a time of day with seconds, and nothing for a bad time', () => {
    expect(feedTime('2026-09-29T12:00:05Z')).toMatch(/^\d\d:\d\d:\d\d$/)
    expect(feedTime('not a time')).toBe('')
  })
})

describe('webhookHosts', () => {
  const hooks = [
    { url: 'https://hooks.example.com/conductor', events: ['needs_input', 'exit_nonzero', 'progress'] },
    { url: 'http://10.0.0.7:9000/in', events: ['attention', 'status', 'progress'] },
    { url: 'https://hooks.example.com/other', events: ['progress', 'join'] },
  ]

  it('names the hosts of the webhooks that get a type, once each, in order', () => {
    expect(webhookHosts(hooks, 'progress')).toEqual(['10.0.0.7:9000', 'hooks.example.com'])
    expect(webhookHosts(hooks, 'artifact')).toEqual([])
    expect(webhookHosts(undefined, 'progress')).toEqual([])
    expect(webhookHosts([], 'progress')).toEqual([])
  })

  it('counts a webhook that lists attention for each attention state, and one that lists status for exit_nonzero', () => {
    expect(webhookHosts(hooks, 'needs_input')).toEqual(['10.0.0.7:9000', 'hooks.example.com'])
    expect(webhookHosts(hooks, 'done')).toEqual(['10.0.0.7:9000'])
    expect(webhookHosts(hooks, 'working')).toEqual(['10.0.0.7:9000'])
    expect(webhookHosts(hooks, 'exit_nonzero')).toEqual(['10.0.0.7:9000', 'hooks.example.com'])
    expect(webhookHosts([{ url: 'https://a.example', events: ['status'] }], 'error')).toEqual([])
  })

  it('shows a URL it cannot read as it is', () => {
    expect(webhookHosts([{ url: 'not a url', events: ['error'] }], 'error')).toEqual(['not a url'])
  })
})

describe('a note that the session is not shared', () => {
  it('is no event: no badge, no alert (round 14: a refused publication was an "error" on the agent)', () => {
    for (const message of ['not shared through the switchyard yet: the switchyard refused it (429 open_host_limit): …; trying again', 'not shared through the switchyard: dial tcp: connection refused', 'shared through the switchyard after 3 tries']) {
      expect(eventTypeOf({ at: '2026-10-09T12:00:00Z', type: 'status', message })).toBe(null)
    }
  })
})

describe('eventsRedirect', () => {
  it('lands every old link to the Events page where its tab lives now', () => {
    expect(eventsRedirect({})).toEqual({ path: '/history', query: { view: 'feed' } })
    expect(eventsRedirect({ tab: 'feed' })).toEqual({ path: '/history', query: { view: 'feed' } })
    expect(eventsRedirect({ tab: 'routing' })).toEqual({ path: '/history', query: { view: 'feed', alerts: null } })
    expect(eventsRedirect({ tab: 'integrations' })).toEqual({ path: '/agents', query: {}, hash: '#reporting' })
    expect(eventsRedirect({ tab: 'nonsense' })).toEqual({ path: '/history', query: { view: 'feed' } })
    expect(eventsRedirect({ tab: ['routing', 'feed'] })).toEqual({ path: '/history', query: { view: 'feed' } })
  })
})
