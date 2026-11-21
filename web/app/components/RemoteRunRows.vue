<script setup lang="ts">
import { remoteRuns } from '~/utils/remote'

/**
 * Other machines' runs of the ticket `ticketKey` names (docs/plans/sync-seams.md, OS-4): each run's pointer as that machine sent it,
 * "running on studio", "finished on studio", read only and opening nothing here. Nothing is drawn with no sync store.
 */
const props = defineProps<{ ticketKey: string }>()
const remote = useRemote('pointer', () => props.ticketKey)
const rows = computed(() => remoteRuns(remote.records.value))
const tones = {
  active: 'text-primary',
  error: 'text-error',
  done: 'text-muted',
}
</script>

<template>
  <ul v-if="rows.length" class="flex flex-col gap-1.5" data-remote-runs>
    <li
      v-for="r in rows"
      :key="r.key"
      class="flex min-w-0 items-center gap-2 rounded-md px-3 py-2 text-sm opacity-80 ring ring-default"
      :data-remote-run="r.id"
    >
      <UIcon name="i-lucide-monitor-smartphone" class="size-4 flex-none text-muted" />
      <span class="min-w-0 truncate font-mono text-xs text-default" data-mono="id">{{ r.id }}</span>
      <span class="ml-auto flex-none text-xs" :class="tones[r.tone]" data-remote-run-words>{{ r.words }}</span>
    </li>
  </ul>
</template>
