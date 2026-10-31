<script setup lang="ts">
import type { DropdownMenuItem } from '@nuxt/ui'

/**
 * A row's actions on a phone (design 3e), opened by a long press in place of
 * the context menu: the row's name and meta on top, then what hover offers
 * on a desktop as 44 px buttons (a session: Open, Share…, Show in the Yard,
 * Rename…, Stop…; a run's header: Open run, Share run, Stop run…), then Cancel. Stop
 * asks again, in the row. No swipe actions: a swipe that stops a session is
 * too easy to make by accident.
 */
defineProps<{ title: string; meta: string; items: DropdownMenuItem[][] }>()
const open = defineModel<boolean>('open', { default: false })
function act(item: DropdownMenuItem) {
  open.value = false
  item.onSelect?.(new Event('select'))
}
</script>

<template>
  <UDrawer v-model:open="open" direction="bottom" :title="title" :description="meta">
    <template #body>
      <div class="flex flex-col gap-1" data-row-sheet>
        <template v-for="(group, g) in items" :key="g">
          <UButton
            v-for="item in group"
            :key="String(item.label)"
            :label="item.label"
            :icon="item.icon"
            :color="item.color ?? 'neutral'"
            variant="ghost"
            size="lg"
            class="min-h-11 justify-start"
            :disabled="item.disabled"
            :data-row-sheet-item="item.label"
            @click="act(item)"
          />
        </template>
        <UButton label="Cancel" color="neutral" variant="outline" size="lg" class="mt-2 min-h-11" data-row-sheet-cancel @click="open = false" />
      </div>
    </template>
  </UDrawer>
</template>
