import { describe, expect, it, vi } from 'vitest'
import { createRenderer, defineComponent, h, nextTick, ref } from 'vue'
import type { GuideOperation } from '../utils/brakemanCards'
import { GuideOpsRead, followGuideToken, readGuideWhileMounted } from './useGuideOps'

// The read of the guide's operations that Settings' grants and a brief's proposals share: read again for a new token while a reader
// shows, so a page opened before the right token was saved offers its grants once it is (#538's review), and never another token's.

const OPS: GuideOperation[] = [{ name: 'launch_approved_plan', scope: 'act' }]
const OTHER: GuideOperation[] = [{ name: 'request_repair', scope: 'act' }]
const refused = () => Object.assign(new Error('workbench token required'), { status: 401 })

/** Lets the read's reply be taken: its continuation runs on the microtasks before a timer. */
const settle = () => new Promise<void>((r) => setTimeout(r, 0))

/** The route, held: each call waits for the test's answer, given in order (`answer`, `fail`). */
function heldRoute() {
  const pending: Array<{ ok: (b: { operations: GuideOperation[] }) => void; no: (e: Error) => void }> = []
  const fetch = vi.fn(() => new Promise<{ operations: GuideOperation[] }>((ok, no) => pending.push({ ok, no })))
  return {
    fetch,
    answer: async (ops: GuideOperation[]) => {
      pending.shift()!.ok({ operations: ops })
      await settle()
    },
    fail: async (e: Error) => {
      pending.shift()!.no(e)
      await settle()
    },
  }
}

// A component that reads the operations (readGuideWhileMounted), mounted and unmounted by Vue itself through a renderer with no DOM.
const nodeOps = {
  insert: () => {},
  remove: () => {},
  createElement: () => ({}),
  createText: () => ({}),
  createComment: () => ({}),
  setText: () => {},
  setElementText: () => {},
  parentNode: () => null,
  nextSibling: () => null,
  patchProp: () => {},
}
const { createApp } = createRenderer<object, object>(nodeOps)
function mountReader(r: GuideOpsRead) {
  const app = createApp(defineComponent({ setup: () => (readGuideWhileMounted(r), () => h('div')) }))
  app.mount({})
  return app
}

describe('the initial login', () => {
  it('reads again once the right token is saved while Settings shows, with no remount, and the grants are offered', async () => {
    const route = heldRoute()
    const ops = ref<GuideOperation[] | null>(null)
    const r = new GuideOpsRead(route.fetch, ops)
    const token = ref('a-wrong-token')
    const stop = followGuideToken(r, token)
    // Settings opened with a token the server refuses: its read fails and nothing is known.
    const settings = mountReader(r)
    expect(route.fetch).toHaveBeenCalledTimes(1)
    await route.fail(refused())
    expect(ops.value).toBeNull()
    // The token gate's Save: the same Settings, not mounted again, reads the guide with the new token.
    token.value = 'the-workbench-token'
    await nextTick()
    expect(route.fetch).toHaveBeenCalledTimes(2)
    await route.answer(OPS)
    expect(ops.value).toEqual(OPS)
    settings.unmount()
    stop()
  })

  it('reads with the token the desktop shell hands over after Settings mounted, the read made without one dropped', async () => {
    const route = heldRoute()
    const ops = ref<GuideOperation[] | null>(null)
    const r = new GuideOpsRead(route.fetch, ops)
    const token = ref('')
    const stop = followGuideToken(r, token)
    const settings = mountReader(r)
    expect(route.fetch).toHaveBeenCalledTimes(1)
    token.value = 'the-workbench-token'
    await nextTick()
    expect(route.fetch).toHaveBeenCalledTimes(2)
    await route.answer(OTHER) // the tokenless read's reply, late: not this token's
    expect(ops.value).toBeNull()
    await route.answer(OPS)
    expect(ops.value).toEqual(OPS)
    settings.unmount()
    stop()
  })
})

describe('the workbench token', () => {
  it('forgets what was read when it changes, drops a reply on its way for the token before, and the same token again forgets nothing', async () => {
    const route = heldRoute()
    const ops = ref<GuideOperation[] | null>(null)
    const r = new GuideOpsRead(route.fetch, ops)
    const token = ref('the-workbench-token')
    const stop = followGuideToken(r, token)
    const settings = mountReader(r)
    await route.answer(OPS)
    expect(ops.value).toEqual(OPS)
    token.value = 'another-servers-token'
    await nextTick()
    expect(ops.value).toBeNull()
    await route.answer(OTHER)
    expect(ops.value).toEqual(OTHER)
    token.value = 'another-servers-token'
    await nextTick()
    expect(route.fetch).toHaveBeenCalledTimes(2)
    // Forgotten (none): nothing is read without a token, and nothing is kept.
    token.value = ''
    await nextTick()
    expect(ops.value).toBeNull()
    expect(route.fetch).toHaveBeenCalledTimes(2)
    settings.unmount()
    stop()
  })

  it('is followed while no reader shows: forgotten, read by none, and read by the next reader to mount', async () => {
    const route = heldRoute()
    const ops = ref<GuideOperation[] | null>(null)
    const r = new GuideOpsRead(route.fetch, ops)
    const token = ref('the-workbench-token')
    const stop = followGuideToken(r, token)
    const first = mountReader(r)
    await route.answer(OPS)
    first.unmount()
    token.value = 'another-servers-token'
    await nextTick()
    expect(ops.value).toBeNull()
    expect(route.fetch).toHaveBeenCalledTimes(1)
    const second = mountReader(r)
    expect(route.fetch).toHaveBeenCalledTimes(2)
    await route.answer(OTHER)
    expect(ops.value).toEqual(OTHER)
    second.unmount()
    stop()
  })
})

describe('readers', () => {
  it('share a read on its way and read nothing once the operations are known; a failed read is asked again by the next to mount', async () => {
    const route = heldRoute()
    const ops = ref<GuideOperation[] | null>(null)
    const r = new GuideOpsRead(route.fetch, ops)
    const stop = followGuideToken(r, ref('the-workbench-token'))
    const settings = mountReader(r)
    const brief = mountReader(r)
    expect(route.fetch).toHaveBeenCalledTimes(1)
    await route.fail(new Error('Cannot reach the server'))
    expect(ops.value).toBeNull()
    brief.unmount()
    const again = mountReader(r)
    expect(route.fetch).toHaveBeenCalledTimes(2)
    await route.answer(OPS)
    const later = mountReader(r)
    expect(route.fetch).toHaveBeenCalledTimes(2)
    expect(ops.value).toEqual(OPS)
    for (const a of [settings, again, later]) a.unmount()
    expect(r.shown).toBe(false)
    stop()
  })
})
