import { ApiError } from './useApi'

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
  list: () => Promise<AccountsDoc>
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
  problem = ''
  busy = false
  private reads: AccountsReads | null = null
  private sent = 0
  private taken = 0
  changed: () => void = () => {}

  attach(reads: AccountsReads) {
    this.reads = reads
  }

  /** Reads the document; an answer to an earlier read than one taken is dropped. */
  async load(): Promise<void> {
    if (!this.reads) return
    const n = ++this.sent
    try {
      const doc = await this.reads.list()
      if (n < this.taken) return
      this.taken = n
      this.doc = doc
      this.available = true
      this.problem = ''
    } catch (e) {
      if (n < this.taken) return
      this.taken = n
      if (e instanceof ApiError && (e.status === 403 || e.status === 503 || e.status === 404)) {
        this.available = false
        this.doc = null
      }
      this.problem = (e as Error).message
    } finally {
      this.changed()
    }
  }

  /** Runs one write against the revision read, then reads the document again; a refusal is thrown in its words. */
  private async write(send: (rev: number) => Promise<unknown>): Promise<void> {
    if (!this.reads || !this.doc) throw new Error('The accounts have not been read yet')
    if (this.busy) throw new Error('Another change to the accounts is under way')
    this.busy = true
    this.changed()
    try {
      await send(this.doc.rev)
    } catch (e) {
      await this.load()
      throw new Error(accountRefusalWords(e))
    } finally {
      this.busy = false
      this.changed()
    }
    await this.load()
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

  /** Another workbench token: nothing read through the old one stays, and a read on its way for it is dropped. */
  forget() {
    this.doc = null
    this.available = null
    this.problem = ''
    this.taken = ++this.sent
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
    list: () => request<AccountsDoc>('/api/accounts'),
    add: (body) => request('/api/accounts', { method: 'POST', body }),
    rename: (a, body) => request(`/api/accounts/${id(a)}`, { method: 'PATCH', body }),
    retire: (a, body) => request(`/api/accounts/${id(a)}/retire`, { method: 'POST', body }),
    remove: (a, rev) => request(`/api/accounts/${id(a)}`, { method: 'DELETE', query: { expectedRev: String(rev) } }),
    setDefault: (ad, body) => request(`/api/accounts/defaults/${id(ad)}`, { method: 'PUT', body }),
  })
  store.changed = () => version.value++
  const token = useState<string | null>('accountsToken', () => null)
  if (token.value !== null && token.value !== admin.token.value) store.forget()
  token.value = admin.token.value

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
