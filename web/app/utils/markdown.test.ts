import { describe, expect, it } from 'vitest'
import { dirOf, headingId, isMarkdownPath, localPath, renderMarkdown, resolveRelative } from './markdown'

const dir = '/repo/docs'
const rawUrl = (p: string) => `/api/sessions/s1/files?path=${encodeURIComponent(p)}&raw=1`

describe('the Markdown preview', () => {
  it('renders headings, lists, tables, task lists and code, and names Markdown files and their folders', () => {
    const { html, mermaid } = renderMarkdown('# Title\n\n- one\n- [x] done\n- [ ] not yet\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfunc main() {}\n```\n', { dir })
    expect(html).toContain('<h1 id="md-title">Title</h1>')
    expect(html).toContain('<li class="task"><input type="checkbox" disabled checked> done</li>')
    expect(html).toContain('<li class="task"><input type="checkbox" disabled> not yet</li>')
    expect(html).toContain('<li>one</li>')
    expect(html).toContain('<td>1</td>')
    expect(html).toContain('<pre class="md-code" data-code-lang="go"><code>func main() {}\n</code></pre>')
    expect(mermaid).toBe(0)
    expect(isMarkdownPath('/r/README.md')).toBe(true)
    expect(isMarkdownPath('/r/notes.MARKDOWN')).toBe(true)
    expect(isMarkdownPath('/r/main.go')).toBe(false)
    expect(dirOf('/repo/docs/users.md')).toBe('/repo/docs')
    expect(dirOf('/users.md')).toBe('/')
  })

  it('renders raw HTML as text and refuses scripted links: nothing runs', () => {
    const { html } = renderMarkdown('<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n[run](javascript:alert(1)) and [also](vbscript:x)\n', { dir })
    // Shown as the text it is: no element, no attribute, no link.
    expect(html).not.toMatch(/<script|<img/)
    expect(html).toContain('<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>')
    expect(html).toContain('<p>&lt;img src=x onerror=alert(1)&gt;</p>')
    expect(html).not.toMatch(/href=|<a/)
    expect(html).toContain('<p>[run](javascript:alert(1)) and [also](vbscript:x)</p>')
  })

  it('opens a relative link in the editor, resolved against the folder, and a web link outside the app', () => {
    const { html } = renderMarkdown('[users](users.md), [up](../README.md#top), [here](#intro), [web](https://example.net/x)\n', { dir })
    expect(html).toContain('<a href="#" data-md-link="/repo/docs/users.md">users</a>')
    expect(html).toContain('<a href="#" data-md-link="/repo/README.md">up</a>')
    expect(html).toContain('<a href="#intro">here</a>')
    expect(html).toContain('<a href="https://example.net/x" target="_blank" rel="noopener noreferrer">web</a>')
    expect(resolveRelative('/repo/docs', '../img/../a.md')).toBe('/repo/a.md')
    expect(resolveRelative('/repo/docs', '/abs/b.md')).toBe('/abs/b.md')
    expect(resolveRelative('/repo/docs', './c.md')).toBe('/repo/docs/c.md')
  })

  it("keeps a relative link's words as words where the text has no folder (a Dispatch ticket's), and every other link as it is", () => {
    const { html } = renderMarkdown('[users](users.md), [up](../README.md#top), [here](#intro), [web](https://example.net/x), [none]()\n', { dir: '', relativeLinks: false })
    expect(html).not.toContain('data-md-link')
    expect(html).toContain('<span class="md-link-held" title="users.md">users</span>')
    expect(html).toContain('<span class="md-link-held" title="../README.md#top">up</span>')
    expect(html).toContain('<a href="#intro">here</a>')
    expect(html).toContain('<a href="https://example.net/x" target="_blank" rel="noopener noreferrer">web</a>')
    expect(html).toContain('<a>none</a>')
    // A held link's destination is text in its title, escaped.
    expect(renderMarkdown('[q](a"b<c>.md)\n', { dir: '', relativeLinks: false }).html).toContain('title="a%22b%3Cc%3E.md"')
  })

  it("links a bare URL with its scheme where asked (a Dispatch ticket's text, pasted from a GitHub issue), and nothing else", () => {
    const src = 'See https://github.com/o/r/pull/1, and https://q.example/x?y=1. Mail a@b.example; not README.md, example.com, www.example.com or #359.\n\n`https://code.example` [t](https://t.example) <https://x.example> [u](notes.md) javascript:alert(1)\n'
    const { html } = renderMarkdown(src, { dir: '', relativeLinks: false, linkify: true })
    expect(html).toContain('<a href="https://github.com/o/r/pull/1" target="_blank" rel="noopener noreferrer">https://github.com/o/r/pull/1</a>,')
    expect(html).toContain('<a href="https://q.example/x?y=1" target="_blank" rel="noopener noreferrer">https://q.example/x?y=1</a>.')
    expect(html).toContain('<a href="mailto:a@b.example" target="_blank" rel="noopener noreferrer">a@b.example</a>')
    // A name with no scheme, an issue number, a URL in code and a held relative link stay as they were; a scheme markdown-it refuses is no link.
    expect(html).toContain('not README.md, example.com, www.example.com or #359.')
    expect(html).toContain('<code>https://code.example</code>')
    expect(html).toContain('<a href="https://t.example" target="_blank" rel="noopener noreferrer">t</a>')
    expect(html).toContain('<a href="https://x.example" target="_blank" rel="noopener noreferrer">https://x.example</a>')
    expect(html).toContain('<span class="md-link-held" title="notes.md">u</span>')
    expect(html).not.toContain('href="javascript:')
    expect((html.match(/<a /g) ?? []).length).toBe(5)
    // Off by default: a repository's file keeps its bare URLs as words.
    expect(renderMarkdown('See https://github.com/o/r/pull/1\n', { dir }).html).toBe('<p>See https://github.com/o/r/pull/1</p>\n')
  })

  it('names the file a local link or image means: the encoding markdown-it gives undone once, the query and fragment cut', () => {
    expect(localPath('my%20notes.md')).toBe('my notes.md')
    expect(localPath('a%2520b.md')).toBe('a%20b.md')
    expect(localPath('a%23b.md#x')).toBe('a#b.md')
    expect(localPath('c.md?x=1#y')).toBe('c.md')
    expect(localPath('%E0%A4%A.md')).toBe('%E0%A4%A.md')
    const { html } = renderMarkdown('[n](my%20notes.md) [j](日本.md) [p](a%2520b.md) [h](a%23b.md#x) ![s](im%20g.png)\n', { dir, rawUrl })
    expect(html).toContain('data-md-link="/repo/docs/my notes.md"')
    expect(html).toContain('data-md-link="/repo/docs/日本.md"')
    expect(html).toContain('data-md-link="/repo/docs/a%20b.md"')
    expect(html).toContain('data-md-link="/repo/docs/a#b.md"')
    expect(html).toContain(`src="${rawUrl('/repo/docs/im g.png').replace(/&/g, '&amp;')}"`)
  })

  it('gives headings ids for fragment links, each unique, and makes no link of an empty destination', () => {
    expect(headingId('Intro')).toBe('md-intro')
    expect(headingId('  The Yard & the Roundhouse!  ')).toBe('md-the-yard-the-roundhouse')
    expect(headingId('日本語 の 見出し')).toBe('md-日本語-の-見出し')
    const { html } = renderMarkdown('# Intro\n\n## Intro\n\n### Users, by name\n\nSee [the intro](#intro) and [placeholder]() and [nothing]( ).\n', { dir })
    expect(html).toContain('<h1 id="md-intro">Intro</h1>')
    expect(html).toContain('<h2 id="md-intro-2">Intro</h2>')
    expect(html).toContain('<h3 id="md-users-by-name">Users, by name</h3>')
    expect(html).toContain('<a href="#intro">the intro</a>')
    expect(html).toContain('<a>placeholder</a>')
    expect(html).toContain('<a>nothing</a>')
    expect(html).not.toContain('href=""')
  })

  it('loads a relative image through the session, holds one on the web unless allowed, and shows the alt text otherwise', () => {
    const { html } = renderMarkdown('![a diagram](img/a.png) ![remote](https://example.net/r.png)\n', { dir, rawUrl })
    expect(html).toContain('<img src="/api/sessions/s1/files?path=%2Frepo%2Fdocs%2Fimg%2Fa.png&amp;raw=1" alt="a diagram">')
    expect(html).toContain('<span class="md-image-held" title="An image on the web is not loaded here">remote</span>')
    const allowed = renderMarkdown('![remote](https://example.net/r.png)\n', { dir, rawUrl, allowRemote: true }).html
    expect(allowed).toContain('<img src="https://example.net/r.png" alt="remote">')
    const noRaw = renderMarkdown('![local](a.png)\n', { dir }).html
    expect(noRaw).toContain('<span class="md-image-held" title="a.png">local</span>')
    expect(noRaw).not.toContain('<img')
    // An image embedded in the file fetches nothing: shown; a data URL of anything else is no image to markdown-it.
    const embedded = renderMarkdown('![dot](data:image/png;base64,iVBORw0KGgo=) ![page](data:text/html;base64,PHA+)\n', { dir }).html
    expect(embedded).toContain('<img src="data:image/png;base64,iVBORw0KGgo=" alt="dot">')
    expect(embedded).not.toMatch(/src="data:text/)
    expect(embedded).toContain('![page](data:text/html;base64,PHA+)</p>')
  })

  it('marks a mermaid fence for drawing, its source kept as text, and counts them', () => {
    const { html, mermaid } = renderMarkdown('```mermaid\nflowchart LR\n  A --> B\n```\n\n```mermaid\nsequenceDiagram\n  a->>b: hi\n```\n', { dir })
    expect(mermaid).toBe(2)
    expect(html).toContain('<pre class="md-mermaid" data-mermaid>flowchart LR\n  A --&gt; B\n</pre>')
    expect(html).not.toContain('<svg')
  })
})
