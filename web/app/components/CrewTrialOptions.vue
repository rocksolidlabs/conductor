<script setup lang="ts">
import { COMPARE_LIMITS, agentLetter, rowKey, type DraftAgent, type DraftComparison, type TrialAgent, type TrialField } from '~/utils/compareTemplate'

/**
 * What New agent trial keeps under More options, in five groups, the most used first: each agent (an approach to try, its arguments),
 * every agent (what each is told, the lines a good result meets), checks (more commands, the project's own checks), when they are done
 * (a review, Brakeman's summary) and what it is filed as (its name). Each field shows the words its owner hands in (`marked`), which the
 * form gives only after its first press.
 */
const draft = defineModel<DraftComparison>({ required: true })
const props = defineProps<{
  agents: readonly TrialAgent[]
  /** A field's words, once the form's button was pressed; undefined when it has none. */
  marked: (...fields: TrialField[]) => string | undefined
}>()

const installed = computed(() => props.agents.filter((a) => a.id !== 'shell' && a.available !== false))
const agentItems = computed(() => installed.value.map((a) => ({ label: a.name, value: a.id })))
const takesArgs = (id: string) => props.agents.find((a) => a.id === id)?.allowArgs !== false
const nameOf = (id: string) => props.agents.find((a) => a.id === id)?.name ?? id
// The agent a row's Arguments offers the flags of (the form has no Model field: its model flag and names are offered too).
const flagsAgent = (id: string) => (id ? { id, name: nameOf(id) } : null)

function patch(p: Partial<DraftComparison>) {
  draft.value = { ...draft.value, ...p }
}
function setCriterion(i: number, text: string) {
  patch({ criteria: draft.value.criteria.map((c, j) => (j === i ? { ...c, text } : c)) })
}
function setAgent(i: number, p: Partial<DraftAgent>) {
  patch({ agents: draft.value.agents.map((a, j) => (j === i ? { ...a, ...p } : a)) })
}
function setReviews(on: boolean) {
  patch({ reviews: on, reviewAgent: on ? draft.value.reviewAgent || (agentItems.value[0]?.value ?? '') : '' })
}
</script>

<template>
  <div class="flex min-w-0 flex-col gap-6" data-trial-options>
    <section class="flex min-w-0 flex-col gap-2" data-trial-group="each">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Each agent</h3>
      <div class="hidden gap-2 text-xs text-muted sm:grid sm:grid-cols-[minmax(0,8rem)_minmax(0,1fr)_minmax(0,1fr)]" aria-hidden="true">
        <span />
        <span>Approach</span>
        <span>Arguments</span>
      </div>
      <div v-for="(a, i) in draft.agents" :key="a.key" class="grid min-w-0 items-start gap-2 sm:grid-cols-[minmax(0,8rem)_minmax(0,1fr)_minmax(0,1fr)]" :data-compare-agent-row="i">
        <span class="flex min-h-8 min-w-0 items-center gap-2"><b class="font-semibold text-highlighted">{{ agentLetter(i) }}</b><span class="min-w-0 [overflow-wrap:anywhere]">{{ nameOf(a.agentId) }}</span></span>
        <UFormField :error="marked(`approach-${i}`)">
          <UInput :model-value="a.approach" :maxlength="COMPARE_LIMITS.approach" placeholder="Optional: a way to try it" :aria-label="`Approach of agent ${agentLetter(i)}`" class="w-full" :data-compare-agent-approach="i" @update:model-value="setAgent(i, { approach: String($event ?? '') })" />
        </UFormField>
        <UFormField :error="marked(`args-${i}`)">
          <AgentArgsField v-if="takesArgs(a.agentId)" :model-value="a.args" :agent="flagsAgent(a.agentId)" plain-placeholder="Flags, as in a terminal" base-class="placeholder:font-sans" :aria-label="`Arguments of agent ${agentLetter(i)}`" class="w-full" :data-compare-agent-args="i" @update:model-value="setAgent(i, { args: String($event ?? '') })" />
          <span v-else class="flex min-h-8 items-center text-xs text-muted" :data-compare-agent-no-args="i">Takes no arguments.</span>
        </UFormField>
      </div>
    </section>

    <section class="flex min-w-0 flex-col gap-3" data-trial-group="every">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Every agent</h3>
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
    </section>

    <section class="flex min-w-0 flex-col gap-3" data-trial-group="checks">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Checks</h3>
      <UFormField label="More checks" name="checks" :error="marked('checks')">
        <div data-compare-checks>
          <CrewCheckList :model-value="draft.checks" what="check" add-label="Add a check" :taken="draft.check.trim() ? ['check'] : []" @update:model-value="patch({ checks: $event })" />
        </div>
      </UFormField>
      <UCheckbox :model-value="draft.manifestFile" label="Also run the project's own checks (conductor.manifest.json)" data-compare-manifest-file @update:model-value="patch({ manifestFile: !!$event })" />
    </section>

    <section class="flex min-w-0 flex-col gap-3" data-trial-group="done">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">When they are done</h3>
      <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
        <USwitch :model-value="draft.reviews" label="Review" data-compare-reviews @update:model-value="setReviews" />
        <UFormField v-if="draft.reviews" :error="marked('reviews')" class="min-w-0">
          <div class="flex items-center gap-2">
            <span class="text-sm text-muted">by</span>
            <USelect :model-value="draft.reviewAgent" :items="agentItems" aria-label="The agent that reviews" class="w-56 max-w-full" data-compare-review-agent @update:model-value="patch({ reviewAgent: String($event ?? '') })" />
          </div>
        </UFormField>
      </div>
      <USwitch :model-value="draft.brakeman" label="Summary by Brakeman" data-compare-brakeman @update:model-value="patch({ brakeman: $event })" />
    </section>

    <section class="flex min-w-0 flex-col gap-3" data-trial-group="filed">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Filed as</h3>
      <UFormField label="Name" name="name" :error="marked('name')" class="sm:w-1/2">
        <UInput :model-value="draft.name" :maxlength="COMPARE_LIMITS.name" placeholder="The task's first words" class="w-full" data-compare-name @update:model-value="patch({ name: String($event ?? '') })" />
      </UFormField>
    </section>
  </div>
</template>
