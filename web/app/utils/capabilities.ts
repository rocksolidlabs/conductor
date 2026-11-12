/**
 * What the server has registered beside the open-source feature set (GET /api/capabilities, docs/protocol.md; docs/plans/ext.md, C9):
 * each provider's kind, its name and its state. A screen of a paid feature shows when its kind is here and ready (`has`), never because
 * of a plan or a tier (decision O16); a plain build has none. Pure: the composable (useCapabilities) reads the route.
 */

/** A provider's state: `ready` once its start succeeded (or it needs none); `starting` until then, its slot running the free default. */
export type CapabilityState = 'ready' | 'starting'

/** One registered provider in one slot. A value of the kinds the server names (identity, decisions, context, notifications, sync, entitlements, media, runtime) or one added later: it is listed as it comes. */
export interface Capability {
  kind: string
  name: string
  state: CapabilityState
  /** Who the server is signed in as, on the identity provider's row alone: one line of at most MAX_IDENTITY_DETAIL characters. */
  detail?: string
}

/** A detail's bound, in characters (code points, as Go counts runes): ext.MaxIdentityDetail. */
export const MAX_IDENTITY_DETAIL = 80

/** The identity row's words: "<name>: <detail>" for a ready identity provider with a detail, '' otherwise. */
export function identityLine(caps: Capabilities | null | undefined): string {
  const c = caps?.providers.find((p) => p.kind === 'identity' && isReady(p) && p.detail)
  return c ? `${c.name}: ${c.detail}` : ''
}

/** Whether a ready identity provider shows no detail yet: its sign-in may complete after its start, so the route is read again. */
export function identityPending(caps: Capabilities | null | undefined): boolean {
  return !!caps?.providers.some((p) => p.kind === 'identity' && isReady(p) && !p.detail)
}

/** The route's reply. */
export interface Capabilities {
  providers: Capability[]
}

/** What the Extensions row says when nothing is registered. */
export const NO_EXTENSIONS = 'None: the open-source feature set'

/** Whether a provider is ready: a state the app does not know is taken for starting, so nothing is claimed that is not there. */
export function isReady(c: Capability): boolean {
  return c.state === 'ready'
}

/** Whether the server has a provider of `kind` that is ready; a provider still starting runs the free default, so it is not had yet. */
export function has(caps: Capabilities | null | undefined, kind: string): boolean {
  return !!caps?.providers.some((c) => c.kind === kind && isReady(c))
}

/** Whether any provider is still starting, which the composable reads the route again for. */
export function anyStarting(caps: Capabilities | null | undefined): boolean {
  return !!caps?.providers.some((c) => !isReady(c))
}

/**
 * The Extensions row's words: NO_EXTENSIONS for none, else each provider as `kind: name`, in the server's order, joined by ` · `, one
 * not ready yet followed by ` (starting)`: "identity: cloud · notifications: push (starting)".
 */
export function extensionsWords(caps: Capabilities): string {
  if (caps.providers.length === 0) return NO_EXTENSIONS
  return caps.providers.map((c) => `${c.kind}: ${c.name}${isReady(c) ? '' : ' (starting)'}`).join(' · ')
}

/** A reply as the app keeps it: the rows it can show, anything else in the body dropped, so an odd reply never breaks the card. */
export function asCapabilities(body: unknown): Capabilities {
  const rows = (body as { providers?: unknown } | null)?.providers
  if (!Array.isArray(rows)) return { providers: [] }
  const providers: Capability[] = []
  for (const r of rows) {
    if (!r || typeof r !== 'object') continue
    const { kind, name, state, detail } = r as Record<string, unknown>
    if (typeof kind !== 'string' || typeof name !== 'string' || !kind || !name) continue
    const c: Capability = { kind, name, state: state === 'ready' ? 'ready' : 'starting' }
    if (typeof detail === 'string' && detail && !/[\r\n]/.test(detail) && [...detail].length <= MAX_IDENTITY_DETAIL) c.detail = detail
    providers.push(c)
  }
  return { providers }
}
