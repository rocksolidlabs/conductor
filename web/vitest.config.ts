import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  // Nuxt's `~` (the app's directory), so a composable that imports by it can be tested with its auto-imports stubbed.
  resolve: { alias: { '~': fileURLToPath(new URL('./app', import.meta.url)) } },
  test: {
    include: ['app/**/*.test.ts', 'build/**/*.test.ts'],
    environment: 'node',
  },
})
