<script setup lang="ts">
import type { ScopeItem } from '~/utils/chat'
import { chatBytes, chatCounter, chatTooLong, cleanChatText } from '~/utils/chat'
import { MAX_SUBMIT } from '~/utils/protocol'

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
     * The box of a chat's agent (docs/plans/chat-or-terminal.md, design 11) rather than the people's chat: its messages go to the agent,
     * bound to MAX_SUBMIT bytes; its help line says what the session's `verbatim` means; Stop (`stop`) shows while a turn runs and
     * this viewer may stop it; `said` is the owner's refusal of the last message, in its words.
     */
    agent?: { verbatim?: boolean; running: boolean; canStop: boolean; said?: string }
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
const emit = defineEmits<{ send: [text: string, to: string]; stop: [] }>()

const draft = ref('')
const bytes = computed(() => (props.agent ? chatBytes(draft.value.replace(/\r\n/g, '\n').trim()) : chatBytes(cleanChatText(draft.value))))
/** The agent's box counts from three quarters of its bound, the people's chat from its own. */
const counter = computed(() => (props.agent ? (bytes.value < (MAX_SUBMIT * 3) / 4 ? '' : `${(bytes.value / 1024).toFixed(1)} / ${MAX_SUBMIT / 1024} KiB`) : chatCounter(bytes.value)))
const tooLong = computed(() => (props.agent ? bytes.value > MAX_SUBMIT : chatTooLong(bytes.value)))
const empty = computed(() => (props.agent ? draft.value.trim() === '' : cleanChatText(draft.value) === ''))

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

function send(to: string) {
  if (props.disabled || empty.value || tooLong.value) return
  emit('send', draft.value, to)
  draft.value = ''
}

function onKeydown(e: KeyboardEvent) {
  if (e.key !== 'Enter' || e.isComposing) return
  if (e.shiftKey && !props.phone) return
  e.preventDefault()
  send(scope.value)
}

defineExpose({ focus: () => input.value?.textareaRef?.focus() })
const input = useTemplateRef<{ textareaRef?: HTMLTextAreaElement }>('input')

/** A choice made, the person goes on typing: the menu would hand focus back to its button. */
function keepTyping(e: Event) {
  e.preventDefault()
  input.value?.textareaRef?.focus()
}
</script>

<template>
  <form class="flex flex-col gap-1.5 border-t border-default px-3 py-2.5" data-chat-composer @submit.prevent="send(scope)">
    <p v-if="offline" class="text-[11px] text-warning" data-chat-offline>Messages send once you are back online</p>
    <UTextarea
      ref="input"
      v-model="draft"
      :placeholder="placeholder"
      :rows="1"
      autoresize
      :maxrows="6"
      :disabled="disabled"
      :aria-label="agent ? placeholder : 'Message everyone here'"
      :enterkeyhint="phone ? 'send' : undefined"
      class="w-full"
      :ui="{ base: 'text-sm' }"
      data-chat-input
      @keydown="onKeydown"
    />
    <!-- The agent's box: one help line by the session's verbatim (design 11), and the owner's word on a message it refused. -->
    <p v-if="agent && agent.verbatim === true" class="text-[11px] text-muted" data-chat-help="verbatim">Sent as written: <code class="font-mono">@file</code> and <code class="font-mono">/command</code> are not expanded here.</p>
    <p v-else-if="agent && agent.verbatim === false" class="text-[11px] text-muted" data-chat-help="commands">Messages starting with <code class="font-mono">/</code> may run as the agent's commands.</p>
    <p v-if="agent?.said" class="text-[11px] text-warning" role="status" data-chat-said>{{ agent.said }}</p>
    <div class="flex items-center gap-2 text-[11px] text-muted">
      <span v-if="!phone">Enter sends · Shift+Enter new line</span>
      <span v-if="counter" class="font-mono" :class="tooLong && 'text-error'" data-chat-counter>{{ counter }}</span>
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
      <IconButton v-if="agent?.running && agent.canStop" icon="i-lucide-square" size="xs" color="neutral" variant="outline" label="Stop this turn" data-chat-stop @click="emit('stop')" />
      <UButton type="submit" label="Send" icon="i-lucide-send" size="xs" :disabled="disabled || empty || tooLong" data-chat-send />
    </div>
  </form>
</template>
