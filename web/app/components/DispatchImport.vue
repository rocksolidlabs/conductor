<script setup lang="ts">
import type { ExternalPreview, ImportReply, TicketStatus } from '~/composables/useDispatch'
import type { DispatchOrder } from '~/utils/dispatchOrder'
import { ApiError } from '~/composables/useApi'
import { PRIORITIES, TICKET_STATUSES, statusWord } from '~/utils/dispatch'
import { dispatchFailureWords } from '~/composables/useDispatchOps'
import { githubStateWords, importReady, needsSettings, ownersWords, trackerRefusalWords } from '~/utils/tracker'

/**
 * Import from GitHub (Program item IN1a, docs/plans/tracker-linking.md, PR-8): a GitHub issue or pull request made a ticket of the project.
 * It takes the item's address, `owner/repo#12`, or `#12` in the repository of the project's tracker URL, and shows what the import would
 * make before anything is made (POST …/external/preview, which writes nothing): the title, the description's first 2 KiB, the images
 * found (downloaded as attachments by the import), the steps to reproduce found, GitHub's state, labels and assignees (shown, never
 * mapped), the fields GitHub will own, and the ticket that has the item already, when one does. The ticket's own fields beside them, then
 * Import: one operation, its resend a replay. Every request asks GitHub with the token kept for its host and sets no timeout of its own;
 * while one runs the dialog says "reading GitHub…". GitHub's description is drawn by the Markdown preview's sanitiser, its images held.
 */
const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ project: string; gate: DispatchOrder }>()
const emit = defineEmits<{ created: [ticket: { key: string; title: string }] }>()

const api = useDispatchApi()
const ops = useDispatchOps(props.gate)

const url = ref('')
const preview = ref<ExternalPreview | null>(null)
/** The address the preview shown was read for: Import sends that one. */
const previewed = ref('')
const reading = ref(false)
const importing = ref(false)
const error = ref('')
const errorCode = ref('')
const status = ref<TicketStatus>('backlog')
const priority = ref(0)

/** Each read, numbered: an answer lands only while it is the last read asked in this opening of the dialog. */
let readGen = 0
watch(open, (o) => {
  if (!o) return
  // A read still on its way from an earlier opening lands nowhere: its preview is not this opening's.
  readGen++
  reading.value = false
  url.value = ''
  preview.value = null
  previewed.value = ''
  error.value = ''
  errorCode.value = ''
  status.value = 'backlog'
  priority.value = 0
})

const statusItems = TICKET_STATUSES.map((s) => ({ label: statusWord(s).label, value: s, icon: statusWord(s).icon }))
const priorityItems = PRIORITIES.map((p) => ({ label: p.label, value: p.value, icon: p.icon }))
const state = computed(() => (preview.value ? githubStateWords(preview.value) : null))
const busy = computed(() => reading.value || importing.value)
/** Import sends only the item previewed for the address the field holds now (importReady): a changed address is read again first. */
const ready = computed(() => importReady({ preview: preview.value, previewed: previewed.value, url: url.value, reading: reading.value }))
/** The field names another address than the preview shown: said where Import is. */
const stale = computed(() => !!preview.value && url.value.trim() !== previewed.value)

function failed(e: unknown) {
  if (e instanceof ApiError) {
    errorCode.value = e.code
    error.value = trackerRefusalWords(e.code, e.message)
  } else {
    errorCode.value = ''
    error.value = dispatchFailureWords(e)
  }
}

async function read() {
  const u = url.value.trim()
  if (!u || busy.value) return
  const gen = ++readGen
  reading.value = true
  error.value = ''
  errorCode.value = ''
  try {
    const got = await api.externalPreview(u, props.project)
    if (gen !== readGen) return
    preview.value = got
    previewed.value = u
  } catch (e) {
    if (gen !== readGen) return
    preview.value = null
    previewed.value = ''
    failed(e)
  } finally {
    if (gen === readGen) reading.value = false
  }
}

async function submit() {
  if (!preview.value || !ready.value || importing.value) return
  importing.value = true
  error.value = ''
  try {
    const body = { url: preview.value.url || previewed.value, ...(status.value !== 'backlog' ? { status: status.value } : {}), ...(priority.value ? { priority: priority.value } : {}) }
    const reply = await ops.run<ImportReply>((op) => api.importExternal(props.project, { ...op, ...body }), { thing: `import:${props.project}`, what: 'Import', raise: true })
    const key = reply?.ticket?.key || reply?.receipt.key
    if (key) emit('created', { key, title: reply?.ticket?.title ?? preview.value.title })
    open.value = false
  } catch (e) {
    failed(e)
  } finally {
    importing.value = false
  }
}

/** The description's bytes the preview shows (its first 2 KiB). */
const shownBytes = computed(() => (preview.value ? new TextEncoder().encode(preview.value.description).length : 0))
const kib = (n: number) => `${Math.round((n / 1024) * 10) / 10} KiB`
/** An image's file name, from its id: the last part of its address's path. */
function imageName(id: string): string {
  const last = id.slice(id.lastIndexOf('/') + 1)
  try {
    return decodeURIComponent(last) || 'image'
  } catch {
    return last || 'image'
  }
}
</script>

<template>
  <UModal v-model:open="open" :title="`Import from GitHub into ${project}`" :dismissible="!importing" :close="{ disabled: importing }" :ui="{ content: 'max-w-2xl' }">
    <template #body>
      <div class="flex flex-col gap-4" data-dispatch-import>
        <form class="flex flex-col gap-2" @submit.prevent="read">
          <UFormField label="The issue or pull request" description="Its address, owner/repo#12, or #12 in the repository of the project's tracker URL.">
            <div class="flex gap-2">
              <UInput v-model="url" placeholder="https://github.com/owner/repo/issues/12" class="min-w-0 flex-1" autofocus aria-label="The GitHub item's address" data-import-url />
              <UButton type="submit" label="Read it" icon="i-lucide-github" color="neutral" variant="outline" :loading="reading" :disabled="!url.trim() || importing" data-import-read />
            </div>
          </UFormField>
          <p v-if="reading" class="text-xs text-muted" data-import-reading>reading GitHub…</p>
        </form>

        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" :data-import-error="errorCode">
          <template v-if="needsSettings(errorCode)" #description>
            <NuxtLink to="/settings?section=github" class="underline" @click="open = false">Open Settings, GitHub</NuxtLink>
          </template>
        </UAlert>

        <section v-if="preview" class="flex flex-col gap-3 rounded-md p-3 ring ring-default" data-import-preview :data-import-ref="`${preview.ref.space}#${preview.ref.id}`">
          <div class="flex min-w-0 flex-wrap items-center gap-2">
            <span class="text-xs text-muted">{{ preview.ref.space }}#{{ preview.ref.id }}</span>
            <UBadge v-if="state" :label="state.label" color="neutral" variant="outline" size="sm" data-import-state />
          </div>
          <p class="min-w-0 break-words text-base font-semibold text-highlighted" data-import-title>{{ preview.title }}</p>
          <UAlert v-if="preview.linked" color="neutral" variant="subtle" icon="i-lucide-link" :title="`${preview.linked} has this item as its linked issue already: it is not imported twice.`" data-import-linked />
          <div v-if="preview.description" class="max-h-64 min-w-0 overflow-y-auto rounded-md ring ring-default" data-import-description>
            <MarkdownPreview :text="preview.description" dir="" :relative-links="false" linkify />
          </div>
          <p v-if="shownBytes < preview.descriptionLength" class="text-xs text-dimmed">The first {{ kib(shownBytes) }} of {{ kib(preview.descriptionLength) }}.</p>
          <div v-if="preview.images.length" class="flex flex-col gap-1 text-xs">
            <span class="text-muted">{{ preview.images.length }} {{ preview.images.length === 1 ? 'image' : 'images' }} found, downloaded as attachments (PNG, JPEG, GIF or WebP, at most 16 MiB each, eight at most):</span>
            <ul class="flex flex-wrap gap-1.5">
              <li v-for="id in preview.images" :key="id" data-import-image><UBadge :label="imageName(id)" icon="i-lucide-image" color="neutral" variant="subtle" size="sm" /></li>
            </ul>
          </div>
          <details v-if="preview.repro" class="text-xs" data-import-repro open>
            <summary class="cursor-pointer text-muted">Steps to reproduce found</summary>
            <pre class="mt-1.5 whitespace-pre-wrap break-words rounded bg-elevated px-3 py-2 text-[13px] text-default">{{ preview.repro }}</pre>
          </details>
          <div v-if="preview.labels.length || preview.assignees.length" class="flex flex-wrap items-center gap-1.5 text-xs text-muted">
            <span>On GitHub, shown and not mapped:</span>
            <UBadge v-for="l in preview.labels" :key="`l:${l}`" :label="l" color="neutral" variant="subtle" size="sm" />
            <span v-for="a in preview.assignees" :key="`a:${a}`">@{{ a }}</span>
          </div>
          <p class="text-xs text-muted" data-import-owners>{{ ownersWords(preview.owners) }}: GitHub owns them until you take them over, and a Refresh follows it. The ticket's status is always Dispatch's.</p>
        </section>

        <div v-if="preview && !preview.linked" class="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <UFormField label="Status" description="The ticket's own: GitHub's state never moves it.">
            <USelect v-model="status" :items="statusItems" class="w-full" data-import-status />
          </UFormField>
          <UFormField label="Priority">
            <USelect v-model="priority" :items="priorityItems" class="w-full" data-import-priority />
          </UFormField>
        </div>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full items-center justify-end gap-2">
        <span v-if="importing" class="mr-auto text-xs text-muted">reading GitHub…</span>
        <span v-else-if="stale && !reading" class="mr-auto text-xs text-muted" data-import-stale>The address changed: Read it again to import it.</span>
        <UButton label="Cancel" color="neutral" variant="ghost" :disabled="importing" @click="open = false" />
        <UButton label="Import" icon="i-lucide-download" :loading="importing" :disabled="!ready" data-import-submit @click="submit" />
      </div>
    </template>
  </UModal>
</template>
