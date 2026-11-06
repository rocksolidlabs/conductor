<script setup lang="ts">
import type { PlanSummary } from '~/composables/useDispatch'
import { ApiError } from '~/composables/useApi'
import { ticketWatch } from '~/composables/useDispatchLive'
import { revisionTag } from '~/utils/dispatchPlans'

/**
 * The plan revision a draft became (BrakemanDraftPlan, docs/plans/brakeman.md, PR-11), as Dispatch holds it now, and the link to its
 * ticket. Its id never changes, its state does (approved, withdrawn, superseded by another's approval), so while it is drawn it is read
 * again as a decision card's control is (BrakemanPlanControl): on Dispatch's events (ticketWatch: its ticket at once, its project after
 * the window) and on a reconnect's snapshot, which first forgets what the ordering gate knew; every read goes through that gate, so a slow
 * read never shows an older state over a later one, and a read that fails is asked again by the live store. Until a read has found it,
 * the revision the make answered with is shown; after, whichever of the two is later by its `rev`.
 */
const props = defineProps<{ ticketKey: string; planId: string; answered: PlanSummary | null; to?: string }>()

const ds = useDispatch()
/** The revision as the last list read taken found it; null until one has. */
const listed = ref<PlanSummary | null>(null)

async function reload() {
  const k = props.ticketKey
  const id = props.planId
  if (!k || !id) return
  await ds.readOrAbsent(`plans:${k}`, () => ds.api.plans(k), {
    apply: (ps) => {
      if (k === props.ticketKey && id === props.planId) listed.value = ps.find((p) => p.id === id) ?? null
    },
    absent: (e) => e instanceof ApiError && e.status === 404,
    gone: () => {
      if (k === props.ticketKey) listed.value = null
    },
  })
}

const revision = computed<PlanSummary | null>(() => {
  const a = props.answered?.id === props.planId ? props.answered : null
  const l = listed.value?.id === props.planId ? listed.value : null
  if (a && l) return l.rev >= a.rev ? l : a
  return l ?? a
})
const project = () => props.ticketKey.slice(0, props.ticketKey.lastIndexOf('-'))
// The first read is made once the revision is subscribed to the events (useDispatchLive's `first`), so none falls between them.
const live = useDispatchLive(
  {
    ...ticketWatch(() => revision.value?.ticketId ?? '', project, reload),
    reset: () => ds.gate.reset(),
  },
  () => live.ask(reload),
)
// Another revision named (a make finished again): read it.
watch(
  () => [props.ticketKey, props.planId],
  () => {
    listed.value = null
    live.ask(reload)
  },
)
</script>

<template>
  <div class="flex flex-wrap items-center gap-2 text-xs text-muted" data-draft-plan-made :data-plan-revision="revision?.n ?? ''" :data-plan-state="revision?.state ?? ''">
    <span v-if="revision">{{ ticketKey }} {{ revisionTag(revision.n) }} is {{ revision.state }}.</span>
    <UButton :to="to" :label="revision ? `Open ${ticketKey} ${revisionTag(revision.n)}` : `Open ${ticketKey}`" icon="i-lucide-arrow-up-right" size="xs" color="neutral" variant="outline" data-made-link />
  </div>
</template>
