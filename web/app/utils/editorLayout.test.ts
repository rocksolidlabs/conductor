import { describe, expect, it } from 'vitest'
import { SPLIT_DEFAULT, SPLIT_KEY } from './editorTabs'
import { layoutFromStorage } from './uiState'
import { clampSideSplit, editorWidth, fitsSide, LAYOUT_KEY, layoutFor, readLayout, readSideSplit, readSplitFor, SIDE_SPLIT_DEFAULT, SIDE_SPLIT_KEY, stepSplit, writeLayout, writeSideSplit, writeSplitFor } from './editorLayout'

function memory(init: Record<string, string> = {}) {
  const m = new Map(Object.entries(init))
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), map: m }
}

describe('the editor layout', () => {
  it('is stacked unless side by side is kept, and keeps the choice', () => {
    expect(readLayout(memory())).toBe('stacked')
    expect(readLayout(memory({ [LAYOUT_KEY]: 'side' }))).toBe('side')
    expect(readLayout(memory({ [LAYOUT_KEY]: 'sideways' }))).toBe('stacked')
    expect(readLayout(null)).toBe('stacked')
    const s = memory()
    writeLayout(s, 'side')
    expect(s.map.get(LAYOUT_KEY)).toBe('side')
    writeLayout(null, 'side')
  })

  it('keeps a split per layout, each within its own ends', () => {
    const s = memory()
    expect(readSplitFor('stacked', s)).toBe(SPLIT_DEFAULT)
    expect(readSplitFor('side', s)).toBe(SIDE_SPLIT_DEFAULT)
    writeSplitFor('stacked', s, 0.7)
    writeSplitFor('side', s, 0.9)
    expect(s.map.get(SPLIT_KEY)).toBe('0.7')
    expect(s.map.get(SIDE_SPLIT_KEY)).toBe('0.75')
    expect(readSplitFor('stacked', s)).toBe(0.7)
    expect(readSplitFor('side', s)).toBe(0.75)
    writeSideSplit(s, 0.1)
    expect(readSideSplit(s)).toBe(0.25)
    expect(readSideSplit(memory({ [SIDE_SPLIT_KEY]: 'x' }))).toBe(SIDE_SPLIT_DEFAULT)
    expect(clampSideSplit(0.5)).toBe(0.5)
  })

  it('moves a step on the arrows along its axis, to an end on Home and End, and not on other keys', () => {
    expect(stepSplit('side', 0.5, 'ArrowLeft')).toBeCloseTo(0.48)
    expect(stepSplit('side', 0.5, 'ArrowRight')).toBeCloseTo(0.52)
    expect(stepSplit('side', 0.5, 'ArrowUp')).toBe(0.5)
    expect(stepSplit('stacked', 0.6, 'ArrowUp')).toBeCloseTo(0.58)
    expect(stepSplit('stacked', 0.6, 'ArrowDown')).toBeCloseTo(0.62)
    expect(stepSplit('stacked', 0.6, 'ArrowLeft')).toBe(0.6)
    expect(stepSplit('side', 0.5, 'Home')).toBe(0.25)
    expect(stepSplit('side', 0.5, 'End')).toBe(0.75)
    expect(stepSplit('stacked', 0.6, 'End')).toBe(0.85)
    expect(stepSplit('side', 0.26, 'ArrowLeft')).toBe(0.25)
  })

  it('leaves the terminal its width side by side', () => {
    expect(editorWidth(1200, 0.5, 8)).toBe(600)
    expect(editorWidth(1200, 0.75, 8)).toBe(872)
    expect(editorWidth(500, 0.5, 8)).toBe(172)
    expect(editorWidth(300, 0.5, 8)).toBe(0)
  })

  it('shows side by side only on a wide window whose column holds both panes', () => {
    expect(layoutFor('side', true)).toBe('side')
    expect(layoutFor('side', false)).toBe('stacked')
    expect(layoutFor('stacked', true)).toBe('stacked')
    expect(layoutFor('side', true, false)).toBe('stacked')
    expect(fitsSide(464, 16)).toBe(true)
    expect(fitsSide(463, 16)).toBe(false)
    expect(fitsSide(312, 16)).toBe(false)
    expect(fitsSide(0, 16)).toBe(false)
  })
})

describe('the editor in the app screens\' state', () => {
  it('is what the layout document reads of its keys: the layout and each layout\'s split', () => {
    const m = new Map<string, string>()
    const s = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) }
    writeLayout(s, 'side')
    writeSplitFor('side', s, 0.7)
    writeSplitFor('stacked', s, 0.3)
    expect(layoutFromStorage(s).editor).toEqual({ layout: 'side', split: 0.3, sideSplit: 0.7 })
  })
})
