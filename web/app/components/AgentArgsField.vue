<script setup lang="ts">
import { argRows, holdsYolo, listShows, typedAt, type ArgRow } from '~/utils/argComplete'

/**
 * An agent's arguments as one line, which offers the agent's own flags as the person types (GET /api/catalog/{id}/flags, read
 * from the help of the version installed) and, after a flag that takes a value, the values its help lists. Nuxt UI's UInputMenu
 * in autocomplete mode, as DirInput is: the text is the model and stays editable, the rows are computed here (its own filter
 * off), and the open state is held here, so the list never opens on the focus alone. Each row's value is the whole line a pick
 * leaves (utils/argComplete): Enter, Tab or a click takes the highlighted row, and the cursor goes after it. With the list closed,
 * Enter is the form's and Tab moves on; Escape closes the list and not the dialog. Whatever is typed is sent as typed.
 */
defineOptions({ inheritAttrs: false })
const model = defineModel<string>({ default: '' })
const props = defineProps<{
  /** The agent the arguments are for; none, or one not run on this server, offers nothing. */
  agent?: { id: string; name: string; version?: string } | null
  /** The form has a Model field for this agent: the model flag is left to it. */
  modelField?: boolean
  size?: 'xs' | 'sm' | 'md' | 'lg' | 'xl'
  /** More classes for the input itself (a table's smaller text and touch height). */
  baseClass?: string
  /** The placeholder while the agent has no list of flags (a narrow field's shorter words). */
  plainPlaceholder?: string
}>()

const { flags, reading } = useAgentFlags(() => (props.agent ? { id: props.agent.id, version: props.agent.version } : null))

const root = useTemplateRef<HTMLElement>('root')
const menu = useTemplateRef<{ inputRef?: HTMLInputElement; viewportRef?: HTMLElement }>('menu')
const cursor = ref(model.value.length)
// The field has the focus, and a key or an edit came since it took it: the list never opens on the focus alone.
const focused = ref(false)
const engaged = ref(false)
// Closed by Escape, a pick or a click away, until the text or the cursor moves again.
const dismissed = ref(false)
// Arrow Down on an empty word lists every flag.
const browse = ref(false)
// The line a pick just left: the list's own close as it picks does not close it on that flag's values.
let picked: string | undefined

const rows = computed<ArgRow[]>(() => argRows(model.value, cursor.value, flags.value, { modelField: props.modelField }, browse.value))
const typed = computed(() => typedAt(model.value, cursor.value, flags.value, browse.value))
const waiting = computed(() => reading.value && typed.value.startsWith('-'))
const open = computed(() => focused.value && engaged.value && !dismissed.value && (listShows(rows.value, typed.value) || waiting.value))
const items = computed(() => rows.value.map((r) => ({ ...r, value: r.text })))
const yoloTyped = computed(() => holdsYolo(model.value, flags.value))
const placeholder = computed(() => (flags.value.length ? 'Type -- to see its flags' : props.plainPlaceholder || 'Flags, as you would type them in a terminal'))

function input(): HTMLInputElement | undefined {
  return menu.value?.inputRef
}

function readCursor() {
  const el = input()
  if (el && el.selectionStart !== null) cursor.value = el.selectionStart
}

function placeCursor(at: number) {
  cursor.value = at
  nextTick(() => {
    const el = input()
    if (!el) return
    el.focus()
    el.setSelectionRange(at, at)
    // The focus given back is no new focus: the list goes on.
    focused.value = true
    engaged.value = true
    dismissed.value = false
  })
}

// Typing and picking both change the text: a pick is a row's whole line, and the cursor goes after what it put in.
function onText(text: string) {
  const row = rows.value.find((r) => r.text === text)
  model.value = text
  browse.value = false
  engaged.value = true
  dismissed.value = false
  if (row) {
    // The list closes as it picks; it stays open on the flag's values, when its help lists any.
    picked = text
    placeCursor(row.cursor)
    return
  }
  picked = undefined
  nextTick(readCursor)
}

function onOpen(v: boolean) {
  if (!v && picked !== model.value) dismissed.value = true
}

function highlightedRow(): ArgRow | undefined {
  const el = menu.value?.viewportRef?.querySelector<HTMLElement>('[role="option"][data-highlighted] [data-arg-row]')
  const key = el?.dataset.argRow
  return key === undefined ? undefined : rows.value.find((r) => r.key === key)
}

function onKeydown(e: KeyboardEvent) {
  if (e.isComposing) return
  if (e.key === 'Tab' && !e.shiftKey && open.value) {
    // Tab takes the highlighted row, as Enter does; with the list closed it moves on.
    const row = highlightedRow()
    if (row) {
      e.preventDefault()
      onText(row.text)
    }
    return
  }
  if (e.key === 'ArrowDown' && !open.value && typed.value === '') {
    browse.value = true
  }
  if (e.key === 'Escape') picked = undefined
  if (e.key !== 'Escape' && e.key !== 'Tab') {
    engaged.value = true
    if (e.key !== 'Enter') dismissed.value = false
  }
}

// A key that moves the cursor (the arrows along the line, Home, End) or a click in the text: the list follows the word now under it.
function onCursor() {
  readCursor()
}

function onFocus() {
  if (!focused.value) {
    focused.value = true
    engaged.value = false
    dismissed.value = false
  }
}

// The focus going anywhere but the field or its list closes the list (UInputMenu keeps it open otherwise; DirInput does the same).
function onBlur(e: FocusEvent) {
  const to = e.relatedTarget as Node | null
  if (to && (root.value?.contains(to) || menu.value?.viewportRef?.parentElement?.contains(to))) return
  focused.value = false
  browse.value = false
}
</script>

<template>
  <div ref="root" class="w-full min-w-0" data-args-field @focusin="onFocus" @keydown.capture="onKeydown" @keyup="onCursor" @click="onCursor">
    <UInputMenu
      ref="menu"
      v-bind="$attrs"
      :model-value="model"
      :open="open"
      mode="autocomplete"
      :items="items"
      value-key="value"
      label-key="label"
      ignore-filter
      :placeholder="placeholder"
      :size="size"
      :trailing-icon="false"
      autocapitalize="off"
      autocomplete="off"
      spellcheck="false"
      :ui="{ base: ['font-mono', baseClass].filter(Boolean).join(' '), content: 'min-w-80' }"
      class="w-full"
      @update:model-value="onText"
      @update:open="onOpen"
      @blur="onBlur"
    >
      <template #item-label="{ item }">
        <span class="flex min-w-0 flex-col" :data-arg-row="item.key">
          <code class="truncate text-xs">{{ item.label }}</code>
          <span v-if="item.about" class="truncate text-xs text-muted" :title="item.about">{{ item.about }}</span>
        </span>
      </template>
      <template #item-trailing="{ item }">
        <UTooltip v-if="item.terminalOnly" text="A session launched with this flag stays in its terminal.">
          <UBadge label="Terminal only" color="neutral" variant="outline" size="sm" data-arg-terminal />
        </UTooltip>
      </template>
      <template #empty>
        <span aria-live="polite" data-args-reading>Reading {{ agent?.name }}'s flags…</span>
      </template>
    </UInputMenu>
    <p v-if="yoloTyped" class="mt-1 text-xs text-toned" data-args-yolo>Use the Yolo switch for this.</p>
  </div>
</template>
