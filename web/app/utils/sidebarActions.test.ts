import { describe, expect, it } from 'vitest'
import type { SessionInfo } from '~/composables/useSessions'
import { ANSWER_IN_TERMINAL, TERMINAL_FIRST } from './attention'
import { HOST_AWAY, accountItems, rowMenuItems, rowPrompt, sessionLive, stopQuestion } from './sidebarActions'

const on = { open: () => {}, openRun: () => {}, share: () => {}, yard: () => {}, rename: () => {}, stop: () => {}, forget: () => {} }
const shape = (groups: ReturnType<typeof rowMenuItems>) => groups.map((g) => g.map((i) => `${i.label}${i.kbds ? ` [${i.kbds.join('+')}]` : ''}${i.disabled ? ' (off)' : ''}${i.color ? ` ${i.color}` : ''}`))

describe('rowMenuItems', () => {
  it('offers a session Open, Share, the Yard and Stop; a member its run too', () => {
    expect(shape(rowMenuItems({ kind: 'session', name: 'docs-sweep', live: true }, on))).toEqual([['Open [enter]'], ['Share… [S]', 'Show in the Yard'], ['Stop… [X] error']])
    expect(shape(rowMenuItems({ kind: 'session', name: 'core', live: true, inRun: true }, on))).toEqual([['Open [enter]', 'Open the run [R]'], ['Share… [S]', 'Show in the Yard'], ['Stop… [X] error']])
  })

  it('offers a server session Rename…, running or ended, before Stop; a hosted one none (#318)', () => {
    expect(shape(rowMenuItems({ kind: 'session', name: 'docs-sweep', live: true, renamable: true }, on))).toEqual([['Open [enter]'], ['Share… [S]', 'Show in the Yard'], ['Rename…'], ['Stop… [X] error']])
    expect(shape(rowMenuItems({ kind: 'session', name: 'old', live: false, renamable: true }, on))).toEqual([['Open [enter]'], ['Share… [S] (off)', 'Show in the Yard'], ['Rename…'], ['Stop… [X] (off) error']])
    expect(shape(rowMenuItems({ kind: 'session', name: 'laptop', live: true, renamable: false }, on)).flat()).not.toContain('Rename…')
  })

  it('leaves an exited session nothing to stop or share, and still opens', () => {
    expect(shape(rowMenuItems({ kind: 'session', name: 'old', live: false }, on))).toEqual([['Open [enter]'], ['Share… [S] (off)', 'Show in the Yard'], ['Stop… [X] (off) error']])
  })

  it('offers a run header Open run, Share run and Stop run', () => {
    expect(shape(rowMenuItems({ kind: 'run', name: 'users api', live: true }, on))).toEqual([['Open run [enter]'], ['Share run [S]'], ['Stop run… [X] error']])
    expect(shape(rowMenuItems({ kind: 'run', name: 'users api', live: false }, on))).toEqual([['Open run [enter]'], ['Share run [S] (off)'], ['Stop run… [X] (off) error']])
  })

  it('offers a shared link Open and Forget', () => {
    expect(shape(rowMenuItems({ kind: 'shared', name: 'api-review', live: false }, on))).toEqual([['Open [enter]'], ['Forget']])
  })

  it('wires every item to its handler', () => {
    const calls: string[] = []
    const h = Object.fromEntries(Object.keys(on).map((k) => [k, () => void calls.push(k)])) as unknown as typeof on
    for (const g of rowMenuItems({ kind: 'session', name: 'x', live: true, inRun: true, renamable: true }, h)) for (const i of g) i.onSelect?.(new Event('select'))
    expect(calls).toEqual(['open', 'openRun', 'share', 'yard', 'rename', 'stop'])
  })
})

describe('stopQuestion and sessionLive', () => {
  it('asks in the row with the words of the design', () => {
    expect(stopQuestion({ kind: 'session', name: 'docs-sweep' })).toEqual({ title: 'Stop docs-sweep?', detail: 'Its terminal closes. Resume brings the conversation back from Exited.' })
    expect(stopQuestion({ kind: 'run', name: 'users api' }).title).toBe('Stop users api?')
    expect(sessionLive('running')).toBe(true)
    expect(sessionLive('starting')).toBe(true)
    expect(sessionLive('exited')).toBe(false)
    expect(sessionLive('host_disconnected')).toBe(false)
  })
})

describe('rowPrompt', () => {
  const session = (status: SessionInfo['status']) => ({ id: 's1', status } as SessionInfo)
  const options = [
    { label: 'Yes', input: 'Yes\r' },
    { label: 'No', input: 'No\r' },
  ]

  it('gives a prompt with choices its buttons and one without a reply field', () => {
    expect(rowPrompt({ session: session('running'), prompt: { message: 'Run the migration on staging?', options } })).toEqual({ message: 'Run the migration on staging?', choices: options, away: false, held: false, placeholder: 'Reply…' })
    expect(rowPrompt({ session: session('running'), prompt: { message: 'Which branch should I base it on?', options: [] } })).toEqual({ message: 'Which branch should I base it on?', choices: [], away: false, held: false, placeholder: 'Reply…' })
  })

  it('waits, with the words, while the host is away; nothing without a prompt', () => {
    const p = rowPrompt({ session: session('host_disconnected'), prompt: { message: 'Trust this folder?', options } })
    expect(p?.away).toBe(true)
    expect(p?.placeholder).toBe(HOST_AWAY)
    expect(p?.choices).toEqual(options)
    expect(rowPrompt({ session: session('running') })).toBeNull()
  })

  it('takes no reply while the session waits for its terminal', () => {
    const held = { ...session('running'), attention: { state: 'needs_input', source: 'submit', message: 'Text … type in the terminal to continue' } } as SessionInfo
    const p = rowPrompt({ session: held, prompt: { message: 'Text … type in the terminal to continue', options: [] } })
    expect(p?.held).toBe(true)
    expect(p?.away).toBe(false)
    expect(p?.placeholder).toBe(TERMINAL_FIRST)
  })

  it('takes no reply to a permission dialog whose choices it was not given: it is answered in the terminal (#288)', () => {
    const dialog = { ...session('running'), attention: { state: 'needs_input', source: 'api', kind: 'permission', message: 'Allow Bash?' } } as SessionInfo
    const p = rowPrompt({ session: dialog, prompt: { message: 'Allow Bash?', options: [], kind: 'permission' } })
    expect(p).toMatchObject({ held: true, away: false, placeholder: ANSWER_IN_TERMINAL, choices: [] })
    // With its choices read off the dialog, they are the answer.
    const read = { ...dialog, attention: { ...dialog.attention, source: 'question', options } } as SessionInfo
    expect(rowPrompt({ session: read, prompt: { message: 'Allow Bash?', options, kind: 'permission' } })).toMatchObject({ held: false, placeholder: 'Reply…', choices: options })
  })
})

describe('accountItems', () => {
  const on = { name: () => {}, shortcuts: () => {}, theme: () => {}, token: () => {}, forget: () => {} }
  it('lists your name, the shortcuts, the theme and the token; Forget token only with one', () => {
    expect(shape(accountItems(true, on))).toEqual([['Your name…'], ['Keyboard shortcuts [?]', 'Toggle theme'], ['Workbench token…', 'Forget token error']])
    expect(shape(accountItems(false, on))).toEqual([['Your name…'], ['Keyboard shortcuts [?]', 'Toggle theme'], ['Set workbench token…']])
  })
  it('wires every item to its handler', () => {
    const calls: string[] = []
    const h = Object.fromEntries(Object.keys(on).map((k) => [k, () => void calls.push(k)])) as unknown as typeof on
    for (const g of accountItems(true, h)) for (const i of g) i.onSelect?.(new Event('select'))
    expect(calls).toEqual(['name', 'shortcuts', 'theme', 'token', 'forget'])
  })
})
