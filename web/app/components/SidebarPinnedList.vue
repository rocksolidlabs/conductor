<script setup lang="ts">
import { agentInitials } from '~/utils/sessions'
import { sessionOpen } from '~/utils/sidebar'
import { sessionStatus } from '~/utils/status'
import { unpinSession, type PinRow } from '~/utils/pins'

/**
 * "Pinned": the sessions pinned to the top from a row's menu (utils/pins.ts), first in the list; each is also in its own section.
 * A pinned session no longer listed is drawn greyed, its name and agent as they were when pinned, until it is let go a week after
 * its pinning. Unpin shows on hover where the pointer hovers, and always where it cannot. The section's header is the list's
 * (SidebarSection); this is its rows.
 */
const props = defineProps<{ rows: PinRow[]; now: number }>()
const ui = useUiState()
const route = useRoute()
const hovers = useMedia('(hover: hover)')

function unpin(id: string) {
  ui.setPins(unpinSession(ui.pins.value, id))
}
const rows = computed(() => props.rows)
</script>

<template>
  <ol class="flex flex-col gap-0.5" data-sidebar-pinned>
    <li v-for="r in rows" :key="r.pin.session" class="group relative flex items-center rounded-md" :data-pinned-session="r.pin.session" :data-pinned-gone="r.gone ? '' : undefined">
      <NuxtLink
        v-if="!r.gone"
        :to="`/sessions/${r.pin.session}`"
        class="flex min-w-0 flex-1 items-center gap-1.5 rounded-md border px-2.5 py-1.5 transition-colors"
        :class="[sessionOpen(route.path, r.pin.session) ? 'bg-default border-default shadow-xs' : 'border-transparent hover:bg-canvas-hover', !hovers && 'pr-10']"
        :aria-current="sessionOpen(route.path, r.pin.session) ? 'page' : undefined"
      >
        <UBadge :label="agentInitials(r.session!.agentId)" color="neutral" variant="soft" size="sm" class="flex-none" />
        <span class="truncate text-sm font-medium text-highlighted" data-pinned-name>{{ r.session!.name }}</span>
        <StatusMark :status="sessionStatus(r.session!, now)" class="ml-auto" />
      </NuxtLink>
      <span v-else class="flex min-w-0 flex-1 items-center gap-1.5 rounded-md border border-transparent px-2.5 py-1.5 text-dimmed" :class="!hovers && 'pr-10'" title="No longer listed: let go a week after it was pinned">
        <UBadge :label="agentInitials(r.pin.agent)" color="neutral" variant="outline" size="sm" class="flex-none" />
        <span class="truncate text-sm" data-pinned-name>{{ r.pin.name || r.pin.session }}</span>
        <span class="ml-auto flex-none text-xs">not listed</span>
      </span>
      <IconButton
        icon="i-lucide-pin-off"
        color="neutral"
        variant="ghost"
        size="xs"
        class="absolute right-1"
        :class="hovers && 'opacity-0 group-hover:opacity-100 focus-visible:opacity-100'"
        :label="`Unpin ${r.session?.name ?? r.pin.name}`"
        data-pinned-unpin
        @click="unpin(r.pin.session)"
      />
    </li>
  </ol>
</template>
