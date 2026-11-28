import { describe, expect, it } from 'vitest'
import type { AgentFlag } from '../composables/useSessions'
import { argContext, argRows, argWords, flagLabel, folderPrefix, folderRows, holdsYolo, keepsTerminal, listShows, needsValue, needsValueLine, replaceWord, terminalLine, terminalWords, typedAt, valueWord } from './argComplete'
import { splitArgs } from './argv'

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
      { start: 4, end: 9, text: 'b c', quote: '"', close: 8 },
      { start: 10, end: 11, text: 'd' },
    ])
    // What follows a closing quote up to the next white space is the same word, as splitArgs reads it.
    expect(argWords('"a b"c d')).toEqual([
      { start: 0, end: 6, text: 'a bc', quote: '"', close: 4 },
      { start: 7, end: 8, text: 'd' },
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

describe('a folder flag\'s value', () => {
  const at = (line: string) => argContext(line, line.length, flags)
  it('lists what an absolute path holds, the folders beside the session\'s with nothing typed, and nothing for a relative path', () => {
    expect(folderPrefix(at('--add-dir '), '/r/repo')).toBe('/r/')
    expect(folderPrefix(at('--add-dir '), '/r/repo/')).toBe('/r/')
    expect(folderPrefix(at('--add-dir '), '')).toBeNull()
    expect(folderPrefix(at('--add-dir /r/sh'), '/r/repo')).toBe('/r/sh')
    expect(folderPrefix(at('--add-dir="/r/two w'), '/r/repo')).toBeNull()
    expect(folderPrefix(at('--add-dir "/r/two w'), '/r/repo')).toBe('/r/two w')
    expect(folderPrefix(at('--add-dir ../shared'), '/r/repo')).toBeNull()
    // A value of another kind lists no folders.
    expect(folderPrefix(at('--effort '), '/r/repo')).toBeNull()
    expect(folderPrefix(at('--add-dir=/r/'), '/r/repo')).toBe('/r/')
  })

  it('puts the path in place of the word, in quotes when it holds a space, with no space after it', () => {
    const line = '--add-dir /r/s --verbose'
    const ctx = argContext(line, 14, flags)
    expect(folderRows(line, ctx, [{ path: '/r/shared', name: 'shared' }])).toEqual([{ key: '/r/shared', label: 'shared', text: '--add-dir /r/shared --verbose', cursor: 19 }])
    const open = '--add-dir "/r/two'
    expect(folderRows(open, argContext(open, open.length, flags), [{ path: '/r/two words', name: 'two words' }])[0]).toMatchObject({ text: '--add-dir "/r/two words"', cursor: 24 })
    expect(folderRows(line, argContext(line, 3, flags), [{ path: '/x', name: 'x' }])).toEqual([])
  })

  it('reads a closed quote at the cursor without its closing quote', () => {
    const line = '--add-dir "/r/two words"'
    expect(argContext(line, line.length, flags)).toMatchObject({ kind: 'value', typed: '/r/two words' })
  })
})

describe('a value picked is the one argument its row shows', () => {
  const line = '--add-dir '
  const ctx = argContext(line, line.length, flags)
  const pick = (path: string) => folderRows(line, ctx, [{ path, name: path.split('/').pop()! }])
  it('writes each awkward name so that the launch splits it back to the folder alone', () => {
    for (const path of ['/r/plain', '/r/two words', '/r/a"b c', "/r/it's here", '/r/a"b', '"/r/q', "'/r/q", '/r/émoji 🎈']) {
      const rows = pick(path)
      expect(rows, path).toHaveLength(1)
      expect(splitArgs(rows[0]!.text), path).toEqual(['--add-dir', path])
    }
  })
  it('offers no row for a name it cannot write as one argument, or that holds a mark nobody sees', () => {
    for (const path of ['/r/blank\u3164', '/r/blank\u115F', '/r/blank\u1160', '/r/blank\uFFA0', '/r/blank\u2800', '/r/a"b \'c', '/r/new\nline', '/r/bidi\u202Eevil', '/r/zero\u200Bwidth', '/r/nul\u0000', '/r/tab\there', '']) {
      expect(pick(path), JSON.stringify(path)).toEqual([])
    }
    expect(pick('/r/fine').map((r) => r.key)).toEqual(['/r/fine'])
  })
  it('writes only a plain value after --flag=, where a quote opens nothing', () => {
    const joined = '--add-dir='
    const jctx = argContext(joined, joined.length, flags)
    expect(folderRows(joined, jctx, [{ path: '/r/two words', name: 'two words' }])).toEqual([])
    const [row] = folderRows(joined, jctx, [{ path: '/r/plain', name: 'plain' }])
    expect(splitArgs(row!.text)).toEqual(['--add-dir=/r/plain'])
    expect(valueWord('a b', true)).toBeNull()
  })
  it('writes a listed value the same way', () => {
    const odd: AgentFlag[] = [{ name: '--mode', value: 'required', choices: [{ value: 'a"b c' }, { value: 'bad\u202Ex' }, { value: 'ok' }] }]
    const rows = argRows('--mode ', 7, odd)
    expect(rows.map((r) => r.key)).toEqual(['a"b c', 'ok'])
    expect(splitArgs(rows[0]!.text)).toEqual(['--mode', 'a"b c'])
  })
})

describe('the Yolo hint judges the words the agent gets', () => {
  const codexish: AgentFlag[] = [
    { name: '--ask-for-approval', aliases: ['-a'], value: 'required', choices: [{ value: 'on-request' }, { value: 'never', set: 'yolo' }] },
    { name: '--yes', aliases: ['-y'], value: 'none', set: 'yolo' },
    { name: '--quiet', aliases: ['-q'], value: 'none' },
  ]
  it('sees a flag or value in quotes, a short flag with its value attached, and one in a cluster', () => {
    expect(holdsYolo('"--dangerously-skip"', flags)).toBe(true)
    expect(holdsYolo("'--mode=skip'", flags)).toBe(true)
    expect(holdsYolo('--mode "skip"', flags)).toBe(true)
    expect(holdsYolo('-anever', codexish)).toBe(true)
    expect(holdsYolo('-a=never', codexish)).toBe(true)
    expect(holdsYolo('-a never', codexish)).toBe(true)
    expect(holdsYolo('-qy', codexish)).toBe(true)
    expect(holdsYolo('-aon-request -q', codexish)).toBe(false)
  })
  it('stops at a lone --, quoted or not', () => {
    expect(holdsYolo('-- -anever', codexish)).toBe(false)
    expect(holdsYolo('"--" --dangerously-skip', flags)).toBe(false)
  })
})

describe('keepsTerminal', () => {
  it('says a flag a chat refuses keeps the session in its terminal, and a lone -- for an agent with a chat way', () => {
    expect(keepsTerminal('--verbose', flags, true)).toBe(true)
    expect(keepsTerminal('--effort high', flags, true)).toBe(false)
    expect(keepsTerminal('-- fix the tests', flags, true)).toBe(true)
    expect(keepsTerminal('-- fix the tests', flags, false)).toBe(false)
    expect(keepsTerminal('--effort "a -- b"', flags, true)).toBe(false)
  })
})

describe('a folder picked in quotes', () => {
  const pick = (path: string) => {
    const line = '--add-dir '
    return folderRows(line, argContext(line, line.length, flags), [{ path, name: 'x' }])[0]!
  }
  const typeAt = (text: string, at: number, keys: string) => text.slice(0, at) + keys + text.slice(at)
  it('leaves the cursor after its closing quote: a child typed there is the same argument, a space and a flag are another', () => {
    for (const path of ['/r/two words', '/r/a"b c', "/r/it's here"]) {
      const row = pick(path)
      expect(row.cursor, path).toBe(row.text.length)
      const child = typeAt(row.text, row.cursor, '/sub')
      expect(splitArgs(child), path).toEqual(['--add-dir', path + '/sub'])
      // The list goes on to the folder's children, and to what is typed after the quote.
      expect(folderPrefix(argContext(row.text, row.cursor, flags), '/r/repo'), path).toBe(path)
      expect(folderPrefix(argContext(child, row.cursor + 4, flags), '/r/repo'), path).toBe(path + '/sub')
      const next = typeAt(row.text, row.cursor, ' --mode plan')
      expect(splitArgs(next), path).toEqual(['--add-dir', path, '--mode', 'plan'])
    }
  })
})

describe('the line under the field', () => {
  it('names what keeps the session in its terminal: a flag a chat refuses, and a lone -- for an agent with a chat way', () => {
    expect(terminalLine(terminalWords('--add-dir /x', flags, true))).toBe('With --add-dir, this session stays in its terminal.')
    expect(terminalLine(terminalWords('-- fix the tests', flags, true))).toBe('With --, this session stays in its terminal.')
    expect(terminalLine(terminalWords('--verbose --add-dir=/x -- go', flags, true))).toBe('With --verbose, --add-dir and --, this session stays in its terminal.')
    expect(terminalLine(terminalWords('-- fix', flags, false))).toBe('')
    expect(terminalLine(terminalWords('--effort high', flags, true))).toBe('')
  })
})

describe('a short cluster', () => {
  it('that holds a flag a chat refuses keeps the session in its terminal, as the server says', () => {
    // A letter that takes a value (-w) ends the cluster: what follows is its value, as the server reads it.
    expect(terminalWords('-wc', flags, true)).toEqual([])
    expect(terminalWords('-hc', flags, true)).toEqual(['-hc'])
    expect(terminalLine(terminalWords('-cw', flags, true))).toBe('With -cw, this session stays in its terminal.')
    expect(terminalWords('-hw', flags, true)).toEqual([])
  })
})

describe('a last flag that wants a value', () => {
  it('is said and held, by the help: a required value only, never an optional one or a flag the list does not know', () => {
    expect(needsValue('--effort', flags)).toBe('--effort')
    expect(needsValue('--verbose --effort ', flags)).toBe('--effort')
    expect(needsValueLine(needsValue('--effort', flags))).toBe('--effort needs a value.')
    expect(needsValue('--effort high', flags)).toBe('')
    expect(needsValue('--effort=', flags)).toBe('')
    expect(needsValue('--worktree', flags)).toBe('')
    expect(needsValue('-w', flags)).toBe('')
    expect(needsValue('--not-listed', flags)).toBe('')
    expect(needsValue('-- --effort', flags)).toBe('')
    expect(needsValue('', flags)).toBe('')
  })
})
