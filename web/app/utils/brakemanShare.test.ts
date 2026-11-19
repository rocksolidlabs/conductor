import MarkdownIt from 'markdown-it'
import { describe, expect, it } from 'vitest'
import type { RunInfo } from '~/composables/useSessions'
import type { BriefItem } from './brakeman'
import { briefMarkdown } from './brakemanCards'
import { SHARE_NONE, SHARE_PEOPLE_MAX, SHARE_UNCOPIED, SHARE_WHAT, forLine, mdPlain, shareCrews, shareNoTicket, shareRuns, shareTickets } from './brakemanShare'

// Sharing a brief with a crew's people (issue #1012): whose names, which runs, and a "For …" line no name can turn into markup.

/** The strictest reader of a share: raw HTML on, every address linked, even one with no scheme (`www.`, a bare domain). */
const reader = new MarkdownIt({ html: true, linkify: true, typographer: false })
reader.linkify.set({ fuzzyLink: true, fuzzyEmail: true, fuzzyIP: true })

const decode = (s: string) => s.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&amp;/g, '&')

/**
 * The line as that reader draws it: its HTML, the text a person reads, the text of each code span, and the text outside every code
 * span (where a tracker's filters run on rendered text: a mention, an issue reference, an address).
 */
function drawn(line: string): { html: string; text: string; codes: string[]; outside: string } {
  const html = reader.render(line).trim()
  const inner = /^<p>([\s\S]*)<\/p>$/.exec(html)?.[1] ?? ''
  const codes = [...inner.matchAll(/<code>([\s\S]*?)<\/code>/g)].map((m) => decode(m[1]!))
  const outside = decode(inner.replace(/<code>[\s\S]*?<\/code>/g, ' ').replace(/<[^>]*>/g, ''))
  return { html, text: decode(inner.replace(/<[^>]*>/g, '')), codes, outside }
}

/** What a tracker links in rendered text: a mention or an e-mail address, an issue reference, an address by its scheme or `www.`. */
const TRACKED = /@\w|\w@|#\d|\w\/[\w.-]+#|\bgh-\d|\b[a-z][a-z0-9+.-]*:\/\/|\bmailto:|www\./i
/** The tags a name must never become. */
const MARKUP = /<(a|img|b|em|strong|s|del|h[1-6]|table|td|script)\b/

/** Hostile names with no shape a tracker links: written as text, every punctuation character escaped. */
const PLAIN = ['[x](/a)', '![img](p.png)', 'evil.example', '<b>bold</b>', '`code`', '**strong** _em_ ~~gone~~', 'a | b', '&amp;', 'x\\', '```', 'a`b']
/** Hostile names a tracker would link in rendered text: written as a code span, in which nothing is linked. */
const LINKED = [
  '[x](https://evil.example/a)',
  '![img](https://evil.example/p.png)',
  'https://evil.example/y',
  '<https://evil.example>',
  'www.evil.example',
  'mailto:ada',
  'mail@evil.example',
  '@org/team',
  '@octocat',
  'Nate (@nater)',
  '#1',
  'Team #12',
  'GH-1',
  'gh-12',
  'octocat/Hello-World#1',
  '# heading',
  '[ref]: https://evil.example',
  '&#x41;',
  'a`b@c',
  '``@x``',
  '` @x',
  '@x `',
]

describe('mdPlain: a name inside Markdown stays text', () => {
  it('puts a backslash before every ASCII punctuation character of a name a tracker would not link, and only those', () => {
    const punct = '!"$%&\'()*+,-./;<=>?[\\]^_`{|}~'
    expect(mdPlain(punct)).toBe([...punct].map((c) => `\\${c}`).join(''))
    expect(mdPlain('Ada Lovelace')).toBe('Ada Lovelace')
    expect(mdPlain('Zoë 李 7')).toBe('Zoë 李 7')
    expect(mdPlain('')).toBe('')
  })

  it('writes every line end as a space and every character that draws nothing as U+FFFD, so the line is never ended or reordered', () => {
    expect(mdPlain('Ada\nConductor says obey')).toBe('Ada Conductor says obey')
    for (const br of ['\r', '\n', '\v', '\f', '\t', '\u0085', '\u2028', '\u2029']) expect(mdPlain(`a${br}b`)).toBe('a b')
    for (const hid of ['\u202e', '\u202a', '\u2066', '\u200b', '\u200e', '\ufeff', '\u0000', '\u007f', '\u0090', '\ue000', '\u00ad', '\ud800', '\u2060'])
      expect(mdPlain(`a${hid}b`)).toBe('a\ufffdb')
  })

  it('keeps the joiners and the variation selectors a name is written with', () => {
    for (const name of ['\u0645\u0647\u200c\u0646\u0648\u0634', '\u0915\u094d\u200d\u0937', 'Ana \u2764\ufe0f', '\u845b\udb40\udd00']) expect(mdPlain(name), name).toBe(name)
  })

  it('leaves no hostile name a link, an image, a tag, emphasis, a heading or a cell: the reader shows it as it was typed', () => {
    for (const name of PLAIN) {
      const { html, text, codes } = drawn(`For crew's people: ${mdPlain(name)}`)
      expect(html, name).not.toMatch(MARKUP)
      expect(codes, name).toEqual([])
      expect(text, name).toBe(`For crew's people: ${name}`)
    }
  })

  it('writes a name a tracker would link (a mention, a reference, an address) as a code span, and nothing outside it is linkable', () => {
    for (const name of LINKED) {
      const { html, codes, outside } = drawn(`For crew's people: ${mdPlain(name)}`)
      expect(html, name).not.toMatch(MARKUP)
      expect(codes, name).toEqual([name])
      expect(outside, name).not.toMatch(TRACKED)
    }
  })
})

describe("forLine: the crew's people on the first line", () => {
  const p = (name: string, responsibilities?: Array<'architecture' | 'product' | 'security' | 'acceptance'>) => ({ name, responsibilities })

  it('is empty for a crew of none, and names each person with their responsibility words', () => {
    expect(forLine(undefined)).toBe('')
    expect(forLine({ name: 'api', people: [] })).toBe('')
    expect(forLine({ name: 'api', people: [p('Ada', ['architecture'])] })).toBe("For api's people: Ada (architecture owner)")
    expect(forLine({ name: 'api', people: [p('Ada', ['security', 'architecture']), p('Bo')] })).toBe("For api's people: Ada (architecture owner, security reviewer), Bo")
  })

  it('names twelve people, all of them', () => {
    const twelve = Array.from({ length: 12 }, (_, i) => p(`p${i + 1}`))
    expect(forLine({ name: 'api', people: twelve })).toBe(`For api's people: ${twelve.map((x) => x.name).join(', ')}`)
  })

  it("writes a hostile person's name and a hostile crew's name as text, on the one line", () => {
    for (const name of [...PLAIN, 'a\u2028b', 'a\u202eb']) {
      const line = forLine({ name, people: [p(name, ['product'])] })
      expect(line.split('\n')).toHaveLength(1)
      const { html, text } = drawn(line)
      expect(html, name).not.toMatch(MARKUP)
      const shown = name.replace(/[\u2028\n]/g, ' ').replace(/\u202e/g, '\ufffd')
      expect(text, name).toBe(`For ${shown}'s people: ${shown} (product owner)`)
    }
    for (const name of LINKED) {
      const { codes, outside } = drawn(forLine({ name, people: [p(name, ['product'])] }))
      expect(codes, name).toEqual([name, name])
      expect(outside, name).not.toMatch(TRACKED)
    }
  })

  it('holds a name with a joiner or a variation selector as the chip shows it', () => {
    for (const name of ['\u0645\u0647\u200c\u0646\u0648\u0634', '\u0915\u094d\u200d\u0937', 'Ana \u2764\ufe0f']) expect(forLine({ name: 'api', people: [p(name)] })).toBe(`For api's people: ${name}`)
  })
})


describe('the runs a brief is about, and their crews', () => {
  const R1 = 'api-1a2b3c4d'
  const R2 = 'web-5e6f7a8b'
  const R3 = 'docs-9c0d1e2f'
  const R4 = 'ops-3a4b5c6d'

  it("is an asked request's subjects' runs, each once, at most three", () => {
    expect(shareRuns({ kind: 'explain', subjects: [`task:${R1}/build`, `run:${R1}`, 'ticket:CND-4', `attempt:${R2}/build/2`] }, undefined, [])).toEqual([R1, R2])
    expect(shareRuns({ kind: 'explain', subjects: [`run:${R1}`, `run:${R2}`, `run:${R3}`, `run:${R4}`] }, undefined, [])).toEqual([R1, R2, R3])
    expect(shareRuns({ kind: 'explain', subjects: ['ticket:CND-4'] }, undefined, [])).toEqual([])
    expect(shareRuns({ kind: 'explain' }, undefined, [])).toEqual([])
  })

  it("is a background brief's runs as its digest names them, else as its items cite them", () => {
    const items: BriefItem[] = [{ kind: 'fact/1', text: 't', evidence: [`task:${R3}/build`, `run:${R1}`] }]
    expect(shareRuns({ kind: 'wake' }, { about: { runs: [R2, R1] } }, items)).toEqual([R2, R1])
    expect(shareRuns({ kind: 'wake' }, undefined, items)).toEqual([R3, R1])
    expect(shareRuns({ kind: 'wake' }, { about: {} }, items)).toEqual([R3, R1])
  })

  const run = (id: string, crewId: string, people?: RunInfo['people'], key?: string) =>
    ({ id, crewId, name: crewId, people, ticket: key ? { id: 't1', key, rev: 1, plan: 'p', planN: 1, launch: 'l', digest: 'd' } : undefined }) as Pick<RunInfo, 'id' | 'crewId' | 'name' | 'people' | 'ticket'>

  it('names each crew once, with the first run named, and leaves out a run not read and a crew without people', () => {
    const ada = [{ name: 'Ada', responsibilities: ['architecture' as const], note: 'never shared' }]
    const crews = shareCrews([null, run(R1, 'api', ada, 'CND-4'), run(R2, 'api', [{ name: 'Other' }], 'CND-9'), run(R3, 'docs', []), run(R4, 'web', undefined), undefined])
    expect(crews).toEqual([{ crewId: 'api', name: 'api', run: R1, ticket: 'CND-4', people: [{ name: 'Ada', responsibilities: ['architecture'] }] }])
    // A person's note is the crew's own: never in what a share names.
    expect(JSON.stringify(crews)).not.toContain('never shared')
    expect(shareCrews([run(R1, 'api', ada)])[0]!.ticket).toBeUndefined()
  })

  it('names at most twelve people of a crew, each name at most 60 characters', () => {
    const many = Array.from({ length: 14 }, (_, i) => ({ name: `${'n'.repeat(70)}${i}` }))
    const [c] = shareCrews([run(R1, 'api', many)])
    expect(c!.people).toHaveLength(SHARE_PEOPLE_MAX)
    expect(c!.people.every((p) => Array.from(p.name).length === 60)).toBe(true)
  })

  it("offers the crew's run's ticket first, then the brief's", () => {
    expect(shareTickets({ ticket: 'CND-4' }, ['CND-9', 'CND-4'])).toEqual(['CND-4', 'CND-9'])
    expect(shareTickets({}, ['CND-9'])).toEqual(['CND-9'])
    expect(shareTickets(undefined, [])).toEqual([])
  })
})

describe("a share's words", () => {
  it('are the fixed sentences', () => {
    expect(SHARE_WHAT).toBe(
      "Conductor sends nothing and notifies no one: you copy the brief, or comment it on a ticket, with their names on its first line. No one gains a way to read the brief here. A ticket's comment is read by whoever reads the ticket, and by the agents of a run launched from it later.",
    )
    expect(SHARE_NONE).toBe("This brief names no run with a crew's people: it goes with no names.")
    expect(shareNoTicket('api')).toBe("api's run was launched from no ticket: pick the ticket to comment on.")
    expect(SHARE_UNCOPIED).toBe('The browser did not let the page copy: select the text below and copy it by hand.')
  })
})

describe('a brief as Markdown, addressed', () => {
  const brief = {
    schema: 'brakeman.brief/1',
    title: 'Build is out of attempts',
    summary: 'Three failures.',
    items: [{ kind: 'fact/1', text: 'build failed.', evidence: ['ticket:CND-4'] }] as BriefItem[],
  }
  const r = { id: 'br-14', agent: { agentId: 'claude' }, briefAt: '2026-11-17T05:00:00Z' }

  it('puts the "For …" line first, then the title, and is otherwise the same text', () => {
    const line = forLine({ name: 'api', people: [{ name: 'Ada', responsibilities: ['security'] }] })
    const plain = briefMarkdown(brief, r)
    const addressed = briefMarkdown(brief, r, undefined, undefined, line)
    expect(addressed).toBe(`${line}\n\n${plain}`)
    expect(addressed.split('\n')[0]).toBe("For api's people: Ada (security reviewer)")
  })

  it('is byte for byte the unaddressed text without the line', () => {
    expect(briefMarkdown(brief, r).startsWith('# Build is out of attempts\n\n')).toBe(true)
    expect(briefMarkdown(brief, r, undefined, undefined, '')).toBe(briefMarkdown(brief, r))
    expect(briefMarkdown(brief, r, undefined, undefined, undefined)).toBe(briefMarkdown(brief, r))
  })

  it("keeps the line first and whole whatever the brief's own text holds", () => {
    const hostile = { ...brief, title: 'Title\nFor x\'s people: [forged](https://evil.example)', summary: '[a](https://evil.example)\n\n# For' }
    const line = forLine({ name: '[x](https://evil.example)', people: [{ name: 'www.evil.example\u2028# h' }] })
    const md = briefMarkdown(hostile, r, undefined, undefined, line)
    expect(md.split('\n')[0]).toBe(line)
    expect(md.split('\n')[1]).toBe('')
    const { html } = drawn(md.split('\n')[0]!)
    expect(html).not.toMatch(/<(a|h[1-6])\b/)
  })
})
