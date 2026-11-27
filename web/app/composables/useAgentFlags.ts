import { computed, onScopeDispose, shallowRef, toValue, watch, type MaybeRefOrGetter } from 'vue'
import type { AgentFlags } from './useSessions'

/** A reply `reading` is asked again after this, at most FLAGS_RETRIES times. */
export const FLAGS_RETRY_MS = 1000
export const FLAGS_RETRIES = 5
/** A request with no answer is ended after this. */
export const FLAGS_REQUEST_MS = 10_000
/** A reply `none` is kept this long, then asked again. */
export const FLAGS_NONE_MS = 60_000

type Ask = (id: string, signal: AbortSignal) => Promise<AgentFlags>

/**
 * The flags of each agent and version, asked of the server once (GET /api/catalog/{id}/flags) and kept: a `read` list for the app's
 * life (another version is another key), a `none` for a minute. A `reading` reply is asked again after a second, five times at most;
 * a request is ended after ten seconds. A field narrows the list itself: no request per key.
 */
export class AgentFlagsStore {
  private kept = new Map<string, { reply: AgentFlags; at: number }>()
  private out = new Map<string, Promise<AgentFlags>>()

  constructor(
    private ask: Ask,
    private now: () => number = () => Date.now(),
  ) {}

  private request(key: string, id: string): Promise<AgentFlags> {
    const on = this.out.get(key)
    if (on) return on
    const ctrl = new AbortController()
    const timer = setTimeout(() => ctrl.abort(), FLAGS_REQUEST_MS)
    const p = this.ask(id, ctrl.signal).finally(() => {
      clearTimeout(timer)
      this.out.delete(key)
    })
    this.out.set(key, p)
    return p
  }

  /** Follows agent id at version: `on` gets each answer (null for none to show); the function returned stops it, and drops what comes after. */
  follow(id: string, version: string | undefined, on: (r: AgentFlags | null) => void): () => void {
    const key = `${id}\u0000${version ?? ''}`
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const kept = this.kept.get(key)
    if (kept && (kept.reply.state === 'read' || this.now() - kept.at < FLAGS_NONE_MS)) {
      on(kept.reply)
      return () => {}
    }
    const go = (tries: number) => {
      this.request(key, id).then(
        (r) => {
          if (r.state === 'read' || r.state === 'none') this.kept.set(key, { reply: r, at: this.now() })
          if (stopped) return
          if (r.state === 'reading') {
            if (tries >= FLAGS_RETRIES) {
              on(null)
              return
            }
            on(r)
            timer = setTimeout(() => go(tries + 1), FLAGS_RETRY_MS)
            return
          }
          on(r)
        },
        () => {
          if (!stopped) on(null)
        },
      )
    }
    go(0)
    return () => {
      stopped = true
      clearTimeout(timer)
    }
  }
}

let shared: AgentFlagsStore | undefined

/**
 * The flags of the agent a field is for: `reply` is the server's answer for that agent (null while none has come, or for none to
 * show), `flags` its list when read. A reply for an agent no longer picked is dropped.
 */
export function useAgentFlags(agent: MaybeRefOrGetter<{ id: string; version?: string } | null | undefined>, store?: AgentFlagsStore) {
  const s = store ?? (shared ??= new AgentFlagsStore((id, signal) => useSessions().agentFlags(id, signal)))
  const reply = shallowRef<AgentFlags | null>(null)
  let stop = () => {}
  watch(
    () => {
      const a = toValue(agent)
      return a ? `${a.id}\u0000${a.version ?? ''}` : ''
    },
    () => {
      stop()
      reply.value = null
      const a = toValue(agent)
      if (!a) return
      stop = s.follow(a.id, a.version, (r) => {
        reply.value = r
      })
    },
    { immediate: true },
  )
  onScopeDispose(() => stop())
  return {
    reply,
    flags: computed(() => (reply.value?.state === 'read' ? reply.value.flags : [])),
    reading: computed(() => reply.value?.state === 'reading'),
  }
}
