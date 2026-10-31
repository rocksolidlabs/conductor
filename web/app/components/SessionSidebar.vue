<script setup lang="ts">
import type { SessionInfo } from '~/composables/useSessions'
import { choiceKey, newlyNeedingInput } from '~/utils/attention'
import { sidebarRunFor } from '~/utils/crews'
import { filterSessions } from '~/utils/sessions'
import { joinedPath } from '~/utils/joined'
import { blockSubtitle, focusRows, moveFocus, needsDotShown, reopenNeeds, rowMeta, sectionPreview, sidebarModel, type ListSection, type RunBlock, type SessionRow, type SidebarItem } from '~/utils/sidebar'
import { rowMenuItems, sessionLive } from '~/utils/sidebarActions'
import { replyFailure } from '~/utils/reply'

/**
 * The full sidebar (design 3a, 3b): sessions and runs, ordered by what needs you. Two sections, Needs you then Running, each holding
 * loose sessions and run blocks side by side; a run is one block, placed where its most urgent member is. Then the links shared with
 * you, below your own sessions, and Exited, folded to one line. Every section folds from its header (useSidebarFolds). The run of
 * the page open now is marked and scrolled to; nothing is filtered out for it.
 */
/** `page`: the phone's home screen (pages/sessions/index.vue) rather than the sidebar: full-width 44 px answers, the list named so. */
const props = withDefaults(defineProps<{ page?: boolean }>(), { page: false })
const attention = useAttention()
const events = useEvents()
const route = useRoute()
const router = useRouter()
const launch = useLaunchModal()
const joined = useJoined()
const foldState = useSidebarFolds()
const api = useSessions()
const toast = useToast()
const quick = useQuickReply()
const renameModal = useRenameModal()

const query = ref('')
const filterInput = useTemplateRef<{ inputRef?: HTMLInputElement }>('filterInput')
const now = ref(Date.now())
let tick: number | undefined

const model = computed(() => sidebarModel(filterSessions(attention.sessions.value, query.value), attention.runOf))
const needsDot = computed(() => needsDotShown(events.routes.value))
const empty = computed(() => attention.sessions.value.length === 0 && joined.list.value.length === 0)
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
  for (const key of ['needs', 'running', 'exited'] as const) if (model.value[key].some((it) => it.kind === 'run' && it.runId === runId)) return key
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
function fail(name: string) {
  return (e: unknown) => toast.add(replyFailure(e, name))
}
function answer(row: SessionRow, i: number, question: string | undefined) {
  const o = row.prompt?.options[i]
  if (o) quick.send(row.session, o.input, { question }).catch(fail(row.session.name))
}
function reply(row: SessionRow, text: string, question: string | undefined) {
  quick.reply(row.session, text, { question }).catch(fail(row.session.name))
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
const rows = computed(() => focusRows(model.value, joined.list.value, folds.value))
const byId = computed(() => {
  const m = new Map<string, SessionRow | RunBlock>()
  for (const key of ['needs', 'running', 'exited'] as const) {
    for (const it of model.value[key]) {
      if (it.kind === 'run') {
        m.set(`r:${it.runId}`, it)
        for (const member of it.members) m.set(`s:${member.id}`, member)
      } else m.set(`s:${it.id}`, it)
    }
  }
  return m
})
function rowEl(id: string, within = 'a[href]'): HTMLElement | null {
  return listEl.value?.querySelector<HTMLElement>(`[data-sidebar-row="${CSS.escape(id)}"] ${within}`) ?? null
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
function onFocusOut(e: FocusEvent) {
  // The focus left the list; the question, if one shows, stays until answered (a hover Stop's button unmounts under the click).
  // A row that re-renders under the focus (it moved sections after an answer) gets it back by id first (the watch below), so
  // the focus counts as gone only once nothing in the list has it after that.
  if (listEl.value?.contains(e.relatedTarget as Node | null)) return
  window.setTimeout(() => {
    if (!listEl.value?.contains(document.activeElement)) focused.value = null
  }, 0)
}
watch(rows, async () => {
  const id = focused.value
  if (!id) return
  await nextTick()
  const el = rowEl(id)
  if (el && !el.contains(document.activeElement)) el.focus()
})
function openRow(id: string) {
  const f = rows.value.find((r) => r.id === id)
  if (!f) return
  if (f.kind === 'session') router.push(`/sessions/${f.sessionId}`)
  else if (f.kind === 'run') router.push(`/runs/${encodeURIComponent(f.runId)}`)
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
  const n = Number(e.key)
  if (Number.isInteger(n) && n >= 1 && n <= 9) {
    own()
    // The digit names the question the focused row shows as it is pressed; a choice that changes the permission mode has none.
    const o = row?.kind === 'session' ? row.prompt?.options[n - 1] : undefined
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
      { kind: 'session', name: s.name, live: sessionLive(s.status), inRun: !!s.crew, renamable: s.kind === 'server' },
      { open: () => router.push(`/sessions/${s.id}`), openRun: () => openRunOf(t), share: () => shareSession(t), yard: () => showInYard(t), rename: () => renameModal.show(s), stop: () => askStop(`s:${s.id}`) },
    ),
  }
})

defineExpose({ focusFilter, focusList })

onMounted(() => {
  tick = window.setInterval(() => (now.value = Date.now()), 30000)
})
onBeforeUnmount(() => window.clearInterval(tick))
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <div class="flex flex-col gap-2.5 px-1 pb-2">
      <UButton label="Launch agent" icon="i-lucide-plus" block @click="launch.show()">
        <template #trailing><UKbd value="N" size="sm" class="ml-auto opacity-70" /></template>
      </UButton>
      <!-- ↓ in the filter moves into the list (the key bubbles from the field to here). -->
      <div @keydown.down.prevent="focusList()">
        <UInput ref="filterInput" v-model="query" placeholder="Filter sessions, runs, people" size="sm" icon="i-lucide-slash" :ui="{ base: 'font-normal' }" />
      </div>
    </div>

    <div ref="listEl" class="flex-1 min-h-0 overflow-y-auto px-1 flex flex-col gap-3" :data-session-list="props.page ? 'page' : 'sidebar'" @keydown="onKey" @focusin="onFocusIn" @focusout="onFocusOut">
      <p v-if="empty" class="px-2 py-4 text-xs text-muted leading-relaxed">No sessions yet. Launch an agent here or run <code>conductor host</code> from your machine.</p>

      <SidebarSection v-if="model.needs.length" id="needs" title="Needs you" :count="model.counts.needs" tone="warning" :preview="preview('needs')" :folded="folds.needs" @update:folded="foldState.fold('needs', $event)">
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

      <SidebarSection v-if="model.running.length" id="running" title="Running" :count="model.counts.running" :preview="preview('running')" :folded="folds.running" @update:folded="foldState.fold('running', $event)">
        <ol class="flex flex-col gap-0.5">
          <template v-for="it in model.running" :key="it.id">
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

    <SidebarRowSheet v-if="sheet" v-model:open="sheetOpen" :title="sheet.title" :meta="sheet.meta" :items="sheet.items" />
    <!-- One Share dialog for every row: made afresh for each target (the dialog mints its link on open). -->
    <ShareLinksModal v-if="shareTarget" :key="'sessionId' in shareTarget ? shareTarget.sessionId : shareTarget.runId" v-model:open="shareOpen" v-bind="shareTarget" />
  </div>
</template>
