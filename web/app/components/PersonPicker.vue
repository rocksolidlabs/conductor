<script setup lang="ts">
import { DispatchOrder, through } from '~/utils/dispatchOrder'
import { avatarTone } from '~/utils/avatar'
import { initials } from '~/utils/sessions'

/**
 * A ticket's owner, the accountable person (decision D4: people are names until identity exists): one of the directory's names, a new
 * name typed (the store adds it to the directory as the owner is set), or none (the clear button). The directory is read again whole
 * each time the picker opens, so a name another browser added is offered.
 *
 * Given `names` (a crew's person, HM1: the names Conductor knows, usePeople), it offers those instead and reads no directory; it says
 * `open` as it opens, so its owner can read them again. `create` words the row that takes a typed name.
 */
const props = withDefaults(defineProps<{ modelValue: string; disabled?: boolean; placeholder?: string; names?: readonly string[]; create?: string; size?: 'sm' | 'md' }>(), {
  placeholder: undefined,
  names: undefined,
  create: undefined,
  size: 'sm',
})
const emit = defineEmits<{ 'update:modelValue': [name: string]; open: [] }>()

const api = useDispatchApi()
const people = ref<string[]>([])
const open = ref(false)
watch(open, (o) => {
  if (!o) return
  if (props.names) emit('open')
  else void load()
})

// The directory read whole, through a gate of its own (utils/dispatchOrder.ts): a slow reply never replaces a newer one.
const gate = new DispatchOrder()
async function load() {
  try {
    await through(gate, 'people', () => api.people(), (list) => (people.value = list.map((p) => p.name)))
  } catch {
    // The names read before stay offered; a typed name still sets.
  }
}
onMounted(() => {
  if (!props.names) void load()
})

const items = computed(() => [...new Set([...(props.names ?? people.value), ...(props.modelValue ? [props.modelValue] : [])])].sort((a, b) => a.localeCompare(b)))

/**
 * A pick, said whatever the picker shows: what it shows is the owner as the store last answered, while an edit setting another may still
 * be on its way, so the name shown picked again takes that edit back. The page's edit, made when it is sent, changes nothing when the
 * ticket holds the name then.
 */
function pick(v: unknown) {
  const name = typeof v === 'string' ? v.trim() : ''
  open.value = false
  emit('update:modelValue', name)
}
</script>

<template>
  <USelectMenu
    v-model:open="open"
    :model-value="modelValue || undefined"
    :items="items"
    create-item="always"
    clear
    :placeholder="placeholder ?? 'No owner'"
    :disabled="disabled"
    :search-input="{ placeholder: 'A name…' }"
    :size="size"
    class="w-full"
    data-person-picker
    @update:model-value="pick"
    @create="pick"
  >
    <template #leading>
      <span v-if="modelValue" class="grid size-5 place-items-center rounded-full text-[9px] font-semibold" :class="avatarTone(modelValue)">{{ initials(modelValue) }}</span>
      <UIcon v-else name="i-lucide-user" class="size-4 text-dimmed" />
    </template>
    <template #create-item-label="{ item }">{{ (create ?? 'Make {name} the owner').replace('{name}', item) }}</template>
  </USelectMenu>
</template>
