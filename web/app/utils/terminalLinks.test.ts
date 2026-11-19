import { describe, expect, it, vi } from 'vitest'
import { terminalLinkHandler, webLink } from './terminalLinks'

const MR = 'https://example.com/group/project/-/merge_requests/95'
const click = (mods: Partial<{ ctrlKey: boolean; metaKey: boolean }> = {}) => ({ ctrlKey: false, metaKey: false, ...mods })

describe('a web link', () => {
  it('is an http or https address, kept as it was printed', () => {
    expect(webLink(MR)).toBe(MR)
    expect(webLink('http://127.0.0.1:4312/x')).toBe('http://127.0.0.1:4312/x')
    expect(webLink('HTTPS://EXAMPLE.COM/x')).toBe('HTTPS://EXAMPLE.COM/x')
  })

  it('is no other scheme, and nothing that is not an address', () => {
    for (const u of ['file:///etc/passwd', 'javascript:void(0)', 'JavaScript:alert(1)', 'data:text/html,hi', 'vscode://file/etc/passwd', 'mailto:a@example.com', 'ftp://example.com/x', 'about:blank', '/etc/passwd', 'example.com', '']) {
      expect(webLink(u), u).toBeNull()
    }
  })
})

describe("the terminal's link handler", () => {
  it('opens a web link at once on Ctrl+click or ⌘+click, once, and offers nothing', () => {
    for (const mods of [{ ctrlKey: true }, { metaKey: true }]) {
      const open = vi.fn()
      const offer = vi.fn()
      terminalLinkHandler(open, offer).activate(click(mods), MR)
      expect(open).toHaveBeenCalledTimes(1)
      expect(open).toHaveBeenCalledWith(MR)
      expect(offer).not.toHaveBeenCalled()
    }
  })

  it('offers a web link on a plain click, once, and opens nothing', () => {
    const open = vi.fn()
    const offer = vi.fn()
    terminalLinkHandler(open, offer).activate(click(), MR)
    expect(offer).toHaveBeenCalledTimes(1)
    expect(offer).toHaveBeenCalledWith(MR)
    expect(open).not.toHaveBeenCalled()
  })

  it('does nothing with a link to another scheme, whatever the click', () => {
    const open = vi.fn()
    const offer = vi.fn()
    const h = terminalLinkHandler(open, offer)
    for (const u of ['file:///etc/passwd', 'javascript:void(0)', 'vscode://file/etc/passwd', 'not a link']) {
      h.activate(click(), u)
      h.activate(click({ ctrlKey: true }), u)
    }
    expect(open).not.toHaveBeenCalled()
    expect(offer).not.toHaveBeenCalled()
  })

  it('tells xterm to make no link of a hyperlink to another scheme', () => {
    expect(terminalLinkHandler(vi.fn(), vi.fn()).allowNonHttpProtocols).toBe(false)
  })
})
