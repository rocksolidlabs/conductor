<script setup lang="ts">
import { relativeTime } from '~/utils/sessions'
import { provide } from 'vue'
import { recentBadge, trackKey } from '~/utils/inbox'
import { linkRefAccess, REF_ACCESS } from '~/utils/refAccess'
import type { JoinDecideState, JoinRecent } from '~/composables/useJoinDecide'

/**
 * The join page's Decide (docs/plans/decision-inbox.md, PR-7): what a decision link covers, over the link's own lane, end to end to
 * the server that owns it (through a switchyard, which stores nothing of it). The same cards as the decision page (InboxItem), paged
 * with More ("Showing 25 of 60"), each item read whole before it is acted on, its four steps, and the link's Recent, so an item an
 * action closed stays readable with its steps. Evidence is read through the lane (a check's log's end; a session in the scope opens
 * in its terminal tab, `openSession`); anything else is opened on the desktop or the LAN. What it shows is the page's (`state`,
 * useJoinDecide), kept through a reconnect: `offline` says the lane is being joined again meanwhile. While an action of an item is
 * owed, the item's other actions wait (`busy`): only a reply to that one, or a refusal of it, ends it; what an evidence entry or
 * a terminal could not open is said apart from it (`shownNotice`).
 *
 * A DR1 record is drawn by DR1's own card (DecisionCard) from the record assembled over the lane (useJoinDecide's `record`), with no
 * controls: a link cannot answer one yet, and its item's `now` says where it is answered. Its evidence chips read through the link
 * (REF_ACCESS, provided here: a session's chip opens that member's terminal tab, a check's reads its log's end through the lane, any
 * other says where it is opened), and nothing on this page reads the server's API.
 */
const props = defineProps<{ state: JoinDecideState; title?: string; offline?: boolean }>()
const emit = defineEmits<{ openSession: [session: string] }>()

// The page's state for its whole life: its refs drawn as the template's own.
const { items, next, loaded, error, showing, actions, titles, seenTitles, tracks, openRef, item, missing, record, recordWords, delivery, shownFeedback, shownNotice, more, open, back, act, reread, readEvidence } = props.state

/** A session in the scope, in its terminal tab. */
async function openTerminal(ref: string) {
  const id = await props.state.sessionOf(ref)
  if (id) emit('openSession', id)
}

// What DR1's card draws reads through the link: its chips, and whether its run runs here (not known on a link).
provide(REF_ACCESS, linkRefAccess({ evidence: (r) => readEvidence(r), openSession: (r) => void openTerminal(r) }))
/** The clock DR1's card reads its times by. */
const now = ref(Date.now())
let tick: number | undefined
onMounted(() => (tick = window.setInterval(() => (now.value = Date.now()), 5000)))
onBeforeUnmount(() => window.clearInterval(tick))

/** A Recent row's word: Refused, Work resumed, Taken or what the resumed step says (recentBadge). */
const recentWord = (e: JoinRecent) => recentBadge({ ref: e.ref, receipt: e.receipt, attempt: e.attempt, outcome: 'accepted', at: Date.parse(e.at) || 0 }, tracks.value[trackKey(e)], false)
</script>

<template>
  <div class="flex min-w-0 flex-col gap-4" data-join-decide>
    <UAlert v-if="offline" color="neutral" variant="subtle" icon="i-lucide-loader-circle" title="Connecting again" description="The connection that carries this link's decisions dropped. What you did here is kept, and an action no word came back for is asked again under its own id once the link is connected again." data-decide-offline />
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
              <UBadge v-if="s.kind === 'decision'" :label="s.tracking ? 'Answered, not yet applied' : 'A durable decision'" color="neutral" variant="subtle" size="sm" data-inbox-row-durable />
              <UBadge v-else-if="!s.actions.length" label="Open the terminal" color="warning" variant="subtle" size="sm" />
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
      <UAlert v-if="shownNotice" color="neutral" variant="subtle" icon="i-lucide-info" :title="shownNotice" data-decide-notice />
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
        :drawn="record?.ref === item.ref ? record.decision.request.evidence : undefined"
        @act="act"
        @reread="reread"
      >
        <template #record>
          <UAlert v-if="recordWords" color="neutral" variant="subtle" icon="i-lucide-info" :title="recordWords" data-inbox-record-words />
          <DecisionCard v-if="record && record.ref === item.ref" :decision="record.decision" :refs="record.refs" :now="now" />
          <p v-else-if="!recordWords" class="flex items-center gap-2 text-sm text-muted"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Reading the decision…</p>
        </template>
      </InboxItem>
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
