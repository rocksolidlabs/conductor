import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { LINK_ELSEWHERE, linkRefAccess, REF_ACCESS, refReadShown, type RefAccess } from '~/utils/refAccess'
import type { LaneEvidence } from '~/utils/transport/decide'
import { refPage } from '~/utils/brakeman'
// Imported with the file, not inside a test: a module loaded in a test is loaded on the test's 5 s, which a loaded machine spends on the
// load alone. Their auto-imports are globals read when they are called, stubbed below.
import { useRefAccess, useRefReceipt } from './useRefAccess'
import { useDecisionRun } from './useDecisions'

// BrakemanRefChip and BrakemanReceipt's reads (docs/plans/decision-inbox.md, PR-4, "DR1's card on a link"): with an access provided
// (the join page's), where a chip opens and what its record reads are the access's, and useApi is never asked; with none, today's:
// the workbench's page for the ref and its record read over HTTP with the token. DR1's card's run (useDecisionRun) answers from the
// provided access too, reading nothing. Each is used inside an app's context, as a component's setup is (provide/inject).

let apiCalls: string[]
let reads: string[]

beforeEach(() => {
  apiCalls = []
  reads = []
  for (const k of ['ref', 'computed', 'watch'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('useApi', () => {
    apiCalls.push('useApi')
    return {
      request: async (path: string) => {
        reads.push(`request ${path}`)
        return path.includes('/comments') ? { comments: [{ n: 3, body: 'Looks right.' }] } : { receipt: { status: 'done' } }
      },
      requestText: async (path: string) => {
        reads.push(`text ${path}`)
        return { text: 'the check looked', headers: new Headers({ 'X-Conductor-Log-SHA256': 'ab'.repeat(32) }) }
      },
    }
  })
  vi.stubGlobal('useAttention', () => {
    apiCalls.push('useAttention')
    return { runOf: () => undefined, refreshRun: async () => null }
  })
})
afterEach(() => vi.unstubAllGlobals())

/** Runs f as a component's setup would, with `access` provided by an ancestor (none when undefined). */
function within<T>(access: RefAccess | undefined, f: () => T): T {
  const app = vue.createApp({ render: () => null })
  if (access) app.provide(REF_ACCESS, access)
  return app.runWithContext(f)
}

const settle = () => new Promise((r) => setTimeout(r, 0))
const check = 'check:crew-0000aaaa-11112222/build/1/until/1'

describe("an evidence chip's access", () => {
  it("is the provided one when a page provides it: its route and its read, and no useApi call", async () => {
    const opened: string[] = []
    const asked: Array<[string]> = []
    const link = linkRefAccess({
      evidence: async (ref) => {
        asked.push([ref])
        return { ref, kind: 'log', tail: 'the check looked', exit: 1, sha256: 'cd'.repeat(32) }
      },
      openSession: (ref) => opened.push(ref),
    })
    const a = within(link, () => useRefAccess())
    expect(a).toBe(link)
    // A session's chip opens the member's terminal tab; a check's has no page, and reads its log through the lane; others say where.
    const s = a.route('session:0123456789abcdef')
    expect(s && 'open' in s).toBe(true)
    if (s && 'open' in s) s.open()
    expect(opened).toEqual(['session:0123456789abcdef'])
    expect(a.route(check)).toBeNull()
    expect(a.route('ticket:CND-12')).toEqual({ words: LINK_ELSEWHERE })
    expect(a.read('ticket:CND-12')).toBeNull()
    await expect(a.read(check)!()).resolves.toEqual({ kind: 'text', text: 'the check looked', sha256: 'cd'.repeat(32) })
    expect(asked).toEqual([[check]])
    expect(apiCalls).toEqual([])
    expect(reads).toEqual([])
  })

  it("is today's with none provided: the workbench's page for the ref, and its record read with the token", async () => {
    const a = within(undefined, () => useRefAccess())
    expect(apiCalls).toEqual(['useApi'])
    expect(a.route('run:crew-0000aaaa-11112222')).toEqual({ to: refPage('run:crew-0000aaaa-11112222')! })
    expect(a.route('nonsense')).toBeNull()
    await expect(a.read(check)!()).resolves.toEqual({ kind: 'text', text: 'the check looked', sha256: 'ab'.repeat(32) })
    expect(reads).toEqual([`text /api/runs/crew-0000aaaa-11112222/tasks/build/attempts/1/steps/until/1/log`])
    expect(a.runHeld).toBeUndefined()
  })

  it("says what a link's log read shows when it is not the log its SHA-256 is of: a cut log's last 4 KiB, a dropped log's kept end", async () => {
    const sha = 'ef'.repeat(32)
    const entries: Record<string, LaneEvidence> = {
      cut: { ref: check, kind: 'log', tail: 'the end of a long log', cut: true, bytes: 70_000, sha256: sha, exit: 1 },
      dropped: { ref: check, kind: 'log', tail: 'what the receipt kept', bytes: 70_000, sha256: sha, exit: 1, dropped: true },
      whole: { ref: check, kind: 'log', tail: 'the check looked', bytes: 17, sha256: sha, exit: 1 },
    }
    for (const [name, entry] of Object.entries(entries)) {
      const link = linkRefAccess({ evidence: async () => entry, openSession: () => {} })
      const r = within(link, () => useRefReceipt({ ref: () => check, resolution: () => undefined }))
      await r.read()
      expect([r.text.value, r.sha256.value], name).toEqual([entry.kind === 'log' ? entry.tail : '', sha])
      if (name === 'cut') expect(r.shown.value).toBe("Shown is the log's last 4 KiB of its 70,000 bytes: the whole log is read on the desktop or the LAN.")
      if (name === 'dropped') expect(r.shown.value).toContain('The log was not kept')
      // A log shown whole is shown as it was: nothing said above it.
      if (name === 'whole') expect(r.shown.value).toBe('')
    }
    // The read keeps the lane's facts: whether it was cut, its size, whether it was dropped.
    const cut = await linkRefAccess({ evidence: async () => entries.cut!, openSession: () => {} }).read(check)!()
    expect(cut).toEqual({ kind: 'text', text: 'the end of a long log', sha256: sha, cut: true, bytes: 70_000 })
    expect(refReadShown({ kind: 'text', text: 'x', dropped: true, cut: true })).toContain('not kept')
    // Today's read, the whole log over HTTP, says nothing above it.
    const r = within(undefined, () => useRefReceipt({ ref: () => check, resolution: () => undefined }))
    await r.read()
    expect([r.text.value, r.shown.value]).toEqual(['the check looked', ''])
    expect(apiCalls).toEqual(['useApi'])
  })

  it("refuses a link's log read that answers for another entry", async () => {
    const a = linkRefAccess({ evidence: async () => ({ ref: 'check:crew-0000aaaa-11112222/build/1/other/1', kind: 'log', tail: '', exit: 0 }), openSession: () => {} })
    await expect(a.read(check)!()).rejects.toThrow('another entry')
  })
})

describe("a chip's record read", () => {
  it("reads through the provided access, never useApi, and shows a reply only in the opening it was asked for", async () => {
    const replies: Array<(v: { kind: 'text'; text: string }) => void> = []
    const access: RefAccess = { route: () => null, read: () => () => new Promise((resolve) => replies.push(resolve)), runHeld: () => undefined }
    const r = within(access, () => useRefReceipt({ ref: () => check, resolution: () => undefined }))
    void r.read()
    void r.read()
    expect(replies).toHaveLength(2)
    // The second opening's reply first, then the first's: the first, late, is dropped.
    replies[1]!({ kind: 'text', text: 'second' })
    await settle()
    replies[0]!({ kind: 'text', text: 'first' })
    await settle()
    expect([r.open.value, r.loading.value, r.text.value]).toEqual([true, false, 'second'])
    expect(apiCalls).toEqual([])
  })

  it("reads through today's access with none provided, a comment by its ticket's route", async () => {
    const r = within(undefined, () => useRefReceipt({ ref: () => 'comment:CND-12/00000000000000a3', resolution: () => ({ ref: 'comment:CND-12/00000000000000a3', seen: {}, now: { url: '/api/dispatch/tickets/CND-12/comments?around=3' } }) }))
    await r.read()
    expect(r.comment.value).toMatchObject({ n: 3, body: 'Looks right.' })
    expect(apiCalls).toEqual(['useApi'])
    expect(reads.length).toBe(1)
  })
})

describe("DR1's card's run on a link", () => {
  it('answers from the provided access and reads nothing', () => {
    const held = within({ route: () => null, read: () => null, runHeld: () => undefined }, () => useDecisionRun(() => 'crew-0000aaaa-11112222'))
    expect(held.value).toBeUndefined()
    expect(apiCalls).toEqual([])
  })
})
