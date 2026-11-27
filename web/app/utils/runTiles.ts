// How a run's tiles are drawn, kept for each crew (the run page's Members view): their layout, the member drawn large in One large,
// and whether the run's Activity shows beside the tiles.
// One record in the page's storage, `conductor.runTiles`: a choice for each crew the person set one for, the latest first and at most
// RUN_TILES_CREWS of them, and the last choice made anywhere, which a crew opened for the first time takes. Pure: the storage is
// handed in.
import { isRunLayout, type RunLayout } from './wall'

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
}

export interface RunTilesRecord {
  /** The last choice made for any crew. */
  last: RunTilesChoice
  /** Each crew's choice, the latest first. */
  crews: Array<{ crew: string } & RunTilesChoice>
}

/** A crew id as the server writes it (a crew's file name). */
const CREW = /^[a-z0-9][a-z0-9-]{0,63}$/
/** A member's name, as a crew's manifest takes it. */
const MEMBER = /^[a-z0-9][a-z0-9._-]{0,39}$/

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
  return c
}

/** The choice alone, without the crew it is kept for. */
function onlyChoice(c: RunTilesChoice): RunTilesChoice {
  return { layout: c.layout, activity: c.activity, ...(c.large ? { large: c.large } : {}) }
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
  if (Array.isArray(o.crews)) {
    const seen = new Set<string>()
    for (const e of o.crews) {
      const c = choiceOf(e)
      const crew = (e as { crew?: unknown } | null)?.crew
      if (!c || typeof crew !== 'string' || !CREW.test(crew) || seen.has(crew)) continue
      seen.add(crew)
      out.crews.push({ crew, ...c })
      if (out.crews.length === RUN_TILES_CREWS) break
    }
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
  const next = onlyChoice({ ...choiceFor(record, crew), ...change })
  if (!CREW.test(crew)) return { ...record, last: next }
  const crews = [{ crew, ...next }, ...record.crews.filter((e) => e.crew !== crew)].slice(0, RUN_TILES_CREWS)
  return { last: next, crews }
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
