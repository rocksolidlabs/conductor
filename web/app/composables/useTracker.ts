import { computed, ref } from 'vue'
import { ApiError } from './useApi'
import { isLoopback, trackerRefusalWords, type RefOptions } from '~/utils/tracker'

// A person's own GitHub tokens on this server (Program item IN1a, docs/protocol.md, "GitHub tokens"): the list the server keeps, each
// token's account, when it was saved, its fingerprint (a mark of this server's start, never a character of the token) and its state; a
// save, which GitHub checks before it is kept; a remove. The token is in a request's body once, the save's, and nowhere else: never in
// the store, an error's words, a toast or an address, and the caller's field is emptied whatever the save's answer.

/** A host's token as the server shows it: never the token. */
export interface TrackerEntry {
  host: string
  login?: string
  accountId?: string
  fingerprint: string
  scopes?: string[]
  savedAt?: string
  source: 'settings' | 'environment'
  state: 'ok' | 'refused' | 'unchecked' | 'unused'
  checkedAt?: string
}

/** What the server answered a remove: the environment's token stays in use for that host. */
export interface RemoveReply {
  removed?: boolean
  inUse?: 'environment'
  message?: string
}

type Request = <T>(path: string, opts?: { method?: string; body?: unknown }) => Promise<T>

/** A save's or a remove's outcome: '' once done, else why not in words that carry nothing of the token. */
export type TrackerOutcome = { ok: true; entry?: TrackerEntry; unchanged?: boolean; words: string } | { ok: false; code: string; words: string }

/** A refusal in the app's words: never the request's body, which held the token. */
function refusal(e: unknown): { ok: false; code: string; words: string } {
  if (e instanceof ApiError) return { ok: false, code: e.code, words: trackerRefusalWords(e.code, e.status === 0 ? 'The server could not be reached.' : 'The server refused it.') }
  return { ok: false, code: 'error', words: 'It could not be done.' }
}

/** The tokens' store over a request function (useApi's, or a test's). */
export function trackerStore(request: Request) {
  const entries = ref<TrackerEntry[]>([])
  /** False on a server that keeps no token (a switchyard), null until the first list answers. */
  const available = ref<boolean | null>(null)
  const error = ref('')
  const loading = ref(false)

  async function load(): Promise<void> {
    loading.value = true
    try {
      const r = await request<{ github?: TrackerEntry[] }>('/api/trackers')
      entries.value = r.github ?? []
      available.value = true
      error.value = ''
    } catch (e) {
      if (e instanceof ApiError && e.status === 403 && e.code === 'switchyard') available.value = false
      else error.value = refusal(e).words
    } finally {
      loading.value = false
    }
  }

  /**
   * Checks a token with GitHub and keeps it (POST /api/trackers/github): the one request that carries it. `take` hands the token over and
   * empties the field it came from before anything is sent, so the page holds it nowhere once the save is on its way.
   */
  async function save(host: string, take: () => string): Promise<TrackerOutcome> {
    const token = take()
    if (!token) return { ok: false, code: 'invalid_token', words: trackerRefusalWords('invalid_token', '') }
    try {
      const r = await request<{ tracker: TrackerEntry; unchanged?: boolean }>('/api/trackers/github', { method: 'POST', body: { host, token } })
      await load()
      return { ok: true, entry: r.tracker, unchanged: !!r.unchanged, words: r.unchanged ? `That token is the one kept for ${host} already.` : `Saved for ${host}: GitHub says it is ${r.tracker.login ?? 'an account'}’s.` }
    } catch (e) {
      return refusal(e)
    }
  }

  /** Stops Conductor's use of a host's token (POST /api/trackers/github/remove): it stays valid on GitHub until revoked there. */
  async function remove(host: string): Promise<TrackerOutcome> {
    try {
      const r = await request<RemoveReply>('/api/trackers/github/remove', { method: 'POST', body: { host } })
      await load()
      if (r.inUse === 'environment') return { ok: true, words: `The server’s environment gives the token for ${host}: that one is still the one in use, and is changed where the server is started.` }
      return { ok: true, words: `Conductor no longer uses the token for ${host}. It stays valid on GitHub until you revoke it there.` }
    } catch (e) {
      return refusal(e)
    }
  }

  /** The hosts a reference may name (utils/tracker.ts, parseGitHubRef): the saved ones, a loopback one only where one is saved. */
  const refOptions = computed<RefOptions>(() => {
    const hosts = entries.value.map((e) => e.host)
    return { hosts, loopback: hosts.some(isLoopback) }
  })

  return { entries, available, error, loading, load, save, remove, refOptions }
}

export type TrackerStore = ReturnType<typeof trackerStore>

let store: TrackerStore | undefined
/** The app's one store of the tokens' list, over useApi, made by the first page that asks. */
export function useTracker(): TrackerStore {
  if (!store) {
    const { request } = useApi()
    store = trackerStore(request)
  }
  return store
}
