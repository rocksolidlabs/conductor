import { describe, expect, it } from 'vitest'
import { EXAMPLES_LINE } from './crews'
import { EXAMPLES_FAILED, examplesToast, welcomeTips, welcomeWords } from './welcome'

describe('welcomeTips', () => {
  it("says what each of the welcome's choices does, an agent's by its name and version, the examples in the words Crews says", () => {
    expect(welcomeTips.agent('Claude Code', '2.1.318')).toBe('Opens the Launch dialog with Claude Code 2.1.318 picked.')
    expect(welcomeTips.agent('Claude Code')).toBe('Opens the Launch dialog with Claude Code picked.')
    expect(welcomeTips.more).toBe('Opens the Launch dialog with every agent this machine has.')
    expect(welcomeTips.host).toBe('Opens the Quick start: how to run conductor host on another machine.')
    expect(welcomeTips.examples).toBe(EXAMPLES_LINE)
    // Each a sentence of its own, none the same as another, none speaking of a tab.
    const all = [welcomeTips.agent('Codex'), welcomeTips.more, welcomeTips.agents, welcomeTips.launch, welcomeTips.host, welcomeTips.examples]
    for (const t of all) {
      expect(t).toMatch(/^[A-Z].+\.$/)
      expect(t).not.toMatch(/\btab\b/i)
    }
    expect(new Set(all).size).toBe(all.length)
  })
})

describe('examplesToast', () => {
  it('counts the crews added in the words Crews uses, and says so when none was', () => {
    expect(examplesToast(5)).toEqual({ title: '5 example crews added', color: 'success' })
    expect(examplesToast(1)).toEqual({ title: '1 example crew added', color: 'success' })
    expect(examplesToast(0)).toEqual({ title: 'The examples are here already', color: 'neutral' })
    expect(EXAMPLES_FAILED).toBe('Loading the examples failed')
  })
})

describe('welcomeWords', () => {
  it('says what the welcome found: no agent, one on Windows alone by its name, a look that found none, a read that failed', () => {
    expect(welcomeWords.none).toBe('No coding agent is installed on this machine yet.')
    expect(welcomeWords.onWindows('Claude Code')).toBe('Claude Code is installed on Windows, not in WSL, where Conductor runs. Install it in WSL, then press Look again.')
    expect(welcomeWords.looked).toBe('Looked again: still none found.')
    expect(welcomeWords.failed('the catalog could not be read')).toBe('The agents could not be read: the catalog could not be read')
    expect(welcomeWords.failed('')).toBe('The agents could not be read: The server did not answer.')
    expect(welcomeWords.failed()).toBe('The agents could not be read: The server did not answer.')
  })
})
