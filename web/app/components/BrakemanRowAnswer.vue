<script setup lang="ts">
import type { RunTask } from '~/composables/useSessions'
import type { BrakemanRequest, BrakemanRow } from '~/utils/brakeman'
import { closed, stateWords } from '~/utils/brakeman'
import { answerEvidence, answerWords, askAgainBody } from '~/utils/brakemanAbout'

/**
 * Brakeman's answer under a task's row (issue 993, F8): the newest request with the task (or one of its attempts) as a subject. While
 * it runs, "Reading now" and its progress; once briefed, its reading, at most three pieces of its evidence, Open the brief and Ask
 * again; closed with no brief, why, and Ask again. Its line says when it was asked, and "before attempt N" when the task has tried
 * again since. The words never say where it was asked from: the record does not keep that.
 */
const props = defineProps<{ request: BrakemanRow; runId: string; task: RunTask; now: number }>()
const emit = defineEmits<{ asked: [request: BrakemanRequest] }>()

const bk = useBrakeman()
const asker = useAskBrakeman()
const about = computed(() => `task:${props.runId}/${props.task.id}`)
const open = computed(() => !closed(props.request.state))
const briefed = computed(() => props.request.state === 'briefed' && !!props.request.hasBrief)

// The evidence is read with the brief, once for the request shown (the selected row's alone).
const evidence = ref<string[]>([])
watch(
  () => [props.request.id, briefed.value] as const,
  async ([id, b]) => {
    evidence.value = []
    if (!b) return
    try {
      const out = await bk.brief(id)
      if (id === props.request.id) evidence.value = answerEvidence(out.brief)
    } catch {
      /* the reading still shows; its evidence is the brief's, a click away */
    }
  },
  { immediate: true },
)

const again = computed(() => askAgainBody(props.request))
const asking = ref(false)
const problem = ref('')
async function askAgain() {
  const body = again.value
  if (!body || asking.value) return
  asking.value = true
  problem.value = ''
  try {
    emit('asked', await asker.send(body))
  } catch (e) {
    problem.value = (e as Error).message
  } finally {
    asking.value = false
  }
}
</script>

<template>
  <div class="flex min-w-0 flex-col gap-2 border-t border-default px-3 py-2.5 text-sm" data-task-brakeman-answer :data-request="request.id" :data-answer-state="request.state" :data-match="request.match || 'subject'">
    <div class="flex min-w-0 items-center gap-2 text-xs text-muted">
      <UIcon name="i-lucide-notebook-pen" class="size-3.5 flex-none" />
      <span class="min-w-0 break-words" data-answer-head>{{ answerWords(request, about, now, task.attempt) }}</span>
    </div>

    <template v-if="open">
      <span class="text-xs font-medium text-muted">Reading now</span>
      <BrakemanProgressLine :request="request" :progress="bk.progress.value[request.id]" compact />
    </template>

    <template v-else-if="briefed">
      <div class="min-w-0" data-answer-reading>
        <MarkdownPreview v-if="request.digest?.reading" :text="request.digest.reading" dir="" :relative-links="false" class="text-sm" />
        <p v-else class="text-muted">{{ request.digest?.title || 'The brief has no reading.' }}</p>
        <NuxtLink v-if="request.digest?.readingCut" :to="`/brakeman?request=${encodeURIComponent(request.id)}`" class="text-xs text-muted underline" data-answer-more>more</NuxtLink>
      </div>
      <div v-if="evidence.length" class="flex flex-wrap gap-1.5" data-answer-evidence>
        <BrakemanRefChip v-for="r in evidence" :key="r" :ref-name="r" />
      </div>
    </template>

    <p v-else class="text-muted" data-answer-words>{{ stateWords(request) }}</p>

    <div v-if="!open" class="flex flex-wrap items-center gap-2">
      <UButton v-if="briefed" label="Open the brief" icon="i-lucide-notebook-pen" size="xs" color="neutral" variant="outline" :to="`/brakeman?request=${encodeURIComponent(request.id)}`" data-answer-open />
      <UButton v-if="again" label="Ask again" icon="i-lucide-rotate-ccw" size="xs" color="neutral" variant="ghost" :loading="asking" data-answer-again @click="askAgain" />
    </div>
    <p v-if="problem" class="text-xs text-error" data-answer-problem>{{ problem }}</p>
  </div>
</template>
