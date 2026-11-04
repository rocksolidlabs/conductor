<script setup lang="ts">
import type { RefResolution } from '~/utils/brakeman'
import { changedSince, parseRef, receiptPath, refLabel, refPage } from '~/utils/brakeman'

/**
 * One piece of evidence a brief cites (docs/plans/brakeman.md, PR-6): a chip labelled by what the ref names, a link to where the person
 * follows it, and "changed since" when the record the page has live (a run's state and revision, a task's state, a session's status)
 * differs from what it was as the brief was kept. A check receipt's chip carries its log's SHA-256, as kept. A record no page shows
 * by itself (a check's log, a command receipt, a Dispatch receipt, a comment) is read from the chip (BrakemanReceipt), with the token.
 */
const props = defineProps<{ refName: string; resolution?: RefResolution }>()

const live = useAttention()
const label = computed(() =>
  refLabel(props.refName, {
    session: (id) => live.sessions.value.find((s) => s.id === id)?.name,
    run: (id) => live.runNames.value[id],
  }),
)
const to = computed(() => refPage(props.refName, props.resolution))
const receipt = computed(() => receiptPath(props.refName, props.resolution))

/** What the page has live of the record, in the words the server's resolution keeps (internal/api/brakeman_refs.go). */
const liveState = computed<{ state?: string; rev?: string } | undefined>(() => {
  const r = parseRef(props.refName)
  if (!r) return undefined
  if (r.kind === 'run' || r.kind === 'task') {
    const run = live.runOf(r.run!)
    if (!run) return undefined
    const rev = run.managed ? String(run.managed.revision) : undefined
    if (r.kind === 'run') return { state: run.state, rev }
    const t = run.tasks?.find((x) => x.id === r.task)
    return t ? { state: t.state, rev } : undefined
  }
  if (r.kind === 'session') {
    const s = live.sessions.value.find((x) => x.id === r.session)
    if (!s) return undefined
    const att = s.attention?.state
    return { state: att ? `${s.status}:${att}` : s.status }
  }
  return undefined
})
const changed = computed(() => changedSince(props.resolution?.now, liveState.value))
const deleted = computed(() => props.resolution?.now.state === 'deleted')
const title = computed(() => {
  const now = props.resolution?.now
  const parts = [props.refName]
  if (now?.state) parts.push(`was ${now.state} when the brief was kept`)
  if (now?.sha256) parts.push(`log SHA-256 ${now.sha256}`)
  if (changed.value && liveState.value?.state) parts.push(`${liveState.value.state} now`)
  return parts.join(' · ')
})
</script>

<template>
  <span class="inline-flex items-center gap-1" :data-ref-chip="refName" :data-changed="changed || undefined">
    <UBadge :color="deleted ? 'error' : 'neutral'" variant="outline" size="sm" :title="title" class="max-w-full">
      <NuxtLink v-if="to && !deleted" :to="to" class="truncate hover:underline" data-ref-link>{{ label }}</NuxtLink>
      <span v-else class="truncate">{{ label }}<template v-if="deleted"> (deleted)</template></span>
      <span v-if="resolution?.now.sha256" class="font-mono text-[10px] text-muted">{{ resolution.now.sha256.slice(0, 12) }}</span>
      <BrakemanReceipt v-if="receipt && !deleted" :ref-name="refName" :path="receipt" :label="label" :kept-sha256="resolution?.now.sha256" />
    </UBadge>
    <UBadge v-if="changed" label="changed since" color="warning" variant="subtle" size="sm" data-ref-changed />
  </span>
</template>
