import type { TranscriptEntry } from './protocol'

// A chat's permission question (docs/plans/chat-or-terminal.md, designs 7 and 11): what the call is in plain words, the exact
// command or path in one line, the whole input once opened, and the digest the answer is bound by, computed here over the very
// bytes the page drew, so that an answer carries the digest of what was shown and of nothing else.

/** The question's plain words, by the entry's `plain` (the session's closed set from its tool). */
export function plainWords(plain: string | undefined): string {
  switch (plain) {
    case 'command':
      return 'Run a command?'
    case 'file':
      return 'Change a file?'
    case 'read':
      return 'Read a file?'
  }
  return 'Use a tool?'
}

/** The plain words of a tool by its name, as the session computes `plain` (session.plainOf). */
export function plainOfTool(tool: string): 'command' | 'file' | 'read' | 'tool' {
  switch (tool.toLowerCase()) {
    case 'bash':
    case 'shell':
    case 'exec':
    case 'command':
    case 'commandexecution':
      return 'command'
    case 'edit':
    case 'write':
    case 'multiedit':
    case 'notebookedit':
    case 'filechange':
      return 'file'
    case 'read':
    case 'grep':
    case 'glob':
    case 'ls':
      return 'read'
  }
  return 'tool'
}

/** An input's nesting at most (the session's maxInputDepth): deeper is not an input the session took. */
const MAX_DEPTH = 256

/** A JSON value as the canonical form keeps it: a number by its literal, as written. */
export type JsonValue = null | boolean | { num: string } | string | JsonValue[] | { obj: Array<[string, JsonValue]> }

/**
 * text decoded as one JSON value, strictly as the session decodes a request's input (session.strictCanonical): a key twice in an
 * object, a lone surrogate escape, nesting past 256 or anything after the value is refused (null), and a number keeps its literal.
 */
export function parseStrict(text: string): JsonValue | undefined {
  let i = 0
  const ws = () => {
    while (i < text.length && (text[i] === ' ' || text[i] === '\t' || text[i] === '\n' || text[i] === '\r')) i++
  }
  const fail = (): never => {
    throw new Error('not clean')
  }
  const str = (): string => {
    if (text[i] !== '"') fail()
    i++
    let out = ''
    for (;;) {
      if (i >= text.length) fail()
      const c = text.charCodeAt(i)
      if (c === 0x22) {
        i++
        return out
      }
      if (c < 0x20) fail()
      if (c !== 0x5c) {
        out += text[i]
        i++
        continue
      }
      const e = text[i + 1]
      i += 2
      switch (e) {
        case '"':
          out += '"'
          break
        case '\\':
          out += '\\'
          break
        case '/':
          out += '/'
          break
        case 'b':
          out += '\b'
          break
        case 'f':
          out += '\f'
          break
        case 'n':
          out += '\n'
          break
        case 'r':
          out += '\r'
          break
        case 't':
          out += '\t'
          break
        case 'u': {
          const hex = (at: number) => {
            const h = text.slice(at, at + 4)
            if (!/^[0-9a-fA-F]{4}$/.test(h)) fail()
            return parseInt(h, 16)
          }
          const u = hex(i)
          i += 4
          if (u >= 0xdc00 && u <= 0xdfff) fail()
          if (u >= 0xd800 && u <= 0xdbff) {
            if (text[i] !== '\\' || text[i + 1] !== 'u') fail()
            const lo = hex(i + 2)
            if (lo < 0xdc00 || lo > 0xdfff) fail()
            i += 6
            out += String.fromCharCode(u, lo)
          } else out += String.fromCharCode(u)
          break
        }
        default:
          fail()
      }
    }
  }
  const value = (depth: number): JsonValue => {
    if (depth > MAX_DEPTH) fail()
    ws()
    const c = text[i]
    if (c === '{') {
      i++
      const obj: Array<[string, JsonValue]> = []
      const seen = new Set<string>()
      ws()
      if (text[i] === '}') {
        i++
        return { obj }
      }
      for (;;) {
        ws()
        const k = str()
        if (seen.has(k)) fail()
        seen.add(k)
        ws()
        if (text[i] !== ':') fail()
        i++
        obj.push([k, value(depth + 1)])
        ws()
        if (text[i] === ',') {
          i++
          continue
        }
        if (text[i] === '}') {
          i++
          return { obj }
        }
        fail()
      }
    }
    if (c === '[') {
      i++
      const arr: JsonValue[] = []
      ws()
      if (text[i] === ']') {
        i++
        return arr
      }
      for (;;) {
        arr.push(value(depth + 1))
        ws()
        if (text[i] === ',') {
          i++
          continue
        }
        if (text[i] === ']') {
          i++
          return arr
        }
        fail()
      }
    }
    if (c === '"') return str()
    for (const [lit, v] of [['true', true], ['false', false], ['null', null]] as const) {
      if (text.startsWith(lit, i)) {
        i += lit.length
        return v
      }
    }
    const m = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(text.slice(i, i + 400))
    if (!m || !m[0]) fail()
    i += m![0].length
    return { num: m![0] }
  }
  try {
    const v = value(0)
    ws()
    if (i !== text.length) return undefined
    return v
  } catch {
    return undefined
  }
}

const HEX = '0123456789abcdef'

/** s as Go's encoder writes a string with HTML escaping off: quotes, backslashes and controls escaped, U+2028 and U+2029 too. */
function quote(s: string): string {
  let out = '"'
  for (const ch of s) {
    const c = ch.codePointAt(0)!
    if (ch === '"') out += '\\"'
    else if (ch === '\\') out += '\\\\'
    else if (c === 0x08) out += '\\b'
    else if (c === 0x0c) out += '\\f'
    else if (c === 0x0a) out += '\\n'
    else if (c === 0x0d) out += '\\r'
    else if (c === 0x09) out += '\\t'
    else if (c < 0x20) out += `\\u00${HEX[c >> 4]}${HEX[c & 0xf]}`
    else if (c === 0x2028 || c === 0x2029) out += `\\u${c.toString(16)}`
    else out += ch
  }
  return out + '"'
}

/** a before b by code points, as Go sorts a map's keys (byte by byte of UTF-8). */
function byCodePoint(a: string, b: string): number {
  const x = Array.from(a)
  const y = Array.from(b)
  for (let k = 0; k < Math.min(x.length, y.length); k++) {
    const d = x[k]!.codePointAt(0)! - y[k]!.codePointAt(0)!
    if (d) return d
  }
  return x.length - y.length
}

/** v as canonical JSON: objects' keys sorted, no insignificant space, numbers as written (session.strictCanonical's form). */
export function canonical(v: JsonValue): string {
  if (v === null) return 'null'
  if (v === true) return 'true'
  if (v === false) return 'false'
  if (typeof v === 'string') return quote(v)
  if (Array.isArray(v)) return `[${v.map(canonical).join(',')}]`
  if ('num' in v) return v.num
  const kv = [...v.obj].sort((a, b) => byCodePoint(a[0], b[0]))
  return `{${kv.map(([k, x]) => `${quote(k)}:${canonical(x)}`).join(',')}}`
}

/** SHA-256 of bytes, lowercase hex. Computed here (a page on plain http has no crypto.subtle), synchronously. */
export function sha256Sync(bytes: Uint8Array): string {
  const K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe,
    0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
    0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b,
    0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
  ])
  const h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19])
  const len = bytes.length
  const padded = new Uint8Array(((len + 9 + 63) >> 6) << 6)
  padded.set(bytes)
  padded[len] = 0x80
  const view = new DataView(padded.buffer)
  view.setUint32(padded.length - 8, Math.floor(len / 0x20000000))
  view.setUint32(padded.length - 4, (len << 3) >>> 0)
  const w = new Uint32Array(64)
  const rotr = (x: number, n: number) => (x >>> n) | (x << (32 - n))
  for (let off = 0; off < padded.length; off += 64) {
    for (let t = 0; t < 16; t++) w[t] = view.getUint32(off + t * 4)
    for (let t = 16; t < 64; t++) {
      const a = w[t - 15]!
      const b = w[t - 2]!
      const s0 = rotr(a, 7) ^ rotr(a, 18) ^ (a >>> 3)
      const s1 = rotr(b, 17) ^ rotr(b, 19) ^ (b >>> 10)
      w[t] = (w[t - 16]! + s0 + w[t - 7]! + s1) >>> 0
    }
    let [a, b, c, d, e, f, g, hh] = [h[0]!, h[1]!, h[2]!, h[3]!, h[4]!, h[5]!, h[6]!, h[7]!]
    for (let t = 0; t < 64; t++) {
      const S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)
      const ch = (e & f) ^ (~e & g)
      const t1 = (hh + S1 + ch + K[t]! + w[t]!) >>> 0
      const S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)
      const maj = (a & b) ^ (a & c) ^ (b & c)
      const t2 = (S0 + maj) >>> 0
      hh = g
      g = f
      f = e
      e = (d + t1) >>> 0
      d = c
      c = b
      b = a
      a = (t1 + t2) >>> 0
    }
    h[0] = (h[0]! + a) >>> 0
    h[1] = (h[1]! + b) >>> 0
    h[2] = (h[2]! + c) >>> 0
    h[3] = (h[3]! + d) >>> 0
    h[4] = (h[4]! + e) >>> 0
    h[5] = (h[5]! + f) >>> 0
    h[6] = (h[6]! + g) >>> 0
    h[7] = (h[7]! + hh) >>> 0
  }
  return Array.from(h, (x) => x.toString(16).padStart(8, '0')).join('')
}

/** The digest a request is bound by (design 7): SHA-256 over the tool's name, a newline and the input's canonical JSON. */
export function inputDigest(tool: string, canon: string): string {
  return sha256Sync(new TextEncoder().encode(`${tool}\n${canon}`))
}

/** One row of the whole input as the question draws it: a field's name and its value as text (a string as it is, else JSON). */
export interface InputRow {
  key: string
  text: string
}

/**
 * An input as the question shows it: the one line (the command, the path, else the input's canonical JSON), the rows of the whole
 * input, whether the line alone is all of it (a one-line input, drawn whole at once), and the digest of exactly that input. `exact`
 * is false when the bytes are not an input the session could have taken, or their digest is not the request's: then nothing of it
 * may be allowed.
 */
export interface ShownInput {
  line: string
  rows: InputRow[]
  /** The line is the whole input: one field holding the command or path, on one line, or a short input with no field to show apart. */
  whole: boolean
  digest: string
  exact: boolean
}

/** How long a line may be and still be the whole input at once, in characters; a longer one is opened to be read whole. */
export const ONE_LINE_MAX = 160

const LINE_KEYS: Record<string, string[]> = { command: ['command', 'cmd'], file: ['file_path', 'notebook_path', 'path'], read: ['file_path', 'notebook_path', 'path', 'pattern'] }

/** The request's input as the question shows it, from its bytes (the entry's `input` part, whole) and its entry. */
export function shownInput(entry: Pick<TranscriptEntry, 'tool' | 'plain' | 'inputSha256'>, bytes: Uint8Array): ShownInput {
  let text = ''
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    return { line: '', rows: [], whole: false, digest: '', exact: false }
  }
  const v = parseStrict(text)
  if (v === undefined) return { line: text, rows: [{ key: '', text }], whole: false, digest: '', exact: false }
  const canon = canonical(v)
  const digest = inputDigest(entry.tool ?? '', canon)
  const exact = digest === entry.inputSha256
  const fields = v !== null && typeof v === 'object' && !Array.isArray(v) && 'obj' in v ? v.obj : null
  const asText = (x: JsonValue) => (typeof x === 'string' ? x : canonical(x))
  const rows: InputRow[] = fields ? fields.map(([k, x]) => ({ key: k, text: asText(x) })) : [{ key: '', text: canon }]
  let line = ''
  let lineKey: string | undefined
  for (const k of LINE_KEYS[entry.plain ?? ''] ?? []) {
    const f = fields?.find(([fk]) => fk === k)
    if (!f) continue
    if (typeof f[1] === 'string') line = f[1]
    else if (Array.isArray(f[1]) && f[1].every((a) => typeof a === 'string')) line = (f[1] as string[]).join(' ')
    else continue
    lineKey = k
    break
  }
  const short = (s: string) => !s.includes('\n') && Array.from(s).length <= ONE_LINE_MAX
  let whole: boolean
  if (lineKey !== undefined) whole = fields!.length === 1 && typeof fields![0]![1] === 'string' && short(line)
  else {
    line = canon
    whole = short(canon)
  }
  return { line, rows, whole, digest, exact }
}
