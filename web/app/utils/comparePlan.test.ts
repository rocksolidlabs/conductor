import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type { PlanBody, PlanCompare } from '~/composables/useDispatch'
import { compareLaunch, compareStatement } from './compareLaunch'
import { checksEachWords, comparisonChecks, expectedChecks, manifestForm, planComparison, planFold } from './dispatchPlans'

// A comparison's plan revision (CP1, docs/plans/compare-crew.md): its body and its approval dialog say what is compared (each
// candidate's approach, the criteria they are held to) and count the checks on each candidate attempt as the launch's statement
// counts them; the statement names verify steps only where the revision has a manifest.

const catalog = [
  { id: 'claude', name: 'Claude Code' },
  { id: 'codex', name: 'Codex' },
]

/** A comparison's body as the template makes it: two candidates with one check each, a review after each, the manifest as given. */
function body(o: { manifest?: PlanBody['crew']['manifest']; manifestFile?: PlanBody['manifestFile']; checks?: number } = {}): PlanBody {
  const checks = Array.from({ length: o.checks ?? 1 }, (_, i) => ({ id: i ? `check-${i}` : 'check', argv: ['sh', 'check.sh'] }))
  return {
    base: 'a'.repeat(40),
    crew: {
      id: 'cmp',
      name: 'cmp',
      members: [
        { name: 'cand-a', agentId: 'claude', args: ['--model', 'haiku'] },
        { name: 'cand-b', agentId: 'codex' },
        { name: 'rev-a', agentId: 'claude' },
        { name: 'rev-b', agentId: 'claude' },
      ],
      tasks: [
        { id: 'approach-a', member: 'cand-a', kind: 'work', checks },
        { id: 'approach-b', member: 'cand-b', kind: 'work', checks },
        { id: 'review-a', member: 'rev-a', kind: 'review', needs: ['approach-a'] },
        { id: 'review-b', member: 'rev-b', kind: 'review', needs: ['approach-b'] },
      ],
      ...(o.manifest ? { manifest: o.manifest } : {}),
      compare: {
        criteria: [
          { id: 'check-passes', text: 'check.sh unchanged and passing' },
          { id: 'no-process-per-title', text: 'no process per title' },
        ],
        candidates: [{ task: 'approach-a', approach: '  one awk program for the whole input  ' }, { task: 'approach-b' }],
      },
    },
    agents: [],
    ...(o.manifestFile ? { manifestFile: o.manifestFile } : {}),
    references: [],
    attachments: [],
    comments: [],
    constraints: '',
    decisions: [],
    note: '',
  }
}

/** The server's part for `b`, as internal/api readPlanFacts makes it: the first candidate's checks and the manifest's verify steps. */
function serverPart(b: PlanBody): PlanCompare {
  const first = b.crew.tasks!.find((t) => t.id === b.crew.compare!.candidates[0]!.task)!
  const verify = (b.crew.manifest?.verify?.length ?? 0) + (b.manifestFile?.manifest.verify?.length ?? 0)
  return { candidates: b.crew.compare!.candidates.length, reviews: 2, checksAtBase: (first.checks?.length ?? 0) + verify, brakemanTurn: false }
}

const rev = { n: 1, state: 'proposed' as const, crewName: 'cmp' }

describe("a comparison's plan revision", () => {
  it('names each candidate with its member, its agent and its approach, in the block\'s order, and the criteria', () => {
    expect(planComparison(body(), catalog)).toEqual({
      criteria: [
        { id: 'check-passes', text: 'check.sh unchanged and passing' },
        { id: 'no-process-per-title', text: 'no process per title' },
      ],
      candidates: [
        { task: 'approach-a', member: 'cand-a', agent: 'Claude Code', approach: 'one awk program for the whole input' },
        { task: 'approach-b', member: 'cand-b', agent: 'Codex', approach: '' },
      ],
    })
    // An agent the catalog does not list is named by its id; a revision of no comparison has none of this.
    expect(planComparison(body())!.candidates[1]!.agent).toBe('codex')
    const plain = body()
    delete plain.crew.compare
    expect(planComparison(plain)).toBeNull()
    expect(comparisonChecks(plain)).toBeNull()
  })

  it("counts the checks on each candidate attempt as the server's statement does, never every lane's added together", () => {
    for (const b of [
      body(),
      body({ checks: 3 }),
      body({ manifest: { verify: [{ id: 'lint', argv: ['make', 'lint'] }] } }),
      body({ manifest: { file: true }, manifestFile: { path: 'conductor.manifest.json', blob: 'b'.repeat(40), manifest: { verify: [{ id: 'vet', argv: ['go', 'vet'] }, { id: 'unit', argv: ['go', 'test'] }] } } }),
    ]) {
      const each = comparisonChecks(b)!
      expect(each.length).toBe(serverPart(b).checksAtBase)
      // The two lanes' checks together are more: the count the revision used to show.
      expect(expectedChecks(b).length).toBeGreaterThan(each.length)
    }
    expect(comparisonChecks(body())!.map((c) => c.id)).toEqual(['check'])
  })

  it("says the same count in the revision's fold, its body and the launch's statement", () => {
    const b = body()
    expect(planFold(rev, b).line).toBe('2 candidates · 4 members · 4 tasks · 1 check on each candidate · from aaaaaaaaaaaa · Yolo off')
    const l = compareLaunch({ n: 1, compare: serverPart(b) }, b, catalog)
    if (l.kind !== 'compare') throw new Error(l.kind)
    const checks = l.statement.find((p) => p.key === 'checks')!.words
    expect(checks.startsWith(checksEachWords(comparisonChecks(b)!.length))).toBe(true)
    expect(checksEachWords(1)).toBe('1 check on each submitted candidate attempt')
    expect(checksEachWords(2)).toBe('2 checks on each submitted candidate attempt')
    // A revision of no comparison folds as it always has.
    const plain = body()
    delete plain.crew.compare
    expect(planFold(rev, plain).line).toBe('4 members · 4 tasks · 2 checks · from aaaaaaaaaaaa · Yolo off')
  })

  it("is drawn in the revision's body and its approval dialog, the body's count from the comparison's checks", () => {
    const planBody = readFileSync(new URL('../components/DispatchPlanBody.vue', import.meta.url), 'utf8')
    expect(planBody).toContain('<DispatchPlanCompare :body="body" :agents="agents" />')
    expect(planBody).toMatch(/v-if="each"[^>]*data-plan-expected :data-count="each\.length"/)
    const approve = readFileSync(new URL('../components/DispatchPlanApprove.vue', import.meta.url), 'utf8')
    expect(approve).toMatch(/<DispatchPlanCompare v-if="body\?\.crew\.compare" :body="body" :agents="agents" compact/)
  })
})

describe("the launch statement's checks, by the revision's manifest", () => {
  const part: PlanCompare = { candidates: 2, reviews: 0, checksAtBase: 1, brakemanTurn: false }
  const words = (b: PlanBody) => compareStatement(part, [], manifestForm(b)).find((p) => p.key === 'checks')!.words

  it('names no verify step for a crew with no manifest', () => {
    expect(manifestForm(body())).toBe('none')
    expect(words(body())).toBe("1 check on each submitted candidate attempt (the task's checks; the plan holds no manifest, so no verify steps)")
    expect(words(body())).not.toContain('conductor.manifest.json')
  })

  it("names the crew's own manifest's verify steps, and conductor.manifest.json's only for the file form", () => {
    const own = body({ manifest: { verify: [{ id: 'lint', argv: ['make', 'lint'] }] } })
    expect(manifestForm(own)).toBe('crew')
    expect(words(own)).toBe("1 check on each submitted candidate attempt (the task's checks and the crew's manifest's verify steps)")
    const file = body({ manifest: { file: true } })
    expect(manifestForm(file)).toBe('file')
    // A manifest file read at the base is the file form too.
    expect(manifestForm(body({ manifestFile: { path: 'conductor.manifest.json', blob: 'b'.repeat(40), manifest: {} } }))).toBe('file')
    expect(words(file)).toBe("1 check on each submitted candidate attempt (the task's checks and conductor.manifest.json's verify steps at the base; a candidate that changes the manifest runs its own)")
  })

  it("is told the revision's manifest by the launch", () => {
    const l = compareLaunch({ n: 1, compare: part }, body(), catalog)
    if (l.kind !== 'compare') throw new Error(l.kind)
    expect(l.statement.find((p) => p.key === 'checks')!.words).toContain('the plan holds no manifest')
  })
})
