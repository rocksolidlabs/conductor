<script setup lang="ts">
import type { BrakemanAct } from '~/utils/brakeman'
import { LAUNCH_FOLLOW_FIRST_MS, LAUNCH_FOLLOW_MAX_MS, actLaunchId, actLaunchRef, actReceiptRef, actShownFrom, actWords, followLaunch, keepLaunch, refRoute, refTicket } from '~/utils/brakeman'
import type { LaunchSummary } from '~/composables/useDispatch'

/**
 * An act Brakeman ran under a grant the person made (PR-13), from the request's acts as the server keeps them, never from a brief's
 * words: "Brakeman did this" with its receipt (the act's own, as an outcome's chip shows it), the launch it made, and a link to the run
 * it launched or the ticket it changed, or what else became of it. A
 * launch is done by its launch, not by its receipt, which Dispatch keeps before the run starts: the launch is read, and read again while
 * it is pending, and only a launch started says "Brakeman did this" and links its run; a launch that failed says so with its words. A
 * proposal that names the act draws it inside the proposal; the brief draws every other act of the request apart (actsApart). The
 * launch it reads is kept for the page by the launch's id (useActLaunches), where the proposal reads it too: both draw what they show
 * from the act and that launch (actShownFrom), so a re-read of the request, which hands the same act as a new object, changes neither.
 */
const props = defineProps<{ act: BrakemanAct }>()

const dispatch = useDispatchApi()

/** The act's receipt (op:brakeman/<op>), the ref an outcome of its brief cites and the chip there shows; and the launch it made. */
const receiptRef = computed(() => actReceiptRef(props.act))
const launchRef = computed(() => actLaunchRef(props.act))
const actTicket = computed(() => refTicket(props.act.target))
/** The launch an act's receipt names (GET /api/dispatch/launches/{id}), read until it has started or failed (followLaunch), kept by its id. */
const launches = useActLaunches()
const launchId = computed(() => actLaunchId(props.act))
const launch = computed(() => (launchId.value ? (launches.value[launchId.value] ?? null) : null))
let following = 0
let gone = false
onBeforeUnmount(() => (gone = true))
watch(
  launchId,
  (id) => {
    const g = ++following
    // A launch read settled already (started or failed) is not read again: what it shows stays.
    if (!id || (launches.value[id] && launches.value[id]!.state !== 'pending')) return
    void followLaunch({
      read: () => dispatch.launchRead(id),
      show: (l) => (launches.value = keepLaunch<LaunchSummary>(launches.value, id, l)),
      stop: () => gone || g !== following,
      wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      firstMs: LAUNCH_FOLLOW_FIRST_MS,
      maxMs: LAUNCH_FOLLOW_MAX_MS,
    })
  },
  { immediate: true },
)
/** What the card shows of the act: a launch's by its launch (actShownFrom). */
const shown = computed(() => actShownFrom(props.act, launches.value))
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
      <BrakemanRefChip v-if="launchRef" :ref-name="launchRef" data-proposal-did-launch />
      <UButton v-if="launchedRun" :to="`/runs/${encodeURIComponent(launchedRun)}`" label="The run it launched" icon="i-lucide-arrow-up-right" size="xs" color="neutral" variant="ghost" :data-proposal-did-run="launchedRun" />
      <UButton v-if="actTicket" :to="refRoute(`ticket:${actTicket}`) ?? undefined" :label="`${actTicket}, the ticket it changed`" icon="i-lucide-ticket" size="xs" color="neutral" variant="ghost" data-proposal-did-ticket />
    </div>
  </div>
</template>
