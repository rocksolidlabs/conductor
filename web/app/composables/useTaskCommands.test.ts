import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { RunTask } from './useSessions'
import { useTaskCommands } from './useTaskCommands'

// What Cancel and Accept anyway ask before they run (#1075): the Tasks view's dialog and the palette's row ask the one question, with
// its buttons' words: Keep it, and Cancel the task or Accept the result. The two words of a question are never the same (the palette
// once said Cancel on both of Cancel's buttons). useTaskCommands reads its auto-imports (ref, useSessions, useToast) as it is called.

beforeEach(() => {
  vi.stubGlobal('ref', ref)
  vi.stubGlobal('useSessions', () => ({}))
  vi.stubGlobal('useToast', () => ({ add: () => {} }))
})
afterEach(() => vi.unstubAllGlobals())

const task = (state: string): Pick<RunTask, 'id' | 'state' | 'attempts'> => ({ id: 'build', state, attempts: 1 }) as Pick<RunTask, 'id' | 'state' | 'attempts'>

describe('the task commands’ questions', () => {
  it('Cancel asks with Keep it and Cancel the task, whatever the task’s state', () => {
    const { question } = useTaskCommands(() => 'r1')
    for (const state of ['running', 'verifying', 'blocked', 'ready']) {
      const q = question(task(state), 'cancel')
      expect([q.keep, q.confirm]).toEqual(['Keep it', 'Cancel the task'])
      expect(q.title).toBe('Cancel the task build?')
    }
    expect(question(task('running'), 'cancel').detail).toMatch(/^Its attempt ends and its session is stopped/)
    expect(question(task('blocked'), 'cancel').detail).toMatch(/^The tasks that need it never start/)
  })

  it('Accept anyway asks with Keep it and Accept the result', () => {
    const q = useTaskCommands(() => 'r1').question(task('failed'), 'accept')
    expect([q.keep, q.confirm]).toEqual(['Keep it', 'Accept the result'])
    expect(q.title).toBe("Accept build's last result anyway?")
  })

  it('never gives a question two buttons of one word', () => {
    const { question } = useTaskCommands(() => 'r1')
    for (const c of ['cancel', 'accept'] as const) {
      const q = question(task('failed'), c)
      expect(q.confirm).not.toBe(q.keep)
      expect(q.confirm.split(' ')[0]).not.toBe(q.keep)
    }
  })
})
