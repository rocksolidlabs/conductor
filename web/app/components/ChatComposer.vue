<script setup lang="ts">
import type { ScopeItem } from '~/utils/chat'
import { chatBytes, chatCounter, chatTooLong, cleanChatText } from '~/utils/chat'
import { MAX_SUBMIT } from '~/utils/protocol'
import { giveFocusBack, type ReplyTaken } from '~/utils/reply'
import { OwnLines, RowDrafts } from '~/utils/rowDrafts'
import { expandsLine, freeHeld, markInput } from '~/utils/boxLine'
import { fileRows, mentionAt, outsideMention, pickMention, type Mention, type MenuRow } from '~/utils/chatBoxMenu'

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
     *
     * `expand` is the server's word that this connection may send a line for the agent to expand (UX3 R10): the agent's commands and
     * files then; `folder` is the session's folder, for the line about a file outside it. A line goes for expand only when the person
     * typed or pasted it, or picked from the list, themselves (OwnLines); `send`'s third argument says so, and hands back a refused
     * line (`back`).
     */
    agent?: { verbatim?: boolean; running: boolean; canStop: boolean; said?: string; name?: string; draftKey?: string; asked?: { id?: string; choices: boolean; reply: boolean } | null; expand?: boolean; folder?: string }
    /**
     * The agent's box's files for `@` (UX3 R6): the words typed after it ('' for the files the agent touched) to the paths from the
     * folder, and whether more match than came; rejected when the read was refused. None where this connection may not read files:
     * no list then.
     */
    files?: (words: string) => Promise<{ paths: string[]; more: boolean }>
  }>(),
  {
    disabled: false,
    canSendToAgent: false,
    offline: false,
    placeholder: 'Message everyone here',
    phone: false,
    scopeItems: undefined,
    agent: undefined,
    files: undefined,
  },
)
/** `to` is '' for the chat alone, `agent` for this session's agent, else a run member's name. */
const emit = defineEmits<{ send: [text: string, to: string, how?: { own: boolean; back: (text: string) => void }]; stop: []; answer: [text: string, question: string | undefined, taken: ReplyTaken] }>()

/** The agent's box keeps its line by session in the app's store; the people's chat keeps its own. */
const lines: RowDrafts = props.agent?.draftKey ? useBoxDrafts() : (reactive(new RowDrafts()) as RowDrafts)
const lineKey = computed(() => props.agent?.draftKey ?? '')
/** Whose the line in the box is: the person's own typing or pasting (utils/boxLine.ts), or their own pick; else the page's. */
const own: OwnLines = props.agent?.draftKey ? useOwnLines() : (reactive(new OwnLines()) as OwnLines)
function onInput(e: Event) {
  markInput(own, lineKey.value, e as InputEvent, (e.target as HTMLTextAreaElement | null)?.value ?? '')
  noteCaret()
}
/** The line in the box is the person's own, as it stands now. */
const mineNow = computed(() => own.is(lineKey.value, draft.value))
const people = ref('')
const draft = computed({
  get: () => (props.agent ? lines.text(lineKey.value) : people.value),
  set: (v: string) => {
    if (!props.agent) {
      people.value = v
      return
    }
    note.value = ''
    // An emptied box holds nobody's line.
    if (v === '') own.set(lineKey.value)
    // A line kept for an answered question rebinds only to a question that takes a typed reply: with none shown, it stays bound to
    // its own and goes nowhere until the person empties the box (UX3 R5: what was typed as an answer never becomes a message).
    const d = lines.get(lineKey.value)
    const now = asking()
    lines.edit(lineKey.value, v, d?.kept && now === undefined ? d.question : now)
  },
})
/** Why the line in the box goes nowhere, or goes otherwise than it was begun, said under it until the next edit. */
const note = ref('')
/** What a line begun now is bound to: the question shown while it takes a typed reply (its id, or ASKED for one with none). */
const ASKED = '\u0000asked'
/** The questions this box has sent an answer to: a line begun while one still shows is a message, never a second answer to it. */
const answeredHere = new Set<string>()
function asking(): string | undefined {
  const a = props.agent?.asked
  if (!a?.reply) return undefined
  const id = a.id ?? ASKED
  return answeredHere.has(id) ? undefined : id
}
/** The line in the box is an answer: it was begun for a question (which the owner refuses if it is no longer the one shown). */
const answering = computed(() => !!props.agent && lines.get(lineKey.value)?.question !== undefined)
/** The question its line was begun for is not the one shown: said, with the way on. */
const stale = computed(() => {
  if (!answering.value) return false
  const a = props.agent?.asked
  const shown = a?.reply ? (a.id ?? ASKED) : undefined
  return lines.get(lineKey.value)?.question !== shown
})
// A line begun while its question had no id yet takes the id as it comes (UX3 R5), so that Send names it.
watch(
  () => props.agent?.asked?.id,
  (now, was) => {
    if (now && !was) lines.rebind(lineKey.value, ASKED, now)
  },
)
const bytes = computed(() => (props.agent ? chatBytes(draft.value.replace(/\r\n/g, '\n').trim()) : chatBytes(cleanChatText(draft.value))))
/** The agent's box counts from three quarters of its bound, the people's chat from its own. */
const counter = computed(() => (props.agent ? (bytes.value < (MAX_SUBMIT * 3) / 4 ? '' : `${(bytes.value / 1024).toFixed(1)} / ${MAX_SUBMIT / 1024} KiB`) : chatCounter(bytes.value)))
const tooLong = computed(() => (props.agent ? bytes.value > MAX_SUBMIT : chatTooLong(bytes.value)))
const empty = computed(() => (props.agent ? draft.value.trim() === '' : cleanChatText(draft.value) === ''))
/** Who the agent's box writes to, in its placeholder and Stop's tooltip. */
const who = computed(() => props.agent?.name || 'the agent')
const boxPlaceholder = computed(() => {
  const a = props.agent?.asked
  if (!a?.reply) return props.agent?.expand ? `Message ${who.value} · @ for a file, / for a command` : `Message ${who.value}`
  return a.choices ? 'Pick an answer above, or type your own' : `Answer ${who.value}…`
})
/** The agent's line begins with /: what the session does with a command is said then, and only then. */
const slash = computed(() => !!props.agent && !props.agent.expand && draft.value.trimStart().startsWith('/'))
/** Where the box may expand a line, a line beginning with / that is not the person's own goes as plain text: said, with the way on. */
const plainSlash = computed(() => !!props.agent?.expand && draft.value.trimStart().startsWith('/') && !mineNow.value)
/** A file the line names that lies outside the folder, where the agent would attach it: said, so that the person can take it out. */
const outside = computed(() => (props.agent?.expand ? outsideMention(draft.value) : ''))
const folderName = computed(() => props.agent?.folder?.replace(/\/+$/, '').split('/').pop() || 'the folder')

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
  const key = lineKey.value
  const mine = expandsLine(true, own, key, draft.value)
  // A line the owner refused (`not_sent`) comes back into its own session's box, as a message bound to no question: at once if that
  // box is empty, else when the person empties it (RowDrafts.putBack). It is the page's line then: sent again unchanged, it goes as
  // written.
  const back = (text: string) => lines.putBack(key, text)
  emit('send', draft.value, to, { own: mine, back })
  draft.value = ''
  own.sent(key)
  if (props.agent) keepCursor()
}

/** The line as the question's answer, through the page's answer: it leaves the box as it goes, and a refused one comes back bound. */
function answer() {
  const key = lineKey.value
  const raw = draft.value
  const bound = lines.get(key)?.question
  const from = typeof document === 'undefined' ? null : (document.activeElement as HTMLElement | null)
  if (bound && bound !== ASKED) answeredHere.add(bound)
  emit('answer', raw.trim(), bound === ASKED ? undefined : bound, (outcome) => {
    // This page had already sent its answer to that question (a choice, a line before): the line goes free, a message now, and says so.
    if (outcome === 'held') {
      if (freeHeld(lines, own, key, raw)) note.value = 'That question is already answered. Send now sends this line as a message.'
      return
    }
    if (outcome === 'refused' && bound) answeredHere.delete(bound)
    lines.settle(key, raw, outcome)
    // What comes back into the box is the page's now, until the person changes it.
    if (outcome === 'refused') own.set(key)
    if (outcome === 'refused') giveFocusBack(from)
  })
  keepCursor()
}

/**
 * The list for `@` (UX3 R6): the `@` word at the cursor asks for files; with nothing typed after it, the files the agent touched,
 * else the folder's files whose name holds the words, 150 ms after the last key, one read on its way at a time and a late answer
 * dropped. Escape closes it and keeps the text; while it is open Enter and Tab pick and never send. A pick writes `@<path> ` as plain
 * text in the line.
 */
const caret = ref(0)
function noteCaret() {
  const el = input.value?.textareaRef
  if (el) caret.value = el.selectionStart ?? el.value.length
}
const mention = computed<Mention | null>(() => (props.agent && props.files ? mentionAt(draft.value, caret.value) : null))
const fileList = ref<{ words: string; rows: MenuRow[]; more: boolean } | null>(null)
const finding = ref(false)
const active = ref(0)
const dismissed = ref('')
const mentionKey = computed(() => (mention.value ? `${mention.value.start}:${mention.value.words}` : ''))
let asked = 0
let inFlight = false
let pending: string | null = null
let findTimer: ReturnType<typeof setTimeout> | undefined
let slowTimer: ReturnType<typeof setTimeout> | undefined
function find(words: string) {
  if (inFlight) {
    pending = words
    return
  }
  const n = ++asked
  inFlight = true
  clearTimeout(slowTimer)
  slowTimer = setTimeout(() => n === asked && (finding.value = true), 300)
  props.files!(words)
    .then((r) => {
      if (n !== asked || mention.value?.words !== words) return
      fileList.value = { words, rows: fileRows(r.paths), more: r.more }
      active.value = Math.max(0, fileList.value.rows.findIndex((x) => x.pickable))
    })
    .catch(() => undefined) // a refused read says nothing: the next key tries again
    .finally(() => {
      inFlight = false
      if (n === asked) finding.value = false
      clearTimeout(slowTimer)
      const next = pending
      pending = null
      if (next !== null && next === mention.value?.words) find(next)
    })
}
watch(mentionKey, () => {
  clearTimeout(findTimer)
  const m = mention.value
  if (!m) {
    fileList.value = null
    finding.value = false
    // A list closed with Escape opens again for the next @ word, even one typed the same.
    dismissed.value = ''
    asked++
    return
  }
  if (fileList.value && fileList.value.words !== m.words) active.value = 0
  if (m.words === '') find('')
  else findTimer = setTimeout(() => find(m.words), 150)
})
onBeforeUnmount(() => {
  clearTimeout(findTimer)
  clearTimeout(slowTimer)
})
const menuRows = computed(() => (fileList.value && fileList.value.words === mention.value?.words ? fileList.value.rows : []))
const menuFoot = computed(() => {
  const row = menuRows.value[active.value]
  if (row && !row.pickable) return 'Type this path yourself: its name has a space or a mark.'
  if (finding.value && !menuRows.value.length) return 'Finding…'
  if (fileList.value && fileList.value.words === mention.value?.words) {
    if (!menuRows.value.length) return 'No file matches.'
    if (fileList.value.more) return 'More files match. Keep typing.'
  }
  return ''
})
const menuOpen = computed(() => !!mention.value && dismissed.value !== mentionKey.value && (menuRows.value.length > 0 || !!menuFoot.value))
const menuId = useId()
const activeId = computed(() => (menuOpen.value && menuRows.value.length ? `${menuId}-${active.value}` : undefined))

/** The person's pick: the `@` word becomes `@<path> `, the cursor after it, the list closed. Their own act, when the press is theirs. */
function pick(i: number, trusted: boolean) {
  const m = mention.value
  const row = menuRows.value[i]
  if (!m || !row?.pickable) return
  const put = pickMention(draft.value, m, row.value)
  draft.value = put.text
  if (trusted) own.typed(lineKey.value, put.text)
  else own.set(lineKey.value)
  caret.value = put.caret
  fileList.value = null
  void nextTick(() => {
    const el = input.value?.textareaRef
    el?.focus()
    el?.setSelectionRange(put.caret, put.caret)
  })
}

/** The list's keys, while it is open: the arrows move, Enter and Tab pick, Escape closes; true when the key was the list's. */
function menuKey(e: KeyboardEvent): boolean {
  if (!menuOpen.value || e.isComposing) return false
  const n = menuRows.value.length
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    if (!n) return false
    e.preventDefault()
    active.value = (active.value + (e.key === 'ArrowDown' ? 1 : n - 1)) % n
    return true
  }
  if (e.key === 'Enter' || e.key === 'Tab') {
    if (e.shiftKey && e.key === 'Enter') return false
    e.preventDefault()
    pick(active.value, e.isTrusted)
    return true
  }
  if (e.key === 'Escape') {
    e.preventDefault()
    e.stopPropagation()
    dismissed.value = mentionKey.value
    return true
  }
  return false
}

function onKeydown(e: KeyboardEvent) {
  if (menuKey(e)) return
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
  <form v-if="agent" ref="form" class="relative flex flex-col gap-1.5 px-3 py-2.5" data-chat-composer @submit.prevent="send('')">
    <ChatBoxMenu v-if="menuOpen" :id="menuId" kind="file" :rows="menuRows" :active="active" :foot="menuFoot" @pick="(i, trusted) => pick(i, trusted)" @hover="(i) => (active = i)" />
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
        :aria-controls="menuOpen && menuRows.length ? menuId : undefined"
        :aria-activedescendant="activeId"
        :aria-autocomplete="files ? 'list' : undefined"
        data-chat-input
        @keydown="onKeydown"
        @input="onInput"
        @keyup="noteCaret"
        @click="noteCaret"
        @select="noteCaret"
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
    <p v-else-if="plainSlash" class="text-[11px] text-muted" data-chat-help="plain">Sent as plain text. Type it yourself to run it as a command.</p>
    <p v-if="outside" class="text-[11px] text-warning" role="status" data-chat-outside><span class="font-mono" data-mono="path">{{ outside }}</span> is outside {{ folderName }}. The agent attaches it without asking.</p>
    <p v-if="note || stale" class="text-[11px] text-warning" role="status" data-chat-box-note>{{ note || 'Its question was answered. Empty the box to write a message instead.' }}</p>
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
