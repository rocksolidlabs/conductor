// What pages register (their group in the command palette, their group of the shortcuts list), as a stack: the last one registered is
// the page's. A view mounted inside another page (a crew's member inside its run, UX3 R72) registers over the page it is in, and when
// it goes the page's own group is the page's again. Nuxt sets the next page up before the last one is unmounted, so an entry is taken
// out by its own identity, never by being on top.

/** The stack with `e` on top. */
export function pushEntry<T>(stack: readonly T[], e: T): T[] {
  return [...stack, e]
}

/** The stack without `e` (its last copy); the same stack's copy when `e` is not on it. */
export function dropEntry<T>(stack: readonly T[], e: T): T[] {
  const i = stack.lastIndexOf(e)
  return i < 0 ? [...stack] : [...stack.slice(0, i), ...stack.slice(i + 1)]
}

/** The page's entry: the one registered last, or null. */
export function topEntry<T>(stack: readonly T[]): T | null {
  return stack.length ? (stack[stack.length - 1] as T) : null
}
