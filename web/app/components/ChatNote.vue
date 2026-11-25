<script setup lang="ts">
/**
 * The note shown once, the first time a person presses Chat in this browser (docs/plans/chat-or-terminal.md, design 11): the agent's
 * own terms decide how its non-interactive mode may be used and counted, with a link to its site; under it, that in Chat the agent
 * has Conductor's tools only. Drawn in the chat, under the move's notice, so that it covers nothing; dismissed with its close.
 */
defineProps<{ site?: string; host?: string }>()
const emit = defineEmits<{ dismiss: [] }>()
</script>

<template>
  <aside class="flex items-start gap-3 rounded-lg border border-default bg-elevated/60 px-3.5 py-3 text-sm" data-chat-terms>
    <UIcon name="i-lucide-info" class="mt-0.5 size-4 flex-none text-muted" aria-hidden="true" />
    <div class="flex min-w-0 flex-1 flex-col gap-1">
      <p class="text-default" data-chat-terms-first>Chat runs the agent's own non-interactive mode with its existing sign-in; the agent's terms decide how that may be used and counted.</p>
      <ULink v-if="site && host" :to="site" target="_blank" rel="noopener noreferrer" class="w-fit text-xs text-primary underline underline-offset-2" data-chat-terms-site>{{ host }}</ULink>
      <p class="text-muted" data-chat-terms-second>In Chat the agent has Conductor's tools only; your own tool servers stay in its terminal.</p>
    </div>
    <IconButton icon="i-lucide-x" size="xs" color="neutral" variant="ghost" label="Close the note" data-chat-terms-close @click="emit('dismiss')" />
  </aside>
</template>
