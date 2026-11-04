import { describe, expect, it } from 'vitest'
import { argsText, splitArgs } from './argv'
import { cloneName, cloneNameError, clonePrefill, cloneRefusal, hostedReason, inRunWorktree, nameTaken } from './clone'

const sessions = [{ name: 'api-sweep' }, { name: 'Docs Run' }, { name: 'Ölbild' }]

describe("a clone's name (#434)", () => {
  it("is the rename's rule: cleaned, required, at most 120 bytes", () => {
    expect(cloneName('  api-sweep-2 \t', sessions)).toEqual({ name: 'api-sweep-2', bytes: 11 })
    expect(cloneName('', sessions).problem).toBe('empty')
    expect(cloneName(' \u0001 ', sessions).problem).toBe('empty')
    const long = cloneName('é'.repeat(61), sessions)
    expect(long).toMatchObject({ problem: 'long', bytes: 122 })
    expect(cloneNameError(long)).toBe('Too long: a name is at most 120 bytes, and this one is 122')
    expect(cloneNameError(cloneName('', sessions))).toBeUndefined()
  })

  it("is no listed session's, ignoring case", () => {
    for (const n of ['api-sweep', 'API-SWEEP', ' Api-Sweep ', 'docs run', 'ÖLBILD']) {
      expect(cloneName(n, sessions).problem).toBe('taken')
    }
    expect(cloneNameError(cloneName('docs RUN', sessions))).toBe('A session is named docs RUN already')
    expect(cloneName('api-sweep-2', sessions).problem).toBeUndefined()
    expect(nameTaken('api', sessions)).toBe(false)
  })
})

describe('the prefill of Clone with options… (#434)', () => {
  const source = { id: 's1', name: 'api-sweep' }

  it('carries the yolo choice as given, false as false', () => {
    const p = clonePrefill(source, { agentId: 'claude', cwd: '/repo', args: [], yolo: false })
    expect(p).toEqual({ agentId: 'claude', cwd: '/repo', args: '', argv: [], yolo: false, clone: { id: 's1', name: 'api-sweep', attempt: false } })
    expect(Object.hasOwn(p, 'yolo')).toBe(true)
    expect(clonePrefill(source, { agentId: 'claude', cwd: '/repo', args: [], yolo: true }).yolo).toBe(true)
  })

  it("marks an attempt's clone", () => {
    expect(clonePrefill({ ...source, crew: { runId: 'r', crewId: 'c', member: 'a', task: 't1', attempt: 2 } }, { agentId: 'claude', cwd: '/r/.conductor/worktrees/a', args: [], yolo: false }).clone?.attempt).toBe(true)
  })

  it('shows the arguments as a line that reads back to them, a space or a quote among them', () => {
    for (const args of [
      ['--model', 'opus'],
      ["it's", 'two words', "it's two words"],
      ['say "hi"', '-x="a b"', "'quoted"],
      ['', '--empty='],
      ['"lead'],
    ]) {
      const p = clonePrefill(source, { agentId: 'claude', cwd: '/r', args, yolo: false })
      expect(splitArgs(p.args!)).toEqual(args)
      expect(p.argv).toEqual(args)
    }
    expect(argsText(['--model', 'opus', 'two words'])).toBe('--model opus "two words"')
  })

  it('keeps the arguments themselves for the one a line cannot hold', () => {
    const args = [`it's "both" here`]
    const p = clonePrefill(source, { agentId: 'claude', cwd: '/r', args, yolo: false })
    expect(splitArgs(p.args!)).not.toEqual(args)
    expect(p.argv).toEqual(args)
  })
})

describe('what the clone dialogs say', () => {
  it("warns of a run's worktree", () => {
    expect(inRunWorktree('/home/me/repo/.conductor/worktrees/r-1/a')).toBe(true)
    expect(inRunWorktree('/home/me/repo/.conductor/worktrees')).toBe(true)
    expect(inRunWorktree('/home/me/repo')).toBe(false)
    expect(inRunWorktree('/home/me/repo/.conductor')).toBe(false)
    expect(inRunWorktree(undefined)).toBe(false)
  })

  it("names a hosted session's machine, and says the server's refusals", () => {
    expect(hostedReason('studio')).toBe('Runs on studio: Conductor cannot start a session there yet')
    expect(cloneRefusal('invalid_agent', 'unknown agent id')).toBe("The source's agent is no longer in the catalog")
    expect(cloneRefusal('invalid_cwd', 'working directory does not exist')).toBe('The directory cannot be used: working directory does not exist')
    expect(cloneRefusal('name_taken', 'a session is named x already')).toBe('a session is named x already')
    expect(cloneRefusal('too_many_sessions', 'session limit reached')).toBe('session limit reached (too_many_sessions)')
  })
})
