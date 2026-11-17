<script setup lang="ts">
import type { CaptureDocumentView, CaptureView } from '~/composables/useCaptureSends'
import type { TicketAttachment } from '~/composables/useDispatch'
import { captureOfWords, diffSinceSent } from '~/utils/capture'
import { type CaptureSendView, sendState } from '~/utils/captureSend'
import { relativeTime } from '~/utils/sessions'

/**
 * A ticket's Captures (Program item PV1, "On the ticket"): the captures sent to the ticket, and only those (a capture, a retake too,
 * reaches a ticket by a send), each as it was sent: its page and revision label, its screenshot (the attachment the send made), its
 * annotations as the send froze them, and each send's receipt; Open shows the capture as it is now and what changed since. Read when
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
  <section class="flex flex-col gap-3" aria-labelledby="ticket-captures-title" data-ticket-captures>
    <h2 id="ticket-captures-title" class="text-sm font-semibold">Captures</h2>
    <p v-if="loading" class="text-sm text-muted">Reading the captures sent here…</p>
    <p v-else-if="error" class="text-sm text-muted" role="status" data-ticket-captures-error>{{ error }}</p>
    <p v-else-if="!captures.length" class="text-sm text-muted" data-ticket-captures-none>No capture was sent to this ticket.</p>
    <article v-for="c in captures" :key="c.id" class="flex flex-col gap-3 rounded-lg p-3 ring ring-default" :data-ticket-capture="c.id">
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
        <p v-if="opened.has(c.id)" class="text-xs text-muted" data-ticket-capture-now>
          Now r{{ opened.get(c.id)!.rev }}: {{ changes(c.id, c.sends[0]!.document).join(', ') }}
        </p>
      </div>
    </article>
  </section>
</template>
