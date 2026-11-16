import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, effectScope, nextTick, ref } from 'vue'
import { useWorkbenchToken } from '~/composables/useWorkbenchToken'
import { connectShown, refusedShown, tokenDialog } from './workbenchGate'

// The gate's open state kept with the workbench token's ask (WorkbenchTokenGate.vue runs followAsk as it mounts; an effect scope
// stands for its mount here, stopped for its unmount). The store is the app's own over a Nuxt app of this test's; tokens are invented.
const KEPT = 'kept-token-0123456789'
const KEY = 'conductor.workbenchToken'

describe('the workbench token dialog', () => {
  let local: Map<string, string>
  beforeEach(() => {
    local = new Map([[KEY, KEPT]])
    vi.stubGlobal('localStorage', { getItem: (k: string) => local.get(k) ?? null, setItem: (k: string, v: string) => void local.set(k, v), removeItem: (k: string) => void local.delete(k) })
    vi.stubGlobal('computed', computed)
    const app = {}
    vi.stubGlobal('useNuxtApp', () => app)
  })
  afterEach(() => vi.unstubAllGlobals())

  /** A dialog mounted now: its open state and draft. */
  function mount() {
    const scope = effectScope()
    const open = ref(false)
    const draft = ref('')
    scope.run(() => tokenDialog(open, draft, useWorkbenchToken()))
    return { open, draft, unmount: () => scope.stop() }
  }

  it('is not opened by a refusal: the connect page asks then', async () => {
    const admin = useWorkbenchToken()
    const dialog = mount()
    admin.answered(KEPT, false)
    await nextTick()
    expect(admin.needsToken.value).toBe(true)
    expect(dialog.open.value).toBe(false)
  })

  it('opened by the person, starts from the kept token; closed, the ask ends', async () => {
    const admin = useWorkbenchToken()
    const dialog = mount()
    admin.answered(KEPT, false)
    dialog.open.value = true
    await nextTick()
    expect(dialog.draft.value).toBe(KEPT)
    dialog.open.value = false
    await nextTick()
    expect(admin.needsToken.value).toBe(false)
  })
})

describe('the connect page', () => {
  const browser = { desktop: false, bridged: true }
  it('shows in a browser with no token, and when the held token was refused', () => {
    expect(connectShown({ ...browser, hasToken: false, standing: '' })).toBe(true)
    expect(connectShown({ ...browser, hasToken: true, standing: 'refused' })).toBe(true)
  })
  it('does not show for a held token the server has not refused, nor while a link\'s token is tried or, refused, the page loads again', () => {
    expect(connectShown({ ...browser, hasToken: true, standing: '' })).toBe(false)
    expect(connectShown({ ...browser, hasToken: true, standing: 'trying' })).toBe(false)
    expect(connectShown({ ...browser, hasToken: true, standing: 'dropped' })).toBe(false)
  })
  it('in the desktop app, waits for its bridge: no page before it answered, then as in a browser', () => {
    // The case a rule keyed on the token alone gets wrong: it would flash the page while the shell hands over its token.
    expect(connectShown({ desktop: true, bridged: false, hasToken: false, standing: '' })).toBe(false)
    expect(connectShown({ desktop: true, bridged: true, hasToken: false, standing: '' })).toBe(true)
    expect(connectShown({ desktop: true, bridged: true, hasToken: true, standing: 'refused' })).toBe(true)
    expect(connectShown({ desktop: true, bridged: true, hasToken: true, standing: '' })).toBe(false)
  })
})

describe("the page to connect's words", () => {
  it('say a token was refused only when this page held one; a page that holds none is asked for one', () => {
    expect(refusedShown({ hasToken: true, standing: 'refused' })).toBe(true)
    // A read sent with no token is answered 401 too, and marks the standing: no token was refused.
    expect(refusedShown({ hasToken: false, standing: 'refused' })).toBe(false)
    expect(refusedShown({ hasToken: true, standing: '' })).toBe(false)
    expect(refusedShown({ hasToken: false, standing: '' })).toBe(false)
  })
})
