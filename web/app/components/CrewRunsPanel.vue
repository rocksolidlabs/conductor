<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import { barClass, barsSummary, CHART_AT, DEFAULT_RUN_FILTER, filterCounts, filterRuns, memberLooks, minutesLabel, readRunFilter, RUN_FILTER_CHIPS, RUN_FILTER_KEY, runBars, runListName, runLook, runNote, runPhase, runPhaseView, runResumable, runsSentence, runWhen, shortRunId, tookWords, type RunFilter, type RunFilterSince } from '~/utils/crewWords'
import { runLive, runState } from '~/utils/runs'
import { runItems } from '~/utils/status'

/**
 * A crew's Runs tab: how its last run went and how long its runs usually take, in a sentence (C13; from five runs, a chart of them,
 * each bar in its state's tone with its words in its title), then every run as a row (C12): its own name, else "Run of today
 * 19:28", its state in the one vocabulary (C1, C2: a run whose task failed reads Failed, and the Failed chip counts it), its members
 * by name with their state's word, how long it took in words, a note (who asks, what failed, what a stop cut off, what changed),
 * Stop and Open while it goes, Resume as new run and Open after, and its short id at the row's end. The runs are the live ones and
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
const chart = computed(() => bars.value.length >= CHART_AT)
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
// Each run's state in the one vocabulary: a live run read with its items of the one list, a record as it ended.
const needsYou = useNeedsYou()
const looks = computed(() =>
  props.runs.map((r) => {
    if (r.record) {
      const phase = runPhase(r, [])
      return { run: r, phase, view: runPhaseView(phase, r.state === 'interrupted') }
    }
    const { phase, view } = runLook(r, props.sessions, runItems(needsYou.items.value, r.id), props.now)
    return { run: r, phase, view }
  }),
)
const matching = computed(() => filterRuns(looks.value, filter.value, props.now))
const counts = computed(() => filterCounts(looks.value, filter.value.since, props.now))
const sinceItems = [
  { label: 'Today', value: 'today' },
  { label: '7 days', value: '7d' },
  { label: '30 days', value: '30d' },
  { label: 'All time', value: 'all' },
]

const rows = computed(() =>
  (all.value ? matching.value : matching.value.slice(0, SHOWN)).map(({ run: r, phase, view }) => {
    const st = runState(r, props.sessions)
    const isLive = runLive(st.state)
    return {
      run: r,
      state: st.state,
      phase,
      view,
      live: isLive,
      members: memberLooks(r, props.sessions, props.now),
      took: tookWords(r, isLive, props.now),
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

const cols = 'grid-cols-[minmax(0,1fr)_auto] @min-[64rem]:grid-cols-[minmax(9rem,12rem)_7.5rem_minmax(0,1.8fr)_7rem_minmax(6rem,1fr)_17rem]'
</script>

<template>
  <div class="flex flex-col gap-4" data-crew-runs-panel>
    <section class="flex flex-col gap-2.5 rounded-lg px-4 py-3.5 ring ring-default" data-runs-chart :data-runs="bars.length">
      <p v-if="!chart" class="text-[13px] text-default" data-runs-chart-empty>
        <span data-runs-sentence>{{ runsSentence(bars) }}</span>
        <span v-if="error" class="ml-1.5 text-xs text-warning" :title="error">Records unavailable.</span>
      </p>
      <template v-else>
        <div class="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
          <span class="text-[13px] font-semibold text-highlighted">Last {{ bars.length }} runs</span>
          <span class="text-xs text-muted">{{ barsSummary(bars) }}</span>
          <span v-if="error" class="text-xs text-warning" :title="error">records unavailable</span>
        </div>
        <p class="text-xs text-muted" data-runs-sentence>{{ runsSentence(bars) }}</p>
        <div class="flex h-20 items-end gap-2 border-b border-default px-1">
          <div v-for="b in bars" :key="b.id" class="flex h-full flex-1 flex-col justify-end">
            <div
              class="rounded-t-[3px]"
              :class="[barClass(b.phase), b.asked && 'shadow-[inset_0_3px_0_var(--ui-warning)]']"
              :style="{ height: `${Math.max(4, Math.round((b.minutes / max) * 100))}%` }"
              :title="`${shortRunId(b.id)} · ${minutesLabel(b.minutes)} · ${runPhaseView(b.phase).word}${b.asked ? ` · needed an answer ×${b.asked}` : ''}`"
              data-run-bar
              :data-outcome="b.phase"
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
        v-for="c in RUN_FILTER_CHIPS"
        :key="c.value"
        :label="`${c.label} ${counts[c.value]}`"
        size="xs"
        color="neutral"
        :variant="filter.state === c.value ? 'solid' : 'outline'"
        class="max-sm:min-h-11"
        :title="c.value === 'live' ? 'Running, and the runs that need you' : undefined"
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
      <div v-for="row in rows" :key="row.run.id" class="grid items-start gap-x-3 gap-y-2 border-b border-default px-4 py-2.5 text-[13px] last:border-b-0" :class="cols" :data-run="row.run.id" :data-state="row.state" :data-phase="row.phase">
        <div class="col-start-1 row-start-1 flex min-w-0 flex-col">
          <span class="truncate font-medium text-highlighted" :title="runListName(row.run, now)" :data-run-label="row.run.label ? '' : undefined" data-run-name>{{ runListName(row.run, now) }}</span>
          <span v-if="row.run.label" class="text-xs text-muted">{{ runWhen(row.run.startedAt, now) }}</span>
          <YoloBadge v-if="row.run.yolo" icon />
        </div>
        <span class="col-start-2 row-start-1 justify-self-end pt-0.5 @min-[64rem]:justify-self-start" data-run-state :data-phase="row.phase"><StatusMark :status="row.view" word /></span>
        <div class="col-span-2 row-start-2 flex flex-wrap items-start gap-x-4 gap-y-2 @min-[64rem]:contents">
          <ul class="flex min-w-0 flex-wrap gap-x-4 gap-y-0.5 text-xs @min-[64rem]:col-start-3 @min-[64rem]:row-start-1 @min-[64rem]:pt-0.5" data-run-members>
            <li v-for="t in row.members" :key="t.m.name" class="flex min-w-0 max-w-full items-center gap-1.5" :title="`${t.m.name} · ${t.view.word}`" :data-run-member="t.m.name" :data-status="t.status" :data-phase="t.phase">
              <span class="truncate text-default">{{ t.m.name }}</span>
              <StatusMark :status="t.view" word />
            </li>
          </ul>
          <span class="text-xs @min-[64rem]:col-start-4 @min-[64rem]:row-start-1 @min-[64rem]:pt-0.5" data-run-took>{{ row.took }}</span>
          <span
            class="min-w-0 truncate text-xs @min-[64rem]:col-start-5 @min-[64rem]:row-start-1 @min-[64rem]:pt-0.5"
            :class="row.note.tone === 'warning' ? 'text-warning' : row.note.tone === 'error' ? 'text-error' : 'text-muted'"
            :title="row.note.text"
            data-run-note
            >{{ row.note.text }}</span
          >
          <div class="ml-auto flex items-center justify-end gap-1.5 @min-[64rem]:col-start-6 @min-[64rem]:row-start-1 @min-[64rem]:ml-0">
            <UButton v-if="row.live" label="Stop" icon="i-lucide-square" size="xs" color="error" variant="soft" class="max-sm:min-h-11" :loading="stopping === row.run.id" :aria-label="`Stop the run ${row.run.id}`" data-run-stop @click="stop(row.run)" />
            <UButton
              v-else-if="row.resumable"
              label="Resume as new run"
              icon="i-lucide-play"
              size="xs"
              variant="outline"
              class="max-sm:min-h-11"
              :loading="resuming === row.run.id"
              :title="`A new run of the crew: every member with a conversation continues it in its worktree`"
              data-run-resume
              @click="resume(row.run)"
            />
            <UButton :to="`/runs/${encodeURIComponent(row.run.id)}`" label="Open" trailing-icon="i-lucide-arrow-right" size="xs" color="neutral" variant="outline" class="max-sm:min-h-11" data-run-open />
            <span class="font-mono text-[10.5px] text-dimmed" :title="row.run.id" data-mono="id" data-run-short-id>{{ shortRunId(row.run.id) }}</span>
          </div>
        </div>
      </div>
      <div v-if="matching.length > SHOWN" class="px-4 py-2">
        <UButton :label="all ? 'Show fewer' : `Show ${matching.length - SHOWN} more`" size="xs" color="neutral" variant="link" class="px-0" @click="all = !all" />
      </div>
    </div>
  </div>
</template>
