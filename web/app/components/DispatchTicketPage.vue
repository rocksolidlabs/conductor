<script setup lang="ts">
import type { DispatchChange, DispatchReceipt, DispatchTicket, Horizon, NewLink, OpBody, ProjectRead, TicketAttachment, TicketEdit, TicketLink, TicketRelation, TicketStatus, TicketSummary } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import { ApiError } from '~/composables/useApi'
import { DISPATCH_RETRY_MAX_MS, ticketWatch } from '~/composables/useDispatchLive'
import { refusalWords } from '~/composables/useDispatchOps'
import { AssigneePicks, HORIZONS, PRIORITIES, TICKET_STATUSES, historyEntries, horizonWord, parseTicketKey, priorityWord, projectPath, relationAdd, statusWord, ticketPath, ticketWords, validProjectKey, type RelationChoice, type UploadOutcome } from '~/utils/dispatch'
import { RUN_EVENT_WINDOW_MS } from '~/utils/runs'
import { relativeTime } from '~/utils/sessions'
import { catchUp, serially } from '~/utils/dispatchOrder'

/**
 * A ticket's page (docs/plans/dispatch.md, PR-8): its key and title, its description and acceptance criteria as Markdown with a
 * preview, its status, priority, horizon, owner and assignees, and its history as the store recorded it. Every edit is one operation on
 * the revision shown (useDispatchOps), and every read and every operation's reply goes through the page's ordering gate, the ticket's by
 * its key, a 404 among them. The live events read it again: at once when one names the ticket, after the window on any other of its
 * project, and on a snapshot, which forgets what the gate knew (a store restored from a backup); a read that fails, its first among
 * them, is asked again. An edit waits for the one before it to be shown, read again until a read answers. An editor's save is made
 * against the text it opened on, so another person's edit made meanwhile is never replaced unseen, and what is typed while it goes
 * stays. Around it (PR-10): its parent, set with a ticket picked by key or title, and its children; its relations, what it blocks and
 * what blocks it, Blocked said from the live blockers; its labels and its milestone, picked from the project's; its attachments,
 * uploaded with their progress and shown and downloaded through object URLs of their content fetched with the token; its comments; and
 * Delete and Undelete, a reference to a deleted ticket kept, inactive, and said so. And (PR-11) its close card (DispatchClosePolicy.vue):
 * what its project's policy requires and what it meets, the owner's acceptance recorded and taken back by buttons of their own, and a
 * Close the store refuses, in the policy's words, until the policy is met; its delivery and external links; and the managed runs linked
 * to it, each with the run page's task rows (DispatchRuns.vue). Every operation on the ticket goes through the one queue of its edits, an
 * attachment's, a relation's, an acceptance's and a link's among them, since each bumps the ticket's revision. One column on a phone.
 */
const props = defineProps<{ project: string; ticketKey: string }>()

const { api, gate, read, readOrAbsent } = useDispatch()
const ops = useDispatchOps(gate)
const sessions = useSessions()
const admin = useWorkbenchToken()

const ticket = ref<DispatchTicket | null>(null)
const projectRead = ref<ProjectRead | null>(null)
const changes = ref<DispatchChange[]>([])
/** The seq after which the history's next page starts; 0 when it was read whole. */
const historyNext = ref(0)
/** How many pages of the history are shown: a re-read reads them all again. */
const historyPages = ref(1)
const historyLoading = ref(false)
const agents = ref<AgentInfo[]>([])
const error = ref('')
/** No ticket has the key. */
const missing = ref(false)
const now = ref(Date.now())
/** The person's picks of the assignees, until the edits carrying them have answered (utils/dispatch.ts, AssigneePicks). */
const picks = reactive(new AssigneePicks())
/** The person's picks of the labels, the same way. */
const labelPicks = reactive(new AssigneePicks('labels'))
/** Counts the reads of the ticket the page took: the comments' earlier pages are read again with each. */
const reads = ref(0)
/** Counts the stream's starts the page saw (a snapshot): the comments forget the answers they kept with each. */
const resets = ref(0)
const deleteOpen = ref(false)

useHead({ title: computed(() => (ticket.value ? `${ticket.value.key} ${ticket.value.title}` : props.ticketKey)) })

/**
 * Reads the ticket in full; a key no ticket has says so, and is no failure to ask again. That answer goes through the gate as any reply,
 * so a slow 404 never hides the ticket a later read found (one made since the page opened). Resolves to whether the gate took the answer;
 * what is no key needs no read, and is taken.
 */
async function readTicket(): Promise<boolean> {
  const key = props.ticketKey
  if (!parseTicketKey(key)) {
    missing.value = true
    return true
  }
  return readOrAbsent(`ticket:${key}`, () => api.ticket(key), {
    apply: (t) => {
      if (key !== props.ticketKey) return
      ticket.value = t
      missing.value = false
      reads.value++
    },
    rev: (t) => t.rev,
    absent: notFound,
    gone: () => {
      if (key === props.ticketKey) missing.value = true
    },
  })
}

/** A 404, or a 400 for what is no key: the ticket or the project has no row, which reading again does not change. */
function notFound(e: unknown): boolean {
  return e instanceof ApiError && ((e.status === 404 && e.code === 'not_found') || (e.status === 400 && e.code === 'invalid_request'))
}

/** Reads the pages of the history shown, from the start, as one reply. */
async function readHistory(): Promise<void> {
  const key = props.ticketKey
  if (!parseTicketKey(key)) return
  const pages = historyPages.value
  historyLoading.value = true
  try {
    await read(
      `history:${key}`,
      async () => {
        const all: DispatchChange[] = []
        let after = 0
        let next = 0
        for (let i = 0; i < pages; i++) {
          const page = await api.history(key, after)
          all.push(...(page.changes ?? []))
          next = page.next ?? 0
          if (!next) break
          after = next
        }
        return { all, next }
      },
      (r) => {
        if (key !== props.ticketKey) return
        changes.value = r.all
        historyNext.value = r.next
      },
    )
  } catch (e) {
    if (!notFound(e)) throw e
  } finally {
    historyLoading.value = false
  }
}

/** Reads the ticket's project: its labels and milestones by name, and whether it is archived. */
async function readProject(): Promise<void> {
  const key = props.project
  if (!validProjectKey(key)) return
  try {
    await read(
      `project:${key}`,
      () => api.project(key),
      (p) => {
        if (key === props.project) projectRead.value = p
      },
      (p) => p.project.rev,
    )
  } catch (e) {
    if (!notFound(e)) throw e
  }
}

/**
 * Everything the page shows, read again: the ticket, its history, its project. Rejects when a read failed (the live store asks again);
 * else resolves to whether the gate took the ticket's answer.
 */
async function reload(): Promise<boolean> {
  const results = await Promise.allSettled([readTicket(), readHistory(), readProject()])
  const failed = results.find((r): r is PromiseRejectedResult => r.status === 'rejected')
  if (failed) {
    error.value = (failed.reason as Error).message
    throw failed.reason
  }
  error.value = ''
  const [read] = results
  return read.status === 'fulfilled' && read.value
}

/**
 * The page read through the live store (DispatchLive.ask), so that a read that fails is asked again after the window, doubled, as a
 * re-read is, until one answers, with no event: as the page opens too, when the stream is connected already and no snapshot comes. None
 * without the workbench token, which is asked for; the page is read once it is given.
 */
function load(): void {
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  live.ask(reload)
}

let tick: number | undefined
onMounted(() => {
  sessions
    .catalog()
    .then((a) => (agents.value = a))
    .catch(() => {})
  // The clock of "updated 5m ago": no read of the server.
  tick = window.setInterval(() => (now.value = Date.now()), 30_000)
})
onBeforeUnmount(() => window.clearInterval(tick))
watch(() => admin.token.value, load)

// Another ticket on the same page (a link from this one): what was shown goes, and its late replies with it.
watch(
  () => [props.project, props.ticketKey] as const,
  ([projectKey, key], [oldProject, oldKey]) => {
    gate.drop(`ticket:${oldKey}`)
    gate.drop(`history:${oldKey}`)
    if (projectKey !== oldProject) {
      gate.drop(`project:${oldProject}`)
      projectRead.value = null
    }
    ticket.value = null
    changes.value = []
    historyPages.value = 1
    historyNext.value = 0
    missing.value = false
    // The title being edited and the assignees being picked were the other ticket's.
    leaveTitle()
    picks.clear()
    labelPicks.clear()
    if (key) load()
  },
)

// The live events read the page again; its first read is made once it is subscribed to them (useDispatchLive's `first`).
const live = useDispatchLive(
  {
    ...ticketWatch(() => ticket.value?.id ?? '', () => props.project, reload),
    reset: () => {
      gate.reset()
      resets.value++
    },
  },
  load,
)

const projectRow = computed(() => projectRead.value?.project ?? null)
const archived = computed(() => !!projectRow.value?.archivedAt)
const deleted = computed(() => !!ticket.value?.deletedAt)
/** Nothing is edited on a deleted ticket or in an archived project: the store would refuse it. */
const locked = computed(() => !ticket.value || deleted.value || archived.value)

const words = computed(() => (ticket.value ? ticketWords(ticket.value) : null))
const labelsById = computed(() => new Map((projectRead.value?.labels ?? []).map((l) => [l.id, l])))
const milestonesById = computed(() => new Map((projectRead.value?.milestones ?? []).map((m) => [m.id, m])))
const entries = computed(() =>
  historyEntries(changes.value, {
    labels: new Map([...labelsById.value].map(([id, l]) => [id, l.name])),
    milestones: new Map([...milestonesById.value].map(([id, m]) => [id, m.name])),
    agents: new Map(agents.value.map((a) => [a.id, a.name])),
    tickets: ticketNames.value,
  }),
)

/** The tickets the page knows the keys of, by id, for the history's words: the parent, the children, the relations' other ends. */
const ticketNames = computed(() => {
  const t = ticket.value
  const out = new Map<string, string>()
  if (!t) return out
  if (t.parent) out.set(t.parent.id, t.parent.key)
  for (const c of t.children?.tickets ?? []) out.set(c.id, c.key)
  for (const r of t.relations ?? []) out.set(r.other.id, r.other.key)
  return out
})

/** The ticket's attachments as the browser shows them: images and downloads through object URLs of their content, fetched with the token. */
const files = useDispatchAttachments(() => ticket.value?.attachments ?? [])

/** What an edit's `make` answers when the edit is sent: the change; null when nothing is left to change; false when it must not be sent. */
type Made = TicketEdit | null | false

/**
 * Waits, before the next edit is made, until the page shows the ticket as the store holds it since an edit of it answered (catchUp): at
 * `rev`, the revision that edit committed, or a later one; with no `rev`, once a read of the ticket sent after the answer is shown. The
 * edit's reply shows it when the gate takes it; else the ticket is read now, after the commit, so the read finds it: the reply is dropped
 * when a read numbered after it (a re-read for another ticket's event) was served before its commit and answered first, and a reply
 * whose read after the commit failed carries its receipt alone. `rev` is the reply's ticket's, else its receipt's; there is none after a
 * refusal, nor for a resend answered from a receipt since pruned. A read that fails is asked again after the window, doubled up to
 * DISPATCH_RETRY_MAX_MS, as a re-read is, until one answers or the page shows the ticket some other way (the re-read of the commit's
 * event): the next edit, made on the older ticket shown, would expect its revision and be refused stale_revision, or be taken for no
 * change and never sent (an owner picked back to the name the older ticket holds), and the edit before it would stand. It stops once the
 * page shows another ticket, whose edits are not sent; a page left while the reads fail reads on until one answers, and the edits
 * waiting behind it are made then, on what the store holds, as edits made just before a page is left are.
 */
async function caughtUp(key: string, rev: number | undefined): Promise<void> {
  await catchUp({
    shown: () => rev !== undefined && (ticket.value?.rev ?? 0) >= rev,
    read: readTicket,
    left: () => key !== props.ticketKey,
    wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
    firstMs: RUN_EVENT_WINDOW_MS,
    maxMs: DISPATCH_RETRY_MAX_MS,
  })
}

/** An operation's reply: its receipt, and the ticket as read after it when the route answers one. */
interface ActReply {
  receipt: DispatchReceipt
  ticket?: DispatchTicket | null
}

/** One operation on the ticket, made when its turn comes (operate). */
interface Act {
  /**
   * The ticket the route names: the page's, unless a relation is made from the other's side ("is blocked by" is the other's blocks),
   * when no revision is expected and the page's ticket, whose revision the operation bumps too, is read after it.
   */
  on?: string
  send: (body: OpBody) => Promise<ActReply>
  /** Made on a deleted ticket: an undelete alone; every other operation is made on a live one. */
  deleted?: boolean
  /** The operation in the toasts' words: the key, a file's name. */
  what?: string
  /** A toast's title once it is done. */
  done?: string
  refused?: (receipt: DispatchReceipt) => void
}

/** What `operate` answers when the change came to nothing to send. */
const UNCHANGED = Symbol('unchanged')

/**
 * One operation on the ticket the person acted on (`key`), as one operation on the revision shown when it is sent; its reply once the
 * store has it, UNCHANGED when nothing was left to change, null when it was not done. The page's operations go one after another
 * (serially): each is sent once the one before has answered and the page shows the revision it committed (caughtUp), so it expects
 * the revision that one left, where two sent at once would both expect the revision shown at the first and the second be refused
 * stale_revision; an attachment's upload and a relation's add are among them, since each bumps the ticket's revision as an edit does.
 * The operation is made then too (`make`), against the ticket as the one before left it: null when nothing is left to change, which is
 * no operation (the store refuses one that changes nothing); false when it must not be sent (an editor's text changed by another
 * person since it opened). None is sent once the page shows another ticket, on a ticket deleted (but an undelete) or in an archived
 * project.
 */
const operate = serially(async (key: string, make: () => Act | null | false): Promise<ActReply | typeof UNCHANGED | null> => {
  const t = ticket.value
  if (!t || key !== props.ticketKey || archived.value) return null
  const act = make()
  if (act === false) return null
  if (!act) return UNCHANGED
  if (!!act.deleted !== !!t.deletedAt) return null
  const on = act.on ?? key
  const own = on === key
  let refused = false
  const reply = await ops.run<ActReply>(act.send, {
    thing: `ticket:${on}`,
    what: act.what ?? on,
    expectedRev: own ? t.rev : undefined,
    apply: (r) => {
      if (own && r.ticket && key === props.ticketKey) ticket.value = r.ticket
    },
    rev: (r) => (own ? r.ticket?.rev : undefined),
    refused: (rc) => {
      refused = true
      act.refused?.(rc)
    },
    done: act.done,
  })
  // A refusal (a stale revision: another person's edit came first) shows the page as the store holds it now, read before the next edit
  // is made, so that one expects the revision the store has and is made on what it holds; a read of the ticket that fails, or that the
  // gate drops, is asked again until one is shown, as after a commit (caughtUp). An operation through the other ticket's route moved
  // this one's revision to one its reply does not carry: the page reads it after the answer.
  if (refused) {
    if (!(await reload().catch(() => false))) await caughtUp(key, undefined)
  } else if (reply) {
    await caughtUp(key, own ? (reply.ticket?.rev ?? reply.receipt.rev) : undefined)
    void readHistory().catch(() => {})
  } else {
    // A failure that is no refusal (both replies lost to the network) may follow a commit: the ticket is read before the next edit is
    // made, so that one is made on what the store holds, as after a refusal.
    await caughtUp(key, undefined)
  }
  return reply
})

/** One edit of the ticket (operate): true once the store has it, or when nothing was left to change. */
function edit(key: string, make: () => Made): Promise<boolean> {
  return operate(key, () => {
    if (locked.value) return false
    const change = make()
    if (!change) return change
    return { send: (body: OpBody) => api.editTicket(key, { ...body, ...change }) }
  }).then((r) => r !== null)
}

// The title: shown, or edited in place (Enter saves, Escape leaves it). A save is made against the title the editor opened on: when another
// person's came meanwhile (the page reads the ticket live), it is not sent, the draft is kept and theirs is said; a second save replaces it.
// A title typed on while its save goes stays in the editor once the save is done, and Enter or Save makes nothing of it until then:
// judged before that save is done, a title typed back to the one shown would be taken for no change and the editor closed, and the save
// on its way would stand.
const editingTitle = ref(false)
const titleDraft = ref('')
/** The title the editor opened on, or the other person's once said, or the one saved while it stayed open: what a save is made against. */
const titleBase = ref('')
/** Another person's title, saved while this one was edited: said once a save found it, until the editor closes. */
const titleTheirs = ref<string | null>(null)
/** A save of the title on its way, until it has answered: no other is made meanwhile, and the draft stays (saveTitle). */
const titleSaving = ref(false)
/** Each opening of the editor, counted: a save's answer acts on the editor only while it is the opening the save was made in. */
let titleOpened = 0
const titleInput = useTemplateRef<{ inputRef?: HTMLInputElement }>('titleInput')
function editTitle() {
  if (!ticket.value || locked.value) return
  titleDraft.value = ticket.value.title
  titleBase.value = ticket.value.title
  titleTheirs.value = null
  titleOpened++
  editingTitle.value = true
  void nextTick(() => titleInput.value?.inputRef?.select())
}
function leaveTitle() {
  editingTitle.value = false
  titleTheirs.value = null
}
/** The ticket holds another title than the one the editor opened on: it is said, and is what the next save is made against. */
function titleChangedUnder(): boolean {
  const shown = ticket.value?.title
  if (shown === undefined || shown === titleBase.value) return false
  titleTheirs.value = shown
  titleBase.value = shown
  return true
}
async function saveTitle(e?: KeyboardEvent) {
  // Enter that ends an input method's composition is the composition's.
  if (e?.isComposing) return
  // A save on its way: the draft stays, to be saved by its own Enter once that one has answered, against the title it left.
  if (titleSaving.value) return
  const title = titleDraft.value.trim()
  if (!ticket.value || !title || title === ticket.value.title) {
    leaveTitle()
    return
  }
  if (titleChangedUnder()) return
  const base = titleBase.value
  const draft = titleDraft.value
  const opened = titleOpened
  const made = () => {
    const shown = ticket.value?.title
    if (shown !== base) return false
    return title !== shown ? { set: { title } } : null
  }
  titleSaving.value = true
  const done = await edit(props.ticketKey, made).finally(() => (titleSaving.value = false))
  // The editor left, or opened again, while the save went: what it holds now is not this save's to close.
  if (!editingTitle.value || opened !== titleOpened) return
  if (!done) titleChangedUnder()
  else if (titleDraft.value === draft) leaveTitle()
  else {
    // Typed on while the save went: the newer title stays, and its save is made against the one saved.
    titleBase.value = title
    titleTheirs.value = null
  }
}

/**
 * A text field saved, made against `base`, the text its editor opened on: none when the ticket holds the text already, and not sent when
 * the field no longer holds `base` (another person's text came while this waited its turn), which the editor then shows.
 */
function saveText(field: 'description' | 'acceptance', text: string, base: string): Promise<boolean> {
  return edit(props.ticketKey, () => {
    const shown = ticket.value?.[field] ?? ''
    if (shown !== base) return false
    return text !== shown ? { set: { [field]: text } } : null
  })
}

const statusItems = TICKET_STATUSES.map((s) => ({ label: statusWord(s).label, value: s, icon: statusWord(s).icon }))
const priorityItems = PRIORITIES.map((p) => ({ label: p.label, value: p.value, icon: p.icon }))
// A select's value is never empty: not planned goes as its name.
const horizonItems = HORIZONS.map((h) => ({ label: h.label, value: h.name, icon: h.icon }))

function setStatus(v: unknown) {
  if (typeof v !== 'string') return
  void edit(props.ticketKey, () => (v !== ticket.value?.status ? { set: { status: v as TicketStatus } } : null))
}
function setPriority(v: unknown) {
  if (typeof v !== 'number') return
  void edit(props.ticketKey, () => (v !== ticket.value?.priority ? { set: { priority: v } } : null))
}
function setHorizon(v: unknown) {
  const h = HORIZONS.find((x) => x.name === v)?.value
  if (h === undefined) return
  void edit(props.ticketKey, () => (h !== ticket.value?.horizon ? { set: { horizon: h as Horizon } } : null))
}
/**
 * Every pick of the owner, the name shown among them (PersonPicker): picked while the edit of another name is on its way, it is made after
 * that one and takes it back; when its turn comes and the ticket holds the name already, nothing is sent.
 */
function setOwner(name: string) {
  void edit(props.ticketKey, () => (name !== ticket.value?.owner ? { set: { owner: name } } : null))
}

/** What the assignee picker shows: the ticket's assignees with the person's pending picks applied. */
const shownAssignees = computed(() => picks.shown(ticket.value?.assignees ?? []))
/**
 * The picker's new list, against the list it showed: each member ticked or unticked is a pick, made on the ticket when its edit is sent
 * from the picks pending then, so a second tick while the first one's edit is on its way keeps both, and a member ticked and unticked
 * before its edit went sends nothing. The picks stay shown until the edit that carried them has answered.
 */
function setAssignees(next: string[]) {
  const made = picks.pick(shownAssignees.value, next)
  if (!made.size) return
  let carried: ReadonlyMap<string, number> = new Map()
  void edit(props.ticketKey, () => {
    const t = picks.take(ticket.value?.assignees ?? [])
    carried = t.carried
    return t.edit
  }).finally(() => {
    picks.settle(carried)
    picks.settle(made)
  })
}

/** What the label picker shows: the ticket's labels with the person's pending picks applied. */
const shownLabels = computed(() => labelPicks.shown(ticket.value?.labels ?? []))
/** The picker's new labels, against those it showed: picked as the assignees are (setAssignees), one operation for what is pending. */
function setLabels(next: string[]) {
  const made = labelPicks.pick(shownLabels.value, next)
  if (!made.size) return
  let carried: ReadonlyMap<string, number> = new Map()
  void edit(props.ticketKey, () => {
    const t = labelPicks.take(ticket.value?.labels ?? [])
    carried = t.carried
    return t.edit
  }).finally(() => {
    labelPicks.settle(carried)
    labelPicks.settle(made)
  })
}

/** The milestone, by its id ('' for none). */
function setMilestone(id: string) {
  void edit(props.ticketKey, () => (id !== ticket.value?.milestoneId ? { set: { milestone: id } } : null))
}

/** The parent, by its key ('' for none): a cycle, or a chain of more than 8, is refused in the store's words. */
function setParent(key: string) {
  void edit(props.ticketKey, () => (key !== (ticket.value?.parent?.key ?? '') ? { set: { parent: key } } : null))
}
/** The tickets not to offer as the parent: this one and its children (a child as its parent would be a cycle). */
const parentExclude = computed(() => [ticket.value?.id ?? '', ...(ticket.value?.children?.tickets ?? []).map((c) => c.id)])

/** A relation added, from this ticket's side, or from the other's for "is blocked by". */
async function addRelation(choice: RelationChoice, other: TicketSummary): Promise<boolean> {
  const key = props.ticketKey
  const r = relationAdd(choice, key, other.key)
  const reply = await operate(key, () => (locked.value ? false : { on: r.on, what: key, send: (body: OpBody) => api.addRelation(r.on, { ...body, kind: r.kind, to: r.to }) }))
  return reply !== null
}
/** A relation removed, from this ticket's side whichever end it is. */
async function removeRelation(rel: TicketRelation): Promise<boolean> {
  const key = props.ticketKey
  const reply = await operate(key, () => (locked.value ? false : { what: key, send: (body: OpBody) => api.removeRelation(key, rel.id, body) }))
  return reply !== null
}

/** A file attached, its progress said as it goes: done, swept before its row was written (offered again), or failed. */
async function upload(file: File, progress: (sent: number, total: number) => void): Promise<UploadOutcome> {
  const key = props.ticketKey
  let refusal = ''
  const reply = await operate(key, () =>
    locked.value ? false : { what: file.name, send: (body: OpBody) => api.addAttachment(key, body, file, progress), refused: (rc) => (refusal = rc.refusal ?? '') },
  )
  if (reply !== null) return 'done'
  return refusal === 'expired' ? 'expired' : 'failed'
}
async function removeAttachment(a: TicketAttachment): Promise<boolean> {
  const key = props.ticketKey
  const reply = await operate(key, () => (locked.value ? false : { what: a.name, send: (body: OpBody) => api.removeAttachment(key, a.id, body) }))
  return reply !== null
}

/**
 * One operation on the ticket that the section which asked says the outcome of in place: '' once the store has it, else why not, in the
 * refusal's words (the close policy's, a run another ticket holds), else that it was not made. A failure the store did not answer is said
 * in a toast too (useDispatchOps).
 */
async function attempt(make: (key: string) => Act | null | false): Promise<string> {
  const key = props.ticketKey
  let words = ''
  const reply = await operate(key, () => {
    const act = make(key)
    if (!act) return act
    return { ...act, refused: (rc: DispatchReceipt) => (words = refusalWords(rc, rc.error || 'The store refused it.')) }
  })
  if (reply !== null) return ''
  return words || 'It was not made: see the message.'
}

/** The owner's acceptance recorded, for the owner the ticket has now and against its criteria as they stand, with the note. */
async function accept(note: string): Promise<boolean> {
  const n = note.trim()
  return !(await attempt((key) => (locked.value ? false : { send: (body: OpBody) => api.accept(key, { ...body, ...(n ? { note: n } : {}) }), done: `${key} accepted` })))
}
/** The acceptance on record taken back; no status moves with it. */
async function unaccept(): Promise<boolean> {
  return !(await attempt((key) => (locked.value || !ticket.value?.acceptedAt ? false : { send: (body: OpBody) => api.unaccept(key, body), done: `${key}: acceptance taken back` })))
}
/** The ticket moved to Done: the store applies the close policy, its refusal said in the policy's words. */
function closeTicket(): Promise<string> {
  return attempt((key) => {
    if (locked.value) return false
    if (ticket.value?.status === 'done') return null
    return { send: (body: OpBody) => api.editTicket(key, { ...body, set: { status: 'done' } }), done: `${key} done` }
  })
}
/** A link made outward: a delivery or an external URL, or a run. */
function addLink(link: NewLink): Promise<string> {
  return attempt((key) => (locked.value ? false : { send: (body: OpBody) => api.addLink(key, { ...body, ...link }) }))
}
function linkRun(run: string): Promise<string> {
  return addLink({ kind: 'run', ref: run })
}
async function removeLink(l: TicketLink): Promise<boolean> {
  return !(await attempt((key) => (locked.value ? false : { send: (body: OpBody) => api.removeLink(key, l.id, body) })))
}

/** The ticket deleted: a tombstone, its key kept, every reference to it kept, inactive, until its undelete. */
function deleteTicket() {
  deleteOpen.value = false
  const key = props.ticketKey
  void operate(key, () => (locked.value ? false : { send: (body: OpBody) => api.deleteTicket(key, body), done: `${key} deleted` }))
}
/** The ticket restored whole, every reference to it with it. */
function undeleteTicket() {
  const key = props.ticketKey
  void operate(key, () => ({ deleted: true, send: (body: OpBody) => api.undeleteTicket(key, body), done: `${key} restored` }))
}

const comments = useTemplateRef<{ insert: (text: string) => void }>('comments')

function moreHistory() {
  historyPages.value++
  void readHistory().catch((e: Error) => (error.value = e.message))
}

/** The Markdown fields' bounds, the store's, in bytes. */
const MAX_DESCRIPTION = 64 << 10
const MAX_ACCEPTANCE = 16 << 10
</script>

<template>
  <UDashboardPanel id="dispatch-ticket" :ui="{ body: 'p-0 sm:p-0 gap-0' }">
    <template #header>
      <UDashboardNavbar :toggle="false" :ui="{ left: 'min-w-0 flex-1' }" data-ticket-header>
        <template #leading>
          <UButton icon="i-lucide-arrow-left" color="neutral" variant="ghost" :aria-label="`Back to ${project}`" :to="projectPath(project)" data-back-to-project />
        </template>
        <template #title>
          <span class="flex min-w-0 items-baseline gap-2">
            <NuxtLink to="/dispatch" class="hidden text-muted hover:underline sm:inline">Dispatch</NuxtLink>
            <span class="hidden text-dimmed sm:inline">/</span>
            <NuxtLink :to="projectPath(project)" class="flex-none text-muted hover:underline" data-ticket-project>{{ project }}</NuxtLink>
            <span class="text-dimmed">/</span>
            <span class="flex-none font-mono" data-ticket-key>{{ ticketKey }}</span>
          </span>
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <div v-if="error" class="flex-none px-4 pt-3 sm:px-6">
        <UAlert color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
      </div>

      <div v-if="missing" class="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-muted" data-ticket-missing>
        <UIcon name="i-lucide-search-x" class="size-8" />
        <p class="text-sm">No ticket has the key <code>{{ ticketKey }}</code>.</p>
        <UButton :label="`Back to ${project}`" icon="i-lucide-arrow-left" color="neutral" variant="soft" :to="projectPath(project)" />
      </div>

      <div v-else-if="ticket" class="flex flex-col gap-5 px-4 py-5 sm:px-6" :data-ticket="ticket.key" :data-ticket-rev="ticket.rev">
        <UAlert
          v-if="deleted"
          color="neutral"
          variant="subtle"
          icon="i-lucide-trash-2"
          title="This ticket is deleted."
          description="Its key is kept, and so is every reference to it, inactive: its children's parent, its relations, its runs. Undelete brings it back whole."
          :actions="archived ? [] : [{ label: 'Undelete', icon: 'i-lucide-undo-2', color: 'neutral', variant: 'outline', onClick: undeleteTicket }]"
          data-ticket-deleted
        />
        <UAlert v-else-if="archived" color="neutral" variant="subtle" icon="i-lucide-archive" :title="`${project} is archived.`" description="Nothing of its tickets can be changed until it is unarchived." data-ticket-archived />

        <!-- The title, edited in place, and the ticket's state in words. -->
        <div class="flex flex-col gap-1.5">
          <div v-if="editingTitle" class="flex items-center gap-2">
            <UInput
              ref="titleInput"
              v-model="titleDraft"
              aria-label="Title"
              maxlength="200"
              size="lg"
              class="min-w-0 flex-1"
              data-title-input
              @keydown.enter.prevent="(e: KeyboardEvent) => saveTitle(e)"
              @keydown.escape.prevent.stop="leaveTitle"
            />
            <UButton label="Save" icon="i-lucide-check" :loading="titleSaving" :disabled="!titleDraft.trim()" data-title-save @click="saveTitle()" />
            <UButton label="Cancel" color="neutral" variant="ghost" @click="leaveTitle" />
          </div>
          <h1 v-else class="group flex items-start gap-2 text-xl font-semibold text-highlighted">
            <span class="min-w-0 break-words" data-ticket-title>{{ ticket.title }}</span>
            <UButton v-if="!locked" icon="i-lucide-pencil" size="xs" color="neutral" variant="ghost" aria-label="Edit the title" class="mt-0.5 flex-none opacity-60 group-hover:opacity-100" data-title-edit @click="editTitle" />
          </h1>
          <p v-if="editingTitle && titleTheirs !== null" class="flex items-start gap-1.5 text-xs text-warning" data-title-theirs>
            <UIcon name="i-lucide-triangle-alert" class="mt-px size-3.5 flex-none" />
            <span class="min-w-0 break-words">The title was changed to “{{ titleTheirs }}” while you edited it. Save again to replace it with yours, or Cancel to keep it.</span>
          </p>
          <p class="text-xs text-muted">
            <span :class="{ 'text-warning': words?.tone === 'warning', 'text-success': words?.tone === 'success' }" data-ticket-words>{{ words?.text }}</span>
            <span class="mx-1">·</span>
            made by {{ ticket.createdBy || 'admin' }} {{ relativeTime(ticket.createdAt, now, { suffix: true }) }}
            <span class="mx-1">·</span>
            updated {{ relativeTime(ticket.updatedAt, now, { suffix: true }) }}
          </p>
        </div>

        <!-- Its text and history, its properties beside them on a wide screen; on a phone one column, the properties first. -->
        <div class="flex flex-col gap-6 lg:flex-row lg:items-start">
          <div class="flex min-w-0 flex-1 flex-col gap-6">
            <TicketMarkdown :text="ticket.description" label="Description" empty="Add a description…" :max-bytes="MAX_DESCRIPTION" :save="(text, base) => saveText('description', text, base)" :disabled="locked" name="description" :attachment-url="files.resolver.value" />
            <TicketMarkdown :text="ticket.acceptance" label="Acceptance criteria" empty="Add what must hold for it to be done…" :max-bytes="MAX_ACCEPTANCE" :save="(text, base) => saveText('acceptance', text, base)" :disabled="locked" name="acceptance" :attachment-url="files.resolver.value" />

            <DispatchClosePolicy :ticket="ticket" :locked="locked" :now="now" :accept="accept" :unaccept="unaccept" :close="closeTicket" />
            <DispatchLinks :links="ticket.links ?? []" :locked="locked" :now="now" :add="addLink" :remove="removeLink" />
            <DispatchRuns :links="ticket.links ?? []" :locked="locked" :now="now" :add="linkRun" :remove="removeLink" />

            <DispatchRelations :ticket="ticket" :project="project" :locked="locked" :add="addRelation" :remove="removeRelation" />
            <DispatchAttachments :attachments="ticket.attachments ?? []" :locked="locked" :now="now" :upload="upload" :remove="removeAttachment" :image="files.image" :download="files.download" @insert="(md: string) => comments?.insert(md)" />
            <DispatchComments ref="comments" :ticket="ticket" :gate="gate" :locked="locked" :reads="reads" :resets="resets" :attachment-url="files.resolver.value" :now="now" />

            <DispatchHistory :entries="entries" :now="now" :more="!!historyNext" :loading="historyLoading" @more="moreHistory" />
          </div>

          <aside class="order-first grid grid-cols-2 gap-x-3 gap-y-4 rounded-lg p-4 ring ring-default lg:sticky lg:top-4 lg:order-none lg:flex lg:w-80 lg:flex-none lg:flex-col" data-ticket-properties>
            <UFormField label="Status" size="sm">
              <USelect :model-value="ticket.status" :items="statusItems" :disabled="locked" size="sm" class="w-full" :icon="statusWord(ticket.status).icon" data-ticket-status @update:model-value="setStatus" />
            </UFormField>
            <UFormField label="Priority" size="sm">
              <USelect :model-value="ticket.priority" :items="priorityItems" :disabled="locked" size="sm" class="w-full" :icon="priorityWord(ticket.priority).icon" data-ticket-priority @update:model-value="setPriority" />
            </UFormField>
            <UFormField label="Horizon" size="sm">
              <USelect :model-value="horizonWord(ticket.horizon).name" :items="horizonItems" :disabled="locked" size="sm" class="w-full" :icon="horizonWord(ticket.horizon).icon" data-ticket-horizon @update:model-value="setHorizon" />
            </UFormField>
            <UFormField label="Owner" size="sm" hint="accountable">
              <PersonPicker :model-value="ticket.owner" :disabled="locked" data-ticket-owner @update:model-value="setOwner" />
            </UFormField>
            <UFormField label="Assignees" size="sm" class="col-span-2">
              <AssigneePicker :model-value="shownAssignees" :agents="agents" :disabled="locked" data-ticket-assignees @update:model-value="setAssignees" />
            </UFormField>
            <USeparator class="col-span-2" />
            <UFormField label="Labels" size="sm" class="col-span-2">
              <LabelPicker :model-value="shownLabels" :labels="projectRead?.labels ?? []" :disabled="locked" data-ticket-labels @update:model-value="setLabels" />
            </UFormField>
            <UFormField label="Milestone" size="sm" class="col-span-2">
              <MilestonePicker :model-value="ticket.milestoneId" :milestones="projectRead?.milestones ?? []" :disabled="locked" data-ticket-milestone @update:model-value="setMilestone" />
            </UFormField>
            <div class="col-span-2 flex flex-col gap-1.5 text-sm" data-ticket-parent :data-parent="ticket.parent?.key ?? ''" :data-parent-deleted="ticket.parent?.deletedAt ? '' : undefined">
              <span class="text-xs font-medium text-muted">Parent</span>
              <div v-if="ticket.parent" class="flex min-w-0 items-center gap-1.5">
                <NuxtLink :to="ticketPath(ticket.parent.key)" class="min-w-0 truncate hover:underline" :class="ticket.parent.deletedAt ? 'text-muted' : ''" data-parent-link>
                  <span class="font-mono text-xs text-muted">{{ ticket.parent.key }}</span> <span :class="ticket.parent.deletedAt ? 'line-through' : ''">{{ ticket.parent.title }}</span><template v-if="ticket.parent.deletedAt"> (deleted)</template>
                </NuxtLink>
                <span v-if="!ticket.parent.deletedAt" class="flex flex-none items-center gap-1 text-xs text-dimmed" :data-parent-status="ticket.parent.status">
                  <UIcon :name="statusWord(ticket.parent.status).icon" class="size-3.5" />{{ statusWord(ticket.parent.status).label }}
                </span>
                <UButton v-if="!locked" icon="i-lucide-x" size="xs" color="neutral" variant="ghost" class="ml-auto flex-none" aria-label="No parent" data-parent-clear @click="setParent('')" />
              </div>
              <TicketPicker v-if="!locked" :project="project" :exclude="parentExclude" :placeholder="ticket.parent ? 'Another parent…' : 'A parent ticket…'" data-parent-picker @pick="(t: TicketSummary) => setParent(t.key)" />
              <span v-else-if="!ticket.parent" class="text-dimmed">None</span>
            </div>
            <template v-if="!locked">
              <USeparator class="col-span-2" />
              <UButton label="Delete the ticket" icon="i-lucide-trash-2" color="neutral" variant="ghost" size="sm" class="col-span-2 w-fit" data-ticket-delete @click="deleteOpen = true" />
            </template>
          </aside>
        </div>
      </div>

      <div v-else class="flex flex-1 items-center justify-center p-8 text-sm text-muted">Reading {{ ticketKey }}…</div>
    </template>
  </UDashboardPanel>

  <UModal v-model:open="deleteOpen" :title="`Delete ${ticketKey}?`" description="Its key is kept, and every reference to it stays, inactive: its children keep it as their parent, its relations and its runs stay. Undelete, on its page, brings it back whole." :ui="{ content: 'max-w-md' }">
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Keep it" color="neutral" variant="ghost" @click="deleteOpen = false" />
        <UButton label="Delete" icon="i-lucide-trash-2" color="error" data-ticket-delete-confirm @click="deleteTicket" />
      </div>
    </template>
  </UModal>
</template>
