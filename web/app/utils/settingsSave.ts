import type { DesktopSettings } from './desktop'

/**
 * The Settings page's one Save (#707): Brakeman's configuration, the server's own (PUT /api/brakeman/config), and the desktop app's
 * settings (the bridge), whichever of the two was changed. Both are taken at the click (the page's snapshot of each: what is typed
 * while the save is out is kept for the next Save, never sent by this one). Brakeman's part goes first, against the server as it is:
 * the app's part may restart the server, and the app then loads the page on the new server's port, so a part left for after it would
 * never be sent. A part refused stops the save there: the app's part is not sent after Brakeman's was refused, so no restart takes
 * edits that were not saved, and every edit stays in its form for the next Save. A Save whose app part would leave the store
 * Brakeman's part is written to, or change what that part is checked against, is refused whole before anything is sent (`together`).
 */
export interface SaveOutcome {
  /** `held`: not sent, since the Save was refused before anything was sent. */
  brakeman: 'unchanged' | 'saved' | 'failed' | 'held'
  /**
   * `held`: not sent, since Brakeman's part before it was refused. `refused`: the Save was refused before anything was sent.
   * `unrestarted`: the app kept the settings, and the server did not restart with them. `unknown`: the app refused, and whether it
   * kept the settings could not be read.
   */
  app: 'unchanged' | 'saved' | 'restarted' | 'failed' | 'held' | 'refused' | 'unrestarted' | 'unknown'
  /** The refusal's words, when a part failed or the Save was refused. */
  error?: string
}

/** The app's part: sends the settings taken at the click; asked after a refusal, whether the app kept them all the same. */
export interface AppPart {
  send: () => Promise<'saved' | 'restarted'>
  /** Whether the settings the app holds now are the ones sent (its restart failed after it kept them); throws when unread. */
  kept: () => Promise<boolean>
}

/** Brakeman's part, as its section took it at the click (`take`): whether its folder changes, and the save of what was taken. */
export interface BrakemanPart {
  cwdChanged: boolean
  send: () => Promise<void>
}

/** Runs the parts given (null for one unchanged, which is never sent: a Brakeman save drops its pending work, #721) in order. */
export async function saveParts(parts: { brakeman: (() => Promise<void>) | null; app: AppPart | null }): Promise<SaveOutcome> {
  const out: SaveOutcome = { brakeman: parts.brakeman ? 'saved' : 'unchanged', app: parts.app ? 'held' : 'unchanged' }
  if (parts.brakeman) {
    try {
      await parts.brakeman()
    } catch (e) {
      return { ...out, brakeman: 'failed', error: (e as Error).message }
    }
  }
  if (parts.app) {
    try {
      out.app = await parts.app.send()
    } catch (e) {
      // The app keeps the settings before it restarts the server (desktop/src/save.ts): a failed restart leaves them saved.
      const kept = await parts.app.kept().catch(() => undefined)
      return { ...out, app: kept === true ? 'unrestarted' : kept === false ? 'failed' : 'unknown', error: (e as Error).message }
    }
  }
  return out
}

/** The settings the page's form edits: what `AppPart.kept` compares (the app's own, its zoom and its notices, are not the page's). */
export const EDITED_KEYS: ReadonlyArray<keyof DesktopSettings> = ['dataDir', 'allowedRoots', 'defaultCwd', 'yolo', 'reach', 'closeToTray', 'wslDistro', 'wslWindowsHome', 'switchyardEnabled', 'switchyardServer', 'switchyardToken', 'switchyardName']

/** Whether the app holds the settings sent, field by field of those the page edits. */
export function holds(now: DesktopSettings, sent: DesktopSettings): boolean {
  return EDITED_KEYS.every((k) => JSON.stringify(now[k]) === JSON.stringify(sent[k]))
}

/** The settings that move the server to another store (Brakeman's configuration is in its data directory), with their names. */
const STORE_KEYS: Array<[keyof DesktopSettings, string]> = [
  ['dataDir', 'the data directory'],
  ['wslDistro', 'the WSL distribution'],
]

/**
 * Why the Save cannot send these two parts together, or '' when it can: the app's part moves the server to another data directory
 * (the new server would read that directory's Brakeman configuration, not the one this Save writes), or changes the allowed roots
 * while Brakeman's folder changes (the folder is checked against the roots the server has now). Nothing is sent: each goes in a Save
 * of its own, the app's change first.
 */
export function together(before: DesktopSettings, next: DesktopSettings, brakeman: Pick<BrakemanPart, 'cwdChanged'> | null): string {
  if (!brakeman) return ''
  const changed = (k: keyof DesktopSettings) => JSON.stringify(before[k]) !== JSON.stringify(next[k])
  const moved = STORE_KEYS.find(([k]) => changed(k))
  if (moved) {
    return `Brakeman's settings are kept in the server's data directory, and this Save moves the server off it: save ${moved[1]} alone first, then Brakeman's settings`
  }
  if (brakeman.cwdChanged && changed('allowedRoots')) {
    return "Brakeman's folder is checked against the allowed roots the server has now, and this Save changes them: save the allowed roots alone first, then Brakeman's folder"
  }
  // The Windows profile under /mnt is a root of its own on WSL (the launcher adds it), though it is not in the list (#707's review).
  if (brakeman.cwdChanged && changed('wslWindowsHome')) {
    return "Brakeman's folder is checked against the roots the server allows now, and this Save adds or takes away your Windows profile under /mnt among them: save that switch alone first, then Brakeman's folder"
  }
  return ''
}

/*
 * What answers the person's edits: the page's one rule, for both parts of its form (#707's review). What the person typed after the
 * click is theirs: a save's answer, a re-read the configuration's watcher makes and a reply that arrives late never write over it.
 * Each path that answers a form goes through this rule:
 *
 * - a save's answer (`followSaved`): the fields still holding what was sent follow what was saved; a field changed since the click
 *   keeps its edit, for the next Save, even when it was changed back to what it was before;
 * - a save the app answers by moving the page (`holdsFields`): the app may restart the server and load this page on the new one's
 *   port, another origin, where nothing typed meanwhile could follow, so the page's fields are held from the click until it answers;
 * - the configuration saved moving under the form (`formMove`): a form of this token's follows it until it is changed here, and
 *   never while its own save is out, whose answer decides;
 * - a reply that arrives after one asked later (`ReplyOrder`): it is dropped, so the store never goes back to an older read.
 */

/**
 * The form after a save of `sent` answered `now`: a field whose value `current` (the form as it is, by default the form itself)
 * still holds what was sent (or, before anything was read, every field) follows what is saved now, a copy of it; a field changed
 * while the save was out keeps its edit, for the next Save. Returns the fields that followed.
 */
export function followSaved<T extends object>(form: T, sent: T | null, now: T, current: T = form): Array<keyof T> {
  const followed: Array<keyof T> = []
  const keys = new Set([...Object.keys(now), ...(sent ? Object.keys(sent) : [])]) as Set<keyof T>
  for (const k of keys) {
    if (sent && stable(current[k]) !== stable(sent[k])) continue
    form[k] = (now[k] === undefined ? undefined : JSON.parse(JSON.stringify(now[k]))) as T[keyof T]
    followed.push(k)
  }
  return followed
}

/** Whether a save holds the page's fields until it is answered: one that sends the app's settings, which the app may answer by moving the page. */
export function holdsFields(app: DesktopSettings | null): boolean {
  return app !== null
}

/**
 * What Settings' Brakeman form does when the token or the configuration saved moves: `set` it from the configuration saved, `drop`
 * it, or `keep` it. A form set under another token (the store's generation, which a reset moves) is never kept, edited or not: the
 * server may be another one, whose revision 2 is not the revision the form was read at, so its settings would be saved over
 * another's. A form of this token's follows the configuration saved until it is changed here, and is kept while its own save is out
 * (`saving`): the store takes that save's answer before the form does, and a form changed back meanwhile to what it was set from
 * would otherwise be set from it, its edit lost (#707's review); the save's answer follows it (`followSaved`).
 */
export function formMove(form: { gen: number; edited: boolean; saving?: boolean } | null, gen: number, saved: boolean): 'set' | 'drop' | 'keep' {
  if (form && form.gen !== gen) return saved ? 'set' : 'drop'
  if (!saved || form?.saving) return 'keep'
  return !form || !form.edited ? 'set' : 'keep'
}

/**
 * The order of the replies to one kind of read (Brakeman's state, its configuration among it): each request takes a number as it is
 * asked (`ask`), and its reply is taken (`take` true) only when no request asked after it has had its reply taken. A reply that
 * arrives after a later one's is older than what is held, and is dropped.
 */
export class ReplyOrder {
  private asked = 0
  private taken = 0
  ask(): number {
    return ++this.asked
  }
  take(n: number): boolean {
    if (n < this.taken) return false
    this.taken = n
    return true
  }
}

/** JSON with every object's keys in order, so two values are compared by what they hold. */
function stable(v: unknown): string | undefined {
  if (Array.isArray(v)) return `[${v.map((x) => stable(x) ?? 'null').join(',')}]`
  if (v && typeof v === 'object') {
    return `{${Object.entries(v as Record<string, unknown>)
      .filter(([, x]) => x !== undefined)
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
      .map(([k, x]) => `${JSON.stringify(k)}:${stable(x)}`)
      .join(',')}}`
  }
  return JSON.stringify(v)
}

/** What the page says of a save: which part was saved and which was not, and that the edits are kept. */
export function saveWords(o: SaveOutcome): { ok: boolean; title: string; description?: string } {
  const why = (o.error ?? '').replace(/\.$/, '')
  if (o.app === 'refused') {
    if (o.brakeman === 'held') return { ok: false, title: 'Not saved', description: `${why}. Nothing was sent, Brakeman's settings with the rest. Your edits are kept.` }
    return { ok: false, title: 'Not saved', description: o.error }
  }
  if (o.brakeman === 'failed') {
    const app = o.app === 'held' ? " The app's settings were held back with them, so nothing changed." : ''
    return { ok: false, title: 'Not saved', description: `Brakeman's settings were not saved: ${why}.${app} Your edits are kept.` }
  }
  const brakeman = o.brakeman === 'saved' ? "Brakeman's settings were saved. " : ''
  if (o.app === 'unrestarted') {
    return { ok: false, title: 'Saved; the server did not restart', description: `${brakeman}The app's settings were saved, but the server did not restart with them: ${why}.` }
  }
  if (o.app === 'unknown') {
    return { ok: false, title: o.brakeman === 'saved' ? 'Saved in part' : 'Not saved', description: `${brakeman}The app refused the settings (${why}), and whether it kept them could not be read. Your edits are kept.` }
  }
  if (o.app === 'failed') {
    if (o.brakeman === 'saved') return { ok: false, title: 'Saved in part', description: `Brakeman's settings were saved. The app's settings were not: ${why}. Your edits are kept.` }
    return { ok: false, title: 'Not saved', description: o.error }
  }
  return { ok: true, title: o.app === 'restarted' ? 'Saved; the server restarted' : 'Saved' }
}
