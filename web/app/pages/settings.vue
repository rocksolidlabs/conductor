<script setup lang="ts">
import { pickerStart, type PickerField } from '~/utils/dirPicker'
import { restartQuestion, saveRestartQuestion } from '~/utils/actions'
import type { AgentInfo, ReachInfo } from '~/composables/useSessions'
import { identity, notInstalledBadge } from '~/utils/agents'
import { NOT_INSTALLED_BADGE, identityBadge } from '~/utils/agentBadges'
import { PUBLIC_SWITCHYARD, folderProblems, foldersHeld, plainCopy, switchyardProblem, type DesktopIceStatus, type DesktopSettings } from '~/utils/desktop'
import { followSaved, holds, holdsFields, saveParts, saveWords, together, type BrakemanPart, type SaveOutcome } from '~/utils/settingsSave'
import { SETTINGS_FIELD_UI, SETTINGS_SWITCH_UI, changedSections, changedWords, sectionFromQuery, sectionInView, sectionOf, shownSections, type SectionBox, type SettingsAddress, type SettingsSectionId } from '~/utils/settingsSections'

// The desktop app's settings: what its shell starts the server with. In a
// browser against a server configured by its file, the page says so.
const desktop = useDesktop()
const api = useSessions()
const toast = useToast()
const serverHost = useServerHost()

/** The cards' spacing: a title close to its content (#366). */
const cardUi = { header: 'px-4 py-3 sm:px-5', body: 'p-4 sm:p-5' }
const fieldUi = SETTINGS_FIELD_UI
const switchUi = SETTINGS_SWITCH_UI
/** A section's card: scrolled to, it clears the phone's sticky row of links, and on a wide window keeps the body's padding above it. */
const sectionClass = 'scroll-mt-16 sm:scroll-mt-18 lg:scroll-mt-6'
const settings = ref<DesktopSettings | null>(null)
const form = reactive<DesktopSettings>({ dataDir: '', allowedRoots: [], defaultCwd: '', yolo: false, reach: 'auto', closeToTray: true, wslDistro: '', wslWindowsHome: false, switchyardEnabled: true, switchyardServer: '', switchyardToken: '', switchyardName: '' })
const saving = ref(false)
/**
 * The page's fields are held while a save that sends the app's settings is out (holdsFields): the app may answer it by loading the
 * page on the restarted server's port, where nothing typed meanwhile could follow (#707's review).
 */
const holding = ref(false)
const error = ref('')
const reach = ref<ReachInfo | null>(null)
/** Windows: what the app forwards for WebRTC from WSL (null elsewhere), shown as Switchyard's Windows firewall row. */
const ice = ref<DesktopIceStatus | null>(null)
const allowing = ref(false)
/** An Allow that failed: said in the firewall's row, where it was pressed. */
const iceError = ref('')
async function allowFirewall() {
  const b = desktop.bridge.value
  if (!b || !ice.value) return
  allowing.value = true
  iceError.value = ''
  try {
    ice.value = { ...ice.value, firewall: await b.allowIceFirewall() }
  } catch (e) {
    iceError.value = (e as Error).message
  } finally {
    allowing.value = false
  }
}
const agents = ref<AgentInfo[]>([])
const versions = ref<{ app: string; electron: string; node: string; chrome: string; server: string } | null>(null)
/** The folder fields checked as they are picked (UX3 R55): the words at each field, and whether they hold Save. */
const folders = computed(() => folderProblems(form))
function removeRoot(root: string) {
  if (form.allowedRoots.length > 1) form.allowedRoots = form.allowedRoots.filter((r) => r !== root)
}
const reachItems = [
  { label: 'Automatic: map the port on your router', value: 'auto' },
  { label: 'Manual: find the public address only', value: 'manual' },
  { label: 'Off', value: 'off' },
]
/** The Windows-only rows (the WSL distribution, the Windows folders, the firewall). */
const onWindows = computed(() => desktop.bridge.value?.platform === 'win32')
const appDirty = computed(() => !!settings.value && JSON.stringify({ ...settings.value }) !== JSON.stringify({ ...form }))
/** Brakeman's section, saved by the page's one Save with the rest (#707); in a browser, alone. */
const brakemanCard = useTemplateRef<{ dirty: boolean; blocked: boolean; take: () => BrakemanPart | null; discard: () => void }>('brakemanCard')
/** Brakeman's form holds a change the form's own checks refuse: nothing is saved until it is fixed, where it says. */
const brakemanBlocked = computed(() => !!brakemanCard.value?.dirty && !!brakemanCard.value?.blocked)
/** A change that cannot be saved as it is: Brakeman's form refuses a value, or a folder field holds what the app refuses. Said at the field. */
const blocked = computed(() => brakemanBlocked.value || (appDirty.value && foldersHeld(folders.value)))
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

/** A Save refused before anything was sent: the words in the save bar, beside Save (UX3 R56). */
function refuse(out: SaveOutcome) {
  const words = saveWords(out)
  error.value = words.description ?? words.title
}

// The save bar (UX3 R56): what changed, by section, Discard, and Save; a save that restarts while agents run asks first, in the bar.
const live = useAttention()
/** The sessions of this machine running or starting, a shell's too: what a restart of the server stops. */
const running = computed(() => live.sessions.value.filter((s) => s.kind === 'server' && (s.status === 'running' || s.status === 'starting')).length)
const changedIds = computed(() => changedSections(settings.value, form, !!brakemanCard.value?.dirty))
const changed = computed(() => changedWords(changedIds.value))
/** The question a press of Save and restart asks while agents run: in the bar, never a dialog. */
const asking = ref(false)
const question = computed(() => saveRestartQuestion(running.value))
const keepEditingButton = useTemplateRef<{ $el: HTMLElement }>('keepEditingEl')
watch([dirty, restarts], ([d, r]) => {
  if (!d || !r) asking.value = false
})
/** Save pressed in the bar: a save that restarts the server while agents run asks once; otherwise it saves at once. */
function pressSave() {
  if (saving.value || blocked.value || !dirty.value) return
  if (restarts.value && running.value > 0) {
    asking.value = true
    void nextTick(() => keepEditingButton.value?.$el?.focus())
    return
  }
  void save()
}
/** The question's Save and restart: never the second click of a double click whose first asked. */
function confirmSave(e: MouseEvent) {
  if (e.detail > 1) return
  asking.value = false
  void save()
}
function keepEditing() {
  asking.value = false
}

// Conductor's server (UX3 R57): its state and its three buttons in The app; a line at the top only while it does not run.
const serverState = computed(() => desktop.serverState.value)
const serverWord = computed(() => {
  const st = serverState.value?.state
  return st === 'running' ? 'Running' : st === 'starting' ? 'Starting' : st ? 'Stopped' : ''
})
/** Restart server pressed while agents run: the row asks first, Keep it running or Restart server. */
const askingRestart = ref(false)
const restartAsk = computed(() => restartQuestion(running.value))
const keepRunningButton = useTemplateRef<{ $el: HTMLElement }>('keepRunningEl')
function pressRestart() {
  if (running.value > 0) {
    askingRestart.value = true
    void nextTick(() => keepRunningButton.value?.$el?.focus())
    return
  }
  void desktop.bridge.value?.restartServer()
}
function confirmRestart(e: MouseEvent) {
  if (e.detail > 1) return
  askingRestart.value = false
  void desktop.bridge.value?.restartServer()
}
/** Discard: the app's fields back to what is saved, and Brakeman's form to its saved configuration (a stale choice that waits is kept). */
function discard() {
  if (settings.value) followSaved(form, null, settings.value)
  brakemanCard.value?.discard()
  error.value = ''
  asking.value = false
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
  holding.value = holdsFields(sent)
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
    // A refusal is said in the bar, beside Save, and the edits stay (UX3 R56).
    else error.value = words.description ?? words.title
    if (out.app === 'saved' || out.app === 'restarted') await load()
  } finally {
    saving.value = false
    holding.value = false
  }
}

// The folder picker browses the server's own folders (on Windows, the WSL distribution's), never the native dialog.
const picker = ref<{ field: PickerField; start: string; title: string; expect?: 'under-roots' | 'outside-roots' } | null>(null)
const pickerOpen = ref(false)
const pickerTitles: Record<PickerField, string> = { root: 'Add a folder agents can use', defaultCwd: 'Default folder', dataDir: 'Where Conductor keeps its data' }
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
const accounts = useAccounts()
const github = useTracker()
const sections = computed(() =>
  shownSections({ desktop: desktop.isDesktop.value, platform: desktop.bridge.value?.platform, agents: agents.value.length, brakeman: brakeman.available.value !== false, accounts: accounts.available.value !== false, github: github.available.value !== false }),
)
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
let kept: { id: SettingsAddress | null } | null = null
const body = () => layout.value?.closest<HTMLElement>('[data-slot="body"]') ?? null
const sectionEl = (id: SettingsAddress) => layout.value?.querySelector<HTMLElement>(`[data-settings-section="${id}"]`) ?? null
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
let pinned: SettingsAddress | null = null
const unpin = () => {
  pinned = null
  chosen = null
}
/** Puts a section's top (or a row's that has an address) at the top of the body, less its scroll margin (which clears the phone's row of links). The body alone scrolls. */
function align(id: SettingsAddress): boolean {
  const b = body()
  const el = sectionEl(id)
  if (!b || !el) return false
  const margin = parseFloat(getComputedStyle(el).scrollMarginTop) || 0
  b.scrollTop += el.getBoundingClientRect().top - b.getBoundingClientRect().top - margin
  return true
}
/** Brings a section, or a row with an address, into view, its section marked; none, or one not shown, opens at the top. */
function scrollToSection(id: SettingsAddress | null) {
  const shown = id ? align(id) : false
  chosen = shown && id ? sectionOf(id) : null
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
usePalette().registerPage(() => {
  if (!desktop.isDesktop.value) return null
  const n = running.value
  return {
    label: 'Settings',
    rows: [
      // Asked only while agents run, in the bar's words; a save confirmed here is not asked again by the bar.
      { key: 'page:save', label: restarts.value ? 'Save and restart' : 'Save', icon: 'i-lucide-save', disabled: !dirty.value || saving.value || blocked.value, ask: restarts.value && n ? saveRestartQuestion(n) : undefined, run: save },
      ...(desktop.isDesktop.value
        ? [
            { key: 'page:restart', label: 'Restart server', icon: 'i-lucide-rotate-ccw', ask: n ? restartQuestion(n) : undefined, run: () => void desktop.bridge.value?.restartServer() },
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
          <!-- The server's buttons are The app's (UX3 R57). -->
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <!--
        The sections stay one column of cards that reads top to bottom (#366: two columns left a blank beside every short card), as
        wide as the panel up to 80rem (#433). In a browser there is no navigation: the notice, Accounts, GitHub, Brakeman and The app, stacked.
      -->
      <div v-if="!desktop.isDesktop.value" class="@container mx-auto flex w-full max-w-7xl flex-col gap-5" data-settings-sections>
        <UAlert color="neutral" variant="subtle" icon="i-lucide-info" title="This server is configured by its file" description="These settings belong to the desktop app, which starts a server of its own. A server you run yourself takes its configuration file and CONDUCTOR_* variables (see the README)." />
        <!-- The accounts and Brakeman's configuration are the server's own, changed through its routes: here in a browser too. -->
        <AccountsCard data-settings-section="accounts" />
        <!-- The GitHub token is the server's too; its Check and save is its own, never part of the save bar's Save. -->
        <SettingsGitHub data-settings-section="github" />
        <BrakemanSettings ref="brakemanCard" data-settings-section="brakeman" />
        <SignInCard />
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
        <!-- A fieldset, so a save that may move the page holds every field of the sections at once (holding). -->
        <fieldset :disabled="holding" class="@container m-0 flex min-w-0 flex-1 flex-col gap-5 border-0 p-0" data-settings-sections>
        <!-- Only while Conductor's server does not run: what happened, with its log and a restart (UX3 R57). -->
        <div v-if="serverState && serverState.state !== 'running'" class="flex flex-wrap items-center gap-3 rounded-md border border-default px-4 py-3" :data-server-state="serverState.state">
          <p class="min-w-0 flex-1 text-sm" :class="serverState.state === 'starting' ? 'text-muted' : 'text-error'">
            {{ serverState.state === 'starting' ? "Conductor's server is starting." : `Conductor's server stopped${serverState.lastError ? `: ${serverState.lastError.replace(/\.$/, '')}` : ''}.` }}
          </p>
          <UButton label="Server log" icon="i-lucide-scroll-text" size="sm" color="neutral" variant="outline" data-server-state-log @click="desktop.bridge.value?.showLog()" />
          <UButton label="Restart server" icon="i-lucide-rotate-ccw" size="sm" color="neutral" variant="outline" data-server-state-restart @click="pressRestart" />
        </div>
        <UCard :ui="cardUi" :class="sectionClass" data-settings-section="folders">
          <template #header><SettingsCardTitle title="Where agents work" description="The folders agents may use." /></template>
          <div class="flex flex-col gap-4">
            <p v-if="onWindows" class="text-sm text-muted" data-wsl-paths>
              These are paths inside your WSL distribution, such as <code class="font-mono">/home/&lt;user&gt;/code</code>; the folder picker browses the distribution. Windows folders are under <code class="font-mono">/mnt</code>.
            </p>
            <!-- One chip a folder, wrapping and never scrolling sideways; the last cannot be taken away (UX3 R55). -->
            <UFormField label="Folders agents can use" description="Agents can read and change anything inside." :error="folders.roots" :ui="fieldUi" data-allowed-roots>
              <ul class="flex flex-wrap items-center gap-2">
                <li v-for="r in form.allowedRoots" :key="r" class="flex min-w-0 max-w-full items-center gap-0.5 rounded-md border border-default bg-elevated/40 ps-2" :data-root-chip="r">
                  <span class="min-w-0 font-mono text-xs [overflow-wrap:anywhere]" :title="r" data-mono="path">{{ r }}</span>
                  <span :title="form.allowedRoots.length > 1 ? undefined : 'Agents need at least one folder.'" class="flex-none">
                    <IconButton :label="`Remove ${r}`" icon="i-lucide-x" size="xs" color="neutral" variant="ghost" :disabled="form.allowedRoots.length <= 1" data-root-remove @click="removeRoot(r)" />
                  </span>
                </li>
                <li class="flex-none">
                  <UButton label="Add folder" icon="i-lucide-plus" size="sm" color="neutral" variant="outline" data-pick-dir="root" @click="openPicker('root')" />
                </li>
              </ul>
            </UFormField>
            <UFormField v-if="onWindows" label="Also allow your Windows folders" description="Your Windows profile under /mnt. Slower." :ui="switchUi">
              <USwitch v-model="form.wslWindowsHome" aria-label="Also allow your Windows folders" data-wsl-windows-home />
            </UFormField>
            <UFormField label="Default folder" description="Where a launch runs when you pick no folder." :error="folders.defaultCwd" :ui="fieldUi" data-default-folder>
              <div class="flex gap-2"><UInput v-model="form.defaultCwd" class="min-w-0 flex-1 font-mono text-xs" /><IconButton icon="i-lucide-folder" color="neutral" variant="outline" label="Pick the default folder" data-pick-dir="defaultCwd" @click="openPicker('defaultCwd')" /></div>
            </UFormField>
            <UFormField label="Where Conductor keeps its data" description="Its catalog, crews and certificates. Keep it outside the folders agents can use." :help="folders.dataDir" :ui="{ ...fieldUi, help: 'text-warning' }" data-data-folder>
              <div class="flex gap-2"><UInput v-model="form.dataDir" class="min-w-0 flex-1 font-mono text-xs" /><IconButton icon="i-lucide-folder" color="neutral" variant="outline" label="Pick where Conductor keeps its data" data-pick-dir="dataDir" @click="openPicker('dataDir')" /></div>
            </UFormField>
          </div>
        </UCard>

        <UCard :ui="cardUi" :class="sectionClass" data-settings-section="running">
          <template #header><SettingsCardTitle title="How agents run" description="Whether agents ask before they act." /></template>
          <div class="flex flex-col gap-4">
            <UFormField label="Yolo" description="Skips permission prompts: agents run commands and change files without asking you. The default for new launches. Changing it restarts the server." :ui="switchUi">
              <USwitch v-model="form.yolo" aria-label="Yolo" data-yolo-switch />
            </UFormField>
            <UFormField v-if="onWindows" label="WSL distribution" description="Where Conductor runs. Empty for the default." :ui="fieldUi">
              <UInput v-model="form.wslDistro" class="w-full font-mono text-xs" placeholder="Ubuntu" />
            </UFormField>
          </div>
        </UCard>

        <UCard v-if="agents.length" :ui="cardUi" :class="sectionClass" data-settings-section="agents">
          <template #header>
            <div class="flex items-start gap-3">
              <SettingsCardTitle class="min-w-0 flex-1" title="Agents on this machine" description="What Conductor finds on this machine's PATH. Restart the server after you change your PATH." />
              <UButton label="Open Agents" to="/agents" icon="i-lucide-bot" size="xs" color="neutral" variant="outline" class="flex-none" data-settings-agents-link />
            </div>
          </template>
          <!-- Two columns once the column has room, so a long catalog stays within the visible body (#433). The badges are drawn as on the Agents page (utils/agentBadges.ts). -->
          <ul class="grid gap-x-8 gap-y-1.5 text-sm @3xl:grid-cols-2">
            <li v-for="a in agents" :key="a.id" class="flex min-w-0 items-center gap-2" :data-agent="a.id">
              <span class="min-w-0 truncate font-medium" :title="a.name">{{ a.name }}</span>
              <UBadge v-if="a.available === false" class="flex-none" label="not installed" :title="notInstalledBadge(a, serverHost.host.value).title" :icon="NOT_INSTALLED_BADGE.icon" :color="NOT_INSTALLED_BADGE.color" variant="subtle" size="sm" data-not-installed :data-not-agent="notInstalledBadge(a, serverHost.host.value).notAgent || undefined" />
              <UBadge
                v-else-if="identityBadge(identity(a, serverHost.host.value).state)"
                class="flex-none"
                :label="identity(a, serverHost.host.value).label"
                :title="identity(a, serverHost.host.value).title"
                :icon="identityBadge(identity(a, serverHost.host.value).state)!.icon"
                :color="identityBadge(identity(a, serverHost.host.value).state)!.color"
                variant="subtle"
                size="sm"
                :data-identity="identity(a, serverHost.host.value).state"
              />
              <code class="min-w-0 flex-1 truncate text-right text-xs text-muted" :title="a.command.join(' ')">{{ a.command.join(' ') }}</code>
            </li>
          </ul>
        </UCard>

        <AccountsCard :class="sectionClass" data-settings-section="accounts" />

        <UCard :ui="cardUi" :class="sectionClass" data-settings-section="switchyard" data-switchyard-settings>
          <template #header><SettingsCardTitle title="Switchyard" description="How a share link reaches this machine." /></template>
          <div class="flex flex-col gap-4">
            <!-- What the switchyard is told and does: a statement about links, every fact of it kept (UX3 R54). -->
            <UFormField
              label="Share links work from anywhere"
              description="Through a switchyard. It is told each session's name, agent and folder name, this machine's name, and whether it waits on you; never its chat, activity or command line. It mints and checks the links and decides who joins. What a viewer sees goes straight to them, or through its relay, which can read it, when no direct path forms. Publish only to a switchyard whose operator you trust."
              :ui="switchUi"
            >
              <USwitch v-model="form.switchyardEnabled" aria-label="Share links work from anywhere" data-switchyard-enabled />
            </UFormField>
            <UFormField label="Switchyard address" :description="`Empty for the public one, ${PUBLIC_SWITCHYARD}.`" :error="switchyardError || undefined" :ui="fieldUi" data-switchyard-server>
              <UInput v-model="form.switchyardServer" class="w-full font-mono text-xs" :placeholder="`https://${PUBLIC_SWITCHYARD}`" :disabled="!form.switchyardEnabled" />
            </UFormField>
            <UFormField label="Host token" description="Only for a private switchyard, or a trusted seat on the public one." :ui="fieldUi">
              <UInput v-model="form.switchyardToken" type="password" class="w-full font-mono text-xs" autocomplete="off" :disabled="!form.switchyardEnabled" />
            </UFormField>
            <UFormField label="This machine's name there" description="Its host name when empty." :ui="fieldUi">
              <UInput v-model="form.switchyardName" class="w-full" maxlength="64" :disabled="!form.switchyardEnabled" />
            </UFormField>
            <UFormField label="Reach from outside your network" description="How Conductor finds out it can be reached from outside your network. A link uses the public address only once it has a certificate." :ui="fieldUi">
              <USelect v-model="form.reach" :items="reachItems" class="w-full" aria-label="Reach from outside your network" />
              <p v-if="reach && (reach.externalIp || reach.mapped || reach.tls || reach.error)" class="mt-1.5 text-xs text-muted" data-reach-state>
                <template v-if="reach.externalIp">Public address <code data-mono="id">{{ reach.externalIp }}</code></template><template v-if="reach.mapped"> · port <code data-mono="id">{{ reach.externalPort }}</code> mapped via {{ reach.method }}</template><template v-if="reach.tls"> · certificate {{ reach.tls.ready ? 'ready' : 'pending' }}</template><template v-if="reach.error"> · {{ reach.error }}</template>
              </p>
            </UFormField>
            <!-- WebRTC from WSL, a section before UX3 R54: its address (?section=webrtc) lands on this row. -->
            <div v-if="ice" :class="sectionClass" data-settings-section="webrtc" data-ice-status>
              <UFormField label="Windows firewall" description="Lets viewers connect straight to agents in WSL." :ui="fieldUi">
                <div v-if="ice.forwarding" class="flex flex-wrap items-center gap-2">
                  <UBadge :label="ice.firewall === 'present' ? 'rule present' : ice.firewall === 'missing' ? 'rule missing' : 'rule unknown'" :color="ice.firewall === 'missing' ? 'warning' : 'neutral'" variant="subtle" data-ice-firewall />
                  <UButton v-if="ice.firewall !== 'present'" label="Allow" icon="i-lucide-shield-check" size="sm" color="neutral" variant="outline" :loading="allowing" data-ice-allow @click="allowFirewall" />
                  <span v-if="ice.firewall !== 'present'" class="text-xs text-muted">Windows asks once for permission.</span>
                </div>
                <p v-else class="text-sm text-muted">Nothing is forwarded{{ ice.reason ? `: ${ice.reason}` : '' }}.</p>
                <p v-if="iceError" class="mt-1.5 text-xs text-error" role="alert" data-ice-error>{{ iceError }}</p>
              </UFormField>
            </div>
          </div>
        </UCard>

        <SettingsGitHub :class="sectionClass" data-settings-section="github" />

        <BrakemanSettings ref="brakemanCard" :class="sectionClass" data-settings-section="brakeman" />

        <SignInCard :class="sectionClass" />
        <AboutCard :versions="versions" :class="sectionClass" data-settings-section="app">
          <USwitch v-model="form.closeToTray" label="Closing the window keeps Conductor in the tray; the server goes on" data-tray-switch />
          <template #server>
            <div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-default pt-3.5" data-server-row :data-server-asking="askingRestart || undefined" @keydown.esc="askingRestart = false">
              <p v-if="askingRestart" class="min-w-0 flex-1 text-sm text-highlighted" role="status" data-restart-question>{{ restartAsk.title }} {{ restartAsk.detail }}</p>
              <p v-else class="min-w-0 flex-1 text-sm break-words">
                <span class="font-medium text-highlighted">Conductor's server</span> · <span data-server-word>{{ serverWord }}</span><template v-if="serverState?.url && serverState.state === 'running'"> at <code class="text-xs" data-server-url>{{ serverState.url }}</code></template>
              </p>
              <template v-if="askingRestart">
                <UButton ref="keepRunningEl" :label="restartAsk.keep" size="sm" color="neutral" variant="ghost" data-restart-keep @click="askingRestart = false" />
                <UButton :label="restartAsk.confirm" icon="i-lucide-rotate-ccw" size="sm" color="neutral" variant="outline" data-restart-confirm @click="confirmRestart" />
              </template>
              <template v-else>
                <UButton label="Server log" icon="i-lucide-scroll-text" size="sm" color="neutral" variant="outline" data-server-log @click="desktop.bridge.value?.showLog()" />
                <UButton label="Open in browser" icon="i-lucide-external-link" size="sm" color="neutral" variant="outline" data-open-in-browser @click="desktop.bridge.value?.openInBrowser()" />
                <UButton label="Restart server" icon="i-lucide-rotate-ccw" size="sm" color="neutral" variant="outline" data-restart-server @click="pressRestart" />
              </template>
            </div>
          </template>
        </AboutCard>
        </fieldset>

        <DirPickerModal v-if="picker" v-model:open="pickerOpen" :start="picker.start" :title="picker.title" anywhere :roots="form.allowedRoots" :expect="picker.expect" @pick="onPick" />
      </div>
    </template>
    <!-- The save bar is the panel's footer: in reach wherever the page is scrolled (#366). In a browser it saves Brakeman's section (#707).
         Save at its right end, its note in the room left of it, wrapping there, so a phone never pushes the button out of view (#1154). -->
    <template v-if="desktop.isDesktop.value || brakeman.available.value !== false" #footer>
      <!--
        Left to right: the bar's words, Discard, Save (UX3 R56). The words say what changed, by section, and what a save costs; a refusal in
        the error tone, beside Save, announced. While agents run, Save and restart asks here first: Keep editing, or Save and restart.
      -->
      <div class="flex items-center gap-3 border-t border-default px-4 py-3 sm:px-6" data-settings-save-bar :data-settings-asking="asking || undefined" @keydown.esc="keepEditing">
        <template v-if="asking">
          <span class="min-w-0 flex-1 text-sm text-highlighted" role="status" data-settings-question>{{ question.title }} {{ question.detail }}</span>
          <UButton ref="keepEditingEl" class="shrink-0" :label="question.keep" color="neutral" variant="ghost" data-settings-keep-editing @click="keepEditing" />
          <UButton class="shrink-0" :label="question.confirm" icon="i-lucide-save" :loading="saving" data-save-settings data-settings-confirm @click="confirmSave" />
        </template>
        <template v-else>
          <span class="min-w-0 flex-1 text-xs" aria-live="polite">
            <span v-if="error" class="text-error" role="alert" data-settings-error>{{ error }}</span>
            <span v-else-if="blocked" class="text-error" data-settings-blocked>{{ changed ? `${changed} ` : '' }}Fix the marked field to save.</span>
            <span v-else-if="restarts && dirty" class="text-muted" data-settings-changed>{{ changed }} Saving restarts the server.</span>
            <span v-else-if="dirty" class="text-muted" data-settings-changed>{{ changed }}</span>
          </span>
          <UButton v-if="dirty" class="shrink-0" label="Discard" color="neutral" variant="ghost" :disabled="saving" data-settings-discard @click="discard" />
          <UButton class="shrink-0" :label="restarts ? 'Save and restart' : 'Save'" icon="i-lucide-save" :loading="saving" :disabled="!dirty || blocked" data-save-settings @click="pressSave" />
        </template>
      </div>
    </template>
  </UDashboardPanel>
</template>
