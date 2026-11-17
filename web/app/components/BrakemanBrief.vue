<script setup lang="ts">
import type { BrakemanBrief, BrakemanRequest, BrakemanRow, BriefItem, BriefRegionName, RefResolution } from '~/utils/brakeman'
import { actsApart, briefEvidence, briefRegions, foldWords, provenance, readsWords, refLabel, regionHeading } from '~/utils/brakeman'
import { briefMarkdown } from '~/utils/brakemanCards'
import { statusOfKey } from '~/utils/status'

/**
 * A brief as Conductor renders it (docs/plans/brakeman.md, PR-6), led by what needs a person: its head (with `head`: new, its kind,
 * its age, what it is about), the title, Brakeman's reading (its summary whole, then the caveat that it is a reading and not a fact,
 * with what it is unsure of), then its regions. Needs you: the decision cards with Conductor's controls from the live record (PR-7),
 * marked while a control still offers something to answer. Proposed: proposals (a proposal Brakeman ran under a grant with its
 * receipt, PR-13), the drafts as forms the person makes work (a draft plan revision through DS2's own route, PR-11) and the handoff
 * packages. What Brakeman did: every act the request ran that no proposal shows, with its receipt, and the verified outcomes. What
 * changed: the numbered facts with their evidence as chips, each reading under the last fact it reads, then overlapping changes.
 * Compared: the comparison tables, drawn open. Last, folded: the provenance line, the evidence panels and every chip of the brief.
 *
 * Each item is anchored at `item-<index>`, where a `brief:` ref leads, and keeps its index however the regions order it; a link to
 * an item inside a fold opens the fold before the page scrolls to it. Folds keep their content mounted. The brief and its refs are
 * read once, as the brief is opened. A fact or an interpretation can be kept as one of the background role's notes (BK3), which
 * every later turn is given. On a phone, Proposed and What changed are rows that unfold in place.
 */
const props = withDefaults(defineProps<{ request: BrakemanRequest; head?: boolean }>(), { head: false })
const emit = defineEmits<{ opened: [request: BrakemanRequest] }>()

const bk = useBrakeman()
const route = useRoute()
const phone = useIsPhone()
const brief = ref<BrakemanBrief | null>(null)
const refs = ref<Map<string, RefResolution>>(new Map())
const error = ref('')
const loading = ref(true)
/** Whether the request was not yet opened when the brief was drawn: its head's "new", which its opening does not take away. */
const fresh = ref(false)

/** The request as the page holds it now (its events fold the acts' outcomes in), with its digest when the server sent one. */
const live = computed<BrakemanRow>(() => (bk.requests.value.find((r) => r.id === props.request.id) ?? props.request) as BrakemanRow)
const items = computed<BriefItem[]>(() => brief.value?.items ?? [])
const apart = computed(() => actsApart(items.value, live.value.acts))
const regions = computed(() => briefRegions(items.value, apart.value.length ? ['acts'] : []).filter((r) => r.region !== 'fold'))
const panels = computed(() => briefRegions(items.value).find((r) => r.region === 'fold')?.items ?? [])
const evidence = computed(() => briefEvidence(items.value))
const regionOf = computed(() => {
  const out = new Map<number, BriefRegionName>()
  for (const r of briefRegions(items.value)) for (const x of r.items) out.set(x.index, r.region)
  return out
})

const when = (iso: string) => new Date(iso).toLocaleString()
const line = computed(() => provenance(props.request, props.request.agent?.agentId ? `${props.request.agent.agentId}${props.request.agent.adapter ? ` (${props.request.agent.adapter})` : ''}` : undefined, when))
const fold = ref(false)
/** On a phone, which of the folding regions are open. */
const unfolded = reactive<Record<string, boolean>>({ proposed: false, changed: false })
const folds = (region: BriefRegionName) => phone.value && (region === 'proposed' || region === 'changed')

/** Opens whatever fold holds the item a link names (`#item-<n>`), so that the page's scroll finds it drawn. */
function openFor(hash: string) {
  const m = /^#item-(\d+)$/.exec(hash)
  if (!m) return
  const region = regionOf.value.get(Number(m[1]))
  if (region === 'fold') fold.value = true
  else if (region === 'proposed' || region === 'changed') unfolded[region] = true
}
watch(() => route.hash, (h) => openFor(h))

onMounted(async () => {
  fresh.value = bk.unopened.value.includes(props.request.id)
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
  openFor(route.hash)
  // Opened once its items are drawn: the page scrolls to a `brief:` ref's item-<n> then, which is not there while it reads.
  await nextTick()
  emit('opened', props.request)
})

/** Which decision cards' live controls still offer something to answer, by item index: Needs you's mark shows while any does. */
const offers = reactive(new Map<number, boolean>())
const needsMark = computed(() => [...offers.values()].some(Boolean))
const needs = statusOfKey('needs')

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
const copy = useCopy()
const attention = useAttention()
const label = (r: string) =>
  refLabel(r, {
    session: (id) => attention.sessions.value.find((s) => s.id === id)?.name,
    run: (id) => attention.runNames.value[id],
  })
/** Copies the brief as Markdown: its reading marked as Brakeman's, each kind apart, links by the pages' own paths. */
function copyMarkdown() {
  const md = brief.value ? briefMarkdown(brief.value, props.request, label) : ''
  if (!md) {
    toast.add({ title: 'Nothing to copy', description: 'This brief holds no words to copy.', icon: 'i-lucide-clipboard-x', color: 'neutral' })
    return
  }
  void copy(md, 'The brief is copied as Markdown')
}
const KEEP_WHY = 'given to every later turn, whatever it may see'
const keepLabel = (fact: number | undefined) => (fact ? `Keep fact ${fact} as a note: ${KEEP_WHY}` : `Keep this reading as a note: ${KEEP_WHY}`)
</script>

<template>
  <div class="flex min-w-0 flex-col gap-4" :data-brief="request.id">
    <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
    <p v-else-if="loading" class="text-sm text-muted">Reading the brief…</p>
    <template v-else-if="brief">
      <BrakemanBriefHead v-if="head" :request="live" :brief="brief" :digest="live.digest" :fresh="fresh">
        <IconButton label="Copy as Markdown" icon="i-lucide-clipboard-copy" size="sm" color="neutral" variant="ghost" data-brief-copy @click="copyMarkdown" />
      </BrakemanBriefHead>
      <h2 class="break-words text-lg font-semibold text-highlighted" data-brief-title>{{ brief.title }}</h2>
      <section class="flex flex-col gap-1.5" data-brief-reading>
        <MarkdownPreview :text="brief.summary" dir="" :relative-links="false" class="text-sm" />
        <div class="flex flex-wrap items-baseline gap-x-1 text-xs text-muted" data-brief-caveat>
          <span>Brakeman's reading, not a fact.</span>
          <div v-if="brief.uncertainty?.trim()" class="flex min-w-0 flex-wrap items-baseline gap-x-1" data-brief-uncertainty>
            <span>Uncertain:</span>
            <MarkdownPreview :text="brief.uncertainty" dir="" :relative-links="false" class="min-w-0 text-xs" />
          </div>
        </div>
      </section>

      <section v-for="g in regions" :key="g.region" class="flex min-w-0 flex-col gap-2.5" :data-brief-region="g.region">
        <UButton
          v-if="folds(g.region)"
          :label="regionHeading(g.region, g.items)"
          :trailing-icon="unfolded[g.region] ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
          color="neutral"
          variant="outline"
          block
          class="min-h-11 justify-between"
          :aria-expanded="unfolded[g.region] ? 'true' : 'false'"
          :data-phone-section="g.region"
          @click="unfolded[g.region] = !unfolded[g.region]"
        />
        <h3 v-else class="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide" :class="g.region === 'needs' && needsMark ? 'text-warning' : 'text-muted'" :data-brief-heading="g.region">
          <StatusMark v-if="g.region === 'needs' && needsMark" :status="needs" />
          {{ regionHeading(g.region, g.items) }}
        </h3>
        <div v-show="!folds(g.region) || unfolded[g.region]" class="flex min-w-0 flex-col gap-2.5">
          <div v-if="g.region === 'acts' && apart.length" class="flex flex-col gap-2" data-brief-acts>
            <p class="text-xs text-muted">Each act this request ran under a grant you gave, with its receipt, whether or not the brief names it.</p>
            <BrakemanActCard v-for="a in apart" :key="a.opId" :act="a" />
          </div>
          <div v-for="x in g.items" :id="`item-${x.index}`" :key="x.index" class="flex min-w-0 flex-col gap-1.5" :class="x.item.kind === 'interpretation/1' && x.under ? 'pl-6' : ''" :data-brief-item="x.index">
            <BrakemanFact v-if="x.item.kind === 'fact/1'" :number="x.fact!" :text="x.item.text" :evidence="x.item.evidence" :refs="refs">
              <IconButton :label="keepLabel(x.fact)" icon="i-lucide-bookmark-plus" size="xs" color="neutral" variant="ghost" class="flex-none text-muted max-sm:min-h-11 max-sm:min-w-11 max-sm:justify-center" :loading="keeping === x.index" :data-keep-note="x.index" @click="keep(x.index)" />
            </BrakemanFact>
            <div v-else-if="x.item.kind === 'interpretation/1'" class="flex items-start gap-2" data-brief-interpretation>
              <p class="min-w-0 flex-1 break-words text-xs text-muted">
                <template v-if="readsWords(x.item.facts, items)">Brakeman <span data-reads>{{ readsWords(x.item.facts, items) }}</span>:</template>
                <template v-else>Brakeman's reading:</template>
                {{ x.item.text }}
              </p>
              <IconButton :label="keepLabel(undefined)" icon="i-lucide-bookmark-plus" size="xs" color="neutral" variant="ghost" class="flex-none text-muted max-sm:min-h-11 max-sm:min-w-11 max-sm:justify-center" :loading="keeping === x.index" :data-keep-note="x.index" @click="keep(x.index)" />
            </div>
            <BrakemanProposal v-else-if="x.item.kind === 'proposal/1'" :item="x.item" :items="items" :refs="refs" :request="request" />
            <div v-else-if="x.item.kind === 'outcome/1'" class="flex flex-wrap items-center gap-1.5" data-brief-outcome>
              <span class="text-xs font-medium text-muted">Verified:</span>
              <p class="text-sm">{{ x.item.text }}</p>
              <BrakemanRefChip :ref-name="x.item.receipt" :resolution="refs.get(x.item.receipt)" />
            </div>
            <BrakemanDecision v-else-if="x.item.kind === 'decision/1'" :item="x.item" :index="x.index" :refs="refs" @offered="offers.set(x.index, $event)" />
            <BrakemanDraftTicket v-else-if="x.item.kind === 'draft_ticket/1'" :request="request" :item="x.item" :index="x.index" :items="items" :refs="refs" />
            <BrakemanDraftCrew v-else-if="x.item.kind === 'draft_crew/1'" :request="request" :item="x.item" :index="x.index" :refs="refs" />
            <BrakemanDraftPlan v-else-if="x.item.kind === 'draft_plan/1'" :request="request" :item="x.item" :index="x.index" :refs="refs" />
            <BrakemanHandoff v-else-if="x.item.kind === 'handoff/1'" :item="x.item" :refs="refs" />
            <div v-else-if="x.item.kind === 'overlap/1'" class="flex flex-col gap-1" data-brief-overlap>
              <p class="break-all font-mono text-xs" data-mono="path">{{ x.item.paths.join(', ') }}</p>
              <div class="flex flex-wrap gap-1.5"><BrakemanRefChip v-for="r in x.item.between" :key="r" :ref-name="r" :resolution="refs.get(r)" /></div>
            </div>
            <div v-else-if="x.item.kind === 'comparison/1'" class="flex min-w-0 flex-col gap-1.5 overflow-x-auto" data-brief-comparison>
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
          </div>
        </div>
      </section>

      <!-- Last, folded and kept mounted: where the brief came from, and all it cites. -->
      <section class="flex min-w-0 flex-col" data-brief-region="fold">
        <UCollapsible v-model:open="fold" :unmount-on-hide="false" :ui="{ content: 'flex min-w-0 flex-col gap-2.5 pt-2' }">
          <UButton :label="foldWords(live)" :icon="fold ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" size="xs" color="neutral" variant="ghost" class="-ml-1.5 w-fit max-w-full text-left text-muted max-sm:min-h-11" :ui="{ label: 'whitespace-normal break-words' }" data-brief-fold />
          <template #content>
            <p class="break-words text-xs text-muted" data-brief-provenance>{{ line }}</p>
            <div v-for="x in panels" :id="`item-${x.index}`" :key="x.index" class="flex flex-col gap-1.5" :data-brief-item="x.index">
              <div v-if="x.item.kind === 'evidence/1'" class="flex flex-col gap-1.5" data-brief-evidence>
                <span class="text-sm font-medium">{{ x.item.title }}</span>
                <div class="flex flex-wrap gap-1.5"><BrakemanRefChip v-for="r in x.item.refs" :key="r" :ref-name="r" :resolution="refs.get(r)" /></div>
              </div>
            </div>
            <div v-if="evidence.length" class="flex flex-col gap-1.5" data-brief-all-evidence>
              <span class="text-xs font-medium text-muted">Everything the brief cites</span>
              <div class="flex flex-wrap gap-1.5"><BrakemanRefChip v-for="r in evidence" :key="r" :ref-name="r" :resolution="refs.get(r)" /></div>
            </div>
          </template>
        </UCollapsible>
      </section>
    </template>
    <template v-else>
      <p class="text-sm text-muted">No brief was kept for this request.</p>
      <p class="break-words text-xs text-muted" data-brief-provenance>{{ line }}</p>
    </template>
  </div>
</template>
