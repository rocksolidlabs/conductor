<script setup lang="ts">
import type { BrakemanRequest, BrakemanRow } from '~/utils/brakeman'
import { closed, kindWords, requestNumber } from '~/utils/brakeman'
import { askChips, askDisabled } from '~/utils/brakemanAsk'
import { roleShort } from '~/utils/brakemanRole'
import { pageState } from '~/utils/pageState'

// The Brakeman page (docs/plans/brakeman.md, PR-6; laid out as the Brakeman design review proposes, issue 993): the ask bar, then
// Now, then the latest brief, then Earlier, with In progress, In the background and Notes at the side on a wide window; on a phone
// the counts, the latest brief, the rest in one column and the bar at the foot. `?request=br-n` shows that brief in the latest's
// card (a `brief:` ref leads here), read by itself when it is older than the pages read. Nothing on this page moves on its own when
// a request changes: the card keeps the brief it showed when the page was arrived at, a newer one adds a line that offers it, and the
// sidebar counts a brief until it is opened. Nothing Brakeman does takes the focus.
useHead({ title: 'Brakeman' })

const bk = useBrakeman()
const live = useAttention()
const route = useRoute()
const router = useRouter()
const phone = useIsPhone()
// What changed since the last briefing, for the bar's first chip and Now's line: read while the page is open.
bk.followSince()

onMounted(async () => {
  live.start()
  await bk.load()
  void readLatest()
  void showNamed()
})
onBeforeUnmount(() => {
  for (const off of waiting) off()
})

const state = computed(() => bk.state.value)
const config = computed(() => state.value?.config)
const current = computed(() => (state.value?.current ? (bk.requests.value.find((r) => r.id === state.value!.current!.id) ?? state.value.current) : null))
/** The queue, oldest first, as the requests the page has live. */
const queue = computed(() => (state.value?.queue ?? []).map((q) => bk.requests.value.find((r) => r.id === q.id) ?? q).filter((r) => r.state === 'queued'))
/** The requests that are done, newest first: those with a brief, and those that ended without one. */
const done = computed(() => bk.requests.value.filter((r) => closed(r.state)))
/** The requests under way or waiting, for Earlier's Ask again (not offered for an ask among them). */
const openRequests = computed(() => [...(current.value ? [current.value] : []), ...queue.value])


/** The brief a link names: the query's request. */
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
watch(openId, (id) => {
  if (id) folded.value = false
  void showNamed()
})

/**
 * The latest brief: the newest briefed request as the page was arrived at (GET /api/brakeman/requests?has=brief&limit=1; on an
 * older server, the newest the live store holds). A newer one kept later is offered by a line (`newer`) and swaps nothing; a page
 * that showed no brief takes the first one kept, since nothing was being read.
 */
const latestId = ref('')
const newer = ref<BrakemanRow | null>(null)
/** Whether the latest brief has been read once (the card's state until then is "reading"). */
const latestRead = ref(false)
let latestBusy = false
let latestAgain = false
/** Whether the server takes the list's filters (an older one refuses them): Earlier hides them when it does not. */
const served = ref<boolean | undefined>(undefined)
async function newestBrief(): Promise<BrakemanRow | undefined> {
  try {
    const out = await bk.list({ has: 'brief', limit: 1 })
    served.value = out.filtered
    if (out.filtered) return out.requests[0]
  } catch {
    /* the live store's newest stands in */
  }
  return bk.requests.value.find((r) => r.hasBrief && closed(r.state))
}
async function readLatest() {
  if (latestBusy) {
    latestAgain = true
    return
  }
  latestBusy = true
  const gen = bk.generation.value
  try {
    do {
      latestAgain = false
      const top = await newestBrief()
      if (gen !== bk.generation.value) return
      latestRead.value = true
      if (!top) continue
      if (!latestId.value) latestId.value = top.id
      else if (requestNumber(top.id) > requestNumber(latestId.value)) newer.value = top
    } while (latestAgain)
  } finally {
    latestBusy = false
  }
}
// A brief kept (or the stream back after missing some): the newest is read again, and offered if it is newer.
watch(
  () => bk.events.value.seq,
  () => {
    const e = bk.events.value
    if (e.id === '' || e.state === 'briefed') void readLatest()
  },
)
// A new token is perhaps another server: its latest is its own.
watch(
  () => bk.generation.value,
  () => {
    latestId.value = ''
    newer.value = null
    latestRead.value = false
    void readLatest()
  },
)
const newerShown = computed(() => (newer.value && requestNumber(newer.value.id) > requestNumber(latestId.value) ? newer.value : null))
/** The newer brief, shown in the card at the person's press: it becomes the latest. */
function showNewer() {
  const n = newerShown.value
  if (!n) return
  latestId.value = n.id
  newer.value = null
  folded.value = false
  if (openId.value) void router.replace({ query: { ...route.query, request: undefined }, hash: '' })
  bringCard('nearest')
}

/**
 * Brings the card into view after a person's press showed a brief in it (a row of Earlier, Back to the latest, a newer brief's Show):
 * the card is above Earlier, so without it a press below seems to do nothing. The page scrolls, the focus stays where it was; nothing
 * else on the page scrolls it (a brief kept, a read again).
 */
function bringCard(block: ScrollLogicalPosition = 'start') {
  void nextTick(() => document.querySelector('[data-brakeman-latest]')?.scrollIntoView({ block }))
}

/** The request the card shows: the one a link names, else the latest. */
const cardId = computed(() => openId.value || latestId.value)
const cardRequest = computed(() => (cardId.value ? bk.requests.value.find((r) => r.id === cardId.value) : undefined))
/** The card folded by its row's toggle: its brief is not drawn until it is shown again. */
const folded = ref(false)
/** A row's toggle: the brief shown in the card folds; another is shown in the card (its `?request=`). */
function toggle(id: string) {
  if (openId.value === id && !folded.value) {
    folded.value = true
    void router.replace({ query: { ...route.query, request: undefined }, hash: '' })
    return
  }
  folded.value = false
  void router.replace({ query: { ...route.query, request: id }, hash: '' }).then(() => bringCard())
}
function backToLatest() {
  folded.value = false
  void router.replace({ query: { ...route.query, request: undefined }, hash: '' }).then(() => bringCard())
}
const cardState = computed(() => pageState({ read: latestRead.value || !!openId.value, error: bk.problem.value, count: cardId.value ? 1 : 0 }))

/** What waits for the page to be seen before it marks a brief opened (a tab in the background), each ended at unmount. */
const waiting = new Set<() => void>()
/**
 * A brief drawn in the card is opened once it is drawn while the page is seen (a tab in the background marks it when it is shown,
 * and only if the card still shows it); a brief a link named scrolls to its item (`#item-<n>`) once drawn.
 */
function opened(r: BrakemanRequest) {
  const id = r.id
  const mark = () => {
    if (cardId.value === id && !folded.value) bk.markOpened(r)
  }
  if (document.visibilityState === 'visible') mark()
  else {
    const seen = () => {
      if (document.visibilityState !== 'visible') return
      off()
      mark()
    }
    const off = () => {
      document.removeEventListener('visibilitychange', seen)
      waiting.delete(off)
    }
    document.addEventListener('visibilitychange', seen)
    waiting.add(off)
  }
  nextTick(() => {
    if (route.hash) document.getElementById(route.hash.slice(1))?.scrollIntoView({ block: 'center' })
  })
}

/** The page's standing paragraph: in place until a request is done, then under About this page. Its Settings link is the header's. */
const ABOUT =
  'Brakeman is an agent of your choosing that reads what the crews, the runs and Dispatch hold, and answers with a brief whose facts link to their evidence. It reads and drafts; nothing it writes becomes work until you act. Its agent, what it may see, its budget and whether it is on are in Settings.'
const unavailable = computed(() => bk.available.value === false)
/** The background role's state in a few words, for a phone's header ("cooling until 05:41"); none while it runs only when asked. */
const roleState = computed(() => (config.value?.enabled && config.value.wake.mode === 'background' ? roleShort(state.value?.role) : ''))
/** The page's content (P23): reading until the first answer, failed when it did not come, none yet, or the requests done. */
const briefsState = computed(() => pageState({ read: !!state.value, error: bk.problem.value, count: done.value.length }))

/** The bar: its chips (the first one's time is the last briefing's), why it cannot ask, and what a link asks it to start with. */
const chips = computed(() => askChips({ page: 'brakeman', lastBriefAt: bk.sinceNow.value?.since.from === 'briefing' ? bk.sinceNow.value.since.at : undefined }))
const askWhy = computed(() => askDisabled(state.value))
const prefill = computed(() => {
  const kind = typeof route.query.kind === 'string' ? route.query.kind : undefined
  const subject = typeof route.query.subject === 'string' ? route.query.subject : undefined
  return kind || subject ? { kind, subject } : undefined
})
</script>

<template>
  <UDashboardPanel id="brakeman">
    <template #header>
      <UDashboardNavbar :toggle="false" title="Brakeman" :ui="{ root: 'h-14' }" data-brakeman-header>
        <template #leading><UDashboardSidebarCollapse /></template>
        <template #title>
          <span class="flex min-w-0 items-baseline gap-2">
            <span class="truncate">Brakeman</span>
            <span v-if="roleState" class="truncate text-xs font-normal text-muted sm:hidden" data-brakeman-role-short>{{ roleState }}</span>
          </span>
        </template>
        <template #right>
          <PageAbout :shown="!unavailable && briefsState === 'content'">
            <p>{{ ABOUT }}</p>
          </PageAbout>
          <IconButton label="Settings" icon="i-lucide-settings" color="neutral" variant="ghost" to="/settings" class="max-sm:size-11 max-sm:justify-center" data-brakeman-settings-link />
          <FullscreenButton class="hidden sm:inline-flex" />
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <!-- The container queries' root is the body's own width, at a whole pixel: the page itself is centred in it. -->
      <div class="@container w-full">
      <div class="mx-auto flex w-full max-w-[65rem] flex-col gap-5" data-brakeman-page>
          <UAlert v-if="unavailable" color="neutral" variant="subtle" icon="i-lucide-info" title="Brakeman is not here" :description="bk.problem.value" data-brakeman-unavailable />
          <template v-else>
            <p v-if="briefsState !== 'content'" class="text-sm text-muted" data-brakeman-about>{{ ABOUT }}</p>
            <UAlert v-if="state && !config?.enabled" color="neutral" variant="subtle" icon="i-lucide-power" title="Brakeman is off" data-brakeman-off>
              <template #description>Choose its agent and turn it on in <NuxtLink to="/settings" class="underline">Settings</NuxtLink>.</template>
            </UAlert>
            <UAlert v-if="state?.readOnly" color="warning" variant="subtle" icon="i-lucide-lock" title="Another server owns Brakeman's records" :description="state.why" />
            <UAlert v-if="state?.stuck" color="warning" variant="subtle" icon="i-lucide-octagon-alert" title="The last turn's run did not stop" description="Stop it from its run page; Brakeman takes no new request until it has." />

            <BrakemanAskBar v-if="!phone" label="Ask Brakeman" :chips="chips" size="full" :disabled="askWhy" :prefill="prefill" />

            <BrakemanNow />

            <!-- Wide, two columns: the latest brief and Earlier; In progress, In the background and Notes at the side. Narrower than
                 both side by side, one column: In progress between Now and the latest brief (on a phone, after it). -->
            <div class="flex flex-col gap-5 @min-[53.75rem]:flex-row @min-[53.75rem]:items-start">
              <div class="contents @min-[53.75rem]:flex @min-[53.75rem]:min-w-0 @min-[53.75rem]:flex-[1_1_560px] @min-[53.75rem]:flex-col @min-[53.75rem]:gap-5" data-brakeman-main>
                <section class="order-2 flex min-w-0 flex-col gap-2 max-sm:order-1" data-brakeman-latest :data-request="cardId || undefined" :data-latest-named="openId ? '' : undefined">
                  <div v-if="newerShown" class="flex flex-wrap items-center gap-2 text-sm" data-brief-newer>
                    <span class="min-w-0 text-muted">A newer brief: {{ newerShown.digest?.title || newerShown.id }}</span>
                    <UButton label="Show" size="sm" color="neutral" variant="ghost" class="max-sm:min-h-11" data-brief-newer-show @click="showNewer" />
                  </div>
                  <div v-if="openId && openId !== latestId && latestId" class="flex">
                    <UButton label="Back to the latest" icon="i-lucide-arrow-left" size="sm" color="neutral" variant="ghost" class="max-sm:min-h-11" data-brief-back @click="backToLatest" />
                  </div>
                  <UAlert v-if="named" color="warning" variant="subtle" icon="i-lucide-search-x" :title="named" data-brakeman-named-missing />
                  <PageEmpty v-if="!cardId" page="brakeman" :state="cardState === 'content' ? 'empty' : cardState" words="No brief yet. Ask for the first one." thing="briefs" :error="bk.problem.value" @again="readLatest()" />
                  <UCard v-else-if="cardRequest" :ui="{ body: 'p-4 sm:p-5' }">
                    <div v-if="folded" class="flex flex-wrap items-center gap-2 text-sm" data-brief-folded>
                      <span class="min-w-0 flex-1 truncate">{{ cardRequest.digest?.title || `${cardRequest.id} · ${kindWords(cardRequest.kind)}` }}</span>
                      <UButton label="Show" size="sm" color="neutral" variant="ghost" class="max-sm:min-h-11" data-brief-show @click="toggle(cardRequest.id)" />
                    </div>
                    <BrakemanBrief v-else :key="cardRequest.id" :request="cardRequest" head @opened="opened" />
                  </UCard>
                </section>

                <div v-if="done.length" class="order-3 min-w-0">
                  <BrakemanEarlier :latest="latestId" :shown="cardId" :folded="folded" :served="served" :waiting="openRequests" @toggle="toggle" />
                </div>
              </div>

              <div class="contents @min-[53.75rem]:flex @min-[53.75rem]:min-w-0 @min-[53.75rem]:flex-[0_0_280px] @min-[53.75rem]:flex-col @min-[53.75rem]:gap-5" data-brakeman-side>
                <div v-if="current || queue.length" class="order-1 min-w-0 max-sm:order-2" data-brakeman-progress>
                  <BrakemanInProgress :current="current" :queue="queue" />
                </div>
                <div class="order-4 min-w-0">
                  <BrakemanRoleCard v-if="state && config?.enabled" :background="config.wake.mode === 'background'" :window-seconds="config.wake.windowSeconds" :briefings="config.wake.briefings" />
                </div>
              </div>
            </div>
          </template>
        </div>
      </div>
    </template>
    <template v-if="phone && !unavailable" #footer>
      <div class="border-t border-default bg-default px-4 py-2" data-brakeman-foot>
        <BrakemanAskBar label="Ask Brakeman" :chips="chips" size="foot" :disabled="askWhy" :prefill="prefill" />
      </div>
    </template>
  </UDashboardPanel>
</template>
