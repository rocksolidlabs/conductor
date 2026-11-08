<script setup lang="ts">
import { parseRef } from '~/utils/brakeman'
import type { LaneEvidence } from '~/utils/transport/decide'

/**
 * An item's evidence on the decision page, folded until asked for: each ref with its label, a link where the page's route function
 * gives one (the session's terminal, the run at the task, the attempt, the ticket, the revision), so every deeper page stays one tap
 * away. A failed check's log tail (its last 4 KiB, from the step log route the run page reads) loads only when its entry is unfolded.
 *
 * On a decision link's page (the join page's Decide tab, MB0's PR-7) the workbench's routes are not the holder's: `read` reads an
 * entry through the link's lane instead (a check's log's end), `openTerminal` opens a session in the link's scope in its terminal
 * tab, and any other entry shows its label and where it can be opened.
 */
const props = defineProps<{
  evidence: ReadonlyArray<{ ref: string; label: string }>
  routeOf: (ref: string) => string | null
  /** A decision link's read of one entry (decision_evidence). */
  read?: (evidence: string) => Promise<LaneEvidence>
  /** A decision link's way to a session's terminal tab. */
  openTerminal?: (ref: string) => void
}>()

const api = useSessions()
const open = ref(false)
/** A check's log tail by its ref: the text, or the words why it could not be read; absent until asked for. */
const logs = ref<Record<string, { text?: string; error?: string; loading?: boolean }>>({})
/** How much of a log's end is shown. */
const TAIL = 4 << 10

function tail(text: string): string {
  const b = new TextEncoder().encode(text)
  if (b.length <= TAIL) return text
  return '…' + new TextDecoder().decode(b.subarray(b.length - TAIL)).replace(/^�+/, '')
}

async function toggleLog(r: string) {
  if (logs.value[r]) {
    const next = { ...logs.value }
    delete next[r]
    logs.value = next
    return
  }
  const p = parseRef(r)
  if (!p || p.kind !== 'check' || !p.run || !p.task || p.n === undefined || !p.check || p.k === undefined) return
  logs.value = { ...logs.value, [r]: { loading: true } }
  try {
    if (props.read) {
      const e = await props.read(r)
      logs.value = { ...logs.value, [r]: { text: e.kind === 'log' ? tail(e.tail) : '' } }
      return
    }
    const log = await api.stepLog(p.run, p.task, p.n, p.check, p.k)
    logs.value = { ...logs.value, [r]: { text: tail(log.text) } }
  } catch (e) {
    logs.value = { ...logs.value, [r]: { error: (e as Error).message } }
  }
}
const isCheck = (r: string) => r.startsWith('check:')
const isSession = (r: string) => r.startsWith('session:')
/** On a link: an entry the lane serves (a check's log, a session's terminal); any other is opened elsewhere. */
const linkServes = (r: string) => isCheck(r) || (isSession(r) && !!props.openTerminal)
</script>

<template>
  <div class="flex flex-col gap-1.5" data-inbox-evidence>
    <UButton
      :label="`Evidence (${evidence.length})`"
      :icon="open ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
      color="neutral"
      variant="ghost"
      size="sm"
      class="min-h-11 self-start"
      :aria-expanded="open"
      data-inbox-evidence-toggle
      @click="open = !open"
    />
    <ul v-if="open" class="flex flex-col gap-1" data-inbox-evidence-list>
      <li v-for="e in evidence" :key="e.ref" class="flex flex-col gap-1 rounded-md bg-elevated/40 px-2 py-1.5 text-sm" :data-evidence-ref="e.ref">
        <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
          <ULink v-if="routeOf(e.ref)" :to="routeOf(e.ref)!" class="inline-flex min-h-11 items-center text-primary break-all lg:min-h-8" data-evidence-link>{{ e.label }}</ULink>
          <span v-else class="break-all">{{ e.label }}</span>
          <UButton v-if="openTerminal && isSession(e.ref)" label="Open its terminal" icon="i-lucide-square-terminal" size="xs" color="neutral" variant="outline" class="min-h-11 lg:min-h-8" data-evidence-terminal @click="openTerminal(e.ref)" />
          <UButton v-if="isCheck(e.ref)" :label="logs[e.ref] ? 'Hide the log' : 'The log’s end'" icon="i-lucide-scroll-text" size="xs" color="neutral" variant="outline" class="min-h-11 lg:min-h-8" :loading="logs[e.ref]?.loading" data-evidence-log-toggle @click="toggleLog(e.ref)" />
        </div>
        <pre v-if="logs[e.ref]?.text !== undefined" class="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded bg-default p-2 font-mono text-xs" data-evidence-log>{{ logs[e.ref]!.text || '(the log is empty)' }}</pre>
        <p v-else-if="logs[e.ref]?.error" class="text-xs text-error" data-evidence-log-error>{{ logs[e.ref]!.error }}</p>
        <p v-if="read && !routeOf(e.ref) && !linkServes(e.ref)" class="text-xs text-muted" data-evidence-elsewhere>Open it on the desktop or the LAN.</p>
      </li>
    </ul>
  </div>
</template>
