<script setup lang="ts">
import type { DispatchLabel } from '~/composables/useDispatch'
import { labelDot } from '~/utils/dispatch'

/**
 * A ticket's labels (docs/plans/dispatch.md, "web/app"): the project's live labels, each with its colour's dot, ticked and unticked; the
 * model is the labels' ids, and the page sends what was added and what was removed as one operation. The labels are made, renamed,
 * recoloured and deleted on the project's page.
 */
const props = defineProps<{ modelValue: string[]; labels: readonly DispatchLabel[]; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [ids: string[]] }>()

/** A ticket's labels' bound (the store's MaxTicketLabels). */
const MAX_TICKET_LABELS = 20

const byId = computed(() => new Map(props.labels.map((l) => [l.id, l])))
const items = computed(() => [...props.labels].sort((a, b) => a.name.localeCompare(b.name)).map((l) => ({ label: l.name, value: l.id, color: l.color })))

function pick(v: unknown) {
  emit('update:modelValue', Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])
}
</script>

<template>
  <USelectMenu :model-value="modelValue" :items="items" value-key="value" multiple :disabled="disabled" :search-input="{ placeholder: 'A label…' }" size="sm" class="w-full" data-label-picker @update:model-value="pick">
    <template #default>
      <span v-if="modelValue.length" class="flex min-w-0 flex-wrap items-center gap-1" data-label-chips>
        <span v-for="id in modelValue" :key="id" class="flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs ring ring-default" :data-label="byId.get(id)?.name ?? id">
          <span class="size-2 rounded-full" :class="labelDot(byId.get(id)?.color ?? 'gray')" />{{ byId.get(id)?.name ?? 'a label' }}
        </span>
      </span>
      <span v-else class="text-dimmed">None</span>
    </template>
    <template #item-leading="{ item }">
      <span class="size-2.5 flex-none rounded-full" :class="labelDot(item.color)" />
    </template>
    <template #empty>{{ labels.length ? 'No label matches.' : 'The project has no labels yet: make them on its page.' }}</template>
    <template #content-bottom>
      <p v-if="modelValue.length >= MAX_TICKET_LABELS" class="px-2 py-1.5 text-xs text-muted">A ticket has at most {{ MAX_TICKET_LABELS }} labels.</p>
    </template>
  </USelectMenu>
</template>
