<script setup lang="ts">
import type { RowPrompt } from '~/utils/sidebarActions'
import { MODE_CHOICE, choiceKey } from '~/utils/attention'
import { replyLine, type ReplyTaken } from '~/utils/reply'

/**
 * A prompt answered in the row (design 3b, 3c): the choices as numbered buttons, a free-text prompt as a reply field where Enter
 * sends. Busy while an answer is on its way; waiting, disabled, with the words while a host is away. It sits beside the row's link,
 * never inside it (a link holds no button), and keeps its keys to itself so that the list's and the window's digit keys never see
 * what is typed.
 */
/** `wrap`: in the question's panel (SidebarRowPanel) a label wraps over its lines rather than being cut. */
const props = withDefaults(defineProps<{ prompt: RowPrompt; busy?: boolean; big?: boolean; wrap?: boolean }>(), { busy: false, big: false, wrap: false })
/**
 * Each answer names the question the row showed as it was clicked or typed (its id). A reply hands its page `taken`, which the page
 * calls once it knows whether the line was sent: the field empties then, and keeps a line not sent as typed (#654).
 */
const emit = defineEmits<{ answer: [index: number, question: string | undefined]; reply: [text: string, question: string | undefined, taken: ReplyTaken] }>()

/**
 * The line, and the question it was begun for (as its first character was typed): it names that one when sent, whatever the row
 * shows by then (someone else answered, the next question came), so that the owner refuses it rather than take it for the next; a
 * line that came back refused stays bound to it until the person changes it (replyLine, #654).
 */
const draft = replyLine(() => props.prompt.question)
const text = draft.text
// While the host is away, or the session waits for its terminal, nothing typed here would go.
const off = computed(() => props.busy || props.prompt.away || props.prompt.held)
/** The field keeps its keys from the list's and the window's (a digit, J, X), Escape excepted: it leaves the field (the list's own). */
function keepKeys(e: KeyboardEvent) {
  if (e.key !== 'Escape') e.stopPropagation()
}

function send() {
  if (off.value) return
  const r = draft.send()
  if (r) emit('reply', r.line, r.question, r.taken)
}
</script>

<template>
  <div class="flex flex-wrap items-center gap-1.5 pb-1.5 pr-2.5" :class="big ? 'flex-col items-stretch pl-2 pt-1' : wrap ? 'px-3 pb-3' : 'pl-2.5'" :title="prompt.away || prompt.held ? prompt.placeholder : undefined" data-row-answer>
    <!-- A durable decision's question takes no reply here: the row says where it is answered (noReply). -->
    <p v-if="prompt.decision" class="text-xs text-muted" data-row-decision>{{ prompt.placeholder }}</p>
    <template v-else-if="prompt.choices.length">
      <UButton
        v-for="(o, i) in prompt.choices"
        :key="i"
        :label="o.label"
        :size="big ? 'lg' : 'xs'"
        :color="o.mode ? 'warning' : 'neutral'"
        variant="outline"
        class="min-w-0 max-w-full"
        :class="big && 'min-h-11 justify-start'"
        :ui="{ label: wrap ? 'whitespace-normal break-words text-left' : 'truncate' }"
        :title="o.mode ? `${o.label} (${MODE_CHOICE.toLowerCase()})` : o.label"
        :disabled="off"
        :loading="busy"
        :data-row-choice="i + 1"
        :data-choice-mode="o.mode || undefined"
        @click="emit('answer', i, prompt.question)"
      >
        <template v-if="choiceKey(o, i)" #leading><UKbd :value="String(choiceKey(o, i))" size="sm" class="text-muted" /></template>
      </UButton>
    </template>
    <UInput
      v-else
      v-model="text"
      :size="big ? 'lg' : 'xs'"
      :placeholder="prompt.placeholder"
      maxlength="4096"
      :disabled="off"
      :loading="busy"
      class="w-full"
      :ui="{ base: 'font-normal' }"
      aria-label="Reply"
      data-row-reply
      @keydown="keepKeys"
      @keydown.enter.prevent="send"
    >
      <template #trailing>
        <IconButton icon="i-lucide-send" size="xs" color="neutral" variant="link" class="p-0" label="Send the reply" :disabled="off || !text.trim()" data-row-reply-send @click="send" />
      </template>
    </UInput>
  </div>
</template>
