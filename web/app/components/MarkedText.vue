<script setup lang="ts">
import type { MarkedPiece } from '~/utils/hiddenMarks'

/**
 * Text drawn as utils/hiddenMarks.ts marks it, never applied: words as typed, a character a person cannot see as its escape on a
 * marked background, a line feed as a line break (`breaks`; else a muted ⏎ in one line), and where the whole of an input is drawn, a
 * line's end and the spaces left at it as marks of their own.
 */
withDefaults(defineProps<{ pieces: MarkedPiece[]; breaks?: boolean }>(), { breaks: true })
</script>

<template>
  <template v-for="(p, i) in pieces" :key="i"
    ><span v-if="'mark' in p" class="rounded-sm bg-warning/30 px-0.5 text-highlighted" data-hidden-mark>{{ p.mark }}</span
    ><br v-else-if="'br' in p && breaks" /><span v-else-if="'br' in p" class="text-muted" data-line-break>⏎ </span
    ><span v-else-if="'eol' in p" class="select-none text-muted" data-line-end>↵</span
    ><span v-else-if="'spaces' in p" class="rounded-sm bg-elevated text-muted" data-trailing-spaces>{{ '·'.repeat(p.spaces) }}</span
    ><template v-else>{{ p.text }}</template></template
  >
</template>
