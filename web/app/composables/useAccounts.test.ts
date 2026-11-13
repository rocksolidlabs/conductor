import { describe, expect, it } from 'vitest'
import { ApiError } from './useApi'
import { ACCOUNTS_DEADLINE_MS, AccountsLive, STALE_WORDS, TokenReplacedError, UNANSWERED_WORDS, accountRefusalWords, type AccountsDoc, type AccountsReads } from './useAccounts'

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
    let answer: ((d: AccountsDoc) => void) | null = null
    other.attach({ ...fakeServer(doc(1)).reads, list: () => new Promise((r) => (answer ??= r)) })
    const loading = other.load()
    other.forget()
    answer!(doc(9))
    await loading
    expect(other.doc).toBeNull()
    expect(other.available).toBeNull()
  })

  it('reads the accounts again with the token now held when another token is taken, whatever page shows them', async () => {
    const asked: Array<(d: AccountsDoc) => void> = []
    const live = new AccountsLive()
    live.attach({ ...fakeServer(doc(1)).reads, list: () => new Promise((r) => asked.push(r)) })
    const loading = live.load()
    live.forget()
    // The read begun before is answered after: nothing of it is taken.
    asked[0]!(doc(9))
    await loading
    expect(live).toMatchObject({ doc: null, available: null })
    expect(asked).toHaveLength(2)
    asked[1]!(doc(3))
    await settle()
    expect(live).toMatchObject({ available: true, problem: '' })
    expect(live.doc?.rev).toBe(3)
  })

  it('rejects a write whose token was replaced while it was on its way, whether its request then answers or is refused, and draws nothing of it', async () => {
    for (const outcome of ['answers', 'is refused'] as const) {
      const srv = fakeServer(doc(1))
      const lists: Array<(d: AccountsDoc) => void> = []
      let n = 0
      let held: { answer: () => void; refuse: () => void } | null = null
      const live = new AccountsLive()
      live.attach({
        ...srv.reads,
        list: (signal) => (++n === 1 ? srv.reads.list(signal) : new Promise((r) => lists.push(r))),
        add: () => new Promise((resolve, reject) => (held = { answer: () => resolve({}), refuse: () => reject(new ApiError(409, 'account_exists', 'that folder is an account already')) })),
      })
      await live.load()
      const writing = live.add('claude', 'Work', '/home/me/.claude-work').then(
        () => 'fulfilled',
        (e: unknown) => e,
      )
      await settle()
      expect(live.busy).toBe(true)
      live.forget()
      expect(lists).toHaveLength(1)
      lists[0]!(doc(5))
      await settle()
      expect(live).toMatchObject({ busy: false, available: true, problem: '' })
      if (outcome === 'answers') held!.answer()
      else held!.refuse()
      const result = await writing
      expect(result, `the write whose request ${outcome}`).not.toBe('fulfilled')
      expect(result).toBeInstanceOf(TokenReplacedError)
      // No read after the old answer, and the document and busy are the read after forget()'s.
      expect(lists).toHaveLength(1)
      expect(live).toMatchObject({ busy: false, available: true, problem: '' })
      expect(live.doc?.rev).toBe(5)
    }
  })

  it("rejects a write whose token was replaced while the read after it was on its way, and that read's answer is not taken", async () => {
    const srv = fakeServer(doc(1))
    const lists: Array<(d: AccountsDoc) => void> = []
    let n = 0
    const live = new AccountsLive()
    live.attach({ ...srv.reads, list: (signal) => (++n === 1 ? srv.reads.list(signal) : new Promise((r) => lists.push(r))) })
    await live.load()
    const writing = live.retire('a-1').then(
      () => 'fulfilled',
      (e: unknown) => e,
    )
    await settle()
    expect(lists).toHaveLength(1)
    live.forget()
    expect(lists).toHaveLength(2)
    lists[1]!(doc(7))
    lists[0]!(doc(2))
    const result = await writing
    expect(result).not.toBe('fulfilled')
    expect(result).toBeInstanceOf(TokenReplacedError)
    expect(live.doc?.rev).toBe(7)
    expect(live.busy).toBe(false)
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

  it('aborts a write the server does not answer by its deadline, says its outcome is not known, and stays busy until the accounts are read again', async () => {
    const timers: Array<{ ms: number; fn: () => void; live: boolean }> = []
    const live = new AccountsLive((ms, fn) => {
      const t = { ms, fn, live: true }
      timers.push(t)
      return () => (t.live = false)
    })
    const srv = fakeServer(doc(1))
    let held: { signal: AbortSignal; answer: () => void } | null = null
    let reread: ((d: AccountsDoc) => void) | null = null
    let lists = 0
    live.attach({
      ...srv.reads,
      list: (signal) => (++lists === 1 ? srv.reads.list(signal) : new Promise((r) => (reread = r))),
      setDefault: (_ad, _b, signal) => new Promise((answer) => (held = { signal, answer: () => answer({}) })),
    })
    await live.load()
    let changes = 0
    live.changed = () => changes++
    const writing = live.setDefault('claude', 'a-work').catch((e: Error) => e.message)
    await settle()
    expect(held!.signal.aborted).toBe(false)
    // The write's deadline passes: its request is aborted, and the accounts are read again, busy until that read has ended.
    timers.filter((t) => t.live && t.ms === ACCOUNTS_DEADLINE_MS).forEach((t) => ((t.live = false), t.fn()))
    await settle()
    expect(held!.signal.aborted).toBe(true)
    expect(lists).toBe(2)
    expect(live.busy).toBe(true)
    reread!(doc(2, { adapters: [{ adapter: 'claude', name: 'Claude Code', default: 'a-work' }] }))
    expect(await writing).toBe(UNANSWERED_WORDS)
    expect(UNANSWERED_WORDS).toContain('did not answer within')
    expect(UNANSWERED_WORDS).toContain('may still have been made')
    expect(live.busy).toBe(false)
    expect(live.doc?.adapters[0]?.default).toBe('a-work')
    // The aborted request's late answer draws nothing more.
    const drawn = changes
    held!.answer()
    await settle()
    expect(changes).toBe(drawn)
    expect(live.doc?.rev).toBe(2)
  })

  it('says a store that could not be read (503) as a failed read, at the first read and over the document shown, and leaves the section out only when the accounts are not here', async () => {
    let fail: Error | null = new ApiError(503, 'capacity_unavailable', 'the accounts could not be read')
    const srv = fakeServer(doc(1))
    const live = new AccountsLive()
    live.attach({ ...srv.reads, list: (signal) => (fail ? Promise.reject(fail) : srv.reads.list(signal)) })
    await live.load()
    expect(live).toMatchObject({ doc: null, available: null, problem: 'the accounts could not be read' })
    fail = null
    await live.load()
    expect(live).toMatchObject({ available: true, problem: '' })
    fail = new ApiError(503, 'capacity_unavailable', 'the accounts could not be read')
    await live.load()
    expect(live).toMatchObject({ available: true, problem: 'the accounts could not be read' })
    expect(live.doc?.rev).toBe(1)
    // Not here (404): the section is left out.
    fail = new ApiError(404, 'not_found', 'not found')
    await live.load()
    expect(live).toMatchObject({ available: false, doc: null, problem: '' })
  })
})

const settle = async () => {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0))
}
