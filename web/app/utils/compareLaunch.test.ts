import { describe, expect, it } from 'vitest'
import type { PlanBody, PlanCompare } from '~/composables/useDispatch'
import { COMPARISON_LAUNCHES_FROM_A_TICKET, candidateAgents, compareLaunch, compareStatement, launchButtonWords } from './compareLaunch'

const catalog = [
  { id: 'claude', name: 'Claude Code' },
  { id: 'codex', name: 'Codex' },
]

/** A comparison's body as the template makes it: `agents` one per candidate, a review after each when `reviews`. */
function body(agents: string[], o: { reviews?: boolean; brakeman?: boolean } = {}): PlanBody {
  const letters = agents.map((_, i) => String.fromCharCode(97 + i))
  return {
    base: 'a'.repeat(40),
    crew: {
      id: 'cmp',
      name: 'cmp',
      members: [...letters.map((l, i) => ({ name: `cand-${l}`, agentId: agents[i]! })), ...(o.reviews ? letters.map((l) => ({ name: `rev-${l}`, agentId: 'claude' })) : [])],
      tasks: [
        ...letters.map((l) => ({ id: `approach-${l}`, member: `cand-${l}`, kind: 'work' as const })),
        ...(o.reviews ? letters.map((l) => ({ id: `review-${l}`, member: `rev-${l}`, kind: 'review' as const, needs: [`approach-${l}`] })) : []),
      ],
      compare: { criteria: [{ id: 'deps', text: 'no new dependency' }], candidates: letters.map((l) => ({ task: `approach-${l}` })), brakeman: !!o.brakeman },
    },
    agents: [],
    references: [],
    attachments: [],
    comments: [],
    constraints: '',
    decisions: [],
    note: '',
  }
}

const part = (candidates: number, o: Partial<PlanCompare> = {}): PlanCompare => ({ candidates, reviews: 0, checksAtBase: 2, brakemanTurn: false, ...o })

/** The statement as the dialog draws it: its parts joined, as one sentence. */
const sentence = (p: PlanCompare, agents: string[]) =>
  compareStatement(p, agents)
    .map((x) => x.words)
    .join('; ')

describe('the launch statement of a comparison', () => {
  it("says the plan's sentence for three candidates with reviews and Brakeman", () => {
    expect(sentence(part(3, { reviews: 3, brakemanTurn: true }), ['Claude Code', 'Codex', 'Claude Code'])).toBe(
      "This comparison starts 3 candidates (Claude Code, Codex, Claude Code): 3 agent sessions, once the plan's decisions and the project's prerequisites let the work start; " +
        'then an agent review of each candidate that succeeds: up to 3 more sessions, none for a candidate that fails or is cancelled; ' +
        "then what 3 runs of one candidate's crew would take, since each candidate is worked as a task of its own crew is (each attempt a session of its own; 3 counted attempts a task, and a review's request for changes, an attempt that could not start and each Retry you give add more); " +
        "2 checks on each submitted candidate attempt (the task's checks and conductor.manifest.json's verify steps at the base; a candidate that changes the manifest runs its own); " +
        '1 Brakeman turn on your Brakeman account, when all candidates are done; Usage per account: shown below',
    )
  })

  it('counts two candidates without reviews or Brakeman, and says neither is started', () => {
    const s = compareStatement(part(2, { checksAtBase: 1 }), ['Claude Code', 'Codex'])
    expect(s.map((x) => x.key)).toEqual(['candidates', 'multiplier', 'checks', 'brakeman', 'usage'])
    expect(s[0]!.words).toContain('starts 2 candidates (Claude Code, Codex): 2 agent sessions')
    expect(s[1]!.words).toContain("then what 2 runs of one candidate's crew would take")
    expect(s[2]!.words).toMatch(/^1 check on each submitted candidate attempt/)
    expect(s[3]!.words).toBe('no Brakeman turn: this comparison asks no draft')
  })

  it('counts four candidates with their reviews as "up to", and Brakeman', () => {
    const s = compareStatement(part(4, { reviews: 4, brakemanTurn: true, checksAtBase: 3 }), ['Codex', 'Codex', 'Claude Code', 'Claude Code'])
    expect(s.find((x) => x.key === 'candidates')!.words).toContain('starts 4 candidates (Codex, Codex, Claude Code, Claude Code): 4 agent sessions')
    expect(s.find((x) => x.key === 'reviews')!.words).toBe('then an agent review of each candidate that succeeds: up to 4 more sessions, none for a candidate that fails or is cancelled')
    expect(s.find((x) => x.key === 'checks')!.words).toMatch(/^3 checks on each submitted candidate attempt/)
    expect(s.find((x) => x.key === 'brakeman')!.words).toContain('1 Brakeman turn')
  })

  it('names the refunds and Retries in the multiplier, and states no total', () => {
    for (const p of [part(2), part(2, { reviews: 2, brakemanTurn: true }), part(4, { reviews: 4, checksAtBase: 5 })]) {
      const s = sentence(p, [])
      expect(s).toContain("a review's request for changes, an attempt that could not start and each Retry you give add more")
      expect(s).not.toMatch(/\b(in all|total|at most|altogether)\b/i)
      // Only the counts the part gives are said: none multiplied into a total of sessions or checks.
      const numbers = [...s.matchAll(/\b\d+\b/g)].map((m) => Number(m[0]))
      const allowed = new Set([p.candidates, p.reviews, p.checksAtBase, 1, 3])
      expect(numbers.filter((x) => !allowed.has(x))).toEqual([])
    }
  })

  it("says the checks are not known when the server could not read the manifest's verify steps at the base", () => {
    const s = compareStatement(part(2, { checksAtBase: 0, checksUnread: 'conductor.manifest.json at 9f8e7d6 is not valid JSON' }), [])
    expect(s.find((x) => x.key === 'checks')!.words).toBe('the checks on each submitted candidate attempt are not known: conductor.manifest.json at 9f8e7d6 is not valid JSON (a candidate that changes the manifest runs its own)')
  })
})

describe("a launch from a revision, and the count it sends", () => {
  it("is a comparison counted by the revision's part, its agents named by the catalog in the block's order", () => {
    const l = compareLaunch({ n: 2, compare: part(3, { reviews: 3 }) }, body(['claude', 'codex', 'claude'], { reviews: true }), catalog)
    expect(l.kind).toBe('compare')
    if (l.kind !== 'compare') return
    expect(l.count).toBe(3)
    expect(l.agents).toEqual(['Claude Code', 'Codex', 'Claude Code'])
    expect(l.statement[0]!.words).toContain('starts 3 candidates (Claude Code, Codex, Claude Code)')
  })

  it("names an agent the catalog does not list by its id", () => {
    expect(candidateAgents(body(['claude', 'gone']), catalog)).toEqual(['Claude Code', 'gone'])
  })

  it('is ordinary for a revision with no part and no block: no count is sent', () => {
    const b = body(['claude', 'codex'])
    delete b.crew.compare
    expect(compareLaunch({ n: 1 }, b, catalog)).toEqual({ kind: 'ordinary' })
  })

  it('is held when the block is there and the part is not: no count the person was not shown', () => {
    const l = compareLaunch({ n: 4 }, body(['claude', 'codex']), catalog)
    expect(l).toEqual({ kind: 'unstated', words: 'Revision 4 is a comparison, and the server did not say what it starts: read the ticket again before launching it' })
  })

  it('is held when the part and the body disagree, never counting from the body', () => {
    expect(compareLaunch({ n: 4, compare: part(3) }, body(['claude', 'codex']), catalog).kind).toBe('unstated')
    const b = body(['claude', 'codex'])
    delete b.crew.compare
    expect(compareLaunch({ n: 4, compare: part(2) }, b, catalog).kind).toBe('unstated')
    const missing = body(['claude', 'codex'])
    missing.crew.tasks = missing.crew.tasks!.slice(1)
    expect(compareLaunch({ n: 4, compare: part(2) }, missing, catalog).kind).toBe('unstated')
  })

  it('says the button as the count: "Launch 2 candidates", and keeps a resend\'s words', () => {
    const l = compareLaunch({ n: 1, compare: part(2) }, body(['claude', 'codex']), catalog)
    expect(launchButtonWords(l, null)).toBe('Launch 2 candidates')
    expect(launchButtonWords(l, 'pending')).toBe('Send again')
    expect(launchButtonWords(l, 'error')).toBe('Send again')
    expect(launchButtonWords(l, 'failed')).toBe('Launch again')
    expect(launchButtonWords({ kind: 'ordinary' }, null)).toBe('Launch')
    expect(launchButtonWords(null, null)).toBe('Launch')
  })
})

describe("where an agent trial's crew is started, as the crews page and the launch sheet say it", () => {
  it('names an agent trial and the ticket it starts from, and never a comparison or a plan revision', () => {
    expect(COMPARISON_LAUNCHES_FROM_A_TICKET).toBe("An agent trial starts from a ticket's approved plan: propose this crew as the ticket's plan, approve it, and launch it there")
    expect(COMPARISON_LAUNCHES_FROM_A_TICKET).not.toMatch(/comparison|revision|candidate/i)
  })
})
