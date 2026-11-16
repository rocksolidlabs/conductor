<script setup lang="ts">
/**
 * The Yard's two views, Grid and Roundhouse, as a pair of links in the header of each (the UX review's P12): the Roundhouse keeps its
 * route, its name and its keys, and is reached from here rather than from the nav. The view shown carries `aria-current`. From `sm` up
 * each says its word; below it each is an icon named by its `aria-label`, and under a coarse pointer each is at least 44 px each way.
 */
const props = defineProps<{ current: 'grid' | 'roundhouse' }>()

const VIEWS = [
  { key: 'grid', label: 'Grid', to: '/yard', icon: 'i-lucide-layout-grid' },
  { key: 'roundhouse', label: 'Roundhouse', to: '/roundhouse', icon: 'i-lucide-gallery-horizontal' },
] as const
</script>

<template>
  <nav class="flex flex-none items-center gap-0.5 rounded-md border border-default p-0.5" aria-label="Yard views" data-yard-views>
    <IconButton
      v-for="v in VIEWS"
      :key="v.key"
      :to="v.to"
      :icon="v.icon"
      size="xs"
      color="neutral"
      :variant="props.current === v.key ? 'soft' : 'ghost'"
      :label="v.label"
      :aria-current="props.current === v.key ? 'page' : undefined"
      :data-yard-view="v.key"
      class="pointer-coarse:min-h-11 pointer-coarse:min-w-11 pointer-coarse:justify-center"
      :class="props.current === v.key ? 'text-highlighted' : 'text-muted'"
    >
      <span class="hidden sm:inline">{{ v.label }}</span>
    </IconButton>
  </nav>
</template>
