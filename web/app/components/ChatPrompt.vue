<script setup lang="ts">
import type { TranscriptEntry } from '~/utils/protocol'
import { markHidden } from '~/utils/hiddenMarks'
import { promptParts } from '~/utils/transcript'

/**
 * A message the person wrote, as the conversation draws it (UX3 R10): one sent for the agent to expand draws its leading command and
 * each file it names as a chip in the code font (a file icon on a file), a character a person cannot see as a mark; one sent as
 * written is its text alone. No word says a file was attached: the agent's own steps show what it did.
 */
const props = defineProps<{ entry: TranscriptEntry }>()
const parts = computed(() => promptParts(props.entry))
</script>

<template>
  <template v-for="(p, i) in parts" :key="i"
    ><template v-if="p.kind === 'text'">{{ p.text }}</template
    ><span v-else class="inline-flex items-center gap-1 rounded-sm bg-default px-1 font-mono text-xs" :data-mono="p.kind === 'command' ? 'command' : 'path'" :data-chat-chip="p.kind"
      ><UIcon v-if="p.kind === 'mention'" name="i-lucide-file" class="size-3 text-muted" aria-hidden="true" /><MarkedText :pieces="markHidden(p.text).pieces" :breaks="false"
    /></span></template
  >
</template>
