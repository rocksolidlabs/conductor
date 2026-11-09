<script setup lang="ts">
import { classBadge, historyDetailParts, historyIcon, historySession, historyTarget, linkLine, repeatWords, type HistoryItem } from '~/utils/history'

/**
 * One row of the History page (Program item HS1, PR-7), as the palette draws it: what it is (its badge: Conversation, Decision,
 * Checked ✓ or ✗; a record has none), its title opening the page that shows its evidence, its six fields (the exact time and the full
 * commit on hover), its links, and the words it matched.
 */
const props = defineProps<{ entry: HistoryItem; now: number }>()

const badge = computed(() => classBadge(props.entry))
const to = computed(() => historyTarget(props.entry))
const parts = computed(() => historyDetailParts(props.entry, props.now))
const links = computed(() => [linkLine(props.entry), repeatWords(props.entry)].filter(Boolean).join(' · '))
const session = computed(() => {
  const s = historySession(props.entry)
  return s && s.url !== to.value ? s : null
})
</script>

<template>
  <li class="flex min-w-0 items-start gap-2.5 rounded-md px-2 py-2 hover:bg-elevated/60" :data-history-row="entry.key" :data-history-class="entry.class">
    <UIcon :name="historyIcon(entry)" class="mt-0.5 size-4 flex-none text-muted" aria-hidden="true" />
    <div class="min-w-0 flex-1">
      <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <NuxtLink v-if="to" :to="to" class="min-w-0 break-words text-sm font-medium text-highlighted hover:underline" data-history-row-title>{{ entry.title }}</NuxtLink>
        <span v-else class="min-w-0 break-words text-sm font-medium text-highlighted" data-history-row-title>{{ entry.title }}</span>
        <UBadge v-if="badge" :label="badge.label" :color="badge.color" variant="subtle" size="sm" class="flex-none" data-history-row-badge />
      </div>
      <!-- The six fields, wrapped on a phone. -->
      <p class="mt-0.5 flex flex-wrap gap-x-1.5 font-mono text-[11.5px] text-muted" data-history-row-detail>
        <template v-for="(p, i) in parts" :key="p.field">
          <span v-if="i" aria-hidden="true">·</span>
          <span :title="p.title" :data-history-field="p.field" class="break-all">{{ p.text }}</span>
        </template>
      </p>
      <p v-if="links" class="mt-0.5 text-xs text-toned" data-history-row-links>{{ links }}</p>
      <p v-if="entry.snippet" class="mt-1 line-clamp-2 break-words text-xs text-muted" data-history-row-snippet>{{ entry.snippet }}</p>
      <NuxtLink v-if="session" :to="session.url!" class="mt-0.5 inline-block text-xs text-muted hover:underline" data-history-row-session>{{ session.listed ? 'Open the session' : 'Open the record' }}</NuxtLink>
    </div>
  </li>
</template>
