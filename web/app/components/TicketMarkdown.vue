<script setup lang="ts">
/**
 * A ticket's Markdown field, its description or its acceptance criteria (docs/plans/dispatch.md, "web/app"): the text rendered by the
 * editor's preview (MarkdownPreview, with no folder and its relative links as words, since a ticket's text names no file of a
 * repository), and Edit: a textarea with a Preview beside it, saved as one operation by the page (`save`, which answers whether it was
 * done; the editor stays open with the text when it was not). Its bound is the store's, in bytes: past it Save is off and the field
 * says so. Ctrl+Enter (⌘+Enter) saves, Escape leaves the text as it was. A save is made against the text the editor opened on: when
 * another person saved theirs meanwhile (the page reads the ticket live, behind the editor), Save sends nothing, keeps the draft and
 * shows their text; a second Save replaces it, knowingly. The text stays editable while a save goes: what is written meanwhile stays
 * in the editor once the save is done, and its next Save is made against the text saved. An image naming one of the ticket's
 * attachments (`![a screenshot](attachment:<id>)`) is shown from its content, fetched with the token.
 */
const props = defineProps<{
  /** The text as the store holds it. */
  text: string
  label: string
  /** What an empty field says, and offers to write. */
  empty: string
  /** The store's bound, in bytes of UTF-8. */
  maxBytes: number
  /**
   * Saves the text, made against `base`, the text the editor opened on: true once the store has it; false when it was not done, or not
   * sent because the field no longer holds `base` when the save goes.
   */
  save: (text: string, base: string) => Promise<boolean>
  disabled?: boolean
  /** The field's name, for the page's tests. */
  name: string
  /** The ticket's images of its attachments (`![…](attachment:<id>)`), as object URLs of their content (useDispatchAttachments). */
  attachmentUrl?: (id: string) => Promise<string | null>
}>()

const editing = ref(false)
const draft = ref('')
/** The text the editor opened on, or the other person's once shown, or the one saved while it stayed open: what a save is made against. */
const base = ref('')
/** Another person's text, saved while this one was written: shown once a Save found it, until the editor closes. */
const theirs = ref<string | null>(null)
const tab = ref<'write' | 'preview'>('write')
const saving = ref(false)
/** Each opening of the editor, counted: a save's answer acts on the editor only while it is the opening the save was made in. */
let opened = 0
const area = useTemplateRef<{ textareaRef?: HTMLTextAreaElement }>('area')

const bytes = computed(() => new TextEncoder().encode(draft.value).length)
const over = computed(() => bytes.value > props.maxBytes)
const kib = (n: number) => `${Math.round((n / 1024) * 10) / 10} KiB`
const tabs = [
  { label: 'Write', value: 'write', icon: 'i-lucide-pencil' },
  { label: 'Preview', value: 'preview', icon: 'i-lucide-eye' },
]

function edit() {
  if (props.disabled) return
  draft.value = props.text
  base.value = props.text
  theirs.value = null
  tab.value = 'write'
  opened++
  editing.value = true
  void nextTick(() => area.value?.textareaRef?.focus())
}

function cancel() {
  editing.value = false
  theirs.value = null
}

/** The field holds another text than the one the editor opened on: it is shown, and is what the next Save is made against. */
function changedUnder(): boolean {
  if (props.text === base.value) return false
  theirs.value = props.text
  base.value = props.text
  return true
}

async function submit() {
  if (over.value || saving.value) return
  if (draft.value === props.text) {
    cancel()
    return
  }
  if (changedUnder()) return
  const text = draft.value
  const session = opened
  saving.value = true
  try {
    const done = await props.save(text, base.value)
    // The editor left, or opened again, while the save went: what it holds now is not this save's to close.
    if (!editing.value || session !== opened) return
    if (!done) changedUnder()
    else if (draft.value === text) cancel()
    else {
      // Written on while the save went: the newer text stays, and its Save is made against the text saved.
      base.value = text
      theirs.value = null
    }
  } finally {
    saving.value = false
  }
}

// A ticket deleted, or its project archived, while the field is open: nothing can be saved, so the editor goes.
watch(
  () => props.disabled,
  (d) => {
    if (d) editing.value = false
  },
)
</script>

<template>
  <section class="flex min-w-0 flex-col gap-2" :data-ticket-markdown="name">
    <div class="flex items-center gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">{{ label }}</h3>
      <UButton v-if="!editing && !disabled" icon="i-lucide-pencil" size="xs" color="neutral" variant="ghost" :aria-label="`Edit the ${label.toLowerCase()}`" class="ml-auto" data-markdown-edit @click="edit" />
    </div>

    <template v-if="!editing">
      <div v-if="text" class="min-w-0 rounded-md ring ring-default" data-markdown-view>
        <MarkdownPreview :text="text" dir="" :relative-links="false" :attachment-url="attachmentUrl" />
      </div>
      <button v-else type="button" class="rounded-md border border-dashed border-default px-4 py-3 text-left text-sm text-dimmed enabled:hover:border-accented enabled:hover:text-muted" :disabled="disabled" data-markdown-empty @click="edit">
        {{ empty }}
      </button>
    </template>

    <div v-else class="flex flex-col gap-2" data-markdown-editor>
      <div v-if="theirs !== null" class="flex flex-col gap-2 rounded-md p-3 ring ring-warning/40" data-markdown-theirs>
        <p class="flex items-start gap-2 text-sm text-warning">
          <UIcon name="i-lucide-triangle-alert" class="mt-0.5 size-4 flex-none" />
          <span>The {{ label.toLowerCase() }} was changed while you wrote. It now reads as below: Save again to replace it with yours, or Cancel to keep it.</span>
        </p>
        <div class="min-w-0 rounded-md ring ring-default" data-markdown-theirs-text>
          <MarkdownPreview v-if="theirs" :text="theirs" dir="" :relative-links="false" :attachment-url="attachmentUrl" />
          <p v-else class="px-5 py-4 text-sm text-dimmed">Nothing: it was emptied.</p>
        </div>
      </div>
      <UTabs v-model="tab" :items="tabs" :content="false" size="xs" color="neutral" variant="link" class="w-fit" data-markdown-tabs />
      <UTextarea
        v-if="tab === 'write'"
        ref="area"
        v-model="draft"
        autoresize
        :rows="6"
        :maxrows="24"
        :aria-label="label"
        class="w-full"
        :ui="{ base: 'font-mono text-[13px] leading-relaxed' }"
        data-markdown-input
        @keydown.meta.enter.prevent="submit"
        @keydown.ctrl.enter.prevent="submit"
        @keydown.escape.prevent.stop="cancel"
      />
      <div v-else class="min-h-24 min-w-0 rounded-md ring ring-default" data-markdown-draft-preview>
        <MarkdownPreview v-if="draft" :text="draft" dir="" :relative-links="false" :attachment-url="attachmentUrl" />
        <p v-else class="px-5 py-4 text-sm text-dimmed">Nothing to preview.</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <span class="text-xs" :class="over ? 'text-error' : 'text-dimmed'" data-markdown-bytes>{{ over ? `${kib(bytes)}: more than the ${kib(maxBytes)} it may hold` : `Markdown · ${kib(bytes)} of ${kib(maxBytes)}` }}</span>
        <div class="flex-1" />
        <UButton label="Cancel" color="neutral" variant="ghost" size="sm" @click="cancel" />
        <UButton label="Save" icon="i-lucide-check" size="sm" :loading="saving" :disabled="over" data-markdown-save @click="submit" />
      </div>
    </div>
  </section>
</template>
