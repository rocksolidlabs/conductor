<script setup lang="ts">
import type { DispatchTicket, SnapshotImage } from '~/composables/useDispatch'
import { githubHref, githubStateWords, ownerWord, readAge, refWords, snapshotOf } from '~/utils/tracker'

/**
 * A ticket's GitHub panel (Program item IN1a, docs/plans/tracker-linking.md, PR-8), the section the strip names GitHub: the linked issue
 * or pull request by its reference, GitHub's state in a chip of its own (a neutral badge: GitHub's word, never the ticket's status nor a
 * status's colour), when it was last read and Refresh; the field owners, each with Take over (Dispatch owns it from now on) or Give back
 * to GitHub; where a field Dispatch owns differs from GitHub's, that it differs, with a link to the item; the images an import downloaded,
 * drawn from the server's attachment route alone, and the steps to reproduce, folded; Link a GitHub issue and Unlink. The ticket's status
 * never moves because GitHub's did: a read that finds the item closed says so, and the person decides.
 *
 * What GitHub wrote is drawn as text, a link only where utils/tracker.ts' githubHref admits it (https on the item's own host); every
 * request that asks GitHub sets no timeout of its own and says "reading GitHub…" while it runs.
 */
const props = defineProps<{
  ticket: DispatchTicket
  locked: boolean
  now: number
  /** A token is kept for some host: Link a GitHub issue is offered on a ticket with none linked. */
  canLink: boolean
  /** An image attachment's content as an object URL, fetched with the token (useDispatchAttachments). */
  image: (id: string) => Promise<string | null>
  /** The linked item read again: '' once read, else why not; `changed` whether a field followed it. */
  refresh: () => Promise<{ words: string; changed: boolean }>
  /** A field's owner moved: '' once done, else why not. */
  authority: (field: 'title' | 'description', owner: 'dispatch' | 'github') => Promise<string>
  link: (url: string) => Promise<string>
  unlink: () => Promise<string>
  /** Opens Publish to GitHub (IN1a PR-9) with the linked item as its target; absent, the panel offers none. */
  publish?: () => void
}>()

const snap = computed(() => snapshotOf(props.ticket.externalSnapshot))
const linked = computed(() => !!props.ticket.externalId && !!snap.value)
const imported = computed(() => linked.value && props.ticket.source === 'github')
const state = computed(() => (snap.value ? githubStateWords(snap.value) : null))
const ref_ = computed(() => (snap.value ? refWords(snap.value) : ''))
/** The item's page as a link: only an https address on the item's own host. */
const href = computed(() => githubHref(props.ticket.externalUrl, snap.value?.host))
const kindWord = computed(() => (snap.value?.kind === 'change' ? 'pull request' : 'issue'))
const owners = computed(() => props.ticket.owners ?? { title: 'dispatch', description: 'dispatch' })
const readAt = computed(() => props.ticket.externalRead?.readAt)
/** A read's failure, in the words the server recorded on the ticket's read. */
const readError = computed(() => {
  const r = props.ticket.externalRead
  if (!r?.error || !r.errorAt) return ''
  return !r.readAt || r.errorAt > r.readAt ? r.error : ''
})
/** GitHub closed it while the ticket is open: said, and the person decides. */
const closedOnGitHub = computed(() => !!state.value && state.value.key !== 'open' && props.ticket.status !== 'done' && props.ticket.status !== 'cancelled')

// Where a field Dispatch owns differs from GitHub's: the title compared as written, the description by its SHA-256 against the snapshot's.
const descriptionDiffers = ref(false)
async function sha256(text: string): Promise<string> {
  const b = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text))
  return [...new Uint8Array(b)].map((x) => x.toString(16).padStart(2, '0')).join('')
}
watch(
  () => [props.ticket.description, snap.value?.descriptionSha256, owners.value.description] as const,
  async ([text, want, owner]) => {
    if (!want || owner !== 'dispatch') {
      descriptionDiffers.value = false
      return
    }
    const got = await sha256(text)
    if (text === props.ticket.description) descriptionDiffers.value = got !== want
  },
  { immediate: true },
)
const titleDiffers = computed(() => owners.value.title === 'dispatch' && !!snap.value && snap.value.title.trim() !== props.ticket.title.trim())

const refreshing = ref(false)
const notice = ref('')
const failure = ref('')
async function doRefresh() {
  if (refreshing.value) return
  refreshing.value = true
  notice.value = ''
  failure.value = ''
  try {
    const r = await props.refresh()
    if (r.words) failure.value = r.words
    else notice.value = r.changed ? 'Read from GitHub: what GitHub owns follows it.' : 'Read from GitHub: nothing changed.'
  } finally {
    refreshing.value = false
  }
}

const moving = ref('')
async function move(field: 'title' | 'description', owner: 'dispatch' | 'github') {
  if (moving.value) return
  moving.value = field
  failure.value = ''
  notice.value = ''
  try {
    const words = await props.authority(field, owner)
    if (words) failure.value = words
  } finally {
    moving.value = ''
  }
}

const linking = ref(false)
const linkUrl = ref('')
const linkBusy = ref(false)
async function doLink() {
  const u = linkUrl.value.trim()
  if (!u || linkBusy.value) return
  linkBusy.value = true
  failure.value = ''
  try {
    const words = await props.link(u)
    if (words) failure.value = words
    else {
      linking.value = false
      linkUrl.value = ''
    }
  } finally {
    linkBusy.value = false
  }
}

const unlinkAsk = ref(false)
const unlinking = ref(false)
async function doUnlink() {
  unlinking.value = true
  failure.value = ''
  try {
    const words = await props.unlink()
    if (words) failure.value = words
    unlinkAsk.value = false
  } finally {
    unlinking.value = false
  }
}

// The images an import downloaded: each by its attachment, drawn from its content fetched with the token; never GitHub's address.
const images = computed<SnapshotImage[]>(() => snap.value?.images ?? [])
const shown = ref(new Map<string, string>())
watch(
  images,
  (list) => {
    for (const im of list) {
      const id = im.attachmentId
      if (!id || shown.value.has(id)) continue
      void props.image(id).then((url) => {
        if (url) shown.value = new Map(shown.value).set(id, url)
      })
    }
  },
  { immediate: true },
)
const imageWords = (im: SnapshotImage) => (im.attachmentId ? '' : im.skipped === 'type' ? 'skipped: not a PNG, JPEG, GIF or WebP' : im.skipped === 'size' ? 'skipped: past 16 MiB' : 'not downloaded yet: Refresh fetches it')
/** An image's file name, from its id: the last part of its address's path, as text. */
function imageName(im: SnapshotImage): string {
  const last = im.id.slice(im.id.lastIndexOf('/') + 1)
  try {
    return decodeURIComponent(last) || 'image'
  } catch {
    return last || 'image'
  }
}

watch(
  () => props.ticket.key,
  () => {
    notice.value = ''
    failure.value = ''
    linking.value = false
    unlinkAsk.value = false
  },
)
</script>

<template>
  <section v-if="linked || (canLink && !locked)" class="flex flex-col gap-2" data-ticket-github :data-linked="linked ? '' : undefined">
    <div class="flex min-w-0 flex-wrap items-center gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">GitHub</h3>
      <template v-if="linked && snap">
        <a v-if="href" :href="href" target="_blank" rel="noopener noreferrer" class="min-w-0 truncate text-sm font-medium hover:underline" :title="href" data-github-ref>{{ ref_ }}</a>
        <span v-else class="min-w-0 truncate text-sm font-medium" :title="ticket.externalUrl" data-github-ref>{{ ref_ }}</span>
        <UBadge :label="state!.label" color="neutral" variant="outline" size="sm" class="flex-none" data-github-state-chip :data-github-state="state!.key" />
      </template>
      <div class="flex-1" />
      <template v-if="linked && !locked">
        <UButton v-if="publish" label="Publish to GitHub" icon="i-lucide-send" size="xs" color="neutral" variant="ghost" data-github-publish @click="publish()" />
        <UButton label="Refresh" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="ghost" :loading="refreshing" data-github-refresh @click="doRefresh" />
        <UButton v-if="!unlinkAsk" label="Unlink" icon="i-lucide-unlink" size="xs" color="neutral" variant="ghost" data-github-unlink @click="unlinkAsk = true" />
      </template>
      <UButton v-if="!linked && !linking" label="Link a GitHub issue" icon="i-lucide-github" size="xs" color="neutral" variant="ghost" data-github-link-open @click="linking = true" />
    </div>

    <UAlert v-if="failure" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="failure" data-github-failure>
      <template v-if="/Settings/.test(failure)" #description>
        <NuxtLink to="/settings?section=github" class="underline" data-github-settings-link>Open Settings, GitHub</NuxtLink>
      </template>
    </UAlert>

    <div v-if="linked && snap" class="flex flex-col gap-2 rounded-md px-3 py-2.5 text-sm ring ring-default">
      <p class="text-xs text-muted">
        <template v-if="imported">Imported from {{ ref_ }}</template><template v-else>Linked to {{ ref_ }}</template>, a GitHub {{ kindWord }}
        <span class="mx-1">·</span>
        <span data-github-read-at>{{ refreshing ? 'reading GitHub…' : readAge(readAt, now) }}</span>
      </p>
      <p v-if="readError" class="text-xs text-warning" data-github-read-error>The last read failed: {{ readError }}</p>
      <p v-if="notice" class="text-xs text-muted" data-github-notice>{{ notice }}</p>
      <p v-if="closedOnGitHub" class="text-xs text-default" data-github-closed>
        GitHub says it is {{ state!.label.toLowerCase() }}. This ticket's status is Dispatch's and has not moved: you decide.
      </p>

      <div class="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs" data-github-owners>
        <span v-for="field in ['title', 'description'] as const" :key="field" class="flex items-center gap-1.5" :data-github-owner="field" :data-owner="owners[field] === 'github' ? 'github' : 'dispatch'">
          <span class="text-muted">{{ field === 'title' ? 'Title' : 'Description' }}:</span>
          <span class="font-medium text-default">{{ ownerWord(owners[field]) }}</span>
          <template v-if="!locked">
            <UButton v-if="owners[field] === 'github'" label="Take over" size="xs" color="neutral" variant="link" class="px-0" :loading="moving === field" :data-github-take-over="field" @click="move(field, 'dispatch')" />
            <UButton v-else label="Give back to GitHub" size="xs" color="neutral" variant="link" class="px-0" :loading="moving === field" :data-github-give-back="field" @click="move(field, 'github')" />
          </template>
        </span>
      </div>
      <p v-if="titleDiffers || descriptionDiffers" class="text-xs text-muted" data-github-differs>
        GitHub's {{ titleDiffers && descriptionDiffers ? 'title and description differ' : titleDiffers ? 'title differs' : 'description differs' }}<template v-if="href">: <a :href="href" target="_blank" rel="noopener noreferrer" class="underline">see it on GitHub</a></template>.
      </p>

      <div v-if="snap.labels.length || snap.assignees.length" class="flex flex-wrap items-center gap-1.5 text-xs text-muted" data-github-shown>
        <span>On GitHub, shown and not mapped:</span>
        <UBadge v-for="l in snap.labels" :key="`l:${l}`" :label="l" color="neutral" variant="subtle" size="sm" />
        <span v-for="a in snap.assignees" :key="`a:${a}`">@{{ a }}</span>
      </div>

      <ul v-if="images.length" class="flex flex-wrap gap-2" data-github-images>
        <li v-for="im in images" :key="im.id" class="flex w-28 flex-col gap-1 text-xs" :data-github-image="im.attachmentId ?? ''">
          <img v-if="im.attachmentId && shown.get(im.attachmentId)" :src="shown.get(im.attachmentId)" :alt="imageName(im)" class="h-20 w-28 rounded object-cover ring ring-default" />
          <span v-else class="flex h-20 w-28 items-center justify-center rounded bg-elevated text-dimmed"><UIcon name="i-lucide-image" class="size-5" /></span>
          <span class="truncate text-muted" :title="imageName(im)">{{ imageName(im) }}</span>
          <span v-if="imageWords(im)" class="text-dimmed">{{ imageWords(im) }}</span>
        </li>
      </ul>

      <details v-if="snap.repro" class="text-xs" data-github-repro>
        <summary class="cursor-pointer text-muted">Steps to reproduce, as GitHub's description gives them</summary>
        <pre class="mt-1.5 whitespace-pre-wrap break-words rounded bg-elevated px-3 py-2 text-[13px] text-default">{{ snap.repro }}</pre>
      </details>

      <div v-if="unlinkAsk" class="flex flex-col gap-2 rounded-md bg-elevated/60 p-2.5 text-xs" data-github-unlink-ask>
        <p>Unlink {{ ref_ }}? Each field GitHub owns becomes Dispatch's, with its text as it is. Nothing changes on GitHub.</p>
        <div class="flex justify-end gap-2">
          <UButton label="Keep it" size="xs" color="neutral" variant="ghost" @click="unlinkAsk = false" />
          <UButton label="Unlink" icon="i-lucide-unlink" size="xs" color="neutral" variant="outline" :loading="unlinking" data-github-unlink-confirm @click="doUnlink" />
        </div>
      </div>
    </div>

    <form v-if="!linked && linking" class="flex flex-col gap-2 rounded-md p-3 ring ring-default" data-github-link-form @submit.prevent="doLink">
      <p class="text-xs text-muted">The issue or pull request becomes this ticket's linked item: every field stays Dispatch's, and GitHub's title and state are shown beside them.</p>
      <UInput v-model="linkUrl" placeholder="https://github.com/owner/repo/issues/12, owner/repo#12 or #12" class="w-full" aria-label="The GitHub item's address" autofocus data-github-link-url />
      <div class="flex items-center justify-end gap-2">
        <span v-if="linkBusy" class="mr-auto text-xs text-muted">reading GitHub…</span>
        <UButton label="Cancel" size="sm" color="neutral" variant="ghost" @click="linking = false" />
        <UButton type="submit" label="Link" icon="i-lucide-link" size="sm" color="neutral" variant="outline" :loading="linkBusy" :disabled="!linkUrl.trim()" data-github-link-submit />
      </div>
    </form>
  </section>
</template>
