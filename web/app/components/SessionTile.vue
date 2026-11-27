<script setup lang="ts">
import type { SessionInfo } from '~/composables/useSessions'
import type { TerminalTransport } from '~/utils/transport/types'
import { isEnded } from '~/utils/attention'
import { agentInitials } from '~/utils/sessions'
import { sessionStatus, type MarkView } from '~/utils/status'
import { TILE_FONT_SIZE, TILE_SCROLLBACK } from '~/utils/tile'
import { tileAsk } from '~/utils/wall'

/**
 * A session as a live tile: its terminal fits the tile and takes keys in place (a click on it focuses it); the full view is one action
 * away: the open button, a double-click on the header, or Enter or Space on the tile's frame when the frame itself has the focus.
 */
const props = defineProps<{
  session: SessionInfo
  createTransport: () => TerminalTransport
  /** The state the foot says, where the page says it in words of its own (a run's member: the Crews pages' vocabulary); else the session's. */
  status?: MarkView
  /** The tile's member will get the next line from its run's message bar: its frame carries the primary ring. */
  receiving?: boolean
  /** Resume (or Relaunch) is drawn with its word, not as an icon alone. */
  resumeWord?: boolean
}>()

const emit = defineEmits<{ select: [] }>()

const events = useEvents()
const now = useNow()
// The tile's state (sessionStatus): its mark and word at the foot, the header kept for the name (the UX review's P10); a tile that asks
// has the amber border. An ended session may still carry the prompt it was waiting on: it waits no more (as in the sidebar).
const own = computed(() => sessionStatus(props.session, now.value))
const status = computed(() => props.status ?? own.value)
const needsInput = computed(() => own.value.key === 'needs')
// The host, short: "server" or the host's own name, "hosted · <host>" in its title. It is the part of the foot that gives way.
const hostName = computed(() => props.session.hostName || 'dev machine')
const host = computed(() => (props.session.kind === 'hosted' ? hostName.value : 'server'))
const hostTitle = computed(() => (props.session.kind === 'hosted' ? `hosted · ${hostName.value}` : 'server'))
// A tile that asks says the start of its question where the host was; the whole question is its title and joins the tile's name.
const ask = computed(() => (needsInput.value ? tileAsk(props.session) : null))
const label = computed(() => (ask.value ? `${props.session.name} asks: ${ask.value} Enter opens it in full.` : `${props.session.name}: a live terminal. Enter opens it in full.`))
</script>

<template>
  <!-- The page's own control (the run page's selection box) sits beside the tile, not in it: one control per element. -->
  <div class="relative h-full min-h-0 min-w-0">
    <div v-if="$slots.leading" class="absolute left-[11px] top-2 z-10 flex">
      <slot name="leading" />
    </div>
    <div
      class="group flex h-full min-h-0 flex-col overflow-hidden rounded-lg border bg-elevated/40 transition-colors outline-none focus-visible:ring-2 focus-visible:ring-primary"
      :class="needsInput ? 'border-warning ring-2 ring-warning/60' : [props.receiving && 'ring-2 ring-primary', 'border-default hover:border-accented focus-within:border-accented']"
      :data-receiving="props.receiving || undefined"
      role="group"
      tabindex="0"
      data-session-tile
      :data-session-id="props.session.id"
      :aria-label="label"
      @keydown.enter.self.prevent="emit('select')"
      @keydown.space.self.prevent="emit('select')"
    >
      <div class="flex cursor-pointer items-center gap-2 border-b border-default px-2.5 py-1 text-xs shrink-0 select-none" :class="$slots.leading && 'pl-8'" data-tile-header @dblclick="emit('select')">
        <span class="flex-none font-mono text-[10px] font-semibold text-muted">{{ agentInitials(props.session.agentId) }}</span>
        <span class="min-w-0 flex-1 truncate text-[13px] font-semibold" :title="props.session.name" data-tile-name>{{ props.session.name }}</span>
        <EventMarkBadge :session-id="props.session.id" :finished="own.key === 'finished'" icon />
        <YoloBadge v-if="props.session.yolo" icon />
        <ResumeButton v-if="isEnded(props.session.status)" :session="props.session" stay :icon-only="!props.resumeWord" size="xs" />
        <!-- One more button of the page's own (a run's tile: show it large), before the open button. -->
        <slot name="header-action" />
        <UTooltip text="Full size (or double-click here)">
          <UButton icon="i-lucide-maximize-2" size="xs" color="neutral" variant="ghost" :aria-label="`Open ${props.session.name}`" data-tile-open @click.stop="emit('select')" />
        </UTooltip>
      </div>
      <div class="flex-1 min-h-0" data-tile-terminal>
        <TerminalView :create-transport="props.createTransport" fit="tile" compact :auto-focus="false" :font-size="TILE_FONT_SIZE" :scrollback="TILE_SCROLLBACK" />
      </div>
      <!-- The page's own answer to the tile's question (a run's tile: its choices as buttons), between the terminal and the foot. -->
      <div v-if="$slots.ask" class="shrink-0 border-t border-default px-2.5 py-1.5" data-tile-ask-bar>
        <slot name="ask" />
      </div>
      <!-- The foot: the state's cell first, outside the slot, so a run page's tiles keep their branch and diff after it. -->
      <div class="flex min-w-0 items-center gap-2 border-t border-default px-2.5 py-1 text-[11px] text-muted shrink-0" data-tile-foot>
        <!-- The amber dot follows the Alerts rules' Badge route for needs_input; the word stays. -->
        <!-- The host gives way first, then the question down to its first letters, then the state's word (cut with "…", the whole
             in the mark's title): a phone's tile 114 px wide keeps the question's start and its viewers in view. -->
        <span class="flex min-w-0 shrink items-center text-[11.5px]" data-tile-state>
          <StatusMark :status="status" word :quiet="!events.routes.value.needs_input.badge" class="shrink" />
        </span>
        <slot name="footer">
          <span v-if="ask" class="min-w-8 shrink-[100] truncate text-[11.5px] text-default" :title="ask" data-tile-ask>{{ ask }}</span>
          <span v-else class="min-w-0 shrink-[100] truncate" :title="hostTitle" data-tile-host>{{ host }}</span>
          <span class="ml-auto flex-none tabular-nums" data-tile-viewers>{{ props.session.viewers ? `${props.session.viewers} here` : '—' }}</span>
        </slot>
      </div>
    </div>
  </div>
</template>
