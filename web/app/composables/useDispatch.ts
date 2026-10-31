import { DispatchOrder, through, throughOrAbsent, type AbsentOptions } from '~/utils/dispatchOrder'
import { ApiError, type ApiErrorBody } from './useApi'

// Dispatch's client (docs/plans/dispatch.md, "web/app"; the routes in docs/protocol.md, Dispatch): the types of every reply, as
// useSessions.ts holds the crews' and the runs', the dispatch event's beside them, and the routes over useApi. Every page's state goes
// through its own ordering gate (useDispatch().gate, utils/dispatchOrder.ts); the operations go through useDispatchOps.ts and the live
// events through useDispatchLive.ts.

/** A ticket's status (Owner decisions, 4: six, fixed). */
export type TicketStatus = 'backlog' | 'ready' | 'in_progress' | 'in_review' | 'done' | 'cancelled'
/** A ticket's horizon, a roadmap's column: Now, Next, Later, or '' for not planned. */
export type Horizon = '' | 'now' | 'next' | 'later'
/** A project's tracker: its own (native), or a pointer at another one by its URL (external). */
export type Tracker = 'native' | 'external'
/** What a project's close policy may require before a ticket is done. */
export type CloseRequirement = 'acceptance' | 'delivery'
/** The twelve colours a label may have, by name; the UI maps each to one of the brand's scales (utils/dispatch.ts). */
export type LabelColor = 'gray' | 'brown' | 'red' | 'orange' | 'amber' | 'yellow' | 'lime' | 'green' | 'teal' | 'cyan' | 'blue' | 'indigo'
/** What a list is sorted by: its updated or created time, its priority, its status, its title, its key, its milestone. */
export type DispatchSort = 'updated' | 'created' | 'priority' | 'status' | 'title' | 'key' | 'milestone'
/** What a dispatch event says changed. */
export type DispatchEntity = 'project' | 'ticket' | 'comment' | 'label' | 'milestone' | 'view' | 'person'

/** A project as its read carries it (GET /api/dispatch/projects/{key}). Times are RFC 3339; a zero one is absent. */
export interface DispatchProject {
  id: string
  key: string
  name: string
  description: string
  cwd: string
  tracker: Tracker
  trackerUrl: string
  closeRequires: CloseRequirement[]
  nextNumber: number
  numberFrom: number
  numberTo: number
  archivedAt?: string
  createdAt: string
  updatedAt: string
  rev: number
}

/** A project as the projects page lists it: its counts of open and done tickets, without its description, folder, URL or policy. */
export interface ProjectSummary {
  id: string
  key: string
  name: string
  tracker: Tracker
  archivedAt?: string
  open: number
  done: number
  rev: number
  updatedAt: string
}

export interface ProjectsPage {
  projects: ProjectSummary[]
  next?: string
}

export interface DispatchLabel {
  id: string
  projectId: string
  name: string
  color: LabelColor
  createdAt: string
  deletedAt?: string
  rev: number
}

export interface DispatchMilestone {
  id: string
  projectId: string
  name: string
  description: string
  /** YYYY-MM-DD, or '' for none. */
  target: string
  state: 'open' | 'closed'
  createdAt: string
  updatedAt: string
  deletedAt?: string
  rev: number
}

/** A saved view's query: a list's filter and sort as the store keeps them (labels and the milestone by id), and its grouping. */
export interface ViewQuery {
  status?: string[]
  priority?: number[]
  labels?: string[]
  owner?: string
  assignees?: string[]
  milestone?: string
  horizon?: string[]
  parent?: string
  blocked?: boolean
  q?: string
  deleted?: boolean
  sort?: string
  desc?: boolean
  group?: string
}

/** What a saved view shows its tickets as: a list, a board or a roadmap. */
export type ViewKind = 'list' | 'board' | 'roadmap'

export interface DispatchView {
  id: string
  /** '' for a view of every project. */
  projectId: string
  name: string
  kind: ViewKind
  query: ViewQuery
  createdAt: string
  updatedAt: string
  deletedAt?: string
  rev: number
}

/** A project with what its pages offer: its live labels by name, its live milestones by target, the saved views. */
export interface ProjectRead {
  project: DispatchProject
  labels: DispatchLabel[]
  milestones: DispatchMilestone[]
  views: DispatchView[]
}

/** A name of the directory the owner and assignee pickers offer (people are local names until identity exists). */
export interface DispatchPerson {
  name: string
  createdAt: string
}

/** A ticket as a list shows it: no description, criteria or acceptance; its labels by id, its assignees as `person:<name>` or `agent:<id>`. */
export interface TicketSummary {
  id: string
  projectId: string
  number: number
  key: string
  title: string
  status: TicketStatus
  priority: number
  horizon: Horizon
  owner: string
  assignees: string[]
  labels: string[]
  milestoneId: string
  parentId: string
  blocked: boolean
  createdAt: string
  updatedAt: string
  closedAt?: string
  deletedAt?: string
  rev: number
}

export interface TicketsPage {
  tickets: TicketSummary[]
  next?: string
  /** How many tickets the whole list holds. */
  total: number
}

/** The ticket at the other end of a reference: its parent, a relation's other end, a child, the ticket a run is linked to. */
export interface RelatedTicket {
  id: string
  key: string
  title: string
  status: TicketStatus
  deletedAt?: string
}

export interface TicketRelation {
  id: string
  fromId: string
  toId: string
  kind: 'blocks' | 'relates'
  createdAt: string
  createdBy: string
  deletedAt?: string
  other: RelatedTicket
}

export interface TicketLink {
  id: string
  kind: 'delivery' | 'run' | 'external'
  ref: string
  url: string
  note: string
  createdAt: string
  createdBy: string
  deletedAt?: string
}

export interface TicketAttachment {
  id: string
  ticketId: string
  name: string
  size: number
  sha256: string
  contentType: string
  createdAt: string
  createdBy: string
  deletedAt?: string
}

export interface TicketComment {
  id: string
  ticketId: string
  /** Its 1-based place among the ticket's comments, derived at read. */
  n: number
  author: string
  by: string
  body: string
  createdAt: string
  editedAt?: string
  deletedAt?: string
  rev: number
}

/** A ticket's close policy as read: what its project requires, what it meets, what it lacks, and why an acceptance on record is unmet. */
export interface CloseState {
  requires: CloseRequirement[]
  met: { acceptance: boolean; delivery: boolean }
  missing: CloseRequirement[]
  why?: string
}

/** A ticket in full, as its page reads it (GET /api/dispatch/tickets/{key}), from one read transaction. */
export interface DispatchTicket {
  id: string
  projectId: string
  number: number
  key: string
  title: string
  description: string
  acceptance: string
  status: TicketStatus
  priority: number
  horizon: Horizon
  owner: string
  parentId: string
  milestoneId: string
  acceptedAt?: string
  acceptedBy?: string
  acceptedFor?: string
  acceptedCriteria?: string
  acceptedNote?: string
  source: string
  externalId?: string
  externalUrl?: string
  createdAt: string
  createdBy: string
  updatedAt: string
  closedAt?: string
  deletedAt?: string
  rev: number
  assignees: string[]
  labels: string[]
  parent?: RelatedTicket
  children: { tickets: RelatedTicket[]; total: number }
  relations: TicketRelation[]
  relationsTotal: number
  blocked: boolean
  links: TicketLink[]
  attachments: TicketAttachment[]
  comments: TicketComment[]
  close: CloseState
}

/** One row of a ticket's history, as the store recorded it: one field set, added or removed by one operation. */
export interface DispatchChange {
  seq: number
  entity: DispatchEntity
  entityId: string
  ticketId?: string
  projectId?: string
  field?: string
  op: 'set' | 'add' | 'remove' | 'create' | 'delete' | 'reconcile' | 'migrate'
  value: string
  prior?: string
  at: string
  actor: string
  by?: string
  opId: string
  /** The row's place in its operation, from 0. */
  n: number
}

export interface HistoryPage {
  changes: DispatchChange[]
  /** The seq to read after for the next page. */
  next?: number
}

/** An operation's receipt (docs/protocol.md, Dispatch operations): kept under its id, answered again for the same id. */
export interface DispatchReceipt {
  opId: string
  intent: string
  status: 'done' | 'failed'
  rev?: number
  id?: string
  key?: string
  repeat?: boolean
  error?: string
  refusal?: string
  missing?: CloseRequirement[]
}

/**
 * The `dispatch` event (GET /api/events): something of Dispatch's changed, for a page to read again. `seq` is the commit's last change;
 * `entity` what changed and `id` its id (empty for a person, and for the one event a commit that changed more than 64 entities of a kind
 * in a project sends for them); `ticket` the id of the ticket it belongs to and `project` its project's key, empty where there is none.
 * A commit's events arrive together. Nothing reads an event's `id` or `ticket` without checking it is set.
 */
export interface DispatchEvent {
  seq: number
  entity: DispatchEntity
  id: string
  ticket: string
  project: string
}

/** What every Dispatch write carries beside its fields: the op id the browser mints, the revision it expects (none: absent), the display name. */
export interface OpBody {
  opId: string
  expectedRev?: number
  byName?: string
}

export interface NewProject {
  key: string
  name: string
  description?: string
  cwd?: string
  tracker?: Tracker
  trackerUrl?: string
  closeRequires?: CloseRequirement[]
}

export interface NewTicket {
  project: string
  title: string
  description?: string
  acceptance?: string
  status?: TicketStatus
  priority?: number
  horizon?: Horizon
  owner?: string
  assignees?: string[]
  labels?: string[]
  milestone?: string
  parent?: string
}

/** A ticket's edit, every change one operation: the fields set (a parent by key, a milestone by id, '' for none) and the members added and removed. */
export interface TicketEdit {
  set?: Partial<{ title: string; description: string; acceptance: string; status: TicketStatus; priority: number; horizon: Horizon; owner: string; parent: string; milestone: string }>
  add?: { assignees?: string[]; labels?: string[] }
  remove?: { assignees?: string[]; labels?: string[] }
}

/** A saved view made (POST /api/dispatch/views): of the project with the key, or of every project without one. */
export interface NewView {
  project?: string
  name: string
  kind: ViewKind
  query: ViewQuery
}

/** A saved view's edit: each field given is set; its project never changes. */
export interface ViewEdit {
  name?: string
  kind?: ViewKind
  query?: ViewQuery
}

export interface ViewReply {
  receipt: DispatchReceipt
  view: DispatchView | null
}

export interface ProjectReply {
  receipt: DispatchReceipt
  project: DispatchProject | null
}

export interface TicketReply {
  receipt: DispatchReceipt
  ticket: DispatchTicket | null
}

export interface CommentReply {
  receipt: DispatchReceipt
  comment: TicketComment | null
}

export interface AttachmentReply {
  receipt: DispatchReceipt
  attachment: TicketAttachment | null
}

export interface LabelReply {
  receipt: DispatchReceipt
  label: DispatchLabel | null
}

export interface MilestoneReply {
  receipt: DispatchReceipt
  milestone: DispatchMilestone | null
}

/** A project's edit: each field given is set, its key never changing. */
export interface ProjectEdit {
  name?: string
  description?: string
  cwd?: string
  tracker?: Tracker
  trackerUrl?: string
  closeRequires?: CloseRequirement[]
}

export interface NewLabel {
  name: string
  color: LabelColor
}

export interface LabelEdit {
  name?: string
  color?: LabelColor
}

export interface NewMilestone {
  name: string
  description?: string
  /** YYYY-MM-DD, or '' for none. */
  target?: string
  state?: 'open' | 'closed'
}

export type MilestoneEdit = Partial<NewMilestone>

/** A relation made from the ticket a route names: it `blocks` the other, or `relates` to it. */
export interface NewRelation {
  kind: 'blocks' | 'relates'
  /** The other ticket's key, of the same project. */
  to: string
}

/** What a ticket is linked to outward (PR-11): where it was delivered (a merged pull request's or a release's URL, its merged state never
 * read), a page of another tracker's, or a managed run that worked on it. */
export type LinkKind = TicketLink['kind']

/**
 * A link made from a ticket: a `delivery` or an `external` one by its `url` (its `ref` the URL when none is given), a `run` one by the
 * run's id as its `ref`, a run the engine holds or has a record of and no other live ticket holds.
 */
export interface NewLink {
  kind: LinkKind
  ref?: string
  url?: string
  note?: string
}

/** How far a file's upload has gone, in bytes. */
export type UploadProgress = (sent: number, total: number) => void

const enc = encodeURIComponent

/**
 * An attachment's upload (POST …/{key}/attachments): a multipart body whose parts are the operation's `opId`, then `expectedRev` and
 * `byName` when given, and last `file`, in that order (docs/protocol.md), sent by XMLHttpRequest, the one way a browser says how far
 * a request's body has gone. Its reply and its failures are `request`'s: an ApiError with the server's code, `network` (status 0) when
 * the server could not be reached, the reply's body beside it (a refusal's receipt).
 */
function useUpload() {
  const { httpBase } = useApiBase()
  const admin = useWorkbenchToken()
  return function upload<T>(path: string, body: OpBody, file: File, progress?: UploadProgress): Promise<T> {
    const form = new FormData()
    form.append('opId', body.opId)
    if (body.expectedRev) form.append('expectedRev', String(body.expectedRev))
    if (body.byName) form.append('byName', body.byName)
    form.append('file', file, file.name)
    return new Promise<T>((resolve, reject) => {
      const xhr = new XMLHttpRequest()
      xhr.open('POST', httpBase.value + path)
      const token = admin.token.value
      if (token) xhr.setRequestHeader('Authorization', `Bearer ${token}`)
      xhr.responseType = 'text'
      if (progress) xhr.upload.onprogress = (e) => progress(e.loaded, e.lengthComputable ? e.total : file.size)
      xhr.onerror = () => reject(new ApiError(0, 'network', 'Cannot reach the server'))
      xhr.onabort = () => reject(new ApiError(0, 'network', 'The upload was stopped'))
      xhr.onload = () => {
        let data: unknown
        try {
          data = xhr.responseText ? JSON.parse(xhr.responseText) : undefined
        } catch {
          data = undefined
        }
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve(data as T)
          return
        }
        const err = (data as ApiErrorBody | undefined)?.error
        if (xhr.status === 401) admin.needsToken.value = true
        reject(new ApiError(xhr.status, err?.code ?? 'http_error', err?.message ?? `HTTP ${xhr.status}`, err ?? {}, data))
      }
      xhr.send(form)
    })
  }
}

/** Dispatch's routes, over useApi: the workbench token in the header, errors as ApiError (a refused operation's `body` carries its receipt). */
export function useDispatchApi() {
  const { request, requestBlob } = useApi()
  const upload = useUpload()
  return {
    /** A page of the projects by key, after `cursor` (a page's `next`). */
    projects: (cursor = '') => request<ProjectsPage>('/api/dispatch/projects' + (cursor ? `?cursor=${enc(cursor)}` : '')),
    project: (key: string) => request<ProjectRead>(`/api/dispatch/projects/${enc(key)}`),
    createProject: (body: OpBody & NewProject) => request<ProjectReply>('/api/dispatch/projects', { method: 'POST', body }),
    people: () => request<{ people: DispatchPerson[] }>('/api/dispatch/people').then((r) => r.people ?? []),
    /** A page of a list of tickets; `query` is the API's query string (utils/dispatchQuery.ts, apiQuery), without its `?`. */
    tickets: (query: string) => request<TicketsPage>('/api/dispatch/tickets' + (query ? `?${query}` : '')),
    ticket: (key: string) => request<{ ticket: DispatchTicket }>(`/api/dispatch/tickets/${enc(key)}`).then((r) => r.ticket),
    createTicket: (body: OpBody & NewTicket) => request<TicketReply>('/api/dispatch/tickets', { method: 'POST', body }),
    editTicket: (key: string, body: OpBody & TicketEdit) => request<TicketReply>(`/api/dispatch/tickets/${enc(key)}/edit`, { method: 'POST', body }),
    /** A page of a ticket's history after the change `after` (its seq; 0 from the start), oldest first. */
    history: (key: string, after = 0) => request<HistoryPage>(`/api/dispatch/tickets/${enc(key)}/history` + (after ? `?after=${after}` : '')),
    /** The saved views a project's pages offer, by name: the project's own and every project's. */
    views: (project: string) => request<{ views: DispatchView[] }>(`/api/dispatch/views?project=${enc(project)}`).then((r) => r.views ?? []),
    createView: (body: OpBody & NewView) => request<ViewReply>('/api/dispatch/views', { method: 'POST', body }),
    editView: (id: string, body: OpBody & ViewEdit) => request<ViewReply>(`/api/dispatch/views/${enc(id)}/edit`, { method: 'POST', body }),
    deleteView: (id: string, body: OpBody) => request<ViewReply>(`/api/dispatch/views/${enc(id)}/delete`, { method: 'POST', body }),
    editProject: (key: string, body: OpBody & ProjectEdit) => request<ProjectReply>(`/api/dispatch/projects/${enc(key)}/edit`, { method: 'POST', body }),
    createLabel: (project: string, body: OpBody & NewLabel) => request<LabelReply>(`/api/dispatch/projects/${enc(project)}/labels`, { method: 'POST', body }),
    editLabel: (project: string, id: string, body: OpBody & LabelEdit) => request<LabelReply>(`/api/dispatch/projects/${enc(project)}/labels/${enc(id)}/edit`, { method: 'POST', body }),
    deleteLabel: (project: string, id: string, body: OpBody) => request<LabelReply>(`/api/dispatch/projects/${enc(project)}/labels/${enc(id)}/delete`, { method: 'POST', body }),
    createMilestone: (project: string, body: OpBody & NewMilestone) => request<MilestoneReply>(`/api/dispatch/projects/${enc(project)}/milestones`, { method: 'POST', body }),
    editMilestone: (project: string, id: string, body: OpBody & MilestoneEdit) => request<MilestoneReply>(`/api/dispatch/projects/${enc(project)}/milestones/${enc(id)}/edit`, { method: 'POST', body }),
    deleteMilestone: (project: string, id: string, body: OpBody) => request<MilestoneReply>(`/api/dispatch/projects/${enc(project)}/milestones/${enc(id)}/delete`, { method: 'POST', body }),
    deleteTicket: (key: string, body: OpBody) => request<TicketReply>(`/api/dispatch/tickets/${enc(key)}/delete`, { method: 'POST', body }),
    undeleteTicket: (key: string, body: OpBody) => request<TicketReply>(`/api/dispatch/tickets/${enc(key)}/undelete`, { method: 'POST', body }),
    /** A ticket's comments numbered after `after`, at most `limit` (1 to 50), tombstones among them so the numbers have no gap. */
    comments: (key: string, after: number, limit: number) =>
      request<{ comments: TicketComment[] }>(`/api/dispatch/tickets/${enc(key)}/comments?after=${after}&limit=${limit}`).then((r) => r.comments ?? []),
    addComment: (key: string, body: OpBody & { body: string }) => request<CommentReply>(`/api/dispatch/tickets/${enc(key)}/comments`, { method: 'POST', body }),
    editComment: (key: string, id: string, body: OpBody & { body: string }) => request<CommentReply>(`/api/dispatch/tickets/${enc(key)}/comments/${enc(id)}/edit`, { method: 'POST', body }),
    deleteComment: (key: string, id: string, body: OpBody) => request<CommentReply>(`/api/dispatch/tickets/${enc(key)}/comments/${enc(id)}/delete`, { method: 'POST', body }),
    addRelation: (key: string, body: OpBody & NewRelation) => request<TicketReply>(`/api/dispatch/tickets/${enc(key)}/relations`, { method: 'POST', body }),
    removeRelation: (key: string, id: string, body: OpBody) => request<TicketReply>(`/api/dispatch/tickets/${enc(key)}/relations/${enc(id)}/delete`, { method: 'POST', body }),
    /** A file attached to the ticket, its upload's progress said as it goes. */
    addAttachment: (key: string, body: OpBody, file: File, progress?: UploadProgress) => upload<AttachmentReply>(`/api/dispatch/tickets/${enc(key)}/attachments`, body, file, progress),
    removeAttachment: (key: string, id: string, body: OpBody) => request<AttachmentReply>(`/api/dispatch/tickets/${enc(key)}/attachments/${enc(id)}/delete`, { method: 'POST', body }),
    /** An attachment's content, fetched with the token, as the type the server serves it as (an SVG as application/octet-stream). */
    attachment: (id: string) => requestBlob(`/api/dispatch/attachments/${enc(id)}`),
    /** The owner's acceptance recorded, for the owner the ticket has now and against its criteria as they stand, with a note. */
    accept: (key: string, body: OpBody & { note?: string }) => request<TicketReply>(`/api/dispatch/tickets/${enc(key)}/accept`, { method: 'POST', body }),
    /** The acceptance on record cleared. */
    unaccept: (key: string, body: OpBody) => request<TicketReply>(`/api/dispatch/tickets/${enc(key)}/unaccept`, { method: 'POST', body }),
    addLink: (key: string, body: OpBody & NewLink) => request<TicketReply>(`/api/dispatch/tickets/${enc(key)}/links`, { method: 'POST', body }),
    removeLink: (key: string, id: string, body: OpBody) => request<TicketReply>(`/api/dispatch/tickets/${enc(key)}/links/${enc(id)}/delete`, { method: 'POST', body }),
    /** The ticket a run is linked to, for the run page's chip: the live one, else the latest-linked tombstone with `deletedAt`, else null. */
    runTicket: (run: string) => request<{ ticket: RelatedTicket | null }>(`/api/dispatch/links?run=${enc(run)}`).then((r) => r.ticket ?? null),
    /** An operation's receipt, read without acting. */
    receipt: (opId: string) => request<{ receipt: DispatchReceipt }>(`/api/dispatch/ops/${enc(opId)}`).then((r) => r.receipt),
  }
}

export type DispatchApi = ReturnType<typeof useDispatchApi>

/**
 * A Dispatch page's client and its ordering gate (utils/dispatchOrder.ts): `read` sends a request about one thing the page shows and
 * applies its reply only when the gate takes it, so every read and every operation's reply on the page lands in the order the gate keeps;
 * `readOrAbsent` the same for a thing that may not exist, its 404 ordered as any reply. One gate per page: each call makes its own.
 */
export function useDispatch() {
  const api = useDispatchApi()
  const gate = new DispatchOrder()
  function read<T>(thing: string, send: () => Promise<T>, apply: (reply: T) => void, rev?: (reply: T) => number | undefined): Promise<boolean> {
    return through(gate, thing, send, apply, rev)
  }
  function readOrAbsent<T>(thing: string, send: () => Promise<T>, o: AbsentOptions<T>): Promise<boolean> {
    return throughOrAbsent(gate, thing, send, o)
  }
  return { api, gate, read, readOrAbsent }
}
