<script setup lang="ts">
import { classBadge, historyIcon, historySession, linkLine, repeatWords, type HistoryItem } from '~/utils/history'
import { clockTime, rowAction, rowDetails, rowStatus, sessionIdOf } from '~/utils/historyRows'
import { isEnded } from '~/utils/attention'
import { sessionResumeOffered } from '~/utils/runs'

/**
 * One row of the History page (Program item HS1, PR-7; the review of its screens, #1024): its name first, its state in one word (a
 * session's in the app's status language, StatusMark; another entry's kind of evidence, its badge), its time at the right; under it one
 * line of details (agent · repository · branch, the session's id at its end, copied by a press; the folder in the line's title) and
 * the row's action written out: Open, or Transcript for a session that ended, with Resume beside it where the sidebar offers it. The
 * whole row is the one link (its name's), Enter on it does the action; the id, Resume and the session's link sit above it.
 */
const props = withDefaults(defineProps<{ entry: HistoryItem; now: number; searched?: boolean }>(), { searched: false })

const attention = useAttention()
const copy = useCopy()
const id = computed(() => sessionIdOf(props.entry))
/** The session as the sidebar lists it, where it is listed: its live state and the sidebar's Resume. */
const listed = computed(() => (id.value ? (attention.sessions.value.find((s) => s.id === id.value) ?? null) : null))
const status = computed(() => rowStatus(props.entry, listed.value, props.now))
const badge = computed(() => (status.value ? null : classBadge(props.entry)))
const action = computed(() => rowAction(props.entry, listed.value))
const details = computed(() => rowDetails(props.entry))
const time = computed(() => clockTime(props.entry.at))
// "in 2 places" says where the words were found: only for a search, never on the latest sessions.
const links = computed(() => [linkLine(props.entry), props.searched ? repeatWords(props.entry) : ''].filter(Boolean).join(' · '))
// A session row's summary repeats its line (agent · folder · ending): only another entry's words are shown.
const snippet = computed(() => (props.entry.kind === 'session' ? '' : props.entry.snippet))
const session = computed(() => {
  const s = historySession(props.entry)
  return s && s.url !== action.value?.to ? s : null
})
/** The sidebar's rule for its Exited list's Resume: an ended server session the engine would take back. */
const resumable = computed(() => !!listed.value && isEnded(listed.value.status) && listed.value.kind === 'server' && sessionResumeOffered(listed.value, attention.runOf))
// The line's title holds what it does not print: the folder, and the id (a phone's line leaves the id out).
const lineTitle = computed(() => [details.value.path, id.value ? `id ${id.value}` : '', props.entry.kind === 'session' ? '' : props.entry.summary].filter(Boolean).join('\n') || undefined)
</script>

<template>
  <li class="group/row relative flex min-w-0 items-start gap-3 rounded-md px-2 py-2.5 hover:bg-elevated/60 has-[[data-history-row-title]:focus-visible]:bg-elevated/60" :data-history-row="entry.key" :data-history-class="entry.class">
    <UIcon :name="historyIcon(entry)" class="mt-0.5 size-4 flex-none text-muted" aria-hidden="true" />
    <div class="min-w-0 flex-1">
      <div class="flex min-w-0 items-center gap-2">
        <!-- The row's one link: its box covers the whole row (after:inset-0); its name is the row's first text. -->
        <NuxtLink
          v-if="action"
          :to="action.to"
          :title="entry.title"
          :aria-label="`${entry.title}: ${action.label}`"
          class="min-w-0 truncate text-sm font-medium text-highlighted after:absolute after:inset-0 after:rounded-md focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-primary"
          data-history-row-title
          >{{ entry.title }}</NuxtLink
        >
        <span v-else :title="entry.title" class="min-w-0 truncate text-sm font-medium text-highlighted" data-history-row-title>{{ entry.title }}</span>
        <StatusMark v-if="status" :status="status" word class="text-xs text-toned" data-history-row-state />
        <UBadge v-else-if="badge" :label="badge.label" :color="badge.color" variant="subtle" size="sm" class="flex-none" data-history-row-badge />
        <time v-if="time" :datetime="entry.at" :title="new Date(entry.at).toLocaleString()" class="ml-auto flex-none pl-2 text-xs text-muted tabular-nums" data-history-row-time>{{ time }}</time>
      </div>
      <!-- One line on a wide window; a phone's narrow column wraps the details (none cut to nothing), the action at their right. -->
      <div class="mt-0.5 flex min-w-0 items-end gap-2 sm:items-center">
        <p class="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 text-xs text-muted sm:flex-nowrap sm:overflow-hidden sm:whitespace-nowrap" :title="lineTitle" data-history-row-detail>
          <template v-for="(p, i) in details.parts" :key="p.field">
            <span v-if="i" aria-hidden="true">·</span>
            <span :title="p.title" :data-history-field="p.field" class="max-w-full min-w-0 truncate">{{ p.text }}</span>
          </template>
          <!-- The id: at the line's end on a wide window; a phone leaves it to the line's title and the session's own page. -->
          <template v-if="id">
            <span v-if="details.parts.length" aria-hidden="true" class="max-sm:hidden">·</span>
            <button type="button" class="relative z-10 flex-none max-sm:hidden rounded-sm font-mono text-[11px] text-muted hover:text-toned hover:underline focus-visible:outline-2 focus-visible:outline-primary" :aria-label="`Copy the id ${id}`" :title="`Copy the id ${id}`" data-mono="id" data-history-row-id @click="copy(id, 'Id copied', id)">{{ id }}</button>
          </template>
        </p>
        <ResumeButton v-if="resumable && listed" :session="listed" size="xs" class="relative z-10 flex-none max-sm:min-h-11" />
        <span v-if="action" class="flex flex-none items-center gap-0.5 text-xs font-medium text-primary group-hover/row:underline" aria-hidden="true" data-history-row-action
          >{{ action.label }}<UIcon name="i-lucide-chevron-right" class="size-3.5"
        /></span>
      </div>
      <p v-if="links" class="mt-0.5 text-xs text-toned" data-history-row-links>{{ links }}</p>
      <p v-if="snippet" class="mt-1 line-clamp-2 break-words text-xs text-muted" data-history-row-snippet>{{ snippet }}</p>
      <NuxtLink v-if="session" :to="session.url!" class="relative z-10 mt-0.5 inline-block text-xs text-muted hover:underline" data-history-row-session>{{ session.listed ? 'Open the session' : 'Open the record' }}</NuxtLink>
    </div>
  </li>
</template>
