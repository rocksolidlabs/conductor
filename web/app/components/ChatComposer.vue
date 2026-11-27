<script setup lang="ts">
import type { ScopeItem } from '~/utils/chat'
import { chatBytes, chatCounter, chatTooLong, cleanChatText } from '~/utils/chat'
import { MAX_SUBMIT } from '~/utils/protocol'
import { giveFocusBack, type ReplyTaken } from '~/utils/reply'
import { RowDrafts } from '~/utils/rowDrafts'

/**
 * Where a message is written (design 2a, 2c, 2e): Enter sends, Shift+Enter is
 * a new line (on a phone the keyboard's own key), a counter shows past 1.5
 * KiB, To agent sends and types it into the agent (a controller's). On a
 * run's chat the menu beside Send says where it goes: chat only, or also
 * typed into a member (`scopeItems`), a member that cannot take it disabled
 * with why. Offline, it says messages go once the connection is back, and
 * keeps taking them.
 */
const props = withDefaults(
  defineProps<{
    disabled?: boolean
    canSendToAgent?: boolean
    offline?: boolean
    placeholder?: string
    phone?: boolean
    scopeItems?: ScopeItem[]
    /**
     * The box of a chat's agent (docs/plans/chat-or-terminal.md, design 11; UX3 R4) rather than the people's chat: one row, the field,
     * `Stop turn` while a turn runs and this viewer may stop it (`stop`), then Send. Its messages go to the agent, bound to MAX_SUBMIT
     * bytes; a line that begins with / says what the session's `verbatim` means for it; `said` is the owner's refusal of the last
     * message, in its words. `name` is the agent's, for the placeholder and Stop's tooltip. `draftKey` is the session whose line the
     * app keeps (useBoxDrafts), so that it is there again when the person comes back to this agent; without one the line is this
     * box's alone.
     *
     * `asked` is the question the session's page shows (UX3 R5): while it takes a typed reply (`reply`), a line begun as it shows is
     * bound to it (replyLine's rule, by RowDrafts) and goes as its answer (`answer`), Send reading Answer; a line begun before it came
     * stays a message. A refused answer comes back into the box, bound as it was.
     */
    agent?: { verbatim?: boolean; running: boolean; canStop: boolean; said?: string; name?: string; draftKey?: string; asked?: { id?: string; choices: boolean; reply: boolean } | null }
  }>(),
  {
    disabled: false,
    canSendToAgent: false,
    offline: false,
    placeholder: 'Message everyone here',
    phone: false,
    scopeItems: undefined,
    agent: undefined,
  },
)
/** `to` is '' for the chat alone, `agent` for this session's agent, else a run member's name. */
const emit = defineEmits<{ send: [text: string, to: string]; stop: []; answer: [text: string, question: string | undefined, taken: ReplyTaken] }>()

/** The agent's box keeps its line by session in the app's store; the people's chat keeps its own. */
const lines: RowDrafts = props.agent?.draftKey ? useBoxDrafts() : (reactive(new RowDrafts()) as RowDrafts)
const lineKey = computed(() => props.agent?.draftKey ?? '')
const people = ref('')
const draft = computed({
  get: () => (props.agent ? lines.text(lineKey.value) : people.value),
  set: (v: string) => {
    if (props.agent) lines.edit(lineKey.value, v, asking())
    else people.value = v
  },
})
/** What a line begun now is bound to: the question shown while it takes a typed reply (its id, or ASKED for one with none). */
const ASKED = '\u0000asked'
function asking(): string | undefined {
  const a = props.agent?.asked
  return a?.reply ? (a.id ?? ASKED) : undefined
}
/** The line in the box is an answer: it was begun for a question (which the owner refuses if it is no longer the one shown). */
const answering = computed(() => !!props.agent && lines.get(lineKey.value)?.question !== undefined)
const bytes = computed(() => (props.agent ? chatBytes(draft.value.replace(/\r\n/g, '\n').trim()) : chatBytes(cleanChatText(draft.value))))
/** The agent's box counts from three quarters of its bound, the people's chat from its own. */
const counter = computed(() => (props.agent ? (bytes.value < (MAX_SUBMIT * 3) / 4 ? '' : `${(bytes.value / 1024).toFixed(1)} / ${MAX_SUBMIT / 1024} KiB`) : chatCounter(bytes.value)))
const tooLong = computed(() => (props.agent ? bytes.value > MAX_SUBMIT : chatTooLong(bytes.value)))
const empty = computed(() => (props.agent ? draft.value.trim() === '' : cleanChatText(draft.value) === ''))
/** Who the agent's box writes to, in its placeholder and Stop's tooltip. */
const who = computed(() => props.agent?.name || 'the agent')
const boxPlaceholder = computed(() => {
  const a = props.agent?.asked
  if (!a?.reply) return `Message ${who.value}`
  return a.choices ? 'Pick an answer above, or type your own' : `Answer ${who.value}…`
})
/** The agent's line begins with /: what the session does with a command is said then, and only then. */
const slash = computed(() => !!props.agent && draft.value.trimStart().startsWith('/'))

/** The run chat's choice: where the next message goes; back to chat only when the member can no longer take it. */
const scope = ref('')
const chosen = computed(() => props.scopeItems?.find((s) => s.to === scope.value) ?? props.scopeItems?.[0])
watch(
  () => props.scopeItems,
  (items) => {
    if (scope.value && !items?.some((s) => s.to === scope.value && !s.disabled)) scope.value = ''
  },
)
// `member`, never `to`: a menu item's `to` is a link.
const scopeMenu = computed(() => [(props.scopeItems ?? []).map((s) => ({ label: s.label, member: s.to, detail: s.detail, disabled: s.disabled, onSelect: () => (scope.value = s.to) }))])

const form = useTemplateRef<HTMLFormElement>('form')
const input = useTemplateRef<{ textareaRef?: HTMLTextAreaElement }>('input')
function focus() {
  input.value?.textareaRef?.focus()
}
/** After a send, the cursor goes back to the field: from the field itself, Send, or nowhere; never from what else took it. */
function keepCursor() {
  if (typeof document === 'undefined') return
  const el = document.activeElement
  if (!el || el === document.body || form.value?.contains(el)) void nextTick(focus)
}

function send(to: string) {
  if (props.disabled || empty.value || tooLong.value) return
  if (answering.value) return answer()
  emit('send', draft.value, to)
  draft.value = ''
  if (props.agent) keepCursor()
}

/** The line as the question's answer, through the page's answer: it leaves the box as it goes, and a refused one comes back bound. */
function answer() {
  const key = lineKey.value
  const raw = draft.value
  const bound = lines.get(key)?.question
  const from = typeof document === 'undefined' ? null : (document.activeElement as HTMLElement | null)
  emit('answer', raw.trim(), bound === ASKED ? undefined : bound, (outcome) => {
    lines.settle(key, raw, outcome)
    if (outcome === 'refused') giveFocusBack(from)
  })
  keepCursor()
}

function onKeydown(e: KeyboardEvent) {
  // Escape leaves the agent's box, so that the page's keys (a choice's number, S, X) work again.
  if (e.key === 'Escape' && props.agent) {
    ;(e.target as HTMLElement | null)?.blur()
    return
  }
  if (e.key !== 'Enter' || e.isComposing) return
  if (e.shiftKey && !props.phone) return
  e.preventDefault()
  send(scope.value)
}

defineExpose({ focus })

/** A choice made, the person goes on typing: the menu would hand focus back to its button. */
function keepTyping(e: Event) {
  e.preventDefault()
  focus()
}
</script>

<template>
  <!-- The agent's box: one row, the field, Stop turn while a turn runs, Send; under it only what the person must know now. -->
  <form v-if="agent" ref="form" class="flex flex-col gap-1.5 px-3 py-2.5" data-chat-composer @submit.prevent="send('')">
    <div class="flex items-end gap-2">
      <UTextarea
        ref="input"
        v-model="draft"
        :placeholder="boxPlaceholder"
        :rows="1"
        autoresize
        :maxrows="6"
        :disabled="disabled"
        :aria-label="boxPlaceholder"
        :enterkeyhint="phone ? 'send' : undefined"
        class="min-w-0 flex-1"
        :ui="{ base: 'text-sm' }"
        data-chat-input
        @keydown="onKeydown"
      />
      <UTooltip v-if="agent.running && agent.canStop" :text="`Stops what ${who} is doing now. The conversation stays.`">
        <UButton label="Stop turn" icon="i-lucide-square" color="neutral" variant="outline" class="flex-none" data-chat-stop @click="emit('stop')" />
      </UTooltip>
      <UTooltip text="Enter sends · Shift+Enter for a new line" :disabled="phone">
        <UButton type="submit" :label="answering ? 'Answer' : 'Send'" icon="i-lucide-send" class="flex-none" :disabled="disabled || empty || tooLong" :data-chat-answering="answering || undefined" data-chat-send />
      </UTooltip>
    </div>
    <p v-if="slash && agent.verbatim === true" class="text-[11px] text-muted" data-chat-help="verbatim">Sent as plain text. To run a command, use the Terminal.</p>
    <p v-else-if="slash && agent.verbatim === false" class="text-[11px] text-muted" data-chat-help="commands">Messages starting with <code class="font-mono">/</code> may run as the agent's commands.</p>
    <p v-if="agent.said" class="text-[11px] text-warning" role="status" data-chat-said>{{ agent.said }}</p>
    <span v-if="counter" class="self-end font-mono text-[11px] text-muted" :class="tooLong && 'text-error'" data-mono="measure" data-chat-counter>{{ counter }}</span>
  </form>
  <form v-else class="flex flex-col gap-1.5 border-t border-default px-3 py-2.5" data-chat-composer @submit.prevent="send(scope)">
    <p v-if="offline" class="text-[11px] text-warning" data-chat-offline>Messages send once you are back online</p>
    <UTextarea
      ref="input"
      v-model="draft"
      :placeholder="placeholder"
      :rows="1"
      autoresize
      :maxrows="6"
      :disabled="disabled"
      aria-label="Message everyone here"
      :enterkeyhint="phone ? 'send' : undefined"
      class="w-full"
      :ui="{ base: 'text-sm' }"
      data-chat-input
      @keydown="onKeydown"
    />
    <div class="flex items-center gap-2 text-[11px] text-muted">
      <span v-if="!phone">Enter sends · Shift+Enter new line</span>
      <span v-if="counter" class="font-mono" :class="tooLong && 'text-error'" data-mono="measure" data-chat-counter>{{ counter }}</span>
      <span class="flex-1" />
      <UDropdownMenu v-if="scopeItems" :items="scopeMenu" :content="{ align: 'end', onCloseAutoFocus: keepTyping }">
        <UButton :label="chosen?.label ?? 'Chat only'" trailing-icon="i-lucide-chevron-down" size="xs" color="neutral" variant="outline" :disabled="disabled" data-chat-scope-menu />
        <template #item-label="{ item }">
          <span class="flex flex-col" :data-chat-scope-item="item.member">
            <span>{{ item.label }}</span>
            <span class="text-[11px] text-muted">{{ item.detail }}</span>
          </span>
        </template>
      </UDropdownMenu>
      <UButton v-else-if="canSendToAgent" label="To agent" icon="i-lucide-corner-down-right" size="xs" color="neutral" variant="outline" :disabled="disabled || empty || tooLong" data-chat-to-agent @click="send('agent')" />
      <UButton type="submit" label="Send" icon="i-lucide-send" size="xs" :disabled="disabled || empty || tooLong" data-chat-send />
    </div>
  </form>
</template>
