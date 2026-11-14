import { accountChoices, modelWords, type AccountChoice, type AccountsLike, type CapacityQuery } from './capacity'

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
 * select shows what is saved. [] for an adapter the accounts do not list: such a member has no account to choose.
 */
export function memberAccountItems(doc: AccountsDocLike | null | undefined, adapter: string | undefined, current = ''): AccountChoice[] {
  const choices = accountChoices(doc, adapter)
  if (!choices.length) return []
  const now = defaultNow(doc, adapter)
  const items = [{ value: AT_LAUNCH, label: `The default at launch (now ${now})` }, ...choices]
  if (current && !items.some((i) => i.value === current)) items.push({ value: current, label: accountName(doc, current) })
  return items
}

/** A member's account as the crew names it, in words: the account, or for none the default at launch with what that is now. */
export function memberAccountWords(doc: AccountsDocLike | null | undefined, adapter: string | undefined, account: string | undefined): string {
  if (account) return accountName(doc, account)
  const now = defaultNow(doc, adapter)
  return now ? `the default at launch (now ${now})` : 'the default at launch'
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
 * none), its account (the default at launch, said with what it is now, for none) and the capacity view its launch would get, asked
 * with the account it names (none: the route's default, which the view resolves), its model, the run's kind and its yolo choice.
 */
export function launchLines(members: readonly LaunchMember[], o: { agents: ReadonlyArray<{ id: string; name: string; adapter?: string }>; doc: AccountsDocLike | null | undefined; managed: boolean; yolo: boolean }): LaunchLine[] {
  return members.map((m) => {
    const agent = o.agents.find((a) => a.id === m.agentId)
    const model = m.model?.trim() ?? ''
    const query: CapacityQuery = { agent: m.agentId, launch: o.managed ? 'managed' : 'legacy', yolo: o.yolo }
    if (m.account) query.account = m.account
    if (model) query.model = model
    return { name: m.name, agent: agent?.name ?? m.agentId, model: modelWords({ chosen: model }), account: memberAccountWords(o.doc, agent?.adapter, m.account), query }
  })
}

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
 * `brakeman` for a Brakeman turn's run.
 */
export function boundWords(m: { account?: { id: string; source: string }; model?: string }, doc: AccountsDocLike | null | undefined, named?: string, brakeman = false): { account: string; model: string; atLaunch: boolean } {
  const a = m.account
  // A Brakeman turn's member names no account in its run: its login is the folder Brakeman's settings name (its session's launch).
  const none = brakeman ? "the login Brakeman's settings name" : SOURCE_WORDS.own!
  const account = !a ? none : a.source === 'account' ? accountName(doc, a.id) : (SOURCE_WORDS[a.source] ?? a.source)
  return { account, model: modelWords({ chosen: m.model ?? '' }), atLaunch: named === '' }
}

/** The member names the revision's crew gives an account, by member: `''` for one that names none. */
export function namedAccounts(members: ReadonlyArray<{ name: string; account?: string }> | undefined): Record<string, string> {
  return Object.fromEntries((members ?? []).map((m) => [m.name, m.account ?? '']))
}

/** Brakeman's Account choice's value for the agent's own login: a select item may not be "", so it has one of its own, saved as none. */
export const OWN_LOGIN = '@own'

/**
 * Brakeman's Account choice (docs/plans/capacity.md, PR-7): its configuration keeps a folder, so each item's value is one: the agent's
 * own login (saved as no folder), then each account of its adapter not retired, by its label. A folder saved that no listed account
 * holds is shown as it is, "a folder not in Accounts" (the server binds it `unlisted`), and a retired account's folder by its label,
 * retired. [] for an adapter the accounts do not list.
 */
export function brakemanAccountItems(doc: AccountsDocLike | null | undefined, adapter: string | undefined, folder: string): AccountChoice[] {
  const ad = doc?.adapters.find((a) => a.adapter === adapter)
  if (!doc || !ad) return []
  const mine = doc.accounts.filter((a) => a.adapter === adapter && a.folder)
  const items: AccountChoice[] = [{ value: OWN_LOGIN, label: 'Default login' }, ...mine.filter((a) => !a.retired).map((a) => ({ value: a.folder!, label: a.label }))]
  if (folder && !items.some((i) => i.value === folder)) {
    const retired = mine.find((a) => a.folder === folder)
    items.push({ value: folder, label: retired ? `${retired.label} (retired)` : `${folder} (a folder not in Accounts)` })
  }
  return items
}

/** Whether a folder Brakeman's configuration names is one no listed account of its adapter holds (PR-4's `unlisted`). */
export function unlistedFolder(doc: AccountsDocLike | null | undefined, adapter: string | undefined, folder: string): boolean {
  return !!doc && !!folder && !doc.accounts.some((a) => a.adapter === adapter && a.folder === folder)
}
