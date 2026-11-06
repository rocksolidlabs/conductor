import type { GuideOperation } from '~/utils/brakemanCards'

/**
 * The server's operations as the admin's guide lists them (GET /api/guide?section=operations): each with its scope and the revision its
 * route checks, read once for the app and shared by what reads it (a brief's proposals, Brakeman's grants in Settings). Null until read,
 * or when it could not be: a proposal is text then, and Settings offers no grant it cannot say is served. A read that failed is asked
 * again the next time a reader mounts.
 */
export function useGuideOps() {
  const { request } = useApi()
  const ops = useState<GuideOperation[] | null>('brakemanGuideOps', () => null)
  onMounted(async () => {
    if (ops.value) return
    try {
      ops.value = (await request<{ operations: GuideOperation[] }>('/api/guide', { query: { section: 'operations' } })).operations ?? []
    } catch {
      /* unread: a proposal is text with its page's link, and no grant is offered */
    }
  })
  return ops
}
