/** The tones a person's avatar can take, the brand's: the same name gets the same one everywhere. */
export const AVATAR_TONES = ['bg-primary text-inverted', 'bg-forest-500 text-white', 'bg-forest-200 text-forest-900', 'bg-elevated text-primary'] as const

/** The tone of a name, by a stable hash (FNV-1a), so a person is the same colour in the roster, the header and the chat. */
export function avatarTone(name: string): string {
  let h = 2166136261
  for (const ch of name) {
    h ^= ch.codePointAt(0)!
    h = Math.imul(h, 16777619) >>> 0
  }
  return AVATAR_TONES[h % AVATAR_TONES.length]!
}
