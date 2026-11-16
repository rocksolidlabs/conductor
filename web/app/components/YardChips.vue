<script setup lang="ts">
import { statusOfKey } from '~/utils/status'
import { needsChipWords, type YardChip, type YardFilter } from '~/utils/wall'

/**
 * The Yard's chips (utils/wall.ts, yardChips): each a neutral button with the state's mark before its word (All has none) and its count
 * in the text's colour; the mark alone carries the state's colour, amber for Needs you. The Needs you chip says in its tooltip and its
 * accessible name that it counts the sessions on this grid alone; that name starts with its visible label, as every chip's does.
 * `connected` false adds the polling badge after them. `wrap` is the phone's row under the header: the chips wrap, each at least
 * 44 px tall.
 */
const props = withDefaults(defineProps<{ chips: YardChip[]; connected: boolean; wrap?: boolean }>(), { wrap: false })
const emit = defineEmits<{ pick: [YardFilter] }>()
</script>

<template>
  <div class="flex min-w-0 items-center gap-1.5 text-xs" :class="props.wrap && 'flex-wrap'" data-yard-chips>
    <UTooltip v-for="c in props.chips" :key="c.key" :text="c.key === 'needs' ? needsChipWords(c.count) : undefined" :disabled="c.key !== 'needs'">
      <UButton
        size="xs"
        color="neutral"
        :variant="c.active ? 'solid' : 'outline'"
        class="flex-none gap-1.5"
        :class="props.wrap && 'min-h-11 px-3'"
        :aria-label="c.key === 'needs' ? `${c.label}, ${needsChipWords(c.count)}` : undefined"
        :data-yard-filter="c.key"
        :data-active="c.active ? '' : undefined"
        @click="emit('pick', c.key)"
      >
        <span v-if="c.key !== 'all'" aria-hidden="true" class="inline-flex"><StatusMark :status="statusOfKey(c.key)" /></span>
        <span data-yard-label>{{ c.label }}</span>
        <span class="tabular-nums" data-yard-count>{{ c.count }}</span>
      </UButton>
    </UTooltip>
    <UBadge v-if="!props.connected" label="polling" color="warning" variant="subtle" size="sm" class="flex-none" />
  </div>
</template>
