<script setup lang="ts">
import type { TicketLink } from '~/composables/useDispatch'
import type { RunInfo } from '~/composables/useSessions'
import { runTitle, shortRunId } from '~/utils/crewWords'
import { linkedRunRecords } from '~/utils/linkedRuns'
import { runBadge, runState } from '~/utils/runs'
import { taskBadges } from '~/utils/tasks'

/**
 * The run Tasks view inside a ticket (docs/plans/dispatch.md, PR-11): the managed runs linked to the ticket, each with the run page's own
 * task rows (RunTasks.vue, embedded, not a copy) for the run the live store holds or, for one the server no longer holds, its record,
 * read once, read-only. Nothing is launched from a ticket (DS2's): a run is linked by hand, picked from the runs the server holds or
 * named by its id, one the engine holds or has a record of; a run another live ticket holds is refused (409 run_linked) with that ticket
 * named, since a run belongs to one ticket. A link removed leaves the run as it is. The runs follow the live store's run events.
 */
const props = defineProps<{
  links: readonly TicketLink[]
  locked: boolean
  now: number
  /** A run linked by its id: '' once the store has it, else why not, in words. */
  add: (run: string) => Promise<string>
  remove: (link: TicketLink) => Promise<boolean>
}>()

const live = useAttention()
const router = useRouter()

const runLinks = computed(() => props.links.filter((l) => l.kind === 'run' && !l.deletedAt).sort((a, b) => a.createdAt.localeCompare(b.createdAt)))

/** The records of linked runs the live store does not hold, read again when it stops holding one (linkedRunRecords). */
const { runOf } = linkedRunRecords(() => runLinks.value.map((l) => l.ref), live)

function badge(r: RunInfo) {
  const s = runState(r, live.sessions.value)
  return runBadge(s.state, s.needs)
}

/** The task selected in each run's rows, its result and attempts opened under them. */
const selected = reactive(new Map<string, string>())

// Linking a run: one the server holds, picked by its name, or any id typed.
const adding = ref(false)
const pick = ref<string | undefined>(undefined)
const error = ref('')
const sending = ref(false)
const runItems = computed(() => {
  const linked = new Set(runLinks.value.map((l) => l.ref))
  return live.runs.value
    .filter((r) => !linked.has(r.id))
    .sort((a, b) => Number(!!b.managed) - Number(!!a.managed) || b.startedAt.localeCompare(a.startedAt))
    .map((r) => ({ label: `${runTitle(r)} · ${shortRunId(r.id)}`, value: r.id, description: r.managed ? `${taskBadges(r)[0]?.label ?? ''}` : 'not a managed run: no tasks' }))
})
function openForm() {
  pick.value = undefined
  error.value = ''
  adding.value = true
}
async function link(id: unknown) {
  const run = typeof id === 'string' ? id.trim() : ''
  if (!run || sending.value) return
  pick.value = run
  sending.value = true
  error.value = ''
  try {
    const words = await props.add(run)
    if (words) error.value = words
    else adding.value = false
  } finally {
    sending.value = false
  }
}

const removing = ref('')
async function unlink(l: TicketLink) {
  removing.value = l.id
  try {
    await props.remove(l)
  } finally {
    if (removing.value === l.id) removing.value = ''
  }
}
</script>

<template>
  <section class="flex flex-col gap-2" data-ticket-runs :data-count="runLinks.length">
    <div class="flex items-center gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Runs</h3>
      <div class="flex-1" />
      <UButton v-if="!locked && !adding" label="Link a run" icon="i-lucide-workflow" size="xs" color="neutral" variant="ghost" data-run-link-open @click="openForm" />
    </div>
    <p v-if="!runLinks.length && !adding" class="text-sm text-dimmed">No run is linked to this ticket. A managed run that works on it can be linked here; its tasks show here then.</p>

    <form v-if="adding && !locked" class="flex flex-col gap-2 rounded-md p-3 ring ring-default" data-run-link-form @submit.prevent="link(pick)">
      <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-run-link-error />
      <USelectMenu
        :model-value="pick"
        :items="runItems"
        value-key="value"
        create-item="always"
        placeholder="A run the server holds, or its id…"
        :search-input="{ placeholder: 'A run, or its id…' }"
        size="sm"
        class="w-full"
        data-run-link-pick
        @update:model-value="link"
        @create="link"
      >
        <template #create-item-label="{ item }">Link the run {{ item }}</template>
      </USelectMenu>
      <div class="flex justify-end">
        <UButton label="Cancel" color="neutral" variant="ghost" size="sm" :disabled="sending" @click="adding = false" />
      </div>
    </form>

    <ul v-if="runLinks.length" class="flex flex-col gap-3">
      <li v-for="l in runLinks" :key="l.id" class="flex flex-col gap-2 rounded-lg p-3 ring ring-default" :data-ticket-run="l.ref">
        <div class="flex min-w-0 flex-wrap items-center gap-2">
          <UIcon name="i-lucide-workflow" class="size-4 flex-none text-muted" />
          <template v-if="runOf(l.ref)">
            <NuxtLink :to="`/runs/${encodeURIComponent(l.ref)}?view=tasks`" class="min-w-0 truncate text-sm font-medium text-highlighted hover:underline" data-ticket-run-open>{{ runTitle(runOf(l.ref)!) }}</NuxtLink>
            <span class="flex-none font-mono text-[11px] text-dimmed" :title="l.ref">{{ shortRunId(l.ref) }}</span>
            <UBadge v-if="runOf(l.ref)!.record" label="record" color="neutral" variant="subtle" size="sm" class="flex-none" title="A run the server no longer holds, as it was when it ended: read-only" data-ticket-run-record />
            <UBadge v-else :label="badge(runOf(l.ref)!).label" :color="badge(runOf(l.ref)!).color" variant="subtle" size="sm" class="flex-none" />
            <UBadge v-for="b in runOf(l.ref)!.managed ? taskBadges(runOf(l.ref)!) : []" :key="b.label" :label="b.label" :color="b.color" variant="subtle" size="sm" class="flex-none" data-ticket-run-badge />
          </template>
          <span v-else-if="runOf(l.ref) === null" class="min-w-0 truncate text-sm text-muted" data-ticket-run-missing>
            <span class="font-mono">{{ l.ref }}</span>: the server holds no run and no record of it.
          </span>
          <span v-else class="min-w-0 truncate font-mono text-sm text-muted">Reading {{ l.ref }}…</span>
          <div class="flex-1" />
          <UButton v-if="!locked" icon="i-lucide-unlink" size="xs" color="neutral" variant="ghost" :aria-label="`Unlink the run ${l.ref}`" :loading="removing === l.id" data-ticket-run-remove @click="unlink(l)" />
        </div>
        <RunTasks v-if="runOf(l.ref)" :run="runOf(l.ref)!" :selected="selected.get(l.ref) ?? ''" :now="now" @select="(id: string) => selected.set(l.ref, id)" @open="(id: string) => router.push(`/sessions/${id}`)" />
      </li>
    </ul>
  </section>
</template>
