import type { Ref } from 'vue'
import { anyStarting, asCapabilities, extensionsWords, has, type Capabilities } from '../utils/capabilities'

/** The first re-read of a reply that has a provider still starting, after the reply; each later one waits twice the last, up to the most. */
export const CAPABILITIES_RETRY_FIRST_MS = 2000
export const CAPABILITIES_RETRY_MOST_MS = 60_000

/**
 * The reads of GET /api/capabilities (docs/plans/ext.md, C9), one for the app: read once a reader shows (after its first paint) and kept
 * while no provider is `starting`; while one is, the route is read again, CAPABILITIES_RETRY_FIRST_MS after the last reply, doubling to
 * CAPABILITIES_RETRY_MOST_MS, and no more once none is, so a page opened while a provider was starting shows it ready when it is, with
 * no reload and no change of token. A provider that never starts costs one small request a minute while a reader shows (`attach`); with
 * none, nothing is read. The cache goes with the workbench token (`token`), as the palette's do: another token, or none, never sees the
 * last one's reply, and a reply on its way for it is dropped. A read that fails keeps what is known.
 */
export class CapabilitiesPoll {
  private gen = 0
  private reading: Promise<void> | null = null
  private timer: ReturnType<typeof setTimeout> | undefined
  private delay = CAPABILITIES_RETRY_FIRST_MS
  private readers = 0
  private forToken: string | undefined

  constructor(
    private readonly read: () => Promise<unknown>,
    readonly caps: Ref<Capabilities | null>,
  ) {}

  /** The token the cache is for: another one forgets it first. */
  token(t: string) {
    if (this.forToken === t) return
    if (this.forToken !== undefined) this.forget()
    this.forToken = t
  }

  /** Reads the route, unless what is known has no provider starting or a re-read is due already; a read on its way is shared. */
  load(): Promise<void> {
    if (this.reading) return this.reading
    if (this.timer !== undefined) return Promise.resolve()
    if (this.caps.value && !anyStarting(this.caps.value)) return Promise.resolve()
    return this.readNow()
  }

  /** A reader shows the capabilities: re-reads may be due. */
  attach() {
    this.readers++
  }

  /** A reader is gone: with none left, no re-read is made (what is known stays, and the next reader's load reads on, its waits anew). */
  detach() {
    this.readers = Math.max(0, this.readers - 1)
    if (this.readers > 0) return
    this.stop()
    this.delay = CAPABILITIES_RETRY_FIRST_MS
  }

  /** Forgets what is known and drops a reply on its way. */
  forget() {
    this.gen++
    this.reading = null
    this.stop()
    this.delay = CAPABILITIES_RETRY_FIRST_MS
    this.caps.value = null
  }

  private stop() {
    if (this.timer !== undefined) clearTimeout(this.timer)
    this.timer = undefined
  }

  private readNow(): Promise<void> {
    const gen = this.gen
    const p = (async () => {
      try {
        const body = await this.read()
        if (gen === this.gen) this.caps.value = asCapabilities(body)
      } catch {
        /* what is known stands */
      } finally {
        if (gen === this.gen) {
          this.reading = null
          this.next()
        }
      }
    })()
    this.reading = p
    return p
  }

  /** After a reply: a re-read while a provider is starting and a reader shows, the wait doubled for the one after; none otherwise. */
  private next() {
    if (!anyStarting(this.caps.value)) {
      this.delay = CAPABILITIES_RETRY_FIRST_MS
      return
    }
    if (this.readers === 0) return
    const wait = this.delay
    this.delay = Math.min(2 * this.delay, CAPABILITIES_RETRY_MOST_MS)
    this.timer = setTimeout(() => {
      this.timer = undefined
      void this.readNow()
    }, wait)
  }
}

let poll: CapabilitiesPoll | null = null

/**
 * What the server has registered (GET /api/capabilities, admin), for a card or page that shows it: `caps` (null until read, or without
 * the workbench token), `words` for the Settings row ('' until read) and `has(kind)`, true for a provider of that kind that is ready. The
 * caller's setup registers it as a reader for as long as it is mounted.
 */
export function useCapabilities() {
  const caps = useState<Capabilities | null>('capabilities', () => null)
  const admin = useWorkbenchToken()
  const { request } = useApi()
  poll ??= new CapabilitiesPoll(() => request<unknown>('/api/capabilities'), caps)
  const p = poll
  onMounted(() => {
    p.attach()
    p.token(admin.token.value)
    requestAnimationFrame(() => {
      if (admin.hasToken.value) void p.load()
    })
  })
  onBeforeUnmount(() => p.detach())
  watch(
    () => admin.token.value,
    (t) => {
      p.token(t)
      if (t) void p.load()
    },
  )
  return {
    caps,
    words: computed(() => (caps.value ? extensionsWords(caps.value) : '')),
    has: (kind: string) => has(caps.value, kind),
  }
}
