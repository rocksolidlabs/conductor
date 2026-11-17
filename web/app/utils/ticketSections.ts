import type { PlanSummary, RunSummary } from '~/composables/useDispatch'
import type { CloseCard } from './dispatch'
import { planStateWord, revisionTag } from './dispatchPlans'
import { githubStateWords } from './tracker'

// A ticket's strip of sections (UX1 PR-16, docs/plans/ux-review.md): the sections the strip names, in the page's order, each with its state
// where it has one, from what the page reads already. Pure: DispatchTicketStrip.vue draws it, vitest proves it (ticketSections.test.ts).

/** An item of the one list of what needs a person (useNeedsYou), as far as the strip reads it. */
export interface WaitItem {
  kind: string
  /** `question:<session>/<id>`, `decision:<id>`, `task:<run>/<task>`, `plan:<KEY-n>/<n>`. */
  ref: string
  /** The run it belongs to, when it has one. */
  run?: string
}

/**
 * The items of the one list that are this ticket's: a plan item of its key (`plan:<KEY-n>/<n>`), and a question, a task or a decision
 * whose run is one of the ticket's runs. The list is filtered, never counted again (one list, one number).
 */
export function ticketWaits<T extends WaitItem>(items: readonly T[], ticket: { key: string; runs: readonly string[] }): T[] {
  const runs = new Set(ticket.runs)
  return items.filter((i) => (i.kind === 'plan' ? i.ref.startsWith(`plan:${ticket.key}/`) : !!i.run && runs.has(i.run)))
}

/** A section the strip names: its id on the page (`ticket-<id>`), its name, its state in words ('' for a name alone), its tone. */
export interface TicketSection {
  id: 'github' | 'description' | 'acceptance' | 'plan' | 'runs' | 'decisions' | 'close' | 'people' | 'comments' | 'history'
  label: string
  words: string
  tone: 'neutral' | 'warning'
  /** A number the section holds, where it has one: the runs, the open decisions, the live comments. */
  count?: number
}

export interface TicketSectionsInput {
  /** The ticket's linked GitHub item, as its snapshot says it (IN1a): GitHub's state, apart from the ticket's status; none when unlinked. */
  github?: { state: string; stateReason?: string; merged?: boolean } | null
  /** The revision the Plan section features (the approved one, else the newest proposed), or none. */
  plan: Pick<PlanSummary, 'state' | 'stale'> | null
  /** The ticket's items of the one list (ticketWaits). */
  waits: readonly WaitItem[]
  /** How many runs the ticket has. */
  runs: number
  /** The newest run's done part, in the words Dispatch's run card gives its summary ("2 of 6 done"); '' for none. */
  runDone: string
  /** The decision records the page reads, each with its state. */
  decisions: ReadonlyArray<{ state: string }>
  /** The close card (closeCard): closed, ready, and each requirement met or not. */
  close: Pick<CloseCard, 'closed' | 'ready'> & { items: ReadonlyArray<{ met: boolean }> }
  /** The comments the page holds (the newest page of them), each with its number and whether it is deleted. */
  comments: ReadonlyArray<{ n: number; deletedAt?: string }>
}

/** "1 needs you", "2 need you". */
function needWords(n: number): string {
  return `${n} ${n === 1 ? 'needs' : 'need'} you`
}

/**
 * The sections the strip names, in the page's order: GitHub first, only for a ticket with a linked item (IN1a: its panel is under the
 * title), by GitHub's state in its own words ("open", "closed", "merged"), never the ticket's status nor a status's tone; then the nine:
 * Description and Acceptance by name; Plan by its featured revision's state
 * ("approved", "approval stale", "proposed", "none"), or "r2 waits for your approval" (warning) while the one list holds a plan item of
 * this ticket; Runs by the newest run's done part and the ticket's runs' items that wait ("2 of 6 done · 2 need you", warning while any
 * waits), its count the ticket's runs; Decisions by the open records ("2 open", warning, or "none open"); To close by the close card
 * ("closed", "ready", "1 of 2 met"); People by name; Comments by the live comments' count, "50+" when an earlier page exists (the first
 * comment held is not the ticket's first); History by name. Links, children and relations and attachments are not named.
 */
export function ticketSections(i: TicketSectionsInput): TicketSection[] {
  const plans = i.waits.filter((w) => w.kind === 'plan')
  const runWaits = i.waits.filter((w) => w.kind !== 'plan')
  const plan: TicketSection = plans.length
    ? { id: 'plan', label: 'Plan', words: `${revisionTag(Number(plans[0]!.ref.slice(plans[0]!.ref.lastIndexOf('/') + 1)))} waits for your approval`, tone: 'warning' }
    : { id: 'plan', label: 'Plan', words: i.plan ? planStateWord(i.plan).label : 'none', tone: 'neutral' }
  const runWords = [i.runs ? i.runDone : 'none', runWaits.length ? needWords(runWaits.length) : ''].filter(Boolean).join(' · ')
  const open = i.decisions.filter((d) => d.state === 'open').length
  const met = i.close.items.filter((x) => x.met).length
  const closeWords = i.close.closed ? 'closed' : i.close.ready ? 'ready' : `${met} of ${i.close.items.length} met`
  const live = i.comments.filter((c) => !c.deletedAt).length
  const earlier = (i.comments[0]?.n ?? 1) > 1
  const github: TicketSection[] = i.github ? [{ id: 'github', label: 'GitHub', words: githubStateWords(i.github).key, tone: 'neutral' }] : []
  return [
    ...github,
    { id: 'description', label: 'Description', words: '', tone: 'neutral' },
    { id: 'acceptance', label: 'Acceptance', words: '', tone: 'neutral' },
    plan,
    { id: 'runs', label: 'Runs', words: runWords, tone: runWaits.length ? 'warning' : 'neutral', count: i.runs },
    { id: 'decisions', label: 'Decisions', words: open ? `${open} open` : 'none open', tone: open ? 'warning' : 'neutral', count: open },
    { id: 'close', label: 'To close', words: closeWords, tone: 'neutral' },
    { id: 'people', label: 'People', words: '', tone: 'neutral' },
    { id: 'comments', label: 'Comments', words: earlier ? `${live}+` : String(live), tone: 'neutral', count: live },
    { id: 'history', label: 'History', words: '', tone: 'neutral' },
  ]
}
