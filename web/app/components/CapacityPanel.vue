<script setup lang="ts">
import { capacityKey, capacityStatements, clockWords, compactWords, panelAction, readsOnOpen, severityColor, type CapacityQuery, type CapacityStatement } from '~/utils/capacity'

/**
 * The capacity panel (docs/plans/capacity.md, PR-5): what the server's capacity view says of an agent on an account before a launch.
 * Where it runs, the account, the model and the plan as the tool reported it; each window the provider reported as a bar coloured by
 * the provider's severity (a window not reported is not drawn); the provider's credit figures and flags in their own row, marked as
 * the provider's; Conductor's own count in another, marked as an estimate; how fresh it is. Every state that is not a reading says
 * what it is, in the server's words; a reading's figures are said with the server's own words beside them when it gives any. A limit
 * the provider reports is warned of as a window near its limit is. It never disables a launch. `readOnOpen` asks the server to read as the panel shows a question
 * (the launch dialog's opening), which the server does at most once in five minutes per agent and account, and never of a version
 * the server said is not checked (readsOnOpen): both presentations offer "Check this version" for it (panelAction); `compact` is one line, with every other statement in details that open by keyboard and by touch.
 * A launch with its own login (on this computer, or a login its environment sets) is shown with no account and no model; a launch on
 * this server of an agent whose entry sets its login, with no account and the entry's variable named (`launchLogin` decides which).
 * `folded` (the Launch agent dialog) is the compact one line with a Details button that shows the full panel under it, in place: the
 * full panel stays mounted while hidden, its marks as the full presentation has them, and its open state lives across a new question.
 */
const props = defineProps<{ query: CapacityQuery | null; compact?: boolean; folded?: boolean; readOnOpen?: boolean; /** The model the launch names, shown as typed. */ model?: string }>()
const unfolded = ref(false)
const bodyId = useId()

const cap = useCapacity()
const now = ref(Date.now())
let tick: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  // The clock moves the words; a view whose own words changed by it (stale, a reset passed) is asked of the server again.
  tick = setInterval(() => {
    now.value = Date.now()
    cap.store.age()
  }, 30_000)
})

const entry = computed(() => (props.query ? cap.entry(props.query) : null))
const view = computed(() => entry.value?.view ?? null)
const reading = computed(() => !!entry.value?.reading || view.value?.state === 'reading')
const action = computed(() => (view.value ? panelAction(view.value, now.value, reading.value) : null))

/** Shows a question: fetched now, then read when it is the opening's, still shown here, and the server reads it. */
async function show(q: CapacityQuery, gone: () => boolean) {
  await cap.store.watch(q)
  const v = cap.store.entry(q).view
  if (!gone() && props.readOnOpen && q.launch !== 'remote' && v && readsOnOpen(v)) await cap.store.read(q)
}
watch(
  () => (props.query ? capacityKey(props.query) : ''),
  (key, old, onCleanup) => {
    const q = props.query
    if (!key || !q) return
    const shown = { ...q }
    let gone = false
    void show(shown, () => gone)
    onCleanup(() => {
      gone = true
      cap.store.unwatch(shown)
    })
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  if (tick) clearInterval(tick)
})

function readNow(check = false) {
  if (props.query) void cap.store.read(props.query, check)
}

// Both presentations say the statements of one function (capacityStatements): the full panel each in its place, the compact one in
// its line and its details.
const said = computed(() => (view.value ? capacityStatements(view.value, now.value, props.model) : []))
const one = (kind: CapacityStatement['kind']) => said.value.find((s) => s.kind === kind)
const windows = computed(() => said.value.filter((s) => s.kind === 'window'))
const brief = computed(() => (view.value ? compactWords(view.value, now.value, props.model) : null))
const checkTitle = computed(() => `Runs this version of ${view.value?.readBy?.program ?? view.value?.agent.name ?? 'the agent'} once to see that it answers its usage without a model turn`)
</script>

<template>
  <div v-if="query" class="flex flex-col text-sm" :class="folded ? 'gap-1.5' : compact ? 'gap-1 rounded-md border border-default p-2' : 'gap-2 rounded-md border border-default p-3'" data-capacity-panel :data-capacity-agent="query.agent" :data-capacity-state="view?.state">
    <template v-if="folded">
      <!-- One line on the dialog's face; Details shows the full panel under it. -->
      <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <p class="min-w-0 flex-[1_1_16rem] text-xs [overflow-wrap:anywhere]" data-capacity-line>
          <template v-if="brief">{{ brief.line }}<span class="text-muted"> · {{ brief.fresh }}</span></template>
          <span v-else-if="entry?.error" class="text-error">{{ entry.error }}</span>
          <span v-else class="text-muted">Reading what the server knows of this agent's usage…</span>
        </p>
        <UButton label="Details" :trailing-icon="unfolded ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'" size="xs" color="neutral" variant="ghost" class="max-sm:min-h-11" :aria-expanded="unfolded" :aria-controls="bodyId" data-capacity-unfold @click="unfolded = !unfolded" />
      </div>
      <p v-if="brief?.warning && !unfolded" class="flex items-start gap-1.5 text-xs [overflow-wrap:anywhere]" :class="brief.warning.color === 'error' ? 'text-error' : 'text-warning'" data-capacity-line-warning>
        <UIcon name="i-lucide-triangle-alert" class="mt-0.5 size-3.5 flex-none" />{{ brief.warning.words }}
      </p>
    </template>
    <div v-show="!folded || unfolded" :id="folded ? bodyId : undefined" :class="folded ? 'flex flex-col gap-2 rounded-md border border-default p-3' : 'contents'" :data-capacity-body="folded || undefined">
      <p v-if="!view && !entry?.error && !folded" class="flex items-center gap-2 text-muted"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />Reading what the server knows of this agent's usage…</p>
      <div v-else-if="view && compact && !folded && brief" class="flex min-w-0 flex-col gap-1">
        <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <p class="min-w-0 flex-[1_1_16rem] text-xs [overflow-wrap:anywhere]" data-capacity-line>{{ brief.line }}<span class="text-muted"> · {{ brief.fresh }}</span></p>
          <span v-if="action?.wait" class="flex-none text-xs text-muted" data-capacity-wait>again after {{ clockWords(action.wait) }}</span>
          <UButton v-else-if="action?.kind === 'check'" label="Check this version" icon="i-lucide-shield-question" size="xs" color="neutral" variant="outline" :disabled="action.disabled" :title="checkTitle" data-capacity-check @click="readNow(true)" />
          <UButton v-else-if="action?.kind === 'read'" label="Read now" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="ghost" :loading="reading" :disabled="action.disabled" data-capacity-read @click="readNow()" />
        </div>
        <p v-if="brief.warning" class="flex items-start gap-1.5 text-xs [overflow-wrap:anywhere]" :class="brief.warning.color === 'error' ? 'text-error' : 'text-warning'" data-capacity-warning :data-capacity-warning-color="brief.warning.color">
          <UIcon name="i-lucide-triangle-alert" class="mt-0.5 size-3.5 flex-none" />{{ brief.warning.words }}
        </p>
        <p v-if="brief.host" class="text-xs text-muted [overflow-wrap:anywhere]" data-capacity-host-words>{{ brief.host }}</p>
        <!-- Every other statement of the full panel: each window with its reset, the provider's figures, the estimate… -->
        <details v-if="brief.details.length" class="text-xs" data-capacity-details>
          <summary class="w-fit cursor-pointer text-muted hover:text-default" data-capacity-details-open>Details</summary>
          <ul class="mt-1 flex flex-col gap-0.5 [overflow-wrap:anywhere]">
            <li v-for="d in brief.details" :key="d" data-capacity-detail>{{ d }}</li>
          </ul>
        </details>
      </div>
      <template v-else-if="view">
        <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted">
          <span data-capacity-host>{{ one('host')?.text }}</span>
          <template v-for="s in [one('account'), one('model')]" :key="s?.kind">
            <template v-if="s">
              <span aria-hidden="true">·</span>
              <span>{{ s.label }}: <span class="text-default" :data-capacity-account="s.kind === 'account' || undefined" :data-capacity-model="s.kind === 'model' || undefined">{{ s.text }}</span></span>
            </template>
          </template>
          <UBadge v-if="one('plan')" :label="one('plan')!.text" color="neutral" variant="subtle" size="sm" title="The plan as the agent's own tool reported it" data-capacity-plan />
        </div>
        <p v-if="one('host-words')" class="text-xs text-muted" data-capacity-host-words>{{ one('host-words')!.text }}</p>
        <p v-if="one('entry-login')" class="text-xs text-muted [overflow-wrap:anywhere]" data-capacity-entry-login>{{ one('entry-login')!.text }}</p>

        <div v-if="one('state')" class="flex items-start gap-2" data-capacity-words>
          <UIcon :name="view.state === 'error' ? 'i-lucide-circle-alert' : view.state === 'reading' ? 'i-lucide-loader-circle' : 'i-lucide-circle-help'" class="mt-0.5 size-4 flex-none text-muted" :class="view.state === 'reading' ? 'animate-spin' : ''" />
          <p><span class="font-medium">{{ one('state')!.label }}</span>: {{ one('state')!.text }}</p>
        </div>
        <p v-if="one('words')" class="text-xs [overflow-wrap:anywhere]" data-capacity-server-words>{{ one('words')!.text }}</p>

        <ul v-if="windows.length" class="flex flex-col gap-2" aria-label="Usage windows the provider reported">
          <li v-for="s in windows" :key="`${s.window!.kind}/${s.window!.name}/${s.window!.limit ?? ''}`" class="flex flex-col gap-1" :data-capacity-window="s.window!.kind" :data-capacity-severity="s.window!.severity || undefined">
            <span class="text-xs">{{ s.text }}</span>
            <UProgress :model-value="s.window!.percent" :max="100" size="xs" :color="severityColor(s.window!)" />
          </li>
        </ul>
        <p v-if="one('no-windows')" class="text-xs text-muted" data-capacity-no-windows>{{ one('no-windows')!.text }}</p>

        <p v-if="one('warning')" class="flex items-center gap-1.5 text-xs" :class="one('warning')!.color === 'error' ? 'text-error' : 'text-warning'" data-capacity-warning :data-capacity-warning-color="one('warning')!.color">
          <UIcon name="i-lucide-triangle-alert" class="size-3.5 flex-none" />{{ one('warning')!.text }}
        </p>

        <p v-for="s in [one('provider'), one('estimate')].filter((x) => !!x)" :key="s!.kind" class="text-xs" :data-capacity-provider="s!.kind === 'provider' || undefined" :data-capacity-estimate="s!.kind === 'estimate' || undefined">
          <span class="text-muted">{{ s!.label }}: </span>{{ s!.text }}
        </p>

        <div class="flex flex-wrap items-center gap-2">
          <span class="text-xs text-muted" data-capacity-fresh>{{ one('fresh')?.text }}</span>
          <div class="flex-1" />
          <span v-if="action?.wait" class="text-xs text-muted" data-capacity-wait>Read again after {{ clockWords(action.wait) }}</span>
          <UButton v-if="action?.kind === 'check'" label="Check this version" icon="i-lucide-shield-question" size="xs" color="neutral" variant="outline" :disabled="action.disabled" :title="checkTitle" data-capacity-check @click="readNow(true)" />
          <UButton v-else-if="action?.kind === 'read'" label="Read now" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="ghost" :loading="reading" :disabled="action.disabled" data-capacity-read @click="readNow()" />
        </div>
      </template>
      <p v-if="entry?.error" class="flex flex-wrap items-center gap-2 text-xs text-error [overflow-wrap:anywhere]" data-capacity-error>
        <span class="min-w-0">{{ entry.error }}</span>
        <UButton v-if="!view && query" label="Read again" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="outline" data-capacity-again @click="cap.store.again(query)" />
      </p>
    </div>
  </div>
</template>
