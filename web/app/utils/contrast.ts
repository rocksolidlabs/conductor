/**
 * Contrast as WCAG 2 measures it: a colour's relative luminance, the ratio of two, a translucent colour laid over another, and a
 * parser for the colour forms a browser and Tailwind v4 write (hex with alpha, rgb()/rgba(), color(srgb …), oklab(), oklch()),
 * which keeps the alpha after `/`. `CONTRAST_FLOORS` is the table of minima the token test (themeContrast.test.ts) and the drawn
 * page's walk (web/e2e/contrast.ts) both judge by. Plain data and functions: it imports nothing from Nuxt, so the browser walk
 * imports it too.
 */

/** A colour in sRGB, each channel and the alpha 0 to 1. */
export interface Rgba {
  r: number
  g: number
  b: number
  a: number
}

/**
 * The minima. Text is 4.5:1, large text 3:1 (24 px, or 18.66 px at weight 700 and over). Dimmed text (`--ui-text-dimmed`, kept for
 * what is said again elsewhere) is 3:1 on the page (`--ui-bg`) and 2.7:1 on the canvas and the raised surfaces (`bg-muted`,
 * `bg-elevated`, `bg-canvas`): Nuxt UI draws light dimmed and dark muted text with one step, which cannot be both. A mark (a status
 * dot) is held by no floor: the light amber mark is the brand's signal amber.
 */
export const CONTRAST_FLOORS = {
  text: 4.5,
  large: 3,
  dimmed: { page: 3, raised: 2.7 },
  largeText: { px: 24, boldPx: 18.66, boldWeight: 700 },
} as const

const clamp01 = (x: number) => Math.min(1, Math.max(0, x))

/** True for text the large-text floor applies to. */
export function isLargeText(px: number, weight: number): boolean {
  const l = CONTRAST_FLOORS.largeText
  return px >= l.px || (px >= l.boldPx && weight >= l.boldWeight)
}

/** The floor of a text: dimmed text on the page or on a raised surface, else text or large text. */
export function floorFor(o: { px: number; weight: number; dimmed?: 'page' | 'raised' }): number {
  if (o.dimmed) return CONTRAST_FLOORS.dimmed[o.dimmed]
  return isLargeText(o.px, o.weight) ? CONTRAST_FLOORS.large : CONTRAST_FLOORS.text
}

function srgbToLinear(c: number): number {
  return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)
}

function linearToSrgb(c: number): number {
  return c <= 0.0031308 ? 12.92 * c : 1.055 * Math.pow(c, 1 / 2.4) - 0.055
}

/** WCAG 2's relative luminance of an opaque colour (its alpha is not read). */
export function luminance(c: Rgba | string): number {
  const x = typeof c === 'string' ? mustParse(c) : c
  return 0.2126 * srgbToLinear(x.r) + 0.7152 * srgbToLinear(x.g) + 0.0722 * srgbToLinear(x.b)
}

/** The contrast ratio of two opaque colours, 1 to 21, either order. */
export function ratio(a: Rgba | string, b: Rgba | string): number {
  const la = luminance(a)
  const lb = luminance(b)
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05)
}

/** `top` at its own alpha (times `alpha`, when given) laid over `ground`; the result's alpha is the two combined. */
export function over(top: Rgba | string, ground: Rgba | string, alpha = 1): Rgba {
  const t = typeof top === 'string' ? mustParse(top) : top
  const g = typeof ground === 'string' ? mustParse(ground) : ground
  const ta = clamp01(t.a * alpha)
  const a = ta + g.a * (1 - ta)
  if (a === 0) return { r: 0, g: 0, b: 0, a: 0 }
  const ch = (x: number, y: number) => (x * ta + y * g.a * (1 - ta)) / a
  return { r: ch(t.r, g.r), g: ch(t.g, g.g), b: ch(t.b, g.b), a }
}

/** `color-mix(in srgb, a p%, b)` of two colours. */
export function mixSrgb(a: Rgba | string, b: Rgba | string, p: number): Rgba {
  const x = typeof a === 'string' ? mustParse(a) : a
  const y = typeof b === 'string' ? mustParse(b) : b
  const w = p / 100
  const alpha = x.a * w + y.a * (1 - w)
  if (alpha === 0) return { r: 0, g: 0, b: 0, a: 0 }
  const ch = (u: number, v: number) => (u * x.a * w + v * y.a * (1 - w)) / alpha
  return { r: ch(x.r, y.r), g: ch(x.g, y.g), b: ch(x.b, y.b), a: alpha }
}

const hex2 = (x: number) =>
  Math.round(clamp01(x) * 255)
    .toString(16)
    .padStart(2, '0')

/** `#rrggbb` of a colour (its alpha dropped). */
export function toHex(c: Rgba): string {
  return `#${hex2(c.r)}${hex2(c.g)}${hex2(c.b)}`
}

/** OKLab to sRGB (Björn Ottosson's reference matrices), clamped to the gamut. */
function fromOklab(L: number, A: number, B: number, a: number): Rgba {
  const l_ = L + 0.3963377774 * A + 0.2158037573 * B
  const m_ = L - 0.1055613458 * A - 0.0638541728 * B
  const s_ = L - 0.0894841775 * A - 1.291485548 * B
  const l = l_ * l_ * l_
  const m = m_ * m_ * m_
  const s = s_ * s_ * s_
  const r = 4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s
  const g = -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s
  const bb = -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s
  return { r: linearToSrgb(clamp01(r)), g: linearToSrgb(clamp01(g)), b: linearToSrgb(clamp01(bb)), a }
}

/** A number of a colour function: `none` is 0, a percentage is that share of `scale`. */
function num(token: string | undefined, scale = 1): number | undefined {
  if (token === undefined) return undefined
  const t = token.trim()
  if (t === 'none') return 0
  if (t.endsWith('%')) {
    const v = Number(t.slice(0, -1))
    return Number.isFinite(v) ? (v / 100) * scale : undefined
  }
  const v = Number(t.replace(/deg$/, ''))
  return Number.isFinite(v) ? v : undefined
}

/** A colour function's body split into its channels and its alpha: "a b c / d" or "a, b, c, d". */
function parts(body: string): { ch: string[]; alpha?: string } {
  const [main, slash] = body.split('/')
  const ch = (main ?? '')
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
  return { ch, alpha: slash?.trim() }
}

/**
 * A CSS colour as sRGB with its alpha, or undefined for a form it does not read (a var(), hsl(), a colour space other than srgb, a
 * word other than `transparent`). Hex takes 3, 4, 6 or 8 digits; rgb() and rgba() commas or spaces; oklab() and oklch() a
 * lightness as a number or a percentage.
 */
export function parseColor(value: string | undefined | null): Rgba | undefined {
  if (!value) return undefined
  const v = value.trim().toLowerCase()
  if (v === 'transparent') return { r: 0, g: 0, b: 0, a: 0 }
  const hex = v.match(/^#([\da-f]{3,8})$/)
  if (hex) {
    let h = hex[1]!
    if (h.length === 3 || h.length === 4) h = [...h].map((c) => c + c).join('')
    if (h.length !== 6 && h.length !== 8) return undefined
    const at = (i: number) => parseInt(h.slice(i, i + 2), 16) / 255
    return { r: at(0), g: at(2), b: at(4), a: h.length === 8 ? at(6) : 1 }
  }
  const fn = v.match(/^([a-z]+)\((.*)\)$/)
  if (!fn) return undefined
  const name = fn[1]!
  const { ch, alpha: alphaText } = parts(fn[2]!)
  let alpha = alphaText === undefined ? 1 : num(alphaText)
  if (alpha === undefined) return undefined
  if (name === 'rgb' || name === 'rgba') {
    // The comma form's fourth value is its alpha.
    if (ch.length === 4 && alphaText === undefined) alpha = num(ch[3])
    const [r, g, b] = [num(ch[0], 255), num(ch[1], 255), num(ch[2], 255)]
    if (r === undefined || g === undefined || b === undefined || alpha === undefined) return undefined
    return { r: clamp01(r / 255), g: clamp01(g / 255), b: clamp01(b / 255), a: clamp01(alpha) }
  }
  if (name === 'color') {
    if (ch[0] !== 'srgb') return undefined
    const [r, g, b] = [num(ch[1]), num(ch[2]), num(ch[3])]
    if (r === undefined || g === undefined || b === undefined) return undefined
    return { r: clamp01(r), g: clamp01(g), b: clamp01(b), a: clamp01(alpha) }
  }
  if (name === 'oklch') {
    const [L, C, H] = [num(ch[0]), num(ch[1], 0.4), num(ch[2])]
    if (L === undefined || C === undefined || H === undefined) return undefined
    const rad = (H * Math.PI) / 180
    return fromOklab(L, C * Math.cos(rad), C * Math.sin(rad), clamp01(alpha))
  }
  if (name === 'oklab') {
    const [L, A, B] = [num(ch[0]), num(ch[1], 0.4), num(ch[2], 0.4)]
    if (L === undefined || A === undefined || B === undefined) return undefined
    return fromOklab(L, A, B, clamp01(alpha))
  }
  return undefined
}

function mustParse(value: string): Rgba {
  const c = parseColor(value)
  if (!c) throw new Error(`contrast: not a colour it reads: ${value}`)
  return c
}
