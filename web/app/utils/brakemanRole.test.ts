import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { ROLE_STATES, ReadOrder, WAKE_SOURCES, batchCount, pauseAction, retryWords, roleWords, rowWords, sourceWords } from './brakemanRole'

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

  it("says a log row's wake and what became of it", () => {
    expect(rowWords({ source: 'task_failed', subject: 'task:team-0a1b2c3d/build', at: '', batch: 'wb-1', request: 'br-2' })).toEqual({ what: 'A task failed: task build', fate: 'wb-1 → br-2' })
    expect(rowWords({ source: 'task_failed', subject: 'task:team-0a1b2c3d/lint', at: '', batch: 'wb-2' })).toEqual({ what: 'A task failed: task lint', fate: 'waiting in wb-2' })
    expect(rowWords({ source: 'task_failed', subject: 'task:team-0a1b2c3d/lint', at: '', held: 'paused' }).fate).toBe('not gathered: the role was paused')
    expect(rowWords({ source: 'request', subject: 'br-4', at: '', held: "a person's own request" })).toEqual({ what: 'Your request: br-4', fate: "no batch: a person's own request" })
    expect(rowWords({ source: 'briefing', at: '' })).toEqual({ what: 'A briefing time', fate: '' })
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
