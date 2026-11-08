<script setup lang="ts">
import { MAX_FINDING_MESSAGE, MAX_FINDING_RESOLUTION, SEVERITIES, noteWhere, type DraftNote, type NoteIntent, type Severity } from '~/utils/review'

/**
 * A note's form (Program item RV1, PR-6): where it is (lines of one side of a file, a whole file, the candidate as a whole), the lines as
 * the person saw them, its intent (a change asked for, a question, a note), a change's severity, the words and, for a change, the
 * resolution asked for. Enter with Ctrl or Cmd adds it; Esc closes the form. Nothing is sent: the note goes into the draft.
 */
const props = defineProps<{
  /** The note's place, and its fields when it is edited. */
  note: Omit<DraftNote, 'key'>
  /** Editing a note of the draft rather than adding one. */
  editing?: boolean
}>()
const emit = defineEmits<{ save: [note: Omit<DraftNote, 'key'>]; cancel: [] }>()

const intent = ref<NoteIntent>(props.note.intent)
const severity = ref<Severity>(props.note.severity ?? 'major')
const message = ref(props.note.message)
const resolution = ref(props.note.resolution ?? '')

const intents = [
  { label: 'A change', value: 'change' },
  { label: 'A question', value: 'question' },
  { label: 'A note', value: 'note' },
]
const severities = SEVERITIES.map((s) => ({ label: s, value: s }))
const chars = (s: string) => [...s].length
const tooLong = computed(() => chars(message.value) > MAX_FINDING_MESSAGE || chars(resolution.value) > MAX_FINDING_RESOLUTION)
const ready = computed(() => !!message.value.trim() && !tooLong.value)

function save() {
  if (!ready.value) return
  const change = intent.value === 'change'
  const { severity: _s, resolution: _r, ...place } = props.note
  emit('save', { ...place, intent: intent.value, ...(change ? { severity: severity.value } : {}), message: message.value.trim(), ...(change && resolution.value.trim() ? { resolution: resolution.value.trim() } : {}) })
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.preventDefault()
    e.stopPropagation()
    emit('cancel')
  } else if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
    e.preventDefault()
    save()
  }
}
const area = useTemplateRef<{ textareaRef?: HTMLTextAreaElement }>('area')
onMounted(() => nextTick(() => area.value?.textareaRef?.focus()))
</script>

<template>
  <div class="flex flex-col gap-2 rounded-lg border border-default bg-default p-3 shadow-lg" data-review-note-form :data-review-note-kind="note.kind" @keydown="onKey">
    <div class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-xs">
      <UIcon name="i-lucide-message-square-plus" class="size-3.5 flex-none self-center text-muted" />
      <span class="font-medium text-highlighted">{{ editing ? 'Edit the note on' : 'A note on' }}</span>
      <span class="min-w-0 break-all font-mono text-[11px] text-muted" data-review-note-where>{{ noteWhere(note) }}</span>
    </div>
    <pre v-if="note.kind === 'line' && note.lines?.length" class="max-h-24 overflow-auto rounded border border-default bg-elevated/40 px-2 py-1 font-mono text-[11px] leading-snug" data-review-note-lines><template v-for="(l, i) in note.lines" :key="i"><span class="select-none text-muted">{{ String((note.from ?? 1) + i).padStart(3, ' ') }}  </span>{{ l }}
</template></pre>
    <div class="flex flex-wrap items-center gap-2">
      <USelect v-model="intent" :items="intents" size="xs" class="w-32" aria-label="What the note is" data-review-note-intent />
      <USelect v-if="intent === 'change'" v-model="severity" :items="severities" size="xs" class="w-28" aria-label="Severity" data-review-note-severity />
    </div>
    <UTextarea
      ref="area"
      v-model="message"
      :rows="2"
      autoresize
      :maxrows="8"
      :placeholder="intent === 'question' ? 'What do you want to know?' : intent === 'change' ? 'What should change, and why' : 'Say something about this'"
      aria-label="The note's words"
      data-review-note-message
    />
    <UInput v-if="intent === 'change'" v-model="resolution" size="sm" placeholder="The resolution you ask for (optional)" aria-label="The resolution asked for" data-review-note-resolution />
    <p v-if="tooLong" class="text-xs text-warning">The words and the resolution are at most {{ MAX_FINDING_MESSAGE }} characters each.</p>
    <div class="flex items-center justify-end gap-1.5">
      <span class="mr-auto text-[11px] text-muted">Ctrl+Enter adds it · Esc closes</span>
      <UButton label="Cancel" size="xs" color="neutral" variant="ghost" @click="emit('cancel')" />
      <UButton :label="editing ? 'Save the note' : 'Add to the draft'" size="xs" :disabled="!ready" data-review-note-save @click="save" />
    </div>
  </div>
</template>
