<script setup lang="ts">
import { pickerStart, type PickerField } from '~/utils/dirPicker'
import type { AgentInfo, ReachInfo } from '~/composables/useSessions'
import { identity, notInstalledBadge } from '~/utils/agents'
import { plainCopy, switchyardProblem, type DesktopIceStatus, type DesktopSettings } from '~/utils/desktop'
import { followSaved, holds, saveParts, saveWords, together, type BrakemanPart, type SaveOutcome } from '~/utils/settingsSave'
import { SETTINGS_FIELD_UI, sectionFromQuery, sectionInView, shownSections, type SectionBox, type SettingsSectionId } from '~/utils/settingsSections'

// The desktop app's settings: what its shell starts the server with. In a
// browser against a server configured by its file, the page says so.
const desktop = useDesktop()
const api = useSessions()
const toast = useToast()
const serverHost = useServerHost()

/** The cards' spacing: a title close to its content (#366). */
const cardUi = { header: 'px-4 py-3 sm:px-5', body: 'p-4 sm:p-5' }
const fieldUi = SETTINGS_FIELD_UI
/** A section's card: scrolled to, it clears the phone's sticky row of links, and on a wide window keeps the body's padding above it. */
const sectionClass = 'scroll-mt-16 sm:scroll-mt-18 lg:scroll-mt-6'
/** The navbar's buttons: their words from sm, their icons alone below it (each has an aria-label and a tooltip). */
const navbarButtonUi = { label: 'hidden sm:inline' }
const settings = ref<DesktopSettings | null>(null)
const form = reactive<DesktopSettings>({ dataDir: '', allowedRoots: [], defaultCwd: '', yolo: false, reach: 'auto', closeToTray: true, wslDistro: '', wslWindowsHome: false, switchyardEnabled: true, switchyardServer: '', switchyardToken: '', switchyardName: '' })
const saving = ref(false)
const error = ref('')
const reach = ref<ReachInfo | null>(null)
/** Windows: what the app forwards for WebRTC from WSL (null elsewhere). */
const ice = ref<DesktopIceStatus | null>(null)
const allowing = ref(false)
async function allowFirewall() {
  const b = desktop.bridge.value
  if (!b || !ice.value) return
  allowing.value = true
  try {
    ice.value = { ...ice.value, firewall: await b.allowIceFirewall() }
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    allowing.value = false
  }
}
const agents = ref<AgentInfo[]>([])
const versions = ref<{ app: string; electron: string; node: string; chrome: string; server: string } | null>(null)
const rootsText = computed({
  get: () => form.allowedRoots.join('\n'),
  set: (v: string) => (form.allowedRoots = v.split('\n').map((s) => s.trim()).filter(Boolean)),
})
const reachItems = [
  { label: 'Auto: map the TLS port on the router when there is one', value: 'auto' },
  { label: 'Manual: find the public address only', value: 'manual' },
  { label: 'Off', value: 'off' },
]
const appDirty = computed(() => !!settings.value && JSON.stringify({ ...settings.value }) !== JSON.stringify({ ...form }))
/** Brakeman's section, saved by the page's one Save with the rest (#707); in a browser, alone. */
const brakemanCard = useTemplateRef<{ dirty: boolean; blocked: boolean; take: () => BrakemanPart | null }>('brakemanCard')
/** Brakeman's form holds a change the form's own checks refuse: nothing is saved until it is fixed, where it says. */
const blocked = computed(() => !!brakemanCard.value?.dirty && !!brakemanCard.value?.blocked)
const dirty = computed(() => appDirty.value || !!brakemanCard.value?.dirty)
// The switchyard as last loaded or saved: a plain one on another machine is
// refused with the words why (switchyardProblem, which the app's validate
// repeats) until the field or the switch changes. Loaded so (settings saved
// before the app refused it), the app publishes nowhere meanwhile.
const switchyardChecked = ref('')
const switchyardKey = () => JSON.stringify([form.switchyardEnabled, form.switchyardServer])
const switchyardError = computed(() => (switchyardChecked.value === switchyardKey() ? switchyardProblem(form) : ''))
const restarts = computed(() => {
  if (!settings.value) return false
  const keys: Array<keyof DesktopSettings> = ['dataDir', 'allowedRoots', 'defaultCwd', 'yolo', 'reach', 'wslDistro', 'wslWindowsHome', 'switchyardEnabled', 'switchyardServer', 'switchyardToken', 'switchyardName']
  return keys.some((k) => JSON.stringify(settings.value![k]) !== JSON.stringify(form[k]))
})

async function load() {
  const b = desktop.bridge.value
  if (!b) return
  try {
    const s = await b.settings.get()
    // A field changed while a save was out keeps its edit (followSaved); the first read sets every field.
    followSaved(form, settings.value, s)
    settings.value = s
    switchyardChecked.value = switchyardKey()
    versions.value = await b.versions()
    if (b.platform === 'win32') ice.value = await b.ice()
  } catch (e) {
    error.value = (e as Error).message
  }
  await Promise.all([
    api.reach().then((r) => (reach.value = r)).catch(() => (reach.value = null)),
    api.catalog().then((c) => (agents.value = c)).catch(() => (agents.value = [])),
  ])
}

/** A Save refused before anything was sent: the words at the top, and where the button is too (#348) when Brakeman's is held with it. */
function refuse(out: SaveOutcome) {
  const words = saveWords(out)
  error.value = words.description ?? words.title
  if (out.brakeman === 'held') toast.add({ title: words.title, description: words.description, icon: 'i-lucide-triangle-alert', color: 'error' })
}

async function save() {
  const b = desktop.bridge.value
  // One save at a time: a second click before the first is under way would set the settings and restart the server twice.
  if (saving.value || blocked.value) return
  // Both parts as they are at the click (#707's review): what is typed while the save is out is kept for the next Save, never sent
  // by this one, and what the page tells (a restart, the order) is told of what is sent.
  const sent = b && appDirty.value && settings.value ? plainCopy(form) : null
  const before = settings.value
  const card = brakemanCard.value?.take() ?? null
  if (!sent && !card) return
  error.value = ''
  if (sent && before) {
    switchyardChecked.value = switchyardKey()
    const refused = switchyardProblem(sent) || together(before, sent, card)
    if (refused) {
      refuse({ brakeman: card ? 'held' : 'unchanged', app: 'refused', error: refused })
      return
    }
  }
  // Taken at once, before anything is awaited, so the button and the guard above hold from the first click (#433's review).
  saving.value = true
  // What this page can tell on its own, for an app that cannot say what became
  // of the save (older apps): taken before the save, since once the saved
  // settings are copied into the form nothing differs any more (#214).
  const restarting = !!sent && restarts.value
  try {
    // Brakeman's part first, on this server: the app's may restart it (utils/settingsSave.ts).
    const out = await saveParts({
      brakeman: card ? card.send : null,
      app: sent
        ? {
            send: async () => {
              // A restart moves the server to a fresh port and the app loads this page there with its path and query (desktop/src/window.ts,
              // followOrigin): the section in view goes in the query first, so the page that follows opens on it (#433).
              if (restarting) await keepSectionInQuery()
              // The copy taken at the click: plain, since the form is reactive and a Vue proxy cannot cross the bridge ("An object could
              // not be cloned", #348).
              const next = await b!.settings.set(sent)
              followSaved(form, sent, next)
              settings.value = next
              // The app says what became of it: the server restarted, or the settings
              // were saved alone (a retry after a restart that failed changes nothing the
              // server reads, since the settings were kept).
              const told = await b!.settings.outcome?.().catch(() => undefined)
              return told === undefined ? (restarting ? 'restarted' : 'saved') : told === 'restarted' ? 'restarted' : 'saved'
            },
            // The app keeps the settings before it restarts the server (desktop/src/save.ts): refused, it may hold them all the same.
            kept: async () => holds(await b!.settings.get(), sent),
          }
        : null,
    })
    const words = saveWords(out)
    if (words.ok) toast.add({ title: words.title, icon: 'i-lucide-check', color: 'success' })
    else {
      error.value = words.description ?? words.title
      // The alert sits at the top of a long page: say it where the button is too (#348).
      toast.add({ title: words.title, description: words.description, icon: 'i-lucide-triangle-alert', color: 'error' })
    }
    if (out.app === 'saved' || out.app === 'restarted') await load()
  } finally {
    saving.value = false
  }
}

// The folder picker browses the server's own folders (on Windows, the WSL distribution's), never the native dialog.
const picker = ref<{ field: PickerField; start: string; title: string; expect?: 'under-roots' | 'outside-roots' } | null>(null)
const pickerOpen = ref(false)
const pickerTitles: Record<PickerField, string> = { root: 'Add an allowed root', defaultCwd: 'Default working directory', dataDir: 'Data directory' }
function openPicker(field: PickerField) {
  picker.value = { field, start: pickerStart(field, form), title: pickerTitles[field], expect: field === 'defaultCwd' ? 'under-roots' : field === 'dataDir' ? 'outside-roots' : undefined }
  pickerOpen.value = true
}
function onPick(dir: string) {
  const field = picker.value?.field
  if (field === 'root') form.allowedRoots = form.allowedRoots.includes(dir) ? form.allowedRoots : [...form.allowedRoots, dir]
  else if (field) form[field] = dir
}

/** The save's toast: the server restarted for it, or it was saved alone. */
function savedToast(outcome: string) {
  toast.add({ title: outcome === 'restarted' ? 'Saved; the server restarted' : 'Saved', icon: 'i-lucide-check', color: 'success' })
}

// The sections (#433): a navigation beside them on a wide window, a sticky row of links above them below lg, the section in view
// marked, and the one chosen kept in `?section=` (router.replace). The panel's body is what scrolls, never the window.
const route = useRoute()
const router = useRouter()
const brakeman = useBrakeman()
const sections = computed(() => shownSections({ desktop: desktop.isDesktop.value, platform: desktop.bridge.value?.platform, ice: !!ice.value, agents: agents.value.length, brakeman: brakeman.available.value !== false }))
const layout = ref<HTMLElement | null>(null)
const nav = ref<HTMLElement | null>(null)
const current = ref<SettingsSectionId | null>(null)
/**
 * The section last chosen, from the navigation or the address: marked while any of it shows (sectionInView), until the person
 * scrolls, types or clicks (unpin), after which the mark follows the scroll alone.
 */
let chosen: SettingsSectionId | null = null
/** Set once the page's sections have rendered (the bridge and the catalog answered): an address read before waits for it. */
const loaded = ref(false)
/** The address this page wrote without a choice (keepSectionInQuery): a section, or none at the top. No scroll follows it. */
let kept: { id: SettingsSectionId | null } | null = null
const body = () => layout.value?.closest<HTMLElement>('[data-slot="body"]') ?? null
const sectionEl = (id: SettingsSectionId) => layout.value?.querySelector<HTMLElement>(`[data-settings-section="${id}"]`) ?? null
/** What the phone's sticky row of links covers at the top of the body; nothing beside the sections on a wide window. */
const cover = () => (nav.value && getComputedStyle(nav.value).flexDirection === 'row' ? nav.value.offsetHeight : 0)
function spy() {
  const el = body()
  if (!el) return
  const top = el.getBoundingClientRect().top - el.scrollTop
  const boxes: SectionBox[] = []
  for (const s of sections.value) {
    const r = sectionEl(s.id)?.getBoundingClientRect()
    if (r) boxes.push({ id: s.id, top: r.top - top, bottom: r.bottom - top })
  }
  const next = sectionInView(boxes, { scrollTop: el.scrollTop, clientHeight: el.clientHeight, scrollHeight: el.scrollHeight, cover: cover() }, chosen)
  if (next !== chosen) chosen = null
  current.value = next
}
let frame = 0
const onScroll = () => {
  if (frame) return
  frame = requestAnimationFrame(() => {
    frame = 0
    spy()
  })
}
/**
 * The section brought into view and held there until the person scrolls, types or clicks: what renders after it was chosen (an
 * agent's badge, Brakeman's form, a card above it that the bridge or the catalog answered for) would move it out of place.
 */
let pinned: SettingsSectionId | null = null
const unpin = () => {
  pinned = null
  chosen = null
}
/** Puts a section's top at the top of the body, less its scroll margin (which clears the phone's row of links). The body alone scrolls. */
function align(id: SettingsSectionId): boolean {
  const b = body()
  const el = sectionEl(id)
  if (!b || !el) return false
  const margin = parseFloat(getComputedStyle(el).scrollMarginTop) || 0
  b.scrollTop += el.getBoundingClientRect().top - b.getBoundingClientRect().top - margin
  return true
}
/** Brings a section into view; none, or one not shown, opens at the top. */
function scrollToSection(id: SettingsSectionId | null) {
  const shown = id ? align(id) : false
  chosen = shown ? id : null
  pinned = shown ? id : null
  if (!shown) body()?.scrollTo({ top: 0 })
  spy()
}
/** Writes the section in view in the address, or none at the top, where an older `?section=` would open the next page elsewhere. */
async function keepSectionInQuery() {
  const el = body()
  if (!el) return
  // The mark follows a scroll a frame later (onScroll): read the section in view now, so a save made before that frame writes
  // where the person is, not where they were (#433's review).
  spy()
  const id = el.scrollTop === 0 ? null : current.value
  const now = route.query.section
  if (id ? now === id : now === undefined) return
  const query = { ...route.query }
  if (id) query.section = id
  else delete query.section
  kept = { id }
  await router.replace({ query })
}
// The address names a section: once the sections have rendered (WebRTC and the agents wait for the bridge and the catalog), and
// again whenever it changes while the page is open (the publishing notice's Settings, the sidebar's Settings back to the top).
watch(
  () => [route.query.section, loaded.value] as const,
  async ([q, ready]) => {
    if (!ready || !desktop.isDesktop.value) return
    const id = sectionFromQuery(q)
    if (kept && kept.id === id) {
      kept = null
      return
    }
    await nextTick()
    scrollToSection(id)
  },
)
// The same address again (the sidebar's Settings on plain /settings, the notice's Settings on ?section=switchyard) changes neither
// value the watcher above reads: Vue Router refuses it as a duplicate, which it hands to afterEach, and the page goes where the
// address says as it would on a change.
const stopDuplicates = router.afterEach((to, from, failure) => {
  if (!failure || to.fullPath !== from.fullPath || to.path !== '/settings' || !loaded.value || !desktop.isDesktop.value) return
  void nextTick(() => scrollToSection(sectionFromQuery(to.query.section)))
})
const UNPIN = ['wheel', 'touchstart', 'pointerdown', 'keydown'] as const
let resized: ResizeObserver | null = null
onMounted(() => {
  const b = body()
  if (!b) return
  b.addEventListener('scroll', onScroll, { passive: true })
  for (const e of UNPIN) b.addEventListener(e, unpin, { passive: true })
  // The sections change size as what they wait on answers: the section held in view goes back to its place, and the mark follows.
  // The body too: a window made shorter changes its visible part alone, with no scroll and no change to the sections.
  resized = new ResizeObserver(() => {
    if (pinned) align(pinned)
    spy()
  })
  const sectionsEl = layout.value?.querySelector('[data-settings-sections]')
  if (sectionsEl) resized.observe(sectionsEl)
  resized.observe(b)
  spy()
})
onBeforeUnmount(() => {
  stopDuplicates()
  const b = body()
  b?.removeEventListener('scroll', onScroll)
  for (const e of UNPIN) b?.removeEventListener(e, unpin)
  resized?.disconnect()
  if (frame) cancelAnimationFrame(frame)
})
watch(sections, () => nextTick(spy))

onMounted(async () => {
  // A restarted server listens on a fresh port and the app loads this page
  // there: the page that saved is gone before its save resolved, and the
  // save's outcome is owed to this one (desktop/src/save.ts). Asked first,
  // before the form can be saved from this page: the app answers an ask made
  // while a restart is pending with nothing, so this ask never takes what a
  // later save of this page owes to the page that follows it.
  const owed = desktop.bridge.value?.settings.outcome?.().catch(() => '')
  await load()
  loaded.value = true
  if ((await owed) === 'restarted') savedToast('restarted')
})

// This page's group in the command palette (#302): Save, and the desktop app's buttons. Only in the desktop app (#332): the
// browser's page is the server's file, a notice and Brakeman's section, which its save bar saves, so it offers no row.
const live = useAttention()
usePalette().registerPage(() => {
  if (!desktop.isDesktop.value) return null
  const n = live.sessions.value.filter((s) => s.kind === 'server' && (s.status === 'running' || s.status === 'starting')).length
  return {
    label: 'Settings',
    rows: [
      { key: 'page:save', label: restarts.value ? 'Save and restart the server' : 'Save', icon: 'i-lucide-save', disabled: !dirty.value || saving.value || blocked.value, ask: restarts.value ? { title: 'Save and restart the server?', detail: n ? `${n} ${n === 1 ? 'session ends' : 'sessions end'} with it.` : 'No session is running.' } : undefined, run: save },
      ...(desktop.isDesktop.value
        ? [
            { key: 'page:restart', label: 'Restart server', icon: 'i-lucide-rotate-ccw', ask: { title: 'Restart the server?', detail: n ? `${n} ${n === 1 ? 'session ends' : 'sessions end'} with it.` : 'No session is running.' }, run: () => void desktop.bridge.value?.restartServer() },
            { key: 'page:browser', label: 'Open in browser', icon: 'i-lucide-external-link', run: () => void desktop.bridge.value?.openInBrowser() },
            { key: 'page:log', label: 'Server log', icon: 'i-lucide-scroll-text', run: () => void desktop.bridge.value?.showLog() },
          ]
        : []),
    ],
  }
})
</script>

<template>
  <UDashboardPanel id="settings">
    <template #header>
      <UDashboardNavbar :toggle="false" title="Settings" :ui="{ root: 'h-14' }">
        <template #leading><UDashboardSidebarCollapse /></template>
        <template #right>
          <!-- Icon-only below sm, so the four fit a phone's width (#433); the tooltip names each (#374). -->
          <template v-if="desktop.isDesktop.value">
            <UTooltip text="Open in browser">
              <UButton label="Open in browser" aria-label="Open in browser" icon="i-lucide-external-link" color="neutral" variant="outline" :ui="navbarButtonUi" data-open-in-browser @click="desktop.bridge.value?.openInBrowser()" />
            </UTooltip>
            <UTooltip text="Server log">
              <UButton label="Server log" aria-label="Server log" icon="i-lucide-scroll-text" color="neutral" variant="outline" :ui="navbarButtonUi" data-server-log @click="desktop.bridge.value?.showLog()" />
            </UTooltip>
            <UTooltip text="Restart server">
              <UButton label="Restart server" aria-label="Restart server" icon="i-lucide-rotate-ccw" color="neutral" variant="outline" :ui="navbarButtonUi" data-restart-server @click="desktop.bridge.value?.restartServer()" />
            </UTooltip>
          </template>
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <!--
        The sections stay one column of cards that reads top to bottom (#366: two columns left a blank beside every short card), as
        wide as the panel up to 80rem (#433). In a browser there is no navigation: the notice, Brakeman and The app, stacked.
      -->
      <div v-if="!desktop.isDesktop.value" class="@container mx-auto flex w-full max-w-7xl flex-col gap-5" data-settings-sections>
        <UAlert color="neutral" variant="subtle" icon="i-lucide-info" title="This server is configured by its file" description="These settings belong to the desktop app, which starts a server of its own. A server you run yourself takes its configuration file and CONDUCTOR_* variables (see the README)." />
        <!-- Brakeman's configuration is the server's own, saved through its route: here in a browser too. -->
        <BrakemanSettings ref="brakemanCard" data-settings-section="brakeman" />
        <AboutCard data-settings-section="app" />
      </div>
      <div v-else ref="layout" class="mx-auto flex w-full max-w-7xl flex-col gap-4 lg:flex-row lg:items-start lg:gap-6" data-desktop-settings>
        <!--
          Beside the sections from lg, in place as they scroll; below it a row of links stuck to the top of the body, scrolling
          sideways within itself. An entry brings its section into view at once, whether or not the address changes (it may name it).
        -->
        <nav
          ref="nav"
          aria-label="Settings sections"
          class="sticky -top-4 z-10 -mx-4 -mt-4 flex flex-none gap-1 overflow-x-auto border-b border-default bg-default px-4 py-2 sm:-top-6 sm:-mx-6 sm:-mt-6 sm:px-6 lg:top-0 lg:mx-0 lg:mt-0 lg:w-56 lg:flex-col lg:overflow-visible lg:border-0 lg:bg-transparent lg:p-0"
          data-settings-nav
        >
          <UButton
            v-for="s in sections"
            :key="s.id"
            :to="{ path: '/settings', query: { section: s.id } }"
            replace
            :label="s.title"
            :icon="s.icon"
            color="neutral"
            variant="ghost"
            active-color="primary"
            active-variant="soft"
            :active="current === s.id"
            :aria-current="current === s.id ? 'true' : undefined"
            class="flex-none lg:w-full"
            :data-settings-nav-entry="s.id"
            @click="scrollToSection(s.id)"
          />
        </nav>
        <div class="@container flex min-w-0 flex-1 flex-col gap-5" data-settings-sections>
        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-settings-error />
        <UAlert
          v-if="desktop.serverState.value"
          :color="desktop.serverState.value.state === 'running' ? 'success' : desktop.serverState.value.state === 'failed' ? 'error' : 'neutral'"
          variant="subtle"
          :icon="desktop.serverState.value.state === 'running' ? 'i-lucide-check' : 'i-lucide-loader-circle'"
          :title="`Server ${desktop.serverState.value.state}${desktop.serverState.value.url ? ` at ${desktop.serverState.value.url}` : ''}`"
          :description="desktop.serverState.value.lastError || (desktop.serverState.value.version ? `conductor ${desktop.serverState.value.version}` : '')"
          data-server-state
        />
        <UCard v-if="ice" :ui="cardUi" :class="sectionClass" data-settings-section="webrtc" data-ice-status>
          <template #header><SettingsCardTitle title="WebRTC from WSL" description="What the app forwards so a shared session connects from inside the distribution." /></template>
          <div class="flex flex-col gap-3 text-sm">
            <p v-if="ice.forwarding">
              The app forwards UDP port <code>{{ ice.port }}</code> on <code>{{ ice.publicIp }}</code> into the distribution at <code>{{ ice.wslAddress }}</code>, so a shared session connects peer to peer through your router only. WSL's own networking is left as it is.
            </p>
            <p v-else class="text-muted">Nothing is forwarded{{ ice.reason ? `: ${ice.reason}` : '' }}.</p>
            <div v-if="ice.forwarding" class="flex flex-wrap items-center gap-2">
              <UBadge :label="ice.firewall === 'present' ? 'firewall rule present' : ice.firewall === 'missing' ? 'firewall rule missing' : 'firewall rule unknown'" :color="ice.firewall === 'present' ? 'success' : ice.firewall === 'missing' ? 'warning' : 'neutral'" variant="subtle" data-ice-firewall />
              <UButton v-if="ice.firewall !== 'present'" label="Allow through the Windows firewall" icon="i-lucide-shield-check" size="sm" color="neutral" variant="outline" :loading="allowing" data-ice-allow @click="allowFirewall" />
              <span v-if="ice.firewall !== 'present'" class="text-xs text-muted">One elevation prompt adds an inbound rule for UDP {{ ice.port }}.</span>
            </div>
          </div>
        </UCard>

        <UCard :ui="cardUi" :class="sectionClass" data-settings-section="switchyard" data-switchyard-settings>
          <template #header><SettingsCardTitle title="Switchyard" description="Share links that work from anywhere, through a switchyard." /></template>
          <div class="flex flex-col gap-4">
            <USwitch v-model="form.switchyardEnabled" label="Publish sessions to a switchyard" description="So a share link works from anywhere." data-switchyard-enabled />
            <p class="text-sm text-muted">
              Every session here is published to a switchyard, switchyard.rslabs.net unless you name another. It is told each session's name, agent and folder name, this machine's name, and whether the session waits on you; never its chat, activity or command line. It mints and checks the links and decides who joins, and the terminal goes between the viewer and this machine over WebRTC, or through the switchyard's relay, which can read it, when no direct path forms. Publish only to a switchyard whose operator you trust. Off, links work where this machine is reachable.
            </p>
            <UFormField label="Switchyard" description="Leave empty for the public one. https://, or http:// only for one on this machine (localhost)." :error="switchyardError || undefined" :ui="fieldUi" data-switchyard-server>
              <UInput v-model="form.switchyardServer" class="w-full font-mono text-xs" placeholder="https://switchyard.rslabs.net" :disabled="!form.switchyardEnabled" />
            </UFormField>
            <UFormField label="Host token" description="Optional: a host token for a private switchyard, or a trusted seat on the public one, outside its per-address limits." :ui="fieldUi">
              <UInput v-model="form.switchyardToken" type="password" class="w-full font-mono text-xs" autocomplete="off" :disabled="!form.switchyardEnabled" />
            </UFormField>
            <UFormField label="Shown there as" description="How this machine is named at the switchyard; its host name when empty." :ui="fieldUi">
              <UInput v-model="form.switchyardName" class="w-full" maxlength="64" :disabled="!form.switchyardEnabled" />
            </UFormField>
          </div>
        </UCard>

        <UCard :ui="cardUi" :class="sectionClass" data-settings-section="folders">
          <template #header><SettingsCardTitle title="Where agents work" description="The folders sessions may run in, and where the server keeps its data." /></template>
          <div class="flex flex-col gap-4">
            <p v-if="desktop.bridge.value?.platform === 'win32'" class="text-sm text-muted" data-wsl-paths>
              The server runs inside your WSL distribution, so these are its paths (such as <code class="font-mono">/home/&lt;user&gt;/code</code>); the folder picker browses the distribution. Windows folders need the switch under "How agents run" and live under <code class="font-mono">/mnt</code>.
            </p>
            <UFormField label="Allowed roots" description="Directories server sessions may run in, one per line. The agents can read and change everything under them." :ui="fieldUi">
              <div class="flex gap-2">
                <UTextarea v-model="rootsText" :rows="3" class="min-w-0 flex-1 font-mono text-xs" />
                <UButton icon="i-lucide-folder-plus" color="neutral" variant="outline" aria-label="Add a root" data-pick-dir="root" @click="openPicker('root')" />
              </div>
            </UFormField>
            <UFormField label="Default working directory" description="Where a launch runs when it names no directory; under an allowed root." :ui="fieldUi">
              <div class="flex gap-2"><UInput v-model="form.defaultCwd" class="min-w-0 flex-1 font-mono text-xs" /><UButton icon="i-lucide-folder" color="neutral" variant="outline" aria-label="Pick the default working directory" data-pick-dir="defaultCwd" @click="openPicker('defaultCwd')" /></div>
            </UFormField>
            <UFormField label="Data directory" description="The server's catalog, crews, hooks and certificates. Keep it outside the allowed roots." :ui="fieldUi">
              <div class="flex gap-2"><UInput v-model="form.dataDir" class="min-w-0 flex-1 font-mono text-xs" /><UButton icon="i-lucide-folder" color="neutral" variant="outline" aria-label="Pick the data directory" data-pick-dir="dataDir" @click="openPicker('dataDir')" /></div>
            </UFormField>
          </div>
        </UCard>

        <UCard :ui="cardUi" :class="sectionClass" data-settings-section="running">
          <template #header><SettingsCardTitle title="How agents run" description="Permission prompts, reach from outside your network, and the WSL distribution." /></template>
          <div class="flex flex-col gap-4">
            <UFormField label="Yolo" description="Launch every agent with its yolo recipe, skipping its permission prompts (Codex's drops its sandbox). Only for roots you would let an agent change unasked." :ui="fieldUi">
              <USwitch v-model="form.yolo" label="Skip permission prompts" data-yolo-switch />
            </UFormField>
            <UFormField label="Reach" description="How the server finds out it can be reached from outside your network. Links become public only once a TLS listener has a certificate." :ui="fieldUi">
              <USelect v-model="form.reach" :items="reachItems" class="w-full" />
            </UFormField>
            <div v-if="reach" class="rounded-md bg-elevated px-3 py-2 text-xs font-mono" data-reach-state>
              reach {{ reach.mode }}<template v-if="reach.externalIp"> · public address {{ reach.externalIp }}</template><template v-if="reach.mapped"> · port {{ reach.externalPort }} mapped via {{ reach.method }}</template><template v-if="reach.tls"> · certificate {{ reach.tls.ready ? 'ready' : 'pending' }}</template><template v-if="reach.error"> · {{ reach.error }}</template>
            </div>
            <UFormField v-if="desktop.bridge.value?.platform === 'win32'" label="WSL distribution" description="The distribution the server runs in; empty for the default. Agents are found on its PATH." :ui="fieldUi">
              <UInput v-model="form.wslDistro" class="w-full font-mono text-xs" placeholder="Ubuntu" />
            </UFormField>
            <UFormField v-if="desktop.bridge.value?.platform === 'win32'" label="Windows folders" :ui="fieldUi">
              <USwitch v-model="form.wslWindowsHome" label="Also allow your Windows profile under /mnt (slower)" />
            </UFormField>
          </div>
        </UCard>

        <UCard v-if="agents.length" :ui="cardUi" :class="sectionClass" data-settings-section="agents">
          <template #header><SettingsCardTitle title="Agents on this machine" description="What the server finds on its PATH; a restart reads the PATH again." /></template>
          <!-- Two columns once the column has room, so a long catalog stays within the visible body (#433). -->
          <ul class="grid gap-x-8 gap-y-1.5 text-sm @3xl:grid-cols-2">
            <li v-for="a in agents" :key="a.id" class="flex min-w-0 items-center gap-2" :data-agent="a.id">
              <span class="min-w-0 truncate font-medium" :title="a.name">{{ a.name }}</span>
              <UBadge v-if="a.available === false" class="flex-none" label="not installed" :title="notInstalledBadge(a, serverHost.host.value).title" color="warning" variant="subtle" size="sm" :data-not-agent="notInstalledBadge(a, serverHost.host.value).notAgent || undefined" />
              <UBadge v-else-if="identity(a, serverHost.host.value).state !== 'unprobed'" class="flex-none" :label="identity(a, serverHost.host.value).label" :title="identity(a, serverHost.host.value).title" :color="identity(a, serverHost.host.value).state === 'ok' ? 'success' : identity(a, serverHost.host.value).state === 'impostor' ? 'error' : 'neutral'" variant="subtle" size="sm" />
              <code class="min-w-0 flex-1 truncate text-right text-xs text-muted" :title="a.command.join(' ')">{{ a.command.join(' ') }}</code>
            </li>
          </ul>
        </UCard>

        <BrakemanSettings ref="brakemanCard" :class="sectionClass" data-settings-section="brakeman" />

        <AboutCard :versions="versions" :class="sectionClass" data-settings-section="app">
          <USwitch v-model="form.closeToTray" label="Closing the window keeps Conductor in the tray; the server goes on" data-tray-switch />
        </AboutCard>
        </div>

        <DirPickerModal v-if="picker" v-model:open="pickerOpen" :start="picker.start" :title="picker.title" anywhere :roots="form.allowedRoots" :expect="picker.expect" @pick="onPick" />
      </div>
    </template>
    <!-- The save bar is the panel's footer: in reach wherever the page is scrolled (#366). In a browser it saves Brakeman's section (#707). -->
    <template v-if="desktop.isDesktop.value || brakeman.available.value !== false" #footer>
      <div class="flex items-center gap-3 border-t border-default px-4 py-3 sm:px-6" data-settings-save-bar>
        <UButton :label="restarts ? 'Save and restart the server' : 'Save'" icon="i-lucide-save" :loading="saving" :disabled="!dirty || blocked" data-save-settings @click="save" />
        <span v-if="blocked" class="text-xs text-error" data-settings-blocked>Brakeman's settings hold a value it refuses: fix it, where it says, to save.</span>
        <span v-else-if="restarts" class="text-xs text-muted">Changing where or how agents run restarts the server; running sessions end.</span>
      </div>
    </template>
  </UDashboardPanel>
</template>
