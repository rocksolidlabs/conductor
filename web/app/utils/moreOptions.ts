/**
 * The closed line of the fold named More options (MoreOptions.vue): beside the folded button it names only what the person changed
 * inside, so that what a launch or a trial will use is read without opening it, and an untouched fold says nothing.
 */

/** Between two parts of the closed line. */
export const MORE_SEP = ' · '

/**
 * One part of the closed line: its words, and whether the person changed it from what the form starts with. A plain string is a part
 * its caller already judged changed; nothing (null, undefined, false, an empty string) is no part.
 */
export type MorePart = { words: string; changed: boolean } | string | null | undefined | false

/** The closed line: the words of each changed part, in order, each said once, joined by MORE_SEP; '' when nothing was changed. */
export function moreLine(parts: readonly MorePart[]): string {
  const said: string[] = []
  for (const p of parts) {
    if (!p) continue
    const words = (typeof p === 'string' ? p : p.changed ? p.words : '').trim()
    if (words && !said.includes(words)) said.push(words)
  }
  return said.join(MORE_SEP)
}
