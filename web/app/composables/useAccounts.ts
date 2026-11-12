import { realAfter, withDeadline, type After } from '../utils/capacity'
import { ApiError } from './useApi'

/** A request's deadline: the accounts are a small file the server answers from at once, so this only bounds a hang. */
export const ACCOUNTS_DEADLINE_MS = 30_000
const DEADLINE_WORDS = `The server did not answer within ${ACCOUNTS_DEADLINE_MS / 1000} s`

/** An account as `GET /api/accounts` lists it, the admin's alone: a reference to an agent's own login, never a credential. */
export interface Account {
  id: string
  adapter: string
  label: string
  /** The folder that login lives in, fixed once added. */
  folder: string
  retired?: boolean
  createdAt: string
  /** Its last reading's state and time, when it was read since the server started. */
  reading?: { agent: string; state: string; readAt: string }
}

/** An adapter that moves its login folder, with its default account (`default` for the agent's own login) and that login's last reading. */
export interface AccountsAdapter {
  adapter: string
  name: string
  default: string
  reading?: { agent: string; state: string; readAt: string }
}

/** `GET /api/accounts` (docs/protocol.md, Capacity). */
export interface AccountsDoc {
  owner: boolean
  why?: string
  rev: number
  accounts: Account[]
  adapters: AccountsAdapter[]
}

/** What the store asks of the server; each write names the revision it read (`expectedRev`). */
export interface AccountsReads {
  list: (signal: AbortSignal) => Promise<AccountsDoc>
  add: (body: { adapter: string; label: string; folder: string; expectedRev: number }) => Promise<unknown>
  rename: (id: string, body: { label: string; expectedRev: number }) => Promise<unknown>
  retire: (id: string, body: { expectedRev: number }) => Promise<unknown>
  remove: (id: string, expectedRev: number) => Promise<unknown>
  setDefault: (adapter: string, body: { account: string; expectedRev: number }) => Promise<unknown>
}

/** The words of a write refused because the accounts changed since this page read them; the page has read them again. */
export const STALE_WORDS = 'The accounts changed since this page read them: they are shown again as they are now. Try again if it still applies.'

/** A refusal's words: the server's own, with the sessions and runs that hold an account when it names them. */
export function accountRefusalWords(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === 'stale_rev') return STALE_WORDS
    const names = Array.isArray(e.details.names) ? (e.details.names as unknown[]).filter((n): n is string => typeof n === 'string') : []
    return names.length ? `${e.message}: ${names.join(', ')}` : e.message
  }
  return (e as Error).message
}

/**
 * The accounts document for the app's screens (docs/plans/capacity.md, PR-5), one for the app: read when a page shows it, and again
 * after every write, which names the revision this store read (`expectedRev`); a write refused `stale_rev` reads the document again
 * and says so. One write at a time; a read's answer older than one taken is dropped. A server that offers no accounts (a switchyard,
 * no data directory) is `available: false`, and the pages show nothing of them.
 */
export class AccountsLive {
  doc: AccountsDoc | null = null
  /** Whether the server offers accounts: unknown (null) until a read answers. */
  available: boolean | null = null
  /** Why the last read failed, in the server's words (or "Cannot reach the server"); "" when it did not. */
  problem = ''
  /** A write is under way, until the read of the document after it has ended. */
  busy = false
  private reads: AccountsReads | null = null
  /** Bumped by `forget`: a step begun under another token stands no more. */
  private epoch = 0
  private sent = 0
  private taken = 0
  changed: () => void = () => {}

  constructor(private readonly after: After = realAfter) {}

  attach(reads: AccountsReads) {
    this.reads = reads
  }

  /** Whether a step still stands: its token is the one held, and (for a read, numbered n) no later read's answer was taken. */
  private stands(epoch: number, n = Infinity): boolean {
    return epoch === this.epoch && n > this.taken
  }

  /** Reads the document; an answer to an earlier read than one taken is dropped. */
  async load(): Promise<void> {
    if (!this.reads) return
    const epoch = this.epoch
    const n = ++this.sent
    const reads = this.reads
    let doc: AccountsDoc | null = null
    let error: unknown = null
    try {
      doc = await withDeadline((signal) => reads.list(signal), ACCOUNTS_DEADLINE_MS, DEADLINE_WORDS, this.after)
    } catch (e) {
      error = e
    }
    if (!this.stands(epoch, n)) return
    this.taken = n
    if (doc) {
      this.doc = doc
      this.available = true
      this.problem = ''
    } else if (error instanceof ApiError && (error.status === 403 || error.status === 503 || error.status === 404)) {
      this.available = false
      this.doc = null
      this.problem = ''
    } else {
      this.problem = (error as Error).message || 'Cannot reach the server'
    }
    this.changed()
  }

  /** Runs one write against the revision read, then reads the document again, busy until that read has ended; a refusal is thrown in its words. */
  private async write(send: (rev: number) => Promise<unknown>): Promise<void> {
    if (!this.reads || !this.doc) throw new Error('The accounts have not been read yet')
    if (this.busy) throw new Error('Another change to the accounts is under way')
    const epoch = this.epoch
    const rev = this.doc.rev
    this.busy = true
    this.changed()
    let refusal: unknown = null
    try {
      await withDeadline(() => send(rev), ACCOUNTS_DEADLINE_MS, DEADLINE_WORDS, this.after)
    } catch (e) {
      refusal = e
    }
    if (!this.stands(epoch)) return
    await this.load()
    if (!this.stands(epoch)) return
    this.busy = false
    this.changed()
    if (refusal) throw new Error(accountRefusalWords(refusal))
  }

  add(adapter: string, label: string, folder: string) {
    return this.write((rev) => this.reads!.add({ adapter, label: label.trim(), folder: folder.trim(), expectedRev: rev }))
  }

  rename(id: string, label: string) {
    return this.write((rev) => this.reads!.rename(id, { label: label.trim(), expectedRev: rev }))
  }

  retire(id: string) {
    return this.write((rev) => this.reads!.retire(id, { expectedRev: rev }))
  }

  remove(id: string) {
    return this.write((rev) => this.reads!.remove(id, rev))
  }

  setDefault(adapter: string, account: string) {
    return this.write((rev) => this.reads!.setDefault(adapter, { account, expectedRev: rev }))
  }

  /** Another workbench token: nothing read through the old one stays, and every step on its way for it stands no more. */
  forget() {
    this.epoch++
    this.doc = null
    this.available = null
    this.problem = ''
    this.busy = false
    this.changed()
  }
}

let live: AccountsLive | undefined
/** The app's one store of the accounts document. */
export function accountsLive(): AccountsLive {
  return (live ??= new AccountsLive())
}

/** The accounts for a page: the store, its reads wired to the server's routes, and computed views of it. */
export function useAccounts() {
  const { request } = useApi()
  const admin = useWorkbenchToken()
  const version = useState<number>('accountsVersion', () => 0)
  const store = accountsLive()
  const id = (s: string) => encodeURIComponent(s)
  store.attach({
    list: (signal) => request<AccountsDoc>('/api/accounts', { signal }),
    add: (body) => request('/api/accounts', { method: 'POST', body }),
    rename: (a, body) => request(`/api/accounts/${id(a)}`, { method: 'PATCH', body }),
    retire: (a, body) => request(`/api/accounts/${id(a)}/retire`, { method: 'POST', body }),
    remove: (a, rev) => request(`/api/accounts/${id(a)}`, { method: 'DELETE', query: { expectedRev: String(rev) } }),
    setDefault: (ad, body) => request(`/api/accounts/defaults/${id(ad)}`, { method: 'PUT', body }),
  })
  store.changed = () => version.value++
  // The token is watched while a page shows the accounts: another one empties the store, so nothing read with the old one is shown.
  const held = useState<string | null>('accountsToken', () => null)
  const judge = () => {
    if (held.value !== null && held.value !== admin.token.value) store.forget()
    held.value = admin.token.value
  }
  judge()
  watch(admin.token, judge)

  const doc = computed(() => {
    void version.value
    return store.doc
  })
  const available = computed(() => {
    void version.value
    return store.available
  })
  const busy = computed(() => {
    void version.value
    return store.busy
  })
  const problem = computed(() => {
    void version.value
    return store.problem
  })
  return { store, doc, available, busy, problem }
}
