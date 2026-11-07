<script setup lang="ts">
import type { RowPrompt } from '~/utils/sidebarActions'
import { MODE_CHOICE, choiceKey } from '~/utils/attention'
import { bindDraft } from '~/utils/reply'

/**
 * A prompt answered in the row (design 3b, 3c): the choices as numbered buttons, a free-text prompt as a reply field where Enter
 * sends. Busy while an answer is on its way; waiting, disabled, with the words while a host is away. It sits beside the row's link,
 * never inside it (a link holds no button), and keeps its keys to itself so that the list's and the window's digit keys never see
 * what is typed.
 */
const props = withDefaults(defineProps<{ prompt: RowPrompt; busy?: boolean; big?: boolean }>(), { busy: false, big: false })
/** Each answer names the question the row showed as it was clicked or typed (its id). */
const emit = defineEmits<{ answer: [index: number, question: string | undefined]; reply: [text: string, question: string | undefined] }>()

const text = ref('')
// While the host is away, or the session waits for its terminal, nothing typed here would go.
const off = computed(() => props.busy || props.prompt.away || props.prompt.held)
/**
 * The question a line was begun for (as its first character was typed): it names that one when sent, whatever the row shows by
 * then (someone else answered, the next question came), so that the owner refuses it rather than take it for the next.
 */
const draftFor = ref<string | undefined>()
watch(text, (now, before) => {
  draftFor.value = bindDraft(before, now, draftFor.value, props.prompt.question)
})

function send() {
  const t = text.value.trim()
  if (!t || off.value) return
  emit('reply', t, draftFor.value)
  text.value = ''
}
</script>

<template>
  <div class="flex flex-wrap items-center gap-1.5 pb-1.5 pr-2" :class="big ? 'flex-col items-stretch pl-2 pt-1' : 'pl-10'" :title="prompt.away || prompt.held ? prompt.placeholder : undefined" data-row-answer>
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
        :ui="{ label: 'truncate' }"
        :title="o.mode ? `${o.label} (${MODE_CHOICE.toLowerCase()})` : o.label"
        :disabled="off"
        :loading="busy"
        :data-row-choice="i + 1"
        :data-choice-mode="o.mode || undefined"
        @click="emit('answer', i, prompt.question)"
      >
        <template v-if="choiceKey(o, i)" #leading><UKbd :value="String(choiceKey(o, i))" size="sm" class="opacity-70" /></template>
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
      @keydown.stop
      @keydown.enter.prevent="send"
    >
      <template #trailing>
        <UButton icon="i-lucide-send" size="xs" color="neutral" variant="link" class="p-0" aria-label="Send the reply" :disabled="off || !text.trim()" data-row-reply-send @click="send" />
      </template>
    </UInput>
  </div>
</template>
