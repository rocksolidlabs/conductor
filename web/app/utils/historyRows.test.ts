import { describe, expect, it } from 'vitest'
import type { HistoryItem } from './history'
import { agentChips, byDay, clockTime, cutMiddle, dayWords, localDay, rowAction, rowDetails, rowStatus } from './historyRows'

function entry(p: Partial<HistoryItem> = {}): HistoryItem {
  return {
    key: 'session:43700eeee2a7ce64',
    kind: 'session',
    class: 'record',
    outcome: 'stopped (exit 143)',
    title: 'backfill-and-reconciliation-worker-01 · reconcile #3',
    at: '2026-11-18T09:41:00Z',
    source: { store: 'journal', label: 'Claude Code (worker) session', ref: 'session:43700eeee2a7ce64' },
    host: { name: 'lab-box', user: 'nate', where: 'native' },
    project: { repo: '/home/nate/code/ledger-service/.git', tree: '/home/nate/code/ledger-service/.conductor/worktrees/reconcile-6b7eefa5/reconcile.3' },
    revision: { label: 'crew/reconcile-the-payments-ledger-after-the-6b7eefa5/reconcile.3', commit: 'abc' },
    agent: { id: 'worker-cli', name: 'Claude Code (worker)' },
    anchor: { ref: 'session:43700eeee2a7ce64', label: 'session 43700eeee2a7ce64', url: '/history/sessions/43700eeee2a7ce64' },
    url: '/history/sessions/43700eeee2a7ce64',
    repeats: { count: 1, anchors: 1 },
    links: [],
    availability: { state: 'record', words: 'record only' },
    ...p,
  }
}

describe("a row's one line of details (#1024)", () => {
  it('says agent · repository · branch once each, the branch cut in its middle, the id after them and the path only for the title', () => {
    const d = rowDetails(entry())
    expect(d.parts.map((p) => p.text)).toEqual(['Claude Code (worker)', 'ledger-service', 'crew/reconcile-t…fa5/reconcile.3'])
    expect(d.parts[2]!.title).toBe('crew/reconcile-the-payments-ledger-after-the-6b7eefa5/reconcile.3')
    expect(d.id).toBe('43700eeee2a7ce64')
    expect(d.path).toBe('/home/nate/code/ledger-service/.conductor/worktrees/reconcile-6b7eefa5/reconcile.3')
    // No state, no time, no storage words, no host of this machine, no path in the line.
    const line = d.parts.map((p) => p.text).join(' · ')
    for (const w of ['stopped', 'record only', 'lab-box', '/home/', 'ago', 'session']) expect(line).not.toContain(w)
  })

  it("names a hosted session's host, a Dispatch project by its key, and an entry that is not a session's by where it was said", () => {
    const hosted = rowDetails(entry({ host: { name: 'mac', user: 'nate', hosted: true }, project: { key: 'LED' }, revision: { label: 'main' } }))
    expect(hosted.parts.map((p) => p.text)).toEqual(['Claude Code (worker)', 'nate@mac via conductor host', 'LED', 'main'])
    const chat = rowDetails(entry({ key: 'session:43700eeee2a7ce64#e3', kind: 'session.chat', source: { store: 'journal', label: 'Claude Code (worker) session', ref: 'x' } }))
    expect(chat.parts[0]!.text).toBe('Claude Code (worker) session')
    expect(chat.id).toBe('')
  })

  it('keeps a short name whole and cuts a long one in its middle', () => {
    expect(cutMiddle('main')).toBe('main')
    expect(cutMiddle('a'.repeat(10) + 'b'.repeat(30), 11)).toBe('aaaaa…bbbbb')
    expect(cutMiddle('x'.repeat(40)).length).toBe(32)
  })
})

describe("a session row's one state word, in the app's status language (#1024)", () => {
  const word = (p: Partial<HistoryItem>, listed?: Parameters<typeof rowStatus>[1]) => {
    const s = rowStatus(entry(p), listed, Date.parse('2026-11-18T10:00:00Z'))
    return s ? `${s.key}:${s.word}` : null
  }
  it("says what the session page and the sidebar say: the listed session's own state where the page holds it", () => {
    expect(word({ outcome: 'running', availability: { state: 'live', words: 'live' } }, { status: 'running', attention: { state: 'working', since: '2026-11-18T09:59:00Z', source: 'hook' } as never, turns: true })).toBe('working:working')
    expect(word({ outcome: 'running', availability: { state: 'live', words: 'live' } }, { status: 'running', attention: { state: 'needs_input', since: '2026-11-18T09:59:00Z', source: 'hook' } as never })).toBe('needs:needs you')
    expect(word({}, { status: 'exited', exitCode: 2 })).toBe('exited:exit 2')
  })
  it('else what its record kept: exited, exit N, host away, running while live, not observed', () => {
    expect(word({ outcome: 'exited (exit 0)' })).toBe('exited:exited')
    expect(word({ outcome: 'exited (exit 3)' })).toBe('exited:exit 3')
    // A stopped session was ended by a person: exited, whatever its code (sessionStatus's rule).
    expect(word({ outcome: 'stopped (exit 143)' })).toBe('exited:exited')
    expect(word({ outcome: 'stopped' })).toBe('exited:exited')
    expect(word({ outcome: 'host away', availability: { state: 'host_away', words: '' } })).toBe('exited:host away')
    expect(word({ outcome: 'running', availability: { state: 'live', words: 'live' } })).toBe('running:running')
    expect(word({ outcome: 'lost', availability: { state: 'lost', words: '' } })).toBe('exited:not observed')
  })
  it("draws none for an entry that is not a session's own", () => {
    expect(word({ kind: 'session.chat', class: 'said' })).toBe(null)
    expect(word({ kind: 'check', class: 'checked', outcome: 'failed' })).toBe(null)
  })
})

describe("a row's action, written out (#1024)", () => {
  it('Open for a session still live, Transcript for one that ended, Open for every other entry; to the page the server names', () => {
    expect(rowAction(entry({ availability: { state: 'live', words: 'live' }, url: '/sessions/43700eeee2a7ce64' }))).toEqual({ label: 'Open', to: '/sessions/43700eeee2a7ce64' })
    expect(rowAction(entry())).toEqual({ label: 'Transcript', to: '/history/sessions/43700eeee2a7ce64' })
    // The listed session's own status decides where the page holds it.
    expect(rowAction(entry({ availability: { state: 'live', words: 'live' }, url: '/sessions/x' }), { status: 'stopped' })!.label).toBe('Transcript')
    expect(rowAction(entry({ kind: 'check', class: 'checked', url: '/runs/r?task=t' }))).toEqual({ label: 'Open', to: '/runs/r?task=t' })
    expect(rowAction(entry({ url: '/api/raw', anchor: { ref: 'x', label: 'x' } }))).toBe(null)
  })
})

describe('the rows under day headings, by the local day (#1024)', () => {
  const now = new Date(2026, 10, 18, 15, 0).getTime() // Wed 18 Nov 2026, 15:00 local
  const at = (d: number, h: number) => new Date(2026, 10, d, h, 30).toISOString()
  it('says Today, Yesterday, then the weekday, day and month, with the year when it is not this one', () => {
    expect(dayWords(localDay(now), now)).toBe('Today')
    expect(dayWords('2026-11-17', now)).toBe('Yesterday')
    expect(dayWords('2026-11-16', now)).toBe('Mon 16 Nov')
    expect(dayWords('2025-12-31', now)).toBe('Wed 31 Dec 2025')
  })
  it('groups by the local day, the days newest first, each day in the answer’s order, a key twice kept once, the undated last', () => {
    const e = (key: string, a: string) => ({ key, at: a })
    const groups = byDay([e('a', at(16, 9)), e('b', at(18, 8)), e('c', at(16, 23)), e('d', 'not a time'), e('b', at(18, 8)), e('f', at(17, 0)), e('g', at(18, 14))], now)
    expect(groups.map((g) => [g.label, g.entries.map((x) => x.key)])).toEqual([
      ['Today', ['b', 'g']],
      ['Yesterday', ['f']],
      ['Mon 16 Nov', ['a', 'c']],
      ['Undated', ['d']],
    ])
  })
  it("gives a row its local clock time", () => {
    expect(clockTime(new Date(2026, 10, 18, 9, 5).toISOString())).toBe('09:05')
    expect(clockTime('nope')).toBe('')
  })
})

describe("the Agent filter's order (#1024)", () => {
  const agents = [
    { id: 'aider', name: 'aider' },
    { id: 'claude', name: 'Claude Code' },
    { id: 'codex', name: 'Codex CLI' },
    { id: 'goose', name: 'Goose' },
  ]
  it('puts the agents with history first, the most used first, the rest behind More', () => {
    const c = agentChips(agents, { codex: 2, claude: 7, gone: 1 })
    expect(c.shown.map((x) => x.label)).toEqual(['Claude Code', 'Codex CLI', 'gone'])
    expect(c.more.map((x) => x.value)).toEqual(['aider', 'goose'])
  })
  it('breaks a tie by name, and never hides a pressed agent behind More', () => {
    expect(agentChips(agents, { codex: 1, claude: 1 }).shown.map((x) => x.value)).toEqual(['claude', 'codex'])
    const c = agentChips(agents, { claude: 1 }, ['goose'])
    expect(c.shown.map((x) => x.value)).toEqual(['claude', 'goose'])
    expect(c.more.map((x) => x.value)).toEqual(['aider', 'codex'])
  })
  it('with no history yet, every agent is behind More', () => {
    expect(agentChips(agents, {}).shown).toEqual([])
    expect(agentChips(agents, {}).more).toHaveLength(4)
  })
})
