import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { AT_LAUNCH, OWN_LOGIN, accountName, addMemberProblem, boundWords, brakemanAccount, cleanFolder, launchAccountWords, launchLines, memberAccountItems, memberAccountProblem, namedAccounts, withAgent } from '../utils/crewLaunch'
import { capacityKey, type CapacityView } from '../utils/capacity'
import { draftMember, toCrewInput, toCrewMember } from '../utils/crews'

// The crew launch sheet's lines and the crew's members on their accounts (docs/plans/capacity.md, PR-7). The app's vitest runs in node
// with no component mounted, so the sheet's lines are tested through the function it draws them from (launchLines), and the sheet's
// source is checked for the panels it draws; what the sheet reads as it opens is capacity-crew.spec.ts's.

/** The server's view of a line's question, as much of it as the words take. */
const view = (v: Partial<CapacityView>) => ({ state: 'never_read', host: { kind: 'native' }, account: { id: 'default' }, ...v }) as CapacityView

const doc = {
  accounts: [
    { id: 'a-aaaaaaaaaaaa', adapter: 'claude', label: 'Work', folder: '/home/me/.claude-work' },
    { id: 'a-bbbbbbbbbbbb', adapter: 'claude', label: 'Old', folder: '/home/me/.claude-old', retired: true },
    { id: 'a-cccccccccccc', adapter: 'codex', label: 'Team', folder: '/home/me/.codex-team' },
  ],
  adapters: [
    { adapter: 'claude', default: 'a-aaaaaaaaaaaa' },
    { adapter: 'codex', default: 'default' },
  ],
}
const agents = [
  { id: 'claude', name: 'Claude Code', adapter: 'claude' },
  { id: 'codex', name: 'Codex CLI', adapter: 'codex' },
  { id: 'plain', name: 'Plain shell' },
]

describe('the launch sheet', () => {
  it("draws one line per member: its agent, its model and its account, the default named by the server's view of the line", () => {
    const views: Record<string, CapacityView> = { claude: view({ account: { id: 'a-aaaaaaaaaaaa', label: 'Work' } }) }
    const lines = launchLines(
      [
        { name: 'builder', agentId: 'claude' },
        { name: 'reviewer', agentId: 'codex', account: 'a-cccccccccccc', model: 'gpt-e2e' },
        { name: 'shell', agentId: 'plain' },
      ],
      { agents, doc, managed: true, yolo: false, view: (q) => views[q.agent] ?? null },
    )
    expect(lines.map((l) => [l.name, l.agent, l.model, l.account])).toEqual([
      ['builder', 'Claude Code', "the agent's default", 'the default at the launch (now Work)'],
      ['reviewer', 'Codex CLI', 'gpt-e2e', 'Team'],
      ['shell', 'Plain shell', "the agent's default", 'the default at the launch'],
    ])
  })

  it('names the login the server resolves for a member that names none: its entry, its launch, the default; nothing before the view', () => {
    // The accounts say Work is Claude Code's default; the words never take that from them.
    expect(launchAccountWords(doc, undefined, null)).toBe('the default at the launch')
    expect(launchAccountWords(doc, undefined, view({ entryLogin: 'ANTHROPIC_BASE_URL' }))).toBe('the login its entry sets (ANTHROPIC_BASE_URL)')
    expect(launchAccountWords(doc, undefined, view({ state: 'env_set' }))).toBe('the login its launch sets')
    expect(launchAccountWords(doc, undefined, view({}))).toBe('the default at the launch (now Default login)')
    expect(launchAccountWords(doc, undefined, view({ account: { id: 'a-aaaaaaaaaaaa', label: 'Work' } }))).toBe('the default at the launch (now Work)')
    // A member that names an account is said by it, whatever the view.
    expect(launchAccountWords(doc, 'a-cccccccccccc', view({ entryLogin: 'X' }))).toBe('Team')
  })

  it("asks each line's view as its launch would get it: the account only when the member names one, the model, the run's kind and yolo", () => {
    const [builder, reviewer] = launchLines(
      [
        { name: 'builder', agentId: 'claude' },
        { name: 'reviewer', agentId: 'codex', account: 'default', model: ' gpt-e2e ' },
      ],
      { agents, doc, managed: false, yolo: true },
    )
    expect(builder!.query).toEqual({ agent: 'claude', launch: 'legacy', yolo: true })
    expect(reviewer!.query).toEqual({ agent: 'codex', account: 'default', model: 'gpt-e2e', launch: 'legacy', yolo: true })
    // Two members of one agent on one account and model ask one question.
    const twins = launchLines([{ name: 'a', agentId: 'claude' }, { name: 'b', agentId: 'claude' }], { agents, doc, managed: true, yolo: false })
    expect(capacityKey(twins[0]!.query)).toBe(capacityKey(twins[1]!.query))
  })

  it('names an agent the catalog no longer has by its id, and accounts not read by their ids', () => {
    const [l] = launchLines([{ name: 'x', agentId: 'gone', account: 'a-aaaaaaaaaaaa' }], { agents, doc: null, managed: true, yolo: false })
    expect([l!.agent, l!.account]).toEqual(['gone', 'a-aaaaaaaaaaaa'])
  })

  it("source: the sheet's lines draw compact panels that read on open, the ticket dialog's compact panels do not", () => {
    // The read itself, on an account never read before, is capacity-crew.spec.ts's (the sheet's panels reach `ok`).
    const src = readFileSync(new URL('./CrewLaunchSheet.vue', import.meta.url), 'utf8')
    expect(src).toContain('<CapacityPanel :query="open ? l.query : null" compact read-on-open />')
    const dispatch = readFileSync(new URL('./DispatchLaunch.vue', import.meta.url), 'utf8')
    expect(dispatch).toContain('<CapacityPanel :query="open ? l.query : null" compact />')
    expect(dispatch).not.toContain('read-on-open')
  })
})

describe("the crew editor's Account choice", () => {
  it('offers the default at launch, the default login and the accounts of the tool not retired, the default marked', () => {
    expect(memberAccountItems(doc, 'claude')).toEqual([
      { value: AT_LAUNCH, label: 'The default at launch (now Work)' },
      { value: 'default', label: 'Default login' },
      { value: 'a-aaaaaaaaaaaa', label: 'Work (default)' },
    ])
    expect(memberAccountItems(doc, 'codex').map((i) => i.label)).toEqual(['The default at launch (now Default login)', 'Default login (default)', 'Team'])
  })

  it('keeps the account a member names when it is no choice: retired, or not in Accounts', () => {
    expect(memberAccountItems(doc, 'claude', 'a-bbbbbbbbbbbb').at(-1)).toEqual({ value: 'a-bbbbbbbbbbbb', label: 'Old (retired)' })
    expect(memberAccountItems(doc, 'claude', 'a-zzzzzzzzzzzz').at(-1)).toEqual({ value: 'a-zzzzzzzzzzzz', label: 'a-zzzzzzzzzzzz (not in Accounts)' })
  })

  it('has no choice for a tool whose login is not moved, or before the accounts are read', () => {
    expect(memberAccountItems(doc, undefined)).toEqual([])
    expect(memberAccountItems(doc, 'goose')).toEqual([])
    expect(memberAccountItems(null, 'claude')).toEqual([])
  })

  it('says an account by its name', () => {
    expect(accountName(doc, 'default')).toBe('Default login')
    expect(accountName(doc, 'a-aaaaaaaaaaaa')).toBe('Work')
    expect(accountName(null, 'a-aaaaaaaaaaaa')).toBe('a-aaaaaaaaaaaa')
  })

  it("offers an agent whose entry sets its login that login alone, and says the server's refusal of another account", () => {
    expect(memberAccountItems(doc, 'claude', '', 'ANTHROPIC_BASE_URL')).toEqual([{ value: AT_LAUNCH, label: 'The login its entry sets (ANTHROPIC_BASE_URL)' }])
    expect(memberAccountItems(doc, 'claude', 'a-aaaaaaaaaaaa', 'ANTHROPIC_BASE_URL').at(-1)).toEqual({ value: 'a-aaaaaaaaaaaa', label: 'Work' })
    expect(memberAccountProblem('a-aaaaaaaaaaaa', 'ANTHROPIC_BASE_URL')).toBe("This agent's entry sets ANTHROPIC_BASE_URL: it uses that login, its default account only")
    // The server takes none and the default login for it, and any account for an agent whose entry sets none.
    expect([memberAccountProblem(undefined, 'ANTHROPIC_BASE_URL'), memberAccountProblem('default', 'ANTHROPIC_BASE_URL'), memberAccountProblem('a-aaaaaaaaaaaa', '')]).toEqual(['', '', ''])
  })

  it("clears a member's account and model when its agent moves to another tool, and keeps both within one", () => {
    const adapterOf = (id: string) => agents.find((a) => a.id === id)?.adapter
    const m = { name: 'builder', agentId: 'claude', account: 'a-aaaaaaaaaaaa', model: 'opus' }
    expect(withAgent(m, 'codex', adapterOf)).toEqual({ name: 'builder', agentId: 'codex', account: undefined, model: undefined })
    expect(withAgent(m, 'plain', adapterOf)).toMatchObject({ account: undefined, model: undefined })
    const twin = [...agents, { id: 'claude-2', name: 'Claude Code 2', adapter: 'claude' }]
    expect(withAgent(m, 'claude-2', (id) => twin.find((a) => a.id === id)?.adapter)).toEqual({ ...m, agentId: 'claude-2' })
  })

  it("saves the account and model a member names, and a member naming neither is the body it was", () => {
    const m = { name: 'builder', agentId: 'claude', prompt: '', start: { when: 'task' as const } }
    expect(JSON.stringify(toCrewMember(m))).toBe('{"name":"builder","agentId":"claude","prompt":"","start":{"when":"task"}}')
    expect(toCrewMember({ ...m, account: 'a-aaaaaaaaaaaa', model: ' opus ' })).toMatchObject({ account: 'a-aaaaaaaaaaaa', model: 'opus' })
    const crew = toCrewInput({ name: 'c', goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, members: [{ ...m, account: 'default', model: '' }] })
    expect(crew.members[0]).toEqual({ name: 'builder', agentId: 'claude', prompt: '', args: undefined, start: { when: 'task', member: undefined }, account: 'default', model: undefined })
  })
})

describe("the run page's logins", () => {
  it('names a bound account by its label, any other login by where it comes from, and the agent own login for none', () => {
    expect(boundWords({ account: { id: 'a-aaaaaaaaaaaa', source: 'account' }, model: 'opus' }, doc)).toEqual({ account: 'Work', model: 'opus', atLaunch: false })
    expect(boundWords({ account: { id: 'a-aaaaaaaaaaaa', source: 'account' } }, null).account).toBe('a-aaaaaaaaaaaa')
    expect(boundWords({}, doc)).toEqual({ account: "the agent's own login", model: "the agent's default", atLaunch: false })
    expect(boundWords({ account: { id: 'default', source: 'pinned' } }, doc).account).toBe('the login its entry sets')
    expect(boundWords({ account: { id: 'default', source: 'launch_env' } }, doc).account).toBe('the login its launch sets')
    expect(boundWords({ account: { id: 'unlisted', source: 'unlisted' } }, doc).account).toBe('a folder not in Accounts')
    expect(boundWords({}, doc, undefined, true).account).toBe("the login Brakeman's settings name")
  })

  it("says a ticket's run took the default at the launch for a member its revision named no account for", () => {
    const named = namedAccounts([{ name: 'builder' }, { name: 'reviewer', account: 'a-cccccccccccc' }])
    expect(named).toEqual({ builder: '', reviewer: 'a-cccccccccccc' })
    expect(boundWords({ account: { id: 'a-aaaaaaaaaaaa', source: 'account' } }, doc, named.builder).atLaunch).toBe(true)
    expect(boundWords({ account: { id: 'a-cccccccccccc', source: 'account' } }, doc, named.reviewer).atLaunch).toBe(false)
    // A run from no ticket says nothing of it.
    expect(boundWords({ account: { id: 'a-aaaaaaaaaaaa', source: 'account' } }, doc).atLaunch).toBe(false)
  })
})

describe("the run page's Logins: a member's model", () => {
  it("says a model the run records, the agent's default for an ordinary member with none, and not shown for a Brakeman turn's", () => {
    expect(boundWords({ model: 'opus' }, doc).model).toBe('opus')
    expect(boundWords({}, doc).model).toBe("the agent's default")
    // A Brakeman turn's model is in its arguments: its run records none, so none is said.
    expect(boundWords({}, doc, undefined, true).model).toBe("its model is not shown: a Brakeman turn's run does not record it")
    expect(boundWords({ model: 'opus' }, doc, undefined, true).model).toBe('opus')
  })
})

describe("the run's Add member dialog", () => {
  it('sends the account and model the added member names, and holds Add for a model the field marks', () => {
    const m = draftMember({ name: 'helper', agentId: 'claude', prompt: '', start: { when: 'immediately' }, account: 'a-aaaaaaaaaaaa', model: 'opus' })
    expect(toCrewMember(m)).toEqual({ name: 'helper', agentId: 'claude', prompt: '', args: undefined, start: { when: 'immediately', member: undefined }, account: 'a-aaaaaaaaaaaa', model: 'opus' })
    // The route's body is that member as toCrewMember makes it.
    expect(readFileSync(new URL('../composables/useSessions.ts', import.meta.url), 'utf8')).toContain('/members`, { method: \'POST\', body: toCrewMember(member) }')
    expect(addMemberProblem(m, ['builder'], ['nate'])).toBe('')
    expect(addMemberProblem({ ...m, model: '-bad model' }, ['builder'], [])).toMatch(/^A model is 1 to 100/)
    expect(addMemberProblem({ ...m, name: 'builder' }, ['builder'], [])).not.toBe('')
    expect(addMemberProblem({ ...m, agentId: '' }, [], [])).toBe('Pick an agent')
    expect(addMemberProblem(undefined, [], [])).toBe('Pick an agent')
  })
})

describe("Brakeman's Account choice", () => {
  it('offers the default login, saved as no folder, and each account of the tool not retired by its folder', () => {
    expect(brakemanAccount(doc, 'claude', '')).toEqual({
      items: [
        { value: OWN_LOGIN, label: 'Default login' },
        { value: '/home/me/.claude-work', label: 'Work' },
      ],
      value: OWN_LOGIN,
      unlisted: false,
      retired: '',
    })
  })

  it('shows a folder no account holds as a folder not in Accounts, and a retired account by its label, said refused', () => {
    expect(brakemanAccount(doc, 'claude', '/home/me/elsewhere')).toMatchObject({ value: '/home/me/elsewhere', unlisted: true, retired: '' })
    expect(brakemanAccount(doc, 'claude', '/home/me/elsewhere').items.at(-1)).toEqual({ value: '/home/me/elsewhere', label: '/home/me/elsewhere (a folder not in Accounts)' })
    expect(brakemanAccount(doc, 'claude', '/home/me/.claude-old')).toMatchObject({ value: '/home/me/.claude-old', unlisted: false, retired: 'Old' })
    expect(brakemanAccount(doc, 'claude', '/home/me/.claude-old').items.at(-1)).toEqual({ value: '/home/me/.claude-old', label: 'Old (retired)' })
    expect(brakemanAccount(doc, 'claude', '/home/me/.claude-work')).toMatchObject({ value: '/home/me/.claude-work', unlisted: false })
    // Another tool's account's folder is not this tool's.
    expect(brakemanAccount(doc, 'claude', '/home/me/.codex-team').unlisted).toBe(true)
  })

  it("matches a folder written another way to its account, as the server cleans it: a trailing or doubled separator, a '.' segment", () => {
    for (const spelt of ['/home/me/.claude-work/', '/home/me//.claude-work', '/home/me/./.claude-work', '/home/me/x/../.claude-work']) {
      expect(brakemanAccount(doc, 'claude', spelt), spelt).toMatchObject({ value: '/home/me/.claude-work', unlisted: false, retired: '' })
      expect(brakemanAccount(doc, 'claude', spelt).items, spelt).toHaveLength(2)
    }
    expect(brakemanAccount(doc, 'claude', '/home/me/.claude-old/').retired).toBe('Old')
    expect(cleanFolder('/a//b/./c/')).toBe('/a/b/c')
    expect(cleanFolder('/')).toBe('/')
  })

  it('has no choice without the accounts or for a tool they do not list: the folder is typed then', () => {
    expect(brakemanAccount(null, 'claude', '').items).toEqual([])
    expect(brakemanAccount(doc, undefined, '').items).toEqual([])
    expect(brakemanAccount(null, 'claude', '/home/me/x').unlisted).toBe(false)
  })
})
