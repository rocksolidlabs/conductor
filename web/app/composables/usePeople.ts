import type { Responsibility } from './useSessions'

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

/**
 * The names Conductor knows (docs/plans/decisions.md, HM1): the union of Dispatch's directory and the saved crews' people, read from both
 * and written to neither. `load` reads the first page again (200 names, `q` keeping those that contain it); a reply older than the last
 * read started is dropped, so a slow one never puts back names a newer one replaced. A failed read keeps the names read before: a typed
 * name is still taken.
 */
export function usePeople() {
  const { request } = useApi()
  const names = ref<string[]>([])
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
    try {
      const r = await page({ q })
      if (n === seq) names.value = r.people.map((p) => p.name)
    } catch {
      // The names read before stay offered.
    }
  }

  return { names, load, page }
}
