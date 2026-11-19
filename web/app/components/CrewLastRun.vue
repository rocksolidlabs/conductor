<script setup lang="ts">
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import { runAside, runLook, runPhase, runPhaseView, runWhen, tookWords } from '~/utils/crewWords'
import { runLive, runState } from '~/utils/runs'
import { runItems } from '~/utils/status'

/**
 * A saved crew's last run in its row of the home (C4): the run's state in the one vocabulary, its word beside its mark (never colour
 * alone), when it started and how long it took; "Never run" for a crew without one. The runs are the live ones and the server's
 * records (useCrewRecords), so a run that ended before the page opened is said as it ended: Failed for a run whose task failed (C1).
 */
const props = defineProps<{ crewId: string; live: RunInfo[]; sessions: SessionInfo[]; now: number }>()

const live = computed(() => props.live)
const records = useCrewRecords(() => props.crewId, live)
const needsYou = useNeedsYou()
const last = computed(() => records.runs.value[0])
const look = computed(() => {
  const r = last.value
  if (!r) return null
  const going = runLive(runState(r, props.sessions).state)
  // A record is as it ended; a live run is read with its items of the one list, as its card is.
  const view = r.record ? runPhaseView(runPhase(r, []), r.state === 'interrupted') : runLook(r, props.sessions, runItems(needsYou.items.value, r.id), props.now).view
  return { view, aside: runAside(r), when: runWhen(r.startedAt, props.now), took: tookWords(r, going, props.now) }
})
</script>

<template>
  <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-[12.5px] @min-[64rem]:flex-col @min-[64rem]:items-start" data-crew-last :data-runs="records.runs.value.length">
    <template v-if="look && last">
      <NuxtLink :to="`/runs/${encodeURIComponent(last.id)}`" class="flex min-w-0 items-center gap-1.5 hover:underline" :data-run="last.id" data-crew-last-state :data-phase="look.view.word">
        <StatusMark :status="look.view" word />
      </NuxtLink>
      <span v-if="look.aside" class="inline-flex min-w-0 items-center gap-1 text-xs text-muted" :title="look.aside.text" data-run-aside><UIcon :name="look.aside.icon" class="size-3.5 flex-none" /><span class="truncate">{{ look.aside.text }}</span></span>
      <span class="truncate text-muted" :title="`${look.when} · ${look.took}`" data-crew-last-run>{{ look.when }}<span class="hidden @min-[40rem]:inline"> · {{ look.took }}</span></span>
    </template>
    <span v-else class="text-muted" data-crew-last-run>Never run</span>
    <span v-if="records.error.value" class="text-xs text-warning" :title="records.error.value">records unavailable</span>
  </div>
</template>
