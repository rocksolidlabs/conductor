<script setup lang="ts">
import type { AgentInfo, CrewSummary, RunInfo, SessionInfo } from '~/composables/useSessions'
import { crewMembersWords } from '~/utils/crewWords'
import { runLive, runState } from '~/utils/runs'

/**
 * The saved crews: plans, one row each (C4). Its name, the way into it, and its goal; its members, how many and on which agents; its
 * last run, the state's word with its mark, when and how long ("Never run"); Launch run. Where a crew runs and how its members start
 * are on the crew's own page. Nothing here runs until a crew is launched. On a phone a crew is two lines and its button (C14). A
 * launch from here is said in the crew's row, under its button, with Open run (C11); the run is under Running now at once.
 */
const props = defineProps<{
  crews: CrewSummary[]
  runs: RunInfo[]
  sessions: SessionInfo[]
  now: number
  launching: string
  /** The run this page launched (C11): said in its crew's row, where Launch run was pressed, with Open run, until dismissed. */
  started?: { crewId: string; runId: string; label: string } | null
}>()
const emit = defineEmits<{ launch: [id: string]; dismiss: [] }>()

const serverHost = useServerHost()
const api = useSessions()

// The agents' names, read once: a member's agent is said by its name ("Claude Code"), its id while the catalog is not read.
const agents = ref<AgentInfo[]>([])
onMounted(() => {
  api
    .catalog()
    .then((c) => (agents.value = c))
    .catch(() => {})
})
const names = computed(() => new Map(agents.value.map((a) => [a.id, a.name])))

const cols = 'grid-cols-[minmax(0,1fr)_auto] @min-[64rem]:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_minmax(10rem,0.8fr)_8rem]'

function runsOf(id: string): RunInfo[] {
  return props.runs.filter((r) => r.crewId === id)
}
function liveOf(id: string): RunInfo[] {
  return runsOf(id).filter((r) => runLive(runState(r, props.sessions).state))
}
</script>

<template>
  <div class="@container overflow-hidden rounded-lg ring ring-default" data-crews-table>
    <div class="hidden gap-4 border-b border-default bg-muted px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-muted @min-[64rem]:grid" :class="cols" aria-hidden="true">
      <span>Crew</span>
      <span>Members</span>
      <span>Last run</span>
      <span />
    </div>
    <div v-for="c in crews" :key="c.id" class="grid items-center gap-x-4 gap-y-1 border-b border-default px-4 py-3 last:border-b-0" :class="cols" data-crew-entry :data-crew-id="c.id" :data-live-runs="liveOf(c.id).length">
      <div class="col-start-1 row-start-1 flex min-w-0 flex-col gap-0.5">
        <NuxtLink :to="`/crews/${encodeURIComponent(c.id)}`" class="truncate text-[14.5px] font-semibold text-highlighted hover:underline" :title="c.name" data-crew-item>{{ c.name || 'Untitled crew' }}</NuxtLink>
        <span class="hidden truncate text-[12.5px] text-muted @min-[40rem]:block" :title="c.goal" data-crew-goal>{{ c.goal || 'no goal yet' }}</span>
      </div>

      <div class="col-start-2 row-span-2 row-start-1 flex items-center justify-end @min-[64rem]:col-start-4 @min-[64rem]:row-span-1">
        <UButton
          label="Launch run"
          icon="i-lucide-play"
          size="sm"
          variant="outline"
          class="max-sm:min-h-11"
          :loading="launching === c.id"
          :disabled="!c.members.length || c.where !== 'server' || serverHost.switchyard.value"
          :data-launch-crew="c.id"
          @click="emit('launch', c.id)"
        />
      </div>

      <!-- On a phone the row is two lines (C14): the name, then the last run; the goal and the members from 40rem, in columns from 64rem. -->
      <div class="col-start-1 row-start-2 flex min-w-0 flex-wrap items-center gap-x-4 gap-y-0.5 @min-[64rem]:contents">
        <span class="hidden min-w-0 truncate text-[12.5px] text-default @min-[40rem]:block @min-[64rem]:col-start-2 @min-[64rem]:row-start-1" :title="crewMembersWords(c.members, names)" data-crew-members>{{ crewMembersWords(c.members, names) }}</span>
        <CrewLastRun :crew-id="c.id" :live="runsOf(c.id)" :sessions="sessions" :now="now" class="@min-[64rem]:col-start-3 @min-[64rem]:row-start-1" />
      </div>
      <div
        v-if="started && started.crewId === c.id"
        class="col-span-2 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 rounded-md bg-elevated px-3 py-1.5 text-[13px] @min-[64rem]:col-span-4"
        role="status"
        data-crew-started
        :data-run="started.runId"
      >
        <UIcon name="i-lucide-circle-play" class="size-4 flex-none text-muted" />
        <span class="min-w-0"><b class="font-semibold text-highlighted">Started {{ c.name }}</b> <span class="text-muted">· {{ started.label }}</span></span>
        <div class="ml-auto flex flex-none items-center gap-1">
          <UButton :to="`/runs/${encodeURIComponent(started.runId)}`" label="Open run" trailing-icon="i-lucide-arrow-right" size="xs" color="neutral" variant="outline" class="max-sm:min-h-11" />
          <IconButton icon="i-lucide-x" color="neutral" variant="ghost" size="xs" label="Dismiss" @click="emit('dismiss')" />
        </div>
      </div>
    </div>
  </div>
</template>
