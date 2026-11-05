<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import { ApiError } from '~/composables/useApi'
import { agentPayload, commandCheck, commandOf, formErrors, formForClone, formFromAgent, takesProfile, type AgentForm, type CommandCheck, type Field, type SignalKind } from '~/utils/agentForm'
import { slugId } from '~/utils/argv'
import { desktopBridge } from '~/utils/desktop'
import { BROWSERS, MAX_PROFILE } from '~/utils/opens'

/**
 * Add an agent to the catalog, edit one (`agent` set) or clone one (`clone`
 * set, #291). Saving replaces the agent with the same ID on the server, so an
 * edit sends the whole agent: fields this form has no control for (working
 * directory, icon, adapter, the tool-events flag, the trust prompt, the
 * session recipe) are carried over from the agent being edited. "Links open
 * in" is the browser its links open in (#436). A clone is
 * filled from the agent copied, under a new name and ID, and saved through
 * the clone route, which copies the stored values this form never sees.
 */
const props = defineProps<{
  agent?: AgentInfo
  /** The agent to copy: the form starts filled from it, and saving makes a copy under the form's ID. */
  clone?: AgentInfo
  /** IDs already in the catalog, to warn before an add replaces one, and to suggest a free one for a copy. */
  takenIds?: string[]
}>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ saved: [agent: AgentInfo] }>()

const api = useSessions()
const toast = useToast()
/** The agent whose settings the form shows: the one edited, or the one copied. */
const source = computed(() => props.agent ?? props.clone)

let nextUid = 0
const uid = () => nextUid++
const form = reactive<AgentForm>(formFromAgent(undefined, uid))
const idTouched = ref(false)
/** The ID names the agent being replaced, so it stays fixed once the agent exists. */
const idLocked = ref(false)
const attempted = ref(false)
const error = ref('')
const saving = ref(false)
const testing = ref(false)
/** The copy once saved (clone mode): a save after it, a Test launch's say, edits the copy rather than copying again. */
const made = ref<AgentInfo | undefined>()

function reset() {
  if (props.clone) {
    // A copy: the source's settings under a new name and a free ID, which stays editable until the copy is saved.
    Object.assign(form, formForClone(props.clone, props.takenIds ?? [], uid))
    idTouched.value = true
    idLocked.value = false
  } else {
    Object.assign(form, formFromAgent(props.agent, uid))
    idTouched.value = !!props.agent
    idLocked.value = !!props.agent
  }
  made.value = undefined
  attempted.value = false
  error.value = ''
  scheduleCheck()
}

// The browsers the desktop app finds on this computer (#436), to mark in "Links open in"; none outside the app or from an older one.
const found = ref<string[]>([])
const browserItems = computed(() => [
  { label: 'Default browser', value: 'default' },
  ...BROWSERS.map((b) => ({ label: found.value.includes(b.name) ? `${b.label} · on this computer` : b.label, value: b.name })),
])

watch(open, (v) => {
  if (v) {
    // A toast from the last save sits over the footer buttons of a right-hand slideover.
    toast.clear()
    reset()
    desktopBridge()
      ?.browsers?.()
      .then((list) => (found.value = Array.isArray(list) ? list : []))
      .catch(() => {})
  } else {
    clearTimeout(checkTimer)
  }
})

// An error describes the form as it was submitted; editing it is the fix.
watch(
  form,
  () => {
    error.value = ''
  },
  { deep: true },
)

// The ID follows the name until someone types in the ID field, and stops for
// good once it is locked: after the first save it names the saved agent, and
// following a rename would save a second agent under the new ID.
watch(
  () => form.name,
  (n) => {
    if (!idTouched.value && !idLocked.value) form.id = slugId(n)
  },
)
function setId(v: string | number | null | undefined) {
  form.id = String(v ?? '')
  idTouched.value = form.id !== ''
}
const idHint = computed(() => (idLocked.value ? 'fixed once saved' : idTouched.value ? undefined : 'from the name'))
const replacing = computed(() => !idLocked.value && !!form.id && !!props.takenIds?.includes(form.id))
/** What a taken ID means: an add replaces that agent; a copy is refused under it. */
const takenWords = computed(() => (props.clone ? 'An agent with this ID exists; pick another for the copy.' : 'An agent with this ID exists; saving replaces it.'))

// Whether the program resolves on the server. Informational: saving never waits for it.
const check = ref<CommandCheck>({ state: 'idle' })
let checkTimer: ReturnType<typeof setTimeout> | undefined
let checkSeq = 0

function scheduleCheck() {
  clearTimeout(checkTimer)
  const seq = ++checkSeq
  const program = commandOf(form)[0]?.trim()
  if (!program) {
    check.value = { state: 'idle' }
    return
  }
  check.value = { state: 'pending' }
  checkTimer = setTimeout(async () => {
    try {
      const r = await api.checkCommand(program, source.value?.adapter)
      if (seq === checkSeq) check.value = commandCheck(r, program)
    } catch {
      if (seq === checkSeq) check.value = { state: 'failed' }
    }
  }, 400)
}
watch(() => commandOf(form)[0], scheduleCheck)
onBeforeUnmount(() => clearTimeout(checkTimer))

function addEnv() {
  form.env.push({ uid: uid(), key: '', value: '', masked: false })
}
function removeEnv(rowUid: number) {
  form.env = form.env.filter((r) => r.uid !== rowUid)
}
function addYoloEnv() {
  form.yoloEnv.push({ uid: uid(), key: '', value: '' })
}
function removeYoloEnv(rowUid: number) {
  form.yoloEnv = form.yoloEnv.filter((r) => r.uid !== rowUid)
}

// The hook card needs an adapter to report through; until adapters can be
// picked here, only an agent that already has one (or already reports by hook)
// can use it, so editing a built-in never loses its wiring.
const hookAvailable = computed(() => !!source.value?.adapter || source.value?.signal?.kind === 'hook')
const signalItems = computed<Array<{ value: SignalKind; label: string; suffix?: string; description: string; icon: string; disabled?: boolean }>>(() => [
  {
    value: 'hook',
    label: 'Hook command',
    suffix: source.value?.adapter ? `adapter ${source.value.adapter}` : undefined,
    description: hookAvailable.value ? 'The agent reports through its own hooks.' : 'pick an adapter (coming with Events)',
    icon: 'i-lucide-webhook',
    disabled: !hookAvailable.value,
  },
  { value: 'bell', label: 'Bell / OSC 9·777', description: 'A terminal bell or notification escape. The default.', icon: 'i-lucide-bell' },
  { value: 'pattern', label: 'Screen pattern', description: 'A regex matched against the last screen line.', icon: 'i-lucide-scan-text' },
  { value: 'none', label: 'None', description: 'Never flagged; you watch it yourself.', icon: 'i-lucide-bell-off' },
])

const errors = computed(() => formErrors(form))
const shown = computed<Partial<Record<Field, string>>>(() => (attempted.value ? errors.value : {}))

/** Validates, then saves. Returns the saved agent, or null with `error` set. */
async function persist(): Promise<AgentInfo | null> {
  error.value = ''
  attempted.value = true
  const first = Object.values(errors.value)[0]
  if (first) {
    error.value = first
    return null
  }
  try {
    // A copy goes through the clone route, which fills in the stored values the masked rows stand for; saved once, it is edited.
    const saved = props.clone && !made.value ? await api.cloneAgent(props.clone.id, agentPayload(form, props.clone)) : await api.saveAgent(agentPayload(form, made.value ?? props.agent))
    if (props.clone) made.value = saved
    // A rename typed while the request was in flight may have moved the ID (it
    // is not locked yet): lock the ID that was actually saved.
    form.id = saved.id
    idLocked.value = true
    emit('saved', saved)
    return saved
  } catch (e) {
    error.value = (e as Error).message
    return null
  }
}

async function save() {
  saving.value = true
  try {
    const saved = await persist()
    if (!saved) return
    toast.add({ title: 'Saved to catalog', description: saved.name, icon: 'i-lucide-check', color: 'success' })
    open.value = false
  } finally {
    saving.value = false
  }
}

async function testLaunch() {
  testing.value = true
  try {
    const saved = await persist()
    if (!saved) return
    try {
      const session = await api.create({ agentId: saved.id })
      toast.add({ title: 'Session started', description: session.name, color: 'success', icon: 'i-lucide-play' })
      open.value = false
      await navigateTo(`/sessions/${session.id}`)
    } catch (e) {
      const why = e instanceof ApiError ? `${e.message} (${e.code})` : (e as Error).message
      error.value = `Saved to the catalog, but it did not start: ${why}`
    }
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <USlideover
    v-model:open="open"
    :title="clone ? `Clone ${clone.name}` : agent ? `Edit ${agent.name}` : 'Add agent'"
    :description="clone ? 'Every setting of the agent copied goes into the copy, its stored values included; change what the form shows.' : 'Commands are argv arrays run on the server; nothing goes through a shell.'"
    :ui="{ content: 'sm:max-w-xl' }"
  >
    <template #body>
      <div class="flex flex-col gap-5" :data-agent-form="clone ? 'clone' : agent ? 'edit' : 'add'">
        <div class="grid gap-3 sm:grid-cols-2">
          <UFormField label="Name" name="name" required :error="shown.name">
            <UInput v-model="form.name" placeholder="Aider" maxlength="60" autofocus class="w-full" />
          </UFormField>
          <UFormField label="ID" name="id" :hint="idHint" :error="shown.id">
            <UInput :model-value="form.id" placeholder="aider" maxlength="32" :disabled="idLocked" :ui="{ base: 'font-mono' }" class="w-full" @update:model-value="setId" />
          </UFormField>
          <p v-if="replacing" class="-mt-1 flex items-center gap-1.5 text-sm text-warning sm:col-span-2"><UIcon name="i-lucide-triangle-alert" class="size-4 flex-none" />{{ takenWords }}</p>
        </div>

        <UFormField label="Command" name="command" required :error="shown.command">
          <ArgvInput v-model="form.command" v-model:pending="form.pendingCommand" placeholder="aider --model sonnet" :invalid="!!shown.command" />
          <template #help>
            <span v-if="check.state === 'pending'" class="flex items-center gap-1.5"><UIcon name="i-lucide-loader-circle" class="size-3.5 flex-none animate-spin" />Checking the server…</span>
            <span v-else-if="check.state === 'found'" class="flex items-center gap-1.5 text-success"><UIcon name="i-lucide-check" class="size-3.5 flex-none" />Found on the server: <code class="font-mono">{{ check.path }}</code><template v-if="check.identity"> · <span :class="check.identity.identified ? 'text-success' : check.identity.ran ? 'text-warning' : 'text-muted'" data-check-identity>{{ check.identity.identified ? `${check.identity.name} ${check.identity.version ?? ''}`.trim() : check.identity.ran ? `not identified as ${check.identity.name}: printed "${check.identity.output ?? ''}"` : check.identity.pending ? 'checking the version…' : (check.identity.error ?? 'the version check failed') }}</span></template></span>
            <span v-else-if="check.state === 'missing'" class="flex items-center gap-1.5 text-warning"><UIcon name="i-lucide-triangle-alert" class="size-3.5 flex-none" />Not found on the server; hosts may still have it</span>
            <span v-else-if="check.state === 'atLaunch'" class="flex items-center gap-1.5"><UIcon name="i-lucide-folder" class="size-3.5 flex-none" />Resolved at launch in the session's directory</span>
            <span v-else-if="check.state === 'slow'" class="flex items-center gap-1.5"><UIcon name="i-lucide-clock" class="size-3.5 flex-none" />The server took too long to look; a launch will tell</span>
            <span v-else-if="check.state === 'failed'">Could not check the server.</span>
            <span v-else>Press Enter or Space after each argument. Quote to keep spaces.</span>
          </template>
        </UFormField>

        <UFormField label="Description" name="description" hint="optional">
          <UInput v-model="form.description" placeholder="What it is for" maxlength="200" class="w-full" />
        </UFormField>

        <UFormField label="Website" name="site" hint="optional, https" :error="shown.site">
          <UInput v-model="form.site" type="url" placeholder="https://" maxlength="200" autocapitalize="off" spellcheck="false" class="w-full" />
          <template #help>The Agents page links to it on any agent that has one.</template>
        </UFormField>

        <div class="grid gap-3 sm:grid-cols-2" data-agent-browser>
          <UFormField label="Links open in" name="browser" hint="optional">
            <USelect v-model="form.browser" :items="browserItems" aria-label="Links open in" class="w-full" />
          </UFormField>
          <UFormField v-if="takesProfile(form)" label="Profile" name="profile" hint="optional" :error="shown.profile">
            <UInput v-model="form.profile" placeholder="Profile 2" :maxlength="MAX_PROFILE" autocapitalize="off" spellcheck="false" class="w-full" />
          </UFormField>
          <p class="-mt-1 text-xs text-muted sm:col-span-2">
            Where the desktop app opens the links this agent opens for you (conductor open, the open_url tool, its own opener), on its computer.
            <template v-if="takesProfile(form)">A profile is the browser's profile folder, such as Default or Profile 2.</template>
          </p>
        </div>

        <div class="text-sm">
          <div class="font-medium text-default">Environment</div>
          <p class="mt-1 text-xs text-muted">
            Values stay on the server. Leave one empty to pass the variable through from the server's own environment.
            <template v-if="form.env.some((r) => r.masked)">A stored value is never shown again; remove its row and add it again to change it.</template>
          </p>
          <div class="mt-2 flex flex-col gap-2">
            <div v-for="row in form.env" :key="row.uid" class="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-center gap-2">
              <template v-if="row.masked">
                <code class="truncate font-mono text-sm" :title="row.key">{{ row.key }}</code>
                <span class="flex items-center gap-1.5 text-sm text-muted"><UIcon name="i-lucide-lock" class="size-4 flex-none" />set on the server</span>
              </template>
              <template v-else>
                <UInput v-model="row.key" placeholder="NAME" size="sm" aria-label="Variable name" autocapitalize="off" spellcheck="false" :ui="{ base: 'font-mono' }" class="w-full" />
                <UInput v-model="row.value" type="password" autocomplete="new-password" placeholder="pass through" size="sm" aria-label="Value; empty passes the server's value through" class="w-full" />
              </template>
              <UButton icon="i-lucide-trash-2" size="xs" color="neutral" variant="ghost" :aria-label="`Remove ${row.key || 'variable'}`" @click="removeEnv(row.uid)" />
            </div>
            <p v-if="shown.env" class="text-sm text-error">{{ shown.env }}</p>
            <UButton label="Add variable" icon="i-lucide-plus" size="xs" color="neutral" variant="ghost" class="self-start" @click="addEnv" />
          </div>
        </div>

        <div class="text-sm" data-yolo-recipe>
          <div class="font-medium text-default">Yolo recipe</div>
          <p class="mt-1 text-xs text-muted">
            What a launch with yolo on adds so the agent skips its permission prompts: arguments after the command, variables over its own.
            Shown as they are: put no secret here.
          </p>
          <USwitch v-model="form.yoloNone" label="No yolo recipe" description="Yolo leaves this agent as it is, with a notice." size="sm" class="mt-2" />
          <template v-if="!form.yoloNone">
            <div class="mt-2">
              <ArgvInput v-model="form.yoloArgs" v-model:pending="form.yoloPending" placeholder="--dangerously-skip-permissions" :invalid="!!shown.yolo" />
            </div>
            <div class="mt-2 flex flex-col gap-2">
              <div v-for="row in form.yoloEnv" :key="row.uid" class="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-center gap-2">
                <UInput v-model="row.key" placeholder="NAME" size="sm" aria-label="Yolo variable name" autocapitalize="off" spellcheck="false" :ui="{ base: 'font-mono' }" class="w-full" />
                <UInput v-model="row.value" placeholder="value" size="sm" aria-label="Yolo variable value" autocapitalize="off" spellcheck="false" :ui="{ base: 'font-mono' }" class="w-full" />
                <UButton icon="i-lucide-trash-2" size="xs" color="neutral" variant="ghost" :aria-label="`Remove ${row.key || 'variable'}`" @click="removeYoloEnv(row.uid)" />
              </div>
              <UButton label="Add variable" icon="i-lucide-plus" size="xs" color="neutral" variant="ghost" class="self-start" @click="addYoloEnv" />
            </div>
          </template>
          <p v-if="shown.yolo" class="mt-1 text-sm text-error">{{ shown.yolo }}</p>
        </div>

        <USwitch v-model="form.allowArgs" label="Accept extra arguments" description="The Launch dialog can append arguments to this command." />

        <div class="text-sm">
          <URadioGroup
            v-model="form.signal"
            :items="signalItems"
            variant="card"
            legend="How does it tell Conductor it needs you?"
            aria-label="Needs-input signal"
            :ui="{ fieldset: 'grid gap-2 sm:grid-cols-2', legend: 'mb-2 font-medium text-default', description: 'text-xs leading-snug' }"
          >
            <template #label="{ item }">
              <span class="flex items-center gap-2">
                <UIcon :name="item.icon" class="size-4 flex-none text-muted" />
                <span>{{ item.label }}<span v-if="item.suffix" class="text-xs font-normal text-muted"> · {{ item.suffix }}</span></span>
              </span>
            </template>
          </URadioGroup>
          <UFormField v-if="form.signal === 'pattern'" label="Pattern" name="pattern" hint="RE2 regular expression" :error="shown.pattern" class="mt-3">
            <UInput v-model="form.pattern" placeholder="^> $" autocapitalize="off" spellcheck="false" :ui="{ base: 'font-mono' }" class="w-full" />
            <template #help>Matched against the last line on screen once output goes quiet.</template>
          </UFormField>
        </div>
      </div>
    </template>

    <template #footer>
      <div class="flex w-full flex-col gap-3">
        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
        <div class="flex items-center gap-2">
          <!-- The header's close button does the same; on a phone the three would not fit. -->
          <UButton label="Cancel" color="neutral" variant="ghost" class="hidden sm:inline-flex" @click="open = false" />
          <div class="flex-1" />
          <UButton label="Test launch" icon="i-lucide-play" color="neutral" variant="outline" :loading="testing" :disabled="saving" @click="testLaunch" />
          <UButton label="Save to catalog" icon="i-lucide-check" :loading="saving" :disabled="testing" @click="save" />
        </div>
      </div>
    </template>
  </USlideover>
</template>
