<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import { enough } from '~/utils/charts'
import { barClass, barsSummary, DEFAULT_RUN_FILTER, filterCounts, filterRuns, memberTiles, minutesLabel, outcomeBadge, readRunFilter, RUN_FILTER_KEY, runBars, runNote, runOutcome, runResumable, runTook, runWhen, shortRunId, type RunFilter, type RunFilterSince, type RunFilterState } from '~/utils/crewWords'
import { runLive, runState } from '~/utils/runs'
import { agentInitials } from '~/utils/sessions'

/**
 * A crew's Runs tab: its last runs as bars (how long each went, how it
 * ended, an amber edge when someone had to answer), then every run as a row:
 * when it started with its short id, its state, its members with their
 * status, how long it took, a note (who asks, what failed, what a stop cut
 * off, what changed) and what can be done with it: Stop and Open while it
 * goes, Resume as new run and Open after. The runs are the live ones and
 * the server's records, newest first (useCrewRecords).
 */
const props = defineProps<{ runs: RunInfo[]; sessions: SessionInfo[]; now: number; error?: string }>()

const api = useSessions()
const live = useAttention()
const toast = useToast()

/** How many rows show before "Show N more". */
const SHOWN = 5
const all = ref(false)

const bars = computed(() => runBars(props.runs, props.sessions, props.now))
const max = computed(() => Math.max(0.1, ...bars.value.map((b) => b.minutes)))
const axis = computed(() => {
  const b = bars.value
  if (b.length < 2) return []
  const first = runWhen(new Date(b[0]!.startedAt).toISOString(), props.now, false)
  const last = runWhen(new Date(b.at(-1)!.startedAt).toISOString(), props.now, false)
  const midBar = b[Math.floor(b.length / 2)]!
  const mid = b.length > 2 ? runWhen(new Date(midBar.startedAt).toISOString(), props.now, false) : ''
  return [first, mid, last].filter((x, i, a) => x && a.indexOf(x) === i)
})

// The filter is this browser's habit (localStorage), not part of a link; changing it folds "Show more".
function readFilter(): RunFilter {
  try {
    return readRunFilter(localStorage.getItem(RUN_FILTER_KEY))
  } catch {
    return { ...DEFAULT_RUN_FILTER }
  }
}
const filter = ref<RunFilter>(readFilter())
watch(
  filter,
  (f) => {
    all.value = false
    try {
      localStorage.setItem(RUN_FILTER_KEY, JSON.stringify(f))
    } catch {
      /* storage refused: the filter lasts the page */
    }
  },
  { deep: true },
)
const outcomes = computed(() => props.runs.map((r) => ({ run: r, outcome: runOutcome(r, runState(r, props.sessions).state) })))
const matching = computed(() => filterRuns(outcomes.value, filter.value, props.now))
const counts = computed(() => filterCounts(outcomes.value, filter.value.since, props.now))
const stateChips: Array<{ value: RunFilterState; label: string }> = [
  { value: 'all', label: 'All' },
  { value: 'live', label: 'Live' },
  { value: 'ended', label: 'Ended' },
  { value: 'stopped', label: 'Stopped' },
  { value: 'failed', label: 'Failed' },
]
const sinceItems = [
  { label: 'Today', value: 'today' },
  { label: '7 days', value: '7d' },
  { label: '30 days', value: '30d' },
  { label: 'All time', value: 'all' },
]

const rows = computed(() =>
  (all.value ? matching.value : matching.value.slice(0, SHOWN)).map(({ run: r, outcome }) => {
    const st = runState(r, props.sessions)
    const isLive = runLive(st.state)
    return {
      run: r,
      state: st.state,
      live: isLive,
      badge: outcomeBadge(outcome),
      tiles: memberTiles(r, props.sessions, props.now),
      took: runTook(r, isLive, props.now),
      note: runNote(r, props.sessions),
      resumable: runResumable(r, isLive),
    }
  }),
)

const stopping = ref('')
const resuming = ref('')
async function stop(r: RunInfo) {
  stopping.value = r.id
  try {
    live.applyRun(await api.stopRun(r.id))
    toast.add({ title: 'Run stopped', description: 'Every member was stopped; the worktrees and branches stay.', icon: 'i-lucide-square', color: 'neutral' })
  } catch (e) {
    toast.add({ title: 'Stop failed', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    stopping.value = ''
  }
}
async function resume(r: RunInfo) {
  resuming.value = r.id
  try {
    const next = await api.resumeRun(r.id)
    live.applyRun(next)
    await navigateTo(`/runs/${encodeURIComponent(next.id)}`)
  } catch (e) {
    // Resumed already (#126): the continuation the server names is the run to open.
    if (e instanceof ApiError && e.code === 'already_resumed' && typeof e.details.run === 'string') {
      await navigateTo(`/runs/${encodeURIComponent(e.details.run)}`)
      return
    }
    toast.add({ title: 'Resume failed', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    resuming.value = ''
  }
}

const cols = 'grid-cols-[minmax(0,1fr)_auto] @min-[64rem]:grid-cols-[10rem_7rem_minmax(0,1fr)_5rem_minmax(8rem,1fr)_12rem]'
</script>

<template>
  <div class="flex flex-col gap-4" data-crew-runs-panel>
    <section class="flex flex-col gap-2.5 rounded-lg px-4 py-3.5 ring ring-default" data-runs-chart :data-runs="bars.length">
      <div class="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <span class="text-[13px] font-semibold text-highlighted">Last {{ bars.length }} {{ bars.length === 1 ? 'run' : 'runs' }}</span>
        <span v-if="enough(bars.length)" class="text-xs text-muted">{{ barsSummary(bars) }}</span>
        <span v-if="error" class="text-xs text-warning" :title="error">records unavailable</span>
      </div>
      <p v-if="!enough(bars.length)" class="text-xs text-muted" data-runs-chart-empty>{{ bars.length ? 'A chart of the last runs appears after two.' : 'No runs yet.' }}</p>
      <template v-else>
        <div class="flex h-20 items-end gap-2 border-b border-default px-1">
          <div v-for="b in bars" :key="b.id" class="flex h-full flex-1 flex-col justify-end">
            <div
              class="rounded-t-[3px]"
              :class="[barClass(b.outcome), b.asked && 'shadow-[inset_0_3px_0_var(--ui-warning)]']"
              :style="{ height: `${Math.max(4, Math.round((b.minutes / max) * 100))}%` }"
              :title="`${b.id} · ${minutesLabel(b.minutes)} · ${b.outcome}${b.asked ? ` · needed an answer ×${b.asked}` : ''}`"
              data-run-bar
              :data-outcome="b.outcome"
            />
          </div>
        </div>
        <div class="flex justify-between font-mono text-[10.5px] text-muted">
          <span v-for="(a, i) in axis" :key="i">{{ a }}</span>
        </div>
      </template>
    </section>

    <div v-if="runs.length" class="flex flex-wrap items-center gap-1.5" data-runs-filter>
      <UButton
        v-for="c in stateChips"
        :key="c.value"
        :label="`${c.label} ${counts[c.value]}`"
        size="xs"
        color="neutral"
        :variant="filter.state === c.value ? 'solid' : 'outline'"
        :data-runs-state="c.value"
        :data-active="filter.state === c.value"
        :data-count="counts[c.value]"
        @click="filter = { ...filter, state: c.value }"
      />
      <USelect :model-value="filter.since" :items="sinceItems" size="xs" class="ml-auto w-28" aria-label="When they started" data-runs-since @update:model-value="filter = { ...filter, since: $event as RunFilterSince }" />
    </div>

    <div class="@container overflow-hidden rounded-lg ring ring-default" data-crew-runs :data-runs-matching="matching.length" :data-runs-shown="rows.length">
      <div class="hidden gap-3 border-b border-default bg-muted px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-muted @min-[64rem]:grid" :class="cols" aria-hidden="true">
        <span>Run</span>
        <span>State</span>
        <span>Members</span>
        <span>Took</span>
        <span>Note</span>
        <span />
      </div>
      <p v-if="!runs.length" class="px-4 py-4 text-sm text-muted">No runs yet. Launch run starts one.</p>
      <p v-else-if="!matching.length" class="flex items-center gap-2 px-4 py-4 text-sm text-muted">
        No runs match this filter.
        <UButton label="Show all" size="xs" color="neutral" variant="link" class="px-0" data-runs-filter-reset @click="filter = { ...DEFAULT_RUN_FILTER }" />
      </p>
      <div v-for="row in rows" :key="row.run.id" class="grid items-center gap-x-3 gap-y-2 border-b border-default px-4 py-2.5 text-[13px] last:border-b-0" :class="cols" :data-run="row.run.id" :data-state="row.state">
        <div class="col-start-1 row-start-1 flex min-w-0 flex-wrap items-center gap-x-2 @min-[64rem]:flex-col @min-[64rem]:items-start @min-[64rem]:gap-0">
          <span v-if="row.run.label" class="w-full truncate font-medium text-highlighted" :title="row.run.label" data-run-label>{{ row.run.label }}</span>
          <span :class="row.run.label ? 'text-muted' : 'font-medium text-highlighted'" :title="row.run.id">{{ runWhen(row.run.startedAt, now) }}</span>
          <span class="font-mono text-[11px] text-dimmed">{{ shortRunId(row.run.id) }}</span>
          <YoloBadge v-if="row.run.yolo" icon />
        </div>
        <span class="col-start-2 row-start-1 justify-self-end @min-[64rem]:justify-self-start"><UBadge :label="row.badge.label" :color="row.badge.color" :variant="row.badge.variant" size="sm" data-run-state /></span>
        <div class="col-span-2 row-start-2 flex flex-wrap items-center gap-x-4 gap-y-2 @min-[64rem]:contents">
          <div class="flex flex-wrap gap-1 @min-[64rem]:col-start-3 @min-[64rem]:row-start-1" data-run-members>
            <span
              v-for="t in row.tiles"
              :key="t.m.name"
              class="relative grid h-5 w-[22px] place-items-center rounded bg-elevated font-mono text-[8.5px] font-medium"
              :class="t.status === 'pending' ? 'text-muted' : 'text-default'"
              :title="`${t.m.name} · ${t.words.text}`"
              :data-run-member="t.m.name"
              :data-status="t.status"
            >
              {{ agentInitials(t.m.agentId) }}
              <StatusMark :status="t.mark" badge class="absolute -right-1 -top-1" />
            </span>
          </div>
          <span class="font-mono text-xs @min-[64rem]:col-start-4 @min-[64rem]:row-start-1" data-run-took>{{ row.took }}</span>
          <span
            class="min-w-0 truncate text-xs @min-[64rem]:col-start-5 @min-[64rem]:row-start-1"
            :class="row.note.tone === 'warning' ? 'text-warning' : row.note.tone === 'error' ? 'text-error' : 'text-muted'"
            :title="row.note.text"
            data-run-note
            >{{ row.note.text }}</span
          >
          <div class="ml-auto flex items-center justify-end gap-1.5 @min-[64rem]:col-start-6 @min-[64rem]:row-start-1 @min-[64rem]:ml-0">
            <UButton v-if="row.live" label="Stop" icon="i-lucide-square" size="xs" color="error" variant="soft" :loading="stopping === row.run.id" :aria-label="`Stop the run ${row.run.id}`" data-run-stop @click="stop(row.run)" />
            <UButton
              v-else-if="row.resumable"
              label="Resume as new run"
              icon="i-lucide-play"
              size="xs"
              variant="outline"
              :loading="resuming === row.run.id"
              :title="`A new run of the crew: every member with a conversation continues it in its worktree`"
              data-run-resume
              @click="resume(row.run)"
            />
            <UButton :to="`/runs/${encodeURIComponent(row.run.id)}`" label="Open" trailing-icon="i-lucide-arrow-right" size="xs" color="neutral" variant="outline" data-run-open />
          </div>
        </div>
      </div>
      <div v-if="matching.length > SHOWN" class="px-4 py-2">
        <UButton :label="all ? 'Show fewer' : `Show ${matching.length - SHOWN} more`" size="xs" color="neutral" variant="link" class="px-0" @click="all = !all" />
      </div>
    </div>
  </div>
</template>
