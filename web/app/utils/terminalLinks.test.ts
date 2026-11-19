import { describe, expect, it } from 'vitest'
import { linkHost, linkTip, opensLink, SHOWN_REST_MAX, shownLink, terminalLinkHandler, webLink, type LinkTip } from './terminalLinks'

const MR = 'https://example.com/group/project/-/merge_requests/95'
const OTHER = ['file:///etc/passwd', 'javascript:void(0)', 'JavaScript:alert(1)', 'data:text/html,hi', 'vscode://file/etc/passwd', 'mailto:a@example.com', 'ftp://example.com/x', 'about:blank', '/etc/passwd', 'example.com', 'not a link', '']
const CLICK = { button: 0, detail: 1 }

function handler(selected = false, mac = false) {
  const calls: string[] = []
  const h = terminalLinkHandler({
    open: (u: string) => calls.push(`open ${u}`),
    opened: (u: string) => calls.push(`opened ${u}`),
    show: (t: LinkTip | null) => calls.push(`show ${t ? t.origin + t.rest : null}`),
    selected: () => selected,
    mac,
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

describe('what the hover shows', () => {
  it('is the scheme and host, then the path', () => {
    expect(linkTip(MR)).toEqual({ origin: 'https://example.com', rest: '/group/project/-/merge_requests/95' })
    expect(linkTip('http://127.0.0.1:4312/x?y=1#z')).toEqual({ origin: 'http://127.0.0.1:4312', rest: '/x?y=1#z' })
  })

  it('keeps a very long host whole, first', () => {
    const host = `${'part.'.repeat(40)}example.net`
    const tip = linkTip(`https://${host}/${'p'.repeat(300)}`)!
    expect(tip.origin).toBe(`https://${host}`)
    expect(tip.origin.endsWith('example.net')).toBe(true)
  })

  it('leaves a sign-in part out, showing the host the link goes to', () => {
    const tip = linkTip('https://example.com@other.example.net/x')!
    expect(tip.origin).toBe('https://other.example.net')
    expect(tip.origin + tip.rest).not.toContain('example.com')
    expect(linkTip('https://name:secret@example.net/x')).toEqual({ origin: 'https://example.net', rest: '/x' })
  })

  it('shows an internationalised host in the ASCII form the browser uses', () => {
    // A host with a letter outside ASCII.
    const tip = linkTip('https://exаmple.com/x')!
    expect(tip.origin.startsWith('https://xn--')).toBe(true)
    expect(tip.origin).not.toContain('а')
  })

  it('cuts a long path at its end, never the host', () => {
    const tip = linkTip(`https://example.com/${'a'.repeat(300)}/the-end`)!
    expect(tip.origin).toBe('https://example.com')
    expect(Array.from(tip.rest)).toHaveLength(SHOWN_REST_MAX)
    expect(tip.rest.startsWith('/aaa')).toBe(true)
    expect(tip.rest.endsWith('…')).toBe(true)
    expect(tip.rest).not.toContain('the-end')
  })

  it('is nothing for another scheme', () => {
    for (const u of OTHER) expect(linkTip(u), u).toBeNull()
  })

  it('names the link the same way after the open', () => {
    expect(shownLink(MR)).toBe(MR)
    expect(shownLink('https://example.com@other.example.net/x')).toBe('https://other.example.net/x')
  })
})

describe('which press opens a link', () => {
  it('a single press of the primary button that selected nothing', () => {
    expect(opensLink(CLICK, false, false)).toBe(true)
    expect(opensLink({}, false, false)).toBe(true)
  })

  it('not the second press of a double-click, nor a triple-click’s', () => {
    expect(opensLink({ button: 0, detail: 2 }, false, false)).toBe(false)
    expect(opensLink({ button: 0, detail: 3 }, false, false)).toBe(false)
  })

  it('not a middle or a right press', () => {
    expect(opensLink({ button: 1, detail: 1 }, false, false)).toBe(false)
    expect(opensLink({ button: 2, detail: 1 }, false, false)).toBe(false)
  })

  it('not a press that selected text', () => {
    expect(opensLink(CLICK, true, false)).toBe(false)
  })

  it('on a Mac, not Control with the primary button, the secondary click there; ⌘ with it opens', () => {
    expect(opensLink({ ...CLICK, ctrlKey: true }, false, true)).toBe(false)
    expect(opensLink({ ...CLICK, metaKey: true }, false, true)).toBe(true)
    expect(opensLink(CLICK, false, true)).toBe(true)
  })

  it('elsewhere, Control with the primary button opens', () => {
    expect(opensLink({ ...CLICK, ctrlKey: true }, false, false)).toBe(true)
  })
})

describe("the terminal's link handler", () => {
  it('on a Mac opens nothing on Control with the primary button, and once on ⌘ with it', () => {
    const ctrl = handler(false, true)
    ctrl.h.activate({ ...CLICK, ctrlKey: true }, MR)
    expect(ctrl.calls).toEqual([])
    const cmd = handler(false, true)
    cmd.h.activate({ ...CLICK, metaKey: true }, MR)
    expect(cmd.calls).toEqual([`open ${MR}`, `opened ${MR}`])
  })

  it('opens a web link once on a single click, with or without Ctrl or ⌘ off a Mac, then tells the page', () => {
    for (const mods of [{}, { ctrlKey: true }, { metaKey: true }]) {
      const { h, calls } = handler()
      h.activate({ ...CLICK, ...mods }, MR)
      expect(calls).toEqual([`open ${MR}`, `opened ${MR}`])
    }
  })

  it('opens once for a double-click: the first press opens, the second does nothing more', () => {
    const { h, calls } = handler()
    h.activate({ button: 0, detail: 1 }, MR)
    h.activate({ button: 0, detail: 2 }, MR)
    expect(calls).toEqual([`open ${MR}`, `opened ${MR}`])
  })

  it('opens nothing on a middle or a right press, or after a drag that selected text', () => {
    const { h, calls } = handler()
    h.activate({ button: 1, detail: 1 }, MR)
    h.activate({ button: 2, detail: 1 }, MR)
    expect(calls).toEqual([])
    const dragged = handler(true)
    dragged.h.activate(CLICK, MR)
    expect(dragged.calls).toEqual([])
  })

  it('shows where a link goes while the pointer is over it, from its parsed address, and nothing once it leaves', () => {
    const { h, calls } = handler()
    h.hover({}, 'https://example.com@other.example.net/x')
    h.leave()
    expect(calls).toEqual(['show https://other.example.net/x', 'show null'])
  })

  it('opens, tells and shows nothing for a link to another scheme', () => {
    const { h, calls } = handler()
    for (const u of OTHER) {
      h.activate(CLICK, u)
      h.activate({ ...CLICK, ctrlKey: true }, u)
      h.hover({}, u)
    }
    expect(calls).toEqual([])
  })

  it('tells xterm to make no link of a hyperlink to another scheme', () => {
    expect(handler().h.allowNonHttpProtocols).toBe(false)
  })
})
