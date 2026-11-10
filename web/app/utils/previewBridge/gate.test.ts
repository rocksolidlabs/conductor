import { describe, expect, it } from 'vitest'
import { PREVIEW_MARK } from '../protocol'
import { createGate, originsOf } from './gate'

const WORKBENCH = 'http://127.0.0.1:8080'
const NONCE = 'n0nce-of-the-hello-1234'

function setup() {
  const posts: Array<{ message: unknown; target: string }> = []
  const parent = { postMessage: (message: unknown, target: string) => posts.push({ message, target }) }
  const foreign = { postMessage: () => { throw new Error('a foreign window is never posted to') } }
  const gate = createGate([WORKBENCH, 'http://localhost:8080'], parent)
  const m = (t: string, extra: Record<string, unknown> = {}, nonce = NONCE) => ({ conductor: PREVIEW_MARK, t, nonce, ...extra })
  return { posts, parent, foreign, gate, m }
}

describe('the bridge gate', () => {
  it('takes a hello from the parent at a workbench origin, and answers it there alone', () => {
    const { posts, parent, gate, m } = setup()
    gate.answer({ t: 'cancelled' })
    expect(posts).toEqual([]) // nothing before a hello
    expect(gate.take({ source: parent, origin: WORKBENCH, data: m('hello') })?.t).toBe('hello')
    expect(gate.take({ source: parent, origin: WORKBENCH, data: m('select') })?.t).toBe('select')
    gate.answer({ t: 'cancelled' })
    expect(posts).toEqual([{ message: { t: 'cancelled', conductor: PREVIEW_MARK, nonce: NONCE }, target: WORKBENCH }])
    for (const p of posts) expect(p.target).not.toBe('*')
  })

  it('drops, unanswered, what another origin, another window, a wrong nonce, a big message or an unknown type sends', () => {
    const { posts, parent, foreign, gate, m } = setup()
    expect(gate.take({ source: parent, origin: 'http://127.0.0.1:5173', data: m('hello') })).toBeNull()
    expect(gate.take({ source: foreign, origin: WORKBENCH, data: m('hello') })).toBeNull()
    expect(gate.take({ source: parent, origin: 'null', data: m('hello') })).toBeNull()
    expect(gate.take({ source: parent, origin: WORKBENCH, data: m('select') })).toBeNull() // before any hello
    expect(gate.take({ source: parent, origin: WORKBENCH, data: m('hello') })).not.toBeNull()
    expect(gate.take({ source: parent, origin: WORKBENCH, data: m('select', {}, 'another-nonce-0000000') })).toBeNull()
    expect(gate.take({ source: parent, origin: WORKBENCH, data: { conductor: PREVIEW_MARK, t: 'select' } })).toBeNull()
    expect(gate.take({ source: parent, origin: WORKBENCH, data: m('describe', { selector: 'a'.repeat(5000) }) })).toBeNull()
    expect(gate.take({ source: parent, origin: WORKBENCH, data: m('eval', { code: '1' }) })).toBeNull()
    // An answer's type sent to the bridge is no request.
    expect(gate.take({ source: parent, origin: WORKBENCH, data: m('cancelled') })).toBeNull()
    expect(gate.take({ source: parent, origin: WORKBENCH, data: 'hello' })).toBeNull()
    // A value JSON cannot hold is dropped, never thrown.
    expect(gate.take({ source: parent, origin: WORKBENCH, data: m('select', { x: 1n }) })).toBeNull()
    // The other workbench origin may not speak inside this hello's exchange; its own hello starts another.
    expect(gate.take({ source: parent, origin: 'http://localhost:8080', data: m('select') })).toBeNull()
    expect(gate.take({ source: parent, origin: 'http://localhost:8080', data: m('hello', {}, 'another-nonce-0000000') })).not.toBeNull()
    gate.answer({ t: 'cancelled' })
    expect(posts.map((p) => p.target)).toEqual(['http://localhost:8080'])
  })

  it('takes only the origins the server wrote in', () => {
    expect(originsOf('__CONDUCTOR_WORKBENCH_ORIGINS__')).toEqual([])
    expect(originsOf([WORKBENCH, '*', 'null', '', 7, 'x'.repeat(3000)])).toEqual([WORKBENCH])
  })
})
