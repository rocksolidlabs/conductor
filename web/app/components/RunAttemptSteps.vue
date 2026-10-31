<script setup lang="ts">
import type { RunAttempt, RunStep, StepLog } from '~/composables/useSessions'
import { stepCommand, stepHasLog, stepManifest, stepPorts, stepWhere, stepWords } from '~/utils/tasks'

/**
 * An attempt's receipts (EN1, project manifests), or a run's prerequisites':
 * each step in the order it started, with its phase, id, outcome in words,
 * the command (a probe over HTTP: `GET` and its URL), where it ran (the
 * attempt's worktree, its result's checkout, or the crew's directory), the
 * manifest that produced it and its ports, and Log, which fetches the
 * receipt's log through the API with the bearer header (`log`: `stepLog` or
 * `prerequisiteLog`, never a link the browser follows) and shows it with its
 * SHA-256 and a Save that downloads it from a blob URL: the log's bytes as
 * the server sent them, never its decoded text, so the file matches the hash
 * even where the log holds bytes that are no UTF-8. A run the server has let
 * go reads the same: the steps come from the ledger.
 */
const props = defineProps<{
  steps: RunStep[]
  now: number
  /** The attempt the steps are of, to say worktree or checkout; absent for the prerequisites. */
  attempt?: Pick<RunAttempt, 'n' | 'worktree'>
  /** Fetches a receipt's log. */
  log: (s: RunStep) => Promise<StepLog>
  /** The file name a saved log gets, before `.log`. */
  saveAs: (s: RunStep) => string
  /** What the list is, for its label: "Steps" or "Prerequisites". */
  title?: string
}>()

const tone = { warning: 'text-warning', error: 'text-error', muted: 'text-muted', default: 'text-default', success: 'text-success' }
const phaseColor = (p: RunStep['phase']): 'neutral' | 'info' | 'primary' | 'warning' => (p === 'check' || p === 'verify' ? 'primary' : p === 'service' || p === 'ready' ? 'info' : p === 'cleanup' ? 'warning' : 'neutral')
const key = (s: RunStep) => `${s.phase}:${s.id}:${s.k}`

const shown = ref<{ step: RunStep; text: string; bytes?: Uint8Array; sha256: string; error: string; loading: boolean } | null>(null)
const open = computed({
  get: () => !!shown.value,
  set: (v: boolean) => {
    if (!v) shown.value = null
  },
})
async function showLog(s: RunStep) {
  shown.value = { step: s, text: '', sha256: '', error: '', loading: true }
  try {
    const l = await props.log(s)
    if (shown.value?.step !== s) return
    shown.value = { step: s, text: l.text, bytes: l.bytes, sha256: l.sha256, error: '', loading: false }
  } catch (e) {
    if (shown.value?.step !== s) return
    const err = e as { status?: number; code?: string; message?: string }
    const words = err.status === 410 || err.code === 'log_dropped' ? "The log was not kept: the run's logs were at their bound. Its SHA-256 stays on the receipt." : err.status === 404 ? 'This step has no log.' : err.message || 'The log could not be read.'
    shown.value = { step: s, text: '', sha256: s.logSha256 ?? '', error: words, loading: false }
  }
}
function save() {
  const v = shown.value
  if (!v || v.error || !v.bytes) return
  const url = URL.createObjectURL(new Blob([v.bytes as BlobPart], { type: 'text/plain' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `${props.saveAs(v.step)}.log`
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 60_000)
}
</script>

<template>
  <div class="flex flex-col gap-1" data-steps :data-steps-of="attempt ? attempt.n : 'prerequisites'">
    <div v-if="title" class="text-xs font-medium text-muted">{{ title }}</div>
    <ol class="flex flex-col gap-1">
      <li v-for="s in steps" :key="key(s)" class="flex flex-col gap-0.5 rounded border border-default/60 bg-elevated/20 px-2 py-1 text-xs" :data-step-row="key(s)" :data-step-state="s.state">
        <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
          <UBadge :label="s.phase" :color="phaseColor(s.phase)" variant="subtle" size="sm" class="flex-none" />
          <span class="font-mono font-medium text-highlighted">{{ s.id }}</span>
          <span v-if="s.k > 1" class="text-muted" :title="`The ${s.k}th run of ${s.id}`">#{{ s.k }}</span>
          <span :class="tone[stepWords(s, now).tone]" data-step-words>{{ stepWords(s, now).text }}</span>
          <UButton
            v-if="stepHasLog(s)"
            label="Log"
            icon="i-lucide-file-text"
            size="xs"
            color="neutral"
            variant="ghost"
            class="ml-auto"
            :disabled="!!s.logDropped"
            :title="s.logDropped ? 'The log was not kept: the run\'s logs were at their bound' : `${s.logBytes} bytes`"
            :data-step-log="key(s)"
            @click="showLog(s)"
          />
        </div>
        <p v-if="s.error" class="whitespace-pre-wrap break-words text-error" data-step-error>{{ s.error }}</p>
        <div class="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-muted">
          <code class="min-w-0 max-w-full truncate font-mono" :title="stepCommand(s)" data-step-command>{{ stepCommand(s) }}</code>
          <span v-if="s.dir" :title="s.dir" data-step-where>in {{ stepWhere(s, attempt) }}</span>
          <span v-if="s.manifest" :title="s.manifest" data-step-manifest>{{ stepManifest(s.manifest) }}</span>
          <span v-if="s.ports && Object.keys(s.ports).length" data-step-ports>ports {{ stepPorts(s.ports) }}</span>
        </div>
      </li>
    </ol>

    <UModal v-model:open="open" :title="shown ? `${shown.step.phase} ${shown.step.id}${shown.step.k > 1 ? ` #${shown.step.k}` : ''}: log` : ''" :description="shown ? stepCommand(shown.step) : ''" :ui="{ content: 'sm:max-w-3xl' }">
      <template #body>
        <div class="flex flex-col gap-2" data-step-log-modal>
          <p v-if="shown?.loading" class="text-sm text-muted">Reading the log…</p>
          <UAlert v-else-if="shown?.error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="shown.error" data-step-log-error />
          <pre v-else class="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words rounded bg-elevated/40 p-2 font-mono text-xs" data-step-log-text>{{ shown?.text }}</pre>
          <p v-if="shown?.sha256" class="break-all font-mono text-[11px] text-muted">SHA-256 <span data-step-log-sha>{{ shown.sha256 }}</span></p>
        </div>
      </template>
      <template #footer>
        <div class="flex w-full justify-end gap-2">
          <UButton label="Close" color="neutral" variant="ghost" @click="shown = null" />
          <UButton label="Save" icon="i-lucide-download" color="neutral" variant="outline" :disabled="!shown || shown.loading || !!shown.error" data-step-log-save @click="save" />
        </div>
      </template>
    </UModal>
  </div>
</template>
