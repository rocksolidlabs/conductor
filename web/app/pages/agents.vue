<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import { joinArgv } from '~/utils/argv'
import { agentIcon } from '~/utils/agentIcons'
import { checkingBadge, identity, isAvailable, notInstalledBadge, settleChecks } from '~/utils/agents'

const identityColor: Record<string, 'success' | 'error' | 'warning' | 'neutral'> = { ok: 'success', impostor: 'error', unidentified: 'warning', pending: 'neutral', failed: 'warning' }
const identityIcon: Record<string, string> = { ok: 'i-lucide-badge-check', impostor: 'i-lucide-shield-alert', unidentified: 'i-lucide-help-circle', pending: 'i-lucide-loader-circle', failed: 'i-lucide-triangle-alert' }
import { removalOf, removalText } from '~/utils/catalog'

useHead({ title: 'Agents' })

const api = useSessions()
const admin = useWorkbenchToken()
const toast = useToast()
const agents = ref<AgentInfo[]>([])
/** IDs the server hides from the catalog; each can be restored. */
const hidden = ref<string[]>([])
const loading = ref(false)
const error = ref('')
const launch = useLaunchModal()
const serverHost = useServerHost()

// Each read of the list is a generation: the checks a read started filling
// in stop once a newer one (or leaving the page) supersedes it.
let generation = 0

/**
 * Draws the list from the server's answer of the moment, which waits for
 * nothing (#704), then fills in each agent the server is still checking (its
 * program looked up, its version flag run) as its own read answers.
 */
async function refresh() {
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  const gen = ++generation
  const stop = () => gen !== generation
  loading.value = true
  try {
    const r = await api.catalogNow()
    if (stop()) return
    agents.value = r.agents
    hidden.value = r.hidden
    error.value = ''
    serverHost.load()
    const put = (id: string, f: (a: AgentInfo) => AgentInfo) => (agents.value = agents.value.map((x) => (x.id === id ? f(x) : x)))
    void settleChecks(
      r.agents.filter((a) => a.checking).map((a) => a.id),
      api.catalogAgent,
      (a) => put(a.id, () => a),
      // A read that failed (the agent removed meanwhile) leaves what the list said, no longer spinning.
      { stop, failed: (id) => put(id, (a) => ({ ...a, checking: false })) },
    )
  } catch (e) {
    if (!stop()) error.value = (e as Error).message
  } finally {
    if (!stop()) loading.value = false
  }
}

onMounted(refresh)
onBeforeUnmount(() => generation++)
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
    await api.deleteAgent(a.id)
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
    const a = await api.unhideAgent(id)
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

// This page's group in the command palette (#302): a new agent, Edit for each, the Launch dialog.
usePalette().registerPage(() => ({
  label: 'Agents',
  rows: [
    { key: 'page:add', label: 'Add agent', icon: 'i-lucide-plus', run: addAgent },
    { key: 'page:launch', label: 'Launch an agent', icon: 'i-lucide-play', kbds: ['N'], run: () => launch.show() },
    ...agents.value.map((a) => ({ key: `page:edit:${a.id}`, label: `Edit ${a.name}`, detail: a.description || a.command.join(' '), agentId: a.id, run: () => editAgent(a) })),
    ...agents.value.map((a) => ({ key: `page:clone:${a.id}`, label: `Clone ${a.name}`, detail: 'A copy with its whole configuration, under a name and ID of its own', agentId: a.id, run: () => cloneAgent(a) })),
  ],
}))
</script>

<template>
  <UDashboardPanel id="agents">
    <template #header>
      <UDashboardNavbar :toggle="false" title="Agents">
        <template #right>
          <UButton label="Add agent" icon="i-lucide-plus" color="neutral" variant="outline" @click="addAgent" />
          <UButton label="Launch agent" icon="i-lucide-play" @click="launch.show()" />
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" class="mb-4" />
      <UAlert v-if="serverHost.switchyard.value" color="neutral" variant="subtle" icon="i-lucide-train-track" title="This server is a switchyard" description="It coordinates hosted sessions from other Conductors and launches nothing of its own: the catalog here is not used." class="mb-4" data-switchyard-notice />
      <p class="text-sm text-muted mb-4">
        Agents come from the server catalog: the built-in entries, the <code>catalog</code> section of the config file and whatever you add here, which the server keeps in its data directory. Commands are argv arrays; nothing goes through a shell.
      </p>
      <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        <UCard v-for="a in agents" :key="a.id" :class="!isAvailable(a) && 'opacity-60'" :data-agent="a.id" :data-available="isAvailable(a)">
          <div class="flex items-start gap-3">
            <UIcon :name="agentIcon(a.icon)" class="size-6 text-primary flex-none mt-0.5" />
            <div class="min-w-0 flex-1">
              <div class="font-medium">{{ a.name }} <span class="text-xs text-muted font-mono">{{ a.id }}</span></div>
              <p v-if="a.description" class="text-sm text-muted">{{ a.description }}</p>
              <code class="block text-xs mt-2 truncate" :title="joinArgv(a.command)">{{ joinArgv(a.command) }}</code>
              <div class="mt-2 flex flex-wrap gap-2">
                <UBadge
                  v-if="a.checking"
                  :label="checkingBadge(a, serverHost.host.value).label"
                  :title="checkingBadge(a, serverHost.host.value).title"
                  icon="i-lucide-loader-circle"
                  color="neutral"
                  variant="subtle"
                  size="sm"
                  data-checking
                />
                <UBadge
                  v-else-if="!isAvailable(a)"
                  :label="notInstalledBadge(a, serverHost.host.value).label"
                  :title="notInstalledBadge(a, serverHost.host.value).title"
                  icon="i-lucide-circle-off"
                  color="warning"
                  variant="subtle"
                  size="sm"
                  data-not-installed
                  :data-not-agent="notInstalledBadge(a, serverHost.host.value).notAgent || undefined"
                />
                <UBadge
                  v-else-if="identity(a, serverHost.host.value).state !== 'unprobed'"
                  :label="identity(a, serverHost.host.value).label"
                  :title="identity(a, serverHost.host.value).title"
                  :icon="identityIcon[identity(a, serverHost.host.value).state]"
                  :color="identityColor[identity(a, serverHost.host.value).state]"
                  variant="subtle"
                  size="sm"
                  :data-identity="identity(a, serverHost.host.value).state"
                  :data-version="a.identity?.version"
                />
                <UBadge :label="signalBadge(a).label" :title="signalBadge(a).title" color="neutral" variant="subtle" size="sm" />
                <UBadge v-if="a.allowArgs" label="accepts args" color="neutral" variant="subtle" size="sm" />
                <UBadge v-if="a.cwd" :label="a.cwd" color="neutral" variant="subtle" size="sm" />
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
      <p v-if="!agents.length && !loading && !error" class="text-sm text-muted">No agents in the catalog.</p>

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
