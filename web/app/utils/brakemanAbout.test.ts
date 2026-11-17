import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import type { BrakemanBrief, BrakemanDigestAbout, BrakemanRequest, BrakemanRow } from './brakeman'
import { answerEvidence, answerWords, answersByTask, askAgainBody, citedIn, latestBrief, latestWords, matchOf, noneWords, openAbout, ordinal, queuedWords, subjectUnder } from './brakemanAbout'

const RUN = 'fix-login-1a2b3c4d'
const OTHER = 'checkout-0c1d2e3f'
const SESSION = '0123456789abcdef'

function req(id: string, o: Partial<BrakemanRow> = {}): BrakemanRow {
  return { id, opId: `op-${id}`, kind: 'explain', createdAt: '2026-11-17T05:00:00Z', state: 'briefed', agent: {} as BrakemanRequest['agent'], config: {} as BrakemanRequest['config'], reads: 0, ...o }
}

describe('the about rule', () => {
  // The one table the server's rule is held to (internal/brakeman/testdata/about-table.json, read by the Go test as well): the
  // browser holds its subject half, the rows whose digest cites nothing, since the cited half needs a brief only the server reads.
  const table = JSON.parse(readFileSync(fileURLToPath(new URL('../../../internal/brakeman/testdata/about-table.json', import.meta.url)), 'utf8')) as Array<{
    about: string
    subjects: string[]
    digestAbout?: BrakemanDigestAbout
    match: '' | 'subject' | 'cited'
  }>
  const empty = (d: BrakemanDigestAbout | undefined) => !d || Object.values(d).every((l) => !l?.length)

  it("holds the table's subject half", () => {
    const rows = table.filter((r) => empty(r.digestAbout))
    expect(rows.length).toBeGreaterThan(4)
    for (const r of rows) expect(subjectUnder(r.about, r.subjects), `${r.about} over ${JSON.stringify(r.subjects)}`).toBe(r.match === 'subject')
  })

  it('gives every row of the table its match, a subject winning over a citation', () => {
    for (const r of table) expect(matchOf(r.about, r.subjects, r.digestAbout), `${r.about} over ${JSON.stringify(r)}`).toBe(r.match)
  })

  it('holds a run over its tasks and attempts, a task over its attempts, a session over its questions, a ticket over its comments', () => {
    expect(subjectUnder(`run:${RUN}`, [`task:${RUN}/build`])).toBe(true)
    expect(subjectUnder(`run:${RUN}`, [`attempt:${RUN}/build/3`])).toBe(true)
    expect(subjectUnder(`run:${RUN}`, [`task:${OTHER}/build`])).toBe(false)
    expect(subjectUnder(`task:${RUN}/build`, [`attempt:${RUN}/build/2`])).toBe(true)
    expect(subjectUnder(`task:${RUN}/build`, [`task:${RUN}/deploy`, `run:${RUN}`])).toBe(false)
    expect(subjectUnder(`session:${SESSION}`, [`question:${SESSION}/fedcba9876543210`])).toBe(true)
    expect(subjectUnder('ticket:CND-41', ['comment:CND-41/0123456789abcdef'])).toBe(true)
    expect(subjectUnder('ticket:CND-41', ['ticket:CND-4'])).toBe(false)
    // A check of the run's is no subject the rule holds under it, and a ref of another kind is asked about by no page.
    expect(subjectUnder(`run:${RUN}`, [`check:${RUN}/build/1/tests/1`])).toBe(false)
    expect(subjectUnder(`check:${RUN}/build/1/tests/1`, [`check:${RUN}/build/1/tests/1`])).toBe(false)
    expect(subjectUnder('not a ref', ['not a ref'])).toBe(false)
  })

  it('reads a citation from the digest by the kind asked about', () => {
    expect(citedIn(`run:${RUN}`, { runs: [RUN] })).toBe(true)
    expect(citedIn(`task:${RUN}/build`, { tasks: [`${RUN}/build`] })).toBe(true)
    expect(citedIn(`task:${RUN}/build`, { runs: [RUN] })).toBe(false)
    expect(citedIn('ticket:CND-41', { tickets: ['CND-41'] })).toBe(true)
    expect(citedIn(`session:${SESSION}`, undefined)).toBe(false)
  })
})

describe("the subject's requests", () => {
  it('takes the newest request with a brief as the latest', () => {
    expect(latestBrief([req('br-9001'), req('br-9003', { hasBrief: false, state: 'running' }), req('br-9002', { hasBrief: true })])?.id).toBe('br-9002')
    expect(latestBrief([req('br-9001', { state: 'failed' })])).toBeUndefined()
  })

  it('finds the open requests by their subjects, with their place in line', () => {
    const state = {
      current: req('br-9010', { state: 'running', subjects: [`task:${RUN}/deploy`] }),
      queue: [req('br-9011', { state: 'queued', subjects: [`ticket:CND-41`] }), req('br-9012', { state: 'queued', subjects: [`run:${RUN}`] })],
    }
    expect(openAbout(state, `run:${RUN}`).map((o) => [o.request.id, o.place])).toEqual([
      ['br-9010', 0],
      ['br-9012', 2],
    ])
    expect(openAbout(state, 'ticket:CND-41').map((o) => o.request.id)).toEqual(['br-9011'])
    expect(openAbout(null, `run:${RUN}`)).toEqual([])
    expect(queuedWords(2)).toBe('Queued: 2nd in line')
    expect([1, 2, 3, 4, 11, 12, 13, 21, 22, 101].map(ordinal)).toEqual(['1st', '2nd', '3rd', '4th', '11th', '12th', '13th', '21st', '22nd', '101st'])
  })

  it("stands the newest request with the task or its attempt as a subject under the task's row, never one that cites it", () => {
    const rows = [
      req('br-9020', { subjects: [`task:${RUN}/build`] }),
      req('br-9022', { subjects: [`attempt:${RUN}/build/3`] }),
      req('br-9021', { subjects: [`task:${RUN}/deploy`] }),
      // A wake brief cites both tasks and names no subject: under no row.
      req('br-9030', { kind: 'wake', subjects: [], digest: { schema: 1, title: 't', reading: '', counts: {}, about: { tasks: [`${RUN}/build`, `${RUN}/deploy`] } } }),
      req('br-9031', { subjects: [`task:${OTHER}/build`] }),
    ]
    const by = answersByTask(rows, RUN)
    expect(Object.fromEntries(Object.entries(by).map(([t, r]) => [t, r.id]))).toEqual({ build: 'br-9022', deploy: 'br-9021' })
  })
})

describe("the panel's and the row's words", () => {
  const now = Date.parse('2026-11-17T05:12:00Z')
  it("says the latest brief's age, and a wake's or a citation's", () => {
    expect(latestWords(req('br-9040', { briefAt: '2026-11-17T05:00:00Z', match: 'subject', by: 'Nate' }), `run:${RUN}`, now)).toBe('Latest brief · 12m ago · asked by Nate')
    expect(latestWords(req('br-9041', { kind: 'wake', briefAt: '2026-11-17T05:00:00Z', match: 'cited' }), `run:${RUN}`, now)).toBe('Latest brief · 12m ago · woken by events')
    expect(latestWords(req('br-9042', { briefAt: '2026-11-17T05:00:00Z', match: 'cited' }), 'ticket:CND-41', now)).toBe('Latest brief · 12m ago · asked · mentions this ticket')
    expect(noneWords(`run:${RUN}`)).toBe('Brakeman has said nothing about this run.')
    expect(noneWords('ticket:CND-41')).toBe('Brakeman has said nothing about this ticket.')
  })

  it('says before which attempt an answer was asked, and nothing when it was asked after it', () => {
    const r = req('br-9050', { createdAt: '2026-11-17T03:00:00Z', briefAt: '2026-11-17T03:12:00Z', match: 'subject' })
    expect(answerWords(r, `task:${RUN}/build`, now, { n: 3, startedAt: '2026-11-17T04:00:00Z' })).toBe('Brakeman · about this task · 2h ago · before attempt 3')
    expect(answerWords(r, `task:${RUN}/build`, now, { n: 2, startedAt: '2026-11-17T02:00:00Z' })).toBe('Brakeman · about this task · 2h ago')
    expect(answerWords(r, `task:${RUN}/build`, now)).toBe('Brakeman · about this task · 2h ago')
  })

  it("takes a row's evidence from its decision, else its facts, each once, at most three", () => {
    const facts: BrakemanBrief['items'] = [
      { kind: 'fact/1', text: 'a', evidence: ['run:a-00000000', 'task:a-00000000/b'] },
      { kind: 'fact/1', text: 'b', evidence: ['task:a-00000000/b', 'session:0123456789abcdef', 'ticket:CND-1'] },
    ]
    expect(answerEvidence({ items: facts })).toEqual(['run:a-00000000', 'task:a-00000000/b', 'session:0123456789abcdef'])
    const decision = { kind: 'decision/1' as const, about: 'x', happened: 'h', whyHuman: 'w', question: 'q', alternatives: [], recommendation: null, authorizes: 'a', evidence: ['ticket:CND-9'] }
    expect(answerEvidence({ items: [...facts, decision] })).toEqual(['ticket:CND-9'])
    expect(answerEvidence(null)).toEqual([])
  })

  it('asks again what a request asked, never a wake', () => {
    expect(askAgainBody(req('br-9060', { kind: 'explain', subjects: [`task:${RUN}/build`] }))).toEqual({ kind: 'explain', subjects: [`task:${RUN}/build`] })
    expect(askAgainBody(req('br-9061', { kind: 'ask', note: 'why?', subjects: [] }))).toEqual({ kind: 'ask', note: 'why?' })
    expect(askAgainBody(req('br-9062', { kind: 'wake' }))).toBeNull()
  })
})
