<script setup lang="ts">
import type { BrakemanBrief, BrakemanRequest, BriefItem, RefResolution } from '~/utils/brakeman'
import { actsApart, groupItems, provenance, readingBlocks, readsWords } from '~/utils/brakeman'

/**
 * A brief as Conductor renders it (docs/plans/brakeman.md, PR-6): the provenance line (the agent, its model and version, the
 * request, when it was kept), the title, Brakeman's reading (its summary, marked as its interpretation), then the items in their
 * groups: facts with their evidence as chips, interpretations naming the facts they read, decision cards and proposals with
 * Conductor's controls (PR-7; a proposal Brakeman ran under a grant with its receipt, PR-13; every other act the request ran shown apart,
 * under "What Brakeman did", with its receipt), verified outcomes with their receipt, the
 * drafts as forms the person makes work (a draft plan revision proposed through DS2's own route, PR-11), handoff packages,
 * and the comparison and evidence panels. Each item is anchored at `item-<index>`, where a `brief:` ref leads. The brief and its
 * refs are read once, as the brief is opened. A fact or an interpretation can be kept as one of the background role's notes (BK3),
 * which every later turn is given.
 */
const props = defineProps<{ request: BrakemanRequest }>()
const emit = defineEmits<{ opened: [request: BrakemanRequest] }>()

const bk = useBrakeman()
const brief = ref<BrakemanBrief | null>(null)
const refs = ref<Map<string, RefResolution>>(new Map())
const error = ref('')
const loading = ref(true)

onMounted(async () => {
  try {
    const out = await bk.brief(props.request.id)
    brief.value = out.brief
    // A brief kept is resolved; its refs are read beside it, and a failure leaves the chips without their records.
    if (out.brief) refs.value = await bk.refs(props.request.id).catch(() => new Map())
  } catch (e) {
    error.value = (e as Error).message
    loading.value = false
    return
  }
  loading.value = false
  // Opened once its items are drawn: the page scrolls to a `brief:` ref's item-<n> then, which is not there while it reads.
  await nextTick()
  emit('opened', props.request)
})

const when = (iso: string) => new Date(iso).toLocaleString()
const line = computed(() => provenance(props.request, props.request.agent?.agentId ? `${props.request.agent.agentId}${props.request.agent.adapter ? ` (${props.request.agent.adapter})` : ''}` : undefined, when))
const groups = computed(() => groupItems(brief.value?.items ?? []))
const items = computed<BriefItem[]>(() => brief.value?.items ?? [])
/** The request as the page holds it now (its events fold the acts' outcomes in), and its acts no proposal of the brief shows. */
const live = computed(() => bk.requests.value.find((r) => r.id === props.request.id) ?? props.request)
const apart = computed(() => actsApart(items.value, live.value.acts))

const toast = useToast()
const keeping = ref(-1)
/** Keeps the item's text as one of the role's notes. */
async function keep(index: number) {
  keeping.value = index
  try {
    await bk.keepNote(props.request.id, index)
    toast.add({ title: 'Kept as a note', description: "Brakeman's later turns are given it; clear it on the Brakeman page.", icon: 'i-lucide-check', color: 'success' })
  } catch (e) {
    toast.add({ title: 'Not kept', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    keeping.value = -1
  }
}
</script>

<template>
  <div class="flex flex-col gap-4" :data-brief="request.id">
    <p class="text-xs text-muted" data-brief-provenance>{{ line }}</p>
    <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
    <p v-else-if="loading" class="text-sm text-muted">Reading the brief…</p>
    <template v-else-if="brief">
      <h3 class="text-base font-semibold text-highlighted" data-brief-title>{{ brief.title }}</h3>
      <section class="flex flex-col gap-1 rounded-md border border-default bg-elevated/40 px-3 py-2" data-brief-reading>
        <span class="text-xs font-medium text-muted">Brakeman's reading <span class="font-normal">(its interpretation, not a fact)</span></span>
        <template v-for="b in readingBlocks(brief)" :key="b.key">
          <div v-if="b.label" class="flex flex-col gap-0.5 text-muted" data-brief-uncertainty>
            <span class="text-xs font-medium">{{ b.label }}</span>
            <MarkdownPreview :text="b.text" dir="" :relative-links="false" class="text-xs" />
          </div>
          <MarkdownPreview v-else :text="b.text" dir="" :relative-links="false" class="text-sm" />
        </template>
      </section>
      <section v-if="apart.length" class="flex flex-col gap-2" data-brief-acts>
        <h4 class="text-xs font-semibold uppercase tracking-wide text-muted">What Brakeman did</h4>
        <p class="text-xs text-muted">Each act this request ran under a grant you gave, with its receipt, whether or not the brief names it.</p>
        <BrakemanActCard v-for="a in apart" :key="a.opId" :act="a" />
      </section>
      <section v-for="g in groups" :key="g.group" class="flex flex-col gap-2" :data-brief-group="g.group">
        <h4 class="text-xs font-semibold uppercase tracking-wide text-muted">{{ g.title }}</h4>
        <div v-for="x in g.items" :id="`item-${x.index}`" :key="x.index" class="flex flex-col gap-1.5" :data-brief-item="x.index">
          <BrakemanFact v-if="x.item.kind === 'fact/1'" :number="x.fact!" :text="x.item.text" :evidence="x.item.evidence" :refs="refs" />
          <div v-else-if="x.item.kind === 'interpretation/1'" class="flex flex-col gap-0.5" data-brief-interpretation>
            <p class="text-sm">{{ x.item.text }}</p>
            <span class="text-xs text-muted" data-reads>{{ readsWords(x.item.facts, items) }}</span>
          </div>
          <UButton
            v-if="x.item.kind === 'fact/1' || x.item.kind === 'interpretation/1'"
            label="Keep as a note"
            icon="i-lucide-bookmark-plus"
            size="xs"
            color="neutral"
            variant="ghost"
            class="self-start"
            :loading="keeping === x.index"
            :data-keep-note="x.index"
            @click="keep(x.index)"
          />
          <BrakemanProposal v-else-if="x.item.kind === 'proposal/1'" :item="x.item" :items="items" :refs="refs" :request="request" />
          <div v-else-if="x.item.kind === 'outcome/1'" class="flex flex-wrap items-center gap-1.5" data-brief-outcome>
            <p class="text-sm">{{ x.item.text }}</p>
            <BrakemanRefChip :ref-name="x.item.receipt" :resolution="refs.get(x.item.receipt)" />
          </div>
          <BrakemanDecision v-else-if="x.item.kind === 'decision/1'" :item="x.item" :index="x.index" :refs="refs" />
          <BrakemanDraftTicket v-else-if="x.item.kind === 'draft_ticket/1'" :request="request" :item="x.item" :index="x.index" :items="items" :refs="refs" />
          <BrakemanDraftCrew v-else-if="x.item.kind === 'draft_crew/1'" :request="request" :item="x.item" :index="x.index" :refs="refs" />
          <BrakemanDraftPlan v-else-if="x.item.kind === 'draft_plan/1'" :request="request" :item="x.item" :index="x.index" :refs="refs" />
          <BrakemanHandoff v-else-if="x.item.kind === 'handoff/1'" :item="x.item" :refs="refs" />
          <div v-else-if="x.item.kind === 'overlap/1'" class="flex flex-col gap-1" data-brief-overlap>
            <p class="font-mono text-xs">{{ x.item.paths.join(', ') }}</p>
            <div class="flex flex-wrap gap-1.5"><BrakemanRefChip v-for="r in x.item.between" :key="r" :ref-name="r" :resolution="refs.get(r)" /></div>
          </div>
          <div v-else-if="x.item.kind === 'comparison/1'" class="flex flex-col gap-1.5 overflow-x-auto" data-brief-comparison>
            <span class="text-sm font-medium">{{ x.item.title }}</span>
            <table class="text-xs">
              <thead>
                <tr>
                  <th />
                  <th v-for="c in x.item.columns" :key="c" class="px-2 py-1 text-left font-medium">{{ c }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in x.item.rows" :key="row.label" class="border-t border-default">
                  <th class="px-2 py-1 text-left font-medium">{{ row.label }}</th>
                  <td v-for="(cell, i) in row.cells" :key="i" class="px-2 py-1">{{ cell }}</td>
                </tr>
              </tbody>
            </table>
            <div v-if="x.item.evidence?.length" class="flex flex-wrap gap-1.5"><BrakemanRefChip v-for="r in x.item.evidence" :key="r" :ref-name="r" :resolution="refs.get(r)" /></div>
          </div>
          <div v-else-if="x.item.kind === 'evidence/1'" class="flex flex-col gap-1.5" data-brief-evidence>
            <span class="text-sm font-medium">{{ x.item.title }}</span>
            <div class="flex flex-wrap gap-1.5"><BrakemanRefChip v-for="r in x.item.refs" :key="r" :ref-name="r" :resolution="refs.get(r)" /></div>
          </div>
        </div>
      </section>
    </template>
    <p v-else class="text-sm text-muted">No brief was kept for this request.</p>
  </div>
</template>
