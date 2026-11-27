import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { MORE_SEP, moreLine } from './moreOptions'

// The fold named More options (MoreOptions.vue). The app's vitest runs in node with no component mounted, so the closed line is
// tested through the function it is made by, and the component's source for its marks and its kept body; the fold in a page is
// the Launch dialog's and the trial form's specs'.

describe('moreLine', () => {
  it('says nothing when nothing was changed', () => {
    expect(moreLine([])).toBe('')
    expect(moreLine([{ words: 'opus', changed: false }, { words: '--add-dir ./shared', changed: false }])).toBe('')
    expect(moreLine([null, undefined, false, '', '   '])).toBe('')
  })

  it('names only what was changed, in order', () => {
    expect(moreLine([{ words: '"api fix"', changed: true }, { words: 'sonnet', changed: false }, { words: '--add-dir ./shared', changed: true }])).toBe(`"api fix"${MORE_SEP}--add-dir ./shared`)
    expect(moreLine(['opus', false, '--verbose'])).toBe('opus · --verbose')
  })

  it('says each part once, trimmed', () => {
    expect(moreLine(['  opus ', 'opus', { words: 'opus', changed: true }])).toBe('opus')
  })
})

describe('MoreOptions.vue', () => {
  const source = readFileSync(new URL('../components/MoreOptions.vue', import.meta.url), 'utf8')

  it('draws its three marks and says its state', () => {
    for (const mark of ['data-more-options', 'data-more-line', 'data-more']) expect(source).toMatch(new RegExp(`\\s${mark}[\\s>]`))
    expect(source).toContain(':aria-expanded="open"')
    expect(source).toContain(':aria-controls="bodyId"')
  })

  it('keeps its body mounted while closed, and its line only while closed', () => {
    expect(source).toContain(':unmount-on-hide="false"')
    expect(source).toMatch(/v-if="!open && \(\$slots\.line \|\| props\.line\)"/)
  })
})
