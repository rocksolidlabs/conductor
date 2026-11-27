<script setup lang="ts">
import type { AgentInfo, SessionInfo, ShareLink } from '~/composables/useSessions'
import { answersInChat, liveAttention, offeredChoices, takesReply } from '~/utils/attention'
import type { ActivityEntry, Attention, ChatHistory, ChatMessage, ChatPost, ChatSend, TransportKind, ViewerInfo, Welcome, NvimEvent, NvimBridge, ChatQuote } from '~/utils/protocol'
import type { TransportState } from '~/utils/transport/types'
import type { FileTarget } from '~/components/FileBrowser.vue'
import { railView, type InspectorView } from '~/utils/inspector'
import { SESSION_SHORTCUTS, useShortcutsModal } from '~/composables/useShortcuts'
import { firstMessageLine } from '~/utils/launch'
import { agentName, gonePage, sessionGone, sessionHeadline, type GonePage } from '~/utils/sessions'
import { sessionStatus, statusOfKey } from '~/utils/status'
import { choiceReply, listedAttention, questionOf, repliedQuestions, ReplyError, replyFailure, type QuestionReply, type ReplyOutcome, type ReplyTaken, replyOutcomeOf } from '~/utils/reply'
import { activateTab, toggleCollapse, toggleFold } from '~/utils/editorTabs'
import { revokeQuestion, stopQuestion } from '~/utils/actions'
import { resolveTyped } from '~/utils/fileTree'
import { editorRows } from '~/utils/editorRows'
import type { EditorColumnHandle } from '~/components/EditorColumn.vue'
import type { ChangeRow } from '~/utils/changes'
import type { CommitRow } from '~/utils/commits'
import type { FileHeader, SessionWay, TranscriptEntry } from '~/utils/protocol'
import type { SessionConnection } from '~/composables/useSessionConnection'
import { ENDS_TERMINAL_WORK, firstReplyOf, refusalStands, switchState, type Refusal, type RefusalBasis, type SwitchFacts } from '~/utils/switchState'
import { joinTop } from '~/utils/changes'
import { ApiError } from '~/composables/useApi'
import { stripRunOf } from '~/utils/memberStrip'

const route = useRoute()
// The question bar's Answer on a durable decision's question opens its card (Program item DR1).
const decisionModal = useDecisionModal()
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
const { tabs, openFile, previewFile, linkOpened, linkOffered, openDiff, openCommitDiff } = useEditorTabs()
useShortcutsModal().registerPage(SESSION_SHORTCUTS)
// The inspector's view (design 4h): the rail's item shown; `inspector` says whether the panel is open beside the rail. With no view
// chosen the panel opens on Explorer (P11): People would show the person to themselves.
const tab = ref<InspectorView>('explorer')
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
/** The thread is in front of the person: on the inspector's People view where the inspector shows, or in the sheet. */
const chatShown = computed(() => (xl.value && inspector.value && railView(tab.value) === 'people') || chatSheet.value)
function showChat() {
  if (!xl.value) chatSheet.value = true
  else if (inspector.value && railView(tab.value) === 'people') inspector.value = false
  else {
    tab.value = 'people'
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
  tab.value = 'people'
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
  if (o) void answerQuestion({ question: m.id, ...choiceReply(o, way.value) }, undefined, false)
}
/** Sends a reply to a question over the terminal's connection, once per question; a refusal (answered already, not connected) is said in a toast. */
const replied = repliedQuestions()
const typedAnswers = useTypedAnswers()
/**
 * What became of the reply (ReplyOutcome, without `left`): held when one to the same question is still on its way from here; a
 * failure by its code (replyOutcomeOf). `left` is called as the reply leaves the page.
 */
function answerQuestion(reply: QuestionReply, left?: () => void, typed = 'text' in reply): Promise<ReplyOutcome> {
  if (!terminal.value) {
    toast.add(replyFailure(new ReplyError('not_connected', 'not connected')))
    return Promise.resolve('refused')
  }
  const t = terminal.value
  return replied
    .once(reply.question, () => t.answer(reply, { sent: left }))
    // A line typed as the answer, once sent: this browser shows it under the question to its own person (#364).
    .then((sent) => {
      if (sent && 'text' in reply && reply.text) typedAnswers.remember(reply.question, reply.text)
      return sent ? 'sent' : 'held'
    })
    .catch((e: unknown): ReplyOutcome => {
      toast.add(replyFailure(e, undefined, typed, way.value))
      return replyOutcomeOf(e)
    })
}
function onRequestError(err: { code: string; message: string; requestId: string }) {
  if (chat.fail(err.requestId, err.message)) return
  // A chat_send that failed names the message it would have typed.
  toast.add({ title: 'Not sent to the agent', description: err.message, color: 'warning' })
}

// The panel is closed until a person opens it (P11): the terminal has its room. One choice for every session, kept under a key of its
// own, so that the old key's stored "1" (the panel open by default) keeps nobody's panel open.
const INSPECTOR_KEY = 'conductor.inspectorOpen'
const inspector = ref(false)
try {
  inspector.value = localStorage.getItem(INSPECTOR_KEY) === '1'
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
const clock = useNow()
/** The header's state (sessionStatus): the live store's session, else the one read with the attention the page holds. */
const status = computed(() => {
  const s = stored.value ?? (session.value && { ...session.value, attention: attention.value })
  return s ? sessionStatus(s, clock.value) : undefined
})
const ended = computed(() => !!current.value && (current.value.status === 'exited' || current.value.status === 'stopped'))
// A crew member carries its crew (UX3 R12): the strip above the header, from the moment the session says it is a member, until the
// run turns out gone (no longer held by the server, or a Brakeman turn's).
const stripGone = ref('')
const memberRun = computed(() => {
  const r = stripRunOf(current.value)
  return r && r !== stripGone.value ? r : ''
})
// On a very wide header the strip sits in the name line's row, left of the name (UX3 R12b): a member's terminal keeps the room a
// strip above would take. Judged by the header's own width, never the window's; narrower, the strip stays above.
const STRIP_INLINE_MIN = 1500
const head = useTemplateRef<HTMLElement>('head')
const headWidth = ref(0)
watch(
  head,
  (el, _prev, onCleanup) => {
    if (!el) return
    headWidth.value = el.getBoundingClientRect().width
    const observer = new ResizeObserver((entries) => {
      const r = entries[0]?.contentRect
      if (r) headWidth.value = r.width
    })
    observer.observe(el)
    onCleanup(() => observer.disconnect())
  },
  { immediate: true },
)
const stripInline = computed(() => headWidth.value >= STRIP_INLINE_MIN)
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

const agentLabel = computed(() => agentName(session.value?.agentId ?? ''))
// The first message waiting to be typed (P19): what it waits for, in one line above the terminal, until the listing clears it.
const firstLine = computed(() => firstMessageLine(current.value, agentLabel.value))
// Agent · folder · branch under the name (P11), the whole path its tooltip; the ids are in the More menu.
const headline = computed(() => (current.value ? sessionHeadline(current.value) : null))

// Below sm the bar keeps Back, the name with its state, Chat and More (D9): Files, Share and Fullscreen go first into More.
const sm = useMedia('(min-width: 40rem)')
const fullscreen = useFullscreenToggle()
const canFullscreen = import.meta.client && document.fullscreenEnabled === true

// Rename… (#318): a server session, running or ended, through the one dialog; a hosted session's host names it.
const renameModal = useRenameModal()
const renamable = computed(() => current.value?.kind === 'server')
function renameSession() {
  if (current.value) renameModal.show(current.value)
}

const menu = computed(() => [
  ...wayItems.value,
  ...(sm.value
    ? []
    : [
        [
          { label: 'Files', icon: 'i-lucide-folder-open', onSelect: showFiles },
          { label: 'Share', icon: 'i-lucide-share-2', onSelect: () => (share.value = true) },
          ...(canFullscreen ? [{ label: fullscreen.fullscreen.value ? 'Exit fullscreen' : 'Enter fullscreen', icon: fullscreen.fullscreen.value ? 'i-lucide-minimize' : 'i-lucide-maximize', onSelect: fullscreen.toggle }] : []),
        ],
      ]),
  // Mark as (UX3 R14): the three marks in one item, a nested menu; on a phone, where a nested menu opens over its parent, a labelled
  // group of three.
  sm.value ? [{ label: 'Mark as', icon: 'i-lucide-tag', children: [MARKS] }] : [{ type: 'label' as const, label: 'Mark as' }, ...MARKS],
  // Fit to my window while the terminal shows and another window sets its size, as the palette's row.
  ...(fitOffered() ? [[{ label: 'Fit to my window', icon: 'i-lucide-maximize-2', onSelect: () => terminal.value?.takeSize() }]] : []),
  ...(renamable.value ? [[{ label: 'Rename…', icon: 'i-lucide-pencil', onSelect: renameSession }]] : []),
  [
    { label: 'Copy session id', icon: 'i-lucide-copy', onSelect: () => void copy(id.value, 'Session id copied') },
    ...(current.value?.agentSession
      ? [{ label: "Copy the agent's session id", icon: 'i-lucide-copy', onSelect: () => void copy(current.value!.agentSession!.id, "The agent's session id copied", `Its own session (${current.value!.agentSession!.source})`) }]
      : []),
  ],
  // Stop session… asks first, the palette's question as X asks it (UX3 R14).
  [{ label: 'Stop session…', icon: 'i-lucide-square', disabled: !liveNow.value, onSelect: askStop }],
])
/** The three marks a person may set on the session, as the palette's rows set them. */
const MARKS = [
  { label: 'Needs you', icon: 'i-lucide-hand', onSelect: () => signal('needs_input') },
  { label: 'Working', icon: 'i-lucide-loader-circle', onSelect: () => signal('working') },
  { label: 'Clear the mark', icon: 'i-lucide-x', onSelect: () => signal('clear') },
]

const terminal = ref<{ connection: SessionConnection; showQuestion: () => void; connect: () => void; focus: () => void; sendInput: (t: string) => boolean; submit: (t: string) => boolean; answer: (r: QuestionReply, o?: { sent?: () => void }) => Promise<boolean>; chat: (p: ChatPost) => boolean; chatSend: (s: ChatSend) => boolean; requestFile: (p: string, s?: boolean, x?: FileGetExtra) => Promise<any>; nvimOpen: (p: string) => Promise<NvimEvent>; nvimInput: EditorTerminal['nvimInput']; nvimClose: EditorTerminal['nvimClose']; nvimSwap: EditorTerminal['nvimSwap']; writeFile: EditorTerminal['writeFile']; takeSize: () => void; sizerShown: () => boolean; openPaste: () => void } | null>(null)

// The header's switch, Terminal | Chat (docs/plans/chat-or-terminal.md, design 11): on a session of an agent whose catalog entry
// has a protocol. The way and the noted move are the connection's (its welcome, its `way` messages); a press is the operator's
// POST /api/sessions/{id}/way. Its line is drawn above the terminal; below sm the two sides are items of More.
const agents = ref<AgentInfo[]>([])
const agentInfo = computed(() => agents.value.find((a) => a.id === (current.value?.agentId ?? '')))
// The exposed connection's refs, read whether or not the template ref unwrapped them.
const conn = computed(() => terminal.value?.connection as unknown as SessionConnection | undefined)
const way = computed<SessionWay>(() => unref(conn.value?.way) ?? (current.value?.way === 'protocol' ? 'protocol' : 'terminal'))
const moving = ref(false)
/** The agent's first reply has ended: the session's firstTurn, the fact the route judges a move to Chat by, or a chat already. */
const firstReply = computed(() => firstReplyOf(current.value, way.value))
// The route's refusal of the last press, with what it was judged on: it goes once any of that changes (the first reply ended, the
// conversation became known, the way changed) and at every `way` message; a refusal true for the session's life stays.
const refusalBasis = computed<RefusalBasis>(() => ({ way: way.value, firstReply: firstReply.value, resumable: !!current.value?.agentSession?.resumable }))
const refusal = ref<Refusal | null>(null)
watch(refusalBasis, (b) => {
  if (refusal.value && !refusalStands(refusal.value, b)) refusal.value = null
})
const switchFacts = computed<SwitchFacts>(() => ({
  hasProtocol: !!agentInfo.value?.protocol?.name || way.value === 'protocol',
  way: way.value,
  noted: unref(conn.value?.noted) ?? '',
  why: unref(conn.value?.why) ?? '',
  canMove: !ended.value,
  firstReply: firstReply.value,
  qualified: true,
  inTurn: attention.value.state === 'working',
  moving: moving.value,
  refused: refusal.value?.words || undefined,
  // A crew's member runs the way its crew runs it (UX3 R13): its other side is drawn dimmed, with why.
  member: !!current.value?.crew,
}))
const switched = computed(() => switchState(switchFacts.value))
// A press of a side that cannot be taken shows why, for a moment, as the line; nothing is sent.
const HELD_MS = 5000
const heldLine = ref('')
let heldTimer: ReturnType<typeof setTimeout> | undefined
function showHeld(why: string) {
  heldLine.value = why
  clearTimeout(heldTimer)
  heldTimer = setTimeout(() => (heldLine.value = ''), HELD_MS)
}
onBeforeUnmount(() => clearTimeout(heldTimer))
watch(id, () => (heldLine.value = ''))
/** The switch's line above the terminal: what a press did or why it waits; the at-rest line is the Chat side's own tooltip. */
const wayLine = computed(() => heldLine.value || (switched.value.shown && switched.value.line !== ENDS_TERMINAL_WORK ? switched.value.line : ''))
// Every `way` message ends what a press began: the move made, noted, taken back, or gone back with its sentence (a start that failed
// sends the way it was, unchanged, with why), so the switch never stays on "moving" after it.
let wayMessages = 0
watch(
  conn,
  (c, _, onCleanup) => {
    if (!c) return
    onCleanup(
      c.onControl((m) => {
        if (m.t !== 'way') return
        wayMessages++
        moving.value = false
        refusal.value = null
      }),
    )
  },
  { immediate: true },
)
/**
 * The note on the vendor's terms (design 11), once in this browser: the first time a person presses Chat. It is drawn in the chat,
 * under the move's notice (ChatView's ChatNote), where it covers nothing, until closed; under its sentence, that the chat has
 * Conductor's tools only.
 */
const TERMS_KEY = 'conductor.chatTermsNoted'
const chatNote = ref<{ site?: string } | null>(null)
function noteTerms() {
  try {
    if (localStorage.getItem(TERMS_KEY) === '1') return
    localStorage.setItem(TERMS_KEY, '1')
  } catch {
    /* a browser that keeps nothing shows it each time */
  }
  chatNote.value = { site: agentInfo.value?.site?.startsWith('https://') ? agentInfo.value.site : undefined }
}
// A move to Chat noted at the launch (the Launch agent dialog's Run as Chat, issue 1174) and made by itself is a press of Chat made
// in the dialog: the note on the vendor's terms is shown as for a press, once in this browser.
watch([way, () => switchFacts.value.noted], ([w], [was, wasNoted]) => {
  if (w === 'protocol' && was === 'terminal' && wasNoted === 'protocol') noteTerms()
})
async function moveTo(to: SessionWay) {
  if (!current.value) return
  if (to === 'protocol') noteTerms()
  refusal.value = null
  const basis = refusalBasis.value
  const before = wayMessages
  try {
    const r = await api.moveWay(current.value.id, to)
    // A move that was already made, or went back, before the route's answer came (its `way` message first) is not still moving.
    moving.value = !!r.moving && wayMessages === before
  } catch (e) {
    // Judged on the facts as the press was made: one that changed while the route answered has already made it untrue.
    const r = { words: e instanceof ApiError ? e.message : (e as Error).message, basis }
    refusal.value = refusalStands(r, refusalBasis.value) ? r : null
  }
}
const wayItems = computed(() =>
  switched.value.shown && !sm.value
    ? [
        (['terminal', 'protocol'] as const).map((w) => ({
          label: w === 'protocol' ? 'Chat' : 'Terminal',
          // What a move to Chat ends, said on the item before it is pressed, as the switch's Chat side says it; a side that cannot be
          // taken says why (UX3 R13).
          ...(w !== switched.value.selected && switched.value.held ? { description: switched.value.held } : w === 'protocol' && switched.value.line === ENDS_TERMINAL_WORK ? { description: ENDS_TERMINAL_WORK } : {}),
          type: 'checkbox' as const,
          checked: switched.value.selected === w,
          disabled: !switchFacts.value.canMove || !!switched.value.held || (w !== switched.value.selected && !switched.value.pressable),
          onSelect: () => w !== switched.value.selected && void moveTo(w),
        })),
      ]
    : [],
)
/** A chat's file change, opened in the editor's diff tab: the file against the folder's head. */
function openChange(e: TranscriptEntry) {
  const cwd = current.value?.cwd
  if (!cwd || !e.path || e.outside) return
  const abs = e.path.startsWith('/') ? e.path : joinTop(cwd, e.path)
  const parts = e.path.split('/')
  const name = parts.pop() ?? e.path
  openDiff({ path: e.path, abs, dir: parts.length ? `${parts.join('/')}/` : '', name, status: e.op === 'create' ? 'A' : e.op === 'delete' ? 'D' : 'M', added: e.added ?? 0, removed: e.removed ?? 0, binary: false }, { top: cwd })
}
/**
 * Where the question's card is drawn (UX3 R3): in Chat just above the chat's box, in the chat's column; in the Terminal, and in Chat
 * while the editor folds the pane under it (EditorColumn's terminalFolded), in the column's foot under the pane or its bar.
 */
const askInChat = computed(() => way.value === 'protocol' && !(tabs.value.tabs.length > 0 && !tabs.value.folded && (phone.value || !!tabs.value.collapsed)))
/**
 * The question the chat's box knows of (UX3 R4, R5): while one waits in Chat, its id, whether choices show and whether it takes a
 * typed reply (takesReply: not a permission request, a decision's mirror, a held turn or a startup question).
 */
const chatAsked = computed(() =>
  way.value === 'protocol' && attention.value.state === 'needs_input'
    ? { id: questionOf(attention.value), choices: offeredChoices(attention.value).length > 0, reply: takesReply(attention.value, 'control') }
    : null,
)
/** The reply bar's "Answer it in the chat.": the question in view. */
function showQuestion() {
  terminal.value?.showQuestion()
}

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

// A link to a session the server no longer holds (D7): its record's name and a way to the record, read once as the page loads.
const gone = ref<GonePage | null>(null)
const history = useHistory()
const goneTitle = computed(() => (gone.value?.kind === 'record' ? gone.value.name : 'An ended session'))
/** The name in the header, live or gone. */
const headName = computed(() => (gone.value ? goneTitle.value : current.value?.name || 'Session'))

// The name as the live store has it: a rename, here or anywhere, shows at once.
useHead({ title: headName })

// A name too long for the header is cut with an ellipsis, and its title shows it whole on hover (#906). A touch screen has no hover:
// while the name is cut it is a button that opens the whole name, wrapped, in a popover. Not cut, it is plain text.
const nameEl = ref<HTMLElement | null>(null)
const nameCut = useTextCut(nameEl, () => headName.value)
const nameHint = useId()
const NAME = 'truncate text-[15px] font-semibold'
const NAME_BUTTON = `${NAME} min-w-0 cursor-pointer rounded-sm text-left focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary`
const NAME_POPOVER = { side: 'bottom', align: 'start', collisionPadding: 16 } as const

async function load() {
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  try {
    const res = await api.get(id.value)
    if (!agents.value.length) void api.catalog().then((a) => (agents.value = a), () => undefined)
    session.value = res.session
    attention.value = liveAttention(res.session.attention ?? { state: '' })
    links.value = res.links ?? []
    error.value = ''
    ready.value = true
    gone.value = null
  } catch (e) {
    if (sessionGone(e)) {
      const read = await history.record(id.value).then(
        (r) => ({ ok: r.record }),
        (failed: unknown) => ({ failed }),
      )
      gone.value = gonePage(e, read)
      if (gone.value) {
        error.value = ''
        return
      }
    }
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

/** A line from the reply bar: `taken` tells the bar what became of it, so that a line not sent stays in its field (#654). */
function reply(text: string, question?: string, taken?: ReplyTaken) {
  void answerQuestion({ question, text }, () => taken?.('left')).then((outcome) => taken?.(outcome))
}

function option(index: number, question?: string) {
  const o = attention.value.options?.[index]
  if (!o) return
  // In a chat the choice's words are the answer, as a typed one is; in the terminal its keys, as ever (choiceReply).
  void answerQuestion({ question, ...choiceReply(o, way.value) }, undefined, false)
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
  gone.value = null
  viewers.value = []
  activity.value = []
  fileTarget.value = null
  previewUrl.value = null
  load()
})

// ?tab= (#303): the rail's item a link names, read as the page opens and as the query changes (never written back).
// `chat` and `people` both open People, where the people's chat and who is here are one view (docs/plans/chat-or-terminal.md, design 11).
const TAB_QUERY: Record<string, InspectorView> = { files: 'explorer', explorer: 'explorer', changes: 'changes', commits: 'commits', chat: 'people', activity: 'activity', people: 'people' }
watch(
  () => route.query.tab,
  (q) => {
    const v = typeof q === 'string' ? TAB_QUERY[q] : undefined
    if (!v) return
    if (v === 'people' && !xl.value) {
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
/** A phone folds the terminal under an open file by itself: the palette offers no collapse there, as the strip offers no button (#432). */
const phone = useIsPhone()
/**
 * Fit to my window is offered while the terminal shows and another window sets its size: not while the terminal is collapsed under the
 * open editor, as EditorColumn folds it wider than a phone (#432, review round 2), where its pane has no size to give the session.
 */
function fitOffered(): boolean {
  const t = tabs.value
  return !!terminal.value?.sizerShown() && !(t.tabs.length > 0 && !t.folded && !!t.collapsed && !phone.value)
}
/**
 * Shows a view (#332): the inspector on it, or the chat sheet below xl; one already shown stays shown. The header's Files and
 * Chat buttons toggle (showFiles, showChat); a palette row or a session row's Open … says "open" and never hides.
 */
function showView(v: InspectorView) {
  // Below xl People is the sheet: the people's chat and who is here.
  if (railView(v) === 'people' && !xl.value) {
    chatSheet.value = true
    return
  }
  tab.value = railView(v)
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
    ['activity', 'Open Activity', 'i-lucide-activity'],
    // People: the people's chat and who is here, one view.
    ['people', `Open People${chatUnread.value ? ` · ${chatUnread.value} unread` : ''}`, 'i-lucide-users'],
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
        actions: [{ id: 'revoke', kind: 'page' as const, target: l.id, label: 'Revoke', icon: 'i-lucide-ban', kbds: ['X'], ask: revokeQuestion(l.label), run: () => void revoke(l) }],
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
      // Not while the terminal is collapsed (#432, review round 2): its pane has no size to give the session.
      ...(fitOffered() ? [{ key: 'page:fit', label: 'Fit to my window', icon: 'i-lucide-maximize-2', run: () => terminal.value?.takeSize() }] : []),
      ...(t.tabs.length ? [{ key: 'page:fold', label: t.folded ? 'Bring the editor back' : 'Fold the editor to its tab strip', icon: 'i-lucide-panel-bottom', kbds: ['T'], run: () => (tabs.value = toggleFold(tabs.value)) }] : []),
      // Its mirror (#432): the terminal collapses to a bar under the editor, and back; the strip's button does the same.
      ...(t.tabs.length && !phone.value ? [{ key: 'page:collapse-terminal', label: t.collapsed && !t.folded ? 'Bring the terminal back' : 'Collapse the terminal', icon: t.collapsed && !t.folded ? 'i-lucide-panel-bottom-open' : 'i-lucide-panel-bottom-close', run: () => (tabs.value = toggleCollapse(tabs.value)) }] : []),
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
      <div ref="head" class="flex min-w-0 flex-none flex-col">
      <MemberStrip v-if="memberRun && !gone && !stripInline" :run-id="memberRun" :session-id="id" @gone="stripGone = memberRun" />
      <UDashboardNavbar :toggle="false" :ui="{ root: 'h-14 bg-default', title: 'min-w-0' }" data-session-bar>
        <template #leading>
          <!-- A member's way back is its strip's: above (UX3 R12), or here on a very wide header (UX3 R12b). -->
          <MemberStrip v-if="memberRun && !gone && stripInline" inline :run-id="memberRun" :session-id="id" @gone="stripGone = memberRun" />
          <IconButton v-if="!memberRun" icon="i-lucide-arrow-left" color="neutral" variant="ghost" label="Back to the list" class="lg:hidden" to="/sessions" data-back-to-list />
        </template>
        <template #title>
          <div v-if="gone" class="flex min-w-0 items-center gap-2" data-session-gone-head>
            <UPopover v-if="nameCut" :content="NAME_POPOVER">
              <button ref="nameEl" type="button" :class="NAME_BUTTON" :title="headName" :aria-describedby="nameHint" data-session-title>{{ headName }}</button>
              <template #content><p class="max-w-[min(28rem,calc(100vw-2rem))] p-3 text-sm font-semibold text-highlighted [overflow-wrap:anywhere]" data-session-title-whole>{{ headName }}</p></template>
            </UPopover>
            <span v-else ref="nameEl" :class="NAME" :title="headName" data-session-title>{{ headName }}</span>
            <span :id="nameHint" hidden>Show the whole name</span>
            <StatusMark :status="statusOfKey('exited')" word class="text-xs" data-session-status="gone" />
          </div>
          <div v-else class="flex min-w-0 flex-col">
            <div class="flex items-center gap-2 min-w-0">
              <UPopover v-if="nameCut" :content="NAME_POPOVER">
                <button ref="nameEl" type="button" :class="NAME_BUTTON" :title="headName" :aria-describedby="nameHint" data-session-title>{{ headName }}</button>
                <template #content><p class="max-w-[min(28rem,calc(100vw-2rem))] p-3 text-sm font-semibold text-highlighted [overflow-wrap:anywhere]" data-session-title-whole>{{ headName }}</p></template>
              </UPopover>
              <span v-else ref="nameEl" :class="NAME" :title="headName" data-session-title>{{ headName }}</span>
              <span :id="nameHint" hidden>Show the whole name</span>
              <StatusMark v-if="status" :status="status" word class="text-xs" :data-session-status="current?.id" />
              <YoloBadge v-if="current?.yolo" />
              <WaySwitch v-if="sm" :facts="switchFacts" :line="false" class="flex-none" @move="moveTo" @held="showHeld" />
            </div>
            <span v-if="headline" class="truncate font-mono text-[11.5px] text-muted" :title="headline.title" data-session-meta>{{ headline.line }}</span>
          </div>
        </template>
        <template #right>
          <template v-if="!gone">
            <TransportBadge :kind="transport.kind" :state="transport.state" :rtt="transport.rtt" compact class="hidden md:inline-flex" />
            <ViewerAvatars :viewers="viewers" class="hidden md:flex" />
            <!-- Icons only on a phone, as on the run page: the labels would push the name off the bar. Below sm Files, Share and
                 Fullscreen are the first items of More (D9), so that the name keeps its room. -->
            <!-- Files and Chat only below xl (#329): at xl the inspector rail has both, with the chat's count; here they said it twice. -->
            <UButton icon="i-lucide-folder-open" color="neutral" variant="outline" aria-label="Files" class="hidden sm:inline-flex xl:hidden" :class="inspector && tab === 'explorer' && 'ring-2 ring-primary/40'" @click="showFiles">Files</UButton>
            <UButton icon="i-lucide-share-2" color="neutral" variant="outline" aria-label="Share" class="hidden sm:inline-flex" data-share-button @click="share = true">Share</UButton>
            <!-- The chat (design 2b, 2c): the count while the thread is closed; the inspector's tab at xl, a sheet below. -->
            <IconButton v-if="chat.thread.value.capable" icon="i-lucide-users" color="neutral" variant="outline" label="People" class="xl:hidden" :class="chatShown && 'ring-2 ring-primary/40'" data-chat-button @click="showChat">
              <span class="hidden sm:inline">People</span>
              <ChatUnreadPill :count="chatUnread" />
            </IconButton>
            <UDropdownMenu :items="menu">
              <IconButton icon="i-lucide-ellipsis" color="neutral" variant="outline" label="More" data-more-button />
            </UDropdownMenu>
          </template>
          <div class="hidden sm:flex"><FullscreenButton /></div>
        </template>
      </UDashboardNavbar>
      </div>
    </template>

    <template #body>
      <!-- A session the server no longer holds (D7): what became of it, and the one way on, its record or History. -->
      <UEmpty
        v-if="gone"
        icon="i-lucide-history"
        :title="gone.kind === 'forgotten' ? 'Nothing of this session is kept.' : 'This session ended, and this server no longer holds it.'"
        :description="gone.kind === 'unread' ? gone.words : undefined"
        :actions="gone.kind === 'record' ? [{ label: 'Open its record', to: `/history/sessions/${encodeURIComponent(id)}`, color: 'neutral', variant: 'outline' }] : gone.history ? [{ label: 'Open History', to: '/history', color: 'neutral', variant: 'outline' }] : []"
        class="m-auto"
        :data-session-gone="gone.kind"
      />
      <UAlert v-else-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" class="m-4" />
      <div v-else-if="ready && session" class="flex flex-1 min-h-0">
        <EditorColumn ref="columnRef" v-model:tabs="tabs" :request="requestFile" :nvim="editor.nvim.value" :write="editor.writeFile" :can-edit="editor.canEdit.value" commenting :can-ask="!ended" :cwd="current?.cwd" :raw-url="rawUrl" :host-away="hostAway" :bar-text="attention.message || agentLabel" class="p-3" @comment="onComment">
          <!-- The first message's wait (P19), said above the terminal: the inspector, where Activity says it too, starts closed. -->
          <div class="flex h-full min-h-0 flex-col gap-2">
            <p v-if="wayLine" class="flex flex-none items-center gap-2 rounded-md border border-default bg-elevated/60 px-3 py-1.5 text-xs text-muted" role="status" data-way-line>
              <UIcon name="i-lucide-arrow-left-right" class="size-3.5 flex-none" aria-hidden="true" />
              <span class="min-w-0">{{ wayLine }}</span>
            </p>
            <p v-if="firstLine" class="flex flex-none items-center gap-2 rounded-md border border-default bg-elevated/60 px-3 py-1.5 text-xs text-muted" data-first-message>
              <UIcon name="i-lucide-message-square-text" class="size-3.5 flex-none" aria-hidden="true" />
              <span class="min-w-0 truncate" :title="firstLine">{{ firstLine }}</span>
            </p>
            <div class="relative min-h-0 flex-1">
              <TerminalView
                ref="terminal"
                :create-transport="createTransport"
                operator
                opens-changes
                :chat-note="chatNote"
                :agent-name="agentLabel"
                :draft-key="id"
                :asked="chatAsked"
                @reply="reply"
                @dismiss-note="chatNote = null"
                :verbatim="current?.way === 'protocol' ? !!current.verbatim : undefined"
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
                @open-change="openChange"
                @link-opened="linkOpened"
                @link-offered="linkOffered"
              >
                <!-- In Chat the question's card sits just above the chat's box (UX3 R3), unless the editor folds the pane away. -->
                <template v-if="askInChat" #ask>
                  <QuickReplyBar v-if="!answersInChat(attention)" :attention="attention" :agent-name="agentLabel" role="control" answers pastes :field="false" @answer="decisionModal.open" @chat="showQuestion" @paste="terminal?.openPaste()" @reply="reply" @option="option" @open-plan="openFile({ path: $event })" @preview-plan="previewFile({ path: $event })" />
                </template>
              </TerminalView>
            </div>
          </div>
          <div v-if="typingLine" class="pointer-events-none absolute bottom-2 left-3 flex items-center gap-2 rounded bg-default/80 px-2 py-0.5 text-xs text-muted backdrop-blur-sm"><span class="inline-block h-3.5 w-1.5 bg-muted/70" />{{ typingLine }}</div>
          <template #bar>
            <!-- An ended session says so at its foot, with its way on (UX3 R16): Resume, or Relaunch when there is nothing to resume. -->
            <div v-if="ended && current" class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-default bg-elevated/50 px-3 py-2 text-sm text-default" role="status" data-session-ended>
              <span class="min-w-0">This session ended.</span>
              <ResumeButton :session="current" class="ml-auto flex-none" />
            </div>
            <!-- A chat's permission question is answered in the chat, which shows it: no bar repeats it (only for a terminal's question). -->
            <QuickReplyBar v-else-if="!askInChat && !(way === 'protocol' && answersInChat(attention))" :attention="attention" :agent-name="agentLabel" role="control" answers pastes @answer="decisionModal.open" @chat="showQuestion" @paste="terminal?.openPaste()" @reply="reply" @option="option" @open-plan="openFile({ path: $event })" @preview-plan="previewFile({ path: $event })" />
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

  <ShareLinksModal v-model:open="share" :session-id="id" :session-name="current?.name" :chat="way === 'protocol'" />
  <ChatSheet v-model:open="chatSheet" title="People" :thread="chat.thread.value" role="control" :ended="ended" :offline="chatOffline" :viewers="viewers" @send="chatSend" @send-to-agent="chatSendToAgent" @retry="chatRetry" @answer="chatAnswer" />
</template>
