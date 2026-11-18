<script setup lang="ts">
import type { PendingChat } from '~/composables/useChat'
import type { ScopeItem } from '~/utils/chat'
import type { ChatMessage } from '~/utils/protocol'
import { MODE_CHOICE, choiceKey } from '~/utils/attention'
import { avatarTone } from '~/utils/avatar'
import { chatAnswers, chatTime, linkify, markerLine, systemLine } from '~/utils/chat'
import { agentInitials, initials } from '~/utils/sessions'
import { quoteLocation, relocationWords, type Relocation } from '~/utils/quote'

/**
 * The messages of a chat (design 2a): a person's avatar, name, a `view` tag
 * for a view-only person, the time, the text with its links; a quiet line
 * for a join or leave; a marker where a message was typed into the agent;
 * this browser's own messages on their way, and the ones that failed with
 * Retry. Hovering a message offers Send to agent to a controller; on a run's
 * chat (design 2e) a menu of the members instead (`sendTargets`), and an
 * `on <member>` chip says which member the sender was looking at. The
 * agent's question is a row of its own, amber-tinted, its choices as
 * buttons a controller answers with (`canAnswer`) until a line says it was
 * answered; then, and for anyone who cannot answer, the choices stay as
 * plain text, the one picked marked, and a typed answer under them (#364).
 */
const props = withDefaults(
  defineProps<{ messages: ChatMessage[]; pending: PendingChat[]; canSendToAgent: boolean; sendTargets?: ScopeItem[]; canAnswer?: boolean; answerable?: (m: ChatMessage) => boolean }>(),
  { sendTargets: undefined, canAnswer: false, answerable: undefined },
)
/** A question's choices as buttons: for a controller, until a line says it was answered, and only where the page can answer it. */
function offered(m: ChatMessage): boolean {
  return !!m.options?.length && props.canAnswer && !answers.value.has(m.id) && (props.answerable?.(m) ?? true)
}
const emit = defineEmits<{ sendToAgent: [ref: string]; sendTo: [ref: string, to: string]; retry: [nonce: string]; answer: [m: ChatMessage, index: number] }>()
const answers = computed(() => chatAnswers(props.messages))
/** Whether the question's choice i is the one answered with: its label is what the answered line says. */
function picked(m: ChatMessage, i: number): boolean {
  const a = answers.value.get(m.id)
  return a?.how === 'answered' && !!a.text && m.options?.[i]?.label === a.text
}
// A line this browser typed as the answer, shown to its own person alone: the owner repeats no typed line in the chat (#364).
const typed = useTypedAnswers()
function typedAnswer(m: ChatMessage): string {
  return typed.of(m.id)
}
// A comment on lines of a file (design round 12, F7): the card shows the lines; where the page has an editor, Open goes there and a
// note says when the lines moved or changed since.
const quotes = useChatQuotes()
const where = reactive(new Map<string, Relocation | null>())
watch(
  () => props.messages.filter((m) => m.quote).map((m) => m.id),
  (ids) => {
    if (!quotes) return
    for (const m of props.messages) {
      if (!m.quote || !ids.includes(m.id) || where.has(m.id)) continue
      where.set(m.id, null)
      void quotes.relocate(m.quote).then((r) => where.set(m.id, r))
    }
  },
  { immediate: true },
)
function openQuote(m: ChatMessage) {
  if (!m.quote || !quotes) return
  const r = where.get(m.id)
  quotes.open(m.quote, r?.state === 'moved' ? { from: r.from, to: r.to } : undefined)
}

function targetsFor(m: ChatMessage) {
  // `member`, never `to`: a menu item's `to` is a link.
  return [(props.sendTargets ?? []).filter((t) => t.to).map((t) => ({ label: t.to, member: t.to, detail: t.detail, disabled: t.disabled, onSelect: () => emit('sendTo', m.id, t.to) }))]
}

const scroller = useTemplateRef<HTMLElement>('scroller')
let stick = true
function onScroll() {
  const el = scroller.value
  if (!el) return
  stick = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}
watch(
  () => [props.messages.length, props.pending.length],
  async () => {
    if (!stick) return
    await nextTick()
    const el = scroller.value
    if (el) el.scrollTop = el.scrollHeight
  },
  { flush: 'post' },
)
onMounted(() => {
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
})
</script>

<template>
  <div ref="scroller" class="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-3" data-chat-thread @scroll.passive="onScroll">
    <template v-for="m in messages" :key="m.id">
      <p v-if="m.kind === 'system'" class="text-center text-[11px] text-muted" :data-chat-message="m.id" data-chat-kind="system" data-chat-system>{{ systemLine(m) }} · {{ chatTime(m.at) }}</p>
      <p v-else-if="m.kind === 'sent_to_agent'" class="flex items-center gap-1.5 pl-9 text-[11px] text-muted" :data-chat-message="m.id" data-chat-kind="sent_to_agent" data-chat-marker>
        <UIcon name="i-lucide-corner-down-right" class="size-3 flex-none" aria-hidden="true" />{{ markerLine(m) }}
      </p>
      <div v-else-if="m.kind === 'question'" class="flex gap-2.5" :data-chat-message="m.id" data-chat-kind="question" data-chat-question>
        <span class="grid size-7 flex-none place-items-center rounded-md bg-warning/15 font-mono text-[11px] font-semibold text-warning">{{ agentInitials(m.by.name) }}</span>
        <div class="flex min-w-0 flex-1 flex-col gap-1">
          <div class="flex items-center gap-1.5 text-xs">
            <span class="truncate font-semibold text-highlighted">{{ m.by.name }}</span>
            <span class="text-muted">asks</span>
            <span v-if="m.on" class="truncate rounded-sm bg-elevated px-1 text-[10px] text-muted" :data-chat-on="m.on">on {{ m.on }}</span>
            <span class="font-mono text-[11px] text-muted">{{ chatTime(m.at) }}</span>
          </div>
          <p class="whitespace-pre-wrap break-words text-sm">{{ m.text }}</p>
          <div v-if="offered(m)" class="flex flex-wrap gap-1.5">
            <UButton
              v-for="(o, i) in m.options"
              :key="i"
              :label="o.label"
              size="xs"
              :color="o.mode ? 'warning' : 'neutral'"
              variant="outline"
              :title="o.mode ? MODE_CHOICE : undefined"
              :data-chat-option="i + 1"
              :data-choice-mode="o.mode || undefined"
              @click="emit('answer', m, i)"
            >
              <template v-if="choiceKey(o, i)" #leading><UKbd :value="String(choiceKey(o, i))" size="sm" class="opacity-70" /></template>
            </UButton>
          </div>
          <!-- The choices as text once the question takes no answer, for a viewer, or where the page cannot answer it: the one picked
               is marked (#364). -->
          <div v-else-if="m.options?.length" class="flex flex-wrap gap-1.5" data-chat-choices>
            <span
              v-for="(o, i) in m.options"
              :key="i"
              class="inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-[11px]"
              :class="picked(m, i) ? 'border-warning/50 bg-warning/15 font-medium text-warning' : 'border-default text-muted'"
              :data-chat-choice="i + 1"
              :data-chat-chosen="picked(m, i) || undefined"
            >
              <UIcon v-if="picked(m, i)" name="i-lucide-check" class="size-3 flex-none" aria-hidden="true" />{{ o.label }}
            </span>
          </div>
          <p v-if="typedAnswer(m)" class="whitespace-pre-wrap break-words text-xs text-muted" data-chat-answer><span class="font-semibold text-highlighted">You</span> answered: {{ typedAnswer(m) }}</p>
        </div>
      </div>
      <div v-else class="group flex gap-2.5" :data-chat-message="m.id" data-chat-kind="message">
        <span class="grid size-7 flex-none place-items-center rounded-full text-[11px] font-semibold" :class="avatarTone(m.by.name)">{{ initials(m.by.name) }}</span>
        <div class="flex min-w-0 flex-1 flex-col gap-0.5">
          <div class="flex items-center gap-1.5 text-xs">
            <span class="truncate font-semibold text-highlighted">{{ m.by.name }}</span>
            <span v-if="m.by.role === 'view'" class="rounded-sm border border-default px-1 text-[10px] text-muted" data-chat-view-tag>view</span>
            <span class="font-mono text-[11px] text-muted">{{ chatTime(m.at) }}</span>
            <span v-if="m.on" class="truncate rounded-sm bg-elevated px-1 text-[10px] text-muted" :data-chat-on="m.on">on {{ m.on }}</span>
            <UDropdownMenu v-if="canSendToAgent && sendTargets && (m.text || m.quote)" :items="targetsFor(m)" :content="{ align: 'end' }">
              <UButton label="Send to…" icon="i-lucide-corner-down-right" size="xs" color="neutral" variant="ghost" class="ml-auto opacity-0 transition-opacity focus-visible:opacity-100 group-hover:opacity-100" data-chat-send-to />
              <template #item-label="{ item }">
                <span class="flex flex-col" :data-chat-send-to-item="item.member">
                  <span>{{ item.label }}</span>
                  <span class="text-[11px] text-muted">{{ item.detail }}</span>
                </span>
              </template>
            </UDropdownMenu>
            <UButton
              v-else-if="canSendToAgent && (m.text || m.quote)"
              label="Send to agent"
              icon="i-lucide-corner-down-right"
              size="xs"
              color="neutral"
              variant="ghost"
              class="ml-auto opacity-0 transition-opacity focus-visible:opacity-100 group-hover:opacity-100"
              data-chat-send-to-agent
              @click="emit('sendToAgent', m.id)"
            />
          </div>
          <div v-if="m.quote" class="mt-1 overflow-hidden rounded-md border border-default bg-elevated/40 text-xs" :data-chat-quote="quoteLocation(m.quote)">
            <div class="flex items-center gap-2 border-b border-default px-2 py-1">
              <UIcon name="i-lucide-file-code" class="size-3.5 flex-none text-muted" />
              <span class="min-w-0 flex-1 truncate font-mono text-muted">{{ quoteLocation(m.quote) }}</span>
              <UButton v-if="quotes" label="Open" icon="i-lucide-arrow-up-right" size="xs" color="neutral" variant="ghost" data-chat-quote-open @click="openQuote(m)" />
            </div>
            <pre class="overflow-x-auto px-2 py-1 font-mono text-[11px] leading-snug" data-chat-quote-lines><template v-for="(l, i) in m.quote.lines" :key="i"><span class="select-none text-muted">{{ String(m.quote.from + i).padStart(3, ' ') }}  </span>{{ l }}
</template></pre>
            <p v-if="m.quote.cut" class="px-2 pb-1 text-[11px] text-muted">More lines than shown.</p>
            <p v-if="relocationWords(where.get(m.id))" class="border-t border-default px-2 py-1 text-[11px] text-warning" data-chat-quote-moved>{{ relocationWords(where.get(m.id)) }}</p>
          </div>
          <p v-if="m.text" class="whitespace-pre-wrap break-words text-sm">
            <template v-for="(seg, i) in linkify(m.text ?? '')" :key="i">
              <a v-if="seg.href" :href="seg.href" target="_blank" rel="noopener noreferrer" class="break-all text-primary underline underline-offset-2">{{ seg.text }}</a>
              <template v-else>{{ seg.text }}</template>
            </template>
          </p>
        </div>
      </div>
    </template>
    <div v-for="p in pending" :key="p.nonce" class="flex gap-2.5 opacity-80" :data-chat-pending="p.nonce" :data-chat-pending-state="p.state">
      <span class="grid size-7 flex-none place-items-center rounded-full bg-elevated text-[11px] font-semibold text-muted">…</span>
      <div class="flex min-w-0 flex-1 flex-col gap-0.5">
        <p v-if="p.quote" class="truncate font-mono text-[11px] text-muted">{{ quoteLocation(p.quote) }}</p>
        <p class="whitespace-pre-wrap break-words text-sm">{{ p.text }}</p>
        <p v-if="p.state === 'failed'" class="flex items-center gap-2 text-[11px] text-warning">
          Not sent<template v-if="p.error"> · {{ p.error }}</template>
          <UButton label="Retry" size="xs" color="neutral" variant="link" class="p-0" data-chat-retry @click="emit('retry', p.nonce)" />
        </p>
        <p v-else-if="p.state === 'queued'" class="text-[11px] text-muted">Waiting for the connection…</p>
      </div>
    </div>
  </div>
</template>
