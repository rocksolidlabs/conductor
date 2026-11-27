<script setup lang="ts">
import { COLOR_TEXT, EVENT_INFO, feedGroups, type FeedEntry } from '~/utils/events'
import { agentInitials } from '~/utils/sessions'
// A link to a session opens its place: a crew's member inside its crew (UX3 R72).
const placeOf = useSessionPlace()

/**
 * The live feed, newest first, in groups of one minute. Each row: its time,
 * the event's icon, who reported it (a crew member as "<crew> / <member>"),
 * what happened, and the one thing to do about it: Answer a question, open
 * the run a handoff belongs to, open what an artifact points at. Text only:
 * nothing an agent sends is rendered as HTML, and `link` is set only for a
 * URL linkableUrl allows.
 */
const props = defineProps<{ entries: FeedEntry[]; now: number }>()

const live = useAttention()
const groups = computed(() => feedGroups(props.entries, props.now))

function who(e: FeedEntry): { name: string; agent: string; runId?: string } {
  const s = live.sessions.value.find((x) => x.id === e.sessionId)
  if (s?.crew) return { name: `${live.runNames.value[s.crew.runId] || s.crew.crewId} / ${s.crew.member}`, agent: s.agentId, runId: s.crew.runId }
  return { name: e.sessionName, agent: s?.agentId ?? '' }
}
</script>

<template>
  <section class="flex min-w-0 flex-col" aria-label="Live feed" data-event-feed>
    <!-- The feed is this tab's alone (useEvents keeps it in memory): empty at every load, so its words say so. -->
    <PageEmpty
      v-if="!entries.length"
      page="history-feed"
      state="empty"
      words="Nothing yet since this tab opened. An agent reports when it needs input, finishes a turn, hands work off or hits an error."
      :actions="[{ label: 'Which agents report', icon: 'i-lucide-plug', to: '/agents#reporting' }]"
      data-feed-empty
    />
    <div v-for="g in groups" :key="g.key" class="@container flex flex-col" data-feed-group>
      <div class="pt-3 pb-1 text-[11px] font-semibold uppercase tracking-wider text-muted">{{ g.label }}</div>
      <ol class="flex flex-col">
        <li
          v-for="e in g.entries"
          :key="e.seq"
          class="-mx-2.5 grid grid-cols-[3.5rem_1rem_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-0.5 rounded-md px-2.5 py-2 @min-[40rem]:grid-cols-[4rem_1.125rem_10.5rem_minmax(0,1fr)_auto]"
          :class="e.event === 'needs_input' && 'bg-warning/10'"
          :data-event="e.event"
        >
          <span class="font-mono text-[11.5px] text-muted">{{ e.time }}</span>
          <UIcon :name="EVENT_INFO[e.event].icon" class="size-4" :class="COLOR_TEXT[EVENT_INFO[e.event].color]" />
          <!-- One cell on a phone (what happened, then who); two columns, who first, from 40rem. -->
          <div class="flex min-w-0 flex-col gap-0.5 @min-[40rem]:contents">
            <NuxtLink :to="placeOf(e.sessionId)" class="order-2 flex min-w-0 items-center gap-1.5 hover:underline @min-[40rem]:order-none" data-feed-who>
              <span v-if="who(e).agent" class="grid h-[18px] w-5 flex-none place-items-center rounded bg-elevated font-mono text-[8.5px] font-semibold">{{ agentInitials(who(e).agent) }}</span>
              <span class="truncate font-mono text-[12.5px] font-medium text-highlighted">{{ who(e).name }}</span>
            </NuxtLink>
            <span class="min-w-0 truncate text-[13.5px]" :title="[e.detail, e.url].filter(Boolean).join(' · ')">
              <b class="font-medium" :class="COLOR_TEXT[EVENT_INFO[e.event].color]">{{ EVENT_INFO[e.event].label }}</b>
              <span v-if="e.detail" class="ml-1">{{ e.detail }}</span>
              <template v-if="e.url">
                <span class="mx-1">·</span>
                <a v-if="e.link" :href="e.link" target="_blank" rel="noopener noreferrer" class="text-secondary underline underline-offset-2" data-event-link>{{ e.url }}</a>
                <span v-else>{{ e.url }}</span>
              </template>
            </span>
          </div>
          <span class="flex justify-end">
            <UButton v-if="e.event === 'needs_input'" :to="placeOf(e.sessionId)" label="Answer" size="xs" data-feed-answer />
            <UButton v-else-if="e.event === 'handoff' && who(e).runId" :to="`/runs/${encodeURIComponent(who(e).runId!)}`" label="Open run" size="xs" color="neutral" variant="outline" />
            <UButton v-else-if="e.link" :href="e.link" target="_blank" rel="noopener noreferrer" label="Open" size="xs" color="neutral" variant="outline" />
          </span>
        </li>
      </ol>
    </div>
  </section>
</template>
