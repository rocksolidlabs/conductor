import type { ShellDraft, ShellDrafts } from './shell'

// Drafts of answers (docs/plans/phone-shell.md, OSS-2b, "Drafts and their revalidation"): in a shell, the provider's ShellDrafts over
// the store's drafts area; outside one, this implementation of the same interface over the browser's local storage, under the same
// bounds: at most 32 drafts of at most 12 KiB each as JSON, each the current sign-in's (another sign-in's invisible), none kept after
// a sign-out, and none older than seven days (removed when read).

export const MAX_DRAFTS = 32
export const MAX_DRAFT_BYTES = 12 * 1024
export const DRAFT_MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000
const KEY = 'conductor.shell.drafts'

type Kept = ShellDraft & { signIn: string }

/** The words of each refusal of a put. */
export const DRAFT_REFUSALS = {
  full: 'Not saved on this phone: 32 answers are kept already',
  too_large: 'Not saved on this phone: this answer is longer than a draft can be',
  not_saved: 'Not saved on this phone',
  signed_out: 'Not saved on this phone: you are signed out',
} as const

/**
 * ShellDrafts over `storage` for the sign-in `signIn()` answers (null when signed out). `signOut` removes every draft. Every
 * storage failure is `not_saved`, never shown as kept.
 */
export function localDrafts(storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>, signIn: () => string | null, now: () => number = Date.now): ShellDrafts & { signOut(): void } {
  const read = (): Kept[] => {
    try {
      const v = JSON.parse(storage.getItem(KEY) ?? '[]') as unknown
      return Array.isArray(v) ? (v as Kept[]) : []
    } catch {
      return []
    }
  }
  const write = (all: Kept[]) => storage.setItem(KEY, JSON.stringify(all))
  /** The current sign-in's drafts, those older than seven days removed. */
  const mine = (): Kept[] => {
    const who = signIn()
    const all = read()
    const fresh = all.filter((d) => now() - Date.parse(d.savedAt) <= DRAFT_MAX_AGE_MS)
    if (fresh.length !== all.length) {
      try {
        write(fresh)
      } catch {
        // Kept as it was: the next read removes them.
      }
    }
    return who === null ? [] : fresh.filter((d) => d.signIn === who)
  }
  const strip = ({ signIn: _s, ...d }: Kept): ShellDraft => d

  return {
    async get(id) {
      const d = mine().find((x) => x.id === id)
      return d ? strip(d) : null
    },
    async list() {
      return mine().slice(0, MAX_DRAFTS).map(strip)
    },
    async put(d) {
      const who = signIn()
      if (who === null) return { ok: false, code: 'signed_out', words: DRAFT_REFUSALS.signed_out }
      if (new TextEncoder().encode(JSON.stringify(d)).length > MAX_DRAFT_BYTES) return { ok: false, code: 'too_large', words: DRAFT_REFUSALS.too_large }
      const all = read().filter((x) => now() - Date.parse(x.savedAt) <= DRAFT_MAX_AGE_MS)
      const others = all.filter((x) => !(x.signIn === who && x.id === d.id))
      if (others.filter((x) => x.signIn === who).length >= MAX_DRAFTS) return { ok: false, code: 'full', words: DRAFT_REFUSALS.full }
      try {
        write([...others, { ...d, signIn: who }])
      } catch {
        return { ok: false, code: 'not_saved', words: DRAFT_REFUSALS.not_saved }
      }
      return { ok: true }
    },
    async remove(id) {
      const who = signIn()
      write(read().filter((x) => !(x.signIn === who && x.id === id)))
    },
    signOut() {
      storage.removeItem(KEY)
    },
  }
}
