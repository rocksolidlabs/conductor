import { describe, expect, it } from 'vitest'
import type { Decision } from './decisions'
import { answeredHow, answerers, approves, canAskRouter, countedWords, fullWords, ownResponse, placesFull, eligibleWords, heldBackWords, knownStatus, notInForceWords, operatorHeldBack, overrideAllowed, overrideLine, overrideReasonReady, ownStanding, ownStandingWords, policyRefusalWords, quorumWords, routedPolicy, severalApprovals, stillNeeded, whoMayAnswer, type RoutedPolicy } from './decisionPolicy'
import type { DecisionQuorum } from './protocol'

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

// Several approvals (SW1 PR-9, docs/plans/decision-policy.md, "Groups, quorum and distinct persons"): the count from the operator's
// read's quorum, what is still needed, distinct persons, the person's own counted answer, the two refusals, and a status the page does
// not know.

const LABELS: Record<string, string> = { architecture: 'architecture owner', security: 'security owner', release: 'release manager', privacy: 'privacy reviewer' }
const groupOf = (role: string, quorum = 1) => ({ role, label: LABELS[role] ?? role, quorum, rule: `r-${role}`, eligible: [{ subject: role, name: role, via: 'role', ref: 'x' }] })
const several = (roles: [string, number][], distinct = false): RoutedPolicy => policy({ distinct, groups: roles.map(([r, q]) => groupOf(r, q)) })
const quorum = (gs: [string, number, number][]): DecisionQuorum => {
  const groups = gs.map(([role, q, counted]) => ({ role, quorum: q, counted, met: counted >= q }))
  return { places: groups.reduce((n, g) => n + g.quorum, 0), filled: groups.reduce((n, g) => n + Math.min(g.counted, g.quorum), 0), groups }
}

describe('several approvals: the count', () => {
  it('says nothing new for one group of quorum 1: a person with a simple policy sees what they saw before', () => {
    const p = several([['architecture', 1]])
    expect(severalApprovals(quorum([['architecture', 1, 0]]))).toBe(false)
    expect(quorumWords(p, quorum([['architecture', 1, 0]]))).toBeNull()
    expect(quorumWords(p, quorum([['architecture', 1, 1]]))).toBeNull()
    expect(quorumWords(p, null)).toBeNull()
    expect(stillNeeded(p, quorum([['architecture', 1, 0]]))).toBe('')
  })

  it('says one group of a quorum above one by its count alone, with no lead', () => {
    const p = several([['architecture', 2]])
    expect(quorumWords(p, quorum([['architecture', 2, 1]]))).toEqual({ lead: '', groups: [{ role: 'architecture', words: '1 of 2 for the architecture owner', met: false }], distinct: '' })
    expect(quorumWords(p, quorum([['architecture', 2, 2]]))!.groups[0]).toEqual({ role: 'architecture', words: '2 of 2 for the architecture owner: met', met: true })
    expect(quorumWords(several([['architecture', 4]]), quorum([['architecture', 4, 3]]))!.groups[0]!.words).toBe('3 of 4 for the architecture owner')
    expect(stillNeeded(p, quorum([['architecture', 2, 0]]))).toBe('It waits for 2 more approvals for the architecture owner.')
    expect(stillNeeded(p, quorum([['architecture', 2, 1]]))).toBe('It waits for the architecture owner.')
  })

  it('says "1 of 2" for two groups, each group met or not, and what it still waits for', () => {
    const p = several([['architecture', 1], ['security', 1]])
    const q = quorum([['architecture', 1, 1], ['security', 1, 0]])
    expect(quorumWords(p, q)).toEqual({
      lead: '1 of 2 approvals',
      groups: [
        { role: 'architecture', words: '1 of 1 for the architecture owner: met', met: true },
        { role: 'security', words: '0 of 1 for the security owner', met: false },
      ],
      distinct: '',
    })
    expect(stillNeeded(p, q)).toBe('It waits for the security owner.')
    expect(stillNeeded(p, quorum([['architecture', 1, 1], ['security', 1, 1]]))).toBe('')
    expect(quorumWords(p, quorum([['architecture', 1, 0], ['security', 1, 0]]))!.lead).toBe('0 of 2 approvals')
  })

  it('says four groups of quorum four, each count, and every group still owed', () => {
    const p = several([['architecture', 4], ['security', 4], ['release', 4], ['privacy', 4]], true)
    const q = quorum([['architecture', 4, 4], ['security', 4, 1], ['release', 4, 3], ['privacy', 4, 0]])
    const w = quorumWords(p, q)!
    expect(w.lead).toBe('8 of 16 approvals')
    expect(w.groups.map((g) => g.words)).toEqual(['4 of 4 for the architecture owner: met', '1 of 4 for the security owner', '3 of 4 for the release manager', '0 of 4 for the privacy reviewer'])
    expect(w.distinct).toBe('One person may fill only one group.')
    expect(stillNeeded(p, q)).toBe('It waits for 3 more approvals for the security owner, the release manager and 4 more approvals for the privacy reviewer.')
  })

  it('says distinct persons once, and only where there is more than one group to fill', () => {
    expect(quorumWords(several([['architecture', 1], ['security', 1]], true), quorum([['architecture', 1, 0], ['security', 1, 0]]))!.distinct).toBe('One person may fill only one group.')
    expect(quorumWords(several([['architecture', 1], ['security', 1]], false), quorum([['architecture', 1, 0], ['security', 1, 0]]))!.distinct).toBe('')
    expect(quorumWords(several([['architecture', 3]], true), quorum([['architecture', 3, 1]]))!.distinct).toBe('')
  })

  it('shows the count the read gives once answered by its approvals: full, the last group met, never one short', () => {
    // The server's count includes the approval that answered (cam's, accepted), though the record holds one counted row.
    const w = quorumWords(several([['architecture', 1], ['security', 1]]), { places: 2, filled: 2, groups: [{ role: 'architecture', quorum: 1, counted: 1, met: true }, { role: 'security', quorum: 1, counted: 1, met: true }] })!
    expect(w.lead).toBe('2 of 2 approvals')
    expect(w.groups.map((g) => g.words)).toEqual(['1 of 1 for the architecture owner: met', '1 of 1 for the security owner: met'])
  })

  it('names a group the policy does not label by its role, and never draws a count past its quorum or a broken one', () => {
    const p = several([['architecture', 1]])
    const q: DecisionQuorum = { places: 3, filled: 9, groups: [{ role: 'architecture', quorum: 1, counted: 5, met: true }, { role: 'legal', quorum: 2, counted: -1, met: true }] }
    const w = quorumWords(p, q)!
    expect(w.lead).toBe('3 of 3 approvals')
    expect(w.groups).toEqual([
      { role: 'architecture', words: '1 of 1 for the architecture owner: met', met: true },
      // A group said met with nothing counted is not drawn met.
      { role: 'legal', words: '0 of 2 for the legal', met: false },
    ])
  })
})

describe('several approvals: a person\'s own answer', () => {
  const p = several([['architecture', 1], ['security', 1]])
  it("says the operator's own counted approval with the count and what it still waits for", () => {
    expect(countedWords(p, quorum([['architecture', 1, 1], ['security', 1, 0]]))).toBe('Your approval is counted: 1 of 2 approvals. It waits for the security owner.')
    expect(countedWords(several([['architecture', 2]]), quorum([['architecture', 2, 1]]))).toBe('Your approval is counted: 1 of 2 for the architecture owner. It waits for the architecture owner.')
    expect(countedWords(p, null)).toBe('Your approval is counted. It waits for the other approvals it needs.')
    expect(countedWords(null, null)).toBe('Your approval is counted. It waits for the other approvals it needs.')
  })

  it('reads where a person stands from their newest response as read: counted, no longer counting, or unknown', () => {
    expect(ownStanding([])).toBeNull()
    expect(ownStanding(undefined)).toBeNull()
    expect(ownStanding([{ k: 1, status: 'counted' }])).toBe('counted')
    // A counted approval that no longer counts reads refused, not_confirmed: lapsed, and answered again while open.
    expect(ownStanding([{ k: 1, status: 'refused', code: 'not_confirmed' }])).toBe('lapsed')
    expect(ownStanding([{ k: 1, status: 'refused', code: 'stale_decision' }])).toBeNull()
    expect(ownStanding([{ k: 2, status: 'refused', code: 'not_confirmed' }, { k: 1, status: 'counted' }])).toBe('lapsed')
    expect(ownStanding([{ k: 1, status: 'refused', code: 'not_confirmed' }, { k: 2, status: 'counted' }])).toBe('counted')
    // A counted approver's Reject answers it: the newest row is accepted.
    expect(ownStanding([{ k: 1, status: 'counted' }, { k: 2, status: 'accepted' }])).toBeNull()
    // A status a newer server gives is neither an approval nor nothing: it offers nothing.
    expect(ownStanding([{ k: 1, status: 'pending_review' }])).toBe('unknown')
    expect(knownStatus('counted')).toBe(true)
    expect(knownStatus('approved')).toBe(false)
  })

  it("finds the operator's own responses in the read by its subject's actor alone, never the workbench token's", () => {
    const rows = [
      { decision: 'dr-8', k: 1, status: 'counted', words: '', actor: 'user:ana', at: '', rev: 2 },
      { decision: 'dr-8', k: 2, status: 'counted', words: '', actor: 'user:cam', at: '', rev: 3 },
      { decision: 'dr-8', k: 3, status: 'refused', code: 'not_confirmed', words: '', actor: 'user:ana', at: '', rev: 3 },
      { decision: 'dr-8', k: 4, status: 'accepted', words: '', actor: 'admin', at: '', rev: 4 },
    ]
    expect(ownResponse({ responses: rows }, 'ana')?.k).toBe(3)
    expect(ownResponse({ responses: rows }, 'cam')?.k).toBe(2)
    expect(ownResponse({ responses: rows }, '')).toBeUndefined()
    expect(ownResponse({ responses: rows }, 'dee')).toBeUndefined()
    expect(ownResponse({ responses: undefined }, 'ana')).toBeUndefined()
  })

  it('says the decide page\'s standing: counted offers every answer, an approval no longer counting is said once closed, nothing more', () => {
    expect(ownStandingWords('counted', 'dr-8')).toBe('Your approval of dr-8 is counted. You may still choose another answer; approving again answers dr-8 only when every other approval it needs is in.')
    expect(ownStandingWords('counted', 'dr-8', false)).toBe('')
    // While open the outcome says it (the server's "answer again"); once closed the page says only that it no longer counts.
    expect(ownStandingWords('lapsed', 'dr-8', true)).toBe('')
    expect(ownStandingWords('lapsed', 'dr-8', false)).toBe('Your approval of dr-8 no longer counts.')
    expect(ownStandingWords('lapsed', 'dr-8', false)).not.toContain('answer again')
    expect(ownStandingWords('unknown', 'dr-8')).toBe('This page cannot show what became of your answer to dr-8: nothing more is asked of you here.')
    expect(ownStandingWords('unknown', 'dr-8')).not.toMatch(/approv|counted/)
    expect(ownStandingWords(null, 'dr-8')).toBe('')
  })

  it("holds back only the approval of a counted approver while places are left, and offers it once every place is filled", () => {
    const short = quorum([['architecture', 1, 1], ['security', 1, 0]])
    const full = quorum([['architecture', 1, 1], ['security', 1, 1]])
    expect(placesFull(short)).toBe(false)
    expect(placesFull(full)).toBe(true)
    expect(placesFull(quorum([['architecture', 1, 1]]))).toBe(false)
    expect(placesFull(null)).toBe(false)
    expect(approves({ type: 'approval' }, 0)).toBe(true)
    expect(approves({ type: 'approval' }, 1)).toBe(false)
    expect(approves({ type: 'assignment' }, 0)).toBe(true)
    expect(approves({ type: 'clarification' }, 0)).toBe(false)
    expect(countedWords(p, short, 'Reject')).toBe('Your approval is counted: 1 of 2 approvals. It waits for the security owner. You may still answer Reject.')
    expect(fullWords(true)).toBe('Your approval is counted, and every approval it needs is in: approving again answers it.')
    expect(fullWords(false)).toBe('Every approval it needs is in: an approval now answers it.')
  })

  it('says the two refusals in their own words', () => {
    expect(policyRefusalWords('not_needed', 'dr-7', p)).toBe('Not counted: your approval fills no place dr-7 still needs.')
    expect(policyRefusalWords('already_counted', 'dr-7', p)).toBe('Your approval of dr-7 is counted already: nothing more is asked of you.')
  })
})

describe('several approvals: how it was answered', () => {
  const p = several([['architecture', 1], ['security', 1]])
  const q = quorum([['architecture', 1, 1], ['security', 1, 0]])
  const request = { question: 'Ship it?', options: [{ label: 'Approve' }, { label: 'Reject' }] }
  const counted = { decision: 'dr-8', k: 1, status: 'counted', words: 'Counted', actor: 'user:ana', byName: 'Ana', choice: 0, at: '', rev: 2 }
  const rec = (state: Decision['state'], responses: unknown[], type: Decision['type'] = 'approval') => ({ state, type, request, responses } as unknown as Pick<Decision, 'state' | 'type' | 'responses' | 'request'>)

  it('says one eligible Reject answered it at once, and the approvals counted before it stay', () => {
    const reject = { decision: 'dr-8', k: 2, status: 'accepted', words: 'Recorded', actor: 'user:cam', byName: 'Cam', choice: 1, at: '', rev: 3 }
    expect(answeredHow(rec('answered', [counted, reject]), q)).toBe('Reject by Cam: one eligible Reject answers it at once. The approval counted before it stays in the record.')
    expect(answeredHow(rec('answered', [reject]), q)).toBe('Reject by Cam: one eligible Reject answers it at once.')
    expect(answeredHow(rec('answered', [{ ...counted, k: 3 }, counted, reject]), q)).toContain('The 2 approvals counted before it stay in the record.')
    expect(answeredHow(rec('answered', [{ ...reject, choice: 1 }], 'assignment'), q)).toBe('Not done by Cam: one eligible Not done answers it at once.')
  })

  it('counts only the approvals that stand: one that no longer counts is not said as kept', () => {
    const reject = { decision: 'dr-8', k: 3, status: 'accepted', words: 'Recorded', actor: 'user:cam', byName: 'Cam', choice: 1, at: '', rev: 4 }
    const lapsed = { ...counted, k: 2, status: 'refused', code: 'not_confirmed', words: "Dan's approval of dr-8 no longer counts: its sign-in could not be rechecked" }
    expect(answeredHow(rec('answered', [counted, lapsed, reject]), q)).toBe('Reject by Cam: one eligible Reject answers it at once. The approval counted before it stays in the record.')
    expect(answeredHow(rec('answered', [lapsed, reject]), q)).toBe('Reject by Cam: one eligible Reject answers it at once.')
  })

  it('says an override answered every group at once', () => {
    const override = { decision: 'dr-8', k: 2, status: 'accepted', words: 'Recorded', actor: 'admin', byName: 'Nate', choice: 0, override: true, rationale: 'Ana is away this week', at: '', rev: 3 }
    expect(answeredHow(rec('answered', [counted, override]), q)).toBe('The override answered every group at once. The approval counted before it stays in the record.')
  })

  it('says nothing for an open record, one answered by its approvals, or one of a single place', () => {
    const last = { decision: 'dr-8', k: 2, status: 'accepted', words: 'Recorded', actor: 'user:cam', byName: 'Cam', choice: 0, at: '', rev: 3 }
    expect(answeredHow(rec('open', [counted]), q)).toBe('')
    expect(answeredHow(rec('answered', [counted, last]), q)).toBe('')
    expect(answeredHow(rec('answered', [{ ...last, choice: 1 }]), quorum([['architecture', 1, 0]]))).toBe('')
    void p
  })
})
