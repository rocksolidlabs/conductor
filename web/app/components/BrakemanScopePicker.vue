<script setup lang="ts">
import type { PickerItem } from '~/utils/brakemanSettings'
import { pickerItems } from '~/utils/brakemanSettings'

/**
 * One selector of Brakeman's scope in Settings (#993): picked from what the app lists (Dispatch's projects, the saved crews, the live
 * runs and sessions), or typed, since a scope may name what is not listed now. An id saved but not listed stays as a chip, so it can
 * be seen and taken off. The model is the ids; the form checks each as the server does.
 */
const props = defineProps<{ modelValue: string[]; listed: readonly PickerItem[]; label: string; placeholder: string; scopeKey: string }>()
const emit = defineEmits<{ 'update:modelValue': [ids: string[]] }>()

const items = computed(() => pickerItems(props.listed, props.modelValue))
const byId = computed(() => new Map(items.value.map((i) => [i.value, i])))

function pick(v: unknown) {
  emit('update:modelValue', Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])
}
function create(typed: string) {
  const v = typed.trim()
  if (v && !props.modelValue.includes(v)) emit('update:modelValue', [...props.modelValue, v])
}
</script>

<template>
  <USelectMenu
    :model-value="modelValue"
    :items="items"
    value-key="value"
    multiple
    create-item="always"
    :search-input="{ placeholder }"
    :aria-label="label"
    class="w-full"
    :data-brakeman-scope="scopeKey"
    @update:model-value="pick"
    @create="create"
  >
    <template #default>
      <span v-if="modelValue.length" class="flex min-w-0 flex-wrap items-center gap-1" data-scope-chips>
        <span
          v-for="id in modelValue"
          :key="id"
          class="min-w-0 rounded-full px-2 py-0.5 text-xs ring ring-default [overflow-wrap:anywhere]"
          :class="byId.get(id)?.unlisted ? 'text-muted' : ''"
          :data-scope-chip="id"
          :data-scope-unlisted="byId.get(id)?.unlisted ? '' : undefined"
        >{{ byId.get(id)?.label ?? id }}</span>
      </span>
      <span v-else class="text-dimmed">All of them</span>
    </template>
    <template #create-item-label="{ item }">Add {{ item }}</template>
    <template #empty>Nothing listed: type an id to add it.</template>
  </USelectMenu>
</template>
