import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { AT_LAUNCH, OWN_LOGIN, accountName, boundWords, brakemanAccountItems, launchLines, memberAccountItems, memberAccountWords, namedAccounts, unlistedFolder } from '../utils/crewLaunch'
import { capacityKey } from '../utils/capacity'
import { toCrewInput, toCrewMember } from '../utils/crews'

// The crew launch sheet's lines and the crew's members on their accounts (docs/plans/capacity.md, PR-7). The app's vitest runs in node
// with no component mounted, so the sheet's lines are tested through the function it draws them from (launchLines), and the sheet's
// source is read for what it asks of the server: its compact panels read as it opens, and nothing else does.

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
  it('draws one line per member: its agent, its model and its account, the default said with what it is now', () => {
    const lines = launchLines(
      [
        { name: 'builder', agentId: 'claude' },
        { name: 'reviewer', agentId: 'codex', account: 'a-cccccccccccc', model: 'gpt-e2e' },
        { name: 'shell', agentId: 'plain' },
      ],
      { agents, doc, managed: true, yolo: false },
    )
    expect(lines.map((l) => [l.name, l.agent, l.model, l.account])).toEqual([
      ['builder', 'Claude Code', "the agent's default", 'the default at launch (now Work)'],
      ['reviewer', 'Codex CLI', 'gpt-e2e', 'Team'],
      ['shell', 'Plain shell', "the agent's default", 'the default at launch'],
    ])
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

  it('reads as it opens through its compact panels alone, and launches only on its Launch', () => {
    const src = readFileSync(new URL('./CrewLaunchSheet.vue', import.meta.url), 'utf8')
    expect(src).toContain('<CapacityPanel :query="open ? l.query : null" compact read-on-open />')
    expect(src).not.toContain('/api/capacity/read')
    expect(src).toContain('@click="emit(\'launch\')"')
    // The ticket's launch dialog draws the same lines and asks no read as it opens.
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
    expect(memberAccountWords(doc, 'claude', undefined)).toBe('the default at launch (now Work)')
    expect(memberAccountWords(null, 'claude', undefined)).toBe('the default at launch')
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

describe("Brakeman's Account choice", () => {
  it('offers the default login, saved as no folder, and each account of the tool not retired by its folder', () => {
    expect(brakemanAccountItems(doc, 'claude', '')).toEqual([
      { value: OWN_LOGIN, label: 'Default login' },
      { value: '/home/me/.claude-work', label: 'Work' },
    ])
  })

  it('shows a folder no account holds as a folder not in Accounts, and a retired account by its label', () => {
    expect(brakemanAccountItems(doc, 'claude', '/home/me/elsewhere').at(-1)).toEqual({ value: '/home/me/elsewhere', label: '/home/me/elsewhere (a folder not in Accounts)' })
    expect(unlistedFolder(doc, 'claude', '/home/me/elsewhere')).toBe(true)
    expect(brakemanAccountItems(doc, 'claude', '/home/me/.claude-old').at(-1)).toEqual({ value: '/home/me/.claude-old', label: 'Old (retired)' })
    expect(unlistedFolder(doc, 'claude', '/home/me/.claude-work')).toBe(false)
    // Another tool's account's folder is not this tool's.
    expect(unlistedFolder(doc, 'claude', '/home/me/.codex-team')).toBe(true)
    expect(unlistedFolder(doc, 'claude', '')).toBe(false)
  })

  it('has no choice without the accounts or for a tool they do not list: the folder is typed then', () => {
    expect(brakemanAccountItems(null, 'claude', '')).toEqual([])
    expect(brakemanAccountItems(doc, undefined, '')).toEqual([])
  })
})
