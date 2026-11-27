import { describe, expect, it } from 'vitest'
import { DEFAULT_ROUTES, EVENT_INFO, EVENT_TYPES, feedGroups, ROUTE_GROUPS, routeSummary, sparkBuckets } from './events'

describe('the Events page helpers', () => {
  it('puts every event type in exactly one routing group, each with a title', () => {
    const all = ROUTE_GROUPS.flatMap((g) => g.types)
    expect([...all].sort()).toEqual([...EVENT_TYPES].sort())
    expect(new Set(all).size).toBe(all.length)
    for (const t of EVENT_TYPES) expect(EVENT_INFO[t].title).toMatch(/^[A-Z]/)
    // What reports working, under its title on the Routing tab: not a prompt alone (#206). Claude Code's PreToolUse and the
    // other agents' pre-tool events report it (#195), and a Codex member's window title does (#186).
    expect(EVENT_INFO.working.source).toContain('a prompt submitted')
    expect(EVENT_INFO.working.source).toContain('a tool about to run')
    expect(EVENT_INFO.working.source).toContain("Codex's title")
    expect(EVENT_INFO.working.title).not.toMatch(/prompt/i)
  })

  it('reads the routing as where each kind goes', () => {
    expect(routeSummary(DEFAULT_ROUTES)).toEqual([
      { key: 'browser', icon: 'i-lucide-bell', title: 'Notification on this device', types: 'needs you, tool denied, artifacts, errors, exit ≠ 0' },
      { key: 'wall', icon: 'i-lucide-gallery-horizontal', title: 'Roundhouse jumps to it', types: 'needs you, handoffs' },
      { key: 'badge', icon: 'i-lucide-circle-dot', title: 'Sidebar badge', types: 'needs you, done, tool denied, errors, exit ≠ 0' },
    ])
    const quiet = Object.fromEntries(EVENT_TYPES.map((t) => [t, { badge: false, browser: false, wall: false, feed: true }])) as typeof DEFAULT_ROUTES
    expect(routeSummary(quiet)).toEqual([])
  })

  it('groups the feed by minute, newest first, the current minute saying now', () => {
    const at = (h: number, m: number, s: number) => new Date(2026, 9, 4, h, m, s).toISOString()
    const entries = [
      { at: at(8, 31, 48), seq: 1 },
      { at: at(8, 32, 5), seq: 2 },
      { at: at(8, 32, 41), seq: 3 },
      { at: at(8, 33, 4), seq: 4 },
    ]
    const g = feedGroups(entries, new Date(2026, 9, 4, 8, 33, 30).getTime())
    expect(g.map((x) => x.label)).toEqual(['08:33 · now', '08:32', '08:31'])
    expect(g[1]!.entries.map((e) => e.seq)).toEqual([3, 2])
    expect(feedGroups([], 0)).toEqual([])
  })

  it('counts the last hour per minute by what needs you, handoffs, errors and the rest', () => {
    const now = Date.UTC(2026, 9, 4, 8, 33, 30)
    const iso = (s: number) => new Date(now + s * 1000).toISOString()
    const { buckets, totals } = sparkBuckets(
      [
        { at: iso(-10), event: 'needs_input' },
        { at: iso(-20), event: 'handoff' },
        { at: iso(-70), event: 'exit_nonzero' },
        { at: iso(-75), event: 'tool_use' },
        { at: iso(-3700), event: 'error' },
        { at: iso(90), event: 'error' },
        { at: 'nope', event: 'done' },
      ],
      now,
    )
    expect(buckets).toHaveLength(60)
    expect(totals).toEqual({ needs: 1, handoff: 1, error: 1, other: 1 })
    expect(buckets.at(-1)).toMatchObject({ needs: 1, handoff: 1 })
    expect(buckets.at(-2)).toMatchObject({ error: 1, other: 1 })
  })
})
