<script setup lang="ts">
import type { ChatThread } from '~/composables/useChat'
import type { ChatMessage, Role, ViewerInfo } from '~/utils/protocol'
import { canSendToAgent, type ScopeItem } from '~/utils/chat'

/**
 * The chat beside a terminal (design 2a, 2d, 2h): who is here, the thread,
 * the composer; empty, offline and ended states. `note` is the line a
 * view-only guest reads above the composer; `bare` drops the header row for
 * a sheet that has its own (design 2c).
 */
const props = withDefaults(
  defineProps<{
    thread: ChatThread
    role: Role
    ended?: boolean
    offline?: boolean
    viewers?: ViewerInfo[]
    note?: string
    scope?: 'session' | 'run'
    phone?: boolean
    bare?: boolean
    /** A run's chat: the composer's menu of where a message goes, and the members a kept message can be sent to. */
    scopeItems?: ScopeItem[]
    sendTargets?: ScopeItem[]
    /**
     * Whether a question's choices may be pressed here, beside the role: a run's chat offers them only for a question one of its
     * running members asked, which the page can answer through (useRunChatAnswer); the others' stay as text.
     */
    answerable?: (m: ChatMessage) => boolean
  }>(),
  {
    ended: false,
    offline: false,
    viewers: () => [],
    note: '',
    scope: 'session',
    phone: false,
    bare: false,
    scopeItems: undefined,
    sendTargets: undefined,
    answerable: undefined,
  },
)
/** `to` is '' for the chat alone, `agent` for the session's agent, else a run member's name; `answer` is a question's choice. */
const emit = defineEmits<{ send: [text: string, to: string]; sendToAgent: [ref: string]; sendTo: [ref: string, to: string]; retry: [nonce: string]; answer: [m: ChatMessage, index: number] }>()

const agentOk = computed(() => canSendToAgent(props.role, props.ended))
const here = computed(() => props.viewers.length)
const words = computed(() => (props.scope === 'run' ? `everyone on this run's link sees it` : props.role === 'control' && !props.note ? 'everyone on this session sees the chat' : 'everyone on this link sees it'))
const empty = computed(() => props.thread.messages.every((m) => m.kind === 'system') && !props.thread.pending.length)
</script>

<template>
  <div class="flex h-full min-h-0 flex-col" data-chat :data-chat-scope="scope">
    <div v-if="!bare" class="flex items-center gap-2 border-b border-default px-4 py-2.5 text-xs text-muted">
      <ViewerAvatars :viewers="viewers" :max="3" />
      <span class="min-w-0 leading-snug"><template v-if="here">{{ here }} here · </template>{{ words }}</span>
    </div>
    <p v-if="offline && !ended" class="flex items-center gap-2 border-b border-default bg-elevated/60 px-4 py-2 text-xs text-muted" data-chat-offline-banner><UIcon name="i-lucide-loader-circle" class="size-3.5 animate-spin" />Offline. Reconnecting…</p>
    <p v-if="empty && !ended" class="px-4 pt-6 text-center text-sm text-muted" data-chat-empty>
      Nobody has said anything.<br />
      <template v-if="scope === 'run'">Everyone on this run's link sees this chat.</template>
      <template v-else>Everyone on this link sees this chat.</template>
    </p>
    <ChatThread
      :messages="thread.messages"
      :pending="thread.pending"
      :can-send-to-agent="agentOk"
      :can-answer="role === 'control' && !ended"
      :answerable="answerable"
      :send-targets="sendTargets"
      @send-to-agent="emit('sendToAgent', $event)"
      @send-to="(ref, to) => emit('sendTo', ref, to)"
      @retry="emit('retry', $event)"
      @answer="(m, i) => emit('answer', m, i)"
    />
    <div v-if="ended" class="border-t border-default px-4 py-3 text-xs text-muted" data-chat-ended>
      <template v-if="scope === 'run'">
        <p class="font-medium text-default">This run ended</p>
        <p>The chat is read-only and stays in the run's record.</p>
      </template>
      <template v-else>
        <p class="font-medium text-default">This session ended</p>
        <p>The chat is read-only and goes with the session. A run's chat stays in its record.</p>
      </template>
    </div>
    <template v-else>
      <p v-if="note" class="border-t border-default px-4 py-2 text-[11px] text-muted" data-chat-note>{{ note }}</p>
      <ChatComposer :can-send-to-agent="agentOk" :offline="offline" :phone="phone" :scope-items="agentOk ? scopeItems : undefined" @send="(text, to) => emit('send', text, to)" />
    </template>
  </div>
</template>
