/**
 * What the dev server takes from its environment when a tree of Conductor's own repository serves it (conductor.manifest.json): the
 * tree's Conductor server to proxy to (CONDUCTOR_DEV_API) and the workbench's preview bridge to load (CONDUCTOR_PREVIEW_BRIDGE). Each
 * is read here and held to the loopback; neither reaches the built app (nuxt.config.ts puts them in the dev server's configuration
 * alone).
 */

/** The dev server's API target when none is named: a Conductor server on its default port. */
export const DEFAULT_DEV_API = '127.0.0.1:8080'

/** A loopback host and a port, as CONDUCTOR_DEV_API names the server: 127.0.0.1, localhost or [::1], and 1 to 65535. */
const LOOPBACK_HOST_PORT = /^(127\.0\.0\.1|localhost|\[::1\]):([0-9]{1,5})$/

/**
 * devApi is the host and port the dev server proxies /api and /ws to: CONDUCTOR_DEV_API when set, DEFAULT_DEV_API when unset or empty.
 * A value that is not a loopback host and port is refused (thrown): the proxy carries the workbench token to it.
 */
export function devApi(value: string | undefined): string {
  if (!value) return DEFAULT_DEV_API
  const m = LOOPBACK_HOST_PORT.exec(value)
  const port = m ? Number(m[2]) : 0
  if (!m || port < 1 || port > 65535 || m[2]!.startsWith('0')) {
    throw new Error('CONDUCTOR_DEV_API must be a loopback host and port (127.0.0.1:<port>, localhost:<port> or [::1]:<port>)')
  }
  return value
}

/**
 * devPreviewBridgeSrc is the bridge the dev build loads (devPreviewBridgeHead): CONDUCTOR_PREVIEW_BRIDGE when it is the bridge of a
 * Conductor server on the loopback (http, a loopback host, /preview-bridge.js, nothing after), else '' and none is loaded.
 */
export function devPreviewBridgeSrc(value: string | undefined): string {
  if (!value) return ''
  try {
    const u = new URL(value)
    const loopback = u.hostname === '127.0.0.1' || u.hostname === 'localhost' || u.hostname === '[::1]'
    const exact = u.href === value && !u.username && !u.password && !u.search && !u.hash
    return u.protocol === 'http:' && loopback && exact && u.pathname === '/preview-bridge.js' ? value : ''
  } catch {
    return ''
  }
}

/**
 * devPreviewBridgeHead is the dev build's document head's scripts (nuxt.config.ts's `$development` alone): one tag for the bridge
 * CONDUCTOR_PREVIEW_BRIDGE names, as devPreviewBridgeSrc holds it, `async`, so it loads with the page and runs before the frame's
 * load, not once the app has started; none for anything else. The built app's head never has it.
 */
export function devPreviewBridgeHead(value: string | undefined): Array<{ src: string; async: true; 'data-conductor-preview-bridge': '' }> {
  const src = devPreviewBridgeSrc(value)
  return src ? [{ src, async: true, 'data-conductor-preview-bridge': '' }] : []
}
