// How a run's tiles are drawn, kept for each crew (the run page's Members view): their layout, the member drawn large in One large,
// the sizes a person dragged each layout's gaps to, and whether the run's Activity shows beside the tiles.
// One record in the page's storage, `conductor.runTiles`: a choice for each crew the person set one for, the latest first and at most
// RUN_TILES_CREWS of them, and the last choice made anywhere, which a crew opened for the first time takes. Pure: the storage is
// handed in.
import { isRunLayout, RUN_LAYOUTS, SHARES_MAX, type RunLayout, type WallShares } from './wall'

/** The key of the record in the page's storage. */
export const RUN_TILES_KEY = 'conductor.runTiles'
/** How many crews the record keeps a choice for; the one chosen longest ago goes first. */
export const RUN_TILES_CREWS = 30
/** Whether Activity shows for a crew nobody chose for yet, on a device that has no last choice. */
export const ACTIVITY_AT_FIRST = false

/** What the person chose for a crew's tiles. */
export interface RunTilesChoice {
  layout: RunLayout
  activity: boolean
  /** The member One large draws large, by name; none for the first. */
  large?: string
  /** The shares each layout's tracks were dragged to, for the crew. */
  sizes?: Partial<Record<RunLayout, WallShares>>
}

export interface RunTilesRecord {
  /** The last choice made for any crew. */
  last: RunTilesChoice
  /** Each crew's choice, the latest first. */
  crews: Array<{ crew: string } & RunTilesChoice>
}

/** A crew id as the server writes it (a crew's file name). */
export const CREW = /^[a-z0-9][a-z0-9-]{0,63}$/
/** A member's name, as a crew's manifest takes it. */
export const MEMBER = /^[a-z0-9][a-z0-9._-]{0,39}$/

export const FIRST_CHOICE: RunTilesChoice = { layout: 'grid', activity: ACTIVITY_AT_FIRST }

function emptyRecord(): RunTilesRecord {
  return { last: { ...FIRST_CHOICE }, crews: [] }
}

/** A choice as stored, or null for one that does not read. */
function choiceOf(v: unknown): RunTilesChoice | null {
  if (!v || typeof v !== 'object') return null
  const o = v as Record<string, unknown>
  if (typeof o.activity !== 'boolean') return null
  const c: RunTilesChoice = { layout: isRunLayout(o.layout) ? o.layout : FIRST_CHOICE.layout, activity: o.activity }
  if (typeof o.large === 'string' && MEMBER.test(o.large)) c.large = o.large
  const sizes = sizesOf(o.sizes)
  if (sizes) c.sizes = sizes
  return c
}

/** An axis's shares as stored: at most SHARES_MAX positive numbers, else none. */
function sharesOf(v: unknown): number[] | undefined {
  return Array.isArray(v) && v.length >= 2 && v.length <= SHARES_MAX && v.every((x) => typeof x === 'number' && Number.isFinite(x) && x > 0) ? (v as number[]) : undefined
}

/** Each layout's sizes as stored; a layout or an axis that does not read is left out. */
function sizesOf(v: unknown): RunTilesChoice['sizes'] {
  if (!v || typeof v !== 'object') return undefined
  const out: NonNullable<RunTilesChoice['sizes']> = {}
  for (const l of RUN_LAYOUTS) {
    const s = (v as Record<string, unknown>)[l] as Record<string, unknown> | undefined
    if (!s || typeof s !== 'object') continue
    const cols = sharesOf(s.cols)
    const rows = sharesOf(s.rows)
    if (cols || rows) out[l] = { ...(cols ? { cols } : {}), ...(rows ? { rows } : {}) }
  }
  return Object.keys(out).length ? out : undefined
}

/** The choice alone, without the crew it is kept for. */
function onlyChoice(c: RunTilesChoice): RunTilesChoice {
  return { layout: c.layout, activity: c.activity, ...(c.large ? { large: c.large } : {}), ...(c.sizes ? { sizes: c.sizes } : {}) }
}

/** The record from its stored text: one that does not read is an empty record, and an entry that does not read is left out. */
export function parseRunTiles(raw: string | null): RunTilesRecord {
  if (!raw) return emptyRecord()
  let v: unknown
  try {
    v = JSON.parse(raw)
  } catch {
    return emptyRecord()
  }
  if (!v || typeof v !== 'object') return emptyRecord()
  const o = v as Record<string, unknown>
  const out = emptyRecord()
  const last = choiceOf(o.last)
  if (last) out.last = last
  out.crews = crewsOf(o.crews)
  return out
}

/** One axis's shares as the layout document takes them: fractions of their sum, four decimals, each above 0 and at most 1. */
function fractions(shares: readonly number[]): number[] {
  const sum = shares.reduce((a, b) => a + b, 0)
  return shares.map((s) => Math.min(1, Math.max(0.0001, +(s / sum).toFixed(4))))
}

/**
 * The crews' choices as the layout document keeps them (R73): the shares of each crew's own layout alone, made fractions, so thirty
 * crews of twelve members stay inside the document's bound. The other layouts' sizes stay in this page's storage. Read through
 * crewsOf first, so the document never carries what the server refuses: a crew past RUN_TILES_CREWS, a crew id or a member's name
 * out of its pattern, an axis of fewer than 2 or more than SHARES_MAX shares.
 */
export function crewsForDoc(crews: RunTilesRecord['crews']): RunTilesRecord['crews'] {
  return crewsOf(crews).map((c) => {
    const { sizes: all, ...rest } = c
    const s: WallShares | undefined = all?.[c.layout]
    if (!s || (!s.cols && !s.rows)) return rest
    return { ...rest, sizes: { [c.layout]: { ...(s.cols ? { cols: fractions(s.cols) } : {}), ...(s.rows ? { rows: fractions(s.rows) } : {}) } } }
  })
}

/** Each crew's choice as stored or as the layout document holds it: an entry that does not read, or a crew named twice, left out. */
export function crewsOf(v: unknown): RunTilesRecord['crews'] {
  const out: RunTilesRecord['crews'] = []
  if (!Array.isArray(v)) return out
  const seen = new Set<string>()
  for (const e of v) {
    const c = choiceOf(e)
    const crew = (e as { crew?: unknown } | null)?.crew
    if (!c || typeof crew !== 'string' || !CREW.test(crew) || seen.has(crew)) continue
    seen.add(crew)
    out.push({ crew, ...c })
    if (out.length === RUN_TILES_CREWS) break
  }
  return out
}

/** The choice for a crew: its own, else the last one made anywhere. */
export function choiceFor(record: RunTilesRecord, crew: string): RunTilesChoice {
  const own = record.crews.find((e) => e.crew === crew)
  return onlyChoice(own ?? record.last)
}

/** The record after the person changed the crew's choice: the crew moves to the front, and the oldest past RUN_TILES_CREWS goes. */
export function choose(record: RunTilesRecord, crew: string, change: Partial<RunTilesChoice>): RunTilesRecord {
  // A member drawn large is a member's name; one that is not changes nothing.
  const { large, ...rest } = change
  const next = onlyChoice({ ...choiceFor(record, crew), ...rest, ...(large !== undefined && MEMBER.test(large) ? { large } : {}) })
  if (!CREW.test(crew)) return { ...record, last: next }
  const crews = [{ crew, ...next }, ...record.crews.filter((e) => e.crew !== crew)].slice(0, RUN_TILES_CREWS)
  return { last: next, crews }
}

/** The change that sets one axis of one layout's tracks to `shares` (a drag of a gap), the layout's other axis kept. */
export function withShares(choice: RunTilesChoice, layout: RunLayout, axis: 'cols' | 'rows', shares: number[]): Partial<RunTilesChoice> {
  // An axis of more tracks than a size is kept for has no gap to drag (RunWall); a call with one changes nothing.
  if (shares.length < 2 || shares.length > SHARES_MAX) return {}
  return { sizes: { ...choice.sizes, [layout]: { ...choice.sizes?.[layout], [axis]: shares.map((s) => +s.toFixed(4)) } } }
}

/** `c` as the document holds it, with the sizes of its other layouts from the page's own entry for the crew. */
function keepOtherSizes(c: RunTilesRecord['crews'][number], mine: RunTilesRecord['crews'][number] | undefined): RunTilesRecord['crews'][number] {
  const others = Object.fromEntries(Object.entries(mine?.sizes ?? {}).filter(([l]) => l !== c.layout)) as NonNullable<RunTilesChoice['sizes']>
  const sizes = { ...others, ...c.sizes }
  return Object.keys(sizes).length ? { ...c, sizes } : c
}

/**
 * The record with the server's copy taken up (the layout document's `runTiles`): the last choice (R88) and, when the copy holds
 * them, each crew's own (R73), so what the page's storage lost at a restart of the server comes back, and a crew's choice made on
 * another machine arrives. A layout this page does not draw is left as it was; the same record comes back when nothing changed.
 */
export function takeKept(record: RunTilesRecord, kept: { layout?: string; activity?: boolean; crews?: unknown } | undefined): RunTilesRecord {
  if (!kept) return record
  const last = { ...record.last }
  if (isRunLayout(kept.layout)) last.layout = kept.layout
  if (typeof kept.activity === 'boolean') last.activity = kept.activity
  // A crew's sizes for its other layouts, which the document does not hold, are kept from this page's record.
  const crews = kept.crews === undefined ? record.crews : crewsOf(kept.crews).map((c) => keepOtherSizes(c, record.crews.find((m) => m.crew === c.crew)))
  const same = last.layout === record.last.layout && last.activity === record.last.activity && JSON.stringify(crews) === JSON.stringify(record.crews)
  return same ? record : { last, crews }
}

/** The member One large draws large among `names` (the run's order): the one chosen while it is still a member, else the first. */
export function largeOf(choice: RunTilesChoice, names: readonly string[]): string {
  return choice.large && names.includes(choice.large) ? choice.large : (names[0] ?? '')
}

/** The crew a run belongs to: its crew id, else the run id without its 8 hex digits (`<crew id>-<8 hex>`). */
export function crewOfRun(runId: string, crewId?: string): string {
  if (crewId) return crewId
  const m = /^(.+)-[0-9a-f]{8}$/.exec(runId)
  return m ? m[1]! : runId
}

export function readRunTiles(storage: Pick<Storage, 'getItem'> | null): RunTilesRecord {
  try {
    return parseRunTiles(storage?.getItem(RUN_TILES_KEY) ?? null)
  } catch {
    return emptyRecord()
  }
}

export function writeRunTiles(storage: Pick<Storage, 'setItem'> | null, record: RunTilesRecord): void {
  try {
    storage?.setItem(RUN_TILES_KEY, JSON.stringify(record))
  } catch {
    // Storage refused: the choice lasts the page.
  }
}
