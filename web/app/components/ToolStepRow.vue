<script setup lang="ts">
import type { TranscriptEntry } from '~/utils/protocol'
import { ReplyError } from '~/utils/reply'
import { markHidden } from '~/utils/hiddenMarks'
import { conductorStep, toolStepMark, type ReadPart } from '~/utils/transcript'

/**
 * One tool step of a chat's turn (docs/plans/chat-or-terminal.md, design 11): the tool and its summary in the code font, its state as
 * the status language's word; opened, its output, read on demand (one `transcript_get part` for an output that fits one piece). No
 * id, count of tokens or timing is drawn. What the tool printed is text, never markup.
 */
const props = defineProps<{ entry: TranscriptEntry; read: (seq: number, name: 'output') => Promise<ReadPart> }>()

const opened = ref(false)
const output = ref<string | null>(null)
const cut = ref(false)
const failed = ref('')
const mark = computed(() => toolStepMark(props.entry.status))
// The tool and its summary drawn as text, each character a person cannot see a mark, as the permission question draws them.
// A step of Conductor's own tools (a report, a state, a question) is drawn as what it did, in words, not as its name and input.
const own = computed(() => conductorStep(props.entry))
const tool = computed(() => markHidden(own.value?.name ?? props.entry.tool ?? ''))
const summary = computed(() => markHidden(own.value?.words ?? props.entry.summary ?? ''))

async function toggle() {
  opened.value = !opened.value
  if (!opened.value || output.value !== null || props.entry.status === 'running') return
  failed.value = ''
  try {
    const part = await props.read(props.entry.seq, 'output')
    output.value = new TextDecoder().decode(part.bytes)
    cut.value = part.cut
  } catch (e) {
    failed.value = e instanceof ReplyError && e.code === 'transcript_gone' ? 'That part of the chat is no longer kept.' : 'The step\'s output could not be read.'
  }
}
// A step that ends while open is read then.
watch(
  () => props.entry.status,
  (s, was) => {
    if (opened.value && was === 'running' && s !== 'running') {
      opened.value = false
      void toggle()
    }
  },
)
</script>

<template>
  <li class="flex flex-col" data-tool-step :data-tool-status="entry.status" :data-seq="entry.seq">
    <button type="button" class="flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-1 text-left hover:bg-elevated" :aria-expanded="opened" data-tool-step-row @click="toggle">
      <UIcon :name="opened ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" class="size-3.5 flex-none text-muted" aria-hidden="true" />
      <span v-if="own" class="flex-none text-xs font-semibold text-default" data-tool-name data-tool-own><MarkedText :pieces="tool.pieces" :breaks="false" /></span>
      <span v-else class="flex-none font-mono text-xs font-semibold text-default" data-mono="command" data-tool-name><MarkedText :pieces="tool.pieces" :breaks="false" /></span>
      <span v-if="own" class="min-w-0 flex-1 truncate text-xs text-muted" :title="own.words" data-tool-summary><MarkedText :pieces="summary.pieces" :breaks="false" /></span>
      <span v-else class="min-w-0 flex-1 truncate font-mono text-xs text-muted" :title="entry.summary" data-mono="command" data-tool-summary><MarkedText :pieces="summary.pieces" :breaks="false" /></span>
      <StatusMark :status="mark" word class="text-[11px]" />
    </button>
    <div v-if="opened" class="ml-6 mt-1 mb-1.5" data-tool-step-open>
      <p v-if="failed" class="text-xs text-muted">{{ failed }}</p>
      <pre v-else-if="output !== null" class="max-h-64 overflow-auto rounded-md bg-elevated px-2.5 py-1.5 font-mono text-xs whitespace-pre-wrap break-all text-default" data-tool-output>{{ output || ' ' }}</pre>
      <p v-if="cut" class="mt-1 text-[11px] text-muted" data-tool-output-cut>A step's output was too large to show.</p>
    </div>
  </li>
</template>
