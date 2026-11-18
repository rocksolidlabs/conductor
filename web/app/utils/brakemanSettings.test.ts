import { describe, expect, it } from 'vitest'
import type { BrakemanConfig } from './brakeman'
import { GRANTS, sameConfig } from './brakeman'
import { PRESETS, applyPreset, briefingTimes, crewPicks, pickerItems, presetChoice, presetLabel, presetOf, projectPicks, runPicks, sessionPicks, withEntry, withoutAt, type Preset } from './brakemanSettings'

// Brakeman's settings from a preset (#993): a preset sets what Brakeman may do, and nothing else of the form.

function config(over: Partial<BrakemanConfig> = {}): BrakemanConfig {
  return {
    enabled: true,
    agentId: 'claude',
    account: '/home/me/.claude-work',
    model: 'opus',
    cwd: '/home/me/code',
    allowConductorTools: true,
    visibility: { runs: true, logs: true, dispatch: true, sessions: true, touched: true, terminals: false, capacity: true },
    scope: { projects: ['CND'], runs: ['fix-login-1a2b3c4d'] },
    permissions: { observe: true, draft: true, propose: true },
    wake: { mode: 'background', windowSeconds: 120, cooldownMinutes: 30, questionMinutes: 15, briefings: ['07:00'] },
    budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 40, minutesPerDay: 240 },
    rev: 4,
    ...over,
  }
}

const copy = (c: BrakemanConfig) => JSON.parse(JSON.stringify(c)) as BrakemanConfig

describe('the presets', () => {
  it('are the three, in order, each with its words', () => {
    expect(PRESETS.map((p) => p.value)).toEqual(['observe', 'draft', 'act'])
    expect(PRESETS.map((p) => presetLabel(p.value))).toEqual(['Observe', 'Draft and propose', 'Act within grants'])
    expect(presetLabel('custom')).toBe('Custom')
  })

  it('read back as the preset applied: Observe and Draft and propose from any form, Act within grants once a grant is ticked', () => {
    for (const start of [config(), config({ permissions: { observe: true, draft: false, propose: true, act: ['route_decision'] } })]) {
      const o = copy(start)
      applyPreset(o, 'observe')
      expect(presetOf(o.permissions)).toBe('observe')
      const d = copy(start)
      applyPreset(d, 'draft')
      expect(presetOf(d.permissions)).toBe('draft')
    }
    const a = config()
    applyPreset(a, 'act')
    a.permissions.act = ['launch_approved_plan']
    expect(presetOf(a.permissions)).toBe('act')
  })

  it('Observe and Draft and propose turn every grant off; Act within grants ticks none and keeps those ticked', () => {
    const granted = config({ permissions: { observe: true, draft: false, propose: false, act: ['request_repair'] } })
    const o = copy(granted)
    applyPreset(o, 'observe')
    expect(o.permissions).toEqual({ observe: true, draft: false, propose: false, act: [] })
    const d = copy(granted)
    applyPreset(d, 'draft')
    expect(d.permissions).toEqual({ observe: true, draft: true, propose: true, act: [] })
    const a = copy(granted)
    applyPreset(a, 'act')
    expect(a.permissions).toEqual({ observe: true, draft: true, propose: true, act: ['request_repair'] })
    const none = config()
    applyPreset(none, 'act')
    expect(none.permissions.act ?? []).toEqual([])
  })

  it('never touch the terminals, the agent, its account, model or folder, the tools, the scope, the budget or the wake', () => {
    for (const visibility of [{ ...config().visibility, terminals: false }, { ...config().visibility, terminals: true }]) {
      const start = config({ visibility })
      for (const p of ['observe', 'draft', 'act'] as Preset[]) {
        const c = copy(start)
        applyPreset(c, p)
        const { permissions: _a, ...rest } = c
        const { permissions: _b, ...before } = start
        expect(rest).toEqual(before)
        expect(c.permissions.observe).toBe(true)
      }
    }
  })

  it('never tick a grant, whatever the form held: every grant on after a preset was on before it', () => {
    for (const p of ['observe', 'draft', 'act'] as Preset[]) {
      for (const act of [undefined, [], ['prepare_followup'], GRANTS.map((g) => g.value)]) {
        const c = config({ permissions: { observe: true, draft: false, propose: true, act } })
        applyPreset(c, p)
        for (const g of c.permissions.act ?? []) expect(act ?? []).toContain(g)
      }
    }
  })

  it('a form that matches none is Custom', () => {
    expect(presetOf({ draft: true, propose: false })).toBe('custom')
    expect(presetOf({ draft: false, propose: true })).toBe('custom')
    expect(presetOf({ draft: false, propose: false, act: ['route_decision'] })).toBe('custom')
    expect(presetOf({ draft: true, propose: false, act: ['route_decision'] })).toBe('custom')
    expect(presetOf({ draft: false, propose: false, act: [] })).toBe('observe')
    expect(presetOf({ draft: true, propose: true, act: [] })).toBe('draft')
  })

  it('Act within grants with no grant saves what Draft and propose saves, so it reads as that, and the form says a grant is wanted', () => {
    const a = config()
    applyPreset(a, 'act')
    const d = config()
    applyPreset(d, 'draft')
    expect(sameConfig(a, d)).toBe(true)
    expect(presetOf(a.permissions)).toBe('draft')
    expect(presetChoice(a.permissions, 'act')).toEqual({ value: 'act', hint: 'Tick at least one grant. With none, this saves as Draft and propose.' })
    expect(presetChoice(a.permissions, null)).toEqual({ value: 'draft', hint: '' })
    expect(presetChoice(a.permissions, 'draft').value).toBe('draft')
    a.permissions.act = ['route_decision']
    expect(presetChoice(a.permissions, 'act')).toEqual({ value: 'act', hint: '' })
    expect(presetChoice({ draft: true, propose: false }, 'act').value).toBe('custom')
    expect(presetChoice({ draft: true, propose: false }, null).hint).toContain('Custom')
  })
})

describe('the scope pickers', () => {
  it('list what is read, then each id saved but not listed, by its id, once', () => {
    const listed = runPicks([{ id: 'fix-login-1a2b3c4d', name: 'fix-login', label: 'Fix login' }])
    expect(pickerItems(listed, ['fix-login-1a2b3c4d', 'gone-0a0b0c0d', 'gone-0a0b0c0d'])).toEqual([
      { label: 'Fix login (fix-login-1a2b3c4d)', value: 'fix-login-1a2b3c4d' },
      { label: 'gone-0a0b0c0d', value: 'gone-0a0b0c0d', unlisted: true },
    ])
    expect(pickerItems(listed, undefined)).toEqual(listed)
  })

  it('name each kind by what a person reads, saving its id', () => {
    expect(projectPicks([{ key: 'CND', name: 'Conductor' }, { key: 'OLD', name: 'Old', archivedAt: '2026-01-01T00:00:00Z' }, { key: 'ABC', name: 'ABC' }])).toEqual([
      { label: 'CND · Conductor', value: 'CND' },
      { label: 'ABC', value: 'ABC' },
    ])
    expect(crewPicks([{ id: 'reviewers', name: 'Reviewers' }])).toEqual([{ label: 'Reviewers (reviewers)', value: 'reviewers' }])
    expect(runPicks([{ id: 'r-0a1b2c3d', name: 'r' }])).toEqual([{ label: 'r (r-0a1b2c3d)', value: 'r-0a1b2c3d' }])
    expect(sessionPicks([{ id: '0123456789abcdef', name: 'shell' }, { id: 'fedcba9876543210', name: '' }])).toEqual([
      { label: 'shell (0123456789abcdef)', value: '0123456789abcdef' },
      { label: 'fedcba9876543210', value: 'fedcba9876543210' },
    ])
  })

  it('add a folder once, trimmed, and take one out by its place', () => {
    expect(withEntry(['/a'], ' /b ')).toEqual(['/a', '/b'])
    expect(withEntry(['/a'], '/a')).toEqual(['/a'])
    expect(withEntry(['/a'], '  ')).toEqual(['/a'])
    expect(withoutAt(['/a', '/b', '/c'], 1)).toEqual(['/a', '/c'])
  })
})

describe('the briefing times', () => {
  it('are the rows set, in their order, a row not yet set left out', () => {
    expect(briefingTimes(['07:00', '', ' 18:30 '])).toEqual(['07:00', '18:30'])
    expect(briefingTimes([])).toEqual([])
  })
})
