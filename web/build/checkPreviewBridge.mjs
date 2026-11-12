// node web/build/checkPreviewBridge.mjs <preview-bridge.js> <preview-bridge-served.json>
//
// make web-build's check of the built bridge (build/previewBridge.ts), which web/build/previewBridge.test.ts runs too: the file
// compiles as one classic script inside the wrapper GET /preview-bridge.js serves it in (the wrapper's three parts are those
// internal/api's TestTheBridgeIsServedWhole writes from the route). A missing or an empty file, a module's import or export, or
// any syntax error fails it, with exit 1 and the reason.
import { readFileSync } from 'node:fs'
import { Script } from 'node:vm'

const [file, served] = process.argv.slice(2)
try {
  const [head, tail, end] = JSON.parse(readFileSync(served, 'utf8')).wrapper
  const code = readFileSync(file, 'utf8')
  if (!code.trim()) throw new Error('empty')
  new Script(head + code + tail + '[]' + end, { filename: file })
} catch (e) {
  console.error(`${file}: ${e instanceof Error ? e.message : String(e)}`)
  process.exit(1)
}
