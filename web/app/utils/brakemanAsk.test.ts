import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { KINDS } from './brakeman'
import { MAX_SUBJECTS, askChips, askDisabled, askedToast, briefingChipLabel, chipRequest, kindLabel, lineRequest, mentionMatches, refused, uniqueRefs, type MentionSources } from './brakemanAsk'

// The ask bar's chips and requests (the Brakeman design review), against the server's request rules (internal/brakeman/request.go).

const RUN = 'fix-login-1a2b3c4d'
const OTHER = 'checkout-migration-0c1d2e3f'

describe('the chips each place offers', () => {
  it("offers on the Brakeman page the kinds' own words, with … only where a picker opens", () => {
    const chips = askChips({ page: 'brakeman', lastBriefAt: '2026-11-17T02:55:00Z' }, 'UTC')
    expect(chips.map((c) => c.label)).toEqual(['Brief me since 02:55', 'Explain this…', 'Prepare a decision…', 'Draft tickets…', 'Propose a crew…', 'Prepare a handoff…', 'Overlapping changes', 'Find…'])
    expect(chips.map((c) => c.key)).toEqual(['briefing', 'explain', 'decision', 'draft_tickets', 'propose_crew', 'handoff', 'overlaps', 'locate'])
    expect(chips.map((c) => c.more ?? '')).toEqual(['since', 'subject', 'subject', 'subject', 'subject', 'subject', '', 'note'])
    // No chip asks a comparison: its page does.
    expect(chips.some((c) => c.kind === 'compare')).toBe(false)
    // Every label is a kind's own (KINDS), never a second name for it.
    for (const c of chips.slice(1)) expect(KINDS.map((k) => k.label.replace(/…$/, ''))).toContain(c.label.replace(/…$/, ''))
  })

  it('briefs since yesterday with no briefing kept, and says the time in the browser\'s zone', () => {
    expect(askChips({ page: 'brakeman' })[0]!.label).toBe('Brief me since yesterday')
    expect(briefingChipLabel('2026-11-17T02:55:00Z', 'America/Chicago')).toBe('Brief me since 20:55')
    expect(briefingChipLabel('not a time')).toBe('Brief me since yesterday')
  })

  it("offers on a run its own explain, a handoff for the selected task or the one that needs a person, and the overlaps", () => {
    expect(askChips({ run: RUN, selectedTask: 'deploy', needsTask: 'build' })).toEqual([
      { key: 'explain', label: 'Explain this run', kind: 'explain', subjects: [`run:${RUN}`] },
      { key: 'handoff', label: 'Prepare a handoff for deploy', kind: 'handoff', subjects: [`task:${RUN}/deploy`] },
      { key: 'overlaps', label: 'Overlapping changes', kind: 'overlaps' },
    ])
    expect(askChips({ run: RUN, needsTask: 'build' })[1]!.subjects).toEqual([`task:${RUN}/build`])
    expect(askChips({ run: RUN }).map((c) => c.key)).toEqual(['explain', 'overlaps'])
  })

  it('offers on a ticket four kinds of the ticket, each sent at once', () => {
    const chips = askChips({ ticket: 'CND-41' })
    expect(chips.map((c) => c.label)).toEqual(['Explain this ticket', 'Prepare a decision', 'Draft tickets', 'Propose a crew'])
    for (const c of chips) {
      expect(c.subjects).toEqual(['ticket:CND-41'])
      expect(c.more).toBeUndefined()
      expect(refused(chipRequest(c, []))).toBe(false)
    }
  })

  it('makes each chip a body the server takes, per place', () => {
    const page = askChips({ page: 'brakeman' })
    expect(chipRequest(page[0]!, [])).toEqual({ kind: 'briefing' })
    expect(chipRequest(page[0]!, [], '', '2026-11-16T18:00:00.000Z')).toEqual({ kind: 'briefing', since: '2026-11-16T18:00:00.000Z' })
    expect(chipRequest(page[1]!, [`task:${RUN}/build`], 'why did it fail?')).toEqual({ kind: 'explain', subjects: [`task:${RUN}/build`], note: 'why did it fail?' })
    expect(chipRequest(page[6]!, [])).toEqual({ kind: 'overlaps' })
    expect(chipRequest(page[7]!, [], 'the login handler')).toEqual({ kind: 'locate', note: 'the login handler' })
    // Draft tickets takes a ticket, or the discussion in the line.
    expect(chipRequest(page[3]!, [], 'split the migration in three')).toEqual({ kind: 'draft_tickets', note: 'split the migration in three' })
    const run = askChips({ run: RUN, selectedTask: 'build' })
    expect(chipRequest(run[0]!, [])).toEqual({ kind: 'explain', subjects: [`run:${RUN}`] })
    expect(chipRequest(run[1]!, [])).toEqual({ kind: 'handoff', subjects: [`task:${RUN}/build`] })
  })

  it("refuses a chip whose kind needs a subject none gives, a subject it takes none of, and a Find with a subject, in the server's words", () => {
    const page = askChips({ page: 'brakeman' })
    expect(chipRequest(page[1]!, [])).toEqual({ problem: 'Explain this names one subject.' })
    expect(chipRequest(page[4]!, [`run:${RUN}`])).toEqual({ problem: 'Propose a crew names a ticket.' })
    expect(chipRequest(page[3]!, [])).toEqual({ problem: 'Name a ticket or a comment, or write the discussion in the note.' })
    expect(chipRequest(page[7]!, [`run:${RUN}`], 'x')).toEqual({ problem: 'Find… names no subject.' })
    expect(chipRequest(page[7]!, [])).toEqual({ problem: 'Find… is its note: write it.' })
    expect(chipRequest(page[6]!, [], '', '2026-11-16T18:00:00.000Z')).toEqual({ problem: 'Only a briefing starts at a time.' })
  })
})

describe('a typed line', () => {
  it("is a question about the page's subject and each ref named with @, each once", () => {
    expect(lineRequest('what is ui waiting on?', [`task:${RUN}/ui`, `run:${RUN}`, `task:${RUN}/ui`], `run:${RUN}`)).toEqual({ kind: 'ask', subjects: [`run:${RUN}`, `task:${RUN}/ui`], note: 'what is ui waiting on?' })
    expect(lineRequest('what changed since last night?', [])).toEqual({ kind: 'ask', note: 'what changed since last night?' })
  })

  it(`names at most ${MAX_SUBJECTS} subjects, as the server takes, and needs its words`, () => {
    const ten = Array.from({ length: 10 }, (_, i) => `ticket:CND-${i + 1}`)
    expect(refused(lineRequest('which?', ten))).toBe(false)
    expect(lineRequest('which?', [...ten, 'ticket:CND-11'])).toEqual({ problem: 'A question names at most 10 subjects.' })
    expect(lineRequest('  ', [])).toEqual({ problem: 'A question is its note: write it.' })
    expect(lineRequest('x', ['nothing'])).toEqual({ problem: 'nothing is no ref.' })
  })

  it('takes the most subjects the server does', () => {
    const src = readFileSync(new URL('../../../internal/brakeman/request.go', import.meta.url), 'utf8')
    expect(Number(/MaxSubjects = (\d+)/.exec(src)![1])).toBe(MAX_SUBJECTS)
  })

  it('keeps refs in the order first named, dropping the empty', () => {
    expect(uniqueRefs([undefined, 'a', 'b', 'a', ''])).toEqual(['a', 'b'])
  })
})

describe('the @ list', () => {
  const src: MentionSources = {
    runs: [
      { id: OTHER, name: 'checkout', label: 'checkout-migration', tasks: [{ id: 'migrate' }] },
      { id: RUN, name: 'fix-login', tasks: [{ id: 'build' }, { id: 'ui' }] },
    ],
    sessions: [{ id: '0123456789abcdef', name: 'docs sweep' }],
    questions: [{ ref: '0123456789abcdef'.replace(/^/, 'question:') + '/fedcba9876543210', title: 'allow npm test?', session: 'docs sweep' }],
    tickets: [{ key: 'CND-41', title: 'Login keys the session by tenant' }],
  }

  it('offers what the page holds, the page\'s own run first, narrowed to the kinds asked', () => {
    expect(mentionMatches(src, '', [], `run:${RUN}`).map((m) => m.ref)).toEqual([
      `run:${RUN}`,
      `task:${RUN}/build`,
      `task:${RUN}/ui`,
      `run:${OTHER}`,
      `task:${OTHER}/migrate`,
      'session:0123456789abcdef',
      'question:0123456789abcdef/fedcba9876543210',
      'ticket:CND-41',
    ])
    expect(mentionMatches(src, '', ['ticket']).map((m) => m.ref)).toEqual(['ticket:CND-41'])
    expect(mentionMatches(src, 'build', ['task', 'attempt', 'session']).map((m) => m.label)).toEqual(['build of fix-login'])
  })

  it('shows a few of each kind before anything is typed, so many sessions never hide the tickets', () => {
    const many: MentionSources = { ...src, sessions: Array.from({ length: 20 }, (_, i) => ({ id: `${i}`.padStart(16, '0'), name: `session ${i}` })) }
    const shown = mentionMatches(many, '')
    expect(shown.filter((m) => m.kind === 'session')).toHaveLength(3)
    expect(shown.at(-1)!.ref).toBe('ticket:CND-41')
    expect(mentionMatches(many, 'session').filter((m) => m.kind === 'session')).toHaveLength(12)
  })

  it('matches a name, an id or a ref, in any case', () => {
    expect(mentionMatches(src, 'TENANT').map((m) => m.ref)).toEqual(['ticket:CND-41'])
    expect(mentionMatches(src, 'docs').map((m) => m.kind)).toEqual(['session', 'question'])
    expect(mentionMatches(src, '1a2b3c4d').map((m) => m.ref)).toEqual([`run:${RUN}`, `task:${RUN}/build`, `task:${RUN}/ui`])
  })

  it('offers a ref typed whole that the page does not hold, of a kind asked, and nothing for one the grammar refuses', () => {
    const typed = `attempt:${RUN}/build/3`
    expect(mentionMatches(src, typed)[0]).toEqual({ ref: typed, label: 'build, attempt 3', kind: 'attempt', detail: 'as typed' })
    expect(mentionMatches(src, typed, ['ticket'])).toEqual([])
    expect(mentionMatches(src, 'CND-')).toHaveLength(1)
    expect(mentionMatches(src, 'ticket:CND-999')[0]!.ref).toBe('ticket:CND-999')
  })
})

describe('what a door says', () => {
  it('names the request asked and leads to it on the Brakeman page', () => {
    const t = askedToast({ id: 'br-14' })
    expect(t.title).toBe('Asked as br-14')
    expect(t.actions).toEqual([{ label: 'Open', to: '/brakeman?request=br-14', color: 'neutral', variant: 'outline' }])
  })

  it('cannot ask while Brakeman is off or another server owns its records', () => {
    expect(askDisabled({ config: { enabled: false } })).toBe('Brakeman is off: turn it on in Settings')
    expect(askDisabled({ config: { enabled: true }, readOnly: true })).toBe("Another server owns Brakeman's records")
    expect(askDisabled({ config: { enabled: true } })).toBe('')
  })

  it("words each kind as KINDS does, without its …", () => {
    expect(kindLabel('locate')).toBe('Find')
    expect(kindLabel('explain')).toBe('Explain this')
    expect(kindLabel('unknown')).toBe('unknown')
  })
})
