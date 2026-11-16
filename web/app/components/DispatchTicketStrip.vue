<script setup lang="ts">
import type { TicketSection } from '~/utils/ticketSections'

/**
 * A ticket's strip of sections (UX1 PR-16): under the title, staying at the top of the page as it scrolls, each section by name with its
 * state where it has one (ticketSections). A press brings that section up under the strip at once and gives it the focus; never by a
 * hash, so the router does not scroll the window. On a phone it is one line that scrolls sideways inside itself.
 */
defineProps<{ sections: readonly TicketSection[] }>()

/** Brings the section up under the strip (its scroll margin) and gives it the focus, which it takes by its tabindex of -1. */
function go(id: string) {
  const el = document.getElementById(`ticket-${id}`)
  if (!el) return
  el.scrollIntoView({ block: 'start' })
  el.focus({ preventScroll: true })
}
</script>

<template>
  <nav aria-label="The ticket's sections" class="sticky top-0 z-10 -mx-4 flex h-10 flex-none items-center gap-0.5 overflow-x-auto border-b border-default bg-default px-3 [scrollbar-width:none] sm:-mx-6 sm:px-5" data-ticket-strip>
    <button
      v-for="s in sections"
      :key="s.id"
      type="button"
      class="flex h-7 flex-none items-center gap-1.5 rounded-md px-2 text-xs whitespace-nowrap text-muted hover:bg-elevated hover:text-highlighted focus-visible:outline-2 focus-visible:outline-primary"
      :data-strip-item="s.id"
      :data-tone="s.tone"
      :data-count="s.count"
      @click="go(s.id)"
    >
      <span class="font-medium text-default">{{ s.label }}</span>
      <span v-if="s.words" :class="s.tone === 'warning' ? 'text-warning' : 'text-dimmed'" data-strip-words>{{ s.words }}</span>
    </button>
  </nav>
</template>
