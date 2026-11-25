<script setup lang="ts">
import { itemTooltip, pickItem, pillText, railView, type InspectorItem, type InspectorView } from '~/utils/inspector'

/**
 * The inspector's rail (design 4h, #301): 44 px on the panel's outer edge,
 * one icon button a view, a neutral count pill where the view has a count
 * (never a status colour: amber stays needs-you's), the active item
 * elevated with a 2 px primary bar on the outer edge (the sidebar rail's
 * selection idiom, #314), a tooltip to the left naming the view and its
 * count. Clicking the active item collapses the panel; any item opens it on
 * that view; the rail stays when the panel is collapsed, so the counts read
 * without opening anything. The toggle at the foot opens and collapses too.
 * A rail that cannot collapse (the Yard's and the join page's Files aside)
 * has no toggle and keeps the panel open.
 */
const props = withDefaults(defineProps<{ items: InspectorItem[]; collapsible?: boolean }>(), { collapsible: true })
const view = defineModel<InspectorView>('view', { required: true })
const open = defineModel<boolean>('open', { default: true })

function pick(id: InspectorView) {
  const next = pickItem(id, view.value, open.value, props.collapsible)
  view.value = next.view
  open.value = next.open
}
</script>

<template>
  <div class="relative flex h-full w-11 flex-none flex-col items-center gap-1.5 border-l border-default bg-default px-1 py-2.5" data-inspector-rail :data-rail-open="open ? 'true' : 'false'">
    <UTooltip v-for="it in items" :key="it.id" :text="itemTooltip(it)" :content="{ side: 'left' }">
      <button
        type="button"
        class="relative grid size-9 place-items-center rounded-md transition-colors"
        :class="open && railView(view) === it.id ? 'bg-elevated text-highlighted' : 'text-muted hover:bg-elevated/60 hover:text-default'"
        :aria-label="itemTooltip(it)"
        :aria-pressed="open && railView(view) === it.id ? 'true' : 'false'"
        :data-rail-item="it.id"
        :data-chat-tab="it.chat ? '' : undefined"
        @click="pick(it.id)"
      >
        <UIcon :name="it.icon" class="size-[18px]" aria-hidden="true" />
        <!-- The pill sits inside the rail (#325): at -right-1.5 it stuck out past the rail's edge by two pixels, and the session page's body, which
             scrolls on its own, grew a horizontal scrollbar for them. -->
        <span v-if="pillText(it.count)" class="absolute -right-0.5 -top-1 min-w-4 rounded-full bg-elevated px-1 text-[10px] font-semibold leading-4 text-highlighted ring-2 ring-default" :data-rail-count="it.countOf ?? it.id" aria-hidden="true">{{ pillText(it.count) }}</span>
        <span v-if="open && railView(view) === it.id" class="absolute -right-1 bottom-2 top-2 w-0.5 rounded-full bg-primary" data-rail-active aria-hidden="true" />
      </button>
    </UTooltip>
    <span class="flex-1" />
    <UTooltip v-if="collapsible" :text="open ? 'Collapse the inspector' : 'Open the inspector'" :content="{ side: 'left' }">
      <button type="button" class="grid size-8 place-items-center rounded-md text-muted hover:bg-elevated/60 hover:text-default" :aria-label="open ? 'Collapse the inspector' : 'Open the inspector'" data-rail-toggle @click="open = !open">
        <UIcon :name="open ? 'i-lucide-panel-right-close' : 'i-lucide-panel-right-open'" class="size-[18px]" aria-hidden="true" />
      </button>
    </UTooltip>
  </div>
</template>
