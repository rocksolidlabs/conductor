import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import { memberStatus, type MemberStatus } from './crews'

/** How long the store gathers the runs that run events and member sessions name before it reads them. */
export const RUN_EVENT_WINDOW_MS = 250
/** More runs than this named in one window are read with one GET /api/runs rather than one read each. */
export const RUN_EVENT_LIST_AT = 8

export type RunState = RunInfo['state']

/**
 * A run's state and how many of its members wait on a prompt, from the run as last read brought up to date by the live sessions
 * (memberStatus): stopped once stopped; finished once every member has ended; needs_input while a starting or running member's session
 * waits on a prompt; running otherwise, pending members included (a member's done keeps it running: a done agent is idle, not gone). The
 * same rule as runState in internal/crew/run.go.
 */
export function runState(run: RunInfo, sessions: readonly SessionInfo[]): { state: RunState; needs: number } {
  // A record's state is as recorded: nothing of it is live, and a run interrupted by the server's end stays so.
  if (run.record) return { state: run.state, needs: run.needsInput }
  let needs = 0
  let ended = 0
  for (const m of run.members) {
    const st = memberStatus(run, m, sessions)
    if (st === 'ended') ended++
    else if (st === 'needs_input') needs++
  }
  if (run.stoppedAt) return { state: 'stopped', needs }
  // A managed run is finished once every task is terminal (its outcome), its idle sessions kept for a look; a legacy run once every member ended.
  if (run.managed ? !!run.outcome : ended === run.members.length) return { state: 'finished', needs: 0 }
  if (needs > 0) return { state: 'needs_input', needs }
  return { state: 'running', needs: 0 }
}

/** Whether a run still goes: running or waiting on a person. */
export function runLive(state: RunState): boolean {
  return state === 'running' || state === 'needs_input'
}

/** A run's state as a badge: its words and its colour. */
export function runBadge(state: RunState, needs: number): { label: string; color: 'success' | 'warning' | 'neutral' } {
  switch (state) {
    case 'needs_input':
      return { label: `Needs input ${needs}`, color: 'warning' }
    case 'stopped':
      return { label: 'Stopped', color: 'neutral' }
    case 'finished':
      return { label: 'Finished', color: 'neutral' }
    case 'interrupted':
      return { label: 'Interrupted', color: 'warning' }
  }
  return { label: 'Running', color: 'success' }
}

/**
 * What a member's controls offer by hand (the run page's tile, the graph's node and its list, the page's palette row), as the engine
 * takes it: Start now for a member waiting in a run not stopped (POST /api/runs/{id}/members/{name}/start), Resume or Relaunch for an
 * ended one (…/resume, which reopens a stopped run); neither in a managed run, whose tasks start and continue its members (409
 * managed_run).
 */
export function memberOffer(status: MemberStatus | undefined, run: { stopped: boolean; managed: boolean }): 'start' | 'resume' | null {
  if (run.managed) return null
  if (status === 'pending') return run.stopped ? null : 'start'
  return status === 'ended' ? 'resume' : null
}

/**
 * Whether an ended session offers Resume or Relaunch, by memberOffer's rule: a crew member's session resumes as its member, so a
 * managed run's attempt (409 managed_run) and a Brakeman turn's session (409 brakeman_run) never do.
 */
export function sessionResumeOffered(s: Pick<SessionInfo, 'crew'>): boolean {
  return !s.crew?.task && !s.crew?.brakeman
}

/** A member's status as a dot beside its avatar, and in words. */
export function memberDot(status: MemberStatus): { label: string; dot: string } {
  switch (status) {
    case 'needs_input':
      return { label: 'needs input', dot: 'bg-warning' }
    case 'running':
      return { label: 'running', dot: 'bg-success' }
    case 'starting':
      return { label: 'starting', dot: 'bg-info' }
    case 'ended':
      return { label: 'ended', dot: 'bg-neutral-400' }
  }
  return { label: 'pending', dot: 'bg-transparent ring-1 ring-neutral-400' }
}

/** How the store reads runs: one (null when the server does not have it), or every run. */
export interface RunReader {
  get(id: string): Promise<RunInfo | null>
  list(): Promise<RunInfo[]>
}

/**
 * The runs the browser knows, by id, and the reads that keep them current. Every read takes a ticket as it is asked; its answer replaces a
 * run only when no later read or removal of that run has been applied, so a slow, older reply never replaces a newer one. The runs named by
 * run events, and the runs of member sessions that changed, are gathered for RUN_EVENT_WINDOW_MS and read then: one read each, or a single
 * list when more than RUN_EVENT_LIST_AT are named. `changed` is called after every change, for the page state to follow.
 */
export class RunStore {
  readonly runs = new Map<string, RunInfo>()
  private ticket = 0
  /** By run: the ticket of the read or removal last applied. */
  private applied = new Map<string, number>()
  /** Replies to reads asked before this ticket are dropped: they were asked before a clear. */
  private floor = 0
  private pending = new Set<string>()
  private timer: ReturnType<typeof setTimeout> | undefined

  constructor(
    private readonly reader: RunReader,
    private readonly changed: () => void,
    private readonly windowMs = RUN_EVENT_WINDOW_MS,
  ) {}

  /** The runs, newest first. */
  list(): RunInfo[] {
    return [...this.runs.values()].sort((a, b) => b.startedAt.localeCompare(a.startedAt))
  }

  /** Takes a run as a read with `ticket` answered it (by default the newest: the reply of an action such as a stop). False when a later read or removal of it was applied first. */
  apply(run: RunInfo, ticket = ++this.ticket): boolean {
    if (!this.take(run.id, ticket)) return false
    this.runs.set(run.id, run)
    this.changed()
    return true
  }

  /** Drops a run the server forgot, unless a later read of it was applied first. */
  remove(id: string, ticket = ++this.ticket): void {
    if (!this.take(id, ticket)) return
    if (this.runs.delete(id)) this.changed()
  }

  /** Takes every run as a list read with `ticket` gave them: a run missing from it was forgotten, unless a later read of it was applied first. */
  applyList(list: readonly RunInfo[], ticket: number): void {
    const seen = new Set<string>()
    for (const r of list) {
      seen.add(r.id)
      if (this.take(r.id, ticket)) this.runs.set(r.id, r)
    }
    for (const id of [...this.runs.keys()]) if (!seen.has(id) && this.take(id, ticket)) this.runs.delete(id)
    this.changed()
  }

  /** Reads one run now; resolves to it, or null when the server does not have it. A failure is thrown and changes nothing. */
  async read(id: string): Promise<RunInfo | null> {
    const ticket = ++this.ticket
    const run = await this.reader.get(id)
    if (run?.record) {
      // A record of a run the server no longer holds: the page that asked
      // gets it, the live runs do not (nothing of it is live), and a run of
      // that id the store held is gone.
      this.remove(id, ticket)
      return run
    }
    if (run) this.apply(run, ticket)
    else this.remove(id, ticket)
    return run && (this.runs.get(id) ?? run)
  }

  /** Reads every run now. A failure is thrown and changes nothing. */
  async readAll(): Promise<void> {
    const ticket = ++this.ticket
    this.applyList(await this.reader.list(), ticket)
  }

  /** Gathers a run to read at the end of the window. */
  schedule(id: string): void {
    this.pending.add(id)
    if (this.timer === undefined) this.timer = setTimeout(() => void this.flush(), this.windowMs)
  }

  /** Reads what the window gathered; a read that fails waits for the next event or snapshot. */
  async flush(): Promise<void> {
    this.timer = undefined
    const ids = [...this.pending]
    this.pending.clear()
    if (!ids.length) return
    if (ids.length > RUN_EVENT_LIST_AT) {
      await this.readAll().catch(() => {})
      return
    }
    await Promise.all(ids.map((id) => this.read(id).catch(() => null)))
  }

  /** Forgets everything: another token may be another server. Replies still on their way are dropped by their tickets. */
  clear(): void {
    clearTimeout(this.timer)
    this.timer = undefined
    this.pending.clear()
    this.floor = ++this.ticket
    this.applied.clear()
    this.runs.clear()
    this.changed()
  }

  /** Marks `id` as answered by `ticket`, unless a later ticket was applied to it. */
  private take(id: string, ticket: number): boolean {
    if (ticket < this.floor || (this.applied.get(id) ?? 0) > ticket) return false
    this.applied.set(id, ticket)
    return true
  }
}
