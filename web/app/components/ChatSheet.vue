<script setup lang="ts">
import type { ChatThread } from '~/composables/useChat'
import type { ScopeItem } from '~/utils/chat'
import type { ChatMessage, Role, ViewerInfo } from '~/utils/protocol'
import { SHEET_SNAPS } from '~/utils/chat'

/**
 * The chat where there is no room for the panel beside the terminal (design
 * 2c), and a run's chat beside its tiles (2e, 2f): on a phone a sheet from
 * the bottom, two thirds of the screen and dragging to full height, the
 * terminal live behind it; wider, a slideover from the right. Swipe down,
 * Escape or × closes it. The header names who is here, so the panel inside
 * goes without its own. The drawer itself is as tall as the window allows
 * and slid down to its snap point (vaul writes the slide as
 * `--snap-point-height`), so the body takes what is left above the window's
 * edge, less the handle, and the composer sits at the visible bottom at
 * either height.
 */
withDefaults(
  defineProps<{
    thread: ChatThread
    role: Role
    ended?: boolean
    offline?: boolean
    viewers?: ViewerInfo[]
    note?: string
    scope?: 'session' | 'run'
    title?: string
    description?: string
    scopeItems?: ScopeItem[]
    sendTargets?: ScopeItem[]
    /** Whether a question's choices may be pressed here, beside the role (ChatPanel's `answerable`). */
    answerable?: (m: ChatMessage) => boolean
  }>(),
  {
    ended: false,
    offline: false,
    viewers: () => [],
    note: '',
    scope: 'session',
    title: 'Chat',
    description: '',
    scopeItems: undefined,
    sendTargets: undefined,
    answerable: undefined,
  },
)
const emit = defineEmits<{ send: [text: string, to: string]; sendTo: [ref: string, to: string]; retry: [nonce: string]; answer: [m: ChatMessage, index: number] }>()
const open = defineModel<boolean>('open', { default: false })
const phone = useIsPhone()
const SNAPS = [...SHEET_SNAPS]
/** The drawer's height above the window's edge, less its handle (a 1rem margin and the bar). */
const BODY_HEIGHT = 'calc(100% - var(--snap-point-height, 0px) - 1.375rem)'
</script>

<template>
  <UDrawer v-if="phone" v-model:open="open" direction="bottom" :snap-points="SNAPS" :overlay="false" :modal="false" handle>
    <template #content>
      <div class="flex w-full flex-col overflow-hidden" :style="{ height: BODY_HEIGHT }" data-chat-sheet :data-run-chat="scope === 'run' ? '' : undefined">
        <div class="flex min-h-8 items-center gap-2 border-b border-default px-4 py-2">
          <span class="flex min-w-0 flex-1 flex-col">
            <span class="font-semibold text-highlighted">{{ title }}</span>
            <span v-if="description" class="truncate text-[11px] text-muted">{{ description }}</span>
          </span>
          <ViewerAvatars :viewers="viewers" :max="3" />
          <span v-if="viewers.length" class="text-xs text-muted">{{ viewers.length }} here</span>
          <IconButton icon="i-lucide-x" color="neutral" variant="ghost" label="Close the chat" data-chat-close @click="open = false" />
        </div>
        <ChatPanel
          :thread="thread"
          :role="role"
          :ended="ended"
          :offline="offline"
          :viewers="viewers"
          :note="note"
          :scope="scope"
          :scope-items="scopeItems"
          :send-targets="sendTargets"
          :answerable="answerable"
          bare
          phone
          @send="(text, to) => emit('send', text, to)"
          @send-to="(ref, to) => emit('sendTo', ref, to)"
          @retry="emit('retry', $event)"
          @answer="(m, i) => emit('answer', m, i)"
        />
      </div>
    </template>
  </UDrawer>
  <USlideover v-else v-model:open="open" side="right" :title="title" :description="description || undefined" :ui="{ content: 'sm:max-w-md', body: 'flex min-h-0 flex-1 flex-col p-0 sm:p-0' }">
    <template #actions>
      <ViewerAvatars :viewers="viewers" :max="3" />
      <span v-if="viewers.length" class="text-xs text-muted">{{ viewers.length }} here</span>
    </template>
    <template #close>
      <IconButton icon="i-lucide-x" color="neutral" variant="ghost" label="Close the chat" data-chat-close />
    </template>
    <template #body>
      <div class="flex min-h-0 flex-1 flex-col" data-chat-sheet :data-run-chat="scope === 'run' ? '' : undefined">
        <ChatPanel
          :thread="thread"
          :role="role"
          :ended="ended"
          :offline="offline"
          :viewers="viewers"
          :note="note"
          :scope="scope"
          :scope-items="scopeItems"
          :send-targets="sendTargets"
          :answerable="answerable"
          bare
          @send="(text, to) => emit('send', text, to)"
          @send-to="(ref, to) => emit('sendTo', ref, to)"
          @retry="emit('retry', $event)"
          @answer="(m, i) => emit('answer', m, i)"
        />
      </div>
    </template>
  </USlideover>
</template>
