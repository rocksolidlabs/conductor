<script setup lang="ts">
/**
 * A comment written or edited (DispatchComments): Markdown in a textarea, a Preview beside it rendered as the comment will be, the
 * ticket's images of its attachments among it, and the store's bound in bytes, past which the button is off. Ctrl+Enter (⌘+Enter)
 * sends it, Escape cancels when there is a Cancel. What is written stays while it is sent, and goes once it is done (`submit` answers
 * true), which it says (`sent`) only when nothing more was written meanwhile: what was, stays, and the editor with it.
 */
const props = defineProps<{
  /** The text it opens with: a comment's body to edit, '' for a new one. */
  initial?: string
  /** The button's words: Comment, Save. */
  action: string
  submit: (body: string) => Promise<boolean>
  attachmentUrl?: (id: string) => Promise<string | null>
  /** Offers Cancel, which emits `cancel`. */
  cancellable?: boolean
  disabled?: boolean
  placeholder?: string
}>()
const emit = defineEmits<{ cancel: []; sent: [] }>()

/** A comment's bound, the store's, in bytes. */
const MAX_COMMENT = 16 << 10

const draft = ref(props.initial ?? '')
const tab = ref<'write' | 'preview'>('write')
const sending = ref(false)
const area = useTemplateRef<{ textareaRef?: HTMLTextAreaElement }>('area')
const bytes = computed(() => new TextEncoder().encode(draft.value).length)
const over = computed(() => bytes.value > MAX_COMMENT)
const empty = computed(() => !draft.value.trim())
const kib = (n: number) => `${Math.round((n / 1024) * 10) / 10} KiB`
const tabs = [
  { label: 'Write', value: 'write', icon: 'i-lucide-pencil' },
  { label: 'Preview', value: 'preview', icon: 'i-lucide-eye' },
]

async function send() {
  if (over.value || empty.value || sending.value || props.disabled) return
  const body = draft.value
  sending.value = true
  try {
    // Done: what was sent goes, and whatever was written meanwhile stays.
    if ((await props.submit(body)) && draft.value === body) {
      draft.value = ''
      tab.value = 'write'
      emit('sent')
    }
  } finally {
    sending.value = false
  }
}

function cancel() {
  if (props.cancellable) emit('cancel')
}

/** Text put in at the caret, or at the end: an attachment's Markdown. */
function insert(text: string) {
  const el = area.value?.textareaRef
  tab.value = 'write'
  if (!el) {
    draft.value += (draft.value && !draft.value.endsWith('\n') ? '\n' : '') + text
    return
  }
  const at = el.selectionStart ?? draft.value.length
  draft.value = draft.value.slice(0, at) + text + draft.value.slice(el.selectionEnd ?? at)
  void nextTick(() => {
    el.focus()
    el.setSelectionRange(at + text.length, at + text.length)
  })
}

onMounted(() => {
  if (props.initial !== undefined && props.cancellable) void nextTick(() => area.value?.textareaRef?.focus())
})
defineExpose({ insert })
</script>

<template>
  <div class="flex flex-col gap-2" data-comment-editor>
    <UTabs v-model="tab" :items="tabs" :content="false" size="xs" color="neutral" variant="link" class="w-fit" />
    <UTextarea
      v-if="tab === 'write'"
      ref="area"
      v-model="draft"
      autoresize
      :rows="3"
      :maxrows="16"
      :placeholder="placeholder ?? 'Write a comment… Markdown, and ![an image](attachment:<id>) of the attachments'"
      :disabled="disabled"
      aria-label="Comment"
      class="w-full"
      :ui="{ base: 'text-sm leading-relaxed' }"
      data-comment-input
      @keydown.meta.enter.prevent="send"
      @keydown.ctrl.enter.prevent="send"
      @keydown.escape.stop="cancel"
    />
    <div v-else class="min-h-16 min-w-0 rounded-md ring ring-default" data-comment-draft-preview>
      <MarkdownPreview v-if="draft.trim()" :text="draft" dir="" :relative-links="false" :attachment-url="attachmentUrl" />
      <p v-else class="px-5 py-4 text-sm text-dimmed">Nothing to preview.</p>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      <span class="text-xs" :class="over ? 'text-error' : 'text-dimmed'" data-comment-bytes>{{ over ? `${kib(bytes)}: more than the ${kib(MAX_COMMENT)} a comment may hold` : `Markdown · ${kib(bytes)} of ${kib(MAX_COMMENT)}` }}</span>
      <div class="flex-1" />
      <UButton v-if="cancellable" label="Cancel" color="neutral" variant="ghost" size="sm" :disabled="sending" @click="emit('cancel')" />
      <UButton :label="action" icon="i-lucide-send" size="sm" :loading="sending" :disabled="over || empty || disabled" data-comment-submit @click="send" />
    </div>
  </div>
</template>
