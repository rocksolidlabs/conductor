<script setup lang="ts">
import type { BrakemanRequest, BriefItem, RefResolution } from '~/utils/brakeman'
import { refLabel } from '~/utils/brakeman'
import { COMMENT_MAX, handoffMarkdown, handoffTickets, utf8Bytes } from '~/utils/brakemanCards'
import { ApiError } from '~/composables/useApi'
import { BAD_NAME, type HandPreview, type SentHandoff, handBy, handControl, handedTo, handRefusalWords, sendOffered, sentLine } from '~/utils/brakemanSend'

/**
 * A handoff package (docs/plans/brakeman.md, PR-7), built from Conductor's records and never an agent's conversation: the objective,
 * the constraints, the decisions made, the revision, the open questions and the evidence, as Markdown; Copy; and, for a ticket its
 * refs name, "Comment on the ticket…": the shared comment dialog (BrakemanCommentTicket), Dispatch's comment operation as the admin,
 * under the person's name. A package longer than a comment holds (Dispatch's 16 KiB) is commented as the person shortens it in the
 * dialog: Copy keeps the whole.
 *
 * On the Brakeman page's own brief (`request` and `index` given) of a request to prepare a handoff about a task, it is also sent to
 * that task's next attempt (issue #1012): "Send to <task>'s next attempt…" opens the dialog with the exact text the server will hand
 * (BrakemanSendHandoff), and the box then says truthfully where the package stands: waiting for the next attempt (with Withdraw),
 * handed to attempt N, taken for an attempt that did not start (one press sends it again), withdrawn, or replaced. The preview is read
 * as the box mounts, after the person's own send or withdraw, and again when the page's copy of the package's run changes while it
 * waits or until the attempt it went to has started; never on a timer. Where the server has no such route, nothing is drawn.
 */
/** `quotes`: the turn could read terminal screens or check logs, so the dialog says the package may quote one (as the brief's does). */
const props = defineProps<{ item: Extract<BriefItem, { kind: 'handoff/1' }>; refs: ReadonlyMap<string, RefResolution>; quotes?: boolean; request?: BrakemanRequest; index?: number }>()

const copy = useCopy()

const markdown = computed(() => handoffMarkdown(props.item, (r) => refLabel(r)))
const tickets = computed(() => handoffTickets(props.item))
const over = computed(() => utf8Bytes(markdown.value) > COMMENT_MAX)
const kib = (n: number) => `${(n / 1024).toFixed(1)} KiB`
const commented = ref<Record<string, number>>({})
const commenting = ref(false)
const pick = ref('')
function comment(key: string) {
  pick.value = key
  commenting.value = true
}

// Sending to the task's next attempt.
const hand = useBrakemanSend()
const attention = useAttention()
const identity = useIdentity()
const toast = useToast()
const offered = computed(() => sendOffered(props.request, props.index))
const preview = ref<HandPreview | null>(null)
const readFailed = ref('')
/** The reads in order: a reply to an older read than one already taken is dropped. */
let reads = 0
let taken = 0
async function read() {
  if (!offered.value) return
  const n = ++reads
  try {
    const p = await hand.preview(props.request!.id, props.index!)
    if (n < taken) return
    taken = n
    preview.value = p
    readFailed.value = ''
  } catch (e) {
    if (n < taken) return
    taken = n
    readFailed.value = e instanceof ApiError ? handRefusalWords(e.status === 401 ? 'unauthorized' : e.code) : (e as Error).message
  }
}
onMounted(read)

const control = computed(() => handControl(preview.value))
const record = computed(() => preview.value?.handoff)
const runName = (run: string) => (preview.value?.target?.run === run ? preview.value.target.runName : '') || attention.runNames.value[run] || run
const line = computed(() => (record.value ? sentLine(record.value, runName(record.value.run)) : ''))
const runLink = computed(() => {
  const h = record.value
  if (!h) return ''
  const run = handedTo(h)?.run ?? preview.value?.target?.run ?? h.run
  return `/runs/${encodeURIComponent(run)}?view=tasks&task=${encodeURIComponent(h.task)}`
})

/**
 * The runs whose change may change the record: while it waits, its run and the run it would go to now (a continuation); once handed,
 * the run of the attempt it went to, until the page sees that attempt started (its session) or the server says it did not.
 */
const watched = computed(() => {
  const h = record.value
  if (!h) return []
  if (h.state === 'waiting') return [...new Set([h.run, preview.value?.target?.run ?? h.run])].map((r) => attention.runOf(r))
  const to = handedTo(h)
  if (h.state !== 'handed' || h.notStarted || !to) return []
  const run = attention.runOf(to.run)
  const t = run?.tasks?.find((x) => x.id === h.task)
  return t?.attempt?.n === to.attempt && t.attempt.sessionId ? [] : [run]
})
watch(watched, (now, before) => {
  if (now.length && before?.length && now.some((r, i) => r !== before[i])) void read()
})

const sending = ref(false)
function sent(h: SentHandoff) {
  if (preview.value) preview.value = { ...preview.value, handoff: h }
  void read()
}
const again = ref(false)
/** One press: the same text sent again, after an attempt took it and never started. */
async function sendAgain() {
  const p = preview.value
  if (!p || again.value) return
  const by = handBy(identity.name.value)
  if (by === null) {
    toast.add({ title: 'Not sent', description: BAD_NAME, icon: 'i-lucide-triangle-alert', color: 'error' })
    return
  }
  again.value = true
  try {
    sent(await hand.send(props.request!.id, props.index!, p.sha256, by))
  } catch (e) {
    toast.add({ title: 'Not sent', description: e instanceof ApiError ? handRefusalWords(e.code, { task: p.target?.task, bytes: p.bytes }) : (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    again.value = false
  }
}
const withdrawing = ref(false)
async function withdraw() {
  const h = record.value
  if (!h || withdrawing.value) return
  withdrawing.value = true
  try {
    sent(await hand.withdraw(h.id, handBy(identity.name.value) ?? ''))
  } catch (e) {
    toast.add({ title: 'Not withdrawn', description: e instanceof ApiError ? handRefusalWords(e.code) : (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    withdrawing.value = false
  }
}
</script>

<template>
  <div class="flex flex-col gap-2 rounded-md border border-default p-3" data-brief-handoff>
    <MarkdownPreview :text="markdown" dir="" :relative-links="false" class="text-sm" data-handoff-markdown />
    <p v-if="over && tickets.length" class="text-xs text-muted" data-handoff-bound>
      The package is {{ kib(utf8Bytes(markdown)) }}, more than a ticket's comment holds ({{ kib(COMMENT_MAX) }}). Copy keeps the whole; a comment sends what you shorten it to.
    </p>
    <div class="flex flex-wrap items-center gap-2">
      <UButton label="Copy" icon="i-lucide-copy" size="sm" color="neutral" variant="outline" class="max-sm:min-h-11" data-handoff-copy @click="copy(markdown, 'The handoff is copied')" />
      <UButton
        v-for="key in tickets"
        :key="key"
        :label="commented[key] !== undefined ? `Commented on ${key}` : `Comment on ${key}…`"
        icon="i-lucide-message-square-plus"
        size="sm"
        color="neutral"
        variant="ghost"
        class="max-sm:min-h-11"
        :disabled="commented[key] !== undefined"
        :data-handoff-comment="key"
        @click="comment(key)"
      />
      <UButton
        v-if="control.kind === 'send' && control.again"
        label="Send it again"
        icon="i-lucide-send"
        size="sm"
        color="neutral"
        variant="outline"
        class="max-sm:min-h-11"
        :loading="again"
        data-handoff-send-again
        @click="sendAgain"
      />
      <UButton
        v-else-if="control.kind === 'send' || control.kind === 'disabled'"
        :label="control.label"
        icon="i-lucide-send"
        size="sm"
        color="neutral"
        variant="outline"
        class="max-sm:min-h-11"
        :disabled="control.kind === 'disabled'"
        data-handoff-send
        @click="sending = true"
      />
    </div>
    <p v-if="control.kind === 'disabled'" class="text-xs text-muted" data-handoff-send-why>{{ control.why }}</p>
    <div v-if="record" class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs" :data-handoff-sent="record.state" :data-handoff-id="record.id">
      <span class="min-w-0 break-words" :class="record.notStarted ? 'text-default' : 'text-muted'" data-handoff-sent-line>{{ line }}</span>
      <UButton
        v-if="record.state === 'waiting'"
        label="Withdraw"
        size="xs"
        color="neutral"
        variant="outline"
        class="max-sm:min-h-11"
        :loading="withdrawing"
        data-handoff-withdraw
        @click="withdraw"
      />
      <ULink v-if="record.state === 'waiting' || record.state === 'handed'" :to="runLink" class="text-muted underline max-sm:inline-flex max-sm:min-h-11 max-sm:items-center" data-handoff-run>Open the run at {{ record.task }}</ULink>
    </div>
    <div v-if="readFailed" class="flex flex-wrap items-center gap-2 text-xs" data-handoff-read-failed>
      <span class="text-muted">{{ `Where the package stands could not be read: ${readFailed}` }}</span>
      <UButton label="Read again" size="xs" color="neutral" variant="ghost" class="max-sm:min-h-11" data-handoff-read-again @click="read" />
    </div>
    <BrakemanCommentTicket v-if="tickets.length" v-model:open="commenting" :text="markdown" :keys="tickets" :pick="pick" what="The handoff" :quotes="quotes" @commented="(k, n) => (commented = { ...commented, [k]: n })" />
    <BrakemanSendHandoff v-if="preview?.target && request && index !== undefined" v-model:open="sending" :request="request.id" :index="index" :preview="preview as HandPreview & { target: NonNullable<HandPreview['target']> }" :quotes="quotes" @sent="sent" />
  </div>
</template>
