import { onBeforeUnmount, onMounted, reactive } from 'vue'
import { decideWords, isDecisionId, replaces, respondOutcome } from '~/utils/decisions'
import { DECIDE_CARDS, DECIDE_FRESH_FOR, DECIDE_READ_EVERY, DECIDE_READ_WAIT, GRANT_DECIDE, MAX_DECIDE_LIST, MAX_DECIDE_LIST_ENTRIES, MAX_DECIDE_REPLY, MAX_DECIDE_VIEW, MAX_ME_REPLY } from '~/utils/protocol'
import type { DecideEntry, DecideReceipt, DecideView, MeReply } from '~/utils/protocol'
import { mintCommandId } from '~/utils/tasks'

// The decide page's client and state (docs/plans/decide-grant.md, "web/app"), page-owned: who the pass is (`GET /api/me`), the decide
// list (`GET /api/me/decisions`), at most DECIDE_CARDS cards open, each read (`GET /api/decisions/{did}`) and answered
// (`POST /api/decisions/{did}/respond`) with the pass in the Authorization header, the one request it is ever sent in (`call`). The
// list and each open card read one at a time, the next due DECIDE_READ_EVERY after the last began while the tab is shown and at once
// when it is shown again, each aborted at DECIDE_READ_WAIT; one answer is on its way at a time for the whole page. A new answer is sent
// only on a view a read renewed in the last DECIDE_FRESH_FOR on both the steady clock (`env.now`) and the wall clock (`env.wall`); an
// answer whose outcome is not known is sent again whatever the view's age. A 401 ends the page and the pass it was sent with, wherever
// it arrives; a card's 404 closes that card alone. A read whose own signal was aborted (Close, Dismiss, the card's end, stop) says
// nothing. What belongs to a card (its reads, its freshness timer, its send) is kept by the card
// object, and a reply acts only on a card that is still the page's. Once the page stops, every request on its way is aborted and no
// reply changes it. Drafts and the answer sent stay in the page's memory, never in the browser's storage.

export interface DecideDraft {
  choice?: number
  text: string
  rationale: string
}

/** What the page keeps of one decision it has drawn. */
export interface DecideCard {
  id: string
  /** The view shown: the highest rev a reply taken brought. */
  view: DecideView | null
  draft: DecideDraft
  /** The body of the send whose outcome is not known yet, frozen as sent ('' for none). */
  sent: string
  sending: boolean
  /** The words of a send that had no definite answer ('' for none): Send again sends `sent` unchanged. */
  unknown: string
  /** The words of the last send's definite refusal. */
  refused: string
  /** An end for this person: its answer's final outcome, no longer open to it (404), or its reads refused (403). */
  end: '' | 'applied' | 'not_used' | 'gone' | 'refused'
  open: boolean
  /** When the last read whose reply the card took began, on each clock (0: none yet), and whether that is too long ago to answer on. */
  freshAt: number
  freshWall: number
  suspended: boolean
  /** Whether the last list read held it. */
  listed: boolean
  /** A read failed and none has succeeded yet. */
  failed: boolean
}

/** What the page reads its time and its tab's visibility from: the browser's, or a test's. */
export interface DecideEnv {
  fetch: typeof fetch
  /** `end` is told the pass a 401 refused. */
  pass: { get(): string; end(refused: string): void }
  base: string
  doc: Pick<Document, 'visibilityState' | 'addEventListener' | 'removeEventListener'>
  win: Pick<Window, 'addEventListener' | 'removeEventListener'>
  /** The steady clock, in ms (`performance.now()`): the schedule's, and with `wall` the freshness window's. */
  now: () => number
  /** The wall clock, in ms (`Date.now()`), which goes on while a device sleeps. */
  wall: () => number
}

type Reply = { status: number; body?: any } // eslint-disable-line @typescript-eslint/no-explicit-any

/** A reply's body read through its stream, stopped once it passes max bytes: null then, never held whole and never parsed. */
async function upTo(res: Response, max: number): Promise<string | null> {
  if (!res.body) return ''
  const reader = res.body.getReader()
  const dec = new TextDecoder()
  let n = 0
  let text = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) return text + dec.decode()
    n += value.byteLength
    if (n > max) {
      void reader.cancel().catch(() => {})
      return null
    }
    text += dec.decode(value, { stream: true })
  }
}

/** One read at a time on its own schedule: due EVERY after the last began; one due while one is out, or while hidden, waits. */
class Reads {
  private out?: AbortController
  private due = false
  private stopped = true
  private began = 0
  private timer?: ReturnType<typeof setTimeout>
  constructor(
    private read: (signal: AbortSignal) => Promise<void>,
    private visible: () => boolean,
    private now: () => number,
    private track: (p: Promise<void>) => void,
  ) {}
  start() {
    this.stopped = false
    this.run()
  }
  /** The one place a read starts: none once stopped, none while one is out or the page is hidden (it stays due until shown). */
  run() {
    if (this.stopped) return
    if (this.out || !this.visible()) {
      this.due = true
      return
    }
    clearTimeout(this.timer)
    this.due = false
    const ac = (this.out = new AbortController())
    this.began = this.now()
    this.track(
      this.read(ac.signal).finally(() => {
        if (this.out !== ac) return
        this.out = undefined
        if (this.stopped) return
        if (this.due) this.run()
        else this.timer = setTimeout(() => this.run(), Math.max(0, this.began + DECIDE_READ_EVERY - this.now()))
      }),
    )
  }
  /** Stops the schedule and aborts the read on its way. */
  stop() {
    this.stopped = true
    this.due = false
    clearTimeout(this.timer)
    this.out?.abort()
    this.out = undefined
  }
}

const emptyDraft = (d: DecideDraft) => d.choice === undefined && !d.text.trim() && !d.rationale.trim()
const isObject = (x: unknown): x is Record<string, unknown> => typeof x === 'object' && x !== null && !Array.isArray(x)
const isText = (x: unknown) => x === undefined || typeof x === 'string'
const STRINGS = ['state', 'type', 'question', 'context', 'uncertainty', 'authorizes', 'createdAt', 'outcome', 'outcomeCode']
/** The one check of a view: decision `id`, and every field the card draws or a send reads of the type the protocol names. */
const isView = (v: unknown, id: string): v is DecideView =>
  isObject(v) &&
  v.id === id &&
  Number.isInteger(v.rev) &&
  STRINGS.every((k) => isText(v[k])) &&
  (v.recommendation === undefined || Number.isInteger(v.recommendation)) &&
  (v.options === undefined || (Array.isArray(v.options) && v.options.every((o) => isObject(o) && typeof o.label === 'string' && isText(o.tradeoffs)))) &&
  Array.isArray(v.own) &&
  v.own.every((r) => isObject(r) && typeof r.responseId === 'string' && typeof r.status === 'string' && isText(r.code))
/** A read's definite refusal: a 403 with a code (a server that serves no decisions to this pass). */
const refusal = (r: Reply) => r.status === 403 && typeof r.body?.error?.code === 'string'

export function createDecide(env: DecideEnv, did?: string) {
  const page = reactive({
    me: null as MeReply | null,
    /** The pass is not a decide pass (it holds admin, or lacks decide). */
    notForIt: false,
    /** Access ended: a 401, or no pass at all. */
    ended: !env.pass.get(),
    /** Reading who the pass is failed and none has succeeded yet. */
    meFailed: false,
    /** `failed`: a list read failed and none has succeeded yet; `refused`: the server refused who the pass is or the list (403), read no more. */
    list: { entries: [] as DecideEntry[], cut: false, loaded: false, failed: false, refused: false, freshAt: 0, freshWall: 0, suspended: true },
    cards: [] as DecideCard[],
  })
  let stopped = false
  const aborts = new Set<() => void>()
  const pending = new Set<Promise<void>>()
  const track = (p: Promise<void>) => {
    pending.add(p)
    void p.finally(() => pending.delete(p))
  }
  const visible = () => env.doc.visibilityState === 'visible'
  // Kept by the card object (the list's by page.list), never found again by a decision's id.
  const reads = new Map<object, Reads>()
  const sends = new Map<object, AbortController>()
  const freshTimers = new Map<object, ReturnType<typeof setTimeout>>()
  /** Whether the page goes on: not stopped and its access not ended. Read after every await. */
  const alive = () => !stopped && !page.ended
  /** Whether c is still the page's card: one dismissed is not, even when its decision was opened again. Read after every await. */
  const mine = (c: DecideCard) => page.cards.includes(c)
  /** Whether a read's reply may still change a card: the page alive, the card the page's own, open and not at an end. */
  const cardOpen = (c: DecideCard) => alive() && mine(c) && c.open && !c.end

  /**
   * The one request the pass is sent in: to one of the four routes, in the Authorization header, aborted at DECIDE_READ_WAIT, at
   * `signal`, or when the page stops. A 401 ends the page here, before its body is read and before any caller sees the reply.
   */
  async function call(method: 'GET' | 'POST', path: '/api/me' | '/api/me/decisions' | `/api/decisions/${string}`, max: number, body?: string, signal?: AbortSignal): Promise<Reply> {
    const pass = env.pass.get()
    if (!pass) end(pass)
    if (!alive()) return { status: 0 }
    const ac = new AbortController()
    const abort = () => ac.abort()
    signal?.addEventListener('abort', abort)
    aborts.add(abort)
    const t = setTimeout(abort, DECIDE_READ_WAIT)
    try {
      const headers: Record<string, string> = { Authorization: `Bearer ${pass}` }
      if (body !== undefined) headers['Content-Type'] = 'application/json'
      const res = await env.fetch(env.base + path, { method, headers, body, signal: ac.signal, cache: 'no-store', redirect: 'error' })
      if (res.status === 401) {
        void res.body?.cancel().catch(() => {})
        end(pass)
        return { status: 401 }
      }
      const text = await upTo(res, max)
      if (text === null) return { status: 0 }
      try {
        return { status: res.status, body: JSON.parse(text) }
      } catch {
        return { status: res.status }
      }
    } catch {
      return { status: 0 }
    } finally {
      clearTimeout(t)
      aborts.delete(abort)
      signal?.removeEventListener('abort', abort)
    }
  }

  /**
   * Whether a mark (a card's, the list's) is too old to answer or open on: fresh only while the time passed since it on each clock is
   * between zero and DECIDE_FRESH_FOR; judged again when it turns so.
   */
  function fresh(o: { freshAt: number; freshWall: number; suspended: boolean }) {
    const steady = env.now() - o.freshAt
    const wall = env.wall() - o.freshWall
    o.suspended = !o.freshAt || !(steady >= 0 && steady <= DECIDE_FRESH_FOR && wall >= 0 && wall <= DECIDE_FRESH_FOR)
    clearTimeout(freshTimers.get(o))
    if (!o.suspended && !stopped) freshTimers.set(o, setTimeout(() => fresh(o), DECIDE_FRESH_FOR - Math.max(steady, wall) + 1))
  }

  /** Whether the list may open a card: judged on both clocks at this moment, by Open and by the list's own opening alike. */
  function listFresh() {
    fresh(page.list)
    return !page.list.suspended
  }

  /** The words beside an answer held: "send it again" only where Send again can be offered for it. */
  const heldWords = (c: DecideCard, code = 'decision_moved') => (c.end === 'gone' || page.ended ? `Whether your answer to ${c.id} was recorded cannot be learned here.` : decideWords(code, c.id))

  /** The page ended at a 401 to a request sent with `refused` (or with no pass): the pass store ends that pass and no other. */
  function end(refused: string) {
    if (page.ended) return
    page.ended = true
    env.pass.end(refused)
    for (const r of reads.values()) r.stop()
    for (const t of freshTimers.values()) clearTimeout(t)
    for (const c of page.cards) if (c.sent) c.unknown = heldWords(c)
  }

  const card = (id: string) => page.cards.find((c) => c.id === id)
  const openCount = () => page.cards.filter((c) => c.open && !c.end).length

  function stopCard(c: DecideCard, why: DecideCard['end']) {
    c.end = why
    reads.get(c)?.stop()
    if (c.sent && why === 'gone') c.unknown = heldWords(c)
  }

  /** A reply's receipt of the send under way settles it: accepted, or refused in its code's words. */
  function settle(c: DecideCard, rc: DecideReceipt) {
    c.sent = ''
    c.unknown = ''
    c.refused = rc.status === 'refused' ? decideWords(rc.code ?? '', c.id) : ''
  }

  /** The moment a read begins, on both clocks. */
  const mark = () => ({ freshAt: env.now(), freshWall: env.wall() })

  /** The one order every reply passes: a read's (whole) or a respond's; `at` the moment a read began, which only a read moves. */
  function take(c: DecideCard, v: DecideView, whole: boolean, at?: ReturnType<typeof mark>) {
    if ((whole && c.end) || !replaces(c.view, v, whole)) return
    c.view = v
    if (at) {
      Object.assign(c, at, { failed: false })
      fresh(c)
    }
    const sent = c.sent ? (JSON.parse(c.sent) as { responseId: string }).responseId : ''
    const rc = sent ? v.own.find((r) => r.responseId === sent) : undefined
    if (rc) settle(c, rc)
    if (v.outcome === 'applied' || v.outcome === 'not_used') stopCard(c, v.outcome)
  }

  async function readCard(c: DecideCard, signal: AbortSignal) {
    const at = mark()
    const r = await call('GET', `/api/decisions/${c.id}`, MAX_DECIDE_VIEW, undefined, signal)
    if (signal.aborted || !cardOpen(c)) return
    if (r.status === 404) stopCard(c, 'gone')
    else if (refusal(r)) stopCard(c, 'refused')
    else if (r.status === 200 && isView(r.body, c.id)) take(c, r.body, true, at)
    else c.failed = !c.view
  }

  function openCard(id: string, listed: boolean) {
    let c = card(id)
    if (!c) {
      c = reactive<DecideCard>({ id, view: null, draft: { text: '', rationale: '' }, sent: '', sending: false, unknown: '', refused: '', end: '', open: true, freshAt: 0, freshWall: 0, suspended: true, listed, failed: false })
      page.cards.push(c)
      const cc = c
      reads.set(c, new Reads((s) => readCard(cc, s), visible, env.now, track))
    }
    c.open = true
    reads.get(c)!.start()
  }

  /** A read of who the pass is or of the list that did not succeed: refused (403), it reads no more; else it is tried again. */
  function listFailed(r: Reply, what: 'meFailed' | 'list') {
    if (refusal(r)) {
      page.list.refused = true
      reads.get(page.list)!.stop()
    } else if (what === 'meFailed') page.meFailed = true
    else page.list.failed = !page.list.loaded
  }

  async function readList(signal: AbortSignal) {
    if (!page.me) {
      const r = await call('GET', '/api/me', MAX_ME_REPLY, undefined, signal)
      if (signal.aborted || !alive()) return
      if (r.status !== 200 || !Array.isArray(r.body?.grants)) return listFailed(r, 'meFailed')
      page.me = r.body as MeReply
      page.meFailed = false
      if (!page.me.grants.includes(GRANT_DECIDE) || page.me.grants.includes('admin')) {
        page.notForIt = true
        return reads.get(page.list)!.stop()
      }
      if (isDecisionId(did)) openCard(did, false)
      // Hidden meanwhile: the list's request waits, due, until the page is shown.
      if (!visible()) return reads.get(page.list)!.run()
    }
    const at = mark()
    const r = await call('GET', '/api/me/decisions', MAX_DECIDE_LIST, undefined, signal)
    if (signal.aborted || !alive()) return
    if (r.status !== 200 || !Array.isArray(r.body?.decisions) || r.body.decisions.length > MAX_DECIDE_LIST_ENTRIES) return listFailed(r, 'list')
    const entries = (r.body.decisions as DecideEntry[]).filter((e) => isDecisionId(e?.id) && Number.isInteger(e.rev))
    Object.assign(page.list, { entries, cut: r.body.cut === true, loaded: true, failed: false }, at)
    fresh(page.list)
    for (const c of page.cards) {
      if (!c.open || c.end) continue
      const e = entries.find((x) => x.id === c.id)
      // A listed rev other than the view's, or a decision that left the list since the last read, reads the card at once.
      if ((e && c.view && e.rev !== c.view.rev) || (!e && c.listed)) reads.get(c)?.run()
      c.listed = !!e
    }
    for (const e of entries) if (openCount() < DECIDE_CARDS && !card(e.id) && listFresh()) openCard(e.id, true)
  }
  reads.set(page.list, new Reads(readList, visible, env.now, track))

  /** The tab shown again, or focused: everything due read at once, and every mark judged now. */
  function shown() {
    if (!visible()) return
    for (const r of reads.values()) r.run()
    for (const c of page.cards) if (c.freshAt) fresh(c)
    if (page.list.freshAt) fresh(page.list)
  }

  /** Whether a card may be closed: no draft and no answer whose outcome is not known; its state is kept. */
  const closable = (c: DecideCard) => c.open && !c.end && emptyDraft(c.draft) && !c.sent

  /**
   * Whether a card's answer may be sent now, the one rule of Send and Send again: the page alive, the card its own, nothing of the page's
   * being sent; then an answer held (Send again) whenever its decision has not answered 404 to this pass, whatever a read showed since;
   * a new one only on a fresh view of an open decision, the card open.
   */
  function answerable(c: DecideCard): boolean {
    if (!alive() || !mine(c) || page.cards.some((x) => x.sending)) return false
    if (c.sent) return c.end !== 'gone'
    return c.open && !c.end && !c.suspended && c.view?.state === 'open'
  }

  /** Sends the card's answer: `sent` again, unchanged, when its outcome is not known; else the draft, frozen under a new response id. */
  async function send(id: string) {
    const c = card(id)
    if (c?.freshAt) fresh(c)
    if (!c || !answerable(c)) return
    if (!c.sent) {
      const d = c.draft
      const b: Record<string, unknown> = { responseId: mintCommandId() }
      if (d.choice !== undefined) b.choice = d.choice
      if (d.text.trim()) b.text = d.text
      if (d.rationale.trim()) b.rationale = d.rationale
      b.expectedRev = c.view!.rev
      c.sent = JSON.stringify(b)
    }
    const body = c.sent
    const ac = new AbortController()
    sends.set(c, ac)
    c.sending = true
    c.unknown = ''
    c.refused = ''
    const p = call('POST', `/api/decisions/${id}/respond`, MAX_DECIDE_REPLY, body, ac.signal)
    track(p.then(() => {}))
    const r = await p
    c.sending = false
    sends.delete(c)
    if (!alive() || !mine(c) || c.sent !== body) return
    const outcome = respondOutcome(r.status, r.body, (JSON.parse(body) as { responseId: string }).responseId)
    if (outcome === 'gone') stopCard(c, 'gone')
    else if (outcome === 'taken') {
      settle(c, r.body.receipt)
      if (isView(r.body.decision, id)) take(c, r.body.decision, false)
      // A new answer only on a view read after this one was taken: the marks cleared, a read begun before it dropped, one begun now.
      Object.assign(c, { freshAt: 0, freshWall: 0 })
      fresh(c)
      if (!c.end) {
        reads.get(c)!.stop()
        reads.get(c)!.start()
      }
    } else if (outcome === 'refused') {
      c.sent = ''
      c.refused = decideWords(r.body.error.code, id)
    } else c.unknown = heldWords(c, r.body?.error?.code === 'rate_limited' ? 'rate_limited' : 'decision_moved')
  }

  return {
    page,
    answerable,
    send,
    shown,
    /** Begins: who the pass is, then the list, and the address's decision first. */
    start() {
      env.doc.addEventListener('visibilitychange', shown)
      env.win.addEventListener('focus', shown)
      if (!page.ended) reads.get(page.list)!.start()
    },
    /** Stops the page: no read is started again, every request on its way is aborted, and no reply changes anything. */
    stop() {
      stopped = true
      env.doc.removeEventListener('visibilitychange', shown)
      env.win.removeEventListener('focus', shown)
      for (const r of reads.values()) r.stop()
      for (const a of [...aborts]) a()
      for (const t of freshTimers.values()) clearTimeout(t)
      freshTimers.clear()
    },
    /** Opens a listed decision's card: not while the list is not fresh, nor while DECIDE_CARDS are open, nor once its card has ended. */
    open(id: string) {
      if (alive() && listFresh() && openCount() < DECIDE_CARDS && page.list.entries.some((e) => e.id === id) && !card(id)?.end) openCard(id, true)
    },
    closable,
    /** Closes a card with nothing unsent: its read on the way is aborted, and its state kept as it is. */
    close(id: string) {
      const c = card(id)
      if (!c || !closable(c)) return
      c.open = false
      reads.get(c)?.stop()
    },
    /** Drops a card that reached its end, and everything the page held for it: its reads, its freshness timer and its send. */
    dismiss(id: string) {
      const i = page.cards.findIndex((c) => c.id === id && c.end)
      if (i < 0) return
      const c = page.cards[i]!
      page.cards.splice(i, 1)
      reads.get(c)?.stop()
      reads.delete(c)
      sends.get(c)?.abort()
      clearTimeout(freshTimers.get(c))
      freshTimers.delete(c)
    },
    openCount,
    /** The work under way, settled: the tests' seam. */
    async whenIdle() {
      while (pending.size) await Promise.allSettled([...pending])
    },
  }
}

/** The decide page's client over the browser: the tab's pass, the window's focus, the document's visibility and both clocks. */
export function useDecide(did: string | undefined) {
  const d = createDecide({ fetch: (i, o) => globalThis.fetch(i, o), pass: useIdentityPass(), base: useApiBase().httpBase.value, doc: document, win: window, now: () => performance.now(), wall: () => Date.now() }, did)
  onMounted(d.start)
  onBeforeUnmount(d.stop)
  return d
}
