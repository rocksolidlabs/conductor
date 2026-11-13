import { onBeforeUnmount, onMounted, reactive } from 'vue'
import { decideWords, isDecisionId, replaces } from '~/utils/decisions'
import { DECIDE_CARDS, DECIDE_FRESH_FOR, DECIDE_READ_EVERY, DECIDE_READ_WAIT, GRANT_DECIDE, MAX_DECIDE_LIST, MAX_DECIDE_REPLY, MAX_DECIDE_VIEW, MAX_ME_REPLY } from '~/utils/protocol'
import type { DecideEntry, DecideReceipt, DecideView, MeReply } from '~/utils/protocol'
import { mintCommandId } from '~/utils/tasks'

// The decide page's client and state (docs/plans/decide-grant.md, "web/app"), page-owned: who the pass is (`GET /api/me`), the decide
// list (`GET /api/me/decisions`), at most DECIDE_CARDS cards open, each read (`GET /api/decisions/{did}`) and answered
// (`POST /api/decisions/{did}/respond`) with the pass in the Authorization header, the one request it is ever sent in (`call`). The
// list and each open card read one at a time, the next due DECIDE_READ_EVERY after the last began while the tab is shown and at once
// when it is shown again, each aborted at DECIDE_READ_WAIT. A card is answered only on a view a read renewed in the last
// DECIDE_FRESH_FOR. A 401 ends the page and empties the pass; a card's 404 closes that card alone. Drafts and the answer sent stay in
// the page's memory, never in the browser's storage.

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
  /** No definite answer came to `sent`: Send again sends it unchanged. */
  unknown: boolean
  /** The words of the last send's definite refusal. */
  refused: string
  /** An end for this person: its answer's final outcome, or no longer open to it (404). */
  end: '' | 'applied' | 'not_used' | 'gone'
  open: boolean
  /** When the last read whose reply the card took began (0: none yet), and whether that is too long ago to answer on. */
  freshAt: number
  suspended: boolean
  /** Whether the last list read held it. */
  listed: boolean
}

/** What the page reads its time and its tab's visibility from: the browser's, or a test's. */
export interface DecideEnv {
  fetch: typeof fetch
  pass: { get(): string; end(): void }
  base: string
  doc: Pick<Document, 'visibilityState' | 'addEventListener' | 'removeEventListener'>
  win: Pick<Window, 'addEventListener' | 'removeEventListener'>
}

type Reply = { status: number; body?: any } // eslint-disable-line @typescript-eslint/no-explicit-any

/** One read at a time on its own schedule: due EVERY after the last began while shown; one due while one is out starts as it returns. */
class Reads {
  private out = false
  private due = false
  private stopped = true
  private began = 0
  private timer?: ReturnType<typeof setTimeout>
  constructor(
    private read: () => Promise<void>,
    private visible: () => boolean,
    private track: (p: Promise<void>) => void,
  ) {}
  start() {
    this.stopped = false
    this.now()
  }
  now() {
    if (this.stopped) return
    if (this.out) {
      this.due = true
      return
    }
    clearTimeout(this.timer)
    this.out = true
    this.began = Date.now()
    this.track(
      this.read().finally(() => {
        this.out = false
        if (this.stopped) return
        if (this.due) {
          this.due = false
          this.now()
        } else if (this.visible()) this.timer = setTimeout(() => this.visible() && this.now(), Math.max(0, this.began + DECIDE_READ_EVERY - Date.now()))
      }),
    )
  }
  stop() {
    this.stopped = true
    this.due = false
    clearTimeout(this.timer)
  }
}

const emptyDraft = (d: DecideDraft) => d.choice === undefined && !d.text.trim() && !d.rationale.trim()
const isView = (v: DecideView | undefined, id: string): v is DecideView => !!v && v.id === id && Number.isInteger(v.rev) && Array.isArray(v.own)

export function createDecide(env: DecideEnv, did?: string) {
  const page = reactive({
    me: null as MeReply | null,
    /** The pass is not a decide pass (it holds admin, or lacks decide). */
    notForIt: false,
    /** Access ended: a 401, or no pass at all. */
    ended: !env.pass.get(),
    list: { entries: [] as DecideEntry[], cut: false, loaded: false, freshAt: 0, suspended: true },
    cards: [] as DecideCard[],
  })
  const pending = new Set<Promise<void>>()
  const track = (p: Promise<void>) => {
    pending.add(p)
    void p.finally(() => pending.delete(p))
  }
  const visible = () => env.doc.visibilityState === 'visible'
  const reads = new Map<string, Reads>()
  const freshTimers = new Map<object, ReturnType<typeof setTimeout>>()

  /** The one request the pass is sent in: to one of the four routes, in the Authorization header, aborted at DECIDE_READ_WAIT. */
  async function call(method: 'GET' | 'POST', path: '/api/me' | '/api/me/decisions' | `/api/decisions/${string}`, max: number, body?: string): Promise<Reply> {
    const pass = env.pass.get()
    if (!pass) return { status: 401 }
    const ac = new AbortController()
    const t = setTimeout(() => ac.abort(), DECIDE_READ_WAIT)
    try {
      const headers: Record<string, string> = { Authorization: `Bearer ${pass}` }
      if (body !== undefined) headers['Content-Type'] = 'application/json'
      const res = await env.fetch(env.base + path, { method, headers, body, signal: ac.signal, cache: 'no-store', redirect: 'error' })
      const bytes = await res.arrayBuffer()
      if (bytes.byteLength > max) return { status: 0 }
      try {
        return { status: res.status, body: JSON.parse(new TextDecoder().decode(bytes)) }
      } catch {
        return { status: res.status }
      }
    } catch {
      return { status: 0 }
    } finally {
      clearTimeout(t)
    }
  }

  /** Whether a mark (a card's, the list's) is too old to answer or open on; judged again when it turns so. */
  function fresh(o: { freshAt: number; suspended: boolean }) {
    o.suspended = !o.freshAt || Date.now() - o.freshAt > DECIDE_FRESH_FOR
    clearTimeout(freshTimers.get(o))
    if (!o.suspended) freshTimers.set(o, setTimeout(() => fresh(o), o.freshAt + DECIDE_FRESH_FOR - Date.now() + 1))
  }

  function end() {
    page.ended = true
    env.pass.end()
    for (const r of reads.values()) r.stop()
    for (const t of freshTimers.values()) clearTimeout(t)
  }

  const card = (id: string) => page.cards.find((c) => c.id === id)
  const openCount = () => page.cards.filter((c) => c.open && !c.end).length

  function stopCard(c: DecideCard, why: DecideCard['end']) {
    c.end = why
    reads.get(c.id)?.stop()
  }

  /** A reply's receipt of the send under way settles it: accepted, or refused in its code's words. */
  function settle(c: DecideCard, rc: DecideReceipt) {
    c.sent = ''
    c.unknown = false
    c.refused = rc.status === 'refused' ? decideWords(rc.code ?? '', c.id) : ''
  }

  /** The one order every reply passes: a read's (whole) or a respond's; `at` the moment a read began, which only a read moves. */
  function take(c: DecideCard, v: DecideView, whole: boolean, at?: number) {
    if ((whole && c.end) || !replaces(c.view, v, whole)) return
    c.view = v
    if (at !== undefined) {
      c.freshAt = at
      fresh(c)
    }
    const sent = c.sent ? (JSON.parse(c.sent) as { responseId: string }).responseId : ''
    const rc = sent ? v.own.find((r) => r.responseId === sent) : undefined
    if (rc) settle(c, rc)
    if (v.outcome === 'applied' || v.outcome === 'not_used') stopCard(c, v.outcome)
  }

  async function readCard(c: DecideCard) {
    const at = Date.now()
    const r = await call('GET', `/api/decisions/${c.id}`, MAX_DECIDE_VIEW)
    if (page.ended || c.end) return
    if (r.status === 401) end()
    else if (r.status === 404) stopCard(c, 'gone')
    else if (r.status === 200 && isView(r.body, c.id)) take(c, r.body, true, at)
  }

  function openCard(id: string, listed: boolean) {
    let c = card(id)
    if (!c) {
      c = reactive<DecideCard>({ id, view: null, draft: { text: '', rationale: '' }, sent: '', sending: false, unknown: false, refused: '', end: '', open: true, freshAt: 0, suspended: true, listed })
      page.cards.push(c)
      const cc = c
      reads.set(id, new Reads(() => readCard(cc), visible, track))
    }
    c.open = true
    reads.get(id)!.start()
  }

  async function readList() {
    if (!page.me) {
      const r = await call('GET', '/api/me', MAX_ME_REPLY)
      if (page.ended) return
      if (r.status === 401) return end()
      if (r.status !== 200 || !Array.isArray(r.body?.grants)) return
      page.me = r.body as MeReply
      if (!page.me.grants.includes(GRANT_DECIDE) || page.me.grants.includes('admin')) {
        page.notForIt = true
        return reads.get('')!.stop()
      }
      if (isDecisionId(did)) openCard(did, false)
    }
    const at = Date.now()
    const r = await call('GET', '/api/me/decisions', MAX_DECIDE_LIST)
    if (page.ended) return
    if (r.status === 401) return end()
    if (r.status !== 200 || !Array.isArray(r.body?.decisions)) return
    const entries = (r.body.decisions as DecideEntry[]).filter((e) => isDecisionId(e?.id) && Number.isInteger(e.rev))
    Object.assign(page.list, { entries, cut: r.body.cut === true, loaded: true, freshAt: at })
    fresh(page.list)
    for (const c of page.cards) {
      if (!c.open || c.end) continue
      const e = entries.find((x) => x.id === c.id)
      // A listed rev other than the view's, or a decision that left the list since the last read, reads the card at once.
      if ((e && c.view && e.rev !== c.view.rev) || (!e && c.listed)) reads.get(c.id)?.now()
      c.listed = !!e
    }
    for (const e of entries) if (openCount() < DECIDE_CARDS && !card(e.id)) openCard(e.id, true)
  }
  reads.set('', new Reads(readList, visible, track))

  /** The tab shown again, or focused: everything read at once, and every mark judged now. */
  function shown() {
    if (!visible()) return
    for (const r of reads.values()) r.now()
    for (const c of page.cards) if (c.freshAt) fresh(c)
    if (page.list.freshAt) fresh(page.list)
  }

  /** Whether a card may be closed: no draft and no answer whose outcome is not known; its state is kept. */
  const closable = (c: DecideCard) => c.open && !c.end && emptyDraft(c.draft) && !c.sent

  /** Whether a card's answer may be sent now. */
  function answerable(c: DecideCard): boolean {
    return !page.ended && c.open && !c.end && !c.sending && !c.suspended && c.view?.state === 'open'
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
    c.sending = true
    c.unknown = false
    c.refused = ''
    const p = call('POST', `/api/decisions/${id}/respond`, MAX_DECIDE_REPLY, body)
    track(p.then(() => {}))
    const r = await p
    c.sending = false
    if (page.ended || c.sent !== body) return
    if (r.status === 401) end()
    else if (r.status === 404) stopCard(c, 'gone')
    else if ((r.status === 200 || r.status === 201) && typeof r.body?.receipt?.responseId === 'string') {
      settle(c, r.body.receipt)
      if (isView(r.body.decision, id)) take(c, r.body.decision, false)
    } else if (r.status >= 400 && r.status < 500 && typeof r.body?.error?.code === 'string') {
      c.sent = ''
      c.refused = decideWords(r.body.error.code, id)
    } else c.unknown = true
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
      if (!page.ended) reads.get('')!.start()
    },
    stop() {
      env.doc.removeEventListener('visibilitychange', shown)
      env.win.removeEventListener('focus', shown)
      for (const r of reads.values()) r.stop()
      for (const t of freshTimers.values()) clearTimeout(t)
    },
    /** Opens a listed decision's card: not while the list is not fresh, nor while DECIDE_CARDS are open. */
    open(id: string) {
      if (!page.ended && !page.list.suspended && openCount() < DECIDE_CARDS && page.list.entries.some((e) => e.id === id)) openCard(id, true)
    },
    closable,
    close(id: string) {
      const c = card(id)
      if (!c || !closable(c)) return
      c.open = false
      reads.get(id)?.stop()
    },
    /** Drops a card that reached its end. */
    dismiss(id: string) {
      const i = page.cards.findIndex((c) => c.id === id && c.end)
      if (i < 0) return
      clearTimeout(freshTimers.get(page.cards[i]!))
      page.cards.splice(i, 1)
      reads.delete(id)
    },
    openCount,
    /** The work under way, settled: the tests' seam. */
    async whenIdle() {
      while (pending.size) await Promise.allSettled([...pending])
    },
  }
}

/** The decide page's client over the browser: the tab's pass, the window's focus and the document's visibility. */
export function useDecide(did: string | undefined) {
  const d = createDecide({ fetch: (i, o) => globalThis.fetch(i, o), pass: useIdentityPass(), base: useApiBase().httpBase.value, doc: document, win: window }, did)
  onMounted(d.start)
  onBeforeUnmount(d.stop)
  return d
}
