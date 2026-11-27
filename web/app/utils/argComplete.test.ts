import { describe, expect, it } from 'vitest'
import type { AgentFlag } from '../composables/useSessions'
import { argContext, argRows, argWords, flagLabel, holdsYolo, listShows, replaceWord, typedAt } from './argComplete'

// The flags of a made-up agent, as GET /api/catalog/{id}/flags answers them: the help's order.
const flags: AgentFlag[] = [
  { name: '--add-dir', value: 'required', placeholder: 'directories', repeat: true, about: 'More folders it may read', kind: 'folder', keepsTerminal: true },
  { name: '--continue', aliases: ['-c'], value: 'none', about: 'Goes on with the last talk', keepsTerminal: true },
  { name: '--effort', value: 'required', placeholder: 'level', about: 'How hard it thinks', kind: 'choice', choices: [{ value: 'low' }, { value: 'medium' }, { value: 'high' }] },
  { name: '--model', value: 'required', placeholder: 'model', about: 'The model', kind: 'model', set: 'model', examples: ['alpha', 'beta'] },
  { name: '--mode', value: 'required', placeholder: 'mode', kind: 'choice', choices: [{ value: 'ask', about: 'Asks first' }, { value: 'skip', set: 'yolo' }, { value: 'plan mode' }] },
  { name: '--dangerously-skip', value: 'none', set: 'yolo' },
  { name: '--session-id', value: 'required', placeholder: 'uuid', set: 'session' },
  { name: '--settings', value: 'required', placeholder: 'file', set: 'launch' },
  { name: '--worktree', aliases: ['-w'], value: 'optional', placeholder: 'name' },
  { name: '--help', aliases: ['-h'], value: 'none', set: 'exit' },
  { name: '--version', value: 'none', set: 'exit' },
  { name: '--verbose', value: 'none', keepsTerminal: true },
]

const keys = (line: string, cursor = line.length, o = {}) => argRows(line, cursor, flags, o).map((r) => r.key)

describe('argWords', () => {
  it('splits as argv.ts does, with places, and holds an open quote to the line end', () => {
    expect(argWords('--a "b c" d')).toEqual([
      { start: 0, end: 3, text: '--a' },
      { start: 4, end: 9, text: 'b c', quote: '"' },
      { start: 10, end: 11, text: 'd' },
    ])
    expect(argWords("--a 'b c")).toEqual([
      { start: 0, end: 3, text: '--a' },
      { start: 4, end: 8, text: 'b c', quote: "'", open: true },
    ])
  })
})

describe('argContext', () => {
  it('reads the word at the cursor: the line end, mid-line, after a space', () => {
    expect(argContext('--ef', 4, flags)).toEqual({ kind: 'flag', typed: '--ef', from: 0, to: 4 })
    // Mid-line: only what is before the cursor counts as typed; the whole word is replaced.
    expect(argContext('--effort low', 4, flags)).toEqual({ kind: 'flag', typed: '--ef', from: 0, to: 8 })
    expect(argContext('--verbose ', 10, flags)).toEqual({ kind: 'none' })
    expect(argContext('--verbose ', 10, flags, true)).toEqual({ kind: 'flag', typed: '', from: 10, to: 10 })
  })

  it('reads a value after a flag that takes one, or after --flag=', () => {
    expect(argContext('--effort ', 9, flags)).toMatchObject({ kind: 'value', typed: '', from: 9, to: 9, joined: false })
    expect(argContext('--effort me', 11, flags)).toMatchObject({ kind: 'value', typed: 'me', from: 9, to: 11 })
    expect(argContext('--effort=hi', 11, flags)).toMatchObject({ kind: 'value', typed: 'hi', from: 9, to: 11, joined: true })
    expect(argContext('-c ', 3, flags)).toEqual({ kind: 'none' })
  })

  it('takes a word that starts with a dash after an optional value for a flag', () => {
    expect(argContext('--worktree --ve', 15, flags)).toEqual({ kind: 'flag', typed: '--ve', from: 11, to: 15 })
  })

  it('reads inside an open quote as a value', () => {
    expect(argContext('--mode "plan', 12, flags)).toMatchObject({ kind: 'value', typed: 'plan', from: 7, to: 12 })
  })

  it('offers nothing after a lone --, and takes -- as it is typed for the start of a flag', () => {
    expect(argContext('--', 2, flags)).toEqual({ kind: 'flag', typed: '--', from: 0, to: 2 })
    expect(argContext('-- --ve', 7, flags)).toEqual({ kind: 'none' })
    expect(argContext('-- ', 3, flags, true)).toEqual({ kind: 'none' })
  })
})

describe('argRows', () => {
  it('offers in the help order at --, leaving out what the form or a launch sets', () => {
    expect(keys('--')).toEqual(['--add-dir', '--continue', '--effort', '--model', '--mode', '--worktree', '--verbose'])
    // With a Model field for the agent, the model flag is the field's.
    expect(keys('--', 2, { modelField: true })).toEqual(['--add-dir', '--continue', '--effort', '--mode', '--worktree', '--verbose'])
  })

  it('puts the exact one first, then starts-with, then holds', () => {
    expect(keys('--mode')).toEqual(['--mode', '--model'])
    expect(keys('--effort')).toEqual(['--effort'])
    expect(keys('--mo')).toEqual(['--model', '--mode'])
    expect(keys('--ver')).toEqual(['--verbose'])
    expect(keys('--o')).toEqual(['--add-dir', '--continue', '--effort', '--model', '--mode', '--worktree', '--verbose'].filter((k) => k.slice(2).includes('o')))
    expect(keys('-w')).toEqual(['--worktree'])
  })

  it('offers values: choices with their words, never a value set yolo, and a model flag its examples', () => {
    const rows = argRows('--mode ', 7, flags)
    expect(rows.map((r) => r.key)).toEqual(['ask', 'plan mode'])
    expect(rows[0]!.about).toBe('Asks first')
    expect(keys('--model ')).toEqual(['alpha', 'beta'])
    expect(keys('--effort h')).toEqual(['high'])
    expect(keys('--settings ')).toEqual([])
  })

  it('leaves a pick that replaces the word alone, a space after it, the cursor after that', () => {
    const [ef] = argRows('--ef', 4, flags)
    expect(ef).toMatchObject({ text: '--effort ', cursor: 9 })
    // Mid-line: the rest of the line stays, and its space is not doubled.
    const [mid] = argRows('--ef --verbose', 4, flags)
    expect(mid).toMatchObject({ text: '--effort --verbose', cursor: 9 })
    // A short spelling typed in full is kept.
    const [w] = argRows('-w', 2, flags)
    expect(w).toMatchObject({ key: '--worktree', text: '-w ', cursor: 3 })
    // The = form fills in after the =.
    const [hi] = argRows('--effort=h', 10, flags)
    expect(hi).toMatchObject({ text: '--effort=high ', cursor: 14 })
    // A value with a space goes in double quotes.
    const plan = argRows('--mode pl', 9, flags)[0]
    expect(plan).toMatchObject({ text: '--mode "plan mode" ', cursor: 19 })
  })

  it('marks a flag that keeps a session in its terminal', () => {
    const rows = argRows('--', 2, flags)
    expect(rows.filter((r) => r.terminalOnly).map((r) => r.key)).toEqual(['--add-dir', '--continue', '--verbose'])
  })
})

describe('the list', () => {
  it('closes when the only row is the word already typed', () => {
    expect(listShows(argRows('--effort', 8, flags), typedAt('--effort', 8, flags))).toBe(false)
    expect(listShows(argRows('--mode', 6, flags), typedAt('--mode', 6, flags))).toBe(true)
    expect(listShows(argRows('--mo', 4, flags), typedAt('--mo', 4, flags))).toBe(true)
    expect(listShows(argRows('--effort high', 13, flags), typedAt('--effort high', 13, flags))).toBe(false)
    expect(listShows([], '')).toBe(false)
  })

  it('writes a flag as its help does', () => {
    expect(flagLabel(flags[0]!)).toBe('--add-dir <directories...>')
    expect(flagLabel(flags[1]!)).toBe('--continue, -c')
    expect(flagLabel(flags[8]!)).toBe('--worktree [name], -w')
  })
})

describe('holdsYolo', () => {
  it('sees a flag or a value that turns the questions off, alone or with =', () => {
    expect(holdsYolo('--verbose --dangerously-skip', flags)).toBe(true)
    expect(holdsYolo('--mode skip', flags)).toBe(true)
    expect(holdsYolo('--mode=skip', flags)).toBe(true)
    expect(holdsYolo('--mode ask --effort high', flags)).toBe(false)
    expect(holdsYolo('-- --dangerously-skip', flags)).toBe(false)
  })
})

describe('replaceWord', () => {
  it('replaces the span and adds one space', () => {
    expect(replaceWord('a b', 2, 3, 'c')).toEqual({ text: 'a c ', cursor: 4 })
    expect(replaceWord('a b d', 2, 3, 'c')).toEqual({ text: 'a c d', cursor: 4 })
  })
})
