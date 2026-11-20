import { describe, expect, it } from 'vitest'
import { answerPage, notOwn, onDevice, pointerWords, remoteDecisions, remotePins, remoteRuns } from './remote'
import type { RemoteRecord } from './protocol'

const rec = (kind: RemoteRecord['kind'], id: string, data: unknown, at = '2026-11-20T09:00:00Z', device = 'dev-1'): RemoteRecord => ({
  kind,
  device,
  deviceName: 'studio',
  id,
  rev: 1,
  at,
  data,
})

describe('remote records', () => {
  it('says where a record is from', () => {
    expect(onDevice({ deviceName: 'studio', device: 'dev-1' })).toBe('on studio')
    expect(onDevice({ deviceName: '', device: 'dev-1' })).toBe('on dev-1')
  })

  it("leaves out this server's own device's records", () => {
    const rs = [rec('pins', 'pins', { pins: [] }), rec('pins', 'pins', { pins: [] }, undefined, 'dev-2')]
    expect(notOwn(rs, { device: 'dev-2' }).map((r) => r.device)).toEqual(['dev-1'])
    expect(notOwn(rs, {})).toHaveLength(2)
  })

  it('reads decisions, their question or none when kept, newest first', () => {
    const ds = remoteDecisions([
      rec('decision', 'dr-1', { id: 'dr-1', state: 'applied', request: { question: 'Ship it?' } }, '2026-11-20T08:00:00Z'),
      rec('decision', 'dr-2', {
        id: 'dr-2',
        state: 'open',
        requestKept: 'output',
        ticket: 't1',
      }),
      rec('pins', 'pins', { pins: [] }),
      rec('decision', 'dr-3', { state: 'open' }),
    ])
    expect(ds.map((d) => [d.id, d.question, d.open, d.where, d.ticket])).toEqual([
      ['dr-2', null, true, 'on studio', 't1'],
      ['dr-1', 'Ship it?', false, 'on studio', undefined],
    ])
  })

  it('says a run in the one status language, on its machine', () => {
    expect(pointerWords('running', undefined, false, 'on studio')).toBe('running on studio')
    expect(pointerWords('running', undefined, true, 'on studio')).toBe('paused on studio')
    expect(pointerWords('finished', 'succeeded', false, 'on studio')).toBe('finished on studio')
    expect(pointerWords('finished', 'failed', false, 'on studio')).toBe('failed on studio')
    const runs = remoteRuns([
      rec('pointer', 'team-00000001', {
        id: 'team-00000001',
        state: 'running',
        ticket: 't1',
      }),
      rec('pointer', 'x', { id: 'x' }),
    ])
    expect(runs).toEqual([
      {
        key: 'dev-1/team-00000001',
        id: 'team-00000001',
        where: 'on studio',
        words: 'running on studio',
        tone: 'active',
        ticket: 't1',
      },
    ])
  })

  it("reads another machine's pins, a pin with no name by its session", () => {
    const pins = remotePins([
      rec('pins', 'pins', {
        pins: [{ session: 's1', name: 'build' }, { session: 's2' }, { name: 'no session' }],
      }),
    ])
    expect(pins.map((p) => [p.session, p.name, p.where])).toEqual([
      ['s1', 'build', 'on studio'],
      ['s2', 's2', 'on studio'],
    ])
  })

  it('opens an answer page only for an open decision whose request left, at an https address', () => {
    const open = { id: 'dr-2', open: true, question: 'Ship it?' }
    expect(answerPage({ answerUrl: 'https://example.com/answer' }, open)).toBe('https://example.com/answer?decision=dr-2')
    expect(answerPage({ answerUrl: 'http://example.com/answer' }, open)).toBeNull()
    expect(answerPage({}, open)).toBeNull()
    expect(answerPage({ answerUrl: 'https://example.com/a' }, { ...open, open: false })).toBeNull()
    expect(answerPage({ answerUrl: 'https://example.com/a' }, { ...open, question: null })).toBeNull()
  })
})
