<script setup lang="ts">
import type { AgentInfo, CrewStart, SessionInfo } from '~/composables/useSessions'
import { CREW_NAME_MAX, draftMember, manifestIds, memberNameFrom, MEMBERS_RUN_AS, membersRunAs, moreOptionsLine, ownersFollowPeople, permissionItems, SETUP_WORDS, tasksFollowMembers, viewLinkHoursWords, withMembersRunAs, type DraftCrew, type DraftMember, type DraftPerson } from '~/utils/crews'
import { startSentence } from '~/utils/crewWords'
import { withStart } from '~/utils/crewGraph'
import { isActive } from '~/utils/attention'
import { gitCheckLine, type GitCheckView } from '~/utils/dirInput'
import { shortCwd } from '~/utils/sessions'
import { yoloChoice, yoloFromChoice, type YoloChoice } from '~/utils/yolo'

/**
 * A crew's Setup: its name, its goal, its folder and its workspace, how its
 * members start, its rarely changed options folded under More options (what
 * a launch opens, its permission prompts, a view link), and its members as a
 * table or as the graph of their start rules; a crew
 * with tasks has its tasks and, under them, its project manifest. Between the
 * members and the tasks, its people (HM1): never started, they own its
 * decisions and its tasks' outcomes. A comparison's crew (CP1) shows its
 * block, read only, above its tasks (CrewCompareBlock). The page
 * owns saving, launching, duplicating and deleting, and says why the name
 * cannot be saved (`nameProblem`, beside the field); the editor only changes
 * the draft. A new crew's Name is focused (`focusName`).
 */
const crew = defineModel<DraftCrew>({ required: true })
const props = defineProps<{ agents: AgentInfo[]; yoloDefault?: boolean; nameProblem?: string; focusName?: boolean }>()

const live = useAttention()
const api = useSessions()
const toast = useToast()
// The agent select names the server's host beside an agent not installed there.
const serverHost = useServerHost()
serverHost.load()
// Each member's Account choice is made of the accounts (docs/plans/capacity.md, PR-7): read as the editor shows.
const accounts = useAccounts()
onMounted(() => void accounts.store.load())

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
/** Under "Folder": the git state of the folder, and that it is the server's. */
const runsInHelp = computed(() => {
  const where = serverHost.host.value ? `on ${serverHost.host.value}` : 'on the server'
  const git = gitLine.value.text.replace(/\.$/, '')
  return git ? `${git} · ${where}` : where
})

/** The view link a launch creates lasts 8 hours when the switch is on. */
const VIEW_LINK_TTL = 8 * 3600

// The permission prompts of the crew's runs: the server's default, or ask (yolo off), or skip (yolo on). A launch fixes the choice on
// the run, so members started later follow it.
const yoloItems = computed(() => permissionItems(!!props.yoloDefault))
const yolo = computed({
  get: () => yoloChoice(crew.value.yolo),
  set: (c: YoloChoice) => (crew.value = { ...crew.value, yolo: yoloFromChoice(c) }),
})

// How the crew's members run: in their terminals, or as chats (design 14). Terminal by default; a launch fixes it on the run.
const runAsItems = MEMBERS_RUN_AS.items.map((i) => ({ ...i }))
const runAs = computed({
  get: () => membersRunAs(crew.value),
  set: (v: string) => (crew.value = withMembersRunAs(crew.value, v)),
})

const viewLink = computed({
  get: () => !!crew.value.viewLinkTtlSeconds,
  set: (on: boolean) => (crew.value = { ...crew.value, viewLinkTtlSeconds: on ? VIEW_LINK_TTL : undefined }),
})
const afterLaunch = computed({
  get: () => (crew.value.openAfterLaunch ? 'open' : 'stay'),
  set: (v: string) => (crew.value = { ...crew.value, openAfterLaunch: v === 'open' }),
})
/** More options, folded: the line beside it says what each choice is now. */
const moreOpen = ref(false)
const moreLine = computed(() => moreOptionsLine(crew.value, !!props.yoloDefault))

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
const coordinationItems = SETUP_WORDS.startItems.map((i) => ({ ...i }))
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
    <!-- Each thing a crew is set to, one labelled control each; the detail one level down, as the field's help. -->
    <div class="grid gap-x-6 gap-y-4 lg:grid-cols-2" data-crew-fields>
      <div class="flex min-w-0 flex-col gap-4">
        <UFormField label="Name" name="name" :error="nameProblem || false" :ui="{ error: 'mt-1 text-xs' }" data-crew-name-field>
          <!-- Its accessible name holds the label's word: "Crew name" tells it from each member's "Name of …". -->
          <UInput
            :model-value="crew.name"
            :maxlength="CREW_NAME_MAX"
            placeholder="What people will call this crew"
            aria-label="Crew name"
            :autofocus="focusName"
            :aria-invalid="!!nameProblem"
            class="w-full"
            data-crew-name
            @update:model-value="set('name', String($event))"
          />
        </UFormField>
        <UFormField label="Goal" name="goal" hint="A prompt can use it as $GOAL" :ui="{ hint: 'text-xs' }">
          <UTextarea :model-value="crew.goal" :rows="2" autoresize :maxrows="8" maxlength="2000" placeholder="What the crew is for" aria-label="Goal" class="w-full" @update:model-value="set('goal', String($event))" />
        </UFormField>
      </div>
      <div class="flex min-w-0 flex-col gap-4">
        <!-- The git line is the field's help: announced with the input, and wrapped anywhere so a long path keeps the column's width. -->
        <UFormField label="Folder" name="cwd" :help="runsInHelp" :ui="{ help: 'mt-1 text-xs' }">
          <DirInput :model-value="crew.cwd" placeholder="The server's default folder" name="cwd" picker-title="The crew's folder" @update:model-value="set('cwd', $event)" />
          <template #help>
            <span class="[overflow-wrap:anywhere]" :class="{ 'text-success': gitLine.tone === 'success', 'text-warning': gitLine.tone === 'warning', 'text-muted': gitLine.tone === 'neutral' }" data-git-state>{{ runsInHelp }}</span>
          </template>
        </UFormField>
        <UFormField :label="SETUP_WORDS.workspace" name="isolation" :ui="{ help: 'mt-1 text-xs' }">
          <USelect :model-value="crew.isolation" :items="SETUP_WORDS.workspaceItems.map((i) => ({ ...i }))" :aria-label="SETUP_WORDS.workspace" class="w-full" data-crew-workspace @update:model-value="set('isolation', $event as DraftCrew['isolation'])" />
          <template #help>
            <span v-if="managed" class="text-muted">
              Each attempt at a task works in <code>.conductor/worktrees/&lt;run&gt;/&lt;task&gt;.&lt;n&gt;</code> of the repository, on its own branch <code>crew/&lt;run&gt;/&lt;task&gt;.&lt;n&gt;</code>. The folder must be in a git repository with a commit.
            </span>
            <span v-else-if="crew.isolation === 'worktree'" class="text-muted">
              Each member works in <code>.conductor/worktrees/&lt;run&gt;/&lt;member&gt;</code> of the repository, on its own branch <code>crew/&lt;run&gt;/&lt;member&gt;</code>. The folder must be in a git repository with a commit.
            </span>
            <span v-else class="text-muted">Every member works in the folder itself.</span>
          </template>
        </UFormField>
      </div>
    </div>

    <UFormField :label="SETUP_WORDS.start" name="coordination" :ui="{ help: 'mt-1 text-xs' }" :help="managed ? 'The checks decide a task\'s result, never a quiet terminal. Launched, it runs as a managed run: the run page shows the tasks.' : ''">
      <USelect v-model="coordination" :items="coordinationItems" :aria-label="SETUP_WORDS.start" class="w-full" data-crew-coordination />
    </UFormField>

    <UFormField :label="MEMBERS_RUN_AS.label" name="way" :help="MEMBERS_RUN_AS.chatLine" :ui="{ help: 'mt-1 text-xs leading-4' }">
      <USelect v-model="runAs" :items="runAsItems" :aria-label="MEMBERS_RUN_AS.label" class="w-full sm:w-60" data-crew-way />
    </UFormField>

    <!-- The options a crew rarely changes, folded; the line beside the toggle says what each is now. -->
    <UCollapsible v-model:open="moreOpen" :ui="{ content: 'pt-3' }">
      <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
        <UButton label="More options" :icon="moreOpen ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" color="neutral" variant="ghost" size="sm" class="-ml-2.5 max-sm:min-h-11" :aria-expanded="moreOpen" data-crew-more-options />
        <span v-if="!moreOpen" class="min-w-0 text-xs text-muted [overflow-wrap:anywhere]" data-crew-more-line>{{ moreLine }}</span>
      </div>
      <template #content>
        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3" data-crew-more>
          <UFormField :label="SETUP_WORDS.afterLaunch" name="afterLaunch">
            <USelect v-model="afterLaunch" :items="SETUP_WORDS.afterLaunchItems.map((i) => ({ ...i }))" :aria-label="SETUP_WORDS.afterLaunch" class="w-full" data-crew-after-launch />
          </UFormField>
          <UFormField :label="SETUP_WORDS.permissions" name="yolo" :ui="{ help: 'mt-1 text-xs' }" help="Skipped, a member acts without asking you first: only for work you trust.">
            <USelect v-model="yolo" :items="yoloItems" :aria-label="SETUP_WORDS.permissions" class="w-full" data-crew-yolo />
          </UFormField>
          <UFormField :label="SETUP_WORDS.viewLink" name="viewLink" :ui="{ help: 'mt-1 text-xs' }" help="Each launch makes a link anyone can use to watch every member, view only.">
            <USwitch v-model="viewLink" :label="`Make one for ${viewLinkHoursWords(crew.viewLinkTtlSeconds)}`" class="min-h-8 max-sm:min-h-11" data-crew-view-link />
          </UFormField>
        </div>
      </template>
    </UCollapsible>

    <div class="flex flex-col gap-3" data-crew-members-section>
      <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
        <span class="text-sm font-semibold text-highlighted">Members</span>
        <span class="min-w-0 text-[12.5px] text-muted" data-start-sentence>{{ startSentence(crew.members) }}</span>
        <UTabs v-if="!managed" v-model="membersView" :items="membersViewItems" :content="false" size="xs" color="neutral" class="ml-auto" data-members-view />
      </div>
      <CrewMembersTable v-if="managed || membersView === 'table'" :model-value="crew.members" :agents="agents" :host="serverHost.host.value" :managed="managed" :accounts="accounts.doc.value" :yolo="crew.yolo ?? yoloDefault" @update:model-value="setMembers" @add="addMember" @add-from-session="pickOpen = true" />
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

    <CrewCompareBlock v-if="managed && crew.compare" :compare="crew.compare" :tasks="crew.tasks ?? []" :members="crew.members" :agents="agents" />
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
