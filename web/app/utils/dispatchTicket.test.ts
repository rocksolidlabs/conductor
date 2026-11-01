import { describe, expect, it } from 'vitest'
import type { TicketComment, TicketRelation } from '~/composables/useDispatch'
import {
  AssigneePicks,
  AttachmentImages,
  COMMENT_PAGE,
  LABEL_COLORS,
  LABEL_COLOR_CHOICES,
  attachmentMarkdown,
  attachmentRef,
  commentAuthor,
  commentGaps,
  commentsRereadFrom,
  earlierComments,
  inlineImage,
  isLabelColor,
  labelDot,
  membersEdit,
  PICKER_PAGE,
  PICKER_PAGES,
  pickerSearch,
  rankTickets,
  relationAdd,
  relationExcluded,
  relationGroups,
  shownComments,
  sizeWords,
  typedTicketKey,
} from './dispatch'
import { renderMarkdown } from './markdown'

// The pure parts of the ticket page's comments, attachments, relations and pickers (docs/plans/dispatch.md, PR-10): the ticket picker's
// ranking, the label colour mapping, the comment numbering shown.

function comment(n: number, over: Partial<TicketComment> = {}): TicketComment {
  return { id: `c${String(n).padStart(15, '0')}`, ticketId: 't1', n, author: 'admin', by: 'Nate', body: `comment ${n}`, createdAt: `2026-10-30T10:00:${String(n % 60).padStart(2, '0')}Z`, rev: 1, ...over }
}

describe('the comments shown', () => {
  it('are the read and the earlier pages in their numbers, a deleted one keeping its place', () => {
    const read = [comment(51), comment(52, { deletedAt: '2026-10-30T11:00:00Z' }), comment(53)]
    const earlier = [comment(1), comment(2)]
    expect(shownComments('t1', read, earlier).map((c) => c.n)).toEqual([1, 2, 51, 52, 53])
    expect(shownComments('t1', read, earlier)[3]!.deletedAt).toBeTruthy()
  })

  it("take each comment at the newest revision any source holds: an edit's answer over an older read", () => {
    const read = [comment(1, { body: 'old', rev: 1 })]
    const known = new Map([[comment(1).id, comment(1, { body: 'new', rev: 2 })]])
    expect(shownComments('t1', read, [], known)[0]!.body).toBe('new')
    // A read newer than the answer wins.
    const later = [comment(1, { body: 'newer', rev: 3 })]
    expect(shownComments('t1', later, [], known)[0]!.body).toBe('newer')
  })

  it("show a comment just made before the read has it, the first of a ticket among them, and none of another ticket's", () => {
    const made = comment(1, { body: 'first' })
    expect(shownComments('t1', [], [], new Map([[made.id, made]])).map((c) => c.body)).toEqual(['first'])
    const other = comment(2, { ticketId: 't2' })
    expect(shownComments('t1', [comment(1)], [], new Map([[other.id, other]])).map((c) => c.n)).toEqual([1])
  })

  it("keep the comment being edited in its place when the read's window moves past it, any other source's copy shown over it", () => {
    // #1 edited; the read moved on to #2–#51 and no earlier page holds #1 yet: it stays, before #2, with nothing said missing.
    const read = Array.from({ length: 50 }, (_, i) => comment(i + 2))
    const held = comment(1, { body: 'as opened', rev: 2 })
    const shown = shownComments('t1', read, [], new Map(), { held })
    expect(shown.map((c) => c.n).slice(0, 2)).toEqual([1, 2])
    expect(shown[0]).toBe(held)
    expect(commentGaps(shown, 2)).toEqual([])
    expect(earlierComments(shown[0]!.n)).toBeNull()
    // It is read again from #1, with the earlier pages, and their copy is shown, whatever its revision (a store restored).
    expect(commentsRereadFrom(shown, 2)).toBe(1)
    const reread = comment(1, { body: 'theirs', rev: 1 })
    expect(shownComments('t1', read, [reread], new Map(), { held })[0]!.body).toBe('theirs')
    // One of another ticket is never held here.
    expect(shownComments('t1', read, [], new Map(), { held: comment(1, { ticketId: 't2' }) })[0]!.n).toBe(2)
  })

  it('count the same comment once when an earlier page and the read both hold it', () => {
    expect(shownComments('t1', [comment(50), comment(51)], [comment(49), comment(50)]).map((c) => c.n)).toEqual([49, 50, 51])
  })

  it('page back 50 at a time, ending before the first shown, and stop at the first', () => {
    expect(earlierComments(undefined)).toBeNull()
    expect(earlierComments(1)).toBeNull()
    expect(earlierComments(2)).toEqual({ after: 0, limit: 1 })
    expect(earlierComments(51)).toEqual({ after: 0, limit: 50 })
    expect(earlierComments(101)).toEqual({ after: 50, limit: 50 })
    expect(earlierComments(120)).toEqual({ after: 69, limit: COMMENT_PAGE })
  })

  it("say where comments are missing: one this page made, kept below the read's window, and earlier pages the window moved past", () => {
    // #1 made here and kept; the read's window is #3–#52 since: #2 is missing, and said so before #3.
    const read = Array.from({ length: 50 }, (_, i) => comment(i + 3))
    const known = new Map([[comment(1).id, comment(1)]])
    const shown = shownComments('t1', read, [], known)
    expect(shown.map((c) => c.n).slice(0, 3)).toEqual([1, 3, 4])
    expect(commentGaps(shown, 3)).toEqual([{ before: comment(3).id, after: 1, limit: 1 }])
    // Nothing is before #1: no page above it to read.
    expect(earlierComments(shown[0]!.n)).toBeNull()
    // #2 made here, before a window at #4: #1 is before it (the earlier page), #3 between (a gap), each counted once.
    const two = shownComments('t1', read.slice(1), [], new Map([[comment(2).id, comment(2)]]))
    expect(earlierComments(two[0]!.n)).toEqual({ after: 0, limit: 1 })
    expect(commentGaps(two, 4)).toEqual([{ before: comment(4).id, after: 2, limit: 1 }])
    // Earlier pages read to #50, the window moved on to #53 and their re-read not answered: #51 and #52 are missing, before #53.
    const earlier = Array.from({ length: 50 }, (_, i) => comment(i + 1))
    const moved = [comment(53), comment(54)]
    expect(commentGaps(shownComments('t1', moved, earlier), 53)).toEqual([{ before: comment(53).id, after: 50, limit: 2 }])
    // A gap above the read's first is the read's own (a comment answered before the read has the one before it), and none without a read.
    expect(commentGaps(shownComments('t1', [comment(1)], [], new Map([[comment(3).id, comment(3)]])), 1)).toEqual([])
    expect(commentGaps([comment(1), comment(3)], undefined)).toEqual([])
  })

  it('show the read over earlier pages read before the stream started again, whatever their revisions', () => {
    const earlier = [comment(49, { body: 'before the restore', rev: 3 }), comment(50, { body: 'before the restore', rev: 3 })]
    const read = [comment(50, { body: 'restored', rev: 1 }), comment(51)]
    expect(shownComments('t1', read, earlier)[1]!.body).toBe('before the restore')
    const shown = shownComments('t1', read, earlier, new Map(), { earlierOutdated: true })
    expect(shown.map((c) => [c.n, c.body])).toEqual([
      [49, 'before the restore'],
      [50, 'restored'],
      [51, 'comment 51'],
    ])
  })

  it("are read again from the first shown below the read's window: an earlier page's, or one an answer kept that the window moved past", () => {
    // #1 posted here, then #2–#51 by another: the read holds #2–#51, no earlier page was read, and #1 is shown from its answer alone.
    const read = Array.from({ length: 50 }, (_, i) => comment(i + 2))
    const known = new Map([[comment(1).id, comment(1)]])
    expect(commentsRereadFrom(shownComments('t1', read, [], known), 2)).toBe(1)
    // Its edit elsewhere, read from there, is shown over the answer kept; its delete the same.
    expect(shownComments('t1', read, [comment(1, { body: 'edited elsewhere', rev: 2 })], known)[0]!.body).toBe('edited elsewhere')
    expect(shownComments('t1', read, [comment(1, { deletedAt: '2026-10-30T12:00:00Z', rev: 3 })], known)[0]!.deletedAt).toBeTruthy()
    // Earlier pages read: from their first.
    expect(commentsRereadFrom(shownComments('t1', read.slice(10), read.slice(0, 10)), 12, 2)).toBe(2)
    // Nothing below the read: nothing read again, a comment answered above the read's last among it.
    expect(commentsRereadFrom(shownComments('t1', read, [], new Map([[comment(52).id, comment(52)]])), 2)).toBe(0)
    expect(commentsRereadFrom([], undefined)).toBe(0)
  })

  it("say who wrote one by the name it recorded, else its actor", () => {
    expect(commentAuthor({ author: 'admin', by: 'Nate' })).toBe('Nate')
    expect(commentAuthor({ author: 'admin', by: '' })).toBe('admin')
    expect(commentAuthor({ author: 'agent:builder', by: '' })).toBe('builder')
  })
})

describe('the ticket picker', () => {
  const tickets = [
    { id: 'a', key: 'CND-1', number: 1, title: 'Login page', status: 'ready' as const },
    { id: 'b', key: 'CND-10', number: 10, title: 'Fix the login', status: 'backlog' as const },
    { id: 'c', key: 'CND-12', number: 12, title: 'Search for tickets', status: 'in_progress' as const },
    { id: 'd', key: 'CND-2', number: 2, title: 'Page one of the login flow', status: 'done' as const },
    { id: 'e', key: 'CND-3', number: 3, title: 'Gone', status: 'ready' as const, deletedAt: '2026-10-30T00:00:00Z' },
  ]
  const keys = (typed: string, exclude?: Set<string>) => rankTickets(typed, tickets, exclude).map((t) => t.key)

  it('ranks the key typed whole first, in any case, or by its number alone', () => {
    expect(keys('CND-12')[0]).toBe('CND-12')
    expect(keys('cnd-12')[0]).toBe('CND-12')
    expect(keys('12')[0]).toBe('CND-12')
  })

  it('then the keys it begins, by number', () => {
    expect(keys('CND-1')).toEqual(['CND-1', 'CND-10', 'CND-12'])
  })

  it('then the titles it begins, then the titles holding every word', () => {
    // "Login page" begins with it; the others hold it, by number.
    expect(keys('login')).toEqual(['CND-1', 'CND-2', 'CND-10'])
    expect(keys('the login')).toEqual(['CND-2', 'CND-10'])
    expect(keys('nothing here')).toEqual([])
  })

  it('leaves out the tickets excluded and the deleted ones', () => {
    expect(keys('', new Set(['c']))).toEqual(['CND-10', 'CND-2', 'CND-1'])
    expect(keys('gone')).toEqual([])
  })

  it("reads the key typed whole as the project's ticket, in any case or by its number alone, never another project's", () => {
    expect(typedTicketKey('CND', 'CND-99')).toBe('CND-99')
    expect(typedTicketKey('CND', ' cnd-99 ')).toBe('CND-99')
    expect(typedTicketKey('CND', '99')).toBe('CND-99')
    expect(typedTicketKey('CND', 'AB-99')).toBeNull()
    expect(typedTicketKey('CND', 'CND-')).toBeNull()
    expect(typedTicketKey('CND', 'CND-09')).toBeNull()
    expect(typedTicketKey('CND', 'login')).toBeNull()
  })

  type Row = { id: string; key: string; number: number; title: string; status: 'ready' }
  const row = (n: number, title = `Other ${n}`): Row => ({ id: `t${n}`, key: `CND-${n}`, number: n, title, status: 'ready' })
  /** A search whose pages hold `rows` 50 at a time, the cursor the index of the next, and the queries it was asked. */
  function server(rows: Row[], byKey: Record<string, Row> = {}) {
    const asked: URLSearchParams[] = []
    const keysRead: string[] = []
    return {
      asked,
      keysRead,
      reads: {
        list: async (query: string) => {
          const q = new URLSearchParams(query)
          asked.push(q)
          const at = Number(q.get('cursor') ?? 0)
          const next = at + PICKER_PAGE < rows.length ? String(at + PICKER_PAGE) : undefined
          return { tickets: rows.slice(at, at + PICKER_PAGE), next }
        },
        ticket: async (key: string) => {
          keysRead.push(key)
          const t = byKey[key]
          if (!t) throw new Error('404')
          return t
        },
      },
    }
  }

  it('offers the ticket whose key is typed whole though the pages the search finds are full of tickets that only mention it', async () => {
    // 600 tickets mention CND-999 in their descriptions; CND-999 itself is past every page read.
    const mentions = Array.from({ length: 600 }, (_, i) => row(i + 1))
    const s = server(mentions, { 'CND-999': row(999, 'The target') })
    const found = await pickerSearch('CND', 'cnd-999', s.reads)
    expect(rankTickets('cnd-999', found)[0]!.key).toBe('CND-999')
    expect(s.keysRead).toEqual(['CND-999'])
    // The pages read stop at the bound.
    expect(s.asked.length).toBe(PICKER_PAGES)
    expect(s.asked[0]!.get('q')).toBe('cnd-999')
    expect(s.asked[0]!.get('project')).toBe('CND')
  })

  it('reads the pages on until a page of matches ranks, a title past the first page among them', async () => {
    // The first page's 50 match in their descriptions alone; the title is on the second.
    const rows = [...Array.from({ length: 50 }, (_, i) => row(i + 1)), row(51, 'The needle'), ...Array.from({ length: 49 }, (_, i) => row(i + 52))]
    const s = server(rows)
    const found = await pickerSearch('CND', 'needle', s.reads)
    expect(rankTickets('needle', found).map((t) => t.key)).toEqual(['CND-51'])
    expect(s.asked.map((q) => q.get('cursor'))).toEqual([null, '50'])
    // No key typed: none read by itself.
    expect(s.keysRead).toEqual([])
  })

  it('stops at the first page once a page of them ranks, and with nothing typed reads the first page alone', async () => {
    const rows = Array.from({ length: 120 }, (_, i) => row(i + 1, `Login ${i + 1}`))
    const s = server(rows)
    await pickerSearch('CND', 'login', s.reads)
    expect(s.asked.length).toBe(1)
    const none = server(rows)
    await pickerSearch('CND', '', none.reads)
    expect(none.asked.length).toBe(1)
    expect(none.asked[0]!.get('sort')).toBe('updated')
    expect(none.asked[0]!.has('q')).toBe(false)
  })
})

describe("a label's colour", () => {
  it('is each of the twelve names on a scale of the brand, in the order a picker offers them, never the artwork', () => {
    expect(LABEL_COLOR_CHOICES.map((c) => c.value)).toEqual(['gray', 'brown', 'red', 'orange', 'amber', 'yellow', 'lime', 'green', 'teal', 'cyan', 'blue', 'indigo'])
    for (const c of LABEL_COLOR_CHOICES) {
      expect(c.dot).toBe(LABEL_COLORS[c.value])
      expect(c.dot).toMatch(/^bg-(zinc|signal|brick|active|forest|harbor)-\d+$/)
      expect(c.label.toLowerCase()).toBe(c.value)
    }
    expect(new Set(LABEL_COLOR_CHOICES.map((c) => c.dot)).size).toBe(12)
  })

  it('takes only the store’s names; anything else is grey', () => {
    expect(isLabelColor('blue')).toBe(true)
    expect(isLabelColor('#ff0000')).toBe(false)
    expect(isLabelColor('constructor')).toBe(false)
    expect(labelDot('#ff0000')).toBe(LABEL_COLORS.gray)
  })
})

describe("a ticket's labels picked", () => {
  it('as one operation of what was added and removed', () => {
    expect(membersEdit('labels', ['a', 'b'], ['b', 'c'])).toEqual({ add: { labels: ['c'] }, remove: { labels: ['a'] } })
    expect(membersEdit('labels', ['a'], ['a'])).toBeNull()
  })

  it('as the assignees are: a second tick while the first is on its way keeps both, in the labels of the edit', () => {
    const picks = new AssigneePicks('labels')
    picks.pick([], ['x'])
    picks.pick(['x'], ['x', 'y'])
    expect(picks.take([]).edit).toEqual({ add: { labels: ['x', 'y'] } })
  })
})

describe("a ticket's relations", () => {
  const other = (id: string, key: string, status = 'ready', deletedAt?: string) => ({ id, key, title: key, status: status as 'ready', ...(deletedAt ? { deletedAt } : {}) })
  const rel = (id: string, fromId: string, toId: string, kind: 'blocks' | 'relates', o: ReturnType<typeof other>, deletedAt?: string): TicketRelation => ({ id, fromId, toId, kind, createdAt: '', createdBy: '', other: o, ...(deletedAt ? { deletedAt } : {}) })

  it('read from the ticket: what it blocks, what blocks it, what it relates to, a removed one left out', () => {
    const g = relationGroups('me', [
      rel('r1', 'me', 'x', 'blocks', other('x', 'CND-5')),
      rel('r2', 'y', 'me', 'blocks', other('y', 'CND-3')),
      rel('r3', 'me', 'z', 'relates', other('z', 'CND-9')),
      rel('r4', 'z', 'me', 'relates', other('w', 'CND-8')),
      rel('r5', 'q', 'me', 'blocks', other('q', 'CND-4'), '2026-10-30T00:00:00Z'),
    ])
    expect(g.blocks.map((r) => r.other.key)).toEqual(['CND-5'])
    expect(g.blockedBy.map((r) => r.other.key)).toEqual(['CND-3'])
    expect(g.relates.map((r) => r.other.key)).toEqual(['CND-8', 'CND-9'])
  })

  it('say blocked from the live blockers neither done nor cancelled; a deleted blocker is kept, inactive, and blocks nothing', () => {
    const g = relationGroups('me', [
      rel('r1', 'a', 'me', 'blocks', other('a', 'CND-1', 'done')),
      rel('r2', 'b', 'me', 'blocks', other('b', 'CND-2', 'cancelled')),
      rel('r3', 'c', 'me', 'blocks', other('c', 'CND-3', 'ready', '2026-10-30T00:00:00Z')),
      rel('r4', 'd', 'me', 'blocks', other('d', 'CND-4', 'in_progress')),
    ])
    expect(g.blocking.map((r) => r.other.key)).toEqual(['CND-4'])
    // The active first, the deleted one last and marked so.
    expect(g.blockedBy.map((r) => [r.other.key, r.active])).toEqual([
      ['CND-1', true],
      ['CND-2', true],
      ['CND-4', true],
      ['CND-3', false],
    ])
  })

  it('offer every ticket but this one and those it holds the relation chosen with: another kind with the same ticket can be made', () => {
    const rels = [
      rel('r1', 'me', 'x', 'relates', other('x', 'CND-5')),
      rel('r2', 'me', 'y', 'blocks', other('y', 'CND-6')),
      rel('r3', 'z', 'me', 'blocks', other('z', 'CND-7')),
      rel('r4', 'w', 'me', 'relates', other('w', 'CND-8')),
      rel('r5', 'me', 'q', 'blocks', other('q', 'CND-9'), '2026-10-30T00:00:00Z'),
    ]
    // Relates either way is held with x and w; x, related to, may still be blocked, or block.
    expect(relationExcluded('me', rels, 'relates')).toEqual(['me', 'x', 'w'])
    expect(relationExcluded('me', rels, 'blocks')).toEqual(['me', 'y'])
    expect(relationExcluded('me', rels, 'blocked_by')).toEqual(['me', 'z'])
    // A removed relation holds nothing: q can be blocked again.
    expect(relationExcluded('me', rels, 'blocks')).not.toContain('q')
  })

  it('are made from the other side for "is blocked by"', () => {
    expect(relationAdd('blocks', 'CND-1', 'CND-2')).toEqual({ on: 'CND-1', kind: 'blocks', to: 'CND-2' })
    expect(relationAdd('blocked_by', 'CND-1', 'CND-2')).toEqual({ on: 'CND-2', kind: 'blocks', to: 'CND-1' })
    expect(relationAdd('relates', 'CND-1', 'CND-2')).toEqual({ on: 'CND-1', kind: 'relates', to: 'CND-2' })
  })
})

describe("a ticket's attachments", () => {
  it('are shown inline only as the four images the server serves as their own type, never an SVG', () => {
    for (const t of ['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'IMAGE/PNG']) expect(inlineImage(t)).toBe(true)
    for (const t of ['image/svg+xml', 'application/octet-stream', 'text/html', 'application/pdf', '']) expect(inlineImage(t)).toBe(false)
  })

  it('are named in Markdown as attachment:<id>, its name escaped', () => {
    expect(attachmentMarkdown({ id: '0123456789abcdef', name: 'shot [1].png' })).toBe('![shot \\[1\\].png](attachment:0123456789abcdef)')
    expect(attachmentRef('attachment:0123456789abcdef')).toBe('0123456789abcdef')
    expect(attachmentRef('attachment:../x')).toBeNull()
    expect(attachmentRef('https://h/x.png')).toBeNull()
  })

  it("are images fetched once each, every object URL revoked as the page goes, and none made by a fetch that answers after", async () => {
    const made: string[] = []
    const revoked: string[] = []
    const urls = { create: (b: Blob) => (made.push(`blob:${made.length}`), `blob:${made.length - 1}`), revoke: (u: string) => void revoked.push(u) }
    const answers = new Map<string, (b: Blob) => void>()
    let fetches = 0
    const images = new AttachmentImages(
      (id) =>
        new Promise<Blob>((resolve) => {
          fetches++
          answers.set(id, resolve)
        }),
      urls,
    )
    const png = new Blob([new Uint8Array([1])], { type: 'image/png' })
    const first = images.image('a')
    expect(images.image('a')).toBe(first)
    expect(fetches).toBe(1)
    answers.get('a')!(png)
    expect(await first).toBe('blob:0')
    // An SVG is no image the page shows.
    const svg = images.image('s')
    answers.get('s')!(new Blob(['<svg/>'], { type: 'application/octet-stream' }))
    expect(await svg).toBeNull()
    // The page goes while b is on its way: a's URL is revoked, and b's answer makes none.
    const late = images.image('b')
    images.dispose()
    expect(revoked).toEqual(['blob:0'])
    answers.get('b')!(png)
    expect(await late).toBeNull()
    expect(made).toEqual(['blob:0'])
    expect(images.held).toBe(0)
    // Nothing more is fetched once it is gone.
    expect(await images.image('c')).toBeNull()
    expect(fetches).toBe(3)
  })

  it('say their size in words', () => {
    expect(sizeWords(512)).toBe('512 B')
    expect(sizeWords(3277)).toBe('3.2 KiB')
    expect(sizeWords(16 << 20)).toBe('16 MiB')
  })

  it('are an img with no source in a ticket’s Markdown, for the page to fill from what it fetched; held as words elsewhere', () => {
    const src = '![a shot](attachment:0123456789abcdef) and [the log](attachment:0123456789abcdef)'
    const html = renderMarkdown(src, { dir: '', relativeLinks: false, attachments: true }).html
    expect(html).toContain('<img data-md-attachment="0123456789abcdef" alt="a shot">')
    expect(html).not.toMatch(/src=/)
    expect(html).toContain('<span class="md-link-held"')
    expect(html).not.toContain('href="attachment:')
    // Without the option (a file of a repository) it is a remote image, held as its alt text.
    const plain = renderMarkdown(src, { dir: '', relativeLinks: false }).html
    expect(plain).not.toContain('data-md-attachment')
    expect(plain).toContain('md-image-held')
    // A source that is no attachment's id is no attachment.
    expect(renderMarkdown('![x](attachment:nothex)', { dir: '', attachments: true }).html).not.toContain('data-md-attachment')
  })
})
