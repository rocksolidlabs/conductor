import { effectScope, onBeforeUnmount, onMounted, watch, type Ref } from 'vue'
import type { GuideOperation } from '~/utils/brakemanCards'

/**
 * The read of the server's operations (GET /api/guide?section=operations), one for the app, by the workbench token: read when a reader
 * mounts and nothing is known, a read on its way shared. A read that failed leaves nothing known, so the next reader to mount asks again,
 * and so does a new token while a reader shows: a page opened without a valid token (its read refused, the token gate open) has the
 * guide once the right token is saved, with no reload or remount (#538's review). Another token, or none, never keeps the last one's
 * operations (the server may be another one), and a reply on its way for the token before is dropped.
 */
export class GuideOpsRead {
  private gen = 0
  private reading: Promise<void> | null = null
  private readers = 0
  private forToken: string | undefined

  constructor(
    private readonly read: () => Promise<{ operations?: GuideOperation[] | null }>,
    readonly ops: Ref<GuideOperation[] | null>,
  ) {}

  /** The token the operations are for: another one forgets them first, and is read at once while a reader shows (and it is not none). */
  token(t: string) {
    if (this.forToken === t) return
    const before = this.forToken
    this.forToken = t
    if (before === undefined) return
    this.forget()
    if (t && this.readers > 0) void this.load()
  }

  /** Reads the operations while a reader shows, unless they are known; a read on its way is shared. */
  load(): Promise<void> {
    if (this.reading) return this.reading
    if (this.readers === 0 || this.ops.value) return Promise.resolve()
    const gen = this.gen
    const p = (async () => {
      try {
        const body = await this.read()
        if (gen === this.gen) this.ops.value = body.operations ?? []
      } catch {
        /* unread: a proposal is text with its page's link, and no grant is offered, until a reader mounts or the token changes */
      } finally {
        if (gen === this.gen) this.reading = null
      }
    })()
    this.reading = p
    return p
  }

  /** Whether a reader shows the operations. */
  get shown(): boolean {
    return this.readers > 0
  }

  attach() {
    this.readers++
  }

  detach() {
    this.readers = Math.max(0, this.readers - 1)
  }

  /** Forgets what is known and drops a reply on its way. */
  private forget() {
    this.gen++
    this.reading = null
    this.ops.value = null
  }
}

/** Binds the read to the workbench token for the app's lifetime, not a reader's (as useCapabilities' followToken). Returns the stop. */
export function followGuideToken(r: GuideOpsRead, token: Ref<string>): () => void {
  r.token(token.value)
  const scope = effectScope(true)
  scope.run(() => watch(token, (t) => r.token(t)))
  return () => scope.stop()
}

/** Registers the caller's component as a reader of `r` while it is mounted, reading as it mounts when nothing is known. */
export function readGuideWhileMounted(r: GuideOpsRead) {
  onMounted(() => {
    r.attach()
    void r.load()
  })
  onBeforeUnmount(() => r.detach())
}

let reader: GuideOpsRead | null = null

/**
 * The server's operations as the admin's guide lists them: each with its scope and the revision its route checks, shared by what reads
 * them (a brief's proposals, Brakeman's grants in Settings). Null until read, or when it could not be: a proposal is text then, and
 * Settings offers no grant it cannot say is served. Read again for a new token while a reader shows (GuideOpsRead).
 */
export function useGuideOps() {
  const ops = useState<GuideOperation[] | null>('brakemanGuideOps', () => null)
  if (!reader) {
    const { request } = useApi()
    reader = new GuideOpsRead(() => request<{ operations: GuideOperation[] }>('/api/guide', { query: { section: 'operations' } }), ops)
    followGuideToken(reader, useWorkbenchToken().token)
  }
  readGuideWhileMounted(reader)
  return ops
}
