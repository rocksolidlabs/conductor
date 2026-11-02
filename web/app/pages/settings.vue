<script setup lang="ts">
import { pickerStart, type PickerField } from '~/utils/dirPicker'
import type { AgentInfo, ReachInfo } from '~/composables/useSessions'
import { identity, notInstalledBadge } from '~/utils/agents'
import { plainCopy, switchyardProblem, type DesktopIceStatus, type DesktopSettings } from '~/utils/desktop'

// The desktop app's settings: what its shell starts the server with. In a
// browser against a server configured by its file, the page says so.
const desktop = useDesktop()
const api = useSessions()
const toast = useToast()
const serverHost = useServerHost()

/** The cards' spacing: a title close to its content (#366). */
const cardUi = { header: 'px-4 py-3 sm:px-5', body: 'p-4 sm:p-5' }
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
const dirty = computed(() => !!settings.value && JSON.stringify({ ...settings.value }) !== JSON.stringify({ ...form }))
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
    settings.value = s
    Object.assign(form, s)
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

async function save() {
  const b = desktop.bridge.value
  if (!b) return
  error.value = ''
  switchyardChecked.value = switchyardKey()
  const refused = switchyardProblem(form)
  if (refused) {
    error.value = refused
    return
  }
  saving.value = true
  // What this page can tell on its own, for an app that cannot say what became
  // of the save (older apps): taken before the save, since once the saved
  // settings are copied into the form nothing differs any more (#214).
  const restarting = restarts.value
  try {
    // A plain copy: the form is reactive, and a Vue proxy cannot cross the
    // bridge ("An object could not be cloned", #348).
    const next = await b.settings.set(plainCopy(form))
    settings.value = next
    Object.assign(form, next)
    // The app says what became of it: the server restarted, or the settings
    // were saved alone (a retry after a restart that failed changes nothing the
    // server reads, since the settings were kept).
    const told = await b.settings.outcome?.().catch(() => undefined)
    savedToast(told === undefined ? (restarting ? 'restarted' : 'saved') : told)
    await load()
  } catch (e) {
    error.value = (e as Error).message
    // The alert sits at the top of a long page: say it where the button is too (#348).
    toast.add({ title: 'Not saved', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
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

onMounted(async () => {
  // A restarted server listens on a fresh port and the app loads this page
  // there: the page that saved is gone before its save resolved, and the
  // save's outcome is owed to this one (desktop/src/save.ts). Asked first,
  // before the form can be saved from this page: the app answers an ask made
  // while a restart is pending with nothing, so this ask never takes what a
  // later save of this page owes to the page that follows it.
  const owed = desktop.bridge.value?.settings.outcome?.().catch(() => '')
  await load()
  if ((await owed) === 'restarted') savedToast('restarted')
})

// This page's group in the command palette (#302): Save, and the desktop app's buttons. Only in the desktop app (#332): the
// browser's page is the server's file and a notice, with nothing to save, so it offers no row.
const live = useAttention()
usePalette().registerPage(() => {
  if (!desktop.isDesktop.value) return null
  const n = live.sessions.value.filter((s) => s.kind === 'server' && (s.status === 'running' || s.status === 'starting')).length
  return {
    label: 'Settings',
    rows: [
      { key: 'page:save', label: restarts.value ? 'Save and restart the server' : 'Save', icon: 'i-lucide-save', disabled: !dirty.value || saving.value, ask: restarts.value ? { title: 'Save and restart the server?', detail: n ? `${n} ${n === 1 ? 'session ends' : 'sessions end'} with it.` : 'No session is running.' } : undefined, run: save },
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
          <template v-if="desktop.isDesktop.value">
            <UButton label="Open in browser" icon="i-lucide-external-link" color="neutral" variant="outline" data-open-in-browser @click="desktop.bridge.value?.openInBrowser()" />
            <UButton label="Server log" icon="i-lucide-scroll-text" color="neutral" variant="outline" @click="desktop.bridge.value?.showLog()" />
            <UButton label="Restart server" icon="i-lucide-rotate-ccw" color="neutral" variant="outline" @click="desktop.bridge.value?.restartServer()" />
          </template>
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <!-- One column that reads top to bottom (#366): two left a blank beside every short card. -->
      <div v-if="!desktop.isDesktop.value" class="mx-auto flex w-full max-w-3xl flex-col gap-5">
        <UAlert color="neutral" variant="subtle" icon="i-lucide-info" title="This server is configured by its file" description="These settings belong to the desktop app, which starts a server of its own. A server you run yourself takes its configuration file and CONDUCTOR_* variables (see the README)." />
        <!-- Brakeman's configuration is the server's own, saved through its route: here in a browser too. -->
        <BrakemanSettings />
        <AboutCard />
      </div>
      <div v-else class="mx-auto flex w-full max-w-3xl flex-col gap-5" data-desktop-settings>
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
        <div class="flex flex-col gap-5" data-settings-grid>
        <UCard v-if="ice" :ui="cardUi" data-ice-status>
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

        <UCard :ui="cardUi" data-switchyard-settings>
          <template #header><SettingsCardTitle title="Switchyard" description="Share links that work from anywhere, through a switchyard." /></template>
          <div class="flex flex-col gap-4">
            <USwitch v-model="form.switchyardEnabled" label="Publish sessions to a switchyard" description="So a share link works from anywhere." data-switchyard-enabled />
            <p class="text-sm text-muted">
              Every session here is published to a switchyard, switchyard.rslabs.net unless you name another. It is told each session's name, agent and folder name, this machine's name, and whether the session waits on you; never its chat, activity or command line. It mints and checks the links and decides who joins, and the terminal goes between the viewer and this machine over WebRTC, or through the switchyard's relay, which can read it, when no direct path forms. Publish only to a switchyard whose operator you trust. Off, links work where this machine is reachable.
            </p>
            <UFormField label="Switchyard" description="Leave empty for the public one. https://, or http:// only for one on this machine (localhost)." :error="switchyardError || undefined" data-switchyard-server>
              <UInput v-model="form.switchyardServer" class="w-full font-mono text-xs" placeholder="https://switchyard.rslabs.net" :disabled="!form.switchyardEnabled" />
            </UFormField>
            <UFormField label="Host token" description="Optional: a host token for a private switchyard, or a trusted seat on the public one, outside its per-address limits.">
              <UInput v-model="form.switchyardToken" type="password" class="w-full font-mono text-xs" autocomplete="off" :disabled="!form.switchyardEnabled" />
            </UFormField>
            <UFormField label="Shown there as" description="How this machine is named at the switchyard; its host name when empty.">
              <UInput v-model="form.switchyardName" class="w-full" maxlength="64" :disabled="!form.switchyardEnabled" />
            </UFormField>
          </div>
        </UCard>

        <UCard :ui="cardUi">
          <template #header><SettingsCardTitle title="Where agents work" description="The folders sessions may run in, and where the server keeps its data." /></template>
          <div class="flex flex-col gap-4">
            <p v-if="desktop.bridge.value?.platform === 'win32'" class="text-sm text-muted" data-wsl-paths>
              The server runs inside your WSL distribution, so these are its paths (such as <code class="font-mono">/home/&lt;user&gt;/code</code>); the folder picker browses the distribution. Windows folders need the switch under "How agents run" and live under <code class="font-mono">/mnt</code>.
            </p>
            <UFormField label="Allowed roots" description="Directories server sessions may run in, one per line. The agents can read and change everything under them.">
              <div class="flex gap-2">
                <UTextarea v-model="rootsText" :rows="3" class="flex-1 font-mono text-xs" />
                <UButton icon="i-lucide-folder-plus" color="neutral" variant="outline" aria-label="Add a root" data-pick-dir="root" @click="openPicker('root')" />
              </div>
            </UFormField>
            <UFormField label="Default working directory" description="Where a launch runs when it names no directory; under an allowed root.">
              <div class="flex gap-2"><UInput v-model="form.defaultCwd" class="flex-1 font-mono text-xs" /><UButton icon="i-lucide-folder" color="neutral" variant="outline" aria-label="Pick the default working directory" data-pick-dir="defaultCwd" @click="openPicker('defaultCwd')" /></div>
            </UFormField>
            <UFormField label="Data directory" description="The server's catalog, crews, hooks and certificates. Keep it outside the allowed roots.">
              <div class="flex gap-2"><UInput v-model="form.dataDir" class="flex-1 font-mono text-xs" /><UButton icon="i-lucide-folder" color="neutral" variant="outline" aria-label="Pick the data directory" data-pick-dir="dataDir" @click="openPicker('dataDir')" /></div>
            </UFormField>
          </div>
        </UCard>

        <UCard :ui="cardUi">
          <template #header><SettingsCardTitle title="How agents run" description="Permission prompts, reach from outside your network, and the WSL distribution." /></template>
          <div class="flex flex-col gap-4">
            <UFormField label="Yolo" description="Launch every agent with its yolo recipe, skipping its permission prompts (Codex's drops its sandbox). Only for roots you would let an agent change unasked.">
              <USwitch v-model="form.yolo" label="Skip permission prompts" data-yolo-switch />
            </UFormField>
            <UFormField label="Reach" description="How the server finds out it can be reached from outside your network. Links become public only once a TLS listener has a certificate.">
              <USelect v-model="form.reach" :items="reachItems" class="w-full" />
            </UFormField>
            <div v-if="reach" class="rounded-md bg-elevated px-3 py-2 text-xs font-mono" data-reach-state>
              reach {{ reach.mode }}<template v-if="reach.externalIp"> · public address {{ reach.externalIp }}</template><template v-if="reach.mapped"> · port {{ reach.externalPort }} mapped via {{ reach.method }}</template><template v-if="reach.tls"> · certificate {{ reach.tls.ready ? 'ready' : 'pending' }}</template><template v-if="reach.error"> · {{ reach.error }}</template>
            </div>
            <UFormField v-if="desktop.bridge.value?.platform === 'win32'" label="WSL distribution" description="The distribution the server runs in; empty for the default. Agents are found on its PATH.">
              <UInput v-model="form.wslDistro" class="w-full font-mono text-xs" placeholder="Ubuntu" />
            </UFormField>
            <UFormField v-if="desktop.bridge.value?.platform === 'win32'" label="Windows folders">
              <USwitch v-model="form.wslWindowsHome" label="Also allow your Windows profile under /mnt (slower)" />
            </UFormField>
          </div>
        </UCard>

        <UCard v-if="agents.length" :ui="cardUi">
          <template #header><SettingsCardTitle title="Agents on this machine" description="What the server finds on its PATH; a restart reads the PATH again." /></template>
          <ul class="flex flex-col gap-1.5 text-sm">
            <li v-for="a in agents" :key="a.id" class="flex items-center gap-2" :data-agent="a.id">
              <span class="font-medium">{{ a.name }}</span>
              <UBadge v-if="a.available === false" label="not installed" :title="notInstalledBadge(a, serverHost.host.value).title" color="warning" variant="subtle" size="sm" :data-not-agent="notInstalledBadge(a, serverHost.host.value).notAgent || undefined" />
              <UBadge v-else-if="identity(a, serverHost.host.value).state !== 'unprobed'" :label="identity(a, serverHost.host.value).label" :title="identity(a, serverHost.host.value).title" :color="identity(a, serverHost.host.value).state === 'ok' ? 'success' : identity(a, serverHost.host.value).state === 'impostor' ? 'error' : 'neutral'" variant="subtle" size="sm" />
              <code class="ml-auto text-xs text-muted truncate">{{ a.command.join(' ') }}</code>
            </li>
          </ul>
        </UCard>

        <BrakemanSettings />

        <AboutCard :versions="versions">
          <USwitch v-model="form.closeToTray" label="Closing the window keeps Conductor in the tray; the server goes on" data-tray-switch />
        </AboutCard>
        </div>

        <DirPickerModal v-if="picker" v-model:open="pickerOpen" :start="picker.start" :title="picker.title" anywhere :roots="form.allowedRoots" :expect="picker.expect" @pick="onPick" />
      </div>
    </template>
    <!-- The save bar is the panel's footer: in reach wherever the page is scrolled (#366). -->
    <template v-if="desktop.isDesktop.value" #footer>
      <div class="flex items-center gap-3 border-t border-default px-4 py-3 sm:px-6" data-settings-save-bar>
        <UButton :label="restarts ? 'Save and restart the server' : 'Save'" icon="i-lucide-save" :loading="saving" :disabled="!dirty" data-save-settings @click="save" />
        <span v-if="restarts" class="text-xs text-muted">Changing where or how agents run restarts the server; running sessions end.</span>
      </div>
    </template>
  </UDashboardPanel>
</template>
