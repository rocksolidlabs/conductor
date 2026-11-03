<script setup lang="ts">
import type { BrakemanRequest } from '~/utils/brakeman'
import { closed, kindWords, refLabel, stateWords, stopAction } from '~/utils/brakeman'

// The Brakeman page (docs/plans/brakeman.md, PR-6): ask Brakeman, follow the request in progress (its session's terminal, its
// run, Stop), the queue, and the briefs newest first. A brief is opened in place; `?request=br-n` opens one (a `brief:` ref leads
// here), read by itself when it is older than the pages read. Nothing on this page moves on its own when a request changes: a brief
// that arrives shows in the list, and the sidebar counts it until it is opened.
useHead({ title: 'Brakeman' })

const bk = useBrakeman()
const live = useAttention()
const route = useRoute()
const router = useRouter()
const toast = useToast()

onMounted(async () => {
  live.start()
  await bk.load()
  void showNamed()
})

const state = computed(() => bk.state.value)
const config = computed(() => state.value?.config)
const current = computed(() => (state.value?.current ? (bk.requests.value.find((r) => r.id === state.value!.current!.id) ?? state.value.current) : null))
/** The queue, oldest first, as the requests the page has live. */
const queue = computed(() => (state.value?.queue ?? []).map((q) => bk.requests.value.find((r) => r.id === q.id) ?? q).filter((r) => r.state === 'queued'))
/** The requests that are done, newest first: those with a brief, and those that ended without one. */
const done = computed(() => bk.requests.value.filter((r) => closed(r.state)))

const sessionName = (id?: string) => (id ? live.sessions.value.find((s) => s.id === id)?.name : undefined)
const words = (r: BrakemanRequest) => stateWords(r, sessionName(r.sessionId))
const at = (iso?: string) => (iso ? new Date(iso).toLocaleString() : '')

/** The brief open on the page: the query's request. */
const openId = computed(() => (typeof route.query.request === 'string' ? route.query.request : ''))
/** Why the request the query names cannot be shown ("" when it can, or none is named). */
const named = ref('')
// The request a link names is read by itself when the pages read do not hold it (one older than the first 100).
async function showNamed() {
  const id = openId.value
  named.value = ''
  if (!id) return
  const words = await bk.readNamed(id)
  if (openId.value === id) named.value = words
}
watch(openId, () => void showNamed())
function toggle(id: string) {
  void router.replace({ query: { ...route.query, request: openId.value === id ? undefined : id }, hash: '' })
}
// A brief opened from a link (a `brief:` ref's #item-<n>) scrolls to its item once it is drawn.
function opened(id: string) {
  bk.markOpened(id)
  nextTick(() => {
    if (route.hash) document.getElementById(route.hash.slice(1))?.scrollIntoView({ block: 'center' })
    else document.querySelector(`[data-brakeman-briefs] [data-request="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'nearest' })
  })
}

const busy = ref('')
async function cancel(r: BrakemanRequest) {
  busy.value = r.id
  try {
    await bk.cancel(r.id)
  } catch (e) {
    toast.add({ title: `${r.id} not cancelled`, description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    busy.value = ''
  }
}
/** Stop on the request in progress: an open one is cancelled, a closed one the desk still holds has its run stopped (stopAction). */
async function stop(r: BrakemanRequest) {
  busy.value = r.id
  try {
    await bk.stop(r)
  } catch (e) {
    toast.add({ title: `${r.id} not stopped`, description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    busy.value = ''
  }
}

const unavailable = computed(() => bk.available.value === false)
</script>

<template>
  <UDashboardPanel id="brakeman">
    <template #header>
      <UDashboardNavbar title="Brakeman" :ui="{ root: 'h-14' }">
        <template #leading><UDashboardSidebarCollapse /></template>
        <template #right>
          <span v-if="state" class="text-xs text-muted" data-turns-today>{{ state.turnsToday }} of {{ config?.budget.turnsPerDay }} turns today</span>
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <div class="mx-auto flex w-full max-w-3xl flex-col gap-5" data-brakeman-page>
        <UAlert v-if="unavailable" color="neutral" variant="subtle" icon="i-lucide-info" title="Brakeman is not here" :description="bk.problem.value" data-brakeman-unavailable />
        <template v-else>
          <p class="text-sm text-muted">
            Brakeman is an agent of your choosing that reads what the crews, the runs and Dispatch hold, and answers with a brief whose facts link to their evidence. It reads and drafts; nothing it writes becomes work until you act. Its agent, what it may see, its budget and whether it is on are in
            <NuxtLink to="/settings" class="underline" data-brakeman-settings-link>Settings</NuxtLink>.
          </p>
          <UAlert v-if="state && !config?.enabled" color="neutral" variant="subtle" icon="i-lucide-power" title="Brakeman is off" data-brakeman-off>
            <template #description>Choose its agent and turn it on in <NuxtLink to="/settings" class="underline">Settings</NuxtLink>.</template>
          </UAlert>
          <UAlert v-if="state?.readOnly" color="warning" variant="subtle" icon="i-lucide-lock" title="Another server owns Brakeman's records" :description="state.why" />
          <UAlert v-if="state?.stuck" color="warning" variant="subtle" icon="i-lucide-octagon-alert" title="The last turn's run did not stop" description="Stop it from its run page; Brakeman takes no new request until it has." />

          <UCard :ui="{ header: 'px-4 py-3', body: 'p-4' }">
            <template #header><SettingsCardTitle title="Ask Brakeman" description="One request a turn: Brakeman reads, then answers with one brief." /></template>
            <BrakemanAsk :kind="typeof route.query.kind === 'string' ? route.query.kind : undefined" :subject="typeof route.query.subject === 'string' ? route.query.subject : undefined" :disabled="!config?.enabled || state?.readOnly" />
          </UCard>

          <UCard v-if="current" :ui="{ header: 'px-4 py-3', body: 'p-4' }" data-brakeman-current>
            <template #header><SettingsCardTitle title="In progress" /></template>
            <div class="flex flex-wrap items-center gap-3 text-sm" :data-request="current.id" :data-request-state="current.state">
              <span class="font-medium">{{ current.id }} · {{ kindWords(current.kind) }}</span>
              <span class="text-muted" data-request-words>{{ words(current) }}</span>
              <NuxtLink v-if="current.sessionId" :to="`/sessions/${encodeURIComponent(current.sessionId)}`" class="underline" data-request-session>Watch its terminal</NuxtLink>
              <NuxtLink v-if="current.runId" :to="`/runs/${encodeURIComponent(current.runId)}`" class="underline" data-request-run>Its run</NuxtLink>
              <UButton
                v-if="stopAction(current)"
                :label="stopAction(current) === 'stop_run' ? 'Stop its run' : 'Stop'"
                icon="i-lucide-square"
                size="sm"
                color="neutral"
                variant="outline"
                class="ml-auto"
                :loading="busy === current.id"
                :data-request-stop="stopAction(current)"
                @click="stop(current)"
              />
            </div>
          </UCard>

          <UCard v-if="queue.length" :ui="{ header: 'px-4 py-3', body: 'p-4' }" data-brakeman-queue>
            <template #header><SettingsCardTitle title="Waiting" description="Taken one at a time, oldest first." /></template>
            <ul class="flex flex-col gap-2">
              <li v-for="r in queue" :key="r.id" class="flex items-center gap-3 text-sm" :data-request="r.id" :data-request-state="r.state">
                <span class="font-medium">{{ r.id }} · {{ kindWords(r.kind) }}</span>
                <span class="text-muted" data-request-words>{{ words(r) }}</span>
                <UButton label="Cancel" size="sm" color="neutral" variant="ghost" class="ml-auto" :loading="busy === r.id" data-request-cancel @click="cancel(r)" />
              </li>
            </ul>
          </UCard>

          <section class="flex flex-col gap-2" data-brakeman-briefs>
            <h2 class="text-sm font-semibold text-highlighted">Briefs</h2>
            <UAlert v-if="named" color="warning" variant="subtle" icon="i-lucide-search-x" :title="named" data-brakeman-named-missing />
            <p v-if="!done.length" class="text-sm text-muted">No brief yet.</p>
            <UCard v-for="r in done" :key="r.id" :ui="{ body: 'p-3 sm:p-4' }" :data-request="r.id" :data-request-state="r.state">
              <div class="flex flex-wrap items-center gap-2 text-sm">
                <UButton
                  :label="`${r.id} · ${kindWords(r.kind)}`"
                  :icon="openId === r.id ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
                  color="neutral"
                  variant="ghost"
                  size="sm"
                  :disabled="!r.hasBrief"
                  data-request-open
                  @click="toggle(r.id)"
                />
                <UBadge v-if="r.hasBrief && bk.unopened.value.includes(r.id)" label="new" color="primary" variant="subtle" size="sm" data-request-new />
                <span class="text-muted" data-request-words>{{ words(r) }}</span>
                <span v-if="r.subjects?.length" class="text-xs text-muted">about {{ r.subjects.map((s) => refLabel(s)).join(', ') }}</span>
                <span class="ml-auto text-xs text-muted">{{ at(r.briefAt || r.closedAt || r.createdAt) }}</span>
              </div>
              <div v-if="openId === r.id && r.hasBrief" class="mt-3 border-t border-default pt-3">
                <BrakemanBrief :request="r" @opened="opened" />
              </div>
            </UCard>
            <UButton v-if="bk.hasMore.value" label="Older requests" color="neutral" variant="outline" class="self-start" @click="bk.more()" />
          </section>
        </template>
      </div>
    </template>
  </UDashboardPanel>
</template>
