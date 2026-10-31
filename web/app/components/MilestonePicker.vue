<script setup lang="ts">
import type { DispatchMilestone } from '~/composables/useDispatch'

/**
 * A ticket's milestone (docs/plans/dispatch.md, "web/app"): one of the project's live milestones, the open ones first by their target,
 * the closed ones after them, or none (the clear button). A milestone deleted reads as none. The milestones are made, edited, closed and
 * deleted on the project's page.
 */
const props = defineProps<{ modelValue: string; milestones: readonly DispatchMilestone[]; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [id: string] }>()

const items = computed(() => {
  const order = (m: DispatchMilestone) => (m.state === 'closed' ? 2 : m.target ? 0 : 1)
  return [...props.milestones]
    .sort((a, b) => order(a) - order(b) || a.target.localeCompare(b.target) || a.name.localeCompare(b.name))
    .map((m) => ({ label: m.name, value: m.id, description: [m.target, m.state === 'closed' ? 'closed' : ''].filter(Boolean).join(' · ') || undefined, icon: m.state === 'closed' ? 'i-lucide-milestone' : 'i-lucide-flag' }))
})
/** The milestone shown: none for one the project does not hold (deleted, or a merge's). */
const shown = computed(() => (props.milestones.some((m) => m.id === props.modelValue) ? props.modelValue : undefined))

function pick(v: unknown) {
  emit('update:modelValue', typeof v === 'string' ? v : '')
}
</script>

<template>
  <USelectMenu :model-value="shown" :items="items" value-key="value" clear placeholder="None" :disabled="disabled" :search-input="{ placeholder: 'A milestone…' }" size="sm" class="w-full" data-milestone-picker @update:model-value="pick">
    <template #empty>{{ milestones.length ? 'No milestone matches.' : 'The project has no milestones yet: make them on its page.' }}</template>
  </USelectMenu>
</template>
