import type { DraftCrew } from './crews'
import { memberNameError } from './crews'
import { accountChoices, accountWords, launchLogin, modelProblem, modelWords, type AccountChoice, type AccountsLike, type CapacityQuery, type CapacityView } from './capacity'

/**
 * A crew's members on their accounts and models (docs/plans/capacity.md, PR-7): the crew editor's Account choice, the lines the launch
 * sheet and a ticket's launch draw for each member, and the login a run's member was bound to, as the run page names it. Every word
 * here is what the server said or the person chose: nothing picks an account, and a member that names none is the route's to resolve
 * at the launch (its adapter's default then).
 */

/** The Account choice's value for "the default at launch": a select item may not be "", so it has one of its own, saved as no account. */
export const AT_LAUNCH = '@launch'

/** The accounts as the choices need them: the document's ids, labels and retired marks, and each adapter's default. */
export interface AccountsDocLike extends AccountsLike {
  accounts: Array<{ id: string; adapter: string; label: string; folder?: string; retired?: boolean }>
}

/** An account by its id, in words: "Default login", its label (", retired" when it is), or the id with "not in Accounts". */
export function accountName(doc: AccountsDocLike | null | undefined, id: string): string {
  if (id === 'default') return 'Default login'
  const a = doc?.accounts.find((x) => x.id === id)
  if (!a) return doc ? `${id} (not in Accounts)` : id
  return a.retired ? `${a.label} (retired)` : a.label
}

/** What a member that names no account launches on now: its adapter's default, by name; "" when the accounts are not read. */
function defaultNow(doc: AccountsDocLike | null | undefined, adapter: string | undefined): string {
  const ad = doc?.adapters.find((a) => a.adapter === adapter)
  return ad ? accountName(doc, ad.default) : ''
}

/**
 * The crew editor's Account choice for a member of `adapter`: the default at launch first (what a member that names none takes), then
 * the choices a session's launch offers, and the account the member names when it is none of them (retired, or not in Accounts), so the
 * select shows what is saved. [] for an adapter the accounts do not list: such a member has no account to choose. An agent whose entry
 * sets its login (`entryLogin`, the server's view of it) launches on that login whatever is chosen, and the server takes no other
 * account for it: the entry's login alone is offered.
 */
export function memberAccountItems(doc: AccountsDocLike | null | undefined, adapter: string | undefined, current = '', entryLogin = ''): AccountChoice[] {
  const choices = accountChoices(doc, adapter)
  if (!choices.length) return []
  const items = entryLogin ? [{ value: AT_LAUNCH, label: `The login its entry sets (${entryLogin})` }] : [{ value: AT_LAUNCH, label: `The default at launch (now ${defaultNow(doc, adapter)})` }, ...choices]
  if (current && !items.some((i) => i.value === current)) items.push({ value: current, label: accountName(doc, current) })
  return items
}

/** Why the server refuses the account a member names for an agent whose entry sets its login (bindLogin's words); '' when it takes it. */
export function memberAccountProblem(account: string | undefined, entryLogin: string): string {
  return entryLogin && account && account !== 'default' ? `This agent's entry sets ${entryLogin}: it uses that login, its default account only` : ''
}

/** A member moved to another agent: an account and a model are their tool's, so another tool's agent names neither (its own defaults). */
export function withAgent<M extends { agentId: string; account?: string; model?: string }>(m: M, agentId: string, adapterOf: (id: string) => string | undefined): M {
  return adapterOf(m.agentId) === adapterOf(agentId) ? { ...m, agentId } : { ...m, agentId, account: undefined, model: undefined }
}

/** Why the run's Add member dialog holds Add, in words, '' when it may add: an agent, a name no member or person of the run has, a model the server takes. */
export function addMemberProblem(m: { name: string; agentId: string; model?: string } | undefined, members: readonly string[], people: readonly string[]): string {
  if (!m?.agentId) return 'Pick an agent'
  return memberNameError(m.name, members, people) || modelProblem(m.model ?? '')
}

/**
 * A member's account before a launch, in words (the crew's sheet, a ticket's launch): the account it names; for none, the login the
 * server's view of its launch resolved (`view`): the login its entry sets, the one its launch sets, or the default at the launch with what
 * that is now. Before the view has answered, the default at the launch with no name: never a default the page works out itself.
 */
export function launchAccountWords(doc: AccountsDocLike | null | undefined, account: string | undefined, view: Pick<CapacityView, 'state' | 'host' | 'account' | 'entryLogin'> | null | undefined): string {
  if (account) return accountName(doc, account)
  if (!view) return 'the default at the launch'
  const login = launchLogin(view)
  if (login.kind === 'entry') return `the login its entry sets (${login.variable})`
  return login.kind === 'own' ? 'the login its launch sets' : `the default at the launch (now ${accountWords(view.account)})`
}

/** A member of a crew as a launch line needs it. */
export interface LaunchMember {
  name: string
  agentId: string
  account?: string
  model?: string
}

/** One member's line before a launch: its name, its agent, its model and its account in words, and the question its capacity line asks. */
export interface LaunchLine {
  name: string
  agent: string
  model: string
  account: string
  query: CapacityQuery
}

/**
 * Each member's line before a launch (the crew sheet's, a ticket's launch): its agent by name, its model ("the agent's default" for
 * none), its account (launchAccountWords, from `view`, the server's answer to the line's question) and the capacity view its launch would
 * get, asked with the account it names (none: the route's default, which the view resolves), its model, the run's kind and its yolo choice.
 */
export function launchLines(members: readonly LaunchMember[], o: { agents: ReadonlyArray<{ id: string; name: string }>; doc: AccountsDocLike | null | undefined; managed: boolean; yolo: boolean; view?: (q: CapacityQuery) => CapacityView | null }): LaunchLine[] {
  return members.map((m) => {
    const agent = o.agents.find((a) => a.id === m.agentId)
    const model = m.model?.trim() ?? ''
    const query: CapacityQuery = { agent: m.agentId, launch: o.managed ? 'managed' : 'legacy', yolo: o.yolo }
    if (m.account) query.account = m.account
    if (model) query.model = model
    return { name: m.name, agent: agent?.name ?? m.agentId, model: modelWords({ chosen: model }), account: launchAccountWords(o.doc, m.account, o.view?.(query)), query }
  })
}

/**
 * What a crew's launch sheet was opened for, which its Launch sends: a saved crew by its id, or the crew page's draft itself (`key` its
 * place among the drafts), saved as it launches; with the run's name when one was typed.
 */
export type SheetTarget = { label: string } & ({ id: string } | { key: string; draft: DraftCrew })

/** Where a run member's login comes from (MemberAccount.source), in words, for a source that is not a registered account. */
const SOURCE_WORDS: Record<string, string> = {
  own: "the agent's own login",
  pinned: 'the login its entry sets',
  launch_env: 'the login its launch sets',
  unlisted: 'a folder not in Accounts',
}

/**
 * The login and model a run's member was bound to, in words: a registered account by its label (the admin's accounts name it; its id
 * otherwise), any other source by where the login comes from, none for the agent's own login. `named` is the account the ticket's
 * revision named for the member, when the run was launched from one: "" (named none) says the launch took the default at that launch.
 * `brakeman` for a Brakeman turn's run, whose model is in the turn's arguments, which the run does not record: it is said as not shown.
 */
export function boundWords(m: { account?: { id: string; source: string }; model?: string }, doc: AccountsDocLike | null | undefined, named?: string, brakeman = false): { account: string; model: string; atLaunch: boolean } {
  const a = m.account
  // A Brakeman turn's member names no account in its run: its login is the folder Brakeman's settings name (its session's launch).
  const none = brakeman ? "the login Brakeman's settings name" : SOURCE_WORDS.own!
  const account = !a ? none : a.source === 'account' ? accountName(doc, a.id) : (SOURCE_WORDS[a.source] ?? a.source)
  const model = !m.model && brakeman ? "its model is not shown: a Brakeman turn's run does not record it" : modelWords({ chosen: m.model ?? '' })
  return { account, model, atLaunch: named === '' }
}

/** The member names the revision's crew gives an account, by member: `''` for one that names none. */
export function namedAccounts(members: ReadonlyArray<{ name: string; account?: string }> | undefined): Record<string, string> {
  return Object.fromEntries((members ?? []).map((m) => [m.name, m.account ?? '']))
}

/** Brakeman's Account choice's value for the agent's own login: a select item may not be "", so it has one of its own, saved as none. */
export const OWN_LOGIN = '@own'

/**
 * A folder as the server compares two (filepath.Clean): no doubled or trailing separator, no `.` segment, a `..` taken back. The server
 * also follows links; a page cannot, so a folder reached through a link is matched here only by its own spelling.
 */
export function cleanFolder(p: string): string {
  const out: string[] = []
  for (const s of p.split('/')) {
    if (s === '..') out.pop()
    else if (s && s !== '.') out.push(s)
  }
  return `${p.startsWith('/') ? '/' : ''}${out.join('/')}`
}

/**
 * Brakeman's Account choice (docs/plans/capacity.md, PR-7): its configuration keeps a folder, so each item's value is one: the agent's
 * own login (saved as no folder), then each account of its adapter not retired, by its label. The saved folder is matched to an account
 * as the server matches it, as far as a page can (cleanFolder). One no listed account holds is shown as it is, "a folder not in Accounts"
 * (the server binds it `unlisted`), and a retired account's by its label, retired, with `retired` its label: the server refuses a turn on
 * it. No items for an adapter the accounts do not list.
 */
export function brakemanAccount(doc: AccountsDocLike | null | undefined, adapter: string | undefined, folder: string): { items: AccountChoice[]; value: string; unlisted: boolean; retired: string } {
  const ad = doc?.adapters.find((a) => a.adapter === adapter)
  const mine = (doc?.accounts ?? []).filter((a) => a.adapter === adapter && a.folder)
  const held = folder ? mine.find((a) => cleanFolder(a.folder!) === cleanFolder(folder)) : undefined
  const value = held?.folder ?? (folder || OWN_LOGIN)
  const items: AccountChoice[] = !ad ? [] : [{ value: OWN_LOGIN, label: 'Default login' }, ...mine.filter((a) => !a.retired).map((a) => ({ value: a.folder!, label: a.label }))]
  if (ad && folder && !items.some((i) => i.value === value)) items.push({ value, label: held ? `${held.label} (retired)` : `${folder} (a folder not in Accounts)` })
  return { items, value, unlisted: !!doc && !!folder && !held, retired: held?.retired ? held.label : '' }
}
