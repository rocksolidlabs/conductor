<script setup lang="ts">
import type { BrakemanBrief, BrakemanRequest, BriefItem, RefResolution } from '~/utils/brakeman'
import { groupItems, provenance, readsWords, refRoute } from '~/utils/brakeman'

/**
 * A brief as Conductor renders it (docs/plans/brakeman.md, PR-6): the provenance line (the agent, its model and version, the
 * request, when it was kept), the title, Brakeman's reading (its summary, marked as its interpretation), then the items in their
 * groups: facts with their evidence as chips, interpretations naming the facts they read, proposals naming their operation (their
 * controls come with the decision cards), verified outcomes with their receipt, and the comparison and evidence panels. Each item is
 * anchored at `item-<index>`, where a `brief:` ref leads. The brief and its refs are read once, as the brief is opened.
 */
const props = defineProps<{ request: BrakemanRequest }>()
const emit = defineEmits<{ opened: [id: string] }>()

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
  emit('opened', props.request.id)
})

const when = (iso: string) => new Date(iso).toLocaleString()
const line = computed(() => provenance(props.request, props.request.agent?.agentId ? `${props.request.agent.agentId}${props.request.agent.adapter ? ` (${props.request.agent.adapter})` : ''}` : undefined, when))
const groups = computed(() => groupItems(brief.value?.items ?? []))
const items = computed<BriefItem[]>(() => brief.value?.items ?? [])
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
        <MarkdownPreview :text="brief.summary" dir="" :relative-links="false" class="text-sm" />
        <p v-if="brief.uncertainty" class="text-xs text-muted" data-brief-uncertainty>Uncertain: {{ brief.uncertainty }}</p>
      </section>
      <section v-for="g in groups" :key="g.group" class="flex flex-col gap-2" :data-brief-group="g.group">
        <h4 class="text-xs font-semibold uppercase tracking-wide text-muted">{{ g.title }}</h4>
        <div v-for="x in g.items" :id="`item-${x.index}`" :key="x.index" class="flex flex-col gap-1.5" :data-brief-item="x.index">
          <BrakemanFact v-if="x.item.kind === 'fact/1'" :number="x.fact!" :text="x.item.text" :evidence="x.item.evidence" :refs="refs" />
          <div v-else-if="x.item.kind === 'interpretation/1'" class="flex flex-col gap-0.5" data-brief-interpretation>
            <p class="text-sm">{{ x.item.text }}</p>
            <span class="text-xs text-muted" data-reads>{{ readsWords(x.item.facts, items) }}</span>
          </div>
          <div v-else-if="x.item.kind === 'proposal/1'" class="flex flex-col gap-1" data-brief-proposal>
            <p class="text-sm">{{ x.item.text }}</p>
            <div class="flex flex-wrap items-center gap-1.5 text-xs text-muted">
              <span>Operation <code class="font-mono">{{ x.item.op }}</code> on</span>
              <BrakemanRefChip :ref-name="x.item.target" :resolution="refs.get(x.item.target)" />
              <span v-if="readsWords(x.item.facts, items)">· {{ readsWords(x.item.facts, items) }}</span>
            </div>
            <p class="text-xs text-muted" data-proposal-later>
              Its control comes in the next part of Brakeman; until then do it from
              <NuxtLink v-if="refRoute(x.item.target)" :to="refRoute(x.item.target)!" class="underline">its page</NuxtLink><template v-else>its page</template>.
            </p>
          </div>
          <div v-else-if="x.item.kind === 'outcome/1'" class="flex flex-wrap items-center gap-1.5" data-brief-outcome>
            <p class="text-sm">{{ x.item.text }}</p>
            <BrakemanRefChip :ref-name="x.item.receipt" :resolution="refs.get(x.item.receipt)" />
          </div>
          <div v-else-if="x.item.kind === 'decision/1'" class="flex flex-col gap-1" data-brief-decision>
            <p class="text-sm font-medium">{{ x.item.question }}</p>
            <ul class="list-disc pl-5 text-sm">
              <li v-for="(a, i) in x.item.alternatives" :key="i">{{ a.label }}<span v-if="x.item.recommendation === i" class="text-muted"> (recommended)</span></li>
            </ul>
            <div class="flex flex-wrap gap-1.5"><BrakemanRefChip :ref-name="x.item.about" :resolution="refs.get(x.item.about)" /></div>
            <p class="text-xs text-muted">The decision card's controls come in the next part of Brakeman.</p>
          </div>
          <div v-else-if="x.item.kind === 'draft_ticket/1' || x.item.kind === 'draft_crew/1'" class="text-sm" data-brief-draft>
            {{ x.item.kind === 'draft_ticket/1' ? `A draft ticket in ${x.item.project}: ${x.item.title}` : `A draft crew: ${x.item.crew?.name ?? 'unnamed'}` }}
            <span class="text-xs text-muted"> (made work from the next part of Brakeman)</span>
          </div>
          <div v-else-if="x.item.kind === 'handoff/1'" class="text-sm" data-brief-handoff>A handoff: {{ x.item.objective }}</div>
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
