import { CommandRefused, mintCommandId, sendCommand } from '~/utils/tasks'
import { closeRefusalWords, runLinkedWords } from '~/utils/dispatch'
import { catchUp, editEach, through, type DispatchOrder } from '~/utils/dispatchOrder'
import { RUN_EVENT_WINDOW_MS } from '~/utils/runs'
import { ApiError } from './useApi'
import { DISPATCH_RETRY_MAX_MS } from './useDispatchLive'
import type { DispatchReceipt, DispatchTicket, OpBody, TicketEdit, TicketReply, TicketSummary } from './useDispatch'

/** How one operation is sent and what becomes of its reply. */
export interface DispatchOpOptions<R> {
  /** The thing of the page's gate the reply's entity is (`ticket:CND-12`): the reply is numbered with the page's reads. */
  thing: string
  /** The operation in the toasts' words: `CND-12`, `New project`. */
  what: string
  /** The revision of the entity the page shows: the operation is refused stale_revision once the store's has moved on. None for a create. */
  expectedRev?: number
  /** Applies the reply's entity, when the gate takes it. */
  apply?: (reply: R) => void
  /** The revision of the entity the reply carries, for the gate. */
  rev?: (reply: R) => number | undefined
  /** A toast's title once it is done; none when the page shows what changed. */
  done?: string
  /** Called after a refusal with its receipt, for the page to read what the store holds now (a stale revision: someone else's edit came first). */
  refused?: (receipt: DispatchReceipt) => void
  /** Throw a failure to the caller (a dialog that shows it in place) instead of a toast. */
  raise?: boolean
}

/** What became of several tickets' edits: the keys done (or holding the value already, which needs no operation), and the others with why. */
export interface EditsOutcome {
  done: string[]
  failed: Array<{ key: string; words: string }>
  /** The page was left before it showed what an edit did: the edits after it were not sent, nor are those queued behind (editEach). */
  stranded: boolean
}

/** How `editTickets` shows what it did. */
export interface EditsOptions {
  /** Shows a ticket an edit answered with, once the gate takes it (a list's row patched, a cache's). */
  apply?: (t: DispatchTicket) => void
  /** The toast's title once every edit is done, from the keys; none when the page shows what changed. */
  done?: (keys: string[]) => string
  /** Called once after the edits when any was refused, for the page to read what the store holds now; the tickets refused are read first. */
  refused?: () => void
  /** Whether the page that made the edits is gone: a ticket's read that fails is not asked again for it. */
  left?: () => boolean
  /** Whether an earlier call's edits were stranded (its outcome's `stranded`): none of these is sent, each failing as not sent. */
  stranded?: () => boolean
}

/** A refusal's receipt in words: the close policy's and a run held by another ticket in their own, else `fallback` (the server's words). */
export function refusalWords(r: DispatchReceipt, fallback: string): string {
  if (r.refusal === 'close_policy') return closeRefusalWords(r.missing ?? [])
  if (r.refusal === 'run_linked') return runLinkedWords(r.key)
  return fallback
}

/** A failed operation in words: a refusal's (refusalWords, the server's words for the rest), else the error's. */
export function dispatchFailureWords(e: unknown): string {
  if (e instanceof CommandRefused) return refusalWords(e.receipt as DispatchReceipt, e.message)
  return (e as Error).message
}

/**
 * Dispatch's operations from a page (docs/plans/dispatch.md, "web/app"): each with an op id the browser mints, the revision of the entity
 * the page shows as its `expectedRev` and the display name as its `byName`; a reply lost to the network asked for once more with the same
 * id (sendCommand of utils/tasks.ts, as a task's commands are sent); a refusal read from its receipt; the reply's entity through the
 * page's ordering gate, numbered as the operation is sent, so its `rev` becomes the `expectedRev` of the next operation and a read the
 * page sent later wins over it; the outcome in a toast in the task commands' words. `busy` names the thing an operation is under way on.
 */
export function useDispatchOps(gate: DispatchOrder) {
  const toast = useToast()
  const identity = useIdentity()
  const busy = ref('')

  /** Sends one operation; resolves to its reply, or null when it failed (said in a toast, unless `raise` throws it). */
  async function run<R extends { receipt: DispatchReceipt }>(send: (body: OpBody) => Promise<R>, o: DispatchOpOptions<R>): Promise<R | null> {
    const n = gate.ask()
    const body: OpBody = { opId: mintCommandId() }
    if (o.expectedRev) body.expectedRev = o.expectedRev
    const name = identity.name.value.trim()
    if (name) body.byName = name
    busy.value = o.thing
    try {
      const reply = await sendCommand<OpBody, R>(send, body)
      if (o.apply && gate.take(o.thing, n, o.rev?.(reply))) o.apply(reply)
      if (o.done) toast.add({ title: o.done, description: reply.receipt.repeat ? 'It was done already.' : undefined, icon: 'i-lucide-check', color: 'success' })
      return reply
    } catch (e) {
      if (e instanceof CommandRefused) o.refused?.(e.receipt as DispatchReceipt)
      if (o.raise) throw e
      toast.add({ title: `${o.what}: not changed`, description: dispatchFailureWords(e), icon: 'i-lucide-triangle-alert', color: e instanceof CommandRefused ? 'warning' : 'error' })
      return null
    } finally {
      if (busy.value === o.thing) busy.value = ''
    }
  }

  const api = useDispatchApi()

  /**
   * Edits tickets one after another, one operation each (a pick over the tickets selected is an operation per ticket; editEach of
   * utils/dispatchOrder.ts): each expects the revision of the ticket shown, or the one an earlier answer of this page carried for it when
   * that is later (a list read before that answer may still show the older row), so a second pick made on the same ticket is not refused
   * stale_revision by the first. `make` gives each ticket its edit, null when it holds the value already, which is no operation (the store
   * refuses an edit that changes nothing). Each answer goes through the page's gate as the ticket's (`ticket:<key>`), where `apply` shows
   * it; an answer that carried no ticket, or that the gate dropped, and a failure (a refusal, or both replies lost to the network, which
   * may follow a commit), are followed by a read of the ticket through the same gate, asked again while it fails, before the next edit is
   * made, so the edits queued behind are made on what the store holds; a page left before it shows that makes none of them. A
   * refusal's read of the page (`refused`) is asked once for the batch. One toast says what was not changed and why, in the refusal's
   * words (the close policy's own), and `done`'s title says what was, when given.
   */
  async function editTickets(targets: readonly TicketSummary[], make: (t: TicketSummary) => TicketEdit | null, o: EditsOptions = {}): Promise<EditsOutcome> {
    /** Reads the ticket through the gate: true once taken; a ticket gone (a 404) has nothing more to show, and is taken for read. */
    async function readTicket(key: string): Promise<boolean> {
      try {
        return await through(gate, `ticket:${key}`, () => api.ticket(key), (t) => o.apply?.(t), (t) => t.rev)
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return true
        throw e
      }
    }
    const each = await editEach<TicketSummary, TicketEdit, TicketReply>(targets, {
      make,
      send: async (t, change) => {
        const thing = `ticket:${t.key}`
        const reply = await run<TicketReply>((body) => api.editTicket(t.key, { ...body, ...change }), {
          thing,
          what: t.key,
          expectedRev: Math.max(t.rev, gate.rev(thing)),
          apply: (r) => {
            if (r.ticket) o.apply?.(r.ticket)
          },
          rev: (r) => r.ticket?.rev,
          raise: true,
        })
        return reply!
      },
      committed: (r) => r.ticket?.rev ?? r.receipt.rev,
      caughtUp: (t, rev) =>
        catchUp({
          shown: () => rev !== undefined && gate.rev(`ticket:${t.key}`) >= rev,
          read: () => readTicket(t.key),
          left: o.left ?? (() => false),
          wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
          firstMs: RUN_EVENT_WINDOW_MS,
          maxMs: DISPATCH_RETRY_MAX_MS,
        }),
      refusal: (e) => e instanceof CommandRefused,
      refused: o.refused,
      stranded: o.stranded,
    })
    const out: EditsOutcome = { done: each.done.map((t) => t.key), failed: each.failed.map((f) => ({ key: f.t.key, words: dispatchFailureWords(f.error) })), stranded: each.stranded }
    if (out.failed.length === 1 && targets.length === 1) {
      toast.add({ title: `${out.failed[0]!.key}: not changed`, description: out.failed[0]!.words, icon: 'i-lucide-triangle-alert', color: 'warning' })
    } else if (out.failed.length) {
      toast.add({ title: `${out.failed.length} of ${targets.length} not changed`, description: out.failed.map((f) => `${f.key}: ${f.words}`).join(' · '), icon: 'i-lucide-triangle-alert', color: 'warning' })
    }
    if (out.done.length && o.done) toast.add({ title: o.done(out.done), icon: 'i-lucide-check', color: 'success' })
    return out
  }

  return { busy, run, editTickets }
}
