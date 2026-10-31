/**
 * The ordering gate every Dispatch page's state goes through (docs/plans/dispatch.md, "web/app"): the RunStore's rule (utils/runs.ts,
 * `take`) for each thing a page shows, a ticket by its key, a list by its filter, the project, the projects. Every request a page sends,
 * a read or an operation, is numbered as it is sent (`ask`); its reply is applied only when its number is above the number of the last
 * reply applied for the same thing (`take`), else dropped, so a slow reply never replaces a newer one. A request that fails is never
 * taken: it applies nothing and moves nothing, so a success it overtook still lands, and only a later applied reply supersedes an earlier
 * one. A reply carrying an entity whose `rev` is below the one shown is dropped whatever its number: the store's `rev` orders the states of
 * an entity, which the numbers, saying only when a request was sent, cannot (a list carries no `rev` and is taken by number alone). A thing
 * no longer shown is dropped (`drop`: a filter changed is a new thing), so its late replies are dropped, and the thing asked for again
 * starts afresh. A reply that the thing does not exist (a 404) is ordered as any other (`takeAbsent`), so a slow one never hides what a
 * later read found. A stream started again forgets everything (`reset`), since it may follow a store restored from a backup whose `rev`s
 * are below those shown. Pure: the pages hold one each, vitest proves it.
 */
export class DispatchOrder {
  /** The number of the last request asked: every request's number is above every number before it. */
  private last = 0
  /** The replies to requests numbered at or below this were asked before the last reset, every thing's. */
  private floor = 0
  /** By thing: the number of the last reply applied. */
  private readonly applied = new Map<string, number>()
  /** By thing: the `rev` of the entity shown, for a thing whose replies carry one. */
  private readonly revs = new Map<string, number>()
  /** By thing: the replies to requests numbered at or below this were asked before the thing was dropped. */
  private readonly floors = new Map<string, number>()

  /** The number of a request about to be sent. */
  ask(): number {
    return ++this.last
  }

  /**
   * Whether the reply to request `n` about `thing` is to be applied: its number above that of the last reply applied for the thing and
   * of every request asked before the thing was last dropped, and `rev`, the revision of the entity it carries (none for a list), not
   * below the one shown. When it is, it is recorded as the reply applied, its `rev` as the one shown.
   */
  take(thing: string, n: number, rev?: number): boolean {
    if (n <= this.floor || n <= (this.floors.get(thing) ?? 0) || n <= (this.applied.get(thing) ?? 0)) return false
    if (rev !== undefined && rev < (this.revs.get(thing) ?? 0)) return false
    this.applied.set(thing, n)
    if (rev !== undefined) this.revs.set(thing, rev)
    return true
  }

  /**
   * Whether the reply to request `n` saying that `thing` does not exist (a 404) is to be applied: by its number alone, as a list's, since
   * nothing that is absent has a `rev`. When it is, the `rev` shown is forgotten: nothing is shown, and the thing read again later is
   * taken at whatever `rev` it has then.
   */
  takeAbsent(thing: string, n: number): boolean {
    if (!this.take(thing, n)) return false
    this.revs.delete(thing)
    return true
  }

  /** The `rev` of the entity shown for `thing`, 0 when none was taken: what the next operation on it expects. */
  rev(thing: string): number {
    return this.revs.get(thing) ?? 0
  }

  /** Forgets a thing no longer shown: a reply to any request asked until now is dropped, and a request asked after it starts afresh. */
  drop(thing: string): void {
    this.floors.set(thing, this.last)
    this.applied.delete(thing)
    this.revs.delete(thing)
  }

  /**
   * Forgets every thing shown, for a stream started again (a `snapshot`), which may follow a store restored from a backup whose `rev`s
   * are below those shown: a reply to any request asked until now is dropped, and every thing read after it is taken at whatever `rev`
   * it has, so the page shows the store as it is and its next operation expects the `rev` the store has.
   */
  reset(): void {
    this.floor = this.last
    this.floors.clear()
    this.applied.clear()
    this.revs.clear()
  }
}

/**
 * Sends one request about `thing` through the gate: numbered as it is sent, its reply applied (`apply`) only when the gate takes it, the
 * entity's revision read from the reply by `rev` (none for a list). Resolves to whether the reply was applied; a request that fails
 * rejects with its error and applies nothing.
 */
export async function through<T>(gate: DispatchOrder, thing: string, send: () => Promise<T>, apply: (reply: T) => void, rev?: (reply: T) => number | undefined): Promise<boolean> {
  const n = gate.ask()
  const reply = await send()
  if (!gate.take(thing, n, rev?.(reply))) return false
  apply(reply)
  return true
}

/** How `throughOrAbsent` applies a reply, and tells and applies that the thing does not exist. */
export interface AbsentOptions<T> {
  /** Applies the reply, when the gate takes it. */
  apply: (reply: T) => void
  /** The revision of the entity the reply carries (none for a list). */
  rev?: (reply: T) => number | undefined
  /** Whether a failure says the thing does not exist (a 404). */
  absent: (e: unknown) => boolean
  /** Shows that it does not, when the gate takes that answer. */
  gone: () => void
}

/**
 * `through`, for a thing that may not exist (a ticket's key no ticket has yet, a project's): a failure that `absent` says is no such thing
 * answers that it does not, ordered as any reply (`takeAbsent`), so `gone` is applied only when the gate takes it: a slow 404 never hides
 * what a later read found, and a later read's thing replaces it. Any other failure rejects with its error and applies nothing.
 */
export async function throughOrAbsent<T>(gate: DispatchOrder, thing: string, send: () => Promise<T>, o: AbsentOptions<T>): Promise<boolean> {
  const n = gate.ask()
  let reply: T
  try {
    reply = await send()
  } catch (e) {
    if (!o.absent(e)) throw e
    if (!gate.takeAbsent(thing, n)) return false
    o.gone()
    return true
  }
  if (!gate.take(thing, n, o.rev?.(reply))) return false
  o.apply(reply)
  return true
}

/** How `catchUp` reads a thing until the page shows it as an operation on it left it, and when it stops. */
export interface CatchUpOptions {
  /** Whether the page shows the thing as the operation left it, or later: the revision it committed, or a later one, is shown. */
  shown: () => boolean
  /** Reads the thing, sent now, through the gate: resolves to whether the gate took its reply (`through`); rejects when it failed. */
  read: () => Promise<boolean>
  /** Whether the page has left the thing (it shows another one): nothing more is read. */
  left: () => boolean
  /** Waits that many milliseconds before a read failed is asked again. */
  wait: (ms: number) => Promise<void>
  /** The first wait, doubled after each failure up to `maxMs`: a re-read's (useDispatchLive), the window and DISPATCH_RETRY_MAX_MS. */
  firstMs: number
  maxMs: number
}

/**
 * Waits until the page shows a thing as an operation on it left it, before the next operation on it is made (a ticket page's edits, one
 * after another), and answers whether it does. At once when it shows it already (the operation's reply was taken); else the thing is
 * read, sent now, after the operation answered, so the read finds what it left: once the gate takes a read's reply, the page shows the
 * thing as the store held it then. A read that fails is asked again after `firstMs`, doubled each time up to `maxMs`, as a re-read is,
 * until one is taken or the page shows the thing some other way (the re-read of the operation's event); a read the gate dropped (a later
 * reply taken first, or the gate reset after it was asked) is asked again at once, the next check deciding. A failure never stands for
 * the read: the next operation, judged on the older state shown, would expect its revision and be refused, or be taken for no change and
 * not be sent at all (a pick taken back to what the older state holds), and the one before it stand. False once the page has left the
 * thing, with nothing more read.
 */
export async function catchUp(o: CatchUpOptions): Promise<boolean> {
  let delay = o.firstMs
  for (;;) {
    if (o.left()) return false
    if (o.shown()) return true
    let taken: boolean
    try {
      taken = await o.read()
    } catch {
      await o.wait(delay)
      delay = Math.min(delay * 2, o.maxMs)
      continue
    }
    if (taken) return true
  }
}

/** How `retried` reads, and when it stops. */
export interface RetriedOptions {
  /** The read, sent now: resolves once it answered; rejects when it failed. */
  read: () => Promise<unknown>
  /** Whether it is no longer wanted (a later one took its place, the page shows another thing, or is gone): nothing more is read. */
  stop: () => boolean
  /** Waits that many milliseconds before a read failed is asked again. */
  wait: (ms: number) => Promise<void>
  /** The first wait, doubled after each failure up to `maxMs`: a re-read's (useDispatchLive), the window and DISPATCH_RETRY_MAX_MS. */
  firstMs: number
  maxMs: number
}

/**
 * A read made outside the live store's reload (a ticket's earlier comments, read again with each read of the ticket) asked again as a
 * re-read is: after `firstMs`, doubled up to `maxMs`, until one answers or it is no longer wanted. Answers whether one answered. A
 * failure never stands for the read, which would leave what is shown as it was, with no event to read it again.
 */
export async function retried(o: RetriedOptions): Promise<boolean> {
  let delay = o.firstMs
  for (;;) {
    if (o.stop()) return false
    try {
      await o.read()
      return true
    } catch {
      if (o.stop()) return false
      await o.wait(delay)
      delay = Math.min(delay * 2, o.maxMs)
    }
  }
}

/**
 * `f` made serial: each call starts once every call before it has settled, answered or failed, and resolves or rejects as its own call
 * does. A page's operations on one entity go so (DispatchTicketPage), each expecting the revision the one before it left, where two sent
 * at once would expect the same revision and the second be refused stale_revision by the first.
 */
export function serially<A extends unknown[], R>(f: (...args: A) => Promise<R>): (...args: A) => Promise<R> {
  let last: Promise<unknown> = Promise.resolve()
  return (...args: A) => {
    const next = last.then(() => f(...args))
    last = next.catch(() => {})
    return next
  }
}
