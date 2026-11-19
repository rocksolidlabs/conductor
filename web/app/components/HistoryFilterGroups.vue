<script setup lang="ts">
import type { HistoryFilter } from '~/utils/history'

/**
 * The History page's filter chips, by group (the review of its screens, #1024): each chip writes one filter of the query, a pressed one
 * filled with the brand's tint, its text at full contrast, with a check mark (aria-pressed); an unpressed one an outline. A group's
 * `more` chips (the agents with no history) wait behind "More agents". Drawn beside the results on a wide window and in the Filters
 * sheet on a narrow one; `big` gives each chip a phone's 44 px.
 */
export interface FilterChip {
  value: string
  label: string
}
export interface FilterGroup {
  name: HistoryFilter
  label: string
  chips: FilterChip[]
  more?: FilterChip[]
  moreLabel?: string
}
const props = withDefaults(defineProps<{ groups: FilterGroup[]; on: (name: HistoryFilter, value: string) => boolean; big?: boolean }>(), { big: false })
const emit = defineEmits<{ press: [name: HistoryFilter, value: string] }>()
const opened = ref<Partial<Record<HistoryFilter, boolean>>>({})
const shown = (g: FilterGroup) => (opened.value[g.name] ? [...g.chips, ...(g.more ?? [])] : g.chips)
const chipClass = computed(() => (props.big ? 'min-h-11 px-3' : ''))
</script>

<template>
  <div class="flex min-w-0 flex-col gap-3">
    <div v-for="g in groups" :key="g.name" role="group" :aria-label="g.label" class="min-w-0" :data-history-chips-of="g.name">
      <h3 class="mb-1 text-[11px] font-semibold uppercase tracking-wide text-muted">{{ g.label }}</h3>
      <div class="flex flex-wrap gap-1">
        <UButton
          v-for="c in shown(g)"
          :key="c.value"
          :label="c.label"
          size="xs"
          :color="on(g.name, c.value) ? 'primary' : 'neutral'"
          :variant="on(g.name, c.value) ? 'solid' : 'outline'"
          :class="chipClass"
          :aria-pressed="on(g.name, c.value)"
          :data-history-chip="`${g.name}:${c.value}`"
          :data-history-chip-on="on(g.name, c.value) ? '' : undefined"
          @click="emit('press', g.name, c.value)"
        >
          <template v-if="on(g.name, c.value)" #leading><UIcon name="i-lucide-check" class="size-3.5" aria-hidden="true" data-history-chip-check /></template>
        </UButton>
        <UButton
          v-if="g.more?.length"
          :label="opened[g.name] ? 'Fewer' : (g.moreLabel ?? 'More')"
          :trailing-icon="opened[g.name] ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
          size="xs"
          color="neutral"
          variant="ghost"
          :class="chipClass"
          :aria-expanded="!!opened[g.name]"
          :data-history-chips-more="g.name"
          @click="opened[g.name] = !opened[g.name]"
        />
      </div>
    </div>
  </div>
</template>
