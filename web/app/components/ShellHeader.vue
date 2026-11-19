<script setup lang="ts">
/** A phone shell screen's head: the mark, the screen's title, and a line under it; `back` draws a way back to the shell's first screen. */
defineProps<{ title: string; line?: string; back?: boolean }>()
</script>

<template>
  <header class="flex min-w-0 items-center gap-3" data-shell-header>
    <ULink v-if="back" to="/shell" class="inline-flex min-h-11 min-w-11 flex-none items-center justify-center rounded-md text-muted hover:bg-elevated" aria-label="Back" data-shell-back>
      <UIcon name="i-lucide-chevron-left" class="size-6" />
    </ULink>
    <img v-else src="/brand/conductor-mark.svg" alt="" class="size-8 flex-none dark:hidden" />
    <img v-if="!back" src="/brand/conductor-mark-reversed.svg" alt="" class="hidden size-8 flex-none dark:block" />
    <div class="min-w-0 flex-1">
      <h1 class="break-words text-xl font-semibold tracking-tight" data-shell-title>{{ title }}</h1>
      <p v-if="line" class="break-words text-sm text-muted" data-shell-line>{{ line }}</p>
    </div>
    <slot />
  </header>
</template>
