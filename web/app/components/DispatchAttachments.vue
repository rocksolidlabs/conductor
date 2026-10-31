<script setup lang="ts">
import type { TicketAttachment } from '~/composables/useDispatch'
import { MAX_ATTACHMENTS, MAX_ATTACHMENT_SIZE, attachmentMarkdown, inlineImage, sizeWords, type UploadOutcome } from '~/utils/dispatch'
import { relativeTime } from '~/utils/sessions'

/**
 * A ticket's files (docs/plans/dispatch.md, PR-10; Owner decisions, 8): uploaded with their progress, by the button or dropped on the
 * section, one after another, each one operation; listed with their size and who attached them; an image shown inline through an
 * object URL of its content fetched with the token (useDispatchAttachments), never an `img` whose source is the API, and an SVG never
 * inline, since it can script; every file downloaded the same way, byte for byte; removed (a tombstone, its file kept until no live
 * attachment names its content). An upload the server swept before its row was written (`409 expired`) is offered again.
 */
const props = defineProps<{
  attachments: readonly TicketAttachment[]
  locked: boolean
  now: number
  /** Attaches one file, its progress said as it goes. */
  upload: (file: File, progress: (sent: number, total: number) => void) => Promise<UploadOutcome>
  remove: (a: TicketAttachment) => Promise<boolean>
  /** An image attachment's object URL (useDispatchAttachments.image); null for one that is no image shown. */
  image: (id: string) => Promise<string | null>
  download: (a: TicketAttachment) => Promise<void>
}>()
const emit = defineEmits<{ insert: [markdown: string] }>()

const toast = useToast()

interface Upload {
  n: number
  file: File
  sent: number
  state: 'waiting' | 'sending' | 'expired' | 'failed'
}
const uploads = ref<Upload[]>([])
let uploadN = 0
/** The uploads go one after another: each one's operation expects the revision the one before left. */
let queue: Promise<unknown> = Promise.resolve()

const live = computed(() => props.attachments.filter((a) => !a.deletedAt))
const full = computed(() => live.value.length + uploads.value.filter((u) => u.state !== 'failed' && u.state !== 'expired').length >= MAX_ATTACHMENTS)

/** Each file picked or dropped, queued; one past the bound said and not sent. */
function add(files: Iterable<File>) {
  for (const file of files) {
    if (file.size > MAX_ATTACHMENT_SIZE) {
      toast.add({ title: `${file.name}: not attached`, description: `It is ${sizeWords(file.size)}; a file may be ${sizeWords(MAX_ATTACHMENT_SIZE)}.`, icon: 'i-lucide-triangle-alert', color: 'warning' })
      continue
    }
    if (full.value) {
      toast.add({ title: `${file.name}: not attached`, description: `A ticket holds at most ${MAX_ATTACHMENTS} files.`, icon: 'i-lucide-triangle-alert', color: 'warning' })
      continue
    }
    const u: Upload = { n: ++uploadN, file, sent: 0, state: 'waiting' }
    uploads.value = [...uploads.value, u]
    send(u)
  }
}

function patch(n: number, change: Partial<Upload>) {
  uploads.value = uploads.value.map((u) => (u.n === n ? { ...u, ...change } : u))
}

function send(u: Upload) {
  queue = queue.then(async () => {
    patch(u.n, { state: 'sending', sent: 0 })
    const outcome = await props.upload(u.file, (sent) => patch(u.n, { sent })).catch((): UploadOutcome => 'failed')
    if (outcome === 'done') uploads.value = uploads.value.filter((x) => x.n !== u.n)
    else patch(u.n, { state: outcome })
  })
}

/** An upload swept or failed, sent again: a new operation, a new op id. */
function again(u: Upload) {
  patch(u.n, { state: 'waiting', sent: 0 })
  send({ ...u, state: 'waiting' })
}
function dismiss(u: Upload) {
  uploads.value = uploads.value.filter((x) => x.n !== u.n)
}

const input = useTemplateRef<HTMLInputElement>('input')
function picked(e: Event) {
  const el = e.target as HTMLInputElement
  if (el.files) add([...el.files])
  el.value = ''
}

const over = ref(false)
function dropped(e: DragEvent) {
  over.value = false
  if (props.locked || !e.dataTransfer?.files.length) return
  add([...e.dataTransfer.files])
}

// The images' object URLs, as each is fetched; an image that fails keeps its icon.
const images = ref(new Map<string, string>())
watch(
  live,
  (list) => {
    for (const a of list) {
      if (!inlineImage(a.contentType) || images.value.has(a.id)) continue
      void props.image(a.id).then((url) => {
        if (url) images.value = new Map(images.value).set(a.id, url)
      })
    }
  },
  { immediate: true },
)

const confirming = ref('')
async function removeIt(a: TicketAttachment) {
  confirming.value = ''
  await props.remove(a)
}

async function save(a: TicketAttachment) {
  try {
    await props.download(a)
  } catch (e) {
    toast.add({ title: `${a.name}: not downloaded`, description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  }
}

function fileIcon(a: TicketAttachment): string {
  const t = a.contentType.toLowerCase()
  if (t.startsWith('image/')) return 'i-lucide-file-image'
  if (t === 'application/pdf') return 'i-lucide-file-text'
  if (t.startsWith('text/') || t === 'application/json') return 'i-lucide-file-code'
  return 'i-lucide-file'
}
</script>

<template>
  <section
    class="flex min-w-0 flex-col gap-3 rounded-md"
    :class="over ? 'ring-2 ring-primary/50' : ''"
    data-ticket-attachments
    :data-count="live.length"
    @dragover.prevent="!locked && (over = true)"
    @dragleave="over = false"
    @drop.prevent="dropped"
  >
    <div class="flex items-center gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Attachments</h3>
      <span class="text-xs text-dimmed tabular-nums">{{ live.length }} of {{ MAX_ATTACHMENTS }}</span>
      <UButton v-if="!locked" icon="i-lucide-paperclip" label="Attach" size="xs" color="neutral" variant="soft" class="ml-auto" :disabled="full" data-attachment-add @click="input?.click()" />
      <input ref="input" type="file" multiple class="hidden" data-attachment-input @change="picked" />
    </div>

    <p v-if="!live.length && !uploads.length" class="text-sm text-dimmed">{{ locked ? 'No files.' : 'No files yet. Attach one, or drop it here: 16 MiB at most.' }}</p>

    <ul v-if="uploads.length" class="flex flex-col gap-2">
      <li v-for="u in uploads" :key="u.n" class="flex flex-col gap-1.5 rounded-md px-3 py-2 ring ring-default" :data-upload="u.file.name" :data-upload-state="u.state">
        <div class="flex min-w-0 items-center gap-2 text-sm">
          <UIcon name="i-lucide-upload" class="size-4 flex-none text-muted" />
          <span class="min-w-0 truncate">{{ u.file.name }}</span>
          <span class="flex-none text-xs text-dimmed tabular-nums">{{ sizeWords(u.file.size) }}</span>
          <span v-if="u.state === 'waiting'" class="ml-auto text-xs text-muted">Waiting</span>
          <span v-else-if="u.state === 'sending'" class="ml-auto text-xs text-muted tabular-nums" data-upload-progress>{{ u.file.size ? Math.min(100, Math.round((u.sent / u.file.size) * 100)) : 100 }}%</span>
          <template v-else>
            <span class="ml-auto text-xs" :class="u.state === 'expired' ? 'text-warning' : 'text-error'">{{ u.state === 'expired' ? 'The upload took too long and was let go' : 'Not attached' }}</span>
            <UButton label="Upload again" icon="i-lucide-rotate-ccw" size="xs" color="neutral" variant="soft" data-upload-again @click="again(u)" />
            <UButton icon="i-lucide-x" size="xs" color="neutral" variant="ghost" aria-label="Dismiss" @click="dismiss(u)" />
          </template>
        </div>
        <UProgress v-if="u.state === 'sending' || u.state === 'waiting'" :model-value="u.file.size ? Math.min(100, (u.sent / u.file.size) * 100) : 0" size="xs" />
      </li>
    </ul>

    <ul v-if="live.length" class="flex flex-col gap-2">
      <li v-for="a in live" :key="a.id" class="flex min-w-0 flex-col gap-2 rounded-md px-3 py-2 ring ring-default" :data-attachment="a.name" :data-attachment-id="a.id">
        <div class="flex min-w-0 items-center gap-2 text-sm">
          <UIcon :name="fileIcon(a)" class="size-4 flex-none text-muted" />
          <button type="button" class="min-w-0 truncate text-left hover:underline" :title="`Download ${a.name}`" data-attachment-name @click="save(a)">{{ a.name }}</button>
          <span class="flex-none text-xs text-dimmed tabular-nums">{{ sizeWords(a.size) }}</span>
          <span class="hidden flex-none text-xs text-dimmed sm:inline">· {{ a.createdBy || 'admin' }} {{ relativeTime(a.createdAt, now, { suffix: true }) }}</span>
          <span class="ml-auto flex flex-none gap-0.5">
            <UButton icon="i-lucide-download" size="xs" color="neutral" variant="ghost" :aria-label="`Download ${a.name}`" data-attachment-download @click="save(a)" />
            <UButton v-if="!locked && inlineImage(a.contentType)" icon="i-lucide-image-plus" size="xs" color="neutral" variant="ghost" :aria-label="`Show ${a.name} in a comment`" title="Put it in the comment being written" data-attachment-insert @click="emit('insert', attachmentMarkdown(a))" />
            <UButton v-if="!locked" icon="i-lucide-trash-2" size="xs" color="neutral" variant="ghost" :aria-label="`Remove ${a.name}`" data-attachment-remove @click="confirming = a.id" />
          </span>
        </div>
        <img v-if="images.get(a.id)" :src="images.get(a.id)" :alt="a.name" class="max-h-64 w-fit max-w-full rounded object-contain ring ring-default" data-attachment-image />
        <div v-if="confirming === a.id" class="flex flex-wrap items-center gap-2 text-sm" data-attachment-confirm>
          <span class="text-muted">Remove {{ a.name }} from the ticket?</span>
          <UButton label="Remove" color="error" variant="soft" size="xs" data-attachment-remove-confirm @click="removeIt(a)" />
          <UButton label="Keep it" color="neutral" variant="ghost" size="xs" @click="confirming = ''" />
        </div>
      </li>
    </ul>
  </section>
</template>
