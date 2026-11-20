<script setup lang="ts">
import { DECISION_ANSWERING } from '~/utils/answering'
import { clockTime } from '~/utils/brakemanRole'
import type { Decision } from '~/utils/decisions'
import { REF_ACCESS } from '~/utils/refAccess'
import { decisionIdOf, omittedWords, readItem, readRefusal, withinWait, type ShellItem } from '~/utils/shell'
import { shellRefAccess } from '~/utils/transport/shell'

/**
 * A decision drawn from the provider's inbox (docs/plans/phone-shell.md, OSS-2b): the offline line when it is drawn from what the
 * provider kept, DR1's card from the item's record (its chips through the shell's REF_ACCESS, which reads nothing), the four steps
 * exactly as the provider gives them, the server that holds it and when it last reported, then DR1's answer under the shell's
 * answering, or the item's `elsewhere` words and no Answer. The item is read again on the provider's `watch` while the app is in the
 * foreground; an answer waiting offers Confirm when the app is back and online. A way in that does not open it goes to the inbox
 * with its words, an address that carries no id as `decisionPath` builds it among them (no such decision). The answer it provides reaches this page's item alone; a read that comes back after the person has left the
 * page opens nothing and moves no one.
 */
definePageMeta({ layout: 'shell' })
useHead({ title: 'A decision' })
const route = useRoute()
/** The provider's id the address carries (`decisionPath`), or '' for one that carries none. */
const id = decisionIdOf(route.params.id) ?? ''
const shell = useShell()
const p = shell.provider
const ans = shell.answer()
provide(REF_ACCESS, shellRefAccess())
if (ans && id) provide(DECISION_ANSWERING, ans.answeringFor(id))
/** False once the page is gone: what a late read answers then is dropped. */
let alive = true

const item = ref<ShellItem | null>(null)
const ready = ref(false)
const view = computed(() => ans?.views.value[id])
/** The item drawn: the answer's latest read of it (a changed decision, a kept answer's rows), else the page's. */
const shown = computed(() => view.value?.item ?? item.value)
const now = ref(Date.now())

async function load(first = false) {
  if (!p || !ans) return
  try {
    const it = readItem(await withinWait(p.inbox.item(id), `${shell.name} did not answer`))
    if (!alive) return
    if (!it) throw { code: 'not_visible' }
    item.value = it
    // Opened once, and again only while the answer is not mid-send: a changed decision drawn rebases the draft.
    if (first || !view.value?.lock) await ans.machine.open(id, it)
    if (!alive) return
    ready.value = true
  } catch (e) {
    const r = readRefusal(e)
    if (!first || !alive) return
    await navigateTo({ path: '/shell', query: { note: r.code === 'not_visible' ? 'not_visible' : 'unreachable' } }, { replace: true })
  }
}

/** The record DR1's answer takes after a write, when it is still this item's. */
const begin = () => (d: Decision) => {
  if (item.value && item.value.decision.id === d.id) item.value = { ...item.value, decision: d }
}

let stop: (() => void) | null = null
let tick: ReturnType<typeof setInterval> | undefined
function foreground() {
  stop?.()
  stop = null
  if (document.visibilityState !== 'visible' || !p) return
  try {
    stop = p.inbox.watch((changed) => {
      if (changed === id) void load()
    })
  } catch {
    stop = null
  }
  void ans?.machine.resume(id)
}
const online = () => void ans?.machine.resume(id)
onMounted(() => {
  if (!p) return void navigateTo('/shell', { replace: true })
  if (!id) return void navigateTo({ path: '/shell', query: { note: 'not_visible' } }, { replace: true })
  void load(true)
  foreground()
  document.addEventListener('visibilitychange', foreground)
  window.addEventListener('online', online)
  tick = setInterval(() => (now.value = Date.now()), 30_000)
})
onBeforeUnmount(() => {
  alive = false
  stop?.()
  clearInterval(tick)
  document.removeEventListener('visibilitychange', foreground)
  window.removeEventListener('online', online)
})
watch(
  () => shell.state.value?.signedIn,
  (inNow) => {
    if (inNow === false) void navigateTo('/shell', { replace: true })
  },
)
</script>

<template>
  <ShellHeader title="A decision for you" :line="shown ? `Held by ${shown.owner.name}${shown.owner.reportedAt ? `, last heard from at ${clockTime(shown.owner.reportedAt)}` : ''}` : undefined" back />
  <p v-if="!shown" class="flex items-center gap-2 text-sm text-muted" role="status" data-shell-decision-reading><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Reading the decision…</p>
  <template v-else>
    <p v-if="shown.offline" class="text-sm text-warning" role="status" data-shell-decision-offline>As of {{ clockTime(shown.asOf) }}, offline</p>
    <section class="flex min-w-0 flex-col gap-4" data-shell-decision :data-shell-item="shown.id">
      <DecisionCard :decision="shown.decision" :now="now" :controls="!shown.elsewhere && shown.decision.state === 'open'" />
      <p v-if="omittedWords(shown.responsesOmitted)" class="text-xs text-muted" data-shell-decision-omitted>{{ omittedWords(shown.responsesOmitted) }}</p>
      <InboxDelivery :steps="shown.rows" :route-of="() => null" />
      <p v-if="shown.elsewhere" class="break-words rounded-md bg-elevated/40 p-2 text-sm" data-shell-decision-elsewhere>{{ shown.elsewhere }}</p>
      <DecisionRespond v-else-if="ready" :decision="shown.decision" :begin="begin" @read="load()" />
    </section>
  </template>
</template>
