<script setup lang="ts">
import { clockTime } from '~/utils/brakemanRole'
import { routeWords } from '~/utils/shell'

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
const retrying = ref(false)
/** Reads the provider's state again after a read that failed or did not answer. */
async function again() {
  retrying.value = true
  try {
    await shell.refresh()
  } finally {
    retrying.value = false
  }
}
const note = computed(() => (typeof route.query.note === 'string' ? routeWords(route.query.note, shell.name, shell.lastInbox.value?.asOf ?? null, (t) => clockTime(t)) : ''))
</script>

<template>
  <template v-if="!shell.provider">
    <ShellHeader title="Open a server" line="A Conductor server's own pages, on your network or through a switchyard, open in the in-app browser." />
    <ShellOpenServer v-if="shell.bridge" :bridge="shell.bridge" :initial="initial" />
  </template>
  <template v-else-if="!s">
    <ShellHeader :title="shell.name" />
    <p v-if="!shell.stateWords.value" class="flex items-center gap-2 text-sm text-muted" role="status" data-shell-checking><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Checking whether this phone is signed in…</p>
    <template v-else>
      <p class="break-words text-sm text-warning" role="status" data-shell-state-failed>{{ shell.stateWords.value }}</p>
      <UButton label="Try again" icon="i-lucide-refresh-cw" color="neutral" variant="outline" class="min-h-11 max-w-full self-start" :ui="{ label: 'min-w-0 whitespace-normal break-words text-start' }" :loading="retrying" data-shell-state-again @click="again" />
    </template>
    <ULink to="/shell/server" class="inline-flex min-h-11 items-center gap-2 self-start text-sm text-primary" data-shell-to-server><UIcon name="i-lucide-server" class="size-4" /> Open a server</ULink>
  </template>
  <ShellSignIn v-else-if="!s.signedIn" />
  <template v-else>
    <ShellHeader title="Waiting for you" :line="s.who ? `Signed in as ${s.who}` : undefined">
      <IconButton label="Settings" icon="i-lucide-settings" color="neutral" variant="ghost" class="min-h-11 min-w-11" to="/shell/settings" data-shell-to-settings />
    </ShellHeader>
    <p v-if="s.words" class="break-words text-sm text-warning" role="status" data-shell-state-words>{{ s.words }}</p>
    <ShellInboxList :name="shell.name" :note="note" />
  </template>
</template>
