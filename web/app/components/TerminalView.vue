<script setup lang="ts">
import { Terminal, type ILink, type ILinkProvider } from '@xterm/xterm'
import { liveAttention } from '~/utils/attention'
import { FitAddon } from '@xterm/addon-fit'
import { showsScaled, sizerChip as chipFor, sizesSession, type SizerView } from '~/utils/terminalSizer'
import { clipboardKey, forcesSelection, menuPress, pressClearsSelection, readRightClickPastes, rightClick, RightPress, withholdsMove, writeRightClickPastes } from '~/utils/terminalClipboard'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { WebglAddon } from '@xterm/addon-webgl'
import { findFileLocations } from '~/utils/links'
import { ALT_PASSTHROUGH_CODES, usePageAltCodes } from '~/composables/useShortcuts'
import { NEWLINE_IN_PROMPT, newlineChord } from '~/utils/terminalKeys'
import { closeReason, encodeText, FOLLOW_SIZE, type ActivityEntry, type ChatHistory, type ChatMessage, type ChatPost, type ChatRoster, type ChatSend, type ControlMessage, type FileGetExtra, type FileResponse, type FileWriteOptions, type NvimSwapChoice, type Role, type TransportKind, type ViewerInfo, type Welcome } from '~/utils/protocol'
import type { CloseInfo, TerminalTransport, TransportState } from '~/utils/transport/types'
import type { LaneWire } from '~/utils/transport/decide'
import { ReplyError, type QuestionReply } from '~/utils/reply'
import { FIT_DEBOUNCE_MS, helloSize, tileFit, tileScale } from '~/utils/tile'

const props = withDefaults(
  defineProps<{
    /** Creates a fresh transport for each (re)connection. */
    createTransport: () => TerminalTransport
    /** No input: keys and pastes reach nothing. How the terminal is sized is the `fit` prop's alone. */
    readOnly?: boolean
    /** Connect on mount. Vue casts absent booleans to false, hence the explicit default. */
    autoConnect?: boolean
    fontSize?: number
    /** Rows of history; never none (TILE_SCROLLBACK says why), so 0 keeps one. */
    scrollback?: number
    /** Tile mode: no frame, overlays or notices, just the terminal. */
    compact?: boolean
    /**
     * How the terminal meets its pane. `fill` (default, the full view) fits
     * the pane and, with control, sizes the session to it. `tile` does the
     * same for a wall or crew tile, at the font size given and with no
     * gutter kept for a scrollbar (tileFit), and scales the screen
     * down to show it whole while another viewer has made the session larger
     * than the tile. `scale` keeps the session's cols × rows and scales the
     * whole terminal into the pane: a view-only tile; its hello asks for no
     * size (0 × 0, follow the session).
     */
    fit?: 'fill' | 'scale' | 'tile'
    /** Focus the terminal once connected (controllers only). */
    autoFocus?: boolean
  }>(),
  { readOnly: false, autoConnect: true, fontSize: 13, scrollback: 5000, compact: false, fit: 'fill', autoFocus: true },
)

const emit = defineEmits<{
  welcome: [welcome: Welcome]
  status: [status: string, exitCode?: number]
  attention: [msg: { state: string; message?: string; source?: string; kind?: string; options?: Array<{ label: string; input: string }>; id?: string }]
  viewers: [info: { count: number; list?: ViewerInfo[] }]
  activity: [entry: ActivityEntry]
  transport: [info: { kind: TransportKind; state: TransportState; rtt: number | null }]
  closed: [info: CloseInfo]
  openFile: [loc: { path: string; line?: number }]
  openUrl: [url: string]
  /** A chat message, live; the kept chat on each welcome; an error the owner sent about one of this client's posts or sends. */
  chat: [msg: ChatMessage]
  chatHistory: [history: ChatHistory]
  chatRoster: [roster: ChatRoster]
  requestError: [err: { code: string; message: string; requestId: string }]
  /** What the editor's Neovim reports (design round 12, F8). */
  nvim: [ev: NvimEvent]
  /** A decision link's connection: the scope's items changed (the lane's `decisions_changed`; the join page's Decide tab lists again). */
  decisionsChanged: []
}>()

const host = ref<HTMLDivElement>()
/** The renderer xterm draws with: webgl, or dom when WebGL is not available. */
const renderer = ref<'webgl' | 'dom' | ''>('')
const overlay = ref<{ title: string; detail?: string } | null>(null)
const connecting = ref(false)
const fileView = ref(false)
const notice = ref('')
/** The role the server gave this connection: a view-only one never sizes the session, whatever the page asked for. */
const role = ref<Role | ''>('')
// Who sizes the session (round 14). An owner whose welcome says `sizer` sizes it by one viewer: this view fills its pane and sizes the
// session only while it holds the size; otherwise it shows the session's grid scaled to its pane, and a control viewer gets Fit to my
// window to take the size. An older owner (no `sizer`) takes every controller's size, as before.
const sizerKnown = ref(false)
const sizedBy = ref('')
const subscriberId = ref('')
const roster = ref<ViewerInfo[]>([])
const sessionSize = ref({ cols: 0, rows: 0 })
// grid is the terminal as drawn (xterm's own columns and rows), on the host as data-cols and data-rows: the tests read a cell's
// place from it beside the host's box, since the session's size (the API's) can lag a fit the page just made.
const grid = ref({ cols: 0, rows: 0 })
// The fits this view has made after its pane changed (fitAndResize) and the resizes they asked the session for, on the host as
// data-fit-passes and data-fit-resizes: a test that proves no resize came of a change (the terminal collapsed, #432) waits for the
// pass the change scheduled, past the debounce, and reads both at once.
const fitPasses = ref(0)
const fitResizes = ref(0)
const welcomed = ref(false)
function sizerView(): SizerView {
  return { fit: props.fit, mayFit: mayFit(), welcomed: welcomed.value, sizer: sizerKnown.value, sizedBy: sizedBy.value, me: subscriberId.value, compact: props.compact, ended: ended.value, roster: roster.value, cols: sessionSize.value.cols, rows: sessionSize.value.rows }
}
// Copy and paste (round 15, utils/terminalClipboard.ts): the keys, right-click and the terminal's menu.
const isMac = import.meta.client && /Mac|iPhone|iPad/.test(navigator.platform)
/** The Alt chords the page this terminal is on takes beside the global ones (the Yard's and the Roundhouse's Alt+Q, #312). */
const pageAltCodes = usePageAltCodes()
const hasSelection = ref(false)
const rightClickPastes = ref(true)
/** Whether this connection may type, and so paste. */
function canPaste(): boolean {
  return !props.readOnly && role.value !== 'view'
}
/** Puts text on the clipboard: the async API where the page may use it, else a hidden text area within the gesture (a plain-HTTP origin). */
async function writeClipboard(text: string): Promise<void> {
  if (navigator.clipboard?.writeText && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return
    } catch {
      /* the fallback below */
    }
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  try {
    document.execCommand('copy')
  } finally {
    ta.remove()
    term?.focus()
  }
}
/** Copies the selection, and with clear clears it. */
function copySelection(clear: boolean): void {
  const text = term?.getSelection() ?? ''
  if (!text) return
  void writeClipboard(text)
  if (clear) term?.clearSelection()
}
/** Pastes the clipboard as typed text (bracketed when the program asked), where the page may read it; else says how. */
async function pasteClipboard(): Promise<void> {
  if (!canPaste() || !term) return
  try {
    const text = await navigator.clipboard.readText()
    if (text) term.paste(text)
    term.focus()
  } catch {
    notice.value = 'This page cannot read the clipboard: paste with Ctrl+Shift+V'
  }
}
/** Whether the program running asked for mouse reports. */
function appHoldsMouse(): boolean {
  return !!term && term.modes.mouseTrackingMode !== 'none'
}
/** A right-click is decided by what its press held, not by the moment the menu event comes (RightPress). */
const rightPress = new RightPress()
/**
 * Every press on the page, so one outside the terminal forgets a right press here that brought no menu. A press in the terminal that is
 * the program's clears the selection (pressClearsSelection), where xterm would leave it.
 */
function onAnyMouseDown(e: MouseEvent) {
  const inTerminal = !!host.value?.contains(e.target as Node)
  if (inTerminal && term?.hasSelection() && pressClearsSelection(e, { tracking: term.modes.mouseTrackingMode, mac: isMac })) term.clearSelection()
  if (inTerminal && menuPress(e, isMac)) rightPress.press({ appMouse: appHoldsMouse(), force: forcesSelection(e, isMac) })
  else rightPress.clear()
}
/** The keyboard's menu key, noted as it goes down: its menu event is the keyboard's whatever the browser marks on it. */
function onAnyKeyDown(e: KeyboardEvent) {
  if (e.key === 'ContextMenu') rightPress.menuKey()
}
function onContextMenu(e: MouseEvent) {
  const held = rightPress.take(e as PointerEvent)
  if (held.keyboard) return // the terminal's menu opens
  const force = held.force || forcesSelection(e, isMac)
  const act = rightClick({ hasSelection: !!term?.hasSelection(), canPaste: canPaste(), pastes: rightClickPastes.value, shift: e.shiftKey, appMouse: held.appMouse, force })
  if (act === 'menu') return // the terminal's menu opens
  e.preventDefault()
  e.stopImmediatePropagation()
  if (act === 'app') return // xterm has reported the click to the program
  if (act === 'copy') copySelection(true)
  else void pasteClipboard()
}
/** xterm's screen, where its own listeners find the link under the pointer and set the cursor's style on a move. */
let screenEl: HTMLElement | null = null
/** Whether xterm's report of this move is kept from the program (withholdsMove). */
function withheld(e: MouseEvent): boolean {
  return !!term && withholdsMove(e, { tracking: term.modes.mouseTrackingMode, hasSelection: term.hasSelection(), mac: isMac })
}
/**
 * A move xterm would report to a program that asks for every move is kept from it while Shift is held or a selection shows: xterm takes
 * the report for typing and clears the selection. xterm reports a move from its element. Over the screen the move stops on its way up
 * there, after xterm's listeners on the screen have seen it (they were added first); elsewhere in the terminal (its padding) no listener
 * of xterm's needs it, and it stops on its way down, at the host. A drag (a button held) is never stopped: xterm's selection follows it
 * from the document.
 */
function onScreenMouseMove(e: MouseEvent) {
  if (withheld(e)) e.stopPropagation()
}
function onHostMouseMove(e: MouseEvent) {
  const target = e.target as Node
  if (term?.element?.contains(target) && !screenEl?.contains(target) && withheld(e)) e.stopPropagation()
}
function setRightClickPastes(on: boolean) {
  rightClickPastes.value = on
  writeRightClickPastes(typeof localStorage === 'undefined' ? null : localStorage, on)
}
const terminalMenu = computed(() => [
  [
    { label: 'Copy', icon: 'i-lucide-copy', kbds: isMac ? ['meta', 'c'] : ['ctrl', 'shift', 'c'], disabled: !hasSelection.value, onSelect: () => copySelection(true) },
    { label: 'Paste', icon: 'i-lucide-clipboard-paste', kbds: isMac ? ['meta', 'v'] : ['ctrl', 'shift', 'v'], disabled: !canPaste(), onSelect: () => void pasteClipboard() },
    { label: 'Select all', icon: 'i-lucide-text-select', onSelect: () => term?.selectAll() },
  ],
  [{ label: 'Right-click pastes', type: 'checkbox' as const, checked: rightClickPastes.value, onUpdateChecked: (on: boolean) => setRightClickPastes(on) }],
])

/** The pane shows the session's grid scaled (a scale view, or a full view that does not size the session). */
const scaledView = ref(props.fit === 'scale')
/** Who sizes it and Fit to my window: on a full view of a controller that does not hold the size. */
const sizerChip = computed(() => chipFor(sizerView()))
/** The size the last hello carried: 0 × 0 when this view did not size the session as it connected. */
let helloSent: { cols: number; rows: number } = { ...FOLLOW_SIZE }
/** True once the process is gone: the cursor is hidden and stops blinking. */
const ended = ref(false)
let endedAtWelcome = false

let term: Terminal | undefined
let fit: FitAddon | undefined
let transport: TerminalTransport | undefined
let observer: ResizeObserver | undefined
let resizeTimer: number | undefined
let pingTimer: number | undefined
let stopWatch: (() => void) | undefined
let stopTheme: (() => void) | undefined
const existsCache = new Map<string, Promise<boolean>>()

// Colours follow the workbench palette in both colour modes (see useTerminalTheme).
const theme = useTerminalTheme()

function pathExists(path: string): Promise<boolean> {
  if (!transport || transport.state.value !== 'open' || !fileView.value) return Promise.resolve(false)
  let p = existsCache.get(path)
  if (!p) {
    p = transport
      .requestFile(path, true)
      .then((r) => r.header.exists)
      .catch(() => false)
    existsCache.set(path, p)
    if (existsCache.size > 2000) existsCache.clear()
  }
  return p
}

/** Underlines file locations that the session owner confirms exist. */
const fileLinkProvider: ILinkProvider = {
  provideLinks(y, callback) {
    const line = term?.buffer.active.getLine(y - 1)
    if (!line) return callback(undefined)
    const text = line.translateToString(true)
    const locs = findFileLocations(text)
    if (!locs.length) return callback(undefined)
    Promise.all(locs.map((l) => pathExists(l.path).then((ok) => (ok ? l : null)))).then((checked) => {
      const links: ILink[] = []
      for (const loc of checked) {
        if (!loc) continue
        links.push({
          range: { start: { x: loc.start + 1, y }, end: { x: loc.end, y } },
          text: text.slice(loc.start, loc.end),
          decorations: { underline: true, pointerCursor: true },
          activate: () => emit('openFile', { path: loc.path, line: loc.line }),
        })
      }
      callback(links.length ? links : undefined)
    })
  },
}

/**
 * Whether this view may size the session: a full view or a tile that takes input, unless the server said this connection may only
 * watch. A scaled or read-only view follows the session's size instead.
 */
function mayFit(): boolean {
  return props.fit !== 'scale' && !props.readOnly && role.value !== 'view'
}

/** Whether this view sizes the session now: it may, and it holds the size (round 14). */
function sizes(): boolean {
  return sizesSession(sizerView())
}

/** Whether the pane shows the session's grid scaled: a scale view, or once welcomed a full view that does not size the session. */
function scaled(): boolean {
  return showsScaled(sizerView())
}

/** What the fit addon reads of xterm.js's render service, which xterm.js keeps private: the cell's size in CSS pixels. */
type RenderService = { dimensions: { css: { cell: { width: number; height: number } } }; clear(): void }

/**
 * Fits the terminal to its pane: a full view with the fit addon; a tile as the addon does, but with no gutter kept for the scrollbar
 * every pane hides (tileFit), now that a tile keeps a scrollback.
 */
function fitPane() {
  if (!term || !fit) return
  if (props.fit !== 'tile') return fit.fit()
  const el = term.element
  const pane = el?.parentElement
  const render = (term as unknown as { _core?: { _renderService?: RenderService } })._core?._renderService
  const cell = render?.dimensions?.css?.cell
  if (!el || !pane || !render || !cell) return
  const ps = getComputedStyle(pane)
  const es = getComputedStyle(el)
  const px = (v: string) => Number.parseInt(v) || 0
  const size = tileFit(
    { width: px(ps.width), height: px(ps.height) },
    { width: px(es.paddingLeft) + px(es.paddingRight), height: px(es.paddingTop) + px(es.paddingBottom) },
    cell,
  )
  if (!size || (size.cols === term.cols && size.rows === term.rows)) return
  // As the addon does: the whole screen is drawn again at the new size.
  render.clear()
  term.resize(size.cols, size.rows)
}

/**
 * Fits the terminal to its pane when this view sizes the session (or, asking, may: the hello says the size its window would give the
 * session, which the owner decides on), and returns the size to ask for: 0 × 0 (follow) otherwise, or before the pane is laid out.
 */
function measure(asking = false): { cols: number; rows: number } {
  const h = host.value
  if (!(asking ? mayFit() : sizes()) || !term || !fit || !h?.clientWidth || !h.clientHeight) return helloSize(false, 0, 0)
  fitPane()
  return helloSize(true, term.cols, term.rows)
}

/**
 * Fits and resizes the session once this view's own pane has stopped changing size (FIT_DEBOUNCE_MS), as it becomes visible again, or once
 * the font has loaded. Never in answer to another viewer's resize: two views of one session take turns only when one of them attaches or
 * its pane changes (latest controller wins), never back and forth.
 */
function scheduleResize() {
  if (!sizes()) return
  window.clearTimeout(resizeTimer)
  resizeTimer = window.setTimeout(() => fitAndResize(2), FIT_DEBOUNCE_MS)
}

/**
 * Fits, resizes the session, and checks a frame later that the screen fits its pane: xterm may measure its cells again as it renders the
 * new size (the bundled font settling in), and then the fit is done again, `retries` times at most.
 */
function fitAndResize(retries: number) {
  fitPasses.value++
  const { cols, rows } = measure()
  if (cols && transport) {
    fitResizes.value++
    transport.resize(cols, rows)
  }
  window.requestAnimationFrame(() => {
    if (retries > 0 && cols && overflows()) fitAndResize(retries - 1)
    else scheduleTileScale()
  })
}

/** Whether the terminal's screen, with its padding, is wider or taller than its pane. */
function overflows(): boolean {
  const el = term?.element
  const h = host.value
  const screen = el?.querySelector<HTMLElement>('.xterm-screen')
  if (!el || !h || !screen) return false
  const style = getComputedStyle(el)
  const padX = Number.parseFloat(style.paddingLeft) + Number.parseFloat(style.paddingRight)
  const padY = Number.parseFloat(style.paddingTop) + Number.parseFloat(style.paddingBottom)
  return screen.offsetWidth + padX > h.clientWidth || screen.offsetHeight + padY > h.clientHeight
}

function onVisibility() {
  if (document.visibilityState === 'visible') scheduleResize()
}

// Tile mode's safety net: while another viewer has made the session larger
// than the tile, the screen is scaled down to show it whole; at the tile's own
// size it is not scaled at all.
let tileFrame: number | undefined
function applyTileScale() {
  const el = term?.element
  const h = host.value
  if (props.fit !== 'tile' || !el || !h) return
  const screen = el.querySelector<HTMLElement>('.xterm-screen')
  if (!screen) return
  const style = getComputedStyle(el)
  const padX = Number.parseFloat(style.paddingLeft) + Number.parseFloat(style.paddingRight)
  const padY = Number.parseFloat(style.paddingTop) + Number.parseFloat(style.paddingBottom)
  const s = tileScale({ width: h.clientWidth, height: h.clientHeight }, { width: screen.offsetWidth + padX, height: screen.offsetHeight + padY })
  el.style.transform = s < 1 ? `scale(${s})` : ''
}

function scheduleTileScale() {
  if (props.fit !== 'tile' || tileFrame !== undefined) return
  tileFrame = window.requestAnimationFrame(() => {
    tileFrame = undefined
    applyTileScale()
  })
}

// Scale mode: pick a font size that roughly fills the pane, then apply the
// exact remaining factor as a CSS transform so the whole screen stays visible.
const MIN_FONT = 5
const MAX_FONT = 28
let scaleFrame: number | undefined
let fontAdjustments = 0

function applyScale() {
  const el = term?.element
  const h = host.value
  if (!term || !el || !h || !scaledView.value) return
  const natW = el.offsetWidth
  const natH = el.offsetHeight
  const hw = h.clientWidth
  const hh = h.clientHeight
  if (!natW || !natH || !hw || !hh) return
  const k = Math.min(hw / natW, hh / natH)
  const font = term.options.fontSize ?? props.fontSize
  const want = Math.max(MIN_FONT, Math.min(MAX_FONT, Math.round(font * k)))
  if (want !== font && fontAdjustments < 6) {
    fontAdjustments++
    term.options.fontSize = want // the element resizes; the observer calls applyScale again
    return
  }
  const s = Math.min(k, 1)
  const tx = Math.round((hw - natW * s) / 2)
  const ty = Math.round((hh - natH * s) / 2)
  el.style.transform = `translate(${tx}px, ${ty}px) scale(${s})`
}

/**
 * Switches the pane between filling it (this view sizes the session) and showing the session's grid scaled (it does not), as the
 * welcome and every resize say who sizes it.
 */
function applyFitMode() {
  const want = scaled()
  const el = term?.element
  if (want === scaledView.value) {
    if (want) scheduleScale()
    return
  }
  scaledView.value = want
  if (want) {
    fontAdjustments = 0
    if (el) observer?.observe(el)
    scheduleScale()
  } else {
    if (el) {
      observer?.unobserve(el)
      el.style.transform = ''
    }
    if (term) term.options.fontSize = props.fontSize
    scheduleResize()
  }
}

/**
 * Fit to my window (round 14): this window takes the session's size, at its own size. A pane with no size to give (the terminal
 * collapsed under the editor, #432 review round 2) takes nothing: fitted there it would ask a row or none of every viewer.
 */
function takeSize() {
  const h = host.value
  if (!term || !fit || !transport || !mayFit() || !h?.clientWidth || !h.clientHeight) return
  const el = term.element
  if (el) el.style.transform = ''
  term.options.fontSize = props.fontSize
  fitPane()
  transport.resize(term.cols, term.rows, true)
  sizedBy.value = subscriberId.value
  applyFitMode()
}

function scheduleScale() {
  if (scaleFrame !== undefined) return
  scaleFrame = window.requestAnimationFrame(() => {
    scaleFrame = undefined
    applyScale()
  })
}

function isEnded(status: string) {
  return status === 'exited' || status === 'stopped'
}

/**
 * A finished process has no cursor. The DECTCEM hide sequence goes through
 * the terminal's own write queue, so it lands after any replayed output.
 */
function markEnded() {
  ended.value = true
  if (!term) return
  term.options.cursorBlink = false
  term.write('\x1b[?25l')
}

function handleControl(msg: ControlMessage) {
  switch (msg.t) {
    case 'welcome':
      role.value = msg.role
      fileView.value = msg.fileView
      subscriberId.value = msg.subscriberId ?? ''
      sizerKnown.value = !!msg.sizer
      sizedBy.value = msg.sizedBy ?? ''
      sessionSize.value = { cols: msg.cols, rows: msg.rows }
      welcomed.value = true
      existsCache.clear()
      // Sessions that already ended are marked once the scrollback has replayed.
      endedAtWelcome = isEnded(msg.status)
      // A view that does not size the session shows it at its size, and so
      // does one whose pane was not laid out as it connected, until it fits.
      if (msg.cols && msg.rows && (!sizes() || !helloSent.cols)) term?.resize(msg.cols, msg.rows)
      scheduleTileScale()
      applyFitMode()
      emit('welcome', msg)
      break
    case 'ready':
      if (endedAtWelcome) markEnded()
      break
    case 'resize':
      // Another viewer's resize is followed, never answered with one of this view's own. It also says who sizes the session now.
      if (sizerKnown.value) sizedBy.value = msg.by ?? ''
      if (msg.cols && msg.rows) sessionSize.value = { cols: msg.cols, rows: msg.rows }
      if (msg.cols && msg.rows && (term?.cols !== msg.cols || term?.rows !== msg.rows)) term?.resize(msg.cols, msg.rows)
      scheduleTileScale()
      applyFitMode()
      break
    case 'status':
      emit('status', msg.status, msg.exitCode)
      if (isEnded(msg.status)) {
        notice.value = msg.status === 'exited' ? `Process exited${msg.exitCode !== undefined ? ` with code ${msg.exitCode}` : ''}` : 'Session stopped'
        markEnded()
      }
      break
    case 'attention':
      // A question answered by keys in the terminal reads as none to offer (liveAttention, #258).
      emit('attention', liveAttention({ state: msg.state, message: msg.message, source: msg.source, kind: msg.kind, options: msg.options, id: msg.id, answered: msg.answered, plan: msg.plan, decision: msg.decision }))
      break
    case 'viewers':
      roster.value = msg.list ?? []
      emit('viewers', { count: msg.count, list: msg.list })
      break
    case 'activity':
      emit('activity', { at: msg.at, type: msg.type, by: msg.by, byName: msg.byName, message: msg.message, url: msg.url, to: msg.to, tool: msg.tool, op: msg.op, path: msg.path })
      break
    case 'chat':
      emit('chat', msg)
      break
    case 'decisions_changed':
      emit('decisionsChanged')
      break
    case 'chat_history':
      emit('chatHistory', msg)
      break
    case 'chat_roster':
      emit('chatRoster', msg)
      break
    case 'nvim_event':
      emit('nvim', msg)
      break
    case 'error':
      // An error naming a request (a chat post or send) is that request's, not the terminal's.
      if (msg.requestId) {
        emit('requestError', { code: msg.code, message: msg.message, requestId: msg.requestId })
        break
      }
      if (msg.code === 'read_only') notice.value = 'This link is view-only'
      else if (msg.code !== 'bad_frame') notice.value = msg.message
      break
  }
}

async function connect() {
  if (!term || connecting.value) return
  disconnect(false)
  overlay.value = null
  notice.value = ''
  connecting.value = true
  const t = props.createTransport()
  transport = t
  stopWatch = watch([t.kind, t.state, t.rtt], ([kind, state, rtt]) => emit('transport', { kind, state, rtt }), { immediate: true })
  t.onOutput((data) => term?.write(data))
  t.onControl(handleControl)
  t.onClose((info) => {
    if (transport !== t) return
    connecting.value = false
    overlay.value = { title: info.error?.message || closeReason(info.code, info.reason), detail: info.error ? info.error.code : undefined }
    emit('closed', info)
  })
  // A fresh connection starts live again; reset() also re-shows the cursor.
  ended.value = false
  endedAtWelcome = false
  term.options.cursorBlink = !props.compact
  term.reset()
  try {
    role.value = ''
    welcomed.value = false
    sizerKnown.value = false
    // The hello says the size this window would give the session, at the font it would use: not the scaled one.
    if (props.fit === 'fill' && scaledView.value) {
      scaledView.value = false
      if (term.element) {
        observer?.unobserve(term.element)
        term.element.style.transform = ''
      }
      term.options.fontSize = props.fontSize
    }
    helloSent = measure(true)
    await t.connect(helloSent)
    t.ping()
    if (sizes()) {
      const { cols, rows } = measure()
      if (cols) t.resize(cols, rows)
    }
    // The terminal takes the focus only when nothing else has it: a person who moved to the filter, the chat or a field while it
    // connected keeps their place (the connect used to take it back, and a shortcut pressed meanwhile lost its target).
    if (props.autoFocus && !props.readOnly && focusIsFree()) term.focus()
  } catch (e) {
    if (!overlay.value) overlay.value = { title: (e as Error).message }
  } finally {
    connecting.value = false
  }
}

function disconnect(showOverlay = true) {
  stopWatch?.()
  stopWatch = undefined
  const t = transport
  transport = undefined
  t?.close()
  if (showOverlay && !overlay.value) overlay.value = { title: 'Disconnected' }
}

function writeFile(path: string, data: Uint8Array, opts?: FileWriteOptions): Promise<FileResponse> {
  if (!transport) return Promise.reject(new Error('not connected'))
  return transport.writeFile(path, data, opts)
}
function nvimOpen(path: string): Promise<NvimEvent> {
  if (!transport) return Promise.reject(new Error('not connected'))
  return transport.nvimOpen(path)
}
function nvimInput(id: string, keys: string, seq?: number): void {
  transport?.nvimInput(id, keys, seq)
}
/** Whether nothing holds the focus (the page itself, or this terminal): a terminal that connects may take it then. */
function focusIsFree(): boolean {
  if (typeof document === 'undefined') return true
  const el = document.activeElement
  return !el || el === document.body || !!(host.value && host.value.contains(el))
}
function nvimSwap(id: string, choice: NvimSwapChoice): void {
  transport?.nvimSwap(id, choice)
}
function nvimClose(id: string, discard?: boolean): void {
  transport?.nvimClose(id, discard)
}
function requestFile(path: string, stat = false, extra: FileGetExtra = {}): Promise<FileResponse> {
  if (!transport) return Promise.reject(new Error('not connected'))
  return transport.requestFile(path, stat, extra)
}

/** Sends text to the PTY as if typed; false when the transport is not open. */
function sendInput(text: string): boolean {
  // A read-only view (a thumbnail, a view link's) types nothing, whatever asks: a chord's newline or a page's call.
  if (props.readOnly || !transport || transport.state.value !== 'open') return false
  transport.sendInput(encodeText(text))
  return true
}

/** Submits a line through the session's owner (a paste, then Enter); false when the transport is not open. */
function submit(text: string): boolean {
  if (props.readOnly || !transport || transport.state.value !== 'open') return false
  transport.submit(text)
  return true
}

/**
 * Replies to the agent's question the person was shown (`question`, the attention's id): settles true once the owner took it,
 * false once sent to an older owner that says nothing, and rejects with a ReplyError when it did not go (`question_answered`: the
 * question no longer waits, nothing was sent; `not_connected`).
 */
function answer(reply: QuestionReply): Promise<boolean> {
  if (!transport || transport.state.value !== 'open') return Promise.reject(new ReplyError('not_connected', 'not connected'))
  return transport.answer(reply)
}

/**
 * The lane of a decision link's connection (utils/transport/decide.ts): this view's transport while its owner's welcome says
 * `decisions`, null otherwise (no connection, or a link that is no decision link's).
 */
function lane(): LaneWire | null {
  return transport?.decisions ? transport : null
}

/** Posts to the chat; false when the transport is not open (the caller queues it). */
function chat(post: ChatPost): boolean {
  if (!transport || transport.state.value !== 'open') return false
  transport.chat(post)
  return true
}

/** Types a kept chat message into the agent; false when the transport is not open. */
function chatSend(send: ChatSend): boolean {
  if (!transport || transport.state.value !== 'open') return false
  transport.chatSend(send)
  return true
}

/** The screen as text, a line a row (the Roundhouse's thumbnails, #310): what this view shows, from its own connection. */
function snapshot(): string[] | null {
  if (!term) return null
  const b = term.buffer.active
  const lines: string[] = []
  for (let y = 0; y < term.rows; y++) lines.push(b.getLine(b.viewportY + y)?.translateToString(true) ?? '')
  return lines
}
defineExpose({ connect, disconnect, requestFile, writeFile, nvimOpen, nvimInput, nvimClose, nvimSwap, sendInput, submit, answer, chat, chatSend, lane, focus: () => term?.focus(), scrollToBottom: () => term?.scrollToBottom(), takeSize, sizerShown: () => !!sizerChip.value, snapshot })

onMounted(() => {
  term = new Terminal({
    cursorBlink: !props.compact,
    scrollback: Math.max(1, props.scrollback),
    allowProposedApi: true,
    disableStdin: !!props.readOnly,
    fontSize: props.fontSize,
    fontFamily: '"JetBrains Mono Variable", ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
    theme: theme.value,
    convertEol: false,
    // Right-click is copy or paste (round 15), never a word selection first.
    rightClickSelectsWord: false,
  })
  rightClickPastes.value = readRightClickPastes(typeof localStorage === 'undefined' ? null : localStorage)
  term.onSelectionChange(() => (hasSelection.value = !!term?.hasSelection()))
  term.onResize(({ cols, rows }) => (grid.value = { cols, rows }))
  stopTheme = watch(theme, (t) => {
    if (term) term.options.theme = t
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.loadAddon(
    new WebLinksAddon((event, uri) => {
      if (event.ctrlKey || event.metaKey) window.open(uri, '_blank', 'noopener,noreferrer')
      else emit('openUrl', uri)
    }),
  )
  term.registerLinkProvider(fileLinkProvider)
  // Shift+Enter and Ctrl+Enter are a newline in the agent's prompt, not a
  // submit: the session gets ESC CR instead of xterm's plain CR. Alt+<page
  // shortcut> is for the page, not the agent: xterm skips it and the keydown
  // bubbles to the shortcut handlers. Everything else reaches the PTY.
  term.attachCustomKeyEventHandler((e) => {
    // Copy and paste (round 15): the page copies; a paste is the browser's own, which reaches xterm's text area.
    const clip = clipboardKey(e, { hasSelection: !!term?.hasSelection(), canPaste: canPaste(), mac: isMac })
    if (clip) {
      if (e.type === 'keydown') {
        if (clip === 'copy' || clip === 'copy-and-clear') {
          e.preventDefault()
          copySelection(clip === 'copy-and-clear')
        } else if (clip === 'swallow') {
          e.preventDefault()
        }
      }
      return false
    }
    if (newlineChord(e)) {
      if (e.type === 'keydown') sendInput(NEWLINE_IN_PROMPT)
      return false
    }
    // The Alt chords the page owns go back to it; so does ⌘K on a Mac (#302), a chord no PTY takes. Ctrl+K stays the shell's.
    if (e.type === 'keydown' && isMac && e.metaKey && !e.ctrlKey && !e.altKey && e.code === 'KeyK') return false
    // Shift is not one of the page's chords: Alt+Shift+anything stays the terminal's (Codex's round on #303).
    return !(e.type === 'keydown' && e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey && (ALT_PASSTHROUGH_CODES.has(e.code) || pageAltCodes.has(e.code)))
  })
  term.open(host.value!)
  grid.value = { cols: term.cols, rows: term.rows }
  // Capture, so a right-click that copies or pastes never reaches the menu's trigger.
  window.addEventListener('mousedown', onAnyMouseDown, true)
  window.addEventListener('keydown', onAnyKeyDown, true)
  host.value!.addEventListener('contextmenu', onContextMenu, true)
  // After term.open, so that xterm's own listeners on its screen see a move before this one.
  screenEl = term.element?.querySelector<HTMLElement>('.xterm-screen') ?? null
  screenEl?.addEventListener('mousemove', onScreenMouseMove)
  host.value!.addEventListener('mousemove', onHostMouseMove, true)
  // Re-measure once the bundled font has loaded so cell metrics are exact.
  document.fonts?.ready.then(() => {
    if (!term) return
    if (scaledView.value) scheduleScale()
    else scheduleResize()
  })
  // The renderer in use, for anyone measuring the tiles (data-renderer on
  // the host): webgl once the addon is up, dom when it is not available or
  // its context is lost.
  renderer.value = 'dom'
  try {
    const webgl = new WebglAddon()
    webgl.onContextLoss(() => {
      webgl.dispose()
      renderer.value = 'dom'
    })
    term.loadAddon(webgl)
    renderer.value = 'webgl'
  } catch {
    /* canvas renderer fallback */
  }
  term.onData((data) => transport?.sendInput(encodeText(data)))
  term.onBinary((data) => {
    const bytes = new Uint8Array(data.length)
    for (let i = 0; i < data.length; i++) bytes[i] = data.charCodeAt(i) & 0xff
    transport?.sendInput(bytes)
  })
  if (sizes() && host.value?.clientWidth && host.value.clientHeight) fitPane()
  observer = new ResizeObserver((entries) => {
    if (scaledView.value) {
      if (entries.some((e) => e.target === host.value)) fontAdjustments = 0
      scheduleScale()
    } else {
      scheduleResize()
    }
  })
  observer.observe(host.value!)
  if (scaledView.value && term.element) observer.observe(term.element)
  // A tab brought back sizes its session again: the view the person looks at wins.
  document.addEventListener('visibilitychange', onVisibility)
  pingTimer = window.setInterval(() => transport?.ping(), 10000)
  if (props.autoConnect) connect()
})

onBeforeUnmount(() => {
  window.removeEventListener('mousedown', onAnyMouseDown, true)
  window.removeEventListener('keydown', onAnyKeyDown, true)
  host.value?.removeEventListener('contextmenu', onContextMenu, true)
  host.value?.removeEventListener('mousemove', onHostMouseMove, true)
  screenEl?.removeEventListener('mousemove', onScreenMouseMove)
  screenEl = null
  observer?.disconnect()
  document.removeEventListener('visibilitychange', onVisibility)
  stopTheme?.()
  if (scaleFrame !== undefined) window.cancelAnimationFrame(scaleFrame)
  if (tileFrame !== undefined) window.cancelAnimationFrame(tileFrame)
  window.clearTimeout(resizeTimer)
  window.clearInterval(pingTimer)
  disconnect(false)
  term?.dispose()
  term = undefined
})
</script>

<template>
  <div class="relative h-full w-full overflow-hidden" :class="compact ? '' : 'rounded-lg border border-default'">
    <UContextMenu :items="terminalMenu" :disabled="compact">
      <div ref="host" class="terminal-host" :class="{ 'terminal-compact': compact, 'terminal-scale': scaledView, 'terminal-tile': props.fit === 'tile' }" :data-ended="ended ? 'true' : undefined" :data-renderer="renderer" :data-cols="grid.cols || undefined" :data-rows="grid.rows || undefined" :data-fit-passes="fitPasses" :data-fit-resizes="fitResizes" :aria-label="readOnly ? 'terminal (read-only)' : 'terminal'" role="region" />
    </UContextMenu>

    <div v-if="sizerChip" class="absolute right-2 bottom-2 z-10 flex items-center gap-2 rounded-md border border-default bg-default/90 py-1 pr-1 pl-2.5 text-xs shadow-sm backdrop-blur-sm" data-terminal-sizer :data-sizer-by="sizedBy">
      <UIcon name="i-lucide-scaling" class="size-3.5 text-muted" />
      <span class="text-muted">Sized by <span class="font-medium text-default" data-sizer-name>{{ sizerChip.who }}</span> · <span class="font-mono">{{ sizerChip.cols }} × {{ sizerChip.rows }}</span></span>
      <UButton label="Fit to my window" icon="i-lucide-maximize-2" size="xs" color="neutral" variant="soft" data-fit-mine @click="takeSize" />
    </div>

    <div v-if="notice && !compact" class="absolute top-2 right-2 z-10">
      <UBadge :label="notice" color="warning" variant="solid" size="sm" class="cursor-pointer" @click="notice = ''" />
    </div>

    <div v-if="compact && overlay" class="absolute inset-0 z-20 flex items-center justify-center bg-black/50 text-[10px] text-white/80">{{ overlay.title }}</div>
    <div v-else-if="overlay || connecting" class="absolute inset-0 z-20 flex items-center justify-center bg-black/60 backdrop-blur-[1px]">
      <div class="flex flex-col items-center gap-3 rounded-lg bg-default/95 px-6 py-5 text-center shadow-lg border border-default max-w-sm">
        <template v-if="connecting && !overlay">
          <UIcon name="i-lucide-loader-circle" class="size-6 animate-spin text-primary" />
          <p class="text-sm">Connecting…</p>
        </template>
        <template v-else-if="overlay">
          <UIcon name="i-lucide-plug-zap" class="size-6 text-warning" />
          <p class="text-sm font-medium">{{ overlay.title }}</p>
          <p v-if="overlay.detail" class="text-xs text-muted font-mono">{{ overlay.detail }}</p>
          <UButton label="Reconnect" icon="i-lucide-refresh-cw" size="sm" @click="connect" />
        </template>
      </div>
    </div>
  </div>
</template>
