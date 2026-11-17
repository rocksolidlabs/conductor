<script setup lang="ts">
import { homeLeavesFor, sessionWorthOpening } from '~/utils/sessions'
import { firstLandingInTab, homeNext, type HomeLanding } from '~/utils/inbox'
import { pageState } from '~/utils/pageState'

useHead({ title: 'Sessions' })

const attention = useAttention()
const admin = useWorkbenchToken()
const launch = useLaunchModal()
/** Set token opens the token dialog over the page: with a token held, the store's ask alone would open nothing. */
const tokenDialog = useTokenDialog()
/** Below lg the list is a page of its own (design 3e): a phone opens on it. */
const wide = useMedia('(min-width: 64rem)')

// The sidebar is the session list; this route only picks the first session
// worth looking at (someone waiting, else the newest running one).
const target = computed(() => sessionWorthOpening(attention.sessions.value))

// A phone's first load of the home in a tab lands on Needs you when an item waits (docs/plans/decision-inbox.md, Owner
// decisions 10): it waits for the inbox's first answer (or its failure, or LANDING_WAIT_MS, past which it goes on as before), and
// is asked once per tab, so the Sessions tab and every later `/` stay on the sessions. Once it chose the decision page, nothing the
// home reads after (a session changing its target while that navigation is under way) sends it elsewhere (homeNext).
const inbox = useInbox()
let landing: HomeLanding = { judging: import.meta.client && !wide.value ? firstLandingInTab(sessionStore()) : false, landed: false }
const LANDING_WAIT_MS = 3000
const inboxAnswered = computed(() => inbox.state.value.loaded || !!inbox.state.value.error || !admin.hasToken.value)
const landingLate = ref(false)
if (import.meta.client && landing.judging) setTimeout(() => (landingLate.value = true), LANDING_WAIT_MS)
function sessionStore(): Pick<Storage, 'getItem' | 'setItem'> | null {
  try {
    return sessionStorage
  } catch {
    return null
  }
}

watch(
  [target, wide, inboxAnswered, landingLate],
  ([t, w, answered, late]) => {
    const next = homeNext(landing, { wide: w, answered, late, width: window.innerWidth, open: inbox.count.value, home: homeLeavesFor(w, t) })
    landing = next.landing
    if (next.to) navigateTo(next.to, { replace: true })
  },
  { immediate: true },
)

/** Wide with nothing to open, once the sessions have been read (an empty store before that says nothing): the welcome, which is not the sidebar's empty line (#359). The page stays, so that nothing moves under an open dialog. */
const welcome = computed(() => wide.value && attention.loaded.value && !target.value && !attention.sessions.value.length)
/**
 * The other picture: nothing worth opening, judged once the sessions are read (P23); before, the reading shape. A session worth
 * opening is the page's content: the home is leaving for it, and says it reads until it has.
 */
const shownState = computed(() => pageState({ read: attention.loaded.value, error: attention.error.value, count: target.value ? 1 : 0 }))

onMounted(() => {
  if (!admin.hasToken.value) admin.needsToken.value = true
  attention.start()
  inbox.start()
})
</script>

<template>
  <UDashboardPanel id="home">
    <template #header>
      <UDashboardNavbar :toggle="false" title="Sessions">
        <template #right>
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <UAlert v-if="attention.error.value && shownState !== 'failed'" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="attention.error.value" class="mb-4" :actions="[{ label: 'Set token', onClick: tokenDialog.show }]" />
      <WelcomeCard v-if="welcome" />
      <!-- The home is not a destination of the pages' table: no first line. -->
      <PageEmpty
        v-else
        icon="i-lucide-terminal"
        :state="shownState === 'content' ? 'reading' : shownState"
        words="Nothing is running. Launch an agent, or resume one from Exited in the list."
        thing="sessions"
        :error="attention.error.value"
        :actions="[
          { label: 'Launch agent', icon: 'i-lucide-play', kbd: 'N', onClick: () => launch.show() },
          { label: 'Set token', icon: 'i-lucide-key-round', failed: true, onClick: tokenDialog.show },
        ]"
        @again="attention.refresh()"
      />
    </template>
  </UDashboardPanel>
</template>
