import { describe, expect, it } from 'vitest'
import { effectiveYolo, hasYoloRecipe, resumeLabel, yoloChoice, yoloFromChoice, yoloSummary, yoloUnavailable } from './yolo'

describe('the crew editor yolo select', () => {
  it('round-trips the default, on and off', () => {
    for (const v of [undefined, true, false]) expect(yoloFromChoice(yoloChoice(v))).toBe(v)
    expect(yoloChoice(undefined)).toBe('default')
  })
})

describe('effectiveYolo', () => {
  it('is the launch or crew choice, else the server default', () => {
    expect(effectiveYolo(undefined, true)).toBe(true)
    expect(effectiveYolo(false, true)).toBe(false)
    expect(effectiveYolo(true, false)).toBe(true)
  })
})

describe('yoloSummary', () => {
  const claude = { name: 'Claude Code', command: ['claude'], yolo: { args: ['--dangerously-skip-permissions'] } }
  const copilot = { name: 'Copilot CLI', command: ['copilot'], yolo: { args: ['--yolo'], env: { COPILOT_ALLOW_ALL: 'true' } } }
  it('adds the recipe after the command and the extra arguments when on', () => {
    expect(yoloSummary(claude, true, ['--model', 'opus'])).toEqual({ argv: ['claude', '--model', 'opus', '--dangerously-skip-permissions'], env: [], applies: true, notice: '' })
    expect(yoloSummary(copilot, true).env).toEqual(['COPILOT_ALLOW_ALL'])
  })
  it('adds nothing when off', () => {
    expect(yoloSummary(claude, false)).toMatchObject({ argv: ['claude'], applies: false, notice: '' })
  })
  it('says so for an agent without a recipe, or with an empty one', () => {
    expect(hasYoloRecipe({ yolo: {} })).toBe(false)
    expect(yoloSummary({ id: 'shell', name: 'Shell', command: ['/bin/bash', '-l'] }, true)).toMatchObject({ applies: false, notice: 'does not apply to a shell' })
    expect(yoloSummary({ id: 'pi', name: 'pi', command: ['pi'] }, true)).toMatchObject({ applies: false, notice: 'pi cannot skip its prompts' })
    expect(yoloSummary({ name: 'X', command: ['x'], yolo: {} }, true).applies).toBe(false)
    expect(yoloSummary({ name: 'X', command: ['x'] }, false).notice).toBe('')
  })
})

describe('yoloUnavailable', () => {
  it("says why the Launch dialog's switch is off and disabled, and nothing for an agent with a recipe or none picked", () => {
    expect(yoloUnavailable({ name: 'Claude Code', yolo: { args: ['--dangerously-skip-permissions'] } })).toBe('')
    expect(yoloUnavailable({ name: 'Copilot CLI', yolo: { env: { COPILOT_ALLOW_ALL: 'true' } } })).toBe('')
    expect(yoloUnavailable({ id: 'pi', name: 'pi' })).toBe('pi cannot skip its prompts')
    expect(yoloUnavailable({ id: 'x', name: 'Goose', yolo: {} })).toBe('Goose cannot skip its prompts')
    expect(yoloUnavailable({ id: 'shell', name: 'Shell' })).toBe('does not apply to a shell')
    expect(yoloUnavailable(undefined)).toBe('')
    // The old words are gone.
    expect(yoloUnavailable({ id: 'pi', name: 'pi' })).not.toContain('has no yolo recipe')
  })
})

describe('resumeLabel', () => {
  it('resumes a conversation the agent has had, and relaunches otherwise', () => {
    expect(resumeLabel({ agentSession: { id: 'u', resumable: true, source: 'set' } }).label).toBe('Resume')
    expect(resumeLabel({ agentSession: { id: 'u', resumable: false, source: 'set' } }).label).toBe('Relaunch')
    expect(resumeLabel({}).label).toBe('Relaunch')
  })
})
