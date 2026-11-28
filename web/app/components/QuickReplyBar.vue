<script setup lang="ts">
import { ANSWER_IN_CHAT, ANSWER_IN_TERMINAL, answerDigit, answersInChat, answersInTerminal, choiceTitle, choiceKey, decisionAnsweredBy, decisionOf, MARKED_BY_PERSON, markedByPerson, noReply, offeredChoices, recommendedChoice, startupInTerminal, takesReply, waitingHeading, waitsForTerminal } from '~/utils/attention'
import type { Attention, Role } from '~/utils/protocol'
import { questionOf, replyLine, type ReplyTaken } from '~/utils/reply'

/**
 * `answers`: the viewer is the operator (the session page, the Yard's focused panel), who answers a question that mirrors a durable
 * decision as the decision: Answer opens its card. Without it (a shared session's viewer, whose link no decision route takes) the bar
 * says where the decision is answered, and emits nothing for it.
 */
/**
 * `field` false: the card draws no reply field of its own, for the chat's box answers the question (the session page in Chat, UX3 R5);
 * its choices, plan and other forms stay.
 */
const props = withDefaults(defineProps<{ attention?: Attention; agentName: string; role: Role; busy?: boolean; answers?: boolean; pastes?: boolean; field?: boolean }>(), { busy: false, answers: false, pastes: false, field: true })
/**
 * Each reply names the question the bar showed as it was clicked, typed or keyed (its id; none from an older owner). `openPlan` is
 * the plan a plan approval is about (#289), to open in the editor; `previewPlan` the same plan, to open rendered (#432); `answer` the
 * durable decision the question mirrors (`dr-7`), to open its card; `paste` asks the page for its terminal's paste control
 * (`pastes`: the session's page, which has it). A reply hands its page `taken`, which the page calls once it knows whether the line
 * was sent: the field empties then, and keeps a line not sent as typed (#654).
 */
const emit = defineEmits<{ reply: [text: string, question: string | undefined, taken: ReplyTaken]; option: [index: number, question: string | undefined]; openPlan: [path: string]; previewPlan: [path: string]; answer: [decision: string]; paste: []; chat: [] }>()

/**
 * The line, and the question it was begun for (as its first character was typed): it names that one when sent, whatever the bar shows
 * by then (someone else answered, the next question came), so that the owner refuses it rather than take it for the next; a line
 * that came back refused stays bound to it until the person changes it (replyLine, #654).
 */
const draft = replyLine(() => questionOf(props.attention))
const text = draft.text
const show = computed(() => props.attention?.state === 'needs_input')
// A held turn (text typed and not entered): no reply is taken until someone types in the terminal, which it would only wait for.
const held = computed(() => waitsForTerminal(props.attention))
const heading = computed(() => waitingHeading(props.attention, props.agentName))
// `<agent> asks:` leads the agent's own question; a person's mark reads as two sentences (`<agent> needs you. Marked by a person.`).
const lead = computed(() => (markedByPerson(props.attention) ? `${heading.value}. ` : `${heading.value}: `))
const message = computed(() => (markedByPerson(props.attention) ? MARKED_BY_PERSON : props.attention?.message || 'Answer here or in the terminal. First reply wins; everyone sees who answered.'))
// A permission dialog whose choices are not given (#288): its Enter would pick what it highlights, so it is answered in the terminal.
const terminal = computed(() => answersInTerminal(props.attention))
// A startup question with no choices (#902: a sign-in screen that asks for a code): answered in the terminal, whose paste control
// the session's page opens from here.
const startup = computed(() => startupInTerminal(props.attention))
// A question that mirrors a durable decision (decisionOf) is answered as the decision: no reply field, choice or number key.
const decision = computed(() => decisionOf(props.attention))
// A chat's permission request is answered with its own buttons in the chat (`chat` brings it into view): no field or choice here.
const inChat = computed(() => answersInChat(props.attention))
const options = computed(() => offeredChoices(props.attention))
// The choice drawn filled: only the one the agent itself recommends (its label ends "(Recommended)"), never a mode choice.
const recommended = computed(() => recommendedChoice(options.value))
const canReply = computed(() => !props.busy && takesReply(props.attention, props.role))
const placeholder = computed(() => (props.role !== 'control' ? 'View only' : (noReply(props.attention) ?? `Reply to ${props.agentName}…`)))
function send() {
  if (!canReply.value) return
  const r = draft.send()
  if (r) emit('reply', r.line, r.question, r.taken)
}

function pick(i: number) {
  // The state may have changed under us (someone else answered): options
  // disappear with it, so a stale click is a no-op; one that reaches the
  // owner after the question went names it, and the owner refuses it.
  if (!canReply.value || !options.value[i]) return
  emit('option', i, questionOf(props.attention))
}

// Number keys pick an option while the bar is visible and no field has focus; a choice that changes the permission mode has none.
function onKey(e: KeyboardEvent) {
  if (!options.value.length || e.metaKey || e.ctrlKey || e.altKey) return
  const target = e.target as HTMLElement | null
  // Nor while the sidebar's list has the focus: a digit there answers the focused row (SessionSidebar).
  if (target?.closest?.('input, textarea, select, [contenteditable], .terminal-host, [data-session-list]')) return
  const n = answerDigit(e)
  if (n && n <= options.value.length && choiceKey(options.value[n - 1]!, n - 1) === n) {
    e.preventDefault()
    pick(n - 1)
  }
}
onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <!-- The card for a question (one look wherever it is drawn): amber, as everything that waits for a person, its question whole in
       one line after `<agent> asks:`, the choices beside it when they fit on its row and under it when they do not. The live line
       outside it says the question to a screen reader as it arrives (a region made with its words is not read out). -->
  <div class="contents">
    <p class="sr-only" aria-live="polite" data-quick-reply-live>{{ show ? `${lead}${message}` : '' }}</p>
    <div
      v-if="show"
      class="flex items-start gap-3 rounded-lg border border-warning bg-warning/10 px-3.5 py-2.5"
      data-quick-reply
      :data-held="held || undefined"
      :data-terminal-answer="terminal || undefined"
      :data-decision="decision"
    >
      <span class="mt-1.5 size-2 rounded-full bg-warning flex-none" aria-hidden="true" />
      <div class="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-2">
        <!-- The question whole (#187), where it is answered: wrapped over its lines, never cut (the server bounds it at 500 bytes); a long
             token breaks rather than overflows. `<agent> asks:` leads its first line in semibold (drawn before it, so that the element
             holds the question's words alone); both in the default text tone on the amber tint, never the muted one. -->
        <p class="min-w-0 flex-[1_1_16rem] break-words text-sm text-default before:font-semibold before:content-[attr(data-heading)]" :data-heading="lead" data-quick-reply-message>{{ message }}</p>
        <!-- The plan the approval is about (#289), read in the editor before answering: as its source, or rendered (#432). Icons only
             below sm, so that the two do not widen the bar on a phone (#327). -->
        <template v-if="attention?.plan">
          <UButton label="Open plan" icon="i-lucide-file-text" size="sm" color="neutral" variant="soft" class="flex-none" :ui="{ label: 'hidden sm:inline' }" aria-label="Open plan" :title="attention.plan" data-quick-reply-plan @click="emit('openPlan', attention!.plan!)" />
          <UButton label="Preview plan" icon="i-lucide-eye" size="sm" color="neutral" variant="soft" class="flex-none" :ui="{ label: 'hidden sm:inline' }" aria-label="Preview plan" :title="attention.plan" data-quick-reply-plan-preview @click="emit('previewPlan', attention!.plan!)" />
        </template>
        <UButton v-if="inChat" :label="ANSWER_IN_CHAT" size="sm" color="neutral" variant="link" class="ml-auto flex-none" data-quick-reply-in-chat @click="emit('chat')" />
        <!-- A durable decision's question takes no reply here: it is answered as the decision, in its card (the operator's Answer). -->
        <UButton v-else-if="decision && answers" :label="`Answer ${decision}`" icon="i-lucide-scale" size="sm" color="primary" class="ml-auto flex-none" data-quick-reply-answer @click="emit('answer', decision)" />
        <span v-else-if="decision" class="ml-auto text-xs text-default" data-quick-reply-decision>{{ decisionAnsweredBy(decision) }}</span>
        <!-- The choices shrink to the bar and wrap (#327): never wider than the window on a phone, every one on screen under a thumb. -->
        <div v-else-if="options.length" class="ml-auto flex min-w-0 max-w-full flex-wrap items-center justify-end gap-2">
          <UButton
            v-for="(o, i) in options"
            :key="i"
            :label="o.label"
            size="sm"
            class="max-w-full pointer-coarse:min-h-11"
            :variant="i === recommended ? 'solid' : 'outline'"
            :color="i === recommended ? 'primary' : o.mode ? 'warning' : 'neutral'"
            :data-recommended="i === recommended || undefined"
            :title="choiceTitle(o)"
            :disabled="!canReply"
            :loading="busy"
            :data-choice-mode="o.mode || undefined"
            @click="pick(i)"
          >
            <template v-if="choiceKey(o, i)" #leading><UKbd :value="String(choiceKey(o, i))" size="sm" :class="i === recommended ? 'opacity-70' : ''" /></template>
          </UButton>
        </div>
        <!-- A startup question with no choices is answered in the terminal (#902): no field takes it here; on the session's page a
             controller's button opens the terminal's paste control, which sends no Enter. -->
        <div v-else-if="startup" class="ml-auto flex min-w-0 max-w-full flex-wrap items-center justify-end gap-2" data-quick-reply-startup>
          <span class="text-xs text-default">{{ ANSWER_IN_TERMINAL }}</span>
          <UButton v-if="pastes && role === 'control'" label="Paste into the terminal" icon="i-lucide-clipboard-paste" size="sm" color="neutral" variant="soft" class="max-w-full" data-quick-reply-paste @click="emit('paste')" />
        </div>
        <!-- The form shrinks with its pane (the terminal's, beside the editor: Codex's round on #309) rather than overflowing it. -->
        <form v-else-if="field" class="ml-auto flex min-w-0 max-w-full flex-1 basis-64 items-center gap-2 sm:flex-none" @submit.prevent="send">
          <UInput v-model="text" size="sm" class="w-64 min-w-0 max-w-full flex-1 sm:flex-none" :placeholder="placeholder" :disabled="!canReply" />
          <UButton type="submit" size="sm" label="Send" trailing-icon="i-lucide-corner-down-left" :disabled="!canReply || !text.trim()" />
        </form>
      </div>
    </div>
  </div>
</template>
