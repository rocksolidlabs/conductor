import { startBridge } from './previewBridge/bridge'
import { originsOf } from './previewBridge/gate'

/**
 * The preview bridge (docs/protocol.md, Preview bridge): one classic script a previewed page loads (or the desktop shell puts in
 * it), which reads the page for the workbench that frames it (previewBridge/bridge.ts). Built by Vite's library build
 * (build/previewBridge.ts) into the app's output as preview-bridge.js; GET /preview-bridge.js serves its bytes unchanged inside a
 * function whose one parameter, conductorWorkbenchOrigins, is the server's workbench origins. The bridge reads its origins from
 * that name alone (run without the wrapper, the name is a global of the page's if the page has one; else the bridge has no origin
 * and answers no one).
 */
declare const conductorWorkbenchOrigins: unknown

startBridge(window, originsOf(typeof conductorWorkbenchOrigins === 'undefined' ? undefined : conductorWorkbenchOrigins))
