<script setup lang="ts">
import type { AgentInfo, CrewInfo } from '~/composables/useSessions'
import { COMPARE_LIMITS, compareProblems, compareSpec, draftComparison, draftOnOpen, rowKey, withCandidateAgent, type DraftCandidate, type DraftComparison } from '~/utils/compareTemplate'

/**
 * New comparison (CP1, docs/plans/compare-crew.md, "The template"), opened from Crews: the project folder (the crew's working directory),
 * the objective (its goal), the instructions every candidate gets, one to twelve criteria, the checks every candidate's result must pass
 * (or the project's conductor.manifest.json verify steps), two to four candidates (each an agent, its arguments, which is how a model is
 * chosen, and an optional approach), whether an agent reviews each candidate, and whether Brakeman drafts the comparison. Create posts the
 * template (POST /api/crews/templates/compare), which saves an ordinary crew with tasks approach-a, approach-b… and a `compare` block; the
 * page opens it. The server holds the spec to its rules and its refusal is shown in its words. Nothing launches here: a comparison is
 * launched from a ticket's approved plan revision. A form closed by Escape or a click outside keeps its draft for its next opening; Cancel
 * and a crew created end it.
 */
const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ agents: readonly AgentInfo[] }>()
const emit = defineEmits<{ created: [crew: CrewInfo] }>()

const api = useSessions()
const draft = ref<DraftComparison>(draftComparison())
const creating = ref(false)
const error = ref('')

/** Whether `draft` is one being made, kept across a close; false once Cancel or Create ended it. */
let kept = false
watch(open, (o) => {
  if (!o) return
  draft.value = draftOnOpen(kept ? draft.value : null, props.agents[0]?.id ?? '')
  kept = true
  error.value = ''
})
function cancel() {
  kept = false
  open.value = false
}

const agentItems = computed(() => props.agents.map((a) => ({ label: a.name, value: a.id })))
const problems = computed(() => compareProblems(draft.value, props.agents))
const letter = (i: number) => String.fromCharCode(97 + i)
const takesArgs = (id: string) => props.agents.find((a) => a.id === id)?.allowArgs !== false

function patch(p: Partial<DraftComparison>) {
  draft.value = { ...draft.value, ...p }
}
function setCriterion(i: number, text: string) {
  patch({ criteria: draft.value.criteria.map((c, j) => (j === i ? { ...c, text } : c)) })
}
function setCandidate(i: number, p: Partial<DraftCandidate>) {
  patch({ candidates: draft.value.candidates.map((c, j) => (j === i ? { ...c, ...p } : c)) })
}
function setAgent(i: number, id: string) {
  patch({ candidates: draft.value.candidates.map((c, j) => (j === i ? withCandidateAgent(c, id, props.agents) : c)) })
}
function addCandidate() {
  const last = draft.value.candidates.at(-1)
  patch({ candidates: [...draft.value.candidates, { key: rowKey(), agentId: last?.agentId ?? props.agents[0]?.id ?? '', args: [], approach: '' }] })
}

async function create() {
  if (creating.value || problems.value.length) return
  creating.value = true
  error.value = ''
  try {
    const crew = await api.createCompareCrew(compareSpec(draft.value))
    kept = false
    open.value = false
    emit('created', crew)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    creating.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" title="New comparison" description="One objective, worked by two to four candidates apart from one base, each checked alike. It is saved as a crew; a ticket's approved plan revision launches it." :ui="{ content: 'sm:max-w-2xl', wrapper: 'min-w-0 pe-10' }" data-compare-template-dialog>
    <template #body>
      <UForm :state="draft" class="flex flex-col gap-5 text-sm" data-compare-template @submit="create">
        <div class="grid gap-4 sm:grid-cols-2">
          <UFormField label="Name" name="name" :hint="`at most ${COMPARE_LIMITS.name} characters`">
            <UInput :model-value="draft.name" :maxlength="COMPARE_LIMITS.name" placeholder="What is compared" class="w-full" data-compare-name @update:model-value="patch({ name: String($event ?? '') })" />
          </UFormField>
          <UFormField label="Project folder" name="cwd" help="The crew's working directory: a git repository with a commit, on the server.">
            <DirInput :model-value="draft.cwd" name="cwd" placeholder="the ticket's project folder" picker-title="The project folder" data-compare-cwd @update:model-value="patch({ cwd: $event })" />
          </UFormField>
        </div>

        <UFormField label="Objective" name="goal" help="The crew's goal. The ticket's own intent travels in the launch package.">
          <UTextarea :model-value="draft.goal" :rows="2" autoresize :maxrows="6" :maxlength="COMPARE_LIMITS.goal" placeholder="What every candidate is to achieve" class="w-full" data-compare-goal @update:model-value="patch({ goal: String($event ?? '') })" />
        </UFormField>
        <UFormField label="Instructions" name="instructions" help="Every candidate gets the same.">
          <UTextarea :model-value="draft.instructions" :rows="3" autoresize :maxrows="10" :maxlength="COMPARE_LIMITS.instructions" placeholder="How to work it: what to keep, where to look" class="w-full" data-compare-instructions @update:model-value="patch({ instructions: String($event ?? '') })" />
        </UFormField>

        <div class="flex flex-col gap-2" data-compare-criteria>
          <span class="font-semibold text-highlighted">Criteria <span class="font-normal text-muted">· one line each, {{ COMPARE_LIMITS.minCriteria }} to {{ COMPARE_LIMITS.criteria }}</span></span>
          <div v-for="(c, i) in draft.criteria" :key="c.key" class="flex items-center gap-2">
            <UInput :model-value="c.text" :maxlength="COMPARE_LIMITS.criterionText" placeholder="no new dependency" :aria-label="`Criterion ${i + 1}`" class="min-w-0 flex-1" :data-compare-criterion="i" @update:model-value="setCriterion(i, String($event ?? ''))" />
            <IconButton icon="i-lucide-x" color="neutral" variant="ghost" size="xs" :label="`Remove criterion ${i + 1}`" :disabled="draft.criteria.length <= COMPARE_LIMITS.minCriteria" @click="patch({ criteria: draft.criteria.filter((_, j) => j !== i) })" />
          </div>
          <UButton label="Add criterion" icon="i-lucide-plus" color="secondary" variant="link" size="sm" class="self-start px-0" :disabled="draft.criteria.length >= COMPARE_LIMITS.criteria" data-compare-add-criterion @click="patch({ criteria: [...draft.criteria, { key: rowKey(), text: '' }] })" />
        </div>

        <div class="flex flex-col gap-2" data-compare-checks>
          <span class="font-semibold text-highlighted">Checks <span class="font-normal text-muted">· every candidate's result must pass them, run alike in its own checkout</span></span>
          <CrewCheckList :model-value="draft.checks" what="check" @update:model-value="patch({ checks: $event })" />
          <UCheckbox :model-value="draft.manifestFile" label="Also run the verify steps of the project's conductor.manifest.json, read at each candidate's commit" data-compare-manifest-file @update:model-value="patch({ manifestFile: !!$event })" />
        </div>

        <div class="flex flex-col gap-3" data-compare-candidates>
          <span class="font-semibold text-highlighted">Candidates <span class="font-normal text-muted">· {{ COMPARE_LIMITS.minCandidates }} to {{ COMPARE_LIMITS.candidates }}, each working alone from the same base</span></span>
          <div v-for="(c, i) in draft.candidates" :key="c.key" class="flex flex-col gap-2 rounded-md border border-default p-3" :data-compare-candidate-row="i">
            <div class="flex items-center gap-2">
              <span class="font-medium text-highlighted">Candidate {{ letter(i).toUpperCase() }}</span>
              <code class="text-xs text-muted" data-mono="id">approach-{{ letter(i) }}</code>
              <IconButton icon="i-lucide-x" color="neutral" variant="ghost" size="xs" class="ml-auto" :label="`Remove candidate ${letter(i).toUpperCase()}`" :disabled="draft.candidates.length <= COMPARE_LIMITS.minCandidates" @click="patch({ candidates: draft.candidates.filter((_, j) => j !== i) })" />
            </div>
            <div class="grid gap-2 sm:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]">
              <USelect :model-value="c.agentId" :items="agentItems" placeholder="Agent" :aria-label="`Agent of candidate ${letter(i).toUpperCase()}`" class="w-full" data-compare-candidate-agent @update:model-value="setAgent(i, String($event ?? ''))" />
              <ArgvInput v-if="takesArgs(c.agentId)" :model-value="c.args" :pending="c.argsPending ?? ''" placeholder="arguments, such as --model; a quoted argument is one" :aria-label="`Arguments of candidate ${letter(i).toUpperCase()}`" data-compare-candidate-args @update:model-value="setCandidate(i, { args: $event })" @update:pending="setCandidate(i, { argsPending: $event || undefined })" />
              <span v-else class="self-center text-xs text-muted">This agent takes no arguments.</span>
            </div>
            <UTextarea :model-value="c.approach" :rows="1" autoresize :maxrows="5" :maxlength="COMPARE_LIMITS.approach" placeholder="An approach, if you want one: key the cache by locale. Left empty, the candidates are independent tries." :aria-label="`Approach of candidate ${letter(i).toUpperCase()}`" class="w-full" data-compare-candidate-approach @update:model-value="setCandidate(i, { approach: String($event ?? '') })" />
          </div>
          <UButton label="Add candidate" icon="i-lucide-plus" color="secondary" variant="link" size="sm" class="self-start px-0" :disabled="draft.candidates.length >= COMPARE_LIMITS.candidates || !agents.length" data-compare-add-candidate @click="addCandidate" />
        </div>

        <div class="flex flex-col gap-2">
          <USwitch :model-value="draft.reviews" label="An agent reviews each candidate that succeeds" size="sm" data-compare-reviews @update:model-value="patch({ reviews: $event, reviewAgent: $event ? draft.reviewAgent || (agents[0]?.id ?? '') : '' })" />
          <USelect v-if="draft.reviews" :model-value="draft.reviewAgent" :items="agentItems" placeholder="The reviewing agent" aria-label="The reviewing agent" class="w-full sm:w-72" data-compare-review-agent @update:model-value="patch({ reviewAgent: String($event ?? '') })" />
          <USwitch :model-value="draft.brakeman" label="Brakeman drafts the comparison when all candidates are done, on your Brakeman account" size="sm" data-compare-brakeman @update:model-value="patch({ brakeman: $event })" />
        </div>

        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-circle-x" title="Not created" :description="error" data-compare-template-error />
        <div class="flex flex-wrap items-center justify-end gap-2">
          <span v-if="problems.length" class="mr-auto min-w-0 text-xs text-muted" data-compare-template-problem>{{ problems[0] }}</span>
          <UButton label="Cancel" color="neutral" variant="ghost" :disabled="creating" data-compare-cancel @click="cancel" />
          <UButton type="submit" label="Create" icon="i-lucide-check" :loading="creating" :disabled="!!problems.length" data-compare-create />
        </div>
      </UForm>
    </template>
  </UModal>
</template>
