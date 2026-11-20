import { describe, expect, it } from 'vitest'
import type { Decision } from './decisions'
import { answerers, canAskRouter, eligibleWords, heldBackWords, notInForceWords, operatorHeldBack, overrideAllowed, overrideLine, overrideReasonReady, policyRefusalWords, routedPolicy, whoMayAnswer, type RoutedPolicy } from './decisionPolicy'

// A decision router's policy in words (SW1 PR-8, docs/plans/decision-policy.md, "web/app"): every via, a gap, not in force, and an
// override, from the record alone and the operator's read's policyNow.

function policy(over: Partial<RoutedPolicy> = {}): RoutedPolicy {
  return {
    revision: 'r1',
    approver: 'router',
    router: 'switchman',
    scope: 's1',
    override: 'operator',
    groups: [
      {
        role: 'architecture',
        label: 'architecture owner',
        quorum: 1,
        rule: 'r-1',
        eligible: [
          { subject: 'ana', name: 'Ana', via: 'role', ref: 'x' },
          { subject: 'ben', name: 'Ben', via: 'delegate', for: 'ana', ref: 'y' },
          { subject: 'cam', name: 'Cam', via: 'backup', ref: 'z' },
          { subject: 'dee', name: 'Dee', via: 'assigned', ref: 'w' },
        ],
      },
    ],
    ...over,
  }
}

const record = (p: unknown, state: Decision['state'] = 'open') => ({ policy: p as Decision['policy'], state })

describe('who may answer', () => {
  it('names each person with how they are eligible: the role or by name plainly, a delegate beside whom, a backup', () => {
    const p = policy()
    const g = p.groups[0]!
    expect(g.eligible!.map((e) => eligibleWords(e, g))).toEqual(['Ana', 'Ben (delegate for Ana)', 'Cam (backup)', 'Dee'])
    expect(whoMayAnswer(p)).toEqual([{ role: 'architecture', words: 'For the architecture owner: Ana, Ben (delegate for Ana), Cam (backup), Dee', rule: 'r-1', gap: false }])
    expect(answerers(p)).toBe('Ana, Ben, Cam or Dee')
  })

  it("says a gap in its own words, and the group's when it gave none", () => {
    const p = policy({ groups: [{ role: 'security', label: 'security owner', quorum: 1, rule: 'r-2', gap: 'Waiting for a security owner: none is assigned in CND' }] })
    expect(whoMayAnswer(p)).toEqual([{ role: 'security', words: 'Waiting for a security owner: none is assigned in CND', rule: 'r-2', gap: true }])
    expect(whoMayAnswer(policy({ groups: [{ role: 'security', label: 'security owner', quorum: 1, rule: 'r-2' }] }))[0]!.words).toBe('Waiting for the security owner: none is assigned')
    expect(heldBackWords(p)).toBe('This is for the security owner: nobody is assigned yet')
  })

  it("reads a router's policy alone: the server's own is none", () => {
    expect(routedPolicy(record(policy()))?.revision).toBe('r1')
    expect(routedPolicy(record({ revision: 'local/1', approver: 'operator', quorum: 1 }))).toBeNull()
    expect(routedPolicy(record({ approver: 'router' }))).toBeNull()
    expect(routedPolicy(null)).toBeNull()
  })
})

describe('in force or not', () => {
  it('says the operator answers when the read says the policy is not in force, and nothing when it is or did not say', () => {
    const p = policy({ groups: [policy().groups[0]!].map((g) => ({ ...g, eligible: g.eligible!.slice(0, 1) })) })
    expect(notInForceWords(p, { inForce: false, why: 'the roster is moving' })).toBe('Routed to Ana: not in force now (the roster is moving). The operator answers.')
    expect(notInForceWords(p, { inForce: false })).toBe('Routed to Ana: not in force now. The operator answers.')
    expect(notInForceWords(p, { inForce: true })).toBe('')
    expect(notInForceWords(p, null)).toBe('')
  })

  it("holds the operator's answer back only under a router's policy in force, on an open record", () => {
    expect(operatorHeldBack(record(policy()), { inForce: true })).toBe(true)
    expect(operatorHeldBack(record(policy()), { inForce: false })).toBe(false)
    // An identity's operator the policy names, confirmed now, answers as eligible: never held back.
    expect(operatorHeldBack(record(policy()), { inForce: true, eligible: true })).toBe(false)
    expect(operatorHeldBack(record(policy()), null)).toBe(false)
    expect(operatorHeldBack(record(policy(), 'answered'), { inForce: true })).toBe(false)
    expect(operatorHeldBack(record({ revision: 'local/1', approver: 'operator' }), { inForce: true })).toBe(false)
    expect(heldBackWords(policy({ groups: [policy().groups[0]!].map((g) => ({ ...g, eligible: g.eligible!.slice(0, 1) })) }))).toBe("This is Ana's to answer")
  })
})

describe('an override', () => {
  it('is offered only where the policy lets the operator answer, with a reason of at least ten characters', () => {
    expect(overrideAllowed(policy())).toBe(true)
    expect(overrideAllowed(policy({ override: 'none' }))).toBe(false)
    expect(overrideAllowed(null)).toBe(false)
    expect(overrideReasonReady('too short')).toBe(false)
    expect(overrideReasonReady('   ten chars ')).toBe(false)
    expect(overrideReasonReady('Ana is away')).toBe(true)
  })

  it("says an accepted override by its receipt, and nothing for any other answer", () => {
    expect(overrideLine({ status: 'accepted', override: true, byName: 'Nate', rationale: 'Ana is away this week', actor: 'admin' })).toBe('Overridden by Nate: Ana is away this week')
    expect(overrideLine({ status: 'accepted', override: true, actor: 'admin' })).toBe('Overridden by the operator')
    expect(overrideLine({ status: 'accepted', rationale: 'fine', actor: 'admin' })).toBe('')
    // A refused answer overrode nothing, whatever its receipt says.
    expect(overrideLine({ status: 'refused', override: true, rationale: 'Ana is away this week', actor: 'admin' })).toBe('')
  })

  it('offers Ask the router again for an open record whenever a router is registered, policy or none, never for a selection', () => {
    expect(canAskRouter({ state: 'open', origin: 'agent' }, true)).toBe(true)
    expect(canAskRouter({ state: 'open', origin: 'plan' }, false)).toBe(false)
    expect(canAskRouter({ state: 'answered', origin: 'agent' }, true)).toBe(false)
    expect(canAskRouter({ state: 'open', origin: 'compare' }, true)).toBe(false)
  })

  it("says the policy's refusals in words", () => {
    expect(policyRefusalWords('not_eligible', 'dr-7', policy({ groups: [policy().groups[0]!].map((g) => ({ ...g, eligible: g.eligible!.slice(0, 1) })) }))).toBe("This is Ana's to answer: answer only by an override, with its reason.")
    // A policy allowing no override never sends the operator to one.
    const none = policyRefusalWords('not_eligible', 'dr-7', policy({ override: 'none', groups: [policy().groups[0]!].map((g) => ({ ...g, eligible: g.eligible!.slice(0, 1) })) }))
    expect(none).toBe("This is Ana's to answer: its policy allows no override.")
    expect(none).not.toContain('by an override')
    expect(policyRefusalWords('override_refused', 'dr-7', policy())).toContain('allows no override')
    expect(policyRefusalWords('routed_by_policy', 'dr-7', policy())).toContain('ask the router again')
    expect(policyRefusalWords('stale_decision', 'dr-7', policy())).toBe('')
  })
})
