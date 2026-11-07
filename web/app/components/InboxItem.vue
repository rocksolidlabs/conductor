<script setup lang="ts">
import { relativeTime } from '~/utils/sessions'
import { ticketOf, type DeliveryStep, type InboxAction, type InboxItem } from '~/utils/inbox'

/**
 * One item of the decision page, whole (docs/plans/decision-inbox.md, PR-2): its brief (Brakeman's card, named as Brakeman's, or
 * Conductor's own line), the question whole, the choices from the live record each with exactly what it authorizes, the
 * recommendation and its uncertainty from Brakeman's card alone ("No recommendation" otherwise: Conductor recommends nothing), what
 * is blocked, who answers and whom a router named, the evidence folded, the actions, and after an action its four steps. The card
 * acts through its parent (`act`): the workbench's page through each operation's route, a decision link's page through its lane
 * (MB0's PR-7). Accept anyway, Cancel and Withdraw are asked first, with the run page's question. Its routes come from `routeOf`.
 *
 * One card is one item: the page keys it by the item's ref, so a typed line, a comment's draft or a question asked first never
 * carries over to another item, and every action it emits names the item it was drawn for (the page acts on that item or on none).
 */
const props = defineProps<{
  item: InboxItem
  routeOf: (ref: string) => string | null
  /** This browser's latest action's steps on the item, when it acted: shown before the item's own. */
  delivery?: readonly DeliveryStep[]
  /** The label of this item's action under way, if any (another item's never shows here). */
  busy?: string
  /** Why this item's last action was refused (or not sent), in words; for a question, where it can still be answered. */
  refusal?: string
  /** The words of an action whose outcome nobody confirmed (sent, and no word will come), with a way to read again; none otherwise. */
  unknown?: string
  /** Where Ask Brakeman goes (the Brakeman page's composer about the item's subject), when the server has Brakeman. */
  askBrakeman?: string
  /** The question Accept anyway and Cancel ask, the run page's (useTaskCommands.question). */
  confirmQuestion?: (a: InboxAction) => { title: string; detail: string }
}>()
const emit = defineEmits<{ act: [ref: string, action: InboxAction, text?: string]; comment: [ref: string, ticket: string, text: string, taken: (sent: boolean) => void]; reread: [] }>()

const brakeman = computed(() => props.item.briefBy === 'brakeman')
const steps = computed(() => (props.delivery?.length ? props.delivery : props.item.delivery) ?? [])
const answers = computed(() => props.item.actions.filter((a) => a.name === 'answer' && !a.text))
const line = computed(() => props.item.actions.find((a) => a.name === 'answer' && a.text))
const commands = computed(() => props.item.actions.filter((a) => a.name !== 'answer'))
/** A question whose turn is held for the terminal: no answer at all, the terminal first in its evidence. */
const held = computed(() => props.item.kind === 'question' && !props.item.actions.length && !props.item.delivery?.length)
const terminal = computed(() => props.item.evidence?.find((e) => e.ref.startsWith('session:')))
const ticket = computed(() => ticketOf(props.item))

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
function sendLine() {
  const a = line.value
  if (!a || !text.value.trim()) return
  emit('act', props.item.ref, a, text.value)
  text.value = ''
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
      <h2 class="text-base font-semibold text-highlighted text-pretty" data-inbox-title>{{ item.title }}</h2>
      <p class="text-xs text-muted">
        <span>{{ item.kind === 'question' ? 'A question' : item.kind === 'task' ? 'A failed task' : item.kind === 'plan' ? 'A plan revision' : item.kind }}</span>
        <span v-if="item.since"> · waiting {{ relativeTime(item.since) }}</span>
      </p>
    </header>

    <!-- The brief: Brakeman's card about this item, named as Brakeman's, or Conductor's own line from the record. -->
    <section v-if="brakeman" class="flex flex-col gap-2 rounded-md border border-default p-3" data-inbox-brief="brakeman">
      <p class="flex flex-wrap items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted">
        Brakeman's brief
        <ULink v-if="item.card && routeOf(item.card)" :to="routeOf(item.card)!" class="font-normal normal-case tracking-normal text-primary" data-inbox-card>the brief</ULink>
      </p>
      <BrakemanDecisionText :happened="item.brief" :why-human="item.whyHuman" :answerer="item.answerer" :authorizes="item.authorizes" :alternatives="item.considered ?? []" :recommended="item.recommended" :uncertainty="item.uncertainty" />
      <p v-if="item.cardNote" class="text-xs text-warning" data-inbox-card-note>{{ item.cardNote }}</p>
    </section>
    <section v-else class="flex flex-col gap-1" data-inbox-brief="conductor">
      <p class="text-sm text-pretty" data-inbox-brief-text>{{ item.brief }}</p>
      <p class="text-xs text-muted" data-inbox-recommendation>No recommendation</p>
    </section>

    <section v-if="item.question" class="flex flex-col gap-1">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">The question</h3>
      <p class="whitespace-pre-wrap break-words text-sm font-medium text-highlighted" data-inbox-question>{{ item.question }}</p>
    </section>

    <dl class="grid gap-x-3 gap-y-1 text-sm sm:grid-cols-[max-content_1fr]">
      <dt class="text-muted">Who answers</dt>
      <dd data-inbox-answerer>
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
    <section class="flex flex-col gap-2" data-inbox-actions>
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">{{ item.actions.length ? 'Your choices' : 'Nothing to choose here' }}</h3>
      <div v-for="a in answers" :key="`${a.name}:${a.input}`" class="flex flex-col gap-1">
        <UButton :label="a.label" color="primary" variant="soft" size="lg" block class="min-h-11 justify-start text-left" :loading="busy === a.label" :disabled="!!busy" :data-inbox-action="a.name" :data-action-label="a.label" @click="press(a)" />
        <p class="text-xs text-muted text-pretty" data-action-scope>{{ a.scope }}</p>
      </div>
      <form v-if="line" class="flex flex-col gap-1" data-inbox-line @submit.prevent="sendLine">
        <div class="flex gap-2">
          <UInput v-model="text" :placeholder="line.label" size="lg" class="min-w-0 flex-1" :disabled="!!busy" data-inbox-line-input />
          <UButton type="submit" label="Send" icon="i-lucide-send" size="lg" class="min-h-11" :loading="busy === line.label" :disabled="!!busy || !text.trim()" data-inbox-line-send />
        </div>
        <p class="text-xs text-muted text-pretty" data-action-scope>{{ line.scope }}</p>
      </form>
      <div v-for="a in commands" :key="a.name + a.label" class="flex flex-col gap-1">
        <UButton
          :label="a.confirm ? `${a.label}…` : a.label"
          :color="a.name === 'cancel' || a.name === 'withdraw' ? 'error' : a.name === 'accept' ? 'warning' : 'neutral'"
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
        <UButton v-if="terminal && routeOf(terminal.ref)" :to="routeOf(terminal.ref)!" label="Open the terminal" icon="i-lucide-square-terminal" size="lg" class="min-h-11 self-start" data-inbox-terminal />
      </div>
      <UAlert
        v-if="refusal"
        color="warning"
        variant="subtle"
        icon="i-lucide-triangle-alert"
        :title="refusal"
        :actions="item.kind === 'question' && terminal && routeOf(terminal.ref) ? [{ label: 'Open the terminal', icon: 'i-lucide-square-terminal', color: 'neutral', variant: 'outline', class: 'min-h-11', to: routeOf(terminal.ref)! }] : undefined"
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
        <UButton v-if="askBrakeman" :to="askBrakeman" label="Ask Brakeman" icon="i-lucide-notebook-pen" color="neutral" variant="outline" size="md" class="min-h-11" data-inbox-ask-brakeman />
        <UButton v-if="ticket" :label="`Comment on ${ticket}`" icon="i-lucide-message-square" color="neutral" variant="outline" size="md" class="min-h-11" data-inbox-comment-open @click="commenting = !commenting" />
      </div>
      <form v-if="commenting" class="flex flex-col gap-2" data-inbox-comment @submit.prevent="sendComment">
        <UTextarea v-model="comment" :rows="3" autoresize placeholder="A comment on the ticket" data-inbox-comment-input />
        <UButton type="submit" :label="`Comment on ${ticket}`" icon="i-lucide-send" size="md" class="min-h-11 self-start" :loading="busy === 'Comment'" :disabled="!!busy || !comment.trim()" data-inbox-comment-send />
      </form>
      <p class="text-xs text-muted" data-inbox-dr1>Durable decisions arrive with DR1: asking for more information and commenting on a decision come with them.</p>
    </section>

    <section v-if="steps.length || item.now" class="flex flex-col gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">{{ delivery?.length ? 'What became of your action' : 'Where it is' }}</h3>
      <InboxDelivery v-if="steps.length" :steps="steps" :now="item.now" :route-of="routeOf" />
      <UAlert v-else color="neutral" variant="subtle" icon="i-lucide-info" :title="`Now: ${item.now}`" data-inbox-now />
    </section>

    <InboxEvidence v-if="item.evidence?.length" :evidence="item.evidence" :route-of="routeOf" />

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
