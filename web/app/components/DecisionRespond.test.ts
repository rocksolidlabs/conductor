import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { countedWords, knownStatus, operatorHeldBack, overrideAllowed, overrideReasonReady, policyRefusalWords } from '../utils/decisionPolicy'

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
    expect(src).toMatch(/:disabled="busy \|\| locked \|\| heldBack \|\| choiceHeld\(i\)"/)
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

// Several approvals (SW1 PR-9), judged by the record as read, never by a send's reply: the operator's own newest response (the rows
// kept under its subject) says where its answer stands. Counted with places left: the approval alone held back, with the count's words,
// every other answer (a Reject) offered; every place filled while open: the approval offered, with words; no longer counting: the
// server's words and the answer offered again; a status the page does not know: neutral words, nothing offered.
describe("the operator's own approval under several approvals", () => {
  it('judges its own answer by the read with its subject, never by the last reply', () => {
    expect(src).toContain('const own = computed(() => ownResponse(d.value, props.subject))')
    expect(src).toContain("const standing = computed(() => (d.value.state === 'open' && own.value ? ownStanding([own.value]) : null))")
    expect(src).not.toMatch(/receipt\.value\?\.status === 'counted'/)
    expect(src).not.toContain('refusedCode')
    // The receipt drawn is the record's row for it once the record holds it.
    expect(src).toContain('(k ? d.value.responses?.find((r) => r.k === k) : undefined) ?? receipt.value')
  })

  it('holds back only the approval of a counted approver while places are left, and offers it once every place is filled', () => {
    expect(src).toContain("const approvalHeld = computed(() => standing.value === 'counted' && !full.value)")
    expect(src).toContain('const choiceHeld = (i: number | undefined) => answerHeld.value || (approvalHeld.value && approves(d.value, i))')
    expect(src).toMatch(/:disabled="busy \|\| locked \|\| heldBack \|\| choiceHeld\(i\)"/)
    expect(src).toContain('if (locked.value || choiceHeld(i)) return')
    expect(src).toMatch(/v-if="standingLine"[^>]*data-decision-standing/)
    expect(countedWords(null, null, 'Reject')).toBe('Your approval is counted. It waits for the other approvals it needs. You may still answer Reject.')
  })

  it('reads the record again after a counted approval, counted already or not needed', () => {
    expect(src).toContain("if (r.receipt?.status === 'counted') emit('read')")
    expect(src).toContain("if (err.code === 'already_counted' || err.code === 'not_needed') emit('read')")
    expect(policyRefusalWords('not_needed', 'dr-7', null)).toBe('Not counted: your approval fills no place dr-7 still needs.')
    expect(policyRefusalWords('already_counted', 'dr-7', null)).toContain('counted already')
  })

  it('draws a receipt in a status it does not know, or one no longer counting, without the done mark; the first offers nothing', () => {
    expect(knownStatus('pending_review')).toBe(false)
    expect(src).toContain(":name=\"unknownReceipt || receiptShown?.status === 'refused' ? 'i-lucide-info' : 'i-lucide-circle-check'\"")
    expect(src).toContain("const answerHeld = computed(() => d.value.state === 'open' && (standing.value === 'unknown' || unknownReceipt.value))")
  })
})
