import type { BrakemanConfig, BrakemanPermissions } from './brakeman'
import { observesOnly } from './brakeman'

/**
 * Brakeman's settings start from a preset (#993): Observe, Draft and propose, or Act within grants. A preset sets what Brakeman may
 * do and nothing else: drafting, proposing and the grants. It never turns on terminal screens and never touches the agent, its
 * account, its model, its folder or whether it runs Conductor's tools without asking. It only fills the form: the page's one Save
 * sends the configuration at the revision read, and the server refuses a stale one. A preset never ticks a grant: only the person
 * does, one switch at a time, and only for an operation this server serves.
 */
export type Preset = 'observe' | 'draft' | 'act'
/** What the form shows: a preset, or Custom for permissions no preset sets (drafting without proposing, a grant with either off). */
export type PresetShown = Preset | 'custom'

export const PRESETS: ReadonlyArray<{ value: Preset; label: string; description: string }> = [
  { value: 'observe', label: 'Observe', description: 'It reads and briefs. It drafts nothing, proposes nothing and runs nothing.' },
  { value: 'draft', label: 'Draft and propose', description: 'It drafts tickets, crews and handoffs and proposes operations, which you edit, make and take.' },
  { value: 'act', label: 'Act within grants', description: 'It drafts and proposes, and runs itself the operations you grant below, each through its own route.' },
]

/** The words of a preset, or Custom's. */
export function presetLabel(p: PresetShown): string {
  return p === 'custom' ? 'Custom' : (PRESETS.find((x) => x.value === p)?.label ?? p)
}

/** The preset permissions match: Act within grants needs at least one grant, so with none they read as Draft and propose. */
export function presetOf(p: Pick<BrakemanPermissions, 'draft' | 'propose' | 'act'>): PresetShown {
  if (observesOnly(p)) return 'observe'
  if (p.draft && p.propose) return p.act?.length ? 'act' : 'draft'
  return 'custom'
}

/**
 * The form with a preset's permissions, in place: Observe turns drafting, proposing and every grant off; Draft and propose turns
 * both on and every grant off; Act within grants turns both on and keeps the grants as they are, ticking none. Nothing else of the
 * form changes.
 */
export function applyPreset(form: Pick<BrakemanConfig, 'permissions'>, preset: Preset): void {
  const p = form.permissions
  if (preset === 'observe') {
    p.draft = false
    p.propose = false
    p.act = []
    return
  }
  p.draft = true
  p.propose = true
  if (preset === 'draft') p.act = []
}

/**
 * The preset the form shows, given the one the person last picked here (null when none was, or the form was read again): Act within
 * grants picked with no grant ticked yet stays shown as picked, with the words that a grant is wanted, and saves as Draft and propose.
 */
export function presetChoice(p: Pick<BrakemanPermissions, 'draft' | 'propose' | 'act'>, picked: Preset | null): { value: PresetShown; hint: string } {
  const now = presetOf(p)
  if (picked === 'act' && now === 'draft') return { value: 'act', hint: 'Tick at least one grant. With none, this saves as Draft and propose.' }
  if (now === 'custom') return { value: 'custom', hint: 'Custom: the switches below are as set, matching no preset. Pick one to start from it.' }
  return { value: now, hint: '' }
}

/** A picker's entry: what it shows, the id it saves, and whether the id is saved but not among those listed. */
export interface PickerItem {
  label: string
  value: string
  unlisted?: boolean
}

/**
 * A scope picker's entries: those listed, then each id saved but not listed (a run that ended, a session gone, a project or crew not
 * read), shown by its id so it stays a chip that can be taken off.
 */
export function pickerItems(listed: readonly PickerItem[], picked: readonly string[] | undefined): PickerItem[] {
  const out = [...listed]
  const seen = new Set(listed.map((i) => i.value))
  for (const v of picked ?? []) if (!seen.has(v)) {
    seen.add(v)
    out.push({ label: v, value: v, unlisted: true })
  }
  return out
}

/** Dispatch's projects, by key. */
export function projectPicks(projects: ReadonlyArray<{ key: string; name: string; archivedAt?: string }>): PickerItem[] {
  return projects.filter((p) => !p.archivedAt).map((p) => ({ label: p.name && p.name !== p.key ? `${p.key} · ${p.name}` : p.key, value: p.key }))
}

/** The saved crews, by id. */
export function crewPicks(crews: ReadonlyArray<{ id: string; name: string }>): PickerItem[] {
  return crews.map((c) => ({ label: c.name && c.name !== c.id ? `${c.name} (${c.id})` : c.id, value: c.id }))
}

/** The runs the page has live, by id. */
export function runPicks(runs: ReadonlyArray<{ id: string; name: string; label?: string }>): PickerItem[] {
  return runs.map((r) => ({ label: `${r.label || r.name} (${r.id})`, value: r.id }))
}

/** The sessions the page has live, by id. */
export function sessionPicks(sessions: ReadonlyArray<{ id: string; name: string }>): PickerItem[] {
  return sessions.map((s) => ({ label: s.name ? `${s.name} (${s.id})` : s.id, value: s.id }))
}

/** A list with one entry added at its end, trimmed; unchanged for an empty one or one already in it. */
export function withEntry(list: readonly string[], entry: string): string[] {
  const v = entry.trim()
  return !v || list.includes(v) ? [...list] : [...list, v]
}

/** A list with the entry at `index` taken out. */
export function withoutAt<T>(list: readonly T[], index: number): T[] {
  return list.filter((_, i) => i !== index)
}

/** The briefing times the form's rows hold, as the server takes them: each row trimmed, the empty ones (a row added, not yet set) left out. */
export function briefingTimes(rows: readonly string[]): string[] {
  return rows.map((t) => t.trim()).filter(Boolean)
}
