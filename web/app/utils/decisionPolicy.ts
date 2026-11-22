import type { Decision, ResponseReceipt } from '~/utils/decisions'
import { optionsOf } from '~/utils/decisions'
import type { DecideReceipt, DecisionQuorum } from '~/utils/protocol'

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
  /** One person may fill only one group (SW1 PR-9). */
  distinct?: boolean
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

/** Whether the operator may ask the decision router again for d: a router is registered, d is open and no comparison's selection. */
export function canAskRouter(d: Pick<Decision, 'state' | 'origin'>, router: boolean): boolean {
  return router && d.state === 'open' && d.origin !== 'compare'
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
    case 'not_needed':
      return `Not counted: your approval fills no place ${did} still needs.`
    case 'already_counted':
      return `Your approval of ${did} is counted already: nothing more is asked of you.`
  }
  return ''
}

/*
 * Several approvals (SW1 PR-9, docs/plans/decision-policy.md, "Groups, quorum and distinct persons"): a policy may ask for up to four
 * groups, each a quorum of up to four. The operator's read says how far a record is (`quorum`, DecisionQuorum): the places in all, how
 * many the counted approvals fill now, and each group's count. A record of one group of quorum 1 is said exactly as before: no count.
 */

/** A whole number of at least 0, or 0: the count a newer or broken reply might give otherwise is never drawn. */
const count = (n: unknown) => (typeof n === 'number' && Number.isInteger(n) && n > 0 ? n : 0)

/** Whether a record's count is worth saying: a policy that asks for more than one approval (places above one). */
export function severalApprovals(q: DecisionQuorum | null | undefined): q is DecisionQuorum {
  return !!q && count(q.places) > 1 && Array.isArray(q.groups)
}

/** A group's label for people, from the policy by its role; the role itself when the policy does not name it. */
function labelOf(p: RoutedPolicy, role: string): string {
  return p.groups.find((g) => g.role === role)?.label || role
}

/** One group's count in words: "1 of 2 for the architecture owner", and ": met" once its quorum is reached. */
export interface QuorumLine {
  role: string
  words: string
  met: boolean
}

/**
 * How far a record is from its answer, in words: the lead ("1 of 2 approvals", only with more than one group), each group's line ("1 of 2
 * for the architecture owner", "1 of 1 for the security owner: met"), and, with distinct persons and more than one group, the plan's
 * words of it once. Null for a record of one group of quorum 1, or with no count: its card says what it said before.
 */
export function quorumWords(p: RoutedPolicy, q: DecisionQuorum | null | undefined): { lead: string; groups: QuorumLine[]; distinct: string } | null {
  if (!severalApprovals(q)) return null
  const groups = q.groups.map((g) => {
    const quorum = count(g.quorum)
    const counted = Math.min(count(g.counted), quorum)
    const met = g.met === true && counted === quorum && quorum > 0
    return { role: g.role, words: `${counted} of ${quorum} for the ${labelOf(p, g.role)}${met ? ': met' : ''}`, met }
  })
  const places = count(q.places)
  const lead = groups.length > 1 ? `${Math.min(count(q.filled), places)} of ${places} approvals` : ''
  const distinct = p.distinct && groups.length > 1 ? 'One person may fill only one group.' : ''
  return { lead, groups, distinct }
}

/**
 * What the record still waits for, from its count: "It waits for the security owner", "It waits for 2 more approvals for the
 * architecture owner and the security owner"; '' when every group is met or the count does not say.
 */
export function stillNeeded(p: RoutedPolicy, q: DecisionQuorum | null | undefined): string {
  if (!severalApprovals(q)) return ''
  const owed = q.groups.flatMap((g) => {
    const left = count(g.quorum) - Math.min(count(g.counted), count(g.quorum))
    if (left <= 0) return []
    return [left === 1 ? `the ${labelOf(p, g.role)}` : `${left} more approvals for the ${labelOf(p, g.role)}`]
  })
  if (!owed.length) return ''
  const list = owed.length === 1 ? owed[0] : `${owed.slice(0, -1).join(', ')} and ${owed.at(-1)}`
  return `It waits for ${list}.`
}

/** Whether every place of a record's policy is filled now, as its count says (several approvals only). */
export function placesFull(q: DecisionQuorum | null | undefined): boolean {
  return severalApprovals(q) && count(q.filled) >= count(q.places)
}

/** Whether choice i of d is an approval, which fills places (an approval's or an assignment's first option); any other answers at once. */
export function approves(d: Pick<Decision, 'type'>, i: number | undefined): boolean {
  return i === 0 && (d.type === 'approval' || d.type === 'assignment')
}

/**
 * The operator's own approval counted and standing while places are left (an identity's operator the policy names): "Your approval is
 * counted: 1 of 2 approvals. It waits for the security owner. You may still answer Reject." Without a count, the words say only that it
 * waits for the others; without another answer to give, they leave the last sentence out.
 */
export function countedWords(p: RoutedPolicy | null, q: DecisionQuorum | null | undefined, other = ''): string {
  const w = p ? quorumWords(p, q) : null
  const owed = p ? stillNeeded(p, q) : ''
  const where = w?.lead || w?.groups[0]?.words || ''
  return `Your approval is counted${where ? `: ${where}` : ''}. ${owed || 'It waits for the other approvals it needs.'}${other ? ` You may still answer ${other}.` : ''}`
}

/**
 * An open record whose places are all filled (a re-route can leave it so): the next eligible approval answers it, a counted approver's
 * too. Said to whoever may approve it now.
 */
export function fullWords(mineCounted: boolean): string {
  return mineCounted ? 'Your approval is counted, and every approval it needs is in: approving again answers it.' : 'Every approval it needs is in: an approval now answers it.'
}

/** Whether a response's status is one this page knows how to draw. */
export function knownStatus(status: unknown): status is 'accepted' | 'refused' | 'counted' {
  return status === 'accepted' || status === 'refused' || status === 'counted'
}

/**
 * How an answered record under several approvals was answered, as its responses say: an override answers every group at once; one
 * eligible Reject (any option but an approval's or an assignment's first) answers it at once; the counted approvals before either
 * stay in the record. '' for any other record, or one answered by its approvals.
 */
export function answeredHow(d: Pick<Decision, 'state' | 'type' | 'responses' | 'request'>, q: DecisionQuorum | null | undefined): string {
  if (d.state === 'open' || !severalApprovals(q)) return ''
  const rc = d.responses?.find((r) => r.status === 'accepted')
  if (!rc) return ''
  const n = (d.responses ?? []).filter((r) => r.status === 'counted').length
  const kept = n ? ` ${n === 1 ? 'The approval counted before it stays' : `The ${n} approvals counted before it stay`} in the record.` : ''
  if (rc.override) return `The override answered every group at once.${kept}`
  if ((d.type === 'approval' || d.type === 'assignment') && typeof rc.choice === 'number' && rc.choice > 0) {
    const label = optionsOf(d)[rc.choice]?.label || 'rejection'
    const by = rc.byName || rc.actor
    return `${label}${by ? ` by ${by}` : ''}: one eligible ${label} answers it at once.${kept}`
  }
  return ''
}

/** The newest of the responses a person made, by `k`: the one that says where their answer stands. */
function newest<T extends { k?: number }>(rows: readonly T[]): T | undefined {
  return rows.length ? rows.reduce((a, b) => (count(b.k) > count(a.k) ? b : a)) : undefined
}

/**
 * The operator's own newest response on a record, from the read alone (never a send's reply): the responses kept under its actor,
 * `user:<subject>` for an identity's operator. None for the workbench token (no subject), which is never a subject the policy counts.
 */
export function ownResponse(d: Pick<Decision, 'responses'>, subject: string | null | undefined): ResponseReceipt | undefined {
  if (!subject) return undefined
  return newest((d.responses ?? []).filter((r) => r.actor === `user:${subject}`))
}

/**
 * Where a person's own answer stands, from their newest response as the read gives it (a decide principal's `own`, or the operator's own
 * rows of `responses`): `counted` while their approval counts; `lapsed` once a counted approval no longer counts (refused,
 * `not_confirmed`); `unknown` for a status a newer server gives that this page does not know; null otherwise (nothing yet, accepted, or
 * refused for any other reason).
 */
export function ownStanding(own: readonly { k?: number; status: string; code?: string }[] | null | undefined): 'counted' | 'lapsed' | 'unknown' | null {
  const n = newest(own ?? [])
  if (!n) return null
  if (n.status === 'counted') return 'counted'
  if (n.status === 'refused' && n.code === 'not_confirmed') return 'lapsed'
  return knownStatus(n.status) ? null : 'unknown'
}

/**
 * The decide page's words of where the person's own answer stands, naming the decision and no group or person: counted (the decide view
 * carries no count, so whether an approval again answers it is said as a condition); an approval that no longer counts on a decision no
 * longer open (the server's "answer again" is said only while it is open, by its outcome); a status it does not know. '' for none.
 */
export function ownStandingWords(standing: ReturnType<typeof ownStanding>, did: string, open = true): string {
  if (standing === 'counted' && open) return `Your approval of ${did} is counted. You may still choose another answer; approving again answers ${did} only when every other approval it needs is in.`
  if (standing === 'lapsed' && !open) return `Your approval of ${did} no longer counts.`
  if (standing === 'unknown') return `This page cannot show what became of your answer to ${did}: nothing more is asked of you here.`
  return ''
}
