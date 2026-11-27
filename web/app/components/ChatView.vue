<script setup lang="ts">
import type { SessionConnection } from '~/composables/useSessionConnection'
import type { PermissionAnswer, TranscriptEntry, TranscriptPartName, TranscriptRequest } from '~/utils/protocol'
import { ReplyError, type ReplyTaken } from '~/utils/reply'
import { answerOf, atChatEnd, chatBlocks, limitReads, OWN_ANSWER_LINE, promptLine, promptText, readPart, stepsLine, type ChatTurn } from '~/utils/transcript'
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
    /** The agent's name, for the box's placeholder and Stop's tooltip; `draftKey` the session whose line the app keeps (useBoxDrafts). */
    agentName?: string
    draftKey?: string
    /**
     * The question the session's page shows for this chat, while one waits (the session page alone). One that takes a typed reply
     * (`reply`) is answered from the box (UX3 R5). With choices, or one the box does not answer, the box leaves the cursor where it is,
     * so that 1 to 9 still answer; otherwise it takes the cursor as the chat is drawn, when nothing else holds it.
     */
    asked?: { id?: string; choices: boolean; reply: boolean } | null
    /**
     * A tile's form (UX3 R8a: the Yard's tiles, a run's): the end of the conversation, read-only (no box, no Stop, no answer), each
     * turn's steps one line ("Working." while it runs), always following the end. A tile's question is answered on the tile.
     */
    tile?: boolean
  }>(),
  { readOnly: false, operator: false, verbatim: undefined, opensChanges: false, note: null, agentName: undefined, draftKey: undefined, asked: null, tile: false },
)
/** `reply`: a line typed in the box as the answer to the question the page shows (`asked`), for the page's answer. */
const emit = defineEmits<{ openDiff: [entry: TranscriptEntry]; dismissNote: []; reply: [text: string, question: string | undefined, taken: ReplyTaken] }>()

const conn = props.connection
/** A message that answers a question as the chat draws it: this browser's own answer, from the tab's store of them, or the words for one. */
const typedAnswers = useTypedAnswers()
const answered = (p: TranscriptEntry) => answerOf(p, typedAnswers.of)
const entries = computed(() => {
  void conn.transcriptVersion.value
  return conn.transcript.list()
})
const blocks = computed(() => chatBlocks(entries.value))
const running = computed(() => blocks.value.some((b) => b.kind === 'turn' && b.running))
const controls = computed(() => !props.tile && !props.readOnly && conn.role.value === 'control')
/** The chat's own welcome says both; after a move on this connection the page's word stands in for it. */
const chatWelcome = computed(() => conn.welcome.value?.way === 'protocol')
const canAnswer = computed(() => !props.tile && !props.readOnly && (chatWelcome.value ? !!conn.welcome.value?.permissionAnswer : props.operator))
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

// The newest entry in view while the person is at the bottom: a turn's words follow as they come; one who scrolled up stays. Where the
// person is, is measured as each change arrives, before it is drawn (`flush: 'pre'`): an input opened at the chat's end grows below it
// with no scroll, and a person reading it is not at the bottom, so the next change never carries the page past what they read.
const scroller = ref<HTMLElement | null>(null)
watch(
  () => conn.transcriptVersion.value,
  () => {
    const el = scroller.value
    // A tile has no scroll of its own to leave the end by: it always shows the end.
    if (el && !props.tile && !atChatEnd(el)) return
    void nextTick(() => {
      const box = scroller.value
      if (box) box.scrollTop = box.scrollHeight
    })
  },
  { flush: 'pre' },
)

/** The oldest open permission question, brought into view (the reply bar's "Answer it in the chat."). */
function showQuestion() {
  const q = scroller.value?.querySelector<HTMLElement>('[data-permission-question][data-permission-state="open"]')
  q?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  q?.querySelector<HTMLElement>('button:not([disabled])')?.focus({ preventScroll: true })
}
defineExpose({ showQuestion })

/**
 * The box takes the cursor as the chat is drawn and once a question is answered, when nothing else holds it (UX3 R4), unless choices
 * show (their number keys answer) or the question is one the box does not answer (a permission request's own buttons do).
 */
const composer = useTemplateRef<{ focus: () => void }>('composer')
const keysAnswer = computed(() => !!props.asked && (props.asked.choices || !props.asked.reply))
function takeCursor() {
  if (!controls.value || keysAnswer.value || typeof document === 'undefined') return
  const el = document.activeElement
  if (!el || el === document.body) composer.value?.focus()
}
onMounted(() =>
  void nextTick(() => {
    if (props.tile && scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight
    takeCursor()
  }),
)
watch(
  keysAnswer,
  (now, was) => {
    if (was && !now) void nextTick(takeCursor)
  },
)
</script>

<template>
  <!-- A tile's form is the same conversation, smaller, behind `inert`: nothing in it takes a press, a key or the focus. -->
  <div
    class="flex h-full min-h-0 flex-col bg-default"
    :data-chat-view="tile ? undefined : ''"
    :data-chat-tile="tile ? '' : undefined"
    :data-chat-entries="entries.length"
    :data-turn-running="running || undefined"
    :inert="tile || undefined"
  >
    <div ref="scroller" class="min-h-0 flex-1 overflow-x-hidden" :class="tile ? 'overflow-y-hidden px-2.5 py-2 text-xs' : 'overflow-y-auto px-4 py-4'" data-chat-scroll>
      <div class="mx-auto flex flex-col" :class="tile ? 'gap-2' : 'max-w-3xl gap-4'">
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
              <!-- A message's text; an answer to a question: its text is never kept (#364); its sender's own browser shows what it sent, anyone else the
                   words that say a question was answered. -->
              <p v-if="promptText(p)" class="max-w-[85%] rounded-lg bg-elevated px-3 py-2 text-sm whitespace-pre-wrap break-words text-highlighted" data-chat-prompt-text>{{ promptText(p) }}</p>
              <template v-else-if="answered(p)">
                <p
                  class="max-w-[85%] rounded-lg bg-elevated px-3 py-2 text-sm whitespace-pre-wrap break-words"
                  :class="answered(p)!.own ? 'text-highlighted' : 'text-muted'"
                  data-chat-prompt-answer
                  :data-answer-own="answered(p)!.own || undefined"
                >{{ answered(p)!.text }}</p>
                <span v-if="answered(p)!.own" class="text-[11px] text-muted" data-chat-answer-line>{{ OWN_ANSWER_LINE }}</span>
              </template>
              <span v-if="p.byName && p.by === 'person'" class="text-[11px] text-muted">{{ p.byName }}</span>
              <span v-if="promptLine(p)" class="text-[11px] text-muted" data-chat-prompt-line>{{ promptLine(p) }}</span>
            </div>
            <p v-if="tile && stepsLine(b)" class="flex items-center gap-1.5 text-muted" data-chat-steps data-chat-steps-line>
              <UIcon v-if="!b.steps.length" name="i-lucide-loader-circle" class="size-3 animate-spin" aria-hidden="true" />
              <span>{{ stepsLine(b) }}</span>
            </p>
            <div v-else-if="stepsLine(b)" class="flex flex-col" data-chat-steps>
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
    <!-- The chat's foot, in the conversation's column: the card for the agent's question (the page's `ask`) just above the box, so
         that what it asks and where to answer sit together. The card draws nothing while nothing is asked. -->
    <div v-if="$slots.ask || controls" class="mx-auto w-full max-w-3xl border-t border-default" data-chat-foot>
      <div v-if="$slots.ask" class="px-3 [&_[data-quick-reply]]:mt-2.5" data-chat-ask><slot name="ask" /></div>
      <ChatComposer
        v-if="controls"
        ref="composer"
        :agent="{ verbatim, running, canStop: controls && (chatWelcome ? !!conn.welcome.value?.interrupt : true), said, name: agentName, draftKey, asked }"
        data-chat-box
        @send="(text) => send(text)"
        @stop="stop"
        @answer="(text, question, taken) => emit('reply', text, question, taken)"
      />
    </div>
  </div>
</template>
