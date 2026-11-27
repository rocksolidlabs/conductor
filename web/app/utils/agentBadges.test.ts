import { describe, expect, it } from 'vitest'
import { IDENTITY_COLOR, NOT_INSTALLED_BADGE, identityBadge } from './agentBadges'

describe('agent badges', () => {
  it('draws amber only for a check that failed, red for another program, and neutral for the rest', () => {
    expect(IDENTITY_COLOR).toEqual({ ok: 'neutral', impostor: 'error', unidentified: 'neutral', pending: 'neutral', failed: 'warning' })
    expect(NOT_INSTALLED_BADGE.color).toBe('neutral')
    for (const c of Object.values(IDENTITY_COLOR)) expect(c).not.toBe('success')
  })

  it('gives an identified agent its icon and colour, and an agent not checked no badge', () => {
    expect(identityBadge('ok')).toEqual({ color: 'neutral', icon: 'i-lucide-badge-check' })
    expect(identityBadge('impostor')?.color).toBe('error')
    expect(identityBadge('unprobed')).toBeNull()
  })
})
