import { describe, expect, it, vi } from 'vitest'
import type { MenuItemConstructorOptions } from 'electron'

// buildMenu hands its template to Electron: here the template itself comes back, to read.
vi.mock('electron', () => ({ Menu: { buildFromTemplate: (t: MenuItemConstructorOptions[]) => t }, app: { getVersion: () => '0.0.0-test' } }))
const { buildMenu, zoomMenuItems } = await import('../src/menu')

const items = (t: MenuItemConstructorOptions[]) => t.map((i) => ({ label: i.label, accelerator: i.accelerator, enabled: i.enabled }))

describe("the View menu's zoom items", () => {
  it('show the level, then the reset to 100% and the moves with their keys', () => {
    expect(items(zoomMenuItems(0.5, () => {}))).toEqual([
      { label: 'Zoom: 110%', accelerator: undefined, enabled: false },
      { label: 'Reset Zoom to 100%', accelerator: 'CmdOrCtrl+0', enabled: undefined },
      { label: 'Zoom In', accelerator: 'CmdOrCtrl+=', enabled: undefined },
      { label: 'Zoom Out', accelerator: 'CmdOrCtrl+-', enabled: undefined },
    ])
  })
  it('read the level at every one, the bounds included', () => {
    expect(zoomMenuItems(0, () => {})[0]!.label).toBe('Zoom: 100%')
    expect(zoomMenuItems(-3, () => {})[0]!.label).toBe('Zoom: 58%')
    expect(zoomMenuItems(1.5, () => {})[0]!.label).toBe('Zoom: 131%')
    expect(zoomMenuItems(5, () => {})[0]!.label).toBe('Zoom: 249%')
  })
  it('move the zoom as they say', () => {
    const zoom = vi.fn()
    const [, reset, zin, zout] = zoomMenuItems(2, zoom)
    const click = (i: MenuItemConstructorOptions | undefined) => (i!.click as () => void)()
    click(reset)
    click(zin)
    click(zout)
    expect(zoom.mock.calls).toEqual([['reset'], ['in'], ['out']])
  })
})

describe('buildMenu', () => {
  const deps = (zoomLevel: number) => ({ settings: () => {}, openInBrowser: () => {}, restart: () => {}, showLog: () => {}, zoom: () => {}, zoomLevel, dev: false })
  const view = (t: MenuItemConstructorOptions[]) => (t.find((i) => i.label === 'View')!.submenu as MenuItemConstructorOptions[]).map((i) => i.label ?? i.role ?? i.type)
  it("puts the zoom items in View, with the level it was built at, and leaves Window's Zoom as it was", () => {
    const t = buildMenu(deps(1)) as unknown as MenuItemConstructorOptions[]
    expect(view(t)).toEqual(['reload', 'separator', 'Zoom: 120%', 'Reset Zoom to 100%', 'Zoom In', 'Zoom Out', 'separator', 'togglefullscreen'])
    expect(view(buildMenu(deps(-0.5)) as unknown as MenuItemConstructorOptions[])).toContain('Zoom: 91%')
    expect(view(t)).not.toContain('Actual Size')
    const win = t.find((i) => i.role === 'window')!.submenu as MenuItemConstructorOptions[]
    expect(win.map((i) => i.role)).toContain('zoom')
  })
})
