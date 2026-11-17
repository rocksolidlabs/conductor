<script setup lang="ts">
import type { OpBody, PublicationSummary } from '~/composables/useDispatch'
import { dispatchFailureWords } from '~/composables/useDispatchOps'
import { mintCommandId, sendCommand } from '~/utils/tasks'
import { relativeTime } from '~/utils/sessions'
import { closeWords, commentLink, needYouWords, publicationActions, publicationWords, type PublicationAction } from '~/utils/publication'

/**
 * A ticket's publications to GitHub (Program item IN1a, docs/plans/tracker-linking.md, PR-9): the newest ten from the ticket's read, older
 * ones a page at a time, each with its state in words ("Published to acme/api#612 by @alice at 14:02" and the comment's link; "not
 * confirmed: GitHub has not shown this comment; it may still appear"), its issue's close, and what a person may do with it: Check (one
 * being sent or not confirmed, or a restored one), Send again (a failed one of this store only, its preview read and shown first), Close
 * now (a sent one whose close did not finish), Publish as new (one not confirmed or restored, with its warning) and Discard. When older
 * publications need a person it says how many and lists them, with the same actions. A publication that needs a person is said in words,
 * the error tone for a send that failed and neutral ink otherwise: it is not among the one list of what needs you, so never amber.
 */
const props = defineProps<{
  ticketKey: string
  /** The newest publications, from the ticket's read (at most ten). */
  publications: readonly PublicationSummary[]
  /** How many older ones a person can act on (the ticket's `publicationsNeedYou`). */
  needYou: number
  locked: boolean
  now: number
  /** Whether the ticket has somewhere on GitHub to publish to (a linked item or a link to one): Publish to GitHub is offered then. */
  canPublish: boolean
}>()
const emit = defineEmits<{
  /** Open the composer: new, or from a publication (sent again, or its text published as new). */
  publish: [from: { id: string; mode: 'resend' | 'new' } | null]
  changed: []
}>()

const api = useDispatchApi()
const identity = useIdentity()

/** What an action answered, by id: shown over the read's row until a read of a later revision comes. */
const answered = ref(new Map<string, PublicationSummary>())
const newer = (p: PublicationSummary) => {
  const a = answered.value.get(p.id)
  return a && a.rev > p.rev ? a : p
}
const older = ref<PublicationSummary[]>([])
const olderNext = ref('')
const olderRead = ref(false)
const needing = ref<PublicationSummary[] | null>(null)
const reading = ref<'' | 'older' | 'needing'>('')
const readError = ref('')

const newest = computed(() => props.publications.map(newer))
const shownOlder = computed(() => older.value.map(newer))
const shownNeeding = computed(() => (needing.value ?? []).filter((p) => !older.value.some((o) => o.id === p.id)).map(newer))
/** The rows in the order shown: the newest, the older pages read, then the older ones that need a person when asked for. */
const groups = computed(() => [
  { key: 'newest', rows: newest.value },
  { key: 'older', rows: shownOlder.value },
  { key: 'needing', rows: shownNeeding.value },
])
/** The cursor after the ten: the last of them. */
const afterNewest = computed(() => props.publications[props.publications.length - 1]?.id ?? '')
const moreOlder = computed(() => (olderRead.value ? !!olderNext.value : props.publications.length >= 10))

// A new read of the ticket keeps the older pages and the list of those that need you only while they are still its own.
watch(
  () => props.ticketKey,
  () => {
    older.value = []
    olderNext.value = ''
    olderRead.value = false
    needing.value = null
    answered.value = new Map()
  },
)

async function readOlder() {
  const cursor = olderRead.value ? olderNext.value : afterNewest.value
  if (!cursor || reading.value) return
  reading.value = 'older'
  readError.value = ''
  try {
    const page = await api.publications(props.ticketKey, cursor)
    older.value = [...older.value, ...page.publications.filter((p) => !older.value.some((o) => o.id === p.id))]
    olderNext.value = page.next
    olderRead.value = true
  } catch (e) {
    readError.value = (e as Error).message
  } finally {
    reading.value = ''
  }
}

async function readNeeding() {
  if (!afterNewest.value || reading.value) return
  reading.value = 'needing'
  readError.value = ''
  try {
    needing.value = (await api.publications(props.ticketKey, afterNewest.value, true)).publications
  } catch (e) {
    readError.value = (e as Error).message
  } finally {
    reading.value = ''
  }
}

const acting = ref(new Map<string, PublicationAction>())
const errors = ref(new Map<string, string>())

function op(rev?: number): OpBody {
  const body: OpBody = { opId: mintCommandId() }
  if (rev) body.expectedRev = rev
  const name = identity.name.value.trim()
  if (name) body.byName = name
  return body
}

async function act(p: PublicationSummary, a: PublicationAction) {
  if (a === 'send-again' || a === 'new') {
    emit('publish', { id: p.id, mode: a === 'send-again' ? 'resend' : 'new' })
    return
  }
  if (acting.value.has(p.id)) return
  acting.value = new Map(acting.value).set(p.id, a)
  const errs = new Map(errors.value)
  errs.delete(p.id)
  errors.value = errs
  try {
    let got: PublicationSummary | null = null
    if (a === 'check') got = (await api.checkPublication(p.id)).publication
    else if (a === 'close-now') {
      // Its answer carries no receipt: a reply lost to the network is asked for once more with the same op id, as sendCommand does.
      const body = op(p.rev)
      got = (await api.closePublication(p.id, body).catch((e) => ((e as { status?: number }).status === 0 ? api.closePublication(p.id, body) : Promise.reject(e)))).publication
    }
    else got = (await sendCommand((b: OpBody) => api.discardPublication(p.id, b), op(p.rev))).publication
    if (got) answered.value = new Map(answered.value).set(got.id, got)
    emit('changed')
  } catch (e) {
    errors.value = new Map(errors.value).set(p.id, dispatchFailureWords(e))
    emit('changed')
  } finally {
    const m = new Map(acting.value)
    m.delete(p.id)
    acting.value = m
  }
}

const ACTIONS: Record<PublicationAction, { label: string; icon: string; hook: string }> = {
  check: { label: 'Check', icon: 'i-lucide-search-check', hook: 'data-publication-check' },
  'send-again': { label: 'Send again', icon: 'i-lucide-send', hook: 'data-publication-send-again' },
  'close-now': { label: 'Close now', icon: 'i-lucide-circle-check', hook: 'data-publication-close-now' },
  new: { label: 'Publish as new', icon: 'i-lucide-copy-plus', hook: 'data-publication-new' },
  discard: { label: 'Discard', icon: 'i-lucide-trash-2', hook: 'data-publication-discard' },
}
const actingWords = (a: PublicationAction | undefined) => (a === 'check' ? 'Reading GitHub…' : a === 'close-now' ? 'Closing the issue on GitHub…' : a === 'discard' ? 'Discarding…' : '')
const tones = { error: 'text-error', muted: 'text-muted', default: 'text-default' }
const actionsOf = (p: PublicationSummary) => (props.locked ? [] : publicationActions(p))
</script>

<template>
  <section class="flex flex-col gap-2" data-publications :data-count="newest.length">
    <div class="flex items-center gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Publications</h3>
      <div class="flex-1" />
      <UButton v-if="canPublish && !locked" label="Publish to GitHub" icon="i-lucide-send" size="xs" color="neutral" variant="ghost" data-publish-open @click="emit('publish', null)" />
    </div>
    <p v-if="!newest.length && !needYou" class="text-sm text-dimmed" data-publications-empty>Nothing published from this ticket yet.</p>

    <template v-for="list in groups" :key="list.key">
      <UButton v-if="list.key === 'needing' && moreOlder" label="Show older publications" size="xs" color="neutral" variant="ghost" class="self-start" :loading="reading === 'older'" data-publications-older @click="readOlder" />
      <div v-if="list.key === 'needing' && needYou" class="flex items-center gap-2" data-publications-need-you :data-count="needYou">
        <p class="text-sm text-default">{{ needYouWords(needYou) }}</p>
        <UButton v-if="needing === null" label="Show them" size="xs" color="neutral" variant="ghost" :loading="reading === 'needing'" data-publications-need-you-open @click="readNeeding" />
      </div>
      <ul v-if="list.rows.length" class="flex flex-col gap-1.5" :data-publications-list="list.key">
        <li v-for="p in list.rows" :key="p.id" class="flex min-w-0 flex-col gap-1 rounded-md px-3 py-2 text-sm ring ring-default" :data-publication="p.id" :data-publication-state="p.state" :data-publication-restored="p.restored ? '' : undefined">
          <p class="min-w-0 break-words" :class="tones[publicationWords(p, now).tone]" data-publication-words>{{ publicationWords(p, now).text }}</p>
          <p v-if="closeWords(p).text" class="text-xs" :class="tones[closeWords(p).tone]" data-publication-close>{{ closeWords(p).text }}</p>
          <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-dimmed">
            <a v-if="commentLink(p)" :href="commentLink(p)" target="_blank" rel="noopener noreferrer" class="text-primary hover:underline" data-publication-link>The comment on GitHub</a>
            <span>prepared by {{ p.createdBy || 'admin' }} {{ relativeTime(p.createdAt, now, { suffix: true }) }}</span>
            <span v-if="acting.get(p.id)" data-publication-busy>{{ actingWords(acting.get(p.id)) }}</span>
          </div>
          <p v-if="errors.get(p.id)" class="text-xs text-error" data-publication-error>{{ errors.get(p.id) }}</p>
          <div v-if="actionsOf(p).length" class="flex flex-wrap gap-1">
            <UButton
              v-for="a in actionsOf(p)"
              :key="a"
              :label="ACTIONS[a].label"
              :icon="ACTIONS[a].icon"
              size="xs"
              color="neutral"
              variant="outline"
              :loading="acting.get(p.id) === a"
              :disabled="acting.has(p.id)"
              v-bind="{ [ACTIONS[a].hook]: '' }"
              @click="act(p, a)"
            />
          </div>
        </li>
      </ul>
    </template>
    <p v-if="readError" class="text-xs text-error" data-publications-read-error>{{ readError }}</p>
  </section>
</template>
