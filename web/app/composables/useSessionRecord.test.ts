import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import type { RecordReply } from '~/utils/history'
// Imported with the file, not inside a test (a module loaded in a test is loaded on the test's 5 s). Their auto-imports are globals
// read when they are called, stubbed below.
import { ApiError } from './useApi'
import { RESUME_PENDING_EVERY_MS, useHistory } from './useHistory'
import { useSessionRecord } from './useSessionRecord'

// Codex's round 2 on #731, on the session record page (Program item HS1, docs/plans/history.md, "web/app", PR-6), wired to an API whose
// every request the test holds and lets go (or fails) in the order it needs. One rule is under test: a reply is applied only while the
// page still shows what it asked for. A record read overtaken by Forget does not bring the record back; a resume answered after the
// page was left, or after another record was opened, takes nobody anywhere, and one still pending is asked no more. And a resume
// answered from its receipt alone (its session no longer listed) opens the new session's record, never a session page that answers 404.
// Codex's round 3: of two reads of the same record, only the newest one's answers are applied, the record's and the entry's; and a
// resume whose reply was lost twice is said to have an unknown outcome, never to have failed.

function held<T>() {
  let ok!: (v: T) => void
  let fail!: (e: unknown) => void
  const p = new Promise<T>((resolve, reject) => {
    ok = resolve
    fail = reject
  })
  return { p, ok, fail }
}

/** Every request sent, by path and method, held until the test lets it go. */
let sent: Array<{ path: string; method: string } & ReturnType<typeof held<unknown>>>
let unmount: Array<() => void>
let push: ReturnType<typeof vi.fn>
let toasts: Array<{ title: string; description?: string }>

beforeEach(() => {
  sent = []
  unmount = []
  toasts = []
  push = vi.fn(() => Promise.resolve())
  for (const k of ['ref', 'computed', 'watch'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('onBeforeUnmount', (f: () => void) => unmount.push(f))
  vi.stubGlobal('useApi', () => ({
    request: (path: string, o?: { method?: string }) => {
      const h = held<unknown>()
      sent.push({ path, method: o?.method ?? 'GET', ...h })
      return h.p
    },
  }))
  vi.stubGlobal('useHistory', useHistory)
  vi.stubGlobal('useWorkbenchToken', () => ({ hasToken: vue.ref(true), needsToken: vue.ref(false), token: vue.ref('t') }))
  vi.stubGlobal('useRouter', () => ({ push }))
  vi.stubGlobal('useToast', () => ({ add: (t: { title: string; description?: string }) => toasts.push(t) }))
})
afterEach(() => {
  for (const f of unmount) f()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

/** Lets the microtasks and the watchers run (fake timers or not). */
const settle = async () => {
  for (let i = 0; i < 5; i++) await new Promise<void>((r) => queueMicrotask(r))
  await vue.nextTick()
  for (let i = 0; i < 5; i++) await new Promise<void>((r) => queueMicrotask(r))
}

const ID = '0123456789abcdef'
const NEW = 'fedcba9876543210'
const reply = (id = ID): RecordReply => ({
  record: { v: 1, id, seq: 1, name: 'fix-tests', kind: 'server', agent: { id: 'claude' }, host: {}, cwd: '/w', startedAt: '2026-11-07T10:00:00Z', endedAt: '2026-11-07T11:00:00Z', entries: [] },
  live: false,
  newer: false,
})
const entry = { entry: { key: `session:${ID}`, availability: { state: 'record', words: 'a record', resume: { offered: 'yes' } } } }
const last = (method: string, path: string) => {
  const r = [...sent].reverse().find((s) => s.method === method && s.path === path)
  if (!r) throw new Error(`no ${method} ${path} sent; sent: ${sent.map((s) => `${s.method} ${s.path}`).join(', ')}`)
  return r
}
const count = (method: string, path: string) => sent.filter((s) => s.method === method && s.path === path).length
const all = (method: string, path: string) => sent.filter((s) => s.method === method && s.path === path)
const receipt = (commandId: string, state: 'started' | 'failed' = 'failed') => ({ commandId, state, at: '2026-11-07T12:00:00Z', ...(state === 'started' ? { session: NEW } : { code: 'env_not_kept' }) })
const refusal = (code: string, state = 'failed') => new ApiError(409, code, `refused: ${code}`, {}, { error: { code, message: `refused: ${code}` }, receipt: { commandId: 'x', state, code, at: '' } })

/** A page on the record, drawn: its record read and its entry read answered. */
async function drawn(id = vue.ref(ID)) {
  const page = useSessionRecord(id)
  void page.load()
  await settle()
  last('GET', `/api/history/sessions/${id.value}`).ok(reply(id.value))
  await settle()
  last('GET', '/api/history/entry').ok(entry)
  await settle()
  expect(page.record.value?.id).toBe(id.value)
  return page
}

describe('the session record page', () => {
  it("does not bring a forgotten record back when a read asked before Forget lands after it", async () => {
    const page = await drawn()
    // A resume the route refuses reads the record again (its receipts); that read is slow.
    page.step.value = 'ask'
    const resumed = page.resume(false)
    await settle()
    last('POST', `/api/history/sessions/${ID}/resume`).fail(refusal('env_not_kept'))
    await resumed
    await settle()
    expect(count('GET', `/api/history/sessions/${ID}`)).toBe(2)
    // Forget is asked and done while that read is in flight.
    void page.forget()
    await settle()
    last('DELETE', `/api/history/sessions/${ID}`).ok(undefined)
    await settle()
    expect([page.forgotten.value, page.record.value]).toEqual([true, null])
    // The read lands: nothing of the record comes back, and no entry read follows it.
    last('GET', `/api/history/sessions/${ID}`).ok(reply())
    await settle()
    expect([page.forgotten.value, page.record.value, page.availability.value]).toEqual([true, null, null])
    expect(count('GET', '/api/history/entry')).toBe(1)
  })

  it('does not apply an entry read that lands after Forget', async () => {
    const page = useSessionRecord(vue.ref(ID))
    void page.load()
    await settle()
    last('GET', `/api/history/sessions/${ID}`).ok(reply())
    await settle()
    // The entry read is in flight when Forget is done.
    void page.forget()
    await settle()
    last('DELETE', `/api/history/sessions/${ID}`).ok(undefined)
    await settle()
    last('GET', '/api/history/entry').ok(entry)
    await settle()
    expect([page.forgotten.value, page.availability.value]).toEqual([true, null])
  })

  it('takes nobody anywhere when a resume is answered after the page was left', async () => {
    const page = await drawn()
    page.step.value = 'ask'
    const resumed = page.resume(false)
    await settle()
    for (const f of unmount) f()
    unmount = []
    last('POST', `/api/history/sessions/${ID}/resume`).ok({ session: { id: NEW }, resumed: true, receipt: { commandId: 'x', state: 'started', session: NEW, at: '' } })
    await resumed
    await settle()
    expect(push).not.toHaveBeenCalled()
    expect(toasts).toEqual([])
    // Nor is the record read again for a page that is gone.
    expect(count('GET', `/api/history/sessions/${ID}`)).toBe(1)
    expect([page.step.value, page.resuming.value]).toEqual([null, false])
  })

  it('takes nobody anywhere when a resume is answered after another record was opened', async () => {
    const id = vue.ref(ID)
    const page = await drawn(id)
    page.step.value = 'ask'
    const resumed = page.resume(false)
    await settle()
    expect(page.resuming.value).toBe(true)
    // The person follows a link to another record while the resume is under way: its question closes with it.
    id.value = NEW
    await settle()
    expect([page.step.value, page.resuming.value]).toEqual([null, false])
    last('POST', `/api/history/sessions/${ID}/resume`).ok({ session: { id: 'aaaaaaaaaaaaaaaa' }, resumed: true, receipt: { commandId: 'x', state: 'started', session: 'aaaaaaaaaaaaaaaa', at: '' } })
    await resumed
    await settle()
    expect([push.mock.calls.length, toasts.length, page.resuming.value]).toEqual([0, 0, false])
    // The record now shown is read as asked, and the earlier one's receipts are not read again over it.
    last('GET', `/api/history/sessions/${NEW}`).ok(reply(NEW))
    await settle()
    expect(page.record.value?.id).toBe(NEW)
    expect(count('GET', `/api/history/sessions/${ID}`)).toBe(1)
  })

  it('asks a pending resume no more once the page is left, and says nothing of it', async () => {
    vi.useFakeTimers()
    const page = await drawn()
    page.step.value = 'ask'
    const resumed = page.resume(false)
    await settle()
    last('POST', `/api/history/sessions/${ID}/resume`).fail(refusal('resume_pending', 'pending'))
    await vi.advanceTimersByTimeAsync(RESUME_PENDING_EVERY_MS / 2)
    expect(count('POST', `/api/history/sessions/${ID}/resume`)).toBe(1)
    for (const f of unmount) f()
    unmount = []
    await resumed
    await vi.advanceTimersByTimeAsync(5 * RESUME_PENDING_EVERY_MS)
    expect(count('POST', `/api/history/sessions/${ID}/resume`)).toBe(1)
    expect([push.mock.calls.length, toasts.length]).toEqual([0, 0])
  })

  it("opens the new session's record when a resume is answered from its receipt alone", async () => {
    const page = await drawn()
    page.step.value = 'ask'
    const resumed = page.resume(false)
    await settle()
    // A lost reply's same id, answered from the receipt after the server restarted: the session it started is no longer listed.
    last('POST', `/api/history/sessions/${ID}/resume`).ok({ receipt: { commandId: 'x', state: 'started', session: NEW, at: '' } })
    await resumed
    expect(push).toHaveBeenCalledWith(`/history/sessions/${NEW}`)
    expect(toasts).toEqual([expect.objectContaining({ title: 'fix-tests resumed', description: 'It is no longer listed: its record opens.' })])
    expect(page.step.value).toBe(null)
  })

  it('opens the new session while it is listed', async () => {
    const page = await drawn()
    page.step.value = 'ask'
    const resumed = page.resume(false)
    await settle()
    last('POST', `/api/history/sessions/${ID}/resume`).ok({ session: { id: NEW }, resumed: true, receipt: { commandId: 'x', state: 'started', session: NEW, at: '' } })
    await resumed
    expect(push).toHaveBeenCalledWith(`/sessions/${NEW}`)
    expect(toasts).toEqual([expect.objectContaining({ title: 'fix-tests resumed' })])
  })

  it("applies only the newest read's answers when two reads of the same record land out of order", async () => {
    const page = await drawn()
    // Two resumes refused one after the other: each reads the record again, for its receipts.
    for (const _ of [1, 2]) {
      page.step.value = 'ask'
      const resumed = page.resume(false)
      await settle()
      last('POST', `/api/history/sessions/${ID}/resume`).fail(refusal('env_not_kept'))
      await resumed
      await settle()
    }
    const [, older, newer] = all('GET', `/api/history/sessions/${ID}`)
    expect([older, newer].every(Boolean)).toBe(true)
    const at = (seq: number, resumes: ReturnType<typeof receipt>[]): RecordReply => ({ ...reply(), record: { ...reply().record, seq, resumes } })
    // The newer read lands first, with both receipts, and its entry read answers.
    newer!.ok(at(3, [receipt('a'), receipt('b')]))
    await settle()
    expect(page.record.value?.seq).toBe(3)
    expect(count('GET', '/api/history/entry')).toBe(2)
    last('GET', '/api/history/entry').ok({ entry: { ...entry.entry, availability: { state: 'listed', words: 'listed', resume: { offered: 'no' } } } })
    await settle()
    // The older lands last: neither its record nor an entry read of its own is applied.
    older!.ok(at(2, [receipt('a')]))
    await settle()
    expect([page.record.value?.seq, page.record.value?.resumes?.length, page.availability.value?.state]).toEqual([3, 2, 'listed'])
    expect(count('GET', '/api/history/entry')).toBe(2)
  })

  it("does not apply an older read's entry answer once a newer read was asked", async () => {
    const page = useSessionRecord(vue.ref(ID))
    void page.load()
    await settle()
    last('GET', `/api/history/sessions/${ID}`).ok(reply())
    await settle()
    const olderEntry = last('GET', '/api/history/entry')
    // A newer read is asked while the older read's entry is on its way; the older entry lands first, then the newer read's.
    void page.load()
    await settle()
    olderEntry.ok({ entry: { ...entry.entry, availability: { state: 'listed', words: 'listed', resume: { offered: 'no' } } } })
    await settle()
    expect(page.availability.value).toBe(null)
    last('GET', `/api/history/sessions/${ID}`).ok(reply())
    await settle()
    last('GET', '/api/history/entry').ok(entry)
    await settle()
    expect(page.availability.value?.state).toBe('record')
  })

  it('says the outcome is unknown when the server started the session but both replies were lost', async () => {
    const page = await drawn()
    page.step.value = 'ask'
    const resumed = page.resume(false)
    await settle()
    // The first send starts the session on the server; its reply is lost, and so is the reply to the same id sent again.
    last('POST', `/api/history/sessions/${ID}/resume`).fail(new ApiError(0, 'network', 'Cannot reach the server'))
    await settle()
    expect(count('POST', `/api/history/sessions/${ID}/resume`)).toBe(2)
    last('POST', `/api/history/sessions/${ID}/resume`).fail(new ApiError(0, 'network', 'Cannot reach the server'))
    await resumed
    await settle()
    expect(push).not.toHaveBeenCalled()
    expect(toasts.map((t) => t.title)).toEqual(['Whether it resumed is not known'])
    expect(toasts[0]!.description).toMatch(/The outcome is unknown/)
    // The record is read again: its receipts, the started one among them, say what happened.
    expect(count('GET', `/api/history/sessions/${ID}`)).toBe(2)
    last('GET', `/api/history/sessions/${ID}`).ok({ ...reply(), record: { ...reply().record, seq: 2, resumes: [receipt('x', 'started')] } })
    await settle()
    expect(page.record.value?.resumes?.[0]?.state).toBe('started')
    expect([page.step.value, page.resuming.value]).toEqual([null, false])
  })
})
