import type { SessionInfo } from '~/composables/useSessions'
import type { Attention, AttentionOption, Role } from '~/utils/protocol'

/**
 * Whether the session waits for a person to type into its terminal: a submission's text was typed and not entered,
 * its link no longer opening the session (source "submit"), and the next reply goes once someone has typed there.
 */
export function waitsForTerminal(a: Pick<Attention, 'state' | 'source'> | undefined): boolean {
  return a?.state === 'needs_input' && a.source === 'submit'
}

/** The heading a reply bar shows for a session that needs input: the terminal's for a held turn, else the agent's. */
export function waitingHeading(a: Pick<Attention, 'state' | 'source'> | undefined, agentName: string): string {
  return waitsForTerminal(a) ? 'Type in the terminal to go on' : `${agentName} is waiting on you`
}

/**
 * What a reply field says while the session waits for its terminal: it takes no reply then, for one would only wait for the
 * terminal, and a reply box's connection would not see it go or fail.
 */
export const TERMINAL_FIRST = 'Type in the terminal first'

/**
 * Whether the session's question is answered in its terminal (#288): a permission dialog whose choices the page has not been given
 * (the screen did not show them: a hosted session's dialog, one not read, the moment before the screen is read). A typed reply's
 * Enter would pick whatever the dialog highlights, so none is taken.
 */
export function answersInTerminal(a: Pick<Attention, 'state' | 'kind' | 'options'> | undefined): boolean {
  return a?.state === 'needs_input' && a.kind === 'permission' && !a.options?.length
}

/**
 * The durable decision a question mirrors (`dr-<n>`, the attention's `decision`; Program item DR1), or undefined for any other
 * attention: such a question is answered only as its decision, so a page offers it no reply field, choice or number key, whatever
 * the viewer's role, and the session refuses an answer naming it (`decision`).
 */
export function decisionOf(a: Pick<Attention, 'state' | 'decision'> | undefined): string | undefined {
  return a?.state === 'needs_input' && a.decision ? a.decision : undefined
}

/** The choices a question bar offers for a, each with its number key (choiceKey): its options, none for a decision's (decisionOf). */
export function offeredChoices(a: Pick<Attention, 'state' | 'decision' | 'options'> | undefined): AttentionOption[] {
  return a?.state === 'needs_input' && !decisionOf(a) ? (a.options ?? []) : []
}

/**
 * Why a page takes no typed reply to a, in the words its reply field shows in place of one; undefined when it takes one. Every
 * place that draws a reply field for an attention (the question bar, the sidebar's row and Needs you, the Yard's queue, the
 * command palette) asks this one function: the session waits for its terminal (waitsForTerminal), the question is answered in
 * the terminal (answersInTerminal), or it mirrors a durable decision (decisionOf), answered as that decision, where it says.
 */
export function noReply(a: Pick<Attention, 'state' | 'source' | 'kind' | 'options' | 'decision'> | undefined): string | undefined {
  if (waitsForTerminal(a)) return TERMINAL_FIRST
  if (answersInTerminal(a)) return ANSWER_IN_TERMINAL
  const d = decisionOf(a)
  return d ? decisionAnsweredBy(d) : undefined
}

/** Whether a page takes a typed reply to a from a viewer of role: a controller's, unless a takes none (noReply). */
export function takesReply(a: Pick<Attention, 'state' | 'source' | 'kind' | 'options' | 'decision'> | undefined, role: Role): boolean {
  return role === 'control' && noReply(a) === undefined
}

/** Where a question that mirrors decision id (decisionOf) is answered, said in place of a reply: by the operator, in Conductor. */
export function decisionAnsweredBy(id: string): string {
  return `The operator answers ${id} in Conductor`
}

/** What a reply field says while the question is answered in the terminal (answersInTerminal). */
export const ANSWER_IN_TERMINAL = 'Answer it in the terminal'

/**
 * The number key a page gives the choice at index i, which picks it: its place in the list, 1 to 9; none for one that changes the
 * session's permission mode (#288), which is clicked or nothing.
 */
export function choiceKey(o: AttentionOption, i: number): number | undefined {
  return o.mode || i >= 9 ? undefined : i + 1
}

/** The choice a page offers first, highlighted: the first that does not change the permission mode (#288); -1 for none. */
export function firstChoice(options: AttentionOption[] | undefined): number {
  return options?.findIndex((o) => !o.mode) ?? -1
}

/** What a choice that changes the session's permission mode says on hover. */
export const MODE_CHOICE = "Changes the session's permission mode"

/**
 * The attention as the pages read it (#258): a question keys typed in the terminal have answered (`answered`) is no question to
 * offer, so it reads as the empty state with source `input`, as the stream will say once the screen moves on; the mark stays on
 * it for a page that would say so. Every other attention is itself.
 */
export function liveAttention(a: Attention): Attention
export function liveAttention(a: Attention | undefined): Attention | undefined
export function liveAttention(a: Attention | undefined): Attention | undefined {
  if (!a?.answered || a.state !== 'needs_input') return a
  return { state: '', source: 'input', answered: a.answered }
}

/** A session as the pages read it: its attention the live form (liveAttention); the same object when nothing changes. */
export function liveSession(s: SessionInfo): SessionInfo {
  const a = liveAttention(s.attention)
  return a === s.attention ? s : { ...s, attention: a }
}

/** Sessions that need input, newest signal first. */
export function needingInput(sessions: Iterable<SessionInfo>): SessionInfo[] {
  const out: SessionInfo[] = []
  for (const s of sessions) {
    if (s.attention?.state === 'needs_input' && !isEnded(s.status)) out.push(s)
  }
  return out.sort((a, b) => (b.attention?.since ?? '').localeCompare(a.attention?.since ?? ''))
}

export function isEnded(status: string): boolean {
  return status === 'exited' || status === 'stopped'
}

export function isActive(s: SessionInfo): boolean {
  return s.status === 'running' || s.status === 'starting'
}

/**
 * Returns the sessions that newly entered needs_input compared with the
 * previous snapshot. Used to fire notifications exactly once per request.
 */
export function newlyNeedingInput(prev: Map<string, SessionInfo>, next: Map<string, SessionInfo>): SessionInfo[] {
  const out: SessionInfo[] = []
  for (const s of next.values()) {
    if (s.attention?.state !== 'needs_input' || isEnded(s.status)) continue
    const before = prev.get(s.id)
    const was = before?.attention?.state === 'needs_input'
    const sameSignal = was && before?.attention?.since === s.attention?.since
    if (!sameSignal) out.push(s)
  }
  return out
}

/**
 * The sessions of a list that may alert a person (a browser notification, the chime): all but a quiet one, a background Brakeman
 * turn's (`crew.brakeman.quiet`), whose question Needs you still lists and which pages no one (docs/plans/brakeman.md, the
 * invariant on Brakeman's questions).
 */
export function alerting(list: readonly SessionInfo[]): SessionInfo[] {
  return list.filter((s) => !s.crew?.brakeman?.quiet)
}

/** Document title with the count of sessions needing input. */
export function titleWithCount(base: string, count: number): string {
  return count > 0 ? `(${count}) ${base}` : base
}

/** Amber status dot; the brand terracotta is never used as a status colour. */
export const ATTENTION_DOT_COLOR = '#c98a1b'

let faviconCache: { plain?: string; alert?: string } = {}

/**
 * Draws the brand favicon with an amber status dot in the corner. The dot is
 * a separate status marker; the junction in the mark is never recoloured.
 */
export async function attentionFavicon(alert: boolean, src = '/brand/conductor-favicon.svg'): Promise<string> {
  if (!alert) return src
  if (faviconCache.alert) return faviconCache.alert
  try {
    const img = new Image()
    img.src = src
    await img.decode()
    const size = 64
    const canvas = document.createElement('canvas')
    canvas.width = size
    canvas.height = size
    const ctx = canvas.getContext('2d')!
    ctx.drawImage(img, 0, 0, size, size)
    ctx.beginPath()
    ctx.arc(size - 13, 13, 12, 0, Math.PI * 2)
    ctx.fillStyle = '#eef1e9'
    ctx.fill()
    ctx.beginPath()
    ctx.arc(size - 13, 13, 9, 0, Math.PI * 2)
    ctx.fillStyle = ATTENTION_DOT_COLOR
    ctx.fill()
    faviconCache.alert = canvas.toDataURL('image/png')
    return faviconCache.alert
  } catch {
    return src
  }
}

/** Short two-tone chime through the Web Audio API; no asset needed. */
export function playChime(): void {
  try {
    const AC = window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext
    const ctx = new AC()
    const now = ctx.currentTime
    for (const [i, freq] of [880, 1175].entries()) {
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.type = 'sine'
      osc.frequency.value = freq
      gain.gain.setValueAtTime(0.0001, now + i * 0.14)
      gain.gain.exponentialRampToValueAtTime(0.2, now + i * 0.14 + 0.02)
      gain.gain.exponentialRampToValueAtTime(0.0001, now + i * 0.14 + 0.13)
      osc.connect(gain).connect(ctx.destination)
      osc.start(now + i * 0.14)
      osc.stop(now + i * 0.14 + 0.14)
    }
    window.setTimeout(() => ctx.close(), 600)
  } catch {
    /* audio unavailable */
  }
}

export function resetFaviconCache() {
  faviconCache = {}
}
