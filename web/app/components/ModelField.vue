<script setup lang="ts">
/**
 * A model's name, typed, with a list of the names the agent's own help gives (GET /api/catalog/{id}/flags `models`) when it gives
 * any: the list opens on the focus and narrows to the names that hold what is typed; Enter, Tab or a click takes one; Escape closes
 * the list alone. A name not listed is taken as typed. With no names (an agent whose help names none) it is the plain field it
 * replaces. Its attributes (a data mark, the placeholder) go to the input either way.
 */
defineOptions({ inheritAttrs: false })
const model = defineModel<string>({ default: '' })
const props = defineProps<{
  agent?: { id: string; name: string; version?: string } | null
  placeholder?: string
  size?: 'xs' | 'sm' | 'md' | 'lg' | 'xl'
  /** More classes for the input itself (a table's smaller text and touch height). */
  baseClass?: string
}>()
const base = computed(() => ['font-mono', props.baseClass].filter(Boolean).join(' '))

const { reply } = useAgentFlags(() => (props.agent ? { id: props.agent.id, version: props.agent.version } : null))
const names = computed(() => (reply.value?.state === 'read' ? reply.value.models : []))

const root = useTemplateRef<HTMLElement>('root')
const menu = useTemplateRef<{ inputRef?: HTMLInputElement; viewportRef?: HTMLElement }>('menu')
const focused = ref(false)
const dismissed = ref(false)
const items = computed(() => {
  const t = model.value.trim().toLowerCase()
  const starts = names.value.filter((n) => n.toLowerCase().startsWith(t))
  const holds = names.value.filter((n) => !n.toLowerCase().startsWith(t) && n.toLowerCase().includes(t))
  return [...starts, ...holds]
})
// Closed when nothing holds what is typed, or the only name is the one typed.
const open = computed(() => focused.value && !dismissed.value && items.value.length > 0 && !(items.value.length === 1 && items.value[0] === model.value.trim()))

function onText(text: string) {
  const picked = names.value.includes(text) && text !== model.value
  model.value = text
  dismissed.value = picked
}

function onOpen(v: boolean) {
  if (!v) dismissed.value = true
}

function onKeydown(e: KeyboardEvent) {
  if (e.isComposing) return
  if (e.key === 'Tab' && !e.shiftKey && open.value) {
    const name = menu.value?.viewportRef?.querySelector<HTMLElement>('[role="option"][data-highlighted] [data-model-name]')?.dataset.modelName
    if (name !== undefined) {
      e.preventDefault()
      onText(name)
    }
    return
  }
  if (e.key !== 'Escape' && e.key !== 'Enter' && e.key !== 'Tab') dismissed.value = false
}

function onFocus() {
  if (!focused.value) {
    focused.value = true
    dismissed.value = false
  }
}

function onBlur(e: FocusEvent) {
  const to = e.relatedTarget as Node | null
  if (to && (root.value?.contains(to) || menu.value?.viewportRef?.parentElement?.contains(to))) return
  focused.value = false
}
</script>

<template>
  <UInput v-if="!names.length" v-bind="$attrs" v-model="model" :placeholder="placeholder" :size="size" class="w-full" :ui="{ base }" />
  <div v-else ref="root" class="w-full min-w-0" data-model-field @focusin="onFocus" @keydown.capture="onKeydown">
    <UInputMenu
      ref="menu"
      v-bind="$attrs"
      :model-value="model"
      :open="open"
      mode="autocomplete"
      :items="items"
      ignore-filter
      :placeholder="placeholder"
      :size="size"
      :trailing-icon="false"
      autocapitalize="off"
      autocomplete="off"
      spellcheck="false"
      :ui="{ base }"
      class="w-full"
      @update:model-value="onText"
      @update:open="onOpen"
      @blur="onBlur"
    >
      <template #item-label="{ item }">
        <code class="text-xs" :data-model-name="item">{{ item }}</code>
      </template>
    </UInputMenu>
  </div>
</template>
