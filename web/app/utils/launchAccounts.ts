import { accountName, type AccountsDocLike } from './crewLaunch'
import { accountWords, launchLogin, type CapacityView } from './capacity'

/**
 * A launch's sessions per account (CP1 PR-9, docs/plans/compare-crew.md; beside AC1's member lines, docs/plans/capacity.md PR-7): how
 * many sessions one launch starts first on each login, and, for a comparison, up to how many its reviews start after its candidates.
 * Nothing here picks an account: a member names one, or the server's view of its launch resolves it, or, before that view has answered,
 * it is the default at the launch, unnamed. AC1's own figures (each line's usage) are drawn as they were; this only counts.
 */

/** The sessions one launch starts on one login: `first` at once (once the work may start), `later` at most after them. */
export interface AccountCount {
  key: string
  words: string
  first: number
  later: number
}

/** A member as the count reads it. */
export interface CountedMember {
  name: string
  agentId: string
  account?: string
  start?: { when: string }
}

/**
 * The login a member's sessions run on, as a key and in words: the account it names (by its id; its label in words), else what the
 * server's view of its launch resolved (the account, the login its entry sets, or the one its launch sets), else, before the view, the
 * default at the launch, named by nothing. A tool's own default login is its tool's: two tools' defaults are two logins, never one.
 */
export function memberLogin(m: CountedMember, o: { doc: AccountsDocLike | null | undefined; view: Pick<CapacityView, 'state' | 'host' | 'account' | 'entryLogin' | 'agent'> | null | undefined; agent: string }): { key: string; words: string } {
  const tool = o.view?.agent.adapter || m.agentId
  if (m.account && m.account !== 'default') return { key: `account:${m.account}`, words: accountName(o.doc, m.account) }
  if (m.account === 'default') return { key: `default:${tool}`, words: `${o.agent}'s default login` }
  const v = o.view
  if (!v) return { key: `launch:${m.agentId}`, words: `the default at the launch of ${o.agent}` }
  const login = launchLogin(v)
  if (login.kind === 'entry') return { key: `entry:${m.agentId}:${login.variable}`, words: `the login ${o.agent}'s entry sets (${login.variable})` }
  if (login.kind === 'own') return { key: `own:${m.agentId}`, words: `the login ${o.agent}'s launch sets` }
  return v.account.id === 'default' ? { key: `default:${tool}`, words: `${o.agent}'s default login` } : { key: `account:${v.account.id}`, words: accountWords(v.account) }
}

/**
 * Sessions one launch starts on each login, in the order the members first name them. First: for a comparison, each candidate's task's
 * member; for a crew with tasks, each task that needs none; for a crew without tasks, each member that starts at once (the others start
 * after another, on a rule, maybe never). Later: a comparison's reviews, each a session only after its candidate succeeds.
 */
export function launchAccounts(o: {
  members: readonly CountedMember[]
  tasks?: ReadonlyArray<{ id: string; member: string; kind: 'work' | 'review'; needs?: string[] }>
  compare?: { candidates: ReadonlyArray<{ task: string }> } | null
  accountOf: (member: string) => { key: string; words: string }
}): AccountCount[] {
  const out: AccountCount[] = []
  const add = (member: string, first: number, later: number) => {
    const a = o.accountOf(member)
    let row = out.find((r) => r.key === a.key)
    if (!row) out.push((row = { key: a.key, words: a.words, first: 0, later: 0 }))
    row.first += first
    row.later += later
  }
  const tasks = o.tasks
  if (o.compare && tasks) {
    const lanes = o.compare.candidates.map((c) => c.task)
    for (const t of lanes) {
      const task = tasks.find((x) => x.id === t)
      if (task) add(task.member, 1, 0)
    }
    for (const t of tasks) if (t.kind === 'review' && t.needs?.some((n) => lanes.includes(n))) add(t.member, 0, 1)
  } else if (tasks) {
    for (const t of tasks) if (!t.needs?.length) add(t.member, 1, 0)
  } else {
    for (const m of o.members) if ((m.start?.when ?? 'immediately') === 'immediately') add(m.name, 1, 0)
  }
  return out
}

const sessions = (n: number) => `${n} ${n === 1 ? 'session' : 'sessions'}`

/** A login's line: "this launch: 3 sessions on Work, then up to 3 more"; a login only reviews use, "up to 2 sessions on Work, after the candidates". */
export function accountCountWords(c: AccountCount): string {
  if (!c.first) return `this launch: up to ${sessions(c.later)} on ${c.words}, after the candidates`
  return `this launch: ${sessions(c.first)} on ${c.words}${c.later ? `, then up to ${c.later} more` : ''}`
}
