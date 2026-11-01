import MarkdownIt from 'markdown-it'

/**
 * The Markdown preview (#166): a `.md` file rendered for the editor's Preview
 * toggle. A repository's Markdown is untrusted text, so raw HTML is never
 * rendered (markdown-it parses it as text: no scripts, no handlers), links
 * with a scheme markdown-it refuses (`javascript:`, `vbscript:`, `file:`)
 * are not links, links to the web open outside the app, a relative link
 * opens its file in the editor (`data-md-link`, resolved against the file's
 * folder), a relative image loads through the session's file reads (the
 * raw URL), never by a URL the page makes up, and a remote image is not
 * loaded (its alt text shows) unless allowed. Fenced code is left escaped
 * for the preview component to highlight with shiki; a `mermaid` fence is
 * marked for Mermaid to draw (`data-mermaid`), its source kept as text.
 */
export interface RenderOptions {
  /** The folder of the file, which relative links and images resolve against. */
  dir: string
  /** The session's raw file URL for a path, for relative images; none means they are not shown. */
  rawUrl?: (path: string) => string | null
  /** Whether images on the web are loaded (they tell a third party the file was opened). Off by default. */
  allowRemote?: boolean
  /**
   * Whether a relative link opens its file (`data-md-link`). False where the text has no folder to resolve one against (a Dispatch
   * ticket's description): the link's words stay, as words, and nothing opens. On by default.
   */
  relativeLinks?: boolean
  /**
   * Whether an image naming a Dispatch attachment (`![a screenshot](attachment:<id>)`) is kept for the component to show: an `img` with
   * `data-md-attachment` and no source, which the preview fills with an object URL of the content it fetched with the token, since an
   * `img` cannot carry a header; a link naming one keeps its words. Off by default, where such an image is held as its alt text.
   */
  attachments?: boolean
}

export interface Rendered {
  html: string
  /** How many Mermaid blocks the HTML holds, for the component to draw (none: Mermaid is never loaded). */
  mermaid: number
}

/** Whether a path is a Markdown file the editor previews. */
export function isMarkdownPath(path: string): boolean {
  return /\.(md|markdown)$/i.test(path)
}

/** The folder of a path: everything before its last segment. */
export function dirOf(path: string): string {
  const i = path.lastIndexOf('/')
  return i > 0 ? path.slice(0, i) : i === 0 ? '/' : ''
}

/** The id a heading gets, for a `#fragment` link in the same file: its text lowered, runs of anything but letters and digits as one dash, prefixed so it meets no id of the page's own. */
export function headingId(text: string): string {
  return 'md-' + text.toLowerCase().replace(/[^\p{L}\p{N}]+/gu, '-').replace(/^-|-$/g, '')
}

/** A relative path resolved against dir: `.` and `..` folded, an absolute path kept. */
export function resolveRelative(dir: string, href: string): string {
  const base = href.startsWith('/') ? [] : dir.split('/').filter(Boolean)
  const out = [...base]
  for (const part of href.split('/')) {
    if (part === '' || part === '.') continue
    if (part === '..') out.pop()
    else out.push(part)
  }
  return (dir.startsWith('/') || href.startsWith('/') ? '/' : '') + out.join('/')
}

/** Whether a link or image points outside the repository: a scheme, or a protocol-relative URL. */
export function isRemote(href: string): boolean {
  return /^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith('//')
}

/**
 * The file path a local link or image names: markdown-it gives link destinations URL-encoded (a space as %20, a
 * name in Japanese as its bytes), so the query and fragment are cut and the rest decoded once; one that is not
 * valid encoding is taken as written.
 */
export function localPath(href: string): string {
  const path = href.split(/[?#]/, 1)[0] ?? ''
  try {
    return decodeURIComponent(path)
  } catch {
    return path
  }
}


function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

/** Renders Markdown source to HTML under the rules above. Pure: the same source and options give the same HTML. */
export function renderMarkdown(src: string, opts: RenderOptions): Rendered {
  const md = new MarkdownIt({ html: false, linkify: false, typographer: false, breaks: false })
  let mermaid = 0

  // Task lists: a list item beginning `[ ] ` or `[x] ` gets a checkbox (disabled: the preview changes no file).
  md.core.ruler.after('inline', 'task-lists', (state) => {
    const tokens = state.tokens
    for (let i = 0; i < tokens.length; i++) {
      const t = tokens[i]!
      if (t.type !== 'inline' || !t.children?.length) continue
      const first = t.children[0]!
      const m = first.type === 'text' ? /^\[([ xX])\] /.exec(first.content) : null
      if (!m) continue
      // The inline's paragraph, and the list item it is in.
      const item = tokens[i - 2]
      if (!item || item.type !== 'list_item_open' || tokens[i - 1]?.type !== 'paragraph_open') continue
      first.content = first.content.slice(m[0].length)
      const box = new state.Token('html_inline', '', 0)
      box.content = `<input type="checkbox" disabled${m[1] === ' ' ? '' : ' checked'}> `
      t.children.unshift(box)
      item.attrJoin('class', 'task')
    }
  })

  // Headings carry an id (headingId), each unique in the file, so a `#fragment` link scrolls to its heading.
  md.core.ruler.after('inline', 'heading-ids', (state) => {
    const seen = new Map<string, number>()
    const tokens = state.tokens
    for (let i = 0; i + 1 < tokens.length; i++) {
      const t = tokens[i]!
      if (t.type !== 'heading_open') continue
      const base = headingId(tokens[i + 1]!.content)
      const n = (seen.get(base) ?? 0) + 1
      seen.set(base, n)
      t.attrSet('id', n === 1 ? base : `${base}-${n}`)
    }
  })

  // Each link's open says whether it was held as words (a relative link with relativeLinks off), for its close: links do not nest.
  const held: boolean[] = []
  md.renderer.rules.link_open = (tokens, idx, options, _env, self) => {
    const t = tokens[idx]!
    const href = String(t.attrGet('href') ?? '')
    if (!href) {
      // `[words]()`: no destination, so no link (an empty href would reload the page).
      t.attrs = (t.attrs ?? []).filter((a) => a[0] !== 'href')
    } else if (opts.attachments && /^attachment:/i.test(href)) {
      held.push(true)
      return `<span class="md-link-held" title="An attachment of the ticket's: it is downloaded from its Attachments">`
    } else if (isRemote(href)) {
      t.attrSet('target', '_blank')
      t.attrSet('rel', 'noopener noreferrer')
    } else if (href && !href.startsWith('#')) {
      if (opts.relativeLinks === false) {
        held.push(true)
        return `<span class="md-link-held" title="${escapeHtml(href)}">`
      }
      // A file in the repository: the component opens it in a tab.
      t.attrSet('data-md-link', resolveRelative(opts.dir, localPath(href)))
      t.attrSet('href', '#')
    }
    held.push(false)
    return self.renderToken(tokens, idx, options)
  }
  md.renderer.rules.link_close = (tokens, idx, options, _env, self) => (held.pop() ? '</span>' : self.renderToken(tokens, idx, options))

  md.renderer.rules.image = (tokens, idx, options, env, self) => {
    const t = tokens[idx]!
    const src = String(t.attrGet('src') ?? '')
    const alt = self.renderInlineAsText(t.children ?? [], options, env)
    const attachment = opts.attachments ? /^attachment:([0-9a-f]{16})$/.exec(src) : null
    if (attachment) {
      // Filled by the component with the content it fetched with the token: no URL of the API is ever an img's source.
      return `<img data-md-attachment="${attachment[1]}" alt="${escapeHtml(alt)}">`
    }
    if (/^data:image\//i.test(src)) {
      // An image embedded in the file (markdown-it passes only gif, png, jpeg and webp data URLs): nothing is fetched.
    } else if (isRemote(src)) {
      if (!opts.allowRemote) return `<span class="md-image-held" title="An image on the web is not loaded here">${escapeHtml(alt || src)}</span>`
    } else {
      const url = opts.rawUrl?.(resolveRelative(opts.dir, localPath(src))) ?? null
      if (!url) return `<span class="md-image-held" title="${escapeHtml(src)}">${escapeHtml(alt || src)}</span>`
      t.attrSet('src', url)
    }
    t.attrSet('alt', alt)
    return self.renderToken(tokens, idx, options)
  }

  md.renderer.rules.fence = (tokens, idx) => {
    const t = tokens[idx]!
    const lang = (t.info || '').trim().split(/\s+/)[0] ?? ''
    if (lang === 'mermaid') {
      mermaid++
      return `<pre class="md-mermaid" data-mermaid>${escapeHtml(t.content)}</pre>\n`
    }
    return `<pre class="md-code"${lang ? ` data-code-lang="${escapeHtml(lang)}"` : ''}><code>${escapeHtml(t.content)}</code></pre>\n`
  }

  return { html: md.render(src), mermaid }
}
