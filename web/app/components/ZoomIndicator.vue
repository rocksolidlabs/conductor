<script setup lang="ts">
import { useDesktopZoom } from '~/composables/useDesktopZoom'

// The desktop app's zoom level as it changes (#431), in every workbench window it zoomed: shown for a moment at the top right,
// over the page and under nothing a click needs (pointer-events-none), never focused, announced politely.
const desktop = useDesktop()
const zoom = useDesktopZoom(() => desktop.bridge.value)
</script>

<template>
  <div v-if="zoom.listening.value" role="status" aria-live="polite" aria-atomic="true" class="pointer-events-none fixed top-3 right-3 z-[60] select-none" data-zoom-status :data-zoom-last="zoom.last.value">
    <Transition leave-active-class="transition-opacity duration-300 motion-reduce:transition-none" leave-to-class="opacity-0">
      <UBadge v-if="zoom.shown.value" color="neutral" variant="solid" size="lg" class="font-mono tabular-nums shadow-lg" data-zoom-indicator>{{ zoom.shown.value }}</UBadge>
    </Transition>
  </div>
</template>
