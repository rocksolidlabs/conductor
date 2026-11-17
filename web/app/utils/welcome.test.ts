import { describe, expect, it } from 'vitest'
import { EXAMPLES_LINE } from './crews'
import { welcomeTips } from './welcome'

describe('welcomeTips', () => {
  it("says what each of the welcome's choices does, an agent's by its name, the examples in the words Crews says", () => {
    expect(welcomeTips.agent('Claude Code')).toBe('Opens the Launch dialog with Claude Code picked; nothing starts until you launch.')
    expect(welcomeTips.examples).toBe(EXAMPLES_LINE)
    // Each a sentence of its own, none the same as another.
    const all = [welcomeTips.agent('Codex'), welcomeTips.more, welcomeTips.agents, welcomeTips.launch, welcomeTips.host, welcomeTips.examples]
    for (const t of all) expect(t).toMatch(/^[A-Z].+\.$/)
    expect(new Set(all).size).toBe(all.length)
  })
})
