import { makeBridge, type NativeLayer } from './bridge'

// The bridge's script (mobile/www/shell/bridge.js): loaded by the bundle's index.html before a provider module and the workbench,
// it builds `window.conductorShell` once from the plugins the shell's native runtime puts in the page before its scripts
// (`Capacitor.Plugins`), or with none in a browser. A page that has one already keeps it.

/** The native runtime's global, as much of it as the bridge reads. */
interface NativeRuntime {
  getPlatform?: () => string
  isNativePlatform?: () => boolean
  Plugins?: Record<string, unknown>
}

/** The plugins the shell registers and uses, by their registered names, when the runtime is native and has them all. */
function nativeLayer(rt: NativeRuntime | undefined): NativeLayer | null {
  if (!rt || typeof rt.isNativePlatform !== 'function' || !rt.isNativePlatform()) return null
  const platform = rt.getPlatform?.()
  const p = rt.Plugins ?? {}
  const key = p.ConductorKey, store = p.ConductorStore, push = p.PushNotifications, app = p.App, browser = p.Browser, http = p.CapacitorHttp
  if ((platform !== 'ios' && platform !== 'android') || !key || !store || !push || !app || !browser || !http) return null
  return { platform, key, store, push, app, browser, http } as NativeLayer
}

const g = globalThis as unknown as { conductorShell?: unknown; Capacitor?: NativeRuntime; document?: Document; open?: (u: string, t: string, f: string) => unknown }
if (!g.conductorShell) {
  const { bridge, closeRegistration } = makeBridge(nativeLayer(g.Capacitor), {
    open: (url) => void g.open?.(url, '_blank', 'noopener'),
    console,
  })
  Object.defineProperty(g, 'conductorShell', { value: bridge, writable: false, configurable: false, enumerable: true })
  g.document?.addEventListener('DOMContentLoaded', closeRegistration, { once: true })
}
