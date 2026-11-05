<script setup lang="ts">
import { SPONSOR_URL } from '~/utils/about'
import type { JoinInfo, JoinRunMember, SessionKind } from '~/composables/useSessions'
import type { Attention, ChatHistory, ChatMessage, ChatPost, ChatSend, TransportKind, ViewerInfo, Welcome, ChatQuote, ActivityEntry } from '~/utils/protocol'
import { closedByTheEnd } from '~/utils/protocol'
import type { CloseInfo, TransportState } from '~/utils/transport/types'
import { emptyTabs } from '~/utils/editorTabs'
import { joinServer } from '~/utils/invite'
import { filesRoot, joinedFromInfo } from '~/utils/joined'
import { chatThreadKey, scopeItems } from '~/utils/chat'
import { skipWords, type MemberStatus } from '~/utils/crews'
import { repliedQuestions, ReplyError, replyFailure, type QuestionReply } from '~/utils/reply'

// The workbench (the desktop app, or a browser holding the workbench token) shows the page beside its sidebar, so a shared session
// never takes the window over and its own sessions stay one click away; a guest gets the bare page. Decided once: a layout that
// changed under a live terminal would remount the page and drop it.
// Keyed by the token and the server: a join page for another server (an invite for the same token, say) is a new page, with its own
// session, metadata and connection, never the old one's.
definePageMeta({ layout: false, key: (route) => `${String(route.params.token)}|${String(route.query.server ?? '')}` })
const admin = useWorkbenchToken()
const desktop = useDesktop()
const inWorkbench = admin.hasToken.value || desktop.isDesktop.value
const kept = useJoined()

const route = useRoute()
const api = useSessions()
const toast = useToast()
const identity = useIdentity()
const { create } = useTerminalTransport()

const token = computed(() => String(route.params.token))
/** The server this page talks to for the link: another one (an invite's switchyard, `?server=`) or its own (''). */
const server = computed(() => joinServer(String(route.query.server ?? '')))
const serverHost = computed(() => {
  try {
    return server.value ? new URL(server.value).host : ''
  } catch {
    return ''
  }
})
const info = ref<JoinInfo | null>(null)
const error = ref('')
const joined = ref(false)
const nameDraft = ref('')
const viewers = ref(0)
const viewerList = ref<ViewerInfo[]>([])
function onViewers(info: { count: number; list?: ViewerInfo[] }) {
  viewers.value = info.count
  viewerList.value = info.list ?? []
}
const status = ref('')
const transport = ref<{ kind: TransportKind; state: TransportState; rtt: number | null }>({ kind: 'ws', state: 'idle', rtt: null })
// The editor area (design 4g): files open above the terminal, the Files pane beside it where there is room.
const { tabs, openFile, previewFile, openUrl, openDiff, openCommitDiff } = useEditorTabs()
const filesPanel = ref(true)
const terminal = ref<EditorTerminal & { requestFile: (p: string, s?: boolean, x?: FileGetExtra) => Promise<any>; sendInput: (t: string) => boolean; submit: (t: string) => boolean; answer: (r: QuestionReply) => Promise<boolean>; chat: (p: ChatPost) => boolean; chatSend: (s: ChatSend) => boolean } | null>(null)
const attention = ref<Attention>({ state: '' })
function onAttention(msg: { state: string; message?: string; source?: string }) {
  attention.value = { ...(msg as Attention), since: new Date().toISOString() }
  hearFocused({ attention: msg.state })
}
function onStatus(s: string) {
  status.value = s
  hearFocused({ status: s })
}
/** The connection closed: what was being said waits; a close for the session's end (a server's or a hosted one's) says so, should the status frame not have been read first. */
function onClosed(info: CloseInfo) {
  chat.offline()
  // The editor's Neovims ended with the connection: their tabs keep their text and say so.
  editor.onClosed()
  if (closedByTheEnd(info.code, info.reason) && !ended.value) onStatus('exited')
}

// A run link opens every member of a crew run: a tile each, and one member
// at a time in full with the link's role (`focus`). A session link opens its
// session in full.
const run = computed(() => info.value?.run)
const focus = ref<JoinRunMember | null>(null)
/** The session shown in full: a session link's, or the member opened from a run link's tiles. */
/** The session's folder as the session's own welcome named it: a switchyard lists only its last element (filesRoot). */
const ownerCwd = ref('')
const current = computed<{ id: string; name: string; agentId: string; kind: SessionKind; hostName?: string; hostUser?: string; cwd?: string } | null>(() => {
  const s = info.value?.session
  if (s) return { ...s, cwd: filesRoot(s.cwd, ownerCwd.value) }
  const m = focus.value
  // Crew members run on the server.
  return m?.sessionId ? { id: m.sessionId, name: m.name, agentId: m.agentId, kind: m.kind ?? 'server' } : null
})

/** What each member's tile last heard from its session, by member name: kept while a member is open in full, for the tiles on the way back (backToCrew). */
const tileState = ref<Record<string, { status?: string; attention?: string }>>({})
/**
 * What the full view hears of the member open in it goes to that member's tile too, so Back shows the truth: a tile that joins again is
 * told of a prompt only while one is live, never that one was cleared.
 */
function hearFocused(patch: { status?: string; attention?: string }) {
  const name = focus.value?.name
  if (!name) return
  tileState.value = { ...tileState.value, [name]: { ...tileState.value[name], ...patch } }
}

// The chat for whoever holds the link (design 2d): the session's own thread, over this page's connection, so it works through a
// switchyard too. A panel beside the terminal where there is room, a sheet over it on a phone (2c). A view-only guest can talk
// and never reaches the agent; the owner's welcome says whether chat exists at all.
const chatKey = computed(() => chatThreadKey('session', current.value?.id ?? '', server.value))
const chat = useChat(chatKey)
const unread = useChatUnread()
const chatUnread = computed(() => unread.count(chatKey.value))
const chatOffline = computed(() => transport.value.state !== 'open')
const ended = computed(() => status.value === 'exited' || status.value === 'stopped')
const chatNote = computed(() => (info.value?.role === 'control' ? '' : 'You are view only: what you write reaches the people here, not the agent.'))
const wide = useMedia('(min-width: 48rem)')
const chatPanel = ref(true)
const chatSheet = ref(false)
/** The thread is in front of the person: the panel beside the terminal where it shows, or the sheet. */
const chatShown = computed(() => joined.value && !!current.value && ((wide.value && chatPanel.value) || chatSheet.value))
function showChat() {
  if (wide.value) chatPanel.value = !chatPanel.value
  else chatSheet.value = true
}
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
const viaChat = (post: ChatPost) => terminal.value?.chat(post) ?? false
const viaChatSend = (send: ChatSend) => terminal.value?.chatSend(send) ?? false
// The editor (design round 12, F6 and F8): a guest with control saves and gets Neovim as the owner does; a view link gets neither.
const editor = useEditorBridge(terminal as unknown as Ref<EditorTerminal | null>, () => current.value?.hostName || 'the host')
// The session's activity, for the Files pane's Touched section (design 4e).
const activity = ref<ActivityEntry[]>([])
function onActivity(e: ActivityEntry) {
  activity.value = [...activity.value.slice(-199), e]
}
function onWelcome(w: Welcome) {
  activity.value = []
  ownerCwd.value = w.cwd ?? ''
  editor.onWelcome(w)
  unread.registerSelf(w.subscriberId ?? w.viewerId ?? '')
  chat.welcome(!!w.chat, viaChat)
}
function onChat(m: ChatMessage) {
  // The run's chat comes over its own connection (useRunChat); a member's frames of it are dropped here.
  if (m.scope !== 'run') chat.accept(m, { live: true })
}
function onChatHistory(h: ChatHistory) {
  if (h.scope !== 'run') chat.history(h)
}
// Comments on lines (F7): posted to this session's chat; a controller may ask the agent. A quote card opens the file here.
provideChatQuotes(quoteActionsFor(requestFile, () => current.value?.cwd, openFile))
function onComment(c: { quote: ChatQuote; text: string; toAgent: boolean }) {
  chat.send(c.text, { quote: c.quote, ...(c.toAgent ? { to: 'agent' } : {}) }, viaChat)
}
function chatSend(text: string, to: string) {
  chat.send(text, to ? { to } : {}, viaChat)
}

// The run's chat for a run link (design 2f): one thread for everyone on the link, over a quiet connection of its own to a live
// member, read and posted beside the tiles and while a member is open in full (`on` names that member). A view-only guest talks
// here and reaches no member; a guest with control may also have a message typed into a running member.
const runMembers = computed(() => info.value?.run?.members ?? [])
const runChat = useRunChat(
  computed(() => info.value?.run?.id ?? ''),
  runMembers,
  { token, server },
)
const runChatOpen = ref(false)
const runChatUnread = computed(() => unread.count(runChat.key.value))
const runChatShown = computed(() => joined.value && !!run.value && runChatOpen.value)
watch(
  [() => runChat.key.value, runChatShown],
  ([key, shown]) => {
    if (!key.endsWith(':')) {
      if (shown) unread.openThread(key)
      else unread.closeThread(key)
    }
  },
  { immediate: true },
)
const runChatNote = computed(() => (info.value?.role === 'control' ? '' : 'You are view only: you can talk here; nothing you write reaches a member.'))
/** The members' states for the composer's menu: the tiles' word on a prompt, else the run's status. */
const runStates = computed(() => new Map(runMembers.value.map((m) => [m.name, (tileState.value[m.name]?.attention === 'needs_input' ? 'needs_input' : m.status) as MemberStatus])))
const runScope = computed(() => scopeItems(runMembers.value, runStates.value))
const runTargets = computed(() => runScope.value.slice(1))
const runChatDescription = computed(() => (run.value ? `${run.value.name} · kept with the run` : ''))
function runChatSend(text: string, to: string) {
  runChat.send(text, to, focus.value?.name ?? '')
}
function runChatSendTo(ref: string, to: string) {
  if (!runChat.sendTo(ref, to)) toast.add({ title: 'Not connected', color: 'warning' })
}
watch(runChat.refused, (r) => {
  if (r) toast.add({ title: 'Not sent to the member', description: skipWords(r.reason), color: 'warning' })
})
/** A member's question answered from the run's chat: the choice's keys into that member, over the link. */
const quick = useQuickReply()
function runChatAnswer(m: ChatMessage, index: number) {
  const o = m.options?.[index]
  const member = runMembers.value.find((x) => x.name === m.on)
  if (!o || !member?.sessionId) return
  // The question's line in the chat is the question's own id.
  quick.send({ id: member.sessionId, kind: member.kind ?? 'server' }, o.input, { token: token.value, server: server.value, question: m.id }).catch((e: unknown) => toast.add(replyFailure(e, member.name)))
}
function chatSendToAgent(ref: string) {
  if (!chat.sendToAgent(ref, viaChatSend)) toast.add({ title: 'Not connected', color: 'warning' })
}
function chatRetry(nonce: string) {
  chat.retry(nonce, viaChat)
}
/** A question's choice from the session's chat: its keys into the session, naming the question (its line is the question's id). */
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
  toast.add({ title: 'Not sent to the agent', description: err.message, color: 'warning' })
}

useHead({ title: computed(() => (run.value ? `${run.value.name} (shared)` : info.value?.session ? `${info.value.session.name} (shared)` : 'Join session')) })

function agentName(agentId: string): string {
  const names: Record<string, string> = { claude: 'Claude Code', codex: 'Codex', agy: 'Antigravity' }
  return names[agentId] ?? agentId
}
const agentLabel = computed(() => agentName(current.value?.agentId ?? ''))
/** The switchyard this link was shared through, named on the page: the invite's server, else this page's own host. */
const viaHost = computed(() => serverHost.value || (import.meta.client ? location.host : ''))
const hostedBy = computed(() => {
  const s = current.value
  if (!s) return ''
  return s.kind === 'hosted' ? `hosted by ${s.hostUser || s.hostName || 'a developer'}` : 'running on the server'
})

// The join API returns only metadata; nothing connects until the guest
// presses Join, so a fetched link never exposes terminal content.
async function fetchInfo() {
  try {
    info.value = await api.join(token.value, server.value)
    if (info.value.session) status.value = info.value.session.status
    error.value = ''
    kept.noteInfo(server.value, token.value, info.value)
  } catch (e) {
    error.value = (e as Error).message
  }
}

onMounted(async () => {
  nameDraft.value = identity.name.value
  await fetchInfo()
  // A link kept under Shared with you joins at once: the person joined it before, under this name.
  if (inWorkbench && info.value && !error.value && identity.name.value && kept.find(server.value, token.value)) join()
})

function join() {
  identity.set(nameDraft.value)
  left.value = false
  joined.value = true
  // Kept in the workbench's sidebar; a guest's browser keeps nothing.
  if (inWorkbench && info.value) kept.add({ token: token.value, server: server.value, host: viaHost.value, ...joinedFromInfo(info.value) })
}

/** What the person left, said on the card they come back to; the link still works, so Join again is one click. */
const left = ref(false)

/** Leaves the share: the terminals go (their connections close with them) and the card comes back, the name kept. */
function leave() {
  joined.value = false
  focus.value = null
  tileState.value = {}
  attention.value = { state: '' }
  tabs.value = emptyTabs()
  chatSheet.value = false
  runChatOpen.value = false
  left.value = true
}

function createTransport() {
  const s = current.value!
  return create({ sessionId: s.id, token: token.value, kind: s.kind, name: identity.name.value, server: server.value })
}

function tileTransport(m: JoinRunMember) {
  return () => create({ sessionId: m.sessionId!, token: token.value, kind: m.kind ?? 'server', name: identity.name.value, server: server.value })
}

function openMember(m: JoinRunMember, heard?: { status?: string; attention?: string }) {
  if (!m.sessionId) return
  status.value = heard?.status ?? m.status
  attention.value = { state: '' }
  viewers.value = 0
  transport.value = { kind: 'ws', state: 'idle', rtt: null }
  tabs.value = emptyTabs()
  focus.value = m
}

function backToCrew() {
  // The tiles keep their status. Of their attention only the open member's
  // stays, which the full view kept true: another member's prompt may have
  // been answered meanwhile, and a tile that joins again is told of a prompt
  // only while one is live, so a live one comes back as its tile reattaches.
  const open = focus.value?.name
  const next: Record<string, { status?: string; attention?: string }> = {}
  for (const [name, heard] of Object.entries(tileState.value)) next[name] = name === open ? { ...heard } : { status: heard.status }
  tileState.value = next
  focus.value = null
  // Members may have started or ended meanwhile.
  fetchInfo()
}

function reply(text: string, question?: string) {
  answerQuestion({ question, text })
}

function option(index: number, question?: string) {
  const o = attention.value.options?.[index]
  if (o) answerQuestion({ question, input: o.input })
}

function requestFile(path: string, stat?: boolean, extra?: FileGetExtra) {
  if (!terminal.value) return Promise.reject(new Error('terminal not ready'))
  return terminal.value.requestFile(path, stat, extra)
}
</script>

<template>
  <NuxtLayout :name="inWorkbench ? 'default' : 'bare'">
    <JoinFrame :workbench="inWorkbench">
  <template v-if="!joined">
    <main class="flex flex-1 flex-col items-center justify-center gap-4 p-6">
      <div class="flex w-full max-w-sm flex-col gap-4 rounded-lg border border-default bg-default p-6 shadow-sm">
        <img src="/brand/conductor-mark.svg" alt="" class="size-8 dark:hidden" />
        <img src="/brand/conductor-mark-reversed.svg" alt="" class="size-8 hidden dark:block" />
        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-link-2-off" title="This link cannot be used" :description="error" />
        <UAlert v-else-if="left" color="neutral" variant="subtle" icon="i-lucide-log-out" title="You left" description="Nothing more reaches this page. The link still works: join again below, or close the tab." data-join-left />
        <template v-if="info && !error">
          <div v-if="run" class="flex flex-col gap-1">
            <h1 class="text-lg font-semibold tracking-tight">Join {{ run.name }}</h1>
            <p class="text-sm leading-relaxed text-muted">
              A crew of {{ run.members.length }} {{ run.members.length === 1 ? 'agent' : 'agents' }} running on the server.
              <template v-if="info.role === 'control'">You'll have <b class="text-default">control</b>: your keystrokes reach the agent you open.</template>
              <template v-else>You'll be <b class="text-default">view only</b>: you can watch every agent and open files.</template>
            </p>
          </div>
          <div v-else-if="current" class="flex flex-col gap-1">
            <h1 class="text-lg font-semibold tracking-tight">Join {{ current.name }}</h1>
            <p class="text-sm leading-relaxed text-muted">
              A live {{ agentLabel }} session {{ hostedBy }}.
              <template v-if="info.role === 'control'">You'll have <b class="text-default">control</b>: your keystrokes reach the agent.</template>
              <template v-else>You'll be <b class="text-default">view only</b>: you can watch and open files.</template>
            </p>
          </div>
          <form class="flex flex-col gap-3" @submit.prevent="join">
            <UFormField label="Your name" name="name" hint="shown to others">
              <UInput v-model="nameDraft" class="w-full" autofocus maxlength="40" />
            </UFormField>
            <UButton type="submit" :label="run ? 'Join crew' : 'Join session'" block :disabled="!nameDraft.trim()" />
          </form>
          <p v-if="info.switchyard && viaHost" class="text-xs text-muted" data-join-switchyard>Shared through <span class="font-mono">{{ viaHost }}</span></p>
        </template>
        <div v-else class="text-sm text-muted flex items-center gap-2"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Checking link…</div>
      </div>
      <!-- The Sponsor Kit's badge under the card, on a switchyard's own join page (a guest's), never inside the workbench. -->
      <a v-if="!inWorkbench && info?.switchyard" :href="SPONSOR_URL" target="_blank" rel="noopener" class="inline-flex h-9 w-max items-center gap-2.5 rounded-md border border-[#ddd] bg-white px-3.5 no-underline dark:border-[#333] dark:bg-black" data-join-credit>
        <span class="text-[11px] font-bold uppercase tracking-[.04em] text-[#777] dark:text-[#999]">Sponsored by</span>
        <img src="/sponsor/rocksolidlabs-logo.png" alt="RockSolid Labs" class="block h-[18px] w-auto dark:hidden" />
        <img src="/sponsor/rocksolidlabs-logo-reversed.png" alt="RockSolid Labs" class="hidden h-[18px] w-auto dark:block" />
      </a>
    </main>
  </template>

  <template v-else-if="run && !current">
    <header class="flex items-center gap-3 border-b border-default px-4 py-2">
      <UButton v-if="inWorkbench" icon="i-lucide-arrow-left" color="neutral" variant="ghost" size="sm" aria-label="Back to the list" class="lg:hidden" to="/sessions" data-back-to-list />
      <template v-if="!inWorkbench">
        <img src="/brand/conductor-mark.svg" alt="" class="size-6 dark:hidden" />
        <img src="/brand/conductor-mark-reversed.svg" alt="" class="size-6 hidden dark:block" />
        <span class="font-semibold">Conductor</span>
        <USeparator orientation="vertical" class="h-5" />
      </template>
      <span class="truncate">{{ run.name }}</span>
      <UBadge :label="info!.role === 'control' ? 'control' : 'view only'" :icon="info!.role === 'control' ? 'i-lucide-keyboard' : 'i-lucide-eye'" :color="info!.role === 'control' ? 'primary' : 'neutral'" variant="subtle" size="sm" />
      <UBadge :label="`${run.members.length} ${run.members.length === 1 ? 'agent' : 'agents'}`" icon="i-lucide-users" color="neutral" variant="subtle" size="sm" />
      <div class="flex-1" />
      <span v-if="!inWorkbench" class="text-xs text-muted hidden md:inline">you are <b class="text-default">{{ identity.name.value }}</b></span>
      <UButton icon="i-lucide-refresh-cw" color="neutral" variant="ghost" size="sm" aria-label="Refresh the members" @click="fetchInfo" />
      <UButton icon="i-lucide-message-circle" color="neutral" variant="outline" size="sm" aria-label="Run chat" :class="runChatOpen && 'ring-2 ring-primary/40'" data-run-chat-button :data-run-chat-offline="runChat.offline.value ? '' : undefined" @click="runChatOpen = true">
        <span class="hidden sm:inline">Chat</span>
        <ChatUnreadPill :count="runChatUnread" />
      </UButton>
      <UButton icon="i-lucide-log-out" color="neutral" variant="outline" size="sm" aria-label="Leave" data-join-leave @click="leave"><span class="hidden sm:inline">Leave</span></UButton>
      <FullscreenButton size="sm" />
    </header>

    <main class="flex-1 min-h-0 overflow-y-auto p-2 sm:p-3">
      <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" class="mb-3" />
      <JoinCrewGrid v-model:heard="tileState" :members="run.members" :role="info!.role" :transport-for="tileTransport" :agent-name="agentName" @open="openMember" />
    </main>
    <ChatSheet
      v-model:open="runChatOpen"
      scope="run"
      title="Run chat"
      :description="runChatDescription"
      :thread="runChat.thread.value"
      :role="info!.role"
      :offline="runChat.offline.value"
      :viewers="runChat.people.value"
      :note="runChatNote"
      :scope-items="runScope"
      :send-targets="runTargets"
      @send="runChatSend"
      @send-to="runChatSendTo"
      @retry="runChat.retry"
      @answer="runChatAnswer"
    />
  </template>

  <template v-else-if="info && current">
    <header class="flex items-center gap-3 border-b border-default px-4 py-2">
      <UButton v-if="inWorkbench && !run" icon="i-lucide-arrow-left" color="neutral" variant="ghost" size="sm" aria-label="Back to the list" class="lg:hidden" to="/sessions" data-back-to-list />
      <UTooltip v-if="run" text="Back to the crew">
        <UButton icon="i-lucide-arrow-left" color="neutral" variant="ghost" size="sm" aria-label="Back to the crew" @click="backToCrew" />
      </UTooltip>
      <template v-if="!inWorkbench">
        <img src="/brand/conductor-mark.svg" alt="" class="size-6 dark:hidden" />
        <img src="/brand/conductor-mark-reversed.svg" alt="" class="size-6 hidden dark:block" />
        <span class="font-semibold">Conductor</span>
        <USeparator orientation="vertical" class="h-5" />
      </template>
      <span class="truncate"><template v-if="run">{{ run.name }} · </template>{{ current.name }}</span>
      <UBadge :label="info.role === 'control' ? 'control' : 'view only'" :icon="info.role === 'control' ? 'i-lucide-keyboard' : 'i-lucide-eye'" :color="info.role === 'control' ? 'primary' : 'neutral'" variant="subtle" size="sm" />
      <SessionStatusBadge v-if="status" :status="status as any" />
      <AttentionBadge :attention="attention" />
      <TransportBadge :kind="transport.kind" :state="transport.state" :rtt="transport.rtt" />
      <UBadge :label="`${viewers} here`" icon="i-lucide-users" color="neutral" variant="subtle" size="sm" />
      <UBadge v-if="current.kind === 'hosted'" :label="`hosted on ${current.hostName || 'dev machine'}`" icon="i-lucide-laptop" color="neutral" variant="subtle" size="sm" />
      <div class="flex-1" />
      <span v-if="!inWorkbench" class="text-xs text-muted hidden md:inline">you are <b class="text-default">{{ identity.name.value }}</b></span>
      <ViewerAvatars :viewers="viewerList" class="hidden lg:flex" />
      <UButton icon="i-lucide-folder-open" color="neutral" variant="outline" size="sm" aria-label="Files" class="hidden md:inline-flex" :class="filesPanel && 'ring-2 ring-primary/40'" data-files-button @click="filesPanel = !filesPanel"><span>Files</span></UButton>
      <!-- The chat (design 2c, 2d): the count while the thread is closed; the panel beside the terminal where there is room, a sheet below. -->
      <UButton v-if="chat.thread.value.capable" icon="i-lucide-message-circle" color="neutral" variant="outline" size="sm" aria-label="Chat" :class="chatShown && 'ring-2 ring-primary/40'" data-chat-button @click="showChat">
        <span class="hidden sm:inline">Chat</span>
        <ChatUnreadPill :count="chatUnread" />
      </UButton>
      <UButton v-if="run" icon="i-lucide-messages-square" color="neutral" variant="outline" size="sm" aria-label="Run chat" :class="runChatOpen && 'ring-2 ring-primary/40'" data-run-chat-button :data-run-chat-offline="runChat.offline.value ? '' : undefined" @click="runChatOpen = true">
        <span class="hidden sm:inline">Run chat</span>
        <ChatUnreadPill :count="runChatUnread" />
      </UButton>
      <UButton icon="i-lucide-log-out" color="neutral" variant="outline" size="sm" aria-label="Leave" data-join-leave @click="leave"><span class="hidden sm:inline">Leave</span></UButton>
      <FullscreenButton size="sm" />
    </header>

    <main class="flex-1 min-h-0 p-2 sm:p-3 flex gap-3">
      <EditorColumn v-model:tabs="tabs" :request="requestFile" :nvim="editor.nvim.value" :write="editor.writeFile" :can-edit="editor.canEdit.value" :cwd="current.cwd" :host-away="status === 'host_disconnected' ? current.hostName || 'The host' : undefined" :bar-text="attention.message || agentLabel" :read-only-badge="info.role !== 'control'" commenting :can-ask="info.role === 'control' && !ended" @comment="onComment">
        <TerminalView
          :key="current.id"
          ref="terminal"
          :create-transport="createTransport"
          :read-only="info.role !== 'control'"
          @welcome="onWelcome"
          @activity="onActivity"
          @nvim="editor.onNvim"
          @status="onStatus"
          @attention="onAttention"
          @viewers="onViewers"
          @transport="transport = $event"
          @closed="onClosed"
          @chat="onChat"
          @chat-history="onChatHistory"
          @request-error="onRequestError"
          @open-file="openFile"
          @open-url="openUrl"
        />
        <template #bar>
          <QuickReplyBar :attention="attention" :agent-name="agentLabel" :role="info.role" @reply="reply" @option="option" @open-plan="openFile({ path: $event })" @preview-plan="previewFile({ path: $event })" />
        </template>
      </EditorColumn>
      <aside v-if="filesPanel" class="hidden md:flex w-[332px] flex-none flex-col overflow-hidden rounded-lg border border-default bg-default" data-files-aside>
        <div class="flex h-9 flex-none items-center gap-2 border-b border-default px-3 text-xs font-semibold text-highlighted">
          <UIcon name="i-lucide-folder-open" class="size-4 text-muted" /> Files
          <UBadge v-if="info.role !== 'control'" label="read only" icon="i-lucide-lock" color="neutral" variant="subtle" size="sm" class="ml-auto" data-files-readonly />
        </div>
        <FilesPane :request="requestFile" :cwd="current.cwd" :activity="activity" class="flex-1 min-h-0" @open="openFile" @open-diff="openDiff" @open-commit-diff="openCommitDiff" />
      </aside>
      <aside v-if="chatPanel && chat.thread.value.capable" class="hidden md:flex w-[332px] flex-none" data-chat-aside>
        <ChatPanel class="w-full overflow-hidden rounded-lg border border-default" :thread="chat.thread.value" :role="info.role" :ended="ended" :offline="chatOffline" :viewers="viewerList" :note="chatNote" @send="chatSend" @send-to-agent="chatSendToAgent" @retry="chatRetry" @answer="chatAnswer" />
      </aside>
    </main>

    <ChatSheet v-model:open="chatSheet" :thread="chat.thread.value" :role="info.role" :ended="ended" :offline="chatOffline" :viewers="viewerList" :note="chatNote" @send="chatSend" @send-to-agent="chatSendToAgent" @retry="chatRetry" @answer="chatAnswer" />
    <ChatSheet
      v-if="run"
      v-model:open="runChatOpen"
      scope="run"
      title="Run chat"
      :description="runChatDescription"
      :thread="runChat.thread.value"
      :role="info.role"
      :offline="runChat.offline.value"
      :viewers="runChat.people.value"
      :note="runChatNote"
      :scope-items="runScope"
      :send-targets="runTargets"
      @send="runChatSend"
      @send-to="runChatSendTo"
      @retry="runChat.retry"
      @answer="runChatAnswer"
    />
  </template>
    </JoinFrame>
  </NuxtLayout>
</template>
