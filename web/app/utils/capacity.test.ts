import { describe, expect, it } from 'vitest'
import {
  WARN_PERCENT,
  accountChoices,
  asCapacityEvent,
  capacityHostWords,
  capacityKey,
  capacityParams,
  capacityStateWords,
  clockWords,
  countsWords,
  creditWords,
  defaultChoice,
  figureWords,
  freshnessWords,
  inWords,
  limitsWords,
  modelProblem,
  modelWords,
  offersCheck,
  offersRead,
  planWords,
  readAgoWords,
  readWait,
  severityColor,
  warns,
  windowWords,
  type CapacityState,
  type CapacityView,
} from './capacity'

// Capacity's words (docs/plans/capacity.md, PR-5): the panel says what the server's view holds, in a fixed zone here so the resets
// read the same on every machine; a window the provider did not report is never drawn, and Conductor's estimate stays apart.

const NOW = Date.parse('2026-11-12T10:00:00Z')
const ZONE = 'UTC'

function view(over: Partial<CapacityView> = {}): CapacityView {
  return {
    agent: { id: 'claude', name: 'Claude Code', adapter: 'claude', version: '2.1.322' },
    model: { chosen: '' },
    account: { id: 'default' },
    host: { kind: 'native', name: 'box' },
    state: 'ok',
    windows: [],
    credits: [],
    limits: { source: 'provider' },
    counts: { source: 'conductor', estimate: true, running: 0, started5h: 0, started7d: 0 },
    fresh: 'fresh',
    ...over,
  }
}

describe('a window in words', () => {
  it('names the window, the share used, its reset in the zone and how far off it is', () => {
    const w = { source: 'provider' as const, kind: 'weekly_all', name: 'Weekly, all models', percent: 40, resetsAt: '2026-11-17T13:30:00Z', severity: 'normal' as const }
    expect(windowWords(w, NOW, ZONE)).toBe('Weekly, all models: 40% used, resets Tue 17 Nov 13:30, in 5 days')
    // Another zone, the same moment: the person's own clock.
    expect(windowWords(w, NOW, 'Asia/Tokyo')).toBe('Weekly, all models: 40% used, resets Tue 17 Nov 22:30, in 5 days')
  })

  it('says a reset the provider gave none for, never inventing one', () => {
    expect(windowWords({ source: 'provider', kind: 'primary', name: 'Weekly', percent: 0 }, NOW, ZONE)).toBe('Weekly: 0% used, no reset reported')
    expect(windowWords({ source: 'provider', kind: 'primary', name: 'Weekly', percent: 3, resetsAt: 'not a time' }, NOW, ZONE)).toBe('Weekly: 3% used, no reset reported')
  })

  it('counts down in days, hours and minutes, and a time behind the clock has passed', () => {
    expect(inWords(NOW + 86_400_000, NOW)).toBe('in 1 day')
    expect(inWords(NOW + 3 * 3_600_000 + 1, NOW)).toBe('in 3 hours')
    expect(inWords(NOW + 60_000, NOW)).toBe('in 1 minute')
    expect(inWords(NOW + 59_000, NOW)).toBe('in under a minute')
    expect(inWords(NOW, NOW)).toBe('has passed')
    expect(clockWords(Date.parse('2026-11-11T03:00:00Z'), ZONE)).toBe('Wed 11 Nov 03:00')
  })

  it("colours a window by the provider's severity, from the brand palette's names", () => {
    expect(severityColor({ severity: 'normal' })).toBe('primary')
    expect(severityColor({ severity: 'warning' })).toBe('warning')
    expect(severityColor({ severity: 'critical' })).toBe('error')
    expect(severityColor({})).toBe('neutral')
  })

  it('warns at warning or critical, or at 90% used, whatever the severity says', () => {
    const w = (percent: number, severity?: 'normal' | 'warning' | 'critical') => ({ source: 'provider' as const, kind: 'session', name: '5-hour', percent, severity })
    expect(warns(view({ windows: [w(40, 'normal')] }))).toBe(false)
    expect(warns(view({ windows: [w(10, 'warning')] }))).toBe(true)
    expect(warns(view({ windows: [w(10, 'critical')] }))).toBe(true)
    expect(warns(view({ windows: [w(WARN_PERCENT, 'normal')] }))).toBe(true)
    expect(warns(view({ windows: [w(WARN_PERCENT - 1)] }))).toBe(false)
    // No window reported: nothing to warn of, and nothing drawn as empty.
    expect(warns(view({ windows: [] }))).toBe(false)
  })
})

describe("the provider's figures and Conductor's estimate", () => {
  it("says the provider's credit figures as it wrote them, never summed", () => {
    expect(creditWords({ source: 'provider', kind: 'extra_usage', enabled: false, used: '0', limit: '0', currency: 'USD' })).toBe('Extra usage: off')
    expect(creditWords({ source: 'provider', kind: 'extra_usage', enabled: true, used: '12.5', limit: '1000', currency: 'USD' })).toBe('Extra usage: on, 12.5 USD of 1,000 USD used')
    expect(creditWords({ source: 'provider', kind: 'credits', enabled: true, balance: '1250' })).toBe('Credits: 1,250')
    expect(creditWords({ source: 'provider', kind: 'credits', unlimited: true })).toBe('Credits: unlimited')
    expect(creditWords({ source: 'provider', kind: 'credits' })).toBe('Credits: not reported')
    expect(figureWords('1234567.89')).toBe('1,234,567.89')
    expect(figureWords('about 3')).toBe('about 3')
  })

  it('says each flag the provider reported and none it did not', () => {
    expect(limitsWords(undefined)).toEqual([])
    expect(limitsWords({ source: 'provider' })).toEqual([])
    expect(limitsWords({ source: 'provider', reached: 'rate_limit_reached', spendControl: true, ordinaryAllowed: false })).toEqual(['Limit reached: rate limit reached', 'Spend control reached', 'Ordinary use not allowed now'])
  })

  it("keeps Conductor's count in its own words, an estimate of this server's", () => {
    expect(countsWords({ source: 'conductor', estimate: true, running: 2, started5h: 5, started7d: 9 })).toBe(
      '2 sessions running on this account here, 5 started in the last 5 hours, 9 in the last 7 days, counted by this server since it started',
    )
    expect(countsWords({ source: 'conductor', estimate: true, running: 1, started5h: 1, started7d: 1 })).toMatch(/^1 session running/)
    expect(countsWords(undefined)).toBe('')
  })
})

describe('freshness and the states', () => {
  it('says when it was read and by what, stale, a reset passed, or a read under way', () => {
    const read = { readAt: new Date(NOW - 3 * 60_000).toISOString(), readBy: { program: 'Claude Code', version: '2.1.322', qualified: '2.1.322' } }
    expect(freshnessWords(view(read), NOW)).toBe('Read 3 minutes ago by Claude Code 2.1.322')
    expect(freshnessWords(view({ ...read, readBy: { program: 'Codex', version: '0.164.0', qualified: '0.163.0' } }), NOW)).toBe('Read 3 minutes ago by Codex 0.164.0, checked on 0.163.0')
    expect(freshnessWords(view({ ...read, readAt: new Date(NOW - 40 * 60_000).toISOString(), fresh: 'stale' }), NOW)).toBe('Stale: read 40 minutes ago by Claude Code 2.1.322')
    expect(freshnessWords(view({ ...read, fresh: 'reset_passed' }), NOW)).toBe('A reset has passed since this reading (read 3 minutes ago)')
    expect(freshnessWords(view({ state: 'reading', readAt: read.readAt }), NOW)).toBe('Reading…')
    expect(freshnessWords(view({ state: 'never_read', fresh: 'none' }), NOW)).toBe('')
    expect(readAgoWords(NOW - 2 * 86_400_000, NOW)).toBe('2 days ago')
    expect(readAgoWords(NOW, NOW)).toBe('just now')
  })

  it('gives every state a title and words, the server’s words first, and says an unknown state is unknown', () => {
    const states: CapacityState[] = ['ok', 'reading', 'never_read', 'signed_out', 'key_paid', 'unqualified', 'error', 'unsupported', 'not_installed', 'remote', 'env_set', 'unlisted', 'not_owner']
    for (const s of states) {
      const w = capacityStateWords({ state: s })
      expect(w.words, s).not.toBe('')
      if (s !== 'ok') expect(w.title, s).not.toBe('')
    }
    expect(capacityStateWords({ state: 'unsupported' }).words).toBe("Conductor knows no way to read this agent's usage")
    expect(capacityStateWords({ state: 'error', words: 'the check of 2.1.330 ended without an answer: check it again' })).toEqual({ title: 'Could not read', words: 'the check of 2.1.330 ended without an answer: check it again' })
    expect(capacityStateWords({ state: 'later_state' })).toEqual({ title: 'Usage unknown', words: 'a state this page does not know (later_state)' })
  })

  it('says where it runs, the model and the plan as the tool reported it', () => {
    expect(capacityHostWords({ kind: 'native', name: 'box' })).toBe('Runs on this server, box')
    expect(capacityHostWords({ kind: 'remote' })).toBe('On this computer: your own login there, usage unknown here')
    expect(capacityHostWords({ kind: 'undetermined', words: "this server's login: if boxcar takes the run, its usage there is unknown" })).toMatch(/^this server's login/)
    expect(modelWords({ chosen: '' })).toBe("the agent's default")
    expect(modelWords({ chosen: 'opus' })).toBe('opus')
    expect(planWords('max', 'claude')).toBe('Claude Max')
    expect(planWords('pro', 'codex')).toBe('ChatGPT Pro')
    expect(planWords('', 'claude')).toBe('')
  })

  it('offers a check for a Claude Code version not checked alone, and a read only where the server reads', () => {
    expect(offersCheck(view({ state: 'unqualified' }))).toBe(true)
    expect(offersCheck(view({ state: 'unqualified', agent: { id: 'codex', adapter: 'codex' } }))).toBe(false)
    expect(offersCheck(view({ state: 'ok' }))).toBe(false)
    for (const s of ['reading', 'unsupported', 'not_installed', 'remote', 'env_set', 'unlisted', 'not_owner']) expect(offersRead(view({ state: s })), s).toBe(false)
    for (const s of ['ok', 'never_read', 'signed_out', 'key_paid', 'unqualified', 'error']) expect(offersRead(view({ state: s })), s).toBe(true)
  })

  it('waits for nextReadAfter only while it is ahead of the clock', () => {
    expect(readWait(view(), NOW)).toBeNull()
    expect(readWait(view({ nextReadAfter: new Date(NOW + 60_000).toISOString() }), NOW)).toBe(NOW + 60_000)
    expect(readWait(view({ nextReadAfter: new Date(NOW - 1).toISOString() }), NOW)).toBeNull()
  })
})

describe('the questions and the account choice', () => {
  it('keys each question by all it asks, and sends only what it names', () => {
    expect(capacityKey({ agent: 'claude' })).not.toBe(capacityKey({ agent: 'claude', account: 'default' }))
    expect(capacityKey({ agent: 'claude', yolo: false })).not.toBe(capacityKey({ agent: 'claude' }))
    expect(capacityParams({ agent: 'claude' })).toEqual({ agent: 'claude' })
    expect(capacityParams({ agent: 'claude', account: 'a-1', model: 'opus', launch: 'remote', yolo: true })).toEqual({ agent: 'claude', account: 'a-1', model: 'opus', launch: 'remote', yolo: 'true' })
    expect(capacityParams({ agent: 'claude', launch: 'session' })).toEqual({ agent: 'claude' })
  })

  it('reads a capacity event by its key alone', () => {
    expect(asCapacityEvent({ agent: 'claude', account: 'default', state: 'ok', readAt: '2026-11-12T10:00:00Z' })).toEqual({ agent: 'claude', account: 'default' })
    expect(asCapacityEvent({ agent: 'claude' })).toBeNull()
    expect(asCapacityEvent(null)).toBeNull()
  })

  it("offers the default login and the adapter's accounts not retired, the default marked", () => {
    const doc = {
      accounts: [
        { id: 'a-work', adapter: 'claude', label: 'Work' },
        { id: 'a-old', adapter: 'claude', label: 'Old', retired: true },
        { id: 'a-codex', adapter: 'codex', label: 'Codex work' },
      ],
      adapters: [
        { adapter: 'claude', default: 'a-work' },
        { adapter: 'codex', default: 'default' },
      ],
    }
    expect(accountChoices(doc, 'claude')).toEqual([
      { value: 'default', label: 'Default login' },
      { value: 'a-work', label: 'Work (default)' },
    ])
    expect(accountChoices(doc, 'codex')).toEqual([
      { value: 'default', label: 'Default login (default)' },
      { value: 'a-codex', label: 'Codex work' },
    ])
    // A tool whose login folder Conductor cannot move: no choice at all.
    expect(accountChoices(doc, 'copilot')).toEqual([])
    expect(accountChoices(null, 'claude')).toEqual([])
    expect(defaultChoice(doc, 'claude')).toBe('a-work')
    // A default naming a retired account is the default login, as the server reads it.
    expect(defaultChoice({ ...doc, adapters: [{ adapter: 'claude', default: 'a-old' }] }, 'claude')).toBe('default')
  })

  it('holds a model to the form the server takes', () => {
    expect(modelProblem('')).toBe('')
    expect(modelProblem('claude-opus-4@2026')).toBe('')
    expect(modelProblem('-m')).not.toBe('')
    expect(modelProblem('a b')).not.toBe('')
    expect(modelProblem('x'.repeat(101))).not.toBe('')
  })
})
