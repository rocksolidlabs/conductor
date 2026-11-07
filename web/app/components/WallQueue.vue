<script setup lang="ts">
import type { SessionInfo } from '~/composables/useSessions'
import { MODE_CHOICE, decisionOf, firstChoice, noReply, offeredChoices } from '~/utils/attention'
import { agentInitials, relativeTime } from '~/utils/sessions'
import { draftQuestion, questionOf } from '~/utils/reply'

const props = defineProps<{ sessions: SessionInfo[]; answered: SessionInfo[]; selected: number; sending: Set<string> }>()
/** A reply names the question its card showed (its id), so that one answered meanwhile takes nothing. */
const emit = defineEmits<{ reply: [session: SessionInfo, text: string, question: string | undefined]; option: [session: SessionInfo, index: number, question: string | undefined]; open: [session: SessionInfo]; select: [index: number] }>()

const drafts = reactive<Record<string, string>>({})
/**
 * The question each card's line was begun for (as its first character was typed): it names that one when sent, whatever the card
 * shows by then, so that the owner refuses it rather than take it for the next question.
 */
const draftQuestions: Record<string, string | undefined> = {}
function setDraft(s: SessionInfo, value: string) {
  draftQuestions[s.id] = draftQuestion(drafts[s.id] ?? '', value, draftQuestions[s.id], s.attention)
  drafts[s.id] = value
}
const now = ref(Date.now())
let tick: number | undefined
onMounted(() => (tick = window.setInterval(() => (now.value = Date.now()), 1000)))
onBeforeUnmount(() => window.clearInterval(tick))

const inputs = new Map<string, { inputRef?: HTMLInputElement }>()
function inputRef(id: string) {
  return (el: unknown) => {
    if (el) inputs.set(id, el as { inputRef?: HTMLInputElement })
    else inputs.delete(id)
  }
}

function submit(s: SessionInfo) {
  const text = (drafts[s.id] ?? '').trim()
  // A question that takes no typed reply (noReply: the terminal's, or a durable decision's) is sent none.
  if (!text || noReply(s.attention) !== undefined) return
  emit('reply', s, text, draftQuestions[s.id])
  drafts[s.id] = ''
}

function agentName(s: SessionInfo) {
  const names: Record<string, string> = { claude: 'Claude Code', codex: 'Codex', agy: 'Antigravity' }
  return names[s.agentId] ?? s.agentId
}

/** Puts the keyboard into the selected card's reply box (Enter on the wall). */
function focusSelected() {
  const s = props.sessions[props.selected]
  if (s) inputs.get(s.id)?.inputRef?.focus()
}

defineExpose({ focusSelected })
</script>

<template>
  <aside class="flex h-full min-h-0 w-[340px] flex-none flex-col gap-3 overflow-y-auto border-r border-default p-4" data-wall-queue>
    <h3 class="flex items-center text-[11px] font-semibold uppercase tracking-wider" :class="sessions.length ? 'text-warning' : 'text-muted'">
      Queue · {{ sessions.length ? `${sessions.length} waiting` : 'nothing waiting' }}
      <span class="ml-auto flex items-center gap-1 normal-case tracking-normal"><UKbd value="J" size="sm" /><span class="text-muted">/</span><UKbd value="K" size="sm" /></span>
    </h3>

    <div
      v-for="(s, i) in sessions"
      :key="s.id"
      class="flex flex-col gap-2.5 rounded-md border bg-elevated/40 p-3.5 transition-shadow"
      :class="[i === selected ? 'border-warning ring-2 ring-warning/40' : 'border-default']"
      :data-queue-session="s.id"
      @click="emit('select', i)"
    >
      <div class="flex items-center gap-2">
        <span class="grid size-5.5 place-items-center rounded bg-primary font-mono text-[9.5px] font-semibold text-inverted">{{ agentInitials(s.agentId) }}</span>
        <button type="button" class="truncate text-sm font-semibold hover:underline" @click.stop="emit('open', s)">{{ s.name }}</button>
        <span v-if="s.attention?.since" class="ml-auto font-mono text-[11px] text-muted">{{ relativeTime(s.attention.since, now) }}</span>
      </div>
      <!-- The question whole (#187): a long token (a command, a URL) breaks rather than scrolls the queue sideways. -->
      <p class="break-words text-sm leading-snug" data-queue-message>{{ s.attention?.message || 'Waiting for input' }}</p>
      <!-- A durable decision's question takes no reply here: the card says where it is answered (noReply). -->
      <p v-if="decisionOf(s.attention)" class="text-xs text-muted" data-queue-decision>{{ noReply(s.attention) }}</p>
      <div v-else-if="offeredChoices(s.attention).length" class="flex gap-1.5">
        <UButton
          v-for="(o, oi) in offeredChoices(s.attention)"
          :key="oi"
          :label="o.label"
          size="xs"
          :variant="oi === firstChoice(offeredChoices(s.attention)) ? 'solid' : 'outline'"
          :color="oi === firstChoice(offeredChoices(s.attention)) ? 'primary' : o.mode ? 'warning' : 'neutral'"
          :title="o.mode ? MODE_CHOICE : undefined"
          :data-choice-mode="o.mode || undefined"
          class="flex-1 justify-center"
          :loading="sending.has(s.id)"
          @click.stop="emit('option', s, oi, questionOf(s.attention))"
        />
      </div>
      <form v-else class="flex items-center gap-1.5" @submit.prevent="submit(s)">
        <UInput
          :ref="inputRef(s.id)"
          :model-value="drafts[s.id]"
          size="sm"
          class="flex-1"
          :placeholder="noReply(s.attention) ?? `Reply to ${agentName(s)}…`"
          :disabled="noReply(s.attention) !== undefined"
          :loading="sending.has(s.id)"
          @update:model-value="(v: string | number) => setDraft(s, String(v ?? ''))"
          @click.stop
          @focus="emit('select', i)"
        />
        <UButton type="submit" size="sm" icon="i-lucide-corner-down-left" color="neutral" variant="outline" aria-label="Send" :disabled="noReply(s.attention) !== undefined || !(drafts[s.id] ?? '').trim()" />
      </form>
    </div>

    <template v-if="answered.length">
      <h3 class="mt-1 text-[11px] font-semibold uppercase tracking-wider text-muted">Answered</h3>
      <ol class="flex flex-col gap-1 text-xs text-muted">
        <li v-for="s in answered" :key="s.id" :title="s.lastAnswer?.message">
          <span class="text-default">{{ s.lastAnswer?.byName || 'Someone' }}</span> answered <button type="button" class="text-default hover:underline" @click="emit('open', s)">{{ s.name }}</button>
          <span v-if="s.lastAnswer"> · {{ relativeTime(s.lastAnswer.at, now) }}</span>
        </li>
      </ol>
    </template>
  </aside>
</template>
