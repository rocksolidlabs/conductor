// What a click on a link a terminal shows does (#1020): one handler for both kinds of link, the address xterm's web-links addon
// finds in the text and the hyperlink an agent prints with OSC 8 (its words a link to an address they need not show). Without a
// handler of the page's own, xterm activated a hyperlink with the page's confirm ("Do you want to navigate to …? WARNING: This link
// could potentially be dangerous"), which the desktop app shows as a native dialog.

/** webLink is the address when it is a web one (http or https), else null: no other scheme is opened from a terminal. */
export function webLink(uri: string): string | null {
  try {
    const u = new URL(uri)
    return u.protocol === 'http:' || u.protocol === 'https:' ? uri : null
  } catch {
    return null
  }
}

/** The modifier keys of the click that decide what it does. */
export type LinkClick = Pick<MouseEvent, 'ctrlKey' | 'metaKey'>

/**
 * terminalLinkHandler is the terminal's link handler (xterm's linkHandler option, and the web-links addon's handler): a click with
 * Ctrl (⌘ on a Mac) opens a web link at once (open, a new tab: the desktop app hands it to the system's browser), a plain click
 * offers it (the page's toast: a new tab or the preview pane), each once; a link to any other scheme does nothing, and xterm is
 * told not to make one of a hyperlink to such a scheme at all.
 */
export function terminalLinkHandler(open: (url: string) => void, offer: (url: string) => void) {
  return {
    allowNonHttpProtocols: false,
    activate(event: LinkClick, uri: string): void {
      const url = webLink(uri)
      if (!url) return
      if (event.ctrlKey || event.metaKey) open(url)
      else offer(url)
    },
  }
}
