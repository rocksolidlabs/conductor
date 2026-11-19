// What a click on a link a terminal shows does (#1020): one handler for both kinds of link, the address xterm's web-links addon
// finds in the text and the hyperlink an agent prints with OSC 8 (its words a link to an address they need not show). Without a
// handler of the page's own, xterm activated a hyperlink with the page's confirm ("Do you want to navigate to …? WARNING: This link
// could potentially be dangerous"), which the desktop app shows as a native dialog. The protection a person keeps is the browser's
// own: the address a link goes to is shown while the pointer is over it, before any click.

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

/** The longest address the terminal's hover shows whole, in characters; a longer one is cut in its middle. */
export const SHOWN_ADDRESS_MAX = 160

/** shownAddress is the address as the hover shows it: whole, or its start and its end with an ellipsis between. */
export function shownAddress(url: string, max = SHOWN_ADDRESS_MAX): string {
  const chars = Array.from(url)
  if (chars.length <= max) return url
  const head = Math.ceil((max - 1) / 2)
  const tail = max - 1 - head
  return `${chars.slice(0, head).join('')}…${chars.slice(chars.length - tail).join('')}`
}

export interface TerminalLinks {
  /** Opens a web link, once: a new tab (the desktop app hands it to the system's browser). */
  open(url: string): void
  /** Told after the open: the page says where it went and offers its preview. */
  opened(url: string): void
  /** The address the pointer is over, shown; null when it leaves the link. */
  show(url: string | null): void
}

/**
 * terminalLinkHandler is the terminal's link handler (xterm's linkHandler option, and the web-links addon's handler and hover): a
 * click on a web link, plain or with Ctrl or ⌘, opens it once (open) and then tells the page (opened); the pointer over one shows
 * its real address (show) until it leaves. A link to any other scheme does nothing and shows nothing, and xterm is told not to make
 * one of a hyperlink to such a scheme at all.
 */
export function terminalLinkHandler(o: TerminalLinks) {
  return {
    allowNonHttpProtocols: false,
    activate(_event: unknown, uri: string): void {
      const url = webLink(uri)
      if (!url) return
      o.open(url)
      o.opened(url)
    },
    hover(_event: unknown, uri: string): void {
      const url = webLink(uri)
      if (url) o.show(url)
    },
    leave(): void {
      o.show(null)
    },
  }
}
