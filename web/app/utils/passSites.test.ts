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

/** Every source that mentions pattern, in any form (a call, a reference, an import, a comment), with how many times. */
const mentions = (pattern: RegExp) =>
  Object.fromEntries(
    Object.entries(sources)
      .map(([path, text]) => [path.replace(/^\.\.\//, ''), (text.match(new RegExp(pattern.source, 'g')) ?? []).length] as const)
      .filter(([, n]) => n > 0)
      .sort(([a], [b]) => a.localeCompare(b)),
  )

describe('the pass store', () => {
  it('is read by the fragment plugin and useDecide alone', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(100)
    expect(using(/\buseIdentityPass\(/)).toEqual(['composables/useDecide.ts', 'composables/useIdentityPass.ts', 'plugins/token-fragment.client.ts'])
  })
  it('is named, in any form, by these sources alone, so a new reader has to be named here', () => {
    expect(mentions(/\buseIdentityPass\b/)).toEqual({
      './identityPass.ts': 1,
      'composables/useDecide.ts': 1,
      'composables/useIdentityPass.ts': 1,
      'middleware/decide.global.ts': 1,
      'plugins/token-fragment.client.ts': 1,
    })
    expect(mentions(/\bpassHeld\b/)).toEqual({ 'composables/useIdentityPass.ts': 2, 'middleware/decide.global.ts': 2 })
  })
  it("is the one new reader of the tab's session storage: the sources that held a key there before it are the only others", () => {
    expect(mentions(/sessionStorage/)).toEqual({ 'composables/useIdentityPass.ts': 2, 'pages/index.vue': 1 })
  })
  it('is asked only whether it holds a pass, by the decide middleware', () => {
    expect(using(/\bpassHeld\b/)).toEqual(['composables/useIdentityPass.ts', 'middleware/decide.global.ts'])
  })
  it("has its key named by itself alone, and the workbench token's key is not among what it touches", () => {
    expect(using(/conductor\.identityPass/)).toEqual(['composables/useIdentityPass.ts'])
    expect(sources['../composables/useIdentityPass.ts']).not.toMatch(/workbenchToken|localStorage/)
  })
})
