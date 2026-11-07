import { describe, expect, it, vi } from 'vitest'
import { decisionRows } from './usePaletteRows'

// The palette's store keeps module state in Nuxt's auto-imported refs: not needed by the rows' builder.
vi.mock('~/composables/usePalette', () => ({ usePalette: () => ({}) }))

// The palette's rows for the decision page (docs/plans/decision-inbox.md, PR-2): one per open item, by its ref, each opening the item.

describe("the palette's decision rows", () => {
  it('names each open item by its ref and opens it on the decision page', () => {
    const went: string[] = []
    const rows = decisionRows(
      [
        { ref: 'question:s1/q1', title: 'Claude Code in api-fix asks', question: 'Which database?', brief: '' },
        { ref: 'task:r1/build', title: 'build failed in run release', question: '', brief: 'Task build of run release is failed after 3 attempts' },
      ],
      (to) => went.push(to),
    )
    expect(rows.map((r) => [r.key, r.label, r.detail])).toEqual([
      ['decide:question:s1/q1', 'Decide: Claude Code in api-fix asks', 'Which database?'],
      ['decide:task:r1/build', 'Decide: build failed in run release', 'Task build of run release is failed after 3 attempts'],
    ])
    rows[1]!.run()
    expect(went).toEqual([`/decisions?item=${encodeURIComponent('task:r1/build')}`])
    expect(rows[0]!.href).toBe(`/decisions?item=${encodeURIComponent('question:s1/q1')}`)
    expect(decisionRows([], () => {})).toEqual([])
  })
})
