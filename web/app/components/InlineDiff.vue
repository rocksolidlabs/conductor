<script setup lang="ts">
import type { TranscriptEntry } from '~/utils/protocol'
import { ReplyError } from '~/utils/reply'
import { markHidden } from '~/utils/hiddenMarks'
import { fileLine, INLINE_DIFF_LINES, type ReadPart } from '~/utils/transcript'

/**
 * A file a tool changed, in a chat's turn (docs/plans/chat-or-terminal.md, design 11): one line, "`main.go` changed.", that opens to
 * a small diff, at most INLINE_DIFF_LINES lines of its `diff` part (no editor), and a link that opens the editor's diff tab. The
 * diff's lines are text, never markup.
 */
const props = defineProps<{ entry: TranscriptEntry; read: (seq: number, name: 'diff') => Promise<ReadPart>; canOpen?: boolean }>()
const emit = defineEmits<{ openDiff: [entry: TranscriptEntry] }>()

const opened = ref(false)
const lines = ref<string[] | null>(null)
const more = ref(0)
const failed = ref('')
const head = computed(() => fileLine(props.entry))
// The file's name drawn as text, each character a person cannot see a mark.
const name = computed(() => markHidden(head.value.name))

async function toggle() {
  opened.value = !opened.value
  if (!opened.value || lines.value !== null) return
  failed.value = ''
  try {
    const part = await props.read(props.entry.seq, 'diff')
    const all = new TextDecoder().decode(part.bytes).replace(/\n$/, '').split('\n')
    lines.value = all.slice(0, INLINE_DIFF_LINES)
    more.value = Math.max(0, all.length - INLINE_DIFF_LINES)
  } catch (e) {
    lines.value = []
    failed.value = e instanceof ReplyError && e.code === 'transcript_gone' ? 'That part of the chat is no longer kept.' : 'The change could not be read.'
  }
}

function tone(l: string) {
  if (l.startsWith('@@')) return 'text-muted'
  if (l.startsWith('+')) return 'bg-success/10 text-default'
  if (l.startsWith('-')) return 'bg-error/10 text-default'
  return 'text-default'
}
</script>

<template>
  <div class="flex flex-col" data-file-change :data-file-path="entry.path" :data-seq="entry.seq">
    <button type="button" class="flex w-fit max-w-full min-w-0 items-center gap-1.5 rounded-md px-2 py-1 text-left text-sm hover:bg-elevated" :aria-expanded="opened" data-file-change-line @click="toggle">
      <UIcon :name="opened ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" class="size-3.5 flex-none text-muted" aria-hidden="true" />
      <code class="min-w-0 truncate font-mono text-xs text-highlighted" :title="entry.path"><MarkedText :pieces="name.pieces" :breaks="false" /></code>
      <span class="flex-none text-muted">{{ head.words }}</span>
    </button>
    <div v-if="opened" class="ml-6 mt-1 mb-1.5 flex flex-col gap-1" data-file-change-open>
      <p v-if="failed" class="text-xs text-muted">{{ failed }}</p>
      <pre v-else-if="lines && lines.length" class="max-h-80 overflow-auto rounded-md bg-elevated py-1 font-mono text-xs" data-inline-diff><div v-for="(l, i) in lines" :key="i" class="px-2.5 whitespace-pre-wrap break-all" :class="tone(l)" data-diff-line>{{ l || ' ' }}</div></pre>
      <UButton v-if="canOpen && entry.path && !entry.outside" :label="more ? `${more} more lines in the editor` : 'Open in the editor'" icon="i-lucide-file-diff" size="xs" color="neutral" variant="link" class="w-fit px-0" data-file-change-open-diff @click="emit('openDiff', entry)" />
    </div>
  </div>
</template>
