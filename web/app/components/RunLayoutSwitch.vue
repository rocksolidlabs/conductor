<script setup lang="ts">
import { RUN_LAYOUTS, RUN_LAYOUT_LOOK, type RunLayout } from '~/utils/wall'

/** How a run's member tiles are laid out: one icon button for each layout, the one in use pressed. */
const model = defineModel<RunLayout>({ required: true })
</script>

<template>
  <div class="flex items-center gap-0.5 rounded-md border border-default p-0.5" role="group" aria-label="Layout" data-run-layout-switch>
    <IconButton
      v-for="l in RUN_LAYOUTS"
      :key="l"
      :label="RUN_LAYOUT_LOOK[l].label"
      :icon="RUN_LAYOUT_LOOK[l].icon"
      size="xs"
      color="neutral"
      :variant="model === l ? 'soft' : 'ghost'"
      :aria-pressed="model === l"
      :data-run-layout-choice="l"
      @click="model = l"
    />
  </div>
</template>
