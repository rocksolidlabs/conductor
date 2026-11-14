<script setup lang="ts">
import type { AgentInfo, CrewInfo, CrewSummary, RunInfo } from '~/composables/useSessions'
import { ApiError } from '~/composables/useApi'
import { crewKey, defaultCrew, holdViewLink, keepParked, memberNameError, pageAfterDelete, peopleProblems, refusedAsStale, revisionToSave, elsewhereWords, summaryOf, toDraft, type DraftCrew, taskProblems, taskSaveProblems } from '~/utils/crews'
import { changedFields, liveRunLine, startedClock } from '~/utils/crewWords'
import { runLive, runState } from '~/utils/runs'

// Two things, told apart: a crew is a saved plan, a run is one launch of it.
// /crews is the home: the runs going now, then the saved crews. /crews/<id>
// is one saved crew: its Setup and its Runs; /crews/new a crew not saved
// yet. One page under one key, so moving between them never remounts it.
// Drafts live in app state: an edit survives moving to another page until
// it is saved or discarded.

definePageMeta({ key: 'crews' })

const route = useRoute()
const router = useRouter()
const api = useSessions()
const admin = useWorkbenchToken()
const toast = useToast()
const live = useAttention()
const serverHost = useServerHost()
serverHost.load()

/** The key of a crew never saved in `drafts`, and its route. */
const NEW = ''
const NEW_ROUTE = 'new'

/** Crews the table shows at a time. */
const PAGE_SIZE = 100

/** The page of the table on screen, as GET /api/crews lists it. */
const crews = useState<CrewSummary[]>('crews', () => [])
const total = useState<number>('crewsTotal', () => 0)
const page = useState<number>('crewsPage', () => 1)
/** Crews read in full, by id: the one on screen, and any whose draft is open. */
const full = useState<Record<string, CrewInfo>>('crewsFull', () => ({}))
/** Why the crew's file cannot be used, when the server says so. */
const unreadable = ref('')
/** The id of the crew being read in full: the page shows a loading state meanwhile, never "No crew has the id". */
const crewLoading = ref<string>()
const agents = useState<AgentInfo[]>('crewAgents', () => [])
/** The server's yolo default, for the editor. */
const yoloDefault = useState<boolean>('crewYoloDefault', () => false)
const drafts = useState<Record<string, DraftCrew>>('crewDrafts', () => ({}))
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const saving = ref(false)
/** The key of the crew being launched: a crew's id, or '' for the new draft. */
const launchingKey = ref<string>()
const now = ref(Date.now())

const routeId = computed(() => (typeof route.params.id === 'string' && route.params.id ? route.params.id : undefined))
const home = computed(() => routeId.value === undefined)
const isNew = computed(() => routeId.value === NEW_ROUTE)
/** The key of the crew on screen: '' for the new draft, undefined on the home. */
const selectedKey = computed<string | undefined>(() => (isNew.value ? NEW : routeId.value))
/** The id of the saved crew on screen; '' on the home and for the new draft. */
const crewId = computed(() => (home.value || isNew.value ? '' : routeId.value!))
const saved = computed(() => (crewId.value ? full.value[crewId.value] : undefined))
const draft = computed<DraftCrew | undefined>({
  get: () => (selectedKey.value === undefined ? undefined : drafts.value[selectedKey.value]),
  set: (d) => {
    if (d && selectedKey.value !== undefined) drafts.value = { ...drafts.value, [selectedKey.value]: d }
  },
})
useHead({ title: computed(() => (home.value ? 'Crews' : `${draft.value?.name || (isNew.value ? 'New crew' : routeId.value)} · Crews`)) })

/** The crew page's tab, in the URL so a link can open the runs (?tab=runs). */
const tab = computed<'setup' | 'runs'>({
  get: () => (route.query.tab === 'runs' ? 'runs' : 'setup'),
  set: (t) => router.replace({ query: { ...route.query, tab: t === 'runs' ? 'runs' : undefined } }),
})

async function refresh() {
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  loading.value = true
  try {
    const [list, a] = await Promise.all([api.listCrews((page.value - 1) * PAGE_SIZE, PAGE_SIZE), api.catalogInfo()])
    // A page past the end (crews deleted elsewhere) moves back to the last one, and its watcher reads it.
    const last = Math.max(1, Math.ceil(list.total / PAGE_SIZE))
    if (page.value > last) {
      page.value = last
      return
    }
    crews.value = list.crews
    total.value = list.total
    agents.value = a.agents
    yoloDefault.value = a.yoloDefault
    error.value = ''
    loaded.value = true
    await loadSelected()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

/** Reads the crew on screen in full. A draft nobody edited follows it as saved now: another admin, or conductor crews, may have changed it. */
async function loadSelected() {
  const id = crewId.value
  unreadable.value = ''
  if (!id) return
  crewLoading.value = id
  try {
    const fresh = await api.getCrew(id)
    const before = full.value[id]
    const d = drafts.value[id]
    if (d && before && crewKey(d) === crewKey(before)) drafts.value = { ...drafts.value, [id]: keepParked(toDraft(fresh), d) }
    full.value = { ...full.value, [id]: fresh }
  } catch (e) {
    const next = { ...full.value }
    delete next[id]
    full.value = next
    // An answer for a crew no longer on screen says nothing about the one that is.
    if (crewId.value !== id) return
    if (e instanceof ApiError && e.code === 'crew_unreadable') unreadable.value = e.message
    else if (!(e instanceof ApiError && e.status === 404)) error.value = (e as Error).message
  } finally {
    if (crewLoading.value === id) crewLoading.value = undefined
  }
}
watch(page, refresh)

// A draft for the crew on screen: seeded from the crew read in full once it is read, or, at /crews/new, from the catalog's first agent.
watch(
  [selectedKey, full, loaded, agents],
  () => {
    if (!loaded.value || selectedKey.value === undefined || drafts.value[selectedKey.value]) return
    if (selectedKey.value === NEW) {
      if (agents.value.length) drafts.value = { ...drafts.value, [NEW]: toDraft(defaultCrew(agents.value)) }
      return
    }
    if (saved.value) drafts.value = { ...drafts.value, [selectedKey.value]: toDraft(saved.value) }
  },
  { immediate: true },
)

function isDirty(key: string): boolean {
  const d = drafts.value[key]
  if (!d) return false
  if (key === NEW) return true
  // A draft is seeded from `full`, so the crew is there; without it (a new token cleared it) the draft counts as changed.
  const c = full.value[key]
  return !c || crewKey(d) !== crewKey(c)
}
const dirty = computed(() => selectedKey.value !== undefined && isDirty(selectedKey.value))
/** The unsaved-changes bar's words: what changed, and that a live run keeps the version it launched with. */
const changeWords = computed(() => {
  if (selectedKey.value === NEW) return 'Save to keep it.'
  const fields = draft.value ? changedFields(draft.value, saved.value) : []
  const edited = fields.length ? `${fields.join(', ')} edited.` : 'Edited.'
  return crewLive.value.length ? `${edited} The live run keeps the version it launched with.` : edited
})

/** Runs of a crew in the server's memory, newest first, from the live store: run events keep them current. */
function runsOf(id: string): RunInfo[] {
  return id ? live.runs.value.filter((r) => r.crewId === id) : []
}
function isLive(r: RunInfo): boolean {
  return runLive(runState(r, live.sessions.value).state)
}
/** Every run going now, newest first: the home's "Running now". */
const liveRuns = computed(() => live.runs.value.filter(isLive))
/** The runs of the crew on screen, and the ones going now. */
const crewRuns = computed(() => runsOf(crewId.value))
const crewLive = computed(() => crewRuns.value.filter(isLive))
/** The crew's runs with the server's records of those that ended: the Runs tab and its count. */
const records = useCrewRecords(() => crewId.value, crewRuns)
const tabItems = computed(() => [
  { label: 'Setup', value: 'setup' },
  { label: 'Runs', value: 'runs', badge: records.runs.value.length || undefined },
])
/** The live-run banner's lines, one per run going now. */
const banners = computed(() => crewLive.value.map((r) => ({ run: r, line: liveRunLine(r, live.sessions.value) })))

const nameMissing = computed(() => !draft.value?.name.trim())
/** Why the draft breaks a rule every crew is held to, saved or launched, in words; '' when it does not. */
const ruleProblem = computed(() => {
  const d = draft.value
  if (!d) return 'Loading'
  if (nameMissing.value) return 'The crew needs a name'
  for (const [i, m] of d.members.entries()) {
    const err = memberNameError(m.name, d.members.filter((_, j) => j !== i).map((x) => x.name))
    if (err) return err
    if (!m.agentId) return `${m.name || 'A member'} needs an agent`
  }
  // The server checks the tasks, then the people (internal/crew Validate).
  return taskProblems(d)[0] ?? peopleProblems(d)[0] ?? ''
})
/** Why a save of the draft would be refused by the rules a crew is held to as it is saved only (taskSaveProblems); '' when not. */
const saveOnlyProblem = computed(() => (draft.value ? (taskSaveProblems(draft.value)[0] ?? '') : ''))
/** Why the draft cannot be saved, in words; '' when it can. The Save button says it; the server checks the same things. */
const saveProblem = computed(() => ruleProblem.value || saveOnlyProblem.value)
const invalid = computed(() => !!saveProblem.value)
/** What stops a launch from the crew page; the server still decides (a 409 answers). */
const launchBlocked = computed(() => {
  const d = draft.value
  if (!d) return 'Loading'
  if (serverHost.switchyard.value) return 'A switchyard launches nothing'
  if (!d.members.length) return 'Add a member first'
  if (d.where !== 'server') return 'Only a crew that runs on the server can be launched'
  // A launch saves the draft first only when it changed or is new: an unchanged crew saved before the save-only rules launches.
  if (ruleProblem.value || ((dirty.value || !d.id) && saveOnlyProblem.value)) return 'Fix the fields marked in red first'
  return ''
})
const subtitle = computed(() => {
  const d = draft.value
  if (!d) return ''
  return `${d.id ? `crews/${d.id}.json` : 'not saved yet'} · ${d.members.length} ${d.members.length === 1 ? 'agent' : 'agents'}`
})

function newCrew() {
  router.push(`/crews/${NEW_ROUTE}`)
}

const seeding = ref(false)

/**
 * Seeds the example crews and opens the first one; what already existed is left as it is. The button shows on the empty page only, which
 * is page 1 with a total of 0 (refresh steps back from a page past the end), so reading the list again lists the examples there. The list
 * is read again on failure too: a seed that stops part way keeps the crews it saved before it stopped.
 */
async function loadExamples() {
  seeding.value = true
  let first: string | undefined
  try {
    const r = await api.loadExampleCrews()
    toast.add({
      title: r.added.length ? `${r.added.length} example ${r.added.length === 1 ? 'crew' : 'crews'} added` : 'The examples are here already',
      description: 'Edit or delete them like any crew.',
      icon: 'i-lucide-package-open',
      // Nothing added: every example's id has a file already, on this empty page one the server cannot use (or another admin's seed).
      color: r.added.length ? 'success' : 'neutral',
    })
    first = r.added[0] ?? r.skipped[0]
  } catch (e) {
    fail('Loading the examples failed', e)
  } finally {
    await refresh()
    seeding.value = false
  }
  if (first) await router.push(`/crews/${encodeURIComponent(first)}`)
}

function fail(title: string, e: unknown) {
  toast.add({ title, description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
}

function setName(name: string) {
  if (draft.value) draft.value = { ...draft.value, name }
}

/**
 * Saves the draft on screen and returns the crew as saved; undefined when the server refused it (a toast says why). An update is made from the
 * copy the draft was made from (its base's revision), or, `anyway`, from the copy the server holds now as read after a refusal: the person
 * chose to replace the change made elsewhere. When another save came between, the server refuses it and nothing is saved; the draft stays
 * exactly as typed, marked, and the crew as saved now is read so the page can say what changed (readTheirs). Never saved again by itself.
 */
async function save(anyway = false): Promise<CrewInfo | undefined> {
  const key = selectedKey.value
  const d = draft.value
  if (key === undefined || !d) return undefined
  const revision = revisionToSave(d, anyway)
  // Save mine anyway goes over a copy the page has read, never unconditionally.
  if (anyway && !revision) return undefined
  saving.value = true
  try {
    const c = await api.saveCrew(d, d.id, revision)
    full.value = { ...full.value, [c.id]: c }
    if (crews.value.some((x) => x.id === c.id)) crews.value = crews.value.map((x) => (x.id === c.id ? summaryOf(c) : x))
    else crews.value = [...crews.value, summaryOf(c)].sort((a, b) => a.name.localeCompare(b.name))
    // Only a new crew adds to the count: one opened by its link from another page was counted already.
    if (key === NEW) total.value += 1
    // A new crew moves to its page before its draft goes, so /crews/new never shows without it in between.
    if (key === NEW) await router.replace(`/crews/${encodeURIComponent(c.id)}`)
    // The lists the file switch set aside stay with the draft: turning it off gives them back after the save.
    const next = { ...drafts.value, [c.id]: keepParked(toDraft(c), d) }
    if (key === NEW) delete next[NEW]
    drafts.value = next
    return c
  } catch (e) {
    // The draft as it is now, edits made while the save was out included.
    const now = drafts.value[key]
    if (e instanceof ApiError && e.code === 'stale_revision' && now && key !== NEW) {
      drafts.value = { ...drafts.value, [key]: refusedAsStale(now) }
      void readTheirs(key)
    } else fail(e instanceof ApiError && e.code === 'invalid_crew' ? 'The server refused the crew' : 'Save failed', e)
    return undefined
  } finally {
    saving.value = false
  }
}

/** The crew being read as saved now after a refused save, by id, and why such a read failed, by id. */
const theirsReading = ref<string>()
const theirsError = ref<Record<string, string>>({})

/** Reads the crew as the server holds it now after a refused save and keeps it beside the draft (DraftCrew.theirs); the draft itself is not touched. */
async function readTheirs(id: string) {
  theirsReading.value = id
  theirsError.value = { ...theirsError.value, [id]: '' }
  try {
    const theirs = await api.getCrew(id)
    full.value = { ...full.value, [id]: theirs }
    const d = drafts.value[id]
    // Only a draft still marked: one read again meanwhile, or saved, has nothing to say.
    if (d?.changedElsewhere) drafts.value = { ...drafts.value, [id]: refusedAsStale(d, theirs) }
  } catch (e) {
    theirsError.value = { ...theirsError.value, [id]: (e as Error).message }
  } finally {
    if (theirsReading.value === id) theirsReading.value = undefined
  }
}

/** The choice the person is confirming after a refused save: read the crew again (losing the edits here), or save theirs over the other. */
const staleChoice = ref<'read' | 'save'>()
const staleChoiceOpen = computed({
  get: () => !!staleChoice.value,
  set: (open: boolean) => {
    if (!open) staleChoice.value = undefined
  },
})
const staleText = computed(() => (draft.value ? elsewhereWords(draft.value) : ''))
const staleDescription = computed(() => {
  const head = 'Your save was refused and nothing was saved; your edits are still here.'
  const id = crewId.value
  if (theirsReading.value === id) return `${head} Reading what changed there…`
  if (theirsError.value[id]) return `${head} Reading what changed there failed: ${theirsError.value[id]}`
  return `${head} Changed there: ${staleText.value}.`
})
const reading = ref(false)

/** Read the crew again: the crew as saved now replaces the draft, its edits lost (the person confirmed). */
async function readAgain() {
  const id = crewId.value
  if (!id) return
  reading.value = true
  try {
    const fresh = await api.getCrew(id)
    full.value = { ...full.value, [id]: fresh }
    drafts.value = { ...drafts.value, [id]: toDraft(fresh) }
    staleChoice.value = undefined
  } catch (e) {
    fail('Reading the crew again failed', e)
  } finally {
    reading.value = false
  }
}

/** Save mine anyway: the whole draft over the copy the server holds now (the person confirmed); refused again when it changed once more. */
async function saveAnyway() {
  staleChoice.value = undefined
  // The draft's own rules hold as for any save (the Save button's).
  if (invalid.value) return
  if (await save(true)) toast.add({ title: 'Crew saved', description: 'It replaced the change made elsewhere.', icon: 'i-lucide-check', color: 'success' })
}

async function onSave() {
  if (await save()) toast.add({ title: 'Crew saved', icon: 'i-lucide-check', color: 'success' })
}

function discard() {
  const key = selectedKey.value
  if (key === undefined) return
  const next = { ...drafts.value }
  delete next[key]
  if (key !== NEW && saved.value) next[key] = toDraft(saved.value)
  drafts.value = next
  if (key === NEW) router.push('/crews')
}

async function duplicate() {
  const c = saved.value
  if (!c) return
  try {
    const copy = await api.duplicateCrew(c.id)
    full.value = { ...full.value, [copy.id]: copy }
    crews.value = [...crews.value, summaryOf(copy)].sort((a, b) => a.name.localeCompare(b.name))
    total.value += 1
    toast.add({ title: 'Crew duplicated', description: isDirty(c.id) ? `${copy.name}, from what ${c.name} has saved` : copy.name, icon: 'i-lucide-copy', color: 'success' })
    router.push(`/crews/${encodeURIComponent(copy.id)}`)
  } catch (e) {
    fail('Duplicate failed', e)
  }
}

const deleteOpen = ref(false)
const deleting = ref(false)

async function confirmDelete() {
  // A crew whose file cannot be used has no `saved`: it is deleted by the id on screen.
  const id = saved.value?.id ?? crewId.value
  if (!id) return
  const name = saved.value?.name ?? id
  // Only a crew the server could read counts in `total`.
  const counted = !!saved.value
  deleting.value = true
  try {
    await api.deleteCrew(id)
    crews.value = crews.value.filter((x) => x.id !== id)
    if (counted) total.value = Math.max(0, total.value - 1)
    const nextFull = { ...full.value }
    delete nextFull[id]
    full.value = nextFull
    const next = { ...drafts.value }
    delete next[id]
    drafts.value = next
    unreadable.value = ''
    deleteOpen.value = false
    toast.add({ title: 'Crew deleted', description: name, icon: 'i-lucide-trash-2', color: 'neutral' })
    router.replace('/crews')
    // A page the delete emptied steps back, and its watcher reads it: the table never shows "No crews yet" while there are crews.
    page.value = pageAfterDelete(page.value, crews.value.length)
  } catch (e) {
    fail('Delete failed', e)
  } finally {
    deleting.value = false
  }
}

// Naming a run is one step more than launching one, beside the button.
const nameOpen = ref(false)
const runName = ref('')
function launchNamed() {
  const label = runName.value
  nameOpen.value = false
  runName.value = ''
  launch('', label)
}

const menu = computed(() => [[{ label: 'Delete crew', icon: 'i-lucide-trash-2', color: 'error' as const, onSelect: () => (deleteOpen.value = true) }]])

/**
 * The view link a launch returned, until this page has shown it: in the page's memory (usePageMemory), so a launch that resolves after
 * the page was left keeps it for the next visit, and a toast offers it meanwhile. Its token is in no other state, and in none Nuxt
 * writes at a reload.
 */
const shownLink = usePageMemory<{ url: string; runId: string; name: string; hours: number } | null>('crewShownLink', () => null)
let mounted = false
const shownOpen = computed({
  get: () => !!shownLink.value,
  set: (open: boolean) => {
    if (!open) shownLink.value = null
  },
})
const copy = useCopy()

function hours(ttlSeconds: number): number {
  return Math.round((ttlSeconds / 3600) * 10) / 10
}

/**
 * Launches a run. From the crew page (`id` empty) what changed is saved
 * first; from a row of the home the crew runs as saved. The server makes
 * the crew's view link, if it asks for one, and returns it this once: the
 * run page shows it when the launch opens that, else a dialog here does.
 */
async function launch(id = '', label = '') {
  const key = id ? id : selectedKey.value
  if (key === undefined) return
  launchingKey.value = key
  try {
    let c: CrewInfo | undefined
    if (id) {
      try {
        c = full.value[id] ?? (await api.getCrew(id))
      } catch (e) {
        fail('Launch failed', e)
        return
      }
    } else c = isDirty(key) ? await save() : saved.value
    if (!c) return
    let launched: Awaited<ReturnType<typeof api.launchCrew>>
    try {
      launched = await api.launchCrew(c.id, label.trim() ? { label: label.trim() } : undefined)
    } catch (e) {
      if (e instanceof ApiError && e.code === 'not_a_repo') fail('Not a git repository', e)
      else fail('Launch failed', e)
      return
    }
    const { run, viewLink } = launched
    live.applyRun(run)
    const ttl = c.viewLinkTtlSeconds ?? 0
    if (c.openAfterLaunch) {
      if (viewLink) holdViewLink(run.id, viewLink.url, ttl)
      await router.push(`/runs/${encodeURIComponent(run.id)}`)
      return
    }
    if (viewLink) {
      shownLink.value = { url: viewLink.url, runId: run.id, name: c.name, hours: hours(ttl) }
      if (!mounted) {
        toast.add({
          title: `${c.name}: run started`,
          description: 'Its view link is shown once, on the Crews page.',
          icon: 'i-lucide-link',
          color: 'success',
          actions: [{ label: 'Show the link', onClick: () => router.push(`/crews/${encodeURIComponent(c.id)}`) }],
        })
      }
      return
    }
    toast.add({
      title: `${c.name}: ${run.label || `run started ${startedClock(run.startedAt)}`}`,
      icon: 'i-lucide-circle-play',
      color: 'success',
      actions: [{ label: 'Open run', icon: 'i-lucide-arrow-right', onClick: () => router.push(`/runs/${encodeURIComponent(run.id)}`) }],
    })
  } finally {
    launchingKey.value = undefined
  }
}

let tick: number | undefined
onMounted(() => {
  mounted = true
  live.start()
  refresh()
  tick = window.setInterval(() => (now.value = Date.now()), 30000)
})
onBeforeUnmount(() => {
  mounted = false
  window.clearInterval(tick)
})
watch(
  () => admin.token.value,
  () => {
    // Another token may be another server: nothing typed or read under the
    // old one stays, and neither does a view link held from it.
    drafts.value = {}
    crews.value = []
    full.value = {}
    total.value = 0
    shownLink.value = null
    if (page.value !== 1) page.value = 1 // its watcher reads the list
    else refresh()
  },
)
watch(crewId, loadSelected)

// This page's group in the command palette (#302): a new crew, the examples, and Launch for every crew listed.
usePalette().registerPage(() => ({
  label: 'Crews',
  rows: [
    { key: 'page:new', label: 'New crew', icon: 'i-lucide-plus', run: newCrew },
    { key: 'page:examples', label: 'Load the example crews', icon: 'i-lucide-package-open', run: loadExamples },
    ...crews.value.map((c) => ({
      key: `page:launch:${c.id}`,
      label: `Launch ${c.name}`,
      detail: c.goal || `${c.members.length} ${c.members.length === 1 ? 'member' : 'members'}`,
      icon: 'i-lucide-play',
      note: c.where === 'host' ? 'Runs on another machine: open it to launch from there' : undefined,
      disabled: c.where === 'host',
      run: () => launch(c.id),
      field: c.where === 'host' ? undefined : { placeholder: 'Name the run…', maxLength: 60, button: 'Launch named', submit: (label: string) => launch(c.id, label.trim()) },
      actions: c.where === 'host' ? undefined : [{ id: 'field', kind: 'page' as const, target: c.id, label: 'Launch named…', icon: 'i-lucide-tag', kbds: ['L'], run: () => {} }],
    })),
  ],
}))
</script>

<template>
  <UDashboardPanel id="crews" :ui="{ body: 'p-0 sm:p-0 gap-0' }">
    <template #header>
      <UDashboardNavbar :toggle="false" v-if="home" title="Crews">
        <template #right>
          <UButton label="New crew" icon="i-lucide-plus" color="neutral" variant="outline" data-new-crew @click="newCrew" />
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
      <UDashboardNavbar :toggle="false" v-else :ui="{ root: 'h-14' }" data-crew-header>
        <template #title>
          <div class="flex min-w-0 items-center gap-2">
            <NuxtLink to="/crews" class="flex-none text-[15px] text-muted hover:text-default">Crews /</NuxtLink>
            <UInput
              v-if="draft"
              :model-value="draft.name"
              variant="ghost"
              maxlength="60"
              placeholder="Crew name"
              aria-label="Crew name"
              :color="nameMissing ? 'error' : undefined"
              :highlight="nameMissing"
              :ui="{ base: 'px-1 -mx-1 text-[17px] md:text-[17px] font-semibold text-highlighted' }"
              class="min-w-0 max-w-xs flex-1"
              @update:model-value="setName(String($event))"
            />
            <span v-else class="truncate text-[17px] font-semibold text-highlighted">{{ routeId }}</span>
            <UBadge v-if="draft" :label="draft.id ? 'Saved crew' : 'New crew'" :icon="draft.id ? 'i-lucide-bookmark' : 'i-lucide-pencil'" color="neutral" variant="outline" size="sm" class="hidden flex-none sm:inline-flex" data-crew-kind />
            <span v-if="draft" class="hidden truncate font-mono text-[11.5px] text-muted lg:inline">{{ subtitle }}</span>
          </div>
        </template>
        <template #right>
          <UButton v-if="saved" icon="i-lucide-copy" color="neutral" variant="outline" aria-label="Duplicate" :disabled="saving || !!launchingKey" @click="duplicate"><span class="hidden sm:inline">Duplicate</span></UButton>
          <UDropdownMenu v-if="saved || unreadable" :items="menu" :content="{ align: 'end' }">
            <UButton icon="i-lucide-ellipsis" color="neutral" variant="outline" aria-label="More" />
          </UDropdownMenu>
          <UTooltip :text="launchBlocked" :disabled="!launchBlocked">
            <UFieldGroup>
              <UButton icon="i-lucide-play" :loading="launchingKey === selectedKey" :disabled="!!launchBlocked || saving" aria-label="Launch run" data-launch @click="launch()"><span class="hidden sm:inline">Launch run</span></UButton>
              <UPopover v-model:open="nameOpen" :content="{ align: 'end' }">
                <UButton icon="i-lucide-chevron-down" :disabled="!!launchBlocked || saving" aria-label="Name this run" class="border-l border-inverted/20" data-launch-menu />
                <template #content>
                  <form class="flex w-72 flex-col gap-2 p-3" @submit.prevent="launchNamed">
                    <span class="text-sm font-medium text-highlighted">Name this run</span>
                    <UInput v-model="runName" maxlength="60" placeholder="what this run is for" autofocus aria-label="Run name" data-launch-name />
                    <span class="text-xs text-muted">Shown wherever the run is named; without one the pages say when it started.</span>
                    <UButton type="submit" label="Launch named" icon="i-lucide-play" :disabled="!runName.trim()" class="self-end" data-launch-named />
                  </form>
                </template>
              </UPopover>
            </UFieldGroup>
          </UTooltip>
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <!-- The home: the runs going now, then the saved crews. -->
      <div v-if="home" class="flex flex-col gap-7 p-4 sm:p-6" data-crews-home>
        <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
        <UAlert v-if="serverHost.switchyard.value" color="neutral" variant="subtle" icon="i-lucide-train-track" title="This server is a switchyard" description="It launches no crews: run them from a Conductor of your own and share them here." data-switchyard-notice />

        <section v-if="liveRuns.length" class="flex flex-col gap-2.5" data-running-now>
          <div class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
            <span class="text-[11px] font-semibold uppercase tracking-wider text-muted">Running now · {{ liveRuns.length }}</span>
            <span class="text-xs text-muted">launches of a saved crew; each member is a live session</span>
          </div>
          <div class="grid gap-3 xl:grid-cols-2" data-crew-runs>
            <CrewRunCard v-for="r in liveRuns" :key="r.id" :run="r" :sessions="live.sessions.value" :now="now" />
          </div>
        </section>

        <section class="flex flex-col gap-2.5" data-saved-crews>
          <div class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
            <span class="text-[11px] font-semibold uppercase tracking-wider text-muted">Saved crews · {{ total }}</span>
            <span class="text-xs text-muted">plans: who, which agent, which prompt, who waits for whom. Nothing runs until you launch one</span>
          </div>
          <CrewsTable v-if="crews.length" :crews="crews" :runs="live.runs.value" :sessions="live.sessions.value" :now="now" :launching="launchingKey ?? ''" @launch="launch($event)" />
          <UEmpty
            v-else-if="loaded"
            icon="i-lucide-users"
            title="No crews yet"
            description="A crew is a saved plan: agents, each with a first prompt and a start rule, in one working directory or a git worktree each. A run is one launch of it, here or with conductor up <crew>. The examples show four shapes of crew by start rules and one by tasks, where Yardmaster gives each member its tasks as the results they need come in; they are ordinary crews once loaded."
            :actions="[
              { label: 'New crew', icon: 'i-lucide-plus', onClick: newCrew },
              { label: 'Load the examples', icon: 'i-lucide-package-open', color: 'neutral', variant: 'outline', loading: seeding, onClick: loadExamples },
            ]"
            data-crews-empty
          />
          <div v-else-if="loading" class="flex items-center gap-2 text-sm text-muted"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Loading crews…</div>
          <p v-if="crews.length" class="text-xs text-muted">Bars: the last runs' durations, coloured by how each ended (green finished, grey stopped, red a member failed; amber edge = someone had to answer).</p>
          <UPagination v-if="total > PAGE_SIZE" v-model:page="page" :total="total" :items-per-page="PAGE_SIZE" size="xs" class="self-center" />
        </section>
      </div>

      <!-- One saved crew, or the new draft. -->
      <template v-else>
        <div class="flex min-h-full shrink-0 flex-col gap-4 p-4 sm:p-6" :class="dirty && draft && 'pb-2'" data-crew-page>
          <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
          <div v-if="unreadable" class="flex flex-col items-start gap-3" data-crew-unreadable>
            <UAlert color="error" variant="subtle" icon="i-lucide-file-warning" :title="`The file of ${routeId} cannot be used`" :description="unreadable" />
            <UButton label="Delete it" icon="i-lucide-trash-2" color="error" variant="soft" @click="deleteOpen = true" />
          </div>
          <template v-else-if="draft">
            <div v-for="b in banners" :key="b.run.id" class="flex flex-wrap items-center gap-x-2.5 gap-y-1 rounded-md border border-warning bg-warning/10 px-3.5 py-2.5 text-[13.5px]" data-live-run-banner :data-run="b.run.id">
              <UIcon name="i-lucide-circle-play" class="size-4 flex-none text-success" />
              <b class="font-semibold text-highlighted">live run</b>
              <span class="min-w-0 text-muted">
                started {{ b.line.started }}
                <template v-if="b.line.running"> · {{ b.line.running }} running</template>
                <template v-if="b.line.needs.length"> · <span class="text-warning">{{ b.line.needs.join(', ') }} need{{ b.line.needs.length === 1 ? 's' : '' }} you</span></template>
                <template v-if="b.line.waits.length"> · {{ b.line.waits.join(', ') }} wait{{ b.line.waits.length === 1 ? 's' : '' }} for Start now</template>
              </span>
              <NuxtLink :to="`/runs/${encodeURIComponent(b.run.id)}`" class="ml-auto flex flex-none items-center gap-1 font-medium text-highlighted hover:underline">Open run <UIcon name="i-lucide-arrow-right" class="size-3.5" /></NuxtLink>
            </div>
            <UAlert
              v-if="draft.changedElsewhere"
              color="warning"
              variant="subtle"
              icon="i-lucide-git-compare-arrows"
              title="This crew was changed elsewhere since you opened it"
              :description="staleDescription"
              :actions="[
                { label: 'Read the crew again', icon: 'i-lucide-refresh-cw', color: 'neutral', variant: 'outline', loading: reading, onClick: () => (staleChoice = 'read') },
                { label: 'Save mine anyway', icon: 'i-lucide-save', color: 'warning', variant: 'solid', loading: saving, disabled: !draft.theirs || invalid, onClick: () => (staleChoice = 'save') },
              ]"
              data-crew-stale
            />
            <UTabs v-model="tab" :items="tabItems" :content="false" color="neutral" variant="link" size="sm" class="-mb-1" data-crew-tabs />
            <CrewEditor v-if="tab === 'setup'" :key="selectedKey" v-model="draft" :agents="agents" :yolo-default="yoloDefault" />
            <CrewRunsPanel v-else :runs="records.runs.value" :sessions="live.sessions.value" :now="now" :error="records.error.value" />
          </template>
          <div v-else-if="crewLoading === crewId || !loaded" class="flex items-center gap-2 text-sm text-muted" data-crew-loading>
            <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Loading the crew…
          </div>
          <div v-else-if="isNew" class="flex items-center gap-2 text-sm text-muted">
            <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Reading the catalog…
          </div>
          <div v-else class="flex flex-col items-start gap-3 text-sm text-muted">
            <p>No crew has the id <code>{{ routeId }}</code>.</p>
            <UButton label="All crews" icon="i-lucide-arrow-left" color="neutral" variant="soft" to="/crews" />
          </div>
        </div>
        <!-- The unsaved-changes bar stays in view at the bottom of the page. -->
        <div v-if="dirty && draft" class="sticky bottom-0 px-4 pb-4 pt-2 sm:px-6">
          <!-- The app's own elevated surface in both themes; the forest edge and mark say there is unsaved work. -->
          <div class="flex flex-wrap items-center gap-x-2.5 gap-y-2 rounded-lg border border-default border-l-4 border-l-primary bg-elevated px-3.5 py-2.5 text-[13.5px] text-default shadow-lg" data-unsaved-bar>
            <UIcon name="i-lucide-pencil" class="size-4 flex-none text-primary" />
            <b class="font-semibold text-highlighted">{{ draft.id ? 'Unsaved changes to the saved crew' : 'New crew, not saved yet' }}</b>
            <span class="min-w-0 text-muted" data-unsaved-words>{{ changeWords }}</span>
            <div class="ml-auto flex flex-none items-center gap-2">
              <UButton label="Discard" color="neutral" variant="outline" size="sm" :disabled="saving" @click="discard" />
              <UButton label="Save" color="primary" size="sm" :loading="saving" :disabled="invalid || saving || !!launchingKey" :title="saveProblem || undefined" :data-save-problem="saveProblem || undefined" @click="onSave" />
            </div>
          </div>
        </div>
      </template>

      <UModal
        v-model:open="shownOpen"
        :title="`${shownLink?.name ?? 'The crew'}: run started`"
        :description="`Its view link lets anyone watch every agent of the run, view only, for ${shownLink?.hours ?? 0} h. It is shown this once: copy it now.`"
        data-view-link
      >
        <template #body>
          <div class="flex items-start gap-2.5 rounded-md bg-elevated px-3.5 py-3">
            <code class="min-w-0 flex-1 break-all font-mono text-xs select-all" data-view-link-url>{{ shownLink?.url }}</code>
            <UButton label="Copy" icon="i-lucide-clipboard" size="sm" class="flex-none" @click="shownLink && copy(shownLink.url, 'Link copied', 'Anyone with it can watch every member.')" />
          </div>
        </template>
        <template #footer>
          <div class="flex w-full justify-end gap-2">
            <UButton label="Open run" icon="i-lucide-arrow-right" color="neutral" variant="outline" :to="shownLink ? `/runs/${encodeURIComponent(shownLink.runId)}` : undefined" @click="shownLink = null" />
            <UButton label="Done" @click="shownLink = null" />
          </div>
        </template>
      </UModal>

      <UModal
        v-model:open="staleChoiceOpen"
        :title="staleChoice === 'save' ? 'Save yours over the other change?' : 'Read the crew again?'"
        :description="
          staleChoice === 'save'
            ? `The crew as it is on this page replaces the one saved elsewhere, and what was changed there is lost: ${staleText}.`
            : 'The crew as saved now replaces what is on this page: the edits made here will be lost.'
        "
      >
        <template #footer>
          <div class="flex w-full justify-end gap-2" data-crew-stale-confirm>
            <UButton label="Cancel" color="neutral" variant="ghost" @click="staleChoice = undefined" />
            <UButton v-if="staleChoice === 'save'" label="Save mine" icon="i-lucide-save" color="warning" :loading="saving" @click="saveAnyway" />
            <UButton v-else label="Read it again" icon="i-lucide-refresh-cw" color="warning" :loading="reading" @click="readAgain" />
          </div>
        </template>
      </UModal>
      <UModal v-model:open="deleteOpen" :title="`Delete ${saved?.name ?? routeId ?? 'crew'}?`" description="The saved crew goes. Runs already launched keep going, and their worktrees and branches stay.">
        <template #footer>
          <div class="flex w-full justify-end gap-2">
            <UButton label="Cancel" color="neutral" variant="ghost" @click="deleteOpen = false" />
            <UButton label="Delete" icon="i-lucide-trash-2" color="error" :loading="deleting" @click="confirmDelete" />
          </div>
        </template>
      </UModal>
    </template>
  </UDashboardPanel>
</template>
