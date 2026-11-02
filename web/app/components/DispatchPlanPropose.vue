<script setup lang="ts">
import type { DispatchTicket, NewPlan, NewPlanReference, PlanBody, PlanDecision, PlanSummary } from '~/composables/useDispatch'
import type { CrewSummary } from '~/composables/useSessions'
import type { TicketPlans } from '~/composables/useTicketPlans'
import { PLAN_BOUNDS, parseReference, referenceText, revisionTag, shortCommit } from '~/utils/dispatchPlans'

/**
 * Proposing a plan revision of a ticket (Program item DS2, docs/plans/dispatch-evidence.md, "web/app"): a saved crew with tasks, taken
 * whole as it is now (a crew without tasks, or one that runs on another machine, offered greyed with why: a revision is of a managed crew,
 * and the crew editor is where a crew changes); the commit it starts from, HEAD by default, which the server pins to a full commit id;
 * source references as rows (`path:12-30=note`); the ticket's attachments and recent comments to hand over, as checkboxes; constraints;
 * the decisions a person will make, as rows; a note. Revising prefills it all from the revision it revises, and the new one names it. The
 * server reads the crew, the commit and the references and refuses, in words, what does not hold; the refusal is said in place.
 */
const props = defineProps<{
  ticket: DispatchTicket
  plans: TicketPlans
  /** The revision this one revises, with its body: the dialog is prefilled from it. */
  from?: { revision: PlanSummary; body: PlanBody | null } | null
}>()
const open = defineModel<boolean>('open', { default: false })

const sessions = useSessions()
const toast = useToast()

const crews = ref<CrewSummary[]>([])
const crewsError = ref('')
const crew = ref<string | undefined>(undefined)
/** The picked crew's task ids, for the decisions' "before". */
const crewTasks = ref<string[]>([])
const base = ref('')
const refs = ref<string[]>([])
const attachments = ref<string[]>([])
const comments = ref<string[]>([])
const constraints = ref('')
const decisions = ref<PlanDecision[]>([])
const note = ref('')
const error = ref('')
const sending = ref(false)

/** Whether a crew can be planned, from its summary: with tasks every member starts by them, and it runs on the server. */
function crewWhy(c: CrewSummary): string {
  if (c.where === 'host') return 'runs on another machine: a revision is of a crew the server runs'
  if (!c.members.length || !c.members.every((m) => m.start.when === 'task')) return 'no tasks: a revision is of a crew with tasks'
  return ''
}
const crewItems = computed(() =>
  crews.value
    .map((c) => ({ label: c.name, value: c.id, description: crewWhy(c) || `${c.members.length} ${c.members.length === 1 ? 'member' : 'members'} · ${c.cwd || 'the server’s folder'}`, disabled: !!crewWhy(c) }))
    .sort((a, b) => Number(a.disabled) - Number(b.disabled) || a.label.localeCompare(b.label)),
)

const liveAttachments = computed(() => (props.ticket.attachments ?? []).filter((a) => !a.deletedAt))
/** The recent comments offered: the newest live ones the ticket's read carries, at most the bound. */
const recentComments = computed(() => (props.ticket.comments ?? []).filter((c) => !c.deletedAt).slice(-PLAN_BOUNDS.comments).reverse())

async function loadCrews() {
  crewsError.value = ''
  try {
    crews.value = (await sessions.listCrews(0, 500)).crews
  } catch (e) {
    crewsError.value = (e as Error).message
  }
}

watch(open, (o) => {
  if (!o) return
  const f = props.from
  const b = f?.body
  crew.value = f?.revision.crewId
  base.value = f ? f.revision.base : ''
  refs.value = (b?.references ?? []).map((r) => referenceText(r))
  const live = new Set(liveAttachments.value.map((a) => a.id))
  attachments.value = (b?.attachments ?? []).filter((id) => live.has(id))
  const liveComments = new Set((props.ticket.comments ?? []).filter((c) => !c.deletedAt).map((c) => c.id))
  comments.value = (b?.comments ?? []).map((c) => c.id).filter((id) => liveComments.has(id))
  constraints.value = b?.constraints ?? ''
  decisions.value = (b?.decisions ?? []).map((d) => ({ ...d, before: [...d.before] }))
  note.value = ''
  error.value = ''
  void loadCrews()
})

// The picked crew's tasks, for the decisions' "before": read in full, since a list's summary carries none.
watch(crew, async (id) => {
  crewTasks.value = []
  if (!id) return
  try {
    const c = await sessions.getCrew(id)
    if (crew.value === id) crewTasks.value = (c.tasks ?? []).map((t) => t.id)
  } catch {
    // The proposal says what is wrong with the crew.
  }
})

const parsed = computed(() => refs.value.map((r) => (r.trim() ? parseReference(r) : null)))
const refErrors = computed(() => parsed.value.map((p) => (p && 'error' in p ? p.error : '')))

function addRef() {
  if (refs.value.length < PLAN_BOUNDS.references) refs.value = [...refs.value, '']
}
function removeRef(i: number) {
  refs.value = refs.value.filter((_, j) => j !== i)
}
function addDecision() {
  if (decisions.value.length < PLAN_BOUNDS.decisions) decisions.value = [...decisions.value, { id: `d${decisions.value.length + 1}`, question: '', kind: 'approval', before: [] }]
}
function removeDecision(i: number) {
  decisions.value = decisions.value.filter((_, j) => j !== i)
}
function toggle(which: 'attachments' | 'comments', id: string, on: boolean | 'indeterminate') {
  const list = which === 'attachments' ? attachments : comments
  list.value = on === true ? [...new Set([...list.value, id])] : list.value.filter((x) => x !== id)
}

const kindItems = [
  { label: 'Approval', value: 'approval' },
  { label: 'Clarification', value: 'clarification' },
]

const blocked = computed(() => {
  if (!crew.value) return 'Pick a crew with tasks'
  if (refErrors.value.some((e) => e)) return 'A reference is not one'
  if (attachments.value.length > PLAN_BOUNDS.attachments) return `At most ${PLAN_BOUNDS.attachments} attachments`
  if (comments.value.length > PLAN_BOUNDS.comments) return `At most ${PLAN_BOUNDS.comments} comments`
  if (decisions.value.some((d) => !d.question.trim() || !/^[a-z0-9][a-z0-9_-]{0,39}$/.test(d.id))) return 'Each decision has an id (a-z, 0-9, _ and -) and a question'
  return ''
})

async function submit() {
  if (blocked.value || sending.value) return
  const req: NewPlan = { crew: crew.value! }
  if (base.value.trim()) req.base = base.value.trim()
  if (props.from) req.basedOn = props.from.revision.id
  const references = parsed.value.flatMap((p) => (p && 'ref' in p ? [p.ref as NewPlanReference] : []))
  if (references.length) req.references = references
  if (attachments.value.length) req.attachments = [...attachments.value]
  if (comments.value.length) req.comments = [...comments.value]
  if (constraints.value.trim()) req.constraints = constraints.value
  if (decisions.value.length) req.decisions = decisions.value.map((d) => ({ id: d.id.trim(), question: d.question.trim(), kind: d.kind, before: [...d.before] }))
  if (note.value.trim()) req.note = note.value
  sending.value = true
  error.value = ''
  try {
    const p = await props.plans.propose(req)
    open.value = false
    if (p) toast.add({ title: `${props.ticket.key}: revision ${p.n} proposed`, description: `From commit ${shortCommit(p.base)}`, icon: 'i-lucide-check', color: 'success' })
  } catch (e) {
    error.value = props.plans.failureWords(e)
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" :title="from ? `Revise ${revisionTag(from.revision.n)}` : 'Propose a plan revision'" :description="`Of ${ticket.key}: a saved crew with tasks, as it is now, and what every attempt is handed.`" :ui="{ content: 'max-w-2xl' }">
    <template #body>
      <form class="flex flex-col gap-4 text-sm" data-plan-propose-dialog @submit.prevent="submit">
        <UFormField label="Crew" size="sm" :error="crewsError || undefined">
          <USelectMenu v-model="crew" :items="crewItems" value-key="value" placeholder="A saved crew with tasks…" class="w-full" data-plan-propose-crew />
        </UFormField>
        <UFormField label="From commit" hint="a branch, a tag, HEAD or a commit; pinned to its commit" size="sm">
          <UInput v-model="base" :maxlength="PLAN_BOUNDS.base" placeholder="HEAD" class="w-full font-mono" data-plan-propose-base />
        </UFormField>

        <div class="flex flex-col gap-1.5">
          <div class="flex items-center gap-2">
            <span class="text-xs font-medium text-muted">References</span>
            <span class="text-xs text-dimmed">path, path:12 or path:12-30, then =a note</span>
            <div class="flex-1" />
            <UButton v-if="refs.length < PLAN_BOUNDS.references" label="Add" icon="i-lucide-plus" size="xs" color="neutral" variant="ghost" data-plan-propose-ref-add @click="addRef" />
          </div>
          <div v-for="(_, i) in refs" :key="i" class="flex flex-col gap-0.5">
            <div class="flex items-center gap-1">
              <UInput v-model="refs[i]" placeholder="internal/api/session.go:12-30=the loop" class="min-w-0 flex-1 font-mono" size="sm" :data-plan-propose-ref="i" />
              <UButton icon="i-lucide-x" size="xs" color="neutral" variant="ghost" :aria-label="`Remove reference ${i + 1}`" @click="removeRef(i)" />
            </div>
            <span v-if="refErrors[i]" class="text-xs text-warning">{{ refErrors[i] }}</span>
          </div>
        </div>

        <div v-if="liveAttachments.length" class="flex flex-col gap-1.5">
          <span class="text-xs font-medium text-muted">Attachments to hand over <span class="text-dimmed">(at most {{ PLAN_BOUNDS.attachments }})</span></span>
          <UCheckbox v-for="a in liveAttachments" :key="a.id" :model-value="attachments.includes(a.id)" :label="a.name" :data-plan-propose-attachment="a.name" @update:model-value="(v) => toggle('attachments', a.id, v)" />
        </div>
        <div v-if="recentComments.length" class="flex flex-col gap-1.5">
          <span class="text-xs font-medium text-muted">Comments to hand over <span class="text-dimmed">(their words now; an edit after makes the approval stale)</span></span>
          <UCheckbox
            v-for="c in recentComments"
            :key="c.id"
            :model-value="comments.includes(c.id)"
            :label="`#${c.n} ${c.author || c.by}: ${c.body.slice(0, 80)}${c.body.length > 80 ? '…' : ''}`"
            :data-plan-propose-comment="c.n"
            @update:model-value="(v) => toggle('comments', c.id, v)"
          />
        </div>

        <UFormField label="Constraints" size="sm">
          <UTextarea v-model="constraints" :rows="2" autoresize :maxlength="PLAN_BOUNDS.constraints" placeholder="What the work must not do…" class="w-full" data-plan-propose-constraints />
        </UFormField>

        <div class="flex flex-col gap-1.5">
          <div class="flex items-center gap-2">
            <span class="text-xs font-medium text-muted">Decisions a person makes</span>
            <span class="text-xs text-dimmed">recorded and shown, not held yet</span>
            <div class="flex-1" />
            <UButton v-if="decisions.length < PLAN_BOUNDS.decisions" label="Add" icon="i-lucide-plus" size="xs" color="neutral" variant="ghost" data-plan-propose-decision-add @click="addDecision" />
          </div>
          <div v-for="(d, i) in decisions" :key="i" class="flex flex-col gap-1 rounded-md p-2 ring ring-default sm:flex-row sm:items-start">
            <UInput v-model="d.id" placeholder="id" class="font-mono sm:w-24" size="sm" aria-label="The decision's id" />
            <USelect v-model="d.kind" :items="kindItems" size="sm" class="sm:w-36" aria-label="Its kind" />
            <UInput v-model="d.question" :maxlength="PLAN_BOUNDS.decisionQuestion" placeholder="The question…" class="min-w-0 flex-1" size="sm" aria-label="The question" />
            <USelectMenu v-model="d.before" :items="crewTasks" multiple placeholder="Before tasks…" size="sm" class="sm:w-40" aria-label="The tasks that wait on it" />
            <UButton icon="i-lucide-x" size="xs" color="neutral" variant="ghost" :aria-label="`Remove decision ${i + 1}`" @click="removeDecision(i)" />
          </div>
        </div>

        <UFormField label="Note" hint="why this revision" size="sm">
          <UTextarea v-model="note" :rows="2" autoresize :maxlength="PLAN_BOUNDS.note" class="w-full" data-plan-propose-note />
        </UFormField>

        <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" title="Not proposed" :description="error" data-plan-propose-error />
        <div class="flex items-center justify-end gap-2">
          <span v-if="blocked" class="mr-auto text-xs text-muted">{{ blocked }}</span>
          <UButton label="Cancel" color="neutral" variant="ghost" :disabled="sending" @click="open = false" />
          <UButton type="submit" :label="from ? 'Propose the revision' : 'Propose'" icon="i-lucide-file-pen-line" :disabled="!!blocked" :loading="sending" data-plan-propose-submit />
        </div>
      </form>
    </template>
  </UModal>
</template>
