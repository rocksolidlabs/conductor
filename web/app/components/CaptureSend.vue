<script setup lang="ts">
import type { CaptureDocumentView, CaptureView } from '~/composables/useCaptureSends'
import type { ProjectSummary, TicketSummary } from '~/composables/useDispatch'
import { ApiError } from '~/composables/useApi'
import { diffSinceSent } from '~/utils/capture'
import { reviewOffered } from '~/utils/captureReview'
import { SEND_KINDS, type CaptureSendView, type SendForm, type SendKind, cancellable, cancelWords, sendBody, sendState, titleFrom, MAX_SEND_QUESTION, MAX_TICKET_TITLE } from '~/utils/captureSend'
import { decisionsLive } from '~/composables/useDecisions'

/**
 * A saved capture's Send (Program item PV1, docs/plans/browser-evidence.md, "Sending"), in the capture editor's Send slot: Create a
 * ticket (a project, the title from the first note), Attach to a ticket (the run's own ticket first, else one picked), Ask Brakeman (a
 * question), Attach to a decision (a pending decision of the capture's run, offered when it has one: a comment naming the capture). Each send freezes the capture at the revision shown, and the sends are listed below with their state ("sent as r2 to
 * CND-14", "waiting for Brakeman: …") and what changed since each ("edited since: annotation 3's note"); a send to Brakeman that may
 * still run can be cancelled. The sends are read again when Dispatch says a capture or a send changed.
 */
const props = defineProps<{
  capture: CaptureView
  /** The key of the ticket the capture's run was launched from, when it was: offered first by Attach to a ticket. */
  runTicket?: string
  /**
   * Whether the editor holds changes not saved yet: a send freezes the saved revision, so Send waits until they are saved (the editor
   * keys this panel by the capture, so its state is that capture's alone).
   */
  dirty?: boolean
}>()
const emit = defineEmits<{ sent: [send: CaptureSendView] }>()

const api = useCaptureSends()
const dispatch = useDispatchApi()
const identity = useIdentity()

const kind = ref<SendKind>('ticket-new')
const form = reactive<SendForm>({ project: '', title: '', key: '', question: '', decision: '' })
const { request } = useApi()
/** The capture's run's decisions still open: what Attach to a decision offers. */
const decisions = ref<Array<{ id: string; run: string; request: { question: string } }>>([])
const sending = ref(false)
const error = ref('')
const projects = ref<ProjectSummary[]>([])
const pickerProject = ref('')
/** A ticket picked in Attach to a ticket, beside the run's own. */
const picked = ref<TicketSummary | null>(null)

const sends = ref<CaptureSendView[]>([...(props.capture.sends ?? [])])
/** Each frozen document read, by its send's id: what "edited since" compares the capture with. */
const frozen = reactive(new Map<string, CaptureDocumentView>())
/** The desk's words each pending send waits on, from its own read. */
const waiting = reactive(new Map<string, string>())

const deleted = computed(() => !!props.capture.deletedAt)
const firstNote = computed(() => props.capture.annotations?.find((a) => a.note.trim())?.note ?? '')

function projectOfKey(key: string | undefined): string {
  return key?.slice(0, key.lastIndexOf('-')) ?? ''
}

/** The project a new ticket and the picker start from: the capture's run's, else the run's ticket's, else the first. */
function startProject(): string {
  return props.capture.scope?.project || projectOfKey(props.runTicket) || projects.value[0]?.key || ''
}

async function loadProjects() {
  try {
    const page = await dispatch.projects()
    projects.value = (page.projects ?? []).filter((p) => !p.archivedAt)
  } catch {
    projects.value = []
  }
  if (!form.project) form.project = startProject()
  if (!pickerProject.value) pickerProject.value = startProject()
}

/**
 * The capture's run's open decisions, asked of the server by the run (its newest page of them), and read again whenever a decision of
 * that run changes or the events stream comes back (decisionsLive), so one asked while the editor is open is offered.
 */
async function loadDecisions() {
  const run = props.capture.origin?.run
  if (!run) return
  try {
    const r = await request<{ decisions?: Array<{ id: string; run: string; request: { question: string } }> }>('/api/decisions', { query: { state: 'open', run } })
    decisions.value = (r.decisions ?? []).filter((d) => d.run === run)
    if (!decisions.value.some((d) => d.id === form.decision)) form.decision = decisions.value[0]?.id ?? ''
  } catch {
    decisions.value = []
  }
}
let stopDecisions: (() => void) | undefined
onBeforeUnmount(() => stopDecisions?.())

/** The destinations offered: Attach to a decision while the run has an open one. */
/** A review's kinds are offered on a capture of a candidate's preview alone (Decision 9, CaptureSendReview). */
const reviewable = computed(() => reviewOffered(props.capture).offered)
const reviewKind = computed(() => kind.value === 'review' || kind.value === 'review-draft')
const kinds = computed(() => SEND_KINDS.filter((k) => (k.kind !== 'decision' || decisions.value.length) && ((k.kind !== 'review' && k.kind !== 'review-draft') || reviewable.value)))
const decisionItems = computed(() => decisions.value.map((d) => ({ label: `${d.id} · ${d.request.question}`, value: d.id })))

onMounted(() => {
  void loadDecisions()
  stopDecisions = decisionsLive().subscribe((e) => {
    if (!e || e.run === props.capture.origin?.run) void loadDecisions()
  })
  form.title = titleFrom(firstNote.value)
  if (props.runTicket) form.key = props.runTicket
  void loadProjects()
  void readSends()
})

const projectItems = computed(() => projects.value.map((p) => ({ label: `${p.key} · ${p.name}`, value: p.key })))

/** The body the chosen destination would send, or why not yet. */
const body = computed(() => sendBody(kind.value, kind.value === 'ticket' ? { key: picked.value?.key ?? form.key } : form))
const problem = computed(() => {
  if (deleted.value) return 'The capture is deleted'
  if (props.dirty) return `Save your changes first: Send sends the saved r${props.capture.rev}`
  return 'problem' in body.value ? body.value.problem : ''
})
const chosenKey = computed(() => picked.value?.key ?? form.key)

function chooseRunTicket() {
  picked.value = null
  form.key = props.runTicket ?? ''
}

function pick(t: TicketSummary) {
  picked.value = t
  form.key = t.key
}

/** Reads the capture's sends again, and for each one frozen at another revision, or pending, its frozen read. */
async function readSends() {
  try {
    const c = await api.capture(props.capture.id)
    if (c.sends) sends.value = c.sends
  } catch {
    // The sends shown stay.
  }
  await Promise.all(
    sends.value
      .filter((s) => s.state === 'pending' || (s.rev !== props.capture.rev && !frozen.has(s.id)))
      .map(async (s) => {
        try {
          const f = await api.frozen(props.capture.id, s.id)
          frozen.set(s.id, f.document)
          if (f.send.waiting) waiting.set(s.id, f.send.waiting)
          else waiting.delete(s.id)
        } catch {
          // Read again at the next change.
        }
      }),
  )
}

// Dispatch tells a capture's and a send's changes (its outcomes, its cancels, the settlement's): the sends are read again.
useDispatchLive({ later: (e) => e.entity === 'capture_send' || e.entity === 'capture', read: readSends })
watch(
  () => props.capture.rev,
  () => void readSends(),
)

const rows = computed(() =>
  [...sends.value]
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id))
    .map((s) => {
      const doc = frozen.get(s.id)
      const edits = doc && !deleted.value ? diffSinceSent({ rev: props.capture.rev, annotations: props.capture.annotations ?? [] }, doc) : []
      return { send: s, view: sendState({ ...s, waiting: waiting.get(s.id) ?? s.waiting }), edits }
    }),
)

async function send() {
  if (problem.value || sending.value) return
  sending.value = true
  error.value = ''
  try {
    const reply = await api.send(props.capture.id, props.capture.rev, kind.value, kind.value === 'ticket' ? { key: chosenKey.value } : form, identity.name.value.trim() || undefined)
    if (reply.send.waiting) waiting.set(reply.send.id, reply.send.waiting)
    emit('sent', reply.send)
    if (kind.value === 'brakeman') form.question = ''
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : e instanceof Error ? e.message : 'The send failed'
  } finally {
    sending.value = false
    await readSends()
  }
}

const cancelling = ref('')
async function cancel(s: CaptureSendView) {
  cancelling.value = s.id
  error.value = ''
  try {
    const reply = await api.cancel(props.capture.id, s.id)
    error.value = cancelWords(reply.cancel)
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'The cancel failed'
  } finally {
    cancelling.value = ''
    await readSends()
  }
}
</script>

<template>
  <section class="flex flex-col gap-3" data-capture-send :data-capture-send-rev="capture.rev">
    <h3 class="text-sm font-semibold">Send r{{ capture.rev }}</h3>
    <div class="flex flex-wrap gap-1" role="radiogroup" aria-label="Where to send it">
      <UButton
        v-for="k in kinds"
        :key="k.kind"
        :label="k.label"
        :icon="k.icon"
        size="sm"
        color="neutral"
        :variant="kind === k.kind ? 'soft' : 'ghost'"
        role="radio"
        :aria-checked="kind === k.kind"
        :data-send-kind="k.kind"
        :data-send-chosen="kind === k.kind ? '' : undefined"
        @click="kind = k.kind"
      />
    </div>

    <div v-if="kind === 'ticket-new'" class="flex flex-col gap-2">
      <UFormField label="Project">
        <USelect v-model="form.project" :items="projectItems" class="w-full" data-send-project />
      </UFormField>
      <UFormField label="Title" :hint="`At most ${MAX_TICKET_TITLE} characters`">
        <UInput v-model="form.title" :maxlength="MAX_TICKET_TITLE" class="w-full" data-send-title />
      </UFormField>
      <p class="text-xs text-muted">The description is made from the annotations; the screenshot and the capture's document are attached.</p>
    </div>

    <div v-else-if="kind === 'ticket'" class="flex flex-col gap-2">
      <UButton
        v-if="runTicket"
        :label="`${runTicket} · the run's ticket`"
        icon="i-lucide-ticket"
        size="sm"
        color="neutral"
        :variant="!picked && form.key === runTicket ? 'soft' : 'ghost'"
        class="w-fit"
        data-send-run-ticket
        @click="chooseRunTicket"
      />
      <div class="flex flex-wrap items-center gap-2">
        <USelect v-model="pickerProject" :items="projectItems" size="sm" class="w-40" aria-label="The ticket's project" data-send-picker-project />
        <TicketPicker v-if="pickerProject" :key="pickerProject" :project="pickerProject" placeholder="Another ticket…" @pick="pick" />
      </div>
      <p class="text-xs text-muted" data-send-ticket>
        <template v-if="chosenKey">To <span class="font-mono" data-mono="key">{{ chosenKey }}</span>: the screenshot and the document attached, with a comment.</template>
        <template v-else>Choose the ticket.</template>
      </p>
    </div>

    <div v-else-if="kind === 'brakeman'" class="flex flex-col gap-2">
      <UFormField label="Your question" :hint="`At most ${MAX_SEND_QUESTION} characters`">
        <UTextarea v-model="form.question" :maxlength="MAX_SEND_QUESTION" :rows="3" autoresize class="w-full" data-send-question />
      </UFormField>
      <p class="text-xs text-muted">Brakeman reads the capture as it is sent now, never later edits.</p>
    </div>

    <div v-else-if="kind === 'decision'" class="flex flex-col gap-2">
      <UFormField label="Decision">
        <USelect v-model="form.decision" :items="decisionItems" class="w-full" data-send-decision />
      </UFormField>
      <p class="text-xs text-muted">A comment on the decision names this capture at r{{ capture.rev }}; whoever answers it reads what was sent.</p>
    </div>

    <!-- A review's own form, its Send held as the others' is while edits are not saved. -->
    <fieldset v-else-if="kind === 'review' || kind === 'review-draft'" :disabled="dirty" class="contents">
      <CaptureSendReview :key="kind" :capture="capture" :kind="kind" @sent="readSends" />
    </fieldset>
    <p v-if="reviewKind && dirty" class="text-xs text-muted" data-send-problem>Save your changes first: Send sends the saved r{{ capture.rev }}</p>

    <div v-if="!reviewKind" class="flex flex-wrap items-center gap-2">
      <UButton label="Send" icon="i-lucide-send" :loading="sending" :disabled="!!problem || sending" data-capture-send-go @click="send" />
      <span v-if="problem" class="text-xs text-muted" data-send-problem>{{ problem }}</span>
    </div>
    <p v-if="error" class="text-sm text-muted" role="status" data-send-error>{{ error }}</p>

    <ul v-if="rows.length" class="flex flex-col gap-2" data-capture-sends>
      <li v-for="r in rows" :key="r.send.id" class="flex flex-col gap-0.5 text-sm" :data-send-row="r.send.id" :data-send-state="r.send.state" :data-send-target="r.send.target ?? ''">
        <div class="flex min-w-0 flex-wrap items-center gap-2">
          <StatusMark :status="r.view.mark" word />
          <UButton
            v-if="cancellable(r.send)"
            label="Cancel"
            size="xs"
            color="neutral"
            variant="ghost"
            :loading="cancelling === r.send.id"
            :disabled="!!cancelling"
            data-send-cancel
            @click="cancel(r.send)"
          />
        </div>
        <p v-if="r.edits.length" class="text-xs text-muted" data-send-edited>edited since: {{ r.edits.join(', ') }}</p>
      </li>
    </ul>
  </section>
</template>
