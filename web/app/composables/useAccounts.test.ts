import { describe, expect, it } from 'vitest'
import { ApiError } from './useApi'
import { ACCOUNTS_DEADLINE_MS, AccountsLive, STALE_WORDS, accountRefusalWords, type AccountsDoc, type AccountsReads } from './useAccounts'

// The accounts store (docs/plans/capacity.md, PR-5): every write names the revision read and is followed by a read of the document;
// a stale revision reads it again and says so; a refusal is the server's words, with what holds an account; a server that offers
// no accounts shows none.

function doc(rev: number, over: Partial<AccountsDoc> = {}): AccountsDoc {
  return { owner: true, rev, accounts: [], adapters: [{ adapter: 'claude', name: 'Claude Code', default: 'default' }], ...over }
}

/** A server that keeps a document and records each write with the revision it named. */
function fakeServer(start: AccountsDoc) {
  let current = start
  const writes: Array<{ op: string; args: unknown[] }> = []
  let refuse: ApiError | null = null
  const write = (op: string, rev: number, ...args: unknown[]) => {
    writes.push({ op, args: [...args, rev] })
    if (refuse) {
      const e = refuse
      refuse = null
      return Promise.reject(e)
    }
    if (rev !== current.rev) return Promise.reject(new ApiError(409, 'stale_rev', 'the accounts changed'))
    current = { ...current, rev: current.rev + 1 }
    return Promise.resolve({})
  }
  const reads: AccountsReads = {
    list: () => Promise.resolve(current),
    add: (b) => write('add', b.expectedRev, b.adapter, b.label, b.folder),
    rename: (id, b) => write('rename', b.expectedRev, id, b.label),
    retire: (id, b) => write('retire', b.expectedRev, id),
    remove: (id, rev) => write('remove', rev, id),
    setDefault: (ad, b) => write('default', b.expectedRev, ad, b.account),
  }
  return {
    reads,
    writes,
    set: (d: AccountsDoc) => (current = d),
    refuseNext: (e: ApiError) => (refuse = e),
  }
}

describe('AccountsLive', () => {
  it('sends each write with the revision read, then reads the document again', async () => {
    const srv = fakeServer(doc(4))
    const live = new AccountsLive()
    live.attach(srv.reads)
    await live.load()
    expect(live.available).toBe(true)
    await live.add('claude', '  Work ', ' /home/me/.claude-work ')
    expect(srv.writes[0]).toEqual({ op: 'add', args: ['claude', 'Work', '/home/me/.claude-work', 4] })
    expect(live.doc?.rev).toBe(5)
    await live.setDefault('claude', 'a-work')
    await live.rename('a-work', 'Day job')
    await live.retire('a-work')
    await live.remove('a-work')
    expect(srv.writes.map((w) => [w.op, w.args.at(-1)])).toEqual([
      ['add', 4],
      ['default', 5],
      ['rename', 6],
      ['retire', 7],
      ['remove', 8],
    ])
    expect(live.busy).toBe(false)
  })

  it('reads the document again on a stale revision and says so, sending nothing more', async () => {
    const srv = fakeServer(doc(1))
    const live = new AccountsLive()
    live.attach(srv.reads)
    await live.load()
    srv.set(doc(2, { accounts: [{ id: 'a-other', adapter: 'claude', label: 'Other', folder: '/f', createdAt: '2026-11-12T10:00:00Z' }] }))
    await expect(live.retire('a-other')).rejects.toThrow(STALE_WORDS)
    expect(srv.writes).toHaveLength(1)
    expect(live.doc?.rev).toBe(2)
    expect(live.doc?.accounts.map((a) => a.label)).toEqual(['Other'])
  })

  it("says a refusal in the server's words, naming what holds the account", async () => {
    const srv = fakeServer(doc(1))
    const live = new AccountsLive()
    live.attach(srv.reads)
    await live.load()
    srv.refuseNext(new ApiError(409, 'account_in_use', 'the account is bound to running work', { names: ['session s-1', 'run r-2'] }))
    await expect(live.remove('a-work')).rejects.toThrow('the account is bound to running work: session s-1, run r-2')
    expect(accountRefusalWords(new ApiError(400, 'invalid_account', 'the folder is not a directory the server owns', { field: 'folder' }))).toBe('the folder is not a directory the server owns')
  })

  it('sends one write at a time, and none before the document was read', async () => {
    const srv = fakeServer(doc(1))
    const live = new AccountsLive()
    live.attach(srv.reads)
    await expect(live.retire('a-1')).rejects.toThrow('have not been read')
    await live.load()
    const first = live.retire('a-1')
    await expect(live.retire('a-2')).rejects.toThrow('under way')
    await first
    expect(srv.writes.map((w) => w.args[0])).toEqual(['a-1'])
  })

  it('shows no accounts for a server that offers none, and forgets them for another token', async () => {
    const live = new AccountsLive()
    live.attach({ ...fakeServer(doc(1)).reads, list: () => Promise.reject(new ApiError(403, 'switchyard', 'a switchyard launches nothing')) })
    await live.load()
    expect(live.available).toBe(false)
    expect(live.doc).toBeNull()
    const other = new AccountsLive()
    let answer: (d: AccountsDoc) => void = () => {}
    other.attach({ ...fakeServer(doc(1)).reads, list: () => new Promise((r) => (answer = r)) })
    const loading = other.load()
    other.forget()
    answer(doc(9))
    await loading
    expect(other.doc).toBeNull()
    expect(other.available).toBeNull()
  })

  it("stays busy through a write's read of the document: a second click sends nothing, and the next carries the new revision", async () => {
    const srv = fakeServer(doc(1))
    let hold: Promise<void> | null = null
    const live = new AccountsLive()
    live.attach({ ...srv.reads, list: () => (hold ? hold.then(() => srv.reads.list(new AbortController().signal)) : srv.reads.list(new AbortController().signal)) })
    await live.load()
    let release!: () => void
    hold = new Promise((r) => (release = r))
    const first = live.retire('a-1')
    await settle()
    // The write has landed (revision 2 now); its read of the document is held.
    expect(srv.writes).toHaveLength(1)
    expect(live.busy).toBe(true)
    await expect(live.retire('a-2')).rejects.toThrow('under way')
    expect(srv.writes).toHaveLength(1)
    hold = null
    release()
    await first
    expect(live.busy).toBe(false)
    await live.retire('a-2')
    expect(srv.writes.map((w) => w.args)).toEqual([
      ['a-1', 1],
      ['a-2', 2],
    ])
  })

  it('says a failed read in its words, at the first read and over a document already shown, and reads again when asked', async () => {
    let fail: Error | null = new ApiError(500, 'http_error', 'Internal Server Error')
    const srv = fakeServer(doc(1))
    const live = new AccountsLive()
    live.attach({ ...srv.reads, list: (signal) => (fail ? Promise.reject(fail) : srv.reads.list(signal)) })
    await live.load()
    expect(live).toMatchObject({ doc: null, available: null, problem: 'Internal Server Error' })
    fail = new ApiError(0, 'network', 'Cannot reach the server')
    await live.load()
    expect(live.problem).toBe('Cannot reach the server')
    fail = null
    await live.load()
    expect(live).toMatchObject({ available: true, problem: '' })
    fail = new ApiError(413, 'bound', 'the reply would pass its bound')
    await live.load()
    expect(live.problem).toBe('the reply would pass its bound')
    expect(live.doc?.rev).toBe(1)
  })

  it('lets a read that never answers go at its deadline, in words', async () => {
    let fire: () => void = () => {}
    const live = new AccountsLive((ms, fn) => {
      expect(ms).toBe(ACCOUNTS_DEADLINE_MS)
      fire = fn
      return () => {}
    })
    let signal: AbortSignal | null = null
    live.attach({ ...fakeServer(doc(1)).reads, list: (s) => ((signal = s), new Promise(() => {})) })
    const loading = live.load()
    fire()
    await loading
    expect(live.problem).toBe(`The server did not answer within ${ACCOUNTS_DEADLINE_MS / 1000} s`)
    expect(signal!.aborted).toBe(true)
  })
})

const settle = async () => {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0))
}
