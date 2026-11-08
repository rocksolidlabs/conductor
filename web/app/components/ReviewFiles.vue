<script setup lang="ts">
import type { DiffFile } from '~/composables/useReview'

/**
 * The files of a candidate's diff (Program item RV1, PR-6): each with its status (added, changed, deleted, renamed from), its lines
 * added and removed, whether it is binary, and the draft's notes on it; one picked shows its diff. A list, never a table: one column
 * on a phone.
 */
defineProps<{
  files: DiffFile[]
  selected: string
  /** By path: the draft's notes on the file. */
  notes: Record<string, number>
  truncated?: boolean
}>()
const emit = defineEmits<{ select: [path: string] }>()

const statusWords: Record<DiffFile['status'], { label: string; color: 'success' | 'warning' | 'error' | 'info' }> = {
  A: { label: 'added', color: 'success' },
  M: { label: 'changed', color: 'warning' },
  D: { label: 'deleted', color: 'error' },
  R: { label: 'renamed', color: 'info' },
}
</script>

<template>
  <div class="flex flex-col gap-1" data-review-files :data-review-files-count="files.length">
    <h4 class="text-xs font-medium text-muted">{{ files.length === 1 ? '1 file' : `${files.length} files` }} changed</h4>
    <p v-if="!files.length" class="text-xs text-dimmed">The candidate and the base hold the same files.</p>
    <ul class="flex flex-col gap-0.5">
      <li v-for="f in files" :key="f.path">
        <button
          type="button"
          class="flex w-full min-w-0 items-center gap-1.5 rounded px-1.5 py-1 text-left text-xs hover:bg-elevated/60"
          :class="f.path === selected ? 'bg-elevated ring ring-default' : ''"
          :aria-current="f.path === selected ? 'true' : undefined"
          data-review-file
          :data-review-file-path="f.path"
          :data-review-file-status="f.status"
          @click="emit('select', f.path)"
        >
          <UBadge :label="statusWords[f.status].label" :color="statusWords[f.status].color" variant="subtle" size="sm" class="flex-none" />
          <span class="min-w-0 flex-1 truncate font-mono" :title="f.from ? `${f.from} → ${f.path}` : f.path">{{ f.from ? `${f.from} → ` : '' }}{{ f.path }}</span>
          <span v-if="f.binary" class="flex-none text-dimmed">binary</span>
          <span v-else class="flex-none font-mono text-[11px]"><span class="text-success">+{{ f.added }}</span> <span class="text-error">−{{ f.removed }}</span></span>
          <UBadge v-if="notes[f.path]" :label="String(notes[f.path])" color="warning" variant="subtle" size="sm" class="flex-none" :title="`${notes[f.path]} notes of the draft`" data-review-file-notes />
        </button>
      </li>
    </ul>
    <p v-if="truncated" class="text-xs text-warning">More files differ than one reply holds: the first are listed.</p>
  </div>
</template>
