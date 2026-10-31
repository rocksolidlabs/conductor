<script setup lang="ts">
import type { SessionInfo, ShareLink } from '~/composables/useSessions'
import { liveAttention } from '~/utils/attention'
import type { ActivityEntry, Attention, ChatHistory, ChatMessage, ChatPost, ChatSend, TransportKind, ViewerInfo, Welcome, NvimEvent, NvimBridge, ChatQuote } from '~/utils/protocol'
import type { TransportState } from '~/utils/transport/types'
import type { FileTarget } from '~/components/FileBrowser.vue'
import type { InspectorView } from '~/utils/inspector'
import { SESSION_SHORTCUTS, useShortcutsModal } from '~/composables/useShortcuts'
import { shortCwd } from '~/utils/sessions'
import { listedAttention, repliedQuestions, ReplyError, replyFailure, type QuestionReply } from '~/utils/reply'
import { activateTab, toggleFold } from '~/utils/editorTabs'
import { stopQuestion } from '~/utils/actions'
import { resolveTyped } from '~/utils/fileTree'
import { editorRows } from '~/utils/editorRows'
import type { EditorColumnHandle } from '~/components/EditorColumn.vue'
import type { ChangeRow } from '~/utils/changes'
import type { CommitRow } from '~/utils/commits'
import type { FileHeader } from '~/utils/protocol'

const route = useRoute()
const api = useSessions()
const admin = useWorkbenchToken()
const toast = useToast()
const live = useAttention()
const { httpBase } = useApiBase()
const { create } = useTerminalTransport()

const id = computed(() => String(route.params.id))
const session = ref<SessionInfo | null>(null)
const error = ref('')
const ready = ref(false)
const viewers = ref<ViewerInfo[]>([])
const viewerCount = ref(0)
const activity = ref<ActivityEntry[]>([])
const links = ref<ShareLink[]>([])
const transport = ref<{ kind: TransportKind; state: TransportState; rtt: number | null }>({ kind: 'ws', state: 'idle', rtt: null })
const share = ref(false)
const fileTarget = ref<FileTarget | null>(null)
const previewUrl = ref<string | null>(null)
// The editor area (design 4b): the files open above the terminal (EditorColumn), the tabs this page's.
const { tabs, openFile, openUrl, openDiff, openCommitDiff } = useEditorTabs()
useShortcutsModal().registerPage(SESSION_SHORTCUTS)
// The inspector's view (design 4h): the rail's item shown; `inspector` says whether the panel is open beside the rail.
const tab = ref<InspectorView>('people')
const attention = ref<Attention>({ state: '' })

// The chat beside the terminal (design 2a, 2b): one thread per session, fed by the terminal's connection.
const chatKey = computed(() => `session:${id.value}`)
const chat = useChat(chatKey)
const unread = useChatUnread()
const chatUnread = computed(() => unread.count(chatKey.value))
const chatOffline = computed(() => transport.value.state !== 'open')
// Below xl the inspector is not rendered: the header's Chat button opens the chat as a sheet (design 2c).
const xl = useMedia('(min-width: 80rem)')
const chatSheet = ref(false)
/** The thread is in front of the person: on the inspector's Chat tab where the inspector shows, or in the sheet. */
const chatShown = computed(() => (xl.value && inspector.value && tab.value === 'chat') || chatSheet.value)
function showChat() {
  if (!xl.value) chatSheet.value = true
  else if (inspector.value && tab.value === 'chat') inspector.value = false
  else {
    tab.value = 'chat'
    inspector.value = true
  }
}
const viaChat = (post: ChatPost) => terminal.value?.chat(post) ?? false
const viaChatSend = (send: ChatSend) => terminal.value?.chatSend(send) ?? false
// A member's connection carries its run's chat too (scope run): it feeds the run's thread, so its count is right everywhere.
const runKey = computed(() => (current.value?.crew?.runId ? `run:${current.value.crew.runId}` : ''))
const runChat = useChat(runKey)
function onChat(m: ChatMessage) {
  if (m.scope === 'run') {
    if (runKey.value) runChat.accept(m, { live: true })
    return
  }
  chat.accept(m, { live: true })
}
function onChatHistory(h: ChatHistory) {
  if (h.scope === 'run') {
    if (runKey.value) runChat.history(h)
    return
  }
  chat.history(h)
}
function chatSend(text: string, to: string) {
  chat.send(text, to ? { to } : {}, viaChat)
}

// Comments on lines (design round 12, F7): posted to this session's chat (and, asked of the agent, typed into it); the Chat tab comes
// forward (the sheet below xl). A quote card opens the file here, at the range.
provideChatQuotes(quoteActionsFor(requestFile, () => current.value?.cwd, openFile))
function onComment(c: { quote: ChatQuote; text: string; toAgent: boolean }) {
  chat.send(c.text, { quote: c.quote, ...(c.toAgent ? { to: 'agent' } : {}) }, viaChat)
  tab.value = 'chat'
  if (import.meta.client && !window.matchMedia('(min-width: 1280px)').matches) chatSheet.value = true
}
function chatSendToAgent(ref: string) {
  if (!chat.sendToAgent(ref, viaChatSend)) toast.add({ title: 'Not connected', description: 'Reconnect the terminal and try again.', color: 'warning' })
}
function chatRetry(nonce: string) {
  chat.retry(nonce, viaChat)
}
/** A question's choice from the chat: its keys into the session, as the quick reply bar's, naming the question (its line is the question's id). */
function chatAnswer(m: ChatMessage, index: number) {
  const o = m.options?.[index]
  if (o) answerQuestion({ question: m.id, input: o.input })
}
/** Sends a reply to a question over the terminal's connection, once per question; a refusal (answered already, not connected) is said in a toast. */
const replied = repliedQuestions()
const typedAnswers = useTypedAnswers()
function answerQuestion(reply: QuestionReply) {
  if (!terminal.value) return toast.add(replyFailure(new ReplyError('not_connected', 'not connected')))
  const t = terminal.value
  replied
    .once(reply.question, () => t.answer(reply))
    // A line typed as the answer, once sent: this browser shows it under the question to its own person (#364).
    .then((sent) => {
      if (sent && 'text' in reply && reply.text) typedAnswers.remember(reply.question, reply.text)
    })
    .catch((e: unknown) => toast.add(replyFailure(e)))
}
function onRequestError(err: { code: string; message: string; requestId: string }) {
  if (chat.fail(err.requestId, err.message)) return
  // A chat_send that failed names the message it would have typed.
  toast.add({ title: 'Not sent to the agent', description: err.message, color: 'warning' })
}

const INSPECTOR_KEY = 'conductor.inspector'
const inspector = ref(true)
try {
  inspector.value = localStorage.getItem(INSPECTOR_KEY) !== '0'
} catch {
  /* ignore */
}
watch(inspector, (v) => {
  try {
    localStorage.setItem(INSPECTOR_KEY, v ? '1' : '0')
  } catch {
    /* ignore */
  }
})

// Opening a session takes its event badge away, and one arriving while it is
// open never stays.
const events = useEvents()
watch(
  [id, () => events.marks.value[id.value]],
  ([sid, mark]) => {
    if (mark) events.clearMark(sid)
  },
  { immediate: true },
)

// The live store is the source of truth for attention (it is what the sidebar
// shows); the terminal's own attention message arrives a moment earlier.
const stored = computed(() => live.sessions.value.find((s) => s.id === id.value))
/** The session as the live store has it, else as read: its yolo badge and its agent session follow the stream. */
const current = computed(() => stored.value ?? session.value)
const ended = computed(() => !!current.value && (current.value.status === 'exited' || current.value.status === 'stopped'))
/** A hosted session whose machine is away: its files cannot be read until it returns (design 4f). */
const hostAway = computed(() => (current.value?.kind === 'hosted' && current.value.status === 'host_disconnected' ? current.value.hostName || 'The host' : undefined))
const copy = useCopy()
watch(
  () => stored.value?.attention,
  (a) => {
    // A listing that lacks the question's id (a server older than the host) leaves the owner's own word, the terminal's, standing.
    if (a) attention.value = listedAttention(a, attention.value, transport.value.state === 'open')
  },
  { deep: true },
)

const agentLabel = computed(() => {
  const a = session.value?.agentId ?? ''
  const names: Record<string, string> = { claude: 'Claude Code', codex: 'Codex', agy: 'Antigravity' }
  return names[a] ?? a
})
const meta = computed(() => {
  const s = session.value
  if (!s) return ''
  const parts = [agentLabel.value]
  parts.push(s.kind === 'hosted' ? `hosted by ${s.hostUser || '?'} on ${s.hostName || 'dev machine'}` : 'server')
  parts.push(shortCwd(s.cwd))
  if (s.branch) parts.push(s.branch)
  return parts.join(' · ')
})

// Rename… (#318): a server session, running or ended, through the one dialog; a hosted session's host names it.
const renameModal = useRenameModal()
const renamable = computed(() => current.value?.kind === 'server')
function renameSession() {
  if (current.value) renameModal.show(current.value)
}

const menu = computed(() => [
  [
    { label: 'Mark as needs input', icon: 'i-lucide-hand', onSelect: () => signal('needs_input') },
    { label: 'Mark as working', icon: 'i-lucide-loader-circle', onSelect: () => signal('working') },
    { label: 'Clear signal', icon: 'i-lucide-x', onSelect: () => signal('clear') },
  ],
  ...(renamable.value ? [[{ label: 'Rename…', icon: 'i-lucide-pencil', onSelect: renameSession }]] : []),
  [{ label: 'Stop session', icon: 'i-lucide-square', color: 'error' as const, disabled: !(session.value && (session.value.status === 'running' || session.value.status === 'starting')), onSelect: stop }],
])

const terminal = ref<{ connect: () => void; focus: () => void; sendInput: (t: string) => boolean; submit: (t: string) => boolean; answer: (r: QuestionReply) => Promise<boolean>; chat: (p: ChatPost) => boolean; chatSend: (s: ChatSend) => boolean; requestFile: (p: string, s?: boolean, x?: FileGetExtra) => Promise<any>; nvimOpen: (p: string) => Promise<NvimEvent>; nvimInput: EditorTerminal['nvimInput']; nvimClose: EditorTerminal['nvimClose']; nvimSwap: EditorTerminal['nvimSwap']; writeFile: EditorTerminal['writeFile']; takeSize: () => void; sizerShown: () => boolean } | null>(null)

// The thread counts nothing while it is open in front of this person.
watch(
  [chatKey, chatShown],
  ([key, shown]) => {
    if (shown) unread.openThread(key)
    else unread.closeThread(key)
  },
  { immediate: true },
)
watch(chatKey, (_, old) => old && unread.closeThread(old))
onBeforeUnmount(() => unread.closeThread(chatKey.value))

// "<name> is typing…": anyone else whose last input is under four seconds old.
const now = ref(Date.now())
let tick: number | undefined
onMounted(() => (tick = window.setInterval(() => (now.value = Date.now()), 1000)))
onBeforeUnmount(() => window.clearInterval(tick))
const selfId = ref('')
const typingNames = computed(() =>
  viewers.value.filter((v) => v.id !== selfId.value && v.lastInputAt && now.value - Date.parse(v.lastInputAt) < 4000).map((v) => v.name),
)
const typingLine = computed(() => {
  const n = typingNames.value
  if (!n.length) return ''
  if (n.length === 1) return `${n[0]} is typing…`
  if (n.length === 2) return `${n[0]} and ${n[1]} are typing…`
  return `${n[0]}, ${n[1]} and ${n.length - 2} more are typing…`
})

// The name as the live store has it: a rename, here or anywhere, shows at once.
useHead({ title: computed(() => current.value?.name || 'Session') })

async function load() {
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  try {
    const res = await api.get(id.value)
    session.value = res.session
    attention.value = liveAttention(res.session.attention ?? { state: '' })
    links.value = res.links ?? []
    error.value = ''
    ready.value = true
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function refreshLinks() {
  try {
    links.value = await api.links(id.value)
  } catch {
    /* keep the last list */
  }
}

async function signal(state: 'needs_input' | 'working' | 'clear') {
  try {
    await api.setAttention(id.value, state, state === 'needs_input' ? 'flagged from the workbench' : undefined)
  } catch (e) {
    toast.add({ title: 'Signal failed', description: (e as Error).message, color: 'error' })
  }
}

function onAttention(msg: { state: string; message?: string; source?: string }) {
  attention.value = { ...(msg as Attention), since: new Date().toISOString() }
}

function onViewers(info: { count: number; list?: ViewerInfo[] }) {
  viewerCount.value = info.count
  viewers.value = info.list ?? []
}

function onActivity(e: ActivityEntry) {
  activity.value = [...activity.value.slice(-199), e]
}

// Every (re)connection replays the last 50 entries and the kept chat, so start both afresh.
// The editor (design round 12, F6 and F8): what the welcome allows here, Neovim, saves.
const editor = useEditorBridge(terminal as unknown as Ref<EditorTerminal | null>, () => (current.value?.kind === 'hosted' ? current.value.hostName || 'the host' : 'this server'))

function onWelcome(w: Welcome) {
  editor.onWelcome(w)
  selfId.value = w.subscriberId ?? w.viewerId ?? ''
  activity.value = []
  unread.registerSelf(selfId.value)
  chat.welcome(!!w.chat, viaChat)
}

/** The connection closed: what was being said waits, and the editor's Neovims ended with it (their tabs keep their text). */
function onClosed() {
  chat.offline()
  editor.onClosed()
}

function createTransport() {
  return create({ sessionId: id.value, token: admin.token.value, kind: session.value?.kind ?? 'server' })
}

function onStatus(status: string, exitCode?: number) {
  if (session.value) {
    session.value.status = status as SessionInfo['status']
    if (exitCode !== undefined) session.value.exitCode = exitCode
  }
}

async function stop() {
  if (!session.value) return
  try {
    await api.stop(session.value.id)
    toast.add({ title: 'Stop requested', color: 'neutral' })
    await load()
  } catch (e) {
    toast.add({ title: 'Stop failed', description: (e as Error).message, color: 'error' })
  }
}

function reply(text: string, question?: string) {
  answerQuestion({ question, text })
}

function option(index: number, question?: string) {
  const o = attention.value.options?.[index]
  if (!o) return
  answerQuestion({ question, input: o.input })
}


/** The header's Files button selects the rail's Explorer item, and collapses the panel when that already shows (4h). */
function showFiles() {
  if (inspector.value && tab.value === 'explorer') inspector.value = false
  else {
    tab.value = 'explorer'
    inspector.value = true
  }
}

function requestFile(path: string, stat?: boolean, extra?: FileGetExtra) {
  if (!terminal.value) return Promise.reject(new Error('terminal not ready'))
  return terminal.value.requestFile(path, stat, extra)
}

function rawUrl(path: string) {
  if (session.value?.kind !== 'server') return null
  return `${httpBase.value}/api/sessions/${encodeURIComponent(id.value)}/files?path=${encodeURIComponent(path)}&raw=1&token=${encodeURIComponent(admin.token.value)}`
}

async function revoke(link: ShareLink) {
  try {
    await api.revokeLink(id.value, link.id)
    toast.add({ title: 'Link revoked', description: 'Viewers using it were disconnected.', icon: 'i-lucide-ban', color: 'neutral' })
  } catch (e) {
    toast.add({ title: 'Revoke failed', description: (e as Error).message, color: 'error' })
  }
  await refreshLinks()
}

watch(share, (open) => {
  if (!open) refreshLinks()
})

onMounted(load)
watch(() => admin.token.value, load)
watch(id, () => {
  ready.value = false
  session.value = null
  viewers.value = []
  activity.value = []
  fileTarget.value = null
  previewUrl.value = null
  load()
})

// ?tab= (#303): the rail's item a link names, read as the page opens and as the query changes (never written back).
const TAB_QUERY: Record<string, InspectorView> = { files: 'explorer', explorer: 'explorer', changes: 'changes', commits: 'commits', chat: 'chat', activity: 'activity', people: 'people' }
watch(
  () => route.query.tab,
  (q) => {
    const v = typeof q === 'string' ? TAB_QUERY[q] : undefined
    if (!v) return
    if (v === 'chat' && !xl.value) {
      chatSheet.value = true
      return
    }
    tab.value = v
    inspector.value = true
  },
  { immediate: true },
)

// ?open= (#304): a file the palette's Touched row names on another session's page, opened in this page's editor as a typed
// path is (relative to the working directory or absolute), the path taken whole (a name may end in `:digits`: the line goes
// in `?line=` instead, whole-batch round), once the session's directory is known and the terminal is connected (the editor
// reads the file over it; opened before, the read fails and the tab keeps the failure: Codex's round). Read as the page opens
// and as the query changes, never written back.
let opened = ''
watch(
  [() => route.query.open, () => route.query.line, () => current.value?.cwd, () => transport.value.state === 'open'],
  ([q, l, cwd, connected]) => {
    if (typeof q !== 'string' || !q || !cwd || !connected) return
    const key = `${q}\n${typeof l === 'string' ? l : ''}`
    if (key === opened) return
    opened = key
    openFile({ path: resolveTyped(cwd, q), line: typeof l === 'string' && /^\d+$/.test(l) ? Number(l) : undefined })
  },
  { immediate: true },
)

// S shares and X asks to stop the session this page is on (#303): X opens the palette on the page's Stop row, asking.
const liveNow = computed(() => !!current.value && (current.value.status === 'running' || current.value.status === 'starting'))
function askStop() {
  if (liveNow.value) palette.askRow('page:stop')
}
// The S that completes the layout's G S chord is the chord's, not this page's (each defineShortcuts hears the key on its own).
const lastChord = useState<number>('lastChordAt', () => 0)
defineShortcuts({
  s: () => liveNow.value && Date.now() - lastChord.value > 150 && (share.value = true),
  x: askStop,
  alt_x: { usingInput: true, handler: askStop },
})

// This page's group in the command palette (#302): what the header and the menu offer, the inspector's views, the open tabs; under
// `:` the tabs, the changes and the commits; under `@` the people here and the links. Read when the palette opens, never before.
const palette = usePalette()
const inspectorRef = useTemplateRef<{ changes: ChangeRow[]; commits: CommitRow[]; status: FileHeader | null }>('inspectorRef')
const columnRef = useTemplateRef<EditorColumnHandle>('columnRef')
/**
 * Shows a view (#332): the inspector on it, or the chat sheet below xl; one already shown stays shown. The header's Files and
 * Chat buttons toggle (showFiles, showChat); a palette row or a session row's Open … says "open" and never hides.
 */
function showView(v: InspectorView) {
  if (v === 'chat' && !xl.value) {
    chatSheet.value = true
    return
  }
  tab.value = v
  inspector.value = true
}
async function copyViewLink() {
  const s = current.value
  if (!s) return
  try {
    const made = await api.createLink(s.id, { role: 'view', ttlSeconds: 7200 })
    const note = made.rendezvous ? `Made here, not on ${made.rendezvous.server}: ${made.rendezvous.error}` : made.remote ? 'Minted by the switchyard: it works from anywhere.' : 'It works where this server is reachable.'
    await copy(made.url, `View link to ${s.name} copied`, note)
    await refreshLinks()
  } catch (e) {
    toast.add({ title: 'Link failed', description: (e as Error).message, color: 'error' })
  }
}
palette.registerPage(() => {
  const s = current.value
  if (!s) return null
  const live = s.status === 'running' || s.status === 'starting'
  const views: Array<[InspectorView, string, string]> = [
    ['explorer', 'Open Files', 'i-lucide-folder'],
    ['chat', `Open Chat${chatUnread.value ? ` · ${chatUnread.value} unread` : ''}`, 'i-lucide-message-circle'],
    ['activity', 'Open Activity', 'i-lucide-activity'],
    ['people', `Open People · ${viewers.value.length || s.viewers} here`, 'i-lucide-users'],
  ]
  const t = tabs.value
  const tabRows = t.tabs.map((tb) => ({
    key: `page:tab:${tb.id}`,
    label: tb.title,
    detail: tb.path,
    icon: tb.kind === 'url' ? 'i-lucide-globe' : tb.kind === 'diff' ? 'i-lucide-file-diff' : 'i-lucide-file',
    on: t.active === tb.id && !t.folded ? true : undefined,
    run: () => (tabs.value = { ...activateTab(tabs.value, tb.id), folded: false }),
    actions: [
      // Through the area's own close: an unsaved file asks before it goes (whole-batch round).
      { id: 'close', kind: 'page' as const, target: tb.id, label: 'Close', icon: 'i-lucide-x', kbds: ['X'], run: () => columnRef.value?.requestClose(tb.id) },
      // The tab's menu (#330), as actions: each through the close that asks about unsaved changes.
      { id: 'close-others', kind: 'page' as const, target: tb.id, label: 'Close others', icon: 'i-lucide-copy-x', run: () => columnRef.value?.closeScope(tb.id, 'others') },
      { id: 'close-right', kind: 'page' as const, target: tb.id, label: 'Close to the right', icon: 'i-lucide-arrow-right-to-line', run: () => columnRef.value?.closeScope(tb.id, 'right') },
      { id: 'close-left', kind: 'page' as const, target: tb.id, label: 'Close to the left', icon: 'i-lucide-arrow-left-to-line', run: () => columnRef.value?.closeScope(tb.id, 'left') },
      { id: 'close-all', kind: 'page' as const, target: tb.id, label: 'Close all', icon: 'i-lucide-x', run: () => columnRef.value?.closeScope(tb.id, 'all') },
      { id: 'copy-path', kind: 'page' as const, target: tb.id, label: 'Copy path', icon: 'i-lucide-copy', run: () => void copy(tb.path, 'Path copied') },
      ...(tb.kind === 'file' && rawUrl(tb.path) ? [{ id: 'raw', kind: 'page' as const, target: tb.id, label: 'Open raw', icon: 'i-lucide-external-link', run: () => window.open(rawUrl(tb.path)!, '_blank', 'noopener,noreferrer') }] : []),
    ],
  }))
  const status = inspectorRef.value?.status
  const changes = (inspectorRef.value?.changes ?? []).map((c) => ({
    key: `page:change:${c.path}`,
    label: c.path.startsWith(s.cwd) ? c.path.slice(s.cwd.length + 1) : c.path,
    detail: `${c.status} · +${c.added} −${c.removed}`,
    icon: 'i-lucide-file-diff',
    run: () => openDiff(c, { top: status?.kind === 'status' ? status.path : s.cwd, branch: status?.kind === 'status' ? status.branch : undefined, baseId: status?.kind === 'status' ? status.base : undefined }),
  }))
  const commits = (inspectorRef.value?.commits ?? []).map((c) => ({ key: `page:commit:${c.sha}`, label: c.subject, detail: `${c.short} · ${c.author}`, icon: 'i-lucide-git-commit-horizontal', run: () => showView('commits') }))
  const people = [
    ...viewers.value.map((v) => ({ key: `page:viewer:${v.name}:${v.since}`, label: v.name, detail: `${v.link ? `via “${v.link}”` : v.role === 'control' ? 'Owner' : 'Viewer'} · since ${v.since.slice(11, 16)}`, icon: 'i-lucide-user-round', run: () => showView('people') })),
    ...links.value
      .filter((l) => !l.revoked)
      .map((l) => ({
        key: `page:link:${l.id}`,
        label: `Link ${l.label || l.id.slice(0, 8)} · ${l.role}`,
        detail: `${l.active} ${l.active === 1 ? 'viewer' : 'viewers'} on it`,
        icon: 'i-lucide-link',
        run: () => showView('people'),
        actions: [{ id: 'revoke', kind: 'page' as const, target: l.id, label: 'Revoke', icon: 'i-lucide-ban', kbds: ['X'], ask: { title: `Revoke ${l.label || 'the link'}?`, detail: 'Viewers using it are disconnected.' }, run: () => void revoke(l) }],
      })),
  ]
  return {
    label: s.name,
    sessionId: s.id,
    cwd: s.cwd,
    openFile,
    showView,
    rows: [
      { key: 'page:share', label: 'Share…', icon: 'i-lucide-share-2', kbds: ['S'], disabled: !live, run: () => (share.value = true) },
      { key: 'page:copy-link', label: 'Copy a view link now', icon: 'i-lucide-link', kbds: ['shift', 'S'], disabled: !live, run: copyViewLink },
      ...(s.kind === 'server' ? [{ key: 'page:rename', label: 'Rename…', icon: 'i-lucide-pencil', run: renameSession }] : []),
      { key: 'page:stop', label: 'Stop…', icon: 'i-lucide-square', kbds: ['X'], disabled: !live, ask: stopQuestion({ kind: 'session', name: s.name }), run: stop },
      { key: 'page:mark-needs', label: 'Mark as needs input', icon: 'i-lucide-hand', run: () => signal('needs_input') },
      { key: 'page:mark-working', label: 'Mark as working', icon: 'i-lucide-loader-circle', run: () => signal('working') },
      { key: 'page:mark-clear', label: 'Clear signal', icon: 'i-lucide-x', run: () => signal('clear') },
      ...(terminal.value?.sizerShown() ? [{ key: 'page:fit', label: 'Fit to my window', icon: 'i-lucide-maximize-2', run: () => terminal.value?.takeSize() }] : []),
      ...(t.tabs.length ? [{ key: 'page:fold', label: t.folded ? 'Bring the editor back' : 'Fold the editor to its tab strip', icon: 'i-lucide-panel-bottom', kbds: ['T'], run: () => (tabs.value = toggleFold(tabs.value)) }] : []),
      // The editor's own rows (#321), with a file in front: its state read live through the column.
      ...editorRows(columnRef.value?.palette, { setKeymap: (k) => columnRef.value?.setKeymap(k), save: () => columnRef.value?.save(), close: (id) => columnRef.value?.requestClose(id), askAgent: () => columnRef.value?.askAgent() }),
      ...views.map(([v, label, icon]) => ({ key: `page:view:${v}`, label, icon, run: () => showView(v) })),
      ...tabRows,
    ],
    files: [...tabRows, ...changes, ...commits],
    people,
  }
})
</script>

<template>
  <UDashboardPanel :id="`session-${id}`" :ui="{ body: 'p-0 sm:p-0 flex flex-col min-h-0 gap-0' }">
    <template #header>
      <UDashboardNavbar :toggle="false" :ui="{ root: 'h-14 bg-default', title: 'min-w-0' }">
        <template #leading>
          <UButton icon="i-lucide-arrow-left" color="neutral" variant="ghost" aria-label="Back to the list" class="lg:hidden" to="/sessions" data-back-to-list />
        </template>
        <template #title>
          <div class="flex min-w-0 flex-col">
            <div class="flex items-center gap-2 min-w-0">
              <span class="truncate text-[15px] font-semibold" data-session-title>{{ current?.name || 'Session' }}</span>
              <AttentionBadge :attention="attention" />
              <SessionStatusBadge v-if="session && session.status !== 'running'" :status="session.status" :exit-code="session.exitCode" />
              <YoloBadge v-if="current?.yolo" />
            </div>
            <span class="flex min-w-0 items-center gap-2 font-mono text-[11.5px] text-muted">
              <span class="truncate">{{ meta }}</span>
              <button
                v-if="current?.agentSession"
                type="button"
                class="hidden max-w-40 flex-none truncate hover:text-default md:inline"
                :title="`The agent's own session (${current.agentSession.source}): ${current.agentSession.id}. Click to copy.`"
                data-agent-session
                @click="copy(current.agentSession.id, 'Agent session copied')"
              >
                {{ current.agentSession.id }}
              </button>
            </span>
          </div>
        </template>
        <template #right>
          <ResumeButton v-if="ended && current" :session="current" />
          <TransportBadge :kind="transport.kind" :state="transport.state" :rtt="transport.rtt" class="hidden md:inline-flex" />
          <ViewerAvatars :viewers="viewers" class="hidden md:flex" />
          <!-- Icons only on a phone, as on the run page: the labels would push the name off the bar. -->
          <!-- Files and Chat only below xl (#329): at xl the inspector rail has both, with the chat's count; here they said it twice. -->
          <UButton icon="i-lucide-folder-open" color="neutral" variant="outline" aria-label="Files" class="xl:hidden" :class="inspector && tab === 'explorer' && 'ring-2 ring-primary/40'" @click="showFiles"><span class="hidden sm:inline">Files</span></UButton>
          <UButton icon="i-lucide-share-2" aria-label="Share" @click="share = true"><span class="hidden sm:inline">Share</span></UButton>
          <!-- The chat (design 2b, 2c): the count while the thread is closed; the inspector's tab at xl, a sheet below. -->
          <UButton v-if="chat.thread.value.capable" icon="i-lucide-message-circle" color="neutral" variant="outline" aria-label="Chat" class="xl:hidden" :class="chatShown && 'ring-2 ring-primary/40'" data-chat-button @click="showChat">
            <span class="hidden sm:inline">Chat</span>
            <ChatUnreadPill :count="chatUnread" />
          </UButton>
          <UDropdownMenu :items="menu">
            <UButton icon="i-lucide-ellipsis" color="neutral" variant="outline" aria-label="More" />
          </UDropdownMenu>
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" class="m-4" />
      <div v-else-if="ready && session" class="flex flex-1 min-h-0">
        <EditorColumn ref="columnRef" v-model:tabs="tabs" :request="requestFile" :nvim="editor.nvim.value" :write="editor.writeFile" :can-edit="editor.canEdit.value" commenting :can-ask="!ended" :cwd="current?.cwd" :raw-url="rawUrl" :host-away="hostAway" :bar-text="attention.message || agentLabel" class="p-3" @comment="onComment">
          <TerminalView
            ref="terminal"
            :create-transport="createTransport"
            @nvim="editor.onNvim"
            @welcome="onWelcome"
            @status="onStatus"
            @attention="onAttention"
            @viewers="onViewers"
            @activity="onActivity"
            @transport="transport = $event"
            @closed="onClosed"
            @chat="onChat"
            @chat-history="onChatHistory"
            @request-error="onRequestError"
            @open-file="openFile"
            @open-url="openUrl"
          />
          <div v-if="typingLine" class="pointer-events-none absolute bottom-2 left-3 flex items-center gap-2 rounded bg-default/80 px-2 py-0.5 text-xs text-muted backdrop-blur-sm"><span class="inline-block h-3.5 w-1.5 bg-muted/70" />{{ typingLine }}</div>
          <template #bar>
            <QuickReplyBar :attention="attention" :agent-name="agentLabel" role="control" @reply="reply" @option="option" @open-plan="openFile({ path: $event })" />
          </template>
        </EditorColumn>
        <!-- The inspector (design 4h): its rail stays at xl, the panel beside it open or collapsed. -->
        <div class="hidden xl:flex flex-none">
          <SessionInspector
            ref="inspectorRef"
            v-model:view="tab"
            v-model:open="inspector"
            v-model:target="fileTarget"
            v-model:url="previewUrl"
            :session="session"
            role="control"
            :viewers="viewers"
            :activity="activity"
            :links="links"
            :request="requestFile"
            :raw-url="rawUrl"
            :chat="chat.thread.value"
            :chat-unread="chatUnread"
            :chat-offline="chatOffline"
            :ended="ended"
            @new-link="share = true"
            @revoke="revoke"
            @chat-send="chatSend"
            @chat-send-to-agent="chatSendToAgent"
            @chat-retry="chatRetry"
            @chat-answer="chatAnswer"
            @open-file="openFile"
            @open-diff="openDiff"
            @open-commit-diff="openCommitDiff"
          />
        </div>
      </div>
      <div v-else class="p-6 text-sm text-muted flex items-center gap-2"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Loading session…</div>
    </template>
  </UDashboardPanel>

  <ShareLinksModal v-model:open="share" :session-id="id" :session-name="current?.name" />
  <ChatSheet v-model:open="chatSheet" :thread="chat.thread.value" role="control" :ended="ended" :offline="chatOffline" :viewers="viewers" @send="chatSend" @send-to-agent="chatSendToAgent" @retry="chatRetry" @answer="chatAnswer" />
</template>
