<script setup lang="ts">
/**
 * A page's standing paragraph, folded (the UX review's P23, owner decision 10): once the page has content the paragraph leaves the
 * body for this ghost button in the page's header, "About this page", which opens it in a popover (`data-page-about-open`). While the
 * page has nothing to show the paragraph stands in its place and this button is not drawn: give `shown` the page's state as content.
 */
defineProps<{ shown: boolean }>()
const open = ref(false)
</script>

<template>
  <UPopover v-if="shown" v-model:open="open" :content="{ align: 'end' }">
    <UTooltip text="About this page" :disabled="open">
      <UButton icon="i-lucide-info" color="neutral" variant="ghost" aria-label="About this page" data-page-about-button />
    </UTooltip>
    <template #content>
      <div class="max-w-sm p-3 text-sm text-muted" data-page-about-open>
        <slot />
      </div>
    </template>
  </UPopover>
</template>
