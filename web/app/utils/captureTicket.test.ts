import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { renderMarkdown } from './markdown'

/**
 * A ticket made from a capture (Program item PV1): the new ticket's descriptions and the sends' comments the store writes
 * (internal/dispatch's describe and sendComment), as internal/dispatch/testdata/capture-tickets.json holds them, written by
 * TestNothingACaptureHoldsIsMarkdownInATicket with every string a capture holds but the person's notes filled with words Markdown reads
 * as structure, each word with letters in it marked QZ. Each is rendered as the ticket page renders a ticket's text and its comments
 * (TicketMarkdown and DispatchComments: MarkdownPreview with no folder, relative links as words, bare addresses as links, and the
 * ticket's attachments), and holds the store's own elements alone, every mark inside one of the store's code blocks.
 */
const fixture = JSON.parse(readFileSync(fileURLToPath(new URL('../../../internal/dispatch/testdata/capture-tickets.json', import.meta.url)), 'utf8')) as {
  cases: Array<{ name: string; description: string; comment: string; blocks: number; marks: number; notes?: boolean }>
}

/** The options MarkdownPreview hands renderMarkdown on the ticket page. */
const asTheTicketPage = { dir: '', relativeLinks: false, attachments: true, linkify: true }

/** The elements the store's own words make: paragraphs, the warnings' and the notes' quotes and their bold, headings, code. */
const storeElements = new Set(['p', 'blockquote', 'strong', 'h3', 'pre', 'code'])

/** The HTML with its code blocks taken out (markdown-it escapes `<` in text, so a tag is markup). */
function outsideBlocks(html: string): string {
  return html.replace(/<pre\b[^>]*>[\s\S]*?<\/pre>/g, '')
}

/**
 * The elements opened outside every quote, in order: where a case's notes hold Markdown of their own (a fence, a list), the person's
 * elements stand inside their note's quote, and the store's alone outside it.
 */
function unquoted(html: string): string[] {
  const tags: string[] = []
  let depth = 0
  for (const [, close, tag] of html.matchAll(/<(\/?)([a-z][a-z0-9]*)\b/g)) {
    if (!close && depth === 0) tags.push(tag!)
    if (tag === 'blockquote') depth += close ? -1 : 1
  }
  return tags
}

describe('a ticket made from a capture', () => {
  it('reads the fixture', () => {
    expect(fixture.cases.length).toBeGreaterThan(100)
    expect(fixture.cases.some((c) => c.marks > 0)).toBe(true)
  })

  for (const c of fixture.cases) {
    it(`holds what the page gave in the store's blocks: ${c.name}`, () => {
      for (const [what, text, blocks, marks] of [
        ['description', c.description, c.blocks, c.marks],
        ['comment', c.comment, 0, 0],
      ] as const) {
        const { html, mermaid } = renderMarkdown(text, asTheTicketPage)
        const tags = c.notes ? unquoted(html) : [...html.matchAll(/<([a-z][a-z0-9]*)\b/g)].map((m) => m[1]!)
        expect(tags.filter((t) => !storeElements.has(t)), `${what}: no link, image, mention, list or raw element`).toEqual([])
        expect(mermaid, what).toBe(0)
        expect(tags.filter((t) => t === 'pre').length, `${what}: the store's blocks`).toBe(blocks)
        expect(outsideBlocks(html), `${what}: nothing marked outside a block`).not.toMatch(/QZ/)
        expect((html.match(/QZ/g) ?? []).length, `${what}: every mark inside a block`).toBe(marks)
        expect(html, `${what}: no address's user, password, query or fragment`).not.toMatch(/SECRET/)
      }
    })
  }
})
