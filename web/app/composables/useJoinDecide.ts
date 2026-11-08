import { computed, nextTick, ref, watch, type Ref } from 'vue'
import { itemOf, mintRequestId, reconcileDelivery, showingWords, stepsFinal, trackKey, type DeliveryStep, type InboxAction, type InboxItem, type InboxSummary } from '~/utils/inbox'
import { DecideCode } from '~/utils/protocol'
import { decideOf, decideUnconfirmed, LaneError, refusedWords, type DecisionLane, type LaneDecide, type LaneRecent } from '~/utils/transport/decide'

/** One action taken through the link, as the page keeps it: `from`, the item it was taken on when its steps are read under another. */
export type JoinRecent = LaneRecent & { from?: string }

/** Per item: the action under way, its refusal's words, a decide no word came back for (asked again under the same opId), a note. */
export type JoinFeedback = { busy?: string; words?: string; unknown?: string; again?: LaneDecide; label?: string }

/**
 * What the join page's Decide keeps of a decision link's lane (docs/plans/decision-inbox.md, PR-7), owned by the page for its whole
 * life, not by the tab that draws it: a reconnect of the lane, or the run's members' grid left for a member in full, keeps the item
 * open, the link's actions taken here and an action not yet confirmed (its feedback, with the decide to ask again under its own
 * opId), so nothing the person did is dropped with a connection.
 *
 * The scope is listed from the top when `seq` moves (a welcome, `decisions_changed`): one list under way, one queued behind it,
 * and every action not final tracked again with it. More asks the page after the list's cursor; a reply to a More asked of a list
 * read again since is dropped (each list read is a generation), so a page never lands on another list. Each action is a `decide`
 * through the item's own operation under an id of its own (`opId`), its four steps read through `decision_track` as the link's
 * actor; an action whose steps are read under another item (a blocker's question's Cancel, read as its task's) is kept with the
 * item it was taken on (`from`), so that item's card shows them. The link's Recent is the owner's (at most 50), with this page's
 * own actions not yet in it.
 */
export function useJoinDecide(lane: DecisionLane, seq: Ref<number>) {
  const items = ref<InboxSummary[]>([])
  const next = ref('')
  const total = ref(0)
  const loaded = ref(false)
  const error = ref('')
  /** The link's Recent as the owner keeps it (the first page's), and this page's own actions not yet in it. */
  const recent = ref<LaneRecent[]>([])
  const mine = ref<JoinRecent[]>([])
  const tracks = ref<Record<string, DeliveryStep[]>>({})

  /** The item open. */
  const openRef = ref('')
  /** The Recent entry a row opened, its steps the card shows. */
  const pick = ref<{ receipt: string; attempt: string } | null>(null)
  const item = ref<InboxItem | null>(null)
  const missing = ref('')
  const feedback = ref<Record<string, JoinFeedback>>({})

  const showing = computed(() => showingWords(items.value.length, total.value))
  /** Every action of the link, newest first, one per item and attempt (this page's own, which knows `from`, over the owner's). */
  const actions = computed<JoinRecent[]>(() => {
    const by = new Map<string, JoinRecent>()
    for (const e of [...recent.value, ...mine.value]) by.set(trackKey(e), e)
    const at = (e: LaneRecent) => Date.parse(e.at) || 0
    return [...by.values()].sort((a, b) => at(b) - at(a))
  })
  const titles = computed(() => new Map(items.value.map((s) => [s.ref, s.title])))
  const seenTitles = ref<Record<string, string>>({})

  let listing: Promise<void> | null = null
  let queued = false
  /** The list's generation: one more each time a list read lands, so a More asked of the list before it is dropped. */
  let generation = 0

  /** Lists the scope again from the top (one under way, one queued), tracks what is not final, and reads the open item again. */
  function relist(): Promise<void> {
    if (listing) {
      queued = true
      return listing
    }
    listing = (async () => {
      try {
        do {
          queued = false
          await listOnce()
        } while (queued)
      } finally {
        listing = null
      }
    })()
    return listing
  }
  async function listOnce() {
    try {
      const p = await lane.list()
      generation++
      items.value = p.items
      next.value = p.next ?? ''
      total.value = p.total
      recent.value = p.recent
      for (const s of p.items) seenTitles.value[s.ref] = s.title
      loaded.value = true
      error.value = ''
    } catch (e) {
      error.value = (e as Error).message
    }
    await Promise.all(actions.value.filter((e) => !stepsFinal(tracks.value[trackKey(e)])).map(trackOne))
    if (openRef.value && !feedback.value[openRef.value]?.busy) await readItem()
  }
  /** The page after the list's cursor, added to the list it was asked of; dropped when the list was read again meanwhile. */
  async function more() {
    if (!next.value) return
    const g = generation
    try {
      const p = await lane.list(next.value)
      if (g !== generation) return
      const have = new Set(items.value.map((s) => s.ref))
      items.value = [...items.value, ...p.items.filter((s) => !have.has(s.ref))]
      next.value = p.next ?? ''
      total.value = p.total
    } catch (e) {
      if (g === generation) error.value = (e as Error).message
    }
  }
  watch(seq, () => void relist())

  async function trackOne(e: LaneRecent) {
    try {
      const t = await lane.track(e.ref, e.receipt, e.attempt)
      const k = trackKey(e)
      tracks.value = { ...tracks.value, [k]: reconcileDelivery(tracks.value[k], t.steps) }
    } catch {
      /* read again with the next list */
    }
  }

  async function readItem() {
    const r = openRef.value
    if (!r) return
    try {
      const it = itemOf(await lane.read(r))
      if (openRef.value !== r) return
      item.value = it
      seenTitles.value[it.ref] = it.title
      missing.value = ''
    } catch (e) {
      if (openRef.value !== r) return
      item.value = null
      missing.value = e instanceof LaneError && e.code === DecideCode.NotFound ? 'This link no longer finds that item.' : (e as Error).message
    }
  }
  function open(r: string, p: { receipt: string; attempt: string } | null = null) {
    if (openRef.value !== r) item.value = null
    openRef.value = r
    pick.value = p
    missing.value = ''
    void nextTick(readItem)
  }
  function back() {
    openRef.value = ''
    pick.value = null
    item.value = null
    missing.value = ''
  }

  /** The action whose steps the open card shows: the Recent row's it was opened from, else the newest taken on the item. */
  const shownAction = computed(() => {
    const p = pick.value
    const r = openRef.value
    return actions.value.find((e) => (e.ref === r || e.from === r) && (!p || (e.receipt === p.receipt && e.attempt === p.attempt)))
  })
  const delivery = computed(() => (shownAction.value ? tracks.value[trackKey(shownAction.value)] : undefined))
  const shownFeedback = computed(() => (openRef.value ? feedback.value[openRef.value] : undefined))

  function note(r: string, f: JoinFeedback) {
    feedback.value = { ...feedback.value, [r]: f }
  }
  /** An action of this page's, kept with the item it was taken on (`from`) when its steps are read under another. */
  function keep(taken: string, ref: string, receipt: string, attempt: string): JoinRecent {
    const e: JoinRecent = { ref, receipt, attempt, at: new Date().toISOString(), ...(ref !== taken ? { from: taken } : {}) }
    mine.value = [e, ...mine.value.filter((x) => trackKey(x) !== trackKey(e))].slice(0, 50)
    return e
  }

  /** One action on the item drawn, through the lane: a `decide` under a fresh opId. */
  async function act(r: string, a: InboxAction, text?: string) {
    const it = item.value
    if (!it || it.ref !== r || feedback.value[r]?.busy) return
    pick.value = null
    await send(it.kind, decideOf(it, a, mintRequestId(), text), a.label)
  }
  /** Sends m (again, under its own opId, when no word came the first time: the owner answers a repeat from its record). */
  async function send(kind: string, m: LaneDecide, label: string) {
    note(m.ref, { busy: label })
    try {
      const ok = await lane.decide(m)
      const e = keep(m.ref, ok.ref || m.ref, ok.receipt, m.opId)
      note(m.ref, {})
      await trackOne(e)
    } catch (err) {
      if (decideUnconfirmed(err)) {
        note(m.ref, { unknown: 'Your action went, and no word came back to say whether it was taken. Read again: it is asked again under its own id, and the owner, which takes it once, says what came of it.', again: m, label })
      } else {
        note(m.ref, { words: refusedWords(err, kind) })
        const x = err instanceof LaneError ? err.extra : {}
        if (x.ref && x.receipt) await trackOne(keep(m.ref, x.ref, x.receipt, m.opId))
      }
    } finally {
      await readItem()
      void relist()
    }
  }
  /** Read again: a decide no word came back for is asked again under its own opId; otherwise the action's steps and the item. */
  async function reread() {
    const f = shownFeedback.value
    const it = item.value
    if (f?.again && it) return send(it.kind, f.again, f.label ?? '')
    const e = shownAction.value
    if (e) await trackOne(e)
    await readItem()
  }

  /** The lane's read of one evidence entry of the open item. */
  function readEvidence(evidence: string) {
    return lane.evidence(openRef.value, evidence)
  }
  /** The id of a session in the scope, for its terminal: by the id the item's read gave it, or the lane's read of the entry; '' with words when none. */
  async function sessionOf(r: string): Promise<string> {
    const it = item.value
    if (!it) return ''
    let id = it.evidence?.find((e) => e.ref === r)?.session ?? ''
    try {
      if (!id) {
        const e = await lane.evidence(it.ref, r)
        id = e.kind === 'session' ? e.session : ''
      }
    } catch (e) {
      note(it.ref, { words: refusedWords(e, it.kind) })
      return ''
    }
    if (!id) note(it.ref, { words: 'That session is not open through this link now: open it on the desktop or the LAN.' })
    return id
  }

  return {
    items,
    next,
    total,
    loaded,
    error,
    showing,
    actions,
    titles,
    seenTitles,
    tracks,
    openRef,
    item,
    missing,
    feedback,
    delivery,
    shownFeedback,
    relist,
    more,
    open,
    back,
    act,
    reread,
    readEvidence,
    sessionOf,
  }
}

export type JoinDecideState = ReturnType<typeof useJoinDecide>
