import { describe, expect, it } from 'vitest'
import { compareProblems, compareSpec, criterionId, draftComparison, draftOnOpen, specCriteria, withCandidateAgent, type DraftComparison } from './compareTemplate'

const agents = [
  { id: 'claude', name: 'Claude Code', allowArgs: true },
  { id: 'codex', name: 'Codex', allowArgs: true },
  { id: 'plain', name: 'Plain shell', allowArgs: false },
]

/** A draft the server would take: two candidates, a criterion, a check. */
function ready(): DraftComparison {
  const d = draftComparison('claude')
  d.name = 'Cache key'
  d.cwd = '/srv/app'
  d.goal = 'Make the cache read fast'
  d.instructions = 'Keep the API.'
  d.criteria[0]!.text = 'no new dependency'
  d.checks = [{ id: 'unit', argv: ['go', 'test', './...'], timeoutSeconds: 300 }]
  d.candidates[1]!.agentId = 'codex'
  d.candidates[1]!.args = ['--model', 'x']
  d.candidates[1]!.approach = '  drop the cache and memoise in the handler  '
  return d
}

describe("the New comparison form's body", () => {
  it("posts the template route's fields and nothing else: no review agent without reviews, no manifest unless the project's is used", () => {
    expect(compareSpec(ready())).toEqual({
      name: 'Cache key',
      cwd: '/srv/app',
      goal: 'Make the cache read fast',
      instructions: 'Keep the API.',
      criteria: [{ id: 'no-new-dependency', text: 'no new dependency' }],
      checks: [{ id: 'unit', argv: ['go', 'test', './...'], timeoutSeconds: 300 }],
      candidates: [{ agentId: 'claude' }, { agentId: 'codex', args: ['--model', 'x'], approach: 'drop the cache and memoise in the handler' }],
    })
  })

  it("sends the reviews with their agent, Brakeman's draft, and the project's manifest in its file form", () => {
    const d = ready()
    d.reviews = true
    d.reviewAgent = 'codex'
    d.brakeman = true
    d.manifestFile = true
    d.checks = []
    const s = compareSpec(d)
    expect(s).toMatchObject({ reviews: true, reviewAgent: 'codex', brakeman: true, manifest: { file: true } })
    expect(s.checks).toBeUndefined()
  })

  it("names each criterion from its words, as a task's id is written, never twice", () => {
    expect(specCriteria([{ text: 'p99 of the cache read under 2 ms in `bench_test.go`' }, { text: 'p99 of the cache read under 2 ms in `bench_test.go`' }, { text: '¿?' }])).toEqual([
      { id: 'p99-of-the-cache-read-under-2-ms', text: 'p99 of the cache read under 2 ms in `bench_test.go`' },
      { id: 'p99-of-the-cache-read-under-2-ms-2', text: 'p99 of the cache read under 2 ms in `bench_test.go`' },
      { id: 'criterion-3', text: '¿?' },
    ])
    for (const id of specCriteria([{ text: 'A'.repeat(500) }, { text: '--x--' }]).map((c) => c.id)) expect(id).toMatch(/^[a-z0-9][a-z0-9_-]{0,39}$/)
    expect(criterionId('No new dependency', 1, ['no-new-dependency', 'no-new-dependency-2'])).toBe('no-new-dependency-3')
  })
})

describe('what stops Create', () => {
  it('lets a draft within the bounds be created', () => {
    expect(compareProblems(ready(), agents)).toEqual([])
  })

  it('holds the candidates to 2 to 4', () => {
    const one = ready()
    one.candidates = one.candidates.slice(0, 1)
    expect(compareProblems(one, agents)).toContain('A comparison has 2 to 4 candidates')
    const five = ready()
    five.candidates = [...five.candidates, ...five.candidates, five.candidates[0]!]
    expect(compareProblems(five, agents)).toContain('A comparison has 2 to 4 candidates')
  })

  it('holds the criteria to 1 to 12, each one line of at most 500 characters', () => {
    const none = ready()
    none.criteria = []
    expect(compareProblems(none, agents)).toContain('A comparison has 1 to 12 criteria')
    const many = ready()
    many.criteria = Array.from({ length: 13 }, (_, i) => ({ key: i, text: `c ${i}` }))
    expect(compareProblems(many, agents)).toContain('A comparison has 1 to 12 criteria')
    const long = ready()
    long.criteria[0]!.text = 'x'.repeat(501)
    expect(compareProblems(long, agents)).toEqual(['Criterion 1: at most 500 characters'])
    long.criteria[0]!.text = 'x'.repeat(500)
    expect(compareProblems(long, agents)).toEqual([])
    const lines = ready()
    lines.criteria[0]!.text = 'one\ntwo'
    expect(compareProblems(lines, agents)).toEqual(['Criterion 1: one line'])
  })

  it("asks for a check or the project's manifest", () => {
    const d = ready()
    d.checks = []
    expect(compareProblems(d, agents)).toEqual(["Add a check, or use the project's conductor.manifest.json: a comparison with nothing to check compares nothing"])
    d.manifestFile = true
    expect(compareProblems(d, agents)).toEqual([])
  })

  it("holds an approach to 2,000 characters, an agent's arguments to an agent that takes some, and a review to its agent", () => {
    const d = ready()
    d.candidates[0]!.approach = 'y'.repeat(2001)
    d.candidates[1]!.agentId = 'plain'
    d.reviews = true
    expect(compareProblems(d, agents)).toEqual(['Candidate A: the approach is at most 2000 characters', 'Candidate B: Plain shell takes no arguments', 'Pick the agent that reviews each candidate'])
  })
})

describe('a candidate moved to another agent, and the draft across a close', () => {
  it('drops the arguments, typed or still being typed, when the new agent takes none, so Create is not held by what the form no longer shows', () => {
    const d = ready()
    d.candidates[0]!.args = ['--model', 'x']
    d.candidates[1]!.argsPending = '"half'
    d.candidates = d.candidates.map((c) => withCandidateAgent(c, 'plain', agents))
    expect(d.candidates.map((c) => [c.agentId, c.args, c.argsPending])).toEqual([
      ['plain', [], undefined],
      ['plain', [], undefined],
    ])
    expect(compareProblems(d, agents)).toEqual([])
  })

  it('keeps the arguments for an agent that takes some', () => {
    const c = { key: 1, agentId: 'claude', args: ['--model', 'x'], approach: '' }
    expect(withCandidateAgent(c, 'codex', agents)).toEqual({ ...c, agentId: 'codex' })
  })

  it('opens again on the draft it kept, a candidate made before the catalog was read on its first agent; a new one once ended', () => {
    const kept = ready()
    kept.candidates[1]!.agentId = ''
    const again = draftOnOpen(kept, 'claude')
    expect(again.name).toBe('Cache key')
    expect(again.criteria).toEqual(kept.criteria)
    expect(again.candidates.map((c) => c.agentId)).toEqual(['claude', 'claude'])
    const fresh = draftOnOpen(null, 'codex')
    expect(fresh.name).toBe('')
    expect(fresh.candidates.map((c) => c.agentId)).toEqual(['codex', 'codex'])
  })

  it('takes a criterion as it is sent: trimmed, so a tab at its end is no second line', () => {
    const d = ready()
    d.criteria[0]!.text = 'no new dependency\t'
    expect(compareProblems(d, agents)).toEqual([])
    expect(compareSpec(d).criteria).toEqual([{ id: 'no-new-dependency', text: 'no new dependency' }])
    d.criteria[0]!.text = 'no new\tdependency'
    expect(compareProblems(d, agents)).toEqual(['Criterion 1: one line'])
  })
})
