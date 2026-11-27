<script setup lang="ts">
import { broadcastByName, broadcastSelection, messageToast, MESSAGE_ALL_TIP, togglePick, type BroadcastMember, type MessageChip, type RecipientPick } from '~/utils/crews'
import { STATUS_LOOK, statusOfKey } from '~/utils/status'

/**
 * The run's message bar: `Message to`, a chip for each member, the line and `Send to N`. It types the line into the members the chips
 * tick (POST /api/runs/{run}/broadcast), recorded as input by the display name, or by the server's OS user when none is set
 * (broadcastByName). The chips are the one place a person picks who gets the line (broadcastSelection): `All` and each member that
 * can get a line now, ticked or not, and each one that cannot, saying why (it waits on a question, ended, is starting, has not
 * started). The pick is the page's (`v-model:pick`), so its command palette sends to the same members. A line that reached nobody,
 * or failed, toasts `Not sent` and stays in the field. On a stopped run the bar says so, with no field.
 */
const props = defineProps<{ runId: string; members: readonly BroadcastMember[]; pick: RecipientPick; ended?: boolean; disabled?: boolean }>()
const emit = defineEmits<{ 'update:pick': [pick: RecipientPick] }>()

const api = useSessions()
const toast = useToast()
const identity = useIdentity()
const text = ref('')
const sending = ref(false)

const selection = computed(() => broadcastSelection(props.members, props.pick))
/** The server's limit, in bytes. */
const MAX = 4096
const tooLong = computed(() => new TextEncoder().encode(text.value).length > MAX)
const canSend = computed(() => !props.disabled && !props.ended && !sending.value && selection.value.to.length > 0 && !!text.value.trim() && !tooLong.value)

/** A chip that cannot be ticked: amber for one that waits on a person (the status language's needs), grey for the rest. */
const tone = (c: MessageChip) => (c.state === 'waiting' ? STATUS_LOOK.needs.tone : 'neutral')
const needs = statusOfKey('needs')

function toggle(name: string) {
  emit('update:pick', togglePick(props.members, props.pick, name))
}

async function send() {
  if (!canSend.value) return
  // Taken before the wait: a request that names nobody goes to every member.
  const to = [...selection.value.to]
  sending.value = true
  try {
    const byName = await broadcastByName(identity.name.value, api.whoami)
    const r = await api.broadcastRun(props.runId, { text: text.value, members: to, byName })
    toast.add({ ...messageToast(r), icon: 'i-lucide-megaphone' })
    if (r.sent.length) text.value = ''
  } catch (e) {
    toast.add({ title: 'Not sent', description: (e as Error).message, color: 'error', icon: 'i-lucide-triangle-alert' })
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <div v-if="ended" class="rounded-md border border-default px-3.5 py-2.5 text-sm text-muted" data-broadcast data-broadcast-ended>This run has ended.</div>
  <form v-else class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-default px-3.5 py-2.5" data-broadcast data-message-bar @submit.prevent="send">
    <span id="message-to" class="flex-none text-sm font-semibold text-highlighted">Message to</span>
    <!-- A press on a chip keeps the focus in the field (mousedown), so a line half written stays under the cursor. -->
    <div class="flex flex-wrap items-center gap-1.5" role="group" aria-labelledby="message-to" data-message-chips>
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
        />
        <UTooltip v-else :text="c.tip">
          <UBadge :color="tone(c)" :variant="tone(c) === 'neutral' ? 'outline' : 'subtle'" size="md" class="max-sm:min-h-11" :class="tone(c) === 'neutral' && 'text-muted'" aria-disabled="true" :data-message-chip="c.name" :data-chip-state="c.state">
            <StatusMark v-if="c.state === 'waiting'" :status="needs" />
            {{ c.label }}
          </UBadge>
        </UTooltip>
      </template>
    </div>
    <UInput
      v-model="text"
      size="sm"
      :placeholder="selection.placeholder"
      aria-label="Message text"
      :color="tooLong ? 'error' : undefined"
      :highlight="tooLong"
      class="min-w-48 flex-1"
      :disabled="disabled"
      data-message-field
    />
    <UButton type="submit" size="sm" :label="selection.button" trailing-icon="i-lucide-corner-down-left" :loading="sending" :disabled="!canSend" data-broadcast-send />
  </form>
</template>
