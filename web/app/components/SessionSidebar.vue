<script setup lang="ts">
import { remotePins } from '~/utils/remote'
import { cutNotice, staleNotice } from '~/utils/decisions'
import type { SessionInfo } from '~/composables/useSessions'
import { answerDigit, choiceKey, newlyNeedingInput } from '~/utils/attention'
import { sidebarRunFor } from '~/utils/crews'
import { filterSessions } from '~/utils/sessions'
import { joinedPath } from '~/utils/joined'
import { blockSubtitle, filterNeeds, LIST_SECTIONS, SECTION_TITLES, focusRows, mayRefocusRow, moveFocus, narrowSidebar, needsDotShown, panelRow, reopenNeeds, rowMeta, sectionPreview, sidebarModel, type ItemRow, type ListSection, type RunBlock, type SessionRow, type SidebarItem } from '~/utils/sidebar'
import { sessionTarget } from '~/utils/actions'
import { rowMenuItems, sessionLive } from '~/utils/sidebarActions'
import { replyFailure, replyOutcomeOf, type ReplyTaken } from '~/utils/reply'
import { ROW_DRAFTS, RowDrafts } from '~/utils/rowDrafts'
import { needsCount } from '~/utils/needsYou'
import { showingWords } from '~/utils/inbox'
import { pinRows } from '~/utils/pins'

/**
 * The full sidebar (design 3a, 3b): sessions and runs, ordered by how hard each waits on you. Needs you, Finished (an agent that ended
 * its turn waits for your next word) and Working (an agent in a turn, or one that sends no signal), each holding loose sessions and run
 * blocks side by side; a run is one block, placed where its most urgent member is, or in Needs you while an
 * item of it waits. Needs you lists every kind that waits on a person (SidebarInboxRow). Then the links shared with
 * you, below your own sessions, and Exited, folded to one line. Every section folds from its header (useSidebarFolds). The run of
 * the page open now is marked and scrolled to; nothing is filtered out for it.
 */
/** `page`: the phone's home screen (pages/sessions/index.vue) rather than the sidebar: full-width 44 px answers, the list named so. */
const props = withDefaults(defineProps<{ page?: boolean }>(), { page: false })
const attention = useAttention()
const events = useEvents()
const route = useRoute()
const router = useRouter()
// A row opens its session's place: a crew's member inside its crew (UX3 R72).
const placeOf = useSessionPlace()
const launch = useLaunchModal()
const joined = useJoined()
const foldState = useSidebarFolds()
const api = useSessions()
const toast = useToast()
const quick = useQuickReply()
const renameModal = useRenameModal()
const cloneModal = useCloneModal()

const query = ref('')
const filterInput = useTemplateRef<{ inputRef?: HTMLInputElement }>('filterInput')
const now = useNow()
// A row's More menu stays open while its row moves between sections (useRowMenu): the list keeps which one is open.
provideRowMenu()

// Needs you lists every item of the one list of what needs you (useNeedsYou, the UX review's P3): the sessions' questions as their
// rows, a run's decisions and tasks under its header, a plan (and an item of a run the store does not hold) as a loose row. Its header
// counts the one number, the nav's.
const oneList = useNeedsYou()
// The rows' reply lines, by session (RowDrafts, #654): kept here, past the field that draws one, at most one per session listed.
const rowDrafts = reactive(new RowDrafts())
provide(ROW_DRAFTS, rowDrafts)
watch(
  () => attention.sessions.value.map((s) => s.id),
  (ids) => rowDrafts.keep(ids),
)
const model = computed(() => sidebarModel(filterSessions(attention.sessions.value, query.value), attention.runOf, filterNeeds(oneList.items.value, query.value)))
// The pinned sessions, first (utils/pins.ts): a pin no longer listed is let go a week after its pinning, once the list is read.
const uiState = useUiState()
const pinned = computed(() => pinRows(uiState.pins.value, attention.sessions.value, now.value))
watch(
  () => pinned.value.kept.length,
  (n) => {
    if (attention.loaded.value && n < uiState.pins.value.length) uiState.setPins(pinned.value.kept)
  },
)
const needsWords = computed(() => needsCount(oneList.list.value))
const needsDot = computed(() => needsDotShown(events.routes.value))
const decisions = useDecisions()
const decisionModal = useDecisionModal()
/** The decisions Needs you lists, a run's among them. */
const decisionRows = computed(() => {
  let n = 0
  for (const it of model.value.needs) n += it.kind === 'run' ? it.items.filter((x) => x.item.kind === 'decision').length : it.kind === 'item' && it.item.kind === 'decision' ? 1 : 0
  return n
})
// The list read is cut: said even when none of what was read is listed (every one of them mirrored), so Needs you never reads as
// whole when it is not.
const decisionsCut = computed(() => (query.value ? '' : cutNotice(decisions.cut.value, decisionRows.value)))
// The list's last read failed (Codex's round 3 on #719): said, with Read again, until a read succeeds; the store reads again by itself.
const decisionsStale = computed(() => (query.value ? '' : staleNotice(decisions.stale.value.since, decisions.stale.value.error)))
// The inbox (plans, tasks) holds a next page: "Showing 150 of 212 · all on Needs you"; its read failed: said, with Read again, the
// questions still listed.
const inbox = useInbox()
const inboxShowing = computed(() => (query.value || !inbox.state.value.next ? '' : showingWords(inbox.state.value.items.length, inbox.state.value.total)))
const inboxFailed = computed(() => !query.value && !!inbox.state.value.error && !inbox.state.value.unavailable)
// Judged once the sessions are read (P23): before the first read the store's emptiness says nothing.
const empty = computed(() => attention.loaded.value && attention.sessions.value.length === 0 && joined.list.value.length === 0)
const folds = foldState.folds
/** The run of the page open now: the run page, or a member's page. */
const openRun = computed(() => sidebarRunFor(route.path, attention.sessions.value))

function preview(key: ListSection) {
  return sectionPreview(model.value[key])
}
function isRun(it: SidebarItem) {
  return it.kind === 'run'
}

// Needs you opens again by itself when a new prompt arrives, so folding it never hides one. The first list a page sees is
// what was already there (the store filling after a load), not news: it primes the comparison and reopens nothing.
let seen = new Map<string, SessionInfo>(attention.sessions.value.map((s) => [s.id, s]))
let primed = seen.size > 0
watch(attention.sessions, (list) => {
  const next = new Map(list.map((s) => [s.id, s]))
  const fresh = primed ? newlyNeedingInput(seen, next) : []
  seen = next
  primed ||= next.size > 0
  const after = reopenNeeds(folds.value, fresh)
  if (after !== folds.value) foldState.set(after)
})

// The open run's section unfolds and its block comes into view, once per page.
function sectionOfRun(runId: string): ListSection | undefined {
  for (const key of LIST_SECTIONS) if (model.value[key].some((it) => it.kind === 'run' && it.runId === runId)) return key
  return undefined
}
watch(
  openRun,
  async (runId) => {
    if (!runId) return
    const key = sectionOfRun(runId)
    if (key) foldState.unfold(key)
    await nextTick()
    document.querySelector('[data-sidebar-run-open]')?.scrollIntoView({ block: 'nearest' })
  },
  { immediate: true },
)

// The row actions (design 3c): Share opens the one dialog on the row's target; Stop stops after the row asked; the Yard
// focuses the session there; a member's "Open the run" goes to its run page.
const busy = ref<Set<string>>(new Set())
function mark(key: string, on: boolean) {
  const next = new Set(busy.value)
  if (on) next.add(key)
  else next.delete(key)
  busy.value = next
}
const shareTarget = ref<{ sessionId: string; sessionName?: string } | { runId: string; sessionName?: string } | null>(null)
const shareOpen = ref(false)
/** The dialog mints its link when it opens: it is mounted on the target first, then opened, so that it sees the opening. */
async function openShare(target: NonNullable<typeof shareTarget.value>) {
  shareOpen.value = false
  shareTarget.value = target
  await nextTick()
  shareOpen.value = true
}
function shareSession(row: SessionRow) {
  openShare({ sessionId: row.session.id, sessionName: row.session.name })
}
function shareRun(block: RunBlock) {
  openShare({ runId: block.runId, sessionName: block.title })
}
async function stopSession(row: SessionRow) {
  mark(row.id, true)
  try {
    await api.stop(row.session.id)
    toast.add({ title: `Stopping ${row.session.name}`, icon: 'i-lucide-square', color: 'neutral' })
  } catch (e) {
    toast.add({ title: `Stop ${row.session.name} failed`, description: (e as Error).message, color: 'error' })
  } finally {
    mark(row.id, false)
  }
}
async function stopRun(block: RunBlock) {
  mark(`run:${block.runId}`, true)
  try {
    attention.applyRun(await api.stopRun(block.runId))
    toast.add({ title: `Stopping ${block.title}`, description: "Every member's session is stopped.", icon: 'i-lucide-square', color: 'neutral' })
  } catch (e) {
    toast.add({ title: `Stop ${block.title} failed`, description: (e as Error).message, color: 'error' })
  } finally {
    mark(`run:${block.runId}`, false)
  }
}
function showInYard(row: SessionRow) {
  router.push({ path: '/yard', query: { focus: row.session.id } })
}
function openRunOf(row: SessionRow) {
  if (row.session.crew) router.push(`/runs/${encodeURIComponent(row.session.crew.runId)}`)
}

// Answering in the row (design 3b, 3c): a choice sends its keys, a typed line is submitted, over the Yard's short-lived connection
// (the relay for a hosted session); the stream clears the prompt and the row moves on by itself. Busy is the Yard's set too, so a
// card there and the row here agree. Each answer names the question the row showed as it was clicked, typed or keyed: one that
// another person answered first takes nothing, and the toast says it was answered already.
const busyAll = computed(() => new Set([...busy.value, ...quick.sending.value]))
function fail(name: string, line = false, way?: string) {
  return (e: unknown) => toast.add(replyFailure(e, name, line, way))
}
function answer(row: SessionRow, i: number, question: string | undefined) {
  const o = row.prompt?.options[i]
  if (o) quick.choose(row.session, o, { question }).catch(fail(row.session.name, false, row.session.way))
}
/** A line from a row's field: `taken` tells the field what became of it, so that a line not sent stays in it (#654). */
function reply(row: SessionRow, text: string, question: string | undefined, taken?: ReplyTaken) {
  quick.reply(row.session, text, { question, sent: () => taken?.('left') }).then(
    (sent) => taken?.(sent ? 'sent' : 'held'),
    (e: unknown) => {
      fail(row.session.name, true)(e)
      taken?.(replyOutcomeOf(e))
    },
  )
}

function focusFilter() {
  filterInput.value?.inputRef?.focus()
}

// The keys (design 3c): one handler on the list, live while a row has the focus (↓ from the filter puts the first there, a
// click or Tab any). ↑ ↓ or J K move, Enter opens, 1–9 answer the row's prompt, R opens a member's run, S shares, X asks to
// stop (the question's Stop takes the focus: Enter stops, Escape cancels), Escape leaves the list. Every key the list owns
// stops here, so the Yard's J K and the quick reply's digits never see it; a field inside a row keeps its keys (the reply field
// stops its own), Escape excepted. Focus is by row id, so a row that moves sections after an answer keeps it.
const listEl = useTemplateRef<HTMLElement>('listEl')
const focused = ref<string | null>(null)
/** The row asking whether to stop (`s:<id>`, `r:<runId>`): one at a time, from the keys or a hover Stop. */
const confirmingId = ref<string | null>(null)
/** The runs whose "and 2 more" line was pressed: every member shows, for the visit (kept here, never stored). */
const unfolded = ref<Set<string>>(new Set())
const rows = computed(() => focusRows(model.value, joined.list.value, folds.value, unfolded.value))
/** Unfolds a run's members; the focus, when it was on the line, goes to the first member the line held. */
async function unfoldRun(runId: string) {
  const had = focused.value === `m:${runId}`
  const before = new Set(rows.value.map((r) => r.id))
  unfolded.value = new Set([...unfolded.value, runId])
  if (!had) return
  await nextTick()
  const next = rows.value.find((r) => r.kind === 'session' && r.runId === runId && !before.has(r.id))
  focusRow(next?.id ?? `r:${runId}`)
}
const byId = computed(() => {
  const m = new Map<string, SessionRow | RunBlock>()
  for (const key of LIST_SECTIONS) {
    for (const it of model.value[key]) {
      if (it.kind === 'run') {
        m.set(`r:${it.runId}`, it)
        for (const member of it.members) m.set(`s:${member.id}`, member)
      } else if (it.kind === 'session') m.set(`s:${it.id}`, it)
    }
  }
  return m
})
function rowEl(id: string, within = 'a[href]'): HTMLElement | null {
  // An item's row (a decision's, a plan's, a task's) and a run's "and 2 more" line are buttons: a card opens, the run unfolds, in
  // place, or the item opens on the page.
  const what = (id.startsWith('d:') || id.startsWith('i:') || id.startsWith('m:')) && within === 'a[href]' ? 'button' : within
  return listEl.value?.querySelector<HTMLElement>(`[data-sidebar-row="${CSS.escape(id)}"] ${what}`) ?? null
}
function focusRow(id: string | null) {
  focused.value = id
  if (id) rowEl(id)?.focus()
}
/** From the filter's ↓: the first row. */
function focusList() {
  focusRow(moveFocus(rows.value, null, 1))
}
function onFocusIn(e: FocusEvent) {
  const row = (e.target as HTMLElement).closest('[data-sidebar-row]')
  if (row) focused.value = row.getAttribute('data-sidebar-row')
}
/** Whether `node` is in the list or in the question's panel beside it (a portal of its own): the panel counts as the list's. */
function inList(node: EventTarget | null): boolean {
  return !!listEl.value?.contains(node as Node | null) || !!(node as Element | null)?.closest?.('[data-row-panel]')
}
function onFocusOut(e: FocusEvent) {
  // The focus left the list; the question, if one shows, stays until answered (a hover Stop's button unmounts under the click).
  // A row that re-renders under the focus (it moved sections after an answer) gets it back by id first (the watch below), so
  // the focus counts as gone only once nothing in the list has it after that. Typing in the panel's field keeps the row focused.
  if (inList(e.relatedTarget)) return
  window.setTimeout(() => {
    if (!inList(document.activeElement)) focused.value = null
  }, 0)
}
watch(rows, async () => {
  const id = focused.value
  if (!id) return
  await nextTick()
  const el = rowEl(id)
  // Only a focus that was lost, and never behind a modal: a dialog opened from the row keeps it, and its Escape with it, and a
  // row's reply field keeps it and its keys (#596).
  if (el && !el.contains(document.activeElement) && mayRefocusRow(document.activeElement, document.body, !!el.closest('[aria-hidden="true"]'))) el.focus()
})
function openRow(id: string) {
  const f = rows.value.find((r) => r.id === id)
  if (!f) return
  if (f.kind === 'session') router.push(placeOf(f.sessionId))
  else if (f.kind === 'run') router.push(`/runs/${encodeURIComponent(f.runId)}`)
  else if (f.kind === 'decision') decisionModal.open(f.decision)
  else if (f.kind === 'item') router.push(`/decisions?item=${encodeURIComponent(f.ref)}`)
  else if (f.kind === 'more') void unfoldRun(f.runId)
  else {
    const e = joined.list.value.find((x) => x.id === f.entryId)
    if (e) router.push(joinedPath(e))
  }
}
function liveRow(row: SessionRow | RunBlock): boolean {
  return row.kind === 'run' ? !row.stoppedAt && row.state !== 'exited' : sessionLive(row.session.status)
}
async function askStop(id: string) {
  confirmingId.value = id
  await nextTick()
  rowEl(id, '[data-confirm-stop]')?.focus()
}
function onKey(e: KeyboardEvent) {
  const target = e.target as HTMLElement
  // From the question's panel: Escape closes it and gives the focus back to the row; a digit answers the row as from the row (a
  // field keeps its keys); every other key works the control it is on.
  if (target.closest('[data-row-panel]')) {
    const id = focused.value
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      dismissPanel()
      if (id) focusRow(id)
      return
    }
    if (target.closest('input, textarea') || !id || e.metaKey || e.ctrlKey || e.altKey || !/^[1-9]$/.test(e.key)) return
    e.preventDefault()
    e.stopPropagation()
    // A held key's repeats are taken and answer nothing (answerDigit).
    const n = answerDigit(e)
    const row = byId.value.get(id)
    const o = n && row?.kind === 'session' ? row.prompt?.options[n - 1] : undefined
    if (row?.kind === 'session' && o && choiceKey(o, n - 1) === n) answer(row, n - 1, row.prompt?.question)
    return
  }
  if (e.key === 'Escape') {
    e.preventDefault()
    e.stopPropagation()
    if (confirmingId.value) {
      const id = confirmingId.value
      confirmingId.value = null
      focusRow(id)
    } else {
      target.blur()
      focused.value = null
    }
    return
  }
  if (target.closest('input, textarea, select, [contenteditable]') || e.metaKey || e.ctrlKey || e.altKey) return
  // Enter on a button in a row (Answer, a choice, a run's line or its header's controls) is that button's click, never the row's open.
  if (e.key === 'Enter' && target.closest('button')) {
    e.stopPropagation()
    return
  }
  const id = focused.value
  if (!id) return
  if (confirmingId.value) {
    // The question's Stop button has the focus: Enter is its click, nothing else is a key.
    if (e.key === 'Enter') e.stopPropagation()
    return
  }
  const row = byId.value.get(id)
  const own = () => {
    e.preventDefault()
    e.stopPropagation()
  }
  switch (e.key) {
    case 'ArrowDown':
    case 'j':
    case 'J':
      own()
      focusRow(moveFocus(rows.value, id, 1))
      return
    case 'ArrowUp':
    case 'k':
    case 'K':
      own()
      focusRow(moveFocus(rows.value, id, -1))
      return
    case 'Enter':
      own()
      openRow(id)
      return
    case 'r':
    case 'R':
      if (row?.kind === 'session' && row.session.crew) {
        own()
        openRunOf(row)
      }
      return
    case 's':
    case 'S':
      if (row && liveRow(row)) {
        own()
        if (row.kind === 'run') shareRun(row)
        else shareSession(row)
      }
      return
    case 'x':
    case 'X':
      if (row && liveRow(row)) {
        own()
        askStop(id)
      }
      return
  }
  if (/^[1-9]$/.test(e.key)) {
    own()
    // The digit names the question the focused row shows as it is pressed; a choice that changes the permission mode has none, and
    // a held key's repeats answer nothing (answerDigit).
    const n = answerDigit(e)
    const o = n && row?.kind === 'session' ? row.prompt?.options[n - 1] : undefined
    if (row?.kind === 'session' && o && choiceKey(o, n - 1) === n) answer(row, n - 1, row.prompt?.question)
  }
}

// A row's sheet on a phone (design 3e): what hover offers, as 44 px buttons; Stop asks again in the row.
const sheetFor = ref<SessionRow | RunBlock | null>(null)
const sheetOpen = ref(false)
function openSheet(target: SessionRow | RunBlock) {
  sheetFor.value = target
  sheetOpen.value = true
}
const sheet = computed(() => {
  const t = sheetFor.value
  if (!t) return null
  if (t.kind === 'run') {
    return {
      title: t.title,
      meta: blockSubtitle(t),
      items: rowMenuItems({ kind: 'run', name: t.title, live: liveRow(t) }, { open: () => router.push(`/runs/${encodeURIComponent(t.runId)}`), share: () => shareRun(t), stop: () => askStop(`r:${t.runId}`) }),
    }
  }
  const s = t.session
  return {
    title: s.name,
    meta: rowMeta(s, !!s.crew, now.value).join(' · '),
    items: rowMenuItems(
      sessionTarget(s, sessionLive(s.status)),
      { open: () => router.push(placeOf(s.id, {}, s)), openRun: () => openRunOf(t), share: () => shareSession(t), yard: () => showInYard(t), rename: () => renameModal.show(s), clone: () => cloneModal.show(s), cloneWithOptions: () => void cloneModal.withOptions(s), stop: () => askStop(`s:${s.id}`) },
    ),
  }
})

// The question's panel (the UX review's P5, owner decision 7): in a sidebar narrower than 280 px (narrowSidebar, from the sidebar's
// drawn width) a row shows two lines of its question, and one panel beside the list holds it whole with its controls, for the
// focused row while it asks or the one whose Answer was pressed (panelRow). Focus opens it without moving the focus; Answer moves
// the focus into it; Escape closes it until the focus moves to another row. `data-list-width` says the width is known.
const listWidth = ref<'narrow' | 'wide' | undefined>(props.page ? 'wide' : undefined)
const narrow = computed(() => listWidth.value === 'narrow')
let widthObserver: ResizeObserver | undefined
onMounted(() => {
  if (props.page) return
  const root = document.getElementById('dashboard-sidebar-main')
  if (!root) {
    listWidth.value = 'wide'
    return
  }
  const measure = () => (listWidth.value = narrowSidebar(root.getBoundingClientRect().width) ? 'narrow' : 'wide')
  measure()
  widthObserver = new ResizeObserver(measure)
  widthObserver.observe(root)
})
onBeforeUnmount(() => widthObserver?.disconnect())
/** The row whose Answer was pressed; the row whose panel Escape closed. Both give way when the focus moves to another row. */
const pressed = ref<string | null>(null)
const dismissed = ref<string | null>(null)
watch(focused, (id) => {
  if (pressed.value && pressed.value !== id) pressed.value = null
  if (dismissed.value && dismissed.value !== id) dismissed.value = null
})
const panelFor = computed(() => {
  const id = panelRow(focused.value, pressed.value, byId.value, narrow.value, confirmingId.value)
  return id && id !== dismissed.value ? id : null
})
const panelSession = computed(() => (panelFor.value ? (byId.value.get(panelFor.value) as SessionRow | undefined) : undefined))
/** The row the panel is anchored to, found once the list has drawn it. */
const panelAnchor = ref<HTMLElement | null>(null)
watch(
  panelFor,
  (id) => {
    panelAnchor.value = id ? (listEl.value?.querySelector<HTMLElement>(`[data-sidebar-row="${CSS.escape(id)}"]`) ?? null) : null
  },
  { flush: 'post' },
)
// A row that moves sections is drawn again: the panel follows it to its new element.
watch(rows, async () => {
  if (!panelFor.value) return
  await nextTick()
  const el = listEl.value?.querySelector<HTMLElement>(`[data-sidebar-row="${CSS.escape(panelFor.value)}"]`) ?? null
  if (el !== panelAnchor.value) panelAnchor.value = el
})
const panel = useTemplateRef<{ focusFirst: () => void }>('panel')
/** The row whose Answer was pressed waits for its panel to show, then the focus moves to the panel's first control. */
let focusWhenShown: string | null = null
function openAnswer(row: SessionRow) {
  const id = `s:${row.id}`
  dismissed.value = null
  focused.value = id
  pressed.value = id
  focusWhenShown = id
  if (panelFor.value === id && panelAnchor.value) void nextTick(() => panelShown())
}
function panelShown() {
  if (focusWhenShown && focusWhenShown === panelFor.value) panel.value?.focusFirst()
  focusWhenShown = null
}
function dismissPanel() {
  dismissed.value = panelFor.value
  pressed.value = null
}

defineExpose({ focusFilter, focusList })
/** Another machine's pins (docs/plans/sync-seams.md, OS-4): after this machine's in the pinned group, greyed, opening nothing. */
const otherRemote = useRemote('pins')
const otherPins = computed(() => remotePins(otherRemote.records.value))
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <div class="flex flex-col gap-2.5 px-1 pb-2">
      <UButton label="Launch agent" icon="i-lucide-plus" block @click="launch.show()">
        <template #trailing><UKbd value="N" size="sm" class="ml-auto opacity-70" /></template>
      </UButton>
      <!-- ↓ in the filter moves into the list (the key bubbles from the field to here). The field spans the list's width, as
           Launch agent does (#702): Nuxt UI's input is inline and otherwise keeps the width of its 20 characters. -->
      <div @keydown.down.prevent="focusList()">
        <UInput ref="filterInput" v-model="query" placeholder="Filter sessions, runs, people" size="sm" icon="i-lucide-slash" class="w-full" :ui="{ base: 'font-normal' }" />
      </div>
    </div>

    <div ref="listEl" class="flex-1 min-h-0 overflow-y-auto px-1 flex flex-col gap-3" :data-session-list="props.page ? 'page' : 'sidebar'" :data-list-width="listWidth" @keydown="onKey" @focusin="onFocusIn" @focusout="onFocusOut">
      <!-- The phone's Sessions page draws the welcome in this line's place (the UX review's P17). -->
      <slot v-if="empty" name="empty"><p class="px-2 py-4 text-xs text-muted leading-relaxed" data-sidebar-empty>Nothing is running.</p></slot>

      <SidebarSection v-if="(pinned.rows.length || otherPins.length) && !query" id="pinned" :title="SECTION_TITLES.pinned" :count="pinned.rows.length + otherPins.length" :folded="folds.pinned" @update:folded="foldState.fold('pinned', $event)">
        <SidebarPinnedList :rows="pinned.rows" :now="now" :others="otherPins" />
      </SidebarSection>

      <SidebarSection v-if="model.needs.length || decisionsCut || decisionsStale || inboxShowing || inboxFailed" id="needs" title="Needs you" :count="needsWords" count-of="needs" tone="warning" :preview="preview('needs')" :folded="folds.needs" @update:folded="foldState.fold('needs', $event)">
        <ol class="flex flex-col gap-0.5">
          <template v-for="it in model.needs" :key="it.id">
            <SidebarRunBlock
              v-if="isRun(it)"
              :block="it as RunBlock"
              :now="now"
              :needs-dot="needsDot"
              :open="(it as RunBlock).runId === openRun"
              :busy="busyAll"
              :big="props.page"
              :focused-id="focused"
              :confirming-id="confirmingId"
              section="needs"
              :unfolded="unfolded.has((it as RunBlock).runId)"
              @unfold="unfoldRun"
              :narrow="narrow"
              :panel-for="panelFor"
              @answer-open="openAnswer"
              @confirm="confirmingId = $event"
              @sheet="openSheet"
              @share-run="shareRun"
              @stop-run="stopRun"
              @share="shareSession"
              @stop="stopSession"
              @yard="showInYard"
              @open-run="openRunOf"
              @answer="answer"
              @reply="reply"
            />
            <SidebarInboxRow v-else-if="it.kind === 'item'" :row="it as ItemRow" :focused="focused === it.id" :big="props.page" />
            <SidebarSessionRow
              v-else
              :row="it as SessionRow"
              :now="now"
              :needs-dot="needsDot"
              :busy="busyAll.has((it as SessionRow).id)"
              :big="props.page"
              :focused="focused === `s:${(it as SessionRow).id}`"
              :confirming="confirmingId === `s:${(it as SessionRow).id}`"
              @update:confirming="confirmingId = $event ? `s:${(it as SessionRow).id}` : null"
              :narrow="narrow"
              :panel-open="panelFor === `s:${(it as SessionRow).id}`"
              @answer-open="openAnswer"
              @share="shareSession"
              @stop="stopSession"
              @yard="showInYard"
              @open-run="openRunOf"
              @answer="answer"
              @reply="reply"
              @sheet="openSheet"
            />
          </template>
          <li v-if="decisionsCut" class="px-2 py-1 text-xs text-muted" data-sidebar-decisions-cut>{{ decisionsCut }}</li>
          <li v-if="decisionsStale" class="flex items-center gap-2 px-2 py-1 text-xs text-warning" data-sidebar-decisions-stale>
            <span class="min-w-0 flex-1">{{ decisionsStale }}</span>
            <UButton label="Read again" size="xs" color="neutral" variant="outline" class="flex-none" data-sidebar-decisions-read @click="decisions.read()" />
          </li>
          <li v-if="inboxFailed" class="flex items-center gap-2 px-2 py-1 text-xs text-warning" data-sidebar-inbox-failed>
            <span class="min-w-0 flex-1">Plans and tasks could not be read</span>
            <UButton label="Read again" size="xs" color="neutral" variant="outline" class="flex-none" data-sidebar-inbox-read @click="inbox.changed()" />
          </li>
          <li v-if="inboxShowing" class="px-2 py-1 text-xs text-muted" data-sidebar-inbox-showing>
            {{ inboxShowing }} · <NuxtLink to="/decisions" class="underline hover:text-default" data-sidebar-inbox-all>all on Needs you</NuxtLink>
          </li>
        </ol>
      </SidebarSection>

      <!-- Finished, then Working (the UX review's P4): each drawn only while it has rows. -->
      <template v-for="key in (['finished', 'working'] as const)" :key="key">
      <SidebarSection v-if="model[key].length" :id="key" :title="SECTION_TITLES[key]" :count="model.counts[key]" :preview="preview(key)" :folded="folds[key]" @update:folded="foldState.fold(key, $event)">
        <ol class="flex flex-col gap-0.5">
          <template v-for="it in model[key]" :key="it.id">
            <SidebarRunBlock
              v-if="isRun(it)"
              :block="it as RunBlock"
              :now="now"
              :needs-dot="needsDot"
              :open="(it as RunBlock).runId === openRun"
              :busy="busyAll"
              :big="props.page"
              :focused-id="focused"
              :confirming-id="confirmingId"
              :section="key"
              :unfolded="unfolded.has((it as RunBlock).runId)"
              @unfold="unfoldRun"
              :narrow="narrow"
              :panel-for="panelFor"
              @answer-open="openAnswer"
              @confirm="confirmingId = $event"
              @sheet="openSheet"
              @share-run="shareRun"
              @stop-run="stopRun"
              @share="shareSession"
              @stop="stopSession"
              @yard="showInYard"
              @open-run="openRunOf"
              @answer="answer"
              @reply="reply"
            />
            <SidebarSessionRow
              v-else
              :row="it as SessionRow"
              :now="now"
              :needs-dot="needsDot"
              :busy="busyAll.has((it as SessionRow).id)"
              :big="props.page"
              :focused="focused === `s:${(it as SessionRow).id}`"
              :confirming="confirmingId === `s:${(it as SessionRow).id}`"
              @update:confirming="confirmingId = $event ? `s:${(it as SessionRow).id}` : null"
              :narrow="narrow"
              :panel-open="panelFor === `s:${(it as SessionRow).id}`"
              @answer-open="openAnswer"
              @share="shareSession"
              @stop="stopSession"
              @yard="showInYard"
              @open-run="openRunOf"
              @answer="answer"
              @reply="reply"
              @sheet="openSheet"
            />
          </template>
        </ol>
      </SidebarSection>
      </template>

      <!-- Yours first, then the rest: the links joined from here sit below your own sessions. -->
      <SidebarSection v-if="joined.list.value.length" id="shared" title="Shared with you" :count="joined.list.value.length" :folded="folds.shared" @update:folded="foldState.fold('shared', $event)">
        <SidebarSharedList :focused-id="focused" />
      </SidebarSection>

      <SidebarSection v-if="model.exited.length" id="exited" title="Exited" :count="model.counts.exited" :preview="preview('exited')" :folded="folds.exited" @update:folded="foldState.fold('exited', $event)">
        <ol class="flex flex-col gap-0.5">
          <template v-for="it in model.exited" :key="it.id">
            <SidebarRunBlock
              v-if="isRun(it)"
              :block="it as RunBlock"
              :now="now"
              :needs-dot="needsDot"
              :open="(it as RunBlock).runId === openRun"
              :busy="busyAll"
              :big="props.page"
              :focused-id="focused"
              :confirming-id="confirmingId"
              section="exited"
              :unfolded="unfolded.has((it as RunBlock).runId)"
              @unfold="unfoldRun"
              :narrow="narrow"
              :panel-for="panelFor"
              @answer-open="openAnswer"
              @confirm="confirmingId = $event"
              @sheet="openSheet"
              @share-run="shareRun"
              @stop-run="stopRun"
              @share="shareSession"
              @stop="stopSession"
              @yard="showInYard"
              @open-run="openRunOf"
              @answer="answer"
              @reply="reply"
            />
            <SidebarSessionRow
              v-else
              :row="it as SessionRow"
              :now="now"
              :needs-dot="needsDot"
              :busy="busyAll.has((it as SessionRow).id)"
              :big="props.page"
              :focused="focused === `s:${(it as SessionRow).id}`"
              :confirming="confirmingId === `s:${(it as SessionRow).id}`"
              @update:confirming="confirmingId = $event ? `s:${(it as SessionRow).id}` : null"
              :narrow="narrow"
              :panel-open="panelFor === `s:${(it as SessionRow).id}`"
              @answer-open="openAnswer"
              @share="shareSession"
              @stop="stopSession"
              @yard="showInYard"
              @open-run="openRunOf"
              @answer="answer"
              @reply="reply"
              @sheet="openSheet"
            />
          </template>
        </ol>
      </SidebarSection>
    </div>

    <!-- One panel for the list: the question whole beside its row, in a narrow sidebar. -->
    <SidebarRowPanel
      v-if="panelSession"
      :key="panelSession.id"
      ref="panel"
      :row="panelSession"
      :anchor="panelAnchor"
      :busy="busyAll.has(panelSession.id)"
      @answer="answer"
      @reply="reply"
      @keys="onKey"
      @leave="onFocusOut"
      @shown="panelShown"
    />
    <SidebarRowSheet v-if="sheet" v-model:open="sheetOpen" :title="sheet.title" :meta="sheet.meta" :items="sheet.items" />
    <!-- One Share dialog for every row: made afresh for each target (the dialog mints its link on open). -->
    <ShareLinksModal v-if="shareTarget" :key="'sessionId' in shareTarget ? shareTarget.sessionId : shareTarget.runId" v-model:open="shareOpen" v-bind="shareTarget" />
  </div>
</template>
