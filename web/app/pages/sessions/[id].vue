<script setup lang="ts">
import type { SessionInfo } from '~/composables/useSessions'
import { memberPlace } from '~/utils/memberStrip'

// A session's page: its whole view (SessionView). A crew's member opens inside its crew (UX3 R72, docs/design/system.md, "A run's
// member opens inside its run"): its old address is replaced by its place there, `tab`, `open` and `line` carried, before its terminal
// connects. Decided here when the live list already holds the session, else by the view once it has read it, before it draws it.
// Any other session, a Brakeman turn's among them, stays.
const route = useRoute()
const live = useAttention()
const id = computed(() => String(route.params.id))
const placeOf = (s: Pick<SessionInfo, 'id' | 'crew'>) => memberPlace(s, route.query)
const leaving = ref('')
watch(
  id,
  (sid) => {
    const known = live.sessions.value.find((s) => s.id === sid)
    leaving.value = known ? placeOf(known) : ''
    if (leaving.value) void navigateTo(leaving.value, { replace: true })
  },
  { immediate: true },
)
</script>

<template>
  <SessionView v-if="!leaving" :key="id" :session-id="id" :leave="placeOf" />
</template>
