<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import type { BrakemanConfig, BrakemanScope, BrakemanVisibility } from '~/utils/brakeman'
import { BOUNDS, BRAKEMAN_ADAPTERS, GRANTS, agentChoice, agentIdOf, agentItems as agentChoices, configProblems, formField, formMove, sameConfig, grantHeld, scopeLines, scopeList, servedGrants, withGrant } from '~/utils/brakeman'
import { ApiError } from '~/composables/useApi'
import { SETTINGS_FIELD_UI as fieldUi } from '~/utils/settingsSections'

/**
 * Brakeman's card in Settings (docs/plans/brakeman.md, PR-6): its configuration, saved on the server (PUT /api/brakeman/config, at
 * the revision read): the agent (one whose adapter registers Conductor's tools and has a read-only mode: a turn never runs with yolo),
 * the account (the folder the agent's login lives in, or its default login), the model (for an agent with a model flag), the folder
 * its turns run in, what it may see (terminal screens off until turned on), its scope, whether it may draft and propose, and its
 * budget. The form checks what the server checks; the server's own words are shown for what it refuses, and beside an agent whose
 * read-only mode does not hold (its warnings). What is refused of a field the form draws no control for (the scope as a whole, the wake
 * mode, a model while the agent shows no model field) is said above Save.
 *
 * What it may run (PR-13): the four operations a person may grant Brakeman (BK2's closed list), each with what it authorizes in words,
 * a switch each. A grant this server serves no route of is shown as not in this build and cannot be turned on (the admin's guide lists
 * the ones it serves). A grant gives no read and a read no grant; none approves, answers, accepts, closes or changes this
 * configuration, and the server checks each act against the grant, the scope and the target's state whatever is granted here.
 */
const bk = useBrakeman()
const api = useSessions()
const toast = useToast()
const ops = useGuideOps()
/** The grants this server serves (null while its guide is unread): the others cannot be turned on. */
const served = computed(() => servedGrants(ops.value))
function grant(op: string, on: boolean) {
  if (form.value) form.value.permissions.act = withGrant(form.value.permissions.act, op, on)
}

const agents = ref<AgentInfo[]>([])
const form = ref<BrakemanConfig | null>(null)
/** The configuration the form was last set from: a form that still holds it is untouched, and follows the one saved. */
const base = ref<BrakemanConfig | null>(null)
/** The store's generation the form was set under: a reset (a new token, perhaps another server) leaves it another token's. */
const formGen = ref(-1)
const scopeText = reactive<Record<keyof BrakemanScope, string>>({ projects: '', repos: '', crews: '', runs: '', sessions: '' })
const saving = ref(false)
const serverError = ref<{ field?: string; message: string } | null>(null)

const saved = computed(() => bk.state.value?.config ?? null)
const available = computed(() => bk.available.value)

function reset(c: BrakemanConfig) {
  formGen.value = bk.generation.value
  base.value = JSON.parse(JSON.stringify(c)) as BrakemanConfig
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
/** Whether the form holds what is not saved: Save is offered for it. */
const dirty = computed(() => !!saved.value && !!body.value && !sameConfig(body.value, saved.value))
/** Whether the form was changed here since it was set: compared with what it was set from, not with what is saved now. */
const edited = computed(() => !!base.value && !!body.value && !sameConfig(body.value, base.value))

// The form follows the configuration saved until it is changed here: a save from elsewhere replaces a form left untouched, and
// leaves one being edited as it is (its save then meets the newer revision and is refused stale). A form set under the token
// before is never kept (formMove): the new server's revision may be the same number as the one it was read at.
watch(
  () => [bk.generation.value, saved.value?.rev],
  () => {
    const move = formMove(form.value ? { gen: formGen.value, edited: edited.value } : null, bk.generation.value, !!saved.value)
    if (move === 'set') reset(saved.value!)
    else if (move === 'drop') {
      form.value = null
      base.value = null
      serverError.value = null
    }
  },
  { immediate: true },
)

/** The agents Brakeman can be: an adapter that registers Conductor's tools and has a read-only mode. */
const offered = computed(() => agents.value.filter((a) => a.adapter && BRAKEMAN_ADAPTERS[a.adapter]))
const agentItems = computed(() => agentChoices(offered.value))
/** The Agent select's value: None is a value of its own (a select item may not be ""), saved as no agent. */
const agentModel = computed({
  get: () => agentChoice(form.value?.agentId),
  set: (v: string) => {
    if (form.value) form.value.agentId = agentIdOf(v)
  },
})
const adapter = computed(() => agents.value.find((a) => a.id === form.value?.agentId)?.adapter)
const caps = computed(() => (adapter.value ? BRAKEMAN_ADAPTERS[adapter.value] : undefined))
// Until the catalog is read the agent's adapter is not known: the server checks it on the save.
const problems = computed(() => (body.value ? configProblems(body.value, agents.value.length ? (adapter.value ?? '') : null) : {}))
/** The server's words for the agent saved (Codex's read-only mode leaves its own MCP servers and exec-policy allow rules). */
const warnings = computed(() => (form.value?.agentId && form.value.agentId === saved.value?.agentId ? (bk.state.value?.warnings ?? []) : []))
/** The fields the form draws a control for, by the names the server gives them; `scope` is the scope's fieldset. */
const controls = computed(() => {
  const out = new Set(['agentId', 'account', 'cwd', 'scope', 'permissions.act', 'budget.turnMinutes', 'budget.readsPerTurn', 'budget.turnsPerDay'])
  for (const k of Object.keys(scopeText)) out.add(`scope.${k}`)
  if (caps.value?.model) out.add('model')
  if (caps.value?.allowTools) out.add('allowConductorTools')
  return out
})
const fieldError = (field: string) => problems.value[field] || (serverError.value?.field && formField(serverError.value.field) === field ? serverError.value.message : undefined)
/** What is refused of no field the form shows, said above Save: the server's refusal with no field or none drawn, and the form's own. */
const unshown = computed(() => {
  const out: string[] = []
  for (const [field, words] of Object.entries(problems.value)) if (!controls.value.has(field)) out.push(words)
  const e = serverError.value
  if (e && (!e.field || !controls.value.has(formField(e.field)))) out.push(e.message)
  return out
})

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
  const gen = bk.generation.value
  try {
    const out = await bk.saveConfig(c)
    reset(out.config)
    toast.add({ title: out.config.enabled ? 'Brakeman saved, on' : 'Brakeman saved, off', icon: 'i-lucide-check', color: 'success' })
  } catch (e) {
    // Answered after a new token: the form is this server's now (formMove), and the answer is the token before's.
    if (bk.generation.value !== gen) {
      toast.add({ title: 'Not saved here', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'warning' })
      return
    }
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
      <UFormField label="Agent" description="Its turns run in the agent's own read-only mode, on your login." :error="fieldError('agentId')" :ui="fieldUi">
        <USelect v-model="agentModel" :items="agentItems" class="w-full" data-brakeman-agent />
      </UFormField>
      <UAlert v-for="w in warnings" :key="w" color="warning" variant="subtle" icon="i-lucide-shield-alert" :title="w" data-brakeman-warning />
      <p v-if="form.agentId && form.agentId !== saved?.agentId" class="text-xs text-muted" data-brakeman-warning-later>Once saved, Conductor says what this agent's read-only mode does not hold, if anything.</p>
      <UFormField label="Account" description="The folder the agent's login lives in (its configuration folder); empty for its default login on this machine." :error="fieldError('account')" :ui="fieldUi">
        <DirInput v-model="form.account" placeholder="/home/me/.claude-work" picker-title="The agent's login folder" data-brakeman-account />
      </UFormField>
      <UFormField v-if="caps?.model" label="Model" description="Empty for the agent's default." :error="fieldError('model')" :ui="fieldUi">
        <UInput v-model="form.model" class="w-full font-mono text-xs" placeholder="the agent's default" data-brakeman-model />
      </UFormField>
      <UFormField label="Folder" description="Where its turns run; it reads there and changes nothing." :error="fieldError('cwd')" :ui="fieldUi">
        <DirInput v-model="form.cwd" picker-title="The folder Brakeman's turns run in" data-brakeman-cwd />
      </UFormField>
      <UFormField v-if="caps?.allowTools" label="Conductor's tools" :error="fieldError('allowConductorTools')" :ui="fieldUi">
        <USwitch v-model="form.allowConductorTools" label="Run Conductor's tools without asking" description="Without it the agent asks before each of Conductor's tools, and the turn waits on you in Needs you." data-brakeman-allow-tools />
      </UFormField>

      <!-- Two columns of switches once Settings' column has room for them (#433). -->
      <fieldset class="grid gap-2 @3xl:grid-cols-2 @3xl:gap-x-8">
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
        <p v-if="fieldError('scope')" class="text-xs text-error" data-brakeman-scope-error>{{ fieldError('scope') }}</p>
      </fieldset>

      <fieldset class="grid gap-2 @3xl:grid-cols-2 @3xl:gap-x-8">
        <legend class="mb-1 text-sm font-medium">What it may do</legend>
        <USwitch v-model="form.permissions.draft" label="Draft" description="Draft tickets, crews and handoffs, which you edit and make." data-brakeman-draft />
        <USwitch v-model="form.permissions.propose" label="Propose" description="Propose operations and prepare decisions, which you take." data-brakeman-propose />
        <p v-if="!form.permissions.draft && !form.permissions.propose" class="text-xs text-muted @3xl:col-span-2">Observe only.</p>
      </fieldset>

      <fieldset class="flex flex-col gap-2" data-brakeman-grants>
        <legend class="mb-1 text-sm font-medium">What it may run</legend>
        <p class="text-xs text-muted">
          Off, Brakeman proposes and you act. Granted, it runs that operation itself through the operation's own route, which checks the grant,
          its scope and what it acts on, and leaves a receipt on the brief. No grant approves, answers, accepts or closes anything, and none
          changes these settings.
        </p>
        <div class="grid gap-2 @3xl:grid-cols-2 @3xl:gap-x-8">
          <USwitch
            v-for="g in GRANTS"
            :key="g.value"
            :model-value="form.permissions.act?.includes(g.value) ?? false"
            :label="g.label"
            :description="served && !served.has(g.value) ? `${g.words} Not in this build: nothing serves it yet.` : g.words"
            :disabled="grantHeld(served, form.permissions.act?.includes(g.value) ?? false, g.value)"
            :data-brakeman-grant="g.value"
            :data-grant-served="served ? served.has(g.value) : undefined"
            @update:model-value="(on: boolean) => grant(g.value, on)"
          />
        </div>
        <p v-if="fieldError('permissions.act')" class="text-xs text-error" data-brakeman-grant-error>{{ fieldError('permissions.act') }}</p>
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

      <UAlert v-for="w in unshown" :key="w" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="w" data-brakeman-save-error />
      <div class="flex items-center gap-3">
        <UButton type="submit" label="Save Brakeman" icon="i-lucide-save" :loading="saving" :disabled="!dirty || Object.keys(problems).length > 0" data-brakeman-save />
        <NuxtLink v-if="saved?.enabled" to="/brakeman" class="text-sm underline">Open Brakeman</NuxtLink>
      </div>
    </form>
  </UCard>
</template>
