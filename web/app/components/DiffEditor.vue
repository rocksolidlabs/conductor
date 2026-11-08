<script setup lang="ts">
import type { editor as MonacoEditor } from 'monaco-editor'
import { heldUntilReady, type DiffMark, type DiffSelection, type Side } from '~/utils/review'

/**
 * A file's changes in Monaco's diff editor (design 4d): the base's version beside the working directory's, side by side or inline,
 * read-only. Loads Monaco with the first editor on the page (`utils/monaco.ts`). For a review (Program item RV1): `selectable` emits
 * `select` with the side and the whole lines selected on it (the base's on the left, the candidate's on the right), from the mouse or
 * the keys, and Ctrl+Shift+M (the Files tab's Comment key, F7) asks for the note's form at once, on the selection or the cursor's line;
 * `marks` tints the lines of notes and findings in either side's margin. The Files tab passes neither, and gets the editor it had.
 */
const props = withDefaults(defineProps<{ path: string; original: string; modified: string; inline?: boolean; selectable?: boolean; marks?: DiffMark[] }>(), { inline: false, selectable: false, marks: () => [] })
const emit = defineEmits<{ select: [sel: DiffSelection | null] }>()

const host = ref<HTMLElement>()
const colorMode = useColorMode()
let editor: MonacoEditor.IStandaloneDiffEditor | null = null
let monacoRef: typeof import('monaco-editor') | null = null
let models: { original: MonacoEditor.ITextModel; modified: MonacoEditor.ITextModel } | null = null
const decorations: Partial<Record<Side, MonacoEditor.IEditorDecorationsCollection>> = {}
/** The side whose selection was told last: an empty selection on the other side says nothing of it. */
let toldSide: Side | null = null

const theme = computed(() => (colorMode.value === 'dark' ? 'conductor-dark' : 'conductor-light'))

function sideEditor(side: Side): MonacoEditor.ICodeEditor | null {
  if (!editor) return null
  return side === 'base' ? editor.getOriginalEditor() : editor.getModifiedEditor()
}

/** Tells the page the selection on one side: whole lines, from..to, their text, and the spot below its last line, in the host's terms. */
function tell(side: Side, open = false) {
  const sub = sideEditor(side)
  const model = sub?.getModel()
  if (!sub || !model || !host.value) return
  const s = sub.getSelection()
  if (!s || (s.isEmpty() && !open)) {
    if (toldSide === side) {
      toldSide = null
      emit('select', null)
    }
    return
  }
  const from = s.startLineNumber
  let to = s.endLineNumber
  if (s.endColumn === 1 && to > from) to-- // a selection ending at a line's start ends on the line before
  const at = sub.getScrolledVisiblePosition({ lineNumber: to, column: 1 })
  const dom = sub.getDomNode()
  const off = dom ? dom.getBoundingClientRect() : null
  const base = host.value.getBoundingClientRect()
  const top = (at ? at.top + at.height : 0) + (off ? off.top - base.top : 0)
  const left = (at ? at.left : 0) + (off ? off.left - base.left : 0)
  toldSide = side
  emit('select', { side, from, to, lines: model.getLinesContent().slice(from - 1, to), top, left, ...(open ? { open: true } : {}) })
}

/** Ctrl+Shift+M on either side: the note's form at once. */
function onKey(e: KeyboardEvent) {
  if (!props.selectable || !e.ctrlKey || !e.shiftKey || e.altKey || e.key.toLowerCase() !== 'm') return
  const side: Side | null = sideEditor('base')?.hasTextFocus() ? 'base' : sideEditor('candidate')?.hasTextFocus() ? 'candidate' : null
  if (!side) return
  e.preventDefault()
  e.stopPropagation()
  tell(side, true)
}

function paint() {
  if (!monacoRef || !editor) return
  for (const side of ['base', 'candidate'] as const) {
    const sub = sideEditor(side)
    if (!sub) continue
    const marks = props.marks.filter((m) => m.side === side)
    const ds = marks.map((m) => ({
      range: new monacoRef!.Range(m.from, 1, Math.max(m.from, m.to), 1),
      options: { isWholeLine: true, className: 'conductor-review-mark', linesDecorationsClassName: 'conductor-review-mark-gutter', ...(m.label ? { hoverMessage: { value: m.label } } : {}) },
    }))
    if (!decorations[side]) decorations[side] = sub.createDecorationsCollection(ds)
    else decorations[side]!.set(ds)
  }
}

/**
 * Shows lines from..to of a side in the middle, the cursor on the first: a note of the draft opened at its lines. Asked before Monaco
 * has mounted (it loads asynchronously, after the component), it is held until the editor is there, not lost.
 */
const shower = heldUntilReady((ed: MonacoEditor.IStandaloneDiffEditor, side: Side, from: number, to: number) => {
  const sub = side === 'base' ? ed.getOriginalEditor() : ed.getModifiedEditor()
  sub.revealLinesInCenter(from, Math.max(from, to))
  sub.setPosition({ lineNumber: from, column: 1 })
  sub.focus()
})
function show(side: Side, from: number, to: number) {
  shower.call(side, from, to)
}

async function mount() {
  const { loadMonaco, languageFor } = await import('~/utils/monaco')
  const monaco = loadMonaco()
  monacoRef = monaco
  if (!host.value) return
  const lang = languageFor(props.path)
  models = { original: monaco.editor.createModel(props.original, lang), modified: monaco.editor.createModel(props.modified, lang) }
  editor = monaco.editor.createDiffEditor(host.value, {
    editContext: false,
    theme: theme.value,
    readOnly: true,
    originalEditable: false,
    renderSideBySide: !props.inline,
    automaticLayout: true,
    minimap: { enabled: false },
    fontFamily: '"JetBrains Mono Variable", ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
    fontSize: 13,
    lineHeight: 20,
    scrollBeyondLastLine: false,
    renderOverviewRuler: true,
    useInlineViewWhenSpaceIsLimited: false,
  })
  editor.setModel(models)
  if (props.selectable) {
    editor.getOriginalEditor().onDidChangeCursorSelection(() => tell('base'))
    editor.getModifiedEditor().onDidChangeCursorSelection(() => tell('candidate'))
    // A selection scrolled moves its bar with it: told again where its last line is now.
    editor.getOriginalEditor().onDidScrollChange(() => toldSide === 'base' && tell('base'))
    editor.getModifiedEditor().onDidScrollChange(() => toldSide === 'candidate' && tell('candidate'))
  }
  paint()
  shower.ready(editor)
}

watch(theme, (t) => monacoRef?.editor.setTheme(t))
watch(() => props.inline, (v) => editor?.updateOptions({ renderSideBySide: !v }))
watch(
  () => [props.original, props.modified],
  ([o, m]) => {
    if (models && models.original.getValue() !== o) models.original.setValue(o!)
    if (models && models.modified.getValue() !== m) models.modified.setValue(m!)
  },
)
watch(() => props.marks, paint, { deep: true })

defineExpose({ show })

onMounted(mount)
onBeforeUnmount(() => {
  shower.gone()
  decorations.base?.clear()
  decorations.candidate?.clear()
  editor?.dispose()
  models?.original.dispose()
  models?.modified.dispose()
  editor = null
  models = null
})
</script>

<template>
  <div ref="host" class="h-full w-full min-h-0" data-diff-editor :data-diff-selectable="selectable ? '' : undefined" @keydown.capture="onKey" />
</template>

<style>
.conductor-review-mark {
  background: color-mix(in oklab, var(--ui-warning) 14%, transparent);
}
.conductor-review-mark-gutter {
  border-left: 3px solid var(--ui-warning);
  margin-left: 3px;
}
</style>
