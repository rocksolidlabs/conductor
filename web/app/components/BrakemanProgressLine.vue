<script setup lang="ts">
import type { BrakemanProgress } from '~/utils/brakeman'
import type { ProgressOf } from '~/utils/brakemanProgress'
import { readCounts, readingWords, readsLabel, readsPercent, readCountWords, timeWords } from '~/utils/brakemanProgress'

/**
 * A Brakeman turn under way, in one line (docs/plans/brakeman.md; the Brakeman design review's progress line): the reads it has used
 * of its budget as a meter in neutral ink (a budget's count, not a state's mark), the time it has run of its deadline on the app's
 * one clock, and what it read last. The progress the server tells (the brakeman_progress event, GET /api/brakeman's `progress`) is
 * used when the page holds it; else the request's own reads and deadline, as last read. Drawn by the Brakeman page's In progress, the
 * run's and the ticket's panels and the answer under a task row. `compact` draws it smaller, for a panel or a row.
 */
const props = defineProps<{ request: ProgressOf; progress?: BrakemanProgress; compact?: boolean }>()

const live = useAttention()
const now = useNow()
const counts = computed(() => readCounts(props.request, props.progress))
const mine = computed(() => (props.progress?.id === props.request.id ? props.progress : undefined))
const time = computed(() => timeWords(mine.value?.startedAt, mine.value?.deadline ?? props.request.deadline, now.value))
const words = computed(() =>
  readingWords(mine.value?.reading, counts.value.used, {
    session: (id) => live.sessions.value.find((s) => s.id === id)?.name,
    run: (id) => live.runNames.value[id],
  }),
)
</script>

<template>
  <div class="flex min-w-0 flex-col" :class="compact ? 'gap-1 text-xs' : 'gap-1.5 text-sm'" data-progress :data-reads-used="counts.used" :data-reads-of="counts.of">
    <div class="flex min-w-0 items-baseline justify-between gap-3 tabular-nums">
      <span class="text-default">{{ readCountWords(counts) }}</span>
      <span v-if="time" class="shrink-0 text-muted" data-progress-time>{{ time }}</span>
    </div>
    <UProgress :model-value="readsPercent(counts)" :max="100" :size="compact ? '2xs' : 'xs'" color="neutral" role="img" :aria-label="readsLabel(counts)" />
    <span v-if="words" class="min-w-0 truncate text-muted" :title="words" data-progress-reading>{{ words }}</span>
  </div>
</template>
