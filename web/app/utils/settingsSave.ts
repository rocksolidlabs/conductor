import { computed, ref, type Ref } from 'vue'
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
 * What answers the person's edits: the page's one rule (#707's review). What the person typed after an operation began is theirs:
 * no reply, and no move of the configuration saved under the form, writes over it.
 *
 * - Brakeman's form holds a server revision (its `rev`). A reply to an operation the page started (a save's, a read's, the stale
 *   refusal's Read again and Keep mine) is applied only when the revision it carries is newer than the one the form holds (`newer`,
 *   the one comparison), and only over the fields the person has not edited since that operation began (`answer`, the one guard).
 *   The store takes a configuration by the same comparison: never over a newer one, whatever order the replies came in.
 * - While any operation the page started is in flight, the watcher that mirrors the store into the form is off (`formMove`'s
 *   `held`), and it looks again only once that operation has applied or discarded its result (`configForm`'s `op`).
 * - A save the app answers by moving the page (`holdsFields`): the app may restart the server and load this page on the new one's
 *   port, another origin, where nothing typed meanwhile could follow, so the page's fields are held from the click until it answers.
 *   The app's settings carry no revision: a save's answer follows the fields still holding what was sent (`followSaved`).
 */

/**
 * The form after a save of `sent` answered `now`: a field whose value `current` (the form as it is, by default the form itself)
 * still holds what was sent (or, before anything was read, every field) follows what is saved now, a copy of it; a field changed
 * while the save was out keeps its edit, for the next Save. `keys` limits the fields that may follow. Returns the fields that followed.
 */
export function followSaved<T extends object>(form: T, sent: T | null, now: T, current: T = form, keys?: ReadonlyArray<keyof T>): Array<keyof T> {
  const followed: Array<keyof T> = []
  const all = keys ?? ([...new Set([...Object.keys(now), ...(sent ? Object.keys(sent) : [])])] as Array<keyof T>)
  for (const k of all) {
    if (sent && stable(current[k]) !== stable(sent[k])) continue
    form[k] = (now[k] === undefined ? undefined : JSON.parse(JSON.stringify(now[k]))) as T[keyof T]
    followed.push(k)
  }
  return followed
}

/** The one comparison: whether `a` carries a revision newer than `b`'s (no `b`: none is held). */
export function newer(a: { rev: number } | null | undefined, b: { rev: number } | null | undefined): boolean {
  return !!a && (!b || a.rev > b.rev)
}

/**
 * The one guard: `reply` is applied to `form` only when its revision is newer than the form's (`newer`), and only over the fields
 * that `current` (the form as it is now) holds as the operation took them (`began`); `keys` limits the fields it may set (Keep
 * mine's: the revision alone). Returns the fields set, or null when the reply was discarded.
 */
export function answer<T extends { rev: number }>(form: T, began: T, reply: T, current: T, keys?: ReadonlyArray<keyof T>): Array<keyof T> | null {
  if (!newer(reply, form)) return null
  return followSaved(form, began, reply, current, keys)
}

/** Whether a save holds the page's fields until it is answered: one that sends the app's settings, which the app may answer by moving the page. */
export function holdsFields(app: DesktopSettings | null): boolean {
  return app !== null
}

/**
 * What the watcher does when the token or the configuration saved moves: `set` the form from the configuration saved, `drop` it,
 * or `keep` it. A form set under another token (the store's generation, which a reset moves) is never kept, edited or not: the
 * server may be another one, whose revision 2 is not the revision the form was read at, so its settings would be saved over
 * another's. A form of this token's is kept while it is `held` (an operation the page started is in flight, or a stale refusal
 * waits for its choice: that operation's reply, or that choice, decides), and otherwise follows the configuration saved when it was
 * not changed here and the configuration saved is newer than the form's (`newer`).
 */
export function formMove(form: { gen: number; rev: number; edited: boolean; held?: boolean } | null, gen: number, saved: { rev: number } | null): 'set' | 'drop' | 'keep' {
  if (form && form.gen !== gen) return saved ? 'set' : 'drop'
  if (!saved) return 'keep'
  if (!form) return 'set'
  if (form.held) return 'keep'
  return !form.edited && newer(saved, form) ? 'set' : 'keep'
}

/** What a configuration form needs of its page: the store, the save and read routes, and the form's own text fields. */
export interface ConfigFormDeps<T extends { rev: number }> {
  /** The store's generation now (a reset, a new token, moves it). */
  generation: () => number
  /** The configuration the store holds now, null before it was read. */
  saved: () => T | null
  /** Saves a configuration at its revision: what was saved, or the refusal thrown. */
  save: (c: T) => Promise<{ config: T }>
  /** Reads the configuration saved now, fresh, for a form set under the generation given (throws when the token moved). */
  read: (gen: number) => Promise<T>
  /** Whether a save's refusal says the configuration was saved elsewhere since the form's revision. */
  isStale: (e: unknown) => boolean
  /** The form as the server takes it (its text fields folded in), null without a form. */
  current: (form: T | null) => T | null
  /** Whether two configurations hold the same, their revisions aside. */
  same: (a: T, b: T) => boolean
  /** Fields of the form were set (`all`: the form was set whole): its text fields follow them. */
  shown: (form: T, fields: Array<keyof T> | 'all') => void
}

/**
 * Brakeman's form on Settings and the rule above, in one place, so that each path that answers it goes through `answer` and the
 * watcher through `formMove`: `form`, what the person edits; `base`, what it was set from, by a read or a reply (a form that still
 * holds it is not edited); `gen`, the generation it was set under; `busy`, the operations the page started that are in flight;
 * `stale`, a save refused since the configuration was saved elsewhere, until a choice answers it.
 */
export function configForm<T extends { rev: number }>(deps: ConfigFormDeps<T>) {
  const form = ref(null) as Ref<T | null>
  const base = ref(null) as Ref<T | null>
  const gen = ref(-1)
  const busy = ref(0)
  const stale = ref(false)
  /** A choice's read of what is saved now failed: its words, the form and the refusal kept. */
  const staleError = ref('')
  const choosing = ref(false)
  const current = () => deps.current(form.value)
  /** Whether the form was changed here since it was set: compared with what it was set from, not with what is saved now. */
  const edited = computed(() => {
    const c = current()
    return !!base.value && !!c && !deps.same(c, base.value)
  })
  /** Whether the form holds what is not saved: Save is offered for it. */
  const dirty = computed(() => {
    const c = current()
    const s = deps.saved()
    return !!s && !!c && !deps.same(c, s)
  })
  const copy = (c: T) => JSON.parse(JSON.stringify(c)) as T
  const snap = () => {
    const c = current()
    return c ? copy(c) : null
  }

  function reset(c: T) {
    gen.value = deps.generation()
    base.value = copy(c)
    form.value = copy(c)
    stale.value = false
    staleError.value = ''
    deps.shown(form.value, 'all')
  }
  function drop() {
    form.value = null
    base.value = null
    stale.value = false
    staleError.value = ''
  }
  /** The watcher's step: the move `formMove` decides, made. */
  function follow() {
    const s = deps.saved()
    const f = form.value
    const move = formMove(f ? { gen: gen.value, rev: f.rev, edited: edited.value, held: busy.value > 0 || stale.value } : null, deps.generation(), s)
    if (move === 'set') reset(s!)
    else if (move === 'drop') drop()
    return move
  }
  /**
   * An operation the page started: the watcher is off while it is out, and looks again once it has applied or discarded its result
   * (`follow`, as `busy` falls). `run` hands each reply to the guard (`answered`), which applies it by `answer` over the fields
   * still holding what `began` holds (what a save took at the click; by default, the form as the operation starts) and sets what
   * the form was set from to the reply. Whether it was applied.
   */
  async function op<R>(run: (answered: (reply: T, keys?: ReadonlyArray<keyof T>) => boolean) => Promise<R>, from?: T): Promise<R> {
    const f = form.value
    const at = gen.value
    // Copies: the form as the server takes it shares the form's nested objects, which the person edits in place.
    const began = from ? copy(from) : snap()
    busy.value++
    try {
      return await run((reply, keys) => {
        const now = snap()
        // The form the operation began on, under its generation: a reset meanwhile made the form another's.
        if (!f || !began || !now || form.value !== f || gen.value !== at) return false
        const fields = answer(f, began, reply, now, keys)
        if (!fields) return false
        base.value = copy(reply)
        deps.shown(f, fields)
        return true
      })
    } finally {
      busy.value--
    }
  }

  /**
   * Saves `sent`, the form as the page's Save took it at the click, under the generation `at`: an operation that began at the click,
   * so what was typed since is kept for the next Save against the revision saved (`answered`). A stale refusal sets `stale` and
   * throws a `StaleRefusal`; the form is left as it is, held while the refusal waits for its choice.
   */
  async function save(sent: T, at: number): Promise<void> {
    await op(async (answered) => {
      try {
        const out = await deps.save(sent)
        if (answered(out.config)) {
          stale.value = false
          staleError.value = ''
        }
      } catch (e) {
        if (deps.generation() === at && deps.isStale(e)) {
          stale.value = true
          throw new StaleRefusal(e)
        }
        throw e
      }
    }, sent)
  }
  /**
   * A choice of the stale refusal: reads what is saved now, fresh, as an operation of its own, and hands the reply to `apply`, whose
   * guard applies it only to the form the choice was made on, under its generation, when it is newer. A read that fails leaves the
   * form and the refusal as they are, the failure said. Whether the reply was applied.
   */
  async function choose(apply: (now: T, answered: (reply: T, keys?: ReadonlyArray<keyof T>) => boolean) => boolean): Promise<boolean> {
    const f = form.value
    const at = gen.value
    staleError.value = ''
    choosing.value = true
    try {
      return await op(async (answered) => {
        try {
          return apply(await deps.read(at), answered)
        } catch (e) {
          if (form.value === f && gen.value === at) staleError.value = `Not read: ${(e as Error).message}`
          return false
        }
      })
    } finally {
      choosing.value = false
    }
  }
  /** The first choice: what is saved now replaces the form, but for what was typed since the choice (the person confirmed). */
  function readAgain(): Promise<boolean> {
    return choose((now, answered) => {
      if (!answered(now)) return false
      stale.value = false
      return true
    })
  }
  /** The second: the form keeps its edits and takes the revision saved now (and is set from it), so the next Save replaces the other. */
  function keepMine(): Promise<boolean> {
    return choose((now, answered) => {
      if (!answered(now, ['rev'])) return false
      stale.value = false
      return true
    })
  }
  return { form, base, gen, busy, stale, staleError, choosing, edited, dirty, reset, drop, follow, op, save, readAgain, keepMine }
}

/** A save refused since the configuration was saved elsewhere since the form's revision (its cause the server's refusal). */
export class StaleRefusal extends Error {
  constructor(cause: unknown) {
    super('they were changed elsewhere since this form was read', { cause })
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
