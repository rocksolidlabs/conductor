import { ref } from 'vue'

// The pass store (docs/plans/decide-grant.md, "web/app"): the tab's pass in sessionStorage, in memory when the browser refuses
// storage. A pass is held no longer than its tab, and not past the first 401 it meets (`end`, which ends the pass it was sent with).
// It never reads or writes the workbench token's key. Only the fragment plugin (which sets it) and useDecide (which sends it) use it;
// the decide middleware asks `passHeld` alone (utils/passSites.test.ts).

const KEY = 'conductor.identityPass'
const pass = ref<string | null>(null)
const ended = ref(false)

function storage(): Storage | null {
  try {
    return globalThis.sessionStorage ?? null
  } catch {
    return null
  }
}

/** The pass held, read from the tab's storage once. */
function held(): string {
  if (pass.value === null) {
    try {
      pass.value = storage()?.getItem(KEY) ?? ''
    } catch {
      pass.value = ''
    }
  }
  return pass.value
}

function keep(value: string) {
  pass.value = value
  try {
    if (value) storage()?.setItem(KEY, value)
    else storage()?.removeItem(KEY)
  } catch {
    /* storage refused: the pass is in memory for this page */
  }
}

/** Whether the tab holds a pass: all the decide middleware reads of the store. */
export function passHeld(): boolean {
  return held() !== ''
}

export function useIdentityPass() {
  return {
    /** The pass held, '' for none. */
    get: held,
    /** A new pass replaces the old one: a load's. */
    set(value: string) {
      keep(value)
      ended.value = false
    },
    /** A later link's pass, written to the tab's storage alone (this page's stays): true when the storage took one that differs. */
    stored(value: string): boolean {
      held() // this page's pass, read before the storage changes
      try {
        const s = storage()
        if (!s || s.getItem(KEY) === value) return false
        s.setItem(KEY, value)
        return true
      } catch {
        return false
      }
    },
    /** A 401 to a request sent with `refused`: that pass ended in the page, and in the storage while it holds it; the access ended. */
    end(refused: string) {
      if (held() === refused) pass.value = ''
      try {
        const s = storage()
        if (s?.getItem(KEY) === refused) s.removeItem(KEY)
      } catch {
        /* storage refused */
      }
      ended.value = true
    },
    ended,
  }
}
