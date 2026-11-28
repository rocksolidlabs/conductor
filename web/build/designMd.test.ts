import { describe, expect, it } from 'vitest'
import { anchor, designMd, designSources, nuxtUiCorners, ruleHeadings, sectionHeadings, tagline } from './designMd'

// DESIGN.md at the root is what designMd() makes from the repository's files. Vitest writes a file snapshot only when asked
// (`make design-md`, which runs this file with -u) or when the file is missing on a machine that is not CI; in CI a stale DESIGN.md
// fails here with the difference.

const sources = designSources()

describe('DESIGN.md', () => {
  it('is what the tokens, the brand and the rules make (run `make design-md` after changing them)', async () => {
    await expect(designMd(sources)).toMatchFileSnapshot('../../DESIGN.md')
  })

  it("follows a token's change in the files it is made from", () => {
    const changed = { ...sources, tokensCss: sources.tokensCss.replace('--conductor-forest: #263D35;', '--conductor-forest: #123456;') }
    expect(changed.tokensCss).not.toBe(sources.tokensCss)
    expect(designMd(changed)).toContain('  forest: "#123456"')
  })

  it('names a rule of docs/design/system.md once it is added, and fails when a rule it points at is gone', () => {
    const added = sources.systemMd.replace('## The rules\n', '## The rules\n\n### A rule of a test\n\nDescribed, not decided.\n')
    expect(designMd({ ...sources, systemMd: added })).toContain('- [A rule of a test](docs/design/system.md#a-rule-of-a-test)')
    const gone = sources.systemMd.replace('### Status\n', '### Statuses\n')
    expect(() => designMd({ ...sources, systemMd: gone })).toThrow('no rule "Status"')
  })

  it('refers only to tokens it declares', () => {
    const md = designMd(sources)
    const front = md.split('\n---\n')[0]!
    const declared = new Set<string>()
    let group = ''
    for (const line of front.split('\n')) {
      const top = line.match(/^(\w+):\s*$/)
      if (top) group = top[1]!
      const key = line.match(/^ {2}"?([\w-]+)"?:/)
      if (key && group) declared.add(`${group}.${key[1]}`)
    }
    const refs = [...front.matchAll(/\{([\w.-]+)\}/g)].map((m) => m[1]!)
    expect(refs.length).toBeGreaterThan(10)
    expect(refs.filter((r) => !declared.has(r))).toEqual([])
  })

  it('gives the primary button the corner the app draws: the step of Nuxt UI\'s button class, from --ui-radius', () => {
    // Read here on its own, not through the generator: the class the button's base draws, and that step's factor of --ui-radius.
    const base = sources.nuxtUiShared.match(/const button = \(options\) => \(\{\s*slots: \{\s*base: \[\s*"([^"]*)"/)![1]!
    const cls = base.match(/rounded-([\w-]+)/)![1]!
    const factor = Number(sources.nuxtUiShared.match(new RegExp(`--radius-${cls}:\\s*calc\\(var\\(--ui-radius\\)\\s*\\*\\s*([\\d.]+)\\)`))![1])
    const uiRadius = Number(sources.resolved.dark.vars['--ui-radius']!.replace('rem', '')) * 16
    const md = designMd(sources)
    const front = md.split('\n---\n')[0]!
    for (const theme of ['dark', 'light']) {
      const ref = front.match(new RegExp(`  button-primary-${theme}:[\\s\\S]*?rounded: "\\{rounded\\.([\\w-]+)\\}"`))![1]!
      const corner = front.match(new RegExp(`^  "?${ref}"?: "([\\d.]+)px"$`, 'm'))![1]!
      expect(Number(corner)).toBe(uiRadius * factor)
    }
    expect(cls).toBe('md')
    expect(uiRadius * factor).toBe(6) // the app's button corner today: rounded-md, 1.5 x 0.25rem
  })

  it("follows Nuxt UI's button class and its steps", () => {
    const shared = sources.nuxtUiShared.replace(/(const button = \(options\) => \(\{\s*slots: \{\s*base: \[\s*")rounded-md/, '$1rounded-lg')
    expect(shared).not.toBe(sources.nuxtUiShared)
    expect(nuxtUiCorners(shared).button).toBe('lg')
    const md = designMd({ ...sources, nuxtUiShared: shared })
    expect(md).toContain('    rounded: "{rounded.lg}"')
    expect(md).toContain('  "lg": "8px"')
    expect(() => nuxtUiCorners(shared.replace(/--radius-md:[^;]*;/, ''))).toThrow('--radius-md')
  })

  it("takes the tagline and the brand's sections from docs/design/brand.md, and fails when a section it points at is gone", () => {
    expect(tagline(sources.brandMd)).toBe('Engineering intent, orchestrated.')
    const retold = sources.brandMd.replace('**Conductor — Engineering intent, orchestrated.**', '**Conductor — A tagline of a test.**')
    expect(designMd({ ...sources, brandMd: retold })).toContain('description: "A tagline of a test."')
    expect(sectionHeadings(sources.brandMd)).toContain('Palette and semantic colors')
    for (const h of ['Palette and semantic colors', 'Typography and layout']) {
      const gone = sources.brandMd.replace(`## ${h}\n`, `## ${h}s\n`)
      expect(gone).not.toBe(sources.brandMd)
      expect(() => designMd({ ...sources, brandMd: gone })).toThrow(`no section "${h}" in docs/design/brand.md`)
    }
    expect(() => designMd({ ...sources, brandMd: retold.replace('**Conductor — A tagline of a test.**', '') })).toThrow('no line **Conductor')
  })

  it("makes a rule's anchor as GitHub does", () => {
    expect(anchor('What a pull request cannot do')).toBe('what-a-pull-request-cannot-do')
    expect(anchor("Fields that take an agent's words")).toBe('fields-that-take-an-agents-words')
    expect(ruleHeadings('# x\n\n## The rules\n\n### One\n\ntext\n\n### Two\n\n## After\n\n### Not a rule\n')).toEqual(['One', 'Two'])
  })
})
