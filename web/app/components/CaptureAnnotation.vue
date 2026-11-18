<script setup lang="ts">
import type { CaptureSource, Observed, StoredElement } from '~/utils/capture'
import { MOVING_WORDS } from '~/utils/capture'
import type { ElementReport } from '~/utils/protocol'

/**
 * One annotation of a capture, numbered as its box on the screenshot is: the person's note, then an element's details (CaptureElement)
 * or a region's words; moved up or down and removed with the buttons beside its number. An element seen moving says so.
 */
const props = defineProps<{
  n: number
  kind: 'element' | 'region'
  note: string
  element?: ElementReport | StoredElement
  sources?: CaptureSource[]
  observed?: Observed
  /** The words the pane gave it as it was taken (that it was moving). */
  words?: string
  session?: string
  /** The tree the page is served from, where the editor knows it: a draft's hints are listed relative to it. */
  tree?: string
  first: boolean
  last: boolean
}>()
const emit = defineEmits<{ 'update:note': [note: string]; up: []; down: []; remove: [] }>()

const moving = computed(() => props.words || (props.observed?.stable === false ? MOVING_WORDS : ''))
</script>

<template>
  <li class="flex min-w-0 flex-col gap-2 rounded-md border border-default p-2" :data-annotation="n" :data-annotation-kind="kind">
    <div class="flex min-w-0 items-center gap-2">
      <span class="flex size-5 flex-none items-center justify-center rounded-full bg-inverted text-[11px] font-semibold text-inverted" aria-hidden="true">{{ n }}</span>
      <span class="min-w-0 flex-1 truncate text-xs font-medium text-highlighted">{{ kind === 'element' ? 'Element' : 'Region' }}</span>
      <IconButton icon="i-lucide-arrow-up" size="xs" color="neutral" variant="ghost" :label="`Move annotation ${n} up`" :disabled="first" data-annotation-up @click="emit('up')" />
      <IconButton icon="i-lucide-arrow-down" size="xs" color="neutral" variant="ghost" :label="`Move annotation ${n} down`" :disabled="last" data-annotation-down @click="emit('down')" />
      <IconButton icon="i-lucide-trash-2" size="xs" color="neutral" variant="ghost" :label="`Remove annotation ${n}`" data-annotation-remove @click="emit('remove')" />
    </div>
    <UTextarea :model-value="note" :rows="2" autoresize :maxlength="4000" placeholder="What is wrong here, or what should change" class="w-full" data-annotation-note @update:model-value="(v: string | number) => emit('update:note', String(v ?? ''))" />
    <p v-if="moving" class="text-xs text-muted" data-annotation-stable="false">{{ moving }}</p>
    <span v-else-if="observed?.stable" class="hidden" data-annotation-stable="true" />
    <CaptureElement v-if="element" :element="element" :sources="sources" :session="session" :tree="tree" />
    <p v-else class="text-xs text-muted">A part of the screenshot, with no element.</p>
  </li>
</template>
