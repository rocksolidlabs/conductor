<script setup lang="ts">
import type { AgentInfo, CrewStart, SessionInfo } from '~/composables/useSessions'
import { draftMember, manifestIds, memberNameFrom, ownersFollowPeople, tasksFollowMembers, type DraftCrew, type DraftMember, type DraftPerson } from '~/utils/crews'
import { startSentence } from '~/utils/crewWords'
import { withStart } from '~/utils/crewGraph'
import { isActive } from '~/utils/attention'
import { gitCheckLine, type GitCheckView } from '~/utils/dirInput'
import { shortCwd } from '~/utils/sessions'
import { yoloChoice, yoloFromChoice, type YoloChoice } from '~/utils/yolo'

/**
 * A saved crew's Setup: its goal, where its runs work, what each run does,
 * and its members as a table or as the graph of their start rules; a crew
 * with tasks has its tasks and, under them, its project manifest. Between the
 * members and the tasks, its people (HM1): never started, they own its
 * decisions and its tasks' outcomes. The page
 * owns the name (in its header), saving, launching, duplicating and
 * deleting; the editor only changes the draft.
 */
const crew = defineModel<DraftCrew>({ required: true })
const props = defineProps<{ agents: AgentInfo[]; yoloDefault?: boolean }>()

const live = useAttention()
const api = useSessions()
const toast = useToast()
// The agent select names the server's host beside an agent not installed there.
const serverHost = useServerHost()
serverHost.load()

// The git state of the working directory, read 300 ms after it last
// changed; stale replies are dropped. The line it makes explains a launch
// with worktrees the server would refuse; the server still decides.
const gitCheck = ref<GitCheckView | null>(null)
let gitTimer: number | undefined
let gitSeq = 0
async function checkGit() {
  const n = ++gitSeq
  try {
    const r = await api.gitCheck(crew.value.cwd.trim())
    if (n === gitSeq) gitCheck.value = r
  } catch (e) {
    if (n === gitSeq) gitCheck.value = { inRepo: false, hasCommit: false, message: (e as Error).message, error: true }
  }
}
watch(
  () => crew.value.cwd,
  () => {
    window.clearTimeout(gitTimer)
    gitTimer = window.setTimeout(checkGit, 300)
  },
  { immediate: true },
)
onBeforeUnmount(() => window.clearTimeout(gitTimer))
const gitLine = computed(() => gitCheckLine(gitCheck.value, crew.value.isolation))
/** Under "Runs in": the git state of the directory, and that it is the server's. */
const runsInHelp = computed(() => {
  const where = serverHost.host.value ? `on ${serverHost.host.value}` : 'on the server'
  const git = gitLine.value.text.replace(/\.$/, '')
  return git ? `${git} · ${where}` : where
})

/** The view link a launch creates lasts 8 hours when the switch is on. */
const VIEW_LINK_TTL = 8 * 3600

const isolationItems = [
  { label: 'shares one working directory', value: 'none' },
  { label: 'gives each member a git worktree', value: 'worktree' },
]

// Yolo for the crew's runs: the server's default, or on, or off. A launch
// fixes the choice on the run, so members started later follow it.
const yoloItems = computed<Array<{ label: string; value: YoloChoice }>>(() => [
  { label: `Yolo: the server's default (${props.yoloDefault ? 'on' : 'off'})`, value: 'default' },
  { label: 'Yolo on: skip permission prompts', value: 'on' },
  { label: 'Yolo off', value: 'off' },
])
const yolo = computed({
  get: () => yoloChoice(crew.value.yolo),
  set: (c: YoloChoice) => (crew.value = { ...crew.value, yolo: yoloFromChoice(c) }),
})

const viewLink = computed({
  get: () => !!crew.value.viewLinkTtlSeconds,
  set: (on: boolean) => (crew.value = { ...crew.value, viewLinkTtlSeconds: on ? VIEW_LINK_TTL : undefined }),
})
const viewLinkHours = computed(() => {
  const ttl = crew.value.viewLinkTtlSeconds
  return ttl && ttl !== VIEW_LINK_TTL ? `${Math.round((ttl / 3600) * 10) / 10}h` : '8h'
})
/** Under "Each run": what a launch does, in one line; the popover behind it changes it. */
const eachRunLine = computed(() => {
  const yoloWord = yolo.value === 'default' ? `server default (${props.yoloDefault ? 'on' : 'off'})` : yolo.value
  return `${crew.value.openAfterLaunch ? 'Opens the run page' : 'Stays on this page'} · ${viewLink.value ? `view link for ${viewLinkHours.value}` : 'no view link'} · yolo: ${yoloWord}`
})

function set<K extends keyof DraftCrew>(key: K, value: DraftCrew[K]) {
  crew.value = { ...crew.value, [key]: value }
}

/** The members as the table hands them back; in a crew with tasks, each task's member follows its draft member's name, by key. */
function setMembers(next: DraftMember[]) {
  const tasks = crew.value.tasks ? tasksFollowMembers(crew.value.tasks, next) : undefined
  crew.value = { ...crew.value, members: next, tasks }
}

/** The people as the table hands them back; each task's owner follows its draft person's name, by key. */
function setPeople(next: DraftPerson[]) {
  const tasks = crew.value.tasks ? ownersFollowPeople(crew.value.tasks, next) : undefined
  crew.value = { ...crew.value, people: next, tasks }
}

const taken = computed(() => crew.value.members.map((m) => m.name))

/** How a run starts its members: by their start rules, or by the tasks the crew defines (#334). */
const coordinationItems = [
  { label: 'by start rules: each member starts when its rule says', value: 'starts' },
  { label: 'by tasks: each member does its tasks as their needs are met', value: 'tasks' },
]
const coordination = computed({
  get: () => (crew.value.tasks ? 'tasks' : 'starts'),
  set: (v: string) => {
    if (v === 'tasks') {
      crew.value = { ...crew.value, isolation: 'worktree', tasks: crew.value.tasks ?? [], members: crew.value.members.map((m) => ({ ...m, start: { when: 'task' as const } })) }
    } else {
      // A crew by start rules has no tasks, and so no setup and no project manifest.
      crew.value = {
        ...crew.value,
        tasks: undefined,
        setup: undefined,
        manifest: undefined,
        liftedSetup: undefined,
        members: crew.value.members.map((m) => (m.start.when === 'task' ? { ...m, start: { when: 'immediately' as const } } : m)),
      }
    }
  },
})
const managed = computed(() => !!crew.value.tasks)
/** A new member's start: by its tasks in a managed crew, at launch otherwise. */
const newStart = computed<CrewStart>(() => (managed.value ? { when: 'task' } : { when: 'immediately' }))

function addMember() {
  const agent = props.agents[0]
  if (!agent) return
  set('members', [...crew.value.members, draftMember({ name: memberNameFrom(agent.id, taken.value), agentId: agent.id, prompt: '', start: newStart.value })])
}

// "Add from a running session": its agent becomes a new member, and its
// working directory the crew's when the crew has none yet.
const pickOpen = ref(false)
const running = computed(() => live.sessions.value.filter((s) => s.kind === 'server' && isActive(s)))

function addFrom(s: SessionInfo) {
  const next = { ...crew.value, members: [...crew.value.members, draftMember({ name: memberNameFrom(s.crew?.member || s.name, taken.value), agentId: s.agentId, prompt: '', start: newStart.value })] }
  if (!next.cwd.trim()) next.cwd = s.cwd
  crew.value = next
  pickOpen.value = false
}

// The members as a table, or as the graph of their start rules: dragging an
// edge sets "after", its × removes the rule, a node's menu sets it by hand.
// The server's rules hold while drawing (one parent, no cycle): a refused
// drag says why.
const membersView = ref<'table' | 'graph'>('table')
const membersViewItems = [
  { label: 'Table', value: 'table', icon: 'i-lucide-table' },
  { label: 'Graph', value: 'graph', icon: 'i-lucide-workflow' },
]
const selectedMember = ref('')
const agentNames = computed(() => Object.fromEntries(props.agents.map((a) => [a.id, a.name])))
function setStart(name: string, start: CrewStart) {
  set('members', withStart(crew.value.members, name, start))
}
/** Removes a member from the graph; the members that started after it start at launch, as the table does. */
function removeMember(name: string) {
  set(
    'members',
    crew.value.members.filter((m) => m.name !== name).map((m) => (m.start.when === 'after' && m.start.member === name ? { ...m, start: { when: 'immediately' as const } } : m)),
  )
}
function refused(message: string) {
  toast.add({ title: 'That rule cannot be drawn', description: message, icon: 'i-lucide-triangle-alert', color: 'warning' })
}
</script>

<template>
  <div class="flex min-w-0 flex-col gap-5" data-crew-editor>
    <div class="grid gap-4 lg:grid-cols-[1.4fr_1fr_1fr]">
      <UFormField label="Goal" hint="shared as $GOAL" name="goal">
        <UTextarea :model-value="crew.goal" :rows="3" autoresize :maxrows="8" maxlength="2000" placeholder="What the crew is for. Every first prompt can say $GOAL." class="w-full" @update:model-value="set('goal', String($event))" />
      </UFormField>

      <!-- The git line is the field's help: announced with the input, and wrapped anywhere so a long path keeps the column's width. -->
      <UFormField label="Runs in" name="cwd" :help="runsInHelp" :ui="{ help: 'mt-1 text-xs' }">
        <DirInput :model-value="crew.cwd" placeholder="server default" name="cwd" picker-title="Where the crew runs" @update:model-value="set('cwd', $event)" />
        <template #help>
          <span class="[overflow-wrap:anywhere]" :class="{ 'text-success': gitLine.tone === 'success', 'text-warning': gitLine.tone === 'warning', 'text-muted': gitLine.tone === 'neutral' }" data-git-state>{{ runsInHelp }}</span>
        </template>
      </UFormField>

      <UFormField label="Each run" name="isolation" :ui="{ help: 'mt-1 text-xs' }">
        <USelect :model-value="crew.isolation" :items="isolationItems" aria-label="Each run" class="w-full" @update:model-value="set('isolation', $event as DraftCrew['isolation'])" />
        <template #help>
          <!-- One line says what a launch does; the popover behind it holds the switches. -->
          <UPopover :content="{ align: 'start' }">
            <button type="button" class="flex min-w-0 max-w-full items-center gap-1 rounded-xs text-left text-muted hover:text-default focus-visible:ring-2 focus-visible:ring-primary outline-none" data-each-run>
              <span class="[overflow-wrap:anywhere]">{{ eachRunLine }}</span>
              <UIcon name="i-lucide-pencil" class="size-3 flex-none" />
            </button>
            <template #content>
              <div class="flex w-72 flex-col gap-3 p-3">
                <USwitch :model-value="crew.openAfterLaunch" label="Open the run page after launch" size="sm" @update:model-value="set('openAfterLaunch', $event)" />
                <USwitch v-model="viewLink" :label="`Create a view link (${viewLinkHours})`" size="sm" />
                <USelect v-model="yolo" :items="yoloItems" aria-label="Yolo" size="sm" class="w-full" data-crew-yolo />
              </div>
            </template>
          </UPopover>
        </template>
      </UFormField>
    </div>

    <UFormField label="Coordination" name="coordination" :ui="{ help: 'mt-1 text-xs' }" :help="managed ? 'Each attempt at a task works in a worktree of its own, made at the results it needs; the checks decide a result, never a quiet terminal. Launched, it runs as a managed run: the run page shows the tasks.' : ''">
      <USelect v-model="coordination" :items="coordinationItems" aria-label="Coordination" class="w-full" data-crew-coordination />
    </UFormField>

    <p v-if="crew.isolation === 'worktree' && !managed" class="-mt-2 text-xs text-muted">
      Each member works in <code>.conductor/worktrees/&lt;run&gt;/&lt;member&gt;</code> of the repository, on its own branch <code>crew/&lt;run&gt;/&lt;member&gt;</code>. The directory must be in a git repository with a commit.
    </p>
    <p v-else-if="managed" class="-mt-2 text-xs text-muted">
      Each attempt works in <code>.conductor/worktrees/&lt;run&gt;/&lt;task&gt;.&lt;n&gt;</code> of the repository, on its own branch <code>crew/&lt;run&gt;/&lt;task&gt;.&lt;n&gt;</code>. The directory must be in a git repository with a commit.
    </p>

    <div class="flex flex-col gap-3" data-crew-members-section>
      <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
        <span class="text-sm font-semibold text-highlighted">Members</span>
        <span class="min-w-0 text-[12.5px] text-muted" data-start-sentence>{{ startSentence(crew.members) }}</span>
        <UTabs v-if="!managed" v-model="membersView" :items="membersViewItems" :content="false" size="xs" color="neutral" class="ml-auto" data-members-view />
      </div>
      <CrewMembersTable v-if="managed || membersView === 'table'" :model-value="crew.members" :agents="agents" :host="serverHost.host.value" :managed="managed" @update:model-value="setMembers" @add="addMember" @add-from-session="pickOpen = true" />
      <template v-else>
        <CrewGraph :members="crew.members" :agent-names="agentNames" editable :selected="selectedMember" @select="selectedMember = $event" @set-start="setStart" @remove="removeMember" @refused="refused" />
        <div class="flex flex-wrap gap-x-4 gap-y-1">
          <UButton label="Add member" icon="i-lucide-plus" color="secondary" variant="link" size="sm" class="px-0" :disabled="!agents.length || crew.members.length >= 12" data-add-member @click="addMember" />
          <UButton label="Add from a running session" icon="i-lucide-plus" color="secondary" variant="link" size="sm" class="px-0" :disabled="crew.members.length >= 12" @click="pickOpen = true" />
        </div>
      </template>
    </div>

    <div class="flex flex-col gap-3" data-crew-people-section>
      <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
        <span class="text-sm font-semibold text-highlighted">People</span>
        <span class="min-w-0 text-[12.5px] text-muted">who owns the crew's decisions and its tasks' outcomes: the architecture, product and acceptance owners and the security reviewer</span>
      </div>
      <CrewPeopleTable :model-value="crew.people ?? []" :members="crew.members" :tasks="crew.tasks" @update:model-value="setPeople" />
    </div>

    <CrewTasksTable v-if="managed" :model-value="crew.tasks ?? []" :members="crew.members" :people="crew.people ?? []" :isolation="crew.isolation" :taken="manifestIds(crew.manifest)" @update:model-value="set('tasks', $event)" />
    <CrewManifestEditor v-if="managed" :model-value="crew.manifest" :tasks="crew.tasks ?? []" :setup="crew.setup" @update:model-value="set('manifest', $event)" />

    <UModal v-model:open="pickOpen" title="Add from a running session" description="Its agent becomes a new member, and its working directory the crew's when the crew has none yet.">
      <template #body>
        <p v-if="!running.length" class="text-sm text-muted">No session is running on the server.</p>
        <ul v-else class="flex flex-col gap-1">
          <li v-for="s in running" :key="s.id">
            <button type="button" class="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left transition-colors hover:bg-elevated/60" @click="addFrom(s)">
              <SessionAvatar :agent-id="s.agentId" />
              <span class="flex min-w-0 flex-1 flex-col">
                <span class="truncate text-sm font-medium">{{ s.name }}</span>
                <span class="truncate font-mono text-[11px] text-muted">{{ s.agentId }} · {{ shortCwd(s.cwd) }}</span>
              </span>
              <UIcon name="i-lucide-plus" class="size-4 text-muted" />
            </button>
          </li>
        </ul>
      </template>
    </UModal>
  </div>
</template>
