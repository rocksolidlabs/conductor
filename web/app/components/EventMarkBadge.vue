<script setup lang="ts">
import { EVENT_INFO } from '~/utils/events'

/**
 * The labelled badge a session carries in the sidebar and on its wall tile
 * for its newest event routed to Badge on the Events page (see useEvents).
 * A finished turn's mark is not drawn while the session's status says
 * finished (`finished`): its status mark says it.
 */
const props = defineProps<{ sessionId: string; finished?: boolean }>()

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
</script>

<template>
  <UBadge v-if="mark" :label="mark.label" :color="mark.color" variant="subtle" size="sm" class="flex-none" :title="title" data-event-mark />
</template>
