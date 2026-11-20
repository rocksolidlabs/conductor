import type { Decision, ResponseReceipt } from '~/utils/decisions'

/**
 * A decision router's policy on a decision record, in words (SW1 PR-8, docs/plans/decision-policy.md, "web/app"): who may answer it,
 * a group nobody fills, whether the policy is in force now, and an override, from the record alone (and the operator's read's
 * `policyNow`). A record under the server's own policy (local/1) has none of these: its card keeps the words it had.
 */

/** A person a router's policy names, and how (`via`: assigned by name, the role's holder, a delegate beside someone, a backup). */
export interface PolicyEligible {
  subject: string
  name: string
  via: 'assigned' | 'role' | 'delegate' | 'backup' | string
  for?: string
  ref: string
}

/** One group of a policy: a role, its label for people, its quorum, the rule that made it, its eligible persons, or its gap's words. */
export interface PolicyGroup {
  role: string
  label: string
  quorum: number
  rule: string
  eligible?: PolicyEligible[]
  gap?: string
}

/** A router's policy as the record keeps it (`policy` with `approver: "router"`). */
export interface RoutedPolicy {
  revision: string
  approver: 'router'
  router: string
  scope: string
  groups: PolicyGroup[]
  override: 'operator' | 'none' | string
  unsupported?: boolean
  routedAt?: string
}

/**
 * Whether the router stands behind its policy now, as the operator's read says (`policyNow`); `eligible` when the reader, an identity's
 * operator, is among those it names and confirms now: it answers as eligible, never held back.
 */
export interface PolicyNow {
  inForce: boolean
  why?: string
  eligible?: boolean
}

/** The router's policy a record holds, or null under the server's own. */
export function routedPolicy(d: Pick<Decision, 'policy'> | null | undefined): RoutedPolicy | null {
  const p = d?.policy as unknown as Partial<RoutedPolicy> | undefined
  if (!p || p.approver !== 'router' || !Array.isArray(p.groups)) return null
  return p as RoutedPolicy
}

/** A person's name with how they are eligible, when that says more than their name: "Ben (delegate for Ana)", "Cam (backup)". */
export function eligibleWords(e: PolicyEligible, group: PolicyGroup): string {
  switch (e.via) {
    case 'delegate': {
      const forName = group.eligible?.find((o) => o.subject === e.for)?.name ?? e.for ?? ''
      return forName ? `${e.name} (delegate for ${forName})` : `${e.name} (delegate)`
    }
    case 'backup':
      return `${e.name} (backup)`
    default:
      return e.name
  }
}

/** One line per group of who may answer: "For the architecture owner: Ana, Ben (delegate for Ana)", or the gap's own words. */
export function whoMayAnswer(p: RoutedPolicy): { role: string; words: string; rule: string; gap: boolean }[] {
  return p.groups.map((g) => {
    const names = (g.eligible ?? []).map((e) => eligibleWords(e, g))
    if (!names.length) return { role: g.role, words: g.gap || `Waiting for the ${g.label}: none is assigned`, rule: g.rule, gap: true }
    return { role: g.role, words: `For the ${g.label}: ${names.join(', ')}`, rule: g.rule, gap: false }
  })
}

/** The names of those the policy names, joined for a sentence ("Ana", "Ana or Ben"); '' when no group names anyone. */
export function answerers(p: RoutedPolicy): string {
  const names = [...new Set(p.groups.flatMap((g) => (g.eligible ?? []).map((e) => e.name)))]
  if (names.length <= 1) return names[0] ?? ''
  return `${names.slice(0, -1).join(', ')} or ${names.at(-1)}`
}

/**
 * The line of a routed record whose policy is not in force now: "Routed to Ana: not in force now (the roster is moving). The operator
 * answers." '' while it is in force, or when the read did not say.
 */
export function notInForceWords(p: RoutedPolicy, now: PolicyNow | null | undefined): string {
  if (!now || now.inForce) return ''
  const who = answerers(p) || `the ${p.groups[0]?.label ?? 'people its router names'}`
  const why = now.why ? ` (${now.why})` : ''
  return `Routed to ${who}: not in force now${why}. The operator answers.`
}

/** The operator's words beside answer buttons held back by a policy in force: "This is Ana's to answer". */
export function heldBackWords(p: RoutedPolicy): string {
  const who = answerers(p)
  if (who) return `This is ${who}'s to answer`
  return `This is for the ${p.groups[0]?.label ?? 'people its router names'}: nobody is assigned yet`
}

/**
 * Whether the operator answers only by an override now: a router's policy the operator's read says is in force, and the reader not
 * among those it names (an identity's operator the read says is eligible answers as eligible).
 */
export function operatorHeldBack(d: Pick<Decision, 'policy' | 'state'>, now: PolicyNow | null | undefined): boolean {
  return d.state === 'open' && !!routedPolicy(d) && !!now?.inForce && !now.eligible
}

/** Whether the policy lets the operator answer in its place. */
export function overrideAllowed(p: RoutedPolicy | null): boolean {
  return p?.override === 'operator'
}

/** The least an override's reason holds, in characters once trimmed (the server's minOverrideReason). */
export const MIN_OVERRIDE_REASON = 10

/** Whether reason may go as an override's: at least MIN_OVERRIDE_REASON characters once trimmed. */
export function overrideReasonReady(reason: string): boolean {
  return [...reason.trim()].length >= MIN_OVERRIDE_REASON
}

/** An accepted override's line, from its receipt: "Overridden by the operator: Ana is away this week". '' for any other receipt. */
export function overrideLine(r: Pick<ResponseReceipt, 'rationale' | 'byName' | 'actor' | 'status'> & { override?: boolean }): string {
  if (!r.override || r.status !== 'accepted') return ''
  const who = r.byName || 'the operator'
  return r.rationale ? `Overridden by ${who}: ${r.rationale}` : `Overridden by ${who}`
}

/** The words of the policy's refusals, said to the operator (the server's own words are said beside them). */
export function policyRefusalWords(code: string, did: string, p: RoutedPolicy | null): string {
  switch (code) {
    case 'not_eligible':
      if (!p) return `${did} is not yours to answer.`
      return overrideAllowed(p) ? `${heldBackWords(p)}: answer only by an override, with its reason.` : `${heldBackWords(p)}: its policy allows no override.`
    case 'override_refused':
      return `${did}'s policy allows no override, or its reason is too short.`
    case 'routed_by_policy':
      return `${did} is routed by its router's policy: ask the router again, or override at the answer where the policy allows.`
  }
  return ''
}
