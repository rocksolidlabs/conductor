import type { IdentityState } from './agents'

/**
 * How an agent's badges are drawn, on the Agents page and in Settings' Agents on this machine: one table, so the two never
 * disagree. Amber only where a person should look (the UX review's P16): a check that failed. An identified agent, one the check
 * could not name and one not installed ask nothing, so they are neutral; green is for a live agent. Another program under the
 * agent's name is an error.
 */
export const IDENTITY_COLOR: Record<Exclude<IdentityState, 'unprobed'>, 'error' | 'warning' | 'neutral'> = {
  ok: 'neutral',
  impostor: 'error',
  unidentified: 'neutral',
  pending: 'neutral',
  failed: 'warning',
}

export const IDENTITY_ICON: Record<Exclude<IdentityState, 'unprobed'>, string> = {
  ok: 'i-lucide-badge-check',
  impostor: 'i-lucide-shield-alert',
  unidentified: 'i-lucide-help-circle',
  pending: 'i-lucide-loader-circle',
  failed: 'i-lucide-triangle-alert',
}

/** The badge of an agent not installed: neutral, with its own icon. */
export const NOT_INSTALLED_BADGE = { color: 'neutral', icon: 'i-lucide-circle-off' } as const

/** The colour and icon of an identity badge; none for an agent not yet checked. */
export function identityBadge(state: IdentityState): { color: 'error' | 'warning' | 'neutral'; icon: string } | null {
  if (state === 'unprobed') return null
  return { color: IDENTITY_COLOR[state], icon: IDENTITY_ICON[state] }
}
