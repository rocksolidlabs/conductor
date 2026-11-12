<script setup lang="ts">
import { accountWords, capacityKey, clockWords, countsWords, creditWords, freshnessWords, capacityHostWords, limitsWords, modelWords, offersCheck, offersRead, ownLogin, planWords, readWait, severityColor, capacityStateWords, warns, windowWords, type CapacityQuery } from '~/utils/capacity'

/**
 * The capacity panel (docs/plans/capacity.md, PR-5): what the server's capacity view says of an agent on an account before a launch.
 * Where it runs, the account, the model and the plan as the tool reported it; each window the provider reported as a bar coloured by
 * the provider's severity (a window not reported is not drawn); the provider's credit figures and flags in their own row, marked as
 * the provider's; Conductor's own count in another, marked as an estimate; how fresh it is. Every state that is not a reading says
 * what it is, in the server's words. It never disables a launch. `readOnOpen` asks the server to read as the panel shows a question
 * (the launch dialog's opening), which the server does at most once in five minutes per agent and account; `compact` is one line.
 * A launch with its own login (on this computer, or a login its environment sets) is shown with no account and no model.
 */
const props = defineProps<{ query: CapacityQuery | null; compact?: boolean; readOnOpen?: boolean; /** The model the launch names, shown as typed. */ model?: string }>()

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
const state = computed(() => (view.value ? capacityStateWords(view.value) : null))
const wait = computed(() => (view.value ? readWait(view.value, now.value) : null))
const reading = computed(() => !!entry.value?.reading || view.value?.state === 'reading')

/** Shows a question: fetched now, then read when it is the opening's, still shown here, and the server reads it. */
async function show(q: CapacityQuery, gone: () => boolean) {
  await cap.store.watch(q)
  const v = cap.store.entry(q).view
  if (!gone() && props.readOnOpen && q.launch !== 'remote' && v && offersRead(v)) await cap.store.read(q)
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

const provider = computed(() => [...(view.value?.credits ?? []).map(creditWords), ...limitsWords(view.value?.limits)])
const plan = computed(() => planWords(view.value?.account.plan, view.value?.agent.adapter))
const model = computed(() => modelWords(props.model?.trim() ? { chosen: props.model.trim() } : view.value?.model))
const compactLine = computed(() => {
  const v = view.value
  if (!v) return ''
  if (v.state !== 'ok') return `${state.value!.title}: ${state.value!.words}`
  const shown = v.windows.map((w) => `${w.name} ${w.percent}%`).join(' · ')
  return shown || 'no windows reported'
})
</script>

<template>
  <div v-if="query" class="flex flex-col gap-2 rounded-md border border-default p-3 text-sm" :class="compact ? 'gap-1 p-2' : ''" data-capacity-panel :data-capacity-agent="query.agent" :data-capacity-state="view?.state">
    <p v-if="!view && !entry?.error" class="flex items-center gap-2 text-muted"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />Reading what the server knows of this agent's usage…</p>
    <div v-else-if="view && compact" class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
      <p class="min-w-0 flex-[1_1_16rem] text-xs [overflow-wrap:anywhere]" data-capacity-line>
        <template v-if="plan">{{ plan }} · </template>{{ compactLine }}<span class="text-muted"> · {{ freshnessWords(view, now) || 'not read yet' }}</span>
      </p>
      <span v-if="wait" class="flex-none text-xs text-muted" data-capacity-wait>again after {{ clockWords(wait) }}</span>
      <UButton v-else-if="offersRead(view) || reading" label="Read now" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="ghost" :loading="reading" :disabled="reading" data-capacity-read @click="readNow()" />
    </div>
    <template v-else-if="view">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted">
        <span data-capacity-host>{{ capacityHostWords(view.host) }}</span>
        <template v-if="!ownLogin(view)">
          <span aria-hidden="true">·</span>
          <span>Account: <span class="text-default" data-capacity-account>{{ accountWords(view.account) }}</span></span>
          <span aria-hidden="true">·</span>
          <span>Model: <span class="text-default" data-capacity-model>{{ model }}</span></span>
        </template>
        <UBadge v-if="plan" :label="plan" color="neutral" variant="subtle" size="sm" title="The plan as the agent's own tool reported it" data-capacity-plan />
      </div>
      <p v-if="view.host.kind === 'undetermined' && view.host.words" class="text-xs text-muted" data-capacity-host-words>{{ view.host.words }}</p>

      <div v-if="view.state !== 'ok'" class="flex items-start gap-2" data-capacity-words>
        <UIcon :name="view.state === 'error' ? 'i-lucide-circle-alert' : view.state === 'reading' ? 'i-lucide-loader-circle' : 'i-lucide-circle-help'" class="mt-0.5 size-4 flex-none text-muted" :class="view.state === 'reading' ? 'animate-spin' : ''" />
        <p><span class="font-medium">{{ state!.title }}</span>: {{ state!.words }}</p>
      </div>

      <ul v-if="view.windows.length" class="flex flex-col gap-2" aria-label="Usage windows the provider reported">
        <li v-for="w in view.windows" :key="`${w.kind}/${w.name}/${w.limit ?? ''}`" class="flex flex-col gap-1" :data-capacity-window="w.kind" :data-capacity-severity="w.severity || undefined">
          <span class="text-xs">{{ windowWords(w, now) }}</span>
          <UProgress :model-value="w.percent" :max="100" size="xs" :color="severityColor(w)" />
        </li>
      </ul>
      <p v-else-if="view.state === 'ok'" class="text-xs text-muted" data-capacity-no-windows>The provider reported no usage window.</p>

      <p v-if="warns(view)" class="flex items-center gap-1.5 text-xs text-warning" data-capacity-warning>
        <UIcon name="i-lucide-triangle-alert" class="size-3.5 flex-none" />The agent may stop before the reset: a window is near its limit. Launching is still yours to decide.
      </p>

      <p v-if="provider.length" class="text-xs" data-capacity-provider><span class="text-muted">The provider's figures: </span>{{ provider.join(' · ') }}</p>
      <p v-if="view.counts && !ownLogin(view)" class="text-xs" data-capacity-estimate><span class="text-muted">Conductor's estimate: </span>{{ countsWords(view.counts) }}</p>

      <div class="flex flex-wrap items-center gap-2">
        <span class="text-xs text-muted" data-capacity-fresh>{{ freshnessWords(view, now) }}</span>
        <div class="flex-1" />
        <span v-if="wait" class="text-xs text-muted" data-capacity-wait>Read again after {{ clockWords(wait) }}</span>
        <UButton v-if="offersCheck(view)" label="Check this version" icon="i-lucide-shield-question" size="xs" color="neutral" variant="outline" :disabled="!!wait || reading" :title="`Runs this version of ${view.readBy?.program ?? 'the agent'} once to see that it answers its usage without a model turn`" data-capacity-check @click="readNow(true)" />
        <UButton v-else-if="offersRead(view) || reading" label="Read now" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="ghost" :loading="reading" :disabled="!!wait || reading" data-capacity-read @click="readNow()" />
      </div>
    </template>
    <p v-if="entry?.error" class="text-xs text-error" data-capacity-error>{{ entry.error }}</p>
  </div>
</template>
