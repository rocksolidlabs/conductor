<script setup lang="ts">
import type { PathEntry } from '~/composables/useSessions'
import { caretToEnd, DIR_DEBOUNCE_MS, dirQuery, enterKeepsText, gitMark, matchingEntries, moreNote, movesHighlight } from '~/utils/dirInput'

/**
 * A working-directory field completed from the server: as the text changes,
 * GET /api/paths lists the child directories of the longest existing
 * directory in it (under the allowed roots), each marked when it is a git
 * repository with a commit. Nuxt UI's UInputMenu in autocomplete mode is the
 * combobox: the text is the model and stays editable, the server's entries
 * are its items (its own filter is off: the server filters by what is
 * typed), the arrow keys move, Enter or a click picks (the entry's path
 * replaces the text and the list opens again on its children), Esc closes.
 * The list highlights its first entry after every keystroke, so Enter picks
 * only once the highlight was moved since the text last changed (the arrows,
 * or the pointer over an entry): Enter on a complete path keeps it, and the
 * list stays as it is. With the list closed, Enter belongs to the form
 * around. Until the listing of what is typed arrives, only the entries under
 * it stay, so Enter never puts an entry the text has moved past in its place.
 * Tab moves on and closes the list. The model is the text, whatever is
 * picked.
 */
const model = defineModel<string>({ default: '' })
const props = defineProps<{
  placeholder?: string
  name?: string
  /** The folder picker's title; the button beside the field opens it. */
  pickerTitle?: string
  /** A folder that is not a working folder (an account's login folder): the list and the picker browse the whole machine, as Settings' picker does. */
  anywhere?: boolean
}>()

// The folder button: the picker browses the allowed roots (or the whole machine, `anywhere`), opening on the text or the server's default directory.
const pickerOpen = ref(false)
const pickerStart = ref('')
function openPicker() {
  pickerStart.value = model.value.trim()
  pickerOpen.value = true
}
// The folder the picker chose becomes the text, as a pick from the list would: the field gets the focus back with the caret at
// the end (the modal's close would return it to the folder button), and the list opens on the folder's children once listed.
function onPicked(path: string) {
  picked = true
  model.value = path
  nextTick(() => {
    const input = menu.value?.inputRef
    if (input) caretToEnd(input)
  })
}

const api = useSessions()
const admin = useWorkbenchToken()

const root = useTemplateRef<HTMLElement>('root')
const menu = useTemplateRef<{ inputRef?: HTMLInputElement; viewportRef?: HTMLElement }>('menu')
const open = ref(false)
const entries = ref<PathEntry[]>([])
const truncated = ref(false)
const more = ref<{ more?: number; moreUnknown?: boolean }>({})
const note = computed(() => moreNote({ truncated: truncated.value, ...more.value }))
const loading = ref(false)
// A listing is due after the pause.
const pending = ref(false)
const problem = ref('')
let timer: number | undefined
// Replies may come back out of order, or after the text changed again: only the reply to the latest text counts.
let seq = 0
// The text (trimmed) the entries shown answer; not set by a failed listing, which a refocus retries.
let listedFor: string | undefined
// The text became an entry's path in the field: picked (Enter, or a click, which takes the focus away), or typed in full.
// Only then does a reply open the list again; Esc or leaving the field forgets it.
let picked = false
// The highlight was moved (the arrows, or the pointer over an entry) since the text last changed: only then does Enter pick.
let moved = false

async function fetchNow() {
  pending.value = false
  if (!admin.hasToken.value) {
    // A token lost while a listing was on its way: its reply no longer counts, so nothing is looked for.
    loading.value = false
    return
  }
  const n = ++seq
  const query = dirQuery(model.value)
  loading.value = true
  try {
    const r = await api.listPaths(query, 50, props.anywhere ? 'any' : 'roots')
    if (n !== seq) return
    entries.value = r.entries
    truncated.value = r.truncated
    more.value = { more: r.more, moreUnknown: r.moreUnknown }
    problem.value = ''
    listedFor = query
    // The listing changed under the highlight: an arrow pressed while it was on its way no longer counts.
    moved = false
  } catch (e) {
    if (n !== seq) return
    listedFor = undefined
    entries.value = []
    truncated.value = false
    problem.value = (e as Error).message
  } finally {
    if (n === seq) {
      // A pick closes the list: it opens again on the picked directory's children, the focus back in the field
      // (while loading still holds, so that focus asks nothing). Typing opens it by itself; a reply after Esc leaves it closed.
      const input = menu.value?.inputRef
      if (input && picked) {
        picked = false
        caretToEnd(input)
        open.value = true
      }
      loading.value = false
    }
  }
}

// Lists the text after a pause; a reply to an earlier text, still on its way, no longer counts.
function schedule() {
  seq++
  pending.value = true
  window.clearTimeout(timer)
  timer = window.setTimeout(fetchNow, DIR_DEBOUNCE_MS)
}

// Typing and picking in the field change the text; a change from the form around (a crew discarded) is no pick.
// A pick of the text as it stands changes nothing: its children are listed all the same.
function onText(text: string) {
  moved = false
  if (entries.value.some((e) => e.path === text)) {
    // Picked: the field's view goes to the end of the path as the pick lands in it (#167), and again once its listing is back.
    picked = true
    nextTick(() => {
      const input = menu.value?.inputRef
      if (input) caretToEnd(input)
    })
  }
  if (text === model.value) schedule()
  model.value = text
}

// Any change of the text: the entries it has moved past go at once, and it is listed again after a pause.
watch(model, (text) => {
  moved = false
  entries.value = matchingEntries(entries.value, text)
  schedule()
})

function onKeydown(e: KeyboardEvent) {
  if (movesHighlight(e.key)) moved = true
}

// The list highlights the entry under a moving pointer, as the arrows do.
function onPointerMove(e: PointerEvent) {
  if ((e.target as Element | null)?.closest?.('[role="option"]')) moved = true
}
watch(
  () => menu.value?.viewportRef,
  (el, old) => {
    old?.removeEventListener('pointermove', onPointerMove)
    el?.addEventListener('pointermove', onPointerMove)
  },
)

// Before the list's own Enter (capture, on the way down to the field): with the highlight where the list put it,
// Enter keeps the text and the list as they are, and does not reach the form around either.
function onEnter(e: KeyboardEvent) {
  const highlighted = !!menu.value?.viewportRef?.querySelector('[role="option"][data-highlighted]')
  if (!enterKeepsText(e, { open: open.value, highlighted, moved })) return
  e.preventDefault()
  e.stopPropagation()
}

// The field taking the focus (focusin: not the focus UInputMenu reports when a keystroke opens the
// list, before the text changes, so that keystroke sends one request, not two): it lists a text
// neither listed nor due, and retries one whose listing failed.
function onFocus() {
  if (!pending.value && !loading.value && listedFor !== dirQuery(model.value)) fetchNow()
}

// UInputMenu keeps its list open when the focus leaves the field (its content
// prevents focus-outside), so Tab would leave it over the fields below: the
// focus going anywhere but the field or the list closes it. A click on an
// option focuses the option, inside the list, and picks as before.
function onBlur(e: FocusEvent) {
  const to = e.relatedTarget as Node | null
  if (to && !root.value?.contains(to) && !menu.value?.viewportRef?.parentElement?.contains(to)) {
    picked = false
    open.value = false
  }
}

function onEscape() {
  picked = false
}

onBeforeUnmount(() => {
  window.clearTimeout(timer)
  menu.value?.viewportRef?.removeEventListener('pointermove', onPointerMove)
})
</script>

<template>
  <div class="flex w-full min-w-0 items-start gap-2">
  <div ref="root" class="min-w-0 flex-1" data-dir-input @keydown.esc="onEscape" @keydown="onKeydown" @keydown.enter.capture="onEnter" @focusin="onFocus">
    <UInputMenu
      ref="menu"
      :model-value="model"
      v-model:open="open"
      mode="autocomplete"
      :items="entries"
      value-key="path"
      label-key="name"
      ignore-filter
      open-on-focus
      :loading="loading"
      :placeholder="placeholder"
      :name="name"
      autocapitalize="off"
      autocomplete="off"
      spellcheck="false"
      :ui="{ base: 'font-mono' }"
      class="w-full"
      @update:model-value="onText"
      @blur="onBlur"
    >
      <template #item-leading>
        <UIcon name="i-lucide-folder" class="size-4 flex-none text-muted" />
      </template>
      <template #item-label="{ item }">
        <span class="font-mono text-xs">{{ item.name }}</span>
      </template>
      <template #item-trailing="{ item }">
        <UBadge v-if="gitMark(item.git).label" :label="gitMark(item.git).label" :color="gitMark(item.git).tone" variant="subtle" size="sm" />
      </template>
      <template #empty>
        <span aria-live="polite" :class="{ 'text-warning': problem && !pending && !loading }">{{ pending || loading ? 'Looking…' : problem || 'No directory here.' }}</span>
      </template>
      <template #content-bottom>
        <div aria-live="polite">
          <p v-if="note" class="flex items-center gap-1.5 border-t border-default px-2 py-1.5 text-xs text-toned" data-dir-more><UIcon name="i-lucide-list-filter" class="size-3.5 flex-none text-muted" />{{ note }}</p>
        </div>
      </template>
    </UInputMenu>
  </div>
  <UButton icon="i-lucide-folder" color="neutral" variant="outline" :aria-label="props.pickerTitle || 'Choose a folder'" data-dir-input-pick @click="openPicker" />
  <DirPickerModal v-if="pickerOpen" v-model:open="pickerOpen" :start="pickerStart" :title="props.pickerTitle || 'Choose a folder'" :anywhere="props.anywhere" @pick="onPicked" />
  </div>
</template>
