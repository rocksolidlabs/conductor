<script setup lang="ts">
import { holdLine, type ReplyTaken } from '~/utils/reply'
import { briefLine, inboxKindWords, itemTimeWords, ticketOf, type DeliveryStep, type InboxAction, type InboxItem } from '~/utils/inbox'
import type { LaneEvidence } from '~/utils/transport/decide'

/**
 * One item of the decision page, whole (docs/plans/decision-inbox.md, PR-2): its brief (Brakeman's card, named as Brakeman's, or
 * Conductor's own line), the question whole, the choices from the live record each with exactly what it authorizes, the
 * recommendation and its uncertainty from Brakeman's card alone ("No recommendation" otherwise: Conductor recommends nothing), what
 * is blocked, who answers and whom a router named, the evidence folded, the actions, and after an action its four steps. The card
 * acts through its parent (`act`): the workbench's page through each operation's route, a decision link's page through its lane
 * (MB0's PR-7). Accept anyway, Cancel and Withdraw are asked first, with the run page's question. Its routes come from `routeOf`.
 *
 * A DR1 record (`decision`, docs/plans/decision-inbox.md PR-4) is drawn by DR1's own card in the `record` slot (DecisionCard: the
 * question, the alternatives, the recommendation, the evidence it cites, the four states, the history; DecisionRespond on the
 * workbench: the controls by its type), the inbox's brief, what waits and its own evidence around it, and nothing of the record twice:
 * no question, choices, answerer or steps of the inbox's, and none of the evidence the card draws (`drawn`).
 *
 * One card is one item: the page keys it by the item's ref, so a typed line, a comment's draft or a question asked first never
 * carries over to another item, and every action it emits names the item it was drawn for (the page acts on that item or on none).
 */
const props = defineProps<{
  item: InboxItem
  /** The page's clock: the heading's time follows it. */
  now: number
  routeOf: (ref: string) => string | null
  /** This browser's latest action's steps on the item, when it acted: shown before the item's own. */
  delivery?: readonly DeliveryStep[]
  /** The label of this item's action under way, if any (another item's never shows here). */
  busy?: string
  /** Why this item's last action was refused (or not sent), in words; for a question, where it can still be answered. */
  refusal?: string
  /** The words of an action whose outcome nobody confirmed (sent, and no word will come), with a way to read again; none otherwise. */
  unknown?: string
  /** The subject Ask Brakeman asks Brakeman to explain, at once (the page sends it: `ask`), when the server has Brakeman. */
  askBrakeman?: string
  /** Whether that ask is on its way. */
  askingBrakeman?: boolean
  /** The question Accept anyway and Cancel ask, the run page's (useTaskCommands.question). */
  confirmQuestion?: (a: InboxAction) => { title: string; detail: string }
  /** A decision link's page (the join page's Needs you tab): an evidence entry read through the link's lane. */
  readEvidence?: (evidence: string) => Promise<LaneEvidence>
  /** A decision link's page: a session's terminal, one tab away, in place of the workbench's route. */
  openTerminal?: (ref: string) => void
  /** Comment on the ticket is not offered: a decision link has no comment operation (the workbench's page offers it). */
  noComments?: boolean
  /** A DR1 record's evidence refs its card draws (its request's): left out of the inbox's evidence, never drawn twice. */
  drawn?: readonly string[]
  /**
   * A DR1 record's card is drawn in the `record` slot: its question heads the item. Until it is (the record still read, or its read
   * failed) the item's own title, the question as the server words it, heads the item, so the question is never shown nowhere.
   */
  recordShown?: boolean
}>()
const emit = defineEmits<{ act: [ref: string, action: InboxAction, text?: string, taken?: ReplyTaken]; comment: [ref: string, ticket: string, text: string, taken: (sent: boolean) => void]; reread: []; ask: [subject: string] }>()

const brakeman = computed(() => props.item.briefBy === 'brakeman')
/** The heading's time: waiting while the item is open, when it was answered once it is not (itemTimeWords). */
const timeWords = computed(() => itemTimeWords(props.item, props.now))
/** A DR1 record: its record, alternatives, controls and four states are DR1's card's (the `record` slot). */
const durable = computed(() => props.item.kind === 'decision')
const evidence = computed(() => (props.drawn?.length ? (props.item.evidence ?? []).filter((e) => !props.drawn!.includes(e.ref)) : (props.item.evidence ?? [])))
const steps = computed(() => (props.delivery?.length ? props.delivery : props.item.delivery) ?? [])
/** The brief under the title, never the title or the item's `now` again (briefLine, D12). */
const brief = computed(() => briefLine(props.item))
/**
 * A question's fine print (each choice's and the line's scope) folds under Details, as the evidence folds; a plan's and a task's
 * stay in view: they say what an approval or a retry authorizes.
 */
const folds = computed(() => props.item.kind === 'question')
const scopesOpen = ref(false)
const scopesShown = computed(() => !folds.value || scopesOpen.value)
const answers = computed(() => props.item.actions.filter((a) => a.name === 'answer' && !a.text))
const line = computed(() => props.item.actions.find((a) => a.name === 'answer' && a.text))
const commands = computed(() => props.item.actions.filter((a) => a.name !== 'answer'))
/** A question whose turn is held for the terminal: no answer at all, the terminal first in its evidence. */
const held = computed(() => props.item.kind === 'question' && !props.item.actions.length && !props.item.delivery?.length)
const terminal = computed(() => props.item.evidence?.find((e) => e.ref.startsWith('session:')))
const ticket = computed(() => (props.noComments ? '' : ticketOf(props.item)))
/** The terminal of the session the item is about, by this page's route or a link's tab; null when neither has it. */
const terminalTo = computed(() => (terminal.value && props.routeOf(terminal.value.ref)) || null)
const terminalOpens = computed(() => !!terminal.value && (!!terminalTo.value || !!props.openTerminal))
/**
 * The session the item is about runs as a chat (the listing this page holds says so): it has no terminal, and the button that opens
 * it says it opens the chat. Unknown here (a link's page holds no listing): the terminal's words, as before.
 */
const attentionStore = useAttentionStore()
const inChat = computed(() => {
  const id = terminal.value?.ref.slice('session:'.length)
  return !!id && attentionStore.value.sessions.get(id)?.way === 'protocol'
})
const openWords = computed(() => (inChat.value ? 'Open the chat' : 'Open the terminal'))
function openTheTerminal() {
  if (terminal.value && props.openTerminal) props.openTerminal(terminal.value.ref)
}

const text = ref('')
const asking = ref<InboxAction | null>(null)
const askOpen = computed({
  get: () => !!asking.value,
  set: (v: boolean) => {
    if (!v) asking.value = null
  },
})
const question = computed(() => {
  const a = asking.value
  if (!a) return { title: '', detail: '' }
  if (props.confirmQuestion) return props.confirmQuestion(a)
  return { title: `${a.label}?`, detail: a.scope }
})

function press(a: InboxAction) {
  if (a.confirm) asking.value = a
  else emit('act', props.item.ref, a)
}
function confirm() {
  const a = asking.value
  asking.value = null
  if (a) emit('act', props.item.ref, a)
}
/** Sends the typed line; it stays in the field until the page says it was sent (`taken`), as typed when it was not (#654). */
function sendLine() {
  const a = line.value
  if (!a || !text.value.trim()) return
  emit('act', props.item.ref, a, text.value, holdLine(text))
}

const commenting = ref(false)
const comment = ref('')
/** Sends the draft unless an action on the item is under way; the draft goes only once the parent says it was sent (`taken`). */
function sendComment() {
  if (props.busy || !comment.value.trim()) return
  const body = comment.value
  emit('comment', props.item.ref, ticket.value, body, (sent) => {
    if (!sent || comment.value !== body) return
    comment.value = ''
    commenting.value = false
  })
}
</script>

<template>
  <article class="flex flex-col gap-4" :data-inbox-item="item.ref" :data-inbox-kind="item.kind">
    <header class="flex flex-col gap-1">
      <!-- A run's decision: its card's question heads it once the card is drawn, so no title says it again. -->
      <h2 v-if="!durable || !recordShown" class="text-base font-semibold text-highlighted text-pretty break-words" data-inbox-title>{{ item.title }}</h2>
      <p class="text-xs text-muted">
        <span>{{ inboxKindWords(item.kind) }}</span>
        <span v-if="timeWords" data-inbox-time> · {{ timeWords }}</span>
      </p>
    </header>

    <!-- The brief: Brakeman's card about this item, named as Brakeman's, or Conductor's own line from the record. -->
    <section v-if="brakeman" class="flex flex-col gap-2 rounded-md border border-default p-3" data-inbox-brief="brakeman">
      <p class="flex flex-wrap items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted">
        Brakeman's brief
        <ULink v-if="item.card && routeOf(item.card)" :to="routeOf(item.card)!" class="font-normal normal-case tracking-normal text-primary" data-inbox-card>the brief</ULink>
      </p>
      <BrakemanDecisionText :happened="item.brief" :why-human="item.whyHuman" :answerer="item.answerer" :authorizes="item.authorizes" :alternatives="item.considered ?? []" :recommended="item.recommended" :uncertainty="item.uncertainty" />
      <p v-if="item.cardBlocked?.length" class="flex flex-wrap items-center gap-1.5 text-xs text-muted" data-inbox-card-blocked>
        <span>Brakeman says these wait on it:</span>
        <template v-for="r in item.cardBlocked" :key="r">
          <ULink v-if="routeOf(r)" :to="routeOf(r)!" class="text-primary break-all">{{ r }}</ULink>
          <span v-else class="break-all">{{ r }}</span>
        </template>
      </p>
      <p v-if="item.cardNote" class="text-xs text-warning" data-inbox-card-note>{{ item.cardNote }}</p>
    </section>
    <section v-else-if="brief || !durable" class="flex flex-col gap-1" data-inbox-brief="conductor">
      <p v-if="brief" class="text-sm text-pretty" data-inbox-brief-text>{{ brief }}</p>
      <p v-if="!durable" class="text-xs text-muted" data-inbox-recommendation>No recommendation</p>
    </section>

    <!-- A DR1 record, whole: DR1's card (and its controls, on the workbench), drawn by the page that reads the record. -->
    <section v-if="durable" class="flex flex-col gap-3" data-inbox-record>
      <slot name="record" />
    </section>

    <section v-if="item.question && !durable" class="flex flex-col gap-1">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">The question</h3>
      <p class="whitespace-pre-wrap break-words text-sm font-medium text-highlighted" data-inbox-question>{{ item.question }}</p>
    </section>

    <dl class="grid gap-x-3 gap-y-1 text-sm sm:grid-cols-[max-content_1fr]">
      <dt v-if="!durable" class="text-muted">Who answers</dt>
      <dd v-if="!durable" data-inbox-answerer>
        {{ item.answerer }}
        <span v-if="item.routedTo?.length" class="text-muted" data-inbox-routed>· routed to {{ item.routedTo.map((d) => d.name).join(', ') }}<template v-if="item.routedRule"> ({{ item.routedRule }})</template></span>
      </dd>
      <dt class="text-muted">What waits</dt>
      <dd class="flex flex-col gap-1" data-inbox-blocking>
        <span>{{ item.blocking.words }}</span>
        <span v-if="item.blocking.refs.length || item.blocking.more" class="flex flex-wrap gap-1.5">
          <template v-for="r in item.blocking.refs" :key="r">
            <ULink v-if="routeOf(r)" :to="routeOf(r)!" class="text-xs text-primary break-all" data-inbox-blocked>{{ r }}</ULink>
            <span v-else class="text-xs text-muted break-all" data-inbox-blocked>{{ r }}</span>
          </template>
          <span v-if="item.blocking.more" class="text-xs text-muted">and {{ item.blocking.more }} more</span>
        </span>
      </dd>
    </dl>

    <!-- The choices, from the live record: each with exactly what it authorizes, in Conductor's words. -->
    <section v-if="!durable" class="flex flex-col gap-2" data-inbox-actions>
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">{{ item.actions.length ? 'Your choices' : 'Nothing to choose here' }}</h3>
      <div v-for="a in answers" :key="`${a.name}:${a.input}`" class="flex flex-col gap-1">
        <UButton :label="a.label" color="primary" variant="soft" size="lg" block class="min-h-11 justify-start text-left" :loading="busy === a.label" :disabled="!!busy" :data-inbox-action="a.name" :data-action-label="a.label" @click="press(a)" />
        <p v-if="scopesShown" class="text-xs text-muted text-pretty" data-action-scope>{{ a.scope }}</p>
      </div>
      <form v-if="line" class="flex flex-col gap-1" data-inbox-line @submit.prevent="sendLine">
        <div class="flex gap-2">
          <UInput v-model="text" :placeholder="line.label" size="lg" class="min-w-0 flex-1" :disabled="!!busy" data-inbox-line-input />
          <UButton type="submit" label="Send" icon="i-lucide-send" size="lg" class="min-h-11" :loading="busy === line.label" :disabled="!!busy || !text.trim()" data-inbox-line-send />
        </div>
        <p v-if="scopesShown" class="text-xs text-muted text-pretty" data-action-scope>{{ line.scope }}</p>
      </form>
      <UButton
        v-if="folds && (answers.length || line)"
        label="Details"
        :icon="scopesOpen ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
        color="neutral"
        variant="ghost"
        size="sm"
        class="min-h-11 self-start"
        :aria-expanded="scopesOpen"
        data-inbox-scopes-toggle
        @click="scopesOpen = !scopesOpen"
      />
      <div v-for="a in commands" :key="a.name + a.label" class="flex flex-col gap-1">
        <UButton
          :label="a.confirm ? `${a.label}…` : a.label"
          :color="a.name === 'cancel' || a.name === 'withdraw' ? 'error' : a.name === 'accept' ? 'warning' : a.name === 'approve' || a.name === 'retry' ? 'primary' : 'neutral'"
          :variant="a.name === 'approve' || a.name === 'retry' ? 'solid' : 'outline'"
          size="lg"
          block
          class="min-h-11 justify-start text-left"
          :loading="busy === a.label"
          :disabled="!!busy"
          :data-inbox-action="a.name"
          :data-action-label="a.label"
          @click="press(a)"
        />
        <p class="text-xs text-muted text-pretty" data-action-scope>{{ a.scope }}</p>
      </div>
      <!-- A turn held for the terminal: the session refuses every reply naming it, so none is offered; its terminal goes on. -->
      <div v-if="held" class="flex flex-col gap-2" data-inbox-held>
        <p class="text-sm text-pretty">{{ item.blocking.words }}</p>
        <UButton v-if="terminalTo" :to="terminalTo" :label="openWords" icon="i-lucide-square-terminal" size="lg" class="min-h-11 self-start" data-inbox-terminal />
        <UButton v-else-if="terminalOpens" :label="openWords" icon="i-lucide-square-terminal" size="lg" class="min-h-11 self-start" data-inbox-terminal @click="openTheTerminal" />
      </div>
      <UAlert
        v-if="refusal"
        color="warning"
        variant="subtle"
        icon="i-lucide-triangle-alert"
        :title="refusal"
        :actions="item.kind === 'question' && terminalOpens ? [{ label: openWords, icon: inChat ? 'i-lucide-messages-square' : 'i-lucide-square-terminal', color: 'neutral', variant: 'outline', class: 'min-h-11', ...(terminalTo ? { to: terminalTo } : { onClick: openTheTerminal }) }] : undefined"
        data-inbox-refusal
      />
      <UAlert
        v-if="unknown"
        color="neutral"
        variant="subtle"
        icon="i-lucide-circle-help"
        title="Not confirmed"
        :description="unknown"
        :actions="[{ label: 'Read again', color: 'neutral', variant: 'outline', class: 'min-h-11', onClick: () => emit('reread') }]"
        data-inbox-unknown
      />
      <div class="flex flex-wrap gap-2">
        <UButton v-if="askBrakeman" label="Ask Brakeman" icon="i-lucide-notebook-pen" color="neutral" variant="outline" size="md" class="min-h-11" :loading="askingBrakeman" :disabled="askingBrakeman" data-inbox-ask-brakeman @click="emit('ask', askBrakeman)" />
        <UButton v-if="ticket" :label="`Comment on ${ticket}`" icon="i-lucide-message-square" color="neutral" variant="outline" size="md" class="min-h-11" data-inbox-comment-open @click="commenting = !commenting" />
      </div>
      <form v-if="commenting" class="flex flex-col gap-2" data-inbox-comment @submit.prevent="sendComment">
        <UTextarea v-model="comment" :rows="3" autoresize placeholder="A comment on the ticket" data-inbox-comment-input />
        <UButton type="submit" :label="`Comment on ${ticket}`" icon="i-lucide-send" size="md" class="min-h-11 self-start" :loading="busy === 'Comment'" :disabled="!!busy || !comment.trim()" data-inbox-comment-send />
      </form>
    </section>

    <section v-if="(steps.length && !durable) || item.now" class="flex flex-col gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">{{ delivery?.length && !durable ? 'What became of your action' : 'Where it is' }}</h3>
      <InboxDelivery v-if="steps.length && !durable" :steps="steps" :now="item.now" :route-of="routeOf" />
      <UAlert v-else color="neutral" variant="subtle" icon="i-lucide-info" :title="`Now: ${item.now}`" data-inbox-now />
    </section>

    <InboxEvidence v-if="evidence.length" :evidence="evidence" :route-of="routeOf" :read="readEvidence" :open-terminal="openTerminal" />

    <UModal v-model:open="askOpen" :title="question.title" :description="question.detail">
      <template #footer>
        <div class="flex w-full justify-end gap-2">
          <UButton label="Keep it" color="neutral" variant="ghost" class="min-h-11" @click="asking = null" />
          <UButton :label="asking?.label ?? ''" :color="asking?.name === 'accept' ? 'warning' : 'error'" class="min-h-11" data-inbox-confirm @click="confirm" />
        </div>
      </template>
    </UModal>
  </article>
</template>
