<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import type { BrakemanConfig, BrakemanScope, BrakemanVisibility } from '~/utils/brakeman'
import { BOUNDS, BRAKEMAN_ADAPTERS, GRANTS, agentChoice, agentIdOf, agentItems as agentChoices, configProblems, foldersLine, formField, sameConfig, grantHeld, observesOnly, servedGrants, withGrant } from '~/utils/brakeman'
import { PRESETS, applyPreset, briefingTimes, crewPicks, presetChoice, projectPicks, runPicks, sessionPicks, withEntry, withoutAt, type PickerItem, type Preset } from '~/utils/brakemanSettings'
import { ApiError } from '~/composables/useApi'
import { SETTINGS_FIELD_UI as fieldUi } from '~/utils/settingsSections'
import { StaleRefusal, configForm, type BrakemanPart } from '~/utils/settingsSave'
import { OWN_LOGIN, brakemanAccount } from '~/utils/crewLaunch'
import { BRIEF_SETTINGS_SENTENCE } from '~/utils/brakemanAlert'

/**
 * Brakeman's card in Settings (docs/plans/brakeman.md, PR-6): its configuration, saved on the server (PUT /api/brakeman/config, at
 * the revision read): the agent (one whose adapter registers Conductor's tools and has a read-only mode: a turn never runs with yolo),
 * the account (the folder the agent's login lives in, or its default login), the model (for an agent with a model flag), the folder
 * it reads, with where its turns run and what they read as the server would launch them (a turn whose folder is the home folder runs in
 * the desk's own folder, trusted once, and reads the home folder and the crews' folders besides, #705), what it may see (terminal screens off until turned on), its scope, whether it may draft and propose, and its
 * budget, and whether the background role wakes it (BK3: its window, cooldown, a question's wait and briefing times, and the day's
 * minutes). The form checks what the server checks; the server's own words are shown for what it refuses, and beside an agent whose
 * read-only mode does not hold (its warnings). What is refused of a field the form draws no control for (the scope as a whole, an
 * operation's grant, the wake mode, a model while the agent shows no model field) is said at the section's foot.
 *
 * It has no Save of its own (#707): the page's one Save takes the form at the click (`take`, exposed with `dirty` and `blocked`) when
 * it holds a change, and only then, since every save of the configuration drops Brakeman's pending work (#721). Refused, the form
 * keeps every edit: a revision saved elsewhere meanwhile offers two choices, each on a fresh read of what is saved now: read it (the
 * edits dropped) or keep these (the next Save replaces the other change).
 *
 * It starts from a preset (#993): Observe, Draft and propose, or Act within grants. A preset only fills the switches of what it may do
 * and may run (utils/brakemanSettings.ts), which stay drawn under it; it never turns on terminal screens and never changes the agent,
 * its account, its folder or its tools. A form that matches none is Custom. Its scope is picked from what the app lists, the
 * briefing times are a list of times, and the limits (the budget, the window, the cooldown and a question's wait) sit under Advanced,
 * a fold that keeps its fields mounted and says how many of them hold a value refused.
 *
 * What it may run (PR-13): the four operations a person may grant Brakeman (BK2's closed list), each with what it authorizes in words,
 * a switch each. A grant this server serves no route of is shown as not in this build and cannot be turned on (the admin's guide lists
 * the ones it serves). A grant gives no read and a read no grant; none approves, answers, accepts, closes or changes this
 * configuration, and the server checks each act against the grant, the scope and the target's state whatever is granted here.
 */
const bk = useBrakeman()
const api = useSessions()
const ops = useGuideOps()
/** The grants this server serves (null while its guide is unread): the others cannot be turned on. */
const served = computed(() => servedGrants(ops.value))
function grant(op: string, on: boolean) {
  if (form.value) form.value.permissions.act = withGrant(form.value.permissions.act, op, on)
}

const agents = ref<AgentInfo[]>([])
const saved = computed(() => bk.state.value?.config ?? null)
/**
 * The form and what answers it (utils/settingsSave.ts, the one rule): a reply to an operation started here is applied only when its
 * revision is newer than the form's, and only over the fields not edited since that operation began; the watcher below is off while
 * one is in flight.
 */
const ctl = configForm<BrakemanConfig>({
  generation: () => bk.generation.value,
  saved: () => saved.value,
  save: (c) => bk.saveConfig(c),
  read: (gen) => bk.readConfig(gen),
  isStale: (e) => e instanceof ApiError && e.code === 'stale_revision',
  current: () => body.value,
  same: sameConfig,
  shown,
})
const form = ctl.form
/** A save was refused: the configuration was saved elsewhere since the form was read (the form keeps its edits). */
const stale = ctl.stale
/** A choice's read of what is saved now failed: its words, the form and the refusal kept. */
const staleError = ctl.staleError
const choosing = ctl.choosing
const scopePicked = reactive<Record<keyof BrakemanScope, string[]>>({ projects: [], repos: [], crews: [], runs: [], sessions: [] })
/** The briefing times as the form's rows hold them: a row added and not yet set is left out of what is saved. */
const times = ref<string[]>([])
/** The folder being typed for the repositories, added by Add. */
const repoText = ref('')
/** The preset the person last picked here, null once the form is read again: Act within grants picked with no grant yet stays shown. */
const picked = ref<Preset | null>(null)
const preset = computed(() => (form.value ? presetChoice(form.value.permissions, picked.value) : null))
function choosePreset(v: unknown) {
  const p = PRESETS.find((x) => x.value === v)?.value
  if (!p || !form.value) return
  picked.value = p
  applyPreset(form.value, p)
}
const presetItems = computed(() => [
  ...PRESETS.map((p) => ({ label: p.label, value: p.value, description: p.description })),
  ...(preset.value?.value === 'custom' ? [{ label: 'Custom', value: 'custom', description: 'The switches below, as set: no preset sets them so.', disabled: true }] : []),
])
/** Advanced, the limits: folded at first, its fields kept mounted. */
const advancedOpen = ref(false)
const ADVANCED = ['budget.turnMinutes', 'budget.readsPerTurn', 'budget.turnsPerDay', 'budget.minutesPerDay', 'wake.windowSeconds', 'wake.cooldownMinutes', 'wake.questionMinutes']

/** The background switch: the wake mode, background or invoked. */
function setBackground(on: boolean) {
  if (form.value) form.value.wake.mode = on ? 'background' : 'invoked'
}
const readAgainOpen = ref(false)
const serverError = ref<{ field?: string; message: string } | null>(null)

const available = computed(() => bk.available.value)

/** Fields of the form were set, by a read or a reply (`all`: the form whole): the text fields follow them. */
function shown(c: BrakemanConfig, fields: Array<keyof BrakemanConfig> | 'all') {
  if (fields === 'all' || fields.includes('scope')) for (const k of Object.keys(scopePicked) as Array<keyof BrakemanScope>) scopePicked[k] = [...(c.scope[k] ?? [])]
  if (fields === 'all' || fields.includes('wake')) times.value = [...(c.wake.briefings ?? [])]
  if (fields === 'all' || fields.includes('permissions')) picked.value = null
  if (fields === 'all') serverError.value = null
}

onMounted(async () => {
  await bk.load()
  api
    .catalog()
    .then((c) => (agents.value = c))
    .catch(() => (agents.value = []))
})

/** The form as the server takes it, its scope from the pickers and its briefing times from the rows. */
const body = computed<BrakemanConfig | null>(() => {
  if (!form.value) return null
  const scope: BrakemanScope = {}
  for (const k of Object.keys(scopePicked) as Array<keyof BrakemanScope>) {
    const list = scopePicked[k]
    if (list.length) scope[k] = [...list]
  }
  const briefings = briefingTimes(times.value)
  const c: BrakemanConfig = { ...form.value, scope, wake: { ...form.value.wake, briefings: briefings.length ? briefings : undefined }, account: form.value.account?.trim() || undefined, model: form.value.model?.trim() || undefined, cwd: form.value.cwd.trim() }
  return c
})
const dirty = ctl.dirty

// The form follows the configuration saved until it is changed here: a save from elsewhere replaces a form left untouched and older,
// and leaves one being edited as it is (its save then meets the newer revision and is refused stale). A form set under the token
// before is never kept (formMove): the new server's revision may be the same number as the one it was read at. While an operation
// started here is in flight, or a stale refusal waits for its choice, this watcher keeps the form; it looks again as one ends.
watch(
  () => [bk.generation.value, saved.value?.rev, ctl.busy.value],
  () => {
    if (ctl.follow() === 'drop') serverError.value = null
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
const brakemanAgent = computed(() => {
  const a = agents.value.find((x) => x.id === form.value?.agentId)
  return a ? { id: a.id, name: a.name, version: a.identity?.version } : null
})
const caps = computed(() => (adapter.value ? BRAKEMAN_ADAPTERS[adapter.value] : undefined))
// The account, picked from the accounts of the agent's tool (docs/plans/capacity.md, PR-7): the configuration keeps a folder, so its
// default login saves none and an account its folder. Without the accounts (not read, or a tool they do not list) the folder is typed.
const accounts = useAccounts()
onMounted(() => void accounts.store.load())
const accountFolder = computed(() => form.value?.account?.trim() ?? '')
const login = computed(() => brakemanAccount(accounts.doc.value, adapter.value, accountFolder.value))
const accountItems = computed(() => login.value.items)
const accountChoice = computed({
  get: () => login.value.value,
  set: (v: string) => {
    if (form.value) form.value.account = v === OWN_LOGIN ? '' : v
  },
})
// Until the catalog is read the agent's adapter is not known: the server checks it on the save.
const problems = computed(() => (body.value ? configProblems(body.value, agents.value.length ? (adapter.value ?? '') : null) : {}))
/** The server's words for the agent saved (Codex's read-only mode leaves its own MCP servers and exec-policy allow rules). */
const warnings = computed(() => (form.value?.agentId && form.value.agentId === saved.value?.agentId ? (bk.state.value?.warnings ?? []) : []))
/** Where a turn under the saved configuration runs and what it reads besides, while the form holds the saved agent, folder and account. */
const folders = computed(() => {
  const f = form.value
  const s = saved.value
  if (!f || !s || f.agentId !== s.agentId || f.cwd.trim() !== s.cwd || (f.account?.trim() || '') !== (s.account || '')) return null
  return bk.state.value?.folders ?? null
})
/** The fields the form draws a control for, by the names the server gives them; `scope` is the scope's fieldset. */
const controls = computed(() => {
  const out = new Set(['agentId', 'account', 'cwd', 'scope', 'permissions.act', 'budget.turnMinutes', 'budget.readsPerTurn', 'budget.turnsPerDay', 'budget.minutesPerDay', 'wake.windowSeconds', 'wake.cooldownMinutes', 'wake.questionMinutes', 'wake.briefings'])
  for (const k of Object.keys(scopePicked)) out.add(`scope.${k}`)
  if (caps.value?.model) out.add('model')
  if (caps.value?.allowTools) out.add('allowConductorTools')
  return out
})
/** How many of Advanced's fields hold a value refused: said on its toggle, so a folded field's refusal is not missed. */
const advancedRefused = computed(() => ADVANCED.filter((f) => fieldError(f)).length)
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
  { key: 'capacity', label: 'Capacity', description: "Each agent's usage on its accounts, by their labels and the provider's figures, never a folder: what it waits for a reset or holds work by." },
  { key: 'terminals', label: 'Terminal screens', description: "Lets Brakeman read what a session's screen shows, which can hold what no record does. Off until you turn it on." },
]
// What the scope pickers list: Dispatch's projects and the saved crews, read once as the card mounts (a page each, at most
// PICKER_PAGES of the projects), and the runs and sessions the page has live. A read that fails lists nothing: what is typed or saved
// still shows.
const PICKER_PAGES = 5
const dispatchApi = useDispatchApi()
const live = useAttention()
const projects = ref<PickerItem[]>([])
const crews = ref<PickerItem[]>([])
onMounted(async () => {
  try {
    const all: Array<{ key: string; name: string; archivedAt?: string }> = []
    let cursor = ''
    for (let i = 0; i < PICKER_PAGES; i++) {
      const page = await dispatchApi.projects(cursor)
      all.push(...(page.projects ?? []))
      if (!page.next) break
      cursor = page.next
    }
    projects.value = projectPicks(all)
  } catch {
    projects.value = []
  }
})
onMounted(() =>
  api
    .listCrews(0, 500)
    .then((r) => (crews.value = crewPicks(r.crews)))
    .catch(() => (crews.value = [])),
)
const pickers = computed<Array<{ key: Exclude<keyof BrakemanScope, 'repos'>; label: string; placeholder: string; listed: PickerItem[] }>>(() => [
  { key: 'projects', label: 'Projects', placeholder: 'A project, or its key', listed: projects.value },
  { key: 'crews', label: 'Crews', placeholder: "A crew, or its id", listed: crews.value },
  { key: 'runs', label: 'Runs', placeholder: "A run, or its id", listed: runPicks(live.runs.value) },
  { key: 'sessions', label: 'Sessions', placeholder: "A session, or its id", listed: sessionPicks(live.sessions.value) },
])
function addRepo() {
  scopePicked.repos = withEntry(scopePicked.repos, repoText.value)
  repoText.value = ''
}
function addTime() {
  times.value = [...times.value, '']
}

/**
 * Brakeman's part of the page's Save, taken at the click (#707's review): the form as it is now and the store's generation, or null
 * when there is nothing to save. Its `send` saves what was taken: resolves once saved, throws the refusal's words, every edit kept.
 */
function take(): BrakemanPart | null {
  const c = body.value
  if (!c || !dirty.value || Object.keys(problems.value).length) return null
  const sent = JSON.parse(JSON.stringify(c)) as BrakemanConfig
  return { cwdChanged: sent.cwd !== (saved.value?.cwd ?? ''), send: () => send(sent, bk.generation.value) }
}
async function send(c: BrakemanConfig, gen: number) {
  if (bk.generation.value !== gen) throw new Error("The token changed before the save was sent: the configuration shown is this server's.")
  serverError.value = null
  try {
    // An operation that began at the click (utils/settingsSave.ts, the one rule): what was typed since is kept for the next Save.
    await ctl.save(c, gen)
  } catch (e) {
    // Answered after a new token: the form is this server's now (formMove), and the answer is the token before's. Refused stale:
    // the form is left as it is, and the two choices each read what is saved now, for this form only.
    if (bk.generation.value !== gen || e instanceof StaleRefusal) throw e
    const field = e instanceof ApiError && typeof e.details.field === 'string' ? e.details.field : undefined
    serverError.value = { field, message: (e as Error).message }
    throw e
  }
}
/** The first choice of the stale refusal: what is saved now replaces the form, its edits made before the choice dropped (confirmed). */
async function readAgain() {
  readAgainOpen.value = false
  if (await ctl.readAgain()) serverError.value = null
}
/** The second: the form keeps its edits and takes the revision saved now, so the next Save replaces the other change. */
function keepMine() {
  return ctl.keepMine()
}
defineExpose({ dirty, blocked: computed(() => Object.keys(problems.value).length > 0), take })
</script>

<template>
  <UCard v-if="available !== false" :ui="{ header: 'px-4 py-3 sm:px-5', body: 'p-4 sm:p-5' }" data-brakeman-settings>
    <template #header><SettingsCardTitle title="Brakeman" description="An agent of your choosing that reads what happened and answers with briefs. It reads and drafts; it never runs with yolo." /></template>
    <p v-if="!form" class="text-sm text-muted">Reading Brakeman's configuration…</p>
    <form v-else class="flex flex-col gap-4" @submit.prevent>
      <USwitch v-model="form.enabled" label="Brakeman on" description="Off, nothing is asked of its agent." data-brakeman-enabled />
      <UFormField label="Agent" description="Its turns run in the agent's own read-only mode, on your login." :error="fieldError('agentId')" :ui="fieldUi">
        <USelect v-model="agentModel" :items="agentItems" class="w-full" data-brakeman-agent />
      </UFormField>
      <UAlert v-for="w in warnings" :key="w" color="warning" variant="subtle" icon="i-lucide-shield-alert" :title="w" data-brakeman-warning />
      <p v-if="form.agentId && form.agentId !== saved?.agentId" class="text-xs text-muted" data-brakeman-warning-later>Once saved, Conductor says what this agent's read-only mode does not hold, if anything.</p>
      <UFormField v-if="accountItems.length" label="Account" description="Its default login on this machine, or an account added in Accounts." :error="fieldError('account')" :ui="fieldUi">
        <USelect v-model="accountChoice" :items="accountItems" class="w-full" aria-label="Account" data-brakeman-account-select />
        <p v-if="login.unlisted" class="mt-1 text-xs text-muted [overflow-wrap:anywhere]" data-brakeman-account-unlisted>Not in Accounts by this path: add it there to read its usage.</p>
        <p v-if="login.retired" class="mt-1 text-xs text-warning [overflow-wrap:anywhere]" data-brakeman-account-retired>{{ login.retired }} is retired: Brakeman's turns are refused until another account is chosen.</p>
      </UFormField>
      <UFormField v-else label="Account" description="The folder the agent's login lives in (its configuration folder); empty for its default login on this machine." :error="fieldError('account')" :ui="fieldUi">
        <DirInput v-model="form.account" placeholder="/home/me/.claude-work" picker-title="The agent's login folder" data-brakeman-account />
      </UFormField>
      <UFormField v-if="caps?.model" label="Model" description="Empty for the agent's default." :error="fieldError('model')" :ui="fieldUi">
        <ModelField v-model="form.model" :agent="brakemanAgent" placeholder="the agent's default" data-brakeman-model />
      </UFormField>
      <UFormField label="Folder" description="The folder it reads, and changes nothing in. With your home folder its turns run in a folder of their own, which you trust once, and read this one." :error="fieldError('cwd')" :ui="fieldUi">
        <DirInput v-model="form.cwd" picker-title="The folder Brakeman reads" data-brakeman-cwd />
      </UFormField>
      <p v-if="folders" class="text-xs break-all text-muted" data-brakeman-folders>{{ foldersLine(folders, saved?.cwd ?? '') }}</p>
      <UFormField v-if="caps?.allowTools" label="Conductor's tools" :error="fieldError('allowConductorTools')" :ui="fieldUi">
        <USwitch v-model="form.allowConductorTools" label="Run Conductor's tools without asking" description="Without it the agent asks before each of Conductor's tools, and the turn waits on you in Needs you." data-brakeman-allow-tools />
      </UFormField>

      <!-- What it may do, from a preset (#993): the preset only fills the switches below it, which stay drawn. -->
      <fieldset class="flex flex-col gap-2" data-brakeman-presets>
        <legend class="mb-1 text-sm font-medium">What it may do</legend>
        <URadioGroup
          :model-value="preset?.value"
          :items="presetItems"
          variant="card"
          size="sm"
          aria-label="Start from a preset"
          :ui="{ fieldset: 'grid gap-2 @3xl:grid-cols-3', description: 'text-xs leading-snug' }"
          :data-brakeman-preset="preset?.value"
          @update:model-value="choosePreset"
        />
        <p v-if="preset?.hint" class="text-xs" :class="preset.value === 'act' ? 'text-warning' : 'text-muted'" data-brakeman-preset-hint>{{ preset.hint }}</p>
        <div class="grid gap-2 @3xl:grid-cols-2 @3xl:gap-x-8">
          <USwitch v-model="form.permissions.draft" label="Draft" description="Draft tickets, crews and handoffs, which you edit and make." data-brakeman-draft />
          <USwitch v-model="form.permissions.propose" label="Propose" description="Propose operations and prepare decisions, which you take." data-brakeman-propose />
          <p v-if="observesOnly(form.permissions)" class="text-xs text-muted @3xl:col-span-2" data-brakeman-observe-only>Observe only.</p>
        </div>
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

      <!-- Two columns of switches once Settings' column has room for them (#433). -->
      <fieldset class="grid gap-2 @3xl:grid-cols-2 @3xl:gap-x-8">
        <legend class="mb-1 text-sm font-medium">What it may see</legend>
        <USwitch v-for="v in visibility" :key="v.key" v-model="form.visibility[v.key]" :label="v.label" :description="v.description" :data-brakeman-visibility="v.key" />
      </fieldset>

      <fieldset class="flex flex-col gap-2">
        <legend class="mb-1 text-sm font-medium">Its scope</legend>
        <p class="text-xs text-muted">None picked is all of it; each one picked narrows it, at most {{ BOUNDS.selector }} of a kind.</p>
        <div class="grid gap-3 sm:grid-cols-2">
          <UFormField v-for="f in pickers" :key="f.key" :label="f.label" :error="fieldError(`scope.${f.key}`)">
            <BrakemanScopePicker v-model="scopePicked[f.key]" :listed="f.listed" :label="f.label" :placeholder="f.placeholder" :scope-key="f.key" />
          </UFormField>
          <UFormField label="Repositories" :error="fieldError('scope.repos')" class="sm:col-span-2">
            <div class="flex flex-col gap-2" data-brakeman-scope="repos">
              <ul v-if="scopePicked.repos.length" class="flex flex-col gap-1">
                <li v-for="(r, i) in scopePicked.repos" :key="r" class="flex min-w-0 items-center gap-2 text-sm" :data-scope-chip="r">
                  <span class="min-w-0 flex-1 font-mono text-xs break-all" data-mono="path">{{ r }}</span>
                  <IconButton label="Take this repository out" icon="i-lucide-x" color="neutral" variant="ghost" size="sm" @click="scopePicked.repos = withoutAt(scopePicked.repos, i)" />
                </li>
              </ul>
              <div class="flex items-start gap-2">
                <DirInput v-model="repoText" class="min-w-0 flex-1" placeholder="/home/me/code/app" picker-title="A repository Brakeman reads" data-brakeman-repo-input />
                <UButton label="Add" color="neutral" variant="outline" :disabled="!repoText.trim()" data-brakeman-repo-add @click="addRepo" />
              </div>
            </div>
          </UFormField>
        </div>
        <p v-if="fieldError('scope')" class="text-xs text-error" data-brakeman-scope-error>{{ fieldError('scope') }}</p>
      </fieldset>

      <fieldset class="flex flex-col gap-3" data-brakeman-role-settings>
        <legend class="mb-1 text-sm font-medium">In the background</legend>
        <USwitch
          :model-value="form.wake.mode === 'background'"
          label="Wake on events"
          :description="`A task that failed, a question left waiting, a run that ended or paused, a ticket made ready, and the briefing times below. Its turns ask no one anything that pages: their questions wait in Needs you. ${BRIEF_SETTINGS_SENTENCE}`"
          data-brakeman-background
          @update:model-value="setBackground"
        />
        <UFormField label="Briefing times" :description="`In this server's time, at most ${BOUNDS.briefings}; they fire while the server runs.`" :error="fieldError('wake.briefings')" :ui="fieldUi">
          <div class="flex flex-col gap-2" data-brakeman-briefings>
            <div v-for="(_, i) in times" :key="i" class="flex items-center gap-2" data-briefing-time>
              <UInput v-model="times[i]" type="time" class="w-36" :aria-label="`Briefing time ${i + 1}`" />
              <IconButton label="Take this time out" icon="i-lucide-x" color="neutral" variant="ghost" size="sm" @click="times = withoutAt(times, i)" />
            </div>
            <UButton label="Add a time" icon="i-lucide-plus" color="neutral" variant="outline" size="sm" class="self-start" :disabled="times.length >= BOUNDS.briefings" data-brakeman-briefing-add @click="addTime" />
          </div>
        </UFormField>
      </fieldset>

      <!-- The limits, folded: the fold keeps its fields mounted, and its toggle says how many hold a value refused. -->
      <UCollapsible v-model:open="advancedOpen" :unmount-on-hide="false" data-brakeman-advanced>
        <UButton
          :label="advancedRefused ? `Advanced: ${advancedRefused} to fix` : 'Advanced'"
          :color="advancedRefused ? 'error' : 'neutral'"
          variant="ghost"
          :trailing-icon="advancedOpen ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
          class="-ms-2.5"
          :aria-expanded="advancedOpen"
          data-brakeman-advanced-toggle
        />
        <template #content>
          <div class="flex flex-col gap-4 pt-2">
            <p class="text-xs text-muted">Its budget and how it wakes in the background: each turn, each day, and between turns.</p>
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
              <UFormField label="Minutes a day" description="Every turn's, whoever started it." :error="fieldError('budget.minutesPerDay')">
                <UInput v-model.number="form.budget.minutesPerDay" type="number" :min="BOUNDS.minutesPerDay[0]" :max="BOUNDS.minutesPerDay[1]" class="w-full" data-brakeman-minutes />
              </UFormField>
            </fieldset>
            <fieldset class="grid gap-3 sm:grid-cols-3">
              <legend class="mb-1 text-sm font-medium">Waking in the background</legend>
              <UFormField label="Window, seconds" description="How long it gathers wakes into one turn." :error="fieldError('wake.windowSeconds')">
                <UInput v-model.number="form.wake.windowSeconds" type="number" :min="BOUNDS.windowSeconds[0]" :max="BOUNDS.windowSeconds[1]" class="w-full" data-brakeman-window />
              </UFormField>
              <UFormField label="Cooldown, minutes" description="How long it waits after a turn." :error="fieldError('wake.cooldownMinutes')">
                <UInput v-model.number="form.wake.cooldownMinutes" type="number" :min="BOUNDS.cooldownMinutes[0]" :max="BOUNDS.cooldownMinutes[1]" class="w-full" data-brakeman-cooldown />
              </UFormField>
              <UFormField label="A question waits, minutes" description="Before an open question wakes it." :error="fieldError('wake.questionMinutes')">
                <UInput v-model.number="form.wake.questionMinutes" type="number" :min="BOUNDS.questionMinutes[0]" :max="BOUNDS.questionMinutes[1]" class="w-full" data-brakeman-question />
              </UFormField>
            </fieldset>
          </div>
        </template>
      </UCollapsible>

      <UAlert v-for="w in unshown" :key="w" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="w" data-brakeman-save-error />
      <UAlert
        v-if="stale"
        color="warning"
        variant="subtle"
        icon="i-lucide-git-compare-arrows"
        title="Brakeman's settings were changed elsewhere since this form was read"
        description="Your edits here are kept and were not saved. Read again to see what is saved now, or keep yours: the next Save replaces the other change."
        :actions="[
          { label: 'Read again', icon: 'i-lucide-refresh-cw', color: 'neutral', variant: 'outline', disabled: choosing, onClick: () => (readAgainOpen = true) },
          { label: 'Keep mine', icon: 'i-lucide-check', color: 'warning', variant: 'solid', loading: choosing, disabled: choosing, onClick: keepMine },
        ]"
        data-brakeman-stale
      />
      <p v-if="stale && staleError" class="text-xs text-error" data-brakeman-stale-error>{{ staleError }}</p>
      <NuxtLink v-if="saved?.enabled" to="/brakeman" class="text-sm underline">Open Brakeman</NuxtLink>
    </form>
    <UModal v-model:open="readAgainOpen" title="Read Brakeman's settings again?" description="What is saved now replaces this form: the edits made here will be lost.">
      <template #footer>
        <div class="flex w-full justify-end gap-2">
          <UButton label="Cancel" color="neutral" variant="ghost" @click="readAgainOpen = false" />
          <UButton label="Read it again" icon="i-lucide-refresh-cw" color="warning" data-brakeman-read-again @click="readAgain" />
        </div>
      </template>
    </UModal>
  </UCard>
</template>
