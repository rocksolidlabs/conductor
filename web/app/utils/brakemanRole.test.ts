import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { ROLE_LOG_READ, ROLE_STATES, ReadOrder, WAKE_SOURCES, batchCount, nextBriefingWords, pauseAction, retryWords, roleSentence, roleShort, roleWords, rowWords, sourceWords, subjectLabel, wokenWords } from './brakemanRole'

// The background role's words and the controls' choices (docs/plans/brakeman.md, PR-15), against internal/crew/role.go's names.

const roleGo = () => readFileSync(new URL('../../../internal/crew/role.go', import.meta.url), 'utf8')
const consts = (src: string, list: string) => {
  const names = new RegExp(`var ${list} = \\[\\]string\\{([^}]*)\\}`).exec(src)![1]!.split(',').map((k) => k.trim())
  return names.map((n) => new RegExp(`\\b${n}\\s*=\\s*"([a-z_]+)"`).exec(src)![1])
}

describe("the role's states and sources, the server's", () => {
  it('names every state and every source internal/crew/role.go has', () => {
    const src = roleGo()
    expect([...ROLE_STATES]).toEqual(consts(src, 'RoleStates'))
    expect(Object.keys(WAKE_SOURCES)).toEqual(consts(src, 'WakeSources'))
    expect(sourceWords('task_failed')).toBe('A task failed')
    expect(sourceWords('briefing')).toBe('A briefing time')
    expect(sourceWords('something_new')).toBe('something_new')
  })
})

describe('the role in words', () => {
  const now = new Date('2026-11-05T21:00:00Z')
  const batch = { id: 'wb-3', openedAt: '2026-11-05T20:59:00Z', wakes: [{ source: 'task_failed', subject: 'task:team-0a1b2c3d/build', count: 2, first: '', last: '' }], overflow: { count: 3, first: '', last: '' } }

  it('says each state, with its batch, request, cooldown or why', () => {
    expect(roleWords({ state: 'idle' }, 120, now)).toBe('idle: nothing has woken it')
    expect(batchCount(batch)).toBe(5)
    expect(roleWords({ state: 'gathering', open: batch }, 120, now)).toMatch(/^gathering 5 wakes into wb-3: its turn starts at /)
    expect(roleWords({ state: 'gathering', open: { ...batch, wakes: [{ ...batch.wakes[0]!, count: 1 }], overflow: undefined } })).toBe('gathering 1 wake into wb-3')
    expect(roleWords({ state: 'briefing', request: 'br-9' })).toBe('briefing: br-9')
    expect(roleWords({ state: 'cooling', coolUntil: '2026-11-05T21:30:00Z' }, 120, now)).toMatch(/^cooling until .+: wakes meanwhile wait for the next turn$/)
    expect(roleWords({ state: 'paused' })).toBe('paused: nothing wakes it until you resume it')
    expect(roleWords({ state: 'unavailable', why: 'the turn could not start: the agent exited before it read its request: is it logged in?' })).toBe(
      'unavailable: the turn could not start: the agent exited before it read its request: is it logged in?',
    )
  })

  it('says what tries again for an unavailable role, and nothing for another', () => {
    expect(retryWords({ state: 'idle' })).toBe('')
    expect(retryWords({ state: 'unavailable' })).toBe('It tries again on your next request, a save of its settings or the next briefing time. Meanwhile the wakes wait in its next batch, and nothing else waits on it.')
    expect(retryWords({ state: 'unavailable', retryAt: '2026-11-06T06:00:00Z' }, now)).toMatch(/^It tries again at .+, or sooner on your next request, a save of its settings or the next briefing time\.$/)
  })

  it('says what else tries again by the reason the role is unavailable', () => {
    const at = '2026-11-06T06:00:00Z'
    expect(retryWords({ state: 'unavailable', retryAt: at, reason: 'start' }, now)).toMatch(/^It tries again at .+, or sooner on its agent reading a request \(yours, once it is signed in or past its screen\) or any save of its settings\.$/)
    expect(retryWords({ state: 'unavailable', retryAt: at, reason: 'budget' }, now)).toMatch(/or sooner on your next request, a change of its budget or the next briefing time\.$/)
    expect(retryWords({ state: 'unavailable', reason: 'off' })).toMatch(/^It tries again on your next request, turning it on in its settings or the next briefing time\./)
    expect(retryWords({ state: 'unavailable', reason: 'stuck' })).toMatch(/^It tries again on your next request or the next briefing time, once the last run is stopped\./)
  })

  it("says a log row's wake by name and what became of it, never by the batch's id", () => {
    expect(rowWords({ source: 'task_failed', subject: 'task:team-0a1b2c3d/build', at: '', batch: 'wb-1', request: 'br-2' })).toEqual({ what: 'A task failed: task build', fate: 'taken in a turn', request: 'br-2' })
    expect(rowWords({ source: 'task_failed', subject: 'task:team-0a1b2c3d/lint', at: '', batch: 'wb-2' })).toEqual({ what: 'A task failed: task lint', fate: 'waiting for its turn' })
    expect(rowWords({ source: 'run_ended', subject: 'run:team-0a1b2c3d', at: '', batch: 'wb-2' }, { run: (id) => (id === 'team-0a1b2c3d' ? 'team' : undefined) }).what).toBe('A run ended: run team')
    expect(rowWords({ source: 'question_open', subject: `question:${'a'.repeat(16)}/${'b'.repeat(16)}`, at: '' }, { session: () => 'ui' }).what).toBe('A question waited: question in ui')
    for (const row of [{ source: 'task_failed', at: '', batch: 'wb-1', request: 'br-2' }, { source: 'task_failed', at: '', batch: 'wb-2' }]) expect(rowWords(row).fate).not.toMatch(/wb-/)
    expect(rowWords({ source: 'task_failed', subject: 'task:team-0a1b2c3d/lint', at: '', held: 'paused' }).fate).toBe('not gathered: the role was paused')
    expect(rowWords({ source: 'request', subject: 'br-4', at: '', held: "a person's own request" })).toEqual({ what: 'Your request: br-4', fate: "no batch: a person's own request" })
    expect(rowWords({ source: 'briefing', at: '' })).toEqual({ what: 'A briefing time', fate: '' })
  })

  it("names a capacity wake's agent and account by their ids, its own login in words", () => {
    expect(rowWords({ source: 'capacity', subject: 'capacity:claude/a-abcdefghijkl', at: '', batch: 'wb-3' })).toEqual({
      what: 'An agent neared its usage limit: capacity of claude on a-abcdefghijkl',
      fate: 'waiting for its turn',
    })
    expect(subjectLabel('capacity:codex/default')).toBe('capacity of codex on its default login')
    // A reading's ref keeps its own label; a subject of no known shape is shown as it is.
    expect(subjectLabel('capacity:codex/default@2026-11-16T10:00:00Z')).toBe('capacity of codex on default')
    expect(subjectLabel('capacity:codex/someone')).toBe('capacity:codex/someone')
  })

  it('offers Pause, or Resume for a paused role', () => {
    expect(pauseAction(undefined)).toBe(null)
    expect(pauseAction({ state: 'cooling' })).toBe('pause')
    expect(pauseAction({ state: 'paused' })).toBe('resume')
  })
})

describe("the role card's reads", () => {
  it('take a reply only while no read began after it: an older reply arriving last is dropped', async () => {
    const order = new ReadOrder()
    let shown = ''
    const deferred = () => {
      let resolve!: (v: string) => void
      const p = new Promise<string>((r) => (resolve = r))
      return { p, resolve }
    }
    const read = async (reply: Promise<string>) => {
      const ticket = order.begin()
      const v = await reply
      if (order.current(ticket)) shown = v
    }
    const older = deferred()
    const newer = deferred()
    const a = read(older.p)
    const b = read(newer.p)
    newer.resolve('paused')
    await b
    older.resolve('idle')
    await a
    expect(shown).toBe('paused')
    const last = deferred()
    const c = read(last.p)
    last.resolve('idle')
    await c
    expect(shown).toBe('idle')
  })
})

describe('the role in one sentence', () => {
  const hm = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
  const batch = { id: 'wb-3', openedAt: '2026-11-05T20:59:00Z', wakes: [{ source: 'task_failed', subject: 'task:team-0a1b2c3d/build', count: 3, first: '', last: '' }] }

  it('says each state once, with no id', () => {
    expect(roleSentence({ state: 'idle' })).toBe('Idle: nothing has woken it.')
    expect(roleSentence({ state: 'gathering', open: batch }, 120)).toBe(`Gathering 3 wakes: its turn starts at ${hm('2026-11-05T21:01:00Z')}.`)
    expect(roleSentence({ state: 'gathering', open: { ...batch, wakes: [{ ...batch.wakes[0]!, count: 1 }] } })).toBe('Gathering 1 wake.')
    expect(roleSentence({ state: 'briefing', fired: batch })).toBe('Briefing now on 3 wakes.')
    expect(roleSentence({ state: 'briefing' })).toBe('Briefing now.')
    expect(roleSentence({ state: 'cooling', coolUntil: '2026-11-05T21:30:00Z' })).toBe(`Cooling until ${hm('2026-11-05T21:30:00Z')}.`)
    expect(roleSentence({ state: 'paused' })).toBe('Paused: nothing wakes it until you resume it.')
    expect(roleSentence({ state: 'unavailable', why: 'the agent exited before it read its request.' })).toBe('Unavailable: the agent exited before it read its request.')
    for (const state of ROLE_STATES) expect(roleSentence({ state, open: batch, fired: batch })).not.toMatch(/wb-|br-/)
  })

  it("says the state in a few words for a phone's header", () => {
    expect(roleShort({ state: 'cooling', coolUntil: '2026-11-05T21:30:00Z' })).toBe(`cooling until ${hm('2026-11-05T21:30:00Z')}`)
    expect(roleShort({ state: 'gathering', open: batch })).toBe('gathering 3 wakes')
    expect(roleShort({ state: 'briefing' })).toBe('briefing now')
    expect(roleShort({ state: 'paused' })).toBe('paused')
    expect(roleShort(undefined)).toBe('')
  })

  it("says the next briefing in the browser's clock, an older server's times in its own, and none while paused or unavailable", () => {
    expect(nextBriefingWords({ state: 'cooling' }, '2026-11-06T07:00:00Z', ['07:00'], true)).toBe(`Next briefing at ${hm('2026-11-06T07:00:00Z')}.`)
    expect(nextBriefingWords({ state: 'idle' }, undefined, ['07:00'], true)).toBe('Next briefing at 07:00 (server time).')
    expect(nextBriefingWords({ state: 'idle' }, undefined, ['07:00', '12:00', '18:30'], true)).toBe('Briefings at 07:00, 12:00 and 18:30 (server time).')
    expect(nextBriefingWords({ state: 'idle' }, undefined, [], true)).toBe('')
    expect(nextBriefingWords({ state: 'paused' }, '2026-11-06T07:00:00Z', ['07:00'], true)).toBe('')
    expect(nextBriefingWords({ state: 'unavailable' }, '2026-11-06T07:00:00Z', ['07:00'], true)).toBe('')
    expect(nextBriefingWords({ state: 'idle' }, '2026-11-06T07:00:00Z', ['07:00'], false)).toBe('')
  })

  it("counts today's wakes not held and the briefs they led to, and says when the read reached its bound", () => {
    const now = new Date('2026-11-05T21:00:00')
    const today = (h: number) => new Date(2026, 10, 5, h).toISOString()
    const log = [
      { source: 'task_failed', at: today(20), batch: 'wb-2', request: 'br-5' },
      { source: 'run_ended', at: today(19), batch: 'wb-2', request: 'br-5' },
      { source: 'task_failed', at: today(18), batch: 'wb-1', request: 'br-4' },
      { source: 'task_failed', at: today(17), held: 'paused' },
      { source: 'task_failed', at: new Date(2026, 10, 4, 23).toISOString(), batch: 'wb-0', request: 'br-3' },
    ]
    const requests = new Map([['br-5', { hasBrief: true }], ['br-4', { hasBrief: false }], ['br-3', { hasBrief: true }]])
    expect(wokenWords(log, requests, now)).toBe('Woken 3 times today, 1 brief.')
    expect(wokenWords(log.slice(2, 3), new Map(), now)).toBe('Woken once today, 0 briefs.')
    const full = Array.from({ length: ROLE_LOG_READ }, () => ({ source: 'task_failed', at: today(20), batch: 'wb-9' }))
    expect(wokenWords(full, new Map(), now)).toBe(`Woken ${ROLE_LOG_READ} or more times today, 0 briefs.`)
  })
})
