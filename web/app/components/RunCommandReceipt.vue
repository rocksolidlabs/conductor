<script setup lang="ts">
import type { HistoryCommand } from '~/utils/history'
import { relativeTime } from '~/utils/sessions'

/**
 * A command's receipt on the run page (Program item HS1, docs/plans/history.md, PR-6): the page opened at `?command=<id>&actor=<actor>`,
 * a history row's link, shows that command as the ledger keeps it: its intent, its actor, when it was made, its state and the answer
 * its receipt holds. It is read from the history's evidence reader by the actor the link names (GET /api/history/command), never the
 * caller's own receipt of the same id, which the run's command route finds (the read a client that lost a reply makes). Read when the
 * link names it, never written back; Close hides it on this page alone.
 */
const props = defineProps<{ runId: string; command: string; actor: string; now: number }>()

const history = useHistory()
const shown = ref<HistoryCommand | null>(null)
const error = ref('')
const closed = ref(false)
let asked = 0

watch(
  () => [props.runId, props.command, props.actor] as const,
  async ([run, id, actor]) => {
    const mine = ++asked
    shown.value = null
    error.value = ''
    closed.value = false
    try {
      const c = await history.command(run, actor, id)
      if (mine === asked) shown.value = c
    } catch (e) {
      if (mine !== asked) return
      const err = e as { status?: number; message?: string }
      error.value = err.status === 404 ? `The ledger holds no command ${id} of ${actor} in this run.` : err.message || 'The receipt could not be read.'
    }
  },
  { immediate: true },
)

const answer = computed(() => {
  const r = shown.value?.receipt
  if (!r) return ''
  return [r.error || r.refusal, r.revision ? `at revision ${r.revision}` : '', r.task ? `${r.task.id} ${r.task.state}` : '', r.paused === true ? 'scheduling paused' : r.paused === false ? 'scheduling on' : ''].filter(Boolean).join(' · ')
})
</script>

<template>
  <section v-if="!closed" class="flex flex-col gap-1.5 rounded-md border border-default p-3 text-sm" data-run-command :data-run-command-id="command">
    <div class="flex items-center gap-2">
      <UIcon name="i-lucide-receipt" class="size-4 flex-none text-muted" />
      <span class="font-medium">Command receipt</span>
      <code class="font-mono text-xs text-muted">{{ command }}</code>
      <UButton icon="i-lucide-x" size="xs" variant="ghost" color="neutral" class="ml-auto" aria-label="Close" data-run-command-close @click="closed = true" />
    </div>
    <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-run-command-error />
    <p v-else-if="!shown" class="text-xs text-muted">Reading the receipt…</p>
    <dl v-else class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs">
      <dt class="text-muted">Intent</dt>
      <dd class="font-mono" data-run-command-intent>{{ shown.intent }}</dd>
      <dt class="text-muted">Actor</dt>
      <dd data-run-command-actor>{{ shown.actor }}</dd>
      <dt class="text-muted">When</dt>
      <dd :title="shown.createdAt" data-run-command-at>{{ relativeTime(shown.createdAt, now, { suffix: true }) }} · {{ shown.createdAt }}</dd>
      <dt class="text-muted">State</dt>
      <dd data-run-command-status>
        <UBadge :label="shown.status" :color="shown.status === 'done' ? 'success' : shown.status === 'failed' ? 'error' : 'neutral'" variant="subtle" size="sm" />
      </dd>
      <template v-if="answer">
        <dt class="text-muted">Answer</dt>
        <dd class="break-words" data-run-command-answer>{{ answer }}</dd>
      </template>
    </dl>
  </section>
</template>
