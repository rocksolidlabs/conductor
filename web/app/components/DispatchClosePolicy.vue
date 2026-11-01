<script setup lang="ts">
import type { DispatchTicket } from '~/composables/useDispatch'
import { closeCard, type AcceptSeen } from '~/utils/dispatch'
import { relativeTime } from '~/utils/sessions'

/**
 * A ticket's close card (docs/plans/dispatch.md, PR-11): "To close: the owner's acceptance · a delivery link", each requirement with its
 * state as the ticket's read computed it (`close`, never derived here) and, unmet, why ("accepted for Alice; the owner is now Bob",
 * "accepted before the criteria changed"). Acceptance is its own fact, recorded by a button of its own for the owner the ticket has now and
 * against its criteria as they stand, with a note, and taken back by another; no status implies it. Close moves the ticket to Done: the
 * store decides, and a refusal is said here in the policy's words (the receipt's `missing`), whatever the card read, since the policy,
 * the owner or the criteria may have changed since. The card follows the project's policy edited elsewhere through the page's re-read on
 * the project's event.
 */
const props = defineProps<{
  ticket: DispatchTicket
  locked: boolean
  now: number
  /** The acceptance recorded with the note, for the owner and the criteria shown when Accept was clicked: '' once the store has it, else why not, in words. */
  accept: (note: string, seen: AcceptSeen) => Promise<string>
  unaccept: () => Promise<boolean>
  /** The ticket moved to Done: '' once the store has it, else why not, in words. */
  close: () => Promise<string>
}>()

const card = computed(() => closeCard(props.ticket))
const tone = { warning: 'text-warning', error: 'text-error', muted: 'text-muted', default: 'text-default', success: 'text-success' }

const noteOpen = ref(false)
const note = ref('')
const accepting = ref(false)
const unaccepting = ref(false)
const closing = ref(false)
/** Why the last Close was refused, until the ticket changes. */
const refused = ref('')
/** Why the last Accept was not recorded, until the next one or another ticket. */
const acceptRefused = ref('')
watch(
  () => props.ticket.rev,
  () => (refused.value = ''),
)
watch(
  () => props.ticket.key,
  () => {
    noteOpen.value = false
    note.value = ''
    refused.value = ''
    acceptRefused.value = ''
  },
)

/** The note's bound, the store's, in characters. */
const MAX_NOTE = 1000

async function doAccept() {
  if (accepting.value || card.value.acceptBlocked) return
  accepting.value = true
  acceptRefused.value = ''
  try {
    // What the person sees as they click: the acceptance is of this owner and these criteria, or of nothing.
    const words = await props.accept(note.value, { owner: props.ticket.owner, acceptance: props.ticket.acceptance })
    if (!words) {
      note.value = ''
      noteOpen.value = false
    } else acceptRefused.value = words
  } finally {
    accepting.value = false
  }
}
async function doUnaccept() {
  if (unaccepting.value) return
  unaccepting.value = true
  try {
    await props.unaccept()
  } finally {
    unaccepting.value = false
  }
}
async function doClose() {
  if (closing.value) return
  closing.value = true
  refused.value = ''
  try {
    refused.value = await props.close()
  } finally {
    closing.value = false
  }
}

const acceptLabel = computed(() => {
  const owner = props.ticket.owner
  if (!owner) return 'Accept'
  return card.value.acceptance.recorded ? `Accept again for ${owner}` : `Accept for ${owner}`
})
</script>

<template>
  <section
    class="flex flex-col gap-3 rounded-lg p-4 ring ring-default"
    data-close-policy
    :data-close-ready="card.ready ? '' : undefined"
    :data-close-closed="card.closed ? '' : undefined"
    :data-accepted="card.acceptance.recorded ? (card.acceptance.met ? 'met' : 'unmet') : 'none'"
  >
    <div class="flex items-start gap-2">
      <UIcon :name="card.closed ? 'i-lucide-circle-check-big' : 'i-lucide-flag'" class="mt-0.5 size-4 flex-none text-muted" />
      <div class="flex min-w-0 flex-1 flex-col gap-0.5">
        <h3 class="text-sm font-semibold text-highlighted" data-close-summary>{{ card.summary }}</h3>
        <p v-if="card.closed" class="text-xs text-muted">The ticket is {{ ticket.status === 'done' ? 'done' : 'cancelled' }}.</p>
      </div>
    </div>

    <ul v-if="card.items.length" class="flex flex-col gap-1.5" data-close-items>
      <li v-for="i in card.items" :key="i.requirement" class="flex items-start gap-2 text-sm" :data-close-item="i.requirement" :data-met="i.met ? '' : undefined">
        <UIcon :name="i.met ? 'i-lucide-check' : 'i-lucide-circle-dashed'" class="mt-0.5 size-4 flex-none" :class="i.met ? 'text-success' : 'text-muted'" />
        <span class="flex min-w-0 flex-col">
          <span class="first-letter:uppercase">{{ i.words }}</span>
          <span class="text-xs" :class="i.met ? 'text-muted' : i.requirement === 'acceptance' && card.acceptance.recorded ? 'text-warning' : 'text-muted'" data-close-state>{{ i.state }}</span>
        </span>
      </li>
    </ul>

    <!-- The acceptance on record, said whether the policy requires it or not: it is a fact of its own. -->
    <div v-if="!card.items.some((i) => i.requirement === 'acceptance')" class="flex items-start gap-2 text-sm" data-acceptance-state>
      <UIcon :name="card.acceptance.met ? 'i-lucide-badge-check' : 'i-lucide-badge'" class="mt-0.5 size-4 flex-none text-muted" />
      <span :class="tone[card.acceptance.tone]">{{ card.acceptance.text }}</span>
    </div>
    <p v-if="ticket.acceptedAt" class="text-xs text-muted" data-acceptance-record>
      Accepted {{ relativeTime(ticket.acceptedAt, now, { suffix: true }) }}<template v-if="ticket.acceptedNote">: <span class="whitespace-pre-wrap text-default" data-acceptance-note>{{ ticket.acceptedNote }}</span></template>
    </p>

    <UAlert v-if="refused" color="warning" variant="subtle" icon="i-lucide-triangle-alert" title="Not closed" :description="refused" data-close-refused />
    <UAlert v-if="acceptRefused" color="warning" variant="subtle" icon="i-lucide-triangle-alert" title="Not accepted" :description="acceptRefused" data-accept-refused />

    <template v-if="!locked">
      <div v-if="noteOpen" class="flex flex-col gap-1">
        <UTextarea v-model="note" :rows="2" autoresize :maxlength="MAX_NOTE" size="sm" placeholder="A note with the acceptance (optional)…" aria-label="A note with the acceptance" data-accept-note />
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <UButton
          v-if="!card.acceptance.met"
          :label="acceptLabel"
          icon="i-lucide-badge-check"
          size="sm"
          color="primary"
          variant="soft"
          :disabled="!!card.acceptBlocked"
          :title="card.acceptBlocked || undefined"
          :loading="accepting"
          data-accept
          @click="doAccept"
        />
        <UButton v-if="!card.acceptance.met && !noteOpen" label="With a note" icon="i-lucide-notebook-pen" size="sm" color="neutral" variant="ghost" :disabled="!!card.acceptBlocked" data-accept-note-open @click="noteOpen = true" />
        <UButton v-if="card.acceptance.recorded" label="Take the acceptance back" icon="i-lucide-undo-2" size="sm" color="neutral" variant="ghost" :loading="unaccepting" data-unaccept @click="doUnaccept" />
        <div class="flex-1" />
        <UButton v-if="!card.closed" label="Close as done" icon="i-lucide-circle-check-big" size="sm" :color="card.ready ? 'primary' : 'neutral'" :variant="card.ready ? 'solid' : 'outline'" :loading="closing" data-close @click="doClose" />
      </div>
      <p v-if="card.acceptBlocked && !card.acceptance.met" class="text-xs text-muted" data-accept-blocked>{{ card.acceptBlocked }} Pick the ticket's owner first.</p>
    </template>
  </section>
</template>
