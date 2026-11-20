import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { REF_ACCESS } from './refAccess'
import { SHELL_ELSEWHERE, shellRefAccess } from './transport/shell'
import { useRefAccess, useRefReceipt } from '../composables/useRefAccess'
import { useDecisionRunInfo } from '../composables/useDecisions'

// The shell's REF_ACCESS (docs/plans/phone-shell.md, OSS-2b, Tests per surface, shell-ref-access.test.ts): an evidence chip, a
// receipt and DR1's card's run line under it make no useApi call, and show the words.

let apiCalls: string[]
beforeEach(() => {
  apiCalls = []
  for (const k of ['ref', 'computed', 'watch', 'inject', 'hasInjectionContext'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('useApi', () => {
    apiCalls.push('useApi')
    return { request: async () => ({}), requestText: async () => ({ text: '', headers: new Headers() }) }
  })
  vi.stubGlobal('useAttention', () => {
    apiCalls.push('useAttention')
    return { runOf: () => undefined, refreshRun: async () => null }
  })
})
afterEach(() => vi.unstubAllGlobals())

function within<T>(f: () => T): T {
  const app = vue.createApp({ render: () => null })
  app.provide(REF_ACCESS, shellRefAccess())
  return app.runWithContext(f)
}

describe("the shell's evidence access", () => {
  it('routes every ref to its words, reads none, and does not know whether a run runs', () => {
    const a = shellRefAccess()
    for (const ref of ['check:crew-0000aaaa-11112222/build/1/until/1', 'session:s1', 'run:crew-0000aaaa-11112222', 'commit:x/y']) {
      expect(a.route(ref)).toEqual({ words: SHELL_ELSEWHERE })
      expect(a.read(ref)).toBeNull()
    }
    expect(a.runHeld!('crew-0000aaaa-11112222')).toBeUndefined()
  })

  it('makes no useApi call for a chip, a receipt, or the card’s run line', async () => {
    const ref = 'check:crew-0000aaaa-11112222/build/1/until/1'
    const chip = within(() => useRefAccess())
    expect(chip.route(ref)).toEqual({ words: SHELL_ELSEWHERE })
    const receipt = within(() => useRefReceipt({ ref: () => ref, resolution: () => undefined }))
    await receipt.read()
    expect(receipt.error.value).toBe('This record is not read here.')
    const run = within(() => useDecisionRunInfo(() => 'crew-0000aaaa-11112222'))
    expect(run.held.value).toBeUndefined()
    await new Promise((r) => setTimeout(r, 0))
    expect(apiCalls).toEqual([])
  })
})
