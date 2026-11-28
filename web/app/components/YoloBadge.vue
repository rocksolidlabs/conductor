<script setup lang="ts">
/**
 * "yolo": the session was launched with its agent's yolo recipe, so the agent skips its permission prompts. It says Conductor applied the
 * recipe, not that no dialog can appear. Neutral in both forms: a setting that is on is a standing fact, never amber (docs/design/
 * system.md, Amber when someone must act). `icon` shows the icon alone, for a sidebar row.
 */
withDefaults(defineProps<{ icon?: boolean }>(), { icon: false })
</script>

<template>
  <UTooltip text="Launched with its agent's yolo recipe: it skips its permission prompts">
    <UIcon v-if="icon" name="i-lucide-shield-off" class="size-3.5 flex-none text-muted" aria-label="yolo" data-yolo-badge />
    <UBadge v-else label="yolo" icon="i-lucide-shield-off" color="neutral" variant="subtle" size="sm" class="flex-none" data-yolo-badge />
  </UTooltip>
</template>
