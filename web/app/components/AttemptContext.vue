<script setup lang="ts">
import { type AttemptContextRow, contextReadError, contextSize, contextSummary, phaseWords, shortDigest } from '~/utils/attemptContext'

/**
 * The context an attempt was given (docs/plans/context-seams.md, CT-3): one row per block a context provider gave it, at its start or
 * by a lookup while it worked, as the ledger records it: its phase, the provider, its title, the provider's pack mark with Copy, the
 * short digest of its text and the text's size. A row opens its text in a slideover, as text in a monospace face, as the agent read it
 * (never drawn as Markdown), read from the body route, which gives it only when its SHA-256 is the row's. The list states facts, not a
 * status: no status colour. A list, never a table: one column on a phone.
 */
const props = defineProps<{
  runId: string
  task: string
  attempt: number
  rows: AttemptContextRow[]
}>()

const api = useSessions()
const copy = useCopy()

const open = ref(false)
const shown = ref<AttemptContextRow | null>(null)
const text = ref<string | null>(null)
const error = ref('')
let reading = 0

async function openRow(r: AttemptContextRow) {
  const mine = ++reading
  shown.value = r
  text.value = null
  error.value = ''
  open.value = true
  try {
    const got = await api.attemptContextBody(props.runId, props.task, props.attempt, r.seq)
    if (mine === reading) text.value = got.text
  } catch (e) {
    if (mine === reading) error.value = contextReadError(e)
  }
}
</script>

<template>
  <section class="flex flex-col gap-1" data-attempt-context :data-attempt-context-attempt="attempt">
    <h5 class="text-xs font-medium text-muted" data-attempt-context-summary>{{ contextSummary(rows) }}</h5>
    <ol class="flex flex-col gap-1">
      <li
        v-for="r in rows"
        :key="r.seq"
        class="flex min-w-0 flex-col gap-0.5 rounded border border-default/60 px-2 py-1 text-xs sm:flex-row sm:flex-wrap sm:items-center sm:gap-x-2"
        :data-attempt-context-row="r.seq"
        :data-attempt-context-phase="r.phase"
      >
        <span class="flex-none text-muted">{{ phaseWords(r.phase) }} · {{ r.source }}</span>
        <!-- The title whole, wrapping: a button's own label is cut to one line. -->
        <UButton
          size="xs"
          color="neutral"
          variant="link"
          class="min-w-0 justify-start p-0 text-left text-default"
          :aria-label="`Read the text of ${r.title}`"
          :data-attempt-context-open="r.seq"
          @click="openRow(r)"
        >
          <span class="min-w-0 whitespace-normal break-words">{{ r.title }}</span>
        </UButton>
        <span v-if="r.pack" class="flex min-w-0 items-center gap-1" data-attempt-context-pack>
          <span class="text-muted">pack mark</span>
          <code class="min-w-0 break-all font-mono">{{ r.pack }}</code>
          <IconButton icon="i-lucide-copy" size="xs" color="neutral" variant="ghost" :label="`Copy the pack mark of ${r.title}`" :data-attempt-context-copy="r.seq" @click="copy(r.pack, 'Pack mark copied', r.pack)" />
        </span>
        <span class="flex flex-none items-center gap-2 text-muted sm:ml-auto">
          <code class="font-mono" :title="r.sha256" data-attempt-context-digest>{{ shortDigest(r.sha256) }}</code>
          <span data-attempt-context-size>{{ contextSize(r.bytes) }}</span>
        </span>
      </li>
    </ol>

    <USlideover v-model:open="open" :title="shown?.title ?? ''" :description="shown ? `${phaseWords(shown.phase)} from ${shown.source}, ${contextSize(shown.bytes)}` : ''">
      <template #body>
        <div v-if="shown" class="flex flex-col gap-2 text-sm" data-attempt-context-text :data-attempt-context-text-seq="shown.seq">
          <p v-if="shown.ref" class="text-xs text-muted">About <code class="break-all font-mono">{{ shown.ref }}</code></p>
          <p class="text-xs text-muted">SHA-256 <code class="break-all font-mono">{{ shown.sha256 }}</code></p>
          <p v-if="error" class="text-warning" data-attempt-context-text-error>{{ error }}</p>
          <p v-else-if="text === null" class="text-dimmed">Reading the text…</p>
          <pre v-else class="whitespace-pre-wrap break-words rounded border border-default bg-elevated/40 p-2 font-mono text-xs" data-attempt-context-body>{{ text }}</pre>
        </div>
      </template>
    </USlideover>
  </section>
</template>
