<script setup lang="ts">
import { markHidden, type MarkedPiece } from '~/utils/hiddenMarks'
import { plainWords, shownInput, type ShownInput } from '~/utils/permissionInput'
import type { PermissionAnswer, TranscriptEntry } from '~/utils/protocol'
import { ReplyError } from '~/utils/reply'
import { MAX_PERMISSION_MESSAGE } from '~/utils/protocol'
import { permissionEnd, type ReadPart } from '~/utils/transcript'

/**
 * A chat's permission question (docs/plans/chat-or-terminal.md, designs 7 and 11): amber while it waits on a person. What the call
 * is in plain words, the exact command or path in one line of the code font that opens to the whole input, and Allow once and
 * Deny. The input is read whole (its `input` part) and drawn as text, never applied: every character a person cannot see is a mark
 * of its own (utils/hiddenMarks.ts), a line feed a line break, and a line says so when any is there. Allow once is offered only once
 * the whole input is drawn (a one-line input at once, a longer one once opened), and only when the digest of the bytes drawn is the
 * request's: the answer carries that digest and the call, so it binds to exactly what was shown. Anyone but the operator sees the
 * question and who answers it, never a button.
 */
const props = defineProps<{
  entry: TranscriptEntry
  /** This connection is the operator's own (the welcome's `permissionAnswer`). */
  canAnswer: boolean
  /** Reads a part of an entry whole (utils/transcript.ts readPart over the connection). */
  read: (seq: number, name: 'input') => Promise<ReadPart>
  answer: (a: Omit<PermissionAnswer, 't' | 'reqId'>) => Promise<boolean>
}>()

const open = computed(() => props.entry.state === 'open')
const input = ref<ShownInput | null>(null)
const readError = ref('')
const opened = ref(false)
const sending = ref(false)
const said = ref('')
const message = ref('')

async function load() {
  readError.value = ''
  try {
    const part = await props.read(props.entry.seq, 'input')
    // A part the session kept cut is not the whole input: nothing of it may be allowed.
    const shown = shownInput(props.entry, part.bytes)
    input.value = part.cut || part.total !== props.entry.inputBytes ? { ...shown, exact: false } : shown
  } catch (e) {
    readError.value = e instanceof ReplyError && e.code === 'transcript_gone' ? 'That part of the chat is no longer kept.' : 'Conductor cannot show this call exactly.'
  }
}
watch(() => [props.entry.seq, props.entry.inputSha256], load, { immediate: true })

/** The line and the rows, marked. */
const line = computed(() => markHidden(input.value?.line ?? props.entry.summary ?? ''))
const rows = computed(() => (input.value?.rows ?? []).map((r) => ({ key: markHidden(r.key), value: markHidden(r.text) })))
const cwd = computed(() => (props.entry.cwd ? markHidden(props.entry.cwd) : null))
const hidden = computed(() => line.value.hidden || rows.value.some((r) => r.key.hidden || r.value.hidden) || !!cwd.value?.hidden)
/** The whole input is drawn: a one-line input as it is, a longer one once opened. */
const wholeDrawn = computed(() => !!input.value && (input.value.whole || opened.value))
const allowable = computed(() => open.value && props.canAnswer && !!input.value?.exact && wholeDrawn.value && !sending.value)
const end = computed(() => permissionEnd(props.entry))

const FAILED: Record<string, string> = {
  stale_request: 'This request changed or was answered.',
  question_answered: 'This request changed or was answered.',
  not_answerer: 'Only the person who runs this server can answer.',
  moving: 'The session is moving; try again in a moment.',
}

async function send(choice: 'allow_once' | 'deny') {
  if (!open.value || !props.canAnswer || sending.value) return
  if (choice === 'allow_once' && (!allowable.value || !input.value)) return
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
    said.value = (e instanceof ReplyError && FAILED[e.code]) || (e as Error).message
  } finally {
    sending.value = false
  }
}

function pieceKey(p: MarkedPiece, i: number) {
  return `${i}:${'mark' in p ? p.mark : 'br' in p ? 'br' : p.text.length}`
}
</script>

<template>
  <section
    class="flex flex-col gap-2.5 rounded-lg border px-3.5 py-3"
    :class="open ? 'border-warning bg-warning/10' : 'border-default bg-elevated/40'"
    data-permission-question
    :data-permission-state="entry.state"
    :data-question="entry.question"
  >
    <div class="flex items-center gap-2">
      <span v-if="open" class="size-2 flex-none rounded-full bg-warning" aria-hidden="true" />
      <h3 class="text-sm font-semibold text-highlighted" data-permission-words>{{ plainWords(entry.plain) }}</h3>
    </div>

    <!-- The one line: the exact command or path, opening to the whole input. Drawn as text: a hidden character is a mark. -->
    <component
      :is="input && !input.whole ? 'button' : 'div'"
      :type="input && !input.whole ? 'button' : undefined"
      class="flex min-w-0 items-start gap-1.5 rounded-md bg-default px-2.5 py-1.5 text-left font-mono text-xs text-default ring-1 ring-default"
      :aria-expanded="input && !input.whole ? opened : undefined"
      data-mono="command"
      data-permission-line
      @click="input && !input.whole && (opened = !opened)"
    >
      <UIcon v-if="input && !input.whole" :name="opened ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" class="mt-0.5 size-3.5 flex-none text-muted" aria-hidden="true" />
      <span class="min-w-0 flex-1" :class="input?.whole ? 'break-all whitespace-break-spaces' : 'truncate whitespace-pre'"
        ><template v-for="(p, i) in line.pieces" :key="pieceKey(p, i)"
          ><span v-if="'mark' in p" class="rounded-sm bg-warning/30 px-0.5 text-highlighted" data-hidden-mark>{{ p.mark }}</span
          ><span v-else-if="'br' in p" class="text-muted" data-line-break>⏎ </span
          ><template v-else>{{ p.text }}</template></template
        ></span>
    </component>

    <!-- The whole input, opened: each field's name and its value as text, line feeds as breaks, every hidden character marked. -->
    <dl v-if="opened && input" data-mono="command" class="flex max-h-80 flex-col gap-1.5 overflow-auto rounded-md bg-default px-2.5 py-2 font-mono text-xs ring-1 ring-default" data-permission-whole>
      <div v-if="cwd" class="flex flex-col gap-0.5" data-permission-cwd>
        <dt class="text-muted">folder</dt>
        <dd class="break-all whitespace-break-spaces"><template v-for="(p, i) in cwd.pieces" :key="pieceKey(p, i)"><span v-if="'mark' in p" class="rounded-sm bg-warning/30 px-0.5" data-hidden-mark>{{ p.mark }}</span><br v-else-if="'br' in p" /><template v-else>{{ p.text }}</template></template></dd>
      </div>
      <div v-for="(r, n) in rows" :key="n" class="flex flex-col gap-0.5" data-permission-row>
        <dt v-if="r.key.pieces.length" class="text-muted"><template v-for="(p, i) in r.key.pieces" :key="pieceKey(p, i)"><span v-if="'mark' in p" class="rounded-sm bg-warning/30 px-0.5" data-hidden-mark>{{ p.mark }}</span><br v-else-if="'br' in p" /><template v-else>{{ p.text }}</template></template></dt>
        <dd class="break-all whitespace-break-spaces"><template v-for="(p, i) in r.value.pieces" :key="pieceKey(p, i)"><span v-if="'mark' in p" class="rounded-sm bg-warning/30 px-0.5 text-highlighted" data-hidden-mark>{{ p.mark }}</span><br v-else-if="'br' in p" /><template v-else>{{ p.text }}</template></template></dd>
      </div>
    </dl>

    <p v-if="hidden" class="text-xs font-medium text-default" data-permission-hidden>This call holds characters that are not shown as typed.</p>
    <p v-if="readError" class="text-xs text-muted" data-permission-unread>{{ readError }}</p>
    <p v-else-if="input && !input.exact && open" class="text-xs text-muted" data-permission-inexact>Conductor cannot show this call exactly.</p>

    <template v-if="open">
      <template v-if="canAnswer">
        <UInput v-model="message" size="sm" :maxlength="MAX_PERMISSION_MESSAGE" placeholder="Why, for the agent (optional)" aria-label="What the agent is told with a deny" class="w-full" data-permission-message />
        <div class="flex flex-wrap items-center gap-2">
          <UButton label="Allow once" size="sm" color="warning" :disabled="!allowable" :loading="sending" data-permission-allow @click="send('allow_once')" />
          <UButton label="Deny" size="sm" color="neutral" variant="outline" :disabled="sending" data-permission-deny @click="send('deny')" />
        </div>
      </template>
      <p v-else class="text-xs text-muted" data-permission-operator-only>Only the person who runs this server can answer.</p>
    </template>
    <p v-else class="text-xs text-muted" data-permission-end>{{ end }}</p>
    <p v-if="said" class="text-xs text-error" role="status" data-permission-said>{{ said }}</p>
  </section>
</template>
