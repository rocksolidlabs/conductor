<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import type { AgentInfo, RunInfo } from '~/composables/useSessions'
import { draftMember, memberNameError, memberNameFrom, type DraftMember } from '~/utils/crews'
import { runResumable, runSubtitle, shortRunId } from '~/utils/crewWords'
import { relativeTime } from '~/utils/sessions'
import { commandBody, sendCommand, taskBadges } from '~/utils/tasks'
import type { RelatedTicket } from '~/composables/useDispatch'
import { ticketPath } from '~/utils/dispatch'

/**
 * The run page's header: the run named by its crew and its start, how many
 * members need input and run (a managed run: its tasks done, failed, running
 * and checking, and whether its dispatch is paused), how long it has been
 * up, and its actions: add an agent mid-run, share the run (a run link),
 * pause or resume a managed run's dispatch (a command with a receipt), stop
 * every member, and once stopped resume it as a new run. Each action calls
 * the server and hands the run as it is after it to the page (`changed`).
 * Beside its name, the Dispatch ticket the run is linked to, as a chip that
 * opens the ticket's page, "(deleted)" on it when the ticket is a tombstone.
 */
const props = withDefaults(
  defineProps<{
    run: RunInfo | null
    runId: string
    counts: { needs: number; running: number }
    now: number
    chatUnread?: number
    chatOpen?: boolean
    chatOffline?: boolean
    /** A record of a run the server no longer holds (#127): read-only, so no agent added, nothing stopped, no resume until a run can be resumed from its record. */
    record?: boolean
    /** The Dispatch ticket the run is linked to (GET /api/dispatch/links?run=), null when none. */
    ticket?: RelatedTicket | null
  }>(),
  { chatUnread: 0, chatOpen: false, chatOffline: false, record: false, ticket: null },
)
const emit = defineEmits<{ changed: [run: RunInfo]; chat: [] }>()

const api = useSessions()
const toast = useToast()
const live = useAttention()
const router = useRouter()

const stopped = computed(() => !!props.run?.stoppedAt)
/** Resume as new run: a stopped run the server holds, or a record (#153) not resumed yet. */
const resumable = computed(() => !!props.run && runResumable(props.run, !stopped.value && !props.record))
const uptime = computed(() => {
  const r = props.run
  if (!r) return ''
  const where = `server · ${r.isolation === 'worktree' ? 'worktrees' : 'shared'}`
  if (r.stoppedAt) return `stopped · ran ${relativeTime(r.startedAt, Date.parse(r.stoppedAt))} · ${where}`
  return `up ${relativeTime(r.startedAt, props.now)} · ${where}`
})

// Add agent: one row of the crew editor's members table.
const addOpen = ref(false)
const agents = ref<AgentInfo[]>([])
const member = ref<DraftMember[]>([])
const adding = ref(false)
const addError = ref('')
const runNames = computed(() => props.run?.members.map((m) => m.name) ?? [])
const addInvalid = computed(() => {
  const m = member.value[0]
  return !m || !m.agentId || !!memberNameError(m.name, runNames.value)
})

async function openAdd() {
  addError.value = ''
  addOpen.value = true
  try {
    if (!agents.value.length) agents.value = await api.catalog()
  } catch (e) {
    addError.value = (e as Error).message
  }
  const first = agents.value[0]
  member.value = first ? [draftMember({ name: memberNameFrom(first.id, runNames.value), agentId: first.id, prompt: '', start: { when: 'immediately' } })] : []
}

async function add() {
  const m = member.value[0]
  if (!m || addInvalid.value) return
  adding.value = true
  addError.value = ''
  try {
    const run = await api.addRunMember(props.runId, m)
    emit('changed', run)
    addOpen.value = false
    toast.add({ title: `${m.name} joined the run`, icon: 'i-lucide-user-plus', color: 'success' })
  } catch (e) {
    addError.value = (e as Error).message
  } finally {
    adding.value = false
  }
}

const shareOpen = ref(false)

// Resume as new run: a new run of the crew in which every member with a conversation continues it in its kept worktree.
const resuming = ref(false)
async function resumeRun() {
  resuming.value = true
  try {
    const next = await api.resumeRun(props.runId)
    live.applyRun(next)
    toast.add({ title: next.managed ? 'Run continued' : 'Run resumed', description: next.managed ? `${next.id}: the results kept, the rest taken up.` : `${next.id}: the conversations continue in their worktrees.`, icon: 'i-lucide-play', color: 'success' })
    await router.push(`/runs/${encodeURIComponent(next.id)}`)
  } catch (e) {
    // Resumed already (#126): the continuation the server names is the run to open.
    if (e instanceof ApiError && e.code === 'already_resumed' && typeof e.details.run === 'string') {
      await router.push(`/runs/${encodeURIComponent(e.details.run)}`)
      return
    }
    toast.add({ title: 'Resume failed', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    resuming.value = false
  }
}

// A managed run: its task badges, and Pause / Resume of its dispatch.
const badges = computed(() => (props.run?.managed ? taskBadges(props.run) : null))
const pausing = ref(false)
async function togglePause() {
  const r = props.run
  if (!r?.managed) return
  const paused = !r.paused
  pausing.value = true
  try {
    await sendCommand((body) => api.pauseRun(props.runId, paused, body), commandBody())
    toast.add({ title: paused ? 'Scheduling paused' : 'Scheduling resumed', description: paused ? 'Attempts in flight go on; no task starts until it is resumed.' : undefined, icon: paused ? 'i-lucide-pause' : 'i-lucide-play', color: 'neutral' })
  } catch (e) {
    toast.add({ title: paused ? 'Not paused' : 'Not resumed', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    pausing.value = false
  }
}

// Stop asks first.
const stopOpen = ref(false)
const stopping = ref(false)

async function stopAll() {
  stopping.value = true
  try {
    const run = await api.stopRun(props.runId)
    emit('changed', run)
    stopOpen.value = false
    toast.add({ title: 'Run stopped', description: 'Every member was stopped; the worktrees and branches stay.', icon: 'i-lucide-square', color: 'neutral' })
  } catch (e) {
    toast.add({ title: 'Stop failed', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    stopping.value = false
  }
}

// The run page's command palette (#302) runs the header's buttons: Add agent, Share, Stop (asked in the palette's row), Resume.
defineExpose({ openAdd, share: () => (shareOpen.value = true), stopNow: stopAll, resume: resumeRun, togglePause })
</script>

<template>
  <UDashboardNavbar :toggle="false" :ui="{ root: 'h-14', left: 'min-w-0 flex-1', right: 'flex-none' }" data-crew-run-header>
    <template #leading>
      <UButton icon="i-lucide-arrow-left" color="neutral" variant="ghost" aria-label="Back to the list" class="lg:hidden" to="/sessions" data-back-to-list />
    </template>
    <template #title>
      <span class="flex min-w-0 items-baseline gap-2">
        <!-- The name gives way last: the subtitle shrinks first (it may be cut), the name keeps a readable minimum. -->
        <NuxtLink v-if="run" :to="`/crews/${encodeURIComponent(run.crewId)}`" class="min-w-[12ch] shrink truncate hover:underline" :title="`The saved crew ${run.name}`" data-run-name>{{ run.name }}</NuxtLink>
        <span v-else class="truncate">Run</span>
        <span v-if="run" class="min-w-0 shrink-[4] truncate text-[13px] font-normal text-muted" :class="!run.label && 'hidden sm:inline'" :title="runSubtitle(run)" data-run-subtitle>{{ runSubtitle(run) }}</span>
        <span class="hidden flex-none font-mono text-[11px] font-normal text-dimmed md:inline" :title="runId">{{ shortRunId(runId) }}</span>
        <UButton
          v-if="ticket"
          :to="ticketPath(ticket.key)"
          :label="ticket.deletedAt ? `${ticket.key} (deleted)` : ticket.key"
          icon="i-lucide-ticket"
          size="xs"
          color="neutral"
          :variant="ticket.deletedAt ? 'ghost' : 'soft'"
          class="flex-none self-center font-mono"
          :class="ticket.deletedAt && 'text-muted'"
          :title="`${ticket.key} ${ticket.title}${ticket.deletedAt ? ' (deleted)' : ''}: the ticket this run is linked to`"
          :aria-label="`The ticket ${ticket.key}${ticket.deletedAt ? ', deleted' : ''}: ${ticket.title}`"
          data-run-ticket
          :data-run-ticket-key="ticket.key"
          :data-run-ticket-deleted="ticket.deletedAt ? '' : undefined"
        />
      </span>
    </template>
    <template #trailing>
      <div v-if="run" class="ml-2 hidden min-w-0 items-center gap-1.5 sm:flex">
        <UBadge v-if="record" label="record" color="neutral" variant="subtle" size="sm" class="flex-none" title="A run the server no longer holds, as it was when it ended: read-only" data-run-record />
        <template v-if="badges">
          <UBadge v-for="b in badges" :key="b.label" :label="b.label" :color="b.color" variant="subtle" size="sm" class="flex-none" data-run-task-badge />
          <UBadge v-if="counts.needs" :label="`${counts.needs} need input`" color="warning" variant="subtle" size="sm" class="flex-none" />
        </template>
        <template v-else>
          <UBadge :label="`${counts.needs} need input`" :color="counts.needs ? 'warning' : 'neutral'" variant="subtle" size="sm" class="flex-none" />
          <UBadge :label="`${counts.running} running`" :color="counts.running ? 'success' : 'neutral'" variant="subtle" size="sm" class="flex-none" />
        </template>
        <span class="ml-1 truncate font-mono text-xs text-muted">{{ uptime }}</span>
      </div>
    </template>
    <template #right>
      <!-- Icons only on a phone: the labels would push the name off the bar. -->
      <UButton icon="i-lucide-plus" color="neutral" variant="outline" aria-label="Add agent" :disabled="!run || stopped || record" @click="openAdd"><span class="hidden sm:inline">Add agent</span></UButton>
      <UButton icon="i-lucide-share-2" color="neutral" variant="outline" aria-label="Share" :disabled="!run" @click="shareOpen = true"><span class="hidden sm:inline">Share</span></UButton>
      <!-- The run's chat (design 2e): the count while the drawer is closed. -->
      <UButton icon="i-lucide-message-circle" color="neutral" variant="outline" aria-label="Chat" :disabled="!run" :class="chatOpen && 'ring-2 ring-primary/40'" data-run-chat-button :data-run-chat-offline="chatOffline ? '' : undefined" @click="emit('chat')">
        <span class="hidden sm:inline">Chat</span>
        <ChatUnreadPill :count="chatUnread" />
      </UButton>
      <UButton v-if="resumable" icon="i-lucide-play" color="primary" variant="soft" :aria-label="run?.managed ? 'Continue the run' : 'Resume as new run'" :loading="resuming" data-run-resume @click="resumeRun"><span class="hidden sm:inline">{{ run?.managed ? 'Continue' : 'Resume as new run' }}</span></UButton>
      <UButton v-if="run?.managed && !stopped && !record" :icon="run.paused ? 'i-lucide-play' : 'i-lucide-pause'" color="neutral" variant="outline" :aria-label="run.paused ? 'Resume scheduling' : 'Pause scheduling'" :loading="pausing" data-run-pause :data-run-paused="run.paused ? '' : undefined" @click="togglePause"><span class="hidden sm:inline">{{ run.paused ? 'Resume' : 'Pause' }}</span></UButton>
      <UButton v-if="!stopped && !record" icon="i-lucide-square" color="error" variant="soft" aria-label="Stop" :disabled="!run" data-run-stop @click="stopOpen = true"><span class="hidden sm:inline">Stop</span></UButton>
      <FullscreenButton class="hidden sm:inline-flex" />
    </template>
  </UDashboardNavbar>
  <!-- On a phone the counts and the uptime take a line of their own: the bar keeps the name and the buttons. -->
  <div v-if="run" class="flex items-center gap-1.5 border-b border-default px-4 py-1.5 sm:hidden" data-run-header-strip>
    <template v-if="badges">
      <UBadge v-for="b in badges" :key="b.label" :label="b.label" :color="b.color" variant="subtle" size="sm" class="flex-none" data-run-task-badge />
    </template>
    <template v-else>
      <UBadge :label="`${counts.needs} need input`" :color="counts.needs ? 'warning' : 'neutral'" variant="subtle" size="sm" class="flex-none" data-run-header-needs />
      <UBadge :label="`${counts.running} running`" :color="counts.running ? 'success' : 'neutral'" variant="subtle" size="sm" class="flex-none" data-run-header-running />
    </template>
    <span class="min-w-0 truncate font-mono text-[11px] text-muted">{{ uptime }}</span>
  </div>

  <USlideover v-model:open="addOpen" title="Add an agent" description="It joins this run, not the saved crew. One that starts at launch starts now; its prompt is typed once it is ready." :ui="{ content: 'sm:max-w-4xl' }">
    <template #body>
      <div class="flex flex-col gap-4">
        <UAlert v-if="addError" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="addError" />
        <CrewMembersTable v-if="member.length" v-model="member" :agents="agents" :others="runNames" single />
        <p v-else-if="!addError" class="text-sm text-muted">No agents in the catalog.</p>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Cancel" color="neutral" variant="ghost" @click="addOpen = false" />
        <UButton label="Add to the run" icon="i-lucide-user-plus" :loading="adding" :disabled="addInvalid" @click="add" />
      </div>
    </template>
  </USlideover>

  <ShareLinksModal v-model:open="shareOpen" :run-id="runId" :session-name="run?.name" />

  <UModal v-model:open="stopOpen" :title="`Stop this run of ${run?.name || 'the crew'}?`" description="Every member's session is stopped; a member not started yet never starts. The worktrees and their branches stay, and the run can be resumed as a new run.">
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Cancel" color="neutral" variant="ghost" @click="stopOpen = false" />
        <UButton label="Stop the run" icon="i-lucide-square" color="error" :loading="stopping" data-confirm-stop @click="stopAll" />
      </div>
    </template>
  </UModal>
</template>
