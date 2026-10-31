<script setup lang="ts">
import type { ChatQuote, FileHeader, FileRequester, FileWriter, NvimBridge, NvimSwapChoice } from '~/utils/protocol'
import { swapChoices, swapWords, type NvimViewState } from '~/utils/nvimSwap'
import { confirmChoices, confirmQuestion } from '~/utils/nvimConfirm'
import { lostWords } from '~/utils/nvimLost'
import type { EditorPaletteState } from '~/utils/editorRows'
import { heldChanges } from '~/utils/nvimHold'
import { copyText, makeQuote, quoteLocation, quotePath, rangeWords } from '~/utils/quote'
import { closeWords, conflictWords, forgetModel, savedAt } from '~/utils/editorModels'
import { statusLetter, statusTone } from '~/utils/changes'
import { activateTab, closeTab, cycleTab, fileReadOnlyWords, nvimUnavailableWords, openTab, readKeymap, refusedWords, takeLine, tabsToClose, toggleFold, togglePreview, writeKeymap, type CloseScope, type EditorTab, type Keymap, type TabsState } from '~/utils/editorTabs'
import { dirOf, isMarkdownPath } from '~/utils/markdown'
import { modeWords } from '~/utils/nvimKeys'
import { crumbsOf } from '~/utils/fileTree'
import type { EditorLayout } from '~/utils/editorLayout'

/**
 * The editor area (design 4b, 4c, 4f): a tab per file or URL open, the
 * active one in Monaco (or as an image, a binary's note, a URL's preview,
 * the words of a refused read), under a strip of tabs with the fold button
 * (T; Alt+T in the terminal) and × for all; folded, the strip alone stays
 * above the terminal. The header: the breadcrumb from the working
 * directory down to the file, Ln/Col, copy path, open raw. Ctrl+Tab and
 * Ctrl+Shift+Tab switch tabs, Ctrl+W closes the active one.
 */
const props = defineProps<{
  request: FileRequester
  /** The editor's Neovim, when the page offers it (design round 12, F8): the keymap switch shows then. */
  nvim?: NvimBridge
  cwd?: string
  rawUrl?: (path: string) => string | null
  /** The name of a hosted session's machine while it is away: its files cannot be read until it returns. */
  hostAway?: string
  /** A view-only guest: the editor says Read only where a controller will see Save. */
  readOnlyBadge?: boolean
  /** Saves a file (design round 12, F6), and whether this connection may edit: Save, Ctrl+S and an editable editor then. */
  write?: FileWriter
  canEdit?: boolean
  /** Comments on lines (design round 12, F7): the page has a chat to post them to; `canAsk`, and the person may ask the agent. */
  commenting?: boolean
  canAsk?: boolean
  /** How the column lays the editor and the terminal out (#309), when the window is wide enough to choose: the switch shows then. */
  layout?: EditorLayout
}>()
const emit = defineEmits<{ comment: [c: { quote: ChatQuote; text: string; toAgent: boolean }]; switchLayout: [] }>()
const tabs = defineModel<TabsState>('tabs', { required: true })

interface Loaded {
  state: 'loading' | 'text' | 'image' | 'binary' | 'refused' | 'error' | 'url' | 'diff'
  header?: FileHeader
  text?: string
  imageSrc?: string
  dims?: string
  error?: string
  /** A diff: the base's version and the working directory's. */
  original?: string
  modified?: string
}
const inlineDiff = ref(false)
const loaded = reactive(new Map<string, Loaded>())
const pos = ref({ line: 1, col: 1 })
const editorRef = ref<{
  showLine: (n: number) => void
  showRange: (from: number, to: number) => void
  find: () => void
  gotoLine: () => void
  focus: () => void
  getText: () => string
  version: () => number | undefined
  setText: (t: string) => void
  getLines: () => string[]
} | null>(null)
const copy = useCopy()

const active = computed<EditorTab | null>(() => tabs.value.tabs.find((t) => t.id === tabs.value.active) ?? null)
const view = computed<Loaded | null>(() => (active.value ? (loaded.get(active.value.id) ?? null) : null))

function fmtSize(n?: number) {
  if (n === undefined) return ''
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / 1024 / 1024).toFixed(1)} MiB`
}

/**
 * A diff tab: the base's version through git show (none for an added file), the working directory's through a read (none for a deleted
 * one). A commit's diff (design 4e) shows both sides at their revisions: the parent's (a rename's old path; none for a root commit) and
 * the commit's.
 */
async function loadDiff(tab: EditorTab) {
  loaded.set(tab.id, { state: 'loading' })
  const status = tab.meta?.status
  const rev = tab.meta?.rev
  try {
    let original = ''
    let modified = ''
    if (status !== 'A' && status !== '?' && !(rev && !tab.meta?.base)) {
      const res = await props.request(rev ? tab.meta?.from || tab.path : tab.path, false, { op: 'show', rev: tab.meta?.base || 'HEAD' })
      if (res.header.kind === 'error') {
        loaded.set(tab.id, { state: res.header.error?.code === 'file_denied' ? 'refused' : 'error', header: res.header, error: res.header.error?.message || 'cannot read' })
        return
      }
      original = new TextDecoder().decode(res.body)
    }
    if (status !== 'D') {
      const res = rev ? await props.request(tab.path, false, { op: 'show', rev }) : await props.request(tab.path, false)
      if (res.header.kind === 'error') {
        loaded.set(tab.id, { state: res.header.error?.code === 'file_denied' ? 'refused' : 'error', header: res.header, error: res.header.error?.message || 'cannot read' })
        return
      }
      modified = new TextDecoder().decode(res.body)
    }
    loaded.set(tab.id, { state: 'diff', original, modified })
  } catch (e) {
    loaded.set(tab.id, { state: 'error', error: (e as Error).message })
  }
}

async function load(tab: EditorTab) {
  if (tab.kind === 'url') {
    loaded.set(tab.id, { state: 'url' })
    return
  }
  if (tab.kind === 'diff') return loadDiff(tab)
  loaded.set(tab.id, { state: 'loading' })
  try {
    const res = await props.request(tab.path, false)
    const h = res.header
    if (h.kind === 'error') {
      const msg = h.error?.message || 'cannot read'
      loaded.set(tab.id, { state: h.error?.code === 'file_denied' ? 'refused' : 'error', header: h, error: msg })
    } else if (h.kind === 'dir') {
      loaded.set(tab.id, { state: 'error', header: h, error: 'a folder: open it in the Files pane' })
    } else if (h.mime?.startsWith('image/') && res.body.length) {
      let bin = ''
      for (const b of res.body) bin += String.fromCharCode(b)
      const src = `data:${h.mime};base64,${btoa(bin)}`
      loaded.set(tab.id, { state: 'image', header: h, imageSrc: src })
      const img = new Image()
      img.onload = () => {
        const cur = loaded.get(tab.id)
        if (cur?.state === 'image') loaded.set(tab.id, { ...cur, dims: `${img.naturalWidth} × ${img.naturalHeight}` })
      }
      img.src = src
    } else if (h.binary) {
      loaded.set(tab.id, { state: 'binary', header: h })
    } else {
      loaded.set(tab.id, { state: 'text', header: h, text: new TextDecoder().decode(res.body) })
    }
  } catch (e) {
    loaded.set(tab.id, { state: 'error', error: (e as Error).message })
  }
}

watch(
  active,
  (t) => {
    if (t && !loaded.has(t.id)) load(t)
  },
  { immediate: true },
)

/** A tab closed drops what was loaded for it. */
watch(
  () => tabs.value.tabs.map((t) => t.id),
  (ids) => {
    for (const id of [...loaded.keys()]) if (!ids.includes(id)) loaded.delete(id)
  },
)

/** The line (or a quote's range) a tab was opened at, once the editor is up. */
function onReady() {
  previewSeq.value++
  const t = active.value
  if (t?.line) {
    if (t.lineTo && t.lineTo > t.line) editorRef.value?.showRange(t.line, t.lineTo)
    else editorRef.value?.showLine(t.line)
    tabs.value = takeLine(tabs.value, t.id)
  }
}
watch(
  () => [active.value?.line, active.value?.lineTo],
  () => {
    const t = active.value
    if (t?.line && view.value?.state === 'text' && editorRef.value) {
      if (t.lineTo && t.lineTo > t.line) editorRef.value.showRange(t.line, t.lineTo)
      else editorRef.value.showLine(t.line)
      tabs.value = takeLine(tabs.value, t.id)
    }
  },
)

// Comments on lines (design round 12, F7): a selection in a file shows a bar (Comment, Ask the agent, Copy; Ctrl+Shift+M and
// Ctrl+Shift+A); the composer under it carries the quote and the words to the page's chat, and with Ask the agent into the agent.
const selection = ref<{ from: number; to: number; top: number; left: number } | null>(null)
const composer = ref<{ ask: boolean; quote: ChatQuote; text: string; top: number; left: number } | null>(null)
const commentable = computed(() => !!props.commenting && active.value?.kind === 'file' && view.value?.state === 'text')
watch(
  () => active.value?.id,
  () => {
    selection.value = null
    composer.value = null
  },
)
function onSelection(s: { from: number; to: number; top: number; left: number } | null) {
  selection.value = s
}
function quoteNow(): ChatQuote | null {
  const t = active.value
  if (!t || !editorRef.value) return null
  const s = selection.value
  const from = s?.from ?? pos.value.line
  const to = s?.to ?? pos.value.line
  return makeQuote(quotePath(t.path, props.cwd), editorRef.value.getLines(), from, to)
}
function openComposer(ask: boolean) {
  if (!commentable.value || (ask && !props.canAsk)) return
  const quote = quoteNow()
  if (!quote) return
  composer.value = { ask, quote, text: '', top: selection.value?.top ?? 40, left: Math.max(8, selection.value?.left ?? 8) }
  nextTick(() => (document.querySelector('[data-editor-composer] textarea') as HTMLTextAreaElement | null)?.focus())
}
function copyLines() {
  const q = quoteNow()
  if (q) copy(copyText(q), 'Lines copied')
}
function sendComment(toAgent: boolean) {
  const c = composer.value
  if (!c) return
  emit('comment', { quote: c.quote, text: c.text.trim(), toAgent })
  composer.value = null
  selection.value = null
  editorRef.value?.focus()
}
function onComposerKey(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.preventDefault()
    composer.value = null
    editorRef.value?.focus()
  } else if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    sendComment(!!composer.value?.ask)
  }
}

const crumbs = computed(() => {
  const t = active.value
  if (!t || t.kind === 'url') return []
  const root = (props.cwd || '').replace(/\/+$/, '')
  const rel = root && t.path.startsWith(root + '/') ? t.path.slice(root.length + 1) : null
  if (rel === null) return t.path.split('/').filter(Boolean).map((label) => ({ label }))
  return [{ label: crumbsOf(root).pop()!.label }, ...rel.split('/').map((label) => ({ label }))]
})

const rawHref = computed(() => (active.value?.kind === 'file' && props.rawUrl ? props.rawUrl(active.value.path) : null))

/** A diff's file, as its own tab. */
function openFileOfDiff() {
  if (active.value?.kind === 'diff') tabs.value = openTab(tabs.value, 'file', active.value.path)
}

const refused = refusedWords

function reload() {
  if (active.value) load(active.value)
}

// The keymap (design round 12, F8): Monaco's keys, or the real Neovim on the
// session's machine, chosen with the button and kept per browser. Off unless
// chosen: nobody who never presses it meets a Vim key.
const keymap = ref<Keymap>('default')
onMounted(() => {
  keymap.value = readKeymap(typeof localStorage === 'undefined' ? null : localStorage)
})
function setKeymap(k: Keymap) {
  if (keymapBusy.value) return
  keymap.value = k
  writeKeymap(typeof localStorage === 'undefined' ? null : localStorage, k)
}
/** The file in front is one the owner takes no save of (in a repository's .git): read only, in Monaco, whatever the keymap. */
const fileReadOnly = computed(() => (active.value?.kind === 'file' ? fileReadOnlyWords(view.value?.header) : ''))
const nvimWhy = computed(() => (props.nvim ? nvimUnavailableWords(props.nvim.offer) || fileReadOnly.value : 'Neovim is not offered on this page'))
const nvimOn = computed(() => keymap.value === 'nvim' && !!props.nvim && nvimWhy.value === '')
const nvimState = ref<NvimViewState | null>(null)
// Reopen after Neovim was lost (utils/nvimLost): a new Neovim for the tab in front, the lost one's text kept until it has opened (an open
// that fails leaves the tab as it was, saying why). Copy keeps that text first, as the banner says. Not while the connection is down.
function reopenNvim() {
  void (editorRef.value as { reopenNvim?: () => Promise<boolean> } | null)?.reopenNvim?.()
}
function copyLostText() {
  // The text is the editor's: with no editor up (its machine away) there is none to copy, and nothing says there was.
  const text = editorRef.value?.getText()
  if (text !== undefined) copy(text, 'Text copied')
}
// Neovim's status is the editor's in front: another tab, or the other keymap, starts it empty until that editor reports (round 14: the
// last tab's mode, banner or question stayed over the next tab's file, and a test typed into the new tab before its Neovim listened).
watch(
  () => `${active.value?.id ?? ''}:${nvimOn.value}`,
  () => (nvimState.value = null),
)
/** Neovim's own question and its choices, when one shows (round 13, G3). */
const nvimConfirm = computed(() => {
  const st = nvimState.value
  if (!nvimOn.value || st?.messageKind !== 'confirm') return null
  const choices = confirmChoices(st.message)
  return choices.length ? { question: confirmQuestion(st.message), choices } : null
})
function dismissRefused() {
  ;(editorRef.value as { dismissRefused?: () => void } | null)?.dismissRefused?.()
}
function answerConfirm(key: string) {
  ;(editorRef.value as { answerConfirm?: (k: string) => void } | null)?.answerConfirm?.(key)
}
/** Answers the swap file the editor's Neovim found (round 13, G3). */
function answerSwap(choice: NvimSwapChoice) {
  ;(editorRef.value as { answerSwap?: (c: NvimSwapChoice) => void } | null)?.answerSwap?.(choice)
}
// Keys on their way to a Neovim (in front, or kept for a tab): the keymap waits for them, since what they make is Neovim's to show.
const holdsTick = ref(0)
const keymapBusy = computed(() => {
  void holdsTick.value
  return keymap.value === 'nvim' && (!!nvimState.value?.unsettled || !!props.nvim?.holds.busy())
})
const keymapTip = computed(() => {
  // Each keymap keeps its own text (Neovim's buffer, Monaco's model): switching with changes not written would lose them.
  if (dirty.size) return 'Save or discard the changes first: the other keymap would not have them'
  if (keymapBusy.value) return 'Neovim is still taking keys: a moment'
  if (keymap.value === 'nvim') return nvimWhy.value ? `${nvimWhy.value} · Monaco's keys meanwhile · click for Monaco's keys` : "Neovim keys: the real Neovim on the session's machine holds the file · click for Monaco's keys"
  return "Monaco's keys · click for Neovim keys (the real Neovim on the session's machine)"
})
function closeActive() {
  // Neovim's :q: Neovim kept its own buffer, nothing to ask.
  if (active.value) closeNow([active.value.id])
}
// The paths whose tabs the person closed with Don't save: their held Neovims drop their changes as they end (the orphans below).
const dropConsented = new Set<string>()
// A tab not in front keeps its Neovim while its buffer holds changes not written (the bridge's holds); once its tab is closed, its
// changes go with it (the close asked first: Don't save), and its swap file with them. After the render: a tab closing in front hands
// its Neovim over as its editor leaves.
watch(
  () => tabs.value.tabs.filter((t) => t.kind === 'file').map((t) => t.path),
  (paths) => {
    const bridge = props.nvim
    if (!bridge) return
    for (const o of bridge.holds.orphans(paths)) {
      const dropped = dropConsented.delete(o.path)
      if (o.lost) continue
      // Its changes go when the person said so (Don't save); any it made after, or that the close did not ask about, stay (its swap file).
      if (dropped) bridge.close(o.id, true)
      // Kept for keys on their way: it ends once it has done them, keeping what they made (its swap file), as a lost connection does.
      else if (o.unsettled) closeOnceSettled(bridge, o.id, o.seq)
      else bridge.close(o.id)
    }
  },
  { flush: 'post' },
)
// The Neovim keymap turned off by its button (no tab holds changes then: it waits for them): every Neovim kept for a tab goes, those with keys
// on their way once they are done, keeping what those make (their swap file), so no Neovim writes into a text Monaco's keys now edit.
// After the render, as the editor in front has handed its own over.
watch(
  keymap,
  (k) => {
    const bridge = props.nvim
    if (k === 'nvim' || !bridge) return
    for (const o of bridge.holds.orphans([])) {
      if (o.lost) continue
      if (o.unsettled) closeOnceSettled(bridge, o.id, o.seq)
      else bridge.close(o.id)
    }
  },
  { flush: 'post' },
)
/**
 * Ends Neovim id once it has done the keys up to seq (its `settled`; from an older owner, which never says so, their acknowledgement), or
 * has ended, or after half a minute (a question it waits on).
 */
function closeOnceSettled(bridge: NvimBridge, id: string, seq: number) {
  let off: () => void = () => {}
  const end = () => {
    clearTimeout(timer)
    off()
    bridge.close(id)
  }
  const timer = setTimeout(end, 30_000)
  off = bridge.subscribe((ev) => {
    if (ev.id !== id) return
    if (ev.kind === 'closed') {
      clearTimeout(timer)
      off()
    } else if ((ev.kind === 'settled' || (ev.kind === 'cursor' && !bridge.offer.settled)) && (ev.ack ?? 0) >= seq) end()
  })
}
// A tab whose editor is not up (not in front, or the area folded) wears its unsaved dot as its held Neovim says (Neovim's word coming
// after the tab was left, a loss): the close then asks. An editor that is up says it itself.
let stopListening: (() => void) | null = null
watch(
  () => props.nvim?.holds,
  (holds) => {
    stopListening?.()
    stopListening =
      holds?.listen((path, held) => {
        holdsTick.value++
        const t = tabs.value.tabs.find((x) => x.kind === 'file' && x.path === path)
        if (!t) return
        // Held, its editor is not up, though its tab may be in front (the area folded); let go as the tab in front comes up again, its
        // editor says.
        if (held) setDirty(t.id, heldChanges(held))
        else if (t.id !== active.value?.id) setDirty(t.id, false)
      }) ?? null
  },
  { immediate: true },
)
onBeforeUnmount(() => stopListening?.())

// Editing (design round 12, F6): a controller on a session that allows it edits a text file read whole in Monaco (Neovim's keymap
// edits through Neovim instead); Save and Ctrl+S write it on the session's machine, refused when the file changed on disk since it
// was read (Compare, Reload, Save anyway); a tab with unsaved changes wears a dot and asks before it closes.
const dirty = reactive(new Set<string>())
const saving = ref(false)
// A save with no answer after 20 s goes on waiting (the transport never gives one up while its connection lives): the bar says so.
const saveSlow = ref(false)
const savedFlash = ref(false)
// A save asked for while one is on its way (Ctrl+S during a slow save) is not queued: the bar says one is on its way, and the person
// saves again once it is answered. Never dropped without a word.
const saveBusy = ref(false)
// One timer for the flash: a save within two seconds of another restarts it, so the earlier save's timer cannot hide the new "Saved".
let savedTimer: ReturnType<typeof setTimeout> | undefined
onBeforeUnmount(() => clearTimeout(savedTimer))
const saveError = ref('')
const conflict = ref<{ tabId: string; header: FileHeader } | null>(null)
// mine is the editor's text as Compare opened, and version its model's version then: a save from the compare view saves that version.
const compare = ref<{ tabId: string; disk: string; mine: string; version?: number } | null>(null)
const closing = ref<{ ids: string[] } | null>(null)
const editable = computed(
  () => !!props.write && !!props.canEdit && !nvimOn.value && !fileReadOnly.value && active.value?.kind === 'file' && view.value?.state === 'text' && !view.value.header?.truncated && !!view.value.header?.sha256,
)
const activeConflict = computed(() => (conflict.value && conflict.value.tabId === active.value?.id ? conflict.value : null))
const activeCompare = computed(() => (compare.value && compare.value.tabId === active.value?.id ? compare.value : null))

// The Markdown preview (#166): a `.md` tab shown rendered by its Preview toggle (the header's button; Alt+P), the choice
// kept per tab. The source editor stays mounted behind it (hidden), so its model, cursor and unsaved edits are as they
// were; the preview shows the editor's text as of the toggle (the edits included), or the file as read until the editor has it.
const previewRef = ref<{ focus: () => void } | null>(null)
const previewable = computed(() => active.value?.kind === 'file' && view.value?.state === 'text' && isMarkdownPath(active.value.path))
const previewing = computed(() => previewable.value && !!active.value?.preview && !activeCompare.value)
const previewSeq = ref(0)
const previewText = computed(() => {
  void previewSeq.value
  const ed = editorRef.value
  return ed && ed.version() !== undefined ? ed.getText() : (view.value?.text ?? '')
})
function togglePreviewOf() {
  const t = active.value
  if (!t || !previewable.value) return
  tabs.value = togglePreview(tabs.value, t.id)
  previewSeq.value++
  void nextTick(() => (previewing.value ? previewRef.value?.focus() : editorRef.value?.focus()))
}
function setDirty(id: string, d: boolean) {
  if (d) dirty.add(id)
  else dirty.delete(id)
  // Told on every change to the model (a reload after a save conflict, Neovim's buffer): the preview shows the text as it is now.
  previewSeq.value++
}
async function save(force = false) {
  const t = active.value
  const v = view.value
  if (!t || !v || !editable.value || !props.write) return
  if (saving.value) {
    saveBusy.value = true
    return
  }
  // From the compare view the editor is not up: its text is the compare's right side, the model's text as Compare opened.
  const fromCompare = !!activeCompare.value
  const text = fromCompare ? activeCompare.value!.mine : (editorRef.value?.getText() ?? v.text ?? '')
  // The model's version whose text this is: the answer may land after more typing (back from the compare view too), or with another
  // tab in front, and completes this version alone (savedAt); the editor on screen then may not be this tab's.
  const sent = fromCompare ? activeCompare.value!.version : editorRef.value?.version()
  saving.value = true
  saveError.value = ''
  // "Saved" is this save's to show: an earlier one's flash goes.
  savedFlash.value = false
  clearTimeout(savedTimer)
  try {
    const res = await props.write(t.path, new TextEncoder().encode(text), { baseSha256: v.header?.sha256, force, onSlow: () => (saveSlow.value = true) })
    const h = res.header
    // The tab as the save found it, or nothing: one closed meanwhile (its edits dropped), opened again or read again from disk holds
    // what happened since, and the answer changes none of it. The file on disk is what the answer says either way.
    if (loaded.get(t.id) !== v) return
    if (h.kind === 'written') {
      // The saved version first: the text below reaches the editor's watcher, which keeps a model with edits since as it is.
      setDirty(t.id, savedAt(t.path, sent))
      loaded.set(t.id, { ...v, text, header: { ...v.header!, sha256: h.sha256, mtime: h.mtime, size: h.size } })
      conflict.value = null
      compare.value = null
      savedFlash.value = true
      clearTimeout(savedTimer)
      savedTimer = setTimeout(() => (savedFlash.value = false), 2000)
    } else if (h.error?.code === 'changed_on_disk') {
      conflict.value = { tabId: t.id, header: h }
    } else {
      saveError.value = h.error?.message || 'cannot save'
    }
  } catch (e) {
    saveError.value = (e as Error).message
  } finally {
    saving.value = false
    saveSlow.value = false
    // The note goes with the save it was about.
    saveBusy.value = false
  }
}
/** The file as it is on disk now, the unsaved edits dropped. */
async function reloadFromDisk() {
  const t = active.value
  if (!t) return
  const res = await props.request(t.path, false)
  if (res.header.kind !== 'file') {
    saveError.value = res.header.error?.message || 'cannot read the file'
    return
  }
  const text = new TextDecoder().decode(res.body)
  const fromCompare = !!activeCompare.value
  loaded.set(t.id, { state: 'text', header: res.header, text })
  conflict.value = null
  compare.value = null
  // The unsaved edits go: an editor that is up takes the text; one coming back from the compare view reads it fresh.
  if (fromCompare || !editorRef.value) forgetModel(t.path)
  else editorRef.value.setText(text)
  dirty.delete(t.id)
}
/** Disk and the unsaved edits side by side. */
async function openCompare() {
  const t = active.value
  if (!t) return
  const mine = editorRef.value?.getText() ?? view.value?.text ?? ''
  const version = editorRef.value?.version()
  const res = await props.request(t.path, false)
  compare.value = { tabId: t.id, disk: res.header.kind === 'file' ? new TextDecoder().decode(res.body) : '', mine, version }
}
function closeNow(ids: string[]) {
  let st = tabs.value
  for (const id of ids) {
    const t = st.tabs.find((x) => x.id === id)
    if (t && dirty.has(id)) forgetModel(t.path)
    dirty.delete(id)
    if (conflict.value?.tabId === id) conflict.value = null
    if (compare.value?.tabId === id) compare.value = null
    st = closeTab(st, id)
  }
  tabs.value = st
}
/** Closes a tab, asking first when it has unsaved changes (it comes to the front to be saved). */
function requestClose(id: string) {
  if (!dirty.has(id)) return closeNow([id])
  tabs.value = activateTab(tabs.value, id)
  closing.value = { ids: [id] }
}
function requestCloseAll() {
  requestCloseIds(tabs.value.tabs.map((t) => t.id))
}
/**
 * Closes several tabs at once (#330): unsaved among them ask first (the one unsaved brought to the front), else they close now,
 * through closeNow (Codex's round: a clean tab's conflict and compare state goes with it, or a reopened tab would save the stale text).
 */
function requestCloseIds(ids: string[]) {
  if (!ids.length) return
  const unsaved = ids.filter((id) => dirty.has(id))
  if (!unsaved.length) return closeNow(ids)
  if (unsaved.length === 1) tabs.value = activateTab(tabs.value, unsaved[0]!)
  closing.value = { ids }
}
/** A tab's menu (#330), as VS Code's: Close, and the others, those to the right or the left, or all, each through the close that asks. */
function closeScope(id: string, scope: CloseScope) {
  requestCloseIds(tabsToClose(tabs.value, id, scope))
}
/**
 * Shift+F10 or the Menu key on a focused tab opens its menu (Codex's batch rounds): Chromium fires no contextmenu event for them
 * on macOS, so the tab fires its own at its foot. It takes the key on both edges, so the browsers that do fire one fire no
 * second, and opens on the release: Windows opens its own menu on the Menu key's release, and a menu opened on the press would
 * have the focus by then, the tab never seeing the release to take it. The press is remembered and its release matched by the
 * key alone: Shift let go before F10 leaves the release without it. The press belongs to its tab: released on another tab, or
 * anywhere else, it opens nothing and is forgotten (Codex's fifth round).
 */
let tabKeyPressed: { key: string; el: HTMLElement } | null = null
function onTabKey(e: KeyboardEvent) {
  if (e.type === 'keydown') {
    if (!((e.key === 'F10' && e.shiftKey) || e.key === 'ContextMenu')) return
    e.preventDefault()
    tabKeyPressed = { key: e.key, el: e.currentTarget as HTMLElement }
    return
  }
  if (e.key !== tabKeyPressed?.key) return
  const el = tabKeyPressed.el
  tabKeyPressed = null
  e.preventDefault()
  if (el !== e.currentTarget) return
  const r = el.getBoundingClientRect()
  el.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: r.left + r.width / 2, clientY: r.bottom - 2 }))
}
function forgetTabKey(e: KeyboardEvent) {
  // The chord's own key released elsewhere; Shift let go first is no release of it.
  if (e.key === tabKeyPressed?.key) tabKeyPressed = null
}
/**
 * Keys inside an open tab menu are the menu's (Codex's second round): a letter typed to find an item, or Escape, must not reach
 * the page's shortcuts (S shares a session; Escape leaves the Yard's focus), which listen on the window. The document hears a
 * key first and stops it there; Reka's navigation and typeahead, on the menu itself, still run, and Escape closes the menu
 * through the closer in the tab, since Reka's own Escape listens on the window too.
 */
const tabMenuOpen = ref<string | null>(null)
const menuClosers = new Map<string, { close: () => void; open: () => boolean }>()
function onTabMenu(id: string, open: boolean) {
  if (open) tabMenuOpen.value = id
  else if (tabMenuOpen.value === id) tabMenuOpen.value = null
}
// The closer goes with its tab: a tab closed from its own menu unmounts the menu before Reka says it closed (Codex's third
// round), so the mark clears here too, and a key is only ever taken for a menu whose closer says it is open now.
function setMenuCloser(id: string, el: unknown) {
  if (el) menuClosers.set(id, el as { close: () => void; open: () => boolean })
  else {
    menuClosers.delete(id)
    if (tabMenuOpen.value === id) tabMenuOpen.value = null
  }
}
function onMenuKey(e: KeyboardEvent) {
  const id = tabMenuOpen.value
  const closer = id ? menuClosers.get(id) : undefined
  if (!closer?.open() || !(e.target as HTMLElement | null)?.closest('[role="menu"]')) return
  e.stopPropagation()
  if (e.key === 'Escape') {
    e.preventDefault()
    closer.close()
  }
}
onMounted(() => {
  document.addEventListener('keydown', onMenuKey)
  // After the tab's own keyup handler, so a release that reached the tab opened the menu first; a release anywhere else forgets.
  document.addEventListener('keyup', forgetTabKey)
})
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onMenuKey)
  document.removeEventListener('keyup', forgetTabKey)
})
function tabMenu(id: string) {
  return [
    [{ label: 'Close', icon: 'i-lucide-x', kbds: ['ctrl', 'W'], onSelect: () => requestClose(id) }],
    [
      { label: 'Close others', icon: 'i-lucide-copy-x', onSelect: () => closeScope(id, 'others') },
      { label: 'Close to the right', icon: 'i-lucide-arrow-right-to-line', onSelect: () => closeScope(id, 'right') },
      { label: 'Close to the left', icon: 'i-lucide-arrow-left-to-line', onSelect: () => closeScope(id, 'left') },
    ],
    [{ label: 'Close all', icon: 'i-lucide-x', onSelect: () => closeScope(id, 'all') }],
  ]
}
const closingWords = computed(() => {
  const ids = closing.value?.ids.filter((id) => dirty.has(id)) ?? []
  return closeWords(ids.map((id) => tabs.value.tabs.find((t) => t.id === id)?.title ?? id))
})
const closingOne = computed(() => (closing.value?.ids.filter((id) => dirty.has(id)).length ?? 0) === 1)
async function closingSave() {
  const ids = closing.value?.ids ?? []
  if (nvimOn.value) {
    // Neovim holds the file: its own :w, then the tab closes once Neovim says nothing is left unwritten.
    saving.value = true
    const ok = (await (editorRef.value as { nvimWrite?: () => Promise<boolean> } | null)?.nvimWrite?.()) ?? false
    saving.value = false
    if (ok && closing.value) {
      closing.value = null
      closeNow(ids)
    }
    return
  }
  await save()
  if (active.value && !dirty.has(active.value.id)) {
    closing.value = null
    closeNow(ids)
  }
}
async function closingDiscard() {
  const ids = closing.value?.ids ?? []
  closing.value = null
  // The person said Don't save: their Neovims drop what they hold as they end (the orphans, once the tabs have gone), and only
  // theirs, this once: the word goes with this close, whatever it found.
  for (const id of ids) {
    const t = tabs.value.tabs.find((x) => x.id === id)
    if (t?.kind === 'file') dropConsented.add(t.path)
  }
  closeNow(ids)
  await nextTick()
  dropConsented.clear()
}

function onKeydown(e: KeyboardEvent) {
  if (e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey && (e.code === 'KeyP' || e.key.toLowerCase() === 'p')) {
    // Alt+P: the Markdown preview, or the source again (#166). By the physical key: Option+P types π on a Mac.
    if (!previewable.value) return
    e.preventDefault()
    togglePreviewOf()
    return
  }
  if (!(e.ctrlKey || e.metaKey)) return
  if (e.shiftKey && !e.altKey && (e.key.toLowerCase() === 'm' || e.key.toLowerCase() === 'a') && commentable.value) {
    e.preventDefault()
    openComposer(e.key.toLowerCase() === 'a')
    return
  }
  if (e.key.toLowerCase() === 's' && !e.altKey && !e.shiftKey) {
    // Ctrl+S saves (F6); never the page's own Save as.
    e.preventDefault()
    if (editable.value) void save()
    return
  }
  if (e.key === 'Tab') {
    e.preventDefault()
    tabs.value = cycleTab(tabs.value, e.shiftKey ? -1 : 1)
  } else if (e.key.toLowerCase() === 'w' && !e.shiftKey && !e.altKey) {
    if (!active.value) return
    e.preventDefault()
    requestClose(active.value.id)
  }
}

// The palette's rows (#321): the editor's state as one reactive snapshot, read by the page's group (through the column) into
// Keys → Neovim, Save, Close file and Ask the agent about the selection, and the acts behind them.
// Folded, the editor is not mounted and its text is not at hand: a save then would write the text as loaded and forget the
// edits the kept model holds (Codex's round on #321), so Save waits for the editor to be back.
const paletteState = computed<EditorPaletteState>(() => {
  const t = active.value
  const folded = tabs.value.folded
  const saveWhy = !t
    ? 'No file is open'
    : nvimOn.value
      ? 'Neovim holds the file: :w writes it'
      : fileReadOnly.value ||
        (!props.write || !props.canEdit ? 'This connection may not edit' : !editable.value ? 'Not a file that can be saved here' : !dirty.has(t.id) ? 'Nothing to save' : folded ? 'Bring the editor back first (T)' : '')
  return {
    activeId: t?.id ?? null,
    keymap: keymap.value,
    nvimOffered: !!props.nvim && nvimWhy.value === '',
    nvimWhy: nvimWhy.value,
    switchBlocked: dirty.size ? 'Save or discard the changes first: the other keymap would not have them' : keymapBusy.value ? 'Neovim is still taking keys: a moment' : '',
    saveable: !!t && editable.value && dirty.has(t.id) && !folded,
    saveWhy,
    canAsk: commentable.value && !!props.canAsk,
    hasSelection: !!selection.value,
  }
})
function askAgent() {
  openComposer(true)
}
function saveFromPalette() {
  if (!tabs.value.folded) void save()
}
defineExpose({ find: () => editorRef.value?.find(), gotoLine: () => editorRef.value?.gotoLine(), requestClose, closeScope, palette: paletteState, setKeymap, save: saveFromPalette, askAgent })
</script>

<template>
  <div v-if="tabs.tabs.length" class="flex min-h-0 flex-col overflow-hidden rounded-md border border-default bg-default" :data-editor-area="tabs.folded ? 'folded' : 'open'" tabindex="-1" @keydown="onKeydown">
    <!-- Folded: one line above the terminal, the tabs still there. -->
    <div v-if="tabs.folded" class="flex h-9 items-center gap-2 px-2 text-xs" data-editor-folded>
      <UIcon name="i-lucide-files" class="size-4 flex-none text-muted" />
      <span class="font-medium text-highlighted" data-editor-count>{{ tabs.tabs.length === 1 ? '1 file open' : `${tabs.tabs.length} files open` }}</span>
      <span class="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
        <!-- Each tab wears its unsaved dot folded as it does in the strip: a held Neovim's change said while folded shows here. -->
        <button v-for="t in tabs.tabs" :key="t.id" type="button" class="flex min-w-0 items-center gap-1 rounded px-1.5 py-0.5 font-mono text-[11px] text-muted hover:bg-elevated hover:text-default" :data-editor-folded-tab="t.id" @click="tabs = activateTab(tabs, t.id)">
          <span class="truncate">{{ t.title }}</span>
          <span v-if="dirty.has(t.id)" class="size-1.5 flex-none rounded-full bg-primary" aria-label="unsaved" data-editor-dirty />
        </button>
      </span>
      <UTooltip text="Show the editor (T · Alt+T in the terminal)">
        <UButton icon="i-lucide-panel-top" size="xs" color="neutral" variant="ghost" aria-label="Show the editor" data-editor-unfold @click="tabs = toggleFold(tabs)" />
      </UTooltip>
      <UTooltip text="Close all">
        <UButton icon="i-lucide-x" size="xs" color="neutral" variant="ghost" aria-label="Close all files" data-editor-close-all @click="requestCloseAll()" />
      </UTooltip>
    </div>
    <template v-else>
      <!-- The tab strip. -->
      <div class="flex h-9 flex-none items-stretch border-b border-default bg-elevated/40" data-editor-tabs>
        <div class="flex min-w-0 flex-1 items-stretch overflow-x-auto">
          <!-- A tab's menu (#330): right-click, or Shift+F10 or the Menu key on the focused tab (the tab fires the event itself: Chromium on macOS fires none for them). -->
          <UContextMenu v-for="t in tabs.tabs" :key="t.id" :items="tabMenu(t.id)" @update:open="(v: boolean) => onTabMenu(t.id, v)">
            <div
              class="group flex max-w-56 flex-none items-center gap-1.5 border-r border-default pl-3 pr-1.5 text-xs"
              :class="t.id === tabs.active ? 'bg-default text-highlighted' : 'text-muted hover:text-default'"
              :data-editor-tab="t.id"
              :data-editor-tab-active="t.id === tabs.active ? '' : undefined"
              @keydown="onTabKey"
              @keyup="onTabKey"
            >
              <ContextMenuCloser :ref="(el: unknown) => setMenuCloser(t.id, el)" />
              <button type="button" class="flex min-w-0 items-center gap-1.5" :title="t.path" @click="tabs = activateTab(tabs, t.id)">
                <UIcon :name="t.kind === 'url' ? 'i-lucide-globe' : t.kind === 'diff' ? 'i-lucide-file-diff' : 'i-lucide-file'" class="size-3.5 flex-none" />
                <span class="truncate font-mono">{{ t.title }}</span>
                <span v-if="dirty.has(t.id)" class="size-1.5 flex-none rounded-full bg-primary" aria-label="unsaved" data-editor-dirty />
              </button>
              <UButton icon="i-lucide-x" size="xs" color="neutral" variant="ghost" class="opacity-0 group-hover:opacity-100 data-[active]:opacity-100" :aria-label="`Close ${t.title}`" :data-editor-close="t.id" @click="requestClose(t.id)" />
            </div>
          </UContextMenu>
        </div>
        <div class="flex flex-none items-center gap-0.5 px-1">
          <UTooltip v-if="nvim" :text="keymapTip">
            <UButton :disabled="dirty.size > 0 || keymapBusy" :label="keymap === 'nvim' ? 'Neovim' : 'Keys'" icon="i-lucide-keyboard" size="xs" :color="keymap === 'nvim' ? 'primary' : 'neutral'" :variant="keymap === 'nvim' ? 'soft' : 'ghost'" :aria-label="keymap === 'nvim' ? 'Keymap: Neovim' : 'Keymap: default'" :data-editor-keymap="keymap" @click="setKeymap(keymap === 'nvim' ? 'default' : 'nvim')" />
          </UTooltip>
          <UTooltip v-if="layout" :text="layout === 'side' ? 'Editor above the terminal' : 'Editor beside the terminal'">
            <UButton :icon="layout === 'side' ? 'i-lucide-rows-2' : 'i-lucide-columns-2'" size="xs" color="neutral" variant="ghost" :aria-label="layout === 'side' ? 'Editor above the terminal' : 'Editor beside the terminal'" :data-editor-layout-switch="layout" @click="emit('switchLayout')" />
          </UTooltip>
          <UTooltip text="Terminal only (T · Alt+T in the terminal)">
            <UButton icon="i-lucide-panel-bottom" size="xs" color="neutral" variant="ghost" aria-label="Terminal only" data-editor-fold @click="tabs = toggleFold(tabs)" />
          </UTooltip>
          <UTooltip text="Close all">
            <UButton icon="i-lucide-x" size="xs" color="neutral" variant="ghost" aria-label="Close all files" data-editor-close-all @click="requestCloseAll()" />
          </UTooltip>
        </div>
      </div>
      <!-- The header: crumbs, the position, the buttons. -->
      <div v-if="active" class="flex h-8 flex-none items-center gap-2 border-b border-default px-3 text-xs">
        <template v-if="active.kind === 'url'">
          <UIcon name="i-lucide-globe" class="size-3.5 flex-none text-muted" />
          <span class="min-w-0 flex-1 truncate font-mono text-muted" data-editor-url>{{ active.path }}</span>
          <UButton label="New tab" icon="i-lucide-external-link" size="xs" color="neutral" variant="ghost" :to="active.path" target="_blank" rel="noopener noreferrer" />
          <UButton icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="ghost" aria-label="Refresh" @click="reload" />
          <UButton icon="i-lucide-copy" size="xs" color="neutral" variant="ghost" aria-label="Copy URL" @click="copy(active.path, 'URL copied')" />
        </template>
        <template v-else>
          <nav class="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto font-mono" aria-label="path" data-editor-crumbs>
            <template v-for="(c, i) in crumbs" :key="i">
              <UIcon v-if="i > 0" name="i-lucide-chevron-right" class="size-3 flex-none text-muted" />
              <span :class="i === crumbs.length - 1 ? 'text-highlighted' : 'text-muted'">{{ c.label }}</span>
            </template>
          </nav>
          <template v-if="active.kind === 'diff'">
            <span class="flex-none text-muted" data-editor-against>{{ active.meta?.against || 'working directory vs HEAD' }}</span>
            <UBadge v-if="active.meta?.status" :label="statusLetter(active.meta.status as any)" :color="statusTone(active.meta.status as any)" variant="subtle" size="xs" class="font-mono" />
            <span class="flex-none font-mono text-[11px]"><span class="text-success">+{{ active.meta?.added ?? 0 }}</span> <span class="text-error">−{{ active.meta?.removed ?? 0 }}</span></span>
            <UFieldGroup size="xs">
              <UButton label="Side by side" :variant="inlineDiff ? 'ghost' : 'soft'" color="neutral" data-editor-side-by-side @click="inlineDiff = false" />
              <UButton label="Inline" :variant="inlineDiff ? 'soft' : 'ghost'" color="neutral" data-editor-inline @click="inlineDiff = true" />
            </UFieldGroup>
            <UButton label="Open file" icon="i-lucide-file" size="xs" color="neutral" variant="ghost" data-editor-open-file @click="openFileOfDiff" />
          </template>
          <UBadge v-if="view?.header?.kind === 'file' && view.state !== 'text'" :label="view.dims || fmtSize(view.header.size)" color="neutral" variant="subtle" size="sm" />
          <UBadge v-if="view?.header?.truncated" label="truncated" color="warning" variant="subtle" size="sm" />
          <span v-if="view?.state === 'text' && !previewing" class="flex-none font-mono text-[11px] text-muted" data-editor-pos>Ln {{ pos.line }}, Col {{ pos.col }}</span>
          <UBadge v-if="readOnlyBadge || (view?.state === 'text' && active.kind === 'file' && !editable && !nvimOn)" label="Read only" icon="i-lucide-lock" color="neutral" variant="subtle" size="sm" :title="fileReadOnly || undefined" data-editor-readonly />
          <span v-if="savedFlash" class="flex-none text-[11px] text-success" data-editor-saved>Saved</span>
          <UTooltip v-if="editable" text="Save (Ctrl+S)">
            <UButton label="Save" icon="i-lucide-save" size="xs" :color="active && dirty.has(active.id) ? 'primary' : 'neutral'" :variant="active && dirty.has(active.id) ? 'soft' : 'ghost'" :disabled="!active || !dirty.has(active.id)" :loading="saving" data-editor-save @click="save()" />
          </UTooltip>
          <UTooltip v-if="previewable" :text="previewing ? 'The source (Alt+P)' : 'Preview (Alt+P)'">
            <UButton :label="previewing ? 'Source' : 'Preview'" :icon="previewing ? 'i-lucide-code' : 'i-lucide-eye'" size="xs" :color="previewing ? 'primary' : 'neutral'" :variant="previewing ? 'soft' : 'ghost'" :data-editor-preview="previewing ? 'on' : 'off'" @click="togglePreviewOf" />
          </UTooltip>
          <UButton icon="i-lucide-copy" size="xs" color="neutral" variant="ghost" aria-label="Copy path" data-editor-copy @click="copy(active.path, 'Path copied')" />
          <UButton v-if="rawHref" icon="i-lucide-file-output" size="xs" color="neutral" variant="ghost" aria-label="Open raw" :to="rawHref" target="_blank" rel="noopener noreferrer" data-editor-raw />
        </template>
      </div>
      <!-- Neovim's status line (design round 12, F8): the mode, the command line as typed, the last message. -->
      <div v-if="keymap === 'nvim' && active?.kind === 'file' && view?.state === 'text'" class="flex h-7 flex-none items-center gap-3 overflow-hidden border-b border-default bg-elevated/40 px-3 font-mono text-[11px]" data-nvim-status :data-nvim-mode="nvimOn ? (nvimState?.mode ?? '') : 'off'">
        <template v-if="nvimOn">
          <span class="flex-none font-semibold text-highlighted" data-nvim-mode-words>{{ modeWords(nvimState?.mode ?? '') }}</span>
          <span v-if="nvimState?.cmdline" class="flex-none text-highlighted" data-nvim-cmdline>{{ nvimState.cmdline }}</span>
          <span v-if="nvimState?.message" class="min-w-0 flex-1 truncate" :class="nvimState.messageKind === 'emsg' ? 'text-error' : 'text-muted'" data-nvim-message>{{ nvimState.message }}</span>
          <span v-if="nvimState?.refused" class="flex flex-none items-center gap-1 text-error" data-nvim-refused
            >Not in the file: <code class="font-mono">{{ nvimState.refused }}</code>
            <UButton label="Dismiss" size="xs" color="neutral" variant="ghost" data-nvim-refused-dismiss @click="dismissRefused"
          /></span>
          <span v-if="!nvimState?.cmdline && !nvimState?.message && !modeWords(nvimState?.mode ?? '')" class="text-muted">Neovim · :w writes on {{ nvim?.offer.machine || 'the machine' }} · :q closes the tab</span>
        </template>
        <span v-else class="flex items-center gap-1.5 text-warning" data-editor-keymap-note><UIcon name="i-lucide-info" class="size-3.5" />{{ nvimWhy }} · Monaco's keys meanwhile</span>
      </div>
      <!-- Another editor's swap file (round 13, G3): the file opened read-only under the Neovim keymap; whose it is, and what applies. -->
      <div v-if="nvimOn && nvimState?.swap && active?.kind === 'file'" class="flex flex-none flex-wrap items-center gap-2 border-b border-default bg-warning/10 px-3 py-1.5 text-xs" data-editor-swap :data-swap-running="nvimState.swap.running">
        <UIcon name="i-lucide-file-lock" class="size-3.5 flex-none text-warning" />
        <span class="min-w-0 flex-1 text-highlighted" :title="nvimState.swap.file">{{ swapWords(nvimState.swap, nvimState.recovered) }}</span>
        <UButton v-for="c in swapChoices(nvimState.swap, nvimState.recovered)" :key="c.choice" :label="c.label" size="xs" :color="c.choice === 'delete' ? 'warning' : 'neutral'" variant="soft" :data-swap-choice="c.choice" @click="answerSwap(c.choice)" />
      </div>
      <!-- Neovim lost, not quit (utils/nvimLost): the tab keeps the text it last held, read only; Reopen starts it again. Not while the
           session's machine is away: no editor is up then, and the banner comes back with it. -->
      <div v-if="nvimOn && nvimState?.lost && active?.kind === 'file' && !hostAway" class="flex flex-none flex-wrap items-center gap-2 border-b border-default bg-error/10 px-3 py-1.5 text-xs" data-nvim-lost>
        <UIcon name="i-lucide-circle-x" class="size-3.5 flex-none text-error" />
        <span class="min-w-0 flex-1 text-highlighted">{{ lostWords(nvimState.lost) }}<template v-if="nvimState.unconfirmed"> Keys typed that Neovim may never have had: <code class="rounded bg-elevated px-1 font-mono" data-nvim-unconfirmed>{{ nvimState.unconfirmed }}</code></template></span>
        <UButton label="Copy the text" icon="i-lucide-copy" size="xs" color="neutral" variant="soft" data-nvim-lost-copy @click="copyLostText" />
        <UButton label="Reopen" icon="i-lucide-rotate-ccw" size="xs" color="neutral" variant="soft" :disabled="!nvim?.offer.connected" :title="nvim?.offer.connected ? undefined : 'Reconnect first'" data-nvim-reopen @click="reopenNvim" />
      </div>
      <!-- Neovim asks (:confirm q, a write over a file changed outside): the question and its choices as buttons. -->
      <div v-if="nvimConfirm" class="flex flex-none flex-wrap items-center gap-2 border-b border-default bg-elevated px-3 py-1.5 text-xs" data-nvim-confirm>
        <UIcon name="i-lucide-message-circle-question" class="size-3.5 flex-none text-primary" />
        <span class="min-w-0 flex-1 text-highlighted">{{ nvimConfirm.question }}</span>
        <UButton v-for="c in nvimConfirm.choices" :key="c.key" :label="c.label" size="xs" :color="c.default ? 'primary' : 'neutral'" variant="soft" :data-nvim-confirm-choice="c.key" @click="answerConfirm(c.key)" />
      </div>
      <!-- The file changed on disk since it was read (F6): what changed it, and the three ways on. -->
      <div v-if="activeConflict" class="flex flex-none flex-wrap items-center gap-2 border-b border-default bg-warning/10 px-3 py-1.5 text-xs" data-editor-conflict>
        <UIcon name="i-lucide-triangle-alert" class="size-3.5 flex-none text-warning" />
        <span class="min-w-0 flex-1 text-highlighted">{{ conflictWords(activeConflict.header) }}</span>
        <UButton label="Compare" size="xs" color="neutral" variant="soft" data-editor-compare @click="openCompare" />
        <UButton label="Reload" size="xs" color="neutral" variant="soft" data-editor-reload @click="reloadFromDisk" />
        <UButton label="Save anyway" size="xs" color="warning" variant="soft" :loading="saving" data-editor-save-anyway @click="save(true)" />
      </div>
      <div v-if="saveBusy" class="flex flex-none items-center gap-2 border-b border-default bg-elevated px-3 py-1.5 text-xs" data-editor-save-busy>
        <UIcon name="i-lucide-loader-circle" class="size-3.5 flex-none animate-spin text-muted" /><span class="min-w-0 flex-1 text-muted">A save is still on its way: save again once it is answered to save what you changed since.</span>
      </div>
      <div v-if="saveSlow" class="flex flex-none items-center gap-2 border-b border-default bg-elevated px-3 py-1.5 text-xs" data-editor-save-slow>
        <UIcon name="i-lucide-loader-circle" class="size-3.5 flex-none animate-spin text-muted" /><span class="min-w-0 flex-1 text-muted">Still saving: no answer after 20 seconds. The save goes on, and its answer shows here.</span>
      </div>
      <div v-if="saveError && !activeConflict" class="flex flex-none items-center gap-2 border-b border-default bg-error/10 px-3 py-1.5 text-xs" data-editor-save-error>
        <UIcon name="i-lucide-circle-x" class="size-3.5 flex-none text-error" /><span class="min-w-0 flex-1">Not saved: {{ saveError }}</span>
        <UButton icon="i-lucide-x" size="xs" color="neutral" variant="ghost" aria-label="Dismiss" @click="saveError = ''" />
      </div>
      <div v-if="activeCompare" class="flex flex-none items-center gap-2 border-b border-default px-3 py-1.5 text-xs" data-editor-comparing>
        <span class="min-w-0 flex-1 text-muted">On disk, left · yours, right</span>
        <UButton label="Back to editing" size="xs" color="neutral" variant="ghost" @click="compare = null" />
      </div>
      <!-- The body. -->
      <div class="relative min-h-0 flex-1" :data-editor-state="hostAway ? 'host-away' : (view?.state ?? 'loading')">
        <div v-if="hostAway" class="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm">
          <UIcon name="i-lucide-laptop" class="size-6 text-muted" />
          <span class="font-medium text-highlighted">{{ hostAway }} is away</span>
          <span class="max-w-md text-muted">Files, changes and saves come from that machine. They return when it does; the terminal's scrollback stays.</span>
        </div>
        <div v-else-if="!view || view.state === 'loading'" class="flex items-center gap-2 p-6 text-sm text-muted"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Loading…</div>
        <iframe v-else-if="view.state === 'url'" :src="active!.path" class="h-full w-full bg-white" sandbox="allow-scripts allow-same-origin allow-forms allow-popups" referrerpolicy="no-referrer" title="URL preview" />
        <DiffEditor v-else-if="activeCompare" :key="`${active!.id}:compare`" :path="active!.path" :original="activeCompare.disk" :modified="activeCompare.mine" :inline="false" data-editor-compare-view />
        <template v-else-if="view.state === 'text'">
          <MarkdownPreview v-if="previewing" ref="previewRef" :key="`${active!.id}:preview`" :text="previewText" :dir="dirOf(active!.path)" :raw-url="rawUrl" @open="tabs = openTab(tabs, 'file', $event)" />
          <CodeEditor v-show="!previewing" ref="editorRef" :key="`${active!.id}:${nvimOn ? 'nvim' : 'keys'}`" :path="active!.path" :text="view.text ?? ''" :line="active!.line" :read-only="!editable" :nvim="nvimOn ? nvim : undefined" @cursor="pos = $event" @ready="onReady" @nvim="nvimState = $event" @nvim-closed="closeActive" @dirty="setDirty(active!.id, $event)" @selection="onSelection" @preview="togglePreviewOf" />
        </template>
        <DiffEditor v-else-if="view.state === 'diff'" :key="active!.id" :path="active!.path" :original="view.original ?? ''" :modified="view.modified ?? ''" :inline="inlineDiff" />
        <div v-else-if="view.state === 'image'" class="flex h-full items-center justify-center overflow-auto p-4 [background-image:linear-gradient(45deg,var(--ui-bg-elevated)_25%,transparent_25%),linear-gradient(-45deg,var(--ui-bg-elevated)_25%,transparent_25%),linear-gradient(45deg,transparent_75%,var(--ui-bg-elevated)_75%),linear-gradient(-45deg,transparent_75%,var(--ui-bg-elevated)_75%)] [background-size:16px_16px] [background-position:0_0,0_8px,8px_-8px,-8px_0]">
          <img :src="view.imageSrc" alt="" class="max-h-full max-w-full" />
        </div>
        <div v-else-if="view.state === 'binary'" class="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm">
          <UIcon name="i-lucide-file-digit" class="size-6 text-muted" />
          <span class="font-medium text-highlighted">Binary file; nothing to show</span>
          <span class="text-muted">{{ fmtSize(view.header?.size) }}. Open raw downloads it.</span>
          <UButton v-if="rawHref" label="Open raw" icon="i-lucide-file-output" size="xs" color="neutral" variant="soft" :to="rawHref" target="_blank" rel="noopener noreferrer" />
        </div>
        <div v-else-if="view.state === 'refused'" class="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm" data-editor-refused>
          <UIcon :name="refused(view.error || '').icon" class="size-6 text-muted" />
          <span class="font-medium text-highlighted">{{ refused(view.error || '').title }}</span>
          <span class="max-w-md text-muted">{{ refused(view.error || '').words }}</span>
        </div>
        <UAlert v-else color="error" variant="subtle" icon="i-lucide-triangle-alert" class="m-3" :title="view.error || 'Cannot read this path'" />
        <!-- Over a selection (F7): Comment, Ask the agent, Copy; the composer under the lines. -->
        <div v-if="commentable && selection && !composer && !activeCompare" class="absolute z-10 flex items-center gap-0.5 rounded-md border border-default bg-default p-0.5 shadow-md" :style="{ top: `${selection.top + 4}px`, left: `${Math.max(8, selection.left)}px` }" data-editor-selection-bar>
          <UButton label="Comment" icon="i-lucide-message-square-plus" size="xs" color="neutral" variant="ghost" data-editor-comment @mousedown.prevent @click="openComposer(false)" />
          <UButton v-if="canAsk" label="Ask the agent" icon="i-lucide-bot" size="xs" color="neutral" variant="ghost" data-editor-ask @mousedown.prevent @click="openComposer(true)" />
          <UButton label="Copy" icon="i-lucide-copy" size="xs" color="neutral" variant="ghost" data-editor-copy-lines @mousedown.prevent @click="copyLines" />
        </div>
        <!-- The box takes the editor's width less a margin, capped where it still reads as a box over the code (#168); its heading and
             the file's location show whole, wrapping when the editor is narrow. -->
        <div v-if="composer" class="absolute left-2 right-2 z-10 flex max-w-4xl flex-col gap-2 rounded-lg border border-default bg-default p-3 shadow-lg" :style="{ top: `${composer.top + 4}px` }" data-editor-composer :data-editor-composer-mode="composer.ask ? 'ask' : 'comment'">
          <div class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-xs">
            <UIcon :name="composer.ask ? 'i-lucide-bot' : 'i-lucide-message-square-plus'" class="size-3.5 flex-none self-center text-muted" />
            <span class="min-w-0 font-medium text-highlighted" data-editor-composer-heading>{{ composer.ask ? 'Ask the agent about' : 'Comment on' }} {{ rangeWords(composer.quote.from, composer.quote.to) }}</span>
            <span class="min-w-0 break-all font-mono text-[11px] text-muted" data-editor-composer-location>{{ quoteLocation(composer.quote) }}</span>
          </div>
          <pre class="max-h-24 overflow-auto rounded border border-default bg-elevated/40 px-2 py-1 font-mono text-[11px] leading-snug" data-editor-composer-quote><template v-for="(l, i) in composer.quote.lines" :key="i"><span class="select-none text-muted">{{ String(composer.quote.from + i).padStart(3, ' ') }}  </span>{{ l }}
</template></pre>
          <UTextarea v-model="composer.text" :rows="2" autoresize :maxrows="6" :placeholder="composer.ask ? 'What should the agent do with these lines?' : 'Say something about these lines'" aria-label="Comment" @keydown="onComposerKey" />
          <div class="flex items-center justify-end gap-1.5">
            <span class="mr-auto text-[11px] text-muted">Enter sends · Esc closes</span>
            <UButton label="Comment" size="xs" :color="composer.ask ? 'neutral' : 'primary'" :variant="composer.ask ? 'ghost' : 'solid'" data-editor-composer-send @click="sendComment(false)" />
            <UButton v-if="canAsk" label="Ask the agent" icon="i-lucide-bot" size="xs" :color="composer.ask ? 'primary' : 'neutral'" :variant="composer.ask ? 'solid' : 'ghost'" data-editor-composer-ask @click="sendComment(true)" />
          </div>
        </div>
      </div>
    </template>
    <UModal :open="!!closing" :title="closingWords.title" :description="closingWords.words" :dismissible="!saving" data-editor-close-prompt @update:open="(o) => !o && (closing = null)">
      <template v-if="nvimState?.refused" #body>
        <p class="text-sm text-error" data-editor-close-refused>Keys did not reach Neovim and are not in the file: <code class="font-mono">{{ nvimState.refused }}</code>. Type them again, or dismiss them on the status line, then save; or don't save.</p>
      </template>
      <template #footer>
        <div class="flex w-full justify-end gap-2">
          <UButton label="Cancel" color="neutral" variant="ghost" @click="closing = null" />
          <UButton label="Don't save" color="error" variant="soft" data-editor-discard @click="closingDiscard" />
          <UButton v-if="closingOne && !nvimState?.lost" label="Save" color="primary" :loading="saving" data-editor-close-save @click="closingSave" />
        </div>
      </template>
    </UModal>
  </div>
</template>
