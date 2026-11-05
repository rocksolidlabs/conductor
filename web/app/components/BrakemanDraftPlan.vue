<script setup lang="ts">
import type { PlanSummary } from '~/composables/useDispatch'
import type { BrakemanRequest, BriefItem, RefResolution } from '~/utils/brakeman'
import { displayBy, parseRef, refRoute } from '~/utils/brakeman'
import { draftPlanProblem, makeAgain, madeWords } from '~/utils/brakemanCards'
import { revisionTag } from '~/utils/dispatchPlans'
import { ApiError } from '~/composables/useApi'

/**
 * A draft plan revision (docs/plans/brakeman.md, PR-10 and PR-11): what Brakeman drafted for a ticket's plan, as a form the person edits
 * (the saved crew with tasks it runs, the base, the constraints, the note, the questions of the decisions a person will make), with the
 * inputs it hands over (the revision it revises, the references, the attachments, the comments) shown as drafted. "Propose as plan
 * revision" sends it through the make route (PR-4) to Dispatch's own propose route under the person's name, as the admin: the revision it
 * becomes is proposed, never approved, and is linked once made, with its state as Dispatch holds it. Approving it stays the operator's, on
 * the ticket's page or a decision card's Approve, both DS2's own. A proposal that did not finish (the backend failed after its claim) is
 * finished from here, asked again as it was sent: the form shows that content and takes no edit.
 */
const props = defineProps<{ request: BrakemanRequest; item: Extract<BriefItem, { kind: 'draft_plan/1' }>; index: number; refs: ReadonlyMap<string, RefResolution> }>()

const bk = useBrakeman()
const identity = useIdentity()
const dispatch = useDispatchApi()

const req = computed(() => bk.requests.value.find((r) => r.id === props.request.id) ?? props.request)
const made = computed(() => req.value.made?.find((m) => m.item === props.index))
const done = computed(() => made.value?.state === 'made')
const key = computed(() => parseRef(props.item.ticket)?.ticket ?? '')

/** The fields the person edits, from the draft (or from the make kept, while it is to be finished). */
const crew = ref(props.item.crew)
const base = ref(props.item.base ?? '')
const constraints = ref(props.item.constraints ?? '')
const note = ref(props.item.note ?? '')
const questions = ref<string[]>((props.item.decisions ?? []).map((d) => d.question))

/** The save this browser asked that did not finish, and what the card does about it. */
const kept = computed(() => bk.keptMake(props.request, props.index))
const proposing = ref(false)
const again = computed(() => makeAgain(made.value, proposing.value ? undefined : kept.value))
watch(
  again,
  (a) => {
    if (a.kind === 'drop') bk.forgetMake(props.request, props.index)
  },
  { immediate: true },
)
watch(
  () => (again.value.kind === 'finish' ? kept.value : undefined),
  (k) => {
    const e = k?.body.edit as Partial<Extract<BriefItem, { kind: 'draft_plan/1' }>> | undefined
    if (!e) return
    crew.value = e.crew ?? ''
    base.value = e.base ?? ''
    constraints.value = e.constraints ?? ''
    note.value = e.note ?? ''
    questions.value = (e.decisions ?? []).map((d) => d.question)
  },
  { immediate: true },
)
const finishing = computed(() => again.value.kind === 'finish')

/** The draft as the person edited it, in the draft's own shape: what the make route takes as its edit. */
const edit = computed(() => {
  const x = props.item
  const out: Record<string, unknown> = { ticket: x.ticket, crew: crew.value.trim() }
  if (base.value.trim()) out.base = base.value.trim()
  if (x.basedOn) out.basedOn = x.basedOn
  if (x.references?.length) out.references = x.references
  if (x.attachments?.length) out.attachments = x.attachments
  if (x.comments?.length) out.comments = x.comments
  if (constraints.value.trim()) out.constraints = constraints.value
  if (x.decisions?.length) out.decisions = x.decisions.map((d, i) => ({ ...d, question: questions.value[i]?.trim() || d.question }))
  if (note.value.trim()) out.note = note.value
  if (x.from?.length) out.from = x.from
  return out
})
const problem = computed(() => draftPlanProblem({ crew: crew.value, constraints: constraints.value, note: note.value, questions: questions.value }))

/** The revision it became, as Dispatch lists it now: read once it is made, and from the make's answer. */
const revision = ref<PlanSummary | null>(null)
async function readRevision() {
  const m = made.value
  if (!m?.plan || !m.key) return
  try {
    revision.value = (await dispatch.plans(m.key)).find((p) => p.id === m.plan) ?? null
  } catch {
    /* the link to the ticket still leads to it */
  }
}
watch(
  () => made.value?.plan,
  (id) => {
    if (id && revision.value?.id !== id) void readRevision()
  },
  { immediate: true },
)

const sending = ref(false)
const error = ref('')

async function propose() {
  if (sending.value || done.value || (!finishing.value && problem.value)) return
  sending.value = true
  error.value = ''
  try {
    let out
    if (finishing.value) out = await bk.finishMake(props.request, props.index)
    else {
      proposing.value = true
      out = await bk.make(props.request, props.index, { by: displayBy(identity.name.value) || undefined, edit: edit.value })
    }
    if (out.plan) revision.value = out.plan
  } catch (e) {
    if (e instanceof ApiError && e.code === 'already_made') void bk.readRequest(props.request.id)
    error.value = (e as Error).message
  } finally {
    sending.value = false
    proposing.value = false
  }
}
</script>

<template>
  <div class="flex flex-col gap-2 rounded-md border border-default p-3" data-brief-draft data-draft-plan :data-draft-item="index" :data-made-state="made?.state ?? ''">
    <div class="flex flex-wrap items-center gap-2 text-sm">
      <UIcon name="i-lucide-list-checks" class="size-4 text-muted" />
      <span class="font-medium">A draft plan revision for</span>
      <BrakemanRefChip :ref-name="item.ticket" :resolution="refs.get(item.ticket)" />
      <span v-if="item.basedOn" class="flex flex-wrap items-center gap-1 text-xs text-muted">revising <BrakemanRefChip :ref-name="item.basedOn" :resolution="refs.get(item.basedOn)" /></span>
      <span v-if="item.from?.length" class="flex flex-wrap items-center gap-1 text-xs text-muted">from <BrakemanRefChip v-for="r in item.from" :key="r" :ref-name="r" :resolution="refs.get(r)" /></span>
    </div>
    <ul v-if="item.references?.length || item.attachments?.length || item.comments?.length" class="flex flex-col gap-0.5 text-xs text-muted" data-draft-plan-inputs>
      <li v-for="r in item.references ?? []" :key="`${r.path}:${r.start ?? ''}`">
        <span class="font-mono">{{ r.path }}<template v-if="r.start">:{{ r.start }}<template v-if="r.end">-{{ r.end }}</template></template></span>
        <template v-if="r.note"> · {{ r.note }}</template>
      </li>
      <li v-if="item.attachments?.length">Attachments handed over: <span class="font-mono">{{ item.attachments.join(', ') }}</span></li>
      <li v-if="item.comments?.length" class="flex flex-wrap items-center gap-1">Comments handed over: <BrakemanRefChip v-for="c in item.comments" :key="c" :ref-name="c" :resolution="refs.get(c)" /></li>
    </ul>
    <template v-if="done">
      <p class="text-sm" data-made-words>{{ madeWords(made) }}</p>
      <div class="flex flex-wrap items-center gap-2 text-xs text-muted" data-draft-plan-made :data-plan-revision="revision?.n ?? ''" :data-plan-state="revision?.state ?? ''">
        <span v-if="revision">{{ key }} {{ revisionTag(revision.n) }} is {{ revision.state }}.</span>
        <UButton :to="refRoute(item.ticket) ?? undefined" :label="revision ? `Open ${key} ${revisionTag(revision.n)}` : `Open ${key}`" icon="i-lucide-arrow-up-right" size="xs" color="neutral" variant="outline" data-made-link />
      </div>
    </template>
    <template v-else>
      <div class="grid gap-2 sm:grid-cols-2">
        <UFormField label="Crew" description="A saved crew with tasks: what the revision runs."><UInput v-model="crew" class="w-full font-mono text-xs" :disabled="finishing" data-draft-plan-crew /></UFormField>
        <UFormField label="Base" description="A branch, a tag or a commit; HEAD when empty."><UInput v-model="base" class="w-full font-mono text-xs" placeholder="HEAD" :disabled="finishing" data-draft-plan-base /></UFormField>
      </div>
      <UFormField label="Constraints" description="What the work must keep, Markdown."><UTextarea v-model="constraints" :rows="2" autoresize class="w-full" :disabled="finishing" data-draft-plan-constraints /></UFormField>
      <div v-if="item.decisions?.length" class="flex flex-col gap-1.5" data-draft-plan-decisions>
        <span class="text-xs font-medium text-muted">The decisions a person will make</span>
        <div v-for="(d, i) in item.decisions" :key="d.id" class="flex flex-wrap items-center gap-2 text-xs">
          <UBadge :label="d.kind" color="neutral" variant="soft" size="sm" />
          <UInput v-model="questions[i]" class="min-w-0 flex-1" size="sm" :disabled="finishing" :data-draft-plan-decision="d.id" />
          <span v-if="d.before?.length" class="text-muted">before <span class="font-mono">{{ d.before.join(', ') }}</span></span>
        </div>
      </div>
      <UFormField label="Note" description="The revision's note, Markdown."><UTextarea v-model="note" :rows="2" autoresize class="w-full" :disabled="finishing" data-draft-plan-note /></UFormField>
      <p v-if="problem && !finishing" class="text-xs text-error">{{ problem }}</p>
      <p v-if="again.words" class="text-xs text-muted" data-made-words>{{ again.words }}</p>
      <p v-else-if="made" class="text-xs text-muted" data-made-words>{{ madeWords(made) }}</p>
      <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-draft-error />
      <div class="flex flex-wrap items-center gap-2">
        <UButton v-if="finishing" label="Finish proposing it" icon="i-lucide-rotate-cw" size="sm" :loading="sending" data-draft-finish @click="propose" />
        <UButton v-else label="Propose as plan revision" icon="i-lucide-send" size="sm" :loading="sending" :disabled="!!problem || made?.state === 'pending'" data-draft-propose @click="propose" />
        <span class="text-xs text-muted">Proposed under your name; the operator approves it, or not. Brakeman does neither.</span>
      </div>
    </template>
  </div>
</template>
