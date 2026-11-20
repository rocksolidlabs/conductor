<script setup lang="ts">
// The phone shell's layout (docs/plans/phone-shell.md, OSS-1b): one column the width of a phone, the page's text at the system's
// text size (the root's font is the system's body style where the platform has one, so every rem follows it), and nothing of the
// workbench's layout.
useHead({ htmlAttrs: { class: 'conductor-shell' } })
useShellRoutes()
</script>

<template>
  <div class="flex min-h-dvh flex-col bg-default text-default" data-shell>
    <main class="mx-auto flex w-full max-w-xl min-w-0 flex-1 flex-col gap-4 px-4 pt-[max(1.5rem,env(safe-area-inset-top))] pb-[max(1.5rem,env(safe-area-inset-bottom))]">
      <slot />
    </main>
  </div>
</template>

<style>
html.conductor-shell {
  font: -apple-system-body;
}
</style>
