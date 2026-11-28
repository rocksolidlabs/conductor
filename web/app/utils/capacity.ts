/**
 * Capacity's words (docs/plans/capacity.md, Program item AC1, PR-5): what the server's capacity view says, as the app's screens say
 * it. The view is `GET /api/capacity`'s (docs/protocol.md, Capacity), the server's own judgement of an agent's usage on an account;
 * nothing here reads a login, works out a figure or adds Conductor's counts to the provider's. A window the provider did not report
 * is not in `windows` and is never drawn; the counts are an estimate, in their own row.
 */

/** One usage window the provider reported. */
export interface CapacityWindow {
  source: 'provider'
  kind: string
  name: string
  minutes?: number
  percent: number
  resetsAt?: string
  severity?: '' | 'normal' | 'warning' | 'critical'
  active?: boolean
  limit?: string
}

/** One of the provider's credit figures, as it wrote them. */
export interface CapacityCredit {
  source: 'provider'
  kind: string
  enabled?: boolean
  used?: string
  limit?: string
  balance?: string
  unlimited?: boolean
  currency?: string
}

/** The provider's own refusal flags; an absent one was not reported. */
export interface CapacityLimits {
  source: 'provider'
  reached?: string
  spendControl?: boolean
  ordinaryAllowed?: boolean
}

/** What this server counted itself: an estimate, never the provider's. */
export interface CapacityCounts {
  source: 'conductor'
  estimate: true
  running: number
  started5h: number
  started7d: number
  since?: string
}

export type CapacityState =
  | 'ok'
  | 'reading'
  | 'never_read'
  | 'signed_out'
  | 'key_paid'
  | 'unqualified'
  | 'error'
  | 'unsupported'
  | 'not_installed'
  | 'remote'
  | 'env_set'
  | 'unlisted'
  | 'not_owner'

/** One capacity view (internal/capacity View). */
export interface CapacityView {
  agent: { id: string; name?: string; adapter?: string; version?: string }
  model: { chosen: string; words?: string }
  account: { id: string; label?: string; default?: boolean; retired?: boolean; plan?: string }
  host: { kind: 'native' | 'undetermined' | 'remote' | string; name?: string; words?: string }
  state: CapacityState | string
  words?: string
  windows: CapacityWindow[]
  credits: CapacityCredit[]
  limits?: CapacityLimits
  counts?: CapacityCounts
  readAt?: string
  readBy?: { program: string; version: string; qualified: string }
  fresh: 'fresh' | 'stale' | 'reset_passed' | 'none' | string
  nextReadAfter?: string
  /**
   * The admin's check is taken now though a plain read waits for `nextReadAfter`: the key's last read was refused only for want of that
   * check (a version not yet checked), and no read is under way.
   */
  checkNow?: boolean
  /** The variable (a name of at most 32 bytes) by which the agent's entry sets its login, for every account asked; absent for none. */
  entryLogin?: string
}

/** Where a launch runs, as the view is asked for it. */
export type CapacityLaunch = 'session' | 'remote' | 'managed' | 'legacy'

/** What a view is asked for: an agent, an account (none for its default) and the launch's own model, yolo and place. */
export interface CapacityQuery {
  agent: string
  account?: string
  model?: string
  launch?: CapacityLaunch
  yolo?: boolean
}

/** One string per question, the key a composable keeps its answer under. */
export function capacityKey(q: CapacityQuery): string {
  return JSON.stringify([q.agent, q.account ?? '', q.model ?? '', q.launch ?? 'session', q.yolo === undefined ? '' : q.yolo ? '1' : '0'])
}

/** The query string of `GET /api/capacity` for q: what q leaves out is not sent. */
export function capacityParams(q: CapacityQuery): Record<string, string> {
  const out: Record<string, string> = { agent: q.agent }
  if (q.account) out.account = q.account
  if (q.model) out.model = q.model
  if (q.launch && q.launch !== 'session') out.launch = q.launch
  if (q.yolo !== undefined) out.yolo = q.yolo ? 'true' : 'false'
  return out
}

/** A `capacity` event's key, or null for data that names none. */
export function asCapacityEvent(v: unknown): { agent: string; account: string } | null {
  if (!v || typeof v !== 'object') return null
  const { agent, account } = v as { agent?: unknown; account?: unknown }
  return typeof agent === 'string' && agent && typeof account === 'string' && account ? { agent, account } : null
}

/** Each state's words when the server gave none (internal/capacity stateWords says the same). */
const STATE_WORDS: Record<CapacityState, string> = {
  ok: "the provider's figures at the moment of the read",
  reading: "reading the agent's usage now",
  never_read: 'not read since the server started',
  signed_out: 'the agent is signed out on this account',
  key_paid: 'this login is paid by key: the provider reports no usage windows for it',
  unqualified: 'this version of the agent has not been checked for reading its usage',
  error: 'the read failed',
  unsupported: "Conductor knows no way to read this agent's usage",
  not_installed: "the agent's program is not installed on this server",
  remote: 'read where it runs: not on this server',
  env_set: "this launch's environment sets the agent's login: its usage is not read",
  unlisted: 'a folder not in Accounts: add it to read its usage',
  not_owner: "another server over this data directory reads this agent's usage",
}

/** Each state's title, short, before its words. */
const STATE_TITLES: Record<CapacityState, string> = {
  ok: '',
  reading: 'Reading…',
  never_read: 'Not read yet',
  signed_out: 'Signed out',
  key_paid: 'Paid by key',
  unqualified: 'Version not checked',
  error: 'Could not read',
  unsupported: 'Usage unknown',
  not_installed: 'Not installed',
  remote: 'Usage unknown here',
  env_set: 'Login set by the launch',
  unlisted: 'Folder not in Accounts',
  not_owner: 'Read by another server',
}

/** A state's title and its words: the server's words first, never the tool's. An unknown state is said as unknown. */
export function capacityStateWords(view: Pick<CapacityView, 'state' | 'words'>): { title: string; words: string } {
  const known = view.state in STATE_WORDS ? (view.state as CapacityState) : null
  if (!known) return { title: 'Usage unknown', words: view.words || `a state this page does not know (${view.state})` }
  return { title: STATE_TITLES[known], words: view.words || STATE_WORDS[known] }
}

const DAY = 86_400_000
const HOUR = 3_600_000
const MINUTE = 60_000

/** "in 5 days", "in 3 hours", "in 12 minutes", "in under a minute"; a time behind now "has passed". */
export function inWords(at: number, now: number): string {
  const ms = at - now
  if (ms <= 0) return 'has passed'
  const n = (unit: number, word: string) => {
    const k = Math.floor(ms / unit)
    return `in ${k} ${word}${k === 1 ? '' : 's'}`
  }
  if (ms >= DAY) return n(DAY, 'day')
  if (ms >= HOUR) return n(HOUR, 'hour')
  if (ms >= MINUTE) return n(MINUTE, 'minute')
  return 'in under a minute'
}

/** "3 minutes ago", "2 hours ago", "1 day ago", "just now". */
export function readAgoWords(at: number, now: number): string {
  const ms = Math.max(0, now - at)
  const n = (unit: number, word: string) => {
    const k = Math.floor(ms / unit)
    return `${k} ${word}${k === 1 ? '' : 's'} ago`
  }
  if (ms >= DAY) return n(DAY, 'day')
  if (ms >= HOUR) return n(HOUR, 'hour')
  if (ms >= MINUTE) return n(MINUTE, 'minute')
  return 'just now'
}

/** A time as "Wed 11 Nov 03:00" in the person's zone (`timeZone` for a fixed one). */
export function clockWords(at: number, timeZone?: string): string {
  const parts = new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone }).formatToParts(new Date(at))
  const p = (t: Intl.DateTimeFormatPartTypes) => parts.find((x) => x.type === t)?.value ?? ''
  return `${p('weekday')} ${p('day')} ${p('month')} ${p('hour')}:${p('minute')}`
}

/** A window in words: "Weekly, all models: 40% used, resets Wed 11 Nov 03:00, in 5 days". */
export function windowWords(w: CapacityWindow, now: number, timeZone?: string): string {
  const head = `${w.name}: ${w.percent}% used`
  if (!w.resetsAt) return `${head}, no reset reported`
  const at = Date.parse(w.resetsAt)
  if (Number.isNaN(at)) return `${head}, no reset reported`
  return `${head}, resets ${clockWords(at, timeZone)}, ${inWords(at, now)}`
}

/** A window's colour, the provider's severity in the brand palette (app.config.ts): normal forest, warning signal, critical brick. */
export function severityColor(w: Pick<CapacityWindow, 'severity'>): 'primary' | 'warning' | 'error' | 'neutral' {
  switch (w.severity) {
    case 'critical':
      return 'error'
    case 'warning':
      return 'warning'
    case 'normal':
      return 'primary'
    default:
      return 'neutral'
  }
}

/** The share used past which the panel warns, whatever the provider's severity (the plan's 90%). */
export const WARN_PERCENT = 90

/** Whether the provider says a limit holds: a limit reached, its spend control reached, or ordinary use not allowed now. */
export function limited(l: CapacityLimits | undefined): boolean {
  return !!l && (!!l.reached || l.spendControl === true || l.ordinaryAllowed === false)
}

/**
 * What the panel warns, in its colour, or null: at a limit the provider reports (whatever the windows say), the limit's words in
 * brick; else at a window at warning or critical, or at 90% used, that a window is near its limit (brick for critical, else signal).
 * It never stops a launch.
 */
export function warningOf(v: Pick<CapacityView, 'windows' | 'limits'>): { words: string; color: 'warning' | 'error' } | null {
  if (limited(v.limits)) return { words: LIMIT_WORDS, color: 'error' }
  if (!v.windows.some((w) => w.severity === 'warning' || w.severity === 'critical' || w.percent >= WARN_PERCENT)) return null
  return { words: WARN_WORDS, color: v.windows.some((w) => w.severity === 'critical') ? 'error' : 'warning' }
}

/** Whether the panel says the agent may stop (warningOf). */
export function warns(view: Pick<CapacityView, 'windows' | 'limits'>): boolean {
  return warningOf(view) !== null
}

/** A figure as the provider wrote it, with thousands marked when it is a plain number ("1250" → "1,250"); never computed with. */
export function figureWords(s: string): string {
  const m = /^(-?)(\d+)(\.\d+)?$/.exec(s)
  if (!m) return s
  return `${m[1]}${m[2]!.replace(/\B(?=(\d{3})+(?!\d))/g, ',')}${m[3] ?? ''}`
}

/** A credit row in words: "Extra usage: off", "Extra usage: on, 12 of 50 USD used", "Credits: 1,250", "Credits: unlimited". */
export function creditWords(c: CapacityCredit): string {
  const money = (s: string) => `${figureWords(s)}${c.currency ? ` ${c.currency}` : ''}`
  if (c.kind === 'extra_usage') {
    if (c.enabled === false) return 'Extra usage: off'
    const on = c.enabled ? 'on' : 'not reported whether on'
    if (c.used !== undefined && c.limit !== undefined) return `Extra usage: ${on}, ${money(c.used)} of ${money(c.limit)} used`
    if (c.used !== undefined) return `Extra usage: ${on}, ${money(c.used)} used`
    return `Extra usage: ${on}`
  }
  if (c.unlimited) return 'Credits: unlimited'
  if (c.balance !== undefined) return `Credits: ${money(c.balance)}`
  if (c.enabled === false) return 'Credits: none'
  return 'Credits: not reported'
}

/** The provider's flags in words, each one it reported; none is a list of none. */
export function limitsWords(l: CapacityLimits | undefined): string[] {
  if (!l) return []
  const out: string[] = []
  if (l.reached) out.push(`Limit reached: ${l.reached.replace(/_/g, ' ')}`)
  if (l.spendControl === true) out.push('Spend control reached')
  if (l.spendControl === false) out.push('Spend control not reached')
  if (l.ordinaryAllowed === false) out.push('Ordinary use not allowed now')
  if (l.ordinaryAllowed === true) out.push('Ordinary use allowed')
  return out
}

/** Conductor's own count, marked as an estimate: "2 sessions running on this account here, 5 started in the last 5 hours, …". */
export function countsWords(c: CapacityCounts | undefined): string {
  if (!c) return ''
  const s = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`
  return `${s(c.running, 'session', 'sessions')} running on this account here, ${c.started5h} started in the last 5 hours, ${c.started7d} in the last 7 days, counted by this server since it started`
}

/** The program that read, with its version, and the version the reader was checked on when that is another. */
function readByWords(v: CapacityView): string {
  if (!v.readBy) return ''
  const by = `${v.readBy.program} ${v.readBy.version}`
  return v.readBy.qualified && v.readBy.qualified !== v.readBy.version ? `${by}, checked on ${v.readBy.qualified}` : by
}

/**
 * How fresh the view is: "Reading…" while a read is under way, "Read 3 minutes ago by Claude Code 2.1.322", "Stale: read 40 minutes
 * ago", "A reset has passed since this reading"; "" when nothing was read.
 */
export function freshnessWords(v: CapacityView, now: number): string {
  if (v.state === 'reading') return 'Reading…'
  if (!v.readAt) return ''
  const at = Date.parse(v.readAt)
  if (Number.isNaN(at)) return ''
  const by = readByWords(v)
  const when = readAgoWords(at, now)
  if (v.fresh === 'reset_passed') return `A reset has passed since this reading (read ${when})`
  if (v.fresh === 'stale') return `Stale: read ${when}${by ? ` by ${by}` : ''}`
  return `Read ${when}${by ? ` by ${by}` : ''}`
}

/** Where the launch runs, in words. */
export function capacityHostWords(h: CapacityView['host']): string {
  if (h.kind === 'remote') return 'On this computer: your own login there, usage unknown here'
  if (h.kind === 'native') return h.name ? `Runs on this server, ${h.name}` : 'Runs on this server'
  return h.words || 'Where it runs is not decided yet'
}

/**
 * The model in words: the one chosen; for none, one sentence wherever a launch or a run's member is said (the panels, the launch lines,
 * the run page): its arguments may choose a model, which Conductor does not read, so none is never said to be the agent's default.
 */
export function modelWords(m: CapacityView['model'] | undefined): string {
  return m?.chosen ? m.chosen : 'no model named: the agent chooses, or its arguments do'
}

/** The plan as the tool reported it, with its provider's name: "Claude Max", "ChatGPT Pro"; "" when none was reported. */
export function planWords(plan: string | undefined, adapter: string | undefined): string {
  if (!plan) return ''
  const words = plan.replace(/_/g, ' ')
  const shown = words.charAt(0).toUpperCase() + words.slice(1)
  if (adapter === 'claude') return `Claude ${shown}`
  if (adapter === 'codex') return `ChatGPT ${shown}`
  return shown
}

/** What the panel says at the provider's warning or critical severity, or at 90% used: the agent may stop; the launch stays enabled. */
export const WARN_WORDS = 'The agent may stop before the reset: a window is near its limit. Launching is still yours to decide.'
/** What the panel says when the provider reports a limit reached, spend control reached or ordinary use not allowed. */
export const LIMIT_WORDS = 'The agent may stop: the provider says a limit is reached. Launching is still yours to decide.'

/** One statement of a capacity view, as both the full panel and the compact one say it (capacityStatements). */
export interface CapacityStatement {
  kind: 'host' | 'account' | 'model' | 'plan' | 'host-words' | 'entry-login' | 'state' | 'words' | 'window' | 'no-windows' | 'warning' | 'provider' | 'estimate' | 'fresh'
  /** Its words, said after `label` when it has one. */
  text: string
  label?: string
  /** A warning's colour. */
  color?: 'warning' | 'error'
  /** A window's statement: the window, for its bar. */
  window?: CapacityWindow
}

/** A statement in words, its label first. */
export function statementWords(s: CapacityStatement): string {
  return s.label ? `${s.label}: ${s.text}` : s.text
}

/**
 * Everything a panel says of a view, in order, one statement each (the full panel draws each in its place, the compact one says them
 * in its line or its details): where the launch runs, the account and the model (none for a launch with its own login, the account
 * only for one on a registered account), the plan, the server's words on where the figures come from, the entry's login, the state
 * (or the server's words beside a reading's figures), each window with its reset, the warning, the provider's figures and flags,
 * Conductor's estimate, and how fresh. `model` is the launch's own, as typed.
 */
export function capacityStatements(v: CapacityView, now: number, model = '', timeZone?: string): CapacityStatement[] {
  const login = launchLogin(v)
  const out: CapacityStatement[] = [{ kind: 'host', text: capacityHostWords(v.host) }]
  if (login.kind === 'account') out.push({ kind: 'account', label: 'Account', text: accountWords(v.account) })
  if (login.kind !== 'own') out.push({ kind: 'model', label: 'Model', text: modelWords(model.trim() ? { chosen: model.trim() } : v.model) })
  const plan = planWords(v.account.plan, v.agent.adapter)
  if (plan) out.push({ kind: 'plan', text: plan })
  if (v.host.kind === 'undetermined' && v.host.words) out.push({ kind: 'host-words', text: v.host.words })
  if (login.kind === 'entry') out.push({ kind: 'entry-login', text: `This agent's entry sets ${login.variable}: every launch of it uses that login, not an account chosen here.` })
  const state = capacityStateWords(v)
  if (v.state !== 'ok') out.push({ kind: 'state', label: state.title, text: state.words })
  else if (v.words) out.push({ kind: 'words', text: v.words })
  for (const w of v.windows) out.push({ kind: 'window', text: windowWords(w, now, timeZone), window: w })
  if (!v.windows.length && v.state === 'ok') out.push({ kind: 'no-windows', text: 'The provider reported no usage window.' })
  const warning = warningOf(v)
  if (warning) out.push({ kind: 'warning', text: warning.words, color: warning.color })
  const provider = [...v.credits.map(creditWords), ...limitsWords(v.limits)]
  if (provider.length) out.push({ kind: 'provider', label: "The provider's figures", text: provider.join(' · ') })
  if (v.counts && login.kind !== 'own') out.push({ kind: 'estimate', label: "Conductor's estimate", text: countsWords(v.counts) })
  const fresh = freshnessWords(v, now)
  if (fresh) out.push({ kind: 'fresh', text: fresh })
  return out
}

/**
 * The compact panel (the crew's launch sheet, a ticket's launch, Settings' Accounts), made from capacityStatements: a line (the plan,
 * the windows' shares or the state, and the server's own words beside a reading's figures), how fresh, the warning in its colour and
 * the server's words on where the figures come from, in text a phone shows; `details`, every other statement of the full panel (each
 * window with its reset, the provider's figures, the estimate, the entry's login…), for details that open by keyboard and by touch.
 */
export function compactWords(v: CapacityView, now: number, model = ''): { line: string; fresh: string; warning: { words: string; color: 'warning' | 'error' } | null; host: string; details: string[] } {
  const all = capacityStatements(v, now, model)
  const s = capacityStateWords(v)
  const shown = v.state === 'ok' ? [v.windows.map((w) => `${w.name} ${w.percent}%`).join(' · ') || 'no windows reported', v.words ?? ''].filter(Boolean).join(' · ') : `${s.title}: ${s.words}`
  const plan = planWords(v.account.plan, v.agent.adapter)
  const line = plan ? `${plan} · ${shown}` : shown
  const fresh = freshnessWords(v, now) || 'not read yet'
  const warning = warningOf(v)
  const host = all.find((x) => x.kind === 'host-words')?.text ?? ''
  const said = [line, fresh, warning?.words ?? '', host]
  return { line, fresh, warning, host, details: all.map(statementWords).filter((t) => !said.some((x) => x.includes(t))) }
}

/** The account in words: its label, or the agent's default login. */
export function accountWords(a: CapacityView['account']): string {
  if (a.id === 'default') return 'Default login'
  return a.label || a.id
}

/** Whether the view offers "Check this version": a version Claude Code's reader was not checked on (the admin's explicit check). */
export function offersCheck(v: CapacityView): boolean {
  return v.state === 'unqualified' && v.agent.adapter === 'claude'
}

/** Whether the view offers a read: never for what is answered without one, or while one is under way. */
export function offersRead(v: CapacityView): boolean {
  return !['reading', 'unsupported', 'not_installed', 'remote', 'env_set', 'unlisted', 'not_owner'].includes(v.state)
}

/**
 * Whether a panel that reads as it opens (the launch dialog, the crew's sheet) asks a read of this view: wherever the server reads,
 * unless the view's own button is "Check this version" (a Claude Code version the reader was not checked on, offersCheck): pressing the
 * check is the person's, and a read as the panel opens would spend the five minutes the check waits for when the server keeps none
 * for it. Any other view is read, an unqualified one too (a Codex version read only from a later one, a probe that failed, a program
 * that changed while it was checked), so it is not left stale on every opening.
 */
export function readsOnOpen(v: CapacityView): boolean {
  return offersRead(v) && !offersCheck(v)
}

/** What a panel offers beside its words: the check, a read, or nothing; the time it waits for, if any; whether it can be pressed now. */
export interface PanelAction {
  kind: 'check' | 'read' | 'none'
  wait: number | null
  disabled: boolean
}

/**
 * The panel's one button, in the full panel and the compact one alike: "Check this version" for a Claude Code version not checked, which
 * can be pressed at once when the server says a check is taken now (`checkNow`) and otherwise once `nextReadAfter` has passed; else
 * "Read now" where the server reads, waiting for `nextReadAfter`; nothing for a state answered without a read. Never while a read is out.
 */
export function panelAction(v: CapacityView, now: number, reading: boolean): PanelAction {
  const wait = readWait(v, now)
  if (offersCheck(v)) {
    const w = v.checkNow ? null : wait
    return { kind: 'check', wait: w, disabled: w !== null || reading }
  }
  if (offersRead(v) || reading) return { kind: 'read', wait, disabled: wait !== null || reading }
  return { kind: 'none', wait, disabled: true }
}

/** The time the server reads this key again at the soonest, when it is ahead of now; null when a read may be asked. */
export function readWait(v: Pick<CapacityView, 'nextReadAfter'>, now: number): number | null {
  if (!v.nextReadAfter) return null
  const at = Date.parse(v.nextReadAfter)
  return Number.isNaN(at) || at <= now ? null : at
}

/** One account the Account choice offers. */
export interface AccountChoice {
  /** The account's id, `default` for the agent's own login. */
  value: string
  label: string
}

/** What `GET /api/accounts` answers that the choices are made of. */
export interface AccountsLike {
  accounts: Array<{ id: string; adapter: string; label: string; retired?: boolean }>
  adapters: Array<{ adapter: string; default: string }>
}

/**
 * The accounts a launch of an agent of `adapter` may name: its default login, then each account of that adapter not retired. The
 * adapter's default is marked; a retired account is in no choice. An adapter the accounts do not list (one that does not move its
 * login folder) has no choice: [].
 */
export function accountChoices(doc: AccountsLike | null | undefined, adapter: string | undefined): AccountChoice[] {
  const ad = doc?.adapters.find((a) => a.adapter === adapter)
  if (!doc || !ad) return []
  const mark = (id: string, label: string) => (ad.default === id ? `${label} (default)` : label)
  return [{ value: 'default', label: mark('default', 'Default login') }, ...doc.accounts.filter((a) => a.adapter === adapter && !a.retired).map((a) => ({ value: a.id, label: mark(a.id, a.label) }))]
}

/**
 * The Account choice of a launch on this server, decided by the server's view of the launch with no account named (`unnamed`): it is
 * offered unless that view says the launch has its own login (`env_set`) or the agent's entry sets one (`entryLogin`); it shows the
 * person's pick, else the account the view resolved ('' before the view is here); and only a pick is sent, so a launch nobody chose
 * an account for is the route's to resolve.
 */
export function accountOfLaunch(choices: AccountChoice[], unnamed: Pick<CapacityView, 'state' | 'account' | 'entryLogin'> | null, picked: string): { offered: boolean; shown: string; sent?: string } {
  const offered = choices.length > 0 && unnamed?.state !== 'env_set' && !unnamed?.entryLogin
  const resolved = unnamed && choices.some((c) => c.value === unnamed.account.id) ? unnamed.account.id : ''
  return { offered, shown: picked || resolved, sent: offered && picked ? picked : undefined }
}

/** The login a view's launch uses, as the panel names it (`launchLogin`). */
export type LaunchLogin = { kind: 'own' } | { kind: 'entry'; variable: string } | { kind: 'account' }

/**
 * The login a view's launch uses, as the panel names it: `own` for a launch with its own login, not a registered account (on this
 * computer, where the other computer's login is used and nothing of the entry reaches it, or a login its environment sets); `entry`
 * for a launch on this server whose agent's entry sets its login, with the entry's variable; `account` for any other.
 */
export function launchLogin(v: Pick<CapacityView, 'state' | 'host' | 'entryLogin'>): LaunchLogin {
  if (v.host.kind === 'remote' || v.state === 'env_set') return { kind: 'own' }
  if (v.entryLogin) return { kind: 'entry', variable: v.entryLogin }
  return { kind: 'account' }
}

/** How long a reading is fresh (internal/capacity FreshFor). */
export const FRESH_FOR_MS = 15 * 60_000

/** The first moment a view's words change by the clock alone (it goes stale, or a window resets), or null for none. */
export function viewTurns(v: Pick<CapacityView, 'readAt' | 'fresh' | 'windows'>): number | null {
  const at = Date.parse(v.readAt ?? '')
  if (Number.isNaN(at) || v.fresh === 'reset_passed') return null
  const times = v.windows.map((w) => Date.parse(w.resetsAt ?? '')).filter((r) => r > at)
  if (v.fresh === 'fresh') times.push(at + FRESH_FOR_MS)
  return times.length ? Math.min(...times) : null
}

/** A timer: calls `fn` after `ms`, and answers how to cancel it. A store's clock, a test's own. */
export type After = (ms: number, fn: () => void) => () => void
export const realAfter: After = (ms, fn) => {
  const t = setTimeout(fn, ms)
  return () => clearTimeout(t)
}

/** `ask`, aborted and refused in `words` once `ms` has passed without its answer: a request that never answers holds nothing. */
export function withDeadline<T>(ask: (signal: AbortSignal) => Promise<T>, ms: number, words: string, after: After = realAfter): Promise<T> {
  const ctl = new AbortController()
  return new Promise<T>((resolve, reject) => {
    const cancel = after(ms, () => {
      ctl.abort()
      reject(new Error(words))
    })
    ask(ctl.signal).then(
      (v) => (cancel(), resolve(v)),
      (e) => (cancel(), reject(e)),
    )
  })
}

/** A model as the server holds one to (1 to 100 of `A-Z a-z 0-9 . _ : / @ -`, the first a letter or digit). */
export const MODEL_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,99}$/

/** Why a model typed is not one the server takes, or "" when it is (or none is typed: the agent's default). */
export function modelProblem(model: string): string {
  const m = model.trim()
  if (!m || MODEL_PATTERN.test(m)) return ''
  return 'A model is 1 to 100 letters, digits and . _ : / @ -, starting with a letter or a digit'
}

/** One part of the usage beside Launch agent's Account: its words, and its colour (the text's own, or the warning's or the error's). */
export interface FacePart {
  text: string
  tone: 'default' | 'warning' | 'error'
}

/**
 * The usage beside Launch agent's Account, in one line (faceWords): for a reading, each window the provider reported as `5-hour 12%`
 * (the server's names), a window at warning or past WARN_PERCENT in the warning colour, a critical one in the error colour; for any
 * other state its title (`Reading…`, `Not read yet`, `Signed out`, …); nothing for an agent no reader knows and for a launch on this
 * computer, where there is nothing to read; `Reading…` while no view has come.
 */
export function faceWords(view: Pick<CapacityView, 'state' | 'windows' | 'words'> | null | undefined): FacePart[] {
  if (!view) return [{ text: 'Reading…', tone: 'default' }]
  if (view.state === 'unsupported' || view.state === 'remote') return []
  if (view.state !== 'ok') return [{ text: capacityStateWords(view).title, tone: 'default' }]
  if (!view.windows.length) return [{ text: 'No usage reported', tone: 'default' }]
  return view.windows.map((w) => ({
    text: `${w.name} ${w.percent}%`,
    tone: w.severity === 'critical' ? 'error' : w.severity === 'warning' || w.percent >= WARN_PERCENT ? 'warning' : 'default',
  }))
}
