import { Menu, app, type MenuItemConstructorOptions } from 'electron'
import { zoomPercent, type ZoomMove } from './zoom'

export interface MenuDeps {
  settings: () => void
  openInBrowser: () => void
  restart: () => void
  showLog: () => void
  /** Zooms the workbench (the app keeps the level); the keys are taken before the page sees them, see zoom.ts. */
  zoom: (move: ZoomMove) => void
  /** The workbench's zoom level now, which the View menu shows: the menu is built again after each move. */
  zoomLevel: number
  dev: boolean
}

/**
 * zoomMenuItems are the View menu's zoom items at level: the level as a percent, on a line of its own that does nothing, then the
 * reset, named for what it does, and the moves. Custom items, not Electron's zoom roles, whose Ctrl+Plus needs Shift (zoom.ts).
 */
export function zoomMenuItems(level: number, zoom: (move: ZoomMove) => void): MenuItemConstructorOptions[] {
  return [
    { label: `Zoom: ${zoomPercent(level)}%`, enabled: false },
    { label: 'Reset Zoom to 100%', accelerator: 'CmdOrCtrl+0', click: () => zoom('reset') },
    { label: 'Zoom In', accelerator: 'CmdOrCtrl+=', click: () => zoom('in') },
    { label: 'Zoom Out', accelerator: 'CmdOrCtrl+-', click: () => zoom('out') },
  ]
}

/** buildMenu is the app menu: Conductor (settings, quit), Server (open in browser, restart, log), Edit, View, Window, Help. */
export function buildMenu(d: MenuDeps): Menu {
  const mac = process.platform === 'darwin'
  const template: MenuItemConstructorOptions[] = [
    ...(mac ? [{ role: 'appMenu' as const, submenu: [{ role: 'about' as const }, { type: 'separator' as const }, { label: 'Settings…', accelerator: 'CmdOrCtrl+,', click: d.settings }, { type: 'separator' as const }, { role: 'hide' as const }, { role: 'quit' as const }] }] : []),
    {
      label: 'Server',
      submenu: [
        ...(mac ? [] : [{ label: 'Settings…', accelerator: 'CmdOrCtrl+,', click: d.settings }, { type: 'separator' as const }]),
        { label: 'Open in browser', click: d.openInBrowser },
        { label: 'Restart server', click: d.restart },
        { label: 'Server log', click: d.showLog },
        ...(mac ? [] : [{ type: 'separator' as const }, { role: 'quit' as const }]),
      ],
    },
    { role: 'editMenu' },
    { label: 'View', submenu: [{ role: 'reload' }, ...(d.dev ? [{ role: 'toggleDevTools' as const }] : []), { type: 'separator' }, ...zoomMenuItems(d.zoomLevel, d.zoom), { type: 'separator' }, { role: 'togglefullscreen' }] },
    // Minimize and Zoom (and Front on a Mac), never Close: Electron's windowMenu role binds Close to Ctrl+W on Windows and
    // Linux, which the editor takes for its tab (#312).
    { role: 'window', submenu: [{ role: 'minimize' }, { role: 'zoom' }, ...(mac ? [{ type: 'separator' as const }, { role: 'front' as const }] : [])] },
    { role: 'help', submenu: [{ label: `Conductor ${app.getVersion()}`, enabled: false }] },
  ]
  return Menu.buildFromTemplate(template)
}
