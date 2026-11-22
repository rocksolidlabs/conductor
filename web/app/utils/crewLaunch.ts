import type { DraftCrew } from './crews'
import { memberNameError } from './crews'
import { CREW_CHANGED } from './protocol'
import { accountChoices, accountWords, launchLogin, limited, modelProblem, modelWords, realAfter, warningOf, withDeadline, type After, type AccountChoice, type AccountsLike, type CapacityQuery, type CapacityView } from './capacity'

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

/**
 * The crew editor's Account choice for a member of `adapter`, from the server's view of its launch with no account named (`view`, asked
 * as the launch will be: with the crew's yolo): "Default account" first (what a member that names none takes at the launch, named as the
 * view resolves it now), then the choices a session's launch offers, and the account the member names when it is none of them (retired, or not
 * in Accounts), so the select shows what is saved. A launch whose login its entry or its launch sets takes that login whatever is
 * chosen: that login alone is offered. Before the view has answered, or when it failed, the default at launch alone, named by nothing
 * the page worked out. [] for an adapter the accounts do not list: such a member has no account to choose.
 */
export function memberAccountItems(doc: AccountsDocLike | null | undefined, adapter: string | undefined, current = '', view: Pick<CapacityView, 'state' | 'host' | 'account' | 'entryLogin'> | null = null): AccountChoice[] {
  const choices = accountChoices(doc, adapter)
  if (!choices.length) return []
  const login = view ? launchLogin(view) : null
  const items: AccountChoice[] = !login
    ? [{ value: AT_LAUNCH, label: 'Default account' }]
    : login.kind === 'entry'
      ? [{ value: AT_LAUNCH, label: `The login its entry sets (${login.variable})` }]
      : login.kind === 'own'
        ? [{ value: AT_LAUNCH, label: 'The login its launch sets' }]
        : [{ value: AT_LAUNCH, label: `Default account (now ${accountWords(view!.account)})` }, ...choices]
  if (current && !items.some((i) => i.value === current)) items.push({ value: current, label: accountName(doc, current) })
  return items
}

/** The server's refusal of an account named beside a login the entry sets (bindLogin), the page's copy: a spec compares it with the route's. */
export const ENTRY_REFUSAL = (variable: string) => `this agent's entry sets ${variable}: it uses that login, its default account only`
/** The page's words for an account named beside a login the launch sets, whose variable the server's view does not name. */
export const LAUNCH_REFUSAL = 'this launch sets its login: it uses that login, its default account only'

/**
 * Why the launch refuses the account a member names (`view`, the view of its launch with no account named): beside a login its entry
 * sets, or one its launch sets (its yolo recipe), the server takes its default account only; '' when it takes it, or before the view.
 */
export function memberAccountProblem(account: string | undefined, view: Pick<CapacityView, 'state' | 'host' | 'entryLogin'> | null): string {
  if (!account || account === 'default' || !view) return ''
  const login = launchLogin(view)
  if (login.kind === 'entry') return ENTRY_REFUSAL(login.variable)
  return login.kind === 'own' && view.state === 'env_set' ? LAUNCH_REFUSAL : ''
}

/**
 * A member moved to another agent: an account and a model are their tool's, so another tool's agent names neither (its own defaults),
 * and neither does an agent of no tool Conductor knows (two such agents are not one tool).
 */
export function withAgent<M extends { agentId: string; account?: string; model?: string }>(m: M, agentId: string, adapterOf: (id: string) => string | undefined): M {
  const tool = adapterOf(agentId)
  return tool && adapterOf(m.agentId) === tool ? { ...m, agentId } : { ...m, agentId, account: undefined, model: undefined }
}

/**
 * Why the run's Add member dialog holds Add, in words, '' when it may add: an agent, a name no member or person of the run has, a model
 * the server takes, and no account the field marks refused (`accountProblem`, the field's own check).
 */
export function addMemberProblem(m: { name: string; agentId: string; model?: string } | undefined, members: readonly string[], people: readonly string[], accountProblem = ''): string {
  if (!m?.agentId) return 'Pick an agent'
  return memberNameError(m.name, members, people) || modelProblem(m.model ?? '') || accountProblem
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
 * Each member's line before a launch (the crew sheet's, a ticket's launch): its agent by name, its model (modelWords: for none, the
 * one sentence the run page says too, since its arguments may choose one), its account (launchAccountWords, from `view`, the server's answer to the line's question) and the capacity view its launch would
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

/** A sheet's reads end: each is aborted and refused in words at its deadline, a bound on a hang. */
export const SHEET_DEADLINE_MS = 30_000

/** What a launch sheet reads before its Launch: the saved crew, and the server's yolo default. */
export interface SheetReads<C> {
  crew: (id: string, signal: AbortSignal) => Promise<C>
  yoloDefault: (signal: AbortSignal) => Promise<boolean>
}

/** What a sheet read: the crew it draws and the yolo its launch uses; or why a read failed (`what` it read), `gone` for a crew not there. */
export type SheetRead<C> = { crew: C; yolo: boolean } | { error: string; what: 'crew' | 'default'; gone: boolean }

/**
 * Reads what a launch sheet draws, for what it was opened for: the saved crew as the server holds it (a target by id; a draft is drawn
 * as it is), then, for a crew that inherits its yolo setting, the server's default, read and never assumed. Each read is refused in
 * words at its deadline (`ms`, `after`) and ended when `signal` aborts (the sheet closed, or opened for another target): null then,
 * whatever came, so nothing of it is drawn.
 */
export async function readSheet<C extends { yolo?: boolean | null }>(t: { id: string } | { draft: C }, reads: SheetReads<C>, signal: AbortSignal, ms = SHEET_DEADLINE_MS, after: After = realAfter): Promise<SheetRead<C> | null> {
  const ask = <T>(f: (s: AbortSignal) => Promise<T>) => withDeadline((s) => f(AbortSignal.any([s, signal])), ms, `The server did not answer within ${ms / 1000} s`, after)
  let what: 'crew' | 'default' = 'crew'
  try {
    const crew = 'id' in t ? await ask((s) => reads.crew(t.id, s)) : t.draft
    what = 'default'
    const yolo = typeof crew.yolo === 'boolean' ? crew.yolo : await ask((s) => reads.yoloDefault(s))
    return signal.aborted ? null : { crew, yolo }
  } catch (e) {
    return signal.aborted ? null : { error: (e as Error).message, what, gone: what === 'crew' && (e as { status?: unknown }).status === 404 }
  }
}

/**
 * A sheet's Launch, one run for one confirmation: each opening of the sheet is one confirmation, taken before anything is awaited. A
 * second take of it, a take while a launch is under way, and one from a sheet that is closed or closing are refused.
 */
export class SheetConfirmation {
  private opening = 0
  private taken = 0
  private busy = false
  /** The sheet opened: a new confirmation. */
  opened() {
    this.opening++
  }
  /** Takes the opening's confirmation for a launch: the opening's number, 0 when it may not start one. */
  take(open: boolean): number {
    if (!open || this.busy || this.taken === this.opening) return 0
    this.taken = this.opening
    this.busy = true
    return this.opening
  }
  /** Whether opening `n` (a take's) is still the sheet's: it was not opened again since. */
  holds(n: number): boolean {
    return n === this.opening
  }
  /** The launch it started has ended. */
  done() {
    this.busy = false
  }
}

/** A launch the server refused, kept on the sheet in the server's own words (#1075): the route's code and its message as written. */
export interface ServerRefusal {
  code: string
  words: string
}

/**
 * Why a sheet's Launch started nothing (#957): `changed`, the server refused it `crew_changed` (the crew was saved since the copy the
 * launch named); `unrevised`, the crew came without a revision (a server that does not give one), so the launch was not sent; a
 * ServerRefusal, any other refusal of the launch, which the sheet keeps with the server's words (launchRefusal).
 */
export type SheetRefusal = 'changed' | 'unrevised' | ServerRefusal

/**
 * Where a launch's failure is said (#1075), by the code POST /api/crews/{id}/launch answered (docs/protocol.md): `crew_changed` is
 * `changed` (the sheet reads the crew again); `not_found` is `gone` (the crew is no longer saved: nothing on the sheet can be launched,
 * so the sheet closes and the page says so, as when the sheet's own read finds it gone). Every other refusal stays on the sheet, in the
 * server's words as given, with what the sheet was opened with kept: a person can act on each or must read it to understand why nothing
 * started (`managed_off`: crews with tasks are off on this server; `ledger_unavailable`: another server holds the ledger, or it could
 * not be opened; `invalid_crew`, `invalid_cwd`, `not_a_repo`, `invalid_account`: the crew, its folder or a member's account;
 * `crew_unreadable`, `store_unavailable`, `store_failed`; `launch_failed`, git missing among them; `run_stopped`; `switchyard`;
 * `invalid_request`, the run's name; a member's session refused as `POST /api/sessions` refuses one), and so does a launch that
 * reached no server (`network`), which a toast would take away after a few seconds.
 */
export function launchRefusal(e: unknown): SheetRefusal | 'gone' {
  const code = typeof e === 'object' && e !== null && 'code' in e && typeof e.code === 'string' ? e.code : ''
  if (code === CREW_CHANGED) return 'changed'
  if (code === 'not_found') return 'gone'
  return { code: code || 'error', words: e instanceof Error ? e.message : String(e) }
}

/**
 * The sheet's target once its Launch started nothing (`why`). While the launch's opening is still the sheet's (`current`), the sheet
 * stays open as a new opening of `confirmation`: the launch's end does not close it, and its Launch takes a confirmation again. For
 * `changed` that is a new target, the saved crew by its id with the run's name kept, so the sheet reads it again (even when it was opened
 * by that id) and its Launch names the revision of what it then shows; for `unrevised` the same target, its Launch held. null when the
 * opening is no longer the sheet's (closed or opened again, the page or the token changed): the page then only says so. The server's
 * refusal alone decides which copy is current.
 */
export function refusedTarget(t: SheetTarget, id: string, why: SheetRefusal, current: boolean, confirmation: SheetConfirmation): SheetTarget | null {
  if (!current) return null
  confirmation.opened()
  return why === 'changed' ? { id, label: t.label } : t
}

/** Where the page stands as a launch that started nothing ends: its opening still confirmed, the page still mounted, the sheet open, on the crew it was opened at. */
export interface RefusalPlace { confirmed: boolean; mounted: boolean; open: boolean; at?: string; selected?: string }

/**
 * What the page does once a launch of crew `c` started nothing (`why`): while the sheet of its opening is on the screen (`p`), the sheet's
 * new target and why (refusedTarget); otherwise, the page left meanwhile too (a sheet then drawn nowhere, whatever it holds), the toast's
 * words, which name the crew: a refusal is never lost.
 */
export function afterRefusal(t: SheetTarget, c: { id: string; name: string }, why: SheetRefusal, p: RefusalPlace, confirmation: SheetConfirmation): { target: SheetTarget; refused: SheetRefusal } | { toast: string } {
  const target = refusedTarget(t, c.id, why, p.confirmed && p.mounted && p.open && p.at === p.selected, confirmation)
  if (target) return { target, refused: why }
  if (typeof why === 'object') return { toast: `${c.name} was refused by the server, so no run was started: ${why.words}` }
  return { toast: `${c.name} ${why === 'changed' ? 'was saved since the launch sheet read it' : 'came without a revision from this server'}: no run was started.` }
}

/** The sheet's words for a crew that came without a revision: Launch is held. */
export const UNREVISED_WORDS = 'This server does not say which copy of the crew it holds, so this sheet cannot tell which copy Launch would start: nothing is launched. Load the page again.'

/** The sheet's words before the server's own, for a launch it refused (a ServerRefusal). */
export const REFUSED_WORDS = 'The server refused the launch, so nothing was started:'

/**
 * The sheet's notice and whether it holds Launch: after a server's refusal (a ServerRefusal), its words as the server gave them, Launch
 * not held (the person may set right what it names and launch again); for a crew drawn without a revision (`unrevised`, a saved crew read
 * so) or a launch not sent for want of one, UNREVISED_WORDS, held; after a `changed` refusal, what the sheet is doing about it, in the
 * state of its read again (`read`): being read, failed (the read's own error is beside it, and Launch has nothing to start), or drawn.
 * null otherwise.
 */
export function sheetNotice(refused: SheetRefusal | null | undefined, read: 'reading' | 'failed' | 'drawn', unrevised: boolean): { kind: 'changed' | 'unrevised' | 'refused'; words: string; hold: boolean; code?: string } | null {
  if (typeof refused === 'object' && refused) return { kind: 'refused', words: `${REFUSED_WORDS} ${refused.words}`, hold: false, code: refused.code }
  if (refused === 'unrevised' || (read === 'drawn' && unrevised)) return { kind: 'unrevised', words: UNREVISED_WORDS, hold: true }
  if (refused !== 'changed') return null
  const first = 'The crew was saved since this sheet read it, so nothing was launched.'
  if (read === 'reading') return { kind: 'changed', words: `${first} It is being read again.`, hold: true }
  if (read === 'failed') return { kind: 'changed', words: first, hold: true }
  return { kind: 'changed', words: `${first} It is shown below as the server holds it now: Launch starts the crew as shown here, and what this sheet says is decided at the launch is decided then.`, hold: false }
}

/**
 * The run's permission prompts on the launch sheet, in one plain line: asked or skipped, and whether that is the crew's own setting (the
 * launch's revision covers it) or, for a crew that inherits it (`inherited`), the server's default at the launch, with what it is now
 * (`yolo`).
 */
export function launchYoloWords(yolo: boolean, inherited: boolean): string {
  const what = yolo ? 'skipped: the members act without asking' : 'the members ask before they act'
  return `Permission prompts: ${what} (${inherited ? "the server's default now" : "the crew's setting"})`
}

/** One member's row of the launch sheet's table: member · agent · model · account, in plain words, and the question its usage asks. */
export interface SheetRow {
  name: string
  agent: string
  model: string
  account: string
  query: CapacityQuery
}

/** A member that names no model, in the sheet's table: what the agent (or its arguments) choose, never called the agent's default. */
export const AGENT_CHOOSES = "The agent's choice"

/**
 * A member's account in the sheet's table, in plain words: the account it names; for none, what the server's view of its launch resolved
 * now ("Default login", "Work (the default)", the login its entry or its launch sets); before the view has answered, "Default account", named by nothing
 * the page worked out.
 */
export function sheetAccountWords(doc: AccountsDocLike | null | undefined, account: string | undefined, view: Pick<CapacityView, 'state' | 'host' | 'account' | 'entryLogin'> | null | undefined): string {
  if (account) return accountName(doc, account)
  if (!view) return 'Default account'
  const login = launchLogin(view)
  if (login.kind === 'entry') return `The login its entry sets (${login.variable})`
  if (login.kind === 'own') return 'The login its launch sets'
  // The tool's own login is said as it is; another account the default names is said as the default.
  return view.account.id === 'default' ? accountWords(view.account) : `${accountWords(view.account)} (the default)`
}

/** The launch sheet's table: a row a member, from the lines a launch asks (launchLines), its model and account said plainly. */
export function sheetRows(lines: readonly LaunchLine[], members: readonly LaunchMember[], doc: AccountsDocLike | null | undefined, view: (q: CapacityQuery) => CapacityView | null): SheetRow[] {
  return lines.map((l) => {
    const m = members.find((x) => x.name === l.name)
    return { name: l.name, agent: l.agent, model: l.query.model || AGENT_CHOOSES, account: sheetAccountWords(doc, m?.account, view(l.query)), query: l.query }
  })
}

/** A note the launch sheet shows above its table: only what needs the person before this launch, in its colour. */
export interface SheetNote {
  member: string
  words: string
  color: 'warning' | 'error'
}

/**
 * What of each member's usage needs the person before THIS launch, said with what it means for the launch: a limit the provider says is
 * reached (the member may stop at once), a window near its limit (it may stop before the reset), its agent signed out on the account (it
 * would start signed out) or not installed (it cannot start). Everything else the server says of a member's usage (a version not checked,
 * a read that failed, how fresh, each window) is in the sheet's Details. None of it holds Launch.
 */
export function sheetNotes(rows: readonly SheetRow[], view: (q: CapacityQuery) => CapacityView | null): SheetNote[] {
  const out: SheetNote[] = []
  for (const r of rows) {
    const v = view(r.query)
    if (!v) continue
    const on = accountWords(v.account)
    if (v.state === 'not_installed') out.push({ member: r.name, color: 'error', words: `${r.name} cannot start: ${r.agent} is not installed on this server.` })
    else if (v.state === 'signed_out') out.push({ member: r.name, color: 'error', words: `${r.name} would start signed out: ${r.agent} is not signed in on ${on}. Sign it in first, or it cannot work.` })
    else if (limited(v.limits)) out.push({ member: r.name, color: 'error', words: `${r.name} may stop at once: the provider says ${on} has reached a limit.` })
    else if (warningOf(v)) out.push({ member: r.name, color: warningOf(v)!.color, words: `${r.name} may stop before its usage resets: a window of ${on} is near its limit.` })
  }
  return out
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
 * `brakeman` for a Brakeman turn's run, whose model is in the turn's arguments, which the run does not record: it is said as not shown.
 * A model the run does not record is never said to be the agent's default (modelWords): a member's arguments may choose one, and the run
 * records neither them nor what they chose. A registered account while the accounts are not read (`doc` none) is `unread`, never its id.
 */
export function boundWords(m: { account?: { id: string; source: string }; model?: string }, doc: AccountsDocLike | null | undefined, named?: string, brakeman = false, unread = ACCOUNT_UNREAD): { account: string; model: string; atLaunch: boolean } {
  const a = m.account
  // A Brakeman turn's member names no account in its run: its login is the folder Brakeman's settings name (its session's launch).
  const none = brakeman ? "the login Brakeman's settings name" : SOURCE_WORDS.own!
  const account = !a ? none : a.source === 'account' ? (doc ? accountName(doc, a.id) : unread) : (SOURCE_WORDS[a.source] ?? a.source)
  const model = !m.model && brakeman ? "its model is not shown: a Brakeman turn's run does not record it" : modelWords({ chosen: m.model ?? '' })
  return { account, model, atLaunch: named === '' }
}

/** A run member's registered account while the accounts are being read, and once their read failed (`why`): no id passes for a name. */
export const ACCOUNT_UNREAD = 'a registered account, not read yet'
export const accountUnreadable = (why: string) => `a registered account that could not be read: ${why}`

/**
 * Reads the accounts a ticket's plan revision named for each member (namedAccounts), refused in words at the sheet's reads' deadline; a
 * revision made on another machine, which has no body here and never launches a run here, is refused in words too.
 */
export function readNamedAccounts(read: (signal: AbortSignal) => Promise<{ body?: { crew: { members?: ReadonlyArray<{ name: string; account?: string }> } } }>, after: After = realAfter): Promise<Record<string, string>> {
  return withDeadline(read, SHEET_DEADLINE_MS, `The server did not answer within ${SHEET_DEADLINE_MS / 1000} s`, after).then((p) => {
    if (!p.body) throw new Error('the revision was made on another machine, which keeps its crew')
    return namedAccounts(p.body.crew.members)
  })
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
 * as the server matches it, as far as a page can (cleanFolder). One no listed account holds by that path is shown as it is, "not in
 * Accounts by this path" (a page cannot follow a link: the server binds it `unlisted` unless a link leads to an account's folder), and a retired account's by its label, retired, with `retired` its label: the server refuses a turn on
 * it. No items for an adapter the accounts do not list.
 */
export function brakemanAccount(doc: AccountsDocLike | null | undefined, adapter: string | undefined, folder: string): { items: AccountChoice[]; value: string; unlisted: boolean; retired: string } {
  const ad = doc?.adapters.find((a) => a.adapter === adapter)
  const mine = (doc?.accounts ?? []).filter((a) => a.adapter === adapter && a.folder)
  const held = folder ? mine.find((a) => cleanFolder(a.folder!) === cleanFolder(folder)) : undefined
  const value = held?.folder ?? (folder || OWN_LOGIN)
  const items: AccountChoice[] = !ad ? [] : [{ value: OWN_LOGIN, label: 'Default login' }, ...mine.filter((a) => !a.retired).map((a) => ({ value: a.folder!, label: a.label }))]
  if (ad && folder && !items.some((i) => i.value === value)) items.push({ value, label: held ? `${held.label} (retired)` : `${folder} (not in Accounts by this path)` })
  return { items, value, unlisted: !!doc && !!folder && !held, retired: held?.retired ? held.label : '' }
}
