import { describe, expect, it } from 'vitest'
import type { CapacityView } from './capacity'
import { launchLines } from './crewLaunch'
import { draftTasks, plainTasks } from './crews'
import { accountCountWords, launchAccounts, memberLogin, type CountedMember } from './launchAccounts'

const doc = {
  accounts: [
    { id: 'a-work', adapter: 'claude', label: 'Work' },
    { id: 'a-team', adapter: 'codex', label: 'Team' },
  ],
  adapters: [
    { adapter: 'claude', default: 'a-work' },
    { adapter: 'codex', default: 'default' },
  ],
}
const view = (v: Partial<CapacityView>) => ({ state: 'never_read', host: { kind: 'native' }, account: { id: 'default' }, agent: { id: 'x' }, windows: [], credits: [], fresh: 'none', model: { chosen: '' }, ...v }) as CapacityView

/** A comparison's crew as the template makes it: cand-a… on `agents`, a review after each by rev-a… on `reviewer` when given. */
function comparison(agents: Array<{ agentId: string; account?: string }>, reviewer?: { agentId: string; account?: string }) {
  const l = agents.map((_, i) => String.fromCharCode(97 + i))
  const members: CountedMember[] = [...agents.map((a, i) => ({ name: `cand-${l[i]}`, ...a })), ...(reviewer ? l.map((x) => ({ name: `rev-${x}`, ...reviewer })) : [])]
  const tasks = [
    ...l.map((x) => ({ id: `approach-${x}`, member: `cand-${x}`, kind: 'work' as const })),
    ...(reviewer ? l.map((x) => ({ id: `review-${x}`, member: `rev-${x}`, kind: 'review' as const, needs: [`approach-${x}`] })) : []),
  ]
  return { members, tasks, compare: { candidates: l.map((x) => ({ task: `approach-${x}` })) } }
}

/** The login of each member as the dialog resolves it: by the account it names, else the view of its line. */
const byNamed = (members: readonly CountedMember[], views: Record<string, CapacityView> = {}) => (name: string) => {
  const m = members.find((x) => x.name === name)!
  return memberLogin(m, { doc, view: views[m.agentId] ?? null, agent: m.agentId === 'claude' ? 'Claude Code' : 'Codex' })
}

describe("a launch's sessions per account", () => {
  it('counts a comparison by its candidates, its reviews after them as "up to"', () => {
    const c = comparison([{ agentId: 'claude', account: 'a-work' }, { agentId: 'codex', account: 'a-team' }, { agentId: 'claude', account: 'a-work' }], { agentId: 'claude', account: 'a-work' })
    const counts = launchAccounts({ ...c, accountOf: byNamed(c.members) })
    expect(counts).toEqual([
      { key: 'account:a-work', words: 'Work', first: 2, later: 3 },
      { key: 'account:a-team', words: 'Team', first: 1, later: 0 },
    ])
    expect(counts.map(accountCountWords)).toEqual(['this launch: 2 sessions on Work, then up to 3 more', 'this launch: 1 session on Team'])
  })

  it('says a login only reviews use as up to, after the candidates', () => {
    const c = comparison([{ agentId: 'claude', account: 'a-work' }, { agentId: 'claude', account: 'a-work' }], { agentId: 'codex', account: 'a-team' })
    expect(launchAccounts({ ...c, accountOf: byNamed(c.members) }).map(accountCountWords)).toEqual(['this launch: 2 sessions on Work', 'this launch: up to 2 sessions on Team, after the candidates'])
  })

  it('counts an ordinary crew with tasks by the tasks that need none, and one without tasks by the members that start at once', () => {
    const members: CountedMember[] = [
      { name: 'builder', agentId: 'claude', account: 'a-work' },
      { name: 'tester', agentId: 'claude', account: 'a-work' },
      { name: 'reviewer', agentId: 'codex', account: 'a-team' },
    ]
    const tasks = [
      { id: 'build', member: 'builder', kind: 'work' as const },
      { id: 'test', member: 'tester', kind: 'work' as const },
      { id: 'review', member: 'reviewer', kind: 'review' as const, needs: ['build', 'test'] },
    ]
    expect(launchAccounts({ members, tasks, accountOf: byNamed(members) }).map(accountCountWords)).toEqual(['this launch: 2 sessions on Work'])
    const started: CountedMember[] = [
      { ...members[0]!, start: { when: 'immediately' } },
      { ...members[1]!, start: { when: 'after', member: 'builder' } as { when: string } },
      { ...members[2]!, start: { when: 'immediately' } },
    ]
    expect(launchAccounts({ members: started, accountOf: byNamed(started) }).map(accountCountWords)).toEqual(['this launch: 1 session on Work', 'this launch: 1 session on Team'])
  })

  it("never picks a default: a member naming none is the default at the launch until the server's view names it, each tool's its own", () => {
    const c = comparison([{ agentId: 'claude' }, { agentId: 'codex' }, { agentId: 'claude' }])
    expect(launchAccounts({ ...c, accountOf: byNamed(c.members) }).map(accountCountWords)).toEqual([
      'this launch: 2 sessions on the default at the launch of Claude Code',
      'this launch: 1 session on the default at the launch of Codex',
    ])
    const views = { claude: view({ account: { id: 'a-work', label: 'Work' }, agent: { id: 'claude', adapter: 'claude' } }), codex: view({ account: { id: 'default' }, agent: { id: 'codex', adapter: 'codex' } }) }
    expect(launchAccounts({ ...c, accountOf: byNamed(c.members, views) }).map(accountCountWords)).toEqual(['this launch: 2 sessions on Work', "this launch: 1 session on Codex's default login"])
    // A login the entry sets is said as the entry's, whatever is named.
    const entry = { claude: view({ entryLogin: 'CLAUDE_CONFIG_DIR', agent: { id: 'claude', adapter: 'claude' } }) }
    expect(launchAccounts({ ...comparison([{ agentId: 'claude' }, { agentId: 'claude' }]), accountOf: byNamed(comparison([{ agentId: 'claude' }, { agentId: 'claude' }]).members, entry) }).map(accountCountWords)).toEqual([
      "this launch: 2 sessions on the login Claude Code's entry sets (CLAUDE_CONFIG_DIR)",
    ])
  })

  it("leaves AC1's lines as they were: the count reads them and changes none", () => {
    const c = comparison([{ agentId: 'claude', account: 'a-work' }, { agentId: 'codex' }])
    const o = { agents: [{ id: 'claude', name: 'Claude Code' }, { id: 'codex', name: 'Codex' }], doc, managed: true, yolo: false }
    const before = JSON.stringify(launchLines(c.members, o))
    launchAccounts({ ...c, accountOf: byNamed(c.members) })
    expect(JSON.stringify(launchLines(c.members, o))).toBe(before)
    expect(JSON.parse(before).map((l: { account: string }) => l.account)).toEqual(['Work', 'the default at the launch'])
  })

  it("counts a draft's tasks by their needs as the sheet hands them (plainTasks), a review after its candidate as later", () => {
    const c = comparison([{ agentId: 'claude', account: 'a-work' }, { agentId: 'claude', account: 'a-work' }], { agentId: 'codex', account: 'a-team' })
    const drafted = draftTasks(c.tasks.map((t) => ({ ...t, instructions: '' })))
    // A draft keeps its needs by key: unread, every task would look like one that needs none.
    expect(drafted.every((t) => !('needs' in t))).toBe(true)
    expect(launchAccounts({ ...c, tasks: plainTasks(drafted), accountOf: byNamed(c.members) }).map(accountCountWords)).toEqual(['this launch: 2 sessions on Work', 'this launch: up to 2 sessions on Team, after the candidates'])
    // An ordinary crew's draft: the review needs the build, so it starts nothing at once.
    const plain = draftTasks([
      { id: 'build', member: 'cand-a', kind: 'work', instructions: '' },
      { id: 'look', member: 'rev-a', kind: 'review', instructions: '', needs: ['build'] },
    ])
    expect(launchAccounts({ members: c.members, tasks: plainTasks(plain), accountOf: byNamed(c.members) }).map(accountCountWords)).toEqual(['this launch: 1 session on Work'])
  })
})
