<script setup lang="ts">
import type { JoinRunMember } from '~/composables/useSessions'
import type { Attention, Role } from '~/utils/protocol'
import type { TerminalTransport } from '~/utils/transport/types'
import { joinTileStatus } from '~/utils/crews'
import { agentInitials } from '~/utils/sessions'
import { sessionStatus } from '~/utils/status'
import { TILE_FONT_SIZE, TILE_SCROLLBACK } from '~/utils/tile'

/**
 * The member tiles of a run link: a live terminal for each member that runs, a placeholder for the rest. With a control link a tile
 * fits its pane and takes keys in place, as on the wall; with a view link it shows the member's screen scaled, at the member's size, and
 * asks for none. Opening one in full is the page's to do. What the tiles heard is the page's too (v-model:heard), so it outlasts the grid
 * while a member is open in full.
 */
const props = defineProps<{
  members: JoinRunMember[]
  role: Role
  transportFor: (m: JoinRunMember) => () => TerminalTransport
  agentName: (agentId: string) => string
}>()
const emit = defineEmits<{ open: [member: JoinRunMember, heard: { status?: string; attention?: string } | undefined] }>()

/** What each member's tile last heard from its session, by member name. */
const heard = defineModel<Record<string, { status?: string; attention?: string }>>('heard', { required: true })
// The map as last set here or by the page. heard.value shows the page's copy only once the page has rendered again, so a second report
// before that (two tiles, or a status and an attention, in one task) builds on this one and does not drop the first.
let latest = heard.value
watch(heard, (v) => (latest = v), { flush: 'sync' })
function hear(name: string, patch: { status?: string; attention?: string }) {
  latest = { ...latest, [name]: { ...latest[name], ...patch } }
  heard.value = latest
}
function open(m: JoinRunMember) {
  if (m.sessionId) emit('open', m, latest[m.name])
}
/** A member that asks (sessionStatus's needs): its tile keeps the amber border. */
function asks(m: JoinRunMember): boolean {
  const h = heard.value[m.name]
  return sessionStatus({ status: h?.status ?? m.status, attention: h?.attention ? { state: h.attention as Attention['state'] } : undefined }).key === 'needs'
}
/** A control link types into the tiles; a view link only watches them. */
const interactive = computed(() => props.role === 'control')
</script>

<template>
  <div class="grid auto-rows-[18rem] gap-3 sm:grid-cols-2 xl:grid-cols-3" data-join-tiles>
    <template v-for="m in members" :key="m.name">
      <div
        v-if="m.sessionId"
        class="flex min-h-0 flex-col overflow-hidden rounded-lg border bg-elevated/40 outline-none transition-colors focus-visible:ring-2 focus-visible:ring-primary"
        :class="[asks(m) ? 'border-warning ring-2 ring-warning/60' : 'border-default hover:border-accented', !interactive && 'cursor-pointer']"
        :role="interactive ? 'group' : 'button'"
        tabindex="0"
        :aria-label="interactive ? `${m.name}: a live terminal. Enter opens it in full.` : `Open ${m.name}`"
        :data-member="m.name"
        @click="!interactive && open(m)"
        @keydown.enter.self.prevent="open(m)"
        @keydown.space.self.prevent="open(m)"
      >
        <div class="flex flex-none cursor-pointer select-none items-center gap-2 border-b border-default px-2.5 py-1 text-xs" @dblclick="open(m)">
          <span class="font-mono text-[10px] font-semibold text-muted">{{ agentInitials(m.agentId) }}</span>
          <span class="flex-1 truncate text-[13px] font-semibold">{{ m.name }}</span>
          <span class="flex items-center gap-1.5 text-[11.5px]" :class="joinTileStatus(m, heard[m.name]).cls"><span class="size-[7px] rounded-full" :class="joinTileStatus(m, heard[m.name]).dot" aria-hidden="true" />{{ joinTileStatus(m, heard[m.name]).label }}</span>
          <UButton icon="i-lucide-maximize-2" size="xs" color="neutral" variant="ghost" :aria-label="`Open ${m.name}`" data-tile-open @click.stop="open(m)" />
        </div>
        <div class="min-h-0 flex-1" :class="!interactive && 'pointer-events-none'">
          <TerminalView
            :create-transport="props.transportFor(m)"
            :read-only="!interactive"
            :fit="interactive ? 'tile' : 'scale'"
            compact
            :auto-focus="false"
            :font-size="interactive ? TILE_FONT_SIZE : 13"
            :scrollback="interactive ? TILE_SCROLLBACK : 5000"
            @status="(s) => hear(m.name, { status: s })"
            @attention="(a) => hear(m.name, { attention: a.state })"
          />
        </div>
        <div class="flex flex-none items-center gap-2 border-t border-default px-2.5 py-1 font-mono text-[11px] text-muted">
          <span class="truncate">{{ props.agentName(m.agentId) }}</span>
          <span class="ml-auto flex-none">{{ interactive ? 'type here · double-click to open' : 'open' }}</span>
        </div>
      </div>
      <div v-else class="flex min-h-0 flex-col overflow-hidden rounded-lg border border-dashed border-accented" :data-member="m.name">
        <div class="flex items-center gap-2 border-b border-default px-2.5 py-1.5 text-xs">
          <span class="font-mono text-[10px] font-semibold text-muted">{{ agentInitials(m.agentId) }}</span>
          <span class="flex-1 truncate text-[13px] font-semibold">{{ m.name }}</span>
        </div>
        <div class="flex flex-1 items-center justify-center p-3 text-sm text-muted">{{ m.status === 'ended' ? 'ended' : m.status === 'starting' ? 'starting…' : 'not started yet' }}</div>
      </div>
    </template>
  </div>
</template>
