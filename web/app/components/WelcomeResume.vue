<script setup lang="ts">
import { allEndedWords, endedWords, endNotSeen, WELCOME_RESUME_READ, welcomeResumeRows, type SessionSummary } from '~/utils/history'
import { shortCwd } from '~/utils/sessions'

/**
 * The welcome's "Pick up where you left off" (UX3, the welcome's plan row 4): the sessions that ended last, newest end first, three
 * at most, each with Resume, which asks first and resumes as the palette does (useRecordResume); a record whose end was not seen opens
 * its own page, which asks there. A read that fails, or a history not ready, draws nothing: History says why. Tells the card how many
 * rows it draws (`rows`), so the card's title can say nothing is running.
 */
const emit = defineEmits<{ rows: [n: number] }>()

const history = useHistory()
const router = useRouter()
const now = useNow()
const { resume } = useRecordResume({ go: (to) => void router.push(to) })

const records = ref<SessionSummary[]>([])
const life = new AbortController()
onMounted(async () => {
  try {
    records.value = await history.sessions(WELCOME_RESUME_READ, life.signal)
  } catch {
    records.value = []
  }
})
onBeforeUnmount(() => {
  life.abort()
  emit('rows', 0)
})
const offer = computed(() => welcomeResumeRows(records.value))
watch(
  () => offer.value.rows.length,
  (n) => emit('rows', n),
  { immediate: true },
)

/** The row Resume asks about, and whether its resume is under way. */
const asking = ref<SessionSummary | null>(null)
const open = computed({
  get: () => !!asking.value,
  set: (v) => !v && !busy.value && (asking.value = null),
})
const busy = ref(false)
function ask(r: SessionSummary) {
  if (endNotSeen(r)) return void router.push(`/history/sessions/${encodeURIComponent(r.id)}`)
  asking.value = r
}
async function yes() {
  const r = asking.value
  if (!r || busy.value) return
  busy.value = true
  try {
    await resume(r.id, r.name)
  } finally {
    busy.value = false
    asking.value = null
  }
}
</script>

<template>
  <section v-if="offer.rows.length" class="flex flex-col gap-1.5" data-welcome-resume>
    <h3 class="text-xs font-medium text-muted">Pick up where you left off</h3>
    <ul class="flex flex-col gap-1">
      <li v-for="r in offer.rows" :key="r.id" class="flex min-w-0 items-center gap-2 text-sm" :data-resume-row="r.id">
        <!-- On a phone two lines, the name whole on the first (it wraps before it is cut), the folder and the age on the second; from
             sm up one line, the name cut at its end with its whole form in its title. -->
        <span class="flex min-w-0 flex-1 flex-col gap-0.5 sm:flex-row sm:items-baseline sm:gap-1">
          <span class="min-w-0 text-default [overflow-wrap:anywhere] sm:shrink sm:truncate" :title="r.name" data-resume-name>{{ r.name }}</span>
          <span class="flex-none text-muted max-sm:hidden" aria-hidden="true">·</span>
          <span class="flex min-w-0 items-baseline gap-1 sm:shrink-[2]">
            <span class="min-w-0 truncate text-left font-mono text-xs text-muted [direction:rtl]" :title="r.cwd" data-mono="path" data-resume-folder
              ><bdi>{{ shortCwd(r.cwd) }}</bdi></span
            >
            <span class="flex-none whitespace-nowrap text-muted" data-resume-age>· {{ endedWords(r, now) }}</span>
          </span>
        </span>
        <UButton
          :label="endNotSeen(r) ? 'Resume…' : 'Resume'"
          :aria-label="`${endNotSeen(r) ? 'Resume…' : 'Resume'} ${r.name}`"
          size="sm"
          color="neutral"
          variant="outline"
          class="flex-none pointer-coarse:min-h-11"
          data-resume-button
          @click="ask(r)"
        />
      </li>
    </ul>
    <ULink v-if="offer.total > offer.rows.length" to="/history?q=kind:session" class="inline-flex items-center self-start py-1 text-sm text-muted underline underline-offset-2 hover:text-default pointer-coarse:min-h-11" data-resume-all>{{ allEndedWords(offer.total) }}</ULink>

    <UModal v-model:open="open" :title="asking ? `Resume ${asking.name}?` : ''" :ui="{ footer: 'justify-end' }" data-resume-ask>
      <template #footer>
        <UButton label="Keep it" color="neutral" variant="outline" :disabled="busy" data-resume-keep @click="open = false" />
        <UButton label="Resume" color="primary" :loading="busy" data-resume-yes @click="yes" />
      </template>
    </UModal>
  </section>
</template>
