<script setup lang="ts">
import { alonePick, broadcastByName, broadcastSelection, messageCount, messageToast, MESSAGE_ALL_TIP, MESSAGE_MAX, sendOutcome, sentWords, togglePick, type BroadcastMember, type RecipientPick } from '~/utils/crews'
import { STATUS_LOOK, statusOfKey } from '~/utils/status'

/**
 * The run's message bar: `Message to`, a chip for each member, the line and `Send to N`. It types the line into the members the chips
 * tick (POST /api/runs/{run}/broadcast), recorded as input by the display name, or by the server's OS user when none is set
 * (broadcastByName). The chips are the one place a person picks who gets the line (broadcastSelection): `All` and each member that
 * can get a line now, ticked or not, and each one that cannot, saying why (it waits on a question, ended, is starting, has not
 * started). A double-click on a member's chip leaves that member alone; a press on the chip of one that waits on a question asks the
 * page to show its tile (`show`). The pick is the page's (`v-model:pick`), so its command palette sends to the same members, and the
 * bar says whether it holds the focus (`update:focused`), so the page rings the tiles that will get the line. The pick and the line
 * begun are kept for each run for the tab's life (useRunRecipients). A line every member it named
 * got is said in the bar alone: the field reads `Sent to lead and core.` for SENT_SHOWN ms, and a polite live region says it once. One
 * that reached only some toasts who was skipped and why; one that reached nobody, or failed, toasts `Not sent` and stays in the field.
 * From MESSAGE_COUNT_FROM bytes the field counts them; past MESSAGE_MAX the count is red and Send is off. On a stopped run the bar says
 * so, with no field.
 */
const props = defineProps<{ runId: string; members: readonly BroadcastMember[]; pick: RecipientPick; ended?: boolean; disabled?: boolean }>()
const emit = defineEmits<{ 'update:pick': [pick: RecipientPick]; 'update:focused': [focused: boolean]; show: [name: string] }>()

const api = useSessions()
const toast = useToast()
const identity = useIdentity()
const { text } = useRunRecipients(toRef(props, 'runId'))
const sending = ref(false)

const selection = computed(() => broadcastSelection(props.members, props.pick))
const bytes = computed(() => new TextEncoder().encode(text.value).length)
const tooLong = computed(() => bytes.value > MESSAGE_MAX)
const count = computed(() => messageCount(bytes.value))

/** How long the field says who got the last line, in ms. */
const SENT_SHOWN = 4000
/** The field's words after a line everyone it named got (`Sent to lead and core.`), for SENT_SHOWN ms; else who gets the next. */
const sent = ref('')
/** What the polite live region says: the same words, once. */
const said = ref('')
let sentTimer: ReturnType<typeof setTimeout> | undefined
function showSent(words: string) {
  clearTimeout(sentTimer)
  sent.value = words
  said.value = words
  sentTimer = setTimeout(() => {
    sent.value = ''
    said.value = ''
  }, SENT_SHOWN)
}
onBeforeUnmount(() => clearTimeout(sentTimer))
const placeholder = computed(() => sent.value || selection.value.placeholder)
const canSend = computed(() => !props.disabled && !props.ended && !sending.value && selection.value.to.length > 0 && !!text.value.trim() && !tooLong.value)

/** The chip of a member that waits on a question is amber through the status language (needs); the others that cannot be ticked are grey. */
const needs = statusOfKey('needs')

function toggle(name: string) {
  emit('update:pick', togglePick(props.members, props.pick, name))
}
function alone(name: string) {
  emit('update:pick', alonePick(props.members, props.pick, name))
}

// On a phone the bar at rest is one row: who gets the line, said short, any member that waits on a question (its amber chip, which
// shows its tile), and the field with Send. The chips open when the person presses who gets it or puts the cursor in the field, and
// fold again when the bar loses the focus, so the wall above keeps its room.
const phone = useIsPhone()
const opened = ref(false)
const folded = computed(() => phone.value && !opened.value)
const waiting = computed(() => selection.value.chips.filter((c) => c.state === 'waiting'))
/** Who gets the line, short: `To all`, `To core`, `To 2`, `To nobody`. */
const short = computed(() => {
  const to = selection.value.to
  if (!to.length) return 'To nobody'
  if (selection.value.all) return 'To all'
  return to.length === 1 ? `To ${to[0]}` : `To ${to.length}`
})
const form = useTemplateRef<HTMLFormElement>('form')
async function openChips() {
  opened.value = true
  await nextTick()
  form.value?.querySelector<HTMLInputElement>('input[data-message-field]')?.focus()
}

/** Whether the focus is in the bar: the page rings the tiles that will get the line while it is. The field opens the chips. */
function focusIn(e: FocusEvent) {
  emit('update:focused', true)
  if ((e.target as HTMLElement | null)?.matches?.('input[data-message-field]')) opened.value = true
}
function focusLeft(e: FocusEvent) {
  const to = e.relatedTarget as Node | null
  if (!to || !(e.currentTarget as HTMLElement).contains(to)) {
    emit('update:focused', false)
    opened.value = false
  }
}
onBeforeUnmount(() => emit('update:focused', false))

async function send() {
  if (!canSend.value) return
  // Taken before the wait: a request that names nobody goes to every member.
  const to = [...selection.value.to]
  sending.value = true
  try {
    const byName = await broadcastByName(identity.name.value, api.whoami)
    const r = await api.broadcastRun(props.runId, { text: text.value, members: to, byName })
    const outcome = sendOutcome(r, to)
    if (outcome === 'all') showSent(sentWords(r.sent))
    else toast.add({ ...messageToast(r), icon: 'i-lucide-megaphone' })
    if (outcome !== 'none') text.value = ''
  } catch (e) {
    toast.add({ title: 'Not sent', description: (e as Error).message, color: 'error', icon: 'i-lucide-triangle-alert' })
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <div v-if="ended" class="rounded-md border border-default px-3.5 py-2.5 text-sm text-muted" data-broadcast data-broadcast-ended>This run has ended.</div>
  <form
    v-else
    ref="form"
    class="flex items-center rounded-md border border-default"
    :class="folded ? 'flex-nowrap gap-2 px-2 py-1.5' : 'flex-wrap gap-x-3 gap-y-2 px-3.5 py-2.5'"
    data-broadcast data-message-bar :data-bar-folded="folded || undefined" @submit.prevent="send" @focusin="focusIn" @focusout="focusLeft">
    <template v-if="folded">
      <!-- Pressed, the chips open and the cursor goes to the field (mousedown keeps the focus where it is until then). -->
      <UButton
        :label="short"
        size="sm"
        color="neutral"
        variant="outline"
        trailing-icon="i-lucide-chevron-up"
        class="min-h-11 flex-none"
        :aria-label="`Message to: ${selection.to.length ? selection.to.join(', ') : 'nobody'}. Pick who gets it`"
        aria-expanded="false"
        :disabled="disabled"
        data-message-to
        @mousedown.prevent
        @click="openChips"
      />
      <UTooltip v-for="c in waiting" :key="c.name" :text="c.tip">
        <UButton :label="c.name" size="sm" :color="STATUS_LOOK.needs.tone" variant="subtle" class="min-h-11 flex-none" :aria-label="c.label" :data-message-chip="c.name" :data-chip-state="c.state" @click="emit('show', c.name)">
          <template #leading>
            <StatusMark :status="needs" />
          </template>
        </UButton>
      </UTooltip>
    </template>
    <span v-if="!folded" id="message-to" class="flex-none text-sm font-semibold text-highlighted">Message to</span>
    <!-- A press on a chip keeps the focus in the field (mousedown), so a line half written stays under the cursor. -->
    <div v-if="!folded" class="flex flex-wrap items-center gap-1.5" role="group" aria-labelledby="message-to" data-message-chips>
      <UTooltip :text="MESSAGE_ALL_TIP">
        <UButton
          label="All"
          icon="i-lucide-check"
          size="xs"
          :color="selection.all ? 'primary' : 'neutral'"
          :variant="selection.all ? 'subtle' : 'outline'"
          :ui="{ leadingIcon: selection.all ? '' : 'invisible' }"
          :aria-pressed="selection.all"
          :disabled="disabled"
          class="max-sm:min-h-11"
          data-message-chip="all"
          :data-chip-state="selection.all ? 'on' : 'off'"
          @mousedown.prevent
          @click="emit('update:pick', null)"
        />
      </UTooltip>
      <template v-for="c in selection.chips" :key="c.name">
        <!-- An unticked chip keeps the check's room, so a chip never changes width under the pointer. -->
        <UButton
          v-if="c.state === 'ready'"
          :label="c.label"
          icon="i-lucide-check"
          size="xs"
          :color="c.on ? 'primary' : 'neutral'"
          :variant="c.on ? 'subtle' : 'outline'"
          :ui="{ leadingIcon: c.on ? '' : 'invisible' }"
          :aria-pressed="c.on"
          :disabled="disabled"
          class="max-sm:min-h-11"
          :data-message-chip="c.name"
          :data-chip-state="c.on ? 'on' : 'off'"
          @mousedown.prevent
          @click="toggle(c.name)"
          @dblclick="alone(c.name)"
        />
        <!-- One that waits on a question: a press shows its tile, where the question is answered. -->
        <UTooltip v-else-if="c.state === 'waiting'" :text="c.tip">
          <UButton :label="c.label" size="xs" :color="STATUS_LOOK.needs.tone" variant="subtle" class="max-sm:min-h-11" :data-message-chip="c.name" :data-chip-state="c.state" @click="emit('show', c.name)">
            <template #leading>
              <StatusMark :status="needs" />
            </template>
          </UButton>
        </UTooltip>
        <UTooltip v-else :text="c.tip">
          <UBadge color="neutral" variant="outline" size="md" class="text-muted max-sm:min-h-11" aria-disabled="true" :data-message-chip="c.name" :data-chip-state="c.state">{{ c.label }}</UBadge>
        </UTooltip>
      </template>
    </div>
    <UInput
      v-model="text"
      size="sm"
      :placeholder="placeholder"
      aria-label="Message text"
      :color="tooLong ? 'error' : undefined"
      :highlight="tooLong"
      :aria-invalid="tooLong || undefined"
      class="min-w-0 flex-1 sm:min-w-48"
      :disabled="disabled"
      data-message-field
    >
      <template v-if="count" #trailing>
        <span class="text-xs tabular-nums" :class="tooLong ? 'text-error' : 'text-muted'" :data-message-count="tooLong ? 'over' : 'under'">{{ count }}</span>
      </template>
    </UInput>
    <span class="sr-only" aria-live="polite" data-message-said>{{ said }}</span>
    <IconButton v-if="folded" type="submit" size="sm" :label="selection.button" icon="i-lucide-corner-down-left" class="min-h-11 min-w-11 flex-none justify-center" :loading="sending" :disabled="!canSend" data-broadcast-send />
    <UButton v-else type="submit" size="sm" :label="selection.button" trailing-icon="i-lucide-corner-down-left" class="max-sm:min-h-11" :loading="sending" :disabled="!canSend" data-broadcast-send />
  </form>
</template>
