<script setup lang="ts">
import type { Integration, WebhookInfo } from '~/composables/useSessions'

// The Integrations tab moved to the Agents page's section Reporting to Conductor: an old link lands there.
definePageMeta({ middleware: (to) => (to.query.tab === 'integrations' ? navigateTo('/agents#reporting', { replace: true }) : undefined) })

useHead({ title: 'Events' })

const api = useSessions()
const admin = useWorkbenchToken()
const attention = useAttention()
const events = useEvents()

const integrations = ref<Integration[]>([])
const webhooks = ref<WebhookInfo[]>([])
const loading = ref(false)
const error = ref('')
// The clock of the last hour's window and of the feed's "now".
const now = ref(Date.now())
let tick: number | undefined

async function refresh() {
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  loading.value = true
  try {
    const r = await api.integrations()
    integrations.value = r.integrations
    webhooks.value = r.webhooks
    error.value = ''
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  // The feed fills from the live store's stream; nothing here polls.
  attention.start()
  refresh()
  tick = window.setInterval(() => (now.value = Date.now()), 15000)
})
onBeforeUnmount(() => window.clearInterval(tick))
watch(() => admin.token.value, refresh)

/** The page's tab, in the URL so a link can open Routing. */
const route = useRoute()
const router = useRouter()
type EventsTab = 'feed' | 'routing'
const tab = computed<EventsTab>({
  get: () => (route.query.tab === 'routing' ? 'routing' : 'feed'),
  set: (t) => router.replace({ query: { ...route.query, tab: t === 'feed' ? undefined : t } }),
})
const tabs = [
  { label: 'Feed', value: 'feed' },
  { label: 'Routing', value: 'routing' },
]

// This page's group in the command palette (#302): the tabs and a refresh. The hooks are installed on the Agents page.
usePalette().registerPage(() => ({
  label: 'Events',
  rows: [
    ...tabs.map((t) => ({ key: `page:tab:${t.value}`, label: t.label, icon: t.value === 'feed' ? 'i-lucide-radio-tower' : 'i-lucide-route', on: tab.value === t.value, run: () => (tab.value = t.value as EventsTab) })),
    { key: 'page:refresh', label: 'Refresh', icon: 'i-lucide-refresh-cw', run: refresh },
  ],
}))
</script>

<template>
  <UDashboardPanel id="events" :ui="{ body: 'p-0 sm:p-0 gap-0' }">
    <template #header>
      <UDashboardNavbar :toggle="false" title="Events">
        <template #right>
          <UButton label="Refresh" icon="i-lucide-refresh-cw" color="neutral" variant="outline" :loading="loading" @click="refresh" />
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <div class="flex-none px-4 pt-2 sm:px-6">
        <UTabs v-model="tab" :items="tabs" :content="false" color="neutral" variant="link" size="sm" data-events-tabs />
      </div>
      <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" class="mx-4 mt-4 sm:mx-6" />

      <div v-if="tab === 'feed'" class="flex min-h-0 flex-1 flex-col xl:flex-row" data-events-feed-tab>
        <div class="min-w-0 flex-1 px-4 pb-6 sm:px-6">
          <EventFeed :entries="events.entries.value" :now="now" />
        </div>
        <EventsAside
          :entries="events.entries.value"
          :now="now"
          :routes="events.routes.value"
          :integrations="integrations"
          class="flex-none border-t border-default p-5 xl:w-[340px] xl:border-t-0 xl:border-l"
          @edit-routing="tab = 'routing'"
        />
      </div>

      <div v-else-if="tab === 'routing'" class="flex flex-col gap-4 px-4 py-5 sm:px-6" data-events-routing-tab>
        <p class="max-w-4xl text-[13.5px] text-muted">Where each kind of event goes in this browser. The routing is kept here, not on the server; webhooks are set in <code>conductor.json</code>.</p>
        <RoutingRows :routes="events.routes.value" :webhooks="webhooks" @update="events.setRoute" />
      </div>
    </template>
  </UDashboardPanel>
</template>
