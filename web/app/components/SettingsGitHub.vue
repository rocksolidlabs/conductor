<script setup lang="ts">
import type { TrackerEntry } from '~/composables/useTracker'
import { GITHUB_HOST, githubHref, tokenStateWords } from '~/utils/tracker'
import { relativeTime } from '~/utils/sessions'

/**
 * Settings, GitHub (Program item IN1a, docs/plans/tracker-linking.md, PR-8): a token of the person's own for github.com or a GitHub
 * Enterprise Server, so Dispatch can read GitHub's issues and pull requests and publish what the person chooses. Check and save is this
 * card's own action, never part of the page's one Save: the token goes to the server in that one request, GitHub is asked whose it is,
 * and it is kept only then. The field is a password field the browser does not fill, emptied as the request goes whatever its answer;
 * the token is never shown again, nor held by the page, a toast or the address. Each saved host shows its account, when it was saved,
 * its fingerprint (this server's mark, never a character of the token, different after a restart), its state and Remove, which stops
 * Conductor's use of it only. A token given in the server's environment is shown as such and cannot be removed here.
 */
const store = useTracker()
const host = ref(GITHUB_HOST)
const token = ref('')
const saving = ref(false)
const notice = ref('')
const failure = ref('')
/** The host whose Remove asks first; '' when none does. */
const asking = ref('')
const removing = ref('')
const now = ref(Date.now())

onMounted(() => {
  void store.load()
})

async function save() {
  if (saving.value) return
  const h = host.value.trim()
  if (!h || !token.value) return
  saving.value = true
  notice.value = ''
  failure.value = ''
  try {
    // The field is emptied as the token is handed over: before the request goes, whatever it answers.
    const out = await store.save(h, () => {
      const t = token.value
      token.value = ''
      return t
    })
    if (out.ok) notice.value = out.words
    else failure.value = out.words
  } finally {
    saving.value = false
    now.value = Date.now()
  }
}

async function remove(e: TrackerEntry) {
  removing.value = e.host
  notice.value = ''
  failure.value = ''
  try {
    const out = await store.remove(e.host)
    if (out.ok) notice.value = out.words
    else failure.value = out.words
    asking.value = ''
  } finally {
    removing.value = ''
  }
}

/** Where a host's tokens are revoked on GitHub: its token settings, a link only for a host a link may name. */
const revokeHref = (h: string) => githubHref(`https://${h}/settings/tokens`, h)
</script>

<template>
  <UCard v-if="store.available.value !== false" :ui="{ header: 'px-4 py-3 sm:px-5', body: 'p-4 sm:p-5' }" data-settings-github>
    <template #header>
      <SettingsCardTitle title="GitHub" description="A token of your own, so Dispatch reads GitHub's issues and pull requests and publishes only what you choose. Kept by this server." />
    </template>
    <div class="flex flex-col gap-4">
      <UAlert v-if="failure" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="failure" data-github-error />
      <UAlert v-if="notice" color="success" variant="subtle" icon="i-lucide-check" :title="notice" data-github-notice />
      <UAlert v-if="store.error.value" color="warning" variant="subtle" icon="i-lucide-triangle-alert" title="The tokens could not be read" :description="store.error.value" :actions="[{ label: 'Try again', color: 'neutral', variant: 'outline', onClick: () => store.load() }]" />

      <ul v-if="store.entries.value.length" class="flex flex-col divide-y divide-default rounded-md border border-default" data-github-entries>
        <li v-for="e in store.entries.value" :key="`${e.source}:${e.host}`" class="flex flex-col gap-1.5 p-3" :data-github-entry="e.host" :data-github-state="e.state" :data-github-source="e.source">
          <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            <span class="min-w-0 truncate text-sm font-medium" :title="e.host" data-github-entry-host>{{ e.host }}</span>
            <span v-if="e.login" class="text-sm text-muted" data-github-login>@{{ e.login }}</span>
            <UBadge v-if="e.source === 'environment'" label="from the environment" color="neutral" variant="subtle" size="sm" />
            <div class="flex-1" />
            <UButton v-if="e.source === 'settings' && asking !== e.host" label="Remove" icon="i-lucide-trash-2" size="xs" color="neutral" variant="ghost" data-github-remove @click="asking = e.host" />
          </div>
          <p class="flex flex-wrap gap-x-2 text-xs text-muted">
            <span data-github-state-words>{{ tokenStateWords(e.state) }}</span>
            <span v-if="e.savedAt">· saved {{ relativeTime(e.savedAt, now, { suffix: true }) }}</span>
            <span>· fingerprint <span class="font-mono" data-mono="id" data-github-fingerprint>{{ e.fingerprint }}</span></span>
          </p>
          <p v-if="e.source === 'environment'" class="text-xs text-dimmed">Given to the server in CONDUCTOR_GITHUB_TOKEN: it cannot be removed here, only where the server is started.</p>
          <!-- Under the row it was asked for alone: the environment's token, listed for the same host, cannot be removed here. -->
          <div v-if="e.source === 'settings' && asking === e.host" class="flex flex-col gap-2 rounded-md bg-elevated/60 p-2.5 text-xs" data-github-remove-ask>
            <p>
              Remove stops Conductor's use of this token only: it stays valid on GitHub until you revoke it there<template v-if="revokeHref(e.host)">,
                in <a :href="revokeHref(e.host)!" target="_blank" rel="noopener noreferrer" class="underline" data-github-revoke>GitHub's token settings</a></template>.
            </p>
            <div class="flex justify-end gap-2">
              <UButton label="Keep it" size="xs" color="neutral" variant="ghost" @click="asking = ''" />
              <UButton label="Remove" icon="i-lucide-trash-2" size="xs" color="error" variant="outline" :loading="removing === e.host" data-github-remove-confirm @click="remove(e)" />
            </div>
          </div>
        </li>
      </ul>
      <p v-else-if="store.available.value === null && store.loading.value" class="text-sm text-muted">Reading the tokens…</p>
      <p v-else class="text-sm text-dimmed" data-github-none>No GitHub token is kept yet.</p>

      <form class="flex flex-col gap-3" autocomplete="off" data-github-form @submit.prevent="save">
        <UFormField label="Host" description="github.com, or your GitHub Enterprise Server's name." size="sm">
          <UInput v-model="host" class="w-full" autocomplete="off" spellcheck="false" aria-label="The GitHub host" data-github-host />
        </UFormField>
        <UFormField label="Token" size="sm">
          <template #description>
            A fine-grained token with read access to metadata, pull requests, commit statuses and actions, and read and write access to issues;
            or a classic token with <code data-mono="key">repo</code>. A fine-grained token cannot call GitHub's Checks API: with one, a pull
            request's checks are its commit statuses and its GitHub Actions jobs, and other apps' check runs need a classic token.
          </template>
          <UInput v-model="token" type="password" class="w-full" autocomplete="new-password" spellcheck="false" aria-label="The GitHub token" data-github-token />
        </UFormField>
        <div class="flex flex-wrap items-center gap-2">
          <UButton type="submit" label="Check and save" icon="i-lucide-shield-check" size="sm" color="neutral" variant="outline" :loading="saving" :disabled="!token || !host.trim()" data-github-save />
          <span class="text-xs text-muted">Saved by its own Check and save, which asks GitHub whose it is first; not by the page's Save. The token is never shown again.</span>
        </div>
      </form>
    </div>
  </UCard>
</template>
