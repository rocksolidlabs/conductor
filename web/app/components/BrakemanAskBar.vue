<script setup lang="ts">
import type { BrakemanRequest } from '~/utils/brakeman'
import { KIND_SUBJECTS, parseRef, refLabel } from '~/utils/brakeman'
import type { AskBody, AskChip, Mention } from '~/utils/brakemanAsk'
import { chipRequest, kindLabel, lineRequest, refused } from '~/utils/brakemanAsk'

/**
 * Asking Brakeman from where a person is (the Brakeman design review's ask bar): one field and a row of chips. A typed line is a
 * question about the page's subject and whatever the line names with `@`; a chip sends its kind at once, or, with "…", sets its kind
 * as a token at the field's start and opens the `@` list narrowed to what the kind takes, the line typed after it its note. The `@`
 * list offers only what the page holds (useAskSubjects) and a ref typed whole; the server resolves every subject. The bar sends
 * itself, under the person's display name (useAskBrakeman), and says a refusal's words under the field; `asked` follows a request
 * kept. Brakeman never takes the focus: the answer comes where the host draws it.
 *
 * `full` (the Brakeman page): a 44 px field, Ask filled, the chips in a row and the hint. `compact` (a run's or a ticket's panel): a
 * 36 px field, the chips wrapping, no hint. `foot` (the phone's foot): the field and an outline Ask, 44 px, the label hidden, the chips
 * only while the bar has the focus. Under lg every control is 44 px.
 */
const props = withDefaults(
  defineProps<{
    subject?: string
    label: string
    placeholder?: string
    chips: AskChip[]
    size?: 'full' | 'compact' | 'foot'
    disabled?: string
    prefill?: { kind?: string; subject?: string }
  }>(),
  { subject: undefined, placeholder: 'What changed since last night?', size: 'full', disabled: '', prefill: undefined },
)
const emit = defineEmits<{ asked: [request: BrakemanRequest] }>()

const asker = useAskBrakeman()
const subjects = useAskSubjects()
const live = useAttention()
const id = useId()

const text = ref('')
/** The kind a chip with "…" (or `?kind=`) set: sent with what is picked and the line as its note. */
const token = ref<AskChip | null>(null)
/** The refs picked from the `@` list, each a token. */
const picked = ref<string[]>([])
const sending = ref(false)
/** The server's refusal of the last send. */
const refusal = ref('')
const focused = ref(false)
const sinceOpen = ref(false)
const since = ref('')

const field = useTemplateRef<{ inputRef?: HTMLInputElement }>('field')
const input = () => field.value?.inputRef

/** A token for a kind named by `?kind=`: the chip of that kind when the bar offers one, else the kind's own words. */
function tokenFor(kind: string): AskChip | null {
  const sk = KIND_SUBJECTS[kind]
  if (!sk) return null
  const chip = props.chips.find((c) => c.kind === kind && c.more)
  if (chip) return chip
  return { key: kind, label: kindLabel(kind), kind, more: sk.note && !sk.max ? 'note' : sk.max ? 'subject' : undefined }
}

watch(
  () => [props.prefill?.kind, props.prefill?.subject] as const,
  ([kind, subject]) => {
    if (!kind && !subject) return
    token.value = kind ? tokenFor(kind) : null
    picked.value = subject && parseRef(subject) ? [subject] : []
    refusal.value = ''
  },
  { immediate: true },
)

const names = {
  session: (sid: string) => live.sessions.value.find((s) => s.id === sid)?.name,
  run: (rid: string) => live.runNames.value[rid],
}
const tokenWords = computed(() => (token.value ? kindLabel(token.value.kind) : ''))

/** What the bar would send now: a body, a problem, or nothing (an empty line and no token). */
const body = computed<AskBody | { problem: string } | null>(() => {
  if (token.value) return chipRequest(token.value, picked.value, text.value)
  if (!text.value.trim()) return null
  return lineRequest(text.value, picked.value, props.subject)
})
/** What keeps Ask from sending, in the server's words: the bar holds less than the kind needs, or the server refused the last send. */
const problem = computed(() => refusal.value || (body.value && refused(body.value) ? body.value.problem : ''))
const canAsk = computed(() => !props.disabled && !sending.value && !!body.value && !refused(body.value))

// --- the @ list ----------------------------------------------------------------------------------------------------------------

/** The text before the caret, as last seen. */
const caret = ref(0)
/** The text the person closed the list on (Escape): it stays closed until the text changes. */
const dismissed = ref<string | null>(null)
const cursor = ref(0)

/** What follows an `@` just before the caret, or null when the caret is not in one. */
const mentionQuery = computed(() => {
  if (dismissed.value === text.value) return null
  const m = /(?:^|\s)@([^\s@]*)$/.exec(text.value.slice(0, caret.value))
  return m ? m[1]! : null
})
const kinds = computed(() => (token.value ? (KIND_SUBJECTS[token.value.kind]?.kinds ?? []) : []))
const matches = computed<Mention[]>(() => (mentionQuery.value === null ? [] : subjects.matches(mentionQuery.value, kinds.value, props.subject)))
const listOpen = computed(() => focused.value && mentionQuery.value !== null && !props.disabled)
watch(listOpen, (open) => {
  if (open) subjects.opened(kinds.value)
})
watch(matches, () => (cursor.value = 0))

function noteCaret() {
  caret.value = input()?.selectionStart ?? text.value.length
}
function onText(v: string | number) {
  text.value = String(v)
  refusal.value = ''
  nextTick(noteCaret)
}

/** Puts the caret at the end of the field, with the focus. */
function focusEnd() {
  nextTick(() => {
    const el = input()
    if (!el) return
    el.focus()
    el.setSelectionRange(text.value.length, text.value.length)
    caret.value = text.value.length
  })
}

/** Takes a match: the `@words` before the caret go, and its ref becomes a token. */
function pick(m: Mention) {
  const before = text.value.slice(0, caret.value).replace(/(^|\s)@[^\s@]*$/, '$1')
  const after = text.value.slice(caret.value)
  text.value = (before + after).replace(/\s{2,}/g, ' ')
  if (!picked.value.includes(m.ref)) picked.value = [...picked.value, m.ref]
  refusal.value = ''
  nextTick(() => {
    const el = input()
    if (!el) return
    el.focus()
    el.setSelectionRange(before.length, before.length)
    caret.value = before.length
  })
}

function onKeydown(e: KeyboardEvent) {
  if (!listOpen.value) return
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    const n = matches.value.length
    if (n) cursor.value = (cursor.value + (e.key === 'ArrowDown' ? 1 : n - 1)) % n
  } else if ((e.key === 'Enter' || e.key === 'Tab') && matches.value.length) {
    e.preventDefault()
    pick(matches.value[cursor.value]!)
  } else if (e.key === 'Escape') {
    e.preventDefault()
    e.stopPropagation()
    dismissed.value = text.value
  }
}

const root = useTemplateRef<HTMLElement>('root')
function onFocusOut(e: FocusEvent) {
  const to = e.relatedTarget as Node | null
  // The since popover's content is teleported: the focus moving into it stays the bar's.
  if (to && (root.value?.contains(to) || (to as Element).closest?.('[data-ask-since-panel]'))) return
  focused.value = false
}

// --- sending ---------------------------------------------------------------------------------------------------------------------

async function send(b: AskBody) {
  if (props.disabled || sending.value) return
  sending.value = true
  refusal.value = ''
  try {
    const r = await asker.send(b)
    text.value = ''
    token.value = null
    picked.value = []
    since.value = ''
    sinceOpen.value = false
    emit('asked', r)
  } catch (e) {
    refusal.value = (e as Error).message
  } finally {
    sending.value = false
  }
}

/** Ask, pressed or clicked: Enter or Tab that picks from the @ list never gets here (onKeydown prevents it). */
function submit() {
  const b = body.value
  if (!canAsk.value || !b || refused(b)) return
  void send(b)
}

/** A chip: one that needs more sets its token and opens what it needs; any other sends at once, with the line as its note. */
function chip(c: AskChip) {
  refusal.value = ''
  if (c.more === 'subject' || c.more === 'note') {
    token.value = c
    // A kind takes only its own ref kinds: a pick of another goes.
    const takes = KIND_SUBJECTS[c.kind]?.kinds ?? []
    picked.value = picked.value.filter((s) => !takes.length || takes.includes(parseRef(s)?.kind ?? '')).slice(0, KIND_SUBJECTS[c.kind]?.max ?? 0)
    if (c.more === 'subject' && !picked.value.length) text.value = text.value.trim() ? `${text.value.trim()} @` : '@'
    dismissed.value = null
    focusEnd()
    return
  }
  const b = chipRequest(c, [], text.value)
  if (refused(b)) {
    refusal.value = b.problem
    return
  }
  void send(b)
}

/** The since field's local time as the server takes it: RFC 3339, in UTC. */
const sinceISO = computed(() => {
  const t = new Date(since.value)
  return since.value && !Number.isNaN(t.getTime()) ? t.toISOString() : ''
})
function briefSince(c: AskChip) {
  const b = chipRequest(c, [], text.value, sinceISO.value)
  if (refused(b)) {
    refusal.value = b.problem
    return
  }
  void send(b)
}

function removeToken() {
  token.value = null
  refusal.value = ''
  focusEnd()
}
function removeSubject(ref: string) {
  picked.value = picked.value.filter((s) => s !== ref)
  refusal.value = ''
  focusEnd()
}

const showChips = computed(() => props.chips.length > 0 && (props.size !== 'foot' || focused.value))
const tall = computed(() => (props.size === 'compact' ? 'h-9 max-lg:h-11' : 'h-11'))
/** A control's height (44 px to tap: always in the foot, under lg elsewhere), and an icon's square of it. */
const control = computed(() => (props.size === 'foot' ? 'min-h-11' : 'max-lg:min-h-11'))
const square = computed(() => (props.size === 'foot' ? 'min-h-11 min-w-11 justify-center' : 'max-lg:min-h-11 max-lg:min-w-11 justify-center'))
const listId = `${id}-mentions`
const optionId = (i: number) => `${id}-mention-${i}`
</script>

<template>
  <form
    ref="root"
    class="flex min-w-0 flex-col"
    :class="size === 'compact' ? 'gap-2' : 'gap-2.5'"
    data-brakeman-ask
    :data-ask-size="size"
    :data-ask-foot="size === 'foot' ? '' : undefined"
    @submit.prevent="submit"
    @focusin="focused = true"
    @focusout="onFocusOut"
  >
    <label :for="`${id}-field`" :class="size === 'foot' ? 'sr-only' : size === 'compact' ? 'text-xs font-medium text-muted' : 'text-sm font-medium'">{{ label }}</label>
    <div class="flex min-w-0 items-start gap-2">
      <div class="relative flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
        <span v-if="token" class="inline-flex min-w-0 max-w-full items-center gap-0.5 rounded-md bg-elevated ps-2 text-sm ring ring-default" data-ask-kind>
          <span class="min-w-0 truncate">{{ tokenWords }}</span>
          <IconButton icon="i-lucide-x" size="xs" color="neutral" variant="link" :class="square" :label="`Remove ${tokenWords}`" data-ask-kind-remove @click="removeToken" />
        </span>
        <span v-if="picked.length" class="contents min-w-0" data-ask-subjects>
          <span v-for="s in picked" :key="s" class="inline-flex min-w-0 max-w-full items-center gap-0.5 rounded-md bg-elevated ps-2 text-sm ring ring-default" :data-ask-subject="s">
            <span class="min-w-0 truncate" :title="s">{{ refLabel(s, names) }}</span>
            <IconButton icon="i-lucide-x" size="xs" color="neutral" variant="link" :class="square" :label="`Remove ${refLabel(s, names)}`" @click="removeSubject(s)" />
          </span>
        </span>
        <UInput
          :id="`${id}-field`"
          ref="field"
          :model-value="text"
          :placeholder="placeholder"
          :disabled="!!disabled"
          autocomplete="off"
          class="min-w-48 flex-1"
          :ui="{ base: tall }"
          role="combobox"
          aria-autocomplete="list"
          :aria-expanded="listOpen ? 'true' : 'false'"
          :aria-controls="listOpen ? listId : undefined"
          :aria-activedescendant="listOpen && matches.length ? optionId(cursor) : undefined"
          data-ask-field
          @update:model-value="onText"
          @keydown="onKeydown"
          @keyup="noteCaret"
          @click="noteCaret"
        />
        <div
          v-if="listOpen"
          :id="listId"
          role="listbox"
          :aria-label="token ? `What ${tokenWords} is about` : 'Name a run, a task, a session, a question or a ticket'"
          class="absolute inset-x-0 z-30 min-w-0 max-h-64 overflow-y-auto rounded-md bg-default py-1 shadow-lg ring ring-default"
          :class="size === 'foot' ? 'bottom-full mb-1' : 'top-full mt-1'"
          data-ask-mentions
        >
          <p v-if="!matches.length" class="px-3 py-2 text-sm text-muted" data-ask-mentions-none>Nothing on this page matches. A ref typed whole is taken as it is.</p>
          <button
            v-for="(m, i) in matches"
            :id="optionId(i)"
            :key="m.ref"
            type="button"
            role="option"
            tabindex="-1"
            :aria-selected="i === cursor ? 'true' : 'false'"
            class="flex w-full min-w-0 items-baseline gap-2 px-3 text-left text-sm outline-none max-lg:min-h-11"
            :class="[i === cursor ? 'bg-elevated' : 'hover:bg-elevated/60', size === 'compact' ? 'py-1' : 'py-1.5']"
            :data-ask-mention="m.ref"
            @mousedown.prevent
            @mousemove="cursor = i"
            @click="pick(m)"
          >
            <span class="min-w-0 flex-1 truncate">{{ m.label }}</span>
            <span v-if="m.detail" class="shrink-0 text-xs text-muted">{{ m.detail }}</span>
          </button>
        </div>
      </div>
      <IconButton v-if="size === 'foot'" type="submit" icon="i-lucide-send" color="neutral" variant="outline" class="min-h-11 min-w-11 justify-center" label="Ask" :loading="sending" :disabled="!canAsk" data-ask-send />
      <UButton v-else type="submit" label="Ask" icon="i-lucide-send" :class="[tall, 'shrink-0']" :loading="sending" :disabled="!canAsk" data-ask-send />
    </div>
    <!-- A press keeps the field's focus, as on the @ list's options: where a pressed button takes none (WebKit), the field's blur
         would hide the foot's chips before the click lands. -->
    <div v-if="showChips" class="flex min-w-0 flex-wrap gap-1.5" data-ask-chips @mousedown.prevent>
      <template v-for="c in chips" :key="c.key">
        <UFieldGroup v-if="c.more === 'since'">
          <UButton :label="c.label" color="neutral" variant="outline" :size="size === 'compact' ? 'xs' : 'sm'" :class="control" :disabled="!!disabled || sending" :data-ask-chip="c.key" @click="chip(c)" />
          <UPopover v-model:open="sinceOpen" :content="{ align: 'start' }">
            <IconButton icon="i-lucide-chevron-down" color="neutral" variant="outline" :size="size === 'compact' ? 'xs' : 'sm'" :class="square" label="Brief me since another time" :disabled="!!disabled || sending" data-ask-since-open />
            <template #content>
              <div class="flex w-72 max-w-[calc(100vw-2rem)] flex-col gap-2 p-3" data-ask-since-panel>
                <UFormField label="Since" description="The time the briefing starts from, in this browser's time.">
                  <UInput v-model="since" type="datetime-local" class="w-full" :ui="{ base: 'max-lg:min-h-11' }" data-ask-since />
                </UFormField>
                <UButton label="Brief me" color="neutral" variant="outline" class="self-start max-lg:min-h-11" :disabled="!sinceISO || !!disabled || sending" data-ask-since-send @click="briefSince(c)" />
              </div>
            </template>
          </UPopover>
        </UFieldGroup>
        <UButton v-else :label="c.label" color="neutral" variant="outline" :size="size === 'compact' ? 'xs' : 'sm'" :class="control" :disabled="!!disabled || sending" :data-ask-chip="c.key" @click="chip(c)" />
      </template>
    </div>
    <p v-if="problem" class="text-sm" :class="refusal ? 'text-error' : 'text-muted'" :role="refusal ? 'alert' : undefined" data-ask-problem>{{ problem }}</p>
    <p v-else-if="disabled" class="text-sm text-muted" data-ask-disabled>{{ disabled }}</p>
    <p v-else-if="size === 'full'" class="text-xs text-muted" data-ask-hint>Type @ to name a run, a task, a session, a question or a ticket.</p>
  </form>
</template>
