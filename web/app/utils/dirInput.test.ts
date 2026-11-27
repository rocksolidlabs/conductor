import { describe, expect, it } from 'vitest'
import { caretToEnd, DIR_DEBOUNCE_MS, dirQuery, enterKeepsText, GIT_CAN_WORKTREE, gitCheckLine, gitMark, homeExpanded, matchingEntries, moreNote, movesHighlight } from './dirInput'

describe('dirInput', () => {
  it('a chosen folder leaves the field focused, the caret at the end and the field scrolled to its end (#167)', () => {
    const calls: string[] = []
    const input = {
      value: '/home/nater/go/src/github.com/rocksolidlabs/conductor',
      scrollLeft: 0,
      scrollWidth: 640,
      focus: () => calls.push('focus'),
      setSelectionRange: (a: number, b: number) => calls.push(`select ${a}-${b}`),
    }
    caretToEnd(input)
    expect(calls).toEqual(['focus', `select ${input.value.length}-${input.value.length}`])
    expect(input.scrollLeft).toBe(640)
  })

  it('sends the text trimmed, and empty for the server default', () => {
    expect(dirQuery('  /srv/work/a ')).toBe('/srv/work/a')
    expect(dirQuery('   ')).toBe('')
  })

  it('marks a repository with a commit, warns on one without, says nothing otherwise', () => {
    expect(gitMark({ repo: true, commits: true })).toEqual({ label: 'git', tone: 'success' })
    expect(gitMark({ repo: true, commits: false })).toEqual({ label: 'git, no commit', tone: 'warning' })
    expect(gitMark({ repo: false, commits: false })).toEqual({ label: '', tone: 'neutral' })
  })

  it("explains the launch with worktrees in the server's words, and only informs without them", () => {
    const ok = { inRepo: true, toplevel: '/srv/work', hasCommit: true, message: GIT_CAN_WORKTREE }
    const fresh = { inRepo: true, toplevel: '/srv/work', hasCommit: false, message: 'the working directory is a git repository without a commit: a worktree needs one to branch from' }
    const plain = { inRepo: false, hasCommit: false, message: 'the working directory is not in a git repository' }
    expect(gitCheckLine(null, 'worktree')).toEqual({ text: '', tone: 'neutral', blocks: false })
    expect(gitCheckLine(ok, 'worktree')).toEqual({ text: GIT_CAN_WORKTREE, tone: 'success', blocks: false })
    expect(gitCheckLine(fresh, 'worktree')).toEqual({ text: `${fresh.message}. Launch would be refused.`, tone: 'warning', blocks: true })
    expect(gitCheckLine(plain, 'worktree')).toEqual({ text: `${plain.message}. Launch would be refused.`, tone: 'warning', blocks: true })
    expect(gitCheckLine(plain, 'none')).toEqual({ text: 'Not a git repository; fine with a shared working directory.', tone: 'neutral', blocks: false })
    expect(gitCheckLine(ok, 'none')).toEqual({ text: 'Git repository at /srv/work.', tone: 'neutral', blocks: false })
    expect(gitCheckLine({ ...plain, error: true, message: 'working directory is outside the allowed roots' }, 'none')).toEqual({ text: 'working directory is outside the allowed roots', tone: 'warning', blocks: false })
  })

  it('takes the message as the verdict: a repository with a commit can still be refused', () => {
    const linked = { inRepo: true, toplevel: '/srv/work', hasCommit: true, message: '/srv/work/.conductor in the working directory is a symbolic link: worktrees must stay inside the working directory' }
    expect(gitCheckLine(linked, 'worktree')).toEqual({ text: `${linked.message}. Launch would be refused.`, tone: 'warning', blocks: true })
    expect(gitCheckLine(linked, 'none')).toEqual({ text: 'Git repository at /srv/work.', tone: 'neutral', blocks: false })
  })

  it('falls back on the booleans without a message, and names no top it was not given', () => {
    expect(gitCheckLine({ inRepo: true, hasCommit: true, message: '' }, 'worktree')).toEqual({ text: 'A git repository with a commit: worktrees can be made.', tone: 'success', blocks: false })
    expect(gitCheckLine({ inRepo: true, hasCommit: false, message: '' }, 'worktree')).toEqual({ text: 'Not a git repository with a commit. Launch would be refused.', tone: 'warning', blocks: true })
    expect(gitCheckLine({ inRepo: true, hasCommit: true, message: GIT_CAN_WORKTREE }, 'none')).toEqual({ text: 'In a git repository.', tone: 'neutral', blocks: false })
  })

  it('keeps, until the new listing, only the entries under what is typed', () => {
    const listed = [
      { name: 'api', path: '/srv/work/api' },
      { name: 'app', path: '/srv/work/app' },
    ]
    expect(matchingEntries(listed, '/srv/work/a')).toEqual(listed)
    expect(matchingEntries(listed, ' /srv/work/ap ')).toEqual(listed)
    expect(matchingEntries(listed, '/srv/work/app')).toEqual([listed[1]])
    expect(matchingEntries(listed, '/srv/work/ax')).toEqual([])
    expect(matchingEntries(listed, '/srv/work/api/')).toEqual([])
    expect(matchingEntries(listed, '/srv/work/bi')).toEqual([])
    expect(matchingEntries(listed, 'work/a')).toEqual([])
    expect(matchingEntries(listed, '  ')).toEqual([])
  })

  it('keeps only direct children of what is typed: shortening across a slash drops the deeper entries', () => {
    const children = [{ name: 'pkg', path: '/srv/work/api/pkg' }]
    expect(matchingEntries(children, '/srv/work/api/')).toEqual(children)
    expect(matchingEntries(children, '/srv/work/api/p')).toEqual(children)
    expect(matchingEntries(children, '/srv/work/api')).toEqual([])
    expect(matchingEntries(children, '/srv/work/ap')).toEqual([])
    expect(matchingEntries(children, '/srv/work/a')).toEqual([])
    expect(matchingEntries([{ name: '.hidden', path: '/srv/work/.hidden' }], '/srv/work/.')).toEqual([{ name: '.hidden', path: '/srv/work/.hidden' }])
  })

  it('debounces for longer than a keystroke and shorter than a pause', () => {
    expect(DIR_DEBOUNCE_MS).toBeGreaterThanOrEqual(100)
    expect(DIR_DEBOUNCE_MS).toBeLessThanOrEqual(300)
  })
})

describe('movesHighlight', () => {
  it('is the keys the field hands to the list to move its highlight', () => {
    for (const key of ['ArrowDown', 'ArrowUp', 'Home', 'End']) expect(movesHighlight(key), key).toBe(true)
    for (const key of ['Enter', 'Tab', 'Escape', 'a', '/', 'Backspace', 'ArrowLeft', 'ArrowRight']) expect(movesHighlight(key), key).toBe(false)
  })
})

describe('enterKeepsText', () => {
  const open = { open: true, highlighted: true, moved: false }
  it('keeps the text over the highlight reka puts on the first entry after a keystroke', () => {
    expect(enterKeepsText({}, open)).toBe(true)
  })
  it('lets the list pick once the highlight was moved', () => {
    expect(enterKeepsText({}, { ...open, moved: true })).toBe(false)
  })
  it('leaves Enter alone with the list closed or nothing highlighted: the form around has it', () => {
    expect(enterKeepsText({}, { ...open, open: false })).toBe(false)
    expect(enterKeepsText({}, { ...open, highlighted: false })).toBe(false)
  })
  it('leaves a modified Enter and Enter while composing alone, as the list does', () => {
    for (const e of [{ ctrlKey: true }, { metaKey: true }, { altKey: true }, { isComposing: true }]) expect(enterKeepsText(e, open), JSON.stringify(e)).toBe(false)
  })
})

describe('moreNote', () => {
  it('says how many folders were left out and that typing narrows the list', () => {
    expect(moreNote({ truncated: true, more: 30 })).toBe('30 more folders here: type the start of a name to narrow the list.')
    expect(moreNote({ truncated: true, more: 1 })).toBe('1 more folder here: type the start of a name to narrow the list.')
    expect(moreNote({ truncated: true, more: 12, moreUnknown: true })).toBe('12+ more folders here: type the start of a name to narrow the list.')
  })
  it('says more were not read, or only that marks were cut, and nothing for a whole listing', () => {
    expect(moreNote({ truncated: true, moreUnknown: true })).toBe('More folders here than were read: type the start of a name to narrow the list.')
    expect(moreNote({ truncated: true })).toBe('Some folders are not marked: git did not answer in time.')
    expect(moreNote({ truncated: false, more: 3 })).toBe(null)
  })
})

describe('a leading ~/ typed', () => {
  it("is the server's home, as a shell reads it", () => {
    expect(homeExpanded('~/', '/home/ada')).toBe('/home/ada/')
    expect(homeExpanded('~/code/shop', '/home/ada')).toBe('/home/ada/code/shop')
    expect(homeExpanded('~/code', '/home/ada/')).toBe('/home/ada/code')
  })
  it('leaves every other text alone, and all of it while the home is not known', () => {
    expect(homeExpanded('~/code', '')).toBe('~/code')
    expect(homeExpanded('~', '/home/ada')).toBe('~')
    expect(homeExpanded('~ada/code', '/home/ada')).toBe('~ada/code')
    expect(homeExpanded('/srv/~/x', '/home/ada')).toBe('/srv/~/x')
  })
})
