<script setup lang="ts">
import { markHidden, markWhole } from '~/utils/hiddenMarks'
import { messageBytes, plainWords, shownInput, type ShownInput } from '~/utils/permissionInput'
import type { PermissionAnswer, TranscriptEntry } from '~/utils/protocol'
import { ReplyError } from '~/utils/reply'
import { MAX_PERMISSION_MESSAGE } from '~/utils/protocol'
import { permissionEnd, type ReadPart } from '~/utils/transcript'

/**
 * A chat's permission question (docs/plans/chat-or-terminal.md, designs 7 and 11): amber while it waits on a person. What the call
 * is in plain words, the tool that runs it, the exact command or path in one line of the code font that opens to the whole input,
 * and, below the whole input, Allow once and Deny. The input is read whole (its `input` part) and drawn as text, never applied:
 * every character a person cannot see is a mark of its own (utils/hiddenMarks.ts), a line feed a line break, each line's end and
 * the spaces left at it marked too, and a line says so when a hidden one is there. The opened input has no scroll of its own: it
 * lies in the chat's. Allow once is offered only once the whole input is drawn (a one-line input at once, a longer one once opened),
 * its end has been in view, and the digest of the bytes drawn is the request's: the answer carries that digest and the call, so it
 * binds to exactly what was shown. Anyone but the operator sees the question and who answers it, never a button. An answered
 * question reads its input only when its line is opened.
 */
const props = withDefaults(
  defineProps<{
    entry: TranscriptEntry
    /** This connection is the operator's own (the welcome's `permissionAnswer`). */
    canAnswer: boolean
    /** Reads a part of an entry whole (utils/transcript.ts readPart over the connection, waiting its turn among the reads). */
    read: (seq: number, name: 'input') => Promise<ReadPart>
    answer: (a: Omit<PermissionAnswer, 't' | 'reqId'>) => Promise<boolean>
    /** Rises with each welcome of the connection: an open question whose read failed reads again. */
    epoch?: number
  }>(),
  { epoch: 0 },
)

const open = computed(() => props.entry.state === 'open')
const input = ref<ShownInput | null>(null)
const readError = ref('')
const opened = ref(false)
const sending = ref(false)
const said = ref('')
const message = ref('')
let reading: Promise<void> | null = null

function load(): Promise<void> {
  if (reading) return reading
  readError.value = ''
  reading = (async () => {
    try {
      const part = await props.read(props.entry.seq, 'input')
      // A part the session kept cut is not the whole input: nothing of it may be allowed.
      const shown = shownInput(props.entry, part.bytes)
      input.value = part.cut || part.total !== props.entry.inputBytes ? { ...shown, exact: false } : shown
    } catch (e) {
      readError.value = e instanceof ReplyError && e.code === 'transcript_gone' ? 'That part of the chat is no longer kept.' : 'Conductor cannot show this call exactly.'
    } finally {
      reading = null
    }
  })()
  return reading
}
// An open question reads its input at once (and again after a failed read, at the connection's next welcome); a closed one only
// when its line is opened.
watch(
  () => [props.entry.seq, props.entry.inputSha256, open.value] as const,
  ([, , isOpen], before) => {
    if (before && (before[0] !== props.entry.seq || before[1] !== props.entry.inputSha256)) input.value = null
    if (isOpen && !input.value) void load()
  },
  { immediate: true },
)
watch(
  () => props.epoch,
  () => {
    if (open.value && !input.value) void load()
  },
)

async function toggle() {
  if (!input.value) await load()
  const shown: ShownInput | null = input.value
  if (shown && !shown.whole) opened.value = !opened.value
}

/** The tool, the line and the rows, marked: the opened rows with each line's end and its trailing spaces marked too. */
const tool = computed(() => markHidden(props.entry.tool ?? ''))
const line = computed(() => markHidden(input.value?.line ?? props.entry.summary ?? ''))
const rows = computed(() => (input.value?.rows ?? []).map((r) => ({ key: markHidden(r.key), value: markWhole(r.text) })))
const cwd = computed(() => (props.entry.cwd ? markWhole(props.entry.cwd) : null))
const hidden = computed(() => tool.value.hidden || line.value.hidden || rows.value.some((r) => r.key.hidden || r.value.hidden) || !!cwd.value?.hidden)
/** The line can open: an input not read yet (a closed question's), or one that is more than its line. */
const opens = computed(() => !input.value || !input.value.whole)

/**
 * The input's end has been in view since it was drawn as it is now: an observer on the mark after its last row (after the line when
 * the line is the whole input), seen again after every open or close.
 */
const inputEnd = ref<HTMLElement | null>(null)
const endSeen = ref(false)
let observer: IntersectionObserver | undefined
function watchEnd() {
  endSeen.value = false
  const el = inputEnd.value
  if (!el || typeof IntersectionObserver === 'undefined') return
  observer?.disconnect()
  observer = new IntersectionObserver((seen) => {
    if (seen.some((s) => s.isIntersecting)) endSeen.value = true
  })
  observer.observe(el)
}
watch([opened, input, inputEnd], () => void nextTick(watchEnd), { flush: 'post' })
onBeforeUnmount(() => observer?.disconnect())

/** The whole input is drawn: a one-line input as it is, a longer one once opened; and its end has been in view. */
const wholeDrawn = computed(() => !!input.value && (input.value.whole || opened.value) && endSeen.value)
const allowable = computed(() => open.value && props.canAnswer && !!props.entry.tool && !!input.value?.exact && wholeDrawn.value && !sending.value)
const end = computed(() => permissionEnd(props.entry))
/** A deny's message is bounded in bytes on the wire (500), whatever its script. */
const tooLong = computed(() => messageBytes(message.value.trim()) > MAX_PERMISSION_MESSAGE)

const FAILED: Record<string, string> = {
  stale_request: 'This request changed or was answered.',
  question_answered: 'This request changed or was answered.',
  not_answerer: 'Only the person who runs this server can answer.',
  moving: 'The session is moving; try again in a moment.',
  invalid: 'The message to the agent is too long.',
  not_connected: 'Not sent: the connection is down. Answer once it is back.',
  closed: 'Not sent: the connection is down. Answer once it is back.',
  no_word: 'The session did not say whether it took the answer. Look at the question before answering again.',
}

async function send(choice: 'allow_once' | 'deny') {
  if (!open.value || !props.canAnswer || sending.value) return
  if (choice === 'allow_once' && (!allowable.value || !input.value)) return
  if (choice === 'deny' && tooLong.value) return
  sending.value = true
  said.value = ''
  const words = message.value.trim()
  try {
    await props.answer({
      question: props.entry.question ?? '',
      call: props.entry.call ?? '',
      // An allow carries the digest of the bytes drawn (equal to the request's, or it is not offered); a deny the request's own.
      sha256: choice === 'allow_once' ? input.value!.digest : (props.entry.inputSha256 ?? ''),
      choice,
      ...(choice === 'deny' && words ? { message: words } : {}),
    })
  } catch (e) {
    said.value = (e instanceof ReplyError && FAILED[e.code]) || 'The answer was not sent.'
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <section
    class="flex flex-col gap-2.5 rounded-lg border px-3.5 py-3"
    :class="open ? 'border-warning bg-warning/10' : 'border-default bg-elevated/40'"
    data-permission-question
    :data-permission-state="entry.state"
    :data-question="entry.question"
    :data-input-end-seen="endSeen || undefined"
  >
    <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
      <span v-if="open" class="size-2 flex-none rounded-full bg-warning" aria-hidden="true" />
      <h3 class="text-sm font-semibold text-highlighted" data-permission-words>{{ plainWords(entry.plain) }}</h3>
      <!-- The tool that runs it, always: the digest binds it, so it is read before Allow once. -->
      <code class="min-w-0 break-all [overflow-wrap:anywhere] font-mono text-xs text-muted" data-permission-tool><MarkedText :pieces="tool.pieces" /></code>
    </div>

    <!-- The one line: the exact command or path, opening to the whole input. Drawn as text: a hidden character is a mark. -->
    <component
      :is="opens ? 'button' : 'div'"
      :type="opens ? 'button' : undefined"
      class="flex min-w-0 items-start gap-1.5 rounded-md bg-default px-2.5 py-1.5 text-left font-mono text-xs text-default ring-1 ring-default"
      :aria-expanded="opens ? opened : undefined"
      data-mono="command"
      data-permission-line
      :data-permission-line-opens="opens || undefined"
      @click="opens && toggle()"
    >
      <UIcon v-if="opens" :name="opened ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" class="mt-0.5 size-3.5 flex-none text-muted" aria-hidden="true" />
      <span class="min-w-0 flex-1" :class="input?.whole ? 'break-all whitespace-break-spaces [overflow-wrap:anywhere]' : 'truncate whitespace-pre'"><MarkedText :pieces="line.pieces" :breaks="false" /></span>
    </component>

    <!-- The whole input, opened, in the chat's own scroll (no scroll of its own): each field's name and its value as text, line feeds
         as breaks, each line's end and the spaces left at it marked, every hidden character marked. -->
    <dl v-if="opened && input" data-mono="command" class="flex flex-col gap-1.5 rounded-md bg-default px-2.5 py-2 font-mono text-xs ring-1 ring-default" data-permission-whole>
      <div v-if="cwd" class="flex flex-col gap-0.5" data-permission-cwd>
        <dt class="text-muted">folder</dt>
        <dd class="break-all whitespace-break-spaces [overflow-wrap:anywhere]"><MarkedText :pieces="cwd.pieces" /></dd>
      </div>
      <div v-for="(r, n) in rows" :key="n" class="flex flex-col gap-0.5" data-permission-row>
        <dt v-if="r.key.pieces.length" class="break-all whitespace-break-spaces [overflow-wrap:anywhere] text-muted"><MarkedText :pieces="r.key.pieces" /></dt>
        <dd class="break-all whitespace-break-spaces [overflow-wrap:anywhere]"><MarkedText :pieces="r.value.pieces" /></dd>
      </div>
    </dl>
    <!-- The input's end: Allow once waits until this has been in view. -->
    <span ref="inputEnd" class="-mt-2.5 block h-px" aria-hidden="true" data-permission-input-end />

    <p v-if="hidden" class="text-xs font-medium text-default" data-permission-hidden>This call holds characters that are not shown as typed.</p>
    <p v-if="readError && (open || opened)" class="text-xs text-muted" data-permission-unread>{{ readError }}</p>
    <p v-else-if="input && !input.exact && open" class="text-xs text-muted" data-permission-inexact>Conductor cannot show this call exactly.</p>

    <template v-if="open">
      <template v-if="canAnswer">
        <UInput v-model="message" size="sm" placeholder="Why, for the agent (optional)" aria-label="What the agent is told with a deny" class="w-full" data-permission-message />
        <p v-if="tooLong" class="text-xs text-muted" data-permission-message-long>The message to the agent is too long: it takes at most 500 bytes.</p>
        <div class="flex flex-wrap items-center gap-2" data-permission-buttons>
          <UButton label="Allow once" size="sm" color="warning" :disabled="!allowable" :loading="sending" data-permission-allow @click="send('allow_once')" />
          <UButton label="Deny" size="sm" color="neutral" variant="outline" :disabled="sending || tooLong" data-permission-deny @click="send('deny')" />
        </div>
      </template>
      <p v-else class="text-xs text-muted" data-permission-operator-only>Only the person who runs this server can answer.</p>
    </template>
    <p v-else class="text-xs text-muted" data-permission-end>{{ end }}</p>
    <p v-if="said" class="text-xs text-error" role="status" data-permission-said>{{ said }}</p>
  </section>
</template>
