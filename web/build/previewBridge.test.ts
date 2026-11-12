import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { FakeDoc, FakeWin, h } from '../app/utils/previewBridge/fakeDom'
import { PREVIEW_MARK, parsePreviewMessage } from '../app/utils/protocol'
import { buildPreviewBridge, devPreviewBridge, wrapPreviewBridge } from './previewBridge'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
const WORKBENCH = 'http://127.0.0.1:8080'
const NONCE = 'n0nce-of-the-hello-1234'
const SERVED = fileURLToPath(new URL('../../internal/api/testdata/preview-bridge-served.json', import.meta.url))

/** make web-build's check (checkPreviewBridge.mjs) run on each text given (null: no file), whether it passed. */
function checked(texts: Array<string | null>): boolean[] {
  const dir = mkdtempSync(join(tmpdir(), 'preview-bridge-check-'))
  try {
    return texts.map((text, i) => {
      const file = join(dir, `${i}.js`)
      if (text !== null) writeFileSync(file, text)
      return spawnSync(process.execPath, [fileURLToPath(new URL('./checkPreviewBridge.mjs', import.meta.url)), file, SERVED]).status === 0
    })
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

/** text run as a page runs a classic script, its window given; a module's import or export is a syntax error here. */
function run(text: string, win: FakeWin) {
  new Function('window', text)(win)
}

describe('the built preview bridge', () => {
  it('is one classic script that reads its origins from the wrapper the server serves it in, and answers the workbench there', async () => {
    const code = await buildPreviewBridge(webRoot)
    expect(code).not.toMatch(/^\s*(import|export)\b/m)
    expect(code).not.toContain('__CONDUCTOR_WORKBENCH_ORIGINS__')
    const page = () => new FakeWin(new FakeDoc(h('html', {}, h('body', {}, h('p', {}, 'Hello')))))
    const served = page()
    run(wrapPreviewBridge(code, [WORKBENCH]), served)
    served.deliver('http://127.0.0.1:5173', { conductor: PREVIEW_MARK, t: 'hello', nonce: NONCE })
    served.deliver(WORKBENCH, { conductor: PREVIEW_MARK, t: 'hello', nonce: NONCE })
    expect(served.posts.map((p) => [p.target, (p.message as { t: string }).t])).toEqual([[WORKBENCH, 'ready']])
    expect(parsePreviewMessage(served.posts[0]!.message, 'workbench', NONCE)).not.toBeNull()
    // Run without the wrapper, or wrapped with no origin, it answers no one and listens for nothing.
    for (const text of [code, wrapPreviewBridge(code, [])]) {
      const bare = page()
      run(text, bare)
      bare.deliver(WORKBENCH, { conductor: PREVIEW_MARK, t: 'hello', nonce: NONCE })
      expect([bare.count, bare.posts]).toEqual([0, []])
    }
    // make web-build's check passes what is built.
    expect(checked([code])).toEqual([true])
  }, 60_000)

  it("fails make web-build's check for a file that is missing, empty or not one classic script in the server's wrapper", () => {
    const refused = [null, '', '  \n\n', 'export { a };', 'import x from "y";', 'export default 1', 'import"./y.js";', "import'./y.js';",
      '  export const a = 1;', 'var u = import.meta.url;', 'await Promise.resolve(1);', 'var a=1;export{a}', 'var a = (;',
      // Refused only inside the wrapper: its parameter declared again.
      'let conductorWorkbenchOrigins = 1;']
    // A classic script, and one that compiles only inside the wrapper's function.
    const accepted = ['(function(){ var a = 1; })();', 'return;']
    expect(checked([...refused, ...accepted])).toEqual([...refused.map(() => false), ...accepted.map(() => true)])
  })

  it("answers in the dev server as the route answers a page on the loopback: its headers, its wrapper and the loopback's names", () => {
    // What internal/api's TestTheBridgeIsServedWhole wrote of the route's answer to a listener on 127.0.0.1:8080.
    const served = JSON.parse(readFileSync(fileURLToPath(new URL('../../internal/api/testdata/preview-bridge-served.json', import.meta.url)), 'utf8')) as {
      wrapper: [string, string, string]
      loopback: { listen: string; script: string; headers: Record<string, string>; served: string }
    }
    const { listen, script, headers, served: body } = served.loopback
    expect(devPreviewBridge(script, listen)).toEqual({ headers, body })
    const port = listen.split(':')[1]
    for (const host of [`localhost:${port}`, `[::1]:${port}`]) expect(devPreviewBridge(script, host).body).toBe(body)
    expect(wrapPreviewBridge(script, [])).toBe(served.wrapper[0] + script + served.wrapper[1] + '[]' + served.wrapper[2])
    for (const host of ['example.com:8080', '192.168.1.5:8080', '127.0.0.1:99999', '']) expect(devPreviewBridge(script, host).body).toBe(wrapPreviewBridge(script, []))
  })
})
