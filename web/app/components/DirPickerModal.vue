<script setup lang="ts">
import type { PathEntry } from '~/composables/useSessions'
import { crumbs, parentDir, underRoots } from '~/utils/dirPicker'
import { DIR_DEBOUNCE_MS, dirQuery, gitMark, listingProblem, moreNote } from '~/utils/dirInput'
import { ApiError } from '~/composables/useApi'

/**
 * A folder picker that browses the server's own folders (GET /api/paths):
 * what the server lists is what it can use, so on Windows it is the WSL
 * distribution's folders and never a Windows path. The path on top is
 * editable (Enter lists it); the list below enters a folder on a click or
 * Enter, Backspace goes up; "Use this folder" picks the folder shown. A folder
 * that does not exist yet is typed in the field the picker fills.
 */
const props = defineProps<{
  /** Where the picker opens; when it does not exist the listing lands on the longest part that does. */
  start: string
  title: string
  /** Browse the whole machine (scope any): the settings page; off, the allowed roots alone. */
  anywhere?: boolean
  /** The form's roots, for the note when the folder is on the wrong side of them. */
  roots?: string[]
  expect?: 'under-roots' | 'outside-roots'
}>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ pick: [path: string] }>()

const api = useSessions()
const serverHost = useServerHost()
serverHost.load()

const typed = ref(props.start)
const dir = ref('')
const entries = ref<PathEntry[]>([])
const truncated = ref(false)
const more = ref<{ more?: number; moreUnknown?: boolean }>({})
const moreWords = computed(() => moreNote({ truncated: truncated.value, ...more.value }))
const problem = ref('')
const loading = ref(false)
let seq = 0
let timer: number | undefined

async function list(path: string) {
  const n = ++seq
  loading.value = true
  try {
    // Empty asks for the server's default working directory: inside the roots the listing starts there.
    const prefix = path === '' ? '' : dirQuery(path.endsWith('/') ? path : `${path}/`)
    const r = await api.listPaths(prefix, 50, props.anywhere ? 'any' : 'roots')
    if (n !== seq) return
    dir.value = r.dir
    entries.value = r.entries
    truncated.value = r.truncated
    more.value = { more: r.more, moreUnknown: r.moreUnknown }
    problem.value = ''
    typed.value = r.dir
  } catch (e) {
    if (n === seq) problem.value = listingProblem(e instanceof ApiError ? e.code : '', (e as Error).message)
  } finally {
    if (n === seq) loading.value = false
  }
}
function go(path: string) {
  window.clearTimeout(timer)
  list(path)
}
watch(
  open,
  (o) => {
    // Inside the roots an empty start lists the server's default directory; the whole machine opens at home.
    if (o) go(props.start || (props.anywhere ? serverHost.home.value : '') || '')
  },
  { immediate: true },
)
onBeforeUnmount(() => window.clearTimeout(timer))
function onTyped() {
  window.clearTimeout(timer)
  timer = window.setTimeout(() => list(typed.value), DIR_DEBOUNCE_MS * 3)
}

const note = computed(() => {
  if (!props.expect || !dir.value || !props.roots) return ''
  const under = underRoots(dir.value, props.roots)
  if (props.expect === 'under-roots' && !under) return 'Outside the allowed roots: a launch here would be refused.'
  if (props.expect === 'outside-roots' && under) return 'Inside an allowed root: keep the data directory outside them.'
  return ''
})

function use() {
  if (!dir.value) return
  emit('pick', dir.value)
  open.value = false
}

const highlighted = ref(0)
watch(entries, () => (highlighted.value = 0))
function onListKey(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') highlighted.value = Math.min(entries.value.length - 1, highlighted.value + 1)
  else if (e.key === 'ArrowUp') highlighted.value = Math.max(0, highlighted.value - 1)
  else if (e.key === 'Enter' && entries.value[highlighted.value]) go(entries.value[highlighted.value]!.path)
  else if (e.key === 'Backspace') go(parentDir(dir.value))
  else return
  e.preventDefault()
}
</script>

<template>
  <UModal v-model:open="open" :title="title" description="The server's own folders: what it lists is what it can use." :ui="{ content: 'sm:max-w-xl' }" data-dir-picker>
    <template #body>
      <div class="flex flex-col gap-3">
        <div class="flex items-center gap-1.5">
          <IconButton icon="i-lucide-arrow-up" color="neutral" variant="outline" size="sm" label="Up one folder" :disabled="!dir || dir === '/'" data-dir-picker-up @click="go(parentDir(dir))" />
          <IconButton v-if="anywhere && serverHost.home.value" icon="i-lucide-house" color="neutral" variant="outline" size="sm" label="Home" data-dir-picker-home @click="go(serverHost.home.value)" />
          <UInput v-model="typed" class="flex-1" size="sm" :ui="{ base: 'font-mono text-xs' }" aria-label="Folder" data-dir-picker-path @update:model-value="onTyped" @keydown.enter.prevent="go(typed)" />
        </div>
        <nav class="flex flex-wrap items-center gap-0.5 font-mono text-xs" aria-label="Folders above">
          <template v-for="(c, i) in crumbs(dir)" :key="c.path">
            <span v-if="i > 1" class="text-dimmed">/</span>
            <UButton :label="c.name" size="xs" color="neutral" variant="link" class="px-0.5" :data-dir-picker-crumb="c.path" @click="go(c.path)" />
          </template>
        </nav>
        <UAlert v-if="problem" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="problem" />
        <ul class="flex h-72 flex-col overflow-y-auto rounded-md ring ring-default outline-none" role="listbox" tabindex="0" :aria-label="`Folders in ${dir}`" data-dir-picker-list @keydown="onListKey">
          <li
            v-for="(e, i) in entries"
            :key="e.path"
            role="option"
            :aria-selected="i === highlighted"
            class="flex cursor-pointer items-center gap-2 px-3 py-1.5 text-sm"
            :class="i === highlighted ? 'bg-elevated' : 'hover:bg-elevated/60'"
            :data-dir-picker-entry="e.name"
            @mouseenter="highlighted = i"
            @click="go(e.path)"
          >
            <UIcon name="i-lucide-folder" class="size-4 flex-none text-muted" />
            <span class="truncate font-mono text-[13px]">{{ e.name }}</span>
            <UBadge v-if="gitMark(e.git).label" :label="gitMark(e.git).label" :color="gitMark(e.git).tone" variant="subtle" size="sm" class="ml-auto flex-none" />
          </li>
          <li v-if="!entries.length && !loading" class="px-3 py-3 text-sm text-muted">No folders here. Type a path above, or type one that does not exist yet in the field itself.</li>
          <li v-if="moreWords" class="flex items-center gap-1.5 px-3 py-1.5 text-xs text-toned" data-dir-more><UIcon name="i-lucide-list-filter" class="size-3.5 flex-none text-muted" />{{ moreWords }}</li>
        </ul>
        <p v-if="note" class="text-xs text-warning" data-dir-picker-note>{{ note }}</p>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full items-center gap-2">
        <span class="min-w-0 truncate font-mono text-xs text-muted">{{ dir }}</span>
        <UButton label="Cancel" color="neutral" variant="ghost" class="ml-auto" @click="open = false" />
        <UButton label="Use this folder" icon="i-lucide-check" :disabled="!dir || loading" data-dir-picker-use @click="use" />
      </div>
    </template>
  </UModal>
</template>
