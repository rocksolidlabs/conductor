import type { Responsibility } from './useSessions'
import type { Decision } from '~/utils/decisions'
import { PEOPLE_LIMITS } from '~/utils/crews'
import { oneReadAtATime, personSegment } from '~/utils/people'

/** A person Conductor knows (GET /api/people): in Dispatch's directory, in saved crews, or both. */
export interface KnownPerson {
  name: string
  /** Dispatch's directory has the name. */
  dispatch: boolean
  /** At most 20 of the crews that list the person, with what they hold in each; `crewsMore` past them. */
  crews: Array<{ id: string; name: string; responsibilities: Responsibility[] }>
  crewsMore?: boolean
}

/** One page of GET /api/people: at most `limit` names (200 when absent), `next` the name the next page starts after. */
export interface PeoplePage {
  people: KnownPerson[]
  next?: string
  /** Dispatch is off or could not be read: the crews' people are listed all the same. */
  dispatchUnavailable?: boolean
}

/** A task a person owns in a run, as the run's crew was launched: by the task's own owner, or as the crew's acceptance owner. */
export interface PersonTask {
  run: string
  /** The run's label, else its crew's name. */
  runLabel: string
  task: string
  state: string
  /** The member (the agent) that does it. */
  member: string
  ownerFrom: 'task' | 'acceptance'
}

/** A person's work (GET /api/people/{name}). */
export interface PersonWork {
  name: string
  dispatch: boolean
  /** At most 100 of the saved crews that list the person; `crewsMore` past them. */
  crews: KnownPerson['crews']
  crewsMore?: boolean
  /** At most 200, the newest run first; `more` past them. */
  tasks: PersonTask[]
  more?: boolean
  /** Dispatch's tickets they own and are assigned: the counts and Dispatch's own two lists, paged by their cursor; null while Dispatch is unavailable. */
  tickets: { owner: number; assignee: number; ownerHref: string; assigneeHref: string } | null
  /** The decisions for them, open and answered, the newest first, at most 100, each as the operator's lists give it (HM1, PR-12). */
  decisions: Decision[]
  decisionsMore?: boolean
  /** The ledger could not be read: their decisions are not shown. */
  decisionsUnavailable?: boolean
  /** Dispatch is off or could not be read: no tickets, and `dispatch` true only when its directory was read and lists them. */
  dispatchUnavailable?: boolean
  /** The saved crews, or some of them, could not be read: the crews listed are those read, and may not be all. */
  crewsUnavailable?: boolean
  /** The managed runs' records, or some of them, could not be read: the tasks listed are those of the runs the server holds and the records read, and may not be all. */
  recordsUnavailable?: boolean
}

/**
 * The names Conductor knows (docs/plans/decisions.md, HM1): the union of Dispatch's directory and the saved crews' people, read from both
 * and written to neither. `load` reads the first page again (200 names, `q` keeping those that contain it, so a name past the first page
 * of all is found by searching for it); a reply older than the last read started is dropped, so a slow one never puts back names a newer
 * one replaced. A failed read keeps the names read before: a typed name is still taken. A `q` longer than a name (60 characters) is
 * contained in no name, so it is answered with none, without a read the server would refuse. `search` is `load` for a `q` typed key by
 * key (the palette's `@`), one read at a time: a `q` asked while one is under way is read after it, the latest only, and a reply is
 * dropped when a newer `q` was asked meanwhile, so typing sends no read per key.
 */
export function usePeople() {
  const { request } = useApi()
  const names = ref<string[]>([])
  /** The first page as read last, whole: for the palette's People. */
  const known = ref<KnownPerson[]>([])
  let seq = 0

  /** One page of the people, as the server answers it. */
  function page(query: { q?: string; after?: string; limit?: number } = {}): Promise<PeoplePage> {
    const q: Record<string, string> = {}
    if (query.q) q.q = query.q
    if (query.after) q.after = query.after
    if (query.limit) q.limit = String(query.limit)
    return request<PeoplePage>('/api/people', { query: q }).then((r) => ({ ...r, people: r.people ?? [] }))
  }

  async function load(q?: string): Promise<void> {
    const n = ++seq
    if (q && [...q].length > PEOPLE_LIMITS.name) {
      known.value = []
      names.value = []
      return
    }
    try {
      const r = await page({ q })
      if (n === seq) {
        known.value = r.people
        names.value = r.people.map((p) => p.name)
      }
    } catch {
      // The names read before stay offered.
    }
  }

  const search = oneReadAtATime(
    (q) => ([...q].length > PEOPLE_LIMITS.name ? Promise.resolve<PeoplePage>({ people: [] }) : page({ q })),
    (r) => {
      known.value = r.people
      names.value = r.people.map((p) => p.name)
    },
  )

  /** A person's work, by their name exactly (its segment by `personSegment`, the rule every link to their page uses). */
  function person(name: string): Promise<PersonWork> {
    return request<PersonWork>(`/api/people/${personSegment(name)}`).then((r) => ({ ...r, crews: r.crews ?? [], tasks: r.tasks ?? [], decisions: r.decisions ?? [] }))
  }

  return { names, known, load, search, page, person }
}
