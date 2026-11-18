<script setup lang="ts">
import type { BrakemanRequest } from '~/utils/brakeman'
import type { AskChip } from '~/utils/brakemanAsk'
import { aboutNoun, latestWords, noneWords, queuedWords, subjectUnder } from '~/utils/brakemanAbout'
import { askDisabled, askedToast } from '~/utils/brakemanAsk'
import { countsWords, shortKind, subjectWords } from '~/utils/brakemanRows'
import type { BrakemanAbout } from '~/composables/useBrakemanAbout'

/**
 * Brakeman on a run's or a ticket's page (issue 993, F8): the latest brief about the subject (its age, its title leading to it on
 * Brakeman's page, its counts), what Brakeman is reading about it now, and the ask bar, compact, with the place's chips. Drawn only
 * where Brakeman answers this person (its state read, the list's `about=` filter taken) and is on or has said something about the
 * subject; nothing is drawn on a refusal. Nothing here moves the page or takes the focus: a brief arriving changes the panel's lines.
 */
const props = defineProps<{
  /** The subject's ref: `run:<id>` or `ticket:<key>`. */
  subject: string
  /** The subject by name, for the bar's label: "Ask about <label>". */
  label: string
  /** The bar's chips for the place (askChips). */
  chips: AskChip[]
  /** The page's reads of the subject, when it has them already (a run's page shares them with its rows' answers). */
  about?: BrakemanAbout
}>()
const emit = defineEmits<{ asked: [request: BrakemanRequest] }>()

const bk = useBrakeman()
const own = props.about ? null : useBrakemanAbout(() => props.subject)
const about = computed(() => props.about ?? own!)
const live = useAttention()
const now = useNow()
const names = { run: (id: string) => live.runNames.value[id], session: (id: string) => live.sessions.value.find((s) => s.id === id)?.name }

const noun = computed(() => aboutNoun(props.subject))
/** Drawn where Brakeman's state answered this person and the filter was taken, and Brakeman is on or said something about it. */
const shown = computed(() => {
  const s = bk.state.value
  if (bk.available.value !== true || !s || about.value.refused.value || !about.value.loaded.value) return false
  return s.config.enabled || !!about.value.latest.value
})
const latest = computed(() => about.value.latest.value)
const disabled = computed(() => askDisabled(bk.state.value))
const counts = computed(() => countsWords(latest.value?.digest, latest.value?.made).join(' · '))
const placeholder = computed(() => `Ask about this ${noun.value}`)

const toast = useToast()
/**
 * An ask from the bar: one about the subject is read here; one that is not (Overlapping changes names no subject) says where it went,
 * the request's id with Open, since the panel will not show it.
 */
function asked(r: BrakemanRequest) {
  if (!subjectUnder(props.subject, r.subjects ?? [])) toast.add(askedToast(r))
  about.value.add(r)
  emit('asked', r)
}
</script>

<template>
  <section v-if="shown" class="flex min-w-0 flex-col gap-3 rounded-lg p-4 text-sm ring ring-default" :data-brakeman-subject-panel="subject">
    <h2 class="flex items-center gap-2 text-sm font-semibold text-highlighted">
      <UIcon name="i-lucide-notebook-pen" class="size-4 flex-none text-muted" />
      Brakeman on this {{ noun }}
    </h2>

    <div v-if="latest" class="flex min-w-0 flex-col gap-1" data-panel-latest :data-request="latest.id" :data-match="latest.match">
      <span class="text-xs text-muted">{{ latestWords(latest, subject, now) }}</span>
      <NuxtLink :to="`/brakeman?request=${encodeURIComponent(latest.id)}`" class="min-w-0 break-words font-medium text-highlighted hover:underline" :title="latest.digest?.title" data-panel-latest-link>
        {{ latest.digest?.title || `${shortKind(latest.kind)} · ${latest.id}` }}
      </NuxtLink>
      <span v-if="counts" class="text-xs text-muted" data-panel-counts>{{ counts }}</span>
    </div>

    <div v-if="about.open.value.length" class="flex min-w-0 flex-col gap-2" data-panel-reading-now>
      <span class="text-xs font-medium text-muted">Reading now</span>
      <div v-for="o in about.open.value" :key="o.request.id" class="flex min-w-0 flex-col gap-1" :data-request="o.request.id" :data-request-state="o.request.state">
        <span class="min-w-0 break-words">{{ shortKind(o.request.kind) }}<template v-if="o.request.subjects?.length"> · {{ subjectWords(o.request.subjects, names) }}</template></span>
        <span v-if="o.place > 0" class="text-xs text-muted" data-panel-queued>{{ queuedWords(o.place) }}</span>
        <BrakemanProgressLine v-else :request="o.request" :progress="bk.progress.value[o.request.id]" compact />
      </div>
    </div>

    <p v-if="!latest && !about.open.value.length" class="text-muted" data-panel-none>{{ noneWords(subject) }}</p>

    <BrakemanAskBar :subject="subject" :label="`Ask about ${label}`" :placeholder="placeholder" :chips="chips" size="compact" :disabled="disabled" @asked="asked" />
  </section>
</template>
