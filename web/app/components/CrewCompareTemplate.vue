<script setup lang="ts">
import type { AgentInfo, CrewInfo } from '~/composables/useSessions'
import {
  COMPARE_LIMITS,
  agentLetter,
  checkNote,
  compareProblems,
  compareSpec,
  draftOnOpen,
  inMore,
  optionsLine,
  rowKey,
  type DraftAgent,
  type DraftComparison,
  type TrialField,
} from '~/utils/compareTemplate'
import { effectiveYolo } from '~/utils/yolo'

/**
 * New agent trial (CP1, docs/plans/compare-crew.md, "The template", where a trial is called a comparison), opened from Crews. Its face:
 * the task, the agents, the command that checks each result and the folder; the rest under More options; Yolo beside the button. The
 * press posts the template (POST /api/crews/templates/compare), which saves an ordinary crew with tasks approach-a, approach-b… and a
 * `compare` block; the page opens it. Nothing launches here: a trial starts from a ticket's approved plan. Nothing is marked before the
 * first press; a press with problems marks each field with its words, opens More options when the first is inside it, and puts the
 * cursor in the first. The server holds the spec to its rules and its refusal is shown in its words. A form closed by Escape, the X or a
 * click outside keeps its draft for its next opening; Cancel and a save end it.
 */
const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ agents: readonly AgentInfo[]; yoloDefault: boolean }>()
const emit = defineEmits<{ created: [crew: CrewInfo] }>()

const api = useSessions()
const draft = ref<DraftComparison>(draftOnOpen(null, props.agents))
const creating = ref(false)
const error = ref('')
/** Whether the button was pressed in this draft: only then are its fields marked. */
const pressed = ref(false)
const moreOpen = ref(false)
const formRef = useTemplateRef<HTMLFormElement>('form')

/** Whether `draft` is one being made, kept across a close; false once Cancel or a save ended it. */
let kept = false
watch(open, (o) => {
  if (!o) return
  if (!kept) {
    pressed.value = false
    moreOpen.value = false
  }
  draft.value = draftOnOpen(kept ? draft.value : null, props.agents)
  kept = true
  error.value = ''
})
function cancel() {
  kept = false
  open.value = false
}

const problems = computed(() => compareProblems(draft.value, props.agents))
/** The words under a field, once the button was pressed. */
function marked(...fields: TrialField[]): string | undefined {
  if (!pressed.value) return undefined
  const words = problems.value.filter((p) => fields.includes(p.field)).map((p) => p.words)
  return words.length ? words.join(' ') : undefined
}
const agentsMark = computed(() => marked('agents', ...draft.value.agents.map((_, i) => `agent-${i}` as const)))
const note = computed(() => checkNote(draft.value.check))
const yoloShown = computed(() => effectiveYolo(draft.value.yolo, props.yoloDefault))
const takesArgs = (id: string) => props.agents.find((a) => a.id === id)?.allowArgs !== false
const nameOf = (id: string) => props.agents.find((a) => a.id === id)?.name ?? id
const agentItems = computed(() => props.agents.filter((a) => a.id !== 'shell' && a.available !== false).map((a) => ({ label: a.name, value: a.id })))

function patch(p: Partial<DraftComparison>) {
  draft.value = { ...draft.value, ...p }
}
function setCriterion(i: number, text: string) {
  patch({ criteria: draft.value.criteria.map((c, j) => (j === i ? { ...c, text } : c)) })
}
function setAgent(i: number, p: Partial<DraftAgent>) {
  patch({ agents: draft.value.agents.map((a, j) => (j === i ? { ...a, ...p } : a)) })
}

/** The element a field's problem puts the cursor in. */
function fieldTarget(field: TrialField): string {
  const n = field.replace(/^\D+-/, '')
  if (field === 'task') return '[data-compare-goal] textarea'
  if (field === 'agents') return '[data-trial-agent], [data-trial-add-agent]'
  if (field.startsWith('agent-')) return `[data-trial-agent="${n}"]`
  if (field === 'check') return '[data-compare-check] input'
  if (field === 'folder') return '[data-compare-cwd] input'
  if (field === 'name') return '[data-compare-name] input'
  if (field === 'told') return '[data-compare-instructions] textarea'
  if (field === 'criteria') return '[data-compare-criterion] input'
  if (field === 'checks') return '[data-compare-checks] input'
  if (field === 'reviews') return '[data-compare-review-agent]'
  if (field.startsWith('args-')) return `[data-compare-agent-args="${n}"] input`
  return `[data-compare-agent-approach="${n}"] input`
}

/** The press: a draft with problems marks them and puts the cursor in the first; one without is posted. */
async function press() {
  if (creating.value) return
  pressed.value = true
  const first = problems.value[0]
  if (first) {
    if (inMore(first.field)) moreOpen.value = true
    await nextTick()
    formRef.value?.querySelector<HTMLElement>(fieldTarget(first.field))?.focus()
    return
  }
  creating.value = true
  error.value = ''
  try {
    const crew = await api.createCompareCrew(compareSpec(draft.value, yoloShown.value))
    kept = false
    open.value = false
    emit('created', crew)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    creating.value = false
  }
}

/** Ctrl or Cmd with Enter presses the button from any field. */
function onKey(e: KeyboardEvent) {
  if (e.key !== 'Enter' || !(e.ctrlKey || e.metaKey) || e.isComposing) return
  e.preventDefault()
  void press()
}
</script>

<template>
  <UModal
    v-model:open="open"
    title="New agent trial"
    description="Two to four agents each try the same task on their own copy. You keep the best result."
    :ui="{ content: 'sm:max-w-2xl', wrapper: 'min-w-0 pe-10' }"
    data-compare-template-dialog
  >
    <template #body>
      <form ref="form" class="text-sm" data-compare-template :aria-busy="creating" @submit.prevent="press" @keydown="onKey">
        <fieldset :disabled="creating" class="flex min-w-0 flex-col gap-5">
          <UFormField label="Task" name="task" :error="marked('task')">
            <UTextarea
              :model-value="draft.task"
              :rows="2"
              autoresize
              :maxrows="6"
              :maxlength="COMPARE_LIMITS.goal"
              placeholder="What should each agent do?"
              autofocus
              class="w-full"
              data-compare-goal
              @update:model-value="patch({ task: String($event ?? '') })"
            />
          </UFormField>

          <UFormField label="Agents" name="agents" :error="agentsMark">
            <CrewTrialAgents :model-value="draft.agents" :agents="agents" :disabled="creating" @update:model-value="patch({ agents: $event })" @agents="open = false" />
          </UFormField>

          <div class="grid gap-4 sm:grid-cols-2">
            <UFormField label="Check" name="check" :error="marked('check')" :help="note || 'A command each result must pass, such as npm test.'">
              <UInput :model-value="draft.check" autocapitalize="off" spellcheck="false" :ui="{ base: 'font-mono' }" class="w-full" data-compare-check @update:model-value="patch({ check: String($event ?? '') })" />
            </UFormField>
            <UFormField label="Folder" name="folder" :error="marked('folder')">
              <DirInput :model-value="draft.folder" name="folder" picker-title="The trial's folder" data-compare-cwd @update:model-value="patch({ folder: $event })" />
            </UFormField>
          </div>

          <MoreOptions v-model:open="moreOpen" :line="optionsLine(draft)">
            <div class="grid gap-4 sm:grid-cols-2">
              <UFormField label="Name" name="name" :error="marked('name')">
                <UInput :model-value="draft.name" :maxlength="COMPARE_LIMITS.name" placeholder="The task's first words" class="w-full" data-compare-name @update:model-value="patch({ name: String($event ?? '') })" />
              </UFormField>
            </div>
            <UFormField label="Told to every agent" name="told" :error="marked('told')">
              <UTextarea :model-value="draft.told" :rows="2" autoresize :maxrows="10" :maxlength="COMPARE_LIMITS.instructions" placeholder="Extra instructions, the same for each" class="w-full" data-compare-instructions @update:model-value="patch({ told: String($event ?? '') })" />
            </UFormField>
            <UFormField label="What a good result does" name="criteria" :error="marked('criteria')">
              <div class="flex flex-col gap-2" data-compare-criteria>
                <div v-for="(c, i) in draft.criteria" :key="c.key" class="flex items-center gap-2">
                  <UInput :model-value="c.text" :maxlength="COMPARE_LIMITS.criterionText" :aria-label="`What a good result does, line ${i + 1}`" class="min-w-0 flex-1" :data-compare-criterion="i" @update:model-value="setCriterion(i, String($event ?? ''))" />
                  <IconButton icon="i-lucide-x" color="neutral" variant="ghost" size="xs" :label="`Remove line ${i + 1}`" :disabled="draft.criteria.length <= COMPARE_LIMITS.minCriteria" @click="patch({ criteria: draft.criteria.filter((_, j) => j !== i) })" />
                </div>
                <UButton label="Add a line" icon="i-lucide-plus" color="secondary" variant="link" size="sm" class="self-start px-0" :disabled="draft.criteria.length >= COMPARE_LIMITS.criteria" data-compare-add-criterion @click="patch({ criteria: [...draft.criteria, { key: rowKey(), text: '' }] })" />
              </div>
            </UFormField>
            <UFormField label="More checks" name="checks" :error="marked('checks')">
              <div class="flex flex-col gap-2" data-compare-checks>
                <CrewCheckList :model-value="draft.checks" what="check" :taken="draft.check.trim() ? ['check'] : []" @update:model-value="patch({ checks: $event })" />
                <UCheckbox :model-value="draft.manifestFile" label="Also run the project's own checks (conductor.manifest.json)" data-compare-manifest-file @update:model-value="patch({ manifestFile: !!$event })" />
              </div>
            </UFormField>
            <div v-for="(a, i) in draft.agents" :key="a.key" class="grid gap-2 sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)_minmax(0,1fr)]" :data-compare-agent-row="i">
              <span class="self-center font-medium text-highlighted">{{ agentLetter(i) }} <span class="font-normal text-default">{{ nameOf(a.agentId) }}</span></span>
              <UFormField :error="marked(`approach-${i}`)">
                <UInput :model-value="a.approach" :maxlength="COMPARE_LIMITS.approach" placeholder="Optional: a way to try it" :aria-label="`Approach of agent ${agentLetter(i)}`" class="w-full" :data-compare-agent-approach="i" @update:model-value="setAgent(i, { approach: String($event ?? '') })" />
              </UFormField>
              <UFormField :error="marked(`args-${i}`)">
                <UInput v-if="takesArgs(a.agentId)" :model-value="a.args" placeholder="Flags, as you would type them in a terminal" autocapitalize="off" spellcheck="false" :ui="{ base: 'font-mono' }" :aria-label="`Arguments of agent ${agentLetter(i)}`" class="w-full" :data-compare-agent-args="i" @update:model-value="setAgent(i, { args: String($event ?? '') })" />
                <span v-else class="text-xs text-muted">Takes no arguments.</span>
              </UFormField>
            </div>
            <div class="flex flex-col gap-2">
              <USwitch :model-value="draft.reviews" label="Review" description="An agent reviews each result that passes, and changes nothing." size="sm" data-compare-reviews @update:model-value="patch({ reviews: $event, reviewAgent: $event ? draft.reviewAgent || (agentItems[0]?.value ?? '') : '' })" />
              <UFormField v-if="draft.reviews" label="by" name="reviewAgent" :error="marked('reviews')">
                <USelect :model-value="draft.reviewAgent" :items="agentItems" aria-label="The agent that reviews" class="w-full sm:w-72" data-compare-review-agent @update:model-value="patch({ reviewAgent: String($event ?? '') })" />
              </UFormField>
              <USwitch :model-value="draft.brakeman" label="Summary by Brakeman" description="Brakeman sums up the results when all are done, on your Brakeman account." size="sm" data-compare-brakeman @update:model-value="patch({ brakeman: $event })" />
            </div>
          </MoreOptions>

          <p class="text-xs text-muted" data-compare-next>This saves the trial as a crew. To run it: on a ticket in Dispatch, propose this crew as the plan, approve it, then launch.</p>
          <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-circle-x" title="Not saved" :description="error" data-compare-template-error />
        </fieldset>
      </form>
    </template>
    <template #footer>
      <div class="flex w-full flex-wrap items-center gap-x-2 gap-y-3">
        <UTooltip text="Each agent runs commands and changes files without asking you.">
          <USwitch :model-value="yoloShown" :disabled="creating" data-compare-yolo @update:model-value="patch({ yolo: $event })">
            <template #label>Yolo <span class="font-normal text-muted">skips permission prompts</span></template>
          </USwitch>
        </UTooltip>
        <div class="flex-1" />
        <UButton label="Cancel" color="neutral" variant="ghost" :disabled="creating" data-compare-cancel @click="cancel" />
        <UButton label="Save trial" icon="i-lucide-save" :loading="creating" data-compare-create @click="press" />
      </div>
    </template>
  </UModal>
</template>
