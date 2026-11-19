import { describe, expect, it } from 'vitest'
import { LINK_OFFER_TOAST, linkAction, linkHost, linkModifierWords, linkOfferNote, linkTip, opensLink, SHOWN_REST_MAX, shownLink, terminalLinkHandler, webLink, type LinkTip } from './terminalLinks'

const MR = 'https://example.com/group/project/-/merge_requests/95'
const OTHER = ['file:///etc/passwd', 'javascript:void(0)', 'JavaScript:alert(1)', 'data:text/html,hi', 'vscode://file/etc/passwd', 'mailto:a@example.com', 'ftp://example.com/x', 'about:blank', '/etc/passwd', 'example.com', 'not a link', '']
const CLICK = { button: 0, detail: 1 }

function handler(selected = false, mac = false, appMouse = false) {
  const calls: string[] = []
  const tips: LinkTip[] = []
  const h = terminalLinkHandler({
    open: (u: string) => calls.push(`open ${u}`),
    opened: (u: string) => calls.push(`opened ${u}`),
    offer: (u: string) => calls.push(`offer ${u}`),
    show: (t: LinkTip | null) => {
      if (t) tips.push(t)
      calls.push(`show ${t ? t.origin + t.rest + (t.hint ? ` ${t.hint}` : '') : null}`)
    },
    selected: () => selected,
    mac,
    appMouse: () => appMouse,
  })
  return { h, calls, tips }
}

describe('a web link', () => {
  it('is an http or https address', () => {
    expect(webLink(MR)).toBe(MR)
    expect(webLink('http://127.0.0.1:4312/x')).toBe('http://127.0.0.1:4312/x')
  })

  it('is no other scheme, and nothing that is not an address', () => {
    for (const u of OTHER) expect(webLink(u), u).toBeNull()
  })

  it('is the address as the parser writes it: scheme and host in lower case, no default port, a trailing dot kept', () => {
    expect(webLink('HTTPS://EXAMPLE.COM:443/a')).toBe('https://example.com/a')
    expect(webLink('http://Example.COM:80/a?b=1#c')).toBe('http://example.com/a?b=1#c')
    expect(webLink('https://Example.COM.:443/x')).toBe('https://example.com./x')
    expect(webLink('  https://example.com')).toBe('https://example.com/')
  })

  it('is not an address printed without the two slashes after its scheme, or with one', () => {
    for (const u of ['http:example.com/a', 'HTTPS:example.com/a', 'https:/example.com/a', 'http:/example.com/a', 'https:\\\\example.com/a']) expect(webLink(u), u).toBeNull()
    for (const u of ['http:example.com/a', 'https:/example.com/a']) expect(linkTip(u), u).toBeNull()
  })

  it('is said by its host', () => {
    expect(linkHost(MR)).toBe('example.com')
    expect(linkHost('http://127.0.0.1:4312/x')).toBe('127.0.0.1:4312')
  })
})

describe('what the hover shows', () => {
  it('is the scheme and host, then the path', () => {
    expect(linkTip(MR)).toEqual({ href: MR, origin: 'https://example.com', rest: '/group/project/-/merge_requests/95' })
    expect(linkTip('http://127.0.0.1:4312/x?y=1#z')).toEqual({ href: 'http://127.0.0.1:4312/x?y=1#z', origin: 'http://127.0.0.1:4312', rest: '/x?y=1#z' })
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
    expect(linkTip('https://name:secret@example.net/x')).toMatchObject({ origin: 'https://example.net', rest: '/x' })
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

  it('not a press with Alt, which moves the prompt’s cursor', () => {
    expect(opensLink({ ...CLICK, altKey: true }, false, false)).toBe(false)
    expect(opensLink({ ...CLICK, altKey: true, ctrlKey: true }, false, false, true)).toBe(false)
  })

  it('while the program holds the mouse, not a plain click nor a Ctrl+click; Shift+click elsewhere and ⌘+click on a Mac open', () => {
    expect(opensLink(CLICK, false, false, true)).toBe(false)
    expect(opensLink(CLICK, false, true, true)).toBe(false)
    expect(opensLink({ ...CLICK, ctrlKey: true }, false, false, true)).toBe(false)
    expect(opensLink({ ...CLICK, ctrlKey: true }, false, true, true)).toBe(false)
    expect(opensLink({ ...CLICK, shiftKey: true }, false, false, true)).toBe(true)
    expect(opensLink({ ...CLICK, metaKey: true }, false, true, true)).toBe(true)
    // On a Mac the program hears Shift+click; elsewhere ⌘ (the system key) is not the terminal's.
    expect(opensLink({ ...CLICK, shiftKey: true }, false, true, true)).toBe(false)
    expect(opensLink({ ...CLICK, metaKey: true }, false, false, true)).toBe(false)
  })

  it('while the program holds the mouse, a plain or a Ctrl press offers the link; the kept press opens it; the rest do nothing', () => {
    expect(linkAction(CLICK, false, false, true)).toBe('offer')
    expect(linkAction({ ...CLICK, ctrlKey: true }, false, false, true)).toBe('offer')
    expect(linkAction({ ...CLICK, shiftKey: true }, false, false, true)).toBe('open')
    expect(linkAction(CLICK, false, true, true)).toBe('offer')
    expect(linkAction({ ...CLICK, metaKey: true }, false, true, true)).toBe('open')
    // A menu press, a middle press, Alt, a double-click's second press and a press that left a selection: nothing, no offer.
    for (const [press, selected, mac] of [
      [{ button: 2, detail: 1 }, false, false],
      [{ ...CLICK, ctrlKey: true }, false, true],
      [{ button: 1, detail: 1 }, false, false],
      [{ ...CLICK, altKey: true }, false, false],
      [{ button: 0, detail: 2 }, false, false],
      [CLICK, true, false],
    ] as const) expect(linkAction(press, selected, mac, true)).toBeNull()
    // Without tracking nothing is offered.
    expect(linkAction(CLICK, false, false, false)).toBe('open')
  })

  it('on a page that reports nothing to the program (appMouse false: a view link’s), a plain click opens whatever the program asked', () => {
    expect(opensLink(CLICK, false, false, false)).toBe(true)
    expect(opensLink({ ...CLICK, ctrlKey: true }, false, false, false)).toBe(true)
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

describe('the note that offers a link', () => {
  it('is one note, the address in its parsed form, whose Open opens exactly the string offered', () => {
    const opened: string[] = []
    const previewed: string[] = []
    const url = webLink('HTTPS://EXAMPLE.COM:443/group/project')!
    const note = linkOfferNote(url, (u) => opened.push(u), (u) => previewed.push(u))
    expect(note.id).toBe(LINK_OFFER_TOAST)
    expect(linkOfferNote(MR, () => {}, () => {}).id).toBe(note.id)
    expect(note.title).toBe('https://example.com/group/project')
    expect(note.actions.map((a) => a.label)).toEqual(['Open in new tab', 'Preview in pane'])
    note.actions[0]!.onClick()
    expect(opened).toEqual([url])
    note.actions[1]!.onClick()
    expect(previewed).toEqual([url])
    expect(opened).toEqual([url])
  })

  it('names a link with a sign-in part by its host alone, and its Open still opens the string offered, not the words shown', () => {
    const url = webLink('https://name@example.net/x')!
    const opened: string[] = []
    const note = linkOfferNote(url, (u) => opened.push(u), () => {})
    expect(note.title).toBe('https://example.net/x')
    note.actions[0]!.onClick()
    expect(opened).toEqual([url])
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

  it('opens exactly the address its tip is drawn from', () => {
    for (const printed of ['HTTPS://EXAMPLE.COM:443/a', 'https://Example.COM.:443/x', 'https://name@example.net/x']) {
      const { h, calls, tips } = handler()
      h.hover({}, printed)
      h.activate(CLICK, printed)
      expect(calls.filter((c) => c.startsWith('open '))).toEqual([`open ${tips[0]!.href}`])
      expect(calls).toContain(`opened ${tips[0]!.href}`)
      // That one string is absolute and in the parser's own form, and its origin is the one the tip shows.
      expect(tips[0]!.href).toBe(new URL(tips[0]!.href).href)
      expect(new URL(tips[0]!.href).origin).toBe(tips[0]!.origin)
    }
  })

  it('opens nothing and shows nothing for an address printed without its two slashes', () => {
    const { h, calls } = handler()
    h.hover({}, 'http:example.com/a')
    h.activate(CLICK, 'http:example.com/a')
    h.activate({ ...CLICK, ctrlKey: true }, 'https:/example.com/a')
    expect(calls).toEqual([])
  })

  it('while the program holds the mouse, opens on the modifier alone and says so on the hover', () => {
    const { h, calls } = handler(false, false, true)
    h.hover({}, MR)
    h.activate(CLICK, MR)
    h.activate({ ...CLICK, ctrlKey: true }, MR)
    h.activate({ ...CLICK, shiftKey: true }, MR)
    expect(calls).toEqual([`show ${MR} ${linkModifierWords(false)}`, `offer ${MR}`, `offer ${MR}`, `open ${MR}`, `opened ${MR}`])
    const mac = handler(false, true, true)
    mac.h.hover({}, MR)
    expect(mac.tips[0]!.hint).toBe('⌘+click opens')
    expect(linkModifierWords(false)).toBe('Shift+click opens')
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
