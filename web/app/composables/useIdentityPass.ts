import { ref } from 'vue'

// The pass store (docs/plans/decide-grant.md, "web/app"): the tab's pass in sessionStorage, in memory when the browser refuses
// storage. A pass is held no longer than its tab, and not past the first 401 it meets (`end`). It never reads or writes the workbench
// token's key. Only the fragment plugin (which sets it) and useDecide (which sends it) use it; the decide middleware asks
// `passHeld` alone (utils/passSites.test.ts).

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
    /** A new pass replaces the old one. */
    set(value: string) {
      keep(value)
      ended.value = false
    },
    /** Empties the store and marks the tab's access ended (a 401). */
    end() {
      keep('')
      ended.value = true
    },
    ended,
  }
}
