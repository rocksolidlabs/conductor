import { describe, expect, it } from 'vitest'
import { anchor, designMd, designSources, ruleHeadings } from './designMd'

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

  it("makes a rule's anchor as GitHub does", () => {
    expect(anchor('What a pull request cannot do')).toBe('what-a-pull-request-cannot-do')
    expect(anchor("Fields that take an agent's words")).toBe('fields-that-take-an-agents-words')
    expect(ruleHeadings('# x\n\n## The rules\n\n### One\n\ntext\n\n### Two\n\n## After\n\n### Not a rule\n')).toEqual(['One', 'Two'])
  })
})
