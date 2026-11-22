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
    expect(src).toMatch(/:disabled="busy \|\| locked \|\| heldBack \|\| answerHeld"/)
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

// Several approvals (SW1 PR-9): an approval of the operator's own counted (an identity's operator the policy names), or one the server
// says is counted already, holds the answer: the choices disabled with the count's words, the text, the rationale and the confirm step
// not drawn, the choice as picked kept; a receipt in a status the page does not know is drawn without the done mark and holds it too.
describe("the operator's own approval under several approvals", () => {
  it('holds the answer once counted or counted already, with what it still waits for, and keeps the choice as picked', () => {
    expect(countedWords(null, null)).toBe('Your approval is counted. It waits for the other approvals it needs.')
    expect(src).toContain("const counted = computed(() => d.value.state === 'open' && (receiptStatus.value === 'counted' || refusedCode.value === 'already_counted'))")
    expect(src).toMatch(/v-if="answerHeld"[^>]*data-decision-counted/)
    expect(src).toMatch(/<div v-if="pending && !answerHeld"[^>]*data-decision-confirm-step/)
    expect(src).toMatch(/<UTextarea v-if="!heldBack && !answerHeld" v-model="rationale"/)
    // The refusal path keeps pending and picked: only a kept answer clears them.
    expect(src).toMatch(/catch \(e\) \{\n    refusal\.value = refusalWords\(e\)\n    receipt\.value = null/)
    expect(src).toContain("if (locked.value || answerHeld.value) return")
  })

  it('reads the count again after a counted approval, and judges a receipt by the record as it stands now', () => {
    expect(src).toContain("if (r.receipt?.status === 'counted') emit('read')")
    expect(src).toContain('(k ? d.value.responses?.find((r) => r.k === k)?.status : undefined) ?? receipt.value?.status')
  })

  it('says not needed and counted already in their own words, before the server\'s', () => {
    expect(policyRefusalWords('not_needed', 'dr-7', null)).toBe('Not counted: your approval fills no place dr-7 still needs.')
    expect(policyRefusalWords('already_counted', 'dr-7', null)).toContain('counted already')
  })

  it('draws a receipt in a status it does not know without the done mark, and offers nothing', () => {
    expect(knownStatus('pending_review')).toBe(false)
    expect(src).toContain(":name=\"unknownReceipt ? 'i-lucide-info' : 'i-lucide-circle-check'\"")
    expect(src).toContain("const answerHeld = computed(() => counted.value || (d.value.state === 'open' && unknownReceipt.value))")
  })
})
