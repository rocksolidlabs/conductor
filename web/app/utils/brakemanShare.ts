import type { CrewPerson, RunInfo } from '~/composables/useSessions'
import type { BrakemanDigest, BrakemanRequest, BriefItem } from './brakeman'
import { citedRuns, parseRef } from './brakeman'
import { responsibilityWords } from './crews'

// Sharing a brief with a crew's people (issue #1012): the person's own sending, addressed to them. Share names the people of the
// crews whose runs the brief is about, as those runs were launched, and gives the brief as Markdown with their names on its first
// line, to copy or to comment on the run's ticket through today's comment dialog, under the person's name. Conductor sends nothing,
// keeps no record of a share and makes no link to a brief: nobody new reads a brief here. The only read is the runs the page does
// not hold yet (at most SHARE_RUNS_MAX, GET /api/runs/{run} with the admin's token).

/** The fixed sentence the dialog draws: who reads what, and what Conductor does (nothing). */
export const SHARE_WHAT =
  "Conductor sends nothing and notifies no one: you copy the brief, or comment it on a ticket, with their names on its first line. No one gains a way to read the brief here. A ticket's comment is read by whoever reads the ticket, and by the agents of a run launched from it later."

/** The dialog's words for a brief about no run whose crew lists people. */
export const SHARE_NONE = "This brief names no run with a crew's people: it goes with no names."

/** The dialog's words over the addressed text it draws when the clipboard was refused. */
export const SHARE_UNCOPIED = 'The page could not copy: select the text below and copy it by hand.'

/** The dialog's words for a crew whose run was launched from no ticket: the comment's dialog then asks which ticket. */
export const shareNoTicket = (crew: string) => `${crew}'s run was launched from no ticket: pick the ticket to comment on.`

/** The most runs a share reads, and the most people a crew names (the crew's own bound). */
export const SHARE_RUNS_MAX = 3
export const SHARE_PEOPLE_MAX = 12
/** A person's name and a crew's name are at most 60 characters (the crew's own bounds). */
export const SHARE_NAME_MAX = 60

/** A crew a share is addressed to: its name, the run it was read from, that run's ticket's key, and its people as launched. */
export interface ShareCrew {
  crewId: string
  name: string
  run: string
  ticket?: string
  people: Array<Pick<CrewPerson, 'name' | 'responsibilities'>>
}

/**
 * The runs a brief is about, each once, first named first, at most SHARE_RUNS_MAX: an asked request's subjects' runs; a background
 * (wake) brief's runs as its digest names them, else as its items cite them.
 */
export function shareRuns(r: Pick<BrakemanRequest, 'kind' | 'subjects'>, digest: Pick<BrakemanDigest, 'about'> | undefined, items: readonly BriefItem[]): string[] {
  const named = r.kind === 'wake' ? (digest?.about.runs ?? citedRuns(items)) : (r.subjects ?? []).map((s) => parseRef(s)?.run ?? '')
  const out: string[] = []
  for (const id of named) if (id && !out.includes(id)) out.push(id)
  return out.slice(0, SHARE_RUNS_MAX)
}

/** Characters as one line holds them, cut to `max` code points. */
function bounded(s: string, max: number): string {
  return Array.from(s).slice(0, max).join('')
}

/** The crews of these runs, each once by its id (the first run named gives its people and ticket), crews without people left out. */
export function shareCrews(runs: ReadonlyArray<Pick<RunInfo, 'id' | 'crewId' | 'name' | 'people' | 'ticket'> | null | undefined>): ShareCrew[] {
  const out: ShareCrew[] = []
  for (const run of runs) {
    if (!run || !run.people?.length || out.some((c) => c.crewId === run.crewId)) continue
    out.push({
      crewId: run.crewId,
      name: bounded(run.name, SHARE_NAME_MAX),
      run: run.id,
      ticket: run.ticket?.key || undefined,
      people: run.people.slice(0, SHARE_PEOPLE_MAX).map((p) => ({ name: bounded(p.name, SHARE_NAME_MAX), responsibilities: p.responsibilities })),
    })
  }
  return out
}

/** Every character a reader may take as a line's end (as the server's quote writer splits lines). */
const BREAKS = /[\n\r\t\v\f\u0085\u2028\u2029]/gu
/**
 * Every character that draws nothing of its own (as the server's quote writer judges them): a control, a format character such as
 * those that reorder a line, a surrogate alone, a private-use or unassigned code point, a default-ignorable code point or a
 * variation selector. A name keeps those it is written with (KEPT).
 */
const HIDDEN = /[\p{Cc}\p{Cf}\p{Cs}\p{Co}\p{Cn}\p{Zl}\p{Zp}\p{Default_Ignorable_Code_Point}\p{Variation_Selector}]/gu
/** The hidden characters a name keeps: the zero-width non-joiner and joiner, and the variation selectors, which shape its letters. */
const KEPT = /^[\u200c\u200d\p{Variation_Selector}]$/u
/** Every ASCII punctuation character: CommonMark lets each be escaped, and an escaped one is text. */
const PUNCTUATION = /[!-/:-@[-`{-~]/g
/**
 * The shapes a tracker links in rendered text, whatever escapes the Markdown had: a mention or an e-mail address (`@`), an issue
 * reference (`#`, `GH-<digits>`, `<owner>/<repo>#`), an address by its scheme or by `www.`.
 */
const LINKED = /[@#]|\bgh-\d|[a-z][a-z0-9+.-]*:|www\./i

/** A name on one line: each line end a space, each character that draws nothing U+FFFD except those a name keeps (KEPT). */
function oneLine(name: string): string {
  return name.replace(BREAKS, ' ').replace(HIDDEN, (c) => (KEPT.test(c) ? c : '\ufffd'))
}

/** Text as a Markdown code span: its fence one backtick longer than its longest run of them, padded where it begins or ends with one. */
function codeSpan(text: string): string {
  const longest = Math.max(0, ...(text.match(/`+/g) ?? []).map((r) => r.length))
  const fence = '`'.repeat(longest + 1)
  const pad = /^[` ]|[` ]$/.test(text) ? ' ' : ''
  return `${fence}${pad}${text}${pad}${fence}`
}

/**
 * A name inside Markdown, on one line (oneLine), never markup, a link or a mention. A name with the shape a tracker links in rendered
 * text (LINKED: `@`, `#`, an issue reference, an address) is written as a code span, inside which a renderer and a tracker link nothing;
 * any other is written as text, a backslash before every ASCII punctuation character, so it is no markup and no address a reader links
 * by itself.
 */
export function mdPlain(name: string): string {
  const line = oneLine(name)
  if (LINKED.test(line)) return codeSpan(line)
  return line.replace(PUNCTUATION, (c) => `\\${c}`)
}

/** The first line of a share: "For <crew>'s people: <name> (<responsibility words>), …", every name plain; "" for a crew of none. */
export function forLine(crew: Pick<ShareCrew, 'name' | 'people'> | undefined): string {
  if (!crew?.people.length) return ''
  const who = crew.people.map((p) => {
    const words = responsibilityWords(p.responsibilities)
    return words ? `${mdPlain(p.name)} (${words})` : mdPlain(p.name)
  })
  return `For ${mdPlain(crew.name)}'s people: ${who.join(', ')}`
}

/** The tickets a share's comment offers: the crew's run's own first, then those the brief names, each once. */
export function shareTickets(crew: Pick<ShareCrew, 'ticket'> | undefined, named: readonly string[]): string[] {
  const out: string[] = []
  for (const k of [crew?.ticket ?? '', ...named]) if (k && !out.includes(k)) out.push(k)
  return out
}
