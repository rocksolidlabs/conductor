<script setup lang="ts">
import type { SessionInfo } from '~/composables/useSessions'
import type { TerminalTransport } from '~/utils/transport/types'
import { isEnded } from '~/utils/attention'
import { agentInitials } from '~/utils/sessions'
import { sessionStatus } from '~/utils/status'
import { TILE_FONT_SIZE, TILE_SCROLLBACK } from '~/utils/tile'

/**
 * A session as a live tile: its terminal fits the tile and takes keys in place (a click on it focuses it); the full view is one action
 * away: the open button, a double-click on the header, or Enter or Space on the tile's frame when the frame itself has the focus.
 */
const props = defineProps<{
  session: SessionInfo
  createTransport: () => TerminalTransport
}>()

const emit = defineEmits<{ select: [] }>()

const events = useEvents()
const now = useNow()
// The tile's state (sessionStatus): its mark and word in the header; a tile that asks has the amber border. An ended session may still
// carry the prompt it was waiting on: it waits no more (as in the sidebar).
const status = computed(() => sessionStatus(props.session, now.value))
const needsInput = computed(() => status.value.key === 'needs')
const host = computed(() => (props.session.kind === 'hosted' ? `hosted · ${props.session.hostName || 'dev machine'}` : 'server'))
</script>

<template>
  <!-- The page's own control (the run page's selection box) sits beside the tile, not in it: one control per element. -->
  <div class="relative h-full min-h-0 min-w-0">
    <div v-if="$slots.leading" class="absolute left-[11px] top-2 z-10 flex">
      <slot name="leading" />
    </div>
    <div
      class="group flex h-full min-h-0 flex-col overflow-hidden rounded-lg border bg-elevated/40 transition-colors outline-none focus-visible:ring-2 focus-visible:ring-primary"
      :class="needsInput ? 'border-warning ring-2 ring-warning/60' : 'border-default hover:border-accented focus-within:border-accented'"
      role="group"
      tabindex="0"
      data-session-tile
      :data-session-id="props.session.id"
      :aria-label="`${props.session.name}: a live terminal. Enter opens it in full.`"
      @keydown.enter.self.prevent="emit('select')"
      @keydown.space.self.prevent="emit('select')"
    >
      <div class="flex cursor-pointer items-center gap-2 border-b border-default px-2.5 py-1 text-xs shrink-0 select-none" :class="$slots.leading && 'pl-8'" data-tile-header @dblclick="emit('select')">
        <span class="font-mono text-[10px] font-semibold text-muted">{{ agentInitials(props.session.agentId) }}</span>
        <span class="font-semibold truncate flex-1 text-[13px]">{{ props.session.name }}</span>
        <EventMarkBadge :session-id="props.session.id" :finished="status.key === 'finished'" />
        <YoloBadge v-if="props.session.yolo" icon />
        <!-- The amber dot follows the Events page's Badge route for needs_input; the word stays. -->
        <StatusMark :status="status" word :quiet="!events.routes.value.needs_input.badge" class="text-[11.5px]" />
        <ResumeButton v-if="isEnded(props.session.status)" :session="props.session" stay icon-only size="xs" />
        <UTooltip text="Open in full (or double-click here)">
          <UButton icon="i-lucide-maximize-2" size="xs" color="neutral" variant="ghost" :aria-label="`Open ${props.session.name}`" data-tile-open @click.stop="emit('select')" />
        </UTooltip>
      </div>
      <div class="flex-1 min-h-0" data-tile-terminal>
        <TerminalView :create-transport="props.createTransport" fit="tile" compact :auto-focus="false" :font-size="TILE_FONT_SIZE" :scrollback="TILE_SCROLLBACK" />
      </div>
      <div class="flex items-center gap-2 border-t border-default px-2.5 py-1 font-mono text-[11px] text-muted shrink-0">
        <slot name="footer">
          <span class="truncate">{{ host }}</span>
          <span class="ml-auto flex-none">{{ props.session.viewers ? `${props.session.viewers} here` : '—' }}</span>
        </slot>
      </div>
    </div>
  </div>
</template>
