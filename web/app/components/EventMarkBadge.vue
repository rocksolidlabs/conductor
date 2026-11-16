<script setup lang="ts">
import { EVENT_INFO } from '~/utils/events'

/**
 * The labelled badge a session carries in the sidebar and on its wall tile
 * for its newest event routed to Badge on the Events page (see useEvents).
 * A finished turn's mark is not drawn while the session's status says
 * finished (`finished`): its status mark says it. `icon` draws the event's
 * icon alone, its label and detail in its title and accessible name, as
 * YoloBadge's does: a Yard tile's header keeps its room for the name.
 */
const props = defineProps<{ sessionId: string; finished?: boolean; icon?: boolean }>()

const { marks } = useEvents()
const mark = computed(() => {
  const m = marks.value[props.sessionId]
  return m && !(m.type === 'done' && props.finished) ? m : undefined
})
const title = computed(() => {
  const m = mark.value
  if (!m) return ''
  const at = new Date(m.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return `${m.detail || EVENT_INFO[m.type].source} · ${at}`
})
/** The icon's colour, the badge's colour as a text class (listed whole, so that the stylesheet has each). */
const TONE: Record<string, string> = { warning: 'text-warning', success: 'text-success', error: 'text-error', info: 'text-info', primary: 'text-primary', neutral: 'text-muted' }
</script>

<template>
  <template v-if="mark">
    <UIcon
      v-if="props.icon"
      :name="EVENT_INFO[mark.type].icon"
      class="size-3.5 flex-none"
      :class="TONE[mark.color] ?? 'text-muted'"
      role="img"
      :aria-label="`${mark.label}: ${title}`"
      :title="`${mark.label} · ${title}`"
      data-event-mark
    />
    <UBadge v-else :label="mark.label" :color="mark.color" variant="subtle" size="sm" class="flex-none" :title="title" data-event-mark />
  </template>
</template>
