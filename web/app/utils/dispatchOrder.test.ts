import { describe, expect, it } from 'vitest'
import { DispatchOrder, catchUp, retried, serially, through, throughOrAbsent, type CatchUpOptions } from './dispatchOrder'

/** A request whose reply the test hands back, or fails, when it wants. */
function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: Error) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

interface Shown {
  key: string
  rev: number
  title: string
}

/** A page's state for one ticket, kept through the gate as a page keeps it. */
function page() {
  const gate = new DispatchOrder()
  const shown: { ticket: Shown | null } = { ticket: null }
  const thing = 'ticket:CND-12'
  /** A request sent now; its reply applied as the gate says. */
  function send(reply: Promise<Shown>): Promise<boolean> {
    return through(gate, thing, () => reply, (t) => (shown.ticket = t), (t) => t.rev)
  }
  return { gate, shown, send, thing }
}

describe('the ordering gate', () => {
  it("leaves the later read's reply when two reads of one ticket are answered in reverse order", async () => {
    const p = page()
    const first = deferred<Shown>()
    const second = deferred<Shown>()
    const a = p.send(first.promise)
    const b = p.send(second.promise)
    second.resolve({ key: 'CND-12', rev: 5, title: 'later' })
    expect(await b).toBe(true)
    first.resolve({ key: 'CND-12', rev: 5, title: 'earlier' })
    expect(await a).toBe(false)
    expect(p.shown.ticket?.title).toBe('later')
  })

  it("drops an operation's reply that arrives after a later read's was applied", async () => {
    const p = page()
    const edit = deferred<Shown>()
    const reread = deferred<Shown>()
    const op = p.send(edit.promise)
    const read = p.send(reread.promise)
    reread.resolve({ key: 'CND-12', rev: 6, title: 'after the edit and the next one' })
    expect(await read).toBe(true)
    edit.resolve({ key: 'CND-12', rev: 5, title: 'the edit' })
    expect(await op).toBe(false)
    expect(p.shown.ticket).toEqual({ key: 'CND-12', rev: 6, title: 'after the edit and the next one' })
  })

  it("leaves an earlier success applied when a later request fails: the edit's reply lands after the failed re-read, and the rev moves", async () => {
    const p = page()
    p.shown.ticket = { key: 'CND-12', rev: 4, title: 'before' }
    expect(await p.send(Promise.resolve({ key: 'CND-12', rev: 4, title: 'before' }))).toBe(true)
    // An edit is sent; its commit's event asks a re-read, which fails; the edit's reply arrives last.
    const edit = deferred<Shown>()
    const reread = deferred<Shown>()
    const op = p.send(edit.promise)
    const read = p.send(reread.promise)
    reread.reject(new Error('Cannot reach the server'))
    await expect(read).rejects.toThrow('Cannot reach the server')
    expect(p.gate.rev(p.thing)).toBe(4)
    edit.resolve({ key: 'CND-12', rev: 5, title: 'edited' })
    expect(await op).toBe(true)
    expect(p.shown.ticket).toEqual({ key: 'CND-12', rev: 5, title: 'edited' })
    // The rev shown moved: the next operation expects 5.
    expect(p.gate.rev(p.thing)).toBe(5)
  })

  it('drops a reply whose rev is below the one shown, whatever its number', async () => {
    const p = page()
    expect(await p.send(Promise.resolve({ key: 'CND-12', rev: 7, title: 'seven' }))).toBe(true)
    // A read sent later, served before a commit another browser made, answers an older rev.
    expect(await p.send(Promise.resolve({ key: 'CND-12', rev: 6, title: 'six' }))).toBe(false)
    expect(p.shown.ticket?.title).toBe('seven')
    // The same rev again is the same state, and taken.
    expect(await p.send(Promise.resolve({ key: 'CND-12', rev: 7, title: 'seven again' }))).toBe(true)
    // By hand: a later number with a lower rev is dropped; a higher rev with an earlier number too.
    const g = new DispatchOrder()
    const n1 = g.ask()
    const n2 = g.ask()
    expect(g.take('ticket:A-1', n2, 3)).toBe(true)
    expect(g.take('ticket:A-1', n1, 9)).toBe(false)
    expect(g.take('ticket:A-1', g.ask(), 2)).toBe(false)
    expect(g.rev('ticket:A-1')).toBe(3)
  })

  it("drops the old filter's late page once the filter has changed", async () => {
    const gate = new DispatchOrder()
    let shown = ''
    const oldPage = deferred<string>()
    const old = through(gate, 'list:CND?status=ready', () => oldPage.promise, (v) => (shown = v))
    // The filter changes: a new thing; the old one is dropped.
    gate.drop('list:CND?status=ready')
    expect(await through(gate, 'list:CND?status=done', () => Promise.resolve('done tickets'), (v) => (shown = v))).toBe(true)
    oldPage.resolve('ready tickets')
    expect(await old).toBe(false)
    expect(shown).toBe('done tickets')
    // The old filter asked for again starts afresh.
    expect(await through(gate, 'list:CND?status=ready', () => Promise.resolve('ready again'), (v) => (shown = v))).toBe(true)
    expect(shown).toBe('ready again')
  })

  it("does not drop another thing's reply", async () => {
    const gate = new DispatchOrder()
    const shown: Record<string, string> = {}
    const project = deferred<string>()
    const p = through(gate, 'project:CND', () => project.promise, (v) => (shown.project = v), () => 2)
    expect(await through(gate, 'ticket:CND-12', () => Promise.resolve('ticket'), (v) => (shown.ticket = v), () => 9)).toBe(true)
    expect(await through(gate, 'list:CND?', () => Promise.resolve('list'), (v) => (shown.list = v))).toBe(true)
    project.resolve('project')
    expect(await p).toBe(true)
    expect(shown).toEqual({ project: 'project', ticket: 'ticket', list: 'list' })
  })

  it('takes a reply once: the same number twice is no later reply', () => {
    const g = new DispatchOrder()
    const n = g.ask()
    expect(g.take('projects', n)).toBe(true)
    expect(g.take('projects', n)).toBe(false)
  })
})

describe('a thing that may not exist', () => {
  /** A ticket page's state, its reads through the gate, a 404 among their answers. */
  function ticketPage() {
    const gate = new DispatchOrder()
    const shown: { ticket: Shown | null; missing: boolean } = { ticket: null, missing: false }
    const notFound = new Error('No ticket has the key CND-12.')
    function read(reply: Promise<Shown>): Promise<boolean> {
      return throughOrAbsent(gate, 'ticket:CND-12', () => reply, {
        apply: (t) => {
          shown.ticket = t
          shown.missing = false
        },
        rev: (t) => t.rev,
        absent: (e) => e === notFound,
        gone: () => (shown.missing = true),
      })
    }
    return { gate, shown, read, notFound }
  }

  it("drops a 404 that answers after a later read's success: the ticket made since the page opened stays shown", async () => {
    const p = ticketPage()
    // The page opens before the ticket is made; its read is slow.
    const first = deferred<Shown>()
    const opened = p.read(first.promise)
    // The ticket is made, and its event's re-read answers first.
    expect(await p.read(Promise.resolve({ key: 'CND-12', rev: 1, title: 'made since' }))).toBe(true)
    first.reject(p.notFound)
    expect(await opened).toBe(false)
    expect(p.shown).toEqual({ ticket: { key: 'CND-12', rev: 1, title: 'made since' }, missing: false })
  })

  it('applies a 404 that answers last, and forgets the rev shown, so the thing read again is taken at the rev it has', async () => {
    const p = ticketPage()
    expect(await p.read(Promise.resolve({ key: 'CND-12', rev: 7, title: 'seven' }))).toBe(true)
    expect(await p.read(Promise.reject(p.notFound))).toBe(true)
    expect(p.shown.missing).toBe(true)
    expect(p.gate.rev('ticket:CND-12')).toBe(0)
    expect(await p.read(Promise.resolve({ key: 'CND-12', rev: 2, title: 'two' }))).toBe(true)
    expect(p.shown).toEqual({ ticket: { key: 'CND-12', rev: 2, title: 'two' }, missing: false })
  })

  it('rejects any other failure, applying nothing and moving nothing', async () => {
    const p = ticketPage()
    expect(await p.read(Promise.resolve({ key: 'CND-12', rev: 4, title: 'four' }))).toBe(true)
    await expect(p.read(Promise.reject(new Error('Cannot reach the server')))).rejects.toThrow('Cannot reach the server')
    expect(p.shown).toEqual({ ticket: { key: 'CND-12', rev: 4, title: 'four' }, missing: false })
    expect(p.gate.rev('ticket:CND-12')).toBe(4)
  })
})

describe('a stream started again', () => {
  it('drops every reply asked before it, and takes the next read at the rev it has: a store restored from a backup is shown as it is', async () => {
    const p = page()
    expect(await p.send(Promise.resolve({ key: 'CND-12', rev: 10, title: 'before the restore' }))).toBe(true)
    const late = deferred<Shown>()
    const asked = p.send(late.promise)
    const list = deferred<string>()
    let rows = 'before'
    const listed = through(p.gate, 'list:CND?', () => list.promise, (v) => (rows = v))
    p.gate.reset()
    expect(p.gate.rev(p.thing)).toBe(0)
    expect(await p.send(Promise.resolve({ key: 'CND-12', rev: 3, title: 'restored' }))).toBe(true)
    late.resolve({ key: 'CND-12', rev: 11, title: 'asked before the restore' })
    expect(await asked).toBe(false)
    expect(p.shown.ticket).toEqual({ key: 'CND-12', rev: 3, title: 'restored' })
    // The next operation expects the rev the store has.
    expect(p.gate.rev(p.thing)).toBe(3)
    // A list, which has no rev, asked before it is dropped the same way.
    list.resolve('asked before the restore')
    expect(await listed).toBe(false)
    expect(rows).toBe('before')
  })
})

describe("a page's operations, one after another", () => {
  /** Every promise callback queued so far run: what a call can start on has started. */
  const settle = () => new Promise((resolve) => setTimeout(resolve, 0))
  it('start each call once the one before has settled, a failed one among them, each answering as its own', async () => {
    const started: string[] = []
    const gates = new Map<string, ReturnType<typeof deferred<string>>>()
    const op = serially(async (name: string) => {
      started.push(name)
      const d = deferred<string>()
      gates.set(name, d)
      return d.promise
    })
    const a = op('a')
    const b = op('b')
    const c = op('c')
    await settle()
    expect(started).toEqual(['a'])
    gates.get('a')!.resolve('a done')
    expect(await a).toBe('a done')
    await settle()
    expect(started).toEqual(['a', 'b'])
    gates.get('b')!.reject(new Error('b refused'))
    await expect(b).rejects.toThrow('b refused')
    await settle()
    expect(started).toEqual(['a', 'b', 'c'])
    gates.get('c')!.resolve('c done')
    expect(await c).toBe('c done')
  })
})

describe("an operation's catch-up, before the next operation is made", () => {
  /**
   * A ticket page after an edit committed rev 3, showing rev 2: its reads of the ticket answered by `answers` in turn (a rev taken, a
   * reply the gate dropped, or a failure), its waits recorded and passed at once, unless `during` acts in one (another read's success,
   * the page left).
   */
  function behind(answers: Array<number | 'dropped' | Error>, during?: (waits: number) => void) {
    const state = { rev: 2, left: false, reads: 0, waits: [] as number[] }
    const o: CatchUpOptions = {
      shown: () => state.rev >= 3,
      read: async () => {
        const a = answers[state.reads++]
        if (a === undefined) throw new Error('read more often than the test answers')
        if (a instanceof Error) throw a
        if (a === 'dropped') return false
        state.rev = a
        return true
      },
      left: () => state.left,
      wait: async (ms) => {
        state.waits.push(ms)
        during?.(state.waits.length)
      },
      firstMs: 250,
      maxMs: 1000,
    }
    return { state, o }
  }
  const failed = () => new Error('Cannot reach the server')

  it('reads nothing when the page shows what the operation left already: its reply was taken', async () => {
    const p = behind([])
    p.state.rev = 3
    expect(await catchUp(p.o)).toBe(true)
    expect(p.state.reads).toBe(0)
  })

  it('reads the ticket, and asks again while a read fails, the wait doubled up to the most, until one is shown', async () => {
    const p = behind([failed(), failed(), failed(), failed(), failed(), 3])
    expect(await catchUp(p.o)).toBe(true)
    expect(p.state.reads).toBe(6)
    expect(p.state.waits).toEqual([250, 500, 1000, 1000, 1000])
    expect(p.state.rev).toBe(3)
  })

  it('holds the edit queued behind it while a read fails, and that edit is made on the revision the read then shows', async () => {
    const p = behind([failed(), 3])
    const passes: Array<() => void> = []
    p.o.wait = (ms) => {
      p.state.waits.push(ms)
      return new Promise<void>((resolve) => passes.push(resolve))
    }
    // The page's edits, one after another: the first has answered, and catches up; the next is made on the revision shown then.
    const madeOn: number[] = []
    const edit = serially(async (which: 'first' | 'next') => {
      if (which === 'first') await catchUp(p.o)
      else madeOn.push(p.state.rev)
    })
    const first = edit('first')
    const next = edit('next')
    await new Promise((resolve) => setTimeout(resolve, 0))
    // The read failed: the next edit is not made on the rev 2 shown, which a failure taken for a read would have it made on.
    expect(p.state.reads).toBe(1)
    expect(madeOn).toEqual([])
    passes[0]!()
    await first
    await next
    expect(madeOn).toEqual([3])
  })

  it("stops once the page shows the ticket some other way while it waits: the re-read of the commit's event answered", async () => {
    const p = behind([failed()], () => (p.state.rev = 3))
    expect(await catchUp(p.o)).toBe(true)
    expect(p.state.reads).toBe(1)
  })

  it("reads again at once when the gate dropped a read's reply, until one is taken", async () => {
    const p = behind(['dropped', 'dropped', 3])
    expect(await catchUp(p.o)).toBe(true)
    expect(p.state.reads).toBe(3)
    expect(p.state.waits).toEqual([])
  })

  it('takes a read that answers below the revision committed (a store restored from a backup): the page shows the store as it is', async () => {
    const p = behind([1])
    expect(await catchUp(p.o)).toBe(true)
    expect(p.state.reads).toBe(1)
    expect(p.state.rev).toBe(1)
  })

  it('gives up, reading nothing more, once the page has left the ticket', async () => {
    const p = behind([failed(), failed()], (n) => {
      if (n === 2) p.state.left = true
    })
    expect(await catchUp(p.o)).toBe(false)
    expect(p.state.reads).toBe(2)
    // Left before anything was read: nothing is.
    const q = behind([])
    q.state.left = true
    expect(await catchUp(q.o)).toBe(false)
    expect(q.state.reads).toBe(0)
  })

  it('with no revision to wait for (a refusal), waits for a read taken, whatever it shows', async () => {
    const p = behind([failed(), 'dropped', 2])
    p.o.shown = () => false
    expect(await catchUp(p.o)).toBe(true)
    expect(p.state.reads).toBe(3)
    expect(p.state.waits).toEqual([250])
  })
})

describe('a read outside the reload, asked again', () => {
  it('until one answers, after the window doubled up to its cap, as a re-read is', async () => {
    const waits: number[] = []
    let reads = 0
    const ok = await retried({
      read: async () => {
        if (++reads < 5) throw new Error('down')
      },
      stop: () => false,
      wait: async (ms) => void waits.push(ms),
      firstMs: 250,
      maxMs: 1000,
    })
    expect(ok).toBe(true)
    expect(reads).toBe(5)
    expect(waits).toEqual([250, 500, 1000, 1000])
  })

  it('no more once it is no longer wanted: a later one took its place, or the page went', async () => {
    let reads = 0
    let wanted = true
    const ok = await retried({
      read: async () => {
        reads++
        throw new Error('down')
      },
      stop: () => !wanted,
      wait: async () => {
        if (reads === 2) wanted = false
      },
      firstMs: 250,
      maxMs: 8000,
    })
    expect(ok).toBe(false)
    expect(reads).toBe(2)
    // Not wanted from the start: nothing is read.
    expect(await retried({ read: async () => void reads++, stop: () => true, wait: async () => {}, firstMs: 1, maxMs: 1 })).toBe(false)
    expect(reads).toBe(2)
  })
})
