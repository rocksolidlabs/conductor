<script setup lang="ts">
import type { AgentInfo, SessionInfo } from '~/composables/useSessions'
import type { LaunchPrefill } from '~/composables/useLaunchModal'
import { ApiError } from '~/composables/useApi'
import { launchListNote, serverAgents } from '~/utils/agents'
import { splitArgs } from '~/utils/argv'
import { ATTEMPT_WARNING, SHARED_TREE_WARNING, cloneArgsShown, cloneCwd, cloneLaunchArgs, cloneLaunchCwd, cloneName, cloneNameError, cloneRefusal, inRunWorktree, missingSourceAgent, yoloStored } from '~/utils/clone'
import { hostAdapter, hostCommand } from '~/utils/hostCommand'
import { accountChoices, modelProblem, type CapacityQuery } from '~/utils/capacity'
import { plainOffMachine, showsRunsOn } from '~/utils/launch'
import { effectiveYolo, yoloSummary } from '~/utils/yolo'

const open = defineModel<boolean>('open', { default: false })
const launchModal = useLaunchModal()
/** Counts the openings: a catalog answer of an earlier one applies to nothing. */
let openings = 0
const emit = defineEmits<{ launched: [session: SessionInfo] }>()

const api = useSessions()
const admin = useWorkbenchToken()
const toast = useToast()
const live = useAttention()
const { httpBase } = useApiBase()
const agents = ref<AgentInfo[]>([])
const loading = ref(false)
// The last catalog this page fetched (round 14): the dialog opened again lists it at once and refreshes it behind.
const cachedCatalog = useState<{ agents: AgentInfo[]; yoloDefault: boolean } | null>('launchCatalog', () => null)
const submitting = ref(false)
const error = ref('')
const form = ref<HTMLFormElement>()
const knownHosted = ref<Set<string>>(new Set())
/** The server's yolo default (GET /api/catalog). */
const yoloDefault = ref(false)

/** `yolo` is this launch's own choice; undefined follows the server's default. */
const state = reactive<{ agentId: string; runsOn: 'server' | 'local'; name: string; cwd: string; args: string; yolo?: boolean }>({ agentId: '', runsOn: 'server', name: '', cwd: '', args: '' })

// Clone with options… (#434): the dialog titled for the clone of a session, filled in with its launch, on this server, the name
// empty and required; the arguments as recorded while their line is left as it was; the agent never another than the source's
// unless picked: one this server does not offer is said so, with nothing picked.
const cloning = ref<NonNullable<LaunchPrefill['clone']> | null>(null)
const cloneArgv = ref<{ text: string; argv: string[] } | null>(null)
/** The source's agent, kept for the whole opening (#434 review round 1): the pick may be cleared, the source stays known. */
const sourceAgent = ref('')
/** Whether this opening's own catalog read has answered: before it, a cached catalog may still list an agent since gone. */
const catalogRead = ref(false)
const cloneNamed = computed(() => cloneName(state.name, live.sessions.value))
const cloneNameMessage = computed(() => cloneNameError(cloneNamed.value))
// Judged by where the clone runs: a blank field is the source's directory, which the server takes (#434 review round 2).
const sharedTree = computed(() => !!cloning.value && inRunWorktree(cloneLaunchCwd(state.cwd, cloning.value.cwd)))

// Where the agent runs is asked only when there are two answers: on a
// workbench served from another machine. In the desktop app, or at this
// computer's own address, it runs on this server, which is this computer.
const { isDesktop } = useDesktop()
const runsOnChoice = computed(() => !cloning.value && showsRunsOn(isDesktop.value, import.meta.client ? location.hostname : ''))
watch(
  runsOnChoice,
  (choice) => {
    if (!choice) state.runsOn = 'server'
  },
  { immediate: true },
)

const selected = computed(() => agents.value.find((a) => a.id === state.agentId))
/** The server tab offers the agents installed on the server; My machine offers every agent: what is installed there is the host's. */
const offered = computed(() => (state.runsOn === 'server' ? serverAgents(agents.value) : agents.value))
const listNote = computed(() => launchListNote({ loading: loading.value, known: agents.value.length, offered: offered.value.length, onServer: state.runsOn === 'server' }))
// A clone never falls back to another agent: the source's, not offered by this opening's read (gone, or not installed here), is said so.
const missingAgent = computed(() => (cloning.value ? missingSourceAgent(sourceAgent.value, offered.value, catalogRead.value) : ''))
// The pick stays one the tab offers.
watch(offered, (list) => {
  if (!list.some((a) => a.id === state.agentId)) state.agentId = cloning.value ? '' : (list[0]?.id ?? '')
})

// Every opening, and a prefill given while the dialog is open already (the palette's Clone with options… of another session).
watch([open, launchModal.asked], async ([v]) => {
  if (!v) return
  error.value = ''
  submitting.value = false
  launchAccount.forAgent = ''
  launchAccount.picked = ''
  model.value = ''
  void accounts.store.load()
  knownHosted.value = new Set(live.sessions.value.filter((s) => s.kind === 'hosted').map((s) => s.id))
  state.yolo = undefined
  // A prefill (#303: Launch <agent> here, Launch another like this, the launch verb), taken once per opening: its agent is set
  // the first time the catalog offers it and never again (a pick made meanwhile stands), and a catalog read of an earlier
  // opening sets nothing of a later one (Codex's round on #303).
  const opening = ++openings
  const p = launchModal.prefill.value
  launchModal.prefill.value = null
  // A clone's opening, or the first one after it: nothing of another opening's typing is kept.
  if (p?.clone || cloning.value) Object.assign(state, { agentId: '', name: '', cwd: '', args: '', runsOn: 'server' })
  cloning.value = p?.clone ?? null
  cloneArgv.value = p?.clone && p.argv ? { text: p.args ?? '', argv: [...p.argv] } : null
  sourceAgent.value = p?.clone ? (p.agentId ?? '') : ''
  catalogRead.value = false
  if (p?.cwd !== undefined) state.cwd = p.cwd
  if (p?.args !== undefined) state.args = p.args
  if (p?.yolo !== undefined) state.yolo = p.yolo
  let wantedAgent = p?.agentId
  const wanted = () => {
    if (opening !== openings) return
    if (wantedAgent && offered.value.some((a) => a.id === wantedAgent)) {
      state.agentId = wantedAgent
      wantedAgent = undefined
    } else if (!cloning.value && !state.agentId && offered.value[0]) state.agentId = offered.value[0].id
  }
  if (cachedCatalog.value) {
    agents.value = cachedCatalog.value.agents
    yoloDefault.value = cachedCatalog.value.yoloDefault
    wanted()
  }
  loading.value = true
  try {
    const info = await api.catalogInfo()
    if (opening !== openings) return
    cachedCatalog.value = info
    agents.value = info.agents
    yoloDefault.value = info.yoloDefault
    catalogRead.value = true
    wanted()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
})

// Yolo: the launch's switch shows the server's default until it is moved;
// the preview shows what the recipe adds, and an agent without one says so.
const yolo = computed({
  get: () => effectiveYolo(state.yolo, yoloDefault.value),
  set: (on: boolean) => (state.yolo = yoloStored(on, yoloDefault.value, !!cloning.value)),
})
const extraArgs = computed(() => (selected.value?.allowArgs && state.args.trim() ? splitArgs(state.args) : []))
const yoloView = computed(() => yoloSummary(selected.value, yolo.value, extraArgs.value))

// Account and model (docs/plans/capacity.md, PR-5): for an agent whose tool's login folder Conductor can move (the adapters
// GET /api/accounts lists), on this server, and not for a clone, which keeps its source's unless its body names others. The account
// starts as the one the server resolves for the agent (its adapter's default, or the default login for an entry that sets its own
// login), read from the capacity view; a pick of the person's is sent as picked. An entry whose own environment sets its login is
// offered its default login alone. The capacity panel below shows the view of what is chosen; it never disables Launch.
const accounts = useAccounts()
const capacity = useCapacity()
const launchAccount = reactive<{ forAgent: string; picked: string }>({ forAgent: '', picked: '' })
const model = ref('')
const adapter = computed(() => selected.value?.adapter || undefined)
const choices = computed(() => accountChoices(accounts.doc.value, adapter.value))
const takesAccount = computed(() => !cloning.value && state.runsOn === 'server' && choices.value.length > 0)
/** The panel's question: the agent and, once the person picked one for it, the account; on this computer, the remote state. */
const capacityQuery = computed<CapacityQuery | null>(() => {
  if (!selected.value || cloning.value) return null
  if (state.runsOn === 'local') return { agent: selected.value.id, launch: 'remote' }
  const picked = launchAccount.forAgent === selected.value.id && launchAccount.picked ? launchAccount.picked : undefined
  return { agent: selected.value.id, account: picked, yolo: yolo.value }
})
/** The account the server resolves for the agent with none named, from the view of the question without one. */
const resolved = computed(() => {
  if (!selected.value || state.runsOn !== 'server') return ''
  return capacity.entry({ agent: selected.value.id, yolo: yolo.value }).view?.account.id ?? ''
})
/** An entry that sets its own login: the server answers its default login with none named although its adapter's default is another. */
const pinned = computed(() => {
  const ad = accounts.doc.value?.adapters.find((a) => a.adapter === adapter.value)
  return !!ad && ad.default !== 'default' && resolved.value === 'default'
})
/** The account shown chosen: the person's pick for this agent, else what the server resolves, else the adapter's default. */
const account = computed({
  get: () => {
    if (selected.value && launchAccount.forAgent === selected.value.id && launchAccount.picked) return launchAccount.picked
    if (resolved.value && choices.value.some((c) => c.value === resolved.value)) return resolved.value
    return accounts.doc.value?.adapters.find((a) => a.adapter === adapter.value)?.default ?? 'default'
  },
  set: (v: string) => {
    launchAccount.forAgent = selected.value?.id ?? ''
    launchAccount.picked = v
  },
})
const modelError = computed(() => modelProblem(model.value))

const server = computed(() => httpBase.value || (import.meta.client ? location.origin : ''))
// A workbench opened over plain http from another machine: the host refuses
// that server without --insecure (#213), so the command carries the flag and
// the dialog says what it means.
const plainServer = computed(() => plainOffMachine(server.value))
const command = computed(() => {
  if (!selected.value) return ''
  const argv = [...selected.value.command, ...(selected.value.allowArgs && state.args.trim() ? splitArgs(state.args) : [])]
  const signal = selected.value.signal
  return hostCommand({
    server: server.value,
    token: admin.token.value,
    name: state.name.trim(),
    argv,
    cwd: state.cwd.trim() || undefined,
    // An agent that shows its prompt on screen is noticed by the host the way the server notices it.
    pattern: signal?.kind === 'pattern' ? signal.pattern : undefined,
    // Its hooks too, for the signal a launch from the server wires them for.
    adapter: hostAdapter(selected.value),
    insecure: plainServer.value,
  })
})

// "My machine": the dialog waits for a hosted session with this name that did
// not exist when it opened. Ids, not timestamps, so clock skew between the
// browser and the server cannot make it wait forever.
const localReady = computed(() => state.runsOn === 'local' && !!selected.value && !!state.name.trim())
const arrived = computed(() => {
  if (!open.value || !localReady.value) return null
  return live.sessions.value.find((s) => s.kind === 'hosted' && s.name === state.name.trim() && !knownHosted.value.has(s.id)) ?? null
})
watch(arrived, (s) => {
  if (!s) return
  toast.add({ title: 'Your machine connected', description: s.name, color: 'success', icon: 'i-lucide-laptop' })
  open.value = false
  emit('launched', s)
})

/** Whether the arguments' line shows: the agent takes arguments, or it is the source's and its recorded ones are kept in sight. */
const argsShown = computed(() => (cloning.value ? cloneArgsShown({ allowArgs: !!selected.value?.allowArgs, sameAgent: !!state.agentId && state.agentId === sourceAgent.value, recorded: cloneArgv.value?.argv }) : !!selected.value?.allowArgs))

/** The arguments a clone launches with: the source's as recorded while their line is as it was, else the line split. */
function cloneArgs(): string[] {
  return cloneLaunchArgs({ shown: argsShown.value, line: state.args, recorded: cloneArgv.value, split: splitArgs })
}

async function submitClone(source: NonNullable<LaunchPrefill['clone']>) {
  if (!state.agentId || cloneNamed.value.problem) return
  // The opening this clone is sent from: its answer closes the dialog or says its refusal only while that opening lasts.
  const sentFrom = openings
  const current = () => open.value && openings === sentFrom
  // Clone… and the rename dialog's rule (#281): the name field takes the focus from the Clone button, disabled as it sends, so a
  // late row menu's More button cannot keep it behind the dialog, where the list takes Escape for its own.
  form.value?.querySelector<HTMLInputElement>('[data-clone-name]')?.focus()
  submitting.value = true
  error.value = ''
  try {
    const session = await api.clone(source.id, { name: cloneNamed.value.name, agentId: state.agentId, cwd: cloneCwd(state.cwd), args: cloneArgs(), yolo: yolo.value })
    toast.add({ title: `Cloned ${source.name}`, description: `${session.name} is a new session like ${source.name}`, color: 'success', icon: 'i-lucide-copy' })
    if (current()) open.value = false
    emit('launched', session)
  } catch (e) {
    if (current()) error.value = e instanceof ApiError ? cloneRefusal(e.code, e.message) : (e as Error).message
  } finally {
    if (openings === sentFrom) submitting.value = false
  }
}

async function submit() {
  if (cloning.value) return submitClone(cloning.value)
  if (!state.agentId || state.runsOn !== 'server') return
  submitting.value = true
  error.value = ''
  try {
    const session = await api.create({
      agentId: state.agentId,
      name: state.name || undefined,
      cwd: state.cwd || undefined,
      args: selected.value?.allowArgs && state.args.trim() ? splitArgs(state.args) : undefined,
      yolo: state.yolo,
      // The account as chosen (the server's own refusal is shown in its words); none for an agent that takes none.
      account: takesAccount.value ? (pinned.value ? 'default' : account.value) : undefined,
      model: takesAccount.value && model.value.trim() ? model.value.trim() : undefined,
    })
    toast.add({ title: 'Session started', description: session.name, color: 'success', icon: 'i-lucide-play' })
    emit('launched', session)
    open.value = false
    state.name = ''
    state.args = ''
    model.value = ''
  } catch (e) {
    error.value = e instanceof ApiError ? `${e.message} (${e.code})` : (e as Error).message
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" :title="cloning ? `Clone ${cloning.name}` : 'Launch agent'" :ui="{ content: 'max-w-xl', body: 'p-0' }">
    <template #body>
      <form ref="form" class="flex flex-col gap-5 p-5" :data-launch-clone="cloning ? cloning.id : undefined" @submit.prevent="submit">
        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-launch-error />
        <UAlert v-if="missingAgent" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="`${missingAgent}, the agent it was launched with, is not offered on this server: it is gone from the catalog or not installed here. Pick an agent to clone with.`" data-clone-missing-agent />
        <UAlert v-if="cloning?.attempt" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="ATTEMPT_WARNING" data-clone-attempt />
        <UAlert v-if="sharedTree" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="SHARED_TREE_WARNING" data-clone-shared-tree />

        <div class="grid grid-cols-2 gap-2 sm:grid-cols-4" role="radiogroup" aria-label="Agent">
          <button
            v-for="a in offered"
            :key="a.id"
            type="button"
            role="radio"
            :aria-checked="state.agentId === a.id"
            class="flex flex-col gap-2 rounded-md border p-3 text-left transition-colors"
            :class="state.agentId === a.id ? 'border-primary bg-primary/5 ring-1 ring-primary' : 'border-default hover:border-accented'"
            @click="state.agentId = a.id"
          >
            <SessionAvatar :agent-id="a.id" size="md" :solid="state.agentId === a.id" />
            <span class="text-sm" :class="state.agentId === a.id ? 'font-semibold' : 'font-medium'">{{ a.name }}</span>
          </button>
          <p v-if="listNote === 'checking'" class="col-span-full flex items-center gap-2 text-sm text-muted" data-agents-checking>
            <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Checking which agents are installed here…
          </p>
          <p v-else-if="listNote" class="col-span-full text-sm text-muted" data-none-available>
            <template v-if="listNote === 'none-installed'">No agent in the catalog is installed on this server. <NuxtLink to="/agents" class="underline" @click="open = false">See the Agents page</NuxtLink> for what is missing, or run one on your machine.</template>
            <template v-else>No agents in the catalog.</template>
          </p>
        </div>
        <p v-if="selected?.description" class="-mt-2 text-xs text-muted">{{ selected.description }} <code class="font-mono">{{ selected.command.join(' ') }}</code></p>

        <UFormField v-if="runsOnChoice" label="Where it runs" name="runsOn" data-runs-on>
          <div class="grid grid-cols-2 rounded-md bg-elevated p-0.5 text-sm">
            <button type="button" class="rounded py-1.5 transition-colors" :class="state.runsOn === 'server' ? 'bg-default font-semibold shadow-xs ring-1 ring-default' : 'text-muted'" @click="state.runsOn = 'server'">On this server</button>
            <button type="button" class="rounded py-1.5 transition-colors" :class="state.runsOn === 'local' ? 'bg-default font-semibold shadow-xs ring-1 ring-default' : 'text-muted'" @click="state.runsOn = 'local'">On this computer</button>
          </div>
          <template #hint><span>"this computer" runs a command you paste; the server is where the workbench is served from</span></template>
        </UFormField>

        <UFormField v-if="cloning" label="Name" name="name" hint="required" :error="cloneNameMessage" :help="cloneNamed.problem === 'empty' ? 'Give the clone a name no session has' : undefined">
          <UInput v-model="state.name" placeholder="no session's already" class="w-full" data-clone-name />
        </UFormField>
        <UFormField v-else label="Name" name="name" :hint="state.runsOn === 'local' ? 'required to spot it when it connects' : 'optional'" :error="state.runsOn === 'local' && !state.name.trim() ? 'Give the session a name first' : undefined">
          <UInput v-model="state.name" placeholder="optional" class="w-full" />
        </UFormField>

        <template v-if="state.runsOn === 'server'">
          <UFormField label="Working directory" name="cwd" hint="must be under an allowed root">
            <DirInput v-model="state.cwd" :placeholder="cloning ? `${cloning.name}'s directory` : selected?.cwd || 'server default'" name="cwd" picker-title="Where the session runs" />
          </UFormField>
          <UFormField
            v-if="argsShown"
            label="Extra arguments"
            name="args"
            :hint="selected?.allowArgs ? 'appended to the command' : 'this agent takes no extra arguments now'"
            :help="selected?.allowArgs ? undefined : 'Recorded at its launch: the server refuses a clone with them; clear them to clone without'"
          >
            <UInput v-model="state.args" placeholder="--model opus" class="w-full font-mono" data-launch-args />
          </UFormField>
          <template v-if="takesAccount">
            <UFormField label="Account" name="account" :hint="pinned ? 'set by this agent\'s entry' : 'the login it uses'" data-launch-account>
              <p v-if="pinned" class="text-sm text-muted" data-launch-account-pinned>This agent's entry sets its own login: it launches on that login only.</p>
              <AccountSelect v-else v-model="account" :doc="accounts.doc.value" :adapter="adapter" />
            </UFormField>
            <UFormField label="Model" name="model" hint="empty for the agent's default" :error="modelError || undefined" data-launch-model>
              <UInput v-model="model" placeholder="the agent's default" class="w-full font-mono" data-launch-model-input />
            </UFormField>
          </template>
          <div class="flex flex-col gap-1.5" data-launch-yolo>
            <USwitch
              v-model="yolo"
              label="Yolo"
              :description="cloning ? 'For this clone' : state.yolo === undefined ? `The server's default (${yoloDefault ? 'on' : 'off'})` : 'For this launch'"
              data-yolo-switch
            />
            <p v-if="yoloView.notice" class="flex items-center gap-1.5 text-xs text-warning" data-yolo-missing><UIcon name="i-lucide-triangle-alert" class="size-3.5 flex-none" />{{ yoloView.notice }}</p>
            <p v-else-if="yoloView.applies" class="text-xs text-muted">
              Skips its permission prompts: <code class="font-mono" data-yolo-argv>{{ yoloView.argv.join(' ') }}</code><template v-if="yoloView.env.length"> with {{ yoloView.env.join(', ') }}</template>
            </p>
          </div>
          <CapacityPanel :query="capacityQuery" :model="takesAccount ? model : undefined" read-on-open />
        </template>

        <template v-else>
          <UFormField v-if="selected?.allowArgs" label="Extra arguments" name="args" hint="appended to the command">
            <UInput v-model="state.args" placeholder="--model opus" class="w-full font-mono" />
          </UFormField>
          <UFormField label="Run this in a terminal on this computer" name="command">
            <CodeBlock :commands="[command]" wrap :disabled="!localReady" copy-title="Command copied" copy-description="It carries your workbench token; keep it private." />
            <template #hint><span>carries your workbench token; keep it private</span></template>
          </UFormField>
          <p v-if="plainServer" class="flex items-start gap-1.5 text-xs leading-relaxed text-warning" data-insecure-notice>
            <UIcon name="i-lucide-triangle-alert" class="mt-0.5 size-3.5 flex-none" />
            <span>
              This workbench is plain <code class="font-mono">http://</code> on another machine, so the command carries <code class="font-mono">--insecure</code>: the host refuses such a server without it, and what passes between them, the session's output and its viewers'
              keys among it, travels in the clear and unverified. Serve the workbench over https to do without the flag.
            </span>
          </p>
          <p class="text-xs leading-relaxed text-muted">The session appears here as <b class="text-default">hosted</b> as soon as it connects, and this dialog closes; your terminal stays attached to it.</p>
          <CapacityPanel :query="capacityQuery" />
        </template>
      </form>
    </template>
    <template #footer>
      <div class="flex w-full items-center gap-2">
        <span v-if="localReady" class="flex items-center gap-2 text-xs text-muted"><span class="size-1.5 rounded-full bg-warning animate-pulse" aria-hidden="true" />Waiting for your machine…</span>
        <div class="flex-1" />
        <UButton label="Cancel" color="neutral" variant="ghost" @click="open = false" />
        <UButton v-if="cloning" label="Clone" icon="i-lucide-copy" :loading="submitting" :disabled="!state.agentId || !!cloneNamed.problem" data-launch-clone-submit @click="submit" />
        <UButton v-else-if="state.runsOn === 'server'" label="Launch" icon="i-lucide-play" :loading="submitting" :disabled="!state.agentId" @click="submit" />
      </div>
    </template>
  </UModal>
</template>
