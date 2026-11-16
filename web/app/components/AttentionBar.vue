<script setup lang="ts">
import type { SessionInfo } from '~/composables/useSessions'
import { isActive } from '~/utils/attention'
import { enough, STATUS_SLICES, statusSlices, statusSlicesLabel, type StatusSlice } from '~/utils/charts'

/**
 * The Yard's live sessions by status (utils/status.ts), as one slim stacked bar in its header: needs you (amber), working (green),
 * finished (neutral) and running, no turns reported (green at 40 % with a hatch, told from working by the hatch as the ring tells
 * them apart in a mark), each as wide as its share. The header's buttons carry the counts; the bar is the proportion. Drawn only
 * with two sessions or more.
 */
const props = defineProps<{ sessions: SessionInfo[] }>()

const slices = computed(() => statusSlices(props.sessions, isActive))
const total = computed(() => STATUS_SLICES.reduce((t, k) => t + slices.value[k], 0))
const cls: Record<StatusSlice, string> = {
  needs: 'bg-warning',
  working: 'bg-success',
  finished: 'bg-[var(--ui-text-muted)]',
  running: 'bg-success/40 bg-[repeating-linear-gradient(135deg,transparent_0_2px,var(--ui-bg)_2px_3px)]',
}
const label = computed(() => statusSlicesLabel(slices.value))
</script>

<template>
  <UTooltip v-if="enough(total)" :text="label">
    <div class="flex h-1.5 w-24 flex-none overflow-hidden rounded-full bg-elevated" role="img" :aria-label="label" data-attention-bar :data-total="total">
      <span v-for="k in STATUS_SLICES" :key="k" :class="cls[k]" :style="{ width: `${(slices[k] / total) * 100}%` }" :data-attention-slice="k" :data-count="slices[k]" />
    </div>
  </UTooltip>
</template>
