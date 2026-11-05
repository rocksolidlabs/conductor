import { describe, expect, it, vi } from 'vitest'
import { asOpenEvent, browserWords, offerWords, openAction, openOffered, outcomeWords, routeOpen, type OpenRoute } from './opens'
import type { OpenEvent } from './protocol'

const pending: OpenEvent = { id: 'op-1', sessionId: 's1', url: 'https://claude.ai/artifact/0f1e', browser: { name: 'edge', profile: 'Profile 2' }, state: 'pending' }
const offered: OpenEvent = { ...pending, state: 'offered' }

function route(bridge: OpenRoute['bridge'], o: { claimRefused?: boolean; resultRefused?: boolean } = {}) {
  const calls: string[] = []
  const r: OpenRoute = {
    bridge,
    claim: vi.fn(async (id: string) => {
      calls.push(`claim ${id}`)
      if (o.claimRefused) throw new Error('open_taken')
    }),
    result: vi.fn(async (id: string, outcome: string) => {
      calls.push(`result ${id} ${outcome}`)
      if (o.resultRefused) throw new Error('not_found')
    }),
    offer: vi.fn((ev: OpenEvent) => {
      calls.push(`offer ${ev.url}`)
    }),
    warn: vi.fn((w: { title: string }) => {
      calls.push(`warn ${w.title}`)
    }),
  }
  return { r, calls }
}

describe('which way a link goes', () => {
  it('a page that can open claims a pending link; a plain tab leaves it; every page offers an offered one', () => {
    expect(openAction(pending, true)).toBe('claim')
    expect(openAction(pending, false)).toBe('none')
    expect(openAction(offered, true)).toBe('offer')
    expect(openAction(offered, false)).toBe('offer')
  })

  it('in the desktop app: claimed, opened once through the bridge with its agent\'s browser, and told', async () => {
    const openUrl = vi.fn(async () => ({ outcome: 'browser' }))
    const { r, calls } = route({ openUrl })
    await routeOpen(pending, r)
    expect(openUrl).toHaveBeenCalledTimes(1)
    expect(openUrl).toHaveBeenCalledWith('https://claude.ai/artifact/0f1e', { name: 'edge', profile: 'Profile 2' })
    expect(calls).toEqual(['claim op-1', 'result op-1 browser'])
  })

  it('says a browser not installed on this computer, the link in the default one', async () => {
    const { r, calls } = route({ openUrl: async () => ({ outcome: 'missing' }) })
    await routeOpen(pending, r)
    expect(calls).toEqual(['claim op-1', 'result op-1 missing', 'warn Edge is not installed on this computer'])
  })

  it('opens nothing when another window of the app took the link', async () => {
    const openUrl = vi.fn(async () => ({ outcome: 'browser' }))
    const { r, calls } = route({ openUrl }, { claimRefused: true })
    await routeOpen(pending, r)
    expect(openUrl).not.toHaveBeenCalled()
    expect(calls).toEqual(['claim op-1'])
  })

  it('tells the server a link the bridge refused as offered, which then offers it to every workbench, this one among them', async () => {
    const { r, calls } = route({ openUrl: async () => Promise.reject(new Error('refused')) })
    await routeOpen(pending, r)
    expect(calls).toEqual(['claim op-1', 'result op-1 offered'])
  })

  it('offers a link the bridge refused itself when the server cannot be told', async () => {
    const { r, calls } = route({ openUrl: async () => Promise.reject(new Error('refused')) }, { resultRefused: true })
    await routeOpen(pending, r)
    expect(calls).toEqual(['claim op-1', 'result op-1 offered', 'offer https://claude.ai/artifact/0f1e'])
  })

  it('in a plain tab, or an app older than the bridge\'s openUrl, a pending link starts nothing and claims nothing', async () => {
    for (const bridge of [null, {}]) {
      const { r, calls } = route(bridge)
      await routeOpen(pending, r)
      expect(calls).toEqual([])
      await routeOpen(offered, r)
      expect(calls).toEqual(['offer https://claude.ai/artifact/0f1e'])
    }
  })
})

describe('Open on a link offered', () => {
  it('in the desktop app, opens it through the bridge in its agent\'s browser and says a browser not installed', async () => {
    const openUrl = vi.fn(async () => ({ outcome: 'missing' }))
    const windowOpen = vi.fn()
    const warn = vi.fn()
    await openOffered(offered, { openUrl }, windowOpen, warn)
    expect(openUrl).toHaveBeenCalledWith('https://claude.ai/artifact/0f1e', { name: 'edge', profile: 'Profile 2' })
    expect(warn).toHaveBeenCalledWith({ title: 'Edge is not installed on this computer', description: 'The link opened in the default browser.' })
    expect(windowOpen).not.toHaveBeenCalled()
  })
  it('says nothing for a link opened in its browser, or one the bridge refused', async () => {
    const warn = vi.fn()
    await openOffered(offered, { openUrl: async () => ({ outcome: 'browser' }) }, vi.fn(), warn)
    await openOffered(offered, { openUrl: async () => Promise.reject(new Error('refused')) }, vi.fn(), warn)
    expect(warn).not.toHaveBeenCalled()
  })
  it('in a plain tab, or an app older than the bridge\'s openUrl, opens a tab of this browser', async () => {
    for (const bridge of [null, {}]) {
      const windowOpen = vi.fn()
      await openOffered(offered, bridge, windowOpen, vi.fn())
      expect(windowOpen).toHaveBeenCalledWith('https://claude.ai/artifact/0f1e')
    }
  })
})

describe('the open event and its words', () => {
  it('reads an open event, and nothing else', () => {
    expect(asOpenEvent({ ...pending })).toEqual(pending)
    expect(asOpenEvent({ id: 'op-2', sessionId: 's', url: 'http://localhost:3000/', state: 'offered' })).toEqual({ id: 'op-2', sessionId: 's', url: 'http://localhost:3000/', state: 'offered' })
    // A browser the page does not know is the default one.
    expect(asOpenEvent({ ...pending, browser: { name: 'opera' } })).toEqual({ id: 'op-1', sessionId: 's1', url: pending.url, state: 'pending' })
    for (const bad of [null, 'x', { ...pending, state: 'done' }, { ...pending, url: 'javascript:alert(1)' }, { ...pending, url: 'file:///etc/passwd' }, { ...pending, id: '' }, { ...pending, url: 'https://e.test/' + 'a'.repeat(2048) }]) {
      expect(asOpenEvent(bad)).toBe(null)
    }
  })
  it('names the browser and the profile, and says only what went wrong', () => {
    expect(browserWords({ name: 'edge', profile: 'Profile 2' })).toBe('Edge (Profile 2)')
    expect(browserWords(undefined)).toBe('the default browser')
    expect(outcomeWords('browser', pending.browser)).toBe(null)
    expect(outcomeWords('default', undefined)).toBe(null)
    expect(outcomeWords('failed', { name: 'brave' })?.title).toBe('Brave did not start')
    expect(offerWords('work agent', offered, false)).toEqual({ title: 'work agent opened a link', description: 'https://claude.ai/artifact/0f1e' })
    expect(offerWords('work agent', offered, true).description).toBe('https://claude.ai/artifact/0f1e Open shows it in Edge (Profile 2).')
  })
})
