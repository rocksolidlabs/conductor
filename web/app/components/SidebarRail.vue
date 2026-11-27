<script setup lang="ts">
import { needsDotShown, railModel, sidebarModel } from '~/utils/sidebar'
import { needsCount } from '~/utils/needsYou'

/**
 * The sidebar as a rail (design 3d): the counts on top (how many need you,
 * then how many have new events), Launch, a search button that opens the
 * full sidebar on its filter, then every session as a square and every run
 * as a capsule with its sessions inside, the play icon amber while one needs
 * you, in the list's order: needs you, finished, working, shared, exited. Loose exited
 * sessions fold into +N, which opens the full sidebar on Exited. The mark,
 * the pages, the menus and the expand button are the layout's header and
 * footer.
 */
const emit = defineEmits<{ search: [] }>()

const attention = useAttention()
const events = useEvents()
const launch = useLaunchModal()
const joined = useJoined()
const sidebar = useSidebar()
const folds = useSidebarFolds()
const unread = useChatUnread()
const now = useNow()

// The one list of what needs you (useNeedsYou): the rail is drawn from the full sidebar's model, its runs' decisions and tasks among
// it, so a run that waits on one is amber here as it is in Needs you. The top count is its one number, the nav's: nothing while it
// is not known.
const needs = useNeedsYou()
const rail = computed(() => railModel(sidebarModel(attention.sessions.value, attention.runOf, needs.items.value), joined.list.value, events.marks.value, unread.counts.value, now.value))
const needsShown = computed(() => (needs.number.value ? needsCount(needs.list.value) : ''))
const needsDot = computed(() => needsDotShown(events.routes.value))

/** +N: the full sidebar, its Exited open. */
function openExited() {
  folds.unfold('exited')
  sidebar.expand()
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col items-center gap-2" data-rail>
    <div v-if="needsShown || rail.news" class="flex items-center gap-1" data-rail-counts>
      <UTooltip v-if="needsShown" :text="`${needsShown} ${needsShown === '1' ? 'needs' : 'need'} you`" :content="{ side: 'right' }">
        <span class="rounded-full bg-warning px-1.5 text-[10px] font-semibold leading-4 text-(--ui-color-neutral-900)" data-rail-count="needs" data-count-of="needs">{{ needsShown }}</span>
      </UTooltip>
      <UTooltip v-if="rail.news" :text="`${rail.news} with new events or unread chat`" :content="{ side: 'right' }">
        <span class="rounded-full bg-default px-1.5 text-[10px] font-semibold leading-4 text-muted" data-rail-count="news">{{ rail.news }}</span>
      </UTooltip>
    </div>
    <UTooltip text="Launch agent" :kbds="['N']" :content="{ side: 'right' }">
      <UButton icon="i-lucide-plus" size="sm" aria-label="Launch agent" @click="launch.show()" />
    </UTooltip>
    <UTooltip text="Filter sessions" :kbds="['/']" :content="{ side: 'right' }">
      <UButton icon="i-lucide-search" color="neutral" variant="ghost" size="sm" class="hover:bg-canvas-hover active:bg-canvas-hover" aria-label="Filter sessions" @click="emit('search')" />
    </UTooltip>
    <div class="flex min-h-0 w-full flex-1 flex-col items-center gap-1.5 overflow-y-auto py-0.5" data-rail-sessions>
      <template v-for="it in rail.items" :key="it.id">
        <UTooltip v-if="it.shape === 'capsule'" :text="it.label" :content="{ side: 'right' }">
          <div
            class="flex w-full flex-col items-center gap-1 rounded-full border px-0.5 py-1"
            :class="it.dashed ? 'border-dashed border-accented' : 'border-default bg-default'"
            :data-rail-run="it.id.slice(4)"
            :data-rail-dashed="it.dashed ? '' : undefined"
          >
            <NuxtLink :to="it.to" class="relative grid size-5 place-items-center rounded-full hover:bg-canvas-hover" :aria-label="it.label" :aria-current="$route.path === it.to && typeof $route.query.session !== 'string' ? 'page' : undefined">
              <UIcon name="i-lucide-play" class="size-3.5" :class="it.amber && needsDot ? 'text-warning' : 'text-muted'" :data-rail-play="it.amber && needsDot ? 'needs' : undefined" />
              <!-- The run's state: its busiest member's (blockStatus). -->
              <StatusMark v-if="it.status" :status="it.status" :quiet="!needsDot" badge class="absolute -right-1 -top-1" />
              <!-- The run's own unread chat (design 2g), bottom right of the play icon. -->
              <span v-if="it.chat" class="absolute -bottom-1 -right-1.5 grid min-w-3.5 place-items-center rounded-full bg-default px-0.5 text-[8px] font-semibold leading-3.5 text-highlighted ring-2 ring-default" :data-rail-news="it.chat" aria-hidden="true">{{ it.chat }}</span>
            </NuxtLink>
            <SidebarRailSquare v-for="m in it.members" :key="m.id" :shape="m" :needs-dot="needsDot" member />
          </div>
        </UTooltip>
        <SidebarRailSquare v-else :shape="it" :needs-dot="needsDot" />
      </template>
      <UTooltip v-if="rail.exitedFolded" :text="`${rail.exitedFolded} exited · open the sidebar on them`" :content="{ side: 'right' }">
        <button type="button" class="grid size-6 place-items-center rounded-md border border-dashed border-accented font-mono text-[10px] text-muted hover:bg-canvas-hover" :aria-label="`${rail.exitedFolded} exited, open the sidebar on them`" data-rail-exited @click="openExited">+{{ rail.exitedFolded }}</button>
      </UTooltip>
    </div>
  </div>
</template>
