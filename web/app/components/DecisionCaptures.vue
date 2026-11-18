<script setup lang="ts">
import type { CaptureAnswer, CaptureDocument, CaptureSendAnswer } from '~/utils/capture'
import { captureOfWords, diffSinceSent, elementSummary, hitWords } from '~/utils/capture'
import type { DecisionEntry } from '~/utils/decisions'
import { type CaptureReader, REFUSED_WORDS, asksScreenshot, captureReads, cardSends, historyCaptureRefs, capturePageAddress, captureRefWords, captureRowShot, captureRowWords, captureShotWords } from '~/utils/decisionCaptures'
import { relativeTime } from '~/utils/sessions'

/**
 * The captures sent to a decision, on its card (PV1 PR-11; utils/decisionCaptures.ts says who reads what). For the operator: each
 * capture as it was sent, its screenshot small, the element's name and hit test, its note, who sent it and when, and Open, which shows
 * the capture whole (the screenshot at its size, every annotation, and what changed since it was sent). For a reader every capture
 * route refuses (a decision link's holder): the references the decision's history already gives, as words, and no request at all.
 */
const props = defineProps<{ decisionId: string; rev: number; history?: readonly DecisionEntry[]; reader: CaptureReader; now: number }>()

const reads = computed(() => captureReads(props.reader))
const sends = useCaptureSends()
const captures = useCaptures()
const { request } = useApi()

/** The operator's sends, as last read; null before a read answers. */
const listed = ref<{ shown: CaptureSendAnswer[]; more: number } | null>(null)
/** The operator's read of the sends was refused or failed: the history's references stand in. */
const unread = ref(false)
/** Each send's frozen document, read once: it never changes. */
const docs = reactive(new Map<string, CaptureDocument>())
/** The sends whose frozen document could not be read. */
const docFailed = reactive(new Set<string>())
/** Each capture's screenshot read: its object URL, or failed. The screenshot is the capture's, the same at every revision. */
const shots = reactive(new Map<string, { src?: string; failed?: boolean }>())
/** The capture as it is now, read at Open. */
const live = reactive(new Map<string, CaptureAnswer | { failed: true }>())
const opened = ref('')

/** The references the decision's history gives: all a refused reader is drawn, and the operator's when its own read failed. */
const refs = computed(() => (props.reader === 'refused' || unread.value ? historyCaptureRefs(props.history) : []))

/** Each row the operator is drawn, with what has been read of it. */
const rows = computed(() =>
  (listed.value?.shown ?? []).map((s) => {
    const doc = docs.get(s.id)
    const shot = captureRowShot(doc, docFailed.has(s.id) ? { failed: true } : shots.get(s.capture))
    return { s, doc, shot, words: doc ? captureRowWords(doc) : null, label: doc ? captureOfWords(doc.origin) : null }
  }),
)

let gen = 0
async function read() {
  if (!reads.value.sends) return
  const mine = ++gen
  let got: { shown: CaptureSendAnswer[]; more: number }
  try {
    const r = await request<{ sends?: CaptureSendAnswer[] }>(`/api/decisions/${encodeURIComponent(props.decisionId)}/captures`)
    if (mine !== gen) return
    got = cardSends(r.sends ?? [])
    listed.value = got
    unread.value = false
  } catch {
    if (mine !== gen) return
    listed.value = null
    unread.value = true
    return
  }
  for (const s of got.shown) {
    if (mine !== gen) return
    if (!docs.has(s.id) && reads.value.documents) {
      try {
        docs.set(s.id, (await sends.frozen(s.capture, s.id)).document)
        docFailed.delete(s.id)
      } catch {
        docFailed.add(s.id)
        continue
      }
    }
    if (reads.value.screenshots && asksScreenshot(docs.get(s.id)) && !shots.has(s.capture)) {
      shots.set(s.capture, {})
      try {
        const url = URL.createObjectURL(await captures.image(s.capture))
        if (gen < 0) URL.revokeObjectURL(url)
        else shots.set(s.capture, { src: url })
      } catch {
        shots.set(s.capture, { failed: true })
      }
    }
  }
}
watch(
  () => [props.decisionId, props.rev, props.reader],
  () => void read(),
  { immediate: true },
)
onBeforeUnmount(() => {
  gen = -1
  for (const s of shots.values()) if (s.src) URL.revokeObjectURL(s.src)
})

async function toggle(s: CaptureSendAnswer) {
  if (opened.value === s.id) {
    opened.value = ''
    return
  }
  opened.value = s.id
  if (!reads.value.capture) return
  try {
    live.set(s.capture, await sends.capture(s.capture))
  } catch {
    live.set(s.capture, { failed: true })
  }
}

/** What changed since the send, from the capture as it is now. */
function since(s: CaptureSendAnswer): string {
  const now = live.get(s.capture)
  const sent = docs.get(s.id)
  if (!now || !sent) return ''
  if ('failed' in now) return 'The capture as it is now could not be read.'
  if (now.deletedAt) return 'The capture was deleted since it was sent.'
  const e = diffSinceSent({ rev: now.rev, annotations: now.annotations ?? [] }, sent)
  return `Now r${now.rev}: ${e.length ? e.join(', ') : 'nothing changed since'}.`
}
</script>

<template>
  <div v-if="rows.length || refs.length" class="flex min-w-0 flex-col gap-1.5" data-decision-captures :data-decision-captures-reader="rows.length ? 'operator' : 'refused'">
    <span class="text-xs font-medium text-muted">Captures</span>

    <!-- The operator: each capture as it was sent. -->
    <ul v-if="rows.length" class="flex min-w-0 flex-col gap-2">
      <li v-for="r in rows" :key="r.s.id" class="flex min-w-0 flex-col gap-1 rounded-md p-2 ring ring-default" :data-decision-capture="r.s.id" :data-decision-capture-of="r.s.capture" :data-decision-capture-shot="r.shot.kind">
        <div class="flex min-w-0 gap-3">
          <img
            v-if="r.shot.kind === 'shown'"
            :src="r.shot.src"
            :alt="`The screenshot of capture ${r.s.capture} as sent`"
            class="h-20 w-32 flex-none rounded bg-elevated object-contain ring ring-default"
            data-decision-capture-thumb
          />
          <p v-else class="flex h-20 w-32 flex-none items-center overflow-hidden rounded bg-elevated/40 p-1.5 text-[11px] leading-snug text-muted ring ring-default" data-decision-capture-noshot>{{ captureShotWords(r.shot) }}</p>
          <div class="flex min-w-0 flex-1 flex-col gap-0.5 text-xs">
            <template v-if="r.words">
              <span class="min-w-0 break-words font-medium text-default" data-decision-capture-page>{{ r.words.page }}</span>
              <span v-if="r.words.element" class="min-w-0 break-words text-default" data-decision-capture-element>{{ r.words.element }}</span>
              <span v-if="r.words.hit" class="min-w-0 break-words text-muted" data-decision-capture-hit>{{ r.words.hit }}</span>
              <span v-if="r.words.note" class="min-w-0 whitespace-pre-wrap break-words text-default" data-decision-capture-note>{{ r.words.note }}</span>
              <span v-if="r.words.others" class="text-muted">and {{ r.words.others }} more {{ r.words.others === 1 ? 'annotation' : 'annotations' }}</span>
            </template>
            <span class="min-w-0 break-words text-muted">
              <span class="break-all font-mono" data-mono="id">capture:{{ r.s.capture }}/{{ r.s.id }}</span>
              · r{{ r.s.rev }} · {{ r.s.createdBy || 'admin' }} {{ relativeTime(r.s.createdAt, now, { suffix: true }) }}
            </span>
          </div>
        </div>
        <UButton
          v-if="r.doc"
          :label="opened === r.s.id ? 'Close' : 'Open'"
          :icon="opened === r.s.id ? 'i-lucide-chevron-down' : 'i-lucide-scan-eye'"
          size="sm"
          color="neutral"
          variant="ghost"
          class="min-h-11 self-start"
          :aria-expanded="opened === r.s.id"
          data-decision-capture-open
          @click="toggle(r.s)"
        />
        <!-- The capture whole, as it was sent: the screenshot at its size, its page and revision, every annotation; then what changed. -->
        <div v-if="opened === r.s.id && r.doc && r.label" class="flex min-w-0 flex-col gap-2 text-xs" data-decision-capture-view>
          <img v-if="r.shot.kind === 'shown'" :src="r.shot.src" :alt="`The screenshot of capture ${r.s.capture} at its size`" class="h-auto w-fit max-w-full rounded ring ring-default" data-decision-capture-full />
          <span class="min-w-0 break-all font-mono text-muted" data-mono="path">{{ capturePageAddress(r.doc.url) }}</span>
          <span class="min-w-0 break-words text-muted" data-decision-capture-label>
            r{{ r.doc.rev }} · {{ r.label.lead }}
            <span v-if="r.label.commit" class="font-mono" data-mono="commit">{{ r.label.commit }}</span>
            {{ r.label.tail }} · {{ r.doc.viewport.mode }} {{ r.doc.viewport.width }} by {{ r.doc.viewport.height }}
          </span>
          <ol class="flex min-w-0 flex-col gap-1.5" data-decision-capture-annotations>
            <li v-for="a in r.doc.annotations" :key="a.k" class="flex min-w-0 flex-col gap-0.5" :data-decision-capture-annotation="a.k">
              <span class="min-w-0 break-words text-muted">
                {{ a.k }} · {{ a.kind }}<template v-if="a.element"> · {{ elementSummary(a.element) }}</template>
                <template v-if="a.sources?.length"> · <span class="break-all font-mono" data-mono="path">{{ a.sources[0]!.path }}</span></template>
              </span>
              <span v-if="a.element && hitWords(a.element)" class="min-w-0 break-words text-muted">{{ hitWords(a.element) }}</span>
              <span v-if="a.note" class="min-w-0 whitespace-pre-wrap break-words text-default">{{ a.note }}</span>
            </li>
          </ol>
          <p v-if="since(r.s)" class="text-muted" data-decision-capture-now>{{ since(r.s) }}</p>
        </div>
      </li>
    </ul>
    <p v-if="listed?.more" class="text-xs text-muted" data-decision-captures-more>{{ listed.more }} more {{ listed.more === 1 ? 'capture was' : 'captures were' }} sent: the newest are shown.</p>

    <!-- A reader the capture routes refuse (and the operator whose read failed): the references the history gives, as words. -->
    <template v-if="!rows.length && refs.length">
      <ul class="flex min-w-0 flex-col gap-0.5 text-xs">
        <li v-for="x in refs" :key="x.send" class="flex min-w-0 flex-wrap gap-x-2" :data-decision-capture-ref="x.send">
          <span class="break-all font-mono" data-mono="id">capture:{{ x.capture }}/{{ x.send }}</span>
          <span class="text-muted">{{ captureRefWords(x) }} {{ relativeTime(x.at, now, { suffix: true }) }}</span>
        </li>
      </ul>
      <p v-if="reader === 'refused'" class="text-xs text-muted" data-decision-captures-refused>{{ REFUSED_WORDS }}</p>
    </template>
  </div>
</template>
