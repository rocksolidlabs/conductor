// The desktop tests' pretest: Electron's own installer (node_modules/electron/install.js,
// which downloads the binary through @electron/get into its cache unless it is there
// already) run up to six times with a growing pause, so that a release mirror answering
// 5xx for a few minutes fails one try, not the job (#281). Plain Node, no dependency.
import { spawnSync } from 'node:child_process'
import { setTimeout as sleep } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

const installer = fileURLToPath(new URL('../node_modules/electron/install.js', import.meta.url))
const tries = 6
const pauses = []
for (let n = 1; ; n++) {
  const run = spawnSync(process.execPath, [installer], { stdio: 'inherit' })
  if (run.status === 0) process.exit(0)
  const why = run.error ? run.error.message : run.signal ? `signal ${run.signal}` : `exit status ${run.status}`
  if (n === tries) {
    console.error(`install-electron: ${installer} failed ${tries} times, the last with ${why}, after pauses of ${pauses.join(', ')} s; its cache is electron_config_cache (${process.env.electron_config_cache ?? "@electron/get's default"})`)
    process.exit(1)
  }
  const pause = 15 * n
  pauses.push(pause)
  console.error(`install-electron: try ${n} of ${tries} failed with ${why}; trying again in ${pause} s`)
  await sleep(pause * 1000)
}
