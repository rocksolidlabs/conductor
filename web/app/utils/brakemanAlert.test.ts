import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { asBrakemanNeedsYou, BRIEF_ALERT_TAG, briefAlert, briefPath, MAX_NEEDS_YOU_EVENT, needsYouWords } from './brakemanAlert'

// The page's half of a background brief with a decision: the event it takes, and the words, which are the server's word for word.
describe('brakemanAlert', () => {
  it('words one decision and several as the server does, with one tag and the brief as where a click goes', () => {
    expect(briefAlert({ id: 'br-14', decisions: 1 })).toEqual({
      title: "Brakeman's background brief br-14 holds 1 decision",
      body: 'It opens on the Brakeman page.',
      tag: 'conductor-brakeman-needs-you',
      path: '/brakeman?request=br-14',
    })
    expect(briefAlert({ id: 'br-3', decisions: 3 }).title).toBe("Brakeman's background brief br-3 holds 3 decisions")
    expect(briefAlert({ id: 'br-3', decisions: 3 }).tag).toBe(BRIEF_ALERT_TAG)
  })

  it('holds the words, the path and the bound equal to the Go source', () => {
    const src = readFileSync(new URL('../../../internal/api/brakeman_notify.go', import.meta.url), 'utf8')
    const constant = (name: string) => {
      const m = src.match(new RegExp(`\\b${name}\\s*=\\s*"([^"]*)"`))
      if (!m) throw new Error(`no ${name} in brakeman_notify.go`)
      return m[1]!
    }
    expect(needsYouWords('br-14', 1)).toBe(constant('needsYouOne').replace('%s', 'br-14'))
    expect(needsYouWords('br-14', 7)).toBe(constant('needsYouMany').replace('%s', 'br-14').replace('%d', '7'))
    expect(briefPath('br-14')).toBe(`${constant('needsYouPath')}br-14`)
    expect(src).toMatch(new RegExp(`maxBrakemanNeedsYou = ${MAX_NEEDS_YOU_EVENT}\\b`))
  })

  it('takes the event as the server sends it, its longest too', () => {
    expect(asBrakemanNeedsYou('{"id":"br-14","decisions":1}')).toEqual({ id: 'br-14', decisions: 1 })
    const longest = `{"id":"br-${'9'.repeat(18)}","decisions":100}`
    expect(asBrakemanNeedsYou(longest)).toEqual({ id: `br-${'9'.repeat(18)}`, decisions: 100 })
  })

  it('drops whole an event past its bound, of another shape, with an id not br-<n>, or a count not an integer of 1 to 100', () => {
    for (const data of [
      'not json',
      '[]',
      '["br-1",1]',
      'null',
      '"br-1"',
      '{"decisions":1}',
      '{"id":"br-0","decisions":1}',
      '{"id":"br-01","decisions":1}',
      '{"id":"br-1/../x","decisions":1}',
      '{"id":"https://example.invalid/","decisions":1}',
      `{"id":"br-${'9'.repeat(19)}","decisions":1}`,
      '{"id":"br-1"}',
      '{"id":"br-1","decisions":0}',
      '{"id":"br-1","decisions":101}',
      '{"id":"br-1","decisions":1.5}',
      '{"id":"br-1","decisions":"1"}',
      '{"id":"br-1","decisions":-1}',
    ]) {
      expect(asBrakemanNeedsYou(data), data).toBeNull()
    }
    // 65 bytes, every value valid: dropped by its size alone.
    const padded = `{"id":"br-1","decisions":1,"x":"${'a'.repeat(65 - '{"id":"br-1","decisions":1,"x":""}'.length)}"}`
    expect(new TextEncoder().encode(padded).length).toBe(65)
    expect(asBrakemanNeedsYou(padded)).toBeNull()
    expect(asBrakemanNeedsYou(padded.replace('aa', 'a'))).toEqual({ id: 'br-1', decisions: 1 })
  })

  it('takes only the id and the count: a word beside them reaches no notification', () => {
    const e = asBrakemanNeedsYou('{"id":"br-2","decisions":2,"title":"MARK"}')
    expect(e).toEqual({ id: 'br-2', decisions: 2 })
    expect(JSON.stringify(briefAlert(e!))).not.toContain('MARK')
  })
})
