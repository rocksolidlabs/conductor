<script setup lang="ts">
import type { PaletteItem } from '~/composables/usePalette'
import type { Action } from '~/utils/actions'
import { choiceKey } from '~/utils/attention'
import { bindDraft } from '~/utils/reply'
import { matchRange, noteUse, writeRecent, type PaletteMode } from '~/utils/palette'
import type { TicketSummary } from '~/composables/useDispatch'
import { PICK_WORDS, pickEdit, ticketPath, ticketSummaryOf, type PickField } from '~/utils/dispatch'
import type { DispatchWatch } from '~/composables/useDispatchLive'

/**
 * The command palette (#302): one keystroke from any page opens it over
 * the workbench; what needs you is the first group, its choices inline,
 * so Enter is "take me to what needs me" and a digit answers it. The
 * input has the focus; ↓ moves it into the list, where the sidebar's keys
 * work on the focused row (J K ↵ 1–9 R S ⇧S X → ← ⌘↵ ⇧↵ Esc); digits
 * answer only while a row has the focus. Nothing runs before Enter, Stop
 * and Restart ask in the row, and a row that takes a line (a broadcast, a
 * run's name) opens its field in the row. The rows come from
 * usePaletteRows, the pure parts from utils/palette.ts.
 */
const emit = defineEmits<{ token: [] }>()

const palette = usePalette()
const launch = useLaunchModal()
const shortcuts = useShortcutsModal()
const router = useRouter()

// What a row opens beside the palette: the Share dialog on its target, Your name, the token gate (the layout's).
const nameOpen = ref(false)
const shareTarget = ref<{ sessionId: string; sessionName?: string } | { runId: string; sessionName?: string } | null>(null)
const shareRole = ref<'view' | 'control' | undefined>()
const shareOpen = ref(false)
async function openShare(target: NonNullable<typeof shareTarget.value>, role?: 'view' | 'control') {
  palette.close()
  shareOpen.value = false
  shareTarget.value = target
  shareRole.value = role
  await nextTick()
  shareOpen.value = true
}

const now = ref(Date.now())
const rows = usePaletteRows({
  share: openShare,
  launch: (prefill) => {
    palette.close()
    launch.show(prefill)
  },
  name: () => {
    palette.close()
    nameOpen.value = true
  },
  token: () => {
    palette.close()
    emit('token')
  },
  shortcuts: () => {
    palette.close()
    shortcuts.show()
  },
  close: () => palette.close(),
  now: () => now.value,
  pickTicket: (field, t) => void openTicketPick(field, t),
})

// Dispatch from the palette (docs/plans/dispatch.md, "web/app"): a ticket row's Set status… and Set priority… open a picker here, once
// the palette has closed, one operation on the ticket; New ticket in <project> (a `>` row, a Dispatch page's) opens the dialog here.
// Through the app's Tickets gate (usePalette), which a snapshot resets with the cache's, so the next edit expects the store's revision;
// one after another with the palette's other edits, each made on the ticket as the ones before left it (usePalette's editTicket).
const ticketGate = palette.ticketsGate
const ticketOps = useDispatchOps(ticketGate)
const ticketPick = ref<{ field: PickField; tickets: TicketSummary[] }>({ field: 'status', tickets: [] })
const ticketPickOpen = ref(false)
async function openTicketPick(field: PickField, t: TicketSummary) {
  palette.close()
  await nextTick()
  ticketPick.value = { field, tickets: [t] }
  ticketPickOpen.value = true
}
function ticketPicked(value: string | number, adds: boolean) {
  const { field, tickets } = ticketPick.value
  const t = tickets[0]
  if (!t) return
  void palette.editTicket(t, (x) =>
    ticketOps.editTickets([x], (y) => pickEdit(field, y, value, adds), {
      apply: (y) => palette.patchTicket(ticketSummaryOf(y)),
      done: (keys) => `${PICK_WORDS[field].title} of ${keys.join(', ')} set`,
    }),
  )
}
const newTicketOpen = ref(false)
const newTicketProject = ref('')
watch(
  () => palette.newTicketFor.value,
  async (key) => {
    if (!key) return
    palette.newTicketFor.value = ''
    palette.close()
    await nextTick()
    newTicketProject.value = key
    newTicketOpen.value = true
  },
)
const toast = useToast()
/** A ticket made from the palette's dialog, by its key and title (its reply's ticket's, else its receipt's key and the title sent). */
function ticketMade(t: { key: string; title: string }) {
  toast.add({ title: `${t.key} made`, description: t.title, icon: 'i-lucide-ticket', color: 'success', actions: [{ label: 'Open', icon: 'i-lucide-arrow-right', onClick: () => void router.push(ticketPath(t.key)) }] })
  palette.staleTickets()
}

const result = computed(() => rows.groupsFor(palette.query.value))
const groups = computed(() => result.value.groups)
const flat = computed(() => groups.value.flatMap((g) => g.rows))
const mode = computed<PaletteMode>(() => result.value.mode)
const MODE_NAMES: Record<PaletteMode, string> = { search: '', actions: 'Actions', runs: 'Runs and crews', files: 'Files', people: 'People', events: 'Events', shortcuts: 'Shortcuts' }
const placeholder = computed(() => (mode.value === 'search' ? 'Find a session, run, crew, file or action… (> # : @ ! ? for a kind; stop, share, answer, reply… to act)' : `Search ${MODE_NAMES[mode.value].toLowerCase()}…`))

// Focus: the input, or one row by its key. The row's element takes the real focus, so its keys are its own.
const input = useTemplateRef<{ inputRef?: HTMLInputElement }>('input')
const list = useTemplateRef<HTMLElement>('list')
const focused = ref<string | null>(null)
/** The row whose actions show (→), the row asking before it runs (and which action), the row whose field is open. */
const revealed = ref<string | null>(null)
const asking = ref<{ key: string; action?: Action } | null>(null)
const fielding = ref<string | null>(null)
const fieldText = ref('')
/** A reply drafted in a Needs-you row, with the question it was begun for (bindDraft): sent to that one, whatever the row shows by then. */
const replies = ref<Record<string, { text: string; question?: string }>>({})

function rowEl(key: string, within = ''): HTMLElement | null {
  return list.value?.querySelector<HTMLElement>(`[data-palette-row="${CSS.escape(key)}"]${within ? ` ${within}` : ''}`) ?? null
}
function focusInput() {
  focused.value = null
  input.value?.inputRef?.focus()
}
async function focusRow(key: string | null) {
  if (!key) return focusInput()
  focused.value = key
  await nextTick()
  const el = rowEl(key)
  el?.focus()
  el?.scrollIntoView({ block: 'nearest' })
}
function neighbour(key: string | null, delta: 1 | -1): string | null {
  const keys = flat.value.map((r) => r.key)
  if (!keys.length) return null
  const i = key ? keys.indexOf(key) : -1
  if (i < 0) return delta > 0 ? keys[0]! : null
  const j = i + delta
  if (j < 0) return null
  return keys[Math.min(j, keys.length - 1)]!
}

// Opening: the query as show() set it, the clock fresh, the recent uses read; the two lists that need a request after the first paint.
let tick: number | undefined
watch(
  () => palette.open.value,
  async (open) => {
    focused.value = null
    revealed.value = null
    asking.value = null
    fielding.value = null
    rows.showExited.value = false
    replies.value = {}
    if (!open) {
      window.clearInterval(tick)
      return
    }
    now.value = Date.now()
    rows.readRecentNow()
    tick = window.setInterval(() => (now.value = Date.now()), 10_000)
    await nextTick()
    requestAnimationFrame(() => {
      input.value?.inputRef?.focus()
      void palette.loadLists()
      void palette.loadTouched()
      // Through the live store, so that a read that fails is asked again after the window, doubled, as a re-read is.
      dispatchLive().ask(ticketsWatch, () => palette.loadTickets())
      void rows.loadIntegrations()
      // Opened on a row to ask (X on a session page): the row takes the focus and asks.
      const key = palette.pendingAsk.value
      if (key) {
        palette.pendingAsk.value = null
        const row = flat.value.find((r) => r.key === key)
        if (row) void focusRow(key).then(() => runRow(row))
      }
    })
  },
)
onBeforeUnmount(() => window.clearInterval(tick))

// The Touched index (#304) is read again when a `file` event arrives while the palette is open, one read in two seconds at
// most; closed, the feed's own entries lay over the cache until the next open reads it. The cache goes with the workbench
// token (whole-batch round): forgotten or replaced, the last token's sessions and files are not searchable any more.
const events = useEvents()
const admin = useWorkbenchToken()
watch(
  () => admin.token.value,
  () => {
    palette.forgetTouched()
    palette.forgetTickets()
  },
)
// The Tickets cache is read again on a dispatch event while the palette is open (gathered, DispatchLive's window), and is due again at
// the next open when one came while it was closed; a snapshot forgets what its gate knew. A read that fails is asked again after the
// window, doubled, until one answers (DispatchLive), the cached rows shown meanwhile.
const ticketsWatch: DispatchWatch = {
  later: () => true,
  read: () => {
    if (!palette.open.value) {
      palette.staleTickets()
      return Promise.resolve()
    }
    return palette.loadTickets(true)
  },
  reset: () => palette.resetTickets(),
}
const stopTickets = dispatchLive().subscribe(ticketsWatch)
onBeforeUnmount(stopTickets)
let touchedTimer: number | undefined
const stopTouched = events.onEvent((e) => {
  if (e.type !== 'file' || !palette.open.value) return
  window.clearTimeout(touchedTimer)
  touchedTimer = window.setTimeout(() => void palette.loadTouched(true), 2000)
})
onBeforeUnmount(() => {
  stopTouched()
  window.clearTimeout(touchedTimer)
})
// A row that goes (the session answered and moved on) hands the focus to the input, as the sidebar does; a row that re-renders
// under the focus gets it back, only while the focus was in the list (the input, focused by a click, keeps it).
watch(flat, async (items) => {
  const key = focused.value
  if (!key) return
  if (!items.some((r) => r.key === key)) return focusInput()
  if (!list.value?.contains(document.activeElement)) return
  await nextTick()
  const el = rowEl(key)
  if (el && !el.contains(document.activeElement)) el.focus()
})

/** A use counted for the ranking, kept per browser. */
function noteRow(row: PaletteItem) {
  rows.recent.value = noteUse(rows.recent.value, row.key)
  writeRecent(typeof localStorage === 'undefined' ? null : localStorage, rows.recent.value)
}

/** Enter on a row: its field opens, its question is asked, or it runs; a switch stays open, everything else closes the palette. */
async function runRow(row: PaletteItem) {
  if (row.disabled) return
  if (row.field && row.fieldOnEnter && fielding.value !== row.key) return openField(row)
  if (row.ask && asking.value?.key !== row.key) {
    asking.value = { key: row.key }
    await nextTick()
    rowEl(row.key, '[data-palette-confirm]')?.focus()
    return
  }
  noteRow(row)
  asking.value = null
  if (row.on === undefined && !row.keepOpen) palette.close()
  await row.run()
}
/** The row's field, opened by its `field` action or by Enter on a row that takes a line first. */
async function openField(row: PaletteItem) {
  fielding.value = row.key
  // A line the session refused last time opens the field again (#322), to send as it is or change.
  fieldText.value = row.field?.draft ?? ''
  await nextTick()
  rowEl(row.key, '[data-palette-field] input')?.focus()
}
async function runAction(row: PaletteItem, a: Action) {
  if (a.disabled) return
  if (a.id === 'field') return openField(row)
  if (a.ask && !(asking.value?.key === row.key && asking.value.action?.id === a.id)) {
    asking.value = { key: row.key, action: a }
    await nextTick()
    rowEl(row.key, '[data-palette-confirm]')?.focus()
    return
  }
  noteRow(row)
  asking.value = null
  palette.close()
  a.run()
}
function confirm(row: PaletteItem) {
  const a = asking.value?.action
  if (a) void runAction(row, a)
  else void runRow(row)
}
function cancelAsk(row: PaletteItem) {
  asking.value = null
  void focusRow(row.key)
}
async function submitField(row: PaletteItem) {
  if (!row.field) return
  const text = fieldText.value.slice(0, row.field.maxLength ?? 4000)
  noteRow(row)
  fielding.value = null
  palette.close()
  await row.field.submit(text)
}
function answerChoice(row: PaletteItem, i: number) {
  if (row.answerAll) {
    noteRow(row)
    row.answerAll(i)
    return
  }
  const s = row.session
  const p = row.prompt
  if (!s || !p || p.held || p.away) return
  rows.answer(s, i, p.question)
}
function draftReply(row: PaletteItem, after: string) {
  const d = replies.value[row.key]
  replies.value = { ...replies.value, [row.key]: { text: after, question: bindDraft(d?.text ?? '', after, d?.question, row.prompt?.question) } }
}
function sendReply(row: PaletteItem) {
  const s = row.session
  const p = row.prompt
  const d = replies.value[row.key]
  const text = (d?.text ?? '').trim()
  if (!s || !p || p.held || p.away || !text) return
  replies.value = { ...replies.value, [row.key]: { text: '' } }
  void rows.reply(s, text, d?.question)
}
function openInTab(row: PaletteItem) {
  if (!row.href) return
  noteRow(row)
  window.open(row.href, '_blank', 'noopener,noreferrer')
}
function showInYard(row: PaletteItem) {
  if (!row.yardId) return
  noteRow(row)
  palette.close()
  void router.push({ path: '/yard', query: { focus: row.yardId } })
}

/**
 * No key of the palette reaches the page's handlers (the reply bar's digits, the pages' letters, the layout's chords): every
 * keydown inside it stops here but Tab, which the modal's focus trap reads on its scope (whole-batch round). A button's own
 * Enter and Space are its default action, which propagation does not carry.
 */
function onRootKey(e: KeyboardEvent) {
  if (e.key !== 'Tab') e.stopPropagation()
}

/** The input's keys: ↓ into the list, ↵ the first row, Esc closes (the modal's own Escape listens on the document, which onRootKey keeps the key from). */
function onInputKey(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.preventDefault()
    palette.close()
    return
  }
  if (e.key === 'ArrowDown') {
    const first = neighbour(null, 1)
    if (first) {
      e.preventDefault()
      void focusRow(first)
    }
  } else if (e.key === 'Enter' && !e.isComposing) {
    const first = flat.value[0]
    if (first) {
      e.preventDefault()
      void runRow(first)
    }
  }
}

/** The list's keys, on the focused row: the sidebar's (SessionSidebar.vue onKey), plus → ← for the row's actions. */
function onListKey(e: KeyboardEvent) {
  const key = focused.value
  const row = key ? flat.value.find((r) => r.key === key) : undefined
  const target = e.target as HTMLElement
  const inField = !!target.closest('input, textarea, select, [contenteditable]')
  if (e.key === 'Escape') {
    e.preventDefault()
    e.stopPropagation()
    if (asking.value) asking.value = null
    else if (fielding.value) fielding.value = null
    else if (revealed.value) revealed.value = null
    else return focusInput()
    if (row) void focusRow(row.key)
    return
  }
  if (!row) return
  // A button inside the row (a choice, an action, Confirm, the field's Send) takes its own Enter and Space.
  if ((e.key === 'Enter' || e.key === ' ') && target.closest('button, a[href]')) return
  if (inField) {
    // A field in a row keeps its keys; Enter in a reply field sends, in the row's own field submits.
    if (e.key === 'Enter' && !e.isComposing) {
      e.preventDefault()
      if (target.closest('[data-palette-reply]')) sendReply(row)
      else if (target.closest('[data-palette-field]')) void submitField(row)
    }
    return
  }
  if (asking.value?.key === row.key) {
    // The question's Confirm has the focus: Enter is its click, nothing else is a key.
    if (e.key === 'Enter') e.stopPropagation()
    return
  }
  const own = () => {
    e.preventDefault()
    e.stopPropagation()
  }
  if (e.metaKey || e.ctrlKey) {
    if (e.key === 'Enter') {
      own()
      openInTab(row)
    }
    return
  }
  if (e.altKey) return
  switch (e.key) {
    case 'ArrowDown':
    case 'j':
    case 'J':
      own()
      return void focusRow(neighbour(row.key, 1))
    case 'ArrowUp':
    case 'k':
    case 'K':
      own()
      return void focusRow(neighbour(row.key, -1))
    case 'Enter':
      own()
      if (e.shiftKey) return showInYard(row)
      return void runRow(row)
    case 'ArrowRight':
      own()
      if (row.actions?.length) revealed.value = row.key
      return
    case 'ArrowLeft':
      own()
      revealed.value = null
      return
    case 'r':
    case 'R': {
      if (row.runId) {
        own()
        noteRow(row)
        palette.close()
        void router.push(`/runs/${encodeURIComponent(row.runId)}`)
      } else if (row.prompt && !row.prompt.choices.length && !row.prompt.held && !row.prompt.away) {
        own()
        rowEl(row.key, '[data-palette-reply] input')?.focus()
      }
      return
    }
  }
  // The row's actions by their keys (S shares, ⇧S copies a view link, X asks to stop…), the sidebar's letters among them.
  const pressed = e.key.length === 1 ? e.key.toUpperCase() : e.key
  const a = row.actions?.find((a) => a.kbds && !a.disabled && a.kbds.length === (e.shiftKey ? 2 : 1) && a.kbds[a.kbds.length - 1]!.toUpperCase() === pressed && (!e.shiftKey || a.kbds[0] === 'shift'))
  if (a) {
    own()
    void runAction(row, a)
    return
  }
  const n = Number(e.key)
  if (Number.isInteger(n) && n >= 1 && n <= 9 && row.prompt) {
    own()
    const o = row.prompt.choices[n - 1]
    if (o && choiceKey(o, n - 1) === n) answerChoice(row, n - 1)
  }
}

function onListFocusIn(e: FocusEvent) {
  const el = (e.target as HTMLElement).closest('[data-palette-row]')
  if (el) focused.value = el.getAttribute('data-palette-row')
}

/** The label with the match marked, as three pieces. */
function pieces(text: string): [string, string, string] {
  const q = mode.value === 'search' ? palette.query.value : palette.query.value.slice(1)
  const r = matchRange(text, q)
  if (!r) return [text, '', '']
  return [text.slice(0, r[0]), text.slice(r[0], r[1]), text.slice(r[1])]
}

const dotClass: Record<NonNullable<PaletteItem['state']>, string> = { needs: 'bg-warning', running: 'bg-success', idle: 'bg-neutral-400', exited: 'bg-neutral-400' }
</script>

<template>
  <UModal v-model:open="palette.open.value" title="Command palette" description="Find a session, run, crew, file or action and act on it without leaving the page." :ui="{ content: 'sm:max-w-[680px] sm:top-[10vh] sm:translate-y-0 max-h-[80vh] overflow-hidden' }">
    <template #content>
      <div class="flex max-h-[80vh] min-h-0 flex-col" data-palette :data-palette-mode="mode" @keydown="onRootKey">
        <!-- The input, with the mode's chip while a prefix is typed. -->
        <div class="flex items-center gap-2 border-b border-default px-3 py-2">
          <UBadge v-if="mode !== 'search'" :label="MODE_NAMES[mode]" color="neutral" variant="subtle" size="sm" data-palette-chip />
          <UInput ref="input" v-model="palette.query.value" :placeholder="placeholder" icon="i-lucide-search" variant="none" size="lg" class="flex-1" :ui="{ base: 'px-1' }" aria-label="Command palette" data-palette-input @keydown="onInputKey" @focus="focused = null" />
          <UKbd value="escape" size="sm" />
        </div>
        <!-- The groups; the rows take the focus, their keys are their own. -->
        <div ref="list" class="min-h-0 flex-1 overflow-y-auto py-1" role="listbox" aria-label="Results" data-palette-list @keydown="onListKey" @focusin="onListFocusIn">
          <p v-if="!flat.length" class="px-4 py-6 text-center text-sm text-muted" data-palette-empty>Nothing matches.</p>
          <section v-for="g in groups" :key="g.id" :data-palette-group="g.id" class="px-1 pb-1">
            <h3 class="px-3 pb-0.5 pt-2 text-[11px] font-semibold uppercase tracking-wide text-muted">{{ g.label }}<span v-if="g.rows.length > 1" class="ml-1.5 font-normal normal-case tracking-normal">{{ g.rows.length }}</span></h3>
            <div
              v-for="row in g.rows"
              :key="row.key"
              role="option"
              tabindex="-1"
              class="group relative rounded-md px-2 py-1.5 outline-none"
              :class="[focused === row.key ? 'bg-elevated' : 'hover:bg-elevated/60', row.disabled ? 'opacity-60' : '']"
              :aria-selected="focused === row.key ? 'true' : 'false'"
              :aria-disabled="row.disabled ? 'true' : undefined"
              :data-palette-row="row.key"
              :data-palette-focused="focused === row.key ? '' : undefined"
              :data-palette-state="row.state"
              @click="runRow(row)"
            >
              <div class="flex items-start gap-2.5">
                <span v-if="row.agentId" class="relative mt-0.5 flex-none">
                  <SessionAvatar :agent-id="row.agentId" :solid="row.state === 'needs'" :dashed="row.state === 'exited'" />
                  <span v-if="row.state" class="absolute -right-0.5 -top-0.5 size-2 rounded-full ring-2 ring-default" :class="dotClass[row.state]" aria-hidden="true" />
                </span>
                <UIcon v-else-if="row.icon" :name="row.icon" class="mt-1 size-4 flex-none text-muted" aria-hidden="true" />
                <span v-else class="mt-1 size-4 flex-none" />
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-2">
                    <span class="truncate text-sm font-medium text-highlighted" data-palette-label><template v-for="(p, i) in pieces(row.label)" :key="i"><mark v-if="i === 1 && p" class="rounded-sm bg-primary/20 text-highlighted">{{ p }}</mark><template v-else>{{ p }}</template></template></span>
                    <UBadge v-if="row.on !== undefined" :label="row.on ? 'on' : 'off'" :color="row.on ? 'success' : 'neutral'" variant="subtle" size="sm" :data-palette-switch="row.on ? 'on' : 'off'" />
                    <span v-if="row.kbds?.length" class="ml-auto flex flex-none items-center gap-0.5"><UKbd v-for="(k, i) in row.kbds" :key="i" :value="k" size="sm" /></span>
                  </div>
                  <div v-if="row.detail" class="truncate font-mono text-[11.5px] text-muted" data-palette-detail>{{ row.detail }}</div>
                  <div v-if="row.note" class="text-xs text-warning" data-palette-note>{{ row.note }}</div>
                  <!-- A Needs-you row: its choices numbered, or a reply field; held and away as the sidebar shows them. -->
                  <div v-if="row.prompt" class="mt-1.5" data-palette-prompt>
                    <div v-if="row.prompt.choices.length" class="flex flex-wrap gap-1">
                      <UButton
                        v-for="(o, i) in row.prompt.choices"
                        :key="i"
                        size="xs"
                        color="neutral"
                        variant="soft"
                        :disabled="row.prompt.held || row.prompt.away"
                        :label="`${choiceKey(o, i) ?? '·'} ${o.label}`"
                        :data-palette-choice="choiceKey(o, i) ?? ''"
                        :title="o.mode ? 'Changes the session’s permission mode' : undefined"
                        @click.stop="answerChoice(row, i)"
                      />
                    </div>
                    <div v-else-if="!row.prompt.held && !row.prompt.away" class="flex items-center gap-1.5" data-palette-reply @click.stop>
                      <UInput :model-value="replies[row.key]?.text ?? ''" :placeholder="row.prompt.placeholder" size="xs" class="flex-1" :aria-label="`Reply to ${row.label}`" @update:model-value="(v) => draftReply(row, String(v))" />
                      <UButton icon="i-lucide-send" size="xs" color="neutral" variant="soft" aria-label="Send" @click="sendReply(row)" />
                    </div>
                    <p v-if="row.prompt.held || row.prompt.away" class="text-xs text-muted" data-palette-held>{{ row.prompt.placeholder }}</p>
                  </div>
                  <!-- The question a destructive row asks, in the row: Enter on Confirm, Escape cancels. -->
                  <div v-if="asking?.key === row.key" class="mt-1.5 flex flex-wrap items-center gap-2 rounded-md border border-default bg-default p-2" data-palette-ask @click.stop>
                    <span class="text-sm font-medium">{{ (asking.action?.ask ?? row.ask)?.title }}</span>
                    <span class="text-xs text-muted">{{ (asking.action?.ask ?? row.ask)?.detail }}</span>
                    <span class="ml-auto flex items-center gap-1">
                      <UButton label="Cancel" size="xs" color="neutral" variant="ghost" @click="cancelAsk(row)" />
                      <UButton :label="(asking.action?.ask ?? row.ask)?.title.split(' ')[0] ?? 'Confirm'" size="xs" color="error" data-palette-confirm @click="confirm(row)" />
                    </span>
                  </div>
                  <!-- A row's field: a line it takes before it runs (a run's name, a broadcast). -->
                  <div v-else-if="fielding === row.key && row.field" class="mt-1.5 flex items-center gap-1.5" data-palette-field @click.stop>
                    <UInput v-model="fieldText" :placeholder="row.field.placeholder" :maxlength="row.field.maxLength" size="xs" class="flex-1" :aria-label="row.field.placeholder" />
                    <UButton :label="row.field.button ?? 'Send'" size="xs" data-palette-field-submit @click="submitField(row)" />
                  </div>
                  <!-- The row's actions, revealed by →, folded by ←; each runs with its key. -->
                  <div v-if="revealed === row.key && row.actions?.length" class="mt-1.5 flex flex-wrap gap-1" data-palette-actions @click.stop>
                    <UButton v-for="a in row.actions" :key="a.id" :label="a.label" :icon="a.icon" size="xs" color="neutral" variant="outline" :disabled="a.disabled" :data-palette-action="a.id" @click="runAction(row, a)">
                      <template v-if="a.kbds?.length" #trailing><span class="flex items-center gap-0.5"><UKbd v-for="(k, i) in a.kbds" :key="i" :value="k" size="sm" /></span></template>
                    </UButton>
                  </div>
                </div>
              </div>
            </div>
          </section>
        </div>
        <div class="flex items-center gap-3 border-t border-default px-3 py-1.5 font-mono text-[11px] text-muted" data-palette-foot>
          <span>↑↓ move</span><span>↵ open</span><span>→ actions</span><span>1–9 answer</span><span>⇧↵ Yard</span><span class="ml-auto">esc</span>
        </div>
      </div>
    </template>
  </UModal>
  <ShareLinksModal v-if="shareTarget && 'runId' in shareTarget && shareTarget.runId" v-model:open="shareOpen" :run-id="shareTarget.runId" :session-name="shareTarget.sessionName" :role="shareRole" />
  <ShareLinksModal v-else-if="shareTarget && 'sessionId' in shareTarget && shareTarget.sessionId" v-model:open="shareOpen" :session-id="shareTarget.sessionId" :session-name="shareTarget.sessionName" :role="shareRole" />
  <NameDialog v-model:open="nameOpen" />
  <DispatchPicker v-model:open="ticketPickOpen" :field="ticketPick.field" :tickets="ticketPick.tickets" :agents="palette.catalog.value" @pick="ticketPicked" />
  <DispatchNewTicket v-if="newTicketProject" v-model:open="newTicketOpen" :project="newTicketProject" :gate="ticketGate" @created="ticketMade" />
</template>
