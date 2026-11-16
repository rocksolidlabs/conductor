import { describe, expect, it } from 'vitest'
import type { RunTask } from '~/composables/useSessions'
import { taskDepths, taskListRows } from './taskGraph'

const task = (id: string, needs?: string[]): RunTask => ({ id, member: 'lead', kind: 'work', state: 'ready', attempts: 0, needs })

describe('taskDepths', () => {
  it('puts a task one column past the deepest task it needs', () => {
    const { depth, warnings } = taskDepths([task('build'), task('test', ['build']), task('deploy', ['test', 'build']), task('docs')])
    expect([...depth]).toEqual([
      ['build', 0],
      ['test', 1],
      ['deploy', 2],
      ['docs', 0],
    ])
    expect(warnings.size).toBe(0)
  })
  it('draws a task whose need is not in the run, or a cycle, as a root with a warning', () => {
    const { depth, warnings } = taskDepths([task('a', ['zzz']), task('b', ['c']), task('c', ['b'])])
    expect(depth.get('a')).toBe(0)
    expect(warnings.get('a')).toBe('needs zzz, which the run has no task for')
    expect(depth.get('b')).toBe(0)
    expect(depth.get('c')).toBe(0)
    expect(warnings.get('b') || warnings.get('c')).toBe('needs itself, through the tasks it needs')
  })
})

describe('taskListRows', () => {
  it('lists a task after the tasks it needs: by depth, then in the run\'s order, with its warning', () => {
    const rows = taskListRows([task('deploy', ['test']), task('test', ['build']), task('build'), task('docs'), task('lost', ['zzz'])])
    expect(rows.map((n) => `${n.id}@${n.depth}`)).toEqual(['build@0', 'docs@0', 'lost@0', 'test@1', 'deploy@2'])
    expect(rows.find((n) => n.id === 'lost')?.warning).toBe('needs zzz, which the run has no task for')
    expect(rows.find((n) => n.id === 'build')).not.toHaveProperty('warning')
  })
})
