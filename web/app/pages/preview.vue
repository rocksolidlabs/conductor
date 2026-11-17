<script setup lang="ts">
import type { RunInfo } from '~/composables/useSessions'
import type { LabelTarget } from '~/composables/usePreviewLabel'
import { runTitle } from '~/utils/crewWords'
import { CAPTURE_ID } from '~/utils/capture'

/**
 * The preview page (docs/plans/browser-evidence.md, "A preview in Conductor"): `/preview?run=&task=&attempt=`, and `&seq=` for a
 * candidate's checkout. It reads the run and the label alone, never the attempt's session, so a candidate whose session the
 * registry has collected opens as a live attempt's does; the pane (PreviewPane) frames the preview, labels it and captures it. A
 * candidate's checkout says it is being made while it opens and offers Close; `&retake=<capture>` offers Capture again of that capture.
 */
const route = useRoute()
const live = useAttention()
const admin = useWorkbenchToken()

const RUN = /^[A-Za-z0-9._-]{1,64}$/
const TASK = /^[A-Za-z0-9._-]{1,64}$/
const whole = (v: unknown) => (typeof v === 'string' && /^[1-9]\d{0,8}$/.test(v) ? Number(v) : 0)

/** The attempt the address names, or null for an address that names none. */
const target = computed<LabelTarget | null>(() => {
  const q = route.query
  const run = typeof q.run === 'string' ? q.run : ''
  const task = typeof q.task === 'string' ? q.task : ''
  const attempt = whole(q.attempt)
  const seq = q.seq === undefined ? 0 : whole(q.seq)
  if (!RUN.test(run) || !TASK.test(task) || !attempt || (q.seq !== undefined && !seq)) return null
  return { run, task, attempt, ...(seq ? { seq } : {}) }
})

const recordRun = ref<RunInfo | null>(null)
const run = computed<RunInfo | null>(() => (target.value ? (live.runOf(target.value.run) ?? recordRun.value) : null))
const gone = ref(false)
const error = ref('')
async function load() {
  const t = target.value
  if (!t) return
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  try {
    const r = await live.refreshRun(t.run)
    if (t.run !== target.value?.run) return
    error.value = ''
    gone.value = !r
    recordRun.value = r?.record ? r : null
  } catch (e) {
    error.value = (e as Error).message
  }
}
watch(() => target.value?.run, load, { immediate: true })

const task = computed(() => run.value?.tasks?.find((t) => t.id === target.value?.task))
/** A capture the address offers Capture again of (`&retake=`), its id alone. */
const retake = computed(() => (typeof route.query.retake === 'string' && CAPTURE_ID.test(route.query.retake) ? route.query.retake : undefined))
/** The candidate's checkout this page shows, as the run reads it: its words while it opens, and Close. */
const checkout = computed(() => {
  const a = task.value?.attempt
  const t = target.value
  return t?.seq && a?.n === t.attempt && a.previewing?.seq === t.seq ? a.previewing.state : ''
})
const previews = useCandidatePreview(() => target.value?.run ?? '')
const router = useRouter()
async function closeCheckout() {
  const t = target.value
  if (t?.seq && (await previews.close(t.task, t.attempt, t.seq))) await router.push(runPath.value)
}
const closeRefused = computed(() => (target.value ? (previews.refused.value[`${target.value.task}:${target.value.attempt}`] ?? '') : ''))
/** The attempt's session while the run's read names it: a source's file opens there. */
const attemptSession = computed(() => (task.value?.attempt?.n === target.value?.attempt ? task.value?.attempt?.sessionId : undefined))
const runPath = computed(() => (target.value ? `/runs/${encodeURIComponent(target.value.run)}?view=tasks&task=${encodeURIComponent(target.value.task)}` : '/crews'))
const title = computed(() => (target.value ? `${target.value.task} · attempt ${target.value.attempt}${target.value.seq ? ` · candidate ${target.value.seq}` : ''}` : 'Preview'))
useHead({ title: computed(() => `${title.value} · preview`) })
</script>

<template>
  <UDashboardPanel id="preview" :ui="{ body: 'p-0 sm:p-0 flex flex-col min-h-0 gap-0 overflow-hidden' }">
    <template #header>
      <UDashboardNavbar :toggle="false" :ui="{ left: 'min-w-0 flex-1', right: 'flex-none gap-1.5' }" data-preview-header>
        <template #leading>
          <IconButton icon="i-lucide-arrow-left" color="neutral" variant="ghost" label="Back to the run" :to="runPath" data-preview-back />
        </template>
        <template #title>
          <span class="flex min-w-0 items-baseline gap-2">
            <span class="min-w-0 truncate" data-preview-title>{{ title }}</span>
            <NuxtLink v-if="run" :to="runPath" class="hidden min-w-0 truncate text-[13px] font-normal text-muted hover:underline sm:inline" data-preview-run>{{ runTitle(run) }}</NuxtLink>
          </span>
        </template>
        <template #right>
          <UButton v-if="target?.seq" label="Close the preview" icon="i-lucide-circle-stop" size="sm" color="neutral" variant="outline" :loading="!!previews.busy.value" data-preview-close @click="closeCheckout" />
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <PageEmpty v-if="!target" state="missing" words="This address names no attempt: a preview page needs ?run=, &task= and &attempt=." :actions="[{ label: 'Crews', icon: 'i-lucide-arrow-left', to: '/crews' }]" data-preview-invalid />
      <PageEmpty v-else-if="gone" state="missing" :words="`No run has the id ${target.run}.`" :actions="[{ label: 'Crews', icon: 'i-lucide-arrow-left', to: '/crews' }]" data-preview-gone />
      <template v-else>
        <div v-if="error" class="flex-none px-3 pt-3">
          <UAlert color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
        </div>
        <div v-if="closeRefused" class="flex-none px-3 pt-3">
          <UAlert color="neutral" variant="subtle" icon="i-lucide-circle-alert" :title="closeRefused" data-preview-close-refused />
        </div>
        <PreviewPane :retake="retake" :checkout="checkout" :target="target" :revision="run?.managed?.revision" :run-ticket="run?.ticket?.key" :attempt-session="attemptSession" class="min-h-0 flex-1" />
      </template>
    </template>
  </UDashboardPanel>
</template>
