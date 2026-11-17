<script setup lang="ts">
import type { DispatchTicket, OpBody, Publication, PublicationPreview, PublicationSummary, PublishOffer } from '~/composables/useDispatch'
import { ApiError } from '~/composables/useApi'
import { dispatchFailureWords } from '~/composables/useDispatchOps'
import { CommandRefused, mintCommandId, sendCommand } from '~/utils/tasks'
import {
  PUBLISH_AS_NEW_WARNING,
  bodyProblem,
  closeAs,
  closeWords,
  commentLink,
  composeBody,
  markerWords,
  normalBody,
  offeredParts,
  pickTarget,
  previewHolds,
  previewParts,
  publicationWords,
  targetIsIssue,
  targetLabel,
} from '~/utils/publication'

/**
 * Publish to GitHub (Program item IN1a, docs/plans/tracker-linking.md, PR-9): a person's own act, composed, previewed and sent once. The
 * target (the linked issue, or a linked pull request or issue: the offer's targets); the person's own outcome text, empty to start; the
 * offered parts (the delivery links, the delivered candidate's checks, the ticket's key and title) as checkboxes, each inserting the exact
 * Markdown the server offers and nothing else, all off; Close the issue as completed, offered only for an issue, off. Preview prepares the
 * publication and draws the server's preview, the exact bytes Send posts, as text, the hidden marker said in words; Publish names those
 * bytes by their SHA-256, checked here before Publish is offered, with one op id for every press of it, so a double click, a reply lost or
 * a reload sends one. Then the publication's state: "Published to acme/api#612 by @alice at 14:02", with the comment's link. From the
 * Publications list it also sends a failed one again (its preview read, never composed here) and publishes a text not confirmed, or
 * restored, as a new publication, saying that the first may still appear. Nothing is ever sent for a publication not confirmed or
 * restored. Each request is bounded by the server's own deadline, and says that GitHub is being read while it runs.
 */
const props = defineProps<{
  ticket: DispatchTicket
  /** The target picked to start with (a link's `link:<id>`), else the linked item. */
  target?: string
  /** A publication of the list: sent again (`resend`, a failed one) or published as new (`new`, one not confirmed or restored). */
  from?: { id: string; mode: 'resend' | 'new' } | null
}>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ changed: [publication: PublicationSummary] }>()

const api = useDispatchApi()
const identity = useIdentity()

const step = ref<'compose' | 'preview' | 'done'>('compose')
const offer = ref<PublishOffer | null>(null)
const offerError = ref('')
const target = ref('')
const text = ref('')
const ticked = ref<string[]>([])
const closeIssue = ref(false)
/** The publication prepared or read, with its preview: the bytes Send posts. */
const prepared = ref<PublicationPreview | null>(null)
/** The publication this composer prepared and has not sent: discarded when the person goes back or closes. */
const ownDraft = ref('')
/** The op id of this preview's Send: every press of Publish carries it, so the server answers a second from the first's receipt. */
const sendOp = ref('')
const result = ref<PublicationSummary | null>(null)
const busy = ref<'' | 'offer' | 'prepare' | 'send' | 'read'>('')
const error = ref('')
/** The text published as new comes from a publication not confirmed or restored: the warning says the first may still appear. */
const asNew = ref(false)
/** The target asked for is not among the offer's: why, in words; nothing is offered to prepare. */
const targetRefused = ref('')
/** Each opening is a new one: an answer to an earlier opening changes nothing in this one. */
let opening = 0

const parts = computed(() => offeredParts(offer.value))
const targets = computed(() => (offer.value?.targets ?? []).map((t) => ({ label: targetLabel(t.target, t.url), value: t.target, url: t.url })))
const targetUrl = computed(() => targets.value.find((t) => t.value === target.value)?.url ?? '')
/** Close is offered for an issue alone. */
const closeOffered = computed(() => !!target.value && targetIsIssue(target.value, targetUrl.value, props.ticket.externalSnapshot?.kind))
watch(closeOffered, (o) => {
  if (!o) closeIssue.value = false
})
const composed = computed(() => composeBody(text.value, parts.value, ticked.value))
const problem = computed(() => (targetRefused.value || !target.value ? 'Pick where it is published' : bodyProblem(composed.value.body)))

/** The preview's parts, when it is the bytes its digest names and the body is what was asked for; null when it is not, and nothing is offered to send. */
const checked = computed(() => {
  const p = prepared.value
  if (!p || !previewHolds(p.preview, p.postSha256, p.publication.marker)) return null
  const split = previewParts(p.preview, p.publication.marker)!
  if (split.body !== normalBody(p.publication.body)) return null
  if (ownDraft.value && split.body !== normalBody(composed.value.body)) return null
  return split
})
/** Why Publish is not offered for the preview shown ('' when it is): only a draft this composer made, or a failed one of this store. */
const sendBlocked = computed(() => {
  const p = prepared.value?.publication
  if (!p) return 'Nothing is prepared'
  if (p.restored) return 'Restored from an export: it is never sent from here. Check looks for it on GitHub, or publish its text as new.'
  if (p.state === 'unconfirmed') return 'Not confirmed: it is never sent again. Publish its text as new from the list.'
  if (p.state !== 'draft' && p.state !== 'failed') return `It is ${p.state}: there is nothing to send`
  if (!checked.value) return 'The preview is not the bytes its digest names, so it is not offered to send: close this and prepare it again.'
  return ''
})

watch(open, (o) => {
  if (o) void start()
  else void leave()
})

async function start() {
  const n = ++opening
  step.value = 'compose'
  offer.value = null
  offerError.value = ''
  text.value = ''
  ticked.value = []
  closeIssue.value = false
  prepared.value = null
  ownDraft.value = ''
  sendOp.value = ''
  result.value = null
  error.value = ''
  asNew.value = props.from?.mode === 'new'
  target.value = ''
  targetRefused.value = ''
  // A failed one sent again needs no offer: its preview is read, never composed here.
  if (props.from?.mode !== 'resend') {
    busy.value = 'offer'
    try {
      const o = await api.publishOffer(props.ticket.key)
      if (n !== opening) return
      offer.value = o
      // The item the person pressed, or none and why: never another item in its place.
      const picked = pickTarget(props.target, o.targets)
      target.value = picked.target
      targetRefused.value = picked.refused
    } catch (e) {
      if (n === opening) offerError.value = (e as Error).message
    } finally {
      if (n === opening) busy.value = ''
    }
  }
  if (n !== opening || !props.from) return
  busy.value = 'read'
  try {
    const p = await api.publication(props.from.id)
    if (n !== opening) return
    if (props.from.mode === 'resend') {
      prepared.value = p
      sendOp.value = mintCommandId()
      step.value = 'preview'
    } else {
      // Its text as the person approved it, the parts already in it; the target as before when it is still offered.
      text.value = p.publication.body
      if (offer.value?.targets.some((t) => t.target === p.publication.target)) target.value = p.publication.target
    }
  } catch (e) {
    if (n === opening) error.value = (e as Error).message
  } finally {
    if (n === opening) busy.value = ''
  }
}

function op(): OpBody {
  const body: OpBody = { opId: mintCommandId() }
  const name = identity.name.value.trim()
  if (name) body.byName = name
  return body
}

/** Discards the draft this composer prepared and did not send, as the person leaves it: one left behind is in the list, with Discard. */
async function dropDraft() {
  const id = ownDraft.value
  const rev = prepared.value?.publication.rev
  ownDraft.value = ''
  if (!id) return
  try {
    const r = await api.discardPublication(id, { ...op(), ...(rev ? { expectedRev: rev } : {}) })
    if (r.publication) emit('changed', r.publication)
  } catch {
    // Left a draft: the list offers Discard.
  }
}

async function leave() {
  opening++
  // A send on its way is never raced by a discard: its answer settles the publication, which the list shows.
  if (step.value === 'preview' && busy.value !== 'send') await dropDraft()
}

async function preview() {
  if (problem.value || busy.value) return
  const n = opening
  busy.value = 'prepare'
  error.value = ''
  const { body, parts: names } = composed.value
  try {
    const reply = await sendCommand(
      (b: OpBody) =>
        api.preparePublication(props.ticket.key, {
          ...b,
          target: target.value,
          body,
          ...(names.length ? { parts: names } : {}),
          ...(closeIssue.value && closeOffered.value ? { closeIssue: 'completed' as const } : {}),
        }),
      { ...op(), expectedRev: props.ticket.rev },
    )
    if (n !== opening) return
    prepared.value = reply
    ownDraft.value = reply.publication.id
    sendOp.value = mintCommandId()
    step.value = 'preview'
    emit('changed', reply.publication)
  } catch (e) {
    if (n === opening) error.value = failureWords(e)
  } finally {
    if (n === opening) busy.value = ''
  }
}

async function back() {
  await dropDraft()
  prepared.value = null
  sendOp.value = ''
  error.value = ''
  step.value = 'compose'
}

async function publish() {
  const p = prepared.value
  if (!p || sendBlocked.value || busy.value) return
  const n = opening
  busy.value = 'send'
  error.value = ''
  const body: OpBody & { postSha256: string } = { opId: sendOp.value, expectedRev: p.publication.rev, postSha256: p.postSha256 }
  const name = identity.name.value.trim()
  if (name) body.byName = name
  try {
    const reply = await sendCommand((b: typeof body) => api.sendPublication(p.publication.id, b), body)
    if (reply.publication) emit('changed', reply.publication)
    if (n !== opening) return
    ownDraft.value = ''
    result.value = reply.publication
    step.value = 'done'
  } catch (e) {
    if (n !== opening) return
    error.value = failureWords(e)
    // A refusal is kept with its receipt: the publication is read again, and a send still possible gets an op id of its own.
    if (e instanceof CommandRefused) await reread(p.publication.id, n)
  } finally {
    if (n === opening) busy.value = ''
  }
}

async function reread(id: string, n: number) {
  try {
    const r = await api.publication(id)
    if (n !== opening) return
    prepared.value = r
    sendOp.value = mintCommandId()
    emit('changed', r.publication)
    if (r.publication.state !== 'draft' && r.publication.state !== 'failed') {
      ownDraft.value = ''
      result.value = r.publication
      step.value = 'done'
    }
  } catch {
    // The words say what was refused; the list reads it again.
  }
}

/** A failure in words: the server's, and for a send it was already making, that the same press is answered once it ends. */
function failureWords(e: unknown): string {
  if (e instanceof ApiError && e.status === 503 && e.code === 'tracker_busy') return `${e.message} The same Publish is answered as the first: nothing is posted twice.`
  if (e instanceof ApiError && e.status === 0) return 'The server did not answer. Press it again: the same Publish is answered as the first, and nothing is posted twice.'
  return dispatchFailureWords(e)
}

const now = () => Date.now()
const shown = computed(() => (result.value ? publicationWords(result.value, now()) : null))
const shownClose = computed(() => (result.value ? closeWords(result.value) : null))
const shownLink = computed(() => (result.value ? commentLink(result.value) : ''))
const tones = { error: 'text-error', muted: 'text-muted', default: 'text-default' }
const reading = computed(() => (busy.value === 'prepare' ? 'Reading GitHub: the target is read before the preview is made…' : busy.value === 'send' ? 'Posting to GitHub…' : ''))
</script>

<template>
  <UModal v-model:open="open" title="Publish to GitHub" :description="`From ${ticket.key}: one comment, posted as the account the GitHub token belongs to, once you press Publish.`" :ui="{ content: 'max-w-2xl' }">
    <template #body>
      <div class="flex min-w-0 flex-col gap-4 text-sm" data-dispatch-publish :data-publish-step="step">
        <p v-if="busy === 'offer' || busy === 'read'" class="text-dimmed" data-publish-reading>Reading what can be published…</p>
        <UAlert v-if="offerError" color="error" variant="subtle" icon="i-lucide-circle-alert" title="What can be published could not be read" :description="offerError" data-publish-offer-error />

        <template v-if="step === 'compose' && offer">
          <p v-if="targetRefused" class="text-muted" data-publish-target-refused>{{ targetRefused }}</p>
          <p v-else-if="!targets.length" class="text-muted" data-publish-no-target>This ticket has no linked GitHub item and no link to one: link one to publish.</p>
          <template v-if="!targetRefused && targets.length">
            <p v-if="asNew" class="text-muted" data-publish-as-new>{{ PUBLISH_AS_NEW_WARNING }}</p>
            <UFormField label="Published to" size="sm">
              <USelect v-model="target" :items="targets" class="w-full" aria-label="Where it is published" data-publish-target />
            </UFormField>
            <UFormField label="The outcome, in your words" hint="Markdown, as GitHub draws it" size="sm">
              <UTextarea v-model="text" :rows="5" autoresize class="w-full" placeholder="What came of it…" aria-label="The outcome, in your words" data-publish-body />
            </UFormField>
            <div v-if="parts.length" class="flex flex-col gap-2">
              <span class="text-xs font-medium text-muted">Add to it <span class="text-dimmed">(each as it is below, after your text)</span></span>
              <div v-for="p in parts" :key="p.name" class="flex min-w-0 flex-col gap-1">
                <UCheckbox
                  :model-value="ticked.includes(p.name)"
                  :label="p.label"
                  :data-publish-part="p.name"
                  @update:model-value="(v) => (ticked = v === true ? [...new Set([...ticked, p.name])] : ticked.filter((x) => x !== p.name))"
                />
                <pre class="ml-6 max-h-32 overflow-auto rounded-md bg-elevated px-2 py-1 font-mono text-xs whitespace-pre-wrap break-words text-muted" :data-publish-part-text="p.name">{{ p.markdown }}</pre>
              </div>
            </div>
            <UCheckbox v-if="closeOffered" v-model="closeIssue" label="Close the issue as completed" description="After the comment is posted. Off unless you tick it." data-publish-close />
          </template>
        </template>

        <template v-else-if="step === 'preview' && prepared">
          <p class="text-muted">This is what GitHub gets, byte for byte, as a comment on <span class="text-default">{{ prepared.publication.targetRef }}</span>:</p>
          <pre class="max-h-80 overflow-auto rounded-md bg-elevated px-3 py-2 font-mono text-xs whitespace-pre-wrap break-words text-default" data-publish-preview>{{ prepared.preview }}</pre>
          <p class="text-xs text-muted" data-publish-marker>{{ markerWords(prepared.publication.marker) }}</p>
          <p class="text-xs text-dimmed">Its SHA-256, which Publish names: <span class="break-all font-mono" data-mono="id" data-publish-digest>{{ prepared.postSha256 }}</span></p>
          <p v-if="prepared.publication.closeIssue" class="text-xs text-muted" data-publish-closes>Then the issue is closed {{ closeAs(prepared.publication.closeIssue) }}.</p>
          <p v-if="sendBlocked" class="text-muted" data-publish-blocked>{{ sendBlocked }}</p>
        </template>

        <template v-else-if="step === 'done' && shown">
          <p :class="tones[shown.tone]" data-publish-result :data-publication-state="result!.state">{{ shown.text }}</p>
          <p v-if="shownClose?.text" class="text-xs" :class="tones[shownClose.tone]">{{ shownClose.text }}</p>
          <a v-if="shownLink" :href="shownLink" target="_blank" rel="noopener noreferrer" class="text-xs text-primary hover:underline" data-publish-comment-link>The comment on GitHub</a>
        </template>

        <p v-if="reading" class="text-dimmed" data-publish-busy>{{ reading }}</p>
        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-circle-alert" :title="step === 'preview' ? 'Not published' : 'Not prepared'" :description="error" data-publish-error />

        <div class="flex flex-wrap items-center justify-end gap-2">
          <template v-if="step === 'compose'">
            <span v-if="offer && targets.length && problem" class="mr-auto text-xs text-muted" data-publish-problem>{{ problem }}</span>
            <UButton label="Cancel" color="neutral" variant="ghost" :disabled="busy === 'prepare'" @click="open = false" />
            <UButton label="Preview" icon="i-lucide-eye" :disabled="!offer || !targets.length || !!problem" :loading="busy === 'prepare'" data-publish-prepare @click="preview" />
          </template>
          <template v-else-if="step === 'preview'">
            <UButton v-if="ownDraft" label="Change it" color="neutral" variant="ghost" :disabled="!!busy" data-publish-back @click="back" />
            <UButton v-else label="Cancel" color="neutral" variant="ghost" :disabled="busy === 'send'" @click="open = false" />
            <UButton v-if="!sendBlocked" label="Publish" icon="i-lucide-send" :loading="busy === 'send'" data-publish-send @click="publish" />
          </template>
          <template v-else>
            <UButton label="Done" data-publish-done @click="open = false" />
          </template>
        </div>
      </div>
    </template>
  </UModal>
</template>
