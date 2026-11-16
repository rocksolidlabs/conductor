import { describe, expect, it } from 'vitest'

// The design rules read from the app's sources (the UX review's design system): each rule names what it holds and fails on the
// source that breaks it, with the count. Sources are every .ts and .vue of the app, read as text, by their path under app/ (a
// source beside this file comes as ./name); test files are left out.

const sources = Object.fromEntries(
  Object.entries(import.meta.glob(['../**/*.ts', '../**/*.vue', '!../**/*.test.ts'], { query: '?raw', import: 'default', eager: true }) as Record<string, string>).map(([path, text]) => [path.replace(/^\.\.\//, '').replace(/^\.\//, 'utils/'), text]),
)

/** Every source that matches `pattern` (global), with how many times, apart from the `allowed` lines (by their text). */
function breaking(pattern: RegExp, only: (path: string) => boolean = () => true, allowed: Record<string, RegExp> = {}): Record<string, number> {
  const out: Record<string, number> = {}
  for (const [path, text] of Object.entries(sources)) {
    if (!only(path)) continue
    const n = text
      .split('\n')
      .filter((line) => !allowed[path]?.test(line))
      .reduce((sum, line) => sum + (line.match(new RegExp(pattern.source, pattern.flags.includes('g') ? pattern.flags : `${pattern.flags}g`)) ?? []).length, 0)
    if (n > 0) out[path] = n
  }
  return out
}

/** Tailwind's zinc, the eleven steps, as hex. */
const ZINC = ['#09090b', '#18181b', '#27272a', '#3f3f46', '#52525b', '#71717a', '#a1a1aa', '#d4d4d8', '#e4e4e7', '#f4f4f5', '#fafafa']

describe('the design rules', () => {
  it('read the sources', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(100)
  })

  it("draw greys from the brand's ink scale, never from zinc", () => {
    // One place keeps a zinc class: a ticket label's colour named "gray" (Dispatch's label palette).
    const allowed = { 'utils/dispatch.ts': /^\s*gray: 'bg-zinc-400',$/ }
    expect(breaking(new RegExp(`\\bzinc\\b|${ZINC.join('|')}`, 'i'), () => true, allowed)).toEqual({})
  })

  it("hover the sidebar's rows to the canvas's hover tone, which a raised tone would vanish into on the light canvas", () => {
    const sidebar = (path: string) => /^components\/(Sidebar[^/]*|SessionSidebar)\.vue$/.test(path)
    expect(Object.keys(sources).filter(sidebar).length).toBeGreaterThan(5)
    expect(breaking(/hover:bg-elevated\b/, sidebar)).toEqual({})
  })
})
