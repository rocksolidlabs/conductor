import { copyFileSync, existsSync, mkdirSync, rmSync } from 'node:fs'
import { stopServer } from './server'
import { readState } from './state'

/**
 * Stops the server by its pid, keeps its log as test-results/server.log (CI uploads test-results: a spec that failed against
 * the shared server has the server's side of it to read), and removes the scratch root; CONDUCTOR_E2E_KEEP=1 keeps the root too.
 */
export default async function globalTeardown(): Promise<void> {
  const state = readState()
  await stopServer(state.pid)
  if (state.log && existsSync(state.log)) {
    mkdirSync('test-results', { recursive: true })
    copyFileSync(state.log, 'test-results/server.log')
  }
  if (process.env.CONDUCTOR_E2E_KEEP === '1') {
    console.log(`conductor e2e: kept ${state.root}`)
    return
  }
  rmSync(state.root, { recursive: true, force: true })
}
