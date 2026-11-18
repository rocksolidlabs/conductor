// A background brief of Brakeman's that holds a decision (the server's rule, internal/api/brakeman_notify.go): the
// `brakeman_needs_you` event on the operator's stream, `{id, decisions}`, at most 64 bytes, and what a page makes of it, a browser
// notification and the chime under the person's switches. The words are Conductor's own and fixed, the server's message word for
// word: the request's id and a count, never a word of the brief. Where a click goes is built here from the validated id alone.

/** The longest event the page takes, in UTF-8 bytes; the server's longest is 46. */
export const MAX_NEEDS_YOU_EVENT = 64
/** The most decision cards a brief holds (brakeman.MaxItems). */
const MAX_DECISIONS = 100
const REQUEST_ID = /^br-[1-9][0-9]{0,17}$/

/** The one tag of every such notification: a newer one replaces an older one in the tray. */
export const BRIEF_ALERT_TAG = 'conductor-brakeman-needs-you'
/** The notification's body: where a click goes. */
export const BRIEF_ALERT_BODY = 'It opens on the Brakeman page.'

/** The Alerts switch of its own, per browser, shown while Brakeman runs in the background. */
export const BRIEF_SWITCH_LABEL = "Brakeman's background briefs with a decision"
export const BRIEF_SWITCH_DESCRIPTION =
  "A browser notification and the chime, as switched on above, when a brief holds a decision no earlier one held: once, on the pages open then. Its other briefs, and its turns' own questions, alert no one."
/** Settings → Brakeman's sentence under "Wake on events", after what it already says. */
export const BRIEF_SETTINGS_SENTENCE =
  'A background brief that holds a decision no earlier one held notifies once: the pages open then, and the webhooks that list brakeman_needs_you.'

export interface BrakemanNeedsYou {
  id: string
  decisions: number
}

/**
 * The event's data as the stream carried it, or null: more than MAX_NEEDS_YOU_EVENT bytes, not JSON, not an object, an id that is
 * not `br-<n>`, or a count that is not an integer of 1 to 100 is dropped whole. Only the id and the count are taken.
 */
export function asBrakemanNeedsYou(data: string): BrakemanNeedsYou | null {
  if (new TextEncoder().encode(data).length > MAX_NEEDS_YOU_EVENT) return null
  let v: unknown
  try {
    v = JSON.parse(data)
  } catch {
    return null
  }
  if (!v || typeof v !== 'object' || Array.isArray(v)) return null
  const e = v as Record<string, unknown>
  if (typeof e.id !== 'string' || !REQUEST_ID.test(e.id)) return null
  const n = e.decisions
  if (typeof n !== 'number' || !Number.isInteger(n) || n < 1 || n > MAX_DECISIONS) return null
  return { id: e.id, decisions: n }
}

/** The server's words for a brief id holding n decisions: the webhook's message and the notification's title. */
export function needsYouWords(id: string, n: number): string {
  return n === 1 ? `Brakeman's background brief ${id} holds 1 decision` : `Brakeman's background brief ${id} holds ${n} decisions`
}

/** The page a click opens: the brief's own card on the Brakeman page, at every width. */
export function briefPath(id: string): string {
  return `/brakeman?request=${encodeURIComponent(id)}`
}

/** The notification of a validated event: its title, body, tag and where a click goes. */
export function briefAlert(e: BrakemanNeedsYou): { title: string; body: string; tag: string; path: string } {
  return { title: needsYouWords(e.id, e.decisions), body: BRIEF_ALERT_BODY, tag: BRIEF_ALERT_TAG, path: briefPath(e.id) }
}
