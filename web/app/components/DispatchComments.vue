<script setup lang="ts">
import type { CommentReply, DispatchTicket, TicketComment } from '~/composables/useDispatch'
import type { DispatchOrder } from '~/utils/dispatchOrder'
import { retried, through } from '~/utils/dispatchOrder'
import { COMMENT_PAGE, commentAuthor, commentGaps, commentsRereadFrom, earlierComments, shownComments, type CommentGap } from '~/utils/dispatch'
import { DISPATCH_RETRY_MAX_MS } from '~/composables/useDispatchLive'
import { RUN_EVENT_WINDOW_MS } from '~/utils/runs'
import { avatarTone } from '~/utils/avatar'
import { initials, relativeTime } from '~/utils/sessions'

/**
 * A ticket's discussion (docs/plans/dispatch.md, PR-10): its comments in their order, each with its number (`#3`, the store's, derived
 * from its place, so a deleted comment keeps its number and none moves), who wrote it and when, its body as Markdown with the ticket's
 * images; edit and delete (a tombstone) on a live ticket; the last 50 as the ticket's read carries them and the earlier ones a page at a
 * time; a new comment at the foot. Each is one operation (useDispatchOps); its answer is shown at once, the ticket's re-read on its
 * event (another browser's comment among them) bringing the rest. The earlier pages shown are read again whenever the page reads the
 * ticket again (`reads` moving), from the first comment shown below the read's window (an earlier page's, or one an answer kept that
 * the window moved past), so an old comment edited or deleted elsewhere is shown as it is now; a read of them that fails is asked
 * again as a re-read is. Comments missing among those shown (one this page made, kept below the read's window; earlier pages the window moved
 * past) are said where they are missing, and read from there. An edit is saved against the comment as its editor opened on it: when
 * another person's edit came meanwhile, Save sends nothing, keeps the draft and shows theirs, and a second Save replaces it, knowingly.
 * Once saved with the editor kept open (more written meanwhile), it is made against what it sent at the revision its receipt names,
 * never the comment the answer carries, which is read after the commit and can hold another person's later edit: that one is theirs.
 * The comment being edited stays shown while its editor is open, though the read's window moves past it (`held`), and is read again
 * with the earlier pages from then on. A comment an answer keeps below the read's window is read again with them at once, since the
 * page's reads that came while the answer was on its way had nothing there to read again.
 * A stream started again (`resets` moving: a store restored from a backup, whose `rev`s may be below those shown) forgets the answers
 * kept and those still on their way.
 */
const props = defineProps<{
  ticket: DispatchTicket
  gate: DispatchOrder
  locked: boolean
  /** Counts the page's reads of the ticket: each one reads the earlier pages shown again. */
  reads: number
  /** Counts the page's stream started again (a snapshot): each one forgets the comments the operations answered with. */
  resets: number
  attachmentUrl?: (id: string) => Promise<string | null>
  now: number
}>()

const api = useDispatchApi()
const ops = useDispatchOps(props.gate)
const toast = useToast()

/** The pages before the ticket's read's, as read. */
const earlier = ref<TicketComment[]>([])
/** The earlier pages were read before the stream started again: the read's comments are shown over theirs until they are read again. */
const earlierOutdated = ref(false)
/** The comments this page's operations answered with, by id: newer than a read sent before them and served after. */
const known = ref(new Map<string, TicketComment>())
/** Counts the stream's starts this page saw: an operation's answer asked before the last one is not kept. */
let generation = 0
const loadingEarlier = ref(false)
const confirming = ref('')

/**
 * The comment being edited: the revision and the body its editor opened on, or another person's once shown (`theirs`), or the one saved
 * while it stayed open, which is what a save is made against; `opened` counts the openings, so a save's answer acts on the editor only
 * while it is the opening the save was made in.
 */
interface Editing {
  id: string
  rev: number
  body: string
  theirs: string | null
  /** The text this editor saved last, when its receipt named no revision: no other person's, once read. */
  saved?: string
  opened: number
}
const editing = ref<Editing | null>(null)
let openings = 0
/** The comment being edited, as last shown: shown in its place while its editor is open, when the read's window has moved past it. */
const held = ref<TicketComment | null>(null)

const comments = computed(() =>
  shownComments(props.ticket.id, props.ticket.comments ?? [], earlier.value, known.value, {
    earlierOutdated: earlierOutdated.value,
    held: held.value && editing.value?.id === held.value.id ? held.value : undefined,
  }),
)
// The comment being edited is followed as it is shown, so the one held is the latest the page had.
watch(comments, (shown) => {
  const id = editing.value?.id
  const c = id ? shown.find((s) => s.id === id) : undefined
  if (!id) held.value = null
  else if (c && c !== held.value) held.value = c
})
const before = computed(() => earlierComments(comments.value[0]?.n))
/** The comments missing among those shown, by the id of the one each run comes before. */
const gaps = computed(() => new Map(commentGaps(comments.value, props.ticket.comments?.[0]?.n).map((g) => [g.before, g])))

// Another ticket on the same page: nothing of this one's stays.
watch(
  () => props.ticket.id,
  () => {
    earlier.value = []
    earlierOutdated.value = false
    known.value = new Map()
    editing.value = null
    held.value = null
    confirming.value = ''
  },
)

// The stream started again: the answers kept, and those on their way, are of the store before it.
watch(
  () => props.resets,
  () => {
    generation++
    known.value = new Map()
    if (earlier.value.length) earlierOutdated.value = true
  },
)

let gone = false
onBeforeUnmount(() => (gone = true))

/**
 * Reads again the earlier pages shown, those numbered `from` up to the first the ticket's read carries, 50 at a time, as one answer
 * through the page's gate, so a slower read never replaces a later one.
 */
async function readEarlier(from: number): Promise<void> {
  const key = props.ticket.key
  const id = props.ticket.id
  const to = (props.ticket.comments?.[0]?.n ?? Number.MAX_SAFE_INTEGER) - 1
  if (to < from) {
    // The read's window reaches back past them: the read holds them all, and none is earlier.
    earlier.value = []
    earlierOutdated.value = false
    return
  }
  await through(
    props.gate,
    `comments:${key}`,
    async () => {
      const all: TicketComment[] = []
      for (let after = from - 1; after < to; after += COMMENT_PAGE) {
        const page = await api.comments(key, after, Math.min(COMMENT_PAGE, to - after))
        all.push(...page)
        if (page.length < Math.min(COMMENT_PAGE, to - after)) break
      }
      return all
    },
    (all) => {
      if (props.ticket.id !== id) return
      earlier.value = all
      earlierOutdated.value = false
    },
  )
}

/** Reads the earlier pages from the comment numbered `from`, or from the first of those read when that is before it, as one answer. */
async function readFrom(from: number) {
  loadingEarlier.value = true
  try {
    await readEarlier(Math.min(from, earlier.value[0]?.n ?? from))
  } catch (e) {
    toast.add({ title: 'The earlier comments were not read', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    loadingEarlier.value = false
  }
}

/** The page before those shown: the earlier pages read again with it, from its first, as one answer. */
function more() {
  const page = earlierComments(comments.value[0]?.n)
  if (page) void readFrom(page.after + 1)
}

/** Comments missing among those shown: read with the earlier pages, from the first of them. */
function fill(g: CommentGap) {
  void readFrom(g.after + 1)
}

/** Where a re-read of the earlier pages starts: the first comment shown below the read's window; 0 when none is. */
function rereadFrom(): number {
  return commentsRereadFrom(comments.value, props.ticket.comments?.[0]?.n, earlier.value[0]?.n)
}

/** Counts the re-reads of the earlier pages: a later one takes the place of one still being asked again. */
let rereads = 0

/** Reads the earlier pages shown again, from the first of them, asked again until one answers or a later re-read takes its place. */
function rereadEarlier() {
  const mine = ++rereads
  const id = props.ticket.id
  void retried({
    read: () => readEarlier(rereadFrom()),
    stop: () => gone || mine !== rereads || props.ticket.id !== id || !rereadFrom(),
    wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
    firstMs: RUN_EVENT_WINDOW_MS,
    maxMs: DISPATCH_RETRY_MAX_MS,
  })
}

// The page read the ticket again: the earlier pages shown are read again too.
watch(() => props.reads, rereadEarlier)

/**
 * A comment an operation answered with: shown at once, at its revision or a later one; none asked before the stream started again. One
 * below the read's window is read again with the earlier pages at once: the reads that came while its answer was on its way (another
 * person's edit or delete of it among their events) had nothing below the window to read again, and it would stay as answered.
 */
function keep(c: TicketComment | null | undefined, asked: number) {
  if (!c || c.ticketId !== props.ticket.id || asked !== generation) return
  const had = known.value.get(c.id)
  if (had && had.rev > c.rev) return
  known.value = new Map(known.value).set(c.id, c)
  const readFirst = props.ticket.comments?.[0]?.n
  if (readFirst && c.n < readFirst) rereadEarlier()
}

async function add(body: string): Promise<boolean> {
  const key = props.ticket.key
  const asked = generation
  const reply = await ops.run<CommentReply>((op) => api.addComment(key, { ...op, body }), { thing: `comment:new:${key}`, what: key })
  keep(reply?.comment, asked)
  return !!reply
}

function edit(c: TicketComment) {
  editing.value = { id: c.id, rev: c.rev, body: c.body, theirs: null, opened: ++openings }
  held.value = c
}

/**
 * The comment holds another revision than the one its editor opened on: adopted as what the next save is made against, and, when its
 * text is another, shown (true), so that save sends nothing and a second one replaces it knowingly.
 */
function changedUnder(e: Editing, c: TicketComment): boolean {
  if (c.rev === e.rev) return false
  const theirs = c.body !== e.body && c.body !== e.saved
  e.rev = c.rev
  e.body = c.body
  if (theirs) e.theirs = c.body
  return theirs
}

/**
 * Saves an edit, made against the comment as its editor opened on it (`editing`), never against the one shown now: true once the store
 * has it. The editor closes itself once it is done and nothing more was written (its `sent`); written on meanwhile, the newer text stays,
 * and its save is made against the one saved.
 */
async function save(c: TicketComment, body: string): Promise<boolean> {
  const e = editing.value
  if (!e || e.id !== c.id) return false
  if (changedUnder(e, c)) return false
  if (body === c.body) return true
  const key = props.ticket.key
  const opened = e.opened
  const asked = generation
  const reply = await ops.run<CommentReply>((op) => api.editComment(key, c.id, { ...op, body }), { thing: `comment:${c.id}`, what: `${key} #${c.n}`, expectedRev: e.rev })
  keep(reply?.comment, asked)
  const now = editing.value
  if (reply && now?.opened === opened) {
    // What it sent, at the revision that commit made: a later revision shown since is another person's, said at the next Save.
    now.theirs = null
    if (reply.receipt.rev !== undefined) {
      now.rev = reply.receipt.rev
      now.body = body
      now.saved = undefined
    } else now.saved = body
  }
  return !!reply
}

/** The editor is done: what it sent is saved and nothing more was written. */
function sent(c: TicketComment) {
  if (editing.value?.id === c.id) editing.value = null
}

async function remove(c: TicketComment) {
  confirming.value = ''
  const key = props.ticket.key
  const asked = generation
  const reply = await ops.run<CommentReply>((op) => api.deleteComment(key, c.id, op), { thing: `comment:${c.id}`, what: `${key} #${c.n}`, expectedRev: c.rev })
  keep(reply?.comment, asked)
}

const composer = useTemplateRef<{ insert: (text: string) => void }>('composer')
defineExpose({ insert: (text: string) => composer.value?.insert(text) })
</script>

<template>
  <section class="flex min-w-0 flex-col gap-3" data-ticket-comments :data-count="comments.filter((c) => !c.deletedAt).length">
    <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Comments</h3>

    <UButton v-if="before" :label="`Earlier comments (${before.after + before.limit} more)`" icon="i-lucide-chevrons-up" color="neutral" variant="soft" size="xs" class="w-fit" :loading="loadingEarlier" data-comments-earlier @click="more" />
    <p v-if="!comments.length" class="text-sm text-dimmed" data-comments-none>No comments yet.</p>

    <ol class="flex flex-col gap-3">
      <template v-for="c in comments" :key="c.id">
        <li v-if="gaps.get(c.id)" class="flex items-center gap-2 text-xs text-dimmed" :data-comments-gap="gaps.get(c.id)!.limit">
          <span>{{ gaps.get(c.id)!.limit === 1 ? 'One comment' : `${gaps.get(c.id)!.limit} comments` }} not shown here</span>
          <UButton label="Show" icon="i-lucide-chevrons-up-down" color="neutral" variant="soft" size="xs" :loading="loadingEarlier" data-comments-gap-show @click="fill(gaps.get(c.id)!)" />
        </li>
        <li class="flex min-w-0 gap-3" :data-comment="c.n" :data-comment-id="c.id" :data-comment-rev="c.rev" :data-comment-deleted="c.deletedAt ? '' : undefined">
          <span class="mt-0.5 grid size-7 flex-none place-items-center rounded-full text-[10px] font-semibold" :class="c.deletedAt ? 'bg-elevated text-dimmed' : avatarTone(commentAuthor(c))">{{ initials(commentAuthor(c)) }}</span>
          <div class="flex min-w-0 flex-1 flex-col gap-1">
            <p class="flex flex-wrap items-baseline gap-x-2 text-xs text-muted">
              <span class="font-medium text-highlighted" data-comment-author>{{ commentAuthor(c) }}</span>
              <span class="font-mono text-dimmed" data-comment-n>#{{ c.n }}</span>
              <span :title="c.createdAt">{{ relativeTime(c.createdAt, now, { suffix: true }) }}</span>
              <span v-if="c.editedAt && !c.deletedAt" :title="c.editedAt" data-comment-edited>· edited</span>
              <span v-if="!locked && !c.deletedAt && editing?.id !== c.id" class="ml-auto flex gap-0.5">
                <UButton icon="i-lucide-pencil" size="xs" color="neutral" variant="ghost" :aria-label="`Edit comment #${c.n}`" data-comment-edit @click="edit(c)" />
                <UButton icon="i-lucide-trash-2" size="xs" color="neutral" variant="ghost" :aria-label="`Delete comment #${c.n}`" data-comment-delete @click="confirming = c.id" />
              </span>
            </p>
            <p v-if="c.deletedAt" class="text-sm italic text-dimmed">This comment was deleted.</p>
            <template v-else-if="editing?.id === c.id">
              <div v-if="editing.theirs !== null" class="flex flex-col gap-2 rounded-md p-3 ring ring-warning/40" data-comment-theirs>
                <p class="flex items-start gap-2 text-sm text-warning">
                  <UIcon name="i-lucide-triangle-alert" class="mt-0.5 size-4 flex-none" />
                  <span>Comment #{{ c.n }} was changed while you wrote. It now reads as below: Save again to replace it with yours, or Cancel to keep it.</span>
                </p>
                <div class="min-w-0 rounded-md ring ring-default" data-comment-theirs-text>
                  <MarkdownPreview :text="editing.theirs" dir="" :relative-links="false" :attachment-url="attachmentUrl" />
                </div>
              </div>
              <DispatchCommentEditor :initial="c.body" action="Save" cancellable :submit="(body) => save(c, body)" :attachment-url="attachmentUrl" @sent="sent(c)" @cancel="editing = null" />
            </template>
            <div v-else class="min-w-0 rounded-md ring ring-default" data-comment-body>
              <MarkdownPreview :text="c.body" dir="" :relative-links="false" :attachment-url="attachmentUrl" />
            </div>
            <div v-if="confirming === c.id" class="flex flex-wrap items-center gap-2 text-sm" data-comment-confirm>
              <span class="text-muted">Delete comment #{{ c.n }}? Its number stays, and the history keeps what it said.</span>
              <UButton label="Delete" color="error" variant="soft" size="xs" data-comment-delete-confirm @click="remove(c)" />
              <UButton label="Keep it" color="neutral" variant="ghost" size="xs" @click="confirming = ''" />
            </div>
          </div>
        </li>
      </template>
    </ol>

    <DispatchCommentEditor v-if="!locked" ref="composer" action="Comment" :submit="add" :attachment-url="attachmentUrl" />
  </section>
</template>
