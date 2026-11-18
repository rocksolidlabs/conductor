<script setup lang="ts">
import type { CaptureDocumentView, CaptureView } from '~/composables/useCaptureSends'
import type { TicketAttachment } from '~/composables/useDispatch'
import { captureOfWords, diffSinceSent, hitSentence } from '~/utils/capture'
import { type CaptureSendView, sendState } from '~/utils/captureSend'
import { relativeTime } from '~/utils/sessions'
import { retakeCandidate, type RetakeCandidate, type RetakeRun } from '~/utils/captureReview'
import type { Comparison } from '~/utils/compare'
import { entryRunId } from '~/utils/dispatchEvidence'
import { previewPath } from '~/utils/capture'
import { checkoutOfAttempt, previewRefusal } from '~/composables/useCandidatePreview'
import { mintCommandId } from '~/utils/tasks'

/**
 * A ticket's Captures (Program item PV1, "On the ticket"): the captures sent to the ticket, and only those (a capture, a retake too,
 * reaches a ticket by a send), each as it was sent: its page and revision label, its screenshot (the attachment the send made), its
 * annotations as the send froze them, each element's hit test in words (what a click at its centre reaches: the element itself or what
 * covers it, which tells a capture before a repair from one after it when their screenshots are the same), and each send's receipt; Open shows the capture as it is now and what changed since. Read when
 * the page is, and again when Dispatch says a capture or a send changed.
 */
const props = defineProps<{
  ticketKey: string
  attachments: readonly TicketAttachment[]
  /** An attachment's content as an image's object URL (the page's useDispatchAttachments). */
  image: (id: string) => Promise<string | null>
  now: number
}>()

const api = useCaptureSends()
const dispatch = useDispatchApi()
const sessions = useSessions()
const { request } = useApi()
const router = useRouter()

/** A send of a capture to this ticket, with what it froze. */
interface SentHere {
  send: CaptureSendView
  document: CaptureDocumentView
  screenshot?: TicketAttachment
}
interface CaptureHere {
  id: string
  sends: SentHere[]
}

const captures = ref<CaptureHere[]>([])
const loading = ref(true)
const error = ref('')
const images = reactive(new Map<string, string | null>())
const opened = reactive(new Map<string, CaptureView>())

async function read() {
  try {
    const listed = await api.ticketCaptures(props.ticketKey)
    const out: CaptureHere[] = []
    for (const c of listed) {
      const whole = await api.capture(c.id)
      const here = (whole.sends ?? []).filter((s) => s.kind === 'ticket' && s.state === 'sent' && s.target === props.ticketKey)
      const sends: SentHere[] = []
      for (const s of here.sort((a, b) => b.createdAt.localeCompare(a.createdAt))) {
        const f = await api.frozen(c.id, s.id)
        const name = `capture-${c.id}-r${s.rev}.png`
        const screenshot = f.document.image.sha256 ? props.attachments.find((a) => !a.deletedAt && a.name === name && a.sha256 === f.document.image.sha256) : undefined
        sends.push({ send: f.send, document: f.document, screenshot })
        if (screenshot && !images.has(screenshot.id)) images.set(screenshot.id, await props.image(screenshot.id).catch(() => null))
      }
      if (sends.length) out.push({ id: c.id, sends })
    }
    captures.value = out
    error.value = ''
    await readCandidates()
  } catch (e) {
    error.value = (e as Error).message || 'The captures could not be read'
  } finally {
    loading.value = false
  }
}

// A send's events name no ticket: every capture's and send's change is read again (the page's own ticket events are its comments'
// and fields', which change nothing here).
useDispatchLive({ later: (e) => e.entity === 'capture' || e.entity === 'capture_send', read }, () => void read())
watch(
  () => props.ticketKey,
  () => void read(),
)

// Capture again on a later candidate (PR-8): the ticket's runs' candidates, newest run first, read with the captures; each capture of a
// task is offered the candidate retakeCandidate names (its own task's later attempt in its own run, or a later repair run's, whatever
// that run's crew and tasks are called, a comparison's selected candidate alone), its checkout opened (or the one open) and the preview page asked to capture it again (`&retake=`).
const runCandidates = ref<RetakeRun[]>([])
async function readCandidates() {
  try {
    const page = await dispatch.ticketRuns(props.ticketKey)
    const started = new Map<string, string | undefined>()
    for (const e of page.entries) {
      const run = entryRunId(e)
      if (run && !started.has(run)) started.set(run, e.run?.startedAt)
    }
    const out: RetakeRun[] = []
    for (const [run, startedAt] of started) {
      const c = await dispatch.runCandidates(run).catch(() => null)
      if (c?.managed) out.push({ run, startedAt, candidates: c.candidates, ...(await comparedSelection(run)) })
    }
    runCandidates.value = out
  } catch {
    runCandidates.value = []
  }
}
/**
 * For a run that says it is a comparison (its read's `compare` part), the task of the candidate its applied selection selected, null
 * while none is applied or None of these was answered; for a run of no comparison, nothing. A read that fails offers nothing from the
 * run (null), never a guess.
 */
async function comparedSelection(run: string): Promise<{ selected?: string | null }> {
  try {
    const r = await sessions.getRun(run)
    if (!r.compare) return {}
    const sel = (await request<Comparison>(`/api/runs/${encodeURIComponent(run)}/comparison`)).selection
    return { selected: (sel?.state === 'applied' && !sel.applied?.none && sel.applied?.selected?.task) || null }
  } catch {
    return { selected: null }
  }
}
/**
 * The captures as the ticket shows them: a capture and the captures here that retake it (by their sent documents' `retakeOf`, followed
 * to the first one here) side by side, the first one first and its retakes after it by when each was saved, so a person compares before
 * and after (the plan's "A candidate's preview"); each group where its newest capture is in the list, newest first.
 */
const groups = computed<CaptureHere[][]>(() => {
  const here = new Map(captures.value.map((c) => [c.id, c]))
  const firstOf = (c: CaptureHere) => {
    const seen = new Set([c.id])
    let at = c
    for (let of = at.sends[0]?.document.retakeOf; of && here.has(of) && !seen.has(of); of = at.sends[0]?.document.retakeOf) {
      seen.add(of)
      at = here.get(of)!
    }
    return at.id
  }
  const out = new Map<string, CaptureHere[]>()
  for (const c of captures.value) {
    const first = firstOf(c)
    out.set(first, [...(out.get(first) ?? []), c])
  }
  const saved = (c: CaptureHere) => c.sends[0]?.document.savedAt ?? ''
  return [...out].map(([first, g]) => g.sort((a, b) => (a.id === first ? -1 : b.id === first ? 1 : saved(a).localeCompare(saved(b)))))
})
/** In a group of more than one, the capture this one retakes, as its sent document names it. */
function retakes(c: CaptureHere): string {
  return c.sends[0]?.document.retakeOf ?? ''
}

function retakeOn(c: CaptureHere): RetakeCandidate | null {
  // Later than the capture: its own moment, the selection its screenshot shows.
  const d = c.sends[0]?.document
  return d ? retakeCandidate(d.origin, runCandidates.value, d.selectedAt) : null
}
const retaking = ref('')
const retakeRefused = reactive(new Map<string, string>())
async function captureAgain(c: CaptureHere) {
  const on = retakeOn(c)
  if (!on) return
  retaking.value = c.id
  retakeRefused.delete(c.id)
  const base = `/api/runs/${encodeURIComponent(on.run)}/tasks/${encodeURIComponent(on.task)}/attempts/${on.attempt}/preview`
  try {
    let seq = 0
    try {
      seq = (await request<{ seq: number }>(base, { method: 'POST', body: { commandId: mintCommandId() } })).seq
    } catch (e) {
      // Open already: the one open, as the task's read names it.
      if ((e as { code?: string }).code !== 'preview_open') throw e
      const read = await sessions.runTask(on.run, on.task)
      seq = checkoutOfAttempt(on.attempt, undefined, read.attempts)?.seq ?? 0
      if (!seq) throw e
    }
    await router.push(`${previewPath({ run: on.run, task: on.task, attempt: on.attempt, seq })}&retake=${encodeURIComponent(c.id)}`)
  } catch (e) {
    retakeRefused.set(c.id, previewRefusal(e))
  } finally {
    retaking.value = ''
  }
}

async function open(id: string) {
  if (opened.has(id)) {
    opened.delete(id)
    return
  }
  try {
    opened.set(id, await api.capture(id))
  } catch (e) {
    error.value = (e as Error).message
  }
}

function address(url: string): string {
  try {
    const u = new URL(url)
    return u.origin + u.pathname
  } catch {
    return url
  }
}

function changes(id: string, sent: CaptureDocumentView): string[] {
  const live = opened.get(id)
  if (!live) return []
  if (live.deletedAt) return ['deleted since']
  const e = diffSinceSent({ rev: live.rev, annotations: live.annotations ?? [] }, sent)
  return e.length ? e : ['nothing since']
}
</script>

<template>
  <section class="@container flex flex-col gap-3" aria-labelledby="ticket-captures-title" data-ticket-captures>
    <h2 id="ticket-captures-title" class="text-sm font-semibold">Captures</h2>
    <p v-if="loading" class="text-sm text-muted">Reading the captures sent here…</p>
    <p v-else-if="error" class="text-sm text-muted" role="status" data-ticket-captures-error>{{ error }}</p>
    <p v-else-if="!captures.length" class="text-sm text-muted" data-ticket-captures-none>No capture was sent to this ticket.</p>
    <div
      v-for="g in groups"
      :key="g[0]!.id"
      :class="g.length > 1 ? 'grid grid-cols-1 gap-3 @xl:grid-cols-2' : 'flex flex-col'"
      :data-ticket-capture-pair="g.length > 1 ? g[0]!.id : undefined"
    >
      <article v-for="c in g" :key="c.id" class="flex min-w-0 flex-col gap-3 rounded-lg p-3 ring ring-default" :data-ticket-capture="c.id">
        <p v-if="g.length > 1" class="text-xs font-medium text-highlighted" :data-ticket-capture-role="c.id === g[0]!.id ? 'first' : 'retake'">
          <template v-if="c.id === g[0]!.id">Before: the capture the others retake</template>
          <template v-else>After: captured again from <span class="font-mono" data-mono="id">{{ retakes(c).slice(0, 8) }}</span></template>
        </p>
        <div v-for="s in c.sends" :key="s.send.id" class="flex flex-col gap-2" :data-ticket-capture-send="s.send.id" :data-send-rev="s.send.rev">
          <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-sm">
            <span class="min-w-0 truncate font-medium">{{ s.document.title || address(s.document.url) }}</span>
            <span class="font-mono text-muted" data-mono="path">{{ address(s.document.url) }}</span>
          </div>
          <p class="text-xs text-muted" data-ticket-capture-label>
            r{{ s.document.rev }}
            · {{ captureOfWords(s.document.origin).lead }}
            <span v-if="captureOfWords(s.document.origin).commit" class="font-mono" data-mono="commit">{{ captureOfWords(s.document.origin).commit }}</span>
            {{ captureOfWords(s.document.origin).tail }}
            · {{ s.document.viewport.mode }} {{ s.document.viewport.width }} by {{ s.document.viewport.height }}
          </p>
          <img
            v-if="s.screenshot && images.get(s.screenshot.id)"
            :src="images.get(s.screenshot.id)!"
            :alt="`The screenshot of capture r${s.send.rev}`"
            class="max-h-80 w-fit max-w-full rounded ring ring-default"
            data-ticket-capture-shot
          />
          <p v-else-if="s.document.image.how === 'none'" class="text-xs text-muted" data-ticket-capture-noshot>No screenshot{{ s.document.image.why ? `: ${s.document.image.why}` : '' }}</p>
          <ol class="flex flex-col gap-1.5 text-sm" data-ticket-capture-annotations>
            <li v-for="a in s.document.annotations" :key="a.k" class="flex flex-col gap-0.5" :data-ticket-capture-annotation="a.k">
              <span class="text-xs text-muted">
                {{ a.k }} · {{ a.kind }}<template v-if="a.element"> · <code>{{ a.element.selector || a.element.tag }}</code></template>
                <template v-if="a.sources?.length"> · <span class="font-mono" data-mono="path">{{ a.sources[0]!.path }}</span></template>
              </span>
              <span v-if="a.element?.hit" :class="['text-xs', a.element.hit.self ? 'text-muted' : 'text-default']" data-ticket-capture-hit :data-hit-self="a.element.hit.self ? '' : undefined">{{
                hitSentence(a.element)
              }}</span>
              <span v-if="a.note" class="whitespace-pre-wrap break-words">{{ a.note }}</span>
            </li>
          </ol>
          <div class="flex flex-wrap items-center gap-2 text-xs">
            <StatusMark :status="sendState(s.send).mark" word />
            <span class="text-muted">by {{ s.send.createdBy || 'admin' }} {{ relativeTime(s.send.createdAt, now, { suffix: true }) }}</span>
          </div>
        </div>
        <div class="flex flex-col gap-1">
          <UButton :label="opened.has(c.id) ? 'Close' : 'Open'" icon="i-lucide-scan-eye" size="xs" color="neutral" variant="ghost" class="w-fit" data-ticket-capture-open @click="open(c.id)" />
          <UButton
            v-if="retakeOn(c)"
            :label="`Capture again on ${retakeOn(c)!.task}#${retakeOn(c)!.attempt}`"
            icon="i-lucide-camera"
            size="xs"
            color="neutral"
            variant="ghost"
            class="w-fit"
            :loading="retaking === c.id"
            :data-ticket-capture-again="c.id"
            @click="captureAgain(c)"
          />
          <p v-if="retakeRefused.get(c.id)" class="text-xs text-muted" data-ticket-capture-again-refused>{{ retakeRefused.get(c.id) }}</p>
          <p v-if="opened.has(c.id)" class="text-xs text-muted" data-ticket-capture-now>
            Now r{{ opened.get(c.id)!.rev }}: {{ changes(c.id, c.sends[0]!.document).join(', ') }}
          </p>
        </div>
      </article>
    </div>
  </section>
</template>
