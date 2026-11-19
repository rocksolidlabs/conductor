import { describe, expect, it, vi } from 'vitest'
import { linkHost, SHOWN_ADDRESS_MAX, shownAddress, terminalLinkHandler, webLink } from './terminalLinks'

const MR = 'https://example.com/group/project/-/merge_requests/95'
const OTHER = ['file:///etc/passwd', 'javascript:void(0)', 'JavaScript:alert(1)', 'data:text/html,hi', 'vscode://file/etc/passwd', 'mailto:a@example.com', 'ftp://example.com/x', 'about:blank', '/etc/passwd', 'example.com', 'not a link', '']

function handler() {
  const calls: string[] = []
  const h = terminalLinkHandler({
    open: vi.fn((u: string) => calls.push(`open ${u}`)),
    opened: vi.fn((u: string) => calls.push(`opened ${u}`)),
    show: vi.fn((u: string | null) => calls.push(`show ${u}`)),
  })
  return { h, calls }
}

describe('a web link', () => {
  it('is an http or https address, kept as it was printed', () => {
    expect(webLink(MR)).toBe(MR)
    expect(webLink('http://127.0.0.1:4312/x')).toBe('http://127.0.0.1:4312/x')
    expect(webLink('HTTPS://EXAMPLE.COM/x')).toBe('HTTPS://EXAMPLE.COM/x')
  })

  it('is no other scheme, and nothing that is not an address', () => {
    for (const u of OTHER) expect(webLink(u), u).toBeNull()
  })

  it('is said by its host', () => {
    expect(linkHost(MR)).toBe('example.com')
    expect(linkHost('http://127.0.0.1:4312/x')).toBe('127.0.0.1:4312')
  })
})

describe('the address the hover shows', () => {
  it('is the whole address when it fits', () => {
    expect(shownAddress(MR)).toBe(MR)
    const exact = `https://example.com/${'a'.repeat(SHOWN_ADDRESS_MAX - 20)}`
    expect(shownAddress(exact)).toBe(exact)
  })

  it('is cut in its middle when it is very long, keeping its start (the host) and its end', () => {
    const long = `https://example.com/${'a'.repeat(300)}/the-end`
    const shown = shownAddress(long)
    expect(Array.from(shown)).toHaveLength(SHOWN_ADDRESS_MAX)
    expect(shown.startsWith('https://example.com/')).toBe(true)
    expect(shown.endsWith('/the-end')).toBe(true)
    expect(shown).toContain('…')
  })
})

describe("the terminal's link handler", () => {
  it('opens a web link once on a plain click, then tells the page', () => {
    const { h, calls } = handler()
    h.activate({ ctrlKey: false, metaKey: false }, MR)
    expect(calls).toEqual([`open ${MR}`, `opened ${MR}`])
  })

  it('does the same on Ctrl+click and ⌘+click', () => {
    for (const mods of [{ ctrlKey: true }, { metaKey: true }]) {
      const { h, calls } = handler()
      h.activate(mods, MR)
      expect(calls).toEqual([`open ${MR}`, `opened ${MR}`])
    }
  })

  it('shows the real address while the pointer is over a link, and nothing once it leaves', () => {
    const { h, calls } = handler()
    h.hover({}, MR)
    h.leave()
    expect(calls).toEqual([`show ${MR}`, 'show null'])
  })

  it('opens, tells and shows nothing for a link to another scheme', () => {
    const { h, calls } = handler()
    for (const u of OTHER) {
      h.activate({}, u)
      h.activate({ ctrlKey: true }, u)
      h.hover({}, u)
    }
    expect(calls).toEqual([])
  })

  it('tells xterm to make no link of a hyperlink to another scheme', () => {
    expect(handler().h.allowNonHttpProtocols).toBe(false)
  })
})
