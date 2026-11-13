<script setup lang="ts">
import { codeGroups } from '~/composables/useSignIn'

/**
 * Settings → Sign in: the identity provider's sign-in (GET, POST and DELETE /api/signin), shown only where the server has an identity
 * provider that offers one. Sign in, then the code to enter on the provider's page with Copy and Open the page (an external link the
 * desktop app opens in the system browser) until the code ends (then Sign in again), then who the server is signed in as with Sign out;
 * the provider's `why` line in any state. Nothing waits for it: no modal, no route, and nothing else on the page changes.
 */
const { view, error, note, busy, begin, signOut } = useSignIn()
const copy = useCopy()

const groups = computed(() => (view.value?.state === 'code' ? codeGroups(view.value.code) : []))
const expiresAt = computed(() => {
  const at = view.value?.expires ? new Date(view.value.expires) : null
  return at && !Number.isNaN(at.getTime()) ? at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''
})
</script>

<template>
  <UCard v-if="view" :ui="{ header: 'px-4 py-3 sm:px-5', body: 'p-4 sm:p-5' }" data-signin-card :data-signin-state="view.state">
    <template #header><SettingsCardTitle title="Sign in" description="This server's sign-in with its identity provider." /></template>
    <div class="flex flex-col gap-3">
      <div v-if="view.state === 'signed-in'" class="flex flex-wrap items-center justify-between gap-3">
        <p class="min-w-0 text-sm break-words text-highlighted" data-signin-who>Signed in as {{ view.who }}</p>
        <UButton label="Sign out" icon="i-lucide-log-out" color="neutral" variant="outline" :loading="busy" :disabled="!!note" data-signin-out @click="signOut" />
      </div>
      <div v-else-if="view.state === 'code' && view.ended" class="flex flex-wrap items-center justify-between gap-3">
        <p class="text-sm text-muted" data-signin-ended>The code ended.</p>
        <UButton label="Sign in again" icon="i-lucide-log-in" :loading="busy" :disabled="!!note" data-signin-begin @click="begin" />
      </div>
      <div v-else-if="view.state === 'code'" class="flex flex-col gap-3">
        <p class="text-sm text-muted">Enter this code on the provider's page to sign this server in.</p>
        <div class="flex flex-wrap items-center gap-3">
          <span class="flex gap-3 font-mono text-2xl font-semibold tracking-widest text-highlighted select-all" data-signin-code>
            <span v-for="(g, i) in groups" :key="i" data-signin-group>{{ g }}</span>
          </span>
          <div class="flex flex-wrap gap-2">
            <UButton label="Copy" icon="i-lucide-copy" color="neutral" variant="outline" size="sm" data-signin-copy @click="copy(view.code, 'Code copied')" />
            <UButton v-if="view.url" label="Open the page" icon="i-lucide-external-link" size="sm" :to="view.url" target="_blank" rel="noopener noreferrer" external data-signin-open />
          </div>
        </div>
        <p v-if="expiresAt" class="text-xs text-muted" data-signin-expires>The code ends at {{ expiresAt }}.</p>
      </div>
      <p v-else-if="view.state === 'starting'" class="flex items-center gap-2 text-sm text-muted" data-signin-starting>
        <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />
        Starting the sign-in…
      </p>
      <div v-else class="flex flex-wrap items-center justify-between gap-3">
        <p class="text-sm text-muted">This server is not signed in.</p>
        <UButton label="Sign in" icon="i-lucide-log-in" :loading="busy" :disabled="!!note" data-signin-begin @click="begin" />
      </div>
      <p v-if="view.why" class="text-xs break-words text-muted" data-signin-why>{{ view.why }}</p>
      <p v-if="note" class="text-xs text-muted" data-signin-reading>{{ note }}</p>
      <p v-if="error" class="text-xs text-error" role="alert" data-signin-error>{{ error }}</p>
    </div>
  </UCard>
</template>
