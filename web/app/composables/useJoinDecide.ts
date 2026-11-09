import { computed, nextTick, ref, watch, type Ref } from 'vue'
import { itemOf, mintRequestId, reconcileDelivery, showingWords, stepsFinal, trackKey, type DeliveryStep, type InboxAction, type InboxItem, type InboxSummary } from '~/utils/inbox'
import { DecideCode, type DecideOkMsg } from '~/utils/protocol'
import {
  assembleRecord,
  decideOf,
  decideOutcome,
  decideUnconfirmed,
  LaneError,
  LaneLocal,
  RECORD_MOVED_WORDS,
  RECORD_NO_WORD_WORDS,
  refusedWords,
  type AssembledRecord,
  type DecisionLane,
  type LaneDecide,
  type LaneRecent,
} from '~/utils/transport/decide'

/** One action taken through the link, as the page keeps it: `from`, the item it was taken on when its steps are read under another. */
export type JoinRecent = LaneRecent & { from?: string }

/**
 * An action taken on an item, owed until a reply settles it (decideOutcome): the decide as it went, under its own opId, asked again
 * as it is; `kind` and `label`, the item's and the action's, for its words; `went`, whether a send of it may have been taken (one
 * that got no reply); `sending`, one under way now; `words`, what the page says of it while it is owed ('' on its first send).
 */
export type JoinOwed = { m: LaneDecide; kind: string; label: string; went: boolean; sending: boolean; words: string }

/**
 * Per item, as its card draws it: the action under way (`busy`, its label, or 'owed' while one is owed and not under way), a
 * refusal's words, and an owed action's (`unknown`) with its decide (`again`).
 */
export type JoinFeedback = { busy?: string; words?: string; unknown?: string; again?: LaneDecide }

/** Local codes of a request that got no reply of the owner's. */
const NO_REPLY = new Set<string>([LaneLocal.NotSent, LaneLocal.Closed, LaneLocal.NoWord])

const UNCONFIRMED_WORDS =
  'Your action went, and no word came back to say whether it was taken. It is asked again under its own id when the link is connected again, or when you read again, and the owner, which takes it once, says what came of it.'

/** What the page says of an action still owed after a send that ended with e. */
function owedWords(e: unknown, kind: string, went: boolean): string {
  if (decideUnconfirmed(e)) return UNCONFIRMED_WORDS
  const tail = went
    ? 'Your earlier action is still owed a word: it is asked again under its own id when the link is connected again, or when you read again.'
    : 'Your action is kept, and sent under its own id when the link is connected again, or when you read again.'
  return `${refusedWords(e, kind)} ${tail}`
}

/**
 * What the join page's Decide keeps of a decision link's lane (docs/plans/decision-inbox.md, PR-7), owned by the page for its whole
 * life, not by the tab that draws it: a reconnect of the lane, or the run's members' grid left for a member in full, keeps the item
 * open, the link's actions taken here and an action not yet confirmed (its feedback, with the decide to ask again under its own
 * opId), so nothing the person did is dropped with a connection.
 *
 * An action is in one of three states (decideOutcome): confirmed by the owner's reply to its request, refused by one, or owed.
 * Owed is everything else: a lost reply, nothing sent, a refusal that says nothing of it (too many under way, a receipt the owner
 * could not read), a disconnect, the page's own reads and navigation. An owed action keeps its decide and opId, says so in words,
 * and is asked again under its own opId with each welcome and `decisions_changed` (`seq`, also when one came while it was being
 * sent) and when Read again is pressed, until a reply settles it; while it is owed no other action is taken on its item. Only `put`
 * writes an action's state, at its start and at each send's end; what an evidence read or a terminal could not open is said apart
 * (`notice`), never as the action's.
 *
 * The scope is listed from the top when `seq` moves (a welcome, `decisions_changed`): one list under way, one queued behind it,
 * and every action not final tracked again with it. More asks the page after the list's cursor; a reply to a More asked of a list
 * read again since is dropped (each list read is a generation), so a page never lands on another list. Each action is a `decide`
 * through the item's own operation under an id of its own (`opId`), its four steps read through `decision_track` as the link's
 * actor; an action whose steps are read under another item (a blocker's question's Cancel, read as its task's) is kept with the
 * item it was taken on (`from`), so that item's card shows them. The link's Recent is the owner's (at most 50), with this page's
 * own actions not yet in it.
 *
 * A DR1 record open (docs/plans/decision-inbox.md, PR-4) is assembled from its parts over the lane (assembleRecord: the head, then its
 * responses and its history, every part cut at the head's mark) each time its item is read, so on its opening and at each
 * `decisions_changed`; DR1's card is handed only a record assembled whole (`record`). Each assembly is one generation: another item
 * opened, the list left, or a later assembly begun drops what an earlier one brings back, and it asks no more parts. A record that
 * moved four times in a row while it was read keeps the last one assembled whole, with the words why (`recordWords`), until the next
 * change reads it again; one whose read got no word back says so, and Read again (`reread`) reads the item and its record again.
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
  /** The open DR1 record as last assembled whole, and what the page says beside it ('' for nothing). */
  const record = ref<AssembledRecord | null>(null)
  const recordWords = ref('')
  /** The assemblies' generation: one more for each assembly begun and for each item left. */
  let recordGen = 0
  /** Each item's action owed (JoinOwed), the refusal's words of its last one settled, and what a read of its evidence said. */
  const owed = ref<Record<string, JoinOwed>>({})
  const refused = ref<Record<string, string>>({})
  const notices = ref<Record<string, string>>({})
  const feedback = computed<Record<string, JoinFeedback>>(() => {
    const out: Record<string, JoinFeedback> = {}
    for (const r of new Set([...Object.keys(owed.value), ...Object.keys(refused.value)])) out[r] = feedbackOf(r)
    return out
  })
  function feedbackOf(r: string): JoinFeedback {
    const o = owed.value[r]
    const words = refused.value[r]
    return {
      ...(o ? { busy: o.sending ? o.label : 'owed', again: o.m, ...(o.words ? { unknown: o.words } : {}) } : {}),
      ...(words ? { words } : {}),
    }
  }

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
    if (openRef.value && !owed.value[openRef.value]?.sending) await readItem()
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
  watch(seq, () => {
    void relist()
    // Every action still owed is asked again under its own opId over the connection that moved seq (one being sent, at its end).
    for (const r of Object.keys(owed.value)) void send(r)
  })

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
      if (it.kind === 'decision') void readRecord(r)
    } catch (e) {
      if (openRef.value !== r) return
      // No reply (no connection now): the item stays drawn as last read, with what is owed on it.
      if (e instanceof LaneError && NO_REPLY.has(e.code) && item.value?.ref === r) return
      item.value = null
      missing.value = e instanceof LaneError && e.code === DecideCode.NotFound ? 'This link no longer finds that item.' : (e as Error).message
    }
  }
  /** Assembles the open DR1 record r (one generation); its outcome is taken only while it is still the one asked, for r still open. */
  async function readRecord(r: string) {
    const gen = ++recordGen
    const mine = () => gen === recordGen && openRef.value === r
    try {
      const out = await assembleRecord(r, (part, rev, after) => lane.record(r, part, rev, after), mine)
      if (!mine() || 'dropped' in out) return
      if ('whole' in out) {
        record.value = out.whole
        recordWords.value = ''
      } else {
        // Moved four times in a row: the last record assembled whole stays (or none), read again at the next change.
        if (record.value?.ref !== r) record.value = null
        recordWords.value = RECORD_MOVED_WORDS
      }
    } catch (e) {
      if (!mine()) return
      if (record.value?.ref !== r) record.value = null
      // No connection now (closed, nothing sent): the welcome of the next one reads it again, and the page says it is offline. A read
      // that got no word on a connection still up is said, with Read again: no change may come to read it again by itself.
      recordWords.value =
        e instanceof LaneError && e.code === LaneLocal.NoWord ? RECORD_NO_WORD_WORDS : e instanceof LaneError && NO_REPLY.has(e.code) ? '' : `The record could not be read: ${(e as Error).message}`
    }
  }
  /** Leaves the record shown: nothing an assembly under way brings back is taken. */
  function dropRecord() {
    recordGen++
    record.value = null
    recordWords.value = ''
  }

  function open(r: string, p: { receipt: string; attempt: string } | null = null) {
    if (openRef.value !== r) {
      item.value = null
      dropRecord()
    }
    openRef.value = r
    pick.value = p
    missing.value = ''
    if (notices.value[r]) notices.value = { ...notices.value, [r]: '' }
    void nextTick(readItem)
  }
  function back() {
    openRef.value = ''
    pick.value = null
    item.value = null
    missing.value = ''
    dropRecord()
  }

  /** The action whose steps the open card shows: the Recent row's it was opened from, else the newest taken on the item. */
  const shownAction = computed(() => {
    const p = pick.value
    const r = openRef.value
    return actions.value.find((e) => (e.ref === r || e.from === r) && (!p || (e.receipt === p.receipt && e.attempt === p.attempt)))
  })
  const delivery = computed(() => (shownAction.value ? tracks.value[trackKey(shownAction.value)] : undefined))
  const shownFeedback = computed(() => (openRef.value && (owed.value[openRef.value] || refused.value[openRef.value]) ? feedbackOf(openRef.value) : undefined))
  /** What a read of the open item's evidence, or its terminal, could not do: apart from its action. */
  const shownNotice = computed(() => (openRef.value ? notices.value[openRef.value] || '' : ''))

  /** The one writer of an item's action: owed (o), or settled (undefined, with the refusal's words when it was refused). */
  function put(r: string, o: JoinOwed | undefined, words = '') {
    const next = { ...owed.value }
    if (o) next[r] = o
    else delete next[r]
    owed.value = next
    refused.value = { ...refused.value, [r]: words }
  }
  /** An action of this page's, kept with the item it was taken on (`from`) when its steps are read under another. */
  function keep(taken: string, ref: string, receipt: string, attempt: string): JoinRecent {
    const e: JoinRecent = { ref, receipt, attempt, at: new Date().toISOString(), ...(ref !== taken ? { from: taken } : {}) }
    mine.value = [e, ...mine.value.filter((x) => trackKey(x) !== trackKey(e))].slice(0, 50)
    return e
  }

  /** One action on the item drawn, through the lane: a `decide` under a fresh opId, owed from now until a reply settles it. */
  async function act(r: string, a: InboxAction, text?: string) {
    const it = item.value
    // An action still owed is settled first: no other action replaces it.
    if (!it || it.ref !== r || owed.value[r]) return
    pick.value = null
    put(r, { m: decideOf(it, a, mintRequestId(), text), kind: it.kind, label: a.label, went: false, sending: false, words: '' })
    await send(r)
  }
  /**
   * Sends the action owed on r, as it went (under its own opId: the owner answers a repeat from its record), unless one send of it
   * is under way; its outcome (decideOutcome) settles it or leaves it owed. A welcome or `decisions_changed` that came while it was
   * under way asks it again at once when it is still owed, so no connection's chance to settle it is lost.
   */
  async function send(r: string) {
    const o = owed.value[r]
    if (!o || o.sending) return
    const at = seq.value
    put(r, { ...o, sending: true })
    let ok: DecideOkMsg | undefined
    let err: unknown = null
    try {
      ok = await lane.decide(o.m)
    } catch (e) {
      err = e ?? new Error('Not taken.')
    }
    const how = decideOutcome(err, o.went)
    try {
      if (how === 'confirmed' && ok) {
        put(r, undefined)
        await trackOne(keep(r, ok.ref || r, ok.receipt, o.m.opId))
      } else if (how === 'refused') {
        put(r, undefined, refusedWords(err, o.kind))
        const x = err instanceof LaneError ? err.extra : {}
        if (x.ref && x.receipt) await trackOne(keep(r, x.ref, x.receipt, o.m.opId))
      } else {
        const went = o.went || decideUnconfirmed(err)
        put(r, { ...o, went, sending: false, words: owedWords(err, o.kind, went) })
        if (seq.value !== at) void send(r)
      }
    } finally {
      await readItem()
      void relist()
    }
  }
  /** Read again: an action owed is asked again under its own opId; otherwise the action's steps and the item. */
  async function reread() {
    const r = openRef.value
    const o = r ? owed.value[r] : undefined
    if (o) return send(r)
    const e = shownAction.value
    if (e) await trackOne(e)
    await readItem()
  }

  /** The lane's read of one evidence entry of the open item. */
  function readEvidence(evidence: string) {
    return lane.evidence(openRef.value, evidence)
  }
  /**
   * The id of a session in the scope, for its terminal: by the id the item's read gave it, or the lane's read of the entry; '' with
   * words when none, said apart from the item's action (`notice`), which no read of its evidence settles or ends.
   */
  async function sessionOf(r: string): Promise<string> {
    const it = item.value
    if (!it) return ''
    const say = (words: string) => (notices.value = { ...notices.value, [it.ref]: words })
    let id = it.evidence?.find((e) => e.ref === r)?.session ?? ''
    try {
      if (!id) {
        const e = await lane.evidence(it.ref, r)
        id = e.kind === 'session' ? e.session : ''
      }
    } catch (e) {
      say(refusedWords(e, it.kind))
      return ''
    }
    say(id ? '' : 'That session is not open through this link now: open it on the desktop or the LAN.')
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
    record,
    recordWords,
    owed,
    feedback,
    delivery,
    shownFeedback,
    shownNotice,
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
