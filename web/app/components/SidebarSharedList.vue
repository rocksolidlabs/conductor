<script setup lang="ts">
import { joinedOpen, joinedPath, type JoinedEntry } from '~/utils/joined'
import { joinedStatus } from '~/utils/status'
import { agentInitials } from '~/utils/sessions'

/**
 * "Shared with you": the share links joined from this workbench, so that a session someone shared stays beside your own instead of
 * taking the window over. Each opens its join page (which joins at once); × forgets it. The section's header is the list's
 * (SidebarSection), below your own sessions; this is its rows.
 */
withDefaults(defineProps<{ focusedId?: string | null }>(), { focusedId: null })
const joined = useJoined()
const route = useRoute()
// Forget shows on hover where the pointer hovers; with none to hover (a touch screen) it always shows, and the link stops short of
// it. Hidden, it takes no tap or click: a tap on the row opens the link, never forgets it unseen.
const hovers = useMedia('(hover: hover)')

// The mark is what the entry knows (joinedStatus): running, starting or exited; a link that does not answer draws none and says so.
// Revoked and gone are standing facts, muted; a link that does not answer is a read that failed, amber (the UX review's P16).
function line(e: JoinedEntry): { text: string; warn: boolean } {
  if (e.lastStatus === 'revoked') return { text: 'revoked', warn: false }
  if (e.lastStatus === 'gone') return { text: 'gone', warn: false }
  if (e.lastStatus === 'unreachable') return { text: 'not answering', warn: true }
  return { text: `through ${e.host}${e.hostedOn ? ` · on ${e.hostedOn}` : ''}`, warn: false }
}
function title(e: JoinedEntry): string {
  return e.lastStatus === 'unreachable' ? `${e.host} did not answer, or does not allow this origin` : `${e.name} · ${e.role === 'control' ? 'control' : 'view only'}`
}
</script>

<template>
  <section v-if="joined.list.value.length" class="flex flex-col gap-0.5" data-sidebar-shared>
    <div
      v-for="e in joined.list.value"
      :key="e.id"
      class="group relative flex items-center rounded-md"
      :class="[focusedId === `j:${e.id}` && 'ring-2 ring-primary/40']"
      :data-row-focused="focusedId === `j:${e.id}` ? '' : undefined"
      :data-shared-entry="e.id"
      :data-shared-status="e.lastStatus ?? ''"
      :data-sidebar-row="`j:${e.id}`"
      data-row-kind="shared"
    >
      <NuxtLink
        :to="joinedPath(e)"
        class="flex min-w-0 flex-1 flex-col gap-0.5 rounded-md border px-2.5 py-2 transition-colors"
        :class="[joinedOpen(route.path, e.token) ? 'bg-default border-default shadow-xs' : 'border-transparent hover:bg-canvas-hover', !hovers && 'pr-10']"
        :aria-current="joinedOpen(route.path, e.token) ? 'page' : undefined"
        :title="title(e)"
      >
        <!-- As a session's row (SidebarSessionRow): the name on one line across the row, the mark at its end; the agent's initials (a
             crew's icon for a run) as a chip at the start of the line below. -->
        <span class="flex min-w-0 items-center gap-1.5">
          <span class="truncate text-sm font-medium" :class="e.lastStatus === 'revoked' || e.lastStatus === 'gone' ? 'text-muted' : 'text-highlighted'" data-shared-name>{{ e.name }}</span>
          <UIcon :name="e.role === 'control' ? 'i-lucide-keyboard' : 'i-lucide-eye'" class="size-3.5 flex-none text-muted" :aria-label="e.role === 'control' ? 'control' : 'view only'" />
          <StatusMark v-if="joinedStatus(e)" :status="joinedStatus(e)!" class="ml-auto" />
        </span>
        <span class="flex min-w-0 items-center gap-1.5">
          <span
            v-if="e.kind === 'session'"
            class="flex-none rounded px-1 font-mono text-[10px] leading-4 font-semibold select-none"
            :class="e.lastStatus === 'revoked' || e.lastStatus === 'gone' ? 'border border-dashed border-accented text-muted' : 'bg-default text-toned'"
            :title="e.agentId"
            data-row-initials
          >{{ agentInitials(e.agentId ?? '') }}</span>
          <span v-else class="grid h-4 flex-none place-items-center rounded bg-default px-1 text-toned" data-row-initials><UIcon name="i-lucide-users" class="size-3" /></span>
          <span class="min-w-0 truncate text-[11px]" :class="line(e).warn ? 'text-warning' : 'text-muted'" data-shared-line>{{ line(e).text }}</span>
        </span>
      </NuxtLink>
      <!-- Forget shows over the row's corner on hover or focus, as a session row's actions do, so the name has the row's width (#906);
           on a touch screen it always shows (hovers). -->
      <span
        class="absolute right-1.5 top-1.5 rounded-md bg-default/95 p-0.5 shadow-xs ring ring-default transition-opacity"
        :class="hovers && 'pointer-events-none opacity-0 focus-within:pointer-events-auto focus-within:opacity-100 group-hover:pointer-events-auto group-hover:opacity-100'"
        data-shared-forget-bar
      >
        <UTooltip :text="`Forget ${e.name}`">
          <UButton icon="i-lucide-x" color="neutral" variant="ghost" size="xs" :aria-label="`Forget ${e.name}`" data-shared-forget @click="joined.forget(e.id)" />
        </UTooltip>
      </span>
    </div>
  </section>
</template>
