<script setup lang="ts">
import { ANSWER_IN_TERMINAL, MODE_CHOICE, TERMINAL_FIRST, answersInTerminal, choiceKey, firstChoice, waitingHeading, waitsForTerminal } from '~/utils/attention'
import type { Attention, Role } from '~/utils/protocol'
import { draftQuestion, questionOf } from '~/utils/reply'

const props = defineProps<{ attention?: Attention; agentName: string; role: Role; busy?: boolean }>()
/**
 * Each reply names the question the bar showed as it was clicked, typed or keyed (its id; none from an older owner). `openPlan` is
 * the plan a plan approval is about (#289), to open in the editor.
 */
const emit = defineEmits<{ reply: [text: string, question: string | undefined]; option: [index: number, question: string | undefined]; openPlan: [path: string] }>()

const text = ref('')
const show = computed(() => props.attention?.state === 'needs_input')
// A held turn (text typed and not entered): no reply is taken until someone types in the terminal, which it would only wait for.
const held = computed(() => waitsForTerminal(props.attention))
const heading = computed(() => waitingHeading(props.attention, props.agentName))
// A permission dialog whose choices are not given (#288): its Enter would pick what it highlights, so it is answered in the terminal.
const terminal = computed(() => answersInTerminal(props.attention))
const options = computed(() => (show.value ? (props.attention?.options ?? []) : []))
// The choice offered first, highlighted: never one that changes the permission mode.
const first = computed(() => firstChoice(options.value))
const canReply = computed(() => props.role === 'control' && !props.busy && !held.value && !terminal.value)
const placeholder = computed(() =>
  props.role !== 'control' ? 'View only' : held.value ? TERMINAL_FIRST : terminal.value ? ANSWER_IN_TERMINAL : `Reply to ${props.agentName}…`,
)
/**
 * The question a line was begun for (as its first character was typed): it names that one when sent, whatever the bar shows by
 * then (someone else answered, the next question came), so that the owner refuses it rather than take it for the next.
 */
const draftFor = ref<string | undefined>()
watch(text, (now, before) => {
  draftFor.value = draftQuestion(before, now, draftFor.value, props.attention)
})

function send() {
  const t = text.value.trim()
  if (!t || !canReply.value) return
  emit('reply', t, draftFor.value)
  text.value = ''
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
  const n = Number(e.key)
  if (Number.isInteger(n) && n >= 1 && n <= options.value.length && choiceKey(options.value[n - 1]!, n - 1) === n) {
    e.preventDefault()
    pick(n - 1)
  }
}
onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <div
    v-if="show"
    class="flex flex-wrap items-center gap-3 rounded-md border border-default bg-default px-3.5 py-3"
    data-quick-reply
    :data-held="held || undefined"
    :data-terminal-answer="terminal || undefined"
  >
    <span class="size-2 rounded-full bg-warning flex-none" aria-hidden="true" />
    <div class="min-w-0 flex flex-col gap-0.5">
      <span class="text-sm font-semibold">{{ heading }}</span>
      <!-- The question whole (#187), where it is answered: wrapped over its lines, never cut (the server bounds it at 500 bytes); a long
           token breaks rather than overflows. -->
      <span class="break-words text-xs text-muted" data-quick-reply-message>{{ attention?.message || 'Answer here or in the terminal. First reply wins; everyone sees who answered.' }}</span>
    </div>
    <!-- The plan the approval is about (#289), read in the editor before answering. -->
    <UButton v-if="attention?.plan" label="Open plan" icon="i-lucide-file-text" size="sm" color="neutral" variant="soft" class="flex-none" :title="attention.plan" data-quick-reply-plan @click="emit('openPlan', attention!.plan!)" />
    <!-- The choices shrink to the bar and wrap (#327): never wider than the window on a phone, every one on screen under a thumb. -->
    <div v-if="options.length" class="ml-auto flex min-w-0 max-w-full flex-wrap items-center justify-end gap-2">
      <UButton
        v-for="(o, i) in options"
        :key="i"
        :label="o.label"
        size="sm"
        class="max-w-full"
        :variant="i === first ? 'solid' : 'outline'"
        :color="i === first ? 'primary' : o.mode ? 'warning' : 'neutral'"
        :title="o.mode ? MODE_CHOICE : undefined"
        :disabled="!canReply"
        :loading="busy"
        :data-choice-mode="o.mode || undefined"
        @click="pick(i)"
      >
        <template v-if="choiceKey(o, i)" #leading><UKbd :value="String(choiceKey(o, i))" size="sm" :class="i === first ? 'opacity-70' : ''" /></template>
      </UButton>
    </div>
    <!-- The form shrinks with its pane (the terminal's, beside the editor: Codex's round on #309) rather than overflowing it. -->
    <form v-else class="ml-auto flex min-w-0 max-w-full flex-1 basis-64 items-center gap-2 sm:flex-none" @submit.prevent="send">
      <UInput v-model="text" size="sm" class="w-64 min-w-0 max-w-full flex-1 sm:flex-none" :placeholder="placeholder" :disabled="!canReply" />
      <UButton type="submit" size="sm" label="Send" trailing-icon="i-lucide-corner-down-left" :disabled="!canReply || !text.trim()" />
    </form>
  </div>
</template>
