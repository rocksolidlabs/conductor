<script setup lang="ts">
import type { CrewFeedItem } from '~/utils/crews'
import type { EventColor } from '~/utils/events'

/**
 * The crew activity card: its members' events and the run's own log, newest
 * first (crewFeed). Text only, as the feed in History: nothing an agent sends is
 * rendered as HTML, and a URL is a link only when linkableUrl allowed it.
 */
defineProps<{ items: CrewFeedItem[] }>()

const DOT: Record<EventColor, string> = {
  warning: 'bg-warning',
  success: 'bg-success',
  error: 'bg-error',
  info: 'bg-info',
  neutral: 'bg-neutral-400 dark:bg-neutral-500',
}
</script>

<template>
  <section class="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-default" aria-labelledby="crew-feed-title" data-crew-feed>
    <div class="flex flex-none items-center border-b border-default px-3 py-2">
      <h3 id="crew-feed-title" class="text-[11px] font-semibold uppercase tracking-wider text-muted">Crew activity</h3>
      <ULink to="/history?view=feed" class="ml-auto text-xs font-medium text-secondary hover:underline">All events</ULink>
    </div>
    <p v-if="!items.length" class="px-3 py-4 text-sm text-muted">Nothing yet. Members' events and the run's own steps show up here.</p>
    <ol v-else class="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto px-3 py-2.5">
      <li v-for="e in items" :key="e.key" class="flex gap-2.5 text-[13px] leading-snug">
        <span class="flex-none pt-px font-mono text-[11px] text-muted">{{ e.time }}</span>
        <span class="mt-1.5 size-[7px] flex-none rounded-full" :class="DOT[e.color]" aria-hidden="true" />
        <span class="min-w-0 break-words">
          <NuxtLink v-if="e.who && e.sessionId" :to="`/sessions/${e.sessionId}`" class="font-mono text-xs font-semibold text-highlighted hover:underline">{{ e.who }}</NuxtLink>
          <b v-else-if="e.who" class="font-mono text-xs font-semibold text-highlighted">{{ e.who }}</b>
          {{ e.what }}
          <template v-if="e.url">
            ·
            <a v-if="e.link" :href="e.link" target="_blank" rel="noopener noreferrer" class="text-primary underline underline-offset-2 break-all">{{ e.url }}</a>
            <span v-else class="break-all">{{ e.url }}</span>
          </template>
        </span>
      </li>
    </ol>
  </section>
</template>
