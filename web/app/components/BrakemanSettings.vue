<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import type { BrakemanConfig, BrakemanScope, BrakemanVisibility } from '~/utils/brakeman'
import { BOUNDS, BRAKEMAN_ADAPTERS, configProblems, scopeLines, scopeList } from '~/utils/brakeman'
import { ApiError } from '~/composables/useApi'

/**
 * Brakeman's card in Settings (docs/plans/brakeman.md, PR-6): its configuration, saved on the server (PUT /api/brakeman/config, at
 * the revision read): the agent (one whose adapter registers Conductor's tools and has a read-only mode: a turn never runs with yolo),
 * the account (the folder the agent's login lives in, or its default login), the model (for an agent with a model flag), the folder
 * its turns run in, what it may see (terminal screens off until turned on), its scope, whether it may draft and propose, and its
 * budget. The form checks what the server checks; the server's own words are shown for what it refuses, and beside an agent whose
 * read-only mode does not hold (its warnings).
 */
const bk = useBrakeman()
const api = useSessions()
const toast = useToast()

const agents = ref<AgentInfo[]>([])
const form = ref<BrakemanConfig | null>(null)
const scopeText = reactive<Record<keyof BrakemanScope, string>>({ projects: '', repos: '', crews: '', runs: '', sessions: '' })
const saving = ref(false)
const serverError = ref<{ field?: string; message: string } | null>(null)

const saved = computed(() => bk.state.value?.config ?? null)
const available = computed(() => bk.available.value)

function reset(c: BrakemanConfig) {
  form.value = JSON.parse(JSON.stringify(c)) as BrakemanConfig
  for (const k of Object.keys(scopeText) as Array<keyof BrakemanScope>) scopeText[k] = scopeLines(c.scope[k])
  serverError.value = null
}

onMounted(async () => {
  await bk.load()
  api
    .catalog()
    .then((c) => (agents.value = c))
    .catch(() => (agents.value = []))
})

/** The form as the server takes it, its scope from the text fields. */
const body = computed<BrakemanConfig | null>(() => {
  if (!form.value) return null
  const scope: BrakemanScope = {}
  for (const k of Object.keys(scopeText) as Array<keyof BrakemanScope>) {
    const list = scopeList(scopeText[k])
    if (list.length) scope[k] = list
  }
  const c = { ...form.value, scope, account: form.value.account?.trim() || undefined, model: form.value.model?.trim() || undefined, cwd: form.value.cwd.trim() }
  return c
})
const dirty = computed(() => !!saved.value && !!body.value && JSON.stringify(normal(body.value)) !== JSON.stringify(normal(saved.value)))
function normal(c: BrakemanConfig) {
  const { rev: _rev, updatedAt: _at, ...rest } = c
  return { ...rest, account: rest.account || undefined, model: rest.model || undefined }
}

// The form follows the configuration saved until it is changed here.
watch(
  () => saved.value?.rev,
  () => {
    if (saved.value && (!form.value || !dirty.value)) reset(saved.value)
  },
  { immediate: true },
)

/** The agents Brakeman can be: an adapter that registers Conductor's tools and has a read-only mode. */
const offered = computed(() => agents.value.filter((a) => a.adapter && BRAKEMAN_ADAPTERS[a.adapter]))
const agentItems = computed(() => [{ label: 'None', value: '' }, ...offered.value.map((a) => ({ label: a.available === false ? `${a.name} (not installed)` : a.name, value: a.id }))])
const adapter = computed(() => agents.value.find((a) => a.id === form.value?.agentId)?.adapter)
const caps = computed(() => (adapter.value ? BRAKEMAN_ADAPTERS[adapter.value] : undefined))
// Until the catalog is read the agent's adapter is not known: the server checks it on the save.
const problems = computed(() => (body.value ? configProblems(body.value, agents.value.length ? (adapter.value ?? '') : null) : {}))
/** The server's words for the agent saved (Codex's read-only mode leaves its own MCP servers and exec-policy allow rules). */
const warnings = computed(() => (form.value?.agentId && form.value.agentId === saved.value?.agentId ? (bk.state.value?.warnings ?? []) : []))
const fieldError = (field: string) => problems.value[field] || (serverError.value?.field === field ? serverError.value.message : undefined)

const visibility: Array<{ key: keyof BrakemanVisibility; label: string; description: string }> = [
  { key: 'runs', label: 'Runs', description: 'Runs, tasks, attempts, check receipts, messages and blockers.' },
  { key: 'logs', label: 'Check logs', description: "A check's log text; without it a receipt carries its exit, its commit and its log's SHA-256." },
  { key: 'dispatch', label: 'Dispatch', description: 'Projects, tickets, comments and their history.' },
  { key: 'sessions', label: 'Sessions', description: 'The sessions list: names, states, questions and quiet time.' },
  { key: 'touched', label: 'Touched files', description: 'The files agents touched.' },
  { key: 'terminals', label: 'Terminal screens', description: "Lets Brakeman read what a session's screen shows, which can hold what no record does. Off until you turn it on." },
]
const scopeFields: Array<{ key: keyof BrakemanScope; label: string; placeholder: string }> = [
  { key: 'projects', label: 'Projects', placeholder: 'CND' },
  { key: 'repos', label: 'Repositories', placeholder: '/home/me/code/app' },
  { key: 'crews', label: 'Crews', placeholder: 'a crew id' },
  { key: 'runs', label: 'Runs', placeholder: 'a run id' },
  { key: 'sessions', label: 'Sessions', placeholder: 'a session id' },
]

async function save() {
  const c = body.value
  if (!c || Object.keys(problems.value).length) return
  saving.value = true
  serverError.value = null
  try {
    const out = await bk.saveConfig(c)
    reset(out.config)
    toast.add({ title: out.config.enabled ? 'Brakeman saved, on' : 'Brakeman saved, off', icon: 'i-lucide-check', color: 'success' })
  } catch (e) {
    const field = e instanceof ApiError && typeof e.details.field === 'string' ? e.details.field : undefined
    serverError.value = { field, message: (e as Error).message }
    if (e instanceof ApiError && e.code === 'stale_revision') {
      toast.add({ title: 'Saved elsewhere meanwhile', description: 'The form shows what is saved now.', icon: 'i-lucide-triangle-alert', color: 'warning' })
      await bk.load()
      if (bk.state.value) reset(bk.state.value.config)
    }
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <UCard v-if="available !== false" :ui="{ header: 'px-4 py-3 sm:px-5', body: 'p-4 sm:p-5' }" data-brakeman-settings>
    <template #header><SettingsCardTitle title="Brakeman" description="An agent of your choosing that reads what happened and answers with briefs. It reads and drafts; it never runs with yolo." /></template>
    <p v-if="!form" class="text-sm text-muted">Reading Brakeman's configuration…</p>
    <form v-else class="flex flex-col gap-4" @submit.prevent="save">
      <USwitch v-model="form.enabled" label="Brakeman on" description="Off, nothing is asked of its agent." data-brakeman-enabled />
      <UFormField label="Agent" description="Its turns run in the agent's own read-only mode, on your login." :error="fieldError('agentId')">
        <USelect v-model="form.agentId" :items="agentItems" class="w-full" data-brakeman-agent />
      </UFormField>
      <UAlert v-for="w in warnings" :key="w" color="warning" variant="subtle" icon="i-lucide-shield-alert" :title="w" data-brakeman-warning />
      <p v-if="form.agentId && form.agentId !== saved?.agentId" class="text-xs text-muted" data-brakeman-warning-later>Once saved, Conductor says what this agent's read-only mode does not hold, if anything.</p>
      <UFormField label="Account" description="The folder the agent's login lives in (its configuration folder); empty for its default login on this machine." :error="fieldError('account')">
        <DirInput v-model="form.account" placeholder="/home/me/.claude-work" picker-title="The agent's login folder" data-brakeman-account />
      </UFormField>
      <UFormField v-if="caps?.model" label="Model" description="Empty for the agent's default." :error="fieldError('model')">
        <UInput v-model="form.model" class="w-full font-mono text-xs" placeholder="the agent's default" data-brakeman-model />
      </UFormField>
      <UFormField label="Folder" description="Where its turns run; it reads there and changes nothing." :error="fieldError('cwd')">
        <DirInput v-model="form.cwd" picker-title="The folder Brakeman's turns run in" data-brakeman-cwd />
      </UFormField>
      <UFormField v-if="caps?.allowTools" label="Conductor's tools" :error="fieldError('allowConductorTools')">
        <USwitch v-model="form.allowConductorTools" label="Run Conductor's tools without asking" description="Without it the agent asks before each of Conductor's tools, and the turn waits on you in Needs you." data-brakeman-allow-tools />
      </UFormField>

      <fieldset class="flex flex-col gap-2">
        <legend class="mb-1 text-sm font-medium">What it may see</legend>
        <USwitch v-for="v in visibility" :key="v.key" v-model="form.visibility[v.key]" :label="v.label" :description="v.description" :data-brakeman-visibility="v.key" />
      </fieldset>

      <fieldset class="flex flex-col gap-2">
        <legend class="mb-1 text-sm font-medium">Its scope</legend>
        <p class="text-xs text-muted">Empty is all of it; names given narrow it, one a line, at most {{ BOUNDS.selector }} each.</p>
        <div class="grid gap-3 sm:grid-cols-2">
          <UFormField v-for="f in scopeFields" :key="f.key" :label="f.label" :error="fieldError(`scope.${f.key}`)">
            <UTextarea v-model="scopeText[f.key]" :rows="2" :placeholder="f.placeholder" class="w-full font-mono text-xs" :data-brakeman-scope="f.key" />
          </UFormField>
        </div>
      </fieldset>

      <fieldset class="flex flex-col gap-2">
        <legend class="mb-1 text-sm font-medium">What it may do</legend>
        <USwitch v-model="form.permissions.draft" label="Draft" description="Draft tickets, crews and handoffs, which you edit and make." data-brakeman-draft />
        <USwitch v-model="form.permissions.propose" label="Propose" description="Propose operations and prepare decisions, which you take." data-brakeman-propose />
        <p v-if="!form.permissions.draft && !form.permissions.propose" class="text-xs text-muted">Observe only.</p>
      </fieldset>

      <fieldset class="grid gap-3 sm:grid-cols-3">
        <legend class="mb-1 text-sm font-medium">Its budget</legend>
        <UFormField label="Minutes a turn" :error="fieldError('budget.turnMinutes')">
          <UInput v-model.number="form.budget.turnMinutes" type="number" :min="BOUNDS.turnMinutes[0]" :max="BOUNDS.turnMinutes[1]" class="w-full" data-brakeman-turn-minutes />
        </UFormField>
        <UFormField label="Reads a turn" :error="fieldError('budget.readsPerTurn')">
          <UInput v-model.number="form.budget.readsPerTurn" type="number" :min="BOUNDS.readsPerTurn[0]" :max="BOUNDS.readsPerTurn[1]" class="w-full" data-brakeman-reads />
        </UFormField>
        <UFormField label="Turns a day" :error="fieldError('budget.turnsPerDay')">
          <UInput v-model.number="form.budget.turnsPerDay" type="number" :min="BOUNDS.turnsPerDay[0]" :max="BOUNDS.turnsPerDay[1]" class="w-full" data-brakeman-turns />
        </UFormField>
      </fieldset>

      <UAlert v-if="serverError && !serverError.field" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="serverError.message" data-brakeman-save-error />
      <div class="flex items-center gap-3">
        <UButton type="submit" label="Save Brakeman" icon="i-lucide-save" :loading="saving" :disabled="!dirty || Object.keys(problems).length > 0" data-brakeman-save />
        <NuxtLink v-if="saved?.enabled" to="/brakeman" class="text-sm underline">Open Brakeman</NuxtLink>
      </div>
    </form>
  </UCard>
</template>
