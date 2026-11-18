import type { BrakemanRequest, BrakemanRow } from '../utils/brakeman'
import { latestBrief, openAbout, answersByTask, subjectUnder } from '../utils/brakemanAbout'
import { parseRef } from '../utils/brakeman'

// What Brakeman said about one subject (issue 993, F8): the requests the list route answers for `about=<subject>`, read once per
// subject and again when a `brakeman` event says one of them moved or a brief was kept that may cite it, with one read in flight and
// the rest one read after it. The rows the page shows are the live store's copies (every row a list returns is applied into it), so a
// state an event moves shows at once. An older server refuses the filter: the list answers it unfiltered (`filtered: false`), and the
// panels that read this are then not drawn at all; nothing pages through every request to find the subject's.

/** The list read this needs (useBrakeman().list). */
export type AboutList = (q: { about: string; limit: number }) => Promise<{ requests: BrakemanRow[]; next: string; filtered: boolean }>

/** How many requests about one subject are read: the newest, which is all a panel and a row's answer show. */
export const ABOUT_LIMIT = 50

/**
 * The reads of one subject at a time: `set` a subject and its rows are read; `event` reads them again when the event's request is
 * among them, or was just briefed (its brief may cite the subject); `add` takes a request the page just asked. A read answered after
 * the subject changed is dropped.
 */
export class AboutReads {
  subject = ''
  /** The rows the last read answered, with the requests the page asked since. */
  rows: BrakemanRow[] = []
  /** Whether the server refused the filter (an older server), or the read failed: the panel is then not drawn. */
  refused = false
  /** Whether a read for the subject has answered. */
  loaded = false
  private busy = false
  private again = false
  private gen = 0

  constructor(
    private readonly list: AboutList,
    private readonly changed: () => void = () => {},
  ) {}

  /** Reads `subject`'s requests ("" for none), forgetting the last subject's. */
  set(subject: string) {
    if (subject === this.subject) return
    this.gen++
    this.subject = subject
    this.rows = []
    this.refused = false
    this.loaded = false
    this.busy = false
    this.again = false
    this.changed()
    if (subject && parseRef(subject)) void this.read()
  }

  /**
   * A `brakeman` event: a request among the rows moved, or one was briefed whose brief may cite the subject; or the stream started
   * again (id ""), when what was missed meanwhile is read.
   */
  event(e: { id: string; state: string }) {
    if (!this.subject || this.refused) return
    if (e.id === '' || e.state === 'briefed' || this.rows.some((r) => r.id === e.id)) void this.read()
  }

  /**
   * A request the page asked: among the rows at once when one of its subjects is about the subject, and read again with them later.
   * One about something else (an ask with no subject, or one asked on another subject whose reply came after the page moved) is not
   * the subject's: the server's about= read holds it only if its brief cites the subject.
   */
  add(r: BrakemanRequest) {
    if (!subjectUnder(this.subject, r.subjects ?? []) || this.rows.some((x) => x.id === r.id)) return
    this.rows = [r, ...this.rows]
    this.changed()
  }

  /** Reads the rows; a read asked while one is out is one read after it. */
  async read(): Promise<void> {
    if (this.busy) {
      this.again = true
      return
    }
    this.busy = true
    const gen = this.gen
    try {
      do {
        this.again = false
        try {
          const out = await this.list({ about: this.subject, limit: ABOUT_LIMIT })
          if (gen !== this.gen) return
          if (!out.filtered) {
            this.refused = true
            this.rows = []
          } else {
            // The requests asked here that the read does not hold yet (asked after the server answered) stay.
            const held = new Set(out.requests.map((r) => r.id))
            this.rows = [...this.rows.filter((r) => !held.has(r.id) && r.state === 'queued'), ...out.requests]
            this.refused = false
          }
        } catch {
          if (gen !== this.gen) return
          this.refused = true
          this.rows = []
        }
        this.loaded = true
        this.changed()
      } while (this.again && gen === this.gen)
    } finally {
      if (gen === this.gen) this.busy = false
    }
  }
}

/**
 * Brakeman about `subject` (a `run:`, `task:`, `session:` or `ticket:` ref): its rows as the live store holds them, the latest brief,
 * the requests still open about it, the answer under each task of a run (`byTask`, for a run's subject), and whether the server
 * refused (an older one): a page draws nothing of Brakeman then.
 */
export function useBrakemanAbout(subject: () => string) {
  const bk = useBrakeman()
  const version = ref(0)
  const reads = new AboutReads((q) => bk.list(q), () => version.value++)
  watch(subject, (s) => reads.set(bk.available.value === true ? s : ''), { immediate: true })
  // Brakeman answering only later (the state's first read): the subject is read then.
  watch(
    () => bk.available.value,
    (a) => reads.set(a === true ? subject() : ''),
  )
  // A new token may be another server's: what was read is dropped and read again under it.
  watch(
    () => bk.generation.value,
    () => {
      reads.set('')
      if (bk.available.value === true) reads.set(subject())
    },
  )
  watch(
    () => bk.events.value,
    (e) => {
      if (e.seq > 0) reads.event(e)
    },
  )

  /** The rows, each as the live store has it now (an event moves its state at once), newest first. */
  const rows = computed<BrakemanRow[]>(() => {
    void version.value
    const live = new Map(bk.requests.value.map((r) => [r.id, r as BrakemanRow]))
    return reads.rows.map((r) => {
      const l = live.get(r.id)
      return l ? { ...r, ...l, digest: l.digest ?? r.digest, match: r.match } : r
    })
  })
  const refused = computed(() => {
    void version.value
    return reads.refused
  })
  const loaded = computed(() => {
    void version.value
    return reads.loaded
  })
  const latest = computed(() => latestBrief(rows.value))
  const open = computed(() => openAbout(bk.state.value, subject()))
  /** For a run's subject, the answer under each of its tasks: from the rows and the requests open now. */
  const byTask = computed<Record<string, BrakemanRow>>(() => {
    const p = parseRef(subject())
    if (!p || p.kind !== 'run') return {}
    const held = new Set(rows.value.map((r) => r.id))
    return answersByTask([...rows.value, ...open.value.map((o) => o.request).filter((r) => !held.has(r.id))], p.run!)
  })

  return {
    rows,
    latest,
    open,
    byTask,
    refused,
    loaded,
    /** Takes a request asked from this page about the subject. */
    add: (r: BrakemanRequest) => reads.add(r),
  }
}

export type BrakemanAbout = ReturnType<typeof useBrakemanAbout>
