import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import type { CandidateReceipt } from './useDispatch'
import type { RunStep } from './useSessions'
// Imported with the file, not inside the test: a module loaded in a test is loaded on the test's 5 s, which a loaded machine spends on the
// load alone. Its auto-imports are globals read when it is called, stubbed below.
import { useReceiptRead } from './useReceiptRead'

// A receipt opened in full (Program item DS2, PR-11): its step and its log read when it opens and again when the receipt moves (a check
// that finishes), never when a candidates read hands over the same receipt as new objects, so the log shown and Save stay; against
// replies the test lets go. useReceiptRead reads Nuxt's auto-imports (Vue's, useSessions) as globals: each is stubbed.

let stepsSent: number
let logsSent: number
let failLogs: boolean

beforeEach(() => {
  stepsSent = 0
  logsSent = 0
  failLogs = false
  for (const k of ['ref', 'watch'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('useSessions', () => ({
    runSteps: async () => {
      stepsSent++
      return [{ phase: 'check', id: 'unit', k: 1, state: 'finished', argv: ['go', 'test'] }] as RunStep[]
    },
    stepLog: async () => {
      logsSent++
      if (failLogs) throw new Error('the server could not be reached')
      return { text: `log ${logsSent}`, bytes: new Uint8Array(), sha256: 'ab'.repeat(32) }
    },
  }))
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function receipt(p: Partial<CandidateReceipt> = {}): CandidateReceipt {
  return { phase: 'check', id: 'unit', k: 1, where: 'native', state: 'finished', exit: 0, timedOut: false, killed: false, stable: true, startedAt: '2026-11-01T10:00:00Z', finishedAt: '2026-11-01T10:00:02Z', logSha256: 'ab'.repeat(32), logBytes: 12, logDropped: false, ...p }
}
const settle = () => new Promise((r) => setTimeout(r, 0))

describe("a receipt's full view", () => {
  it('keep the log opened when the receipt is handed over again unchanged, and read again when it moves', async () => {
    const open = vue.ref(true)
    const shown = vue.ref<CandidateReceipt | null>(receipt({ state: 'running', finishedAt: undefined, logSha256: undefined, logBytes: 0 }))
    const r = useReceiptRead({ open: () => open.value, runId: () => 'crew-0000aaaa', task: () => 'build', attempt: () => 1, receipt: () => shown.value })
    await settle()
    expect([stepsSent, logsSent, r.log.value?.text]).toEqual([1, 1, 'log 1'])
    // A candidates read that answers the same: new objects, the same receipt. Nothing is read again; a read that would fail loses nothing.
    failLogs = true
    shown.value = { ...shown.value! }
    await settle()
    expect([stepsSent, logsSent, r.log.value?.text, r.logError.value]).toEqual([1, 1, 'log 1', ''])
    // The check finished: read again.
    failLogs = false
    shown.value = receipt()
    await settle()
    expect([stepsSent, logsSent, r.log.value?.text]).toEqual([2, 2, 'log 2'])
    // Closed, then opened again: read for the new opening.
    open.value = false
    await settle()
    expect(r.log.value).toBeNull()
    open.value = true
    await settle()
    expect([stepsSent, logsSent, r.log.value?.text]).toEqual([3, 3, 'log 3'])
  })
})
