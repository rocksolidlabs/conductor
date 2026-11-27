<script setup lang="ts">
import type { AgentInfo, CrewInfo } from '~/composables/useSessions'
import {
  COMPARE_LIMITS,
  checkNote,
  compareProblems,
  compareSpec,
  draftOnOpen,
  inMore,
  optionsLine,
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

/**
 * The dialog's top edge, fixed once it has opened where the modal centred it: More options then opens downward, its button staying
 * under the pointer, and the dialog grows to the window's bottom before its body scrolls.
 */
function pinTop() {
  const content = formRef.value?.closest<HTMLElement>('[data-slot="content"]')
  if (!content) return
  const top = Math.round(content.getBoundingClientRect().top)
  content.style.top = `${top}px`
  content.style.setProperty('--tw-translate-y', '0px')
  content.style.maxHeight = `calc(100dvh - ${top}px - 1rem)`
}

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

function patch(p: Partial<DraftComparison>) {
  draft.value = { ...draft.value, ...p }
}

/** The element a field's problem puts the cursor in. */
function fieldTarget(field: TrialField): string {
  const n = field.replace(/^\D+-/, '')
  if (field === 'task') return '[data-compare-goal]'
  if (field === 'agents') return '[data-trial-agent], [data-trial-add-agent]'
  if (field.startsWith('agent-')) return `[data-trial-agent="${n}"]`
  if (field === 'check') return '[data-compare-check]'
  if (field === 'folder') return '[data-compare-cwd] input'
  if (field === 'name') return '[data-compare-name]'
  if (field === 'told') return '[data-compare-instructions]'
  if (field === 'criteria') return '[data-compare-criterion]'
  if (field === 'checks') return '[data-compare-checks] input'
  if (field === 'reviews') return '[data-compare-review-agent]'
  if (field.startsWith('args-')) return `[data-compare-agent-args="${n}"]`
  return `[data-compare-agent-approach="${n}"]`
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
    @after:enter="pinTop"
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
            <CrewTrialOptions :model-value="draft" :agents="agents" :marked="marked" @update:model-value="draft = $event" />
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
