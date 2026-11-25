<script setup lang="ts">
import type { SessionConnection } from '~/composables/useSessionConnection'
import type { PermissionAnswer, TranscriptEntry, TranscriptPartName, TranscriptRequest } from '~/utils/protocol'
import { ReplyError } from '~/utils/reply'
import { chatBlocks, limitReads, promptLine, promptText, readPart, stepsLine, type ChatTurn } from '~/utils/transcript'
import { MAX_TRANSCRIPT_IN_FLIGHT } from '~/utils/protocol'

/**
 * A session run as a chat (docs/plans/chat-or-terminal.md, design 11), where its terminal is drawn otherwise: quiet by design. What
 * the person wrote and what the agent wrote (its words through renderMarkdown with no HTML, a link only to https); each turn's tool
 * steps folded into one line ("12 steps", "Working." while it runs) that opens to its rows; a file change as one line that opens to
 * its diff; a permission question where it was asked; a notice as one line; the box to write in, with Stop while a turn runs. No
 * count of tokens, timing or id is drawn. It reads the session's one connection (useSessionConnection): its transcript, its reads,
 * its Stop and its messages.
 */
const props = withDefaults(
  defineProps<{
    connection: SessionConnection
    readOnly?: boolean
    /**
     * The page's own word, for a connection whose welcome was the session's terminal's (the session moved to its chat on it, and a
     * `way` message carries neither): `operator` this page holds the workbench token, whose connection answers a permission request;
     * `verbatim` the session's, from its listing (undefined: not known, and no help line is drawn).
     */
    operator?: boolean
    verbatim?: boolean
    /** A file change's link opens the editor's diff tab: only where the page has the editor to open it in (the session page). */
    opensChanges?: boolean
    /**
     * The note shown once, the first time a person presses Chat in this browser (design 11): drawn in the chat under the move's
     * notice, where it covers nothing, until dismissed (`dismissNote`). `site` is the agent's site from its catalog entry.
     */
    note?: { site?: string } | null
  }>(),
  { readOnly: false, operator: false, verbatim: undefined, opensChanges: false, note: null },
)
const emit = defineEmits<{ openDiff: [entry: TranscriptEntry]; dismissNote: [] }>()

const conn = props.connection
const entries = computed(() => {
  void conn.transcriptVersion.value
  return conn.transcript.list()
})
const blocks = computed(() => chatBlocks(entries.value))
const running = computed(() => blocks.value.some((b) => b.kind === 'turn' && b.running))
const controls = computed(() => !props.readOnly && conn.role.value === 'control')
/** The chat's own welcome says both; after a move on this connection the page's word stands in for it. */
const chatWelcome = computed(() => conn.welcome.value?.way === 'protocol')
const canAnswer = computed(() => !props.readOnly && (chatWelcome.value ? !!conn.welcome.value?.permissionAnswer : props.operator))
const verbatim = computed<boolean | undefined>(() => (chatWelcome.value ? !!conn.welcome.value?.verbatim : props.verbatim))
const opened = ref(new Set<string>())
const said = ref('')

function toggleSteps(t: ChatTurn) {
  const next = new Set(opened.value)
  if (next.has(t.key)) next.delete(t.key)
  else next.add(t.key)
  opened.value = next
}

// The chat's reads wait their turn among the connection's MAX_TRANSCRIPT_IN_FLIGHT, never refused for it.
const get = limitReads((req: TranscriptRequest) => conn.transcriptGet(req), MAX_TRANSCRIPT_IN_FLIGHT)
const read = (seq: number, name: TranscriptPartName) => readPart(get, seq, name)
/** Rises with each welcome: a question whose read failed while the connection was away reads again. */
const epoch = ref(0)
watch(() => conn.welcome.value, () => epoch.value++)
/** The block the note is drawn after: the newest notice (the move's), else none, and it leads the chat. */
const noteAfter = computed(() => [...blocks.value].reverse().find((b) => b.kind === 'notice')?.key ?? '')
const siteHost = computed(() => {
  try {
    return props.note?.site?.startsWith('https://') ? new URL(props.note.site).host : ''
  } catch {
    return ''
  }
})

/** The chat's refusals in the plan's words (design 13), the connection's own in plain ones: never the transport's. */
const MESSAGE_FAILED: Record<string, string> = {
  too_many_requests: 'Too many messages are waiting.',
  moving: 'The session is moving; try again in a moment.',
  no_turn: 'The agent is not working on anything.',
  not_protocol: 'Stop is for a chat; use the agent\'s own key in its terminal.',
  read_only: 'This link is view-only.',
  not_connected: 'Not sent: the connection is down.',
  closed: 'Not sent: the connection is down.',
}

/**
 * The chat's requests on their way (a message, Stop, an answer). A refusal that names no request (a relay refuses a control it does not
 * pass with no request id) is drawn in its own words while any is on its way, and a request is never waited on for an id it will not get.
 */
let inFlight = 0
async function going<T>(p: () => Promise<T>): Promise<T> {
  inFlight++
  try {
    return await p()
  } finally {
    inFlight--
  }
}
const stopListening = conn.onControl((msg) => {
  if (msg.t === 'error' && !msg.requestId && inFlight > 0 && msg.code !== 'bad_frame') said.value = msg.message
})
onBeforeUnmount(stopListening)

async function send(text: string) {
  said.value = ''
  try {
    await going(() => conn.submitAcked(text))
  } catch (e) {
    // A message not acknowledged in time is shown by its entry ("Sent, not yet confirmed."); every other refusal in the owner's words.
    if (e instanceof ReplyError && (e.code === 'no_word' || e.code === 'unconfirmed')) return
    said.value = (e instanceof ReplyError && MESSAGE_FAILED[e.code]) || 'The message was not sent.'
  }
}

async function stop() {
  said.value = ''
  try {
    await going(() => conn.interrupt())
  } catch (e) {
    // A Stop the relay refused is already said in the owner's words.
    if (e instanceof ReplyError && e.code === 'no_word' && said.value) return
    said.value = (e instanceof ReplyError && MESSAGE_FAILED[e.code]) || 'The session did not say whether it stopped the turn.'
  }
}
const answer = (a: Omit<PermissionAnswer, 't' | 'reqId'>) => going(() => conn.permissionAnswer(a))

// The newest entry in view while the person is at the bottom: a turn's words follow as they come; one who scrolled up stays.
const scroller = ref<HTMLElement | null>(null)
let atBottom = true
function onScroll() {
  const el = scroller.value
  if (el) atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 48
}
watch(
  () => conn.transcriptVersion.value,
  () => {
    if (!atBottom) return
    void nextTick(() => {
      const el = scroller.value
      if (el) el.scrollTop = el.scrollHeight
    })
  },
  { flush: 'post' },
)

/** The oldest open permission question, brought into view (the reply bar's "Answer it in the chat."). */
function showQuestion() {
  const q = scroller.value?.querySelector<HTMLElement>('[data-permission-question][data-permission-state="open"]')
  q?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  q?.querySelector<HTMLElement>('button:not([disabled])')?.focus({ preventScroll: true })
}
defineExpose({ showQuestion })
</script>

<template>
  <div class="flex h-full min-h-0 flex-col bg-default" data-chat-view :data-chat-entries="entries.length" :data-turn-running="running || undefined">
    <div ref="scroller" class="min-h-0 flex-1 overflow-y-auto overflow-x-hidden px-4 py-4" data-chat-scroll @scroll.passive="onScroll">
      <div class="mx-auto flex max-w-3xl flex-col gap-4">
        <ChatNote v-if="note && !noteAfter" :site="note.site" :host="siteHost" @dismiss="emit('dismissNote')" />
        <template v-for="b in blocks" :key="b.key">
          <template v-if="b.kind === 'notice'">
            <p class="text-center text-xs text-muted" data-chat-notice :data-notice-code="b.entry.code">{{ b.entry.words }}</p>
            <ChatNote v-if="note && b.key === noteAfter" :site="note.site" :host="siteHost" @dismiss="emit('dismissNote')" />
          </template>
          <section v-else class="flex flex-col gap-2.5" data-chat-turn :data-turn="b.turn" :data-turn-outcome="b.outcome || undefined">
            <!-- What was written for the turn, then its steps folded into one line until opened ("Working." while the turn runs), then
                 what the agent wrote, changed and asked, in the order its stream gave them. -->
            <div v-for="p in b.prompts" :key="`e${p.seq}`" class="flex flex-col items-end gap-1" data-chat-prompt :data-prompt-state="p.state">
              <p v-if="promptText(p)" class="max-w-[85%] rounded-lg bg-elevated px-3 py-2 text-sm whitespace-pre-wrap break-words text-highlighted" data-chat-prompt-text>{{ promptText(p) }}</p>
              <span v-if="p.byName && p.by === 'person'" class="text-[11px] text-muted">{{ p.byName }}</span>
              <span v-if="promptLine(p)" class="text-[11px] text-muted" data-chat-prompt-line>{{ promptLine(p) }}</span>
            </div>
            <div v-if="stepsLine(b)" class="flex flex-col" data-chat-steps>
              <button type="button" class="flex w-fit items-center gap-1.5 rounded-md px-2 py-1 text-xs text-muted hover:bg-elevated hover:text-default" :aria-expanded="opened.has(b.key)" :disabled="!b.steps.length" data-chat-steps-line @click="toggleSteps(b)">
                <UIcon v-if="b.steps.length" :name="opened.has(b.key) ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" class="size-3.5" aria-hidden="true" />
                <UIcon v-else name="i-lucide-loader-circle" class="size-3.5 animate-spin" aria-hidden="true" />
                <span>{{ stepsLine(b) }}</span>
              </button>
              <ul v-if="opened.has(b.key)" class="mt-1 flex flex-col" data-chat-step-rows>
                <ToolStepRow v-for="s in b.steps" :key="s.seq" :entry="s" :read="read" />
              </ul>
            </div>
            <template v-for="it in b.items" :key="it.kind === 'text' ? `t${it.seq}` : `e${it.entry.seq}`">
              <!-- The agent's words, each block rendered by its own component, again only when its text changed. -->
              <ChatWords v-if="it.kind === 'text'" :text="it.text" />
              <InlineDiff v-else-if="it.kind === 'file'" :entry="it.entry" :read="read" :can-open="!readOnly && opensChanges" @open-diff="emit('openDiff', $event)" />
              <PermissionQuestion v-else :entry="it.entry" :can-answer="canAnswer" :read="read" :answer="answer" :epoch="epoch" />
            </template>
            <p v-if="b.outcome === 'interrupted'" class="text-xs text-muted" data-chat-turn-stopped>The turn was stopped.</p>
          </section>
        </template>
      </div>
    </div>
    <ChatComposer
      v-if="controls"
      class="mx-auto w-full max-w-3xl"
      placeholder="Write to the agent"
      :agent="{ verbatim, running, canStop: controls && (chatWelcome ? !!conn.welcome.value?.interrupt : true), said }"
      data-chat-box
      @send="(text) => send(text)"
      @stop="stop"
    />
  </div>
</template>
