import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { operatorHeldBack, overrideAllowed, overrideReasonReady } from '../utils/decisionPolicy'

// The operator's answer under a decision router's policy (SW1 PR-8, docs/plans/decision-policy.md, "web/app"): under a policy in force
// the answer buttons are disabled with whose it is to answer, and Override… (where the policy allows one) opens a dialog whose send is
// held until a choice and a reason of at least ten characters are there, and which sends the answer with override and the reason as
// its rationale. The app's vitest mounts no component: the rules are the functions the component draws from, and its source is
// checked for the bindings that draw them; what a browser shows is decision-policy.spec.ts's.

const src = readFileSync(new URL('./DecisionRespond.vue', import.meta.url), 'utf8')
const routed = { approver: 'router', groups: [{ role: 'architecture', label: 'architecture owner', quorum: 1, rule: 'r-1', eligible: [{ subject: 'ana', name: 'Ana', via: 'role', ref: 'x' }] }], override: 'operator' }

describe("the operator's answer under a router's policy", () => {
  it('holds the buttons back exactly while the policy is in force, and says whose it is', () => {
    expect(operatorHeldBack({ policy: routed as never, state: 'open' }, { inForce: true })).toBe(true)
    expect(operatorHeldBack({ policy: routed as never, state: 'open' }, { inForce: false })).toBe(false)
    expect(src).toMatch(/:disabled="busy \|\| heldBack"/)
    expect(src).toMatch(/v-if="heldBack"[^>]*data-decision-held-back/)
    expect(src).toContain('{{ heldWords }}.')
  })

  it('offers Override… only where the policy allows one', () => {
    expect(overrideAllowed(routed as never)).toBe(true)
    expect(overrideAllowed({ ...routed, override: 'none' } as never)).toBe(false)
    expect(src).toMatch(/v-if="canOverride" label="Override…"/)
    expect(src).toContain('const canOverride = computed(() => heldBack.value && overrideAllowed(policy.value))')
  })

  it('requires a reason of at least ten characters and a choice before the override goes, and sends it as an override', () => {
    expect(overrideReasonReady('')).toBe(false)
    expect(overrideReasonReady('short')).toBe(false)
    expect(overrideReasonReady('Ana is away this week')).toBe(true)
    expect(src).toContain('overrideReasonReady(overrideReason.value) && (overrideChoice.value !== undefined || !!overrideText.value.trim())')
    expect(src).toMatch(/:disabled="!overrideReady" data-decision-override-send/)
    expect(src).toMatch(/rationale: overrideReason\.value, expectedRev: d\.value\.rev, byName: identity\.name\.value, override: true/)
  })
})
