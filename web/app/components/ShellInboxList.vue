<script setup lang="ts">
import { clockTime } from '~/utils/brakemanRole'
import { INBOX_ROW_CLASS } from '~/utils/inbox'
import { answeredNotApplied, decisionPath, readItem, readRefusal, routeWords, withinWait, type ShellItem } from '~/utils/shell'

/**
 * What waits, as the provider lists it (docs/plans/phone-shell.md, OSS-2b, "The inbox screen"): the items in its order, each as a
 * decision link's list draws a row (the question, the decision's kind, "Answered, not yet applied" while its rows say so), "As of
 * 14:02" and the offline line. It is read again on the provider's `watch` while the app is in the foreground, and no longer in the
 * background. A provider that cannot be reached leaves the last inbox it listed, with its time. `note` is the words of a way in that
 * did not open its decision.
 */
const props = defineProps<{ name: string; note?: string }>()

const shell = useShell()
const p = shell.provider!
const items = ref<ShellItem[]>(shell.lastInbox.value?.items ?? [])
const asOf = ref<string | null>(shell.lastInbox.value?.asOf ?? null)
const offline = ref(false)
const words = ref('')
const loaded = ref(!!shell.lastInbox.value)

const reading = ref(false)
/** False once the list is gone: what a late read answers then is dropped. */
let alive = true

async function load() {
  reading.value = true
  try {
    const r = await withinWait(p.inbox.list(), `${props.name} did not answer`)
    // A read that answers after the list is gone, or after a sign-out, keeps nothing.
    if (!alive || !shell.state.value?.signedIn) return
    items.value = (Array.isArray(r?.items) ? r.items : []).map(readItem).filter((x): x is ShellItem => !!x)
    asOf.value = typeof r?.asOf === 'string' ? r.asOf : new Date().toISOString()
    offline.value = false
    words.value = ''
    shell.lastInbox.value = { items: items.value, asOf: asOf.value }
  } catch (e) {
    if (!alive) return
    const r = readRefusal(e, `${props.name} cannot be reached`)
    offline.value = true
    words.value = shell.lastInbox.value ? routeWords('unreachable', props.name, shell.lastInbox.value.asOf, (t) => clockTime(t)) : r.words
  } finally {
    if (alive) {
      loaded.value = true
      reading.value = false
    }
  }
}

let stop: (() => void) | null = null
function watchNow() {
  stop?.()
  try {
    stop = p.inbox.watch(() => void load())
  } catch {
    stop = null
  }
}
function visibility() {
  if (document.visibilityState === 'visible') {
    watchNow()
    void load()
  } else {
    stop?.()
    stop = null
  }
}
onMounted(() => {
  void load()
  if (document.visibilityState === 'visible') watchNow()
  document.addEventListener('visibilitychange', visibility)
})
onBeforeUnmount(() => {
  alive = false
  stop?.()
  document.removeEventListener('visibilitychange', visibility)
})

const TYPE_WORDS: Record<string, string> = { approval: 'An approval', clarification: 'A question', assignment: 'An assignment' }
</script>

<template>
  <section class="flex min-w-0 flex-col gap-2" data-shell-inbox>
    <p v-if="note" class="break-words text-sm text-warning" role="status" data-shell-inbox-note>{{ note }}</p>
    <p v-if="!loaded" class="flex items-center gap-2 text-sm text-muted" role="status" data-shell-inbox-reading><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Reading what waits…</p>
    <p v-if="words && !note" class="break-words text-sm text-warning" role="status" data-shell-inbox-words>{{ words }}</p>
    <UButton v-if="words" label="Try again" icon="i-lucide-refresh-cw" color="neutral" variant="outline" class="min-h-11 max-w-full self-start" :ui="{ label: 'min-w-0 whitespace-normal break-words text-start' }" :loading="reading" data-shell-inbox-again @click="load" />
    <p v-if="asOf" class="text-xs text-muted" data-shell-inbox-asof>As of {{ clockTime(asOf) }}<template v-if="offline">, offline</template></p>
    <ul v-if="items.length" class="flex min-w-0 flex-col gap-2">
      <li v-for="it in items" :key="it.id" class="min-w-0">
        <NuxtLink :to="decisionPath(it.id)" :class="[INBOX_ROW_CLASS, 'border-default']" :data-shell-inbox-row="it.id">
          <span class="break-words font-medium text-highlighted" data-shell-inbox-question>{{ it.decision.request.question }}</span>
          <span class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted">
            <span data-shell-inbox-kind>{{ TYPE_WORDS[it.decision.type] ?? 'A decision' }}</span>
            <UBadge v-if="answeredNotApplied(it)" label="Answered, not yet applied" color="neutral" variant="subtle" size="sm" data-shell-inbox-answered />
          </span>
        </NuxtLink>
      </li>
    </ul>
    <p v-else-if="loaded && !words" class="py-6 text-center text-sm text-muted" data-shell-inbox-empty>Nothing waits for you here.</p>
  </section>
</template>
