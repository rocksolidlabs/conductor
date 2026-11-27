import type { TrialAgent } from './compareTemplate'

/**
 * What New agent trial keeps of the last trial the server took, per viewer (localStorage), so that the next one starts where the last
 * left off: the agents in order, the folder, and for each of the last MAX_FOLDER_CHECKS folders the Check line last used there. Never
 * an arguments line (it can hold a key), an account, a name, an approach, what each agent was told or a ticket. Every read and write is
 * guarded: a store that throws or holds something else is as none, and the form works without it.
 */

/** The store's key. */
export const TRIAL_MEMORY_KEY = 'conductor-trial'

/** How many folders' Check lines are kept, the most recent first. */
export const MAX_FOLDER_CHECKS = 20

/** How many agents a trial has at most (COMPARE_LIMITS.candidates), and so how many ids are kept. */
const MAX_AGENTS = 4

/** What is kept. */
export interface TrialMemory {
  agents: string[]
  folder: string
  checks: Array<{ folder: string; check: string }>
}

const strings = (v: unknown): v is string[] => Array.isArray(v) && v.every((x) => typeof x === 'string')

/** What the store holds, checked; null for nothing, something else, or a store that throws. */
export function readTrialMemory(store: Pick<Storage, 'getItem'> | null): TrialMemory | null {
  try {
    const raw = store?.getItem(TRIAL_MEMORY_KEY)
    if (!raw) return null
    const v = JSON.parse(raw) as Partial<TrialMemory>
    if (!v || typeof v !== 'object' || !strings(v.agents) || typeof v.folder !== 'string' || !Array.isArray(v.checks)) return null
    const checks = v.checks.filter((c): c is { folder: string; check: string } => !!c && typeof c.folder === 'string' && typeof c.check === 'string')
    return { agents: v.agents.slice(0, MAX_AGENTS), folder: v.folder, checks: checks.slice(0, MAX_FOLDER_CHECKS) }
  } catch {
    return null
  }
}

/** The memory after a trial the server took: its agents and folder, its Check line first for its folder. */
export function withTrial(prev: TrialMemory | null, t: { agents: readonly string[]; folder: string; check: string }): TrialMemory {
  const folder = t.folder.trim()
  const others = (prev?.checks ?? []).filter((c) => c.folder !== folder)
  return {
    agents: t.agents.filter(Boolean).slice(0, MAX_AGENTS),
    folder,
    checks: (folder ? [{ folder, check: t.check.trim() }, ...others] : others).slice(0, MAX_FOLDER_CHECKS),
  }
}

/** Keeps a trial the server took. A store that refuses is told nothing more. */
export function rememberTrial(store: Pick<Storage, 'getItem' | 'setItem'> | null, t: { agents: readonly string[]; folder: string; check: string }) {
  try {
    store?.setItem(TRIAL_MEMORY_KEY, JSON.stringify(withTrial(readTrialMemory(store), t)))
  } catch {
    // The store may refuse; the next trial then starts as a new one does.
  }
}

/** The Check line last used in `folder`; '' for a folder not kept. */
export function checkFor(mem: TrialMemory | null, folder: string): string {
  const f = folder.trim()
  return mem?.checks.find((c) => c.folder === f)?.check ?? ''
}

/**
 * The agents a new draft starts with from what was kept: each kept agent that is still installed, in order; one no longer installed
 * is left out and an installed one not yet in the trial stands in, while one is left. Null when nothing usable is kept.
 */
export function rememberedAgents(mem: TrialMemory | null, agents: readonly TrialAgent[]): string[] | null {
  if (!mem?.agents.length) return null
  const installed = agents.filter((a) => a.id !== 'shell' && a.available !== false).map((a) => a.id)
  if (!installed.length) return null
  const out: string[] = []
  for (const id of mem.agents) {
    if (installed.includes(id)) out.push(id)
    else {
      const stand = installed.find((x) => !out.includes(x) && !mem.agents.includes(x))
      if (stand) out.push(stand)
    }
  }
  return out.length >= 2 ? out : null
}
