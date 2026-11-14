import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, effectScope, nextTick, ref } from 'vue'
import { useWorkbenchToken } from '~/composables/useWorkbenchToken'
import { followAsk } from './workbenchGate'

// The gate's open state kept with the workbench token's ask (WorkbenchTokenGate.vue runs followAsk as it mounts; an effect scope
// stands for its mount here, stopped for its unmount). The store is the app's own over a Nuxt app of this test's; tokens are invented.
const KEPT = 'kept-token-0123456789'
const KEY = 'conductor.workbenchToken'

describe('the workbench token gate', () => {
  let local: Map<string, string>
  beforeEach(() => {
    local = new Map([[KEY, KEPT]])
    vi.stubGlobal('localStorage', { getItem: (k: string) => local.get(k) ?? null, setItem: (k: string, v: string) => void local.set(k, v), removeItem: (k: string) => void local.delete(k) })
    vi.stubGlobal('computed', computed)
    const app = {}
    vi.stubGlobal('useNuxtApp', () => app)
  })
  afterEach(() => vi.unstubAllGlobals())

  /** A gate mounted now: its open state and draft. */
  function mount() {
    const scope = effectScope()
    const open = ref(false)
    const draft = ref('')
    scope.run(() => followAsk(open, draft, useWorkbenchToken()))
    return { open, draft, unmount: () => scope.stop() }
  }

  it('mounted with no ask, stays closed until a refusal, which opens it with the kept token as its draft', async () => {
    const admin = useWorkbenchToken()
    const gate = mount()
    await nextTick()
    expect(gate.open.value).toBe(false)
    admin.answered(KEPT, false)
    await nextTick()
    expect(gate.open.value).toBe(true)
    expect(gate.draft.value).toBe(KEPT)
  })

  it('mounted while the ask stands (left open with its layout): open at once; cancelled, the ask ends and the next refusal opens it again', async () => {
    const admin = useWorkbenchToken()
    const first = mount()
    admin.answered(KEPT, false)
    await nextTick()
    expect(first.open.value).toBe(true)
    // A page of another layout: the gate goes with its layout while open, and nothing closed it.
    first.unmount()
    expect(admin.needsToken.value).toBe(true)
    const again = mount()
    expect(again.open.value).toBe(true)
    await nextTick()
    expect(again.draft.value).toBe(KEPT)
    again.open.value = false
    await nextTick()
    expect(admin.needsToken.value).toBe(false)
    admin.answered(KEPT, false)
    await nextTick()
    expect(again.open.value).toBe(true)
  })

  it('closes when a token is saved, and the ask is over', async () => {
    const admin = useWorkbenchToken()
    admin.answered(KEPT, false)
    const gate = mount()
    expect(gate.open.value).toBe(true)
    admin.set('another-token-0123456789')
    await nextTick()
    expect(gate.open.value).toBe(false)
    expect(admin.needsToken.value).toBe(false)
  })
})
