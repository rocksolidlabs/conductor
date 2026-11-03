<script setup lang="ts">
import type { DispatchChange, TicketLink, TicketRunEntry } from '~/composables/useDispatch'
import type { TicketRuns } from '~/composables/useTicketRuns'
import { runTitle, shortRunId } from '~/utils/crewWords'
import { historyEntries, ticketPath, type ChangeNames } from '~/utils/dispatch'
import { changesSince, entryKey, entryRunId, launchEntryWords } from '~/utils/dispatchEvidence'
import { launchStateWords, revisionTag } from '~/utils/dispatchPlans'
import { linkedRunRecords } from '~/utils/linkedRuns'
import { relativeTime } from '~/utils/sessions'
import { taskBadges } from '~/utils/tasks'

/**
 * A ticket's Runs (docs/plans/dispatch.md, PR-11; Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-11): every launch the
 * ticket made, every run linked to it by hand, and every other run of a chain the ticket holds (the runs they continue and that continue
 * them), newest first, a page at a time (GET …/{key}/runs; Show older follows the page's `next`, so every launch and continuation is
 * reachable, and a continuation on a later page than its launch names the run it continues). A launch says its state (a failed one why),
 * its plan revision and, when the ticket changed after it, what changed (DispatchChangedSince) with Revise and Launch a follow-up; a run
 * linked by hand can be unlinked, and one whose chain another ticket holds too says so. Each run shows its state, its tasks counted, its
 * blockers and the questions its members wait on, the run page's own task rows (RunTasks.vue, embedded, not a copy) for the run the live
 * store holds or, for one the server no longer holds, its record, read once, and its candidates with their receipts and repairs
 * (DispatchRunCard). A run is linked by hand by picking one the server holds or naming its id; a run another live ticket holds is refused
 * (409 run_linked) with that ticket named, since a run belongs to one ticket, and a launched run needs no link: its launch is the
 * ticket's reference to it. The section is read with the ticket (useTicketRuns) and follows the live store's run events. One column on a
 * phone.
 */
const props = defineProps<{
  links: readonly TicketLink[]
  runs: TicketRuns
  /** The ticket's history read so far, and the names its words take: the history between a launch and now. */
  changes: readonly DispatchChange[]
  names: ChangeNames
  /** Whether the history has later rows not read yet (it is read oldest first): what follows a launch may be among them. */
  historyMore: boolean
  locked: boolean
  now: number
  /** A run linked by its id: '' once the store has it, else why not, in words. */
  add: (run: string) => Promise<string>
  remove: (link: TicketLink) => Promise<boolean>
}>()
const emit = defineEmits<{ revise: [planId: string]; followUp: [] }>()

const live = useAttention()

const runLinks = computed(() => props.links.filter((l) => l.kind === 'run' && !l.deletedAt))

/**
 * The entries shown: the runs read's; while it has not answered, or when it failed, the live hand links it does not have yet, so a run
 * linked by hand shows at once and the section never empties for a read that failed.
 */
const entries = computed<TicketRunEntry[]>(() => {
  const read = props.runs.entries.value
  if (props.runs.pages.value && !props.runs.error.value) return read
  const have = new Set(read.map((e) => e.link?.id).filter(Boolean))
  return [...read, ...runLinks.value.filter((l) => !have.has(l.id)).map((l): TicketRunEntry => ({ kind: 'link', link: l }))]
})

/** The runs shown as the live store holds them, else their records (linkedRunRecords). */
const { runOf } = linkedRunRecords(() => entries.value.map(entryRunId).filter(Boolean), live)

/** The history between a launch and now, in the history's words; null when the history read so far does not reach the launch. */
function since(launchId: string) {
  const rows = changesSince(props.changes, launchId)
  return rows ? historyEntries(rows, props.names) : null
}

// Linking a run: one the server holds, picked by its name, or any id typed.
const adding = ref(false)
const pick = ref<string | undefined>(undefined)
const error = ref('')
const sending = ref(false)
const runItems = computed(() => {
  const shown = new Set(entries.value.map(entryRunId))
  return live.runs.value
    .filter((r) => !shown.has(r.id) && !r.ticket)
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

const older = ref(false)
async function showOlder() {
  older.value = true
  try {
    await props.runs.older()
  } finally {
    older.value = false
  }
}
</script>

<template>
  <section class="flex flex-col gap-2" data-ticket-runs :data-count="entries.length">
    <div class="flex items-center gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Runs</h3>
      <UIcon v-if="runs.loading.value" name="i-lucide-loader" class="size-3 animate-spin text-dimmed" />
      <div class="flex-1" />
      <UButton v-if="!locked && !adding" label="Link a run" icon="i-lucide-workflow" size="xs" color="neutral" variant="ghost" data-run-link-open @click="openForm" />
    </div>
    <p v-if="runs.error.value" class="text-xs text-warning" data-ticket-runs-error>The ticket's runs could not be read: {{ runs.error.value }}</p>
    <p v-if="!entries.length && !adding && runs.pages.value" class="text-sm text-dimmed">
      No run yet. A launch from an approved plan revision lists its run here; any other managed run that works on the ticket can be linked
      by hand. Their tasks, candidates and checks show here then.
    </p>
    <p v-else-if="!entries.length && !adding && !runs.pages.value && !runs.error.value" class="text-sm text-dimmed">Reading the ticket's runs…</p>

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

    <ul v-if="entries.length" class="flex flex-col gap-3">
      <li
        v-for="e in entries"
        :key="entryKey(e)"
        class="flex min-w-0 flex-col gap-2 rounded-lg p-3 ring ring-default"
        data-run-entry
        :data-run-entry-kind="e.kind"
        :data-ticket-run="entryRunId(e) || undefined"
        :data-launch-entry="e.launch && e.kind === 'launch' ? e.launch.id : undefined"
        :data-launch-state="e.launch && e.kind === 'launch' ? e.launch.state : undefined"
      >
        <!-- A launch: its label, the plan revision it ran, its state, who launched it and when. -->
        <div v-if="e.kind === 'launch' && e.launch" class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-sm">
          <UIcon name="i-lucide-play" class="size-3.5 flex-none text-muted" />
          <span class="min-w-0 truncate font-medium">{{ e.launch.label || e.launch.runId }}</span>
          <UBadge :label="revisionTag(e.launch.planN)" color="neutral" variant="outline" size="sm" class="font-mono" :title="`Launched from plan revision ${e.launch.planN}, at the ticket's revision ${e.launch.ticketRev}`" data-launch-entry-plan />
          <UBadge :label="launchStateWords(e.launch).label" :color="launchStateWords(e.launch).color" variant="subtle" size="sm" data-launch-entry-state />
          <span class="text-xs text-dimmed">launched by {{ e.launch.createdBy || 'admin' }} {{ relativeTime(e.launch.createdAt, now, { suffix: true }) }}</span>
          <p v-if="launchEntryWords(e)" class="w-full text-xs" :class="e.launch.state === 'failed' ? 'text-error' : 'text-muted'" data-launch-entry-why>{{ launchEntryWords(e) }}</p>
        </div>
        <!-- A hand link: who linked it, and Unlink. -->
        <div v-else-if="e.kind === 'link' && e.link" class="flex min-w-0 flex-wrap items-center gap-x-2 text-xs text-dimmed">
          <UIcon name="i-lucide-link" class="size-3.5 flex-none" />
          <span>Linked by hand by {{ e.link.createdBy || 'admin' }} {{ relativeTime(e.link.createdAt, now, { suffix: true }) }}</span>
        </div>
        <!-- Another run of a chain the ticket holds: what holds it, and the run it continues. -->
        <div v-else-if="e.kind === 'run'" class="flex min-w-0 flex-wrap items-center gap-x-2 text-xs text-dimmed" data-run-entry-chain>
          <UIcon name="i-lucide-git-branch-plus" class="size-3.5 flex-none" />
          <span v-if="e.continues">Continues <span class="font-mono" :data-run-entry-continues="e.continues">{{ shortRunId(e.continues) }}</span></span>
          <span v-else>Of the chain</span>
          <span v-if="e.launch">of the launch {{ e.launch.label || e.launch.runId }} ({{ revisionTag(e.launch.planN) }})</span>
          <span v-else-if="e.link">of the run linked by hand, {{ shortRunId(e.link.ref) }}</span>
        </div>

        <UAlert v-if="e.contested" color="warning" variant="subtle" icon="i-lucide-split" :title="`Also held by ${e.contested.keys.join(', ')}`" data-run-entry-contested>
          <template #description>
            <p>{{ e.contested.words }}</p>
            <p class="mt-1 flex flex-wrap gap-2">
              <NuxtLink v-for="k in e.contested.keys" :key="k" :to="ticketPath(k)" class="font-mono hover:underline">{{ k }}</NuxtLink>
            </p>
          </template>
        </UAlert>

        <DispatchChangedSince v-if="e.kind === 'launch' && e.launch && e.changed" :launch="e.launch" :changed="e.changed" :rows="since(e.launch.id)" :more="historyMore" :locked="locked" :now="now" @revise="emit('revise', e.launch!.planId)" @follow-up="emit('followUp')" />

        <DispatchRunCard v-if="entryRunId(e)" :run-id="entryRunId(e)" :summary="e.run" :run="runOf(entryRunId(e))" :now="now">
          <template #actions>
            <UButton v-if="e.kind === 'link' && e.link && !locked" icon="i-lucide-unlink" size="xs" color="neutral" variant="ghost" :aria-label="`Unlink the run ${e.link.ref}`" :loading="removing === e.link.id" data-ticket-run-remove @click="unlink(e.link)" />
          </template>
        </DispatchRunCard>
      </li>
    </ul>
    <UButton v-if="runs.next.value" label="Show older" icon="i-lucide-chevrons-down" size="xs" color="neutral" variant="ghost" class="self-start" :loading="older" data-runs-older @click="showOlder" />
  </section>
</template>
