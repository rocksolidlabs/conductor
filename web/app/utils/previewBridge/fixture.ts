import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { expect } from 'vitest'

/**
 * A fixture of the bridge's own writing that internal/proto's tests read (internal/api/testdata), for the bridge's tests alone: it
 * is written with CONDUCTOR_WRITE_FIXTURES=1, as the Go generators write theirs, and otherwise the committed file must be the bytes
 * the test made, so a change of what the bridge writes fails here until the fixture is written again and committed.
 */
export function fixture(name: string, text: string) {
  const path = fileURLToPath(new URL(`../../../../internal/api/testdata/${name}`, import.meta.url))
  if (process.env.CONDUCTOR_WRITE_FIXTURES === '1') writeFileSync(path, text)
  const committed = existsSync(path) ? readFileSync(path, 'utf8') : ''
  expect(committed === text, `internal/api/testdata/${name} is not what the bridge writes: write it again with CONDUCTOR_WRITE_FIXTURES=1`).toBe(true)
}
