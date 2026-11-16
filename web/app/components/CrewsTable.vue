<script setup lang="ts">
import type { CrewSummary, RunInfo, SessionInfo } from '~/composables/useSessions'
import { crewShape } from '~/utils/crewWords'
import { runLive, runState } from '~/utils/runs'
import { shortCwd } from '~/utils/sessions'

/**
 * The saved crews: plans, one row each. Its name and goal (and "1 live run"
 * when one is going, leading to it), its shape as a mini graph with a few
 * words, where it runs, its last runs as bars, Edit and Launch run. Nothing
 * here runs until a crew is launched.
 */
const props = defineProps<{ crews: CrewSummary[]; runs: RunInfo[]; sessions: SessionInfo[]; now: number; launching: string }>()
const emit = defineEmits<{ launch: [id: string] }>()

const serverHost = useServerHost()

const cols = 'grid-cols-[minmax(0,1fr)_auto] @min-[64rem]:grid-cols-[minmax(0,1.3fr)_11rem_8rem_minmax(0,1fr)_13rem]'

function liveOf(id: string): RunInfo[] {
  return props.runs.filter((r) => r.crewId === id && runLive(runState(r, props.sessions).state))
}
function allOf(id: string): RunInfo[] {
  return props.runs.filter((r) => r.crewId === id)
}
/** "1 live run" leads to the run; more lead to the crew's Runs tab. */
function liveTo(id: string): string {
  const live = liveOf(id)
  return live.length === 1 ? `/runs/${encodeURIComponent(live[0]!.id)}` : `/crews/${encodeURIComponent(id)}?tab=runs`
}
function where(c: CrewSummary): { cwd: string; mode: string } {
  return { cwd: shortCwd(c.cwd) || 'server default', mode: c.isolation === 'worktree' ? 'worktrees' : 'shared cwd' }
}
</script>

<template>
  <div class="@container overflow-hidden rounded-lg ring ring-default" data-crews-table>
    <div class="hidden gap-4 border-b border-default bg-muted px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-muted @min-[64rem]:grid" :class="cols" aria-hidden="true">
      <span>Crew</span>
      <span>Shape</span>
      <span>Where</span>
      <span>Last runs</span>
      <span />
    </div>
    <div v-for="c in crews" :key="c.id" class="grid items-center gap-x-4 gap-y-2 border-b border-default px-4 py-3 last:border-b-0" :class="cols" data-crew-entry :data-crew-id="c.id">
      <div class="col-start-1 row-start-1 flex min-w-0 flex-col gap-0.5">
        <div class="flex min-w-0 items-center gap-2">
          <NuxtLink :to="`/crews/${encodeURIComponent(c.id)}`" class="truncate text-[14.5px] font-semibold text-highlighted hover:underline" data-crew-item>{{ c.name || 'Untitled crew' }}</NuxtLink>
          <NuxtLink v-if="liveOf(c.id).length" :to="liveTo(c.id)" class="flex flex-none items-center gap-1 text-[11px] text-muted hover:underline" data-crew-live>
            <span class="size-1.5 rounded-full bg-(--ui-text-muted)" aria-hidden="true" />{{ liveOf(c.id).length }} live {{ liveOf(c.id).length === 1 ? 'run' : 'runs' }}
          </NuxtLink>
        </div>
        <span class="truncate text-[12.5px] text-muted" :title="c.goal">{{ c.goal || 'no goal yet' }}</span>
      </div>

      <div class="col-start-2 row-start-1 flex items-center justify-end gap-1.5 @min-[64rem]:col-start-5">
        <UButton :to="`/crews/${encodeURIComponent(c.id)}`" label="Edit" size="xs" color="neutral" variant="outline" />
        <UButton label="Launch run" icon="i-lucide-play" size="xs" variant="outline" :loading="launching === c.id" :disabled="!c.members.length || c.where !== 'server' || serverHost.switchyard.value" :data-launch-crew="c.id" @click="emit('launch', c.id)" />
      </div>

      <div class="col-span-2 row-start-2 flex flex-wrap items-center gap-x-6 gap-y-2 @min-[64rem]:contents">
        <div class="flex flex-col gap-1 @min-[64rem]:col-start-2 @min-[64rem]:row-start-1">
          <CrewMiniDag :members="c.members" size="mini" :max-width="150" />
          <span class="text-[11.5px] text-muted" data-crew-shape>{{ crewShape(c.members) }}</span>
        </div>
        <span class="flex min-w-0 flex-col font-mono text-[11.5px] leading-normal text-muted @min-[64rem]:col-start-3 @min-[64rem]:row-start-1" data-crew-meta>
          <span class="truncate" :title="c.cwd || 'server default'">{{ where(c).cwd }}</span>
          <span>{{ where(c).mode }}</span>
        </span>
        <CrewRunBars :crew-id="c.id" :live="allOf(c.id)" :sessions="sessions" :now="now" class="@min-[64rem]:col-start-4 @min-[64rem]:row-start-1" />
      </div>
    </div>
  </div>
</template>
