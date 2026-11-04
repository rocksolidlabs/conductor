<script setup lang="ts">
import type { ChatQuote, FileRequester, FileWriter, NvimBridge } from '~/utils/protocol'
import { clampSplit, SPLIT_DEFAULT, toggleCollapse, toggleFold, type CloseScope, type Keymap, type TabsState } from '~/utils/editorTabs'
import type { EditorPaletteState } from '~/utils/editorRows'
import { clampSideSplit, editorWidth, fitsSide, layoutFor, MIN_TERMINAL_WIDTH, readLayout, readSplitFor, SIDE_SPLIT_DEFAULT, stepSplit, writeLayout, writeSplitFor, type EditorLayout } from '~/utils/editorLayout'

/**
 * The column a terminal shares with the editor area (design 4b, 4g): the
 * area above the terminal, or beside it on the left (#309), a bar you drag
 * or move with the arrow keys between them (the layout and each layout's
 * split kept per browser, the terminal keeping its minimum height stacked
 * and its minimum width side by side), the terminal in the default slot,
 * the reply bar in `bar`. Side by side needs a wide window: narrower, the
 * column stacks whatever is kept. On a phone the editor takes the column
 * and the terminal folds to a bar at the foot that says what the agent is
 * doing; a tap brings the terminal back (the editor folds to its strip).
 * Wider, the terminal folds the same way only when the person collapses it
 * (#432; the strip's button, the palette's row): stacked or side by side,
 * the bar at the column's foot, and the bar or the button again brings it
 * back with the layout and the split it had. Folded, the terminal stays
 * mounted and asks no resize (its pane has no height), and the reply bar
 * stays under the bar while a question waits.
 */
const emit = defineEmits<{ comment: [c: { quote: ChatQuote; text: string; toAgent: boolean }] }>()
const props = defineProps<{
  request: FileRequester
  /** The editor's Neovim, when the page offers it (design round 12, F8). */
  nvim?: NvimBridge
  /** Saves a file (design round 12, F6), and whether this connection may: Save and Ctrl+S show then. */
  write?: FileWriter
  canEdit?: boolean
  /** Comments on lines (F7): the page posts them to its chat; `canAsk` offers Ask the agent. */
  commenting?: boolean
  canAsk?: boolean
  cwd?: string
  rawUrl?: (path: string) => string | null
  hostAway?: string
  /** What the folded terminal's bar says the agent is doing: the attention's message, else the agent's name. */
  barText?: string
  readOnlyBadge?: boolean
}>()
const tabs = defineModel<TabsState>('tabs', { required: true })

/** The gap between the area and the terminal (gap-3), which the handle sits in. */
const GAP = 12

const phone = useIsPhone()
/** Side by side needs a wide column: Tailwind's lg, where the desktop sidebar shows too. */
const wide = useMedia('(min-width: 64rem)')
const column = ref<HTMLElement>()
const kept = ref<EditorLayout>('stacked')
const splits = reactive<Record<EditorLayout, number>>({ stacked: SPLIT_DEFAULT, side: SIDE_SPLIT_DEFAULT })
const editorOpen = computed(() => tabs.value.tabs.length > 0 && !tabs.value.folded)
/** The terminal under the editor folds to its bar: by itself on a phone, wider only as the person collapsed it (#432). */
const terminalFolded = computed(() => editorOpen.value && (phone.value || !!tabs.value.collapsed))
/** Whether the two share the column at all: the editor open, the terminal not folded away under it. */
const shared = computed(() => editorOpen.value && !terminalFolded.value)
/** The column's own width: a wide window whose column sits beside wide panels (a join page's Files and Chat) may still be too narrow for two panes. */
const colWidth = ref(0)
const fits = computed(() => fitsSide(colWidth.value, GAP))
/** Side by side can be chosen: a wide window and a column both panes fit in. */
const canSide = computed(() => shared.value && wide.value && fits.value)
/** The layout the column shows: the kept one where it can be chosen, stacked otherwise. */
const layout = computed<EditorLayout>(() => (shared.value ? layoutFor(kept.value, wide.value, fits.value) : 'stacked'))
const side = computed(() => layout.value === 'side')
const split = computed(() => splits[layout.value])

function storage() {
  return typeof localStorage === 'undefined' ? null : localStorage
}

function switchLayout() {
  kept.value = kept.value === 'side' ? 'stacked' : 'side'
  writeLayout(storage(), kept.value)
}

/** Sets the current layout's split; side by side, what shows once the terminal keeps its width, so a kept split is one that shows. */
function setSplit(v: number) {
  if (!side.value) {
    splits.stacked = clampSplit(v)
    return
  }
  const width = column.value?.getBoundingClientRect().width ?? 0
  splits.side = clampSideSplit(width > 0 ? editorWidth(width, v, GAP) / width : v)
}

let dragging = false
function splitDown(e: PointerEvent) {
  dragging = true
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
}
function splitMove(e: PointerEvent) {
  if (!dragging || !column.value) return
  const r = column.value.getBoundingClientRect()
  if (side.value) {
    if (r.width > 0) setSplit((e.clientX - r.left) / r.width)
  } else if (r.height > 0) {
    setSplit((e.clientY - r.top) / r.height)
  }
}
function splitUp(e: PointerEvent) {
  if (!dragging) return
  dragging = false
  ;(e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId)
  writeSplitFor(layout.value, storage(), split.value)
}
/** The arrows along the bar's axis move it a step, Home and End take it to its ends. */
function splitKey(e: KeyboardEvent) {
  const v = stepSplit(layout.value, split.value, e.key)
  if (v === split.value) return
  e.preventDefault()
  setSplit(v)
  writeSplitFor(layout.value, storage(), split.value)
}
let observer: ResizeObserver | undefined
onMounted(() => {
  const s = storage()
  kept.value = readLayout(s)
  splits.stacked = readSplitFor('stacked', s)
  splits.side = readSplitFor('side', s)
  if (column.value) {
    colWidth.value = column.value.getBoundingClientRect().width
    if (typeof ResizeObserver !== 'undefined') {
      observer = new ResizeObserver((entries) => {
        const w = entries[0]?.contentRect.width
        if (w !== undefined) colWidth.value = w
      })
      observer.observe(column.value)
    }
  }
})
onBeforeUnmount(() => observer?.disconnect())

/** The bar brings the terminal back: on a phone the editor folds to its strip; wider, the collapse ends, the editor staying as it was. */
function bringTerminalUp() {
  tabs.value = phone.value ? toggleFold(tabs.value) : toggleCollapse(tabs.value)
}

/** What a page reaches of the area through the column (#321): the palette's snapshot of the editor and the acts its rows run. */
export interface EditorColumnHandle {
  /** The area's close, with its unsaved-changes guard: what the palette's Close on a tab calls (whole-batch round). */
  requestClose: (id: string) => void
  /** A tab's menu choices (#330): the others, those to the right or the left, or all, through the same guard. */
  closeScope: (id: string, scope: CloseScope) => void
  /** The editor's state for the palette's rows, live; null with no area. */
  palette: EditorPaletteState | null
  setKeymap: (k: Keymap) => void
  save: () => void
  askAgent: () => void
}
const area = useTemplateRef<{ requestClose: (id: string) => void; closeScope: (id: string, scope: CloseScope) => void; palette: EditorPaletteState; setKeymap: (k: Keymap) => void; save: () => void; askAgent: () => void }>('area')
const palette = computed<EditorPaletteState | null>(() => area.value?.palette ?? null)
defineExpose({
  requestClose: (id: string) => area.value?.requestClose(id),
  closeScope: (id: string, scope: CloseScope) => area.value?.closeScope(id, scope),
  palette,
  setKeymap: (k: Keymap) => area.value?.setKeymap(k),
  save: () => area.value?.save(),
  askAgent: () => area.value?.askAgent(),
})
</script>

<template>
  <div ref="column" class="flex flex-1 min-w-0 gap-3" :class="side ? 'flex-row' : 'flex-col'" data-editor-column :data-editor-layout="layout">
    <EditorArea
      ref="area"
      v-model:tabs="tabs"
      :request="request"
      :nvim="nvim"
      :write="write"
      :can-edit="canEdit"
      :commenting="commenting"
      :can-ask="canAsk"
      @comment="emit('comment', $event)"
      :cwd="cwd"
      :raw-url="rawUrl"
      :host-away="hostAway"
      :read-only-badge="readOnlyBadge"
      :layout="canSide ? layout : undefined"
      :class="editorOpen ? (terminalFolded ? 'min-h-0 flex-1' : side ? 'min-w-32 shrink' : 'min-h-32 shrink') : 'flex-none'"
      :style="shared ? { flexBasis: `${Math.round(split * 100)}%` } : undefined"
      @switch-layout="switchLayout"
    />
    <!-- The bar between the two: a separator the pointer drags and the keyboard moves. -->
    <div
      v-if="shared"
      class="flex flex-none items-center justify-center touch-none outline-none focus-visible:[&>span]:bg-primary"
      :class="side ? '-mx-2 w-2 cursor-col-resize' : '-my-2 h-2 cursor-row-resize'"
      role="separator"
      :aria-orientation="side ? 'vertical' : 'horizontal'"
      :aria-valuenow="Math.round(split * 100)"
      aria-valuemin="0"
      aria-valuemax="100"
      :aria-label="side ? 'Resize the editor: the arrow keys move the bar, Home and End take it to its ends' : 'Resize the editor: the arrow keys move the bar, Home and End take it to its ends'"
      tabindex="0"
      data-editor-split
      @pointerdown="splitDown"
      @pointermove="splitMove"
      @pointerup="splitUp"
      @pointercancel="splitUp"
      @keydown="splitKey"
    >
      <span class="rounded-full bg-accented" :class="side ? 'h-10 w-0.5' : 'h-0.5 w-10'" />
    </div>
    <!-- Side by side the terminal and the reply bar stack on the right, the terminal keeping its width; stacked, they stay the
         column's own children (contents). The nodes are the same either way, so the terminal is not remounted by a switch. -->
    <div :class="side ? 'flex min-w-0 flex-1 flex-col gap-3' : 'contents'" :style="side ? { minWidth: `${MIN_TERMINAL_WIDTH}px` } : undefined">
      <!-- The terminal's least height is the slot's (min-h-32): the terminal fills the slot and no more (main.css), so that it lays
           out only the rows that show (#189). -->
      <div class="relative" :class="terminalFolded ? 'h-0 flex-none overflow-hidden' : 'flex-1 min-h-32'" :data-terminal-slot="terminalFolded ? 'folded' : 'open'">
        <slot />
      </div>
      <button v-if="terminalFolded" type="button" class="flex h-11 flex-none items-center gap-2 rounded-md border border-default bg-elevated/60 px-3 text-left text-xs" data-terminal-bar @click="bringTerminalUp">
        <UIcon name="i-lucide-square-terminal" class="size-4 flex-none text-muted" />
        <span class="min-w-0 flex-1 truncate font-medium text-highlighted">{{ barText || 'Terminal' }}</span>
        <span class="flex-none text-muted">{{ phone ? 'tap to bring it up' : 'click to bring it back' }}</span>
      </button>
      <!-- The reply bar, under the folded terminal's bar too: a waiting question's choices stay at hand (#432, decision 2). -->
      <slot name="bar" />
    </div>
  </div>
</template>
