<script setup lang="ts">
import { relativeTime } from '~/utils/sessions'
import { HISTORY_DECISIONS, trackKey, type InboxSummary, type RecentEntry } from '~/utils/inbox'

/**
 * The decision page's "Decided in the last day": the server's records closed in the last day (the same for every viewer) and this
 * browser's own acts, ending with the way to every decision in History. On the page of nothing (`zero`) the link shows even when the
 * last day holds nothing. Drawn under the list, or inside the empty shape at zero.
 */
const props = defineProps<{
  closed: readonly InboxSummary[]
  recent: readonly RecentEntry[]
  /** A row's word for one of this browser's acts (recentBadge). */
  word: (e: RecentEntry) => { text: string; color: 'success' | 'warning' | 'error' | 'neutral' }
  zero?: boolean
}>()
const emit = defineEmits<{ open: [ref: string]; openRecent: [e: RecentEntry] }>()
const some = computed(() => props.closed.length > 0 || props.recent.length > 0)
</script>

<template>
  <section v-if="some || zero" class="flex flex-col gap-2 text-left" data-decisions-recent>
    <h2 v-if="some" class="text-xs font-semibold uppercase tracking-wide text-muted">Decided in the last day</h2>
    <!-- DR1's records closed in the last day: the server's, the same for every viewer. -->
    <template v-if="closed.length">
      <p class="text-xs text-muted">A run's decisions</p>
      <ul class="flex flex-col gap-1">
        <li v-for="s in closed" :key="s.ref">
          <button type="button" class="flex min-h-11 w-full items-center justify-between gap-2 rounded-md px-3 py-2 text-left hover:bg-elevated/50" :data-recent-decision="s.ref" @click="emit('open', s.ref)">
            <span class="min-w-0">
              <span class="block truncate text-sm">{{ s.title || s.ref }}</span>
              <span class="block truncate text-xs text-muted">{{ s.closed ? `${relativeTime(s.closed)} ago` : '' }}</span>
            </span>
            <UBadge :label="s.state || 'closed'" :color="s.state === 'applied' ? 'success' : 'neutral'" variant="subtle" size="sm" data-recent-word />
          </button>
        </li>
      </ul>
    </template>
    <template v-if="recent.length">
      <p class="text-xs text-muted">From this device</p>
      <ul class="flex flex-col gap-1">
        <li v-for="e in recent" :key="trackKey(e)">
          <button type="button" class="flex min-h-11 w-full items-center justify-between gap-2 rounded-md px-3 py-2 text-left hover:bg-elevated/50" :data-recent-row="e.ref" :data-recent-receipt="e.receipt" @click="emit('openRecent', e)">
            <span class="min-w-0">
              <span class="block truncate text-sm">{{ e.title || e.ref }}</span>
              <span class="block truncate text-xs text-muted">{{ e.label }} · {{ relativeTime(new Date(e.at).toISOString()) }} ago</span>
            </span>
            <UBadge :label="word(e).text" :color="word(e).color" variant="subtle" size="sm" data-recent-word />
          </button>
        </li>
      </ul>
    </template>
    <ULink :to="HISTORY_DECISIONS" class="inline-flex min-h-11 items-center gap-1 self-start text-sm text-muted hover:text-highlighted" data-decisions-history>
      <UIcon name="i-lucide-history" class="size-4" />
      Every decision, in History
    </ULink>
  </section>
</template>
