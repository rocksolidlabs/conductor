<script setup lang="ts">
/**
 * The phone shell's first screen (docs/plans/phone-shell.md, OSS-1b): with no provider, Open a server, the whole screen; with one,
 * its sign-in while it reports this phone signed out, and once signed in what waits, with Settings a tap away.
 */
definePageMeta({ layout: 'shell' })
useHead({ title: 'Conductor' })
const shell = useShell()
const route = useRoute()
const initial = computed(() => (typeof route.query.address === 'string' ? route.query.address : ''))
const s = shell.state
</script>

<template>
  <template v-if="!shell.provider">
    <ShellHeader title="Open a server" line="A Conductor server's own pages, on your network or through a switchyard, open in the in-app browser." />
    <ShellOpenServer v-if="shell.bridge" :bridge="shell.bridge" :initial="initial" />
  </template>
  <template v-else-if="!s">
    <ShellHeader :title="shell.name" />
    <p class="flex items-center gap-2 text-sm text-muted" role="status" data-shell-checking><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> {{ shell.stateWords.value || 'Checking whether this phone is signed in…' }}</p>
    <ULink to="/shell/server" class="inline-flex min-h-11 items-center gap-2 self-start text-sm text-primary" data-shell-to-server><UIcon name="i-lucide-server" class="size-4" /> Open a server</ULink>
  </template>
  <ShellSignIn v-else-if="!s.signedIn" />
  <template v-else>
    <ShellHeader title="Waiting for you" :line="s.who ? `Signed in as ${s.who}` : undefined">
      <IconButton label="Settings" icon="i-lucide-settings" color="neutral" variant="ghost" class="min-h-11 min-w-11" to="/shell/settings" data-shell-to-settings />
    </ShellHeader>
    <p v-if="s.words" class="break-words text-sm text-warning" role="status" data-shell-state-words>{{ s.words }}</p>
    <p class="text-sm text-muted" data-shell-signed-in>Signed in. What waits for you is listed here.</p>
  </template>
</template>
