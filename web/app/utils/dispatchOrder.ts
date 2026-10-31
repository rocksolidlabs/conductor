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
  /** Whether the page has left the thing (it shows another one): nothing more is read, unless it shows the thing as the operation left it. */
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
 * thing before it showed it, with nothing more read: what the page shows may not be what the store holds, and the edits queued behind
 * are not made on it (editEach's `stranded`). A page left once it shows it answers true: the edits queued behind are made on what it shows.
 */
export async function catchUp(o: CatchUpOptions): Promise<boolean> {
  let delay = o.firstMs
  for (;;) {
    if (o.shown()) return true
    if (o.left()) return false
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

/** How `editEach` sends one ticket's edit, waits until the page shows what it left, and tells a refusal from another failure. */
export interface EachOptions<T, C, R> {
  /** The edit of a ticket: null when it holds the value already, which is no operation (the store refuses an edit that changes nothing). */
  make: (t: T) => C | null
  /** Sends one ticket's edit as one operation: resolves to its reply, rejects with the refusal or the failure. */
  send: (t: T, change: C) => Promise<R>
  /** The revision an answer committed: its ticket's, else its receipt's (a reply whose read after the commit failed carries its receipt alone). */
  committed: (reply: R) => number | undefined
  /**
   * Waits until the page shows the ticket at `rev` or later (catchUp), or, with no `rev` (after a failure), once a read of it sent now is
   * shown: the next edit of it is made on what the store holds. Resolves to false when the page was left before it showed it (catchUp).
   */
  caughtUp: (t: T, rev: number | undefined) => Promise<boolean>
  /** Whether a failure is the store's refusal (a stale revision, the close policy), which says the page shows something the store has not. */
  refusal: (e: unknown) => boolean
  /** Called once after the edits when any was refused, for the page to read what the store holds now beside the tickets refused. */
  refused?: () => void
  /**
   * Whether an edit before these, of an earlier call, left the page showing what may not be the store's (`stranded` of its outcome): none
   * of these is made, each failing as not sent (NotSent), rather than made on that and taken for no change.
   */
  stranded?: () => boolean
}

/** What became of each ticket's edit: done (or holding the value already), or failed with its error. */
export interface EachOutcome<T> {
  done: T[]
  failed: Array<{ t: T; error: unknown }>
  /**
   * The page was left before it showed what an edit did (a catch-up given up): the edits after it were not made, each failed as not
   * sent, and those queued behind are not to be made either (EachOptions' `stranded`).
   */
  stranded: boolean
}

/** An edit not sent: the page was left before it showed what the edit before it did, which the edit would have been made on. */
export class NotSent extends Error {
  constructor() {
    super('Not sent: the page was left before it showed what the edit before this one did.')
    this.name = 'NotSent'
  }
}

/**
 * Edits tickets one after another, one operation each (a pick over the tickets selected, a card moved): each is sent once the one before
 * has answered, and the next is not made until the page shows what the one before left (`caughtUp`), as a ticket page's edits are
 * (DispatchTicketPage.vue): at the revision it committed, read again when its reply carried none (the server's read after the commit
 * failed) or the gate dropped it. A failure reads the ticket again before the next edit, a refusal and any other alike: a failure that is
 * no refusal (both replies of an operation lost to the network) may follow a commit, so the store may hold the edit or not, and the edit
 * queued behind on the same ticket is made on what it holds and expects its revision: a card moved and moved back on a reply that showed
 * nothing is moved back, not taken for no change. The page's own read after a refusal (`refused`) is asked once, however many were
 * refused. A page left before it showed what an edit did makes none of the edits after it (`stranded`): each fails as not sent.
 */
export async function editEach<T, C, R>(targets: readonly T[], o: EachOptions<T, C, R>): Promise<EachOutcome<T>> {
  const out: EachOutcome<T> = { done: [], failed: [], stranded: o.stranded?.() ?? false }
  let refusals = 0
  for (const t of targets) {
    if (out.stranded) {
      out.failed.push({ t, error: new NotSent() })
      continue
    }
    const change = o.make(t)
    if (!change) {
      out.done.push(t)
      continue
    }
    let reply: R
    try {
      reply = await o.send(t, change)
    } catch (e) {
      out.failed.push({ t, error: e })
      if (o.refusal(e)) refusals++
      if (!(await o.caughtUp(t, undefined))) out.stranded = true
      continue
    }
    out.done.push(t)
    if (!(await o.caughtUp(t, o.committed(reply)))) out.stranded = true
  }
  if (refusals) o.refused?.()
  return out
}
