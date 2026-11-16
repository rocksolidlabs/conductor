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
// The tile's state (sessionStatus): its mark and word at the foot, the header kept for the name (the UX review's P10); a tile that asks
// has the amber border. An ended session may still carry the prompt it was waiting on: it waits no more (as in the sidebar).
const status = computed(() => sessionStatus(props.session, now.value))
const needsInput = computed(() => status.value.key === 'needs')
// The host, short: "server" or the host's own name, "hosted · <host>" in its title. It is the part of the foot that gives way.
const hostName = computed(() => props.session.hostName || 'dev machine')
const host = computed(() => (props.session.kind === 'hosted' ? hostName.value : 'server'))
const hostTitle = computed(() => (props.session.kind === 'hosted' ? `hosted · ${hostName.value}` : 'server'))
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
        <span class="flex-none font-mono text-[10px] font-semibold text-muted">{{ agentInitials(props.session.agentId) }}</span>
        <span class="min-w-0 flex-1 truncate text-[13px] font-semibold" :title="props.session.name" data-tile-name>{{ props.session.name }}</span>
        <EventMarkBadge :session-id="props.session.id" :finished="status.key === 'finished'" icon />
        <YoloBadge v-if="props.session.yolo" icon />
        <ResumeButton v-if="isEnded(props.session.status)" :session="props.session" stay icon-only size="xs" />
        <UTooltip text="Open in full (or double-click here)">
          <UButton icon="i-lucide-maximize-2" size="xs" color="neutral" variant="ghost" :aria-label="`Open ${props.session.name}`" data-tile-open @click.stop="emit('select')" />
        </UTooltip>
      </div>
      <div class="flex-1 min-h-0" data-tile-terminal>
        <TerminalView :create-transport="props.createTransport" fit="tile" compact :auto-focus="false" :font-size="TILE_FONT_SIZE" :scrollback="TILE_SCROLLBACK" />
      </div>
      <!-- The foot: the state's cell first, outside the slot, so a run page's tiles keep their branch and diff after it. -->
      <div class="flex min-w-0 items-center gap-2 border-t border-default px-2.5 py-1 font-mono text-[11px] text-muted shrink-0" data-tile-foot>
        <!-- The amber dot follows the Events page's Badge route for needs_input; the word stays. -->
        <span class="flex flex-none items-center font-sans text-[11.5px]" data-tile-state>
          <StatusMark :status="status" word :quiet="!events.routes.value.needs_input.badge" />
        </span>
        <slot name="footer">
          <span class="min-w-0 truncate" :title="hostTitle" data-tile-host>{{ host }}</span>
          <span class="ml-auto flex-none" data-tile-viewers>{{ props.session.viewers ? `${props.session.viewers} here` : '—' }}</span>
        </slot>
      </div>
    </div>
  </div>
</template>
