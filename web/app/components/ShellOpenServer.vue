<script setup lang="ts">
import type { ConductorShell } from '~/utils/shell'

/**
 * Open a server (docs/plans/phone-shell.md, OSS-1b): a Conductor server's address or a decision link, opened by the shell in its
 * in-app browser; `http://` with the words of what it means, any other address refused with words; the last five addresses opened,
 * each opened again by a tap; and Forget these addresses. It asks the shell alone, never the provider. `initial` fills the field
 * without opening it (a link the shell was opened with and no provider takes).
 */
const props = defineProps<{ bridge: ConductorShell; initial?: string }>()

const address = ref(props.initial ?? '')
const words = ref('')
const refused = ref(false)
const busy = ref(false)
const recent = ref<string[]>([])

async function readRecent() {
  try {
    recent.value = (await props.bridge.servers.recent()).slice(0, 5)
  } catch {
    recent.value = []
  }
}
onMounted(readRecent)

async function open(a: string) {
  if (busy.value) return
  busy.value = true
  words.value = ''
  try {
    const r = await props.bridge.openServer(a)
    refused.value = !r.ok
    words.value = r.words ?? ''
  } catch {
    refused.value = true
    words.value = 'The address could not be opened.'
  } finally {
    busy.value = false
  }
  await readRecent()
}

async function forget() {
  await props.bridge.servers.forget().catch(() => {})
  await readRecent()
}
</script>

<template>
  <section class="flex min-w-0 flex-col gap-3" data-shell-open-server>
    <form class="flex min-w-0 flex-col gap-2" @submit.prevent="open(address)">
      <label for="shell-server-address" class="text-sm font-medium">A Conductor server's address, or a decision link</label>
      <div class="flex min-w-0 flex-wrap gap-2">
        <UInput
          id="shell-server-address"
          v-model="address"
          type="url"
          inputmode="url"
          autocomplete="url"
          autocapitalize="off"
          :spellcheck="false"
          placeholder="https://"
          size="lg"
          class="min-w-0 flex-1 basis-56"
          :ui="{ base: 'min-h-11' }"
          aria-label="Server address"
          data-shell-address
        />
        <UButton :ui="{ label: 'min-w-0 whitespace-normal break-words text-start' }" type="submit" label="Open" color="primary" size="lg" class="max-w-full min-h-11 min-w-11 justify-center" :loading="busy" :disabled="!address.trim()" data-shell-open />
      </div>
    </form>
    <p v-if="words" class="break-words text-sm" :class="refused ? 'text-error' : 'text-warning'" role="status" :data-shell-open-words="refused ? 'refused' : 'opened'">{{ words }}</p>

    <div v-if="recent.length" class="flex min-w-0 flex-col gap-1.5" data-shell-recent>
      <h2 class="text-sm font-medium text-muted">Opened lately</h2>
      <ul class="flex min-w-0 flex-col gap-1">
        <li v-for="a in recent" :key="a" class="min-w-0">
          <UButton :label="a" color="neutral" variant="outline" class="min-h-11 w-full" :ui="{ label: 'min-w-0 whitespace-normal break-all text-start' }" :aria-label="`Open ${a}`" data-shell-recent-address @click="open(a)" />
        </li>
      </ul>
      <UButton :ui="{ label: 'min-w-0 whitespace-normal break-words text-start' }" label="Forget these addresses" icon="i-lucide-eraser" color="neutral" variant="ghost" class="max-w-full min-h-11 self-start" data-shell-forget @click="forget" />
    </div>
  </section>
</template>
