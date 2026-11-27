<script setup lang="ts">
import { sessionOpen } from '~/utils/sidebar'
import type { RailShape } from '~/utils/sidebar'

/**
 * One square of the rail (design 3d): a session as its agent's initials,
 * solid while it needs you, dashed once exited; the state top right (amber,
 * its StatusMark), new events bottom right, where it comes from bottom left (a
 * laptop for another machine, a globe for a link shared with you). A run
 * link shared with you shows a crew. Every square has a tooltip.
 *
 * The one open on the page carries the selection mark (#314): a 2 px
 * primary bar down the rail's edge beside the square, clear of the corner
 * badges (the ordinal top left, the origin tile bottom left reach 4 px out
 * of the square; the bar stands 8 px out, in the rail's margin), another
 * shape in another place than the state dot and the news count, so it
 * never reads as a state. Two squares never look the same: the rail gives
 * a square whose agent's initials another square shares its folder's
 * letters instead, and an ordinal top left when those match too
 * (railModel's glyphs).
 */
const props = withDefaults(defineProps<{ shape: RailShape; member?: boolean; needsDot?: boolean }>(), { member: false, needsDot: true })
const route = useRoute()
// A session's square is current at its own page and, for a crew's member, at its place inside its crew (UX3 R72).
const current = computed(() => (props.shape.kind === 'session' ? sessionOpen(route.path, props.shape.id, route.query) : route.path === props.shape.to || route.path.startsWith(props.shape.to)))
</script>

<template>
  <UTooltip :text="shape.label" :content="{ side: 'right' }">
    <NuxtLink
      :to="shape.to"
      class="relative grid place-items-center rounded-md p-0.5 transition-colors"
      :class="current ? 'bg-default ring-1 ring-default shadow-xs' : 'hover:bg-canvas-hover'"
      :aria-label="shape.label"
      :aria-current="current ? 'page' : undefined"
      :data-rail-session="shape.kind === 'session' ? shape.id : undefined"
      :data-rail-shared-entry="shape.kind !== 'session' ? shape.id.replace(/^j:/, '') : undefined"
      :data-rail-member="member ? '' : undefined"
      :data-rail-dashed="shape.dashed ? '' : undefined"
      :data-rail-tile="shape.tile"
    >
      <span v-if="current" class="absolute -left-2 bottom-1 top-1 w-0.5 rounded-full bg-primary" data-rail-current aria-hidden="true" />
      <span v-if="shape.kind === 'run'" class="grid size-6 place-items-center rounded-md bg-default text-primary"><UIcon name="i-lucide-users" class="size-3.5" /></span>
      <SessionAvatar v-else :agent-id="shape.agentId" :label="shape.glyph" :solid="shape.state === 'needs'" :dashed="shape.dashed" />
      <span v-if="shape.ordinal" class="absolute -left-1 -top-1 grid size-3.5 place-items-center rounded-sm bg-default font-mono text-[8px] font-semibold leading-3.5 text-muted ring-2 ring-default" :data-rail-ordinal="shape.ordinal" aria-hidden="true">{{ shape.ordinal }}</span>
      <StatusMark v-if="shape.status" :status="shape.status" :quiet="!needsDot" badge class="absolute -right-0.5 -top-0.5" />
      <span v-if="shape.news" class="absolute -bottom-1 -right-1 grid min-w-3.5 place-items-center rounded-full bg-default px-0.5 text-[8px] font-semibold leading-3.5 text-highlighted ring-2 ring-default" :data-rail-news="shape.news" aria-hidden="true">{{ shape.news }}</span>
      <span v-if="shape.tile" class="absolute -bottom-1 -left-1 grid size-3.5 place-items-center rounded-sm bg-default text-muted ring-2 ring-default" aria-hidden="true">
        <UIcon :name="shape.tile === 'machine' ? 'i-lucide-laptop' : 'i-lucide-globe'" class="size-2.5" />
      </span>
    </NuxtLink>
  </UTooltip>
</template>
