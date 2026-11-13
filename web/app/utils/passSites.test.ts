import { describe, expect, it } from 'vitest'

// Who reads the pass (docs/plans/decide-grant.md, I4): the app's sources, read as text. Only the fragment plugin, which sets it, and
// useDecide, which sends it, use the pass store; the decide middleware asks whether one is held (`passHeld`) and nothing else; no
// other source names the store's key. A composable is an auto-import, so a use is found by its name, with or without an import.

const sources = import.meta.glob(['../**/*.ts', '../**/*.vue', '!../**/*.test.ts'], { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const using = (pattern: RegExp) =>
  Object.entries(sources)
    .filter(([, text]) => pattern.test(text))
    .map(([path]) => path.replace(/^\.\.\//, ''))
    .sort()

describe('the pass store', () => {
  it('is read by the fragment plugin and useDecide alone', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(100)
    expect(using(/\buseIdentityPass\(/)).toEqual(['composables/useDecide.ts', 'composables/useIdentityPass.ts', 'plugins/token-fragment.client.ts'])
  })
  it('is asked only whether it holds a pass, by the decide middleware', () => {
    expect(using(/\bpassHeld\b/)).toEqual(['composables/useIdentityPass.ts', 'middleware/decide.global.ts'])
  })
  it("has its key named by itself alone, and the workbench token's key is not among what it touches", () => {
    expect(using(/conductor\.identityPass/)).toEqual(['composables/useIdentityPass.ts'])
    expect(sources['../composables/useIdentityPass.ts']).not.toMatch(/workbenchToken|localStorage/)
  })
})
