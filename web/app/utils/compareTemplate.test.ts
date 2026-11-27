import { describe, expect, it } from 'vitest'
import {
  CHIP_ARGS,
  DEFAULT_CRITERION,
  checkNote,
  chipArgs,
  compareProblems,
  compareSpec,
  criterionId,
  draftComparison,
  draftOnOpen,
  inMore,
  nameFromTask,
  nextAgent,
  notSavedWords,
  optionsLine,
  specCriteria,
  startingAgents,
  trialFolderLine,
  withAgent,
  type DraftComparison,
  type TrialAgent,
} from './compareTemplate'

const agents: TrialAgent[] = [
  { id: 'shell', name: 'Shell', allowArgs: true },
  { id: 'aider', name: 'Aider', allowArgs: true },
  { id: 'claude', name: 'Claude Code', allowArgs: true, adapter: 'claude', firstMessage: true },
  { id: 'gone', name: 'Gone', allowArgs: true, adapter: 'claude', firstMessage: true, available: false },
  { id: 'codex', name: 'Codex', allowArgs: true, adapter: 'codex', firstMessage: true },
  { id: 'plain', name: 'Plain', allowArgs: false },
]

/** A draft the server would take: two agents, a task, a check, a folder. */
function ready(): DraftComparison {
  const d = draftComparison(agents)
  d.task = 'Make the cache read fast'
  d.folder = '/srv/app'
  d.check = 'go test ./...'
  d.agents[1]!.args = '--model x'
  d.agents[1]!.approach = '  drop the cache and memoise in the handler  '
  return d
}

describe('the agents a new trial starts with', () => {
  it('takes two different installed agents that take a first message and have an adapter, never the shell', () => {
    expect(startingAgents(agents)).toEqual(['claude', 'codex'])
    expect(draftComparison(agents).agents.map((a) => a.agentId)).toEqual(['claude', 'codex'])
  })

  it('falls back to an agent with an adapter, then any other, and never to one not installed', () => {
    expect(startingAgents(agents.filter((a) => a.id !== 'codex'))).toEqual(['claude', 'aider'])
    expect(startingAgents([{ id: 'goose', name: 'Goose', adapter: 'goose' }, { id: 'aider', name: 'Aider' }, { id: 'claude', name: 'Claude Code', adapter: 'claude', firstMessage: true }])).toEqual(['claude', 'goose'])
  })

  it('takes one installed agent twice, and none when none is installed', () => {
    expect(startingAgents([agents[0]!, agents[2]!, agents[3]!])).toEqual(['claude', 'claude'])
    expect(startingAgents([agents[0]!, agents[3]!])).toEqual([])
    expect(startingAgents([])).toEqual([])
    expect(draftComparison([]).agents).toEqual([])
  })

  it('adds the next installed agent not yet in the trial, else the last one again', () => {
    const d = draftComparison(agents)
    expect(nextAgent(d, agents)).toBe('aider')
    d.agents.push({ key: 0, agentId: 'aider', args: '', approach: '' }, { key: 0, agentId: 'plain', args: '', approach: '' })
    expect(nextAgent({ agents: d.agents.slice(0, 3) }, agents)).toBe('plain')
    expect(nextAgent(d, agents)).toBe('plain')
  })
})

describe('a new draft', () => {
  it('starts with one line for a good result, no more checks, Yolo as the server says, nothing typed', () => {
    const d = draftComparison(agents)
    expect(d.criteria.map((c) => c.text)).toEqual([DEFAULT_CRITERION])
    expect(DEFAULT_CRITERION).toBe('Does what the task asks')
    expect([d.task, d.folder, d.check, d.name, d.told, d.checks, d.yolo]).toEqual(['', '', '', '', '', [], undefined])
  })
})

describe("the New agent trial form's body", () => {
  it("posts the template route's fields: the task as the goal, the name from it, the Check line as an argv, the arguments split, Yolo as shown", () => {
    expect(compareSpec(ready(), true)).toEqual({
      name: 'Make the cache read fast',
      cwd: '/srv/app',
      goal: 'Make the cache read fast',
      instructions: '',
      criteria: [{ id: 'does-what-the-task-asks', text: 'Does what the task asks' }],
      checks: [{ id: 'check', argv: ['go', 'test', './...'] }],
      candidates: [{ agentId: 'claude' }, { agentId: 'codex', args: ['--model', 'x'], approach: 'drop the cache and memoise in the handler' }],
      yolo: true,
    })
    expect(compareSpec(ready(), false).yolo).toBe(false)
  })

  it('sends a typed name, what every agent is told, a quoted argument as one, and more checks after the Check line', () => {
    const d = ready()
    d.name = '  Cache key  '
    d.told = ' Keep the API. '
    d.agents[0]!.args = '--append-system-prompt "be brief"'
    d.check = 'npm test'
    d.checks = [{ id: 'lint', argv: ['npm', 'run', 'lint'], timeoutSeconds: 300 }]
    const s = compareSpec(d, false)
    expect(s.name).toBe('Cache key')
    expect(s.instructions).toBe('Keep the API.')
    expect(s.candidates[0]).toEqual({ agentId: 'claude', args: ['--append-system-prompt', 'be brief'] })
    expect(s.checks).toEqual([
      { id: 'check', argv: ['npm', 'test'] },
      { id: 'lint', argv: ['npm', 'run', 'lint'], timeoutSeconds: 300 },
    ])
  })

  it("sends the reviews with their agent, Brakeman's summary, the project's checks, and no check without a Check line", () => {
    const d = ready()
    d.reviews = true
    d.reviewAgent = 'codex'
    d.brakeman = true
    d.manifestFile = true
    d.check = ''
    const s = compareSpec(d, false)
    expect(s).toMatchObject({ reviews: true, reviewAgent: 'codex', brakeman: true, manifest: { file: true } })
    expect(s.checks).toBeUndefined()
    expect(compareSpec(ready(), false)).not.toHaveProperty('reviewAgent')
  })

  it("names a trial from its task's first line, cut at a word within 60 characters", () => {
    expect(nameFromTask('  Add rate limiting to the login route\nand tell nobody  ')).toBe('Add rate limiting to the login route')
    const long = 'Make every read of the price cache fast under load without a new dependency at all'
    const name = nameFromTask(long)
    expect([...name].length).toBeLessThanOrEqual(60)
    expect(long.startsWith(name) && long[name.length] === ' ').toBe(true)
    expect(nameFromTask('x'.repeat(80))).toBe('x'.repeat(60))
  })

  it("names each line for a good result from its words, as a task's id is written, never twice", () => {
    expect(specCriteria([{ text: 'p99 of the cache read under 2 ms in `bench_test.go`' }, { text: 'p99 of the cache read under 2 ms in `bench_test.go`' }, { text: '¿?' }])).toEqual([
      { id: 'p99-of-the-cache-read-under-2-ms', text: 'p99 of the cache read under 2 ms in `bench_test.go`' },
      { id: 'p99-of-the-cache-read-under-2-ms-2', text: 'p99 of the cache read under 2 ms in `bench_test.go`' },
      { id: 'criterion-3', text: '¿?' },
    ])
    for (const id of specCriteria([{ text: 'A'.repeat(500) }, { text: '--x--' }]).map((c) => c.id)) expect(id).toMatch(/^[a-z0-9][a-z0-9_-]{0,39}$/)
    expect(criterionId('No new dependency', 1, ['no-new-dependency', 'no-new-dependency-2'])).toBe('no-new-dependency-3')
  })
})

describe('what stops the press, field by field', () => {
  const fields = (d: DraftComparison) => compareProblems(d, agents).map((p) => p.field)

  it('lets a draft within the bounds be pressed', () => {
    expect(compareProblems(ready(), agents)).toEqual([])
  })

  it('marks the task, the check and the folder of an empty form, in the form order, each in its words', () => {
    expect(compareProblems(draftComparison(agents), agents)).toEqual([
      { field: 'task', words: 'Say what each agent should do.' },
      { field: 'check', words: 'Give a command that tests each result, such as npm test.' },
      { field: 'folder', words: 'Pick the folder.' },
    ])
  })

  it("takes a check under More options or the project's checks in place of the Check line", () => {
    const d = ready()
    d.check = ''
    expect(fields(d)).toEqual(['check'])
    d.manifestFile = true
    expect(fields(d)).toEqual([])
    d.manifestFile = false
    d.checks = [{ id: 'lint', argv: ['npm', 'run', 'lint'] }]
    expect(fields(d)).toEqual([])
  })

  it('marks an open quote in the check, and a check under More options whose id is the Check line\'s', () => {
    const d = ready()
    d.check = 'npm test -- "half'
    expect(compareProblems(d, agents)).toEqual([{ field: 'check', words: 'Close the quote in the check.' }])
    d.check = 'npm test'
    d.checks = [{ id: 'check', argv: ['npm', 'run', 'lint'] }]
    expect(fields(d)).toEqual(['checks'])
  })

  it('holds the agents to 2 to 4, each picked and on this server', () => {
    const one = ready()
    one.agents = one.agents.slice(0, 1)
    expect(compareProblems(one, agents)).toEqual([{ field: 'agents', words: 'A trial has 2 to 4 agents.' }])
    const five = ready()
    five.agents = [...five.agents, ...five.agents, five.agents[0]!]
    expect(fields(five)).toEqual(['agents'])
    const unpicked = ready()
    unpicked.agents[1]!.agentId = ''
    expect(compareProblems(unpicked, agents)).toEqual([{ field: 'agent-1', words: 'Pick an agent.' }])
    unpicked.agents[1]!.agentId = 'nobody'
    expect(compareProblems(unpicked, agents)).toEqual([{ field: 'agent-1', words: 'Agent B: nobody is not on this server.' }])
  })

  it("marks an agent's arguments with an open quote, or given to an agent that takes none, on that agent's row", () => {
    const d = ready()
    d.agents[0]!.args = '--model "opus'
    expect(compareProblems(d, agents)).toEqual([{ field: 'args-0', words: 'Agent A: close the quote in its arguments.' }])
    d.agents[0]!.args = ''
    d.agents[1]!.agentId = 'plain'
    expect(compareProblems(d, agents)).toEqual([{ field: 'args-1', words: 'Agent B: Plain takes no arguments.' }])
  })

  it('holds the lines for a good result to 1 to 12, each one line of at most 500 characters', () => {
    const none = ready()
    none.criteria = []
    expect(compareProblems(none, agents)).toEqual([{ field: 'criteria', words: '1 to 12 lines.' }])
    const many = ready()
    many.criteria = Array.from({ length: 13 }, (_, i) => ({ key: i, text: `c ${i}` }))
    expect(fields(many)).toEqual(['criteria'])
    const long = ready()
    long.criteria[0]!.text = 'x'.repeat(501)
    expect(compareProblems(long, agents)).toEqual([{ field: 'criteria', words: 'Line 1: at most 500 characters.' }])
    long.criteria[0]!.text = 'x'.repeat(500)
    expect(compareProblems(long, agents)).toEqual([])
    long.criteria[0]!.text = 'one\ntwo'
    expect(compareProblems(long, agents)).toEqual([{ field: 'criteria', words: 'Line 1: one line.' }])
    long.criteria[0]!.text = 'one line\t'
    expect(compareProblems(long, agents)).toEqual([])
  })

  it('holds an approach, the name and what every agent is told to their bounds, and a review to its agent', () => {
    const d = ready()
    d.agents[0]!.approach = 'y'.repeat(2001)
    d.reviews = true
    d.name = 'n'.repeat(61)
    d.told = 't'.repeat(20_001)
    expect(fields(d)).toEqual(['approach-0', 'told', 'reviews', 'name'])
    expect(compareProblems(d, agents).find((p) => p.field === 'reviews')?.words).toBe('Pick the agent that reviews.')
  })

  it('says which fields are inside More options', () => {
    expect(['task', 'agents', 'agent-1', 'check', 'folder'].map((f) => inMore(f as never))).toEqual([false, false, false, false, false])
    expect(['name', 'told', 'criteria', 'checks', 'reviews', 'args-0', 'approach-3'].map((f) => inMore(f as never))).toEqual([true, true, true, true, true, true, true])
  })
})

describe('the note under a Check line written as a shell line', () => {
  it("says it runs as one command when a shell's operator is a word of its own, and stops nothing", () => {
    for (const line of ['npm test && npm run lint', 'make || true', 'go test ./... | tee out', 'npm test ; npm run lint'])
      expect(checkNote(line)).toBe('This runs as one command, not in a shell. Add a second check under More options.')
    for (const line of ['npm test', 'grep -e a|b', 'npm test;', '']) expect(checkNote(line)).toBe('')
    const d = ready()
    d.check = 'npm test && npm run lint'
    expect(compareProblems(d, agents)).toEqual([])
  })
})

describe("an agent's chip", () => {
  it('shows its arguments as typed, cut in the middle past 24 characters', () => {
    expect(chipArgs('  --model   opus ')).toBe('--model opus')
    expect(chipArgs('x'.repeat(24))).toBe('x'.repeat(24))
    const cut = chipArgs('--model opus --append-system-prompt "be brief"')
    expect([...cut].length).toBe(CHIP_ARGS)
    expect(cut).toBe('--model opus… "be brief"')
  })
})

describe('the closed More options row', () => {
  it('says nothing of an untouched fold', () => {
    expect(optionsLine(draftComparison(agents))).toBe('')
  })

  it('names what was changed inside, in the order of the fold', () => {
    const d = ready()
    d.told = 'Keep the API.'
    d.criteria.push({ key: 0, text: 'no new dependency' })
    d.checks = [{ id: 'lint', argv: ['npm', 'run', 'lint'] }]
    d.manifestFile = true
    d.reviews = true
    d.brakeman = true
    d.name = 'Cache key'
    expect(optionsLine(d)).toBe("approaches · arguments · instructions · 2 lines for a good result · 1 more check · project's checks · reviewed · summary by Brakeman · named")
    const one = draftComparison(agents)
    one.criteria[0]!.text = 'no new dependency'
    expect(optionsLine(one)).toBe('1 line for a good result')
  })
})

describe('an agent moved to another, and the draft across a close', () => {
  it('drops the arguments when the new agent takes none, so the press is not held by what the form no longer shows', () => {
    const a = { key: 1, agentId: 'claude', args: '--model "x', approach: 'p' }
    expect(withAgent(a, 'plain', agents)).toEqual({ key: 1, agentId: 'plain', args: '', approach: 'p' })
    expect(withAgent(a, 'codex', agents)).toEqual({ ...a, agentId: 'codex' })
  })

  it('opens again on the draft it kept; one with no agent yet takes the starting agents; a new one once ended', () => {
    const kept = ready()
    kept.task = 'Kept'
    expect(draftOnOpen(kept, agents)).toBe(kept)
    kept.agents = []
    expect(draftOnOpen(kept, agents).agents.map((a) => a.agentId)).toEqual(['claude', 'codex'])
    expect(draftOnOpen(kept, agents).task).toBe('Kept')
    const fresh = draftOnOpen(null, agents)
    expect(fresh.task).toBe('')
    expect(fresh.agents.map((a) => a.agentId)).toEqual(['claude', 'codex'])
  })
})

describe('what the form says of the folder as it is picked, and of a save the server refused', () => {
  it("says the server's words for a folder agents may not use, and nothing for any other answer, a new project's among them", () => {
    expect(trialFolderLine({ code: 'invalid_cwd', message: '/etc is not under an allowed root' })).toBe('/etc is not under an allowed root.')
    expect(trialFolderLine({ code: 'git_failed', message: 'could not run git' })).toBe('')
    expect(trialFolderLine(null)).toBe('')
  })

  it("keeps the server's words and says what was kept", () => {
    expect(notSavedWords('compare: a comparison has 2 to 4 candidates, not 1')).toBe('Compare: a comparison has 2 to 4 candidates, not 1. What you typed is kept.')
    expect(notSavedWords('Crews are off here. ')).toBe('Crews are off here. What you typed is kept.')
  })
})
