<script setup lang="ts">
import { relativeTime } from '~/utils/sessions'
import { itemOf, mintRequestId, reconcileDelivery, recentBadge, showingWords, stepsFinal, trackKey, type DeliveryStep, type InboxAction, type InboxItem, type InboxSummary } from '~/utils/inbox'
import { DecideCode } from '~/utils/protocol'
import { decideOf, decideUnconfirmed, LaneError, refusedWords, type DecisionLane, type LaneDecide, type LaneRecent } from '~/utils/transport/decide'

/**
 * The join page's Decide (docs/plans/decision-inbox.md, PR-7): what a decision link covers, over the link's own lane, end to end to
 * the server that owns it (through a switchyard, which stores nothing of it). The same cards as the decision page (InboxItem), paged
 * with More ("Showing 25 of 60"), each item read whole before it is acted on, each action a `decide` through the item's own
 * operation under an id of its own (`opId`), its four steps read through `decision_track` as the link's actor, and the link's
 * Recent (the server keeps it with the link, at most 50), so an item an action closed stays readable with its steps. Evidence is
 * read through the lane (a check's log's end; a session in the scope opens in its terminal tab, `openSession`); anything else is
 * opened on the desktop or the LAN. `seq` moves when the lane must be listed again (a welcome, `decisions_changed`): one list under
 * way, one queued behind it, and every Recent entry not final tracked again with it.
 */
const props = defineProps<{ lane: DecisionLane; seq: number; title?: string }>()
const emit = defineEmits<{ openSession: [session: string] }>()

const items = ref<InboxSummary[]>([])
const next = ref('')
const total = ref(0)
const loaded = ref(false)
const error = ref('')
/** The link's Recent as the owner keeps it (the first page's), and this page's own actions not yet in it. */
const recent = ref<LaneRecent[]>([])
const mine = ref<LaneRecent[]>([])
const tracks = ref<Record<string, DeliveryStep[]>>({})

/** The item open, kept by the page (`v-model:open`), so a member's terminal opened from it and left comes back to it. */
const openRef = defineModel<string>('open', { default: '' })
/** The Recent entry a row opened, its steps the card shows. */
const pick = ref<{ receipt: string; attempt: string } | null>(null)
const item = ref<InboxItem | null>(null)
const missing = ref('')
/** Per item: the action under way, its refusal's words, a decide no word came back for (asked again under the same opId), a note. */
const feedback = ref<Record<string, { busy?: string; words?: string; unknown?: string; again?: LaneDecide; label?: string }>>({})

const showing = computed(() => showingWords(items.value.length, total.value))
/** Every action of the link, newest first, one per item and attempt. */
const actions = computed<LaneRecent[]>(() => {
  const by = new Map<string, LaneRecent>()
  for (const e of [...recent.value, ...mine.value]) by.set(trackKey(e), e)
  const at = (e: LaneRecent) => Date.parse(e.at) || 0
  return [...by.values()].sort((a, b) => at(b) - at(a))
})
const titles = computed(() => new Map(items.value.map((s) => [s.ref, s.title])))
const seenTitles = ref<Record<string, string>>({})

let listing: Promise<void> | null = null
let again = false
/** Lists the scope again from the top (one under way, one queued), tracks what is not final, and reads the open item again. */
function relist(): Promise<void> {
  if (listing) {
    again = true
    return listing
  }
  listing = (async () => {
    try {
      do {
        again = false
        await listOnce()
      } while (again)
    } finally {
      listing = null
    }
  })()
  return listing
}
async function listOnce() {
  try {
    const p = await props.lane.list()
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
async function more() {
  if (!next.value) return
  try {
    const p = await props.lane.list(next.value)
    const have = new Set(items.value.map((s) => s.ref))
    items.value = [...items.value, ...p.items.filter((s) => !have.has(s.ref))]
    next.value = p.next ?? ''
    total.value = p.total
  } catch (e) {
    error.value = (e as Error).message
  }
}
watch(() => props.seq, () => void relist(), { immediate: true })

async function trackOne(e: LaneRecent) {
  try {
    const t = await props.lane.track(e.ref, e.receipt, e.attempt)
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
    const it = itemOf(await props.lane.read(r))
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
function open(ref: string, p: { receipt: string; attempt: string } | null = null) {
  if (openRef.value !== ref) item.value = null
  openRef.value = ref
  pick.value = p
  missing.value = ''
  // The page holds the open item: its prop says the new one once the page has taken it.
  void nextTick(readItem)
}
function back() {
  openRef.value = ''
  pick.value = null
  item.value = null
  missing.value = ''
}

/** The action whose steps the open card shows: the Recent row's it was opened from, else the newest on the item. */
const shownAction = computed(() => {
  const p = pick.value
  return actions.value.find((e) => e.ref === openRef.value && (!p || (e.receipt === p.receipt && e.attempt === p.attempt)))
})
const delivery = computed(() => (shownAction.value ? tracks.value[trackKey(shownAction.value)] : undefined))
const shownFeedback = computed(() => (openRef.value ? feedback.value[openRef.value] : undefined))

function note(ref: string, f: (typeof feedback.value)[string]) {
  feedback.value = { ...feedback.value, [ref]: f }
}
function keep(e: LaneRecent) {
  mine.value = [e, ...mine.value.filter((x) => trackKey(x) !== trackKey(e))].slice(0, 50)
}

/** One action on the item drawn, through the lane: a `decide` under a fresh opId. */
async function act(ref: string, a: InboxAction, text?: string) {
  const it = item.value
  if (!it || it.ref !== ref || feedback.value[ref]?.busy) return
  pick.value = null
  await send(it.kind, decideOf(it, a, mintRequestId(), text), a.label)
}
/** Sends m (again, under its own opId, when no word came the first time: the owner answers a repeat from its record). */
async function send(kind: string, m: LaneDecide, label: string) {
  note(m.ref, { busy: label })
  try {
    const ok = await props.lane.decide(m)
    const e = { ref: ok.ref || m.ref, receipt: ok.receipt, attempt: m.opId, at: new Date().toISOString() }
    keep(e)
    note(m.ref, {})
    await trackOne(e)
  } catch (err) {
    if (decideUnconfirmed(err)) {
      note(m.ref, { unknown: 'Your action went, and no word came back to say whether it was taken. Read again: it is asked again under its own id, and the owner, which takes it once, says what came of it.', again: m, label })
    } else {
      note(m.ref, { words: refusedWords(err, kind) })
      const x = err instanceof LaneError ? err.extra : {}
      if (x.ref && x.receipt) {
        const e = { ref: x.ref, receipt: x.receipt, attempt: m.opId, at: new Date().toISOString() }
        keep(e)
        await trackOne(e)
      }
    }
  } finally {
    await readItem()
    void relist()
  }
}
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
  return props.lane.evidence(openRef.value, evidence)
}
/** A session in the scope, in its terminal tab: by the id the item's read gave it, or the lane's read of the entry. */
async function openTerminal(ref: string) {
  const it = item.value
  if (!it) return
  let id = it.evidence?.find((e) => e.ref === ref)?.session ?? ''
  try {
    if (!id) {
      const e = await props.lane.evidence(it.ref, ref)
      id = e.kind === 'session' ? e.session : ''
    }
  } catch (e) {
    note(it.ref, { words: refusedWords(e, it.kind) })
    return
  }
  if (id) emit('openSession', id)
  else note(it.ref, { words: 'That session is not open through this link now: open it on the desktop or the LAN.' })
}

/** A Recent row's word: Refused, Work resumed, Taken or what the resumed step says (recentBadge). */
const recentWord = (e: LaneRecent) => recentBadge({ ref: e.ref, receipt: e.receipt, attempt: e.attempt, outcome: 'accepted', at: Date.parse(e.at) || 0 }, tracks.value[trackKey(e)], false)
</script>

<template>
  <div class="flex min-w-0 flex-col gap-4" data-join-decide>
    <template v-if="!openRef">
      <h2 v-if="title" class="text-sm font-semibold text-highlighted">{{ title }}</h2>
      <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" :description="loaded ? 'The list below is as it was last read.' : undefined" data-decisions-error />
      <div v-if="loaded && !items.length && !error" class="flex flex-col items-center gap-2 py-6 text-center text-muted" data-decisions-empty>
        <UIcon name="i-lucide-circle-check" class="size-8" />
        <p class="text-sm">Nothing waits on this link now.</p>
      </div>
      <div v-else-if="!loaded && !error" class="flex items-center gap-2 text-sm text-muted"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Reading what waits…</div>
      <ul v-if="items.length" class="flex flex-col gap-2">
        <li v-for="s in items" :key="s.ref">
          <button type="button" class="flex min-h-11 w-full flex-col gap-1 rounded-md border border-default px-3 py-2 text-left transition-colors hover:bg-elevated/50" :data-inbox-row="s.ref" @click="open(s.ref)">
            <span class="flex items-baseline justify-between gap-2">
              <span class="truncate text-sm font-medium text-highlighted" data-inbox-row-title>{{ s.title }}</span>
              <span class="shrink-0 text-xs text-muted">{{ relativeTime(s.since) }}</span>
            </span>
            <span class="line-clamp-2 text-xs text-muted">{{ s.question || s.brief }}</span>
            <span class="flex flex-wrap gap-1">
              <UBadge v-if="s.blocks" :label="`${s.blocks} waiting on it`" color="neutral" variant="subtle" size="sm" />
              <UBadge v-if="!s.actions.length" label="Open the terminal" color="warning" variant="subtle" size="sm" />
              <UBadge v-else :label="`${s.actions.length} ${s.actions.length === 1 ? 'choice' : 'choices'}`" color="neutral" variant="outline" size="sm" />
            </span>
          </button>
        </li>
      </ul>
      <div v-if="showing" class="flex items-center justify-between gap-2 text-xs text-muted">
        <span data-decisions-showing>{{ showing }}</span>
        <UButton v-if="next" label="More" color="neutral" variant="outline" size="sm" class="min-h-11" data-decisions-more @click="more" />
      </div>

      <section v-if="actions.length" class="flex flex-col gap-2" data-decisions-recent>
        <h2 class="text-xs font-semibold uppercase tracking-wide text-muted">Recent <span class="font-normal normal-case tracking-normal">· acted on through this link</span></h2>
        <ul class="flex flex-col gap-1">
          <li v-for="e in actions" :key="trackKey(e)">
            <button type="button" class="flex min-h-11 w-full items-center justify-between gap-2 rounded-md px-3 py-2 text-left hover:bg-elevated/50" :data-recent-row="e.ref" :data-recent-receipt="e.receipt" @click="open(e.ref, { receipt: e.receipt, attempt: e.attempt })">
              <span class="min-w-0">
                <span class="block truncate text-sm">{{ titles.get(e.ref) || seenTitles[e.ref] || e.ref }}</span>
                <span class="block truncate text-xs text-muted">{{ relativeTime(e.at) }} ago</span>
              </span>
              <UBadge :label="recentWord(e).text" :color="recentWord(e).color" variant="subtle" size="sm" data-recent-word />
            </button>
          </li>
        </ul>
      </section>
    </template>

    <template v-else>
      <UButton label="Back to the list" icon="i-lucide-arrow-left" color="neutral" variant="ghost" size="lg" class="min-h-11 self-start" data-inbox-back @click="back" />
      <InboxItem
        v-if="item"
        :key="item.ref"
        :item="item"
        :route-of="() => null"
        :delivery="delivery"
        :busy="shownFeedback?.busy"
        :refusal="shownFeedback?.words"
        :unknown="shownFeedback?.unknown"
        :read-evidence="readEvidence"
        :open-terminal="openTerminal"
        no-comments
        @act="act"
        @reread="reread"
      />
      <div v-else-if="missing" class="flex flex-col gap-4">
        <UAlert color="neutral" variant="subtle" icon="i-lucide-info" :title="missing" data-inbox-missing />
        <section v-if="delivery?.length" class="flex flex-col gap-2" data-inbox-missing-delivery>
          <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">What became of your action</h3>
          <InboxDelivery :steps="delivery" :route-of="() => null" />
        </section>
      </div>
      <div v-else class="flex items-center gap-2 text-sm text-muted"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Reading the item…</div>
    </template>
  </div>
</template>
