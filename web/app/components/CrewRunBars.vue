<script setup lang="ts">
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import { barClass, minutesLabel, runBars, runWhen } from '~/utils/crewWords'

/**
 * A saved crew's last runs in its table row: one bar per run, its height how
 * long the run went, its colour how it ended (grey ended, lighter grey
 * stopped, red a member failed, the working green still going), an amber edge when
 * someone had to answer; then when the last run started, or "never". The
 * runs are the live ones and the server's records (useCrewRecords).
 */
const props = defineProps<{ crewId: string; live: RunInfo[]; sessions: SessionInfo[]; now: number }>()

const live = computed(() => props.live)
const records = useCrewRecords(() => props.crewId, live)
const bars = computed(() => runBars(records.runs.value, props.sessions, props.now))
const max = computed(() => Math.max(0.1, ...bars.value.map((b) => b.minutes)))
const last = computed(() => records.runs.value[0])
</script>

<template>
  <div class="flex h-7 items-end gap-[3px]" data-crew-bars :data-runs="bars.length">
    <span
      v-for="b in bars"
      :key="b.id"
      class="w-2 flex-none rounded-sm"
      :class="[barClass(b.outcome), b.asked && 'shadow-[inset_0_3px_0_var(--ui-warning)]']"
      :style="{ height: `${Math.max(3, Math.round((b.minutes / max) * 28))}px` }"
      :title="`${b.id} · ${minutesLabel(b.minutes)} · ${b.outcome}${b.asked ? ` · needed an answer ×${b.asked}` : ''}`"
      data-run-bar
      :data-outcome="b.outcome"
    />
    <span class="self-center text-xs text-muted" :class="bars.length ? 'ml-2' : ''" data-crew-last-run>{{ last ? runWhen(last.startedAt, now) : 'never' }}</span>
    <span v-if="records.error.value" class="self-center text-xs text-warning" :title="records.error.value">records unavailable</span>
  </div>
</template>
