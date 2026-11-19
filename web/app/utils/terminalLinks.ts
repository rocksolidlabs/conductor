// What a click on a link a terminal shows does (#1020): one handler for both kinds of link, the address xterm's web-links addon
// finds in the text and the hyperlink an agent prints with OSC 8 (its words a link to an address they need not show). Without a
// handler of the page's own, xterm activated a hyperlink with the page's confirm ("Do you want to navigate to …? WARNING: This link
// could potentially be dangerous"), which the desktop app shows as a native dialog. The protection a person keeps is the browser's
// own: where a link goes is shown while the pointer is over it, before any click, from its parsed address.

/** webLink is the address when it is a web one (http or https), else null: no other scheme is opened from a terminal. */
export function webLink(uri: string): string | null {
  try {
    const u = new URL(uri)
    return u.protocol === 'http:' || u.protocol === 'https:' ? uri : null
  } catch {
    return null
  }
}

/** linkHost is where a web link goes, in the words a toast says it with: its host (example.com, 127.0.0.1:4312). */
export function linkHost(url: string): string {
  try {
    return new URL(url).host || url
  } catch {
    return url
  }
}

/** The longest part after the host the terminal's hover shows, in characters; a longer one is cut at its end. */
export const SHOWN_REST_MAX = 120

/** What the hover shows of a web link: where it goes (its scheme and host, never cut) and what follows (its path, cut at its end). */
export interface LinkTip {
  /** The scheme and the host as the browser uses it (an internationalised name in its ASCII form), with no sign-in part. */
  origin: string
  /** The path, query and fragment, cut at its end when long. */
  rest: string
}

/**
 * linkTip draws the hover from the parsed address, never from the words it was printed as: the origin (`URL.protocol` and
 * `URL.host`, which the URL standard gives in its ASCII form and without the `user:password@` part) whole and first, then the
 * path and what follows, cut at its end. Null for anything that is not a web link.
 */
export function linkTip(uri: string, max = SHOWN_REST_MAX): LinkTip | null {
  let u: URL
  try {
    u = new URL(uri)
  } catch {
    return null
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return null
  const rest = `${u.pathname}${u.search}${u.hash}`
  const chars = Array.from(rest)
  return { origin: `${u.protocol}//${u.host}`, rest: chars.length <= max ? rest : `${chars.slice(0, max - 1).join('')}…` }
}

/** shownLink is a web link as the page names it after the open: its origin and the rest, from the parsed address. */
export function shownLink(url: string): string {
  const t = linkTip(url, Number.MAX_SAFE_INTEGER)
  return t ? t.origin + t.rest : url
}

/** The parts of the mouse-up a link is activated on that decide whether it opens. */
export type LinkPress = Partial<Pick<MouseEvent, 'button' | 'detail'>>

/**
 * opensLink is whether a mouse-up on a link opens it: a single press of the primary button (xterm calls a link's activate on every
 * mouse-up that ends on the link the press began on, whatever the button or the count) that made no selection. The first click of
 * a double-click opens it once; the second (detail 2), a triple's third, a middle or a right press, and a drag that selected text
 * inside the link open nothing.
 */
export function opensLink(e: LinkPress, selected: boolean): boolean {
  return (e.button ?? 0) === 0 && (e.detail ?? 1) <= 1 && !selected
}

export interface TerminalLinks {
  /** Opens a web link, once: a new tab (the desktop app hands it to the system's browser). */
  open(url: string): void
  /** Told after the open: the page says where it went and offers its preview. */
  opened(url: string): void
  /** Where the link the pointer is over goes, shown; null when it leaves the link. */
  show(tip: LinkTip | null): void
  /** Whether the terminal holds a selection now, at the mouse-up: a drag that selected text is not a click. */
  selected(): boolean
}

/**
 * terminalLinkHandler is the terminal's link handler (xterm's linkHandler option, and the web-links addon's handler and hover): a
 * plain single click of the primary button on a web link, with Ctrl or ⌘ or not, that made no selection opens it once (open) and
 * then tells the page (opened); the pointer over one shows where it goes, drawn from its parsed address (show), until it leaves. A
 * link to any other scheme does nothing and shows nothing, and xterm is told not to make one of a hyperlink to such a scheme at all.
 */
export function terminalLinkHandler(o: TerminalLinks) {
  return {
    allowNonHttpProtocols: false,
    activate(event: LinkPress, uri: string): void {
      const url = webLink(uri)
      if (!url || !opensLink(event, o.selected())) return
      o.open(url)
      o.opened(url)
    },
    hover(_event: unknown, uri: string): void {
      const tip = linkTip(uri)
      if (tip) o.show(tip)
    },
    leave(): void {
      o.show(null)
    },
  }
}
