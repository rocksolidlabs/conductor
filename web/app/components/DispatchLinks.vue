<script setup lang="ts">
import type { LinkKind, NewLink, TicketLink } from '~/composables/useDispatch'
import { linkLabel } from '~/utils/dispatch'
import { relativeTime } from '~/utils/sessions'

/**
 * A ticket's links outward (docs/plans/dispatch.md, PR-11): where it was delivered, the merged pull request's or the release's URL that a
 * person adds (a merge is such a link here; its merged state is never read, IN1a's), and pages of another tracker's (external); each with
 * its note, opened in a new tab, and removed. A delivery link is what a close policy's "a delivery link" asks for. The run links are the
 * Runs section's (DispatchRuns.vue). Each add and remove is one operation on the ticket, made by the page; a refusal is said here.
 */
const props = defineProps<{
  links: readonly TicketLink[]
  locked: boolean
  now: number
  /** A link made: '' once the store has it, else why not, in words. */
  add: (link: NewLink) => Promise<string>
  remove: (link: TicketLink) => Promise<boolean>
}>()

const KINDS: ReadonlyArray<{ value: Exclude<LinkKind, 'run'>; label: string; icon: string; description: string }> = [
  { value: 'delivery', label: 'Delivery', icon: 'i-lucide-package-check', description: "Where it was delivered: the merged pull request's or the release's URL" },
  { value: 'external', label: 'External', icon: 'i-lucide-external-link', description: "A page of another tracker's, or anything else it relates to" },
]
const kindOf = (k: string) => KINDS.find((x) => x.value === k) ?? KINDS[1]!

const shown = computed(() => {
  const order = (l: TicketLink) => (l.kind === 'delivery' ? 0 : 1)
  return props.links.filter((l) => l.kind !== 'run' && !l.deletedAt).sort((a, b) => order(a) - order(b) || a.createdAt.localeCompare(b.createdAt))
})

const adding = ref(false)
const kind = ref<Exclude<LinkKind, 'run'>>('delivery')
const url = ref('')
const note = ref('')
const error = ref('')
/**
 * Each opening of the form, and each cancel of it, is a new one: an add answers only the opening it was sent from, so one still on its way
 * after a cancel never closes, or says its refusal in, a form opened since with another link typed in it.
 */
const opening = ref(0)
/** The opening whose add is on its way, null when none is. */
const sendingFrom = ref<number | null>(null)
const sending = computed(() => sendingFrom.value === opening.value && adding.value)
const urlProblem = computed(() => (url.value.trim() && !/^https?:\/\/[^\s/]+/i.test(url.value.trim()) ? 'An http or https URL' : ''))

function openForm(k: Exclude<LinkKind, 'run'> = 'delivery') {
  opening.value++
  kind.value = k
  url.value = ''
  note.value = ''
  error.value = ''
  adding.value = true
}

function cancel() {
  opening.value++
  adding.value = false
}

async function submit() {
  const u = url.value.trim()
  if (!u || urlProblem.value || sending.value) return
  const from = opening.value
  sendingFrom.value = from
  error.value = ''
  try {
    const words = await props.add({ kind: kind.value, url: u, ...(note.value.trim() ? { note: note.value.trim() } : {}) })
    if (from !== opening.value) return
    if (words) error.value = words
    else adding.value = false
  } finally {
    if (sendingFrom.value === from) sendingFrom.value = null
  }
}

const removing = ref('')
async function drop(l: TicketLink) {
  removing.value = l.id
  try {
    await props.remove(l)
  } finally {
    if (removing.value === l.id) removing.value = ''
  }
}

/** The note's bound, the store's, in characters. */
const MAX_NOTE = 1000
</script>

<template>
  <section class="flex flex-col gap-2" data-ticket-links :data-count="shown.length">
    <div class="flex items-center gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Links</h3>
      <div class="flex-1" />
      <UButton v-if="!locked && !adding" label="Add a link" icon="i-lucide-link" size="xs" color="neutral" variant="ghost" data-link-add-open @click="openForm()" />
    </div>
    <p v-if="!shown.length && !adding" class="text-sm text-dimmed">No delivery or external link yet.</p>
    <ul v-if="shown.length" class="flex flex-col gap-1.5">
      <li v-for="l in shown" :key="l.id" class="flex min-w-0 items-start gap-2 rounded-md px-3 py-2 text-sm ring ring-default" :data-link="l.ref" :data-link-kind="l.kind">
        <UIcon :name="kindOf(l.kind).icon" class="mt-0.5 size-4 flex-none text-muted" :title="kindOf(l.kind).label" />
        <div class="flex min-w-0 flex-1 flex-col gap-0.5">
          <div class="flex min-w-0 flex-wrap items-baseline gap-x-2">
            <UBadge :label="kindOf(l.kind).label" size="sm" variant="subtle" :color="l.kind === 'delivery' ? 'success' : 'neutral'" class="flex-none" />
            <a :href="l.url" target="_blank" rel="noopener noreferrer" class="min-w-0 truncate font-medium hover:underline" :title="l.url" data-link-url>{{ linkLabel(l.url) }}</a>
          </div>
          <p v-if="l.note" class="whitespace-pre-wrap break-words text-xs text-muted" data-link-note>{{ l.note }}</p>
          <p class="text-xs text-dimmed">added by {{ l.createdBy || 'admin' }} {{ relativeTime(l.createdAt, now, { suffix: true }) }}</p>
        </div>
        <UButton v-if="!locked" icon="i-lucide-x" size="xs" color="neutral" variant="ghost" class="flex-none" :aria-label="`Remove the link ${linkLabel(l.url)}`" :loading="removing === l.id" data-link-remove @click="drop(l)" />
      </li>
    </ul>

    <form v-if="adding && !locked" class="flex flex-col gap-2 rounded-md p-3 ring ring-default" data-link-form @submit.prevent="submit">
      <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-link-error />
      <URadioGroup v-model="kind" :items="KINDS.map((k) => ({ label: k.label, value: k.value, description: k.description }))" orientation="horizontal" size="sm" data-link-kind-pick />
      <UFormField :error="urlProblem || undefined" size="sm">
        <UInput v-model="url" :placeholder="kind === 'delivery' ? 'https://github.com/acme/api/pull/12' : 'https://…'" autofocus class="w-full" aria-label="The link's URL" data-link-url-input />
      </UFormField>
      <UInput v-model="note" :maxlength="MAX_NOTE" placeholder="A note (optional)…" size="sm" class="w-full" aria-label="The link's note" data-link-note-input />
      <div class="flex justify-end gap-2">
        <UButton label="Cancel" color="neutral" variant="ghost" size="sm" data-link-cancel @click="cancel" />
        <UButton type="submit" label="Add" icon="i-lucide-plus" size="sm" :loading="sending" :disabled="!url.trim() || !!urlProblem" data-link-submit />
      </div>
    </form>
  </section>
</template>
