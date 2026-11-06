<script setup lang="ts">
import type { BrakemanAct } from '~/utils/brakeman'
import { LAUNCH_FOLLOW_FIRST_MS, LAUNCH_FOLLOW_MAX_MS, actReceiptRef, actShown, actWords, followLaunch, refRoute, refTicket } from '~/utils/brakeman'
import type { LaunchSummary } from '~/composables/useDispatch'

/**
 * An act Brakeman ran under a grant the person made (PR-13), from the request's acts as the server keeps them, never from a brief's
 * words: "Brakeman did this" with its receipt and a link to the run it launched or the ticket it changed, or what else became of it. A
 * launch is done by its launch, not by its receipt, which Dispatch keeps before the run starts: the launch is read, and read again while
 * it is pending, and only a launch started says "Brakeman did this" and links its run; a launch that failed says so with its words. A
 * proposal that names the act draws it inside the proposal; the brief draws every other act of the request apart (actsApart). It tells
 * its parent what it shows (`shown`), which a proposal needs to offer its own control again or not.
 */
const props = defineProps<{ act: BrakemanAct }>()
const emit = defineEmits<{ shown: [state: string] }>()

const dispatch = useDispatchApi()

const receiptRef = computed(() => actReceiptRef(props.act))
const actTicket = computed(() => refTicket(props.act.target))
/** The launch an act's receipt names (GET /api/dispatch/launches/{id}), read until it has started or failed (followLaunch). */
const launch = ref<LaunchSummary | null>(null)
let following = 0
let gone = false
onBeforeUnmount(() => (gone = true))
watch(
  receiptRef,
  (ref) => {
    const g = ++following
    launch.value = null
    if (!ref) return
    void followLaunch({
      read: () => dispatch.launchRead(ref.slice('launch:'.length)),
      show: (l) => (launch.value = l),
      stop: () => gone || g !== following,
      wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      firstMs: LAUNCH_FOLLOW_FIRST_MS,
      maxMs: LAUNCH_FOLLOW_MAX_MS,
    })
  },
  { immediate: true },
)
/** What the card shows of the act: a launch's by its launch (actShown). */
const shown = computed(() => actShown(props.act, launch.value))
watch(shown, (s) => emit('shown', s), { immediate: true })
/** The run the act's launch started: only a launch started has one. */
const launchedRun = computed(() => (shown.value === 'done' && launch.value?.state === 'started' ? launch.value.runId : ''))
</script>

<template>
  <div class="flex flex-col gap-1 rounded-md border px-2 py-1.5" :class="shown === 'done' ? 'border-primary/40 bg-primary/5' : 'border-default bg-elevated/40'" data-proposal-did :data-act="act.opId" :data-act-shown="shown">
    <div class="flex flex-wrap items-center gap-1.5 text-sm">
      <UBadge :label="shown === 'done' ? 'Brakeman did this' : shown === 'pending' ? 'Brakeman is doing this' : 'Not done'" :color="shown === 'done' ? 'primary' : shown === 'refused' || shown === 'failed' ? 'warning' : 'neutral'" variant="subtle" size="sm" data-proposal-did-badge />
      <span class="text-xs text-muted" data-proposal-did-words>{{ actWords(act, launch) }}</span>
    </div>
    <div v-if="act.state === 'done'" class="flex flex-wrap items-center gap-1.5 text-xs text-muted">
      <BrakemanRefChip v-if="receiptRef" :ref-name="receiptRef" data-proposal-did-receipt />
      <UButton v-if="launchedRun" :to="`/runs/${encodeURIComponent(launchedRun)}`" label="The run it launched" icon="i-lucide-arrow-up-right" size="xs" color="neutral" variant="ghost" :data-proposal-did-run="launchedRun" />
      <UButton v-if="actTicket" :to="refRoute(`ticket:${actTicket}`) ?? undefined" :label="`${actTicket}, the ticket it changed`" icon="i-lucide-ticket" size="xs" color="neutral" variant="ghost" data-proposal-did-ticket />
    </div>
  </div>
</template>
