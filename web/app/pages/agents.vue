<script setup lang="ts">
import type { AgentInfo, Integration } from '~/composables/useSessions'
import { joinArgv } from '~/utils/argv'
import { reportStatus } from '~/utils/integrations'
import { agentIcon } from '~/utils/agentIcons'
import { checkBadge, identity, isAvailable, notInstalledBadge, pageRequests, settleChecks } from '~/utils/agents'
import { IDENTITY_COLOR as identityColor, IDENTITY_ICON as identityIcon, NOT_INSTALLED_BADGE } from '~/utils/agentBadges'

// The badges' colours and icons: one table with Settings' Agents on this machine (utils/agentBadges.ts).
import { removalOf, removalText } from '~/utils/catalog'
import { pageState } from '~/utils/pageState'

useHead({ title: 'Agents' })

const api = useSessions()
const admin = useWorkbenchToken()
const toast = useToast()
const agents = ref<AgentInfo[]>([])
/** IDs the server hides from the catalog; each can be restored. */
const hidden = ref<string[]>([])
const loading = ref(false)
const error = ref('')
/** Whether the catalog has answered once. */
const loaded = ref(false)
/** What the page shows of the catalog (P23); with an agent listed, its standing paragraph folds to About this page. */
const shownState = computed(() => pageState({ read: loaded.value, error: error.value, count: agents.value.length }))
const launch = useLaunchModal()
const serverHost = useServerHost()

// Each read of the list is a generation: the checks a read started filling
// in are aborted once a newer one (or leaving the page) supersedes it. Once
// the page is left, no request of it still under way (a hide, a restore, a
// read) starts a read or changes the page when it answers.
const requests = pageRequests()
/** The agents whose check the page gave up reading (#704): their reads ran out or one failed. */
const unanswered = ref<ReadonlySet<string>>(new Set())
const check = (a: AgentInfo) => checkBadge(a, serverHost.host.value, unanswered.value.has(a.id))

// Whether each agent reports to Conductor: the hook adapters as GET /api/integrations lists them, null until read (a card draws no
// line it does not know). Each read supersedes the one before it.
const integrations = ref<Integration[] | null>(null)
const integrationsHost = ref('')
const integrationsError = ref('')
let integrationsRead = 0

async function readIntegrations() {
  const mine = ++integrationsRead
  try {
    const r = await requests.live(api.integrations())
    if (mine !== integrationsRead) return
    integrations.value = r.integrations
    integrationsHost.value = r.host
    integrationsError.value = ''
  } catch (e) {
    if (mine === integrationsRead) integrationsError.value = (e as Error).message
  }
}

/** A card's line: whether its agent reports to Conductor, by the integration of its adapter (reportStatus, the one rule). */
function reportOf(a: AgentInfo) {
  return reportStatus(a.adapter ? integrations.value?.find((i) => i.id === a.adapter) : undefined)
}

// A link to the section or one of its rows (#reporting, #reporting-<adapter>) is shown once the rows are drawn, and again when a
// card's Set up is pressed on the hash already in the URL; a later read of the integrations (an install, Refresh) leaves the
// scroll where the person put it.
const route = useRoute()
let shownHash = ''
function showHash(hash: string) {
  void nextTick(() => document.getElementById(hash.slice(1))?.scrollIntoView({ block: 'start' }))
}
watch(
  [() => route.hash, integrations],
  ([hash, list]) => {
    if (!hash.startsWith('#reporting') || !list || hash === shownHash) return
    shownHash = hash
    showHash(hash)
  },
  { flush: 'post' },
)

/**
 * Draws the list from the server's answer of the moment, which waits for
 * nothing (#704), then fills in each agent the server is still checking (its
 * program looked up, its version flag run), or showed from an answer that
 * has expired, as its own read answers. Whether each agent reports is read
 * beside it.
 */
async function refresh() {
  if (requests.left()) return
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  const gen = requests.next()
  const stop = () => gen.aborted
  loading.value = true
  const reporting = readIntegrations()
  try {
    const r = await requests.live(api.catalogNow())
    if (stop()) return
    agents.value = r.agents
    hidden.value = r.hidden
    loaded.value = true
    unanswered.value = new Set()
    error.value = ''
    serverHost.load()
    void settleChecks(
      r.agents.filter((a) => a.checking || a.recheck).map((a) => a.id),
      api.catalogAgent,
      (a) => (agents.value = agents.value.map((x) => (x.id === a.id ? a : x))),
      { signal: gen, gaveUp: (id) => (unanswered.value = new Set([...unanswered.value, id])) },
    )
  } catch (e) {
    if (!stop()) error.value = (e as Error).message
  } finally {
    // The cards are drawn as soon as the catalog answers; Refresh spins until both reads have.
    await reporting
    if (!stop()) loading.value = false
  }
}

// The hooks not installed yet, from the palette: the one install of their adapter, then the integrations read again.
async function installHooks(i: Integration) {
  try {
    const changed = await requests.live(api.installIntegration(i.id))
    toast.add({ title: changed.length ? `Installed ${i.name} hooks` : 'Nothing to change', description: changed.length ? changed.join('\n') : `${i.name} already has Conductor's hooks.`, icon: 'i-lucide-download', color: changed.length ? 'success' : 'neutral' })
  } catch (e) {
    toast.add({ title: `Installing ${i.name} failed`, description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    await readIntegrations()
  }
}

onMounted(refresh)
onBeforeUnmount(requests.end)
watch(() => admin.token.value, refresh)

// Add, edit and clone share one slideover; `editing` is the agent being
// changed, `cloning` the one copied (#291).
const formOpen = ref(false)
const editing = ref<AgentInfo | undefined>()
const cloning = ref<AgentInfo | undefined>()

function addAgent() {
  editing.value = undefined
  cloning.value = undefined
  formOpen.value = true
}
function editAgent(a: AgentInfo) {
  editing.value = a
  cloning.value = undefined
  formOpen.value = true
}
function cloneAgent(a: AgentInfo) {
  editing.value = undefined
  cloning.value = a
  formOpen.value = true
}
async function onSaved() {
  await refresh()
}

// Hide asks first. The target outlives the dialog so its text does not blank
// while it fades out.
const hideOpen = ref(false)
const hideTarget = ref<AgentInfo | null>(null)
const hiding = ref(false)
const hideError = ref('')

function askHide(a: AgentInfo) {
  hideTarget.value = a
  hideError.value = ''
  hideOpen.value = true
}

const hideText = computed(() => (hideTarget.value ? removalText(removalOf(hideTarget.value), hideTarget.value.name, hideTarget.value.id) : undefined))

async function confirmHide() {
  const a = hideTarget.value
  if (!a) return
  const text = removalText(removalOf(a), a.name, a.id)
  hiding.value = true
  hideError.value = ''
  try {
    await requests.live(api.deleteAgent(a.id))
    hideOpen.value = false
    toast.add({ title: text.toast.title, description: text.toast.description, icon: text.icon, color: 'neutral' })
    await refresh()
  } catch (e) {
    hideError.value = (e as Error).message
  } finally {
    hiding.value = false
  }
}

// Restore takes an ID off the hidden list; its agent comes back as it was.
const restoring = ref('')

async function restore(id: string) {
  restoring.value = id
  try {
    const a = await requests.live(api.unhideAgent(id))
    if (a) toast.add({ title: 'Restored', description: a.name, icon: 'i-lucide-eye', color: 'success' })
    else toast.add({ title: 'Nothing to restore', description: `No agent has the ID ${id} any more; it is off the hidden list.`, icon: 'i-lucide-eye', color: 'neutral' })
  } catch (e) {
    toast.add({ title: 'Restore failed', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    // Reload after a failure too: a 404 usually means another admin restored
    // the agent first, and its stale entry should leave the Hidden list.
    await refresh()
    restoring.value = ''
  }
}

function signalBadge(a: AgentInfo): { label: string; title: string } {
  const s = a.signal ?? { kind: 'bell' as const }
  switch (s.kind) {
    case 'hook':
      return { label: 'hooks', title: a.adapter ? `Reports through hooks (${a.adapter} adapter)` : 'Reports through hooks' }
    case 'pattern':
      return { label: 'pattern', title: `Screen pattern: ${s.pattern ?? ''}` }
    case 'none':
      return { label: 'none', title: 'Never flagged as needing input' }
    default:
      return { label: 'bell / OSC', title: 'Terminal bell or notification escape' }
  }
}

// This page's group in the command palette (#302): a new agent, Edit for each, the Launch dialog, a refresh, the hooks not installed yet.
usePalette().registerPage(() => ({
  label: 'Agents',
  rows: [
    { key: 'page:add', label: 'Add agent', icon: 'i-lucide-plus', run: addAgent },
    { key: 'page:launch', label: 'Launch an agent', icon: 'i-lucide-play', kbds: ['N'], run: () => launch.show() },
    { key: 'page:refresh', label: 'Refresh', icon: 'i-lucide-refresh-cw', run: refresh },
    ...(integrations.value ?? []).filter((i) => i.installable && !i.installed).map((i) => ({ key: `page:install:${i.id}`, label: `Install hooks for ${i.name}`, detail: i.where, icon: 'i-lucide-plug', run: () => installHooks(i) })),
    ...agents.value.map((a) => ({ key: `page:edit:${a.id}`, label: `Edit ${a.name}`, detail: a.description || a.command.join(' '), agentId: a.id, run: () => editAgent(a) })),
    ...agents.value.map((a) => ({ key: `page:clone:${a.id}`, label: `Clone ${a.name}`, detail: 'A copy with its whole configuration, under a name and ID of its own', agentId: a.id, run: () => cloneAgent(a) })),
  ],
}))
</script>

<template>
  <UDashboardPanel id="agents">
    <template #header>
      <UDashboardNavbar :toggle="false" title="Agents" data-agents-header>
        <template #right>
          <PageAbout :shown="shownState === 'content'">
            <p>Agents come from the server catalog: the built-in entries, the <code>catalog</code> section of the config file and whatever you add here, which the server keeps in its data directory. Commands are argv arrays; nothing goes through a shell.</p>
          </PageAbout>
          <IconButton icon="i-lucide-refresh-cw" color="neutral" variant="outline" :loading="loading" label="Refresh" data-agents-refresh @click="refresh"><span class="max-sm:sr-only">Refresh</span></IconButton>
          <!-- Its label is the icon's on a phone: About this page sits beside it once the catalog has an agent. -->
          <IconButton icon="i-lucide-plus" color="neutral" variant="outline" label="Add agent" @click="addAgent"><span class="max-sm:sr-only">Add agent</span></IconButton>
          <UButton label="Launch agent" icon="i-lucide-play" @click="launch.show()" />
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <UAlert v-if="error && shownState !== 'failed'" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" class="mb-4" />
      <UAlert v-if="serverHost.switchyard.value" color="neutral" variant="subtle" icon="i-lucide-train-track" title="This server is a switchyard" description="It coordinates hosted sessions from other Conductors and launches nothing of its own: the catalog here is not used." class="mb-4" data-switchyard-notice />
      <p v-if="shownState !== 'content'" class="text-sm text-muted mb-4" data-agents-about>
        Agents come from the server catalog: the built-in entries, the <code>catalog</code> section of the config file and whatever you add here, which the server keeps in its data directory. Commands are argv arrays; nothing goes through a shell.
      </p>
      <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        <!-- An agent that is not installed is a standing fact: muted words and a neutral icon, never faded (P16). -->
        <UCard v-for="a in agents" :key="a.id" :data-agent="a.id" :data-available="isAvailable(a)">
          <div class="flex items-start gap-3">
            <UIcon :name="agentIcon(a.icon)" class="size-6 flex-none mt-0.5" :class="isAvailable(a) ? 'text-primary' : 'text-muted'" data-agent-icon />
            <div class="min-w-0 flex-1">
              <div class="font-medium" :class="!isAvailable(a) && 'text-muted'" data-agent-name>{{ a.name }} <span class="text-xs text-muted font-mono" data-mono="id">{{ a.id }}</span></div>
              <p v-if="a.description" class="text-sm text-muted">{{ a.description }}</p>
              <code class="block text-xs mt-2 truncate" :class="!isAvailable(a) && 'text-muted'" :title="joinArgv(a.command)">{{ joinArgv(a.command) }}</code>
              <div class="mt-2 flex flex-wrap gap-2">
                <UBadge
                  v-if="check(a)"
                  :label="check(a)?.label"
                  :title="check(a)?.title"
                  :icon="check(a)?.state === 'checking' ? 'i-lucide-loader-circle' : 'i-lucide-circle-help'"
                  :color="check(a)?.state === 'checking' ? 'neutral' : 'warning'"
                  variant="subtle"
                  size="sm"
                  :data-checking="check(a)?.state === 'checking' || undefined"
                  :data-unanswered="check(a)?.state === 'unanswered' || undefined"
                />
                <UBadge
                  v-else-if="!isAvailable(a)"
                  :label="notInstalledBadge(a, serverHost.host.value).label"
                  :title="notInstalledBadge(a, serverHost.host.value).title"
                  :icon="NOT_INSTALLED_BADGE.icon"
                  :color="NOT_INSTALLED_BADGE.color"
                  variant="subtle"
                  size="sm"
                  data-not-installed
                  :data-not-agent="notInstalledBadge(a, serverHost.host.value).notAgent || undefined"
                />
                <UBadge
                  v-else-if="identity(a, serverHost.host.value).state !== 'unprobed'"
                  :label="identity(a, serverHost.host.value).label"
                  :title="identity(a, serverHost.host.value).title"
                  :icon="identityIcon[identity(a, serverHost.host.value).state as keyof typeof identityIcon]"
                  :color="identityColor[identity(a, serverHost.host.value).state as keyof typeof identityColor]"
                  variant="subtle"
                  size="sm"
                  :data-identity="identity(a, serverHost.host.value).state"
                  :data-version="a.identity?.version"
                />
                <UBadge :label="signalBadge(a).label" :title="signalBadge(a).title" color="neutral" variant="subtle" size="sm" />
                <UBadge v-if="a.allowArgs" label="accepts args" color="neutral" variant="subtle" size="sm" />
                <UBadge v-if="a.cwd" :label="a.cwd" color="neutral" variant="subtle" size="sm" />
              </div>
              <!-- Whether it reports to Conductor: neutral for every key (green is for a live agent); Not set up links its adapter's row. -->
              <div v-if="integrations" class="mt-2 flex flex-wrap items-center gap-x-1.5 text-xs text-muted" :data-agent-reports="reportOf(a).key">
                <span class="size-[7px] flex-none rounded-full bg-neutral-400" aria-hidden="true" />
                <span data-agent-reports-words>{{ reportOf(a).words }}</span>
                <UButton
                  v-if="reportOf(a).key === 'unset'"
                  label="Set up"
                  :to="`#reporting-${a.adapter}`"
                  size="xs"
                  color="neutral"
                  variant="link"
                  class="px-1 underline max-lg:min-h-11 max-lg:min-w-11 max-lg:justify-center"
                  :aria-label="`Set up ${a.name}'s reporting to Conductor`"
                  data-agent-setup
                  @click="showHash(`#reporting-${a.adapter}`)"
                />
              </div>
              <div class="mt-3 -mb-1 flex justify-end gap-1">
                <UButton v-if="a.site" label="Website" icon="i-lucide-external-link" size="xs" color="neutral" variant="ghost" :to="a.site" target="_blank" rel="noopener noreferrer" :aria-label="`${a.name} website`" />
                <UButton label="Edit" icon="i-lucide-pencil" size="xs" color="neutral" variant="ghost" :aria-label="`Edit ${a.name}`" @click="editAgent(a)" />
                <UButton label="Clone" icon="i-lucide-copy" size="xs" color="neutral" variant="ghost" :aria-label="`Clone ${a.name}`" data-agent-clone @click="cloneAgent(a)" />
                <UButton
                  :label="removalText(removalOf(a), a.name, a.id).button"
                  :icon="removalText(removalOf(a), a.name, a.id).icon"
                  size="xs"
                  color="neutral"
                  variant="ghost"
                  :aria-label="`${removalText(removalOf(a), a.name, a.id).button} ${a.name}`"
                  @click="askHide(a)"
                />
              </div>
            </div>
          </div>
        </UCard>
      </div>
      <PageEmpty
        v-if="shownState !== 'content'"
        page="agents"
        :state="shownState"
        words="The catalog has no agent. Add one here, or in the config file's catalog."
        thing="catalog"
        :error="error"
        :actions="[{ label: 'Add agent', icon: 'i-lucide-plus', onClick: addAgent }]"
        @again="refresh"
      />

      <section v-if="hidden.length" class="mt-8" aria-labelledby="hidden-agents">
        <h2 id="hidden-agents" class="text-[11px] font-semibold uppercase tracking-wider text-muted">Hidden · {{ hidden.length }}</h2>
        <p class="mt-1 mb-3 text-sm text-muted">Not offered when launching. Restore brings an agent back as it was.</p>
        <ul class="flex flex-wrap gap-2">
          <li v-for="id in hidden" :key="id" class="flex items-center gap-1 rounded-md border border-default py-1 ps-3 pe-1">
            <span class="font-mono text-xs">{{ id }}</span>
            <UButton
              label="Restore"
              icon="i-lucide-eye"
              size="xs"
              color="neutral"
              variant="ghost"
              :loading="restoring === id"
              :disabled="!!restoring && restoring !== id"
              :aria-label="`Restore ${id}`"
              @click="restore(id)"
            />
          </li>
        </ul>
      </section>

      <AgentsReporting class="mt-8" :integrations="integrations" :host="integrationsHost" :error="integrationsError" @changed="readIntegrations" />

      <AddAgentSlideover v-model:open="formOpen" :agent="editing" :clone="cloning" :taken-ids="agents.map((a) => a.id)" @saved="onSaved" />

      <UModal v-model:open="hideOpen" :title="hideText?.title ?? 'Hide agent?'" :description="hideText?.description">
        <template v-if="hideError" #body>
          <UAlert color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="hideError" />
        </template>
        <template #footer>
          <div class="flex w-full justify-end gap-2">
            <UButton label="Cancel" color="neutral" variant="ghost" @click="hideOpen = false" />
            <UButton :label="hideText?.button ?? 'Hide'" :icon="hideText?.icon" color="error" :loading="hiding" @click="confirmHide" />
          </div>
        </template>
      </UModal>
    </template>
  </UDashboardPanel>
</template>
