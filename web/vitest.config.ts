import { availableParallelism } from 'node:os'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  // Nuxt's `~` (the app's directory), so a composable that imports by it can be tested with its auto-imports stubbed.
  resolve: { alias: { '~': fileURLToPath(new URL('./app', import.meta.url)) } },
  test: {
    include: ['app/**/*.test.ts', 'build/**/*.test.ts'],
    environment: 'node',
    // Vitest's own count (a worker for each core but one), at most 16: every worker's modules are transformed by the one main
    // process, and on a 64-core machine 63 workers made a whole run slower (46 to 54 s against 38 s at 16, 37 s at 24, 44 s at
    // 8). A runner of 17 cores or fewer keeps vitest's count (CI's four cores: three workers).
    maxWorkers: Math.max(1, Math.min(availableParallelism() - 1, 16)),
  },
})
