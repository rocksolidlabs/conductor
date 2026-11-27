<script setup lang="ts">
import type { BadgeProps, DropdownMenuItem } from '@nuxt/ui'
import { pageActive, pageTip, type NavTile, type PageEntry, type PageId } from '~/utils/pages'

/**
 * The full sidebar's foot (the UX review's P6): the pages as tiles in a grid of three columns, an icon over its label and a count on
 * the corner, and More, a menu of the rail's other pages. Brakeman's slot is an empty cell while the server has not said whether it
 * has Brakeman, so nothing moves when it fills. A tile's tooltip, on hover and on keyboard focus, is the page's sentence and its key.
 */
const props = defineProps<{
  tiles: NavTile[]
  more: PageEntry[]
  /** A page's count on its tile's corner; none for no count. */
  badge: (id: PageId) => BadgeProps | undefined
}>()

const route = useRoute()

/** The hooks the tests find a page's tile by, as they found its row before. */
const HOOKS: Partial<Record<PageId, string>> = { decisions: 'data-sidebar-decide', brakeman: 'data-sidebar-brakeman' }

function active(id: PageId): boolean {
  return pageActive(id, route.path)
}
function hook(id: PageId): Record<string, string> {
  const h = HOOKS[id]
  return h ? { [h]: '' } : {}
}

const moreActive = computed(() => props.more.some((p) => active(p.id)))
const moreItems = computed<DropdownMenuItem[]>(() => props.more.map((p) => ({ label: p.label, icon: p.icon, to: p.to, kbds: p.chord ? [...p.chord] : undefined, active: active(p.id) })))

/**
 * A tile's tooltip opens to the right, over the next tile: it closes as the pointer leaves its tile (`disable-hoverable-content`: there
 * is nothing in it to reach) and is gone at once, with no closing animation, so a press on the tile beside it lands on that tile, as
 * TipButton's does.
 */
const TIP_UI = { content: 'data-[state=closed]:animate-none' }

// On the canvas the theme's raised hover would vanish in light (sage on sage): a tile hovers to the canvas's hover tone, and the
// current page's tile takes the page's tone, as the sidebar's rows do.
const TILE = 'relative flex h-12 w-full min-w-0 flex-col items-center justify-center gap-0.5 rounded-md px-0 py-1 text-[11px] font-medium leading-tight tracking-tight'
function tileClass(on: boolean): string {
  return `${TILE} ${on ? 'bg-default text-highlighted' : 'text-muted hover:bg-canvas-hover hover:text-highlighted'}`
}
</script>

<template>
  <nav aria-label="Pages" class="grid w-full grid-cols-3 gap-x-0.5 gap-y-1" data-sidebar-tiles>
    <template v-for="(t, i) in tiles" :key="t.kind === 'page' ? t.page.id : t.kind">
      <div v-if="t.kind === 'held'" class="h-12" aria-hidden="true" data-sidebar-tile="held" />
      <UTooltip
        v-else-if="t.kind === 'page'"
        :text="pageTip(t.page)"
        :kbds="t.page.chord ? [...t.page.chord] : undefined"
        :content="{ side: 'right' }"
        :delay-duration="300"
        :ui="TIP_UI"
        ignore-non-keyboard-focus
        disable-hoverable-content
      >
        <UButton
          :to="t.page.to"
          color="neutral"
          variant="ghost"
          :class="tileClass(active(t.page.id))"
          :aria-current="active(t.page.id) ? 'page' : undefined"
          :data-sidebar-tile="t.page.id"
          v-bind="hook(t.page.id)"
        >
          <UIcon :name="t.page.icon" class="size-5 shrink-0" />
          <span class="block max-w-full truncate" data-nav-label>{{ t.page.label }}</span>
          <UBadge v-if="badge(t.page.id)" v-bind="badge(t.page.id)" size="sm" data-tile-badge class="absolute right-0.5 top-0.5 min-w-4 justify-center px-1 py-0 text-[10px] leading-4" />
        </UButton>
      </UTooltip>
      <UDropdownMenu v-else :items="moreItems" :content="{ side: 'top', align: i % 3 === 2 ? 'end' : 'start' }">
        <UButton color="neutral" variant="ghost" :class="tileClass(moreActive)" aria-label="More pages" data-sidebar-tile="more" :data-active="moreActive ? '' : undefined">
          <UIcon name="i-lucide-ellipsis" class="size-5 shrink-0" />
          <span class="block max-w-full truncate" data-nav-label>More</span>
        </UButton>
      </UDropdownMenu>
    </template>
  </nav>
</template>
