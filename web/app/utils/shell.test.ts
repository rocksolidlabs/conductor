import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ConductorShell, ShellDraft, ShellItem, ShellProvider, ShellState } from './shell'
import { boundCode, boundLine, boundPage, decisionIdOf, decisionPath, endingWords, withinWait, notificationWords, providerName, readBridge, readRefusal, readSignIn, readState, readWords, resetShellBridge, SHELL_BOUNDS, shellBridge, shellRedirect, timeLeft } from './shell'

// The phone shell's bridge as the workbench reads it (docs/plans/phone-shell.md, OSS-1b, Tests per surface): the reader, every
// provider string bounded where it is read, and a fake provider written against the whole of v1 (the typecheck holds it to it).

afterEach(() => {
  vi.restoreAllMocks()
  resetShellBridge()
})

/** A fake provider of the whole v1 interface: its type is the check. */
function fakeProvider(): ShellProvider {
  const draft: ShellDraft = { id: 'i1', responseId: 'r1', rev: 1, state: 'editing', savedAt: '2026-11-19T10:00:00Z' }
  const item = { id: 'i1', rows: [], owner: { name: 'desk' }, asOf: '2026-11-19T10:00:00Z', offline: false } as unknown as ShellItem
  return {
    name: 'Fake',
    state: async (): Promise<ShellState> => ({ signedIn: false, notifications: 'unknown' }),
    onState: () => () => {},
    signIn: async () => ({ words: 'not now' }),
    cancelSignIn: async () => {},
    signOut: async () => {},
    enableNotifications: async () => 'on',
    test: async () => ({ words: 'sent' }),
    route: async () => null,
    inbox: {
      list: async () => ({ items: [item], asOf: item.asOf }),
      item: async () => item,
      respond: async () => ({ outcome: 'kept', item }),
      answerOf: async () => ({ state: 'none' }),
      resolve: async () => ({ id: 'i1' }),
      watch: () => () => {},
    },
    drafts: { get: async () => draft, list: async () => [draft], put: async () => ({ ok: true }), remove: async () => {} },
  }
}

function fakeBridge(extra: Partial<ConductorShell> = {}): ConductorShell {
  return {
    v: 1,
    platform: 'web',
    push: { permission: async () => 'denied', request: async () => 'denied', onTap: () => () => {} },
    links: { onOpen: () => () => {} },
    openServer: async () => ({ ok: true }),
    servers: { recent: async () => [], forget: async () => {} },
    provider: fakeProvider(),
    register: () => {},
    ...extra,
  }
}

describe('the reader', () => {
  it('answers null outside a shell, and reads the bridge once', () => {
    expect(shellBridge({})).toBeNull()
    const b = fakeBridge()
    // Read once: a bridge put in place after the first read is not seen.
    expect(shellBridge({ conductorShell: b })).toBeNull()
    resetShellBridge()
    expect(shellBridge({ conductorShell: b })).toBe(b)
    expect(shellBridge({})).toBe(b)
  })

  it('ignores a bridge of another version, or one lacking a part of v1, with one console line', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(readBridge({ ...fakeBridge(), v: 2 })).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
    expect(String(warn.mock.calls[0]![0])).toContain('version 2')
    expect(readBridge({ ...fakeBridge(), openServer: undefined })).toBeNull()
    expect(warn).toHaveBeenCalledTimes(2)
    expect(readBridge('a string')).toBeNull()
    expect(readBridge(fakeBridge())).not.toBeNull()
    expect(warn).toHaveBeenCalledTimes(3)
  })

  it('keeps a page inside a shell on /shell, and one outside a shell off it', () => {
    expect(shellRedirect('/', true)).toBe('/shell')
    expect(shellRedirect('/runs/x', true)).toBe('/shell')
    expect(shellRedirect('/shellfish', true)).toBe('/shell')
    expect(shellRedirect('/shell', true)).toBeNull()
    expect(shellRedirect('/shell/decisions/a', true)).toBeNull()
    expect(shellRedirect('/shell', false)).toBe('/')
    expect(shellRedirect('/shell/settings', false)).toBe('/')
    expect(shellRedirect('/shellfish', false)).toBeNull()
    expect(shellRedirect('/', false)).toBeNull()
  })
})

describe('every provider string, bounded where it is read', () => {
  const n = (k: number, c = 'a') => c.repeat(k)

  it("the provider's name: 40 characters, one past cut, a control character a space, markup kept as text", () => {
    expect(providerName({ name: n(40) })).toBe(n(40))
    expect([...providerName({ name: n(41) })]).toHaveLength(40)
    expect(providerName({ name: n(41) }).endsWith('…')).toBe(true)
    expect(providerName({ name: 'Desk\nPhone' })).toBe('Desk Phone')
    expect(providerName({ name: '<b>Desk</b>' })).toBe('<b>Desk</b>')
    expect(providerName({ name: '' })).toBe('The provider')
    expect(providerName(null)).toBe('The provider')
    expect(providerName({ name: 7 as unknown as string })).toBe('The provider')
  })

  it("a state's who (80) and words (500), its notifications and its time", () => {
    const s = readState({ signedIn: true, who: n(80), words: n(500), notifications: 'on', lastNotifiedAt: '2026-11-19T10:00:00Z', ending: true })
    expect(s).toEqual({ signedIn: true, who: n(80), words: n(500), notifications: 'on', lastNotifiedAt: '2026-11-19T10:00:00Z', ending: true })
    const past = readState({ signedIn: true, who: n(81), words: n(501), notifications: 'loud', lastNotifiedAt: 'yesterday', ending: 'yes' })
    expect([...past.who!]).toHaveLength(80)
    expect([...past.words!]).toHaveLength(500)
    expect(past.notifications).toBe('unknown')
    expect(past.lastNotifiedAt).toBeUndefined()
    expect(past.ending).toBeUndefined()
    expect(readState({ signedIn: 'true', who: 'a\u0007b' })).toEqual({ signedIn: false, who: 'a b', notifications: 'unknown' })
    expect(readState(null)).toEqual({ signedIn: false, notifications: 'unknown' })
  })

  it("a sign-in's code (16), its page (https://, 200 bytes) and its words (500)", () => {
    const page = `https://example.test/${n(200 - 'https://example.test/'.length)}`
    expect(page).toHaveLength(200)
    expect(readSignIn({ code: n(16, 'C'), page, expiresAt: '2026-11-19T10:10:00Z' })).toEqual({ code: n(16, 'C'), page, expiresAt: '2026-11-19T10:10:00Z' })
    for (const bad of [
      { code: n(17, 'C'), page, expiresAt: '2026-11-19T10:10:00Z' },
      { code: 'AB\nCD', page, expiresAt: '2026-11-19T10:10:00Z' },
      { code: 'ABCD', page: `${page}x`, expiresAt: '2026-11-19T10:10:00Z' },
      { code: 'ABCD', page: 'http://example.test/in', expiresAt: '2026-11-19T10:10:00Z' },
      { code: 'ABCD', page: 'javascript:alert(1)', expiresAt: '2026-11-19T10:10:00Z' },
      { code: 'ABCD', page, expiresAt: 'soon' },
    ]) {
      const r = readSignIn(bad)
      expect('words' in r && !('code' in r), JSON.stringify(bad)).toBe(true)
    }
    expect(readSignIn({ words: n(501) })).toEqual({ words: `${n(499)}…` })
    expect(readSignIn({})).toEqual({ words: 'No sign-in can begin now.' })
    expect(boundPage(page)).toBe(page)
    expect(boundPage('https://a b.test/')).toBeNull()
  })

  it("a refusal's code (32 bytes of a-z and _) and its words (500)", () => {
    expect(readRefusal({ code: n(32, 'x'), words: n(500) })).toEqual({ code: n(32, 'x'), words: n(500) })
    expect(readRefusal({ code: n(33, 'x'), words: 'w' }).code).toBe('refused')
    expect(readRefusal({ code: 'Not_Visible', words: 'w' }).code).toBe('refused')
    expect(readRefusal({ code: 'not-visible', words: 'w' }).code).toBe('refused')
    expect([...readRefusal({ code: 'x', words: n(501) }).words]).toHaveLength(500)
    expect(readRefusal(new Error('boom'), 'Fallback.')).toEqual({ code: 'refused', words: 'Fallback.' })
    expect(boundCode('not_visible')).toBe('not_visible')
    expect(readWords({ words: '<i>sent</i>' }, 'x')).toBe('<i>sent</i>')
    expect(readWords({}, 'Fallback.')).toBe('Fallback.')
  })

  it('cut a line by characters, never inside one', () => {
    expect(boundLine('ab😀cd', 4)).toBe('ab😀…')
    expect(boundLine(undefined, 4)).toBe('')
    expect(SHELL_BOUNDS.words).toBe(500)
  })
})

describe("the screens' words", () => {
  it("say a code's time left, and its end", () => {
    const now = Date.parse('2026-11-19T10:00:00Z')
    expect(timeLeft('2026-11-19T10:09:30Z', now)).toBe('Expires in 9 min')
    expect(timeLeft('2026-11-19T10:00:30Z', now)).toBe('Expires in less than a minute')
    expect(timeLeft('2026-11-19T10:00:00Z', now)).toBe('')
    expect(timeLeft('nonsense', now)).toBe('')
  })

  it("say the notifications' state with the last one received, and a sign-out not yet confirmed", () => {
    expect(notificationWords({ notifications: 'on', lastNotifiedAt: '2026-11-19T10:00:00Z' }, () => '10:00')).toBe('On · the last received at 10:00')
    expect(notificationWords({ notifications: 'denied' }, () => '')).toBe('Turned off in the phone’s settings')
    expect(endingWords('Desk')).toBe('Signed out on this phone; Desk will be told when it can be reached')
  })
})

describe('a wait on the provider', () => {
  it('is refused unreachable, in its words, once its bound has passed with no answer; an answer before it stands', async () => {
    vi.useFakeTimers()
    try {
      const never = withinWait(new Promise(() => {}), 'Desk relay did not answer', 1000)
      const seen = never.catch((e) => e)
      await vi.advanceTimersByTimeAsync(999)
      let settled = false
      void seen.then(() => (settled = true))
      await vi.advanceTimersByTimeAsync(0)
      expect(settled).toBe(false)
      await vi.advanceTimersByTimeAsync(1)
      expect(await seen).toEqual({ code: 'unreachable', words: 'Desk relay did not answer' })
      expect(await withinWait(Promise.resolve(7), 'x', 1000)).toBe(7)
    } finally {
      vi.useRealTimers()
    }
  })
})

describe("a decision's page address", () => {
  // The longest id the contract allows is 64 bytes.
  const longest = 'x'.repeat(62) + 'é'
  const ids = ['i1', 'org/repo#12', 'a:b', 'a~b', 'a.b', 'a b', '..', '%2F', '?q=1', 'é', 'Ünïcødé 決定', '😀', 'a\\b', longest, 'x'.repeat(64)]

  it('carries any id the shell takes in the alphabet the content rules let through, and reads it back', () => {
    expect(new TextEncoder().encode(longest).length).toBe(64)
    for (const id of ids) {
      const path = decisionPath(id)
      expect(path).toMatch(/^\/shell\/decisions\/[A-Za-z0-9_-]+$/)
      expect(decisionIdOf(path.slice('/shell/decisions/'.length))).toBe(id)
    }
    // The boot check's probe asks the app for exactly this segment (mobile/ios, BootCheck.decisionSegment).
    expect(decisionPath('org/repo#12 a:b~é')).toBe('/shell/decisions/b3JnL3JlcG8jMTIgYTpifsOp')
  })

  it('reads a segment that does not read back as no decision', () => {
    for (const bad of [undefined, 7, '', 'a', 'a=', 'aTE=', 'a+b', 'a/b', '%41', 'aTF', '_w', 'wA', 'gA', 'eA'.repeat(44), ['aTE']]) {
      expect(decisionIdOf(bad), String(bad)).toBeNull()
    }
    // An id the shell does not take: past 64 bytes, or with a control character.
    expect(decisionIdOf(decisionPath('x'.repeat(65)).slice('/shell/decisions/'.length))).toBeNull()
    expect(decisionIdOf(decisionPath('a\nb').slice('/shell/decisions/'.length))).toBeNull()
  })
})
